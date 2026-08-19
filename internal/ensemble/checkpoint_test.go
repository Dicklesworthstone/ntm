package ensemble

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewCheckpointStore(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}
	if store == nil {
		t.Fatal("store is nil")
	}

	// Verify directory was created
	checkpointDir := filepath.Join(tmpDir, checkpointDirName)
	if _, err := os.Stat(checkpointDir); os.IsNotExist(err) {
		t.Error("checkpoint directory was not created")
	}

	t.Logf("TEST: %s - assertion: checkpoint store created successfully", t.Name())
}

func TestCheckpointStore_SaveAndLoadMetadata(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	meta := CheckpointMetadata{
		SessionName: "test-session",
		Question:    "What is the meaning of life?",
		RunID:       "test-run-2",
		Status:      EnsembleActive,
		CreatedAt:   time.Now().UTC(),
		ContextHash: "def456",
		PendingIDs:  []string{"deductive", "inductive"},
		TotalModes:  2,
	}

	// Save metadata
	if err := store.SaveMetadata(meta); err != nil {
		t.Fatalf("SaveMetadata failed: %v", err)
	}

	// Load metadata
	loaded, err := store.LoadMetadata("test-run-2")
	if err != nil {
		t.Fatalf("LoadMetadata failed: %v", err)
	}

	if loaded.SessionName != meta.SessionName {
		t.Errorf("SessionName = %q, want %q", loaded.SessionName, meta.SessionName)
	}
	if loaded.Question != meta.Question {
		t.Errorf("Question = %q, want %q", loaded.Question, meta.Question)
	}
	if len(loaded.PendingIDs) != len(meta.PendingIDs) {
		t.Errorf("PendingIDs count = %d, want %d", len(loaded.PendingIDs), len(meta.PendingIDs))
	}

	t.Logf("TEST: %s - assertion: metadata save/load works", t.Name())
}

func TestCheckpointStore_LoadMetadata_RejectsRunIDMismatch(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "expected-run"
	runDir := filepath.Join(tmpDir, checkpointDirName, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir failed: %v", err)
	}

	data, err := json.Marshal(CheckpointMetadata{RunID: "other-run"})
	if err != nil {
		t.Fatalf("marshal metadata failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, checkpointMetaFile), data, 0o644); err != nil {
		t.Fatalf("write metadata failed: %v", err)
	}

	_, err = store.LoadMetadata(runID)
	if err == nil {
		t.Fatal("LoadMetadata() error = nil, want run ID mismatch")
	}
	if !strings.Contains(err.Error(), "metadata run ID mismatch") {
		t.Fatalf("LoadMetadata() error = %v, want run ID mismatch", err)
	}
}

// bd-du7e5: NormalizeCheckpointRunID is the load-bearing guard
// against path traversal in every cli/ensemble caller that joins a
// runID into a filesystem path. The function exists and is called
// throughout (including from cli/ensemble.go::resolveEnsembleCheckpoint
// StoreForRunID) but had no focused unit test pinning the rejection
// contract — a future refactor that loosens the validation could go
// unnoticed. This test pins every documented-malformed shape.
func TestNormalizeCheckpointRunID_RejectsTraversalAndPathSeparators(t *testing.T) {
	t.Parallel()
	rejectCases := []struct {
		name  string
		runID string
	}{
		{"empty", ""},
		{"whitespace_only", "   "},
		{"single_dot", "."},
		{"double_dot", ".."},
		{"parent_then_subdir", "../foo"},
		{"deep_traversal", "../../../etc/passwd"},
		{"absolute_unix", "/abs/path"},
		{"absolute_root", "/"},
		{"contains_forward_slash", "a/b"},
		{"contains_backslash", "a\\b"},
		{"contains_nul_byte", "run\x00id"},
		{"trailing_slash", "trailing/"},
		{"leading_slash", "/leading"},
		{"dot_dot_in_middle", "foo/../bar"},
	}
	for _, c := range rejectCases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NormalizeCheckpointRunID(c.runID)
			if err == nil {
				t.Errorf("NormalizeCheckpointRunID(%q) = (%q, nil); want non-nil error", c.runID, got)
			}
		})
	}

	acceptCases := []struct {
		name  string
		runID string
		want  string
	}{
		{"alphanumeric", "abc123", "abc123"},
		{"with_dash", "run-2026-05-08", "run-2026-05-08"},
		{"with_underscore", "run_42", "run_42"},
		{"leading_dot_dot_basename", "..hidden_id", "..hidden_id"},
		{"trimmed_whitespace", "   ok   ", "ok"},
		{"single_dot_in_middle", "v1.0.beta", "v1.0.beta"},
	}
	for _, c := range acceptCases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NormalizeCheckpointRunID(c.runID)
			if err != nil {
				t.Errorf("NormalizeCheckpointRunID(%q) error = %v, want nil", c.runID, err)
			}
			if got != c.want {
				t.Errorf("NormalizeCheckpointRunID(%q) = %q, want %q", c.runID, got, c.want)
			}
		})
	}
}

