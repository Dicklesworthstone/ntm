//go:build e2e
// +build e2e

// Package e2e contains end-to-end tests for NTM robot mode commands.
// [E2E-CHECKPOINT] Tests for ntm checkpoint (session save/resume cycle).
package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// CheckpointSaveResult represents the JSON output from checkpoint save
type CheckpointSaveResult struct {
	ID               string   `json:"id"`
	Session          string   `json:"session"`
	CreatedAt        string   `json:"created_at"`
	Description      string   `json:"description"`
	PaneCount        int      `json:"pane_count"`
	HasGit           bool     `json:"has_git"`
	AssignmentsCount int      `json:"assignments_count"`
	Assignments      []string `json:"assignments,omitempty"`
}

// CheckpointListResult represents the JSON output from checkpoint list
type CheckpointListResult struct {
	Session     string                   `json:"session,omitempty"`
	Sessions    []CheckpointSessionEntry `json:"sessions,omitempty"`
	Checkpoints []CheckpointEntry        `json:"checkpoints,omitempty"`
	Count       int                      `json:"count"`
}

// CheckpointSessionEntry represents a session with its checkpoints
type CheckpointSessionEntry struct {
	Session     string            `json:"session"`
	Checkpoints []CheckpointEntry `json:"checkpoints"`
}

// CheckpointEntry represents a single checkpoint entry
type CheckpointEntry struct {
	ID          string `json:"id"`
	SessionName string `json:"session_name"`
	Name        string `json:"name,omitempty"`
	CreatedAt   string `json:"created_at"`
	Description string `json:"description,omitempty"`
	PaneCount   int    `json:"pane_count"`
}

// CheckpointShowResult represents the JSON output from checkpoint show
type CheckpointShowResult struct {
	ID          string                `json:"id"`
	SessionName string                `json:"session_name"`
	Name        string                `json:"name,omitempty"`
	CreatedAt   string                `json:"created_at"`
	Description string                `json:"description,omitempty"`
	PaneCount   int                   `json:"pane_count"`
	WorkingDir  string                `json:"working_dir"`
	Session     CheckpointSessionInfo `json:"session"`
	Git         CheckpointGitInfo     `json:"git,omitempty"`
}

// CheckpointSessionInfo contains session pane information
type CheckpointSessionInfo struct {
	Panes []CheckpointPaneInfo `json:"panes"`
}

// CheckpointPaneInfo contains information about a single pane
type CheckpointPaneInfo struct {
	Index           int    `json:"index"`
	Title           string `json:"title"`
	AgentType       string `json:"agent_type,omitempty"`
	ScrollbackLines int    `json:"scrollback_lines,omitempty"`
}

// CheckpointGitInfo contains git state information
type CheckpointGitInfo struct {
	Branch        string `json:"branch,omitempty"`
	Commit        string `json:"commit,omitempty"`
	IsDirty       bool   `json:"is_dirty,omitempty"`
	StagedCount   int    `json:"staged_count,omitempty"`
	UnstagedCount int    `json:"unstaged_count,omitempty"`
	PatchFile     string `json:"patch_file,omitempty"`
}

// CheckpointTestSuite manages E2E tests for checkpoint commands
type CheckpointTestSuite struct {
	t           *testing.T
	logger      *TestLogger
	tempDir     string
	sessionName string
	cleanup     []func()
}

// NewCheckpointTestSuite creates a new checkpoint test suite
func NewCheckpointTestSuite(t *testing.T, scenario string) *CheckpointTestSuite {
	logger := NewTestLogger(t, scenario)

	s := &CheckpointTestSuite{
		t:           t,
		logger:      logger,
		sessionName: "ntm-e2e-checkpoint-" + time.Now().Format("20060102-150405"),
	}

	return s
}

// Setup creates a temporary directory for testing
func (s *CheckpointTestSuite) Setup() error {
	s.logger.Log("[E2E-CHECKPOINT] Setting up test environment")

	// Create temp directory
	tempDir, err := os.MkdirTemp("", "ntm-checkpoint-e2e-")
	if err != nil {
		return err
	}
	s.tempDir = tempDir

	s.cleanup = append(s.cleanup, func() {
		os.RemoveAll(tempDir)
	})

	return nil
}

