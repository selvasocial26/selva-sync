package engine

// Adversarial tests ("hostile tester"). Each TestBreak_* FAILS when the
// problem it describes is present. Run: go test -race -run TestBreak -v ./internal/engine/

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/selvasync/selvasync/internal/config"
	"github.com/selvasync/selvasync/internal/drive"
	"github.com/selvasync/selvasync/internal/fakedrive"
)

func (e *env) key(rel string) string { return RemotePrefix(e.src) + "/" + rel }

func (e *env) archivedCopies(rel string) []*fakedrive.File {
	date := time.Now().Format("2006-01-02")
	return e.fd.Live()["Selva Sync/TEST-PC/"+ArchiveFolderName+"/"+date+"/"+RemotePrefix(e.src)+"/"+rel]
}

func (e *env) writeBytes(rel string, b []byte, mt time.Time) {
	p := filepath.Join(e.src, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		e.t.Fatal(err)
	}
	if !mt.IsZero() {
		os.Chtimes(p, mt, mt)
	}
}

func randBytes(n int) []byte { b := make([]byte, n); rand.Read(b); return b }

func (e *env) findDrive(name string) *fakedrive.File {
	e.fd.Lock()
	defer e.fd.Unlock()
	for _, f := range e.fd.Files {
		if f.Name == name && !f.Trashed {
			return f
		}
	}
	return nil
}

// ---------- rename / move = full re-upload + archived copy ----------

func TestBreak_RenameFileReuploads(t *testing.T) {
	e := newEnv(t)
	e.writeBytes("video.mp4", randBytes(2<<20), time.Time{})
	e.run(Options{})
	before := e.fd.UploadedBytes
	os.Rename(filepath.Join(e.src, "video.mp4"), filepath.Join(e.src, "video-final.mp4"))
	s := e.run(Options{})
	wasted := e.fd.UploadedBytes - before
	if wasted > 0 {
		t.Errorf("rename of a 2 MiB file re-uploaded %d bytes (archived=%d uploaded=%d); Drive now stores the data twice (archive copy %d)",
			wasted, s.Archived, s.Uploaded, len(e.archivedCopies("video.mp4")))
	}
}

func TestBreak_RenameFolderReuploadsAll(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		e.writeBytes(fmt.Sprintf("Photos/img%d.jpg", i), randBytes(300<<10), time.Time{})
	}
	e.run(Options{})
	before := e.fd.UploadedBytes
	os.Rename(filepath.Join(e.src, "Photos"), filepath.Join(e.src, "Photos 2026"))
	s := e.run(Options{})
	if w := e.fd.UploadedBytes - before; w > 0 {
		t.Errorf("folder rename re-uploaded %d bytes (%d files) and archived the old folder (archived ops=%d)", w, s.Uploaded, s.Archived)
	}
}

func TestBreak_CaseOnlyRenameReuploads(t *testing.T) {
	e := newEnv(t)
	e.writeBytes("Report.docx", randBytes(1<<20), time.Time{})
	e.run(Options{})
	before := e.fd.UploadedBytes
	os.Rename(filepath.Join(e.src, "Report.docx"), filepath.Join(e.src, "report.docx"))
	s := e.run(Options{})
	if w := e.fd.UploadedBytes - before; w > 0 {
		t.Errorf("case-only rename re-uploaded %d bytes, archived=%d", w, s.Archived)
	}
}

func TestBreak_MoveAcrossSourcesReuploads(t *testing.T) {
	e := newEnv(t)
	src2 := t.TempDir()
	e.set.Sources = append(e.set.Sources, config.Source{ID: "b", Path: src2, Enabled: true})
	e.writeBytes("big.bin", randBytes(1<<20), time.Time{})
	e.run(Options{})
	before := e.fd.UploadedBytes
	os.Rename(filepath.Join(e.src, "big.bin"), filepath.Join(src2, "big.bin"))
	s := e.run(Options{})
	if w := e.fd.UploadedBytes - before; w > 0 {
		t.Errorf("moving a file between sources re-uploaded %d bytes, archived=%d", w, s.Archived)
	}
}

// ---------- change detection ----------

