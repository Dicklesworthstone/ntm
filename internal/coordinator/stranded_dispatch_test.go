package coordinator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	assignmentstore "github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// setCoordinatorLivePanes installs a fresh physical topology snapshot, the way
// a successful whole-session observation would.
func setCoordinatorLivePanes(c *SessionCoordinator, paneIDs ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.livePaneIDs = make(map[string]struct{}, len(paneIDs))
	c.deadPaneIDs = nil
	for _, paneID := range paneIDs {
		c.livePaneIDs[paneID] = struct{}{}
	}
	c.livePaneTopologyAt = time.Now().UTC()
	c.livePaneTopologyValid = true
}

// strandedDispatchLedger builds the incident shape from ntm#304: a claimed row
// that recorded its dispatch barrier and never recorded an outcome.
func strandedDispatchLedger(t *testing.T, session, beadID, target, agentName string) (*assignmentstore.AssignmentStore, assignmentstore.AtomicRequest) {
	t.Helper()
	request := assignmentstore.AtomicRequest{
		BeadID: beadID, BeadTitle: "Stranded work", Target: target, OccupancyKey: target, Pane: 6,
		AgentType: "cod", AgentName: agentName, Actor: agentName,
		Prompt: "durable stranded dispatch prompt", IdempotencyKey: beadID + "-generation",
	}
	store := assignmentstore.NewStore(session)
	actor := assignmentstore.StableClaimActor(request.Actor, request.IdempotencyKey)
	if _, err := store.RecordAtomicIntent(request, actor, time.Now().UTC()); err != nil {
		t.Fatalf("RecordAtomicIntent: %v", err)
	}
	if _, err := store.RecordAtomicClaim(request, assignmentstore.ClaimReceipt{
		BeadID: request.BeadID, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordAtomicClaim: %v", err)
	}
	if err := store.RecordAtomicDispatchStarted(request.BeadID, request.IdempotencyKey, time.Now().UTC()); err != nil {
		t.Fatalf("RecordAtomicDispatchStarted: %v", err)
	}
	return store, request
}

func newStrandedDispatchCoordinator(t *testing.T, session string) *SessionCoordinator {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := New(session, t.TempDir(), &agentmail.Client{}, "CoordinatorAgent")
	c.config.AutoAssign = true
	c.config.AssignOnlyIdle = true
	c.config.IdleThreshold = 0
	c.workItemStatusFn = func(context.Context, string) (string, error) { return "in_progress", nil }
	c.actionableRecommendationsFn = func(context.Context, string, int) ([]bv.TriageRecommendation, error) { return nil, nil }
	c.releaseWorkItemClaimFn = func(context.Context, string, string, string) (bool, error) { return true, nil }
	c.atomicCoordinatorFactory = func(*assignmentstore.AssignmentStore) *assignmentstore.AtomicCoordinator {
		t.Fatal("a stranded dispatch must never reach an atomic dispatch")
		return nil
	}
	store, err := assignmentstore.LoadStoreStrict(session)
	if err != nil {
		t.Fatal(err)
	}
	registry := agentmail.NewSessionAgentRegistry(session, c.projectKey)
	for _, current := range store.ListActive() {
		registry.AddAgent(session+"__cod_6", current.OccupancyKey, current.AgentName)
	}
	if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
		t.Fatal(err)
	}
	c.workItemDetailsFn = func(_ context.Context, beadID string) (*bv.BeadAssignmentDetails, error) {
		current := store.Get(beadID)
		if current == nil {
			return nil, errors.New("test tracker has no such assignment")
		}
		return &bv.BeadAssignmentDetails{ID: beadID, Status: "in_progress", Assignee: current.ClaimActor}, nil
	}
	c.monitor = NewAgentMonitor(session, nil, c.projectKey)
	setLostAssignmentObservation(c, func(context.Context, string) ([]tmux.PaneActivity, error) {
		panes, observedAt, ok := c.assignmentOwnerTopology()
		if !ok || !status.DispatchObservationIsCurrent(observedAt, time.Now()) {
			return nil, errors.New("test topology is unavailable")
		}
		result := make([]tmux.PaneActivity, 0, len(panes))
		for id := range panes {
			result = append(result, tmux.PaneActivity{Pane: tmux.Pane{ID: id, Type: tmux.AgentUser}})
		}
		return result, nil
	}, nil)
	c.allPanesForRecoveryFn = func(context.Context) (map[string][]tmux.Pane, error) {
		panes, _, ok := c.assignmentOwnerTopology()
		if !ok {
			return nil, errors.New("test topology is unavailable")
		}
		result := make([]tmux.Pane, 0, len(panes))
		for id, dead := range panes {
			result = append(result, tmux.Pane{ID: id, Dead: dead})
		}
		return map[string][]tmux.Pane{session: result}, nil
	}
	return c
}

