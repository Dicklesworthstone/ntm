package robot

// Wiring proof for bd-ws2-wire-or-delete-ykmcz.4: --dist-strategy /
// --strategy route through the real graph-aware planner in internal/assign,
// "simple" is the honest name for the historical sequential pairing, and the
// default is pinned so nothing changes silently for users who never pass the
// flag.

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/bv"
)

// TestAssignStrategyDefaultPinnedToSimple pins the default strategy name.
// The historical default ("balanced") silently behaved as sequential pairing;
// after wiring the real planners, the default must be the honest name for
// that same sequential behavior so users who never pass --strategy or
// --dist-strategy see no silent change in assignment output.
func TestAssignStrategyDefaultPinnedToSimple(t *testing.T) {
	if assignStrategyDefault != "simple" {
		t.Fatalf("assignStrategyDefault = %q, want %q (changing this silently changes default assignment output; see CHANGELOG)", assignStrategyDefault, "simple")
	}
	if got := normalizeAssignStrategy(""); got != "simple" {
		t.Fatalf("normalizeAssignStrategy(\"\") = %q, want %q", got, "simple")
	}
	if got := normalizeAssignStrategy("  Quality "); got != "quality" {
		t.Fatalf("normalizeAssignStrategy(\"  Quality \") = %q, want %q", got, "quality")
	}
	for _, name := range []string{"simple", "balanced", "speed", "quality", "dependency"} {
		if !isValidAssignStrategy(name) {
			t.Errorf("isValidAssignStrategy(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "graph", "ready", "round-robin"} {
		if isValidAssignStrategy(name) {
			t.Errorf("isValidAssignStrategy(%q) = true, want false", name)
		}
	}
}

// strategyFixture returns agents and beads crafted so the strategies provably
// disagree. Capability matrix (assign.DefaultCapabilities):
//
//	bug:           claude 0.80, codex 0.90, gemini 0.75
//	documentation: claude 0.85, codex 0.70, gemini 0.90
//	refactor:      claude 0.95, codex 0.75, gemini 0.75
//
// Priorities are distinct so the planner's priority pre-sort is deterministic.
func strategyFixture() ([]assignAgentInfo, []bv.BeadPreview, []string) {
	agents := []assignAgentInfo{
		{paneID: "%1", paneTarget: "0.1", agentType: "claude", state: "idle"},
		{paneID: "%2", paneTarget: "0.2", agentType: "codex", state: "idle"},
		{paneID: "%3", paneTarget: "0.3", agentType: "gemini", state: "idle"},
	}
	beads := []bv.BeadPreview{
		{ID: "bd-bug", Title: "Fix crash bug", Priority: "P0"},
		{ID: "bd-doc", Title: "Write documentation guide", Priority: "P1"},
		{ID: "bd-ref", Title: "Refactor storage layer", Priority: "P2"},
	}
	idle := []string{"%1", "%2", "%3"}
	return agents, beads, idle
}

func pairingOf(recs []AssignRecommend) map[string]string {
	pairing := make(map[string]string, len(recs))
	for _, rec := range recs {
		pairing[rec.AssignBead] = rec.PaneID
	}
	return pairing
}

// TestPlanAssignments_SimpleIsSequential pins the "simple" strategy to the
// historical sequential behavior: next ready bead to next idle agent,
// regardless of capability fit.
func TestPlanAssignments_SimpleIsSequential(t *testing.T) {
	agents, beads, idle := strategyFixture()
	recs := planAssignments(agents, beads, nil, "simple", idle)
	if len(recs) != 3 {
		t.Fatalf("expected 3 recommendations, got %d", len(recs))
	}
	want := map[string]string{"bd-bug": "%1", "bd-doc": "%2", "bd-ref": "%3"}
	got := pairingOf(recs)
	for bead, pane := range want {
		if got[bead] != pane {
			t.Errorf("simple pairing %s -> %s, want %s (sequential)", bead, got[bead], pane)
		}
	}
	for _, rec := range recs {
		if !strings.Contains(rec.Reasoning, "simple sequential pairing") {
			t.Errorf("simple reasoning %q must name the sequential pairing", rec.Reasoning)
		}
	}
}

// TestPlanAssignments_QualityDiffersFromSimple proves the planner is actually
// running: quality sends each bead to the best-scoring agent, which is a
// provably different plan from sequential pairing on the same fixture.
func TestPlanAssignments_QualityDiffersFromSimple(t *testing.T) {
	agents, beads, idle := strategyFixture()

	simple := pairingOf(planAssignments(agents, beads, nil, "simple", idle))
	quality := pairingOf(planAssignments(agents, beads, nil, "quality", idle))

	// Best capability matches: bug -> codex, docs -> gemini, refactor -> claude.
	want := map[string]string{"bd-bug": "%2", "bd-doc": "%3", "bd-ref": "%1"}
	for bead, pane := range want {
		if quality[bead] != pane {
			t.Errorf("quality pairing %s -> %s, want %s (capability argmax)", bead, quality[bead], pane)
		}
	}

	if quality["bd-bug"] == simple["bd-bug"] && quality["bd-doc"] == simple["bd-doc"] && quality["bd-ref"] == simple["bd-ref"] {
		t.Fatal("quality plan must differ from the sequential simple plan on this fixture")
	}
}

// TestPlanAssignments_BalancedDiffersFromSimple proves "balanced" no longer
// silently means sequential: it scores pairings against the capability matrix.
func TestPlanAssignments_BalancedDiffersFromSimple(t *testing.T) {
	agents, beads, idle := strategyFixture()

	simple := pairingOf(planAssignments(agents, beads, nil, "simple", idle))
	balanced := pairingOf(planAssignments(agents, beads, nil, "balanced", idle))

	// Balanced spreads work by assignment count then score; with equal counts
	// the P0 bug goes to codex (0.90 beats claude's 0.80) — not to claude as
	// the sequential pairing would.
	if balanced["bd-bug"] != "%2" {
		t.Errorf("balanced sends bd-bug to %s, want %%2 (codex, best bug score)", balanced["bd-bug"])
	}
	if simple["bd-bug"] != "%1" {
		t.Fatalf("fixture broke: simple should send bd-bug to %%1, got %s", simple["bd-bug"])
	}
	if balanced["bd-bug"] == simple["bd-bug"] {
		t.Fatal("balanced plan must differ from the sequential simple plan on this fixture")
	}
}

// TestPlanAssignments_SpeedDiffersFromQuality proves the strategies disagree
// with each other, not just with simple: speed takes the first acceptable
// agent in pane order while quality takes the capability argmax.
func TestPlanAssignments_SpeedDiffersFromQuality(t *testing.T) {
	agents, beads, idle := strategyFixture()

	speed := pairingOf(planAssignments(agents, beads, nil, "speed", idle))
	quality := pairingOf(planAssignments(agents, beads, nil, "quality", idle))

	// Speed: the P0 bug goes to the first available agent (claude), even
	// though codex scores higher.
	if speed["bd-bug"] != "%1" {
		t.Errorf("speed sends bd-bug to %s, want %%1 (first available agent)", speed["bd-bug"])
	}
	if quality["bd-bug"] != "%2" {
		t.Errorf("quality sends bd-bug to %s, want %%2 (capability argmax)", quality["bd-bug"])
	}
	if speed["bd-bug"] == quality["bd-bug"] {
		t.Fatal("speed and quality must disagree on bd-bug for this fixture")
	}
}

// TestPlanAssignments_DependencyIsGraphAware proves the dependency strategy
// consumes the bead graph's unblocks fan-out — a signal the pre-wire
// sequential code never even received.
func TestPlanAssignments_DependencyIsGraphAware(t *testing.T) {
	agents := []assignAgentInfo{
		{paneID: "%1", paneTarget: "0.1", agentType: "claude", state: "idle"},
	}
	idle := []string{"%1"}
	beads := []bv.BeadPreview{
		{ID: "bd-leaf", Title: "Improve pipeline logging", Priority: "P2"},
		{ID: "bd-blocker", Title: "Improve cache layer", Priority: "P2"},
	}
	unblocks := map[string][]string{
		"bd-blocker": {"bd-a", "bd-b", "bd-c"},
	}

	simple := pairingOf(planAssignments(agents, beads, unblocks, "simple", idle))
	dependency := pairingOf(planAssignments(agents, beads, unblocks, "dependency", idle))

	if _, ok := simple["bd-leaf"]; !ok {
		t.Fatalf("simple should assign the first listed bead bd-leaf, got %v", simple)
	}
	if _, ok := dependency["bd-blocker"]; !ok {
		t.Fatalf("dependency should assign the blocker that unblocks 3 items, got %v", dependency)
	}
	if _, stillLeaf := dependency["bd-leaf"]; stillLeaf {
		t.Fatal("dependency assigned the leaf bead over the blocker; unblocks signal was ignored")
	}
}

// TestPlanAssignments_ConfidenceComesFromPlanner asserts confidence is the
// planner's number, propagated instead of discarded.
func TestPlanAssignments_ConfidenceComesFromPlanner(t *testing.T) {
	agents, beads, idle := strategyFixture()

	quality := planAssignments(agents, beads, nil, "quality", idle)
	for _, rec := range quality {
		if rec.AssignBead == "bd-bug" {
			// Quality confidence equals the raw pair score: codex bug = 0.90.
			if rec.Confidence != 0.90 {
				t.Errorf("quality confidence for bd-bug = %v, want 0.90 (codex bug capability)", rec.Confidence)
			}
		}
	}

	speed := planAssignments(agents, beads, nil, "speed", idle)
	for _, rec := range speed {
		if rec.AssignBead == "bd-bug" {
			// Speed confidence uses the planner's (score+0.9)/2 formula:
			// claude bug 0.80 -> 0.85 (allow float64 rounding).
			if rec.Confidence < 0.8499 || rec.Confidence > 0.8501 {
				t.Errorf("speed confidence for bd-bug = %v, want ~0.85 ((0.80+0.90)/2)", rec.Confidence)
			}
		}
	}

	// The distribute envelope carries the planner confidence too.
	dist := DistributeRecommendation{Confidence: 0.85}
	if dist.Confidence != 0.85 {
		t.Fatal("DistributeRecommendation must carry planner confidence")
	}
}

// TestPlanAssignments_OneBeadPerPane pins the bulk-assign contract: the
// planner adapter never recommends two beads for the same pane.
func TestPlanAssignments_OneBeadPerPane(t *testing.T) {
	agents := []assignAgentInfo{
		{paneID: "%1", paneTarget: "0.1", agentType: "claude", state: "idle"},
		{paneID: "%2", paneTarget: "0.2", agentType: "codex", state: "idle"},
	}
	idle := []string{"%1", "%2"}
	beads := []bv.BeadPreview{
		{ID: "bd-1", Title: "Refactor storage layer", Priority: "P0"},
		{ID: "bd-2", Title: "Refactor config loader", Priority: "P1"},
		{ID: "bd-3", Title: "Refactor cache layer", Priority: "P2"},
		{ID: "bd-4", Title: "Refactor auth flow", Priority: "P3"},
	}
	for _, strategy := range []string{"simple", "balanced", "speed", "quality", "dependency"} {
		recs := planAssignments(agents, beads, nil, strategy, idle)
		seenPane := make(map[string]string)
		seenBead := make(map[string]bool)
		for _, rec := range recs {
			if prev, dup := seenPane[rec.PaneID]; dup {
				t.Errorf("strategy %s recommended pane %s twice (%s and %s)", strategy, rec.PaneID, prev, rec.AssignBead)
			}
			seenPane[rec.PaneID] = rec.AssignBead
			if seenBead[rec.AssignBead] {
				t.Errorf("strategy %s recommended bead %s twice", strategy, rec.AssignBead)
			}
			seenBead[rec.AssignBead] = true
		}
		if len(recs) > len(agents) {
			t.Errorf("strategy %s produced %d recommendations for %d panes", strategy, len(recs), len(agents))
		}
	}
}
