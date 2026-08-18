package robot

import (
	"fmt"
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
		rs, err := store.GetRoutingState("rrproj", "")
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
// anchor under the bd-88um4 successor semantics: when the previously routed
// pane ID no longer resolves, the list shrank at/before the cursor, so the
// vanished pane's SUCCESSOR now sits AT the cursor position — the rotation
// starts there WITHOUT advancing (advancing skipped the successor).
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
	// %gone held index 2; whatever occupies index 2 now is its successor.
	if result.Selected.PaneID != "%3" {
		t.Fatalf("selected %s, want %%3 (vanished pane's successor AT cursor 2)", result.Selected.PaneID)
	}
}

// TestRoundRobin_VanishedPaneDoesNotSkipSuccessor is the bd-88um4 canonical
// off-by-one case, driven end-to-end through real persisted sends: with panes
// A,B,C,D, route to A then B, then kill B. The next send must pick C — the
// old code anchored at B's stale index and advanced +1 in the SHRUNK list,
// selecting D and starving C.
func TestRoundRobin_VanishedPaneDoesNotSkipSuccessor(t *testing.T) {
	store := routingStateTestStore(t)
	opts := RouteOptions{Session: "rrvanish", Strategy: StrategyRoundRobin}
	full := []ScoredAgent{
		{PaneID: "%A", PaneIndex: 1, AgentType: "claude", Score: 50},
		{PaneID: "%B", PaneIndex: 2, AgentType: "claude", Score: 50},
		{PaneID: "%C", PaneIndex: 3, AgentType: "claude", Score: 50},
		{PaneID: "%D", PaneIndex: 4, AgentType: "claude", Score: 50},
	}

	for send, want := range []string{"%A", "%B"} {
		agents := append([]ScoredAgent(nil), full...)
		result := routeWithSessionState(agents, opts, store, true)
		if result.Selected == nil || result.Selected.PaneID != want {
			t.Fatalf("send %d selected %+v, want %s", send+1, result.Selected, want)
		}
		t.Logf("send %d: selected %s", send+1, result.Selected.PaneID)
	}

	// Kill B: the candidate list shrinks to A,C,D.
	shrunk := []ScoredAgent{full[0], full[2], full[3]}
	result := routeWithSessionState(shrunk, opts, store, true)
	if result.Selected == nil {
		t.Fatal("no selection after pane removal")
	}
	t.Logf("send 3 (B killed): selected %s", result.Selected.PaneID)
	if result.Selected.PaneID != "%C" {
		t.Fatalf("selected %s after killing %%B, want %%C (old bug skipped to %%D)", result.Selected.PaneID)
	}

	// And the rotation keeps going correctly: C resolves by pane ID -> D.
	result = routeWithSessionState(append([]ScoredAgent(nil), shrunk...), opts, store, true)
	if result.Selected == nil || result.Selected.PaneID != "%D" {
		t.Fatalf("send 4 selected %+v, want %%D", result.Selected)
	}
}

// TestRoundRobin_CursorRobustnessTopologies tables the anchor behavior across
// pane-set mutations (bd-88um4): removals anchor on the successor, insertions
// cannot shift a pane-ID anchor, and a cursor past the shrunk end wraps to
// the head (which IS the vanished tail's successor).
func TestRoundRobin_CursorRobustnessTopologies(t *testing.T) {
	mk := func(ids ...string) []ScoredAgent {
		agents := make([]ScoredAgent, len(ids))
		for i, id := range ids {
			agents[i] = ScoredAgent{PaneID: id, PaneIndex: i + 1, AgentType: "claude", Score: 50}
		}
		return agents
	}

	cases := []struct {
		name       string
		lastAgent  string
		cursor     int
		agents     []ScoredAgent
		want       string
		wantCursor int // persisted cursor after the send
	}{
		{
			name:      "vanished middle pane anchors on successor",
			lastAgent: "%B", cursor: 1,
			agents: mk("%A", "%C", "%D"),
			want:   "%C", wantCursor: 1,
		},
		{
			name:      "vanished tail pane wraps to head",
			lastAgent: "%D", cursor: 3,
			agents: mk("%A", "%B", "%C"),
			want:   "%A", wantCursor: 0,
		},
		{
			name:      "insertion before last agent cannot shift the anchor",
			lastAgent: "%B", cursor: 1,
			agents: mk("%NEW", "%A", "%B", "%C"),
			want:   "%C", wantCursor: 3,
		},
		{
			name:      "surviving last agent ignores stale cursor entirely",
			lastAgent: "%C", cursor: 0, // cursor lies; pane ID is authoritative
			agents: mk("%A", "%B", "%C"),
			want:   "%A", wantCursor: 0,
		},
		{
			name:      "mass shrink clamps cursor to head",
			lastAgent: "%F", cursor: 5,
			agents: mk("%A"),
			want:   "%A", wantCursor: 0,
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := routingStateTestStore(t)
			session := fmt.Sprintf("topo%d", i)
			if err := store.SaveRoutingState(&state.RoutingState{
				SessionName: session, LastAgent: tc.lastAgent, RotationCursor: tc.cursor,
			}); err != nil {
				t.Fatalf("seed: %v", err)
			}

			result := routeWithSessionState(tc.agents,
				RouteOptions{Session: session, Strategy: StrategyRoundRobin}, store, true)
			if result.Selected == nil {
				t.Fatal("no selection")
			}
			t.Logf("last=%s cursor=%d -> selected %s", tc.lastAgent, tc.cursor, result.Selected.PaneID)
			if result.Selected.PaneID != tc.want {
				t.Fatalf("selected %s, want %s", result.Selected.PaneID, tc.want)
			}

			rs, err := store.GetRoutingState(session, "")
			if err != nil || rs == nil {
				t.Fatalf("reload state: %+v, %v", rs, err)
			}
			if rs.LastAgent != tc.want || rs.RotationCursor != tc.wantCursor {
				t.Fatalf("persisted state = %+v, want last=%s cursor=%d", rs, tc.want, tc.wantCursor)
			}
		})
	}
}