// TestStrandedDispatchTerminalizesWhenPaneIsGoneAndNothingLanded is the core
// recovery: the pane named by the row is gone from live topology and the probe
// proves the message never landed, so the row is retired and its pane freed.
func TestStrandedDispatchTerminalizesWhenPaneIsGoneAndNothingLanded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "coordinator-stranded-terminalize"
	store, request := strandedDispatchLedger(t, session, "ntm-stranded", "%993", "BlueLake")
	before := store.Get(request.BeadID)

	c := newStrandedDispatchCoordinator(t, session)
	setCoordinatorLivePanes(c, "%77")
	probeCalls := 0
	c.dispatchDeliveryProbeFn = func(_ context.Context, recorded *assignmentstore.Assignment) (assignmentstore.DispatchReceipt, bool, error) {
		probeCalls++
		if recorded.BeadID != request.BeadID || recorded.IdempotencyKey != request.IdempotencyKey {
			t.Fatalf("probe saw the wrong generation: %+v", recorded)
		}
		return assignmentstore.DispatchReceipt{}, false, nil
	}

	results, err := c.AssignWork(t.Context())
	if err != nil {
		t.Fatalf("AssignWork: %v", err)
	}
	if probeCalls != 1 {
		t.Fatalf("delivery probe calls = %d, want 1", probeCalls)
	}
	if len(results) != 1 || results[0].Success || !strings.Contains(results[0].Error, "%993") {
		t.Fatalf("stranded results = %+v", results)
	}

	reloaded, err := assignmentstore.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("reload ledger: %v", err)
	}
	stored := reloaded.Get(request.BeadID)
	if stored == nil {
		t.Fatal("the stranded row disappeared instead of being retired")
	}
	if stored.Status != assignmentstore.StatusFailed {
		t.Fatalf("status = %q, want failed", stored.Status)
	}
	if stored.DispatchState == assignmentstore.DispatchSending {
		t.Fatal("the outcome-unknown barrier survived terminalization")
	}
	if !strings.Contains(stored.FailReason, "%993") {
		t.Fatalf("fail_reason = %q, want the missing target named", stored.FailReason)
	}
	if !strings.Contains(stored.StrandedDispatchReason, "%993") || stored.StrandedDispatchResolvedAt == nil {
		t.Fatalf("audit record = %q at %v", stored.StrandedDispatchReason, stored.StrandedDispatchResolvedAt)
	}
	// The incident row's identity must survive the retirement intact.
	if stored.IdempotencyKey != before.IdempotencyKey || stored.ClaimActor != before.ClaimActor ||
		stored.OccupancyKey != before.OccupancyKey || stored.DispatchAttempts != before.DispatchAttempts ||
		!stored.AssignedAt.Equal(before.AssignedAt) {
		t.Fatalf("preserved incident metadata drifted: before=%+v after=%+v", before, stored)
	}
	for _, active := range reloaded.ListActive() {
		if active.BeadID == request.BeadID {
			t.Fatal("the retired row still occupies its pane")
		}
	}
}

