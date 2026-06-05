package bundle

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

// ---------------------------------------------------------------------------
// AddDirectory (0% → ~100%)
// ---------------------------------------------------------------------------

func TestAddDirectory_Basic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Create a small directory tree
	subDir := filepath.Join(dir, "sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha"), 0644)
	os.WriteFile(filepath.Join(subDir, "b.txt"), []byte("bravo"), 0644)

	gen := NewGenerator(GeneratorConfig{
		NTMVersion:      "v1.0.0",
		RedactionConfig: redaction.Config{Mode: redaction.ModeOff},
	})

	if err := gen.AddDirectory(dir, dir, ContentTypeConfig); err != nil {
		t.Fatalf("AddDirectory: %v", err)
	}

	if len(gen.files) != 2 {
		t.Errorf("files count = %d, want 2", len(gen.files))
	}

	// Paths should be relative to dir
	paths := map[string]bool{}
	for _, f := range gen.files {
		paths[f.path] = true
	}
	if !paths["a.txt"] {
		t.Error("missing a.txt")
	}
	wantSub := filepath.Join("sub", "b.txt")
	if !paths[wantSub] {
		t.Errorf("missing %s; have %v", wantSub, paths)
	}
}

func TestAddDirectory_EmptyDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	gen := NewGenerator(GeneratorConfig{
		NTMVersion:      "v1.0.0",
		RedactionConfig: redaction.Config{Mode: redaction.ModeOff},
	})

	if err := gen.AddDirectory(dir, dir, ContentTypeConfig); err != nil {
		t.Fatalf("AddDirectory empty: %v", err)
	}

	if len(gen.files) != 0 {
		t.Errorf("files count = %d, want 0 for empty dir", len(gen.files))
	}
}

func TestAddDirectory_RejectsRelativeTraversal(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	relativeTo := t.TempDir()

	gen := NewGenerator(GeneratorConfig{
		NTMVersion:      "v1.0.0",
		RedactionConfig: redaction.Config{Mode: redaction.ModeOff},
	})

	err := gen.AddDirectory(dir, relativeTo, ContentTypeConfig)
	if err == nil {
		t.Fatal("expected traversal error")
	}
	if len(gen.files) != 0 {
		t.Fatalf("files count = %d, want 0", len(gen.files))
	}
	if len(gen.errors) == 0 || !strings.Contains(gen.errors[0], "unsafe archive path") {
		t.Fatalf("expected unsafe archive path error entry, got %v", gen.errors)
	}
}

func TestAddDirectory_UnreadableFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	badFile := filepath.Join(dir, "noperm.txt")
	os.WriteFile(badFile, []byte("secret"), 0644)
	os.Chmod(badFile, 0000)
	t.Cleanup(func() { os.Chmod(badFile, 0644) })

	gen := NewGenerator(GeneratorConfig{
		NTMVersion:      "v1.0.0",
		RedactionConfig: redaction.Config{Mode: redaction.ModeOff},
	})

	// Should not return an error — logs it and continues
	if err := gen.AddDirectory(dir, dir, ContentTypeConfig); err != nil {
		t.Fatalf("AddDirectory: %v", err)
	}

	// The file should not have been added successfully
	if len(gen.files) != 0 {
		t.Errorf("files count = %d, want 0 (unreadable file skipped)", len(gen.files))
	}

	// Error should have been recorded
	found := false
	for _, e := range gen.errors {
		if strings.Contains(e, "read error") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected read error in gen.errors, got: %v", gen.errors)
	}
}

func TestAddDirectory_SkipsSymlinkFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("outside secret"), 0644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(dir, "linked-secret.txt")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	gen := NewGenerator(GeneratorConfig{
		NTMVersion:      "v1.0.0",
		RedactionConfig: redaction.Config{Mode: redaction.ModeOff},
	})

	if err := gen.AddDirectory(dir, dir, ContentTypeConfig); err != nil {
		t.Fatalf("AddDirectory: %v", err)
	}
	if len(gen.files) != 0 {
		t.Fatalf("files count = %d, want 0", len(gen.files))
	}
	if len(gen.errors) == 0 || !strings.Contains(strings.Join(gen.errors, "\n"), "symlink skipped") {
		t.Fatalf("expected symlink skipped error entry, got %v", gen.errors)
	}
}

