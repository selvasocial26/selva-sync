package app

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/selvasync/selvasync/internal/config"
	"github.com/selvasync/selvasync/internal/drive"
	"github.com/selvasync/selvasync/internal/engine"
	"github.com/selvasync/selvasync/internal/platform"
)

const cookieName = "selva_session"

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFS, "web")
	fileServer := http.FileServer(http.FS(static))

	mux.HandleFunc("/", a.handleIndex)
	mux.Handle("/static/", a.requireSession(http.StripPrefix("/static/", fileServer)))
	mux.HandleFunc("/i18n/", a.requireSessionFunc(a.handleI18n))
	mux.HandleFunc("/oauth/callback", a.handleCallback)
	mux.HandleFunc("/picked", a.handlePicked)

	api := map[string]http.HandlerFunc{
		"GET /api/ping":            func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]bool{"ok": true}) },
		"GET /api/state":           a.apiState,
		"GET /api/status":          a.apiStatus,
		"POST /api/settings":       a.apiSettings,
		"POST /api/sync":           a.apiSync,
		"POST /api/stop":           a.apiStop,
		"GET /api/browse":          a.apiBrowse,
		"GET /api/network":         a.apiNetwork,
		"POST /api/signin":         a.apiSignIn,
		"POST /api/signout":        a.apiSignOut,
		"POST /api/open":           a.apiOpen,
		"POST /api/quit":           a.apiQuit,
		"POST /api/uninstall":      a.apiUninstall,
		"POST /api/reload-langs":   a.apiReloadLangs,
		"GET /api/preview":         a.apiPreview,
		"POST /api/reset-excludes": a.apiResetExcludes,
		"POST /api/accept-disk":    a.apiAcceptDisk,
		"POST /api/picker/start":   a.apiPickerStart,
		"POST /api/picker/clear":   a.apiPickerClear,
		"POST /api/export-setup":   a.apiExportSetup,
	}
	for pattern, h := range api {
		method, path, _ := strings.Cut(pattern, " ")
		h := h
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != method {
				http.Error(w, "method not allowed", 405)
				return
			}
			if !a.validToken(r) {
				http.Error(w, "forbidden", 403)
				return
			}
			h(w, r)
		})
	}
	return a.secure(mux)
}

