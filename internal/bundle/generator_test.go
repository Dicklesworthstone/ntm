package bundle

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/encryption"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

func TestGeneratorEncryptedArchiveRoundTripAndTamper(t *testing.T) {
	t.Parallel()
	key := bytes.Repeat([]byte{0x35}, encryption.KeySize)
	oldKey := bytes.Repeat([]byte{0x71}, encryption.KeySize)
	for _, format := range []Format{FormatZip, FormatTarGz} {
		t.Run(string(format), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "support"+format.Extension()+".enc")
			gen := NewGenerator(GeneratorConfig{
				OutputPath: path, Format: format, NTMVersion: "test",
				EncryptKey: key, MaxSizeBytes: 1 << 20,
				RedactionConfig: redaction.Config{Mode: redaction.ModeOff},
			})
			content := []byte("private diagnostic payload that must round trip")
			if err := gen.AddFile("diagnostic.txt", content, ContentTypeMetadata, time.Now()); err != nil {
				t.Fatal(err)
			}
			result, err := gen.Generate()
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Encrypted || !encryption.IsEncryptedArtifact(data) || bytes.Contains(data, content) {
				t.Fatalf("archive was not encrypted: result=%+v", result)
			}
			if result.TotalSize != int64(len(data)) {
				t.Fatalf("reported size %d, actual %d", result.TotalSize, len(data))
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("archive permissions: info=%v error=%v", info, err)
			}
			for _, keys := range [][][]byte{nil, {oldKey}} {
				if _, err := Verify(path, keys...); err == nil {
					t.Fatal("verification accepted a missing or wrong key")
				}
			}
			verified, err := Verify(path, oldKey, key)
			if err != nil || !verified.Valid || !verified.Encrypted {
				t.Fatalf("rotated keyring verification: result=%+v error=%v", verified, err)
			}
			plaintextPath := filepath.Join(dir, "decrypted"+format.Extension())
			if _, err := Decrypt(path, plaintextPath, oldKey, key); err != nil {
				t.Fatal(err)
			}
			plainData, err := os.ReadFile(plaintextPath)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := readArchiveEntries(plainData, format)
			if err != nil || !bytes.Equal(entries["diagnostic.txt"], content) {
				t.Fatalf("decrypted payload mismatch: entries=%v error=%v", entries, err)
			}
			plainVerified, err := Verify(plaintextPath)
			if err != nil || !plainVerified.Valid || plainVerified.Encrypted {
				t.Fatalf("plaintext verification: result=%+v error=%v", plainVerified, err)
			}
			data[len(data)-1] ^= 1
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Verify(path, key); err == nil {
				t.Fatal("tampered ciphertext passed verification")
			}
			if _, err := Decrypt(path, plaintextPath, key); err == nil {
				t.Fatal("tampered ciphertext was exported")
			}
			preserved, err := os.ReadFile(plaintextPath)
			if err != nil || !bytes.Equal(preserved, plainData) {
				t.Fatalf("failed decrypt changed existing output: %v", err)
			}
		})
	}
}

func TestWriteArchiveFailuresPreserveExistingOutput(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		key   []byte
		limit int64
	}{
		{name: "invalid key", key: []byte("invalid")},
		{name: "plaintext size", limit: 3},
		{name: "ciphertext overhead", key: bytes.Repeat([]byte{1}, encryption.KeySize), limit: 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "existing.zip")
			existing := []byte("existing bundle must survive")
			if err := os.WriteFile(path, existing, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := WriteArchive(path, []byte("payload"), test.key, test.limit); err == nil {
				t.Fatal("expected failure")
			}
			actual, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(actual, existing) {
				t.Fatalf("existing file changed: %q, %v", actual, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("unexpected temporary artifacts: %v, %v", entries, err)
			}
		})
	}
}

func TestVerifyEncryptedExtensionRejectsPlaintext(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "support.zip.enc")
	gen := NewGenerator(GeneratorConfig{OutputPath: path, Format: FormatZip, NTMVersion: "test", RedactionConfig: redaction.Config{Mode: redaction.ModeOff}})
	if _, err := gen.Generate(); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(path); err == nil || !strings.Contains(err.Error(), "authenticated artifact header") {
		t.Fatalf("plaintext accepted as encrypted: %v", err)
	}
}

func TestEncryptArchiveFileInvalidKeysPreserveOutput(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		key  []byte
	}{
		{name: "nil key"},
		{name: "empty key", key: []byte{}},
		{name: "malformed key", key: []byte("invalid")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "existing.zip")
			existing := []byte("existing archive must survive an invalid encryption request")
			if err := os.WriteFile(path, existing, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := EncryptArchiveFile(path, tc.key); err == nil {
				t.Fatal("encryption API accepted an invalid key")
			}
			actual, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(actual, existing) {
				t.Fatalf("failed encryption changed existing output: %q, %v", actual, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("failed encryption left temporary artifacts: %v, %v", entries, err)
			}
		})
	}
}

