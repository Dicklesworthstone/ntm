// Package bundle provides support bundle generation and manifest handling.
// Support bundles are archives containing diagnostic information for debugging
// and support purposes, with built-in redaction for sensitive content.
package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
	"time"
)

// SchemaVersion is the current manifest schema version.
// Increment when making breaking changes to the manifest format.
const SchemaVersion = 1

// MinSchemaVersion is the minimum supported manifest schema version.
const MinSchemaVersion = 1

// Manifest represents the metadata for a support bundle.
// It provides integrity verification and content inventory.
type Manifest struct {
	// SchemaVersion is the manifest format version.
	SchemaVersion int `json:"schema_version"`

	// GeneratedAt is when the bundle was created (RFC3339).
	GeneratedAt string `json:"generated_at"`

	// NTMVersion is the version of NTM that created the bundle.
	NTMVersion string `json:"ntm_version"`

	// Host contains information about the generating machine.
	Host HostInfo `json:"host"`

	// Session contains optional session context.
	Session *SessionInfo `json:"session,omitempty"`

	// Files lists all files included in the bundle.
	Files []FileEntry `json:"files"`

	// RedactionSummary provides an aggregate view of redaction actions.
	RedactionSummary *RedactionSummary `json:"redaction_summary,omitempty"`

	// Filters contains the time/size constraints used during generation.
	Filters *BundleFilters `json:"filters,omitempty"`

	// Errors lists any non-fatal errors during bundle generation.
	Errors []string `json:"errors,omitempty"`
}

// HostInfo contains information about the machine that generated the bundle.
type HostInfo struct {
	// OS is the operating system (e.g., "linux", "darwin", "windows").
	OS string `json:"os"`

	// Arch is the CPU architecture (e.g., "amd64", "arm64").
	Arch string `json:"arch"`

	// Hostname is the machine hostname (may be redacted).
	Hostname string `json:"hostname,omitempty"`

	// Username is the current user (may be redacted).
	Username string `json:"username,omitempty"`
}

// SessionInfo contains optional session context for the bundle.
type SessionInfo struct {
	// Name is the tmux session name.
	Name string `json:"name"`

	// Directory is the working directory.
	Directory string `json:"directory,omitempty"`

	// PaneCount is the number of panes in the session.
	PaneCount int `json:"pane_count,omitempty"`

	// AgentCounts maps agent type to count.
	AgentCounts map[string]int `json:"agent_counts,omitempty"`
}

// FileEntry describes a single file in the bundle.
type FileEntry struct {
	// Path is the relative path within the bundle.
	Path string `json:"path"`

	// SHA256 is the hex-encoded SHA256 hash of the file content.
	SHA256 string `json:"sha256"`

	// SizeBytes is the file size in bytes.
	SizeBytes int64 `json:"size_bytes"`

	// ContentType describes the file content (e.g., "scrollback", "config", "logs").
	ContentType string `json:"content_type,omitempty"`

	// Redaction contains redaction details for this file.
	Redaction *FileRedaction `json:"redaction,omitempty"`

	// SourcePath is the original file path (may be redacted or omitted).
	SourcePath string `json:"source_path,omitempty"`

	// ModTime is the original file modification time (RFC3339).
	ModTime string `json:"mod_time,omitempty"`
}

// FileRedaction describes redaction actions for a single file.
type FileRedaction struct {
	// WasRedacted indicates if any content was redacted.
	WasRedacted bool `json:"was_redacted"`

	// FindingCount is the number of secrets detected.
	FindingCount int `json:"finding_count"`

	// Categories lists the types of secrets found.
	Categories []string `json:"categories,omitempty"`

	// OriginalSize is the file size before redaction.
	OriginalSize int64 `json:"original_size,omitempty"`
}

