package history

import (
	"crypto/rand"
	"os"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/encryption"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, encryption.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestSetEncryptionConfig(t *testing.T) {
	// Reset after test
	defer SetEncryptionConfig(nil)

	t.Run("nil disables", func(t *testing.T) {
		SetEncryptionConfig(nil)
		if encryptionEnabledForTest() {
			t.Error("expected disabled")
		}
	})

	t.Run("enabled with key", func(t *testing.T) {
		key := testKey(t)
		SetEncryptionConfig(&EncryptionConfig{
			Enabled:     true,
			EncryptKey:  key,
			DecryptKeys: [][]byte{key},
		})
		if !encryptionEnabledForTest() {
			t.Error("expected enabled")
		}
	})

	t.Run("enabled without key disables", func(t *testing.T) {
		SetEncryptionConfig(&EncryptionConfig{
			Enabled:    true,
			EncryptKey: nil,
		})
		if encryptionEnabledForTest() {
			t.Error("expected disabled when no key")
		}
	})
}

func TestEncryptedHistoryRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	key := testKey(t)
	SetEncryptionConfig(&EncryptionConfig{
		Enabled:     true,
		EncryptKey:  key,
		DecryptKeys: [][]byte{key},
	})
	defer SetEncryptionConfig(nil)

	// Write encrypted entries
	entry1 := &HistoryEntry{
		ID:        "enc-1",
		Session:   "test",
		Prompt:    "encrypted prompt alpha",
		Timestamp: time.Now(),
		Source:    SourceCLI,
	}
	entry2 := &HistoryEntry{
		ID:        "enc-2",
		Session:   "test",
		Prompt:    "encrypted prompt beta",
		Timestamp: time.Now(),
		Source:    SourceCLI,
	}

	if err := Append(entry1); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := Append(entry2); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Read them back
	entries, err := ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Prompt != "encrypted prompt alpha" {
		t.Errorf("entry 0 prompt = %q, want %q", entries[0].Prompt, "encrypted prompt alpha")
	}
	if entries[1].Prompt != "encrypted prompt beta" {
		t.Errorf("entry 1 prompt = %q, want %q", entries[1].Prompt, "encrypted prompt beta")
	}
}

func TestEncryptedReadRecent(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	key := testKey(t)
	SetEncryptionConfig(&EncryptionConfig{
		Enabled:     true,
		EncryptKey:  key,
		DecryptKeys: [][]byte{key},
	})
	defer SetEncryptionConfig(nil)

	for i := 0; i < 5; i++ {
		e := &HistoryEntry{
			ID:        "r-" + string(rune('a'+i)),
			Session:   "test",
			Prompt:    "prompt " + string(rune('a'+i)),
			Timestamp: time.Now(),
			Source:    SourceCLI,
		}
		if err := Append(e); err != nil {
			t.Fatal(err)
		}
	}

	recent, err := ReadRecent(2)
	if err != nil {
		t.Fatalf("ReadRecent: %v", err)
	}
	if len(recent) != 2 {
		t.Fatalf("expected 2 recent, got %d", len(recent))
	}
}

func TestMixedPlaintextAndEncrypted(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	// Write plaintext entries first (no encryption)
	SetEncryptionConfig(nil)
	plain := &HistoryEntry{
		ID:        "plain-1",
		Session:   "test",
		Prompt:    "plaintext prompt",
		Timestamp: time.Now(),
		Source:    SourceCLI,
	}
	if err := Append(plain); err != nil {
		t.Fatal(err)
	}

	// Now enable encryption and write more
	key := testKey(t)
	SetEncryptionConfig(&EncryptionConfig{
		Enabled:     true,
		EncryptKey:  key,
		DecryptKeys: [][]byte{key},
	})
	defer SetEncryptionConfig(nil)

	enc := &HistoryEntry{
		ID:        "enc-1",
		Session:   "test",
		Prompt:    "encrypted prompt",
		Timestamp: time.Now(),
		Source:    SourceCLI,
	}
	if err := Append(enc); err != nil {
		t.Fatal(err)
	}

	// Both should be readable
	entries, err := ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (mixed), got %d", len(entries))
	}
	if entries[0].ID != "plain-1" {
		t.Errorf("entry 0 ID = %q, want plain-1", entries[0].ID)
	}
	if entries[1].ID != "enc-1" {
		t.Errorf("entry 1 ID = %q, want enc-1", entries[1].ID)
	}
}

func TestKeyRotation(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	key1 := testKey(t)
	key2 := testKey(t)

	// Write with key1
	SetEncryptionConfig(&EncryptionConfig{
		Enabled:     true,
		EncryptKey:  key1,
		DecryptKeys: [][]byte{key1},
	})

	if err := Append(&HistoryEntry{
		ID: "k1-entry", Session: "test", Prompt: "written with key1",
		Timestamp: time.Now(), Source: SourceCLI,
	}); err != nil {
		t.Fatal(err)
	}

	// Rotate to key2, but keep key1 in the keyring for reading old entries
	SetEncryptionConfig(&EncryptionConfig{
		Enabled:     true,
		EncryptKey:  key2,
		DecryptKeys: [][]byte{key2, key1},
	})
	defer SetEncryptionConfig(nil)

	if err := Append(&HistoryEntry{
		ID: "k2-entry", Session: "test", Prompt: "written with key2",
		Timestamp: time.Now(), Source: SourceCLI,
	}); err != nil {
		t.Fatal(err)
	}

	// Both should be readable with the combined keyring
	entries, err := ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].ID != "k1-entry" {
		t.Errorf("entry 0 ID = %q, want k1-entry", entries[0].ID)
	}
	if entries[1].ID != "k2-entry" {
		t.Errorf("entry 1 ID = %q, want k2-entry", entries[1].ID)
	}
}

// encryptionEnabledForTest reports whether encryption is currently enabled.
func encryptionEnabledForTest() bool {
	encryptMu.RLock()
	defer encryptMu.RUnlock()
	return encryptionEnabled
}
