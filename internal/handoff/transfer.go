package handoff

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

const (
	defaultTransferTTLSeconds   = 15 * 60 // 15 minutes
	defaultTransferGraceSeconds = 2
	defaultTransferCleanupTime  = 10 * time.Second
)

// ErrTransferGrantEvidence means the reservation reply did not establish the
// requested coverage. Its paths are evidence for inspection, not permission to
// release reservations or repeat a possibly committed acquisition.
var ErrTransferGrantEvidence = errors.New("invalid reservation transfer grant evidence")

// ReservationTransferClient is the subset of Agent Mail client methods needed for transfers.
type ReservationTransferClient interface {
	ReservePaths(ctx context.Context, opts agentmail.FileReservationOptions) (*agentmail.ReservationResult, error)
	ReleaseReservations(ctx context.Context, projectKey, agentName string, paths []string, ids []int) (*agentmail.ReleaseReservationsResult, error)
	RenewReservations(ctx context.Context, opts agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error)
}

// TransferReservationsOptions configures a reservation transfer.
type TransferReservationsOptions struct {
	ProjectKey   string
	FromAgent    string
	ToAgent      string
	Reservations []ReservationSnapshot

	// TTLSeconds refreshes the reservation TTL on transfer (0 uses default).
	TTLSeconds int
	// GracePeriod waits and retries once on conflict to allow release propagation.
	GracePeriod time.Duration

	Logger *slog.Logger
}

// ReservationTransferResult reports transfer outcomes for debugging and recovery.
type ReservationTransferResult struct {
	FromAgent      string                          `json:"from_agent"`
	ToAgent        string                          `json:"to_agent"`
	RequestedPaths []string                        `json:"requested_paths"`
	GrantedPaths   []string                        `json:"granted_paths"`
	ReleasedPaths  []string                        `json:"released_paths"`
	Conflicts      []agentmail.ReservationConflict `json:"conflicts,omitempty"`
	RolledBack     bool                            `json:"rolled_back,omitempty"`
	Success        bool                            `json:"success"`
	Error          string                          `json:"error,omitempty"`

	// Stage identifies the last attempted phase, including compensation.
	Stage string `json:"stage,omitempty"`
	// Attempts counts destination acquisition attempts, not rollback calls.
	Attempts int `json:"attempts,omitempty"`
	// These errors remain visible alongside the original operation failure.
	CleanupError  string `json:"cleanup_error,omitempty"`
	RollbackError string `json:"rollback_error,omitempty"`
	// OutcomeUnknown prohibits interpreting an error as proof of no effects.
	// RolledBack only records restored source coverage, not an atomic transfer.
	OutcomeUnknown bool `json:"outcome_unknown,omitempty"`
}