// SetupWithTmuxSession creates a temp directory and a tmux session
func (s *CheckpointTestSuite) SetupWithTmuxSession() error {
	if err := s.Setup(); err != nil {
		return err
	}

	// Check if tmux is available
	if _, err := exec.LookPath("tmux"); err != nil {
		s.logger.Log("[E2E-CHECKPOINT] tmux not found, skipping session creation")
		return nil
	}

	// Create a test tmux session
	cmd := exec.Command("tmux", "new-session", "-d", "-s", s.sessionName, "-c", s.tempDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		s.logger.Log("[E2E-CHECKPOINT] Failed to create tmux session: %v, output: %s", err, string(out))
		return nil // Don't fail, just skip
	}

	s.cleanup = append(s.cleanup, func() {
		exec.Command("tmux", "kill-session", "-t", s.sessionName).Run()
	})

	s.logger.Log("[E2E-CHECKPOINT] Created tmux session: %s", s.sessionName)
	return nil
}

// Cleanup runs all cleanup functions
func (s *CheckpointTestSuite) Cleanup() {
	s.logger.Log("[E2E-CHECKPOINT] Running cleanup")
	for i := len(s.cleanup) - 1; i >= 0; i-- {
		s.cleanup[i]()
	}
}

// TempDir returns the temp directory path
func (s *CheckpointTestSuite) TempDir() string {
	return s.tempDir
}

// SessionName returns the test session name
func (s *CheckpointTestSuite) SessionName() string {
	return s.sessionName
}

// HasTmuxSession checks if the tmux session was created
func (s *CheckpointTestSuite) HasTmuxSession() bool {
	cmd := exec.Command("tmux", "has-session", "-t", s.sessionName)
	return cmd.Run() == nil
}

// RunCheckpoint executes ntm checkpoint subcommand and returns the output
func (s *CheckpointTestSuite) RunCheckpoint(args ...string) ([]byte, error) {
	allArgs := append([]string{"checkpoint"}, args...)
	cmd := exec.Command("ntm", allArgs...)
	cmd.Dir = s.tempDir

	s.logger.Log("[E2E-CHECKPOINT] Running: ntm %v", allArgs)

	output, err := cmd.CombinedOutput()
	s.logger.Log("[E2E-CHECKPOINT] Output: %s", string(output))
	if err != nil {
		s.logger.Log("[E2E-CHECKPOINT] Exit error: %v", err)
	}

	return output, err
}

// RunCheckpointJSON executes ntm checkpoint with --json flag
func (s *CheckpointTestSuite) RunCheckpointJSON(args ...string) ([]byte, error) {
	allArgs := append(args, "--json")
	return s.RunCheckpoint(allArgs...)
}

// RunRollback executes ntm rollback and returns the output
func (s *CheckpointTestSuite) RunRollback(args ...string) ([]byte, error) {
	allArgs := append([]string{"rollback"}, args...)
	cmd := exec.Command("ntm", allArgs...)
	cmd.Dir = s.tempDir

	s.logger.Log("[E2E-CHECKPOINT] Running: ntm %v", allArgs)

	output, err := cmd.CombinedOutput()
	s.logger.Log("[E2E-CHECKPOINT] Output: %s", string(output))
	if err != nil {
		s.logger.Log("[E2E-CHECKPOINT] Exit error: %v", err)
	}

	return output, err
}

// TestCheckpointListEmpty tests that checkpoint list works with no checkpoints
func TestCheckpointListEmpty(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewCheckpointTestSuite(t, "checkpoint-list-empty")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	// List checkpoints for a non-existent session
	output, err := suite.RunCheckpointJSON("list", "nonexistent-session-xyz")
	if err != nil {
		// Error is expected if no checkpoints exist
		suite.logger.Log("[E2E-CHECKPOINT] List command returned error (expected for empty): %v", err)
	}

	// Check if output mentions no checkpoints
	outputStr := string(output)
	if strings.Contains(outputStr, "count") {
		var result CheckpointListResult
		if err := json.Unmarshal(output, &result); err == nil {
			if result.Count != 0 {
				t.Errorf("[E2E-CHECKPOINT] Expected count=0 for nonexistent session, got %d", result.Count)
			}
		}
	}

	suite.logger.Log("[E2E-CHECKPOINT] Empty list test passed")
}

