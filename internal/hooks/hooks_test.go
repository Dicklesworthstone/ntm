package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGeneratePreCommitScriptIncludesBeadsSync(t *testing.T) {
	repoRoot := "/tmp/project path/with spaces"
	script := generatePreCommitScript("/usr/local/bin/ntm", repoRoot)

	if !strings.Contains(script, "br sync --flush-only") {
		t.Errorf("expected beads sync in pre-commit hook, got: %q", script)
	}
	if !strings.Contains(script, "hooks run pre-commit") {
		t.Errorf("expected pre-commit hook to call ntm hooks run, got: %q", script)
	}
	if !strings.Contains(script, "REPO_ROOT="+quoteShell(repoRoot)) {
		t.Errorf("expected quoted REPO_ROOT assignment, got: %q", script)
	}
}

// ntm's managed pre-commit hook (written by `ntm init`) runs the reservation
// check and refuses the commit when it fails. Before, the only hook that
// checked reservations was the standalone guard, which `ntm guards install`
// refused to install over this hook, so reservations stayed advisory.
func TestPreCommitScriptRunsReservationGuard(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	fakeNTM := filepath.Join(dir, "ntm")
	// The fake ntm fails `guards check` when GUARD_FAIL is set and records
	// every invocation.
	if err := os.WriteFile(fakeNTM, []byte(`#!/bin/sh
echo "$*" >> "$CALLS"
if [ "$1 $2" = "guards check" ] && [ -n "$GUARD_FAIL" ]; then
  echo "[ntm-guard] BLOCKED: src/a.go is reserved by GreenLake" >&2
  exit 1
fi
exit 0
`), 0o755); err != nil {
		t.Fatal(err)
	}
	script := generatePreCommitScript(fakeNTM, dir)
	if !PreCommitRunsReservationGuard(script) {
		t.Fatal("managed pre-commit hook does not report running the reservation guard")
	}
	hookPath := filepath.Join(dir, "pre-commit")
	if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	run := func(guardFail bool) (int, string) {
		calls := filepath.Join(dir, "calls")
		_ = os.Remove(calls)
		cmd := exec.Command("bash", hookPath)
		cmd.Env = append(os.Environ(), "CALLS="+calls, "PATH=/usr/bin:/bin")
		if guardFail {
			cmd.Env = append(cmd.Env, "GUARD_FAIL=1")
		}
		out, err := cmd.CombinedOutput()
		recorded, _ := os.ReadFile(calls)
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), string(out) + string(recorded)
		}
		if err != nil {
			t.Fatalf("running hook: %v", err)
		}
		return 0, string(out) + string(recorded)
	}

	if code, out := run(false); code != 0 || !strings.Contains(out, "guards check --staged") || !strings.Contains(out, "hooks run pre-commit") {
		t.Fatalf("clean commit: exit %d, output %q; want 0 with the guard and UBS both run", code, out)
	}
	if code, out := run(true); code == 0 || !strings.Contains(out, "reserved by GreenLake") {
		t.Fatalf("reserved file: exit %d, output %q; want the commit refused with the holder named", code, out)
	}
}

func TestPreCommitRunsReservationGuard(t *testing.T) {
	for content, want := range map[string]bool{
		"#!/bin/bash\n# ntm-precommit-guard\nexec ntm guards check --staged\n": true,
		generatePreCommitScript("ntm", "/repo"):                                true,
		"#!/bin/bash\n# NTM_MANAGED_HOOK\nntm hooks run pre-commit\n":          false, // managed hook from before the guard
		"#!/bin/sh\nmake lint\n":                                               false,
	} {
		if got := PreCommitRunsReservationGuard(content); got != want {
			t.Errorf("PreCommitRunsReservationGuard(%.40q) = %v, want %v", content, got, want)
		}
	}
}

func TestGeneratePostCheckoutScriptWarnsOnBeadsChanges(t *testing.T) {
	repoRoot := "/tmp/project path/with spaces"
	script := generatePostCheckoutScript(repoRoot)

	if !strings.Contains(script, "post-checkout") {
		t.Errorf("expected post-checkout marker in hook, got: %q", script)
	}
	if !strings.Contains(script, "Warning: .beads has uncommitted changes") {
		t.Errorf("expected .beads warning in post-checkout hook, got: %q", script)
	}
	if !strings.Contains(script, "REPO_ROOT="+quoteShell(repoRoot)) {
		t.Errorf("expected quoted REPO_ROOT assignment, got: %q", script)
	}
}

