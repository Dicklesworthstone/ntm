package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestGenerateID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantLen  int
		contains string
	}{
		{
			name:    "simple name",
			input:   "backup",
			wantLen: 31, // YYYYMMDD-HHMMSS.mmm-XXXX-backup (with random suffix)
		},
		{
			name:    "empty name",
			input:   "",
			wantLen: 24, // YYYYMMDD-HHMMSS.mmm-XXXX (with random suffix)
		},
		{
			name:     "name with spaces",
			input:    "my backup",
			contains: "my_backup",
		},
		{
			name:     "name with slashes",
			input:    "test/backup",
			contains: "test-backup",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := GenerateID(tt.input)

			if tt.wantLen > 0 && len(id) != tt.wantLen {
				t.Errorf("GenerateID(%q) length = %d, want %d", tt.input, len(id), tt.wantLen)
			}

			if tt.contains != "" && !containsSubstring(id, tt.contains) {
				t.Errorf("GenerateID(%q) = %q, want to contain %q", tt.input, id, tt.contains)
			}
		})
	}
}

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"with spaces", "with_spaces"},
		{"with/slash", "with-slash"},
		{"with\\backslash", "with-backslash"},
		{"with:colon", "with-colon"},
		{"a*b?c<d>e|f", "a-b-c-d-e-f"},
		{"  trimmed  ", "trimmed"},
		{"verylongnamethatexceedsfiftycharacterssothatshouldbetruncated", "verylongnamethatexceedsfiftycharacterssothatshould"},
		// 49 'a's (49 bytes) + '€' (3 bytes) = 52 bytes. Cutting at 50 splits the Euro sign.
		// Result should be 49 'a's (length 49), dropping the partial char.
		{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa€", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeName(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestStorage_SaveAndLoad(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "ntm-checkpoint-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)

	// Create a test checkpoint
	cp := &Checkpoint{
		ID:          "20251210-120000-test",
		Name:        "test",
		Description: "Test checkpoint",
		SessionName: "myproject",
		WorkingDir:  "/tmp/myproject",
		CreatedAt:   time.Now(),
		Session: SessionState{
			Panes: []PaneState{
				{
					Index:     0,
					ID:        "%0",
					Title:     "myproject__cc_1",
					AgentType: "cc",
					Width:     80,
					Height:    24,
				},
			},
			ActivePaneIndex: 0,
		},
		Git: GitState{
			Branch:  "main",
			Commit:  "abc123",
			IsDirty: false,
		},
		PaneCount: 1,
	}

	// Save checkpoint
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	// Verify directory was created
	checkpointDir := storage.CheckpointDir(cp.SessionName, cp.ID)
	if _, err := os.Stat(checkpointDir); os.IsNotExist(err) {
		t.Errorf("Checkpoint directory was not created: %s", checkpointDir)
	}

	// Load checkpoint
	loaded, err := storage.Load(cp.SessionName, cp.ID)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	// Verify fields
	if loaded.ID != cp.ID {
		t.Errorf("ID = %q, want %q", loaded.ID, cp.ID)
	}
	if loaded.Name != cp.Name {
		t.Errorf("Name = %q, want %q", loaded.Name, cp.Name)
	}
	if loaded.SessionName != cp.SessionName {
		t.Errorf("SessionName = %q, want %q", loaded.SessionName, cp.SessionName)
	}
	if loaded.Git.Branch != cp.Git.Branch {
		t.Errorf("Git.Branch = %q, want %q", loaded.Git.Branch, cp.Git.Branch)
	}
	if len(loaded.Session.Panes) != 1 {
		t.Errorf("len(Session.Panes) = %d, want 1", len(loaded.Session.Panes))
	}
}

func TestStorage_List(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-checkpoint-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"

	// Create multiple checkpoints
	times := []time.Time{
		time.Date(2025, 12, 10, 10, 0, 0, 0, time.UTC),
		time.Date(2025, 12, 10, 11, 0, 0, 0, time.UTC),
		time.Date(2025, 12, 10, 12, 0, 0, 0, time.UTC),
	}

	for i, cpTime := range times {
		cp := &Checkpoint{
			ID:          GenerateID("backup" + string(rune('A'+i))),
			Name:        "backup" + string(rune('A'+i)),
			SessionName: sessionName,
			CreatedAt:   cpTime,
			Session:     SessionState{Panes: []PaneState{}},
		}
		if err := storage.Save(cp); err != nil {
			t.Fatalf("Save() failed: %v", err)
		}
	}

	// List checkpoints
	list, err := storage.List(sessionName)
	if err != nil {
		t.Fatalf("List() failed: %v", err)
	}

	if len(list) != 3 {
		t.Errorf("len(list) = %d, want 3", len(list))
	}

	// Verify sorted by newest first
	for i := 1; i < len(list); i++ {
		if list[i].CreatedAt.After(list[i-1].CreatedAt) {
			t.Errorf("List not sorted by newest first: %v after %v", list[i].CreatedAt, list[i-1].CreatedAt)
		}
	}
}

func TestStorage_SaveScrollback(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-checkpoint-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)
	sessionName := "myproject"
	checkpointID := "20251210-120000-test"

	// Create checkpoint directory first
	cp := &Checkpoint{
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session: SessionState{Panes: []PaneState{
			{Index: 0, ID: "%0"},
			{Index: 1, ID: "%1"},
			{Index: 2, ID: "%2"},
		}},
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	// Save scrollback
	content := "Line 1\nLine 2\nLine 3\n"
	relativePath, err := storage.SaveScrollback(sessionName, checkpointID, "%0", content)
	if err != nil {
		t.Fatalf("SaveScrollback() failed: %v", err)
	}

	if relativePath != "panes/pane__0.txt" {
		t.Errorf("relativePath = %q, want %q", relativePath, "panes/pane__0.txt")
	}

	// Load scrollback
	loaded, err := storage.LoadScrollback(sessionName, checkpointID, "%0")
	if err != nil {
		t.Fatalf("LoadScrollback() failed: %v", err)
	}

	if loaded != content {
		t.Errorf("loaded content = %q, want %q", loaded, content)
	}
}

