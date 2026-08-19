package events

import (
	"crypto/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/encryption"
)

func evtTestKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, encryption.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestSetEncryptionConfig_Events(t *testing.T) {
	defer SetEncryptionConfig(nil)

	t.Run("nil disables", func(t *testing.T) {
		SetEncryptionConfig(nil)
		if encryptionEnabledForTest() {
			t.Error("expected disabled")
		}
	})

	t.Run("enabled with key", func(t *testing.T) {
		key := evtTestKey(t)
		SetEncryptionConfig(&EncryptionConfig{
			Enabled:     true,
			EncryptKey:  key,
			DecryptKeys: [][]byte{key},
		})
		if !encryptionEnabledForTest() {
			t.Error("expected enabled")
		}
	})
}

func TestEncryptedEventLogRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "events.jsonl")

	key := evtTestKey(t)
	SetEncryptionConfig(&EncryptionConfig{
		Enabled:     true,
		EncryptKey:  key,
		DecryptKeys: [][]byte{key},
	})
	defer SetEncryptionConfig(nil)

	logger, err := NewLogger(LoggerOptions{
		Path:          logPath,
		RetentionDays: 30,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	defer closeLogger(logger)

	// Write encrypted events
	event1 := NewEvent(EventSessionCreate, "test-session", map[string]interface{}{
		"agents": 3,
	})
	event2 := NewEvent(EventPromptSend, "test-session", map[string]interface{}{
		"targets": 2,
	})

	if err := logger.Log(event1); err != nil {
		t.Fatalf("Log event1: %v", err)
	}
	if err := logger.Log(event2); err != nil {
		t.Fatalf("Log event2: %v", err)
	}

	// Read them back
	events, err := ReadSince(logger.path, time.Time{})
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Type != EventSessionCreate {
		t.Errorf("event 0 type = %q, want %q", events[0].Type, EventSessionCreate)
	}
	if events[1].Type != EventPromptSend {
		t.Errorf("event 1 type = %q, want %q", events[1].Type, EventPromptSend)
	}
}

// ReadSince backs read-only surfaces such as `ntm analytics`. Reading the raw
// lines there silently reported zero events whenever encryption was enabled, so
// pin the decrypting behavior.
func TestReadSince_DecryptsEncryptedLog(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "events.jsonl")

	key := evtTestKey(t)
	SetEncryptionConfig(&EncryptionConfig{
		Enabled:     true,
		EncryptKey:  key,
		DecryptKeys: [][]byte{key},
	})
	defer SetEncryptionConfig(nil)

	logger, err := NewLogger(LoggerOptions{Path: logPath, RetentionDays: 30, Enabled: true})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	if err := logger.Log(NewEvent(EventSessionCreate, "read-since", map[string]interface{}{"agents": 2})); err != nil {
		t.Fatalf("Log: %v", err)
	}
	if err := closeLogger(logger); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := ReadSince(logPath, time.Time{})
	if err != nil {
		t.Fatalf("ReadSince: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1 (encrypted lines must be decrypted, not skipped)", len(got))
	}
	if got[0].Type != EventSessionCreate || got[0].Session != "read-since" {
		t.Fatalf("got event %+v, want session create for %q", got[0], "read-since")
	}

	// Without keys the same log must not silently masquerade as empty-but-fine:
	// the lines are skipped, but the caller sees zero events rather than plaintext.
	SetEncryptionConfig(nil)
	blind, err := ReadSince(logPath, time.Time{})
	if err != nil {
		t.Fatalf("ReadSince without keys: %v", err)
	}
	if len(blind) != 0 {
		t.Fatalf("got %d events without keys, want 0", len(blind))
	}
}

func TestReadSince_MissingFile(t *testing.T) {
	if _, err := ReadSince(filepath.Join(t.TempDir(), "absent.jsonl"), time.Time{}); err == nil {
		t.Fatal("expected an error for a missing log file")
	}
}

func TestMixedPlaintextAndEncryptedEvents(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "events.jsonl")

	// Write plaintext events first
	SetEncryptionConfig(nil)

	logger, err := NewLogger(LoggerOptions{
		Path:          logPath,
		RetentionDays: 30,
		Enabled:       true,
	})
	if err != nil {
		t.Fatal(err)
	}

	plainEvt := NewEvent(EventSessionCreate, "s1", nil)
	if err := logger.Log(plainEvt); err != nil {
		t.Fatal(err)
	}

	// Enable encryption and write more
	key := evtTestKey(t)
	SetEncryptionConfig(&EncryptionConfig{
		Enabled:     true,
		EncryptKey:  key,
		DecryptKeys: [][]byte{key},
	})
	defer SetEncryptionConfig(nil)

	encEvt := NewEvent(EventPromptSend, "s1", nil)
	if err := logger.Log(encEvt); err != nil {
		t.Fatal(err)
	}
	closeLogger(logger)

	// Re-open and read all
	logger2, err := NewLogger(LoggerOptions{
		Path:          logPath,
		RetentionDays: 30,
		Enabled:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeLogger(logger2)

	events, err := ReadSince(logger2.path, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events (mixed), got %d", len(events))
	}
}

// encryptionEnabledForTest reports whether encryption is currently enabled.
func encryptionEnabledForTest() bool {
	encryptMu.RLock()
	defer encryptMu.RUnlock()
	return encryptionEnabled
}
