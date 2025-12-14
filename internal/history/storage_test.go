package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewEntry(t *testing.T) {
	entry := NewEntry("test-session", []string{"1", "2"}, "test prompt", SourceCLI)

	if entry.ID == "" {
		t.Error("expected non-empty ID")
	}
	if entry.Session != "test-session" {
		t.Errorf("expected session 'test-session', got %q", entry.Session)
	}
	if len(entry.Targets) != 2 {
		t.Errorf("expected 2 targets, got %d", len(entry.Targets))
	}
	if entry.Prompt != "test prompt" {
		t.Errorf("expected prompt 'test prompt', got %q", entry.Prompt)
	}
	if entry.Source != SourceCLI {
		t.Errorf("expected source 'cli', got %q", entry.Source)
	}
	if entry.Timestamp.IsZero() {
		t.Error("expected non-zero timestamp")
	}
}

func TestSetSuccessAndError(t *testing.T) {
	entry := NewEntry("test", nil, "test", SourceCLI)

	entry.SetSuccess()
	if !entry.Success {
		t.Error("expected success=true")
	}
	if entry.Error != "" {
		t.Error("expected empty error")
	}

	entry.SetError(&testError{})
	if entry.Success {
		t.Error("expected success=false after SetError")
	}
	if entry.Error != "test error" {
		t.Errorf("expected error 'test error', got %q", entry.Error)
	}
}

type testError struct{}

func (e *testError) Error() string { return "test error" }