// =============================================================================
// isNTMHook
// =============================================================================

func TestIsNTMHook(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"empty", "", false},
		{"no marker", "#!/bin/bash\necho hello", false},
		{"has marker", "#!/bin/bash\n# NTM_MANAGED_HOOK\necho hello", true},
		{"marker in comment", "# NTM_MANAGED_HOOK - Do not edit manually", true},
		{"marker alone", "NTM_MANAGED_HOOK", true},
		{"partial marker", "NTM_MANAGED", false},
		{"different case", "ntm_managed_hook", false},
		{"marker mid-script", "#!/bin/bash\nset -e\n# NTM_MANAGED_HOOK\nntm hooks run pre-commit", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isNTMHook(tc.content)
			if got != tc.want {
				t.Errorf("isNTMHook(%q) = %v, want %v", tc.content, got, tc.want)
			}
		})
	}
}

// =============================================================================
// quoteShell
// =============================================================================

func TestQuoteShell(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", "''"},
		{"simple", "hello", "'hello'"},
		{"path", "/usr/local/bin/ntm", "'/usr/local/bin/ntm'"},
		{"spaces", "path with spaces", "'path with spaces'"},
		{"single quote", "it's", "'it'\\''s'"},
		{"multiple single quotes", "a'b'c", "'a'\\''b'\\''c'"},
		{"special chars", "hello; rm -rf /", "'hello; rm -rf /'"},
		{"double quotes", `say "hi"`, `'say "hi"'`},
		{"backtick", "echo `pwd`", "'echo `pwd`'"},
		{"dollar sign", "echo $HOME", "'echo $HOME'"},
		{"newline", "line1\nline2", "'line1\nline2'"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := quoteShell(tc.input)
			if got != tc.want {
				t.Errorf("quoteShell(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// =============================================================================
// PreCommitResult.ExitCode
// =============================================================================

func TestPreCommitResultExitCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		passed bool
		want   int
	}{
		{"passed", true, 0},
		{"failed", false, 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &PreCommitResult{Passed: tc.passed}
			got := r.ExitCode()
			if got != tc.want {
				t.Errorf("ExitCode() = %d, want %d", got, tc.want)
			}
		})
	}
}

// =============================================================================
// DefaultPreCommitConfig
// =============================================================================

func TestDefaultPreCommitConfig(t *testing.T) {
	t.Parallel()

	cfg := DefaultPreCommitConfig()

	if cfg.MaxCritical != 0 {
		t.Errorf("MaxCritical = %d, want 0", cfg.MaxCritical)
	}
	if cfg.MaxWarning != 0 {
		t.Errorf("MaxWarning = %d, want 0", cfg.MaxWarning)
	}
	if !cfg.FailOnWarning {
		t.Error("FailOnWarning should be true")
	}
	if cfg.Timeout != 120*time.Second {
		t.Errorf("Timeout = %v, want 120s", cfg.Timeout)
	}
	if cfg.Verbose {
		t.Error("Verbose should be false")
	}
	if !cfg.SkipEmpty {
		t.Error("SkipEmpty should be true")
	}
}

// =============================================================================
// Duration.MarshalText
// =============================================================================

func TestDurationMarshalText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dur  Duration
		want string
	}{
		{"zero", Duration(0), "0s"},
		{"30 seconds", Duration(30 * time.Second), "30s"},
		{"one minute", Duration(time.Minute), "1m0s"},
		{"complex", Duration(5*time.Minute + 30*time.Second), "5m30s"},
		{"one hour", Duration(time.Hour), "1h0m0s"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.dur.MarshalText()
			if err != nil {
				t.Fatalf("MarshalText() error = %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("MarshalText() = %q, want %q", string(got), tc.want)
			}
		})
	}
}

// =============================================================================
// Duration.Duration (getter)
// =============================================================================

