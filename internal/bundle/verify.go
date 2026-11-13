package bundle

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// maxVerifyEntryBytes bounds individual archive entry reads during verification.
const maxVerifyEntryBytes = 256 * 1024 * 1024

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

// DetectFormat determines the archive format from a bundle file path.
func DetectFormat(bundlePath string) Format {
	lower := strings.ToLower(strings.TrimSpace(bundlePath))
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return FormatZip
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return FormatTarGz
	default:
		return FormatUnknown
	}
}

// Verify inspects a support bundle archive, validates its manifest schema, and
// checks that every declared file is present with matching size and SHA256.
func Verify(bundlePath string) (*VerifyResult, error) {
	if strings.TrimSpace(bundlePath) == "" {
		return nil, fmt.Errorf("bundle path is required")
	}
	if _, err := os.Stat(bundlePath); err != nil {
		return nil, fmt.Errorf("stat bundle: %w", err)
	}

	format := DetectFormat(bundlePath)
	var (
		entries map[string][]byte
		err     error
	)
	switch format {
	case FormatZip:
		entries, err = readZipEntries(bundlePath)
	case FormatTarGz:
		entries, err = readTarGzEntries(bundlePath)
	default:
		return nil, fmt.Errorf("unsupported bundle format for %q (expected .zip, .tar.gz, or .tgz)", bundlePath)
	}
	if err != nil {
		return nil, err
	}

	res := &VerifyResult{
		Details: map[string]string{
			"format": string(format),
		},
	}

	manifestRaw, ok := entries[ManifestFileName]
	if !ok {
		res.Errors = append(res.Errors, fmt.Sprintf("missing %s in archive", ManifestFileName))
		return res, nil
	}

	var manifest Manifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("parse %s: %v", ManifestFileName, err))
		return res, nil
	}

	res.Manifest = &manifest
	res.Details["file_count"] = strconv.Itoa(manifest.FileCount())
	res.Details["total_size"] = strconv.FormatInt(manifest.TotalSize(), 10)
	res.Details["ntm_version"] = manifest.NTMVersion

	if manifest.SchemaVersion >= MinSchemaVersion && manifest.SchemaVersion <= SchemaVersion {
		res.SchemaValid = true
	} else {
		res.Errors = append(res.Errors, fmt.Sprintf("unsupported schema_version: %d (expected %d-%d)", manifest.SchemaVersion, MinSchemaVersion, SchemaVersion))
	}

	if err := manifest.Validate(); err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("manifest validation failed: %v", err))
	} else {
		res.ManifestValid = true
	}

	allPresent := true
	allChecksums := true
	for _, fe := range manifest.Files {
		data, exists := entries[fe.Path]
		if !exists {
			allPresent = false
			allChecksums = false
			res.Errors = append(res.Errors, fmt.Sprintf("missing file %q declared in manifest", fe.Path))
			continue
		}
		if int64(len(data)) != fe.SizeBytes {
			allChecksums = false
			res.Errors = append(res.Errors, fmt.Sprintf("size mismatch for %q: archive=%d manifest=%d", fe.Path, len(data), fe.SizeBytes))
		}
		actualHash := HashBytes(data)
		if actualHash != fe.SHA256 {
			allChecksums = false
			res.Errors = append(res.Errors, fmt.Sprintf("checksum mismatch for %q: archive=%s manifest=%s", fe.Path, actualHash, fe.SHA256))
		}
	}

	for name := range entries {
		if name == ManifestFileName {
			continue
		}
		declared := false
		for _, fe := range manifest.Files {
			if fe.Path == name {
				declared = true
				break
			}
		}
		if !declared {
			res.Warnings = append(res.Warnings, fmt.Sprintf("undeclared archive entry %q", name))
		}
	}

	res.FilesPresent = allPresent
	res.ChecksumsValid = allPresent && allChecksums
	res.Valid = res.ManifestValid && res.SchemaValid && res.FilesPresent && res.ChecksumsValid
	return res, nil
}

func readZipEntries(bundlePath string) (map[string][]byte, error) {
	zr, err := zip.OpenReader(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("open zip bundle: %w", err)
	}
	defer func() { _ = zr.Close() }()

	entries := make(map[string][]byte, len(zr.File))
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		cleanName, err := SanitizeArchivePath(f.Name)
		if err != nil {
			return nil, fmt.Errorf("unsafe zip entry %q: %w", f.Name, err)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open zip entry %q: %w", f.Name, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(rc, maxVerifyEntryBytes+1))
		_ = rc.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read zip entry %q: %w", f.Name, readErr)
		}
		if len(data) > maxVerifyEntryBytes {
			return nil, fmt.Errorf("zip entry %q exceeds max verify size (%d bytes)", f.Name, maxVerifyEntryBytes)
		}
		entries[cleanName] = data
	}
	return entries, nil
}

func readTarGzEntries(bundlePath string) (map[string][]byte, error) {
	f, err := os.Open(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("open tar.gz bundle: %w", err)
	}
	defer func() { _ = f.Close() }()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("open gzip stream: %w", err)
	}
	defer func() { _ = gr.Close() }()

	tr := tar.NewReader(gr)
	entries := make(map[string][]byte)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar header: %w", err)
		}
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		cleanName, err := SanitizeArchivePath(hdr.Name)
		if err != nil {
			return nil, fmt.Errorf("unsafe tar entry %q: %w", hdr.Name, err)
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxVerifyEntryBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read tar entry %q: %w", hdr.Name, err)
		}
		if len(data) > maxVerifyEntryBytes {
			return nil, fmt.Errorf("tar entry %q exceeds max verify size (%d bytes)", hdr.Name, maxVerifyEntryBytes)
		}
		entries[cleanName] = data
	}
	return entries, nil
}
