package coordinator

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	assignpkg "github.com/Dicklesworthstone/ntm/internal/assign"
	assignmentstore "github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

// ScoreConfig controls how work assignments are scored.
type ScoreConfig struct {
	PreferCriticalPath      bool    // Weight critical path items higher
	PenalizeFileOverlap     bool    // Avoid assigning overlapping files
	UseAgentProfiles        bool    // Match work to agent capabilities
	BudgetAware             bool    // Consider token budgets
	ContextThreshold        float64 // Max context usage before penalizing (percentage 0-100, default 80)
	ProfileTagBoostWeight   float64 // Weight for profile tag matches (default 0.15)
	FocusPatternBoostWeight float64 // Weight for focus pattern matches (default 0.10)
}

// DefaultScoreConfig returns a reasonable default configuration.
func DefaultScoreConfig() ScoreConfig {
	return ScoreConfig{
		PreferCriticalPath:  true,
		PenalizeFileOverlap: true,
		UseAgentProfiles:    true,
		BudgetAware:         true,
		ContextThreshold:    80,
	}
}

// ScoredAssignment pairs an assignment with its computed score breakdown.
type ScoredAssignment struct {
	Assignment     *WorkAssignment
	Recommendation *bv.TriageRecommendation
	Agent          *AgentState
	TotalScore     float64
	ScoreBreakdown AssignmentScoreBreakdown
}

// AssignmentScoreBreakdown shows how the score was computed.
type AssignmentScoreBreakdown struct {
	BaseScore          float64 `json:"base_score"`           // From bv triage score
	AgentTypeBonus     float64 `json:"agent_type_bonus"`     // Bonus for agent-task match
	CriticalPathBonus  float64 `json:"critical_path_bonus"`  // Bonus for critical path items
	FileOverlapPenalty float64 `json:"file_overlap_penalty"` // Penalty for file conflicts
	ContextPenalty     float64 `json:"context_penalty"`      // Penalty for high context usage
	ProfileTagBonus    float64 `json:"profile_tag_bonus"`    // Bonus for profile tag matches
	FocusPatternBonus  float64 `json:"focus_pattern_bonus"`  // Bonus for focus pattern matches
}

// WorkAssignment represents a work assignment to an agent.
type WorkAssignment struct {
	BeadID         string    `json:"bead_id"`
	BeadTitle      string    `json:"bead_title"`
	AgentPaneID    string    `json:"agent_pane_id"`
	AgentPaneIndex int       `json:"agent_pane_index"`
	AgentMailName  string    `json:"agent_mail_name,omitempty"`
	AgentType      string    `json:"agent_type"`
	AssignedAt     time.Time `json:"assigned_at"`
	Priority       int       `json:"priority"`
	Score          float64   `json:"score"`
	FilesToReserve []string  `json:"files_to_reserve,omitempty"`
}