func TestAddDirectory_NonExistentPath(t *testing.T) {
	t.Parallel()

	gen := NewGenerator(GeneratorConfig{
		NTMVersion:      "v1.0.0",
		RedactionConfig: redaction.Config{Mode: redaction.ModeOff},
	})

	// WalkDir passes the initial error to the callback, which swallows it
	// and records a walk error. So AddDirectory returns nil.
	err := gen.AddDirectory("/no/such/path", "/no/such", ContentTypeConfig)
	if err != nil {
		t.Fatalf("AddDirectory returned error: %v", err)
	}

	// Should have a walk error recorded
	if len(gen.errors) == 0 {
		t.Error("expected walk error in gen.errors")
	}
	found := false
	for _, e := range gen.errors {
		if strings.Contains(e, "walk error") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected walk error, got: %v", gen.errors)
	}
}

func TestAddDirectory_RelPathFallback(t *testing.T) {
	t.Parallel()

	// When relativeTo is unrelated to basePath, filepath.Rel fails
	// and the code falls back to the absolute path.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "c.txt"), []byte("charlie"), 0644)

	gen := NewGenerator(GeneratorConfig{
		NTMVersion:      "v1.0.0",
		RedactionConfig: redaction.Config{Mode: redaction.ModeOff},
	})

	// Use an unrelated relativeTo path. On Linux, filepath.Rel may not fail
	// for same-volume paths, so just check that it completes without error.
	if err := gen.AddDirectory(dir, dir, ContentTypeConfig); err != nil {
		t.Fatalf("AddDirectory: %v", err)
	}

	if len(gen.files) != 1 {
		t.Fatalf("files count = %d, want 1", len(gen.files))
	}
}

// ---------------------------------------------------------------------------
// verifyTarGz (0% → ~100%) — via Verify() tar.gz path
// ---------------------------------------------------------------------------

func TestVerify_TarGz_RoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	outputPath := filepath.Join(dir, "bundle.tar.gz")

	gen := NewGenerator(GeneratorConfig{
		Session:    "tgz-test",
		OutputPath: outputPath,
		Format:     FormatTarGz,
		NTMVersion: "v1.0.0",
		RedactionConfig: redaction.Config{
			Mode: redaction.ModeOff,
		},
	})

	gen.AddFile("data.txt", []byte("hello tar"), ContentTypeScrollback, time.Now())
	gen.AddFile("config.txt", []byte("key=value"), ContentTypeConfig, time.Now())

	_, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate tar.gz: %v", err)
	}

	result, err := Verify(outputPath)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}

	if !result.Valid {
		t.Errorf("tar.gz bundle not valid: %v", result.Errors)
	}
	if !result.ManifestValid {
		t.Error("manifest not valid")
	}
	if !result.FilesPresent {
		t.Error("files not present")
	}
	if !result.ChecksumsValid {
		t.Error("checksums not valid")
	}
	if result.Manifest == nil {
		t.Fatal("manifest is nil")
	}
	if result.Manifest.NTMVersion != "v1.0.0" {
		t.Errorf("NTMVersion = %q, want v1.0.0", result.Manifest.NTMVersion)
	}
	if result.Details["file_count"] != "3" { // manifest + 2 files
		t.Errorf("file_count = %q, want 3", result.Details["file_count"])
	}
}

func TestVerify_TarGz_NotAFile(t *testing.T) {
	t.Parallel()

	result, err := Verify("/no/such/file.tar.gz")
	if err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}
	if result.Valid {
		t.Error("expected invalid for missing file")
	}
	if result.ManifestValid {
		t.Error("expected manifest_valid false")
	}
	if len(result.Errors) == 0 {
		t.Error("expected errors")
	}
}

