package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/spf13/cobra"
)

var (
	cfgFile string
	cfg     *config.Config

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
		// Skip config loading for init, completion, version, and upgrade commands
		if cmd.Name() == "init" || cmd.Name() == "completion" || cmd.Name() == "version" || cmd.Name() == "upgrade" {
			return nil
		}

		var err error
		cfg, err = config.Load(cfgFile)
		if err != nil {
			// Use defaults if config doesn't exist
			cfg = config.Default()
		}
		return nil
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
				err = robot.PrintSnapshot()
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

		// Show stunning help with gradients when run without subcommand
		PrintStunningHelp()
	},
}

func Execute() error {
	return rootCmd.Execute()
}

// Robot output flags for AI agent integration
var (
	robotHelp     bool
	robotStatus   bool
	robotVersion  bool
	robotPlan     bool
	robotSnapshot bool   // unified state query
	robotSince    string // ISO8601 timestamp for delta snapshot
	robotTail     string // session name for tail
	robotLines    int    // number of lines to capture
	robotPanes    string // comma-separated pane filter
	robotGraph    bool   // bv insights passthrough

	// Robot-send flags
	robotSend        string // session name for send
	robotSendMsg     string // message to send
	robotSendAll     bool   // send to all panes
	robotSendType    string // filter by agent type (e.g., "claude")
	robotSendExclude string // comma-separated panes to exclude
	robotSendDelay   int    // delay between sends in ms
)

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default ~/.config/ntm/config.toml)")

	// Global JSON output flag - applies to all commands
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output in JSON format (machine-readable)")

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

	// Robot-send flags for batch messaging
	rootCmd.Flags().StringVar(&robotSend, "robot-send", "", "Send prompt to panes atomically (JSON output)")
	rootCmd.Flags().StringVar(&robotSendMsg, "msg", "", "Message to send (used with --robot-send)")
	rootCmd.Flags().BoolVar(&robotSendAll, "all", false, "Send to all panes including user (used with --robot-send)")
	rootCmd.Flags().StringVar(&robotSendType, "type", "", "Filter by agent type: claude, codex, gemini (used with --robot-send)")
	rootCmd.Flags().StringVar(&robotSendExclude, "exclude", "", "Comma-separated pane indices to exclude (used with --robot-send)")
	rootCmd.Flags().IntVar(&robotSendDelay, "delay-ms", 0, "Delay between sends in milliseconds (used with --robot-send)")

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
		newInterruptCmd(),

		// Session navigation
		newAttachCmd(),
		newListCmd(),
		newStatusCmd(),
		newViewCmd(),
		newZoomCmd(),
		newDashboardCmd(),

		// Output management
		newCopyCmd(),
		newSaveCmd(),

		// Utilities
		newPaletteCmd(),
		newBindCmd(),
		newDepsCmd(),
		newKillCmd(),
		newScanCmd(),

		// Shell integration
		newInitCmd(),
		newCompletionCmd(),
		newVersionCmd(),
		newConfigCmd(),
		newUpgradeCmd(),

		// Tutorial
		newTutorialCmd(),
	)
}

func newVersionCmd() *cobra.Command {
	var short bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			if short {
				fmt.Println(Version)
				return
			}
			fmt.Printf("ntm version %s\n", Version)
			fmt.Printf("  commit:  %s\n", Commit)
			fmt.Printf("  built:   %s\n", Date)
			fmt.Printf("  builder: %s\n", BuiltBy)
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

	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Show current configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load("")
			if err != nil {
				cfg = config.Default()
				fmt.Println("# Using default configuration (no config file found)")
				fmt.Println()
			}
			return config.Print(cfg, os.Stdout)
		},
	})

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

