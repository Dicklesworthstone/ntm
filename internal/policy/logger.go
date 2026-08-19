package policy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// defaultBlockedLogPath returns the default blocked log path in the user's home directory.
func defaultBlockedLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		// Fallback to current directory if home is unavailable
		return DefaultBlockedLogSubPath
	}
	return filepath.Join(home, DefaultBlockedLogSubPath)
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
