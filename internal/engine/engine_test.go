package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/selvasync/selvasync/internal/config"
	"github.com/selvasync/selvasync/internal/drive"
	"github.com/selvasync/selvasync/internal/fakedrive"
)

type env struct {
	t     *testing.T
	fd    *fakedrive.Server
	src   string
	data  string
	set   config.Settings
	eng   *Engine
	store *config.Store
}

func newEnv(t *testing.T) *env {
	fd := fakedrive.New()
	t.Cleanup(fd.Close)
	e := &env{t: t, fd: fd, src: t.TempDir(), data: t.TempDir()}
	e.set = config.Defaults()
	e.set.ComputerName = "TEST-PC"
	e.set.Sources = []config.Source{{ID: "a", Path: e.src, Enabled: true}}
	e.set.Excludes = []string{"*.tmp", "skipme"}
	e.newEngine()
	return e
}

func (e *env) newEngine() {
	store, err := config.NewStore(e.data)
	if err != nil {
		e.t.Fatal(err)
	}
	e.store = store
	d := drive.New(func(ctx context.Context) (string, error) { return "test-token", nil })
	d.APIBase = e.fd.URL + "/drive/v3"
	d.UploadBase = e.fd.URL + "/upload/drive/v3"
	e.eng = New(store, d, func() config.Settings { return e.set }, Hooks{MachineID: "testmachine01"}, &Logger{})
}

func (e *env) write(rel, content string) {
	p := filepath.Join(e.src, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) run(opt Options) config.RunSummary {
	return e.eng.Run(context.Background(), opt)
}

func (e *env) prefix() string {
	return "Selva Sync/TEST-PC/" + RemotePrefix(e.src)
}

func (e *env) live(rel string) []*fakedrive.File {
	return e.fd.Live()[e.prefix()+"/"+rel]
}

func (e *env) noDuplicates() {
	for p, fs := range e.fd.Live() {
		if len(fs) > 1 {
			e.t.Errorf("duplicate file on Drive: %s (%d copies)", p, len(fs))
		}
	}
	for p, n := range e.fd.Folders() {
		if n > 1 {
			e.t.Errorf("duplicate folder on Drive: %s (%d copies)", p, n)
		}
	}
}

func TestSyncLifecycle(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "hello")
	e.write("docs/b.txt", "world")
	e.write("docs/deep/c.txt", "deep")
	e.write("docs/old/x.txt", "x")
	e.write("junk.tmp", "temp")
	e.write("skipme/secret.txt", "s")

	// 1. first sync uploads everything except excluded files
	sum := e.run(Options{})
	if sum.Result != "ok" || sum.Uploaded != 4 {
		t.Fatalf("first sync: %+v", sum)
	}
	if f := e.live("docs/deep/c.txt"); len(f) != 1 || string(f[0].Data) != "deep" {
		t.Fatalf("c.txt not uploaded correctly")
	}
	if len(e.live("junk.tmp")) != 0 || len(e.live("skipme/secret.txt")) != 0 {
		t.Fatal("excluded files were uploaded")
	}
	e.noDuplicates()

	// 2. nothing changed: nothing uploaded
	before := e.fd.UploadedBytes
	sum = e.run(Options{})
	if sum.Result != "nothing" || e.fd.UploadedBytes != before {
		t.Fatalf("second sync should do nothing: %+v", sum)
	}

	// 3. change a file: same Drive file is updated, no duplicate
	idBefore := e.live("a.txt")[0].ID
	time.Sleep(20 * time.Millisecond)
	e.write("a.txt", "hello again")
	sum = e.run(Options{})
	if sum.Updated != 1 || sum.Uploaded != 0 {
		t.Fatalf("update: %+v", sum)
	}
	f := e.live("a.txt")
	if len(f) != 1 || f[0].ID != idBefore || string(f[0].Data) != "hello again" {
		t.Fatalf("a.txt not updated in place")
	}

	// 4. delete a file and a folder: both go to the dated archive
	os.Remove(filepath.Join(e.src, "docs", "b.txt"))
	os.RemoveAll(filepath.Join(e.src, "docs", "old"))
	sum = e.run(Options{})
	if sum.Archived != 2 {
		t.Fatalf("archive: %+v", sum)
	}
	if len(e.live("docs/b.txt")) != 0 || len(e.live("docs/old/x.txt")) != 0 {
		t.Fatal("deleted files still in mirror")
	}
	date := time.Now().Format("2006-01-02")
	arch := "Selva Sync/TEST-PC/" + ArchiveFolderName + "/" + date + "/" + RemotePrefix(e.src)
	live := e.fd.Live()
	if len(live[arch+"/docs/b.txt"]) != 1 || len(live[arch+"/docs/old/x.txt"]) != 1 {
		t.Fatalf("archived files not found under %s", arch)
	}
	e.noDuplicates()

	// 5. lost state (reinstall): re-index from Drive, upload nothing
	os.Remove(filepath.Join(e.data, "state.json"))
	e.newEngine()
	before = e.fd.UploadedBytes
	sum = e.run(Options{})
	if sum.Result != "nothing" || e.fd.UploadedBytes != before {
		t.Fatalf("after state loss nothing should upload: %+v (bytes %d)", sum, e.fd.UploadedBytes-before)
	}
	e.noDuplicates()

	// 6. file deleted directly on Drive: full check uploads it again
	e.live("docs/deep/c.txt")[0].Trashed = true
	sum = e.run(Options{FullCheck: true})
	if sum.Uploaded != 1 || len(e.live("docs/deep/c.txt")) != 1 {
		t.Fatalf("full check did not restore: %+v", sum)
	}

	// 7. source folder missing (USB unplugged): nothing archived
	e.set.Sources = append(e.set.Sources, config.Source{ID: "b", Path: filepath.Join(e.data, "nope"), Enabled: true})
	sum = e.run(Options{})
	if sum.Archived != 0 {
		t.Fatalf("missing source must not archive: %+v", sum)
	}
	e.noDuplicates()
}

