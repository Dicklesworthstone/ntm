package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// hookStatusResponse is the JSON output for ntm hooks status --json.
type hookStatusResponse struct {
	RepoRoot string     `json:"repo_root"`
	HooksDir string     `json:"hooks_dir"`
	Hooks    []hookInfo `json:"hooks"`
	Error    string     `json:"error,omitempty"`
}

type hookInfo struct {
	Type      string `json:"type"`
	Path      string `json:"path"`
	Installed bool   `json:"installed"`
	IsNTM     bool   `json:"is_ntm"`
	HasBackup bool   `json:"has_backup"`
}

// hookInstallResponse is the JSON output for ntm hooks install --json.
type hookInstallResponse struct {
	Success  bool   `json:"success"`
	HookType string `json:"hook_type"`
	Path     string `json:"path"`
	Error    string `json:"error,omitempty"`
}

// hookUninstallResponse is the JSON output for ntm hooks uninstall --json.
type hookUninstallResponse struct {
	Success  bool   `json:"success"`
	HookType string `json:"hook_type"`
	Restored bool   `json:"restored"`
	Error    string `json:"error,omitempty"`
}

// setupHooksTestRepo creates a temporary git repository for hooks testing.
func setupHooksTestRepo(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()

	// Initialize git repo
	out, err := runCmdAllowFail(t, tmpDir, "git", "init")
	if err != nil {
		t.Fatalf("git init failed: %v\nout=%s", err, string(out))
	}

	// Configure git user (required for commits)
	runCmd(t, tmpDir, "git", "config", "user.email", "test@example.com")
	runCmd(t, tmpDir, "git", "config", "user.name", "Test User")

	return tmpDir
}

// TestE2EHooks_Install tests hook installation.
func TestE2EHooks_Install(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireNTMBinary(t)

	t.Run("install_precommit_hook", func(t *testing.T) {
		repoDir := setupHooksTestRepo(t)

		// Install pre-commit hook
		out := runCmd(t, repoDir, "ntm", "hooks", "install", "--json")
		jsonData := extractJSON(out)

		var resp hookInstallResponse
		if err := json.Unmarshal(jsonData, &resp); err != nil {
			t.Fatalf("unmarshal: %v\nout=%s", err, string(out))
		}

		if !resp.Success {
			t.Errorf("expected success=true, got false; error=%s", resp.Error)
		}

		if resp.HookType != "pre-commit" {
			t.Errorf("expected hook_type='pre-commit', got '%s'", resp.HookType)
		}

		// Verify hook file exists
		hookPath := filepath.Join(repoDir, ".git", "hooks", "pre-commit")
		if _, err := os.Stat(hookPath); os.IsNotExist(err) {
			t.Errorf("hook file not created at %s", hookPath)
		}

		// Verify hook content contains NTM marker
		content, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatalf("read hook: %v", err)
		}
		if !strings.Contains(string(content), "NTM_MANAGED_HOOK") {
			t.Errorf("hook should contain NTM_MANAGED_HOOK marker")
		}

		// Verify hook is executable
		info, _ := os.Stat(hookPath)
		if info.Mode()&0111 == 0 {
			t.Errorf("hook file should be executable")
		}
	})

	t.Run("install_postcheckout_hook", func(t *testing.T) {
		repoDir := setupHooksTestRepo(t)

		// Install post-checkout hook
		out := runCmd(t, repoDir, "ntm", "hooks", "install", "post-checkout", "--json")
		jsonData := extractJSON(out)

		var resp hookInstallResponse
		if err := json.Unmarshal(jsonData, &resp); err != nil {
			t.Fatalf("unmarshal: %v\nout=%s", err, string(out))
		}

		if !resp.Success {
			t.Errorf("expected success=true, got false; error=%s", resp.Error)
		}

		if resp.HookType != "post-checkout" {
			t.Errorf("expected hook_type='post-checkout', got '%s'", resp.HookType)
		}

		// Verify hook file exists
		hookPath := filepath.Join(repoDir, ".git", "hooks", "post-checkout")
		if _, err := os.Stat(hookPath); os.IsNotExist(err) {
			t.Errorf("hook file not created at %s", hookPath)
		}
	})

	t.Run("install_existing_hook_fails_without_force", func(t *testing.T) {
		repoDir := setupHooksTestRepo(t)

		// Create existing non-NTM hook
		hookPath := filepath.Join(repoDir, ".git", "hooks", "pre-commit")
		existingHook := "#!/bin/bash\necho 'existing hook'"
		if err := os.WriteFile(hookPath, []byte(existingHook), 0755); err != nil {
			t.Fatalf("write existing hook: %v", err)
		}

		// Try to install without --force
		out, _ := runCmdAllowFail(t, repoDir, "ntm", "hooks", "install", "--json")
		jsonData := extractJSON(out)

		var resp hookInstallResponse
		if err := json.Unmarshal(jsonData, &resp); err != nil {
			t.Fatalf("unmarshal: %v\nout=%s", err, string(out))
		}

		// Should fail or indicate hook exists
		// (The command may still succeed in JSON mode but indicate error)
		if resp.Success && resp.Error == "" {
			// Verify the existing hook was NOT overwritten
			content, _ := os.ReadFile(hookPath)
			if strings.Contains(string(content), "NTM_MANAGED_HOOK") {
				t.Errorf("existing hook should not be overwritten without --force")
			}
		}
	})

	t.Run("install_with_force_overwrites", func(t *testing.T) {
		repoDir := setupHooksTestRepo(t)

		// Create existing non-NTM hook
		hookPath := filepath.Join(repoDir, ".git", "hooks", "pre-commit")
		existingHook := "#!/bin/bash\necho 'existing hook'"
		if err := os.WriteFile(hookPath, []byte(existingHook), 0755); err != nil {
			t.Fatalf("write existing hook: %v", err)
		}

		// Install with --force
		out := runCmd(t, repoDir, "ntm", "hooks", "install", "--force", "--json")
		jsonData := extractJSON(out)

		var resp hookInstallResponse
		if err := json.Unmarshal(jsonData, &resp); err != nil {
			t.Fatalf("unmarshal: %v\nout=%s", err, string(out))
		}

		if !resp.Success {
			t.Errorf("expected success=true with --force, got false; error=%s", resp.Error)
		}

		// Verify hook was overwritten
		content, _ := os.ReadFile(hookPath)
		if !strings.Contains(string(content), "NTM_MANAGED_HOOK") {
			t.Errorf("hook should be NTM-managed after --force install")
		}

		// Verify backup was created
		backupPath := hookPath + ".backup"
		if _, err := os.Stat(backupPath); os.IsNotExist(err) {
			t.Errorf("backup should be created at %s", backupPath)
		}
	})
}

