// Package app wires Selva Sync together: settings, Google sign-in, the sync
// engine, the scheduler, the tray icon and the local app window.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/selvasync/selvasync/internal/auth"
	"github.com/selvasync/selvasync/internal/config"
	"github.com/selvasync/selvasync/internal/drive"
	"github.com/selvasync/selvasync/internal/engine"
	"github.com/selvasync/selvasync/internal/platform"
)

// Build-time defaults (set with -ldflags). Users can override them in
// Settings > Advanced with their own Google Cloud credentials.
var (
	Version             = "1.2.0"
	DefaultClientID     = ""
	DefaultClientSecret = ""
)

const (
	Author      = "Dr. Selvakumar Vellingiri"
	AuthorURL   = "https://www.linkedin.com/in/selvakumar-vellingiri-phd-97744572/"
	instanceTxt = "instance.json"
)

// App is the running program.
type App struct {
	Store  *config.Store
	Auth   *auth.Manager
	Drive  *drive.Client
	Engine *engine.Engine
	Log    *engine.Logger
	I18n   *I18n
	Exe    string

	mu         sync.Mutex
	settings   config.Settings
	loadStatus config.LoadStatus

	token    string
	port     int
	srv      *http.Server
	started  time.Time
	quitOnce sync.Once
	QuitHook func()
	quitting atomic.Bool

	picker       pickerState
	saveMu       sync.Mutex // one settings save at a time
	setupProblem bool       // the office setup file has mistakes
	setupBadKeys string     // settings in it that could not be read

	aboutMu sync.Mutex
	about   *drive.About
	aboutAt time.Time
}

// New builds the app from the data folder.
func New(dataDir string) (*App, error) {
	store, err := config.NewStore(dataDir)
	if err != nil {
		return nil, err
	}
	a := &App{Store: store, started: time.Now()}
	a.Exe, _ = os.Executable()
	a.settings, a.loadStatus = store.LoadSettings()
	a.I18n = &I18n{UserDir: filepath.Join(dataDir, "languages")}
	if a.settings.Language == "" || !a.I18n.Has(a.settings.Language) {
		a.settings.Language = platform.UserLanguage()
		if !a.I18n.Has(a.settings.Language) {
			a.settings.Language = "en"
		}
	}
	a.ensureSourceIDs(&a.settings)
	if a.loadStatus == config.LoadNew || a.loadStatus == config.LoadRecovered {
		store.SaveSettings(a.settings)
	}
	a.loadSetupFile(a.loadStatus == config.LoadNew)
	a.Log = &engine.Logger{Dir: filepath.Join(dataDir, "logs")}
	a.Log.PruneOld(time.Now())
	if a.setupProblem {
		a.Log.Warn("setup_problem", map[string]string{"keys": a.setupBadKeys})
	}
	switch a.loadStatus {
	case config.LoadRecovered:
		a.Log.Warn("settings_recovered", nil)
	case config.LoadReset:
		a.Log.Error("settings_reset", nil)
	}
	httpClient := &http.Client{Timeout: 60 * time.Second}
	a.Auth = &auth.Manager{
		Store:   store,
		Protect: platform.Protect,
		Unprot:  platform.Unprotect,
		Creds:   a.creds,
		HTTP:    httpClient,
	}
	a.Auth.OnChange = func() {
		a.aboutMu.Lock()
		a.about, a.aboutAt = nil, time.Time{}
		a.aboutMu.Unlock()
	}
	a.Drive = drive.New(a.Auth.Token)
	a.Drive.Invalidate = a.Auth.Invalidate
	a.Engine = engine.New(store, a.Drive, a.Settings, engine.Hooks{
		Metered:         platform.Metered,
		WifiSSID:        platform.WifiSSID,
		SkipAttr:        platform.SkipAttr,
		Notify:          a.notify,
		MachineID:       machineID(store),
		InstallID:       installID(store),
		LegacyMachineID: platform.LegacyMachineID(),
		VolumeID:        platform.VolumeID,
		OnBattery:       platform.OnBattery,
	}, a.Log)
	a.Engine.Hooks.ReportText = a.reportText
	return a, nil
}

