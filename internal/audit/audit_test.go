package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

func TestAuditLogger_LogPopulatesFields(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)

	logger, err := NewAuditLogger(&LoggerConfig{
		SessionID:     "test-fields",
		BufferSize:    1,
		FlushInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("Failed to create audit logger: %v", err)
	}

	entry1 := AuditEntry{
		EventType: EventTypeCommand,
		Actor:     ActorUser,
		Target:    "session.create",
		Payload:   map[string]interface{}{"args": "--cc=1"},
	}
	entry2 := AuditEntry{
		EventType: EventTypeSend,
		Actor:     ActorSystem,
		Target:    "agent.send",
		Payload:   map[string]interface{}{"preview": "hello"},
	}

	if err := logger.Log(entry1); err != nil {
		logger.Close()
		t.Fatalf("Failed to log entry1: %v", err)
	}
	if err := logger.Log(entry2); err != nil {
		logger.Close()
		t.Fatalf("Failed to log entry2: %v", err)
	}

	if err := logger.Close(); err != nil {
		t.Fatalf("Failed to close logger: %v", err)
	}

	auditDir := filepath.Join(tempDir, ".local", "share", "ntm", "audit")
	files, err := os.ReadDir(auditDir)
	if err != nil {
		t.Fatalf("Failed to read audit directory: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("Expected 1 audit file, got %d", len(files))
	}

	logPath := filepath.Join(auditDir, files[0].Name())
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("Failed to read audit log: %v", err)
	}

	lines := splitLines(string(content))
	if len(lines) != 2 {
		t.Fatalf("Expected 2 log entries, got %d", len(lines))
	}

	var logged1, logged2 AuditEntry
	if err := json.Unmarshal([]byte(lines[0]), &logged1); err != nil {
		t.Fatalf("Failed to unmarshal entry1: %v", err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &logged2); err != nil {
		t.Fatalf("Failed to unmarshal entry2: %v", err)
	}

	if logged1.SessionID != "test-fields" {
		t.Fatalf("Entry1 session mismatch: %q", logged1.SessionID)
	}
	if logged1.Timestamp.IsZero() {
		t.Fatalf("Entry1 timestamp missing")
	}
	if logged1.SequenceNum != 1 {
		t.Fatalf("Entry1 sequence expected 1, got %d", logged1.SequenceNum)
	}
	if logged1.Checksum == "" {
		t.Fatalf("Entry1 checksum missing")
	}
	if logged1.PrevHash != "" {
		t.Fatalf("Entry1 prev hash should be empty, got %q", logged1.PrevHash)
	}
	if logged1.EventType != entry1.EventType || logged1.Actor != entry1.Actor || logged1.Target != entry1.Target {
		t.Fatalf("Entry1 fields not preserved")
	}

	if logged2.SequenceNum != 2 {
		t.Fatalf("Entry2 sequence expected 2, got %d", logged2.SequenceNum)
	}
	if logged2.PrevHash != logged1.Checksum {
		t.Fatalf("Entry2 prev hash mismatch")
	}
}

func TestAuditLogger_FlushesOnBufferSize(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)

	logger, err := NewAuditLogger(&LoggerConfig{
		SessionID:     "test-flush",
		BufferSize:    2,
		FlushInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("Failed to create audit logger: %v", err)
	}

	entry := AuditEntry{
		EventType: EventTypeCommand,
		Actor:     ActorUser,
		Target:    "flush.test",
	}

	if err := logger.Log(entry); err != nil {
		logger.Close()
		t.Fatalf("Failed to log entry 1: %v", err)
	}
	if err := logger.Log(entry); err != nil {
		logger.Close()
		t.Fatalf("Failed to log entry 2: %v", err)
	}

	info, err := logger.file.Stat()
	if err != nil {
		logger.Close()
		t.Fatalf("Failed to stat log file: %v", err)
	}
	if info.Size() == 0 {
		logger.Close()
		t.Fatalf("Expected flushed data after reaching buffer size")
	}

	if err := logger.Close(); err != nil {
		t.Fatalf("Failed to close logger: %v", err)
	}
}

func TestLogEvent_RedactsSecrets(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	t.Setenv("NTM_TEST_MODE", "")
	t.Setenv("NTM_E2E", "")

	oldArg0 := os.Args[0]
	os.Args[0] = "ntm"
	t.Cleanup(func() {
		os.Args[0] = oldArg0
	})

	SetRedactionConfig(&redaction.Config{Mode: redaction.ModeRedact})
	t.Cleanup(func() {
		SetRedactionConfig(nil)
	})

	payload := map[string]interface{}{
		"token": "token=abcdefghijklmnopqrstuvwxyz",
	}

	if err := LogEvent("", EventTypeCommand, ActorUser, "redaction.test", payload, nil); err != nil {
		t.Fatalf("LogEvent failed: %v", err)
	}
	if err := CloseAll(); err != nil {
		t.Fatalf("CloseAll failed: %v", err)
	}

	auditDir := filepath.Join(tempDir, ".local", "share", "ntm", "audit")
	files, err := os.ReadDir(auditDir)
	if err != nil {
		t.Fatalf("Failed to read audit directory: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("Expected 1 audit file, got %d", len(files))
	}

	logPath := filepath.Join(auditDir, files[0].Name())
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("Failed to read audit log: %v", err)
	}

	lines := splitLines(string(content))
	if len(lines) != 1 {
		t.Fatalf("Expected 1 log entry, got %d", len(lines))
	}

	var entry AuditEntry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("Failed to unmarshal entry: %v", err)
	}

	redacted, ok := entry.Payload["token"].(string)
	if !ok {
		t.Fatalf("Expected string payload token, got %T", entry.Payload["token"])
	}
	if redacted == payload["token"] {
		t.Fatalf("Expected token to be redacted")
	}
	if !strings.HasPrefix(redacted, "[REDACTED:") {
		t.Fatalf("Expected redaction placeholder, got %q", redacted)
	}
}

