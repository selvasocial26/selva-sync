// Package engine compares local folders with what was uploaded before and
// sends only new or changed files to Google Drive.
package engine

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/selvasync/selvasync/internal/config"
	"github.com/selvasync/selvasync/internal/drive"
)

// ArchiveFolderName is the Drive folder that keeps deleted and old files.
const ArchiveFolderName = "Archive (Selva Sync)"

const archiveKey = "_archive"

// How often Drive itself is re-read to catch files deleted there, and how
// often local contents are compared by checksum.
const (
	fullCheckEvery = 7 * 24 * time.Hour
	deepCheckEvery = 30 * 24 * time.Hour
	deepAutoMax    = 50 << 20 // automatic checksum check only for files up to 50 MB
	deepManualMax  = 2 << 30  // "Check Drive copy now": up to 2 GB
)

// Hooks connect the engine to the operating system.
type Hooks struct {
	Metered  func() bool
	WifiSSID func() (ssid string, known bool)
	SkipAttr func(fs.FileInfo) bool
	Notify   func(key string, args map[string]string)
	// MachineID identifies this computer + Windows user (a hash).
	MachineID string
	// InstallID identifies this installation of Selva Sync (random).
	InstallID string
	// LegacyMachineID is the identity used by version 1.0 (to find its folder).
	LegacyMachineID string
	// VolumeID returns the serial number of the disk holding path, or "".
	VolumeID func(path string) string
	// OnBattery reports that a laptop is running on battery.
	OnBattery func() bool
	// ReportText writes the monthly report (month "2026-10").
	ReportText func(month string, st config.MonthStats, set config.Settings) string
}

// Status is shown live in the app window.
type Status struct {
	Running    bool      `json:"running"`
	DryRun     bool      `json:"dryRun"`
	Phase      string    `json:"phase"`
	FilesTotal int       `json:"filesTotal"`
	FilesDone  int       `json:"filesDone"`
	BytesTotal int64     `json:"bytesTotal"`
	BytesDone  int64     `json:"bytesDone"`
	Current    string    `json:"current"`
	StartedAt  time.Time `json:"startedAt"`
	SpeedBps   int64     `json:"speedBps"`
}

// PlanItem is one planned change, shown in the preview.
type PlanItem struct {
	Op   string `json:"op"` // new, update, move, archive, trash, archive_dir, trash_dir
	Path string `json:"path"`
	From string `json:"from,omitempty"`
	Size int64  `json:"size"`
}

// Preview is the result of a dry run.
type Preview struct {
	Time      time.Time  `json:"time"`
	New       int        `json:"new"`
	Update    int        `json:"update"`
	Move      int        `json:"move"`
	Remove    int        `json:"remove"`
	Bytes     int64      `json:"bytes"`
	Items     []PlanItem `json:"items"`
	Truncated bool       `json:"truncated"`
	Missing   []string   `json:"missing"`
	Complete  bool       `json:"complete"`
}

// Engine runs syncs. Only one sync runs at a time.
type Engine struct {
	Store    *config.Store
	Drive    *drive.Client
	Settings func() config.Settings
	Hooks    Hooks
	Log      *Logger

	smu      sync.Mutex // guards state
	state    *config.State
	dirty    atomic.Bool
	saveMu   sync.Mutex
	folderMu sync.Mutex // serialises folder creation so no duplicate folders appear

	gen      atomic.Uint64 // changes when Drive knowledge is reset (sign-out etc.)
	runGen   uint64
	inRun    atomic.Bool
	legacyOK bool
	stamp    string // state file version we last loaded or saved (guarded by smu)

	mu      sync.Mutex
	paused  int // while > 0 no sync may start (sign-in / sign-out in progress)
	status  Status
	cancel  context.CancelFunc
	done    chan struct{}
	preview *Preview

	bytesNow    atomic.Int64
	usageDelta  atomic.Int64 // bytes uploaded this run not yet added to state.Usage
	capReserved atomic.Int64
	verified    sync.Map // folder keys checked on Drive during this run
	runErrMu    sync.Mutex
	runErrs     []string
	skipDirs    sync.Map // folders not to archive this run (a move out of them failed)
	mismatch    sync.Map // prefix -> path of sources skipped because the disk changed
	speedAt     time.Time
	speedB      int64
}

// New creates an engine and loads saved state.
func New(store *config.Store, d *drive.Client, settings func() config.Settings, hooks Hooks, log *Logger) *Engine {
	st := store.LoadState()
	return &Engine{Store: store, Drive: d, Settings: settings, Hooks: hooks, Log: log, state: st, stamp: store.StateStamp(), legacyOK: st.MigratedFromV1}
}

// ErrBusy is returned when a sync is already running.
var ErrBusy = errors.New("a sync is already running")

var (
	errCap   = errors.New("monthly data limit reached")
	errStale = errors.New("account changed during sync")
	// ErrLocation means the chosen backup folder is missing or not writable.
	ErrLocation = errors.New("backup folder not available")
	// ErrLocationInside means the chosen folder is inside a Selva Sync backup
	// (of this or another computer), where it would be archived or mixed up.
	ErrLocationInside = errors.New("backup folder is inside a Selva Sync backup")
)

// Options for one run.
type Options struct {
	DryRun    bool
	Force     bool // ignore Wi-Fi / metered rules (user pressed "Sync anyway")
	FullCheck bool // re-read the Drive copy and compare checksums
	Trigger   string
	OnDone    func(config.RunSummary)
}

// Start launches a sync in the background.
func (e *Engine) Start(opt Options) error {
	e.mu.Lock()
	if e.status.Running || e.paused > 0 {
		e.mu.Unlock()
		return ErrBusy
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.done = make(chan struct{})
	done := e.done
	e.status = Status{Running: true, DryRun: opt.DryRun, Phase: "prepare", StartedAt: time.Now()}
	if opt.DryRun {
		e.preview = nil
	}
	e.bytesNow.Store(0)
	e.speedAt, e.speedB = time.Now(), 0
	e.mu.Unlock()
	go func() {
		defer close(done)
		defer cancel()
		sum := e.Run(ctx, opt)
		if opt.OnDone != nil {
			opt.OnDone(sum)
		}
	}()
	return nil
}

// Pause stops the running sync and prevents new ones until Resume. Pauses
// can overlap (each Pause needs its own Resume).
func (e *Engine) Pause(wait time.Duration) {
	e.mu.Lock()
	e.paused++
	e.mu.Unlock()
	e.StopAndWait(wait)
}

// Resume undoes one Pause.
func (e *Engine) Resume() {
	e.mu.Lock()
	if e.paused > 0 {
		e.paused--
	}
	e.mu.Unlock()
}

// Paused reports whether syncing is paused.
func (e *Engine) Paused() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.paused > 0
}

// Stop cancels the running sync.
func (e *Engine) Stop() {
	e.mu.Lock()
	if e.cancel != nil {
		e.cancel()
	}
	e.mu.Unlock()
}

// StopAndWait cancels the running sync and waits until it has finished.
func (e *Engine) StopAndWait(timeout time.Duration) {
	e.mu.Lock()
	if e.cancel != nil {
		e.cancel()
	}
	done := e.done
	e.mu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

// Status returns the current progress.
func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.status
	s.BytesDone = e.bytesNow.Load()
	if s.Running {
		now := time.Now()
		if dt := now.Sub(e.speedAt); dt >= 2*time.Second {
			e.status.SpeedBps = int64(float64(s.BytesDone-e.speedB) / dt.Seconds())
			e.speedAt, e.speedB = now, s.BytesDone
		}
		s.SpeedBps = e.status.SpeedBps
	}
	return s
}

// LastPreview returns the most recent complete dry-run result.
func (e *Engine) LastPreview() *Preview {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.preview
}

// StateInfo returns the summary parts of the state.
func (e *Engine) StateInfo() (last *config.RunSummary, lastSuccess, lastScheduled time.Time, computerID string, monthUsage int64) {
	e.smu.Lock()
	defer e.smu.Unlock()
	if e.state.LastRun != nil {
		c := *e.state.LastRun
		last = &c
	}
	return last, e.state.LastSuccess, e.state.LastScheduled, e.state.ComputerID,
		e.state.Usage[time.Now().Format("2006-01")] + e.usageDelta.Load()
}

// MarkScheduled records that the scheduler started a run at t.
func (e *Engine) MarkScheduled(t time.Time) {
	e.smu.Lock()
	e.state.LastScheduled = t
	e.smu.Unlock()
	e.markDirty()
	e.saveState()
}

// FailStreak returns how many runs in a row have failed.
func (e *Engine) FailStreak() int {
	e.smu.Lock()
	defer e.smu.Unlock()
	return e.state.FailStreak
}

// FixClock resets saved times that are in the future (the PC clock was
// wrong and has been corrected), so schedules keep working.
func (e *Engine) FixClock(now time.Time) {
	now = now.Round(0) // compare wall clocks only
	e.smu.Lock()
	changed := false
	for _, t := range []*time.Time{&e.state.LastScheduled, &e.state.LastAttempt, &e.state.LastSuccess,
		&e.state.LastArchiveTrim, &e.state.LastFullCheck, &e.state.LastDeepCheck, &e.state.LastAlert,
		&e.state.FirstAttempt, &e.state.LastPartial} {
		if t.Round(0).After(now.Add(5 * time.Minute)) {
			*t = now
			changed = true
		}
	}
	e.smu.Unlock()
	if changed {
		e.markDirty()
		e.saveState()
	}
}

// AlertDue reports (once a day) that an overdue reminder may be shown.
func (e *Engine) AlertDue(now time.Time) bool {
	e.smu.Lock()
	defer e.smu.Unlock()
	if d := now.Sub(e.state.LastAlert.Round(0)); d >= 0 && d < 23*time.Hour {
		return false
	}
	e.state.LastAlert = now
	e.dirty.Store(true)
	return true
}

// SaveNow writes the state to disk immediately (used at Windows shutdown).
func (e *Engine) SaveNow() { e.markDirty(); e.saveState() }

