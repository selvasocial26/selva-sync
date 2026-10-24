//go:build windows

package config

import (
	"os"
	"syscall"
	"unsafe"
)

var procLockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

func lockFile(f *os.File) bool {
	var ol syscall.Overlapped
	// LOCKFILE_EXCLUSIVE_LOCK | LOCKFILE_FAIL_IMMEDIATELY
	r, _, _ := procLockFileEx.Call(f.Fd(), 3, 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
	return r != 0
}
