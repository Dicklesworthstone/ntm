package audit

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuditLogger_BasicLogging(t *testing.T) {
	// Create temporary directory for test
	tempDir, err := ioutil.TempDir("", "audit_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Override home directory for test
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", oldHome)

	// Create logger
	config := &LoggerConfig{
		SessionID:     "test-session",
		BufferSize:    2, // Small buffer for testing
		FlushInterval: 1 * time.Second,
	}

	logger, err := NewAuditLogger(config)
	if err != nil {
		t.Fatalf("Failed to create audit logger: %v", err)
	}
	defer logger.Close()

	// Log some entries
	entries := []AuditEntry{
		{
			EventType: EventTypeCommand,
			Actor:     ActorUser,
			Target:    "test-target-1",
			Payload:   map[string]interface{}{"command": "ls -la"},
			Metadata:  map[string]interface{}{"user": "testuser"},
		},
		{
			EventType: EventTypeSend,
			Actor:     ActorSystem,
			Target:    "agent-1",
			Payload:   map[string]interface{}{"message": "hello world"},
			Metadata:  map[string]interface{}{"correlation_id": "12345"},
		},
	}

	for _, entry := range entries {
		if err := logger.Log(entry); err != nil {
			t.Errorf("Failed to log entry: %v", err)
		}
	}

	// Flush and close
	if err := logger.Close(); err != nil {
		t.Errorf("Failed to close logger: %v", err)
	}

	// Verify log file exists and has correct content
	auditDir := filepath.Join(tempDir, ".local", "share", "ntm", "audit")
	files, err := ioutil.ReadDir(auditDir)
	if err != nil {
		t.Fatalf("Failed to read audit directory: %v", err)
	}

	if len(files) != 1 {
		t.Fatalf("Expected 1 audit file, got %d", len(files))
	}

	// Read and verify entries
	logPath := filepath.Join(auditDir, files[0].Name())
	content, err := ioutil.ReadFile(logPath)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	lines := splitLines(string(content))
	if len(lines) != 2 {
		t.Fatalf("Expected 2 log entries, got %d", len(lines))
	}

	// Verify each entry has required fields
	for i, line := range lines {
		var entry AuditEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Errorf("Failed to unmarshal entry %d: %v", i, err)
			continue
		}

		// Check required fields
		if entry.SessionID != "test-session" {
			t.Errorf("Entry %d: expected session_id 'test-session', got '%s'", i, entry.SessionID)
		}
		if entry.Checksum == "" {
			t.Errorf("Entry %d: missing checksum", i)
		}
		if entry.SequenceNum == 0 {
			t.Errorf("Entry %d: invalid sequence number", i)
		}
		if entry.Timestamp.IsZero() {
			t.Errorf("Entry %d: missing timestamp", i)
		}
	}
}

