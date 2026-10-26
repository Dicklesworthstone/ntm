package coordinator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	assignmentstore "github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/bv"
)

// Resolution of a stranded dispatch_state="sending" row (ntm#304).
//
// The barrier exists because a row in "sending" records that a dispatch was
// attempted and nothing more: the process that owned it stopped before it could
// write down whether the message went out. Every ledger path refuses such a row
// on purpose, so a row stranded there keeps its occupancy_key and owns its pane
// forever. Recovery used to require `ntm assign --clear <bead> --force`.
//
// Terminalizing the row means asserting a non-delivery, and getting that wrong
// converts a permanent stall into a silent DOUBLE DISPATCH — two agents on one
// bead — which is strictly worse than the stall. So the assertion is only made
// when two independent facts both hold:
//
//  1. Live ownership. After the mailbox probe, the same proof used for
//     delivered-owner recovery refreshes local topology and verifies every
//     tmux session, Agent Mail binding, and exact tracker claim. A moved pane
//     or a rebound recipient still owns the work. This proof is obtained under
//     the external cleanup lock, before persisting the terminal barrier.
//
//  2. Non-delivery. NTM dispatches an assignment as an Agent Mail message
//     addressed to an agent *name*, not to a pane, so a dead pane alone does
//     not prove the message is unreachable: a message that landed can still be
//     read by that name later. The recipient's mailbox is therefore probed for
//     the message this exact generation was sending, and the row is only
//     declared undelivered when the probe completes and finds nothing.
//
// A positive mailbox receipt repairs the row forward to "sent" even if the
// owner is still live: delivery proof does not require owner loss. Anything
// else — unavailable or stale ownership evidence, a live owner, or a failed,
// malformed, or saturated negative mailbox probe — leaves the barrier intact.

const (
	// coordinatorDispatchProbeSkew tolerates clock skew between this host and
	// the Agent Mail server when bounding the probe by the dispatch start.
	coordinatorDispatchProbeSkew = 5 * time.Minute
	// coordinatorDispatchProbeLimit bounds one mailbox probe.
	coordinatorDispatchProbeLimit = 200
)

// dispatchDeliveryProbe reports whether the assignment message that a stranded
// generation was sending is present in its recipient's mailbox. A non-nil error
// means the question could not be answered and the caller must fail closed.
type dispatchDeliveryProbe func(context.Context, *assignmentstore.Assignment) (assignmentstore.DispatchReceipt, bool, error)

func coordinatorAssignmentSubject(beadID string) string {
	return fmt.Sprintf("Work Assignment: %s", strings.TrimSpace(beadID))
}

// strandedDispatchTarget names the pane a stranded row owns, using the same
// canonical identity the occupancy filter uses. A row whose identity is not
// canonical cannot be compared against a tmux pane id at all, so it yields no
// target and the caller fails closed.
func strandedDispatchTarget(recorded *assignmentstore.Assignment) string {
	target, err := assignmentstore.CanonicalPaneIdentity(recorded)
	if err != nil {
		return ""
	}
	return target
}