// LastAttempt returns when the last real (non-preview) sync started.
func (e *Engine) LastAttempt() time.Time {
	e.smu.Lock()
	defer e.smu.Unlock()
	return e.state.LastAttempt
}

// ForgetRemote clears Drive knowledge (after sign-out / account change).
// Call StopAndWait first; anything a still-running sync reports afterwards
// is ignored.
func (e *Engine) ForgetRemote() {
	e.smu.Lock()
	e.gen.Add(1)
	e.state.ResetRemote()
	e.smu.Unlock()
	e.markDirty()
	e.saveState()
}

func (e *Engine) markDirty() { e.dirty.Store(true) }

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// MismatchedSources lists selected folders skipped because their disk
// changed (e.g. another USB stick got the same drive letter).
func (e *Engine) MismatchedSources() []string {
	var out []string
	e.mismatch.Range(func(_, v any) bool { out = append(out, v.(string)); return true })
	sort.Strings(out)
	return out
}

// AcceptDisk tells Selva Sync that the disk now holding path is the right
// one (the user replaced the disk), so syncing continues with it.
func (e *Engine) AcceptDisk(path string) {
	prefix := RemotePrefix(path)
	e.smu.Lock()
	delete(e.state.SourceVolumes, prefix)
	e.smu.Unlock()
	e.mismatch.Delete(prefix)
	e.markDirty()
	e.saveState()
}

// saveState writes the state if something changed.
func (e *Engine) saveState() {
	e.saveMu.Lock()
	defer e.saveMu.Unlock()
	if !e.dirty.Swap(false) {
		return
	}
	e.smu.Lock()
	if !e.inRun.Load() {
		if stamp := e.Store.StateStamp(); stamp != "" && e.stamp != "" && stamp != e.stamp {
			// Another copy of the app synced meanwhile: its file list is newer.
			// Keep it and only carry over this copy's own small settings.
			disk := e.Store.LoadState()
			disk.LastScheduled = maxTime(disk.LastScheduled, e.state.LastScheduled)
			e.state = disk
		}
	}
	if d := e.usageDelta.Swap(0); d != 0 {
		e.state.Usage[time.Now().Format("2006-01")] += d
	}
	err := e.Store.SaveState(e.state)
	if err == nil {
		e.stamp = e.Store.StateStamp()
	}
	e.smu.Unlock()
	if err != nil {
		e.dirty.Store(true) // try again next time
	}
}

// stale reports that the account was changed after this run started.
func (e *Engine) stale() bool { return e.gen.Load() != e.runGen }

func (e *Engine) setPhase(p string) {
	e.mu.Lock()
	e.status.Phase = p
	e.mu.Unlock()
}

// Run performs one sync synchronously.
func (e *Engine) Run(ctx context.Context, opt Options) config.RunSummary {
	sum := config.RunSummary{Start: time.Now(), DryRun: opt.DryRun}
	set := e.Settings()

	// Only one sync per data folder, even across two copies of the app. A
	// blocked run changes nothing at all, not even the saved state.
	unlock, ok := e.Store.TryLock("sync.lock")
	if !ok {
		sum.End = time.Now()
		sum.Result, sum.Reason = "blocked", "other_instance"
		e.Log.Warn("blocked", map[string]string{"reason": "other_instance"})
		e.mu.Lock()
		e.status.Running, e.status.Phase = false, "done"
		e.mu.Unlock()
		return sum
	}
	defer unlock()
	e.inRun.Store(true)
	defer e.inRun.Store(false)
	// Start from what is saved on disk (another copy may have synced).
	e.saveState()
	e.smu.Lock()
	if stamp := e.Store.StateStamp(); stamp != "" && e.stamp != "" && stamp != e.stamp && !e.dirty.Load() {
		disk := e.Store.LoadState()
		disk.LastScheduled = maxTime(disk.LastScheduled, e.state.LastScheduled)
		e.state = disk
		e.stamp = stamp
	}
	e.smu.Unlock()

	e.runGen = e.gen.Load()
	e.verified = sync.Map{}
	e.runErrMu.Lock()
	e.runErrs = nil
	e.runErrMu.Unlock()
	e.skipDirs = sync.Map{}
	markedRunning := false
	reindexIncomplete := false
	defer func() {
		sum.End = time.Now()
		e.mu.Lock()
		e.status.Running = false
		e.status.Phase = "done"
		e.status.Current = ""
		e.mu.Unlock()
		if !opt.DryRun {
			e.smu.Lock()
			s := sum
			e.state.LastRun = &s
			if markedRunning && !reindexIncomplete {
				e.state.RunInProgress = false
			}
			switch sum.Result {
			case "ok", "nothing", "partial", "cap":
				e.state.FailStreak = 0
			case "failed":
				e.state.FailStreak++
			}
			if sum.Result == "ok" || sum.Result == "nothing" {
				e.state.LastSuccess = sum.Start
			}
			if (sum.Result == "partial" || sum.Result == "cap" || sum.Result == "blocked") && sum.Uploaded+sum.Updated > 0 {
				e.state.LastPartial = sum.Start
			}
			if sum.Result != "blocked" || sum.Uploaded+sum.Updated+sum.Moved+sum.Archived > 0 {
				e.addMonthStats(sum)
			}
			e.smu.Unlock()
			e.markDirty()
			e.saveState()
			e.Log.Add(levelFor(sum.Result), "run_end", map[string]string{
				"result": sum.Result, "uploaded": itoa(sum.Uploaded), "updated": itoa(sum.Updated),
				"archived": itoa(sum.Archived), "failed": itoa(sum.Failed), "bytes": HumanBytes(sum.Bytes),
				"moved": itoa(sum.Moved),
			})
			e.notify(sum, set)
		}
	}()

	if opt.DryRun {
		e.Log.Info("preview_start", nil)
	} else {
		mode := opt.Trigger
		if mode == "" {
			mode = "manual"
		}
		e.Log.Info("run_start", map[string]string{"mode": mode})
		e.smu.Lock()
		e.state.LastAttempt = sum.Start
		if e.state.FirstAttempt.IsZero() {
			e.state.FirstAttempt = sum.Start
		}
		e.smu.Unlock()
	}

	if !opt.DryRun && !opt.Force {
		if reason := e.networkBlocked(ctx, set); reason != "" {
			sum.Result, sum.Reason = "blocked", reason
			e.Log.Warn("blocked", map[string]string{"reason": reason})
			return sum
		}
	}

	e.setPhase("connect")
	if err := e.ensureRoot(ctx, set); err != nil {
		e.fail(&sum, err)
		return sum
	}
	if err := e.dropTrashed(ctx); err != nil {
		e.fail(&sum, err)
		return sum
	}
	e.smu.Lock()
	// After a crash or power cut the saved list may miss files that were
	// already uploaded; re-reading Drive prevents uploading them twice.
	unclean := e.state.RunInProgress
	needCheck := opt.FullCheck || unclean || len(e.state.Files) == 0 && e.state.ComputerID != "" ||
		!opt.DryRun && time.Since(e.state.LastFullCheck.Round(0)) > fullCheckEvery
	deep := opt.FullCheck || !opt.DryRun && !e.state.LastDeepCheck.IsZero() && time.Since(e.state.LastDeepCheck.Round(0)) > deepCheckEvery
	if e.state.LastDeepCheck.IsZero() {
		e.state.LastDeepCheck = time.Now()
	}
	if !opt.DryRun {
		if !e.state.RunInProgress {
			e.state.RunSince = sum.Start // kept until a run finishes properly
		}
		e.state.RunInProgress = true
		markedRunning = true
	}
	e.smu.Unlock()
	e.markDirty()
	e.saveState()
	if needCheck || deep {
		e.setPhase("check")
		e.Log.Info("full_check", nil)
		trustAfter := time.Time{}
		if unclean {
			e.smu.Lock()
			trustAfter = e.state.RunSince.Add(-time.Minute)
			e.smu.Unlock()
		}
		reindexIncomplete = true // until it finishes, the next run checks again
		if err := e.reindex(ctx, trustAfter); err != nil {
			e.fail(&sum, err)
			return sum
		}
		reindexIncomplete = false
	}
	if e.stale() {
		sum.Result = "stopped"
		return sum
	}

	e.setPhase("scan")
	deepMax := int64(0)
	if deep {
		deepMax = deepAutoMax
		if opt.FullCheck {
			deepMax = deepManualMax
		}
	}
	plan, missing, skippedBig := e.scan(ctx, set, deepMax)
	sum.Skipped += skippedBig
	if ctx.Err() != nil {
		sum.Result = "stopped"
		return sum
	}
	if deep && !opt.DryRun {
		e.smu.Lock()
		e.state.LastDeepCheck = time.Now()
		e.smu.Unlock()
		e.markDirty()
	}

	if opt.DryRun {
		e.buildPreview(plan, missing)
		sum.Result = "ok"
		return sum
	}

	if len(plan) == 0 {
		sum.Result = "nothing"
		e.Log.Info("nothing", nil)
		e.trimArchive(ctx, set)
		e.writeReport(ctx, set)
		return sum
	}

	e.execute(ctx, set, plan, &sum)
	if sum.Result == "" {
		switch {
		case sum.Failed > 0:
			sum.Result = "partial"
		case sum.Reason == "cap":
			sum.Result = "cap"
		default:
			sum.Result = "ok"
		}
		e.trimArchive(ctx, set)
		e.writeReport(ctx, set)
	}
	return sum
}

// addMonthStats adds a finished run to this month's report totals.
// Caller holds smu.
func (e *Engine) addMonthStats(sum config.RunSummary) {
	m := sum.Start.Format("2006-01")
	st := e.state.Monthly[m]
	if st == nil {
		st = &config.MonthStats{}
		e.state.Monthly[m] = st
	}
	st.Runs++
	switch sum.Result {
	case "ok", "nothing":
		st.OK++
	case "failed":
		st.Failed++
		st.Errors = append(st.Errors, sum.Start.Format("2006-01-02 15:04")+"  sync failed: "+sum.Reason)
	default:
		st.Problems++
	}
	st.Uploaded += sum.Uploaded
	st.Updated += sum.Updated
	st.Moved += sum.Moved
	st.Archived += sum.Archived
	st.Bytes += sum.Bytes
	e.runErrMu.Lock()
	st.Errors = append(st.Errors, e.runErrs...)
	e.runErrMu.Unlock()
	if len(st.Errors) > 50 {
		st.Errors = st.Errors[len(st.Errors)-50:]
	}
	// keep about a year of totals
	cutoff := sum.Start.AddDate(-1, -1, 0).Format("2006-01")
	for k := range e.state.Monthly {
		if k < cutoff {
			delete(e.state.Monthly, k)
		}
	}
	for k := range e.state.ReportDone {
		if k < cutoff {
			delete(e.state.ReportDone, k)
		}
	}
}

