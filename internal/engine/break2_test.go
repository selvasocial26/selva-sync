package engine

// Round-2 adversarial tests against the patched engine. Each TestBreak2_*
// FAILS when the problem it describes is present.
// Run: go test -race -run TestBreak2 -v ./internal/engine/

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func (e *env) mirrorCount(rel string) int {
	n := 0
	for p, fs := range e.fd.Live() {
		if strings.EqualFold(p, e.prefix()+"/"+rel) {
			n += len(fs)
		}
	}
	return n
}

func (e *env) archivedAnywhere(name string) []string {
	var out []string
	for p := range e.fd.Live() {
		if strings.Contains(p, ArchiveFolderName) && path_base(p) == name {
			out = append(out, p)
		}
	}
	return out
}

func path_base(p string) string { return p[strings.LastIndex(p, "/")+1:] }

// 1. Move detection pairs two DIFFERENT files that merely share size+mtime:
// the new file's Drive copy silently holds the deleted file's content.
func TestBreak2_MoveFalsePositiveWrongContent(t *testing.T) {
	e := newEnv(t)
	mt := time.Now().Add(-2 * time.Hour).Truncate(2 * time.Second) // FAT/exFAT: 2 s resolution
	e.writeBytes("invoices/inv-001.pdf", []byte("INVOICE 001 amount 5000"), mt)
	e.run(Options{})
	os.Remove(filepath.Join(e.src, "invoices", "inv-001.pdf"))
	e.writeBytes("invoices/inv-002.pdf", []byte("INVOICE 002 amount 7000"), mt) // same size, same time
	s := e.run(Options{})
	// weekly full check (not the monthly deep check)
	e.eng.smu.Lock()
	e.eng.state.LastFullCheck = time.Time{}
	e.eng.smu.Unlock()
	s2 := e.run(Options{})
	f := e.live("invoices/inv-002.pdf")
	if len(f) != 1 || string(f[0].Data) != "INVOICE 002 amount 7000" {
		got := ""
		if len(f) == 1 {
			got = string(f[0].Data)
		}
		t.Errorf("Drive inv-002.pdf holds %q (moved=%d, then weekly check %+v); deleted inv-001 archived copies=%d",
			got, s.Moved, s2.Result, len(e.archivedAnywhere("inv-001.pdf")))
	}
}

// 2. Case-only folder rename ("Docs" -> "docs") on a case-insensitive Drive
// name lookup: the files are "moved" into the SAME Drive folder, which is
// then archived as the "gone" folder - taking the just-moved files with it.
func TestBreak2_CaseOnlyFolderRenameArchivesLiveFiles(t *testing.T) {
	e := newEnv(t)
	e.fd.CaseInsensitiveNames = true
	e.write("Docs/a.txt", "aaa")
	e.run(Options{})
	if err := os.Rename(filepath.Join(e.src, "Docs"), filepath.Join(e.src, "docs")); err != nil {
		t.Fatal(err)
	}
	s := e.run(Options{})
	if n := e.mirrorCount("docs/a.txt"); n != 1 {
		t.Errorf("after case-only folder rename the mirror has %d copies of docs/a.txt (%+v); archived at %v",
			n, s, e.archivedAnywhere("a.txt"))
	}
	e.write("docs/new.txt", "n")
	s = e.run(Options{})
	if n := e.mirrorCount("docs/new.txt"); n != 1 {
		t.Errorf("new file in docs not in mirror (%+v); it went to %v", s, e.archivedAnywhere("new.txt"))
	}
}

// 3. The README-supported "move the folder to another disk and leave a
// junction" case: the VolumeID check sees a different disk and skips the
// source forever. New files are never backed up again.
func TestBreak2_JunctionToOtherDiskNeverSyncedAgain(t *testing.T) {
	e := newEnv(t)
	other := t.TempDir() // "E:" disk
	e.eng.Hooks.VolumeID = func(p string) string {
		if strings.HasPrefix(p, other) {
			return "DISK-E"
		}
		return "DISK-D"
	}
	e.write("a.txt", "a")
	e.run(Options{})
	moved := filepath.Join(other, "moved")
	if err := os.Rename(e.src, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, e.src); err != nil {
		t.Fatal(err)
	}
	e.write("new.txt", "n")
	var s = e.run(Options{})
	s = e.run(Options{})
	if len(e.live("new.txt")) != 1 {
		t.Errorf("source behind a junction to another disk is skipped forever: %+v (new.txt not on Drive)", s)
	}
}