func TestStorageRoundTrip(t *testing.T) {
	// Use temp directory for test
	tmpDir := t.TempDir()
	origPath := os.Getenv("XDG_DATA_HOME")
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Setenv("XDG_DATA_HOME", origPath)

	// Clear any existing data
	Clear()

	// Append entries
	entry1 := NewEntry("session1", []string{"1"}, "prompt one", SourceCLI)
	entry1.SetSuccess()
	if err := Append(entry1); err != nil {
		t.Fatalf("failed to append entry1: %v", err)
	}

	entry2 := NewEntry("session2", []string{"2", "3"}, "prompt two", SourcePalette)
	entry2.SetError(&testError{})
	if err := Append(entry2); err != nil {
		t.Fatalf("failed to append entry2: %v", err)
	}

	// Read all
	entries, err := ReadAll()
	if err != nil {
		t.Fatalf("failed to read all: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(entries))
	}

	// Check first entry
	if entries[0].Session != "session1" {
		t.Errorf("expected session 'session1', got %q", entries[0].Session)
	}
	if !entries[0].Success {
		t.Error("expected first entry to be successful")
	}

	// Check second entry
	if entries[1].Session != "session2" {
		t.Errorf("expected session 'session2', got %q", entries[1].Session)
	}
	if entries[1].Success {
		t.Error("expected second entry to be failed")
	}
}

func TestReadRecent(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	Clear()

	// Add 5 entries
	for i := 0; i < 5; i++ {
		entry := NewEntry("session", nil, "prompt", SourceCLI)
		entry.SetSuccess()
		Append(entry)
		time.Sleep(time.Millisecond) // ensure different IDs
	}

	// Read last 3
	entries, err := ReadRecent(3)
	if err != nil {
		t.Fatalf("failed to read recent: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}
}

func TestReadForSession(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	Clear()

	// Add entries for different sessions
	for i := 0; i < 3; i++ {
		entry := NewEntry("session-a", nil, "prompt", SourceCLI)
		Append(entry)
	}
	for i := 0; i < 2; i++ {
		entry := NewEntry("session-b", nil, "prompt", SourceCLI)
		Append(entry)
	}

	// Read session-a
	entries, err := ReadForSession("session-a")
	if err != nil {
		t.Fatalf("failed to read for session: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("expected 3 entries for session-a, got %d", len(entries))
	}
}

func TestPrune(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	Clear()

	// Add 10 entries
	for i := 0; i < 10; i++ {
		entry := NewEntry("session", nil, "prompt", SourceCLI)
		Append(entry)
	}

	// Prune to keep 3
	removed, err := Prune(3)
	if err != nil {
		t.Fatalf("failed to prune: %v", err)
	}
	if removed != 7 {
		t.Errorf("expected 7 removed, got %d", removed)
	}

	// Verify count
	count, err := Count()
	if err != nil {
		t.Fatalf("failed to count: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 entries after prune, got %d", count)
	}
}

func TestPruneByTime(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	Clear()

	// Add entries with different timestamps
	now := time.Now()
	entry1 := NewEntry("session", nil, "old", SourceCLI)
	entry1.Timestamp = now.Add(-2 * time.Hour)
	Append(entry1)

	entry2 := NewEntry("session", nil, "recent", SourceCLI)
	entry2.Timestamp = now.Add(-30 * time.Minute)
	Append(entry2)

	// Prune older than 1 hour
	cutoff := now.Add(-1 * time.Hour)
	removed, err := PruneByTime(cutoff)
	if err != nil {
		t.Fatalf("failed to prune by time: %v", err)
	}
	if removed != 1 {
		t.Errorf("expected 1 removed, got %d", removed)
	}

	// Verify remaining
	entries, err := ReadAll()
	if err != nil {
		t.Fatalf("failed to read all: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 entry left, got %d", len(entries))
	}
	if entries[0].Prompt != "recent" {
		t.Errorf("expected 'recent' entry, got %q", entries[0].Prompt)
	}
}

func TestSearch(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	Clear()

	// Add entries with different prompts
	Append(NewEntry("session", nil, "implement authentication", SourceCLI))
	Append(NewEntry("session", nil, "run tests", SourceCLI))
	Append(NewEntry("session", nil, "fix authentication bug", SourceCLI))

	// Search for "authentication"
	results, err := Search("authentication")
	if err != nil {
		t.Fatalf("failed to search: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}

	// Case-insensitive search
	results, err = Search("TESTS")
	if err != nil {
		t.Fatalf("failed to search: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
}

func TestClear(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	// Add entries
	Append(NewEntry("session", nil, "prompt", SourceCLI))
	Append(NewEntry("session", nil, "prompt", SourceCLI))

	// Clear
	if err := Clear(); err != nil {
		t.Fatalf("failed to clear: %v", err)
	}

	// Verify empty
	entries, err := ReadAll()
	if err != nil {
		t.Fatalf("failed to read after clear: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries after clear, got %d", len(entries))
	}
}

func TestGetStats(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	Clear()

	// Add entries
	entry1 := NewEntry("session1", nil, "prompt", SourceCLI)
	entry1.SetSuccess()
	Append(entry1)

	entry2 := NewEntry("session2", nil, "prompt", SourceCLI)
	entry2.SetSuccess()
	Append(entry2)

	entry3 := NewEntry("session1", nil, "prompt", SourceCLI)
	entry3.SetError(&testError{})
	Append(entry3)

	stats, err := GetStats()
	if err != nil {
		t.Fatalf("failed to get stats: %v", err)
	}

	if stats.TotalEntries != 3 {
		t.Errorf("expected 3 total entries, got %d", stats.TotalEntries)
	}
	if stats.SuccessCount != 2 {
		t.Errorf("expected 2 successes, got %d", stats.SuccessCount)
	}
	if stats.FailureCount != 1 {
		t.Errorf("expected 1 failure, got %d", stats.FailureCount)
	}
	if stats.UniqueSessions != 2 {
		t.Errorf("expected 2 unique sessions, got %d", stats.UniqueSessions)
	}
}

func TestExportImport(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	Clear()

	// Add entries
	Append(NewEntry("session", nil, "prompt1", SourceCLI))
	Append(NewEntry("session", nil, "prompt2", SourceCLI))

	// Export
	exportPath := filepath.Join(tmpDir, "export.jsonl")
	if err := ExportTo(exportPath); err != nil {
		t.Fatalf("failed to export: %v", err)
	}

	// Clear and import
	Clear()
	imported, err := ImportFrom(exportPath)
	if err != nil {
		t.Fatalf("failed to import: %v", err)
	}
	if imported != 2 {
		t.Errorf("expected 2 imported, got %d", imported)
	}

	// Verify
	entries, _ := ReadAll()
	if len(entries) != 2 {
		t.Errorf("expected 2 entries after import, got %d", len(entries))
	}
}

func TestExists(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Unsetenv("XDG_DATA_HOME")

	Clear()

	if Exists() {
		t.Error("expected Exists() to be false after clear")
	}

	Append(NewEntry("session", nil, "prompt", SourceCLI))

	if !Exists() {
		t.Error("expected Exists() to be true after append")
	}
}
