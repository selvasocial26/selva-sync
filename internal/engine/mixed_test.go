package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The user's real case: a research folder with documents in it that also
// holds a small code project. The documents are backed up; the code is not.
func TestMixedFolderKeepsDocumentsDropsCode(t *testing.T) {
	e := newEnv(t)
	base := "Projects/GROW_Fisherfolk_study/"
	for _, f := range []string{"report.pdf", "survey.xlsx", "notes.docx", "draft.docx",
		"photos.jpg", "budget.xlsx", "minutes.docx"} {
		e.write(base+f, "real work")
	}
	for _, f := range []string{"package.json", "index.js", "src/app.ts", "style.css"} {
		e.write(base+f, "code")
	}
	if s := e.run(Options{}); s.Result != "ok" {
		t.Fatalf("run: %+v", s)
	}
	live := e.fd.Live()
	for _, f := range []string{"report.pdf", "survey.xlsx", "notes.docx", "photos.jpg"} {
		if len(live[e.prefix()+"/"+base+f]) == 0 {
			t.Errorf("the user's document was not backed up: %s", f)
		}
	}
	for _, f := range []string{"package.json", "index.js", "src/app.ts", "style.css"} {
		if len(live[e.prefix()+"/"+base+f]) > 0 {
			t.Errorf("a project file was backed up: %s", f)
		}
	}
	// nothing of the sort happens in an ordinary folder with no project in it
	e.write("Reports/summary.docx", "x")
	e.write("Reports/chart.css", "x")
	if s := e.run(Options{}); s.Result != "ok" {
		t.Fatalf("second run: %+v", s)
	}
	if len(e.fd.Live()[e.prefix()+"/Reports/chart.css"]) == 0 {
		t.Error("a file in an ordinary folder must be backed up, whatever its type")
	}
}

// A file that reads as empty must never replace a backup that has content.
func TestEmptyFileDoesNotReplaceBackup(t *testing.T) {
	e := newEnv(t)
	e.write("video.mov", "a real video")
	if s := e.run(Options{}); s.Result != "ok" {
		t.Fatalf("first run: %+v", s)
	}
	p := filepath.Join(e.src, "video.mov")
	if err := os.WriteFile(p, nil, 0o644); err != nil { // placeholder: reads as 0 bytes
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Minute)
	os.Chtimes(p, future, future)
	e.run(Options{})
	got := e.fd.Live()[e.prefix()+"/video.mov"]
	if len(got) == 0 {
		t.Fatal("the backup copy disappeared")
	}
	if len(got[0].Data) == 0 {
		t.Error("an empty file replaced the real one in the backup")
	}
	for p := range e.fd.Live() {
		if strings.Contains(p, ArchiveFolderName) {
			t.Errorf("the real file was archived: %s", p)
		}
	}
}
