// Package dcg provides integration with the Destructive Command Guard (DCG) tool,
// including audit logging for blocked commands.
package dcg

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultMaxBytes is the default maximum log file size before rotation (10MB)
const DefaultMaxBytes = 10 * 1024 * 1024

// AuditEntry represents a single DCG audit log entry
type AuditEntry struct {
	Timestamp string `json:"timestamp"`
	Event     string `json:"event"`
	Command   string `json:"command"`
	Pane      string `json:"pane"`
	Session   string `json:"session"`
	Rule      string `json:"rule"`
	DCGOutput string `json:"dcg_output"`
}

// AuditLogger provides logging for DCG blocked commands with automatic rotation
type AuditLogger struct {
	path     string
	file     *os.File
	writer   *bufio.Writer
	mu       sync.Mutex
	maxBytes int64
	closed   bool
}

// AuditLoggerConfig holds configuration for the DCG audit logger
type AuditLoggerConfig struct {
	Path     string
	MaxBytes int64
}

// DefaultAuditLoggerConfig returns sensible defaults for the audit logger
func DefaultAuditLoggerConfig() *AuditLoggerConfig {
	homeDir, _ := os.UserHomeDir()
	return &AuditLoggerConfig{
		Path:     filepath.Join(homeDir, ".local", "share", "ntm", "dcg-audit.jsonl"),
		MaxBytes: DefaultMaxBytes,
	}
}

// NewAuditLogger creates a new DCG audit logger
func NewAuditLogger(config *AuditLoggerConfig) (*AuditLogger, error) {
	if config == nil {
		config = DefaultAuditLoggerConfig()
	}

	if config.Path == "" {
		config.Path = DefaultAuditLoggerConfig().Path
	}

	if config.MaxBytes <= 0 {
		config.MaxBytes = DefaultMaxBytes
	}

	// Create parent directory if needed
	dir := filepath.Dir(config.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create audit log directory: %w", err)
	}

	// Open file in append mode
	file, err := os.OpenFile(config.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open audit log file: %w", err)
	}

	return &AuditLogger{
		path:     config.Path,
		file:     file,
		writer:   bufio.NewWriter(file),
		maxBytes: config.MaxBytes,
	}, nil
}

// LogBlocked logs a blocked command event
func (l *AuditLogger) LogBlocked(command, pane, session, rule, dcgOutput string) error {
	entry := AuditEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Event:     "command_blocked",
		Command:   command,
		Pane:      pane,
		Session:   session,
		Rule:      rule,
		DCGOutput: dcgOutput,
	}

	return l.writeEntry(entry)
}

// LogChecked logs a completed check-cycle event. Exactly one entry is written
// per command run through a DCG check, regardless of outcome, so consumers
// (e.g. ntm --robot-dcg-status) can report a real commands-checked counter.
func (l *AuditLogger) LogChecked(command, pane, session, rule, dcgOutput string) error {
	entry := AuditEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Event:     "command_checked",
		Command:   command,
		Pane:      pane,
		Session:   session,
		Rule:      rule,
		DCGOutput: dcgOutput,
	}

	return l.writeEntry(entry)
}

// writeEntry marshals and writes an entry to the log
func (l *AuditLogger) writeEntry(entry AuditEntry) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return fmt.Errorf("audit logger is closed")
	}

	// Check if rotation is needed
	if err := l.checkRotation(); err != nil {
		return fmt.Errorf("rotation check failed: %w", err)
	}

	// Marshal entry
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to marshal audit entry: %w", err)
	}

	// Write entry
	if _, err := l.writer.Write(data); err != nil {
		return fmt.Errorf("failed to write audit entry: %w", err)
	}
	if _, err := l.writer.WriteString("\n"); err != nil {
		return fmt.Errorf("failed to write newline: %w", err)
	}

	// Flush immediately for audit entries (important for durability)
	if err := l.writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush buffer: %w", err)
	}
	if err := l.file.Sync(); err != nil {
		return fmt.Errorf("failed to sync file: %w", err)
	}

	return nil
}

// checkRotation checks if the log file exceeds maxBytes and rotates if needed
// Caller must hold the mutex
func (l *AuditLogger) checkRotation() error {
	info, err := l.file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat log file: %w", err)
	}

	if info.Size() < l.maxBytes {
		return nil
	}

	// Flush any buffered data before rotation
	if err := l.writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush before rotation: %w", err)
	}

	// Close current file
	if err := l.file.Close(); err != nil {
		return fmt.Errorf("failed to close file for rotation: %w", err)
	}

	// Rename with timestamp
	timestamp := time.Now().UTC().Format("20060102-150405")
	ext := filepath.Ext(l.path)
	base := l.path[:len(l.path)-len(ext)]
	rotatedPath := fmt.Sprintf("%s-%s%s", base, timestamp, ext)

	if err := os.Rename(l.path, rotatedPath); err != nil {
		return fmt.Errorf("failed to rotate log file: %w", err)
	}

	// Open new file
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open new log file after rotation: %w", err)
	}

	l.file = file
	l.writer = bufio.NewWriter(file)

	return nil
}

// Close flushes and closes the audit logger
func (l *AuditLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return nil
	}

	l.closed = true

	// Flush any remaining data
	if err := l.writer.Flush(); err != nil {
		l.file.Close()
		return fmt.Errorf("failed to flush before close: %w", err)
	}

	// Close file
	if err := l.file.Close(); err != nil {
		return fmt.Errorf("failed to close audit log file: %w", err)
	}

	return nil
}
