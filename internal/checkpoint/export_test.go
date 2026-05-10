package checkpoint

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

// =============================================================================
// DefaultImportOptions (bd-9czd7)
// =============================================================================

func TestDefaultImportOptions(t *testing.T) {
	t.Parallel()
	opts := DefaultImportOptions()
	if !opts.VerifyChecksums {
		t.Error("VerifyChecksums should default to true")
	}
	if opts.AllowOverwrite {
		t.Error("AllowOverwrite should default to false")
	}
	if opts.TargetSession != "" {
		t.Errorf("TargetSession should be empty, got %q", opts.TargetSession)
	}
	if opts.TargetDir != "" {
		t.Errorf("TargetDir should be empty, got %q", opts.TargetDir)
	}
}

// =============================================================================
// GetRedactionConfig / SetRedactionConfig round-trip (bd-9czd7)
// =============================================================================

func TestGetRedactionConfig_Default(t *testing.T) {
	// Save and restore global state
	SetRedactionConfig(nil)
	t.Cleanup(func() { SetRedactionConfig(nil) })

	cfg := GetRedactionConfig()
	if cfg != nil {
		t.Error("GetRedactionConfig should return nil when not set")
	}
}

func TestGetRedactionConfig_SetAndGet(t *testing.T) {
	SetRedactionConfig(nil)
	t.Cleanup(func() { SetRedactionConfig(nil) })

	original := &redaction.Config{
		Mode: redaction.ModeRedact,
	}
	SetRedactionConfig(original)

	got := GetRedactionConfig()
	if got == nil {
		t.Fatal("GetRedactionConfig returned nil after Set")
	}
	if got.Mode != redaction.ModeRedact {
		t.Errorf("Mode = %v, want ModeRedact", got.Mode)
	}

	// Verify it returns a copy, not the same pointer
	got.Mode = redaction.ModeWarn
	got2 := GetRedactionConfig()
	if got2.Mode != redaction.ModeRedact {
		t.Error("GetRedactionConfig should return a copy, not shared state")
	}
}

// bd-pmdpn: Get/Set must deep-copy the reference-typed fields on
// redaction.Config so a caller mutating Allowlist/ExtraPatterns/
// DisabledCategories on either input or output cannot reach into the
// stored state. Pre-fix the existing TestGetRedactionConfig_SetAndGet
// only exercised the Mode value-type field; the slice/map fields
// were aliased.
func TestGetRedactionConfig_DeepCopiesSliceMapFields(t *testing.T) {
	SetRedactionConfig(nil)
	t.Cleanup(func() { SetRedactionConfig(nil) })

	original := &redaction.Config{
		Mode:               redaction.ModeRedact,
		Allowlist:          []string{"^foo$"},
		ExtraPatterns:      map[redaction.Category][]string{"api_key": {"^xyz$"}},
		DisabledCategories: []redaction.Category{"email"},
	}
	SetRedactionConfig(original)

	// Mutate the input AFTER Set — must not leak into the stored config.
	original.Allowlist[0] = "MUTATED_INPUT"
	original.ExtraPatterns["api_key"][0] = "MUTATED_INPUT"
	original.DisabledCategories[0] = "MUTATED_INPUT"

	got := GetRedactionConfig()
	if got.Allowlist[0] != "^foo$" {
		t.Errorf("Set leaked input mutation into Allowlist[0]: %q", got.Allowlist[0])
	}
	if got.ExtraPatterns["api_key"][0] != "^xyz$" {
		t.Errorf("Set leaked input mutation into ExtraPatterns: %q", got.ExtraPatterns["api_key"][0])
	}
	if got.DisabledCategories[0] != "email" {
		t.Errorf("Set leaked input mutation into DisabledCategories[0]: %q", got.DisabledCategories[0])
	}

	// Mutate the returned copy — must not leak into the stored config.
	got.Allowlist[0] = "MUTATED_OUTPUT"
	got.ExtraPatterns["api_key"][0] = "MUTATED_OUTPUT"
	got.DisabledCategories[0] = "MUTATED_OUTPUT"

	got2 := GetRedactionConfig()
	if got2.Allowlist[0] != "^foo$" {
		t.Errorf("Get leaked output mutation into Allowlist[0]: %q", got2.Allowlist[0])
	}
	if got2.ExtraPatterns["api_key"][0] != "^xyz$" {
		t.Errorf("Get leaked output mutation into ExtraPatterns: %q", got2.ExtraPatterns["api_key"][0])
	}
	if got2.DisabledCategories[0] != "email" {
		t.Errorf("Get leaked output mutation into DisabledCategories[0]: %q", got2.DisabledCategories[0])
	}
}

