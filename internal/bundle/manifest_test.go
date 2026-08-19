package bundle

import (
	"encoding/json"
	"runtime"
	"testing"
	"time"
)

func TestNewManifest(t *testing.T) {
	m := NewManifest("v1.2.3")

	if m.SchemaVersion != SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", m.SchemaVersion, SchemaVersion)
	}

	if m.NTMVersion != "v1.2.3" {
		t.Errorf("NTMVersion = %q, want %q", m.NTMVersion, "v1.2.3")
	}

	if m.Host.OS != runtime.GOOS {
		t.Errorf("Host.OS = %q, want %q", m.Host.OS, runtime.GOOS)
	}

	if m.Host.Arch != runtime.GOARCH {
		t.Errorf("Host.Arch = %q, want %q", m.Host.Arch, runtime.GOARCH)
	}

	if m.GeneratedAt == "" {
		t.Error("GeneratedAt should not be empty")
	}

	// Verify GeneratedAt is valid RFC3339
	if _, err := time.Parse(time.RFC3339, m.GeneratedAt); err != nil {
		t.Errorf("GeneratedAt %q is not valid RFC3339: %v", m.GeneratedAt, err)
	}

	if len(m.Files) != 0 {
		t.Errorf("Files should be empty, got %d", len(m.Files))
	}
}

func TestManifest_AddFile(t *testing.T) {
	m := NewManifest("v1.0.0")

	entry := FileEntry{
		Path:        "panes/cc_1.txt",
		SHA256:      "abc123" + string(make([]byte, 58)), // 64 chars
		SizeBytes:   1234,
		ContentType: ContentTypeScrollback,
	}
	m.AddFile(entry)

	if len(m.Files) != 1 {
		t.Errorf("Files count = %d, want 1", len(m.Files))
	}

	if m.Files[0].Path != entry.Path {
		t.Errorf("Files[0].Path = %q, want %q", m.Files[0].Path, entry.Path)
	}
}

func TestManifest_TotalSize(t *testing.T) {
	m := NewManifest("v1.0.0")
	m.AddFile(FileEntry{Path: "a.txt", SHA256: makeValidHash(), SizeBytes: 100})
	m.AddFile(FileEntry{Path: "b.txt", SHA256: makeValidHash(), SizeBytes: 200})
	m.AddFile(FileEntry{Path: "c.txt", SHA256: makeValidHash(), SizeBytes: 300})

	if m.TotalSize() != 600 {
		t.Errorf("TotalSize() = %d, want 600", m.TotalSize())
	}
}

