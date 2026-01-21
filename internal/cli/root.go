package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/pipeline"
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
  Add to your .zshrc:  eval "$(ntm shell zsh)"
  Add to your .bashrc: eval "$(ntm shell bash)"`,
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

			// Run automatic temp file cleanup if enabled
			MaybeRunStartupCleanup(
				cfg.Cleanup.AutoCleanOnStartup,
				cfg.Cleanup.MaxAgeHours,
				cfg.Cleanup.Verbose,
			)
		}
		return nil
	},
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
		// Print profiling output if enabled
		PrintProfilingIfEnabled()
	},
	Run: func(cmd *cobra.Command, args []string) {
		// Resolve robot output format and verbosity: CLI flag > env var > config > default
		resolveRobotFormat()
		resolveRobotVerbosity(cfg)

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
		if robotCapabilities {
			if err := robot.PrintCapabilities(); err != nil {
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
		if robotTriage {
			if err := robot.PrintTriage(robot.TriageOptions{Limit: robotTriageLimit}); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotForecast != "" {
			if err := robot.PrintForecast(robotForecast); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotSuggest {
			if err := robot.PrintSuggest(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotImpact != "" {
			if err := robot.PrintImpact(robotImpact); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotSearch != "" {
			if err := robot.PrintSearch(robotSearch); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotLabelAttention {
			opts := robot.LabelAttentionOptions{Limit: robotAttentionLimit}
			if err := robot.PrintLabelAttention(opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotLabelFlow {
			if err := robot.PrintLabelFlow(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotLabelHealth {
			if err := robot.PrintLabelHealth(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotFileBeads != "" {
			opts := robot.FileBeadsOptions{FilePath: robotFileBeads, Limit: robotFileBeadsLimit}
			if err := robot.PrintFileBeads(opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotFileHotspots {
			opts := robot.FileHotspotsOptions{Limit: robotHotspotsLimit}
			if err := robot.PrintFileHotspots(opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotFileRelations != "" {
			opts := robot.FileRelationsOptions{FilePath: robotFileRelations, Limit: robotRelationsLimit, Threshold: robotRelationsThreshold}
			if err := robot.PrintFileRelations(opts); err != nil {
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
		if robotTools {
			if err := robot.PrintTools(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotMail {
			projectKey := GetProjectRoot()
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
		if robotActivity != "" {
			// Parse pane filter (reuse --panes flag)
			var paneFilter []string
			if robotPanes != "" {
				paneFilter = strings.Split(robotPanes, ",")
			}
			// Parse agent types
			var agentTypes []string
			if robotActivityType != "" {
				agentTypes = strings.Split(robotActivityType, ",")
			}
			opts := robot.ActivityOptions{
				Session:    robotActivity,
				Panes:      paneFilter,
				AgentTypes: agentTypes,
			}
			if err := robot.PrintActivity(opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotWait != "" {
			// Parse timeout and poll interval
			timeout, err := time.ParseDuration(robotWaitTimeout)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid timeout '%s': %v\n", robotWaitTimeout, err)
				os.Exit(2)
			}
			poll, err := time.ParseDuration(robotWaitPoll)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid poll interval '%s': %v\n", robotWaitPoll, err)
				os.Exit(2)
			}
			// Parse pane filter
			var paneFilter []int
			if robotWaitPanes != "" {
				for _, p := range strings.Split(robotWaitPanes, ",") {
					idx, err := strconv.Atoi(strings.TrimSpace(p))
					if err != nil {
						fmt.Fprintf(os.Stderr, "Error: invalid pane index '%s': %v\n", p, err)
						os.Exit(2)
					}
					paneFilter = append(paneFilter, idx)
				}
			}
			opts := robot.WaitOptions{
				Session:           robotWait,
				Condition:         robotWaitUntil,
				Timeout:           timeout,
				PollInterval:      poll,
				PaneIndices:       paneFilter,
				AgentType:         robotWaitType,
				WaitForAny:        robotWaitAny,
				ExitOnError:       robotWaitOnError,
				RequireTransition: robotWaitTransition,
			}
			exitCode := robot.PrintWait(opts)
			os.Exit(exitCode)
		}
		if robotRoute != "" {
			// Parse exclude panes
			excludePanes, err := robot.ParseExcludePanes(robotRouteExclude)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(2)
			}
			opts := robot.RouteOptions{
				Session:      robotRoute,
				Strategy:     robot.StrategyName(robotRouteStrategy),
				AgentType:    robotRouteType,
				ExcludePanes: excludePanes,
			}
			exitCode := robot.PrintRoute(opts)
			os.Exit(exitCode)
		}
		// Robot-pipeline commands
		if robotPipelineRun != "" {
			vars, err := pipeline.ParsePipelineVars(robotPipelineVars)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(2)
			}
			opts := pipeline.PipelineRunOptions{
				WorkflowFile: robotPipelineRun,
				Session:      robotPipelineSession,
				Variables:    vars,
				DryRun:       robotPipelineDryRun,
				Background:   robotPipelineBG,
			}
			exitCode := pipeline.PrintPipelineRun(opts)
			os.Exit(exitCode)
		}
		if robotPipelineStatus != "" {
			exitCode := pipeline.PrintPipelineStatus(robotPipelineStatus)
			os.Exit(exitCode)
		}
		if robotPipelineList {
			exitCode := pipeline.PrintPipelineList()
			os.Exit(exitCode)
		}
		if robotPipelineCancel != "" {
			exitCode := pipeline.PrintPipelineCancel(robotPipelineCancel)
			os.Exit(exitCode)
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
		if robotIsWorking != "" {
			// Parse pane filter
			panes, err := robot.ParsePanesArg(robotPanes)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid --panes: %v\n", err)
				os.Exit(3)
			}
			opts := robot.IsWorkingOptions{
				Session:       robotIsWorking,
				Panes:         panes,
				LinesCaptured: robotLines,
				Verbose:       robotIsWorkingVerbose,
			}
			if err := robot.PrintIsWorking(opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotAgentHealth != "" {
			// Parse pane filter
			panes, err := robot.ParsePanesArg(robotPanes)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid --panes: %v\n", err)
				os.Exit(3)
			}
			opts := robot.AgentHealthOptions{
				Session:       robotAgentHealth,
				Panes:         panes,
				LinesCaptured: robotLines,
				IncludeCaut:   !robotAgentHealthNoCaut,
				Verbose:       robotAgentHealthVerbose,
			}
			if err := robot.PrintAgentHealth(opts); err != nil {
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
		if robotHealth != "" {
			if err := robot.PrintSessionHealth(robotHealth); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotDiagnose != "" {
			if robotDiagnoseBrief {
				if err := robot.PrintDiagnoseBrief(robotDiagnose); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
			} else {
				opts := robot.DiagnoseOptions{
					Session: robotDiagnose,
					Pane:    robotDiagnosePane,
					Fix:     robotDiagnoseFix,
					Brief:   robotDiagnoseBrief,
				}
				if err := robot.PrintDiagnose(opts); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
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
				Session:        robotSpawn,
				CCCount:        robotSpawnCC,
				CodCount:       robotSpawnCod,
				GmiCount:       robotSpawnGmi,
				Preset:         robotSpawnPreset,
				NoUserPane:     robotSpawnNoUser,
				WorkingDir:     robotSpawnDir,
				WaitReady:      robotSpawnWait,
				ReadyTimeout:   int(spawnTimeout.Seconds()),
				DryRun:         robotRestoreDry,
				Safety:         robotSpawnSafety,
				AssignWork:     robotSpawnAssignWork,
				AssignStrategy: robotSpawnStrategy,
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
		if robotRestartPane != "" {
			// Parse pane filter (reuse --panes flag)
			var paneFilter []string
			if robotPanes != "" {
				paneFilter = strings.Split(robotPanes, ",")
			}
			opts := robot.RestartPaneOptions{
				Session: robotRestartPane,
				Panes:   paneFilter,
				Type:    robotSendType,
				All:     robotSendAll,
				DryRun:  robotRestoreDry,
			}
			if err := robot.PrintRestartPane(opts); err != nil {
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

		// TUI Parity robot handlers - expose TUI functionality to AI agents
		if robotFiles != "" {
			opts := robot.FilesOptions{
				Session:    robotFiles,
				TimeWindow: robotFilesWindow,
				Limit:      robotFilesLimit,
			}
			if err := robot.PrintFiles(opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotInspectPane != "" {
			opts := robot.InspectPaneOptions{
				Session:     robotInspectPane,
				PaneIndex:   robotInspectIndex,
				Lines:       robotInspectLines,
				IncludeCode: robotInspectCode,
			}
			if err := robot.PrintInspectPane(opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotMetrics != "" {
			opts := robot.MetricsOptions{
				Session: robotMetrics,
				Period:  robotMetricsPeriod,
			}
			if err := robot.PrintMetrics(opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotReplay != "" {
			opts := robot.ReplayOptions{
				Session:   robotReplay,
				HistoryID: robotReplayID,
				DryRun:    robotReplayDryRun,
			}
			if err := robot.PrintReplay(opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotPaletteInfo {
			opts := robot.PaletteOptions{
				Session:     robotPaletteSession,
				Category:    robotPaletteCategory,
				SearchQuery: robotPaletteSearch,
			}
			if err := robot.PrintPalette(cfg, opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotDismissAlert != "" {
			opts := robot.DismissAlertOptions{
				AlertID:    robotDismissAlert,
				Session:    robotDismissSession,
				DismissAll: robotDismissAll,
			}
			if err := robot.PrintDismissAlert(opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}

		// Robot-diff handler for comparing agent activity (synthesis)
		if robotDiff != "" {
			// Parse duration
			since, err := util.ParseDurationWithDefault(robotDiffSince, time.Minute, "diff-since")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid --diff-since: %v\n", err)
				os.Exit(1)
			}
			opts := robot.DiffOptions{
				Session: robotDiff,
				Since:   since,
			}
			if err := robot.PrintDiff(opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}

		// Robot-alerts handler for alert listing (TUI parity)
		if robotAlerts {
			opts := robot.TUIAlertsOptions{
				Session:  robotAlertsSession,
				Severity: robotAlertsSeverity,
				Type:     robotAlertsType,
			}
			if err := robot.PrintAlertsTUI(cfg, opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}

		// Robot-beads-list handler for bead listing (TUI parity)
		if robotBeadsList {
			opts := robot.BeadsListOptions{
				Status:   robotBeadsStatus,
				Priority: robotBeadsPriority,
				Assignee: robotBeadsAssignee,
				Type:     robotBeadsType,
				Limit:    robotBeadsLimit,
			}
			if err := robot.PrintBeadsList(opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}

		// Robot-bead handlers for bead management
		if robotBeadClaim != "" {
			opts := robot.BeadClaimOptions{
				BeadID:   robotBeadClaim,
				Assignee: beadAssignee,
			}
			if err := robot.PrintBeadClaim(opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotBeadCreate {
			var labels, dependsOn []string
			if beadLabels != "" {
				labels = strings.Split(beadLabels, ",")
			}
			if beadDependsOn != "" {
				dependsOn = strings.Split(beadDependsOn, ",")
			}
			opts := robot.BeadCreateOptions{
				Title:       beadTitle,
				Type:        beadType,
				Priority:    beadPriority,
				Description: beadDescription,
				Labels:      labels,
				DependsOn:   dependsOn,
			}
			if err := robot.PrintBeadCreate(opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if robotBeadShow != "" {
			opts := robot.BeadShowOptions{
				BeadID: robotBeadShow,
			}
			if err := robot.PrintBeadShow(opts); err != nil {
				// RobotError already outputs JSON-formatted error to stdout
				os.Exit(1)
			}
			return
		}
		if robotBeadClose != "" {
			opts := robot.BeadCloseOptions{
				BeadID: robotBeadClose,
				Reason: beadCloseReason,
			}
			if err := robot.PrintBeadClose(opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}

		// Robot-summary handler for session activity summary
		if robotSummary != "" {
			since, err := util.ParseDurationWithDefault(robotSummarySince, time.Minute, "summary-since")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid --summary-since: %v\n", err)
				os.Exit(1)
			}
			opts := robot.SummaryOptions{
				Session: robotSummary,
				Since:   since,
			}
			if err := robot.PrintSummary(opts); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}

		// Show help with appropriate verbosity when run without subcommand
		if helpMinimal {
			PrintMinimalHelp(cmd.OutOrStdout())
		} else {
			// Default to full help (current stunning help)
			PrintStunningHelp(cmd.OutOrStdout())
		}
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
	robotHelp         bool
	robotStatus       bool
	robotVersion      bool
	robotCapabilities bool
	robotPlan         bool
	robotSnapshot     bool   // unified state query
	robotSince        string // ISO8601 timestamp for delta snapshot
	robotTail         string // session name for tail
	robotLines        int    // number of lines to capture
	robotPanes        string // comma-separated pane filter
	robotGraph        bool   // bv insights passthrough
	robotBeadLimit    int    // limit for ready/in-progress beads in snapshot
	robotDashboard    bool   // dashboard summary output
	robotContext      string // session name for context usage

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
	robotHealth string // session health or project health (empty = project)

	// Robot-diagnose flags
	robotDiagnose      string // session name for comprehensive diagnosis
	robotDiagnoseFix   bool   // attempt auto-fix
	robotDiagnoseBrief bool   // minimal output mode
	robotDiagnosePane  int    // specific pane to diagnose (-1 = all)

	// Robot-recipes flag
	robotRecipes bool // list available recipes as JSON

	// Robot-schema flag
	robotSchema string // schema type to generate

	// Robot-mail flag
	robotMail bool // Agent Mail state output

	// Robot-tools flag for tool inventory
	robotTools bool // tool inventory and health output

	// Robot-ack flags for send confirmation tracking
	robotAck        string // session name for ack
	robotAckTimeout string // timeout (e.g., "30s", "5000ms")
	robotAckPoll    int    // poll interval in milliseconds
	robotAckTrack   bool   // combined send+ack mode

	// Robot-spawn flags for structured session creation
	robotSpawn           string // session name for spawn
	robotSpawnCC         int    // number of Claude agents
	robotSpawnCod        int    // number of Codex agents
	robotSpawnGmi        int    // number of Gemini agents
	robotSpawnPreset     string // recipe/preset name
	robotSpawnNoUser     bool   // don't create user pane
	robotSpawnWait       bool   // wait for agents to be ready
	robotSpawnTimeout    string // timeout for ready detection (e.g., "30s", "1m")
	robotSpawnSafety     bool   // fail if session already exists
	robotSpawnDir        string // working directory override
	robotSpawnAssignWork bool   // enable orchestrator work assignment mode
	robotSpawnStrategy   string // assignment strategy: top-n, diverse, dependency-aware, skill-matched

	// Robot-interrupt flags for priority course correction
	robotInterrupt        string // session name for interrupt
	robotInterruptMsg     string // message to send after interrupt
	robotInterruptAll     bool   // include all panes (including user)
	robotInterruptForce   bool   // send Ctrl+C even if agent appears idle
	robotInterruptNoWait  bool   // don't wait for ready state
	robotInterruptTimeout string // timeout for ready state (e.g., "10s", "5000ms")

	// Robot-terse flag for ultra-compact output
	robotTerse bool // single-line encoded state

	// Robot-format flag for output serialization format
	robotFormat    string // json, toon, or auto
	robotVerbosity string // terse, default, or debug

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
	robotTokensSession string // filter to session
	robotTokensAgent   string // filter to agent type

	// Robot-history flags for command history tracking
	robotHistory      string // session name for history query
	robotHistoryPane  string // filter by pane ID
	robotHistoryType  string // filter by agent type
	robotHistoryLast  int    // last N entries
	robotHistorySince string // time-based filter
	robotHistoryStats bool   // show statistics instead of entries

	// Robot-activity flags for agent activity detection
	robotActivity     string // session name for activity query
	robotActivityType string // filter by agent type (claude, codex, gemini)

	// Robot-wait flags for waiting on agent states
	robotWait           string // session name for wait
	robotWaitUntil      string // wait condition: idle, complete, generating, healthy
	robotWaitTimeout    string // timeout (e.g., "30s", "5m")
	robotWaitPoll       string // poll interval (e.g., "2s", "500ms")
	robotWaitPanes      string // comma-separated pane indices
	robotWaitType       string // filter by agent type
	robotWaitAny        bool   // wait for ANY agent (vs ALL)
	robotWaitOnError    bool   // exit immediately on error state
	robotWaitTransition bool   // require state transition before returning

	// Robot-route flags for routing recommendations
	robotRoute         string // session name for route
	robotRouteStrategy string // routing strategy (least-loaded, first-available, round-robin, etc.)
	robotRouteType     string // filter by agent type (claude, codex, gemini)
	robotRouteExclude  string // comma-separated pane indices to exclude

	// Robot-pipeline flags for workflow execution
	robotPipelineRun     string // workflow file to run
	robotPipelineStatus  string // run ID to check status
	robotPipelineList    bool   // list all pipelines
	robotPipelineCancel  string // run ID to cancel
	robotPipelineSession string // session name for pipeline execution
	robotPipelineVars    string // JSON variables for pipeline
	robotPipelineDryRun  bool   // validate without executing
	robotPipelineBG      bool   // run in background

	// TUI Parity robot flags - expose TUI dashboard functionality to AI agents
	robotFiles           string // session name for file changes query
	robotFilesWindow     string // time window: 5m, 15m, 1h, all (default: 15m)
	robotFilesLimit      int    // max changes to return
	robotInspectPane     string // session name for pane inspection
	robotInspectIndex    int    // pane index to inspect
	robotInspectLines    int    // lines to capture for inspection
	robotInspectCode     bool   // parse code blocks in output
	robotMetrics         string // session name for metrics
	robotMetricsPeriod   string // period: 1h, 24h, 7d, all
	robotReplay          string // session name for replay
	robotReplayID        string // history entry ID to replay
	robotReplayDryRun    bool   // just show what would be replayed
	robotPaletteInfo     bool   // query palette information
	robotPaletteSession  string // filter to session
	robotPaletteCategory string // filter by category
	robotPaletteSearch   string // search query
	robotDismissAlert    string // alert ID to dismiss
	robotDismissSession  string // session scope for alert dismissal
	robotDismissAll      bool   // dismiss all matching alerts

	// Robot-diff flags for comparing agent activity
	robotDiff      string // session name for diff
	robotDiffSince string // duration like "10m", "1h"

	// Robot-alerts flags for alert listing
	robotAlerts         bool   // list alerts
	robotAlertsSeverity string // filter by severity
	robotAlertsType     string // filter by alert type
	robotAlertsSession  string // filter by session

	// Robot-beads-list flags for bead listing
	robotBeadsList     bool   // list beads
	robotBeadsStatus   string // filter by status: open, in_progress, closed, blocked
	robotBeadsPriority string // filter by priority: 0-4 or P0-P4
	robotBeadsAssignee string // filter by assignee
	robotBeadsType     string // filter by type: task, bug, feature, epic, chore
	robotBeadsLimit    int    // max beads to return

	// Robot-bead flags for programmatic bead management
	robotBeadClaim  string // bead ID to claim
	robotBeadCreate bool   // create a new bead
	robotBeadShow   string // bead ID to show details
	robotBeadClose  string // bead ID to close
	beadTitle       string // title for new bead
	beadType        string // type: task, bug, feature, epic, chore
	beadPriority    int    // priority: 0-4
	beadDescription string // description for new bead
	beadLabels      string // comma-separated labels
	beadDependsOn   string // comma-separated dependency IDs
	beadAssignee    string // assignee for claim
	beadCloseReason string // reason for closing

	// Robot-summary flags for session summary
	robotSummary      string // session name for summary
	robotSummarySince string // duration like "30m", "1h"

	// Robot-triage flag for direct bv triage integration
	robotTriage      bool // bv triage output
	robotTriageLimit int  // max recommendations to return

	// BV Analysis robot flags for advanced analysis modes
	robotForecast string // bv forecast analysis target (all or specific ID)
	robotSuggest  bool   // bv hygiene suggestions
	robotImpact   string // file impact analysis target
	robotSearch   string // semantic vector search query

	// BV Label robot flags for label-based analysis
	robotLabelAttention bool // attention-ranked labels by impact and urgency
	robotLabelFlow      bool // cross-label dependency flow matrix
	robotLabelHealth    bool // per-label health analysis
	robotAttentionLimit int  // max attention items to return

	// BV File robot flags for file-based analysis
	robotFileBeads          string  // beads that touched a file path
	robotFileHotspots       bool    // files touched by most beads
	robotFileRelations      string  // files that co-change with given file
	robotFileBeadsLimit     int     // max beads to return per file
	robotHotspotsLimit      int     // max hotspots to return
	robotRelationsLimit     int     // max related files to return
	robotRelationsThreshold float64 // correlation threshold (0.0-1.0)

	// Robot-restart-pane flag
	robotRestartPane string // session name for pane restart

	// Robot-is-working flags for agent work state detection (bd-16ptx)
	robotIsWorking        string // session name to check
	robotIsWorkingVerbose bool   // include raw sample output

	// Robot-agent-health flags for comprehensive health check (bd-2pwzf)
	robotAgentHealth        string // session name to check
	robotAgentHealthNoCaut  bool   // skip caut provider query
	robotAgentHealthVerbose bool   // include raw sample output

	// Help verbosity flags
	helpMinimal bool // show minimal help with essential commands only
	helpFull    bool // show full help (default behavior)
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
	rootCmd.Flags().BoolVar(&robotCapabilities, "robot-capabilities", false, "Get all available robot commands with parameters and descriptions (JSON). Machine-discoverable API")
	rootCmd.Flags().BoolVar(&robotPlan, "robot-plan", false, "Get bv execution plan with parallelizable tracks (JSON). Example: ntm --robot-plan")
	rootCmd.Flags().BoolVar(&robotSnapshot, "robot-snapshot", false, "Unified state: sessions + beads + alerts + mail. Use --since for delta. Example: ntm --robot-snapshot")
	rootCmd.Flags().StringVar(&robotSince, "since", "", "RFC3339 timestamp for delta snapshot. Optional with --robot-snapshot. Example: --since=2025-12-15T10:00:00Z")
	rootCmd.Flags().StringVar(&robotTail, "robot-tail", "", "Capture recent pane output. Required: SESSION. Example: ntm --robot-tail=myproject --lines=50")
	rootCmd.Flags().IntVar(&robotLines, "lines", 20, "Lines to capture per pane. Optional with --robot-tail. Example: --lines=100")
	rootCmd.Flags().StringVar(&robotPanes, "panes", "", "Filter to specific pane indices. Optional with --robot-tail, --robot-send, --robot-ack, --robot-interrupt, --robot-is-working. Example: --panes=1,2")
	rootCmd.Flags().StringVar(&robotIsWorking, "robot-is-working", "", "Check if agents are working. Returns work state with recommendations. Required: SESSION. Example: ntm --robot-is-working=myproject --panes=2,3")
	rootCmd.Flags().BoolVar(&robotIsWorkingVerbose, "is-working-verbose", false, "Include raw sample output in --robot-is-working response. Example: --is-working-verbose")
	rootCmd.Flags().StringVar(&robotAgentHealth, "robot-agent-health", "", "Comprehensive agent health check combining local state and provider usage. Required: SESSION. Example: ntm --robot-agent-health=myproject --panes=2,3")
	rootCmd.Flags().BoolVar(&robotAgentHealthNoCaut, "no-caut", false, "Skip caut provider query for faster local-only health check. Optional with --robot-agent-health")
	rootCmd.Flags().BoolVar(&robotAgentHealthVerbose, "agent-health-verbose", false, "Include raw sample output in --robot-agent-health response. Example: --agent-health-verbose")
	rootCmd.Flags().BoolVar(&robotGraph, "robot-graph", false, "Get bv dependency graph insights: PageRank, critical path, cycles (JSON)")
	rootCmd.Flags().BoolVar(&robotTriage, "robot-triage", false, "Get bv triage analysis with recommendations, quick wins, blockers (JSON). Example: ntm --robot-triage --triage-limit=20")
	rootCmd.Flags().IntVar(&robotTriageLimit, "triage-limit", 10, "Max recommendations per category. Optional with --robot-triage. Example: --triage-limit=20")
	rootCmd.Flags().BoolVar(&robotDashboard, "robot-dashboard", false, "Get dashboard summary as markdown (or JSON with --json). Token-efficient overview")
	rootCmd.Flags().StringVar(&robotContext, "robot-context", "", "Get context window usage for all agents in a session. Required: SESSION. Example: ntm --robot-context=myproject")
	rootCmd.Flags().IntVar(&robotBeadLimit, "bead-limit", 5, "Max beads per category in snapshot. Optional with --robot-snapshot, --robot-status. Example: --bead-limit=10")
	rootCmd.Flags().StringVar(&robotVerbosity, "robot-verbosity", "", "Robot verbosity profile for JSON/TOON: terse, default, or debug. Env: NTM_ROBOT_VERBOSITY")

	// BV Analysis robot flags for advanced analysis modes
	rootCmd.Flags().StringVar(&robotForecast, "robot-forecast", "", "Get ETA predictions. Use 'all' or specific ID. Example: ntm --robot-forecast=br-123")
	rootCmd.Flags().BoolVar(&robotSuggest, "robot-suggest", false, "Get hygiene suggestions: duplicates, missing deps, label suggestions (JSON)")
	rootCmd.Flags().StringVar(&robotImpact, "robot-impact", "", "Get file impact analysis. Required: FILE_PATH. Example: ntm --robot-impact=src/main.go")
	rootCmd.Flags().StringVar(&robotSearch, "robot-search", "", "Semantic vector search. Required: QUERY. Example: ntm --robot-search='authentication bug'")

	// BV Label robot flags for label-based analysis
	rootCmd.Flags().BoolVar(&robotLabelAttention, "robot-label-attention", false, "Get attention-ranked labels by impact and urgency (JSON)")
	rootCmd.Flags().IntVar(&robotAttentionLimit, "attention-limit", 10, "Max attention items to return. Optional with --robot-label-attention")
	rootCmd.Flags().BoolVar(&robotLabelFlow, "robot-label-flow", false, "Get cross-label dependency flow matrix and bottleneck analysis (JSON)")
	rootCmd.Flags().BoolVar(&robotLabelHealth, "robot-label-health", false, "Get per-label health analysis: velocity, staleness, blocked count (JSON)")

	// BV File robot flags for file-based analysis
	rootCmd.Flags().StringVar(&robotFileBeads, "robot-file-beads", "", "Get beads that touched a file path. Required: FILE_PATH. Example: ntm --robot-file-beads=src/main.go")
	rootCmd.Flags().IntVar(&robotFileBeadsLimit, "file-beads-limit", 20, "Max beads to return per file. Optional with --robot-file-beads")
	rootCmd.Flags().BoolVar(&robotFileHotspots, "robot-file-hotspots", false, "Get files touched by most beads (quality hotspots) (JSON)")
	rootCmd.Flags().IntVar(&robotHotspotsLimit, "hotspots-limit", 10, "Max hotspots to return. Optional with --robot-file-hotspots")
	rootCmd.Flags().StringVar(&robotFileRelations, "robot-file-relations", "", "Get files that co-change with given file. Required: FILE_PATH. Example: ntm --robot-file-relations=src/api.go")
	rootCmd.Flags().IntVar(&robotRelationsLimit, "relations-limit", 10, "Max related files to return. Optional with --robot-file-relations")
	rootCmd.Flags().Float64Var(&robotRelationsThreshold, "relations-threshold", 0.5, "Correlation threshold (0.0-1.0). Optional with --robot-file-relations")

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

	// Robot-health flag for session/project health summary
	rootCmd.Flags().StringVar(&robotHealth, "robot-health", "", "Get session or project health (JSON). SESSION for per-agent health, empty for project health. Example: ntm --robot-health=myproject")

	// Robot-diagnose flags for comprehensive health diagnosis
	rootCmd.Flags().StringVar(&robotDiagnose, "robot-diagnose", "", "Comprehensive health check with fix recommendations. Required: SESSION. Example: ntm --robot-diagnose=myproject")
	rootCmd.Flags().BoolVar(&robotDiagnoseFix, "diagnose-fix", false, "Attempt auto-fix for fixable issues. Use with --robot-diagnose. Example: --robot-diagnose=proj --diagnose-fix")
	rootCmd.Flags().BoolVar(&robotDiagnoseBrief, "diagnose-brief", false, "Minimal output (summary only). Use with --robot-diagnose")
	rootCmd.Flags().IntVar(&robotDiagnosePane, "diagnose-pane", -1, "Diagnose specific pane only. Use with --robot-diagnose. Example: --diagnose-pane=2")

	// Robot-recipes flag for recipe listing
	rootCmd.Flags().BoolVar(&robotRecipes, "robot-recipes", false, "List available spawn recipes/presets (JSON). Use with --robot-spawn --spawn-preset")

	// Robot-schema flag for JSON Schema generation
	rootCmd.Flags().StringVar(&robotSchema, "robot-schema", "", "Generate JSON Schema for response types. Required: TYPE (status, send, spawn, interrupt, tail, ack, snapshot, all)")

	// Robot-mail flag for Agent Mail state
	rootCmd.Flags().BoolVar(&robotMail, "robot-mail", false, "Get Agent Mail inbox/outbox state (JSON). Shows pending messages and coordination status")

	// Robot-tools flag for tool inventory and health
	rootCmd.Flags().BoolVar(&robotTools, "robot-tools", false, "Get tool inventory with health status (JSON). Shows all registered flywheel tools")

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
	rootCmd.Flags().StringVar(&robotSpawnTimeout, "ready-timeout", "30s", "Alias for --spawn-timeout. Max wait for agent ready state. Use with --spawn-wait")
	rootCmd.Flags().BoolVar(&robotSpawnSafety, "spawn-safety", false, "Fail if session already exists. Prevents accidental reuse of existing sessions")
	rootCmd.Flags().StringVar(&robotSpawnDir, "spawn-dir", "", "Working directory for spawned session. Use with --robot-spawn. Example: --spawn-dir=/path/to/project")
	rootCmd.Flags().BoolVar(&robotSpawnAssignWork, "spawn-assign-work", false, "Enable orchestrator work assignment: get bv triage, claim beads, send work prompts to agents")
	rootCmd.Flags().StringVar(&robotSpawnStrategy, "spawn-assign-strategy", "top-n", "Work assignment strategy (use with --spawn-assign-work). Values: top-n, diverse, dependency-aware, skill-matched")

	// Robot-interrupt flags for priority course correction
	rootCmd.Flags().StringVar(&robotInterrupt, "robot-interrupt", "", "Send Ctrl+C to stop agents, optionally send new task. Required: SESSION. Example: ntm --robot-interrupt=proj --interrupt-msg='Stop and fix bug'")
	rootCmd.Flags().StringVar(&robotInterruptMsg, "interrupt-msg", "", "New task to send after Ctrl+C. Optional with --robot-interrupt. Agents receive this after stopping")
	rootCmd.Flags().BoolVar(&robotInterruptAll, "interrupt-all", false, "Interrupt all panes including user. Default: agents only. Use with --robot-interrupt")
	rootCmd.Flags().BoolVar(&robotInterruptForce, "interrupt-force", false, "Send Ctrl+C even if agent shows idle/ready. Use for stuck agents")
	rootCmd.Flags().BoolVar(&robotInterruptNoWait, "interrupt-no-wait", false, "Return immediately after Ctrl+C without waiting for ready state")
	rootCmd.Flags().StringVar(&robotInterruptTimeout, "interrupt-timeout", "10s", "Max wait for ready state after interrupt (e.g., 10s, 5000ms). Ignored with --interrupt-no-wait")

	// Robot-restart-pane flag
	rootCmd.Flags().StringVar(&robotRestartPane, "robot-restart-pane", "", "Restart pane process (kill and respawn). Required: SESSION. Example: ntm --robot-restart-pane=proj --panes=1,2")

	// Robot-terse flag for ultra-compact output
	rootCmd.Flags().BoolVar(&robotTerse, "robot-terse", false, "Single-line state: S:session|A:ready/total|W:working|I:idle|B:beads|M:mail|!:alerts. Minimal tokens")

	// Robot-format flag for output serialization format
	rootCmd.Flags().StringVar(&robotFormat, "robot-format", "", "Output format for robot commands: json (default), toon (token-efficient), or auto. Env: NTM_ROBOT_FORMAT")

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

	// Robot-activity flags for agent activity detection
	rootCmd.Flags().StringVar(&robotActivity, "robot-activity", "", "Get agent activity state (idle/busy/error). Required: SESSION. Example: ntm --robot-activity=myproject")
	rootCmd.Flags().StringVar(&robotActivityType, "activity-type", "", "Filter by agent type: claude, codex, gemini. Optional with --robot-activity. Example: --activity-type=claude")

	// Robot-wait flags for waiting on agent states
	rootCmd.Flags().StringVar(&robotWait, "robot-wait", "", "Wait for agents to reach state. Required: SESSION. Example: ntm --robot-wait=myproject --wait-until=idle")
	rootCmd.Flags().StringVar(&robotWaitUntil, "wait-until", "idle", "Wait condition: idle, complete, generating, healthy. Optional with --robot-wait. Example: --wait-until=idle")
	rootCmd.Flags().StringVar(&robotWaitUntil, "condition", "idle", "Alias for --wait-until. Wait condition: idle, complete, generating, healthy")
	rootCmd.Flags().StringVar(&robotWaitTimeout, "wait-timeout", "5m", "Maximum wait time. Optional with --robot-wait. Example: --wait-timeout=2m")
	rootCmd.Flags().StringVar(&robotWaitPoll, "wait-poll", "2s", "Polling interval. Optional with --robot-wait. Example: --wait-poll=500ms")
	rootCmd.Flags().StringVar(&robotWaitPanes, "wait-panes", "", "Comma-separated pane indices. Optional with --robot-wait. Example: --wait-panes=1,2")
	rootCmd.Flags().StringVar(&robotWaitType, "wait-type", "", "Filter by agent type: claude, codex, gemini. Optional with --robot-wait. Example: --wait-type=claude")
	rootCmd.Flags().BoolVar(&robotWaitAny, "wait-any", false, "Wait for ANY agent instead of ALL. Optional with --robot-wait")
	rootCmd.Flags().BoolVar(&robotWaitOnError, "wait-exit-on-error", false, "Exit immediately if ERROR state detected. Optional with --robot-wait")
	rootCmd.Flags().BoolVar(&robotWaitOnError, "exit-on-error", false, "Alias for --wait-exit-on-error. Exit immediately if ERROR state detected")
	rootCmd.Flags().BoolVar(&robotWaitTransition, "wait-transition", false, "Require state transition: agents must leave then return to target state. Use after sending prompts to wait for complete processing cycle. Optional with --robot-wait")
	rootCmd.Flags().BoolVar(&robotWaitTransition, "transition", false, "Alias for --wait-transition")

	// Robot-route flags for routing recommendations
	rootCmd.Flags().StringVar(&robotRoute, "robot-route", "", "Get routing recommendation. Required: SESSION. Example: ntm --robot-route=myproject --route-strategy=least-loaded")
	rootCmd.Flags().StringVar(&robotRouteStrategy, "route-strategy", "least-loaded", "Routing strategy: least-loaded, first-available, round-robin, round-robin-available, random, sticky, explicit. Optional with --robot-route")
	rootCmd.Flags().StringVar(&robotRouteType, "route-type", "", "Filter by agent type: claude, codex, gemini. Optional with --robot-route. Example: --route-type=claude")
	rootCmd.Flags().StringVar(&robotRouteExclude, "route-exclude", "", "Exclude pane indices (comma-separated). Optional with --robot-route. Example: --route-exclude=0,3")

	// Robot-pipeline flags for workflow execution
	rootCmd.Flags().StringVar(&robotPipelineRun, "robot-pipeline-run", "", "Run a workflow. Required: WORKFLOW_FILE, --pipeline-session. Example: ntm --robot-pipeline-run=workflow.yaml --pipeline-session=proj")
	rootCmd.Flags().StringVar(&robotPipelineStatus, "robot-pipeline", "", "Get pipeline status. Required: RUN_ID. Example: ntm --robot-pipeline=run-20241230-123456-abcd")
	rootCmd.Flags().BoolVar(&robotPipelineList, "robot-pipeline-list", false, "List all tracked pipelines. Example: ntm --robot-pipeline-list")
	rootCmd.Flags().StringVar(&robotPipelineCancel, "robot-pipeline-cancel", "", "Cancel a running pipeline. Required: RUN_ID. Example: ntm --robot-pipeline-cancel=run-20241230-123456-abcd")
	rootCmd.Flags().StringVar(&robotPipelineSession, "pipeline-session", "", "Tmux session for pipeline execution. Required with --robot-pipeline-run. Example: --pipeline-session=myproject")
	rootCmd.Flags().StringVar(&robotPipelineVars, "pipeline-vars", "", "JSON variables for pipeline. Optional with --robot-pipeline-run. Example: --pipeline-vars='{\"env\":\"prod\"}'")
	rootCmd.Flags().BoolVar(&robotPipelineDryRun, "pipeline-dry-run", false, "Validate workflow without executing. Optional with --robot-pipeline-run")
	rootCmd.Flags().BoolVar(&robotPipelineBG, "pipeline-background", false, "Run pipeline in background. Optional with --robot-pipeline-run")

	// TUI Parity robot flags - expose TUI dashboard functionality to AI agents
	rootCmd.Flags().StringVar(&robotFiles, "robot-files", "", "Get file changes with agent attribution. Optional SESSION filter. Example: ntm --robot-files=myproject --files-window=15m")
	rootCmd.Flags().StringVar(&robotFilesWindow, "files-window", "15m", "Time window: 5m, 15m, 1h, all. Optional with --robot-files. Example: --files-window=1h")
	rootCmd.Flags().IntVar(&robotFilesLimit, "files-limit", 100, "Max changes to return. Optional with --robot-files. Example: --files-limit=50")

	rootCmd.Flags().StringVar(&robotInspectPane, "robot-inspect-pane", "", "Detailed pane inspection. Required: SESSION. Example: ntm --robot-inspect-pane=myproject --inspect-index=1")
	rootCmd.Flags().IntVar(&robotInspectIndex, "inspect-index", 0, "Pane index to inspect. Optional with --robot-inspect-pane. Example: --inspect-index=2")
	rootCmd.Flags().IntVar(&robotInspectLines, "inspect-lines", 100, "Lines to capture. Optional with --robot-inspect-pane. Example: --inspect-lines=200")
	rootCmd.Flags().BoolVar(&robotInspectCode, "inspect-code", false, "Parse code blocks from output. Optional with --robot-inspect-pane")

	rootCmd.Flags().StringVar(&robotMetrics, "robot-metrics", "", "Session metrics export. Optional SESSION. Example: ntm --robot-metrics=myproject --metrics-period=24h")
	rootCmd.Flags().StringVar(&robotMetricsPeriod, "metrics-period", "24h", "Period: 1h, 24h, 7d, all. Optional with --robot-metrics. Example: --metrics-period=7d")

	rootCmd.Flags().StringVar(&robotReplay, "robot-replay", "", "Replay command from history. Required: SESSION. Use with --replay-id. Example: ntm --robot-replay=myproject --replay-id=1735830245123-a1b2c3d4")
	rootCmd.Flags().StringVar(&robotReplayID, "replay-id", "", "History entry ID to replay. Required with --robot-replay. Get IDs from --robot-history")
	rootCmd.Flags().BoolVar(&robotReplayDryRun, "replay-dry-run", false, "Preview replay without executing. Optional with --robot-replay")

	rootCmd.Flags().BoolVar(&robotPaletteInfo, "robot-palette", false, "Query palette commands. Example: ntm --robot-palette --palette-category=quick")
	rootCmd.Flags().StringVar(&robotPaletteSession, "palette-session", "", "Filter recents to session. Optional with --robot-palette")
	rootCmd.Flags().StringVar(&robotPaletteCategory, "palette-category", "", "Filter by category. Optional with --robot-palette. Example: --palette-category=code_quality")
	rootCmd.Flags().StringVar(&robotPaletteSearch, "palette-search", "", "Search commands. Optional with --robot-palette. Example: --palette-search=test")

	rootCmd.Flags().StringVar(&robotDismissAlert, "robot-dismiss-alert", "", "Dismiss an alert by ID. Example: ntm --robot-dismiss-alert=alert-abc123")
	rootCmd.Flags().StringVar(&robotDismissSession, "dismiss-session", "", "Scope dismissal to session. Optional with --robot-dismiss-alert")
	rootCmd.Flags().BoolVar(&robotDismissAll, "dismiss-all", false, "Dismiss all matching alerts. Optional with --robot-dismiss-alert")

	// Robot-diff flags for comparing agent activity (synthesis)
	rootCmd.Flags().StringVar(&robotDiff, "robot-diff", "", "Compare agent activity and file changes. Required: SESSION. Example: ntm --robot-diff=myproject --diff-since=10m")
	rootCmd.Flags().StringVar(&robotDiffSince, "diff-since", "15m", "Duration to look back (e.g., 10m, 1h). Optional with --robot-diff. Default: 15m")

	// Robot-alerts flags for alert listing (TUI parity)
	rootCmd.Flags().BoolVar(&robotAlerts, "robot-alerts", false, "List active alerts with filtering. TUI parity for Alerts panel. Example: ntm --robot-alerts --alerts-severity=critical")
	rootCmd.Flags().StringVar(&robotAlertsSeverity, "alerts-severity", "", "Filter by severity: info, warning, error, critical. Optional with --robot-alerts")
	rootCmd.Flags().StringVar(&robotAlertsType, "alerts-type", "", "Filter by alert type. Optional with --robot-alerts")
	rootCmd.Flags().StringVar(&robotAlertsSession, "alerts-session", "", "Filter by session. Optional with --robot-alerts")

	// Robot-beads-list flags for bead listing (TUI parity)
	rootCmd.Flags().BoolVar(&robotBeadsList, "robot-beads-list", false, "List beads with filtering. TUI parity for Beads panel. Example: ntm --robot-beads-list --beads-status=open")
	rootCmd.Flags().StringVar(&robotBeadsStatus, "beads-status", "", "Filter by status: open, in_progress, closed, blocked. Optional with --robot-beads-list")
	rootCmd.Flags().StringVar(&robotBeadsPriority, "beads-priority", "", "Filter by priority: 0-4 or P0-P4. Optional with --robot-beads-list")
	rootCmd.Flags().StringVar(&robotBeadsAssignee, "beads-assignee", "", "Filter by assignee. Optional with --robot-beads-list")
	rootCmd.Flags().StringVar(&robotBeadsType, "beads-type", "", "Filter by type: task, bug, feature, epic, chore. Optional with --robot-beads-list")
	rootCmd.Flags().IntVar(&robotBeadsLimit, "beads-limit", 20, "Max beads to return (default 20). Optional with --robot-beads-list")

	// Robot-bead flags for programmatic bead management
	rootCmd.Flags().StringVar(&robotBeadClaim, "robot-bead-claim", "", "Claim a bead by ID. Example: ntm --robot-bead-claim=ntm-abc123")
	rootCmd.Flags().BoolVar(&robotBeadCreate, "robot-bead-create", false, "Create a new bead. Requires --bead-title. Example: ntm --robot-bead-create --bead-title='Fix bug'")
	rootCmd.Flags().StringVar(&robotBeadShow, "robot-bead-show", "", "Show bead details. Example: ntm --robot-bead-show=ntm-abc123")
	rootCmd.Flags().StringVar(&robotBeadClose, "robot-bead-close", "", "Close a bead by ID. Example: ntm --robot-bead-close=ntm-abc123")
	rootCmd.Flags().StringVar(&beadTitle, "bead-title", "", "Title for new bead. Required with --robot-bead-create")
	rootCmd.Flags().StringVar(&beadType, "bead-type", "task", "Type: task, bug, feature, epic, chore. Optional with --robot-bead-create")
	rootCmd.Flags().IntVar(&beadPriority, "bead-priority", 2, "Priority 0-4 (0=critical, 4=backlog). Optional with --robot-bead-create")
	rootCmd.Flags().StringVar(&beadDescription, "bead-description", "", "Description for new bead. Optional with --robot-bead-create")
	rootCmd.Flags().StringVar(&beadLabels, "bead-labels", "", "Comma-separated labels. Optional with --robot-bead-create. Example: --bead-labels=backend,api")
	rootCmd.Flags().StringVar(&beadDependsOn, "bead-depends-on", "", "Comma-separated dependency bead IDs. Optional with --robot-bead-create")
	rootCmd.Flags().StringVar(&beadAssignee, "bead-assignee", "", "Assignee for claim. Optional with --robot-bead-claim")
	rootCmd.Flags().StringVar(&beadCloseReason, "bead-close-reason", "", "Reason for closing. Optional with --robot-bead-close")

	// Robot-summary flags for session activity summary
	rootCmd.Flags().StringVar(&robotSummary, "robot-summary", "", "Get session activity summary with agent metrics. Required: SESSION. Example: ntm --robot-summary=myproject --summary-since=30m")
	rootCmd.Flags().StringVar(&robotSummarySince, "summary-since", "30m", "Duration to look back (e.g., 30m, 1h). Optional with --robot-summary. Default: 30m")

	// Help verbosity flags
	rootCmd.Flags().BoolVar(&helpMinimal, "minimal", false, "Show minimal help with essential commands only (spawn, send, status, kill, help)")
	rootCmd.Flags().BoolVar(&helpFull, "full", false, "Show full help with all commands (default behavior)")

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
		newWaitCmd(),
		newMailCmd(),
		newPluginsCmd(),
		newAgentsCmd(),
		newAssignCmd(),

		// Session navigation
		newAttachCmd(),
		newListCmd(),
		newStatusCmd(),
		newViewCmd(),
		newZoomCmd(),
		newDashboardCmd(),
		newWatchCmd(),
		newGetAllSessionTextCmd(),

		// Output management
		newCopyCmd(),
		newSaveCmd(),
		newGrepCmd(),
		newExtractCmd(),
		newDiffCmd(),
		newChangesCmd(),
		newConflictsCmd(),
		newSummaryCmd(),

		// Session persistence
		newCheckpointCmd(),
		newRollbackCmd(),
		newSessionPersistCmd(),
		newHandoffCmd(),
		newResumeCmd(),

		// Utilities
		newPaletteCmd(),
		newBindCmd(),
		newDepsCmd(),
		newKillCmd(),
		newRespawnCmd(),
		newScanCmd(),
		newBugsCmd(),
		newCassCmd(),
		newHooksCmd(),
		newHealthCmd(),
		newDoctorCmd(),
		newCleanupCmd(),
		newSafetyCmd(),
		newPolicyCmd(),
		newGuardsCmd(),
		newApproveCmd(),
		newServeCmd(),
		newSetupCmd(),
		newActivityCmd(),
		newHistoryCmd(),
		newAnalyticsCmd(),
		newMetricsCmd(),
		newWorkCmd(),

		// Internal commands
		newMonitorCmd(),

		// Memory integration
		newMemoryCmd(),

		// Context pack building
		newContextCmd(),

		// Beads daemon management
		newBeadsCmd(),

		// Project initialization + shell integration
		newInitCmd(),
		newShellCmd(),
		newCompletionCmd(),
		newVersionCmd(),
		newConfigCmd(),
		newUpgradeCmd(),
		newLevelCmd(),

		// Tutorial
		newTutorialCmd(),

		// Agent Mail & File Reservations
		newLockCmd(),
		newUnlockCmd(),
		newLocksCmd(),
		newMessageCmd(),     // Unified messaging
		newCoordinatorCmd(), // Multi-agent coordination

		// Git coordination
		newGitCmd(),
		newWorktreesCmd(),

		// Configuration management
		newRecipesCmd(),
		newWorkflowsCmd(),
		newPersonasCmd(),
		newTemplateCmd(),
		newSessionTemplatesCmd(),
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

	// Add diff subcommand
	cmd.AddCommand(&cobra.Command{
		Use:   "diff",
		Short: "Show configuration differences from defaults",
		Long:  `Shows all configuration values that differ from the built-in defaults.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			effectiveCfg := cfg
			if effectiveCfg == nil {
				loaded, err := config.Load(cfgFile)
				if err != nil {
					loaded = config.Default()
				}
				effectiveCfg = loaded
			}

			diffs := config.Diff(effectiveCfg)

			if IsJSONOutput() {
				return output.PrintJSON(map[string]interface{}{
					"count": len(diffs),
					"diffs": diffs,
				})
			}

			if len(diffs) == 0 {
				fmt.Println("No differences from defaults")
				return nil
			}

			fmt.Printf("Configuration differences (%d):\n\n", len(diffs))
			for _, d := range diffs {
				fmt.Printf("  %s\n", d.Path)
				fmt.Printf("    default: %v\n", d.Default)
				fmt.Printf("    current: %v\n", d.Current)
				fmt.Println()
			}
			return nil
		},
	})

	// Add validate subcommand (comprehensive validation from validate.go)
	cmd.AddCommand(newConfigValidateCmd())

	// Add get subcommand
	cmd.AddCommand(&cobra.Command{
		Use:   "get <key>",
		Short: "Get a configuration value",
		Long: `Retrieves a configuration value by its dotted path.

Examples:
  ntm config get projects_base
  ntm config get alerts.enabled
  ntm config get context_rotation.warning_threshold`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			effectiveCfg := cfg
			if effectiveCfg == nil {
				loaded, err := config.Load(cfgFile)
				if err != nil {
					loaded = config.Default()
				}
				effectiveCfg = loaded
			}

			value, err := config.GetValue(effectiveCfg, args[0])
			if err != nil {
				return err
			}

			if IsJSONOutput() {
				return output.PrintJSON(map[string]interface{}{
					"key":   args[0],
					"value": value,
				})
			}

			fmt.Printf("%v\n", value)
			return nil
		},
	})

	// Add edit subcommand
	cmd.AddCommand(&cobra.Command{
		Use:   "edit",
		Short: "Open configuration file in editor",
		Long:  `Opens the configuration file in your default editor ($EDITOR or vi).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := config.DefaultPath()

			// Ensure config exists
			if _, err := os.Stat(path); os.IsNotExist(err) {
				if _, err := config.CreateDefault(); err != nil {
					return fmt.Errorf("creating config: %w", err)
				}
			}

			editorCmd, err := buildEditorCommand(path)
			if err != nil {
				return err
			}
			editorCmd.Stdin = os.Stdin
			editorCmd.Stdout = os.Stdout
			editorCmd.Stderr = os.Stderr
			return editorCmd.Run()
		},
	})

	// Add reset subcommand
	var resetConfirm bool
	resetCmd := &cobra.Command{
		Use:   "reset",
		Short: "Reset configuration to defaults",
		Long:  `Removes the current configuration file and creates a new one with defaults.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !resetConfirm {
				return fmt.Errorf("use --confirm to reset configuration (this will delete your current config)")
			}

			if err := config.Reset(); err != nil {
				return err
			}

			fmt.Printf("Configuration reset to defaults: %s\n", config.DefaultPath())
			return nil
		},
	}
	resetCmd.Flags().BoolVar(&resetConfirm, "confirm", false, "confirm reset operation")
	cmd.AddCommand(resetCmd)

	projectCmd := &cobra.Command{
		Use:   "project",
		Short: "Manage project-specific configuration",
	}

	var projectInitForce bool
	projectInitCmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize .ntm configuration for current project",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.InitProjectConfig(projectInitForce); err != nil {
				return err
			}

			projectPath, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getting working directory: %w", err)
			}

			configPath := filepath.Join(projectPath, ".ntm", "config.toml")
			registered, warning, err := registerAgentMailProject(projectPath, configPath)
			if err != nil {
				return err
			}
			if warning != "" {
				output.PrintWarningf("Agent Mail: %s", warning)
			} else if registered {
				output.PrintSuccess("Registered project with Agent Mail")
			}

			return nil
		},
	}
	projectInitCmd.Flags().BoolVar(&projectInitForce, "force", false, "overwrite .ntm/config.toml if it already exists")
	projectCmd.AddCommand(projectInitCmd)

	cmd.AddCommand(projectCmd)

	return cmd
}

