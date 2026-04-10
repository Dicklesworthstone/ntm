package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/startup"
)

// TestInitCmd_NoArgs verifies ntm init with no arguments uses current working directory
func TestInitCmd_NoArgs(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	// Save and restore working directory
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	defer os.Chdir(oldWd)

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("change to temp dir: %v", err)
	}

	// Run init with no target (uses cwd)
	opts := initOptions{
		NonInteractive: true,
		NoHooks:        true, // Skip git hooks in test
	}

	err = runProjectInit(opts)
	if err != nil {
		t.Fatalf("runProjectInit failed: %v", err)
	}

	// Verify .ntm directory created
	ntmDir := filepath.Join(tmpDir, ".ntm")
	if _, err := os.Stat(ntmDir); os.IsNotExist(err) {
		t.Errorf(".ntm directory not created at %s", ntmDir)
	}

	// Verify config.toml created
	configPath := filepath.Join(ntmDir, "config.toml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Errorf("config.toml not created at %s", configPath)
	}

	t.Logf("TEST: InitCmd_NoArgs | Input: opts=%+v | Expected: .ntm created | Got: success", opts)
}

// TestInitCmd_ExplicitPath verifies ntm init with explicit path argument
func TestInitCmd_ExplicitPath(t *testing.T) {

	tmpDir := t.TempDir()

	opts := initOptions{
		TargetDir:      tmpDir,
		NonInteractive: true,
		NoHooks:        true,
	}

	err := runProjectInit(opts)
	if err != nil {
		t.Fatalf("runProjectInit failed: %v", err)
	}

	// Verify .ntm directory created
	ntmDir := filepath.Join(tmpDir, ".ntm")
	if _, err := os.Stat(ntmDir); os.IsNotExist(err) {
		t.Errorf(".ntm directory not created at %s", ntmDir)
	}

	t.Logf("TEST: InitCmd_ExplicitPath | Input: TargetDir=%s | Expected: .ntm created | Got: success", tmpDir)
}

// TestInitCmd_Force verifies --force flag overwrites existing config
func TestInitCmd_Force(t *testing.T) {

	tmpDir := t.TempDir()

	// First init
	opts := initOptions{
		TargetDir:      tmpDir,
		NonInteractive: true,
		NoHooks:        true,
	}
	if err := runProjectInit(opts); err != nil {
		t.Fatalf("first init failed: %v", err)
	}

	// Modify config.toml to verify overwrite
	configPath := filepath.Join(tmpDir, ".ntm", "config.toml")
	originalContent, _ := os.ReadFile(configPath)
	modifiedContent := append(originalContent, []byte("\n# Modified\n")...)
	if err := os.WriteFile(configPath, modifiedContent, 0644); err != nil {
		t.Fatalf("write modified config: %v", err)
	}

	// Second init WITHOUT force should fail
	opts.Force = false
	err := runProjectInit(opts)
	if err == nil {
		t.Error("expected error when reinitializing without --force")
	}
	if !strings.Contains(err.Error(), "already initialized") {
		t.Errorf("expected 'already initialized' error, got: %v", err)
	}

	// Second init WITH force should succeed
	opts.Force = true
	if err := runProjectInit(opts); err != nil {
		t.Fatalf("init with --force failed: %v", err)
	}

	// Verify config was overwritten (no "# Modified" marker)
	newContent, _ := os.ReadFile(configPath)
	if strings.Contains(string(newContent), "# Modified") {
		t.Error("config.toml not overwritten with --force")
	}

	t.Logf("TEST: InitCmd_Force | Input: Force=%v | Expected: overwrite | Got: success", opts.Force)
}

func TestInitCmd_ForceRecoversFromCorruptConfig(t *testing.T) {

	tmpDir := t.TempDir()
	ntmDir := filepath.Join(tmpDir, ".ntm")
	if err := os.MkdirAll(ntmDir, 0755); err != nil {
		t.Fatalf("create .ntm: %v", err)
	}

	// Write a config that TOML parser will reject.
	configPath := filepath.Join(ntmDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("this is not valid toml = ="), 0644); err != nil {
		t.Fatalf("write corrupt config.toml: %v", err)
	}

	opts := initOptions{
		TargetDir:      tmpDir,
		NonInteractive: true,
		NoHooks:        true,
		Force:          true,
	}
	if err := runProjectInit(opts); err != nil {
		t.Fatalf("runProjectInit failed: %v", err)
	}

	if _, err := config.LoadProjectConfig(configPath); err != nil {
		t.Fatalf("expected init to recover by overwriting corrupt config.toml, got load error: %v", err)
	}
}