// TestStrandedDispatchStaysFailClosedForALiveTarget covers the predicate that
// PR #296 got wrong: eligibility is not existence. A pane that is alive but
// busy, unhealthy, or simply not a candidate must never be terminalized on a
// negative mailbox probe.
func TestStrandedDispatchStaysFailClosedForALiveTarget(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(*SessionCoordinator)
	}{
		{
			name: "busy",
			prepare: func(c *SessionCoordinator) {
				now := time.Now().UTC()
				c.agents["%993"] = &AgentState{
					PaneID: "%993", PaneIndex: 6, AgentType: "cod", AgentMailName: "BlueLake",
					Status: robot.StateGenerating, Healthy: true, SafeToDispatch: false,
					LastActivity: now, ObservedAt: now, ObservationFreshness: status.FreshnessFresh,
				}
			},
		},
		{
			name: "unrecognized agent",
			// The pane exists in topology but carries no tracked agent at all,
			// which is exactly the case `agents` cannot see.
			prepare: func(*SessionCoordinator) {},
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			session := "coordinator-stranded-live-" + strings.ReplaceAll(test.name, " ", "-")
			store, request := strandedDispatchLedger(t, session, "ntm-stranded-live", "%993", "BlueLake")

			c := newStrandedDispatchCoordinator(t, session)
			test.prepare(c)
			setCoordinatorLivePanes(c, "%993", "%77")
			c.dispatchDeliveryProbeFn = func(context.Context, *assignmentstore.Assignment) (assignmentstore.DispatchReceipt, bool, error) {
				return assignmentstore.DispatchReceipt{}, false, nil
			}

			results, err := c.AssignWork(t.Context())
			if err != nil {
				t.Fatalf("AssignWork: %v", err)
			}
			if len(results) != 1 || results[0].Success ||
				!strings.Contains(results[0].Error, assignmentstore.ErrDispatchOutcomeUnknown.Error()) {
				t.Fatalf("live-target results = %+v", results)
			}
			assertStrandedRowUnchanged(t, store, session, request)
		})
	}
}

// TestStrandedDispatchStaysFailClosedWithoutUsableEvidence walks every way the
// evidence can be missing. Each one must leave the barrier exactly as it was.
func TestStrandedDispatchStaysFailClosedWithoutUsableEvidence(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(*testing.T, *SessionCoordinator)
		want    string
	}{
		{
			name:    "no topology observation yet",
			prepare: func(*testing.T, *SessionCoordinator) {},
			want:    "no fresh complete pane topology",
		},
		{
			name: "topology observation is stale",
			prepare: func(_ *testing.T, c *SessionCoordinator) {
				setCoordinatorLivePanes(c, "%77")
				c.mu.Lock()
				c.livePaneTopologyAt = time.Now().Add(-24 * time.Hour)
				c.mu.Unlock()
			},
			want: "no fresh complete pane topology",
		},
		{
			name: "recipient is live on another pane",
			prepare: func(t *testing.T, c *SessionCoordinator) {
				setCoordinatorLivePanes(c, "%77")
				now := time.Now().UTC()
				// The pane the row named is gone, but the agent it addressed
				// came back somewhere else and can still read its mail.
				c.agents["%77"] = &AgentState{
					PaneID: "%77", PaneIndex: 1, AgentType: "cod", AgentMailName: "BlueLake",
					Status: robot.StateWaiting, Healthy: true, SafeToDispatch: true,
					LastActivity: now.Add(-time.Minute), ObservedAt: now, ObservationFreshness: status.FreshnessFresh,
				}
				registry, err := agentmail.LoadSessionAgentRegistry(c.session, c.projectKey)
				if err != nil {
					t.Fatal(err)
				}
				registry.AddAgent("rebound", "%77", "BlueLake")
				if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
					t.Fatal(err)
				}
			},
			want: "no longer has its recorded Agent Mail owner BlueLake",
		},
		{
			name: "delivery probe failed",
			prepare: func(_ *testing.T, c *SessionCoordinator) {
				setCoordinatorLivePanes(c, "%77")
				c.dispatchDeliveryProbeFn = func(context.Context, *assignmentstore.Assignment) (assignmentstore.DispatchReceipt, bool, error) {
					return assignmentstore.DispatchReceipt{}, false, errors.New("agent mail unreachable")
				}
			},
			want: "could not be verified",
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			session := "coordinator-stranded-" + strings.ReplaceAll(test.name, " ", "-")
			store, request := strandedDispatchLedger(t, session, "ntm-stranded-evidence", "%993", "BlueLake")

			c := newStrandedDispatchCoordinator(t, session)
			c.dispatchDeliveryProbeFn = func(context.Context, *assignmentstore.Assignment) (assignmentstore.DispatchReceipt, bool, error) {
				return assignmentstore.DispatchReceipt{}, false, nil
			}
			test.prepare(t, c)

			results, err := c.AssignWork(t.Context())
			if err != nil {
				t.Fatalf("AssignWork: %v", err)
			}
			if len(results) != 1 || results[0].Success ||
				!strings.Contains(results[0].Error, assignmentstore.ErrDispatchOutcomeUnknown.Error()) ||
				!strings.Contains(results[0].Error, test.want) {
				t.Fatalf("results = %+v, want an outcome-unknown error mentioning %q", results, test.want)
			}
			assertStrandedRowUnchanged(t, store, session, request)
		})
	}
}

