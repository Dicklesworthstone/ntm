package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/coordinator"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func newCoordinatorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "coordinator",
		Aliases: []string{"coord"},
		Short:   "Manage session coordination for multi-agent workflows",
		Long: `Manage session coordination for multi-agent workflows.

The coordinator monitors agents, detects file conflicts, sends periodic
digests, and can automatically assign work to idle agents based on bv
triage recommendations.

Examples:
  ntm coordinator status myproject        # Show coordinator status
  ntm coordinator digest myproject        # Generate and display digest
  ntm coordinator conflicts myproject     # List current file conflicts
  ntm coordinator assign myproject        # Trigger work assignment

  # Enable/disable features (global config)
  ntm coordinator enable auto-assign
  ntm coordinator enable digest --interval=30m
  ntm coordinator disable conflict-negotiate`,
	}

	cmd.AddCommand(newCoordinatorStatusCmd())
	cmd.AddCommand(newCoordinatorDigestCmd())
	cmd.AddCommand(newCoordinatorConflictsCmd())
	cmd.AddCommand(newCoordinatorAssignCmd())
	cmd.AddCommand(newCoordinatorEnableCmd())
	cmd.AddCommand(newCoordinatorDisableCmd())

	return cmd
}

// newCoordinatorStatusCmd shows coordinator and agent status
func newCoordinatorStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status [session]",
		Short: "Show coordinator status for a session",
		Long: `Show the current coordinator status including:
- Agent states (idle, active, error)
- Context usage per agent
- Active file reservations
- Configuration settings

Examples:
  ntm coordinator status myproject
  ntm coordinator status myproject --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: runCoordinatorStatus,
	}

	return cmd
}

func runCoordinatorStatus(cmd *cobra.Command, args []string) error {
	var session string
	if len(args) > 0 {
		session = args[0]
	}

	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	res, err := ResolveSession(session, cmd.OutOrStdout())
	if err != nil {
		return err
	}
	if res.Session == "" {
		return nil
	}
	res.ExplainIfInferred(cmd.ErrOrStderr())
	session = res.Session

	projectKey, err := resolveAgentMailProjectKey(session)
	if err != nil {
		return err
	}
	if projectKey == "" {
		return fmt.Errorf("getting project root failed")
	}

	// Create coordinator to get status
	mailClient := newAgentMailClient(projectKey)
	coord := coordinator.New(session, projectKey, mailClient, "NTM-Coordinator")

	// Get agent states
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start coordinator briefly to get current state
	if err := coord.Start(ctx); err != nil {
		return fmt.Errorf("starting coordinator: %w", err)
	}
	defer coord.Stop()

	agents := coord.GetAgents()
	idleAgents := coord.GetIdleAgents()

	// Honor [coordinator] settings from ~/.config/ntm/config.toml. Fall back
	// to runtime defaults if the config file is missing or fails to parse.
	coordConfig := coordinator.DefaultCoordinatorConfig()
	if cfg, err := config.Load(selectedConfigPath()); err == nil && cfg != nil {
		coordConfig = coordinatorConfigFromTOML(cfg.Coordinator, coordConfig)
	}

	if jsonOutput {
		return outputCoordinatorStatusJSON(session, agents, idleAgents, coordConfig)
	}

	return renderCoordinatorStatus(session, agents, idleAgents, coordConfig)
}

func outputCoordinatorStatusJSON(session string, agents map[string]*coordinator.AgentState, idleAgents []*coordinator.AgentState, coordCfg coordinator.CoordinatorConfig) error {
	result := map[string]interface{}{
		"session":     session,
		"timestamp":   time.Now().Format(time.RFC3339),
		"agent_count": len(agents),
		"idle_count":  len(idleAgents),
		"agents":      agents,
		"config": map[string]interface{}{
			"auto_assign":        coordCfg.AutoAssign,
			"send_digests":       coordCfg.SendDigests,
			"conflict_notify":    coordCfg.ConflictNotify,
			"conflict_negotiate": coordCfg.ConflictNegotiate,
			"poll_interval":      coordCfg.PollInterval.String(),
			"digest_interval":    coordCfg.DigestInterval.String(),
			"idle_threshold":     coordCfg.IdleThreshold,
		},
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func renderCoordinatorStatus(session string, agents map[string]*coordinator.AgentState, idleAgents []*coordinator.AgentState, coordCfg coordinator.CoordinatorConfig) error {
	t := theme.Current()

	fmt.Printf("\n%s Coordinator Status: %s%s\n\n",
		colorize(t.Primary), session, "\033[0m")

	// Summary
	fmt.Printf("  %sAgents:%s %d total, %d idle\n",
		"\033[1m", "\033[0m", len(agents), len(idleAgents))
	fmt.Println()

	// Agent table
	if len(agents) > 0 {
		// Sort agents by PaneIndex for deterministic output
		sortedAgents := make([]*coordinator.AgentState, 0, len(agents))
		for _, agent := range agents {
			sortedAgents = append(sortedAgents, agent)
		}
		slices.SortFunc(sortedAgents, func(a, b *coordinator.AgentState) int {
			return a.PaneIndex - b.PaneIndex
		})

		fmt.Printf("  %sAgent Status%s\n", "\033[1m", "\033[0m")
		fmt.Printf("  %s%s%s\n", "\033[2m", strings.Repeat("─", 60), "\033[0m")
		fmt.Printf("  %-12s %-8s %-12s %-8s %s\n",
			"Pane", "Type", "Status", "Context", "Idle For")
		fmt.Printf("  %s%s%s\n", "\033[2m", strings.Repeat("─", 60), "\033[0m")

		for _, agent := range sortedAgents {
			statusColor := "\033[32m" // green
			switch agent.Status {
			case robot.StateError:
				statusColor = "\033[31m" // red
			case robot.StateGenerating, robot.StateThinking:
				statusColor = "\033[33m" // yellow
			}

			idleFor := "-"
			if !agent.LastActivity.IsZero() && agent.Status == robot.StateWaiting {
				idleFor = formatIdleDuration(time.Since(agent.LastActivity))
			}

			fmt.Printf("  %-12d %-8s %s%-12s%s %-8.0f%% %s\n",
				agent.PaneIndex, agent.AgentType,
				statusColor, string(agent.Status), "\033[0m",
				agent.ContextUsage, idleFor)
		}
		fmt.Println()
	}

	// Configuration
	fmt.Printf("  %sConfiguration%s\n", "\033[1m", "\033[0m")
	fmt.Printf("  %s%s%s\n", "\033[2m", strings.Repeat("─", 60), "\033[0m")

	printConfigBool("  Auto-assign:         ", coordCfg.AutoAssign)
	printConfigBool("  Send digests:        ", coordCfg.SendDigests)
	printConfigBool("  Conflict notify:     ", coordCfg.ConflictNotify)
	printConfigBool("  Conflict negotiate:  ", coordCfg.ConflictNegotiate)
	fmt.Printf("  Poll interval:       %s\n", coordCfg.PollInterval)
	fmt.Printf("  Digest interval:     %s\n", coordCfg.DigestInterval)
	fmt.Printf("  Idle threshold:      %.0fs\n", coordCfg.IdleThreshold)
	fmt.Println()

	return nil
}

func printConfigBool(label string, value bool) {
	status := "\033[31m✗ disabled\033[0m"
	if value {
		status = "\033[32m✓ enabled\033[0m"
	}
	fmt.Printf("%s%s\n", label, status)
}

func formatIdleDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

// newCoordinatorDigestCmd generates a session digest
func newCoordinatorDigestCmd() *cobra.Command {
	var sendMail bool

	cmd := &cobra.Command{
		Use:   "digest [session]",
		Short: "Generate and display a session digest",
		Long: `Generate a summary digest of the current session state.

