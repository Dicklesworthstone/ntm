package checkpoint

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckpoint_ValidateSchema(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		checkpoint Checkpoint
		wantValid  bool
		wantErrors int
	}{
		{
			name: "valid checkpoint",
			checkpoint: Checkpoint{
				Version:     CurrentVersion,
				ID:          "20251210-143052-test",
				SessionName: "test-session",
				CreatedAt:   time.Now(),
				Session: SessionState{
					Panes: []PaneState{{ID: "%0", Index: 0}},
				},
				PaneCount: 1,
			},
			wantValid:  true,
			wantErrors: 0,
		},
		{
			name: "missing ID",
			checkpoint: Checkpoint{
				Version:     CurrentVersion,
				SessionName: "test-session",
				CreatedAt:   time.Now(),
			},
			wantValid:  false,
			wantErrors: 1,
		},
		{
			name: "missing session name",
			checkpoint: Checkpoint{
				Version:   CurrentVersion,
				ID:        "20251210-143052-test",
				CreatedAt: time.Now(),
			},
			wantValid:  false,
			wantErrors: 1,
		},
		{
			name: "invalid version - too low",
			checkpoint: Checkpoint{
				Version:     0,
				ID:          "20251210-143052-test",
				SessionName: "test-session",
				CreatedAt:   time.Now(),
			},
			wantValid:  false,
			wantErrors: 1,
		},
		{
			name: "invalid version - too high",
			checkpoint: Checkpoint{
				Version:     CurrentVersion + 10,
				ID:          "20251210-143052-test",
				SessionName: "test-session",
				CreatedAt:   time.Now(),
			},
			wantValid:  false,
			wantErrors: 1,
		},
		{
			name: "missing timestamp",
			checkpoint: Checkpoint{
				Version:     CurrentVersion,
				ID:          "20251210-143052-test",
				SessionName: "test-session",
			},
			wantValid:  false,
			wantErrors: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &IntegrityResult{
				SchemaValid: true,
				Errors:      []string{},
				Warnings:    []string{},
				Details:     make(map[string]string),
			}
			tt.checkpoint.validateSchema(result)

			if result.SchemaValid != tt.wantValid {
				t.Errorf("SchemaValid = %v, want %v", result.SchemaValid, tt.wantValid)
			}
			if len(result.Errors) != tt.wantErrors {
				t.Errorf("len(Errors) = %d, want %d; errors: %v", len(result.Errors), tt.wantErrors, result.Errors)
			}
		})
	}
}

func TestCheckpoint_ValidateConsistency(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		checkpoint Checkpoint
		wantValid  bool
		wantErrors int
	}{
		{
			name: "consistent pane count",
			checkpoint: Checkpoint{
				Session: SessionState{
					Panes:           []PaneState{{ID: "%0", Index: 0}, {ID: "%1", Index: 1}},
					ActivePaneIndex: 0,
				},
				PaneCount: 2,
			},
			wantValid:  true,
			wantErrors: 0,
		},
		{
			name: "inconsistent pane count",
			checkpoint: Checkpoint{
				Session: SessionState{
					Panes: []PaneState{{ID: "%0", Index: 0}},
				},
				PaneCount: 5, // Wrong!
			},
			wantValid:  false,
			wantErrors: 1,
		},
		{
			name: "invalid active pane index - negative",
			checkpoint: Checkpoint{
				Session: SessionState{
					Panes:           []PaneState{{ID: "%0", Index: 0}},
					ActivePaneIndex: -1,
				},
				PaneCount: 1,
			},
			wantValid:  false,
			wantErrors: 1,
		},
		{
			name: "invalid active pane index - too high",
			checkpoint: Checkpoint{
				Session: SessionState{
					Panes:           []PaneState{{ID: "%0", Index: 0}},
					ActivePaneIndex: 5,
				},
				PaneCount: 1,
			},
			wantValid:  false,
			wantErrors: 1,
		},
		{
			name: "window layout references missing window",
			checkpoint: Checkpoint{
				Session: SessionState{
					Panes: []PaneState{
						{ID: "%0", Index: 0, WindowIndex: 0},
						{ID: "%1", Index: 0, WindowIndex: 1},
					},
					WindowLayouts: []WindowLayoutState{
						{WindowIndex: 0, Layout: "even-horizontal"},
						{WindowIndex: 9, Layout: "main-vertical"},
					},
				},
				PaneCount: 2,
			},
			wantValid:  false,
			wantErrors: 2,
		},
		{
			name: "missing window layout for existing window",
			checkpoint: Checkpoint{
				Session: SessionState{
					Panes: []PaneState{
						{ID: "%0", Index: 0, WindowIndex: 0},
						{ID: "%1", Index: 0, WindowIndex: 1},
					},
					WindowLayouts: []WindowLayoutState{
						{WindowIndex: 0, Layout: "even-horizontal"},
					},
				},
				PaneCount: 2,
			},
			wantValid:  false,
			wantErrors: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &IntegrityResult{
				ConsistencyValid: true,
				Errors:           []string{},
				Warnings:         []string{},
				Details:          make(map[string]string),
			}
			tt.checkpoint.validateConsistency(result)

			if result.ConsistencyValid != tt.wantValid {
				t.Errorf("ConsistencyValid = %v, want %v", result.ConsistencyValid, tt.wantValid)
			}
			if len(result.Errors) != tt.wantErrors {
				t.Errorf("len(Errors) = %d, want %d; errors: %v", len(result.Errors), tt.wantErrors, result.Errors)
			}
		})
	}
}

