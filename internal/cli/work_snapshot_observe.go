package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/robot/adapters"
)

const minimumWorkObservationInterval = 100 * time.Millisecond

var errWorkObservationProjectChanged = errors.New("work observation project changed")

// A work observation is not an assignment or a lease. The verified count is
// useful for deciding when to ask for a claim; it cannot authorize dispatch.
type workObservationInfo struct {
	Sequence  uint64    `json:"sequence"`
	Samples   uint64    `json:"samples"`
	CheckedAt time.Time `json:"checked_at"`
	Terminal  bool      `json:"terminal"`
	Matched   bool      `json:"matched,omitempty"`
}

type workObservation struct {
	Info workObservationInfo
	Work *adapters.WorkSection
	Err  error
}

type workObservationOptions struct {
	Interval            time.Duration
	SampleTimeout       time.Duration
	MinimumReady        int // zero streams all observations instead of waiting
	RequireReservations bool
}

func (o workObservationOptions) validate() error {
	if o.Interval < minimumWorkObservationInterval || o.Interval > 30*time.Second {
		return errors.New("--interval must be between 100ms and 30s")
	}
	if o.SampleTimeout <= 0 || o.SampleTimeout > time.Minute {
		return errors.New("--timeout must be positive and no more than one minute")
	}
	if o.MinimumReady < 0 || o.MinimumReady > 100000 {
		return errors.New("--wait-ready must be between 1 and 100000")
	}
	if o.RequireReservations && o.MinimumReady == 0 {
		return errors.New("--require-reservations requires --wait-ready")
	}
	return nil
}

// observeWorkSnapshots deliberately knows nothing about tracker tools, claims,
// caches or reservations. Its collector is the existing CollectDurableWork
// query. Reads and writes are sequential: a slow consumer cannot accumulate a
// queue of stale observations or cause overlapping collection goroutines.
func observeWorkSnapshots(ctx context.Context, opts workObservationOptions,
	collect func(context.Context) (*adapters.WorkSection, error),
	emit func(workObservation) error,
) error {
	if ctx == nil || collect == nil || emit == nil {
		return errors.New("work observation requires a context, collector and output")
	}
	if err := opts.validate(); err != nil {
		return err
	}
	var sequence, samples uint64
	publish := func(work *adapters.WorkSection, err error, terminal, matched bool) error {
		sequence++
		// A partial result accompanying an error is inspection evidence, not
		// ready work. Never carry a prior or failed sample into another event.
		if err != nil {
			work = nil
		}
		return emit(workObservation{
			Info: workObservationInfo{Sequence: sequence, Samples: samples, CheckedAt: time.Now().UTC(), Terminal: terminal, Matched: matched},
			Work: work, Err: err,
		})
	}
	finish := func(err error) error {
		if outputErr := publish(nil, err, true, false); outputErr != nil {
			return errors.Join(err, outputErr)
		}
		return err
	}

	for {
		if err := ctx.Err(); err != nil {
			return finish(err)
		}
		readCtx, cancel := context.WithTimeout(ctx, opts.SampleTimeout)
		samples++
		work, err := collect(readCtx)
		// A collector must not turn a late success into a fresh observation.
		if readErr := readCtx.Err(); readErr != nil {
			err = readErr
		}
		cancel()
		if parentErr := ctx.Err(); parentErr != nil {
			return finish(parentErr)
		}
		if errors.Is(err, errWorkObservationProjectChanged) {
			return finish(err)
		}
		if err == nil && (work == nil || !work.Available) {
			err = adapters.ErrWorkSnapshotUnavailable
		}

		if opts.MinimumReady > 0 {
			if err != nil {
				// Unavailable evidence is not an empty queue. In particular a
				// stale source must not silently trigger an implicit refresh.
				return finish(err)
			}
			ready, readyErr := verifiedWorkReadyCount(work)
			if readyErr != nil {
				return finish(readyErr)
			}
			reservationsOK := !opts.RequireReservations || observedWorkReservations(work)
			if ready >= opts.MinimumReady && reservationsOK {
				if parentErr := ctx.Err(); parentErr != nil {
					return finish(parentErr)
				}
				return publish(work, nil, true, true)
			}
		} else if outputErr := publish(work, err, false, false); outputErr != nil {
			// Do not retry a broken output pipe or make more source reads.
			return outputErr
		}

		// Start a new interval after collection AND publication. A ticker
		// would accumulate a tick during slow reads and cause catch-up bursts.
		timer := time.NewTimer(opts.Interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return finish(ctx.Err())
		case <-timer.C:
		}
	}
}

func verifiedWorkReadyCount(work *adapters.WorkSection) (int, error) {
	if work == nil || !work.Available || work.Summary == nil || work.Verification == nil {
		return 0, errors.New("ready wait requires an available verified work summary")
	}
	v := work.Verification
	if v.Source == nil || !v.Source.Bound() || v.CountScope != "verified_candidates" || v.VerifiedReady == nil {
		return 0, errors.New("ready wait requires a complete canonical candidate count; tool-reported or preview-only counts cannot satisfy it")
	}
	count := *v.VerifiedReady
	if count < 0 || count != work.Summary.Ready || count < len(work.Ready) {
		return 0, errors.New("verified work count is inconsistent with its summary or preview")
	}
	return count, nil
}

func observedWorkReservations(work *adapters.WorkSection) bool {
	if work == nil || work.Verification == nil || work.Verification.Reservations == nil {
		return false
	}
	r := work.Verification.Reservations
	observed, err := time.Parse(time.RFC3339Nano, r.ObservedAt)
	return r.State == "observed" && r.ProjectID > 0 && err == nil && !observed.IsZero()
}

// Pin the resolved directory once for a long-lived command. Moving the input
// symlink must not redirect a running observer to a different project. Replacing
// the pinned directory itself is an error, not a request to observe its heir.
func pinWorkObservationProject(project string) (string, func() error, error) {
	absolute, err := filepath.Abs(project)
	if err != nil {
		return "", nil, err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", nil, fmt.Errorf("resolve work observation project: %w", err)
	}
	original, err := os.Stat(resolved)
	if err != nil {
		return "", nil, err
	}
	if !original.IsDir() {
		return "", nil, errors.New("work observation project is not a directory")
	}
	check := func() error {
		current, err := os.Stat(resolved)
		if err != nil {
			return fmt.Errorf("%w: %w", errWorkObservationProjectChanged, err)
		}
		if !current.IsDir() || !os.SameFile(original, current) {
			return fmt.Errorf("%w: directory was replaced; restart with an explicit project", errWorkObservationProjectChanged)
		}
		return nil
	}
	return resolved, check, nil
}
