package dcg

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestNewAuditLogger(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "test-dcg-audit.jsonl")

	logger, err := NewAuditLogger(&AuditLoggerConfig{
		Path:     logPath,
		MaxBytes: DefaultMaxBytes,
	})
	if err != nil {
		t.Fatalf("NewAuditLogger() error = %v", err)
	}
	defer logger.Close()

	if logger.path != logPath {
		t.Errorf("Path() = %v, want %v", logger.path, logPath)
	}

	// Verify file was created
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		t.Error("Log file was not created")
	}
}

func TestAuditLogger_LogBlocked(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "test-dcg-audit.jsonl")

	logger, err := NewAuditLogger(&AuditLoggerConfig{
		Path:     logPath,
		MaxBytes: DefaultMaxBytes,
	})
	if err != nil {
		t.Fatalf("NewAuditLogger() error = %v", err)
	}

	// Log a blocked command
	err = logger.LogBlocked(
		"rm -rf /important",
		"agent-1",
		"my-project",
		"rm_recursive_dangerous",
		"Blocked: rm -rf with dangerous path",
	)
	if err != nil {
		t.Fatalf("LogBlocked() error = %v", err)
	}

	// Close to ensure flush
	if err := logger.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	// Read and verify the entry
	file, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("Failed to open log file: %v", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		t.Fatal("No entries in log file")
	}

	var entry AuditEntry
	if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
		t.Fatalf("Failed to unmarshal entry: %v", err)
	}

	if entry.Event != "command_blocked" {
		t.Errorf("Event = %v, want command_blocked", entry.Event)
	}
	if entry.Command != "rm -rf /important" {
		t.Errorf("Command = %v, want rm -rf /important", entry.Command)
	}
	if entry.Pane != "agent-1" {
		t.Errorf("Pane = %v, want agent-1", entry.Pane)
	}
	if entry.Session != "my-project" {
		t.Errorf("Session = %v, want my-project", entry.Session)
	}
	if entry.Rule != "rm_recursive_dangerous" {
		t.Errorf("Rule = %v, want rm_recursive_dangerous", entry.Rule)
	}
	if entry.DCGOutput != "Blocked: rm -rf with dangerous path" {
		t.Errorf("DCGOutput = %v, want Blocked: rm -rf with dangerous path", entry.DCGOutput)
	}
	if entry.Timestamp == "" {
		t.Error("Timestamp is empty")
	}
}

func TestAuditLogger_Rotation(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "test-dcg-audit.jsonl")

	// Create logger with very small max size to trigger rotation
	logger, err := NewAuditLogger(&AuditLoggerConfig{
		Path:     logPath,
		MaxBytes: 100, // Very small to trigger rotation quickly
	})
	if err != nil {
		t.Fatalf("NewAuditLogger() error = %v", err)
	}

	// Log multiple entries to trigger rotation
	for i := 0; i < 10; i++ {
		err = logger.LogBlocked(
			"rm -rf /important",
			"agent-1",
			"my-project",
			"rm_recursive_dangerous",
			"Blocked: rm -rf with dangerous path",
		)
		if err != nil {
			t.Fatalf("LogBlocked() error on iteration %d: %v", i, err)
		}
	}

	if err := logger.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	// Check that rotated files exist
	files, err := filepath.Glob(filepath.Join(tmpDir, "test-dcg-audit-*.jsonl"))
	if err != nil {
		t.Fatalf("Glob error: %v", err)
	}

	if len(files) == 0 {
		t.Error("No rotated files found")
	}
}

func TestAuditLogger_ConcurrentLogging(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "test-dcg-audit.jsonl")

	logger, err := NewAuditLogger(&AuditLoggerConfig{
		Path:     logPath,
		MaxBytes: DefaultMaxBytes,
	})
	if err != nil {
		t.Fatalf("NewAuditLogger() error = %v", err)
	}

	// Concurrent logging from multiple goroutines
	var wg sync.WaitGroup
	numGoroutines := 10
	numEntries := 100

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(agentNum int) {
			defer wg.Done()
			for j := 0; j < numEntries; j++ {
				err := logger.LogBlocked(
					"rm -rf /important",
					"agent-"+string(rune('0'+agentNum)),
					"my-project",
					"rm_recursive_dangerous",
					"Blocked",
				)
				if err != nil {
					t.Errorf("LogBlocked() error: %v", err)
				}
			}
		}(i)
	}

	wg.Wait()

	if err := logger.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	// Count entries in the log file
	file, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("Failed to open log file: %v", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	count := 0
	for scanner.Scan() {
		var entry AuditEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("Failed to unmarshal entry %d: %v", count, err)
		}
		count++
	}

	expected := numGoroutines * numEntries
	if count != expected {
		t.Errorf("Entry count = %d, want %d", count, expected)
	}
}

func TestAuditLogger_ClosedLogger(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "test-dcg-audit.jsonl")

	logger, err := NewAuditLogger(&AuditLoggerConfig{
		Path:     logPath,
		MaxBytes: DefaultMaxBytes,
	})
	if err != nil {
		t.Fatalf("NewAuditLogger() error = %v", err)
	}

	// Close the logger
	if err := logger.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	// Try to log after close
	err = logger.LogBlocked("test", "pane", "session", "rule", "output")
	if err == nil {
		t.Error("LogBlocked() should return error after Close()")
	}

	// Close again should be safe
	if err := logger.Close(); err != nil {
		t.Errorf("Second Close() error = %v", err)
	}
}

func TestDefaultAuditLoggerConfig(t *testing.T) {
	config := DefaultAuditLoggerConfig()

	if config.Path == "" {
		t.Error("Default path is empty")
	}
	if config.MaxBytes != DefaultMaxBytes {
		t.Errorf("MaxBytes = %d, want %d", config.MaxBytes, DefaultMaxBytes)
	}
}