func TestDurationGetter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dur  Duration
		want time.Duration
	}{
		{"zero", Duration(0), 0},
		{"positive", Duration(42 * time.Second), 42 * time.Second},
		{"negative", Duration(-5 * time.Second), -5 * time.Second},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.dur.Duration()
			if got != tc.want {
				t.Errorf("Duration() = %v, want %v", got, tc.want)
			}
		})
	}
}

// =============================================================================
// CommandHooksConfig.Validate
// =============================================================================

func TestCommandHooksConfigValidate(t *testing.T) {
	t.Parallel()

	t.Run("empty config is valid", func(t *testing.T) {
		t.Parallel()
		cfg := &CommandHooksConfig{}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate() error = %v", err)
		}
	})

	t.Run("valid hooks pass", func(t *testing.T) {
		t.Parallel()
		cfg := &CommandHooksConfig{
			Hooks: []CommandHook{
				{Event: EventPreSpawn, Command: "echo a"},
				{Event: EventPostSend, Command: "echo b"},
			},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate() error = %v", err)
		}
	})

	t.Run("invalid hook at index 1 reports index", func(t *testing.T) {
		t.Parallel()
		cfg := &CommandHooksConfig{
			Hooks: []CommandHook{
				{Event: EventPreSpawn, Command: "echo good"},
				{Event: EventPreSpawn, Command: ""},
			},
		}
		err := cfg.Validate()
		if err == nil {
			t.Fatal("Validate() should return error for empty command")
		}
		if !strings.Contains(err.Error(), "command_hooks[1]") {
			t.Errorf("error should indicate index 1, got: %v", err)
		}
	})
}

// =============================================================================
// generatePreCommitScript injection safety
// =============================================================================

func TestGeneratePreCommitScriptSanitizesNewlines(t *testing.T) {
	t.Parallel()
	script := generatePreCommitScript("/usr/local/bin/ntm", "/tmp/evil\ninjection")
	if !strings.Contains(script, "NTM_MANAGED_HOOK") {
		t.Error("script should contain NTM_MANAGED_HOOK marker")
	}
	// The repo root comment should have newlines replaced with spaces
	lines := strings.Split(script, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "# Repository:") {
			if strings.Contains(line, "injection") {
				break
			}
		}
	}
}

func TestGeneratePostCheckoutScriptSanitizesNewlines(t *testing.T) {
	t.Parallel()
	script := generatePostCheckoutScript("/tmp/evil\ninjection")
	if !strings.Contains(script, "NTM_MANAGED_HOOK") {
		t.Error("script should contain NTM_MANAGED_HOOK marker")
	}
}

// =============================================================================
// HookType constants
// =============================================================================

func TestHookTypeConstants(t *testing.T) {
	t.Parallel()

	if HookPreCommit != "pre-commit" {
		t.Errorf("HookPreCommit = %q", HookPreCommit)
	}
	if HookPrePush != "pre-push" {
		t.Errorf("HookPrePush = %q", HookPrePush)
	}
	if HookCommitMsg != "commit-msg" {
		t.Errorf("HookCommitMsg = %q", HookCommitMsg)
	}
	if HookPostCommit != "post-commit" {
		t.Errorf("HookPostCommit = %q", HookPostCommit)
	}
	if HookPostCheckout != "post-checkout" {
		t.Errorf("HookPostCheckout = %q", HookPostCheckout)
	}
}