The digest includes:
- Agent counts and status breakdown
- Active/idle/error agent counts
- Context usage alerts
- Work summary (if beads available)

Examples:
  ntm coordinator digest myproject
  ntm coordinator digest myproject --send   # Also send via Agent Mail
  ntm coordinator digest myproject --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCoordinatorDigest(cmd, args, sendMail)
		},
	}

	cmd.Flags().BoolVar(&sendMail, "send", false, "Send digest via Agent Mail")

	return cmd
}

func runCoordinatorDigest(cmd *cobra.Command, args []string, sendMail bool) error {
	var session string
	if len(args) > 0 {
		session = args[0]
	}

	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	res, err := ResolveSession(session, cmd.OutOrStdout())
	if err != nil {
		return err
	}
	if res.Session == "" {
		return nil
	}
	res.ExplainIfInferred(cmd.ErrOrStderr())
	session = res.Session

	projectKey, err := resolveAgentMailProjectKey(session)
	if err != nil {
		return err
	}
	if projectKey == "" {
		return fmt.Errorf("getting project root failed")
	}

	mailClient := newAgentMailClient(projectKey)
	coord := coordinator.New(session, projectKey, mailClient, "NTM-Coordinator")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := coord.Start(ctx); err != nil {
		return fmt.Errorf("starting coordinator: %w", err)
	}
	defer coord.Stop()

	digest := coord.GenerateDigest()

	if sendMail {
		if err := coord.SendDigest(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to send digest: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "Digest sent via Agent Mail\n")
		}
	}

	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(digest)
	}

	return renderDigest(digest)
}

