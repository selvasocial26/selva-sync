//go:build windows

package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	advapi32 = syscall.NewLazyDLL("advapi32.dll")
	crypt32  = syscall.NewLazyDLL("crypt32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	user32   = syscall.NewLazyDLL("user32.dll")

	procLocalFree                = kernel32.NewProc("LocalFree")
	procGetLogicalDrives         = kernel32.NewProc("GetLogicalDrives")
	procGetDriveTypeW            = kernel32.NewProc("GetDriveTypeW")
	procGetVolumeInformationW    = kernel32.NewProc("GetVolumeInformationW")
	procCreateMutexW             = kernel32.NewProc("CreateMutexW")
	procGetUserDefaultUILanguage = kernel32.NewProc("GetUserDefaultUILanguage")
	procGetModuleHandleW         = kernel32.NewProc("GetModuleHandleW")
	procSetErrorMode             = kernel32.NewProc("SetErrorMode")
	procGetVolumePathNameW       = kernel32.NewProc("GetVolumePathNameW")
	procGetSystemPowerStatus     = kernel32.NewProc("GetSystemPowerStatus")

	procRegCreateKeyExW = advapi32.NewProc("RegCreateKeyExW")
	procRegSetValueExW  = advapi32.NewProc("RegSetValueExW")
	procRegDeleteValueW = advapi32.NewProc("RegDeleteValueW")
	procRegDeleteTreeW  = advapi32.NewProc("RegDeleteTreeW")
	procRegCloseKey     = advapi32.NewProc("RegCloseKey")

	procCryptProtectData   = crypt32.NewProc("CryptProtectData")
	procCryptUnprotectData = crypt32.NewProc("CryptUnprotectData")

	procShellExecuteW = shell32.NewProc("ShellExecuteW")
	procMessageBoxW   = user32.NewProc("MessageBoxW")
)

const hkeyCurrentUser = 0x80000001

func u16(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }

// sysTool returns the full path of a Windows tool in System32, so a
// program with the same name elsewhere on PATH is never run instead.
func sysTool(name string) string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", name)
}

// runTool runs a system tool hidden, with a time limit, and returns its output.
func runTool(timeout time.Duration, cmdLine string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := hidden(exec.CommandContext(ctx, sysTool(name), args...))
	if cmdLine != "" {
		cmd.SysProcAttr.CmdLine = cmdLine
	}
	return cmd.Output()
}

func hidden(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	return cmd
}

// ---------- browser / explorer ----------

// OpenURL opens a link in the default browser.
func OpenURL(url string) {
	procShellExecuteW.Call(0, uintptr(unsafe.Pointer(u16("open"))), uintptr(unsafe.Pointer(u16(url))), 0, 0, 1)
}

// OpenFolder shows a folder in File Explorer.
func OpenFolder(path string) { OpenURL(path) }

