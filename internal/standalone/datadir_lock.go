package standalone

import (
	"errors"
	"os"
	"path/filepath"
)

var errDataDirLocked = errors.New("standalone_data_dir_locked")

func lockDataDir(directory string) (*os.File, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, "agentmesh.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.Mode().IsRegular() || !os.SameFile(opened, pathInfo) {
		_ = file.Close()
		return nil, errors.New("standalone_data_dir_lock_invalid")
	}
	if err := tryLockFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
