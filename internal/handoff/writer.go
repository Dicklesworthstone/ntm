package handoff

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

var (
	// writeMu protects concurrent writes to the same directory.
	writeMu sync.Mutex

	// descriptionRegex is defined in validate.go as sessionNameRegex
	// We reuse it for description validation (alphanumeric + hyphen).
)

// DefaultMaxPerDir is the default number of handoffs to keep before rotation.
const DefaultMaxPerDir = 50

// Writer handles writing handoff files to disk with atomic writes and rotation.
type Writer struct {
	baseDir   string // typically .ntm/handoffs
	maxPerDir int    // max handoffs before rotation (default 50)
	logger    *slog.Logger
}

// NewWriter creates a Writer for the given project directory.
func NewWriter(projectDir string) *Writer {
	return &Writer{
		baseDir:   filepath.Join(projectDir, ".ntm", "handoffs"),
		maxPerDir: DefaultMaxPerDir,
		logger:    slog.Default().With("component", "handoff.writer"),
	}
}

// NewWriterWithOptions creates a Writer with custom options.
func NewWriterWithOptions(projectDir string, maxPerDir int, logger *slog.Logger) *Writer {
	if maxPerDir <= 0 {
		maxPerDir = DefaultMaxPerDir
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Writer{
		baseDir:   filepath.Join(projectDir, ".ntm", "handoffs"),
		maxPerDir: maxPerDir,
		logger:    logger.With("component", "handoff.writer"),
	}
}

func normalizeWriterSessionName(sessionName string) (string, error) {
	if sessionName == "" {
		sessionName = "general"
	}
	if sessionName != "general" && !sessionNameRegex.MatchString(sessionName) {
		return "", fmt.Errorf("invalid session name: %s", sessionName)
	}
	return sessionName, nil
}

func ensureNoSymlinkComponents(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}

	volume := filepath.VolumeName(absPath)
	remainder := strings.TrimPrefix(absPath, volume)
	sep := string(filepath.Separator)
	remainder = strings.TrimPrefix(remainder, sep)

	current := volume + sep
	if current == "" {
		current = sep
	}

	for _, part := range strings.Split(remainder, sep) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("lstat %s: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path %s traverses symlinked component %s", path, current)
		}
	}

	return nil
}

func ensureSafeDir(path string) error {
	if err := ensureNoSymlinkComponents(path); err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0755); err != nil {
		return fmt.Errorf("mkdir failed: %w", err)
	}

	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("lstat %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("directory %s is a symlink", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("path %s is not a directory", path)
	}

	return nil
}

func (w *Writer) ensureSessionDir(sessionName string) (string, error) {
	sessionName, err := normalizeWriterSessionName(sessionName)
	if err != nil {
		return "", err
	}
	if err := ensureSafeDir(w.baseDir); err != nil {
		return "", err
	}

	dir := filepath.Join(w.baseDir, sessionName)
	if err := ensureSafeDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

func (w *Writer) sessionDirPath(sessionName string) (string, error) {
	sessionName, err := normalizeWriterSessionName(sessionName)
	if err != nil {
		return "", err
	}
	if err := ensureNoSymlinkComponents(w.baseDir); err != nil {
		return "", err
	}

	dir := filepath.Join(w.baseDir, sessionName)
	if err := ensureNoSymlinkComponents(dir); err != nil {
		return "", err
	}
	return dir, nil
}

func visibleYAMLPaths(paths []string) []string {
	filtered := make([]string, 0, len(paths))
	for _, path := range paths {
		if strings.HasPrefix(filepath.Base(path), ".") {
			continue
		}
		filtered = append(filtered, path)
	}
	return filtered
}

func (w *Writer) validateManagedHandoffPath(path string) (string, string, error) {
	if path == "" {
		return "", "", fmt.Errorf("path is required")
	}
	if !strings.HasSuffix(filepath.Base(path), ".yaml") {
		return "", "", fmt.Errorf("path %s is not a handoff yaml file", path)
	}

	absBase, err := filepath.Abs(w.baseDir)
	if err != nil {
		return "", "", fmt.Errorf("invalid base dir: %w", err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", "", fmt.Errorf("invalid path: %w", err)
	}

	rel, err := filepath.Rel(absBase, absPath)
	if err != nil {
		return "", "", fmt.Errorf("invalid path: %w", err)
	}
	parentEscape := ".." + string(filepath.Separator)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, parentEscape) {
		return "", "", fmt.Errorf("path %s is not within handoff directory", path)
	}

	if err := ensureNoSymlinkComponents(absBase); err != nil {
		return "", "", err
	}
	if err := ensureNoSymlinkComponents(absPath); err != nil {
		return "", "", err
	}

	info, err := os.Lstat(absPath)
	if err == nil {
		if !info.Mode().IsRegular() {
			return "", "", fmt.Errorf("path %s is not a regular handoff file", path)
		}
	} else if !os.IsNotExist(err) {
		return "", "", fmt.Errorf("lstat %s: %w", path, err)
	}

	return absPath, rel, nil
}

func pathContainsArchiveDir(rel string) bool {
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == ".archive" {
			return true
		}
	}
	return false
}