func TestManifest_Validate(t *testing.T) {
	tests := []struct {
		name    string
		modify  func(*Manifest)
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid manifest",
			modify:  func(m *Manifest) {},
			wantErr: false,
		},
		{
			name: "invalid schema version - too low",
			modify: func(m *Manifest) {
				m.SchemaVersion = 0
			},
			wantErr: true,
			errMsg:  "unsupported schema_version",
		},
		{
			name: "invalid schema version - too high",
			modify: func(m *Manifest) {
				m.SchemaVersion = 999
			},
			wantErr: true,
			errMsg:  "unsupported schema_version",
		},
		{
			name: "missing generated_at",
			modify: func(m *Manifest) {
				m.GeneratedAt = ""
			},
			wantErr: true,
			errMsg:  "missing generated_at",
		},
		{
			name: "missing ntm_version",
			modify: func(m *Manifest) {
				m.NTMVersion = ""
			},
			wantErr: true,
			errMsg:  "missing ntm_version",
		},
		{
			name: "missing host os",
			modify: func(m *Manifest) {
				m.Host.OS = ""
			},
			wantErr: true,
			errMsg:  "incomplete host info",
		},
		{
			name: "missing host arch",
			modify: func(m *Manifest) {
				m.Host.Arch = ""
			},
			wantErr: true,
			errMsg:  "incomplete host info",
		},
		{
			name: "file with missing path",
			modify: func(m *Manifest) {
				m.Files = []FileEntry{{Path: "", SHA256: makeValidHash(), SizeBytes: 100}}
			},
			wantErr: true,
			errMsg:  "missing path",
		},
		{
			name: "file with missing sha256",
			modify: func(m *Manifest) {
				m.Files = []FileEntry{{Path: "a.txt", SHA256: "", SizeBytes: 100}}
			},
			wantErr: true,
			errMsg:  "missing sha256",
		},
		{
			name: "file with invalid sha256 length",
			modify: func(m *Manifest) {
				m.Files = []FileEntry{{Path: "a.txt", SHA256: "short", SizeBytes: 100}}
			},
			wantErr: true,
			errMsg:  "invalid sha256 length",
		},
		{
			name: "file with negative size",
			modify: func(m *Manifest) {
				m.Files = []FileEntry{{Path: "a.txt", SHA256: makeValidHash(), SizeBytes: -1}}
			},
			wantErr: true,
			errMsg:  "invalid size_bytes",
		},
		{
			name: "duplicate file paths",
			modify: func(m *Manifest) {
				m.Files = []FileEntry{
					{Path: "a.txt", SHA256: makeValidHash(), SizeBytes: 100},
					{Path: "a.txt", SHA256: makeValidHash(), SizeBytes: 200},
				}
			},
			wantErr: true,
			errMsg:  "duplicate path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewManifest("v1.0.0")
			tt.modify(m)

			err := m.Validate()
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				} else if tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
					t.Errorf("error = %q, want to contain %q", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestManifest_JSON(t *testing.T) {
	m := NewManifest("v1.0.0")
	m.Session = &SessionInfo{
		Name:        "myproject",
		Directory:   "/home/user/myproject",
		PaneCount:   5,
		AgentCounts: map[string]int{"cc": 2, "cod": 2, "user": 1},
	}
	m.AddFile(FileEntry{
		Path:        "panes/cc_1.txt",
		SHA256:      makeValidHash(),
		SizeBytes:   1234,
		ContentType: ContentTypeScrollback,
		Redaction: &FileRedaction{
			WasRedacted:  true,
			FindingCount: 2,
			Categories:   []string{"OPENAI_KEY", "GITHUB_TOKEN"},
		},
	})
	m.RedactionSummary = &RedactionSummary{
		Mode:          "redact",
		TotalFindings: 2,
		FilesScanned:  1,
		FilesRedacted: 1,
		CategoryCounts: map[string]int{
			"OPENAI_KEY":   1,
			"GITHUB_TOKEN": 1,
		},
	}

	// Marshal to JSON
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	// Unmarshal back
	var m2 Manifest
	if err := json.Unmarshal(data, &m2); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// Verify round-trip
	if m2.SchemaVersion != m.SchemaVersion {
		t.Errorf("SchemaVersion mismatch: %d != %d", m2.SchemaVersion, m.SchemaVersion)
	}
	if m2.Session.Name != m.Session.Name {
		t.Errorf("Session.Name mismatch: %q != %q", m2.Session.Name, m.Session.Name)
	}
	if len(m2.Files) != len(m.Files) {
		t.Errorf("Files count mismatch: %d != %d", len(m2.Files), len(m.Files))
	}
	if m2.RedactionSummary.TotalFindings != m.RedactionSummary.TotalFindings {
		t.Errorf("RedactionSummary.TotalFindings mismatch: %d != %d",
			m2.RedactionSummary.TotalFindings, m.RedactionSummary.TotalFindings)
	}
}

func TestHashBytes(t *testing.T) {
	content := []byte("hello world")
	hash := HashBytes(content)

	expected := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if hash != expected {
		t.Errorf("hash = %q, want %q", hash, expected)
	}
}

func TestFormat_Extension(t *testing.T) {
	tests := []struct {
		format Format
		ext    string
	}{
		{FormatZip, ".zip"},
		{FormatTarGz, ".tar.gz"},
		{FormatUnknown, ""},
	}

	for _, tt := range tests {
		t.Run(string(tt.format), func(t *testing.T) {
			got := tt.format.Extension()
			if got != tt.ext {
				t.Errorf("Format(%q).Extension() = %q, want %q", tt.format, got, tt.ext)
			}
		})
	}
}

// Helper functions

func makeValidHash() string {
	// Return a valid 64-character hex string
	return "0000000000000000000000000000000000000000000000000000000000000000"
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr, 0))
}

func containsAt(s, substr string, start int) bool {
	for i := start; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestManifest_AddError(t *testing.T) {
	t.Parallel()

	m := NewManifest("v1.0.0")

	// Initially no errors
	if len(m.Errors) != 0 {
		t.Fatalf("expected no errors initially, got %d", len(m.Errors))
	}

	// Add first error
	m.AddError("failed to read file: permission denied")
	if len(m.Errors) != 1 {
		t.Errorf("Errors count = %d, want 1", len(m.Errors))
	}
	if m.Errors[0] != "failed to read file: permission denied" {
		t.Errorf("Errors[0] = %q, want %q", m.Errors[0], "failed to read file: permission denied")
	}

	// Add second error
	m.AddError("skipped large binary file")
	if len(m.Errors) != 2 {
		t.Errorf("Errors count = %d, want 2", len(m.Errors))
	}
	if m.Errors[1] != "skipped large binary file" {
		t.Errorf("Errors[1] = %q, want %q", m.Errors[1], "skipped large binary file")
	}
}

func TestManifest_FileCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files int
	}{
		{"empty manifest", 0},
		{"single file", 1},
		{"multiple files", 5},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := NewManifest("v1.0.0")

			for i := 0; i < tc.files; i++ {
				m.AddFile(FileEntry{
					Path:      "file" + string(rune('a'+i)) + ".txt",
					SHA256:    makeValidHash(),
					SizeBytes: int64(100 * (i + 1)),
				})
			}

			if got := m.FileCount(); got != tc.files {
				t.Errorf("FileCount() = %d, want %d", got, tc.files)
			}
		})
	}
}
