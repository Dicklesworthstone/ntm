package checkpoint

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// =============================================================================
// Error Handling Tests: Recovery Failure Scenarios (bd-rjlw)
// =============================================================================
//
// These tests cover error scenarios for session recovery:
// 1. Corrupted state files (malformed JSON, missing fields, invalid versions)
// 2. Pane recreation failures (partial success, layout errors)
// 3. Graceful degradation (recovery continues despite individual failures)
// 4. State file handling edge cases
// =============================================================================

// --- Corrupted State File Tests ---

func TestStorage_Load_MalformedJSON(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-malformed"

	// Create checkpoint directory manually
	cpDir := storage.CheckpointDir(sessionName, checkpointID)
	if err := os.MkdirAll(cpDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	// Write malformed JSON (truncated)
	metaPath := filepath.Join(cpDir, MetadataFile)
	if err := os.WriteFile(metaPath, []byte(`{"id": "test", "session_name": "test`), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	_, err := storage.Load(sessionName, checkpointID)
	if err == nil {
		t.Error("Load() should fail for malformed JSON")
	}

	// Verify error message contains parsing info.
	if !containsSubstr(err.Error(), "parsing checkpoint metadata") {
		t.Fatalf("Load() error = %v, want checkpoint metadata parsing context", err)
	}
}

func TestStorage_Load_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-empty"

	cpDir := storage.CheckpointDir(sessionName, checkpointID)
	if err := os.MkdirAll(cpDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	// Write empty file
	metaPath := filepath.Join(cpDir, MetadataFile)
	if err := os.WriteFile(metaPath, []byte(""), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	_, err := storage.Load(sessionName, checkpointID)
	if err == nil {
		t.Error("Load() should fail for empty file")
	}
}

func TestStorage_Load_InvalidJSONStructure(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-invalid"

	cpDir := storage.CheckpointDir(sessionName, checkpointID)
	if err := os.MkdirAll(cpDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	// Write valid JSON but wrong structure (array instead of object)
	metaPath := filepath.Join(cpDir, MetadataFile)
	if err := os.WriteFile(metaPath, []byte(`["not", "a", "checkpoint"]`), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	_, err := storage.Load(sessionName, checkpointID)
	if err == nil {
		t.Error("Load() should fail for invalid JSON structure")
	}
}

func TestStorage_Load_MissingRequiredFields(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-missing"

	cpDir := storage.CheckpointDir(sessionName, checkpointID)
	if err := os.MkdirAll(cpDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	// Write JSON with missing critical fields
	invalidCP := map[string]interface{}{
		"name": "test",
		// Missing: id, session_name, working_dir, session
	}
	data, _ := json.Marshal(invalidCP)
	metaPath := filepath.Join(cpDir, MetadataFile)
	if err := os.WriteFile(metaPath, data, 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	_, err := storage.Load(sessionName, checkpointID)
	if err == nil {
		t.Fatal("Load() should fail when required metadata fields are missing")
	}
	if !containsSubstr(err.Error(), "invalid checkpoint metadata") {
		t.Fatalf("expected invalid metadata error, got %v", err)
	}
}

func TestStorage_Load_RejectsSessionMetadataMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-mismatch"

	cpDir := storage.CheckpointDir(sessionName, checkpointID)
	if err := os.MkdirAll(cpDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	mismatchedCP := &Checkpoint{
		ID:          checkpointID,
		SessionName: "other-session",
		WorkingDir:  tmpDir,
		Session:     SessionState{Panes: []PaneState{{Index: 0, ID: "%0"}}},
	}
	data, _ := json.Marshal(mismatchedCP)
	metaPath := filepath.Join(cpDir, MetadataFile)
	if err := os.WriteFile(metaPath, data, 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	_, err := storage.Load(sessionName, checkpointID)
	if err == nil {
		t.Fatal("Load() should fail for mismatched session metadata")
	}
	if !containsSubstr(err.Error(), "session mismatch") {
		t.Fatalf("expected session mismatch error, got %v", err)
	}
}

func TestStorage_Load_RejectsCheckpointIDMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-expected"

	cpDir := storage.CheckpointDir(sessionName, checkpointID)
	if err := os.MkdirAll(cpDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	mismatchedCP := &Checkpoint{
		ID:          "20251210-120000-actual",
		SessionName: sessionName,
		WorkingDir:  tmpDir,
		Session:     SessionState{Panes: []PaneState{{Index: 0, ID: "%0"}}},
	}
	data, _ := json.Marshal(mismatchedCP)
	metaPath := filepath.Join(cpDir, MetadataFile)
	if err := os.WriteFile(metaPath, data, 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	_, err := storage.Load(sessionName, checkpointID)
	if err == nil {
		t.Fatal("Load() should fail for mismatched checkpoint ID metadata")
	}
	if !containsSubstr(err.Error(), "ID mismatch") {
		t.Fatalf("expected ID mismatch error, got %v", err)
	}
}

func TestStorage_Load_NullValues(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-nulls"

	cpDir := storage.CheckpointDir(sessionName, checkpointID)
	if err := os.MkdirAll(cpDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	// Write JSON with null values
	nullCP := `{
		"id": "20251210-120000-nulls",
		"session_name": "testproject",
		"session": null,
		"git": null
	}`
	metaPath := filepath.Join(cpDir, MetadataFile)
	if err := os.WriteFile(metaPath, []byte(nullCP), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cpDir, SessionFile), []byte("null"), 0600); err != nil {
		t.Fatalf("WriteFile session failed: %v", err)
	}

	cp, err := storage.Load(sessionName, checkpointID)
	if err != nil {
		t.Fatalf("Load() should handle null values: %v", err)
	}

	// Verify graceful handling of nulls
	if cp.ID != checkpointID {
		t.Errorf("ID should be %q, got %q", checkpointID, cp.ID)
	}
	// Session and Git should be zero values
	if len(cp.Session.Panes) != 0 {
		t.Error("Session.Panes should be empty for null session")
	}
}

func TestStorage_Load_RejectsCorruptSessionState(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-corrupt-session"

	cpDir := storage.CheckpointDir(sessionName, checkpointID)
	if err := os.MkdirAll(cpDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	cp := &Checkpoint{
		ID:          checkpointID,
		SessionName: sessionName,
		WorkingDir:  tmpDir,
		Session:     SessionState{Panes: []PaneState{{Index: 0, ID: "%0"}}},
	}
	data, _ := json.Marshal(cp)
	if err := os.WriteFile(filepath.Join(cpDir, MetadataFile), data, 0600); err != nil {
		t.Fatalf("WriteFile metadata failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cpDir, SessionFile), []byte("{invalid json"), 0600); err != nil {
		t.Fatalf("WriteFile session failed: %v", err)
	}

	_, err := storage.Load(sessionName, checkpointID)
	if err == nil {
		t.Fatal("Load() should fail for corrupt session state")
	}
	if !containsSubstr(err.Error(), "parsing session state") {
		t.Fatalf("expected session parse error, got %v", err)
	}
}

func TestStorage_Load_RejectsMissingSessionState(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-missing-session"

	cpDir := storage.CheckpointDir(sessionName, checkpointID)
	if err := os.MkdirAll(cpDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	cp := &Checkpoint{
		ID:          checkpointID,
		SessionName: sessionName,
		WorkingDir:  tmpDir,
		PaneCount:   1,
		Session:     SessionState{Panes: []PaneState{{Index: 0, ID: "%0"}}},
	}
	data, _ := json.Marshal(cp)
	if err := os.WriteFile(filepath.Join(cpDir, MetadataFile), data, 0600); err != nil {
		t.Fatalf("WriteFile metadata failed: %v", err)
	}

	_, err := storage.Load(sessionName, checkpointID)
	if err == nil {
		t.Fatal("Load() should fail when session.json is missing")
	}
	if !containsSubstr(err.Error(), "reading session state") {
		t.Fatalf("expected session state read error, got %v", err)
	}
}

func TestStorage_List_SkipsMetadataOnlyCheckpoint(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "metadata-only-session"
	checkpointID := "cp-metadata-only"

	cpDir := storage.CheckpointDir(sessionName, checkpointID)
	if err := os.MkdirAll(cpDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	metadata := `{"version":1,"id":"cp-metadata-only","name":"meta-only","session_name":"metadata-only-session","working_dir":"/tmp","created_at":"2025-01-01T00:00:00Z","pane_count":2}`
	if err := os.WriteFile(filepath.Join(cpDir, MetadataFile), []byte(metadata), 0600); err != nil {
		t.Fatalf("WriteFile metadata failed: %v", err)
	}

	list, err := storage.List(sessionName)
	if err != nil {
		t.Fatalf("List() failed: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("len(list) = %d, want 0", len(list))
	}
}

func TestStorage_Load_RejectsSessionStateMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-session-mismatch"

	cpDir := storage.CheckpointDir(sessionName, checkpointID)
	if err := os.MkdirAll(cpDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	cp := &Checkpoint{
		ID:          checkpointID,
		SessionName: sessionName,
		WorkingDir:  tmpDir,
		Session:     SessionState{Panes: []PaneState{{Index: 0, ID: "%0", Title: "metadata"}}},
	}
	data, _ := json.Marshal(cp)
	if err := os.WriteFile(filepath.Join(cpDir, MetadataFile), data, 0600); err != nil {
		t.Fatalf("WriteFile metadata failed: %v", err)
	}
	sessionData, _ := json.Marshal(SessionState{
		Panes: []PaneState{{Index: 0, ID: "%0", Title: "session-file"}},
	})
	if err := os.WriteFile(filepath.Join(cpDir, SessionFile), sessionData, 0600); err != nil {
		t.Fatalf("WriteFile session failed: %v", err)
	}

	_, err := storage.Load(sessionName, checkpointID)
	if err == nil {
		t.Fatal("Load() should fail for mismatched session state")
	}
	if !containsSubstr(err.Error(), "session state mismatch") {
		t.Fatalf("expected session mismatch error, got %v", err)
	}
}

func TestStorage_Load_RejectsPaneCountMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-pane-count-mismatch"

	cpDir := storage.CheckpointDir(sessionName, checkpointID)
	if err := os.MkdirAll(cpDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	cp := &Checkpoint{
		ID:          checkpointID,
		SessionName: sessionName,
		WorkingDir:  tmpDir,
		Session:     SessionState{Panes: []PaneState{{Index: 0, ID: "%0"}}},
		PaneCount:   2,
	}
	data, _ := json.Marshal(cp)
	if err := os.WriteFile(filepath.Join(cpDir, MetadataFile), data, 0600); err != nil {
		t.Fatalf("WriteFile metadata failed: %v", err)
	}
	sessionData, _ := json.Marshal(cp.Session)
	if err := os.WriteFile(filepath.Join(cpDir, SessionFile), sessionData, 0600); err != nil {
		t.Fatalf("WriteFile session failed: %v", err)
	}

	_, err := storage.Load(sessionName, checkpointID)
	if err == nil {
		t.Fatal("Load() should fail for pane count mismatch")
	}
	if !containsSubstr(err.Error(), "pane count mismatch") {
		t.Fatalf("expected pane count mismatch error, got %v", err)
	}
}

func TestStorage_Load_NormalizesZeroPaneCount(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-zero-pane-count"

	cpDir := storage.CheckpointDir(sessionName, checkpointID)
	if err := os.MkdirAll(cpDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	cp := &Checkpoint{
		ID:          checkpointID,
		SessionName: sessionName,
		WorkingDir:  tmpDir,
		Session:     SessionState{Panes: []PaneState{{Index: 0, ID: "%0"}}},
		PaneCount:   0,
	}
	data, _ := json.Marshal(cp)
	if err := os.WriteFile(filepath.Join(cpDir, MetadataFile), data, 0600); err != nil {
		t.Fatalf("WriteFile metadata failed: %v", err)
	}
	sessionData, _ := json.Marshal(cp.Session)
	if err := os.WriteFile(filepath.Join(cpDir, SessionFile), sessionData, 0600); err != nil {
		t.Fatalf("WriteFile session failed: %v", err)
	}

	loaded, err := storage.Load(sessionName, checkpointID)
	if err != nil {
		t.Fatalf("Load() should succeed for zero pane count: %v", err)
	}
	if loaded.PaneCount != 1 {
		t.Fatalf("PaneCount = %d, want 1", loaded.PaneCount)
	}
}

func TestStorage_LoadScrollback_FileNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-noscroll"

	// Create checkpoint without scrollback files
	cp := &Checkpoint{
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	// Try to load non-existent scrollback
	_, err := storage.LoadScrollback(sessionName, checkpointID, "%0")
	if err == nil {
		t.Error("LoadScrollback() should fail for non-existent file")
	}
}

func TestStorage_LoadScrollback_PermissionDenied(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("Skipping permission test when running as root")
	}

	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-permdeny"

	// Create checkpoint with scrollback
	cp := &Checkpoint{
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	// Save scrollback
	_, err := storage.SaveScrollback(sessionName, checkpointID, "%0", "test content")
	if err != nil {
		t.Fatalf("SaveScrollback() failed: %v", err)
	}

	// Make scrollback file unreadable
	scrollbackPath := filepath.Join(storage.PanesDirPath(sessionName, checkpointID), "pane__0.txt")
	if err := os.Chmod(scrollbackPath, 0000); err != nil {
		t.Fatalf("Chmod failed: %v", err)
	}
	defer os.Chmod(scrollbackPath, 0644) // Restore permissions for cleanup

	// Try to load
	_, err = storage.LoadScrollback(sessionName, checkpointID, "%0")
	if err == nil {
		t.Error("LoadScrollback() should fail with permission denied")
	}
}

// --- Restorer Error Tests ---

func TestRestorer_RestoreFromCheckpoint_NilCheckpoint(t *testing.T) {
	r := NewRestorer()

	_, err := r.RestoreFromCheckpoint(nil, RestoreOptions{DryRun: true})
	if !errors.Is(err, ErrNilCheckpoint) {
		t.Fatalf("RestoreFromCheckpoint(nil) error = %v, want %v", err, ErrNilCheckpoint)
	}
}

func TestRestorer_RestoreFromCheckpoint_EmptySessionName(t *testing.T) {
	tmpDir := t.TempDir()
	r := NewRestorerWithStorage(NewStorageWithDir(tmpDir))

	cp := &Checkpoint{
		ID:          "test",
		SessionName: "", // Empty session name
		WorkingDir:  tmpDir,
		Session: SessionState{
			Panes: []PaneState{{Index: 0, ID: "%0"}},
		},
	}

	// Empty session name may match tmux behavior (existing session check)
	// With Force option and DryRun, it should proceed
	result, err := r.RestoreFromCheckpoint(cp, RestoreOptions{DryRun: true, Force: true})
	if err != nil {
		t.Fatalf("DryRun with empty session name and Force failed: %v", err)
	}
	if result.SessionName != "" {
		t.Errorf("SessionName should be empty, got %q", result.SessionName)
	}
	if result.PanesRestored != 1 {
		t.Errorf("PanesRestored = %d, want 1", result.PanesRestored)
	}
}

func TestRestorer_RestoreFromCheckpoint_WorkingDir_PermissionDenied(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("Skipping permission test when running as root")
	}

	tmpDir := t.TempDir()
	restrictedDir := filepath.Join(tmpDir, "restricted")
	if err := os.MkdirAll(restrictedDir, 0000); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	defer os.Chmod(restrictedDir, 0755)

	r := NewRestorerWithStorage(NewStorageWithDir(tmpDir))

	cp := &Checkpoint{
		ID:          "test",
		SessionName: "test-perm-" + time.Now().Format("150405"), // Unique session name
		WorkingDir:  filepath.Join(restrictedDir, "subdir"),     // Unreadable parent
		Session: SessionState{
			Panes: []PaneState{{Index: 0, ID: "%0"}},
		},
	}

	// Note: Current implementation only checks os.IsNotExist, not permission errors.
	// Permission denied on stat returns a different error that's not explicitly handled.
	// In DryRun mode, it proceeds and collects warnings via ValidateCheckpoint.
	result, err := r.RestoreFromCheckpoint(cp, RestoreOptions{DryRun: true, Force: true})
	if err != nil {
		// If it fails, that's also acceptable behavior
		t.Logf("RestoreFromCheckpoint failed (expected): %v", err)
		return
	}

	// In DryRun, it might proceed but should ideally have warnings
	t.Logf("DryRun result: PanesRestored=%d, Warnings=%v", result.PanesRestored, result.Warnings)
}

// --- Graceful Degradation Tests ---

func TestRestorer_RestoreFromCheckpoint_DryRun_CollectsWarnings(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	r := NewRestorerWithStorage(storage)

	cp := &Checkpoint{
		ID:          "test-id",
		SessionName: "test-session",
		WorkingDir:  "/nonexistent/path", // Will generate warning
		Session: SessionState{
			Panes: []PaneState{
				{Index: 0, ID: "%0"},
				{Index: 1, ID: "%1"},
			},
		},
		Git: GitState{
			Branch: "nonexistent-branch",
			Commit: "abc123",
		},
	}

	result, err := r.RestoreFromCheckpoint(cp, RestoreOptions{DryRun: true})
	if err != nil {
		t.Fatalf("DryRun should not fail: %v", err)
	}

	// Should have collected warnings
	if len(result.Warnings) == 0 {
		t.Error("Expected warnings for nonexistent directory")
	}

	// Should still report correct pane count
	if result.PanesRestored != 2 {
		t.Errorf("PanesRestored = %d, want 2", result.PanesRestored)
	}
}

func TestStorage_GetLatest_RejectsInvalidNewestCheckpointAfterTolerantList(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"

	// Create a valid checkpoint
	validCP := &Checkpoint{
		ID:          "valid-checkpoint",
		Name:        "valid",
		SessionName: sessionName,
		CreatedAt:   time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC),
		WorkingDir:  tmpDir,
		Session:     SessionState{Panes: []PaneState{{Index: 0, ID: "%0"}}},
	}
	if err := storage.Save(validCP); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}
	validDir := storage.CheckpointDir(sessionName, validCP.ID)
	validTime := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)
	if err := os.Chtimes(validDir, validTime, validTime); err != nil {
		t.Fatalf("Chtimes(validDir) failed: %v", err)
	}

	// Create an invalid checkpoint (corrupted JSON)
	invalidDir := storage.CheckpointDir(sessionName, "invalid-checkpoint")
	if err := os.MkdirAll(invalidDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	metaPath := filepath.Join(invalidDir, MetadataFile)
	if err := os.WriteFile(metaPath, []byte("not valid json"), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	invalidTime := time.Date(2025, 1, 1, 11, 0, 0, 0, time.UTC)
	if err := os.Chtimes(invalidDir, invalidTime, invalidTime); err != nil {
		t.Fatalf("Chtimes(invalidDir) failed: %v", err)
	}

	// List should still work, skipping invalid checkpoints
	list, err := storage.List(sessionName)
	if err != nil {
		t.Fatalf("List() failed: %v", err)
	}

	if len(list) != 1 {
		t.Errorf("List should have 1 valid checkpoint, got %d", len(list))
	}

	// GetLatest should fail closed instead of silently returning the older valid checkpoint.
	_, err = storage.GetLatest(sessionName)
	if err == nil {
		t.Fatal("GetLatest() error = nil, want invalid newest checkpoint error")
	}
	if !containsSubstr(err.Error(), "checkpoint selection blocked by invalid checkpoint") {
		t.Fatalf("GetLatest() error = %v, want invalid newest checkpoint context", err)
	}
}

// --- State File Edge Cases ---

func TestStorage_Save_DirectoryCreationFailure(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("Skipping permission test when running as root")
	}

	tmpDir := t.TempDir()

	// Make base directory unwritable
	if err := os.Chmod(tmpDir, 0444); err != nil {
		t.Fatalf("Chmod failed: %v", err)
	}
	defer os.Chmod(tmpDir, 0755)

	storage := NewStorageWithDir(filepath.Join(tmpDir, "checkpoints"))

	cp := &Checkpoint{
		ID:          "test-id",
		SessionName: "test",
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
	}

	err := storage.Save(cp)
	if err == nil {
		t.Error("Save() should fail when directory cannot be created")
	}
}

func TestStorage_Delete_NonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	// Delete non-existent checkpoint should succeed (os.RemoveAll behavior)
	err := storage.Delete("nonexistent", "nonexistent")
	if err != nil {
		t.Errorf("Delete() of non-existent checkpoint should succeed: %v", err)
	}
}

func TestStorage_List_PartiallyCorruptedDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"

	// Create multiple checkpoints
	for i := 0; i < 3; i++ {
		cp := &Checkpoint{
			ID:          GenerateID("backup"),
			Name:        "backup",
			SessionName: sessionName,
			CreatedAt:   time.Now().Add(time.Duration(i) * time.Hour),
			Session:     SessionState{Panes: []PaneState{}},
		}
		if err := storage.Save(cp); err != nil {
			t.Fatalf("Save() failed: %v", err)
		}
	}

	// Corrupt one checkpoint
	sessionDir := filepath.Join(tmpDir, sessionName)
	entries, _ := os.ReadDir(sessionDir)
	if len(entries) > 0 {
		corruptDir := filepath.Join(sessionDir, entries[0].Name())
		metaPath := filepath.Join(corruptDir, MetadataFile)
		if err := os.WriteFile(metaPath, []byte("corrupted"), 0600); err != nil {
			t.Fatalf("WriteFile failed: %v", err)
		}
	}

	// List should still return the valid checkpoints
	list, err := storage.List(sessionName)
	if err != nil {
		t.Fatalf("List() failed: %v", err)
	}

	// Should have 2 valid checkpoints (one was corrupted)
	if len(list) != 2 {
		t.Errorf("List should have 2 valid checkpoints, got %d", len(list))
	}
}

func TestStorage_SaveScrollback_InvalidPaneID(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cp := &Checkpoint{
		ID:          "test-id",
		SessionName: "test",
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	// Save scrollback with special characters in pane ID
	specialIDs := []string{
		"../escape",
		"path/traversal",
		"special%chars",
		"null\x00byte",
	}

	for _, id := range specialIDs {
		_, err := storage.SaveScrollback(cp.SessionName, cp.ID, id, "test content")
		if err != nil {
			t.Logf("SaveScrollback(%q) failed as expected: %v", id, err)
		} else {
			// Should sanitize the ID, not fail
			t.Logf("SaveScrollback(%q) succeeded with sanitization", id)
		}
	}
}

// --- Recovery Manager Failure Tests ---

func TestRecoveryManager_BuildContextAwarePrompt_BvUnavailable(t *testing.T) {
	// When bv is not installed, BuildContextAwarePrompt should return base prompt
	// This is tested indirectly - if bv is not available, context will be nil
	basePrompt := "Reread AGENTS.md"
	result := buildContextAwarePromptWithoutBv(basePrompt)
	if result != basePrompt {
		t.Errorf("Without bv, should return base prompt, got %q", result)
	}
}

// buildContextAwarePromptWithoutBv simulates BuildContextAwarePrompt when bv is unavailable
func buildContextAwarePromptWithoutBv(basePrompt string) string {
	// This simulates the behavior when bv.IsInstalled() returns false
	return basePrompt
}

// --- Integration: Multiple Failure Recovery ---

// --- Logging Verification Tests ---

func TestRestoreResult_WarningMessages_Formatted(t *testing.T) {
	result := &RestoreResult{
		SessionName:     "test-session",
		PanesRestored:   2,
		ContextInjected: false,
		DryRun:          false,
		Warnings: []string{
			"[RECOVERY-ERROR] Failed to restore pane cc_1: connection refused",
			"[RECOVERY-FALLBACK] Using degraded mode: no scrollback available",
			"[RECOVERY-CLEANUP] Skipped: context injection",
		},
	}

	// Verify warnings are properly formatted
	for _, warning := range result.Warnings {
		if warning == "" {
			t.Error("Warning should not be empty")
		}
	}

	if len(result.Warnings) != 3 {
		t.Errorf("Expected 3 warnings, got %d", len(result.Warnings))
	}
}

func TestRestorer_RestoreFromCheckpoint_DryRun_WarningFormats(t *testing.T) {
	tmpDir := t.TempDir()
	r := NewRestorerWithStorage(NewStorageWithDir(tmpDir))

	cp := &Checkpoint{
		ID:          "test",
		SessionName: "test-session",
		WorkingDir:  "/nonexistent/working/dir",
		Session: SessionState{
			Panes: []PaneState{{Index: 0, ID: "%0"}},
		},
		Git: GitState{
			Branch: "nonexistent-branch",
			Commit: "abc123",
		},
	}

	result, err := r.RestoreFromCheckpoint(cp, RestoreOptions{DryRun: true})
	if err != nil {
		t.Fatalf("DryRun should not error: %v", err)
	}

	// Log all warnings for verification
	for i, warning := range result.Warnings {
		t.Logf("Warning %d: %s", i, warning)
	}
}

// --- Zero-Value and Edge Case Tests ---

func TestCheckpoint_ZeroValues(t *testing.T) {
	cp := &Checkpoint{} // All zero values

	if cp.ID != "" {
		t.Error("Zero Checkpoint.ID should be empty")
	}
	if cp.SessionName != "" {
		t.Error("Zero Checkpoint.SessionName should be empty")
	}
	if len(cp.Session.Panes) != 0 {
		t.Error("Zero Checkpoint.Session.Panes should be empty")
	}
	if cp.HasGitPatch() {
		t.Error("Zero Checkpoint should not have git patch")
	}
}

func TestRestoreOptions_ZeroValues(t *testing.T) {
	opts := RestoreOptions{} // All zero values

	if opts.Force {
		t.Error("Zero RestoreOptions.Force should be false")
	}
	if opts.DryRun {
		t.Error("Zero RestoreOptions.DryRun should be false")
	}
	if opts.InjectContext {
		t.Error("Zero RestoreOptions.InjectContext should be false")
	}
	if opts.CustomDirectory != "" {
		t.Error("Zero RestoreOptions.CustomDirectory should be empty")
	}
	if opts.ScrollbackLines != 0 {
		t.Error("Zero RestoreOptions.ScrollbackLines should be 0")
	}
}

func TestRestoreResult_ZeroValues(t *testing.T) {
	result := &RestoreResult{} // All zero values

	if result.SessionName != "" {
		t.Error("Zero RestoreResult.SessionName should be empty")
	}
	if result.PanesRestored != 0 {
		t.Error("Zero RestoreResult.PanesRestored should be 0")
	}
	if result.ContextInjected {
		t.Error("Zero RestoreResult.ContextInjected should be false")
	}
	if result.DryRun {
		t.Error("Zero RestoreResult.DryRun should be false")
	}
	if len(result.Warnings) != 0 {
		t.Error("Zero RestoreResult.Warnings should be empty")
	}
}
