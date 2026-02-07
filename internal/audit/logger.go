package audit

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/privacy"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
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

var (
	redactionMu     sync.RWMutex
	redactionCfg    redaction.Config
	redactionCfgSet bool

	loggerMu    sync.Mutex
	loggerCache = map[string]*AuditLogger{}
)

// SetRedactionConfig sets the redaction config for audit payloads.
// Audit logs always redact when redaction is enabled.
func SetRedactionConfig(cfg *redaction.Config) {
	redactionMu.Lock()
	defer redactionMu.Unlock()
	if cfg == nil {
		redactionCfgSet = false
		redactionCfg = redaction.Config{}
		return
	}
	redactionCfg = *cfg
	redactionCfgSet = true
}

// NewCorrelationID returns a unique correlation ID for command tracing.
func NewCorrelationID() string {
	ms := time.Now().UnixMilli()
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("cmd-%d-%x", ms, b)
}

// LogEvent writes an audit event. It is safe to ignore returned errors.
func LogEvent(session string, eventType EventType, actor Actor, target string, payload, metadata map[string]interface{}) error {
	if shouldSkipAudit(session) {
		return nil
	}

	logger, err := getLoggerForSession(session)
	if err != nil {
		return err
	}

	entry := AuditEntry{
		EventType: eventType,
		Actor:     actor,
		Target:    target,
		Payload:   sanitizeMap(payload),
		Metadata:  sanitizeMap(metadata),
	}

	return logger.Log(entry)
}

// CloseAll closes any cached audit loggers.
func CloseAll() error {
	loggerMu.Lock()
	defer loggerMu.Unlock()
	var firstErr error
	for key, logger := range loggerCache {
		if logger == nil {
			delete(loggerCache, key)
			continue
		}
		if err := logger.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(loggerCache, key)
	}
	return firstErr
}

func shouldSkipAudit(session string) bool {
	if os.Getenv("NTM_TEST_MODE") != "" || os.Getenv("NTM_E2E") != "" {
		return true
	}
	if strings.HasSuffix(os.Args[0], ".test") {
		return true
	}
	if session != "" {
		if err := privacy.GetDefaultManager().CanPersist(session, privacy.OpEventLog); err != nil {
			return true
		}
	}
	return false
}

func getLoggerForSession(session string) (*AuditLogger, error) {
	sessionID := strings.TrimSpace(session)
	if sessionID == "" {
		sessionID = "global"
	}

	loggerMu.Lock()
	defer loggerMu.Unlock()

	if logger := loggerCache[sessionID]; logger != nil {
		return logger, nil
	}

	cfg := &LoggerConfig{
		SessionID:     sessionID,
		BufferSize:    1,
		FlushInterval: 2 * time.Second,
	}
	logger, err := NewAuditLogger(cfg)
	if err != nil {
		return nil, err
	}
	loggerCache[sessionID] = logger
	return logger, nil
}

func sanitizeMap(input map[string]interface{}) map[string]interface{} {
	if input == nil {
		return nil
	}
	out := make(map[string]interface{}, len(input))
	for k, v := range input {
		out[k] = sanitizeValue(v)
	}
	return out
}

func sanitizeValue(value interface{}) interface{} {
	switch v := value.(type) {
	case string:
		return redactString(v)
	case []string:
		out := make([]string, len(v))
		for i, item := range v {
			out[i] = redactString(item)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, item := range v {
			out[i] = sanitizeValue(item)
		}
		return out
	case map[string]interface{}:
		return sanitizeMap(v)
	default:
		return value
	}
}

func redactString(value string) string {
	cfg := getRedactionConfig()
	if cfg.Mode == redaction.ModeOff {
		return value
	}
	cfg.Mode = redaction.ModeRedact
	result := redaction.ScanAndRedact(value, cfg)
	return result.Output
}

func getRedactionConfig() redaction.Config {
	redactionMu.RLock()
	defer redactionMu.RUnlock()
	if redactionCfgSet {
		return redactionCfg
	}
	return redaction.Config{Mode: redaction.ModeRedact}
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
	file, err := os.OpenFile(filepath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
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

	// Read the last entry efficiently
	lastEntry, err := al.readLastEntry(info.Size())
	if err != nil {
		return fmt.Errorf("failed to read last entry: %w", err)
	}

	if lastEntry != nil && lastEntry.Checksum != "" {
		al.lastHash = lastEntry.Checksum
		al.sequenceNum = lastEntry.SequenceNum
	}

	return nil
}

// readLastEntry reads the last valid JSON entry from the file by scanning backwards
func (al *AuditLogger) readLastEntry(fileSize int64) (*AuditEntry, error) {
	// We need to read the file, but our main file handle is write-only
	readFile, err := os.Open(al.file.Name())
	if err != nil {
		return nil, fmt.Errorf("failed to open audit log for reading: %w", err)
	}
	defer readFile.Close()

	// Scan backwards for newlines
	const bufferSize = 4096
	buf := make([]byte, bufferSize)
	offset := fileSize

	// We need to find the last line that contains valid JSON.
	// We'll scan backward looking for newlines, and try to parse the content.

	// Start from end
	for offset > 0 {
		readSize := int64(bufferSize)
		if offset < readSize {
			readSize = offset
		}
		offset -= readSize

		_, err := readFile.ReadAt(buf[:readSize], offset)
		if err != nil && err != io.EOF {
			return nil, err
		}

		// Scan from end of buffer to find newlines
		for i := int(readSize) - 1; i >= 0; i-- {
			if buf[i] == '\n' {
				// Potential end of a line.
				// If this is the very last byte of file, it's just the terminator of the last line.
				if offset+int64(i) == fileSize-1 {
					continue
				}

				// Found a newline. The line starts after this newline (or at file start).
				lineStart := offset + int64(i) + 1

				// Read this line
				if _, err := readFile.Seek(lineStart, io.SeekStart); err != nil {
					return nil, err
				}

				scanner := bufio.NewScanner(readFile)
				if scanner.Scan() {
					line := scanner.Text()
					var entry AuditEntry
					if err := json.Unmarshal([]byte(line), &entry); err == nil {
						return &entry, nil
					}
					// If invalid JSON, keep searching backwards (skip corrupted tail)
				}
			}
		}
	}

	// If we reached start of file, try reading the first line
	if _, err := readFile.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(readFile)
	if scanner.Scan() {
		line := scanner.Text()
		var entry AuditEntry
		if err := json.Unmarshal([]byte(line), &entry); err == nil {
			return &entry, nil
		}
	}

	return nil, nil // No valid entry found
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