// =============================================================================
// Storage.GitPatchPath (bd-9czd7)
// =============================================================================

func TestGitPatchPath(t *testing.T) {
	t.Parallel()
	storage := NewStorageWithDir("/base/dir")
	got := storage.GitPatchPath("my-session", "chk-123")
	want := filepath.Join("/base/dir", "my-session", "chk-123", GitPatchFile)
	if got != want {
		t.Errorf("GitPatchPath = %q, want %q", got, want)
	}
}

// =============================================================================
// Existing tests below
// =============================================================================

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
				{ID: "%0", Index: 0},
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
	cp.Session.Panes[0].ScrollbackFile = "panes/pane__0.txt"
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Failed to save checkpoint with scrollback reference: %v", err)
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
	if len(manifest.Files) < 3 {
		t.Errorf("FileCount = %d, want at least 3", len(manifest.Files))
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
	if !foundFiles[SessionFile] {
		t.Error("Archive missing session.json")
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
	if !foundFiles[SessionFile] {
		t.Error("Zip missing session.json")
	}
}

func TestExport_Zip_WithScrollbackAndRedaction(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-export-zip-redact")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)

	sessionName := "test-session"
	checkpointID := "20251210-143052-zip-redact"

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

	if err := storage.Save(cp); err != nil {
		t.Fatalf("Failed to save checkpoint: %v", err)
	}

	// Create scrollback file with a secret
	panesDir := storage.PanesDirPath(sessionName, checkpointID)
	scrollbackPath := filepath.Join(panesDir, "pane__0.txt")
	scrollbackContent := "normal output\nAKIAIOSFODNN7EXAMPLE secret in scrollback\nmore output"
	if err := os.WriteFile(scrollbackPath, []byte(scrollbackContent), 0644); err != nil {
		t.Fatalf("Failed to create scrollback file: %v", err)
	}
	cp.Session.Panes[0].ScrollbackFile = "panes/pane__0.txt"
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Failed to save checkpoint with scrollback reference: %v", err)
	}

	// Enable redaction
	SetRedactionConfig(&redaction.Config{Mode: redaction.ModeRedact})
	t.Cleanup(func() { SetRedactionConfig(nil) })

	outputPath := filepath.Join(tmpDir, "test-export-redact.zip")
	opts := DefaultExportOptions()
	opts.Format = FormatZip
	opts.RedactSecrets = true

	manifest, err := storage.Export(sessionName, checkpointID, outputPath, opts)
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	if len(manifest.Files) < 2 {
		t.Errorf("Expected at least 2 files (metadata + scrollback), got %d", len(manifest.Files))
	}

	content := string(readZipEntry(t, outputPath, "panes/pane__0.txt"))
	if strings.Contains(content, "AKIAIOSFODNN7EXAMPLE") {
		t.Error("Expected AWS key to be redacted in exported scrollback")
	}
	if !strings.Contains(content, "normal output") {
		t.Error("Expected normal output to be preserved in exported scrollback")
	}
}

func TestExport_Zip_RedactsCompressedScrollback(t *testing.T) {
	assertExportRedactsCompressedScrollback(t, FormatZip, "test-export-redact-gzip.zip", readZipEntry)
}

func TestExport_TarGz_RedactsCompressedScrollback(t *testing.T) {
	assertExportRedactsCompressedScrollback(t, FormatTarGz, "test-export-redact-gzip.tar.gz", readTarGzEntry)
}

