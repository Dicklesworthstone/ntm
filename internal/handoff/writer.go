package handoff

import (
	"crypto/rand"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
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

func normalizeWriterSessionName(sessionName string) (string, error) {
	if sessionName == "" {
		sessionName = "general"
	}
	if sessionName != "general" && !sessionNameRegex.MatchString(sessionName) {
		return "", fmt.Errorf("invalid session name: %s", sessionName)
	}
	return sessionName, nil
}

// ensureNoSymlinkComponents rejects paths whose canonical form differs
// from the lexical absolute form anywhere except a known macOS-style
// system-level prefix (e.g. /var → /private/var, /tmp → /private/tmp).
//
// Why this shape: the original implementation lstat'd every component
// from "/" downward and refused any symlink it saw. That was correct
// on Linux but unsound on macOS, where /var and /tmp are themselves
// symlinks to /private/var and /private/tmp respectively — every
// tempdir used by the tests would (and did) fail the check.
//
// The replacement compares the canonical form of `absPath` against the
// path itself: any in-tree symlink (e.g. .ntm/handoffs/linked pointing
// at /etc) causes the canonical to diverge below the system-level
// prefix and the call rejects. macOS's /var ↔ /private/var (and
// /tmp, /etc) substitutions are the only divergences we tolerate.
func ensureNoSymlinkComponents(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}
	absPath = filepath.Clean(absPath)

	canonical, err := canonicalPath(absPath)
	if err != nil {
		return err
	}
	canonical = filepath.Clean(canonical)

	if canonical == absPath {
		return nil
	}

	// macOS rewrites a leading /var, /tmp, or /etc to /private/var,
	// /private/tmp, /private/etc. Recognise that exact substitution
	// (in either direction) and accept the canonicalisation; anything
	// else is project-controlled and must be rejected.
	sep := string(filepath.Separator)
	systemPrefixes := []string{sep + "var", sep + "tmp", sep + "etc"}
	for _, p := range systemPrefixes {
		privatePrefix := sep + "private" + p
		// absPath uses raw prefix, canonical uses /private + prefix.
		if (absPath == p || strings.HasPrefix(absPath, p+sep)) &&
			canonical == privatePrefix+strings.TrimPrefix(absPath, p) {
			return nil
		}
		// absPath uses canonical prefix, canonical uses raw — the
		// reverse case (the supplied path was already canonical and
		// EvalSymlinks returned the same thing or a raw-prefix
		// equivalent). Kept for symmetry; not expected in practice.
		if (absPath == privatePrefix || strings.HasPrefix(absPath, privatePrefix+sep)) &&
			canonical == p+strings.TrimPrefix(absPath, privatePrefix) {
			return nil
		}
	}

	// Report the divergence at the raw side so the error names
	// something the caller actually passed in.
	return fmt.Errorf("path %s traverses symlinked component %s", path, absPath)
}

