package engine

import (
	"strconv"
	"strings"
	"testing"

	"github.com/selvasync/selvasync/internal/config"
)

// A PC with one C: drive and the standard Windows layout, backed up with
// "Back up whole computer". The two tests below check the two halves of what
// the user sees: what is uploaded, and what the computer folder looks like.

// cDriveTree is the drive-relative path of every folder and file in the
// scenario, as Windows would report it under C:\.
var cDriveTree = []string{
	"Windows/System32/drivers/etc/hosts",
	"Program Files/Google/Chrome/chrome.exe",
	"Program Files (x86)/Steam/steam.exe",
	"ProgramData/Microsoft/defender.log",
	"$Recycle.Bin/S-1-5-21/deleted.txt",
	"pagefile.sys",
	"hiberfil.sys",
	"Users/Admin/Documents/report.docx",
	"Users/Admin/Documents/Accounts/ledger.xlsx",
	"Users/Admin/Desktop/notes.txt",
	"Users/Admin/Pictures/holiday.jpg",
	"Users/Admin/Videos/clip.mp4",
	"Users/Admin/Music/song.mp3",
	"Users/Admin/Downloads/SelvaSync.exe",
	"Users/Admin/Downloads/bank-statement.pdf",
	"Users/Admin/Downloads/scanned-id.jpg",
	"Users/Admin/Downloads/EmpowerNGO-main/test.v.02/storage/framework/cache/data/.gitignore",
	"Users/Admin/Downloads/archive/vendor/laravel/framework/src/Illuminate/Mail/resources/views/text/table.blade.php",
	"Users/Admin/.claude/remote-settings.json.synced-at",
	"Users/Admin/.cache/pip/http/a/b",
	"Users/Admin/AppData/Roaming/app/state.json",
	"Users/Default/NTUSER.DAT",
	"Users/Admin/Documents/~$draft.docx",
	"Users/Admin/Documents/scan.tmp",
}

// wantSkipped are the paths above that must never reach Google Drive.
var wantSkipped = map[string]bool{
	"Windows/System32/drivers/etc/hosts":     true,
	"Program Files/Google/Chrome/chrome.exe": true,
	"Program Files (x86)/Steam/steam.exe":    true,
	"ProgramData/Microsoft/defender.log":     true,
	"$Recycle.Bin/S-1-5-21/deleted.txt":      true,
	"pagefile.sys":                           true,
	"hiberfil.sys":                           true,
	"Users/Admin/Downloads/SelvaSync.exe":    true,
	"Users/Admin/Downloads/archive/vendor/laravel/framework/src/Illuminate/Mail/resources/views/text/table.blade.php": true,
	"Users/Admin/Downloads/EmpowerNGO-main/test.v.02/storage/framework/cache/data/.gitignore":                         true,
	"Users/Admin/.claude/remote-settings.json.synced-at":                                                              true,
	"Users/Admin/.cache/pip/http/a/b":                                                                                 true,
	"Users/Admin/AppData/Roaming/app/state.json":                                                                      true,
	"Users/Default/NTUSER.DAT":                                                                                        true,
	"Users/Admin/Documents/~$draft.docx":                                                                              true,
	"Users/Admin/Documents/scan.tmp":                                                                                  true,
}

// skippedOnCDrive walks the tree the way the engine does: a folder that is
// excluded is not descended into, so everything under it is skipped too.
func skippedOnCDrive(t *testing.T, rules []string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, p := range cDriveTree {
		segs := strings.Split(p, "/")
		for i := range segs {
			part := strings.Join(segs[:i+1], "/")
			// whole drive is the source, so rel and driveRel are the same
			if excluded(rules, segs[i], part, part) {
				out[p] = true
				break
			}
		}
	}
	return out
}

