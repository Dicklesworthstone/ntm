package coordinator

import (
	"context"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/rotation"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// TestCoordinatorRotationAndFailoverShareLifecycleFSMFence verifies live
// integration between rotation.DefaultLifecycleCoordinator(), rotationChecker,
// and failoverChecker: while one owner holds an active FSM lease on a pane,
// the other coordinator engine declines cleanly with "lifecycle_lease_held"
// without double-mutating the pane, and resumes cleanly after lease release.
func TestCoordinatorRotationAndFailoverShareLifecycleFSMFence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	redirectPendingStore(t)

	const paneID = "%fsm-fence-901"
	workDir := "/tmp/fsm-fence-proj"
	seedClaudeTranscript(t, home, workDir, claudeUsageLine("claude-sonnet-4-6", 170000, 0, 0, 0))

	panes := []tmux.Pane{{
		ID:    paneID,
		Index: 1,
		Title: "fsmsess__cc_1",
		Type:  tmux.AgentClaude,
		Width: 120,
	}}
	rotEnv := newRotationTestEnv(t, 80.0, true, panes, map[string]string{paneID: workDir}, map[string]string{paneID: failoverIdleCapture}, nil)
	foEnv := newFailoverTestEnv(t, []string{"claude"}, 30, panes, map[string]string{paneID: limitedCapture})

	fsm := rotation.DefaultLifecycleCoordinator()

	// 1. Hold an active context_rotation lease on paneID.
	lease, err := fsm.Acquire(paneID, "context-rotator", "op-fence-1", "context_rotation")
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	t.Cleanup(func() { _ = fsm.Abort(lease, "test_cleanup") })

	// 2. Both rotationChecker and failoverChecker MUST decline with lifecycle_lease_held
	//    and perform ZERO confirmations or account switches.
	rotDecisions := rotEnv.rc.runOnce(context.Background())
	if len(rotDecisions) != 1 || rotDecisions[0].Action != "skipped" || rotDecisions[0].SkipReason != "lifecycle_lease_held" {
		t.Fatalf("rotationChecker should skip with lifecycle_lease_held, got %+v", rotDecisions)
	}
	if len(rotEnv.confirmed) != 0 {
		t.Fatalf("rotationChecker bypassed active FSM lease: confirmed=%v decisions=%+v", rotEnv.confirmed, rotDecisions)
	}

	decisions := foEnv.fc.runOnce(context.Background())
	if len(decisions) != 1 || decisions[0].Action != "declined" || decisions[0].DeclineReason != "lifecycle_lease_held" {
		t.Fatalf("failoverChecker should decline with lifecycle_lease_held, got %+v", decisions)
	}
	if len(foEnv.switched) != 0 {
		t.Fatalf("failoverChecker mutated account while FSM lease held: switched=%v", foEnv.switched)
	}

	// 3. Inject crash -> failed_recoverable -> verify failoverChecker CAN recover
	//    the pane via failed_recoverable -> draining without split-brain.
	if err := fsm.FailRecoverable(lease, "handoff", "simulated_crash", "retry failover"); err != nil {
		t.Fatalf("FailRecoverable failed: %v", err)
	}
	afterCrash := foEnv.fc.runOnce(context.Background())
	if len(afterCrash) != 1 {
		t.Fatalf("expected 1 failover decision after crash, got %+v", afterCrash)
	}
	if afterCrash[0].DeclineReason == "lifecycle_lease_held" {
		t.Fatalf("failoverChecker stayed deadlocked on failed_recoverable pane: %+v", afterCrash[0])
	}
	// After failoverChecker exits, the pane must never be left stranded in-flight.
	if fsm.IsActive(paneID) {
		snap, _ := fsm.Snapshot(paneID)
		t.Fatalf("pane left active after failoverChecker returned: %+v", snap)
	}

	// 4. Once the pane is no longer held in-flight, rotationChecker resumes cleanly.
	afterRelease := rotEnv.rc.runOnce(context.Background())
	if len(afterRelease) != 1 || afterRelease[0].Action != "auto_confirmed" {
		t.Fatalf("rotationChecker should resume and auto-confirm after lease release, got %+v", afterRelease)
	}
	if len(rotEnv.confirmed) != 1 {
		t.Fatalf("expected 1 confirmed rotation after lease release, got %v", rotEnv.confirmed)
	}
}
