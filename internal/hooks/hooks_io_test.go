package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// validHookTOML is a minimal valid hooks TOML config.
const validHookTOML = `
[[command_hooks]]
event = "pre-spawn"
command = "echo hello"
`

// multiHookTOML has two hooks.
const multiHookTOML = `
[[command_hooks]]
event = "pre-spawn"
command = "echo one"

[[command_hooks]]
event = "post-spawn"
command = "echo two"
`

func TestLoadCommandHooksFromMainConfig_NonExistent(t *testing.T) {
	t.Parallel()
	cfg, err := LoadCommandHooksFromMainConfig("/nonexistent/path/config.toml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Hooks) != 0 {
		t.Errorf("expected 0 hooks for non-existent file, got %d", len(cfg.Hooks))
	}
}

func TestLoadCommandHooksFromMainConfig_ValidFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte(validHookTOML), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadCommandHooksFromMainConfig(configPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Hooks) != 1 {
		t.Errorf("expected 1 hook, got %d", len(cfg.Hooks))
	}
	if cfg.Hooks[0].Event != "pre-spawn" {
		t.Errorf("event = %q, want pre-spawn", cfg.Hooks[0].Event)
	}
}

func TestLoadCommandHooksFromMainConfig_AllowsUnrelatedSections(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	content := `theme = "nord"

[agents]
claude = "claude"
` + validHookTOML
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadCommandHooksFromMainConfig(configPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Hooks) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(cfg.Hooks))
	}
}