// OpenAppWindow shows the Selva Sync window. It uses Microsoft Edge in "app"
// mode (a plain window with no tabs or address bar), which every Windows
// 10/11 PC has. Falls back to the default browser.
func OpenAppWindow(url string) {
	candidates := []string{
		filepath.Join(os.Getenv("ProgramFiles(x86)"), `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(os.Getenv("ProgramFiles"), `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(os.Getenv("LOCALAPPDATA"), `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(os.Getenv("ProgramFiles"), `Google\Chrome\Application\chrome.exe`),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), `Google\Chrome\Application\chrome.exe`),
		filepath.Join(os.Getenv("LOCALAPPDATA"), `Google\Chrome\Application\chrome.exe`),
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			cmd := exec.Command(c, "--app="+url, "--window-size=1060,760", "--no-first-run", "--no-default-browser-check")
			if cmd.Start() == nil {
				go cmd.Wait()
				return
			}
		}
	}
	OpenURL(url)
}

// MessageBox shows a simple dialog. With yesNo it returns true for "Yes".
func MessageBox(title, text string, yesNo bool) bool {
	flags := uintptr(0x40) // MB_ICONINFORMATION
	if yesNo {
		flags = 0x24 // MB_YESNO | MB_ICONQUESTION
	}
	r, _, _ := procMessageBoxW.Call(0, uintptr(unsafe.Pointer(u16(text))), uintptr(unsafe.Pointer(u16(title))), flags|0x10000) // MB_SETFOREGROUND
	return r == 6 || (!yesNo && r == 1)
}

// ---------- registry ----------

func regSetString(path, name, value string) error {
	var key syscall.Handle
	r, _, _ := procRegCreateKeyExW.Call(hkeyCurrentUser, uintptr(unsafe.Pointer(u16(path))), 0, 0, 0, 0x20006 /*KEY_WRITE*/, 0, uintptr(unsafe.Pointer(&key)), 0)
	if r != 0 {
		return syscall.Errno(r)
	}
	defer procRegCloseKey.Call(uintptr(key))
	v, _ := syscall.UTF16FromString(value)
	r, _, _ = procRegSetValueExW.Call(uintptr(key), uintptr(unsafe.Pointer(u16(name))), 0, 1 /*REG_SZ*/, uintptr(unsafe.Pointer(&v[0])), uintptr(len(v)*2))
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

func regSetDword(path, name string, value uint32) error {
	var key syscall.Handle
	r, _, _ := procRegCreateKeyExW.Call(hkeyCurrentUser, uintptr(unsafe.Pointer(u16(path))), 0, 0, 0, 0x20006, 0, uintptr(unsafe.Pointer(&key)), 0)
	if r != 0 {
		return syscall.Errno(r)
	}
	defer procRegCloseKey.Call(uintptr(key))
	r, _, _ = procRegSetValueExW.Call(uintptr(key), uintptr(unsafe.Pointer(u16(name))), 0, 4 /*REG_DWORD*/, uintptr(unsafe.Pointer(&value)), 4)
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

func regDeleteValue(path, name string) {
	var key syscall.Handle
	if err := syscall.RegOpenKeyEx(hkeyCurrentUser, u16(path), 0, 0x20006, &key); err != nil {
		return
	}
	defer syscall.RegCloseKey(key)
	procRegDeleteValueW.Call(uintptr(key), uintptr(unsafe.Pointer(u16(name))))
}

func regDeleteTree(path string) {
	procRegDeleteTreeW.Call(hkeyCurrentUser, uintptr(unsafe.Pointer(u16(path))))
	var key syscall.Handle
	if syscall.RegOpenKeyEx(hkeyCurrentUser, u16(filepath.Dir(path)), 0, 0x20006, &key) == nil {
		advapi32.NewProc("RegDeleteKeyW").Call(uintptr(key), uintptr(unsafe.Pointer(u16(filepath.Base(path)))))
		syscall.RegCloseKey(key)
	}
}

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// SetStartup adds or removes Selva Sync from "start with Windows".
func SetStartup(enabled bool, exe string) error {
	if !enabled {
		regDeleteValue(runKey, "SelvaSync")
		return nil
	}
	return regSetString(runKey, "SelvaSync", `"`+exe+`" --background`)
}

// ---------- secure storage (Windows DPAPI) ----------

type dataBlob struct {
	cbData uint32
	pbData *byte
}

func newBlob(b []byte) *dataBlob {
	if len(b) == 0 {
		return &dataBlob{}
	}
	return &dataBlob{cbData: uint32(len(b)), pbData: &b[0]}
}

func (b *dataBlob) bytes() []byte {
	out := make([]byte, b.cbData)
	copy(out, unsafe.Slice(b.pbData, b.cbData))
	return out
}

var entropy = []byte("SelvaSync-token-v1")

// Protect encrypts data so only this Windows user on this PC can read it.
func Protect(data []byte) ([]byte, error) {
	var out dataBlob
	r, _, err := procCryptProtectData.Call(uintptr(unsafe.Pointer(newBlob(data))), 0, uintptr(unsafe.Pointer(newBlob(entropy))), 0, 0, 1, uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return nil, err
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return out.bytes(), nil
}

// Unprotect decrypts data written by Protect.
func Unprotect(data []byte) ([]byte, error) {
	var out dataBlob
	r, _, err := procCryptUnprotectData.Call(uintptr(unsafe.Pointer(newBlob(data))), 0, uintptr(unsafe.Pointer(newBlob(entropy))), 0, 0, 1, uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return nil, err
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return out.bytes(), nil
}

// ---------- disks ----------

// Drives lists the disks on this PC.
func Drives() []DriveInfo {
	procSetErrorMode.Call(0x0001) // no "insert disk" popups
	mask, _, _ := procGetLogicalDrives.Call()
	var out []DriveInfo
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		t, _, _ := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(u16(root))))
		kind := "other"
		switch t {
		case 2:
			kind = "removable"
		case 3:
			kind = "fixed"
		case 4:
			kind = "network"
		case 5:
			kind = "cdrom"
		}
		label := ""
		if kind != "cdrom" && kind != "network" { // offline network drives can hang
			buf := make([]uint16, 261)
			r, _, _ := procGetVolumeInformationW.Call(uintptr(unsafe.Pointer(u16(root))), uintptr(unsafe.Pointer(&buf[0])), 261, 0, 0, 0, 0, 0)
			if r != 0 {
				label = syscall.UTF16ToString(buf)
			}
		}
		out = append(out, DriveInfo{Path: root, Label: label, Kind: kind})
	}
	return out
}

// SkipAttr skips cloud-only placeholder files (OneDrive "Files On-Demand"
// etc.) so that reading them does not download them, and junction folders.
func SkipAttr(info fs.FileInfo) bool {
	d, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return false
	}
	const (
		offline        = 0x1000
		recallOnOpen   = 0x40000
		recallOnAccess = 0x400000
		reparse        = 0x400
	)
	a := d.FileAttributes
	if a&(offline|recallOnOpen|recallOnAccess) != 0 {
		return true
	}
	return info.IsDir() && a&reparse != 0
}

// ---------- language ----------

// UserLanguage returns the Windows display language as a short code.
func UserLanguage() string {
	r, _, _ := procGetUserDefaultUILanguage.Call()
	switch r & 0x3ff {
	case 0x39:
		return "hi"
	case 0x47:
		return "gu"
	case 0x0a:
		return "es"
	case 0x0c:
		return "fr"
	case 0x16:
		return "pt"
	case 0x49:
		return "ta"
	case 0x4e:
		return "mr"
	case 0x45:
		return "bn"
	case 0x01:
		return "ar"
	case 0x07:
		return "de"
	case 0x10:
		return "it"
	case 0x21:
		return "id"
	case 0x41:
		return "sw"
	case 0x4a:
		return "te"
	case 0x4b:
		return "kn"
	case 0x4c:
		return "ml"
	case 0x46:
		return "pa"
	case 0x48:
		return "or"
	case 0x20:
		return "ur"
	}
	return "en"
}

// ---------- network ----------

var (
	netMu     sync.Mutex
	ssidCache string
	ssidKnown = true
	ssidAt    time.Time
	metCache  bool
	metAt     time.Time
)

// WifiSSID returns the connected Wi-Fi network name ("" when wired or not
// connected). known is false when Windows refuses to tell apps the name
// (Windows 11 requires the Location permission for that).
func WifiSSID() (string, bool) {
	netMu.Lock()
	defer netMu.Unlock()
	if time.Since(ssidAt) < time.Minute {
		return ssidCache, ssidKnown
	}
	ssidAt = time.Now()
	ssidCache, ssidKnown = "", true
	// UTF-8 output so Wi-Fi names in any language are read correctly
	cmdLine := `"` + sysTool("cmd.exe") + `" /d /c "chcp 65001 >nul & "` + sysTool("netsh.exe") + `" wlan show interfaces"`
	out, err := runTool(10*time.Second, cmdLine, "cmd.exe")
	if err != nil && len(out) == 0 {
		return "", true // no Wi-Fi adapter / service: wired
	}
	text := string(out)
	for _, line := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(k) == "SSID" {
			ssidCache = strings.TrimSpace(v)
			break
		}
	}
	if ssidCache == "" && strings.Contains(strings.ToLower(text), "location") {
		ssidKnown = false
	}
	return ssidCache, ssidKnown
}

// Metered reports whether Windows considers the current connection metered
// (mobile data, hotspot, or a Wi-Fi the user marked as metered).
func Metered() bool {
	netMu.Lock()
	defer netMu.Unlock()
	if time.Since(metAt) < 2*time.Minute {
		return metCache
	}
	metAt = time.Now()
	metCache = false
	if m, ok := connectionMetered(); ok {
		metCache = m
	}
	return metCache
}

// ---------- single instance ----------

// SingleInstance returns false if Selva Sync is already running.
// One copy per Windows user, even when signed in twice (console + remote).
func SingleInstance() bool {
	sum := sha256.Sum256([]byte(strings.ToLower(os.Getenv("USERDOMAIN") + `\` + os.Getenv("USERNAME"))))
	name := `Global\SelvaSync-` + hex.EncodeToString(sum[:8])
	h, _, err := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(u16(name))))
	if h == 0 {
		// Global namespace not allowed: fall back to this session only
		_, _, err = procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(u16(`Local\SelvaSync-Instance`))))
	}
	return !errors.Is(err, syscall.Errno(183)) // ERROR_ALREADY_EXISTS
}

// ---------- install / uninstall (per user, no admin needed) ----------

const uninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\SelvaSync`

// InstallDir is where Selva Sync installs itself.
func InstallDir() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Selva Sync")
}

// InstalledExe is the installed program path.
func InstalledExe() string { return filepath.Join(InstallDir(), "SelvaSync.exe") }

func shortcutPath() string {
	return filepath.Join(os.Getenv("APPDATA"), `Microsoft\Windows\Start Menu\Programs\Selva Sync.lnk`)
}

// Install copies the program to the install folder, adds a Start Menu
// shortcut and an entry in "Apps & features".
func Install(self, version, publisher string) error {
	dst := InstalledExe()
	if err := os.MkdirAll(InstallDir(), 0o755); err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Clean(self), filepath.Clean(dst)) {
		data, err := os.ReadFile(self)
		if err != nil {
			return err
		}
		var werr error
		for i := 0; i < 20; i++ { // the old version may still be closing
			if werr = os.WriteFile(dst+".new", data, 0o755); werr == nil {
				if werr = os.Rename(dst+".new", dst); werr == nil {
					break
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
		if werr != nil {
			return werr
		}
	}
	// copy the Google credentials and office setup files shipped next to
	// the downloaded .exe (small files only)
	for _, name := range []string{"google-client.json", "selvasync-setup.json"} {
		src := filepath.Join(filepath.Dir(self), name)
		if st, err := os.Stat(src); err == nil && st.Mode().IsRegular() && st.Size() < 256<<10 {
			if b, err := os.ReadFile(src); err == nil {
				os.WriteFile(filepath.Join(InstallDir(), name), b, 0o644)
			}
		}
	}
	_ = createShortcut(shortcutPath(), dst, InstallDir(), "Selva Sync - mirror your PC to Google Drive")

	regSetString(uninstallKey, "DisplayName", "Selva Sync")
	regSetString(uninstallKey, "DisplayVersion", version)
	regSetString(uninstallKey, "Publisher", publisher)
	regSetString(uninstallKey, "DisplayIcon", dst)
	regSetString(uninstallKey, "InstallLocation", InstallDir())
	regSetString(uninstallKey, "UninstallString", `"`+dst+`" --uninstall`)
	regSetString(uninstallKey, "URLInfoAbout", "https://www.linkedin.com/in/selvakumar-vellingiri-phd-97744572/")
	if st, err := os.Stat(dst); err == nil {
		regSetDword(uninstallKey, "EstimatedSize", uint32(st.Size()/1024))
	}
	regSetDword(uninstallKey, "NoModify", 1)
	regSetDword(uninstallKey, "NoRepair", 1)
	return nil
}

// Uninstall removes the shortcut, registry entries and program files.
// Files already on Google Drive are not touched.
func Uninstall(removeData bool, dataDir string) {
	SetStartup(false, "")
	regDeleteTree(uninstallKey)
	os.Remove(shortcutPath())
	if removeData && dataDir != "" {
		os.RemoveAll(dataDir)
	}
	// The running .exe cannot delete itself; ask cmd to remove the program
	// folder a few seconds after this process exits.
	// The command line is passed exactly as written: Go's automatic quoting
	// confuses cmd.exe when the folder name contains a space.
	dir := InstallDir()
	cmd := hidden(exec.Command(sysTool("cmd.exe")))
	cmd.SysProcAttr.CmdLine = `"` + sysTool("cmd.exe") + `" /d /c "ping 127.0.0.1 -n 5 >nul & rmdir /s /q "` + dir + `""`
	cmd.Start()
}

// InstalledVersion returns the version recorded by the installed copy, or "".
func InstalledVersion() string {
	var key syscall.Handle
	if syscall.RegOpenKeyEx(hkeyCurrentUser, u16(uninstallKey), 0, syscall.KEY_READ, &key) != nil {
		return ""
	}
	defer syscall.RegCloseKey(key)
	buf := make([]uint16, 64)
	n := uint32(len(buf) * 2)
	var typ uint32
	if syscall.RegQueryValueEx(key, u16("DisplayVersion"), nil, &typ, (*byte)(unsafe.Pointer(&buf[0])), &n) != nil {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

// ModuleHandle is needed by the tray window.
func moduleHandle() uintptr {
	h, _, _ := procGetModuleHandleW.Call(0)
	return h
}

func machineGUID() string {
	var key syscall.Handle
	if err := syscall.RegOpenKeyEx(syscall.HKEY_LOCAL_MACHINE, u16(`SOFTWARE\Microsoft\Cryptography`), 0, syscall.KEY_READ|0x0100, &key); err != nil {
		return ""
	}
	defer syscall.RegCloseKey(key)
	buf := make([]uint16, 128)
	n := uint32(len(buf) * 2)
	var typ uint32
	if err := syscall.RegQueryValueEx(key, u16("MachineGuid"), nil, &typ, (*byte)(unsafe.Pointer(&buf[0])), &n); err != nil {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

// LegacyMachineID is the identity Selva Sync 1.0 used (without the user),
// so an upgraded installation finds its existing Drive folder.
func LegacyMachineID() string {
	guid := machineGUID()
	if guid == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("selvasync:" + guid))
	return hex.EncodeToString(sum[:8])
}

// MachineID returns a stable, anonymous identifier for this Windows
// installation (a hash of MachineGuid). Used only to tell this PC's Drive
// folder apart from other PCs on the same Google account.
func MachineID() string {
	var key syscall.Handle
	if err := syscall.RegOpenKeyEx(syscall.HKEY_LOCAL_MACHINE, u16(`SOFTWARE\Microsoft\Cryptography`), 0, syscall.KEY_READ|0x0100, &key); err != nil {
		return ""
	}
	defer syscall.RegCloseKey(key)
	buf := make([]uint16, 128)
	n := uint32(len(buf) * 2)
	var typ uint32
	if err := syscall.RegQueryValueEx(key, u16("MachineGuid"), nil, &typ, (*byte)(unsafe.Pointer(&buf[0])), &n); err != nil {
		return ""
	}
	guid := syscall.UTF16ToString(buf)
	if guid == "" {
		return ""
	}
	user := strings.ToLower(os.Getenv("USERDOMAIN") + `\` + os.Getenv("USERNAME"))
	sum := sha256.Sum256([]byte("selvasync:" + guid + ":" + user))
	return hex.EncodeToString(sum[:8])
}

// VolumeID returns the serial number of the disk that holds path.
func VolumeID(path string) string {
	buf := make([]uint16, 300)
	r, _, _ := procGetVolumePathNameW.Call(uintptr(unsafe.Pointer(u16(path))), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if r == 0 {
		return ""
	}
	root := syscall.UTF16ToString(buf)
	var serial uint32
	r, _, _ = procGetVolumeInformationW.Call(uintptr(unsafe.Pointer(u16(root))), 0, 0, uintptr(unsafe.Pointer(&serial)), 0, 0, 0, 0)
	if r == 0 || serial == 0 {
		return ""
	}
	return fmt.Sprintf("%08X", serial)
}

// OnBattery reports that the computer is running on battery power
// (false for desktops and when plugged in).
func OnBattery() bool {
	var st struct {
		ACLineStatus        byte
		BatteryFlag         byte
		BatteryLifePercent  byte
		SystemStatusFlag    byte
		BatteryLifeTime     uint32
		BatteryFullLifeTime uint32
	}
	r, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&st)))
	if r == 0 {
		return false
	}
	// ACLineStatus 0 = offline (battery); 128 in BatteryFlag = no battery
	return st.ACLineStatus == 0 && st.BatteryFlag != 128
}