func TestBreak_SameSizeSameMtimeEditNeverUploaded(t *testing.T) {
	e := newEnv(t)
	mt := time.Now().Add(-time.Hour).Truncate(time.Second)
	e.writeBytes("ledger.dat", []byte("balance=1000"), mt)
	e.run(Options{})
	e.writeBytes("ledger.dat", []byte("balance=9999"), mt) // tool preserves mtime
	e.run(Options{})
	e.run(Options{FullCheck: true})
	if got := string(e.live("ledger.dat")[0].Data); got != "balance=9999" {
		t.Errorf("Drive keeps stale content %q forever (even after full check)", got)
	}
}

func TestBreak_DefaultExcludesSkipUserFolders(t *testing.T) {
	e := newEnv(t)
	e.set.Excludes = append([]string(nil), config.DefaultExcludes...)
	e.write("Clients/Windows/quotation.xlsx", "q") // a client project about windows/doors
	e.write("Office/ProgramData/budget.xlsx", "b") // user folder name
	e.write("Designs/door.part", "p")              // CAD part file
	e.write("Accounts/AppData/notes.txt", "n")     // any folder named AppData
	e.write("Projects/.cache/important.txt", "i")
	e.run(Options{})
	for _, rel := range []string{"Clients/Windows/quotation.xlsx", "Office/ProgramData/budget.xlsx", "Designs/door.part", "Accounts/AppData/notes.txt"} {
		if len(e.live(rel)) == 0 {
			t.Errorf("user file silently never backed up by default excludes: %s", rel)
		}
	}
}

// ---------- Drive-side trash / delete ----------

func TestBreak_UpdateGoesIntoTrashedDriveFile(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "v1")
	e.run(Options{})
	tf := e.live("a.txt")[0]
	e.fd.Lock()
	tf.Trashed = true
	e.fd.Unlock()
	time.Sleep(20 * time.Millisecond)
	e.write("a.txt", "version 2")
	s := e.run(Options{})
	if s.Updated == 1 && len(e.live("a.txt")) == 0 {
		t.Errorf("update reported success but the only Drive copy is in the trash (will be purged after 30 days): %+v", s)
	}
}

func TestBreak_TrashedDriveFolderNotRestored(t *testing.T) {
	e := newEnv(t)
	e.write("d/a.txt", "a")
	e.write("d/b.txt", "b")
	e.run(Options{})
	f := e.findDrive("d")
	e.fd.Lock()
	f.Trashed = true
	e.fd.Unlock()
	e.write("d/new.txt", "n")
	s := e.run(Options{})
	if len(e.live("d/a.txt")) == 0 {
		t.Errorf("after the user trashed a mirror folder, unchanged files are not re-uploaded (only new.txt=%d) until the 30-day full check — same length as Drive's 30-day trash purge: %+v",
			len(e.live("d/new.txt")), s)
	}
}

// ---------- KeepOldVersions copy spam ----------

func TestBreak_KeepOldVersionsCopiesOnEveryFailedRun(t *testing.T) {
	e := newEnv(t)
	e.set.KeepOldVersions = true
	e.writeBytes("db.pst", randBytes(500<<10), time.Time{})
	e.run(Options{})
	time.Sleep(20 * time.Millisecond)
	e.writeBytes("db.pst", randBytes(600<<10), time.Time{})
	e.fd.FailUploadStatus = 400 // persistent failure (e.g. file locked / rejected)
	for i := 0; i < 3; i++ {
		e.run(Options{})
	}
	if n := len(e.archivedCopies("db.pst")); n > 1 {
		t.Errorf("%d identical 'old version' copies of db.pst were made in the archive (one per failed run)", n)
	}
}

// ---------- sign-out / account switch during a run ----------

