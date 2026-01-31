package cli

import (
	"os"
	"path/filepath"
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
			command: "git push origin master --force-with-lease",
			want:    policy.ActionAllow,
		},
		{
			name:    "Blocked pattern matches",
			command: "git push origin master --force",
			want:    policy.ActionBlock,
		},
		{
			name:    "Approval required",
			command: "git rebase master",
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

func TestEvaluateSafetyCheck_DCGMissing_DoesNotBlockApprovalRequired(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())

	resp, exitCode, err := evaluateSafetyCheck("git commit --amend")
	if err != nil {
		t.Fatalf("evaluateSafetyCheck returned error: %v", err)
	}

	if exitCode != 0 {
		t.Fatalf("expected exitCode=0, got %d", exitCode)
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
if [ "$1" = "check" ]; then
  # dcg check --json <command>
  cmd="$3"
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
