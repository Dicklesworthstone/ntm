package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/health"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/swarm"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func newSwarmCmd() *cobra.Command {
	var (
		scanDir         string
		projects        []string
		dryRun          bool
		remote          string
		jsonOutput      bool
		sessionsPerType int
		panesPerSession int
		outputPath      string
		autoRotate      bool
		initialPrompt   string
		promptFile      string
		waitReady       bool
		readyTimeout    int
	)

	cmd := &cobra.Command{
		Use:   "swarm",
		Short: "Orchestrate weighted multi-project agent swarm",
		Long: `Create and manage a weighted swarm of AI agents across multiple projects.

The swarm system allocates agents based on each project's open bead count:
  - Tier 1 (≥400 beads): Heavy allocation (e.g., 4 CC, 4 Codex, 2 Gemini)
  - Tier 2 (≥100 beads): Medium allocation (e.g., 3 CC, 3 Codex, 2 Gemini)
  - Tier 3 (<100 beads): Light allocation (e.g., 1 CC, 1 Codex, 1 Gemini)

Examples:
  ntm swarm                           # Scan /dp and create swarm
  ntm swarm --scan-dir=/projects      # Scan custom directory
  ntm swarm --dry-run                 # Preview plan without executing
  ntm swarm --projects=foo,bar        # Only include specific projects
  ntm swarm --remote=user@host        # Execute on remote host via SSH`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSwarm(cmd.Context(), swarmOptions{
				ScanDir:         scanDir,
				Projects:        projects,
				DryRun:          dryRun,
				Remote:          remote,
				JSONOutput:      jsonOutput,
				SessionsPerType: sessionsPerType,
				PanesPerSession: panesPerSession,
				OutputPath:      outputPath,
				AutoRotate:      autoRotate,
				InitialPrompt:   initialPrompt,
				PromptFile:      promptFile,
				WaitReady:       waitReady,
				ReadyTimeout:    readyTimeout,
			})
		},
	}

	// Set default scan dir from config or /dp
	defaultScanDir := "/dp"
	if cfg != nil && cfg.Swarm.DefaultScanDir != "" {
		defaultScanDir = cfg.Swarm.DefaultScanDir
	}
	defaultSessionsPerType := 3
	if cfg != nil && cfg.Swarm.SessionsPerType > 0 {
		defaultSessionsPerType = cfg.Swarm.SessionsPerType
	}
	defaultAutoRotate := false
	if cfg != nil {
		defaultAutoRotate = cfg.Swarm.AutoRotateAccounts
	}

	cmd.Flags().StringVar(&scanDir, "scan-dir", defaultScanDir, "Directory to scan for projects")
	cmd.Flags().StringSliceVar(&projects, "projects", nil, "Explicit list of project paths (comma-separated)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview plan without creating sessions")
	cmd.Flags().StringVar(&remote, "remote", "", "Remote host for SSH execution (user@host)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output plan as JSON")
	cmd.Flags().IntVar(&sessionsPerType, "sessions-per-type", defaultSessionsPerType, "Number of tmux sessions per agent type (default: 3)")
	cmd.Flags().IntVar(&panesPerSession, "panes-per-session", 0, "Max panes per session (0 = auto-calculate from total agents)")
	cmd.Flags().StringVar(&outputPath, "output", "", "Write swarm plan to JSON file (optional)")
	cmd.Flags().StringVar(&initialPrompt, "prompt", "", "Initial prompt to inject into all agents after launch")
	cmd.Flags().StringVar(&promptFile, "prompt-file", "", "File containing initial prompt (mutually exclusive with --prompt)")
	cmd.Flags().BoolVar(&waitReady, "wait-ready", false, "Wait for all agents to reach idle/ready state before returning")
	cmd.Flags().IntVar(&readyTimeout, "ready-timeout", 30, "Timeout in seconds for --wait-ready (default: 30)")
	cmd.PersistentFlags().BoolVar(&autoRotate, "auto-rotate-accounts", defaultAutoRotate, "Automatically rotate accounts on usage limit hit (requires caam)")

	// Add subcommands
	cmd.AddCommand(newSwarmPlanCmd())
	cmd.AddCommand(newSwarmStatusCmd())
	cmd.AddCommand(newSwarmStopCmd())

	return cmd
}