// AssignmentResult contains the result of an assignment attempt.
type AssignmentResult struct {
	Success        bool            `json:"success"`
	Assignment     *WorkAssignment `json:"assignment,omitempty"`
	Error          string          `json:"error,omitempty"`
	Reservations   []string        `json:"reservations,omitempty"`
	MessageSent    bool            `json:"message_sent"`
	ClaimActor     string          `json:"claim_actor,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
}

// coordinatorDispatchAttemptLimit bounds retries that are proven to have
// stopped before transport actuation. Outcome-unknown sends are never retried;
// they remain behind the ambiguity barrier unless their physical pane dies.
const coordinatorDispatchAttemptLimit = 3

// AssignWork assigns verified actionable work to idle agents.
func (c *SessionCoordinator) AssignWork(ctx context.Context) ([]AssignmentResult, error) {
	if !c.config.AutoAssign {
		return nil, nil
	}
	store, err := assignmentstore.LoadStoreStrict(c.session)
	if err != nil {
		return nil, fmt.Errorf("loading assignment ledger: %w", err)
	}
	// Reconcile before candidate and planning gates. A fully occupied session has
	// no eligible target until terminal work is retired, and an empty actionable
	// set must not leave closed assignments permanently occupying panes.
	if err := c.reconcileTerminalAssignments(ctx, store); err != nil {
		return nil, fmt.Errorf("reconciling assignment ledger: %w", err)
	}

	// Get assignment candidates according to the configured queueing policy.
	assignmentCandidates := c.getAssignmentCandidates()
	if len(assignmentCandidates) == 0 {
		return c.recoverPendingAssignments(ctx, store, assignmentCandidates), nil
	}

	// Automated assignment requires verified plan membership and live labels.
	// This gate must precede pending recovery because recovery can reserve and
	// dispatch work just like a new assignment.
	getActionableRecommendations := bv.GetActionableRecommendationsContext
	if c.actionableRecommendationsFn != nil {
		getActionableRecommendations = c.actionableRecommendationsFn
	}
	recommendations, err := getActionableRecommendations(ctx, c.projectKey, 0)
	if err != nil {
		return nil, fmt.Errorf("getting verified actionable recommendations: %w", err)
	}

	results := c.recoverPendingAssignments(ctx, store, assignmentCandidates)
	if len(recommendations) == 0 {
		return results, nil
	}
	activeBeads := make(map[string]struct{})
	activeAssignments := store.ListActive()
	for _, active := range activeAssignments {
		activeBeads[active.BeadID] = struct{}{}
	}
	assignmentCandidates, err = filterOccupiedAgents(assignmentCandidates, activeAssignments)
	if err != nil {
		return results, fmt.Errorf("validate occupied assignment identities: %w", err)
	}
	if len(assignmentCandidates) == 0 {
		return results, nil
	}
	filtered, terminalRecommendation, err := c.filterActionableRecommendations(ctx, recommendations, activeBeads)
	if terminalRecommendation {
		// BV can lag a tracker transition or a terminal cleanup can race the
		// source update. Do not retain a closed recommendation for the full
		// cache TTL; the next poll must be able to observe its successor.
		bv.InvalidateTriageCache()
	}
	if err != nil {
		return results, err
	}
	recommendations = filtered
	if len(recommendations) == 0 {
		return results, nil
	}

	// Score every agent-task pairing and select the globally best
	// non-conflicting set (bd-v394r). Multi-factor scoring — agent/task
	// affinity, persona tag and focus-pattern routing, critical-path bonus,
	// reservation-load and context-headroom penalties — replaces the previous
	// first-fit match, which handed each agent the first gate-passing
	// recommendation regardless of fit. A failed attempt is not retried with
	// another agent this cycle; the next RunCycle tick re-plans from a fresh
	// snapshot.
	for _, scored := range ScoreAndSelectAssignments(assignmentCandidates, recommendations, DefaultScoreConfig(), nil) {
		result := c.attemptAssignment(ctx, scored.Assignment, scored.Recommendation)
		results = append(results, result)

		if result.Success {
			// Emit event
			select {
			case c.events <- CoordinatorEvent{
				Type:      EventWorkAssigned,
				Timestamp: time.Now(),
				AgentID:   scored.Agent.PaneID,
				Details:   coordinatorWorkAssignedEventDetails(result, scored.Assignment, scored.Agent),
			}:
			default:
			}
		}
	}

	return results, nil
}

func coordinatorWorkAssignedEventDetails(result AssignmentResult, planned *WorkAssignment, agent *AgentState) map[string]any {
	details := map[string]any{
		"bead_id":    "",
		"bead_title": "",
		"agent_type": "",
		"score":      0.0,
	}
	if planned != nil {
		details["bead_id"] = planned.BeadID
		details["score"] = planned.Score
	}
	if agent != nil {
		details["agent_type"] = agent.AgentType
	}
	if result.Assignment != nil {
		details["bead_id"] = result.Assignment.BeadID
		details["bead_title"] = result.Assignment.BeadTitle
	}
	return details
}

func (c *SessionCoordinator) filterActionableRecommendations(ctx context.Context, recommendations []bv.TriageRecommendation, activeBeads map[string]struct{}) ([]bv.TriageRecommendation, bool, error) {
	filtered := make([]bv.TriageRecommendation, 0, len(recommendations))
	terminalRecommendation := false
	for _, recommendation := range recommendations {
		if _, alreadyAssigned := activeBeads[recommendation.ID]; alreadyAssigned {
			continue
		}
		live := recommendation
		var trackerStatus string
		var err error
		switch {
		case c.workItemDetailsFn != nil:
			var details *bv.BeadAssignmentDetails
			details, err = c.workItemDetailsFn(ctx, recommendation.ID)
			if details != nil {
				trackerStatus = details.Status
				live.Title = details.Title
				live.Status = details.Status
				live.Labels = append([]string(nil), details.Labels...)
				live.BlockedBy = append([]string(nil), details.BlockedBy...)
				live.Type = liveIssueType(details.IssueType, recommendation.Type)
			}
		case c.workItemStatusFn != nil:
			trackerStatus, err = c.workItemStatusFn(ctx, recommendation.ID)
			live.Status = trackerStatus
		default:
			var details *bv.BeadAssignmentDetails
			details, err = bv.GetBeadAssignmentDetailsContext(ctx, c.projectKey, recommendation.ID)
			if details != nil {
				trackerStatus = details.Status
				live.Title = details.Title
				live.Status = details.Status
				live.Labels = append([]string(nil), details.Labels...)
				live.BlockedBy = append([]string(nil), details.BlockedBy...)
				live.Type = liveIssueType(details.IssueType, recommendation.Type)
			}
		}
		if err != nil {
			return nil, terminalRecommendation, fmt.Errorf("read recommendation %s status: %w", recommendation.ID, err)
		}
		switch strings.ToLower(strings.TrimSpace(trackerStatus)) {
		case "open":
			// Type evidence is part of the authorization boundary, not a
			// display field: without it the container (epic) gate below cannot
			// prove this item is implementation work. bv's actionable-plan
			// reader already refuses to emit a recommendation with no verified
			// type, so an empty one here means the evidence chain broke —
			// stop rather than dispatch, exactly as a label-coverage gap does.
			if strings.TrimSpace(live.Type) == "" {
				return nil, terminalRecommendation, fmt.Errorf(
					"recommendation %s has no verified issue type; refusing to auto-assign unverified work", recommendation.ID)
			}
			if recommendationPassesSemanticGatesWithOperatorLabels(live, c.operatorGatedLabels) {
				filtered = append(filtered, live)
			}
		case "closed", "tombstone":
			terminalRecommendation = true
		}
	}
	return filtered, terminalRecommendation, nil
}

// liveIssueType prefers the freshly read tracker type over the planned one,
// but never lets an absent live value erase evidence the plan already proved.
func liveIssueType(live, planned string) string {
	if strings.TrimSpace(live) != "" {
		return live
	}
	return planned
}

func recommendationPassesSemanticGates(recommendation bv.TriageRecommendation) bool {
	return recommendationPassesSemanticGatesWithOperatorLabels(recommendation, bv.OperatorGatedLabels())
}

func recommendationPassesSemanticGatesWithOperatorLabels(recommendation bv.TriageRecommendation, gatedLabels []string) bool {
	status := strings.ToLower(strings.TrimSpace(recommendation.Status))
	if status != "open" && status != "ready" {
		return false
	}
	if len(recommendation.BlockedBy) > 0 {
		return false
	}
	// Container types (epic) are grouping nodes, not implementation work.
	// bv.GetActionableRecommendations populates Type specifically so this can
	// be proven, and this gate previously ignored it: `ntm coordinator run`
	// claimed, reserved, and mailed open epics into worker panes that the
	// `ntm assign` classifier refuses. Checked before the operator gate
	// because the exclusion is intrinsic to the type, not a policy that
	// labels could lift.
	if bv.IsContainerBeadType(recommendation.Type) {
		return false
	}
	for _, rawLabel := range recommendation.Labels {
		normalized := strings.ToLower(strings.TrimSpace(rawLabel))
		for _, gatedLabel := range gatedLabels {
			if normalized == strings.ToLower(strings.TrimSpace(gatedLabel)) {
				return false
			}
		}
	}
	return true
}

func (c *SessionCoordinator) recoverPendingAssignments(ctx context.Context, store *assignmentstore.AssignmentStore, candidates []*AgentState) []AssignmentResult {
	if store == nil {
		return []AssignmentResult{{Error: "assignment recovery unavailable: assignment store is not configured"}}
	}
	eligibleTargets := make(map[string]*AgentState, len(candidates))
	for _, candidate := range candidates {
		if candidate != nil && strings.TrimSpace(candidate.PaneID) != "" {
			eligibleTargets[strings.TrimSpace(candidate.PaneID)] = candidate
		}
	}
	physicalTargets, paneSnapshotUsable := c.physicalAssignmentTargets()
	active := store.ListActive()
	sort.Slice(active, func(i, j int) bool {
		if active[i] == nil {
			return false
		}
		if active[j] == nil {
			return true
		}
		return active[i].BeadID < active[j].BeadID
	})

	results := make([]AssignmentResult, 0)
	for _, recorded := range active {
		if recorded == nil || recorded.ClearState == assignmentstore.ClearStateReservationReleasing || recorded.DispatchState == assignmentstore.DispatchSent {
			continue
		}
		work := coordinatorWorkAssignmentFromRecord(recorded)
		target := strings.TrimSpace(recorded.OccupancyKey)
		if target == "" {
			target = strings.TrimSpace(recorded.DispatchTarget)
		}
		if target != "" && paneSnapshotUsable {
			if _, present := physicalTargets[target]; !present {
				reason := fmt.Sprintf("assignment dispatch target %q is absent from the current tmux pane snapshot", target)
				results = append(results, failCoordinatorAssignment(ctx, store, recorded, work, reason))
				continue
			}
		}
		if recorded.DispatchState == assignmentstore.DispatchSending {
			results = append(results, AssignmentResult{
				Assignment: work, ClaimActor: recorded.ClaimActor, IdempotencyKey: recorded.IdempotencyKey,
				Error: assignmentstore.ErrDispatchOutcomeUnknown.Error(),
			})
			continue
		}
		if !coordinatorAssignmentIsRecoverable(recorded) {
			continue
		}
		if recorded.DispatchAttempts >= coordinatorDispatchAttemptLimit {
			reason := fmt.Sprintf("assignment dispatch attempt limit reached for target %q (%d attempts)", target, recorded.DispatchAttempts)
			results = append(results, failCoordinatorAssignment(ctx, store, recorded, work, reason))
			continue
		}
		candidate, eligible := eligibleTargets[target]
		if !eligible {
			results = append(results, AssignmentResult{
				Assignment: work, ClaimActor: recorded.ClaimActor, IdempotencyKey: recorded.IdempotencyKey,
				Error: fmt.Sprintf("assignment recovery target %q is not freshly eligible", target),
			})
			continue
		}
		if identityErr := pendingRecoveryIdentityError(recorded, candidate); identityErr != nil {
			results = append(results, AssignmentResult{
				Assignment: work, ClaimActor: recorded.ClaimActor, IdempotencyKey: recorded.IdempotencyKey,
				Error: identityErr.Error(),
			})
			continue
		}
		results = append(results, c.recoverPendingAssignment(ctx, store, recorded, work))
	}
	return results
}

// physicalAssignmentTargets returns the complete pane topology observed by
// the monitor, including user and unclassified panes omitted from c.agents.
// The boolean is false when topology enumeration failed or no successful
// observation has occurred; an unavailable snapshot can never prove death.
func (c *SessionCoordinator) physicalAssignmentTargets() (map[string]struct{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	targets := make(map[string]struct{}, len(c.assignmentPaneIDs))
	for paneID := range c.assignmentPaneIDs {
		targets[paneID] = struct{}{}
	}
	return targets, c.assignmentPaneSnapshotUsable
}

func failCoordinatorAssignment(ctx context.Context, store *assignmentstore.AssignmentStore, recorded *assignmentstore.Assignment, work *WorkAssignment, reason string) AssignmentResult {
	result := AssignmentResult{
		Assignment: safeCoordinatorWorkProjection(work), ClaimActor: recorded.ClaimActor,
		IdempotencyKey: recorded.IdempotencyKey, Error: reason,
	}
	applied, err := store.MarkFailedIfCurrent(ctx, recorded, reason)
	if err != nil {
		result.Error = fmt.Sprintf("%s: persist terminal assignment: %v", reason, err)
		return result
	}
	if !applied {
		result.Error = fmt.Sprintf("%s: assignment changed before terminalization", reason)
	}
	return result
}

func pendingRecoveryIdentityError(recorded *assignmentstore.Assignment, candidate *AgentState) error {
	if recorded == nil || candidate == nil {
		return errors.New("pending assignment recovery identity is unavailable")
	}
	recordedType := agent.AgentType(strings.TrimSpace(recorded.AgentType)).Canonical()
	candidateType := agent.AgentType(strings.TrimSpace(candidate.AgentType)).Canonical()
	if recordedType != candidateType {
		return fmt.Errorf("pending assignment recovery target %s changed agent type from %s to %s", recorded.OccupancyKey, recordedType, candidateType)
	}
	recordedName := strings.TrimSpace(recorded.AgentName)
	if recordedName != "" && strings.TrimSpace(candidate.AgentMailName) != recordedName {
		return fmt.Errorf("pending assignment recovery target %s changed Agent Mail identity from %s to %s", recorded.OccupancyKey, recordedName, strings.TrimSpace(candidate.AgentMailName))
	}
	return nil
}

func coordinatorAssignmentIsRecoverable(recorded *assignmentstore.Assignment) bool {
	if recorded == nil || recorded.DispatchState != assignmentstore.DispatchPending ||
		strings.TrimSpace(recorded.IdempotencyKey) == "" || strings.TrimSpace(recorded.ClaimActor) == "" ||
		strings.TrimSpace(recorded.PendingPrompt) == "" {
		return false
	}
	switch recorded.Status {
	case assignmentstore.StatusClaiming, assignmentstore.StatusClaimed:
		return true
	default:
		return false
	}
}

func coordinatorWorkAssignmentFromRecord(recorded *assignmentstore.Assignment) *WorkAssignment {
	if recorded == nil {
		return nil
	}
	requested := append([]string(nil), recorded.ReservationInputPaths...)
	if len(requested) == 0 {
		requested = append([]string(nil), recorded.ReservationRequested...)
	}
	return &WorkAssignment{
		BeadID: recorded.BeadID, BeadTitle: recorded.BeadTitle,
		AgentPaneID: recorded.OccupancyKey, AgentPaneIndex: recorded.Pane,
		AgentMailName: recorded.AgentName, AgentType: recorded.AgentType,
		AssignedAt: recorded.AssignedAt, FilesToReserve: requested,
	}
}

func (c *SessionCoordinator) recoverPendingAssignment(ctx context.Context, store *assignmentstore.AssignmentStore, recorded *assignmentstore.Assignment, work *WorkAssignment) AssignmentResult {
	target := strings.TrimSpace(recorded.DispatchTarget)
	if target == "" {
		target = strings.TrimSpace(recorded.OccupancyKey)
	}
	occupancyKey := strings.TrimSpace(recorded.OccupancyKey)
	if occupancyKey == "" {
		occupancyKey = target
	}
	recoveredIntent := strings.TrimSpace(recorded.IntentSHA256)
	if recoveredIntent == "" {
		recoveredIntent = strings.TrimSpace(recorded.PromptSHA256)
	}
	request := assignmentstore.AtomicRequest{
		BeadID: recorded.BeadID, BeadTitle: recorded.BeadTitle,
		Target: target, OccupancyKey: occupancyKey, Pane: recorded.Pane,
		AgentType: recorded.AgentType, AgentName: recorded.AgentName,
		Actor: recorded.ClaimActor, Prompt: recorded.PendingPrompt,
		IdempotencyKey: recorded.IdempotencyKey, RecoveredIntentSHA256: recoveredIntent,
		RequireReservation:        recorded.ReservationRequired,
		AllowReservationDiscovery: recorded.ReservationDiscovery,
		RequestedPaths:            append([]string(nil), work.FilesToReserve...), ReservationTTL: time.Hour,
	}
	return c.executeAtomicAssignment(ctx, store, work, request)
}

func (c *SessionCoordinator) reconcileTerminalAssignments(ctx context.Context, store *assignmentstore.AssignmentStore) error {
	if store == nil {
		return errors.New("assignment store is not configured")
	}
	lookup := c.workItemStatusFn
	if lookup == nil {
		lookup = func(statusCtx context.Context, beadID string) (string, error) {
			return bv.GetBeadStatusContext(statusCtx, c.projectKey, beadID)
		}
	}

	var reconcileErrors []error
	for _, active := range store.ListActive() {
		if active == nil {
			continue
		}
		terminalStatus := active.PendingTerminalStatus
		terminalReason := active.PendingTerminalReason
		if terminalStatus != assignmentstore.StatusCompleted && terminalStatus != assignmentstore.StatusFailed {
			trackerStatus, err := lookup(ctx, active.BeadID)
			if err != nil {
				reconcileErrors = append(reconcileErrors, fmt.Errorf("read %s status: %w", active.BeadID, err))
				continue
			}
			switch strings.ToLower(strings.TrimSpace(trackerStatus)) {
			case "closed":
				if active.Status == assignmentstore.StatusAssigned || active.Status == assignmentstore.StatusWorking {
					terminalStatus = assignmentstore.StatusCompleted
				} else {
					terminalStatus = assignmentstore.StatusFailed
					terminalReason = "tracker closed before assignment delivery completed"
				}
			case "tombstone":
				terminalStatus = assignmentstore.StatusFailed
				terminalReason = "tracker work item was tombstoned"
			default:
				continue
			}
		}

		completed, err := c.reconcileTerminalAssignment(ctx, store, active, terminalStatus, terminalReason)
		if err != nil {
			reconcileErrors = append(reconcileErrors, err)
			continue
		}
		if !completed {
			continue
		}
		// The cached recommendation can still point at the work item we just
		// retired. Refresh only after a real terminal transition so the freed
		// pane can receive newly actionable work without disabling BV caching
		// for ordinary polling cycles.
		bv.InvalidateTriageCache()
	}
	return errors.Join(reconcileErrors...)
}

func (c *SessionCoordinator) reconcileTerminalAssignment(ctx context.Context, store *assignmentstore.AssignmentStore, observed *assignmentstore.Assignment, terminalStatus assignmentstore.AssignmentStatus, terminalReason string) (bool, error) {
	cleanupUnlock, err := store.AcquireExternalCleanupLock(ctx, observed.BeadID)
	if err != nil {
		return false, fmt.Errorf("lock terminal assignment %s external cleanup: %w", observed.BeadID, err)
	}
	defer cleanupUnlock()
	if err := store.LoadStrict(); err != nil {
		return false, fmt.Errorf("refresh terminal assignment %s external cleanup: %w", observed.BeadID, err)
	}
	current := store.Get(observed.BeadID)
	if current == nil || !assignmentstore.SameAssignmentGeneration(observed, current) {
		return false, nil
	}
	if current.ClearState == assignmentstore.ClearStateNone && (current.Status == assignmentstore.StatusCompleted || current.Status == assignmentstore.StatusFailed) {
		return false, nil
	}
	if current.PendingTerminalStatus == assignmentstore.StatusCompleted || current.PendingTerminalStatus == assignmentstore.StatusFailed {
		terminalStatus = current.PendingTerminalStatus
		terminalReason = current.PendingTerminalReason
	}

	barrier, applied, err := store.BeginTerminalReconciliationIfCurrent(ctx, current, terminalStatus, terminalReason)
	if err != nil {
		return false, fmt.Errorf("begin terminal reconciliation for %s: %w", observed.BeadID, err)
	}
	if !applied {
		return false, nil
	}
	if err := c.releaseTerminalAssignmentReservations(ctx, barrier); err != nil {
		if persistErr := store.RecordClearReleaseFailed(ctx, observed.BeadID, err); persistErr != nil {
			err = errors.Join(err, persistErr)
		}
		return false, fmt.Errorf("release terminal assignment %s reservations: %w", observed.BeadID, err)
	}
	leasesReleased, err := store.RecordClearLeasesReleased(ctx, observed.BeadID)
	if err != nil {
		return false, fmt.Errorf("record terminal assignment %s lease release: %w", observed.BeadID, err)
	}
	claimReleased := leasesReleased
	if !leasesReleased.TerminalClaimReleased {
		claimActor := strings.TrimSpace(leasesReleased.ClaimActor)
		if claimActor != "" {
			releaseClaim := c.releaseWorkItemClaimFn
			if releaseClaim == nil {
				releaseClaim = bv.ReleaseBeadClaim
			}
			if _, err := releaseClaim(ctx, c.projectKey, observed.BeadID, claimActor); err != nil {
				if persistErr := store.RecordClearReleaseFailed(ctx, observed.BeadID, err); persistErr != nil {
					err = errors.Join(err, persistErr)
				}
				return false, fmt.Errorf("release terminal assignment %s Beads claim: %w", observed.BeadID, err)
			}
		}
		claimReleased, err = store.RecordTerminalClaimReleased(ctx, observed.BeadID)
		if err != nil {
			return false, fmt.Errorf("record terminal assignment %s claim release: %w", observed.BeadID, err)
		}
	}
	if err := store.CompleteTerminalReconciliation(ctx, observed.BeadID, claimReleased.PendingTerminalStatus, claimReleased.PendingTerminalReason); err != nil {
		return false, fmt.Errorf("complete terminal reconciliation for %s: %w", observed.BeadID, err)
	}
	return true, nil
}

func (c *SessionCoordinator) releaseTerminalAssignmentReservations(ctx context.Context, current *assignmentstore.Assignment) error {
	if current == nil {
		return errors.New("assignment is required")
	}
	if !current.ReservationRequired && len(current.ReservationIDs) == 0 && len(current.ReservedPaths) == 0 {
		return nil
	}

	lease := coordinatorLeaseFromAssignment(current)
	switch current.ReservationState {
	case assignmentstore.ReservationReleased, assignmentstore.ReservationFailed:
		if !assignmentstore.ReservationOutcomeNeedsReconciliation(current) {
			return nil
		}
	case assignmentstore.ReservationPending:
		if current.ReservationAttempts == 0 {
			return nil
		}
	}

	if !assignmentstore.ReservationOutcomeNeedsReconciliation(current) {
		if len(lease.ReservationIDs) > 0 || len(lease.Granted) > 0 {
			return c.releaseCoordinatorLease(ctx, current, lease)
		}
		if current.ReservationState == assignmentstore.ReservationReserved {
			return fmt.Errorf("reservation for %s is marked reserved without durable release handles", current.BeadID)
		}
		return nil
	}

	requested, err := validateCoordinatorReservationPaths(lease.Requested)
	if err != nil {
		return fmt.Errorf("reconcile reservation for %s: %w", current.BeadID, err)
	}
	lease.Requested = requested
	port := &coordinatorAgentMailReservationPort{client: c.reservationClient, projectKey: agentmail.CanonicalProjectKey(c.projectKey)}
	reconciled, err := port.ReconcileReservation(ctx, assignmentstore.ReservationRequest{
		BeadID: current.BeadID, BeadTitle: current.BeadTitle, AgentName: lease.AgentName,
		Target: lease.Target, RequestedPaths: requested, TTL: time.Hour,
	}, lease)
	if err != nil {
		return fmt.Errorf("reconcile reservation for %s: %w", current.BeadID, err)
	}
	switch reconciled.State {
	case assignmentstore.ReservationReconciliationAbsent:
		return nil
	case assignmentstore.ReservationReconciliationReserved:
		if len(reconciled.Lease.ReservationIDs) == 0 && len(reconciled.Lease.Granted) == 0 {
			return fmt.Errorf("reconcile reservation for %s returned reserved without handles", current.BeadID)
		}
		return c.releaseCoordinatorLease(ctx, current, reconciled.Lease)
	case assignmentstore.ReservationReconciliationUnknown, "":
		return fmt.Errorf("reservation outcome remains unknown for %s", current.BeadID)
	default:
		return fmt.Errorf("invalid reservation reconciliation state %q for %s", reconciled.State, current.BeadID)
	}
}

func coordinatorLeaseFromAssignment(current *assignmentstore.Assignment) assignmentstore.LeaseReceipt {
	agentName := strings.TrimSpace(current.ReservationAgent)
	if agentName == "" {
		agentName = strings.TrimSpace(current.AgentName)
	}
	target := strings.TrimSpace(current.ReservationTarget)
	if target == "" {
		target = strings.TrimSpace(current.OccupancyKey)
	}
	requested := append([]string(nil), current.ReservationRequested...)
	if len(requested) == 0 {
		requested = append([]string(nil), current.ReservationInputPaths...)
	}
	return assignmentstore.LeaseReceipt{
		AgentName: agentName, Target: target, Requested: requested,
		Granted:        append([]string(nil), current.ReservedPaths...),
		ReservationIDs: append([]int(nil), current.ReservationIDs...),
		ExpiresAt:      current.ReservationExpiresAt,
	}
}

func (c *SessionCoordinator) releaseCoordinatorLease(ctx context.Context, current *assignmentstore.Assignment, lease assignmentstore.LeaseReceipt) error {
	if c.reservationClient == nil {
		return errors.New("agent mail reservation client is not configured")
	}
	manager := assignpkg.NewFileReservationManager(c.reservationClient, c.projectKey)
	_, err := manager.ReleaseExactForBead(ctx, current, lease.ReservationIDs, lease.Granted)
	return err
}

func filterOccupiedAgents(agents []*AgentState, activeAssignments []*assignmentstore.Assignment) ([]*AgentState, error) {
	occupiedTargets := make(map[string]struct{})
	for _, active := range activeAssignments {
		if active == nil {
			continue
		}
		target, err := assignmentstore.CanonicalPaneIdentity(active)
		if err != nil {
			return nil, fmt.Errorf("active assignment %s has noncanonical pane identity: %w", active.BeadID, err)
		}
		occupiedTargets[target] = struct{}{}
	}
	filtered := make([]*AgentState, 0, len(agents))
	for _, agent := range agents {
		if agent == nil {
			continue
		}
		if _, occupied := occupiedTargets[strings.TrimSpace(agent.PaneID)]; occupied {
			continue
		}
		filtered = append(filtered, agent)
	}
	return filtered, nil
}

// attemptAssignment attempts to assign work to an agent.
func (c *SessionCoordinator) attemptAssignment(ctx context.Context, assignment *WorkAssignment, rec *bv.TriageRecommendation) AssignmentResult {
	if c.mailClient == nil {
		return AssignmentResult{Assignment: safeCoordinatorWorkProjection(assignment), Error: "assignment delivery unavailable: agent mail client is not configured"}
	}
	if assignment.AgentMailName == "" {
		return AssignmentResult{Assignment: safeCoordinatorWorkProjection(assignment), Error: "assignment delivery unavailable: agent has no agent-mail identity"}
	}

	idempotencyKey, err := assignmentstore.NewAssignmentIdempotencyKey()
	if err != nil {
		return AssignmentResult{Assignment: safeCoordinatorWorkProjection(assignment), Error: err.Error()}
	}
	claimActor := assignmentstore.StableClaimActor(assignment.AgentMailName, idempotencyKey)
	body := c.formatAssignmentMessage(assignment, rec, claimActor)

	store, err := assignmentstore.LoadStoreStrict(c.session)
	if err != nil {
		return AssignmentResult{Assignment: safeCoordinatorWorkProjection(assignment), ClaimActor: claimActor, IdempotencyKey: idempotencyKey, Error: fmt.Sprintf("loading assignment ledger: %v", err)}
	}
	request := assignmentstore.AtomicRequest{
		BeadID:             assignment.BeadID,
		BeadTitle:          assignment.BeadTitle,
		Target:             assignment.AgentPaneID,
		OccupancyKey:       assignment.AgentPaneID,
		Pane:               assignment.AgentPaneIndex,
		AgentType:          assignment.AgentType,
		AgentName:          assignment.AgentMailName,
		Actor:              assignment.AgentMailName,
		Prompt:             body,
		IdempotencyKey:     idempotencyKey,
		RequireReservation: len(assignment.FilesToReserve) > 0,
		RequestedPaths:     append([]string(nil), assignment.FilesToReserve...),
		ReservationTTL:     time.Hour,
	}
	return c.executeAtomicAssignment(ctx, store, assignment, request)
}

func (c *SessionCoordinator) executeAtomicAssignment(ctx context.Context, store *assignmentstore.AssignmentStore, work *WorkAssignment, request assignmentstore.AtomicRequest) AssignmentResult {
	result := AssignmentResult{
		Assignment: safeCoordinatorWorkProjection(work), ClaimActor: assignmentstore.StableClaimActor(request.Actor, request.IdempotencyKey),
		IdempotencyKey: request.IdempotencyKey,
	}
	if c.mailClient == nil {
		result.Error = "assignment delivery unavailable: agent mail client is not configured"
		return result
	}
	if strings.TrimSpace(request.AgentName) == "" {
		result.Error = "assignment delivery unavailable: agent has no agent-mail identity"
		return result
	}
	atomicCoordinator := c.newAtomicAssignmentCoordinator(store)
	if c.atomicCoordinatorFactory != nil {
		atomicCoordinator = c.atomicCoordinatorFactory(store)
	}
	atomicResult, err := atomicCoordinator.Execute(ctx, request)
	if coordinatorAtomicRecordMatchesRequest(atomicResult.Assignment, request) {
		result.Assignment = coordinatorWorkAssignmentFromRecord(atomicResult.Assignment)
	}
	result.Reservations = append([]string(nil), atomicResult.Lease.Granted...)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.MessageSent = atomicResult.Sent
	result.Success = atomicResult.Sent
	return result
}

func coordinatorAtomicRecordMatchesRequest(recorded *assignmentstore.Assignment, request assignmentstore.AtomicRequest) bool {
	return recorded != nil && recorded.BeadID == request.BeadID &&
		strings.TrimSpace(recorded.IdempotencyKey) != "" && recorded.IdempotencyKey == request.IdempotencyKey
}

func safeCoordinatorWorkProjection(work *WorkAssignment) *WorkAssignment {
	if work == nil {
		return nil
	}
	copy := *work
	copy.BeadTitle = ""
	copy.FilesToReserve = nil
	return &copy
}

type coordinatorReservationClient interface {
	EnsureProject(context.Context, string) (*agentmail.Project, error)
	ReservePaths(context.Context, agentmail.FileReservationOptions) (*agentmail.ReservationResult, error)
	ListReservations(context.Context, string, string, bool) ([]agentmail.FileReservation, error)
	ReleaseReservations(context.Context, string, string, []string, []int) (*agentmail.ReleaseReservationsResult, error)
	RenewReservations(context.Context, agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error)
}

type coordinatorAgentMailReservationPort struct {
	client     coordinatorReservationClient
	projectKey string
}

func (p *coordinatorAgentMailReservationPort) Reserve(ctx context.Context, req assignmentstore.ReservationRequest) (assignmentstore.LeaseReceipt, error) {
	lease := assignmentstore.LeaseReceipt{
		AgentName: req.AgentName,
		Target:    req.Target,
		Requested: append([]string(nil), req.RequestedPaths...),
	}
	if len(req.RequestedPaths) == 0 {
		return lease, nil
	}
	if p == nil || p.client == nil {
		return lease, assignmentstore.GuaranteeNoReservation(errors.New("agent mail reservation client is not configured"))
	}
	requested, err := validateCoordinatorReservationPaths(req.RequestedPaths)
	if err != nil {
		return lease, assignmentstore.GuaranteeNoReservation(err)
	}
	lease.Requested = append([]string(nil), requested...)
	projectID, err := p.ensureProject(ctx)
	if err != nil {
		return lease, assignmentstore.GuaranteeNoReservation(err)
	}

	ttlSeconds := int(req.TTL.Seconds())
	if ttlSeconds < 60 {
		ttlSeconds = 3600
	}
	reserved, reserveErr := p.client.ReservePaths(ctx, agentmail.FileReservationOptions{
		ProjectKey: p.projectKey,
		AgentName:  req.AgentName,
		Paths:      requested,
		TTLSeconds: ttlSeconds,
		Exclusive:  true,
		Reason:     fmt.Sprintf("bead assignment: %s", req.BeadID),
	})
	if reserved == nil {
		if reserveErr != nil {
			return lease, reserveErr
		}
		return lease, assignmentstore.GuaranteeNoReservation(errors.New("agent mail returned no reservation result"))
	}

	requestedSet := make(map[string]struct{}, len(requested))
	for _, path := range requested {
		requestedSet[path] = struct{}{}
	}
	seenPaths := make(map[string]struct{}, len(reserved.Granted))
	seenIDs := make(map[int]struct{}, len(reserved.Granted))
	expectedReason := fmt.Sprintf("bead assignment: %s", req.BeadID)
	now := time.Now().UTC()
	for _, granted := range reserved.Granted {
		if granted.ID > 0 {
			if _, duplicate := seenIDs[granted.ID]; duplicate {
				sort.Ints(lease.ReservationIDs)
				return lease, fmt.Errorf("agent mail repeated reservation ID %d", granted.ID)
			}
			seenIDs[granted.ID] = struct{}{}
			lease.ReservationIDs = append(lease.ReservationIDs, granted.ID)
		}
		expiresAt := granted.ExpiresTS.Time
		if granted.ID <= 0 || granted.ProjectID != projectID || granted.AgentName != req.AgentName || granted.Reason != expectedReason || !granted.Exclusive ||
			granted.ReleasedTS != nil || expiresAt.IsZero() || !expiresAt.After(now) {
			sort.Ints(lease.ReservationIDs)
			return lease, fmt.Errorf("agent mail returned invalid reservation receipt for %q", granted.PathPattern)
		}
		if _, expected := requestedSet[granted.PathPattern]; !expected {
			sort.Ints(lease.ReservationIDs)
			return lease, fmt.Errorf("agent mail granted unexpected path %q", granted.PathPattern)
		}
		if _, duplicate := seenPaths[granted.PathPattern]; duplicate {
			sort.Ints(lease.ReservationIDs)
			return lease, fmt.Errorf("agent mail granted path %q more than once", granted.PathPattern)
		}
		seenPaths[granted.PathPattern] = struct{}{}
		lease.Granted = append(lease.Granted, granted.PathPattern)
		if lease.ExpiresAt == nil || expiresAt.Before(*lease.ExpiresAt) {
			lease.ExpiresAt = &expiresAt
		}
	}
	if reserveErr != nil {
		return lease, reserveErr
	}
	if len(reserved.Conflicts) > 0 {
		conflictErr := fmt.Errorf("file reservation conflicts for %s", req.BeadID)
		if len(lease.ReservationIDs) == 0 {
			return lease, assignmentstore.GuaranteeNoReservation(conflictErr)
		}
		return lease, conflictErr
	}
	if len(seenPaths) != len(requestedSet) {
		grantErr := fmt.Errorf("agent mail granted %d of %d requested paths", len(seenPaths), len(requestedSet))
		if len(lease.ReservationIDs) == 0 {
			return lease, assignmentstore.GuaranteeNoReservation(grantErr)
		}
		return lease, grantErr
	}
	sort.Strings(lease.Granted)
	sort.Ints(lease.ReservationIDs)
	return lease, nil
}

func (p *coordinatorAgentMailReservationPort) ReconcileReservation(ctx context.Context, req assignmentstore.ReservationRequest, _ assignmentstore.LeaseReceipt) (assignmentstore.ReservationReconciliation, error) {
	if p == nil || p.client == nil {
		return assignmentstore.ReservationReconciliation{State: assignmentstore.ReservationReconciliationUnknown}, errors.New("agent mail reservation client is not configured")
	}
	requested, err := validateCoordinatorReservationPaths(req.RequestedPaths)
	if err != nil {
		return assignmentstore.ReservationReconciliation{State: assignmentstore.ReservationReconciliationUnknown}, err
	}
	projectID, err := p.ensureProject(ctx)
	if err != nil {
		return assignmentstore.ReservationReconciliation{State: assignmentstore.ReservationReconciliationUnknown}, err
	}
	reservations, err := p.client.ListReservations(ctx, p.projectKey, req.AgentName, false)
	if err != nil {
		return assignmentstore.ReservationReconciliation{State: assignmentstore.ReservationReconciliationUnknown}, err
	}

	requestedSet := make(map[string]struct{}, len(requested))
	for _, path := range requested {
		requestedSet[path] = struct{}{}
	}
	lease := assignmentstore.LeaseReceipt{
		AgentName: req.AgentName,
		Target:    req.Target,
		Requested: append([]string(nil), requested...),
	}
	seen := make(map[string]struct{}, len(requested))
	seenIDs := make(map[int]struct{}, len(requested))
	reason := fmt.Sprintf("bead assignment: %s", req.BeadID)
	now := time.Now().UTC()
	for _, reservation := range reservations {
		if reservation.ReleasedTS != nil || reservation.AgentName != req.AgentName || reservation.Reason != reason {
			continue
		}
		if _, wanted := requestedSet[reservation.PathPattern]; !wanted {
			continue
		}
		if reservation.ID > 0 {
			if _, duplicate := seenIDs[reservation.ID]; duplicate {
				sort.Ints(lease.ReservationIDs)
				return assignmentstore.ReservationReconciliation{State: assignmentstore.ReservationReconciliationUnknown, Lease: lease}, nil
			}
			seenIDs[reservation.ID] = struct{}{}
			lease.ReservationIDs = append(lease.ReservationIDs, reservation.ID)
		}
		expiresAt := reservation.ExpiresTS.Time
		if reservation.ID <= 0 || reservation.ProjectID != projectID || !reservation.Exclusive ||
			expiresAt.IsZero() || !expiresAt.After(now) {
			sort.Ints(lease.ReservationIDs)
			return assignmentstore.ReservationReconciliation{State: assignmentstore.ReservationReconciliationUnknown, Lease: lease}, nil
		}
		if _, duplicate := seen[reservation.PathPattern]; !duplicate {
			seen[reservation.PathPattern] = struct{}{}
			lease.Granted = append(lease.Granted, reservation.PathPattern)
		}
		if lease.ExpiresAt == nil || expiresAt.Before(*lease.ExpiresAt) {
			lease.ExpiresAt = &expiresAt
		}
	}
	if len(seen) == 0 {
		return assignmentstore.ReservationReconciliation{State: assignmentstore.ReservationReconciliationAbsent}, nil
	}
	sort.Strings(lease.Granted)
	sort.Ints(lease.ReservationIDs)
	// Any exact durable handle proves a lease exists. The atomic coordinator
	// validates completeness and persists partial grants as release-required.
	return assignmentstore.ReservationReconciliation{State: assignmentstore.ReservationReconciliationReserved, Lease: lease}, nil
}

func (p *coordinatorAgentMailReservationPort) ensureProject(ctx context.Context) (int, error) {
	if p == nil || p.client == nil {
		return 0, errors.New("agent mail reservation client is not configured")
	}
	project, err := p.client.EnsureProject(ctx, p.projectKey)
	if err != nil {
		return 0, fmt.Errorf("ensure Agent Mail project %q: %w", p.projectKey, err)
	}
	return validatedCoordinatorAgentMailProjectID(project, p.projectKey)
}

func validatedCoordinatorAgentMailProjectID(project *agentmail.Project, projectKey string) (int, error) {
	if project == nil || project.ID <= 0 {
		return 0, fmt.Errorf("ensure Agent Mail project %q returned no durable project ID", projectKey)
	}
	expectedKey := strings.TrimSpace(projectKey)
	if expectedKey == "" {
		return 0, errors.New("agent-mail project binding requires a non-empty project key")
	}
	// Symlink-equivalent compare: Agent Mail canonicalizes project paths, so
	// the returned human key may be the resolved form of a symlinked key
	// (GH#239). Fail-closed semantics are preserved for genuinely different
	// directories.
	if humanKey := strings.TrimSpace(project.HumanKey); !agentmail.ProjectKeysEquivalent(humanKey, expectedKey) {
		return 0, fmt.Errorf("agent-mail project binding mismatch: got %q, want %q", humanKey, projectKey)
	}
	return project.ID, nil
}

func validateCoordinatorReservationPaths(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, assignmentstore.ErrReservationPathsRequired
	}
	result := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, raw := range paths {
		path := strings.TrimSpace(raw)
		if path == "" {
			return nil, assignmentstore.ErrReservationPathsRequired
		}
		if _, duplicate := seen[path]; duplicate {
			return nil, fmt.Errorf("duplicate reservation path %q", path)
		}
		seen[path] = struct{}{}
		result = append(result, path)
	}
	return result, nil
}

func (c *SessionCoordinator) newAtomicAssignmentCoordinator(store *assignmentstore.AssignmentStore) *assignmentstore.AtomicCoordinator {
	operatorGatedLabels := append([]string(nil), c.operatorGatedLabels...)
	claimBeadForAssignment := bv.ClaimBeadForAssignmentWithOperatorGatedLabels
	if c.claimBeadForAssignmentFn != nil {
		claimBeadForAssignment = c.claimBeadForAssignmentFn
	}
	claimPort := assignmentstore.ClaimFunc(func(ctx context.Context, beadID, actor string) (assignmentstore.ClaimReceipt, error) {
		claim, err := claimBeadForAssignment(ctx, c.projectKey, beadID, actor, operatorGatedLabels)
		if err != nil {
			switch {
			case errors.Is(err, bv.ErrBeadAssignmentIneligible):
				return assignmentstore.ClaimReceipt{}, fmt.Errorf("%w: %v", assignmentstore.ErrClaimIneligible, err)
			case errors.Is(err, bv.ErrBeadAlreadyClaimed):
				return assignmentstore.ClaimReceipt{}, fmt.Errorf("%w: %v", assignmentstore.ErrClaimConflict, err)
			}
			return assignmentstore.ClaimReceipt{}, err
		}
		return assignmentstore.ClaimReceipt{
			BeadID: claim.ID, Actor: claim.Actor, Status: claim.Status, ClaimedAt: claim.ClaimedAt,
		}, nil
	})
	reservationPort := &coordinatorAgentMailReservationPort{client: c.reservationClient, projectKey: agentmail.CanonicalProjectKey(c.projectKey)}
	dispatchPort := assignmentstore.DispatchFunc(func(ctx context.Context, req assignmentstore.DispatchRequest) (assignmentstore.DispatchReceipt, error) {
		started := time.Now()
		project, err := c.mailClient.EnsureProject(ctx, c.projectKey)
		if err != nil {
			return assignmentstore.DispatchReceipt{Duration: time.Since(started)}, assignmentstore.GuaranteeNoActuation(
				fmt.Errorf("ensure Agent Mail dispatch project: %w", err),
			)
		}
		projectID, projectErr := validatedCoordinatorAgentMailProjectID(project, c.projectKey)
		if projectErr != nil {
			return assignmentstore.DispatchReceipt{Duration: time.Since(started)}, assignmentstore.GuaranteeNoActuation(
				fmt.Errorf("validate Agent Mail dispatch project: %w", projectErr),
			)
		}
		subject := fmt.Sprintf("Work Assignment: %s", req.BeadID)
		sent, err := c.mailClient.SendMessage(ctx, agentmail.SendMessageOptions{
			ProjectKey:  c.projectKey,
			SenderName:  c.agentName,
			To:          []string{req.AgentName},
			Subject:     subject,
			BodyMD:      req.Prompt,
			Importance:  "normal",
			AckRequired: true,
		})
		receipt := assignmentstore.DispatchReceipt{Duration: time.Since(started)}
		if err != nil {
			// An explicit MCP tool-level rejection is a completed negative
			// response: the server received the request and refused to send.
			// Mark it as guaranteed no-actuation so the atomic ledger returns
			// the assignment from sending back to pending (retryable) instead
			// of stranding the bead and pane behind a permanent
			// dispatch_state="sending" barrier. Timeouts and transport
			// failures stay outcome-unknown to preserve at-most-once delivery.
			return receipt, coordinatorMailDispatchError(err)
		}
		deliveryID, receiptErr := validatedAgentMailDeliveryID(
			sent, c.projectKey, projectID, c.agentName, req.AgentName, subject, req.Prompt,
		)
		if receiptErr != nil {
			return receipt, receiptErr
		}
		receipt.DeliveryID = deliveryID
		return receipt, nil
	})
	preflight := assignmentstore.PromptPreflightFunc(func(_ context.Context, req assignmentstore.DispatchRequest) (assignmentstore.PromptPreflightResult, error) {
		loaded, err := config.LoadMerged(c.projectKey, config.DefaultPath())
		if err != nil {
			return assignmentstore.PromptPreflightResult{}, fmt.Errorf("load redaction policy: %w", err)
		}
		redactionConfig := loaded.Redaction.ToRedactionLibConfig()
		titleResult := redaction.ScanAndRedact(req.BeadTitle, redactionConfig)
		if titleResult.Blocked {
			return assignmentstore.PromptPreflightResult{}, fmt.Errorf("redaction policy blocked assignment title (%d findings)", len(titleResult.Findings))
		}
		for _, requestedPath := range req.RequestedPaths {
			pathResult := redaction.ScanAndRedact(requestedPath, redactionConfig)
			if len(pathResult.Findings) > 0 {
				return assignmentstore.PromptPreflightResult{}, fmt.Errorf("redaction policy blocked assignment reservation path (%d findings)", len(pathResult.Findings))
			}
		}
		dispatchResult := redaction.ScanAndRedact(req.Prompt, redactionConfig)
		if dispatchResult.Blocked {
			return assignmentstore.PromptPreflightResult{}, fmt.Errorf("redaction policy blocked assignment prompt (%d findings)", len(dispatchResult.Findings))
		}
		durableConfig := redactionConfig.DeepCopy()
		durableConfig.Mode = redaction.ModeRedact
		durablePrompt := redaction.ScanAndRedact(req.Prompt, durableConfig).Output
		durableTitle := redaction.ScanAndRedact(req.BeadTitle, durableConfig).Output
		return assignmentstore.PromptPreflightResult{
			// Agent Mail is an external durable transport. Never send a raw
			// credential merely because the local display policy is configured
			// as off; use the same forced-redacted value persisted in the ledger.
			DispatchPrompt: durablePrompt,
			DurablePrompt:  durablePrompt,
			DurableTitle:   durableTitle,
		}, nil
	})
	readLiveDetails := func(detailsCtx context.Context, beadID string) (*bv.BeadAssignmentDetails, error) {
		var (
			details *bv.BeadAssignmentDetails
			err     error
		)
		if c.workItemDetailsFn != nil {
			details, err = c.workItemDetailsFn(detailsCtx, beadID)
		} else {
			details, err = bv.GetBeadAssignmentDetailsContext(detailsCtx, c.projectKey, beadID)
		}
		if err != nil {
			return nil, err
		}
		if details == nil {
			return nil, errors.New("live work-item details are missing")
		}
		return details, nil
	}
	authorizeLiveDetails := func(details *bv.BeadAssignmentDetails, request assignmentstore.AssignmentEligibilityAuthorizationRequest) error {
		err := bv.ValidateBeadAssignmentAuthorizationWithOperatorGatedLabels(details, bv.BeadAssignmentAuthorization{
			BeadID: request.BeadID, ExpectedAssignee: request.ClaimActor,
			AllowUnassignedOpen:  request.AllowUnassignedOpen,
			AllowOwnedOpen:       request.AllowOwnedOpen,
			AllowOwnedInProgress: request.AllowOwnedInProgress,
		}, operatorGatedLabels)
		if errors.Is(err, bv.ErrBeadAssignmentIneligible) {
			return fmt.Errorf("%w: %v", assignmentstore.ErrClaimIneligible, err)
		}
		return err
	}
	replacementAuthorization := assignmentstore.WorkingReplacementAuthorizationFunc(func(ctx context.Context, beadID string) (assignmentstore.WorkingReplacementAuthorization, error) {
		details, err := readLiveDetails(ctx, beadID)
		if err != nil {
			return assignmentstore.WorkingReplacementAuthorization{}, err
		}
		if err := authorizeLiveDetails(details, assignmentstore.AssignmentEligibilityAuthorizationRequest{
			BeadID: beadID, ClaimActor: details.Assignee, AllowOwnedInProgress: true,
		}); err != nil {
			return assignmentstore.WorkingReplacementAuthorization{}, err
		}
		return assignmentstore.WorkingReplacementAuthorization{
			Status: details.Status, Assignee: details.Assignee,
		}, nil
	})
	return assignmentstore.NewAtomicCoordinator(store, claimPort, reservationPort, dispatchPort, preflight).
		WithWorkItemStatusPort(assignmentstore.WorkItemStatusFunc(func(statusCtx context.Context, beadID string) (string, error) {
			return bv.GetBeadStatusContext(statusCtx, c.projectKey, beadID)
		})).
		WithAssignmentEligibilityAuthorizationPort(assignmentstore.AssignmentEligibilityAuthorizationFunc(
			func(detailsCtx context.Context, request assignmentstore.AssignmentEligibilityAuthorizationRequest) error {
				details, err := readLiveDetails(detailsCtx, request.BeadID)
				if err != nil {
					return err
				}
				return authorizeLiveDetails(details, request)
			},
		)).
		WithWorkingReplacementAuthorizationPort(replacementAuthorization)
}

// coordinatorMailDispatchError classifies an Agent Mail send failure for the
// atomic dispatch ledger. Only a typed tool-level rejection proves the server
// did not deliver; everything else (timeout, transport, decode) remains
// outcome-unknown.
func coordinatorMailDispatchError(err error) error {
	if err != nil && agentmail.IsToolRejection(err) {
		return assignmentstore.GuaranteeNoActuation(err)
	}
	return err
}

func validatedAgentMailDeliveryID(sent *agentmail.SendResult, projectKey string, projectID int, senderName, agentName, subject, body string) (string, error) {
	if sent == nil {
		return "", errors.New("agent mail returned no delivery result")
	}
	if sent.Count != 1 || len(sent.Deliveries) != 1 {
		return "", fmt.Errorf("agent mail returned count=%d deliveries=%d, want exactly one", sent.Count, len(sent.Deliveries))
	}
	payload := sent.Deliveries[0].Payload
	if payload == nil || payload.ID <= 0 {
		return "", errors.New("agent mail returned no concrete delivery receipt")
	}
	if sent.Deliveries[0].Project != projectKey {
		return "", fmt.Errorf("agent mail delivery project %q does not match %q", sent.Deliveries[0].Project, projectKey)
	}
	if payload.ProjectID != projectID {
		return "", fmt.Errorf("agent mail delivery project ID %d does not match %d", payload.ProjectID, projectID)
	}
	if payload.From != senderName {
		return "", fmt.Errorf("agent mail delivery sender %q does not match %q", payload.From, senderName)
	}
	if len(payload.To) != 1 || payload.To[0] != agentName {
		return "", fmt.Errorf("agent mail delivery recipients %v do not match [%s]", payload.To, agentName)
	}
	if payload.Subject != subject {
		return "", errors.New("agent mail delivery subject does not match the prepared assignment")
	}
	if payload.BodyMD != body {
		return "", errors.New("agent mail delivery body does not match the prepared assignment")
	}
	if payload.Importance != "normal" || !payload.AckRequired {
		return "", errors.New("agent mail delivery policy does not match the prepared assignment")
	}
	return fmt.Sprintf("%d", payload.ID), nil
}

// formatAssignmentMessage formats a work assignment message.
func (c *SessionCoordinator) formatAssignmentMessage(assignment *WorkAssignment, rec *bv.TriageRecommendation, claimActor ...string) string {
	var sb strings.Builder

	sb.WriteString("# Work Assignment\n\n")
	sb.WriteString(fmt.Sprintf("**Bead:** %s\n", assignment.BeadID))
	sb.WriteString(fmt.Sprintf("**Title:** %s\n", assignment.BeadTitle))
	sb.WriteString(fmt.Sprintf("**Priority:** P%d\n", assignment.Priority))
	sb.WriteString(fmt.Sprintf("**Score:** %.2f\n\n", assignment.Score))

	if len(rec.Reasons) > 0 {
		sb.WriteString("## Why This Task\n\n")
		for _, reason := range rec.Reasons {
			sb.WriteString(fmt.Sprintf("- %s\n", reason))
		}
		sb.WriteString("\n")
	}

	if len(rec.UnblocksIDs) > 0 {
		sb.WriteString("## Impact\n\n")
		sb.WriteString(fmt.Sprintf("Completing this will unblock %d other tasks:\n", len(rec.UnblocksIDs)))
		for _, id := range rec.UnblocksIDs {
			if sb.Len() > 1500 {
				sb.WriteString("- ...\n")
				break
			}
			sb.WriteString(fmt.Sprintf("- %s\n", id))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## Instructions\n\n")
	sb.WriteString("1. Review the bead with `br show " + assignment.BeadID + "`\n")
	if len(claimActor) > 0 && strings.TrimSpace(claimActor[0]) != "" {
		sb.WriteString("2. NTM already claimed this bead atomically as `" + claimActor[0] + "`; do not claim it again\n")
	} else {
		sb.WriteString("2. Verify the bead is assigned to you before editing\n")
	}
	sb.WriteString("3. Verify the listed file reservations before editing\n")
	sb.WriteString("4. Implement and test\n")
	sb.WriteString("5. Close with `br close " + assignment.BeadID + "`\n")
	sb.WriteString("6. Commit with `.beads/` changes\n\n")

	sb.WriteString("Please acknowledge this message when you begin work.\n")

	return sb.String()
}

// ScoreAndSelectAssignments computes optimal agent-task pairings using multi-factor scoring.
// It scores every agent-recommendation combination, then greedily selects the
// highest-scoring non-conflicting set (each agent and each task appear at most
// once). AssignWork calls it every auto-assignment cycle (bd-v394r).
func ScoreAndSelectAssignments(
	idleAgents []*AgentState,
	recommendations []bv.TriageRecommendation,
	config ScoreConfig,
	existingReservations map[string][]string, // agent -> reserved file patterns
) []ScoredAssignment {
	if len(idleAgents) == 0 || len(recommendations) == 0 {
		return nil
	}

	var candidates []ScoredAssignment

	// Score all possible agent-task combinations
	for _, agent := range idleAgents {
		for i := range recommendations {
			rec := &recommendations[i]

			// Skip dependency- or operator-gated items even when a stale triage
			// payload ranked them highly.
			if !recommendationPassesSemanticGates(*rec) {
				continue
			}

			scored := scoreAssignment(agent, rec, config, existingReservations)
			if scored.TotalScore > 0 {
				candidates = append(candidates, scored)
			}
		}
	}

	// Sort by total score (highest first)
	sortScoredAssignments(candidates)

	// Select non-conflicting assignments (each agent gets at most one task)
	var selected []ScoredAssignment
	assignedAgents := make(map[string]bool)
	assignedTasks := make(map[string]bool)

	for _, candidate := range candidates {
		agentID := candidate.Agent.PaneID
		taskID := candidate.Recommendation.ID

		if assignedAgents[agentID] || assignedTasks[taskID] {
			continue
		}

		selected = append(selected, candidate)
		assignedAgents[agentID] = true
		assignedTasks[taskID] = true
	}

	return selected
}

// sortScoredAssignments sorts assignments by total score (highest first).
func sortScoredAssignments(candidates []ScoredAssignment) {
	for i := 0; i < len(candidates)-1; i++ {
		for j := i + 1; j < len(candidates); j++ {
			if candidates[j].TotalScore > candidates[i].TotalScore {
				candidates[i], candidates[j] = candidates[j], candidates[i]
			}
		}
	}
}

// scoreAssignment computes the score for a single agent-task pairing.
func scoreAssignment(
	agent *AgentState,
	rec *bv.TriageRecommendation,
	config ScoreConfig,
	existingReservations map[string][]string,
) ScoredAssignment {
	breakdown := AssignmentScoreBreakdown{
		BaseScore: rec.Score,
	}

	// Agent type matching
	if config.UseAgentProfiles {
		breakdown.AgentTypeBonus = computeAgentTypeBonus(agent.AgentType, rec)
	}

	// Profile-based routing bonuses
	if config.UseAgentProfiles && agent.Profile != nil {
		// Extract task tags from title and any available description
		taskTags := ExtractTaskTags(rec.Title, "")

		// Compute profile tag bonus based on tag overlap
		tagWeight := config.ProfileTagBoostWeight
		if tagWeight == 0 {
			tagWeight = 0.15 // Default 15% weight
		}
		breakdown.ProfileTagBonus = computeProfileTagBonus(agent.Profile, taskTags, tagWeight)

		// Extract mentioned files from task title
		mentionedFiles := ExtractMentionedFiles(rec.Title, "")

		// Compute focus pattern bonus based on file pattern matching
		patternWeight := config.FocusPatternBoostWeight
		if patternWeight == 0 {
			patternWeight = 0.10 // Default 10% weight
		}
		breakdown.FocusPatternBonus = computeFocusPatternBonus(agent.Profile, mentionedFiles, patternWeight)
	}

	// Critical path bonus
	if config.PreferCriticalPath && rec.Breakdown != nil {
		breakdown.CriticalPathBonus = computeCriticalPathBonus(rec.Breakdown)
	}

	// File overlap penalty
	// Note: computeFileOverlapPenalty falls back to agent.Reservations if map is nil
	if config.PenalizeFileOverlap {
		breakdown.FileOverlapPenalty = computeFileOverlapPenalty(agent, existingReservations)
	}

	// Context/budget penalty
	// Note: ContextUsage is in percentage scale (0-100), not ratio (0-1)
	if config.BudgetAware {
		threshold := config.ContextThreshold
		if threshold == 0 {
			threshold = 80 // 80% threshold (percentage scale)
		}
		breakdown.ContextPenalty = computeContextPenalty(agent.ContextUsage, threshold)
	}

	totalScore := breakdown.BaseScore +
		breakdown.AgentTypeBonus +
		breakdown.CriticalPathBonus +
		breakdown.ProfileTagBonus +
		breakdown.FocusPatternBonus -
		breakdown.FileOverlapPenalty -
		breakdown.ContextPenalty

	return ScoredAssignment{
		Assignment: &WorkAssignment{
			BeadID:         rec.ID,
			BeadTitle:      rec.Title,
			AgentPaneID:    agent.PaneID,
			AgentPaneIndex: agent.PaneIndex,
			AgentMailName:  agent.AgentMailName,
			AgentType:      agent.AgentType,
			AssignedAt:     time.Now(),
			Priority:       rec.Priority,
			Score:          totalScore,
			FilesToReserve: ExtractMentionedFiles(rec.Title, strings.Join(rec.Reasons, " ")),
		},
		Recommendation: rec,
		Agent:          agent,
		TotalScore:     totalScore,
		ScoreBreakdown: breakdown,
	}
}

// computeAgentTypeBonus returns a bonus based on agent-task compatibility.
// Claude (cc) is better for complex tasks (epics, features), Codex (cod) for quick fixes.
func computeAgentTypeBonus(agentType string, rec *bv.TriageRecommendation) float64 {
	taskComplexity := estimateTaskComplexity(rec)

	switch agent.AgentType(agentType).Canonical() {
	case agent.AgentTypeClaudeCode:
		// Claude excels at complex, multi-step work
		if taskComplexity >= 0.7 {
			return 0.15 // 15% bonus for complex tasks
		} else if taskComplexity <= 0.3 {
			return -0.05 // Small penalty for simple tasks (overkill)
		}
	case agent.AgentTypeCodex:
		// Codex is great for quick, focused fixes
		if taskComplexity <= 0.3 {
			return 0.15 // 15% bonus for simple tasks
		} else if taskComplexity >= 0.7 {
			return -0.1 // Penalty for complex tasks
		}
	case agent.AgentTypeGemini, agent.AgentTypeAntigravity:
		// Gemini (and its successor Antigravity) are balanced
		if taskComplexity >= 0.4 && taskComplexity <= 0.6 {
			return 0.05 // Small bonus for medium complexity
		}
	}

	return 0
}

// estimateTaskComplexity returns a 0-1 score based on task characteristics.
func estimateTaskComplexity(rec *bv.TriageRecommendation) float64 {
	complexity := 0.5 // Start with medium

	// Task type affects complexity
	switch rec.Type {
	case "epic":
		complexity += 0.3
	case "feature":
		complexity += 0.2
	case "bug":
		complexity += 0.0 // Varies
	case "task":
		complexity -= 0.1
	case "chore":
		complexity -= 0.2
	}

	// Priority affects perceived complexity (urgent items often simpler)
	if rec.Priority == 0 {
		complexity -= 0.1 // Critical items often need quick fixes
	} else if rec.Priority >= 3 {
		complexity += 0.1 // Backlog items often bigger
	}

	// Number of items unblocked indicates scope
	if len(rec.UnblocksIDs) >= 5 {
		complexity += 0.15
	} else if len(rec.UnblocksIDs) >= 3 {
		complexity += 0.1
	}

	// Clamp to 0-1
	if complexity < 0 {
		complexity = 0
	} else if complexity > 1 {
		complexity = 1
	}

	return complexity
}

// computeCriticalPathBonus gives bonus for items with high graph centrality.
func computeCriticalPathBonus(breakdown *bv.ScoreBreakdown) float64 {
	bonus := 0.0

	// High PageRank means central to the project
	if breakdown.Pagerank > 0.05 {
		bonus += breakdown.Pagerank * 2 // Up to ~0.15 bonus
	}

	// High blocker ratio means it unblocks many things
	if breakdown.BlockerRatio > 0.05 {
		bonus += breakdown.BlockerRatio * 1.5
	}

	// Time-to-impact indicates depth in critical path
	if breakdown.TimeToImpact > 0.04 {
		bonus += 0.05
	}

	return bonus
}

// computeFileOverlapPenalty penalizes agents who already have many file reservations.
func computeFileOverlapPenalty(agent *AgentState, reservations map[string][]string) float64 {
	agentReservations := reservations[agent.PaneID]
	if len(agentReservations) == 0 {
		agentReservations = agent.Reservations
	}

	// Penalty increases with number of reservations
	// This encourages spreading work across agents
	count := len(agentReservations)
	if count == 0 {
		return 0
	} else if count <= 2 {
		return 0.05
	} else if count <= 5 {
		return 0.1
	}
	return 0.2
}

// computeContextPenalty penalizes agents with high context window usage.
// Both contextUsage and threshold are in percentage scale (0-100).
func computeContextPenalty(contextUsage float64, threshold float64) float64 {
	if contextUsage <= threshold {
		return 0
	}

	// Linear penalty above threshold, normalized to score scale (0-1)
	// e.g., 10% over threshold → 0.05 penalty; 20% over → 0.10 penalty
	excess := contextUsage - threshold
	return (excess / 100) * 0.5
}

// taskTagKeywords maps keywords to profile tags for task routing.
var taskTagKeywords = map[string]string{
	// Testing keywords
	"test":      "testing",
	"tests":     "testing",
	"testing":   "testing",
	"unittest":  "testing",
	"unit test": "testing",
	"e2e":       "testing",
	"qa":        "testing",
	"coverage":  "testing",

	// Architecture keywords
	"refactor":     "architecture",
	"restructure":  "architecture",
	"redesign":     "architecture",
	"architecture": "architecture",
	"pattern":      "architecture",
	"design":       "architecture",

	// Documentation keywords
	"document":      "documentation",
	"documentation": "documentation",
	"readme":        "documentation",
	"docs":          "documentation",
	"docstring":     "documentation",
	"comment":       "documentation",

	// Implementation keywords
	"implement": "implementation",
	"add":       "implementation",
	"create":    "implementation",
	"build":     "implementation",
	"feature":   "implementation",
	"develop":   "implementation",

	// Review keywords
	"review":  "review",
	"audit":   "review",
	"inspect": "review",
	"check":   "review",

	// Bug/fix keywords
	"fix":   "bugs",
	"bug":   "bugs",
	"patch": "bugs",
	"error": "bugs",
	"crash": "bugs",
}

// ExtractTaskTags extracts relevant profile tags from task title and description.
func ExtractTaskTags(title, description string) []string {
	text := strings.ToLower(title + " " + description)
	tagSet := make(map[string]bool)

	for keyword, tag := range taskTagKeywords {
		if strings.Contains(text, keyword) {
			tagSet[tag] = true
		}
	}

	tags := make([]string, 0, len(tagSet))
	for tag := range tagSet {
		tags = append(tags, tag)
	}
	return tags
}

// ExtractMentionedFiles extracts file paths mentioned in task text.
func ExtractMentionedFiles(title, description string) []string {
	text := title + " " + description
	words := strings.Fields(text)
	var files []string

	for _, word := range words {
		// Clean punctuation
		word = strings.Trim(word, ",.;:()[]{}\"'`")
		if isFilePath(word) {
			files = append(files, word)
		}
	}
	return files
}

