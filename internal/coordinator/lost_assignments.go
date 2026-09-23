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
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// reconcileLostAssignments recovers delivered work whose bound physical pane
// has disappeared. A busy, stalled, unhealthy, or unreadable pane is still an
// owner: only absence from a successful complete topology can start recovery.
// Delivery remains recorded as sent; this is task failure, never evidence that
// the old prompt may be replayed. Normal admission can claim the reopened task
// only after the existing exact-lease and exact-claim cleanup barrier finishes.
func (c *SessionCoordinator) reconcileLostAssignments(ctx context.Context, store *assignmentstore.AssignmentStore) error {
	var failures []error
	for _, current := range store.ListActive() {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if current == nil || current.ClearState != assignmentstore.ClearStateNone ||
			current.DispatchState != assignmentstore.DispatchSent ||
			(current.Status != assignmentstore.StatusAssigned && current.Status != assignmentstore.StatusWorking) {
			continue
		}
		target, err := assignmentstore.CanonicalPaneIdentity(current)
		if err != nil {
			// Legacy identities cannot prove which physical pane disappeared.
			continue
		}
		// This snapshot only selects a possible loss. Slow tracker cleanup
		// may have aged it; authorization always obtains fresh topology again.
		panes, _, complete := c.livePaneTopology()
		if !complete {
			continue
		}
		if _, exists := panes[target]; exists {
			continue
		}
		reason := fmt.Sprintf("delivered assignment owner %s lost physical pane %s; exact ownership verified before recovery", current.AgentName, target)
		retired, err := c.reconcileTerminalAssignmentAuthorized(ctx, store, current, assignmentstore.StatusFailed, reason, c.verifyLostAssignmentOwner)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if retired {
			bv.InvalidateTriageCache()
		}
	}
	return errors.Join(failures...)
}

