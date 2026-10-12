package approval

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

func forceReleaseTestOperation() ForceReleaseOperation {
	return ForceReleaseOperation{
		ProjectKey: "/test/project", ReservationID: 17, RequestedBy: "alice",
		AgentName: "RedStone", Note: "holder crashed", NotifyPrevious: true,
	}
}

func forceReleaseTestLease(context.Context) (ForceReleaseLease, error) {
	return ForceReleaseLease{
		Holder: "BlueLake", Path: "internal/serve/*", CreatedAt: "2026-01-01T00:00:00Z",
		ExpiresAt: "2026-01-02T00:00:00Z", ProjectID: 4, Exclusive: true,
	}, nil
}

func requireForceReleaseDecision(t *testing.T, e *Engine, op ForceReleaseOperation, predecessor string, want string, allowed bool) ForceReleaseDecision {
	t.Helper()
	decision, err := e.EvaluateForceRelease(t.Context(), "approval", op, predecessor, forceReleaseTestLease)
	if err != nil {
		t.Fatalf("evaluate gate: %v", err)
	}
	if decision.ApprovalStatus != want || decision.Allowed != allowed {
		t.Fatalf("decision = %+v, want status %q allowed=%v", decision, want, allowed)
	}
	return decision
}

func TestForceReleaseGateReauthorizationIsReplaySafe(t *testing.T) {
	store := setupTestStore(t)
	engine := New(store, nil, nil, hermeticConfig())
	op := forceReleaseTestOperation()
	first := requireForceReleaseDecision(t, engine, op, "", "pending", false)
	if !first.Created || first.ApprovalID == "" {
		t.Fatalf("first request not created: %+v", first)
	}
	retry := requireForceReleaseDecision(t, engine, op, "", "pending", false)
	if retry.Created || retry.ApprovalID != first.ApprovalID {
		t.Fatalf("pending replay created a new request: %+v", retry)
	}
	if err := engine.Approve(t.Context(), first.ApprovalID, "alice"); !errors.Is(err, ErrSLBSelfApproval) {
		t.Fatalf("self-approval = %v, want ErrSLBSelfApproval", err)
	}
	if err := engine.Approve(t.Context(), first.ApprovalID, "bob"); err != nil {
		t.Fatal(err)
	}
	requireForceReleaseDecision(t, engine, op, "", "consumed", true)
	spent := requireForceReleaseDecision(t, engine, op, "", "consumed", false)
	if spent.Created || spent.ApprovalID != first.ApprovalID {
		t.Fatalf("spent replay minted approval: %+v", spent)
	}

	second := requireForceReleaseDecision(t, engine, op, first.ApprovalID, "pending", false)
	if !second.Created || second.ApprovalID == first.ApprovalID {
		t.Fatalf("explicit new attempt did not create a successor: %+v", second)
	}
	retry = requireForceReleaseDecision(t, engine, op, first.ApprovalID, "pending", false)
	if retry.Created || retry.ApprovalID != second.ApprovalID {
		t.Fatalf("renewal replay created a different successor: %+v", retry)
	}
	if err := engine.Approve(t.Context(), second.ApprovalID, "bob"); err != nil {
		t.Fatal(err)
	}
	requireForceReleaseDecision(t, engine, op, first.ApprovalID, "consumed", true)
	requireForceReleaseDecision(t, engine, op, first.ApprovalID, "consumed", false)
	history, err := engine.History(t.Context())
	if err != nil || len(history) != 2 {
		t.Fatalf("history after replays = %+v, %v; want exactly two requests", history, err)
	}
}

