package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/selvasync/selvasync/internal/auth"
	"github.com/selvasync/selvasync/internal/config"
	"github.com/selvasync/selvasync/internal/engine"
	"github.com/selvasync/selvasync/internal/fakedrive"
	"github.com/selvasync/selvasync/internal/platform"
)

type tenv struct {
	a   *App
	fd  *fakedrive.Server
	src string
	dir string
}

func newT(t *testing.T, tokenDelay time.Duration, prep func(dir string)) *tenv {
	fd := fakedrive.New()
	t.Cleanup(fd.Close)
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(tokenDelay)
		json.NewEncoder(w).Encode(map[string]any{"access_token": "x", "expires_in": 3600})
	}))
	t.Cleanup(tok.Close)
	auth.TokenURL = tok.URL
	auth.RevokeURL = tok.URL
	dir := t.TempDir()
	src := filepath.Join(dir, "Documents")
	for i := 0; i < 5; i++ {
		p := filepath.Join(src, fmt.Sprintf("f%d.txt", i))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, bytes.Repeat([]byte("x"), 1000), 0o644)
	}
	DefaultClientID = "demo.apps.googleusercontent.com"
	data := filepath.Join(dir, "data")
	os.MkdirAll(data, 0o700)
	b, _ := json.Marshal(map[string]string{"refresh_token": "r", "client_id": DefaultClientID})
	if enc, err := platform.Protect(b); err == nil { // DPAPI on Windows
		b = enc
	}
	os.WriteFile(filepath.Join(data, "token.bin"), b, 0o600)
	if prep != nil {
		prep(data)
	}
	a, err := New(data)
	if err != nil {
		t.Fatal(err)
	}
	a.Drive.APIBase = fd.URL + "/drive/v3"
	a.Drive.UploadBase = fd.URL + "/upload/drive/v3"
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Shutdown)
	return &tenv{a: a, fd: fd, src: src, dir: data}
}