func TestCheckpoint_ValidateConsistency_WarnsForLegacyMultiWindowLayout(t *testing.T) {
	t.Parallel()
	cp := Checkpoint{
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%0", Index: 0, WindowIndex: 0},
				{ID: "%1", Index: 0, WindowIndex: 1},
			},
			Layout: "even-horizontal",
		},
		PaneCount: 2,
	}

	result := &IntegrityResult{
		ConsistencyValid: true,
		Errors:           []string{},
		Warnings:         []string{},
		Details:          make(map[string]string),
	}
	cp.validateConsistency(result)

	if !result.ConsistencyValid {
		t.Fatalf("ConsistencyValid = false, want true (warnings=%v errors=%v)", result.Warnings, result.Errors)
	}
	found := false
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "legacy single layout string") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected legacy-layout warning, got %v", result.Warnings)
	}
}

func TestCheckpoint_CheckFiles(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "ntm-integrity-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)

	// Create a valid checkpoint with all files
	sessionName := "test-session"
	checkpointID := "20251210-143052-valid"

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{{ID: "%0", Index: 0}}},
		PaneCount:   1,
	}

	// Save the checkpoint (creates directories and metadata)
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Failed to save checkpoint: %v", err)
	}

	// Create the scrollback file
	panesDir := storage.PanesDirPath(sessionName, checkpointID)
	scrollbackPath := filepath.Join(panesDir, "pane__0.txt")
	if err := os.WriteFile(scrollbackPath, []byte("test content"), 0644); err != nil {
		t.Fatalf("Failed to create scrollback file: %v", err)
	}
	cp.Session.Panes[0].ScrollbackFile = "panes/pane__0.txt"
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Failed to save checkpoint with scrollback reference: %v", err)
	}

	t.Run("all files present", func(t *testing.T) {
		result := &IntegrityResult{
			FilesPresent: true,
			Errors:       []string{},
			Details:      make(map[string]string),
		}
		dir := storage.CheckpointDir(sessionName, checkpointID)
		cp.checkFiles(storage, dir, result)

		if !result.FilesPresent {
			t.Errorf("FilesPresent = false, want true; errors: %v", result.Errors)
		}
	})

	t.Run("missing scrollback file", func(t *testing.T) {
		// Remove the scrollback file
		os.Remove(scrollbackPath)

		result := &IntegrityResult{
			FilesPresent: true,
			Errors:       []string{},
			Details:      make(map[string]string),
		}
		dir := storage.CheckpointDir(sessionName, checkpointID)
		cp.checkFiles(storage, dir, result)

		if result.FilesPresent {
			t.Errorf("FilesPresent = true, want false")
		}
		if len(result.Errors) == 0 {
			t.Error("Expected error for missing scrollback file")
		}
	})
}

