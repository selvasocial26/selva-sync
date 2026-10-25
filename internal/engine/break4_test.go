package engine

// Round-4 adversarial tests against the version 1.2 features (backup
// location, shared drives, charging-only, monthly report, overdue alert).
// Each TestBreak4_* FAILS when the problem it describes is present.
// Run: go test -race -count=1 -run TestBreak4 -v ./internal/engine/

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/selvasync/selvasync/internal/config"
	"github.com/selvasync/selvasync/internal/fakedrive"
)

// findFolder returns the ID of the live folder at Drive path p.
func findFolder(fd *fakedrive.Server, p string) string {
	fd.Lock()
	defer fd.Unlock()
	for id, f := range fd.Files {
		if f.Mime == "application/vnd.google-apps.folder" && fd.Path(f) == p {
			return id
		}
	}
	return ""
}

// 1. Changing the location always calls ResetRemote, so the "seen on this
// PC" flags are lost. When the computer folder is found again (A -> B -> A,
// or the user simply picks the folder already in use), files deleted on the
// PC meanwhile come back from reindex with Seen=false and are never
// archived: the mirror keeps deleted files forever (and renamed files twice).
func TestBreak4_LocationChangeLosesSeenDeletedFilesStay(t *testing.T) {
	t.Run("A-B-A", func(t *testing.T) {
		e := newEnv(t)
		e.fd.AddFolder("A", "A", "root", "")
		e.fd.AddFolder("B", "B", "root", "")
		e.write("keep.txt", "keep")
		e.write("gone.txt", "deleted while backing up to B")
		e.write("old-name.txt", "renamed while backing up to B")
		e.useLocation("A", "My Drive › A", "")
		e.run(Options{})
		e.useLocation("B", "My Drive › B", "")
		e.run(Options{})
		e.remove("gone.txt")
		e.rename("old-name.txt", "new-name.txt")
		e.run(Options{}) // B mirrors the deletion and the rename
		e.useLocation("A", "My Drive › A", "")
		e.run(Options{})
		e.run(Options{})
		pre := "A/TEST-PC/" + RemotePrefix(e.src)
		live := e.fd.Live()
		if len(live[pre+"/gone.txt"]) != 0 {
			t.Errorf("file deleted on the PC is still in A's mirror after switching back")
		}
		if len(live[pre+"/old-name.txt"]) != 0 {
			t.Errorf("renamed file is in A's mirror under both names: %v", filesUnder(e.fd, pre))
		}
	})
	t.Run("pick folder already in use", func(t *testing.T) {
		e := newEnv(t)
		e.write("keep.txt", "keep")
		e.write("gone.txt", "bye")
		e.run(Options{}) // default My Drive › Selva Sync
		root := findFolder(e.fd, "Selva Sync")
		e.remove("gone.txt")
		// the user opens the folder chooser and picks the same "Selva Sync" folder
		e.useLocation(root, "My Drive › Selva Sync", "")
		e.run(Options{})
		e.run(Options{})
		if len(e.live("gone.txt")) != 0 {
			t.Errorf("deleted file never archived after re-picking the same folder")
		}
	})
}

// 2. A user who picks the existing computer folder ("Selva Sync/TEST-PC",
// i.e. "keep it where it is") gets a NEW computer folder nested inside it and
// the whole PC is uploaded again: double the data and Drive storage, and the
// old copy is no longer maintained.
func TestBreak4_PickingOwnComputerFolderReuploadsEverything(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", strings.Repeat("a", 1000))
	e.write("d/b.txt", strings.Repeat("b", 1000))
	if s := e.run(Options{}); s.Result != "ok" {
		t.Fatalf("first run: %+v", s)
	}
	comp := findFolder(e.fd, "Selva Sync/TEST-PC")
	if comp == "" {
		t.Fatal("computer folder not found")
	}
	e.useLocation(comp, "My Drive › TEST-PC", "")
	before := e.fd.UploadedBytes
	s := e.run(Options{})
	t.Logf("after picking the computer folder: %+v", s)
	if s.Reason != "location_inside" {
		t.Errorf("picking the computer folder itself should be refused with a clear reason, got %+v", s)
	}
	if got := e.fd.UploadedBytes - before; got != 0 {
		t.Errorf("%d bytes uploaded again after picking the folder that already holds the backup", got)
	}
	for p := range e.fd.Folders() {
		if strings.HasPrefix(p, "Selva Sync/TEST-PC/TEST-PC") {
			t.Errorf("nested computer folder created inside the existing one: %s", p)
			break
		}
	}
}

