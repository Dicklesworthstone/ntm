// Package ensemble provides checkpoint storage for partial synthesis recovery.
// Checkpoints allow resuming ensemble runs after failures or interruptions.
package ensemble

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	// checkpointDirName is the directory name for checkpoint storage.
	checkpointDirName = "ensemble-checkpoints"
	// checkpointMetaFile is the metadata filename within a checkpoint.
	checkpointMetaFile = "_meta.json"
	// checkpointSynthesisFile stores streaming synthesis resume state.
	checkpointSynthesisFile = "synthesis.json"
)

// NormalizeCheckpointRunID trims and validates a run ID before it is used as a
// filesystem path segment.
func NormalizeCheckpointRunID(runID string) (string, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return "", errors.New("run ID is required")
	}
	if filepath.IsAbs(runID) || runID == "." || runID == ".." {
		return "", fmt.Errorf("invalid run ID %q", runID)
	}
	if strings.Contains(runID, "/") || strings.Contains(runID, "\\") || strings.ContainsRune(runID, 0) {
		return "", fmt.Errorf("invalid run ID %q", runID)
	}
	if filepath.Clean(runID) != runID {
		return "", fmt.Errorf("invalid run ID %q", runID)
	}
	return runID, nil
}

func normalizeCheckpointModeID(modeID string) (string, error) {
	modeID = strings.TrimSpace(modeID)
	if modeID == "" {
		return "", errors.New("mode ID is required")
	}
	if filepath.IsAbs(modeID) || modeID == "." || modeID == ".." {
		return "", fmt.Errorf("invalid mode ID %q", modeID)
	}
	if strings.Contains(modeID, "/") || strings.Contains(modeID, "\\") || strings.ContainsRune(modeID, 0) {
		return "", fmt.Errorf("invalid mode ID %q", modeID)
	}
	if filepath.Clean(modeID) != modeID {
		return "", fmt.Errorf("invalid mode ID %q", modeID)
	}
	return modeID, nil
}

func validateExistingCheckpointDirectory(path, kind string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return os.ErrNotExist
		}
		return fmt.Errorf("stat %s: %w", kind, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s must not be a symlink: %s", kind, path)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory: %s", kind, path)
	}
	return nil
}

func readRegularCheckpointFile(path, kind string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("stat %s: %w", kind, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s must not be a symlink: %s", kind, path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file: %s", kind, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("read %s: %w", kind, err)
	}
	return data, nil
}

// CheckpointMetadata holds metadata about a checkpoint.
type CheckpointMetadata struct {
	SessionName  string         `json:"session_name"`
	Question     string         `json:"question"`
	RunID        string         `json:"run_id"`
	Status       EnsembleStatus `json:"status"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	ContextHash  string         `json:"context_hash,omitempty"`
	CompletedIDs []string       `json:"completed_ids"`
	PendingIDs   []string       `json:"pending_ids"`
	ErrorIDs     []string       `json:"error_ids,omitempty"`
	TotalModes   int            `json:"total_modes"`
}

// SynthesisCheckpoint tracks streaming synthesis progress for resume.
type SynthesisCheckpoint struct {
	RunID       string    `json:"run_id"`
	SessionName string    `json:"session_name,omitempty"`
	LastIndex   int       `json:"last_index"`
	Error       string    `json:"error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ModeCheckpoint stores a single mode's output for recovery.
type ModeCheckpoint struct {
	ModeID      string      `json:"mode_id"`
	Output      *ModeOutput `json:"output,omitempty"`
	Status      string      `json:"status"`
	CapturedAt  time.Time   `json:"captured_at"`
	ContextHash string      `json:"context_hash,omitempty"`
	TokensUsed  int         `json:"tokens_used,omitempty"`
	Error       string      `json:"error,omitempty"`
}

// CheckpointStore manages checkpoint persistence.
type CheckpointStore struct {
	baseDir string
	logger  *slog.Logger
}

// NewCheckpointStore creates a checkpoint store in the given base directory.
// If baseDir is empty, uses the current working directory's .ntm folder.
func NewCheckpointStore(baseDir string) (*CheckpointStore, error) {
	if baseDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("get working directory: %w", err)
		}
		baseDir = filepath.Join(cwd, ".ntm")
	}

	checkpointDir := filepath.Join(baseDir, checkpointDirName)
	if err := os.MkdirAll(checkpointDir, 0o755); err != nil {
		return nil, fmt.Errorf("create checkpoint directory: %w", err)
	}

	return &CheckpointStore{
		baseDir: checkpointDir,
		logger:  slog.Default(),
	}, nil
}

func (s *CheckpointStore) ensureRunDir(runID string) (string, error) {
	runDir := filepath.Join(s.baseDir, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return "", fmt.Errorf("create run directory: %w", err)
	}
	if err := validateExistingCheckpointDirectory(runDir, "checkpoint run path"); err != nil {
		return "", err
	}
	return runDir, nil
}