func TestStorage_SaveScrollback_RejectsInvalidIdentifiers(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	tests := []struct {
		name        string
		sessionName string
		checkpoint  string
		wantErr     string
	}{
		{
			name:        "invalid session",
			sessionName: "../escape",
			checkpoint:  "valid-id",
			wantErr:     "invalid session name",
		},
		{
			name:        "invalid checkpoint",
			sessionName: "valid_session",
			checkpoint:  "../escape",
			wantErr:     "invalid checkpoint ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := storage.SaveScrollback(tt.sessionName, tt.checkpoint, "%0", "content")
			if err == nil {
				t.Fatal("SaveScrollback() error = nil, want validation failure")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("SaveScrollback() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestStorage_ScrollbackWritesRejectPanesSymlink(t *testing.T) {
	tests := []struct {
		name        string
		save        func(*Storage, string, string) (string, error)
		outsideFile string
	}{
		{
			name: "plain scrollback",
			save: func(storage *Storage, sessionName, checkpointID string) (string, error) {
				return storage.SaveScrollback(sessionName, checkpointID, "%0", "secret scrollback")
			},
			outsideFile: "pane__0.txt",
		},
		{
			name: "compressed scrollback",
			save: func(storage *Storage, sessionName, checkpointID string) (string, error) {
				return storage.SaveCompressedScrollback(sessionName, checkpointID, "%0", []byte("compressed secret scrollback"))
			},
			outsideFile: "pane__0.txt.gz",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			storage := NewStorageWithDir(tmpDir)
			sessionName := "symlink-panes-session"
			checkpointID := "20251210-120000-panes-symlink"
			cpDir := storage.CheckpointDir(sessionName, checkpointID)
			if err := os.MkdirAll(cpDir, 0755); err != nil {
				t.Fatalf("MkdirAll() failed: %v", err)
			}

			outsideDir := filepath.Join(tmpDir, "outside-panes")
			if err := os.MkdirAll(outsideDir, 0755); err != nil {
				t.Fatalf("MkdirAll(outside) failed: %v", err)
			}
			if err := os.Symlink(outsideDir, filepath.Join(cpDir, PanesDir)); err != nil {
				t.Skipf("cannot create symlink: %v", err)
			}

			_, err := tt.save(storage, sessionName, checkpointID)
			if err == nil {
				t.Fatal("scrollback write error = nil, want panes symlink rejection")
			}
			if !strings.Contains(err.Error(), "panes path must not be a symlink") {
				t.Fatalf("scrollback write error = %v, want panes symlink rejection", err)
			}
			outsidePath := filepath.Join(outsideDir, tt.outsideFile)
			if _, statErr := os.Stat(outsidePath); statErr == nil {
				t.Fatalf("scrollback write escaped checkpoint root and created %s", outsidePath)
			} else if !os.IsNotExist(statErr) {
				t.Fatalf("stat outside file: %v", statErr)
			}
		})
	}
}

func TestStorage_SaveRejectsPanesSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "save-symlink-panes-session"
	checkpointID := "20251210-120000-save-panes-symlink"
	cpDir := storage.CheckpointDir(sessionName, checkpointID)
	if err := os.MkdirAll(cpDir, 0755); err != nil {
		t.Fatalf("MkdirAll() failed: %v", err)
	}

	outsideDir := filepath.Join(tmpDir, "outside-panes")
	if err := os.MkdirAll(outsideDir, 0755); err != nil {
		t.Fatalf("MkdirAll(outside) failed: %v", err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(cpDir, PanesDir)); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	cp := &Checkpoint{
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session: SessionState{Panes: []PaneState{
			{Index: 0, ID: "%0"},
		}},
	}
	err := storage.Save(cp)
	if err == nil {
		t.Fatal("Save() error = nil, want panes symlink rejection")
	}
	if !strings.Contains(err.Error(), "panes path must not be a symlink") {
		t.Fatalf("Save() error = %v, want panes symlink rejection", err)
	}
}

func TestStorage_Save_RejectsInvalidArtifactPaths(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-checkpoint-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)
	cp := &Checkpoint{
		ID:          "20251210-120000-invalid-artifacts",
		SessionName: "testproject",
		CreatedAt:   time.Now(),
		Session: SessionState{Panes: []PaneState{
			{Index: 0, ID: "%0", ScrollbackFile: "../../escape.txt"},
		}},
	}

	err = storage.Save(cp)
	if err == nil {
		t.Fatal("Save() error = nil, want invalid artifact path")
	}
	if !strings.Contains(err.Error(), "invalid scrollback path") {
		t.Fatalf("Save() error = %v, want invalid scrollback path", err)
	}
}

func TestStorage_Save_RejectsMissingArtifactReferences(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-checkpoint-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)

	tests := []struct {
		name       string
		checkpoint *Checkpoint
		wantErr    string
	}{
		{
			name: "missing scrollback",
			checkpoint: &Checkpoint{
				ID:          "20251210-120000-missing-scrollback",
				SessionName: "testproject",
				CreatedAt:   time.Now(),
				Session: SessionState{Panes: []PaneState{
					{Index: 0, ID: "%0", ScrollbackFile: "panes/pane__0.txt"},
				}},
			},
			wantErr: "artifact file does not exist",
		},
		{
			name: "missing git patch",
			checkpoint: &Checkpoint{
				ID:          "20251210-120000-missing-patch",
				SessionName: "testproject",
				CreatedAt:   time.Now(),
				Git:         GitState{PatchFile: GitPatchFile},
			},
			wantErr: "artifact file does not exist",
		},
		{
			name: "missing git status",
			checkpoint: &Checkpoint{
				ID:          "20251210-120000-missing-status",
				SessionName: "testproject",
				CreatedAt:   time.Now(),
				Git:         GitState{StatusFile: GitStatusFile},
			},
			wantErr: "artifact file does not exist",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := storage.Save(tt.checkpoint)
			if err == nil {
				t.Fatal("Save() error = nil, want missing artifact error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Save() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestStorage_Save_RejectsSymlinkArtifactReferences(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-checkpoint-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-symlink"
	cpDir := storage.CheckpointDir(sessionName, checkpointID)
	panesDir := filepath.Join(cpDir, PanesDir)
	if err := os.MkdirAll(panesDir, 0755); err != nil {
		t.Fatalf("MkdirAll() failed: %v", err)
	}

	outsidePath := filepath.Join(tmpDir, "outside.txt")
	if err := os.WriteFile(outsidePath, []byte("secret"), 0600); err != nil {
		t.Fatalf("WriteFile() failed: %v", err)
	}

	scrollbackPath := filepath.Join(panesDir, "pane__0.txt")
	if err := os.Symlink(outsidePath, scrollbackPath); err != nil {
		t.Fatalf("Symlink() failed: %v", err)
	}

	cp := &Checkpoint{
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session: SessionState{Panes: []PaneState{
			{Index: 0, ID: "%0", ScrollbackFile: filepath.Join(PanesDir, "pane__0.txt")},
		}},
	}

	err = storage.Save(cp)
	if err == nil {
		t.Fatal("Save() error = nil, want symlink artifact rejection")
	}
	if !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("Save() error = %v, want symlink rejection", err)
	}
}

func TestStorage_Save_PrunesRemovedArtifactReferences(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-checkpoint-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-prune"

	cp := &Checkpoint{
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session: SessionState{Panes: []PaneState{
			{Index: 0, ID: "%0"},
		}},
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	scrollbackPath, err := storage.SaveScrollback(sessionName, checkpointID, "%0", "line 1\nline 2\n")
	if err != nil {
		t.Fatalf("SaveScrollback() failed: %v", err)
	}
	if err := storage.SaveGitPatch(sessionName, checkpointID, "diff --git a/file b/file"); err != nil {
		t.Fatalf("SaveGitPatch() failed: %v", err)
	}
	if err := storage.SaveGitStatus(sessionName, checkpointID, "On branch main\nnothing to commit\n"); err != nil {
		t.Fatalf("SaveGitStatus() failed: %v", err)
	}

	cp.Session.Panes[0].ScrollbackFile = scrollbackPath
	cp.Session.Panes[0].ScrollbackLines = 2
	cp.Git.PatchFile = GitPatchFile
	cp.Git.StatusFile = GitStatusFile
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() with artifact references failed: %v", err)
	}

	scrollbackAbs := filepath.Join(storage.CheckpointDir(sessionName, checkpointID), scrollbackPath)
	if _, err := os.Stat(scrollbackAbs); err != nil {
		t.Fatalf("expected scrollback artifact to exist: %v", err)
	}
	if _, err := os.Stat(storage.GitPatchPath(sessionName, checkpointID)); err != nil {
		t.Fatalf("expected git patch artifact to exist: %v", err)
	}
	statusAbs := filepath.Join(storage.CheckpointDir(sessionName, checkpointID), GitStatusFile)
	if _, err := os.Stat(statusAbs); err != nil {
		t.Fatalf("expected git status artifact to exist: %v", err)
	}

	cp.Session.Panes[0].ScrollbackFile = ""
	cp.Session.Panes[0].ScrollbackLines = 0
	cp.Git.PatchFile = ""
	cp.Git.StatusFile = ""
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() clearing artifact references failed: %v", err)
	}

	if _, err := os.Stat(scrollbackAbs); !os.IsNotExist(err) {
		t.Fatalf("expected scrollback artifact to be pruned, stat err = %v", err)
	}
	if _, err := os.Stat(storage.GitPatchPath(sessionName, checkpointID)); !os.IsNotExist(err) {
		t.Fatalf("expected git patch artifact to be pruned, stat err = %v", err)
	}
	if _, err := os.Stat(statusAbs); !os.IsNotExist(err) {
		t.Fatalf("expected git status artifact to be pruned, stat err = %v", err)
	}
}

func TestStorage_Save_AllowsRepairingExistingInvalidArtifactMetadata(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-checkpoint-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-repair"

	bad := &Checkpoint{
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session: SessionState{Panes: []PaneState{
			{Index: 0, ID: "%0", ScrollbackFile: "../../escape.txt"},
		}},
	}
	cpDir := storage.CheckpointDir(sessionName, checkpointID)
	if err := os.MkdirAll(cpDir, 0755); err != nil {
		t.Fatalf("MkdirAll() failed: %v", err)
	}
	if err := writeJSON(filepath.Join(cpDir, MetadataFile), bad); err != nil {
		t.Fatalf("write metadata failed: %v", err)
	}
	if err := writeJSON(filepath.Join(cpDir, SessionFile), bad.Session); err != nil {
		t.Fatalf("write session failed: %v", err)
	}

	repaired := &Checkpoint{
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   bad.CreatedAt,
		Session: SessionState{Panes: []PaneState{
			{Index: 0, ID: "%0"},
		}},
	}
	if err := storage.Save(repaired); err != nil {
		t.Fatalf("Save() repairing invalid artifact metadata failed: %v", err)
	}

	loaded, err := storage.Load(sessionName, checkpointID)
	if err != nil {
		t.Fatalf("Load() after repair failed: %v", err)
	}
	if loaded.Session.Panes[0].ScrollbackFile != "" {
		t.Fatalf("ScrollbackFile after repair = %q, want empty", loaded.Session.Panes[0].ScrollbackFile)
	}
}

func TestStorage_LoadRejectsInvalidIdentifiers(t *testing.T) {
	storage := NewStorageWithDir(t.TempDir())

	if _, err := storage.Load("../escape", "valid-id"); err == nil {
		t.Fatal("expected invalid session name error")
	}
	if _, err := storage.Load("valid_session", "../escape"); err == nil {
		t.Fatal("expected invalid checkpoint ID error")
	}
	if _, err := storage.Load("valid_session", "."); err == nil {
		t.Fatal("expected dot checkpoint ID to be rejected")
	}
}

func TestStorage_Load_RejectsSymlinkCanonicalFiles(t *testing.T) {
	storage := NewStorageWithDir(t.TempDir())
	sessionName := "testproject"

	baseCheckpoint := &Checkpoint{
		Version:     CurrentVersion,
		ID:          "20251210-120000-symlink-canonical",
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session: SessionState{Panes: []PaneState{
			{Index: 0, ID: "%0"},
		}},
		PaneCount: 1,
	}

	t.Run("metadata", func(t *testing.T) {
		checkpointID := "20251210-120000-symlink-metadata"
		cp := *baseCheckpoint
		cp.ID = checkpointID

		cpDir := storage.CheckpointDir(sessionName, checkpointID)
		if err := os.MkdirAll(cpDir, 0755); err != nil {
			t.Fatalf("MkdirAll() failed: %v", err)
		}

		outsidePath := filepath.Join(t.TempDir(), "outside-metadata.json")
		if err := writeJSON(outsidePath, &cp); err != nil {
			t.Fatalf("writeJSON(outside metadata) failed: %v", err)
		}
		if err := os.Symlink(outsidePath, filepath.Join(cpDir, MetadataFile)); err != nil {
			t.Fatalf("Symlink() failed: %v", err)
		}
		if err := writeJSON(filepath.Join(cpDir, SessionFile), cp.Session); err != nil {
			t.Fatalf("writeJSON(session) failed: %v", err)
		}

		_, err := storage.Load(sessionName, checkpointID)
		if err == nil {
			t.Fatal("Load() error = nil, want symlink rejection")
		}
		if !strings.Contains(err.Error(), "must not be a symlink") {
			t.Fatalf("Load() error = %v, want symlink rejection", err)
		}
	})

	t.Run("session", func(t *testing.T) {
		checkpointID := "20251210-120000-symlink-session"
		cp := *baseCheckpoint
		cp.ID = checkpointID

		cpDir := storage.CheckpointDir(sessionName, checkpointID)
		if err := os.MkdirAll(cpDir, 0755); err != nil {
			t.Fatalf("MkdirAll() failed: %v", err)
		}

		outsidePath := filepath.Join(t.TempDir(), "outside-session.json")
		if err := writeJSON(outsidePath, cp.Session); err != nil {
			t.Fatalf("writeJSON(outside session) failed: %v", err)
		}
		if err := writeJSON(filepath.Join(cpDir, MetadataFile), &cp); err != nil {
			t.Fatalf("writeJSON(metadata) failed: %v", err)
		}
		if err := os.Symlink(outsidePath, filepath.Join(cpDir, SessionFile)); err != nil {
			t.Fatalf("Symlink() failed: %v", err)
		}

		_, err := storage.Load(sessionName, checkpointID)
		if err == nil {
			t.Fatal("Load() error = nil, want symlink rejection")
		}
		if !strings.Contains(err.Error(), "must not be a symlink") {
			t.Fatalf("Load() error = %v, want symlink rejection", err)
		}
	})
}

func TestStorage_Save_RejectsSymlinkDirectories(t *testing.T) {
	t.Run("session", func(t *testing.T) {
		storage := NewStorageWithDir(t.TempDir())
		sessionName := "testproject"
		checkpointID := "20251210-120000-symlink-session-dir"
		outsideDir := t.TempDir()

		if err := os.Symlink(outsideDir, filepath.Join(storage.BaseDir, sessionName)); err != nil {
			t.Skipf("cannot create symlink: %v", err)
		}

		cp := &Checkpoint{
			ID:          checkpointID,
			SessionName: sessionName,
			CreatedAt:   time.Now(),
			Session:     SessionState{Panes: []PaneState{}},
		}
		err := storage.Save(cp)
		if err == nil {
			t.Fatal("Save() error = nil, want session symlink rejection")
		}
		if !strings.Contains(err.Error(), "session path must not be a symlink") {
			t.Fatalf("Save() error = %v, want session symlink rejection", err)
		}
	})

	t.Run("checkpoint", func(t *testing.T) {
		storage := NewStorageWithDir(t.TempDir())
		sessionName := "testproject"
		checkpointID := "20251210-120000-symlink-checkpoint-dir"
		sessionDir := filepath.Join(storage.BaseDir, sessionName)
		if err := os.MkdirAll(sessionDir, 0755); err != nil {
			t.Fatalf("MkdirAll(session dir) failed: %v", err)
		}

		outsideDir := t.TempDir()
		if err := os.Symlink(outsideDir, filepath.Join(sessionDir, checkpointID)); err != nil {
			t.Skipf("cannot create symlink: %v", err)
		}

		cp := &Checkpoint{
			ID:          checkpointID,
			SessionName: sessionName,
			CreatedAt:   time.Now(),
			Session:     SessionState{Panes: []PaneState{}},
		}
		err := storage.Save(cp)
		if err == nil {
			t.Fatal("Save() error = nil, want checkpoint symlink rejection")
		}
		if !strings.Contains(err.Error(), "checkpoint path must not be a symlink") {
			t.Fatalf("Save() error = %v, want checkpoint symlink rejection", err)
		}
	})
}

func TestStorage_GetLatest(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-checkpoint-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"

	// No checkpoints yet
	_, err = storage.GetLatest(sessionName)
	if err == nil {
		t.Error("GetLatest() should fail with no checkpoints")
	}
	if !errors.Is(err, ErrNoCheckpoints) {
		t.Fatalf("GetLatest() error = %v, want ErrNoCheckpoints", err)
	}

	// Create checkpoints
	cp1 := &Checkpoint{
		ID:          "20251210-100000-first",
		Name:        "first",
		SessionName: sessionName,
		CreatedAt:   time.Date(2025, 12, 10, 10, 0, 0, 0, time.UTC),
		Session: SessionState{Panes: []PaneState{
			{Index: 0, ID: "%0"},
			{Index: 1, ID: "%1"},
			{Index: 2, ID: "%2"},
		}},
	}
	cp2 := &Checkpoint{
		ID:          "20251210-120000-second",
		Name:        "second",
		SessionName: sessionName,
		CreatedAt:   time.Date(2025, 12, 10, 12, 0, 0, 0, time.UTC),
		Session: SessionState{Panes: []PaneState{
			{Index: 0, ID: "%0"},
			{Index: 1, ID: "%1"},
			{Index: 2, ID: "%2"},
		}},
	}

	storage.Save(cp1)
	storage.Save(cp2)

	latest, err := storage.GetLatest(sessionName)
	if err != nil {
		t.Fatalf("GetLatest() failed: %v", err)
	}

	if latest.Name != "second" {
		t.Errorf("GetLatest().Name = %q, want %q", latest.Name, "second")
	}
}

func TestStorage_GetLatest_PrefersCreatedAtOverDirectoryModTime(t *testing.T) {
	storage := NewStorageWithDir(t.TempDir())
	sessionName := "testproject"

	older := &Checkpoint{
		ID:          "20251210-100000-older",
		Name:        "older",
		SessionName: sessionName,
		CreatedAt:   time.Date(2025, 12, 10, 10, 0, 0, 0, time.UTC),
		Session:     SessionState{Panes: []PaneState{{Index: 0, ID: "%0"}}},
	}
	newer := &Checkpoint{
		ID:          "20251210-120000-newer",
		Name:        "newer",
		SessionName: sessionName,
		CreatedAt:   time.Date(2025, 12, 10, 12, 0, 0, 0, time.UTC),
		Session:     SessionState{Panes: []PaneState{{Index: 0, ID: "%1"}}},
	}
	if err := storage.Save(older); err != nil {
		t.Fatalf("Save(older) failed: %v", err)
	}
	if err := storage.Save(newer); err != nil {
		t.Fatalf("Save(newer) failed: %v", err)
	}

	olderDir := storage.CheckpointDir(sessionName, older.ID)
	newerDir := storage.CheckpointDir(sessionName, newer.ID)
	olderModTime := time.Date(2025, 12, 10, 14, 0, 0, 0, time.UTC)
	newerModTime := time.Date(2025, 12, 10, 11, 0, 0, 0, time.UTC)
	if err := os.Chtimes(olderDir, olderModTime, olderModTime); err != nil {
		t.Fatalf("Chtimes(olderDir) failed: %v", err)
	}
	if err := os.Chtimes(newerDir, newerModTime, newerModTime); err != nil {
		t.Fatalf("Chtimes(newerDir) failed: %v", err)
	}

	latest, err := storage.GetLatest(sessionName)
	if err != nil {
		t.Fatalf("GetLatest() failed: %v", err)
	}
	if latest.ID != newer.ID {
		t.Fatalf("GetLatest() ID = %q, want %q", latest.ID, newer.ID)
	}
}

func TestStorage_ListAndGetLatest_AgreeWhenCreatedAtEqual(t *testing.T) {
	storage := NewStorageWithDir(t.TempDir())
	sessionName := "testproject"
	createdAt := time.Date(2025, 12, 10, 12, 0, 0, 0, time.UTC)

	first := &Checkpoint{
		ID:          "20251210-120000-alpha",
		Name:        "alpha",
		SessionName: sessionName,
		CreatedAt:   createdAt,
		Session:     SessionState{Panes: []PaneState{{Index: 0, ID: "%0"}}},
	}
	second := &Checkpoint{
		ID:          "20251210-120000-zulu",
		Name:        "zulu",
		SessionName: sessionName,
		CreatedAt:   createdAt,
		Session:     SessionState{Panes: []PaneState{{Index: 0, ID: "%1"}}},
	}
	if err := storage.Save(first); err != nil {
		t.Fatalf("Save(first) failed: %v", err)
	}
	if err := storage.Save(second); err != nil {
		t.Fatalf("Save(second) failed: %v", err)
	}

	list, err := storage.List(sessionName)
	if err != nil {
		t.Fatalf("List() failed: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(List()) = %d, want 2", len(list))
	}
	if list[0].ID != second.ID {
		t.Fatalf("List()[0].ID = %q, want %q", list[0].ID, second.ID)
	}

	latest, err := storage.GetLatest(sessionName)
	if err != nil {
		t.Fatalf("GetLatest() failed: %v", err)
	}
	if latest.ID != second.ID {
		t.Fatalf("GetLatest() ID = %q, want %q", latest.ID, second.ID)
	}
}

func TestStorage_GetLatest_RejectsInvalidNewestCheckpoint(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"

	valid := &Checkpoint{
		ID:          "20251210-100000-valid",
		Name:        "valid",
		SessionName: sessionName,
		CreatedAt:   time.Date(2025, 12, 10, 10, 0, 0, 0, time.UTC),
		Session:     SessionState{Panes: []PaneState{{Index: 0, ID: "%0"}}},
	}
	if err := storage.Save(valid); err != nil {
		t.Fatalf("Save(valid) failed: %v", err)
	}
	validDir := storage.CheckpointDir(sessionName, valid.ID)
	validTime := time.Date(2025, 12, 10, 10, 0, 0, 0, time.UTC)
	if err := os.Chtimes(validDir, validTime, validTime); err != nil {
		t.Fatalf("Chtimes(validDir) failed: %v", err)
	}

	invalidDir := storage.CheckpointDir(sessionName, "20251210-120000-invalid")
	if err := os.MkdirAll(invalidDir, 0755); err != nil {
		t.Fatalf("MkdirAll(invalidDir) failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(invalidDir, MetadataFile), []byte("not valid json"), 0600); err != nil {
		t.Fatalf("WriteFile(metadata) failed: %v", err)
	}
	newestTime := time.Date(2025, 12, 10, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(invalidDir, newestTime, newestTime); err != nil {
		t.Fatalf("Chtimes(invalidDir) failed: %v", err)
	}

	_, err := storage.GetLatest(sessionName)
	if err == nil {
		t.Fatal("GetLatest() error = nil, want invalid newest checkpoint error")
	}
	if !strings.Contains(err.Error(), "checkpoint selection blocked by invalid checkpoint") {
		t.Fatalf("GetLatest() error = %v, want invalid newest checkpoint context", err)
	}
}

func TestStorage_GetLatest_IgnoresIncrementalNamespaceDirectory(t *testing.T) {
	t.Parallel()

	storage := NewStorageWithDir(t.TempDir())
	sessionName := "selection-incremental-session"

	valid := &Checkpoint{
		ID:          "20251210-100000-valid",
		Name:        "valid",
		SessionName: sessionName,
		CreatedAt:   time.Date(2025, 12, 10, 10, 0, 0, 0, time.UTC),
		Session:     SessionState{Panes: []PaneState{{Index: 0, ID: "%0"}}},
	}
	if err := storage.Save(valid); err != nil {
		t.Fatalf("Save(valid) failed: %v", err)
	}
	validDir := storage.CheckpointDir(sessionName, valid.ID)
	validTime := time.Date(2025, 12, 10, 10, 0, 0, 0, time.UTC)
	if err := os.Chtimes(validDir, validTime, validTime); err != nil {
		t.Fatalf("Chtimes(validDir) failed: %v", err)
	}

	incrementalDir := filepath.Join(storage.BaseDir, sessionName, "incremental")
	if err := os.MkdirAll(incrementalDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) failed: %v", incrementalDir, err)
	}
	newestTime := time.Date(2025, 12, 10, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(incrementalDir, newestTime, newestTime); err != nil {
		t.Fatalf("Chtimes(incrementalDir) failed: %v", err)
	}

	latest, err := storage.GetLatest(sessionName)
	if err != nil {
		t.Fatalf("GetLatest() failed: %v", err)
	}
	if latest.ID != valid.ID {
		t.Fatalf("GetLatest() ID = %q, want %q", latest.ID, valid.ID)
	}
}

func TestCheckpoint_Age(t *testing.T) {
	cp := &Checkpoint{
		CreatedAt: time.Now().Add(-1 * time.Hour),
	}

	age := cp.Age()
	if age < 59*time.Minute || age > 61*time.Minute {
		t.Errorf("Age() = %v, want ~1 hour", age)
	}
}

func TestCheckpoint_HasGitPatch(t *testing.T) {
	cp := &Checkpoint{}
	if cp.HasGitPatch() {
		t.Error("HasGitPatch() should be false with no patch file")
	}

	cp.Git.PatchFile = "git.patch"
	if !cp.HasGitPatch() {
		t.Error("HasGitPatch() should be true with patch file")
	}
}

func TestParseGitStatus(t *testing.T) {
	tests := []struct {
		name      string
		status    string
		staged    int
		unstaged  int
		untracked int
	}{
		{
			name:      "clean",
			status:    "",
			staged:    0,
			unstaged:  0,
			untracked: 0,
		},
		{
			name:      "staged file",
			status:    "M  file.go",
			staged:    1,
			unstaged:  0,
			untracked: 0,
		},
		{
			name:      "unstaged file",
			status:    " M file.go",
			staged:    0,
			unstaged:  1,
			untracked: 0,
		},
		{
			name:      "untracked file",
			status:    "?? newfile.go",
			staged:    0,
			unstaged:  0,
			untracked: 1,
		},
		{
			name:      "mixed status",
			status:    "M  staged.go\n M unstaged.go\n?? untracked.go\nMM both.go",
			staged:    2, // M staged.go and MM both.go
			unstaged:  2, // M unstaged.go and MM both.go
			untracked: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			staged, unstaged, untracked := parseGitStatus(tt.status)
			if staged != tt.staged {
				t.Errorf("staged = %d, want %d", staged, tt.staged)
			}
			if unstaged != tt.unstaged {
				t.Errorf("unstaged = %d, want %d", unstaged, tt.unstaged)
			}
			if untracked != tt.untracked {
				t.Errorf("untracked = %d, want %d", untracked, tt.untracked)
			}
		})
	}
}

func TestCountLines(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"\n", 0}, // Just a newline = 0 lines
		{"one", 1},
		{"one\n", 1}, // Trailing newline doesn't add a line
		{"one\ntwo", 2},
		{"one\ntwo\n", 2}, // Trailing newline doesn't add a line
		{"one\ntwo\nthree", 3},
		{"one\ntwo\nthree\n", 3}, // Trailing newline doesn't add a line
	}

	for _, tt := range tests {
		got := countLines(tt.input)
		if got != tt.want {
			t.Errorf("countLines(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestMatchWildcard(t *testing.T) {
	tests := []struct {
		s       string
		pattern string
		want    bool
	}{
		{"backup", "backup", true},
		{"backup", "BACKUP", true},
		{"backup", "back*", true},
		{"backup", "*up", true},
		{"backup", "b*p", true},
		{"backup", "*", true},
		{"backup", "nope", false},
		{"backup", "b*x", false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			got := matchWildcard(tt.s, tt.pattern)
			if got != tt.want {
				t.Errorf("matchWildcard(%q, %q) = %v, want %v", tt.s, tt.pattern, got, tt.want)
			}
		})
	}
}

// Helper function
func containsSubstring(s, substr string) bool {
	return filepath.Base(s) == substr || len(s) >= len(substr) && s[len(s)-len(substr):] == substr
}

func TestStorage_ListAll(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-checkpoint-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)

	// Create checkpoints for multiple sessions
	sessions := []string{"project-a", "project-b"}
	for _, sessionName := range sessions {
		for i := 0; i < 2; i++ {
			cp := &Checkpoint{
				ID:          GenerateID("backup" + string(rune('A'+i))),
				Name:        "backup" + string(rune('A'+i)),
				SessionName: sessionName,
				CreatedAt:   time.Now().Add(time.Duration(-i) * time.Hour),
				Session:     SessionState{Panes: []PaneState{}},
			}
			if err := storage.Save(cp); err != nil {
				t.Fatalf("Save() failed: %v", err)
			}
		}
	}

	// List all checkpoints
	all, err := storage.ListAll()
	if err != nil {
		t.Fatalf("ListAll() failed: %v", err)
	}

	if len(all) != 4 {
		t.Errorf("len(ListAll()) = %d, want 4", len(all))
	}

	// Verify sorted by newest first
	for i := 1; i < len(all); i++ {
		if all[i].CreatedAt.After(all[i-1].CreatedAt) {
			t.Errorf("ListAll not sorted by newest first")
		}
	}
}

func TestStorage_ListAll_TieBreaksEqualCreatedAtDeterministically(t *testing.T) {
	storage := NewStorageWithDir(t.TempDir())
	createdAt := time.Date(2025, 12, 10, 12, 0, 0, 0, time.UTC)

	cpB := &Checkpoint{
		ID:          "20251210-120000-same",
		Name:        "same",
		SessionName: "project-b",
		CreatedAt:   createdAt,
		Session:     SessionState{Panes: []PaneState{}},
	}
	cpA := &Checkpoint{
		ID:          "20251210-120000-same",
		Name:        "same",
		SessionName: "project-a",
		CreatedAt:   createdAt,
		Session:     SessionState{Panes: []PaneState{}},
	}
	if err := storage.Save(cpB); err != nil {
		t.Fatalf("Save(cpB) failed: %v", err)
	}
	if err := storage.Save(cpA); err != nil {
		t.Fatalf("Save(cpA) failed: %v", err)
	}

	all, err := storage.ListAll()
	if err != nil {
		t.Fatalf("ListAll() failed: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("len(ListAll()) = %d, want 2", len(all))
	}
	if all[0].SessionName != "project-a" || all[1].SessionName != "project-b" {
		t.Fatalf("ListAll() session order = [%s %s], want [project-a project-b]", all[0].SessionName, all[1].SessionName)
	}
}

func TestStorage_ListAll_NoDir(t *testing.T) {
	storage := NewStorageWithDir("/nonexistent/path")

	all, err := storage.ListAll()
	if err != nil {
		t.Fatalf("ListAll() should not error for nonexistent dir: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("ListAll() should return nil/empty for nonexistent dir")
	}
}

func TestStorage_Delete(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-checkpoint-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-test"

	// Create a checkpoint
	cp := &Checkpoint{
		ID:          checkpointID,
		Name:        "test",
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	// Verify it exists
	if !storage.Exists(sessionName, checkpointID) {
		t.Fatal("Checkpoint should exist after save")
	}

	// Delete it
	if err := storage.Delete(sessionName, checkpointID); err != nil {
		t.Fatalf("Delete() failed: %v", err)
	}

	// Verify it's gone
	if storage.Exists(sessionName, checkpointID) {
		t.Error("Checkpoint should not exist after delete")
	}
}

func TestStorage_Exists(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-checkpoint-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)

	// Non-existent checkpoint
	if storage.Exists("nosession", "nocheckpoint") {
		t.Error("Exists() should return false for non-existent checkpoint")
	}

	// Create a checkpoint
	sessionName := "testproject"
	checkpointID := "20251210-120000-test"
	cp := &Checkpoint{
		ID:          checkpointID,
		Name:        "test",
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	// Now it should exist
	if !storage.Exists(sessionName, checkpointID) {
		t.Error("Exists() should return true for existing checkpoint")
	}
}

func TestStorage_SaveGitPatch(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-checkpoint-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-test"

	// Create checkpoint first
	cp := &Checkpoint{
		ID:          checkpointID,
		Name:        "test",
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	// Save empty patch should be no-op
	if err := storage.SaveGitPatch(sessionName, checkpointID, ""); err != nil {
		t.Errorf("SaveGitPatch() with empty patch should succeed: %v", err)
	}

	// Save actual patch
	patch := "diff --git a/file.go b/file.go\n--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-old\n+new"
	if err := storage.SaveGitPatch(sessionName, checkpointID, patch); err != nil {
		t.Fatalf("SaveGitPatch() failed: %v", err)
	}

	// Verify patch was saved
	patchPath := filepath.Join(storage.CheckpointDir(sessionName, checkpointID), GitPatchFile)
	data, err := os.ReadFile(patchPath)
	if err != nil {
		t.Fatalf("Failed to read patch file: %v", err)
	}
	if string(data) != patch {
		t.Errorf("Patch content mismatch: got %q, want %q", string(data), patch)
	}
}

func TestStorage_Delete_RemovesNonDirectoryCheckpointPath(t *testing.T) {
	t.Parallel()

	storage := NewStorageWithDir(t.TempDir())
	sessionName := "delete-broken-session"
	checkpointID := "20251210-120000-broken"

	sessionDir := filepath.Join(storage.BaseDir, sessionName)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", sessionDir, err)
	}

	checkpointPath := filepath.Join(sessionDir, checkpointID)
	if err := os.WriteFile(checkpointPath, []byte("not a checkpoint directory"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", checkpointPath, err)
	}

	if err := storage.Delete(sessionName, checkpointID); err != nil {
		t.Fatalf("Delete() failed: %v", err)
	}

	if _, err := os.Lstat(checkpointPath); !os.IsNotExist(err) {
		t.Fatalf("checkpoint path still exists after Delete(): err=%v", err)
	}
}

func TestStorage_LoadGitPatch(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-checkpoint-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-test"

	// Create checkpoint
	cp := &Checkpoint{
		ID:          checkpointID,
		Name:        "test",
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	// Load non-existent patch should return empty string
	patch, err := storage.LoadGitPatch(sessionName, checkpointID)
	if err != nil {
		t.Errorf("LoadGitPatch() should not error for missing patch: %v", err)
	}
	if patch != "" {
		t.Errorf("LoadGitPatch() should return empty for missing patch, got %q", patch)
	}

	// Save and load patch
	expectedPatch := "diff --git a/file.go b/file.go\n"
	if err := storage.SaveGitPatch(sessionName, checkpointID, expectedPatch); err != nil {
		t.Fatalf("SaveGitPatch() failed: %v", err)
	}

	loaded, err := storage.LoadGitPatch(sessionName, checkpointID)
	if err != nil {
		t.Fatalf("LoadGitPatch() failed: %v", err)
	}
	if loaded != expectedPatch {
		t.Errorf("LoadGitPatch() = %q, want %q", loaded, expectedPatch)
	}
}

func TestStorage_LoadGitPatch_RejectsSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-patch-symlink"

	cp := &Checkpoint{
		ID:          checkpointID,
		Name:        "test",
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	outsidePath := filepath.Join(t.TempDir(), "outside.patch")
	if err := os.WriteFile(outsidePath, []byte("diff --git a/file.go b/file.go\n"), 0600); err != nil {
		t.Fatalf("WriteFile(outside patch) failed: %v", err)
	}
	if err := os.Symlink(outsidePath, filepath.Join(storage.CheckpointDir(sessionName, checkpointID), GitPatchFile)); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	_, err := storage.LoadGitPatch(sessionName, checkpointID)
	if err == nil {
		t.Fatal("LoadGitPatch() error = nil, want symlink rejection")
	}
	if !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("LoadGitPatch() error = %v, want symlink rejection", err)
	}
}

func TestStorage_LoadGitPatch_UsesRecordedPath(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-patch-custom"

	cp := &Checkpoint{
		ID:          checkpointID,
		Name:        "test",
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	cpDir := storage.CheckpointDir(sessionName, checkpointID)
	if err := os.MkdirAll(filepath.Join(cpDir, "git"), 0755); err != nil {
		t.Fatalf("MkdirAll(git) failed: %v", err)
	}
	cp.Git.PatchFile = "git/custom.patch"
	expectedPatch := "diff --git a/custom.go b/custom.go\n"
	if err := os.WriteFile(filepath.Join(cpDir, cp.Git.PatchFile), []byte(expectedPatch), 0600); err != nil {
		t.Fatalf("WriteFile(custom patch) failed: %v", err)
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() with custom patch path failed: %v", err)
	}

	loaded, err := storage.LoadGitPatch(sessionName, checkpointID)
	if err != nil {
		t.Fatalf("LoadGitPatch() failed: %v", err)
	}
	if loaded != expectedPatch {
		t.Fatalf("LoadGitPatch() = %q, want %q", loaded, expectedPatch)
	}
}

func TestStorage_SaveGitPatch_CreatesCheckpointDir(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-autocreate"
	expectedPatch := "diff --git a/file.go b/file.go\n"

	if err := storage.SaveGitPatch(sessionName, checkpointID, expectedPatch); err != nil {
		t.Fatalf("SaveGitPatch() failed: %v", err)
	}

	loaded, err := storage.LoadGitPatch(sessionName, checkpointID)
	if err != nil {
		t.Fatalf("LoadGitPatch() failed: %v", err)
	}
	if loaded != expectedPatch {
		t.Errorf("LoadGitPatch() = %q, want %q", loaded, expectedPatch)
	}
}

func TestStorage_SaveGitStatus(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-checkpoint-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-test"

	// Create checkpoint
	cp := &Checkpoint{
		ID:          checkpointID,
		Name:        "test",
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	// Save git status
	status := "M  file.go\n?? newfile.go"
	if err := storage.SaveGitStatus(sessionName, checkpointID, status); err != nil {
		t.Fatalf("SaveGitStatus() failed: %v", err)
	}

	// Verify status was saved
	statusPath := filepath.Join(storage.CheckpointDir(sessionName, checkpointID), GitStatusFile)
	data, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatalf("Failed to read status file: %v", err)
	}
	if string(data) != status {
		t.Errorf("Status content mismatch: got %q, want %q", string(data), status)
	}
}

func TestStorage_SaveGitStatus_CreatesCheckpointDir(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-autocreate"
	status := "M  file.go\n?? newfile.go"

	if err := storage.SaveGitStatus(sessionName, checkpointID, status); err != nil {
		t.Fatalf("SaveGitStatus() failed: %v", err)
	}

	statusPath := filepath.Join(storage.CheckpointDir(sessionName, checkpointID), GitStatusFile)
	data, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatalf("Failed to read status file: %v", err)
	}
	if string(data) != status {
		t.Errorf("Status content mismatch: got %q, want %q", string(data), status)
	}
}

func TestStorage_LoadGitStatus(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-status"

	cp := &Checkpoint{
		ID:          checkpointID,
		Name:        "test",
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	status, err := storage.LoadGitStatus(sessionName, checkpointID)
	if err != nil {
		t.Fatalf("LoadGitStatus() missing file error = %v", err)
	}
	if status != "" {
		t.Fatalf("LoadGitStatus() missing file = %q, want empty string", status)
	}

	expectedStatus := "M  file.go\n?? newfile.go\n"
	if err := storage.SaveGitStatus(sessionName, checkpointID, expectedStatus); err != nil {
		t.Fatalf("SaveGitStatus() failed: %v", err)
	}

	loaded, err := storage.LoadGitStatus(sessionName, checkpointID)
	if err != nil {
		t.Fatalf("LoadGitStatus() failed: %v", err)
	}
	if loaded != expectedStatus {
		t.Fatalf("LoadGitStatus() = %q, want %q", loaded, expectedStatus)
	}
}

func TestStorage_LoadGitStatus_RejectsSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-status-symlink"

	cp := &Checkpoint{
		ID:          checkpointID,
		Name:        "test",
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	outsidePath := filepath.Join(t.TempDir(), "outside.status")
	if err := os.WriteFile(outsidePath, []byte("M  file.go\n"), 0600); err != nil {
		t.Fatalf("WriteFile(outside status) failed: %v", err)
	}
	if err := os.Symlink(outsidePath, filepath.Join(storage.CheckpointDir(sessionName, checkpointID), GitStatusFile)); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	_, err := storage.LoadGitStatus(sessionName, checkpointID)
	if err == nil {
		t.Fatal("LoadGitStatus() error = nil, want symlink rejection")
	}
	if !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("LoadGitStatus() error = %v, want symlink rejection", err)
	}
}

func TestStorage_LoadGitStatus_UsesRecordedPath(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-status-custom"

	cp := &Checkpoint{
		ID:          checkpointID,
		Name:        "test",
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	cpDir := storage.CheckpointDir(sessionName, checkpointID)
	if err := os.MkdirAll(filepath.Join(cpDir, "git"), 0755); err != nil {
		t.Fatalf("MkdirAll(git) failed: %v", err)
	}
	cp.Git.StatusFile = "git/custom.status"
	expectedStatus := "M  custom.go\n"
	if err := os.WriteFile(filepath.Join(cpDir, cp.Git.StatusFile), []byte(expectedStatus), 0600); err != nil {
		t.Fatalf("WriteFile(custom status) failed: %v", err)
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() with custom status path failed: %v", err)
	}

	loaded, err := storage.LoadGitStatus(sessionName, checkpointID)
	if err != nil {
		t.Fatalf("LoadGitStatus() failed: %v", err)
	}
	if loaded != expectedStatus {
		t.Fatalf("LoadGitStatus() = %q, want %q", loaded, expectedStatus)
	}
}

func TestCheckpoint_Summary(t *testing.T) {
	cp := &Checkpoint{
		ID:   "20251210-120000-backup",
		Name: "backup",
	}

	summary := cp.Summary()
	expected := "backup (20251210-120000-backup)"
	if summary != expected {
		t.Errorf("Summary() = %q, want %q", summary, expected)
	}
}

func TestFromTmuxPane(t *testing.T) {
	pane := tmux.Pane{
		Index:   0,
		ID:      "%0",
		Title:   "test-pane",
		Type:    tmux.AgentClaude,
		Command: "claude",
		Width:   120,
		Height:  40,
	}

	state := FromTmuxPane(pane)

	if state.Index != 0 {
		t.Errorf("Index = %d, want 0", state.Index)
	}
	if state.ID != "%0" {
		t.Errorf("ID = %q, want %%0", state.ID)
	}
	if state.Title != "test-pane" {
		t.Errorf("Title = %q, want test-pane", state.Title)
	}
	if state.AgentType != string(tmux.AgentClaude) {
		t.Errorf("AgentType = %q, want %s", state.AgentType, tmux.AgentClaude)
	}
	if state.Command != "claude" {
		t.Errorf("Command = %q, want claude", state.Command)
	}
	if state.Width != 120 {
		t.Errorf("Width = %d, want 120", state.Width)
	}
	if state.Height != 40 {
		t.Errorf("Height = %d, want 40", state.Height)
	}
}

func TestCheckpointOptions(t *testing.T) {
	// Test default options
	opts := defaultOptions()
	if !opts.captureGit {
		t.Error("defaultOptions().captureGit should be true")
	}
	if opts.scrollbackLines != 5000 {
		t.Errorf("defaultOptions().scrollbackLines = %d, want 5000", opts.scrollbackLines)
	}
	if opts.description != "" {
		t.Errorf("defaultOptions().description should be empty")
	}
	if !opts.scrollbackCompress {
		t.Error("defaultOptions().scrollbackCompress should be true")
	}
	if opts.scrollbackMaxSizeMB != 10 {
		t.Errorf("defaultOptions().scrollbackMaxSizeMB = %d, want 10", opts.scrollbackMaxSizeMB)
	}

	// Test WithDescription
	opts = checkpointOptions{}
	WithDescription("test description")(&opts)
	if opts.description != "test description" {
		t.Errorf("WithDescription failed: got %q", opts.description)
	}

	// Test WithGitCapture
	opts = checkpointOptions{captureGit: true}
	WithGitCapture(false)(&opts)
	if opts.captureGit {
		t.Error("WithGitCapture(false) should set captureGit to false")
	}
	WithGitCapture(true)(&opts)
	if !opts.captureGit {
		t.Error("WithGitCapture(true) should set captureGit to true")
	}

	// Test WithScrollbackLines
	opts = checkpointOptions{}
	WithScrollbackLines(5000)(&opts)
	if opts.scrollbackLines != 5000 {
		t.Errorf("WithScrollbackLines(5000) = %d, want 5000", opts.scrollbackLines)
	}
}

func TestStorage_List_NonexistentSession(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-checkpoint-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)

	// List checkpoints for non-existent session
	list, err := storage.List("nonexistent")
	if err != nil {
		t.Fatalf("List() should not error for non-existent session: %v", err)
	}
	if len(list) != 0 {
		t.Error("List() should return nil/empty for non-existent session")
	}
}

func TestNewStorage(t *testing.T) {
	storage := NewStorage()
	if storage.BaseDir == "" {
		t.Error("NewStorage() should set BaseDir")
	}
	// Should end with the default checkpoint dir
	if !filepath.IsAbs(storage.BaseDir) {
		t.Error("NewStorage().BaseDir should be an absolute path")
	}
}

func TestStorage_SaveScrollback_LargeContent(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	// Create checkpoint first
	cp := &Checkpoint{
		ID:          "20251210-120000-large",
		SessionName: "testproject",
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	// Create ~1MB of scrollback content
	var content string
	for i := 0; i < 10000; i++ {
		content += "Line " + string(rune('0'+i%10)) + " with some text content here\n"
	}

	_, err := storage.SaveScrollback(cp.SessionName, cp.ID, "%0", content)
	if err != nil {
		t.Fatalf("SaveScrollback() failed for large content: %v", err)
	}

	loaded, err := storage.LoadScrollback(cp.SessionName, cp.ID, "%0")
	if err != nil {
		t.Fatalf("LoadScrollback() failed: %v", err)
	}

	if loaded != content {
		t.Errorf("loaded content length = %d, want %d", len(loaded), len(content))
	}
}

func TestStorage_SaveScrollback_SpecialCharacters(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	// Create checkpoint first
	cp := &Checkpoint{
		ID:          "20251210-120000-special",
		SessionName: "testproject",
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	// Content with various special characters including ANSI escape codes
	content := "Normal text\n\x1b[31mRed text\x1b[0m\nUnicode: 你好世界 🎉\nTabs:\t\there\nNullbyte:\x00end\n"

	_, err := storage.SaveScrollback(cp.SessionName, cp.ID, "%1", content)
	if err != nil {
		t.Fatalf("SaveScrollback() failed: %v", err)
	}

	loaded, err := storage.LoadScrollback(cp.SessionName, cp.ID, "%1")
	if err != nil {
		t.Fatalf("LoadScrollback() failed: %v", err)
	}

	if loaded != content {
		t.Errorf("content mismatch with special characters\ngot: %q\nwant: %q", loaded, content)
	}
}

func TestStorage_FilePermissions(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cp := &Checkpoint{
		ID:          "20251210-120000-perms",
		SessionName: "testproject",
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	// Save scrollback and git patch
	_, err := storage.SaveScrollback(cp.SessionName, cp.ID, "%0", "test content")
	if err != nil {
		t.Fatalf("SaveScrollback() failed: %v", err)
	}

	if err := storage.SaveGitPatch(cp.SessionName, cp.ID, "diff content"); err != nil {
		t.Fatalf("SaveGitPatch() failed: %v", err)
	}

	// Verify directory is created with reasonable permissions
	cpDir := storage.CheckpointDir(cp.SessionName, cp.ID)
	info, err := os.Stat(cpDir)
	if err != nil {
		t.Fatalf("stat checkpoint dir: %v", err)
	}
	if !info.IsDir() {
		t.Error("checkpoint path should be a directory")
	}

	// Files should exist
	metaPath := filepath.Join(cpDir, MetadataFile)
	if _, err := os.Stat(metaPath); err != nil {
		t.Errorf("metadata file should exist: %v", err)
	}

	patchPath := filepath.Join(cpDir, GitPatchFile)
	if _, err := os.Stat(patchPath); err != nil {
		t.Errorf("git patch file should exist: %v", err)
	}
}

func TestStorage_LoadCorruptedJSON(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "testproject"
	checkpointID := "20251210-120000-corrupt"

	// Create checkpoint directory manually
	cpDir := storage.CheckpointDir(sessionName, checkpointID)
	if err := os.MkdirAll(cpDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	// Write corrupted metadata
	metaPath := filepath.Join(cpDir, MetadataFile)
	if err := os.WriteFile(metaPath, []byte("not valid json {"), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	_, err := storage.Load(sessionName, checkpointID)
	if err == nil {
		t.Error("Load() should fail for corrupted JSON")
	}
}

func TestStorage_MultiplePanes(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cp := &Checkpoint{
		ID:          "20251210-120000-multipane",
		SessionName: "multitest",
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	// Save scrollback for multiple panes
	paneData := map[string]string{
		"%0": "Content for pane 0\nline 2\n",
		"%1": "Content for pane 1\n",
		"%2": "Content for pane 2\nmore lines\neven more\n",
	}

	for paneID, content := range paneData {
		_, err := storage.SaveScrollback(cp.SessionName, cp.ID, paneID, content)
		if err != nil {
			t.Fatalf("SaveScrollback(%s) failed: %v", paneID, err)
		}
	}

	// Load and verify each pane
	for paneID, expectedContent := range paneData {
		loaded, err := storage.LoadScrollback(cp.SessionName, cp.ID, paneID)
		if err != nil {
			t.Fatalf("LoadScrollback(%s) failed: %v", paneID, err)
		}
		if loaded != expectedContent {
			t.Errorf("pane %s content mismatch\ngot: %q\nwant: %q", paneID, loaded, expectedContent)
		}
	}
}

func TestStorage_EmptyScrollback(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cp := &Checkpoint{
		ID:          "20251210-120000-empty",
		SessionName: "emptytest",
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	// Save empty scrollback
	_, err := storage.SaveScrollback(cp.SessionName, cp.ID, "%0", "")
	if err != nil {
		t.Fatalf("SaveScrollback() failed for empty content: %v", err)
	}

	loaded, err := storage.LoadScrollback(cp.SessionName, cp.ID, "%0")
	if err != nil {
		t.Fatalf("LoadScrollback() failed: %v", err)
	}

	if loaded != "" {
		t.Errorf("expected empty content, got %q", loaded)
	}
}

// bd-32ck: Tests for assignment and BV snapshot checkpoint fields

func TestCheckpointOptions_Assignments(t *testing.T) {
	// Test default options have assignments enabled
	opts := defaultOptions()
	if !opts.captureAssignments {
		t.Error("defaultOptions().captureAssignments should be true")
	}

	// Test WithAssignments(false)
	opts = checkpointOptions{captureAssignments: true}
	WithAssignments(false)(&opts)
	if opts.captureAssignments {
		t.Error("WithAssignments(false) should set captureAssignments to false")
	}

	// Test WithAssignments(true)
	opts = checkpointOptions{captureAssignments: false}
	WithAssignments(true)(&opts)
	if !opts.captureAssignments {
		t.Error("WithAssignments(true) should set captureAssignments to true")
	}
}

func TestCheckpointOptions_BVSnapshot(t *testing.T) {
	// Test default options have BV snapshot enabled
	opts := defaultOptions()
	if !opts.captureBVSnapshot {
		t.Error("defaultOptions().captureBVSnapshot should be true")
	}

	// Test WithBVSnapshot(false)
	opts = checkpointOptions{captureBVSnapshot: true}
	WithBVSnapshot(false)(&opts)
	if opts.captureBVSnapshot {
		t.Error("WithBVSnapshot(false) should set captureBVSnapshot to false")
	}

	// Test WithBVSnapshot(true)
	opts = checkpointOptions{captureBVSnapshot: false}
	WithBVSnapshot(true)(&opts)
	if !opts.captureBVSnapshot {
		t.Error("WithBVSnapshot(true) should set captureBVSnapshot to true")
	}
}

func TestCheckpoint_WithAssignments(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	// Create checkpoint with assignments
	assignedAt := time.Date(2025, 12, 10, 12, 0, 0, 0, time.UTC)
	cp := &Checkpoint{
		ID:          "20251210-120000-assign",
		Name:        "test-assign",
		SessionName: "myproject",
		WorkingDir:  "/tmp/myproject",
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
		Assignments: []AssignmentSnapshot{
			{
				BeadID:     "bd-1234",
				BeadTitle:  "Fix the widget",
				Pane:       1,
				AgentType:  "claude",
				AgentName:  "BlueLake",
				Status:     "working",
				AssignedAt: assignedAt,
			},
			{
				BeadID:     "bd-5678",
				BeadTitle:  "Add feature X",
				Pane:       2,
				AgentType:  "codex",
				Status:     "assigned",
				AssignedAt: assignedAt,
			},
		},
	}

	// Save checkpoint
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	// Load checkpoint
	loaded, err := storage.Load(cp.SessionName, cp.ID)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	// Verify assignments were preserved
	if len(loaded.Assignments) != 2 {
		t.Fatalf("len(Assignments) = %d, want 2", len(loaded.Assignments))
	}

	a0 := loaded.Assignments[0]
	if a0.BeadID != "bd-1234" {
		t.Errorf("Assignments[0].BeadID = %q, want bd-1234", a0.BeadID)
	}
	if a0.BeadTitle != "Fix the widget" {
		t.Errorf("Assignments[0].BeadTitle = %q, want 'Fix the widget'", a0.BeadTitle)
	}
	if a0.Pane != 1 {
		t.Errorf("Assignments[0].Pane = %d, want 1", a0.Pane)
	}
	if a0.AgentType != "claude" {
		t.Errorf("Assignments[0].AgentType = %q, want claude", a0.AgentType)
	}
	if a0.AgentName != "BlueLake" {
		t.Errorf("Assignments[0].AgentName = %q, want BlueLake", a0.AgentName)
	}
	if a0.Status != "working" {
		t.Errorf("Assignments[0].Status = %q, want working", a0.Status)
	}

	a1 := loaded.Assignments[1]
	if a1.BeadID != "bd-5678" {
		t.Errorf("Assignments[1].BeadID = %q, want bd-5678", a1.BeadID)
	}
	if a1.AgentName != "" {
		t.Errorf("Assignments[1].AgentName = %q, want empty", a1.AgentName)
	}
}

func TestCheckpoint_WithBVSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	// Create checkpoint with BV snapshot
	capturedAt := time.Date(2025, 12, 10, 12, 0, 0, 0, time.UTC)
	cp := &Checkpoint{
		ID:          "20251210-120000-bv",
		Name:        "test-bv",
		SessionName: "myproject",
		WorkingDir:  "/tmp/myproject",
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
		BVSummary: &BVSnapshot{
			OpenCount:       15,
			ActionableCount: 8,
			BlockedCount:    5,
			InProgressCount: 2,
			TopPicks:        []string{"bd-1234", "bd-5678", "bd-9abc"},
			CapturedAt:      capturedAt,
		},
	}

	// Save checkpoint
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	// Load checkpoint
	loaded, err := storage.Load(cp.SessionName, cp.ID)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	// Verify BV snapshot was preserved
	if loaded.BVSummary == nil {
		t.Fatal("BVSummary should not be nil")
	}

	bv := loaded.BVSummary
	if bv.OpenCount != 15 {
		t.Errorf("BVSummary.OpenCount = %d, want 15", bv.OpenCount)
	}
	if bv.ActionableCount != 8 {
		t.Errorf("BVSummary.ActionableCount = %d, want 8", bv.ActionableCount)
	}
	if bv.BlockedCount != 5 {
		t.Errorf("BVSummary.BlockedCount = %d, want 5", bv.BlockedCount)
	}
	if bv.InProgressCount != 2 {
		t.Errorf("BVSummary.InProgressCount = %d, want 2", bv.InProgressCount)
	}
	if len(bv.TopPicks) != 3 {
		t.Fatalf("len(TopPicks) = %d, want 3", len(bv.TopPicks))
	}
	if bv.TopPicks[0] != "bd-1234" {
		t.Errorf("TopPicks[0] = %q, want bd-1234", bv.TopPicks[0])
	}
}

func TestCheckpoint_BackwardCompatibility_NoAssignments(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	// Create checkpoint WITHOUT assignments or BV snapshot (simulates old checkpoint)
	cp := &Checkpoint{
		ID:          "20251210-120000-old",
		Name:        "old-checkpoint",
		SessionName: "myproject",
		WorkingDir:  "/tmp/myproject",
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
		Git: GitState{
			Branch: "main",
			Commit: "abc123",
		},
		// Note: Assignments and BVSummary are not set (nil/empty)
	}

	// Save checkpoint
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	// Load checkpoint
	loaded, err := storage.Load(cp.SessionName, cp.ID)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	// Verify other fields work and new fields are nil/empty
	if loaded.Git.Branch != "main" {
		t.Errorf("Git.Branch = %q, want main", loaded.Git.Branch)
	}
	if len(loaded.Assignments) != 0 {
		t.Errorf("len(Assignments) = %d, want 0", len(loaded.Assignments))
	}
	if loaded.BVSummary != nil {
		t.Errorf("BVSummary should be nil for old checkpoints")
	}
}

func TestCheckpoint_WithBothAssignmentsAndBVSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	// Create checkpoint with both assignments and BV snapshot
	now := time.Now()
	cp := &Checkpoint{
		ID:          "20251210-120000-full",
		Name:        "full-checkpoint",
		SessionName: "myproject",
		WorkingDir:  "/tmp/myproject",
		CreatedAt:   now,
		Session: SessionState{Panes: []PaneState{
			{Index: 0, ID: "%0"},
			{Index: 1, ID: "%1"},
			{Index: 2, ID: "%2"},
		}},
		Git: GitState{
			Branch:  "feature/test",
			Commit:  "def456",
			IsDirty: true,
		},
		PaneCount: 3,
		Assignments: []AssignmentSnapshot{
			{
				BeadID:     "bd-abc",
				BeadTitle:  "Task A",
				Pane:       0,
				AgentType:  "gemini",
				Status:     "working",
				AssignedAt: now,
			},
		},
		BVSummary: &BVSnapshot{
			OpenCount:       10,
			ActionableCount: 5,
			BlockedCount:    3,
			InProgressCount: 2,
			TopPicks:        []string{"bd-abc"},
			CapturedAt:      now,
		},
	}

	// Save checkpoint
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	// Load checkpoint
	loaded, err := storage.Load(cp.SessionName, cp.ID)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	// Verify all fields
	if loaded.Git.Branch != "feature/test" {
		t.Errorf("Git.Branch = %q, want feature/test", loaded.Git.Branch)
	}
	if loaded.PaneCount != 3 {
		t.Errorf("PaneCount = %d, want 3", loaded.PaneCount)
	}
	if len(loaded.Assignments) != 1 {
		t.Fatalf("len(Assignments) = %d, want 1", len(loaded.Assignments))
	}
	if loaded.Assignments[0].AgentType != "gemini" {
		t.Errorf("Assignments[0].AgentType = %q, want gemini", loaded.Assignments[0].AgentType)
	}
	if loaded.BVSummary == nil {
		t.Fatal("BVSummary should not be nil")
	}
	if loaded.BVSummary.ActionableCount != 5 {
		t.Errorf("BVSummary.ActionableCount = %d, want 5", loaded.BVSummary.ActionableCount)
	}
}
