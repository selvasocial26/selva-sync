// Package config holds Selva Sync's settings, sync state and file locations.
// Everything is stored locally on this computer. Nothing is sent anywhere
// except to the user's own Google Drive.
package config

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Source is one local folder (or whole drive) that is mirrored to Drive.
type Source struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	Enabled bool   `json:"enabled"`
}

// Schedule decides when automatic syncs run.
type Schedule struct {
	Mode       string `json:"mode"`       // "manual", "interval", "daily", "weekly"
	EveryHours int    `json:"everyHours"` // for "interval"
	Time       string `json:"time"`       // "HH:MM" for daily / weekly
	Weekday    int    `json:"weekday"`    // 0 = Sunday ... 6 = Saturday
}

// Settings are the user's choices, edited from the app window.
type Settings struct {
	Language         string   `json:"language"`
	Sources          []Source `json:"sources"`
	DriveFolderName  string   `json:"driveFolderName"`
	ComputerName     string   `json:"computerName"`
	Schedule         Schedule `json:"schedule"`
	SyncOnStartup    bool     `json:"syncOnStartup"`
	StartWithWindows bool     `json:"startWithWindows"`
	DayLimitKBps     int      `json:"dayLimitKBps"`   // 0 = no limit
	NightLimitKBps   int      `json:"nightLimitKBps"` // 0 = no limit
	NightStart       string   `json:"nightStart"`
	NightEnd         string   `json:"nightEnd"`
	MonthlyCapMB     int      `json:"monthlyCapMB"` // 0 = no cap
	PauseOnMetered   bool     `json:"pauseOnMetered"`
	BlockedWifi      []string `json:"blockedWifi"`
	AllowedWifi      []string `json:"allowedWifi"`
	Excludes         []string `json:"excludes"`
	MaxFileSizeMB    int      `json:"maxFileSizeMB"` // 0 = no limit
	DeleteMode       string   `json:"deleteMode"`    // "archive", "keep", "trash"
	KeepOldVersions  bool     `json:"keepOldVersions"`
	ArchiveDays      int      `json:"archiveDays"` // 0 = keep forever
	Notifications    bool     `json:"notifications"`
	Parallel         int      `json:"parallel"`
	ClientID         string   `json:"clientId,omitempty"`
	ClientSecret     string   `json:"clientSecret,omitempty"`
	// Location is the one Google Drive folder that holds the whole backup.
	// Empty FolderID = default "Selva Sync" folder in My Drive.
	Location  Location `json:"location"`
	PickerURL string   `json:"pickerUrl,omitempty"` // web page with Google's folder chooser
	AlertDays int      `json:"alertDays"`           // warn after this many days without a successful backup (0 = off)
	OnlyOnAC  bool     `json:"onlyOnAC"`            // laptops: sync only while plugged in
	Reports   bool     `json:"reports"`             // write a monthly report file into the backup folder
	// CloudFiles also backs up OneDrive files that are only in the cloud.
	// They are downloaded for the backup and made online-only again after.
	CloudFiles bool `json:"cloudFiles"`
	// Managed lists settings fixed by the organisation's setup file.
	Managed []string `json:"managed,omitempty"`
}

// Location is a folder in My Drive or in a shared drive.
type Location struct {
	FolderID string `json:"folderId,omitempty"`
	Name     string `json:"name,omitempty"`
	DriveID  string `json:"driveId,omitempty"` // shared drive ID, empty for My Drive
}

// DefaultExcludes skips files that are temporary, rebuilt automatically or
// belong to Windows itself. Users can edit this list.
//
// Rules starting with "/" only match from the top of a drive, so a user's own
// folder called "Windows" or "AppData" inside D:\Clients is still backed up.
// Rules without "/" match a file or folder name anywhere.
var DefaultExcludes = []string{
	// temporary and system helper files (anywhere)
	"~$*", "*.tmp", "*.temp", "Thumbs.db", "desktop.ini", ".DS_Store",
	"*.crdownload", "*.partial", "NTUSER.DAT*", "ntuser.dat.LOG*", "UsrClass.dat*",
	"node_modules", "__pycache__",
	// Windows system areas (top of the drive only)
	"/Windows", "/Windows.old", "/Program Files", "/Program Files (x86)", "/ProgramData",
	"/$Recycle.Bin", "/System Volume Information", "/Recovery", "/PerfLogs",
	"/$WinREAgent", "/$SysReset", "/$Windows.~BT", "/$Windows.~WS", "/Config.Msi", "/MSOCache",
	"/OneDriveTemp", "/pagefile.sys", "/hiberfil.sys", "/swapfile.sys", "/DumpStack.log*",
	"/Users/*/AppData", "/Users/*/.cache", "/Users/Default", "/Users/All Users",
	"/Documents and Settings",
}

