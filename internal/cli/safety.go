package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/policy"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
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
Use 'ntm safety check <command>' to test a command against the policy.
Use 'ntm safety simulate <command>' to dry-run a multi-step plan.`,
	}

	cmd.AddCommand(
		newSafetyStatusCmd(),
		newSafetyBlockedCmd(),
		newSafetyCheckCmd(),
		newSafetySimulateCmd(),
		newSafetyInstallCmd(),
		newSafetyUninstallCmd(),
		newSafetyClaudeHookCmd(),
	)

	return cmd
}

// newSafetyClaudeHookCmd is the command Claude Code's PreToolUse hook runs —
// both the script `ntm safety install` registers and the hook spawned Claude
// agents get through --settings. It is not for interactive use.
func newSafetyClaudeHookCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "claude-hook",
		Short:  "Check a Claude Code PreToolUse event from stdin against the safety policy",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if code := runSafetyClaudeHook(cmd.InOrStdin(), cmd.ErrOrStderr()); code != 0 {
				return newSilentExitError(code)
			}
			return nil
		},
	}
}

// claudeHookRefuse is the exit status that makes Claude Code refuse the tool
// call and show stderr to the model; any other non-zero status lets the call
// proceed.
const claudeHookRefuse = 2

// runSafetyClaudeHook evaluates one PreToolUse payload and returns the hook's
// exit status. Non-Bash tools and empty commands pass. A payload that cannot
// be read is refused: the hook is registered for Bash, so an unreadable event
// is a Bash command nobody checked.
func runSafetyClaudeHook(stdin io.Reader, stderr io.Writer) int {
	in, err := policy.ParseClaudeHookInput(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "BLOCKED: ntm safety hook could not read the tool call (%v), so the command was not checked.\n", err)
		return claudeHookRefuse
	}
	if in.ToolName != "Bash" || strings.TrimSpace(in.ToolInput.Command) == "" {
		return 0
	}
	resp, exitCode, err := evaluateSafetyCheck(in.ToolInput.Command)
	if err != nil {
		fmt.Fprintf(stderr, "BLOCKED: ntm safety hook could not evaluate the policy: %v\n", err)
		return claudeHookRefuse
	}
	if exitCode == 0 {
		return 0
	}
	recordHookRefusal(resp)
	reason := strings.TrimSpace(resp.Reason)
	if reason == "" {
		reason = "Policy violation"
	}
	if resp.Action == string(policy.ActionApprove) {
		fmt.Fprintf(stderr, "APPROVAL REQUIRED: %s\n", reason)
		fmt.Fprintln(stderr, "This check is advisory: the command was refused but no approval request was queued.")
		fmt.Fprintln(stderr, "Approval-gated ntm commands (e.g. 'ntm locks force-release') request approval when run; decide with 'ntm approve'.")
	} else {
		fmt.Fprintf(stderr, "BLOCKED: %s\n", reason)
	}
	return claudeHookRefuse
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
	Installed         bool   `json:"installed"`
	InstallationState string `json:"installation_state"`
	PolicyPath        string `json:"policy_path,omitempty"`
	BlockedCount      int    `json:"blocked_rules"`
	ApprovalCount     int    `json:"approval_rules"`
	AllowedCount      int    `json:"allowed_rules"`
	WrapperPath       string `json:"wrapper_path,omitempty"`
	WrappersEffective bool   `json:"wrappers_effective"`
	// HookInstalled is true only when the hook script exists and the Claude
	// settings file registers it (HookScriptPresent && HookRegistered).
	HookInstalled     bool   `json:"hook_installed"`
	HookScriptPresent bool   `json:"hook_script_present"`
	HookRegistered    bool   `json:"hook_registered"`
	HookSettingsPath  string `json:"hook_settings_path,omitempty"`
	HookSettingsError string `json:"hook_settings_error,omitempty"`
}

const (
	safetyNotInstalled          = "not_installed"
	safetyInstalledNotEffective = "installed_not_effective"
	safetyEffective             = "effective"
)

// safetyInstallationState reports whether the installed shell wrappers are
// the commands the current process will execute. Installing files alone is
// insufficient: ~/.ntm/bin must appear before the system command directory in
// PATH for git and rm to be protected.
func safetyInstallationState(wrapperDir string, hookInstalled bool) (installed bool, wrappersEffective bool, state string) {
	gitWrapper := filepath.Join(wrapperDir, "git")
	rmWrapper := filepath.Join(wrapperDir, "rm")
	wrapperInstalled := fileExists(gitWrapper) && fileExists(rmWrapper)
	installed = wrapperInstalled || hookInstalled
	if !installed {
		return false, false, safetyNotInstalled
	}

	wrappersEffective = wrapperInstalled && safetyWrapperIsFirst(gitWrapper) && safetyWrapperIsFirst(rmWrapper)
	if wrappersEffective {
		return true, true, safetyEffective
	}
	return true, false, safetyInstalledNotEffective
}

func safetyWrapperIsFirst(wrapperPath string) bool {
	resolved, err := exec.LookPath(filepath.Base(wrapperPath))
	if err != nil {
		return false
	}
	resolvedInfo, err := os.Stat(resolved)
	if err != nil {
		return false
	}
	wrapperInfo, err := os.Stat(wrapperPath)
	return err == nil && os.SameFile(resolvedInfo, wrapperInfo)
}

func runSafetyStatus(cmd *cobra.Command, args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting home directory: %w", err)
	}
	ntmDir := filepath.Join(home, ".ntm")
	wrapperDir := filepath.Join(ntmDir, "bin")

	// Check whether installed wrappers actually shadow the system commands.
	gitWrapper := filepath.Join(wrapperDir, "git")

	// The Claude Code hook protects only when its script exists AND a settings
	// file registers it: Claude Code never runs unregistered hook scripts.
	hookPath := policy.ClaudeHookScriptPath(home)
	settingsPath := policy.ClaudeUserSettingsPath(home)
	hookScriptPresent := fileExists(hookPath)
	hookRegistered, registeredErr := policy.ClaudeHookRegistered(settingsPath, hookPath)
	hookInstalled := hookScriptPresent && hookRegistered
	installed, wrappersEffective, installationState := safetyInstallationState(wrapperDir, hookInstalled)
	if !installed && hookScriptPresent {
		installed, installationState = true, safetyInstalledNotEffective
	}

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
			Installed:           installed,
			InstallationState:   installationState,
			PolicyPath:          policyPath,
			BlockedCount:        blocked,
			ApprovalCount:       approval,
			AllowedCount:        allowed,
			WrapperPath:         wrapperDir,
			WrappersEffective:   wrappersEffective,
			HookInstalled:       hookInstalled,
			HookScriptPresent:   hookScriptPresent,
			HookRegistered:      hookRegistered,
			HookSettingsPath:    settingsPath,
		}
		if registeredErr != nil {
			resp.HookSettingsError = registeredErr.Error()
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
	switch installationState {
	case safetyInstalledNotEffective:
		statusStr = warnStyle.Render("Installed, Not Effective")
	case safetyEffective:
		statusStr = okStyle.Render("Effective")
	}
	fmt.Printf("  %s %s\n", labelStyle.Render("Overall Status:"), statusStr)

	if wrappersEffective {
		fmt.Printf("  %s %s (%s)\n", labelStyle.Render("Shell Wrappers:"), okStyle.Render("Active"), gitWrapper)
	} else if installed {
		fmt.Printf("  %s %s\n", labelStyle.Render("Shell Wrappers:"), warnStyle.Render("Installed but not on PATH"))
		fmt.Printf("  %s export PATH=\"$HOME/.ntm/bin:$PATH\"\n", labelStyle.Render("Fix:           "))
	} else {
		fmt.Printf("  %s %s\n", labelStyle.Render("Shell Wrappers:"), warnStyle.Render("Inactive"))
	}

	switch {
	case hookInstalled:
		fmt.Printf("  %s %s (%s)\n", labelStyle.Render("Claude Hook:   "), okStyle.Render("Active"), hookPath)
	case hookScriptPresent:
		fmt.Printf("  %s %s\n", labelStyle.Render("Claude Hook:   "), warnStyle.Render("Script present but not registered in "+settingsPath))
		if registeredErr != nil {
			fmt.Printf("  %s %v\n", labelStyle.Render("Error:         "), registeredErr)
		}
		fmt.Printf("  %s ntm safety install --force\n", labelStyle.Render("Fix:           "))
	default:
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
	var hook bool
	cmd := &cobra.Command{
		Use:   "check <command>",
		Short: "Check a command against safety policy",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSafetyCheck(strings.Join(args, " "), hook)
		},
	}
	// The installed wrappers and hook pass --hook: a refusal there is an
	// agent's command being stopped, so it is recorded. A check run by hand
	// only reports the verdict.
	cmd.Flags().BoolVar(&hook, "hook", false, "record a refusal as a blocked command (used by the installed safety hooks)")
	_ = cmd.Flags().MarkHidden("hook")
	return cmd
}

func newSafetySimulateCmd() *cobra.Command {
	var steps []string

	cmd := &cobra.Command{
		Use:   "simulate [command]",
		Short: "Simulate a command plan against safety policy",
		Long: `Simulate one or more proposed shell commands against the safety policy.

