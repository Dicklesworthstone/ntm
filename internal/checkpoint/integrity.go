package checkpoint

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
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
	// ChecksumsValid indicates if all checksums match (if manifest exists).
	ChecksumsValid bool `json:"checksums_valid"`
	// ConsistencyValid indicates if internal consistency checks pass.
	ConsistencyValid bool `json:"consistency_valid"`

	// Errors contains any validation errors.
	Errors []string `json:"errors,omitempty"`
	// Warnings contains non-fatal issues.
	Warnings []string `json:"warnings,omitempty"`
	// Details contains detailed check results.
	Details map[string]string `json:"details,omitempty"`

	// Manifest contains file checksums for verification.
	Manifest *FileManifest `json:"manifest,omitempty"`
}

// FileManifest contains checksums for all checkpoint files.
type FileManifest struct {
	// Files maps relative paths to SHA256 hex hashes.
	Files map[string]string `json:"files"`
	// CreatedAt is when the manifest was generated.
	CreatedAt string `json:"created_at,omitempty"`
}

func newIntegrityResult() *IntegrityResult {
	return &IntegrityResult{
		Valid:            true,
		SchemaValid:      true,
		FilesPresent:     true,
		ChecksumsValid:   true,
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
	c.validateConsistency(result)

	result.Valid = result.SchemaValid && result.FilesPresent && result.ConsistencyValid
	return result
}

// Verify performs all integrity checks on a checkpoint.
func (c *Checkpoint) Verify(storage *Storage) *IntegrityResult {
	dir, err := storage.safeCheckpointDir(c.SessionName, c.ID)
	if err != nil {
		result := newIntegrityResult()
		result.FilesPresent = false
		result.Errors = append(result.Errors, err.Error())
		result.Valid = false
		return result
	}
	return c.verifyWithDir(storage, dir)
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
		result.Warnings = append(result.Warnings, "checkpoint has no working_dir")
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
	result.Details["has_git_state"] = fmt.Sprintf("%v", c.Git.Branch != "")
}

// GenerateManifest creates a manifest with checksums for all checkpoint files.
func (c *Checkpoint) GenerateManifest(storage *Storage) (*FileManifest, error) {
	dir, err := storage.safeCheckpointDir(c.SessionName, c.ID)
	if err != nil {
		return nil, err
	}
	manifest := &FileManifest{
		Files:     make(map[string]string),
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}

	// Hash metadata.json
	metaPath, err := resolveExistingCheckpointArtifactPath(dir, MetadataFile)
	if err == nil {
		hash, err := hashFile(metaPath)
		if err != nil {
			return nil, fmt.Errorf("hashing %s: %w", MetadataFile, err)
		}
		manifest.Files[MetadataFile] = hash
	} else if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("missing %s", MetadataFile)
	} else {
		return nil, fmt.Errorf("invalid %s: %w", MetadataFile, err)
	}

	// Hash session.json
	sessionPath, err := resolveExistingCheckpointArtifactPath(dir, SessionFile)
	if err == nil {
		hash, err := hashFile(sessionPath)
		if err != nil {
			return nil, fmt.Errorf("hashing %s: %w", SessionFile, err)
		}
		manifest.Files[SessionFile] = hash
	} else if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("missing %s", SessionFile)
	} else {
		return nil, fmt.Errorf("invalid %s: %w", SessionFile, err)
	}

	// Hash scrollback files
	for _, pane := range c.Session.Panes {
		if pane.ScrollbackFile != "" {
			path, err := resolveExistingCheckpointArtifactPath(dir, pane.ScrollbackFile)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil, fmt.Errorf("missing scrollback %s", pane.ScrollbackFile)
				}
				return nil, fmt.Errorf("invalid scrollback path %s: %w", pane.ScrollbackFile, err)
			}
			if hash, err := hashFile(path); err == nil {
				manifest.Files[pane.ScrollbackFile] = hash
			} else if os.IsNotExist(err) {
				return nil, fmt.Errorf("missing scrollback %s", pane.ScrollbackFile)
			} else {
				return nil, fmt.Errorf("hashing scrollback %s: %w", pane.ScrollbackFile, err)
			}
		}
	}

	// Hash git patch if exists
	if c.Git.PatchFile != "" {
		path, err := resolveExistingCheckpointArtifactPath(dir, c.Git.PatchFile)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("missing git patch %s", c.Git.PatchFile)
			}
			return nil, fmt.Errorf("invalid git patch path %s: %w", c.Git.PatchFile, err)
		}
		if hash, err := hashFile(path); err == nil {
			manifest.Files[c.Git.PatchFile] = hash
		} else if os.IsNotExist(err) {
			return nil, fmt.Errorf("missing git patch %s", c.Git.PatchFile)
		} else {
			return nil, fmt.Errorf("hashing git patch: %w", err)
		}
	}

	if c.Git.StatusFile != "" {
		path, err := resolveExistingCheckpointArtifactPath(dir, c.Git.StatusFile)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("missing git status %s", c.Git.StatusFile)
			}
			return nil, fmt.Errorf("invalid git status path %s: %w", c.Git.StatusFile, err)
		}
		if hash, err := hashFile(path); err == nil {
			manifest.Files[c.Git.StatusFile] = hash
		} else if os.IsNotExist(err) {
			return nil, fmt.Errorf("missing git status %s", c.Git.StatusFile)
		} else {
			return nil, fmt.Errorf("hashing git status: %w", err)
		}
	}

	return manifest, nil
}

