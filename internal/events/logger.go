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
	rotationMu    sync.Mutex // Serializes snapshot/filter/commit cycles.
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
	// Include room for the newline in the scanner's buffer limit. Reject the
	// final stored representation, since encryption can increase its size.
	if len(data) >= maxEventLineBytes {
		return fmt.Errorf("event exceeds log line limit of %d bytes", maxEventLineBytes-1)
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

// rotateOldEntries filters a bounded snapshot without moving the active log.
// Writes through this Logger continue during filtering and are merged before
// replacement. Any pre-commit failure leaves both the log and its writer intact.
func (l *Logger) rotateOldEntries() error {
	l.rotationMu.Lock()
	defer l.rotationMu.Unlock()

	l.mu.Lock()
	if l.closed || !l.enabled || l.file == nil {
		l.mu.Unlock()
		return nil
	}
	activeInfo, err := l.file.Stat()
	if err != nil {
		l.mu.Unlock()
		return fmt.Errorf("stat active log: %w", err)
	}
	srcFile, err := os.Open(l.path)
	if err != nil {
		l.mu.Unlock()
		return fmt.Errorf("opening log snapshot: %w", err)
	}
	defer srcFile.Close()
	snapshot, err := srcFile.Stat()
	l.mu.Unlock()
	if err != nil {
		return fmt.Errorf("stat log snapshot: %w", err)
	}
	if !snapshot.Mode().IsRegular() || !os.SameFile(activeInfo, snapshot) {
		return fmt.Errorf("active log changed before rotation")
	}

	// A private, uniquely named staging file cannot truncate another rotation
	// or a recovery artifact left by an earlier version of the logger.
	tmpFile, err := os.CreateTemp(filepath.Dir(l.path), "."+filepath.Base(l.path)+".rotate-*")
	if err != nil {
		return fmt.Errorf("creating rotation file: %w", err)
	}
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
	}()

	cutoff := time.Now().AddDate(0, 0, -l.retentionDays)
	if err := filterRetainedEvents(io.NewSectionReader(srcFile, 0, snapshot.Size()), tmpFile, cutoff); err != nil {
		return err
	}
	return l.commitRotation(srcFile, snapshot, tmpFile)
}

// filterRetainedEvents expires only records with a known, old timestamp.
// Malformed or undecryptable records must not silently disappear on rotation.
func filterRetainedEvents(src io.Reader, dst io.Writer, cutoff time.Time) error {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 64*1024), maxEventLineBytes)
	writer := bufio.NewWriter(dst)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		plain, decErr := decryptJSONLine(line)
		var event Event
		if decErr == nil && json.Unmarshal(plain, &event) == nil &&
			!event.Timestamp.IsZero() && !event.Timestamp.After(cutoff) {
			continue
		}
		if _, err := writer.Write(line); err != nil {
			return fmt.Errorf("writing retained event: %w", err)
		}
		if err := writer.WriteByte('\n'); err != nil {
			return fmt.Errorf("writing retained event newline: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanning log snapshot: %w", err)
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flushing retained events: %w", err)
	}
	return nil
}

// commitRotation merges writes accepted after the snapshot, then replaces the
// file only after every read, write, sync, and replacement-writer open succeeds.
func (l *Logger) commitRotation(src *os.File, snapshot os.FileInfo, tmp *os.File) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || !l.enabled || l.file == nil {
		return nil
	}

	activeInfo, err := l.file.Stat()
	if err != nil {
		return fmt.Errorf("stat active log before rotation commit: %w", err)
	}
	pathInfo, err := os.Stat(l.path)
	if err != nil {
		return fmt.Errorf("stat log path before rotation commit: %w", err)
	}
	if !os.SameFile(snapshot, activeInfo) || !os.SameFile(snapshot, pathInfo) || activeInfo.Size() < snapshot.Size() {
		return fmt.Errorf("active log changed during rotation")
	}
	added := activeInfo.Size() - snapshot.Size()
	if _, err := io.CopyN(tmp, io.NewSectionReader(src, snapshot.Size(), added), added); err != nil {
		return fmt.Errorf("merging new log events: %w", err)
	}
	if err := tmp.Chmod(activeInfo.Mode().Perm()); err != nil {
		return fmt.Errorf("preserving log permissions: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("syncing rotated log: %w", err)
	}
	writer, err := os.OpenFile(tmp.Name(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("opening rotated log writer: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = writer.Close()
		return fmt.Errorf("closing rotation file: %w", err)
	}
	if err := os.Rename(tmp.Name(), l.path); err != nil {
		_ = writer.Close()
		return fmt.Errorf("replacing active log: %w", err)
	}
	previous := l.file
	l.file = writer
	if err := previous.Close(); err != nil {
		return fmt.Errorf("closing previous log after rotation: %w", err)
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
