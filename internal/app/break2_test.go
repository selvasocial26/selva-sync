package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/selvasync/selvasync/internal/auth"
	"github.com/selvasync/selvasync/internal/config"
	"github.com/selvasync/selvasync/internal/engine"
)

// helper: a token endpoint that answers code exchanges after codeDelay.
func codeServer(t *testing.T, codeDelay time.Duration) {
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("grant_type") == "authorization_code" {
			time.Sleep(codeDelay)
			json.NewEncoder(w).Encode(map[string]any{"access_token": "x2", "refresh_token": "r2", "expires_in": 3600})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "x", "expires_in": 3600})
	}))
	t.Cleanup(tok.Close)
	auth.TokenURL = tok.URL
	auth.RevokeURL = tok.URL
}

func beginState(t *testing.T, r *tenv) string {
	u, err := r.a.Auth.BeginLogin(r.a.callbackURL())
	if err != nil {
		t.Fatal(err)
	}
	pu, _ := url.Parse(u)
	return pu.Query().Get("state")
}

func getCallback(r *tenv, state string) (int, string) {
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/oauth/callback?state=%s&code=abc", r.a.port, state))
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b := make([]byte, 4000)
	n, _ := resp.Body.Read(b)
	return resp.StatusCode, string(b[:n])
}

// B2-1: the sign-in timeout starts BEFORE waiting up to 30 s for the old sync
// to stop, so a slow stop makes the token exchange fail with an English
// "context deadline exceeded" and burns the one-time code.
func TestB2CallbackDeadlineEatenByStop(t *testing.T) {
	if os.Getenv("B2_SLOW") == "" {
		t.Skip("B2_SLOW=1 (takes ~31 s)")
	}
	r := newT(t, 0, nil)
	codeServer(t, 0)
	s := r.a.Settings()
	s.Sources = []config.Source{{Path: r.src, Enabled: true}}
	r.a.SaveSettings(s)
	// Simulate a run that needs >30 s to wind down (e.g. stuck on a
	// disconnected network drive): its completion is delayed.
	r.a.Engine.Start(engine.Options{Trigger: "manual", OnDone: func(config.RunSummary) { time.Sleep(32 * time.Second) }})
	state := beginState(t, r)
	t0 := time.Now()
	code, body := getCallback(r, state)
	t.Logf("callback after %v: %d %s", time.Since(t0).Round(time.Second), code, body[strings.Index(body, "<h2"):])
	if strings.Contains(body, "deadline exceeded") {
		t.Errorf("CONFIRMED: sign-in failed because ctx expired during StopAndWait")
	}
	if r.a.Auth.ValidState(state) {
		t.Log("state still valid")
	} else {
		t.Log("state consumed: user must restart sign-in")
	}
}

// B2-2: a duplicate callback (browser reload / double request) clears the
// shared authBusy flag while the first callback is still signing in, so a
// sync can start in the middle of the account switch.
func TestB2DoubleCallbackClearsBusy(t *testing.T) {
	r := newT(t, 0, nil)
	codeServer(t, 3*time.Second)
	s := r.a.Settings()
	s.Sources = []config.Source{{Path: r.src, Enabled: true}}
	r.a.SaveSettings(s)
	state := beginState(t, r)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); getCallback(r, state) }()
	time.Sleep(300 * time.Millisecond)
	if !r.a.Engine.Paused() {
		t.Fatal("busy not set by first callback")
	}
	c2, b2 := getCallback(r, state) // duplicate
	t.Logf("second callback: %d %q", c2, b2[strings.Index(b2, "<p>"):])
	busy := r.a.Engine.Paused()
	code, body := r.call("POST", "/api/sync", map[string]any{})
	t.Logf("while first callback still exchanging code: authBusy=%v, /api/sync -> %d %s", busy, code, body)
	if !busy && code == 200 {
		t.Errorf("CONFIRMED: sync started during sign-in after duplicate callback")
	}
	wg.Wait()
	r.a.Engine.StopAndWait(10 * time.Second)
}

// B2-3: JSON type confusion / odd payloads on /api/settings.
func TestB2SettingsFuzz(t *testing.T) {
	r := newT(t, 0, nil)
	s := r.a.Settings()
	s.Sources = []config.Source{{Path: r.src, Enabled: true}}
	s.Excludes = []string{"*.bak"}
	r.a.SaveSettings(s)
	payloads := []string{
		`null`, `[]`, `"x"`, `{}`, `{"sources":null}`, `{"sources":[null]}`, `{"excludes":null}`,
		`{"schedule":null}`, `{"schedule":{"mode":"weekly","weekday":"3"}}`, `{"parallel":1e30}`,
		`{"language":"zz"}`, `{"language":null}`, `{"sources":{}}`, `{"computerName":"..","driveFolderName":"."}`,
		`{"nightStart":"24:00","schedule":{"time":"7:5"}}`, `{"clientId":" "}`,
		`{"sources":[{"path":"C:\\x","enabled":true},{"path":"c:/x/","enabled":true}]}`,
		`{"excludes":["*","**"]}`,
	}
	for _, p := range payloads {
		before := r.a.Settings()
		code, body := r.call("POST", "/api/settings", p)
		after := r.a.Settings()
		if len(body) > 120 {
			body = body[:120]
		}
		t.Logf("%-70s -> %d src %d->%d excl %d->%d lang=%s name=%q root=%q sched=%+v %s", p, code, len(before.Sources), len(after.Sources), len(before.Excludes), len(after.Excludes), after.Language, after.ComputerName, after.DriveFolderName, after.Schedule, strings.TrimSpace(body))
		r.a.SaveSettings(s)
	}
}

