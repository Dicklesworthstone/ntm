package agent

import (
	"strings"
	"testing"
)

func TestDefaultGrokAutomationPolicyIsLeastPrivilege(t *testing.T) {
	policy := DefaultGrokAutomationPolicy()
	if policy.Name != DefaultGrokAutomationPolicyName || policy.Sandbox != "read-only" || policy.PermissionMode != "dontAsk" {
		t.Fatalf("policy = %+v", policy)
	}
	command := DefaultGrokAutomationCommandTemplate
	for _, required := range []string{
		"--no-auto-update", "--sandbox read-only", "--permission-mode dontAsk", "--allow 'Read'",
		"--deny 'Edit'", "--deny 'Bash(*)'", "--deny 'Read(**/.grok/**)'",
		"--deny 'Read(**/.config/gh/**)'", "--deny 'Read(**/.azure/**)'", "--deny 'Read(**/*.key)'",
		"--deny 'Grep(**/.grok/**)'", "--deny 'Grep(**/.config/gh/**)'", "--deny 'Grep(**/.azure/**)'", "--deny 'Grep(**/*.key)'",
	} {
		if !strings.Contains(command, required) {
			t.Errorf("default Grok command omits %q", required)
		}
	}
	if strings.Contains(command, "--always-approve") || strings.Contains(command, "--yolo") {
		t.Fatalf("default Grok command grants broad approval: %q", command)
	}
}

func TestDefaultGrokAutomationPolicyReturnsCopies(t *testing.T) {
	first := DefaultGrokAutomationPolicy()
	first.AllowRules[0] = "mutated"
	second := DefaultGrokAutomationPolicy()
	if second.AllowRules[0] != "Read" {
		t.Fatalf("policy metadata shared mutable backing storage: %+v", second.AllowRules)
	}
}

func TestDefaultGrokAutomationACPPolicyIsExplicitAndDigestStable(t *testing.T) {
	args := DefaultGrokAutomationACPPolicyArgs()
	if len(args) < 2 || args[0] != "--sandbox=read-only" || args[1] != "--permission-mode=dontAsk" {
		t.Fatalf("ACP policy args = %#v", args)
	}
	joined := strings.Join(args, "\n")
	for _, required := range []string{"--allow=Read", "--deny=Edit", "--deny=Bash(*)", "--deny=Read(**/.grok/**)", "--deny=Read(**/.config/gh/**)", "--deny=Read(**/.azure/**)", "--deny=Read(**/*.key)", "--deny=Grep(**/.grok/**)", "--deny=Grep(**/.config/gh/**)", "--deny=Grep(**/.azure/**)", "--deny=Grep(**/*.key)"} {
		if !strings.Contains(joined, required) {
			t.Errorf("ACP policy omits %q", required)
		}
	}
	if digest := DefaultGrokAutomationPolicySHA256(); len(digest) != 64 || digest != DefaultGrokAutomationPolicySHA256() {
		t.Fatalf("policy digest = %q", digest)
	}
}