func assertExportRedactsCompressedScrollback(t *testing.T, format ExportFormat, archiveName string, readEntry func(*testing.T, string, string) []byte) {
	t.Helper()

	tmpDir := t.TempDir()
	exportStorage := NewStorageWithDir(filepath.Join(tmpDir, "export"))
	importStorage := NewStorageWithDir(filepath.Join(tmpDir, "import"))

	sessionName := "test-session"
	checkpointID := "20251210-143052-zip-redact-gzip"
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

	if err := exportStorage.Save(cp); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	scrollbackContent := "normal output\nAKIAIOSFODNN7EXAMPLE secret in scrollback\nmore output"
	compressed, err := gzipCompress([]byte(scrollbackContent))
	if err != nil {
		t.Fatalf("gzipCompress failed: %v", err)
	}
	scrollbackFile, err := exportStorage.SaveCompressedScrollback(sessionName, checkpointID, "%0", compressed)
	if err != nil {
		t.Fatalf("SaveCompressedScrollback failed: %v", err)
	}
	cp.Session.Panes[0].ScrollbackFile = scrollbackFile
	cp.Session.Panes[0].ScrollbackLines = countLines(scrollbackContent)
	cp.Session.Panes[0].Scrollback = &ScrollbackArtifactSummary{
		Captured:          true,
		ArtifactPreserved: true,
		Compacted:         true,
		Compression:       scrollbackCompressionGzip,
		LineCount:         countLines(scrollbackContent),
		RawBytes:          len(scrollbackContent),
		StoredBytes:       int64(len(compressed)),
	}
	if err := exportStorage.Save(cp); err != nil {
		t.Fatalf("Save with compressed scrollback reference failed: %v", err)
	}

	SetRedactionConfig(&redaction.Config{Mode: redaction.ModeRedact})
	t.Cleanup(func() { SetRedactionConfig(nil) })

	outputPath := filepath.Join(tmpDir, archiveName)
	opts := DefaultExportOptions()
	opts.Format = format
	opts.RedactSecrets = true
	if _, err := exportStorage.Export(sessionName, checkpointID, outputPath, opts); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	archivedData := readEntry(t, outputPath, scrollbackFile)
	plainData, err := gzipDecompress(archivedData)
	if err != nil {
		t.Fatalf("exported compressed scrollback is not valid gzip: %v", err)
	}
	plainText := string(plainData)
	if strings.Contains(plainText, "AKIAIOSFODNN7EXAMPLE") {
		t.Fatal("expected AWS key to be redacted in compressed exported scrollback")
	}
	if !strings.Contains(plainText, "normal output") {
		t.Fatal("expected normal output to be preserved in compressed exported scrollback")
	}

	sessionData := readEntry(t, outputPath, SessionFile)
	var archivedSession SessionState
	if err := json.Unmarshal(sessionData, &archivedSession); err != nil {
		t.Fatalf("failed to parse archived session.json: %v", err)
	}
	summary := archivedSession.Panes[0].Scrollback
	if summary == nil {
		t.Fatal("archived compressed scrollback summary is nil")
	}
	if summary.StoredBytes != int64(len(archivedData)) {
		t.Fatalf("archived summary StoredBytes = %d, want %d", summary.StoredBytes, len(archivedData))
	}
	if summary.RawBytes != len(plainData) {
		t.Fatalf("archived summary RawBytes = %d, want %d", summary.RawBytes, len(plainData))
	}
	if summary.LineCount != countLines(plainText) {
		t.Fatalf("archived summary LineCount = %d, want %d", summary.LineCount, countLines(plainText))
	}
	if !summary.Compacted || summary.Compression != scrollbackCompressionGzip {
		t.Fatalf("archived summary compaction = %v/%q, want gzip", summary.Compacted, summary.Compression)
	}
	if cp.Session.Panes[0].Scrollback.StoredBytes != int64(len(compressed)) {
		t.Fatalf("export mutated original summary StoredBytes = %d, want %d", cp.Session.Panes[0].Scrollback.StoredBytes, len(compressed))
	}

	imported, err := importStorage.Import(outputPath, ImportOptions{})
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}
	loaded, err := importStorage.LoadPaneScrollback(imported.SessionName, imported.ID, imported.Session.Panes[0])
	if err != nil {
		t.Fatalf("LoadPaneScrollback imported compressed artifact failed: %v", err)
	}
	if strings.Contains(loaded, "AKIAIOSFODNN7EXAMPLE") {
		t.Fatal("imported compressed scrollback still contains original AWS key")
	}
	if !strings.Contains(loaded, "normal output") {
		t.Fatal("imported compressed scrollback lost normal output")
	}
}

