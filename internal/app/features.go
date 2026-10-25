package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/selvasync/selvasync/internal/config"
	"github.com/selvasync/selvasync/internal/engine"
	"github.com/selvasync/selvasync/internal/platform"
)

// DefaultPickerURL is the web page with Google's folder chooser (set at
// build time, in Settings > Advanced, or in the office setup file).
var DefaultPickerURL = ""

// AlertIcon is the tray icon shown when backups are overdue (set by main).
var AlertIcon, NormalIcon []byte

// ---------- office setup file ----------

// SetupFileName is the organisation's setup file. Placed next to
// SelvaSync.exe (or in the data folder) it provides first-run settings,
// and any settings listed in "managed" are applied at every start and
// cannot be changed in the app.
const SetupFileName = "selvasync-setup.json"

// managedKeys are the settings an organisation may fix.
var managedKeys = map[string]bool{
	"schedule": true, "location": true, "dayLimitKBps": true, "nightLimitKBps": true, "nightStart": true, "nightEnd": true,
	"monthlyCapMB": true, "pauseOnMetered": true, "blockedWifi": true, "allowedWifi": true, "excludes": true,
	"maxFileSizeMB": true, "deleteMode": true, "keepOldVersions": true, "archiveDays": true, "onlyOnAC": true,
	"alertDays": true, "reports": true, "driveFolderName": true, "parallel": true, "syncOnStartup": true,
	"startWithWindows": true, "notifications": true, "pickerUrl": true,
}

// never taken from a setup file: they belong to each person / PC
var personalKeys = map[string]bool{"sources": true, "language": true, "computerName": true, "clientId": true, "clientSecret": true}