func renderDigest(digest coordinator.DigestSummary) error {
	t := theme.Current()

	fmt.Printf("\n%s Session Digest: %s%s\n",
		colorize(t.Primary), digest.Session, "\033[0m")
	fmt.Printf("  Generated: %s\n\n", digest.GeneratedAt.Format(time.RFC3339))

	// Summary
	fmt.Printf("  %sSummary%s\n", "\033[1m", "\033[0m")
	fmt.Printf("  %s%s%s\n", "\033[2m", strings.Repeat("─", 40), "\033[0m")
	fmt.Printf("  Total Agents: %d\n", digest.AgentCount)
	fmt.Printf("  Active:       %d\n", digest.ActiveCount)
	fmt.Printf("  Idle:         %d\n", digest.IdleCount)
	if digest.ErrorCount > 0 {
		fmt.Printf("  %sErrors:       %d%s ⚠️\n", "\033[31m", digest.ErrorCount, "\033[0m")
	}
	fmt.Println()

	// Alerts
	if len(digest.Alerts) > 0 {
		fmt.Printf("  %sAlerts%s\n", "\033[1m", "\033[0m")
		fmt.Printf("  %s%s%s\n", "\033[2m", strings.Repeat("─", 40), "\033[0m")
		for _, alert := range digest.Alerts {
			fmt.Printf("  %s⚠️  %s%s\n", "\033[33m", alert, "\033[0m")
		}
		fmt.Println()
	}

	// Agent table
	if len(digest.AgentStatuses) > 0 {
		fmt.Printf("  %sAgent Status%s\n", "\033[1m", "\033[0m")
		fmt.Printf("  %s%s%s\n", "\033[2m", strings.Repeat("─", 50), "\033[0m")
		fmt.Printf("  %-6s %-8s %-12s %-8s %s\n",
			"Pane", "Type", "Status", "Context", "Idle")
		fmt.Printf("  %s%s%s\n", "\033[2m", strings.Repeat("─", 50), "\033[0m")

		for _, agent := range digest.AgentStatuses {
			idleFor := "-"
			if agent.IdleFor != "" {
				idleFor = agent.IdleFor
			}
			fmt.Printf("  %-6d %-8s %-12s %-8.0f%% %s\n",
				agent.PaneIndex, agent.AgentType, agent.Status,
				agent.ContextUsage, idleFor)
		}
		fmt.Println()
	}

	return nil
}