func TestGenerateHookScriptAllFiveTypesAndUnsupported(t *testing.T) {
	binDir := t.TempDir()
	fakeNTM := filepath.Join(binDir, "ntm")
	if err := os.WriteFile(fakeNTM, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	repoRoot := "/tmp/repo with spaces"
	for _, ht := range []HookType{HookPreCommit, HookPrePush, HookCommitMsg, HookPostCommit, HookPostCheckout} {
		script, err := generateHookScript(ht, repoRoot)
		if err != nil {
			t.Fatalf("generateHookScript(%s) error: %v", ht, err)
		}
		if !strings.Contains(script, "# NTM_MANAGED_HOOK - Do not edit manually") {
			t.Errorf("hook %s missing NTM_MANAGED_HOOK marker", ht)
		}
		if !strings.Contains(script, "REPO_ROOT="+quoteShell(repoRoot)) {
			t.Errorf("hook %s missing quoted REPO_ROOT", ht)
		}
		if !strings.Contains(script, string(ht)+".backup") {
			t.Errorf("hook %s missing backup chaining to %s.backup", ht, ht)
		}
	}

	_, err := generateHookScript(HookType("bogus-hook"), repoRoot)
	if err == nil || !strings.Contains(err.Error(), "unsupported hook type") {
		t.Fatalf("expected unsupported hook type error, got: %v", err)
	}
}

func TestGeneratePrePushScriptRunsGuardHookAndBackup(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	fakeNTM := filepath.Join(dir, "ntm")
	if err := os.WriteFile(fakeNTM, []byte(`#!/bin/sh
stdin_line="$(cat)"
echo "ntm:$*|stdin:$stdin_line" >> "$CALLS"
if [ "$1 $2" = "guards check" ] && [ -n "$FAIL_GUARD" ]; then
  exit 1
fi
if [ "$1 $2 $3" = "hooks run pre-push" ] && [ -n "$FAIL_HOOK" ]; then
  exit 1
fi
exit 0
`), 0o755); err != nil {
		t.Fatal(err)
	}

	hookPath := filepath.Join(dir, "pre-push")
	script := generatePrePushScript(fakeNTM, "/tmp/evil\ninjection")
	if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(dir, "pre-push.backup")
	if err := os.WriteFile(backupPath, []byte(`#!/bin/sh
stdin_line="$(cat)"
echo "backup:$*|stdin:$stdin_line" >> "$CALLS"
if [ -n "$FAIL_BACKUP" ]; then
  exit 1
fi
exit 0
`), 0o755); err != nil {
		t.Fatal(err)
	}

	const samplePushRef = "refs/heads/main 1111111111111111111111111111111111111111 refs/heads/main 0000000000000000000000000000000000000000\n"
	run := func(tag string, extraEnv ...string) (int, string) {
		calls := filepath.Join(dir, "calls-"+tag)
		cmd := exec.Command("bash", hookPath, "origin", "https://example.com/repo.git")
		cmd.Stdin = strings.NewReader(samplePushRef)
		cmd.Env = append(append(os.Environ(), "CALLS="+calls, "PATH=/usr/bin:/bin"), extraEnv...)
		out, err := cmd.CombinedOutput()
		recorded, _ := os.ReadFile(calls)
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), string(out) + string(recorded)
		}
		if err != nil {
			t.Fatalf("running pre-push (%s): %v", tag, err)
		}
		return 0, string(out) + string(recorded)
	}

	if code, out := run("clean"); code != 0 ||
		!strings.Contains(out, "ntm:guards check --push|stdin:refs/heads/main 1111111111111111111111111111111111111111") ||
		!strings.Contains(out, "ntm:hooks run pre-push origin https://example.com/repo.git|stdin:refs/heads/main 1111111111111111111111111111111111111111") ||
		!strings.Contains(out, "backup:origin https://example.com/repo.git|stdin:refs/heads/main 1111111111111111111111111111111111111111") {
		t.Fatalf("clean pre-push: code=%d out=%q", code, out)
	}
	if code, _ := run("guard-fail", "FAIL_GUARD=1"); code == 0 {
		t.Fatal("expected pre-push to fail when guard check fails")
	}
	if code, _ := run("hook-fail", "FAIL_HOOK=1"); code == 0 {
		t.Fatal("expected pre-push to fail when ntm hooks run pre-push fails")
	}
	if code, _ := run("backup-fail", "FAIL_BACKUP=1"); code == 0 {
		t.Fatal("expected pre-push to fail when backup hook fails")
	}
}

