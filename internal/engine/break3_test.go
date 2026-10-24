package engine

// Round-3 adversarial tests. Each TestBreak3_* FAILS when the problem exists.
// Run: go test -race -run TestBreak3 -v ./internal/engine/

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/selvasync/selvasync/internal/config"

	"github.com/selvasync/selvasync/internal/fakedrive"
)

// A. Second app instance (stale in-memory state) saves at an idle moment
// (MarkScheduled after a settings change / its own OnDone, SaveNow at
// Windows shutdown). The round-2 stamp reload then makes instance 1 LOAD
// that stale state -> files it uploaded are forgotten -> duplicates.
func TestBreak3_StaleInstanceIdleSaveCausesDuplicates(t *testing.T) {
	e := newEnv(t)
	e.write("f1.txt", "1")
	e.run(Options{})
	e2 := &env{t: t, fd: e.fd, src: e.src, data: e.data, set: e.set}
	e2.newEngine() // second copy of the app, loaded state with only f1
	time.Sleep(20 * time.Millisecond)
	e.write("f2.txt", "22")
	e.run(Options{}) // instance 1 uploads f2
	time.Sleep(20 * time.Millisecond)
	e2.eng.MarkScheduled(time.Now()) // instance 2: user saved settings (idle, no lock needed)
	s := e.run(Options{})            // instance 1: next scheduled run
	t.Logf("run after other instance's idle save: %+v", s)
	e.noDuplicates()
}

// B. Case-only folder rename where one file is not paired as a move
// (zero-byte files are never paired; same happens for a file edited at the
// time of the rename). ensureFolder renames the old Drive folder and reuses
// it, the shared-ID guard skips archiving it -> the old Drive file stays in
// the SAME folder next to the new upload.
func TestBreak3_CaseRenameUnpairedFileDuplicates(t *testing.T) {
	e := newEnv(t)
	e.fd.CaseInsensitiveNames = true
	e.write("Docs/a.txt", "aaa")
	e.write("Docs/empty.txt", "")
	e.write("Docs/edit.txt", "v1")
	e.run(Options{})
	if err := os.Rename(filepath.Join(e.src, "Docs"), filepath.Join(e.src, "docs")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	e.write("docs/edit.txt", "v2-edited") // edited around the rename
	s1 := e.run(Options{})
	s2 := e.run(Options{})
	for _, n := range []string{"a.txt", "empty.txt", "edit.txt"} {
		if c := e.mirrorCount("docs/" + n); c != 1 {
			t.Errorf("docs/%s has %d copies in the mirror (runs %+v / %+v)", n, c, s1, s2)
		}
	}
}

// C. Crash reindex trusts files created after prevAttempt-1min, but
// prevAttempt is just the LAST attempt: a run blocked by Wi-Fi/metered (or
// failed offline) after the crash moves LastAttempt forward, so files the
// crashed run uploaded become Seen=false and are never archived.
func TestBreak3_BlockedRunAfterCrashLosesSeen(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		e := newEnv(t)
		e.write("a.txt", "a")
		e.write("b.txt", "b")
		e.run(Options{})
		crashStart := time.Now().Add(-2 * time.Hour)
		// b was uploaded 10 minutes into the crashed run, then power cut
		e.fd.Lock()
		for _, f := range e.fd.Files {
			if f.Name == "b.txt" {
				f.Created = crashStart.Add(10 * time.Minute).UTC().Format(time.RFC3339Nano)
			}
		}
		e.fd.Unlock()
		e.eng.smu.Lock()
		delete(e.eng.state.Files, e.key("b.txt"))
		e.eng.state.RunInProgress = true
		e.eng.state.LastAttempt = crashStart
		e.eng.state.RunSince = crashStart // the crashed run recorded when it started
		e.eng.smu.Unlock()
		e.eng.SaveNow()
		if blocked {
			// after reboot the first run is blocked (hotspot / metered)
			e.eng.Hooks.Metered = func() bool { return true }
			if s := e.run(Options{}); s.Result != "blocked" {
				t.Fatalf("expected blocked: %+v", s)
			}
			e.eng.Hooks.Metered = nil
		}
		os.Remove(filepath.Join(e.src, "b.txt"))
		s1 := e.run(Options{})
		s2 := e.run(Options{})
		if len(e.live("b.txt")) != 0 {
			t.Errorf("blockedFirst=%v: deleted b.txt stays in mirror forever (runs %+v / %+v)", blocked, s1, s2)
		}
	}
}

// D. Move with content check: a file moved while still being written
// (size/mtime matched at scan, content differs at check) must end with the
// new content on Drive, the old copy archived, and no duplicate.
func TestBreak3_MoveFallbackArchivesAndUploads(t *testing.T) {
	e := newEnv(t)
	mt := time.Now().Add(-3 * time.Hour).Truncate(2 * time.Second)
	e.writeBytes("old/x.bin", []byte("AAAAAAAAAA"), mt)
	e.writeBytes("old/keep.txt", []byte("k"), time.Time{})
	e.run(Options{})
	os.RemoveAll(filepath.Join(e.src, "old"))
	e.writeBytes("new/x.bin", []byte("BBBBBBBBBB"), mt)
	s := e.run(Options{})
	f := e.live("new/x.bin")
	if len(f) != 1 || string(f[0].Data) != "BBBBBBBBBB" {
		t.Errorf("new/x.bin wrong: %d copies (%+v)", len(f), s)
	}
	if len(e.live("old/x.bin")) != 0 || len(e.live("old/keep.txt")) != 0 {
		t.Errorf("old folder not archived (%+v)", s)
	}
	if len(e.archivedAnywhere("x.bin")) != 1 {
		t.Errorf("old x.bin archived copies: %v", e.archivedAnywhere("x.bin"))
	}
	e.noDuplicates()
}