func TestDeleteModes(t *testing.T) {
	e := newEnv(t)
	e.write("k.txt", "keep")
	e.run(Options{})
	e.set.DeleteMode = "keep"
	os.Remove(filepath.Join(e.src, "k.txt"))
	if s := e.run(Options{}); s.Archived != 0 || len(e.live("k.txt")) != 1 {
		t.Fatalf("keep mode removed file: %+v", s)
	}
	e.set.DeleteMode = "trash"
	if s := e.run(Options{}); s.Archived != 1 || len(e.live("k.txt")) != 0 {
		t.Fatalf("trash mode: %+v", s)
	}
}

func TestKeepOldVersions(t *testing.T) {
	e := newEnv(t)
	e.set.KeepOldVersions = true
	e.write("v.txt", "v1")
	e.run(Options{})
	time.Sleep(20 * time.Millisecond)
	e.write("v.txt", "v2")
	e.run(Options{})
	date := time.Now().Format("2006-01-02")
	old := e.fd.Live()["Selva Sync/TEST-PC/"+ArchiveFolderName+"/"+date+"/"+RemotePrefix(e.src)+"/v.txt"]
	if len(old) != 1 || string(old[0].Data) != "v1" {
		t.Fatal("old version not kept")
	}
	if string(e.live("v.txt")[0].Data) != "v2" {
		t.Fatal("new version not uploaded")
	}
}

func TestResumableUploadWithFailure(t *testing.T) {
	old := drive.ChunkSize
	drive.ChunkSize = 256 << 10
	defer func() { drive.ChunkSize = old }()
	e := newEnv(t)
	big := make([]byte, drive.SimpleUploadMax+700_000)
	rand.Read(big)
	os.WriteFile(filepath.Join(e.src, "big.bin"), big, 0o644)
	e.fd.FailChunkOnce = true
	sum := e.run(Options{})
	if sum.Result != "ok" {
		t.Fatalf("big upload: %+v", sum)
	}
	f := e.live("big.bin")
	if len(f) != 1 || !bytes.Equal(f[0].Data, big) {
		t.Fatal("big file content wrong")
	}
}

func TestMonthlyCap(t *testing.T) {
	e := newEnv(t)
	e.set.MonthlyCapMB = 1
	e.write("small.txt", "abc")
	big := make([]byte, 2<<20)
	os.WriteFile(filepath.Join(e.src, "big.bin"), big, 0o644)
	sum := e.run(Options{})
	if sum.Result != "cap" || sum.Uploaded != 1 || sum.Skipped != 1 {
		t.Fatalf("cap: %+v", sum)
	}
	if len(e.live("big.bin")) != 0 {
		t.Fatal("file over cap uploaded")
	}
}

func TestMaxFileSizeAndNested(t *testing.T) {
	e := newEnv(t)
	e.set.MaxFileSizeMB = 1
	e.write("sub/ok.txt", "ok")
	os.WriteFile(filepath.Join(e.src, "huge.bin"), make([]byte, 2<<20), 0o644)
	e.set.Sources = append(e.set.Sources, config.Source{ID: "n", Path: filepath.Join(e.src, "sub"), Enabled: true})
	sum := e.run(Options{})
	if sum.Uploaded != 1 || sum.Skipped != 1 {
		t.Fatalf("size/nested: %+v", sum)
	}
	e.noDuplicates()
}