// 3. The computer folder is moved out of the chosen location (e.g. a shared
// drive manager moves "TEST-PC" into another shared drive). ensureComputer
// only checks that the cached folder exists, never that it is still inside
// the location, so every later backup is written outside the chosen folder.
func TestBreak4_ComputerFolderMovedOutOfLocationStillUsed(t *testing.T) {
	e := newEnv(t)
	e.fd.AddFolder("sd1", "Team Drive", "", "sd1")
	e.fd.AddFolder("sdBackup", "PC Backups", "sd1", "sd1")
	e.fd.AddFolder("sd2", "Old Machines", "", "sd2")
	e.useLocation("sdBackup", "Team Drive › PC Backups", "sd1")
	e.write("a.txt", "aaa")
	if s := e.run(Options{}); s.Result != "ok" {
		t.Fatalf("first run: %+v", s)
	}
	comp := findFolder(e.fd, "Team Drive/PC Backups/TEST-PC")
	// move the computer folder (with its contents) to the other shared drive
	e.fd.Lock()
	e.fd.Files[comp].Parents = []string{"sd2"}
	var mark func(id string)
	mark = func(id string) {
		e.fd.Files[id].DriveID = "sd2"
		for cid, c := range e.fd.Files {
			if len(c.Parents) > 0 && c.Parents[0] == id {
				mark(cid)
			}
		}
	}
	mark(comp)
	e.fd.Unlock()

	e.write("b.txt", "new file after the move")
	s := e.run(Options{})
	t.Logf("run after the move: %+v", s)
	for p := range e.fd.Live() {
		if strings.HasSuffix(p, "/b.txt") && !strings.HasPrefix(p, "Team Drive/PC Backups/") {
			t.Errorf("backup written outside the chosen location: %s", p)
		}
	}
}

// 4. "Sync only while plugged in" is checked once at the start. A laptop
// unplugged one minute into a multi-hour backup keeps uploading on battery
// until everything is done.
func TestBreak4_UnpluggedDuringRunKeepsUploading(t *testing.T) {
	e := newEnv(t)
	var battery atomic.Bool
	e.eng.Hooks.OnBattery = func() bool { return battery.Load() }
	e.set.OnlyOnAC = true
	e.set.Parallel = 1
	for i := 0; i < 20; i++ {
		e.write("f"+itoa(i)+".txt", strings.Repeat("x", i+1))
	}
	e.fd.AfterCommit = func(string) { battery.Store(true) } // unplugged after the first file
	s := e.run(Options{})
	t.Logf("run unplugged after first file: %+v", s)
	if s.Uploaded > 2 {
		t.Errorf("%d files uploaded on battery after the laptop was unplugged (result %q)", s.Uploaded-1, s.Result)
	}
}

// 5. FixClock repairs saved times that are in the future after a wrong PC
// clock is corrected, but it forgets LastAlert (new in 1.2). One overdue
// reminder shown while the clock was a year ahead silences the reminder for
// a whole year after the clock is fixed.
func TestBreak4_FixClockLeavesOverdueAlertSilenced(t *testing.T) {
	e := newEnv(t)
	now := time.Now()
	wrong := now.AddDate(1, 0, 0)
	if !e.eng.AlertDue(wrong) {
		t.Fatal("alert not due with wrong clock")
	}
	e.eng.FixClock(now) // clock corrected
	if !e.eng.AlertDue(now.Add(25 * time.Hour)) {
		t.Errorf("overdue reminder still silenced after the clock was corrected (LastAlert stays in the future)")
	}
}

// 2b. Same root cause as 2, worse outcome: in an office sharing one Google
// account, the laptop's location is picked inside the reception PC's mirror
// ("Selva Sync/RECEPTION-PC/<D-Drive>/Shared"). The reception PC's weekly full
// check finds the laptop's computer folder in its mirror, sees no such local
// folder and ARCHIVES the laptop's whole backup (later deleted for good by
// archive trimming); the laptop keeps writing into the archived folder.
func TestBreak4_LocationInsideAnotherPCsMirrorGetsArchived(t *testing.T) {
	pc1 := newEnv(t)
	pc1.write("Shared/report.txt", "reception data")
	if s := pc1.run(Options{}); s.Result != "ok" {
		t.Fatalf("pc1: %+v", s)
	}
	shared := findFolder(pc1.fd, pc1.prefix()+"/Shared")
	if shared == "" {
		t.Fatal("mirror folder not found")
	}

	pc2 := &env{t: t, fd: pc1.fd, src: t.TempDir(), data: t.TempDir(), set: pc1.set}
	pc2.set.ComputerName = "LAPTOP"
	pc2.set.Sources = []config.Source{{ID: "l", Path: pc2.src, Enabled: true}}
	pc2.newEngine()
	pc2.eng.Hooks.MachineID = "laptopmachine"
	pc2.useLocation(shared, "My Drive › Shared", "")
	pc2.write("thesis.docx", "laptop data")
	// fixed: a folder inside another computer's backup is refused up front
	if s := pc2.run(Options{}); s.Result != "failed" || s.Reason != "location_inside" {
		t.Fatalf("pc2 should refuse a folder inside PC1's mirror: %+v", s)
	}
	for p := range pc1.fd.Live() {
		if strings.Contains(p, "thesis.docx") {
			t.Errorf("laptop file uploaded although the folder was refused: %s", p)
		}
	}
	pc1.run(Options{FullCheck: true})
	if a := filesUnder(pc1.fd, "Selva Sync/TEST-PC/"+ArchiveFolderName); len(a) != 0 {
		t.Errorf("PC1 archived something: %v", a)
	}
}
