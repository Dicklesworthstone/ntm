package cli

import (
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/output"
)

func newBeadsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "beads",
		Short: "Beads are managed by the br CLI",
		Long: `Beads issue tracking is managed by the standalone br (beads_rust) CLI.

Use br directly:
  br ready              Show issues ready to work
  br list --status=open All open issues
  br show <id>          Full issue details
  br create             Create a new issue
  br close <id>         Close an issue
  br sync --flush-only  Export to JSONL

See: https://github.com/Dicklesworthstone/beads_rust`,
		RunE: func(cmd *cobra.Command, args []string) error {
			output.PrintInfo("Beads are managed by the br CLI. Run 'br --help' for usage.")
			return nil
		},
	}

	return cmd
}
