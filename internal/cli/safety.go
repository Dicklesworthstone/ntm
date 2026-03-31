package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/policy"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

func newSafetyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "safety",
		Short: "Manage destructive command protection",
		Long: `Manage NTM's destructive command protection system.

The safety system blocks or warns about dangerous commands like:
  - git reset --hard (loses uncommitted changes)
  - git push --force (overwrites remote history)
  - rm -rf / (catastrophic deletion)

Use 'ntm safety status' to see current protection status.
Use 'ntm safety blocked' to view blocked command history.
Use 'ntm safety check <command>' to test a command against the policy.`,
	}

	cmd.AddCommand(
		newSafetyStatusCmd(),
		newSafetyBlockedCmd(),
		newSafetyCheckCmd(),
		newSafetyInstallCmd(),
		newSafetyUninstallCmd(),
	)

	return cmd
}

func newSafetyStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show safety system status",
		RunE:  runSafetyStatus,
	}
}

// SafetyStatusResponse is the JSON output for safety status.
type SafetyStatusResponse struct {
	output.TimestampedResponse
	Installed     bool   `json:"installed"`
	PolicyPath    string `json:"policy_path,omitempty"`
	BlockedCount  int    `json:"blocked_rules"`
	ApprovalCount int    `json:"approval_rules"`
	AllowedCount  int    `json:"allowed_rules"`
	WrapperPath   string `json:"wrapper_path,omitempty"`
	HookInstalled bool   `json:"hook_installed"`
}

func runSafetyStatus(cmd *cobra.Command, args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting home directory: %w", err)
	}
	ntmDir := filepath.Join(home, ".ntm")
	wrapperDir := filepath.Join(ntmDir, "bin")

	// Check if wrappers are installed
	gitWrapper := filepath.Join(wrapperDir, "git")
	wrapperInstalled := fileExists(gitWrapper)

	// Check if Claude Code hook is installed
	hookPath := filepath.Join(home, ".claude", "hooks", "PreToolUse", "ntm-safety.sh")
	hookInstalled := fileExists(hookPath)

	// Load policy
	p, err := policy.LoadOrDefault()
	var blocked, approval, allowed int
	var policyPath string
	if err == nil {
		blocked, approval, allowed = p.Stats()
		// Check if custom policy exists
		customPath := filepath.Join(ntmDir, "policy.yaml")
		if fileExists(customPath) {
			policyPath = customPath
		}
	}

	if IsJSONOutput() {
		resp := SafetyStatusResponse{
			TimestampedResponse: output.NewTimestamped(),
			Installed:           wrapperInstalled || hookInstalled,
			PolicyPath:          policyPath,
			BlockedCount:        blocked,
			ApprovalCount:       approval,
			AllowedCount:        allowed,
			WrapperPath:         wrapperDir,
			HookInstalled:       hookInstalled,
		}
		return output.PrintJSON(resp)
	}

	// TUI output
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))

	fmt.Println()
	fmt.Printf("  %s\n", titleStyle.Render("NTM Safety System Status"))
	fmt.Println()

	statusStr := warnStyle.Render("Not Installed")
	if wrapperInstalled || hookInstalled {
		statusStr = okStyle.Render("Installed")
	}
	fmt.Printf("  %s %s\n", labelStyle.Render("Overall Status:"), statusStr)

	if wrapperInstalled {
		fmt.Printf("  %s %s (%s)\n", labelStyle.Render("Shell Wrappers:"), okStyle.Render("Active"), gitWrapper)
	} else {
		fmt.Printf("  %s %s\n", labelStyle.Render("Shell Wrappers:"), warnStyle.Render("Inactive"))
	}

	if hookInstalled {
		fmt.Printf("  %s %s (%s)\n", labelStyle.Render("Claude Hook:   "), okStyle.Render("Active"), hookPath)
	} else {
		fmt.Printf("  %s %s\n", labelStyle.Render("Claude Hook:   "), warnStyle.Render("Inactive"))
	}

	fmt.Println()
	fmt.Printf("  %s\n", titleStyle.Render("Active Policy"))
	if policyPath != "" {
		fmt.Printf("  %s %s\n", labelStyle.Render("Source: "), policyPath)
	} else {
		fmt.Printf("  %s %s\n", labelStyle.Render("Source: "), "Built-in Defaults")
	}
	fmt.Printf("  %s %d blocked, %d approval required, %d allowed rules\n",
		labelStyle.Render("Rules:  "), blocked, approval, allowed)
	fmt.Println()

	return nil
}

