package checkpoint

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	// DefaultCheckpointDir is the default directory for checkpoints
	DefaultCheckpointDir = ".local/share/ntm/checkpoints"
	// MetadataFile is the name of the checkpoint metadata file
	MetadataFile = "metadata.json"
	// SessionFile is the name of the session state file
	SessionFile = "session.json"
	// GitPatchFile is the name of the git diff patch file
	GitPatchFile = "git.patch"
	// GitStatusFile is the name of the git status file
	GitStatusFile = "git-status.txt"
	// PanesDir is the subdirectory for pane scrollback captures
	PanesDir = "panes"
)

var checkpointIDRegex = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

var (
	ErrNoCheckpoints = errors.New("no checkpoints found")
)

// Storage manages checkpoint storage on disk.
type Storage struct {
	// BaseDir is the base directory for all checkpoints
	BaseDir string
}

type checkpointSelectionEntry struct {
	name     string
	modTime  time.Time
	sortTime time.Time
}

// NewStorage creates a new Storage with the default directory.
// Falls back to /tmp if the user's home directory cannot be determined.
func NewStorage() *Storage {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		// Fallback to /tmp when home directory is unavailable (e.g., containers)
		home = os.TempDir()
	}
	return &Storage{
		BaseDir: filepath.Join(home, DefaultCheckpointDir),
	}
}

// NewStorageWithDir creates a Storage with a custom directory.
func NewStorageWithDir(dir string) *Storage {
	return &Storage{
		BaseDir: dir,
	}
}

// CheckpointDir returns the directory path for a specific checkpoint.
func (s *Storage) CheckpointDir(sessionName, checkpointID string) string {
	dir, err := s.safeCheckpointDir(sessionName, checkpointID)
	if err != nil {
		return filepath.Join(s.BaseDir, safePathFallbackComponent(sessionName), safePathFallbackComponent(checkpointID))
	}
	return dir
}

func (s *Storage) safeSessionDir(sessionName string) (string, error) {
	if err := tmux.ValidateSessionName(sessionName); err != nil {
		return "", fmt.Errorf("invalid session name: %w", err)
	}
	sessionDir := filepath.Join(s.BaseDir, sessionName)
	if err := validateExistingDirectoryPath(sessionDir, "session"); err != nil {
		return "", err
	}
	return sessionDir, nil
}