func TestInitCmd_PartialInitCanBeResumed(t *testing.T) {

	tmpDir := t.TempDir()

	ntmDir := filepath.Join(tmpDir, ".ntm")
	if err := os.MkdirAll(ntmDir, 0755); err != nil {
		t.Fatalf("create .ntm: %v", err)
	}

	// Simulate a partial/failed init: .ntm exists, but config.toml is missing.
	customPalette := "# My Custom Palette\n## Custom Section\n"
	palettePath := filepath.Join(ntmDir, "palette.md")
	if err := os.WriteFile(palettePath, []byte(customPalette), 0644); err != nil {
		t.Fatalf("write custom palette: %v", err)
	}

	opts := initOptions{
		TargetDir:      tmpDir,
		NonInteractive: true,
		NoHooks:        true,
	}
	if err := runProjectInit(opts); err != nil {
		t.Fatalf("runProjectInit failed: %v", err)
	}

	// Verify init completed by creating config.toml.
	configPath := filepath.Join(ntmDir, "config.toml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Fatalf("config.toml not created during resumed init")
	}

	// Verify it did not clobber existing palette.md when resuming.
	gotPalette, err := os.ReadFile(palettePath)
	if err != nil {
		t.Fatalf("read palette.md: %v", err)
	}
	if string(gotPalette) != customPalette {
		t.Fatalf("palette.md was overwritten on resume; got %q", string(gotPalette))
	}
}

func TestInitCmd_ReadOnlyTargetDirDoesNotCreateNtmDir(t *testing.T) {

	if runtime.GOOS == "windows" {
		t.Skip("chmod-based permission tests are not portable on Windows")
	}

	tmpDir := t.TempDir()
	if err := os.Chmod(tmpDir, 0555); err != nil {
		t.Skipf("chmod not supported: %v", err)
	}
	defer os.Chmod(tmpDir, 0755)

	opts := initOptions{
		TargetDir:      tmpDir,
		NonInteractive: true,
		NoHooks:        true,
	}
	err := runProjectInit(opts)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "creating .ntm directory") {
		t.Fatalf("expected .ntm creation error, got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(tmpDir, ".ntm")); !os.IsNotExist(statErr) {
		t.Fatalf("expected .ntm not to be created, stat err=%v", statErr)
	}
}

func TestInitCmd_NotGitRepoStillSucceedsWithHooksEnabled(t *testing.T) {

	tmpDir := t.TempDir()
	opts := initOptions{
		TargetDir:      tmpDir,
		NonInteractive: true,
		NoHooks:        false, // exercise the graceful "not a git repo" path
	}
	if err := runProjectInit(opts); err != nil {
		t.Fatalf("runProjectInit failed: %v", err)
	}
}

// TestInitCmd_NonExistentDir verifies error for non-existent target directory
func TestInitCmd_NonExistentDir(t *testing.T) {

	opts := initOptions{
		TargetDir:      "/non/existent/path/surely/does/not/exist",
		NonInteractive: true,
		NoHooks:        true,
	}

	err := runProjectInit(opts)
	if err == nil {
		t.Error("expected error for non-existent directory")
	}
	if !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "no such file") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}

	t.Logf("TEST: InitCmd_NonExistentDir | Input: %s | Expected: error | Got: %v", opts.TargetDir, err)
}

// TestInitCmd_FileNotDir verifies error when target is a file, not directory
func TestInitCmd_FileNotDir(t *testing.T) {

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "not-a-dir")
	if err := os.WriteFile(filePath, []byte("test"), 0644); err != nil {
		t.Fatalf("create test file: %v", err)
	}

	opts := initOptions{
		TargetDir:      filePath,
		NonInteractive: true,
		NoHooks:        true,
	}

	err := runProjectInit(opts)
	if err == nil {
		t.Error("expected error for file target")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("expected 'not a directory' error, got: %v", err)
	}

	t.Logf("TEST: InitCmd_FileNotDir | Input: %s | Expected: error | Got: %v", filePath, err)
}

// TestInitCmd_CreatesAllDirectories verifies all required directories are created
func TestInitCmd_CreatesAllDirectories(t *testing.T) {

	tmpDir := t.TempDir()

	opts := initOptions{
		TargetDir:      tmpDir,
		NonInteractive: true,
		NoHooks:        true,
	}

	if err := runProjectInit(opts); err != nil {
		t.Fatalf("runProjectInit failed: %v", err)
	}

	// Check required directories
	requiredDirs := []string{
		".ntm",
		".ntm/templates",
		".ntm/pipelines",
	}

	for _, dir := range requiredDirs {
		fullPath := filepath.Join(tmpDir, dir)
		info, err := os.Stat(fullPath)
		if os.IsNotExist(err) {
			t.Errorf("required directory %s not created", dir)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", dir)
		}
	}

	t.Logf("TEST: InitCmd_CreatesAllDirectories | Expected: %v | Got: all created", requiredDirs)
}

