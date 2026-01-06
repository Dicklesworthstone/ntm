package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/supervisor"
)

func newBeadsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "beads",
		Short: "Manage the beads (bd) daemon for issue tracking sync",
		Long: `Manage the beads daemon that automatically syncs issues with git remote.

The daemon handles:
  - Automatic export of database changes to JSONL
  - Auto-commit and push when configured
  - Pull and import of remote changes
  - Health monitoring and auto-restart

Examples:
  ntm beads daemon start           # Start BD daemon for current session
  ntm beads daemon stop            # Stop BD daemon
  ntm beads daemon status          # Show daemon status
  ntm beads daemon health          # Check daemon health
  ntm beads daemon metrics         # Show detailed metrics`,
	}

	cmd.AddCommand(newBeadsDaemonCmd())

	return cmd
}

func newBeadsDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the BD daemon lifecycle",
	}

	cmd.AddCommand(
		newBeadsDaemonStartCmd(),
		newBeadsDaemonStopCmd(),
		newBeadsDaemonStatusCmd(),
		newBeadsDaemonHealthCmd(),
		newBeadsDaemonMetricsCmd(),
	)

	return cmd
}

func newBeadsDaemonStartCmd() *cobra.Command {
	var (
		sessionID   string
		autoCommit  bool
		autoPush    bool
		interval    string
		foreground  bool
	)

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the BD daemon",
		Long: `Start the BD daemon for automatic issue sync.

When running within an NTM session, the daemon is managed by the supervisor
with automatic health monitoring and restart capability.

For standalone use, run 'bd daemon --start' directly.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}

			// If session specified, use supervisor
			if sessionID != "" {
				return startBDWithSupervisor(projectDir, sessionID, autoCommit, autoPush, interval)
			}

			// Otherwise, run bd daemon directly
			return startBDDirect(projectDir, autoCommit, autoPush, interval, foreground)
		},
	}

	cmd.Flags().StringVar(&sessionID, "session", "", "NTM session ID (uses supervisor)")
	cmd.Flags().BoolVar(&autoCommit, "auto-commit", true, "Automatically commit changes")
	cmd.Flags().BoolVar(&autoPush, "auto-push", false, "Automatically push commits (requires policy approval)")
	cmd.Flags().StringVar(&interval, "interval", "5s", "Sync check interval")
	cmd.Flags().BoolVar(&foreground, "foreground", false, "Run in foreground (standalone mode only)")

	return cmd
}

func newBeadsDaemonStopCmd() *cobra.Command {
	var sessionID string

	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the BD daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}

			// If session specified, use supervisor
			if sessionID != "" {
				return stopBDWithSupervisor(projectDir, sessionID)
			}

			// Otherwise, run bd daemon --stop directly
			return runBDCommand(projectDir, "--stop")
		},
	}

	cmd.Flags().StringVar(&sessionID, "session", "", "NTM session ID (uses supervisor)")

	return cmd
}

func newBeadsDaemonStatusCmd() *cobra.Command {
	var sessionID string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show BD daemon status",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}

			// If session specified, check supervisor status
			if sessionID != "" {
				return showBDSupervisorStatus(projectDir, sessionID)
			}

			// Otherwise, run bd daemon --status
			return runBDCommand(projectDir, "--status", "--json")
		},
	}

	cmd.Flags().StringVar(&sessionID, "session", "", "NTM session ID (uses supervisor)")

	return cmd
}

func newBeadsDaemonHealthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "health",
		Short: "Check BD daemon health",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}

			// Run bd daemon health
			return runBDCommand(projectDir, "health", "--json")
		},
	}

	return cmd
}

func newBeadsDaemonMetricsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "metrics",
		Short: "Show BD daemon metrics",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}

			// Run bd daemon --metrics
			return runBDCommand(projectDir, "--metrics", "--json")
		},
	}

	return cmd
}

// startBDWithSupervisor starts BD daemon using the NTM supervisor
func startBDWithSupervisor(projectDir, sessionID string, autoCommit, autoPush bool, interval string) error {
	sup, err := supervisor.New(supervisor.Config{
		SessionID:  sessionID,
		ProjectDir: projectDir,
	})
	if err != nil {
		return fmt.Errorf("create supervisor: %w", err)
	}

	// Build args based on flags
	args := []string{"daemon", "--start", "--foreground"}
	if autoCommit {
		args = append(args, "--auto-commit")
	}
	if autoPush {
		args = append(args, "--auto-push")
	}
	if interval != "" {
		args = append(args, "--interval", interval)
	}

	spec := supervisor.DaemonSpec{
		Name:      "bd",
		Command:   "bd",
		Args:      args,
		HealthCmd: []string{"bd", "daemon", "--health"},
		WorkDir:   projectDir,
	}

	if err := sup.Start(spec); err != nil {
		return fmt.Errorf("start bd daemon: %w", err)
	}

	fmt.Println("BD daemon started via supervisor")
	return nil
}

// stopBDWithSupervisor stops BD daemon via supervisor
func stopBDWithSupervisor(projectDir, sessionID string) error {
	sup, err := supervisor.New(supervisor.Config{
		SessionID:  sessionID,
		ProjectDir: projectDir,
	})
	if err != nil {
		return fmt.Errorf("create supervisor: %w", err)
	}

	if err := sup.Stop("bd"); err != nil {
		return fmt.Errorf("stop bd daemon: %w", err)
	}

	fmt.Println("BD daemon stopped")
	return nil
}

// showBDSupervisorStatus shows BD daemon status from supervisor
func showBDSupervisorStatus(projectDir, sessionID string) error {
	// Try to read PID file
	pidPath := filepath.Join(projectDir, ".ntm", "pids", fmt.Sprintf("bd-%s.pid", sessionID))
	data, err := os.ReadFile(pidPath)
	if err != nil {
		if os.IsNotExist(err) {
			if jsonOutput {
				return output.PrintJSON(map[string]interface{}{
					"status":  "not_running",
					"session": sessionID,
				})
			}
			fmt.Println("BD daemon is not running for this session")
			return nil
		}
		return fmt.Errorf("read pid file: %w", err)
	}

	var info supervisor.PIDFileInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return fmt.Errorf("parse pid file: %w", err)
	}

	if jsonOutput {
		return output.PrintJSON(map[string]interface{}{
			"status":     "running",
			"pid":        info.PID,
			"session":    sessionID,
			"started_at": info.StartedAt,
		})
	}

	fmt.Printf("BD daemon status:\n")
	fmt.Printf("  Status:     running\n")
	fmt.Printf("  PID:        %d\n", info.PID)
	fmt.Printf("  Session:    %s\n", sessionID)
	fmt.Printf("  Started:    %s\n", info.StartedAt.Format("2006-01-02 15:04:05"))

	return nil
}

// startBDDirect starts BD daemon directly without supervisor
func startBDDirect(projectDir string, autoCommit, autoPush bool, interval string, foreground bool) error {
	args := []string{"daemon", "--start"}

	if foreground {
		args = append(args, "--foreground")
	}
	if autoCommit {
		args = append(args, "--auto-commit")
	}
	if autoPush {
		args = append(args, "--auto-push")
	}
	if interval != "" {
		args = append(args, "--interval", interval)
	}

	cmd := exec.CommandContext(context.Background(), "bd", args...)
	cmd.Dir = projectDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if foreground {
		// Run in foreground - blocks
		return cmd.Run()
	}

	// Start in background
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start bd daemon: %w", err)
	}

	fmt.Println("BD daemon started")
	return nil
}

// runBDCommand runs a bd daemon subcommand
func runBDCommand(projectDir string, args ...string) error {
	fullArgs := append([]string{"daemon"}, args...)
	cmd := exec.CommandContext(context.Background(), "bd", fullArgs...)
	cmd.Dir = projectDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}