// TestStrandedDispatchAdoptsAMessageThatActuallyLanded is the dangerous race:
// the dispatch really did go out and only the bookkeeping was lost. The row
// must converge to "sent" — never to a non-delivery — so the assignment can
// never be handed to a second agent.
func TestStrandedDispatchAdoptsAMessageThatActuallyLanded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "coordinator-stranded-landed"
	store, request := strandedDispatchLedger(t, session, "ntm-stranded-landed", "%993", "BlueLake")
	before := store.Get(request.BeadID)

	c := newStrandedDispatchCoordinator(t, session)
	setCoordinatorLivePanes(c, "%77")
	c.dispatchDeliveryProbeFn = func(context.Context, *assignmentstore.Assignment) (assignmentstore.DispatchReceipt, bool, error) {
		return assignmentstore.DispatchReceipt{DeliveryID: "agent-mail-message-4242"}, true, nil
	}
	// A ready recommendation for the same bead exists and a free pane is
	// waiting: if the row were mistaken for a non-delivery, this is where the
	// second dispatch would happen.
	now := time.Now().UTC()
	c.agents["%77"] = &AgentState{
		PaneID: "%77", PaneIndex: 1, AgentType: "cod", AgentMailName: "GreenHill",
		Status: robot.StateWaiting, Healthy: true, SafeToDispatch: true,
		LastActivity: now.Add(-time.Minute), ObservedAt: now, ObservationFreshness: status.FreshnessFresh,
	}
	c.actionableRecommendationsFn = func(context.Context, string, int) ([]bv.TriageRecommendation, error) {
		return []bv.TriageRecommendation{{
			ID: request.BeadID, Title: "Stranded work", Type: "task", Status: "open", Priority: 1, Score: 1,
		}}, nil
	}
	c.workItemDetailsFn = func(context.Context, string) (*bv.BeadAssignmentDetails, error) {
		return &bv.BeadAssignmentDetails{ID: request.BeadID, Title: "Stranded work", IssueType: "task", Status: "open"}, nil
	}

	results, err := c.AssignWork(t.Context())
	if err != nil {
		t.Fatalf("AssignWork: %v", err)
	}
	if len(results) != 1 || !results[0].Success || !results[0].MessageSent {
		t.Fatalf("landed-dispatch results = %+v", results)
	}

	reloaded, err := assignmentstore.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("reload ledger: %v", err)
	}
	stored := reloaded.Get(request.BeadID)
	if stored == nil || stored.DispatchState != assignmentstore.DispatchSent {
		t.Fatalf("dispatch state = %+v, want sent", stored)
	}
	if stored.DispatchReceiptID != "agent-mail-message-4242" {
		t.Fatalf("receipt = %q, want the discovered delivery", stored.DispatchReceiptID)
	}
	if stored.Status != assignmentstore.StatusAssigned {
		t.Fatalf("status = %q, want assigned", stored.Status)
	}
	if stored.PendingPrompt != "" || stored.PromptSent != before.PendingPrompt {
		t.Fatalf("prompt bookkeeping = pending %q sent %q", stored.PendingPrompt, stored.PromptSent)
	}
	if stored.IdempotencyKey != before.IdempotencyKey || stored.OccupancyKey != before.OccupancyKey {
		t.Fatalf("adopted row changed generation: before=%+v after=%+v", before, stored)
	}
	if stored.StrandedDispatchResolvedAt == nil || !strings.Contains(stored.StrandedDispatchReason, "agent-mail-message-4242") {
		t.Fatalf("audit record = %q at %v", stored.StrandedDispatchReason, stored.StrandedDispatchResolvedAt)
	}
}