func TestBreak_SignOutDuringRunCausesDuplicates(t *testing.T) {
	e := newEnv(t)
	e.set.Parallel = 1
	for i, n := range []string{"a", "b", "c", "d"} {
		e.write("sub/"+n+".txt", strings.Repeat("x", i+1))
	}
	e.write("old.txt", "o")
	e.run(Options{})
	// new files appear; during the next run the user signs out and back in
	// (apiSignOut: Stop() then ForgetRemote() without waiting)
	e.write("sub/e.txt", "eeeeeeeeee")
	e.write("sub/f.txt", "fffffffffff")
	e.write("sub/g.txt", "ggggggggggggg")
	var once sync.Once
	e.fd.AfterCommit = func(name string) {
		if name == "e.txt" {
			once.Do(e.eng.ForgetRemote)
		}
	}
	e.run(Options{})
	e.fd.AfterCommit = nil
	s := e.run(Options{})
	t.Logf("run after re-sign-in: %+v; folders=%v", s, e.fd.Folders())
	e.noDuplicates()
	for p := range e.fd.Live() {
		if !strings.HasPrefix(p, "Selva Sync/") {
			t.Errorf("file written outside the Selva Sync folder: %s", p)
		}
	}
}

func TestBreak_AccountSwitchDuringRunLeavesFilesMissing(t *testing.T) {
	e := newEnv(t)
	e.set.Parallel = 1
	e.write("a.txt", "a")
	e.write("b.txt", "bb")
	e.write("c.txt", "ccc")
	fd2 := fakedrive.New()
	t.Cleanup(fd2.Close)
	var once sync.Once
	e.fd.AfterCommit = func(name string) {
		if name == "b.txt" {
			once.Do(func() {
				// FinishLogin with another account while a sync runs: ForgetRemote, new token
				e.eng.ForgetRemote()
				e.eng.Drive.APIBase = fd2.URL + "/drive/v3"
				e.eng.Drive.UploadBase = fd2.URL + "/upload/drive/v3"
			})
		}
	}
	e.run(Options{})
	e.run(Options{})
	live := fd2.Live()
	for p := range live {
		t.Logf("new account has: %s", p)
	}
	for _, n := range []string{"a.txt", "b.txt", "c.txt"} {
		if len(live[e.prefix()+"/"+n]) != 1 {
			t.Errorf("new account never receives %s (state thinks it is uploaded); new account has %d files", n, len(live))
		}
	}
}

// ---------- two Selva Sync installs with the same MachineGuid ----------

func TestBreak_SameMachineIDArchivesOtherPCsFiles(t *testing.T) {
	// Cloned Windows image (same MachineGuid) or two Windows users on one PC
	// (MachineGuid is HKLM, the single-instance mutex is Local\ per session).
	a := newEnv(t)
	a.write("pc-a-report.xlsx", "A")
	a.run(Options{})

	b := &env{t: t, fd: a.fd, src: a.src, data: t.TempDir(), set: a.set}
	b.newEngine() // same MachineID "testmachine01"
	os.Remove(filepath.Join(a.src, "pc-a-report.xlsx"))
	b.write("pc-b-report.xlsx", "B")
	s := b.run(Options{})
	if s.Archived > 0 || len(a.live("pc-a-report.xlsx")) == 0 {
		t.Errorf("PC B's first sync archived PC A's files from the mirror: %+v", s)
	}
}

// ---------- stale resumable session ----------

func TestBreak_PendingSessionResumesIntoArchive(t *testing.T) {
	old := drive.ChunkSize
	drive.ChunkSize = 256 << 10
	defer func() { drive.ChunkSize = old }()
	e := newEnv(t)
	big := randBytes(drive.SimpleUploadMax + 700_000)
	e.writeBytes("dir/big.bin", big, time.Now().Add(-time.Hour))
	e.fd.FailUploadStatus = 400 // upload interrupted after first chunk
	if s := e.run(Options{}); s.Failed != 1 {
		t.Fatalf("setup: %+v", s)
	}
	e.fd.FailUploadStatus = 0
	// user moves the folder away (e.g. to Recycle Bin) -> folder archived on Drive
	away := filepath.Join(t.TempDir(), "dir")
	os.Rename(filepath.Join(e.src, "dir"), away)
	if s := e.run(Options{}); s.Archived != 1 {
		t.Fatalf("setup archive: %+v", s)
	}
	// ... and restores it (mtime preserved)
	os.Rename(away, filepath.Join(e.src, "dir"))
	s := e.run(Options{})
	inMirror := len(e.live("dir/big.bin"))
	inArchive := len(e.archivedCopies("dir/big.bin"))
	if inMirror == 0 {
		t.Errorf("restored file reported uploaded (%+v) but is not in the mirror; it landed in the archive (%d copies) which is trashed after ArchiveDays", s, inArchive)
	}
	// next run thinks it's fine
	if s2 := e.run(Options{}); s2.Result == "nothing" && inMirror == 0 {
		t.Errorf("subsequent runs report 'nothing' while mirror copy is missing")
	}
}

