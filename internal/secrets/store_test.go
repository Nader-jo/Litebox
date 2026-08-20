package secrets

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestLoadOrCreateIsAtomicAcrossConcurrentCallers(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".litebox", "master.key")
	start := make(chan struct{})
	keys := make(chan []byte, 8)
	errors := make(chan error, 8)
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			key, err := LoadOrCreate(path)
			keys <- key
			errors <- err
		}()
	}
	close(start)
	wait.Wait()
	close(keys)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var expected []byte
	for key := range keys {
		if expected == nil {
			expected = key
		} else if !bytes.Equal(expected, key) {
			t.Fatal("concurrent callers observed different master keys")
		}
	}
	if len(expected) != keySize {
		t.Fatalf("key length = %d, want %d", len(expected), keySize)
	}
}

func TestLoadNeverCreatesMissingKeyAndAcceptsReadOnlySecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	if _, err := Load(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load missing key error = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load created a key: %v", err)
	}
	expected := bytes.Repeat([]byte{0x42}, keySize)
	if err := os.WriteFile(path, expected, 0o444); err != nil {
		t.Fatal(err)
	}
	actual, err := Load(path)
	if err != nil || !bytes.Equal(actual, expected) {
		t.Fatalf("read-only key = %x, err=%v", actual, err)
	}
}

func TestPublishKeyFallbackSerializesConcurrentWriters(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "master.key")
	unsupported := func(string, string) error { return errors.New("hard links unsupported") }
	keys := [][]byte{bytes.Repeat([]byte{0x11}, keySize), bytes.Repeat([]byte{0x22}, keySize)}
	start := make(chan struct{})
	type result struct {
		created bool
		err     error
	}
	results := make(chan result, len(keys))
	for index, key := range keys {
		source := filepath.Join(root, fmt.Sprintf("candidate-%d", index))
		if err := os.WriteFile(source, key, 0o600); err != nil {
			t.Fatal(err)
		}
		go func() {
			<-start
			created, err := publishKeyNoReplaceWithLink(source, destination, unsupported)
			results <- result{created: created, err: err}
		}()
	}
	close(start)
	created := 0
	for range keys {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.created {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created writers = %d, want 1", created)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || (!bytes.Equal(contents, keys[0]) && !bytes.Equal(contents, keys[1])) {
		t.Fatalf("published master key=%x err=%v", contents, err)
	}
}