func validateCheckpointID(checkpointID string) error {
	if checkpointID == "" {
		return fmt.Errorf("checkpoint ID cannot be empty")
	}
	if strings.HasPrefix(checkpointID, ".") {
		return fmt.Errorf("invalid checkpoint ID: %q", checkpointID)
	}
	if strings.Contains(checkpointID, "..") || strings.ContainsAny(checkpointID, `/\`) {
		return fmt.Errorf("invalid checkpoint ID: %q", checkpointID)
	}
	if !checkpointIDRegex.MatchString(checkpointID) {
		return fmt.Errorf("invalid checkpoint ID: %q", checkpointID)
	}
	return nil
}

// IsValidCheckpointID reports whether checkpointID is valid for exact checkpoint lookup.
func IsValidCheckpointID(checkpointID string) bool {
	return validateCheckpointID(checkpointID) == nil
}

func safePathFallbackComponent(value string) string {
	safe := strings.Trim(sanitizeName(value), ".")
	if safe == "" {
		return "invalid"
	}
	return safe
}

func validateExistingDirectoryPath(path, kind string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s path: %w", kind, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s path must not be a symlink: %s", kind, path)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s path is not a directory: %s", kind, path)
	}
	return nil
}

func directoryLikeEntry(entry os.DirEntry) bool {
	return entry.IsDir() || entry.Type()&os.ModeSymlink != 0
}

func selectionCandidateEntry(entry os.DirEntry) bool {
	if entry.Name() == "incremental" {
		return false
	}
	return validateCheckpointID(entry.Name()) == nil
}

func (s *Storage) safeCheckpointDir(sessionName, checkpointID string) (string, error) {
	sessionDir, err := s.safeSessionDir(sessionName)
	if err != nil {
		return "", err
	}
	if err := validateCheckpointID(checkpointID); err != nil {
		return "", err
	}
	checkpointDir := filepath.Join(sessionDir, checkpointID)
	if err := validateExistingDirectoryPath(checkpointDir, "checkpoint"); err != nil {
		return "", err
	}
	return checkpointDir, nil
}

func (s *Storage) checkpointDirForLookup(sessionName, checkpointID string) (string, error) {
	if err := tmux.ValidateSessionName(sessionName); err != nil {
		return "", fmt.Errorf("invalid session name: %w", err)
	}
	if err := validateCheckpointID(checkpointID); err != nil {
		return "", err
	}
	return filepath.Join(s.BaseDir, sessionName, checkpointID), nil
}

func resolveCheckpointRelativePath(baseDir, relPath string) (string, error) {
	if strings.TrimSpace(relPath) == "" {
		return "", fmt.Errorf("relative path cannot be empty")
	}

	cleaned := filepath.Clean(relPath)
	if cleaned == "." {
		return "", fmt.Errorf("invalid relative path: %q", relPath)
	}

	fullPath := filepath.Join(baseDir, cleaned)
	relToBase, err := filepath.Rel(baseDir, fullPath)
	if err != nil {
		return "", fmt.Errorf("invalid relative path %q: %w", relPath, err)
	}
	if relToBase == ".." || strings.HasPrefix(relToBase, ".."+string(filepath.Separator)) || filepath.IsAbs(relToBase) {
		return "", fmt.Errorf("path escapes checkpoint directory: %s", relPath)
	}

	return fullPath, nil
}

func resolveExistingCheckpointArtifactPath(baseDir, relPath string) (string, error) {
	resolvedPath, err := isPathWithinDirResolved(baseDir, relPath)
	if err != nil {
		return "", fmt.Errorf("invalid artifact path %q: %w", relPath, err)
	}

	info, err := os.Lstat(resolvedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("artifact file does not exist: %s: %w", relPath, err)
		}
		return "", fmt.Errorf("stat artifact path %q: %w", relPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("artifact path must not be a symlink: %s", relPath)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("artifact path is not a regular file: %s", relPath)
	}

	return resolvedPath, nil
}

func resolveExistingCheckpointArtifactPathBestEffort(baseDir, relPath string) (string, bool) {
	resolvedPath, err := isPathWithinDirResolved(baseDir, relPath)
	if err != nil {
		return "", false
	}

	info, err := os.Lstat(resolvedPath)
	if err != nil {
		return "", false
	}
	if info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return resolvedPath, true
	}
	return "", false
}

// GitPatchPath returns the file path for the git patch.
func (s *Storage) GitPatchPath(sessionName, checkpointID string) string {
	return filepath.Join(s.CheckpointDir(sessionName, checkpointID), GitPatchFile)
}

// PanesDir returns the panes subdirectory for a checkpoint.
func (s *Storage) PanesDirPath(sessionName, checkpointID string) string {
	return filepath.Join(s.CheckpointDir(sessionName, checkpointID), PanesDir)
}

// GenerateID creates a unique checkpoint ID from timestamp and name.
func GenerateID(name string) string {
	// Use milliseconds + random suffix to prevent collisions
	now := time.Now()
	timestamp := now.Format("20060102-150405.000")

	// Add 4 random hex digits (pseudo-random based on time is sufficient here)
	// We don't need crypto/rand complexity for this, just collision avoidance
	randSuffix := now.UnixNano() % 0xffff
	id := fmt.Sprintf("%s-%04x", timestamp, randSuffix)

	// Sanitize name for filesystem safety
	safeName := sanitizeName(name)
	if safeName == "" {
		return id
	}
	return fmt.Sprintf("%s-%s", id, safeName)
}

// sanitizeName makes a name safe for use in file paths.
func sanitizeName(name string) string {
	// Replace unsafe characters
	replacer := strings.NewReplacer(
		"/", "-",
		"\\", "-",
		":", "-",
		"*", "-",
		"?", "-",
		"\"", "-",
		"<", "-",
		">", "-",
		"|", "-",
		"%", "_",
		" ", "_",
	)
	safe := replacer.Replace(strings.TrimSpace(name))

	// Limit length while respecting UTF-8 boundaries
	if len(safe) > 50 {
		// Find the last valid rune boundary within the limit
		for i := 50; i >= 0; i-- {
			if utf8.RuneStart(safe[i]) {
				// We found the start of the character that crosses or is at the boundary.
				// If i == 50, safe[:50] is valid (cut exactly before next char).
				// If i < 50, safe[:i] is valid (cut before the char that would exceed).
				return safe[:i]
			}
		}
		// Fallback for extremely weird cases (shouldn't happen with valid UTF-8 input)
		return safe[:50]
	}
	return safe
}

// Save writes a checkpoint to disk.
func (s *Storage) Save(cp *Checkpoint) error {
	dir, err := s.safeCheckpointDir(cp.SessionName, cp.ID)
	if err != nil {
		return err
	}

	// Create checkpoint directory
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating checkpoint directory: %w", err)
	}

	// Create panes directory
	panesDir := filepath.Join(dir, PanesDir)
	if err := os.MkdirAll(panesDir, 0755); err != nil {
		return fmt.Errorf("creating panes directory: %w", err)
	}

	currentArtifacts, err := resolveCheckpointArtifactPathSet(dir, cp)
	if err != nil {
		return fmt.Errorf("validating checkpoint artifact paths: %w", err)
	}

	var previousArtifacts map[string]struct{}
	if existing, err := s.Load(cp.SessionName, cp.ID); err == nil {
		previousArtifacts = resolveCheckpointArtifactPathSetBestEffort(dir, existing)
	}

	// Save metadata
	metaPath := filepath.Join(dir, MetadataFile)
	if err := writeJSON(metaPath, cp); err != nil {
		return fmt.Errorf("saving metadata: %w", err)
	}

	// Save session state separately for easy reading
	sessionPath := filepath.Join(dir, SessionFile)
	if err := writeJSON(sessionPath, cp.Session); err != nil {
		return fmt.Errorf("saving session state: %w", err)
	}

	if err := pruneCheckpointArtifactPaths(previousArtifacts, currentArtifacts); err != nil {
		return err
	}

	return nil
}

func resolveCheckpointArtifactPathSet(baseDir string, cp *Checkpoint) (map[string]struct{}, error) {
	paths := make(map[string]struct{})
	if cp == nil {
		return paths, nil
	}
	if err := validateCheckpointArtifactReferences(cp); err != nil {
		return nil, err
	}

	for _, pane := range cp.Session.Panes {
		if pane.ScrollbackFile == "" {
			continue
		}
		resolvedPath, err := resolveExistingCheckpointArtifactPath(baseDir, pane.ScrollbackFile)
		if err != nil {
			return nil, fmt.Errorf("invalid scrollback path %q: %w", pane.ScrollbackFile, err)
		}
		paths[resolvedPath] = struct{}{}
	}
	if cp.Git.PatchFile != "" {
		resolvedPath, err := resolveExistingCheckpointArtifactPath(baseDir, cp.Git.PatchFile)
		if err != nil {
			return nil, fmt.Errorf("invalid git patch path %q: %w", cp.Git.PatchFile, err)
		}
		paths[resolvedPath] = struct{}{}
	}
	if cp.Git.StatusFile != "" {
		resolvedPath, err := resolveExistingCheckpointArtifactPath(baseDir, cp.Git.StatusFile)
		if err != nil {
			return nil, fmt.Errorf("invalid git status path %q: %w", cp.Git.StatusFile, err)
		}
		paths[resolvedPath] = struct{}{}
	}

	return paths, nil
}

func resolveCheckpointArtifactPathSetBestEffort(baseDir string, cp *Checkpoint) map[string]struct{} {
	paths := make(map[string]struct{})
	if cp == nil {
		return paths
	}

	for _, pane := range cp.Session.Panes {
		if pane.ScrollbackFile == "" {
			continue
		}
		if resolvedPath, ok := resolveExistingCheckpointArtifactPathBestEffort(baseDir, pane.ScrollbackFile); ok {
			paths[resolvedPath] = struct{}{}
		}
	}
	if cp.Git.PatchFile != "" {
		if resolvedPath, ok := resolveExistingCheckpointArtifactPathBestEffort(baseDir, cp.Git.PatchFile); ok {
			paths[resolvedPath] = struct{}{}
		}
	}
	if cp.Git.StatusFile != "" {
		if resolvedPath, ok := resolveExistingCheckpointArtifactPathBestEffort(baseDir, cp.Git.StatusFile); ok {
			paths[resolvedPath] = struct{}{}
		}
	}

	return paths
}

func pruneCheckpointArtifactPaths(previous, current map[string]struct{}) error {
	if len(previous) == 0 {
		return nil
	}

	for path := range previous {
		if _, ok := current[path]; ok {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing stale checkpoint artifact %s: %w", path, err)
		}
	}

	return nil
}

// Load reads a checkpoint from disk.
func (s *Storage) Load(sessionName, checkpointID string) (*Checkpoint, error) {
	dir, err := s.safeCheckpointDir(sessionName, checkpointID)
	if err != nil {
		return nil, err
	}
	metaPath, err := resolveExistingCheckpointArtifactPath(dir, MetadataFile)
	if err != nil {
		return nil, fmt.Errorf("reading checkpoint metadata: %w", err)
	}

	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, fmt.Errorf("reading checkpoint metadata: %w", err)
	}

	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("parsing checkpoint metadata: %w", err)
	}
	if err := validateLoadedCheckpointMetadata(&cp, sessionName, checkpointID); err != nil {
		return nil, err
	}
	if err := loadCheckpointSessionState(dir, &cp); err != nil {
		return nil, err
	}
	if err := normalizeLoadedCheckpoint(&cp); err != nil {
		return nil, err
	}

	return &cp, nil
}

func loadCheckpointSessionState(dir string, cp *Checkpoint) error {
	sessionPath, err := resolveExistingCheckpointArtifactPath(dir, SessionFile)
	if err != nil {
		return fmt.Errorf("reading session state: %w", err)
	}

	data, err := os.ReadFile(sessionPath)
	if err != nil {
		return fmt.Errorf("reading session state: %w", err)
	}

	var session SessionState
	if err := json.Unmarshal(data, &session); err != nil {
		return fmt.Errorf("parsing session state: %w", err)
	}

	metadataJSON, err := json.Marshal(cp.Session)
	if err != nil {
		return fmt.Errorf("marshaling checkpoint session metadata: %w", err)
	}
	sessionJSON, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("marshaling checkpoint session state: %w", err)
	}
	if !bytes.Equal(metadataJSON, sessionJSON) {
		return fmt.Errorf("checkpoint session state mismatch between %s and %s", MetadataFile, SessionFile)
	}

	cp.Session = session
	return nil
}

func normalizeLoadedCheckpoint(cp *Checkpoint) error {
	actualPaneCount := len(cp.Session.Panes)
	if cp.PaneCount != 0 && cp.PaneCount != actualPaneCount {
		return fmt.Errorf("checkpoint pane count mismatch: metadata says %d, session contains %d panes", cp.PaneCount, actualPaneCount)
	}
	cp.PaneCount = actualPaneCount
	return nil
}

func validateLoadedCheckpointMetadata(cp *Checkpoint, sessionName, checkpointID string) error {
	if cp == nil {
		return fmt.Errorf("checkpoint metadata is nil")
	}
	if err := validateCheckpointID(cp.ID); err != nil {
		return fmt.Errorf("invalid checkpoint metadata: %w", err)
	}
	if err := tmux.ValidateSessionName(cp.SessionName); err != nil {
		return fmt.Errorf("invalid checkpoint metadata: invalid session name: %w", err)
	}
	if cp.ID != checkpointID {
		return fmt.Errorf("checkpoint metadata ID mismatch: expected %q, got %q", checkpointID, cp.ID)
	}
	if cp.SessionName != sessionName {
		return fmt.Errorf("checkpoint metadata session mismatch: expected %q, got %q", sessionName, cp.SessionName)
	}
	return nil
}

// List returns all checkpoints for a session, sorted by creation time (newest first).
func (s *Storage) List(sessionName string) ([]*Checkpoint, error) {
	sessionDir, err := s.safeSessionDir(sessionName)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No checkpoints yet
		}
		return nil, fmt.Errorf("reading session directory: %w", err)
	}

	var checkpoints []*Checkpoint
	for _, entry := range entries {
		if !directoryLikeEntry(entry) {
			continue
		}

		cp, err := s.Load(sessionName, entry.Name())
		if err != nil {
			// Skip invalid checkpoints
			continue
		}
		checkpoints = append(checkpoints, cp)
	}

	// Sort by creation time, newest first
	sort.Slice(checkpoints, func(i, j int) bool {
		return checkpointNewerFirst(checkpoints[i], checkpoints[j])
	})

	return checkpoints, nil
}

// ListAll returns all checkpoints across all sessions.
func (s *Storage) ListAll() ([]*Checkpoint, error) {
	entries, err := os.ReadDir(s.BaseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading checkpoints directory: %w", err)
	}

	var all []*Checkpoint
	for _, entry := range entries {
		if !directoryLikeEntry(entry) {
			continue
		}
		sessionCheckpoints, err := s.List(entry.Name())
		if err != nil {
			continue
		}
		all = append(all, sessionCheckpoints...)
	}

	// Sort by creation time, newest first
	sort.Slice(all, func(i, j int) bool {
		return checkpointNewerFirst(all[i], all[j])
	})

	return all, nil
}

func checkpointNewerFirst(a, b *Checkpoint) bool {
	if a.CreatedAt.Equal(b.CreatedAt) {
		if a.SessionName != b.SessionName {
			return a.SessionName < b.SessionName
		}
		return a.ID > b.ID
	}
	return a.CreatedAt.After(b.CreatedAt)
}

func (s *Storage) selectionEntries(sessionName string) ([]checkpointSelectionEntry, error) {
	sessionDir, err := s.safeSessionDir(sessionName)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading session directory: %w", err)
	}

	var candidates []checkpointSelectionEntry
	for _, entry := range entries {
		if !selectionCandidateEntry(entry) {
			continue
		}
		entryPath := filepath.Join(sessionDir, entry.Name())
		info, err := os.Lstat(entryPath)
		if err != nil {
			return nil, fmt.Errorf("stat checkpoint entry %s: %w", entry.Name(), err)
		}
		modTime := info.ModTime()
		candidates = append(candidates, checkpointSelectionEntry{
			name:     entry.Name(),
			modTime:  modTime,
			sortTime: s.selectionSortTime(sessionName, entry.Name(), modTime),
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].sortTime.Equal(candidates[j].sortTime) {
			return candidates[i].name > candidates[j].name
		}
		return candidates[i].sortTime.After(candidates[j].sortTime)
	})

	return candidates, nil
}

func (s *Storage) selectionSortTime(sessionName, checkpointID string, fallback time.Time) time.Time {
	dir, err := s.safeCheckpointDir(sessionName, checkpointID)
	if err != nil {
		return fallback
	}
	metaPath, err := resolveExistingCheckpointArtifactPath(dir, MetadataFile)
	if err != nil {
		return fallback
	}
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return fallback
	}

	var meta struct {
		ID          string    `json:"id"`
		SessionName string    `json:"session_name"`
		CreatedAt   time.Time `json:"created_at"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return fallback
	}
	if meta.ID != checkpointID || meta.SessionName != sessionName || meta.CreatedAt.IsZero() {
		return fallback
	}
	return meta.CreatedAt
}

