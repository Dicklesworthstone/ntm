package cli

import (
	"os"
	"testing"
)

// TestDetermineAgentStateOnRealIdleCaptures feeds determineAgentState the exact
// 20-line scrollback the dispatch path captures (LinesStatusDetection) from real
// idle Claude panes (2026-06-14 live swarm). Each pane has finished its turn —
// a completed spinner ("✶ Pontificating… (16m 20s)" / "✻ Baked for 16m 20s") is
// still in the window, with the idle "new task? /clear to save …" prompt below
// it. The correct verdict is "idle": the agent is waiting for work. Before the
// fix, IsLiveBusy matched the stale spinner and returned "working", so
// autonomous dispatch saw 0 idle agents and the swarm stalled.
func TestDetermineAgentStateOnRealIdleCaptures(t *testing.T) {
	for _, p := range []string{"2", "3", "4", "5"} {
		data, err := os.ReadFile("/tmp/ntm-fix-disp" + p + ".txt")
		if err != nil {
			t.Skipf("no capture for pane %s: %v", p, err)
		}
		got := determineAgentState(string(data), "claude")
		if got != "idle" {
			t.Errorf("pane %s: determineAgentState = %q, want \"idle\" (stale completed spinner above a fresh idle prompt must not read as busy)", p, got)
		}
	}
}
