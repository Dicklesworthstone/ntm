package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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

func TestClaudeHookScriptReadsCurrentStdinPayload(t *testing.T) {
	for _, want := range []string{
		"HOOK_INPUT=\"$(cat)\"",
		"'.tool_name // empty'",
		"'.tool_input.command // empty'",
		"exit 2",
	} {
		if !strings.Contains(claudeHookScript, want) {
			t.Fatalf("claude hook script missing %q", want)
		}
	}
	if strings.Contains(claudeHookScript, "exit 1\nfi\n\nexit 0") {
		t.Fatal("claude hook script still uses non-blocking exit 1 for denied commands")
	}
}

func TestClaudeHookScriptBlocksCurrentStdinPayload(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	dir := t.TempDir()
	fakeNTM := filepath.Join(dir, "ntm")
	fakeScript := `#!/bin/sh
if [ "${1:-}" = "safety" ] && [ "${2:-}" = "check" ]; then
  echo '{"action":"block","reason":"blocked by fake safety"}'
  exit 1
fi
exit 0
`
	if err := os.WriteFile(fakeNTM, []byte(fakeScript), 0o755); err != nil {
		t.Fatalf("write fake ntm: %v", err)
	}

	hookPath := filepath.Join(dir, "ntm-safety.sh")
	if err := os.WriteFile(hookPath, []byte(claudeHookScript), 0o755); err != nil {
		t.Fatalf("write hook script: %v", err)
	}

	cmd := exec.Command("bash", hookPath)
	cmd.Env = append(os.Environ(),
		"HOME="+dir,
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"echo ok"}}`)

	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("hook script allowed fake blocked command; output=%s", output)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("hook script error has type %T: %v", err, err)
	}
	if exitErr.ExitCode() != 2 {
		t.Fatalf("hook script exit code = %d, want 2; output=%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), "BLOCKED: blocked by fake safety") {
		t.Fatalf("hook script output = %q, want blocked reason", output)
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
