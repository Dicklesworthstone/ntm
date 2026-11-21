package rotation

import (
	"errors"
	"sync"
	"testing"
)

func TestLifecycleFSMValidTransitionsAndOwnerFencing(t *testing.T) {
	t.Parallel()
	coord := NewLifecycleCoordinator()

	// Happy path: idle -> draining -> handing_off -> verifying -> active -> idle
	lease, err := coord.Acquire("%10", "context-rotator", "op-1", "context_rotation")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if !coord.IsActive("%10") {
		t.Fatal("expected pane %10 to be active after Acquire")
	}

	// Competing owner (e.g. CAAM failover) must be fenced out cleanly.
	if _, err := coord.Acquire("%10", "caam-failover", "op-2", "caam_failover"); !errors.Is(err, ErrLifecycleLeaseHeld) {
		t.Fatalf("competing Acquire err = %v, want ErrLifecycleLeaseHeld", err)
	}

	// Same owner + same opID re-acquire is idempotent.
	sameLease, err := coord.Acquire("%10", "context-rotator", "op-1", "context_rotation")
	if err != nil || sameLease.Token != lease.Token {
		t.Fatalf("idempotent Acquire = (%+v, %v), want token %d", sameLease, err, lease.Token)
	}

	// Disallowed jump (draining -> active) fails closed.
	if err := coord.Transition(lease, StateActive, "skip"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("invalid transition err = %v, want ErrInvalidTransition", err)
	}

	if err := coord.Transition(lease, StateHandingOff, "handoff_started"); err != nil {
		t.Fatalf("Transition(handing_off): %v", err)
	}
	// Idempotent same-state transition succeeds.
	if err := coord.Transition(lease, StateHandingOff, "handoff_retry"); err != nil {
		t.Fatalf("idempotent Transition(handing_off): %v", err)
	}
	// Cannot abort once past draining into handing_off (must fail_recoverable).
	if err := coord.Abort(lease, "too_late"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Abort from handing_off err = %v, want ErrInvalidTransition", err)
	}
	if err := coord.Transition(lease, StateVerifying, "verify_started"); err != nil {
		t.Fatalf("Transition(verifying): %v", err)
	}
	if err := coord.Complete(lease, "verified"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if coord.IsActive("%10") {
		t.Fatal("pane %10 still active after Complete")
	}
	snap, ok := coord.Snapshot("%10")
	if !ok || snap.State != StateIdle {
		t.Fatalf("final Snapshot = (%+v, %v), want StateIdle", snap, ok)
	}
}

func TestLifecycleFSMCrashRecoveryAndStaleTokenFencing(t *testing.T) {
	t.Parallel()
	coord := NewLifecycleCoordinator()

	// Crash during handing_off -> failed_recoverable retains unambiguous recovery state.
	lease1, err := coord.Acquire("%20", "caam-failover", "op-crash-1", "caam_failover")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := coord.Transition(lease1, StateHandingOff, "activate_account"); err != nil {
		t.Fatalf("Transition(handing_off): %v", err)
	}
	if err := coord.FailRecoverable(lease1, "account_activated_unverified", "restart timeout", "inspect pane %20 and retry failover"); err != nil {
		t.Fatalf("FailRecoverable: %v", err)
	}

	snap, ok := coord.Snapshot("%20")
	if !ok || snap.State != StateFailedRecoverable || snap.FailedFrom != StateHandingOff || snap.LastCheckpoint != "account_activated_unverified" {
		t.Fatalf("unexpected failed_recoverable snapshot: %+v", snap)
	}

	// Retry/takeover from failed_recoverable succeeds with a new fenced token.
	lease2, err := coord.Acquire("%20", "caam-failover", "op-retry-2", "caam_failover")
	if err != nil {
		t.Fatalf("retry Acquire from failed_recoverable: %v", err)
	}
	if lease2.Token == lease1.Token {
		t.Fatalf("retry reused stale token %d", lease2.Token)
	}

	// Old crashed owner token is permanently fenced out!
	if err := coord.Transition(lease1, StateVerifying, "zombie_continue"); !errors.Is(err, ErrLeaseFenced) {
		t.Fatalf("stale lease Transition err = %v, want ErrLeaseFenced", err)
	}

	if err := coord.Abort(lease2, "operator_cancelled"); err != nil {
		t.Fatalf("Abort from draining: %v", err)
	}
	if coord.IsActive("%20") {
		t.Fatal("expected pane %20 idle after Abort")
	}
}

func TestLifecycleFSMConcurrentAcquireSingleOwnerInvariant(t *testing.T) {
	t.Parallel()
	coord := NewLifecycleCoordinator()

	const contenders = 16
	var wg sync.WaitGroup
	winners := make(chan *LifecycleLease, contenders)

	for i := 0; i < contenders; i++ {
		id := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, err := coord.Acquire("%99", "worker", string(rune('A'+id)), "context_rotation")
			if err == nil {
				winners <- lease
			}
		}()
	}
	wg.Wait()
	close(winners)

	count := 0
	for range winners {
		count++
	}
	if count != 1 {
		t.Fatalf("concurrent Acquire admitted %d owners for pane %%99, want exactly 1", count)
	}
}