// RedactionSummary provides aggregate redaction statistics.
type RedactionSummary struct {
	// Mode is the redaction mode used (off, warn, redact, block).
	Mode string `json:"mode"`

	// TotalFindings is the total number of secrets detected.
	TotalFindings int `json:"total_findings"`

	// FilesScanned is the number of files scanned.
	FilesScanned int `json:"files_scanned"`

	// FilesRedacted is the number of files with redactions.
	FilesRedacted int `json:"files_redacted"`

	// CategoryCounts maps category to occurrence count.
	CategoryCounts map[string]int `json:"category_counts,omitempty"`
}

// BundleFilters contains the constraints used during bundle generation.
type BundleFilters struct {
	// Since is the earliest timestamp for included content (RFC3339).
	Since string `json:"since,omitempty"`

	// Lines is the maximum lines of scrollback per pane.
	Lines int `json:"lines,omitempty"`

	// MaxSizeBytes is the maximum bundle size limit.
	MaxSizeBytes int64 `json:"max_size_bytes,omitempty"`
}

// NewManifest creates a new manifest with default values.
func NewManifest(ntmVersion string) *Manifest {
	hostname, _ := os.Hostname()

	return &Manifest{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		NTMVersion:    ntmVersion,
		Host: HostInfo{
			OS:       runtime.GOOS,
			Arch:     runtime.GOARCH,
			Hostname: hostname,
		},
		Files: []FileEntry{},
	}
}

// AddFile adds a file entry to the manifest.
func (m *Manifest) AddFile(entry FileEntry) {
	m.Files = append(m.Files, entry)
}

// AddError records a non-fatal error during bundle generation.
func (m *Manifest) AddError(msg string) {
	m.Errors = append(m.Errors, msg)
}

// TotalSize returns the total size of all files in the bundle.
func (m *Manifest) TotalSize() int64 {
	var total int64
	for _, f := range m.Files {
		total += f.SizeBytes
	}
	return total
}

// FileCount returns the number of files in the bundle.
func (m *Manifest) FileCount() int {
	return len(m.Files)
}

// Validate checks if the manifest is valid.
func (m *Manifest) Validate() error {
	if m.SchemaVersion < MinSchemaVersion || m.SchemaVersion > SchemaVersion {
		return fmt.Errorf("unsupported schema_version: %d (expected %d-%d)", m.SchemaVersion, MinSchemaVersion, SchemaVersion)
	}

	if m.GeneratedAt == "" {
		return fmt.Errorf("missing generated_at")
	}

	if m.NTMVersion == "" {
		return fmt.Errorf("missing ntm_version")
	}

	if m.Host.OS == "" || m.Host.Arch == "" {
		return fmt.Errorf("incomplete host info: os=%q arch=%q", m.Host.OS, m.Host.Arch)
	}

	// Validate file entries
	seenPaths := make(map[string]bool)
	for i, f := range m.Files {
		if f.Path == "" {
			return fmt.Errorf("file[%d]: missing path", i)
		}
		if seenPaths[f.Path] {
			return fmt.Errorf("file[%d]: duplicate path %q", i, f.Path)
		}
		seenPaths[f.Path] = true

		if f.SHA256 == "" {
			return fmt.Errorf("file[%d] (%s): missing sha256", i, f.Path)
		}
		if len(f.SHA256) != 64 {
			return fmt.Errorf("file[%d] (%s): invalid sha256 length: %d", i, f.Path, len(f.SHA256))
		}

		if f.SizeBytes < 0 {
			return fmt.Errorf("file[%d] (%s): invalid size_bytes: %d", i, f.Path, f.SizeBytes)
		}
	}

	return nil
}

// HashBytes computes the SHA256 hash of a byte slice.
func HashBytes(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// ContentTypes for FileEntry.ContentType field.
const (
	ContentTypeScrollback = "scrollback"
	ContentTypeConfig     = "config"
	ContentTypeLogs       = "logs"
	ContentTypeState      = "state"
	ContentTypeMetadata   = "metadata"
	ContentTypePatch      = "patch"
	ContentTypeUnknown    = "unknown"
)