func newSafetyBlockedCmd() *cobra.Command {
	var hours int

	cmd := &cobra.Command{
		Use:   "blocked",
		Short: "Show history of blocked commands",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSafetyBlocked(hours)
		},
	}

	cmd.Flags().IntVar(&hours, "hours", 24, "Show events from the last N hours")

	return cmd
}

func runSafetyBlocked(hours int) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting home directory: %w", err)
	}
	logPath := filepath.Join(home, ".ntm", "logs", "blocked.jsonl")

	entries, err := policy.RecentBlocked(logPath, hours)
	if err != nil {
		return err
	}

	if IsJSONOutput() {
		return output.PrintJSON(entries)
	}

	// TUI output
	if len(entries) == 0 {
		fmt.Printf("\n  No blocked commands in the last %d hours.\n\n", hours)
		return nil
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))
	fmt.Println()
	fmt.Printf("  %s\n", titleStyle.Render(fmt.Sprintf("Blocked Commands (Last %d Hours)", hours)))
	fmt.Println()

	for _, e := range entries {
		ts := e.Timestamp.Local().Format("2006-01-02 15:04:05")
		fmt.Printf("  [%s] %s\n", ts, e.Command)
		fmt.Printf("    Reason: %s\n", e.Reason)
		fmt.Println()
	}

	return nil
}

func newSafetyCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check <command>",
		Short: "Check a command against safety policy",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSafetyCheck(strings.Join(args, " "))
		},
	}
}

// CheckResponse is the JSON output for safety check.
type CheckResponse struct {
	output.TimestampedResponse
	Command string `json:"command"`
	Action  string `json:"action"` // allow, block, approve
	Pattern string `json:"pattern,omitempty"`
	Reason  string `json:"reason,omitempty"`

	Policy *CheckPolicyVerdict `json:"policy,omitempty"`
	DCG    *CheckDCGVerdict    `json:"dcg,omitempty"`
}

type CheckPolicyVerdict struct {
	Action  string `json:"action"` // allow, block, approve
	Pattern string `json:"pattern,omitempty"`
	Reason  string `json:"reason,omitempty"`
	SLB     bool   `json:"slb,omitempty"` // Requires SLB two-person approval
}

type CheckDCGVerdict struct {
	Available bool   `json:"available"`
	Checked   bool   `json:"checked"`
	Blocked   bool   `json:"blocked"`
	Reason    string `json:"reason,omitempty"`
	Error     string `json:"error,omitempty"`
}