func TestBlockedNetwork(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "a")
	e.eng.Hooks.Metered = func() bool { return true }
	e.eng.Hooks.WifiSSID = func() (string, bool) { return "PhoneHotspot", true }
	if s := e.run(Options{}); s.Result != "blocked" || s.Reason != "metered" {
		t.Fatalf("metered: %+v", s)
	}
	e.set.PauseOnMetered = false
	e.set.BlockedWifi = []string{"phonehotspot"}
	if s := e.run(Options{}); s.Result != "blocked" || s.Reason != "wifi_blocked" {
		t.Fatalf("wifi: %+v", s)
	}
	if s := e.run(Options{Force: true}); s.Result != "ok" {
		t.Fatalf("force: %+v", s)
	}
}

func TestPreview(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "a")
	e.write("b/c.txt", "c")
	before := e.fd.UploadedBytes
	e.run(Options{DryRun: true})
	pv := e.eng.LastPreview()
	if pv == nil || pv.New != 2 || e.fd.UploadedBytes != before {
		t.Fatalf("preview: %+v", pv)
	}
}

func TestRenameDriveFolders(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "a")
	e.run(Options{})
	e.set.DriveFolderName = "Office Backup"
	e.set.ComputerName = "Accounts-PC"
	e.run(Options{})
	if len(e.fd.Live()["Office Backup/Accounts-PC/"+RemotePrefix(e.src)+"/a.txt"]) != 1 {
		t.Fatal("rename not applied")
	}
	e.noDuplicates()
}

func TestRemotePrefix(t *testing.T) {
	cases := map[string]string{
		"/home/user/docs": "Root/home/user/docs",
	}
	for in, want := range cases {
		if got := RemotePrefix(in); got != want {
			t.Errorf("%s -> %s want %s", in, got, want)
		}
	}
}

func TestExcluded(t *testing.T) {
	p := []string{"*.tmp", "node_modules", "Projects/old/*", "~$*", "/Windows", "/Users/*/AppData"}
	for _, c := range []struct {
		name, rel, drel string
		want            bool
	}{
		{"a.TMP", "x/a.TMP", "D/x/a.TMP", true},
		{"node_modules", "src/node_modules", "D/src/node_modules", true},
		{"file.txt", "Projects/old/file.txt", "Projects/old/file.txt", true},
		{"~$report.docx", "~$report.docx", "~$report.docx", true},
		{"report.docx", "report.docx", "report.docx", false},
		{"Windows", "Windows", "Windows", true},                         // C:\Windows
		{"Windows", "Clients/Windows", "Clients/Windows", false},        // D:\Clients\Windows
		{"AppData", "AppData", "Users/Selva/AppData", true},             // source C:\Users\Selva
		{"AppData", "Accounts/AppData", "Data/Accounts/AppData", false}, // user folder
		{"notes.txt", "Selva/AppData/x/notes.txt", "Users/Selva/AppData/x/notes.txt", true},
	} {
		if got := excluded(p, c.name, c.rel, c.drel); got != c.want {
			t.Errorf("%s (%s): got %v", c.rel, c.drel, got)
		}
	}
}

func TestLimiter(t *testing.T) {
	l := &Limiter{Rate: func() int64 { return 200 * 1024 }}
	r := l.Wrap(context.Background(), bytes.NewReader(make([]byte, 600*1024)), nil)
	start := time.Now()
	buf := make([]byte, 32*1024)
	for {
		if _, err := r.Read(buf); err != nil {
			break
		}
	}
	el := time.Since(start)
	// 600 KB at 200 KB/s with up to 1 s burst: about 2 s
	if el < 1500*time.Millisecond || el > 4*time.Second {
		t.Fatalf("limiter took %v", el)
	}
	if !isNight(time.Date(2026, 1, 1, 23, 0, 0, 0, time.Local), "22:00", "07:00") || isNight(time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local), "22:00", "07:00") {
		t.Fatal("night window wrong")
	}
	_ = strings.TrimSpace
}

// ---- scenarios found in code review ----

