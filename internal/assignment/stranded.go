package assignment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// A dispatch_state="sending" row is the at-most-once ambiguity barrier: the
// process that owned it stopped between recording the barrier and recording the
// transport outcome, so nothing on disk says whether the assignment message was
// actually delivered. The barrier is deliberately fail-closed — every retire,
// clear, and retry path refuses it — which means a row stranded there keeps its
// occupancy_key and owns its pane forever (ntm#304).
//
// The only safe way out is external evidence. These two entry points are the
// evidence-consuming half of that resolution; the coordinator supplies the
// evidence and is responsible for its strength. Neither one decides anything on
// its own, and both are exact-generation, idempotent, and no-ops on any row that
// is not still sitting on the barrier.

// AdoptStrandedDispatchReceiptIfCurrent resolves an outcome-unknown dispatch in
// the *delivered* direction: the caller found the message the stranded dispatch
// was trying to send, so the row is completed forward to "sent" against the
// discovered receipt. This is the safe resolution — the assignment is already
// in the recipient's hands, so it must never be dispatched a second time and
// must never be treated as a non-delivery.
//
// evidence is persisted verbatim as the audit record for the repair.
func (s *AssignmentStore) AdoptStrandedDispatchReceiptIfCurrent(ctx context.Context, observed *Assignment, receipt DispatchReceipt, evidence string) (*Assignment, bool, error) {
	if err := validateDispatchReceipt(receipt); err != nil {
		return nil, false, err
	}
	adopted, applied, err := s.resolveStrandedDispatch(ctx, observed, evidence, func(current *Assignment, now time.Time) {
		// A delivered dispatch exposes the row to assigned/working consumers,
		// exactly as RecordAtomicDispatchSent does — but never walk a row that
		// has already moved on backwards to "assigned".
		if current.Status == StatusClaiming || current.Status == StatusClaimed {
			current.Status = StatusAssigned
		}
		if strings.TrimSpace(current.PendingPrompt) != "" {
			current.PromptSent = current.PendingPrompt
		}
		current.PendingPrompt = ""
		current.DispatchState = DispatchSent
		current.DispatchedAt = cloneTimePtr(&now)
		current.DispatchReceiptID = receipt.DeliveryID
		if receipt.Duration > 0 {
			current.DispatchDuration = receipt.Duration
		}
		current.LastDispatchError = ""
	})
	if err != nil || !applied {
		return adopted, applied, err
	}
	emitAtomicAssignmentEvent(s.SessionName, adopted)
	return adopted, applied, nil
}

// ResolveStrandedDispatchIfCurrent resolves an outcome-unknown dispatch in the
// *non-delivery* direction: the caller proved the dispatch target is gone from
// live topology and that no message for this exact generation ever landed. The
// row returns to "pending", which is the retryable state, so the caller may then
// retire it through the ordinary terminal-reconciliation path (releasing its
// reservations, its claim, and finally its pane).
//
// This is the dangerous direction — declaring an unknown outcome to be a
// non-delivery — so it is deliberately inert unless the row is still on the
// barrier, is the exact generation the caller observed, and has no clear
// already in flight. evidence is persisted verbatim and must name the concrete
// observation that justified the call.
func (s *AssignmentStore) ResolveStrandedDispatchIfCurrent(ctx context.Context, observed *Assignment, evidence string) (*Assignment, bool, error) {
	return s.resolveStrandedDispatch(ctx, observed, evidence, func(current *Assignment, _ time.Time) {
		current.DispatchState = DispatchPending
		current.LastDispatchError = evidence
	})
}

func (s *AssignmentStore) resolveStrandedDispatch(ctx context.Context, observed *Assignment, evidence string, apply func(*Assignment, time.Time)) (*Assignment, bool, error) {
	if observed == nil || strings.TrimSpace(observed.BeadID) == "" {
		return nil, false, errors.New("observed assignment generation is required")
	}
	if strings.TrimSpace(evidence) == "" {
		return nil, false, fmt.Errorf("resolving stranded dispatch %s requires a recorded evidence reason", observed.BeadID)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	beadID := observed.BeadID
	operationUnlock, err := acquireAtomicBeadOperationLock(ctx, s.path, beadID)
	if err != nil {
		return nil, false, fmt.Errorf("lock stranded dispatch resolution %s: %w", beadID, err)
	}
	defer operationUnlock()
	if err := s.LoadStrict(); err != nil {
		return nil, false, fmt.Errorf("refresh stranded dispatch resolution %s: %w", beadID, err)
	}

	s.mutex.Lock()
	defer s.mutex.Unlock()
	current := s.Assignments[beadID]
	if !SameAssignmentGeneration(observed, current) {
		return nil, false, nil
	}
	// Anything other than a live barrier means somebody else already resolved
	// this row — including this process on an earlier cycle. Report "not
	// applied" rather than re-deciding an outcome that is no longer unknown.
	if current.DispatchState != DispatchSending {
		return nil, false, nil
	}
	if current.ClearState != ClearStateNone {
		return nil, false, nil
	}

	previous := cloneAssignment(current)
	now := time.Now().UTC()
	apply(current, now)
	current.StrandedDispatchReason = evidence
	current.StrandedDispatchResolvedAt = cloneTimePtr(&now)
	if s.replace == nil {
		s.replace = make(map[string]struct{})
	}
	s.replace[beadID] = struct{}{}
	if err := s.saveLocked(); err != nil {
		var concurrentMutation *ConcurrentMutationError
		if errors.As(err, &concurrentMutation) {
			return nil, false, nil
		}
		s.Assignments[beadID] = previous
		delete(s.replace, beadID)
		return nil, false, err
	}
	return cloneAssignment(current), true, nil
}
