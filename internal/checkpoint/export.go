package checkpoint

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

// ExportFormat specifies the archive format for export.
type ExportFormat string

const (
	FormatTarGz ExportFormat = "tar.gz"
	FormatZip   ExportFormat = "zip"
)

const errImportArchiveTooLarge = "archive contents too large"

var (
	maxImportEntrySize    int64 = 100 << 20
	maxImportArchiveBytes int64 = 1 << 30
)

// ExportOptions configures checkpoint export.
type ExportOptions struct {
	// Format specifies the archive format (default: tar.gz)
	Format ExportFormat
	// RedactSecrets removes potential secrets from scrollback
	RedactSecrets bool
	// RewritePaths makes paths portable across machines
	RewritePaths bool
	// IncludeScrollback includes scrollback files in export
	IncludeScrollback bool
	// IncludeGitPatch includes git patch file in export
	IncludeGitPatch bool
}

// DefaultExportOptions returns sensible defaults for export.
func DefaultExportOptions() ExportOptions {
	return ExportOptions{
		Format:            FormatTarGz,
		RedactSecrets:     false,
		RewritePaths:      true,
		IncludeScrollback: true,
		IncludeGitPatch:   true,
	}
}

// ExportManifest contains metadata about an exported checkpoint.
type ExportManifest struct {
	Version        int               `json:"version"`
	ExportedAt     time.Time         `json:"exported_at"`
	SessionName    string            `json:"session_name"`
	CheckpointID   string            `json:"checkpoint_id"`
	CheckpointName string            `json:"checkpoint_name"`
	OriginalPath   string            `json:"original_path"`
	Files          []ManifestEntry   `json:"files"`
	Checksums      map[string]string `json:"checksums"`
}

// ManifestEntry describes a file in the export.
type ManifestEntry struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Checksum string `json:"checksum"`
}

// ImportOptions configures checkpoint import.
type ImportOptions struct {
	// TargetSession overrides the session name on import
	TargetSession string
	// TargetDir overrides the working directory on import
	TargetDir string
	// VerifyChecksums validates file integrity on import
	VerifyChecksums bool
	// AllowOverwrite permits overwriting existing checkpoints
	AllowOverwrite bool
}

// DefaultImportOptions returns sensible defaults for import.
func DefaultImportOptions() ImportOptions {
	return ImportOptions{
		VerifyChecksums: true,
		AllowOverwrite:  false,
	}
}

var (
	redactionConfig *redaction.Config
	redactionMu     sync.RWMutex
)

// SetRedactionConfig sets the global redaction config for checkpoint export redaction.
// Pass nil to use the default redaction config.
func SetRedactionConfig(cfg *redaction.Config) {
	redactionMu.Lock()
	defer redactionMu.Unlock()
	if cfg != nil {
		// bd-pmdpn: deep-copy reference-typed fields so a caller
		// mutating cfg after Set cannot reach into stored state.
		c := cfg.DeepCopy()
		redactionConfig = &c
	} else {
		redactionConfig = nil
	}
}

// GetRedactionConfig returns the current redaction config (or nil if unset).
// Returned value is independent of the stored config — mutating its
// reference-typed fields does not leak into future Get/Set calls.
func GetRedactionConfig() *redaction.Config {
	redactionMu.RLock()
	defer redactionMu.RUnlock()
	if redactionConfig == nil {
		return nil
	}
	c := redactionConfig.DeepCopy()
	return &c
}

// Export creates a portable archive of a checkpoint.
func (s *Storage) Export(sessionName, checkpointID string, destPath string, opts ExportOptions) (*ExportManifest, error) {
	if opts.Format == "" {
		opts.Format = FormatTarGz
	}

	// Load the checkpoint
	cp, err := s.Load(sessionName, checkpointID)
	if err != nil {
		return nil, fmt.Errorf("failed to load checkpoint: %w", err)
	}

	// Build the checkpoint directory path
	cpDir, err := s.safeCheckpointDir(sessionName, checkpointID)
	if err != nil {
		return nil, err
	}

	// Determine output path
	if destPath == "" {
		ext := ".tar.gz"
		if opts.Format == FormatZip {
			ext = ".zip"
		}
		destPath = fmt.Sprintf("%s_%s%s", sessionName, checkpointID, ext)
	}

	// Collect files to export
	var files []string
	seenFiles := make(map[string]struct{})
	addFile := func(file string) {
		if file == "" {
			return
		}
		if _, ok := seenFiles[file]; ok {
			return
		}
		seenFiles[file] = struct{}{}
		files = append(files, file)
	}
	addFile(MetadataFile)
	addFile(SessionFile)

	if opts.IncludeScrollback {
		for _, pane := range cp.Session.Panes {
			addFile(pane.ScrollbackFile)
		}
	}

	if opts.IncludeGitPatch {
		addFile(cp.Git.PatchFile)
	}
	addFile(cp.Git.StatusFile)

	// Create manifest
	manifest := &ExportManifest{
		Version:        1,
		ExportedAt:     time.Now(),
		SessionName:    sessionName,
		CheckpointID:   cp.ID,
		CheckpointName: cp.Name,
		OriginalPath:   cp.WorkingDir,
		Checksums:      make(map[string]string),
	}

	// Prepare checkpoint data (potentially with path rewriting)
	cpData := rewriteCheckpointForExport(cp, opts)
	redactedScrollbackFiles, err := prepareRedactedScrollbackArtifacts(cpDir, cpData, opts)
	if err != nil {
		return nil, err
	}

	// Create the archive
	switch opts.Format {
	case FormatTarGz:
		err = s.exportTarGz(destPath, cpDir, cpData, files, opts, manifest, redactedScrollbackFiles)
	case FormatZip:
		err = s.exportZip(destPath, cpDir, cpData, files, opts, manifest, redactedScrollbackFiles)
	default:
		return nil, fmt.Errorf("unsupported export format: %s", opts.Format)
	}

	if err != nil {
		return nil, err
	}

	return manifest, nil
}

