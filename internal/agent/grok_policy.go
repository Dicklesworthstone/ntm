package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// DefaultGrokAutomationPolicyName is the named least-privilege policy used by
// unattended Grok Build panes. Keeping the name stable lets robot capability
// output and execution receipts identify the policy without copying rules.
const DefaultGrokAutomationPolicyName = "grok-readonly-ci"

// DefaultGrokAutomationCommand is the fully rendered compatibility launch
// when no model or reasoning override is requested. Restart/restore paths use
// this value directly and must never receive Go template syntax.
//
// Rules intentionally allow only provider-native read/search operations.
// Bash is denied entirely: an allowlist of test commands cannot honestly
// guarantee a credential-isolated process. NTM itself performs test/verification
// outside the provider process until a non-exportable credential broker and
// kernel-enforced sandbox are available. Writes, pushes, destructive commands,
// package installation, privilege escalation, and common credential paths
// remain denied. --no-auto-update is mandatory for every NTM-managed automated
// launch.
const DefaultGrokAutomationCommand = `grok --no-auto-update --sandbox read-only --permission-mode dontAsk` +
	` --allow 'Read' --allow 'Grep' --allow 'WebFetch' --allow 'WebSearch'` +
	` --deny 'Edit' --deny 'Bash(*)'` +
	` --deny 'Read(**/.env*)' --deny 'Read(**/.ssh/**)' --deny 'Read(**/.aws/**)'` +
	` --deny 'Read(**/.config/gcloud/**)' --deny 'Read(**/.config/gh/**)' --deny 'Read(**/.grok/**)'` +
	` --deny 'Read(**/.azure/**)' --deny 'Read(**/.kube/**)' --deny 'Read(**/.docker/**)'` +
	` --deny 'Read(**/.netrc)' --deny 'Read(**/.npmrc)' --deny 'Read(**/.pypirc)' --deny 'Read(**/*.pem)' --deny 'Read(**/*.key)'` +
	` --deny 'Read(**/*credential*)' --deny 'Read(**/*secret*)'` +
	` --deny 'Grep(**/.env*)' --deny 'Grep(**/.ssh/**)' --deny 'Grep(**/.aws/**)'` +
	` --deny 'Grep(**/.config/gcloud/**)' --deny 'Grep(**/.config/gh/**)' --deny 'Grep(**/.grok/**)'` +
	` --deny 'Grep(**/.azure/**)' --deny 'Grep(**/.kube/**)' --deny 'Grep(**/.docker/**)'` +
	` --deny 'Grep(**/.netrc)' --deny 'Grep(**/.npmrc)' --deny 'Grep(**/.pypirc)' --deny 'Grep(**/*.pem)' --deny 'Grep(**/*.key)'` +
	` --deny 'Grep(**/*credential*)' --deny 'Grep(**/*secret*)'`

// DefaultGrokAutomationCommandTemplate is the model-aware interactive
// compatibility launch. Provider-native automation should use ACP; this TUI
// command remains available for humans and older workflows. dontAsk silently
// denies anything not explicitly allowed, while explicit deny rules take
// precedence over allows.
const DefaultGrokAutomationCommandTemplate = DefaultGrokAutomationCommand +
	`{{if .Model}} --model {{shellQuote .Model}}{{end}}` +
	`{{if .ReasoningEffort}} --effort {{shellQuote .ReasoningEffort}}{{end}}`

// GrokAutomationPolicyDescriptor is safe capability metadata. It deliberately
// contains rule names rather than credentials, paths, or provider output.
type GrokAutomationPolicyDescriptor struct {
	Name           string
	Sandbox        string
	PermissionMode string
	AllowRules     []string
	DenyRules      []string
}