func TestJunctionSourceNotArchived(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "a")
	e.write("d/b.txt", "b")
	if s := e.run(Options{}); s.Uploaded != 2 {
		t.Fatalf("%+v", s)
	}
	// user moves the folder to another disk and leaves a link behind
	moved := filepath.Join(t.TempDir(), "moved")
	if err := os.Rename(e.src, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, e.src); err != nil {
		t.Fatal(err)
	}
	s := e.run(Options{})
	if s.Archived != 0 || s.Uploaded != 0 || len(e.live("a.txt")) != 1 {
		t.Fatalf("link source mishandled: %+v", s)
	}
}

func TestSameComputerNameKeepsSeparateFolders(t *testing.T) {
	a := newEnv(t)
	a.eng.Hooks.MachineID = "aaaaaaaaaaaa"
	a.write("from-a.txt", "A")
	a.run(Options{})

	// second PC, same Google account, same computer name, same folder path
	b := &env{t: t, fd: a.fd, src: a.src, data: t.TempDir(), set: a.set}
	b.newEngine()
	b.eng.Hooks.MachineID = "bbbbbbbbbbbb"
	os.Remove(filepath.Join(a.src, "from-a.txt"))
	b.write("from-b.txt", "B")
	s := b.run(Options{})
	if s.Archived != 0 {
		t.Fatalf("PC B archived PC A's files: %+v", s)
	}
	if len(a.live("from-a.txt")) != 1 {
		t.Fatal("PC A's file disappeared")
	}
	found := false
	for p := range a.fd.Live() {
		if strings.HasPrefix(p, "Selva Sync/TEST-PC (bbbbbb)/") && strings.HasSuffix(p, "from-b.txt") {
			found = true
		}
	}
	if !found {
		t.Fatalf("PC B should get its own folder; have %v", a.fd.Folders())
	}
	// PC A reinstalls: finds its own folder again by machine tag
	os.Remove(filepath.Join(a.data, "state.json"))
	a.newEngine()
	a.eng.Hooks.MachineID = "aaaaaaaaaaaa"
	a.write("from-a.txt", "A")
	if s := a.run(Options{}); s.Uploaded != 0 || s.Archived != 1 {
		// from-b.txt is not on PC A's disk... but lives in B's folder, so A only
		// sees its own files: nothing new to upload, nothing of B's archived.
		if s.Archived != 0 {
			t.Fatalf("reinstalled PC A touched other files: %+v", s)
		}
	}
}

func TestCrashDoesNotDuplicate(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "a")
	e.write("b.txt", "b")
	e.run(Options{})
	// simulate a power cut: b.txt was uploaded but the state was not saved
	e.eng.smu.Lock()
	delete(e.eng.state.Files, e.prefix()[len("Selva Sync/TEST-PC/"):]+"/b.txt")
	e.eng.state.RunInProgress = true
	e.eng.smu.Unlock()
	e.run(Options{})
	e.noDuplicates()
}

func TestLostReplyDoesNotDuplicate(t *testing.T) {
	e := newEnv(t)
	e.fd.GenerateIDs = true
	e.write("a.txt", "a")
	e.fd.DropCreateOnce = true
	s := e.run(Options{})
	if s.Uploaded != 1 || s.Failed != 0 {
		t.Fatalf("%+v", s)
	}
	e.noDuplicates()
}

func TestUserDeletedFoldersOnDrive(t *testing.T) {
	e := newEnv(t)
	e.write("d/a.txt", "a")
	e.write("x.txt", "x")
	e.run(Options{})
	os.Remove(filepath.Join(e.src, "x.txt"))
	e.run(Options{}) // creates the archive folder
	// user trashes the archive folder and the "d" mirror folder on Drive
	for _, f := range e.fd.Files {
		if f.Name == ArchiveFolderName || f.Name == "d" {
			f.Trashed = true
		}
	}
	e.write("d/new.txt", "n")
	os.Remove(filepath.Join(e.src, "d", "a.txt"))
	s := e.run(Options{})
	if s.Failed != 0 {
		t.Fatalf("%+v", s)
	}
	if len(e.live("d/new.txt")) != 1 {
		t.Fatal("new file not uploaded into recreated folder")
	}
	e.noDuplicates()
}

func TestOtherDiskSameLetterNotArchived(t *testing.T) {
	e := newEnv(t)
	vol := "AAAA1111"
	e.eng.Hooks.VolumeID = func(string) string { return vol }
	e.write("a.txt", "a")
	e.run(Options{})
	vol = "BBBB2222" // a different USB stick got the same letter
	os.Remove(filepath.Join(e.src, "a.txt"))
	if s := e.run(Options{}); s.Archived != 0 {
		t.Fatalf("archived files of the other disk: %+v", s)
	}
}
