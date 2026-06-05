package checkpoint

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// Helpers for building test archives
// =============================================================================

// buildTarGz creates a tar.gz archive from a map of filename→content.
func buildTarGz(t *testing.T, destPath string, files map[string][]byte) {
	t.Helper()
	f, err := os.Create(destPath)
	if err != nil {
		t.Fatalf("create tar.gz: %v", err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	for name, data := range files {
		hdr := &tar.Header{
			Name:    name,
			Mode:    0644,
			Size:    int64(len(data)),
			ModTime: time.Now(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header %s: %v", name, err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("write tar body %s: %v", name, err)
		}
	}
}

// buildZip creates a zip archive from a map of filename→content.
func buildZip(t *testing.T, destPath string, files map[string][]byte) {
	t.Helper()
	f, err := os.Create(destPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
}

func buildTarGzEntries(t *testing.T, destPath string, entries []struct {
	name string
	data []byte
}) {
	t.Helper()
	f, err := os.Create(destPath)
	if err != nil {
		t.Fatalf("create tar.gz: %v", err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	for _, entry := range entries {
		hdr := &tar.Header{
			Name:    entry.name,
			Mode:    0644,
			Size:    int64(len(entry.data)),
			ModTime: time.Now(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header %s: %v", entry.name, err)
		}
		if _, err := tw.Write(entry.data); err != nil {
			t.Fatalf("write tar body %s: %v", entry.name, err)
		}
	}
}

func buildZipEntries(t *testing.T, destPath string, entries []struct {
	name string
	data []byte
}) {
	t.Helper()
	f, err := os.Create(destPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	for _, entry := range entries {
		w, err := zw.Create(entry.name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", entry.name, err)
		}
		if _, err := w.Write(entry.data); err != nil {
			t.Fatalf("write zip entry %s: %v", entry.name, err)
		}
	}
}

// validCheckpointJSON returns a minimal valid checkpoint JSON blob.
func validCheckpointJSON(t *testing.T, sessionName, cpID string) []byte {
	t.Helper()
	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          cpID,
		SessionName: sessionName,
		WorkingDir:  "/tmp/test",
		CreatedAt:   time.Now(),
	}
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		t.Fatalf("marshal checkpoint: %v", err)
	}
	return data
}

func validSessionJSON(t *testing.T, session SessionState) []byte {
	t.Helper()
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		t.Fatalf("marshal session: %v", err)
	}
	return data
}

// =============================================================================
// Import: unknown format
// =============================================================================

func TestImport_UnknownFormat(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	// Create a dummy file with unsupported extension
	archivePath := filepath.Join(tmpDir, "archive.rar")
	if err := os.WriteFile(archivePath, []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := storage.Import(archivePath, ImportOptions{})
	if err == nil {
		t.Fatal("expected error for unknown format")
	}
	if !strings.Contains(err.Error(), "unknown archive format") {
		t.Errorf("unexpected error: %v", err)
	}
}

// =============================================================================
// Export: unsupported format
// =============================================================================

func TestExport_UnsupportedFormat(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          "test-cp",
		SessionName: "test-session",
		WorkingDir:  "/tmp/test",
		CreatedAt:   time.Now(),
	}
	if err := storage.Save(cp); err != nil {
		t.Fatal(err)
	}

	_, err := storage.Export("test-session", "test-cp", "out.bad", ExportOptions{
		Format: ExportFormat("unsupported"),
	})
	if err == nil {
		t.Fatal("expected error for unsupported format")
	}
	if !strings.Contains(err.Error(), "unsupported export format") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSaveRejectsNonCanonicalArtifactReference(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "save-invalid-artifact"
	checkpointID := "save-invalid-cp"
	cpDir := storage.CheckpointDir(sessionName, checkpointID)
	panesDir := filepath.Join(cpDir, PanesDir)
	if err := os.MkdirAll(panesDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(panesDir, "pane__0.txt"), []byte("scrollback"), 0644); err != nil {
		t.Fatal(err)
	}

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		SessionName: sessionName,
		WorkingDir:  "/tmp/test",
		CreatedAt:   time.Now(),
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%0", Index: 0, ScrollbackFile: "panes/./pane__0.txt"},
			},
		},
		PaneCount: 1,
	}

	err := storage.Save(cp)
	if err == nil {
		t.Fatal("expected save to reject non-canonical artifact reference")
	}
	if !strings.Contains(err.Error(), "non-canonical path") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExportRejectsNonCanonicalArtifactReference(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "export-invalid-artifact"
	checkpointID := "export-invalid-cp"
	cpDir := storage.CheckpointDir(sessionName, checkpointID)
	panesDir := filepath.Join(cpDir, PanesDir)
	if err := os.MkdirAll(panesDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(panesDir, "pane__0.txt"), []byte("scrollback"), 0644); err != nil {
		t.Fatal(err)
	}

	session := SessionState{
		Panes: []PaneState{
			{ID: "%0", Index: 0, ScrollbackFile: "panes/./pane__0.txt"},
		},
	}
	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		SessionName: sessionName,
		WorkingDir:  "/tmp/test",
		CreatedAt:   time.Now(),
		Session:     session,
		PaneCount:   1,
	}
	if err := writeJSON(filepath.Join(cpDir, MetadataFile), cp); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(cpDir, SessionFile), session); err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(tmpDir, "invalid-artifact.zip")
	_, err := storage.Export(sessionName, checkpointID, archivePath, ExportOptions{
		Format:            FormatZip,
		IncludeScrollback: true,
	})
	if err == nil {
		t.Fatal("expected export to reject non-canonical artifact reference")
	}
	if !strings.Contains(err.Error(), "non-canonical path") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// =============================================================================
// Export: auto-generated dest path
// =============================================================================

func TestExport_AutoDestPath_TarGz(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "auto-dest-session"
	cpID := "auto-dest-cp"
	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          cpID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
	}
	if err := storage.Save(cp); err != nil {
		t.Fatal(err)
	}

	// Change to tmpDir so the auto-generated file goes there
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	manifest, err := storage.Export(sessionName, cpID, "", DefaultExportOptions())
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}
	if manifest == nil {
		t.Fatal("manifest is nil")
	}

	// Check that auto-generated file was created with .tar.gz extension
	autoPath := filepath.Join(tmpDir, sessionName+"_"+cpID+".tar.gz")
	if _, err := os.Stat(autoPath); err != nil {
		t.Errorf("auto-generated archive not found at %s: %v", autoPath, err)
	}
}

func TestExport_EmptyFormatDefaultsToTarGz(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "empty-format-session"
	cpID := "empty-format-cp"
	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          cpID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
	}
	if err := storage.Save(cp); err != nil {
		t.Fatal(err)
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("failed to restore working directory: %v", err)
		}
	}()

	manifest, err := storage.Export(sessionName, cpID, "", ExportOptions{})
	if err != nil {
		t.Fatalf("Export with zero-value options failed: %v", err)
	}
	if manifest == nil {
		t.Fatal("manifest is nil")
	}

	autoPath := filepath.Join(tmpDir, sessionName+"_"+cpID+".tar.gz")
	if _, err := os.Stat(autoPath); err != nil {
		t.Fatalf("default tar.gz export not found at %s: %v", autoPath, err)
	}
}