// ---------- removable drive letter reuse ----------

func TestBreak_OtherUsbStickOverwritesCloudCopy(t *testing.T) {
	e := newEnv(t)
	vol := "STICK-A"
	e.eng.Hooks.VolumeID = func(string) string { return vol }
	e.write("Report.docx", "stick A: final thesis chapter")
	e.run(Options{})
	vol = "STICK-B" // colleague's stick gets the same letter, same file name
	e.write("Report.docx", "stick B: other")
	e.run(Options{})
	if got := string(e.live("Report.docx")[0].Data); got != "stick A: final thesis chapter" {
		t.Errorf("Drive copy of stick A's file was overwritten by stick B's content %q", got)
	}
}

// ---------- Drive-side rename of a mirror folder ----------

func TestBreak_DriveSideFolderRenameFullReupload(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 4; i++ {
		e.writeBytes(fmt.Sprintf("Accounts/f%d.xlsx", i), randBytes(200<<10), time.Time{})
	}
	e.run(Options{})
	f := e.findDrive("Accounts")
	e.fd.Lock()
	f.Name = "Accounts (old)" // user renames inside the mirror on Drive
	e.fd.Unlock()
	before := e.fd.UploadedBytes
	s := e.run(Options{FullCheck: true})
	if w := e.fd.UploadedBytes - before; w > 0 {
		t.Errorf("Drive-side folder rename -> full check re-uploaded %d bytes and archived the renamed folder (%+v)", w, s)
	}
}

// ---------- archive move fails forever when Drive file has another parent ----------

func TestBreak_ArchiveFailsForeverAfterUserMovedFileOnDrive(t *testing.T) {
	e := newEnv(t)
	e.fd.StrictParents = true
	e.write("a.txt", "a")
	e.run(Options{})
	e.fd.Lock()
	e.fd.Files["userfolder"] = &fakedrive.File{ID: "userfolder", Name: "My stuff", Mime: drive.FolderMime, Parents: []string{"root"}}
	e.fd.Unlock()
	f := e.live("a.txt")[0]
	e.fd.Lock()
	f.Parents = []string{"userfolder"}
	e.fd.Unlock()
	os.Remove(filepath.Join(e.src, "a.txt"))
	var fails []int
	for i := 0; i < 3; i++ {
		fails = append(fails, e.run(Options{}).Failed)
	}
	if fails[2] > 0 {
		t.Errorf("archiving fails on every run forever (failed per run: %v)", fails)
	}
}

func TestBreak_ArchiveFailsAfterMirrorFolderRecreated(t *testing.T) {
	e := newEnv(t)
	e.fd.StrictParents = true
	e.write("d/a.txt", "a")
	e.run(Options{})
	f := e.findDrive("d")
	e.fd.Lock()
	f.Trashed = true
	e.fd.Unlock()
	e.write("d/new.txt", "n") // causes "d" to be recreated with a new ID
	e.run(Options{})
	e.fd.Lock()
	f.Trashed = false // user restores the old folder from Drive trash
	e.fd.Unlock()
	os.Remove(filepath.Join(e.src, "d", "a.txt"))
	s1 := e.run(Options{})
	s2 := e.run(Options{})
	if s2.Failed > 0 {
		t.Errorf("archive of d/a.txt fails every run (%d, %d)", s1.Failed, s2.Failed)
	}
	// the weekly full check merges the folder the user restored from trash
	if s := e.run(Options{FullCheck: true}); s.Failed > 0 {
		t.Errorf("full check: %+v", s)
	}
	e.noDuplicates()
}

