package standalone

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func (a *App) Backup(ctx context.Context, destination string) error {
	if a == nil || a.store == nil {
		return errors.New("catalog_unavailable")
	}
	destination, err := filepath.Abs(strings.TrimSpace(destination))
	if err != nil || strings.TrimSpace(destination) == "" {
		return errors.New("backup_directory_invalid")
	}
	destinationLock, err := lockDataDir(destination)
	if err != nil {
		return err
	}
	defer destinationLock.Close()
	target := filepath.Join(destination, "catalog.db")
	temporary := target + ".tmp-" + catalogRandomSuffix()
	_ = os.Remove(temporary)
	query := `VACUUM INTO '` + strings.ReplaceAll(filepath.ToSlash(temporary), "'", "''") + `'`
	if _, err := a.store.DB.ExecContext(ctx, query); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("catalog_backup_failed: %w", err)
	}
	if err := replaceFile(temporary, target); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	sourceKey := filepath.Join(a.cfg.DataDir, "master.key")
	keyTemporary := filepath.Join(destination, "master.key.tmp-"+catalogRandomSuffix())
	if err := copyFile(sourceKey, keyTemporary, 0o600); err != nil {
		_ = os.Remove(keyTemporary)
		return err
	}
	if err := replaceFile(keyTemporary, filepath.Join(destination, "master.key")); err != nil {
		_ = os.Remove(keyTemporary)
		return err
	}
	return nil
}

func RestoreDataDir(source, destination string) error {
	if strings.TrimSpace(destination) == "" {
		return errors.New("restore_directory_invalid")
	}
	lock, err := lockDataDir(destination)
	if err != nil {
		return err
	}
	defer lock.Close()
	return restoreDataDir(source, destination)
}

func restoreDataDir(source, destination string) error {
	source, err := filepath.Abs(strings.TrimSpace(source))
	if err != nil || strings.TrimSpace(source) == "" {
		return errors.New("restore_directory_invalid")
	}
	destination, err = filepath.Abs(strings.TrimSpace(destination))
	if err != nil || strings.TrimSpace(destination) == "" {
		return errors.New("restore_directory_invalid")
	}
	if source == destination {
		return errors.New("restore_target_must_differ")
	}
	sourceInfo, err := os.Stat(source)
	if err != nil {
		return err
	}
	destinationInfo, err := os.Stat(destination)
	if err != nil {
		return err
	}
	if os.SameFile(sourceInfo, destinationInfo) {
		return errors.New("restore_target_must_differ")
	}
	sourceLock, err := lockDataDir(source)
	if err != nil {
		return err
	}
	defer sourceLock.Close()
	for _, name := range []string{"catalog.db", "master.key"} {
		sourcePath := filepath.Join(source, name)
		temporary := filepath.Join(destination, name+".restore-"+catalogRandomSuffix())
		if err := copyFile(sourcePath, temporary, 0o600); err != nil {
			_ = os.Remove(temporary)
			return fmt.Errorf("restore_%s_failed: %w", name, err)
		}
		if err := replaceFile(temporary, filepath.Join(destination, name)); err != nil {
			_ = os.Remove(temporary)
			return err
		}
	}
	return nil
}

func copyFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(destination)
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func replaceFile(temporary, target string) error {
	_ = os.Remove(target)
	return os.Rename(temporary, target)
}

func catalogRandomSuffix() string {
	value, err := randomToken("tmp_")
	if err != nil {
		return fmt.Sprintf("%d", os.Getpid())
	}
	return value
}
