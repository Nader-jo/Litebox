// Package secrets provides small, file-backed encryption primitives for
// installation secrets. The key is deliberately kept outside SQLite.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const keySize = 32

// LoadOrCreate loads a 256-bit installation key, creating it with restrictive
// permissions on first boot. A Docker secret can provide the path in hardened
// deployments; the default lives under the persistent data volume.
func LoadOrCreate(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("master key path is required")
	}
	if encoded, err := os.ReadFile(path); err == nil {
		if len(encoded) != keySize {
			return nil, fmt.Errorf("master key must contain %d bytes", keySize)
		}
		return encoded, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create master key directory: %w", err)
	}
	key := make([]byte, keySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return LoadOrCreate(path)
		}
		return nil, fmt.Errorf("create master key: %w", err)
	}
	if _, err := file.Write(key); err != nil {
		file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("write master key: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close master key: %w", err)
	}
	return key, nil
}

// Encrypt authenticates and encrypts one value. The caller must supply stable
// associated data such as the installation and setting name.
func Encrypt(key []byte, plaintext, associatedData string) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	sealed := aead.Seal(nil, nonce, []byte(plaintext), []byte(associatedData))
	return append(nonce, sealed...), nil
}

// Decrypt verifies and decrypts one value.
func Decrypt(key, ciphertext []byte, associatedData string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(ciphertext) < aead.NonceSize() {
		return "", errors.New("encrypted value is truncated")
	}
	plaintext, err := aead.Open(nil, ciphertext[:aead.NonceSize()], ciphertext[aead.NonceSize():], []byte(associatedData))
	if err != nil {
		return "", errors.New("encrypted value failed authentication")
	}
	return string(plaintext), nil
}
