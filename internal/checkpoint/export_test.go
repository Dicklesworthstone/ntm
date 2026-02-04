package checkpoint

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

func TestExport_TarGz(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-export-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)

	sessionName := "test-session"
	checkpointID := "20251210-143052-export"

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		SessionName: sessionName,
		WorkingDir:  "/tmp/test-project",
		CreatedAt:   time.Now(),
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%0", Index: 0, ScrollbackFile: "panes/pane__0.txt"},
			},
			ActivePaneIndex: 0,
		},
		PaneCount: 1,
	}

	if err := storage.Save(cp); err != nil {
		t.Fatalf("Failed to save checkpoint: %v", err)
	}

	// Create scrollback file
	panesDir := storage.PanesDirPath(sessionName, checkpointID)
	scrollbackPath := filepath.Join(panesDir, "pane__0.txt")
	if err := os.WriteFile(scrollbackPath, []byte("test scrollback content"), 0644); err != nil {
		t.Fatalf("Failed to create scrollback file: %v", err)
	}

	outputPath := filepath.Join(tmpDir, "test-export.tar.gz")
	opts := DefaultExportOptions()
	opts.Format = FormatTarGz

	manifest, err := storage.Export(sessionName, checkpointID, outputPath, opts)
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	if manifest.SessionName != sessionName {
		t.Errorf("SessionName = %s, want %s", manifest.SessionName, sessionName)
	}
	if manifest.CheckpointID != checkpointID {
		t.Errorf("CheckpointID = %s, want %s", manifest.CheckpointID, checkpointID)
	}
	if len(manifest.Files) < 2 {
		t.Errorf("FileCount = %d, want at least 2", len(manifest.Files))
	}

	// Verify the archive is valid
	if _, err := os.Stat(outputPath); err != nil {
		t.Errorf("Archive file not created: %v", err)
	}

	// Open and verify archive contents
	f, err := os.Open(outputPath)
	if err != nil {
		t.Fatalf("Failed to open archive: %v", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("Failed to create gzip reader: %v", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	foundFiles := make(map[string]bool)
	for {
		header, err := tr.Next()
		if err != nil {
			break
		}
		foundFiles[header.Name] = true
	}

	if !foundFiles["MANIFEST.json"] {
		t.Error("Archive missing MANIFEST.json")
	}
	if !foundFiles[MetadataFile] {
		t.Error("Archive missing metadata.json")
	}
}

func TestExport_Zip(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-export-zip-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)

	sessionName := "test-session"
	checkpointID := "20251210-143052-zip"

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

	if err := storage.Save(cp); err != nil {
		t.Fatalf("Failed to save checkpoint: %v", err)
	}

	outputPath := filepath.Join(tmpDir, "test-export.zip")
	opts := DefaultExportOptions()
	opts.Format = FormatZip

	manifest, err := storage.Export(sessionName, checkpointID, outputPath, opts)
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	if manifest.SessionName != sessionName {
		t.Errorf("SessionName = %s, want %s", manifest.SessionName, sessionName)
	}

	// Verify zip archive contents
	r, err := zip.OpenReader(outputPath)
	if err != nil {
		t.Fatalf("Failed to open zip: %v", err)
	}
	defer r.Close()

	foundFiles := make(map[string]bool)
	for _, f := range r.File {
		foundFiles[f.Name] = true
	}

	if !foundFiles["MANIFEST.json"] {
		t.Error("Zip missing MANIFEST.json")
	}
	if !foundFiles[MetadataFile] {
		t.Error("Zip missing metadata.json")
	}
}

func TestImport_TarGz(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-import-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	exportStorage := NewStorageWithDir(filepath.Join(tmpDir, "export"))
	importStorage := NewStorageWithDir(filepath.Join(tmpDir, "import"))

	sessionName := "original-session"
	checkpointID := "20251210-143052-import"

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		SessionName: sessionName,
		WorkingDir:  "/original/path",
		CreatedAt:   time.Now(),
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%0", Index: 0, Title: "main", AgentType: "claude"},
			},
			ActivePaneIndex: 0,
		},
		PaneCount: 1,
	}

	if err := exportStorage.Save(cp); err != nil {
		t.Fatalf("Failed to save checkpoint: %v", err)
	}

	// Export
	archivePath := filepath.Join(tmpDir, "checkpoint.tar.gz")
	opts := DefaultExportOptions()
	if _, err := exportStorage.Export(sessionName, checkpointID, archivePath, opts); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// Import
	imported, err := importStorage.Import(archivePath, ImportOptions{})
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	if imported.SessionName != sessionName {
		t.Errorf("SessionName = %s, want %s", imported.SessionName, sessionName)
	}
	if imported.ID != checkpointID {
		t.Errorf("CheckpointID = %s, want %s", imported.ID, checkpointID)
	}
	if len(imported.Session.Panes) != 1 {
		t.Errorf("Pane count = %d, want 1", len(imported.Session.Panes))
	}
	if imported.Session.Panes[0].AgentType != "claude" {
		t.Errorf("AgentType = %s, want claude", imported.Session.Panes[0].AgentType)
	}
}

