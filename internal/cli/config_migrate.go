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

// migrateNoBehaviorChange is the safety sentence for the batches whose keys
// were removed from the schema precisely because nothing ever read them.
const migrateNoBehaviorChange = "Every removed key was a provable no-op (no runtime reader), so ntm behavior is unchanged."

// migrateRecoveryAliasChange is the honest sentence for the recovery-alias
// batch. Those keys were NOT no-ops — they fed [recovery] through an alias
// fold — so a config that set them to a non-default value must carry that
// value across, and claiming "behavior is unchanged" would be a lie (ntm#323).
const migrateRecoveryAliasChange = "Note: memory.include_in_recovery / memory.max_rules were NOT no-ops — they fed [recovery]. If you had set either to a non-default value, set recovery.include_cm_memories / recovery.max_cm_rules to the same value; the backup has the old values."

// migrateBehaviorNote reports whether every removed key was a provable no-op,
// and the sentence to print. A migration is only behavior-preserving when it
// touched no key that actually had a reader.
func migrateBehaviorNote(changes []config.MigrationChange) (noBehaviorChange bool, note string) {
	for _, change := range changes {
		if change.Tier == config.DeadKeyTierRecoveryAlias {
			return false, migrateRecoveryAliasChange
		}
	}
	return true, migrateNoBehaviorChange
}

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
before any change. Nearly every key removed was a provable no-op — it had no
runtime reader (that is why it was removed) — so migrating cannot change
behavior. The exception is the recovery-alias batch (memory.include_in_recovery,
memory.max_rules), which fed [recovery] before being removed; migrate names it
explicitly and tells you which [recovery] key to set.

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

			noBehaviorChange, behaviorNote := migrateBehaviorNote(result.Changes)

			if IsJSONOutput() {
				return output.PrintJSON(map[string]interface{}{
					"path":            result.Path,
					"clean":           result.Clean,
					"dry_run":         result.DryRun,
					"backup_path":     result.BackupPath,
					"removed_count":   len(result.Changes),
					"changes":         result.Changes,
					"unresolved":      result.Unresolved,
					"behavior_change": !noBehaviorChange,
					"note":            behaviorNote,
				})
			}

			if result.Clean {
				fmt.Printf("config is clean — no removed or deprecated keys in %s\n", result.Path)
				return nil
			}

			if len(result.Changes) > 0 {
				if dryRun {
					fmt.Printf("dry-run: %d dead key(s) would be removed from %s (nothing written):\n", len(result.Changes), result.Path)
				} else {
					fmt.Printf("removed %d dead key(s) from %s:\n", len(result.Changes), result.Path)
				}
				for _, change := range result.Changes {
					fmt.Printf("  - %s (%s): %s\n", change.Key, change.Tier, change.Disposition)
				}
				if !dryRun && result.BackupPath != "" {
					fmt.Printf("backup written: %s\n", result.BackupPath)
				}
				fmt.Println(behaviorNote)
			} else {
				// Nothing was removable but dead keys remain (unresolved-only
				// config, e.g. inside a live inline table): the file was NOT
				// modified and no backup was written.
				fmt.Printf("no dead keys could be removed automatically from %s (file unchanged)\n", result.Path)
			}
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
