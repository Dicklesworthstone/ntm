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
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	fmt.Println()
	fmt.Println(titleStyle.Render("NTM Safety Status"))
	fmt.Println()

	// Wrapper status
	if wrapperInstalled {
		fmt.Printf("  %s PATH wrappers installed (%s)\n", okStyle.Render("✓"), wrapperDir)
	} else {
		fmt.Printf("  %s PATH wrappers not installed\n", warnStyle.Render("○"))
	}

	// Hook status
	if hookInstalled {
		fmt.Printf("  %s Claude Code hook installed\n", okStyle.Render("✓"))
	} else {
		fmt.Printf("  %s Claude Code hook not installed\n", warnStyle.Render("○"))
	}

	// Policy status
	fmt.Println()
	fmt.Println("  Policy rules:")
	fmt.Printf("    %s blocked patterns\n", mutedStyle.Render(fmt.Sprintf("%d", blocked)))
	fmt.Printf("    %s approval-required patterns\n", mutedStyle.Render(fmt.Sprintf("%d", approval)))
	fmt.Printf("    %s explicitly allowed patterns\n", mutedStyle.Render(fmt.Sprintf("%d", allowed)))

	if policyPath != "" {
		fmt.Printf("    %s\n", mutedStyle.Render("Custom: "+policyPath))
	} else {
		fmt.Printf("    %s\n", mutedStyle.Render("Using default policy"))
	}

	// Installation hint
	if !wrapperInstalled && !hookInstalled {
		fmt.Println()
		fmt.Printf("  %s\n", mutedStyle.Render("Run 'ntm safety install' to enable protection"))
	}

	fmt.Println()
	return nil
}

func newSafetyBlockedCmd() *cobra.Command {
	var hours int
	var limit int

	cmd := &cobra.Command{
		Use:   "blocked",
		Short: "Show blocked command history",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSafetyBlocked(hours, limit)
		},
	}

	cmd.Flags().IntVar(&hours, "hours", 24, "Show blocked commands from last N hours")
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "Maximum entries to show")

	return cmd
}

// BlockedResponse is the JSON output for blocked commands.
type BlockedResponse struct {
	output.TimestampedResponse
	Entries []policy.BlockedEntry `json:"entries"`
	Count   int                   `json:"count"`
}

func runSafetyBlocked(hours, limit int) error {
	entries, err := policy.RecentBlocked("", hours)
	if err != nil {
		return fmt.Errorf("reading blocked log: %w", err)
	}

	// Limit entries
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}

	if IsJSONOutput() {
		return output.PrintJSON(BlockedResponse{
			TimestampedResponse: output.NewTimestamped(),
			Entries:             entries,
			Count:               len(entries),
		})
	}

	// TUI output
	if len(entries) == 0 {
		fmt.Println("No blocked commands in the last", hours, "hours")
		return nil
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	fmt.Println()
	fmt.Println(titleStyle.Render(fmt.Sprintf("Blocked Commands (last %d hours)", hours)))
	fmt.Println()

	for _, e := range entries {
		ts := e.Timestamp.Format("01/02 15:04")
		fmt.Printf("  %s %s\n", mutedStyle.Render(ts), errorStyle.Render(e.Command))
		if e.Reason != "" {
			fmt.Printf("           %s\n", mutedStyle.Render(e.Reason))
		}
		if e.Session != "" {
			fmt.Printf("           %s\n", mutedStyle.Render("Session: "+e.Session))
		}
	}

	fmt.Println()
	return nil
}

func newSafetyCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check <command>",
		Short: "Check if a command would be blocked",
		Long: `Check if a shell command would be blocked by the current safety policy.

If the command you are checking contains flags like --hard, use "--" to stop flag parsing:
  ntm safety check -- git reset --hard
  ntm --json safety check -- git reset --hard`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			command := strings.Join(args, " ")
			return runSafetyCheck(command)
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

	// Exit with code 1 for blocked commands (both JSON and TUI modes).
	// This is critical for wrapper scripts that rely on exit code.
	if exitCode == 1 {
		os.Exit(1)
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

	exitCode := 0
	if resp.Action == string(policy.ActionBlock) {
		exitCode = 1
	}
	return resp, exitCode, nil
}

func newSafetyInstallCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install safety wrappers and hooks",
		Long: `Install PATH wrappers and Claude Code hooks for destructive command protection.

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
	// This is how we handle --force-with-lease: allow it explicitly, then block --force.
	// Go's regexp doesn't support lookaheads (?!...), so we use precedence instead.
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
  - pattern: 'git\s+clean\s+.*(-fd|-df|-f\s+-d|-d\s+-f)'
    reason: "Removes untracked files permanently"
  - pattern: 'git\s+push\s+.*--force'
    reason: "Force push can overwrite remote history"
  - pattern: 'git\s+push\s+.*\s-f(\s|$)'
    reason: "Force push can overwrite remote history"
  - pattern: 'git\s+push\s+-f(\s|$)'
    reason: "Force push can overwrite remote history"
  - pattern: 'git\s+push\s+.*(\s\+|:\+)'
    reason: "Force push via +refspec can overwrite remote history"
  - pattern: 'rm\s+-rf\s+/+$'
    reason: "Recursive delete of root is catastrophic"
  - pattern: 'rm\s+-rf\s+~'
    reason: "Recursive delete of home directory"
  - pattern: 'rm\s+-rf\s+\*'
    reason: "Recursive delete of everything in current directory"
  - pattern: 'git\s+branch\s+-D'
    reason: "Force delete branch loses unmerged work"
  - pattern: 'git\s+stash\s+drop'
    reason: "Dropping stash loses saved work"
  - pattern: 'git\s+stash\s+clear'
    reason: "Clearing all stashes loses saved work"

# Approval required (potentially dangerous, need confirmation)
approval_required:
  - pattern: 'git\s+rebase\s+-i'
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

# ntm safety check exits 0 for allow/approve, 1 for block
if [ $exit_code -eq 1 ]; then
    # Command was blocked
    reason=$(echo "$check_result" | jq -r '.reason // "Policy violation"' 2>/dev/null)
    echo "NTM Safety: Command blocked" >&2
    echo "  Reason: $reason" >&2
    echo "  Command: git $*" >&2

    # Log the blocked command (use jq for proper JSON escaping)
    mkdir -p "$HOME/.ntm/logs"
    if command -v jq >/dev/null 2>&1; then
        jq -n --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
              --arg cmd "git $*" \
              --arg reason "${reason:-Policy violation}" \
              '{timestamp: $ts, command: $cmd, reason: $reason, action: "block"}' >> "$HOME/.ntm/logs/blocked.jsonl"
    else
        # Fallback without proper escaping (best effort)
        echo "{\"timestamp\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"action\":\"block\"}" >> "$HOME/.ntm/logs/blocked.jsonl"
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

# ntm safety check exits 0 for allow/approve, 1 for block
if [ $exit_code -eq 1 ]; then
    # Command was blocked
    reason=$(echo "$check_result" | jq -r '.reason // "Policy violation"' 2>/dev/null)
    echo "NTM Safety: Command blocked" >&2
    echo "  Reason: $reason" >&2
    echo "  Command: rm $*" >&2

    # Log the blocked command (use jq for proper JSON escaping)
    mkdir -p "$HOME/.ntm/logs"
    if command -v jq >/dev/null 2>&1; then
        jq -n --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
              --arg cmd "rm $*" \
              --arg reason "${reason:-Policy violation}" \
              '{timestamp: $ts, command: $cmd, reason: $reason, action: "block"}' >> "$HOME/.ntm/logs/blocked.jsonl"
    else
        # Fallback without proper escaping (best effort)
        echo "{\"timestamp\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"action\":\"block\"}" >> "$HOME/.ntm/logs/blocked.jsonl"
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

# ntm safety check exits 0 for allow/approve, 1 for block
if [ $exit_code -eq 1 ]; then
    # Command was blocked
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
              '{timestamp: $ts, session: $session, agent: $agent, command: $cmd, reason: $reason, action: "block"}' >> "$HOME/.ntm/logs/blocked.jsonl"
    else
        # Fallback without proper escaping (best effort)
        echo "{\"timestamp\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"action\":\"block\"}" >> "$HOME/.ntm/logs/blocked.jsonl"
    fi

    # Return error to Claude Code
    echo "BLOCKED: $reason"
    exit 1
fi

exit 0
`