func TestForceReleaseGateTerminalOutcomesRequireExplicitAttempt(t *testing.T) {
	for _, terminal := range []string{"denied", "expired", "approved_expired"} {
		t.Run(terminal, func(t *testing.T) {
			store := setupTestStore(t)
			engine := New(store, nil, nil, hermeticConfig())
			op := forceReleaseTestOperation()
			first := requireForceReleaseDecision(t, engine, op, "", "pending", false)
			if terminal == "denied" {
				if err := engine.Deny(t.Context(), first.ApprovalID, "bob", "holder still active"); err != nil {
					t.Fatal(err)
				}
			} else {
				if terminal == "approved_expired" {
					if err := engine.Approve(t.Context(), first.ApprovalID, "bob"); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := store.DB().Exec("UPDATE approvals SET expires_at = ? WHERE id = ?", time.Now().Add(-time.Hour), first.ApprovalID); err != nil {
					t.Fatal(err)
				}
			}
			want := terminal
			if want == "approved_expired" {
				want = "expired"
			}
			blocked := requireForceReleaseDecision(t, engine, op, "", want, false)
			if blocked.Created || blocked.ApprovalID != first.ApprovalID {
				t.Fatalf("terminal replay replaced its record: %+v", blocked)
			}
			next := requireForceReleaseDecision(t, engine, op, first.ApprovalID, "pending", false)
			if !next.Created || next.ApprovalID == first.ApprovalID {
				t.Fatalf("explicit reauthorization did not create a request: %+v", next)
			}
		})
	}
}

func TestForceReleaseGateBindsEveryActionField(t *testing.T) {
	store := setupTestStore(t)
	engine := New(store, nil, nil, hermeticConfig())
	op := forceReleaseTestOperation()
	first := requireForceReleaseDecision(t, engine, op, "", "pending", false)
	if err := engine.Approve(t.Context(), first.ApprovalID, "bob"); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*ForceReleaseOperation){
		"project":      func(op *ForceReleaseOperation) { op.ProjectKey = "/other/project" },
		"reservation":  func(op *ForceReleaseOperation) { op.ReservationID++ },
		"requester":    func(op *ForceReleaseOperation) { op.RequestedBy = "mallory" },
		"mail actor":   func(op *ForceReleaseOperation) { op.AgentName = "GreenCastle" },
		"note":         func(op *ForceReleaseOperation) { op.Note = "different reason" },
		"notification": func(op *ForceReleaseOperation) { op.NotifyPrevious = false },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			other := op
			change(&other)
			if other.CorrelationKey() == op.CorrelationKey() {
				t.Fatal("operation key did not bind changed field")
			}
			_, err := engine.EvaluateForceRelease(t.Context(), "approval", other, first.ApprovalID, forceReleaseTestLease)
			if !errors.Is(err, ErrForceReleaseScope) {
				t.Fatalf("foreign approval reference = %v, want scope refusal", err)
			}
			requireForceReleaseDecision(t, engine, other, "", "pending", false)
		})
	}
	record, err := store.GetApproval(first.ApprovalID)
	if err != nil || record.Status != state.ApprovalApproved {
		t.Fatalf("foreign attempts spent original approval: %+v, %v", record, err)
	}
}

func TestForceReleaseGateChangedLeaseRequiresNewReview(t *testing.T) {
	store := setupTestStore(t)
	engine := New(store, nil, nil, hermeticConfig())
	op := forceReleaseTestOperation()
	first := requireForceReleaseDecision(t, engine, op, "", "pending", false)
	if err := engine.Approve(t.Context(), first.ApprovalID, "bob"); err != nil {
		t.Fatal(err)
	}
	changedLease := func(ctx context.Context) (ForceReleaseLease, error) {
		lease, _ := forceReleaseTestLease(ctx)
		lease.Holder = "GreenCastle"
		lease.CreatedAt = "2026-02-01T00:00:00Z"
		return lease, nil
	}
	decision, err := engine.EvaluateForceRelease(t.Context(), "approval", op, "", changedLease)
	if err != nil || decision.Allowed || decision.ApprovalStatus != "target_changed" {
		t.Fatalf("changed lease decision = %+v, %v", decision, err)
	}
	record, _ := store.GetApproval(first.ApprovalID)
	if record.Status != state.ApprovalApproved {
		t.Fatalf("changed target consumed old grant: %+v", record)
	}
	decision, err = engine.EvaluateForceRelease(t.Context(), "approval", op, first.ApprovalID, changedLease)
	if err != nil || !decision.Created || decision.Allowed || decision.ApprovalStatus != "pending" {
		t.Fatalf("new review for changed target = %+v, %v", decision, err)
	}
}