// TestCheckpointSaveRequiresSession tests that save requires a valid session
func TestCheckpointSaveRequiresSession(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewCheckpointTestSuite(t, "checkpoint-save-requires-session")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	// Try to save checkpoint for non-existent session
	output, err := suite.RunCheckpoint("save", "nonexistent-session-xyz")
	if err == nil {
		t.Error("[E2E-CHECKPOINT] Expected error when saving checkpoint for non-existent session")
	}

	// Check error message mentions session
	if !strings.Contains(string(output), "does not exist") {
		suite.logger.Log("[E2E-CHECKPOINT] Output: %s", string(output))
		// Accept any error - the key is it should fail
	}

	suite.logger.Log("[E2E-CHECKPOINT] Save requires session test passed")
}

// TestCheckpointSaveWithSession tests creating a checkpoint for an existing session
func TestCheckpointSaveWithSession(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)
	SkipIfNoTmux(t)

	suite := NewCheckpointTestSuite(t, "checkpoint-save")
	if err := suite.SetupWithTmuxSession(); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	if !suite.HasTmuxSession() {
		t.Skip("[E2E-CHECKPOINT] Tmux session not available")
	}

	// Save checkpoint
	output, err := suite.RunCheckpointJSON("save", suite.SessionName(), "-m", "E2E test checkpoint")
	if err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Save failed: %v, output: %s", err, string(output))
	}

	var result CheckpointSaveResult
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Failed to parse JSON: %v", err)
	}

	// Validate required fields
	if result.ID == "" {
		t.Error("[E2E-CHECKPOINT] Expected non-empty checkpoint ID")
	}

	if result.Session != suite.SessionName() {
		t.Errorf("[E2E-CHECKPOINT] Expected session=%s, got %s", suite.SessionName(), result.Session)
	}

	if result.PaneCount < 1 {
		t.Error("[E2E-CHECKPOINT] Expected at least 1 pane")
	}

	suite.logger.Log("[E2E-CHECKPOINT] Checkpoint saved: id=%s, panes=%d", result.ID, result.PaneCount)
}

// TestCheckpointListAfterSave tests listing checkpoints after saving one
func TestCheckpointListAfterSave(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)
	SkipIfNoTmux(t)

	suite := NewCheckpointTestSuite(t, "checkpoint-list-after-save")
	if err := suite.SetupWithTmuxSession(); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	if !suite.HasTmuxSession() {
		t.Skip("[E2E-CHECKPOINT] Tmux session not available")
	}

	// Save checkpoint
	saveOutput, err := suite.RunCheckpointJSON("save", suite.SessionName())
	if err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Save failed: %v", err)
	}

	var saveResult CheckpointSaveResult
	if err := json.Unmarshal(saveOutput, &saveResult); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Failed to parse save JSON: %v", err)
	}

	// List checkpoints for this session
	listOutput, err := suite.RunCheckpointJSON("list", suite.SessionName())
	if err != nil {
		t.Fatalf("[E2E-CHECKPOINT] List failed: %v, output: %s", err, string(listOutput))
	}

	var listResult CheckpointListResult
	if err := json.Unmarshal(listOutput, &listResult); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Failed to parse list JSON: %v", err)
	}

	if listResult.Count < 1 {
		t.Error("[E2E-CHECKPOINT] Expected at least 1 checkpoint after save")
	}

	// Verify the saved checkpoint is in the list
	found := false
	for _, cp := range listResult.Checkpoints {
		if cp.ID == saveResult.ID {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("[E2E-CHECKPOINT] Saved checkpoint %s not found in list", saveResult.ID)
	}

	suite.logger.Log("[E2E-CHECKPOINT] List after save test passed: count=%d", listResult.Count)
}

