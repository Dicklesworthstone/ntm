package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newBeadsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "beads",
		Short: "Legacy beads daemon controls",
		Long: `Legacy beads daemon controls.

The currently supported br CLI no longer exposes daemon lifecycle commands.
These subcommands remain for parity with older plans and docs, but they now
return an explicit error instead of shelling out to nonexistent br commands.`,
	}

	cmd.AddCommand(newBeadsDaemonCmd())

	return cmd
}

func newBeadsDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Legacy beads daemon lifecycle controls",
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
		sessionID  string
		autoCommit bool
		autoPush   bool
		interval   string
		foreground bool
	)

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the legacy BD daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = sessionID
			_ = autoCommit
			_ = autoPush
			_ = interval
			_ = foreground
			return beadsDaemonUnsupportedError()
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
		Short: "Stop the legacy BD daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = sessionID
			return beadsDaemonUnsupportedError()
		},
	}

	cmd.Flags().StringVar(&sessionID, "session", "", "NTM session ID (uses supervisor)")

	return cmd
}

func newBeadsDaemonStatusCmd() *cobra.Command {
	var sessionID string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show legacy BD daemon status",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = sessionID
			return beadsDaemonUnsupportedError()
		},
	}

	cmd.Flags().StringVar(&sessionID, "session", "", "NTM session ID (uses supervisor)")

	return cmd
}

func newBeadsDaemonHealthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "health",
		Short: "Check legacy BD daemon health",
		RunE: func(cmd *cobra.Command, args []string) error {
			return beadsDaemonUnsupportedError()
		},
	}

	return cmd
}

func newBeadsDaemonMetricsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "metrics",
		Short: "Show legacy BD daemon metrics",
		RunE: func(cmd *cobra.Command, args []string) error {
			return beadsDaemonUnsupportedError()
		},
	}

	return cmd
}

func beadsDaemonUnsupportedError() error {
	return fmt.Errorf("beads daemon commands are not supported by the installed br version")
}
