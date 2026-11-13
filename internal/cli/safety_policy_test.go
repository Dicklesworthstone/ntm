package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/policy"
)

func TestPolicyPrecedence(t *testing.T) {
	// Create a temporary policy file with conflicting rules
	content := `
version: 1
allowed:
  - pattern: 'git\s+push\s+.*--force-with-lease'
blocked:
  - pattern: 'git\s+push\s+.*--force'
approval_required:
  - pattern: 'git\s+rebase'
`
	tmpDir := t.TempDir()
	policyPath := filepath.Join(tmpDir, "policy.yaml")
	if err := os.WriteFile(policyPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write policy: %v", err)
	}

	p, err := policy.Load(policyPath)
	if err != nil {
		t.Fatalf("failed to load policy: %v", err)
	}

	tests := []struct {
		name    string
		command string
		want    policy.Action
	}{
		{
			name:    "Allowed takes precedence over blocked",
			command: "git push origin main --force-with-lease",
			want:    policy.ActionAllow,
		},
		{
			name:    "Blocked pattern matches",
			command: "git push origin main --force",
			want:    policy.ActionBlock,
		},
		{
			name:    "Approval required",
			command: "git rebase main",
			want:    policy.ActionApprove,
		},
		{
			name:    "Implicitly allowed",
			command: "ls -la",
			want:    "", // nil match
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			match := p.Check(tt.command)
			if tt.want == "" {
				if match != nil {
					t.Errorf("Check(%q) = %v, want nil", tt.command, match)
				}
			} else {
				if match == nil {
					t.Errorf("Check(%q) = nil, want %v", tt.command, tt.want)
				} else if match.Action != tt.want {
					t.Errorf("Check(%q) action = %v, want %v", tt.command, match.Action, tt.want)
				}
			}
		})
	}
}

func TestEvaluateSafetyCheck_DCGMissing_PreservesApprovalRequired(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())

	resp, exitCode, err := evaluateSafetyCheck("git commit --amend")
	if err != nil {
		t.Fatalf("evaluateSafetyCheck returned error: %v", err)
	}

	if exitCode != 1 {
		t.Fatalf("expected exitCode=1, got %d", exitCode)
	}

	if resp.Action != string(policy.ActionApprove) {
		t.Fatalf("expected action=%s, got %q", policy.ActionApprove, resp.Action)
	}

	if resp.DCG == nil {
		t.Fatalf("expected dcg verdict to be present for dangerous commands")
	}
	if resp.DCG.Available {
		t.Fatalf("expected dcg.available=false when dcg missing")
	}
}

func TestEvaluateSafetyCheck_DCGBlocks_PromotesApprovalToBlock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()
	dcgPath := filepath.Join(dir, "dcg")
	script := `#!/bin/sh
if [ "${1:-}" = "--robot" ]; then
  shift
fi

if [ "${1:-}" = "test" ]; then
  shift
  cmd=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --format)
        shift 2
        ;;
      *)
        cmd="$1"
        shift
        ;;
    esac
  done
  echo "{\"command\":\"$cmd\",\"reason\":\"blocked by fake dcg\"}"
  exit 1
fi
exit 0
`
	if err := os.WriteFile(dcgPath, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake dcg: %v", err)
	}

	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	resp, exitCode, err := evaluateSafetyCheck("git commit --amend")
	if err != nil {
		t.Fatalf("evaluateSafetyCheck returned error: %v", err)
	}

	if exitCode != 1 {
		t.Fatalf("expected exitCode=1, got %d", exitCode)
	}

	if resp.Action != string(policy.ActionBlock) {
		t.Fatalf("expected action=%s, got %q", policy.ActionBlock, resp.Action)
	}
	if resp.Pattern != "dcg" {
		t.Fatalf("expected pattern=dcg, got %q", resp.Pattern)
	}
	if resp.Reason != "blocked by fake dcg" {
		t.Fatalf("expected reason from dcg, got %q", resp.Reason)
	}

	if resp.Policy == nil || resp.Policy.Action != string(policy.ActionApprove) {
		t.Fatalf("expected policy verdict to reflect approval_required; got %+v", resp.Policy)
	}
	if resp.DCG == nil || !resp.DCG.Available || !resp.DCG.Checked || !resp.DCG.Blocked {
		t.Fatalf("expected dcg verdict populated and blocked=true; got %+v", resp.DCG)
	}
}

