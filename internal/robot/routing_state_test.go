package robot

import (
	"math/rand"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

// routingStateTestStore opens a real (temp-file) state DB with migrations
// applied, matching what openRoutingStateStore does in production.
func routingStateTestStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state store: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func routingStateTestAgents() []ScoredAgent {
	return []ScoredAgent{
		{PaneID: "%1", PaneIndex: 1, AgentType: "claude", Score: 50},
		{PaneID: "%2", PaneIndex: 2, AgentType: "codex", Score: 50},
		{PaneID: "%3", PaneIndex: 3, AgentType: "gemini", Score: 50},
	}
}

// TestRandomStrategy_SeededSourceHitsMultipleAgents pins the B7 fix
// (bd-ws1-truth-safety-l5ddi.10): random routing with a seeded source must hit
// >=2 distinct agents over 20 draws of 3 — the old nil-randFunc behavior
// always returned the middle agent.
func TestRandomStrategy_SeededSourceHitsMultipleAgents(t *testing.T) {
	src := rand.New(rand.NewSource(42))
	strat := &RandomStrategy{randFunc: src.Intn}
	agents := routingStateTestAgents()

	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		selected := strat.Select(agents, RoutingContext{ExplicitPane: -1})
		if selected == nil {
			t.Fatal("random strategy returned nil with 3 available agents")
		}
		seen[selected.PaneID] = true
	}
	if len(seen) < 2 {
		t.Fatalf("seeded random hit only %d distinct agents over 20 draws of 3: %v", len(seen), seen)
	}
}

// TestRandomStrategy_DefaultIsReallyRandom asserts the production default
// (nil randFunc) no longer degenerates to the deterministic middle agent.
// 60 draws of 3 all landing on one agent has probability ~2e-28.
func TestRandomStrategy_DefaultIsReallyRandom(t *testing.T) {
	strat := &RandomStrategy{}
	agents := routingStateTestAgents()

	seen := map[string]bool{}
	for i := 0; i < 60; i++ {
		selected := strat.Select(agents, RoutingContext{ExplicitPane: -1})
		if selected == nil {
			t.Fatal("random strategy returned nil with 3 available agents")
		}
		seen[selected.PaneID] = true
	}
	if len(seen) < 2 {
		t.Fatalf("default random hit only %d distinct agents over 60 draws of 3 (deterministic bug is back): %v", len(seen), seen)
	}
}

// TestRoundRobin_PersistedRotationAcrossInvocations is the tightened B7 proof:
// >=3 panes, >=4 sequential sends THROUGH FRESH ROUTERS (each call simulates a
// separate CLI invocation; only the state DB is shared), asserting the EXACT
// rotation sequence including wrap-around (p1->p2->p3->p1) AND that the
// persisted rotation cursor advances per send. Degenerate least-loaded cannot
// reproduce an exact modular order over 4 draws.
func TestRoundRobin_PersistedRotationAcrossInvocations(t *testing.T) {
	store := routingStateTestStore(t)
	opts := RouteOptions{Session: "rrproj", Strategy: StrategyRoundRobin}

	wantSequence := []string{"%1", "%2", "%3", "%1"}
	wantCursors := []int{0, 1, 2, 0}
	for send := 0; send < 4; send++ {
		agents := routingStateTestAgents() // fresh slice per invocation
		result := routeWithSessionState(agents, opts, store, true)
		if result.Selected == nil {
			t.Fatalf("send %d: no selection", send+1)
		}
		t.Logf("send %d: strategy=%s selected pane_id=%s pane_index=%d",
			send+1, opts.Strategy, result.Selected.PaneID, result.Selected.PaneIndex)
		if result.Selected.PaneID != wantSequence[send] {
			t.Fatalf("send %d selected %s, want %s (exact rotation %v)",
				send+1, result.Selected.PaneID, wantSequence[send], wantSequence)
		}

		// The MECHANISM, not just the symptom: the persisted cursor advanced.
		rs, err := store.GetRoutingState("rrproj")
		if err != nil {
			t.Fatalf("send %d: load routing state: %v", send+1, err)
		}
		if rs == nil {
			t.Fatalf("send %d: routing state not persisted", send+1)
		}
		t.Logf("send %d: persisted last_agent=%s rotation_cursor=%d", send+1, rs.LastAgent, rs.RotationCursor)
		if rs.RotationCursor != wantCursors[send] {
			t.Fatalf("send %d persisted cursor = %d, want %d", send+1, rs.RotationCursor, wantCursors[send])
		}
		if rs.LastAgent != wantSequence[send] {
			t.Fatalf("send %d persisted last_agent = %s, want %s", send+1, rs.LastAgent, wantSequence[send])
		}
	}
}