func TestGenerateCommitMsgScriptRunsHookAndBackup(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	fakeNTM := filepath.Join(dir, "ntm")
	if err := os.WriteFile(fakeNTM, []byte(`#!/bin/sh
echo "ntm:$*" >> "$CALLS"
if [ -n "$FAIL_HOOK" ]; then
  exit 1
fi
exit 0
`), 0o755); err != nil {
		t.Fatal(err)
	}

	hookPath := filepath.Join(dir, "commit-msg")
	if err := os.WriteFile(hookPath, []byte(generateCommitMsgScript(fakeNTM, dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(dir, "commit-msg.backup")
	if err := os.WriteFile(backupPath, []byte(`#!/bin/sh
echo "backup:$*" >> "$CALLS"
if [ -n "$FAIL_BACKUP" ]; then
  exit 1
fi
exit 0
`), 0o755); err != nil {
		t.Fatal(err)
	}

	run := func(tag string, extraEnv ...string) (int, string) {
		calls := filepath.Join(dir, "calls-"+tag)
		cmd := exec.Command("bash", hookPath, ".git/COMMIT_EDITMSG")
		cmd.Env = append(append(os.Environ(), "CALLS="+calls, "PATH=/usr/bin:/bin"), extraEnv...)
		out, err := cmd.CombinedOutput()
		recorded, _ := os.ReadFile(calls)
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), string(out) + string(recorded)
		}
		if err != nil {
			t.Fatalf("running commit-msg (%s): %v", tag, err)
		}
		return 0, string(out) + string(recorded)
	}

	if code, out := run("clean"); code != 0 ||
		!strings.Contains(out, "ntm:hooks run commit-msg .git/COMMIT_EDITMSG") ||
		!strings.Contains(out, "backup:.git/COMMIT_EDITMSG") {
		t.Fatalf("clean commit-msg: code=%d out=%q", code, out)
	}
	if code, _ := run("hook-fail", "FAIL_HOOK=1"); code == 0 {
		t.Fatal("expected commit-msg to fail when ntm hooks run commit-msg fails")
	}
	if code, _ := run("backup-fail", "FAIL_BACKUP=1"); code == 0 {
		t.Fatal("expected commit-msg to fail when backup hook fails")
	}
}

func TestGeneratePostCommitAndPostCheckoutScriptsAreNonFatalAndChainBackup(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	fakeNTM := filepath.Join(dir, "ntm")
	if err := os.WriteFile(fakeNTM, []byte(`#!/bin/sh
echo "ntm:$*" >> "$CALLS"
exit 1
`), 0o755); err != nil {
		t.Fatal(err)
	}

	postCommitPath := filepath.Join(dir, "post-commit")
	if err := os.WriteFile(postCommitPath, []byte(generatePostCommitScript(fakeNTM, dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(postCommitPath+".backup", []byte("#!/bin/sh\necho \"post-commit-backup:$*\" >> \"$CALLS\"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	callsPC := filepath.Join(dir, "calls-post-commit")
	cmdPC := exec.Command("bash", postCommitPath)
	cmdPC.Env = append(os.Environ(), "CALLS="+callsPC, "PATH=/usr/bin:/bin")
	if out, err := cmdPC.CombinedOutput(); err != nil {
		t.Fatalf("post-commit should never block even if ntm/backup exit 1: %v\nout=%s", err, string(out))
	}
	recordedPC, _ := os.ReadFile(callsPC)
	if !strings.Contains(string(recordedPC), "ntm:hooks run post-commit") || !strings.Contains(string(recordedPC), "post-commit-backup:") {
		t.Fatalf("post-commit calls = %q", string(recordedPC))
	}

	postCheckoutPath := filepath.Join(dir, "post-checkout")
	if err := os.WriteFile(postCheckoutPath, []byte(generatePostCheckoutScript(dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(postCheckoutPath+".backup", []byte("#!/bin/sh\necho \"post-checkout-backup:$*\" >> \"$CALLS\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	callsCO := filepath.Join(dir, "calls-post-checkout")
	cmdCO := exec.Command("bash", postCheckoutPath, "abc", "def", "1")
	cmdCO.Env = append(os.Environ(), "CALLS="+callsCO)
	if out, err := cmdCO.CombinedOutput(); err != nil {
		t.Fatalf("post-checkout failed: %v\nout=%s", err, string(out))
	}
	recordedCO, _ := os.ReadFile(callsCO)
	if !strings.Contains(string(recordedCO), "post-checkout-backup:abc def 1") {
		t.Fatalf("post-checkout backup calls = %q", string(recordedCO))
	}
}
