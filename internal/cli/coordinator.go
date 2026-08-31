package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	ntmgit "github.com/Dicklesworthstone/ntm/internal/git"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

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
	cmd.AddCommand(newCoordinatorBootstrapCmd())
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

type coordinatorBootstrapOptions struct {
	MailProjectKey  string
	CoordinatorName string
	TokenFile       string
	Bindings        []string
	DryRun          bool
}

type coordinatorBootstrapResult struct {
	Success         bool              `json:"success"`
	DryRun          bool              `json:"dry_run"`
	Session         string            `json:"session"`
	ProjectDir      string            `json:"project_dir"`
	MailProjectKey  string            `json:"mail_project_key"`
	CoordinatorName string            `json:"coordinator_name"`
	PaneBindings    map[string]string `json:"pane_bindings"`
	WouldCreate     bool              `json:"would_create_coordinator,omitempty"`
	Persisted       bool              `json:"persisted"`
}

func newCoordinatorBootstrapCmd() *cobra.Command {
	var opts coordinatorBootstrapOptions
	cmd := &cobra.Command{
		Use:   "bootstrap <session>",
		Short: "Explicitly persist the coordinator sender and live pane identities",
		Long: `Bootstrap is the only coordinator command allowed to create its Agent Mail
sender. It requires an explicit canonical mail project key, exact coordinator
name, and exact live pane-to-agent bindings. Dispatch commands never register
or invent identities. Use --dry-run to validate without creating or writing.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCoordinatorBootstrap(cmd, args[0], opts)
		},
	}
	cmd.Flags().StringVar(&opts.MailProjectKey, "mail-project-key", "", "Canonical Agent Mail project key (required)")
	cmd.Flags().StringVar(&opts.CoordinatorName, "coordinator-name", "", "Exact durable coordinator Agent Mail name (required)")
	cmd.Flags().StringVar(&opts.TokenFile, "registration-token-file", "", "Absolute mode-0600 file containing an existing coordinator token")
	cmd.Flags().StringArrayVar(&opts.Bindings, "bind", nil, "Exact live pane binding PANE_ID=AGENT_NAME (repeatable, required)")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Validate only; do not create an identity or persist state")
	_ = cmd.MarkFlagRequired("mail-project-key")
	_ = cmd.MarkFlagRequired("coordinator-name")
	_ = cmd.MarkFlagRequired("bind")
	return cmd
}

func runCoordinatorBootstrap(cmd *cobra.Command, requestedSession string, opts coordinatorBootstrapOptions) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}
	resolved, err := ResolveSession(requestedSession, cmd.OutOrStdout())
	if err != nil {
		return err
	}
	if resolved.Session == "" {
		return errors.New("session is required")
	}
	session := resolved.Session
	projectDir, err := resolveCoordinatorProjectKey(cmd.Context(), session, resolved.Inferred)
	if err != nil {
		return err
	}
	mailProjectKey := filepath.Clean(strings.TrimSpace(opts.MailProjectKey))
	if !filepath.IsAbs(mailProjectKey) {
		return markCLIInvalidInput(fmt.Errorf("--mail-project-key must be an absolute canonical key"))
	}
	coordinatorName := strings.TrimSpace(opts.CoordinatorName)
	if coordinatorName == "" {
		return markCLIInvalidInput(fmt.Errorf("--coordinator-name is required"))
	}

	panes, err := coordinatorGetPanes(session)
	if err != nil {
		return fmt.Errorf("getting coordinator panes: %w", err)
	}
	bindings, publicBindings, err := validateCoordinatorBootstrapBindings(cmd.Context(), projectDir, panes, opts.Bindings)
	if err != nil {
		return markCLIInvalidInput(err)
	}

	client := newAgentMailClient(mailProjectKey)
	roster, err := client.ListAgents(cmd.Context(), mailProjectKey)
	if err != nil {
		return fmt.Errorf("listing Agent Mail roster for %s: %w", mailProjectKey, err)
	}
	rosterByName := make(map[string]agentmail.Agent, len(roster))
	for _, agent := range roster {
		rosterByName[agent.Name] = agent
	}
	for paneID, binding := range bindings {
		if _, ok := rosterByName[binding.AgentName]; !ok {
			return fmt.Errorf("pane %s identity %q is absent from Agent Mail project %s; bootstrap never mints pane identities", paneID, binding.AgentName, mailProjectKey)
		}
	}

	existing, err := agentmail.LoadCoordinatorSessionState(session, projectDir)
	if err != nil {
		return err
	}
	registrationToken := ""
	if existing != nil {
		if existing.MailProjectKey != mailProjectKey || existing.Coordinator.AgentName != coordinatorName {
			return fmt.Errorf("persisted coordinator identity is %s in %s; refusing to replace it with %s in %s", existing.Coordinator.AgentName, existing.MailProjectKey, coordinatorName, mailProjectKey)
		}
		registrationToken = existing.Coordinator.RegistrationToken
	}
	if opts.TokenFile != "" {
		importedToken, tokenErr := readCoordinatorRegistrationToken(opts.TokenFile)
		if tokenErr != nil {
			return tokenErr
		}
		if registrationToken != "" && registrationToken != importedToken {
			return fmt.Errorf("token file does not match the persisted coordinator token")
		}
		registrationToken = importedToken
	}
	_, coordinatorExists := rosterByName[coordinatorName]
	wouldCreate := !coordinatorExists && existing == nil && registrationToken == ""
	if coordinatorExists && registrationToken == "" {
		return fmt.Errorf("coordinator name %q already exists but no registration token was supplied; refusing to mint a duplicate", coordinatorName)
	}
	if !coordinatorExists && registrationToken != "" {
		return fmt.Errorf("registration token supplied for absent coordinator %q", coordinatorName)
	}

	result := coordinatorBootstrapResult{
		Success: true, DryRun: opts.DryRun, Session: session, ProjectDir: projectDir,
		MailProjectKey: mailProjectKey, CoordinatorName: coordinatorName,
		PaneBindings: publicBindings, WouldCreate: wouldCreate,
	}
	if opts.DryRun {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}

	client.SetRegistrationToken(mailProjectKey, coordinatorName, registrationToken)
	registered, err := client.RegisterAgent(cmd.Context(), agentmail.RegisterAgentOptions{
		ProjectKey: mailProjectKey, Program: "ntm", Model: "coordinator", Name: coordinatorName,
		TaskDescription: fmt.Sprintf("NTM session coordinator for %s", session),
	})
	if err != nil {
		return fmt.Errorf("registering exact coordinator identity %q: %w", coordinatorName, err)
	}
	if registered.Name != coordinatorName {
		return fmt.Errorf("Agent Mail returned coordinator %q, want exact name %q", registered.Name, coordinatorName)
	}
	if registered.Program != "" && registered.Program != "ntm" || registered.Model != "" && registered.Model != "coordinator" {
		return fmt.Errorf("Agent Mail coordinator identity has program=%q model=%q, want ntm/coordinator", registered.Program, registered.Model)
	}
	if registered.RegistrationToken != "" {
		registrationToken = registered.RegistrationToken
	}
	if registrationToken == "" {
		return fmt.Errorf("Agent Mail did not return a coordinator registration token")
	}

	state := &agentmail.CoordinatorSessionState{
		Version: 1, SessionName: session, ProjectDir: projectDir, MailProjectKey: mailProjectKey,
		Coordinator: agentmail.CoordinatorIdentity{
			AgentName: coordinatorName, RegistrationToken: registrationToken, Program: "ntm", Model: "coordinator",
		},
		PaneBindings: bindings,
	}
	if existing != nil {
		state.CreatedAt = existing.CreatedAt
	}
	if err := agentmail.SaveCoordinatorSessionState(state); err != nil {
		return err
	}
	result.Persisted = true
	return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
}

func validateCoordinatorBootstrapBindings(ctx context.Context, projectDir string, panes []tmux.Pane, specs []string) (map[string]agentmail.CoordinatorPaneBinding, map[string]string, error) {
	live := make(map[string]tmux.Pane, len(panes))
	for _, pane := range panes {
		live[pane.ID] = pane
	}
	bindings := make(map[string]agentmail.CoordinatorPaneBinding, len(specs))
	public := make(map[string]string, len(specs))
	seenNames := make(map[string]string, len(specs))
	for _, spec := range specs {
		paneID, agentName, ok := strings.Cut(spec, "=")
		paneID, agentName = strings.TrimSpace(paneID), strings.TrimSpace(agentName)
		if !ok || paneID == "" || agentName == "" {
			return nil, nil, fmt.Errorf("invalid --bind %q; want PANE_ID=AGENT_NAME", spec)
		}
		pane, ok := live[paneID]
		if !ok || pane.PID <= 0 {
			return nil, nil, fmt.Errorf("bound pane %s is not live with a stable pid", paneID)
		}
		if previous, duplicate := seenNames[agentName]; duplicate && previous != paneID {
			return nil, nil, fmt.Errorf("agent %q is bound to both %s and %s", agentName, previous, paneID)
		}
		paneDir, err := coordinatorPaneCurrentDir(paneID)
		if err != nil {
			return nil, nil, fmt.Errorf("reading project for pane %s: %w", paneID, err)
		}
		matches, err := coordinatorProjectDirsMatch(ctx, projectDir, paneDir)
		if err != nil {
			return nil, nil, err
		}
		if !matches {
			return nil, nil, fmt.Errorf("pane %s is in cross-project directory %q; refusing binding", paneID, strings.TrimSpace(paneDir))
		}
		bindings[paneID] = agentmail.CoordinatorPaneBinding{
			AgentName: agentName, PanePID: pane.PID, ProjectDir: filepath.Clean(projectDir), ImportedAt: time.Now().UTC(),
		}
		public[paneID] = agentName
		seenNames[agentName] = paneID
	}
	if len(bindings) == 0 {
		return nil, nil, fmt.Errorf("at least one --bind is required")
	}
	return bindings, public, nil
}

func readCoordinatorRegistrationToken(path string) (string, error) {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		return "", markCLIInvalidInput(fmt.Errorf("--registration-token-file must be absolute"))
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("reading coordinator token file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("coordinator token file must be a mode-0600 regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading coordinator token file: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("coordinator token file is empty")
	}
	return token, nil
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
	runtimeConfig := coordConfig
	runtimeConfig.AutoAssign = false
	runtimeConfig.SendDigests = false
	// Status is a read-only surface: never let its brief coordinator run
	// enqueue context rotations (bd-rpmg8).
	runtimeConfig.RotationUsageThreshold = 0

	// Create coordinator to get status without enabling configured side effects.
	coord, _, err := loadBootstrappedCoordinator(session, projectKey)
	if err != nil {
		return err
	}
	coord.WithConfig(runtimeConfig)

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
		// Mirror every CoordinatorConfig field the user can set in
		// ~/.config/ntm/config.toml so `coordinator status --json` can be
		// used to confirm whether a TOML override actually took effect at
		// runtime. Pre-fix this map omitted assign_only_idle and human_agent,
		// which meant a user toggling those keys in config.toml had no way
		// to verify the change was applied — the symptom #111 was filed for.
		"config": map[string]interface{}{
			"auto_assign":        coordCfg.AutoAssign,
			"assign_only_idle":   coordCfg.AssignOnlyIdle,
			"send_digests":       coordCfg.SendDigests,
			"conflict_notify":    coordCfg.ConflictNotify,
			"conflict_negotiate": coordCfg.ConflictNegotiate,
			"poll_interval":      coordCfg.PollInterval.String(),
			"digest_interval":    coordCfg.DigestInterval.String(),
			"idle_threshold":     coordCfg.IdleThreshold,
			"human_agent":        coordCfg.HumanAgent,
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
	printConfigBool("  Assign only idle:    ", coordCfg.AssignOnlyIdle)
	printConfigBool("  Send digests:        ", coordCfg.SendDigests)
	printConfigBool("  Conflict notify:     ", coordCfg.ConflictNotify)
	printConfigBool("  Conflict negotiate:  ", coordCfg.ConflictNegotiate)
	fmt.Printf("  Poll interval:       %s\n", coordCfg.PollInterval)
	fmt.Printf("  Digest interval:     %s\n", coordCfg.DigestInterval)
	fmt.Printf("  Idle threshold:      %.0fs\n", coordCfg.IdleThreshold)
	fmt.Printf("  Human agent:         %s\n", coordCfg.HumanAgent)
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

	runtimeConfig := loadCoordinatorRuntimeConfig()
	runtimeConfig.AutoAssign = false
	runtimeConfig.SendDigests = false
	// Digest is a read-only surface: never let its brief coordinator run
	// enqueue context rotations (bd-rpmg8).
	runtimeConfig.RotationUsageThreshold = 0
	coord, _, err := loadBootstrappedCoordinator(session, projectKey)
	if err != nil {
		return err
	}
	coord.WithConfig(runtimeConfig)

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

type coordinatorRunOutput struct {
	Success     bool                           `json:"success"`
	Session     string                         `json:"session"`
	Timestamp   string                         `json:"timestamp"`
	Once        bool                           `json:"once"`
	AutoAssign  bool                           `json:"auto_assign"`
	Assignments []coordinator.AssignmentResult `json:"assignments"`
	ErrorCode   string                         `json:"error_code,omitempty"`
	Error       string                         `json:"error,omitempty"`
}

func newCoordinatorRunCmd() *cobra.Command {
	var once bool
	cmd := &cobra.Command{
		Use:   "run [session]",
		Short: "Run the session coordinator until interrupted",
		Long: `Run continuous session observation, configured digest delivery, and
opt-in automatic assignment. The command exits cleanly on SIGINT or SIGTERM.

Use --once to execute exactly one fresh observation and assignment cycle.`,
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

	runtimeConfig, ntmConfig := loadCoordinatorRuntimeConfigWithNTM()
	coord, _, err := loadBootstrappedCoordinator(session, projectKey)
	if err != nil {
		return err
	}
	coord.WithConfig(runtimeConfig).WithNTMConfig(ntmConfig)
	if once {
		assignments, cycleErr := coord.RunCycle(cmd.Context())
		runErr := coordinatorRunFailure(assignments, cycleErr)
		output := coordinatorRunOutput{
			Success: runErr == nil, Session: session, Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
			Once: true, AutoAssign: runtimeConfig.AutoAssign, Assignments: assignments,
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

func loadBootstrappedCoordinator(session, projectDir string) (*coordinator.SessionCoordinator, *agentmail.CoordinatorSessionState, error) {
	state, err := agentmail.LoadCoordinatorSessionState(session, projectDir)
	if err != nil {
		return nil, nil, err
	}
	if state == nil {
		return nil, nil, fmt.Errorf("coordinator is not bootstrapped for session %q and project %q; run `ntm coordinator bootstrap %s --mail-project-key <canonical-key> --coordinator-name <name> --bind <pane-id>=<agent-name>`", session, projectDir, session)
	}
	client := newAgentMailClient(state.MailProjectKey)
	client.SetRegistrationToken(state.MailProjectKey, state.Coordinator.AgentName, state.Coordinator.RegistrationToken)
	coord := coordinator.New(session, projectDir, client, state.Coordinator.AgentName).
		WithAgentMailScope(state.MailProjectKey, state.Coordinator.AgentName, state.PaneBindings)
	return coord, state, nil
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
		// A session may intentionally contain an unrelated utility pane. When
		// the configured session checkout is live in at least one pane, use it
		// as the coordinator target and let bootstrap bindings select the exact
		// in-project panes. Never let an unrelated pane redefine the project.
		if cfg != nil {
			configured := filepath.Clean(cfg.GetProjectDir(session))
			if util.ProjectDirScore(configured) > 0 {
				for _, pane := range panes {
					paneDir, pathErr := coordinatorPaneCurrentDir(pane.ID)
					if pathErr != nil {
						continue
					}
					if same, _ := coordinatorProjectDirsMatch(ctx, configured, paneDir); same {
						return configured, nil
					}
				}
			}
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

func coordinatorProjectDirsMatch(ctx context.Context, targetDir, candidateDir string) (bool, error) {
	targetRoot := filepath.Clean(util.ResolveProjectDir(strings.TrimSpace(targetDir)))
	candidateRoot := filepath.Clean(util.ResolveProjectDir(strings.TrimSpace(candidateDir)))
	if !filepath.IsAbs(targetRoot) || !filepath.IsAbs(candidateRoot) {
		return false, nil
	}
	if targetRoot == candidateRoot {
		return true, nil
	}
	targetCommon, targetErr := ntmgit.CommonDir(ctx, targetRoot)
	candidateCommon, candidateErr := ntmgit.CommonDir(ctx, candidateRoot)
	if targetErr != nil || candidateErr != nil {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		return false, nil
	}
	return targetCommon != "" && targetCommon == candidateCommon, nil
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
	coordConfig := coordinator.DefaultCoordinatorConfig()
	var ntmConfig *config.Config
	loaded, err := config.Load(selectedConfigPath())
	switch {
	case err != nil:
		// Falling back to defaults silently would also silently drop
		// persisted flags like [coordinator] conflict_negotiate — say so.
		fmt.Fprintf(os.Stderr,
			"Warning: could not load config (%v); coordinator running with DEFAULTS — persisted [coordinator] settings (e.g. conflict_negotiate) are NOT in effect\n", err)
	case loaded != nil:
		ntmConfig = loaded
		coordConfig = coordinatorConfigFromTOML(loaded.Coordinator, coordConfig)
		coordConfig.RotationUsageThreshold = loaded.Rotation.UsagePercentThreshold
		coordConfig.RotationAutoConfirm = loaded.Rotation.AutoConfirm
	}
	return coordConfig, ntmConfig
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

	_, state, err := loadBootstrappedCoordinator(session, projectKey)
	if err != nil {
		return err
	}
	mailClient := newAgentMailClient(state.MailProjectKey)
	mailClient.SetRegistrationToken(state.MailProjectKey, state.Coordinator.AgentName, state.Coordinator.RegistrationToken)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	detector := coordinator.NewConflictDetector(mailClient, state.MailProjectKey)
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
(--config, or the global ~/.config/ntm/config.toml). A running
'ntm coordinator run' daemon reads config at startup; restart it to apply.

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
(--config, or the global ~/.config/ntm/config.toml). A running
'ntm coordinator run' daemon reads config at startup; restart it to apply.

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
	fmt.Println("A running `ntm coordinator run` daemon reads config at startup; restart it to apply.")

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