func (r *tenv) call(method, path string, body any) (int, string) {
	var rd io.Reader
	if body != nil {
		if s, ok := body.(string); ok {
			rd = strings.NewReader(s)
		} else {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
	}
	req, _ := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", r.a.port, path), rd)
	req.Header.Set("X-Selva-Token", r.a.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// M1: a slow Google token refresh must not freeze the tray or window.
func TestFixSlowTokenDoesNotFreezeUI(t *testing.T) {
	r := newT(t, 8*time.Second, nil)
	s := r.a.Settings()
	s.Sources = []config.Source{{Path: r.src, Enabled: true}}
	r.a.SaveSettings(s)
	r.a.Engine.Start(engine.Options{Trigger: "manual"})
	time.Sleep(300 * time.Millisecond)
	t0 := time.Now()
	r.a.TrayConfig(nil, false).Menu()
	code, _ := r.call("GET", "/api/status", nil)
	if d := time.Since(t0); d > time.Second || code != 200 {
		t.Fatalf("UI blocked for %v (code %d)", d, code)
	}
	r.a.Engine.StopAndWait(15 * time.Second)
}

// M3: adding the first folder must not start a sync right away.
func TestFixFirstFolderWaitsForSchedule(t *testing.T) {
	r := newT(t, 0, func(data string) {
		st := config.NewState()
		st.LastScheduled = time.Now().Add(-48 * time.Hour)
		b, _ := json.Marshal(st)
		os.WriteFile(filepath.Join(data, "state.json"), b, 0o600)
	})
	s := r.a.Settings()
	s.Schedule = config.Schedule{Mode: "daily", Time: time.Now().Add(3 * time.Hour).Format("15:04")}
	r.a.SaveSettings(s)
	s.Sources = []config.Source{{Path: r.src, Enabled: true}}
	r.a.SaveSettings(s)
	_, _, anchor, _, _ := r.a.Engine.StateInfo()
	if next := NextRun(r.a.Settings().Schedule, anchor, time.Now()); !next.After(time.Now()) {
		t.Fatalf("sync is due immediately after adding the first folder (next=%v)", next)
	}
}

// L1/L2: bad values through the API are cleaned, and fields left out are kept.
func TestFixSettingsValidation(t *testing.T) {
	r := newT(t, 0, nil)
	s := r.a.Settings()
	s.Excludes = []string{"*.bak"}
	s.Sources = []config.Source{{Path: r.src, Enabled: true}}
	r.a.SaveSettings(s)

	code, _ := r.call("POST", "/api/settings", map[string]any{
		"sources":      []map[string]any{{"path": "", "enabled": true}, {"path": "relative/dir", "enabled": true}, {"path": `\\evil\share`, "enabled": true}, {"path": r.src, "enabled": true}, {"path": r.src, "enabled": true}},
		"monthlyCapMB": 1 << 44, "dayLimitKBps": 1 << 40, "archiveDays": -5,
	})
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	got := r.a.Settings()
	if len(got.Sources) != 1 || got.Sources[0].Path != r.src {
		t.Errorf("sources not cleaned: %+v", got.Sources)
	}
	if got.MonthlyCapMB != config.MaxCapMB || got.DayLimitKBps != config.MaxSpeedKBps || got.ArchiveDays != 0 {
		t.Errorf("numbers not clamped: cap=%d speed=%d days=%d", got.MonthlyCapMB, got.DayLimitKBps, got.ArchiveDays)
	}
	if len(got.Excludes) != 1 || got.Excludes[0] != "*.bak" {
		t.Errorf("excludes lost when not sent: %v", got.Excludes)
	}
	// a partial update keeps everything else
	r.call("POST", "/api/settings", map[string]any{"language": "gu"})
	if g := r.a.Settings(); g.Language != "gu" || len(g.Sources) != 1 || g.MonthlyCapMB != config.MaxCapMB {
		t.Errorf("partial update reset fields: %+v", g)
	}
}

// L3: the client secret is never sent to the window.
func TestFixSecretNotExposed(t *testing.T) {
	r := newT(t, 0, nil)
	s := r.a.Settings()
	s.ClientID, s.ClientSecret = "x.apps.googleusercontent.com", "TOP-SECRET"
	r.a.SaveSettings(s)
	_, body := r.call("GET", "/api/state", nil)
	if strings.Contains(body, "TOP-SECRET") {
		t.Fatal("secret returned by /api/state")
	}
	// saving other settings keeps the secret
	r.call("POST", "/api/settings", map[string]any{"language": "hi", "clientId": "x.apps.googleusercontent.com", "clientSecret": ""})
	if r.a.Settings().ClientSecret != "TOP-SECRET" {
		t.Fatal("secret lost after saving")
	}
}

// H1: browsing network shares and relative paths is refused.
func TestFixBrowseRestricted(t *testing.T) {
	r := newT(t, 0, nil)
	for _, p := range []string{`\\attacker\share`, `//attacker/share`, "relative", `\\?\C:\`} {
		if code, _ := r.call("GET", "/api/browse?path="+urlq(p), nil); code == 200 {
			t.Errorf("browse allowed %q", p)
		}
	}
	if code, _ := r.call("GET", "/api/browse?path="+urlq(r.src), nil); code != 200 {
		t.Errorf("normal folder refused")
	}
}

func urlq(s string) string {
	return strings.NewReplacer(`\`, "%5C", "/", "%2F", "?", "%3F", ":", "%3A").Replace(s)
}

// C7: a damaged settings file is restored from backup, never silently wiped.
func TestFixDamagedSettingsRecovered(t *testing.T) {
	dir := t.TempDir()
	st, _ := config.NewStore(dir)
	set := config.Defaults()
	set.Sources = []config.Source{{ID: "a", Path: dir, Enabled: true}}
	set.MonthlyCapMB = 777
	st.SaveSettings(set)
	st.SaveSettings(set) // second save creates the .bak copy
	b, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	os.WriteFile(filepath.Join(dir, "settings.json"), b[:len(b)-40], 0o600) // torn write

	got, status := st.LoadSettings()
	if status != config.LoadRecovered || got.MonthlyCapMB != 777 || len(got.Sources) != 1 {
		t.Fatalf("not recovered: status=%q cap=%d sources=%d", status, got.MonthlyCapMB, len(got.Sources))
	}
	// no backup: defaults, but the damaged file is kept and the status says so
	os.Remove(filepath.Join(dir, "settings.json.bak"))
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{garbage"), 0o600)
	if _, status := st.LoadSettings(); status != config.LoadReset {
		t.Fatalf("status %q", status)
	}
	m, _ := filepath.Glob(filepath.Join(dir, "settings.damaged-*.json"))
	if len(m) == 0 {
		t.Fatal("damaged file not kept")
	}
}

// C4: signing out during a sync stops it first; nothing is duplicated.
func TestFixSignOutDuringSync(t *testing.T) {
	r := newT(t, 0, nil)
	for i := 0; i < 40; i++ {
		os.WriteFile(filepath.Join(r.src, fmt.Sprintf("g%02d.txt", i)), bytes.Repeat([]byte("y"), 2000), 0o644)
	}
	s := r.a.Settings()
	s.Sources = []config.Source{{Path: r.src, Enabled: true}}
	s.DayLimitKBps = 20 // slow, so the sync is still running
	s.NightLimitKBps = 20
	r.a.SaveSettings(s)
	r.a.Engine.Start(engine.Options{Trigger: "manual"})
	time.Sleep(700 * time.Millisecond)
	if code, _ := r.call("POST", "/api/signout", map[string]any{}); code != 200 {
		t.Fatal("signout failed")
	}
	if r.a.Engine.Status().Running {
		t.Fatal("sync still running after sign-out")
	}
	// sign in again (same fake account) and sync: no duplicates
	b, _ := json.Marshal(map[string]string{"refresh_token": "r", "client_id": DefaultClientID})
	os.WriteFile(filepath.Join(r.dir, "token.bin"), b, 0o600)
	r.a.Auth = &auth.Manager{Store: r.a.Store, Protect: func(b []byte) ([]byte, error) { return b, nil },
		Unprot: func(b []byte) ([]byte, error) { return b, nil }, Creds: r.a.creds, HTTP: http.DefaultClient}
	r.a.Drive.Token = r.a.Auth.Token
	s.DayLimitKBps, s.NightLimitKBps = 0, 0
	r.a.SaveSettings(s)
	sum := r.a.Engine.Run(t.Context(), engine.Options{})
	if sum.Result != "ok" {
		t.Fatalf("%+v", sum)
	}
	for p, fs := range r.fd.Live() {
		if len(fs) > 1 {
			t.Errorf("duplicate: %s", p)
		}
	}
}

// M8: daylight-saving changes never make the schedule repeat.
func TestFixNextRunDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tz data")
	}
	sched := config.Schedule{Mode: "daily", Time: "02:30"}
	// spring forward 2026-03-08: 02:30 does not exist
	anchor := time.Date(2026, 3, 8, 3, 31, 0, 0, loc) // the run that just happened
	for m := 0; m < 24*60; m += 7 {
		now := anchor.Add(time.Duration(m) * time.Minute)
		next := NextRun(sched, anchor, now)
		if !next.After(anchor) {
			t.Fatalf("at %v next=%v is not after the last run", now, next)
		}
		if now.Before(time.Date(2026, 3, 9, 2, 0, 0, 0, loc)) && !next.After(now) {
			t.Fatalf("due again at %v (next=%v)", now, next)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	if CompareVersions("1.1.0", "1.0.9") != 1 || CompareVersions("1.0", "1.0.0") != 0 || CompareVersions("0.9", "1.0") != -1 {
		t.Fatal("bad comparison")
	}
}

// Round 2: junk Windows paths are refused, real ones kept.
func TestFixWindowsPathRules(t *testing.T) {
	bad := []string{`C:\data.`, `C:\data. .`, `C:\data::$INDEX_ALLOCATION`, `C:\CON`, `C:\data\NUL`, `C:\nul.txt`,
		"C:\\da\x00ta", `C:\\?\C:`, `C:\a\..\b`, `\\server\share`, `\\?\C:\x`, `C:relative`, "", "relative"}
	for _, p := range bad {
		if got := config.CleanSourcePath(p); got != "" {
			t.Errorf("accepted %q as %q", p, got)
		}
	}
	good := map[string]string{`c:\Data`: `C:\Data`, `D:`: `D:\`, `E:\Clients\Windows`: `E:\Clients\Windows`, `C:/x/`: `C:\x`, `C:\ગુજરાતી\ફાઇલ`: `C:\ગુજરાતી\ફાઇલ`}
	for in, want := range good {
		if got := config.CleanSourcePath(in); got != want {
			t.Errorf("%q -> %q want %q", in, got, want)
		}
	}
}
