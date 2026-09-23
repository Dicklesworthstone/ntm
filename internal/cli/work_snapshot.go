package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/robot/adapters"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/util"
	"github.com/Dicklesworthstone/ntm/internal/worksource"
)

func init() {
	rootCmd.AddCommand(newWorkSnapshotCmd())
}

// Work snapshots have an explicit query surface while legacy status/snapshot
// row consumers are migrated. This command never reads readiness from those
// rows: its only cache is a complete, source-bound candidate observation.
func newWorkSnapshotCmd() *cobra.Command {
	var project string
	var limit int
	var refresh bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "work-snapshot",
		Short: "Read restart-safe, source-verified work with live reservation checks",
		Long: `Read a durable work observation as JSON.

Complete candidates are cached per project for 45 seconds. Each reuse verifies
checkout/tracker identity, current policy and live reservation evidence before
applying the display limit. A missing or expired observation is collected live.
Source mismatch or corruption fails visibly; --refresh explicitly collects a
replacement without repairing, claiming or dispatching tracker work.

Examples:
  ntm work-snapshot --project=/data/projects/myapp --limit=5
  ntm work-snapshot --refresh`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit <= 0 || limit > 100000 {
				return fmt.Errorf("--limit must be between 1 and 100000")
			}
			if timeout <= 0 || timeout > time.Minute {
				return fmt.Errorf("--timeout must be positive and no more than one minute")
			}
			resolved := project
			if resolved == "" {
				resolved = util.ResolveProjectDir("")
			}
			return runWorkSnapshot(cmd, resolved, limit, refresh, timeout)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project directory (defaults to the current project)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 10, "Maximum visible ready candidates; verified totals are not truncated")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "Explicitly replace cached evidence with a fresh live collection")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "Budget for work collection and verification")
	return cmd
}

func runWorkSnapshot(cmd *cobra.Command, project string, limit int, refresh bool, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()
	var work *adapters.WorkSection
	err := ctx.Err()
	if err == nil {
		var store *state.Store
		store, err = state.Open("")
		if err == nil {
			defer store.Close()
			err = store.Migrate()
			if err == nil {
				cfg := adapters.DefaultWorkCoordinationAdapterConfig(project)
				cfg.WorkItemLimit = limit
				work, err = adapters.CollectDurableWork(ctx, store, cfg, refresh)
			}
		}
	}
	response := robot.NewRobotResponse(err == nil && work != nil && work.Available)
	if err == nil && (work == nil || !work.Available) {
		err = adapters.ErrWorkSnapshotUnavailable
	}
	if err != nil {
		code := "WORK_SNAPSHOT_UNAVAILABLE"
		if errors.Is(err, worksource.ErrStale) {
			code = worksource.StaleCode
		} else if errors.Is(err, context.Canceled) {
			code = "CANCELLED"
		} else if errors.Is(err, context.DeadlineExceeded) {
			code = "TIMEOUT"
		}
		safe, _ := adapters.NormalizeDisclosureText(err.Error())
		response = robot.NewErrorResponse(errors.New(safe), code, "Inspect the source and retry with --refresh to request a new observation")
		// Cobra may also print the returned error; do not redact only JSON
		// while leaking the original provider error through stderr.
		err = errors.New(safe)
	}
	payload := struct {
		robot.RobotResponse
		Project string                `json:"project"`
		Work    *adapters.WorkSection `json:"work"`
	}{response, project, work}
	if encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(payload); encodeErr != nil {
		return encodeErr
	}
	return err
}