func TestImport_Zip(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-import-zip-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	exportStorage := NewStorageWithDir(filepath.Join(tmpDir, "export"))
	importStorage := NewStorageWithDir(filepath.Join(tmpDir, "import"))

	sessionName := "zip-session"
	checkpointID := "20251210-143052-zipimport"

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
	}

	if err := exportStorage.Save(cp); err != nil {
		t.Fatalf("Failed to save checkpoint: %v", err)
	}

	// Export as zip
	archivePath := filepath.Join(tmpDir, "checkpoint.zip")
	opts := DefaultExportOptions()
	opts.Format = FormatZip
	if _, err := exportStorage.Export(sessionName, checkpointID, archivePath, opts); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// Import
	imported, err := importStorage.Import(archivePath, ImportOptions{})
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	if imported.SessionName != sessionName {
		t.Errorf("SessionName = %s, want %s", imported.SessionName, sessionName)
	}
}

func TestImport_WithOverrides(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-import-override-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	exportStorage := NewStorageWithDir(filepath.Join(tmpDir, "export"))
	importStorage := NewStorageWithDir(filepath.Join(tmpDir, "import"))

	originalSession := "original-session"
	checkpointID := "20251210-143052-override"

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		SessionName: originalSession,
		WorkingDir:  "/original/path",
		CreatedAt:   time.Now(),
	}

	if err := exportStorage.Save(cp); err != nil {
		t.Fatalf("Failed to save checkpoint: %v", err)
	}

	// Export
	archivePath := filepath.Join(tmpDir, "checkpoint.tar.gz")
	if _, err := exportStorage.Export(originalSession, checkpointID, archivePath, DefaultExportOptions()); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// Import with overrides
	newSession := "new-session"
	newProject := "/new/project/path"
	imported, err := importStorage.Import(archivePath, ImportOptions{
		TargetSession: newSession,
		TargetDir:     newProject,
	})
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	if imported.SessionName != newSession {
		t.Errorf("SessionName = %s, want %s", imported.SessionName, newSession)
	}
	if imported.WorkingDir != newProject {
		t.Errorf("WorkingDir = %s, want %s", imported.WorkingDir, newProject)
	}
}

func TestExportImport_RoundTrip(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-roundtrip-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	exportStorage := NewStorageWithDir(filepath.Join(tmpDir, "export"))
	importStorage := NewStorageWithDir(filepath.Join(tmpDir, "import"))

	sessionName := "roundtrip-session"
	checkpointID := GenerateID("roundtrip")

	original := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		Name:        "My Checkpoint",
		Description: "A test checkpoint for roundtrip",
		SessionName: sessionName,
		WorkingDir:  "/test/project",
		CreatedAt:   time.Now().Truncate(time.Second),
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%0", Index: 0, Title: "main", AgentType: "claude", Width: 120, Height: 40},
				{ID: "%1", Index: 1, Title: "helper", AgentType: "codex", Width: 60, Height: 20},
			},
			Layout:          "main-horizontal",
			ActivePaneIndex: 0,
		},
		Git: GitState{
			Branch:         "main",
			Commit:         "abc123def456",
			IsDirty:        true,
			StagedCount:    2,
			UnstagedCount:  3,
			UntrackedCount: 1,
		},
		PaneCount: 2,
	}

	if err := exportStorage.Save(original); err != nil {
		t.Fatalf("Failed to save checkpoint: %v", err)
	}

	// Export
	archivePath := filepath.Join(tmpDir, "roundtrip.tar.gz")
	manifest, err := exportStorage.Export(sessionName, checkpointID, archivePath, DefaultExportOptions())
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	t.Logf("Exported %d files", len(manifest.Files))

	// Import
	imported, err := importStorage.Import(archivePath, ImportOptions{})
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	// Verify key fields match
	if imported.Name != original.Name {
		t.Errorf("Name = %s, want %s", imported.Name, original.Name)
	}
	if imported.Description != original.Description {
		t.Errorf("Description = %s, want %s", imported.Description, original.Description)
	}
	if imported.PaneCount != original.PaneCount {
		t.Errorf("PaneCount = %d, want %d", imported.PaneCount, original.PaneCount)
	}
	if len(imported.Session.Panes) != len(original.Session.Panes) {
		t.Errorf("Pane count = %d, want %d", len(imported.Session.Panes), len(original.Session.Panes))
	}
	if imported.Git.Branch != original.Git.Branch {
		t.Errorf("Git.Branch = %s, want %s", imported.Git.Branch, original.Git.Branch)
	}
}