func (s *CheckpointStore) safeRunDir(runID string) (string, error) {
	runDir := filepath.Join(s.baseDir, runID)
	if err := validateExistingCheckpointDirectory(runDir, "checkpoint run path"); err != nil {
		return "", err
	}
	return runDir, nil
}

// SaveMetadata saves or updates the checkpoint metadata.
func (s *CheckpointStore) SaveMetadata(meta CheckpointMetadata) error {
	if s == nil {
		return errors.New("checkpoint store is nil")
	}
	normalizedRunID, err := NormalizeCheckpointRunID(meta.RunID)
	if err != nil {
		return err
	}
	meta.RunID = normalizedRunID

	runDir, err := s.ensureRunDir(meta.RunID)
	if err != nil {
		return err
	}

	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = time.Now().UTC()
	}
	meta.UpdatedAt = time.Now().UTC()

	filename := filepath.Join(runDir, checkpointMetaFile)
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	if err := util.AtomicWriteFile(filename, data, 0o644); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}

	s.logger.Info("checkpoint metadata saved",
		"run_id", meta.RunID,
		"session", meta.SessionName,
		"completed", len(meta.CompletedIDs),
		"pending", len(meta.PendingIDs),
	)

	return nil
}

// SaveSynthesisCheckpoint saves streaming synthesis resume state.
func (s *CheckpointStore) SaveSynthesisCheckpoint(runID string, checkpoint SynthesisCheckpoint) error {
	if s == nil {
		return errors.New("checkpoint store is nil")
	}
	normalizedRunID, err := NormalizeCheckpointRunID(runID)
	if err != nil {
		return err
	}
	runID = normalizedRunID

	runDir, err := s.ensureRunDir(runID)
	if err != nil {
		return err
	}

	if checkpoint.CreatedAt.IsZero() {
		checkpoint.CreatedAt = time.Now().UTC()
	}
	checkpoint.UpdatedAt = time.Now().UTC()
	checkpoint.RunID = runID

	filename := filepath.Join(runDir, checkpointSynthesisFile)
	data, err := json.MarshalIndent(checkpoint, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal synthesis checkpoint: %w", err)
	}

	if err := util.AtomicWriteFile(filename, data, 0o644); err != nil {
		return fmt.Errorf("write synthesis checkpoint: %w", err)
	}

	s.logger.Info("synthesis checkpoint saved",
		"run_id", runID,
		"last_index", checkpoint.LastIndex,
	)

	return nil
}

// LoadSynthesisCheckpoint loads streaming synthesis resume state.
func (s *CheckpointStore) LoadSynthesisCheckpoint(runID string) (*SynthesisCheckpoint, error) {
	if s == nil {
		return nil, errors.New("checkpoint store is nil")
	}
	normalizedRunID, err := NormalizeCheckpointRunID(runID)
	if err != nil {
		return nil, err
	}
	runID = normalizedRunID

	runDir, err := s.safeRunDir(runID)
	if err != nil {
		return nil, err
	}

	filename := filepath.Join(runDir, checkpointSynthesisFile)
	data, err := readRegularCheckpointFile(filename, "synthesis checkpoint file")
	if err != nil {
		if os.IsNotExist(err) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}

	var checkpoint SynthesisCheckpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return nil, fmt.Errorf("unmarshal synthesis checkpoint: %w", err)
	}
	if checkpoint.RunID != runID {
		return nil, fmt.Errorf("synthesis checkpoint run ID mismatch: got %q, want %q", checkpoint.RunID, runID)
	}

	return &checkpoint, nil
}

// LoadCheckpoint loads a specific mode's checkpoint.
func (s *CheckpointStore) LoadCheckpoint(runID, modeID string) (*ModeCheckpoint, error) {
	if s == nil {
		return nil, errors.New("checkpoint store is nil")
	}
	normalizedRunID, err := NormalizeCheckpointRunID(runID)
	if err != nil {
		return nil, err
	}
	normalizedModeID, err := normalizeCheckpointModeID(modeID)
	if err != nil {
		return nil, err
	}
	runID = normalizedRunID
	modeID = normalizedModeID

	runDir, err := s.safeRunDir(runID)
	if err != nil {
		return nil, err
	}

	filename := filepath.Join(runDir, modeID+".json")
	data, err := readRegularCheckpointFile(filename, "checkpoint file")
	if err != nil {
		if os.IsNotExist(err) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}

	var checkpoint ModeCheckpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return nil, fmt.Errorf("unmarshal checkpoint: %w", err)
	}
	if checkpoint.ModeID != modeID {
		return nil, fmt.Errorf("checkpoint mode ID mismatch: got %q, want %q", checkpoint.ModeID, modeID)
	}
	if checkpoint.Output != nil && checkpoint.Output.ModeID != modeID {
		return nil, fmt.Errorf("checkpoint output mode ID mismatch: got %q, want %q", checkpoint.Output.ModeID, modeID)
	}

	return &checkpoint, nil
}