// oldDefaults were name-anywhere rules in version 1.0 that wrongly skipped
// users' own folders; they are replaced by the drive-top rules above.
var oldDefaults = map[string]string{
	"windows": "/Windows", "program files": "/Program Files", "program files (x86)": "/Program Files (x86)",
	"programdata": "/ProgramData", "appdata": "/Users/*/AppData", "$recycle.bin": "/$Recycle.Bin",
	"system volume information": "/System Volume Information", "pagefile.sys": "/pagefile.sys",
	"hiberfil.sys": "/hiberfil.sys", "swapfile.sys": "/swapfile.sys", ".cache": "/Users/*/.cache",
	"*.part": "", "*.lock": "", ".git": "",
}

// Limits keep numbers in a safe range.
const (
	MaxSpeedKBps   = 10_000_000
	MaxCapMB       = 100_000_000
	MaxFileSizeMB  = 100_000_000
	MaxArchiveDays = 3650
	MaxSources     = 200
)

// Defaults returns sensible first-run settings.
func Defaults() Settings {
	host, _ := os.Hostname()
	return Settings{
		Language:         "",
		Sources:          []Source{},
		DriveFolderName:  "Selva Sync",
		ComputerName:     host,
		Schedule:         Schedule{Mode: "daily", EveryHours: 6, Time: "21:00", Weekday: 0},
		SyncOnStartup:    false,
		StartWithWindows: true,
		NightStart:       "22:00",
		NightEnd:         "07:00",
		PauseOnMetered:   true,
		BlockedWifi:      []string{},
		AllowedWifi:      []string{},
		Excludes:         append([]string(nil), DefaultExcludes...),
		DeleteMode:       "archive",
		KeepOldVersions:  false,
		ArchiveDays:      90,
		Notifications:    true,
		Parallel:         3,
		AlertDays:        3,
		Reports:          true,
	}
}

// CleanSourcePath returns a safe absolute local path, or "" if the path is
// not allowed (empty, relative, network share or device path).
func CleanSourcePath(p string) string {
	if p == "" || strings.TrimSpace(p) != p {
		return "" // leading/trailing spaces would point at a different folder
	}
	if runtime.GOOS == "windows" || strings.Contains(p, `\`) || (len(p) >= 2 && p[1] == ':') {
		p = strings.ReplaceAll(p, "/", `\`)
		if strings.HasPrefix(p, `\\`) {
			return "" // network share or device path: map a drive letter instead
		}
		if len(p) < 2 || p[1] != ':' || !isLetter(p[0]) {
			return ""
		}
		if len(p) == 2 {
			p += `\` // "D:" -> "D:\"
		}
		if p[2] != '\\' {
			return "" // "D:folder" is relative to the current folder on D:
		}
		if !validWinPath(p[3:]) {
			return ""
		}
		p = strings.ToUpper(p[:1]) + p[1:]
		cleaned := filepath.Clean(p)
		if runtime.GOOS != "windows" { // keep tests on Linux working
			cleaned = p
			for strings.Contains(cleaned, `\\`) {
				cleaned = strings.ReplaceAll(cleaned, `\\`, `\`)
			}
			if len(cleaned) > 3 {
				cleaned = strings.TrimRight(cleaned, `\`)
			}
		}
		return cleaned
	}
	if !filepath.IsAbs(p) {
		return ""
	}
	return filepath.Clean(p)
}

var reserved = map[string]bool{"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"conin$": true, "conout$": true, "com¹": true, "com²": true, "com³": true, "lpt¹": true, "lpt²": true, "lpt³": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true}

// validWinPath checks the part after "X:\": no control characters, no
// characters Windows forbids, no reserved device names, no names ending
// in a dot or space (Windows would silently treat them as another folder).
func validWinPath(rest string) bool {
	if len(rest) > 32000 {
		return false
	}
	for _, seg := range strings.Split(rest, `\`) {
		if seg == "" {
			continue
		}
		if seg == "." || seg == ".." {
			return false
		}
		for _, r := range seg {
			if r < 32 || strings.ContainsRune(`<>:"/|?*`, r) {
				return false
			}
		}
		if strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " ") {
			return false
		}
		base := strings.ToLower(seg)
		if i := strings.IndexByte(base, '.'); i >= 0 {
			base = base[:i]
		}
		if reserved[strings.TrimSpace(base)] {
			return false
		}
	}
	return true
}