// TestStrandedDispatchLetsAReadyBeadThroughInTheSameCycle proves the stall is
// actually cleared: once dead A is retired, ready B lands on the free pane
// without waiting for the next poll.
func TestStrandedDispatchLetsAReadyBeadThroughInTheSameCycle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "coordinator-stranded-unblocks"
	_, request := strandedDispatchLedger(t, session, "ntm-stranded-blocker", "%993", "BlueLake")

	c := newStrandedDispatchCoordinator(t, session)
	setCoordinatorLivePanes(c, "%77")
	c.dispatchDeliveryProbeFn = func(context.Context, *assignmentstore.Assignment) (assignmentstore.DispatchReceipt, bool, error) {
		return assignmentstore.DispatchReceipt{}, false, nil
	}
	now := time.Now().UTC()
	c.agents["%77"] = &AgentState{
		PaneID: "%77", PaneIndex: 1, AgentType: "cod", AgentMailName: "GreenHill",
		Status: robot.StateWaiting, Healthy: true, SafeToDispatch: true,
		LastActivity: now.Add(-time.Minute), ObservedAt: now, ObservationFreshness: status.FreshnessFresh,
	}
	c.actionableRecommendationsFn = func(context.Context, string, int) ([]bv.TriageRecommendation, error) {
		return []bv.TriageRecommendation{{
			ID: "ntm-ready-b", Title: "Ready work", Type: "task", Status: "open", Priority: 1, Score: 1,
		}}, nil
	}
	c.workItemDetailsFn = func(_ context.Context, beadID string) (*bv.BeadAssignmentDetails, error) {
		if beadID == request.BeadID {
			return &bv.BeadAssignmentDetails{ID: beadID, Status: "in_progress", Assignee: assignmentstore.StableClaimActor(request.Actor, request.IdempotencyKey)}, nil
		}
		return &bv.BeadAssignmentDetails{ID: beadID, Title: "Ready work", IssueType: "task", Status: "open"}, nil
	}

	dispatched := make([]string, 0, 1)
	c.atomicCoordinatorFactory = func(store *assignmentstore.AssignmentStore) *assignmentstore.AtomicCoordinator {
		claim := assignmentstore.ClaimFunc(func(_ context.Context, beadID, actor string) (assignmentstore.ClaimReceipt, error) {
			return assignmentstore.ClaimReceipt{BeadID: beadID, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC()}, nil
		})
		reserve := assignmentstore.ReservationFunc(func(_ context.Context, req assignmentstore.ReservationRequest) (assignmentstore.LeaseReceipt, error) {
			return assignmentstore.LeaseReceipt{AgentName: req.AgentName, Target: req.Target}, nil
		})
		dispatch := assignmentstore.DispatchFunc(func(_ context.Context, req assignmentstore.DispatchRequest) (assignmentstore.DispatchReceipt, error) {
			if req.BeadID == request.BeadID {
				t.Fatalf("the stranded bead was dispatched a second time: %+v", req)
			}
			dispatched = append(dispatched, req.BeadID)
			return assignmentstore.DispatchReceipt{DeliveryID: "mail-ready-b"}, nil
		})
		preflight := assignmentstore.PromptPreflightFunc(func(_ context.Context, req assignmentstore.DispatchRequest) (assignmentstore.PromptPreflightResult, error) {
			return assignmentstore.PromptPreflightResult{DispatchPrompt: req.Prompt, DurablePrompt: req.Prompt}, nil
		})
		return assignmentstore.NewAtomicCoordinator(store, claim, reserve, dispatch, preflight)
	}

	if _, err := c.AssignWork(t.Context()); err != nil {
		t.Fatalf("AssignWork: %v", err)
	}
	if len(dispatched) != 1 || dispatched[0] != "ntm-ready-b" {
		t.Fatalf("same-cycle dispatches = %v, want only ntm-ready-b", dispatched)
	}
}

