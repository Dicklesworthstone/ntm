package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/coordinator"
	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func registerCoordinatorKernel() {
	kernel.MustRegister(kernel.Command{
		Name:        "sessions.coordinator",
		Description: "Inspect coordinator agent states, runtime status, and active configuration for a session",
		Category:    "sessions",
		Input: &kernel.SchemaRef{
			Name: "CoordinatorInput",
			Ref:  "robot.CoordinatorInput",
		},
		Output: &kernel.SchemaRef{
			Name: "CoordinatorOutput",
			Ref:  "robot.CoordinatorOutput",
		},
		REST: &kernel.RESTBinding{
			Method: "GET",
			Path:   "/api/v1/sessions/{id}/coordinator",
		},
		Examples: []kernel.Example{
			{
				Name:        "coordinator-status",
				Description: "Inspect coordinator status for myproject",
				Command:     "ntm --robot-coordinator=myproject",
			},
		},
		SafetyLevel: kernel.SafetySafe,
		Idempotent:  true,
	})
	kernel.MustRegisterHandler("sessions.coordinator", func(ctx context.Context, input any) (any, error) {
		opts, err := decodeKernelInput[robot.CoordinatorInput]("sessions.coordinator", input)
		if err != nil {
			return nil, fmt.Errorf("invalid sessions.coordinator input: %w", err)
		}
		if strings.TrimSpace(opts.Action) == "" {
			opts.Action = "status"
		}
		out, err := executeCoordinatorRobot(ctx, opts)
		if err != nil {
			return nil, err
		}
		if !out.Success {
			return nil, errors.New(out.Error)
		}
		return out, nil
	})

	kernel.MustRegister(kernel.Command{
		Name:        "sessions.coordinator_digest",
		Description: "Generate a coordinator session digest and optionally dispatch it via Agent Mail",
		Category:    "sessions",
		Input: &kernel.SchemaRef{
			Name: "CoordinatorInput",
			Ref:  "robot.CoordinatorInput",
		},
		Output: &kernel.SchemaRef{
			Name: "CoordinatorOutput",
			Ref:  "robot.CoordinatorOutput",
		},
		REST: &kernel.RESTBinding{
			Method: "POST",
			Path:   "/api/v1/sessions/{id}/coordinator/digest",
		},
		Examples: []kernel.Example{
			{
				Name:        "coordinator-digest",
				Description: "Generate a coordinator digest for myproject",
				Command:     "ntm --robot-coordinator=myproject --coordinator-action=digest",
			},
		},
		SafetyLevel: kernel.SafetySafe,
		Idempotent:  false,
	})
	kernel.MustRegisterHandler("sessions.coordinator_digest", func(ctx context.Context, input any) (any, error) {
		opts, err := decodeKernelInput[robot.CoordinatorInput]("sessions.coordinator_digest", input)
		if err != nil {
			return nil, fmt.Errorf("invalid sessions.coordinator_digest input: %w", err)
		}
		opts.Action = "digest"
		out, err := executeCoordinatorRobot(ctx, opts)
		if err != nil {
			return nil, err
		}
		if !out.Success {
			return nil, errors.New(out.Error)
		}
		return out, nil
	})

	kernel.MustRegister(kernel.Command{
		Name:        "sessions.coordinator_conflicts",
		Description: "Detect active Agent Mail file reservation conflicts for a session",
		Category:    "sessions",
		Input: &kernel.SchemaRef{
			Name: "CoordinatorInput",
			Ref:  "robot.CoordinatorInput",
		},
		Output: &kernel.SchemaRef{
			Name: "CoordinatorOutput",
			Ref:  "robot.CoordinatorOutput",
		},
		REST: &kernel.RESTBinding{
			Method: "GET",
			Path:   "/api/v1/sessions/{id}/coordinator/conflicts",
		},
		Examples: []kernel.Example{
			{
				Name:        "coordinator-conflicts",
				Description: "List active file reservation conflicts for myproject",
				Command:     "ntm --robot-coordinator=myproject --coordinator-action=conflicts",
			},
		},
		SafetyLevel: kernel.SafetySafe,
		Idempotent:  true,
	})
	kernel.MustRegisterHandler("sessions.coordinator_conflicts", func(ctx context.Context, input any) (any, error) {
		opts, err := decodeKernelInput[robot.CoordinatorInput]("sessions.coordinator_conflicts", input)
		if err != nil {
			return nil, fmt.Errorf("invalid sessions.coordinator_conflicts input: %w", err)
		}
		opts.Action = "conflicts"
		opts.SendDigest = false
		out, err := executeCoordinatorRobot(ctx, opts)
		if err != nil {
			return nil, err
		}
		if !out.Success {
			return nil, errors.New(out.Error)
		}
		return out, nil
	})
}

