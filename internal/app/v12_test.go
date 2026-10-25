package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/selvasync/selvasync/internal/config"
)

func TestV12_SetupFileFirstRunAndManaged(t *testing.T) {
	base := config.Defaults()
	base.Sources = []config.Source{{ID: "x", Path: `C:\Users\A\Documents`, Enabled: true}}
	base.Language = "ta"
	base.ComputerName = "PC-1"
	file := []byte(`{"managed":["schedule","archiveDays","sources","bogus"],
		"schedule":{"mode":"daily","time":"21:30","everyHours":6,"weekday":0},
		"archiveDays":30, "dayLimitKBps":256, "sources":[{"path":"D:\\evil","enabled":true}],
		"language":"en","computerName":"X","clientId":"evil.apps.googleusercontent.com"}`)

	first, ok := applySetup(base, file, true)
	if !ok {
		t.Fatal("not applied")
	}
	if first.Schedule.Mode != "daily" || first.Schedule.Time != "21:30" || first.ArchiveDays != 30 || first.DayLimitKBps != 256 {
		t.Fatalf("first run values: %+v", first)
	}
	if len(first.Sources) != 1 || first.Sources[0].Path != `C:\Users\A\Documents` || first.Language != "ta" || first.ComputerName != "PC-1" || first.ClientID != "" {
		t.Fatalf("personal settings were taken from the setup file: %+v", first)
	}
	if strings.Join(first.Managed, ",") != "archiveDays,schedule" && strings.Join(first.Managed, ",") != "schedule,archiveDays" {
		t.Fatalf("managed list: %v", first.Managed)
	}

	// later start: user changed things; only managed keys are reset
	later := first
	later.ArchiveDays, later.DayLimitKBps = 5, 0
	later.Schedule.Mode = "manual"
	again, _ := applySetup(later, file, false)
	if again.ArchiveDays != 30 || again.Schedule.Mode != "daily" {
		t.Fatalf("managed values not restored: %+v", again)
	}
	if again.DayLimitKBps != 0 {
		t.Fatal("unmanaged value overwritten on a later start")
	}

	// broken file changes nothing
	if _, ok := applySetup(base, []byte("{not json"), true); ok {
		t.Fatal("broken setup file applied")
	}
	// hostile values are cleaned by Normalize
	bad, _ := applySetup(base, []byte(`{"archiveDays":-5,"parallel":999,"pickerUrl":"javascript:alert(1)","alertDays":100000}`), true)
	if bad.ArchiveDays < 0 || bad.Parallel > 8 || bad.PickerURL != "" || bad.AlertDays > 365 {
		t.Fatalf("hostile setup values kept: archive=%d parallel=%d picker=%q alert=%d", bad.ArchiveDays, bad.Parallel, bad.PickerURL, bad.AlertDays)
	}
}

func TestV12_ExportSetupRoundTrip(t *testing.T) {
	s := config.Defaults()
	s.ArchiveDays = 45
	s.ClientID, s.ClientSecret = "id.apps.googleusercontent.com", "SECRET"
	s.Sources = []config.Source{{ID: "a", Path: `C:\private`, Enabled: true}}
	s.ComputerName = "BOSS-PC"
	b := ExportSetup(s)
	txt := string(b)
	for _, leak := range []string{"SECRET", "id.apps", `private`, "BOSS-PC"} {
		if strings.Contains(txt, leak) {
			t.Fatalf("export contains personal data %q:\n%s", leak, txt)
		}
	}
	got, ok := applySetup(config.Defaults(), b, true)
	if !ok || got.ArchiveDays != 45 {
		t.Fatalf("round trip: %+v", got)
	}
}