func TestStrandedDispatchRequiresFreshServerWideOwnershipProof(t *testing.T) {
	for _, failure := range []string{
		"pane moved to another session", "recipient rebound in another session",
		"owner returned during probe", "recipient rebound during probe", "topology failed during probe",
		"topology aged during probe", "recipient rebound during server query",
		"registry changed during server query", "server topology unavailable", "claim owner changed",
	} {
		t.Run(failure, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			const session = "stranded-owner-proof"
			store, request := strandedDispatchLedger(t, session, "ntm-stranded-owner", "%993", "BlueLake")
			c := newStrandedDispatchCoordinator(t, session)
			setCoordinatorLivePanes(c, "%77")
			claims := 0
			c.releaseWorkItemClaimFn = func(context.Context, string, string, string) (bool, error) {
				claims++
				return true, nil
			}
			rebind := func(paneID string) {
				registry, err := agentmail.LoadSessionAgentRegistry(session, c.projectKey)
				if err != nil {
					t.Fatal(err)
				}
				registry.PaneIDMap[paneID] = request.AgentName
				if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
					t.Fatal(err)
				}
			}
			c.dispatchDeliveryProbeFn = func(context.Context, *assignmentstore.Assignment) (assignmentstore.DispatchReceipt, bool, error) {
				switch failure {
				case "owner returned during probe":
					setCoordinatorLivePanes(c, "%77", request.Target)
				case "recipient rebound during probe":
					rebind("%77")
				case "topology failed during probe":
					c.mu.Lock()
					c.livePaneTopologyValid = false
					c.mu.Unlock()
				case "topology aged during probe":
					c.mu.Lock()
					c.livePaneTopologyAt = time.Now().Add(-time.Minute)
					c.mu.Unlock()
				}
				return assignmentstore.DispatchReceipt{}, false, nil
			}
			switch failure {
			case "pane moved to another session":
				c.allPanesForRecoveryFn = func(context.Context) (map[string][]tmux.Pane, error) {
					return map[string][]tmux.Pane{session: {{ID: "%77"}}, "destination": {{ID: request.Target}}}, nil
				}
			case "recipient rebound in another session":
				registry := agentmail.NewSessionAgentRegistry("destination", c.projectKey)
				registry.AddAgent("shell", "%88", request.AgentName)
				if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
					t.Fatal(err)
				}
				c.allPanesForRecoveryFn = func(context.Context) (map[string][]tmux.Pane, error) {
					return map[string][]tmux.Pane{session: {{ID: "%77"}}, "destination": {{ID: "%88", Type: tmux.AgentUser}}}, nil
				}
			case "recipient rebound during server query":
				c.allPanesForRecoveryFn = func(context.Context) (map[string][]tmux.Pane, error) {
					rebind("%77")
					return map[string][]tmux.Pane{session: {{ID: "%77"}}}, nil
				}
			case "registry changed during server query":
				c.allPanesForRecoveryFn = func(context.Context) (map[string][]tmux.Pane, error) {
					registry, err := agentmail.LoadSessionAgentRegistry(session, c.projectKey)
					if err != nil {
						t.Fatal(err)
					}
					registry.PaneIDMap[request.Target] = "OtherOwner"
					if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
						t.Fatal(err)
					}
					return map[string][]tmux.Pane{session: {{ID: "%77"}}}, nil
				}
			case "server topology unavailable":
				c.allPanesForRecoveryFn = func(context.Context) (map[string][]tmux.Pane, error) {
					return nil, errors.New("tmux server query failed")
				}
			case "claim owner changed":
				c.workItemDetailsFn = func(context.Context, string) (*bv.BeadAssignmentDetails, error) {
					return &bv.BeadAssignmentDetails{ID: request.BeadID, Status: "in_progress", Assignee: "other-generation"}, nil
				}
			}
			results, err := c.AssignWork(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].Success || !strings.Contains(results[0].Error, "owner loss is unproven") {
				t.Fatalf("unproven owner loss results = %+v", results)
			}
			if claims != 0 {
				t.Fatalf("released %d claims despite unproven owner loss", claims)
			}
			assertStrandedRowUnchanged(t, store, session, request)
		})
	}
}

