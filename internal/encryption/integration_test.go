package encryption_test

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/encryption"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/history"
)

const plaintextMarker = "ENCRYPTION_E2E_CANARY_8f3a2b91"

func generateKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, encryption.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

// TestIntegration_HistoryNotPlaintext verifies that when encryption is enabled,
// the on-disk history.jsonl file does not contain plaintext prompt content.
func TestIntegration_HistoryNotPlaintext(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	t.Setenv("XDG_DATA_HOME", dataDir)

	key := generateKey(t)
	history.SetEncryptionConfig(&history.EncryptionConfig{
		Enabled:     true,
		EncryptKey:  key,
		DecryptKeys: [][]byte{key},
	})
	defer history.SetEncryptionConfig(nil)

	// Write entries containing the plaintext marker
	if err := history.Append(&history.HistoryEntry{
		ID:        "enc-test-1",
		Session:   "e2e-encryption",
		Prompt:    "This prompt contains the marker: " + plaintextMarker,
		Timestamp: time.Now(),
		Source:    history.SourceCLI,
		Targets:   []string{"1", "2"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	if err := history.Append(&history.HistoryEntry{
		ID:        "enc-test-2",
		Session:   "e2e-encryption",
		Prompt:    "Second entry with marker " + plaintextMarker + " included",
		Timestamp: time.Now(),
		Source:    history.SourceCLI,
	}); err != nil {
		t.Fatalf("Append (second entry): %v", err)
	}

	// Read the raw file
	histPath := history.StoragePath()
	raw, err := os.ReadFile(histPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	fileHash := sha256.Sum256(raw)
	t.Logf("file_size=%d sha256=%s", len(raw), hex.EncodeToString(fileHash[:]))

	// Plaintext marker must NOT appear in raw file
	if bytes.Contains(raw, []byte(plaintextMarker)) {
		t.Fatalf("SECURITY: plaintext marker %q found in encrypted history file", plaintextMarker)
	}
	if bytes.Contains(raw, []byte(`"prompt"`)) {
		t.Error("SECURITY: JSON field name '\"prompt\"' found in encrypted history file")
	}

	// Each line should be base64, not JSON
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	for i, line := range lines {
		if len(line) > 0 && line[0] == '{' {
			t.Errorf("line %d starts with '{', expected encrypted (base64)", i)
		}
	}

	// Verify decryption round-trip
	entries, err := history.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	found := false
	for _, e := range entries {
		if strings.Contains(e.Prompt, plaintextMarker) {
			found = true
			break
		}
	}
	if !found {
		t.Error("plaintext marker not found in decrypted entries")
	}
}

// TestIntegration_EventLogNotPlaintext verifies that the events log is encrypted on disk.
func TestIntegration_EventLogNotPlaintext(t *testing.T) {
	tmpDir := t.TempDir()
	evtPath := filepath.Join(tmpDir, "events.jsonl")

	key := generateKey(t)
	events.SetEncryptionConfig(&events.EncryptionConfig{
		Enabled:     true,
		EncryptKey:  key,
		DecryptKeys: [][]byte{key},
	})
	defer events.SetEncryptionConfig(nil)

	evtLogger, err := events.NewLogger(events.LoggerOptions{
		Path:          evtPath,
		RetentionDays: 30,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	if err := evtLogger.Log(events.NewEvent(events.EventSessionCreate, "e2e-encryption", map[string]interface{}{
		"marker": plaintextMarker,
		"agents": 3,
	})); err != nil {
		t.Fatal(err)
	}
	if err := evtLogger.Log(events.NewEvent(events.EventPromptSend, "e2e-encryption", map[string]interface{}{
		"prompt_preview": "Prompt with " + plaintextMarker,
	})); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(evtPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	fileHash := sha256.Sum256(raw)
	t.Logf("file_size=%d sha256=%s", len(raw), hex.EncodeToString(fileHash[:]))

	if bytes.Contains(raw, []byte(plaintextMarker)) {
		t.Fatalf("SECURITY: plaintext marker found in encrypted event log")
	}
	if bytes.Contains(raw, []byte(`"session"`)) {
		t.Error("SECURITY: JSON field name found in encrypted event log")
	}

	allEvents, err := events.ReadSince(evtPath, time.Time{})
	if err != nil {
		t.Fatalf("ReadSince: %v", err)
	}
	if len(allEvents) != 2 {
		t.Fatalf("expected 2 events, got %d", len(allEvents))
	}
}

// TestIntegration_BackwardCompatibility verifies mixed plaintext/encrypted files work.
func TestIntegration_BackwardCompatibility(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	t.Setenv("XDG_DATA_HOME", dataDir)

	// Phase 1: Write plaintext (no encryption)
	history.SetEncryptionConfig(nil)
	if err := history.Append(&history.HistoryEntry{
		ID: "plain-compat", Session: "compat", Prompt: "Plaintext " + plaintextMarker,
		Timestamp: time.Now(), Source: history.SourceCLI,
	}); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(history.StoragePath())
	if !bytes.Contains(raw, []byte(plaintextMarker)) {
		t.Fatal("sanity check: marker should be plaintext in unencrypted file")
	}

	// Phase 2: Enable encryption
	key := generateKey(t)
	history.SetEncryptionConfig(&history.EncryptionConfig{
		Enabled:     true,
		EncryptKey:  key,
		DecryptKeys: [][]byte{key},
	})
	defer history.SetEncryptionConfig(nil)

	if err := history.Append(&history.HistoryEntry{
		ID: "enc-compat", Session: "compat", Prompt: "Encrypted entry",
		Timestamp: time.Now(), Source: history.SourceCLI,
	}); err != nil {
		t.Fatal(err)
	}

	// Phase 3: Both entries readable
	entries, err := history.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].ID != "plain-compat" || entries[1].ID != "enc-compat" {
		t.Errorf("unexpected IDs: %q, %q", entries[0].ID, entries[1].ID)
	}

	// Verify mixed: line 1 plaintext, line 2 encrypted
	raw2, _ := os.ReadFile(history.StoragePath())
	lines := bytes.Split(bytes.TrimSpace(raw2), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if lines[0][0] != '{' {
		t.Error("line 0 should be plaintext JSON")
	}
	if lines[1][0] == '{' {
		t.Error("line 1 should be encrypted")
	}
}

// TestIntegration_KeyRotation verifies key rotation works across history entries.
func TestIntegration_KeyRotation(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	t.Setenv("XDG_DATA_HOME", dataDir)

	key1 := generateKey(t)
	key2 := generateKey(t)

	// Write with key1
	history.SetEncryptionConfig(&history.EncryptionConfig{
		Enabled: true, EncryptKey: key1, DecryptKeys: [][]byte{key1},
	})
	if err := history.Append(&history.HistoryEntry{
		ID: "rot-1", Session: "rotation", Prompt: "key1 " + plaintextMarker,
		Timestamp: time.Now(), Source: history.SourceCLI,
	}); err != nil {
		t.Fatal(err)
	}

	// Rotate to key2, keep key1 in keyring
	history.SetEncryptionConfig(&history.EncryptionConfig{
		Enabled: true, EncryptKey: key2, DecryptKeys: [][]byte{key2, key1},
	})
	defer history.SetEncryptionConfig(nil)

	if err := history.Append(&history.HistoryEntry{
		ID: "rot-2", Session: "rotation", Prompt: "key2",
		Timestamp: time.Now(), Source: history.SourceCLI,
	}); err != nil {
		t.Fatal(err)
	}

	// Both readable with combined keyring
	entries, err := history.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2, got %d", len(entries))
	}

	// File fully encrypted
	raw, _ := os.ReadFile(history.StoragePath())
	if bytes.Contains(raw, []byte(plaintextMarker)) {
		t.Fatal("SECURITY: marker in raw file after rotation")
	}

	// key2-only keyring can't read key1 data
	history.SetEncryptionConfig(&history.EncryptionConfig{
		Enabled: true, EncryptKey: key2, DecryptKeys: [][]byte{key2},
	})
	entries2, err := history.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries2) != 1 {
		t.Errorf("expected 1 (key1 skipped), got %d", len(entries2))
	}
}

// TestIntegration_PruneReEncrypts verifies that Prune re-encrypts remaining entries.
func TestIntegration_PruneReEncrypts(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	t.Setenv("XDG_DATA_HOME", dataDir)

	key := generateKey(t)
	history.SetEncryptionConfig(&history.EncryptionConfig{
		Enabled: true, EncryptKey: key, DecryptKeys: [][]byte{key},
	})
	defer history.SetEncryptionConfig(nil)

	for i := 0; i < 5; i++ {
		if err := history.Append(&history.HistoryEntry{
			ID: fmt.Sprintf("prune-%d", i), Session: "prune", Prompt: fmt.Sprintf("entry %d %s", i, plaintextMarker),
			Timestamp: time.Now(), Source: history.SourceCLI,
		}); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := history.Prune(2)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 3 {
		t.Errorf("expected 3 removed, got %d", removed)
	}

	raw, _ := os.ReadFile(history.StoragePath())
	if bytes.Contains(raw, []byte(plaintextMarker)) {
		t.Fatal("SECURITY: marker in file after prune")
	}

	entries, err := history.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 after prune, got %d", len(entries))
	}
}