func runSafetyCheck(command string) error {
	resp, exitCode, err := evaluateSafetyCheck(command)
	if err != nil {
		return err
	}

	if IsJSONOutput() {
		if err := output.PrintJSON(resp); err != nil {
			return err
		}
	} else {
		// TUI output
		okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
		warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
		errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
		mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

		fmt.Println()
		fmt.Printf("  Command: %s\n", command)
		fmt.Println()

		if resp.Policy == nil {
			fmt.Printf("  %s Allowed (no policy match)\n", okStyle.Render("✓"))
		} else {
			switch resp.Action {
			case string(policy.ActionAllow):
				fmt.Printf("  %s Explicitly allowed\n", okStyle.Render("✓"))
			case string(policy.ActionBlock):
				fmt.Printf("  %s BLOCKED\n", errorStyle.Render("✗"))
			case string(policy.ActionApprove):
				fmt.Printf("  %s Requires approval\n", warnStyle.Render("⚠"))
			}
			if resp.Reason != "" {
				fmt.Printf("    %s\n", mutedStyle.Render(resp.Reason))
			}
			if resp.Pattern != "" {
				fmt.Printf("    %s\n", mutedStyle.Render("Pattern: "+resp.Pattern))
			}
			if resp.DCG != nil && resp.DCG.Checked {
				if resp.DCG.Blocked {
					fmt.Printf("    %s\n", mutedStyle.Render("DCG: BLOCKED"))
				} else {
					fmt.Printf("    %s\n", mutedStyle.Render("DCG: allowed"))
				}
				if resp.DCG.Reason != "" {
					fmt.Printf("    %s\n", mutedStyle.Render("DCG reason: "+resp.DCG.Reason))
				}
				if resp.DCG.Error != "" {
					fmt.Printf("    %s\n", mutedStyle.Render("DCG error: "+resp.DCG.Error))
				}
			}
			if resp.Policy != nil && resp.Pattern == "dcg" {
				fmt.Printf("    %s\n", mutedStyle.Render("Policy: "+resp.Policy.Action+" (pattern: "+resp.Policy.Pattern+")"))
			}
		}

		fmt.Println()
	}

	// Exit with code 1 for blocked or approval-required commands (both JSON and TUI modes).
	// This is critical for wrapper scripts that rely on exit code to intercept commands.
	if exitCode != 0 {
		os.Exit(exitCode)
	}

	return nil
}

func evaluateSafetyCheck(command string) (CheckResponse, int, error) {
	p, err := policy.LoadOrDefault()
	if err != nil {
		return CheckResponse{}, 0, fmt.Errorf("loading policy: %w", err)
	}

	match := p.Check(command)

	resp := CheckResponse{
		TimestampedResponse: output.NewTimestamped(),
		Command:             command,
		Action:              string(policy.ActionAllow),
	}
	if match != nil {
		resp.Action = string(match.Action)
		resp.Pattern = match.Pattern
		resp.Reason = match.Reason
		resp.Policy = &CheckPolicyVerdict{
			Action:  string(match.Action),
			Pattern: match.Pattern,
			Reason:  match.Reason,
			SLB:     match.SLB,
		}
	}

	// If policy marks the command as dangerous, consult DCG when available.
	if match != nil && match.Action != policy.ActionAllow {
		dcg := &CheckDCGVerdict{}
		adapter := tools.NewDCGAdapter()
		if _, installed := adapter.Detect(); installed {
			dcg.Available = true
			dcg.Checked = true

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			blocked, err := adapter.CheckCommand(ctx, command)
			if err != nil {
				dcg.Error = err.Error()
			} else if blocked != nil {
				dcg.Blocked = true
				dcg.Reason = blocked.Reason

				// If DCG blocks, enforce that decision even if policy only required approval.
				if match.Action != policy.ActionBlock {
					resp.Action = string(policy.ActionBlock)
					resp.Pattern = "dcg"
					if dcg.Reason != "" {
						resp.Reason = dcg.Reason
					} else {
						resp.Reason = "blocked by dcg"
					}
				}
			}
		}
		resp.DCG = dcg
	}

	// Exit code 1 for Block or Approve (to block execution in wrapper scripts)
	exitCode := 0
	if resp.Action == string(policy.ActionBlock) || resp.Action == string(policy.ActionApprove) {
		exitCode = 1
	}
	return resp, exitCode, nil
}

func newSafetyInstallCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install safety wrappers and hooks",
		Long: `Install the NTM safety system.

This installs:
  1. Shell wrappers in ~/.ntm/bin/ that intercept git and rm commands
  2. A Claude Code PreToolUse hook that validates Bash commands

After installation, add ~/.ntm/bin to your PATH (before /usr/bin) for wrapper protection.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSafetyInstall(force)
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Overwrite existing files")

	return cmd
}

func runSafetyInstall(force bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting home directory: %w", err)
	}

	ntmDir := filepath.Join(home, ".ntm")
	binDir := filepath.Join(ntmDir, "bin")
	logsDir := filepath.Join(ntmDir, "logs")

	// Create directories
	for _, dir := range []string{ntmDir, binDir, logsDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating directory %s: %w", dir, err)
		}
	}

	// Install git wrapper
	gitWrapper := filepath.Join(binDir, "git")
	if err := installWrapper(gitWrapper, gitWrapperScript, force); err != nil {
		return err
	}

	// Install rm wrapper
	rmWrapper := filepath.Join(binDir, "rm")
	if err := installWrapper(rmWrapper, rmWrapperScript, force); err != nil {
		return err
	}

	// Install Claude Code hook
	hookDir := filepath.Join(home, ".claude", "hooks", "PreToolUse")
	if err := os.MkdirAll(hookDir, 0755); err != nil {
		return fmt.Errorf("creating hook directory: %w", err)
	}

	hookPath := filepath.Join(hookDir, "ntm-safety.sh")
	if err := installWrapper(hookPath, claudeHookScript, force); err != nil {
		return err
	}

	// Create default policy file if it doesn't exist
	policyPath := filepath.Join(ntmDir, "policy.yaml")
	policyCreated := false
	if !fileExists(policyPath) || force {
		if err := writeDefaultPolicy(policyPath); err != nil {
			return err
		}
		policyCreated = true
	}

	if !IsJSONOutput() {
		okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
		mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

		fmt.Println()
		fmt.Printf("  %s Installed git wrapper: %s\n", okStyle.Render("✓"), gitWrapper)
		fmt.Printf("  %s Installed rm wrapper: %s\n", okStyle.Render("✓"), rmWrapper)
		fmt.Printf("  %s Installed Claude Code hook: %s\n", okStyle.Render("✓"), hookPath)
		if policyCreated {
			fmt.Printf("  %s Created policy file: %s\n", okStyle.Render("✓"), policyPath)
		} else {
			fmt.Printf("  %s Using existing policy: %s\n", okStyle.Render("✓"), policyPath)
		}
		fmt.Println()
		fmt.Printf("  %s\n", mutedStyle.Render("Add to your shell profile:"))
		fmt.Printf("    %s\n", "export PATH=\"$HOME/.ntm/bin:$PATH\"")
		fmt.Println()
	} else {
		return output.PrintJSON(map[string]interface{}{
			"success":     true,
			"timestamp":   time.Now(),
			"git_wrapper": gitWrapper,
			"rm_wrapper":  rmWrapper,
			"hook":        hookPath,
			"policy":      policyPath,
		})
	}

	return nil
}

func newSafetyUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove safety wrappers and hooks",
		RunE:  runSafetyUninstall,
	}
}

func runSafetyUninstall(cmd *cobra.Command, args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting home directory: %w", err)
	}

	var removed []string

	// Remove wrappers
	binDir := filepath.Join(home, ".ntm", "bin")
	for _, name := range []string{"git", "rm"} {
		path := filepath.Join(binDir, name)
		if fileExists(path) {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("removing %s: %w", path, err)
			}
			removed = append(removed, path)
		}
	}

	// Remove hook
	hookPath := filepath.Join(home, ".claude", "hooks", "PreToolUse", "ntm-safety.sh")
	if fileExists(hookPath) {
		if err := os.Remove(hookPath); err != nil {
			return fmt.Errorf("removing hook: %w", err)
		}
		removed = append(removed, hookPath)
	}

	if !IsJSONOutput() {
		if len(removed) == 0 {
			fmt.Println("Nothing to uninstall")
		} else {
			okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
			fmt.Println()
			for _, path := range removed {
				fmt.Printf("  %s Removed: %s\n", okStyle.Render("✓"), path)
			}
			fmt.Println()
		}
	} else {
		return output.PrintJSON(map[string]interface{}{
			"success":   true,
			"timestamp": time.Now(),
			"removed":   removed,
		})
	}

	return nil
}

func installWrapper(path, content string, force bool) error {
	if fileExists(path) && !force {
		return fmt.Errorf("%s already exists (use --force to overwrite)", path)
	}

	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}

	return nil
}

func writeDefaultPolicy(path string) error {
	// NOTE: Allowed patterns are checked FIRST (take precedence over blocked).
	// Precedence order is: allowed > blocked > approval_required
	content := `# NTM Safety Policy