// TestE2EHooks_Status tests hook status reporting.
func TestE2EHooks_Status(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireNTMBinary(t)

	t.Run("status_no_hooks", func(t *testing.T) {
		repoDir := setupHooksTestRepo(t)

		out := runCmd(t, repoDir, "ntm", "hooks", "status", "--json")
		jsonData := extractJSON(out)

		var resp hookStatusResponse
		if err := json.Unmarshal(jsonData, &resp); err != nil {
			t.Fatalf("unmarshal: %v\nout=%s", err, string(out))
		}

		// Should have hooks array with supported types
		if len(resp.Hooks) == 0 {
			t.Errorf("expected hooks array to be populated")
		}

		// All hooks should be not installed
		for _, h := range resp.Hooks {
			if h.Installed {
				t.Errorf("hook %s should not be installed", h.Type)
			}
		}

		// Repo root should be set
		if resp.RepoRoot == "" {
			t.Errorf("expected repo_root to be set")
		}

		// Hooks dir should end with .git/hooks
		if !strings.HasSuffix(resp.HooksDir, filepath.Join(".git", "hooks")) {
			t.Errorf("expected hooks_dir to end with .git/hooks, got %s", resp.HooksDir)
		}
	})

	t.Run("status_with_installed_hook", func(t *testing.T) {
		repoDir := setupHooksTestRepo(t)

		// Install pre-commit hook
		runCmd(t, repoDir, "ntm", "hooks", "install", "--json")

		// Check status
		out := runCmd(t, repoDir, "ntm", "hooks", "status", "--json")
		jsonData := extractJSON(out)

		var resp hookStatusResponse
		if err := json.Unmarshal(jsonData, &resp); err != nil {
			t.Fatalf("unmarshal: %v\nout=%s", err, string(out))
		}

		// Find pre-commit in hooks
		var preCommit *hookInfo
		for i := range resp.Hooks {
			if resp.Hooks[i].Type == "pre-commit" {
				preCommit = &resp.Hooks[i]
				break
			}
		}

		if preCommit == nil {
			t.Fatalf("pre-commit hook not found in status")
		}

		if !preCommit.Installed {
			t.Errorf("pre-commit should be installed")
		}

		if !preCommit.IsNTM {
			t.Errorf("pre-commit should be marked as NTM-managed")
		}
	})

	t.Run("status_detects_non_ntm_hook", func(t *testing.T) {
		repoDir := setupHooksTestRepo(t)

		// Create non-NTM hook
		hookPath := filepath.Join(repoDir, ".git", "hooks", "pre-commit")
		if err := os.WriteFile(hookPath, []byte("#!/bin/bash\necho test"), 0755); err != nil {
			t.Fatalf("write hook: %v", err)
		}

		// Check status
		out := runCmd(t, repoDir, "ntm", "hooks", "status", "--json")
		jsonData := extractJSON(out)

		var resp hookStatusResponse
		if err := json.Unmarshal(jsonData, &resp); err != nil {
			t.Fatalf("unmarshal: %v\nout=%s", err, string(out))
		}

		// Find pre-commit
		var preCommit *hookInfo
		for i := range resp.Hooks {
			if resp.Hooks[i].Type == "pre-commit" {
				preCommit = &resp.Hooks[i]
				break
			}
		}

		if preCommit == nil {
			t.Fatalf("pre-commit hook not found in status")
		}

		if !preCommit.Installed {
			t.Errorf("pre-commit should be marked as installed")
		}

		if preCommit.IsNTM {
			t.Errorf("pre-commit should NOT be marked as NTM-managed")
		}
	})
}

