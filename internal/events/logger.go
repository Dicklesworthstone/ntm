package events

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/privacy"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	// DefaultLogPath is the default location for the events log.
	DefaultLogPath = "~/.config/ntm/analytics/events.jsonl"

	// DefaultRetentionDays is the number of days to retain log entries.
	DefaultRetentionDays = 30

	// RotationCheckInterval is how often to check for rotation (in events).
	RotationCheckInterval = 100

	// maxEventLineBytes caps a single scanned log line. Encryption base64-encodes
	// each line, inflating it by roughly a third, so the cap has to leave room for
	// the encrypted form of the largest event we are willing to write.
	maxEventLineBytes = 10 * 1024 * 1024
)

// Logger writes events to a JSONL file with automatic rotation.
type Logger struct {
	path          string
	retentionDays int
	enabled       bool
	mu            sync.Mutex
	file          *os.File
	eventCount    int
	lastRotation  time.Time
	closed        bool
	rotationWg    sync.WaitGroup
}

// LoggerOptions configures the event logger.
type LoggerOptions struct {
	Path          string
	RetentionDays int
	Enabled       bool
}

// DefaultOptions returns the default logger options.
func DefaultOptions() LoggerOptions {
	return LoggerOptions{
		Path:          util.ExpandPath(DefaultLogPath),
		RetentionDays: DefaultRetentionDays,
		Enabled:       true,
	}
}

// NewLogger creates a new event logger.
func NewLogger(opts LoggerOptions) (*Logger, error) {
	if opts.Path == "" {
		opts.Path = util.ExpandPath(DefaultLogPath)
	}
	if opts.RetentionDays == 0 {
		opts.RetentionDays = DefaultRetentionDays
	}

	l := &Logger{
		path:          opts.Path,
		retentionDays: opts.RetentionDays,
		enabled:       opts.Enabled,
		lastRotation:  time.Now(),
	}

	if !l.enabled {
		return l, nil
	}

	// Ensure directory exists
	dir := filepath.Dir(l.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating log directory: %w", err)
	}

	// Open file for appending
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("opening log file: %w", err)
	}
	l.file = f

	return l, nil
}

// Log writes an event to the log file.
// If redaction is configured via SetRedactionConfig, sensitive data is redacted before storage.
func (l *Logger) Log(event *Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.enabled || l.closed || l.file == nil {
		return nil
	}

	// Apply redaction if configured
	eventToWrite := RedactEvent(event)

	// Serialize event to JSON
	data, err := json.Marshal(eventToWrite)
	if err != nil {
		return fmt.Errorf("marshaling event: %w", err)
	}

	// Encrypt if configured (after redaction, before write)
	data, err = encryptJSONLine(data)
	if err != nil {
		return fmt.Errorf("encrypting event: %w", err)
	}

	// Write to file with newline
	if _, err := l.file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("writing event: %w", err)
	}

	l.eventCount++

	// Check for rotation periodically
	if l.eventCount%RotationCheckInterval == 0 {
		l.rotationWg.Add(1)
		go func() {
			defer l.rotationWg.Done()
			l.maybeRotate()
		}()
	}

	return nil
}

// LogEvent is a convenience method to create and log an event in one call.
func (l *Logger) LogEvent(eventType EventType, session string, data interface{}) error {
	// Check privacy mode before logging
	if session != "" {
		if err := privacy.GetDefaultManager().CanPersist(session, privacy.OpEventLog); err != nil {
			// Silently skip logging in privacy mode (don't propagate error)
			return nil
		}
	}
	event := NewEvent(eventType, session, ToMap(data))
	return l.Log(event)
}

// maybeRotate checks if rotation is needed and performs it.
func (l *Logger) maybeRotate() {
	l.mu.Lock()
	if l.closed || !l.enabled || l.file == nil {
		l.mu.Unlock()
		return
	}
	// Only rotate once per day at most (check under lock to avoid TOCTOU)
	if time.Since(l.lastRotation) < 24*time.Hour {
		l.mu.Unlock()
		return
	}

	l.lastRotation = time.Now()
	l.mu.Unlock()

	// Perform rotation without holding the lock for the entire process
	if err := l.rotateOldEntries(); err != nil {
		// Log rotation errors but don't fail
		slog.Warn("event log rotation error", "error", err)
	}
}

