package engine

// Tests for the version 1.2 features: one user-chosen backup folder (My
// Drive or shared drive), charging-only, monthly report, overdue alert.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/selvasync/selvasync/internal/config"
	"github.com/selvasync/selvasync/internal/fakedrive"
)

// filesUnder lists live file paths starting with prefix.
func filesUnder(fd *fakedrive.Server, prefix string) []string {
	var out []string
	for p, fs := range fd.Live() {
		if strings.HasPrefix(p, prefix) {
			for range fs {
				out = append(out, p)
			}
		}
	}
	return out
}

func (e *env) useLocation(id, name, driveID string) {
	e.set.Location = config.Location{FolderID: id, Name: name, DriveID: driveID}
}

func TestV12_CustomFolderInMyDrive(t *testing.T) {
	e := newEnv(t)
	e.fd.AddFolder("officeBackup", "Office Backups", "root", "")
	e.useLocation("officeBackup", "My Drive › Office Backups", "")
	e.write("a.txt", "aaa")
	e.write("d/b.txt", "bbb")
	s := e.run(Options{})
	if s.Result != "ok" || s.Uploaded != 2 {
		t.Fatalf("run: %+v", s)
	}
	pre := "Office Backups/TEST-PC/" + RemotePrefix(e.src)
	if got := filesUnder(e.fd, pre); len(got) != 2 {
		t.Fatalf("files under chosen folder: %v (all: %v)", got, e.fd.Live())
	}
	for p := range e.fd.Folders() {
		if strings.HasPrefix(p, "Selva Sync") {
			t.Fatalf("default folder was created although a folder was chosen: %s", p)
		}
	}
	e.noDuplicates()
}

func TestV12_SharedDrive(t *testing.T) {
	e := newEnv(t)
	e.fd.NoDeleteShared = true
	e.fd.AddFolder("sd1", "Team Drive", "", "sd1") // shared drive root
	e.fd.AddFolder("sdBackup", "PC Backups", "sd1", "sd1")
	e.useLocation("sdBackup", "Team Drive › PC Backups", "sd1")
	e.write("a.txt", "aaa")
	e.write("old.txt", "old-file")
	e.write("d/b.txt", "bbb")
	if s := e.run(Options{}); s.Result != "ok" || s.Uploaded != 3 {
		t.Fatalf("first run: %+v", s)
	}
	// change, rename, delete
	e.write("a.txt", "aaa-changed")
	e.rename("d/b.txt", "d/c.txt")
	e.remove("old.txt")
	s := e.run(Options{})
	if s.Result != "ok" || s.Failed != 0 {
		t.Fatalf("second run: %+v", s)
	}
	pre := "Team Drive/PC Backups/TEST-PC/" + RemotePrefix(e.src)
	if f := e.fd.Live()[pre+"/a.txt"]; len(f) != 1 || string(f[0].Data) != "aaa-changed" {
		t.Fatalf("a.txt not updated in shared drive: %v", f)
	}
	if len(e.fd.Live()[pre+"/d/c.txt"]) != 1 || len(e.fd.Live()[pre+"/d/b.txt"]) != 0 {
		t.Fatalf("rename not mirrored: %v", filesUnder(e.fd, pre))
	}
	if len(filesUnder(e.fd, "Team Drive/PC Backups/TEST-PC/Archive")) != 1 {
		t.Fatalf("deleted file not archived: %v", filesUnder(e.fd, "Team Drive"))
	}
	e.noDuplicates()

	// archive trimming in a shared drive: permanent delete is forbidden, so
	// the old day folder must go to the shared drive's bin instead
	e.set.ArchiveDays = 1
	e.fd.Lock()
	for _, f := range e.fd.Files {
		if f.Mime == "application/vnd.google-apps.folder" && len(f.Name) == 10 && f.Name[4] == '-' {
			f.Created = time.Now().AddDate(0, 0, -5).UTC().Format(time.RFC3339Nano)
			f.Name = time.Now().AddDate(0, 0, -5).Format("2006-01-02")
		}
	}
	e.fd.Unlock()
	e.eng.smu.Lock()
	e.eng.state.LastArchiveTrim = time.Time{}
	e.eng.smu.Unlock()
	e.write("new.txt", "n")
	e.run(Options{})
	if got := filesUnder(e.fd, "Team Drive/PC Backups/TEST-PC/Archive"); len(got) != 0 {
		t.Fatalf("old archive not removed in shared drive: %v", got)
	}
}

