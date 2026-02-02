package supportbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"runtime"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

const (
	// SchemaVersion is the version of the support bundle manifest schema.
	SchemaVersion = 1

	// ManifestFilename is the canonical manifest name at the root of the bundle.
	ManifestFilename = "manifest.json"
)

// Host describes the environment the bundle was generated on.
type Host struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// Manifest is a versioned, machine-readable index of a support bundle's contents.
//
// Schema v1 (intentionally small, extensible):
//   - Root: manifest.json
//   - Manifest.Files[].Path MUST be a relative path within the bundle archive.
type Manifest struct {
	SchemaVersion int       `json:"schema_version"`
	GeneratedAt   time.Time `json:"generated_at"`
	NTMVersion    string    `json:"ntm_version"`
	Host          Host      `json:"host"`
	Files         []File    `json:"files"`
}

// File describes a single file included in the support bundle.
type File struct {
	Path             string            `json:"path"`
	SHA256           string            `json:"sha256"`
	SizeBytes        int64             `json:"size_bytes"`
	RedactionSummary *RedactionSummary `json:"redaction_summary,omitempty"`
}

// RedactionSummary captures counts of redaction findings for a file.
// It must never include raw matched secrets.
type RedactionSummary struct {
	Total      int                        `json:"total"`
	ByCategory map[redaction.Category]int `json:"by_category,omitempty"`
}

func DefaultHost() Host {
	return Host{
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
	}
}

func NewManifest(ntmVersion string) Manifest {
	return Manifest{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   time.Now().UTC(),
		NTMVersion:    ntmVersion,
		Host:          DefaultHost(),
	}
}

func NewFile(path string, data []byte, redactionSummary *RedactionSummary) File {
	return File{
		Path:             path,
		SHA256:           SHA256Hex(data),
		SizeBytes:        int64(len(data)),
		RedactionSummary: redactionSummary,
	}
}

func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func SummarizeRedactionFindings(findings []redaction.Finding) RedactionSummary {
	if len(findings) == 0 {
		return RedactionSummary{}
	}

	byCategory := make(map[redaction.Category]int)
	for _, f := range findings {
		byCategory[f.Category]++
	}

	return RedactionSummary{
		Total:      len(findings),
		ByCategory: byCategory,
	}
}