func TestExport_AutoDestPath_Zip(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "auto-dest-zip"
	cpID := "auto-dest-zip-cp"
	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          cpID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
	}
	if err := storage.Save(cp); err != nil {
		t.Fatal(err)
	}

	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	opts := DefaultExportOptions()
	opts.Format = FormatZip
	manifest, err := storage.Export(sessionName, cpID, "", opts)
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}
	if manifest == nil {
		t.Fatal("manifest is nil")
	}

	autoPath := filepath.Join(tmpDir, sessionName+"_"+cpID+".zip")
	if _, err := os.Stat(autoPath); err != nil {
		t.Errorf("auto-generated zip not found at %s: %v", autoPath, err)
	}
}

// =============================================================================
// Import tar.gz: missing metadata.json
// =============================================================================

func TestImportTarGz_MissingMetadata(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	archive := filepath.Join(tmpDir, "no-meta.tar.gz")
	buildTarGz(t, archive, map[string][]byte{
		"MANIFEST.json": []byte(`{"version":1}`),
		"other.txt":     []byte("data"),
	})

	_, err := storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err == nil {
		t.Fatal("expected error for missing metadata")
	}
	if !strings.Contains(err.Error(), "archive missing") {
		t.Errorf("unexpected error: %v", err)
	}
}

// =============================================================================
// Import zip: missing metadata.json
// =============================================================================

func TestImportZip_MissingMetadata(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	archive := filepath.Join(tmpDir, "no-meta.zip")
	buildZip(t, archive, map[string][]byte{
		"MANIFEST.json": []byte(`{"version":1}`),
		"other.txt":     []byte("data"),
	})

	_, err := storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err == nil {
		t.Fatal("expected error for missing metadata")
	}
	if !strings.Contains(err.Error(), "archive missing") {
		t.Errorf("unexpected error: %v", err)
	}
}

// =============================================================================
// Import tar.gz: checksum mismatch
// =============================================================================

func TestImportTarGz_ChecksumMismatch(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cpJSON := validCheckpointJSON(t, "chk-session", "chk-cp-id")

	// Build manifest with wrong checksum
	manifest := &ExportManifest{
		Version:     1,
		SessionName: "chk-session",
		Checksums: map[string]string{
			MetadataFile: "0000000000000000000000000000000000000000000000000000000000000000",
		},
	}
	manifestJSON, _ := json.Marshal(manifest)

	archive := filepath.Join(tmpDir, "bad-checksum.tar.gz")
	buildTarGz(t, archive, map[string][]byte{
		MetadataFile:    cpJSON,
		"MANIFEST.json": manifestJSON,
	})

	_, err := storage.Import(archive, ImportOptions{VerifyChecksums: true})
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("unexpected error: %v", err)
	}
}

// =============================================================================
// Import zip: checksum mismatch
// =============================================================================