// writeReport puts last month's report file into this computer's backup
// folder, once per month.
func (e *Engine) writeReport(ctx context.Context, set config.Settings) {
	if !set.Reports || e.Hooks.ReportText == nil {
		return
	}
	now := time.Now()
	// first day of this month, minus one month (AddDate on the 31st would skip a month)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local).AddDate(0, -1, 0).Format("2006-01")
	e.smu.Lock()
	st := e.state.Monthly[month]
	done := e.state.ReportDone[month]
	compID := e.state.ComputerID
	var copyStats config.MonthStats
	if st != nil {
		copyStats = *st
		copyStats.Errors = append([]string(nil), st.Errors...)
	}
	e.smu.Unlock()
	if st == nil || done || compID == "" {
		return
	}
	text := e.Hooks.ReportText(month, copyStats, set)
	tmp := filepath.Join(e.Store.Dir, "report.tmp")
	if os.WriteFile(tmp, []byte(text), 0o600) != nil {
		return
	}
	defer os.Remove(tmp)
	name := "Selva Sync report " + month + ".txt"
	existing, _ := e.Drive.FindChild(ctx, compID, name, false)
	sp := drive.UploadSpec{ParentID: compID, Name: name, Path: tmp, Size: int64(len(text)), ModTime: time.Now()}
	if existing != nil {
		sp.ExistingID = existing.ID
	}
	if _, err := e.Drive.Upload(ctx, sp); err == nil {
		e.commit(func() { e.state.ReportDone[month] = true })
		e.Log.Info("report_written", map[string]string{"name": name})
	}
}

func levelFor(result string) string {
	switch result {
	case "ok", "nothing":
		return "info"
	case "failed":
		return "error"
	}
	return "warn"
}

func itoa(n int) string { return strconv.Itoa(n) }

func (e *Engine) notify(sum config.RunSummary, set config.Settings) {
	if e.Hooks.Notify == nil || !set.Notifications {
		return
	}
	args := map[string]string{
		"uploaded": itoa(sum.Uploaded + sum.Updated), "archived": itoa(sum.Archived),
		"failed": itoa(sum.Failed), "bytes": HumanBytes(sum.Bytes), "reason": sum.Reason,
	}
	switch sum.Result {
	case "ok":
		e.Hooks.Notify("notify_ok", args)
	case "partial":
		e.Hooks.Notify("notify_partial", args)
	case "cap":
		e.Hooks.Notify("notify_cap", args)
	case "failed":
		streak := e.FailStreak()
		if sum.Reason == "signin" || sum.Reason == "drive_full" || sum.Reason == "location_missing" || sum.Reason == "location_inside" {
			if streak > 1 {
				return // already told the user about this problem
			}
		} else if streak != 2 {
			// a single network failure (e.g. just after waking up, before
			// Wi-Fi connects) is retried quietly; tell the user only once
			// if it keeps failing
			return
		}
		switch sum.Reason {
		case "signin":
			e.Hooks.Notify("notify_signin", args)
		case "drive_full":
			e.Hooks.Notify("notify_full", args)
		case "location_missing", "location_inside":
			e.Hooks.Notify("notify_location", args)
		default:
			e.Hooks.Notify("notify_failed", args)
		}
	}
}

func (e *Engine) fail(sum *config.RunSummary, err error) {
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(err, errStale):
		sum.Result = "stopped"
		return
	case drive.IsAuth(err) || errors.Is(err, ErrNotSignedIn):
		sum.Result, sum.Reason = "failed", "signin"
		e.Log.Error("signin_needed", nil)
	case drive.IsQuota(err):
		sum.Result, sum.Reason = "failed", "drive_full"
		e.Log.Error("drive_full", nil)
	case errors.Is(err, ErrLocation):
		sum.Result, sum.Reason = "failed", "location_missing"
		e.Log.Error("location_missing", nil)
	case errors.Is(err, ErrLocationInside):
		sum.Result, sum.Reason = "failed", "location_inside"
		e.Log.Error("location_inside", nil)
	default:
		sum.Result, sum.Reason = "failed", "network"
		e.Log.Error("error", map[string]string{"error": shortErr(err)})
	}
}

// ErrNotSignedIn is returned by the token source when no account is linked.
var ErrNotSignedIn = errors.New("not signed in")

