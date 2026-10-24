package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// LogLine is one activity entry. Key + Args let the app window show the
// message in the user's language; Text is the English version for the log file.
type LogLine struct {
	Seq   int64             `json:"seq"`
	Time  time.Time         `json:"time"`
	Level string            `json:"level"` // info, warn, error
	Key   string            `json:"key"`
	Args  map[string]string `json:"args,omitempty"`
}

var english = map[string]string{
	"run_start":           "Sync started ({mode})",
	"preview_start":       "Preview started",
	"run_end":             "Sync finished: {result}. New {uploaded}, updated {updated}, archived {archived}, failed {failed}, sent {bytes}",
	"preview_end":         "Preview ready: {new} new, {update} changed, {remove} removed ({bytes})",
	"blocked":             "Sync paused: {reason}",
	"source_missing":      "Folder not found (drive not connected?): {path}",
	"source_nested":       "Skipped because it is inside another selected folder: {path}",
	"uploaded":            "Uploaded {path} ({size})",
	"updated":             "Updated {path} ({size})",
	"archived":            "Moved to archive: {path}",
	"trashed":             "Moved to Drive trash: {path}",
	"version_saved":       "Old version saved in archive: {path}",
	"version_failed":      "Old version could not be saved in the archive: {path}",
	"source_new_drive":    "This drive is a different disk than before, so it was skipped to protect the first disk's backup. If you replaced the disk, remove the folder and add it again: {path}",
	"source_duplicate":    "Skipped because another selected folder already uses the same place on Drive: {path}",
	"moved":               "Moved on Drive (no upload needed): {from} → {path}",
	"relocated":           "Put back in its place on Drive: {path}",
	"restored_from_trash": "{n} files that were deleted on Google Drive will be uploaded again",
	"failed":              "Could not upload {path}: {error}",
	"skipped_big":         "Skipped (larger than limit): {path} ({size})",
	"skipped_cap":         "Skipped (monthly data limit reached): {path}",
	"cap_reached":         "Monthly data limit reached. Remaining files will upload next month or when you raise the limit.",
	"full_check":          "Checking the Drive copy for missing files",
	"archive_trimmed":     "Old archive removed: {name}",
	"signin_needed":       "Google sign-in expired. Please sign in again.",
	"drive_full":          "Google Drive storage is full.",
	"error":               "Error: {error}",
	"nothing":             "Everything is already up to date.",
	"folder_ready":        "Drive folder ready: {path}",
	"settings_saved":      "Settings saved",
	"signed_in":           "Signed in as {email}",
	"signed_out":          "Signed out",
}

// PruneOld deletes monthly log files older than about a year.
func (l *Logger) PruneOld(now time.Time) {
	if l.Dir == "" {
		return
	}
	cutoff := now.AddDate(-1, 0, 0).Format("2006-01")
	ents, _ := os.ReadDir(l.Dir)
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, "selvasync-") && strings.HasSuffix(n, ".log") {
			if m := strings.TrimSuffix(strings.TrimPrefix(n, "selvasync-"), ".log"); len(m) == 7 && m < cutoff {
				os.Remove(filepath.Join(l.Dir, n))
			}
		}
	}
}

// Format renders a log line in English.
func Format(key string, args map[string]string) string {
	t, ok := english[key]
	if !ok {
		t = key
	}
	for k, v := range args {
		t = strings.ReplaceAll(t, "{"+k+"}", v)
	}
	return t
}

// Logger keeps recent lines in memory for the app window and appends all
// lines to a monthly log file in the data folder.
type Logger struct {
	Dir   string
	mu    sync.Mutex
	seq   int64
	lines []LogLine
}

const keepLines = 500

func (l *Logger) Add(level, key string, args map[string]string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	ln := LogLine{Seq: l.seq, Time: time.Now(), Level: level, Key: key, Args: args}
	l.lines = append(l.lines, ln)
	if len(l.lines) > keepLines {
		l.lines = append([]LogLine(nil), l.lines[len(l.lines)-keepLines:]...)
	}
	if l.Dir != "" {
		name := filepath.Join(l.Dir, "selvasync-"+ln.Time.Format("2006-01")+".log")
		if f, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			fmt.Fprintf(f, "%s [%s] %s\r\n", ln.Time.Format("2006-01-02 15:04:05"), strings.ToUpper(level), Format(key, args))
			f.Close()
		}
	}
}

func (l *Logger) Info(key string, args map[string]string)  { l.Add("info", key, args) }
func (l *Logger) Warn(key string, args map[string]string)  { l.Add("warn", key, args) }
func (l *Logger) Error(key string, args map[string]string) { l.Add("error", key, args) }

// Since returns lines newer than seq.
func (l *Logger) Since(seq int64) []LogLine {
	l.mu.Lock()
	defer l.mu.Unlock()
	i := sort.Search(len(l.lines), func(i int) bool { return l.lines[i].Seq > seq })
	return append([]LogLine(nil), l.lines[i:]...)
}

// HumanBytes formats a byte count.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