// DefaultGrokAutomationPolicy returns a copy of the built-in policy metadata.
func DefaultGrokAutomationPolicy() GrokAutomationPolicyDescriptor {
	return GrokAutomationPolicyDescriptor{
		Name:           DefaultGrokAutomationPolicyName,
		Sandbox:        "read-only",
		PermissionMode: "dontAsk",
		AllowRules: []string{
			"Read", "Grep", "WebFetch", "WebSearch",
		},
		DenyRules: []string{
			"Edit", "Bash(*)",
			"Read(**/.env*)", "Read(**/.ssh/**)", "Read(**/.aws/**)",
			"Read(**/.config/gcloud/**)", "Read(**/.config/gh/**)", "Read(**/.grok/**)",
			"Read(**/.azure/**)", "Read(**/.kube/**)", "Read(**/.docker/**)",
			"Read(**/.netrc)", "Read(**/.npmrc)", "Read(**/.pypirc)", "Read(**/*.pem)", "Read(**/*.key)",
			"Read(**/*credential*)", "Read(**/*secret*)",
			"Grep(**/.env*)", "Grep(**/.ssh/**)", "Grep(**/.aws/**)",
			"Grep(**/.config/gcloud/**)", "Grep(**/.config/gh/**)", "Grep(**/.grok/**)",
			"Grep(**/.azure/**)", "Grep(**/.kube/**)", "Grep(**/.docker/**)",
			"Grep(**/.netrc)", "Grep(**/.npmrc)", "Grep(**/.pypirc)", "Grep(**/*.pem)", "Grep(**/*.key)",
			"Grep(**/*credential*)", "Grep(**/*secret*)",
		},
	}
}

// DefaultGrokAutomationPermissionArgs returns exec-ready permission arguments.
// Values are not shell-quoted because native adapters pass this vector directly
// to execve rather than joining it into a shell command.
func DefaultGrokAutomationPermissionArgs() []string {
	policy := DefaultGrokAutomationPolicy()
	args := []string{"--permission-mode", policy.PermissionMode}
	for _, rule := range policy.AllowRules {
		args = append(args, "--allow", rule)
	}
	for _, rule := range policy.DenyRules {
		args = append(args, "--deny", rule)
	}
	return args
}

// DefaultGrokAutomationACPPolicyArgs returns the same named policy in the
// single-argument form accepted by the ACP adapter's narrow launch validator.
// Keeping the permission mode explicit prevents CLI-default drift.
func DefaultGrokAutomationACPPolicyArgs() []string {
	policy := DefaultGrokAutomationPolicy()
	args := []string{"--sandbox=" + policy.Sandbox, "--permission-mode=" + policy.PermissionMode}
	for _, rule := range policy.AllowRules {
		args = append(args, "--allow="+rule)
	}
	for _, rule := range policy.DenyRules {
		args = append(args, "--deny="+rule)
	}
	return args
}

// DefaultGrokAutomationPolicySHA256 is a stable, non-secret authorization
// digest for receipts. Length-prefix-free ambiguity is avoided with NUL
// separators because policy names and rules cannot contain control bytes.
func DefaultGrokAutomationPolicySHA256() string {
	policy := DefaultGrokAutomationPolicy()
	fields := []string{policy.Name, policy.Sandbox, policy.PermissionMode}
	fields = append(fields, policy.AllowRules...)
	fields = append(fields, policy.DenyRules...)
	sum := sha256.Sum256([]byte(strings.Join(fields, "\x00")))
	return hex.EncodeToString(sum[:])
}

// DefaultGrokAutomationShellArgs returns shell-ready static arguments for
// launchers that keep the binary and argument vector separately. Rule values
// containing spaces are single-quoted because those launchers join the vector
// into a shell command rather than invoking execve directly.
func DefaultGrokAutomationShellArgs() []string {
	policy := DefaultGrokAutomationPolicy()
	args := []string{"--no-auto-update", "--sandbox", policy.Sandbox, "--permission-mode", policy.PermissionMode}
	for _, rule := range policy.AllowRules {
		args = append(args, "--allow", "'"+rule+"'")
	}
	for _, rule := range policy.DenyRules {
		args = append(args, "--deny", "'"+rule+"'")
	}
	return args
}
