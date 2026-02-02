package events

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/privacy"
)

const (
	// DefaultLogPath is the default location for the events log.
	DefaultLogPath = "~/.config/ntm/analytics/events.jsonl"

	// DefaultRetentionDays is the number of days to retain log entries.
	DefaultRetentionDays = 30

	// RotationCheckInterval is how often to check for rotation (in events).
	RotationCheckInterval = 100
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
		Path:          expandPath(DefaultLogPath),
		RetentionDays: DefaultRetentionDays,
		Enabled:       true,
	}
}

// NewLogger creates a new event logger.
func NewLogger(opts LoggerOptions) (*Logger, error) {
	if opts.Path == "" {
		opts.Path = expandPath(DefaultLogPath)
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
func (l *Logger) Log(event *Event) error {
	if !l.enabled || l.file == nil {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Serialize event to JSON
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshaling event: %w", err)
	}

	// Write to file with newline
	if _, err := l.file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("writing event: %w", err)
	}

	l.eventCount++

	// Check for rotation periodically
	if l.eventCount%RotationCheckInterval == 0 {
		go l.maybeRotate()
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

// Close closes the log file.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file != nil {
		err := l.file.Close()
		l.file = nil
		return err
	}
	return nil
}

// maybeRotate checks if rotation is needed and performs it.
func (l *Logger) maybeRotate() {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Only rotate once per day at most (check under lock to avoid TOCTOU)
	if time.Since(l.lastRotation) < 24*time.Hour {
		return
	}

	l.lastRotation = time.Now()

	// Perform rotation
	if err := l.rotateOldEntries(); err != nil {
		// Log rotation errors but don't fail
		slog.Warn("event log rotation error", "error", err)
	}
}

// rotateOldEntries removes entries older than retention period using streaming.
func (l *Logger) rotateOldEntries() error {
	// Create temp file for filtered logs
	tmpFile, err := os.CreateTemp(filepath.Dir(l.path), "events-rotate-*.jsonl")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath) // Cleanup on error

	// Open source file for reading
	// We need to read from the current path, but we have l.file open for writing.
	// It's safer to open a separate read handle.
	srcFile, err := os.Open(l.path)
	if err != nil {
		tmpFile.Close()
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("opening source file: %w", err)
	}
	defer srcFile.Close()

	cutoff := time.Now().AddDate(0, 0, -l.retentionDays)
	scanner := bufio.NewScanner(srcFile)

	// Buffer for writing to temp file
	writer := bufio.NewWriter(tmpFile)
	defer tmpFile.Close()

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event Event
		if err := json.Unmarshal(line, &event); err != nil {
			// Keep malformed entries
			if _, err := writer.Write(line); err != nil {
				tmpFile.Close()
				return err
			}
			writer.WriteByte('\n')
			continue
		}

		if event.Timestamp.After(cutoff) {
			if _, err := writer.Write(line); err != nil {
				tmpFile.Close()
				return err
			}
			writer.WriteByte('\n')
		}
	}

	if err := scanner.Err(); err != nil {
		tmpFile.Close()
		return fmt.Errorf("scanning log file: %w", err)
	}

	if err := writer.Flush(); err != nil {
		tmpFile.Close()
		return fmt.Errorf("flushing temp file: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}

	// Now swap the files
	// First close our write handle
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}

	// Rename temp to target (atomic replace)
	if err := os.Rename(tmpPath, l.path); err != nil {
		// Attempt to reopen original if rename fails
		if f, openErr := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); openErr == nil {
			l.file = f
		}
		return fmt.Errorf("renaming temp file: %w", err)
	}

	// Reopen file for appending
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("reopening log file: %w", err)
	}
	l.file = f

	return nil
}

// expandPath expands ~ in a path to the home directory.
func expandPath(path string) string {
	if len(path) > 0 && path[0] == '~' {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, path[1:])
		}
	}
	return path
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

// EmitSessionCreate logs a session creation event.
func EmitSessionCreate(session string, claudeCount, codexCount, geminiCount int, workDir, recipe string) {
	Emit(EventSessionCreate, session, SessionCreateData{
		ClaudeCount: claudeCount,
		CodexCount:  codexCount,
		GeminiCount: geminiCount,
		WorkDir:     workDir,
		Recipe:      recipe,
	})
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

// EmitError logs an error event.
func EmitError(session, errorType, message string) {
	Emit(EventError, session, ErrorData{
		ErrorType: errorType,
		Message:   message,
	})
}

// Replay reads events from the log file and sends them to a channel.
// Events are filtered to only include those after the 'since' timestamp.
// The channel is closed when all events have been sent.
// Note: Errors during reading are silently ignored since this runs in a goroutine.
// Use Since() if you need error handling.
func (l *Logger) Replay(since time.Time) (<-chan *Event, error) {
	ch := make(chan *Event, 100)

	go func() {
		defer close(ch)

		if !l.enabled {
			return
		}

		f, err := os.Open(l.path)
		if err != nil {
			// No log file or can't open - nothing to replay
			// Errors are logged for debugging
			if !os.IsNotExist(err) {
				slog.Warn("event log replay error", "error", err)
			}
			return
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}

			var event Event
			if err := json.Unmarshal(line, &event); err != nil {
				continue // Skip malformed entries
			}

			if event.Timestamp.After(since) {
				ch <- &event
			}
		}

		// Check for scanner errors (I/O errors during reading)
		if err := scanner.Err(); err != nil {
			slog.Warn("event log replay scan error", "error", err)
		}
	}()

	return ch, nil
}

// Since returns all events after a specific timestamp.
// This is a convenience method that collects all events from Replay into a slice.
func (l *Logger) Since(ts time.Time) ([]*Event, error) {
	ch, err := l.Replay(ts)
	if err != nil {
		return nil, err
	}

	var events []*Event
	for e := range ch {
		events = append(events, e)
	}
	return events, nil
}

// ReplaySession returns events for a specific session after a timestamp.
func (l *Logger) ReplaySession(session string, since time.Time) (<-chan *Event, error) {
	ch := make(chan *Event, 100)

	go func() {
		defer close(ch)

		allEvents, err := l.Replay(since)
		if err != nil {
			return
		}

		for event := range allEvents {
			if event.Session == session {
				ch <- event
			}
		}
	}()

	return ch, nil
}

// SinceByType returns events of a specific type after a timestamp.
func (l *Logger) SinceByType(eventType EventType, since time.Time) ([]*Event, error) {
	allEvents, err := l.Since(since)
	if err != nil {
		return nil, err
	}

	var filtered []*Event
	for _, e := range allEvents {
		if e.Type == eventType {
			filtered = append(filtered, e)
		}
	}
	return filtered, nil
}

// EventCount returns the count of events after a timestamp.
func (l *Logger) EventCount(since time.Time) (int, error) {
	events, err := l.Since(since)
	if err != nil {
		return 0, err
	}
	return len(events), nil
}

// LastEvent returns the most recent event, or nil if no events exist.
func (l *Logger) LastEvent() (*Event, error) {
	if !l.enabled {
		return nil, nil
	}

	f, err := os.Open(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var lastEvent *Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event Event
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		lastEvent = &event
	}

	return lastEvent, nil
}