// isFilePath checks if a string looks like a file path.
func isFilePath(s string) bool {
	if len(s) < 3 {
		return false
	}

	// Contains path separator or file extension
	if strings.Contains(s, "/") || strings.Contains(s, "\\") {
		return true
	}

	// Has common file extensions
	extensions := []string{".go", ".ts", ".js", ".py", ".rs", ".md", ".yaml", ".yml", ".json", ".toml"}
	for _, ext := range extensions {
		if strings.HasSuffix(s, ext) {
			return true
		}
	}

	// Contains glob patterns
	if strings.Contains(s, "*") || strings.Contains(s, "**") {
		return true
	}

	// Starts with dot (hidden file/directory)
	if strings.HasPrefix(s, ".") && len(s) > 1 {
		return true
	}

	return false
}

// computeProfileTagBonus computes bonus based on matching persona tags.
func computeProfileTagBonus(profile *persona.Persona, taskTags []string, weight float64) float64 {
	if profile == nil || len(profile.Tags) == 0 || len(taskTags) == 0 {
		return 0
	}

	// Create a set of profile tags for quick lookup
	profileTags := make(map[string]bool)
	for _, tag := range profile.Tags {
		profileTags[strings.ToLower(tag)] = true
	}

	// Count matching tags
	matches := 0
	for _, tag := range taskTags {
		if profileTags[strings.ToLower(tag)] {
			matches++
		}
	}

	if matches == 0 {
		return 0
	}

	// Score based on proportion of profile tags matched
	matchRatio := float64(matches) / float64(len(profile.Tags))
	return matchRatio * weight
}