// canonicalPath resolves all symlinks in absPath. If part of the path
// does not yet exist, it canonicalises the deepest existing ancestor
// and re-attaches the missing tail. Returns absPath unchanged if no
// part of the path exists.
func canonicalPath(absPath string) (string, error) {
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		return resolved, nil
	}
	dir := absPath
	missing := ""
	for {
		resolved, err := filepath.EvalSymlinks(dir)
		if err == nil {
			if missing == "" {
				return resolved, nil
			}
			return filepath.Join(resolved, missing), nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("eval symlinks %s: %w", dir, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return absPath, nil
		}
		base := filepath.Base(dir)
		if missing == "" {
			missing = base
		} else {
			missing = filepath.Join(base, missing)
		}
		dir = parent
	}
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

	// Ledger entries must keep referring to distinct snapshots even when two
	// ntm processes save the same description in the same clock tick.
	filename := fmt.Sprintf("%s_%s_%s.yaml",
		time.Now().Format("2006-01-02_15-04-05.000000000"),
		rand.Text(), desc,
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

	snapshots := slices.DeleteFunc(discoverHandoffs(dir, entries), func(meta HandoffMeta) bool {
		return strings.HasPrefix(filepath.Base(meta.Path), ".")
	})

	// Check rotation needs: since we're about to write a new file,
	// rotate if count >= maxPerDir to make room
	if len(snapshots) < w.maxPerDir {
		return nil // No rotation needed
	}

	// Create archive dir
	if err := ensureSafeDir(archiveDir); err != nil {
		return err
	}

	// Move oldest files to archive (at least 1 to make room for new file)
	moved := 0
	for i := len(snapshots) - 1; i >= w.maxPerDir-1; i-- {
		oldPath := snapshots[i].Path
		newPath := filepath.Join(archiveDir, filepath.Base(oldPath))
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
	session, err := normalizeWriterSessionName(h.Session)
	if err != nil {
		return err
	}

	ntmDir, err := filepath.Abs(filepath.Dir(w.baseDir))
	if err != nil {
		return fmt.Errorf("resolve ledger root: %w", err)
	}
	if err := ensureSafeDir(ntmDir); err != nil {
		return fmt.Errorf("validate ledger root: %w", err)
	}

	// Open parent -> .ntm -> ledgers with os.OpenRoot (RESOLVE_IN_ROOT) and verify
	// SameFile against Lstat at every hop so neither cross-root nor in-root symlink
	// swaps can redirect writes or hang on FIFOs.
	var ntmRoot *os.Root
	parentDir := filepath.Dir(ntmDir)
	if parentDir == ntmDir {
		ntmRoot, err = os.OpenRoot(ntmDir)
		if err != nil {
			return fmt.Errorf("open ntm root: %w", err)
		}
	} else {
		ntmName := filepath.Base(ntmDir)
		parentRoot, openErr := os.OpenRoot(parentDir)
		if openErr != nil {
			return fmt.Errorf("open ledger root parent: %w", openErr)
		}
		defer parentRoot.Close()

		ntmInfo, statErr := parentRoot.Lstat(ntmName)
		if statErr != nil {
			return fmt.Errorf("lstat ntm dir: %w", statErr)
		}
		if ntmInfo.Mode()&os.ModeSymlink != 0 || !ntmInfo.IsDir() {
			return fmt.Errorf("ledger root %s is not a real directory", ntmDir)
		}
		ntmRoot, err = parentRoot.OpenRoot(ntmName)
		if err != nil {
			return fmt.Errorf("open ntm root: %w", err)
		}
		openedNtm, statErr := ntmRoot.Stat(".")
		if statErr != nil || !os.SameFile(ntmInfo, openedNtm) {
			_ = ntmRoot.Close()
			return fmt.Errorf("ledger root %s changed during open", ntmDir)
		}
	}
	defer ntmRoot.Close()

	linfo, lerr := ntmRoot.Lstat("ledgers")
	if os.IsNotExist(lerr) {
		if mkErr := ntmRoot.Mkdir("ledgers", 0755); mkErr != nil && !os.IsExist(mkErr) {
			return fmt.Errorf("create ledger dir: %w", mkErr)
		}
		linfo, lerr = ntmRoot.Lstat("ledgers")
	}
	if lerr != nil {
		return fmt.Errorf("lstat ledger dir: %w", lerr)
	}
	if linfo.Mode()&os.ModeSymlink != 0 || !linfo.IsDir() {
		return fmt.Errorf("ledger dir %s is not a real directory", filepath.Join(ntmDir, "ledgers"))
	}

	ledgerRoot, err := ntmRoot.OpenRoot("ledgers")
	if err != nil {
		return fmt.Errorf("open ledger dir root: %w", err)
	}
	defer ledgerRoot.Close()
	if openedLedger, statErr := ledgerRoot.Stat("."); statErr != nil || !os.SameFile(linfo, openedLedger) {
		return fmt.Errorf("ledger dir %s changed during open", filepath.Join(ntmDir, "ledgers"))
	}

	ledgerName := fmt.Sprintf("CONTINUITY_%s.md", session)
	ledgerPath := filepath.Join(ntmDir, "ledgers", ledgerName)
	entry := formatLedgerEntry(h, handoffPath, isAuto, time.Now().UTC())

	var f *os.File
	for attempt := 0; attempt < 2; attempt++ {
		info, statErr := ledgerRoot.Lstat(ledgerName)
		if os.IsNotExist(statErr) {
			// O_CREATE|O_EXCL refuses to follow any symlink (including in-root links).
			opened, openErr := ledgerRoot.OpenFile(ledgerName, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0644)
			if openErr != nil {
				if os.IsExist(openErr) && attempt == 0 {
					continue
				}
				return fmt.Errorf("open ledger: %w", openErr)
			}
			fi, fstatErr := opened.Stat()
			if fstatErr != nil || !fi.Mode().IsRegular() {
				_ = opened.Close()
				return fmt.Errorf("ledger path %s is not a regular file", ledgerPath)
			}
			f = opened
			break
		}
		if statErr != nil {
			return fmt.Errorf("lstat ledger: %w", statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("ledger path %s is a symlink", ledgerPath)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("ledger path %s is not a regular file", ledgerPath)
		}
		// Open existing regular file without O_CREATE and confirm inode identity
		// so an in-root symlink swap cannot redirect the append.
		opened, openErr := ledgerRoot.OpenFile(ledgerName, os.O_WRONLY|os.O_APPEND, 0644)
		if openErr != nil {
			return fmt.Errorf("open ledger: %w", openErr)
		}
		fi, fstatErr := opened.Stat()
		if fstatErr != nil || !fi.Mode().IsRegular() || !os.SameFile(info, fi) {
			_ = opened.Close()
			return fmt.Errorf("ledger path %s changed or became a symlink during open", ledgerPath)
		}
		f = opened
		break
	}
	if f == nil {
		return fmt.Errorf("open ledger %s: concurrent creation race", ledgerPath)
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
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 3 {
		return "" // Can't fit any content + "..."
	}
	return string(runes[:max-3]) + "..."
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
