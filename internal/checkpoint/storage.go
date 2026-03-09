package checkpoint

import (
	"encoding/json"
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

// Storage manages checkpoint storage on disk.
type Storage struct {
	// BaseDir is the base directory for all checkpoints
	BaseDir string
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
	return filepath.Join(s.BaseDir, sessionName), nil
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

func safePathFallbackComponent(value string) string {
	safe := strings.Trim(sanitizeName(value), ".")
	if safe == "" {
		return "invalid"
	}
	return safe
}

func (s *Storage) safeCheckpointDir(sessionName, checkpointID string) (string, error) {
	sessionDir, err := s.safeSessionDir(sessionName)
	if err != nil {
		return "", err
	}
	if err := validateCheckpointID(checkpointID); err != nil {
		return "", err
	}
	return filepath.Join(sessionDir, checkpointID), nil
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

	return nil
}

// Load reads a checkpoint from disk.
func (s *Storage) Load(sessionName, checkpointID string) (*Checkpoint, error) {
	dir, err := s.safeCheckpointDir(sessionName, checkpointID)
	if err != nil {
		return nil, err
	}
	metaPath := filepath.Join(dir, MetadataFile)

	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, fmt.Errorf("reading checkpoint metadata: %w", err)
	}

	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("parsing checkpoint metadata: %w", err)
	}

	return &cp, nil
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
		if !entry.IsDir() {
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
		return checkpoints[i].CreatedAt.After(checkpoints[j].CreatedAt)
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
		if !entry.IsDir() {
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
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})

	return all, nil
}

// Delete removes a checkpoint from disk.
func (s *Storage) Delete(sessionName, checkpointID string) error {
	dir, err := s.safeCheckpointDir(sessionName, checkpointID)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// GetLatest returns the most recent checkpoint for a session.
func (s *Storage) GetLatest(sessionName string) (*Checkpoint, error) {
	checkpoints, err := s.List(sessionName)
	if err != nil {
		return nil, err
	}
	if len(checkpoints) == 0 {
		return nil, fmt.Errorf("no checkpoints found for session: %s", sessionName)
	}
	return checkpoints[0], nil
}

// SaveScrollback writes pane scrollback to a file.
func (s *Storage) SaveScrollback(sessionName, checkpointID string, paneID string, content string) (string, error) {
	panesDir := s.PanesDirPath(sessionName, checkpointID)
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
	fullPath := filepath.Join(s.PanesDirPath(sessionName, checkpointID), filename)

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
	path := filepath.Join(dir, GitPatchFile)
	return util.AtomicWriteFile(path, []byte(patch), 0600)
}

// LoadGitPatch reads the git diff patch from the checkpoint.
func (s *Storage) LoadGitPatch(sessionName, checkpointID string) (string, error) {
	dir, err := s.safeCheckpointDir(sessionName, checkpointID)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, GitPatchFile)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading git patch: %w", err)
	}

	return string(data), nil
}

// SaveGitStatus writes the git status output to the checkpoint.
func (s *Storage) SaveGitStatus(sessionName, checkpointID, status string) error {
	dir, err := s.safeCheckpointDir(sessionName, checkpointID)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, GitStatusFile)
	return util.AtomicWriteFile(path, []byte(status), 0600)
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
