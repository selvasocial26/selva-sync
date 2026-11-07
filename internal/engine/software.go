package engine

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Programs and their building blocks are not what people keep a backup for:
// they can be downloaded again, and one unpacked project can hold tens of
// thousands of tiny files that make every later backup slower for no gain.
//
// A folder is left out only when the evidence is strong, because nothing
// asks the user at backup time. A folder the user chose by hand (Include)
// is never examined at all.
const (
	SoftwareProject = "project" // holds package.json, composer.json, go.mod ...
	SoftwareCode    = "code"    // almost everything in it is source code
	SoftwareProgram = "program" // an installed program: a .exe with its .dlls
	SoftwareCache   = "cache"   // a folder a program marked as its own cache
)

// projectMarkers name a folder as the top of a software project. Each is a
// file a developer tool creates, never something a person writes by hand.
var projectMarkers = map[string]bool{
	"package.json": true, "package-lock.json": true, "yarn.lock": true,
	"composer.json": true, "composer.lock": true,
	"go.mod": true, "cargo.toml": true, "pyproject.toml": true,
	"pom.xml": true, "build.gradle": true, "build.gradle.kts": true,
	"gemfile": true, "pubspec.yaml": true, "tsconfig.json": true,
	"cmakelists.txt": true, "dockerfile": true,
	"requirements.txt": true, "pipfile": true, "environment.yml": true,
}

// cacheTag is the standard marker a program drops in a folder to say "this
// is a cache, do not back it up". Honouring it is free and exact.
const cacheTag = "CACHEDIR.TAG"

// codeExts are file types that belong to software rather than to a person's
// work. Images and PDFs are deliberately absent: they are what documents are
// made of, and a folder full of them is never treated as software.
var codeExts = map[string]bool{
	".js": true, ".mjs": true, ".cjs": true, ".ts": true, ".tsx": true, ".jsx": true,
	".php": true, ".py": true, ".go": true, ".rs": true, ".java": true, ".kt": true,
	".swift": true, ".c": true, ".h": true, ".cpp": true, ".hpp": true, ".cs": true,
	".rb": true, ".vue": true, ".scss": true, ".sass": true, ".less": true,
	".css": true, ".html": true, ".htm": true, ".json": true, ".yml": true,
	".yaml": true, ".toml": true, ".lock": true, ".gradle": true, ".sql": true,
	".sh": true, ".bat": true, ".ps1": true, ".dll": true, ".so": true, ".dylib": true,
	".o": true, ".a": true, ".class": true, ".jar": true, ".pyc": true, ".map": true,
	".mako": true, ".blade": true, ".twig": true, ".ejs": true, ".mts": true, ".cts": true,
}

// documentExts are the files a person would be upset to lose. Office files,
// PDFs, photos and video only: formats that are never part of a code project
// in any number. PNG and SVG are left out on purpose, because projects are
// full of icons and would otherwise look like document folders.
var documentExts = map[string]bool{
	".doc": true, ".docx": true, ".xls": true, ".xlsx": true, ".ppt": true, ".pptx": true,
	".pdf": true, ".odt": true, ".ods": true, ".odp": true, ".rtf": true,
	".jpg": true, ".jpeg": true, ".heic": true, ".tif": true, ".tiff": true,
	".mp4": true, ".mov": true, ".avi": true, ".mkv": true, ".mp3": true, ".wav": true,
	".m4a": true, ".dwg": true, ".psd": true,
}

// A folder stops counting as software when the user's own documents are a
// real part of it: more than a handful, AND a fair share of everything in
// it. A website with 200 files and 8 photos in it is still a website; a
// person's folder of 8 reports and 3 scripts is theirs.
const (
	documentsKeepFolder = 5
	documentsKeepShare  = 0.2
)

// documentsRule reports whether the user's own files are enough of a folder
// for it to be left alone.
func documentsRule(docs, total int) bool {
	return docs > documentsKeepFolder && total > 0 &&
		float64(docs)/float64(total) >= documentsKeepShare
}

// codeShare and codeMinFiles decide the "almost all source code" test, which
// catches unpacked websites and projects that carry no marker file.
const (
	codeShare    = 0.7
	codeMinFiles = 20
	codeScanMax  = 400 // files looked at before deciding; keeps the scan cheap
)