func TestForceReleaseGateCannotReplaceValidPendingOrApproved(t *testing.T) {
	engine := New(setupTestStore(t), nil, nil, hermeticConfig())
	op := forceReleaseTestOperation()
	first := requireForceReleaseDecision(t, engine, op, "", "pending", false)
	for _, status := range []string{"pending", "approved"} {
		if status == "approved" {
			if err := engine.Approve(t.Context(), first.ApprovalID, "bob"); err != nil {
				t.Fatal(err)
			}
		}
		decision := requireForceReleaseDecision(t, engine, op, first.ApprovalID, status, false)
		if decision.Created || decision.ApprovalID != first.ApprovalID {
			t.Fatalf("replaced a valid approval: %+v", decision)
		}
	}
}

func TestForceReleaseGateDeduplicatesAndConsumesAcrossConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	open := func() *Engine {
		store, err := state.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.Close() })
		if err := store.Migrate(); err != nil {
			t.Fatal(err)
		}
		return New(store, nil, nil, hermeticConfig())
	}
	engines := []*Engine{open(), open()}
	op := forceReleaseTestOperation()
	run := func() []ForceReleaseDecision {
		t.Helper()
		var wg sync.WaitGroup
		start := make(chan struct{})
		results := make([]ForceReleaseDecision, len(engines))
		errs := make([]error, len(engines))
		for i, engine := range engines {
			wg.Add(1)
			go func(i int, engine *Engine) {
				defer wg.Done()
				<-start
				results[i], errs[i] = engine.EvaluateForceRelease(t.Context(), "approval", op, "", forceReleaseTestLease)
			}(i, engine)
		}
		close(start)
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatalf("concurrent gate: %v", err)
			}
		}
		return results
	}
	results := run()
	if results[0].ApprovalID == "" || results[0].ApprovalID != results[1].ApprovalID ||
		results[0].Allowed || results[1].Allowed {
		t.Fatalf("concurrent requests were not deduplicated: %+v", results)
	}
	if results[0].Created == results[1].Created {
		t.Fatalf("exactly one request must be created: %+v", results)
	}
	if err := engines[0].Approve(t.Context(), results[0].ApprovalID, "bob"); err != nil {
		t.Fatal(err)
	}
	results = run()
	if results[0].Allowed == results[1].Allowed {
		t.Fatalf("exactly one concurrent execution must be admitted: %+v", results)
	}
	for _, decision := range results {
		if decision.ApprovalStatus != "consumed" || decision.Created {
			t.Fatalf("concurrent loser must report the spent grant: %+v", decision)
		}
	}
}

func TestForceReleaseGateCancellationDoesNotSpendApproval(t *testing.T) {
	store := setupTestStore(t)
	engine := New(store, nil, nil, hermeticConfig())
	op := forceReleaseTestOperation()
	first := requireForceReleaseDecision(t, engine, op, "", "pending", false)
	if err := engine.Approve(t.Context(), first.ApprovalID, "bob"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	loader := func(ctx context.Context) (ForceReleaseLease, error) {
		lease, err := forceReleaseTestLease(ctx)
		cancel()
		return lease, err
	}
	decision, err := engine.EvaluateForceRelease(ctx, "approval", op, "", loader)
	if !errors.Is(err, context.Canceled) || decision.Allowed {
		t.Fatalf("canceled observation admitted release: %+v, %v", decision, err)
	}
	record, _ := store.GetApproval(first.ApprovalID)
	if record.Status != state.ApprovalApproved {
		t.Fatalf("cancellation spent approval: %+v", record)
	}
}

func TestForceReleaseGateDoesNotReuseLegacyActorIndependentGrant(t *testing.T) {
	engine := New(setupTestStore(t), nil, nil, hermeticConfig())
	old, err := engine.Request(t.Context(), RequestParams{
		Action: "force_release", Resource: "reservation #17", RequestedBy: "alice",
		CorrelationID: "force_release:/test/project:legacy", RequiresSLB: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Approve(t.Context(), old.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	fresh := requireForceReleaseDecision(t, engine, forceReleaseTestOperation(), "", "pending", false)
	if fresh.ApprovalID == old.ID || !fresh.Created {
		t.Fatalf("legacy grant authorized a v2 operation: %+v", fresh)
	}
}