// TestRoundRobin_CursorAnchorsWhenPaneIDsChange pins the persisted-cursor
// anchor: when the previously routed pane ID no longer resolves (e.g. panes
// were recreated), the rotation continues from the persisted cursor instead of
// falling back to activity heuristics.
func TestRoundRobin_CursorAnchorsWhenPaneIDsChange(t *testing.T) {
	store := routingStateTestStore(t)
	if err := store.SaveRoutingState(&state.RoutingState{
		SessionName: "rrproj2", LastAgent: "%gone", RotationCursor: 2,
	}); err != nil {
		t.Fatalf("seed routing state: %v", err)
	}

	result := routeWithSessionState(routingStateTestAgents(),
		RouteOptions{Session: "rrproj2", Strategy: StrategyRoundRobin}, store, true)
	if result.Selected == nil {
		t.Fatal("no selection")
	}
	// Cursor 2 anchors at index 2; the next rotation step wraps to index 0.
	if result.Selected.PaneID != "%1" {
		t.Fatalf("selected %s, want %%1 (cursor 2 -> wrap to index 0)", result.Selected.PaneID)
	}
}

// TestSticky_PersistedAcrossInvocations is the sticky discriminator case: two
// consecutive sticky sends land on the SAME pane even when that pane has
// become the WORST-scored (least-loaded would move away from it, so a
// degenerate implementation fails this).
func TestSticky_PersistedAcrossInvocations(t *testing.T) {
	store := routingStateTestStore(t)
	opts := RouteOptions{Session: "stickyproj", Strategy: StrategySticky}

	// Send 1: no history -> sticky falls back to least-loaded -> %2 (best score).
	first := routingStateTestAgents()
	first[1].Score = 90
	result := routeWithSessionState(first, opts, store, true)
	if result.Selected == nil || result.Selected.PaneID != "%2" {
		t.Fatalf("send 1 selected %+v, want fallback to best-scored %%2", result.Selected)
	}
	rs, err := store.GetRoutingState("stickyproj")
	if err != nil || rs == nil || rs.LastAgent != "%2" {
		t.Fatalf("send 1 persisted state = %+v (err %v), want last_agent %%2", rs, err)
	}
	t.Logf("send 1: selected %s, persisted last_agent=%s", result.Selected.PaneID, rs.LastAgent)

	// Send 2: %2 is now the busiest/worst-scored agent. Least-loaded would
	// move AWAY from it; sticky must return to it.
	second := routingStateTestAgents()
	second[1].Score = 5
	second[0].Score = 95
	result = routeWithSessionState(second, opts, store, true)
	if result.Selected == nil || result.Selected.PaneID != "%2" {
		t.Fatalf("send 2 selected %+v, want sticky %%2 despite worst score", result.Selected)
	}
	t.Logf("send 2: selected %s (sticky held despite worst score)", result.Selected.PaneID)
}

// TestRoutingState_ExplicitLastAgentWinsOverPersisted asserts an explicit
// --last-agent still takes precedence over persisted state.
func TestRoutingState_ExplicitLastAgentWinsOverPersisted(t *testing.T) {
	store := routingStateTestStore(t)
	if err := store.SaveRoutingState(&state.RoutingState{
		SessionName: "explicitproj", LastAgent: "%1", RotationCursor: 0,
	}); err != nil {
		t.Fatalf("seed routing state: %v", err)
	}

	result := routeWithSessionState(routingStateTestAgents(),
		RouteOptions{Session: "explicitproj", Strategy: StrategySticky, LastAgent: "%3"}, store, true)
	if result.Selected == nil || result.Selected.PaneID != "%3" {
		t.Fatalf("selected %+v, want explicit last-agent %%3", result.Selected)
	}
}