func TestV12_ManagedCannotBeChangedFromWindow(t *testing.T) {
	r := newT(t, 0, func(dir string) {
		os.WriteFile(filepath.Join(dir, SetupFileName), []byte(`{"managed":["archiveDays","location"],"archiveDays":30}`), 0o644)
	})
	if got := r.a.Settings(); got.ArchiveDays != 30 || len(got.Managed) != 2 {
		t.Fatalf("setup file not applied at start: %d %v", got.ArchiveDays, got.Managed)
	}
	code, body := r.call("POST", "/api/settings", map[string]any{"archiveDays": 2, "maxFileSizeMB": 7, "managed": []string{}})
	if code != 200 {
		t.Fatalf("save: %d %s", code, body)
	}
	got := r.a.Settings()
	if got.ArchiveDays != 30 || got.MaxFileSizeMB != 7 || len(got.Managed) != 2 {
		t.Fatalf("managed setting changed from window: archive=%d max=%d managed=%v", got.ArchiveDays, got.MaxFileSizeMB, got.Managed)
	}
	if code, _ := r.call("POST", "/api/picker/start", map[string]any{}); code != 403 {
		t.Fatalf("picker allowed although location is managed: %d", code)
	}
	if code, _ := r.call("POST", "/api/picker/clear", map[string]any{}); code != 403 {
		t.Fatalf("clear allowed although location is managed: %d", code)
	}
}

func TestV12_WindowCannotSetManagedOrLocation(t *testing.T) {
	r := newT(t, 0, nil)
	r.call("POST", "/api/settings", map[string]any{
		"managed":  []string{"archiveDays"},
		"location": map[string]string{"folderId": "attackerFolder", "name": "x"},
	})
	got := r.a.Settings()
	if len(got.Managed) != 0 || got.Location.FolderID != "" {
		t.Fatalf("window set managed=%v location=%+v", got.Managed, got.Location)
	}
}

// startPicker runs /api/picker/start and returns the state from the URL.
func startPicker(t *testing.T, r *tenv) string {
	t.Helper()
	r.a.mu.Lock()
	r.a.settings.PickerURL = "https://example.github.io/selva-sync/picker.html"
	r.a.mu.Unlock()
	code, body := r.call("POST", "/api/picker/start", map[string]any{})
	if code != 200 {
		t.Fatalf("picker start: %d %s", code, body)
	}
	var res struct{ URL string }
	json.Unmarshal([]byte(body), &res)
	u, _ := url.Parse(res.URL)
	if u.RawQuery != "" || !strings.HasPrefix(res.URL, "https://example.github.io/selva-sync/picker.html#") {
		t.Fatalf("picker details must be after '#': %s", res.URL)
	}
	frag, _ := url.ParseQuery(u.Fragment)
	if cb := frag.Get("cb"); cb != fmt.Sprintf("http://127.0.0.1:%d/picked", r.a.port) {
		t.Fatalf("callback: %q", cb)
	}
	return frag.Get("state")
}

func picked(r *tenv, q url.Values) (int, string) {
	req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/picked?%s", r.a.port, q.Encode()), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b := new(strings.Builder)
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, b.String()
}

func TestV12_PickerFlow(t *testing.T) {
	r := newT(t, 0, nil)
	r.fd.AddFolder("sd9", "Team", "", "sd9")
	r.fd.AddFolder("fold9", "Backups <b>", "sd9", "sd9")
	ro := r.fd.AddFolder("ro1", "Read only", "root", "")
	ro.ReadOnly = true
	hidden := r.fd.AddFolder("hid1", "Hidden", "root", "")
	hidden.NoAccess = true

	// no state / wrong state
	if _, body := picked(r, url.Values{"id": {"fold9"}}); r.a.Settings().Location.FolderID != "" || !strings.Contains(body, "expired") && !strings.Contains(body, "Click") {
		t.Fatalf("accepted without state: %s", body)
	}
	st := startPicker(t, r)
	if _, _ = picked(r, url.Values{"state": {st + "x"}, "id": {"fold9"}}); r.a.Settings().Location.FolderID != "" {
		t.Fatal("accepted with a wrong state")
	}
	// the wrong guess must not burn... but a used/expired state cannot be reused
	st = startPicker(t, r)
	for _, bad := range []string{"ro1", "hid1", "../../etc", "nope12345"} {
		st = startPicker(t, r)
		picked(r, url.Values{"state": {st}, "id": {bad}})
		if r.a.Settings().Location.FolderID != "" {
			t.Fatalf("bad folder %q accepted", bad)
		}
	}
	st = startPicker(t, r)
	_, body := picked(r, url.Values{"state": {st}, "id": {"fold9"}, "dn": {"Team\u202eevil\x07"}})
	loc := r.a.Settings().Location
	if loc.FolderID != "fold9" || loc.DriveID != "sd9" {
		t.Fatalf("location not saved: %+v (%s)", loc, body)
	}
	if strings.Contains(loc.Name, "\u202e") || strings.Contains(loc.Name, "\x07") {
		t.Fatalf("unsafe characters kept in name: %q", loc.Name)
	}
	if strings.Contains(body, "<b>") {
		t.Fatal("folder name not escaped in the page")
	}
	// state is one-time
	picked(r, url.Values{"state": {st}, "id": {"ro1"}})
	if r.a.Settings().Location.FolderID != "fold9" {
		t.Fatal("state reused")
	}
	// window shows the label
	_, sbody := r.call("GET", "/api/state", nil)
	if !strings.Contains(sbody, `"locationLabel"`) || !strings.Contains(sbody, "Team") {
		t.Fatalf("state lacks location: %s", sbody)
	}
	// back to default
	if code, _ := r.call("POST", "/api/picker/clear", map[string]any{}); code != 200 || r.a.Settings().Location.FolderID != "" {
		t.Fatal("clear failed")
	}
}

