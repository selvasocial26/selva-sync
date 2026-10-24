package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/selvasync/selvasync/internal/config"
)

func TestNextRun(t *testing.T) {
	loc := time.Local
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, loc) // Saturday
	daily := config.Schedule{Mode: "daily", Time: "21:00"}
	// done yesterday after 21:00 -> next is today 21:00
	if got := NextRun(daily, time.Date(2026, 10, 23, 21, 5, 0, 0, loc), now); !got.Equal(time.Date(2026, 10, 24, 21, 0, 0, 0, loc)) {
		t.Errorf("daily next = %v", got)
	}
	// PC was off yesterday at 21:00 -> due now (missed run)
	if got := NextRun(daily, time.Date(2026, 10, 22, 21, 5, 0, 0, loc), now); got.After(now) {
		t.Errorf("missed daily run should be due, got %v", got)
	}
	weekly := config.Schedule{Mode: "weekly", Time: "09:00", Weekday: 1} // Monday
	if got := NextRun(weekly, time.Date(2026, 10, 19, 9, 30, 0, 0, loc), now); !got.Equal(time.Date(2026, 10, 26, 9, 0, 0, 0, loc)) {
		t.Errorf("weekly next = %v", got)
	}
	interval := config.Schedule{Mode: "interval", EveryHours: 6}
	if got := NextRun(interval, now.Add(-2*time.Hour), now); !got.Equal(now.Add(4 * time.Hour)) {
		t.Errorf("interval next = %v", got)
	}
	if !NextRun(config.Schedule{Mode: "manual"}, now, now).IsZero() {
		t.Error("manual should be zero")
	}
}

// Every language must have every English key and keep all {placeholders}.
func TestTranslationsComplete(t *testing.T) {
	i := &I18n{}
	enRaw, _ := webFS.ReadFile("web/i18n/en.json")
	var en map[string]string
	if err := json.Unmarshal(enRaw, &en); err != nil {
		t.Fatal(err)
	}
	langs := i.Languages()
	if len(langs) < 9 {
		t.Fatalf("expected 9+ languages, got %d", len(langs))
	}
	for _, l := range langs {
		raw, _ := webFS.ReadFile("web/i18n/" + l.Code + ".json")
		var m map[string]string
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Errorf("%s: invalid JSON: %v", l.Code, err)
			continue
		}
		for k, v := range en {
			tv, ok := m[k]
			if !ok {
				t.Errorf("%s: missing key %s", l.Code, k)
				continue
			}
			for _, part := range strings.Split(v, "{")[1:] {
				ph := "{" + strings.SplitN(part, "}", 2)[0] + "}"
				if !strings.Contains(tv, ph) {
					t.Errorf("%s: %s lost placeholder %s", l.Code, k, ph)
				}
			}
		}
	}
}