type swarmOptions struct {
	ScanDir         string
	Projects        []string
	DryRun          bool
	Remote          string
	JSONOutput      bool
	SessionsPerType int
	PanesPerSession int
	OutputPath      string
	AutoRotate      bool
	InitialPrompt   string
	PromptFile      string
	WaitReady       bool
	ReadyTimeout    int
}

// SwarmPlanOutput is the JSON output format for swarm plan
type SwarmPlanOutput struct {
	ScanDir         string             `json:"scan_dir"`
	TotalCC         int                `json:"total_cc"`
	TotalCod        int                `json:"total_cod"`
	TotalGmi        int                `json:"total_gmi"`
	TotalAgents     int                `json:"total_agents"`
	SessionsPerType int                `json:"sessions_per_type"`
	PanesPerSession int                `json:"panes_per_session"`
	Allocations     []AllocationOutput `json:"allocations"`
	Sessions        []SessionOutput    `json:"sessions"`
	DryRun          bool               `json:"dry_run"`
	Error           string             `json:"error,omitempty"`
}

type AllocationOutput struct {
	Project     string `json:"project"`
	Path        string `json:"path"`
	OpenBeads   int    `json:"open_beads"`
	Tier        int    `json:"tier"`
	CCAgents    int    `json:"cc_agents"`
	CodAgents   int    `json:"cod_agents"`
	GmiAgents   int    `json:"gmi_agents"`
	TotalAgents int    `json:"total_agents"`
}

type SessionOutput struct {
	Name      string       `json:"name"`
	AgentType string       `json:"agent_type"`
	PaneCount int          `json:"pane_count"`
	Panes     []PaneOutput `json:"panes"`
}

type PaneOutput struct {
	Index     int    `json:"index"`
	Project   string `json:"project"`
	AgentType string `json:"agent_type"`
}