func TestNewCorrelationID_IsUnique(t *testing.T) {
	id1 := NewCorrelationID()
	id2 := NewCorrelationID()

	if id1 == id2 {
		t.Fatalf("Expected unique correlation IDs")
	}
	if !strings.HasPrefix(id1, "cmd-") || !strings.HasPrefix(id2, "cmd-") {
		t.Fatalf("Expected correlation IDs to start with cmd-")
	}
}

func TestAuditLogger_FlushMethod(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)

	logger, err := NewAuditLogger(&LoggerConfig{
		SessionID:     "flush-method",
		BufferSize:    10,
		FlushInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("Failed to create audit logger: %v", err)
	}

	if err := logger.Log(AuditEntry{
		EventType: EventTypeCommand,
		Actor:     ActorUser,
		Target:    "flush.method",
	}); err != nil {
		logger.Close()
		t.Fatalf("Failed to log entry: %v", err)
	}

	if err := logger.Flush(); err != nil {
		logger.Close()
		t.Fatalf("Flush failed: %v", err)
	}

	if err := logger.Close(); err != nil {
		t.Fatalf("Failed to close logger: %v", err)
	}
}

func TestAuditLogger_FlushTimer(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)

	logger, err := NewAuditLogger(&LoggerConfig{
		SessionID:     "flush-timer",
		BufferSize:    10,
		FlushInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Failed to create audit logger: %v", err)
	}

	if err := logger.Log(AuditEntry{
		EventType: EventTypeCommand,
		Actor:     ActorUser,
		Target:    "flush.timer",
	}); err != nil {
		logger.Close()
		t.Fatalf("Failed to log entry: %v", err)
	}

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		logger.mutex.Lock()
		pending := logger.entriesWritten
		logger.mutex.Unlock()
		if pending == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	logger.mutex.Lock()
	pending := logger.entriesWritten
	logger.mutex.Unlock()
	if pending != 0 {
		logger.Close()
		t.Fatalf("Expected flush timer to clear pending entries")
	}

	if err := logger.Close(); err != nil {
		t.Fatalf("Failed to close logger: %v", err)
	}
}

