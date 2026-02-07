package checkpoint

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckpoint_ValidateSchema(t *testing.T) {
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

func TestCheckpoint_CheckFiles(t *testing.T) {
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
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%0", Index: 0, ScrollbackFile: "panes/pane__0.txt"},
			},
		},
		PaneCount: 1,
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

func TestCheckpoint_FullVerify(t *testing.T) {
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

	result := cp.Verify(storage)

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

func TestCheckpoint_GenerateManifest(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-manifest-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)

	sessionName := "test-session"
	checkpointID := "20251210-143052-manifest"

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%0", Index: 0},
			},
		},
		PaneCount: 1,
	}

	// Save the checkpoint
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Failed to save checkpoint: %v", err)
	}

	manifest, err := cp.GenerateManifest(storage)
	if err != nil {
		t.Fatalf("GenerateManifest failed: %v", err)
	}

	// Should have at least metadata.json and session.json
	if len(manifest.Files) < 2 {
		t.Errorf("Expected at least 2 files in manifest, got %d", len(manifest.Files))
	}

	if _, ok := manifest.Files[MetadataFile]; !ok {
		t.Error("Missing metadata.json in manifest")
	}
	if _, ok := manifest.Files[SessionFile]; !ok {
		t.Error("Missing session.json in manifest")
	}

	// Verify the hashes are valid hex strings
	for path, hash := range manifest.Files {
		if len(hash) != 64 { // SHA256 = 32 bytes = 64 hex chars
			t.Errorf("Invalid hash length for %s: %d", path, len(hash))
		}
	}
}

func TestCheckpoint_GenerateManifest_WithScrollbackAndPatch(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-manifest-full-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)

	sessionName := "test-session"
	checkpointID := "20251210-143052-manifest-full"

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%0", Index: 0, ScrollbackFile: "panes/pane__0.txt"},
				{ID: "%1", Index: 1, ScrollbackFile: "panes/pane__1.txt"},
			},
		},
		Git: GitState{
			PatchFile: "changes.patch",
		},
		PaneCount: 2,
	}

	// Save the checkpoint
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Failed to save checkpoint: %v", err)
	}

	dir := storage.CheckpointDir(sessionName, checkpointID)

	// Create scrollback files
	panesDir := filepath.Join(dir, "panes")
	if err := os.MkdirAll(panesDir, 0755); err != nil {
		t.Fatalf("Failed to create panes dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "panes/pane__0.txt"), []byte("scrollback 0"), 0644); err != nil {
		t.Fatalf("Failed to write scrollback 0: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "panes/pane__1.txt"), []byte("scrollback 1"), 0644); err != nil {
		t.Fatalf("Failed to write scrollback 1: %v", err)
	}

	// Create git patch file
	if err := os.WriteFile(filepath.Join(dir, "changes.patch"), []byte("diff --git a/foo"), 0644); err != nil {
		t.Fatalf("Failed to write patch: %v", err)
	}

	manifest, err := cp.GenerateManifest(storage)
	if err != nil {
		t.Fatalf("GenerateManifest failed: %v", err)
	}

	// Should have metadata.json, session.json, 2 scrollback files, 1 patch
	if len(manifest.Files) < 5 {
		t.Errorf("Expected at least 5 files in manifest, got %d: %v", len(manifest.Files), manifest.Files)
	}

	if _, ok := manifest.Files["panes/pane__0.txt"]; !ok {
		t.Error("Missing panes/pane__0.txt in manifest")
	}
	if _, ok := manifest.Files["panes/pane__1.txt"]; !ok {
		t.Error("Missing panes/pane__1.txt in manifest")
	}
	if _, ok := manifest.Files["changes.patch"]; !ok {
		t.Error("Missing changes.patch in manifest")
	}
}

func TestCheckpoint_GenerateManifest_NoPanes(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-manifest-nopanes-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)

	sessionName := "test-session"
	checkpointID := "20251210-143052-manifest-nopanes"

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
	}

	if err := storage.Save(cp); err != nil {
		t.Fatalf("Failed to save checkpoint: %v", err)
	}

	manifest, err := cp.GenerateManifest(storage)
	if err != nil {
		t.Fatalf("GenerateManifest failed: %v", err)
	}

	// Should only have metadata and session files
	if len(manifest.Files) > 2 {
		t.Errorf("Expected at most 2 files in manifest for no panes, got %d", len(manifest.Files))
	}
}

func TestCheckpoint_GenerateManifest_EmptyScrollbackFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-manifest-empty-scroll")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)

	sessionName := "test-session"
	checkpointID := "20251210-143052-manifest-empty"

	// Pane with empty ScrollbackFile string - should be skipped
	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%0", Index: 0, ScrollbackFile: ""}, // empty
			},
		},
		PaneCount: 1,
	}

	if err := storage.Save(cp); err != nil {
		t.Fatalf("Failed to save checkpoint: %v", err)
	}

	manifest, err := cp.GenerateManifest(storage)
	if err != nil {
		t.Fatalf("GenerateManifest failed: %v", err)
	}

	// Should only have metadata and session
	if len(manifest.Files) > 2 {
		t.Errorf("Expected at most 2 files for empty scrollback, got %d", len(manifest.Files))
	}
}

func TestCheckpoint_VerifyManifest(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-verify-manifest-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)

	sessionName := "test-session"
	checkpointID := "20251210-143052-verify-manifest"

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session: SessionState{
			Panes: []PaneState{{ID: "%0", Index: 0}},
		},
		PaneCount: 1,
	}

	// Save the checkpoint
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Failed to save checkpoint: %v", err)
	}

	// Generate manifest
	manifest, err := cp.GenerateManifest(storage)
	if err != nil {
		t.Fatalf("GenerateManifest failed: %v", err)
	}

	t.Run("valid manifest", func(t *testing.T) {
		result := cp.VerifyManifest(storage, manifest)
		if !result.Valid {
			t.Errorf("Valid = false, want true; errors: %v", result.Errors)
		}
		if !result.ChecksumsValid {
			t.Error("ChecksumsValid = false, want true")
		}
	})

	t.Run("tampered file", func(t *testing.T) {
		// Modify a file after generating manifest
		metaPath := filepath.Join(storage.CheckpointDir(sessionName, checkpointID), MetadataFile)
		if err := os.WriteFile(metaPath, []byte("tampered content"), 0644); err != nil {
			t.Fatalf("Failed to tamper file: %v", err)
		}

		result := cp.VerifyManifest(storage, manifest)
		if result.Valid {
			t.Error("Valid = true, want false for tampered file")
		}
		if result.ChecksumsValid {
			t.Error("ChecksumsValid = true, want false for tampered file")
		}
	})
}

func TestCheckpoint_QuickCheck(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-quickcheck-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)

	sessionName := "test-session"
	checkpointID := "20251210-143052-quickcheck"

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
	}

	// Save the checkpoint
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Failed to save checkpoint: %v", err)
	}

	// QuickCheck should pass
	if err := cp.QuickCheck(storage); err != nil {
		t.Errorf("QuickCheck failed: %v", err)
	}

	// QuickCheck with invalid version
	cp.Version = 0
	if err := cp.QuickCheck(storage); err == nil {
		t.Error("QuickCheck should fail with version 0")
	}
}

func TestVerifyAll(t *testing.T) {
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
