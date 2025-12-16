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
	"github.com/Dicklesworthstone/ntm/internal/util"
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
		if robotContext != "" {
			// Use --lines flag for scrollback (default 20, or as specified)
			scrollbackLines := robotLines
			if scrollbackLines <= 0 {
				scrollbackLines = 1000 // Default to capturing more for context estimation
			}
			if err := robot.PrintContext(robotContext, scrollbackLines); err != nil {
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
		if robotTokens {
			opts := robot.TokensOptions{
				Days:      robotTokensDays,
				Since:     robotTokensSince,
				GroupBy:   robotTokensGroupBy,
				Session:   robotTokensSession,
				AgentType: robotTokensAgent,
			}
			if err := robot.PrintTokens(opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotHistory != "" {
			opts := robot.HistoryOptions{
				Session:   robotHistory,
				Pane:      robotHistoryPane,
				AgentType: robotHistoryType,
				Last:      robotHistoryLast,
				Since:     robotHistorySince,
				Stats:     robotHistoryStats,
			}
			if err := robot.PrintHistory(opts); err != nil {
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
				// Parse ack timeout duration
				ackTimeout, err := util.ParseDurationWithDefault(robotAckTimeout, time.Millisecond, "ack-timeout")
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: invalid --ack-timeout: %v\n", err)
					os.Exit(1)
				}
				opts := robot.SendAndAckOptions{
					SendOptions: robot.SendOptions{
						Session:    robotSend,
						Message:    robotSendMsg,
						All:        robotSendAll,
						Panes:      paneFilter,
						AgentTypes: agentTypes,
						Exclude:    excludeList,
						DelayMs:    robotSendDelay,
						DryRun:     robotRestoreDry,
					},
					AckTimeoutMs: int(ackTimeout.Milliseconds()),
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
				DryRun:     robotRestoreDry,
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
		if robotSchema != "" {
			if err := robot.PrintSchema(robotSchema); err != nil {
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
			// Parse ack timeout duration
			ackTimeout, err := util.ParseDurationWithDefault(robotAckTimeout, time.Millisecond, "ack-timeout")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid --ack-timeout: %v\n", err)
				os.Exit(1)
			}
			opts := robot.AckOptions{
				Session:   robotAck,
				Message:   robotSendMsg, // Reuse --msg flag for echo detection
				Panes:     paneFilter,
				TimeoutMs: int(ackTimeout.Milliseconds()),
				PollMs:    robotAckPoll,
			}
			if err := robot.PrintAck(opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotAssign != "" {
			var beads []string
			if robotAssignBeads != "" {
				beads = strings.Split(robotAssignBeads, ",")
			}
			opts := robot.AssignOptions{
				Session:  robotAssign,
				Beads:    beads,
				Strategy: robotAssignStrategy,
			}
			if err := robot.PrintAssign(opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotSpawn != "" {
			// Parse spawn timeout duration (expects seconds)
			spawnTimeout, err := util.ParseDurationWithDefault(robotSpawnTimeout, time.Second, "spawn-timeout")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid --spawn-timeout: %v\n", err)
				os.Exit(1)
			}
			opts := robot.SpawnOptions{
				Session:      robotSpawn,
				CCCount:      robotSpawnCC,
				CodCount:     robotSpawnCod,
				GmiCount:     robotSpawnGmi,
				Preset:       robotSpawnPreset,
				NoUserPane:   robotSpawnNoUser,
				WaitReady:    robotSpawnWait,
				ReadyTimeout: int(spawnTimeout.Seconds()),
				DryRun:       robotRestoreDry,
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
			// Parse interrupt timeout duration
			interruptTimeout, err := util.ParseDurationWithDefault(robotInterruptTimeout, time.Millisecond, "interrupt-timeout")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid --interrupt-timeout: %v\n", err)
				os.Exit(1)
			}
			opts := robot.InterruptOptions{
				Session:   robotInterrupt,
				Message:   robotInterruptMsg,
				Panes:     paneFilter,
				All:       robotInterruptAll,
				Force:     robotInterruptForce,
				NoWait:    robotInterruptNoWait,
				TimeoutMs: int(interruptTimeout.Milliseconds()),
				DryRun:    robotRestoreDry,
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
	robotContext   string // session name for context usage

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

	// Robot-schema flag
	robotSchema string // schema type to generate

	// Robot-mail flag
	robotMail bool // Agent Mail state output

	// Robot-ack flags for send confirmation tracking
	robotAck        string // session name for ack
	robotAckTimeout string // timeout (e.g., "30s", "5000ms")
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
	robotSpawnTimeout string // timeout for ready detection (e.g., "30s", "1m")

	// Robot-interrupt flags for priority course correction
	robotInterrupt        string // session name for interrupt
	robotInterruptMsg     string // message to send after interrupt
	robotInterruptAll     bool   // include all panes (including user)
	robotInterruptForce   bool   // send Ctrl+C even if agent appears idle
	robotInterruptNoWait  bool   // don't wait for ready state
	robotInterruptTimeout string // timeout for ready state (e.g., "10s", "5000ms")

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

	// Robot-tokens flags for token usage analysis
	robotTokens        bool   // token usage output
	robotTokensDays    int    // number of days to analyze
	robotTokensSince   string // ISO8601 timestamp to analyze since
	robotTokensGroupBy string // grouping: agent, model, day, week, month
	robotTokensSession string // filter to specific session
	robotTokensAgent   string // filter to specific agent type

	// Robot-history flags for command history tracking
	robotHistory      string // session name for history query
	robotHistoryPane  string // filter by pane ID
	robotHistoryType  string // filter by agent type
	robotHistoryLast  int    // last N entries
	robotHistorySince string // time-based filter
	robotHistoryStats bool   // show statistics instead of entries
)

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default ~/.config/ntm/config.toml)")

	// Global JSON output flag - applies to all commands
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output in JSON format (machine-readable)")
	rootCmd.PersistentFlags().StringVar(&sshHost, "ssh", "", "Remote host for SSH execution (e.g. user@host)")

	// Profiling flag for startup timing analysis
	rootCmd.PersistentFlags().BoolVar(&profileStartup, "profile-startup", false, "Enable startup profiling (outputs timing data)")

	// Robot flags for AI agents - state inspection commands
	rootCmd.Flags().BoolVar(&robotHelp, "robot-help", false, "Show comprehensive AI agent integration guide with examples (JSON)")
	rootCmd.Flags().BoolVar(&robotStatus, "robot-status", false, "Get tmux sessions, panes, agent states. Start here. Example: ntm --robot-status")
	rootCmd.Flags().BoolVar(&robotVersion, "robot-version", false, "Get ntm version, commit, build info (JSON). Example: ntm --robot-version")
	rootCmd.Flags().BoolVar(&robotPlan, "robot-plan", false, "Get bv execution plan with parallelizable tracks (JSON). Example: ntm --robot-plan")
	rootCmd.Flags().BoolVar(&robotSnapshot, "robot-snapshot", false, "Unified state: sessions + beads + alerts + mail. Use --since for delta. Example: ntm --robot-snapshot")
	rootCmd.Flags().StringVar(&robotSince, "since", "", "RFC3339 timestamp for delta snapshot. Optional with --robot-snapshot. Example: --since=2025-12-15T10:00:00Z")
	rootCmd.Flags().StringVar(&robotTail, "robot-tail", "", "Capture recent pane output. Required: SESSION. Example: ntm --robot-tail=myproject --lines=50")
	rootCmd.Flags().IntVar(&robotLines, "lines", 20, "Lines to capture per pane. Optional with --robot-tail. Example: --lines=100")
	rootCmd.Flags().StringVar(&robotPanes, "panes", "", "Filter to specific pane indices. Optional with --robot-tail, --robot-send, --robot-ack, --robot-interrupt. Example: --panes=1,2")
	rootCmd.Flags().BoolVar(&robotGraph, "robot-graph", false, "Get bv dependency graph insights: PageRank, critical path, cycles (JSON)")
	rootCmd.Flags().BoolVar(&robotDashboard, "robot-dashboard", false, "Get dashboard summary as markdown (or JSON with --json). Token-efficient overview")
	rootCmd.Flags().StringVar(&robotContext, "robot-context", "", "Get context window usage for all agents in a session. Required: SESSION. Example: ntm --robot-context=myproject")
	rootCmd.Flags().IntVar(&robotBeadLimit, "bead-limit", 5, "Max beads per category in snapshot. Optional with --robot-snapshot, --robot-status. Example: --bead-limit=10")

	// Robot-send flags for batch messaging
	rootCmd.Flags().StringVar(&robotSend, "robot-send", "", "Send message to panes atomically. Required: SESSION, --msg. Example: ntm --robot-send=proj --msg='Fix auth'")
	rootCmd.Flags().StringVar(&robotSendMsg, "msg", "", "Message content to send. Required with --robot-send. Optional with --robot-ack (enables echo detection)")
	rootCmd.Flags().BoolVar(&robotSendAll, "all", false, "Include user pane (default: agents only). Optional with --robot-send, --robot-interrupt")
	rootCmd.Flags().StringVar(&robotSendType, "type", "", "Filter by agent type: claude|cc, codex|cod, gemini|gmi, cursor, windsurf, aider. Works with --robot-send, --robot-ack, --robot-interrupt")
	rootCmd.Flags().StringVar(&robotSendExclude, "exclude", "", "Exclude pane indices (comma-separated). Optional with --robot-send. Example: --exclude=0,3")
	rootCmd.Flags().IntVar(&robotSendDelay, "delay-ms", 0, "Delay between sends (ms). Optional with --robot-send. Example: --delay-ms=500 for 0.5s between panes")

	// Robot-assign flags for work distribution
	rootCmd.Flags().StringVar(&robotAssign, "robot-assign", "", "Get work distribution recommendations. Required: SESSION. Example: ntm --robot-assign=proj --strategy=speed")
	rootCmd.Flags().StringVar(&robotAssignBeads, "beads", "", "Specific bead IDs to assign (comma-separated). Optional with --robot-assign. Example: --beads=ntm-abc,ntm-xyz")
	rootCmd.Flags().StringVar(&robotAssignStrategy, "strategy", "balanced", "Assignment strategy: balanced (default), speed, quality, dependency. Optional with --robot-assign")

	// Robot-health flag for project health summary
	rootCmd.Flags().BoolVar(&robotHealth, "robot-health", false, "Get project health: tests, linting, coverage, dependencies (JSON). Example: ntm --robot-health")

	// Robot-recipes flag for recipe listing
	rootCmd.Flags().BoolVar(&robotRecipes, "robot-recipes", false, "List available spawn recipes/presets (JSON). Use with --robot-spawn --spawn-preset")

	// Robot-schema flag for JSON Schema generation
	rootCmd.Flags().StringVar(&robotSchema, "robot-schema", "", "Generate JSON Schema for response types. Required: TYPE (status, send, spawn, interrupt, tail, ack, snapshot, all)")

	// Robot-mail flag for Agent Mail state
	rootCmd.Flags().BoolVar(&robotMail, "robot-mail", false, "Get Agent Mail inbox/outbox state (JSON). Shows pending messages and coordination status")

	// Robot-ack flags for send confirmation tracking
	rootCmd.Flags().StringVar(&robotAck, "robot-ack", "", "Watch for agent responses after send. Required: SESSION. Example: ntm --robot-ack=proj --ack-timeout=30s")
	rootCmd.Flags().StringVar(&robotAckTimeout, "ack-timeout", "30s", "Max wait time for responses (e.g., 30s, 5000ms, 1m). Works with --robot-ack, --track")
	rootCmd.Flags().IntVar(&robotAckPoll, "ack-poll", 500, "Poll interval in ms. Optional with --robot-ack. Lower = faster detection, higher CPU")
	rootCmd.Flags().BoolVar(&robotAckTrack, "track", false, "Combined send+ack: send --msg and wait for response. Use with --robot-send. Example: ntm --robot-send=proj --msg='hello' --track")

	// Robot-spawn flags for structured session creation
	rootCmd.Flags().StringVar(&robotSpawn, "robot-spawn", "", "Create session with agents. Required: SESSION name. Example: ntm --robot-spawn=myproject --spawn-cc=2")
	rootCmd.Flags().IntVar(&robotSpawnCC, "spawn-cc", 0, "Claude Code agents to spawn. Use with --robot-spawn. Example: --spawn-cc=2")
	rootCmd.Flags().IntVar(&robotSpawnCod, "spawn-cod", 0, "Codex CLI agents to spawn. Use with --robot-spawn. Example: --spawn-cod=1")
	rootCmd.Flags().IntVar(&robotSpawnGmi, "spawn-gmi", 0, "Gemini CLI agents to spawn. Use with --robot-spawn. Example: --spawn-gmi=1")
	rootCmd.Flags().StringVar(&robotSpawnPreset, "spawn-preset", "", "Use recipe preset instead of counts. See --robot-recipes. Example: --spawn-preset=standard")
	rootCmd.Flags().BoolVar(&robotSpawnNoUser, "spawn-no-user", false, "Skip user pane creation. Optional with --robot-spawn. For headless/automation")
	rootCmd.Flags().BoolVar(&robotSpawnWait, "spawn-wait", false, "Wait for agents to show ready state before returning. Recommended for automation")
	rootCmd.Flags().StringVar(&robotSpawnTimeout, "spawn-timeout", "30s", "Max wait for agent ready state (e.g., 30s, 1m). Use with --spawn-wait")

	// Robot-interrupt flags for priority course correction
	rootCmd.Flags().StringVar(&robotInterrupt, "robot-interrupt", "", "Send Ctrl+C to stop agents, optionally send new task. Required: SESSION. Example: ntm --robot-interrupt=proj --interrupt-msg='Stop and fix bug'")
	rootCmd.Flags().StringVar(&robotInterruptMsg, "interrupt-msg", "", "New task to send after Ctrl+C. Optional with --robot-interrupt. Agents receive this after stopping")
	rootCmd.Flags().BoolVar(&robotInterruptAll, "interrupt-all", false, "Interrupt all panes including user. Default: agents only. Use with --robot-interrupt")
	rootCmd.Flags().BoolVar(&robotInterruptForce, "interrupt-force", false, "Send Ctrl+C even if agent shows idle/ready. Use for stuck agents")
	rootCmd.Flags().BoolVar(&robotInterruptNoWait, "interrupt-no-wait", false, "Return immediately after Ctrl+C without waiting for ready state")
	rootCmd.Flags().StringVar(&robotInterruptTimeout, "interrupt-timeout", "10s", "Max wait for ready state after interrupt (e.g., 10s, 5000ms). Ignored with --interrupt-no-wait")

	// Robot-terse flag for ultra-compact output
	rootCmd.Flags().BoolVar(&robotTerse, "robot-terse", false, "Single-line state: S:session|A:ready/total|W:working|I:idle|B:beads|M:mail|!:alerts. Minimal tokens")

	// Robot-markdown flags for token-efficient markdown output
	rootCmd.Flags().BoolVar(&robotMarkdown, "robot-markdown", false, "System state as markdown tables. LLM-friendly, ~50% fewer tokens than JSON")
	rootCmd.Flags().BoolVar(&robotMarkdownCompact, "md-compact", false, "Ultra-compact markdown: abbreviations, minimal whitespace. Use with --robot-markdown")
	rootCmd.Flags().StringVar(&robotMarkdownSession, "md-session", "", "Filter to one session. Optional with --robot-markdown. Example: --md-session=myproject")
	rootCmd.Flags().StringVar(&robotMarkdownSections, "md-sections", "", "Include only specific sections: sessions,beads,alerts,mail. Example: --md-sections=sessions,beads")
	rootCmd.Flags().IntVar(&robotMarkdownMaxBeads, "md-max-beads", 0, "Max beads per category (0=default). Optional with --robot-markdown")
	rootCmd.Flags().IntVar(&robotMarkdownMaxAlerts, "md-max-alerts", 0, "Max alerts to show (0=default). Optional with --robot-markdown")

	// Robot-save flags for session state persistence
	rootCmd.Flags().StringVar(&robotSave, "robot-save", "", "Save session state for later restore. Required: SESSION. Example: ntm --robot-save=proj --save-output=backup.json")
	rootCmd.Flags().StringVar(&robotSaveOutput, "save-output", "", "Output file path. Optional with --robot-save. Default: ntm-save-{session}-{timestamp}.json")

	// Robot-restore flags for session state restoration
	rootCmd.Flags().StringVar(&robotRestore, "robot-restore", "", "Restore session from saved state. Required: path to save file. Example: ntm --robot-restore=backup.json")
	rootCmd.Flags().BoolVar(&robotRestoreDry, "dry-run", false, "Preview mode: show what would happen without executing. Use with --robot-restore, --robot-interrupt, --robot-send, or --robot-spawn")

	// Robot-cass flags for CASS (Cross-Agent Semantic Search) integration
	rootCmd.Flags().BoolVar(&robotCassStatus, "robot-cass-status", false, "Get CASS health: index status, message counts, freshness (JSON)")
	rootCmd.Flags().StringVar(&robotCassSearch, "robot-cass-search", "", "Search past agent conversations. Required: QUERY. Example: ntm --robot-cass-search='authentication error'")
	rootCmd.Flags().BoolVar(&robotCassInsights, "robot-cass-insights", false, "Get CASS aggregated insights: topics, patterns, agent activity (JSON)")
	rootCmd.Flags().StringVar(&robotCassContext, "robot-cass-context", "", "Get relevant past context for a task. Example: ntm --robot-cass-context='how to implement auth'")

	// CASS filters - work with --robot-cass-search and --robot-cass-context
	rootCmd.Flags().StringVar(&cassAgent, "cass-agent", "", "Filter CASS by agent: claude, codex, gemini, cursor, etc. Example: --cass-agent=claude")
	rootCmd.Flags().StringVar(&cassWorkspace, "cass-workspace", "", "Filter CASS by workspace/project path. Example: --cass-workspace=/path/to/project")
	rootCmd.Flags().StringVar(&cassSince, "cass-since", "", "Filter CASS by recency: 1d, 7d, 30d, etc. Example: --cass-since=7d")
	rootCmd.Flags().IntVar(&cassLimit, "cass-limit", 10, "Max CASS results to return. Example: --cass-limit=20")

	// Robot-tokens flags for token usage analysis
	rootCmd.Flags().BoolVar(&robotTokens, "robot-tokens", false, "Get token usage statistics (JSON). Group by agent, model, or time period")
	rootCmd.Flags().IntVar(&robotTokensDays, "tokens-days", 30, "Days to analyze. Optional with --robot-tokens. Example: --tokens-days=7")
	rootCmd.Flags().StringVar(&robotTokensSince, "tokens-since", "", "Analyze since date (ISO8601 or YYYY-MM-DD). Optional with --robot-tokens")
	rootCmd.Flags().StringVar(&robotTokensGroupBy, "tokens-group-by", "agent", "Grouping: agent, model, day, week, month. Optional with --robot-tokens")
	rootCmd.Flags().StringVar(&robotTokensSession, "tokens-session", "", "Filter to session. Optional with --robot-tokens. Example: --tokens-session=myproject")
	rootCmd.Flags().StringVar(&robotTokensAgent, "tokens-agent", "", "Filter to agent type. Optional with --robot-tokens. Example: --tokens-agent=claude")

	// Robot-history flags for command history tracking
	rootCmd.Flags().StringVar(&robotHistory, "robot-history", "", "Get command history for a session (JSON). Required: SESSION. Example: ntm --robot-history=myproject")
	rootCmd.Flags().StringVar(&robotHistoryPane, "history-pane", "", "Filter by pane ID. Optional with --robot-history. Example: --history-pane=0.1")
	rootCmd.Flags().StringVar(&robotHistoryType, "history-type", "", "Filter by agent type. Optional with --robot-history. Example: --history-type=claude")
	rootCmd.Flags().IntVar(&robotHistoryLast, "history-last", 0, "Show last N entries. Optional with --robot-history. Example: --history-last=10")
	rootCmd.Flags().StringVar(&robotHistorySince, "history-since", "", "Show entries since time (1h, 30m, 2d, or ISO8601). Optional with --robot-history")
	rootCmd.Flags().BoolVar(&robotHistoryStats, "history-stats", false, "Show statistics instead of entries. Optional with --robot-history")

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

		// Internal commands
		newMonitorCmd(),

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
		newMonitorCmd(),
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
			robotTerse || robotMarkdown || robotSave != "" || robotRestore != "" ||
			robotContext != "" {
			return true
		}
	}

	return false
}