# Patterns are regular expressions matched against commands
#
# IMPORTANT: Precedence order is: allowed > blocked > approval_required
# This means you can use 'allowed' to create exceptions to 'blocked' patterns.

# Explicitly allowed patterns (checked FIRST - use for exceptions)
allowed:
  - pattern: 'git\s+push\s+.*--force-with-lease'
    reason: "Safe force push (prevents overwriting others' work)"
  - pattern: 'git\s+reset\s+--soft'
    reason: "Soft reset preserves changes in staging"
  - pattern: 'git\s+reset\s+HEAD~?\d*$' 
    reason: "Mixed reset preserves working directory"

# Blocked patterns (dangerous commands)
blocked:
  - pattern: 'git\s+reset\s+--hard'
    reason: "Hard reset loses uncommitted changes"
  - pattern: 'git\s+clean(\s+.*?)?\s(-[\w]*f[\w]*|--force)(\s|$)'
    reason: "Removes untracked files permanently"
  - pattern: 'git\s+push(\s+.*?)?\s(-[\w]*f[\w]*|--force)(\s|$)'
    reason: "Force push can overwrite remote history"
  - pattern: 'git\s+push(\s+.*?)?\s(\+|:\+)'
    reason: "Force push via +refspec can overwrite remote history"
  - pattern: 'rm\s+-rf\s+(/|~|\*|\.|\.\.)(/|\s|$)'
    reason: "Critical recursive deletion"
  - pattern: 'git\s+branch\s+-D'
    reason: "Force delete branch loses unmerged work"
  - pattern: 'git\s+stash\s+(drop|clear)'
    reason: "Losing stashed work"

# Approval required (potentially dangerous, need confirmation)
approval_required:
  - pattern: 'git\s+rebase(\s+.*?)?\s(-i|--interactive)'
    reason: "Interactive rebase rewrites history"
  - pattern: 'git\s+commit\s+--amend'
    reason: "Amending rewrites history"
  - pattern: 'rm\s+-rf\s+\S'
    reason: "Recursive force delete"
`
	return os.WriteFile(path, []byte(content), 0644)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Wrapper scripts

const gitWrapperScript = `#!/bin/bash
# NTM Safety Wrapper for git
# Intercepts destructive git commands

REAL_GIT=$(which -a git | grep -v "$HOME/.ntm/bin" | head -1)
if [ -z "$REAL_GIT" ]; then
    REAL_GIT="/usr/bin/git"
fi

# Check command against policy (include "git" in the command string)
check_result=$(ntm safety check "git $*" --json 2>&1)
exit_code=$?

# ntm safety check exits 0 for allow, 1 for block/approve
if [ $exit_code -ne 0 ]; then
    action=$(echo "$check_result" | jq -r '.action // "block"' 2>/dev/null)
    reason=$(echo "$check_result" | jq -r '.reason // "Policy violation"' 2>/dev/null)
    
    if [ "$action" = "approve" ]; then
        echo "NTM Safety: Command requires approval" >&2
        echo "  Reason: $reason" >&2
        echo "  Command: git $*" >&2
        echo "  Run 'ntm approve list' to see pending requests." >&2
    else
        echo "NTM Safety: Command blocked" >&2
        echo "  Reason: $reason" >&2
        echo "  Command: git $*" >&2
    fi

    # Log the blocked command (use jq for proper JSON escaping)
    mkdir -p "$HOME/.ntm/logs"
    if command -v jq >/dev/null 2>&1; then
        jq -n --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
              --arg cmd "git $*" \
              --arg reason "${reason:-Policy violation}" \
              --arg action "$action" \
              '{timestamp: $ts, command: $cmd, reason: $reason, action: $action}' >> "$HOME/.ntm/logs/blocked.jsonl"
    else
        # Fallback without proper escaping (best effort)
        echo "{\"timestamp\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"action\":\"$action\"}" >> "$HOME/.ntm/logs/blocked.jsonl"
    fi

    exit 1