// Write saves a handoff to the appropriate directory using atomic write.
// The description is sanitized to kebab-case for use in the filename.
// Returns the path to the written file.
func (w *Writer) Write(h *Handoff, description string) (string, error) {
	writeMu.Lock()
	defer writeMu.Unlock()

	w.logger.Debug("starting handoff write",
		"session", h.Session,
		"description", description,
		"goal_length", len(h.Goal),
		"now_length", len(h.Now),
	)

	// Validate handoff (includes session name validation)
	if errs := h.Validate(); len(errs) > 0 {
		w.logger.Error("handoff validation failed",
			"session", h.Session,
			"error_count", len(errs),
			"first_error", errs[0].Error(),
		)
		return "", fmt.Errorf("validation failed: %v", errs[0])
	}

	// Sanitize description
	desc := sanitizeDescription(description)
	if desc == "" {
		desc = "handoff"
	}

	// Ensure directory exists
	dir, err := w.ensureSessionDir(h.Session)
	if err != nil {
		return "", err
	}

	// Check for rotation
	if err := w.checkRotation(h.Session); err != nil {
		w.logger.Warn("rotation check failed", "error", err)
		// Non-fatal, continue with write
	}

	// Set defaults
	h.SetDefaults()

	// Generate filename: YYYY-MM-DD_HH-MM_{description}.yaml
	filename := fmt.Sprintf("%s_%s.yaml",
		time.Now().Format("2006-01-02_15-04"),
		desc,
	)
	path := filepath.Join(dir, filename)

	// Serialize to YAML
	data, err := yaml.Marshal(h)
	if err != nil {
		w.logger.Error("yaml marshaling failed",
			"session", h.Session,
			"error", err,
		)
		return "", fmt.Errorf("marshal failed: %w", err)
	}

	// Atomic write: write to temp, then rename
	if err := w.atomicWrite(path, data); err != nil {
		return "", err
	}

	if err := w.appendLedgerEntry(h, path, false); err != nil {
		w.logger.Warn("failed to append handoff ledger",
			"session", h.Session,
			"path", path,
			"error", err,
		)
	}

	w.logger.Info("handoff written successfully",
		"path", path,
		"session", h.Session,
		"goal", truncateLog(h.Goal, 50),
		"now", truncateLog(h.Now, 50),
		"size_bytes", len(data),
	)

	return path, nil
}

// WriteAuto saves an auto-generated handoff with timestamp naming.
// Auto handoffs use the format: auto-handoff-{ISO8601-timestamp}.yaml
func (w *Writer) WriteAuto(h *Handoff) (string, error) {
	writeMu.Lock()
	defer writeMu.Unlock()

	w.logger.Debug("starting auto-handoff write",
		"session", h.Session,
		"tokens_pct", h.TokensPct,
	)

	// Validate
	if errs := h.Validate(); len(errs) > 0 {
		w.logger.Error("auto-handoff validation failed",
			"session", h.Session,
			"error_count", len(errs),
		)
		return "", fmt.Errorf("validation failed: %v", errs[0])
	}

	// Ensure directory
	dir, err := w.ensureSessionDir(h.Session)
	if err != nil {
		return "", err
	}

	// Check for rotation
	if err := w.checkRotation(h.Session); err != nil {
		w.logger.Warn("rotation check failed", "error", err)
	}

	// Set defaults
	h.SetDefaults()

	// Generate auto filename with ISO8601 timestamp
	filename := fmt.Sprintf("auto-handoff-%s.yaml",
		time.Now().Format("2006-01-02T15-04-05"),
	)
	path := filepath.Join(dir, filename)

	// Serialize
	data, err := yaml.Marshal(h)
	if err != nil {
		return "", fmt.Errorf("marshal failed: %w", err)
	}

	// Atomic write
	if err := w.atomicWrite(path, data); err != nil {
		return "", err
	}

	if err := w.appendLedgerEntry(h, path, true); err != nil {
		w.logger.Warn("failed to append auto-handoff ledger",
			"session", h.Session,
			"path", path,
			"error", err,
		)
	}

	w.logger.Info("auto-handoff written",
		"path", path,
		"session", h.Session,
		"tokens_pct", h.TokensPct,
		"size_bytes", len(data),
	)

	return path, nil
}