// probeDispatchDelivery answers "did the message this generation was sending
// actually land?" against the recipient's Agent Mail inbox.
//
// The match is deliberately liberal — subject and a creation time at or after
// the recorded dispatch start — because a false "not landed" authorizes a
// second dispatch of work that is already out. The sending coordinator's name
// is not persisted on old assignments and can change after a restart; the
// current name therefore cannot exclude a message from recovery evidence.
func (c *SessionCoordinator) probeDispatchDelivery(ctx context.Context, recorded *assignmentstore.Assignment) (assignmentstore.DispatchReceipt, bool, error) {
	if c.dispatchDeliveryProbeFn != nil {
		return c.dispatchDeliveryProbeFn(ctx, recorded)
	}
	if recorded == nil {
		return assignmentstore.DispatchReceipt{}, false, errors.New("assignment generation is required")
	}
	if c.mailClient == nil {
		return assignmentstore.DispatchReceipt{}, false, errors.New("agent mail client is not configured")
	}
	recipient := strings.TrimSpace(recorded.AgentName)
	if recipient == "" {
		return assignmentstore.DispatchReceipt{}, false, errors.New("assignment records no Agent Mail recipient")
	}
	opts := agentmail.FetchInboxOptions{
		ProjectKey:    c.projectKey,
		AgentName:     recipient,
		Limit:         coordinatorDispatchProbeLimit,
		IncludeBodies: true,
	}
	if recorded.DispatchStartedAt != nil && !recorded.DispatchStartedAt.IsZero() {
		since := recorded.DispatchStartedAt.Add(-coordinatorDispatchProbeSkew)
		opts.SinceTS = &since
	}
	messages, err := c.mailClient.FetchInbox(ctx, opts)
	if err != nil {
		return assignmentstore.DispatchReceipt{}, false, fmt.Errorf("probe %s inbox: %w", recipient, err)
	}
	subject := coordinatorAssignmentSubject(recorded.BeadID)
	var malformed error
	for _, message := range messages {
		if message.ID <= 0 || strings.TrimSpace(message.Subject) == "" {
			// A zero-value row (including a JSON null) is not an inspected
			// unrelated message. It may be the delivery we are looking for.
			malformed = errors.New("mailbox response contains a message without a valid ID or subject")
			continue
		}
		if strings.TrimSpace(message.Subject) != subject {
			continue
		}
		if !coordinatorProbeMessageIsRecentEnough(recorded, message) {
			continue
		}
		return assignmentstore.DispatchReceipt{
			DeliveryID: fmt.Sprintf("agent-mail-message-%d", message.ID),
		}, true, nil
	}
	if malformed != nil {
		return assignmentstore.DispatchReceipt{}, false, fmt.Errorf("probe %s inbox: %w", recipient, malformed)
	}
	// A full page is not an exhaustive answer. Reporting "nothing landed" from
	// a truncated listing would be exactly the false negative that authorizes a
	// second dispatch, so a saturated probe is inconclusive instead.
	if len(messages) >= coordinatorDispatchProbeLimit {
		return assignmentstore.DispatchReceipt{}, false, fmt.Errorf(
			"probe %s inbox: %d messages returned is the whole page, so non-delivery cannot be established",
			recipient, len(messages))
	}
	return assignmentstore.DispatchReceipt{}, false, nil
}

// coordinatorProbeMessageIsRecentEnough keeps an older generation's message for
// the same bead from being read as this generation's delivery. A message with
// no usable timestamp counts as a match: an unreadable timestamp is not
// evidence of non-delivery.
func coordinatorProbeMessageIsRecentEnough(recorded *assignmentstore.Assignment, message agentmail.InboxMessage) bool {
	if recorded == nil || recorded.DispatchStartedAt == nil || recorded.DispatchStartedAt.IsZero() {
		return true
	}
	created := message.CreatedTS.Time
	if created.IsZero() {
		return true
	}
	return !created.Before(recorded.DispatchStartedAt.Add(-coordinatorDispatchProbeSkew))
}

// resolveStrandedDispatch handles one dispatch_state="sending" row. It returns
// the AssignmentResult to report for that row this cycle and never dispatches
// anything itself.
func (c *SessionCoordinator) resolveStrandedDispatch(
	ctx context.Context,
	store *assignmentstore.AssignmentStore,
	recorded *assignmentstore.Assignment,
	work *WorkAssignment,
) AssignmentResult {
	base := AssignmentResult{
		Assignment: work, ClaimActor: recorded.ClaimActor, IdempotencyKey: recorded.IdempotencyKey,
	}
	outcomeUnknown := func(detail string) AssignmentResult {
		result := base
		result.Error = assignmentstore.ErrDispatchOutcomeUnknown.Error()
		if strings.TrimSpace(detail) != "" {
			result.Error = fmt.Sprintf("%s: %s", result.Error, detail)
		}
		return result
	}

	resolved, landed, err := c.recoverStrandedDispatchEvidence(ctx, store, recorded)
	if err != nil {
		if resolved != nil && resolved.DispatchState != assignmentstore.DispatchSending {
			result := base
			result.Error = err.Error()
			return result
		}
		return outcomeUnknown(err.Error())
	}
	if landed {
		result := base
		result.Assignment = coordinatorWorkAssignmentFromRecord(resolved)
		result.MessageSent = true
		result.Success = true
		return result
	}

	evidence := resolved.StrandedDispatchReason
	completed, err := c.reconcileTerminalAssignment(ctx, store, resolved, assignmentstore.StatusFailed, evidence)
	if err != nil {
		result := base
		result.Error = fmt.Sprintf("retiring the stranded assignment failed: %v", err)
		return result
	}
	if !completed {
		// The barrier is gone but the row was not retired (its generation moved
		// under us). It is no longer outcome-unknown, so do not say it is.
		result := base
		result.Error = "the stranded dispatch was resolved but its assignment was not retired this cycle"
		return result
	}
	// The pane and the bead are free again; the cached recommendation set must
	// be able to observe that within this same cycle.
	bv.InvalidateTriageCache()
	result := base
	result.Error = evidence
	return result
}