// newCoordinatorConflictsCmd lists file reservation conflicts
func newCoordinatorConflictsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "conflicts [session]",
		Short: "List current file reservation conflicts",
		Long: `List any active file reservation conflicts between agents.

Conflicts occur when multiple agents hold overlapping file reservations.
The coordinator can notify holders or attempt automatic resolution.

Examples:
  ntm coordinator conflicts myproject
  ntm coordinator conflicts myproject --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: runCoordinatorConflicts,
	}

	return cmd
}

func runCoordinatorConflicts(cmd *cobra.Command, args []string) error {
	var session string
	if len(args) > 0 {
		session = args[0]
	}

	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	res, err := ResolveSession(session, cmd.OutOrStdout())
	if err != nil {
		return err
	}
	if res.Session == "" {
		return nil
	}
	res.ExplainIfInferred(cmd.ErrOrStderr())
	session = res.Session

	projectKey, err := resolveAgentMailProjectKey(session)
	if err != nil {
		return err
	}
	if projectKey == "" {
		return fmt.Errorf("getting project root failed")
	}

	mailClient := newAgentMailClient(projectKey)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	detector := coordinator.NewConflictDetector(mailClient, projectKey)
	conflicts, err := detector.DetectConflicts(ctx)
	if err != nil {
		return fmt.Errorf("detecting conflicts: %w", err)
	}

	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
			"session":   session,
			"conflicts": conflicts,
			"count":     len(conflicts),
		})
	}

	t := theme.Current()
	fmt.Printf("\n%s File Conflicts: %s%s\n\n",
		colorize(t.Primary), session, "\033[0m")

	if len(conflicts) == 0 {
		fmt.Println("  No active conflicts detected.")
		fmt.Println()
		return nil
	}

	for _, c := range conflicts {
		fmt.Printf("  %s⚠️  %s%s\n", "\033[33m", c.Pattern, "\033[0m")
		fmt.Printf("     Detected: %s\n", c.DetectedAt.Format(time.RFC3339))
		fmt.Printf("     Holders:\n")
		for _, h := range c.Holders {
			fmt.Printf("       - %s (reserved %s, expires %s)\n",
				h.AgentName,
				h.ReservedAt.Format("15:04:05"),
				h.ExpiresAt.Format("15:04:05"))
			if h.Reason != "" {
				fmt.Printf("         Reason: %s\n", h.Reason)
			}
		}
		fmt.Println()
	}

	return nil
}

// newCoordinatorAssignCmd triggers work assignment
func newCoordinatorAssignCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "assign [session]",
		Short: "Trigger work assignment to idle agents",
		Long: `Assign work to idle agents based on bv triage recommendations.

This is a thin wrapper around the canonical "ntm assign" flow
(recommended: "ntm assign <session> --auto").

Examples:
  ntm coordinator assign myproject
  ntm coordinator assign myproject --dry-run   # Preview without sending
  ntm coordinator assign myproject --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCoordinatorAssign(cmd, args, dryRun)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview assignments without executing")
	cmd.Flags().StringVar(&assignStrategy, "strategy", "balanced", "Assignment strategy: balanced, speed, quality, dependency, round-robin")
	cmd.Flags().IntVar(&assignLimit, "limit", 0, "Maximum number of assignments (0 = unlimited)")
	cmd.Flags().StringVar(&assignAgentType, "agent", "", "Filter by agent type: claude, codex, gemini")
	cmd.Flags().BoolVar(&assignCCOnly, "cc-only", false, "Only assign to Claude agents (alias for --agent=claude)")
	cmd.Flags().BoolVar(&assignCodOnly, "cod-only", false, "Only assign to Codex agents (alias for --agent=codex)")
	cmd.Flags().BoolVar(&assignGmiOnly, "gmi-only", false, "Only assign to Gemini agents (alias for --agent=gemini)")
	cmd.Flags().StringVar(&assignBeads, "beads", "", "Comma-separated list of specific bead IDs to assign")
	cmd.Flags().StringVar(&assignTemplate, "template", "", "Prompt template: impl, review, custom")
	cmd.Flags().StringVar(&assignTemplateFile, "template-file", "", "Custom prompt template file path")
	cmd.Flags().BoolVar(&assignVerbose, "verbose", false, "Show detailed scoring/decision logs")
	cmd.Flags().BoolVar(&assignQuiet, "quiet", false, "Suppress non-essential output")
	cmd.Flags().DurationVar(&assignTimeout, "timeout", 30*time.Second, "Timeout for external calls (bv, br, Agent Mail)")
	cmd.Flags().BoolVar(&assignReserveFiles, "reserve-files", true, "Reserve files via Agent Mail when assigning")
	cmd.Flags().IntVar(&assignPane, "pane", -1, "Assign bead directly to a specific pane (requires --beads)")
	cmd.Flags().BoolVar(&assignForce, "force", false, "Force assignment even if pane is busy")
	cmd.Flags().BoolVar(&assignIgnoreDeps, "ignore-deps", false, "Ignore dependency checks for assignment")
	cmd.Flags().StringVar(&assignPrompt, "prompt", "", "Custom prompt for direct assignment")

	return cmd
}