// EnsureDir creates the handoff directory for a session if needed.
func (w *Writer) EnsureDir(sessionName string) error {
	dir, err := w.ensureSessionDir(sessionName)
	if err != nil {
		w.logger.Error("failed to create handoff directory",
			"session", sessionName,
			"error", err,
		)
		return err
	}

	w.logger.Debug("ensured handoff directory", "dir", dir)
	return nil
}

// BaseDir returns the base directory where handoffs are stored.
func (w *Writer) BaseDir() string {
	return w.baseDir
}

// atomicWrite writes data to a temp file then renames to target path.
// This ensures the file is either fully written or not at all.
func (w *Writer) atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)

	// Create temp file in same directory (ensures same filesystem for atomic rename)
	tmp, err := os.CreateTemp(dir, ".handoff-*.tmp")
	if err != nil {
		w.logger.Error("failed to create temp file",
			"dir", dir,
			"error", err,
		)
		return fmt.Errorf("temp file creation failed: %w", err)
	}
	tmpPath := tmp.Name()

	// Cleanup on failure
	success := false
	defer func() {
		if !success {
			os.Remove(tmpPath)
		}
	}()

	// Write data
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		w.logger.Error("failed to write temp file",
			"path", tmpPath,
			"error", err,
		)
		return fmt.Errorf("write failed: %w", err)
	}

	// Sync to disk
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		w.logger.Error("failed to sync temp file",
			"path", tmpPath,
			"error", err,
		)
		return fmt.Errorf("sync failed: %w", err)
	}

	// Close before rename
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close failed: %w", err)
	}

	// Set permissions
	if err := os.Chmod(tmpPath, 0644); err != nil {
		return fmt.Errorf("chmod failed: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpPath, path); err != nil {
		w.logger.Error("failed to rename temp to target",
			"temp", tmpPath,
			"target", path,
			"error", err,
		)
		return fmt.Errorf("rename failed: %w", err)
	}

	success = true // Prevent cleanup of successful write
	return nil
}

// checkRotation moves old handoffs to .archive if count exceeds maxPerDir.
func (w *Writer) checkRotation(sessionName string) error {
	dir, err := w.sessionDirPath(sessionName)
	if err != nil {
		return err
	}
	archiveDir := filepath.Join(dir, ".archive")

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Directory doesn't exist yet, nothing to rotate
		}
		return err
	}

	yamlFiles := visibleYAMLPaths(discoverHandoffYAMLPaths(dir, entries))

	// Check rotation needs: since we're about to write a new file,
	// rotate if count >= maxPerDir to make room
	if len(yamlFiles) < w.maxPerDir {
		return nil // No rotation needed
	}

	// Sort by name ascending (older files have earlier timestamps in name)
	sort.Slice(yamlFiles, func(i, j int) bool {
		return filepath.Base(yamlFiles[i]) < filepath.Base(yamlFiles[j])
	})

	// Create archive dir
	if err := ensureSafeDir(archiveDir); err != nil {
		return err
	}

	// Move oldest files to archive (at least 1 to make room for new file)
	toMove := len(yamlFiles) - w.maxPerDir + 1
	moved := 0
	for i := 0; i < toMove; i++ {
		oldPath := yamlFiles[i]
		newPath := filepath.Join(archiveDir, filepath.Base(yamlFiles[i]))
		if err := os.Rename(oldPath, newPath); err != nil {
			w.logger.Warn("failed to archive handoff",
				"path", oldPath,
				"error", err,
			)
		} else {
			w.logger.Debug("archived old handoff",
				"from", oldPath,
				"to", newPath,
			)
			moved++
		}
	}

	if moved > 0 {
		w.logger.Info("handoff rotation completed",
			"session", sessionName,
			"archived", moved,
		)
	}

	return nil
}