func (c *SessionCoordinator) verifyLostAssignmentOwner(ctx context.Context, current *assignmentstore.Assignment) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if current.DispatchState != assignmentstore.DispatchSent || strings.TrimSpace(current.DispatchReceiptID) == "" ||
		strings.TrimSpace(current.IdempotencyKey) == "" || strings.TrimSpace(current.ClaimActor) == "" || strings.TrimSpace(current.AgentName) == "" {
		return errors.New("lost owner recovery requires a durable delivery receipt, generation, recipient, and claim actor")
	}
	target, err := assignmentstore.CanonicalPaneIdentity(current)
	if err != nil {
		return err
	}
	if current.ReservationRequired || len(current.ReservationIDs) > 0 || len(current.ReservedPaths) > 0 {
		if current.ReservationState != assignmentstore.ReservationReserved || !current.ReservationCompleted ||
			current.ReservationAgent != current.AgentName || current.ReservationTarget != target ||
			len(current.ReservationIDs) == 0 || len(current.ReservedPaths) == 0 {
			return errors.New("lost owner recovery requires the assignment's exact reservation owner, target, IDs, and paths")
		}
	}
	lookup := c.workItemDetailsFn
	if lookup == nil {
		lookup = func(ctx context.Context, beadID string) (*bv.BeadAssignmentDetails, error) {
			return bv.GetBeadAssignmentDetailsContext(ctx, c.projectKey, beadID)
		}
	}
	details, err := lookup(ctx, current.BeadID)
	if err != nil {
		return fmt.Errorf("read lost assignment claim: %w", err)
	}
	if details == nil || details.ID != current.BeadID || strings.TrimSpace(details.Assignee) != current.ClaimActor {
		return errors.New("lost assignment no longer has its exact Beads claim owner")
	}
	switch strings.ToLower(strings.TrimSpace(details.Status)) {
	case "open", "in_progress":
	default:
		return fmt.Errorf("live work status %q does not authorize lost owner recovery", details.Status)
	}

	// Refresh after acquiring the cleanup lock and after the claim lookup. An
	// unrelated capture failure may coexist with complete topology; a failed
	// list must invalidate it. Never use eligible agents as a topology proxy.
	observationErr := c.Observe(ctx)
	if err := ctx.Err(); err != nil {
		return errors.Join(observationErr, err)
	}
	panes, observedAt, complete := c.livePaneTopology()
	if !complete || !status.DispatchObservationIsCurrent(observedAt, time.Now()) {
		return errors.Join(observationErr, errors.New("lost assignment has no fresh complete pane topology"))
	}
	if _, exists := panes[target]; exists {
		return fmt.Errorf("assignment target %s is present; owner loss is not proven", target)
	}
	registry, err := agentmail.LoadSessionAgentRegistry(c.session, c.projectKey)
	if err != nil {
		return fmt.Errorf("read lost assignment owner registry: %w", err)
	}
	if registry == nil || strings.TrimSpace(registry.PaneIDMap[target]) != current.AgentName {
		return fmt.Errorf("missing pane %s no longer has its recorded Agent Mail owner %s", target, current.AgentName)
	}
	for paneID, owner := range registry.PaneIDMap {
		if !strings.EqualFold(strings.TrimSpace(owner), strings.TrimSpace(current.AgentName)) {
			continue
		}
		if _, alive := panes[paneID]; alive {
			return fmt.Errorf("assignment recipient %s is still bound to live pane %s", current.AgentName, paneID)
		}
	}

	// Moving a pane to another session preserves the running agent. Prove
	// absence from the entire tmux server as well, and reject an Agent Mail
	// recipient rebound in another live session for this same project.
	listAll := c.allPanesForRecoveryFn
	if listAll == nil {
		listAll = tmux.GetAllPanesContext
	}
	allObservedAt := time.Now()
	allPanes, err := listAll(ctx)
	if err != nil {
		return fmt.Errorf("verify lost owner across tmux sessions: %w", err)
	}
	if len(allPanes[c.session]) == 0 {
		return errors.New("server-wide topology did not confirm the observed assignment session")
	}
	confirmedLocalPanes := make(map[string]struct{}, len(allPanes[c.session]))
	for session, sessionPanes := range allPanes {
		if strings.TrimSpace(session) == "" {
			return errors.New("server-wide topology has an unnamed session")
		}
		// Registration can change during the server-wide query, including
		// rebinding the recipient to an already-listed local pane. Re-read
		// every registry after that query instead of reusing the earlier one.
		sessionRegistry, err := agentmail.LoadSessionAgentRegistry(session, c.projectKey)
		if err != nil {
			return fmt.Errorf("read possible owner binding in session %s: %w", session, err)
		}
		if session == c.session && (sessionRegistry == nil || strings.TrimSpace(sessionRegistry.PaneIDMap[target]) != current.AgentName) {
			return errors.New("original Agent Mail binding changed during owner-loss verification")
		}
		for _, pane := range sessionPanes {
			if strings.TrimSpace(pane.ID) == "" {
				return errors.New("server-wide topology has an unidentified pane")
			}
			if pane.ID == target {
				return fmt.Errorf("assignment target %s still exists in session %s", target, session)
			}
			if session == c.session {
				confirmedLocalPanes[pane.ID] = struct{}{}
			}
			if sessionRegistry != nil {
				owner, registered := sessionRegistry.GetAgent(pane.Title, pane.ID)
				if registered && strings.EqualFold(strings.TrimSpace(owner), strings.TrimSpace(current.AgentName)) {
					return fmt.Errorf("assignment recipient %s is still bound to pane %s in session %s", current.AgentName, pane.ID, session)
				}
			}
		}
	}
	if len(confirmedLocalPanes) != len(panes) {
		return errors.New("assignment session topology changed during owner-loss verification")
	}
	for paneID := range panes {
		if _, confirmed := confirmedLocalPanes[paneID]; !confirmed {
			return errors.New("assignment session topology changed during owner-loss verification")
		}
	}
	if !status.DispatchObservationIsCurrent(allObservedAt, time.Now()) {
		return errors.New("server-wide owner-loss observation is no longer current")
	}
	return ctx.Err()
}
