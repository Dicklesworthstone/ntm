// Package context provides context window monitoring for AI agent orchestration.
// history.go implements rotation history and audit logging.
package context

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	rotationHistoryDir  = "rotation_history"
	rotationHistoryFile = "rotations.jsonl"
)

var (
	// historyMu provides goroutine safety for history operations
	historyMu sync.Mutex
)

// RotationRecord contains detailed information about a rotation event for audit/analysis.
type RotationRecord struct {
	ID        string    `json:"id"`        // Unique ID for the record
	Timestamp time.Time `json:"timestamp"` // When rotation occurred

	// Session and Agent Info
	SessionName string `json:"session_name"`
	AgentID     string `json:"agent_id"`
	AgentType   string `json:"agent_type"`
	Profile     string `json:"profile,omitempty"` // Profile/persona name if any

	// Context state before rotation
	ContextBefore    float64 `json:"context_before_percent"` // Usage % before
	EstimationMethod string  `json:"estimation_method"`      // How usage was estimated

	// Compaction attempt
	CompactionTried  bool   `json:"compaction_tried"`
	CompactionResult string `json:"compaction_result,omitempty"` // "success", "failed", "skipped"
	CompactionMethod string `json:"compaction_method,omitempty"` // "builtin", "summarize", etc.

	// Rotation result
	Method        RotationMethod `json:"method"` // threshold, manual, etc.
	Success       bool           `json:"success"`
	FailureReason string         `json:"failure_reason,omitempty"`
	SummaryTokens int            `json:"summary_tokens"`
	ContextAfter  float64        `json:"context_after_percent"` // Usage % after (should be ~0)

	// Duration
	DurationMs int64 `json:"duration_ms"`
}

// RotationHistoryStore provides persistent storage for rotation records.
type RotationHistoryStore struct {
	storagePath string
}

// NewRotationHistoryStore creates a new history store with default storage path.
func NewRotationHistoryStore() *RotationHistoryStore {
	return &RotationHistoryStore{
		storagePath: defaultRotationHistoryPath(),
	}
}

// NewRotationHistoryStoreWithPath creates a history store with a custom path.
func NewRotationHistoryStoreWithPath(path string) *RotationHistoryStore {
	return &RotationHistoryStore{
		storagePath: path,
	}
}

// defaultRotationHistoryPath returns the path to the rotation history file.
// Uses ~/.ntm/rotation_history/rotations.jsonl
func defaultRotationHistoryPath() string {
	ntmDir, err := util.NTMDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "ntm", rotationHistoryDir, rotationHistoryFile)
	}
	return filepath.Join(ntmDir, rotationHistoryDir, rotationHistoryFile)
}

// StoragePath returns the path to the rotation history file.
func (s *RotationHistoryStore) StoragePath() string {
	return s.storagePath
}

// Append adds a rotation record to the history file.
func (s *RotationHistoryStore) Append(record *RotationRecord) error {
	historyMu.Lock()
	defer historyMu.Unlock()

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(s.storagePath), 0755); err != nil {
		return fmt.Errorf("creating history directory: %w", err)
	}

	f, err := os.OpenFile(s.storagePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("opening history file: %w", err)
	}
	defer f.Close()

	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshaling record: %w", err)
	}

	// Write line with newline atomically
	_, err = f.Write(append(data, '\n'))
	return err
}

// ReadAll reads all rotation records from the history file.
func (s *RotationHistoryStore) ReadAll() ([]RotationRecord, error) {
	historyMu.Lock()
	defer historyMu.Unlock()

	return s.readAllLocked()
}

// readAllLocked reads all records (caller must hold lock)
func (s *RotationHistoryStore) readAllLocked() ([]RotationRecord, error) {
	f, err := os.Open(s.storagePath)
	if err != nil {
		if os.IsNotExist(err) {
			return []RotationRecord{}, nil
		}
		return nil, err
	}
	defer f.Close()

	var records []RotationRecord
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 5*1024*1024)

	for scanner.Scan() {
		var record RotationRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			// Skip malformed lines
			continue
		}
		records = append(records, record)
	}

	if err := scanner.Err(); err != nil {
		return records, err
	}

	return records, nil
}

// ReadRecent reads the last n rotation records.
func (s *RotationHistoryStore) ReadRecent(n int) ([]RotationRecord, error) {
	historyMu.Lock()
	defer historyMu.Unlock()

	if n <= 0 {
		return []RotationRecord{}, nil
	}

	f, err := os.Open(s.storagePath)
	if err != nil {
		if os.IsNotExist(err) {
			return []RotationRecord{}, nil
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
		return []RotationRecord{}, nil
	}

	// Scan backwards for newlines
	const bufferSize = 4096
	buf := make([]byte, bufferSize)
	offset := fileSize
	newlinesFound := 0

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

	var records []RotationRecord
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 5*1024*1024)

	for scanner.Scan() {
		var record RotationRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			continue
		}
		records = append(records, record)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	if len(records) > n {
		records = records[len(records)-n:]
	}

	return records, nil
}

