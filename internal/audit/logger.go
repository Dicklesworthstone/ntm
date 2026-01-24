package audit

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// EventType represents the type of audit event
type EventType string

const (
	EventTypeCommand     EventType = "command"
	EventTypeSpawn       EventType = "spawn"
	EventTypeSend        EventType = "send"
	EventTypeResponse    EventType = "response"
	EventTypeError       EventType = "error"
	EventTypeStateChange EventType = "state_change"
)

// Actor represents who performed the action
type Actor string

const (
	ActorUser   Actor = "user"
	ActorAgent  Actor = "agent"
	ActorSystem Actor = "system"
)

// AuditEntry represents a single audit log entry
type AuditEntry struct {
	Timestamp   time.Time              `json:"timestamp"`
	SessionID   string                 `json:"session_id"`
	EventType   EventType              `json:"event_type"`
	Actor       Actor                  `json:"actor"`
	Target      string                 `json:"target"`
	Payload     map[string]interface{} `json:"payload"`
	Metadata    map[string]interface{} `json:"metadata"`
	PrevHash    string                 `json:"prev_hash,omitempty"`
	Checksum    string                 `json:"checksum"`
	SequenceNum uint64                 `json:"sequence_num"`
}

// AuditLogger provides append-only audit logging with tamper evidence
type AuditLogger struct {
	sessionID     string
	file          *os.File
	writer        *bufio.Writer
	mutex         sync.Mutex
	lastHash      string
	sequenceNum   uint64
	bufferSize    int
	flushInterval time.Duration
	closed        bool
	flushTimer    *time.Timer

	// Buffering settings
	entriesWritten int
	lastFlush      time.Time
}

// LoggerConfig holds configuration for the audit logger
type LoggerConfig struct {
	SessionID     string
	BufferSize    int           // Number of entries to buffer before flush
	FlushInterval time.Duration // Maximum time between flushes
}

// DefaultConfig returns a sensible default configuration
func DefaultConfig(sessionID string) *LoggerConfig {
	return &LoggerConfig{
		SessionID:     sessionID,
		BufferSize:    10,              // Flush every 10 entries
		FlushInterval: 5 * time.Second, // Or every 5 seconds
	}
}

// NewAuditLogger creates a new audit logger for the specified session
func NewAuditLogger(config *LoggerConfig) (*AuditLogger, error) {
	if config == nil {
		config = DefaultConfig("")
	}

	// Create audit log directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	auditDir := filepath.Join(homeDir, ".local", "share", "ntm", "audit")
	if err := os.MkdirAll(auditDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create audit directory: %w", err)
	}

	// Create log file with session and date
	now := time.Now()
	filename := fmt.Sprintf("%s-%s.jsonl", config.SessionID, now.Format("2006-01-02"))
	filepath := filepath.Join(auditDir, filename)

	// Open file in append mode with exclusive locking intent
	file, err := os.OpenFile(filepath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open audit log file: %w", err)
	}

	logger := &AuditLogger{
		sessionID:     config.SessionID,
		file:          file,
		writer:        bufio.NewWriter(file),
		bufferSize:    config.BufferSize,
		flushInterval: config.FlushInterval,
		lastFlush:     time.Now(),
	}

	// Load the last hash from the file if it exists
	if err := logger.loadLastHash(); err != nil {
		file.Close()
		return nil, fmt.Errorf("failed to load last hash: %w", err)
	}

	// Start flush timer
	logger.startFlushTimer()

	return logger, nil
}

// loadLastHash reads the last entry from the file to get the previous hash
func (al *AuditLogger) loadLastHash() error {
	// Get file info to check if file has content
	info, err := al.file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat audit log file: %w", err)
	}

	// If file is empty, nothing to load
	if info.Size() == 0 {
		return nil
	}

	// We need to read the file, but our main file handle is write-only
	// Open a separate read handle to the same file
	readFile, err := os.Open(al.file.Name())
	if err != nil {
		return fmt.Errorf("failed to open audit log for reading: %w", err)
	}
	defer readFile.Close()

	scanner := bufio.NewScanner(readFile)
	var lastEntry AuditEntry

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var entry AuditEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			// Corrupted entry - we'll continue but log this
			continue
		}

		lastEntry = entry
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading existing audit log: %w", err)
	}

	// Initialize state based on last entry
	if lastEntry.Checksum != "" {
		al.lastHash = lastEntry.Checksum
		al.sequenceNum = lastEntry.SequenceNum
	}

	return nil
}