func TestStrandedDispatchRecoversDeliveredMessageAcrossCoordinatorRestart(t *testing.T) {
	for _, topology := range []string{"live owner", "moved owner", "unavailable"} {
		t.Run(topology, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			const session = "stranded-delivered-restart"
			store, request := strandedDispatchLedger(t, session, "ntm-delivered-restart", "%993", "BlueLake")
			c := newStrandedDispatchCoordinator(t, session)
			c.agentName = "NewCoordinator"
			if topology == "live owner" {
				setCoordinatorLivePanes(c, request.Target)
			} else if topology == "moved owner" {
				setCoordinatorLivePanes(c, "%77")
				registry := agentmail.NewSessionAgentRegistry("destination", c.projectKey)
				registry.AddAgent("worker", request.Target, request.AgentName)
				if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
					t.Fatal(err)
				}
			}
			server := newMailInboxServer(t, map[string][]map[string]interface{}{
				request.AgentName: {{
					"id": 4242, "subject": coordinatorAssignmentSubject(request.BeadID),
					"from": "OriginalCoordinator", "created_ts": time.Now().UTC().Format(time.RFC3339Nano),
				}},
			})
			defer server.Close()
			c.mailClient = agentmail.NewClient(agentmail.WithBaseURL(server.URL + "/"))
			c.workItemDetailsFn = func(context.Context, string) (*bv.BeadAssignmentDetails, error) {
				t.Fatal("positive delivery evidence does not require owner loss")
				return nil, nil
			}
			c.releaseWorkItemClaimFn = func(context.Context, string, string, string) (bool, error) {
				t.Fatal("a delivered assignment must retain its claim")
				return false, nil
			}
			results, err := c.AssignWork(t.Context())
			if err != nil || len(results) != 1 || !results[0].Success || !results[0].MessageSent {
				t.Fatalf("recovered delivery results=%+v err=%v", results, err)
			}
			if err := store.LoadStrict(); err != nil {
				t.Fatal(err)
			}
			current := store.Get(request.BeadID)
			if current.DispatchState != assignmentstore.DispatchSent || current.DispatchReceiptID != "agent-mail-message-4242" ||
				current.ClearState != assignmentstore.ClearStateNone || current.Status != assignmentstore.StatusAssigned {
				t.Fatalf("delivered assignment was not recovered forward: %+v", current)
			}
		})
	}
}

func TestStrandedDispatchDoesNotInferNonDeliveryFromMalformedOrSaturatedInbox(t *testing.T) {
	for _, shape := range []string{"null row", "empty row", "invalid ID", "missing subject", "full page"} {
		t.Run(shape, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			const session = "stranded-mailbox-proof"
			store, request := strandedDispatchLedger(t, session, "ntm-mailbox-proof", "%993", "BlueLake")
			c := newStrandedDispatchCoordinator(t, session)
			setCoordinatorLivePanes(c, "%77")
			var messages []map[string]interface{}
			switch shape {
			case "null row":
				messages = []map[string]interface{}{nil}
			case "empty row":
				messages = []map[string]interface{}{{}}
			case "invalid ID":
				messages = []map[string]interface{}{{"id": 0, "subject": coordinatorAssignmentSubject(request.BeadID)}}
			case "missing subject":
				messages = []map[string]interface{}{{"id": 44}}
			case "full page":
				for i := 1; i <= coordinatorDispatchProbeLimit; i++ {
					messages = append(messages, map[string]interface{}{"id": i, "subject": "unrelated"})
				}
			}
			server := newMailInboxServer(t, map[string][]map[string]interface{}{request.AgentName: messages})
			defer server.Close()
			c.mailClient = agentmail.NewClient(agentmail.WithBaseURL(server.URL + "/"))
			c.releaseWorkItemClaimFn = func(context.Context, string, string, string) (bool, error) {
				t.Fatal("an inconclusive inbox cannot authorize claim release")
				return false, nil
			}
			results, err := c.AssignWork(t.Context())
			if err != nil || len(results) != 1 || results[0].Success || !strings.Contains(results[0].Error, "could not be verified") {
				t.Fatalf("inconclusive inbox results=%+v err=%v", results, err)
			}
			assertStrandedRowUnchanged(t, store, session, request)
		})
	}
}

func TestStrandedDispatchWaitsForActiveSenderBeforeProbing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "stranded-active-sender"
	store, request := strandedDispatchLedger(t, session, "ntm-active-sender", "%993", "BlueLake")
	current := store.Get(request.BeadID)
	c := newStrandedDispatchCoordinator(t, session)
	setCoordinatorLivePanes(c, "%77")
	unlock, err := store.AcquireExternalCleanupLock(t.Context(), request.BeadID)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	c.dispatchDeliveryProbeFn = func(context.Context, *assignmentstore.Assignment) (assignmentstore.DispatchReceipt, bool, error) {
		t.Fatal("mailbox must not be probed while the original dispatch can still finish")
		return assignmentstore.DispatchReceipt{}, false, nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	result := c.resolveStrandedDispatch(ctx, store, current, coordinatorWorkAssignmentFromRecord(current))
	if result.Success || !strings.Contains(result.Error, "lock stranded assignment recovery") {
		t.Fatalf("active sender did not block recovery: %+v", result)
	}
	assertStrandedRowUnchanged(t, store, session, request)
}