func (w *Writer) appendLedgerEntry(h *Handoff, handoffPath string, isAuto bool) error {
	ledgerDir := filepath.Join(filepath.Dir(w.baseDir), "ledgers")
	if err := os.MkdirAll(ledgerDir, 0755); err != nil {
		return fmt.Errorf("create ledger dir: %w", err)
	}

	session := h.Session
	if session == "" {
		session = "general"
	}

	ledgerPath := filepath.Join(ledgerDir, fmt.Sprintf("CONTINUITY_%s.md", session))
	entry := formatLedgerEntry(h, handoffPath, isAuto, time.Now().UTC())

	f, err := os.OpenFile(ledgerPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open ledger: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(entry); err != nil {
		return fmt.Errorf("append ledger: %w", err)
	}

	return nil
}

func formatLedgerEntry(h *Handoff, handoffPath string, isAuto bool, now time.Time) string {
	mode := "manual"
	if isAuto {
		mode = "auto"
	}

	lines := []string{
		fmt.Sprintf("## %s (%s)", now.Format(time.RFC3339), mode),
		fmt.Sprintf("- file: %s", filepath.Base(handoffPath)),
	}

	if h.Status != "" {
		lines = append(lines, fmt.Sprintf("- status: %s", h.Status))
	}
	if h.Outcome != "" {
		lines = append(lines, fmt.Sprintf("- outcome: %s", h.Outcome))
	}
	if goal := truncateLog(singleLine(h.Goal), 200); goal != "" {
		lines = append(lines, fmt.Sprintf("- goal: %s", goal))
	}
	if nowText := truncateLog(singleLine(h.Now), 200); nowText != "" {
		lines = append(lines, fmt.Sprintf("- now: %s", nowText))
	}
	if test := truncateLog(singleLine(h.Test), 200); test != "" {
		lines = append(lines, fmt.Sprintf("- test: %s", test))
	}
	if blockers := compactList(h.Blockers, 5); blockers != "" {
		lines = append(lines, fmt.Sprintf("- blockers: %s", blockers))
	}
	if next := compactList(h.Next, 5); next != "" {
		lines = append(lines, fmt.Sprintf("- next: %s", next))
	}
	if len(h.ActiveBeads) > 0 {
		lines = append(lines, fmt.Sprintf("- beads: %s", strings.Join(h.ActiveBeads, ", ")))
	}
	if h.TokensPct > 0 {
		lines = append(lines, fmt.Sprintf("- tokens_pct: %.2f", h.TokensPct))
	}

	return strings.Join(lines, "\n") + "\n\n"
}

func singleLine(s string) string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return ""
	}
	return strings.Join(strings.Fields(trimmed), " ")
}

func compactList(items []string, maxItems int) string {
	if len(items) == 0 {
		return ""
	}

	limit := maxItems
	if limit <= 0 || limit > len(items) {
		limit = len(items)
	}

	clean := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		item := singleLine(items[i])
		if item != "" {
			clean = append(clean, item)
		}
	}

	if len(clean) == 0 {
		return ""
	}

	out := strings.Join(clean, ", ")
	if remaining := len(items) - limit; remaining > 0 {
		out = fmt.Sprintf("%s +%d more", out, remaining)
	}

	return truncateLog(out, 200)
}

// sanitizeDescription converts a description to kebab-case for use in filenames.
// It lowercases, replaces spaces/underscores with hyphens, removes non-alphanumeric
// characters (except hyphens), collapses multiple hyphens, and limits length.
func sanitizeDescription(desc string) string {
	// Lowercase
	desc = strings.ToLower(desc)

	// Replace spaces/underscores with hyphens
	desc = strings.ReplaceAll(desc, " ", "-")
	desc = strings.ReplaceAll(desc, "_", "-")

	// Remove non-alphanumeric except hyphens
	var result strings.Builder
	for _, r := range desc {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			result.WriteRune(r)
		}
	}
	desc = result.String()

	// Collapse multiple hyphens
	for strings.Contains(desc, "--") {
		desc = strings.ReplaceAll(desc, "--", "-")
	}

	// Trim hyphens from ends
	desc = strings.Trim(desc, "-")

	// Limit length
	if len(desc) > 50 {
		desc = desc[:50]
		// Don't leave trailing hyphen after truncation
		desc = strings.TrimRight(desc, "-")
	}

	return desc
}