func TestRedactSecrets(t *testing.T) {
	SetRedactionConfig(&redaction.Config{
		Mode:      redaction.ModeWarn,
		Allowlist: []string{`token=NO_REDACT_SECRET_[0-9]+`},
	})
	t.Cleanup(func() { SetRedactionConfig(nil) })

	tests := []struct {
		name       string
		input      string
		shouldFind bool // whether we expect to find the original string after redaction
		category   string
	}{
		{
			name:       "aws key",
			input:      "AKIAIOSFODNN7EXAMPLE",
			shouldFind: false,
			category:   "AWS_ACCESS_KEY",
		},
		{
			name:       "api key pattern",
			input:      "api_key: myverysecretkeyvalue12345678",
			shouldFind: false,
			category:   "GENERIC_API_KEY",
		},
		{
			name:       "bearer token",
			input:      "Authorization: Bearer tok_abcdefghijklmnopqrstuvwxyz12345",
			shouldFind: false,
			category:   "BEARER_TOKEN",
		},
		{
			name:       "no secrets",
			input:      "Hello, this is normal text without any secrets",
			shouldFind: true,
			category:   "",
		},
		{
			name:       "allowlist bypass",
			input:      "token=NO_REDACT_SECRET_1234567890",
			shouldFind: true,
			category:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := redactSecrets([]byte(tt.input))
			found := strings.Contains(string(result), tt.input)
			if found != tt.shouldFind {
				t.Errorf("redactSecrets() found original = %v, want %v; result = %q", found, tt.shouldFind, result)
			}
			if tt.category != "" && !strings.Contains(string(result), "[REDACTED:"+tt.category+":") {
				t.Errorf("redactSecrets() missing category placeholder %s; result = %q", tt.category, result)
			}
		})
	}
}

// =============================================================================
// sha256sum
// =============================================================================

func TestSha256sum(t *testing.T) {
	t.Parallel()

	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		got := sha256sum(nil)
		// SHA256 of empty input is e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
		if got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
			t.Errorf("sha256sum(nil) = %q", got)
		}
	})

	t.Run("hello", func(t *testing.T) {
		t.Parallel()
		got := sha256sum([]byte("hello"))
		// SHA256 of "hello" is 2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824
		if got != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
			t.Errorf("sha256sum(hello) = %q", got)
		}
	})

	t.Run("deterministic", func(t *testing.T) {
		t.Parallel()
		a := sha256sum([]byte("test"))
		b := sha256sum([]byte("test"))
		if a != b {
			t.Errorf("sha256sum not deterministic: %q != %q", a, b)
		}
	})
}

// =============================================================================
// rewriteCheckpointPaths
// =============================================================================

func TestRewriteCheckpointPaths(t *testing.T) {
	t.Parallel()

	t.Run("rewrites working dir", func(t *testing.T) {
		t.Parallel()
		cp := &Checkpoint{
			ID:         "test-id",
			Name:       "test",
			WorkingDir: "/data/projects/myapp",
		}
		result := rewriteCheckpointPaths(cp)
		if result.WorkingDir != "${WORKING_DIR}" {
			t.Errorf("WorkingDir = %q, want ${WORKING_DIR}", result.WorkingDir)
		}
		if result.ID != "test-id" {
			t.Errorf("ID should be preserved: %q", result.ID)
		}
	})

	t.Run("does not mutate original", func(t *testing.T) {
		t.Parallel()
		cp := &Checkpoint{
			WorkingDir: "/original/path",
		}
		_ = rewriteCheckpointPaths(cp)
		if cp.WorkingDir != "/original/path" {
			t.Errorf("original mutated: WorkingDir = %q", cp.WorkingDir)
		}
	})

	t.Run("empty working dir unchanged", func(t *testing.T) {
		t.Parallel()
		cp := &Checkpoint{
			WorkingDir: "",
		}
		result := rewriteCheckpointPaths(cp)
		if result.WorkingDir != "" {
			t.Errorf("WorkingDir = %q, want empty", result.WorkingDir)
		}
	})
}

// =============================================================================
// isPathWithinDir
// =============================================================================

func TestIsPathWithinDir(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseDir string
		target  string
		want    bool
	}{
		{"within dir", "/base", "subdir/file.txt", true},
		{"same dir", "/base", "file.txt", true},
		{"traversal attack", "/base", "../../../etc/passwd", false},
		{"double dot in middle", "/base", "sub/../other/file.txt", true},
		{"absolute escape", "/base", "../../outside", false},
		{"current dir", "/base", ".", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isPathWithinDir(tc.baseDir, tc.target)
			if got != tc.want {
				t.Errorf("isPathWithinDir(%q, %q) = %v, want %v", tc.baseDir, tc.target, got, tc.want)
			}
		})
	}
}

// =============================================================================
// Checkpoint.HasGitPatch and Summary
// =============================================================================

func TestCheckpointHasGitPatch(t *testing.T) {
	t.Parallel()

	t.Run("has patch", func(t *testing.T) {
		t.Parallel()
		cp := &Checkpoint{Git: GitState{PatchFile: "patch.diff"}}
		if !cp.HasGitPatch() {
			t.Error("expected HasGitPatch() = true")
		}
	})

	t.Run("no patch", func(t *testing.T) {
		t.Parallel()
		cp := &Checkpoint{Git: GitState{}}
		if cp.HasGitPatch() {
			t.Error("expected HasGitPatch() = false")
		}
	})
}

func TestCheckpointSummary(t *testing.T) {
	t.Parallel()

	cp := &Checkpoint{Name: "my-checkpoint", ID: "abc123"}
	got := cp.Summary()
	if got != "my-checkpoint (abc123)" {
		t.Errorf("Summary() = %q, want %q", got, "my-checkpoint (abc123)")
	}
}
