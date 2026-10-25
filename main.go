// Selva Sync - mirror selected folders of your PC to Google Drive on your
// own schedule, without using extra disk space and within your data limits.
//
// Created by Dr. Selvakumar Vellingiri. Free and open source (MIT licence).
package main

import (
	"bytes"
	_ "embed"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/selvasync/selvasync/internal/app"
	"github.com/selvasync/selvasync/internal/config"
	"github.com/selvasync/selvasync/internal/platform"
)

//go:embed assets/selvasync.ico
var iconICO []byte

//go:embed assets/selvasync-alert.ico
var alertICO []byte

func init() {
	// The Windows tray icon needs the main goroutine to stay on one OS thread.
	runtime.LockOSThread()
}

func main() {
	background := flag.Bool("background", false, "start in the tray without opening the window")
	uninstall := flag.Bool("uninstall", false, "remove Selva Sync from this computer")
	portable := flag.Bool("portable", false, "run without installing")
	flag.Parse()

	dataDir := config.DataDir()
	self, _ := os.Executable()
	tr := app.Translator(dataDir)

	if *uninstall {
		runUninstall(dataDir, tr)
		return
	}

	// First run from a download: install for this user (no admin needed),
	// then start the installed copy. Running a newer download updates it.
	if runtime.GOOS == "windows" && !*portable && !strings.EqualFold(filepath.Clean(self), filepath.Clean(platform.InstalledExe())) {
		if !sameFile(self, platform.InstalledExe()) {
			// Never replace the installed copy silently: ask first.
			old := platform.InstalledVersion()
			if _, err := os.Stat(platform.InstalledExe()); err == nil && old == "" {
				old = "?"
			}
			if old != "" {
				key := "msg_update_confirm"
				if app.CompareVersions(app.Version, old) < 0 {
					key = "msg_downgrade_confirm"
				} else if app.CompareVersions(app.Version, old) == 0 {
					key = "msg_reinstall_confirm"
				}
				if !platform.MessageBox("Selva Sync", tr(key, map[string]string{"old": old, "new": app.Version}), true) {
					return
				}
			} else if !platform.MessageBox("Selva Sync", tr("msg_install_confirm", map[string]string{"new": app.Version}), true) {
				// first install: say what will change on this PC and ask first
				return
			}
			if app.SignalRunning(dataDir, "quit") {
				for i := 0; i < 100; i++ { // let the running copy save and close
					if _, err := os.Stat(filepath.Join(dataDir, "instance.json")); err != nil {
						break
					}
					time.Sleep(100 * time.Millisecond)
				}
			}
			if err := platform.Install(self, app.Version, app.Author); err != nil {
				platform.MessageBox("Selva Sync", tr("msg_install_failed", map[string]string{"error": err.Error()}), false)
				return
			}
		}
		exec.Command(platform.InstalledExe()).Start()
		return
	}

	if !platform.SingleInstance() {
		app.SignalRunning(dataDir, "open")
		return
	}

	a, err := app.New(dataDir)
	if err != nil {
		platform.MessageBox("Selva Sync", tr("msg_start_failed", map[string]string{"error": err.Error()}), false)
		return
	}
	if err := a.Start(); err != nil {
		platform.MessageBox("Selva Sync", tr("msg_start_failed", map[string]string{"error": err.Error()}), false)
		return
	}
	if runtime.GOOS != "windows" {
		println("Selva Sync window:", a.URL())
	}
	a.QuitHook = func() { platform.Current().Quit() }
	app.AlertIcon, app.NormalIcon = alertICO, iconICO
	platform.RunTray(a.TrayConfig(iconICO, !*background))
	a.Shutdown()
}

func runUninstall(dataDir string, tr func(string, map[string]string) string) {
	if !platform.MessageBox("Selva Sync", tr("msg_uninstall_confirm", nil), true) {
		return
	}
	removeData := platform.MessageBox("Selva Sync", tr("msg_uninstall_data", nil), true)
	if app.SignalRunning(dataDir, "quit") {
		// wait until the running copy has saved and closed (it removes instance.json)
		for i := 0; i < 100; i++ {
			if _, err := os.Stat(filepath.Join(dataDir, "instance.json")); err != nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		time.Sleep(500 * time.Millisecond)
	}
	platform.MessageBox("Selva Sync", tr("msg_uninstall_done", nil), false)
	platform.Uninstall(removeData, dataDir) // removes files a few seconds after we exit
}

// sameFile reports whether two files have identical contents.
func sameFile(a, b string) bool {
	x, err1 := os.ReadFile(a)
	y, err2 := os.ReadFile(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}
