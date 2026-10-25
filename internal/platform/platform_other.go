//go:build !windows

package platform

// Development stubs so the app can be built and tested on Linux / macOS.
// Selva Sync version 1 targets Windows.

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
)

func OpenURL(url string) {
	if os.Getenv("SELVASYNC_NO_BROWSER") != "" {
		println("Open:", url)
		return
	}
	cmd := "xdg-open"
	if runtime.GOOS == "darwin" {
		cmd = "open"
	}
	exec.Command(cmd, url).Start()
}

func OpenFolder(path string)                         { OpenURL(path) }
func OpenAppWindow(url string)                       { OpenURL(url) }
func MessageBox(title, text string, yesNo bool) bool { println(title + ": " + text); return false }
func SetStartup(enabled bool, exe string) error      { return nil }
func Protect(b []byte) ([]byte, error)               { return b, nil }
func Unprotect(b []byte) ([]byte, error)             { return b, nil }
func SkipAttr(fs.FileInfo) bool                      { return false }
func UserLanguage() string                           { return "en" }
func WifiSSID() (string, bool)                       { return os.Getenv("SELVASYNC_TEST_SSID"), true }
func Metered() bool                                  { return os.Getenv("SELVASYNC_TEST_METERED") != "" }
func SingleInstance() bool                           { return true }
func InstallDir() string                             { return filepath.Dir(os.Args[0]) }
func InstalledExe() string                           { p, _ := os.Executable(); return p }
func Install(self, version, publisher string) error  { return nil }
func Uninstall(removeData bool, dataDir string)      {}

func Drives() []DriveInfo {
	home, _ := os.UserHomeDir()
	return []DriveInfo{{Path: "/", Label: "Root", Kind: "fixed"}, {Path: home, Label: "Home", Kind: "fixed"}}
}

type Tray struct{ quit chan struct{} }

var theTray *Tray

func (t *Tray) SetTooltip(s string)       {}
func (t *Tray) SetIcon(ico []byte)        {}
func (t *Tray) Notify(title, text string) { println("NOTIFY:", title, "-", text) }
func (t *Tray) Quit() {
	if t != nil {
		select {
		case <-t.quit:
		default:
			close(t.quit)
		}
	}
}

func RunTray(cfg TrayConfig) *Tray {
	t := &Tray{quit: make(chan struct{})}
	theTray = t
	if cfg.OnReady != nil {
		go cfg.OnReady()
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	select {
	case <-sig:
	case <-t.quit:
	}
	return t
}

func Current() *Tray { return theTray }

func MachineID() string {
	b, err := os.ReadFile("/etc/machine-id")
	if err != nil || len(b) == 0 {
		return ""
	}
	sum := sha256.Sum256(append([]byte("selvasync:"), b...))
	return hex.EncodeToString(sum[:8])
}

func VolumeID(path string) string { return "" }

func InstalledVersion() string { return "" }

func LegacyMachineID() string { return "" }

func OnBattery() bool { return os.Getenv("SELVASYNC_TEST_BATTERY") != "" }