func TestExport_TarGz_FailsWhenReferencedScrollbackMissing(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "missing-scrollback-session"
	checkpointID := "20251210-143052-missing-scrollback"

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

	_, err := storage.Export(
		sessionName,
		checkpointID,
		filepath.Join(tmpDir, "missing-scrollback.tar.gz"),
		DefaultExportOptions(),
	)
	if err == nil {
		t.Fatal("expected export to fail when referenced scrollback file is missing")
	}
	if !strings.Contains(err.Error(), "artifact file does not exist") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExport_Zip_FailsWhenReferencedGitPatchMissing(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "missing-patch-session"
	checkpointID := "20251210-143052-missing-patch"

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Git: GitState{
			PatchFile: GitPatchFile,
		},
	}

	cpDir := storage.CheckpointDir(sessionName, checkpointID)
	if err := os.MkdirAll(cpDir, 0755); err != nil {
		t.Fatalf("MkdirAll() failed: %v", err)
	}
	if err := writeJSON(filepath.Join(cpDir, MetadataFile), cp); err != nil {
		t.Fatalf("write metadata failed: %v", err)
	}
	if err := writeJSON(filepath.Join(cpDir, SessionFile), cp.Session); err != nil {
		t.Fatalf("write session failed: %v", err)
	}

	opts := DefaultExportOptions()
	opts.Format = FormatZip

	_, err := storage.Export(
		sessionName,
		checkpointID,
		filepath.Join(tmpDir, "missing-patch.zip"),
		opts,
	)
	if err == nil {
		t.Fatal("expected export to fail when referenced git patch file is missing")
	}
	if !strings.Contains(err.Error(), "artifact file does not exist") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExport_TarGz_RejectsSymlinkArtifactReference(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "symlink-export-session"
	checkpointID := "20251210-143052-symlink-export"
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

	_, err := storage.Export(
		sessionName,
		checkpointID,
		filepath.Join(tmpDir, "symlink-export.tar.gz"),
		DefaultExportOptions(),
	)
	if err == nil {
		t.Fatal("expected export to reject symlink-backed artifact")
	}
	if !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("unexpected error: %v", err)
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
	if _, err := os.Stat(filepath.Join(importStorage.CheckpointDir(sessionName, checkpointID), SessionFile)); err != nil {
		t.Fatalf("imported checkpoint missing session.json: %v", err)
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
	if _, err := os.Stat(filepath.Join(importStorage.CheckpointDir(sessionName, checkpointID), SessionFile)); err != nil {
		t.Fatalf("imported zip checkpoint missing session.json: %v", err)
	}
}

func TestImport_WritesPrivateCheckpointFiles(t *testing.T) {
	testImportWritesPrivateCheckpointFiles(t, FormatTarGz)
}

func TestImportZip_WritesPrivateCheckpointFiles(t *testing.T) {
	testImportWritesPrivateCheckpointFiles(t, FormatZip)
}

func testImportWritesPrivateCheckpointFiles(t *testing.T, format ExportFormat) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "ntm-import-perms-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	exportStorage := NewStorageWithDir(filepath.Join(tmpDir, "export"))
	importStorage := NewStorageWithDir(filepath.Join(tmpDir, "import"))

	sessionName := "perm-session"
	checkpointID := "20251210-143052-perms"
	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session: SessionState{
			Panes: []PaneState{{ID: "%0", Index: 0, Title: "main"}},
		},
		Git: GitState{
			Branch:  "main",
			Commit:  "abc123",
			IsDirty: true,
		},
		PaneCount: 1,
	}

	if err := exportStorage.Save(cp); err != nil {
		t.Fatalf("Failed to save checkpoint: %v", err)
	}
	scrollbackFile, err := exportStorage.SaveScrollback(sessionName, checkpointID, "%0", "hello from scrollback")
	if err != nil {
		t.Fatalf("Failed to save scrollback: %v", err)
	}
	if err := exportStorage.SaveGitPatch(sessionName, checkpointID, "diff --git a/file b/file"); err != nil {
		t.Fatalf("Failed to save git patch: %v", err)
	}
	if err := exportStorage.SaveGitStatus(sessionName, checkpointID, "On branch main\nChanges not staged for commit:\n"); err != nil {
		t.Fatalf("Failed to save git status: %v", err)
	}
	cp.Session.Panes[0].ScrollbackFile = scrollbackFile
	cp.Session.Panes[0].ScrollbackLines = 1
	cp.Git.PatchFile = GitPatchFile
	cp.Git.StatusFile = GitStatusFile
	if err := exportStorage.Save(cp); err != nil {
		t.Fatalf("Failed to resave checkpoint metadata: %v", err)
	}

	archivePath := filepath.Join(tmpDir, "checkpoint.tar.gz")
	opts := DefaultExportOptions()
	if format == FormatZip {
		archivePath = filepath.Join(tmpDir, "checkpoint.zip")
		opts.Format = FormatZip
	}
	if _, err := exportStorage.Export(sessionName, checkpointID, archivePath, opts); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	imported, err := importStorage.Import(archivePath, ImportOptions{})
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	cpDir := importStorage.CheckpointDir(imported.SessionName, imported.ID)
	files := []string{MetadataFile, SessionFile, imported.Session.Panes[0].ScrollbackFile, imported.Git.PatchFile, imported.Git.StatusFile}
	for _, rel := range files {
		info, err := os.Stat(filepath.Join(cpDir, rel))
		if err != nil {
			t.Fatalf("Stat(%s) failed: %v", rel, err)
		}
		if got := info.Mode().Perm(); got != 0600 {
			t.Fatalf("%s permissions = %#o, want %#o", rel, got, os.FileMode(0600))
		}
	}
}