func TestV12_PickerNeedsURL(t *testing.T) {
	r := newT(t, 0, nil)
	DefaultPickerURL = ""
	if code, body := r.call("POST", "/api/picker/start", map[string]any{}); code != 400 || !strings.Contains(body, "err_no_picker") {
		t.Fatalf("got %d %s", code, body)
	}
}

func TestV12_OverdueDays(t *testing.T) {
	r := newT(t, 0, nil)
	set := r.a.Settings()
	set.Sources = []config.Source{{Path: r.src, Enabled: true}}
	set.AlertDays = 3
	r.a.SaveSettings(set)
	now := time.Now()
	if d := r.a.OverdueDays(now.Add(48 * time.Hour)); d != 0 {
		t.Fatalf("2 days counted as overdue: %d", d)
	}
	if d := r.a.OverdueDays(now.Add(80 * time.Hour)); d != 3 {
		t.Fatalf("want 3 overdue days, got %d", d)
	}
	set.AlertDays = 0
	r.a.SaveSettings(set)
	if d := r.a.OverdueDays(now.Add(500 * time.Hour)); d != 0 {
		t.Fatal("alert not turned off by 0")
	}
}

func TestV12_ReportTextAllLanguages(t *testing.T) {
	r := newT(t, 0, nil)
	st := config.MonthStats{Runs: 4, OK: 3, Failed: 1, Uploaded: 12, Bytes: 5 << 20, Errors: []string{"2026-09-03 10:00  sync failed: network"}}
	for _, l := range r.a.I18n.Languages() {
		set := r.a.Settings()
		set.Language = l.Code
		txt := r.a.reportText("2026-09", st, set)
		if strings.Contains(txt, "report_") || !strings.Contains(txt, "2026-09") || !strings.Contains(txt, "network") {
			t.Errorf("%s report incomplete:\n%s", l.Code, txt)
		}
	}
}

func TestV12_ManagedLocationGrant(t *testing.T) {
	r := newT(t, 0, func(dir string) {
		os.WriteFile(filepath.Join(dir, SetupFileName), []byte(`{"managed":["location"],
			"location":{"folderId":"teamF2","name":"Team › Backups","driveId":"sd2"}}`), 0o644)
	})
	r.fd.AddFolder("sd2", "Team", "", "sd2")
	r.fd.AddFolder("teamF2", "Backups", "sd2", "sd2")
	st := startPicker(t, r)
	_, body := picked(r, url.Values{"state": {st}, "id": {"teamF2"}})
	if !strings.Contains(body, "Team › Backups") || r.a.Settings().Location.FolderID != "teamF2" {
		t.Fatalf("grant page: %s", body)
	}
	if !isManaged(r.a.Settings(), "location") {
		t.Fatal("managed flag lost")
	}
}

func TestV12_PickedInsideBackupRefused(t *testing.T) {
	r := newT(t, 0, nil)
	comp := r.fd.AddFolder("comp1", "OTHER-PC", "root", "")
	comp.Props = map[string]string{"selvasync": "computer"}
	r.fd.AddFolder("deep1", "D-Drive", "comp1", "")
	st := startPicker(t, r)
	_, body := picked(r, url.Values{"state": {st}, "id": {"deep1"}})
	if r.a.Settings().Location.FolderID != "" || !strings.Contains(body, "inside") {
		t.Fatalf("folder inside a backup accepted: %s", body)
	}
}
