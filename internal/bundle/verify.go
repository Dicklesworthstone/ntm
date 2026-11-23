package bundle

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/Dicklesworthstone/ntm/internal/encryption"
)

// EncryptionConfig controls optional AES-256-GCM artifact encryption for
// support bundles (docs/ENCRYPTION_SPEC.md).
type EncryptionConfig struct {
	Enabled     bool
	EncryptKey  []byte
	DecryptKeys [][]byte
}

var (
	bundleEncMu  sync.RWMutex
	bundleEncCfg *EncryptionConfig
)

// SetEncryptionConfig configures process-wide support bundle encryption keys.
// Passing nil disables encryption.
func SetEncryptionConfig(cfg *EncryptionConfig) {
	bundleEncMu.Lock()
	defer bundleEncMu.Unlock()
	if cfg == nil {
		bundleEncCfg = nil
		return
	}
	cp := &EncryptionConfig{
		Enabled: cfg.Enabled,
	}
	if len(cfg.EncryptKey) > 0 {
		cp.EncryptKey = append([]byte(nil), cfg.EncryptKey...)
	}
	if len(cfg.DecryptKeys) > 0 {
		cp.DecryptKeys = make([][]byte, len(cfg.DecryptKeys))
		for i, k := range cfg.DecryptKeys {
			cp.DecryptKeys[i] = append([]byte(nil), k...)
		}
	}
	bundleEncCfg = cp
}

// ActiveEncryptionConfig returns a defensive copy of the active support bundle
// encryption config, or nil when encryption is disabled.
func ActiveEncryptionConfig() *EncryptionConfig {
	bundleEncMu.RLock()
	defer bundleEncMu.RUnlock()
	if bundleEncCfg == nil {
		return nil
	}
	cp := &EncryptionConfig{
		Enabled: bundleEncCfg.Enabled,
	}
	if len(bundleEncCfg.EncryptKey) > 0 {
		cp.EncryptKey = append([]byte(nil), bundleEncCfg.EncryptKey...)
	}
	if len(bundleEncCfg.DecryptKeys) > 0 {
		cp.DecryptKeys = make([][]byte, len(bundleEncCfg.DecryptKeys))
		for i, k := range bundleEncCfg.DecryptKeys {
			cp.DecryptKeys[i] = append([]byte(nil), k...)
		}
	}
	return cp
}

// Bounds for archive verification to prevent zip/tar decompression bombs.
const (
	maxVerifyEntryBytes = 256 * 1024 * 1024
	maxVerifyTotalBytes = 512 * 1024 * 1024
	maxVerifyEntries    = 10000
)