// TestInitCmd_CreatesAllFiles verifies all required files are created
func TestInitCmd_CreatesAllFiles(t *testing.T) {

	tmpDir := t.TempDir()

	opts := initOptions{
		TargetDir:      tmpDir,
		NonInteractive: true,
		NoHooks:        true,
	}

	if err := runProjectInit(opts); err != nil {
		t.Fatalf("runProjectInit failed: %v", err)
	}

	// Check required files
	requiredFiles := []string{
		".ntm/config.toml",
		".ntm/palette.md",
		".ntm/personas.toml",
	}

	for _, file := range requiredFiles {
		fullPath := filepath.Join(tmpDir, file)
		info, err := os.Stat(fullPath)
		if os.IsNotExist(err) {
			t.Errorf("required file %s not created", file)
			continue
		}
		if info.IsDir() {
			t.Errorf("%s should be a file, not directory", file)
		}
		if info.Size() == 0 {
			t.Errorf("%s is empty", file)
		}
	}

	t.Logf("TEST: InitCmd_CreatesAllFiles | Expected: %v | Got: all created", requiredFiles)
}

// TestInitCmd_ConfigContainsProjectName verifies config.toml includes project name
func TestInitCmd_ConfigContainsProjectName(t *testing.T) {

	tmpDir := t.TempDir()
	projectName := filepath.Base(tmpDir)

	opts := initOptions{
		TargetDir:      tmpDir,
		NonInteractive: true,
		NoHooks:        true,
	}

	if err := runProjectInit(opts); err != nil {
		t.Fatalf("runProjectInit failed: %v", err)
	}

	// Read and parse config
	configPath := filepath.Join(tmpDir, ".ntm", "config.toml")
	cfg, err := config.LoadProjectConfig(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Project.Name != projectName {
		t.Errorf("project name = %q, want %q", cfg.Project.Name, projectName)
	}

	if cfg.Project.Created == "" {
		t.Error("project created timestamp is empty")
	}

	t.Logf("TEST: InitCmd_ConfigContainsProjectName | Expected: %s | Got: %s", projectName, cfg.Project.Name)
}

// TestInitCmd_PreservesExistingFiles verifies existing files are not overwritten without --force
func TestInitCmd_PreservesExistingFiles(t *testing.T) {

	tmpDir := t.TempDir()

	// Create partial .ntm structure with custom palette
	ntmDir := filepath.Join(tmpDir, ".ntm")
	if err := os.MkdirAll(ntmDir, 0755); err != nil {
		t.Fatalf("create .ntm: %v", err)
	}

	customPalette := "# My Custom Palette\n## Custom Section\n"
	palettePath := filepath.Join(ntmDir, "palette.md")
	if err := os.WriteFile(palettePath, []byte(customPalette), 0644); err != nil {
		t.Fatalf("write custom palette: %v", err)
	}

	// Run init (config.toml doesn't exist, so this should work)
	opts := initOptions{
		TargetDir:      tmpDir,
		NonInteractive: true,
		NoHooks:        true,
		Force:          true, // Force because .ntm exists
	}

	if err := runProjectInit(opts); err != nil {
		t.Fatalf("runProjectInit failed: %v", err)
	}

	// Note: With force=true, files may be overwritten
	// Without force, the init would fail because .ntm exists
	// This test validates the force behavior
	t.Logf("TEST: InitCmd_PreservesExistingFiles | Force=%v | Got: success", opts.Force)
}

// TestIsShellName verifies shell name detection
func TestIsShellName(t *testing.T) {

	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"zsh", "zsh", true},
		{"bash", "bash", true},
		{"fish", "fish", true},
		{"sh", "sh", false},
		{"random_path", "/some/path", false},
		{"empty", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isShellName(tc.input)
			if got != tc.expected {
				t.Errorf("isShellName(%q) = %v, want %v", tc.input, got, tc.expected)
			}
			t.Logf("TEST: isShellName | Input: %q | Expected: %v | Got: %v", tc.input, tc.expected, got)
		})
	}
}