// machineID identifies this PC's folder on Drive. It is a hash of the
// Windows machine GUID (stable across reinstalls), or a random value kept
// in the data folder if that is unavailable.
func machineID(store *config.Store) string {
	if id := platform.MachineID(); id != "" {
		return id
	}
	if b, err := store.ReadFile("machine.id"); err == nil && len(b) >= 8 {
		return strings.TrimSpace(string(b))
	}
	id := randHex(8)
	store.WriteFile("machine.id", []byte(id))
	return id
}

// installID identifies this installation (kept in the data folder).
func installID(store *config.Store) string {
	if b, err := store.ReadFile("install.id"); err == nil && len(strings.TrimSpace(string(b))) >= 8 {
		return strings.TrimSpace(string(b))
	}
	id := randHex(8)
	store.WriteFile("install.id", []byte(id))
	return id
}

// LoadStatus tells whether settings were recovered from a backup or reset.
func (a *App) LoadStatus() config.LoadStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.loadStatus
}

func (a *App) ensureSourceIDs(s *config.Settings) {
	seen := map[string]bool{}
	for i := range s.Sources {
		if s.Sources[i].ID == "" || seen[s.Sources[i].ID] {
			s.Sources[i].ID = randHex(6)
		}
		seen[s.Sources[i].ID] = true
	}
}

func (a *App) creds() (string, string) {
	s := a.Settings()
	if s.ClientID != "" {
		return s.ClientID, s.ClientSecret
	}
	if DefaultClientID != "" {
		return DefaultClientID, DefaultClientSecret
	}
	return fileCreds()
}

// CredsFileName is the file Google Cloud Console downloads for a "Desktop
// app" OAuth client. Placing it next to SelvaSync.exe lets a publisher share
// Selva Sync with their own Google connection without rebuilding it.
const CredsFileName = "google-client.json"

var (
	fileCredsOnce        sync.Once
	fileCredID, fileCred string
	filePickerURL        string
)

func fileCreds() (string, string) {
	fileCredsOnce.Do(func() {
		exe, _ := os.Executable()
		for _, p := range []string{filepath.Join(filepath.Dir(exe), CredsFileName), filepath.Join(config.DataDir(), CredsFileName)} {
			if id, sec, ok := ParseCredsFile(p); ok {
				fileCredID, fileCred = id, sec
				filePickerURL = parsePickerURL(p)
				return
			}
		}
	})
	return fileCredID, fileCred
}

// ParseCredsFile reads a Google OAuth client JSON file.
func ParseCredsFile(path string) (id, secret string, ok bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", false
	}
	var f struct {
		Installed struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
		} `json:"installed"`
	}
	if json.Unmarshal(b, &f) != nil || !strings.HasSuffix(f.Installed.ClientID, ".apps.googleusercontent.com") {
		return "", "", false
	}
	return f.Installed.ClientID, f.Installed.ClientSecret, true
}

// parsePickerURL reads the optional "selvasync_picker_url" that a
// publisher may add to google-client.json.
func parsePickerURL(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var f struct {
		URL string `json:"selvasync_picker_url"`
	}
	json.Unmarshal(b, &f)
	s := config.Settings{PickerURL: f.URL}
	s.Normalize()
	return s.PickerURL
}

// Settings returns a copy of the current settings.
func (a *App) Settings() config.Settings {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.settings
	s.Sources = append([]config.Source{}, s.Sources...)
	s.Excludes = append([]string{}, s.Excludes...)
	s.BlockedWifi = append([]string{}, s.BlockedWifi...)
	s.AllowedWifi = append([]string{}, s.AllowedWifi...)
	s.Managed = append([]string{}, s.Managed...)
	return s
}

// SaveSettings validates, stores and applies new settings.
func (a *App) SaveSettings(s config.Settings) error { return a.saveSettings(s, false) }

