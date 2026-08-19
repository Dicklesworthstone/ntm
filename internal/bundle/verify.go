package bundle

// VerifyResult contains the results of bundle verification.
type VerifyResult struct {
	// Valid is true if all checks passed.
	Valid bool `json:"valid"`

	// ManifestValid indicates if the manifest was found and parseable.
	ManifestValid bool `json:"manifest_valid"`

	// SchemaValid indicates if the schema version is supported.
	SchemaValid bool `json:"schema_valid"`

	// FilesPresent indicates if all manifest files exist in the bundle.
	FilesPresent bool `json:"files_present"`

	// ChecksumsValid indicates if all checksums match.
	ChecksumsValid bool `json:"checksums_valid"`

	// Errors contains any validation errors.
	Errors []string `json:"errors,omitempty"`

	// Warnings contains non-fatal issues.
	Warnings []string `json:"warnings,omitempty"`

	// Details contains additional verification details.
	Details map[string]string `json:"details,omitempty"`

	// Manifest is the parsed manifest (nil if not found/invalid).
	Manifest *Manifest `json:"manifest,omitempty"`
}

// ManifestFileName is the expected manifest file name in bundles.
const ManifestFileName = "manifest.json"

// Format represents a bundle archive format.
type Format string

const (
	FormatZip     Format = "zip"
	FormatTarGz   Format = "tar.gz"
	FormatUnknown Format = "unknown"
)

// DefaultFormat is the default bundle format.
const DefaultFormat = FormatZip

// Extension returns the file extension for a format.
func (f Format) Extension() string {
	switch f {
	case FormatZip:
		return ".zip"
	case FormatTarGz:
		return ".tar.gz"
	default:
		return ""
	}
}
