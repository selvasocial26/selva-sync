package engine

// Round-5 adversarial tests. Each TestBreak5_* FAILS while the problem it
// describes is present.

import (
	"strings"
	"testing"
)

// "Sync anyway" (Options.Force) is offered by the window for every blocked
// run, including reason on_battery, and Run skips networkBlocked when Force
// is set. But the new mid-run check in execute ignores Force, so the forced
// run stops before the first file with "blocked / on_battery" again: the
// button can never back up anything while on battery.
func TestBreak5_SyncAnywayOnBatteryStopsAtFirstFile(t *testing.T) {
	e := newEnv(t)
	e.eng.Hooks.OnBattery = func() bool { return true }
	e.set.OnlyOnAC = true
	e.write("a.txt", strings.Repeat("a", 10))
	e.write("b.txt", strings.Repeat("b", 10))
	if s := e.run(Options{}); s.Result != "blocked" || s.Reason != "on_battery" {
		t.Fatalf("normal run on battery: %+v", s)
	}
	s := e.run(Options{Force: true})
	if s.Result != "ok" || s.Uploaded != 2 {
		t.Errorf("forced run on battery did not back up: %+v", s)
	}
}