func TestLifecycleFSMPreventsReentryCrossReleaseAndEmptyOpCollision(t *testing.T) {
	t.Parallel()
	coord := NewLifecycleCoordinator()

	// Empty opID must mint unique monotonic IDs so concurrent calls from the
	// same ownerID do not collide as idempotent re-acquires.
	lease1, err := coord.Acquire("%50", "caam-failover", "", "caam_failover")
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	if _, err := coord.Acquire("%50", "caam-failover", "", "caam_failover"); !errors.Is(err, ErrLifecycleLeaseHeld) {
		t.Fatalf("second empty-op Acquire err = %v, want ErrLifecycleLeaseHeld", err)
	}

	// Transition cannot bypass Abort to reach StateIdle.
	if err := coord.Transition(lease1, StateIdle, "bypass"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Transition(StateIdle) err = %v, want ErrInvalidTransition", err)
	}

	// Abort releases the pane; subsequent Complete must NOT silently succeed,
	// and Transition(StateDraining) must NOT re-enter an active state.
	if err := coord.Abort(lease1, "cancelled"); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if err := coord.Complete(lease1, "after_abort"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Complete after Abort err = %v, want ErrInvalidTransition", err)
	}
	if err := coord.Transition(lease1, StateDraining, "reenter"); !errors.Is(err, ErrLeaseFenced) {
		t.Fatalf("Transition(StateDraining) after Abort err = %v, want ErrLeaseFenced", err)
	}

	// Full completion: subsequent Abort must NOT silently succeed, and
	// Transition(StateDraining) must NOT re-enter an active state.
	lease2, err := coord.Acquire("%50", "auth-restart", "", "auth_restart")
	if err != nil {
		t.Fatalf("Acquire lease2: %v", err)
	}
	if err := coord.Transition(lease2, StateHandingOff, "handoff"); err != nil {
		t.Fatalf("Transition(handing_off): %v", err)
	}
	if err := coord.Transition(lease2, StateVerifying, "verify"); err != nil {
		t.Fatalf("Transition(verifying): %v", err)
	}
	// Transition cannot bypass Complete/FailRecoverable to reach StateActive or StateFailedRecoverable.
	if err := coord.Transition(lease2, StateActive, "bypass_complete"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Transition(StateActive) err = %v, want ErrInvalidTransition", err)
	}
	if err := coord.Transition(lease2, StateFailedRecoverable, "bypass_fail"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Transition(StateFailedRecoverable) err = %v, want ErrInvalidTransition", err)
	}
	if err := coord.Complete(lease2, "done"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := coord.Abort(lease2, "after_complete"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Abort after Complete err = %v, want ErrInvalidTransition", err)
	}
	if err := coord.Transition(lease2, StateDraining, "reenter_after_complete"); !errors.Is(err, ErrLeaseFenced) {
		t.Fatalf("Transition(StateDraining) after Complete err = %v, want ErrLeaseFenced", err)
	}

	// Failed lease cannot re-enter StateDraining via Transition without re-acquiring.
	lease3, err := coord.Acquire("%50", "context-rotator", "", "context_rotation")
	if err != nil {
		t.Fatalf("Acquire lease3: %v", err)
	}
	if err := coord.FailRecoverable(lease3, "drain", "boom", "retry"); err != nil {
		t.Fatalf("FailRecoverable: %v", err)
	}
	if err := coord.Transition(lease3, StateDraining, "reenter_from_failed"); !errors.Is(err, ErrLeaseFenced) {
		t.Fatalf("Transition(StateDraining) from failed_recoverable err = %v, want ErrLeaseFenced", err)
	}
	if err := coord.Abort(lease3, "reset"); err != nil {
		t.Fatalf("Abort from failed_recoverable: %v", err)
	}
	abortedSnap, ok := coord.Snapshot("%50")
	if !ok || abortedSnap.FailedFrom != "" || abortedSnap.RecoveryHint != "" {
		t.Fatalf("Abort must clear stale FailedFrom and RecoveryHint, got FailedFrom=%q RecoveryHint=%q", abortedSnap.FailedFrom, abortedSnap.RecoveryHint)
	}

	// History length stays bounded across many cycles.
	for i := 0; i < maxLifecycleHistory; i++ {
		l, err := coord.Acquire("%50", "worker", "", "context_rotation")
		if err != nil {
			t.Fatalf("cycle %d Acquire: %v", i, err)
		}
		if err := coord.Abort(l, "cycle"); err != nil {
			t.Fatalf("cycle %d Abort: %v", i, err)
		}
	}
	snap, ok := coord.Snapshot("%50")
	if !ok || len(snap.History) > maxLifecycleHistory {
		t.Fatalf("history exceeded bound: len=%d max=%d", len(snap.History), maxLifecycleHistory)
	}
}
