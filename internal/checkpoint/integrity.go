package checkpoint

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// CurrentVersion is the current checkpoint format version.
const CurrentVersion = 1

// MinVersion is the minimum supported checkpoint format version.
const MinVersion = 1

// IntegrityResult contains the results of checkpoint verification.
type IntegrityResult struct {
	// Valid is true if all checks passed.
	Valid bool `json:"valid"`

	// SchemaValid indicates if the schema is valid.
	SchemaValid bool `json:"schema_valid"`
	// FilesPresent indicates if all referenced files exist.
	FilesPresent bool `json:"files_present"`
	// ConsistencyValid indicates if internal consistency checks pass.
	ConsistencyValid bool `json:"consistency_valid"`

	// Errors contains any validation errors.
	Errors []string `json:"errors,omitempty"`
	// Warnings contains non-fatal issues.
	Warnings []string `json:"warnings,omitempty"`
	// Details contains detailed check results.
	Details map[string]string `json:"details,omitempty"`
}

func newIntegrityResult() *IntegrityResult {
	return &IntegrityResult{
		Valid:            true,
		SchemaValid:      true,
		FilesPresent:     true,
		ConsistencyValid: true,
		Errors:           []string{},
		Warnings:         []string{},
		Details:          make(map[string]string),
	}
}

func formatArtifactCheckError(name string, err error) string {
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Sprintf("missing %s", name)
	}
	return fmt.Sprintf("invalid %s: %v", name, err)
}

func (c *Checkpoint) verifyWithDir(storage *Storage, dir string) *IntegrityResult {
	result := newIntegrityResult()

	c.validateSchema(result)
	c.checkFiles(storage, dir, result)
	c.checkArtifactChecksums(dir, result)
	c.validateConsistency(result)

	result.Valid = result.SchemaValid && result.FilesPresent && result.ConsistencyValid
	return result
}

// VerifyStoredCheckpoint verifies a checkpoint from disk without requiring it
// to be fully loadable through Storage.Load.
func VerifyStoredCheckpoint(storage *Storage, sessionName, checkpointID string) *IntegrityResult {
	result := newIntegrityResult()
	result.Details["requested_session"] = sessionName
	result.Details["requested_id"] = checkpointID

	dir, err := storage.safeCheckpointDir(sessionName, checkpointID)
	if err != nil {
		result.FilesPresent = false
		result.Errors = append(result.Errors, err.Error())
		result.Valid = false
		return result
	}

	metaPath, err := resolveExistingCheckpointArtifactPath(dir, MetadataFile)
	if err != nil {
		result.FilesPresent = false
		result.Errors = append(result.Errors, formatArtifactCheckError(MetadataFile, err))
		if _, sessionErr := resolveExistingCheckpointArtifactPath(dir, SessionFile); sessionErr != nil {
			result.FilesPresent = false
			result.Errors = append(result.Errors, formatArtifactCheckError(SessionFile, sessionErr))
		}
		result.Valid = false
		return result
	}

	data, err := os.ReadFile(metaPath)
	if err != nil {
		result.FilesPresent = false
		result.Errors = append(result.Errors, fmt.Sprintf("reading %s: %v", MetadataFile, err))
		result.Valid = false
		return result
	}
	data, err = decryptCheckpointBytes(data)
	if err != nil {
		result.SchemaValid = false
		result.Errors = append(result.Errors, fmt.Sprintf("reading %s: %v", MetadataFile, err))
		result.Valid = false
		return result
	}

	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		result.SchemaValid = false
		result.Errors = append(result.Errors, fmt.Sprintf("parsing %s: %v", MetadataFile, err))
		if _, sessionErr := resolveExistingCheckpointArtifactPath(dir, SessionFile); sessionErr != nil {
			result.FilesPresent = false
			result.Errors = append(result.Errors, formatArtifactCheckError(SessionFile, sessionErr))
		}
		result.Valid = false
		return result
	}

	result = cp.verifyWithDir(storage, dir)
	result.Details["requested_session"] = sessionName
	result.Details["requested_id"] = checkpointID
	if err := validateLoadedCheckpointMetadata(&cp, sessionName, checkpointID); err != nil {
		result.SchemaValid = false
		result.Valid = false
		result.Errors = append([]string{err.Error()}, result.Errors...)
	}
	return result
}