// maxSoftwareListed caps the list one run can collect, so a drive full of
// projects cannot grow the state file without bound.
const maxSoftwareListed = 200

// softwareKind reports why a folder is software, or "" when it is not.
// entries are the folder's own entries, already read by the caller.
func softwareKind(dir string, entries []fs.DirEntry) string {
	docs, exes, dlls, total := 0, 0, 0, 0
	marker := false
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if e.IsDir() {
			continue
		}
		if e.Name() == cacheTag {
			return SoftwareCache
		}
		total++
		ext := filepath.Ext(name)
		switch {
		case projectMarkers[name]:
			marker = true
		case documentExts[ext]:
			docs++
		case ext == ".exe":
			exes++
		case ext == ".dll":
			dlls++
		}
	}
	// The user's own documents win over every signal below, when there are
	// enough of them to say the folder is really theirs.
	if documentsRule(docs, total) {
		return ""
	}
	if marker {
		return SoftwareProject
	}
	if exes > 0 && dlls >= 3 {
		return SoftwareProgram
	}
	if mostlyCode(dir) {
		return SoftwareCode
	}
	return ""
}

// mostlyCode walks a bounded part of the folder and reports whether source
// code accounts for almost everything in it.
func mostlyCode(dir string) bool {
	code, other, docs, seen := 0, 0, 0, 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		seen++
		ext := strings.ToLower(filepath.Ext(d.Name()))
		switch {
		case documentExts[ext]:
			docs++
			other++
		case codeExts[ext] || strings.HasPrefix(d.Name(), "."):
			code++
		default:
			other++
		}
		if seen >= codeScanMax {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil || seen < codeMinFiles || documentsRule(docs, seen) {
		return false
	}
	return float64(code)/float64(code+other) >= codeShare
}

// hasMarker reports whether a folder carries a developer's marker file. Such
// a folder is a software project even when the user's own documents keep it
// in the backup: the documents are theirs, the code is not.
func hasMarker(entries []fs.DirEntry) bool {
	for _, e := range entries {
		if !e.IsDir() && projectMarkers[strings.ToLower(e.Name())] {
			return true
		}
	}
	return false
}

// isCodeFile reports whether one file belongs to the software in a folder
// rather than to the person using it. Only used inside a folder that holds
// a marker file, never on its own.
func isCodeFile(name string) bool {
	n := strings.ToLower(name)
	if filepath.Ext(n) == ".sql" {
		return false // a database dump is data someone exported, not build output
	}
	return projectMarkers[n] || codeExts[filepath.Ext(n)]
}

// underAnyDir reports whether path sits inside one of the folders in set.
func underAnyDir(path string, set map[string]bool) bool {
	if len(set) == 0 {
		return false
	}
	for d := filepath.Dir(path); ; d = filepath.Dir(d) {
		if set[d] {
			return true
		}
		if parent := filepath.Dir(d); parent == d {
			return false
		}
	}
}

// included reports whether path is one of the folders the user asked for by
// hand, or sits inside one. Inside such a folder nothing is skipped: the
// user already said what they wanted.
func included(path string, includes []string) bool {
	p := foldCase(filepath.Clean(path))
	for _, inc := range includes {
		i := foldCase(filepath.Clean(strings.TrimSpace(inc)))
		if i == "" {
			continue
		}
		if p == i || strings.HasPrefix(p, strings.TrimRight(i, `\/`)+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// folderSize counts what a skipped folder holds, so the list the user can
// look at says how much was left out. The count stops at a sane limit: the
// exact number of files in a node_modules tree helps nobody.
const folderSizeMax = 20000

func folderSize(dir string) (files int, bytes int64, truncated bool) {
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		files++
		if info, e := d.Info(); e == nil {
			bytes += info.Size()
		}
		if files >= folderSizeMax {
			truncated = true
			return fs.SkipAll
		}
		return nil
	})
	return files, bytes, truncated
}

// readDirNames is a small helper so the walk can look at a folder's own
// entries without a second stat of every file.
func readDirEntries(dir string) []fs.DirEntry {
	f, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer f.Close()
	ents, err := f.ReadDir(-1)
	if err != nil {
		return nil
	}
	return ents
}
