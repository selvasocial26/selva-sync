package engine

import (
	"strconv"
	"testing"

	"github.com/selvasync/selvasync/internal/config"
)

// The twenty copies of a static website in the user's Downloads: no marker
// file, a handful of photos, and hundreds of code files. 1.3.1 kept them
// because of the photos; they are software.
func TestStaticSiteCopyIsSoftware(t *testing.T) {
	dir := t.TempDir()
	site := []string{"index.html", "favicon.svg", "gtc-mu-loader.php"}
	for i := 0; i < 40; i++ {
		site = append(site, "assets/p"+strconv.Itoa(i)+".js")
	}
	for i := 0; i < 8; i++ { // photos, but a small share of the whole
		site = append(site, "img/photo"+strconv.Itoa(i)+".jpg")
	}
	mk(t, dir, site...)
	if got := kindOf(t, dir); got != SoftwareCode {
		t.Errorf("a website with a few photos in it: got %q, want %q", got, SoftwareCode)
	}
}

// A person's folder keeps its documents even with code beside them.
func TestDocumentFolderStillWins(t *testing.T) {
	dir := t.TempDir()
	files := []string{"package.json", "build.js", "style.css"}
	for i := 0; i < 8; i++ {
		files = append(files, "report"+strconv.Itoa(i)+".pdf")
	}
	mk(t, dir, files...)
	if got := kindOf(t, dir); got != "" {
		t.Errorf("a folder that is mostly documents was called software: %q", got)
	}
}

// A folder a program marked as its own cache.
func TestCacheTagFolder(t *testing.T) {
	dir := mk(t, t.TempDir(), "CACHEDIR.TAG", "a.jpg", "b.jpg", "c.jpg",
		"d.jpg", "e.jpg", "f.jpg", "g.jpg")
	if got := kindOf(t, dir); got != SoftwareCache {
		t.Errorf("CACHEDIR.TAG folder: got %q, want %q", got, SoftwareCache)
	}
}

// Dot-files are app state, including the OneDrive lock file that failed the
// user's backup. A database dump is not: it is data someone exported.
func TestDotFilesAndSQL(t *testing.T) {
	e := newEnv(t)
	e.set.Excludes = append([]string(nil), config.DefaultExcludes...)
	e.write(".Rhistory", "x")
	e.write(".849C9593-D756-4E56-8D6E-42412F2A707B", "x")
	e.write("Projects/site/package.json", "{}")
	e.write("Projects/site/backup.sql", "INSERT INTO donors")
	for i := 0; i < 7; i++ {
		e.write("Projects/site/report"+strconv.Itoa(i)+".pdf", "work")
	}
	if s := e.run(Options{}); s.Result != "ok" {
		t.Fatalf("run: %+v", s)
	}
	live := e.fd.Live()
	for _, p := range []string{".Rhistory", ".849C9593-D756-4E56-8D6E-42412F2A707B"} {
		if len(live[e.prefix()+"/"+p]) > 0 {
			t.Errorf("a dot-file was backed up: %s", p)
		}
	}
	if len(live[e.prefix()+"/Projects/site/backup.sql"]) == 0 {
		t.Error("a database dump must be backed up: it is the user's data")
	}
	if len(live[e.prefix()+"/Projects/site/package.json"]) > 0 {
		t.Error("a project file inside the folder must not be backed up")
	}
}

// Uploads go folder by folder, not smallest first, so people watching a
// backup see their own files appear.
func TestUploadsFollowFolderOrder(t *testing.T) {
	plan := []*item{
		{op: "new", key: "C/zz/tiny.txt", size: 1},
		{op: "new", key: "C/aa/big.mov", size: 999999},
		{op: "new", key: "C/aa/also.docx", size: 5000},
	}
	sortPlan(plan)
	want := []string{"C/aa/also.docx", "C/aa/big.mov", "C/zz/tiny.txt"}
	for i, w := range want {
		if plan[i].key != w {
			t.Errorf("position %d = %s, want %s", i, plan[i].key, w)
		}
	}
}