// HasCheckpointCandidates reports whether a session has any checkpoint-shaped
// on-disk entries, even if they are corrupted and would fail to load.
func (s *Storage) HasCheckpointCandidates(sessionName string) (bool, error) {
	candidates, err := s.selectionEntries(sessionName)
	if err != nil {
		return false, err
	}
	return len(candidates) > 0, nil
}

// InvalidCheckpointIDs reports checkpoint-shaped entries that exist on disk but
// cannot be loaded successfully.
func (s *Storage) InvalidCheckpointIDs(sessionName string) ([]string, error) {
	candidates, err := s.selectionEntries(sessionName)
	if err != nil {
		return nil, err
	}

	var invalid []string
	for _, candidate := range candidates {
		if _, err := s.Load(sessionName, candidate.name); err != nil {
			invalid = append(invalid, candidate.name)
		}
	}

	return invalid, nil
}

func (s *Storage) getByRecentIndex(sessionName string, index int) (*Checkpoint, error) {
	if index < 1 {
		return nil, fmt.Errorf("checkpoint index %d out of range", index)
	}

	candidates, err := s.selectionEntries(sessionName)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("%w for session: %s", ErrNoCheckpoints, sessionName)
	}

	validSeen := 0
	for _, candidate := range candidates {
		cp, err := s.Load(sessionName, candidate.name)
		if err != nil {
			if validSeen < index {
				return nil, fmt.Errorf("checkpoint selection blocked by invalid checkpoint %q: %w", candidate.name, err)
			}
			continue
		}
		validSeen++
		if validSeen == index {
			return cp, nil
		}
	}

	return nil, fmt.Errorf("checkpoint index %d out of range (1-%d valid checkpoints)", index, validSeen)
}