// validDriveID accepts Google Drive IDs (letters, digits, - and _).
func validDriveID(id string) bool {
	if id == "" || len(id) > 200 {
		return id == ""
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func isLetter(c byte) bool { return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }

// Normalize fixes missing or out-of-range values.
func (s *Settings) Normalize() {
	d := Defaults()
	if strings.TrimSpace(s.DriveFolderName) == "" {
		s.DriveFolderName = d.DriveFolderName
	}
	if strings.TrimSpace(s.ComputerName) == "" {
		s.ComputerName = d.ComputerName
	}
	if s.ComputerName == "" {
		s.ComputerName = "My Computer"
	}
	if s.DriveFolderName = cleanName(s.DriveFolderName); s.DriveFolderName == "" {
		s.DriveFolderName = d.DriveFolderName
	}
	if s.ComputerName = cleanName(s.ComputerName); s.ComputerName == "" {
		s.ComputerName = cleanName(d.ComputerName)
	}
	if s.ComputerName == "" {
		s.ComputerName = "My Computer"
	}
	switch s.Schedule.Mode {
	case "manual", "interval", "daily", "weekly":
	default:
		s.Schedule.Mode = "daily"
	}
	s.Schedule.EveryHours = clamp(s.Schedule.EveryHours, 1, 168)
	if !validHHMM(s.Schedule.Time) {
		s.Schedule.Time = d.Schedule.Time
	}
	if s.Schedule.Weekday < 0 || s.Schedule.Weekday > 6 {
		s.Schedule.Weekday = 0
	}
	if !validHHMM(s.NightStart) {
		s.NightStart = d.NightStart
	}
	if !validHHMM(s.NightEnd) {
		s.NightEnd = d.NightEnd
	}
	s.DayLimitKBps = clamp(s.DayLimitKBps, 0, MaxSpeedKBps)
	s.NightLimitKBps = clamp(s.NightLimitKBps, 0, MaxSpeedKBps)
	s.MonthlyCapMB = clamp(s.MonthlyCapMB, 0, MaxCapMB)
	s.MaxFileSizeMB = clamp(s.MaxFileSizeMB, 0, MaxFileSizeMB)
	s.ArchiveDays = clamp(s.ArchiveDays, 0, MaxArchiveDays)
	switch s.DeleteMode {
	case "archive", "keep", "trash":
	default:
		s.DeleteMode = "archive"
	}
	s.Parallel = clamp(s.Parallel, 1, 8)

	// sources: valid absolute local paths only, no duplicates
	var srcs []Source
	seen := map[string]bool{}
	ids := map[string]bool{}
	for _, src := range s.Sources {
		p := CleanSourcePath(src.Path)
		if p == "" || seen[strings.ToLower(p)] || len(srcs) >= MaxSources {
			continue
		}
		seen[strings.ToLower(p)] = true
		src.Path = p
		src.ID = strings.TrimSpace(src.ID)
		if src.ID == "" || ids[src.ID] {
			src.ID = ""
		} else {
			ids[src.ID] = true
		}
		srcs = append(srcs, src)
	}
	if srcs == nil {
		srcs = []Source{}
	}
	s.Sources = srcs

	s.BlockedWifi = cleanList(s.BlockedWifi, 100)
	s.AllowedWifi = cleanList(s.AllowedWifi, 100)
	s.Excludes = migrateExcludes(cleanList(s.Excludes, 1000))
	s.ClientID = strings.TrimSpace(s.ClientID)
	s.ClientSecret = strings.TrimSpace(s.ClientSecret)
	s.AlertDays = clamp(s.AlertDays, 0, 365)
	s.Location.FolderID = strings.TrimSpace(s.Location.FolderID)
	if !validDriveID(s.Location.FolderID) {
		s.Location = Location{}
	}
	if !validDriveID(s.Location.DriveID) {
		s.Location.DriveID = ""
	}
	if r := []rune(s.Location.Name); len(r) > 200 {
		s.Location.Name = string(r[:200])
	}
	s.PickerURL = strings.TrimSpace(s.PickerURL)
	if pu, err := url.Parse(s.PickerURL); err != nil || pu.Scheme != "https" || pu.Host == "" || pu.User != nil ||
		len(s.PickerURL) > 500 || strings.ContainsAny(s.PickerURL, " \t\r\n\"'<>\\") {
		s.PickerURL = ""
	}
	s.Managed = cleanList(s.Managed, 50)
}

func migrateExcludes(in []string) []string {
	out := []string{}
	have := map[string]bool{}
	for _, v := range in {
		have[strings.ToLower(v)] = true
	}
	for _, v := range in {
		if repl, ok := oldDefaults[strings.ToLower(v)]; ok {
			if repl != "" && !have[strings.ToLower(repl)] {
				out = append(out, repl)
				have[strings.ToLower(repl)] = true
			}
			continue
		}
		out = append(out, v)
	}
	return out
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func cleanList(in []string, max int) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[strings.ToLower(v)] || len(out) >= max {
			continue
		}
		if len(v) > 260 {
			v = v[:260]
		}
		seen[strings.ToLower(v)] = true
		out = append(out, v)
	}
	return out
}

func cleanName(n string) string {
	n = strings.TrimSpace(n)
	n = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r < 32 {
			return '-'
		}
		return r
	}, n)
	if r := []rune(n); len(r) > 100 {
		n = string(r[:100]) // cut on character boundaries (safe for Gujarati etc.)
	}
	n = strings.TrimSpace(n)
	if strings.Trim(n, ".") == "" {
		return "" // "." or ".." are not usable folder names
	}
	return n
}

