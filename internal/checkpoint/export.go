package checkpoint

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ExportFormat specifies the archive format for export.
type ExportFormat string

const (
	FormatTarGz ExportFormat = "tar.gz"
	FormatZip   ExportFormat = "zip"
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

// Secret patterns to redact from scrollback.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(api[_-]?key|apikey)\s*[:=]\s*['"]?[\w-]{20,}['"]?`),
	regexp.MustCompile(`(?i)(secret|password|passwd|pwd)\s*[:=]\s*['"]?[^\s'"]{8,}['"]?`),
	regexp.MustCompile(`(?i)(token|bearer)\s*[:=]\s*['"]?[\w-]{20,}['"]?`),
	regexp.MustCompile(`(?i)Authorization:\s*Bearer\s+[\w-]+`),
	regexp.MustCompile(`(?i)(aws_secret|aws_access)\s*[:=]\s*['"]?[\w/+=]{20,}['"]?`),
	regexp.MustCompile(`ghp_[a-zA-Z0-9]{36}`),
	regexp.MustCompile(`sk-[a-zA-Z0-9]{48}`),
	regexp.MustCompile(`sk-ant-[a-zA-Z0-9-]{95}`),
	regexp.MustCompile(`AKIA[A-Z0-9]{16}`),
	regexp.MustCompile(`-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----`),
}

// Export creates a portable archive of a checkpoint.
func (s *Storage) Export(sessionName, checkpointID string, destPath string, opts ExportOptions) (*ExportManifest, error) {
	// Load the checkpoint
	cp, err := s.Load(sessionName, checkpointID)
	if err != nil {
		return nil, fmt.Errorf("failed to load checkpoint: %w", err)
	}

	// Build the checkpoint directory path
	cpDir := s.CheckpointDir(sessionName, checkpointID)

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
	files = append(files, MetadataFile)

	if opts.IncludeScrollback {
		for _, pane := range cp.Session.Panes {
			if pane.ScrollbackFile != "" {
				files = append(files, pane.ScrollbackFile)
			}
		}
	}

	if opts.IncludeGitPatch && cp.Git.PatchFile != "" {
		files = append(files, cp.Git.PatchFile)
	}

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
	cpData := cp
	if opts.RewritePaths {
		cpData = rewriteCheckpointPaths(cp)
	}

	// Create the archive
	switch opts.Format {
	case FormatTarGz:
		err = s.exportTarGz(destPath, cpDir, cpData, files, opts, manifest)
	case FormatZip:
		err = s.exportZip(destPath, cpDir, cpData, files, opts, manifest)
	default:
		return nil, fmt.Errorf("unsupported export format: %s", opts.Format)
	}

	if err != nil {
		return nil, err
	}

	return manifest, nil
}