func (s *Storage) deleteCheckpointPath(sessionName, checkpointID string) error {
	if err := tmux.ValidateSessionName(sessionName); err != nil {
		return fmt.Errorf("invalid session name: %w", err)
	}
	if err := validateCheckpointID(checkpointID); err != nil {
		return err
	}

	sessionDir := filepath.Join(s.BaseDir, sessionName)
	info, err := os.Lstat(sessionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat session path: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("session path must not be a symlink: %s", sessionDir)
	}
	if !info.IsDir() {
		return fmt.Errorf("session path is not a directory: %s", sessionDir)
	}

	return os.RemoveAll(filepath.Join(sessionDir, checkpointID))
}

// Delete removes a checkpoint from disk.
func (s *Storage) Delete(sessionName, checkpointID string) error {
	return s.deleteCheckpointPath(sessionName, checkpointID)
}

// GetLatest returns the most recent checkpoint for a session.
func (s *Storage) GetLatest(sessionName string) (*Checkpoint, error) {
	return s.getByRecentIndex(sessionName, 1)
}

// SaveScrollback writes pane scrollback to a file.
func (s *Storage) SaveScrollback(sessionName, checkpointID string, paneID string, content string) (string, error) {
	dir, err := s.safeCheckpointDir(sessionName, checkpointID)
	if err != nil {
		return "", err
	}
	panesDir := filepath.Join(dir, PanesDir)
	if err := os.MkdirAll(panesDir, 0755); err != nil {
		return "", fmt.Errorf("creating panes directory: %w", err)
	}

	// Use sanitized pane ID for filename to handle % and other chars
	filename := fmt.Sprintf("pane_%s.txt", sanitizeName(paneID))
	fullPath := filepath.Join(panesDir, filename)

	if err := util.AtomicWriteFile(fullPath, []byte(content), 0600); err != nil {
		return "", fmt.Errorf("saving scrollback: %w", err)
	}

	return filepath.Join(PanesDir, filename), nil
}