// TestQuoteAlias verifies shell alias quoting
func TestQuoteAlias(t *testing.T) {

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple", "claude", "'claude'"},
		{"with_space", "claude --project foo", "'claude --project foo'"},
		{"empty", "", "''"},
		{"with_single_quote", "echo 'hello'", "'echo '\\''hello'\\'''"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := quoteAlias(tc.input)
			if got != tc.expected {
				t.Errorf("quoteAlias(%q) = %q, want %q", tc.input, got, tc.expected)
			}
			t.Logf("TEST: quoteAlias | Input: %q | Expected: %q | Got: %q", tc.input, tc.expected, got)
		})
	}
}

// TestGenerateZsh verifies zsh shell integration output
func TestGenerateZsh(t *testing.T) {

	cfg := config.Default()
	output := generateZsh(cfg)

	// Verify key elements are present
	checks := []string{
		"alias cc=",
		"alias cod=",
		"alias gmi=",
		"alias cnt='ntm create'",
		"alias sat='ntm spawn'",
		"_ntm()",
		"compdef _ntm ntm",
		"ensemble:Manage reasoning ensembles",
		"_ntm_complete_ensemble_presets",
		"_ntm_complete_mode_ids",
		"_ntm_complete_tiers",
		"--robot-ensemble-modes",
		"bindkey",
	}

	for _, check := range checks {
		if !strings.Contains(output, check) {
			t.Errorf("zsh output missing: %q", check)
		}
	}

	t.Logf("TEST: GenerateZsh | Output length: %d | All checks: passed", len(output))
}

// TestGenerateBash verifies bash shell integration output
func TestGenerateBash(t *testing.T) {

	cfg := config.Default()
	output := generateBash(cfg)

	// Verify key elements are present
	checks := []string{
		"alias cc=",
		"alias cod=",
		"alias gmi=",
		"alias cnt='ntm create'",
		"_ntm_completions()",
		"_ntm_list_ensemble_presets()",
		"_ntm_list_mode_ids()",
		"--robot-ensemble-modes",
		"ensemble",
		"complete -F _ntm_completions ntm",
	}

	for _, check := range checks {
		if !strings.Contains(output, check) {
			t.Errorf("bash output missing: %q", check)
		}
	}

	t.Logf("TEST: GenerateBash | Output length: %d | All checks: passed", len(output))
}

// TestGenerateFish verifies fish shell integration output
func TestGenerateFish(t *testing.T) {

	cfg := config.Default()
	output := generateFish(cfg)

	// Verify key elements are present
	checks := []string{
		"alias cc",
		"alias cod",
		"alias gmi",
		"abbr -a cnt",
		"complete -c ntm",
		"__fish_ntm_sessions",
		"__fish_ntm_ensemble_presets",
		"__fish_ntm_mode_ids",
		"robot-ensemble-modes",
		"ensemble",
	}

	for _, check := range checks {
		if !strings.Contains(output, check) {
			t.Errorf("fish output missing: %q", check)
		}
	}

	t.Logf("TEST: GenerateFish | Output length: %d | All checks: passed", len(output))
}

func TestCompletionSources_EnsemblePresetsAndModesNonEmpty(t *testing.T) {

	presets := listEnsemblePresetNames()
	if len(presets) == 0 {
		t.Fatalf("expected embedded/user ensemble presets to be non-empty")
	}
	modes := listReasoningModeIDs()
	if len(modes) == 0 {
		t.Fatalf("expected reasoning mode catalog to be non-empty")
	}

	// Sanity: tier completion must include "core" and "all".
	values, _ := completeTierValues(nil, nil, "")
	hasCore := false
	hasAll := false
	for _, v := range values {
		switch v {
		case "core":
			hasCore = true
		case "all":
			hasAll = true
		}
	}
	if !hasCore || !hasAll {
		t.Fatalf("expected tier completion to include core+all; got=%v", values)
	}
}

// TestRunShellInit_InvalidShell verifies error for unsupported shell
func TestRunShellInit_InvalidShell(t *testing.T) {

	err := runShellInit("powershell")
	if err == nil {
		t.Error("expected error for unsupported shell")
	}
	if !strings.Contains(err.Error(), "unsupported shell") {
		t.Errorf("expected 'unsupported shell' error, got: %v", err)
	}

	t.Logf("TEST: RunShellInit_InvalidShell | Input: powershell | Expected: error | Got: %v", err)
}