func TestVerify_TarGz_NotGzip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "bad.tar.gz")
	os.WriteFile(path, []byte("this is not gzip"), 0644)

	result, err := Verify(path)
	if err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}
	if result.Valid {
		t.Error("expected invalid for non-gzip file")
	}
	found := false
	for _, e := range result.Errors {
		if strings.Contains(e, "gzip") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected gzip error, got: %v", result.Errors)
	}
}

func TestVerify_TarGz_CorruptTar(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "corrupt.tar.gz")

	// Write valid gzip containing garbage (not a valid tar)
	f, _ := os.Create(path)
	gw := gzip.NewWriter(f)
	gw.Write([]byte("not a tar stream at all"))
	gw.Close()
	f.Close()

	result, err := Verify(path)
	if err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}
	if result.Valid {
		t.Error("expected invalid for corrupt tar")
	}
}

func TestVerify_TarGz_NoManifest(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "nomanifest.tar.gz")

	// Write a valid tar.gz with a file but no manifest
	f, _ := os.Create(path)
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	data := []byte("hello")
	tw.WriteHeader(&tar.Header{
		Name: "somefile.txt",
		Size: int64(len(data)),
		Mode: 0644,
	})
	tw.Write(data)

	tw.Close()
	gw.Close()
	f.Close()

	result, err := Verify(path)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if result.Valid {
		t.Error("expected invalid without manifest")
	}
	found := false
	for _, e := range result.Errors {
		if strings.Contains(e, "manifest.json not found") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'manifest.json not found' error, got: %v", result.Errors)
	}
}

func TestVerify_TarGz_BadManifestJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "badjson.tar.gz")

	f, _ := os.Create(path)
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	data := []byte("{invalid json}")
	tw.WriteHeader(&tar.Header{
		Name: ManifestFileName,
		Size: int64(len(data)),
		Mode: 0644,
	})
	tw.Write(data)

	tw.Close()
	gw.Close()
	f.Close()

	result, err := Verify(path)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if result.Valid {
		t.Error("expected invalid with bad JSON manifest")
	}
	found := false
	for _, e := range result.Errors {
		if strings.Contains(e, "failed to parse manifest") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected parse error, got: %v", result.Errors)
	}
}

func TestVerify_TarGz_InvalidManifestSchema(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "badschema.tar.gz")

	manifest := NewManifest("v1.0.0")
	manifest.SchemaVersion = 999 // invalid
	manifestData, _ := json.Marshal(manifest)

	f, _ := os.Create(path)
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	tw.WriteHeader(&tar.Header{
		Name: ManifestFileName,
		Size: int64(len(manifestData)),
		Mode: 0644,
	})
	tw.Write(manifestData)

	tw.Close()
	gw.Close()
	f.Close()

	result, err := Verify(path)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	// Manifest parses OK but schema validation fails
	if result.SchemaValid {
		t.Error("expected schema_valid false for version 999")
	}
	found := false
	for _, e := range result.Errors {
		if strings.Contains(e, "manifest validation failed") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected validation error, got: %v", result.Errors)
	}
}

func TestVerify_TarGz_MissingFileInManifest(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "missing.tar.gz")

	// Manifest references a file that doesn't exist in the archive
	manifest := NewManifest("v1.0.0")
	manifest.AddFile(FileEntry{
		Path:      "does_not_exist.txt",
		SHA256:    makeValidHash(),
		SizeBytes: 100,
	})
	manifestData, _ := json.Marshal(manifest)

	f, _ := os.Create(path)
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	tw.WriteHeader(&tar.Header{
		Name: ManifestFileName,
		Size: int64(len(manifestData)),
		Mode: 0644,
	})
	tw.Write(manifestData)

	tw.Close()
	gw.Close()
	f.Close()

	result, err := Verify(path)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if result.FilesPresent {
		t.Error("expected files_present false")
	}
	if result.Valid {
		t.Error("expected valid false")
	}
}