// validateSchema checks that all required fields are present and valid.
func (c *Checkpoint) validateSchema(result *IntegrityResult) {
	// Check version
	if c.Version < MinVersion || c.Version > CurrentVersion {
		result.SchemaValid = false
		result.Errors = append(result.Errors, fmt.Sprintf("unsupported version: %d (expected %d-%d)", c.Version, MinVersion, CurrentVersion))
	}

	// Check required fields
	if c.ID == "" {
		result.SchemaValid = false
		result.Errors = append(result.Errors, "missing checkpoint ID")
	}

	if c.SessionName == "" {
		result.SchemaValid = false
		result.Errors = append(result.Errors, "missing session_name")
	}

	if c.CreatedAt.IsZero() {
		result.SchemaValid = false
		result.Errors = append(result.Errors, "missing or invalid created_at timestamp")
	}

	// Optional warnings
	if c.Name == "" {
		result.Warnings = append(result.Warnings, "checkpoint has no name (using ID only)")
	}

	if c.WorkingDir == "" {
		warning := "checkpoint has no working_dir"
		if c.WorkingDirError != "" {
			warning += ": " + c.WorkingDirError
		}
		result.Warnings = append(result.Warnings, warning)
	}

	if len(c.Session.Panes) == 0 {
		result.Warnings = append(result.Warnings, "checkpoint has no panes captured")
	}

	result.Details["version"] = fmt.Sprintf("%d", c.Version)
	result.Details["id"] = c.ID
	result.Details["session"] = c.SessionName
}

// checkFiles verifies all referenced files exist on disk.
func (c *Checkpoint) checkFiles(storage *Storage, dir string, result *IntegrityResult) {
	// Check metadata.json
	if _, err := resolveExistingCheckpointArtifactPath(dir, MetadataFile); err != nil {
		result.FilesPresent = false
		if errors.Is(err, os.ErrNotExist) {
			result.Errors = append(result.Errors, "missing metadata.json")
		} else {
			result.Errors = append(result.Errors, fmt.Sprintf("invalid metadata.json: %v", err))
		}
	}

	// Check session.json
	sessionPath, err := resolveExistingCheckpointArtifactPath(dir, SessionFile)
	if err != nil {
		result.FilesPresent = false
		if errors.Is(err, os.ErrNotExist) {
			result.Errors = append(result.Errors, "missing session.json")
		} else {
			result.Errors = append(result.Errors, fmt.Sprintf("invalid session.json: %v", err))
		}
	} else {
		data, err := os.ReadFile(sessionPath)
		if err != nil {
			result.FilesPresent = false
			result.Errors = append(result.Errors, fmt.Sprintf("reading session.json: %v", err))
		} else if data, err = decryptCheckpointBytes(data); err != nil {
			result.FilesPresent = false
			result.Errors = append(result.Errors, fmt.Sprintf("reading session.json: %v", err))
		} else {
			var session SessionState
			if err := json.Unmarshal(data, &session); err != nil {
				result.FilesPresent = false
				result.Errors = append(result.Errors, fmt.Sprintf("parsing session.json: %v", err))
			} else {
				metadataJSON, err := json.Marshal(c.Session)
				if err != nil {
					result.ConsistencyValid = false
					result.Errors = append(result.Errors, fmt.Sprintf("marshaling metadata session state: %v", err))
				} else {
					sessionJSON, err := json.Marshal(session)
					if err != nil {
						result.ConsistencyValid = false
						result.Errors = append(result.Errors, fmt.Sprintf("marshaling session.json state: %v", err))
					} else if !bytes.Equal(metadataJSON, sessionJSON) {
						result.ConsistencyValid = false
						result.Errors = append(result.Errors, fmt.Sprintf("checkpoint session state mismatch between %s and %s", MetadataFile, SessionFile))
					}
				}
			}
		}
	}

	// Check scrollback files for each pane
	missingScrollback := 0
	for _, pane := range c.Session.Panes {
		if pane.ScrollbackFile != "" {
			_, err := resolveExistingCheckpointArtifactPath(dir, pane.ScrollbackFile)
			if err != nil {
				missingScrollback++
				if errors.Is(err, os.ErrNotExist) {
					result.Errors = append(result.Errors, fmt.Sprintf("missing scrollback file for pane %s: %s", pane.ID, pane.ScrollbackFile))
				} else {
					result.Errors = append(result.Errors, fmt.Sprintf("invalid scrollback file for pane %s: %v", pane.ID, err))
				}
				continue
			}
		}
	}

	if missingScrollback > 0 {
		result.FilesPresent = false
	}

	// Check git patch if referenced
	if c.Git.PatchFile != "" {
		_, err := resolveExistingCheckpointArtifactPath(dir, c.Git.PatchFile)
		if err != nil {
			result.FilesPresent = false
			if errors.Is(err, os.ErrNotExist) {
				result.Errors = append(result.Errors, fmt.Sprintf("missing git patch file: %s", c.Git.PatchFile))
			} else {
				result.Errors = append(result.Errors, fmt.Sprintf("invalid git patch file: %v", err))
			}
			result.Details["panes_dir"] = filepath.Join(dir, PanesDir)
			result.Details["files_checked"] = fmt.Sprintf("%d", 2+len(c.Session.Panes))
			return
		}
	}

	if c.Git.StatusFile != "" {
		_, err := resolveExistingCheckpointArtifactPath(dir, c.Git.StatusFile)
		if err != nil {
			result.FilesPresent = false
			if errors.Is(err, os.ErrNotExist) {
				result.Errors = append(result.Errors, fmt.Sprintf("missing git status file: %s", c.Git.StatusFile))
			} else {
				result.Errors = append(result.Errors, fmt.Sprintf("invalid git status file: %v", err))
			}
			result.Details["panes_dir"] = filepath.Join(dir, PanesDir)
			result.Details["files_checked"] = fmt.Sprintf("%d", 2+len(c.Session.Panes))
			return
		}
	}

	result.Details["panes_dir"] = filepath.Join(dir, PanesDir)
	result.Details["files_checked"] = fmt.Sprintf("%d", 2+len(c.Session.Panes))
}