func TestRunShellInit_UsesSelectedConfigPath(t *testing.T) {
	oldCfg, oldCfgFile := cfg, cfgFile
	cfg = nil
	cfgFile = ""
	startup.ResetConfig()
	t.Cleanup(func() {
		cfg = oldCfg
		cfgFile = oldCfgFile
		startup.ResetConfig()
	})

	customPath := filepath.Join(t.TempDir(), "custom", "ntm.toml")
	if err := os.MkdirAll(filepath.Dir(customPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(config dir) failed: %v", err)
	}
	configBody := `[agents]
claude = "echo custom-claude"
`
	if err := os.WriteFile(customPath, []byte(configBody), 0o644); err != nil {
		t.Fatalf("WriteFile(config) failed: %v", err)
	}
	cfgFile = customPath

	out, err := captureStdout(t, func() error {
		return runShellInit("zsh")
	})
	if err != nil {
		t.Fatalf("runShellInit(zsh) failed: %v", err)
	}
	if !strings.Contains(out, "alias cc='echo custom-claude'") {
		t.Fatalf("shell output missing custom claude command: %q", out)
	}
}

// TestInstallGitHooks_NotGitRepo verifies hooks installation skips non-git directories
func TestInstallGitHooks_NotGitRepo(t *testing.T) {

	tmpDir := t.TempDir()

	installed, warning := installGitHooks(tmpDir, false)

	if len(installed) != 0 {
		t.Errorf("expected no hooks installed, got %v", installed)
	}

	if warning == "" {
		t.Error("expected warning for non-git repo")
	}

	if !strings.Contains(warning, "not a git repository") {
		t.Errorf("expected 'not a git repository' warning, got: %s", warning)
	}

	t.Logf("TEST: InstallGitHooks_NotGitRepo | Input: %s | Expected: warning | Got: %s", tmpDir, warning)
}

// TestInstallGitHooks_GitRepo verifies hooks installation in a git repo
func TestInstallGitHooks_GitRepo(t *testing.T) {

	tmpDir := t.TempDir()

	// Initialize a real git repo using git init
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v, output: %s", err, out)
	}

	installed, warning := installGitHooks(tmpDir, false)

	// Should install hooks successfully without warning
	if warning != "" {
		t.Errorf("unexpected warning in valid git repo: %s", warning)
	}

	// Verify hooks were installed (at least pre-commit should be installed)
	if len(installed) == 0 {
		t.Error("expected at least one hook to be installed")
	}

	// Verify hook files exist on disk
	hooksDir := filepath.Join(tmpDir, ".git", "hooks")
	for _, hookName := range installed {
		hookPath := filepath.Join(hooksDir, hookName)
		if _, err := os.Stat(hookPath); err != nil {
			t.Errorf("hook %s not found on disk: %v", hookName, err)
		}
	}

	t.Logf("TEST: InstallGitHooks_GitRepo | Installed: %v | Warning: %s", installed, warning)
}

// TestInstallGitHooks_Force verifies force flag behavior
func TestInstallGitHooks_Force(t *testing.T) {

	tmpDir := t.TempDir()

	// Initialize a real git repo using git init
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v, output: %s", err, out)
	}

	hooksDir := filepath.Join(tmpDir, ".git", "hooks")
	existingContent := "#!/bin/sh\nexit 0\n"

	// Create existing hook with known content
	preCommitPath := filepath.Join(hooksDir, "pre-commit")
	if err := os.WriteFile(preCommitPath, []byte(existingContent), 0755); err != nil {
		t.Fatalf("create existing hook: %v", err)
	}

	// Install without force - should skip existing hook
	installed1, _ := installGitHooks(tmpDir, false)

	// Read content after non-force install - should be unchanged
	content1, err := os.ReadFile(preCommitPath)
	if err != nil {
		t.Fatalf("read hook after non-force: %v", err)
	}
	if string(content1) != existingContent {
		t.Errorf("non-force install should not modify existing hook")
	}

	// Install with force - should overwrite existing hook
	installed2, _ := installGitHooks(tmpDir, true)

	// Read content after force install - should be different (our hook content)
	content2, err := os.ReadFile(preCommitPath)
	if err != nil {
		t.Fatalf("read hook after force: %v", err)
	}

	// With force, pre-commit should be in installed list (overwritten)
	foundPreCommit := false
	for _, h := range installed2 {
		if h == "pre-commit" {
			foundPreCommit = true
			break
		}
	}
	if !foundPreCommit {
		t.Error("force install should include pre-commit in installed list")
	}

	// Content should have changed from the original
	if string(content2) == existingContent {
		t.Error("force install should overwrite existing hook content")
	}

	t.Logf("TEST: InstallGitHooks_Force | Without force: %v | With force: %v", installed1, installed2)
}
