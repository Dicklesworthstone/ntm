package rotation

import (
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"
)

// TestLifecycleFSMCrashPointModelChecker exhaustively injects deterministic
// failures at every transition checkpoint boundary across all 4 rotation and
// failover owners, plus a deterministic adversarial interleaved schedule,
// verifying:
//
//  1. Single-owner invariant: at most one active owner per pane at any instant.
//  2. Unambiguous crash recovery: every failure from an in-flight state lands in
//     StateFailedRecoverable with non-empty FailedFrom, LastCheckpoint, and
//     RecoveryHint, and supports both retry (Acquire -> draining) and operator
//     recovery (Abort -> idle).
//  3. Idempotent retries & monotonic lease fencing: replaying Acquire(owner,op)
//     or Transition(lease, curState) is idempotent, whereas any stale token
//     from a superseded lease is rejected with ErrLeaseFenced.
func TestLifecycleFSMCrashPointModelChecker(t *testing.T) {
	t.Parallel()

	owners := []struct {
		ownerID string
		kind    string
	}{
		{"context-rotator", "context_rotation"},
		{"coordinator-rotator", "coordinator_rotation"},
		{"caam-failover", "caam_failover"},
		{"auth-restart", "auth_restart"},
	}

	crashPoints := []struct {
		name       string
		advanceTo  []LifecycleState
		checkpoint string
		failedFrom LifecycleState
	}{
		{
			name:       "crash_during_draining",
			advanceTo:  nil,
			checkpoint: "drain_flush",
			failedFrom: StateDraining,
		},
		{
			name:       "crash_during_handing_off",
			advanceTo:  []LifecycleState{StateHandingOff},
			checkpoint: "handoff_spawn",
			failedFrom: StateHandingOff,
		},
		{
			name:       "crash_during_verifying",
			advanceTo:  []LifecycleState{StateHandingOff, StateVerifying},
			checkpoint: "verify_ready",
			failedFrom: StateVerifying,
		},
	}

	// Part 1: Exhaustive deterministic crash-point matrix (4 owners × 3 crash points × 2 recovery modes).
	for _, owner := range owners {
		for _, cp := range crashPoints {
			for _, recoverMode := range []string{"retry_to_completion", "operator_abort"} {
				name := fmt.Sprintf("%s/%s/%s", owner.kind, cp.name, recoverMode)
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					fsm := NewLifecycleCoordinator()
					pane := "%crash-" + name

					lease, err := fsm.Acquire(pane, owner.ownerID, "op-1", owner.kind)
					if err != nil {
						t.Fatalf("Acquire failed: %v", err)
					}

					// Idempotent re-acquire with same (ownerID, opID) returns identical token.
					reacquired, err := fsm.Acquire(pane, owner.ownerID, "op-1", owner.kind)
					if err != nil || reacquired.Token != lease.Token {
						t.Fatalf("idempotent re-acquire failed: lease=%+v err=%v", reacquired, err)
					}

					// Competing owner must be rejected while lease is active.
					if _, err := fsm.Acquire(pane, "rival-owner", "rival-op", "caam_failover"); !errors.Is(err, ErrLifecycleLeaseHeld) {
						t.Fatalf("rival acquire error = %v, want ErrLifecycleLeaseHeld", err)
					}

					for _, st := range cp.advanceTo {
						if err := fsm.Transition(lease, st, "advance"); err != nil {
							t.Fatalf("Transition(%s) failed: %v", st, err)
						}
						// Idempotent replay of current state must succeed.
						if err := fsm.Transition(lease, st, "replay"); err != nil {
							t.Fatalf("idempotent Transition(%s) failed: %v", st, err)
						}
					}

					// Inject crash at checkpoint.
					if err := fsm.FailRecoverable(lease, cp.checkpoint, "injected_crash", "retry or abort"); err != nil {
						t.Fatalf("FailRecoverable failed: %v", err)
					}
					// Idempotent FailRecoverable replay must succeed.
					if err := fsm.FailRecoverable(lease, cp.checkpoint, "injected_crash", "retry or abort"); err != nil {
						t.Fatalf("idempotent FailRecoverable failed: %v", err)
					}

					snap, ok := fsm.Snapshot(pane)
					if !ok {
						t.Fatal("missing snapshot after crash")
					}
					if snap.State != StateFailedRecoverable || snap.FailedFrom != cp.failedFrom || snap.LastCheckpoint != cp.checkpoint || snap.RecoveryHint == "" {
						t.Fatalf("ambiguous crash snapshot: %+v", snap)
					}
					if fsm.IsActive(pane) {
						t.Fatal("pane should not report IsActive in failed_recoverable")
					}
					// Complete must be rejected directly from failed_recoverable.
					if err := fsm.Complete(lease, "illegal_skip"); !errors.Is(err, ErrInvalidTransition) {
						t.Fatalf("Complete from failed_recoverable error = %v, want ErrInvalidTransition", err)
					}

					switch recoverMode {
					case "retry_to_completion":
						retryLease, err := fsm.Acquire(pane, owner.ownerID, "op-2", owner.kind)
						if err != nil {
							t.Fatalf("retry Acquire from failed_recoverable failed: %v", err)
						}
						if retryLease.Token <= lease.Token {
							t.Fatalf("expected strictly monotonic token: retry=%d prev=%d", retryLease.Token, lease.Token)
						}
						// Stale lease from crashed attempt MUST be fenced on every mutating call.
						if err := fsm.Transition(lease, StateHandingOff, "stale"); !errors.Is(err, ErrLeaseFenced) {
							t.Fatalf("stale Transition error = %v, want ErrLeaseFenced", err)
						}
						if err := fsm.FailRecoverable(lease, "stale", "stale", "stale"); !errors.Is(err, ErrLeaseFenced) {
							t.Fatalf("stale FailRecoverable error = %v, want ErrLeaseFenced", err)
						}
						if err := fsm.Complete(lease, "stale"); !errors.Is(err, ErrLeaseFenced) {
							t.Fatalf("stale Complete error = %v, want ErrLeaseFenced", err)
						}
						if err := fsm.Abort(lease, "stale"); !errors.Is(err, ErrLeaseFenced) {
							t.Fatalf("stale Abort error = %v, want ErrLeaseFenced", err)
						}

						if err := fsm.Transition(retryLease, StateHandingOff, "retry_handoff"); err != nil {
							t.Fatalf("retry handing_off failed: %v", err)
						}
						if err := fsm.Transition(retryLease, StateVerifying, "retry_verify"); err != nil {
							t.Fatalf("retry verifying failed: %v", err)
						}
						if err := fsm.Complete(retryLease, "recovered"); err != nil {
							t.Fatalf("retry Complete failed: %v", err)
						}
						// Idempotent Complete replay must succeed.
						if err := fsm.Complete(retryLease, "recovered_replay"); err != nil {
							t.Fatalf("idempotent Complete failed: %v", err)
						}

					case "operator_abort":
						if err := fsm.Abort(lease, "operator_reset"); err != nil {
							t.Fatalf("operator Abort from failed_recoverable failed: %v", err)
						}
						// Idempotent Abort replay must succeed.
						if err := fsm.Abort(lease, "operator_reset_replay"); err != nil {
							t.Fatalf("idempotent Abort failed: %v", err)
						}
					}

					finalSnap, ok := fsm.Snapshot(pane)
					if !ok || finalSnap.State != StateIdle || fsm.IsActive(pane) {
						t.Fatalf("expected clean idle state after recovery, got %+v", finalSnap)
					}
					// Every transition recorded in history must satisfy ValidTransition.
					for i, tr := range finalSnap.History {
						if !ValidTransition(tr.From, tr.To) {
							t.Fatalf("invalid transition recorded at history[%d]: %s -> %s", i, tr.From, tr.To)
						}
					}
				})
			}
		}
	}

	// Part 2: Deterministic pseudo-random multi-owner concurrent stress model check.
	t.Run("adversarial_multi_owner_schedule", func(t *testing.T) {
		t.Parallel()
		fsm := NewLifecycleCoordinator()
		const pane = "%adversarial-1"
		var activeCountMu sync.Mutex
		activeOwners := 0

		var wg sync.WaitGroup
		for worker := 0; worker < 4; worker++ {
			worker := worker
			wg.Add(1)
			go func() {
				defer wg.Done()
				rng := rand.New(rand.NewSource(int64(20261121 + worker)))
				o := owners[worker]
				for step := 0; step < 50; step++ {
					opID := fmt.Sprintf("op-%d-%d", worker, step)
					lease, err := fsm.Acquire(pane, o.ownerID, opID, o.kind)
					if err != nil {
						if !errors.Is(err, ErrLifecycleLeaseHeld) {
							t.Errorf("worker %d step %d unexpected Acquire error: %v", worker, step, err)
						}
						continue
					}

					// Check mutual exclusion invariant: while we hold an in-flight lease,
					// activeOwners MUST be strictly 1.
					activeCountMu.Lock()
					activeOwners++
					if activeOwners != 1 {
						t.Errorf("single-owner invariant violated: activeOwners=%d", activeOwners)
					}
					activeCountMu.Unlock()

					// Check mutual exclusion invariant: while we hold an in-flight lease,
					// activeOwners MUST be strictly 1. Decrement immediately BEFORE
					// releasing the lease (Abort/FailRecoverable/Complete) so the next
					// waiter that acquires the freed lease cannot observe our stale count.
					releaseCounter := func() {
						activeCountMu.Lock()
						if activeOwners != 1 {
							t.Errorf("single-owner invariant violated before release: activeOwners=%d", activeOwners)
						}
						activeOwners--
						activeCountMu.Unlock()
					}
					action := rng.Intn(4)
					var endErr error
					switch action {
					case 0: // abort during draining
						releaseCounter()
						endErr = fsm.Abort(lease, "abort_drain")
					case 1: // fail during draining
						releaseCounter()
						endErr = fsm.FailRecoverable(lease, "drain", "fail_drain", "retry")
					case 2: // fail during handing_off
						if err := fsm.Transition(lease, StateHandingOff, "handoff"); err != nil {
							releaseCounter()
							endErr = err
						} else {
							releaseCounter()
							endErr = fsm.FailRecoverable(lease, "handoff", "fail_handoff", "retry")
						}
					case 3: // complete full lifecycle
						if err := fsm.Transition(lease, StateHandingOff, "handoff"); err != nil {
							releaseCounter()
							endErr = err
						} else if err := fsm.Transition(lease, StateVerifying, "verify"); err != nil {
							releaseCounter()
							endErr = err
						} else {
							releaseCounter()
							endErr = fsm.Complete(lease, "done")
						}
					}
					if endErr != nil {
						t.Errorf("worker %d step %d action %d error: %v", worker, step, action, endErr)
					}
				}
			}()
		}
		wg.Wait()

		snap, ok := fsm.Snapshot(pane)
		if !ok {
			t.Fatal("expected snapshot after adversarial schedule")
		}
		if snap.State != StateIdle && snap.State != StateFailedRecoverable {
			t.Fatalf("unexpected final state %s after all workers exited", snap.State)
		}
		for i, tr := range snap.History {
			if !ValidTransition(tr.From, tr.To) {
				t.Fatalf("illegal transition in concurrent history[%d]: %s -> %s", i, tr.From, tr.To)
			}
		}
	})
}
