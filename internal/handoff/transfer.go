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
)

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
}

// TransferReservations moves reservations from one agent to another.
// It releases the old reservations, attempts to reserve for the new agent,
// and rolls back on conflicts where possible to approximate atomicity.
func TransferReservations(ctx context.Context, client ReservationTransferClient, opts TransferReservationsOptions) (*ReservationTransferResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	result := &ReservationTransferResult{
		FromAgent: opts.FromAgent,
		ToAgent:   opts.ToAgent,
		Success:   false,
	}

	if client == nil {
		err := errors.New("reservation transfer requires an Agent Mail client")
		result.Error = err.Error()
		return result, err
	}
	if opts.ProjectKey == "" {
		err := errors.New("reservation transfer requires project_key")
		result.Error = err.Error()
		return result, err
	}
	if opts.FromAgent == "" || opts.ToAgent == "" {
		err := errors.New("reservation transfer requires both from_agent and to_agent")
		result.Error = err.Error()
		return result, err
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
		return result, nil
	}

	logger.Info("starting reservation transfer",
		"from_agent", opts.FromAgent,
		"to_agent", opts.ToAgent,
		"paths", len(requested),
	)

	// If transferring to the same agent, just refresh TTL.
	if opts.FromAgent == opts.ToAgent {
		_, err := client.RenewReservations(ctx, agentmail.RenewReservationsOptions{
			ProjectKey:    opts.ProjectKey,
			AgentName:     opts.ToAgent,
			ExtendSeconds: ttlSeconds,
		})
		if err != nil {
			result.Error = err.Error()
			logger.Warn("reservation refresh failed", "error", err)
			return result, err
		}
		result.Success = true
		logger.Info("reservation refresh complete", "agent", opts.ToAgent, "paths", len(requested))
		return result, nil
	}

	// Release old reservations first.
	if _, err := client.ReleaseReservations(ctx, opts.ProjectKey, opts.FromAgent, requested, nil); err != nil {
		result.Error = err.Error()
		logger.Warn("reservation release failed", "error", err)
		return result, err
	}
	result.ReleasedPaths = requested

	// Attempt reservation for the new agent, with one retry for propagation.
	grant, conflicts, err := reserveAll(ctx, client, opts.ProjectKey, opts.ToAgent, ttlSeconds, opts.FromAgent, exclusivePaths, sharedPaths)
	if agentmail.IsReservationConflict(err) && grace > 0 {
		// Release any partial grants before retrying to keep atomic semantics.
		_, _ = client.ReleaseReservations(ctx, opts.ProjectKey, opts.ToAgent, grant, nil)
		if waitErr := waitWithContext(ctx, grace); waitErr != nil {
			result.Error = waitErr.Error()
			return result, waitErr
		}
		grant, conflicts, err = reserveAll(ctx, client, opts.ProjectKey, opts.ToAgent, ttlSeconds, opts.FromAgent, exclusivePaths, sharedPaths)
	}

	if err != nil {
		result.GrantedPaths = grant
		result.Conflicts = conflicts
		result.Error = err.Error()

		// Roll back to old agent when conflicts happen.
		if agentmail.IsReservationConflict(err) && len(requested) > 0 {
			rollbackErr := rollbackReservations(ctx, client, opts.ProjectKey, opts.FromAgent, ttlSeconds, exclusivePaths, sharedPaths)
			if rollbackErr != nil {
				logger.Warn("reservation rollback failed", "error", rollbackErr)
			} else {
				result.RolledBack = true
			}
		}
		logger.Warn("reservation transfer failed", "error", err, "conflicts", len(conflicts))
		return result, err
	}

	result.GrantedPaths = grant
	result.Success = true
	logger.Info("reservation transfer complete",
		"from_agent", opts.FromAgent,
		"to_agent", opts.ToAgent,
		"paths", len(grant),
	)
	return result, nil
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

	if len(exclusive) > 0 {
		grant, conflict, err := reserveGroup(ctx, client, projectKey, agentName, exclusive, ttlSeconds, true, fromAgent)
		granted = append(granted, grant...)
		conflicts = append(conflicts, conflict...)
		if err != nil && !agentmail.IsReservationConflict(err) {
			return granted, conflicts, err
		}
	}

	if len(shared) > 0 {
		grant, conflict, err := reserveGroup(ctx, client, projectKey, agentName, shared, ttlSeconds, false, fromAgent)
		granted = append(granted, grant...)
		conflicts = append(conflicts, conflict...)
		if err != nil && !agentmail.IsReservationConflict(err) {
			return granted, conflicts, err
		}
	}

	if len(conflicts) > 0 {
		return granted, conflicts, fmt.Errorf("%w: %d conflicts", agentmail.ErrReservationConflict, len(conflicts))
	}
	return granted, conflicts, nil
}

func reserveGroup(ctx context.Context, client ReservationTransferClient, projectKey, agentName string, paths []string, ttlSeconds int, exclusive bool, fromAgent string) ([]string, []agentmail.ReservationConflict, error) {
	res, err := client.ReservePaths(ctx, agentmail.FileReservationOptions{
		ProjectKey: projectKey,
		AgentName:  agentName,
		Paths:      paths,
		TTLSeconds: ttlSeconds,
		Exclusive:  exclusive,
		Reason:     fmt.Sprintf("handoff transfer from %s", fromAgent),
	})

	var granted []string
	var conflicts []agentmail.ReservationConflict
	if res != nil {
		for _, g := range res.Granted {
			granted = append(granted, g.PathPattern)
		}
		conflicts = append(conflicts, res.Conflicts...)
	}

	return granted, conflicts, err
}

func rollbackReservations(ctx context.Context, client ReservationTransferClient, projectKey, agentName string, ttlSeconds int, exclusive, shared []string) error {
	_, _, err := reserveAll(ctx, client, projectKey, agentName, ttlSeconds, agentName, exclusive, shared)
	if err != nil {
		return err
	}
	return nil
}

func waitWithContext(ctx context.Context, d time.Duration) error {
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