func TestLoadCommandHooksFromMainConfig_UnknownHookField(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	content := `theme = "nord"

[[command_hooks]]
event = "pre-spawn"
command = "echo hello"
legacy = true
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadCommandHooksFromMainConfig(configPath)
	if err == nil {
		t.Fatal("expected error for unknown command_hooks field")
	}
	if got := err.Error(); !strings.Contains(got, "unknown command_hooks field") {
		t.Fatalf("expected unknown command_hooks field error, got %q", got)
	}
	if got := err.Error(); !strings.Contains(got, "command_hooks.legacy") {
		t.Fatalf("expected error to mention command_hooks.legacy, got %q", got)
	}
}

func TestLoadCommandHooksFromMainConfig_InvalidTOML(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	// Write non-TOML content — malformed main config should be surfaced.
	if err := os.WriteFile(configPath, []byte("not valid toml {{{"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadCommandHooksFromMainConfig(configPath)
	if err == nil {
		t.Fatal("expected error for invalid TOML in main config")
	}
	if got := err.Error(); !strings.Contains(got, "parsing main config") {
		t.Fatalf("expected parsing main config error, got %q", got)
	}
}

func TestLoadCommandHooksFromMainConfig_InvalidHook(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	// Valid TOML but invalid event name
	invalidHook := `
[[command_hooks]]
event = "not-valid-event"
command = "echo test"
`
	if err := os.WriteFile(configPath, []byte(invalidHook), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadCommandHooksFromMainConfig(configPath)
	if err == nil {
		t.Error("expected error for invalid hook config")
	}
}

func TestLoadCommandHooksFromMainConfig_ReadError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	// Create an unreadable file
	if err := os.WriteFile(configPath, []byte(validHookTOML), 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(configPath, 0644) })

	_, err := LoadCommandHooksFromMainConfig(configPath)
	if err == nil {
		t.Error("expected error for unreadable file")
	}
}

func TestLoadHooksFromDirectory_NonExistent(t *testing.T) {
	t.Parallel()
	cfg, err := LoadHooksFromDirectory("/nonexistent/dir/hooks")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Hooks) != 0 {
		t.Errorf("expected 0 hooks for non-existent dir, got %d", len(cfg.Hooks))
	}
}

func TestLoadHooksFromDirectory_EmptyDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	cfg, err := LoadHooksFromDirectory(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Hooks) != 0 {
		t.Errorf("expected 0 hooks for empty dir, got %d", len(cfg.Hooks))
	}
}

func TestLoadHooksFromDirectory_WithTOMLFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Write two hook files
	if err := os.WriteFile(filepath.Join(dir, "a.toml"), []byte(validHookTOML), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.toml"), []byte(validHookTOML), 0644); err != nil {
		t.Fatal(err)
	}
	// Non-TOML file should be skipped
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("not a toml"), 0644); err != nil {
		t.Fatal(err)
	}
	// Subdirectory should be skipped
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadHooksFromDirectory(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Hooks) != 2 {
		t.Errorf("expected 2 hooks (from 2 toml files), got %d", len(cfg.Hooks))
	}
}

func TestLoadHooksFromDirectory_InvalidTOMLFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Write invalid TOML content
	if err := os.WriteFile(filepath.Join(dir, "bad.toml"), []byte("not valid {{{"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadHooksFromDirectory(dir)
	if err == nil {
		t.Error("expected error for invalid TOML file in directory")
	}
}

func TestLoadCommandHooks_WithValidFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.toml")
	if err := os.WriteFile(path, []byte(multiHookTOML), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadCommandHooks(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Hooks) != 2 {
		t.Errorf("expected 2 hooks, got %d", len(cfg.Hooks))
	}
}

func TestLoadCommandHooks_ReadError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.toml")
	if err := os.WriteFile(path, []byte(validHookTOML), 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0644) })

	_, err := LoadCommandHooks(path)
	if err == nil {
		t.Error("expected error for unreadable file")
	}
}

func TestLoadCommandHooks_ParseError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.toml")
	if err := os.WriteFile(path, []byte("not valid toml {{{"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadCommandHooks(path)
	if err == nil {
		t.Error("expected error for invalid TOML")
	}
}

func TestLoadCommandHooks_ValidationError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.toml")
	// Valid TOML but invalid event name triggers validation error
	invalidHook := `
[[command_hooks]]
event = "not-a-real-event"
command = "echo test"
`
	if err := os.WriteFile(path, []byte(invalidHook), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadCommandHooks(path)
	if err == nil {
		t.Error("expected validation error")
	}
}

func TestLoadAllCommandHooks_InvalidMainConfigPropagatesError(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", filepath.Join(xdg, "home"))

	configDir := filepath.Join(xdg, "ntm")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("not valid toml {{{"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadAllCommandHooks()
	if err == nil {
		t.Fatal("expected error for invalid main config in LoadAllCommandHooks")
	}
	if got := err.Error(); !strings.Contains(got, "parsing main config") {
		t.Fatalf("expected parsing main config error, got %q", got)
	}
}

// --- HookManager tests using temp git repos ---

func initTempGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", dir)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	return dir
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmdArgs := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", cmdArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, string(output))
	}
	return strings.TrimSpace(string(output))
}

func initTempGitRepoWithCommit(t *testing.T) string {
	t.Helper()
	repo := initTempGitRepo(t)
	runGit(t, repo, "config", "user.email", "ntm-test@example.com")
	runGit(t, repo, "config", "user.name", "NTM Test")
	runGit(t, repo, "commit", "--allow-empty", "-m", "initial")
	return repo
}

func TestNewManager(t *testing.T) {
	t.Parallel()
	repo := initTempGitRepo(t)

	m, err := NewManager(repo)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if m.RepoRoot() != repo {
		t.Errorf("RepoRoot = %q, want %q", m.RepoRoot(), repo)
	}
	if m.HooksDir() != filepath.Join(repo, ".git", "hooks") {
		t.Errorf("HooksDir = %q, want %q", m.HooksDir(), filepath.Join(repo, ".git", "hooks"))
	}
}

func TestNewManager_LinkedWorktreeUsesGitHooksDir(t *testing.T) {
	t.Parallel()
	repo := initTempGitRepoWithCommit(t)
	linked := filepath.Join(t.TempDir(), "linked")
	runGit(t, repo, "worktree", "add", linked)

	m, err := NewManager(linked)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if m.RepoRoot() != linked {
		t.Errorf("RepoRoot = %q, want %q", m.RepoRoot(), linked)
	}
	wantHooksDir := filepath.Join(repo, ".git", "hooks")
	if m.HooksDir() != wantHooksDir {
		t.Errorf("HooksDir = %q, want %q", m.HooksDir(), wantHooksDir)
	}
}

func TestNewManager_NotGitRepo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	_, err := NewManager(dir)
	if err == nil {
		t.Error("expected error for non-git directory")
	}
}

func TestManagerStatus_NotInstalled(t *testing.T) {
	t.Parallel()
	repo := initTempGitRepo(t)
	m, err := NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}

	info, err := m.Status(HookPreCommit)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if info.Installed {
		t.Error("expected not installed")
	}
	if info.Type != HookPreCommit {
		t.Errorf("Type = %q, want %q", info.Type, HookPreCommit)
	}
}

func TestManagerStatus_Installed(t *testing.T) {
	t.Parallel()
	repo := initTempGitRepo(t)
	m, err := NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}

	// Write a hook manually
	hookPath := filepath.Join(m.HooksDir(), string(HookPostCheckout))
	hookContent := "#!/bin/sh\n# NTM_MANAGED_HOOK\necho test\n"
	if err := os.MkdirAll(filepath.Dir(hookPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte(hookContent), 0755); err != nil {
		t.Fatal(err)
	}

	info, err := m.Status(HookPostCheckout)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !info.Installed {
		t.Error("expected installed")
	}
	if !info.IsNTM {
		t.Error("expected IsNTM")
	}
	if info.HasBackup {
		t.Error("expected no backup")
	}
}

func TestManagerStatus_InstalledWithBackup(t *testing.T) {
	t.Parallel()
	repo := initTempGitRepo(t)
	m, err := NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}

	hooksDir := m.HooksDir()
	hookPath := filepath.Join(hooksDir, string(HookPostCommit))
	backupPath := hookPath + ".backup"

	hookContent := "#!/bin/sh\n# NTM_MANAGED_HOOK\necho test\n"
	if err := os.MkdirAll(filepath.Dir(hookPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte(hookContent), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupPath, []byte("#!/bin/sh\necho old\n"), 0755); err != nil {
		t.Fatal(err)
	}

	info, err := m.Status(HookPostCommit)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !info.HasBackup {
		t.Error("expected HasBackup = true")
	}
}

func TestManagerListAll(t *testing.T) {
	t.Parallel()
	repo := initTempGitRepo(t)
	m, err := NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}

	infos, err := m.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	// Should return info for all 5 hook types
	if len(infos) != 5 {
		t.Errorf("expected 5 hook infos, got %d", len(infos))
	}
	for _, info := range infos {
		if info.Installed {
			t.Errorf("hook %q should not be installed in fresh repo", info.Type)
		}
	}
}

func TestManagerUninstall_NotInstalled(t *testing.T) {
	t.Parallel()
	repo := initTempGitRepo(t)
	m, err := NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}

	err = m.Uninstall(HookPreCommit, false)
	if err != ErrHookNotInstalled {
		t.Errorf("expected ErrHookNotInstalled, got %v", err)
	}
}

func TestManagerUninstall_NTMHook(t *testing.T) {
	t.Parallel()
	repo := initTempGitRepo(t)
	m, err := NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}

	// Write an NTM hook
	hookPath := filepath.Join(m.HooksDir(), string(HookCommitMsg))
	hookContent := "#!/bin/sh\n# NTM_MANAGED_HOOK\necho test\n"
	if err := os.MkdirAll(filepath.Dir(hookPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte(hookContent), 0755); err != nil {
		t.Fatal(err)
	}

	err = m.Uninstall(HookCommitMsg, false)
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	// Should be gone
	if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
		t.Error("hook file should be removed")
	}
}

func TestManagerUninstall_NonNTMHook(t *testing.T) {
	t.Parallel()
	repo := initTempGitRepo(t)
	m, err := NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}

	// Write a non-NTM hook
	hookPath := filepath.Join(m.HooksDir(), string(HookCommitMsg))
	if err := os.MkdirAll(filepath.Dir(hookPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\necho foreign\n"), 0755); err != nil {
		t.Fatal(err)
	}

	err = m.Uninstall(HookCommitMsg, false)
	if err == nil {
		t.Error("expected error for non-NTM hook")
	}
}

func TestManagerUninstall_WithRestore(t *testing.T) {
	t.Parallel()
	repo := initTempGitRepo(t)
	m, err := NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}

	hookPath := filepath.Join(m.HooksDir(), string(HookCommitMsg))
	backupPath := hookPath + ".backup"
	backupContent := "#!/bin/sh\necho old\n"

	// Write an NTM hook + backup
	if err := os.MkdirAll(filepath.Dir(hookPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\n# NTM_MANAGED_HOOK\necho new\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupPath, []byte(backupContent), 0755); err != nil {
		t.Fatal(err)
	}

	err = m.Uninstall(HookCommitMsg, true)
	if err != nil {
		t.Fatalf("Uninstall with restore: %v", err)
	}

	// Original should be restored
	content, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("reading restored hook: %v", err)
	}
	if string(content) != backupContent {
		t.Errorf("restored content = %q, want %q", string(content), backupContent)
	}
}

func TestManagerInstall_PostCheckout(t *testing.T) {
	t.Parallel()
	repo := initTempGitRepo(t)
	m, err := NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}

	// PostCheckout doesn't require ntm binary
	err = m.Install(HookPostCheckout, false)
	if err != nil {
		t.Fatalf("Install post-checkout: %v", err)
	}

	hookPath := filepath.Join(m.HooksDir(), string(HookPostCheckout))
	content, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("reading installed hook: %v", err)
	}
	if !isNTMHook(string(content)) {
		t.Error("installed hook should contain NTM_MANAGED_HOOK marker")
	}
}

func TestManagerInstall_OverwriteExistingNTMHook(t *testing.T) {
	t.Parallel()
	repo := initTempGitRepo(t)
	m, err := NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}

	hookPath := filepath.Join(m.HooksDir(), string(HookPostCheckout))
	if err := os.MkdirAll(filepath.Dir(hookPath), 0755); err != nil {
		t.Fatal(err)
	}
	// Write an existing NTM hook
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\n# NTM_MANAGED_HOOK\necho old\n"), 0755); err != nil {
		t.Fatal(err)
	}

	// Should overwrite without --force since it's an NTM hook
	err = m.Install(HookPostCheckout, false)
	if err != nil {
		t.Fatalf("Install over existing NTM hook: %v", err)
	}
}

func TestManagerInstall_ExistingNTMSymlinkDoesNotOverwriteTarget(t *testing.T) {
	t.Parallel()
	repo := initTempGitRepo(t)
	m, err := NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}

	hookPath := filepath.Join(m.HooksDir(), string(HookPostCheckout))
	if err := os.MkdirAll(filepath.Dir(hookPath), 0755); err != nil {
		t.Fatal(err)
	}
	outsidePath := filepath.Join(t.TempDir(), "outside-hook")
	outsideContent := "#!/bin/sh\n# NTM_MANAGED_HOOK\necho outside\n"
	if err := os.WriteFile(outsidePath, []byte(outsideContent), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePath, hookPath); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	err = m.Install(HookPostCheckout, false)
	if err != nil {
		t.Fatalf("Install over existing NTM symlink: %v", err)
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
		t.Fatalf("lstat installed hook: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("installed hook is still a symlink")
	}
	content, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("reading installed hook: %v", err)
	}
	if !isNTMHook(string(content)) {
		t.Fatal("installed hook should contain NTM_MANAGED_HOOK marker")
	}
}

func TestManagerInstall_ExistingForeignHook_NoForce(t *testing.T) {
	t.Parallel()
	repo := initTempGitRepo(t)
	m, err := NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}

	hookPath := filepath.Join(m.HooksDir(), string(HookPostCheckout))
	if err := os.MkdirAll(filepath.Dir(hookPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\necho foreign\n"), 0755); err != nil {
		t.Fatal(err)
	}

	err = m.Install(HookPostCheckout, false)
	if err != ErrHookExists {
		t.Errorf("expected ErrHookExists, got %v", err)
	}
}

func TestManagerInstall_ExistingForeignHook_Force(t *testing.T) {
	t.Parallel()
	repo := initTempGitRepo(t)
	m, err := NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}

	hookPath := filepath.Join(m.HooksDir(), string(HookPostCheckout))
	backupPath := hookPath + ".backup"
	if err := os.MkdirAll(filepath.Dir(hookPath), 0755); err != nil {
		t.Fatal(err)
	}
	foreignContent := "#!/bin/sh\necho foreign\n"
	if err := os.WriteFile(hookPath, []byte(foreignContent), 0755); err != nil {
		t.Fatal(err)
	}

	err = m.Install(HookPostCheckout, true)
	if err != nil {
		t.Fatalf("Install with force: %v", err)
	}

	// Backup should exist
	backup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("reading backup: %v", err)
	}
	if string(backup) != foreignContent {
		t.Errorf("backup content = %q, want %q", string(backup), foreignContent)
	}

	// New hook should be NTM
	content, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if !isNTMHook(string(content)) {
		t.Error("installed hook should contain NTM_MANAGED_HOOK marker")
	}
}

func TestGenerateHookScript_PostCheckout(t *testing.T) {
	t.Parallel()
	script, err := generateHookScript(HookPostCheckout, "/tmp/repo")
	if err != nil {
		t.Fatalf("generateHookScript: %v", err)
	}
	if script == "" {
		t.Error("expected non-empty script")
	}
	if !isNTMHook(script) {
		t.Error("script should contain NTM_MANAGED_HOOK marker")
	}
}

func TestGenerateHookScript_UnsupportedType(t *testing.T) {
	t.Parallel()
	_, err := generateHookScript(HookType("unsupported"), "/tmp/repo")
	if err == nil {
		t.Error("expected error for unsupported hook type")
	}
}

func TestFindGitRoot(t *testing.T) {
	t.Parallel()
	repo := initTempGitRepo(t)

	root, err := findGitRoot(repo)
	if err != nil {
		t.Fatalf("findGitRoot: %v", err)
	}
	if root != repo {
		t.Errorf("root = %q, want %q", root, repo)
	}
}

func TestFindGitRoot_NotGitRepo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	_, err := findGitRoot(dir)
	if err != ErrNotGitRepo {
		t.Errorf("expected ErrNotGitRepo, got %v", err)
	}
}