func TestCheckpoint_Verify_RejectsSymlinkArtifactReference(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "test-session"
	checkpointID := "20251210-143052-symlink"
	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%0", Index: 0, ScrollbackFile: "panes/pane__0.txt"},
			},
		},
		PaneCount: 1,
	}

	cpDir := storage.CheckpointDir(sessionName, checkpointID)
	if err := os.MkdirAll(filepath.Join(cpDir, PanesDir), 0755); err != nil {
		t.Fatalf("MkdirAll() failed: %v", err)
	}
	if err := writeJSON(filepath.Join(cpDir, MetadataFile), cp); err != nil {
		t.Fatalf("write metadata failed: %v", err)
	}
	if err := writeJSON(filepath.Join(cpDir, SessionFile), cp.Session); err != nil {
		t.Fatalf("write session failed: %v", err)
	}

	outsidePath := filepath.Join(tmpDir, "outside.txt")
	if err := os.WriteFile(outsidePath, []byte("secret"), 0600); err != nil {
		t.Fatalf("WriteFile() failed: %v", err)
	}
	if err := os.Symlink(outsidePath, filepath.Join(cpDir, PanesDir, "pane__0.txt")); err != nil {
		t.Fatalf("Symlink() failed: %v", err)
	}

	result := VerifyStoredCheckpoint(storage, sessionName, checkpointID)
	if result.FilesPresent {
		t.Fatalf("FilesPresent = true, want false; errors: %v", result.Errors)
	}
	if len(result.Errors) == 0 || !strings.Contains(strings.Join(result.Errors, "\n"), "must not be a symlink") {
		t.Fatalf("VerifyStoredCheckpoint() errors = %v, want symlink rejection", result.Errors)
	}
}

func TestCheckpoint_Verify_RejectsSymlinkCheckpointDir(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "test-session"
	checkpointID := "20251210-143052-symlink-dir"
	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
	}

	sessionDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatalf("MkdirAll(session dir) failed: %v", err)
	}

	if err := os.Symlink(t.TempDir(), filepath.Join(sessionDir, checkpointID)); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	result := VerifyStoredCheckpoint(storage, cp.SessionName, cp.ID)
	if result.FilesPresent {
		t.Fatalf("FilesPresent = true, want false; errors: %v", result.Errors)
	}
	if len(result.Errors) == 0 || !strings.Contains(strings.Join(result.Errors, "\n"), "checkpoint path must not be a symlink") {
		t.Fatalf("VerifyStoredCheckpoint() errors = %v, want checkpoint symlink rejection", result.Errors)
	}
}

func TestVerifyStoredCheckpoint_ReportsSessionStateMismatch(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "test-session"
	checkpointID := "20251210-143052-verify-mismatch"

	cpDir := storage.CheckpointDir(sessionName, checkpointID)
	if err := os.MkdirAll(cpDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		SessionName: sessionName,
		WorkingDir:  tmpDir,
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{{Index: 0, ID: "%0", Title: "metadata"}}},
		PaneCount:   1,
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

	result := VerifyStoredCheckpoint(storage, sessionName, checkpointID)
	if result.Valid {
		t.Fatalf("Valid = true, want false")
	}
	if result.ConsistencyValid {
		t.Fatalf("ConsistencyValid = true, want false")
	}
	if len(result.Errors) == 0 || !containsSubstr(strings.Join(result.Errors, "\n"), "session state mismatch") {
		t.Fatalf("VerifyStoredCheckpoint() errors = %v, want session state mismatch", result.Errors)
	}
}

func TestCheckpoint_FullVerify(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "ntm-verify-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)

	sessionName := "test-session"
	checkpointID := "20251210-143052-full"

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		Name:        "test-checkpoint",
		SessionName: sessionName,
		WorkingDir:  "/tmp/test",
		CreatedAt:   time.Now(),
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%0", Index: 0, Width: 80, Height: 24},
			},
			ActivePaneIndex: 0,
		},
		PaneCount: 1,
	}

	// Save the checkpoint
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Failed to save checkpoint: %v", err)
	}

	result := VerifyStoredCheckpoint(storage, sessionName, checkpointID)

	if !result.Valid {
		t.Errorf("Valid = false, want true; errors: %v", result.Errors)
	}
	if !result.SchemaValid {
		t.Errorf("SchemaValid = false, want true")
	}
	if !result.FilesPresent {
		t.Errorf("FilesPresent = false, want true")
	}
	if !result.ConsistencyValid {
		t.Errorf("ConsistencyValid = false, want true")
	}
}