func TestSanitizeValue_ModeOff(t *testing.T) {
	SetRedactionConfig(&redaction.Config{Mode: redaction.ModeOff})
	t.Cleanup(func() {
		SetRedactionConfig(nil)
	})

	input := map[string]interface{}{
		"token": "token=abcdefghijklmnopqrstuvwxyz",
		"list":  []string{"alpha", "beta"},
		"nested": []interface{}{
			"gamma",
			map[string]interface{}{"key": "value"},
		},
	}

	out := sanitizeValue(input).(map[string]interface{})
	if out["token"].(string) != input["token"] {
		t.Fatalf("Expected token to remain unredacted in ModeOff")
	}
	if got := out["list"].([]string); len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("Expected list values preserved, got %#v", got)
	}

	nested := out["nested"].([]interface{})
	if nested[0].(string) != "gamma" {
		t.Fatalf("Expected nested value preserved")
	}
	if nestedMap := nested[1].(map[string]interface{}); nestedMap["key"].(string) != "value" {
		t.Fatalf("Expected nested map value preserved")
	}
}

func TestSanitizeValue_DefaultCase(t *testing.T) {
	SetRedactionConfig(&redaction.Config{Mode: redaction.ModeOff})
	t.Cleanup(func() {
		SetRedactionConfig(nil)
	})

	// Non-string, non-slice, non-map types should pass through unchanged
	tests := []struct {
		name  string
		input interface{}
	}{
		{"int", 42},
		{"float64", 3.14},
		{"bool", true},
		{"nil", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeValue(tt.input)
			if got != tt.input {
				t.Errorf("sanitizeValue(%v) = %v, want %v", tt.input, got, tt.input)
			}
		})
	}
}

func TestShouldSkipAudit(t *testing.T) {
	oldArg0 := os.Args[0]
	os.Args[0] = "ntm"
	t.Cleanup(func() {
		os.Args[0] = oldArg0
	})

	t.Setenv("NTM_TEST_MODE", "1")
	if !shouldSkipAudit("") {
		t.Fatalf("Expected shouldSkipAudit to return true when NTM_TEST_MODE is set")
	}

	t.Setenv("NTM_TEST_MODE", "")
	t.Setenv("NTM_E2E", "")
	if shouldSkipAudit("") {
		t.Fatalf("Expected shouldSkipAudit to return false when test flags are cleared")
	}
}

// bd-ijgum: getRedactionConfig must deep-copy the stored redaction.Config
// so a caller mutating the returned slices/map cannot reach back into
// the shared state. Pre-fix the getter returned redactionCfg directly,
// value-copying Mode but aliasing Allowlist / ExtraPatterns /
// DisabledCategories. bd-pmdpn's symmetric Set/Get DeepCopy invariant
// was missed on this getter.
func TestGetRedactionConfig_DeepCopiesSliceMapFields(t *testing.T) {
	t.Cleanup(func() { SetRedactionConfig(nil) })

	original := &redaction.Config{
		Mode:               redaction.ModeRedact,
		Allowlist:          []string{"^foo$"},
		ExtraPatterns:      map[redaction.Category][]string{"api_key": {"^xyz$"}},
		DisabledCategories: []redaction.Category{"email"},
	}
	SetRedactionConfig(original)

	got := getRedactionConfig()
	// Mutate the returned struct's reference-typed fields. Pre-fix
	// these mutations leaked back into the stored config because the
	// getter returned redactionCfg by value (Allowlist etc. shared
	// the same backing array).
	got.Allowlist[0] = "MUTATED"
	got.ExtraPatterns["api_key"][0] = "MUTATED"
	got.DisabledCategories[0] = "MUTATED"

	got2 := getRedactionConfig()
	if got2.Allowlist[0] != "^foo$" {
		t.Errorf("getter leaked Allowlist mutation: %q", got2.Allowlist[0])
	}
	if got2.ExtraPatterns["api_key"][0] != "^xyz$" {
		t.Errorf("getter leaked ExtraPatterns mutation: %q", got2.ExtraPatterns["api_key"][0])
	}
	if got2.DisabledCategories[0] != "email" {
		t.Errorf("getter leaked DisabledCategories mutation: %q", got2.DisabledCategories[0])
	}
}