// secure adds protection headers and blocks requests that do not come from
// this computer's own Selva Sync window (DNS-rebinding protection).
func (a *App) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if host != fmt.Sprintf("127.0.0.1:%d", a.port) && host != fmt.Sprintf("localhost:%d", a.port) {
			http.Error(w, "forbidden", 403)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func eq(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

func (a *App) hasSession(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	return err == nil && eq(c.Value, a.token)
}

func (a *App) validToken(r *http.Request) bool {
	t := r.Header.Get("X-Selva-Token")
	return t != "" && eq(t, a.token)
}

func (a *App) requireSession(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.hasSession(r) {
			http.Error(w, "forbidden", 403)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func (a *App) requireSessionFunc(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.hasSession(r) {
			http.Error(w, "forbidden", 403)
			return
		}
		h(w, r)
	}
}

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if t := r.URL.Query().Get("t"); t != "" {
		if eq(t, a.token) {
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: a.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
	}
	if !a.hasSession(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(403)
		fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><title>Selva Sync</title><body style="font-family:Segoe UI,sans-serif;padding:40px">Please open Selva Sync from its icon in the taskbar notification area.</body>`)
		return
	}
	b, _ := webFS.ReadFile("web/index.html")
	page := strings.ReplaceAll(string(b), "{{TOKEN}}", a.token)
	page = strings.ReplaceAll(page, "{{LANG}}", html.EscapeString(a.lang()))
	page = strings.ReplaceAll(page, "{{VERSION}}", html.EscapeString(Version))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, page)
}

func (a *App) handleI18n(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/i18n/"), ".json")
	writeJSON(w, a.I18n.Strings(code))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, key string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": key})
}

func (a *App) callbackURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/oauth/callback", a.port)
}

// ---------- API ----------

func (a *App) aboutInfo() *drive.About {
	a.aboutMu.Lock()
	ab, at := a.about, a.aboutAt
	a.aboutMu.Unlock()
	if ab == nil && at.IsZero() && a.Auth.SignedIn() {
		// first time: fetch now so the window shows the account at once
		a.aboutMu.Lock()
		a.aboutAt = time.Now()
		a.aboutMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		if res, err := a.Drive.About(ctx); err == nil {
			a.aboutMu.Lock()
			a.about = res
			a.aboutMu.Unlock()
			return res
		}
		return nil
	}
	if time.Since(at) > 5*time.Minute && a.Auth.SignedIn() {
		a.aboutMu.Lock()
		a.aboutAt = time.Now() // avoid parallel refreshes
		a.aboutMu.Unlock()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if res, err := a.Drive.About(ctx); err == nil {
				a.aboutMu.Lock()
				a.about = res
				a.aboutMu.Unlock()
			}
		}()
	}
	return ab
}

// publicSettings hides the client secret from the window.
func publicSettings(set config.Settings) config.Settings {
	set.ClientSecret = ""
	return set
}

func (a *App) apiState(w http.ResponseWriter, r *http.Request) {
	set := a.Settings()
	last, lastSuccess, anchor, compID, monthUsage := a.Engine.StateInfo()
	next := NextRun(set.Schedule, anchor, time.Now())
	resp := map[string]any{
		"version":     Version,
		"author":      Author,
		"authorUrl":   AuthorURL,
		"os":          runtime.GOOS,
		"configured":  a.Auth.Configured(),
		"customCreds": set.ClientID != "",
		"signedIn":    a.Auth.SignedIn(),
		"settings":    publicSettings(set),
		"secretSet":   set.ClientSecret != "",
		"loadStatus":  string(a.LoadStatus()),
		"diskChanged": a.Engine.MismatchedSources(),
		"status":      a.Engine.Status(),
		"lastRun":     last,
		"lastSuccess": zeroNil(lastSuccess),
		"nextRun":     zeroNil(next),
		"monthUsage":  monthUsage,
		"languages":   a.I18n.Languages(),
		"dataDir":     a.Store.Dir,
		"hasPreview":  a.Engine.LastPreview() != nil,
		"redirectUri": a.callbackURL(),
	}
	a.featureState(resp, set)
	if compID != "" {
		resp["driveUrl"] = "https://drive.google.com/drive/folders/" + compID
	}
	if ab := a.aboutInfo(); ab != nil {
		limit, _ := strconv.ParseInt(ab.StorageQuota.Limit, 10, 64)
		usage, _ := strconv.ParseInt(ab.StorageQuota.Usage, 10, 64)
		resp["account"] = map[string]any{"email": ab.User.EmailAddress, "name": ab.User.DisplayName, "limit": limit, "usage": usage}
	}
	writeJSON(w, resp)
}

func zeroNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func (a *App) apiStatus(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	last, _, _, _, monthUsage := a.Engine.StateInfo()
	writeJSON(w, map[string]any{
		"status":     a.Engine.Status(),
		"logs":       a.Log.Since(since),
		"lastRun":    last,
		"signedIn":   a.Auth.SignedIn(),
		"monthUsage": monthUsage,
	})
}

func (a *App) apiSettings(w http.ResponseWriter, r *http.Request) {
	// Start from the current settings so fields left out are kept.
	cur := a.Settings()
	s := cur
	s.Sources, s.Excludes, s.BlockedWifi, s.AllowedWifi = nil, nil, nil, nil
	var raw map[string]json.RawMessage
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil || json.Unmarshal(body, &raw) != nil || raw == nil {
		writeErr(w, 400, "bad_request")
		return
	}
	if err := json.Unmarshal(body, &s); err != nil {
		writeErr(w, 400, "bad_request")
		return
	}
	for field, keep := range map[string]func(){
		"sources":     func() { s.Sources = cur.Sources },
		"excludes":    func() { s.Excludes = cur.Excludes },
		"blockedWifi": func() { s.BlockedWifi = cur.BlockedWifi },
		"allowedWifi": func() { s.AllowedWifi = cur.AllowedWifi },
	} {
		if v, ok := raw[field]; !ok || strings.TrimSpace(string(v)) == "null" {
			keep()
		}
	}
	// the window never receives the secret; empty means "unchanged"
	if s.ClientSecret == "" && s.ClientID == cur.ClientID {
		s.ClientSecret = cur.ClientSecret
	}
	// the backup folder is changed only through Google's folder chooser
	if err := a.saveSettings(s, true); err != nil {
		writeErr(w, 500, "save_failed")
		return
	}
	a.Log.Info("settings_saved", nil)
	writeJSON(w, map[string]any{"ok": true, "settings": publicSettings(a.Settings()), "secretSet": a.Settings().ClientSecret != ""})
}

func (a *App) apiSync(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DryRun    bool `json:"dryRun"`
		Force     bool `json:"force"`
		FullCheck bool `json:"fullCheck"`
	}
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req)
	if !a.Auth.SignedIn() {
		writeErr(w, 400, "err_not_signed_in")
		return
	}
	if !a.hasSources() {
		writeErr(w, 400, "err_no_folders")
		return
	}
	if err := a.Engine.Start(engine.Options{DryRun: req.DryRun, Force: req.Force, FullCheck: req.FullCheck, Trigger: "manual"}); err != nil {
		writeErr(w, 409, "err_busy")
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *App) apiStop(w http.ResponseWriter, r *http.Request) {
	a.Engine.Stop()
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *App) apiPreview(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, a.Engine.LastPreview())
}

type browseEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Kind string `json:"kind,omitempty"`
}

func (a *App) apiBrowse(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		var out []browseEntry
		home, _ := os.UserHomeDir()
		if home != "" {
			for _, n := range []string{"Desktop", "Documents", "Downloads", "Pictures", "Videos", "Music"} {
				fp := filepath.Join(home, n)
				if st, err := os.Stat(fp); err == nil && st.IsDir() {
					out = append(out, browseEntry{Name: n, Path: fp, Kind: "shortcut"})
				}
			}
		}
		for _, d := range platform.Drives() {
			name := d.Path
			if d.Label != "" {
				name = d.Label + " (" + strings.TrimRight(d.Path, `\`) + ")"
			}
			out = append(out, browseEntry{Name: name, Path: d.Path, Kind: d.Kind})
		}
		writeJSON(w, map[string]any{"path": "", "parent": nil, "entries": out})
		return
	}
	// Only local drive paths: no network shares (\\server\share) or device
	// paths, which could leak Windows credentials to another computer.
	p = config.CleanSourcePath(p)
	if p == "" {
		writeErr(w, 400, "err_cannot_open")
		return
	}
	type result struct {
		ents []os.DirEntry
		err  error
	}
	ch := make(chan result, 1)
	go func() { e, err := os.ReadDir(p); ch <- result{e, err} }()
	var ents []os.DirEntry
	select {
	case res := <-ch:
		if res.err != nil {
			writeErr(w, 400, "err_cannot_open")
			return
		}
		ents = res.ents
	case <-time.After(15 * time.Second): // disconnected network drive
		writeErr(w, 400, "err_cannot_open")
		return
	}
	out := []browseEntry{}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, "$") || n == "System Volume Information" {
			continue
		}
		if info, err := e.Info(); err == nil && platform.SkipAttr(info) {
			continue
		}
		out = append(out, browseEntry{Name: n, Path: filepath.Join(p, n)})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	var parent any
	if dp := filepath.Dir(p); dp != p {
		parent = dp
	} else {
		parent = ""
	}
	writeJSON(w, map[string]any{"path": p, "parent": parent, "entries": out})
}

func (a *App) apiNetwork(w http.ResponseWriter, r *http.Request) {
	ssid, known := platform.WifiSSID()
	writeJSON(w, map[string]any{"ssid": ssid, "ssidKnown": known, "metered": platform.Metered()})
}

func (a *App) apiSignIn(w http.ResponseWriter, r *http.Request) {
	u, err := a.Auth.BeginLogin(a.callbackURL())
	if err != nil {
		writeErr(w, 400, "err_no_client")
		return
	}
	platform.OpenURL(u)
	writeJSON(w, map[string]string{"url": u})
}

func (a *App) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	l := a.lang()
	page := func(ok bool, msg string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		color := "#1a7f4b"
		title := a.I18n.T(l, "signin_ok_title", nil)
		if !ok {
			color = "#b42318"
			title = a.I18n.T(l, "signin_fail_title", nil)
		}
		fmt.Fprintf(w, `<!doctype html><html lang="%s"><meta charset="utf-8"><title>Selva Sync</title><body style="font-family:'Segoe UI','Nirmala UI',sans-serif;display:grid;place-items:center;height:90vh;margin:0;background:#f6f7f9"><div style="background:#fff;padding:36px 44px;border-radius:14px;box-shadow:0 2px 12px #0001;max-width:460px;text-align:center"><h2 style="color:%s;margin-top:0">%s</h2><p>%s</p></div></body></html>`,
			html.EscapeString(l), color, html.EscapeString(title), html.EscapeString(msg))
	}
	if e := q.Get("error"); e != "" {
		page(false, a.I18n.T(l, "signin_cancelled", nil))
		return
	}
	if !a.Auth.ValidState(q.Get("state")) {
		page(false, a.I18n.T(l, "signin_expired", nil))
		return
	}
	// stop any sync of the previous account before switching
	a.Engine.Pause(45 * time.Second)
	defer a.Engine.Resume()
	if a.Engine.Status().Running {
		// still stopping (e.g. a slow network drive): keep the link usable
		page(false, a.I18n.T(l, "signin_busy", nil))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := a.Auth.FinishLogin(ctx, q.Get("state"), q.Get("code")); err != nil {
		a.Log.Error("error", map[string]string{"error": err.Error()})
		page(false, a.I18n.T(l, "signin_failed_d", nil))
		return
	}
	a.Engine.ForgetRemote()
	a.Engine.MarkScheduled(time.Now())
	if ab, err := a.Drive.About(ctx); err == nil {
		a.aboutMu.Lock()
		a.about, a.aboutAt = ab, time.Now()
		a.aboutMu.Unlock()
		a.Log.Info("signed_in", map[string]string{"email": ab.User.EmailAddress})
	}
	page(true, a.I18n.T(l, "signin_ok_text", nil))
}

func (a *App) apiSignOut(w http.ResponseWriter, r *http.Request) {
	a.Engine.Pause(60 * time.Second)
	defer a.Engine.Resume()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	a.Auth.SignOut(ctx)
	a.Engine.ForgetRemote()
	a.Log.Info("signed_out", nil)
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *App) apiOpen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		What string `json:"what"`
	}
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req)
	switch req.What {
	case "drive":
		_, _, _, compID, _ := a.Engine.StateInfo()
		if compID != "" {
			platform.OpenURL("https://drive.google.com/drive/folders/" + compID)
		} else {
			platform.OpenURL("https://drive.google.com/")
		}
	case "logs":
		platform.OpenFolder(filepath.Join(a.Store.Dir, "logs"))
	case "languages":
		platform.OpenFolder(filepath.Join(a.Store.Dir, "languages"))
	case "author":
		platform.OpenURL(AuthorURL)
	case "gcloud":
		platform.OpenURL("https://console.cloud.google.com/apis/credentials")
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *App) apiQuit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]bool{"ok": true})
	go func() { time.Sleep(300 * time.Millisecond); a.Quit() }()
}

func (a *App) apiUninstall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RemoveData bool `json:"removeData"`
	}
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req)
	writeJSON(w, map[string]bool{"ok": true})
	go func() {
		time.Sleep(300 * time.Millisecond)
		a.Engine.Pause(60 * time.Second)
		if req.RemoveData {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			a.Auth.SignOut(ctx)
			cancel()
		}
		a.Shutdown()
		platform.Uninstall(req.RemoveData, a.Store.Dir)
		os.Exit(0)
	}()
}

func (a *App) apiReloadLangs(w http.ResponseWriter, r *http.Request) {
	a.I18n.Reload()
	writeJSON(w, a.I18n.Languages())
}

func (a *App) apiResetExcludes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, config.DefaultExcludes)
}

func (a *App) apiAcceptDisk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req)
	p := config.CleanSourcePath(req.Path)
	if p == "" {
		writeErr(w, 400, "bad_request")
		return
	}
	a.Engine.AcceptDisk(p)
	writeJSON(w, map[string]bool{"ok": true})
}