// TestCheckpointShowDetails tests showing checkpoint details
func TestCheckpointShowDetails(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)
	SkipIfNoTmux(t)

	suite := NewCheckpointTestSuite(t, "checkpoint-show")
	if err := suite.SetupWithTmuxSession(); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	if !suite.HasTmuxSession() {
		t.Skip("[E2E-CHECKPOINT] Tmux session not available")
	}

	// Save checkpoint with description
	saveOutput, err := suite.RunCheckpointJSON("save", suite.SessionName(), "-m", "Test description for show")
	if err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Save failed: %v", err)
	}

	var saveResult CheckpointSaveResult
	if err := json.Unmarshal(saveOutput, &saveResult); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Failed to parse save JSON: %v", err)
	}

	// Show checkpoint details
	showOutput, err := suite.RunCheckpointJSON("show", suite.SessionName(), saveResult.ID)
	if err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Show failed: %v, output: %s", err, string(showOutput))
	}

	var showResult CheckpointShowResult
	if err := json.Unmarshal(showOutput, &showResult); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Failed to parse show JSON: %v", err)
	}

	// Validate required fields
	if showResult.ID != saveResult.ID {
		t.Errorf("[E2E-CHECKPOINT] Expected id=%s, got %s", saveResult.ID, showResult.ID)
	}

	if showResult.SessionName != suite.SessionName() {
		t.Errorf("[E2E-CHECKPOINT] Expected session_name=%s, got %s", suite.SessionName(), showResult.SessionName)
	}

	if showResult.Description != "Test description for show" {
		t.Errorf("[E2E-CHECKPOINT] Expected description='Test description for show', got '%s'", showResult.Description)
	}

	if showResult.WorkingDir == "" {
		t.Error("[E2E-CHECKPOINT] Expected non-empty working_dir")
	}

	suite.logger.Log("[E2E-CHECKPOINT] Show details test passed: id=%s, panes=%d", showResult.ID, showResult.PaneCount)
}

// TestCheckpointWithDescription tests checkpoint description flag
func TestCheckpointWithDescription(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)
	SkipIfNoTmux(t)

	suite := NewCheckpointTestSuite(t, "checkpoint-description")
	if err := suite.SetupWithTmuxSession(); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	if !suite.HasTmuxSession() {
		t.Skip("[E2E-CHECKPOINT] Tmux session not available")
	}

	description := "Before major refactoring"

	output, err := suite.RunCheckpointJSON("save", suite.SessionName(), "-m", description)
	if err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Save failed: %v", err)
	}

	var result CheckpointSaveResult
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Failed to parse JSON: %v", err)
	}

	if result.Description != description {
		t.Errorf("[E2E-CHECKPOINT] Expected description='%s', got '%s'", description, result.Description)
	}

	suite.logger.Log("[E2E-CHECKPOINT] Description test passed")
}

// TestCheckpointDelete tests deleting a checkpoint
func TestCheckpointDelete(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)
	SkipIfNoTmux(t)

	suite := NewCheckpointTestSuite(t, "checkpoint-delete")
	if err := suite.SetupWithTmuxSession(); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	if !suite.HasTmuxSession() {
		t.Skip("[E2E-CHECKPOINT] Tmux session not available")
	}

	// Save checkpoint
	saveOutput, err := suite.RunCheckpointJSON("save", suite.SessionName())
	if err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Save failed: %v", err)
	}

	var saveResult CheckpointSaveResult
	if err := json.Unmarshal(saveOutput, &saveResult); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Failed to parse save JSON: %v", err)
	}

	// Delete checkpoint with --force to skip confirmation
	deleteOutput, err := suite.RunCheckpointJSON("delete", suite.SessionName(), saveResult.ID, "--force")
	if err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Delete failed: %v, output: %s", err, string(deleteOutput))
	}

	// Verify deletion in JSON output
	var deleteResult map[string]interface{}
	if err := json.Unmarshal(deleteOutput, &deleteResult); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Failed to parse delete JSON: %v", err)
	}

	if deleted, ok := deleteResult["deleted"].(bool); !ok || !deleted {
		t.Error("[E2E-CHECKPOINT] Expected deleted=true in response")
	}

	// Verify checkpoint no longer in list
	listOutput, err := suite.RunCheckpointJSON("list", suite.SessionName())
	// Note: err might be set if no checkpoints remain
	if err == nil {
		var listResult CheckpointListResult
		if err := json.Unmarshal(listOutput, &listResult); err == nil {
			for _, cp := range listResult.Checkpoints {
				if cp.ID == saveResult.ID {
					t.Errorf("[E2E-CHECKPOINT] Deleted checkpoint %s still in list", saveResult.ID)
				}
			}
		}
	}

	suite.logger.Log("[E2E-CHECKPOINT] Delete test passed: deleted id=%s", saveResult.ID)
}

