package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// newActivityCollisionTestSession builds a two-window session where each
// window's sole pane is index 1 — the exact topology gs-5wlp reports:
// `ntm activity` prints two rows both labelled "pane 1" with no way to tell
// them apart.
func newActivityCollisionTestSession(t *testing.T) string {
	t.Helper()
	testutil.RequireTmuxThrottled(t)

	session := fmt.Sprintf("ntm_test_activity_collision_%d", time.Now().UnixNano())
	dir := t.TempDir()
	if err := tmux.CreateSession(session, dir); err != nil {
		t.Fatalf("create test session: %v", err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(session) })

	initial, err := tmux.GetPanes(session)
	if err != nil || len(initial) != 1 {
		t.Fatalf("initial panes = %d, err = %v; want one pane", len(initial), err)
	}
	if _, err := tmux.DefaultClient.Run("select-pane", "-t", initial[0].ID, "-T", session+"__cc_1"); err != nil {
		t.Fatalf("title window1 pane1: %v", err)
	}

	if _, err := tmux.DefaultClient.Run("new-window", "-d", "-t", session, "-c", dir); err != nil {
		t.Fatalf("create second window: %v", err)
	}

	afterWindow, err := tmux.GetPanes(session)
	if err != nil {
		t.Fatalf("get panes after new window: %v", err)
	}
	var secondWindowPaneID string
	for _, p := range afterWindow {
		if p.WindowIndex != initial[0].WindowIndex {
			secondWindowPaneID = p.ID
			break
		}
	}
	if secondWindowPaneID == "" {
		t.Fatalf("second window pane not found in %+v", afterWindow)
	}
	if _, err := tmux.DefaultClient.Run("select-pane", "-t", secondWindowPaneID, "-T", session+"__cc_2"); err != nil {
		t.Fatalf("title window2 pane1: %v", err)
	}

	// Let tmux settle before the panes are captured for state classification.
	time.Sleep(150 * time.Millisecond)
	return session
}

// TestCollectActivityData_DisambiguatesCollidingPaneIndexAcrossWindows is the
// hostile collision regression for gs-5wlp: two panes that share the same
// per-window index (both "pane 1" of their own window) must still resolve to
// two distinct, identifiable agent rows in both the in-memory result and the
// --json envelope.
func TestCollectActivityData_DisambiguatesCollidingPaneIndexAcrossWindows(t *testing.T) {
	session := newActivityCollisionTestSession(t)

	panes, err := tmux.GetPanes(session)
	if err != nil {
		t.Fatalf("get panes: %v", err)
	}
	if len(panes) != 2 {
		t.Fatalf("panes = %d, want 2: %+v", len(panes), panes)
	}

	// Hostile precondition: confirm the collision this test exists to catch is
	// actually present before asserting anything about the fix.
	if panes[0].Index != panes[1].Index {
		t.Fatalf("test precondition failed: panes do not share an index (%d vs %d) — cannot exercise the collision", panes[0].Index, panes[1].Index)
	}
	if panes[0].WindowIndex == panes[1].WindowIndex {
		t.Fatalf("test precondition failed: panes are in the same window")
	}
	if panes[0].ID == "" || panes[1].ID == "" || panes[0].ID == panes[1].ID {
		t.Fatalf("test precondition failed: pane IDs not distinct (%q vs %q)", panes[0].ID, panes[1].ID)
	}

	result, err := collectActivityData(session, activityOptions{})
	if err != nil {
		t.Fatalf("collectActivityData: %v", err)
	}
	if len(result.Agents) != 2 {
		t.Fatalf("agents = %d, want 2: %+v", len(result.Agents), result.Agents)
	}

	seenPaneIDs := map[string]bool{}
	seenWindows := map[int]bool{}
	for _, a := range result.Agents {
		if a.PaneID == "" {
			t.Fatalf("agent %+v missing PaneID — cannot disambiguate the colliding rows", a)
		}
		if seenPaneIDs[a.PaneID] {
			t.Fatalf("duplicate PaneID %s across agents: %+v", a.PaneID, result.Agents)
		}
		seenPaneIDs[a.PaneID] = true
		seenWindows[a.WindowIndex] = true
	}
	if len(seenPaneIDs) != 2 {
		t.Fatalf("expected 2 distinct pane IDs, got %v", seenPaneIDs)
	}
	if len(seenWindows) != 2 {
		t.Fatalf("expected agents to retain 2 distinct WindowIndex values, got %v: %+v", seenWindows, result.Agents)
	}

	// The JSON envelope --- the actual public contract automation reads --- must
	// carry the same disambiguating fields, not just the in-memory struct.
	envelope := buildActivityJSONEnvelope(result)
	if len(envelope.Agents) != 2 {
		t.Fatalf("envelope agents = %d, want 2: %+v", len(envelope.Agents), envelope.Agents)
	}
	jsonSeenPaneIDs := map[string]bool{}
	for _, a := range envelope.Agents {
		if a.PaneID == "" {
			t.Fatalf("envelope agent %+v missing pane_id", a)
		}
		if jsonSeenPaneIDs[a.PaneID] {
			t.Fatalf("duplicate pane_id in JSON envelope: %+v", envelope.Agents)
		}
		jsonSeenPaneIDs[a.PaneID] = true
	}

	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	if !strings.Contains(string(data), `"pane_id"`) || !strings.Contains(string(data), `"window_index"`) {
		t.Fatalf("JSON envelope missing disambiguating fields: %s", data)
	}
}