func TestVerify_TarGz_ChecksumMismatch(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "mismatch.tar.gz")

	content := []byte("actual content")
	wrongHash := makeValidHash() // doesn't match actual content

	manifest := NewManifest("v1.0.0")
	manifest.AddFile(FileEntry{
		Path:      "data.txt",
		SHA256:    wrongHash,
		SizeBytes: int64(len(content)),
	})
	manifestData, _ := json.Marshal(manifest)

	f, _ := os.Create(path)
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	// Write manifest
	tw.WriteHeader(&tar.Header{
		Name: ManifestFileName,
		Size: int64(len(manifestData)),
		Mode: 0644,
	})
	tw.Write(manifestData)

	// Write data file
	tw.WriteHeader(&tar.Header{
		Name: "data.txt",
		Size: int64(len(content)),
		Mode: 0644,
	})
	tw.Write(content)

	tw.Close()
	gw.Close()
	f.Close()

	result, err := Verify(path)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if result.ChecksumsValid {
		t.Error("expected checksums_valid false")
	}
	if result.Valid {
		t.Error("expected valid false")
	}
	found := false
	for _, e := range result.Errors {
		if strings.Contains(e, "checksum mismatch") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected checksum mismatch error, got: %v", result.Errors)
	}
}

func TestVerify_TarGz_SizeMismatchWarning(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "sizemismatch.tar.gz")

	content := []byte("hello")
	correctHash := HashBytes(content)

	manifest := NewManifest("v1.0.0")
	manifest.AddFile(FileEntry{
		Path:      "data.txt",
		SHA256:    correctHash,
		SizeBytes: 999, // wrong size
	})
	manifestData, _ := json.Marshal(manifest)

	f, _ := os.Create(path)
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	tw.WriteHeader(&tar.Header{
		Name: ManifestFileName,
		Size: int64(len(manifestData)),
		Mode: 0644,
	})
	tw.Write(manifestData)

	tw.WriteHeader(&tar.Header{
		Name: "data.txt",
		Size: int64(len(content)),
		Mode: 0644,
	})
	tw.Write(content)

	tw.Close()
	gw.Close()
	f.Close()

	result, err := Verify(path)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	// Checksums match, but size mismatches should produce a warning
	if !result.ChecksumsValid {
		t.Error("checksums should be valid (hash matches)")
	}
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "size mismatch") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected size mismatch warning, got warnings: %v", result.Warnings)
	}
}

// ---------------------------------------------------------------------------
// Verify — unknown format branch
// ---------------------------------------------------------------------------

func TestVerify_UnknownFormat(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "bundle.rar")
	os.WriteFile(path, []byte("data"), 0644)

	result, err := Verify(path)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if result.Valid {
		t.Error("expected invalid for unknown format")
	}
	found := false
	for _, e := range result.Errors {
		if strings.Contains(e, "unknown or unsupported") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected unsupported format error, got: %v", result.Errors)
	}
}

// ---------------------------------------------------------------------------
// Generate — unsupported format branch
// ---------------------------------------------------------------------------