// The installed hook script hands Claude Code's stdin payload to `ntm safety
// claude-hook` and passes its exit status through; without ntm on PATH it
// refuses (exit 2) instead of letting the command run unchecked.
func TestClaudeHookScriptDelegatesStdinAndFailsClosed(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	hookPath := filepath.Join(dir, "ntm-safety.sh")
	if err := os.WriteFile(hookPath, []byte(policy.ClaudeHookScript), 0o755); err != nil {
		t.Fatalf("write hook script: %v", err)
	}
	payload := `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git reset --hard"}}`

	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeNTM := `#!/bin/sh
printf '%s\n' "$*" > "$HOME/ntm-args"
cat > "$HOME/ntm-stdin"
echo "BLOCKED: fake refusal" >&2
exit 2
`
	if err := os.WriteFile(filepath.Join(binDir, "ntm"), []byte(fakeNTM), 0o755); err != nil {
		t.Fatal(err)
	}
	newHookCmd := func() *exec.Cmd {
		if runtime.GOOS == "windows" {
			return exec.Command("sh", hookPath)
		}
		return exec.Command(hookPath)
	}

	cmd := newHookCmd()
	cmd.Env = []string{"HOME=" + dir, "PATH=" + binDir + string(os.PathListSeparator) + "/usr/bin:/bin"}
	cmd.Stdin = strings.NewReader(payload)
	out, err := cmd.CombinedOutput()
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("hook with ntm: err = %v, want exit 2; output=%s", err, out)
	}
	if args, _ := os.ReadFile(filepath.Join(dir, "ntm-args")); strings.TrimSpace(string(args)) != "safety claude-hook" {
		t.Fatalf("hook ran ntm with %q, want `safety claude-hook`", args)
	}
	if stdin, _ := os.ReadFile(filepath.Join(dir, "ntm-stdin")); string(stdin) != payload {
		t.Fatalf("ntm received stdin %q, want the hook payload", stdin)
	}

	cmd = newHookCmd()
	cmd.Env = []string{"HOME=" + dir, "PATH=/nonexistent"}
	cmd.Stdin = strings.NewReader(payload)
	out, err = cmd.CombinedOutput()
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("hook without ntm: err = %v, want exit 2 (refuse); output=%s", err, out)
	}
	if !strings.Contains(string(out), "could not find ntm") || !strings.Contains(string(out), "settings.json") {
		t.Fatalf("hook without ntm said %q, want how to fix or remove it", out)
	}
}