// ---------- two sources mapping to the same Drive path ----------

func TestBreak_PrefixCollisionPingPong(t *testing.T) {
	// On Windows: \\srv\share-x and \\srv-share\x both map to "Network-srv-share-x".
	// On Linux the same happens with a backslash in a folder name.
	base := t.TempDir()
	src1 := filepath.Join(base, `a\b`)
	src2 := filepath.Join(base, "a", "b")
	os.MkdirAll(src1, 0o755)
	os.MkdirAll(src2, 0o755)
	if RemotePrefix(src1) != RemotePrefix(src2) {
		t.Skip("no collision on this OS")
	}
	e := newEnv(t)
	e.set.Sources = []config.Source{{ID: "1", Path: src1, Enabled: true}, {ID: "2", Path: src2, Enabled: true}}
	os.WriteFile(filepath.Join(src1, "x.txt"), []byte("from source 1"), 0o644)
	os.WriteFile(filepath.Join(src2, "y.txt"), []byte("from source 2"), 0o644)
	var log []string
	for i := 0; i < 4; i++ {
		s := e.run(Options{})
		log = append(log, fmt.Sprintf("run%d up=%d arch=%d", i+1, s.Uploaded, s.Archived))
	}
	t.Log(log)
	if strings.Contains(strings.Join(log[1:], ","), "arch=2") || strings.Contains(strings.Join(log[1:], ","), "arch=1") {
		t.Errorf("two sources share one Drive folder and archive/re-upload each other every run: %v", log)
	}
}

// ---------- odd names / state corruption / concurrency (sanity) ----------

func TestBreak_OddNamesStable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("names with backslashes cannot exist on Windows")
	}
	e := newEnv(t)
	names := []string{"it's.txt", `back\slash.txt`, "ગુજરાતી ફાઇલ.txt", "emoji 😀.txt", "zero.bin", "O'Brien's docs/a.txt", `x\y'z/b.txt`, "trail .txt"}
	for _, n := range names {
		c := "c"
		if n == "zero.bin" {
			c = ""
		}
		e.write(n, c)
	}
	if s := e.run(Options{}); s.Uploaded != len(names) || s.Failed != 0 {
		t.Fatalf("first: %+v", s)
	}
	if s := e.run(Options{}); s.Result != "nothing" {
		t.Errorf("second run not idle: %+v", s)
	}
	if s := e.run(Options{FullCheck: true}); s.Result != "nothing" {
		t.Errorf("full check not idle: %+v", s)
	}
	os.WriteFile(filepath.Join(e.data, "state.json"), []byte(`{"files":{"x":`), 0o600) // corrupted
	e.newEngine()
	if s := e.run(Options{}); s.Result != "nothing" {
		t.Errorf("after corrupt state: %+v", s)
	}
	e.noDuplicates()
}

func TestBreak_TwoEnginesSameStateConcurrent(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 20; i++ {
		e.write(fmt.Sprintf("f%02d.txt", i), "data")
	}
	e2 := &env{t: t, fd: e.fd, src: e.src, data: e.data, set: e.set}
	e2.newEngine()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); e.eng.Run(context.Background(), Options{}) }()
	go func() { defer wg.Done(); e2.eng.Run(context.Background(), Options{}) }()
	wg.Wait()
	e.noDuplicates()
}

func TestBreak_SameDayArchiveDuplicateFolders(t *testing.T) {
	e := newEnv(t)
	e.write("old/x.txt", "1")
	e.run(Options{})
	os.RemoveAll(filepath.Join(e.src, "old"))
	e.run(Options{})
	e.write("old/x.txt", "2")
	e.run(Options{})
	os.RemoveAll(filepath.Join(e.src, "old"))
	e.run(Options{})
	date := time.Now().Format("2006-01-02")
	p := "Selva Sync/TEST-PC/" + ArchiveFolderName + "/" + date + "/" + RemotePrefix(e.src) + "/old"
	if n := e.fd.Folders()[p]; n > 1 {
		t.Errorf("archive day folder holds %d folders named 'old' (restore is ambiguous)", n)
	}
}

var _ = bytes.Equal
