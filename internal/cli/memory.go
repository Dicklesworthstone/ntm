package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/cm"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/supervisor"
)

func newMemoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "memory",
		Short: "Interact with CASS Memory (cm) system",
	}

	cmd.AddCommand(
		newMemoryServeCmd(),
		newMemoryContextCmd(),
		newMemoryOutcomeCmd(),
		newMemoryPrivacyCmd(),
	)

	return cmd
}

func newMemoryServeCmd() *cobra.Command {
	var port int
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the CASS Memory (cm) daemon under NTM supervision (foreground)",
		Long: `Run the CASS Memory (cm) daemon in the foreground under the NTM supervisor.

The supervisor launches 'cm serve', restarts it on crashes with backoff, and
health-checks it with an MCP JSON-RPC round-trip at the daemon root (cm speaks
MCP only; it has no REST /health endpoint). State transitions are printed here
and appended to .ntm/logs/. Stop with Ctrl-C.

Note: 'ntm spawn' does NOT start the memory daemon; only 'ntm monitor' and this
command run the supervisor. This is the one obvious way to run cm by hand.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMemoryServe(cmd.Context(), cmd.OutOrStdout(), port)
		},
	}
	cmd.Flags().IntVar(&port, "port", 8200, "Port for the cm daemon to listen on")
	return cmd
}

// memoryServePollInterval is how often runMemoryServe samples supervisor state
// for transition reporting. Package-level so tests can tighten it.
var memoryServePollInterval = 500 * time.Millisecond

// runMemoryServe supervises the cm daemon in the foreground until ctx is
// cancelled (Ctrl-C) or the daemon exhausts its restart budget.
func runMemoryServe(ctx context.Context, out io.Writer, port int) error {
	// Fail fast and loud when cm is not installed: the most common real-world
	// failure must not become a silent launch-retry loop.
	if _, err := exec.LookPath("cm"); err != nil {
		return fmt.Errorf("cm binary not found in PATH: %w\ninstall CASS Memory first (https://github.com/Dicklesworthstone/cass_memory), then re-run 'ntm memory serve'", err)
	}

	dir, err := os.Getwd()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	sessionID := fmt.Sprintf("memory-serve-%d", os.Getpid())
	sup, err := supervisor.New(supervisor.Config{
		SessionID:   sessionID,
		ProjectDir:  dir,
		MaxRestarts: supervisor.DefaultMaxRestarts,
	})
	if err != nil {
		return fmt.Errorf("initialize supervisor: %w", err)
	}
	defer sup.Shutdown()

	var spec supervisor.DaemonSpec
	found := false
	for _, s := range supervisor.DefaultSpecs() {
		if s.Name == "cm" {
			spec = s
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("internal error: no 'cm' daemon spec in supervisor defaults")
	}
	if port > 0 {
		spec.DefaultPort = port
	}

	if err := sup.Start(spec); err != nil {
		return fmt.Errorf("start cm daemon: %w", err)
	}

	d, ok := sup.GetDaemon("cm")
	if !ok {
		return fmt.Errorf("internal error: cm daemon not tracked after start")
	}
	fmt.Fprintf(out, "Supervising cm daemon (pid=%d port=%d state=%s)\n", d.PID, d.Port, d.State)
	fmt.Fprintf(out, "Logs: %s\n", filepath.Join(dir, ".ntm", "logs", fmt.Sprintf("cm-%s.log", sessionID)))
	fmt.Fprintln(out, "Press Ctrl-C to stop.")

	lastState := d.State
	ticker := time.NewTicker(memoryServePollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(out, "Shutting down cm daemon...")
			if err := sup.Shutdown(); err != nil {
				return fmt.Errorf("shutdown: %w", err)
			}
			fmt.Fprintln(out, "cm daemon stopped.")
			return nil
		case <-ticker.C:
			d, ok := sup.GetDaemon("cm")
			if !ok {
				return fmt.Errorf("cm daemon disappeared from supervisor")
			}
			if d.State != lastState {
				fmt.Fprintf(out, "cm daemon state: %s -> %s (pid=%d restarts=%d)\n", lastState, d.State, d.PID, d.Restarts)
				lastState = d.State
			}
			if d.State == supervisor.StateFailed && d.Restarts > supervisor.DefaultMaxRestarts {
				return fmt.Errorf("cm daemon failed permanently after %d restarts; see .ntm/logs/cm-%s.log", d.Restarts-1, sessionID)
			}
		}
	}
}

func newMemoryContextCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "context <task>",
		Short: "Get relevant context for a task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			task := args[0]

			dir, err := os.Getwd()
			if err != nil {
				return err
			}

			sessionID, err := findSessionID(dir)
			if err != nil {
				return err
			}

			client, err := cm.NewClient(dir, sessionID)
			if err != nil {
				return err
			}

			// `dir` is the project directory the user invoked us from; pass it
			// as the workspace scope so same-basename projects don't bleed
			// memory results into each other (#132).
			ctxResult, err := client.GetContext(context.Background(), task, dir)
			if err != nil {
				return err
			}

			return output.PrintJSON(ctxResult)
		},
	}
}

func newMemoryOutcomeCmd() *cobra.Command {
	var rules []string
	cmd := &cobra.Command{
		Use:   "outcome <success|failure|partial>",
		Short: "Record task outcome feedback",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			statusStr := args[0]
			var status cm.OutcomeStatus
			switch statusStr {
			case "success":
				status = cm.OutcomeSuccess
			case "failure":
				status = cm.OutcomeFailure
			case "partial":
				status = cm.OutcomePartial
			default:
				return fmt.Errorf("invalid status: %s", statusStr)
			}

			dir, err := os.Getwd()
			if err != nil {
				return err
			}

			sessionID, err := findSessionID(dir)
			if err != nil {
				return err
			}

			client, err := cm.NewClient(dir, sessionID)
			if err != nil {
				return err
			}

			report := cm.OutcomeReport{
				Status:  status,
				RuleIDs: rules,
			}

			return client.RecordOutcome(context.Background(), report)
		},
	}
	cmd.Flags().StringSliceVar(&rules, "rules", nil, "Comma-separated list of rule IDs applied")
	return cmd
}

func findSessionID(dir string) (string, error) {
	pidsDir := ".ntm/pids"
	entries, err := os.ReadDir(pidsDir)
	if err != nil {
		return "", fmt.Errorf("could not find .ntm/pids in current directory (run from project root): %w", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if len(name) > 3 && name[:3] == "cm-" && name[len(name)-4:] == ".pid" {
			return name[3 : len(name)-4], nil
		}
	}
	return "", fmt.Errorf("no running memory daemon found in current directory")
}

func newMemoryPrivacyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "privacy",
		Short: "Manage cross-agent privacy settings",
		Long: `Manage privacy controls for cross-agent enrichment in CASS Memory.

Cross-agent enrichment allows agents to share relevant context and learned rules
with each other. By default, this is disabled for privacy. You can enable it
and control which agents can participate.

Examples:
  ntm memory privacy status           # Show privacy settings
  ntm memory privacy enable           # Enable cross-agent enrichment
  ntm memory privacy disable          # Disable cross-agent enrichment
  ntm memory privacy allow GreenLake  # Allow specific agent
  ntm memory privacy deny BlueCat     # Remove agent from allowlist`,
	}

	cmd.AddCommand(
		newMemoryPrivacyStatusCmd(),
		newMemoryPrivacyEnableCmd(),
		newMemoryPrivacyDisableCmd(),
		newMemoryPrivacyAllowCmd(),
		newMemoryPrivacyDenyCmd(),
	)

	return cmd
}

func newMemoryPrivacyStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show cross-agent privacy settings",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCMPrivacyCommand("status", "--json")
		},
	}
}

func newMemoryPrivacyEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable [agents...]",
		Short: "Enable cross-agent enrichment",
		Long: `Enable cross-agent enrichment. Optionally specify agents to auto-allow.
This requires explicit consent as it allows sharing data between agents.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmdArgs := []string{"enable", "--json"}
			cmdArgs = append(cmdArgs, args...)
			return runCMPrivacyCommand(cmdArgs...)
		},
	}
}

func newMemoryPrivacyDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable",
		Short: "Disable cross-agent enrichment",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCMPrivacyCommand("disable", "--json")
		},
	}
}

func newMemoryPrivacyAllowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "allow <agent>",
		Short: "Allow a specific agent for cross-agent enrichment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCMPrivacyCommand("allow", args[0], "--json")
		},
	}
}

func newMemoryPrivacyDenyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "deny <agent>",
		Short: "Remove an agent from the cross-agent allowlist",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCMPrivacyCommand("deny", args[0], "--json")
		},
	}
}

// runCMPrivacyCommand executes a cm privacy subcommand
func runCMPrivacyCommand(args ...string) error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}

	fullArgs := append([]string{"privacy"}, args...)
	cmd := exec.CommandContext(context.Background(), "cm", fullArgs...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}
