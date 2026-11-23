package bundle

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
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

// ---------------------------------------------------------------------------
// Verify — zip, tar.gz, corruption, and unknown format branches
// ---------------------------------------------------------------------------

func TestVerify_ZipAndTarGzRoundTrip(t *testing.T) {
	t.Parallel()

	for _, format := range []Format{FormatZip, FormatTarGz} {
		t.Run(string(format), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			outputPath := filepath.Join(dir, "bundle"+format.Extension())

			gen := NewGenerator(GeneratorConfig{
				Session:         "verify-session",
				OutputPath:      outputPath,
				Format:          format,
				NTMVersion:      "v1.2.3",
				RedactionConfig: redaction.Config{Mode: redaction.ModeOff},
			})
			if err := gen.AddFile("config/app.toml", []byte("key = 'val'\n"), ContentTypeConfig, time.Now()); err != nil {
				t.Fatalf("AddFile: %v", err)
			}
			if err := gen.AddScrollback("pane_1", "hello\nworld", 10); err != nil {
				t.Fatalf("AddScrollback: %v", err)
			}
			// Trigger a non-fatal error recorded on manifest via AddError
			_ = gen.AddFile("../escape.txt", []byte("nope"), ContentTypeUnknown, time.Now())

			genRes, err := gen.Generate()
			if err != nil {
				t.Fatalf("Generate(%s): %v", format, err)
			}
			if genRes.FileCount != 2 {
				t.Fatalf("FileCount = %d, want 2", genRes.FileCount)
			}
			if len(genRes.Manifest.Errors) != 1 {
				t.Fatalf("Manifest.Errors = %v, want 1 recorded error", genRes.Manifest.Errors)
			}

			verRes, err := Verify(outputPath)
			if err != nil {
				t.Fatalf("Verify(%s): %v", format, err)
			}
			if !verRes.Valid || !verRes.ManifestValid || !verRes.SchemaValid || !verRes.FilesPresent || !verRes.ChecksumsValid {
				t.Fatalf("Verify(%s) = %+v, want all valid", format, verRes)
			}
			if verRes.Details["file_count"] != "2" {
				t.Errorf("Details[file_count] = %q, want 2", verRes.Details["file_count"])
			}
			if verRes.Details["total_size"] != "23" {
				t.Errorf("Details[total_size] = %q, want 23", verRes.Details["total_size"])
			}
		})
	}
}

func TestVerify_UnknownFormatAndMissingFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	unknownPath := filepath.Join(dir, "bundle.unknown")
	if err := os.WriteFile(unknownPath, []byte("not an archive"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := Verify(unknownPath); err == nil || !strings.Contains(err.Error(), "unsupported bundle format") {
		t.Fatalf("Verify(unknown) error = %v, want unsupported bundle format", err)
	}
	if _, err := Verify(filepath.Join(dir, "missing.zip")); err == nil {
		t.Fatal("Verify(missing) expected error")
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

	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("tar.gz output missing: %v", err)
	}
}

// ---------------------------------------------------------------------------
// AddDirectory with full Generate + Verify round-trip
// ---------------------------------------------------------------------------

func TestVerifyArchiveBoundsRejectsExcessEntriesAndTotalBytes(t *testing.T) {
	t.Parallel()

	// Build a valid zip with 3 entries of 10 bytes each (30 bytes total).
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	for i := 0; i < 3; i++ {
		w, err := zw.Create("f" + string(rune('0'+i)) + ".txt")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("0123456789")); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(zipBuf.Bytes()), int64(zipBuf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := extractZipEntriesWithLimits(zr, 100, 100, 2); err == nil || !strings.Contains(err.Error(), "max verify entries") {
		t.Fatalf("zip entry count cap err = %v, want max verify entries", err)
	}
	if _, err := extractZipEntriesWithLimits(zr, 100, 25, 10); err == nil || !strings.Contains(err.Error(), "max total verify size") {
		t.Fatalf("zip total bytes cap err = %v, want max total verify size", err)
	}
	if entries, err := extractZipEntriesWithLimits(zr, 100, 30, 3); err != nil || len(entries) != 3 {
		t.Fatalf("zip within bounds = (%d, %v), want (3, nil)", len(entries), err)
	}

	// Build a valid tar.gz with 3 entries of 10 bytes each (30 bytes total).
	var tgzBuf bytes.Buffer
	gw := gzip.NewWriter(&tgzBuf)
	tw := tar.NewWriter(gw)
	for i := 0; i < 3; i++ {
		hdr := &tar.Header{Name: "f" + string(rune('0'+i)) + ".txt", Mode: 0o600, Size: 10}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("0123456789")); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readTarGzEntriesFromReaderWithLimits(bytes.NewReader(tgzBuf.Bytes()), 100, 100, 2); err == nil || !strings.Contains(err.Error(), "max verify entries") {
		t.Fatalf("tar.gz entry count cap err = %v, want max verify entries", err)
	}
	if _, err := readTarGzEntriesFromReaderWithLimits(bytes.NewReader(tgzBuf.Bytes()), 100, 25, 10); err == nil || !strings.Contains(err.Error(), "max total verify size") {
		t.Fatalf("tar.gz total bytes cap err = %v, want max total verify size", err)
	}
	if entries, err := readTarGzEntriesFromReaderWithLimits(bytes.NewReader(tgzBuf.Bytes()), 100, 30, 3); err != nil || len(entries) != 3 {
		t.Fatalf("tar.gz within bounds = (%d, %v), want (3, nil)", len(entries), err)
	}

	// Directory-only archives must also count against maxEntries (preventing dir-header bombs).
	var dirZipBuf bytes.Buffer
	dzw := zip.NewWriter(&dirZipBuf)
	for i := 0; i < 4; i++ {
		if _, err := dzw.Create("dir" + string(rune('0'+i)) + "/"); err != nil {
			t.Fatal(err)
		}
	}
	if err := dzw.Close(); err != nil {
		t.Fatal(err)
	}
	dzr, err := zip.NewReader(bytes.NewReader(dirZipBuf.Bytes()), int64(dirZipBuf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := extractZipEntriesWithLimits(dzr, 100, 100, 3); err == nil || !strings.Contains(err.Error(), "max verify entries") {
		t.Fatalf("zip dir-bomb err = %v, want max verify entries", err)
	}

	var dirTgzBuf bytes.Buffer
	dgw := gzip.NewWriter(&dirTgzBuf)
	dtw := tar.NewWriter(dgw)
	for i := 0; i < 4; i++ {
		if err := dtw.WriteHeader(&tar.Header{Name: "dir" + string(rune('0'+i)) + "/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
			t.Fatal(err)
		}
	}
	if err := dtw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := dgw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readTarGzEntriesFromReaderWithLimits(bytes.NewReader(dirTgzBuf.Bytes()), 100, 100, 3); err == nil || !strings.Contains(err.Error(), "max verify entries") {
		t.Fatalf("tar.gz dir-bomb err = %v, want max verify entries", err)
	}
}