func TestCheckpointStore_RejectsInvalidRunID(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	err = store.SaveMetadata(CheckpointMetadata{
		RunID:       "../escape",
		SessionName: "test-session",
		Question:    "bad run id",
	})
	if err == nil {
		t.Fatal("SaveMetadata() error = nil, want invalid run ID")
	}
	if !strings.Contains(err.Error(), "invalid run ID") {
		t.Fatalf("SaveMetadata() error = %v, want invalid run ID", err)
	}

	if _, err := store.LoadMetadata("../escape"); err == nil {
		t.Fatal("LoadMetadata() error = nil, want invalid run ID")
	} else if !strings.Contains(err.Error(), "invalid run ID") {
		t.Fatalf("LoadMetadata() error = %v, want invalid run ID", err)
	}
}

func TestCheckpointStore_SaveAndLoadSynthesisCheckpoint(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "test-synth-run"
	checkpoint := SynthesisCheckpoint{
		RunID:       runID,
		SessionName: "test-session",
		LastIndex:   7,
		Error:       "context canceled",
		CreatedAt:   time.Now().UTC(),
	}

	if err := store.SaveSynthesisCheckpoint(runID, checkpoint); err != nil {
		t.Fatalf("SaveSynthesisCheckpoint failed: %v", err)
	}

	loaded, err := store.LoadSynthesisCheckpoint(runID)
	if err != nil {
		t.Fatalf("LoadSynthesisCheckpoint failed: %v", err)
	}

	if loaded.LastIndex != checkpoint.LastIndex {
		t.Errorf("LastIndex = %d, want %d", loaded.LastIndex, checkpoint.LastIndex)
	}
	if loaded.Error != checkpoint.Error {
		t.Errorf("Error = %q, want %q", loaded.Error, checkpoint.Error)
	}
	if loaded.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero")
	}

	t.Logf("TEST: %s - assertion: synthesis checkpoint save/load works", t.Name())
}

func TestCheckpointStore_LoadSynthesisCheckpoint_RejectsRunIDMismatch(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "expected-synth-run"
	runDir := filepath.Join(tmpDir, checkpointDirName, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir failed: %v", err)
	}

	data, err := json.Marshal(SynthesisCheckpoint{RunID: "other-run"})
	if err != nil {
		t.Fatalf("marshal synthesis checkpoint failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, checkpointSynthesisFile), data, 0o644); err != nil {
		t.Fatalf("write synthesis checkpoint failed: %v", err)
	}

	_, err = store.LoadSynthesisCheckpoint(runID)
	if err == nil {
		t.Fatal("LoadSynthesisCheckpoint() error = nil, want run ID mismatch")
	}
	if !strings.Contains(err.Error(), "synthesis checkpoint run ID mismatch") {
		t.Fatalf("LoadSynthesisCheckpoint() error = %v, want run ID mismatch", err)
	}
}

func TestCheckpointStore_SaveMetadata_RejectsSymlinkedRunDir(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	targetDir := filepath.Join(tmpDir, "outside-run")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir target dir failed: %v", err)
	}

	runID := "symlink-run"
	runPath := filepath.Join(tmpDir, checkpointDirName, runID)
	if err := os.Symlink(targetDir, runPath); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	err = store.SaveMetadata(CheckpointMetadata{RunID: runID})
	if err == nil {
		t.Fatal("SaveMetadata() error = nil, want symlink rejection")
	}
	if !strings.Contains(err.Error(), "checkpoint run path must not be a symlink") {
		t.Fatalf("SaveMetadata() error = %v, want run dir symlink rejection", err)
	}
}

func TestCheckpointStore_LoadMetadata_RejectsSymlinkedMetadataFile(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "symlink-meta-run"
	runDir := filepath.Join(tmpDir, checkpointDirName, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir failed: %v", err)
	}

	target := filepath.Join(tmpDir, "outside-meta.json")
	if err := os.WriteFile(target, []byte(`{"run_id":"outside"}`), 0o644); err != nil {
		t.Fatalf("write target metadata failed: %v", err)
	}

	metaPath := filepath.Join(runDir, checkpointMetaFile)
	if err := os.Symlink(target, metaPath); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	_, err = store.LoadMetadata(runID)
	if err == nil {
		t.Fatal("LoadMetadata() error = nil, want symlink rejection")
	}
	if !strings.Contains(err.Error(), "metadata file must not be a symlink") {
		t.Fatalf("LoadMetadata() error = %v, want metadata symlink rejection", err)
	}
}

