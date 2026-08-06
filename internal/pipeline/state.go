package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	pipelineStateDirName = "pipelines"

	// PipelineStateSchemaVersion is the on-disk schema version for persisted
	// pipeline execution state files.
	PipelineStateSchemaVersion = 1
)

func pipelineStateDir(projectDir string) string {
	return filepath.Join(projectDir, ".ntm", pipelineStateDirName)
}

// validateRunID rejects run IDs that would escape the pipeline state
// directory. Generated run IDs are basename-shaped (e.g.
// "run-20260507-143015-abcd1234"); CLI/from-state inputs and any injected
// ExecutorConfig.RunID flow through here before they touch disk so a
// hostile or malformed value cannot reach LoadState/SaveState. Rejects
// path separators (`/`, `\\`), NUL bytes, and the special parents
// "." / "..", and is intentionally stricter than filepath.Clean so a
// platform-specific separator quirk cannot smuggle a traversal through.
func validateRunID(runID string) error {
	if runID == "" {
		return fmt.Errorf("run id is required")
	}
	if runID == "." || runID == ".." {
		return fmt.Errorf("invalid run id %q: reserved path component", runID)
	}
	if strings.ContainsAny(runID, "/\\\x00") {
		return fmt.Errorf("invalid run id %q: must not contain path separators or NUL", runID)
	}
	return nil
}

func pipelineStatePath(projectDir, runID string) string {
	return filepath.Join(pipelineStateDir(projectDir), fmt.Sprintf("%s.json", runID))
}

// SaveState persists the execution state to .ntm/pipelines/<run-id>.json.
func SaveState(projectDir string, state *ExecutionState) error {
	if state == nil {
		return fmt.Errorf("state is nil")
	}
	if err := validateRunID(state.RunID); err != nil {
		return err
	}

	dir := pipelineStateDir(projectDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create pipeline state dir: %w", err)
	}

	data, err := marshalStateForDisk(state)
	if err != nil {
		return fmt.Errorf("marshal pipeline state: %w", err)
	}

	path := pipelineStatePath(projectDir, state.RunID)

	if err := util.AtomicWriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write pipeline state: %w", err)
	}

	return nil
}

// LoadState loads execution state from .ntm/pipelines/<run-id>.json.
func LoadState(projectDir, runID string) (*ExecutionState, error) {
	if err := validateRunID(runID); err != nil {
		return nil, err
	}

	path := pipelineStatePath(projectDir, runID)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read pipeline state: %w", err)
	}

	var header struct {
		StateSchemaVersion int `json:"state_schema_version,omitempty"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, fmt.Errorf("parse pipeline state header: %w", err)
	}
	if header.StateSchemaVersion > PipelineStateSchemaVersion {
		return nil, fmt.Errorf("pipeline state schema version %d is newer than supported version %d", header.StateSchemaVersion, PipelineStateSchemaVersion)
	}

	var state ExecutionState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse pipeline state: %w", err)
	}

	if state.RunID == "" {
		state.RunID = runID
	} else if state.RunID != runID {
		return nil, fmt.Errorf("pipeline state run id %q does not match requested run id %q", state.RunID, runID)
	}

	return &state, nil
}

func marshalStateForDisk(state *ExecutionState) ([]byte, error) {
	data, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}

	version, err := json.Marshal(PipelineStateSchemaVersion)
	if err != nil {
		return nil, err
	}
	doc["state_schema_version"] = version

	return json.MarshalIndent(doc, "", "  ")
}

// CleanupStates removes pipeline state files older than the provided duration.
// Returns the number of deleted state files.
func CleanupStates(projectDir string, olderThan time.Duration) (int, error) {
	if olderThan <= 0 {
		return 0, fmt.Errorf("olderThan must be greater than zero")
	}

	dir := pipelineStateDir(projectDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("read pipeline state dir: %w", err)
	}

	cutoff := time.Now().Add(-olderThan)
	deleted := 0

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.ModTime().Before(cutoff) {
			path := filepath.Join(dir, entry.Name())
			if err := os.Remove(path); err != nil {
				return deleted, fmt.Errorf("remove pipeline state: %w", err)
			}
			deleted++
		}
	}

	return deleted, nil
}