// saveSettings stores new settings. keepLocation keeps the stored backup
// folder (the window never changes it; only Google's folder chooser does).
// Everything is decided under the lock, so a folder chosen at the same
// moment is never undone by a window save.
func (a *App) saveSettings(s config.Settings, keepLocation bool) error {
	a.saveMu.Lock()
	defer a.saveMu.Unlock()
	a.ensureSourceIDs(&s)
	a.mu.Lock()
	old := a.settings
	if keepLocation {
		s.Location = old.Location
	}
	s = keepManaged(s, old)
	s.Normalize()
	if !a.I18n.Has(s.Language) {
		s.Language = "en"
	}
	a.settings = s
	a.loadStatus = config.LoadOK // the user has now saved settings
	a.mu.Unlock()
	if err := a.Store.SaveSettings(s); err != nil {
		return err
	}
	if old.StartWithWindows != s.StartWithWindows {
		platform.SetStartup(s.StartWithWindows, a.Exe)
	}
	// A new schedule, or the first folder, starts counting from now instead
	// of syncing at once for "missed" times.
	if old.Schedule != s.Schedule || !anyEnabled(old.Sources) && anyEnabled(s.Sources) {
		a.Engine.MarkScheduled(time.Now())
	}
	if old.ClientID != s.ClientID {
		a.Engine.Pause(60 * time.Second)
		a.Auth.SignOut(context.Background())
		a.Engine.ForgetRemote()
		a.Engine.Resume()
	}
	if old.Location != s.Location {
		// wait for any running sync to finish before the new folder is used
		a.Engine.Pause(60 * time.Second)
		a.Engine.Resume()
		a.Log.Info("location_set", map[string]string{"name": locationLabel(s)})
	}
	return nil
}

// locationLabel names the backup folder for people.
func locationLabel(s config.Settings) string {
	if s.Location.FolderID == "" {
		return "My Drive › " + s.DriveFolderName
	}
	return s.Location.Name
}

// pickerURL is the web page with Google's folder chooser ("" if none).
func (a *App) pickerURL() string {
	if u := a.Settings().PickerURL; u != "" {
		return u
	}
	if DefaultPickerURL != "" {
		return DefaultPickerURL
	}
	fileCreds()
	return filePickerURL
}

func anyEnabled(src []config.Source) bool {
	for _, s := range src {
		if s.Enabled {
			return true
		}
	}
	return false
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (a *App) lang() string { return a.Settings().Language }

func (a *App) notify(key string, args map[string]string) {
	platform.Current().Notify("Selva Sync", a.I18n.T(a.lang(), key, args))
}

// URL is the address of the app window, including this session's key.
func (a *App) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/?t=%s", a.port, a.token)
}

// OpenWindow shows the app window.
func (a *App) OpenWindow() { platform.OpenAppWindow(a.URL()) }

type instanceInfo struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

// Start begins serving the local app window on 127.0.0.1 only.
func (a *App) Start() error {
	a.token = randHex(24)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	a.port = ln.Addr().(*net.TCPAddr).Port
	a.srv = &http.Server{Handler: a.routes(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 2 * time.Minute, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 64 << 10}
	go a.srv.Serve(ln)
	tok := a.token
	if enc, err := platform.Protect([]byte(a.token)); err == nil {
		tok = "dpapi:" + hex.EncodeToString(enc) // readable only by this Windows user
	}
	b, _ := json.Marshal(instanceInfo{Port: a.port, Token: tok, PID: os.Getpid()})
	a.Store.WriteFile(instanceTxt, b)
	if a.Settings().StartWithWindows {
		platform.SetStartup(true, a.Exe)
	}
	go a.scheduler()
	go a.trayUpdater()
	go a.alertWatcher()
	return nil
}

// EndSession is called when Windows shuts down: stop and save quickly.
func (a *App) EndSession() {
	a.quitting.Store(true)
	a.Engine.Stop()
	for i := 0; i < 30 && a.Engine.Status().Running; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	a.Engine.SaveNow()
}

// Shutdown stops everything.
func (a *App) Shutdown() {
	a.quitting.Store(true)
	a.Engine.Stop()
	for i := 0; i < 50 && a.Engine.Status().Running; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if a.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		a.srv.Shutdown(ctx)
		cancel()
	}
	a.Store.Remove(instanceTxt)
}

// Quit ends the program.
func (a *App) Quit() {
	a.quitting.Store(true)
	a.quitOnce.Do(func() {
		if a.QuitHook != nil {
			a.QuitHook()
		}
	})
}

