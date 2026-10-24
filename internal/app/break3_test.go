package app

// Round-3 adversarial tests. Each TestBreak3_* FAILS when the problem exists.

import (
	"testing"
	"time"

	"github.com/selvasync/selvasync/internal/config"
	"github.com/selvasync/selvasync/internal/engine"
)

// NextRun: simulate the scheduler (1-min ticks) for a year, with the PC off
// for 3 days in the middle. Daily: exactly one run per local date while on,
// one catch-up run at wake, never two on one date. Weekly: once per week,
// on the right weekday (except the catch-up).
func TestBreak3_NextRunYearSimulation(t *testing.T) {
	zones := []string{"Asia/Kolkata", "America/Santiago", "Europe/London", "Australia/Lord_Howe", "America/Havana"}
	bad := 0
	report := func(f string, a ...any) {
		if bad < 15 {
			t.Errorf(f, a...)
		}
		bad++
	}
	for _, z := range zones {
		loc, err := time.LoadLocation(z)
		if err != nil {
			t.Skip(err)
		}
		for _, mode := range []string{"daily", "weekly", "interval"} {
			for _, hm := range []string{"00:00", "00:15", "02:10", "23:59"} {
				sch := config.Schedule{Mode: mode, Time: hm, Weekday: 3, EveryHours: 5}
				start := time.Date(2026, 1, 1, 6, 0, 0, 0, loc)
				end := start.AddDate(1, 0, 0)
				offFrom := time.Date(2026, 6, 10, 8, 0, 0, 0, loc)
				offTo := offFrom.Add(72 * time.Hour)
				anchor := start
				perDate := map[string]int{}
				var last time.Time
				for now := start; now.Before(end); now = now.Add(time.Minute) {
					if !now.Before(offFrom) && now.Before(offTo) {
						continue
					}
					next := NextRun(sch, anchor, now)
					if next.IsZero() || now.Before(next) {
						continue
					}
					d := now.In(loc).Format("2006-01-02")
					perDate[d]++
					catchUp := now.Sub(offTo) >= 0 && now.Sub(offTo) < 2*time.Minute
					if mode == "interval" && !last.IsZero() && !catchUp && now.Sub(last) < 5*time.Hour {
						report("%s interval: runs %v and %v too close", z, last, now)
					}
					if mode == "weekly" && !catchUp && int(now.In(loc).Weekday()) != 3 {
						report("%s weekly %s: run on %v (%s)", z, hm, now, now.Weekday())
					}
					last = now
					anchor = now
				}
				if mode == "interval" {
					continue
				}
				for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
					ds := d.Format("2006-01-02")
					n := perDate[ds]
					if ds == start.Format("2006-01-02") || z == "America/Santiago" && (ds == "2026-09-05" || ds == "2026-09-06") {
						continue // first day (anchor=start) / DST gap at midnight moves the slot to 23:xx the day before
					}
					inOff := !d.Before(offFrom.Truncate(24*time.Hour).Add(-24*time.Hour)) && d.Before(offTo.Add(24*time.Hour))
					if mode == "daily" && !inOff && n != 1 {
						report("%s daily %s: %d runs on %s", z, hm, n, ds)
					}
					if n > 1 && !(inOff && n == 2) {
						report("%s %s %s: %d runs on %s", z, mode, hm, n, ds)
					}
				}
			}
		}
	}
}

// CleanSourcePath must keep legitimate Windows folder names.
func TestBreak3_CleanSourcePathLegit(t *testing.T) {
	good := []string{`C:\Program Files (x86)\App`, `C:\Users\me\.config`, `D:\a...b`, `D:\...hidden`,
		`D:\Backups\2026.10.24`, `E:\COM10`, `E:\console`, `E:\nul-files`, `E:\LPT0`, `D:\Ünïcødé\日本語`, `D:\a b\ c`,
		`D:\$Recycle.Bin2`, `D:\con file`}
	for _, p := range good {
		if got := config.CleanSourcePath(p); got != p {
			t.Errorf("%q -> %q", p, got)
		}
	}
	bad := []string{`D:\AUX.tar.gz`, `D:\x\.`, `D:\x\..`, `D:\a\b `, "D:\\a\tb", `D:\CONIN$`, `D:\CONOUT$`, `D:\COM¹`}
	for _, p := range bad {
		if got := config.CleanSourcePath(p); got != "" {
			t.Errorf("accepted %q as %q", p, got)
		}
	}
}

// Pause/Resume balance through the sign-in callback paths: after expired
// state, cancelled, failed code exchange and a busy run, syncing is not
// paused forever.
func TestBreak3_PauseBalancedAfterCallbacks(t *testing.T) {
	r := newT(t, 0, nil)
	for _, q := range []string{"?error=access_denied", "?state=bogus&code=x"} {
		r.call("GET", "/oauth/callback"+q, nil)
		getCallback(r, "") // also via helper
	}
	_ = q0(r)
	st := beginState(t, r)
	getCallback(r, st)
	getCallback(r, st) // replayed
	if r.a.Engine.Paused() {
		t.Errorf("engine paused after failed callbacks")
	}
	s := r.a.Settings()
	s.Sources = []config.Source{{Path: r.src, Enabled: true}}
	r.a.SaveSettings(s)
	if err := r.a.Engine.Start(engine.Options{Trigger: "manual"}); err != nil {
		t.Errorf("start refused after callbacks: %v", err)
	}
	r.a.Engine.StopAndWait(10 * time.Second)
}

func q0(r *tenv) int { c, _ := r.call("GET", "/api/status", nil); return c }