func runSwarm(ctx context.Context, opts swarmOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	logger := slog.Default()

	initialPrompt, promptSource, promptPath, err := resolveSwarmInitialPrompt(opts.InitialPrompt, opts.PromptFile)
	if err != nil {
		return err
	}
	if promptSource == "file" {
		logger.Info("loaded initial prompt from file", "path", promptPath, "length", len(initialPrompt))
	}
	if initialPrompt != "" {
		logger.Info("initial prompt configured",
			"source", promptSource,
			"length", len(initialPrompt),
			"preview", truncate(initialPrompt, 50),
		)
	} else {
		logger.Debug("no initial prompt configured")
	}

	// Get swarm config
	swarmCfg := cfg.Swarm
	if !swarmCfg.Enabled && !opts.DryRun {
		return fmt.Errorf("swarm orchestration is disabled in config; set swarm.enabled=true or use --dry-run")
	}
	swarmCfg.AutoRotateAccounts = opts.AutoRotate
	logger.Info("account rotation configuration", "auto_rotate_accounts", swarmCfg.AutoRotateAccounts)

	if opts.SessionsPerType < 1 {
		return fmt.Errorf("--sessions-per-type must be at least 1, got %d", opts.SessionsPerType)
	}
	if opts.SessionsPerType > 10 {
		logger.Warn("high sessions-per-type may impact performance", "value", opts.SessionsPerType)
	}
	swarmCfg.SessionsPerType = opts.SessionsPerType

	if opts.PanesPerSession < 0 {
		return fmt.Errorf("--panes-per-session cannot be negative, got %d", opts.PanesPerSession)
	}
	if opts.PanesPerSession > 20 {
		logger.Warn("high panes-per-session may impact performance", "value", opts.PanesPerSession)
	}
	swarmCfg.PanesPerSession = opts.PanesPerSession
	if opts.PanesPerSession > 0 {
		logger.Info("session configuration", "sessions_per_type", opts.SessionsPerType, "panes_per_session", opts.PanesPerSession, "mode", "manual")
	} else {
		logger.Info("session configuration", "sessions_per_type", opts.SessionsPerType, "panes_per_session", "auto", "mode", "auto-calculate")
	}

	// Discover projects
	projects, err := discoverProjects(opts.ScanDir, opts.Projects)
	if err != nil {
		return fmt.Errorf("failed to discover projects: %w", err)
	}

	if len(projects) == 0 {
		if opts.JSONOutput {
			return printSwarmJSON(SwarmPlanOutput{
				ScanDir: opts.ScanDir,
				DryRun:  opts.DryRun,
				Error:   "no projects found",
			})
		}
		return fmt.Errorf("no projects found in %s", opts.ScanDir)
	}

	// Calculate allocations
	calc := swarm.NewAllocationCalculator(&swarmCfg)
	plan := calc.GenerateSwarmPlan(opts.ScanDir, projects)
	logger.Info("calculated panes per session",
		"sessions_per_type", plan.SessionsPerType,
		"panes_per_session", plan.PanesPerSession,
	)

	if opts.OutputPath != "" {
		if err := writePlanToFile(plan, opts.OutputPath); err != nil {
			return fmt.Errorf("write plan: %w", err)
		}
		logger.Info("swarm plan written", "path", opts.OutputPath)
	}

	// Build output
	out := buildSwarmPlanOutput(plan, opts.DryRun)

	if opts.JSONOutput {
		return printSwarmJSON(out)
	}

	// Pretty print plan
	printSwarmPlan(out)

	if opts.DryRun {
		output.PrintInfo("Dry run - no sessions created")
		return nil
	}

	staggerDelay := time.Duration(swarmCfg.StaggerDelayMs) * time.Millisecond
	if staggerDelay < 0 {
		staggerDelay = 0
	}

	// Create a tmux session orchestrator (local or remote).
	var sessOrch *swarm.SessionOrchestrator
	if opts.Remote != "" {
		sessOrch = swarm.NewRemoteSessionOrchestrator(opts.Remote)
		sessOrch.StaggerDelay = staggerDelay
		output.PrintInfof("Creating swarm on remote host: %s", opts.Remote)
	} else {
		sessOrch = swarm.NewSessionOrchestrator()
		sessOrch.StaggerDelay = staggerDelay
	}

	// Derive a concrete tmux client for follow-up actions.
	tmuxClient := sessOrch.TmuxClient
	if tmuxClient == nil {
		tmuxClient = tmux.DefaultClient
	}

	executor := &swarm.SwarmOrchestrator{
		SessionOrchestrator: sessOrch,
		PaneLauncher:        swarm.NewPaneLauncherWithClient(tmuxClient).WithLogger(logger),
		PromptInjector:      swarm.NewPromptInjectorWithClient(tmuxClient).WithLogger(logger),
		Logger:              logger,
		StaggerDelay:        staggerDelay,
	}

	execResult, err := executor.Execute(ctx, plan, initialPrompt)
	if err != nil {
		return err
	}

	// Report results
	if execResult.Sessions != nil {
		output.PrintSuccessf("Created %d sessions with %d/%d panes",
			len(execResult.Sessions.Sessions), execResult.Sessions.SuccessfulPanes, execResult.Sessions.TotalPanes)

		if execResult.Sessions.FailedPanes > 0 {
			output.PrintWarningf("%d panes failed to create", execResult.Sessions.FailedPanes)
			for _, err := range execResult.Sessions.Errors {
				fmt.Fprintf(os.Stderr, "  %v\n", err)
			}
		}
	}

	if execResult.Launch != nil {
		output.PrintSuccessf("Launched agents: %d succeeded, %d failed", execResult.Launch.Successful, execResult.Launch.Failed)
		if execResult.Launch.Failed > 0 {
			output.PrintWarningf("%d agents failed to launch (see logs)", execResult.Launch.Failed)
		}
	}

	if initialPrompt != "" && execResult.Injection != nil {
		output.PrintSuccessf("Injected initial prompt: %d succeeded, %d failed", execResult.Injection.Successful, execResult.Injection.Failed)
		if execResult.Injection.Failed > 0 {
			output.PrintWarningf("%d panes failed prompt injection (see logs)", execResult.Injection.Failed)
		}
	}

	// Phase 4 (optional): Wait for agents to reach idle/ready state.
	// This gates external callers (e.g., --robot-send) from sending prompts
	// before agents have fully initialized their TUIs.
	if opts.WaitReady && execResult.Sessions != nil {
		timeout := time.Duration(opts.ReadyTimeout) * time.Second
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		output.PrintInfof("Waiting for agents to reach ready state (timeout: %s)...", timeout)
		ready, total := waitForSwarmAgentsReady(ctx, plan, tmuxClient, timeout, logger)
		if ready == total {
			output.PrintSuccessf("All %d agents are ready", total)
		} else {
			output.PrintWarningf("%d/%d agents reached ready state (timeout reached)", ready, total)
		}
	}

	return nil
}