// E. trimArchive permanent delete: only date-named day folders older than
// the retention are deleted; a recent one and a non-date folder survive.
func TestBreak3_TrimDeletesOnlyOldDayFolders(t *testing.T) {
	e := newEnv(t)
	e.set.ArchiveDays = 30
	e.write("a.txt", "a")
	e.write("b.txt", "b")
	e.run(Options{})
	os.Remove(filepath.Join(e.src, "a.txt"))
	e.run(Options{}) // archive a.txt today
	e.fd.Lock()
	var archID string
	for _, f := range e.fd.Files {
		if f.Name == ArchiveFolderName {
			archID = f.ID
		}
	}
	e.fd.Unlock()
	if archID == "" {
		t.Fatal("no archive")
	}
	e.fd.Lock()
	old := time.Now().AddDate(0, 0, -60).UTC().Format(time.RFC3339Nano)
	e.fd.Files["u1"] = &fakedrive.File{ID: "u1", Name: "2020-01-01", Mime: "application/vnd.google-apps.folder", Parents: []string{archID}, Created: old}
	e.fd.Files["u2"] = &fakedrive.File{ID: "u2", Name: "my stuff", Mime: "application/vnd.google-apps.folder", Parents: []string{archID}, Created: old}
	e.fd.Unlock()
	e.eng.smu.Lock()
	e.eng.state.LastArchiveTrim = time.Time{}
	e.eng.smu.Unlock()
	os.Remove(filepath.Join(e.src, "b.txt"))
	e.run(Options{})
	e.fd.Lock()
	_, u1 := e.fd.Files["u1"]
	_, u2 := e.fd.Files["u2"]
	e.fd.Unlock()
	if u1 || !u2 {
		t.Errorf("trim: old day folder present=%v, user folder present=%v", u1, u2)
	}
	if len(e.archivedAnywhere("a.txt")) != 1 || len(e.archivedAnywhere("b.txt")) != 1 {
		t.Errorf("today's archive lost: a=%v b=%v", e.archivedAnywhere("a.txt"), e.archivedAnywhere("b.txt"))
	}
}

// F. LegacyMachineID is per-PC (no Windows user). A second Windows user on
// the same PC installing 2.0 adopts the first user's 1.0 folder (and retags
// it as theirs) while the first user is still on 1.0 / has not run 2.0 yet.
func TestBreak3_LegacyIDAdoptsOtherUsersFolder(t *testing.T) {
	const legacy = "legacy0001"
	a := newEnv(t)
	a.eng.Hooks = Hooks{MachineID: legacy} // what 1.0 tagged: machine=<legacy hash>
	a.write("userA.txt", "A")
	a.run(Options{})
	aComp := a.eng.state.ComputerID
	b := &env{t: t, fd: a.fd, src: t.TempDir(), data: t.TempDir(), set: a.set}
	b.set.Sources = []config.Source{{ID: "b", Path: b.src, Enabled: true}}
	b.newEngine()
	b.eng.Hooks = Hooks{MachineID: "userB-hash", InstallID: "instB", LegacyMachineID: legacy}
	b.write("userB.txt", "B")
	b.run(Options{})
	if b.eng.state.ComputerID == aComp {
		t.Errorf("user B (new 2.0 install) adopted user A's 1.0 folder %s", aComp)
	}
	// user A now upgrades in place: its 1.0 state.json (no "version") is kept
	raw, _ := os.ReadFile(filepath.Join(a.data, "state.json"))
	var m map[string]any
	json.Unmarshal(raw, &m)
	delete(m, "version")
	raw, _ = json.Marshal(m)
	os.WriteFile(filepath.Join(a.data, "state.json"), raw, 0o600)
	a2 := &env{t: t, fd: a.fd, src: a.src, data: a.data, set: a.set}
	a2.newEngine()
	a2.eng.Hooks = Hooks{MachineID: "userA-hash", InstallID: "instA", LegacyMachineID: legacy}
	before := a.fd.UploadedBytes
	a2.run(Options{})
	if a2.eng.state.ComputerID != aComp {
		t.Errorf("user A lost its folder after upgrade (new folder %s, old %s), re-uploaded %d bytes",
			a2.eng.state.ComputerID, aComp, a.fd.UploadedBytes-before)
	}
}

// G. AcceptDisk while a run is in progress: the run must not undo it, and a
// following run syncs the source.
func TestBreak3_AcceptDiskDuringRun(t *testing.T) {
	e := newEnv(t)
	disk := "DISK-1"
	e.eng.Hooks.VolumeID = func(string) string { return disk }
	e.write("a.txt", "a")
	e.run(Options{})
	disk = "DISK-2"
	e.write("b.txt", "b")
	e.run(Options{})
	if len(e.eng.MismatchedSources()) != 1 {
		t.Fatalf("expected mismatch, got %v", e.eng.MismatchedSources())
	}
	e.fd.AfterCommit = func(string) {}
	started := make(chan struct{})
	e.eng.Hooks.Metered = func() bool { close(started); time.Sleep(100 * time.Millisecond); return false }
	done := make(chan config.RunSummary)
	go func() { done <- e.run(Options{}) }()
	<-started
	e.eng.AcceptDisk(e.src)
	<-done
	e.eng.Hooks.Metered = nil
	s := e.run(Options{})
	if len(e.live("b.txt")) != 1 || len(e.eng.MismatchedSources()) != 0 {
		t.Errorf("after AcceptDisk during a run: b.txt copies=%d mismatched=%v (%+v)", len(e.live("b.txt")), e.eng.MismatchedSources(), s)
	}
}