// The verify path refuses symlinked canonical files: a checkpoint must not
// read metadata or session state from outside its own directory.
func TestVerifyStoredCheckpoint_RejectsSymlinkCanonicalFiles(t *testing.T) {
	t.Parallel()
	storage := NewStorageWithDir(t.TempDir())
	sessionName := "test-session"

	for _, symlinked := range []string{MetadataFile, SessionFile} {
		t.Run(symlinked, func(t *testing.T) {
			t.Parallel()
			checkpointID := "20251210-143052-verify-symlink-" + strings.TrimSuffix(symlinked, ".json")
			cp := &Checkpoint{
				Version:     CurrentVersion,
				ID:          checkpointID,
				SessionName: sessionName,
				CreatedAt:   time.Now(),
			}
			cpDir := storage.CheckpointDir(sessionName, checkpointID)
			if err := os.MkdirAll(cpDir, 0755); err != nil {
				t.Fatalf("MkdirAll() failed: %v", err)
			}
			outsidePath := filepath.Join(t.TempDir(), "outside-"+symlinked)
			var outside any = cp
			if symlinked == SessionFile {
				outside = cp.Session
			}
			if err := writeJSON(outsidePath, outside); err != nil {
				t.Fatalf("writeJSON(outside) failed: %v", err)
			}
			if symlinked == MetadataFile {
				if err := writeJSON(filepath.Join(cpDir, SessionFile), cp.Session); err != nil {
					t.Fatalf("writeJSON(session) failed: %v", err)
				}
			} else if err := writeJSON(filepath.Join(cpDir, MetadataFile), cp); err != nil {
				t.Fatalf("writeJSON(metadata) failed: %v", err)
			}
			if err := os.Symlink(outsidePath, filepath.Join(cpDir, symlinked)); err != nil {
				t.Fatalf("Symlink() failed: %v", err)
			}

			result := VerifyStoredCheckpoint(storage, sessionName, checkpointID)
			if result.Valid || result.FilesPresent {
				t.Fatalf("valid=%v files_present=%v, want a symlinked %s refused", result.Valid, result.FilesPresent, symlinked)
			}
			if !strings.Contains(strings.Join(result.Errors, "\n"), "must not be a symlink") {
				t.Fatalf("errors = %v, want symlink rejection", result.Errors)
			}
		})
	}
}

func TestVerifyAll(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "ntm-verifyall-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)
	sessionName := "test-session"

	// Create multiple checkpoints
	for i := 0; i < 3; i++ {
		cp := &Checkpoint{
			Version:     CurrentVersion,
			ID:          GenerateID("test"),
			SessionName: sessionName,
			CreatedAt:   time.Now(),
			Session: SessionState{
				Panes: []PaneState{{ID: "%0", Index: 0}},
			},
			PaneCount: 1,
		}
		if err := storage.Save(cp); err != nil {
			t.Fatalf("Failed to save checkpoint %d: %v", i, err)
		}
	}

	results, err := VerifyAll(storage, sessionName)
	if err != nil {
		t.Fatalf("VerifyAll failed: %v", err)
	}

	if len(results) != 3 {
		t.Errorf("Expected 3 results, got %d", len(results))
	}

	// All should be valid
	for id, result := range results {
		if !result.Valid {
			t.Errorf("Checkpoint %s: Valid = false, want true", id)
		}
	}
}

func TestVerifyAll_IgnoresIncrementalContainerDirectory(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "ntm-verifyall-incremental-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)
	sessionName := "test-session"

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          GenerateID("valid"),
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session: SessionState{
			Panes: []PaneState{{ID: "%0", Index: 0}},
		},
		PaneCount: 1,
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Failed to save valid checkpoint: %v", err)
	}

	incDir := filepath.Join(tmpDir, sessionName, "incremental", "inc-001")
	if err := os.MkdirAll(incDir, 0755); err != nil {
		t.Fatalf("Failed to create incremental directory: %v", err)
	}

	results, err := VerifyAll(storage, sessionName)
	if err != nil {
		t.Fatalf("VerifyAll failed: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("Expected 1 checkpoint result, got %d: %#v", len(results), results)
	}
	if _, ok := results["incremental"]; ok {
		t.Fatalf("VerifyAll incorrectly treated incremental container as a checkpoint: %#v", results["incremental"])
	}
	if result, ok := results[cp.ID]; !ok {
		t.Fatalf("Missing valid checkpoint result for %s", cp.ID)
	} else if !result.Valid {
		t.Fatalf("Valid checkpoint reported invalid: %#v", result.Errors)
	}
}