// runSafetyClaudeHook is what Claude Code's PreToolUse hook runs: a blocked
// Bash command exits 2 with the reason on stderr and is recorded; allowed
// commands and other tools pass; an unreadable payload is refused.
func TestRunSafetyClaudeHook(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("NTM_CONFIG", filepath.Join(root, "cfg", "config.toml"))
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("PATH", "/nonexistent") // no dcg: the policy alone decides

	run := func(payload string) (int, string) {
		var stderr strings.Builder
		code := runSafetyClaudeHook(strings.NewReader(payload), &stderr)
		return code, stderr.String()
	}

	code, msg := run(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git reset --hard HEAD~3"}}`)
	if code != claudeHookRefuse || !strings.Contains(msg, "BLOCKED: Hard reset loses uncommitted changes") {
		t.Fatalf("blocked command: code=%d stderr=%q, want 2 and the policy reason", code, msg)
	}
	entries, err := policy.ReadBlockedLog(filepath.Join(root, ".ntm", "logs", "blocked.jsonl"))
	if err != nil || len(entries) != 1 || entries[0].Command != "git reset --hard HEAD~3" {
		t.Fatalf("blocked log = %+v (err %v), want the refusal recorded", entries, err)
	}

	for name, payload := range map[string]string{
		"allowed command": `{"tool_name":"Bash","tool_input":{"command":"git status"}}`,
		"other tool":      `{"tool_name":"Edit","tool_input":{"file_path":"x.go"}}`,
		"empty command":   `{"tool_name":"Bash","tool_input":{"command":"  "}}`,
	} {
		if code, msg := run(payload); code != 0 || msg != "" {
			t.Errorf("%s: code=%d stderr=%q, want 0 and silence", name, code, msg)
		}
	}

	if code, msg := run(`{not json`); code != claudeHookRefuse || !strings.Contains(msg, "not checked") {
		t.Fatalf("malformed payload: code=%d stderr=%q, want a refusal", code, msg)
	}
}

// `ntm safety install` registers the hook in ~/.claude/settings.json (Claude
// Code never runs unregistered scripts), status reports it only when both the
// script and the registration exist, and uninstall removes both.
func TestSafetyInstallRegistersClaudeHookAndUninstallRemovesIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("NTM_CONFIG", filepath.Join(home, "cfg", "config.toml"))
	settings := policy.ClaudeUserSettingsPath(home)
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"model":"opus"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	if _, err := captureStdout(t, func() error { return runSafetyInstall(false) }); err != nil {
		t.Fatalf("install: %v", err)
	}
	script := policy.ClaudeHookScriptPath(home)
	if registered, err := policy.ClaudeHookRegistered(settings, script); err != nil || !registered {
		t.Fatalf("after install registered = %v, %v; want true", registered, err)
	}
	if data, _ := os.ReadFile(settings); !strings.Contains(string(data), `"model": "opus"`) {
		t.Fatalf("install lost the user's settings: %s", data)
	}

	out, err := captureStdout(t, func() error { return runSafetyStatus(nil, nil) })
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, `"hook_installed": true`) || !strings.Contains(out, `"hook_registered": true`) {
		t.Fatalf("status after install = %s, want the hook installed and registered", out)
	}

	if _, err := captureStdout(t, func() error { return runSafetyUninstall(nil, nil) }); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if registered, _ := policy.ClaudeHookRegistered(settings, script); registered {
		t.Fatal("uninstall left the hook registered")
	}
	if _, err := os.Stat(script); !os.IsNotExist(err) {
		t.Fatalf("uninstall left the hook script (stat err %v)", err)
	}
	if data, _ := os.ReadFile(settings); strings.TrimSpace(string(data)) != "{\n  \"model\": \"opus\"\n}" {
		t.Fatalf("settings after uninstall = %q, want only the user's key", data)
	}

	// A script nobody registered is reported, not counted as protection.
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte(policy.ClaudeHookScript), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err = captureStdout(t, func() error { return runSafetyStatus(nil, nil) })
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, `"hook_installed": false`) || !strings.Contains(out, `"hook_script_present": true`) || !strings.Contains(out, `"hook_registered": false`) {
		t.Fatalf("status with an unregistered script = %s", out)
	}
}

// recordHookRefusal runs inside an agent's tool-call hook, so a tmux server
// that does not answer must not hang it: the refusal is still logged, without
// a session.
func TestRecordHookRefusalDoesNotHangOnStuckTmux(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("NTM_CONFIG", filepath.Join(root, "cfg", "config.toml"))
	fakeTmux := filepath.Join(root, "tmux")
	if err := os.WriteFile(fakeTmux, []byte("#!/bin/sh\nexec sleep 10\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", fakeTmux)
	t.Setenv("TMUX", "/tmp/fake-tmux-socket,1,0")
	t.Setenv("TMUX_PANE", "%7")

	start := time.Now()
	recordHookRefusal(CheckResponse{Command: "rm -rf build", Action: "block", Reason: "recursive delete"})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("recordHookRefusal took %v with an unresponsive tmux", elapsed)
	}
	entries, err := policy.ReadBlockedLog(filepath.Join(root, ".ntm", "logs", "blocked.jsonl"))
	if err != nil || len(entries) != 1 || entries[0].Session != "" || entries[0].Command != "rm -rf build" {
		t.Fatalf("blocked log entries = %+v (err %v), want the refusal logged without a session", entries, err)
	}
}