// recoverStrandedDispatchEvidence serializes observation with active dispatch,
// replacement, and cleanup. Waiting for an active sender must happen before
// probing: a mailbox read taken before that wait could otherwise become false
// non-delivery evidence while the sender finishes its request.
func (c *SessionCoordinator) recoverStrandedDispatchEvidence(ctx context.Context, store *assignmentstore.AssignmentStore, recorded *assignmentstore.Assignment) (*assignmentstore.Assignment, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	unlock, err := store.AcquireExternalCleanupLock(ctx, recorded.BeadID)
	if err != nil {
		return nil, false, fmt.Errorf("lock stranded assignment recovery: %w", err)
	}
	defer unlock()
	if err := store.LoadStrict(); err != nil {
		return nil, false, fmt.Errorf("refresh stranded assignment: %w", err)
	}
	current := store.Get(recorded.BeadID)
	if !assignmentstore.SameAssignmentGeneration(recorded, current) || current.ClearState != assignmentstore.ClearStateNone {
		return nil, false, errors.New("stranded assignment generation or cleanup state changed")
	}
	if current.DispatchState == assignmentstore.DispatchSent && current.DispatchReceiptID != "" {
		return current, true, nil
	}
	if current.DispatchState != assignmentstore.DispatchSending {
		return nil, false, errors.New("assignment no longer has an outcome-unknown dispatch")
	}
	receipt, landed, err := c.probeDispatchDelivery(ctx, current)
	if err != nil {
		return nil, false, fmt.Errorf("delivery to %s could not be verified: %w", strings.TrimSpace(current.AgentName), err)
	}
	if landed {
		evidence := fmt.Sprintf(
			"dispatch outcome recovered: assignment message %s was delivered to %s before the recording process stopped",
			receipt.DeliveryID, strings.TrimSpace(current.AgentName))
		adopted, applied, err := store.AdoptStrandedDispatchReceiptIfCurrent(ctx, current, receipt, evidence)
		if err != nil {
			return nil, false, fmt.Errorf("record recovered delivery: %w", err)
		}
		if !applied {
			return nil, false, errors.New("recovered delivery no longer matches the observed assignment generation")
		}
		return adopted, true, nil
	}
	// A negative probe may be slow. Re-observe ownership after it, including
	// global tmux topology and registries, rather than reusing a local snapshot
	// taken before an owner moved or its recipient was rebound.
	if err := c.verifyAssignmentOwnerLoss(ctx, current); err != nil {
		return nil, false, fmt.Errorf("stranded assignment owner loss is unproven: %w", err)
	}
	evidence := fmt.Sprintf(
		"dispatch target %s has no live owner in verified server-wide tmux topology, its exact claim and recipient bindings are unchanged, and no assignment message for this generation reached %s",
		strandedDispatchTarget(current), strings.TrimSpace(current.AgentName))
	resolved, applied, err := store.ResolveStrandedDispatchIfCurrent(ctx, current, evidence)
	if err != nil {
		return nil, false, fmt.Errorf("resolve stranded dispatch: %w", err)
	}
	if !applied {
		return nil, false, errors.New("stranded dispatch no longer matches the observed assignment generation")
	}
	// Persist retirement before releasing the cleanup lock so another
	// coordinator cannot send this now-pending generation between resolution
	// and the external lease/claim cleanup.
	begin := store.BeginTerminalReconciliationIfCurrent
	if c.completionEvents {
		begin = store.BeginTerminalReconciliationWithCompletionEventIfCurrent
	}
	barrier, applied, err := begin(ctx, resolved, assignmentstore.StatusFailed, evidence)
	if err != nil {
		return resolved, false, fmt.Errorf("persist stranded assignment retirement: %w", err)
	}
	if !applied {
		return resolved, false, errors.New("resolved assignment changed before its retirement barrier")
	}
	return barrier, false, nil
}
