//go:build e2e
// +build e2e

package e2e

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// TestNoOrphanTmuxSessionSurvivesCompletedRun pins AC2 for the normal
// completion path: the harness registers a kill-session cleanup and Close runs
// it on both the pass and the fail path, so no ntm-e2e-* tmux session may
// outlive a completed e2e run. The mid-run-kill case (a SIGKILLed binary that
// runs no cleanup) is covered separately by the TestMain startup reaper and its
// unit test testutil.TestReapStaleTmuxTestServersRemovesLeakedServerButSparesFresh.
func TestNoOrphanTmuxSessionSurvivesCompletedRun(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	h, err := NewScenarioHarness(t, HarnessOptions{Scenario: "tmux-teardown", Retain: RetainNever})
	if err != nil {
		t.Fatalf("new scenario harness: %v", err)
	}
	if err := h.SetupTmuxSession(TmuxSessionOptions{}); err != nil {
		t.Fatalf("setup tmux session: %v", err)
	}

	session := h.SessionName()
	if !testutil.SessionExists(session) {
		t.Fatalf("precondition: session %q should exist after setup", session)
	}

	// Close is idempotent (guarded by closeOnce) and also runs via tb.Cleanup;
	// calling it here lets the assertion observe the completed-run teardown.
	h.Close()

	if testutil.SessionExists(session) {
		t.Errorf("ntm-e2e session %q survived a completed run", session)
	}
}
