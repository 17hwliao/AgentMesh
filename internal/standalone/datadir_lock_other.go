//go:build !windows && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package standalone

import (
	"errors"
	"os"
)

func tryLockFile(*os.File) error {
	return errors.New("standalone_data_dir_lock_unsupported")
}
