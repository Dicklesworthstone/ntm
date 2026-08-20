package cli

// config_migrate.go — `ntm config migrate` (bd-config-migrate-warning-wall-151x2).
//
// Field incident: v1.29.1 shell integration invoked ntm on every new pane and
// the strict loader's removed/deprecated-key failure printed a ~30-line
// disposition wall to stderr each time, with no automated fix offered. This
// subcommand IS the automated fix: it surgically deletes every dead key from
// the selected config file (backup first), preserving everything else.

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/output"
)

// migrateNoBehaviorChange is the safety sentence: every key migrate removes
// was removed from the schema precisely because nothing ever read it.
const migrateNoBehaviorChange = "Every removed key was a provable no-op (no runtime reader), so ntm behavior is unchanged."

func newConfigMigrateCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Remove removed/deprecated config keys (backup kept)",
		Long: `Removes every removed (v1.26.0 batch) and deprecated (v1.28.0 batch) config
key from the selected config file. The edit is text-surgical: all other keys,
comments, ordering, and formatting are preserved; tables left empty by the
removals lose their headers too.

A timestamped backup (<config>.bak.<unix>) is always written next to the file
before any change. Every key removed was a provable no-op — it had no runtime
reader (that is why it was removed) — so migrating cannot change behavior.

Examples:
  ntm config migrate --dry-run   # show what would be removed, write nothing
  ntm config migrate             # clean the config (backup kept)
  ntm config migrate --json      # machine-readable report`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := config.MigrateDeadKeys(selectedConfigPath(), dryRun)
			if err != nil {
				return err
			}

			if IsJSONOutput() {
				return output.PrintJSON(map[string]interface{}{
					"path":            result.Path,
					"clean":           result.Clean,
					"dry_run":         result.DryRun,
					"backup_path":     result.BackupPath,
					"removed_count":   len(result.Changes),
					"changes":         result.Changes,
					"unresolved":      result.Unresolved,
					"behavior_change": false,
					"note":            migrateNoBehaviorChange,
				})
			}

			if result.Clean {
				fmt.Printf("config is clean — no removed or deprecated keys in %s\n", result.Path)
				return nil
			}

			if dryRun {
				fmt.Printf("dry-run: %d dead key(s) would be removed from %s (nothing written):\n", len(result.Changes), result.Path)
			} else {
				fmt.Printf("removed %d dead key(s) from %s:\n", len(result.Changes), result.Path)
			}
			for _, change := range result.Changes {
				fmt.Printf("  - %s (%s): %s\n", change.Key, change.Tier, change.Disposition)
			}
			if !dryRun {
				fmt.Printf("backup written: %s\n", result.BackupPath)
			}
			fmt.Println(migrateNoBehaviorChange)
			if len(result.Unresolved) > 0 {
				fmt.Printf("WARNING: %d dead key(s) could not be removed automatically (edit by hand; 'ntm doctor' names each):\n", len(result.Unresolved))
				for _, change := range result.Unresolved {
					fmt.Printf("  - %s (%s): %s\n", change.Key, change.Tier, change.Disposition)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be removed without writing anything")
	return cmd
}
