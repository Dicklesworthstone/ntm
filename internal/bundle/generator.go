package bundle

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

// GeneratorConfig configures support bundle generation.
type GeneratorConfig struct {
	// Session is the target session name (optional).
	Session string

	// OutputPath is the destination file path.
	OutputPath string

	// Format specifies the archive format (zip or tar.gz).
	Format Format

	// NTMVersion is included in the manifest.
	NTMVersion string

	// Since filters content to entries after this time (optional).
	Since *time.Time

	// Lines limits scrollback capture per pane (0 = unlimited).
	Lines int

	// MaxSizeBytes is the maximum bundle size (0 = unlimited).
	MaxSizeBytes int64

	// RedactionConfig configures secret redaction.
	RedactionConfig redaction.Config

	// WorkDir is the working directory for relative paths.
	WorkDir string
}

// GeneratorResult contains the outcome of bundle generation.
type GeneratorResult struct {
	// Path is the generated bundle file path.
	Path string `json:"path"`

	// Format is the archive format used.
	Format Format `json:"format"`

	// FileCount is the number of files in the bundle.
	FileCount int `json:"file_count"`

	// TotalSize is the total size in bytes.
	TotalSize int64 `json:"total_size"`

	// RedactionSummary contains aggregate redaction stats.
	RedactionSummary *RedactionSummary `json:"redaction_summary,omitempty"`

	// Manifest is the generated manifest.
	Manifest *Manifest `json:"manifest,omitempty"`

	// Errors contains any non-fatal errors during generation.
	Errors []string `json:"errors,omitempty"`

	// Warnings contains any warnings.
	Warnings []string `json:"warnings,omitempty"`
}

// Generator creates support bundles.
type Generator struct {
	config GeneratorConfig
	files  []bundleFile
	errors []string
}

// bundleFile represents a file to include in the bundle.
type bundleFile struct {
	path        string
	data        []byte
	contentType string
	redaction   *FileRedaction
	modTime     time.Time
}

// NewGenerator creates a new bundle generator.
func NewGenerator(config GeneratorConfig) *Generator {
	return &Generator{
		config: config,
		files:  []bundleFile{},
		errors: []string{},
	}
}

// SanitizeArchivePath converts a user- or system-derived path into a safe
// relative archive member name.
func SanitizeArchivePath(relativePath string) (string, error) {
	candidate := strings.ReplaceAll(relativePath, "\\", "/")
	if strings.TrimSpace(candidate) == "" {
		return "", fmt.Errorf("archive path is empty")
	}
	if strings.ContainsRune(candidate, 0) {
		return "", fmt.Errorf("archive path %q contains a NUL byte", relativePath)
	}
	if path.IsAbs(candidate) {
		return "", fmt.Errorf("archive path %q must be relative", relativePath)
	}

	for _, part := range strings.Split(candidate, "/") {
		if part == ".." {
			return "", fmt.Errorf("archive path %q escapes bundle root", relativePath)
		}
		if strings.Contains(part, ":") {
			return "", fmt.Errorf("archive path %q must not contain ':' segments", relativePath)
		}
	}

	clean := path.Clean(candidate)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return "", fmt.Errorf("archive path %q escapes bundle root", relativePath)
	}
	return clean, nil
}