func validHHMM(v string) bool {
	_, err := time.Parse("15:04", v)
	return err == nil
}

// FileEntry remembers what was uploaded for one file.
type FileEntry struct {
	ID    string `json:"id"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`          // unix milliseconds of the local file when uploaded
	MD5   string `json:"md5,omitempty"`  // Drive checksum, filled by full checks
	Seen  bool   `json:"seen,omitempty"` // this install has seen the file on this PC
}

// PendingUpload lets a large upload continue after an interruption.
type PendingUpload struct {
	SessionURL string `json:"url"`
	Size       int64  `json:"size"`
	MTime      int64  `json:"mtime"`
	Created    int64  `json:"created"`
	ParentID   string `json:"parent,omitempty"`   // folder the upload goes into
	ExistingID string `json:"existing,omitempty"` // file being updated, if any
}

// RunSummary describes the result of one sync.
type RunSummary struct {
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	DryRun   bool      `json:"dryRun"`
	Result   string    `json:"result"` // ok, partial, failed, stopped, cap, blocked, nothing
	Reason   string    `json:"reason,omitempty"`
	Uploaded int       `json:"uploaded"`
	Updated  int       `json:"updated"`
	Moved    int       `json:"moved"`
	Archived int       `json:"archived"`
	Skipped  int       `json:"skipped"`
	Failed   int       `json:"failed"`
	Bytes    int64     `json:"bytes"`
}

// State is what Selva Sync remembers between syncs so that unchanged files
// are never uploaded twice and nothing is duplicated on Drive.
type State struct {
	Version         int                      `json:"version"`
	RootID          string                   `json:"rootId"`
	ComputerID      string                   `json:"computerId"`
	ArchiveID       string                   `json:"archiveId"`
	Folders         map[string]string        `json:"folders"`
	Files           map[string]FileEntry     `json:"files"`
	Pending         map[string]PendingUpload `json:"pending"`
	Usage           map[string]int64         `json:"usage"` // "2026-10" -> bytes uploaded
	VersionDone     map[string]int64         `json:"versionDone,omitempty"`
	LastRun         *RunSummary              `json:"lastRun,omitempty"`
	LastSuccess     time.Time                `json:"lastSuccess"`
	LastAttempt     time.Time                `json:"lastAttempt"`
	LastScheduled   time.Time                `json:"lastScheduled"`
	LastArchiveTrim time.Time                `json:"lastArchiveTrim"`
	LastFullCheck   time.Time                `json:"lastFullCheck"`
	LastDeepCheck   time.Time                `json:"lastDeepCheck"`
	RunInProgress   bool                     `json:"runInProgress"`
	RunSince        time.Time                `json:"runSince"` // start of the run that set RunInProgress
	MigratedFromV1  bool                     `json:"-"`
	AppliedRoot     string                   `json:"appliedRoot"`
	AppliedComputer string                   `json:"appliedComputer"`
	SourceVolumes   map[string]string        `json:"sourceVolumes"`
	FailStreak      int                      `json:"failStreak"`
	Monthly         map[string]*MonthStats   `json:"monthly,omitempty"`
	ReportDone      map[string]bool          `json:"reportDone,omitempty"`
	LastAlert       time.Time                `json:"lastAlert"`
	// KeptSeen remembers which Drive files were seen on this PC before the
	// backup folder was changed, so going back to a folder keeps working.
	KeptSeen     map[string]bool `json:"keptSeen,omitempty"`
	FirstAttempt time.Time       `json:"firstAttempt"`         // first real sync attempt (for the overdue reminder)
	LastPartial  time.Time       `json:"lastPartial"`          // last run that backed up files but had some problems
	LocationID   string          `json:"locationId,omitempty"` // backup location the remote IDs belong to
}