func TestExportImport_OverwritePreservesGitStatusArtifact(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-export-import-status-overwrite")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)
	sessionName := "status-session"
	checkpointID := "status-overwrite-cp"

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Git: GitState{
			Branch: "main",
			Commit: "abc123",
		},
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Failed to save checkpoint: %v", err)
	}
	if err := storage.SaveGitStatus(sessionName, checkpointID, "On branch main\nnothing to commit\n"); err != nil {
		t.Fatalf("Failed to save git status: %v", err)
	}
	cp.Git.StatusFile = GitStatusFile
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Failed to save checkpoint with git status reference: %v", err)
	}

	archivePath := filepath.Join(tmpDir, "status-overwrite.tar.gz")
	if _, err := storage.Export(sessionName, checkpointID, archivePath, DefaultExportOptions()); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	imported, err := storage.Import(archivePath, ImportOptions{
		VerifyChecksums: true,
		AllowOverwrite:  true,
	})
	if err != nil {
		t.Fatalf("Import with overwrite failed: %v", err)
	}
	if imported.Git.StatusFile != GitStatusFile {
		t.Fatalf("StatusFile = %q, want %q", imported.Git.StatusFile, GitStatusFile)
	}
	if _, err := os.Stat(filepath.Join(storage.CheckpointDir(sessionName, checkpointID), GitStatusFile)); err != nil {
		t.Fatalf("git status artifact missing after overwrite import: %v", err)
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

	loaded, err := importStorage.Load(newSession, checkpointID)
	if err != nil {
		t.Fatalf("Load after import failed: %v", err)
	}
	if loaded.SessionName != newSession {
		t.Errorf("loaded.SessionName = %s, want %s", loaded.SessionName, newSession)
	}
	if loaded.WorkingDir != newProject {
		t.Errorf("loaded.WorkingDir = %s, want %s", loaded.WorkingDir, newProject)
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

func TestRewriteCheckpointForExport(t *testing.T) {
	t.Parallel()

	t.Run("rewrites working dir", func(t *testing.T) {
		t.Parallel()
		cp := &Checkpoint{
			ID:         "test-id",
			Name:       "test",
			WorkingDir: "/data/projects/myapp",
		}
		result := rewriteCheckpointForExport(cp, DefaultExportOptions())
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
		_ = rewriteCheckpointForExport(cp, DefaultExportOptions())
		if cp.WorkingDir != "/original/path" {
			t.Errorf("original mutated: WorkingDir = %q", cp.WorkingDir)
		}
	})

	t.Run("empty working dir unchanged", func(t *testing.T) {
		t.Parallel()
		cp := &Checkpoint{
			WorkingDir: "",
		}
		result := rewriteCheckpointForExport(cp, DefaultExportOptions())
		if result.WorkingDir != "" {
			t.Errorf("WorkingDir = %q, want empty", result.WorkingDir)
		}
	})

	t.Run("clears omitted scrollback and git patch metadata", func(t *testing.T) {
		t.Parallel()
		cp := &Checkpoint{
			WorkingDir: "/data/projects/myapp",
			Session: SessionState{
				Panes: []PaneState{
					{
						ID:              "%0",
						ScrollbackFile:  "panes/pane__0.txt",
						ScrollbackLines: 123,
						Scrollback: &ScrollbackArtifactSummary{
							Captured:          true,
							ArtifactPreserved: true,
							Compacted:         true,
							Compression:       scrollbackCompressionGzip,
							LineCount:         123,
						},
					},
				},
			},
			Git: GitState{
				PatchFile: "git.patch",
			},
		}

		result := rewriteCheckpointForExport(cp, ExportOptions{
			RewritePaths:      true,
			IncludeScrollback: false,
			IncludeGitPatch:   false,
		})

		if result.Session.Panes[0].ScrollbackFile != "" || result.Session.Panes[0].ScrollbackLines != 0 {
			t.Fatalf("scrollback metadata = %+v, want cleared", result.Session.Panes[0])
		}
		if result.Session.Panes[0].Scrollback != nil {
			t.Fatalf("scrollback summary = %+v, want cleared", result.Session.Panes[0].Scrollback)
		}
		if result.Git.PatchFile != "" {
			t.Fatalf("Git.PatchFile = %q, want empty", result.Git.PatchFile)
		}
		if cp.Session.Panes[0].ScrollbackFile == "" || cp.Session.Panes[0].Scrollback == nil || cp.Git.PatchFile == "" {
			t.Fatal("original checkpoint was mutated")
		}
	})
}

func TestExportImport_OmitsScrollbackMetadataWhenScrollbackExcluded(t *testing.T) {
	tmpDir := t.TempDir()
	exportStorage := NewStorageWithDir(filepath.Join(tmpDir, "export"))
	importStorage := NewStorageWithDir(filepath.Join(tmpDir, "import"))
	sessionName := "test-session"
	checkpointID := "test-checkpoint"

	cp := &Checkpoint{
		ID:          checkpointID,
		Name:        "checkpoint-without-scrollback",
		SessionName: sessionName,
		WorkingDir:  "/data/projects/app",
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%0", Index: 0},
			},
		},
		PaneCount: 1,
	}

	if err := exportStorage.Save(cp); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	if _, err := exportStorage.SaveScrollback(sessionName, checkpointID, "%0", "line1\nline2"); err != nil {
		t.Fatalf("SaveScrollback failed: %v", err)
	}
	cp.Session.Panes[0].ScrollbackFile = "panes/pane__0.txt"
	cp.Session.Panes[0].ScrollbackLines = 42
	if err := exportStorage.Save(cp); err != nil {
		t.Fatalf("Save with scrollback reference failed: %v", err)
	}

	archivePath := filepath.Join(tmpDir, "no-scrollback.tar.gz")
	opts := DefaultExportOptions()
	opts.IncludeScrollback = false
	if _, err := exportStorage.Export(sessionName, checkpointID, archivePath, opts); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	sessionData := readTarGzEntry(t, archivePath, SessionFile)
	var archivedSession SessionState
	if err := json.Unmarshal(sessionData, &archivedSession); err != nil {
		t.Fatalf("failed to parse archived session.json: %v", err)
	}
	if archivedSession.Panes[0].ScrollbackFile != "" {
		t.Fatalf("archived session ScrollbackFile = %q, want empty", archivedSession.Panes[0].ScrollbackFile)
	}
	if archivedSession.Panes[0].ScrollbackLines != 0 {
		t.Fatalf("archived session ScrollbackLines = %d, want 0", archivedSession.Panes[0].ScrollbackLines)
	}

	imported, err := importStorage.Import(archivePath, ImportOptions{})
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	if imported.Session.Panes[0].ScrollbackFile != "" {
		t.Fatalf("ScrollbackFile = %q, want empty", imported.Session.Panes[0].ScrollbackFile)
	}
	if imported.Session.Panes[0].ScrollbackLines != 0 {
		t.Fatalf("ScrollbackLines = %d, want 0", imported.Session.Panes[0].ScrollbackLines)
	}
}