// AddFile adds a file to the bundle with optional redaction.
func (g *Generator) AddFile(relativePath string, data []byte, contentType string, modTime time.Time) error {
	archivePath, err := SanitizeArchivePath(relativePath)
	if err != nil {
		g.errors = append(g.errors, fmt.Sprintf("unsafe archive path: %s: %v", relativePath, err))
		return err
	}

	// Apply redaction if mode is not off
	var fileRedaction *FileRedaction
	processedData := data

	if g.config.RedactionConfig.Mode != redaction.ModeOff {
		result := redaction.ScanAndRedact(string(data), g.config.RedactionConfig)

		if len(result.Findings) > 0 {
			fileRedaction = &FileRedaction{
				WasRedacted:  g.config.RedactionConfig.Mode == redaction.ModeRedact,
				FindingCount: len(result.Findings),
				Categories:   make([]string, 0),
				OriginalSize: int64(len(data)),
			}

			// Collect unique categories
			seen := make(map[string]bool)
			for _, f := range result.Findings {
				cat := string(f.Category)
				if !seen[cat] {
					seen[cat] = true
					fileRedaction.Categories = append(fileRedaction.Categories, cat)
				}
			}

			// Use redacted output if in redact mode
			if g.config.RedactionConfig.Mode == redaction.ModeRedact {
				processedData = []byte(result.Output)
			}

			// Block if configured
			if result.Blocked {
				g.errors = append(g.errors, fmt.Sprintf("blocked: %s contains %d secrets", archivePath, len(result.Findings)))
				return fmt.Errorf("file %s blocked due to secrets", archivePath)
			}
		}
	}

	g.files = append(g.files, bundleFile{
		path:        archivePath,
		data:        processedData,
		contentType: contentType,
		redaction:   fileRedaction,
		modTime:     modTime,
	})

	return nil
}

// AddDirectory adds all files from a directory recursively.
func (g *Generator) AddDirectory(basePath, relativeTo, contentType string) error {
	return filepath.WalkDir(basePath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			g.errors = append(g.errors, fmt.Sprintf("walk error: %s: %v", path, err))
			return nil // Continue walking
		}

		if d.IsDir() {
			return nil
		}

		info, err := os.Lstat(path)
		if err != nil {
			g.errors = append(g.errors, fmt.Sprintf("stat error: %s: %v", path, err))
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			g.errors = append(g.errors, fmt.Sprintf("symlink skipped: %s", path))
			return nil
		}
		if !info.Mode().IsRegular() {
			g.errors = append(g.errors, fmt.Sprintf("non-regular file skipped: %s", path))
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			g.errors = append(g.errors, fmt.Sprintf("read error: %s: %v", path, err))
			return nil
		}

		relPath, err := filepath.Rel(relativeTo, path)
		if err != nil {
			relPath = path
		}

		return g.AddFile(relPath, data, contentType, info.ModTime())
	})
}

// AddScrollback adds pane scrollback with optional line limit.
func (g *Generator) AddScrollback(paneName string, content string, lines int) error {
	if lines > 0 {
		content = limitLines(content, lines)
	}
	return g.AddFile(
		fmt.Sprintf("panes/%s.txt", paneName),
		[]byte(content),
		ContentTypeScrollback,
		time.Now(),
	)
}

// Generate creates the bundle archive.
func (g *Generator) Generate() (*GeneratorResult, error) {
	result := &GeneratorResult{
		Path:     g.config.OutputPath,
		Format:   g.config.Format,
		Errors:   g.errors,
		Warnings: []string{},
	}

	// Create manifest
	manifest := NewManifest(g.config.NTMVersion)
	manifest.Filters = &BundleFilters{
		Lines:        g.config.Lines,
		MaxSizeBytes: g.config.MaxSizeBytes,
	}
	if g.config.Since != nil {
		manifest.Filters.Since = g.config.Since.Format(time.RFC3339)
	}
	if g.config.Session != "" {
		manifest.Session = &SessionInfo{
			Name: g.config.Session,
		}
	}

	// Calculate total size and check limit
	var totalSize int64
	for _, f := range g.files {
		totalSize += int64(len(f.data))
	}

	if g.config.MaxSizeBytes > 0 && totalSize > g.config.MaxSizeBytes {
		return nil, fmt.Errorf("bundle size %d exceeds limit %d", totalSize, g.config.MaxSizeBytes)
	}

	// Add file entries to manifest
	redactionStats := &RedactionSummary{
		Mode:           string(g.config.RedactionConfig.Mode),
		CategoryCounts: make(map[string]int),
	}

	for _, f := range g.files {
		entry := FileEntry{
			Path:        f.path,
			SHA256:      HashBytes(f.data),
			SizeBytes:   int64(len(f.data)),
			ContentType: f.contentType,
			Redaction:   f.redaction,
		}
		if !f.modTime.IsZero() {
			entry.ModTime = f.modTime.Format(time.RFC3339)
		}
		manifest.AddFile(entry)

		// Aggregate redaction stats
		redactionStats.FilesScanned++
		if f.redaction != nil && f.redaction.WasRedacted {
			redactionStats.FilesRedacted++
			redactionStats.TotalFindings += f.redaction.FindingCount
			for _, cat := range f.redaction.Categories {
				redactionStats.CategoryCounts[cat]++
			}
		}
	}

	manifest.RedactionSummary = redactionStats
	manifest.Errors = g.errors

	// Generate the archive
	switch g.config.Format {
	case FormatZip:
		if err := g.generateZip(manifest); err != nil {
			return nil, err
		}
	case FormatTarGz:
		if err := g.generateTarGz(manifest); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported format: %s", g.config.Format)
	}

	// Get final file size
	info, err := os.Stat(g.config.OutputPath)
	if err == nil {
		result.TotalSize = info.Size()
	}

	result.FileCount = len(g.files)
	result.Manifest = manifest
	result.RedactionSummary = redactionStats

	return result, nil
}