func TestV12_MissingLocationNeverFallsBack(t *testing.T) {
	cases := map[string]func(f *fakedrive.File){
		"deleted":   nil,
		"trashed":   func(f *fakedrive.File) { f.Trashed = true },
		"no access": func(f *fakedrive.File) { f.NoAccess = true },
		"read only": func(f *fakedrive.File) { f.ReadOnly = true },
		"not folder": func(f *fakedrive.File) {
			f.Mime = "text/plain"
		},
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			f := e.fd.AddFolder("loc1", "Backups", "root", "")
			e.fd.Lock()
			if mod == nil {
				delete(e.fd.Files, "loc1")
			} else {
				mod(f)
			}
			e.fd.Unlock()
			e.useLocation("loc1", "My Drive › Backups", "")
			e.write("a.txt", "aaa")
			s := e.run(Options{})
			if s.Result != "failed" || s.Reason != "location_missing" {
				t.Fatalf("want failed/location_missing, got %+v", s)
			}
			live := e.fd.Live()
			delete(live, "Backups") // the chosen item itself (the "not a folder" case)
			if n := len(live); n != 0 {
				t.Fatalf("%d files were uploaded somewhere else: %v", n, e.fd.Live())
			}
			if n := len(e.fd.Folders()); n > 1 { // only the (broken) chosen folder may exist
				t.Fatalf("folders created elsewhere: %v", e.fd.Folders())
			}
		})
	}
}

// The chosen folder disappears after backups were made there.
func TestV12_LocationRemovedLater(t *testing.T) {
	e := newEnv(t)
	e.fd.AddFolder("loc1", "Backups", "root", "")
	e.useLocation("loc1", "My Drive › Backups", "")
	e.write("a.txt", "aaa")
	e.run(Options{})
	e.fd.Lock()
	e.fd.Files["loc1"].Trashed = true
	e.fd.Unlock()
	e.write("b.txt", "bbb")
	s := e.run(Options{})
	if s.Reason != "location_missing" {
		t.Fatalf("got %+v", s)
	}
	for p := range e.fd.Live() {
		t.Errorf("file outside the trashed folder: %s", p)
	}
}

// Switching A -> B -> A must never create duplicates in A.
func TestV12_SwitchLocationsBackAndForth(t *testing.T) {
	e := newEnv(t)
	e.fd.AddFolder("A", "A", "root", "")
	e.fd.AddFolder("B", "B", "root", "")
	e.write("x/1.txt", "one")
	e.write("2.txt", "two")
	e.useLocation("A", "My Drive › A", "")
	e.run(Options{})
	e.useLocation("B", "My Drive › B", "")
	if s := e.run(Options{}); s.Result != "ok" || s.Uploaded != 2 {
		t.Fatalf("B run: %+v", s)
	}
	e.write("2.txt", "two-v2")
	e.write("3.txt", "three")
	e.useLocation("A", "My Drive › A", "")
	s := e.run(Options{})
	t.Logf("back to A: %+v", s)
	e.noDuplicates()
	pre := "A/TEST-PC/" + RemotePrefix(e.src)
	if f := e.fd.Live()[pre+"/2.txt"]; len(f) != 1 || string(f[0].Data) != "two-v2" {
		t.Fatalf("2.txt in A not current: %v", f)
	}
	if len(e.fd.Live()[pre+"/3.txt"]) != 1 {
		t.Fatal("3.txt missing in A")
	}
	// and back to the default folder
	e.useLocation("", "", "")
	e.run(Options{})
	e.noDuplicates()
	if len(e.fd.Live()["Selva Sync/TEST-PC/"+RemotePrefix(e.src)+"/3.txt"]) != 1 {
		t.Fatal("default folder not used after clearing the location")
	}
}

func TestV12_OnlyOnAC(t *testing.T) {
	e := newEnv(t)
	battery := true
	e.eng.Hooks.OnBattery = func() bool { return battery }
	e.write("a.txt", "a")
	e.set.OnlyOnAC = true
	if s := e.run(Options{}); s.Result != "blocked" || s.Reason != "on_battery" {
		t.Fatalf("on battery: %+v", s)
	}
	if len(e.fd.Live()) != 0 {
		t.Fatal("uploaded on battery")
	}
	e.set.OnlyOnAC = false
	if s := e.run(Options{}); s.Result != "ok" {
		t.Fatalf("setting off: %+v", s)
	}
	e.set.OnlyOnAC = true
	battery = false
	e.write("b.txt", "b")
	if s := e.run(Options{}); s.Result != "ok" {
		t.Fatalf("charging: %+v", s)
	}
}

