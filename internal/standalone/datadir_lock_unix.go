//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package standalone

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func tryLockFile(file *os.File) error {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return errDataDirLocked
	}
	return err
}