// generateZip creates a zip archive.
func (g *Generator) generateZip(manifest *Manifest) (err error) {
	f, err := os.Create(g.config.OutputPath)
	if err != nil {
		return fmt.Errorf("create zip: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close zip file: %w", closeErr)
		}
	}()

	w := zip.NewWriter(f)
	defer func() {
		if closeErr := w.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close zip writer: %w", closeErr)
		}
	}()

	// Write manifest first
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}

	mf, err := w.Create(ManifestFileName)
	if err != nil {
		return fmt.Errorf("create manifest entry: %w", err)
	}
	if _, err := mf.Write(manifestData); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	// Write files
	for _, file := range g.files {
		fw, err := w.Create(file.path)
		if err != nil {
			return fmt.Errorf("create entry %s: %w", file.path, err)
		}
		if _, err := fw.Write(file.data); err != nil {
			return fmt.Errorf("write entry %s: %w", file.path, err)
		}
	}

	return nil
}

// generateTarGz creates a tar.gz archive.
func (g *Generator) generateTarGz(manifest *Manifest) (err error) {
	f, err := os.Create(g.config.OutputPath)
	if err != nil {
		return fmt.Errorf("create tar.gz: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close tar.gz file: %w", closeErr)
		}
	}()

	gw := gzip.NewWriter(f)
	defer func() {
		if closeErr := gw.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close gzip writer: %w", closeErr)
		}
	}()

	tw := tar.NewWriter(gw)
	defer func() {
		if closeErr := tw.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close tar writer: %w", closeErr)
		}
	}()

	// Write manifest first
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}

	if err := g.writeTarEntry(tw, ManifestFileName, manifestData, time.Now()); err != nil {
		return err
	}

	// Write files
	for _, file := range g.files {
		modTime := file.modTime
		if modTime.IsZero() {
			modTime = time.Now()
		}
		if err := g.writeTarEntry(tw, file.path, file.data, modTime); err != nil {
			return err
		}
	}

	return nil
}

// writeTarEntry writes a single entry to a tar archive.
func (g *Generator) writeTarEntry(tw *tar.Writer, name string, data []byte, modTime time.Time) error {
	header := &tar.Header{
		Name:    name,
		Size:    int64(len(data)),
		Mode:    0644,
		ModTime: modTime,
	}

	if err := tw.WriteHeader(header); err != nil {
		return fmt.Errorf("write tar header %s: %w", name, err)
	}

	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("write tar data %s: %w", name, err)
	}

	return nil
}

// limitLines returns the last n lines of content.
func limitLines(content string, n int) string {
	if n <= 0 {
		return content
	}

	lines := strings.Split(content, "\n")
	if len(lines) <= n {
		return content
	}

	return strings.Join(lines[len(lines)-n:], "\n")
}

// SuggestOutputPath generates a default output path.
func SuggestOutputPath(session string, format Format) string {
	timestamp := time.Now().Format("20060102-150405")
	name := "ntm-bundle"
	if session != "" {
		name = fmt.Sprintf("ntm-%s", session)
	}
	return fmt.Sprintf("%s-%s%s", name, timestamp, format.Extension())
}