fi

# Pass through to real git
exec "$REAL_GIT" "$@"
`

const rmWrapperScript = `#!/bin/bash
# NTM Safety Wrapper for rm
# Intercepts destructive rm commands

REAL_RM=$(which -a rm | grep -v "$HOME/.ntm/bin" | head -1)
if [ -z "$REAL_RM" ]; then
    REAL_RM="/bin/rm"
fi

# Check command against policy
check_result=$(ntm safety check "rm $*" --json 2>&1)
exit_code=$?

# ntm safety check exits 0 for allow, 1 for block/approve
if [ $exit_code -ne 0 ]; then
    action=$(echo "$check_result" | jq -r '.action // "block"' 2>/dev/null)
    reason=$(echo "$check_result" | jq -r '.reason // "Policy violation"' 2>/dev/null)
    
    if [ "$action" = "approve" ]; then
        echo "NTM Safety: Command requires approval" >&2
        echo "  Reason: $reason" >&2
        echo "  Command: rm $*" >&2
        echo "  Run 'ntm approve list' to see pending requests." >&2
    else
        echo "NTM Safety: Command blocked" >&2
        echo "  Reason: $reason" >&2
        echo "  Command: rm $*" >&2
    fi

    # Log the blocked command (use jq for proper JSON escaping)
    mkdir -p "$HOME/.ntm/logs"
    if command -v jq >/dev/null 2>&1; then
        jq -n --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
              --arg cmd "rm $*" \
              --arg reason "${reason:-Policy violation}" \
              --arg action "$action" \
              '{timestamp: $ts, command: $cmd, reason: $reason, action: $action}' >> "$HOME/.ntm/logs/blocked.jsonl"
    else
        # Fallback without proper escaping (best effort)
        echo "{\"timestamp\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"action\":\"$action\"}" >> "$HOME/.ntm/logs/blocked.jsonl"
    fi

    exit 1
fi

# Pass through to real rm
exec "$REAL_RM" "$@"
`

const claudeHookScript = `#!/bin/bash
# NTM Safety Hook for Claude Code
# PreToolUse hook that validates Bash commands

# Only process Bash tool calls
TOOL_NAME="${CLAUDE_TOOL_NAME:-}"
if [ "$TOOL_NAME" != "Bash" ]; then
    exit 0
fi

# Get the command from the tool input
COMMAND="${CLAUDE_TOOL_INPUT_command:-}"
if [ -z "$COMMAND" ]; then
    exit 0
fi

# Check against policy
check_result=$(ntm safety check "$COMMAND" --json 2>&1)
exit_code=$?

# ntm safety check exits 0 for allow, 1 for block/approve
if [ $exit_code -ne 0 ]; then
    action=$(echo "$check_result" | jq -r '.action // "block"' 2>/dev/null)
    reason=$(echo "$check_result" | jq -r '.reason // "Policy violation"' 2>/dev/null)

    # Log the blocked command (use jq for proper JSON escaping)
    mkdir -p "$HOME/.ntm/logs"
    session="${NTM_SESSION:-unknown}"
    agent="${CLAUDE_AGENT_TYPE:-claude}"
    if command -v jq >/dev/null 2>&1; then
        jq -n --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
              --arg session "$session" \
              --arg agent "$agent" \
              --arg cmd "$COMMAND" \
              --arg reason "${reason:-Policy violation}" \
              --arg action "$action" \
              '{timestamp: $ts, session: $session, agent: $agent, command: $cmd, reason: $reason, action: $action}' >> "$HOME/.ntm/logs/blocked.jsonl"
    else
        # Fallback without proper escaping (best effort)
        echo "{\"timestamp\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"action\":\"$action\"}" >> "$HOME/.ntm/logs/blocked.jsonl"
    fi

    # Return error to Claude Code
    if [ "$action" = "approve" ]; then
        echo "APPROVAL REQUIRED: $reason"
        echo "Run 'ntm approve list' to see pending requests."
    else
        echo "BLOCKED: $reason"
    fi
    exit 1
fi

exit 0
`