// truncateLog truncates a string for logging purposes.
func truncateLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return "" // Can't fit any content + "..."
	}
	return s[:max-3] + "..."
}

// Delete removes a specific handoff file.
// Use with caution - typically handoffs should be archived, not deleted.
func (w *Writer) Delete(path string) error {
	writeMu.Lock()
	defer writeMu.Unlock()

	safePath, _, err := w.validateManagedHandoffPath(path)
	if err != nil {
		return err
	}

	if err := os.Remove(safePath); err != nil {
		w.logger.Error("failed to delete handoff",
			"path", safePath,
			"error", err,
		)
		return fmt.Errorf("delete failed: %w", err)
	}

	w.logger.Info("handoff deleted", "path", safePath)
	return nil
}

// Archive moves a specific handoff to the .archive directory.
func (w *Writer) Archive(path string) error {
	writeMu.Lock()
	defer writeMu.Unlock()

	safePath, rel, err := w.validateManagedHandoffPath(path)
	if err != nil {
		return err
	}

	// Don't archive files already in .archive
	if pathContainsArchiveDir(rel) {
		return fmt.Errorf("file is already archived")
	}

	dir := filepath.Dir(safePath)
	archiveDir := filepath.Join(dir, ".archive")

	// Ensure archive dir exists
	if err := ensureSafeDir(archiveDir); err != nil {
		return fmt.Errorf("create archive dir failed: %w", err)
	}

	newPath := filepath.Join(archiveDir, filepath.Base(safePath))
	if err := os.Rename(safePath, newPath); err != nil {
		w.logger.Error("failed to archive handoff",
			"from", safePath,
			"to", newPath,
			"error", err,
		)
		return fmt.Errorf("archive failed: %w", err)
	}

	w.logger.Info("handoff archived",
		"from", safePath,
		"to", newPath,
	)
	return nil
}

// WriteToPath writes a handoff directly to the specified path.
// Unlike Write(), this doesn't auto-generate the filename or session directory.
func (w *Writer) WriteToPath(h *Handoff, path string) error {
	writeMu.Lock()
	defer writeMu.Unlock()

	w.logger.Debug("writing handoff to path",
		"path", path,
		"session", h.Session,
	)

	// Validate handoff
	if errs := h.Validate(); len(errs) > 0 {
		w.logger.Error("handoff validation failed",
			"path", path,
			"error_count", len(errs),
		)
		return fmt.Errorf("validation failed: %v", errs[0])
	}

	// Set defaults
	h.SetDefaults()

	// Ensure parent directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Serialize to YAML
	data, err := yaml.Marshal(h)
	if err != nil {
		return fmt.Errorf("marshal failed: %w", err)
	}

	// Atomic write
	if err := w.atomicWrite(path, data); err != nil {
		return err
	}

	w.logger.Info("handoff written to path",
		"path", path,
		"session", h.Session,
		"size_bytes", len(data),
	)

	return nil
}

// MarshalYAML serializes a handoff to YAML bytes.
func MarshalYAML(h *Handoff) ([]byte, error) {
	return yaml.Marshal(h)
}

// CleanArchive removes archived handoffs older than the given duration.
func (w *Writer) CleanArchive(sessionName string, olderThan time.Duration) (int, error) {
	writeMu.Lock()
	defer writeMu.Unlock()

	sessionDir, err := w.sessionDirPath(sessionName)
	if err != nil {
		return 0, err
	}

	archiveDir := filepath.Join(sessionDir, ".archive")
	if err := ensureNoSymlinkComponents(archiveDir); err != nil {
		return 0, err
	}

	entries, err := os.ReadDir(archiveDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil // No archive dir, nothing to clean
		}
		return 0, err
	}

	cutoff := time.Now().Add(-olderThan)
	removed := 0

	for _, path := range visibleYAMLPaths(discoverHandoffYAMLPaths(archiveDir, entries)) {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}

		if info.ModTime().Before(cutoff) {
			if err := os.Remove(path); err != nil {
				w.logger.Warn("failed to remove old archived handoff",
					"path", path,
					"error", err,
				)
			} else {
				removed++
			}
		}
	}

	if removed > 0 {
		w.logger.Info("cleaned archive",
			"session", sessionName,
			"removed", removed,
			"older_than", olderThan,
		)
	}

	return removed, nil
}
