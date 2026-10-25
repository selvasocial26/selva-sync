package engine

// Tests for version 1.2.4: OneDrive online-only files.

import (
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// cloudHooks makes every file whose name starts with "cloud" look like an
// online-only OneDrive placeholder, and records which files were freed.
func cloudHooks(e *env) *[]string {
	var mu sync.Mutex
	freed := []string{}
	isCloud := func(name string) bool { return strings.HasPrefix(name, "cloud") }
	e.eng.Hooks.SkipAttr = func(info fs.FileInfo) bool { return !info.IsDir() && isCloud(info.Name()) }
	e.eng.Hooks.CloudOnly = func(p string) bool { return isCloud(filepath.Base(p)) }
	e.eng.Hooks.FreeCloud = func(p string) { mu.Lock(); freed = append(freed, filepath.Base(p)); mu.Unlock() }
	return &freed
}

func TestV124_CloudFilesSkippedByDefault(t *testing.T) {
	e := newEnv(t)
	freed := cloudHooks(e)
	e.write("local.txt", "here")
	e.write("cloud1.txt", "only online")
	s := e.run(Options{})
	if s.Result != "ok" || s.Uploaded != 1 {
		t.Fatalf("run: %+v", s)
	}
	if len(*freed) != 0 {
		t.Fatalf("nothing should be freed when the option is off: %v", *freed)
	}
	// turning the option on later uploads the online-only file too
	e.set.CloudFiles = true
	s = e.run(Options{})
	if s.Result != "ok" || s.Uploaded != 1 {
		t.Fatalf("run with cloud files: %+v", s)
	}
	if len(*freed) != 1 || (*freed)[0] != "cloud1.txt" {
		t.Fatalf("online-only file not made online-only again: %v", *freed)
	}
	e.noDuplicates()
}

func TestV124_CloudFilesIncluded(t *testing.T) {
	e := newEnv(t)
	freed := cloudHooks(e)
	e.set.CloudFiles = true
	e.write("local.txt", "here")
	e.write("d/cloud2.txt", "only online")
	s := e.run(Options{})
	if s.Result != "ok" || s.Uploaded != 2 {
		t.Fatalf("run: %+v", s)
	}
	if len(*freed) != 1 || (*freed)[0] != "cloud2.txt" {
		t.Fatalf("freed: %v (local files must never be freed)", *freed)
	}
	// unchanged online-only files are not downloaded again, not even by
	// the monthly full check
	*freed = (*freed)[:0]
	s = e.run(Options{FullCheck: true})
	if (s.Result != "ok" && s.Result != "nothing") || s.Uploaded+s.Updated != 0 {
		t.Fatalf("second run should do nothing: %+v", s)
	}
	if len(*freed) != 0 {
		t.Fatalf("an unchanged online-only file was read again: %v", *freed)
	}
	e.noDuplicates()
}
