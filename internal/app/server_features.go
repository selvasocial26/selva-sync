package app

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/selvasync/selvasync/internal/config"
	"github.com/selvasync/selvasync/internal/drive"
	"github.com/selvasync/selvasync/internal/engine"
	"github.com/selvasync/selvasync/internal/platform"
)

// ---------- choosing the backup folder (Google Picker) ----------

type pickerState struct {
	mu     sync.Mutex
	value  string
	expiry time.Time
}

var driveIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{5,200}$`)

// isManaged reports whether the organisation fixed this setting.
func isManaged(s config.Settings, key string) bool {
	for _, k := range s.Managed {
		if k == key {
			return true
		}
	}
	return false
}

func (a *App) apiPickerStart(w http.ResponseWriter, r *http.Request) {
	set := a.Settings()
	if isManaged(set, "location") && set.Location.FolderID == "" {
		writeErr(w, 403, "err_managed")
		return
	}
	// (with a managed folder the chooser is only used to give this PC
	// access to the organisation's folder: drive.file needs that once)
	if !a.Auth.SignedIn() {
		writeErr(w, 400, "err_not_signed_in")
		return
	}
	pu := a.pickerURL()
	if pu == "" {
		writeErr(w, 400, "err_no_picker")
		return
	}
	st := randHex(16)
	a.picker.mu.Lock()
	a.picker.value, a.picker.expiry = st, time.Now().Add(15*time.Minute)
	a.picker.mu.Unlock()
	v := url.Values{}
	v.Set("state", st)
	v.Set("cb", fmt.Sprintf("http://127.0.0.1:%d/picked", a.port))
	v.Set("lang", set.Language)
	if ab := a.aboutInfo(); ab != nil && ab.User.EmailAddress != "" {
		v.Set("hint", ab.User.EmailAddress)
	}
	// the details go after '#', so they are never sent to the web server
	u := strings.SplitN(pu, "#", 2)[0] + "#" + v.Encode()
	platform.OpenURL(u)
	writeJSON(w, map[string]string{"url": u})
}

// takePickerState checks and uses up the one-time picker key.
func (a *App) takePickerState(got string) bool {
	a.picker.mu.Lock()
	defer a.picker.mu.Unlock()
	ok := got != "" && a.picker.value != "" && time.Now().Before(a.picker.expiry) && eq(got, a.picker.value)
	if ok {
		a.picker.value = ""
	}
	return ok
}

// cleanLabel keeps a name safe for display.
func cleanLabel(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '‮' || r == '‭' || r == '⁦' || r == '⁧' || r == '⁨' || r == '⁩' {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "…"
	}
	return s
}

func (a *App) handlePicked(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	l := a.lang()
	page := func(ok bool, msg string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		color, title := "#1a7f4b", a.I18n.T(l, "picked_ok_title", nil)
		if !ok {
			color, title = "#b42318", a.I18n.T(l, "picked_fail_title", nil)
		}
		fmt.Fprintf(w, `<!doctype html><html lang="%s"><meta charset="utf-8"><title>Selva Sync</title><body style="font-family:'Segoe UI','Nirmala UI',sans-serif;display:grid;place-items:center;height:90vh;margin:0;background:#f6f7f9"><div style="background:#fff;padding:36px 44px;border-radius:14px;box-shadow:0 2px 12px #0001;max-width:480px;text-align:center"><h2 style="color:%s;margin-top:0">%s</h2><p>%s</p></div></body></html>`,
			html.EscapeString(l), color, html.EscapeString(title), html.EscapeString(msg))
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !a.takePickerState(q.Get("state")) {
		page(false, a.I18n.T(l, "picked_expired", nil))
		return
	}
	if q.Get("cancel") != "" {
		page(false, a.I18n.T(l, "picked_cancelled", nil))
		return
	}
	id := q.Get("id")
	if !driveIDRe.MatchString(id) {
		page(false, a.I18n.T(l, "picked_bad", nil))
		return
	}
	cur := a.Settings()
	managedLoc := isManaged(cur, "location")
	if managedLoc && (cur.Location.FolderID == "" || id != cur.Location.FolderID) {
		page(false, a.I18n.T(l, "picked_wrong_managed", map[string]string{"name": cur.Location.Name}))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	loc, key := a.checkLocation(ctx, id, q.Get("dn"))
	if key != "" {
		page(false, a.I18n.T(l, key, nil))
		return
	}
	if managedLoc {
		// access given; the organisation's settings stay as they are
		a.Log.Info("location_granted", map[string]string{"name": cur.Location.Name})
		if a.hasSources() {
			a.Engine.Start(engine.Options{Trigger: "manual"}) // try again now
		}
		page(true, a.I18n.T(l, "picked_granted", map[string]string{"name": cur.Location.Name}))
		return
	}
	set := a.Settings()
	set.Location = loc
	if err := a.SaveSettings(set); err != nil {
		page(false, a.I18n.T(l, "save_failed", nil))
		return
	}
	page(true, a.I18n.T(l, "picked_ok_text", map[string]string{"name": loc.Name}))
}

// checkLocation makes sure Selva Sync can really save into the folder.
// It returns an error message key when it cannot.
func (a *App) checkLocation(ctx context.Context, id, driveName string) (config.Location, string) {
	f, err := a.Drive.Get(ctx, id)
	switch {
	case err != nil && (drive.IsNotFound(err) || drive.IsForbidden(err)):
		return config.Location{}, "picked_no_access"
	case err != nil:
		return config.Location{}, "picked_net"
	case !f.IsFolder() || f.Trashed:
		return config.Location{}, "picked_not_folder"
	case f.Capabilities != nil && !f.Capabilities.CanAddChildren:
		return config.Location{}, "picked_read_only"
	}
	if err := a.Engine.CheckLocation(ctx, f); errors.Is(err, engine.ErrLocationInside) {
		return config.Location{}, "picked_inside"
	} else if err != nil {
		return config.Location{}, "picked_net"
	}
	name := cleanLabel(f.Name, 80)
	if f.DriveID != "" {
		dn := cleanLabel(driveName, 60)
		if dn == "" {
			dn = "Shared drive"
		}
		if f.ID == f.DriveID {
			name = dn
		} else {
			name = dn + " › " + name
		}
	} else {
		name = "My Drive › " + name
	}
	return config.Location{FolderID: f.ID, Name: name, DriveID: f.DriveID}, ""
}

// apiPickerClear goes back to the default folder (My Drive › Selva Sync).
func (a *App) apiPickerClear(w http.ResponseWriter, r *http.Request) {
	set := a.Settings()
	if isManaged(set, "location") {
		writeErr(w, 403, "err_managed")
		return
	}
	set.Location = config.Location{}
	if err := a.SaveSettings(set); err != nil {
		writeErr(w, 500, "save_failed")
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// ---------- office setup file ----------

func (a *App) apiExportSetup(w http.ResponseWriter, r *http.Request) {
	b := ExportSetup(a.Settings())
	dir := ""
	if home, err := os.UserHomeDir(); err == nil {
		for _, d := range []string{"Downloads", "Desktop"} {
			if st, err := os.Stat(filepath.Join(home, d)); err == nil && st.IsDir() {
				// own folder, so the copy of SelvaSync.exe in Downloads does
				// not pick the file up by itself
				dir = filepath.Join(home, d, "Selva Sync office setup")
				if os.Mkdir(dir, 0o755) != nil {
					if st, err := os.Lstat(dir); err != nil || !st.IsDir() {
						dir = ""
						continue
					}
				}
				break
			}
		}
	}
	if dir == "" {
		dir = a.Store.Dir
	}
	p := filepath.Join(dir, SetupFileName)
	for i := 2; i < 100; i++ {
		if _, err := os.Lstat(p); os.IsNotExist(err) {
			break
		}
		p = filepath.Join(dir, fmt.Sprintf("selvasync-setup (%d).json", i))
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		writeErr(w, 500, "save_failed")
		return
	}
	_, werr := f.Write(b)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(p)
		writeErr(w, 500, "save_failed")
		return
	}
	platform.OpenFolder(dir)
	writeJSON(w, map[string]string{"path": p})
}

// featureState adds the v1.2 fields to /api/state.
func (a *App) featureState(resp map[string]any, set config.Settings) {
	resp["locationLabel"] = locationLabel(set)
	resp["pickerAvailable"] = a.pickerURL() != ""
	resp["overdueDays"] = a.OverdueDays(time.Now())
	resp["onBattery"] = platform.OnBattery()
	resp["setupFile"] = a.setupFilePath() != ""
	if a.setupProblem {
		resp["setupProblem"] = a.setupBadKeys
	}
}
