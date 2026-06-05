package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuardsCmd(t *testing.T) {
	cmd := newGuardsCmd()

	// Test that the command has expected subcommands
	expectedSubs := []string{"install", "uninstall", "status"}
	for _, sub := range expectedSubs {
		found := false
		for _, c := range cmd.Commands() {
			if c.Name() == sub {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected subcommand %q not found", sub)
		}
	}
}

func TestGuardsInstallCmd(t *testing.T) {
	cmd := newGuardsInstallCmd()
	if cmd.Use != "install" {
		t.Errorf("expected Use to be 'install', got %q", cmd.Use)
	}

	// Test help doesn't error
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Errorf("help command failed: %v", err)
	}

	// Check flags exist
	expectedFlags := []string{"project-key", "force"}
	for _, name := range expectedFlags {
		f := cmd.Flags().Lookup(name)
		if f == nil {
			t.Errorf("expected --%s flag", name)
		}
	}
}

func TestGuardsUninstallCmd(t *testing.T) {
	cmd := newGuardsUninstallCmd()
	if cmd.Use != "uninstall" {
		t.Errorf("expected Use to be 'uninstall', got %q", cmd.Use)
	}
}

func TestGuardsStatusCmd(t *testing.T) {
	cmd := newGuardsStatusCmd()
	if cmd.Use != "status" {
		t.Errorf("expected Use to be 'status', got %q", cmd.Use)
	}
}

func TestFindGitRoot(t *testing.T) {
	// Get current directory (should be in the ntm repo)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}

	// This should work since we're in a git repo
	root, err := findGitRoot(cwd)
	if err != nil {
		t.Skipf("not in a git repository: %v", err)
	}

	// Root should contain a .git directory
	gitDir := filepath.Join(root, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		t.Errorf("expected .git directory at %s", gitDir)
	}
}

func runGuardTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmdArgs := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", cmdArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, string(output))
	}
	return strings.TrimSpace(string(output))
}

func initGuardTestGitRepoWithCommit(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if output, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, string(output))
	}
	runGuardTestGit(t, repo, "config", "user.email", "ntm-test@example.com")
	runGuardTestGit(t, repo, "config", "user.name", "NTM Test")
	runGuardTestGit(t, repo, "commit", "--allow-empty", "-m", "initial")
	return repo
}

func TestFindGitHookPathLinkedWorktree(t *testing.T) {
	t.Parallel()
	repo := initGuardTestGitRepoWithCommit(t)
	linked := filepath.Join(t.TempDir(), "linked")
	runGuardTestGit(t, repo, "worktree", "add", linked)

	got, err := findGitHookPath(linked, "pre-commit")
	if err != nil {
		t.Fatalf("findGitHookPath: %v", err)
	}
	want := filepath.Join(repo, ".git", "hooks", "pre-commit")
	if got != want {
		t.Fatalf("hook path = %q, want %q", got, want)
	}
}

func TestInstallFallbackGuard(t *testing.T) {
	// Create a temp directory
	tmpDir, err := os.MkdirTemp("", "ntm-guards-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	hookPath := filepath.Join(tmpDir, "hooks", "pre-commit")
	projectKey := "/test/project"
	repoPath := "/test/repo"

	err = installFallbackGuard(hookPath, projectKey, repoPath)
	if err != nil {
		t.Fatalf("installFallbackGuard failed: %v", err)
	}

	// Check file exists and is executable
	info, err := os.Stat(hookPath)
	if err != nil {
		t.Fatalf("hook file not created: %v", err)
	}

	// Check it's executable (mode & 0111)
	if info.Mode()&0111 == 0 {
		t.Error("hook should be executable")
	}

	// Check content
	content, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("failed to read hook: %v", err)
	}

	// Verify expected markers
	checks := []string{
		"#!/bin/bash",
		"ntm-precommit-guard",
		projectKey,
		repoPath,
	}
	for _, check := range checks {
		if !bytes.Contains(content, []byte(check)) {
			t.Errorf("expected %q in hook content", check)
		}
	}
}

func TestInstallFallbackGuardDoesNotOverwriteSymlinkTarget(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	hookPath := filepath.Join(tmpDir, "hooks", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0755); err != nil {
		t.Fatal(err)
	}
	outsidePath := filepath.Join(t.TempDir(), "outside-hook")
	outsideContent := "#!/bin/bash\n# ntm-precommit-guard\necho outside\n"
	if err := os.WriteFile(outsidePath, []byte(outsideContent), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePath, hookPath); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	if err := installFallbackGuard(hookPath, "/test/project", "/test/repo"); err != nil {
		t.Fatalf("installFallbackGuard: %v", err)
	}

	outside, err := os.ReadFile(outsidePath)
	if err != nil {
		t.Fatalf("reading outside hook target: %v", err)
	}
	if string(outside) != outsideContent {
		t.Fatalf("outside hook target was overwritten: got %q, want %q", string(outside), outsideContent)
	}
	info, err := os.Lstat(hookPath)
	if err != nil {
		t.Fatalf("lstat hook path: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("fallback hook path is still a symlink")
	}
}

func TestGuardsStatusResponse(t *testing.T) {
	resp := GuardsStatusResponse{
		Installed:    true,
		RepoPath:     "/test/repo",
		HookPath:     "/test/repo/.git/hooks/pre-commit",
		ProjectKey:   "/test/repo",
		IsNTMGuard:   true,
		OtherHook:    false,
		MCPAvailable: true,
	}

	if !resp.Installed {
		t.Error("expected Installed to be true")
	}
	if !resp.IsNTMGuard {
		t.Error("expected IsNTMGuard to be true")
	}
}

func TestGuardsInstallResponse(t *testing.T) {
	resp := GuardsInstallResponse{
		Success:    true,
		RepoPath:   "/test/repo",
		ProjectKey: "/test/repo",
		HookPath:   "/test/repo/.git/hooks/pre-commit",
	}

	if !resp.Success {
		t.Error("expected Success to be true")
	}
}

func TestGuardsUninstallResponse(t *testing.T) {
	resp := GuardsUninstallResponse{
		Success:  true,
		RepoPath: "/test/repo",
		HookPath: "/test/repo/.git/hooks/pre-commit",
	}

	if !resp.Success {
		t.Error("expected Success to be true")
	}
}
