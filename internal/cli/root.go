package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/plugins"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/startup"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

var (
	cfgFile string
	cfg     *config.Config
	sshHost string

	// Global JSON output flag - inherited by all subcommands
	jsonOutput bool

	// Build information - set by goreleaser via ldflags
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
	BuiltBy = "unknown"
)

var rootCmd = &cobra.Command{
	Use:   "ntm",
	Short: "Named Tmux Manager - orchestrate AI coding agents in tmux sessions",
	Long: `NTM (Named Tmux Manager) helps you create and manage tmux sessions
with multiple AI coding agents (Claude, Codex, Gemini) in separate panes.

Quick Start:
  ntm spawn myproject --cc=2 --cod=2    # Create session with 4 agents
  ntm attach myproject                   # Attach to session
  ntm palette                            # Open command palette (TUI)
  ntm send myproject --all "fix bugs"   # Broadcast prompt to all agents

Shell Integration:
  Add to your .zshrc:  eval "$(ntm init zsh)"
  Add to your .bashrc: eval "$(ntm init bash)"`,
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Configure remote client if requested
		if sshHost != "" {
			tmux.DefaultClient = tmux.NewClient(sshHost)
		}

		// Phase 1: Critical startup (always runs, minimal overhead)
		startup.BeginPhase1()
		EnableProfilingIfRequested()
		startup.EndPhase1()

		// Check if this command can skip config loading (Phase 1 only)
		// This includes subcommands AND robot flags that don't need config
		if canSkipConfigLoading(cmd.Name()) {
			return nil
		}

		// Phase 2: Deferred initialization (config loading)
		startup.BeginPhase2()
		defer startup.EndPhase2()

		// Set config path for lazy loader
		startup.SetConfigPath(cfgFile)

		// Load config lazily - only commands that need it will trigger loading
		if needsConfigLoading(cmd.Name()) {
			endProfile := ProfileConfigLoad()
			var err error
			cfg, err = startup.GetConfig()
			endProfile()
			if err != nil {
				// Use defaults if config loading fails
				cfg = config.Default()
			}
		}
		return nil
	},
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
		// Print profiling output if enabled
		PrintProfilingIfEnabled()
	},
	Run: func(cmd *cobra.Command, args []string) {
		// Handle robot flags for AI agent integration
		if robotHelp {
			robot.PrintHelp()
			return
		}
		if robotStatus {
			if err := robot.PrintStatus(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotVersion {
			if err := robot.PrintVersion(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotPlan {
			if err := robot.PrintPlan(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotSnapshot {
			// Set bead limit from flag
			if robotBeadLimit > 0 {
				robot.BeadLimit = robotBeadLimit
			}
			var err error
			if robotSince != "" {
				// Parse the since timestamp
				sinceTime, parseErr := time.Parse(time.RFC3339, robotSince)
				if parseErr != nil {
					fmt.Fprintf(os.Stderr, "Error: invalid --since timestamp (expected ISO8601/RFC3339 format): %v\n", parseErr)
					os.Exit(1)
				}
				err = robot.PrintSnapshotDelta(sinceTime)
			} else {
				err = robot.PrintSnapshot(cfg)
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotGraph {
			if err := robot.PrintGraph(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotDashboard {
			if err := robot.PrintDashboard(jsonOutput); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotMail {
			projectKey, _ := os.Getwd()
			sessionName := ""
			if len(args) > 0 {
				sessionName = args[0]
			} else if tmux.IsInstalled() {
				// Best-effort: infer a session when running inside tmux or when cwd matches
				// a project dir. Robot mode must never prompt.
				if res, err := ResolveSessionWithOptions("", cmd.OutOrStdout(), SessionResolveOptions{TreatAsJSON: true}); err == nil && res.Session != "" {
					sessionName = res.Session
				}
			}

			if sessionName != "" && cfg != nil {
				projectKey = cfg.GetProjectDir(sessionName)
			}

			if err := robot.PrintMail(sessionName, projectKey); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotCassStatus {
			if err := robot.PrintCASSStatus(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotCassSearch != "" {
			if err := robot.PrintCASSSearch(robotCassSearch, cassAgent, cassWorkspace, cassSince, cassLimit); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotCassInsights {
			if err := robot.PrintCASSInsights(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotCassContext != "" {
			if err := robot.PrintCASSContext(robotCassContext); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotTail != "" {
			// Parse pane filter
			var paneFilter []string
			if robotPanes != "" {
				paneFilter = strings.Split(robotPanes, ",")
			}
			if err := robot.PrintTail(robotTail, robotLines, paneFilter); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotSend != "" {
			// Validate message is provided
			if robotSendMsg == "" {
				fmt.Fprintf(os.Stderr, "Error: --msg is required with --robot-send\n")
				os.Exit(1)
			}
			// Parse pane filter
			var paneFilter []string
			if robotPanes != "" {
				paneFilter = strings.Split(robotPanes, ",")
			}
			// Parse exclude list
			var excludeList []string
			if robotSendExclude != "" {
				excludeList = strings.Split(robotSendExclude, ",")
			}
			// Parse agent types
			var agentTypes []string
			if robotSendType != "" {
				agentTypes = strings.Split(robotSendType, ",")
			}

			// Check if --track flag is set for combined send+ack mode
			if robotAckTrack {
				opts := robot.SendAndAckOptions{
					SendOptions: robot.SendOptions{
						Session:    robotSend,
						Message:    robotSendMsg,
						All:        robotSendAll,
						Panes:      paneFilter,
						AgentTypes: agentTypes,
						Exclude:    excludeList,
						DelayMs:    robotSendDelay,
					},
					AckTimeoutMs: robotAckTimeout,
					AckPollMs:    robotAckPoll,
				}
				if err := robot.PrintSendAndAck(opts); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
				return
			}

			opts := robot.SendOptions{
				Session:    robotSend,
				Message:    robotSendMsg,
				All:        robotSendAll,
				Panes:      paneFilter,
				AgentTypes: agentTypes,
				Exclude:    excludeList,
				DelayMs:    robotSendDelay,
			}
			if err := robot.PrintSend(opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotHealth {
			if err := robot.PrintHealth(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotRecipes {
			if err := robot.PrintRecipes(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotAck != "" {
			// Parse pane filter
			var paneFilter []string
			if robotPanes != "" {
				paneFilter = strings.Split(robotPanes, ",")
			}
			opts := robot.AckOptions{
				Session:   robotAck,
				Message:   robotSendMsg, // Reuse --msg flag for echo detection
				Panes:     paneFilter,
				TimeoutMs: robotAckTimeout,
				PollMs:    robotAckPoll,
			}
			if err := robot.PrintAck(opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		// TODO(ntm-20n): --robot-assign is in development
		if robotAssign != "" {
			fmt.Fprintf(os.Stderr, "Error: --robot-assign is not yet implemented\n")
			os.Exit(1)
		}
		if robotSpawn != "" {
			opts := robot.SpawnOptions{
				Session:      robotSpawn,
				CCCount:      robotSpawnCC,
				CodCount:     robotSpawnCod,
				GmiCount:     robotSpawnGmi,
				Preset:       robotSpawnPreset,
				NoUserPane:   robotSpawnNoUser,
				WaitReady:    robotSpawnWait,
				ReadyTimeout: robotSpawnTimeout,
			}
			if err := robot.PrintSpawn(opts, cfg); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotInterrupt != "" {
			// Parse pane filter (reuse --panes flag)
			var paneFilter []string
			if robotPanes != "" {
				paneFilter = strings.Split(robotPanes, ",")
			}
			opts := robot.InterruptOptions{
				Session:   robotInterrupt,
				Message:   robotInterruptMsg,
				Panes:     paneFilter,
				All:       robotInterruptAll,
				Force:     robotInterruptForce,
				NoWait:    robotInterruptNoWait,
				TimeoutMs: robotInterruptTimeout,
			}
			if err := robot.PrintInterrupt(opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotTerse {
			if err := robot.PrintTerse(cfg); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotMarkdown {
			opts := robot.DefaultMarkdownOptions()
			opts.Compact = robotMarkdownCompact
			opts.Session = robotMarkdownSession
			if robotMarkdownSections != "" {
				parts := strings.Split(robotMarkdownSections, ",")
				var sections []string
				for _, p := range parts {
					p = strings.TrimSpace(p)
					if p != "" {
						sections = append(sections, p)
					}
				}
				if len(sections) > 0 {
					opts.IncludeSections = sections
				}
			}
			if robotMarkdownMaxBeads > 0 {
				opts.MaxBeads = robotMarkdownMaxBeads
			}
			if robotMarkdownMaxAlerts > 0 {
				opts.MaxAlerts = robotMarkdownMaxAlerts
			}
			if err := robot.PrintMarkdown(cfg, opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotSave != "" {
			opts := robot.SaveOptions{
				Session:    robotSave,
				OutputFile: robotSaveOutput,
			}
			if err := robot.PrintSave(opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotRestore != "" {
			opts := robot.RestoreOptions{
				SavedName: robotRestore,
				DryRun:    robotRestoreDry,
			}
			if err := robot.PrintRestore(opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}

		// Show stunning help with gradients when run without subcommand
		PrintStunningHelp(cmd.OutOrStdout())
	},
}

func Execute() error {
	if err := rootCmd.Execute(); err != nil {
		// If not in JSON mode, print the error to stderr
		// (SilenceErrors is set to true to handle JSON mode properly)
		if !jsonOutput {
			fmt.Fprintln(os.Stderr, "Error:", err)
		}
		return err
	}
	return nil
}

// goVersion returns the current Go runtime version.
func goVersion() string {
	return runtime.Version()
}

// goPlatform returns the OS/ARCH string.
func goPlatform() string {
	return fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
}

// Robot output flags for AI agent integration
var (
	robotHelp      bool
	robotStatus    bool
	robotVersion   bool
	robotPlan      bool
	robotSnapshot  bool   // unified state query
	robotSince     string // ISO8601 timestamp for delta snapshot
	robotTail      string // session name for tail
	robotLines     int    // number of lines to capture
	robotPanes     string // comma-separated pane filter
	robotGraph     bool   // bv insights passthrough
	robotBeadLimit int    // limit for ready/in-progress beads in snapshot
	robotDashboard bool   // dashboard summary output

	// Robot-send flags
	robotSend        string // session name for send
	robotSendMsg     string // message to send
	robotSendAll     bool   // send to all panes
	robotSendType    string // filter by agent type (e.g., "claude")
	robotSendExclude string // comma-separated panes to exclude
	robotSendDelay   int    // delay between sends in ms

	// Robot-assign flags for work distribution
	robotAssign         string // session name for work assignment
	robotAssignBeads    string // comma-separated bead IDs to assign
	robotAssignStrategy string // assignment strategy: balanced, speed, quality, dependency

	// Robot-health flag
	robotHealth bool // project health summary

	// Robot-recipes flag
	robotRecipes bool // list available recipes as JSON

	// Robot-mail flag
	robotMail bool // Agent Mail state output

	// Robot-ack flags for send confirmation tracking
	robotAck        string // session name for ack
	robotAckTimeout int    // timeout in milliseconds
	robotAckPoll    int    // poll interval in milliseconds
	robotAckTrack   bool   // combined send+ack mode

	// Robot-spawn flags for structured session creation
	robotSpawn        string // session name for spawn
	robotSpawnCC      int    // number of Claude agents
	robotSpawnCod     int    // number of Codex agents
	robotSpawnGmi     int    // number of Gemini agents
	robotSpawnPreset  string // recipe/preset name
	robotSpawnNoUser  bool   // don't create user pane
	robotSpawnWait    bool   // wait for agents to be ready
	robotSpawnTimeout int    // timeout for ready detection in seconds

	// Robot-interrupt flags for priority course correction
	robotInterrupt        string // session name for interrupt
	robotInterruptMsg     string // message to send after interrupt
	robotInterruptAll     bool   // include all panes (including user)
	robotInterruptForce   bool   // send Ctrl+C even if agent appears idle
	robotInterruptNoWait  bool   // don't wait for ready state
	robotInterruptTimeout int    // timeout for ready state in milliseconds

	// Robot-terse flag for ultra-compact output
	robotTerse bool // single-line encoded state

	// Robot-markdown flags for token-efficient markdown output
	robotMarkdown          bool   // markdown output mode
	robotMarkdownCompact   bool   // ultra-compact markdown
	robotMarkdownSession   string // filter to specific session
	robotMarkdownSections  string // comma-separated sections to include
	robotMarkdownMaxBeads  int    // max beads per category
	robotMarkdownMaxAlerts int    // max alerts to show

	// Robot-save flags for session state persistence
	robotSave       string // session name to save
	robotSaveOutput string // custom output file path

	// Robot-restore flags for session state restoration
	robotRestore    string // saved state name to restore
	robotRestoreDry bool   // dry-run mode

	// Robot-cass flags for CASS integration
	robotCassStatus   bool   // CASS health check
	robotCassSearch   string // search query
	robotCassInsights bool   // aggregated insights
	robotCassContext  string // context query
	cassAgent         string // filter by agent
	cassWorkspace     string // filter by workspace
	cassSince         string // filter by time
	cassLimit         int    // max results
)

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default ~/.config/ntm/config.toml)")

	// Global JSON output flag - applies to all commands
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output in JSON format (machine-readable)")
	rootCmd.PersistentFlags().StringVar(&sshHost, "ssh", "", "Remote host for SSH execution (e.g. user@host)")

	// Profiling flag for startup timing analysis
	rootCmd.PersistentFlags().BoolVar(&profileStartup, "profile-startup", false, "Enable startup profiling (outputs timing data)")

	// Robot flags for AI agents
	rootCmd.Flags().BoolVar(&robotHelp, "robot-help", false, "Show AI agent help documentation (JSON)")
	rootCmd.Flags().BoolVar(&robotStatus, "robot-status", false, "Output session status as JSON for AI agents")
	rootCmd.Flags().BoolVar(&robotVersion, "robot-version", false, "Output version info as JSON")
	rootCmd.Flags().BoolVar(&robotPlan, "robot-plan", false, "Output execution plan as JSON for AI agents")
	rootCmd.Flags().BoolVar(&robotSnapshot, "robot-snapshot", false, "Output unified system state snapshot (JSON)")
	rootCmd.Flags().StringVar(&robotSince, "since", "", "ISO8601 timestamp for delta snapshot (used with --robot-snapshot)")
	rootCmd.Flags().StringVar(&robotTail, "robot-tail", "", "Tail pane output for session (JSON)")
	rootCmd.Flags().IntVar(&robotLines, "lines", 20, "Number of lines to capture (used with --robot-tail)")
	rootCmd.Flags().StringVar(&robotPanes, "panes", "", "Comma-separated pane indices to filter (used with --robot-tail/--robot-send)")
	rootCmd.Flags().BoolVar(&robotGraph, "robot-graph", false, "Output bv graph insights as JSON for AI agents")
	rootCmd.Flags().BoolVar(&robotDashboard, "robot-dashboard", false, "Output dashboard summary as markdown tables (or JSON with --json) for AI agents")
	rootCmd.Flags().IntVar(&robotBeadLimit, "bead-limit", 5, "Limit for ready/in-progress beads in snapshot (used with --robot-snapshot)")

	// Robot-send flags for batch messaging
	rootCmd.Flags().StringVar(&robotSend, "robot-send", "", "Send prompt to panes atomically (JSON output)")
	rootCmd.Flags().StringVar(&robotSendMsg, "msg", "", "Message to send (used with --robot-send)")
	rootCmd.Flags().BoolVar(&robotSendAll, "all", false, "Send to all panes including user (used with --robot-send)")
	rootCmd.Flags().StringVar(&robotSendType, "type", "", "Filter by agent type: claude, codex, gemini (used with --robot-send)")
	rootCmd.Flags().StringVar(&robotSendExclude, "exclude", "", "Comma-separated pane indices to exclude (used with --robot-send)")
	rootCmd.Flags().IntVar(&robotSendDelay, "delay-ms", 0, "Delay between sends in milliseconds (used with --robot-send)")

	// Robot-assign flags for work distribution
	rootCmd.Flags().StringVar(&robotAssign, "robot-assign", "", "Get work distribution recommendations for session (JSON)")
	rootCmd.Flags().StringVar(&robotAssignBeads, "beads", "", "Comma-separated bead IDs to assign (used with --robot-assign)")
	rootCmd.Flags().StringVar(&robotAssignStrategy, "strategy", "balanced", "Assignment strategy: balanced, speed, quality, dependency (used with --robot-assign)")

	// Robot-health flag for project health summary
	rootCmd.Flags().BoolVar(&robotHealth, "robot-health", false, "Output project health summary as JSON for AI agents")

	// Robot-recipes flag for recipe listing
	rootCmd.Flags().BoolVar(&robotRecipes, "robot-recipes", false, "List available recipes as JSON for AI agents")

	// Robot-mail flag for Agent Mail state
	rootCmd.Flags().BoolVar(&robotMail, "robot-mail", false, "Output Agent Mail state as JSON for AI agents")

	// Robot-ack flags for send confirmation tracking
	rootCmd.Flags().StringVar(&robotAck, "robot-ack", "", "Watch panes for acknowledgment after send (JSON output)")
	rootCmd.Flags().IntVar(&robotAckTimeout, "ack-timeout", 30000, "Timeout in milliseconds for acknowledgment (used with --robot-ack)")
	rootCmd.Flags().IntVar(&robotAckPoll, "ack-poll", 500, "Poll interval in milliseconds (used with --robot-ack)")
	rootCmd.Flags().BoolVar(&robotAckTrack, "track", false, "Combined send+ack mode: send message and wait for acknowledgment (used with --robot-send)")

	// Robot-spawn flags for structured session creation
	rootCmd.Flags().StringVar(&robotSpawn, "robot-spawn", "", "Create session and spawn agents (JSON output)")
	rootCmd.Flags().IntVar(&robotSpawnCC, "spawn-cc", 0, "Number of Claude agents (used with --robot-spawn)")
	rootCmd.Flags().IntVar(&robotSpawnCod, "spawn-cod", 0, "Number of Codex agents (used with --robot-spawn)")
	rootCmd.Flags().IntVar(&robotSpawnGmi, "spawn-gmi", 0, "Number of Gemini agents (used with --robot-spawn)")
	rootCmd.Flags().StringVar(&robotSpawnPreset, "spawn-preset", "", "Recipe/preset name (used with --robot-spawn)")
	rootCmd.Flags().BoolVar(&robotSpawnNoUser, "spawn-no-user", false, "Don't create user pane (used with --robot-spawn)")
	rootCmd.Flags().BoolVar(&robotSpawnWait, "spawn-wait", false, "Wait for agents to be ready (used with --robot-spawn)")
	rootCmd.Flags().IntVar(&robotSpawnTimeout, "spawn-timeout", 30, "Timeout in seconds for ready detection (used with --robot-spawn)")

	// Robot-interrupt flags for priority course correction
	rootCmd.Flags().StringVar(&robotInterrupt, "robot-interrupt", "", "Interrupt agents with Ctrl+C and optionally send message (JSON output)")
	rootCmd.Flags().StringVar(&robotInterruptMsg, "interrupt-msg", "", "Message to send after interrupt (used with --robot-interrupt)")
	rootCmd.Flags().BoolVar(&robotInterruptAll, "interrupt-all", false, "Include all panes including user (used with --robot-interrupt)")
	rootCmd.Flags().BoolVar(&robotInterruptForce, "interrupt-force", false, "Send Ctrl+C even if agent appears idle (used with --robot-interrupt)")
	rootCmd.Flags().BoolVar(&robotInterruptNoWait, "interrupt-no-wait", false, "Don't wait for ready state (used with --robot-interrupt)")
	rootCmd.Flags().IntVar(&robotInterruptTimeout, "interrupt-timeout", 10000, "Timeout in ms for ready state (used with --robot-interrupt)")

	// Robot-terse flag for ultra-compact output
	rootCmd.Flags().BoolVar(&robotTerse, "robot-terse", false, "Output ultra-compact single-line state (e.g., S:proj|A:2/3|W:2|I:1|E:0|C:0%|B:R10/I5/B2|M:3|!:1c,2w)")

	// Robot-markdown flags for token-efficient markdown output
	rootCmd.Flags().BoolVar(&robotMarkdown, "robot-markdown", false, "Output system state as token-efficient markdown for LLM consumption")
	rootCmd.Flags().BoolVar(&robotMarkdownCompact, "md-compact", false, "Use ultra-compact markdown format (used with --robot-markdown)")
	rootCmd.Flags().StringVar(&robotMarkdownSession, "md-session", "", "Filter markdown output to specific session (used with --robot-markdown)")
	rootCmd.Flags().StringVar(&robotMarkdownSections, "md-sections", "", "Comma-separated sections to include: sessions, beads, alerts, mail (used with --robot-markdown)")
	rootCmd.Flags().IntVar(&robotMarkdownMaxBeads, "md-max-beads", 0, "Override max beads shown per category (used with --robot-markdown)")
	rootCmd.Flags().IntVar(&robotMarkdownMaxAlerts, "md-max-alerts", 0, "Override max alerts shown (used with --robot-markdown)")

	// Robot-save flags for session state persistence
	rootCmd.Flags().StringVar(&robotSave, "robot-save", "", "Save session state as JSON for AI agents")
	rootCmd.Flags().StringVar(&robotSaveOutput, "save-output", "", "Custom output file path (used with --robot-save)")

	// Robot-restore flags for session state restoration
	rootCmd.Flags().StringVar(&robotRestore, "robot-restore", "", "Restore session from saved state (JSON output)")
	rootCmd.Flags().BoolVar(&robotRestoreDry, "dry-run", false, "Preview restore without executing (used with --robot-restore)")

	// Robot-cass flags for CASS integration
	rootCmd.Flags().BoolVar(&robotCassStatus, "robot-cass-status", false, "Output CASS health and stats as JSON")
	rootCmd.Flags().StringVar(&robotCassSearch, "robot-cass-search", "", "Search CASS index and return JSON results")
	rootCmd.Flags().BoolVar(&robotCassInsights, "robot-cass-insights", false, "Output CASS aggregated insights as JSON")
	rootCmd.Flags().StringVar(&robotCassContext, "robot-cass-context", "", "Get relevant past context for a query as JSON")

	// CASS filters
	rootCmd.Flags().StringVar(&cassAgent, "cass-agent", "", "Filter CASS search by agent type")
	rootCmd.Flags().StringVar(&cassWorkspace, "cass-workspace", "", "Filter CASS search by workspace")
	rootCmd.Flags().StringVar(&cassSince, "cass-since", "", "Filter CASS search by time (e.g. 7d)")
	rootCmd.Flags().IntVar(&cassLimit, "cass-limit", 10, "Limit CASS search results")

	// Sync version info with robot package
	robot.Version = Version
	robot.Commit = Commit
	robot.Date = Date
	robot.BuiltBy = BuiltBy

	// Add all subcommands
	rootCmd.AddCommand(
		// Session creation
		newCreateCmd(),
		newSpawnCmd(),
		newQuickCmd(),

		// Agent management
		newAddCmd(),
		newSendCmd(),
		newReplayCmd(),
		newInterruptCmd(),
		newRotateCmd(),
		newQuotaCmd(),
		newPipelineCmd(),
		newMailCmd(),
		newPluginsCmd(),

		// Session navigation
		newAttachCmd(),
		newListCmd(),
		newStatusCmd(),
		newViewCmd(),
		newZoomCmd(),
		newDashboardCmd(),
		newWatchCmd(),

		// Output management
		newCopyCmd(),
		newSaveCmd(),
		newGrepCmd(),
		newExtractCmd(),
		newDiffCmd(),
		newChangesCmd(),
		newConflictsCmd(),

		// Session persistence
		newCheckpointCmd(),
		newRollbackCmd(),
		newSessionPersistCmd(),

		// Utilities
		newPaletteCmd(),
		newBindCmd(),
		newDepsCmd(),
		newKillCmd(),
		newScanCmd(),
		newCassCmd(),
		newHooksCmd(),
		newHealthCmd(),
		newHistoryCmd(),
		newAnalyticsCmd(),

		// Shell integration
		newInitCmd(),
		newCompletionCmd(),
		newVersionCmd(),
		newConfigCmd(),
		newUpgradeCmd(),

		// Tutorial
		newTutorialCmd(),

		// Agent Mail & File Reservations
		newLockCmd(),
		newUnlockCmd(),
		newLocksCmd(),

		// Git coordination
		newGitCmd(),

		// Configuration management
		newRecipesCmd(),
		newPersonasCmd(),
		newTemplateCmd(),
	)

	// Load command plugins
	configDir := filepath.Dir(config.DefaultPath())
	cmdDir := filepath.Join(configDir, "commands")
	cmds, _ := plugins.LoadCommandPlugins(cmdDir)

	for _, p := range cmds {
		plugin := p // Capture for closure
		cmd := &cobra.Command{
			Use:                plugin.Name,
			Short:              plugin.Description,
			Long:               plugin.Description + "\n\nUsage: " + plugin.Usage,
			DisableFlagParsing: true,
			RunE: func(c *cobra.Command, args []string) error {
				// Prepare env
				env := map[string]string{
					"NTM_CONFIG_PATH": config.DefaultPath(),
					"NTM_VERSION":     Version,
				}
				if jsonOutput {
					env["NTM_JSON"] = "1"
				}
				if s := tmux.GetCurrentSession(); s != "" {
					env["NTM_SESSION"] = s
				}

				return plugin.Execute(args, env)
			},
		}
		rootCmd.AddCommand(cmd)
	}
}

func newVersionCmd() *cobra.Command {
	var short bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		RunE: func(cmd *cobra.Command, args []string) error {
			if IsJSONOutput() {
				response := output.VersionResponse{
					TimestampedResponse: output.NewTimestamped(),
					Version:             Version,
					Commit:              Commit,
					BuiltAt:             Date,
					BuiltBy:             BuiltBy,
					GoVersion:           goVersion(),
					Platform:            goPlatform(),
				}
				return output.PrintJSON(response)
			}

			if short {
				fmt.Println(Version)
				return nil
			}
			fmt.Printf("ntm version %s\n", Version)
			fmt.Printf("  commit:    %s\n", Commit)
			fmt.Printf("  built:     %s\n", Date)
			fmt.Printf("  builder:   %s\n", BuiltBy)
			fmt.Printf("  go:        %s\n", goVersion())
			fmt.Printf("  platform:  %s\n", goPlatform())
			return nil
		},
	}
	cmd.Flags().BoolVarP(&short, "short", "s", false, "Print only version number")
	return cmd
}

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage configuration",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "init",
		Short: "Create default configuration file",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := config.CreateDefault()
			if err != nil {
				return err
			}
			fmt.Printf("Created config file: %s\n", path)
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "path",
		Short: "Print configuration file path",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(config.DefaultPath())
		},
	})

	// Add 'set' subcommand for easy configuration
	setCmd := &cobra.Command{
		Use:   "set",
		Short: "Set configuration values",
	}

	setCmd.AddCommand(&cobra.Command{
		Use:   "projects-base <path>",
		Short: "Set the base directory for projects",
		Long: `Set the base directory where ntm creates project folders.

Examples:
  ntm config set projects-base ~/projects
  ntm config set projects-base /data/projects
  ntm config set projects-base ~/Developer`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			if err := config.SetProjectsBase(path); err != nil {
				return err
			}
			expanded := config.ExpandHome(path)
			fmt.Printf("Projects base set to: %s\n", expanded)
			fmt.Printf("Config saved to: %s\n", config.DefaultPath())
			return nil
		},
	})

	cmd.AddCommand(setCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Show current configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			effectiveCfg := cfg
			if effectiveCfg == nil {
				loaded, err := config.Load(cfgFile)
				if err != nil {
					loaded = config.Default()
				}
				effectiveCfg = loaded
			}

			if IsJSONOutput() {
				palette := make([]map[string]interface{}, 0, len(effectiveCfg.Palette))
				for _, pal := range effectiveCfg.Palette {
					palette = append(palette, map[string]interface{}{
						"key":      pal.Key,
						"label":    pal.Label,
						"prompt":   pal.Prompt,
						"category": pal.Category,
						"tags":     pal.Tags,
					})
				}

				return output.PrintJSON(map[string]interface{}{
					"projects_base": effectiveCfg.ProjectsBase,
					"theme":         effectiveCfg.Theme,
					"palette_file":  effectiveCfg.PaletteFile,
					"agents": map[string]string{
						"claude": effectiveCfg.Agents.Claude,
						"codex":  effectiveCfg.Agents.Codex,
						"gemini": effectiveCfg.Agents.Gemini,
					},
					"tmux": map[string]interface{}{
						"default_panes": effectiveCfg.Tmux.DefaultPanes,
						"palette_key":   effectiveCfg.Tmux.PaletteKey,
					},
					"checkpoints": map[string]interface{}{
						"enabled":                  effectiveCfg.Checkpoints.Enabled,
						"before_broadcast":         effectiveCfg.Checkpoints.BeforeBroadcast,
						"before_add_agents":        effectiveCfg.Checkpoints.BeforeAddAgents,
						"max_auto_checkpoints":     effectiveCfg.Checkpoints.MaxAutoCheckpoints,
						"scrollback_lines":         effectiveCfg.Checkpoints.ScrollbackLines,
						"include_git":              effectiveCfg.Checkpoints.IncludeGit,
						"auto_checkpoint_on_spawn": effectiveCfg.Checkpoints.AutoCheckpointOnSpawn,
					},
					"alerts": map[string]interface{}{
						"enabled":                effectiveCfg.Alerts.Enabled,
						"agent_stuck_minutes":    effectiveCfg.Alerts.AgentStuckMinutes,
						"disk_low_threshold_gb":  effectiveCfg.Alerts.DiskLowThresholdGB,
						"mail_backlog_threshold": effectiveCfg.Alerts.MailBacklogThreshold,
						"bead_stale_hours":       effectiveCfg.Alerts.BeadStaleHours,
						"resolved_prune_minutes": effectiveCfg.Alerts.ResolvedPruneMinutes,
					},
					"palette": palette,
				})
			}

			return config.Print(effectiveCfg, os.Stdout)
		},
	})

	projectCmd := &cobra.Command{
		Use:   "project",
		Short: "Manage project-specific configuration",
	}

	var projectInitForce bool
	projectInitCmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize .ntm configuration for current project",
		RunE: func(cmd *cobra.Command, args []string) error {
			return config.InitProjectConfig(projectInitForce)
		},
	}
	projectInitCmd.Flags().BoolVar(&projectInitForce, "force", false, "overwrite .ntm/config.toml if it already exists")
	projectCmd.AddCommand(projectInitCmd)

	cmd.AddCommand(projectCmd)

	return cmd
}

// IsJSONOutput returns true if JSON output is enabled
func IsJSONOutput() bool {
	return jsonOutput
}

// GetOutputFormat returns the current output format
func GetOutputFormat() output.Format {
	return output.DetectFormat(jsonOutput)
}

// GetFormatter returns a formatter configured for the current output mode
func GetFormatter() *output.Formatter {
	return output.New(output.WithJSON(jsonOutput))
}

// canSkipConfigLoading returns true if we can skip Phase 2 config loading.
// This checks both subcommand names and robot flags for Phase 1 only operations.
func canSkipConfigLoading(cmdName string) bool {
	// Check subcommand first
	if startup.CanSkipConfig(cmdName) {
		return true
	}

	// Check robot flags that don't need config
	// These flags are processed in the root command's Run function
	if cmdName == "ntm" || cmdName == "" {
		if robotHelp || robotVersion {
			return true
		}
	}

	return false
}

// needsConfigLoading returns true if config should be loaded for this command.
// This checks both subcommand names and robot flags.
func needsConfigLoading(cmdName string) bool {
	// Check subcommand first
	if startup.NeedsConfig(cmdName) {
		return true
	}

	// Check robot flags that need config
	if cmdName == "ntm" || cmdName == "" {
		// robot-recipes needs config but not full startup
		if robotRecipes {
			return true
		}
		// Most other robot flags need full config
		if robotStatus || robotPlan || robotSnapshot || robotTail != "" ||
			robotSend != "" || robotAck != "" || robotSpawn != "" ||
			robotInterrupt != "" || robotGraph || robotMail || robotHealth ||
			robotTerse || robotMarkdown || robotSave != "" || robotRestore != "" {
			return true
		}
	}

	return false
}