// SignalRunning asks an already running Selva Sync to open its window (or
// to quit, for updates). Returns false if none is running.
func SignalRunning(dataDir, action string) bool {
	b, err := os.ReadFile(filepath.Join(dataDir, instanceTxt))
	if err != nil {
		return false
	}
	var in instanceInfo
	if json.Unmarshal(b, &in) != nil || in.Port == 0 {
		return false
	}
	if strings.HasPrefix(in.Token, "dpapi:") {
		raw, err := hex.DecodeString(strings.TrimPrefix(in.Token, "dpapi:"))
		if err != nil {
			return false
		}
		plain, err := platform.Unprotect(raw)
		if err != nil {
			return false
		}
		in.Token = string(plain)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", in.Port)
	switch action {
	case "open":
		c := &http.Client{Timeout: 3 * time.Second}
		req, _ := http.NewRequest("GET", base+"/api/ping", nil)
		req.Header.Set("X-Selva-Token", in.Token)
		resp, err := c.Do(req)
		if err != nil {
			return false
		}
		resp.Body.Close()
		platform.OpenAppWindow(fmt.Sprintf("%s/?t=%s", base, in.Token))
		return true
	case "quit":
		c := &http.Client{Timeout: 5 * time.Second}
		req, _ := http.NewRequest("POST", base+"/api/quit", strings.NewReader("{}"))
		req.Header.Set("X-Selva-Token", in.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.Do(req)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return true
	}
	return false
}

// ---------- scheduler ----------

// NextRun returns when the next automatic sync is due, measured from the
// last completed scheduled run. Zero means manual only. Each slot is built
// from its own date and clock time, so daylight-saving changes never make
// the schedule repeat or drift.
func NextRun(s config.Schedule, anchor, now time.Time) time.Time {
	hm, _ := time.Parse("15:04", s.Time)
	loc := now.Location()
	anchor = anchor.Round(0)
	now = now.Round(0)
	if s.Mode == "interval" {
		if anchor.IsZero() {
			return now
		}
		return anchor.Add(time.Duration(s.EveryHours) * time.Hour)
	}
	if s.Mode != "daily" && s.Mode != "weekly" {
		return time.Time{}
	}
	// The first scheduled time after the last scheduled run. Each day's time
	// is built from its own date, so daylight-saving days (even when the
	// change happens at midnight) never produce repeated runs.
	from := anchor
	if from.IsZero() || from.After(now) {
		from = now
	}
	y, m, d := from.In(loc).Date()
	for i := -1; i <= 8; i++ {
		day := time.Date(y, m, d+i, 12, 0, 0, 0, loc) // noon: safe from DST gaps
		if s.Mode == "weekly" && int(day.Weekday()) != s.Weekday {
			continue
		}
		slot := time.Date(day.Year(), day.Month(), day.Day(), hm.Hour(), hm.Minute(), 0, 0, loc)
		for h := 1; h <= 2 && slot.Day() != day.Day(); h++ {
			// this time does not exist today (clock jumps at midnight):
			// use the first existing time after it, on the same day
			slot = time.Date(day.Year(), day.Month(), day.Day(), hm.Hour()+h, hm.Minute(), 0, 0, loc)
		}
		if slot.After(from) || (anchor.IsZero() && !slot.Before(from)) {
			return slot
		}
	}
	return now.Add(24 * time.Hour)
}

func (a *App) hasSources() bool {
	for _, s := range a.Settings().Sources {
		if s.Enabled {
			return true
		}
	}
	return false
}

// retryDelay grows after repeated failures: 3 min (e.g. Wi-Fi still
// connecting after wake-up), then 15, 30, 60 ... up to 6 h.
func retryDelay(streak int) time.Duration {
	if streak <= 1 {
		return 3 * time.Minute
	}
	d := 15 * time.Minute
	for i := 2; i < streak && d < 6*time.Hour; i++ {
		d *= 2
	}
	if d > 6*time.Hour {
		d = 6 * time.Hour
	}
	return d
}

func (a *App) scheduler() {
	a.Engine.FixClock(time.Now())
	_, _, lastSched, _, _ := a.Engine.StateInfo()
	if lastSched.IsZero() {
		// first run: wait for the next scheduled time instead of syncing at once
		a.Engine.MarkScheduled(time.Now())
	}
	startupDone := false
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for range tick.C {
		if a.quitting.Load() {
			return
		}
		set := a.Settings()
		if a.Engine.Paused() || !a.Auth.SignedIn() || !a.hasSources() || a.Engine.Status().Running {
			continue
		}
		now := time.Now()
		a.Engine.FixClock(now)
		trigger := ""
		wantStartup := !startupDone && set.SyncOnStartup && now.Sub(a.started) > 90*time.Second
		if wantStartup {
			trigger = "startup"
		}
		_, _, anchor, _, _ := a.Engine.StateInfo()
		next := NextRun(set.Schedule, anchor, now)
		if trigger == "" && !next.IsZero() && !now.Before(next) {
			trigger = "schedule"
		}
		if trigger == "" {
			continue
		}
		wait := 15 * time.Minute
		if s := a.Engine.FailStreak(); s > 0 {
			wait = retryDelay(s)
		}
		if time.Since(a.Engine.LastAttempt()) < wait {
			continue // retry a blocked or failed run later, not in a loop
		}
		if trigger == "startup" {
			startupDone = true
		}
		started := now
		a.Engine.Start(engine.Options{Trigger: trigger, OnDone: func(sum config.RunSummary) {
			if a.quitting.Load() {
				return // closed or shut down mid-sync: run again next time
			}
			switch sum.Result {
			case "ok", "nothing", "partial", "cap", "stopped":
				a.Engine.MarkScheduled(started)
			}
		}})
	}
}

// ---------- tray ----------

const (
	menuStatus = 1
	menuOpen   = 2
	menuSync   = 3
	menuStop   = 4
	menuQuit   = 9
)

// TrayConfig returns the tray icon setup.
func (a *App) TrayConfig(icon []byte, openOnReady bool) platform.TrayConfig {
	return platform.TrayConfig{
		Icon:    icon,
		Tooltip: "Selva Sync",
		Menu: func() []platform.MenuItem {
			l := a.lang()
			items := []platform.MenuItem{{ID: -menuStatus, Label: a.statusLine()}, {ID: 0}, {ID: menuOpen, Label: a.I18n.T(l, "tray_open", nil)}}
			if a.Engine.Status().Running {
				items = append(items, platform.MenuItem{ID: menuStop, Label: a.I18n.T(l, "tray_stop", nil)})
			} else {
				items = append(items, platform.MenuItem{ID: menuSync, Label: a.I18n.T(l, "tray_sync", nil)})
			}
			return append(items, platform.MenuItem{ID: 0}, platform.MenuItem{ID: menuQuit, Label: a.I18n.T(l, "tray_quit", nil)})
		},
		OnOpen: a.OpenWindow,
		OnMenu: func(id int) {
			switch id {
			case menuOpen:
				a.OpenWindow()
			case menuSync:
				if a.Engine.Paused() || !a.Auth.SignedIn() || !a.hasSources() {
					a.OpenWindow() // nothing to sync yet: show what is missing
					return
				}
				if err := a.Engine.Start(engine.Options{Trigger: "manual"}); err == nil && a.Settings().Notifications {
					a.notify("notify_started", nil)
				}
			case menuStop:
				a.Engine.Stop()
			case menuQuit:
				a.Quit()
			}
		},
		OnReady: func() {
			if openOnReady {
				a.OpenWindow()
			}
		},
		OnEndSession: a.EndSession,
	}
}

func (a *App) statusLine() string {
	l := a.lang()
	st := a.Engine.Status()
	if st.Running {
		pct := 0
		if st.BytesTotal > 0 {
			pct = int(st.BytesDone * 100 / st.BytesTotal)
		}
		return a.I18n.T(l, "tray_syncing", map[string]string{"pct": fmt.Sprint(pct)})
	}
	if !a.Auth.SignedIn() {
		return a.I18n.T(l, "tray_not_signed_in", nil)
	}
	last, _, _, _, _ := a.Engine.StateInfo()
	if last == nil {
		return a.I18n.T(l, "tray_never", nil)
	}
	return a.I18n.T(l, "tray_last", map[string]string{"time": last.End.Format("02 Jan 15:04")})
}

func (a *App) trayUpdater() {
	prev := ""
	for {
		time.Sleep(2 * time.Second)
		s := "Selva Sync - " + a.statusLine()
		if s != prev {
			prev = s
			platform.Current().SetTooltip(s)
		}
	}
}