// ReadForSession reads rotation records for a specific session.
func (s *RotationHistoryStore) ReadForSession(session string) ([]RotationRecord, error) {
	records, err := s.ReadAll()
	if err != nil {
		return nil, err
	}

	var result []RotationRecord
	for _, r := range records {
		if r.SessionName == session {
			result = append(result, r)
		}
	}
	return result, nil
}

// ReadFailed reads only failed rotation records.
func (s *RotationHistoryStore) ReadFailed() ([]RotationRecord, error) {
	records, err := s.ReadAll()
	if err != nil {
		return nil, err
	}

	var result []RotationRecord
	for _, r := range records {
		if !r.Success {
			result = append(result, r)
		}
	}
	return result, nil
}

// Count returns the number of rotation records.
func (s *RotationHistoryStore) Count() (int, error) {
	historyMu.Lock()
	defer historyMu.Unlock()

	f, err := os.Open(s.storagePath)
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

// Clear removes all rotation history.
func (s *RotationHistoryStore) Clear() error {
	historyMu.Lock()
	defer historyMu.Unlock()

	err := os.Remove(s.storagePath)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Prune keeps only the last n records, removing older ones.
func (s *RotationHistoryStore) Prune(keep int) (int, error) {
	historyMu.Lock()
	defer historyMu.Unlock()

	records, err := s.readAllLocked()
	if err != nil {
		return 0, err
	}

	if len(records) <= keep {
		return 0, nil
	}

	toKeep := records[len(records)-keep:]
	removed := len(records) - keep

	// Rewrite file atomically
	dir := filepath.Dir(s.storagePath)
	tmpFile, err := os.CreateTemp(dir, "rotation-*.tmp")
	if err != nil {
		return 0, err
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()

	writer := bufio.NewWriter(tmpFile)
	for _, record := range toKeep {
		data, err := json.Marshal(record)
		if err != nil {
			continue
		}
		if _, err := writer.Write(data); err != nil {
			return 0, err
		}
		if err := writer.WriteByte('\n'); err != nil {
			return 0, err
		}
	}

	if err := writer.Flush(); err != nil {
		return 0, err
	}
	if err := tmpFile.Close(); err != nil {
		return 0, err
	}

	if err := os.Chmod(tmpFile.Name(), 0600); err != nil {
		return 0, err
	}

	if err := os.Rename(tmpFile.Name(), s.storagePath); err != nil {
		return 0, err
	}

	return removed, nil
}

// PruneByTime removes records older than the cutoff time.
func (s *RotationHistoryStore) PruneByTime(cutoff time.Time) (int, error) {
	historyMu.Lock()
	defer historyMu.Unlock()

	records, err := s.readAllLocked()
	if err != nil {
		return 0, err
	}

	var toKeep []RotationRecord
	for _, r := range records {
		if r.Timestamp.After(cutoff) {
			toKeep = append(toKeep, r)
		}
	}

	removed := len(records) - len(toKeep)
	if removed == 0 {
		return 0, nil
	}

	// Rewrite file atomically
	dir := filepath.Dir(s.storagePath)
	tmpFile, err := os.CreateTemp(dir, "rotation-*.tmp")
	if err != nil {
		return 0, err
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()

	writer := bufio.NewWriter(tmpFile)
	for _, record := range toKeep {
		data, err := json.Marshal(record)
		if err != nil {
			continue
		}
		if _, err := writer.Write(data); err != nil {
			return 0, err
		}
		if err := writer.WriteByte('\n'); err != nil {
			return 0, err
		}
	}

	if err := writer.Flush(); err != nil {
		return 0, err
	}
	if err := tmpFile.Close(); err != nil {
		return 0, err
	}

	if err := os.Chmod(tmpFile.Name(), 0600); err != nil {
		return 0, err
	}

	if err := os.Rename(tmpFile.Name(), s.storagePath); err != nil {
		return 0, err
	}

	return removed, nil
}

// Exists checks if rotation history file exists and has content.
func (s *RotationHistoryStore) Exists() bool {
	info, err := os.Stat(s.storagePath)
	if err != nil {
		return false
	}
	return info.Size() > 0
}

// RotationStats contains summary statistics about rotation history.
type RotationStats struct {
	TotalRotations   int     `json:"total_rotations"`
	SuccessCount     int     `json:"success_count"`
	FailureCount     int     `json:"failure_count"`
	UniqueSessions   int     `json:"unique_sessions"`
	AvgContextBefore float64 `json:"avg_context_before_percent"`
	AvgDurationMs    int64   `json:"avg_duration_ms"`
	FileSizeBytes    int64   `json:"file_size_bytes"`

	// By method
	ThresholdRotations int `json:"threshold_rotations"`
	ManualRotations    int `json:"manual_rotations"`

	// Compaction stats
	CompactionAttempts  int `json:"compaction_attempts"`
	CompactionSuccesses int `json:"compaction_successes"`

	// By agent type
	RotationsByAgentType map[string]int `json:"rotations_by_agent_type"`

	// By profile
	RotationsByProfile map[string]int `json:"rotations_by_profile,omitempty"`
}

// GetStats returns rotation history statistics.
func (s *RotationHistoryStore) GetStats() (*RotationStats, error) {
	records, err := s.ReadAll()
	if err != nil {
		return nil, err
	}

	stats := &RotationStats{
		TotalRotations:       len(records),
		RotationsByAgentType: make(map[string]int),
		RotationsByProfile:   make(map[string]int),
	}

	sessions := make(map[string]bool)
	var totalContext float64
	var totalDuration int64

	for _, r := range records {
		if r.Success {
			stats.SuccessCount++
		} else {
			stats.FailureCount++
		}

		sessions[r.SessionName] = true
		totalContext += r.ContextBefore
		totalDuration += r.DurationMs

		switch r.Method {
		case RotationThresholdExceeded:
			stats.ThresholdRotations++
		case RotationManual:
			stats.ManualRotations++
		}

		if r.CompactionTried {
			stats.CompactionAttempts++
			if r.CompactionResult == "success" {
				stats.CompactionSuccesses++
			}
		}

		if r.AgentType != "" {
			stats.RotationsByAgentType[r.AgentType]++
		}

		if r.Profile != "" {
			stats.RotationsByProfile[r.Profile]++
		}
	}

	stats.UniqueSessions = len(sessions)

	if len(records) > 0 {
		stats.AvgContextBefore = totalContext / float64(len(records))
		stats.AvgDurationMs = totalDuration / int64(len(records))
	}

	// Get file size
	if info, err := os.Stat(s.storagePath); err == nil {
		stats.FileSizeBytes = info.Size()
	}

	// Don't return empty profile map
	if len(stats.RotationsByProfile) == 0 {
		stats.RotationsByProfile = nil
	}

	return stats, nil
}

// NewRotationRecord creates a new rotation record from a RotationResult.
func NewRotationRecord(result *RotationResult, session, profile, estimationMethod string) *RotationRecord {
	record := &RotationRecord{
		ID:               newRecordID(),
		Timestamp:        result.Timestamp,
		SessionName:      session,
		AgentID:          result.OldAgentID,
		AgentType:        deriveAgentTypeFromID(result.OldAgentID),
		Profile:          profile,
		Method:           result.Method,
		Success:          result.Success,
		SummaryTokens:    result.SummaryTokens,
		DurationMs:       result.Duration.Milliseconds(),
		EstimationMethod: estimationMethod,
	}

	if !result.Success {
		record.FailureReason = result.Error
	}

	return record
}

// newRecordID generates a unique, sortable ID for rotation records.
func newRecordID() string {
	ms := time.Now().UnixMilli()
	b := make([]byte, 4)
	_, _ = rand.Read(b) // Error is extremely unlikely; fallback is still unique via timestamp
	return fmt.Sprintf("rot-%d-%x", ms, b)
}

// DefaultRotationHistoryStore is the default global history store.
var DefaultRotationHistoryStore = NewRotationHistoryStore()

// RecordRotation is a convenience function to append a rotation to the default store.
func RecordRotation(record *RotationRecord) error {
	return DefaultRotationHistoryStore.Append(record)
}

// GetRotationStats is a convenience function to get stats from the default store.
func GetRotationStats() (*RotationStats, error) {
	return DefaultRotationHistoryStore.GetStats()
}

// GetRecentRotations is a convenience function to get recent rotations from the default store.
func GetRecentRotations(n int) ([]RotationRecord, error) {
	return DefaultRotationHistoryStore.ReadRecent(n)
}

// GetRotationsForSession is a convenience function to get rotations for a session.
func GetRotationsForSession(session string) ([]RotationRecord, error) {
	return DefaultRotationHistoryStore.ReadForSession(session)
}

// GetFailedRotations is a convenience function to get failed rotations.
func GetFailedRotations() ([]RotationRecord, error) {
	return DefaultRotationHistoryStore.ReadFailed()
}

// RotationHistoryStoragePath returns the path to the rotation history file.
func RotationHistoryStoragePath() string {
	return DefaultRotationHistoryStore.StoragePath()
}