// startFlushTimer starts the periodic flush timer
func (al *AuditLogger) startFlushTimer() {
	al.flushTimer = time.AfterFunc(al.flushInterval, func() {
		al.mutex.Lock()
		defer al.mutex.Unlock()
		if !al.closed {
			_ = al.flushUnlocked() // Ignore error in background flush
			al.startFlushTimer()   // Restart timer
		}
	})
}

// Log writes an audit entry to the log
func (al *AuditLogger) Log(entry AuditEntry) error {
	al.mutex.Lock()
	defer al.mutex.Unlock()

	if al.closed {
		return fmt.Errorf("audit logger is closed")
	}

	// Fill in missing fields
	entry.Timestamp = time.Now().UTC()
	entry.SessionID = al.sessionID
	entry.PrevHash = al.lastHash
	al.sequenceNum++
	entry.SequenceNum = al.sequenceNum

	// Calculate hash without the checksum field
	entryForHash := entry
	entryForHash.Checksum = ""
	hashData, err := json.Marshal(entryForHash)
	if err != nil {
		return fmt.Errorf("failed to marshal entry for hashing: %w", err)
	}

	hash := sha256.Sum256(hashData)
	entry.Checksum = hex.EncodeToString(hash[:])

	// Re-marshal with checksum
	entryData, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to marshal final audit entry: %w", err)
	}

	// Write to buffer
	if _, err := al.writer.Write(entryData); err != nil {
		return fmt.Errorf("failed to write audit entry: %w", err)
	}
	if _, err := al.writer.WriteString("\n"); err != nil {
		return fmt.Errorf("failed to write newline: %w", err)
	}

	// Update state
	al.lastHash = entry.Checksum
	al.entriesWritten++

	// Flush if buffer is full
	if al.entriesWritten >= al.bufferSize {
		if err := al.flushUnlocked(); err != nil {
			return fmt.Errorf("failed to flush buffer: %w", err)
		}
	}

	return nil
}

// flushUnlocked flushes the buffer (caller must hold mutex)
func (al *AuditLogger) flushUnlocked() error {
	if err := al.writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush buffer: %w", err)
	}
	if err := al.file.Sync(); err != nil {
		return fmt.Errorf("failed to sync file: %w", err)
	}
	al.entriesWritten = 0
	al.lastFlush = time.Now()
	return nil
}

// Flush manually flushes any buffered entries to disk
func (al *AuditLogger) Flush() error {
	al.mutex.Lock()
	defer al.mutex.Unlock()
	return al.flushUnlocked()
}

// Close flushes any remaining entries and closes the audit log
func (al *AuditLogger) Close() error {
	al.mutex.Lock()
	defer al.mutex.Unlock()

	if al.closed {
		return nil
	}

	al.closed = true

	// Stop flush timer
	if al.flushTimer != nil {
		al.flushTimer.Stop()
	}

	// Flush remaining entries
	if err := al.flushUnlocked(); err != nil {
		// Still close the file even if flush fails
		al.file.Close()
		return fmt.Errorf("failed to flush before close: %w", err)
	}

	// Close file
	if err := al.file.Close(); err != nil {
		return fmt.Errorf("failed to close audit log file: %w", err)
	}

	return nil
}

// VerifyIntegrity verifies the integrity of the audit log
func VerifyIntegrity(logPath string) error {
	file, err := os.Open(logPath)
	if err != nil {
		return fmt.Errorf("failed to open audit log: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var prevHash string
	var sequenceNum uint64

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var entry AuditEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return fmt.Errorf("invalid JSON in audit log: %w", err)
		}

		// Verify sequence number
		sequenceNum++
		if entry.SequenceNum != sequenceNum {
			return fmt.Errorf("sequence number mismatch: expected %d, got %d", sequenceNum, entry.SequenceNum)
		}

		// Verify previous hash
		if entry.PrevHash != prevHash {
			return fmt.Errorf("hash chain broken at sequence %d", sequenceNum)
		}

		// Verify checksum
		entryForHash := entry
		entryForHash.Checksum = ""
		hashData, err := json.Marshal(entryForHash)
		if err != nil {
			return fmt.Errorf("failed to marshal entry for verification: %w", err)
		}

		hash := sha256.Sum256(hashData)
		expectedChecksum := hex.EncodeToString(hash[:])

		if entry.Checksum != expectedChecksum {
			return fmt.Errorf("checksum mismatch at sequence %d", sequenceNum)
		}

		prevHash = entry.Checksum
	}

	return scanner.Err()
}