func resolveSwarmInitialPrompt(prompt, promptFile string) (resolved string, source string, path string, err error) {
	if prompt != "" && promptFile != "" {
		return "", "", "", fmt.Errorf("--prompt and --prompt-file are mutually exclusive")
	}
	if promptFile != "" {
		data, readErr := os.ReadFile(promptFile)
		if readErr != nil {
			return "", "", "", fmt.Errorf("read prompt file %q: %w", promptFile, readErr)
		}
		return string(data), "file", promptFile, nil
	}
	if prompt != "" {
		return prompt, "flag", "", nil
	}
	return "", "", "", nil
}

// discoverProjects finds projects with bead counts using BeadScanner
func discoverProjects(scanDir string, explicitProjects []string) ([]swarm.ProjectBeadCount, error) {
	var opts []swarm.BeadScannerOption

	if len(explicitProjects) > 0 {
		opts = append(opts, swarm.WithExplicitProjects(explicitProjects))
	}

	scanner := swarm.NewBeadScanner(scanDir, opts...)
	result, err := scanner.Scan(context.Background())
	if err != nil {
		return nil, fmt.Errorf("scan projects: %w", err)
	}

	return result.Projects, nil
}

func buildSwarmPlanOutput(plan *swarm.SwarmPlan, dryRun bool) SwarmPlanOutput {
	out := SwarmPlanOutput{
		ScanDir:         plan.ScanDir,
		TotalCC:         plan.TotalCC,
		TotalCod:        plan.TotalCod,
		TotalGmi:        plan.TotalGmi,
		TotalAgents:     plan.TotalAgents,
		SessionsPerType: plan.SessionsPerType,
		PanesPerSession: plan.PanesPerSession,
		Allocations:     make([]AllocationOutput, 0, len(plan.Allocations)),
		Sessions:        make([]SessionOutput, 0, len(plan.Sessions)),
		DryRun:          dryRun,
	}

	for _, alloc := range plan.Allocations {
		out.Allocations = append(out.Allocations, AllocationOutput{
			Project:     alloc.Project.Name,
			Path:        alloc.Project.Path,
			OpenBeads:   alloc.Project.OpenBeads,
			Tier:        alloc.Project.Tier,
			CCAgents:    alloc.CCAgents,
			CodAgents:   alloc.CodAgents,
			GmiAgents:   alloc.GmiAgents,
			TotalAgents: alloc.TotalAgents,
		})
	}

	for _, sess := range plan.Sessions {
		sessOut := SessionOutput{
			Name:      sess.Name,
			AgentType: sess.AgentType,
			PaneCount: sess.PaneCount,
			Panes:     make([]PaneOutput, 0, len(sess.Panes)),
		}
		for _, pane := range sess.Panes {
			sessOut.Panes = append(sessOut.Panes, PaneOutput{
				Index:     pane.Index,
				Project:   pane.Project,
				AgentType: pane.AgentType,
			})
		}
		out.Sessions = append(out.Sessions, sessOut)
	}

	return out
}