// computeFocusPatternBonus computes bonus based on file pattern matches.
func computeFocusPatternBonus(profile *persona.Persona, mentionedFiles []string, weight float64) float64 {
	if profile == nil || len(profile.FocusPatterns) == 0 || len(mentionedFiles) == 0 {
		return 0
	}

	// Count how many mentioned files match any focus pattern
	matches := 0
	for _, file := range mentionedFiles {
		for _, pattern := range profile.FocusPatterns {
			if matchFocusPattern(pattern, file) {
				matches++
				break // Count each file only once
			}
		}
	}

	if matches == 0 {
		return 0
	}

	// Score based on proportion of files matched
	matchRatio := float64(matches) / float64(len(mentionedFiles))
	return matchRatio * weight
}

// matchFocusPattern checks if a file matches a focus pattern using glob-style matching.
func matchFocusPattern(pattern, file string) bool {
	// Handle ** (any path depth)
	if strings.Contains(pattern, "**") {
		// Convert ** to regex-style matching
		parts := strings.Split(pattern, "**")
		if len(parts) == 2 {
			prefix := parts[0]
			suffix := strings.TrimPrefix(parts[1], "/")

			// File must start with prefix
			if prefix != "" && !strings.HasPrefix(file, prefix) {
				return false
			}

			// File must end with suffix (if any)
			if suffix != "" {
				// Remove leading * from suffix for extension matching
				suffix = strings.TrimPrefix(suffix, "*")
				return strings.HasSuffix(file, suffix)
			}
			return true
		}
	}

	// Use filepath.Match for simple glob patterns
	matched, err := filepath.Match(pattern, file)
	if err != nil {
		return false
	}
	return matched
}