// coordinatorFallbackName is the legacy hard-coded coordinator sender. It was
// never a registered Agent Mail identity, so the server rejects mail and
// claims made under it ("Agent 'NTM-Coordinator' not found in project"). It
// survives only as the last-resort identity when Agent Mail is unreachable
// AND no previously registered session identity is persisted — in that state
// nothing can be delivered anyway, and a recognizable literal beats an empty
// sender.
const coordinatorFallbackName = "NTM-Coordinator"

// resolveCoordinatorIdentity returns the Agent Mail identity the coordinator
// acts as. First choice is the session's server-registered coordinator agent:
// RegisterSessionAgent registers a server-generated adjective+noun identity at
// first use, persists it (name + registration token) under the session, and
// reuses it on every later call — the same identity `ntm spawn` registers.
// When the server is unreachable, a previously persisted identity is still
// the right sender; the legacy literal is the final fallback.
func resolveCoordinatorIdentity(ctx context.Context, client *agentmail.Client, session, projectKey string) string {
	if client != nil {
		regCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		// A non-nil info with a non-nil err means the local identity exists
		// but the server activity refresh failed — the name is still valid.
		if info, _ := client.RegisterSessionAgent(regCtx, session, projectKey); info != nil && strings.TrimSpace(info.AgentName) != "" {
			return info.AgentName
		}
	}
	if saved, err := agentmail.LoadSessionAgent(session, projectKey); err == nil && saved != nil && strings.TrimSpace(saved.AgentName) != "" {
		return saved.AgentName
	}
	return coordinatorFallbackName
}

var (
	coordinatorSessionExists  = tmux.SessionExists
	coordinatorGetPanes       = tmux.GetPanes
	coordinatorPaneCurrentDir = func(paneID string) (string, error) {
		return tmux.DefaultClient.Run("display-message", "-p", "-t", tmux.ExactTarget(paneID), "#{pane_current_path}")
	}
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

Every session started by 'ntm spawn' runs the coordinator inside its session
monitor. It always renews the file reservations of in-progress assignments and
releases them when the assigned bead closes; the optional features below act
only once enabled.

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
	cmd.AddCommand(newCoordinatorRunCmd())
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

	projectKey, err := resolveCoordinatorProjectKey(cmd.Context(), session, res.Inferred)
	if err != nil {
		return err
	}
	if projectKey == "" {
		return fmt.Errorf("getting project root failed")
	}

	coordConfig := loadCoordinatorRuntimeConfig()
	// Inspection reads the saved identity without registering or refreshing
	// it on the server, and never starts the coordinator's maintenance loops.
	mailClient := newAgentMailClient(projectKey)
	coordName := resolveCoordinatorIdentity(cmd.Context(), nil, session, projectKey)
	coord := coordinator.New(session, projectKey, mailClient, coordName).WithConfig(coordConfig)

	// Get agent states
	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
	defer cancel()

	if err := coord.Observe(ctx); err != nil {
		return fmt.Errorf("observing coordinator: %w", err)
	}

	agents := coord.GetAgents()
	idleAgents := coord.GetIdleAgents()
	runtime := readCoordinatorRuntime(session)

	if jsonOutput {
		return outputCoordinatorStatusJSON(session, agents, idleAgents, coordConfig, runtime)
	}

	return renderCoordinatorStatus(session, agents, idleAgents, coordConfig, runtime)
}

// coordinatorRuntimeView reports where, if anywhere, the session's
// coordinator is running. Only a live session monitor publishes one.
type coordinatorRuntimeView = robot.CoordinatorRuntimeView

func buildCoordinatorConfigView(coordCfg coordinator.CoordinatorConfig) robot.CoordinatorConfigView {
	return robot.CoordinatorConfigView{
		AutoAssign:        coordCfg.AutoAssign,
		AssignOnlyIdle:    coordCfg.AssignOnlyIdle,
		SendDigests:       coordCfg.SendDigests,
		ConflictNotify:    coordCfg.ConflictNotify,
		ConflictNegotiate: coordCfg.ConflictNegotiate,
		PollInterval:      coordCfg.PollInterval.String(),
		DigestInterval:    coordCfg.DigestInterval.String(),
		IdleThreshold:     coordCfg.IdleThreshold,
		HumanAgent:        coordCfg.HumanAgent,
	}
}

func newCoordinatorRobotErrorOutput(session, action string, err error, code, hint string) *robot.CoordinatorOutput {
	return &robot.CoordinatorOutput{
		RobotResponse: robot.NewErrorResponse(err, code, hint),
		SchemaID:      robot.CoordinatorSchemaID,
		SchemaVersion: robot.CoordinatorSchemaVersion,
		Session:       strings.TrimSpace(session),
		Action:        action,
	}
}

