package secrets

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

type diskKeyring struct {
	CurrentKeyID string            `json:"current_key_id"`
	Keys         map[string]string `json:"keys"`
}

func TestLoadOrCreateCreatesKeyring(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	keyring, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("LoadOrCreate() error = %v", err)
	}

	stored := readDiskKeyring(t, path)
	if stored.CurrentKeyID == "" {
		t.Fatal("current_key_id is empty")
	}
	if stored.CurrentKeyID != keyring.currentKeyID {
		t.Fatalf("current key ID = %q, want %q", stored.CurrentKeyID, keyring.currentKeyID)
	}
	if len(stored.Keys) != 1 {
		t.Fatalf("stored key count = %d, want 1", len(stored.Keys))
	}
	rawKey, err := base64.StdEncoding.DecodeString(stored.Keys[stored.CurrentKeyID])
	if err != nil {
		t.Fatalf("decode current key: %v", err)
	}
	if len(rawKey) != keySize {
		t.Fatalf("current key length = %d, want %d", len(rawKey), keySize)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("keyring mode = %o, want 600", info.Mode().Perm())
	}
}

func TestLoadOrCreateReusesPersistedKeyring(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	first, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("first LoadOrCreate() error = %v", err)
	}
	plaintext := []byte("persisted secret")
	aad := "tenant-1"
	ciphertext, nonce, keyID, err := first.Encrypt(plaintext, aad)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	second, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("second LoadOrCreate() error = %v", err)
	}
	if second.currentKeyID != first.currentKeyID {
		t.Fatalf("current key ID = %q, want %q", second.currentKeyID, first.currentKeyID)
	}
	decrypted, err := second.Decrypt(ciphertext, nonce, keyID, aad)
	if err != nil {
		t.Fatalf("Decrypt() after reload error = %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypted plaintext = %q, want %q", decrypted, plaintext)
	}
}

func TestEncryptDecrypt(t *testing.T) {
	keyring := newTestKeyring(t)
	tests := []struct {
		name      string
		plaintext []byte
		aad       string
	}{
		{name: "text", plaintext: []byte("secret value"), aad: "tenant-1/credential"},
		{name: "empty", plaintext: []byte{}, aad: ""},
		{name: "binary", plaintext: []byte{0x00, 0x01, 0x02, 0xff}, aad: "binary"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ciphertext, nonce, keyID, err := keyring.Encrypt(test.plaintext, test.aad)
			if err != nil {
				t.Fatalf("Encrypt() error = %v", err)
			}
			if keyID != keyring.currentKeyID {
				t.Fatalf("key ID = %q, want current key %q", keyID, keyring.currentKeyID)
			}
			if len(nonce) != 12 {
				t.Fatalf("nonce length = %d, want 12", len(nonce))
			}
			decrypted, err := keyring.Decrypt(ciphertext, nonce, keyID, test.aad)
			if err != nil {
				t.Fatalf("Decrypt() error = %v", err)
			}
			if !bytes.Equal(decrypted, test.plaintext) {
				t.Fatalf("decrypted plaintext = %v, want %v", decrypted, test.plaintext)
			}
		})
	}
}

func TestPreviousKeyCanDecrypt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	original, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("LoadOrCreate() error = %v", err)
	}
	aad := "key rotation"
	oldPlaintext := []byte("encrypted before rotation")
	oldCiphertext, oldNonce, oldKeyID, err := original.Encrypt(oldPlaintext, aad)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	stored := readDiskKeyring(t, path)
	newRawKey := make([]byte, keySize)
	if _, err := rand.Read(newRawKey); err != nil {
		t.Fatalf("generate new key: %v", err)
	}
	const newKeyID = "rotated-key"
	stored.Keys[newKeyID] = base64.StdEncoding.EncodeToString(newRawKey)
	stored.CurrentKeyID = newKeyID
	writeDiskKeyring(t, path, stored)

	rotated, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("reload rotated keyring: %v", err)
	}
	newPlaintext := []byte("encrypted after rotation")
	newCiphertext, newNonce, writtenKeyID, err := rotated.Encrypt(newPlaintext, aad)
	if err != nil {
		t.Fatalf("Encrypt() after rotation error = %v", err)
	}
	if writtenKeyID != newKeyID {
		t.Fatalf("new ciphertext key ID = %q, want %q", writtenKeyID, newKeyID)
	}

	decrypted, err := rotated.Decrypt(oldCiphertext, oldNonce, oldKeyID, aad)
	if err != nil {
		t.Fatalf("decrypt with previous key: %v", err)
	}
	if !bytes.Equal(decrypted, oldPlaintext) {
		t.Fatalf("decrypted old plaintext = %q, want %q", decrypted, oldPlaintext)
	}
	decrypted, err = rotated.Decrypt(newCiphertext, newNonce, writtenKeyID, aad)
	if err != nil {
		t.Fatalf("decrypt with current key: %v", err)
	}
	if !bytes.Equal(decrypted, newPlaintext) {
		t.Fatalf("decrypted new plaintext = %q, want %q", decrypted, newPlaintext)
	}
}

func TestDecryptWrongAAD(t *testing.T) {
	keyring := newTestKeyring(t)
	ciphertext, nonce, keyID, err := keyring.Encrypt([]byte("secret"), "expected")
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if _, err := keyring.Decrypt(ciphertext, nonce, keyID, "wrong"); !errors.Is(err, ErrAADMismatch) {
		t.Fatalf("Decrypt() error = %v, want %v", err, ErrAADMismatch)
	}
}

func TestDecryptUnknownKey(t *testing.T) {
	keyring := newTestKeyring(t)
	ciphertext, nonce, _, err := keyring.Encrypt([]byte("secret"), "aad")
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if _, err := keyring.Decrypt(ciphertext, nonce, "missing-key", "aad"); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("Decrypt() error = %v, want %v", err, ErrUnknownKey)
	}
}

func TestLoadOrCreateRejectsCorruptKeyring(t *testing.T) {
	validKey := base64.StdEncoding.EncodeToString(make([]byte, keySize))
	tests := []struct {
		name string
		data string
	}{
		{name: "invalid JSON", data: `{"current_key_id":`},
		{name: "empty document", data: `{}`},
		{name: "missing current key", data: `{"current_key_id":"missing","keys":{"other":"` + validKey + `"}}`},
		{name: "no keys", data: `{"current_key_id":"current","keys":{}}`},
		{name: "invalid base64", data: `{"current_key_id":"current","keys":{"current":"not-base64!"}}`},
		{name: "wrong key length", data: `{"current_key_id":"current","keys":{"current":"` + base64.StdEncoding.EncodeToString(make([]byte, keySize-1)) + `"}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "keyring.json")
			if err := os.WriteFile(path, []byte(test.data), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			if _, err := LoadOrCreate(path); !errors.Is(err, ErrUnreadable) {
				t.Fatalf("LoadOrCreate() error = %v, want %v", err, ErrUnreadable)
			}
		})
	}
}

func newTestKeyring(t *testing.T) *Keyring {
	t.Helper()
	keyring, err := LoadOrCreate(filepath.Join(t.TempDir(), "keyring.json"))
	if err != nil {
		t.Fatalf("LoadOrCreate() error = %v", err)
	}
	return keyring
}

func readDiskKeyring(t *testing.T, path string) diskKeyring {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var stored diskKeyring
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if stored.Keys == nil {
		t.Fatal("keys map is nil")
	}
	return stored
}

func writeDiskKeyring(t *testing.T, path string, stored diskKeyring) {
	t.Helper()
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