// MonthStats are totals for the monthly report.
type MonthStats struct {
	Runs     int      `json:"runs"`
	OK       int      `json:"ok"`
	Problems int      `json:"problems"`
	Failed   int      `json:"failed"`
	Uploaded int      `json:"uploaded"`
	Updated  int      `json:"updated"`
	Moved    int      `json:"moved"`
	Archived int      `json:"archived"`
	Bytes    int64    `json:"bytes"`
	Errors   []string `json:"errors,omitempty"` // up to 50 recent problems
}

// NewState returns an empty state.
func NewState() *State {
	st := &State{Version: StateVersion}
	st.fill()
	return st
}

// StateVersion 2 added FileEntry.Seen.
const StateVersion = 2

func (st *State) fill() {
	if st.Folders == nil {
		st.Folders = map[string]string{}
	}
	if st.Files == nil {
		st.Files = map[string]FileEntry{}
	}
	if st.Pending == nil {
		st.Pending = map[string]PendingUpload{}
	}
	if st.Usage == nil {
		st.Usage = map[string]int64{}
	}
	if st.SourceVolumes == nil {
		st.SourceVolumes = map[string]string{}
	}
	if st.VersionDone == nil {
		st.VersionDone = map[string]int64{}
	}
	if st.Monthly == nil {
		st.Monthly = map[string]*MonthStats{}
	}
	if st.ReportDone == nil {
		st.ReportDone = map[string]bool{}
	}
	if st.KeptSeen == nil {
		st.KeptSeen = map[string]bool{}
	}
}

// ResetForLocation forgets the Drive side when the backup folder changes,
// but remembers which Drive files were seen on this PC.
func (st *State) ResetForLocation() {
	if st.KeptSeen == nil || len(st.KeptSeen) > 500000 {
		st.KeptSeen = map[string]bool{}
	}
	for _, fe := range st.Files {
		if fe.Seen && fe.ID != "" {
			st.KeptSeen[fe.ID] = true
		}
	}
	st.ResetRemote()
}

// ResetRemote forgets everything about Drive (used when the Drive folder was
// deleted or the user signed in with another account).
func (st *State) ResetRemote() {
	st.RootID, st.ComputerID, st.ArchiveID = "", "", ""
	st.AppliedRoot, st.AppliedComputer = "", ""
	st.Folders = map[string]string{}
	st.Files = map[string]FileEntry{}
	st.Pending = map[string]PendingUpload{}
	st.VersionDone = map[string]int64{}
	st.LastFullCheck = time.Time{}
}

// LoadStatus tells the app how settings were loaded.
type LoadStatus string

const (
	LoadOK        LoadStatus = ""
	LoadNew       LoadStatus = "new"       // first run
	LoadRecovered LoadStatus = "recovered" // main file damaged, backup used
	LoadReset     LoadStatus = "reset"     // main file and backup damaged
)

// Store loads and saves settings and state as JSON files.
type Store struct {
	Dir string
	mu  sync.Mutex
}

// DataDir is %APPDATA%\SelvaSync on Windows.
func DataDir() string {
	if d := os.Getenv("SELVASYNC_DATA"); d != "" {
		return d
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	return filepath.Join(base, "SelvaSync")
}

// NewStore creates the data folder if needed.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	_ = os.MkdirAll(filepath.Join(dir, "logs"), 0o700)
	_ = os.MkdirAll(filepath.Join(dir, "languages"), 0o700)
	return &Store{Dir: dir}, nil
}

func (s *Store) path(name string) string { return filepath.Join(s.Dir, name) }

