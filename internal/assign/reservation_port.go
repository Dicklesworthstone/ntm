package assign

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
)

// AtomicReservationPort adapts FileReservationManager onto the
// assignment.ReservationPort interface used by AtomicCoordinator.
type AtomicReservationPort struct {
	manager       *FileReservationManager
	projectDir    string
	discoverPaths func(context.Context, assignment.ReservationRequest) []string
}

// NewAtomicReservationPort constructs a reusable AtomicReservationPort.
func NewAtomicReservationPort(
	manager *FileReservationManager,
	projectDir string,
	discoverPaths func(context.Context, assignment.ReservationRequest) []string,
) *AtomicReservationPort {
	return &AtomicReservationPort{
		manager:       manager,
		projectDir:    projectDir,
		discoverPaths: discoverPaths,
	}
}

// Reserve attempts to acquire file reservations for req via Agent Mail.
func (p *AtomicReservationPort) Reserve(ctx context.Context, req assignment.ReservationRequest) (assignment.LeaseReceipt, error) {
	if p == nil || p.manager == nil {
		return assignment.LeaseReceipt{AgentName: req.AgentName, Target: req.Target},
			assignment.GuaranteeNoReservation(errors.New("file reservation manager is not configured"))
	}
	paths := req.RequestedPaths
	if len(paths) == 0 && p.discoverPaths != nil {
		paths = p.discoverPaths(ctx, req)
	}
	var result *FileReservationResult
	var err error
	if len(paths) > 0 {
		result, err = p.manager.ReservePathsForBead(ctx, req.BeadID, req.AgentName, paths)
	} else {
		result, err = p.manager.ReserveForBead(ctx, req.BeadID, req.BeadTitle, "", req.AgentName)
	}
	return ClassifyReservationAttempt(req, result, err)
}

// ReconcileReservation inspects Agent Mail after an ambiguous reservation
// attempt and classifies whether durable lease handles exist.
func (p *AtomicReservationPort) ReconcileReservation(ctx context.Context, req assignment.ReservationRequest, _ assignment.LeaseReceipt) (assignment.ReservationReconciliation, error) {
	requested := append([]string(nil), req.RequestedPaths...)
	if len(requested) == 0 && p != nil && p.discoverPaths != nil {
		requested = p.discoverPaths(ctx, req)
	}
	if p == nil || p.manager == nil {
		return assignment.ReservationReconciliation{State: assignment.ReservationReconciliationUnknown},
			errors.New("file reservation manager is not configured")
	}
	result, err := p.manager.ReconcileForBead(ctx, req.BeadID, req.AgentName, requested)
	lease, _ := ClassifyReservationAttempt(req, result, err)
	if err != nil {
		return assignment.ReservationReconciliation{State: assignment.ReservationReconciliationUnknown, Lease: lease}, err
	}
	if len(lease.ReservationIDs) == 0 && len(lease.Granted) == 0 {
		return assignment.ReservationReconciliation{State: assignment.ReservationReconciliationAbsent, Lease: lease}, nil
	}
	return assignment.ReservationReconciliation{State: assignment.ReservationReconciliationReserved, Lease: lease}, nil
}

// ClassifyReservationAttempt converts a FileReservationResult and transport
// error into a LeaseReceipt with fail-closed release-required or
// guaranteed-no-reservation error wrappers.
func ClassifyReservationAttempt(req assignment.ReservationRequest, result *FileReservationResult, reservationErr error) (assignment.LeaseReceipt, error) {
	lease := assignment.LeaseReceipt{AgentName: req.AgentName, Target: req.Target}
	var knownFailure error
	if result != nil {
		lease.Requested = append([]string(nil), result.RequestedPaths...)
		lease.Granted = append([]string(nil), result.GrantedPaths...)
		lease.ReservationIDs = append([]int(nil), result.ReservationIDs...)
		if result.ExpiresAt != nil {
			expiresAt := *result.ExpiresAt
			lease.ExpiresAt = &expiresAt
		}
		if len(result.Conflicts) > 0 {
			paths := make([]string, 0, len(result.Conflicts))
			for _, conflict := range result.Conflicts {
				paths = append(paths, conflict.Path)
			}
			knownFailure = fmt.Errorf("file reservation conflicts: %s", strings.Join(paths, ", "))
		} else if !result.Success && reservationErr == nil {
			message := strings.TrimSpace(result.Error)
			if message == "" {
				message = "file reservation was not granted"
			}
			knownFailure = errors.New(message)
		}
	}
	hasHandles := len(lease.ReservationIDs) > 0 || len(lease.Granted) > 0
	if knownFailure != nil {
		if hasHandles {
			return lease, assignment.RequireReservationRelease(knownFailure)
		}
		return lease, assignment.GuaranteeNoReservation(knownFailure)
	}
	if reservationErr != nil && hasHandles {
		return lease, assignment.RequireReservationRelease(reservationErr)
	}
	return lease, reservationErr
}

// WorkingReplacementReleaseReceipt normalizes the released paths and
// reservation IDs recorded when releasing a replaced working assignment's leases.
func WorkingReplacementReleaseReceipt(current *assignment.Assignment, released []string, releaseErr error) assignment.WorkingReplacementReleaseReceipt {
	receipt := assignment.WorkingReplacementReleaseReceipt{ReleasedPaths: NormalizedReleasePaths(released)}
	if releaseErr != nil || current == nil {
		return receipt
	}
	receipt.ReleasedPaths = NormalizedReleasePaths(
		append(append(append([]string(nil), current.ReservedPaths...), current.ReservationRequested...), current.ReservationInputPaths...),
	)
	seenIDs := make(map[int]struct{}, len(current.ReservationIDs))
	for _, id := range current.ReservationIDs {
		if id <= 0 {
			continue
		}
		if _, duplicate := seenIDs[id]; duplicate {
			continue
		}
		seenIDs[id] = struct{}{}
		receipt.ReleasedReservationIDs = append(receipt.ReleasedReservationIDs, id)
	}
	return receipt
}

// NormalizedReleasePaths trims and deduplicates reservation path lists while
// preserving first-seen order.
func NormalizedReleasePaths(paths []string) []string {
	normalized := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, rawPath := range paths {
		path := strings.TrimSpace(rawPath)
		if path == "" {
			continue
		}
		if _, duplicate := seen[path]; duplicate {
			continue
		}
		seen[path] = struct{}{}
		normalized = append(normalized, path)
	}
	return normalized
}
