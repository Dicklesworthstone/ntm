package history

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	historyFileName   = "history.jsonl"
	defaultMaxEntries = 10000
)

var (
	// ErrNoHistory is returned when history file doesn't exist
	ErrNoHistory = errors.New("no history file found")

	// internal mutex for goroutine safety
	localMu sync.Mutex
)

// StoragePath returns the path to the history file.
// Uses XDG_DATA_HOME if set, otherwise ~/.local/share/ntm/history.jsonl
func StoragePath() string {
	dataDir := os.Getenv("XDG_DATA_HOME")
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return historyFileName // fallback to current dir
		}
		dataDir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataDir, "ntm", historyFileName)
}

// acquireLock is implemented in platform-specific files:
// - lock_unix.go for Unix systems (with flock)
// - lock_windows.go for Windows (mutex only)

// Append adds an entry to the history file.
// Thread-safe and process-safe.
func Append(entry *HistoryEntry) error {
	unlock, err := acquireLock()
	if err != nil {
		return err
	}
	defer unlock()

	path := StoragePath()

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	// Write line with newline atomically
	_, err = f.Write(append(data, '\n'))
	return err
}

// BatchAppend adds multiple entries to the history file atomically.
func BatchAppend(entries []*HistoryEntry) error {
	if len(entries) == 0 {
		return nil
	}

	unlock, err := acquireLock()
	if err != nil {
		return err
	}
	defer unlock()

	path := StoragePath()

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()

	writer := bufio.NewWriter(f)
	for _, entry := range entries {
		data, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		if _, err := writer.Write(data); err != nil {
			return err
		}
		if err := writer.WriteByte('\n'); err != nil {
			return err
		}
	}

	return writer.Flush()
}

// ReadAll reads all history entries from the file.
// Returns empty slice if file doesn't exist.
func ReadAll() ([]HistoryEntry, error) {
	unlock, err := acquireLock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	return readAllLocked()
}

// readAllLocked reads all entries (caller must hold lock)
func readAllLocked() ([]HistoryEntry, error) {
	path := StoragePath()

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []HistoryEntry{}, nil
		}
		return nil, err
	}
	defer f.Close()

	var entries []HistoryEntry
	scanner := bufio.NewScanner(f)
	// Set max line size for large prompts (5MB), start with 64KB
	scanner.Buffer(make([]byte, 64*1024), 5*1024*1024)

	for scanner.Scan() {
		var entry HistoryEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			// Skip malformed lines
			continue
		}
		entries = append(entries, entry)
	}

	if err := scanner.Err(); err != nil {
		return entries, err
	}

	return entries, nil
}

// ReadRecent reads the last n entries efficiently by scanning backwards.
func ReadRecent(n int) ([]HistoryEntry, error) {
	unlock, err := acquireLock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	if n <= 0 {
		return []HistoryEntry{}, nil
	}

	path := StoragePath()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []HistoryEntry{}, nil
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
		return []HistoryEntry{}, nil
	}

	// Scan backwards for newlines
	const bufferSize = 4096
	buf := make([]byte, bufferSize)
	offset := fileSize
	newlinesFound := 0

	// If the file ends with a newline, we start counting from the one before it.
	// But JSONL usually implies "line separated", so effectively we count line separators.
	// Let's just count backward. If we find N+1 newlines, the read starts after the (N+1)th newline.
	// If we don't find N+1 newlines, we read from start.

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
				// Ignore newline at the very end of file
				if offset+int64(i) == fileSize-1 {
					continue
				}
				newlinesFound++
				if newlinesFound >= n {
					// Found start of the Nth line (from end)
					offset += int64(i) + 1
					goto ReadEntries
				}
			}
		}
	}
	// If we got here, we didn't find enough newlines, so we read from start
	offset = 0

ReadEntries:
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}

	var entries []HistoryEntry
	scanner := bufio.NewScanner(f)
	// Set max line size for large prompts (5MB), start with 64KB
	scanner.Buffer(make([]byte, 64*1024), 5*1024*1024)

	for scanner.Scan() {
		var entry HistoryEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// We might have read slightly more if we landed in the middle of a line (unlikely with this logic)
	// or if the file grew (we hold lock though).
	// But scanner reads line by line.
	// Just return what we got. If we got more than n, slice it.
	if len(entries) > n {
		entries = entries[len(entries)-n:]
	}

	return entries, nil
}

// ReadForSession reads entries for a specific session.
func ReadForSession(session string) ([]HistoryEntry, error) {
	entries, err := ReadAll()
	if err != nil {
		return nil, err
	}

	var result []HistoryEntry
	for _, e := range entries {
		if e.Session == session {
			result = append(result, e)
		}
	}
	return result, nil
}

// Count returns the number of entries in history.
func Count() (int, error) {
	unlock, err := acquireLock()
	if err != nil {
		return 0, err
	}
	defer unlock()

	path := StoragePath()

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()

	count := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		count++
	}

	return count, scanner.Err()
}

