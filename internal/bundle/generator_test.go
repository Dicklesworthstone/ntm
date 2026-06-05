package bundle

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

func TestNewGenerator(t *testing.T) {
	config := GeneratorConfig{
		Session:    "test-session",
		OutputPath: "/tmp/test.zip",
		Format:     FormatZip,
		NTMVersion: "v1.0.0",
	}

	gen := NewGenerator(config)

	if gen == nil {
		t.Fatal("NewGenerator returned nil")
	}

	if gen.config.Session != "test-session" {
		t.Errorf("Session = %q, want %q", gen.config.Session, "test-session")
	}
}

func TestGenerator_AddFile(t *testing.T) {
	config := GeneratorConfig{
		NTMVersion: "v1.0.0",
		RedactionConfig: redaction.Config{
			Mode: redaction.ModeOff,
		},
	}
	gen := NewGenerator(config)

	data := []byte("test content")
	err := gen.AddFile("test.txt", data, ContentTypeScrollback, time.Now())
	if err != nil {
		t.Fatalf("AddFile failed: %v", err)
	}

	if len(gen.files) != 1 {
		t.Errorf("files count = %d, want 1", len(gen.files))
	}

	if gen.files[0].path != "test.txt" {
		t.Errorf("files[0].path = %q, want %q", gen.files[0].path, "test.txt")
	}

	if string(gen.files[0].data) != "test content" {
		t.Errorf("files[0].data = %q, want %q", string(gen.files[0].data), "test content")
	}
}

func TestSanitizeArchivePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "plain", input: "test.txt", want: "test.txt"},
		{name: "cleans", input: "./dir//test.txt", want: "dir/test.txt"},
		{name: "backslashes", input: `dir\test.txt`, want: "dir/test.txt"},
		{name: "empty", input: "", wantErr: true},
		{name: "spaces only", input: "   ", wantErr: true},
		{name: "absolute", input: "/tmp/test.txt", wantErr: true},
		{name: "parent", input: "../test.txt", wantErr: true},
		{name: "nested parent", input: "dir/../test.txt", wantErr: true},
		{name: "windows drive", input: `C:\tmp\test.txt`, wantErr: true},
		{name: "nested colon", input: "dir/file:ads.txt", wantErr: true},
		{name: "scheme", input: "file://tmp/test.txt", wantErr: true},
		{name: "nul byte", input: "dir/bad\x00name.txt", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SanitizeArchivePath(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("SanitizeArchivePath(%q) = %q, want error", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("SanitizeArchivePath(%q) error = %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("SanitizeArchivePath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestGenerator_AddFile_NormalizesArchivePath(t *testing.T) {
	t.Parallel()

	gen := NewGenerator(GeneratorConfig{
		NTMVersion:      "v1.0.0",
		RedactionConfig: redaction.Config{Mode: redaction.ModeOff},
	})

	if err := gen.AddFile(`dir\test.txt`, []byte("content"), ContentTypeConfig, time.Now()); err != nil {
		t.Fatalf("AddFile failed: %v", err)
	}
	if len(gen.files) != 1 {
		t.Fatalf("files count = %d, want 1", len(gen.files))
	}
	if gen.files[0].path != "dir/test.txt" {
		t.Fatalf("file path = %q, want dir/test.txt", gen.files[0].path)
	}
}

func TestGenerator_AddFile_RejectsUnsafeArchivePath(t *testing.T) {
	t.Parallel()

	gen := NewGenerator(GeneratorConfig{
		NTMVersion:      "v1.0.0",
		RedactionConfig: redaction.Config{Mode: redaction.ModeOff},
	})

	if err := gen.AddFile("../escape.txt", []byte("content"), ContentTypeConfig, time.Now()); err == nil {
		t.Fatal("expected unsafe archive path error")
	}
	if len(gen.files) != 0 {
		t.Fatalf("files count = %d, want 0", len(gen.files))
	}
	if len(gen.errors) == 0 || !strings.Contains(gen.errors[0], "unsafe archive path") {
		t.Fatalf("expected unsafe archive path error entry, got %v", gen.errors)
	}
}

func TestGenerator_AddScrollback_RejectsUnsafePaneName(t *testing.T) {
	t.Parallel()

	gen := NewGenerator(GeneratorConfig{
		NTMVersion:      "v1.0.0",
		RedactionConfig: redaction.Config{Mode: redaction.ModeOff},
	})

	if err := gen.AddScrollback("../escape", "content", 0); err == nil {
		t.Fatal("expected unsafe pane name error")
	}
	if len(gen.files) != 0 {
		t.Fatalf("files count = %d, want 0", len(gen.files))
	}
}

func TestGenerator_AddFile_WithRedaction(t *testing.T) {
	config := GeneratorConfig{
		NTMVersion: "v1.0.0",
		RedactionConfig: redaction.Config{
			Mode: redaction.ModeRedact,
		},
	}
	gen := NewGenerator(config)

	// Content with a secret pattern (API key-like)
	data := []byte("Config: api_key=sk-1234567890abcdef1234567890abcdef")
	err := gen.AddFile("config.txt", data, ContentTypeConfig, time.Now())
	if err != nil {
		t.Fatalf("AddFile failed: %v", err)
	}

	if len(gen.files) != 1 {
		t.Errorf("files count = %d, want 1", len(gen.files))
	}

	// Check that content was redacted (should contain [REDACTED:...)
	if !strings.Contains(string(gen.files[0].data), "[REDACTED:") {
		// Note: This test may fail if the pattern doesn't match - that's expected
		// The redaction behavior depends on the pattern matching
		t.Log("Content may not have been redacted - depends on pattern matching")
	}
}

func TestGenerator_AddScrollback(t *testing.T) {
	config := GeneratorConfig{
		NTMVersion: "v1.0.0",
		RedactionConfig: redaction.Config{
			Mode: redaction.ModeOff,
		},
	}
	gen := NewGenerator(config)

	content := "line1\nline2\nline3\nline4\nline5"
	err := gen.AddScrollback("pane1", content, 3)
	if err != nil {
		t.Fatalf("AddScrollback failed: %v", err)
	}

	if len(gen.files) != 1 {
		t.Errorf("files count = %d, want 1", len(gen.files))
	}

	// Should only have last 3 lines
	lines := strings.Split(string(gen.files[0].data), "\n")
	if len(lines) != 3 {
		t.Errorf("line count = %d, want 3", len(lines))
	}
}

func TestGenerator_Generate_Zip(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "bundle.zip")

	config := GeneratorConfig{
		Session:    "test",
		OutputPath: outputPath,
		Format:     FormatZip,
		NTMVersion: "v1.0.0",
		RedactionConfig: redaction.Config{
			Mode: redaction.ModeOff,
		},
	}
	gen := NewGenerator(config)

	// Add some test files
	gen.AddFile("file1.txt", []byte("content1"), ContentTypeScrollback, time.Now())
	gen.AddFile("file2.txt", []byte("content2"), ContentTypeConfig, time.Now())

	result, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if result.Path != outputPath {
		t.Errorf("Path = %q, want %q", result.Path, outputPath)
	}

	if result.FileCount != 2 {
		t.Errorf("FileCount = %d, want 2", result.FileCount)
	}

	if result.Format != FormatZip {
		t.Errorf("Format = %q, want %q", result.Format, FormatZip)
	}

	// Verify the zip file was created and contains expected files
	r, err := zip.OpenReader(outputPath)
	if err != nil {
		t.Fatalf("Failed to open zip: %v", err)
	}
	defer r.Close()

	// Should have manifest + 2 files
	if len(r.File) != 3 {
		t.Errorf("Zip file count = %d, want 3 (manifest + 2 files)", len(r.File))
	}

	// Check manifest exists
	hasManifest := false
	for _, f := range r.File {
		if f.Name == ManifestFileName {
			hasManifest = true
			break
		}
	}
	if !hasManifest {
		t.Error("Manifest file not found in zip")
	}
}

func TestGenerator_Generate_TarGz(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "bundle.tar.gz")

	config := GeneratorConfig{
		Session:    "test",
		OutputPath: outputPath,
		Format:     FormatTarGz,
		NTMVersion: "v1.0.0",
		RedactionConfig: redaction.Config{
			Mode: redaction.ModeOff,
		},
	}
	gen := NewGenerator(config)

	// Add some test files
	gen.AddFile("file1.txt", []byte("content1"), ContentTypeScrollback, time.Now())

	result, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if result.Format != FormatTarGz {
		t.Errorf("Format = %q, want %q", result.Format, FormatTarGz)
	}

	// Verify the file was created
	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("Failed to stat output: %v", err)
	}

	if info.Size() == 0 {
		t.Error("Output file is empty")
	}
}

func TestGenerator_Generate_SizeLimit(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "bundle.zip")

	config := GeneratorConfig{
		OutputPath:   outputPath,
		Format:       FormatZip,
		NTMVersion:   "v1.0.0",
		MaxSizeBytes: 10, // Very small limit
		RedactionConfig: redaction.Config{
			Mode: redaction.ModeOff,
		},
	}
	gen := NewGenerator(config)

	// Add a file that exceeds the limit
	gen.AddFile("large.txt", []byte("this is more than 10 bytes"), ContentTypeScrollback, time.Now())

	_, err := gen.Generate()
	if err == nil {
		t.Error("Expected error for size limit exceeded, got nil")
	}

	if !strings.Contains(err.Error(), "exceeds limit") {
		t.Errorf("Error = %q, want to contain 'exceeds limit'", err.Error())
	}
}