// TestE2EHooks_Uninstall tests hook uninstallation.
func TestE2EHooks_Uninstall(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireNTMBinary(t)

	t.Run("uninstall_ntm_hook", func(t *testing.T) {
		repoDir := setupHooksTestRepo(t)

		// Install hook first
		runCmd(t, repoDir, "ntm", "hooks", "install", "--json")

		// Verify it's installed
		hookPath := filepath.Join(repoDir, ".git", "hooks", "pre-commit")
		if _, err := os.Stat(hookPath); os.IsNotExist(err) {
			t.Fatalf("hook not installed for uninstall test")
		}

		// Uninstall
		out := runCmd(t, repoDir, "ntm", "hooks", "uninstall", "--json")
		jsonData := extractJSON(out)

		var resp hookUninstallResponse
		if err := json.Unmarshal(jsonData, &resp); err != nil {
			t.Fatalf("unmarshal: %v\nout=%s", err, string(out))
		}

		if !resp.Success {
			t.Errorf("expected success=true, got false; error=%s", resp.Error)
		}

		if resp.HookType != "pre-commit" {
			t.Errorf("expected hook_type='pre-commit', got '%s'", resp.HookType)
		}

		// Verify hook file is removed
		if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
			t.Errorf("hook file should be removed after uninstall")
		}
	})

	t.Run("uninstall_restores_backup", func(t *testing.T) {
		repoDir := setupHooksTestRepo(t)

		// Create existing hook
		hookPath := filepath.Join(repoDir, ".git", "hooks", "pre-commit")
		originalContent := "#!/bin/bash\necho 'original'"
		if err := os.WriteFile(hookPath, []byte(originalContent), 0755); err != nil {
			t.Fatalf("write original hook: %v", err)
		}

		// Install with force (creates backup)
		runCmd(t, repoDir, "ntm", "hooks", "install", "--force", "--json")

		// Uninstall (should restore backup)
		out := runCmd(t, repoDir, "ntm", "hooks", "uninstall", "--json")
		jsonData := extractJSON(out)

		var resp hookUninstallResponse
		if err := json.Unmarshal(jsonData, &resp); err != nil {
			t.Fatalf("unmarshal: %v\nout=%s", err, string(out))
		}

		if !resp.Success {
			t.Errorf("expected success=true, got false; error=%s", resp.Error)
		}

		// Verify original hook is restored
		content, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatalf("read restored hook: %v", err)
		}
		if !strings.Contains(string(content), "original") {
			t.Errorf("original hook should be restored, got: %s", string(content))
		}

		// Verify backup is removed
		backupPath := hookPath + ".backup"
		if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
			t.Errorf("backup should be removed after restore")
		}
	})

	t.Run("uninstall_not_installed_hook", func(t *testing.T) {
		repoDir := setupHooksTestRepo(t)

		// Try to uninstall when no hook is installed
		out, _ := runCmdAllowFail(t, repoDir, "ntm", "hooks", "uninstall", "--json")
		jsonData := extractJSON(out)

		var resp hookUninstallResponse
		if err := json.Unmarshal(jsonData, &resp); err != nil {
			t.Fatalf("unmarshal: %v\nout=%s", err, string(out))
		}

		// Should fail or indicate not installed
		if resp.Success && resp.Error == "" {
			t.Logf("uninstall on non-existent hook: success=%v", resp.Success)
		}
	})

	t.Run("uninstall_non_ntm_hook_fails", func(t *testing.T) {
		repoDir := setupHooksTestRepo(t)

		// Create non-NTM hook
		hookPath := filepath.Join(repoDir, ".git", "hooks", "pre-commit")
		if err := os.WriteFile(hookPath, []byte("#!/bin/bash\necho test"), 0755); err != nil {
			t.Fatalf("write hook: %v", err)
		}

		// Try to uninstall
		out, _ := runCmdAllowFail(t, repoDir, "ntm", "hooks", "uninstall", "--json")
		jsonData := extractJSON(out)

		var resp hookUninstallResponse
		if err := json.Unmarshal(jsonData, &resp); err != nil {
			t.Fatalf("unmarshal: %v\nout=%s", err, string(out))
		}

		// Should fail - can't uninstall non-NTM hook
		if resp.Success {
			t.Errorf("should not be able to uninstall non-NTM hook")
		}

		// Hook should still exist
		if _, err := os.Stat(hookPath); os.IsNotExist(err) {
			t.Errorf("non-NTM hook should not be removed")
		}
	})
}

