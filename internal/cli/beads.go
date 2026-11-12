package cli

import (
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/output"
)

func newBeadsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "beads",
		Short: "Beads are managed via br and ntm work",
		Long: `Beads issue tracking is managed by the standalone br (beads_rust) CLI, with
graph-aware triage exposed through 'ntm work' (backed by bv).

Use 'ntm work' inside NTM:
  ntm work triage       Ranked bead recommendations & quick wins
  ntm work next         Single top bead recommendation
  ntm work alerts       Stale issues, blocking cascades & drift
  ntm work search <q>   Semantic search over beads

Use 'br' directly for mutations:
  br ready              Show issues ready to work
  br list --status=open All open issues
  br show <id>          Full issue details
  br create             Create a new issue
  br close <id>         Close an issue
  br sync --flush-only  Export to JSONL

See: https://github.com/Dicklesworthstone/beads_rust`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			msg := "Beads are managed by the br CLI and 'ntm work' (ntm work triage | next | alerts). Run 'ntm work --help' or 'br --help' for usage."
			if IsJSONOutput() {
				return output.PrintJSON(map[string]interface{}{
					"success":     true,
					"message":     msg,
					"replacement": "ntm work",
					"cli":         "br",
				})
			}
			output.PrintInfo(msg)
			return nil
		},
	}

	return cmd
}