func executeCoordinatorRobot(ctx context.Context, input robot.CoordinatorInput) (*robot.CoordinatorOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	action := strings.ToLower(strings.TrimSpace(input.Action))
	if action == "" {
		action = "status"
	}
	if err := ctx.Err(); err != nil {
		return newCoordinatorRobotErrorOutput(input.Session, action, fmt.Errorf("coordinator canceled: %w", err), robot.ErrCodeTimeout, "Retry --robot-coordinator with an active context"), nil
	}

	switch action {
	case "status", "digest", "conflicts":
	default:
		return newCoordinatorRobotErrorOutput(input.Session, action, fmt.Errorf("invalid coordinator action %q (expected status, digest, or conflicts)", input.Action), robot.ErrCodeInvalidFlag, "Use --coordinator-action=status, --coordinator-action=digest, or --coordinator-action=conflicts"), nil
	}
	if input.SendDigest && action != "digest" {
		return newCoordinatorRobotErrorOutput(input.Session, action, errors.New("invalid coordinator option: --coordinator-send-digest requires --coordinator-action=digest"), robot.ErrCodeInvalidFlag, "Set --coordinator-action=digest when passing --coordinator-send-digest"), nil
	}

	session := strings.TrimSpace(input.Session)
	if session == "" {
		return newCoordinatorRobotErrorOutput("", action, errors.New("session is required"), robot.ErrCodeInvalidFlag, "Pass --robot-coordinator=SESSION"), nil
	}
	if err := tmux.ValidateSessionName(session); err != nil {
		return newCoordinatorRobotErrorOutput(session, action, fmt.Errorf("invalid session name: %w", err), robot.ErrCodeInvalidFlag, "Provide a valid tmux session name"), nil
	}

	if !coordinatorSessionExists(session) {
		return newCoordinatorRobotErrorOutput(session, action, fmt.Errorf("session '%s' does not exist", session), robot.ErrCodeSessionNotFound, "Use 'ntm --robot-status' to see active sessions"), nil
	}

	projectKey, err := resolveCoordinatorProjectKey(ctx, session, false)
	if err != nil {
		code := robot.ErrCodeInternalError
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = robot.ErrCodeTimeout
		}
		return newCoordinatorRobotErrorOutput(session, action, err, code, "Verify the session has a resolvable project directory"), nil
	}
	if projectKey == "" {
		return newCoordinatorRobotErrorOutput(session, action, errors.New("getting project root failed"), robot.ErrCodeInternalError, "Verify the session has a resolvable project directory"), nil
	}

	out := &robot.CoordinatorOutput{
		RobotResponse: robot.NewRobotResponse(true),
		SchemaID:      robot.CoordinatorSchemaID,
		SchemaVersion: robot.CoordinatorSchemaVersion,
		Session:       session,
		Action:        action,
	}

	switch action {
	case "status":
		coordConfig := loadCoordinatorRuntimeConfig()
		mailClient := newAgentMailClient(projectKey)
		coordName := resolveCoordinatorIdentity(ctx, nil, session, projectKey)
		coord := coordinator.New(session, projectKey, mailClient, coordName).WithConfig(coordConfig)

		obsCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := coord.Observe(obsCtx); err != nil {
			code := robot.ErrCodeInternalError
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				code = robot.ErrCodeTimeout
			}
			return newCoordinatorRobotErrorOutput(session, action, fmt.Errorf("observing coordinator: %w", err), code, "Check tmux session health"), nil
		}

		agents := coord.GetAgents()
		if agents == nil {
			agents = map[string]*coordinator.AgentState{}
		}
		idleAgents := coord.GetIdleAgents()
		runtimeView := readCoordinatorRuntime(session)
		cfgView := buildCoordinatorConfigView(coordConfig)

		out.AgentCount = len(agents)
		out.IdleCount = len(idleAgents)
		out.Agents = agents
		out.Runtime = &runtimeView
		out.Config = &cfgView
		out.AgentHints = &robot.AgentHints{
			Summary: fmt.Sprintf("Coordinator status for %s: %d agent(s) (%d idle), runtime=%s", session, out.AgentCount, out.IdleCount, runtimeView.State),
		}

	case "digest":
		mailClient := newAgentMailClient(projectKey)
		runtimeConfig := loadCoordinatorRuntimeConfig()
		identityClient := mailClient
		if !input.SendDigest {
			identityClient = nil
		}
		coordName := resolveCoordinatorIdentity(ctx, identityClient, session, projectKey)
		coord := coordinator.New(session, projectKey, mailClient, coordName).WithConfig(runtimeConfig)

		obsCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := coord.Observe(obsCtx); err != nil {
			code := robot.ErrCodeInternalError
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				code = robot.ErrCodeTimeout
			}
			return newCoordinatorRobotErrorOutput(session, action, fmt.Errorf("observing coordinator: %w", err), code, "Check tmux session health"), nil
		}

		digest := coord.GenerateDigest()
		var warnings []string
		if input.SendDigest {
			if err := coord.SendDigest(obsCtx); err != nil {
				warnings = append(warnings, fmt.Sprintf("failed to send digest via Agent Mail: %v", err))
			} else {
				out.DigestSent = true
			}
		}
		out.AgentCount = digest.AgentCount
		out.IdleCount = digest.IdleCount
		out.Digest = &digest
		out.AgentHints = &robot.AgentHints{
			Summary:  fmt.Sprintf("Digest for %s: %d agent(s) (%d active, %d idle, %d error)", session, digest.AgentCount, digest.ActiveCount, digest.IdleCount, digest.ErrorCount),
			Warnings: warnings,
		}

	case "conflicts":
		mailClient := newAgentMailClient(projectKey)
		detCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		detector := coordinator.NewConflictDetector(mailClient, projectKey)
		conflicts, err := detector.DetectConflicts(detCtx)
		if err != nil {
			code := robot.ErrCodeInternalError
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				code = robot.ErrCodeTimeout
			}
			return newCoordinatorRobotErrorOutput(session, action, fmt.Errorf("detecting conflicts: %w", err), code, "Verify Agent Mail connectivity"), nil
		}
		if conflicts == nil {
			conflicts = []coordinator.Conflict{}
		}
		out.Conflicts = conflicts
		out.ConflictCount = len(conflicts)
		out.AgentHints = &robot.AgentHints{
			Summary: fmt.Sprintf("Coordinator conflict scan for %s: %d active conflict(s)", session, len(conflicts)),
		}
	}

	return out, nil
}

