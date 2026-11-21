package events

import (
	"bytes"
	"crypto/rand"
	"os"
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

func TestEventEncryptionUnavailableWriteKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  []byte
	}{
		{name: "missing"},
		{name: "empty", key: []byte{}},
		{name: "invalid", key: []byte("invalid-key")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events.jsonl")
			readKey := evtTestKey(t)
			SetEncryptionConfig(&EncryptionConfig{Enabled: true, EncryptKey: readKey, DecryptKeys: [][]byte{readKey}})
			t.Cleanup(func() { SetEncryptionConfig(nil) })
			logger, err := NewLogger(LoggerOptions{Path: path, Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			defer closeLogger(logger)
			if err := logger.Log(NewEvent(EventSessionCreate, "existing", nil)); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			SetEncryptionConfig(&EncryptionConfig{Enabled: true, EncryptKey: tc.key, DecryptKeys: [][]byte{readKey}})
			err = logger.Log(NewEvent(EventPromptSend, "private-event-must-not-become-plaintext", nil))
			if !encryption.IsKind(err, encryption.ErrInvalidKey) {
				t.Fatalf("Log must reject unavailable write key: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("failed append changed existing encrypted events: %v", err)
			}
			entries, err := ReadSince(path, time.Time{})
			if err != nil || len(entries) != 1 || entries[0].Session != "existing" {
				t.Fatalf("read keyring must remain usable without a write key: entries=%+v error=%v", entries, err)
			}
		})
	}
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

// ReadTypesSince returns only the requested types — from plaintext and
// encrypted lines alike — and never a line that merely quotes a type name.
func TestReadTypesSince_FiltersByDecodedType(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	logger, err := NewLogger(LoggerOptions{Path: logPath, RetentionDays: 30, Enabled: true})
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	crash := AgentLifecycleData{PaneID: "%3", PaneIndex: 1, AgentType: "cc", Reason: "process exited"}
	if err := logger.LogEvent(EventAgentCrash, "proj", crash); err != nil {
		t.Fatalf("Log crash: %v", err)
	}
	// A prompt whose payload quotes the marker must not pass as a crash.
	if err := logger.LogEvent(EventPromptSend, "proj", map[string]interface{}{"template": `"type":"agent_crash"`}); err != nil {
		t.Fatalf("Log prompt: %v", err)
	}

	key := evtTestKey(t)
	SetEncryptionConfig(&EncryptionConfig{Enabled: true, EncryptKey: key, DecryptKeys: [][]byte{key}})
	defer SetEncryptionConfig(nil)
	if err := logger.LogEvent(EventAgentRestart, "proj", AgentLifecycleData{PaneID: "%3", RestartCount: 1}); err != nil {
		t.Fatalf("Log restart: %v", err)
	}
	if err := closeLogger(logger); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := ReadTypesSince(logPath, time.Time{}, EventAgentCrash, EventAgentRestart)
	if err != nil {
		t.Fatalf("ReadTypesSince: %v", err)
	}
	if len(got) != 2 || got[0].Type != EventAgentCrash || got[1].Type != EventAgentRestart {
		t.Fatalf("got %+v, want the crash then the (encrypted) restart", got)
	}
	if got[0].Data["pane_id"] != "%3" || got[0].Data["reason"] != "process exited" || got[1].Data["restart_count"] != float64(1) {
		t.Fatalf("lifecycle payloads = %v / %v", got[0].Data, got[1].Data)
	}

	if later, err := ReadTypesSince(logPath, time.Now().Add(time.Hour), EventAgentCrash); err != nil || len(later) != 0 {
		t.Fatalf("since bound: %+v, %v", later, err)
	}
	if none, err := ReadTypesSince(logPath, time.Time{}); err != nil || len(none) != 0 {
		t.Fatalf("no types requested: %+v, %v", none, err)
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
