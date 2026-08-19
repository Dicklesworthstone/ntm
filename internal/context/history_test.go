package context

import (
	"path/filepath"
	"testing"
	"time"
)

func TestNewRotationHistoryStore(t *testing.T) {
	t.Parallel()

	store := NewRotationHistoryStore()
	if store == nil {
		t.Fatal("expected non-nil store")
	}
	if store.storagePath == "" {
		t.Error("expected non-empty storage path")
	}
}

func TestNewRotationHistoryStoreWithPath(t *testing.T) {
	t.Parallel()

	customPath := "/tmp/custom/rotations.jsonl"
	store := NewRotationHistoryStoreWithPath(customPath)
	if store.StoragePath() != customPath {
		t.Errorf("StoragePath() = %q, want %q", store.StoragePath(), customPath)
	}
}

func TestRotationHistoryStore_AppendAndRead(t *testing.T) {
	t.Parallel()

	// Create a temp directory for the test
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "rotations.jsonl")
	store := NewRotationHistoryStoreWithPath(path)

	// Initially should be empty
	records, err := store.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if len(records) != 0 {
		t.Errorf("expected 0 records initially, got %d", len(records))
	}

	// Append a record
	record1 := &RotationRecord{
		ID:            "rot-1",
		Timestamp:     time.Now(),
		SessionName:   "test-session",
		AgentID:       "test__cc_1",
		AgentType:     "claude",
		ContextBefore: 85.5,
		Method:        RotationThresholdExceeded,
		Success:       true,
		DurationMs:    1500,
	}
	if err := store.Append(record1); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	// Read back
	records, err = store.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if len(records) != 1 {
		t.Errorf("expected 1 record, got %d", len(records))
	}
	if records[0].ID != "rot-1" {
		t.Errorf("record ID = %q, want %q", records[0].ID, "rot-1")
	}
	if records[0].SessionName != "test-session" {
		t.Errorf("SessionName = %q, want %q", records[0].SessionName, "test-session")
	}

	// Append another record
	record2 := &RotationRecord{
		ID:            "rot-2",
		Timestamp:     time.Now(),
		SessionName:   "test-session-2",
		AgentID:       "test2__cc_1",
		AgentType:     "claude",
		ContextBefore: 90.0,
		Method:        RotationManual,
		Success:       false,
		FailureReason: "spawn failed",
		DurationMs:    500,
	}
	if err := store.Append(record2); err != nil {
		t.Fatalf("Append() second error = %v", err)
	}

	// Read back
	records, err = store.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll() second error = %v", err)
	}
	if len(records) != 2 {
		t.Errorf("expected 2 records, got %d", len(records))
	}
}

func TestRotationHistoryStore_ReadRecent(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "rotations.jsonl")
	store := NewRotationHistoryStoreWithPath(path)

	// Append 5 records
	for i := 1; i <= 5; i++ {
		record := &RotationRecord{
			ID:          newRecordID(),
			Timestamp:   time.Now(),
			SessionName: "test",
			AgentID:     "test__cc_1",
			Success:     true,
		}
		if err := store.Append(record); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
	}

	// Read last 3
	records, err := store.ReadRecent(3)
	if err != nil {
		t.Fatalf("ReadRecent() error = %v", err)
	}
	if len(records) != 3 {
		t.Errorf("expected 3 records, got %d", len(records))
	}
}

func TestRotationHistoryStore_ReadForSession(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "rotations.jsonl")
	store := NewRotationHistoryStoreWithPath(path)

	// Append records for different sessions
	sessions := []string{"session-a", "session-b", "session-a", "session-c", "session-a"}
	for _, s := range sessions {
		record := &RotationRecord{
			ID:          newRecordID(),
			Timestamp:   time.Now(),
			SessionName: s,
			Success:     true,
		}
		if err := store.Append(record); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
	}

	// Read for session-a
	records, err := store.ReadForSession("session-a")
	if err != nil {
		t.Fatalf("ReadForSession() error = %v", err)
	}
	if len(records) != 3 {
		t.Errorf("expected 3 records for session-a, got %d", len(records))
	}

	// Read for session-b
	records, err = store.ReadForSession("session-b")
	if err != nil {
		t.Fatalf("ReadForSession() error = %v", err)
	}
	if len(records) != 1 {
		t.Errorf("expected 1 record for session-b, got %d", len(records))
	}
}

func TestRotationHistoryStore_ReadFailed(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "rotations.jsonl")
	store := NewRotationHistoryStoreWithPath(path)

	// Append mix of successful and failed records
	for i := 0; i < 5; i++ {
		record := &RotationRecord{
			ID:        newRecordID(),
			Timestamp: time.Now(),
			Success:   i%2 == 0, // alternating success/failure
		}
		if !record.Success {
			record.FailureReason = "test failure"
		}
		if err := store.Append(record); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
	}

	// Read failed
	records, err := store.ReadFailed()
	if err != nil {
		t.Fatalf("ReadFailed() error = %v", err)
	}
	if len(records) != 2 {
		t.Errorf("expected 2 failed records, got %d", len(records))
	}
	for _, r := range records {
		if r.Success {
			t.Error("ReadFailed() returned a successful record")
		}
	}
}

func TestRotationHistoryStore_Count(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "rotations.jsonl")
	store := NewRotationHistoryStoreWithPath(path)

	// Initially 0
	count, err := store.Count()
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}

	// Append 3 records
	for i := 0; i < 3; i++ {
		record := &RotationRecord{ID: newRecordID(), Timestamp: time.Now()}
		if err := store.Append(record); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
	}

	count, err = store.Count()
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3, got %d", count)
	}
}