func TestCDriveWholeComputerExcludes(t *testing.T) {
	rules := config.WithSystemExcludes(config.Defaults().Excludes)
	got := skippedOnCDrive(t, rules)
	for _, p := range cDriveTree {
		if wantSkipped[p] && !got[p] {
			t.Errorf("uploaded but must be skipped: C:\\%s", strings.ReplaceAll(p, "/", `\`))
		}
		if !wantSkipped[p] && got[p] {
			t.Errorf("skipped but must be backed up: C:\\%s", strings.ReplaceAll(p, "/", `\`))
		}
	}
}

// Downloads itself is backed up: the things people actually keep there must
// survive, while the software that can be fetched again must not.
func TestCDriveDownloadsKeepsRealFiles(t *testing.T) {
	rules := config.WithSystemExcludes(config.Defaults().Excludes)
	got := skippedOnCDrive(t, rules)
	for _, p := range []string{"Users/Admin/Downloads/bank-statement.pdf", "Users/Admin/Downloads/scanned-id.jpg"} {
		if got[p] {
			t.Errorf("a real file in Downloads must be backed up: %s", p)
		}
	}
	for _, p := range []string{
		"Users/Admin/Downloads/SelvaSync.exe",
		"Users/Admin/Downloads/archive/vendor/laravel/framework/src/Illuminate/Mail/resources/views/text/table.blade.php",
	} {
		if !got[p] {
			t.Errorf("software in Downloads must be skipped: %s", p)
		}
	}
}

// An installer is software wherever it sits, not only in Downloads.
func TestInstallersSkippedAnywhere(t *testing.T) {
	rules := config.WithSystemExcludes(config.Defaults().Excludes)
	for _, name := range []string{"setup.exe", "office.msi", "ubuntu.iso", "backup.vhdx", "app.apk"} {
		if !excluded(rules, name, "Users/Admin/Documents/"+name, "Users/Admin/Documents/"+name) {
			t.Errorf("installer or disk image must be skipped: %s", name)
		}
	}
	for _, name := range []string{"report.docx", "photo.jpg", "notes.txt", "data.csv"} {
		if excluded(rules, name, "Users/Admin/Documents/"+name, "Users/Admin/Documents/"+name) {
			t.Errorf("an ordinary file must be backed up: %s", name)
		}
	}
}

// With the whole drive as the source, the computer folder must open with a
// shortcut per real user folder, not just one shortcut to the drive.
func TestCDriveWholeComputerShortcuts(t *testing.T) {
	e := &Engine{Log: &Logger{}}
	// folders as they exist on Drive after the run above: the head is what
	// RemotePrefix gives for the root of the only drive.
	head := RemotePrefix("/")
	folders := map[string]string{head: "idDrive"}
	n := 0
	for _, p := range cDriveTree {
		if wantSkipped[p] {
			continue
		}
		segs := strings.Split(p, "/")
		for i := range segs[:len(segs)-1] {
			key := head + "/" + strings.Join(segs[:i+1], "/")
			if _, ok := folders[key]; !ok {
				n++
				folders[key] = "id" + strconv.Itoa(n)
			}
		}
	}
	set := config.Defaults()
	set.Sources = []config.Source{{ID: "all", Path: "/", Enabled: true}}

	got := e.wantedShortcuts(set, folders)
	if got[driveLabel(head)] != "idDrive" {
		t.Errorf("no drive shortcut: %v", got)
	}
	for _, name := range []string{"Documents", "Desktop", "Pictures", "Videos", "Music"} {
		want := folders[head+"/Users/Admin/"+name]
		if got[name] != want {
			t.Errorf("shortcut %q = %q, want %q (all: %v)", name, got[name], want, got)
		}
	}
	if got["Downloads"] != folders[head+"/Users/Admin/Downloads"] {
		t.Errorf("Downloads is backed up, so it must get a shortcut: %v", got)
	}
	if _, ok := got["Accounts"]; ok {
		t.Error("only well-known user folders get automatic shortcuts")
	}
	seen := map[string]bool{}
	for name, id := range got {
		if name == "" || seen[id] {
			t.Errorf("bad shortcut set: %v", got)
		}
		seen[id] = true
	}
}