// 4. FAT/exFAT daylight-saving shift: the 1-hour rule needs an MD5 in state,
// but upload() never stores one, so every file uploaded since the last
// weekly check is re-uploaded in full.
func TestBreak2_DSTShiftReuploadsRecentFiles(t *testing.T) {
	e := newEnv(t)
	mt := time.Now().Add(-48 * time.Hour).Truncate(2 * time.Second)
	e.writeBytes("photos/p1.jpg", randBytes(1<<20), mt)
	e.run(Options{})
	p := filepath.Join(e.src, "photos", "p1.jpg")
	os.Chtimes(p, mt.Add(time.Hour), mt.Add(time.Hour))
	before := e.fd.UploadedBytes
	s := e.run(Options{})
	if w := e.fd.UploadedBytes - before; w > 0 {
		t.Errorf("1-hour DST shift re-uploaded %d bytes (%+v)", w, s)
	}
}

// 5. State written by v1.0 has no "seen" flag: after the upgrade, files
// deleted or renamed before the first scan are never archived/moved; the
// mirror keeps them forever (rename = duplicate data in the mirror).
func TestBreak2_UpgradedStateNeverArchivesOrMoves(t *testing.T) {
	e := newEnv(t)
	e.write("old-report.txt", "r")
	e.writeBytes("video.mp4", randBytes(1<<20), time.Time{})
	e.run(Options{})
	e.eng.smu.Lock()
	for k, fe := range e.eng.state.Files {
		fe.Seen = false // what LoadState gives for a v1.0 state.json
		e.eng.state.Files[k] = fe
	}
	e.eng.smu.Unlock()
	e.eng.SaveNow()
	// a real v1.0 state.json has no "version" and no "seen" fields
	raw, _ := os.ReadFile(filepath.Join(e.data, "state.json"))
	var m map[string]any
	json.Unmarshal(raw, &m)
	delete(m, "version")
	raw, _ = json.Marshal(m)
	os.WriteFile(filepath.Join(e.data, "state.json"), raw, 0o600)
	e.newEngine() // upgrade + restart
	os.Remove(filepath.Join(e.src, "old-report.txt"))
	os.Rename(filepath.Join(e.src, "video.mp4"), filepath.Join(e.src, "video-final.mp4"))
	before := e.fd.UploadedBytes
	s := e.run(Options{})
	e.run(Options{})
	e.eng.smu.Lock()
	e.eng.state.LastFullCheck = time.Time{}
	e.eng.smu.Unlock()
	e.run(Options{}) // weekly check too
	if len(e.live("old-report.txt")) != 0 || len(e.live("video.mp4")) != 0 {
		t.Errorf("after upgrade, deleted/renamed files stay in the mirror forever (old-report=%d video.mp4=%d video-final=%d), re-uploaded %d bytes, first run %+v",
			len(e.live("old-report.txt")), len(e.live("video.mp4")), len(e.live("video-final.mp4")), e.fd.UploadedBytes-before, s)
	}
}

// 6. Crash after a file was uploaded but before the 15 s save: the crash
// reindex finds it with Seen=false; if it is deleted locally before the next
// run it is never archived.
func TestBreak2_FileUploadedBeforeCrashNeverArchived(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "a")
	e.write("b.txt", "b")
	e.run(Options{})
	e.eng.smu.Lock()
	delete(e.eng.state.Files, e.key("b.txt"))
	e.eng.state.RunInProgress = true
	e.eng.smu.Unlock()
	os.Remove(filepath.Join(e.src, "b.txt"))
	s1 := e.run(Options{})
	s2 := e.run(Options{})
	if len(e.live("b.txt")) != 0 {
		t.Errorf("deleted b.txt stays in the mirror forever (runs: %+v / %+v)", s1, s2)
	}
}

// 7. A run blocked by the data-folder lock still writes its (stale) state
// to state.json, wiping what the lock holder recorded. After a restart the
// lost entries are uploaded again as NEW files: duplicates.
func TestBreak2_BlockedRunOverwritesStateCausesDuplicates(t *testing.T) {
	e := newEnv(t)
	e.write("f1.txt", "1")
	e.run(Options{})
	e2 := &env{t: t, fd: e.fd, src: e.src, data: e.data, set: e.set}
	e2.newEngine() // second instance of the app, same data folder
	e.write("f2.txt", "22")
	e.run(Options{})                           // instance 1 uploads f2
	unlock, ok := e.store.TryLock("sync.lock") // instance 1 busy with another run
	if !ok {
		t.Fatal("lock")
	}
	s := e2.run(Options{})
	unlock()
	e.newEngine() // instance 1 restarts (e.g. PC reboot)
	s2 := e.run(Options{})
	t.Logf("blocked run: %s/%s; after restart: %+v", s.Result, s.Reason, s2)
	e.noDuplicates()
}