func runCoordinatorAssign(cmd *cobra.Command, args []string, dryRun bool) error {
	var session string
	if len(args) > 0 {
		session = args[0]
	}

	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	res, err := ResolveSession(session, cmd.OutOrStdout())
	if err != nil {
		return err
	}
	if res.Session == "" {
		return nil
	}
	res.ExplainIfInferred(cmd.ErrOrStderr())
	session = res.Session

	// Apply default strategy for coordinator wrapper.
	strategy := strings.TrimSpace(assignStrategy)
	if strategy == "" {
		if cfg != nil && cfg.Assign.Strategy != "" {
			strategy = cfg.Assign.Strategy
		} else {
			strategy = config.DefaultAssignConfig().Strategy
		}
	}

	// Validate strategy
	if !config.IsValidStrategy(strategy) {
		return fmt.Errorf("unknown strategy %q. Valid strategies: %s",
			strategy, strings.Join(config.ValidAssignStrategies, ", "))
	}

	// Resolve agent type filter from flags
	agentTypeFilter := resolveAgentTypeFilter()

	timeout := assignTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	var beadIDs []string
	if assignBeads != "" {
		beadIDs = strings.Split(assignBeads, ",")
		for i := range beadIDs {
			beadIDs[i] = strings.TrimSpace(beadIDs[i])
		}
	}

	assignOpts := &AssignCommandOptions{
		Session:         session,
		BeadIDs:         beadIDs,
		Strategy:        strategy,
		Limit:           assignLimit,
		AgentTypeFilter: agentTypeFilter,
		Template:        assignTemplate,
		TemplateFile:    assignTemplateFile,
		Verbose:         assignVerbose,
		Quiet:           assignQuiet,
		Timeout:         timeout,
		ReserveFiles:    assignReserveFiles,
		Pane:            assignPane,
		Force:           assignForce,
		IgnoreDeps:      assignIgnoreDeps,
		Prompt:          assignPrompt,
	}

	if assignPane >= 0 {
		return runDirectPaneAssignment(cmd, assignOpts)
	}

	if IsJSONOutput() {
		return runAssignJSON(assignOpts)
	}

	assignOutput, err := getAssignOutputEnhanced(assignOpts)
	if err != nil {
		return err
	}

	if !assignQuiet {
		displayAssignOutputEnhanced(assignOutput, assignVerbose)
	}

	if dryRun || len(assignOutput.Assignments) == 0 {
		return nil
	}

	return executeAssignmentsEnhanced(session, assignOutput, assignOpts)
}

// newCoordinatorEnableCmd enables coordinator features
func newCoordinatorEnableCmd() *cobra.Command {
	var interval string

	cmd := &cobra.Command{
		Use:   "enable <feature>",
		Short: "Enable a coordinator feature",
		Long: `Enable a coordinator feature globally.

Available features:
  auto-assign         - Automatically assign work to idle agents
  digest              - Send periodic digest summaries
  conflict-notify     - Notify when conflicts are detected
  conflict-negotiate  - Attempt automatic conflict resolution

Note: These settings are configured globally in ~/.config/ntm/config.toml.

Examples:
  ntm coordinator enable auto-assign
  ntm coordinator enable digest --interval=30m
  ntm coordinator enable conflict-notify`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCoordinatorToggle(cmd, args, true, interval)
		},
	}

	cmd.Flags().StringVar(&interval, "interval", "", "Interval for digest (e.g., 5m, 30m, 1h)")

	return cmd
}