func (a *App) setupFilePath() string {
	exe, _ := os.Executable()
	for _, p := range []string{filepath.Join(filepath.Dir(exe), SetupFileName), filepath.Join(a.Store.Dir, SetupFileName)} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// applySetup merges an organisation setup file into s. firstRun applies
// every key; later starts apply only the managed keys.
func applySetup(s config.Settings, raw []byte, firstRun bool) (config.Settings, bool) {
	var file map[string]json.RawMessage
	if json.Unmarshal(raw, &file) != nil {
		return s, false
	}
	var managed []string
	if m, ok := file["managed"]; ok {
		json.Unmarshal(m, &managed)
	}
	isManaged := map[string]bool{}
	for _, k := range managed {
		if managedKeys[k] {
			isManaged[k] = true
		}
	}
	out := s
	changed := false
	for k, v := range file {
		if k == "managed" || personalKeys[k] || !managedKeys[k] {
			continue
		}
		if firstRun || isManaged[k] {
			// one key at a time, so one wrong value does not spoil the rest
			try := out
			cloneLists(&try) // never write into the caller's lists
			if k == "location" {
				try.Location = config.Location{} // replace, never mix two folders
			}
			if json.Unmarshal([]byte(`{"`+k+`":`+string(v)+`}`), &try) != nil {
				badSetupKeys = append(badSetupKeys, k)
				continue
			}
			out = try
			changed = true
		}
	}
	var keep []string
	for k := range isManaged {
		keep = append(keep, k)
	}
	sort.Strings(keep)
	out.Sources, out.Language, out.ComputerName, out.ClientID, out.ClientSecret = s.Sources, s.Language, s.ComputerName, s.ClientID, s.ClientSecret
	out.Managed = keep
	out.Normalize()
	old := append([]string(nil), s.Managed...)
	sort.Strings(old)
	return out, changed || strings.Join(keep, ",") != strings.Join(old, ",")
}

// loadSetupFile applies the organisation setup file, if any.
func (a *App) loadSetupFile(firstRun bool) {
	p := a.setupFilePath()
	if p == "" {
		if len(a.settings.Managed) > 0 {
			// the organisation removed its setup file: unlock the settings
			a.settings.Managed = nil
			a.Store.SaveSettings(a.settings)
		}
		return
	}
	raw, err := os.ReadFile(p)
	if err != nil || len(raw) > 256<<10 {
		return
	}
	badSetupKeys = nil
	s, ok := applySetup(a.settings, raw, firstRun)
	if ok {
		a.settings = s
		a.Store.SaveSettings(s)
	}
	a.setupProblem = !json.Valid(raw) || len(badSetupKeys) > 0
	a.setupBadKeys = strings.Join(badSetupKeys, ", ")
}

// badSetupKeys collects setup-file keys whose values had the wrong type.
var badSetupKeys []string

// ExportSetup returns settings an admin can share with other PCs.
func ExportSetup(s config.Settings) []byte {
	cur, _ := json.Marshal(s)
	var m map[string]any
	json.Unmarshal(cur, &m)
	out := map[string]any{
		"_comment": "Selva Sync office setup file. Put it next to SelvaSync.exe on each PC. Settings listed in 'managed' cannot be changed on the PC.",
		"managed":  []string{},
	}
	for k, v := range m {
		if managedKeys[k] {
			out[k] = v
		}
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return b
}

// keepManaged puts organisation-fixed values back into settings sent by
// the window.
func keepManaged(next, cur config.Settings) config.Settings {
	next.Managed = cur.Managed // only the setup file can change this list
	if len(cur.Managed) == 0 {
		return next
	}
	nb, _ := json.Marshal(next)
	cb, _ := json.Marshal(cur)
	var nm, cm map[string]json.RawMessage
	json.Unmarshal(nb, &nm)
	json.Unmarshal(cb, &cm)
	for _, k := range cur.Managed {
		if v, ok := cm[k]; ok {
			nm[k] = v
		} else {
			delete(nm, k) // empty value ("omitempty"): keep it empty
		}
	}
	b, _ := json.Marshal(nm)
	var out config.Settings // start from zero so removed keys stay empty
	json.Unmarshal(b, &out)
	out.Managed = cur.Managed
	return out
}

// ---------- overdue alert ----------

// OverdueDays returns how many days have passed without a successful
// backup, if that is more than the alert setting (else 0).
func (a *App) OverdueDays(now time.Time) int {
	set := a.Settings()
	if set.AlertDays <= 0 || !a.Auth.SignedIn() || !a.hasSources() {
		return 0
	}
	lastSuccess, lastPartial, firstAttempt := a.Engine.AlertTimes()
	// a run that backed up files but had a few locked files also counts
	since := lastSuccess
	if lastPartial.After(since) {
		since = lastPartial
	}
	if since.IsZero() {
		since = firstAttempt // never succeeded: count from the first attempt
	}
	if since.IsZero() {
		since = a.started
	}
	days := int(now.Sub(since.Round(0)).Hours() / 24)
	if days >= set.AlertDays {
		return days
	}
	return 0
}

func (a *App) alertWatcher() {
	alerting := false
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	check := func() {
		d := a.OverdueDays(time.Now())
		if d > 0 != alerting {
			alerting = d > 0
			if alerting && AlertIcon != nil {
				platform.Current().SetIcon(AlertIcon)
			} else if !alerting && NormalIcon != nil {
				platform.Current().SetIcon(NormalIcon)
			}
		}
		if d > 0 && a.Settings().Notifications && a.Engine.AlertDue(time.Now()) {
			a.notify("notify_overdue", map[string]string{"days": fmt.Sprint(d)})
		}
	}
	time.Sleep(2 * time.Minute)
	check()
	for range t.C {
		if a.quitting.Load() {
			return
		}
		check()
	}
}

// ---------- monthly report ----------

// reportText writes the monthly report in the user's language.
func (a *App) reportText(month string, st config.MonthStats, set config.Settings) string {
	l := set.Language
	T := func(k string, args map[string]string) string { return a.I18n.T(l, k, args) }
	loc := "My Drive › " + set.DriveFolderName
	if set.Location.FolderID != "" {
		loc = set.Location.Name
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\r\n%s\r\n\r\n", T("report_title", map[string]string{"month": month}), strings.Repeat("=", 40))
	fmt.Fprintf(&b, "%s: %s\r\n", T("report_computer", nil), set.ComputerName)
	fmt.Fprintf(&b, "%s: %s\r\n\r\n", T("report_location", nil), loc)
	fmt.Fprintf(&b, "%s: %d (%d %s, %d %s, %d %s)\r\n", T("report_runs", nil), st.Runs,
		st.OK, T("report_ok", nil), st.Problems, T("report_problems", nil), st.Failed, T("report_failed", nil))
	fmt.Fprintf(&b, "%s: %d\r\n%s: %d\r\n%s: %d\r\n%s: %d\r\n%s: %s\r\n\r\n",
		T("report_uploaded", nil), st.Uploaded, T("report_updated", nil), st.Updated,
		T("report_moved", nil), st.Moved, T("report_archived", nil), st.Archived,
		T("report_data", nil), engine.HumanBytes(st.Bytes))
	fmt.Fprintf(&b, "%s:\r\n", T("report_folders", nil))
	for _, s := range set.Sources {
		state := "✓"
		if !s.Enabled {
			state = "✗"
		}
		fmt.Fprintf(&b, "  %s %s\r\n", state, s.Path)
	}
	if len(st.Errors) > 0 {
		fmt.Fprintf(&b, "\r\n%s:\r\n", T("report_errors", nil))
		for _, e := range st.Errors {
			fmt.Fprintf(&b, "  - %s\r\n", e)
		}
	} else {
		fmt.Fprintf(&b, "\r\n%s\r\n", T("report_no_errors", nil))
	}
	fmt.Fprintf(&b, "\r\n-- Selva Sync %s\r\n", Version)
	return b.String()
}

func cloneLists(s *config.Settings) {
	s.Sources = append([]config.Source(nil), s.Sources...)
	s.Excludes = append([]string(nil), s.Excludes...)
	s.BlockedWifi = append([]string(nil), s.BlockedWifi...)
	s.AllowedWifi = append([]string(nil), s.AllowedWifi...)
	s.Managed = append([]string(nil), s.Managed...)
}
