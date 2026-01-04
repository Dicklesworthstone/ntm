package policy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultBlockedLogSubPath is the default subdirectory for blocked command logs.
// This is relative to the user's home directory.
const DefaultBlockedLogSubPath = ".ntm/logs/blocked.jsonl"

// BlockedEntry represents a single blocked command log entry.
type BlockedEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Session   string    `json:"session,omitempty"`
	Agent     string    `json:"agent,omitempty"`
	Command   string    `json:"command"`
	Pattern   string    `json:"pattern"`
	Reason    string    `json:"reason"`
	Action    Action    `json:"action"` // block or approve (for logged approvals)
}

// BlockedLogger writes blocked command events to a JSONL file.
type BlockedLogger struct {
	path string
	mu   sync.Mutex
	file *os.File
}

// defaultBlockedLogPath returns the default blocked log path in the user's home directory.
func defaultBlockedLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		// Fallback to current directory if home is unavailable
		return DefaultBlockedLogSubPath
	}
	return filepath.Join(home, DefaultBlockedLogSubPath)
}

// NewBlockedLogger creates a new blocked command logger.
func NewBlockedLogger(path string) (*BlockedLogger, error) {
	if path == "" {
		path = defaultBlockedLogPath()
	}

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating log directory: %w", err)
	}

	// Open file for appending
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("opening log file: %w", err)
	}

	return &BlockedLogger{
		path: path,
		file: f,
	}, nil
}

// Log writes a blocked command entry to the log.
func (l *BlockedLogger) Log(entry *BlockedEntry) error {
	if l.file == nil {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshaling entry: %w", err)
	}

	if _, err := l.file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("writing entry: %w", err)
	}

	return nil
}

// LogBlocked is a convenience method to log a blocked command.
func (l *BlockedLogger) LogBlocked(session, agent, command, pattern, reason string) error {
	return l.Log(&BlockedEntry{
		Timestamp: time.Now(),
		Session:   session,
		Agent:     agent,
		Command:   command,
		Pattern:   pattern,
		Reason:    reason,
		Action:    ActionBlock,
	})
}

// Close closes the log file.
func (l *BlockedLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file != nil {
		err := l.file.Close()
		l.file = nil
		return err
	}
	return nil
}

// ReadBlockedLog reads all entries from a blocked log file.
func ReadBlockedLog(path string) ([]BlockedEntry, error) {
	if path == "" {
		path = defaultBlockedLogPath()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading log file: %w", err)
	}

	var entries []BlockedEntry
	lines := splitLines(data)
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var entry BlockedEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue // Skip malformed entries
		}
		entries = append(entries, entry)
	}

	return entries, nil
}

// splitLines splits data into lines without allocating new strings.
func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

// RecentBlocked returns blocked entries from the last n hours.
func RecentBlocked(path string, hours int) ([]BlockedEntry, error) {
	all, err := ReadBlockedLog(path)
	if err != nil {
		return nil, err
	}

	cutoff := time.Now().Add(-time.Duration(hours) * time.Hour)
	var recent []BlockedEntry
	for _, e := range all {
		if e.Timestamp.After(cutoff) {
			recent = append(recent, e)
		}
	}
	return recent, nil
}
