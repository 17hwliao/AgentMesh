package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	keySize                     = 32
	keyIDSize                   = 16
	keyringFileMode os.FileMode = 0o600
)

var (
	// ErrUnreadable identifies a keyring that cannot be read or validated.
	ErrUnreadable = errors.New("secrets: keyring unreadable")
	// ErrUnknownKey identifies a key ID that is not present in the keyring.
	ErrUnknownKey = errors.New("secrets: unknown key")
	// ErrAADMismatch identifies an authentication failure. AES-GCM cannot
	// distinguish a wrong AAD from modified ciphertext or a modified nonce.
	ErrAADMismatch = errors.New("secrets: authentication failed; AAD or ciphertext mismatch")
	// ErrInvalidNonce identifies a nonce with the wrong length.
	ErrInvalidNonce = errors.New("secrets: invalid nonce")
)

// Keyring holds one current encryption key and any previous decryption keys.
type Keyring struct {
	currentKeyID string
	keys         map[string][keySize]byte
}

type keyringFile struct {
	CurrentKeyID string            `json:"current_key_id"`
	Keys         map[string]string `json:"keys"`
}

// LoadOrCreate reads a keyring from path, creating it with a new random key if
// it does not exist. New files use mode 0600; permission enforcement on
// Windows is best effort.
func LoadOrCreate(path string) (*Keyring, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return decodeKeyring(data)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %v", ErrUnreadable, err)
	}
	return createKeyring(path)
}

// Encrypt encrypts plaintext with the current key and returns the ciphertext,
// nonce, and current key ID.
func (k *Keyring) Encrypt(plaintext []byte, aad string) ([]byte, []byte, string, error) {
	if k == nil {
		return nil, nil, "", errors.New("secrets: nil keyring")
	}
	key, ok := k.keys[k.currentKeyID]
	if !ok || k.currentKeyID == "" {
		return nil, nil, "", fmt.Errorf("%w: current key %q is unavailable", ErrUnknownKey, k.currentKeyID)
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, nil, "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, "", fmt.Errorf("secrets: generate nonce: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, []byte(aad))
	return ciphertext, nonce, k.currentKeyID, nil
}

// Decrypt decrypts ciphertext with the requested key ID. Any key in the
// keyring can be used, including keys that are not current.
func (k *Keyring) Decrypt(ciphertext, nonce []byte, keyID, aad string) ([]byte, error) {
	if k == nil {
		return nil, errors.New("secrets: nil keyring")
	}
	key, ok := k.keys[keyID]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownKey, keyID)
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("%w: got %d bytes, want %d", ErrInvalidNonce, len(nonce), gcm.NonceSize())
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, []byte(aad))
	if err != nil {
		return nil, ErrAADMismatch
	}
	return plaintext, nil
}

func decodeKeyring(data []byte) (*Keyring, error) {
	var stored keyringFile
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("%w: invalid JSON: %v", ErrUnreadable, err)
	}
	if stored.CurrentKeyID == "" {
		return nil, fmt.Errorf("%w: current_key_id is empty", ErrUnreadable)
	}

	keys := make(map[string][keySize]byte, len(stored.Keys))
	for keyID, encodedKey := range stored.Keys {
		if keyID == "" {
			return nil, fmt.Errorf("%w: empty key ID", ErrUnreadable)
		}
		rawKey, err := base64.StdEncoding.DecodeString(encodedKey)
		if err != nil {
			return nil, fmt.Errorf("%w: key %q is not valid base64: %v", ErrUnreadable, keyID, err)
		}
		if len(rawKey) != keySize {
			return nil, fmt.Errorf("%w: key %q has length %d, want %d", ErrUnreadable, keyID, len(rawKey), keySize)
		}
		var key [keySize]byte
		copy(key[:], rawKey)
		keys[keyID] = key
	}
	if _, ok := keys[stored.CurrentKeyID]; !ok {
		return nil, fmt.Errorf("%w: current key %q is missing", ErrUnreadable, stored.CurrentKeyID)
	}
	return &Keyring{currentKeyID: stored.CurrentKeyID, keys: keys}, nil
}

func createKeyring(path string) (*Keyring, error) {
	rawKey := make([]byte, keySize)
	if _, err := io.ReadFull(rand.Reader, rawKey); err != nil {
		return nil, fmt.Errorf("secrets: generate key: %w", err)
	}
	rawKeyID := make([]byte, keyIDSize)
	if _, err := io.ReadFull(rand.Reader, rawKeyID); err != nil {
		return nil, fmt.Errorf("secrets: generate key ID: %w", err)
	}

	var key [keySize]byte
	copy(key[:], rawKey)
	keyID := hex.EncodeToString(rawKeyID)
	keyring := &Keyring{
		currentKeyID: keyID,
		keys:         map[string][keySize]byte{keyID: key},
	}
	data, err := encodeKeyring(keyring)
	if err != nil {
		return nil, err
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, keyringFileMode)
	if errors.Is(err, os.ErrExist) {
		return readExistingKeyring(path)
	}
	if err != nil {
		return nil, fmt.Errorf("secrets: create keyring: %w", err)
	}

	keep := false
	defer func() {
		if !keep {
			_ = file.Close()
			_ = os.Remove(path)
		}
	}()
	if written, err := file.Write(data); err != nil {
		return nil, fmt.Errorf("secrets: write keyring: %w", err)
	} else if written != len(data) {
		return nil, fmt.Errorf("secrets: write keyring: wrote %d of %d bytes", written, len(data))
	}
	if err := file.Sync(); err != nil {
		return nil, fmt.Errorf("secrets: sync keyring: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("secrets: close keyring: %w", err)
	}
	keep = true
	return keyring, nil
}

func readExistingKeyring(path string) (*Keyring, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreadable, err)
	}
	return decodeKeyring(data)
}

func encodeKeyring(keyring *Keyring) ([]byte, error) {
	stored := keyringFile{
		CurrentKeyID: keyring.currentKeyID,
		Keys:         make(map[string]string, len(keyring.keys)),
	}
	for keyID, key := range keyring.keys {
		stored.Keys[keyID] = base64.StdEncoding.EncodeToString(key[:])
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("secrets: encode keyring: %w", err)
	}
	return append(data, '\n'), nil
}

func newGCM(key [keySize]byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("secrets: initialize AES: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: initialize GCM: %w", err)
	}
	return gcm, nil
}