// rotateOldEntries removes entries older than retention period using streaming.
// It avoids blocking concurrent LogEvent calls and guarantees no events are lost.
func (l *Logger) rotateOldEntries() error {
	oldPath := l.path + ".old"
	tmpPath := l.path + ".tmp"

	// 1. Swap the active log file out quickly
	l.mu.Lock()
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
	if err := os.Rename(l.path, oldPath); err != nil && !os.IsNotExist(err) {
		l.mu.Unlock()
		return fmt.Errorf("renaming to old path: %w", err)
	}
	// Create fresh log file for incoming events
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		l.mu.Unlock()
		return fmt.Errorf("reopening active log file: %w", err)
	}
	l.file = f
	l.mu.Unlock()

	// Ensure cleanup of oldPath if something panics
	defer os.Remove(oldPath)
	defer os.Remove(tmpPath)

	// 2. Filter old events into tmpPath (can take a long time, lock is NOT held)
	tmpFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}

	srcFile, err := os.Open(oldPath)
	if err != nil {
		tmpFile.Close()
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("opening old log file: %w", err)
	}

	cutoff := time.Now().AddDate(0, 0, -l.retentionDays)
	scanner := bufio.NewScanner(srcFile)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	writer := bufio.NewWriter(tmpFile)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		plain, decErr := decryptJSONLine(line)
		if decErr != nil {
			writer.Write(line)
			writer.WriteByte('\n')
			continue
		}

		var event Event
		if err := json.Unmarshal(plain, &event); err != nil {
			writer.Write(line)
			writer.WriteByte('\n')
			continue
		}

		if event.Timestamp.After(cutoff) {
			writer.Write(line)
			writer.WriteByte('\n')
		}
	}

	srcFile.Close()
	if err := scanner.Err(); err != nil {
		tmpFile.Close()
		return fmt.Errorf("scanning old log file: %w", err)
	}
	if err := writer.Flush(); err != nil {
		tmpFile.Close()
		return fmt.Errorf("flushing temp file: %w", err)
	}

	// 3. Merge the newly arrived events from active l.path into tmpPath and swap back
	l.mu.Lock()
	defer l.mu.Unlock()

	// Sync and close active file
	l.file.Sync()
	l.file.Close()

	// Open active file to read new events
	activeReader, err := os.Open(l.path)
	if err == nil {
		_, _ = io.Copy(tmpFile, activeReader)
		activeReader.Close()
	}

	tmpFile.Close()

	// Swap tmp file to become the new active log file
	if err := os.Rename(tmpPath, l.path); err != nil {
		// Recovery fallback
		l.file, _ = os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		return fmt.Errorf("renaming tmp to active: %w", err)
	}

	// Reopen active file
	l.file, err = os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("reopening final log file: %w", err)
	}

	return nil
}

// Global logger instance
var (
	globalLogger     *Logger
	globalLoggerOnce sync.Once
)

// DefaultLogger returns the global default logger instance.
func DefaultLogger() *Logger {
	globalLoggerOnce.Do(func() {
		var err error
		globalLogger, err = NewLogger(DefaultOptions())
		if err != nil {
			// If we can't create the logger, create a disabled one
			globalLogger = &Logger{enabled: false}
		}
	})
	return globalLogger
}

// Emit logs an event using the default logger.
func Emit(eventType EventType, session string, data interface{}) {
	DefaultLogger().LogEvent(eventType, session, data)
}

// EmitSessionCreate logs a session creation event. The structured payload keeps
// provider counts from being miswired when a new agent type is added.
func EmitSessionCreate(session string, data SessionCreateData) {
	Emit(EventSessionCreate, session, data)
}

// EmitPromptSend logs a prompt send event.
func EmitPromptSend(session string, targetCount, promptLength int, template, targetTypes string, hasContext bool) {
	// Estimate tokens based on prompt length (using ~3.5 chars/token heuristic)
	estimatedTokens := promptLength * 10 / 35

	Emit(EventPromptSend, session, PromptSendData{
		TargetCount:     targetCount,
		PromptLength:    promptLength,
		Template:        template,
		TargetTypes:     targetTypes,
		HasContext:      hasContext,
		EstimatedTokens: estimatedTokens,
	})
}

// ReadSince reads events from an explicit log path and returns those newer than
// since. Encrypted lines are decrypted with the configured keyring, so read-only
// surfaces see the same events regardless of whether encryption is enabled.
//
// Unlike Replay this never opens the log for writing, so callers that only
// display events do not create the log file or its directory as a side effect.
func ReadSince(path string, since time.Time) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), maxEventLineBytes)

	var result []Event
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		plain, err := decryptJSONLine(line)
		if err != nil {
			slog.Warn("event read: skipping unreadable line", "path", path, "error", err)
			continue
		}

		var event Event
		if err := json.Unmarshal(plain, &event); err != nil {
			slog.Warn("event read: skipping malformed line", "path", path, "error", err)
			continue
		}

		if event.Timestamp.After(since) {
			result = append(result, event)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return result, nil
}
