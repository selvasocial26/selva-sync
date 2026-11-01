package engine

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/selvasync/selvasync/internal/config"
	"github.com/selvasync/selvasync/internal/drive"
	"github.com/selvasync/selvasync/internal/fakedrive"
)

// shortcuts returns the Selva Sync shortcuts in this computer's folder,
// as name -> target folder ID.
func (e *env) shortcuts() map[string]string {
	out := map[string]string{}
	e.fd.Lock()
	defer e.fd.Unlock()
	for _, f := range e.fd.Files {
		if f.Mime == drive.ShortcutMime && !f.Trashed && f.Props["selvasync"] == ShortcutProp {
			out[f.Name] = f.Target
		}
	}
	return out
}

func TestDriveLabel(t *testing.T) {
	for in, want := range map[string]string{
		"C-Drive":           "Local Disk (C)",
		"D-Drive":           "Local Disk (D)",
		"Network-nas-share": "Network (nas-share)",
		"Root":              "Root",
	} {
		if got := driveLabel(in); got != want {
			t.Errorf("driveLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWantedShortcuts(t *testing.T) {
	// Paths are built through RemotePrefix so the test means the same thing
	// on Windows (where it yields "C-Drive/...") and on Linux (where the
	// tests run and it yields "Root/...").
	e := &Engine{Log: &Logger{}}
	paths := []string{
		filepath.Join("Users", "Selva", "Documents"),
		filepath.Join("Work", "Reports"),
		filepath.Join("Old", "Reports"),
	}
	folders := map[string]string{}
	var srcs []config.Source
	for i, p := range paths {
		pre := RemotePrefix(p)
		folders[pre] = "id" + strconv.Itoa(i)
		folders[strings.Split(pre, "/")[0]] = "idHead"
		srcs = append(srcs, config.Source{ID: strconv.Itoa(i), Path: p, Enabled: true})
	}
	set := config.Defaults()
	set.Sources = srcs
	got := e.wantedShortcuts(set, folders)

	head := strings.Split(RemotePrefix(paths[0]), "/")[0]
	if got[driveLabel(head)] != "idHead" {
		t.Errorf("no drive shortcut: %v", got)
	}
	if got["Documents"] != "id0" {
		t.Errorf("Documents: %v", got)
	}
	// "Reports" appears twice, so each keeps its own parent and neither is lost
	if got["Work - Reports"] != "id1" || got["Old - Reports"] != "id2" {
		t.Errorf("duplicate names not separated: %v", got)
	}
	seen := map[string]bool{}
	for name, id := range got {
		if seen[id] {
			t.Errorf("two shortcuts point at %s: %v", id, got)
		}
		if name == "" {
			t.Errorf("empty shortcut name: %v", got)
		}
		seen[id] = true
	}
}

func TestShortcutsSurviveAndFollowSources(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "a")
	if s := e.run(Options{}); s.Result != "ok" {
		t.Fatalf("first run: %+v", s)
	}
	sc := e.shortcuts()
	if len(sc) == 0 {
		t.Fatal("no shortcuts created")
	}
	// every shortcut points at a folder that really exists
	e.fd.Lock()
	for name, target := range sc {
		f, ok := e.fd.Files[target]
		if !ok || f.Mime != drive.FolderMime {
			e.fd.Unlock()
			t.Fatalf("shortcut %q points at %q which is not a folder", name, target)
		}
	}
	e.fd.Unlock()

	// a second run must not archive them or make a duplicate set
	e.write("b.txt", "b")
	if s := e.run(Options{}); s.Result != "ok" {
		t.Fatalf("second run: %+v", s)
	}
	again := e.shortcuts()
	if len(again) != len(sc) {
		t.Fatalf("shortcut count changed: %v -> %v", sc, again)
	}
	for name, target := range sc {
		if again[name] != target {
			t.Errorf("shortcut %q changed target: %q -> %q", name, target, again[name])
		}
	}
	for p := range e.fd.Live() {
		if strings.Contains(p, ArchiveFolderName) {
			t.Errorf("something was archived: %s", p)
		}
	}
	e.noDuplicates()
}

func TestShortcutsAreNotArchivedOrTouched(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "a")
	e.write("gone.txt", "g")
	if s := e.run(Options{}); s.Result != "ok" {
		t.Fatalf("first run: %+v", s)
	}
	before := e.shortcuts()
	if len(before) == 0 {
		t.Fatal("no shortcuts created")
	}

	// a shortcut the user made themselves, which Selva Sync must leave alone
	e.fd.Lock()
	compID := ""
	for _, f := range e.fd.Files {
		if f.Mime == drive.FolderMime && f.Name == "TEST-PC" {
			compID = f.ID
		}
	}
	e.fd.Unlock()
	if compID == "" {
		t.Fatal("computer folder not found")
	}
	mine := &fakedrive.File{ID: "usershortcut", Name: "My own link", Mime: drive.ShortcutMime,
		Parents: []string{compID}, Target: compID}
	e.fd.Lock()
	e.fd.Files[mine.ID] = mine
	e.fd.Unlock()

	// delete a file locally and force the full reconcile + archive pass
	os.Remove(filepath.Join(e.src, "gone.txt"))
	if s := e.run(Options{FullCheck: true}); s.Result != "ok" && s.Result != "nothing" {
		t.Fatalf("second run: %+v", s)
	}

	after := e.shortcuts()
	if len(after) != len(before) {
		t.Errorf("shortcut set changed: %v -> %v", before, after)
	}
	for name, target := range before {
		if after[name] != target {
			t.Errorf("shortcut %q changed: %q -> %q", name, target, after[name])
		}
	}
	e.fd.Lock()
	f := e.fd.Files["usershortcut"]
	e.fd.Unlock()
	if f == nil || f.Trashed || f.Name != "My own link" {
		t.Error("a shortcut the user made was modified or removed")
	}

	// the deleted file really was archived, so the sweep did run
	archived := false
	for p := range e.fd.Live() {
		if strings.Contains(p, ArchiveFolderName) && strings.Contains(p, "gone.txt") {
			archived = true
		}
		if strings.Contains(p, ArchiveFolderName) && strings.Contains(p, "Local Disk") {
			t.Errorf("a shortcut was archived: %s", p)
		}
	}
	if !archived {
		t.Error("the archive sweep did not run, so this test proves nothing")
	}

	// A shortcut must never be recorded as a backed-up file: nothing on the
	// PC corresponds to it, so it would sit in the sync state for ever and
	// be counted as a file that is on Drive.
	e.eng.smu.Lock()
	defer e.eng.smu.Unlock()
	for k, ent := range e.eng.state.Files {
		for _, target := range after {
			if ent.ID == target {
				t.Errorf("shortcut recorded as a file in the sync state: %s", k)
			}
		}
		if ent.ID == "usershortcut" {
			t.Errorf("the user's own shortcut was recorded as a file: %s", k)
		}
	}
	e.noDuplicates()
}
