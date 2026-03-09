package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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

// Verify performs all integrity checks on a checkpoint.
func (c *Checkpoint) Verify(storage *Storage) *IntegrityResult {
	result := &IntegrityResult{
		Valid:            true,
		SchemaValid:      true,
		FilesPresent:     true,
		ChecksumsValid:   true,
		ConsistencyValid: true,
		Errors:           []string{},
		Warnings:         []string{},
		Details:          make(map[string]string),
	}

	dir := storage.CheckpointDir(c.SessionName, c.ID)

	// Run all checks
	c.validateSchema(result)
	c.checkFiles(storage, dir, result)
	c.validateConsistency(result)

	// Overall validity
	result.Valid = result.SchemaValid && result.FilesPresent && result.ConsistencyValid
	// Checksums are optional (only checked if manifest exists)

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
	metaPath := filepath.Join(dir, MetadataFile)
	if !fileExists(metaPath) {
		result.FilesPresent = false
		result.Errors = append(result.Errors, "missing metadata.json")
	}

	// Check session.json
	sessionPath := filepath.Join(dir, SessionFile)
	if !fileExists(sessionPath) {
		result.FilesPresent = false
		result.Errors = append(result.Errors, "missing session.json")
	}

	// Check scrollback files for each pane
	missingScrollback := 0
	for _, pane := range c.Session.Panes {
		if pane.ScrollbackFile != "" {
			scrollPath, err := resolveCheckpointRelativePath(dir, pane.ScrollbackFile)
			if err != nil {
				missingScrollback++
				result.Errors = append(result.Errors, fmt.Sprintf("invalid scrollback file for pane %s: %v", pane.ID, err))
				continue
			}
			if !fileExists(scrollPath) {
				missingScrollback++
				result.Errors = append(result.Errors, fmt.Sprintf("missing scrollback file for pane %s: %s", pane.ID, pane.ScrollbackFile))
			}
		}
	}

	if missingScrollback > 0 {
		result.FilesPresent = false
	}

	// Check git patch if referenced
	if c.Git.PatchFile != "" {
		patchPath, err := resolveCheckpointRelativePath(dir, c.Git.PatchFile)
		if err != nil {
			result.FilesPresent = false
			result.Errors = append(result.Errors, fmt.Sprintf("invalid git patch file: %v", err))
			result.Details["panes_dir"] = filepath.Join(dir, PanesDir)
			result.Details["files_checked"] = fmt.Sprintf("%d", 2+len(c.Session.Panes))
			return
		}
		if !fileExists(patchPath) {
			result.FilesPresent = false
			result.Errors = append(result.Errors, fmt.Sprintf("missing git patch file: %s", c.Git.PatchFile))
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
	dir := storage.CheckpointDir(c.SessionName, c.ID)
	manifest := &FileManifest{
		Files: make(map[string]string),
	}

	// Hash metadata.json
	if hash, err := hashFile(filepath.Join(dir, MetadataFile)); err == nil {
		manifest.Files[MetadataFile] = hash
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("hashing %s: %w", MetadataFile, err)
	}

	// Hash session.json
	if hash, err := hashFile(filepath.Join(dir, SessionFile)); err == nil {
		manifest.Files[SessionFile] = hash
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("hashing %s: %w", SessionFile, err)
	}

	// Hash scrollback files
	for _, pane := range c.Session.Panes {
		if pane.ScrollbackFile != "" {
			path, err := resolveCheckpointRelativePath(dir, pane.ScrollbackFile)
			if err != nil {
				return nil, fmt.Errorf("invalid scrollback path %s: %w", pane.ScrollbackFile, err)
			}
			if hash, err := hashFile(path); err == nil {
				manifest.Files[pane.ScrollbackFile] = hash
			} else if !os.IsNotExist(err) {
				return nil, fmt.Errorf("hashing scrollback %s: %w", pane.ScrollbackFile, err)
			}
		}
	}

	// Hash git patch if exists
	if c.Git.PatchFile != "" {
		path, err := resolveCheckpointRelativePath(dir, c.Git.PatchFile)
		if err != nil {
			return nil, fmt.Errorf("invalid git patch path %s: %w", c.Git.PatchFile, err)
		}
		if hash, err := hashFile(path); err == nil {
			manifest.Files[c.Git.PatchFile] = hash
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("hashing git patch: %w", err)
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

	dir := storage.CheckpointDir(c.SessionName, c.ID)
	verified := 0
	failed := 0

	for relPath, expectedHash := range manifest.Files {
		fullPath := filepath.Join(dir, relPath)
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
			result.Errors = append(result.Errors, fmt.Sprintf("checksum mismatch: %s (expected %s, got %s)", relPath, expectedHash[:16]+"...", actualHash[:16]+"..."))
			failed++
		} else {
			verified++
		}
	}

	if failed > 0 {
		result.Valid = false
		result.ChecksumsValid = false
	}

	result.Details["verified"] = fmt.Sprintf("%d", verified)
	result.Details["failed"] = fmt.Sprintf("%d", failed)
	result.Details["total"] = fmt.Sprintf("%d", len(manifest.Files))

	return result
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
	dir := storage.CheckpointDir(c.SessionName, c.ID)
	if !fileExists(filepath.Join(dir, MetadataFile)) {
		errs = append(errs, errors.New("missing metadata.json"))
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
	checkpoints, err := storage.List(sessionName)
	if err != nil {
		return nil, err
	}

	results := make(map[string]*IntegrityResult)
	for _, cp := range checkpoints {
		results[cp.ID] = cp.Verify(storage)
	}

	return results, nil
}