// LoadScrollback reads pane scrollback from a file.
func (s *Storage) LoadScrollback(sessionName, checkpointID string, paneID string) (string, error) {
	filename := fmt.Sprintf("pane_%s.txt", sanitizeName(paneID))
	dir, err := s.safeCheckpointDir(sessionName, checkpointID)
	if err != nil {
		return "", err
	}
	fullPath, err := resolveExistingCheckpointArtifactPath(dir, filepath.Join(PanesDir, filename))
	if err != nil {
		return "", fmt.Errorf("resolving scrollback path: %w", err)
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		return "", fmt.Errorf("reading scrollback: %w", err)
	}

	return string(data), nil
}

// SaveGitPatch writes the git diff patch to the checkpoint.
func (s *Storage) SaveGitPatch(sessionName, checkpointID, patch string) error {
	if patch == "" {
		return nil
	}
	dir, err := s.safeCheckpointDir(sessionName, checkpointID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating checkpoint directory: %w", err)
	}
	path := filepath.Join(dir, GitPatchFile)
	return util.AtomicWriteFile(path, []byte(patch), 0600)
}

func (s *Storage) loadGitArtifact(sessionName, checkpointID, relPath, defaultName, kind string) (string, error) {
	dir, err := s.safeCheckpointDir(sessionName, checkpointID)
	if err != nil {
		return "", err
	}

	target := defaultName
	if relPath != "" {
		target = relPath
	}

	path, err := resolveExistingCheckpointArtifactPath(dir, target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && relPath == "" {
			return "", nil
		}
		return "", fmt.Errorf("resolving %s path: %w", kind, err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && relPath == "" {
			return "", nil
		}
		return "", fmt.Errorf("reading %s: %w", kind, err)
	}

	return string(data), nil
}

func (s *Storage) loadGitPatchForState(sessionName, checkpointID string, git GitState) (string, error) {
	return s.loadGitArtifact(sessionName, checkpointID, git.PatchFile, GitPatchFile, "git patch")
}

func (s *Storage) loadGitStatusForState(sessionName, checkpointID string, git GitState) (string, error) {
	return s.loadGitArtifact(sessionName, checkpointID, git.StatusFile, GitStatusFile, "git status")
}

// LoadGitPatch reads the git diff patch from the checkpoint.
func (s *Storage) LoadGitPatch(sessionName, checkpointID string) (string, error) {
	dir, err := s.safeCheckpointDir(sessionName, checkpointID)
	if err != nil {
		return "", err
	}
	if checkpointStateExists(dir) {
		cp, err := s.Load(sessionName, checkpointID)
		if err != nil {
			return "", err
		}
		return s.loadGitPatchForState(sessionName, checkpointID, cp.Git)
	}
	return s.loadGitArtifact(sessionName, checkpointID, "", GitPatchFile, "git patch")
}

// LoadGitStatus reads the git status text from the checkpoint.
func (s *Storage) LoadGitStatus(sessionName, checkpointID string) (string, error) {
	dir, err := s.safeCheckpointDir(sessionName, checkpointID)
	if err != nil {
		return "", err
	}
	if checkpointStateExists(dir) {
		cp, err := s.Load(sessionName, checkpointID)
		if err != nil {
			return "", err
		}
		return s.loadGitStatusForState(sessionName, checkpointID, cp.Git)
	}
	return s.loadGitArtifact(sessionName, checkpointID, "", GitStatusFile, "git status")
}

// SaveGitStatus writes the git status output to the checkpoint.
func (s *Storage) SaveGitStatus(sessionName, checkpointID, status string) error {
	dir, err := s.safeCheckpointDir(sessionName, checkpointID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating checkpoint directory: %w", err)
	}
	path := filepath.Join(dir, GitStatusFile)
	return util.AtomicWriteFile(path, []byte(status), 0600)
}

func checkpointStateExists(dir string) bool {
	for _, name := range []string{MetadataFile, SessionFile} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err == nil {
			return true
		}
		if !os.IsNotExist(err) && info != nil {
			return true
		}
	}
	return false
}

// writeJSON writes data as formatted JSON to a file atomically.
func writeJSON(path string, data interface{}) error {
	bytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}

	return util.AtomicWriteFile(path, bytes, 0600)
}

// Exists returns true if a checkpoint exists.
func (s *Storage) Exists(sessionName, checkpointID string) bool {
	dir, err := s.safeCheckpointDir(sessionName, checkpointID)
	if err != nil {
		return false
	}
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// HasCheckpointPath reports whether a checkpoint path exists at all, even if the
// on-disk entry is corrupted or symlink-backed and would later fail validation.
func (s *Storage) HasCheckpointPath(sessionName, checkpointID string) (bool, error) {
	dir, err := s.checkpointDirForLookup(sessionName, checkpointID)
	if err != nil {
		return false, err
	}
	_, err = os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat checkpoint path: %w", err)
	}
	return true, nil
}