// LoadMetadata loads the checkpoint metadata for a run.
func (s *CheckpointStore) LoadMetadata(runID string) (*CheckpointMetadata, error) {
	if s == nil {
		return nil, errors.New("checkpoint store is nil")
	}
	normalizedRunID, err := NormalizeCheckpointRunID(runID)
	if err != nil {
		return nil, err
	}
	runID = normalizedRunID

	runDir, err := s.safeRunDir(runID)
	if err != nil {
		return nil, err
	}

	filename := filepath.Join(runDir, checkpointMetaFile)
	data, err := readRegularCheckpointFile(filename, "metadata file")
	if err != nil {
		if os.IsNotExist(err) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}

	var meta CheckpointMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("unmarshal metadata: %w", err)
	}
	if meta.RunID != runID {
		return nil, fmt.Errorf("metadata run ID mismatch: got %q, want %q", meta.RunID, runID)
	}

	return &meta, nil
}

// LoadAllCheckpoints loads all mode checkpoints for a run.
func (s *CheckpointStore) LoadAllCheckpoints(runID string) ([]ModeCheckpoint, error) {
	if s == nil {
		return nil, errors.New("checkpoint store is nil")
	}
	normalizedRunID, err := NormalizeCheckpointRunID(runID)
	if err != nil {
		return nil, err
	}
	runID = normalizedRunID

	runDir, err := s.safeRunDir(runID)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(runDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("read checkpoint directory: %w", err)
	}

	var checkpoints []ModeCheckpoint
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		// Skip metadata and synthesis checkpoint files
		if entry.Name() == checkpointMetaFile || entry.Name() == checkpointSynthesisFile {
			continue
		}

		modeID := strings.TrimSuffix(entry.Name(), ".json")
		checkpoint, err := s.LoadCheckpoint(runID, modeID)
		if err != nil {
			return nil, fmt.Errorf("load checkpoint %q: %w", modeID, err)
		}
		checkpoints = append(checkpoints, *checkpoint)
	}

	return checkpoints, nil
}

// ListRuns returns all available checkpoint run IDs.
func (s *CheckpointStore) ListRuns() ([]CheckpointMetadata, error) {
	if s == nil {
		return nil, errors.New("checkpoint store is nil")
	}

	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read checkpoint directory: %w", err)
	}

	var runs []CheckpointMetadata
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		runID := entry.Name()
		meta, err := s.LoadMetadata(runID)
		if err != nil {
			// Create minimal metadata from directory
			info, infoErr := entry.Info()
			if infoErr != nil || info == nil {
				meta = &CheckpointMetadata{
					RunID: runID,
				}
			} else {
				meta = &CheckpointMetadata{
					RunID:     runID,
					CreatedAt: info.ModTime(),
				}
			}
		}
		runs = append(runs, *meta)
	}

	// Sort by creation time, newest first
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].CreatedAt.After(runs[j].CreatedAt)
	})

	return runs, nil
}

// DeleteRun removes all checkpoints for a run.
func (s *CheckpointStore) DeleteRun(runID string) error {
	if s == nil {
		return errors.New("checkpoint store is nil")
	}
	normalizedRunID, err := NormalizeCheckpointRunID(runID)
	if err != nil {
		return err
	}
	runID = normalizedRunID

	runDir := filepath.Join(s.baseDir, runID)
	if err := os.RemoveAll(runDir); err != nil {
		return fmt.Errorf("remove checkpoint directory: %w", err)
	}

	s.logger.Info("checkpoint deleted", "run_id", runID)
	return nil
}

// CleanOld removes checkpoints older than the given duration.
func (s *CheckpointStore) CleanOld(maxAge time.Duration) (int, error) {
	if s == nil {
		return 0, errors.New("checkpoint store is nil")
	}

	runs, err := s.ListRuns()
	if err != nil {
		return 0, err
	}

	cutoff := time.Now().Add(-maxAge)
	removed := 0

	for _, run := range runs {
		if run.UpdatedAt.Before(cutoff) || (run.UpdatedAt.IsZero() && run.CreatedAt.Before(cutoff)) {
			if err := s.DeleteRun(run.RunID); err != nil {
				s.logger.Warn("failed to delete old checkpoint",
					"run_id", run.RunID,
					"error", err,
				)
				continue
			}
			removed++
		}
	}

	s.logger.Info("old checkpoints cleaned", "removed", removed, "max_age", maxAge)
	return removed, nil
}

// RunExists checks if a checkpoint run exists.
func (s *CheckpointStore) RunExists(runID string) bool {
	if s == nil {
		return false
	}
	normalizedRunID, err := NormalizeCheckpointRunID(runID)
	if err != nil {
		return false
	}
	runID = normalizedRunID
	_, err = s.safeRunDir(runID)
	return err == nil
}

// GetCompletedOutputs returns all successfully completed mode outputs for a run.
func (s *CheckpointStore) GetCompletedOutputs(runID string) ([]*ModeOutput, error) {
	checkpoints, err := s.LoadAllCheckpoints(runID)
	if err != nil {
		return nil, err
	}

	var outputs []*ModeOutput
	for _, cp := range checkpoints {
		if cp.Status == string(AssignmentDone) && cp.Output != nil {
			outputs = append(outputs, cp.Output)
		}
	}

	return outputs, nil
}