const (
	coordinatorRuntimeHostNone   = "none"
	coordinatorRuntimeNotRunning = "not_running"
)

func readCoordinatorRuntime(session string) coordinatorRuntimeView {
	status, err := resilience.ReadSessionCoordinatorStatus(session)
	if err != nil || !status.Alive {
		return coordinatorRuntimeView{
			Host: coordinatorRuntimeHostNone, State: coordinatorRuntimeNotRunning, Features: []string{},
			Summary: fmt.Sprintf("not running: no live session monitor hosts it; 'ntm coordinator run %s' coordinates it in the foreground", session),
		}
	}
	features := status.Features
	if features == nil {
		features = []string{}
	}
	stateSince, heartbeatAt := status.StateSince, status.HeartbeatAt
	view := coordinatorRuntimeView{
		Host: status.Host, State: status.State, PID: status.PID, Mode: status.Mode, Features: features,
		ConfigPath: status.ConfigPath, Healthy: status.Healthy, StateSince: &stateSince, HeartbeatAt: &heartbeatAt,
		LastMaintenanceAt: status.LastMaintenanceAt, MaintenanceError: status.MaintenanceError, Error: status.Error,
	}
	switch status.State {
	case resilience.SessionCoordinatorRunning:
		if status.Mode == monitorCoordinatorModeMaintenance {
			view.Summary = fmt.Sprintf("running inside the session monitor (pid %d): maintaining assignment leases; no optional features enabled", status.PID)
		} else {
			view.Summary = fmt.Sprintf("running inside the session monitor (pid %d) with %s", status.PID, strings.Join(features, ", "))
		}
	case resilience.SessionCoordinatorYielded:
		view.Summary = fmt.Sprintf("paused inside the session monitor (pid %d) while a foreground 'ntm coordinator run' or 'ntm assign --watch' coordinates the session", status.PID)
	default:
		view.Summary = fmt.Sprintf("%s inside the session monitor (pid %d)", status.State, status.PID)
	}
	if !status.Healthy {
		view.Summary += " (status heartbeat is stale)"
	}
	return view
}

