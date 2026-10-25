package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/selvasync/selvasync/internal/config"
	"github.com/selvasync/selvasync/internal/engine"
)

// B4-1 (high): with the drive.file scope a PC's token can only see folders
// that THIS user created with the app or opened through Google Picker. A
// folder fixed by the office setup file ("managed": ["location"]) was chosen
// on the admin's PC, so on every other PC it is invisible: backups stop with
// "location missing", and the only way to grant access (choosing that folder
// once in Google's chooser) is refused with err_managed. The PC is stuck.
func TestBreak4_ManagedLocationUnreachableUnderDriveFile(t *testing.T) {
	r := newT(t, 0, func(dir string) {
		os.WriteFile(filepath.Join(dir, SetupFileName), []byte(`{"managed":["location"],
			"location":{"folderId":"teamF1","name":"Team › Backups","driveId":"sdrive1"}}`), 0o644)
	})
	r.fd.AddFolder("sdrive1", "Team", "", "sdrive1")
	f := r.fd.AddFolder("teamF1", "Backups", "sdrive1", "sdrive1")
	f.NoAccess = true // not yet opened with the app by this user (drive.file)
	r.fd.AddFolder("other1", "Elsewhere", "root", "")

	if got := r.a.Settings().Location.FolderID; got != "teamF1" {
		t.Fatalf("setup file location not applied: %q", got)
	}
	set := r.a.Settings()
	set.Sources = []config.Source{{Path: r.src, Enabled: true}}
	r.a.SaveSettings(set)
	r.a.Engine.Start(engine.Options{Trigger: "manual"})
	waitIdle(t, r)
	last, _, _, _, _ := r.a.Engine.StateInfo()
	t.Logf("sync result on this PC: %+v", last)

	r.a.mu.Lock()
	r.a.settings.PickerURL = "https://example.github.io/selva-sync/picker.html"
	r.a.mu.Unlock()
	code, body := r.call("POST", "/api/picker/start", map[string]any{})
	if code != 200 {
		t.Fatalf("the organisation's folder is not visible to this PC (drive.file) and the user is not allowed to "+
			"open it in Google's chooser to grant access: picker/start = %d %s", code, body)
	}
	// once allowed, only the organisation's own folder may be accepted
	var res struct{ URL string }
	json.Unmarshal([]byte(body), &res)
	u, _ := url.Parse(res.URL)
	frag, _ := url.ParseQuery(u.Fragment)
	picked(r, url.Values{"state": {frag.Get("state")}, "id": {"other1"}})
	if r.a.Settings().Location.FolderID != "teamF1" {
		t.Fatal("managed location replaced by another folder")
	}
}

func waitIdle(t *testing.T, r *tenv) {
	t.Helper()
	time.Sleep(200 * time.Millisecond)
	for i := 0; i < 300 && r.a.Engine.Status().Running; i++ {
		time.Sleep(100 * time.Millisecond)
	}
}

// B4-2 (medium): /api/settings takes its copy of the current settings BEFORE
// reading the request body and then forces s.Location = cur.Location. A
// folder saved by /picked while the body is still arriving is silently
// reverted by the window's next autosave.
func TestBreak4_SettingsSaveRevertsPickedLocation(t *testing.T) {
	r := newT(t, 0, nil)
	r.fd.AddFolder("fold77", "Office backups", "root", "")
	st := startPicker(t, r)

	body := `{"archiveDays": 7, "language": "en"}`
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", r.a.port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "POST /api/settings HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nX-Selva-Token: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
		r.a.port, r.a.token, len(body), body[:10])
	time.Sleep(300 * time.Millisecond) // handler has started and copied the settings

	_, page := picked(r, url.Values{"state": {st}, "id": {"fold77"}})
	if r.a.Settings().Location.FolderID != "fold77" {
		t.Fatalf("picker did not save: %s", page)
	}
	conn.Write([]byte(body[10:]))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("settings save: %v %v", resp, err)
	}
	resp.Body.Close()
	if got := r.a.Settings(); got.ArchiveDays != 7 || got.Location.FolderID != "fold77" {
		t.Fatalf("the chosen backup folder was reverted by a concurrent settings save: location=%+v archiveDays=%d",
			got.Location, got.ArchiveDays)
	}
}

// B4-3 (low): keepManaged copies managed values through JSON, but pickerUrl
// is "omitempty". When the organisation fixes pickerUrl to "" (= use the
// publisher's page), the key is missing from the current settings and the
// window's value wins: the user can point the folder chooser anywhere.
func TestBreak4_ManagedEmptyPickerURLCanBeChanged(t *testing.T) {
	r := newT(t, 0, func(dir string) {
		os.WriteFile(filepath.Join(dir, SetupFileName), []byte(`{"managed":["pickerUrl"],"pickerUrl":""}`), 0o644)
	})
	if !isManaged(r.a.Settings(), "pickerUrl") {
		t.Fatalf("not managed: %v", r.a.Settings().Managed)
	}
	r.call("POST", "/api/settings", map[string]any{"pickerUrl": "https://attacker.example/picker.html"})
	if got := r.a.Settings().PickerURL; got != "" {
		t.Fatalf("managed pickerUrl changed from the window: %q", got)
	}
}