// VerifyResult contains the results of bundle verification.
type VerifyResult struct {
	// Valid is true if all checks passed.
	Valid bool `json:"valid"`

	// Encrypted reports whether AEAD authentication preceded archive verification.
	Encrypted bool `json:"encrypted"`

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
	lower = strings.TrimSuffix(lower, ".enc")
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
func Verify(bundlePath string, keys ...[]byte) (*VerifyResult, error) {
	if strings.TrimSpace(bundlePath) == "" {
		return nil, fmt.Errorf("bundle path is required")
	}
	encrypted, err := IsEncrypted(bundlePath)
	if err != nil {
		return nil, err
	}
	if !encrypted && strings.HasSuffix(strings.ToLower(bundlePath), ".enc") {
		return nil, fmt.Errorf("encrypted bundle is missing its authenticated artifact header")
	}

	format := DetectFormat(bundlePath)
	if format != FormatZip && format != FormatTarGz {
		return nil, fmt.Errorf("unsupported bundle format for %q (expected .zip, .tar.gz, or .tgz, optionally .enc)", bundlePath)
	}
	var entries map[string][]byte
	if encrypted {
		var plaintext []byte
		plaintext, err = decryptArchive(bundlePath, keys)
		if err == nil {
			entries, err = readArchiveEntries(plaintext, format)
		}
	} else {
		switch format {
		case FormatZip:
			entries, err = readZipEntries(bundlePath)
		case FormatTarGz:
			entries, err = readTarGzEntries(bundlePath)
		}
	}
	if err != nil {
		return nil, err
	}

	res := verifyEntries(entries, format)
	res.Encrypted = encrypted
	res.Details["encrypted"] = strconv.FormatBool(encrypted)
	return res, nil
}

// IsEncrypted detects the authenticated artifact envelope without loading the
// archive. Callers can defer key resolution until an encrypted input needs it.
func IsEncrypted(bundlePath string) (bool, error) {
	header, err := readFilePrefix(bundlePath, len(encryption.ArtifactEnvelopeMagic))
	if err != nil {
		return false, fmt.Errorf("read bundle header: %w", err)
	}
	return encryption.IsEncryptedArtifact(header), nil
}

// Decrypt verifies an encrypted bundle's AEAD tag, manifest and checksums before
// atomically exporting its plaintext archive. The caller must explicitly request
// this operation; a failed verification never publishes plaintext.
func Decrypt(bundlePath, outputPath string, keys ...[]byte) (*VerifyResult, error) {
	plaintext, err := decryptArchive(bundlePath, keys)
	if err != nil {
		return nil, err
	}
	format := DetectFormat(bundlePath)
	if DetectFormat(outputPath) != format || strings.HasSuffix(strings.ToLower(outputPath), ".enc") {
		return nil, fmt.Errorf("decrypted output must have the %s archive extension", format.Extension())
	}
	entries, err := readArchiveEntries(plaintext, format)
	if err != nil {
		return nil, err
	}
	res := verifyEntries(entries, format)
	res.Encrypted = true
	res.Details["encrypted"] = "true"
	if !res.Valid {
		return res, fmt.Errorf("bundle verification failed: %s", strings.Join(res.Errors, "; "))
	}
	if _, err := WriteArchive(outputPath, plaintext, nil, 0); err != nil {
		return nil, err
	}
	return res, nil
}

func decryptArchive(bundlePath string, keys [][]byte) ([]byte, error) {
	if len(keys) == 0 {
		if encCfg := ActiveEncryptionConfig(); encCfg != nil {
			keys = encCfg.DecryptKeys
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("bundle %q is encrypted (NTMENC1), but no decryption keys are configured", bundlePath)
	}
	f, err := os.Open(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("open encrypted bundle: %w", err)
	}
	defer func() { _ = f.Close() }()
	// AEAD needs the whole ciphertext. Bound that allocation independently of
	// individual expanded ZIP/tar entries (the default generation limit is 50MB).
	data, err := io.ReadAll(io.LimitReader(f, maxVerifyEntryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read encrypted bundle: %w", err)
	}
	if len(data) > maxVerifyEntryBytes {
		return nil, fmt.Errorf("encrypted bundle exceeds max verify size (%d bytes)", maxVerifyEntryBytes)
	}
	plaintext, err := encryption.DecryptArtifactWithKeyring(keys, data)
	if err != nil {
		return nil, fmt.Errorf("decrypt support bundle: %w", err)
	}
	return plaintext, nil
}

func readArchiveEntries(data []byte, format Format) (map[string][]byte, error) {
	switch format {
	case FormatZip:
		return readZipEntriesFromBytes(data)
	case FormatTarGz:
		return readTarGzEntriesFromReader(bytes.NewReader(data))
	default:
		return nil, fmt.Errorf("unsupported bundle format %q", format)
	}
}

func verifyEntries(entries map[string][]byte, format Format) *VerifyResult {
	res := &VerifyResult{
		Details: map[string]string{
			"format": string(format),
		},
	}

	manifestRaw, ok := entries[ManifestFileName]
	if !ok {
		res.Errors = append(res.Errors, fmt.Sprintf("missing %s in archive", ManifestFileName))
		return res
	}

	var manifest Manifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("parse %s: %v", ManifestFileName, err))
		return res
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
	return res
}

func readFilePrefix(bundlePath string, n int) ([]byte, error) {
	f, err := os.Open(bundlePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, n)
	readN, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	return buf[:readN], nil
}

func readZipEntries(bundlePath string) (map[string][]byte, error) {
	zr, err := zip.OpenReader(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("open zip bundle: %w", err)
	}
	defer func() { _ = zr.Close() }()
	return extractZipEntries(&zr.Reader)
}

func readZipEntriesFromBytes(data []byte) (map[string][]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open decrypted zip bundle: %w", err)
	}
	return extractZipEntries(zr)
}

func extractZipEntries(zr *zip.Reader) (map[string][]byte, error) {
	return extractZipEntriesWithLimits(zr, maxVerifyEntryBytes, maxVerifyTotalBytes, maxVerifyEntries)
}

func extractZipEntriesWithLimits(zr *zip.Reader, maxEntryBytes, maxTotalBytes int64, maxEntries int) (map[string][]byte, error) {
	entries := make(map[string][]byte, min(len(zr.File), maxEntries))
	var entryCount int
	var totalBytes int64
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		entryCount++
		if entryCount > maxEntries {
			return nil, fmt.Errorf("zip bundle exceeds max verify entries (%d)", maxEntries)
		}
		cleanName, err := SanitizeArchivePath(f.Name)
		if err != nil {
			return nil, fmt.Errorf("unsafe zip entry %q: %w", f.Name, err)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open zip entry %q: %w", f.Name, err)
		}
		remaining := maxTotalBytes - totalBytes
		if remaining < 0 {
			remaining = 0
		}
		limit := maxEntryBytes
		if remaining < limit {
			limit = remaining
		}
		data, readErr := io.ReadAll(io.LimitReader(rc, limit+1))
		_ = rc.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read zip entry %q: %w", f.Name, readErr)
		}
		if int64(len(data)) > maxEntryBytes {
			return nil, fmt.Errorf("zip entry %q exceeds max verify size (%d bytes)", f.Name, maxEntryBytes)
		}
		if totalBytes+int64(len(data)) > maxTotalBytes {
			return nil, fmt.Errorf("zip bundle exceeds max total verify size (%d bytes)", maxTotalBytes)
		}
		totalBytes += int64(len(data))
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
	return readTarGzEntriesFromReader(f)
}

