package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
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
	var timeout, interval, waitTimeout time.Duration
	var watch, requireReservations bool
	var minimumReady int
	cmd := &cobra.Command{
		Use:   "work-snapshot",
		Short: "Read restart-safe, source-verified work with live reservation checks",
		Long: `Read a durable work observation as JSON.

Complete candidates are cached per project for 45 seconds. Each reuse verifies
checkout/tracker identity, current policy and live reservation evidence before
applying the display limit. A missing or expired observation is collected live.
Source mismatch or corruption fails visibly; --refresh explicitly collects a
replacement without repairing, claiming or dispatching tracker work.

--watch emits one JSON object per observation until interrupted. Failed reads
emit unavailable events, never a replay of the previous ready list. --wait-ready
returns one result after that many canonical, verified candidates are ready;
it uses the complete count, not --limit. --wait-timeout bounds the entire wait,
while --timeout bounds each read. --require-reservations additionally waits for
an observed Agent Mail reservation read. Neither mode claims work.

--refresh applies to every observation. Without it, source mismatch remains an
explicit error rather than permission to silently replace cached evidence.

Examples:
  ntm work-snapshot --project=/data/projects/myapp --limit=5
  ntm work-snapshot --refresh
  ntm work-snapshot --watch --refresh --interval=2s
  ntm work-snapshot --wait-ready=2 --refresh --wait-timeout=5m`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit <= 0 || limit > 100000 {
				return fmt.Errorf("--limit must be between 1 and 100000")
			}
			if timeout <= 0 || timeout > time.Minute {
				return fmt.Errorf("--timeout must be positive and no more than one minute")
			}
			observing := watch || minimumReady > 0
			if cmd.Flags().Changed("wait-ready") && minimumReady <= 0 {
				return errors.New("--wait-ready must be between 1 and 100000")
			}
			if watch && minimumReady > 0 {
				return errors.New("--watch and --wait-ready are mutually exclusive")
			}
			if !observing && (cmd.Flags().Changed("interval") || requireReservations) {
				return errors.New("--interval and --require-reservations require an observation mode")
			}
			if cmd.Flags().Changed("wait-timeout") && minimumReady == 0 {
				return errors.New("--wait-timeout requires --wait-ready")
			}
			observationOpts := workObservationOptions{
				Interval: interval, SampleTimeout: timeout, MinimumReady: minimumReady,
				RequireReservations: requireReservations,
			}
			if observing {
				if err := observationOpts.validate(); err != nil {
					return err
				}
			}
			if minimumReady > 0 && (waitTimeout <= 0 || waitTimeout > 24*time.Hour) {
				return errors.New("--wait-timeout must be positive and no more than 24 hours")
			}
			resolved := project
			if resolved == "" {
				resolved = util.ResolveProjectDir("")
			}
			if observing {
				return runWorkSnapshotObservation(cmd, resolved, limit, refresh, waitTimeout, observationOpts)
			}
			return runWorkSnapshot(cmd, resolved, limit, refresh, timeout)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project directory (defaults to the current project)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 10, "Maximum visible ready candidates; verified totals are not truncated")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "Explicitly replace cached evidence on each read with a fresh live collection")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "Budget for each work collection and verification")
	cmd.Flags().BoolVar(&watch, "watch", false, "Stream JSON observations until interrupted (failures are explicit events)")
	cmd.Flags().IntVar(&minimumReady, "wait-ready", 0, "Wait for at least this many source-verified ready candidates")
	cmd.Flags().DurationVar(&interval, "interval", 2*time.Second, "Pause between completed observations (100ms to 30s)")
	cmd.Flags().DurationVar(&waitTimeout, "wait-timeout", 5*time.Minute, "Overall budget for --wait-ready")
	cmd.Flags().BoolVar(&requireReservations, "require-reservations", false, "With --wait-ready, also require observed live reservation evidence")
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
	payload, err := makeWorkSnapshotReply(project, work, err, nil)
	if encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(payload); encodeErr != nil {
		return encodeErr
	}
	return err
}

type workSnapshotReply struct {
	robot.RobotResponse
	Project     string                `json:"project"`
	Work        *adapters.WorkSection `json:"work"`
	Observation *workObservationInfo  `json:"observation,omitempty"`
}

// The one-shot and observation surfaces share error classification and
// disclosure. Returned errors remain classifiable without leaking raw text
// through Cobra's stderr path.
type disclosedWorkSnapshotError struct {
	cause error
	text  string
}

func (e *disclosedWorkSnapshotError) Error() string { return e.text }
func (e *disclosedWorkSnapshotError) Unwrap() error { return e.cause }

func discloseWorkSnapshotError(err error) error {
	if err == nil {
		return nil
	}
	safe, _ := adapters.NormalizeDisclosureText(err.Error())
	return &disclosedWorkSnapshotError{cause: err, text: safe}
}

func makeWorkSnapshotReply(project string, work *adapters.WorkSection, err error, observation *workObservationInfo) (workSnapshotReply, error) {
	response := robot.NewRobotResponse(err == nil && work != nil && work.Available)
	if err == nil && (work == nil || !work.Available) {
		err = adapters.ErrWorkSnapshotUnavailable
	}
	if err != nil {
		code := "WORK_SNAPSHOT_UNAVAILABLE"
		if errors.Is(err, context.Canceled) {
			code = "CANCELLED"
		} else if errors.Is(err, context.DeadlineExceeded) {
			code = "TIMEOUT"
		} else if errors.Is(err, worksource.ErrStale) {
			code = worksource.StaleCode
		}
		err = discloseWorkSnapshotError(err)
		response = robot.NewErrorResponse(err, code, "Inspect the source and retry with --refresh to request a new observation")
		if work != nil && work.Available {
			work = nil
		}
	}
	return workSnapshotReply{RobotResponse: response, Project: project, Work: work, Observation: observation}, err
}

func runWorkSnapshotObservation(cmd *cobra.Command, project string, limit int, refresh bool, waitTimeout time.Duration, opts workObservationOptions) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if opts.MinimumReady > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, waitTimeout)
		defer cancel()
	}
	encoder := json.NewEncoder(cmd.OutOrStdout())
	emit := func(observation workObservation) error {
		payload, _ := makeWorkSnapshotReply(project, observation.Work, observation.Err, &observation.Info)
		return encoder.Encode(payload)
	}
	fail := func(err error) error {
		if outputErr := emit(workObservation{Info: workObservationInfo{Sequence: 1, CheckedAt: time.Now().UTC(), Terminal: true}, Err: err}); outputErr != nil {
			return outputErr
		}
		return discloseWorkSnapshotError(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	pinned, checkProject, err := pinWorkObservationProject(project)
	if err != nil {
		return fail(err)
	}
	project = pinned
	store, err := state.Open("")
	if err != nil {
		return fail(err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		return fail(err)
	}
	cfg := adapters.DefaultWorkCoordinationAdapterConfig(project)
	cfg.WorkItemLimit = limit
	collect := func(ctx context.Context) (*adapters.WorkSection, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := checkProject(); err != nil {
			return nil, err
		}
		work, err := adapters.CollectDurableWork(ctx, store, cfg, refresh)
		if scopeErr := checkProject(); scopeErr != nil {
			return nil, scopeErr
		}
		return work, err
	}
	return discloseWorkSnapshotError(observeWorkSnapshots(ctx, opts, collect, emit))
}