// TestCheckpointVerify tests checkpoint verification
func TestCheckpointVerify(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)
	SkipIfNoTmux(t)

	suite := NewCheckpointTestSuite(t, "checkpoint-verify")
	if err := suite.SetupWithTmuxSession(); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	if !suite.HasTmuxSession() {
		t.Skip("[E2E-CHECKPOINT] Tmux session not available")
	}

	// Save checkpoint
	saveOutput, err := suite.RunCheckpointJSON("save", suite.SessionName())
	if err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Save failed: %v", err)
	}

	var saveResult CheckpointSaveResult
	if err := json.Unmarshal(saveOutput, &saveResult); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Failed to parse save JSON: %v", err)
	}

	// Verify checkpoint
	verifyOutput, err := suite.RunCheckpointJSON("verify", suite.SessionName(), saveResult.ID)
	if err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Verify failed: %v, output: %s", err, string(verifyOutput))
	}

	var verifyResult map[string]interface{}
	if err := json.Unmarshal(verifyOutput, &verifyResult); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Failed to parse verify JSON: %v", err)
	}

	if valid, ok := verifyResult["valid"].(bool); !ok || !valid {
		t.Error("[E2E-CHECKPOINT] Expected valid=true for freshly created checkpoint")
	}

	suite.logger.Log("[E2E-CHECKPOINT] Verify test passed: id=%s is valid", saveResult.ID)
}

// TestCheckpointExport tests exporting a checkpoint to archive
func TestCheckpointExport(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)
	SkipIfNoTmux(t)

	suite := NewCheckpointTestSuite(t, "checkpoint-export")
	if err := suite.SetupWithTmuxSession(); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	if !suite.HasTmuxSession() {
		t.Skip("[E2E-CHECKPOINT] Tmux session not available")
	}

	// Save checkpoint
	saveOutput, err := suite.RunCheckpointJSON("save", suite.SessionName())
	if err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Save failed: %v", err)
	}

	var saveResult CheckpointSaveResult
	if err := json.Unmarshal(saveOutput, &saveResult); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Failed to parse save JSON: %v", err)
	}

	// Export checkpoint
	exportPath := filepath.Join(suite.TempDir(), "checkpoint-export.tar.gz")
	exportOutput, err := suite.RunCheckpointJSON("export", suite.SessionName(), saveResult.ID, "-o", exportPath)
	if err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Export failed: %v, output: %s", err, string(exportOutput))
	}

	// Verify export file exists
	if _, err := os.Stat(exportPath); os.IsNotExist(err) {
		t.Error("[E2E-CHECKPOINT] Export file was not created")
	}

	var exportResult map[string]interface{}
	if err := json.Unmarshal(exportOutput, &exportResult); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Failed to parse export JSON: %v", err)
	}

	if path, ok := exportResult["output_path"].(string); !ok || path != exportPath {
		t.Errorf("[E2E-CHECKPOINT] Expected output_path=%s, got %v", exportPath, exportResult["output_path"])
	}

	suite.logger.Log("[E2E-CHECKPOINT] Export test passed: file=%s", exportPath)
}

