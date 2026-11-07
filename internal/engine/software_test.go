package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mk builds a folder tree: each entry is a path relative to dir.
func mk(t *testing.T, dir string, files ...string) string {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func kindOf(t *testing.T, dir string) string {
	t.Helper()
	return softwareKind(dir, readDirEntries(dir))
}

// The real folders from the user's own PC that started all this.
func TestSoftwareDetection(t *testing.T) {
	base := t.TempDir()

	laravel := mk(t, filepath.Join(base, "EmpowerNGO-main"),
		"composer.json", "artisan", "app/Http/Controllers/loan.php",
		"storage/framework/cache/data/aa/bb/cc", "resources/views/home.blade.php")
	if got := kindOf(t, laravel); got != SoftwareProject {
		t.Errorf("unpacked Laravel project: got %q, want %q", got, SoftwareProject)
	}

	node := mk(t, filepath.Join(base, "VoiceStudio-main"),
		"package.json", "electron/src/main/index.ts", "changelog.d/3118.docs.md")
	if got := kindOf(t, node); got != SoftwareProject {
		t.Errorf("unpacked node project: got %q, want %q", got, SoftwareProject)
	}

	// A static website copy: no marker file at all, just code.
	var site []string
	for _, f := range []string{"index.html", "about.html", "contact.html", "style.css",
		"app.js", "script.js", "favicon.svg", "assets/main.css", "assets/app.js"} {
		site = append(site, f)
	}
	for i := 0; i < 15; i++ {
		site = append(site, filepath.Join("pages", "p"+string(rune('a'+i))+".html"))
	}
	gtc := mk(t, filepath.Join(base, "GTC-Final-Deploy"), site...)
	if got := kindOf(t, gtc); got != SoftwareCode {
		t.Errorf("static site copy: got %q, want %q", got, SoftwareCode)
	}

	prog := mk(t, filepath.Join(base, "Tally"),
		"tally.exe", "core.dll", "ui.dll", "net.dll", "data/company.900")
	if got := kindOf(t, prog); got != SoftwareProgram {
		t.Errorf("installed program: got %q, want %q", got, SoftwareProgram)
	}
}

// Nothing a person works with may ever be mistaken for software.
func TestUserFoldersAreNotSoftware(t *testing.T) {
	base := t.TempDir()

	docs := mk(t, filepath.Join(base, "Documents"),
		"report.docx", "budget.xlsx", "aadhaar.pdf", "photo.jpg", "notes.txt")
	if got := kindOf(t, docs); got != "" {
		t.Errorf("a documents folder was called software: %q", got)
	}

	// A working folder that happens to hold a script and a web page.
	mixed := mk(t, filepath.Join(base, "APF Project"),
		"proposal.docx", "budget.xlsx", "report.pdf", "photos/site.jpg",
		"beneficiaries.xlsx", "minutes.docx", "helper.py", "index.html")
	if got := kindOf(t, mixed); got != "" {
		t.Errorf("a folder with real work in it was called software: %q", got)
	}

	// Even with a marker file, documents win: this is someone's folder.
	withMarker := mk(t, filepath.Join(base, "Website work"),
		"package.json", "contract.pdf", "quote.docx", "invoice.pdf",
		"design.jpg", "brief.docx", "notes.pdf")
	if got := kindOf(t, withMarker); got != "" {
		t.Errorf("documents must outweigh a marker file: %q", got)
	}

	// Too few files to judge.
	small := mk(t, filepath.Join(base, "Scans"), "a.jpg", "b.jpg")
	if got := kindOf(t, small); got != "" {
		t.Errorf("a small folder was called software: %q", got)
	}
}

// A folder the user asked for by hand is never examined, and neither is
// anything inside it.
func TestIncludedFoldersWin(t *testing.T) {
	inc := []string{filepath.FromSlash("/home/selva/Projects")}
	for _, p := range []string{
		filepath.FromSlash("/home/selva/Projects"),
		filepath.FromSlash("/home/selva/Projects/selva-sync"),
		filepath.FromSlash("/home/selva/Projects/a/b/c"),
	} {
		if !included(p, inc) {
			t.Errorf("%s must count as included", p)
		}
	}
	for _, p := range []string{
		filepath.FromSlash("/home/selva/Downloads"),
		filepath.FromSlash("/home/selva/ProjectsOld"),
	} {
		if included(p, inc) {
			t.Errorf("%s must not count as included", p)
		}
	}
}

func TestFolderSize(t *testing.T) {
	dir := mk(t, t.TempDir(), "a.txt", "sub/b.txt", "sub/deep/c.txt")
	files, bytes, more := folderSize(dir)
	if files != 3 || bytes != 3 || more {
		t.Errorf("folderSize = %d files, %d bytes, more=%v; want 3, 3, false", files, bytes, more)
	}
}

// End to end: a backup that contains both an unpacked project and the
// user's own documents.
func TestSoftwareSkippedInRealRun(t *testing.T) {
	e := newEnv(t)
	e.write("Reports/budget.xlsx", "money")
	e.write("aadhaar.pdf", "id")
	e.write("VoiceStudio-main/package.json", "{}")
	e.write("VoiceStudio-main/src/index.ts", "code")
	e.write("VoiceStudio-main/changelog.d/3118.docs.md", "note")

	if s := e.run(Options{}); s.Result != "ok" {
		t.Fatalf("run: %+v", s)
	}
	live := e.fd.Live()
	for _, want := range []string{"Reports/budget.xlsx", "aadhaar.pdf"} {
		if len(live[e.prefix()+"/"+want]) == 0 {
			t.Errorf("the user's own file was not backed up: %s", want)
		}
	}
	for p := range live {
		if strings.Contains(p, "VoiceStudio-main") {
			t.Errorf("software was backed up: %s", p)
		}
	}
	sk := e.eng.SkippedSoftware()
	if len(sk) != 1 || !strings.HasSuffix(sk[0].Path, "VoiceStudio-main") || sk[0].Reason != SoftwareProject {
		t.Fatalf("skipped list: %+v", sk)
	}
	if sk[0].Files != 3 {
		t.Errorf("skipped file count = %d, want 3", sk[0].Files)
	}

	// The user asks for that folder after all: now it is backed up whole.
	e.set.IncludeFolders = []string{sk[0].Path}
	if s := e.run(Options{}); s.Result != "ok" {
		t.Fatalf("second run: %+v", s)
	}
	live = e.fd.Live()
	for _, want := range []string{
		"VoiceStudio-main/package.json",
		"VoiceStudio-main/src/index.ts",
		"VoiceStudio-main/changelog.d/3118.docs.md",
	} {
		if len(live[e.prefix()+"/"+want]) == 0 {
			t.Errorf("an included folder must be backed up whole, missing: %s", want)
		}
	}
	if len(e.eng.SkippedSoftware()) != 0 {
		t.Errorf("nothing should be listed as skipped now: %+v", e.eng.SkippedSoftware())
	}
	e.noDuplicates()
}

// Skipping must never archive what was backed up before the rule existed.
func TestSoftwareSkipDoesNotArchive(t *testing.T) {
	e := newEnv(t)
	e.set.IncludeFolders = []string{filepath.Join(e.src, "proj")}
	e.write("proj/package.json", "{}")
	e.write("proj/app.js", "code")
	if s := e.run(Options{}); s.Result != "ok" {
		t.Fatalf("first run: %+v", s)
	}
	// the user changes their mind: the folder is software again
	e.set.IncludeFolders = nil
	if s := e.run(Options{FullCheck: true}); s.Result != "ok" && s.Result != "nothing" {
		t.Fatalf("second run: %+v", s)
	}
	for p := range e.fd.Live() {
		if strings.Contains(p, ArchiveFolderName) {
			t.Errorf("a file already on Drive was archived: %s", p)
		}
	}
	if len(e.fd.Live()[e.prefix()+"/proj/app.js"]) == 0 {
		t.Error("the copy already on Drive must be left alone, not removed")
	}
}