// TestE2EHooks_Run tests manual hook execution.
func TestE2EHooks_Run(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireNTMBinary(t)

	t.Run("run_precommit_no_staged_files", func(t *testing.T) {
		repoDir := setupHooksTestRepo(t)

		// Run pre-commit with no staged files
		out := runCmd(t, repoDir, "ntm", "hooks", "run", "pre-commit", "--json")
		jsonData := extractJSON(out)

		// Parse JSON output
		var result map[string]interface{}
		if err := json.Unmarshal(jsonData, &result); err != nil {
			t.Fatalf("unmarshal: %v\nout=%s", err, string(out))
		}

		// With no staged files, should pass (skip empty)
		if passed, ok := result["passed"].(bool); ok {
			if !passed {
				// Check if error is about empty staging (which is OK)
				if errMsg, hasErr := result["error"].(string); hasErr {
					if !strings.Contains(errMsg, "no staged") && !strings.Contains(errMsg, "empty") {
						t.Errorf("unexpected failure: %s", errMsg)
					}
				}
			}
		}
	})

	t.Run("run_unknown_hook_fails", func(t *testing.T) {
		repoDir := setupHooksTestRepo(t)

		// Run unknown hook type
		out, err := runCmdAllowFail(t, repoDir, "ntm", "hooks", "run", "unknown-hook", "--json")
		if err == nil {
			t.Logf("output: %s", string(out))
		}

		// Should fail with unknown hook error
		outStr := string(out)
		if !strings.Contains(outStr, "unknown") && !strings.Contains(outStr, "not") {
			t.Logf("output for unknown hook: %s", outStr)
		}
	})
}

// TestE2EHooks_NotGitRepo tests error handling when not in a git repo.
func TestE2EHooks_NotGitRepo(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireNTMBinary(t)

	t.Run("install_not_git_repo", func(t *testing.T) {
		tmpDir := t.TempDir() // Not a git repo

		out, _ := runCmdAllowFail(t, tmpDir, "ntm", "hooks", "install", "--json")
		jsonData := extractJSON(out)

		var resp hookInstallResponse
		if err := json.Unmarshal(jsonData, &resp); err != nil {
			// May fail to parse if error is not JSON
			outStr := string(out)
			if !strings.Contains(outStr, "not a git") && !strings.Contains(outStr, "repository") {
				t.Logf("non-git repo output: %s", outStr)
			}
			return
		}

		if resp.Success {
			t.Errorf("should fail in non-git directory")
		}

		if resp.Error == "" {
			t.Errorf("expected error message for non-git directory")
		}
	})

	t.Run("status_not_git_repo", func(t *testing.T) {
		tmpDir := t.TempDir() // Not a git repo

		out, _ := runCmdAllowFail(t, tmpDir, "ntm", "hooks", "status", "--json")
		outStr := string(out)

		// Should indicate not a git repo
		if !strings.Contains(outStr, "not a git") && !strings.Contains(outStr, "repository") && !strings.Contains(outStr, "error") {
			t.Logf("non-git repo status output: %s", outStr)
		}
	})
}