func TestAuditLogger_TamperEvidence(t *testing.T) {
	// Create temporary directory for test
	tempDir, err := ioutil.TempDir("", "audit_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Override home directory for test
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", oldHome)

	// Create logger and log entries
	logger, err := NewAuditLogger(DefaultConfig("test-tamper"))
	if err != nil {
		t.Fatalf("Failed to create audit logger: %v", err)
	}

	entries := []AuditEntry{
		{
			EventType: EventTypeCommand,
			Actor:     ActorUser,
			Target:    "test-1",
			Payload:   map[string]interface{}{"cmd": "test1"},
		},
		{
			EventType: EventTypeCommand,
			Actor:     ActorUser,
			Target:    "test-2",
			Payload:   map[string]interface{}{"cmd": "test2"},
		},
		{
			EventType: EventTypeCommand,
			Actor:     ActorUser,
			Target:    "test-3",
			Payload:   map[string]interface{}{"cmd": "test3"},
		},
	}

	for _, entry := range entries {
		if err := logger.Log(entry); err != nil {
			t.Errorf("Failed to log entry: %v", err)
		}
	}

	if err := logger.Close(); err != nil {
		t.Errorf("Failed to close logger: %v", err)
	}

	// Find log file
	auditDir := filepath.Join(tempDir, ".local", "share", "ntm", "audit")
	files, err := ioutil.ReadDir(auditDir)
	if err != nil {
		t.Fatalf("Failed to read audit directory: %v", err)
	}
	logPath := filepath.Join(auditDir, files[0].Name())

	// Verify integrity passes
	if err := VerifyIntegrity(logPath); err != nil {
		t.Errorf("Integrity verification failed: %v", err)
	}

	// Tamper with the log file
	content, err := ioutil.ReadFile(logPath)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	newline := strings.IndexByte(string(content), '\n')
	if newline < 0 {
		t.Fatal("Expected audit log to contain at least one newline")
	}
	trailingContent := string(content[:newline]) + " trailing-data" + string(content[newline:])
	if err := ioutil.WriteFile(logPath, []byte(trailingContent), 0644); err != nil {
		t.Fatalf("Failed to write trailing-data tampered file: %v", err)
	}
	if err := VerifyIntegrity(logPath); err == nil {
		t.Error("Expected integrity verification to fail when a line has trailing data")
	}
	if err := ioutil.WriteFile(logPath, content, 0644); err != nil {
		t.Fatalf("Failed to restore original audit log: %v", err)
	}

	// Replace part of the content
	tamperedContent := string(content)
	tamperedContent = tamperedContent[:len(tamperedContent)-10] + "tampered\n"

	if err := ioutil.WriteFile(logPath, []byte(tamperedContent), 0644); err != nil {
		t.Fatalf("Failed to write tampered file: %v", err)
	}

	// Verify integrity now fails
	if err := VerifyIntegrity(logPath); err == nil {
		t.Error("Expected integrity verification to fail after tampering")
	}
}

func TestAuditLogger_ConcurrentWrites(t *testing.T) {
	// Create temporary directory for test
	tempDir, err := ioutil.TempDir("", "audit_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Override home directory for test
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", oldHome)

	// Create logger
	logger, err := NewAuditLogger(DefaultConfig("test-concurrent"))
	if err != nil {
		t.Fatalf("Failed to create audit logger: %v", err)
	}
	defer logger.Close()

	// Launch multiple goroutines to write concurrently
	numWorkers := 100
	entriesPerWorker := 2
	done := make(chan error, numWorkers)

	for worker := 0; worker < numWorkers; worker++ {
		go func(workerID int) {
			for i := 0; i < entriesPerWorker; i++ {
				entry := AuditEntry{
					EventType: EventTypeCommand,
					Actor:     ActorUser,
					Target:    "test-target",
					Payload: map[string]interface{}{
						"worker": workerID,
						"entry":  i,
					},
				}

				if err := logger.Log(entry); err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}(worker)
	}

	// Wait for all workers to complete
	for i := 0; i < numWorkers; i++ {
		if err := <-done; err != nil {
			t.Errorf("Worker failed: %v", err)
		}
	}

	// Flush and close
	if err := logger.Close(); err != nil {
		t.Errorf("Failed to close logger: %v", err)
	}

	// Verify we have all entries
	auditDir := filepath.Join(tempDir, ".local", "share", "ntm", "audit")
	files, err := ioutil.ReadDir(auditDir)
	if err != nil {
		t.Fatalf("Failed to read audit directory: %v", err)
	}

	logPath := filepath.Join(auditDir, files[0].Name())
	content, err := ioutil.ReadFile(logPath)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	lines := splitLines(string(content))
	expectedEntries := numWorkers * entriesPerWorker
	if len(lines) != expectedEntries {
		t.Errorf("Expected %d entries, got %d", expectedEntries, len(lines))
	}

	// Verify integrity
	if err := VerifyIntegrity(logPath); err != nil {
		t.Errorf("Integrity verification failed: %v", err)
	}
}

func TestAuditLogger_ResumeFromExistingLog(t *testing.T) {
	// Create temporary directory for test
	tempDir, err := ioutil.TempDir("", "audit_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Override home directory for test
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", oldHome)

	sessionID := "test-resume"

	// Create first logger and write some entries
	logger1, err := NewAuditLogger(DefaultConfig(sessionID))
	if err != nil {
		t.Fatalf("Failed to create first audit logger: %v", err)
	}

	for i := 0; i < 3; i++ {
		entry := AuditEntry{
			EventType: EventTypeCommand,
			Actor:     ActorUser,
			Target:    "test",
			Payload:   map[string]interface{}{"iteration": i},
		}
		if err := logger1.Log(entry); err != nil {
			t.Errorf("Failed to log entry %d: %v", i, err)
		}
	}

	if err := logger1.Close(); err != nil {
		t.Errorf("Failed to close first logger: %v", err)
	}

	// Create second logger with same session (should resume)
	logger2, err := NewAuditLogger(DefaultConfig(sessionID))
	if err != nil {
		t.Fatalf("Failed to create second audit logger: %v", err)
	}

	// Write more entries
	for i := 3; i < 6; i++ {
		entry := AuditEntry{
			EventType: EventTypeCommand,
			Actor:     ActorUser,
			Target:    "test",
			Payload:   map[string]interface{}{"iteration": i},
		}
		if err := logger2.Log(entry); err != nil {
			t.Errorf("Failed to log entry %d: %v", i, err)
		}
	}

	if err := logger2.Close(); err != nil {
		t.Errorf("Failed to close second logger: %v", err)
	}

	// Verify log has all entries and maintains integrity
	auditDir := filepath.Join(tempDir, ".local", "share", "ntm", "audit")
	files, err := ioutil.ReadDir(auditDir)
	if err != nil {
		t.Fatalf("Failed to read audit directory: %v", err)
	}

	logPath := filepath.Join(auditDir, files[0].Name())
	content, err := ioutil.ReadFile(logPath)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	lines := splitLines(string(content))
	if len(lines) != 6 {
		t.Fatalf("Expected 6 entries, got %d", len(lines))
	}

	// Verify integrity
	if err := VerifyIntegrity(logPath); err != nil {
		t.Errorf("Integrity verification failed: %v", err)
	}

	// Verify sequence numbers are continuous
	for i, line := range lines {
		var entry AuditEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Errorf("Failed to unmarshal entry %d: %v", i, err)
			continue
		}

		expectedSeq := uint64(i + 1)
		if entry.SequenceNum != expectedSeq {
			t.Errorf("Entry %d: expected sequence %d, got %d", i, expectedSeq, entry.SequenceNum)
		}
	}
}

// Helper function to split content into non-empty lines
func splitLines(content string) []string {
	parts := strings.Split(content, "\n")
	lines := make([]string, 0, len(parts))
	for _, line := range parts {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}
