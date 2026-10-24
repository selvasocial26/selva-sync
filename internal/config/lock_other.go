//go:build !windows

package config

import (
	"os"
	"syscall"
)

func lockFile(f *os.File) bool {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil
}
