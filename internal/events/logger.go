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

// Close closes the log file.
func (l *Logger) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		l.rotationWg.Wait()
		return nil
	}
	l.closed = true
	l.mu.Unlock()

	l.rotationWg.Wait()

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

// EmitSessionCreate logs a session creation event.
func EmitSessionCreate(session string, claudeCount, codexCount, geminiCount, cursorCount, windsurfCount, aiderCount, ollamaCount int, workDir, recipe string) {
	Emit(EventSessionCreate, session, SessionCreateData{
		ClaudeCount:   claudeCount,
		CodexCount:    codexCount,
		GeminiCount:   geminiCount,
		CursorCount:   cursorCount,
		WindsurfCount: windsurfCount,
		AiderCount:    aiderCount,
		OllamaCount:   ollamaCount,
		WorkDir:       workDir,
		Recipe:        recipe,
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
		// Set max line size for large events (10MB), start with 64KB
		scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)

		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}

			// Decrypt if encrypted
			plain, err := decryptJSONLine(line)
			if err != nil {
				slog.Warn("event replay: skipping unreadable line", "error", err)
				continue
			}

			var event Event
			if err := json.Unmarshal(plain, &event); err != nil {
				slog.Warn("event replay: skipping malformed line", "error", err)
				continue
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

	l.mu.Lock()
	path := l.path
	l.mu.Unlock()

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	fileSize := stat.Size()
	if fileSize == 0 {
		return nil, nil
	}

	// Scan backward for the last line
	const bufferSize = 4096
	buf := make([]byte, bufferSize)
	offset := fileSize

	for offset > 0 {
		readSize := int64(bufferSize)
		if offset < readSize {
			readSize = offset
		}
		offset -= readSize

		_, err := f.ReadAt(buf[:readSize], offset)
		if err != nil && err != io.EOF {
			return nil, err
		}

		for i := int(readSize) - 1; i >= 0; i-- {
			if buf[i] == '\n' {
				// Potential end of a line.
				// If this is the very last byte of file, it's just the terminator of the last line.
				if offset+int64(i) == fileSize-1 {
					continue
				}

				// Found start of the last line
				lineStart := offset + int64(i) + 1
				lineLen := fileSize - lineStart
				if lineLen > 10*1024*1024 {
					continue // Sanity check
				}

				lineBuf := make([]byte, lineLen)
				if _, err := f.ReadAt(lineBuf, lineStart); err != nil && err != io.EOF {
					continue
				}

				// Decrypt if encrypted
				plain, err := decryptJSONLine(lineBuf)
				if err != nil {
					continue
				}

				var event Event
				if err := json.Unmarshal(plain, &event); err == nil {
					return &event, nil
				}
			}
		}
	}

	// If we got here, maybe only one line and no trailing newline
	if _, err := f.Seek(0, io.SeekStart); err == nil {
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
		if scanner.Scan() {
			plain, err := decryptJSONLine(scanner.Bytes())
			if err == nil {
				var event Event
				if err := json.Unmarshal(plain, &event); err == nil {
					return &event, nil
				}
			}
		}
	}

	return nil, nil
}