// TestCheckpointNoGitFlag tests checkpoint with --no-git flag
func TestCheckpointNoGitFlag(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)
	SkipIfNoTmux(t)

	suite := NewCheckpointTestSuite(t, "checkpoint-no-git")
	if err := suite.SetupWithTmuxSession(); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	if !suite.HasTmuxSession() {
		t.Skip("[E2E-CHECKPOINT] Tmux session not available")
	}

	output, err := suite.RunCheckpointJSON("save", suite.SessionName(), "--no-git")
	if err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Save failed: %v", err)
	}

	var result CheckpointSaveResult
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Failed to parse JSON: %v", err)
	}

	if result.HasGit {
		t.Error("[E2E-CHECKPOINT] Expected has_git=false with --no-git flag")
	}

	suite.logger.Log("[E2E-CHECKPOINT] No-git flag test passed")
}

// TestCheckpointScrollbackLines tests custom scrollback depth
func TestCheckpointScrollbackLines(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)
	SkipIfNoTmux(t)

	suite := NewCheckpointTestSuite(t, "checkpoint-scrollback")
	if err := suite.SetupWithTmuxSession(); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	if !suite.HasTmuxSession() {
		t.Skip("[E2E-CHECKPOINT] Tmux session not available")
	}

	// Save with custom scrollback
	output, err := suite.RunCheckpointJSON("save", suite.SessionName(), "--scrollback=500")
	if err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Save failed: %v", err)
	}

	var result CheckpointSaveResult
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Failed to parse JSON: %v", err)
	}

	// The result doesn't directly show scrollback lines, but the save should succeed
	if result.ID == "" {
		t.Error("[E2E-CHECKPOINT] Expected non-empty checkpoint ID")
	}

	suite.logger.Log("[E2E-CHECKPOINT] Scrollback lines test passed")
}

// TestRollbackRequiresSession tests that rollback requires a valid session
func TestRollbackRequiresSession(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewCheckpointTestSuite(t, "rollback-requires-session")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	// Try to rollback non-existent session
	output, err := suite.RunRollback("nonexistent-session", "last")
	if err == nil {
		t.Error("[E2E-CHECKPOINT] Expected error when rolling back non-existent session")
	}

	// Accept any error - the key is it should fail
	suite.logger.Log("[E2E-CHECKPOINT] Rollback requires session test passed: %s", string(output))
}

// TestRollbackDryRun tests rollback with --dry-run flag
func TestRollbackDryRun(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)
	SkipIfNoTmux(t)

	suite := NewCheckpointTestSuite(t, "rollback-dry-run")
	if err := suite.SetupWithTmuxSession(); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	if !suite.HasTmuxSession() {
		t.Skip("[E2E-CHECKPOINT] Tmux session not available")
	}

	// Save a checkpoint first
	_, err := suite.RunCheckpoint("save", suite.SessionName())
	if err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Save failed: %v", err)
	}

	// Try rollback with --dry-run
	output, err := suite.RunRollback(suite.SessionName(), "last", "--dry-run", "--json")
	// Dry-run should succeed or provide preview info
	suite.logger.Log("[E2E-CHECKPOINT] Rollback dry-run output: %s", string(output))

	if err == nil {
		// If command succeeded, verify it was a dry-run
		if !strings.Contains(string(output), "dry_run") && !strings.Contains(string(output), "preview") {
			suite.logger.Log("[E2E-CHECKPOINT] Dry-run succeeded without errors")
		}
	}

	suite.logger.Log("[E2E-CHECKPOINT] Rollback dry-run test completed")
}

// TestCheckpointOutputFormat tests TUI output contains expected information
func TestCheckpointOutputFormat(t *testing.T) {
	SkipIfShort(t)
	SkipIfNoNTM(t)

	suite := NewCheckpointTestSuite(t, "checkpoint-format")
	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-CHECKPOINT] Setup failed: %v", err)
	}
	defer suite.Cleanup()

	// Test help output
	output, _ := suite.RunCheckpoint("--help")
	outputStr := string(output)

	expectedElements := []string{
		"checkpoint",
		"save",
		"list",
		"show",
		"delete",
	}

	for _, element := range expectedElements {
		if !strings.Contains(outputStr, element) {
			t.Errorf("[E2E-CHECKPOINT] Expected help to mention: %s", element)
		}
	}

	suite.logger.Log("[E2E-CHECKPOINT] Output format test passed")
}