func (s *Storage) exportTarGz(destPath, cpDir string, cp *Checkpoint, files []string, opts ExportOptions, manifest *ExportManifest, preparedFiles map[string][]byte) (err error) {
	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create export file: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("closing export file: %w", closeErr)
		}
	}()

	gw := gzip.NewWriter(f)
	defer func() {
		if closeErr := gw.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("closing gzip export stream: %w", closeErr)
		}
	}()

	tw := tar.NewWriter(gw)
	defer func() {
		if closeErr := tw.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("closing tar export stream: %w", closeErr)
		}
	}()

	// Write metadata.json
	cpJSON, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal checkpoint: %w", err)
	}

	checksum := sha256sum(cpJSON)
	manifest.Checksums[MetadataFile] = checksum
	manifest.Files = append(manifest.Files, ManifestEntry{
		Path:     MetadataFile,
		Size:     int64(len(cpJSON)),
		Checksum: checksum,
	})

	if err := writeTarEntry(tw, MetadataFile, cpJSON); err != nil {
		return err
	}

	sessionJSON, err := json.MarshalIndent(cp.Session, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session state: %w", err)
	}

	checksum = sha256sum(sessionJSON)
	manifest.Checksums[SessionFile] = checksum
	manifest.Files = append(manifest.Files, ManifestEntry{
		Path:     SessionFile,
		Size:     int64(len(sessionJSON)),
		Checksum: checksum,
	})

	if err := writeTarEntry(tw, SessionFile, sessionJSON); err != nil {
		return err
	}

	// Write other files
	for _, file := range files {
		if file == MetadataFile || file == SessionFile {
			continue
		}

		data, prepared := preparedFiles[file]
		if !prepared {
			srcPath, err := resolveExistingCheckpointArtifactPath(cpDir, file)
			if err != nil {
				return fmt.Errorf("invalid checkpoint file path %s: %w", file, err)
			}
			data, err = os.ReadFile(srcPath)
			if err != nil {
				return fmt.Errorf("failed to read checkpoint file %s: %w", file, err)
			}
		}

		checksum := sha256sum(data)
		manifest.Checksums[file] = checksum
		manifest.Files = append(manifest.Files, ManifestEntry{
			Path:     file,
			Size:     int64(len(data)),
			Checksum: checksum,
		})

		if err := writeTarEntry(tw, file, data); err != nil {
			return err
		}
	}

	// Write manifest
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal manifest: %w", err)
	}
	if err := writeTarEntry(tw, "MANIFEST.json", manifestJSON); err != nil {
		return err
	}

	return nil
}

func (s *Storage) exportZip(destPath, cpDir string, cp *Checkpoint, files []string, opts ExportOptions, manifest *ExportManifest, preparedFiles map[string][]byte) (err error) {
	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create export file: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("closing export file: %w", closeErr)
		}
	}()

	zw := zip.NewWriter(f)
	defer func() {
		if closeErr := zw.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("closing zip export stream: %w", closeErr)
		}
	}()

	// Write metadata.json
	cpJSON, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal checkpoint: %w", err)
	}

	checksum := sha256sum(cpJSON)
	manifest.Checksums[MetadataFile] = checksum
	manifest.Files = append(manifest.Files, ManifestEntry{
		Path:     MetadataFile,
		Size:     int64(len(cpJSON)),
		Checksum: checksum,
	})

	if err := writeZipEntry(zw, MetadataFile, cpJSON); err != nil {
		return err
	}

	sessionJSON, err := json.MarshalIndent(cp.Session, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session state: %w", err)
	}

	checksum = sha256sum(sessionJSON)
	manifest.Checksums[SessionFile] = checksum
	manifest.Files = append(manifest.Files, ManifestEntry{
		Path:     SessionFile,
		Size:     int64(len(sessionJSON)),
		Checksum: checksum,
	})

	if err := writeZipEntry(zw, SessionFile, sessionJSON); err != nil {
		return err
	}

	// Write other files
	for _, file := range files {
		if file == MetadataFile || file == SessionFile {
			continue
		}

		data, prepared := preparedFiles[file]
		if !prepared {
			srcPath, err := resolveExistingCheckpointArtifactPath(cpDir, file)
			if err != nil {
				return fmt.Errorf("invalid checkpoint file path %s: %w", file, err)
			}
			data, err = os.ReadFile(srcPath)
			if err != nil {
				return fmt.Errorf("failed to read checkpoint file %s: %w", file, err)
			}
		}

		checksum := sha256sum(data)
		manifest.Checksums[file] = checksum
		manifest.Files = append(manifest.Files, ManifestEntry{
			Path:     file,
			Size:     int64(len(data)),
			Checksum: checksum,
		})

		if err := writeZipEntry(zw, file, data); err != nil {
			return err
		}
	}

	// Write manifest
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal manifest: %w", err)
	}
	if err := writeZipEntry(zw, "MANIFEST.json", manifestJSON); err != nil {
		return err
	}

	return nil
}

