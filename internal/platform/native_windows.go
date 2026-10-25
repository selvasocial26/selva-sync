//go:build windows

package platform

import (
	"errors"
	"runtime"
	"syscall"
	"unsafe"
)

// Native Windows calls that replace earlier PowerShell scripts. Running
// PowerShell with encoded commands is a pattern antivirus programs treat as
// suspicious, so Selva Sync now talks to Windows directly.

var (
	iphlpapi = syscall.NewLazyDLL("iphlpapi.dll")
	ole32    = syscall.NewLazyDLL("ole32.dll")

	procGetNetworkConnectivityHint = iphlpapi.NewProc("GetNetworkConnectivityHint")
	procCoInitializeEx             = ole32.NewProc("CoInitializeEx")
	procCoUninitialize             = ole32.NewProc("CoUninitialize")
	procCoCreateInstance           = ole32.NewProc("CoCreateInstance")
)

// nlNetworkConnectivityHint mirrors NL_NETWORK_CONNECTIVITY_HINT.
type nlNetworkConnectivityHint struct {
	ConnectivityLevel    int32
	ConnectivityCost     int32
	ApproachingDataLimit byte
	OverDataLimit        byte
	Roaming              byte
	_                    byte
}

// connectionMetered asks Windows (10 version 2004 or later) whether the
// current internet connection costs money per use. ok is false when
// Windows cannot tell.
func connectionMetered() (metered, ok bool) {
	if procGetNetworkConnectivityHint.Find() != nil {
		return false, false
	}
	var h nlNetworkConnectivityHint
	r, _, _ := procGetNetworkConnectivityHint.Call(uintptr(unsafe.Pointer(&h)))
	if r != 0 {
		return false, false
	}
	// NetworkConnectivityCostHint: 0 unknown, 1 unrestricted, 2 fixed, 3 variable
	return h.ConnectivityCost == 2 || h.ConnectivityCost == 3 || h.OverDataLimit != 0 || h.Roaming != 0, true
}

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

var (
	clsidShellLink  = guid{0x00021401, 0, 0, [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIShellLinkW  = guid{0x000214F9, 0, 0, [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIPersistFile = guid{0x0000010B, 0, 0, [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
)

// comCall calls method number idx of a COM object.
func comCall(obj unsafe.Pointer, idx int, args ...uintptr) uintptr {
	vtbl := *(*unsafe.Pointer)(obj)
	fn := *(*uintptr)(unsafe.Add(vtbl, uintptr(idx)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(obj)}, args...)...)
	return r
}

// createShortcut writes a Windows .lnk shortcut using the Shell's own
// IShellLinkW object. It runs on its own OS thread so COM set-up never
// affects the tray thread.
func createShortcut(lnkPath, target, workDir, description string) error {
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		done <- createShortcutOnThread(lnkPath, target, workDir, description)
	}()
	return <-done
}

func createShortcutOnThread(lnkPath, target, workDir, description string) error {
	const coinitApartmentThreaded = 0x2
	const clsctxInprocServer = 0x1
	if r, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded); int32(r) < 0 {
		return errors.New("COM initialisation failed")
	}
	defer procCoUninitialize.Call()

	var link unsafe.Pointer
	r, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidShellLink)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIShellLinkW)), uintptr(unsafe.Pointer(&link)))
	if int32(r) < 0 || link == nil {
		return errors.New("could not create shell link")
	}
	defer comCall(link, 2) // Release

	// IShellLinkW methods: 7 SetDescription, 9 SetWorkingDirectory,
	// 17 SetIconLocation, 20 SetPath
	if int32(comCall(link, 20, uintptr(unsafe.Pointer(u16(target))))) < 0 {
		return errors.New("SetPath failed")
	}
	comCall(link, 9, uintptr(unsafe.Pointer(u16(workDir))))
	comCall(link, 7, uintptr(unsafe.Pointer(u16(description))))
	comCall(link, 17, uintptr(unsafe.Pointer(u16(target))), 0)

	var pf unsafe.Pointer
	if int32(comCall(link, 0, uintptr(unsafe.Pointer(&iidIPersistFile)), uintptr(unsafe.Pointer(&pf)))) < 0 || pf == nil {
		return errors.New("IPersistFile not available")
	}
	defer comCall(pf, 2) // Release
	// IPersistFile::Save(pszFileName, fRemember)
	if int32(comCall(pf, 6, uintptr(unsafe.Pointer(u16(lnkPath))), 1)) < 0 {
		return errors.New("saving the shortcut failed")
	}
	return nil
}