// TestRoundRobin_FilterSetsRotateIndependently pins the bd-88um4 cross-filter
// fix: alternating sends with different agent-type filters each keep their
// OWN persisted rotation, so no pane in either filtered set is starved. With
// the old session-only key, the cc and cod cursors corrupted each other.
func TestRoundRobin_FilterSetsRotateIndependently(t *testing.T) {
	store := routingStateTestStore(t)
	ccAgents := func() []ScoredAgent {
		return []ScoredAgent{
			{PaneID: "%cc1", PaneIndex: 1, AgentType: "claude", Score: 50},
			{PaneID: "%cc2", PaneIndex: 2, AgentType: "claude", Score: 50},
		}
	}
	codAgents := func() []ScoredAgent {
		return []ScoredAgent{
			{PaneID: "%cod1", PaneIndex: 3, AgentType: "codex", Score: 50},
			{PaneID: "%cod2", PaneIndex: 4, AgentType: "codex", Score: 50},
		}
	}
	ccOpts := RouteOptions{Session: "filtproj", Strategy: StrategyRoundRobin, AgentType: "claude"}
	codOpts := RouteOptions{Session: "filtproj", Strategy: StrategyRoundRobin, AgentType: "codex"}

	// Interleaved sends: each filter must rotate through BOTH of its panes.
	steps := []struct {
		opts   RouteOptions
		agents []ScoredAgent
		want   string
	}{
		{ccOpts, ccAgents(), "%cc1"},
		{codOpts, codAgents(), "%cod1"},
		{ccOpts, ccAgents(), "%cc2"},
		{codOpts, codAgents(), "%cod2"},
		{ccOpts, ccAgents(), "%cc1"},
		{codOpts, codAgents(), "%cod1"},
	}
	for i, step := range steps {
		result := routeWithSessionState(step.agents, step.opts, store, true)
		if result.Selected == nil {
			t.Fatalf("step %d: no selection", i+1)
		}
		t.Logf("step %d (type=%s): selected %s", i+1, step.opts.AgentType, result.Selected.PaneID)
		if result.Selected.PaneID != step.want {
			t.Fatalf("step %d selected %s, want %s (filter cursors corrupted each other)",
				i+1, result.Selected.PaneID, step.want)
		}
	}

	// The mechanism: two independent rows, one per filter key.
	cc, err := store.GetRoutingState("filtproj", routingStateFilterKey(ccOpts))
	if err != nil || cc == nil || cc.LastAgent != "%cc1" {
		t.Fatalf("cc filter state = %+v, %v; want last %%cc1", cc, err)
	}
	cod, err := store.GetRoutingState("filtproj", routingStateFilterKey(codOpts))
	if err != nil || cod == nil || cod.LastAgent != "%cod1" {
		t.Fatalf("cod filter state = %+v, %v; want last %%cod1", cod, err)
	}
}

// TestRoutingStateFilterKey pins the key derivation: unfiltered sends map to
// the legacy empty key, and exclude sets are order-insensitive.
func TestRoutingStateFilterKey(t *testing.T) {
	if got := routingStateFilterKey(RouteOptions{Session: "s"}); got != "" {
		t.Fatalf("unfiltered key = %q, want empty (legacy row compatibility)", got)
	}
	a := routingStateFilterKey(RouteOptions{AgentType: "Claude", ExcludePanes: []int{3, 1}})
	b := routingStateFilterKey(RouteOptions{AgentType: "claude", ExcludePanes: []int{1, 3}})
	if a != b {
		t.Fatalf("equivalent filter sets produced different keys: %q vs %q", a, b)
	}
	c := routingStateFilterKey(RouteOptions{AgentType: "codex", ExcludePanes: []int{1, 3}})
	if a == c {
		t.Fatalf("different agent types share filter key %q", a)
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
	rs, err := store.GetRoutingState("stickyproj", "")
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