// VerifyManifest checks that all files match the manifest checksums.
func (c *Checkpoint) VerifyManifest(storage *Storage, manifest *FileManifest) *IntegrityResult {
	result := &IntegrityResult{
		Valid:          true,
		ChecksumsValid: true,
		Errors:         []string{},
		Details:        make(map[string]string),
		Manifest:       manifest,
	}

	if manifest == nil || len(manifest.Files) == 0 {
		result.Warnings = append(result.Warnings, "no manifest provided, skipping checksum verification")
		return result
	}

	dir, err := storage.safeCheckpointDir(c.SessionName, c.ID)
	if err != nil {
		result.Valid = false
		result.ChecksumsValid = false
		result.Errors = append(result.Errors, err.Error())
		return result
	}

	expectedFiles := expectedManifestFiles(c)
	coverageFailures := 0
	for relPath := range expectedFiles {
		if _, ok := manifest.Files[relPath]; !ok {
			result.Errors = append(result.Errors, fmt.Sprintf("manifest missing file: %s", relPath))
			coverageFailures++
		}
	}
	for relPath := range manifest.Files {
		if _, ok := expectedFiles[relPath]; !ok {
			result.Errors = append(result.Errors, fmt.Sprintf("manifest contains unexpected file: %s", relPath))
			coverageFailures++
		}
	}

	verified := 0
	failed := 0

	for relPath, expectedHash := range manifest.Files {
		if _, ok := expectedFiles[relPath]; !ok {
			continue
		}

		fullPath, err := resolveExistingCheckpointArtifactPath(dir, relPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				result.Errors = append(result.Errors, fmt.Sprintf("file missing: %s", relPath))
			} else {
				result.Errors = append(result.Errors, fmt.Sprintf("invalid manifest path: %s (%v)", relPath, err))
			}
			failed++
			continue
		}
		actualHash, err := hashFile(fullPath)
		if err != nil {
			if os.IsNotExist(err) {
				result.Errors = append(result.Errors, fmt.Sprintf("file missing: %s", relPath))
			} else {
				result.Errors = append(result.Errors, fmt.Sprintf("error reading %s: %v", relPath, err))
			}
			failed++
			continue
		}

		if actualHash != expectedHash {
			result.Errors = append(result.Errors, fmt.Sprintf("checksum mismatch: %s (expected %s, got %s)", relPath, hashDisplayPrefix(expectedHash), hashDisplayPrefix(actualHash)))
			failed++
		} else {
			verified++
		}
	}

	totalFailures := failed + coverageFailures
	if totalFailures > 0 {
		result.Valid = false
		result.ChecksumsValid = false
	}

	result.Details["verified"] = fmt.Sprintf("%d", verified)
	result.Details["failed"] = fmt.Sprintf("%d", totalFailures)
	result.Details["total"] = fmt.Sprintf("%d", len(manifest.Files))

	return result
}

func hashDisplayPrefix(hash string) string {
	const prefixLen = 16
	if len(hash) <= prefixLen {
		return hash
	}
	return hash[:prefixLen] + "..."
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

// hashFile computes the SHA256 hash of a file.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// QuickCheck performs a fast validation without reading file contents.
func (c *Checkpoint) QuickCheck(storage *Storage) error {
	var errs []error

	// Version check
	if c.Version < MinVersion || c.Version > CurrentVersion {
		errs = append(errs, fmt.Errorf("unsupported version: %d", c.Version))
	}

	// Required fields
	if c.ID == "" {
		errs = append(errs, errors.New("missing checkpoint ID"))
	}
	if c.SessionName == "" {
		errs = append(errs, errors.New("missing session_name"))
	}

	// Check critical files exist
	dir, err := storage.safeCheckpointDir(c.SessionName, c.ID)
	if err != nil {
		errs = append(errs, err)
	}
	if err == nil {
		if _, err := resolveExistingCheckpointArtifactPath(dir, MetadataFile); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				errs = append(errs, errors.New("missing metadata.json"))
			} else {
				errs = append(errs, fmt.Errorf("invalid metadata.json: %w", err))
			}
		}
		if _, err := resolveExistingCheckpointArtifactPath(dir, SessionFile); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				errs = append(errs, errors.New("missing session.json"))
			} else {
				errs = append(errs, fmt.Errorf("invalid session.json: %w", err))
			}
		}
	}

	if len(errs) == 0 {
		return nil
	}

	// Combine errors
	errMsg := "checkpoint validation failed:"
	for _, e := range errs {
		errMsg += " " + e.Error() + ";"
	}
	return errors.New(errMsg)
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