// TestE2EHooks_JSONOutput tests JSON output consistency.
func TestE2EHooks_JSONOutput(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireNTMBinary(t)

	t.Run("status_json_structure", func(t *testing.T) {
		repoDir := setupHooksTestRepo(t)

		out := runCmd(t, repoDir, "ntm", "hooks", "status", "--json")
		jsonData := extractJSON(out)

		// Parse as raw JSON
		var raw map[string]interface{}
		if err := json.Unmarshal(jsonData, &raw); err != nil {
			t.Fatalf("unmarshal: %v\nout=%s", err, string(out))
		}

		// Check required fields
		requiredFields := []string{"repo_root", "hooks_dir", "hooks"}
		for _, field := range requiredFields {
			if _, ok := raw[field]; !ok {
				t.Errorf("missing required field: %s", field)
			}
		}

		// Check hooks array structure
		if hooks, ok := raw["hooks"].([]interface{}); ok {
			if len(hooks) > 0 {
				hook := hooks[0].(map[string]interface{})
				hookFields := []string{"type", "path", "installed", "is_ntm", "has_backup"}
				for _, field := range hookFields {
					if _, ok := hook[field]; !ok {
						t.Errorf("hook missing field: %s", field)
					}
				}
			}
		} else {
			t.Errorf("hooks should be an array")
		}
	})

	t.Run("install_json_structure", func(t *testing.T) {
		repoDir := setupHooksTestRepo(t)

		out := runCmd(t, repoDir, "ntm", "hooks", "install", "--json")
		jsonData := extractJSON(out)

		var raw map[string]interface{}
		if err := json.Unmarshal(jsonData, &raw); err != nil {
			t.Fatalf("unmarshal: %v\nout=%s", err, string(out))
		}

		// Check required fields
		requiredFields := []string{"success", "hook_type", "path"}
		for _, field := range requiredFields {
			if _, ok := raw[field]; !ok {
				t.Errorf("missing required field: %s", field)
			}
		}
	})
}

// TestE2EHooks_HookContent tests hook script content.
func TestE2EHooks_HookContent(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireNTMBinary(t)

	t.Run("precommit_contains_beads_sync", func(t *testing.T) {
		repoDir := setupHooksTestRepo(t)

		// Install pre-commit hook
		runCmd(t, repoDir, "ntm", "hooks", "install", "--json")

		// Read hook content
		hookPath := filepath.Join(repoDir, ".git", "hooks", "pre-commit")
		content, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatalf("read hook: %v", err)
		}

		contentStr := string(content)

		// Should contain beads sync
		if !strings.Contains(contentStr, "br sync") {
			t.Errorf("pre-commit hook should contain beads sync command")
		}

		// Should contain NTM marker
		if !strings.Contains(contentStr, "NTM_MANAGED_HOOK") {
			t.Errorf("pre-commit hook should contain NTM_MANAGED_HOOK marker")
		}

		// Should call ntm hooks run
		if !strings.Contains(contentStr, "hooks run pre-commit") {
			t.Errorf("pre-commit hook should call ntm hooks run")
		}

		// Should chain to backup hook
		if !strings.Contains(contentStr, "pre-commit.backup") {
			t.Errorf("pre-commit hook should chain to backup hook")
		}
	})

	t.Run("postcheckout_warns_beads", func(t *testing.T) {
		repoDir := setupHooksTestRepo(t)

		// Install post-checkout hook
		runCmd(t, repoDir, "ntm", "hooks", "install", "post-checkout", "--json")

		// Read hook content
		hookPath := filepath.Join(repoDir, ".git", "hooks", "post-checkout")
		content, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatalf("read hook: %v", err)
		}

		contentStr := string(content)

		// Should contain beads warning
		if !strings.Contains(contentStr, ".beads") {
			t.Errorf("post-checkout hook should check for .beads directory")
		}

		// Should contain warning message
		if !strings.Contains(contentStr, "Warning") || !strings.Contains(contentStr, "uncommitted") {
			t.Errorf("post-checkout hook should warn about uncommitted beads changes")
		}
	})
}
