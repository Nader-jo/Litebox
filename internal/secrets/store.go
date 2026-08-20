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
	"time"
)

const keySize = 32

// LoadOrCreate loads a 256-bit installation key, creating it with restrictive
// permissions on first boot. A Docker secret can provide the path in hardened
// deployments; the default lives under the persistent data volume.
func LoadOrCreate(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("master key path is required")
	}
	if encoded, err := Load(path); err == nil {
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
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, ".litebox-master-key-*")
	if err != nil {
		return nil, fmt.Errorf("create master key temporary file: %w", err)
	}
	temporary := file.Name()
	defer func() { _ = os.Remove(temporary) }()
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return nil, fmt.Errorf("secure master key temporary file: %w", err)
	}
	if _, err := file.Write(key); err != nil {
		file.Close()
		return nil, fmt.Errorf("write master key: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return nil, fmt.Errorf("sync master key: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close master key: %w", err)
	}
	created, err := publishKeyNoReplace(temporary, path)
	if err != nil {
		return nil, fmt.Errorf("commit master key without replacement: %w", err)
	}
	if !created {
		return LoadOrCreate(path)
	}
	return key, nil
}

// Load reads an existing key without ever creating or replacing it. Read-only
// Docker secret mounts are supported; their access policy is enforced by the
// container runtime rather than portable Unix mode bits.
func Load(path string) ([]byte, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("master key must be a regular file")
	}
	if len(encoded) != keySize {
		return nil, fmt.Errorf("master key must contain %d bytes", keySize)
	}
	return encoded, nil
}

func publishKeyNoReplace(source, destination string) (bool, error) {
	return publishKeyNoReplaceWithLink(source, destination, os.Link)
}

func publishKeyNoReplaceWithLink(source, destination string, link func(string, string) error) (bool, error) {
	if err := link(source, destination); err == nil {
		return true, nil
	} else if _, statErr := os.Stat(destination); statErr == nil {
		_ = os.Remove(destination + ".litebox-lock")
		return false, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return false, statErr
	}
	lockPath := destination + ".litebox-lock"
	deadline := time.Now().Add(5 * time.Second)
	for {
		lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			if closeErr := lock.Close(); closeErr != nil {
				_ = os.Remove(lockPath)
				return false, closeErr
			}
			defer func() { _ = os.Remove(lockPath) }()
			if _, statErr := os.Stat(destination); statErr == nil {
				return false, nil
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return false, statErr
			}
			if err := os.Rename(source, destination); err != nil {
				return false, err
			}
			return true, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return false, err
		}
		if _, statErr := os.Stat(destination); statErr == nil {
			_ = os.Remove(lockPath)
			return false, nil
		}
		if time.Now().After(deadline) {
			return false, fmt.Errorf("timed out waiting for master-key publish lock %s; verify no writer is active before removing it", lockPath)
		}
		time.Sleep(10 * time.Millisecond)
	}
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