func TestImportZip_ChecksumMismatch(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cpJSON := validCheckpointJSON(t, "zip-chk-session", "zip-chk-cp")

	manifest := &ExportManifest{
		Version:     1,
		SessionName: "zip-chk-session",
		Checksums: map[string]string{
			MetadataFile: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		},
	}
	manifestJSON, _ := json.Marshal(manifest)

	archive := filepath.Join(tmpDir, "bad-checksum.zip")
	buildZip(t, archive, map[string][]byte{
		MetadataFile:    cpJSON,
		"MANIFEST.json": manifestJSON,
	})

	_, err := storage.Import(archive, ImportOptions{VerifyChecksums: true})
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestImportTarGz_VerifyChecksumsRequiresManifest(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cpJSON := validCheckpointJSON(t, "manifest-required-session", "manifest-required-cp")
	archive := filepath.Join(tmpDir, "manifest-required.tar.gz")
	buildTarGz(t, archive, map[string][]byte{
		MetadataFile: cpJSON,
	})

	_, err := storage.Import(archive, ImportOptions{VerifyChecksums: true})
	if err == nil {
		t.Fatal("expected error when checksum verification is requested without a manifest")
	}
	if !strings.Contains(err.Error(), "missing MANIFEST.json") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestImportZip_VerifyChecksumsRejectsUncheckedArchiveFiles(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cpJSON := validCheckpointJSON(t, "unchecked-session", "unchecked-cp")
	sessionJSON, err := json.Marshal(SessionState{})
	if err != nil {
		t.Fatal(err)
	}
	manifest := &ExportManifest{
		Version:     1,
		SessionName: "unchecked-session",
		Checksums: map[string]string{
			MetadataFile: sha256sum(cpJSON),
		},
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(tmpDir, "unchecked.zip")
	buildZip(t, archive, map[string][]byte{
		MetadataFile:    cpJSON,
		SessionFile:     sessionJSON,
		"MANIFEST.json": manifestJSON,
	})

	_, err = storage.Import(archive, ImportOptions{VerifyChecksums: true})
	if err == nil {
		t.Fatal("expected error for archive file missing from manifest checksum set")
	}
	if !strings.Contains(err.Error(), "manifest missing checksum for "+SessionFile) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestImportZip_VerifyChecksumsRejectsDriftedManifestFileEntry(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "drifted-manifest-session"
	checkpointID := "drifted-manifest-cp"
	cpJSON := validCheckpointJSON(t, sessionName, checkpointID)
	sessionJSON := validSessionJSON(t, SessionState{})

	manifest := &ExportManifest{
		Version:      1,
		SessionName:  sessionName,
		CheckpointID: checkpointID,
		Checksums: map[string]string{
			MetadataFile: sha256sum(cpJSON),
			SessionFile:  sha256sum(sessionJSON),
		},
		Files: []ManifestEntry{
			{
				Path:     MetadataFile,
				Size:     int64(len(cpJSON)),
				Checksum: strings.Repeat("0", 64),
			},
			{
				Path:     SessionFile,
				Size:     int64(len(sessionJSON)),
				Checksum: sha256sum(sessionJSON),
			},
		},
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(tmpDir, "drifted-manifest.zip")
	buildZip(t, archive, map[string][]byte{
		MetadataFile:    cpJSON,
		SessionFile:     sessionJSON,
		"MANIFEST.json": manifestJSON,
	})

	_, err = storage.Import(archive, ImportOptions{VerifyChecksums: true})
	if err == nil {
		t.Fatal("expected error for manifest files entry that disagrees with checksum map")
	}
	if !strings.Contains(err.Error(), "manifest entry checksum mismatch for "+MetadataFile) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// =============================================================================
// Import: manifest lists file not in archive
// =============================================================================

func TestImportTarGz_ManifestListsMissingFile(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cpJSON := validCheckpointJSON(t, "miss-session", "miss-cp")

	manifest := &ExportManifest{
		Version:     1,
		SessionName: "miss-session",
		Checksums: map[string]string{
			MetadataFile:       sha256sum(cpJSON),
			"nonexistent.file": "deadbeef",
		},
	}
	manifestJSON, _ := json.Marshal(manifest)

	archive := filepath.Join(tmpDir, "missing-file.tar.gz")
	buildTarGz(t, archive, map[string][]byte{
		MetadataFile:    cpJSON,
		"MANIFEST.json": manifestJSON,
	})

	_, err := storage.Import(archive, ImportOptions{VerifyChecksums: true})
	if err == nil {
		t.Fatal("expected error for missing file referenced in manifest")
	}
	if !strings.Contains(err.Error(), "manifest lists missing file") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestImportTarGz_RejectsScrollbackAliasToMetadata(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "alias-scrollback-session"
	checkpointID := "alias-scrollback-cp"
	session := SessionState{
		Panes: []PaneState{
			{ID: "%0", Index: 0, ScrollbackFile: MetadataFile},
		},
	}
	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session:     session,
		PaneCount:   1,
	}
	cpJSON, err := json.Marshal(cp)
	if err != nil {
		t.Fatal(err)
	}
	sessionJSON := validSessionJSON(t, session)

	archive := filepath.Join(tmpDir, "alias-scrollback.tar.gz")
	buildTarGz(t, archive, map[string][]byte{
		MetadataFile: cpJSON,
		SessionFile:  sessionJSON,
	})

	_, err = storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err == nil {
		t.Fatal("expected import to reject scrollback alias to metadata.json")
	}
	if !strings.Contains(err.Error(), "invalid scrollback path") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestImportZip_RejectsGitPatchAliasToMetadata(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "alias-git-patch-session"
	checkpointID := "alias-git-patch-cp"
	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Git: GitState{
			PatchFile: MetadataFile,
		},
	}
	cpJSON, err := json.Marshal(cp)
	if err != nil {
		t.Fatal(err)
	}
	sessionJSON := validSessionJSON(t, SessionState{})

	archive := filepath.Join(tmpDir, "alias-git-patch.zip")
	buildZip(t, archive, map[string][]byte{
		MetadataFile: cpJSON,
		SessionFile:  sessionJSON,
	})

	_, err = storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err == nil {
		t.Fatal("expected import to reject git patch alias to metadata.json")
	}
	if !strings.Contains(err.Error(), "invalid git patch path") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestImportZip_AllowsSafeCustomGitArtifactPaths(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "custom-git-artifacts-session"
	checkpointID := "custom-git-artifacts-cp"
	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Git: GitState{
			PatchFile:  "git/custom.patch",
			StatusFile: "git/custom.status",
		},
	}
	cpJSON, err := json.Marshal(cp)
	if err != nil {
		t.Fatal(err)
	}
	sessionJSON := validSessionJSON(t, SessionState{})

	archive := filepath.Join(tmpDir, "custom-git-artifacts.zip")
	buildZip(t, archive, map[string][]byte{
		MetadataFile:        cpJSON,
		SessionFile:         sessionJSON,
		"git/custom.patch":  []byte("diff --git a/custom.go b/custom.go\n"),
		"git/custom.status": []byte("M  custom.go\n"),
	})

	imported, err := storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err != nil {
		t.Fatalf("import with custom git artifact paths failed: %v", err)
	}
	if imported.Git.PatchFile != "git/custom.patch" {
		t.Fatalf("PatchFile = %q", imported.Git.PatchFile)
	}
	if imported.Git.StatusFile != "git/custom.status" {
		t.Fatalf("StatusFile = %q", imported.Git.StatusFile)
	}
}

// =============================================================================
// Import tar.gz: overwrite protection
// =============================================================================

func TestImportTarGz_OverwriteProtection(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "ow-session"
	cpID := "ow-cp-id"

	// Save a checkpoint first so the directory exists
	existing := &Checkpoint{
		Version:     CurrentVersion,
		ID:          cpID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
	}
	if err := storage.Save(existing); err != nil {
		t.Fatal(err)
	}

	// Build an archive for the same checkpoint
	cpJSON := validCheckpointJSON(t, sessionName, cpID)
	sessionJSON := validSessionJSON(t, SessionState{})
	archive := filepath.Join(tmpDir, "overwrite.tar.gz")
	buildTarGz(t, archive, map[string][]byte{
		MetadataFile: cpJSON,
		SessionFile:  sessionJSON,
	})

	// Import without AllowOverwrite should fail
	_, err := storage.Import(archive, ImportOptions{
		VerifyChecksums: false,
		AllowOverwrite:  false,
	})
	if err == nil {
		t.Fatal("expected error when overwriting without AllowOverwrite")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("unexpected error: %v", err)
	}

	// Import with AllowOverwrite should succeed
	_, err = storage.Import(archive, ImportOptions{
		VerifyChecksums: false,
		AllowOverwrite:  true,
	})
	if err != nil {
		t.Fatalf("import with AllowOverwrite failed: %v", err)
	}
}

func TestImportTarGz_AllowOverwriteRejectsStaleArtifacts(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "ow-stale-session"
	cpID := "ow-stale-cp-id"

	existing := &Checkpoint{
		Version:     CurrentVersion,
		ID:          cpID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%0", Index: 0},
			},
		},
	}
	if err := storage.Save(existing); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storage.PanesDirPath(sessionName, cpID), "pane__0.txt"), []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}
	existing.Session.Panes[0].ScrollbackFile = "panes/pane__0.txt"
	if err := storage.Save(existing); err != nil {
		t.Fatal(err)
	}

	cpJSON := validCheckpointJSON(t, sessionName, cpID)
	sessionJSON := validSessionJSON(t, SessionState{})
	archive := filepath.Join(tmpDir, "overwrite-stale.tar.gz")
	buildTarGz(t, archive, map[string][]byte{
		MetadataFile: cpJSON,
		SessionFile:  sessionJSON,
	})

	_, err := storage.Import(archive, ImportOptions{
		VerifyChecksums: false,
		AllowOverwrite:  true,
	})
	if err == nil {
		t.Fatal("expected stale-artifact error on overwrite import")
	}
	if !strings.Contains(err.Error(), "stale checkpoint artifact") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "panes/pane__0.txt") {
		t.Fatalf("expected stale pane artifact in error, got: %v", err)
	}
}

// =============================================================================
// Import zip: overwrite protection and AllowOverwrite
// =============================================================================

func TestImportZip_OverwriteProtection(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "ow-zip-session"
	cpID := "ow-zip-cp"

	existing := &Checkpoint{
		Version:     CurrentVersion,
		ID:          cpID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
	}
	if err := storage.Save(existing); err != nil {
		t.Fatal(err)
	}

	cpJSON := validCheckpointJSON(t, sessionName, cpID)
	sessionJSON := validSessionJSON(t, SessionState{})
	archive := filepath.Join(tmpDir, "overwrite.zip")
	buildZip(t, archive, map[string][]byte{
		MetadataFile: cpJSON,
		SessionFile:  sessionJSON,
	})

	// Without AllowOverwrite
	_, err := storage.Import(archive, ImportOptions{
		VerifyChecksums: false,
		AllowOverwrite:  false,
	})
	if err == nil {
		t.Fatal("expected error when overwriting without AllowOverwrite")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("unexpected error: %v", err)
	}

	// With AllowOverwrite
	_, err = storage.Import(archive, ImportOptions{
		VerifyChecksums: false,
		AllowOverwrite:  true,
	})
	if err != nil {
		t.Fatalf("import with AllowOverwrite failed: %v", err)
	}
}

func TestImportZip_AllowOverwriteRejectsStaleArtifacts(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "ow-zip-stale-session"
	cpID := "ow-zip-stale-cp"

	existing := &Checkpoint{
		Version:     CurrentVersion,
		ID:          cpID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
	}
	if err := storage.Save(existing); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storage.GitPatchPath(sessionName, cpID), []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}
	existing.Git.PatchFile = GitPatchFile
	if err := storage.Save(existing); err != nil {
		t.Fatal(err)
	}

	cpJSON := validCheckpointJSON(t, sessionName, cpID)
	sessionJSON := validSessionJSON(t, SessionState{})
	archive := filepath.Join(tmpDir, "overwrite-stale.zip")
	buildZip(t, archive, map[string][]byte{
		MetadataFile: cpJSON,
		SessionFile:  sessionJSON,
	})

	_, err := storage.Import(archive, ImportOptions{
		VerifyChecksums: false,
		AllowOverwrite:  true,
	})
	if err == nil {
		t.Fatal("expected stale-artifact error on overwrite zip import")
	}
	if !strings.Contains(err.Error(), "stale checkpoint artifact") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), GitPatchFile) {
		t.Fatalf("expected stale patch artifact in error, got: %v", err)
	}
}