// TransferReservations moves reservations from one agent to another.
// Release/acquire is not atomic: a failure can leave effects requiring manual
// inspection. Only a complete grant set is success, and an unconfirmed cleanup
// must never authorize another acquisition or a claimed successful rollback.
func TransferReservations(ctx context.Context, client ReservationTransferClient, opts TransferReservationsOptions) (*ReservationTransferResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	result := &ReservationTransferResult{FromAgent: opts.FromAgent, ToAgent: opts.ToAgent, Stage: "validate"}
	fail := func(err error) (*ReservationTransferResult, error) {
		result.Error = err.Error()
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if client == nil {
		return fail(errors.New("reservation transfer requires an Agent Mail client"))
	}
	if opts.ProjectKey == "" {
		return fail(errors.New("reservation transfer requires project_key"))
	}
	if opts.FromAgent == "" || opts.ToAgent == "" {
		return fail(errors.New("reservation transfer requires both from_agent and to_agent"))
	}

	ttlSeconds := opts.TTLSeconds
	if ttlSeconds <= 0 {
		ttlSeconds = defaultTransferTTLSeconds
	}
	grace := opts.GracePeriod
	if grace <= 0 {
		grace = time.Duration(defaultTransferGraceSeconds) * time.Second
	}
	exclusivePaths, sharedPaths, requested := splitReservationPaths(opts.Reservations)
	result.RequestedPaths = requested
	if len(requested) == 0 {
		result.Success = true
		result.Stage = "complete"
		return result, nil
	}
	logger.Info("starting reservation transfer", "from_agent", opts.FromAgent, "to_agent", opts.ToAgent, "paths", len(requested))

	if opts.FromAgent == opts.ToAgent {
		result.Stage = "renew"
		renewed, err := client.RenewReservations(ctx, agentmail.RenewReservationsOptions{
			ProjectKey: opts.ProjectKey, AgentName: opts.ToAgent, ExtendSeconds: ttlSeconds, Paths: append([]string(nil), requested...),
		})
		err = errors.Join(err, ctx.Err())
		if err != nil {
			result.OutcomeUnknown = true
			return fail(err)
		}
		count := 0
		if renewed != nil {
			count = renewed.Renewed
		}
		if count != len(requested) {
			result.OutcomeUnknown = true
			return fail(fmt.Errorf("renewed %d of %d reservations for %s", count, len(requested), opts.ToAgent))
		}
		result.GrantedPaths = append([]string(nil), requested...)
		result.Success = true
		result.Stage = "complete"
		return result, nil
	}

	result.Stage = "release"
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	released, err := client.ReleaseReservations(ctx, opts.ProjectKey, opts.FromAgent, append([]string(nil), requested...), nil)
	if err != nil {
		result.OutcomeUnknown = true
		return fail(errors.Join(err, ctx.Err()))
	}
	count := 0
	if released != nil {
		count = released.Released
	}
	if count != len(requested) {
		result.OutcomeUnknown = true
		return fail(fmt.Errorf("released %d of %d requested reservations", count, len(requested)))
	}
	result.ReleasedPaths = append([]string(nil), requested...)

	// Compensation gets its own bounded context after caller cancellation.
	// A failed cleanup stops here: blindly clearing the grant slice and retrying
	// would lose evidence and can acquire more leases while the old ones remain.
	cleanup := func(granted []string) error {
		if len(granted) == 0 {
			return nil
		}
		result.Stage = "cleanup"
		cleanupCtx, cancel := newCleanupContext()
		defer cancel()
		if err := releaseGrantedReservations(cleanupCtx, client, opts.ProjectKey, opts.ToAgent, granted); err != nil {
			result.CleanupError = err.Error()
			result.OutcomeUnknown = true
			return fmt.Errorf("clean up destination grants: %w", err)
		}
		return nil
	}
	rollback := func() error {
		result.Stage = "rollback"
		rollbackCtx, cancel := newCleanupContext()
		defer cancel()
		if err := rollbackReservations(rollbackCtx, client, opts.ProjectKey, opts.FromAgent, ttlSeconds, exclusivePaths, sharedPaths); err != nil {
			result.RollbackError = err.Error()
			result.OutcomeUnknown = true
			return fmt.Errorf("restore source reservations: %w", err)
		}
		result.RolledBack = true
		return nil
	}

	for attempt := 1; attempt <= 2; attempt++ {
		// Cancellation after a confirmed source release still needs rollback,
		// but must not enter the destination mutation even for a lax client.
		if err := ctx.Err(); err != nil {
			return fail(errors.Join(err, rollback()))
		}
		result.Stage = "reserve"
		result.Attempts = attempt
		granted, conflicts, reserveErr := reserveAll(ctx, client, opts.ProjectKey, opts.ToAgent, ttlSeconds, opts.FromAgent, exclusivePaths, sharedPaths)
		result.GrantedPaths = append([]string(nil), granted...)
		result.Conflicts = append([]agentmail.ReservationConflict(nil), conflicts...)
		reserveErr = errors.Join(reserveErr, ctx.Err())
		if reserveErr == nil {
			result.Success = true
			result.Stage = "complete"
			logger.Info("reservation transfer complete", "from_agent", opts.FromAgent, "to_agent", opts.ToAgent, "paths", len(granted))
			return result, nil
		}

		if errors.Is(reserveErr, ErrTransferGrantEvidence) {
			// An omitted or foreign path is not a trustworthy cleanup scope.
			// Preserve all returned paths and the source release evidence rather
			// than broadening a release or claiming rollback on uncertain data.
			result.OutcomeUnknown = true
			return fail(reserveErr)
		}
		retryable := transferRetryableConflict(reserveErr, 0)
		if !retryable {
			result.OutcomeUnknown = true
		}
		if cleanupErr := cleanup(granted); cleanupErr != nil {
			return fail(errors.Join(reserveErr, cleanupErr))
		}
		if attempt == 1 && retryable && ctx.Err() == nil {
			result.Stage = "retry_wait"
			if waitErr := waitWithContext(ctx, grace); waitErr != nil {
				return fail(errors.Join(reserveErr, waitErr, rollback()))
			}
			continue
		}
		return fail(errors.Join(reserveErr, rollback()))
	}
	panic("unreachable reservation transfer attempt")
}

func splitReservationPaths(reservations []ReservationSnapshot) (exclusive []string, shared []string, requested []string) {
	seen := make(map[string]bool)
	exclusiveSet := make(map[string]bool)
	for _, r := range reservations {
		if r.PathPattern == "" {
			continue
		}
		if existingExclusive, ok := exclusiveSet[r.PathPattern]; ok {
			if r.Exclusive && !existingExclusive {
				exclusiveSet[r.PathPattern] = true
			}
			continue
		}
		exclusiveSet[r.PathPattern] = r.Exclusive
	}
	for path, exclusiveFlag := range exclusiveSet {
		if seen[path] {
			continue
		}
		seen[path] = true
		requested = append(requested, path)
		if exclusiveFlag {
			exclusive = append(exclusive, path)
		} else {
			shared = append(shared, path)
		}
	}
	sort.Strings(requested)
	sort.Strings(exclusive)
	sort.Strings(shared)
	return exclusive, shared, requested
}

func reserveAll(ctx context.Context, client ReservationTransferClient, projectKey, agentName string, ttlSeconds int, fromAgent string, exclusive, shared []string) ([]string, []agentmail.ReservationConflict, error) {
	var granted []string
	var conflicts []agentmail.ReservationConflict
	for i, paths := range [][]string{exclusive, shared} {
		if len(paths) == 0 {
			continue
		}
		grant, conflict, err := reserveGroup(ctx, client, projectKey, agentName, paths, ttlSeconds, i == 0, fromAgent)
		granted = append(granted, grant...)
		conflicts = append(conflicts, conflict...)
		if err != nil {
			// Keep the actual error even when a server returns a conflict code
			// without a conflict array. Do not acquire the next group on failure.
			return granted, conflicts, err
		}
	}
	return granted, conflicts, nil
}

func reserveGroup(ctx context.Context, client ReservationTransferClient, projectKey, agentName string, paths []string, ttlSeconds int, exclusive bool, fromAgent string) ([]string, []agentmail.ReservationConflict, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	res, err := client.ReservePaths(ctx, agentmail.FileReservationOptions{
		ProjectKey: projectKey, AgentName: agentName, Paths: append([]string(nil), paths...),
		TTLSeconds: ttlSeconds, Exclusive: exclusive,
		Reason: fmt.Sprintf("handoff transfer from %s", fromAgent),
	})
	if errors.Is(err, agentmail.ErrReservationUnverified) {
		// Decoding/readback can preserve plausible requested paths while
		// rejecting their ownership. Never turn those diagnostic handles into
		// a cleanup scope, including when the error also carries a conflict.
		err = errors.Join(ErrTransferGrantEvidence, err)
	}
	var granted []string
	var conflicts []agentmail.ReservationConflict
	if res == nil {
		if err == nil {
			err = fmt.Errorf("%w: server returned no reservation result", ErrTransferGrantEvidence)
		}
		return nil, nil, errors.Join(err, ctx.Err())
	}
	wanted := make(map[string]bool, len(paths))
	for _, path := range paths {
		wanted[path] = true
	}
	seen := make(map[string]bool, len(res.Granted))
	var evidenceErr error
	for _, g := range res.Granted {
		granted = append(granted, g.PathPattern)
		if !wanted[g.PathPattern] || seen[g.PathPattern] {
			evidenceErr = errors.Join(evidenceErr, fmt.Errorf("%w: unexpected or duplicate grant path %q", ErrTransferGrantEvidence, g.PathPattern))
		}
		seen[g.PathPattern] = true
	}
	conflicts = append(conflicts, res.Conflicts...)
	if len(conflicts) > 0 && !agentmail.IsReservationConflict(err) {
		err = errors.Join(err, fmt.Errorf("%w: %d conflicts", agentmail.ErrReservationConflict, len(conflicts)))
	}
	// Partial coverage is legitimate only as evidence accompanying failure.
	// A nil error, even with a non-nil result, must cover every requested path.
	if err == nil && len(seen) != len(wanted) {
		evidenceErr = errors.Join(evidenceErr, fmt.Errorf("%w: granted %d of %d requested paths", ErrTransferGrantEvidence, len(seen), len(wanted)))
	}
	return granted, conflicts, errors.Join(err, evidenceErr, ctx.Err())
}

func rollbackReservations(ctx context.Context, client ReservationTransferClient, projectKey, agentName string, ttlSeconds int, exclusive, shared []string) error {
	_, _, err := reserveAll(ctx, client, projectKey, agentName, ttlSeconds, agentName, exclusive, shared)
	return err
}

func releaseGrantedReservations(ctx context.Context, client ReservationTransferClient, projectKey, agentName string, granted []string) error {
	if len(granted) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	res, err := client.ReleaseReservations(ctx, projectKey, agentName, append([]string(nil), granted...), nil)
	if err = errors.Join(err, ctx.Err()); err != nil {
		return err
	}
	released := 0
	if res != nil {
		released = res.Released
	}
	if released != len(granted) {
		return fmt.Errorf("released %d of %d partial grants for %s", released, len(granted), agentName)
	}
	return nil
}

// IsReservationConflict also matches joined errors carrying a failed ownership
// readback or transport error. Those are NOT authorization to acquire again.
// Only wrapped/joined conflict-only causes may take the propagation retry.
func transferRetryableConflict(err error, depth int) bool {
	if err == nil || depth > 32 {
		return false
	}
	if err == agentmail.ErrReservationConflict {
		return true
	}
	switch e := err.(type) {
	case interface{ Unwrap() []error }:
		causes := e.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !transferRetryableConflict(cause, depth+1) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		return transferRetryableConflict(e.Unwrap(), depth+1)
	default:
		return false
	}
}

func newCleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), defaultTransferCleanupTime)
}

func waitWithContext(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