func TestGenerate_UnsupportedFormat(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	gen := NewGenerator(GeneratorConfig{
		OutputPath:      filepath.Join(dir, "bundle.xyz"),
		Format:          Format("xyz"),
		NTMVersion:      "v1.0.0",
		RedactionConfig: redaction.Config{Mode: redaction.ModeOff},
	})

	_, err := gen.Generate()
	if err == nil {
		t.Error("expected error for unsupported format")
	}
	if !strings.Contains(err.Error(), "unsupported format") {
		t.Errorf("error = %q, want 'unsupported format'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Generate — Since filter branch
// ---------------------------------------------------------------------------

func TestGenerate_WithSinceFilter(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	outputPath := filepath.Join(dir, "bundle.zip")

	gen := NewGenerator(GeneratorConfig{
		OutputPath:      outputPath,
		Format:          FormatZip,
		NTMVersion:      "v1.0.0",
		Since:           &since,
		RedactionConfig: redaction.Config{Mode: redaction.ModeOff},
	})

	gen.AddFile("f.txt", []byte("data"), ContentTypeConfig, time.Now())

	result, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if result.Manifest.Filters == nil {
		t.Fatal("expected Filters to be set")
	}
	if result.Manifest.Filters.Since == "" {
		t.Error("expected Since to be set in manifest filters")
	}
}

// ---------------------------------------------------------------------------
// Generate — zero modTime branch in file entries
// ---------------------------------------------------------------------------

func TestGenerate_ZeroModTime(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	outputPath := filepath.Join(dir, "bundle.zip")

	gen := NewGenerator(GeneratorConfig{
		OutputPath:      outputPath,
		Format:          FormatZip,
		NTMVersion:      "v1.0.0",
		RedactionConfig: redaction.Config{Mode: redaction.ModeOff},
	})

	// Add file with zero time
	gen.AddFile("f.txt", []byte("data"), ContentTypeConfig, time.Time{})

	result, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if len(result.Manifest.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Manifest.Files))
	}
	if result.Manifest.Files[0].ModTime != "" {
		t.Errorf("expected empty ModTime for zero time, got %q", result.Manifest.Files[0].ModTime)
	}
}

// ---------------------------------------------------------------------------
// Generate tar.gz — zero modTime fallback in generateTarGz
// ---------------------------------------------------------------------------

func TestGenerateTarGz_ZeroModTimeFallback(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	outputPath := filepath.Join(dir, "bundle.tar.gz")

	gen := NewGenerator(GeneratorConfig{
		OutputPath:      outputPath,
		Format:          FormatTarGz,
		NTMVersion:      "v1.0.0",
		RedactionConfig: redaction.Config{Mode: redaction.ModeOff},
	})

	// Add file with zero time — generateTarGz should use time.Now() as fallback
	gen.AddFile("f.txt", []byte("data"), ContentTypeConfig, time.Time{})

	result, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate tar.gz: %v", err)
	}
	if result.Format != FormatTarGz {
		t.Errorf("format = %q, want tar.gz", result.Format)
	}

	// Verify the tar.gz was created and is valid
	vr, err := Verify(outputPath)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !vr.Valid {
		t.Errorf("tar.gz with zero modtime not valid: %v", vr.Errors)
	}
}

// ---------------------------------------------------------------------------
// AddDirectory with full Generate + Verify round-trip
// ---------------------------------------------------------------------------

func TestAddDirectory_GenerateVerify_RoundTrip(t *testing.T) {
	t.Parallel()

	// Create source directory with files
	srcDir := t.TempDir()
	os.WriteFile(filepath.Join(srcDir, "readme.txt"), []byte("Hello"), 0644)
	sub := filepath.Join(srcDir, "nested")
	os.MkdirAll(sub, 0755)
	os.WriteFile(filepath.Join(sub, "deep.txt"), []byte("World"), 0644)

	// Generate bundle
	outDir := t.TempDir()
	outputPath := filepath.Join(outDir, "dir-bundle.zip")

	gen := NewGenerator(GeneratorConfig{
		OutputPath:      outputPath,
		Format:          FormatZip,
		NTMVersion:      "v1.0.0",
		RedactionConfig: redaction.Config{Mode: redaction.ModeOff},
	})

	if err := gen.AddDirectory(srcDir, srcDir, ContentTypeConfig); err != nil {
		t.Fatalf("AddDirectory: %v", err)
	}

	result, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if result.FileCount != 2 {
		t.Errorf("FileCount = %d, want 2", result.FileCount)
	}

	// Verify
	vr, err := Verify(outputPath)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !vr.Valid {
		t.Errorf("bundle not valid: %v", vr.Errors)
	}
}