func TestV12_MonthlyReport(t *testing.T) {
	e := newEnv(t)
	calls := 0
	e.eng.Hooks.ReportText = func(month string, st config.MonthStats, set config.Settings) string {
		calls++
		return fmt.Sprintf("report %s runs=%d uploaded=%d", month, st.Runs, st.Uploaded)
	}
	e.write("a.txt", "a")
	e.run(Options{})
	prev := time.Now().AddDate(0, -1, 0)
	prev = time.Date(prev.Year(), prev.Month(), 1, 0, 0, 0, 0, time.Local)
	month := prev.Format("2006-01")
	e.eng.smu.Lock()
	e.eng.state.Monthly[month] = &config.MonthStats{Runs: 7, OK: 6, Failed: 1, Uploaded: 42}
	e.eng.smu.Unlock()
	e.run(Options{})
	e.write("b.txt", "b")
	e.run(Options{})
	name := "Selva Sync/TEST-PC/Selva Sync report " + month + ".txt"
	f := e.fd.Live()[name]
	if len(f) != 1 {
		t.Fatalf("report files: %d (%v)", len(f), e.fd.Live())
	}
	if !strings.Contains(string(f[0].Data), "runs=7 uploaded=42") {
		t.Fatalf("report text: %q", f[0].Data)
	}
	if calls != 1 {
		t.Fatalf("report written %d times", calls)
	}
	// this month's totals are counted
	e.eng.smu.Lock()
	cur := e.eng.state.Monthly[time.Now().Format("2006-01")]
	e.eng.smu.Unlock()
	if cur == nil || cur.Runs != 3 || cur.Uploaded != 2 {
		t.Fatalf("this month stats: %+v", cur)
	}
	// the report survives a full check and is never archived as "deleted"
	e.run(Options{FullCheck: true})
	e.run(Options{})
	if len(e.fd.Live()[name]) != 1 {
		t.Fatal("report removed by a full check")
	}
	if a := filesUnder(e.fd, "Selva Sync/TEST-PC/Archive"); len(a) != 0 {
		t.Fatalf("archived: %v", a)
	}
}

func TestV12_ReportOff(t *testing.T) {
	e := newEnv(t)
	e.set.Reports = false
	e.eng.Hooks.ReportText = func(string, config.MonthStats, config.Settings) string { return "x" }
	prev := time.Now().AddDate(0, -1, 0).Format("2006-01")
	e.eng.smu.Lock()
	e.eng.state.Monthly[prev] = &config.MonthStats{Runs: 1}
	e.eng.smu.Unlock()
	e.write("a.txt", "a")
	e.run(Options{})
	for p := range e.fd.Live() {
		if strings.Contains(p, "report") {
			t.Fatalf("report written although turned off: %s", p)
		}
	}
}

func TestV12_DryRunNotCounted(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "a")
	e.run(Options{DryRun: true})
	e.eng.smu.Lock()
	defer e.eng.smu.Unlock()
	if st := e.eng.state.Monthly[time.Now().Format("2006-01")]; st != nil {
		t.Fatalf("preview counted in monthly report: %+v", st)
	}
}

func TestV12_AlertDueOncePerDay(t *testing.T) {
	e := newEnv(t)
	now := time.Now()
	if !e.eng.AlertDue(now) {
		t.Fatal("first alert not due")
	}
	if e.eng.AlertDue(now.Add(time.Hour)) {
		t.Fatal("alert repeated within a day")
	}
	if !e.eng.AlertDue(now.Add(24 * time.Hour)) {
		t.Fatal("alert not repeated next day")
	}
}

var _ = context.Background

func (e *env) rename(from, to string) {
	time.Sleep(10 * time.Millisecond)
	if err := os.Rename(filepath.Join(e.src, filepath.FromSlash(from)), filepath.Join(e.src, filepath.FromSlash(to))); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) remove(rel string) {
	if err := os.Remove(filepath.Join(e.src, filepath.FromSlash(rel))); err != nil {
		e.t.Fatal(err)
	}
}