func TestCheckpointStore_LoadCheckpoint_RejectsSymlinkedCheckpointFile(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "symlink-checkpoint-run"
	runDir := filepath.Join(tmpDir, checkpointDirName, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir failed: %v", err)
	}

	target := filepath.Join(tmpDir, "outside-checkpoint.json")
	if err := os.WriteFile(target, []byte(`{"mode_id":"deductive","status":"done"}`), 0o644); err != nil {
		t.Fatalf("write target checkpoint failed: %v", err)
	}

	cpPath := filepath.Join(runDir, "deductive.json")
	if err := os.Symlink(target, cpPath); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	_, err = store.LoadCheckpoint(runID, "deductive")
	if err == nil {
		t.Fatal("LoadCheckpoint() error = nil, want symlink rejection")
	}
	if !strings.Contains(err.Error(), "checkpoint file must not be a symlink") {
		t.Fatalf("LoadCheckpoint() error = %v, want checkpoint symlink rejection", err)
	}
}

func TestCheckpointStore_LoadCheckpoint_RejectsModeIDMismatch(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "mode-mismatch-run"
	runDir := filepath.Join(tmpDir, checkpointDirName, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir failed: %v", err)
	}

	data, err := json.Marshal(ModeCheckpoint{ModeID: "other-mode", Status: string(AssignmentDone)})
	if err != nil {
		t.Fatalf("marshal checkpoint failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "expected-mode.json"), data, 0o644); err != nil {
		t.Fatalf("write checkpoint failed: %v", err)
	}

	_, err = store.LoadCheckpoint(runID, "expected-mode")
	if err == nil {
		t.Fatal("LoadCheckpoint() error = nil, want mode ID mismatch")
	}
	if !strings.Contains(err.Error(), "checkpoint mode ID mismatch") {
		t.Fatalf("LoadCheckpoint() error = %v, want mode ID mismatch", err)
	}
}

func TestCheckpointStore_LoadCheckpoint_RejectsOutputModeIDMismatch(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "output-mode-mismatch-run"
	runDir := filepath.Join(tmpDir, checkpointDirName, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir failed: %v", err)
	}

	data, err := json.Marshal(ModeCheckpoint{
		ModeID: "expected-mode",
		Output: &ModeOutput{ModeID: "other-mode"},
		Status: string(AssignmentDone),
	})
	if err != nil {
		t.Fatalf("marshal checkpoint failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "expected-mode.json"), data, 0o644); err != nil {
		t.Fatalf("write checkpoint failed: %v", err)
	}

	_, err = store.LoadCheckpoint(runID, "expected-mode")
	if err == nil {
		t.Fatal("LoadCheckpoint() error = nil, want output mode ID mismatch")
	}
	if !strings.Contains(err.Error(), "checkpoint output mode ID mismatch") {
		t.Fatalf("LoadCheckpoint() error = %v, want output mode ID mismatch", err)
	}
}

func logTestStartCheckpoint(t *testing.T, input any) {
	t.Helper()
	t.Logf("TEST: %s - starting with input: %v", t.Name(), input)
}

func logTestResultCheckpoint(t *testing.T, result any) {
	t.Helper()
	t.Logf("TEST: %s - got result: %v", t.Name(), result)
}

func assertNoErrorCheckpoint(t *testing.T, desc string, err error) {
	t.Helper()
	t.Logf("TEST: %s - assertion: %s", t.Name(), desc)
	if err != nil {
		t.Fatalf("%s: %v", desc, err)
	}
}

func assertTrueCheckpoint(t *testing.T, desc string, ok bool) {
	t.Helper()
	t.Logf("TEST: %s - assertion: %s", t.Name(), desc)
	if !ok {
		t.Fatalf("assertion failed: %s", desc)
	}
}

func assertEqualCheckpoint(t *testing.T, desc string, got, want any) {
	t.Helper()
	t.Logf("TEST: %s - assertion: %s", t.Name(), desc)
	if got != want {
		t.Fatalf("%s: got %v want %v", desc, got, want)
	}
}

func TestCheckpointStore_LoadCheckpoint_NotFound(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	_, err = store.LoadCheckpoint("nonexistent-run", "nonexistent-mode")
	if err == nil {
		t.Error("expected error for nonexistent checkpoint")
	}
	if !os.IsNotExist(err) {
		t.Errorf("expected os.ErrNotExist, got %v", err)
	}

	t.Logf("TEST: %s - assertion: not found error returned", t.Name())
}

func TestCheckpointStore_ListRuns(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	// Create multiple runs
	runs := []string{"run-a", "run-b", "run-c"}
	for _, runID := range runs {
		meta := CheckpointMetadata{
			RunID:     runID,
			CreatedAt: time.Now().UTC(),
		}
		if err := store.SaveMetadata(meta); err != nil {
			t.Fatalf("SaveMetadata failed for %s: %v", runID, err)
		}
	}

	// List runs
	listed, err := store.ListRuns()
	if err != nil {
		t.Fatalf("ListRuns failed: %v", err)
	}

	if len(listed) != len(runs) {
		t.Errorf("got %d runs, want %d", len(listed), len(runs))
	}

	t.Logf("TEST: %s - assertion: all runs listed", t.Name())
}

func TestCheckpointStore_DeleteRun(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "test-run-delete"
	meta := CheckpointMetadata{
		RunID:     runID,
		CreatedAt: time.Now().UTC(),
	}
	if err := store.SaveMetadata(meta); err != nil {
		t.Fatalf("SaveMetadata failed: %v", err)
	}

	// Verify exists
	if !store.RunExists(runID) {
		t.Error("run should exist before delete")
	}

	// Delete
	if err := store.DeleteRun(runID); err != nil {
		t.Fatalf("DeleteRun failed: %v", err)
	}

	// Verify gone
	if store.RunExists(runID) {
		t.Error("run should not exist after delete")
	}

	t.Logf("TEST: %s - assertion: run deleted successfully", t.Name())
}

func TestCheckpointStore_RunExists(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	if store.RunExists("nonexistent") {
		t.Error("nonexistent run should return false")
	}

	runID := "existing-run"
	meta := CheckpointMetadata{RunID: runID}
	if err := store.SaveMetadata(meta); err != nil {
		t.Fatalf("SaveMetadata failed: %v", err)
	}

	if !store.RunExists(runID) {
		t.Error("existing run should return true")
	}

	t.Logf("TEST: %s - assertion: RunExists works correctly", t.Name())
}

func TestCheckpointStore_RunExists_RejectsSymlinkedRunDir(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	targetDir := filepath.Join(tmpDir, "outside-run")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir target dir failed: %v", err)
	}

	runID := "symlink-run"
	runPath := filepath.Join(tmpDir, checkpointDirName, runID)
	if err := os.Symlink(targetDir, runPath); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	if store.RunExists(runID) {
		t.Fatal("RunExists() = true, want false for symlinked run dir")
	}
}

func TestCheckpointStore_CleanOld(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	// Create old run - manually write metadata with old timestamp
	oldRunID := "old-run"
	oldRunDir := filepath.Join(tmpDir, checkpointDirName, oldRunID)
	if err := os.MkdirAll(oldRunDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	oldMeta := CheckpointMetadata{
		RunID:     oldRunID,
		CreatedAt: time.Now().Add(-48 * time.Hour),
		UpdatedAt: time.Now().Add(-48 * time.Hour),
	}
	oldData, _ := json.Marshal(oldMeta)
	if err := os.WriteFile(filepath.Join(oldRunDir, checkpointMetaFile), oldData, 0o644); err != nil {
		t.Fatalf("write old metadata failed: %v", err)
	}

	// Create new run
	newMeta := CheckpointMetadata{
		RunID:     "new-run",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.SaveMetadata(newMeta); err != nil {
		t.Fatalf("SaveMetadata failed: %v", err)
	}

	// Clean runs older than 24 hours
	removed, err := store.CleanOld(24 * time.Hour)
	if err != nil {
		t.Fatalf("CleanOld failed: %v", err)
	}

	if removed != 1 {
		t.Errorf("CleanOld removed %d, want 1", removed)
	}

	// Old run should be gone
	if store.RunExists(oldRunID) {
		t.Error("old run should be removed")
	}

	// New run should still exist
	if !store.RunExists("new-run") {
		t.Error("new run should still exist")
	}
}

func TestCheckpointStore_CleanOld_NilStore(t *testing.T) {
	var store *CheckpointStore
	_, err := store.CleanOld(24 * time.Hour)
	if err == nil {
		t.Error("CleanOld on nil should return error")
	}
}