func printSwarmPlan(out SwarmPlanOutput) {
	printSwarmHeader("Swarm Plan")
	fmt.Printf("  Scan Directory: %s\n", out.ScanDir)
	fmt.Printf("  Total Agents:   %d (CC: %d, Codex: %d, Gemini: %d)\n",
		out.TotalAgents, out.TotalCC, out.TotalCod, out.TotalGmi)
	fmt.Printf("  Sessions:       %d per type, %d panes max each\n",
		out.SessionsPerType, out.PanesPerSession)
	fmt.Println()

	printSwarmHeader("Project Allocations")
	for _, alloc := range out.Allocations {
		tierStr := fmt.Sprintf("T%d", alloc.Tier)
		fmt.Printf("  %-20s [%s] %d beads → CC:%d Cod:%d Gmi:%d\n",
			alloc.Project, tierStr, alloc.OpenBeads,
			alloc.CCAgents, alloc.CodAgents, alloc.GmiAgents)
	}
	fmt.Println()

	printSwarmHeader("Sessions")
	for _, sess := range out.Sessions {
		fmt.Printf("  %s (%d panes)\n", sess.Name, sess.PaneCount)
	}
}

func printSwarmHeader(title string) {
	fmt.Printf("\n\033[1m%s\033[0m\n", title)
}

func printSwarmJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writePlanToFile(plan *swarm.SwarmPlan, path string) error {
	if plan == nil {
		return fmt.Errorf("plan cannot be nil")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal plan: %w", err)
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return nil
}

// Subcommand: swarm plan
func newSwarmPlanCmd() *cobra.Command {
	var (
		scanDir  string
		projects []string
	)

	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Preview swarm allocation plan without executing",
		RunE: func(cmd *cobra.Command, args []string) error {
			autoRotate, err := cmd.Flags().GetBool("auto-rotate-accounts")
			if err != nil {
				return err
			}
			return runSwarm(cmd.Context(), swarmOptions{
				ScanDir:    scanDir,
				Projects:   projects,
				DryRun:     true,
				JSONOutput: jsonOutput,
				AutoRotate: autoRotate,
			})
		},
	}

	defaultScanDir := "/dp"
	if cfg != nil && cfg.Swarm.DefaultScanDir != "" {
		defaultScanDir = cfg.Swarm.DefaultScanDir
	}

	cmd.Flags().StringVar(&scanDir, "scan-dir", defaultScanDir, "Directory to scan for projects")
	cmd.Flags().StringSliceVar(&projects, "projects", nil, "Explicit list of project paths")

	return cmd
}

// Subcommand: swarm status
func newSwarmStatusCmd() *cobra.Command {
	swarmSessionRE := regexp.MustCompile(`^(cc|cod|gmi)_agents_[0-9]+$`)

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show current swarm status",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := tmux.EnsureInstalled(); err != nil {
				return err
			}

			sessions, err := tmux.ListSessions()
			if err != nil {
				return err
			}

			var swarmSessions []string
			for _, sess := range sessions {
				if swarmSessionRE.MatchString(sess.Name) {
					swarmSessions = append(swarmSessions, sess.Name)
				}
			}
			sort.Strings(swarmSessions)

			if len(swarmSessions) == 0 {
				output.PrintInfo("No swarm sessions found")
				return nil
			}

			type swarmSessionStatus struct {
				Session string                `json:"session"`
				Health  *health.SessionHealth `json:"health,omitempty"`
				Error   string                `json:"error,omitempty"`
			}

			type swarmStatusOutput struct {
				CheckedAt     time.Time            `json:"checked_at"`
				Sessions      []swarmSessionStatus `json:"sessions"`
				Summary       health.HealthSummary `json:"summary"`
				OverallStatus health.Status        `json:"overall_status"`
			}

			out := swarmStatusOutput{
				CheckedAt:     time.Now().UTC(),
				Sessions:      make([]swarmSessionStatus, 0, len(swarmSessions)),
				Summary:       health.HealthSummary{},
				OverallStatus: health.StatusOK,
			}

			statusSeverity := func(s health.Status) int {
				switch s {
				case health.StatusError:
					return 3
				case health.StatusWarning:
					return 2
				case health.StatusOK:
					return 1
				default:
					return 0
				}
			}

			for _, name := range swarmSessions {
				ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
				sessionHealth, err := health.CheckSession(ctx, name)
				cancel()
				entry := swarmSessionStatus{Session: name}
				if err != nil {
					entry.Error = err.Error()
					out.Sessions = append(out.Sessions, entry)
					continue
				}

				entry.Health = sessionHealth
				out.Sessions = append(out.Sessions, entry)

				out.Summary.Total += sessionHealth.Summary.Total
				out.Summary.Healthy += sessionHealth.Summary.Healthy
				out.Summary.Warning += sessionHealth.Summary.Warning
				out.Summary.Error += sessionHealth.Summary.Error
				out.Summary.Unknown += sessionHealth.Summary.Unknown

				if statusSeverity(sessionHealth.OverallStatus) > statusSeverity(out.OverallStatus) {
					out.OverallStatus = sessionHealth.OverallStatus
				}
			}

			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			output.PrintInfof("Swarm sessions: %d", len(out.Sessions))
			for _, sess := range out.Sessions {
				if sess.Health == nil {
					output.PrintWarningf("  %s: error (%s)", sess.Session, sess.Error)
					continue
				}
				output.PrintInfof("  %s: %s (ok:%d warn:%d err:%d unk:%d)",
					sess.Session,
					sess.Health.OverallStatus,
					sess.Health.Summary.Healthy,
					sess.Health.Summary.Warning,
					sess.Health.Summary.Error,
					sess.Health.Summary.Unknown,
				)
			}
			output.PrintInfof("Overall: %s (total:%d ok:%d warn:%d err:%d unk:%d)",
				out.OverallStatus,
				out.Summary.Total,
				out.Summary.Healthy,
				out.Summary.Warning,
				out.Summary.Error,
				out.Summary.Unknown,
			)
			return nil
		},
	}
	return cmd
}