func shortErr(err error) string {
	s := err.Error()
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

func (e *Engine) networkBlocked(ctx context.Context, set config.Settings) string {
	if e.Hooks.WifiSSID != nil && (len(set.BlockedWifi) > 0 || len(set.AllowedWifi) > 0) {
		ssid, known := e.Hooks.WifiSSID()
		if !known && len(set.AllowedWifi) > 0 {
			return "wifi_unknown"
		}
		if ssid != "" {
			for _, b := range set.BlockedWifi {
				if strings.EqualFold(b, ssid) {
					return "wifi_blocked"
				}
			}
			if len(set.AllowedWifi) > 0 {
				ok := false
				for _, a := range set.AllowedWifi {
					if strings.EqualFold(a, ssid) {
						ok = true
					}
				}
				if !ok {
					return "wifi_not_allowed"
				}
			}
		}
	}
	if set.OnlyOnAC && e.Hooks.OnBattery != nil && e.Hooks.OnBattery() {
		return "on_battery"
	}
	if ctx.Err() != nil {
		return ""
	}
	if set.PauseOnMetered && e.Hooks.Metered != nil && e.Hooks.Metered() {
		return "metered"
	}
	return ""
}

// ---------- Drive folders ----------

func (e *Engine) ensureRoot(ctx context.Context, set config.Settings) error {
	// The whole backup lives in ONE folder. If the user chose a folder (in
	// My Drive or a shared drive), use only that one; a different folder
	// means starting fresh there.
	loc := set.Location.FolderID
	e.Drive.SetSharedDrive(set.Location.DriveID)
	e.smu.Lock()
	if e.state.LocationID != loc {
		e.state.ResetForLocation()
		e.state.LocationID = loc
		e.dirty.Store(true)
	}
	rootID, compID := e.state.RootID, e.state.ComputerID
	appliedRoot, appliedComp := e.state.AppliedRoot, e.state.AppliedComputer
	e.smu.Unlock()

	if loc != "" {
		f, err := e.Drive.Get(ctx, loc)
		if drive.IsNotFound(err) || drive.IsForbidden(err) || err == nil && (f.Trashed || !f.IsFolder() ||
			f.Capabilities != nil && !f.Capabilities.CanAddChildren) {
			// never fall back to another place: stop and tell the user
			return ErrLocation
		}
		if err != nil {
			return err
		}
		if err := e.checkNotInsideBackup(ctx, f); err != nil {
			return err
		}
		rootID, appliedRoot = loc, set.DriveFolderName
		if !e.commit(func() { e.state.RootID, e.state.AppliedRoot = rootID, appliedRoot }) {
			return errStale
		}
		return e.ensureComputer(ctx, set, rootID, compID, appliedComp)
	}

	// main "Selva Sync" folder
	if rootID != "" {
		f, err := e.Drive.Get(ctx, rootID)
		if drive.IsNotFound(err) || err == nil && f.Trashed {
			e.resetRemoteKeepGen()
			rootID, compID, appliedRoot, appliedComp = "", "", "", ""
		} else if err != nil {
			return err
		}
	}
	if rootID == "" {
		found, err := e.Drive.FindByAppProperty(ctx, "selvasync", "root")
		if err != nil {
			return err
		}
		if len(found) > 0 {
			rootID, appliedRoot = found[0].ID, found[0].Name
		} else {
			rootID, err = e.Drive.CreateFolder(ctx, "", set.DriveFolderName, map[string]string{"selvasync": "root"})
			if err != nil {
				return err
			}
			appliedRoot = set.DriveFolderName
		}
	}
	if appliedRoot != set.DriveFolderName {
		if err := e.Drive.Rename(ctx, rootID, set.DriveFolderName); err == nil {
			appliedRoot = set.DriveFolderName
		}
	}
	if !e.commit(func() { e.state.RootID, e.state.AppliedRoot = rootID, appliedRoot }) {
		return errStale
	}
	return e.ensureComputer(ctx, set, rootID, compID, appliedComp)
}

func hasParent(f *drive.File, id string) bool {
	for _, p := range f.Parents {
		if p == id {
			return true
		}
	}
	return false
}

// checkNotInsideBackup refuses a chosen folder that is (inside) a computer
// folder made by Selva Sync: the owner PC would archive it as "deleted".
func (e *Engine) checkNotInsideBackup(ctx context.Context, f *drive.File) error {
	cur := f
	for depth := 0; depth < 30 && cur != nil; depth++ {
		if cur.AppProperties["selvasync"] == "computer" {
			return ErrLocationInside
		}
		if len(cur.Parents) == 0 {
			return nil
		}
		next, err := e.Drive.Get(ctx, cur.Parents[0])
		if drive.IsNotFound(err) || drive.IsForbidden(err) {
			return nil // not made by Selva Sync (it could not be seen otherwise)
		}
		if err != nil {
			return err
		}
		cur = next
	}
	return nil
}

// CheckLocation tells whether a folder can be used as the backup folder:
// nil, ErrLocation or ErrLocationInside (or a network error).
func (e *Engine) CheckLocation(ctx context.Context, f *drive.File) error {
	if f.Trashed || !f.IsFolder() || f.Capabilities != nil && !f.Capabilities.CanAddChildren {
		return ErrLocation
	}
	return e.checkNotInsideBackup(ctx, f)
}

// AlertTimes returns the times the overdue reminder is based on.
func (e *Engine) AlertTimes() (lastSuccess, lastPartial, firstAttempt time.Time) {
	e.smu.Lock()
	defer e.smu.Unlock()
	lastPartial = e.state.LastPartial
	if lr := e.state.LastRun; lr != nil && (lr.Result == "partial" || lr.Result == "cap") && lr.Uploaded+lr.Updated > 0 && lr.Start.After(lastPartial) {
		lastPartial = lr.Start
	}
	return e.state.LastSuccess, lastPartial, e.state.FirstAttempt
}

// ensureComputer finds or creates this computer's folder inside rootID.
func (e *Engine) ensureComputer(ctx context.Context, set config.Settings, rootID, compID, appliedComp string) error {
	e.smu.Lock()
	appliedRoot := e.state.AppliedRoot
	e.smu.Unlock()
	// This computer's folder is found by its tags, never by name alone, so
	// two PCs with the same name never share (and archive) each other's files.
	if compID != "" {
		f, err := e.Drive.Get(ctx, compID)
		// gone, or moved out of the backup folder (never write outside it)
		if drive.IsNotFound(err) || err == nil && (f.Trashed || !hasParent(f, rootID)) {
			e.smu.Lock()
			e.state.ResetForLocation()
			e.state.RootID, e.state.AppliedRoot = rootID, appliedRoot
			e.smu.Unlock()
			compID, appliedComp = "", ""
		} else if err != nil {
			return err
		}
	}
	if compID == "" {
		machine, install := e.Hooks.MachineID, e.Hooks.InstallID
		pick := func(key, val string) (string, string, error) {
			if val == "" {
				return "", "", nil
			}
			found, err := e.Drive.FindByAppProperty(ctx, key, val)
			if err != nil {
				return "", "", err
			}
			for _, f := range found {
				for _, p := range f.Parents {
					if p == rootID && f.IsFolder() {
						return f.ID, f.Name, nil
					}
				}
			}
			return "", "", nil
		}
		id, name, err := pick("install", install)
		if err != nil {
			return err
		}
		retag := false
		if id == "" {
			// reinstall on the same PC and Windows user
			if id, name, err = pick("machine", machine); err != nil {
				return err
			}
			retag = id != ""
		}
		if id == "" && e.Hooks.LegacyMachineID != "" && e.legacyOK {
			// folder made by version 1.0 on this PC
			if id, name, err = pick("machine", e.Hooks.LegacyMachineID); err != nil {
				return err
			}
			retag = id != ""
		}
		if retag {
			props := map[string]string{}
			if machine != "" {
				props["machine"] = machine
			}
			if install != "" {
				props["install"] = install
			}
			_ = e.Drive.SetProps(ctx, id, props)
		}
		if id == "" && machine == "" && install == "" {
			// no identity available: fall back to matching by name
			f, err := e.Drive.FindChild(ctx, rootID, set.ComputerName, true)
			if err != nil {
				return err
			}
			if f != nil {
				id, name = f.ID, f.Name
			}
		}
		if id != "" {
			compID, appliedComp = id, name
			if strings.HasPrefix(name, set.ComputerName+" (") {
				appliedComp = set.ComputerName // keep the name chosen to avoid a clash
			}
		} else {
			name := set.ComputerName
			other, err := e.Drive.FindChild(ctx, rootID, name, true)
			if err != nil {
				return err
			}
			if other != nil {
				// another computer already uses this name
				suffix := install
				if suffix == "" {
					suffix = machine
				}
				if len(suffix) > 6 {
					suffix = suffix[:6]
				}
				if suffix == "" {
					suffix = strconv.FormatInt(time.Now().Unix()%100000, 10)
				}
				name = name + " (" + suffix + ")"
			}
			props := map[string]string{"selvasync": "computer"}
			if machine != "" {
				props["machine"] = machine
			}
			if install != "" {
				props["install"] = install
			}
			id, err := e.Drive.CreateFolder(ctx, rootID, name, props)
			if err != nil {
				return err
			}
			compID = id
			appliedComp = set.ComputerName
			e.smu.Lock()
			e.state.LastFullCheck = time.Now()
			e.smu.Unlock()
		}
		if !e.commit(func() {
			e.state.ComputerID = compID
			e.state.ArchiveID = ""
		}) {
			return errStale
		}
		base := set.DriveFolderName
		if set.Location.FolderID != "" {
			base = set.Location.Name
		}
		e.Log.Info("folder_ready", map[string]string{"path": base + "/" + set.ComputerName})
	}
	if appliedComp != set.ComputerName {
		if err := e.Drive.Rename(ctx, compID, set.ComputerName); err == nil {
			appliedComp = set.ComputerName
		}
	}
	e.smu.Lock()
	e.state.AppliedComputer = appliedComp
	archiveID := e.state.ArchiveID
	e.smu.Unlock()

	// the archive folder may have been deleted by the user
	if archiveID != "" {
		f, err := e.Drive.Get(ctx, archiveID)
		if drive.IsNotFound(err) || err == nil && f.Trashed {
			e.invalidateFolder(archiveKey)
		} else if err != nil {
			return err
		}
	}
	e.markDirty()
	e.saveState()
	return nil
}

func (e *Engine) resetRemoteKeepGen() {
	e.smu.Lock()
	e.state.ResetRemote()
	e.smu.Unlock()
	e.markDirty()
}

// commit applies a state change unless the account was changed meanwhile.
func (e *Engine) commit(f func()) bool {
	e.smu.Lock()
	defer e.smu.Unlock()
	if e.stale() {
		return false
	}
	f()
	e.dirty.Store(true)
	return true
}

// dropTrashed forgets every item the user put in the Drive trash, so those
// files are uploaded again instead of being written into the trash.
func (e *Engine) dropTrashed(ctx context.Context) error {
	trashed, err := e.Drive.Trashed(ctx)
	if err != nil {
		return err
	}
	if len(trashed) == 0 {
		return nil
	}
	ids := make(map[string]bool, len(trashed))
	for _, f := range trashed {
		ids[f.ID] = true
	}
	n := 0
	e.commit(func() {
		for k, id := range e.state.Folders {
			if ids[id] {
				for fk := range e.state.Folders {
					if fk == k || strings.HasPrefix(fk, k+"/") {
						delete(e.state.Folders, fk)
					}
				}
				for fk, fe := range e.state.Files {
					if strings.HasPrefix(fk, k+"/") {
						delete(e.state.Files, fk)
						delete(e.state.Pending, fk)
						if fe.Seen {
							n++
						}
					}
				}
			}
		}
		if ids[e.state.ArchiveID] {
			e.state.ArchiveID = ""
		}
		for k, fe := range e.state.Files {
			if ids[fe.ID] {
				delete(e.state.Files, k)
				delete(e.state.Pending, k)
				n++
			}
		}
	})
	if n > 0 {
		e.Log.Info("restored_from_trash", map[string]string{"n": itoa(n)})
	}
	return nil
}

// invalidateFolder forgets a cached folder ID (and everything below it)
// after Drive reported it missing.
func (e *Engine) invalidateFolder(key string) {
	e.smu.Lock()
	defer e.smu.Unlock()
	if key == archiveKey {
		e.state.ArchiveID = ""
	}
	for k := range e.state.Folders {
		if k == key || strings.HasPrefix(k, key+"/") {
			delete(e.state.Folders, k)
		}
	}
	e.dirty.Store(true)
}

// ensureFolder returns the Drive ID for a folder key such as
// "D-Drive/Accounts/2026", creating missing folders one level at a time.
func (e *Engine) ensureFolder(ctx context.Context, key string) (string, error) {
	e.folderMu.Lock()
	defer e.folderMu.Unlock()
	id, err := e.ensureFolderLocked(ctx, key)
	if drive.IsNotFound(err) {
		// a cached parent was deleted on Drive: forget the chain and retry once
		for k := key; k != "" && k != "."; k = path.Dir(k) {
			e.invalidateFolder(k)
		}
		id, err = e.ensureFolderLocked(ctx, key)
	}
	return id, err
}

func (e *Engine) ensureFolderLocked(ctx context.Context, key string) (string, error) {
	if e.stale() {
		return "", errStale
	}
	e.smu.Lock()
	if key == "" || key == "." {
		id := e.state.ComputerID
		e.smu.Unlock()
		if id == "" {
			return "", errStale
		}
		return id, nil
	}
	if key == archiveKey && e.state.ArchiveID != "" {
		id := e.state.ArchiveID
		e.smu.Unlock()
		return id, nil
	}
	if id, ok := e.state.Folders[key]; ok {
		e.smu.Unlock()
		return id, nil
	}
	e.smu.Unlock()

	parentKey := path.Dir(key)
	name := path.Base(key)
	if key == archiveKey {
		parentKey, name = "", ArchiveFolderName
	}
	pid, err := e.ensureFolderLocked(ctx, parentKey)
	if err != nil {
		return "", err
	}
	var id string
	f, err := e.Drive.FindChild(ctx, pid, name, true)
	if err != nil {
		return "", err
	}
	if f != nil {
		id = f.ID
		if f.Name != name && strings.EqualFold(f.Name, name) {
			_ = e.Drive.Rename(ctx, id, name) // folder renamed only in upper/lower case
		}
	} else {
		id, err = e.Drive.CreateFolder(ctx, pid, name, nil)
		if err != nil {
			return "", err
		}
	}
	if !e.commit(func() {
		if key == archiveKey {
			e.state.ArchiveID = id
		} else {
			e.state.Folders[key] = id
		}
	}) {
		return "", errStale
	}
	return id, nil
}

// verifyFolder checks once per run that a cached Drive folder still exists
// (the user may have deleted it on Drive) and recreates it if needed.
func (e *Engine) verifyFolder(ctx context.Context, key, id string) (string, error) {
	if key == "" || key == "." {
		return id, nil
	}
	if v, ok := e.verified.Load(key); ok {
		return v.(string), nil
	}
	f, err := e.Drive.Get(ctx, id)
	if err != nil && !drive.IsNotFound(err) {
		return "", err
	}
	if err != nil || f.Trashed {
		for k := key; k != "" && k != "."; k = path.Dir(k) {
			e.invalidateFolder(k)
			e.verified.Delete(k)
		}
		if id, err = e.ensureFolder(ctx, key); err != nil {
			return "", err
		}
	}
	e.verified.Store(key, id)
	return id, nil
}

// reindex rebuilds the list of uploaded files from Drive itself. Used after
// reinstalling or a crash (so nothing is uploaded twice) and for the weekly
// full check. Items the user renamed or moved inside the mirror on Drive are
// put back where they belong.
func (e *Engine) reindex(ctx context.Context, trustAfter time.Time) error {
	return e.reindexPass(ctx, true, trustAfter)
}

// trustAfter: after a crash, files Drive created after the crashed run
// started were uploaded by this installation, so they count as seen.
func (e *Engine) reindexPass(ctx context.Context, allowRelocate bool, trustAfter time.Time) error {
	e.smu.Lock()
	compID := e.state.ComputerID
	oldFileKey := make(map[string]string, len(e.state.Files))
	oldSeen := make(map[string]bool, len(e.state.Files))
	for id := range e.state.KeptSeen {
		oldSeen[id] = true
	}
	for k, fe := range e.state.Files {
		oldFileKey[fe.ID] = k
		oldSeen[fe.ID] = oldSeen[fe.ID] || fe.Seen
	}
	oldFolderKey := make(map[string]string, len(e.state.Folders))
	for k, id := range e.state.Folders {
		if !strings.HasPrefix(k, archiveKey) {
			oldFolderKey[id] = k
		}
	}
	e.smu.Unlock()

	type reloc struct {
		id, want string
		folder   bool
	}
	var relocs []reloc
	type dup struct {
		id, key string
		folder  bool
	}
	var dups []dup
	folders := map[string]string{}
	files := map[string]config.FileEntry{}
	var archiveID string
	var walk func(id, prefix string) error
	walk = func(id, prefix string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		kids, err := e.Drive.Children(ctx, id)
		if err != nil {
			return err
		}
		for _, k := range kids {
			if prefix == "" && k.IsFolder() && k.Name == ArchiveFolderName {
				archiveID = k.ID
				continue
			}
			key := k.Name
			if prefix != "" {
				key = prefix + "/" + k.Name
			}
			if k.IsFolder() {
				if want, ok := oldFolderKey[k.ID]; ok && want != key && allowRelocate {
					relocs = append(relocs, reloc{k.ID, want, true})
					continue
				}
				if _, isDup := folders[key]; isDup {
					if allowRelocate {
						dups = append(dups, dup{k.ID, key, true})
					}
					continue
				}
				folders[key] = k.ID
				if err := walk(k.ID, key); err != nil {
					return err
				}
				continue
			}
			if want, ok := oldFileKey[k.ID]; ok && want != key && allowRelocate {
				relocs = append(relocs, reloc{k.ID, want, false})
				continue
			}
			if old, isDup := files[key]; isDup {
				// two files with the same name: keep the newest in the mirror,
				// move the other one to the archive
				older := k.ID
				if k.ModTimeMs() > old.MTime {
					older = old.ID
					files[key] = config.FileEntry{ID: k.ID, Size: k.SizeInt(), MTime: k.ModTimeMs(), MD5: k.MD5, Seen: oldSeen[k.ID] || old.Seen}
				}
				if allowRelocate {
					dups = append(dups, dup{older, key, false})
				}
				continue
			}
			seen := oldSeen[k.ID] || !trustAfter.IsZero() && !k.Created().IsZero() && !k.Created().Before(trustAfter)
			files[key] = config.FileEntry{ID: k.ID, Size: k.SizeInt(), MTime: k.ModTimeMs(), MD5: k.MD5, Seen: seen}
		}
		return nil
	}
	if err := walk(compID, ""); err != nil {
		return err
	}

	if len(dups) > 0 {
		// Duplicates made on Drive (e.g. a folder restored from the trash
		// next to the one Selva Sync recreated): merge folders, archive the
		// extra copies. Nothing is deleted.
		date := e.Drive.ServerNow().Local().Format("2006-01-02")
		for _, d := range dups {
			if !d.folder {
				if err := e.archiveDriveItem(ctx, d.id, d.key, date); err != nil {
					return err
				}
				continue
			}
			primary := folders[d.key]
			kids, err := e.Drive.Children(ctx, d.id)
			if err != nil {
				return err
			}
			for _, c := range kids {
				other, err := e.Drive.FindChild(ctx, primary, c.Name, c.IsFolder())
				if err != nil {
					return err
				}
				if other == nil {
					if err := e.Drive.Move(ctx, c.ID, primary, ""); err != nil && !drive.IsNotFound(err) {
						return err
					}
					continue
				}
				if err := e.archiveDriveItem(ctx, c.ID, d.key+"/"+c.Name, date); err != nil {
					return err
				}
			}
			if err := e.Drive.Trash(ctx, d.id); err != nil && !drive.IsNotFound(err) {
				return err
			}
			e.Log.Info("relocated", map[string]string{"path": d.key})
		}
		return e.reindexPass(ctx, true, trustAfter)
	}

	if len(relocs) > 0 {
		// folders first, shallowest first, then files
		sort.SliceStable(relocs, func(i, j int) bool {
			if relocs[i].folder != relocs[j].folder {
				return relocs[i].folder
			}
			return strings.Count(relocs[i].want, "/") < strings.Count(relocs[j].want, "/")
		})
		if !e.commit(func() { e.state.Folders = folders }) { // use what really exists while moving back
			return errStale
		}
		for _, r := range relocs {
			pid, err := e.ensureFolder(ctx, path.Dir(r.want))
			if err != nil {
				return err
			}
			if err := e.Drive.Move(ctx, r.id, pid, path.Base(r.want)); err != nil && !drive.IsNotFound(err) {
				return err
			}
			if r.folder {
				e.commit(func() { e.state.Folders[r.want] = r.id })
			}
			e.Log.Info("relocated", map[string]string{"path": r.want})
		}
		return e.reindexPass(ctx, false, trustAfter)
	}

	if !e.commit(func() {
		for k, v := range e.state.Folders {
			if strings.HasPrefix(k, archiveKey+"/") {
				folders[k] = v
			}
		}
		e.state.Folders = folders
		e.state.Files = files
		if archiveID != "" {
			e.state.ArchiveID = archiveID
		}
		e.state.LastFullCheck = time.Now()
	}) {
		return errStale
	}
	e.saveState()
	return nil
}

// ---------- Scanning ----------

type item struct {
	op       string // new, update, move, archive, trash, archive_dir, trash_dir
	key      string
	from     string // move: old key
	local    string
	size     int64
	mtime    int64
	existing config.FileEntry
}

// RemotePrefix turns a local folder into its Drive path, e.g.
// `D:\Accounts\2026` -> "D-Drive/Accounts/2026".
func RemotePrefix(p string) string {
	p = filepath.Clean(p)
	vol := filepath.VolumeName(p)
	rest := strings.TrimPrefix(p, vol)
	var head string
	switch {
	case len(vol) == 2 && vol[1] == ':':
		head = strings.ToUpper(vol[:1]) + "-Drive"
	case strings.HasPrefix(vol, `\\`):
		head = "Network-" + strings.ReplaceAll(strings.Trim(vol, `\`), `\`, "-")
	default:
		head = "Root"
	}
	parts := []string{head}
	for _, s := range strings.FieldsFunc(rest, func(r rune) bool { return r == '/' || r == '\\' }) {
		parts = append(parts, s)
	}
	return strings.Join(parts, "/")
}

// driveRelative returns the path from the top of its drive with "/"
// separators, e.g. `C:\Users\Selva\AppData` -> "Users/Selva/AppData".
func driveRelative(p string) string {
	p = strings.TrimPrefix(p, filepath.VolumeName(p))
	p = filepath.ToSlash(p)
	return strings.Trim(p, "/")
}

func foldCase(s string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(s)
	}
	return s
}

// excluded checks the skip rules:
//   - "/Windows", "/Users/*/AppData": from the top of the drive only
//   - "Projects/old/*": relative to the selected folder
//   - "*.tmp", "node_modules": a file or folder name anywhere
func excluded(patterns []string, name, rel, driveRel string) bool {
	ln := strings.ToLower(name)
	lr := strings.ToLower(rel)
	ld := strings.ToLower(driveRel)
	for _, p := range patterns {
		lp := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(p), `\`, "/"))
		if lp == "" {
			continue
		}
		if strings.HasPrefix(lp, "/") {
			if matchPrefix(strings.Trim(lp, "/"), ld) {
				return true
			}
			continue
		}
		if strings.Contains(lp, "/") {
			lp = strings.Trim(lp, "/")
			if ok, _ := path.Match(lp, lr); ok || lr == lp || strings.HasPrefix(lr, lp+"/") {
				return true
			}
			continue
		}
		if ok, _ := path.Match(lp, ln); ok {
			return true
		}
	}
	return false
}

// matchPrefix reports whether the first segments of p match pattern.
func matchPrefix(pattern, p string) bool {
	ps := strings.Split(pattern, "/")
	segs := strings.Split(p, "/")
	if len(segs) < len(ps) {
		return false
	}
	for i, x := range ps {
		if ok, _ := path.Match(x, segs[i]); !ok {
			return false
		}
	}
	return true
}

func underAny(key string, prefixes []string) bool {
	for _, p := range prefixes {
		if key == p || strings.HasPrefix(key, p+"/") {
			return true
		}
	}
	return false
}

type activeSource struct {
	config.Source
	prefix string
}

// activeSources returns enabled sources, dropping ones nested in another or
// mapping to the same Drive folder as another.
func (e *Engine) activeSources(set config.Settings) []activeSource {
	var srcs []config.Source
	for _, s := range set.Sources {
		if s.Enabled && strings.TrimSpace(s.Path) != "" {
			s.Path = filepath.Clean(s.Path)
			srcs = append(srcs, s)
		}
	}
	sort.SliceStable(srcs, func(i, j int) bool { return len(srcs[i].Path) < len(srcs[j].Path) })
	var out []activeSource
	usedPrefix := map[string]bool{}
	for _, s := range srcs {
		nested := false
		for _, o := range out {
			op := foldCase(o.Path)
			sp := foldCase(s.Path)
			if sp == op || strings.HasPrefix(sp, strings.TrimRight(op, `\/`)+string(filepath.Separator)) {
				nested = true
			}
		}
		if nested {
			e.Log.Warn("source_nested", map[string]string{"path": s.Path})
			continue
		}
		pre := RemotePrefix(s.Path)
		lp := strings.ToLower(pre)
		clash := usedPrefix[lp]
		for u := range usedPrefix {
			if strings.HasPrefix(lp, u+"/") || strings.HasPrefix(u, lp+"/") {
				clash = true
			}
		}
		if clash {
			e.Log.Warn("source_duplicate", map[string]string{"path": s.Path})
			continue
		}
		usedPrefix[lp] = true
		out = append(out, activeSource{s, pre})
	}
	return out
}

// hourShift reports whether two modified times are exactly one hour apart
// (FAT and exFAT USB drives shift times when daylight saving changes).
func hourShift(a, b int64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d >= 3600000-2000 && d <= 3600000+2000
}

func fileMD5(p string, max int64) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, io.LimitReader(f, max+1)); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

type goneFile struct {
	key string
	ent config.FileEntry
}

func (e *Engine) scan(ctx context.Context, set config.Settings, deepMax int64) (plan []*item, missing []string, skippedBig int) {
	maxSize := int64(set.MaxFileSizeMB) << 20
	e.smu.Lock()
	files := make(map[string]config.FileEntry, len(e.state.Files))
	for k, v := range e.state.Files {
		files[k] = v
	}
	folders := make(map[string]string, len(e.state.Folders))
	for k, v := range e.state.Folders {
		folders[k] = v
	}
	// forget disk IDs of folders that are no longer selected
	keep := map[string]bool{}
	for _, s := range set.Sources {
		keep[RemotePrefix(s.Path)] = true
	}
	for k := range e.state.SourceVolumes {
		if !keep[k] {
			delete(e.state.SourceVolumes, k)
		}
	}
	active := map[string]bool{}
	for _, s := range set.Sources {
		if s.Enabled {
			active[RemotePrefix(s.Path)] = true
		}
	}
	e.mismatch.Range(func(k, _ any) bool {
		if !active[k.(string)] {
			e.mismatch.Delete(k)
		}
		return true
	})
	e.smu.Unlock()

	var news []*item
	var gones []goneFile
	var goneDirs []string
	var seenKeys []string
	var mtimeFix []goneFile
	dirOp, fileOp := "archive_dir", "archive"
	if set.DeleteMode == "trash" {
		dirOp, fileOp = "trash_dir", "trash"
	}

	for _, src := range e.activeSources(set) {
		if ctx.Err() != nil {
			return
		}
		st, err := os.Stat(src.Path)
		if err != nil || !st.IsDir() {
			missing = append(missing, src.Path)
			e.Log.Warn("source_missing", map[string]string{"path": src.Path})
			continue
		}
		prefix := src.prefix
		// Walk the real folder behind a junction or link, so a moved folder
		// is still read correctly.
		root := src.Path
		if r, err := filepath.EvalSymlinks(src.Path); err == nil {
			root = r
		}
		// A different disk under the same drive letter (another USB stick)
		// must never be mixed with the first disk's backup.
		if e.Hooks.VolumeID != nil {
			if vid := e.Hooks.VolumeID(src.Path); vid != "" {
				e.smu.Lock()
				known := e.state.SourceVolumes[prefix]
				if known == "" {
					e.state.SourceVolumes[prefix] = vid
					e.dirty.Store(true)
				}
				e.smu.Unlock()
				if known != "" && known != vid {
					missing = append(missing, src.Path)
					e.mismatch.Store(prefix, src.Path)
					e.Log.Warn("source_new_drive", map[string]string{"path": src.Path})
					continue
				}
				e.mismatch.Delete(prefix)
			}
		}
		present := map[string]bool{}
		dirs := map[string]bool{prefix: true}
		var protected []string
		rootFailed := false
		var srcNews []*item

		walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if p == root {
				if err != nil {
					rootFailed = true // cannot list the folder: treat as missing
					return fs.SkipAll
				}
				return nil
			}
			rel, rerr := filepath.Rel(root, p)
			if rerr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			key := prefix + "/" + rel
			if err != nil {
				// unreadable: protect what was uploaded before
				protected = append(protected, key)
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			drel := driveRelative(filepath.Join(src.Path, filepath.FromSlash(rel)))
			if excluded(set.Excludes, d.Name(), rel, drel) || d.Type()&fs.ModeSymlink != 0 {
				protected = append(protected, key)
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			info, ierr := d.Info()
			if ierr != nil {
				protected = append(protected, key)
				return nil
			}
			if e.Hooks.SkipAttr != nil && e.Hooks.SkipAttr(info) {
				protected = append(protected, key)
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				dirs[key] = true
				return nil
			}
			if !info.Mode().IsRegular() {
				protected = append(protected, key)
				return nil
			}
			present[key] = true
			size, mt := info.Size(), info.ModTime().UnixMilli()
			if maxSize > 0 && size > maxSize {
				skippedBig++
				e.Log.Warn("skipped_big", map[string]string{"path": p, "size": HumanBytes(size)})
				return nil
			}
			ent, ok := files[key]
			switch {
			case !ok:
				srcNews = append(srcNews, &item{op: "new", key: key, local: p, size: size, mtime: mt})
			case ent.Size == size && ent.MTime != mt && hourShift(ent.MTime, mt) && ent.MD5 != "" && fileMD5(p, size) == ent.MD5:
				// same content, clock shifted by daylight saving: just remember the new time
				mtimeFix = append(mtimeFix, goneFile{key, config.FileEntry{MTime: mt}})
				if !ent.Seen {
					seenKeys = append(seenKeys, key)
				}
			case ent.Size != size || ent.MTime != mt:
				plan = append(plan, &item{op: "update", key: key, local: p, size: size, mtime: mt, existing: ent})
			default:
				if deepMax > 0 && ent.MD5 != "" && size <= deepMax && fileMD5(p, deepMax) != ent.MD5 {
					plan = append(plan, &item{op: "update", key: key, local: p, size: size, mtime: mt, existing: ent})
				}
				if !ent.Seen {
					seenKeys = append(seenKeys, key)
				}
			}
			return nil
		})
		if ctx.Err() != nil {
			return
		}
		if rootFailed || walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
			missing = append(missing, src.Path)
			e.Log.Warn("source_missing", map[string]string{"path": src.Path})
			continue // never archive anything for a folder we could not read
		}
		news = append(news, srcNews...)

		if set.DeleteMode == "keep" {
			continue
		}
		var srcGoneDirs []string
		for k := range folders {
			if !strings.HasPrefix(k, prefix+"/") || dirs[k] || underAny(k, protected) {
				continue
			}
			if dirs[path.Dir(k)] {
				srcGoneDirs = append(srcGoneDirs, k)
			}
		}
		for k, ent := range files {
			// Only files this installation has seen on this PC are ever
			// archived, so files of another PC are never touched.
			if !strings.HasPrefix(k, prefix+"/") || present[k] || underAny(k, protected) || !ent.Seen {
				continue
			}
			gones = append(gones, goneFile{k, ent})
		}
		goneDirs = append(goneDirs, srcGoneDirs...)
	}

	// Renamed or moved files: same size and modified time as a file that
	// disappeared. Move them on Drive instead of uploading again.
	if set.DeleteMode != "keep" {
		news, gones, plan = matchMoves(news, gones, plan)
	}
	plan = append(plan, news...)
	sort.Strings(goneDirs)
	for _, k := range goneDirs {
		plan = append(plan, &item{op: dirOp, key: k})
	}
	for _, g := range gones {
		if underAny(g.key, goneDirs) {
			continue // archived together with its folder
		}
		plan = append(plan, &item{op: fileOp, key: g.key, existing: g.ent})
	}

	// remember which files are present here; forget stale upload sessions
	inPlan := map[string]bool{}
	for _, it := range plan {
		if it.op == "new" || it.op == "update" {
			inPlan[it.key] = true
		}
	}
	e.smu.Lock()
	for _, k := range seenKeys {
		if fe, ok := e.state.Files[k]; ok {
			fe.Seen = true
			e.state.Files[k] = fe
		}
	}
	for _, m := range mtimeFix {
		if fe, ok := e.state.Files[m.key]; ok {
			fe.MTime = m.ent.MTime
			e.state.Files[m.key] = fe
		}
	}
	for k := range e.state.Pending {
		if !inPlan[k] {
			delete(e.state.Pending, k)
		}
	}
	e.smu.Unlock()
	e.markDirty()
	return
}

// matchMoves pairs new files with disappeared files of the same size and
// modified time. Only unambiguous pairs are used (same name preferred).
func matchMoves(news []*item, gones []goneFile, plan []*item) ([]*item, []goneFile, []*item) {
	type sig struct {
		size, mtime int64
	}
	byNew := map[sig][]*item{}
	for _, n := range news {
		if n.size > 0 {
			byNew[sig{n.size, n.mtime}] = append(byNew[sig{n.size, n.mtime}], n)
		}
	}
	byGone := map[sig][]int{}
	for i, g := range gones {
		byGone[sig{g.ent.Size, g.ent.MTime}] = append(byGone[sig{g.ent.Size, g.ent.MTime}], i)
	}
	usedNew := map[*item]bool{}
	usedGone := map[int]bool{}
	for s, ns := range byNew {
		gs := byGone[s]
		if len(gs) == 0 {
			continue
		}
		if len(ns) == 1 && len(gs) == 1 {
			pairMove(ns[0], gones[gs[0]], &plan)
			usedNew[ns[0]], usedGone[gs[0]] = true, true
			continue
		}
		// several candidates: pair by identical file name when unique
		nameN := map[string][]*item{}
		for _, n := range ns {
			nameN[strings.ToLower(path.Base(n.key))] = append(nameN[strings.ToLower(path.Base(n.key))], n)
		}
		nameG := map[string][]int{}
		for _, gi := range gs {
			nameG[strings.ToLower(path.Base(gones[gi].key))] = append(nameG[strings.ToLower(path.Base(gones[gi].key))], gi)
		}
		for name, nn := range nameN {
			if gg := nameG[name]; len(nn) == 1 && len(gg) == 1 {
				pairMove(nn[0], gones[gg[0]], &plan)
				usedNew[nn[0]], usedGone[gg[0]] = true, true
			}
		}
	}
	var restN []*item
	for _, n := range news {
		if !usedNew[n] {
			restN = append(restN, n)
		}
	}
	var restG []goneFile
	for i, g := range gones {
		if !usedGone[i] {
			restG = append(restG, g)
		}
	}
	return restN, restG, plan
}

func pairMove(n *item, g goneFile, plan *[]*item) {
	*plan = append(*plan, &item{op: "move", key: n.key, from: g.key, local: n.local, size: n.size, mtime: n.mtime, existing: g.ent})
}

func (e *Engine) buildPreview(plan []*item, missing []string) {
	pv := &Preview{Time: time.Now(), Missing: missing, Items: []PlanItem{}, Complete: true}
	sortPlan(plan)
	for _, it := range plan {
		switch it.op {
		case "new":
			pv.New++
			pv.Bytes += it.size
		case "update":
			pv.Update++
			pv.Bytes += it.size
		case "move":
			pv.Move++
		default:
			pv.Remove++
		}
		if len(pv.Items) < 1000 {
			pv.Items = append(pv.Items, PlanItem{Op: it.op, Path: it.key, From: it.from, Size: it.size})
		} else {
			pv.Truncated = true
		}
	}
	e.mu.Lock()
	e.preview = pv
	e.mu.Unlock()
	e.Log.Info("preview_end", map[string]string{"new": itoa(pv.New), "update": itoa(pv.Update), "remove": itoa(pv.Remove), "bytes": HumanBytes(pv.Bytes), "move": itoa(pv.Move)})
}

// order: moves, then folder archives, file archives, then uploads
// (smallest first so many files finish quickly).
func sortPlan(plan []*item) {
	rank := func(op string) int {
		switch op {
		case "move":
			return 0
		case "archive_dir", "trash_dir":
			return 1
		case "archive", "trash":
			return 2
		}
		return 3
	}
	sort.SliceStable(plan, func(i, j int) bool {
		ri, rj := rank(plan[i].op), rank(plan[j].op)
		if ri != rj {
			return ri < rj
		}
		if ri == 3 {
			return plan[i].size < plan[j].size
		}
		return plan[i].key < plan[j].key
	})
}

// ---------- Executing ----------

func (e *Engine) execute(ctx context.Context, set config.Settings, plan []*item, sum *config.RunSummary) {
	sortPlan(plan)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var total int64
	for _, it := range plan {
		if it.op == "new" || it.op == "update" {
			total += it.size
		}
	}
	e.mu.Lock()
	e.status.Phase = "upload"
	e.status.FilesTotal = len(plan)
	e.status.BytesTotal = total
	e.mu.Unlock()

	limiter := &Limiter{Rate: func() int64 {
		return RateForTime(time.Now(), set.DayLimitKBps, set.NightLimitKBps, set.NightStart, set.NightEnd)
	}}
	capBytes := int64(set.MonthlyCapMB) << 20
	fileOp := "archive"
	if set.DeleteMode == "trash" {
		fileOp = "trash"
	}
	month := time.Now().Format("2006-01")
	date := e.Drive.ServerNow().Local().Format("2006-01-02")
	e.capReserved.Store(0)

	var smu sync.Mutex // guards sum
	var fatal error
	done := 0
	// Save progress every 15 seconds (only if something changed) so a crash
	// or power cut loses little.
	saverDone := make(chan struct{})
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-saverDone:
				return
			case <-t.C:
				e.saveState()
			}
		}
	}()
	defer close(saverDone)

	var midBlock atomic.Value // reason the run had to stop (e.g. unplugged)
	// stop only when the laptop is unplugged DURING the run ("Sync anyway"
	// on battery keeps going)
	startedOnAC := set.OnlyOnAC && e.Hooks.OnBattery != nil && !e.Hooks.OnBattery()
	handle := func(it *item) {
		if startedOnAC && e.Hooks.OnBattery() {
			midBlock.Store("on_battery")
			cancel()
			return
		}
		e.mu.Lock()
		e.status.Current = it.key
		e.mu.Unlock()
		var err error
		switch it.op {
		case "new", "update":
			err = e.upload(runCtx, set, it, limiter, capBytes, month, date)
		case "move":
			err = e.move(runCtx, it)
			if errors.Is(err, errMoveFallback) {
				// not the same file after all: archive the old one, upload the new one
				old := &item{op: fileOp, key: it.from, existing: it.existing}
				if rerr := e.remove(runCtx, old, date); rerr == nil {
					smu.Lock()
					sum.Archived++
					smu.Unlock()
				}
				it.op = "new"
				err = e.upload(runCtx, set, it, limiter, capBytes, month, date)
			} else if err != nil {
				// keep the old folder (with the file) until the move works
				for k := path.Dir(it.from); k != "." && k != ""; k = path.Dir(k) {
					e.skipDirs.Store(k, true)
				}
			}
		default:
			err = e.remove(runCtx, it, date)
		}
		if err == nil && e.stale() {
			err = errStale
		}
		smu.Lock()
		defer smu.Unlock()
		done++
		e.mu.Lock()
		e.status.FilesDone = done
		e.mu.Unlock()
		if err == nil {
			switch it.op {
			case "new":
				sum.Uploaded++
				e.Log.Info("uploaded", map[string]string{"path": it.local, "size": HumanBytes(it.size)})
			case "update":
				sum.Updated++
				e.Log.Info("updated", map[string]string{"path": it.local, "size": HumanBytes(it.size)})
			case "move":
				sum.Moved++
				e.Log.Info("moved", map[string]string{"from": it.from, "path": it.local})
			case "trash", "trash_dir":
				sum.Archived++
				e.Log.Info("trashed", map[string]string{"path": it.key})
			default:
				sum.Archived++
				e.Log.Info("archived", map[string]string{"path": it.key})
			}
			return
		}
		switch {
		case errors.Is(err, errCap):
			sum.Skipped++
			if sum.Reason != "cap" {
				sum.Reason = "cap"
				e.Log.Warn("cap_reached", nil)
			}
		case errors.Is(err, context.Canceled):
		case errors.Is(err, errStale) || drive.IsAuth(err) || drive.IsQuota(err) || errors.Is(err, ErrNotSignedIn):
			if fatal == nil {
				fatal = err
				cancel()
			}
		default:
			sum.Failed++
			name := it.local
			if name == "" {
				name = it.key
			}
			e.Log.Warn("failed", map[string]string{"path": name, "error": shortErr(err)})
			e.runErrMu.Lock()
			if len(e.runErrs) < 50 {
				e.runErrs = append(e.runErrs, time.Now().Format("2006-01-02 15:04")+"  "+name+": "+shortErr(err))
			}
			e.runErrMu.Unlock()
		}
	}

	// moves and removals run one by one (fast, server side); uploads in parallel
	i := 0
	for ; i < len(plan) && plan[i].op != "new" && plan[i].op != "update"; i++ {
		if runCtx.Err() != nil {
			break
		}
		handle(plan[i])
	}
	jobs := make(chan *item)
	var wg sync.WaitGroup
	for w := 0; w < set.Parallel; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range jobs {
				handle(it)
			}
		}()
	}
feed:
	for ; i < len(plan); i++ {
		select {
		case <-runCtx.Done():
			break feed
		case jobs <- plan[i]:
		}
	}
	close(jobs)
	wg.Wait()

	sum.Bytes = e.bytesNow.Load()
	switch {
	case fatal != nil:
		e.fail(sum, fatal)
	case ctx.Err() != nil:
		sum.Result = "stopped"
	case midBlock.Load() != nil:
		sum.Result, sum.Reason = "blocked", midBlock.Load().(string)
		e.Log.Info("blocked", map[string]string{"reason": sum.Reason})
	}
	e.saveState()
}

var errMoveFallback = errors.New("move not possible, upload instead")

// move renames/moves a file on Drive to match a rename or move on this PC.
// The contents are compared first (checksum), so two different files that
// happen to have the same size and time are never confused.
func (e *Engine) move(ctx context.Context, it *item) error {
	f, err := e.Drive.Get(ctx, it.existing.ID)
	if drive.IsNotFound(err) || err == nil && (f.Trashed || f.MD5 == "" || fileMD5(it.local, it.size) != f.MD5) {
		return errMoveFallback
	}
	if err != nil {
		return err
	}
	pid, err := e.ensureFolder(ctx, path.Dir(it.key))
	if err != nil {
		return err
	}
	if pid, err = e.verifyFolder(ctx, path.Dir(it.key), pid); err != nil {
		return err
	}
	err = e.Drive.Move(ctx, it.existing.ID, pid, path.Base(it.key))
	if drive.IsNotFound(err) {
		return errMoveFallback
	}
	if err != nil {
		return err
	}
	if !e.commit(func() {
		delete(e.state.Files, it.from)
		delete(e.state.Pending, it.from)
		e.state.Files[it.key] = config.FileEntry{ID: it.existing.ID, Size: it.size, MTime: it.mtime, MD5: f.MD5, Seen: true}
	}) {
		return errStale
	}
	return nil
}

func (e *Engine) upload(ctx context.Context, set config.Settings, it *item, lim *Limiter, capBytes int64, month, date string) error {
	// Reserve this file's size against the monthly limit so parallel
	// uploads together never go over it.
	var left atomic.Int64
	if capBytes > 0 {
		e.smu.Lock()
		used := e.state.Usage[month] + e.usageDelta.Load()
		e.smu.Unlock()
		if used+e.capReserved.Add(it.size) > capBytes {
			e.capReserved.Add(-it.size)
			e.Log.Warn("skipped_cap", map[string]string{"path": it.local})
			return errCap
		}
		left.Store(it.size)
		defer func() { e.capReserved.Add(-left.Load()) }()
	}
	parentKey := path.Dir(it.key)
	pid, err := e.ensureFolder(ctx, parentKey)
	if err != nil {
		return err
	}
	if pid, err = e.verifyFolder(ctx, parentKey, pid); err != nil {
		return err
	}
	existing := ""
	if it.op == "update" && it.existing.ID != "" {
		// the Drive copy may have been deleted or trashed by the user
		f, gerr := e.Drive.Get(ctx, it.existing.ID)
		switch {
		case gerr == nil && !f.Trashed:
			existing = it.existing.ID
		case gerr != nil && !drive.IsNotFound(gerr):
			return gerr
		}
	}
	if existing != "" && set.KeepOldVersions {
		e.smu.Lock()
		doneFor, saved := e.state.VersionDone[it.key]
		e.smu.Unlock()
		if !saved || doneFor != it.existing.MTime {
			apid, err := e.ensureFolder(ctx, archiveKey+"/"+date+"/"+parentKey)
			if err == nil {
				_, err = e.Drive.Copy(ctx, existing, path.Base(it.key), apid)
			}
			if err == nil {
				e.commit(func() { e.state.VersionDone[it.key] = it.existing.MTime })
				e.Log.Info("version_saved", map[string]string{"path": it.key})
			} else {
				e.Log.Warn("version_failed", map[string]string{"path": it.key})
			}
		}
	}

	e.smu.Lock()
	pend, hasPend := e.state.Pending[it.key]
	e.smu.Unlock()
	gotMD5 := ""
	session := ""
	if hasPend && pend.Size == it.size && pend.MTime == it.mtime && pend.ParentID == pid && pend.ExistingID == existing &&
		time.Since(time.Unix(pend.Created, 0)) < 6*24*time.Hour {
		session = pend.SessionURL
	}
	sp := drive.UploadSpec{
		ExistingID: existing,
		ParentID:   pid,
		Name:       path.Base(it.key),
		Path:       it.local,
		Size:       it.size,
		ModTime:    time.UnixMilli(it.mtime),
		Session:    &session,
		MD5:        &gotMD5,
		Wrap: func(r io.Reader) io.Reader {
			return lim.Wrap(ctx, r, func(n int) {
				e.bytesNow.Add(int64(n))
				e.usageDelta.Add(int64(n))
				if l := left.Load(); l > 0 {
					d := int64(n)
					if d > l {
						d = l
					}
					left.Add(-d)
					e.capReserved.Add(-d)
				}
			})
		},
		OnSession: func(u string) {
			e.commit(func() {
				e.state.Pending[it.key] = config.PendingUpload{SessionURL: u, Size: it.size, MTime: it.mtime,
					Created: time.Now().Unix(), ParentID: pid, ExistingID: existing}
			})
		},
	}
	id, err := e.Drive.Upload(ctx, sp)
	if errors.Is(err, drive.ErrSessionGone) {
		session = ""
		id, err = e.Drive.Upload(ctx, sp)
	}
	if err != nil && sp.ExistingID != "" && drive.IsNotFound(err) {
		// the Drive copy was deleted meanwhile; upload it again as new
		sp.ExistingID = ""
		session = ""
		id, err = e.Drive.Upload(ctx, sp)
	}
	if err != nil && sp.ExistingID == "" && drive.IsNotFound(err) {
		// the Drive folder was deleted by someone: recreate it and retry once
		for k := parentKey; k != "" && k != "."; k = path.Dir(k) {
			e.invalidateFolder(k)
			e.verified.Delete(k)
		}
		if pid, err = e.ensureFolder(ctx, parentKey); err == nil {
			sp.ParentID = pid
			session = ""
			id, err = e.Drive.Upload(ctx, sp)
		}
	}
	if err != nil {
		if errors.Is(err, drive.ErrChanged) {
			e.commit(func() { delete(e.state.Pending, it.key) })
		}
		return err
	}
	if !e.commit(func() {
		e.state.Files[it.key] = config.FileEntry{ID: id, Size: it.size, MTime: it.mtime, MD5: gotMD5, Seen: true}
		delete(e.state.Pending, it.key)
		delete(e.state.VersionDone, it.key)
	}) {
		return errStale
	}
	return nil
}

func (e *Engine) remove(ctx context.Context, it *item, date string) error {
	isDir := it.op == "archive_dir" || it.op == "trash_dir"
	if isDir {
		if _, skip := e.skipDirs.Load(it.key); skip {
			return nil // a file could not be moved out yet: try next time
		}
	}
	e.smu.Lock()
	var id string
	if isDir {
		id = e.state.Folders[it.key]
		// the same Drive folder is still used under another name (e.g. the
		// folder was only renamed in upper/lower case): never archive it
		for k, other := range e.state.Folders {
			if other == id && k != it.key && !strings.HasPrefix(k, it.key+"/") {
				delete(e.state.Folders, it.key)
				e.smu.Unlock()
				e.markDirty()
				return nil
			}
		}
	} else {
		id = e.state.Files[it.key].ID
	}
	e.smu.Unlock()

	var err error
	if id != "" {
		if it.op == "trash" || it.op == "trash_dir" {
			err = e.Drive.Trash(ctx, id)
		} else {
			parentKey := path.Dir(it.key)
			move := func() error {
				to, err := e.ensureFolder(ctx, archiveKey+"/"+date+"/"+parentKey)
				if err != nil {
					return err
				}
				name, err := e.uniqueName(ctx, to, path.Base(it.key), id)
				if err != nil {
					return err
				}
				return e.Drive.Move(ctx, id, to, name)
			}
			err = move()
			if drive.IsNotFound(err) {
				if _, gerr := e.Drive.Get(ctx, id); gerr == nil {
					// the file exists but a folder was missing: rebuild and retry once
					e.invalidateFolder(archiveKey)
					err = move()
				}
			}
		}
		if drive.IsNotFound(err) {
			if _, gerr := e.Drive.Get(ctx, id); drive.IsNotFound(gerr) {
				err = nil // already gone on Drive
			}
		}
	}
	if err != nil {
		return err
	}
	if !e.commit(func() {
		if isDir {
			for k := range e.state.Folders {
				if k == it.key || strings.HasPrefix(k, it.key+"/") {
					delete(e.state.Folders, k)
				}
			}
			for k := range e.state.Files {
				if strings.HasPrefix(k, it.key+"/") {
					delete(e.state.Files, k)
					delete(e.state.Pending, k)
				}
			}
		} else {
			delete(e.state.Files, it.key)
			delete(e.state.Pending, it.key)
		}
	}) {
		return errStale
	}
	return nil
}

// archiveDriveItem moves a Drive item into today's archive under key.
func (e *Engine) archiveDriveItem(ctx context.Context, id, key, date string) error {
	to, err := e.ensureFolder(ctx, archiveKey+"/"+date+"/"+path.Dir(key))
	if err != nil {
		return err
	}
	name, err := e.uniqueName(ctx, to, path.Base(key), id)
	if err != nil {
		return err
	}
	err = e.Drive.Move(ctx, id, to, name)
	if drive.IsNotFound(err) {
		return nil
	}
	return err
}

// uniqueName returns a name not yet used in folder to (except by id itself).
func (e *Engine) uniqueName(ctx context.Context, to, name, id string) (string, error) {
	try := name
	for i := 0; i < 50; i++ {
		other, err := e.Drive.FindChild(ctx, to, try, false)
		if err != nil {
			return "", err
		}
		if other == nil || other.ID == id {
			return try, nil
		}
		try = archiveName(name, time.Now())
		if i > 0 {
			try = archiveName(name, time.Now()) + "-" + strconv.Itoa(i+1)
		}
	}
	return try, nil
}

// archiveName adds the time to a name: "report.docx" -> "report (14.05.09).docx".
func archiveName(name string, t time.Time) string {
	ext := path.Ext(name)
	if ext == name {
		ext = ""
	}
	return strings.TrimSuffix(name, ext) + " (" + t.Format("15.04.05") + ")" + ext
}

// trimArchive permanently deletes archive day-folders older than the
// retention period (as the user chose). Age is measured with Google's clock and the folder's
// creation time on Drive, so a wrong PC clock can never expire files early.
func (e *Engine) trimArchive(ctx context.Context, set config.Settings) {
	if set.ArchiveDays <= 0 {
		return
	}
	e.smu.Lock()
	aid := e.state.ArchiveID
	last := e.state.LastArchiveTrim
	e.smu.Unlock()
	if aid == "" || time.Since(last.Round(0)) < 20*time.Hour {
		return
	}
	kids, err := e.Drive.Children(ctx, aid)
	if err != nil {
		return
	}
	now := e.Drive.ServerNow()
	cutoff := now.AddDate(0, 0, -set.ArchiveDays)
	for _, k := range kids {
		if !k.IsFolder() {
			continue
		}
		if _, err := time.ParseInLocation("2006-01-02", k.Name, time.Local); err != nil {
			continue // not a day folder made by Selva Sync
		}
		created := k.Created()
		if created.IsZero() || !created.Before(cutoff) {
			continue
		}
		err := e.Drive.Delete(ctx, k.ID)
		if drive.IsForbidden(err) {
			// shared drive members who may not delete permanently: use the trash
			err = e.Drive.Trash(ctx, k.ID)
		}
		if err == nil || drive.IsNotFound(err) {
			e.Log.Info("archive_trimmed", map[string]string{"name": k.Name})
			e.smu.Lock()
			for fk := range e.state.Folders {
				if fk == archiveKey+"/"+k.Name || strings.HasPrefix(fk, archiveKey+"/"+k.Name+"/") {
					delete(e.state.Folders, fk)
				}
			}
			e.smu.Unlock()
		}
	}
	e.smu.Lock()
	e.state.LastArchiveTrim = time.Now()
	e.smu.Unlock()
	e.markDirty()
	e.saveState()
}