The simulator is a dry-run surface: it reports allow/block/approval decisions,
policy provenance, and safer alternatives without executing any command.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && len(steps) == 0 {
				return fmt.Errorf("provide at least one command or --step")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSafetySimulate(safetySimulationCommands(strings.Join(args, " "), steps))
		},
	}

	cmd.Flags().StringArrayVar(&steps, "step", nil, "Command step to simulate; repeat for multi-step plans")

	return cmd
}

func safetySimulationCommands(command string, steps []string) []string {
	commands := make([]string, 0, len(steps)+1)
	if strings.TrimSpace(command) != "" {
		commands = append(commands, command)
	}
	commands = append(commands, steps...)
	return commands
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

// hookSessionLookupTimeout bounds the tmux query recordHookRefusal makes.
const hookSessionLookupTimeout = 2 * time.Second

// recordHookRefusal files a command an installed safety hook refused: an entry
// in the blocked-command log (`ntm safety blocked`) naming the tmux session and
// pane it ran in, and a row in that session's blocked_commands, which `ntm
// metrics` reports as destructive_cmd_incidents. The hook scripts used to log
// in shell with no usable session ("unknown"), and the metric never counted
// these refusals (bd-cl6me).
func recordHookRefusal(resp CheckResponse) {
	// This runs inside an agent's tool-call hook: a stuck tmux server must not
	// hang the agent, so the session lookup is bounded.
	lookupCtx, cancel := context.WithTimeout(context.Background(), hookSessionLookupTimeout)
	session, _ := tmux.GetCurrentSessionContext(lookupCtx)
	cancel()
	pane := strings.TrimSpace(os.Getenv("TMUX_PANE"))
	entry := policy.BlockedEntry{
		Timestamp: time.Now().UTC(),
		Session:   session,
		Agent:     pane,
		Command:   resp.Command,
		Pattern:   resp.Pattern,
		Reason:    resp.Reason,
		Action:    policy.Action(resp.Action),
	}
	if err := policy.AppendBlocked("", entry); err != nil {
		slog.Default().Debug("blocked command not logged", "error", err)
	}
	if session == "" {
		return // outside tmux there is no session to attribute it to
	}
	store, collector, err := getMetricsCollector(session)
	if err != nil {
		slog.Default().Debug("blocked-command metric unavailable", "session", session, "error", err)
		return
	}
	if store != nil {
		defer store.Close()
	}
	collector.RecordBlockedCommand(pane, resp.Command, "policy: "+resp.Reason)
}

func runSafetyCheck(command string, hook bool) error {
	resp, exitCode, err := evaluateSafetyCheck(command)
	if err != nil {
		return err
	}
	if hook && exitCode != 0 {
		recordHookRefusal(resp)
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
		return newSilentExitError(exitCode)
	}

	return nil
}

func runSafetySimulate(commands []string) error {
	report, err := evaluateSafetySimulation(commands)
	if err != nil {
		return err
	}

	if IsJSONOutput() {
		return output.PrintJSON(report)
	}

	renderSafetySimulation(report)
	return nil
}

func evaluateSafetySimulation(commands []string) (policy.SimulationReport, error) {
	p, err := policy.LoadOrDefault()
	if err != nil {
		return policy.SimulationReport{}, fmt.Errorf("loading policy: %w", err)
	}
	return policy.SimulatePlan(p, commands), nil
}

func renderSafetySimulation(report policy.SimulationReport) {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	fmt.Println()
	fmt.Printf("  %s\n", titleStyle.Render("Safety Policy Simulation"))
	fmt.Printf("  %s %d steps, %d allowed, %d blocked, %d approval, %d invalid\n",
		labelStyle.Render("Summary:"),
		report.Summary.TotalSteps,
		report.Summary.AllowedSteps,
		report.Summary.BlockedSteps,
		report.Summary.ApprovalSteps,
		report.Summary.InvalidSteps,
	)
	if report.SafeToRun {
		fmt.Printf("  %s %s\n", labelStyle.Render("Verdict:"), okStyle.Render("safe to run"))
	} else {
		fmt.Printf("  %s %s\n", labelStyle.Render("Verdict:"), warnStyle.Render("not safe to run without changes or approval"))
	}
	fmt.Println()

	for _, step := range report.Steps {
		decisionStyle := okStyle
		switch step.Decision {
		case policy.SimulationDecisionBlock, policy.SimulationDecisionInvalid:
			decisionStyle = errorStyle
		case policy.SimulationDecisionApproval:
			decisionStyle = warnStyle
		}
		fmt.Printf("  %d. %s %s\n", step.Index, decisionStyle.Render(step.Decision), step.Command)
		if step.Policy != nil {
			if step.Policy.Reason != "" {
				fmt.Printf("     %s %s\n", labelStyle.Render("Reason:"), step.Policy.Reason)
			}
			if step.Policy.Pattern != "" {
				fmt.Printf("     %s %s\n", labelStyle.Render("Pattern:"), mutedStyle.Render(step.Policy.Pattern))
			}
			if step.RequiresSLB {
				fmt.Printf("     %s %s\n", labelStyle.Render("Approval:"), warnStyle.Render("SLB required"))
			}
		}
		if step.Error != "" {
			fmt.Printf("     %s %s\n", labelStyle.Render("Error:"), step.Error)
		}
		for _, alt := range step.SaferAlternatives {
			fmt.Printf("     %s %s\n", labelStyle.Render("Alternative:"), alt)
		}
	}
	for _, note := range report.Notes {
		fmt.Printf("  %s %s\n", labelStyle.Render("Note:"), mutedStyle.Render(note))
	}
	fmt.Println()
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
	if err := installWrapper(gitWrapper, policy.GitWrapperScript, force); err != nil {
		return err
	}

	// Install rm wrapper
	rmWrapper := filepath.Join(binDir, "rm")
	if err := installWrapper(rmWrapper, policy.RmWrapperScript, force); err != nil {
		return err
	}

	// Install the Claude Code hook script and register it: Claude Code runs
	// only hooks a settings file lists, never scripts it finds in a directory.
	hookPath := policy.ClaudeHookScriptPath(home)
	if err := os.MkdirAll(filepath.Dir(hookPath), 0755); err != nil {
		return fmt.Errorf("creating hook directory: %w", err)
	}
	if err := installWrapper(hookPath, policy.ClaudeHookScript, force); err != nil {
		return err
	}
	settingsPath := policy.ClaudeUserSettingsPath(home)
	if _, err := policy.RegisterClaudeHook(settingsPath, hookPath); err != nil {
		return fmt.Errorf("registering Claude Code hook in %s: %w", settingsPath, err)
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
		fmt.Printf("  %s Registered it for the Bash tool in: %s\n", okStyle.Render("✓"), settingsPath)
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
			"settings":    settingsPath,
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

	// Unregister the hook before removing its script, so Claude Code is never
	// left pointing at a missing file.
	hookPath := policy.ClaudeHookScriptPath(home)
	settingsPath := policy.ClaudeUserSettingsPath(home)
	if changed, err := policy.UnregisterClaudeHook(settingsPath, hookPath); err != nil {
		return fmt.Errorf("unregistering Claude Code hook from %s: %w", settingsPath, err)
	} else if changed {
		removed = append(removed, settingsPath+" (hooks.PreToolUse entry)")
	}
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
