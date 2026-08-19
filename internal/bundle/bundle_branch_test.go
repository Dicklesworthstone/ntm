package bundle

import (
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
// verifyTarGz (0% → ~100%) — via Verify() tar.gz path
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Verify — unknown format branch
// ---------------------------------------------------------------------------

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