// B4-4 (low/medium): one value of the wrong type ("true" instead of true)
// makes applySetup throw the whole file away silently, including every
// managed setting; nothing is logged, so the admin never learns why.
func TestBreak4_SetupFileOneBadValueDropsEverything(t *testing.T) {
	file := []byte(`{"managed":["schedule","archiveDays"],
		"schedule":{"mode":"weekly","time":"08:00","everyHours":6,"weekday":1},
		"archiveDays":30, "onlyOnAC":"true"}`)
	got, _ := applySetup(config.Defaults(), file, true)
	if got.Schedule.Mode != "weekly" || got.ArchiveDays != 30 || len(got.Managed) != 2 {
		t.Fatalf("one wrongly typed value (onlyOnAC:\"true\") discarded the whole setup file: schedule=%+v archiveDays=%d managed=%v",
			got.Schedule, got.ArchiveDays, got.Managed)
	}
}

// B4-5 (medium): the managed list is only ever replaced when a setup file is
// found. After the organisation removes selvasync-setup.json the settings
// stay locked forever (window shows "set by your organisation", the picker
// is refused) and nothing in the app can unlock them.
func TestBreak4_RemovedSetupFileKeepsSettingsLocked(t *testing.T) {
	r := newT(t, 0, func(dir string) {
		os.WriteFile(filepath.Join(dir, SetupFileName), []byte(`{"managed":["archiveDays","location"],"archiveDays":30}`), 0o644)
	})
	if len(r.a.Settings().Managed) != 2 {
		t.Fatal("setup not applied")
	}
	r.a.Shutdown()
	os.Remove(filepath.Join(r.dir, SetupFileName))
	a2, err := New(r.dir) // next start, without the setup file
	if err != nil {
		t.Fatal(err)
	}
	if m := a2.Settings().Managed; len(m) != 0 {
		t.Fatalf("setup file removed, but settings are still locked: managed=%v", m)
	}
}

// B4-6 (medium): when there was never a successful backup, OverdueDays counts
// from a.started, i.e. from the last time the program started. An office PC
// that is switched on every morning and whose backups always fail never gets
// the overdue warning.
func TestBreak4_OverdueResetByEveryRestart(t *testing.T) {
	r := newT(t, 0, nil)
	hid := r.fd.AddFolder("hid42", "Gone", "root", "")
	hid.NoAccess = true
	set := r.a.Settings()
	set.Sources = []config.Source{{Path: r.src, Enabled: true}}
	set.AlertDays = 3
	r.a.SaveSettings(set)
	r.a.mu.Lock()
	r.a.settings.Location = config.Location{FolderID: "hid42", Name: "My Drive › Gone"}
	r.a.mu.Unlock()
	r.a.Engine.Start(engine.Options{Trigger: "manual"})
	waitIdle(t, r)
	last, ok, _, _, _ := r.a.Engine.StateInfo()
	if last == nil || !ok.IsZero() {
		t.Fatalf("setup: expected a run without success, got %+v %v", last, ok)
	}
	r.a.Shutdown()

	// the PC was used (and restarted) every day; today is day 5
	day5 := time.Now().Add(5 * 24 * time.Hour)
	a2, err := New(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	a2.started = day5.Add(-time.Hour) // switched on this morning
	if d := a2.OverdueDays(day5); d < 3 {
		t.Fatalf("5 days without any successful backup, but OverdueDays = %d (counted from the last program start)", d)
	}
}

// B4-7 (medium): LastSuccess is only set for "ok"/"nothing". One file that is
// always locked (an open Outlook .pst) makes every daily run "partial", and
// after AlertDays the app says "No backup for N days. Your files have not
// been backed up" although everything else was uploaded an hour ago.
func TestBreak4_OverdueFalseAlarmWithPartialRuns(t *testing.T) {
	now := time.Now()
	r := newT(t, 0, func(data string) {
		st := config.NewState()
		st.LastSuccess = now.Add(-10 * 24 * time.Hour)
		st.LastRun = &config.RunSummary{Start: now.Add(-time.Hour), End: now.Add(-50 * time.Minute), Result: "partial", Uploaded: 240, Updated: 31, Failed: 1}
		st.LastAttempt, st.LastScheduled = now.Add(-time.Hour), now.Add(-time.Hour)
		b, _ := json.Marshal(st)
		os.WriteFile(filepath.Join(data, "state.json"), b, 0o600)
	})
	set := r.a.Settings()
	set.Sources = []config.Source{{Path: r.src, Enabled: true}}
	set.AlertDays = 3
	r.a.SaveSettings(set)
	if d := r.a.OverdueDays(now); d != 0 {
		t.Fatalf("a backup ran an hour ago (271 files uploaded, 1 locked file failed), but the app reports %q",
			strings.ReplaceAll(r.a.I18n.T("en", "overdue_title", map[string]string{"days": fmt.Sprint(d)}), "\n", " "))
	}
}