func TestExportZip_OmitsScrollbackMetadataFromSessionFileWhenScrollbackExcluded(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "zip-no-scrollback-session"
	checkpointID := "zip-no-scrollback-checkpoint"

	cp := &Checkpoint{
		ID:          checkpointID,
		Name:        "zip-checkpoint-without-scrollback",
		SessionName: sessionName,
		WorkingDir:  "/data/projects/app",
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%0", Index: 0},
			},
		},
		PaneCount: 1,
	}

	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	if _, err := storage.SaveScrollback(sessionName, checkpointID, "%0", "line1\nline2"); err != nil {
		t.Fatalf("SaveScrollback failed: %v", err)
	}
	cp.Session.Panes[0].ScrollbackFile = "panes/pane__0.txt"
	cp.Session.Panes[0].ScrollbackLines = 42
	if err := storage.Save(cp); err != nil {
		t.Fatalf("Save with scrollback reference failed: %v", err)
	}

	archivePath := filepath.Join(tmpDir, "no-scrollback.zip")
	opts := DefaultExportOptions()
	opts.Format = FormatZip
	opts.IncludeScrollback = false
	if _, err := storage.Export(sessionName, checkpointID, archivePath, opts); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	sessionData := readZipEntry(t, archivePath, SessionFile)
	var archivedSession SessionState
	if err := json.Unmarshal(sessionData, &archivedSession); err != nil {
		t.Fatalf("failed to parse archived session.json: %v", err)
	}
	if archivedSession.Panes[0].ScrollbackFile != "" {
		t.Fatalf("archived zip session ScrollbackFile = %q, want empty", archivedSession.Panes[0].ScrollbackFile)
	}
	if archivedSession.Panes[0].ScrollbackLines != 0 {
		t.Fatalf("archived zip session ScrollbackLines = %d, want 0", archivedSession.Panes[0].ScrollbackLines)
	}
}