// newCoordinatorDisableCmd disables coordinator features
func newCoordinatorDisableCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "disable <feature>",
		Short: "Disable a coordinator feature",
		Long: `Disable a coordinator feature globally.

Available features:
  auto-assign         - Automatic work assignment
  digest              - Periodic digest summaries
  conflict-notify     - Conflict notifications
  conflict-negotiate  - Automatic conflict resolution

Note: These settings are configured globally in ~/.config/ntm/config.toml.

Examples:
  ntm coordinator disable auto-assign
  ntm coordinator disable digest`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCoordinatorToggle(cmd, args, false, "")
		},
	}

	return cmd
}

func runCoordinatorToggle(cmd *cobra.Command, args []string, enable bool, interval string) error {
	feature := args[0]

	// Validate feature name
	validFeatures := []string{"auto-assign", "digest", "conflict-notify", "conflict-negotiate"}
	valid := false
	for _, f := range validFeatures {
		if f == feature {
			valid = true
			break
		}
	}

	if !valid {
		return fmt.Errorf("unknown feature '%s'. Valid features: %s",
			feature, strings.Join(validFeatures, ", "))
	}

	action := "disabled"
	if enable {
		action = "enabled"
	}

	// Show configuration instructions
	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
			"feature":     feature,
			"enabled":     enable,
			"config_hint": "Add to ~/.config/ntm/config.toml under [coordinator] section",
		})
	}

	status := "\033[31m✗ " + action + "\033[0m"
	if enable {
		status = "\033[32m✓ " + action + "\033[0m"
	}

	fmt.Printf("Coordinator feature '%s': %s\n\n", feature, status)

	// Show config instructions
	fmt.Printf("To persist this setting, add to ~/.config/ntm/config.toml:\n\n")
	fmt.Printf("  [coordinator]\n")

	switch feature {
	case "auto-assign":
		fmt.Printf("  auto_assign = %t\n", enable)
	case "digest":
		fmt.Printf("  send_digests = %t\n", enable)
		if enable && interval != "" {
			fmt.Printf("  digest_interval = \"%s\"\n", interval)
		}
	case "conflict-notify":
		fmt.Printf("  conflict_notify = %t\n", enable)
	case "conflict-negotiate":
		fmt.Printf("  conflict_negotiate = %t\n", enable)
	}
	fmt.Println()

	return nil
}

// coordinatorConfigFromTOML maps the TOML mirror struct (already merged on
// top of config.DefaultCoordinatorConfig() during config.Load) onto the
// runtime coordinator.CoordinatorConfig. Durations are clamped to
// coordinator.MinPollInterval / coordinator.MinDigestInterval to match the
// existing validation inside SessionCoordinator.Start.
func coordinatorConfigFromTOML(toml config.CoordinatorConfig, fallback coordinator.CoordinatorConfig) coordinator.CoordinatorConfig {
	out := coordinator.CoordinatorConfig{
		PollInterval:      toml.PollInterval,
		DigestInterval:    toml.DigestInterval,
		AutoAssign:        toml.AutoAssign,
		IdleThreshold:     toml.IdleThreshold,
		AssignOnlyIdle:    toml.AssignOnlyIdle,
		ConflictNotify:    toml.ConflictNotify,
		ConflictNegotiate: toml.ConflictNegotiate,
		SendDigests:       toml.SendDigests,
		HumanAgent:        toml.HumanAgent,
	}
	if out.PollInterval < coordinator.MinPollInterval {
		out.PollInterval = coordinator.MinPollInterval
	}
	if out.DigestInterval < coordinator.MinDigestInterval {
		out.DigestInterval = coordinator.MinDigestInterval
	}
	if strings.TrimSpace(out.HumanAgent) == "" {
		out.HumanAgent = fallback.HumanAgent
	}
	return out
}
