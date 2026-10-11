package configenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompatibleKeyFileIsLoadedWithoutPlainEnvironmentSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if os.WriteFile(path, []byte("fixture-compatible-key\n"), 0600) != nil {
		t.Fatal("write fixture")
	}
	t.Setenv("COMPATIBLE_API_KEY", "")
	t.Setenv("COMPATIBLE_API_KEY_FILE", path)
	if LoadSecretFiles() != nil || os.Getenv("COMPATIBLE_API_KEY") != "fixture-compatible-key" {
		t.Fatal("compatible secret not loaded")
	}
	t.Setenv("COMPATIBLE_API_KEY", "conflicting-value")
	if LoadSecretFiles() == nil {
		t.Fatal("conflicting key accepted")
	}
}