func TestExportImport_OmitsGitPatchMetadataWhenPatchExcluded(t *testing.T) {
	tmpDir := t.TempDir()
	exportStorage := NewStorageWithDir(filepath.Join(tmpDir, "export"))
	importStorage := NewStorageWithDir(filepath.Join(tmpDir, "import"))
	sessionName := "test-session"
	checkpointID := "test-checkpoint"

	cp := &Checkpoint{
		ID:          checkpointID,
		Name:        "checkpoint-without-patch",
		SessionName: sessionName,
		WorkingDir:  "/data/projects/app",
		Session: SessionState{
			Panes: []PaneState{{ID: "%0", Index: 0}},
		},
		Git: GitState{
			Branch: "main",
			Commit: "abc123",
		},
		PaneCount: 1,
	}

	if err := exportStorage.Save(cp); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	patchPath := filepath.Join(exportStorage.CheckpointDir(sessionName, checkpointID), GitPatchFile)
	if err := os.WriteFile(patchPath, []byte("diff --git a/file b/file"), 0600); err != nil {
		t.Fatalf("WriteFile(%s) failed: %v", patchPath, err)
	}
	cp.Git.PatchFile = GitPatchFile
	if err := exportStorage.Save(cp); err != nil {
		t.Fatalf("Save with git patch reference failed: %v", err)
	}

	archivePath := filepath.Join(tmpDir, "no-patch.tar.gz")
	opts := DefaultExportOptions()
	opts.IncludeGitPatch = false
	if _, err := exportStorage.Export(sessionName, checkpointID, archivePath, opts); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	imported, err := importStorage.Import(archivePath, ImportOptions{})
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	if imported.Git.PatchFile != "" {
		t.Fatalf("Git.PatchFile = %q, want empty", imported.Git.PatchFile)
	}
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

func readTarGzEntry(t *testing.T, archivePath, entryName string) []byte {
	t.Helper()

	f, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("failed to open tar.gz archive: %v", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("failed to create gzip reader: %v", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("failed to iterate tar.gz archive: %v", err)
		}
		if header.Name == entryName {
			data, err := io.ReadAll(tr)
			if err != nil {
				t.Fatalf("failed to read tar.gz entry %s: %v", entryName, err)
			}
			return data
		}
	}

	t.Fatalf("tar.gz archive missing %s", entryName)
	return nil
}

func readZipEntry(t *testing.T, archivePath, entryName string) []byte {
	t.Helper()

	r, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatalf("failed to open zip archive: %v", err)
	}
	defer r.Close()

	for _, f := range r.File {
		if f.Name != entryName {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("failed to open zip entry %s: %v", entryName, err)
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("failed to read zip entry %s: %v", entryName, err)
		}
		return data
	}

	t.Fatalf("zip archive missing %s", entryName)
	return nil
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