// Clear removes all history.
func Clear() error {
	unlock, err := acquireLock()
	if err != nil {
		return err
	}
	defer unlock()

	path := StoragePath()
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Prune keeps only the last n entries, removing older ones.
func Prune(keep int) (int, error) {
	unlock, err := acquireLock()
	if err != nil {
		return 0, err
	}
	defer unlock()

	entries, err := readAllLocked()
	if err != nil {
		return 0, err
	}

	if len(entries) <= keep {
		return 0, nil // nothing to prune
	}

	// Keep only recent entries
	toKeep := entries[len(entries)-keep:]
	removed := len(entries) - keep

	// Rewrite file atomically
	var buf bytes.Buffer
	for _, entry := range toKeep {
		data, err := json.Marshal(entry)
		if err != nil {
			continue
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}

	path := StoragePath()
	if err := util.AtomicWriteFile(path, buf.Bytes(), 0600); err != nil {
		return 0, err
	}

	return removed, nil
}

// PruneByTime removes entries older than the cutoff time.
func PruneByTime(cutoff time.Time) (int, error) {
	unlock, err := acquireLock()
	if err != nil {
		return 0, err
	}
	defer unlock()

	entries, err := readAllLocked()
	if err != nil {
		return 0, err
	}

	var toKeep []HistoryEntry
	for _, e := range entries {
		if e.Timestamp.After(cutoff) {
			toKeep = append(toKeep, e)
		}
	}

	removed := len(entries) - len(toKeep)
	if removed == 0 {
		return 0, nil
	}

	// Rewrite file atomically
	var buf bytes.Buffer
	for _, entry := range toKeep {
		data, err := json.Marshal(entry)
		if err != nil {
			continue
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}

	path := StoragePath()
	if err := util.AtomicWriteFile(path, buf.Bytes(), 0600); err != nil {
		return 0, err
	}

	return removed, nil
}

// Search finds entries matching a query string in the prompt.
func Search(query string) ([]HistoryEntry, error) {
	entries, err := ReadAll()
	if err != nil {
		return nil, err
	}

	var result []HistoryEntry
	queryLower := strings.ToLower(query)
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Prompt), queryLower) {
			result = append(result, e)
		}
	}
	return result, nil
}

// SearchRegex finds entries where the prompt matches the provided regex pattern.
// Pattern uses Go's regexp syntax. Use inline flags like (?i) for case-insensitive matches.
func SearchRegex(pattern string) ([]HistoryEntry, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}

	entries, err := ReadAll()
	if err != nil {
		return nil, err
	}

	var result []HistoryEntry
	for _, e := range entries {
		if re.MatchString(e.Prompt) {
			result = append(result, e)
		}
	}
	return result, nil
}

// Exists checks if history file exists and has content.
func Exists() bool {
	path := StoragePath()
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Size() > 0
}

// ExportTo writes history to a specific file.
func ExportTo(path string) error {
	unlock, err := acquireLock()
	if err != nil {
		return err
	}
	defer unlock()

	entries, err := readAllLocked()
	if err != nil {
		return err
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	writer := bufio.NewWriter(f)
	for _, entry := range entries {
		data, err := json.Marshal(entry)
		if err != nil {
			continue
		}
		if _, err := writer.Write(data); err != nil {
			return err
		}
		if err := writer.WriteByte('\n'); err != nil {
			return err
		}
	}

	return writer.Flush()
}

// ImportFrom reads history from a specific file and appends to current history.
func ImportFrom(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	var entries []*HistoryEntry
	scanner := bufio.NewScanner(f)
	// Set max line size for large prompts (5MB), start with 64KB
	scanner.Buffer(make([]byte, 64*1024), 5*1024*1024)

	for scanner.Scan() {
		var entry HistoryEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		entries = append(entries, &entry)
	}

	if err := scanner.Err(); err != nil {
		return len(entries), err
	}

	if err := BatchAppend(entries); err != nil {
		return 0, err
	}

	return len(entries), nil
}

// Stats returns summary statistics about history.
type Stats struct {
	TotalEntries   int   `json:"total_entries"`
	SuccessCount   int   `json:"success_count"`
	FailureCount   int   `json:"failure_count"`
	UniqueSessions int   `json:"unique_sessions"`
	FileSizeBytes  int64 `json:"file_size_bytes"`
}

// GetStats returns history statistics.
func GetStats() (*Stats, error) {
	entries, err := ReadAll()
	if err != nil {
		return nil, err
	}

	stats := &Stats{
		TotalEntries: len(entries),
	}

	sessions := make(map[string]bool)
	for _, e := range entries {
		if e.Success {
			stats.SuccessCount++
		} else {
			stats.FailureCount++
		}
		sessions[e.Session] = true
	}
	stats.UniqueSessions = len(sessions)

	// Get file size
	path := StoragePath()
	if info, err := os.Stat(path); err == nil {
		stats.FileSizeBytes = info.Size()
	}

	return stats, nil
}

// ensure we use io package
var _ io.Reader = (*os.File)(nil)