func TestVerifyAll_ReportsUnreadableCheckpoint(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "ntm-verifyall-invalid-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)
	sessionName := "test-session"

	valid := &Checkpoint{
		Version:     CurrentVersion,
		ID:          GenerateID("valid"),
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session: SessionState{
			Panes: []PaneState{{ID: "%0", Index: 0}},
		},
		PaneCount: 1,
	}
	if err := storage.Save(valid); err != nil {
		t.Fatalf("Failed to save valid checkpoint: %v", err)
	}

	brokenID := "broken-checkpoint"
	brokenDir := filepath.Join(tmpDir, sessionName, brokenID)
	if err := os.MkdirAll(brokenDir, 0755); err != nil {
		t.Fatalf("Failed to create broken checkpoint dir: %v", err)
	}
	broken := &Checkpoint{
		Version:     CurrentVersion,
		ID:          brokenID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session: SessionState{
			Panes: []PaneState{{ID: "%0", Index: 0}},
		},
		PaneCount: 1,
	}
	if err := writeJSON(filepath.Join(brokenDir, MetadataFile), broken); err != nil {
		t.Fatalf("Failed to write broken metadata: %v", err)
	}

	results, err := VerifyAll(storage, sessionName)
	if err != nil {
		t.Fatalf("VerifyAll failed: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(results))
	}
	if result, ok := results[valid.ID]; !ok {
		t.Fatalf("Missing valid checkpoint result for %s", valid.ID)
	} else if !result.Valid {
		t.Fatalf("Valid checkpoint reported invalid: %#v", result.Errors)
	}
	result, ok := results[brokenID]
	if !ok {
		t.Fatalf("Missing broken checkpoint result for %s", brokenID)
	}
	if result.Valid {
		t.Fatalf("Broken checkpoint unexpectedly reported valid")
	}
	if len(result.Errors) == 0 {
		t.Fatalf("Broken checkpoint errors = %#v, want file verification mentioning %s", result.Errors, SessionFile)
	}
	if !containsSubstr(strings.Join(result.Errors, "\n"), SessionFile) {
		t.Fatalf("Broken checkpoint errors = %#v, want missing %s", result.Errors, SessionFile)
	}
}

func TestVerifyAll_ReportsSymlinkCheckpointEntry(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "ntm-verifyall-symlink-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)
	sessionName := "test-session"

	valid := &Checkpoint{
		Version:     CurrentVersion,
		ID:          GenerateID("valid"),
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session: SessionState{
			Panes: []PaneState{{ID: "%0", Index: 0}},
		},
		PaneCount: 1,
	}
	if err := storage.Save(valid); err != nil {
		t.Fatalf("Failed to save valid checkpoint: %v", err)
	}

	brokenID := "symlink-checkpoint"
	outsideDir := t.TempDir()
	if err := os.Symlink(outsideDir, filepath.Join(tmpDir, sessionName, brokenID)); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	results, err := VerifyAll(storage, sessionName)
	if err != nil {
		t.Fatalf("VerifyAll failed: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(results))
	}
	result, ok := results[brokenID]
	if !ok {
		t.Fatalf("Missing symlink checkpoint result for %s", brokenID)
	}
	if result.Valid {
		t.Fatal("Symlink checkpoint unexpectedly reported valid")
	}
	if len(result.Errors) == 0 {
		t.Fatal("Symlink checkpoint errors = nil, want checkpoint path error")
	}
	if !strings.Contains(strings.Join(result.Errors, "\n"), "checkpoint path must not be a symlink") {
		t.Fatalf("Symlink checkpoint errors = %#v, want symlink rejection", result.Errors)
	}
}