func (s *Storage) exportTarGz(destPath, cpDir string, cp *Checkpoint, files []string, opts ExportOptions, manifest *ExportManifest) error {
	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create export file: %w", err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

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

	// Write other files
	for _, file := range files {
		if file == MetadataFile {
			continue
		}

		srcPath := filepath.Join(cpDir, file)
		data, err := os.ReadFile(srcPath)
		if err != nil {
			continue
		}

		// Redact secrets from scrollback files
		if opts.RedactSecrets && strings.HasPrefix(file, PanesDir+"/") {
			data = redactSecrets(data)
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

func (s *Storage) exportZip(destPath, cpDir string, cp *Checkpoint, files []string, opts ExportOptions, manifest *ExportManifest) error {
	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create export file: %w", err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

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

	// Write other files
	for _, file := range files {
		if file == MetadataFile {
			continue
		}

		srcPath := filepath.Join(cpDir, file)
		data, err := os.ReadFile(srcPath)
		if err != nil {
			continue
		}

		if opts.RedactSecrets && strings.HasPrefix(file, PanesDir+"/") {
			data = redactSecrets(data)
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

func (s *Storage) importTarGz(archivePath string, opts ImportOptions) (*Checkpoint, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open archive: %w", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

	var manifest *ExportManifest
	var cp *Checkpoint
	fileContents := make(map[string][]byte)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read tar entry: %w", err)
		}

		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("failed to read file %s: %w", header.Name, err)
		}

		fileContents[header.Name] = data

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
	if opts.VerifyChecksums && manifest != nil {
		for file, expectedSum := range manifest.Checksums {
			data, ok := fileContents[file]
			if !ok {
				return nil, fmt.Errorf("manifest lists missing file: %s", file)
			}
			actualSum := sha256sum(data)
			if actualSum != expectedSum {
				return nil, fmt.Errorf("checksum mismatch for %s: expected %s, got %s", file, expectedSum, actualSum)
			}
		}
	}

	// Use session name from manifest if available, or from checkpoint
	sessionName := cp.SessionName
	if manifest != nil && manifest.SessionName != "" {
		sessionName = manifest.SessionName
	}

	// Apply overrides
	if opts.TargetSession != "" {
		sessionName = opts.TargetSession
		cp.SessionName = opts.TargetSession
	}
	if opts.TargetDir != "" {
		cp.WorkingDir = opts.TargetDir
	}

	// Check for existing checkpoint
	cpDir := s.CheckpointDir(sessionName, cp.ID)
	if _, err := os.Stat(cpDir); err == nil && !opts.AllowOverwrite {
		return nil, fmt.Errorf("checkpoint %s already exists (use AllowOverwrite to replace)", cp.ID)
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

		destPath := filepath.Join(cpDir, name)
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory for %s: %w", name, err)
		}
		if err := os.WriteFile(destPath, data, 0644); err != nil {
			return nil, fmt.Errorf("failed to write %s: %w", name, err)
		}
	}

	return cp, nil
}

func (s *Storage) importZip(archivePath string, opts ImportOptions) (*Checkpoint, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open zip archive: %w", err)
	}
	defer zr.Close()

	var manifest *ExportManifest
	var cp *Checkpoint
	fileContents := make(map[string][]byte)

	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("failed to open %s: %w", f.Name, err)
		}

		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", f.Name, err)
		}

		fileContents[f.Name] = data

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
	if opts.VerifyChecksums && manifest != nil {
		for file, expectedSum := range manifest.Checksums {
			data, ok := fileContents[file]
			if !ok {
				return nil, fmt.Errorf("manifest lists missing file: %s", file)
			}
			actualSum := sha256sum(data)
			if actualSum != expectedSum {
				return nil, fmt.Errorf("checksum mismatch for %s", file)
			}
		}
	}

	// Use session name from manifest if available, or from checkpoint
	sessionName := cp.SessionName
	if manifest != nil && manifest.SessionName != "" {
		sessionName = manifest.SessionName
	}

	// Apply overrides
	if opts.TargetSession != "" {
		sessionName = opts.TargetSession
		cp.SessionName = opts.TargetSession
	}
	if opts.TargetDir != "" {
		cp.WorkingDir = opts.TargetDir
	}

	// Check for existing
	cpDir := s.CheckpointDir(sessionName, cp.ID)
	if _, err := os.Stat(cpDir); err == nil && !opts.AllowOverwrite {
		return nil, fmt.Errorf("checkpoint %s already exists", cp.ID)
	}

	// Create checkpoint directory
	if err := os.MkdirAll(cpDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create checkpoint directory: %w", err)
	}

	for name, data := range fileContents {
		if name == "MANIFEST.json" {
			continue
		}

		destPath := filepath.Join(cpDir, name)
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory for %s: %w", name, err)
		}
		if err := os.WriteFile(destPath, data, 0644); err != nil {
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

func sha256sum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func redactSecrets(data []byte) []byte {
	result := string(data)
	for _, pattern := range secretPatterns {
		result = pattern.ReplaceAllString(result, "[REDACTED]")
	}
	return []byte(result)
}

func rewriteCheckpointPaths(cp *Checkpoint) *Checkpoint {
	result := *cp
	if result.WorkingDir != "" {
		result.WorkingDir = "${WORKING_DIR}"
	}
	return &result
}
