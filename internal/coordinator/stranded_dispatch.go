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
	"github.com/Dicklesworthstone/ntm/internal/status"
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
//  1. Live topology. The pane named by the row's occupancy_key (or, failing
//     that, its dispatch_target) is absent from the most recent *successful*
//     whole-session pane listing, which includes panes carrying no recognized
//     agent. Absence is never inferred from an observation that failed, from a
//     stale one, or from the eligible-candidate set — candidates exclude busy
//     and unhealthy panes, which are very much still alive.
//
//  2. Non-delivery. NTM dispatches an assignment as an Agent Mail message
//     addressed to an agent *name*, not to a pane, so a dead pane alone does
//     not prove the message is unreachable: a message that landed can still be
//     read by that name later. The recipient's mailbox is therefore probed for
//     the message this exact generation was sending, and the row is only
//     declared undelivered when the probe completes and finds nothing.
//
// A third check falls out of the second: if the row's recipient is registered
// to some *other* live pane, the recipient is alive and the dead pane proves
// nothing, so the row stays fail-closed without probing at all.
//
// Anything else — an unavailable or stale topology, a pane that still exists, a
// probe that fails or comes back saturated, a row with nothing to probe with —
// leaves the barrier exactly as it was. And when the probe *finds* the message, the row is
// repaired forward to "sent" instead: that is the race where the dispatch
// really did land and only the bookkeeping was lost, and it must not be
// mistaken for a non-delivery.

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

// livePaneTopology returns a copy of the physical pane set from the most recent
// successful whole-session observation, plus the time it was taken. ok is false
// when no such observation exists.
func (c *SessionCoordinator) livePaneTopology() (map[string]struct{}, time.Time, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.livePaneTopologyValid {
		return nil, time.Time{}, false
	}
	snapshot := make(map[string]struct{}, len(c.livePaneIDs))
	for paneID := range c.livePaneIDs {
		snapshot[paneID] = struct{}{}
	}
	return snapshot, c.livePaneTopologyAt, true
}

// probeDispatchDelivery answers "did the message this generation was sending
// actually land?" against the recipient's Agent Mail inbox.
//
// The match is deliberately liberal — subject, sender, and a creation time at
// or after the recorded dispatch start — because the two errors are not
// symmetric. A false "landed" only leaves the existing stall in place, which an
// operator can already clear by hand. A false "not landed" authorizes a second
// dispatch of work that is already out. When in doubt, say it landed.
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
	sender := strings.TrimSpace(c.agentName)
	for _, message := range messages {
		if strings.TrimSpace(message.Subject) != subject {
			continue
		}
		if sender != "" && strings.TrimSpace(message.From) != sender {
			continue
		}
		if !coordinatorProbeMessageIsRecentEnough(recorded, message) {
			continue
		}
		return assignmentstore.DispatchReceipt{
			DeliveryID: fmt.Sprintf("agent-mail-message-%d", message.ID),
		}, true, nil
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

// recipientLivesOnAnotherPane reports whether the row's Agent Mail recipient is
// currently registered to some other live pane. Dispatch is addressed to a
// name, not to a pane, so a recipient that is alive elsewhere can still act on
// a message that landed — which makes the dead pane no evidence at all.
func (c *SessionCoordinator) recipientLivesOnAnotherPane(recipient string) (string, bool) {
	recipient = strings.TrimSpace(recipient)
	if recipient == "" {
		return "", false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for paneID, agent := range c.agents {
		if agent == nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(agent.AgentMailName), recipient) {
			return paneID, true
		}
	}
	return "", false
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

	target := strandedDispatchTarget(recorded)
	if target == "" {
		return outcomeUnknown("assignment records no dispatch target to evaluate")
	}
	topology, observedAt, ok := c.livePaneTopology()
	if !ok {
		return outcomeUnknown("live pane topology is unavailable")
	}
	if !status.DispatchObservationIsCurrent(observedAt, time.Now()) {
		return outcomeUnknown("live pane topology observation is not current")
	}
	if _, alive := topology[target]; alive {
		// The pane still exists. Whether it is idle, busy, or unrecognized is
		// beside the point: an agent there may already hold this assignment.
		return outcomeUnknown("")
	}

	if paneID, alive := c.recipientLivesOnAnotherPane(recorded.AgentName); alive {
		return outcomeUnknown(fmt.Sprintf(
			"dispatch target %s is gone but its Agent Mail recipient %s is live on pane %s",
			target, strings.TrimSpace(recorded.AgentName), paneID))
	}

	receipt, landed, err := c.probeDispatchDelivery(ctx, recorded)
	if err != nil {
		return outcomeUnknown(fmt.Sprintf("dispatch target %s is gone but delivery to %s could not be verified: %v",
			target, strings.TrimSpace(recorded.AgentName), err))
	}
	if landed {
		evidence := fmt.Sprintf(
			"dispatch outcome recovered: assignment message %s was delivered to %s before the recording process stopped",
			receipt.DeliveryID, strings.TrimSpace(recorded.AgentName))
		adopted, applied, adoptErr := store.AdoptStrandedDispatchReceiptIfCurrent(ctx, recorded, receipt, evidence)
		if adoptErr != nil {
			return outcomeUnknown(fmt.Sprintf("recording recovered delivery %s failed: %v", receipt.DeliveryID, adoptErr))
		}
		if !applied {
			return outcomeUnknown("recovered delivery no longer matches the observed assignment generation")
		}
		result := base
		result.Assignment = coordinatorWorkAssignmentFromRecord(adopted)
		result.MessageSent = true
		result.Success = true
		return result
	}

	evidence := fmt.Sprintf(
		"dispatch target %s is absent from the live tmux topology observed at %s, and no assignment message for this generation reached %s",
		target, observedAt.UTC().Format(time.RFC3339), strings.TrimSpace(recorded.AgentName))
	resolved, applied, err := store.ResolveStrandedDispatchIfCurrent(ctx, recorded, evidence)
	if err != nil {
		return outcomeUnknown(fmt.Sprintf("resolving the stranded dispatch failed: %v", err))
	}
	if !applied {
		return outcomeUnknown("the stranded dispatch no longer matches the observed assignment generation")
	}
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