// =============================================================================
// Import tar.gz: path traversal protection
// =============================================================================

func TestImportTarGz_PathTraversal(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cpJSON := validCheckpointJSON(t, "trav-session", "trav-cp")
	sessionJSON := validSessionJSON(t, SessionState{})

	archive := filepath.Join(tmpDir, "traversal.tar.gz")
	buildTarGz(t, archive, map[string][]byte{
		MetadataFile:                  cpJSON,
		SessionFile:                   sessionJSON,
		"../../../etc/evil-file.conf": []byte("pwned"),
	})

	_, err := storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
	if !strings.Contains(err.Error(), "path traversal") {
		t.Errorf("unexpected error: %v", err)
	}
}

// =============================================================================
// Import zip: path traversal protection
// =============================================================================

func TestImportZip_PathTraversal(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cpJSON := validCheckpointJSON(t, "ztrav-session", "ztrav-cp")
	sessionJSON := validSessionJSON(t, SessionState{})

	archive := filepath.Join(tmpDir, "traversal.zip")
	buildZip(t, archive, map[string][]byte{
		MetadataFile:                  cpJSON,
		SessionFile:                   sessionJSON,
		"../../../etc/evil-file.conf": []byte("pwned"),
	})

	_, err := storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err == nil {
		t.Fatal("expected error for path traversal in zip")
	}
	if !strings.Contains(err.Error(), "path traversal") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestImportTarGz_RejectsNonCanonicalArchivePath(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "noncanonical-session"
	checkpointID := "noncanonical-cp"
	session := SessionState{
		Panes: []PaneState{
			{ID: "%0", Index: 0, ScrollbackFile: "panes/./pane__0.txt"},
		},
	}
	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session:     session,
		PaneCount:   1,
	}
	cpJSON, err := json.Marshal(cp)
	if err != nil {
		t.Fatal(err)
	}
	sessionJSON := validSessionJSON(t, session)

	archive := filepath.Join(tmpDir, "noncanonical.tar.gz")
	buildTarGz(t, archive, map[string][]byte{
		MetadataFile:          cpJSON,
		SessionFile:           sessionJSON,
		"panes/./pane__0.txt": []byte("aliased scrollback"),
	})

	_, err = storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err == nil {
		t.Fatal("expected import to reject non-canonical archive path")
	}
	if !strings.Contains(err.Error(), "non-canonical path") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestImportZip_RejectsColonArchivePath(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "colon-path-session"
	checkpointID := "colon-path-cp"
	session := SessionState{
		Panes: []PaneState{
			{ID: "%0", Index: 0, ScrollbackFile: "panes/pane:0.txt"},
		},
	}
	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session:     session,
		PaneCount:   1,
	}
	cpJSON, err := json.Marshal(cp)
	if err != nil {
		t.Fatal(err)
	}
	sessionJSON := validSessionJSON(t, session)

	archive := filepath.Join(tmpDir, "colon-path.zip")
	buildZip(t, archive, map[string][]byte{
		MetadataFile:       cpJSON,
		SessionFile:        sessionJSON,
		"panes/pane:0.txt": []byte("colon path scrollback"),
	})

	_, err = storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err == nil {
		t.Fatal("expected import to reject colon archive path")
	}
	if !strings.Contains(err.Error(), "colon path segment") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestImportZip_AllowsCanonicalDirectoryEntries(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cpJSON := validCheckpointJSON(t, "zip-dir-session", "zip-dir-cp")
	sessionJSON := validSessionJSON(t, SessionState{})

	archive := filepath.Join(tmpDir, "with-dir.zip")
	buildZip(t, archive, map[string][]byte{
		MetadataFile: cpJSON,
		SessionFile:  sessionJSON,
		"panes/":     nil,
	})

	if _, err := storage.Import(archive, ImportOptions{VerifyChecksums: false}); err != nil {
		t.Fatalf("import with canonical directory entry failed: %v", err)
	}
}

// =============================================================================
// Import tar.gz: corrupt checkpoint JSON
// =============================================================================

func TestImportTarGz_CorruptCheckpointJSON(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	archive := filepath.Join(tmpDir, "corrupt.tar.gz")
	buildTarGz(t, archive, map[string][]byte{
		MetadataFile: []byte("{invalid json"),
	})

	_, err := storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err == nil {
		t.Fatal("expected error for corrupt JSON")
	}
	if !strings.Contains(err.Error(), "failed to parse checkpoint") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestImportTarGz_CorruptSessionJSON(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cpJSON := validCheckpointJSON(t, "corrupt-session-state", "corrupt-session-state-cp")
	archive := filepath.Join(tmpDir, "corrupt-session-state.tar.gz")
	buildTarGz(t, archive, map[string][]byte{
		MetadataFile: cpJSON,
		SessionFile:  []byte("{invalid json"),
	})

	_, err := storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err == nil {
		t.Fatal("expected error for corrupt session state JSON")
	}
	if !strings.Contains(err.Error(), "failed to parse session state") {
		t.Errorf("unexpected error: %v", err)
	}
}

// =============================================================================
// Import zip: corrupt checkpoint JSON
// =============================================================================

func TestImportZip_CorruptCheckpointJSON(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	archive := filepath.Join(tmpDir, "corrupt.zip")
	buildZip(t, archive, map[string][]byte{
		MetadataFile: []byte("{invalid json"),
	})

	_, err := storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err == nil {
		t.Fatal("expected error for corrupt JSON")
	}
	if !strings.Contains(err.Error(), "failed to parse checkpoint") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestImportZip_MismatchedSessionState(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          "mismatch-session-state-cp",
		SessionName: "mismatch-session-state",
		CreatedAt:   time.Now(),
		Session: SessionState{
			Panes: []PaneState{{ID: "%0", Index: 0, Title: "metadata"}},
		},
		PaneCount: 1,
	}
	cpJSON, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	sessionJSON, err := json.MarshalIndent(SessionState{
		Panes: []PaneState{{ID: "%0", Index: 0, Title: "session-file"}},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(tmpDir, "mismatched-session-state.zip")
	buildZip(t, archive, map[string][]byte{
		MetadataFile: cpJSON,
		SessionFile:  sessionJSON,
	})

	_, err = storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err == nil {
		t.Fatal("expected error for mismatched session state")
	}
	if !strings.Contains(err.Error(), "session.json does not match metadata.json") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestImportTarGz_MissingSessionState(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cpJSON := validCheckpointJSON(t, "missing-session-state", "missing-session-state-cp")
	archive := filepath.Join(tmpDir, "missing-session-state.tar.gz")
	buildTarGz(t, archive, map[string][]byte{
		MetadataFile: cpJSON,
	})

	_, err := storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err == nil {
		t.Fatal("expected missing session state to fail import")
	}
	if !strings.Contains(err.Error(), "archive missing "+SessionFile) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestImportZip_MissingSessionState(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cpJSON := validCheckpointJSON(t, "missing-session-state-zip", "missing-session-state-cp-zip")
	archive := filepath.Join(tmpDir, "missing-session-state.zip")
	buildZip(t, archive, map[string][]byte{
		MetadataFile: cpJSON,
	})

	_, err := storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err == nil {
		t.Fatal("expected missing session state to fail import")
	}
	if !strings.Contains(err.Error(), "archive missing "+SessionFile) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestImportTarGz_MissingReferencedScrollback(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          "missing-scrollback-cp",
		SessionName: "missing-scrollback-session",
		CreatedAt:   time.Now(),
		Session: SessionState{
			Panes: []PaneState{{
				ID:              "%0",
				Index:           0,
				ScrollbackFile:  "panes/pane__0.txt",
				ScrollbackLines: 12,
			}},
		},
		PaneCount: 1,
	}
	cpJSON, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	sessionJSON, err := json.MarshalIndent(cp.Session, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(tmpDir, "missing-scrollback.tar.gz")
	buildTarGz(t, archive, map[string][]byte{
		MetadataFile: cpJSON,
		SessionFile:  sessionJSON,
	})

	_, err = storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err == nil {
		t.Fatal("expected error for missing referenced scrollback")
	}
	if !strings.Contains(err.Error(), "archive missing scrollback referenced by metadata") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestImportZip_MissingReferencedGitPatch(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          "missing-git-patch-cp",
		SessionName: "missing-git-patch-session",
		CreatedAt:   time.Now(),
		Session:     SessionState{},
		Git: GitState{
			PatchFile: "git.patch",
		},
	}
	cpJSON, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	sessionJSON, err := json.MarshalIndent(cp.Session, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(tmpDir, "missing-git-patch.zip")
	buildZip(t, archive, map[string][]byte{
		MetadataFile: cpJSON,
		SessionFile:  sessionJSON,
	})

	_, err = storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err == nil {
		t.Fatal("expected error for missing referenced git patch")
	}
	if !strings.Contains(err.Error(), "archive missing git patch referenced by metadata") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestImportTarGz_MissingReferencedGitStatus(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          "missing-git-status-cp",
		SessionName: "missing-git-status-session",
		CreatedAt:   time.Now(),
		Session:     SessionState{},
		Git: GitState{
			StatusFile: GitStatusFile,
		},
	}
	cpJSON, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	sessionJSON, err := json.MarshalIndent(cp.Session, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(tmpDir, "missing-git-status.tar.gz")
	buildTarGz(t, archive, map[string][]byte{
		MetadataFile: cpJSON,
		SessionFile:  sessionJSON,
	})

	_, err = storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err == nil {
		t.Fatal("expected error for missing referenced git status")
	}
	if !strings.Contains(err.Error(), "archive missing git status referenced by metadata") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestImportZip_MissingReferencedGitStatus(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          "missing-git-status-cp-zip",
		SessionName: "missing-git-status-session-zip",
		CreatedAt:   time.Now(),
		Session:     SessionState{},
		Git: GitState{
			StatusFile: GitStatusFile,
		},
	}
	cpJSON, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	sessionJSON, err := json.MarshalIndent(cp.Session, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(tmpDir, "missing-git-status.zip")
	buildZip(t, archive, map[string][]byte{
		MetadataFile: cpJSON,
		SessionFile:  sessionJSON,
	})

	_, err = storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err == nil {
		t.Fatal("expected error for missing referenced git status")
	}
	if !strings.Contains(err.Error(), "archive missing git status referenced by metadata") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestImportTarGz_RejectsOversizedEntry(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cpJSON := validCheckpointJSON(t, "oversized-tar-session", "oversized-tar-cp")
	limit := int64(len(cpJSON) + 8)
	oldLimit := maxImportEntrySize
	maxImportEntrySize = limit
	t.Cleanup(func() {
		maxImportEntrySize = oldLimit
	})

	sessionJSON := append(bytes.Repeat([]byte(" "), len(cpJSON)+32), validSessionJSON(t, SessionState{})...)

	archive := filepath.Join(tmpDir, "oversized.tar.gz")
	buildTarGz(t, archive, map[string][]byte{
		MetadataFile: cpJSON,
		SessionFile:  sessionJSON,
	})

	_, err := storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err == nil {
		t.Fatal("expected oversized archive entry to fail import")
	}
	if !strings.Contains(err.Error(), "archive entry too large: "+SessionFile) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestImportZip_RejectsOversizedEntry(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cpJSON := validCheckpointJSON(t, "oversized-zip-session", "oversized-zip-cp")
	limit := int64(len(cpJSON) + 8)
	oldLimit := maxImportEntrySize
	maxImportEntrySize = limit
	t.Cleanup(func() {
		maxImportEntrySize = oldLimit
	})

	sessionJSON := append(bytes.Repeat([]byte(" "), len(cpJSON)+32), validSessionJSON(t, SessionState{})...)

	archive := filepath.Join(tmpDir, "oversized.zip")
	buildZip(t, archive, map[string][]byte{
		MetadataFile: cpJSON,
		SessionFile:  sessionJSON,
	})

	_, err := storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err == nil {
		t.Fatal("expected oversized archive entry to fail import")
	}
	if !strings.Contains(err.Error(), "archive entry too large: "+SessionFile) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestImportTarGz_RejectsOversizedArchiveContent(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cpJSON := validCheckpointJSON(t, "oversized-total-tar-session", "oversized-total-tar-cp")
	sessionJSON := validSessionJSON(t, SessionState{})
	oldLimit := maxImportArchiveBytes
	maxImportArchiveBytes = int64(len(cpJSON) + len(sessionJSON) - 1)
	t.Cleanup(func() {
		maxImportArchiveBytes = oldLimit
	})

	archive := filepath.Join(tmpDir, "oversized-total.tar.gz")
	buildTarGzEntries(t, archive, []struct {
		name string
		data []byte
	}{
		{name: MetadataFile, data: cpJSON},
		{name: SessionFile, data: sessionJSON},
	})

	_, err := storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err == nil {
		t.Fatal("expected oversized archive content to fail import")
	}
	if !strings.Contains(err.Error(), errImportArchiveTooLarge) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestImportZip_RejectsOversizedArchiveContent(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cpJSON := validCheckpointJSON(t, "oversized-total-zip-session", "oversized-total-zip-cp")
	sessionJSON := validSessionJSON(t, SessionState{})
	oldLimit := maxImportArchiveBytes
	maxImportArchiveBytes = int64(len(cpJSON) + len(sessionJSON) - 1)
	t.Cleanup(func() {
		maxImportArchiveBytes = oldLimit
	})

	archive := filepath.Join(tmpDir, "oversized-total.zip")
	buildZipEntries(t, archive, []struct {
		name string
		data []byte
	}{
		{name: MetadataFile, data: cpJSON},
		{name: SessionFile, data: sessionJSON},
	})

	_, err := storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err == nil {
		t.Fatal("expected oversized archive content to fail import")
	}
	if !strings.Contains(err.Error(), errImportArchiveTooLarge) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestImportTarGz_RejectsUnexpectedArchiveFile(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cpJSON := validCheckpointJSON(t, "unexpected-file-session", "unexpected-file-cp")
	sessionJSON, err := json.MarshalIndent(SessionState{}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(tmpDir, "unexpected-file.tar.gz")
	buildTarGz(t, archive, map[string][]byte{
		MetadataFile: cpJSON,
		SessionFile:  sessionJSON,
		"junk.txt":   []byte("surprise"),
	})

	_, err = storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err == nil {
		t.Fatal("expected error for unexpected archive file")
	}
	if !strings.Contains(err.Error(), "archive contains unexpected file") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestImportZip_RejectsUnexpectedArchiveFile(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cpJSON := validCheckpointJSON(t, "unexpected-file-session-zip", "unexpected-file-cp-zip")
	sessionJSON, err := json.MarshalIndent(SessionState{}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(tmpDir, "unexpected-file.zip")
	buildZip(t, archive, map[string][]byte{
		MetadataFile: cpJSON,
		SessionFile:  sessionJSON,
		"junk.txt":   []byte("surprise"),
	})

	_, err = storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err == nil {
		t.Fatal("expected error for unexpected archive file")
	}
	if !strings.Contains(err.Error(), "archive contains unexpected file") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestImportTarGz_RejectsDuplicateEntry(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cpJSON := validCheckpointJSON(t, "duplicate-entry-session", "duplicate-entry-cp")
	archive := filepath.Join(tmpDir, "duplicate-entry.tar.gz")
	buildTarGzEntries(t, archive, []struct {
		name string
		data []byte
	}{
		{name: MetadataFile, data: cpJSON},
		{name: MetadataFile, data: cpJSON},
	})

	_, err := storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err == nil {
		t.Fatal("expected duplicate entry import to fail")
	}
	if !strings.Contains(err.Error(), "archive contains duplicate entry") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestImportZip_RejectsDuplicateEntry(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cpJSON := validCheckpointJSON(t, "duplicate-entry-session-zip", "duplicate-entry-cp-zip")
	archive := filepath.Join(tmpDir, "duplicate-entry.zip")
	buildZipEntries(t, archive, []struct {
		name string
		data []byte
	}{
		{name: MetadataFile, data: cpJSON},
		{name: MetadataFile, data: cpJSON},
	})

	_, err := storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err == nil {
		t.Fatal("expected duplicate entry import to fail")
	}
	if !strings.Contains(err.Error(), "archive contains duplicate entry") {
		t.Errorf("unexpected error: %v", err)
	}
}

// =============================================================================
// Import tar.gz: corrupt manifest JSON
// =============================================================================

func TestImportTarGz_CorruptManifestJSON(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cpJSON := validCheckpointJSON(t, "mf-session", "mf-cp")

	archive := filepath.Join(tmpDir, "corrupt-manifest.tar.gz")
	buildTarGz(t, archive, map[string][]byte{
		MetadataFile:    cpJSON,
		"MANIFEST.json": []byte("{bad json"),
	})

	_, err := storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err == nil {
		t.Fatal("expected error for corrupt manifest")
	}
	if !strings.Contains(err.Error(), "failed to parse manifest") {
		t.Errorf("unexpected error: %v", err)
	}
}

// =============================================================================
// Import: WorkingDir placeholder expansion
// =============================================================================

func TestImportTarGz_WorkingDirPlaceholder(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          "wd-cp",
		SessionName: "wd-session",
		WorkingDir:  "${WORKING_DIR}",
		CreatedAt:   time.Now(),
	}
	cpJSON, _ := json.MarshalIndent(cp, "", "  ")
	sessionJSON := validSessionJSON(t, cp.Session)

	archive := filepath.Join(tmpDir, "working-dir.tar.gz")
	buildTarGz(t, archive, map[string][]byte{
		MetadataFile: cpJSON,
		SessionFile:  sessionJSON,
	})

	imported, err := storage.Import(archive, ImportOptions{VerifyChecksums: false})
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	cwd, _ := os.Getwd()
	if imported.WorkingDir != cwd {
		t.Errorf("WorkingDir = %q, want current dir %q", imported.WorkingDir, cwd)
	}
}

// =============================================================================
// Import tar.gz: TargetSession override
// =============================================================================

func TestImportTarGz_TargetSession(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cpJSON := validCheckpointJSON(t, "orig-session", "ts-cp")
	sessionJSON := validSessionJSON(t, SessionState{})

	archive := filepath.Join(tmpDir, "target-session.tar.gz")
	buildTarGz(t, archive, map[string][]byte{
		MetadataFile: cpJSON,
		SessionFile:  sessionJSON,
	})

	imported, err := storage.Import(archive, ImportOptions{
		VerifyChecksums: false,
		TargetSession:   "new-session",
		TargetDir:       "/new/dir",
	})
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	if imported.SessionName != "new-session" {
		t.Errorf("SessionName = %q, want new-session", imported.SessionName)
	}
	if imported.WorkingDir != "/new/dir" {
		t.Errorf("WorkingDir = %q, want /new/dir", imported.WorkingDir)
	}
}

// =============================================================================
// Import: manifest metadata must match metadata.json
// =============================================================================

func TestImportTarGz_RejectsManifestSessionMismatch(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cpJSON := validCheckpointJSON(t, "cp-session", "mn-cp")
	sessionJSON := validSessionJSON(t, SessionState{})

	manifest := &ExportManifest{
		Version:     1,
		SessionName: "manifest-session",
		Checksums: map[string]string{
			MetadataFile: sha256sum(cpJSON),
			SessionFile:  sha256sum(sessionJSON),
		},
	}
	manifestJSON, _ := json.Marshal(manifest)

	archive := filepath.Join(tmpDir, "manifest-name.tar.gz")
	buildTarGz(t, archive, map[string][]byte{
		MetadataFile:    cpJSON,
		SessionFile:     sessionJSON,
		"MANIFEST.json": manifestJSON,
	})

	_, err := storage.Import(archive, ImportOptions{VerifyChecksums: true})
	if err == nil {
		t.Fatal("expected manifest/session mismatch to fail import")
	}
	if !strings.Contains(err.Error(), "manifest session name") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// =============================================================================
// checkFiles: missing metadata.json and session.json
// =============================================================================

func TestCheckFiles_MissingMetadataAndSession(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	// Create an empty checkpoint directory (no files inside)
	cpDir := filepath.Join(tmpDir, "test-session", "test-cp")
	if err := os.MkdirAll(cpDir, 0755); err != nil {
		t.Fatal(err)
	}

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          "test-cp",
		SessionName: "test-session",
	}

	result := &IntegrityResult{
		FilesPresent: true,
		Errors:       []string{},
		Details:      make(map[string]string),
	}
	cp.checkFiles(storage, cpDir, result)

	if result.FilesPresent {
		t.Error("FilesPresent should be false when metadata.json and session.json are missing")
	}
	if len(result.Errors) < 2 {
		t.Errorf("expected at least 2 errors, got %d: %v", len(result.Errors), result.Errors)
	}

	foundMeta := false
	foundSession := false
	for _, e := range result.Errors {
		if strings.Contains(e, "metadata.json") {
			foundMeta = true
		}
		if strings.Contains(e, "session.json") {
			foundSession = true
		}
	}
	if !foundMeta {
		t.Error("expected error about missing metadata.json")
	}
	if !foundSession {
		t.Error("expected error about missing session.json")
	}
}

// =============================================================================
// checkFiles: missing git patch
// =============================================================================

func TestCheckFiles_MissingGitPatch(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "patch-session"
	cpID := "patch-cp"

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          cpID,
		SessionName: sessionName,
		CreatedAt:   time.Now(),
	}

	if err := storage.Save(cp); err != nil {
		t.Fatal(err)
	}
	cp.Git.PatchFile = "changes.patch"

	result := &IntegrityResult{
		FilesPresent: true,
		Errors:       []string{},
		Details:      make(map[string]string),
	}
	dir := storage.CheckpointDir(sessionName, cpID)
	cp.checkFiles(storage, dir, result)

	if result.FilesPresent {
		t.Error("FilesPresent should be false with missing git patch")
	}

	foundPatch := false
	for _, e := range result.Errors {
		if strings.Contains(e, "missing git patch") {
			foundPatch = true
		}
	}
	if !foundPatch {
		t.Errorf("expected error about missing git patch, got: %v", result.Errors)
	}
}

// =============================================================================
// validateConsistency: dirty git with zero changes
// =============================================================================

func TestValidateConsistency_DirtyGitZeroChanges(t *testing.T) {
	t.Parallel()

	cp := &Checkpoint{
		Session: SessionState{
			Panes:           []PaneState{{ID: "%0", Index: 0, Width: 80, Height: 24}},
			ActivePaneIndex: 0,
		},
		PaneCount: 1,
		Git: GitState{
			IsDirty:        true,
			StagedCount:    0,
			UnstagedCount:  0,
			UntrackedCount: 0,
		},
	}

	result := &IntegrityResult{
		ConsistencyValid: true,
		Errors:           []string{},
		Warnings:         []string{},
		Details:          make(map[string]string),
	}
	cp.validateConsistency(result)

	if !result.ConsistencyValid {
		t.Error("should still be valid (warning only)")
	}

	foundWarning := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "dirty but no changes") {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Errorf("expected warning about dirty with no changes, got: %v", result.Warnings)
	}
}

// =============================================================================
// validateConsistency: pane with invalid dimensions
// =============================================================================

func TestValidateConsistency_InvalidPaneDimensions(t *testing.T) {
	t.Parallel()

	cp := &Checkpoint{
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%0", Index: 0, Width: 0, Height: 0},
			},
			ActivePaneIndex: 0,
		},
		PaneCount: 1,
	}

	result := &IntegrityResult{
		ConsistencyValid: true,
		Errors:           []string{},
		Warnings:         []string{},
		Details:          make(map[string]string),
	}
	cp.validateConsistency(result)

	foundWarning := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "invalid dimensions") {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Errorf("expected warning about invalid dimensions, got: %v", result.Warnings)
	}
}

// =============================================================================
// VerifyManifest: nil manifest
// =============================================================================

func TestVerifyManifest_NilManifest(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          "nil-mf-cp",
		SessionName: "nil-mf-session",
	}

	result := cp.VerifyManifest(storage, nil)
	if !result.Valid {
		t.Error("should be valid with nil manifest")
	}
	if len(result.Warnings) == 0 {
		t.Error("expected warning about no manifest")
	}
}

// =============================================================================
// VerifyManifest: empty manifest
// =============================================================================

func TestVerifyManifest_EmptyManifest(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          "em-mf-cp",
		SessionName: "em-mf-session",
	}

	result := cp.VerifyManifest(storage, &FileManifest{Files: map[string]string{}})
	if !result.Valid {
		t.Error("should be valid with empty manifest")
	}
}

// =============================================================================
// VerifyManifest: missing file on disk
// =============================================================================

func TestVerifyManifest_MissingFileOnDisk(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          "miss-disk-cp",
		SessionName: "miss-disk-session",
	}
	if err := storage.Save(cp); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(storage.CheckpointDir(cp.SessionName, cp.ID), SessionFile)); err != nil {
		t.Fatal(err)
	}

	manifest := &FileManifest{
		Files: map[string]string{
			MetadataFile: strings.Repeat("a", 64),
			SessionFile:  strings.Repeat("b", 64),
		},
	}

	result := cp.VerifyManifest(storage, manifest)
	if result.Valid {
		t.Error("should be invalid with missing file")
	}
	if result.ChecksumsValid {
		t.Error("ChecksumsValid should be false")
	}
	if result.FilesPresent {
		t.Error("FilesPresent should be false")
	}

	foundMissing := false
	for _, e := range result.Errors {
		if strings.Contains(e, "file missing: "+SessionFile) {
			foundMissing = true
			break
		}
	}
	if !foundMissing {
		t.Errorf("expected missing session file error, got: %v", result.Errors)
	}
}

func TestGenerateManifest_MissingReferencedScrollbackFails(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          "manifest-missing-scrollback",
		SessionName: "manifest-missing-scrollback-session",
		CreatedAt:   time.Now(),
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%0", Index: 0},
			},
		},
		PaneCount: 1,
	}
	if err := storage.Save(cp); err != nil {
		t.Fatal(err)
	}
	cp.Session.Panes[0].ScrollbackFile = "panes/pane__0.txt"

	_, err := cp.GenerateManifest(storage)
	if err == nil {
		t.Fatal("expected GenerateManifest to fail when referenced scrollback is missing")
	}
	if !strings.Contains(err.Error(), "missing scrollback") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVerifyManifest_MissingExpectedCoverageFails(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          "partial-manifest-cp",
		SessionName: "partial-manifest-session",
		CreatedAt:   time.Now(),
	}
	if err := storage.Save(cp); err != nil {
		t.Fatal(err)
	}

	metaHash, err := hashFile(filepath.Join(storage.CheckpointDir(cp.SessionName, cp.ID), MetadataFile))
	if err != nil {
		t.Fatal(err)
	}
	manifest := &FileManifest{
		Files: map[string]string{
			MetadataFile: metaHash,
		},
	}

	result := cp.VerifyManifest(storage, manifest)
	if result.Valid {
		t.Fatal("expected partial manifest to be invalid")
	}
	foundMissing := false
	for _, e := range result.Errors {
		if strings.Contains(e, "manifest missing file: "+SessionFile) {
			foundMissing = true
			break
		}
	}
	if !foundMissing {
		t.Fatalf("expected missing session file coverage error, got: %v", result.Errors)
	}
}

func TestVerifyManifest_UnexpectedManifestFileFails(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          "unexpected-manifest-cp",
		SessionName: "unexpected-manifest-session",
		CreatedAt:   time.Now(),
	}
	if err := storage.Save(cp); err != nil {
		t.Fatal(err)
	}

	manifest, err := cp.GenerateManifest(storage)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Files["extra.txt"] = strings.Repeat("a", 64)

	result := cp.VerifyManifest(storage, manifest)
	if result.Valid {
		t.Fatal("expected manifest with unexpected file to be invalid")
	}
	foundUnexpected := false
	for _, e := range result.Errors {
		if strings.Contains(e, "manifest contains unexpected file: extra.txt") {
			foundUnexpected = true
			break
		}
	}
	if !foundUnexpected {
		t.Fatalf("expected unexpected-file error, got: %v", result.Errors)
	}
}

func TestVerifyManifest_UnexpectedPathDoesNotAffectVerifiedCount(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          "unexpected-manifest-path-cp",
		SessionName: "unexpected-manifest-path-session",
		CreatedAt:   time.Now(),
	}
	if err := storage.Save(cp); err != nil {
		t.Fatal(err)
	}

	manifest, err := cp.GenerateManifest(storage)
	if err != nil {
		t.Fatal(err)
	}
	outsidePath := filepath.Join(tmpDir, "outside.txt")
	if err := os.WriteFile(outsidePath, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	outsideHash, err := hashFile(outsidePath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Files["../outside.txt"] = outsideHash

	result := cp.VerifyManifest(storage, manifest)
	if result.Valid {
		t.Fatal("expected manifest with unexpected path to be invalid")
	}
	if got := result.Details["verified"]; got != "2" {
		t.Fatalf("verified count = %q, want 2", got)
	}
	foundUnexpected := false
	for _, e := range result.Errors {
		if strings.Contains(e, "manifest contains unexpected file: ../outside.txt") {
			foundUnexpected = true
			break
		}
	}
	if !foundUnexpected {
		t.Fatalf("expected unexpected-path error, got: %v", result.Errors)
	}
}

func TestVerifyManifest_MalformedShortHashReportsMismatch(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          "short-hash-manifest-cp",
		SessionName: "short-hash-manifest-session",
		CreatedAt:   time.Now(),
	}
	if err := storage.Save(cp); err != nil {
		t.Fatal(err)
	}

	manifest, err := cp.GenerateManifest(storage)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Files[MetadataFile] = "bad"

	result := cp.VerifyManifest(storage, manifest)
	if result.Valid {
		t.Fatal("expected malformed short hash to invalidate manifest")
	}
	foundMismatch := false
	for _, e := range result.Errors {
		if strings.Contains(e, "checksum mismatch: "+MetadataFile) && strings.Contains(e, "expected bad") {
			foundMismatch = true
			break
		}
	}
	if !foundMismatch {
		t.Fatalf("expected checksum mismatch error for short hash, got: %v", result.Errors)
	}
}

// =============================================================================
// hashFile: nonexistent file
// =============================================================================

func TestHashFile_Nonexistent(t *testing.T) {
	t.Parallel()

	_, err := hashFile("/nonexistent/path/file.txt")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
	if !os.IsNotExist(err) {
		t.Errorf("expected IsNotExist error, got: %v", err)
	}
}

// =============================================================================
// QuickCheck: multiple errors
// =============================================================================

func TestQuickCheck_MultipleErrors(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	cp := &Checkpoint{
		Version:     0,  // invalid
		ID:          "", // missing
		SessionName: "", // missing
	}

	err := cp.QuickCheck(storage)
	if err == nil {
		t.Fatal("expected error with multiple failures")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "unsupported version") {
		t.Error("expected version error in message")
	}
	if !strings.Contains(errMsg, "missing checkpoint ID") {
		t.Error("expected missing ID error in message")
	}
	if !strings.Contains(errMsg, "missing session_name") {
		t.Error("expected missing session_name error in message")
	}
}

// =============================================================================
// gzipDecompress: invalid data
// =============================================================================

func TestGzipDecompress_InvalidInput(t *testing.T) {
	t.Parallel()

	_, err := gzipDecompress([]byte("not gzip data"))
	if err == nil {
		t.Fatal("expected error for invalid gzip data")
	}
}

// =============================================================================
// LoadCompressedScrollback: nonexistent session
// =============================================================================

func TestLoadCompressedScrollback_NoFile(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	_, err := storage.LoadCompressedScrollback("no-session", "no-cp", "%0")
	if err == nil {
		t.Fatal("expected error for nonexistent scrollback")
	}
}

// =============================================================================
// LoadCompressedScrollback: corrupt compressed file
// =============================================================================

func TestLoadCompressedScrollback_CorruptGzipFile(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "corrupt-session"
	cpID := "corrupt-cp"
	paneID := "%0"

	// Create the panes directory and a corrupt .gz file
	panesDir := storage.PanesDirPath(sessionName, cpID)
	if err := os.MkdirAll(panesDir, 0755); err != nil {
		t.Fatal(err)
	}

	filename := "pane__0.txt.gz"
	corruptPath := filepath.Join(panesDir, filename)
	if err := os.WriteFile(corruptPath, []byte("not valid gzip"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := storage.LoadCompressedScrollback(sessionName, cpID, paneID)
	if err == nil {
		t.Fatal("expected error for corrupt gzip file")
	}
	if !strings.Contains(err.Error(), "decompressing") {
		t.Errorf("unexpected error: %v", err)
	}
}

// =============================================================================
// rotateAutoCheckpoints
// =============================================================================

func TestRotateAutoCheckpoints(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	auto := &AutoCheckpointer{storage: storage}
	sessionName := "rotate-session"

	// Create 5 auto-checkpoints
	for i := 0; i < 5; i++ {
		cp := &Checkpoint{
			Version:     CurrentVersion,
			ID:          GenerateID("test"),
			Name:        AutoCheckpointPrefix + "-test",
			SessionName: sessionName,
			CreatedAt:   time.Now().Add(time.Duration(i) * time.Second),
		}
		if err := storage.Save(cp); err != nil {
			t.Fatal(err)
		}
	}

	// Verify we have 5
	checkpoints, _ := storage.List(sessionName)
	if len(checkpoints) != 5 {
		t.Fatalf("expected 5 checkpoints, got %d", len(checkpoints))
	}

	// Rotate to keep max 3
	if err := auto.rotateAutoCheckpoints(sessionName, 3); err != nil {
		t.Fatalf("rotateAutoCheckpoints failed: %v", err)
	}

	// Verify we have 3 left
	remaining, _ := storage.List(sessionName)
	if len(remaining) != 3 {
		t.Errorf("expected 3 remaining after rotation, got %d", len(remaining))
	}
}

// =============================================================================
// rotateAutoCheckpoints: under limit does nothing
// =============================================================================

func TestRotateAutoCheckpoints_UnderLimit(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	auto := &AutoCheckpointer{storage: storage}
	sessionName := "under-limit-session"

	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          GenerateID("test"),
		Name:        AutoCheckpointPrefix + "-test",
		SessionName: sessionName,
		CreatedAt:   time.Now(),
	}
	if err := storage.Save(cp); err != nil {
		t.Fatal(err)
	}

	if err := auto.rotateAutoCheckpoints(sessionName, 5); err != nil {
		t.Fatalf("rotateAutoCheckpoints failed: %v", err)
	}

	remaining, _ := storage.List(sessionName)
	if len(remaining) != 1 {
		t.Errorf("expected 1 remaining (under limit), got %d", len(remaining))
	}
}

// =============================================================================
// isPathWithinDir: edge cases
// =============================================================================

func TestIsPathWithinDir_AdditionalCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseDir string
		target  string
		want    bool
	}{
		{"deep nested valid", "/base", "a/b/c/d/e.txt", true},
		{"dot-dot in valid position", "/base", "sub/../sub/file.txt", true},
		{"empty target", "/base", "", true},
		{"root traversal", "/base", "/../../../etc/shadow", false},
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
// Import tar.gz: not a gzip file
// =============================================================================

func TestImportTarGz_NotGzip(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	archive := filepath.Join(tmpDir, "not-gzip.tar.gz")
	if err := os.WriteFile(archive, []byte("plaintext not gzip"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := storage.Import(archive, ImportOptions{})
	if err == nil {
		t.Fatal("expected error for non-gzip file")
	}
	if !strings.Contains(err.Error(), "gzip") {
		t.Errorf("unexpected error: %v", err)
	}
}

// =============================================================================
// Import zip: not a zip file
// =============================================================================

func TestImportZip_NotZip(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	archive := filepath.Join(tmpDir, "not-a-zip.zip")
	if err := os.WriteFile(archive, []byte("plaintext not zip"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := storage.Import(archive, ImportOptions{})
	if err == nil {
		t.Fatal("expected error for non-zip file")
	}
	if !strings.Contains(err.Error(), "zip") {
		t.Errorf("unexpected error: %v", err)
	}
}

// =============================================================================
// Export: nonexistent checkpoint
// =============================================================================

func TestExport_NonexistentCheckpoint(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	_, err := storage.Export("no-session", "no-cp", filepath.Join(tmpDir, "out.tar.gz"), DefaultExportOptions())
	if err == nil {
		t.Fatal("expected error for nonexistent checkpoint")
	}
	if !strings.Contains(err.Error(), "failed to load checkpoint") {
		t.Errorf("unexpected error: %v", err)
	}
}