func outputCoordinatorStatusJSON(session string, agents map[string]*coordinator.AgentState, idleAgents []*coordinator.AgentState, coordCfg coordinator.CoordinatorConfig, runtime coordinatorRuntimeView) error {
	result := map[string]interface{}{
		"session":     session,
		"timestamp":   time.Now().Format(time.RFC3339),
		"agent_count": len(agents),
		"idle_count":  len(idleAgents),
		"agents":      agents,
		"runtime":     runtime,
		// Mirror every CoordinatorConfig field the user can set in
		// ~/.config/ntm/config.toml so `coordinator status --json` can be
		// used to confirm whether a TOML override actually took effect at
		// runtime.
		"config": buildCoordinatorConfigView(coordCfg),
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func renderCoordinatorStatus(session string, agents map[string]*coordinator.AgentState, idleAgents []*coordinator.AgentState, coordCfg coordinator.CoordinatorConfig, runtime coordinatorRuntimeView) error {
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
	printConfigBool("  Assign only idle:    ", coordCfg.AssignOnlyIdle)
	printConfigBool("  Send digests:        ", coordCfg.SendDigests)
	printConfigBool("  Conflict notify:     ", coordCfg.ConflictNotify)
	printConfigBool("  Conflict negotiate:  ", coordCfg.ConflictNegotiate)
	fmt.Printf("  Poll interval:       %s\n", coordCfg.PollInterval)
	fmt.Printf("  Digest interval:     %s\n", coordCfg.DigestInterval)
	fmt.Printf("  Idle threshold:      %.0fs\n", coordCfg.IdleThreshold)
	fmt.Printf("  Human agent:         %s\n", coordCfg.HumanAgent)
	fmt.Println()

	fmt.Printf("  %sRuntime%s\n", "\033[1m", "\033[0m")
	fmt.Printf("  %s%s%s\n", "\033[2m", strings.Repeat("─", 60), "\033[0m")
	fmt.Printf("  Coordinator:         %s\n", runtime.Summary)
	if runtime.ConfigPath != "" {
		fmt.Printf("  Monitor config:      %s\n", runtime.ConfigPath)
	}
	if runtime.LastMaintenanceAt != nil {
		fmt.Printf("  Last maintenance:    %s\n", runtime.LastMaintenanceAt.Local().Format(time.RFC3339))
	}
	if runtime.MaintenanceError != "" {
		fmt.Printf("  Maintenance:         \033[31m%s\033[0m\n", runtime.MaintenanceError)
	}
	if runtime.Error != "" {
		fmt.Printf("  Error:               \033[31m%s\033[0m\n", runtime.Error)
	}
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

	projectKey, err := resolveCoordinatorProjectKey(cmd.Context(), session, res.Inferred)
	if err != nil {
		return err
	}
	if projectKey == "" {
		return fmt.Errorf("getting project root failed")
	}

	mailClient := newAgentMailClient(projectKey)
	runtimeConfig := loadCoordinatorRuntimeConfig()
	// Only an explicit --send needs to register a sender. Generating a
	// digest is observation and must not run assignment maintenance.
	identityClient := mailClient
	if !sendMail {
		identityClient = nil
	}
	coordName := resolveCoordinatorIdentity(cmd.Context(), identityClient, session, projectKey)
	coord := coordinator.New(session, projectKey, mailClient, coordName).WithConfig(runtimeConfig)

	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
	defer cancel()

	if err := coord.Observe(ctx); err != nil {
		return fmt.Errorf("observing coordinator: %w", err)
	}

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

type coordinatorRunOutput struct {
	Success     bool                           `json:"success"`
	Session     string                         `json:"session"`
	Timestamp   string                         `json:"timestamp"`
	Once        bool                           `json:"once"`
	AutoAssign  bool                           `json:"auto_assign"`
	Assignments []coordinator.AssignmentResult `json:"assignments"`
	// MonitorYielded reports that the session monitor was running the
	// coordinator and paused it for this command.
	MonitorYielded bool   `json:"monitor_yielded,omitempty"`
	ErrorCode      string `json:"error_code,omitempty"`
	Error          string `json:"error,omitempty"`
}

func newCoordinatorRunCmd() *cobra.Command {
	var once bool
	cmd := &cobra.Command{
		Use:   "run [session]",
		Short: "Run the session coordinator in the foreground until interrupted",
		Long: `Run continuous session observation, assignment cleanup and lease renewal,
configured digest delivery, and opt-in automatic assignment. Disabling new
automatic assignments still releases finished work and maintains active leases.
The command exits cleanly on SIGINT or SIGTERM.

Sessions started by 'ntm spawn' already run this coordinator inside their
session monitor (see 'ntm coordinator status'). This command takes precedence:
the monitor pauses its coordinator while this command runs and resumes when it
exits, so the session is never coordinated twice. Use it to coordinate a
session without a monitor, or with a different --config.

Use --once to execute exactly one fresh observation and maintenance cycle,
including new assignments when enabled.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCoordinatorRun(cmd, args, once)
		},
	}
	cmd.Flags().BoolVar(&once, "once", false, "Run exactly one fresh coordinator cycle")
	return cmd
}

func runCoordinatorRun(cmd *cobra.Command, args []string, once bool) error {
	var session string
	if len(args) > 0 {
		session = args[0]
	}
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}
	resolved, err := ResolveSession(session, cmd.OutOrStdout())
	if err != nil {
		return err
	}
	if resolved.Session == "" {
		return nil
	}
	resolved.ExplainIfInferred(cmd.ErrOrStderr())
	session = resolved.Session
	projectKey, err := resolveCoordinatorProjectKey(cmd.Context(), session, resolved.Inferred)
	if err != nil {
		return err
	}
	if projectKey == "" {
		return errors.New("getting project root failed")
	}
	if err := configureAuthoritativeAssignmentPolicy(projectKey); err != nil {
		return err
	}
	// A session monitor hosting the coordinator stops it before this command
	// coordinates, and cannot restart it until the claim is released.
	claim, err := resilience.ClaimSessionCoordinator(cmd.Context(), session)
	if err != nil {
		return fmt.Errorf("claim session coordination: %w", err)
	}
	defer claim.Release()
	if claim.Preempted && !jsonOutput {
		fmt.Fprintf(cmd.ErrOrStderr(), "Paused the session monitor's coordinator for %s; it resumes when this command exits.\n", session)
	}

	runtimeConfig, ntmConfig := loadCoordinatorRuntimeConfigWithNTM()
	mailClient := newAgentMailClient(projectKey)
	coordName := resolveCoordinatorIdentity(cmd.Context(), mailClient, session, projectKey)
	coord := coordinator.New(session, projectKey, mailClient, coordName).
		WithConfig(runtimeConfig).
		WithNTMConfig(ntmConfig).
		WithPromptContext(resolvePromptContextOptions(false, false, false, ntmConfig))
	// The coordinator's actuation records (rotation enqueued, mail nudges,
	// account failover) must land in the durable attention feed that other
	// processes read, not this process's in-memory default (bd-viwo4).
	if closeAttention, err := openDurableAttentionFeed(); err != nil {
		slog.Default().Debug("durable attention feed unavailable", "session", session, "error", err)
	} else {
		defer closeAttention()
	}
	if once {
		assignments, cycleErr := coord.RunCycle(cmd.Context())
		runErr := coordinatorRunFailure(assignments, cycleErr)
		output := coordinatorRunOutput{
			Success: runErr == nil, Session: session, Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
			Once: true, AutoAssign: runtimeConfig.AutoAssign, Assignments: assignments, MonitorYielded: claim.Preempted,
		}
		if runErr != nil {
			output.ErrorCode = "ASSIGNMENT_FAILED"
			if cycleErr != nil {
				output.ErrorCode = "COORDINATOR_CYCLE_FAILED"
			}
			output.Error = runErr.Error()
		}
		if output.Assignments == nil {
			output.Assignments = []coordinator.AssignmentResult{}
		}
		if jsonOutput {
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(output); err != nil {
				return err
			}
			if runErr != nil {
				return jsonFailureExit()
			}
			return nil
		}
		if runErr != nil {
			return runErr
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Coordinator cycle complete for %s: %d assignment result(s)\n", session, len(assignments))
		return nil
	}

	if err := coord.Start(cmd.Context()); err != nil {
		return fmt.Errorf("starting coordinator: %w", err)
	}
	defer coord.Stop()
	if jsonOutput {
		output := coordinatorRunOutput{
			Success: true, Session: session, Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
			Once: false, AutoAssign: runtimeConfig.AutoAssign, Assignments: []coordinator.AssignmentResult{},
			MonitorYielded: claim.Preempted,
		}
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(output); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Coordinator running for %s (auto-assign=%t); press Ctrl-C to stop\n", session, runtimeConfig.AutoAssign)
	}
	<-cmd.Context().Done()
	return nil
}

func coordinatorRunFailure(assignments []coordinator.AssignmentResult, cycleErr error) error {
	if cycleErr != nil {
		return cycleErr
	}
	failed := 0
	for _, result := range assignments {
		if !result.Success {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d coordinator assignment attempt(s) failed", failed)
	}
	return nil
}

func resolveCoordinatorProjectKey(ctx context.Context, session string, inferred bool) (string, error) {
	session = strings.TrimSpace(session)
	if session == "" {
		return "", errors.New("session is required")
	}
	if coordinatorSessionExists(session) {
		panes, err := coordinatorGetPanes(session)
		if err != nil {
			return "", fmt.Errorf("resolve live coordinator panes for %s: %w", session, err)
		}
		if len(panes) == 0 {
			return "", fmt.Errorf("resolve live coordinator project for %s: session has no panes", session)
		}
		projectKey, err := robot.ResolveLiveSessionProject(session, panes, coordinatorPaneCurrentDir)
		if err != nil {
			return "", err
		}
		return projectKey, nil
	}

	var projectKey string
	if inferred {
		projectKey = resolveProjectDirForSession(session, false)
	} else {
		var err error
		projectKey, err = resolveExplicitProjectDirForSessionContext(ctx, session)
		if err != nil {
			return "", err
		}
	}

	projectKey = refineAgentMailProjectKey(session, projectKey)
	if strings.TrimSpace(projectKey) == "" {
		return "", errors.New("getting project root failed")
	}
	return projectKey, nil
}

func loadCoordinatorRuntimeConfig() coordinator.CoordinatorConfig {
	coordConfig, _ := loadCoordinatorRuntimeConfigWithNTM()
	return coordConfig
}

// loadCoordinatorRuntimeConfigWithNTM also returns the loaded NTM config so
// callers that run the coordinator loop can hand it to subsystems needing more
// than CoordinatorConfig (the context rotation trigger, bd-rpmg8). The
// [rotation] usage_percent_threshold / auto_confirm keys are mapped onto the
// runtime config here; with no [rotation] section both stay at their zero
// values and the trigger remains fully off.
func loadCoordinatorRuntimeConfigWithNTM() (coordinator.CoordinatorConfig, *config.Config) {
	coordConfig, ntmConfig, err := loadCoordinatorRuntimeConfigFrom(selectedConfigPath())
	if err != nil {
		// Falling back to defaults silently would also silently drop
		// persisted flags like [coordinator] conflict_negotiate — say so.
		fmt.Fprintf(os.Stderr,
			"Warning: could not load config (%v); coordinator running with DEFAULTS — persisted [coordinator] settings (e.g. conflict_negotiate) are NOT in effect\n", err)
	}
	return coordConfig, ntmConfig
}

// loadCoordinatorRuntimeConfigFrom loads path and maps it onto the runtime
// coordinator configuration. On error it returns the built-in defaults with
// the error, so a resident caller can keep its last good settings instead.
func loadCoordinatorRuntimeConfigFrom(path string) (coordinator.CoordinatorConfig, *config.Config, error) {
	coordConfig := coordinator.DefaultCoordinatorConfig()
	loaded, err := config.Load(path)
	if err != nil {
		return coordConfig, nil, err
	}
	if loaded == nil {
		return coordConfig, nil, nil
	}
	coordConfig = coordinatorConfigFromTOML(loaded.Coordinator, coordConfig)
	coordConfig.RotationUsageThreshold = loaded.Rotation.UsagePercentThreshold
	coordConfig.RotationAutoConfirm = loaded.Rotation.AutoConfirm
	return coordConfig, loaded, nil
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

	projectKey, err := resolveCoordinatorProjectKey(cmd.Context(), session, res.Inferred)
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
	cmd.Flags().StringVar(&assignPane, "pane", "", "Assign bead directly to exactly one N, W.P, or %N pane selector (requires --beads)")
	cmd.Flags().BoolVar(&assignForce, "force", false, "Force assignment even if pane is busy")
	cmd.Flags().BoolVar(&assignIgnoreDeps, "ignore-deps", false, "Ignore dependency checks for assignment")
	cmd.Flags().StringVar(&assignPrompt, "prompt", "", "Custom prompt for direct assignment")
	registerAssignPromptContextFlags(cmd)

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
	projectDir, err := resolveCoordinatorProjectKey(cmd.Context(), session, res.Inferred)
	if err != nil {
		return err
	}
	if err := configureAuthoritativeAssignmentPolicy(projectDir); err != nil {
		return err
	}

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
		ProjectDir:      projectDir,
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
		PaneSelector:    assignPane,
		Force:           assignForce,
		IgnoreDeps:      assignIgnoreDeps,
		Prompt:          assignPrompt,
		WithCASS:        assignWithCASS,
		NoCASS:          assignNoCASS,
		WithMemory:      assignWithMemory,
		policyProject:   filepath.Clean(projectDir),
	}

	if strings.TrimSpace(assignPane) != "" {
		return runDirectPaneAssignment(cmd.Context(), assignOpts)
	}

	if IsJSONOutput() {
		return runAssignJSON(cmd.Context(), assignOpts)
	}

	assignOutput, err := getAssignOutputEnhanced(cmd.Context(), assignOpts)
	if err != nil {
		return err
	}

	if !assignQuiet {
		displayAssignOutputEnhanced(assignOutput, assignVerbose)
	}

	if dryRun || len(assignOutput.Assignments) == 0 {
		return nil
	}

	return executeAssignmentsEnhanced(cmd.Context(), session, assignOutput, assignOpts)
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
  mail-nudge          - Prompt idle panes when they have unread Agent Mail

The flag is written to the [coordinator] section of the selected config file
(--config, or the global ~/.config/ntm/config.toml). Session monitors started
by 'ntm spawn' host the coordinator and apply the change within 15 seconds; a
foreground 'ntm coordinator run' reads config at startup, so restart it to
apply. conflict-notify is the exception: session monitors never run it, only
'ntm coordinator run' does.

Examples:
  ntm coordinator enable auto-assign
  ntm coordinator enable digest --interval=30m
  ntm coordinator enable conflict-notify
  ntm coordinator enable mail-nudge`,
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
  mail-nudge          - Unread Agent Mail prompts for idle panes

The flag is written to the [coordinator] section of the selected config file
(--config, or the global ~/.config/ntm/config.toml). Session monitors started
by 'ntm spawn' host the coordinator and apply the change within 15 seconds; a
foreground 'ntm coordinator run' reads config at startup, so restart it to
apply. conflict-notify is the exception: session monitors never run it, only
'ntm coordinator run' does. Disabling a feature never stops assignment lease maintenance.

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

// coordinatorFeatureKeys maps a toggle feature name to the TOML assignments it
// persists under [coordinator]. An error is returned for an unknown feature or
// an unparseable digest interval.
func coordinatorFeatureKeys(feature string, enable bool, interval string) ([][2]string, error) {
	enableTOML := strconv.FormatBool(enable)
	switch feature {
	case "auto-assign":
		if interval != "" {
			return nil, fmt.Errorf("--interval is only valid with the digest feature")
		}
		return [][2]string{{"auto_assign", enableTOML}}, nil
	case "digest":
		keys := [][2]string{{"send_digests", enableTOML}}
		if enable && interval != "" {
			duration, err := time.ParseDuration(interval)
			if err != nil {
				return nil, fmt.Errorf("invalid --interval %q: %w", interval, err)
			}
			if duration < coordinator.MinDigestInterval {
				return nil, fmt.Errorf("invalid --interval %q: must be at least %s", interval, coordinator.MinDigestInterval)
			}
			keys = append(keys, [2]string{"digest_interval", strconv.Quote(interval)})
		}
		return keys, nil
	case "conflict-notify":
		if interval != "" {
			return nil, fmt.Errorf("--interval is only valid with the digest feature")
		}
		return [][2]string{{"conflict_notify", enableTOML}}, nil
	case "conflict-negotiate":
		if interval != "" {
			return nil, fmt.Errorf("--interval is only valid with the digest feature")
		}
		return [][2]string{{"conflict_negotiate", enableTOML}}, nil
	case "mail-nudge":
		if interval != "" {
			return nil, fmt.Errorf("--interval is only valid with the digest feature")
		}
		return [][2]string{{"mail_nudge", enableTOML}}, nil
	default:
		return nil, fmt.Errorf("unknown feature '%s'. Valid features: auto-assign, digest, conflict-notify, conflict-negotiate, mail-nudge", feature)
	}
}

// coordinatorToggleConfigPath resolves the config file a coordinator toggle
// writes to: the explicitly selected --config file when given, otherwise the
// default global config path.
func coordinatorToggleConfigPath() string {
	if path := strings.TrimSpace(selectedConfigPath()); path != "" {
		return path
	}
	return config.DefaultPath()
}

// runCoordinatorToggle persists the feature flag into the [coordinator]
// section of the selected config file. A command named `enable` must actually
// enable: it writes the flag (preserving the rest of the file, comments
// included) instead of only printing a hint (#223).
func runCoordinatorToggle(cmd *cobra.Command, args []string, enable bool, interval string) error {
	feature := args[0]

	keys, err := coordinatorFeatureKeys(feature, enable, interval)
	if err != nil {
		return markCLIInvalidInput(err)
	}

	configPath := coordinatorToggleConfigPath()
	if err := config.PersistTOMLKeys(configPath, "coordinator", keys); err != nil {
		return fmt.Errorf("persisting coordinator.%s: %w", strings.ReplaceAll(feature, "-", "_"), err)
	}

	action := "disabled"
	if enable {
		action = "enabled"
	}

	if jsonOutput {
		written := make(map[string]string, len(keys))
		for _, kv := range keys {
			written[kv[0]] = kv[1]
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
			"feature":     feature,
			"enabled":     enable,
			"persisted":   true,
			"config_path": configPath,
			"written":     written,
		})
	}

	status := "\033[31m✗ " + action + "\033[0m"
	if enable {
		status = "\033[32m✓ " + action + "\033[0m"
	}

	fmt.Printf("Coordinator feature '%s': %s\n\n", feature, status)
	fmt.Printf("Persisted to %s:\n\n", configPath)
	fmt.Printf("  [coordinator]\n")
	for _, kv := range keys {
		fmt.Printf("  %s = %s\n", kv[0], kv[1])
	}
	fmt.Println()
	if feature == "conflict-notify" {
		fmt.Println("Session monitors never run conflict-notify; it runs only under `ntm coordinator run` (restart a running one to apply it).")
	} else {
		fmt.Println("Session monitors apply this within 15 seconds; restart a foreground `ntm coordinator run` to apply it there.")
	}

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
		MailNudge:         toml.MailNudge,
		NudgeCooldown:     time.Duration(toml.NudgeCooldownSeconds) * time.Second,
		NudgeMessage:      toml.NudgeMessage,
	}
	if out.NudgeCooldown <= 0 {
		out.NudgeCooldown = fallback.NudgeCooldown
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