func parseSettings(b []byte) (Settings, bool) {
	set := Defaults()
	var probe map[string]json.RawMessage
	if json.Unmarshal(b, &probe) != nil || probe == nil {
		return Settings{}, false
	}
	if _, ok := probe["schedule"]; !ok {
		if _, ok := probe["sources"]; !ok {
			return Settings{}, false // not a real settings file ({} / null)
		}
	}
	if json.Unmarshal(b, &set) != nil {
		return Settings{}, false
	}
	set.Normalize()
	return set, true
}

// LoadSettings returns saved settings. A damaged settings file is never
// overwritten silently: the backup copy is used, or the damaged file is kept
// aside and the status tells the app to warn the user.
func (s *Store) LoadSettings() (Settings, LoadStatus) {
	b, err := os.ReadFile(s.path("settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		if bb, err := os.ReadFile(s.path("settings.json.bak")); err == nil {
			if set, ok := parseSettings(bb); ok {
				return set, LoadRecovered
			}
		}
		set := Defaults()
		set.Normalize()
		return set, LoadNew
	}
	if set, ok := parseSettings(b); ok && err == nil {
		return set, LoadOK
	}
	// damaged: keep it for inspection, try the backup
	_ = os.WriteFile(s.path("settings.damaged-"+time.Now().Format("20060102-150405")+".json"), b, 0o600)
	if bb, err := os.ReadFile(s.path("settings.json.bak")); err == nil {
		if set, ok := parseSettings(bb); ok {
			return set, LoadRecovered
		}
	}
	set := Defaults()
	set.Normalize()
	return set, LoadReset
}

// SaveSettings writes settings safely and keeps the previous good copy.
func (s *Store) SaveSettings(set Settings) error {
	set.Normalize()
	b, err := json.MarshalIndent(set, "", " ")
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, err := os.ReadFile(s.path("settings.json")); err == nil {
		if _, ok := parseSettings(old); ok {
			_ = atomicWrite(s.path("settings.json.bak"), old)
		}
	}
	return atomicWrite(s.path("settings.json"), b)
}

// LoadState returns the saved sync state (an empty state if missing/damaged;
// the engine then rebuilds it from Drive without uploading twice).
func (s *Store) LoadState() *State {
	for _, name := range []string{"state.json", "state.json.tmp"} {
		b, err := os.ReadFile(s.path(name))
		if err != nil || len(b) == 0 {
			continue
		}
		st := &State{}
		if json.Unmarshal(b, st) == nil {
			st.fill()
			if st.Version < 2 {
				st.MigratedFromV1 = true
				// version 1.0 state: every file in it was uploaded by this
				// installation, so all of them count as seen here
				for k, fe := range st.Files {
					fe.Seen = true
					st.Files[k] = fe
				}
			}
			st.Version = StateVersion
			return st
		}
	}
	return NewState()
}

// SaveState writes the sync state atomically (compact JSON: it can be large).
func (s *Store) SaveState(st *State) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.WriteFile("state.json", b)
}

// ReadFile / WriteFile give raw access for small private files.
func (s *Store) ReadFile(name string) ([]byte, error) { return os.ReadFile(s.path(name)) }

func (s *Store) WriteFile(name string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return atomicWrite(s.path(name), data)
}

func (s *Store) Remove(name string) error {
	err := os.Remove(s.path(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// atomicWrite writes to a temp file, flushes it to disk and renames it, so a
// power cut never leaves a half-written file. Retries briefly if antivirus or
// a search indexer is holding the file.
func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	for i := 0; ; i++ {
		err = os.Rename(tmp, path)
		if err == nil || i >= 10 {
			return err
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TryLock takes an exclusive lock on the data folder so two copies of Selva
// Sync (for example the same Windows user signed in twice) never sync with
// the same saved state at the same time. The lock is released by the
// returned function, or automatically if the program ends.
func (s *Store) TryLock(name string) (unlock func(), ok bool) {
	f, err := os.OpenFile(s.path(name), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}, true // cannot create a lock file: do not block syncing
	}
	if !lockFile(f) {
		f.Close()
		return nil, false
	}
	return func() { f.Close() }, true
}

// StateStamp identifies the saved state file version (size + time), so the
// engine can tell whether another copy of the app changed it.
func (s *Store) StateStamp() string {
	st, err := os.Stat(s.path("state.json"))
	if err != nil {
		return ""
	}
	return st.ModTime().String() + "/" + strconv.FormatInt(st.Size(), 10)
}