// A refusal checked in hook mode lands in the blocked log with the tmux session
// and pane it came from, and in that session's blocked_commands metric. The
// scripts used to log session "unknown" and the metric never saw these.
func TestRecordHookRefusalAttributesSessionAndCountsMetric(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("NTM_CONFIG", filepath.Join(root, "cfg", "config.toml"))
	fakeTmux := filepath.Join(root, "tmux")
	if err := os.WriteFile(fakeTmux, []byte("#!/bin/sh\necho hooksess\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", fakeTmux)
	t.Setenv("TMUX", "/tmp/fake-tmux-socket,1,0")
	t.Setenv("TMUX_PANE", "%7")

	recordHookRefusal(CheckResponse{Command: `git reset --hard "HEAD~1"`, Action: "block", Pattern: `git\s+reset\s+--hard`, Reason: "destroys uncommitted work"})

	entries, err := policy.ReadBlockedLog(filepath.Join(root, ".ntm", "logs", "blocked.jsonl"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("blocked log entries = %+v (err %v), want one", entries, err)
	}
	if got := entries[0]; got.Session != "hooksess" || got.Agent != "%7" || got.Command != `git reset --hard "HEAD~1"` || got.Action != policy.ActionBlock {
		t.Fatalf("blocked log entry = %+v, want session hooksess, pane %%7, the exact command, action block", got)
	}

	store, collector, err := getMetricsCollector("hooksess")
	if err != nil || store == nil {
		t.Fatalf("getMetricsCollector: store=%v err=%v", store, err)
	}
	defer store.Close()
	report, err := collector.GenerateReport()
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}
	if report.BlockedCommands != 1 {
		t.Fatalf("BlockedCommands = %d, want the one hook refusal", report.BlockedCommands)
	}
}

func TestSafetySimulationCommandsPreserveMalformedStep(t *testing.T) {
	got := safetySimulationCommands("git status", []string{"git reset --hard HEAD~1", ""})
	want := []string{"git status", "git reset --hard HEAD~1", ""}
	if len(got) != len(want) {
		t.Fatalf("commands = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("commands[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSafetyInstallationState(t *testing.T) {
	home := t.TempDir()
	wrapperDir := filepath.Join(home, ".ntm", "bin")
	if err := os.MkdirAll(wrapperDir, 0o755); err != nil {
		t.Fatalf("create wrapper dir: %v", err)
	}

	assertState := func(wantInstalled, wantEffective bool, wantState string) {
		t.Helper()
		installed, effective, state := safetyInstallationState(wrapperDir, false)
		if installed != wantInstalled || effective != wantEffective || state != wantState {
			t.Fatalf("safetyInstallationState() = (%t, %t, %q), want (%t, %t, %q)", installed, effective, state, wantInstalled, wantEffective, wantState)
		}
	}

	assertState(false, false, safetyNotInstalled)

	for _, name := range []string{"git", "rm"} {
		path := filepath.Join(wrapperDir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write %s wrapper: %v", name, err)
		}
	}

	t.Setenv("PATH", t.TempDir())
	assertState(true, false, safetyInstalledNotEffective)

	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	assertState(true, true, safetyEffective)
}

func TestSafetyInstallationState_HookOnlyIsNotEffective(t *testing.T) {
	installed, effective, state := safetyInstallationState(t.TempDir(), true)
	if !installed || effective || state != safetyInstalledNotEffective {
		t.Fatalf("hook-only safetyInstallationState() = (%t, %t, %q), want (true, false, %q)", installed, effective, state, safetyInstalledNotEffective)
	}
}

func TestEvaluateSafetySimulationReportsUnsafePlan(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	report, err := evaluateSafetySimulation([]string{
		"git status",
		"git reset --hard HEAD~1",
		"git commit --amend",
		"",
	})
	if err != nil {
		t.Fatalf("evaluateSafetySimulation returned error: %v", err)
	}

	if report.SafeToRun {
		t.Fatal("SafeToRun = true, want false")
	}
	if report.Summary.AllowedSteps != 1 || report.Summary.BlockedSteps != 1 ||
		report.Summary.ApprovalSteps != 1 || report.Summary.InvalidSteps != 1 {
		t.Fatalf("summary = %+v, want one allowed, blocked, approval, and invalid", report.Summary)
	}
	if len(report.Steps) != 4 {
		t.Fatalf("steps = %d, want 4", len(report.Steps))
	}
	if len(report.Steps[1].SaferAlternatives) == 0 {
		t.Fatalf("blocked step missing safer alternatives: %+v", report.Steps[1])
	}
}