// B2-4: settings.json that is valid JSON but not an object.
func TestB2SettingsNullFile(t *testing.T) {
	for _, content := range []string{"null", "[]", "{}", "\"\"", "0"} {
		dir := t.TempDir()
		st, _ := config.NewStore(dir)
		good := config.Defaults()
		good.Sources = []config.Source{{ID: "a", Path: "/tmp", Enabled: true}}
		st.SaveSettings(good)
		st.SaveSettings(good) // .bak now good too
		os.WriteFile(filepath.Join(dir, "settings.json"), []byte(content), 0o600)
		got, status := st.LoadSettings()
		t.Logf("settings.json=%-5q -> status=%q sources=%d", content, status, len(got.Sources))
		if len(got.Sources) == 0 && status == config.LoadOK {
			t.Errorf("CONFIRMED: %q silently loads defaults (sources lost, no warning)", content)
		}
		st.SaveSettings(got)
		bb, _ := os.ReadFile(filepath.Join(dir, "settings.json.bak"))
		if !strings.Contains(string(bb), "/tmp") {
			t.Logf("   ...and the good .bak was overwritten by the next save: %q", string(bb)[:min(len(bb), 20)])
		}
	}
}

// B2-5: NextRun property check across DST zones.
func TestB2NextRunProperty(t *testing.T) {
	zones := []string{"Asia/Kolkata", "Europe/London", "America/Santiago", "America/Havana", "Australia/Lord_Howe", "America/New_York"}
	bad := 0
	for _, z := range zones {
		loc, err := time.LoadLocation(z)
		if err != nil {
			continue
		}
		for _, mode := range []string{"daily", "weekly"} {
			for _, hm := range []string{"00:00", "00:30", "01:30", "02:30", "23:59", "21:00"} {
				for wd := 0; wd < 7; wd += 3 {
					sch := config.Schedule{Mode: mode, Time: hm, Weekday: wd, EveryHours: 6}
					start := time.Date(2026, 1, 1, 0, 0, 0, 0, loc)
					anchor := start
					now := start
					// simulate scheduler: tick every 30 min for a year
					runs := 0
					for now.Before(start.AddDate(1, 0, 0)) {
						next := NextRun(sch, anchor, now)
						if !now.Before(next) {
							if next.Before(anchor) || next.Equal(anchor) {
								if bad < 10 {
									t.Errorf("%s %s %s wd%d: next %v <= anchor %v", z, mode, hm, wd, next, anchor)
								}
								bad++
							}
							anchor = now
							runs++
						}
						now = now.Add(30 * time.Minute)
					}
					want := 365
					if mode == "weekly" {
						want = 52
					}
					if runs < want-1 || runs > want+1 {
						if bad < 10 {
							t.Errorf("%s %s %s wd%d: %d runs in a year (want ~%d)", z, mode, hm, wd, runs, want)
						}
						bad++
					}
				}
			}
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// B2-2b: two callbacks for the same sign-in that arrive while a sync is
// still stopping: both pass ValidState, the loser clears authBusy while the
// winner is still exchanging the code.
func TestB2DoubleCallbackDuringStop(t *testing.T) {
	r := newT(t, 0, nil)
	codeServer(t, 3*time.Second)
	s := r.a.Settings()
	s.Sources = []config.Source{{Path: r.src, Enabled: true}}
	r.a.SaveSettings(s)
	r.a.Engine.Start(engine.Options{Trigger: "manual", OnDone: func(config.RunSummary) { time.Sleep(2 * time.Second) }})
	time.Sleep(100 * time.Millisecond)
	state := beginState(t, r)
	var wg sync.WaitGroup
	res := make([]string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _, b := getCallback(r, state); res[i] = b[strings.Index(b, "<h2"):] }(i)
	}
	// stop finishes at ~2 s; winner then waits 3 s for Google; loser returns at once
	time.Sleep(2500 * time.Millisecond)
	busy := r.a.Engine.Paused()
	code, body := r.call("POST", "/api/sync", map[string]any{})
	t.Logf("t=2.5s (winner still exchanging code): authBusy=%v /api/sync -> %d %s", busy, code, strings.TrimSpace(body))
	wg.Wait()
	for _, x := range res {
		t.Logf("callback page: %.160s", x)
	}
	if !busy && code == 200 {
		t.Errorf("CONFIRMED: sync started in the middle of sign-in")
	}
	r.a.Engine.StopAndWait(10 * time.Second)
	last, _, _, _, _ := r.a.Engine.StateInfo()
	if last != nil {
		t.Logf("last run result: %s %s", last.Result, last.Reason)
	}
}

func TestB2SantiagoRepeat(t *testing.T) {
	loc, _ := time.LoadLocation("America/Santiago")
	sch := config.Schedule{Mode: "daily", Time: "00:00"}
	anchor := time.Date(2026, 9, 5, 0, 0, 30, 0, loc)
	lastAttempt := anchor
	for now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc); now.Before(time.Date(2026, 9, 7, 12, 0, 0, 0, loc)); now = now.Add(30 * time.Second) {
		next := NextRun(sch, anchor, now)
		if !now.Before(next) && now.Sub(lastAttempt) >= 15*time.Minute {
			t.Logf("scheduled sync starts at %v (next was %v)", now, next)
			anchor, lastAttempt = now, now
		}
	}
}