// Import loads a checkpoint from an exported archive.
func (s *Storage) Import(archivePath string, opts ImportOptions) (*Checkpoint, error) {
	var format ExportFormat
	switch {
	case strings.HasSuffix(archivePath, ".tar.gz") || strings.HasSuffix(archivePath, ".tgz"):
		format = FormatTarGz
	case strings.HasSuffix(archivePath, ".zip"):
		format = FormatZip
	default:
		return nil, fmt.Errorf("unknown archive format: %s", filepath.Ext(archivePath))
	}

	switch format {
	case FormatTarGz:
		return s.importTarGz(archivePath, opts)
	case FormatZip:
		return s.importZip(archivePath, opts)
	default:
		return nil, fmt.Errorf("unsupported import format: %s", format)
	}
}

func (s *Storage) importTarGz(archivePath string, opts ImportOptions) (result *Checkpoint, err error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open archive: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			result = nil
			err = fmt.Errorf("closing archive file: %w", closeErr)
		}
	}()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer func() {
		if closeErr := gr.Close(); err == nil && closeErr != nil {
			result = nil
			err = fmt.Errorf("closing gzip archive reader: %w", closeErr)
		}
	}()

	tr := tar.NewReader(gr)

	var manifest *ExportManifest
	var cp *Checkpoint
	fileContents := make(map[string][]byte)
	var totalBytes int64

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read tar entry: %w", err)
		}
		skipEntry, err := validateTarImportEntry(header)
		if err != nil {
			return nil, err
		}
		if skipEntry {
			continue
		}
		if _, exists := fileContents[header.Name]; exists {
			return nil, fmt.Errorf("archive contains duplicate entry: %s", header.Name)
		}

		data, err := readImportEntryLimited(tr, header.Name, maxImportEntrySize)
		if err != nil {
			return nil, err
		}
		if err := storeImportEntry(fileContents, &totalBytes, header.Name, data); err != nil {
			return nil, err
		}

		switch header.Name {
		case "MANIFEST.json":
			manifest = &ExportManifest{}
			if err := json.Unmarshal(data, manifest); err != nil {
				return nil, fmt.Errorf("failed to parse manifest: %w", err)
			}
		case MetadataFile:
			cp = &Checkpoint{}
			if err := json.Unmarshal(data, cp); err != nil {
				return nil, fmt.Errorf("failed to parse checkpoint: %w", err)
			}
		}
	}

	if cp == nil {
		return nil, fmt.Errorf("archive missing %s", MetadataFile)
	}

	// Verify checksums if requested
	if opts.VerifyChecksums {
		if err := verifyImportChecksums(fileContents, manifest); err != nil {
			return nil, err
		}
	}
	if err := validateImportedSessionState(fileContents, cp); err != nil {
		return nil, err
	}
	if err := validateImportedManifestMetadata(manifest, cp); err != nil {
		return nil, err
	}
	if err := validateImportedArchiveFiles(fileContents, cp); err != nil {
		return nil, err
	}

	sessionName := cp.SessionName

	// Apply overrides
	if opts.TargetSession != "" {
		sessionName = opts.TargetSession
	}
	cp.SessionName = sessionName

	// Apply TargetDir override or expand ${WORKING_DIR} placeholder
	if opts.TargetDir != "" {
		cp.WorkingDir = opts.TargetDir
	} else if cp.WorkingDir == "${WORKING_DIR}" {
		// No explicit target dir and checkpoint was exported with path rewriting
		// Use current working directory as default
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("failed to get current directory for path expansion: %w", err)
		}
		cp.WorkingDir = cwd
	}

	cpJSON, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal imported checkpoint: %w", err)
	}
	fileContents[MetadataFile] = cpJSON

	sessionJSON, err := json.MarshalIndent(cp.Session, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal imported session state: %w", err)
	}
	fileContents[SessionFile] = sessionJSON

	// Check for existing checkpoint
	cpDir, err := s.safeCheckpointDir(sessionName, cp.ID)
	if err != nil {
		return nil, fmt.Errorf("invalid imported checkpoint metadata: %w", err)
	}
	if _, err := os.Stat(cpDir); err == nil && !opts.AllowOverwrite {
		return nil, fmt.Errorf("checkpoint %s already exists (use AllowOverwrite to replace)", cp.ID)
	}
	if opts.AllowOverwrite {
		if err := validateImportOverwrite(cpDir, fileContents); err != nil {
			return nil, err
		}
	}

	// Create checkpoint directory
	if err := os.MkdirAll(cpDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create checkpoint directory: %w", err)
	}

	// Write all files
	for name, data := range fileContents {
		if name == "MANIFEST.json" {
			continue
		}

		// Validate path doesn't escape checkpoint directory (path traversal protection)
		// First pass: textual validation before creating directories
		if !isPathWithinDir(cpDir, name) {
			return nil, fmt.Errorf("invalid path in archive (path traversal attempt): %s", name)
		}

		destPath := filepath.Join(cpDir, name)
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory for %s: %w", name, err)
		}

		// Second pass: symlink-safe validation after directories are created (TOCTOU protection)
		resolvedPath, err := isPathWithinDirResolved(cpDir, name)
		if err != nil {
			return nil, fmt.Errorf("invalid path in archive (symlink escape): %s", name)
		}

		if err := util.AtomicWriteFile(resolvedPath, data, 0600); err != nil {
			return nil, fmt.Errorf("failed to write %s: %w", name, err)
		}
	}

	return cp, nil
}