func buildEditorCommand(path string) (*exec.Cmd, error) {
	editor := strings.TrimSpace(os.Getenv("EDITOR"))
	if editor == "" {
		editor = "vi"
	}

	parts := strings.Fields(editor)
	if len(parts) == 0 || !editorTokensSafe(parts) {
		parts = []string{"vi"}
	}

	cmdPath, err := exec.LookPath(parts[0])
	if err != nil {
		return nil, fmt.Errorf("editor not found: %w", err)
	}

	args := append(parts[1:], path)
	return exec.Command(cmdPath, args...), nil
}

func editorTokensSafe(tokens []string) bool {
	for _, token := range tokens {
		if strings.ContainsAny(token, ";&|<>`$\n\r") {
			return false
		}
	}
	return true
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

// resolveRobotFormat determines the robot output format from CLI flag, env var, or default.
// Priority: --robot-format flag > NTM_ROBOT_FORMAT env var > default (auto)
func resolveRobotFormat() {
	formatStr := robotFormat

	// Fall back to environment variable if flag not set
	if formatStr == "" {
		formatStr = os.Getenv("NTM_ROBOT_FORMAT")
	}

	// Parse and set the format
	if formatStr != "" {
		format, err := robot.ParseRobotFormat(formatStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: %v, using default (auto)\n", err)
			robot.OutputFormat = robot.FormatAuto
			return
		}
		robot.OutputFormat = format
	} else {
		robot.OutputFormat = robot.FormatAuto
	}
}

// resolveRobotVerbosity determines the robot verbosity profile from CLI flag, env var, or config.
// Priority: --robot-verbosity flag > NTM_ROBOT_VERBOSITY env var > config.robot.verbosity > default
func resolveRobotVerbosity(cfg *config.Config) {
	verbosityStr := robotVerbosity

	// Fall back to environment variable if flag not set
	if verbosityStr == "" {
		verbosityStr = os.Getenv("NTM_ROBOT_VERBOSITY")
	}

	// Fall back to config if available
	if verbosityStr == "" && cfg != nil {
		verbosityStr = cfg.Robot.Verbosity
	}

	if verbosityStr == "" {
		robot.OutputVerbosity = robot.VerbosityDefault
		return
	}

	verbosity, err := robot.ParseRobotVerbosity(verbosityStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: %v, using default verbosity\n", err)
		robot.OutputVerbosity = robot.VerbosityDefault
		return
	}
	robot.OutputVerbosity = verbosity
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
		if robotHelp || robotVersion || robotCapabilities {
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
			robotInterrupt != "" || robotRestartPane != "" || robotGraph || robotMail || robotHealth != "" ||
			robotDiagnose != "" || robotTerse || robotMarkdown || robotSave != "" || robotRestore != "" ||
			robotContext != "" || robotAlerts || robotIsWorking != "" || robotAgentHealth != "" {
			return true
		}
	}

	return false
}
