package cli

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/tools"
)

func newRepoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Repo management commands",
		Long: `Repo-level commands that pass through to external tooling.

Examples:
  ntm repo sync --dry-run     # Run ru sync in dry-run mode
  ntm repo sync --workers=4   # Pass through ru flags as-is`,
	}

	cmd.AddCommand(newRepoSyncCmd())
	return cmd
}

func newRepoSyncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:                "sync [ru flags...]",
		Short:              "Run ru sync (passthrough)",
		Long:               "Thin wrapper around ru sync. All flags/args are passed through to ru.",
		Args:               cobra.ArbitraryArgs,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRepoSync(args)
		},
	}

	return cmd
}

func runRepoSync(args []string) error {
	adapter := tools.NewRUAdapter()
	path, installed := adapter.Detect()
	if !installed {
		return fmt.Errorf("ru not installed: install ru to use 'ntm repo sync'")
	}

	ruArgs := append([]string{"sync"}, args...)
	cmd := exec.Command(path, ruArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	return cmd.Run()
}