func (s *Storage) importZip(archivePath string, opts ImportOptions) (result *Checkpoint, err error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open zip archive: %w", err)
	}
	defer func() {
		if closeErr := zr.Close(); err == nil && closeErr != nil {
			result = nil
			err = fmt.Errorf("closing zip archive: %w", closeErr)
		}
	}()

	var manifest *ExportManifest
	var cp *Checkpoint
	fileContents := make(map[string][]byte)
	var totalBytes int64

	for _, f := range zr.File {
		skipEntry, err := validateZipImportEntry(f)
		if err != nil {
			return nil, err
		}
		if skipEntry {
			continue
		}
		if _, exists := fileContents[f.Name]; exists {
			return nil, fmt.Errorf("archive contains duplicate entry: %s", f.Name)
		}

		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("failed to open %s: %w", f.Name, err)
		}

		data, readErr := readImportEntryLimited(rc, f.Name, maxImportEntrySize)
		closeErr := rc.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, fmt.Errorf("failed to close %s: %w", f.Name, closeErr)
		}
		if err := storeImportEntry(fileContents, &totalBytes, f.Name, data); err != nil {
			return nil, err
		}

		switch f.Name {
		case "MANIFEST.json":
			manifest = &ExportManifest{}
			if err := json.Unmarshal(data, manifest); err != nil {
				return nil, fmt.Errorf("failed to parse manifest: %w", err)
			}
		case MetadataFile:
			cp = &Checkpoint{}
			if err := json.Unmarshal(data, cp); err != nil {
				return nil, fmt.Errorf("failed to parse checkpoint: %w", err)
			}
		}
	}

	if cp == nil {
		return nil, fmt.Errorf("archive missing %s", MetadataFile)
	}

	// Verify checksums
	if opts.VerifyChecksums {
		if err := verifyImportChecksums(fileContents, manifest); err != nil {
			return nil, err
		}
	}
	if err := validateImportedSessionState(fileContents, cp); err != nil {
		return nil, err
	}
	if err := validateImportedManifestMetadata(manifest, cp); err != nil {
		return nil, err
	}
	if err := validateImportedArchiveFiles(fileContents, cp); err != nil {
		return nil, err
	}

	sessionName := cp.SessionName

	// Apply overrides
	if opts.TargetSession != "" {
		sessionName = opts.TargetSession
	}
	cp.SessionName = sessionName

	// Apply TargetDir override or expand ${WORKING_DIR} placeholder
	if opts.TargetDir != "" {
		cp.WorkingDir = opts.TargetDir
	} else if cp.WorkingDir == "${WORKING_DIR}" {
		// No explicit target dir and checkpoint was exported with path rewriting
		// Use current working directory as default
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("failed to get current directory for path expansion: %w", err)
		}
		cp.WorkingDir = cwd
	}

	cpJSON, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal imported checkpoint: %w", err)
	}
	fileContents[MetadataFile] = cpJSON

	sessionJSON, err := json.MarshalIndent(cp.Session, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal imported session state: %w", err)
	}
	fileContents[SessionFile] = sessionJSON

	// Check for existing
	cpDir, err := s.safeCheckpointDir(sessionName, cp.ID)
	if err != nil {
		return nil, fmt.Errorf("invalid imported checkpoint metadata: %w", err)
	}
	if _, err := os.Stat(cpDir); err == nil && !opts.AllowOverwrite {
		return nil, fmt.Errorf("checkpoint %s already exists", cp.ID)
	}
	if opts.AllowOverwrite {
		if err := validateImportOverwrite(cpDir, fileContents); err != nil {
			return nil, err
		}
	}

	// Create checkpoint directory
	if err := os.MkdirAll(cpDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create checkpoint directory: %w", err)
	}

	// Write all files
	for name, data := range fileContents {
		if name == "MANIFEST.json" {
			continue
		}

		// Validate path doesn't escape checkpoint directory (path traversal protection)
		// First pass: textual validation before creating directories
		if !isPathWithinDir(cpDir, name) {
			return nil, fmt.Errorf("invalid path in archive (path traversal attempt): %s", name)
		}

		destPath := filepath.Join(cpDir, name)
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory for %s: %w", name, err)
		}

		// Second pass: symlink-safe validation after directories are created (TOCTOU protection)
		resolvedPath, err := isPathWithinDirResolved(cpDir, name)
		if err != nil {
			return nil, fmt.Errorf("invalid path in archive (symlink escape): %s", name)
		}

		if err := util.AtomicWriteFile(resolvedPath, data, 0600); err != nil {
			return nil, fmt.Errorf("failed to write %s: %w", name, err)
		}
	}

	return cp, nil
}