// Subcommand: swarm stop
func newSwarmStopCmd() *cobra.Command {
	var (
		force           bool
		timeout         time.Duration
		sessionPatterns []string
		jsonOutput      bool
	)

	cmd := &cobra.Command{
		Use:   "stop [session-pattern...]",
		Short: "Stop the swarm and destroy all sessions",
		Long: `Gracefully stop swarm agent sessions.

By default, discovers and stops all swarm sessions (cc_agents_*, cod_agents_*, gmi_agents_*).
Optionally specify session name patterns to stop specific sessions.

The graceful shutdown process:
  1. Send exit signals to all agents (Ctrl+C for Claude, /exit for Codex, etc.)
  2. Wait for graceful timeout to allow agents to exit cleanly
  3. Destroy all tmux sessions

Examples:
  ntm swarm stop                    # Stop all swarm sessions gracefully
  ntm swarm stop --force            # Immediately destroy without graceful exit
  ntm swarm stop cc_agents_*        # Stop only Claude Code sessions
  ntm swarm stop --timeout=10s      # Wait 10s for graceful exit`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			// Determine which sessions to stop
			patterns := sessionPatterns
			if len(args) > 0 {
				patterns = args
			}

			// If no patterns specified, use default swarm session patterns
			if len(patterns) == 0 {
				patterns = []string{"cc_agents_*", "cod_agents_*", "gmi_agents_*"}
			}

			// Discover matching sessions
			sessions, err := discoverSwarmSessions(patterns)
			if err != nil {
				return fmt.Errorf("discovering sessions: %w", err)
			}

			if len(sessions) == 0 {
				output.PrintInfo("No swarm sessions found matching the specified patterns")
				return nil
			}

			output.PrintInfof("Found %d swarm session(s) to stop", len(sessions))
			for _, sess := range sessions {
				output.PrintInfof("  - %s", sess)
			}

			// Configure shutdown
			cfg := swarm.DefaultShutdownConfig()
			cfg.ForceKill = force
			if timeout > 0 {
				cfg.GracefulTimeout = timeout
			}

			// Execute shutdown
			orchestrator := swarm.NewSwarmOrchestrator()
			result, err := orchestrator.GracefulShutdown(ctx, sessions, cfg)
			if err != nil {
				return fmt.Errorf("shutdown failed: %w", err)
			}

			// Output results
			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			output.PrintInfof("Shutdown complete:")
			output.PrintInfof("  Sessions destroyed: %d", result.SessionsDestroyed)
			output.PrintInfof("  Panes signaled: %d", result.PanesKilled)
			output.PrintInfof("  Graceful exits: %d", result.GracefulExits)
			output.PrintInfof("  Duration: %s", result.Duration.Round(time.Millisecond))

			if len(result.Errors) > 0 {
				output.PrintWarningf("  Errors: %d", len(result.Errors))
				for _, e := range result.Errors {
					output.PrintWarningf("    - %v", e)
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Force stop without graceful exit")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Second, "Timeout for graceful exit")
	cmd.Flags().StringSliceVar(&sessionPatterns, "sessions", nil, "Session name patterns to stop")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output results as JSON")

	return cmd
}

// discoverSwarmSessions finds tmux sessions matching the given patterns.
func discoverSwarmSessions(patterns []string) ([]string, error) {
	client := tmux.DefaultClient

	// List all sessions
	allSessions, err := client.ListSessions()
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}

	// Match sessions against patterns
	var matched []string
	seen := make(map[string]bool)

	for _, pattern := range patterns {
		// Compile pattern as glob-like regex
		regexPattern := globToRegex(pattern)
		re, err := regexp.Compile("^" + regexPattern + "$")
		if err != nil {
			slog.Warn("invalid session pattern", "pattern", pattern, "error", err)
			continue
		}

		for _, sess := range allSessions {
			if seen[sess.Name] {
				continue
			}
			if re.MatchString(sess.Name) {
				matched = append(matched, sess.Name)
				seen[sess.Name] = true
			}
		}
	}

	return matched, nil
}

// globToRegex converts a glob pattern to a regex pattern.
func globToRegex(glob string) string {
	result := ""
	for _, c := range glob {
		switch c {
		case '*':
			result += ".*"
		case '?':
			result += "."
		case '.', '+', '^', '$', '(', ')', '[', ']', '{', '}', '|', '\\':
			result += "\\" + string(c)
		default:
			result += string(c)
		}
	}
	return result
}

// swarmAgentTypeToLong maps short swarm agent types to the long form used
// by the robot pattern library.
var swarmAgentTypeToLong = map[string]string{
	"cc":  "claude",
	"cod": "codex",
	"gmi": "gemini",
}

// waitForSwarmAgentsReady polls all agent panes in the swarm plan until they
// show idle/ready state or the timeout expires.  Returns (readyCount, totalCount).
// This implements the readiness gate for --wait-ready (issue #61).
func waitForSwarmAgentsReady(ctx context.Context, plan *swarm.SwarmPlan, client *tmux.Client, timeout time.Duration, logger *slog.Logger) (int, int) {
	deadline := time.Now().Add(timeout)
	pollInterval := 500 * time.Millisecond

	// Collect all pane targets: "session:0.index"
	type paneInfo struct {
		target        string
		shortType     string // "cc", "cod", "gmi" (for status package)
		longType      string // "claude", "codex", "gemini" (for robot patterns)
		ready         bool
	}
	var panes []paneInfo
	for _, sess := range plan.Sessions {
		longType := swarmAgentTypeToLong[sess.AgentType]
		if longType == "" {
			longType = sess.AgentType
		}
		for _, ps := range sess.Panes {
			target := fmt.Sprintf("%s:0.%d", sess.Name, ps.Index)
			panes = append(panes, paneInfo{
				target:    target,
				shortType: sess.AgentType,
				longType:  longType,
			})
		}
	}

	if len(panes) == 0 {
		return 0, 0
	}

	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			break
		}

		allReady := true
		for i := range panes {
			if panes[i].ready {
				continue
			}

			captured, err := client.CapturePaneOutput(panes[i].target, 50)
			if err != nil {
				allReady = false
				continue
			}

			// Check idle using both the status package (short types) and
			// robot pattern library (long types) for comprehensive detection.
			if status.DetectIdleFromOutput(captured, panes[i].shortType) ||
				robot.HasIdlePattern(captured, panes[i].longType) {
				panes[i].ready = true
				logger.Debug("agent ready", "pane", panes[i].target, "type", panes[i].shortType)
			} else {
				allReady = false
			}
		}

		if allReady {
			return len(panes), len(panes)
		}

		time.Sleep(pollInterval)
	}

	readyCount := 0
	for _, p := range panes {
		if p.ready {
			readyCount++
		}
	}
	return readyCount, len(panes)
}