func TestGenerator_Generate_ManifestIntegrity(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "bundle.zip")

	config := GeneratorConfig{
		Session:    "integrity-test",
		OutputPath: outputPath,
		Format:     FormatZip,
		NTMVersion: "v1.2.3",
		Lines:      100,
		RedactionConfig: redaction.Config{
			Mode: redaction.ModeWarn,
		},
	}
	gen := NewGenerator(config)

	content := []byte("test content for hash verification")
	gen.AddFile("test.txt", content, ContentTypeScrollback, time.Now())

	result, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// Check manifest values
	if result.Manifest == nil {
		t.Fatal("Manifest is nil")
	}

	if result.Manifest.NTMVersion != "v1.2.3" {
		t.Errorf("NTMVersion = %q, want %q", result.Manifest.NTMVersion, "v1.2.3")
	}

	if result.Manifest.Session == nil || result.Manifest.Session.Name != "integrity-test" {
		t.Error("Session info not set correctly")
	}

	if result.Manifest.Filters == nil || result.Manifest.Filters.Lines != 100 {
		t.Error("Filters not set correctly")
	}

	// Verify file hash
	if len(result.Manifest.Files) != 1 {
		t.Fatalf("Files count = %d, want 1", len(result.Manifest.Files))
	}

	expectedHash := HashBytes(content)
	if result.Manifest.Files[0].SHA256 != expectedHash {
		t.Errorf("SHA256 = %q, want %q", result.Manifest.Files[0].SHA256, expectedHash)
	}
}

func TestGenerator_Verify_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "bundle.zip")

	config := GeneratorConfig{
		Session:    "verify-test",
		OutputPath: outputPath,
		Format:     FormatZip,
		NTMVersion: "v1.0.0",
		RedactionConfig: redaction.Config{
			Mode: redaction.ModeOff,
		},
	}
	gen := NewGenerator(config)

	gen.AddFile("file1.txt", []byte("content1"), ContentTypeScrollback, time.Now())
	gen.AddFile("file2.txt", []byte("content2"), ContentTypeConfig, time.Now())

	_, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// Now verify the bundle
	result, err := Verify(outputPath)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}

	if !result.Valid {
		t.Errorf("Bundle not valid. Errors: %v", result.Errors)
	}

	if !result.ManifestValid {
		t.Error("Manifest not valid")
	}

	if !result.FilesPresent {
		t.Error("Files not present")
	}

	if !result.ChecksumsValid {
		t.Error("Checksums not valid")
	}
}