func readTarGzEntriesFromReader(r io.Reader) (map[string][]byte, error) {
	return readTarGzEntriesFromReaderWithLimits(r, maxVerifyEntryBytes, maxVerifyTotalBytes, maxVerifyEntries)
}

func readTarGzEntriesFromReaderWithLimits(r io.Reader, maxEntryBytes, maxTotalBytes int64, maxEntries int) (map[string][]byte, error) {
	gr, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("open gzip stream: %w", err)
	}
	defer func() { _ = gr.Close() }()

	tr := tar.NewReader(gr)
	entries := make(map[string][]byte)
	var entryCount int
	var totalBytes int64
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
		entryCount++
		if entryCount > maxEntries {
			return nil, fmt.Errorf("tar.gz bundle exceeds max verify entries (%d)", maxEntries)
		}
		cleanName, err := SanitizeArchivePath(hdr.Name)
		if err != nil {
			return nil, fmt.Errorf("unsafe tar entry %q: %w", hdr.Name, err)
		}
		remaining := maxTotalBytes - totalBytes
		if remaining < 0 {
			remaining = 0
		}
		limit := maxEntryBytes
		if remaining < limit {
			limit = remaining
		}
		data, err := io.ReadAll(io.LimitReader(tr, limit+1))
		if err != nil {
			return nil, fmt.Errorf("read tar entry %q: %w", hdr.Name, err)
		}
		if int64(len(data)) > maxEntryBytes {
			return nil, fmt.Errorf("tar entry %q exceeds max verify size (%d bytes)", hdr.Name, maxEntryBytes)
		}
		if totalBytes+int64(len(data)) > maxTotalBytes {
			return nil, fmt.Errorf("tar.gz bundle exceeds max total verify size (%d bytes)", maxTotalBytes)
		}
		totalBytes += int64(len(data))
		entries[cleanName] = data
	}
	return entries, nil
}