// Helper functions

func writeTarEntry(tw *tar.Writer, name string, data []byte) error {
	header := &tar.Header{
		Name:    name,
		Mode:    0644,
		Size:    int64(len(data)),
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(header); err != nil {
		return fmt.Errorf("failed to write tar header for %s: %w", name, err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("failed to write tar content for %s: %w", name, err)
	}
	return nil
}

func writeZipEntry(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("failed to create zip entry for %s: %w", name, err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("failed to write zip content for %s: %w", name, err)
	}
	return nil
}

func validateTarImportEntry(header *tar.Header) (bool, error) {
	if header == nil {
		return false, fmt.Errorf("archive contains nil tar header")
	}
	switch header.Typeflag {
	case tar.TypeReg, tar.TypeRegA:
		if err := validateImportEntryName(header.Name); err != nil {
			return false, err
		}
		return false, nil
	case tar.TypeDir:
		if err := validateImportDirectoryEntryName(header.Name); err != nil {
			return false, err
		}
		return true, nil
	default:
		return false, fmt.Errorf("archive contains non-regular entry: %s", header.Name)
	}
}

func validateZipImportEntry(f *zip.File) (bool, error) {
	if f == nil {
		return false, fmt.Errorf("archive contains nil zip entry")
	}
	info := f.FileInfo()
	if info.IsDir() {
		if err := validateImportDirectoryEntryName(f.Name); err != nil {
			return false, err
		}
		return true, nil
	}
	if err := validateImportEntryName(f.Name); err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("archive contains non-regular entry: %s", f.Name)
	}
	return false, nil
}

func validateImportDirectoryEntryName(name string) error {
	return validateImportEntryName(strings.TrimSuffix(name, "/"))
}

func validateImportEntryName(name string) error {
	if name == "" {
		return fmt.Errorf("archive contains empty entry name")
	}
	if strings.Contains(name, `\`) {
		return fmt.Errorf("invalid path in archive (backslash path separator): %s", name)
	}
	for _, segment := range strings.Split(name, "/") {
		if strings.Contains(segment, ":") {
			return fmt.Errorf("invalid path in archive (colon path segment): %s", name)
		}
	}
	if filepath.IsAbs(name) {
		return fmt.Errorf("invalid path in archive (absolute path): %s", name)
	}
	cleanName := path.Clean(name)
	if cleanName == "." || cleanName != name {
		return fmt.Errorf("invalid path in archive (non-canonical path): %s", name)
	}
	if !isPathWithinDir(".", name) {
		return fmt.Errorf("invalid path in archive (path traversal attempt): %s", name)
	}
	return nil
}

func storeImportEntry(fileContents map[string][]byte, totalBytes *int64, name string, data []byte) error {
	nextTotal := *totalBytes + int64(len(data))
	if nextTotal > maxImportArchiveBytes {
		return fmt.Errorf("%s: exceeds %d bytes", errImportArchiveTooLarge, maxImportArchiveBytes)
	}
	fileContents[name] = data
	*totalBytes = nextTotal
	return nil
}

func readImportEntryLimited(r io.Reader, name string, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("invalid import size limit for %s: %d", name, limit)
	}

	reader := &io.LimitedReader{R: r, N: limit + 1}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", name, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("archive entry too large: %s exceeds %d bytes", name, limit)
	}
	return data, nil
}

func sha256sum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

type redactedScrollbackArtifact struct {
	data        []byte
	raw         []byte
	compacted   bool
	compression string
}

func prepareRedactedScrollbackArtifacts(cpDir string, cp *Checkpoint, opts ExportOptions) (map[string][]byte, error) {
	if !opts.RedactSecrets || !opts.IncludeScrollback {
		return nil, nil
	}

	prepared := make(map[string][]byte)
	for i := range cp.Session.Panes {
		pane := &cp.Session.Panes[i]
		if pane.ScrollbackFile == "" {
			continue
		}

		srcPath, err := resolveExistingCheckpointArtifactPath(cpDir, pane.ScrollbackFile)
		if err != nil {
			return nil, fmt.Errorf("invalid checkpoint file path %s: %w", pane.ScrollbackFile, err)
		}
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read checkpoint file %s: %w", pane.ScrollbackFile, err)
		}
		artifact, err := redactScrollbackArtifact(pane.ScrollbackFile, data)
		if err != nil {
			return nil, fmt.Errorf("redacting scrollback file %s: %w", pane.ScrollbackFile, err)
		}

		prepared[pane.ScrollbackFile] = artifact.data
		if pane.Scrollback != nil {
			summary := *pane.Scrollback
			summary.Captured = true
			summary.ArtifactPreserved = true
			summary.Compacted = artifact.compacted
			summary.Compression = artifact.compression
			summary.LineCount = countLines(string(artifact.raw))
			summary.RawBytes = len(artifact.raw)
			summary.StoredBytes = int64(len(artifact.data))
			pane.Scrollback = &summary
		}
		pane.ScrollbackLines = countLines(string(artifact.raw))
	}

	return prepared, nil
}

func redactSecrets(data []byte) []byte {
	redactionMu.RLock()
	cfg := redactionConfig
	redactionMu.RUnlock()

	var effective redaction.Config
	if cfg == nil {
		effective = redaction.DefaultConfig()
	} else {
		effective = *cfg
	}
	// Export redaction is explicitly requested by flag; always redact.
	effective.Mode = redaction.ModeRedact

	result := redaction.ScanAndRedact(string(data), effective)
	return []byte(result.Output)
}

func redactScrollbackArtifact(path string, data []byte) (redactedScrollbackArtifact, error) {
	if filepath.Ext(path) != ".gz" {
		redacted := redactSecrets(data)
		return redactedScrollbackArtifact{
			data: redacted,
			raw:  redacted,
		}, nil
	}

	decompressed, err := gzipDecompress(data)
	if err != nil {
		return redactedScrollbackArtifact{}, fmt.Errorf("decompressing compressed scrollback: %w", err)
	}
	redacted := redactSecrets(decompressed)
	recompressed, err := gzipCompress(redacted)
	if err != nil {
		return redactedScrollbackArtifact{}, fmt.Errorf("recompressing redacted scrollback: %w", err)
	}
	return redactedScrollbackArtifact{
		data:        recompressed,
		raw:         redacted,
		compacted:   true,
		compression: scrollbackCompressionGzip,
	}, nil
}

func rewriteCheckpointForExport(cp *Checkpoint, opts ExportOptions) *Checkpoint {
	result := *cp
	result.Session.WindowLayouts = cloneWindowLayouts(cp.Session.WindowLayouts)
	result.Session.Panes = clonePaneStatesForExport(cp.Session.Panes)
	if opts.RewritePaths && result.WorkingDir != "" {
		result.WorkingDir = "${WORKING_DIR}"
	}
	if !opts.IncludeScrollback && result.Session.Panes != nil {
		result.Session.Panes = make([]PaneState, len(cp.Session.Panes))
		copy(result.Session.Panes, cp.Session.Panes)
		for i := range result.Session.Panes {
			result.Session.Panes[i].ScrollbackFile = ""
			result.Session.Panes[i].ScrollbackLines = 0
			result.Session.Panes[i].Scrollback = nil
		}
	}
	if !opts.IncludeGitPatch {
		result.Git.PatchFile = ""
	}
	return &result
}

func clonePaneStatesForExport(panes []PaneState) []PaneState {
	if panes == nil {
		return nil
	}
	cloned := make([]PaneState, len(panes))
	copy(cloned, panes)
	for i := range cloned {
		if cloned[i].Scrollback == nil {
			continue
		}
		summary := *cloned[i].Scrollback
		cloned[i].Scrollback = &summary
	}
	return cloned
}

func validateImportOverwrite(cpDir string, fileContents map[string][]byte) error {
	if _, err := os.Stat(cpDir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to inspect existing checkpoint directory: %w", err)
	}

	incomingFiles := make(map[string]struct{}, len(fileContents))
	for name := range fileContents {
		if name == "MANIFEST.json" {
			continue
		}
		incomingFiles[name] = struct{}{}
	}

	var staleFiles []string
	err := filepath.WalkDir(cpDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == cpDir || d.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(cpDir, path)
		if err != nil {
			return err
		}
		relPath = filepath.ToSlash(relPath)
		if _, ok := incomingFiles[relPath]; !ok {
			staleFiles = append(staleFiles, relPath)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to inspect existing checkpoint directory: %w", err)
	}

	if len(staleFiles) == 0 {
		return nil
	}
	if len(staleFiles) == 1 {
		return fmt.Errorf("overwrite would leave stale checkpoint artifact behind: %s", staleFiles[0])
	}
	return fmt.Errorf("overwrite would leave stale checkpoint artifacts behind: %s", strings.Join(staleFiles, ", "))
}

func verifyImportChecksums(fileContents map[string][]byte, manifest *ExportManifest) error {
	if manifest == nil {
		return fmt.Errorf("checksum verification requested but archive missing MANIFEST.json")
	}
	if err := validateImportManifestEntries(fileContents, manifest); err != nil {
		return err
	}

	for file, data := range fileContents {
		if file == "MANIFEST.json" {
			continue
		}
		expectedSum, ok := manifest.Checksums[file]
		if !ok {
			return fmt.Errorf("manifest missing checksum for %s", file)
		}
		actualSum := sha256sum(data)
		if actualSum != expectedSum {
			return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", file, expectedSum, actualSum)
		}
	}

	for file := range manifest.Checksums {
		if file == "MANIFEST.json" {
			continue
		}
		if _, ok := fileContents[file]; !ok {
			return fmt.Errorf("manifest lists missing file: %s", file)
		}
	}

	return nil
}

func validateImportManifestEntries(fileContents map[string][]byte, manifest *ExportManifest) error {
	if len(manifest.Files) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(manifest.Files))
	for _, entry := range manifest.Files {
		if entry.Path == "MANIFEST.json" {
			return fmt.Errorf("manifest must not list MANIFEST.json")
		}
		if err := validateImportEntryName(entry.Path); err != nil {
			return fmt.Errorf("invalid manifest entry path %q: %w", entry.Path, err)
		}
		if _, ok := seen[entry.Path]; ok {
			return fmt.Errorf("manifest contains duplicate file entry: %s", entry.Path)
		}
		seen[entry.Path] = struct{}{}

		expectedSum, ok := manifest.Checksums[entry.Path]
		if !ok {
			return fmt.Errorf("manifest file entry missing checksum map entry for %s", entry.Path)
		}
		if entry.Checksum != expectedSum {
			return fmt.Errorf("manifest entry checksum mismatch for %s: files entry has %s, checksums map has %s", entry.Path, entry.Checksum, expectedSum)
		}

		data, ok := fileContents[entry.Path]
		if !ok {
			return fmt.Errorf("manifest lists missing file: %s", entry.Path)
		}
		if entry.Size != int64(len(data)) {
			return fmt.Errorf("manifest entry size mismatch for %s: expected %d, got %d", entry.Path, entry.Size, len(data))
		}
	}

	for file := range manifest.Checksums {
		if file == "MANIFEST.json" {
			continue
		}
		if _, ok := seen[file]; !ok {
			return fmt.Errorf("manifest checksums missing file entry for %s", file)
		}
	}

	return nil
}

func validateImportedSessionState(fileContents map[string][]byte, cp *Checkpoint) error {
	sessionData, ok := fileContents[SessionFile]
	if !ok {
		return fmt.Errorf("archive missing %s", SessionFile)
	}

	var session SessionState
	if err := json.Unmarshal(sessionData, &session); err != nil {
		return fmt.Errorf("failed to parse session state: %w", err)
	}

	metadataJSON, err := json.Marshal(cp.Session)
	if err != nil {
		return fmt.Errorf("failed to marshal checkpoint session state: %w", err)
	}
	sessionJSON, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("failed to marshal imported session state: %w", err)
	}
	if !bytes.Equal(metadataJSON, sessionJSON) {
		return fmt.Errorf("archive %s does not match %s session state", SessionFile, MetadataFile)
	}

	return nil
}

func validateImportedManifestMetadata(manifest *ExportManifest, cp *Checkpoint) error {
	if manifest == nil {
		return nil
	}
	if manifest.SessionName != "" && manifest.SessionName != cp.SessionName {
		return fmt.Errorf("manifest session name %q does not match %s session name %q", manifest.SessionName, MetadataFile, cp.SessionName)
	}
	if manifest.CheckpointID != "" && manifest.CheckpointID != cp.ID {
		return fmt.Errorf("manifest checkpoint id %q does not match %s checkpoint id %q", manifest.CheckpointID, MetadataFile, cp.ID)
	}
	if manifest.CheckpointName != "" && manifest.CheckpointName != cp.Name {
		return fmt.Errorf("manifest checkpoint name %q does not match %s checkpoint name %q", manifest.CheckpointName, MetadataFile, cp.Name)
	}

	return nil
}

func validateImportedArchiveFiles(fileContents map[string][]byte, cp *Checkpoint) error {
	if err := validateImportedArtifactReferences(cp); err != nil {
		return err
	}

	expectedFiles := expectedManifestFiles(cp)
	for name := range fileContents {
		if name == "MANIFEST.json" {
			continue
		}
		if !isPathWithinDir(".", name) {
			return fmt.Errorf("invalid path in archive (path traversal attempt): %s", name)
		}
		if _, ok := expectedFiles[name]; !ok {
			return fmt.Errorf("archive contains unexpected file: %s", name)
		}
	}

	for _, pane := range cp.Session.Panes {
		if pane.ScrollbackFile == "" {
			continue
		}
		if _, ok := fileContents[pane.ScrollbackFile]; !ok {
			return fmt.Errorf("archive missing scrollback referenced by metadata: %s", pane.ScrollbackFile)
		}
	}
	if cp.Git.PatchFile != "" {
		if _, ok := fileContents[cp.Git.PatchFile]; !ok {
			return fmt.Errorf("archive missing git patch referenced by metadata: %s", cp.Git.PatchFile)
		}
	}
	if cp.Git.StatusFile != "" {
		if _, ok := fileContents[cp.Git.StatusFile]; !ok {
			return fmt.Errorf("archive missing git status referenced by metadata: %s", cp.Git.StatusFile)
		}
	}

	return nil
}

func validateImportedArtifactReferences(cp *Checkpoint) error {
	for _, pane := range cp.Session.Panes {
		if pane.ScrollbackFile == "" {
			continue
		}
		if err := validateImportEntryName(pane.ScrollbackFile); err != nil {
			return fmt.Errorf("invalid scrollback path for pane %s: %w", pane.ID, err)
		}
		cleanPath := filepath.ToSlash(filepath.Clean(pane.ScrollbackFile))
		if cleanPath == PanesDir || !strings.HasPrefix(cleanPath, PanesDir+"/") {
			return fmt.Errorf("invalid scrollback path for pane %s: must be under %s/: %s", pane.ID, PanesDir, pane.ScrollbackFile)
		}
	}

	if cp.Git.PatchFile != "" && cp.Git.PatchFile != GitPatchFile {
		return fmt.Errorf("invalid git patch path: expected %s, got %s", GitPatchFile, cp.Git.PatchFile)
	}
	if cp.Git.StatusFile != "" && cp.Git.StatusFile != GitStatusFile {
		return fmt.Errorf("invalid git status path: expected %s, got %s", GitStatusFile, cp.Git.StatusFile)
	}

	return nil
}

// isPathWithinDir checks if a path (after cleaning) stays within the base directory.
// This prevents path traversal attacks like "../../../etc/passwd".
func isPathWithinDir(baseDir, targetPath string) bool {
	// Clean and make absolute
	cleanBase := filepath.Clean(baseDir)
	fullPath := targetPath
	if !filepath.IsAbs(fullPath) {
		fullPath = filepath.Join(cleanBase, targetPath)
	}
	cleanPath := filepath.Clean(fullPath)

	// The clean path must be within or equal to the base directory
	// Use filepath.Rel to check - if it requires ".." then it's outside
	rel, err := filepath.Rel(cleanBase, cleanPath)
	if err != nil {
		return false
	}

	return !pathEscapesBase(rel)
}

// isPathWithinDirResolved validates a path after resolving symlinks to prevent TOCTOU attacks.
// Returns the resolved absolute path if valid, or an error if the path escapes the base directory.
func isPathWithinDirResolved(baseDir, targetPath string) (string, error) {
	// First do textual validation (fast path)
	if !isPathWithinDir(baseDir, targetPath) {
		return "", fmt.Errorf("path escapes base directory: %s", targetPath)
	}

	// Resolve symlinks in the base directory to get canonical path
	resolvedBase, err := filepath.EvalSymlinks(baseDir)
	if err != nil {
		// If base doesn't exist yet, fall back to Clean
		resolvedBase = filepath.Clean(baseDir)
	}

	// Build the full path
	fullPath := targetPath
	if !filepath.IsAbs(fullPath) {
		fullPath = filepath.Join(resolvedBase, targetPath)
	}

	// For the target, resolve parent directories but not the final component
	// (since we're about to create it). This catches symlink attacks in intermediate dirs.
	parentDir := filepath.Dir(fullPath)

	// Try to resolve symlinks in the parent path (if it exists)
	resolvedParent, err := filepath.EvalSymlinks(parentDir)
	if err == nil {
		// Verify the resolved parent is still within base
		relParent, err := filepath.Rel(resolvedBase, resolvedParent)
		if err != nil || pathEscapesBase(relParent) {
			return "", fmt.Errorf("symlink escape detected in path: %s", targetPath)
		}
		// Reconstruct full path with resolved parent
		fullPath = filepath.Join(resolvedParent, filepath.Base(fullPath))
	}

	// Final validation: clean path must be within resolved base
	cleanPath := filepath.Clean(fullPath)
	rel, err := filepath.Rel(resolvedBase, cleanPath)
	if err != nil || pathEscapesBase(rel) {
		return "", fmt.Errorf("resolved path escapes base directory: %s", targetPath)
	}

	return cleanPath, nil
}

func pathEscapesBase(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel)
}