// TestUpdateAgentStatesRecordsEveryPhysicalPane pins the topology snapshot to
// the raw pane listing rather than to the tracked-agent map: a pane holding a
// plain shell is still a pane, and reading it as "gone" would license a second
// dispatch into it.
func TestUpdateAgentStatesRecordsEveryPhysicalPane(t *testing.T) {
	origGetPanesWithActivity := getPanesWithActivity
	origCaptureForHealthCheckWithCtx := captureForHealthCheckWithCtx
	t.Cleanup(func() {
		getPanesWithActivity = origGetPanesWithActivity
		captureForHealthCheckWithCtx = origCaptureForHealthCheckWithCtx
	})

	lastActivity := time.Now().Add(-time.Minute).UTC()
	getPanesWithActivity = func(context.Context, string) ([]tmux.PaneActivity, error) {
		return []tmux.PaneActivity{
			{Pane: tmux.Pane{ID: "%0", Index: 0, Title: "test-session__cc_1", Type: tmux.AgentClaude}, LastActivity: lastActivity},
			{Pane: tmux.Pane{ID: "%1", Index: 1, Title: "shell", Type: tmux.AgentUser}, LastActivity: lastActivity},
			{Pane: tmux.Pane{ID: "%2", Index: 2, Title: "mystery", Type: tmux.AgentUnknown}, LastActivity: lastActivity},
		}, nil
	}
	captureForHealthCheckWithCtx = func(context.Context, string) (string, error) { return "", nil }

	c := New("test-session", t.TempDir(), nil, "TestAgent")
	c.monitor = NewAgentMonitor(c.session, nil, c.projectKey)
	_ = c.updateAgentStatesContext(context.Background())

	topology, observedAt, ok := c.assignmentOwnerTopology()
	if !ok {
		t.Fatal("a successful observation left no topology snapshot")
	}
	if observedAt.IsZero() {
		t.Fatal("topology snapshot has no observation time")
	}
	for _, paneID := range []string{"%0", "%1", "%2"} {
		if _, present := topology[paneID]; !present {
			t.Fatalf("pane %s missing from the physical topology snapshot: %v", paneID, topology)
		}
	}
	if _, tracked := c.GetAgents()["%1"]; tracked {
		t.Fatal("a user pane must not become a tracked agent")
	}

	// A failed observation must clear the snapshot rather than let a stale one
	// answer "does this pane exist?".
	getPanesWithActivity = func(context.Context, string) ([]tmux.PaneActivity, error) { return nil, errors.New("topology failed") }
	_ = c.updateAgentStatesContext(context.Background())
	if _, _, ok := c.assignmentOwnerTopology(); ok {
		t.Fatal("a failed observation left the previous topology snapshot usable")
	}
}

func assertStrandedRowUnchanged(t *testing.T, store *assignmentstore.AssignmentStore, session string, request assignmentstore.AtomicRequest) {
	t.Helper()
	reloaded, err := assignmentstore.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("reload ledger: %v", err)
	}
	stored := reloaded.Get(request.BeadID)
	if stored == nil || stored.DispatchState != assignmentstore.DispatchSending ||
		stored.DispatchAttempts != 1 || stored.StrandedDispatchReason != "" || stored.StrandedDispatchResolvedAt != nil {
		t.Fatalf("the outcome-unknown barrier was disturbed: %+v", stored)
	}
	if stored.Status == assignmentstore.StatusFailed || stored.Status == assignmentstore.StatusCompleted {
		t.Fatalf("the row was terminalized without evidence: %+v", stored)
	}
	if inMemory := store.Get(request.BeadID); inMemory != nil && inMemory.DispatchState != assignmentstore.DispatchSending {
		t.Fatalf("in-memory row drifted from the barrier: %+v", inMemory)
	}
}