func TestRotationHistoryStore_Clear(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "rotations.jsonl")
	store := NewRotationHistoryStoreWithPath(path)

	// Append some records
	for i := 0; i < 3; i++ {
		record := &RotationRecord{ID: newRecordID(), Timestamp: time.Now()}
		if err := store.Append(record); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
	}

	// Clear
	if err := store.Clear(); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}

	// Should be empty
	count, err := store.Count()
	if err != nil {
		t.Fatalf("Count() after clear error = %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 after clear, got %d", count)
	}

	// Clear on non-existent file should not error
	if err := store.Clear(); err != nil {
		t.Errorf("Clear() on empty should not error, got %v", err)
	}
}

func TestRotationHistoryStore_GetStats(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "rotations.jsonl")
	store := NewRotationHistoryStoreWithPath(path)

	// Append mix of records
	records := []*RotationRecord{
		{ID: "1", SessionName: "s1", AgentType: "claude", Method: RotationThresholdExceeded, Success: true, ContextBefore: 80.0, DurationMs: 1000, CompactionTried: true, CompactionResult: "success"},
		{ID: "2", SessionName: "s1", AgentType: "claude", Method: RotationThresholdExceeded, Success: true, ContextBefore: 85.0, DurationMs: 1200, CompactionTried: true, CompactionResult: "failed"},
		{ID: "3", SessionName: "s2", AgentType: "codex", Method: RotationManual, Success: false, FailureReason: "spawn error", ContextBefore: 90.0, DurationMs: 500},
		{ID: "4", SessionName: "s2", AgentType: "codex", Method: RotationManual, Success: true, ContextBefore: 92.0, DurationMs: 800, Profile: "backend"},
	}

	for _, r := range records {
		r.Timestamp = time.Now()
		if err := store.Append(r); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
	}

	stats, err := store.GetStats()
	if err != nil {
		t.Fatalf("GetStats() error = %v", err)
	}

	if stats.TotalRotations != 4 {
		t.Errorf("TotalRotations = %d, want 4", stats.TotalRotations)
	}
	if stats.SuccessCount != 3 {
		t.Errorf("SuccessCount = %d, want 3", stats.SuccessCount)
	}
	if stats.FailureCount != 1 {
		t.Errorf("FailureCount = %d, want 1", stats.FailureCount)
	}
	if stats.UniqueSessions != 2 {
		t.Errorf("UniqueSessions = %d, want 2", stats.UniqueSessions)
	}
	if stats.ThresholdRotations != 2 {
		t.Errorf("ThresholdRotations = %d, want 2", stats.ThresholdRotations)
	}
	if stats.ManualRotations != 2 {
		t.Errorf("ManualRotations = %d, want 2", stats.ManualRotations)
	}
	if stats.CompactionAttempts != 2 {
		t.Errorf("CompactionAttempts = %d, want 2", stats.CompactionAttempts)
	}
	if stats.CompactionSuccesses != 1 {
		t.Errorf("CompactionSuccesses = %d, want 1", stats.CompactionSuccesses)
	}
	if stats.RotationsByAgentType["claude"] != 2 {
		t.Errorf("RotationsByAgentType[claude] = %d, want 2", stats.RotationsByAgentType["claude"])
	}
	if stats.RotationsByAgentType["codex"] != 2 {
		t.Errorf("RotationsByAgentType[codex] = %d, want 2", stats.RotationsByAgentType["codex"])
	}
	if stats.RotationsByProfile["backend"] != 1 {
		t.Errorf("RotationsByProfile[backend] = %d, want 1", stats.RotationsByProfile["backend"])
	}
}

func TestNewRecordID(t *testing.T) {
	t.Parallel()

	id1 := newRecordID()
	id2 := newRecordID()

	if id1 == "" || id2 == "" {
		t.Error("expected non-empty IDs")
	}
	if id1 == id2 {
		t.Error("expected unique IDs")
	}
	if len(id1) < 10 {
		t.Errorf("expected ID length >= 10, got %d", len(id1))
	}
}

func TestDefaultRotationHistoryPath(t *testing.T) {
	// t.Parallel() removed because t.Setenv is incompatible with parallel tests

	// Test with HOME set (XDG_DATA_HOME should be ignored)
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_DATA_HOME", "/should/be/ignored")

	path := defaultRotationHistoryPath()
	// New behavior: ~/.ntm/rotation_history/rotations.jsonl
	expected := filepath.Join(tmpDir, ".ntm", "rotation_history", "rotations.jsonl")
	if path != expected {
		t.Errorf("path = %q, want %q", path, expected)
	}
}

func TestRotationHistoryStoragePath(t *testing.T) {
	t.Parallel()

	// RotationHistoryStoragePath returns the path from DefaultRotationHistoryStore
	path := RotationHistoryStoragePath()
	if path == "" {
		t.Error("RotationHistoryStoragePath() returned empty string")
	}
	// Should contain the expected filename
	if !filepath.IsAbs(path) && path != "" {
		// If it's a relative path, that's okay for some test environments
		// but it should still have the expected basename
	}
	if filepath.Base(path) != rotationHistoryFile {
		t.Errorf("RotationHistoryStoragePath() basename = %q, want %q", filepath.Base(path), rotationHistoryFile)
	}
}