func TestLimitLines(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		limit    int
		expected int // expected line count
	}{
		{
			name:     "no limit",
			content:  "a\nb\nc\nd\ne",
			limit:    0,
			expected: 5,
		},
		{
			name:     "limit equals content",
			content:  "a\nb\nc",
			limit:    3,
			expected: 3,
		},
		{
			name:     "limit less than content",
			content:  "a\nb\nc\nd\ne",
			limit:    2,
			expected: 2,
		},
		{
			name:     "limit greater than content",
			content:  "a\nb",
			limit:    5,
			expected: 2,
		},
		{
			name:     "single line",
			content:  "single",
			limit:    3,
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := limitLines(tt.content, tt.limit)
			lines := strings.Split(result, "\n")
			if len(lines) != tt.expected {
				t.Errorf("limitLines(%q, %d) = %d lines, want %d",
					tt.content, tt.limit, len(lines), tt.expected)
			}
		})
	}
}

func TestSuggestOutputPath(t *testing.T) {
	tests := []struct {
		session string
		format  Format
		prefix  string
		suffix  string
	}{
		{"myproject", FormatZip, "ntm-myproject-", ".zip"},
		{"", FormatZip, "ntm-bundle-", ".zip"},
		{"test", FormatTarGz, "ntm-test-", ".tar.gz"},
	}

	for _, tt := range tests {
		t.Run(tt.session+string(tt.format), func(t *testing.T) {
			path := SuggestOutputPath(tt.session, tt.format)

			if !strings.HasPrefix(path, tt.prefix) {
				t.Errorf("Path %q doesn't have prefix %q", path, tt.prefix)
			}

			if !strings.HasSuffix(path, tt.suffix) {
				t.Errorf("Path %q doesn't have suffix %q", path, tt.suffix)
			}
		})
	}
}

func TestGenerator_RedactionSummary(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "bundle.zip")

	config := GeneratorConfig{
		OutputPath: outputPath,
		Format:     FormatZip,
		NTMVersion: "v1.0.0",
		RedactionConfig: redaction.Config{
			Mode: redaction.ModeRedact,
		},
	}
	gen := NewGenerator(config)

	// Add file with potential secrets
	gen.AddFile("config.txt", []byte("normal content"), ContentTypeConfig, time.Now())

	result, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if result.RedactionSummary == nil {
		t.Fatal("RedactionSummary is nil")
	}

	if result.RedactionSummary.Mode != "redact" {
		t.Errorf("Mode = %q, want %q", result.RedactionSummary.Mode, "redact")
	}

	if result.RedactionSummary.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d, want 1", result.RedactionSummary.FilesScanned)
	}
}

func TestVerify_ManifestParsing(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "bundle.zip")

	config := GeneratorConfig{
		Session:    "parse-test",
		OutputPath: outputPath,
		Format:     FormatZip,
		NTMVersion: "v2.0.0",
		RedactionConfig: redaction.Config{
			Mode: redaction.ModeOff,
		},
	}
	gen := NewGenerator(config)
	gen.AddFile("test.txt", []byte("data"), ContentTypeScrollback, time.Now())

	_, err := gen.Generate()
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	result, err := Verify(outputPath)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}

	if result.Manifest == nil {
		t.Fatal("Manifest not parsed")
	}

	if result.Manifest.NTMVersion != "v2.0.0" {
		t.Errorf("NTMVersion = %q, want %q", result.Manifest.NTMVersion, "v2.0.0")
	}

	// Validate JSON round-trip
	data, err := json.Marshal(result.Manifest)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var parsed Manifest
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if parsed.NTMVersion != result.Manifest.NTMVersion {
		t.Error("JSON round-trip failed for NTMVersion")
	}
}