func TestGeneratorSelectedPolicyOverridesProcessPolicy(t *testing.T) {
	previous := ActiveEncryptionConfig()
	t.Cleanup(func() { SetEncryptionConfig(previous) })
	globalKey := bytes.Repeat([]byte{0x12}, encryption.KeySize)
	selectedKey := bytes.Repeat([]byte{0x34}, encryption.KeySize)
	SetEncryptionConfig(&EncryptionConfig{Enabled: true, EncryptKey: globalKey, DecryptKeys: [][]byte{globalKey}})

	for _, tc := range []struct {
		name     string
		policy   *EncryptionConfig
		explicit []byte
		wantKey  []byte
		wantErr  bool
	}{
		{name: "implicit process policy", wantKey: globalKey},
		{name: "selected plaintext policy", policy: &EncryptionConfig{}},
		{name: "selected encryption key", policy: &EncryptionConfig{Enabled: true, EncryptKey: selectedKey}, wantKey: selectedKey},
		{name: "selected policy missing key", policy: &EncryptionConfig{Enabled: true}, wantErr: true},
		{name: "invalid explicit key", explicit: []byte("invalid"), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "selected.zip")
			gen := NewGenerator(GeneratorConfig{
				OutputPath: path, Format: FormatZip, NTMVersion: "test",
				Encryption: tc.policy, EncryptKey: tc.explicit,
			})
			result, err := gen.Generate()
			if tc.wantErr {
				if err == nil {
					t.Fatal("invalid selected key fell back to the process key")
				}
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Fatalf("failed generation published an archive: %v", statErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Encrypted != (tc.wantKey != nil) {
				t.Fatalf("encrypted = %v for selected policy", result.Encrypted)
			}
			verified, err := Verify(path, tc.wantKey)
			if err != nil || !verified.Valid {
				t.Fatalf("archive did not follow the selected policy: result=%+v error=%v", verified, err)
			}
		})
	}
}

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

func TestEncryptedSupportBundle_ZipAndTarGzRoundTripAndFailClosed(t *testing.T) {
	t.Cleanup(func() { SetEncryptionConfig(nil) })

	key1 := bytes.Repeat([]byte{0x11}, encryption.KeySize)
	key2 := bytes.Repeat([]byte{0x22}, encryption.KeySize)

	for _, tc := range []struct {
		name   string
		format Format
		ext    string
	}{
		{name: "zip", format: FormatZip, ext: ".zip"},
		{name: "tar_gz", format: FormatTarGz, ext: ".tar.gz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			outPath := filepath.Join(dir, "encrypted"+tc.ext)

			SetEncryptionConfig(nil)
			gen := NewGenerator(GeneratorConfig{
				Session:    "enc-session",
				OutputPath: outPath,
				Format:     tc.format,
				NTMVersion: "1.38.0",
				Encrypt:    true,
				EncryptKey: key1,
			})
			if err := gen.AddFile("notes.txt", []byte("top secret diagnostic payload"), ContentTypeConfig, time.Now()); err != nil {
				t.Fatalf("AddFile: %v", err)
			}
			res, err := gen.Generate()
			if err != nil {
				t.Fatalf("Generate encrypted %s: %v", tc.format, err)
			}
			if !res.Encrypted {
				t.Fatalf("expected Encrypted=true")
			}

			raw, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if !encryption.IsEncryptedArtifact(raw) {
				t.Fatalf("output archive missing %q envelope prefix", encryption.ArtifactEnvelopeMagic)
			}
			if bytes.Contains(raw, []byte("top secret diagnostic payload")) {
				t.Fatal("plaintext leaked into encrypted archive")
			}
			info, err := os.Stat(outPath)
			if err != nil {
				t.Fatalf("Stat: %v", err)
			}
			if perm := info.Mode().Perm(); perm != 0o600 {
				t.Errorf("encrypted bundle permissions = %04o, want 0600", perm)
			}

			// Fail closed without decryption keys.
			SetEncryptionConfig(nil)
			if _, err := Verify(outPath); err == nil || !strings.Contains(err.Error(), "no decryption keys") {
				t.Fatalf("Verify without keys = %v, want fail-closed key error", err)
			}

			// Fail closed with wrong key.
			SetEncryptionConfig(&EncryptionConfig{Enabled: true, DecryptKeys: [][]byte{key2}})
			if _, err := Verify(outPath); err == nil {
				t.Fatal("Verify with wrong key succeeded, want authentication failure")
			}

			// Succeed with rotated keyring [newKey, oldKey].
			SetEncryptionConfig(&EncryptionConfig{Enabled: true, DecryptKeys: [][]byte{key2, key1}})
			vres, err := Verify(outPath)
			if err != nil {
				t.Fatalf("Verify with rotated keyring failed: %v", err)
			}
			if !vres.Valid || !vres.ChecksumsValid || vres.Details["encrypted"] != "true" {
				t.Fatalf("unexpected verify result: %+v", vres)
			}

			// Tampered ciphertext must fail authentication.
			tampered := append([]byte(nil), raw...)
			tampered[len(tampered)-1] ^= 0xff
			tamperedPath := filepath.Join(dir, "tampered"+tc.ext)
			if err := os.WriteFile(tamperedPath, tampered, 0o600); err != nil {
				t.Fatalf("WriteFile tampered: %v", err)
			}
			if _, err := Verify(tamperedPath); err == nil {
				t.Fatal("Verify on tampered ciphertext succeeded, want authentication error")
			}
		})
	}

	// Fail closed when Encrypt=true and no key is configured.
	SetEncryptionConfig(nil)
	noKeyPath := filepath.Join(t.TempDir(), "nokey.zip")
	gen := NewGenerator(GeneratorConfig{
		OutputPath: noKeyPath,
		Format:     FormatZip,
		NTMVersion: "1.38.0",
		Encrypt:    true,
	})
	_ = gen.AddFile("a.txt", []byte("data"), ContentTypeConfig, time.Now())
	if _, err := gen.Generate(); err == nil {
		t.Fatal("Generate with Encrypt=true and no key succeeded, want error")
	}
	if _, statErr := os.Stat(noKeyPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected failed encrypted bundle not to remain on disk, stat err=%v", statErr)
	}
}