// validateConsistency checks internal consistency of the checkpoint data.
func (c *Checkpoint) validateConsistency(result *IntegrityResult) {
	// Check pane count matches
	if c.PaneCount != len(c.Session.Panes) {
		result.ConsistencyValid = false
		result.Errors = append(result.Errors, fmt.Sprintf("pane_count (%d) does not match actual panes (%d)", c.PaneCount, len(c.Session.Panes)))
	}

	// Check active pane index is valid
	if len(c.Session.Panes) > 0 && (c.Session.ActivePaneIndex < 0 || c.Session.ActivePaneIndex >= len(c.Session.Panes)) {
		result.ConsistencyValid = false
		result.Errors = append(result.Errors, fmt.Sprintf("active_pane_index (%d) out of range (0-%d)", c.Session.ActivePaneIndex, len(c.Session.Panes)-1))
	}

	// Check pane dimensions are reasonable
	for _, pane := range c.Session.Panes {
		if pane.Width <= 0 || pane.Height <= 0 {
			result.Warnings = append(result.Warnings, fmt.Sprintf("pane %s has invalid dimensions: %dx%d", pane.ID, pane.Width, pane.Height))
		}
	}

	layoutErrors, layoutWarnings := validateSessionWindowLayouts(c.Session)
	if len(layoutErrors) > 0 {
		result.ConsistencyValid = false
		result.Errors = append(result.Errors, layoutErrors...)
	}
	result.Warnings = append(result.Warnings, layoutWarnings...)

	// Check git state consistency
	if c.Git.IsDirty {
		totalChanges := c.Git.StagedCount + c.Git.UnstagedCount + c.Git.UntrackedCount
		if totalChanges == 0 {
			result.Warnings = append(result.Warnings, "git marked as dirty but no changes counted")
		}
	}

	result.Details["pane_count"] = fmt.Sprintf("%d", len(c.Session.Panes))
	result.Details["has_git_state"] = fmt.Sprintf("%v", c.Git.HasState())
	if c.Git.Unavailable() {
		result.Details["git_skip_reason"] = c.Git.SkipReason
		if c.Git.SkipReason != GitSkipDisabled && c.Git.SkipReason != GitSkipNotRepository {
			result.Warnings = append(result.Warnings, "git state was not captured: "+describeGitSkip(c.Git))
		}
	}
}

func expectedManifestFiles(c *Checkpoint) map[string]struct{} {
	files := map[string]struct{}{
		MetadataFile: {},
		SessionFile:  {},
	}

	for _, pane := range c.Session.Panes {
		if pane.ScrollbackFile != "" {
			files[pane.ScrollbackFile] = struct{}{}
		}
	}
	if c.Git.PatchFile != "" {
		files[c.Git.PatchFile] = struct{}{}
	}
	if c.Git.StatusFile != "" {
		files[c.Git.StatusFile] = struct{}{}
	}

	return files
}

// VerifyAll verifies all checkpoints for a session.
func VerifyAll(storage *Storage, sessionName string) (map[string]*IntegrityResult, error) {
	sessionDir, err := storage.safeSessionDir(sessionName)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]*IntegrityResult{}, nil
		}
		return nil, fmt.Errorf("reading session directory: %w", err)
	}

	results := make(map[string]*IntegrityResult)
	for _, entry := range entries {
		if !directoryLikeEntry(entry) {
			continue
		}
		if entry.Name() == "incremental" {
			continue
		}

		id := entry.Name()
		results[id] = VerifyStoredCheckpoint(storage, sessionName, id)
	}

	return results, nil
}
