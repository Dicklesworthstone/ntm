package pipeline

import (
	"errors"
	"testing"
)

func TestRotateAdjudicatorSkipsChampionsAndRecentAdjudicator(t *testing.T) {
	got, err := rotateAdjudicator(
		[]string{"p1", "p2", "p3", "p4", "p5"},
		[]string{"p1", "p2"},
		[]string{"p3"},
	)
	if err != nil {
		t.Fatalf("rotateAdjudicator() error = %v", err)
	}
	if got != "p4" {
		t.Fatalf("rotateAdjudicator() = %q, want p4", got)
	}
}

func TestRotateAdjudicatorNoPriorAdjudicationUsesFirstNonChampion(t *testing.T) {
	got, err := rotateAdjudicator(
		[]string{"p1", "p2", "p3", "p4", "p5"},
		[]string{"p1", "p2"},
		nil,
	)
	if err != nil {
		t.Fatalf("rotateAdjudicator() error = %v", err)
	}
	if got != "p3" {
		t.Fatalf("rotateAdjudicator() = %q, want p3", got)
	}
}

func TestRotateAdjudicatorErrorsWhenOnlyChampionsAvailable(t *testing.T) {
	got, err := rotateAdjudicator(
		[]string{"p1", "p2"},
		[]string{"p1", "p2"},
		nil,
	)
	if !errors.Is(err, errNoAdjudicatorPane) {
		t.Fatalf("rotateAdjudicator() error = %v, want %v", err, errNoAdjudicatorPane)
	}
	if got != "" {
		t.Fatalf("rotateAdjudicator() = %q, want empty pane", got)
	}
}

func TestRotateAdjudicatorUsesLongestHistoryGap(t *testing.T) {
	got, err := rotateAdjudicator(
		[]string{"p1", "p2", "p3", "p4", "p5"},
		[]string{"p1", "p2"},
		[]string{"p5", "p4", "p3"},
	)
	if err != nil {
		t.Fatalf("rotateAdjudicator() error = %v", err)
	}
	if got != "p5" {
		t.Fatalf("rotateAdjudicator() = %q, want p5", got)
	}
}

func TestByModelFamilyReturnsFirstMatchingPane(t *testing.T) {
	panes := []paneStrategyPane{
		{ID: "p1", ModelFamily: "cc"},
		{ID: "p2", ModelFamily: "cc"},
		{ID: "p3", ModelFamily: "cod"},
		{ID: "p4", ModelFamily: "gmi"},
	}

	got, err := byModelFamily(panes, "cc")
	if err != nil {
		t.Fatalf("byModelFamily() error = %v", err)
	}
	if got != "p1" {
		t.Fatalf("byModelFamily() = %q, want p1", got)
	}
}

func TestByModelFamilyReturnsSingleMatchingPane(t *testing.T) {
	panes := []paneStrategyPane{
		{ID: "p1", ModelFamily: "cc"},
		{ID: "p2", ModelFamily: "cc"},
		{ID: "p3", ModelFamily: "cod"},
		{ID: "p4", ModelFamily: "gmi"},
	}

	got, err := byModelFamily(panes, "cod")
	if err != nil {
		t.Fatalf("byModelFamily() error = %v", err)
	}
	if got != "p3" {
		t.Fatalf("byModelFamily() = %q, want p3", got)
	}
}

func TestByModelFamilyErrorsWhenNoPaneMatches(t *testing.T) {
	panes := []paneStrategyPane{
		{ID: "p1", ModelFamily: "cc"},
		{ID: "p2", ModelFamily: "cod"},
	}

	got, err := byModelFamily(panes, "ollama")
	if !errors.Is(err, errNoModelFamilyPane) {
		t.Fatalf("byModelFamily() error = %v, want %v", err, errNoModelFamilyPane)
	}
	if got != "" {
		t.Fatalf("byModelFamily() = %q, want empty pane", got)
	}
}

func TestByModelFamilyDifferenceReturnsDifferentFamilyPane(t *testing.T) {
	panes := []paneStrategyPane{
		{ID: "p1", ModelFamily: "cc"},
		{ID: "p2", ModelFamily: "cod"},
	}

	got, warned, err := byModelFamilyDifference(panes, "cc")
	if err != nil {
		t.Fatalf("byModelFamilyDifference() error = %v", err)
	}
	if warned {
		t.Fatal("byModelFamilyDifference() warned, want no warning")
	}
	if got != "p2" {
		t.Fatalf("byModelFamilyDifference() = %q, want p2", got)
	}
}

func TestByModelFamilyDifferenceFallsBackWhenAllPanesShareFamily(t *testing.T) {
	panes := []paneStrategyPane{
		{ID: "p1", ModelFamily: "cc"},
		{ID: "p2", ModelFamily: "cc"},
	}

	got, warned, err := byModelFamilyDifference(panes, "cc")
	if err != nil {
		t.Fatalf("byModelFamilyDifference() error = %v", err)
	}
	if !warned {
		t.Fatal("byModelFamilyDifference() warned = false, want true")
	}
	if got != "p1" {
		t.Fatalf("byModelFamilyDifference() = %q, want p1", got)
	}
}

func TestByModelFamilyDifferenceErrorsWhenNoPanesAvailable(t *testing.T) {
	got, warned, err := byModelFamilyDifference(nil, "cc")
	if !errors.Is(err, errNoPaneForStrategy) {
		t.Fatalf("byModelFamilyDifference() error = %v, want %v", err, errNoPaneForStrategy)
	}
	if warned {
		t.Fatal("byModelFamilyDifference() warned = true, want false")
	}
	if got != "" {
		t.Fatalf("byModelFamilyDifference() = %q, want empty pane", got)
	}
}

func TestSelectForeachPaneWithStateRotatesAdjudicatorAcrossDebates(t *testing.T) {
	// rotate_adjudicator must avoid the most recent adjudicator across
	// successive debate items. Without history threading, the strategy
	// always returned the first non-champion pane, so a debate set with
	// rotating champions would pin every adjudication to the same pane
	// and never balance the adjudication load (bd-2ubxp.9 contract).
	cfg := DefaultExecutorConfig("test-session")
	e := NewExecutor(cfg)

	strategyPanes := []paneStrategyPane{
		{ID: "%1"},
		{ID: "%2"},
		{ID: "%3"},
		{ID: "%4"},
	}
	debates := []map[string]interface{}{
		{"champion_a": "%1", "champion_b": "%2"}, // %3 or %4 eligible; first is %3
		{"champion_a": "%1", "champion_b": "%2"}, // same eligibility, but %3 just adjudicated
		{"champion_a": "%3", "champion_b": "%4"}, // now only %1/%2 eligible
	}
	got := make([]string, 0, len(debates))
	for i, item := range debates {
		paneID, _, _, err := e.selectForeachPaneWithState("rotate_adjudicator", strategyPanes, nil, item, i)
		if err != nil {
			t.Fatalf("debate %d: error = %v", i, err)
		}
		got = append(got, paneID)
	}

	// First debate must pick %3 (first eligible non-champion).
	// Second debate must AVOID %3 (most-recent adjudicator) and pick %4.
	// Third debate's champions exclude %3,%4 — must pick %1 (first eligible).
	want := []string{"%3", "%4", "%1"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("debate %d adjudicator = %q, want %q (full sequence: %v)", i, got[i], w, got)
		}
	}
}

func TestSelectForeachPaneWithStateNonRotateUnaffected(t *testing.T) {
	// Sanity: non-rotate strategies must still go through the unchanged
	// selectForeachPane path so the adjudicator-history wrapper does not
	// alter pane assignment for round_robin / by_model_family / domain.
	cfg := DefaultExecutorConfig("test-session")
	e := NewExecutor(cfg)

	strategyPanes := []paneStrategyPane{
		{ID: "%1"},
		{ID: "%2"},
		{ID: "%3"},
	}
	for i := 0; i < 4; i++ {
		paneID, _, _, err := e.selectForeachPaneWithState("round_robin", strategyPanes, nil, nil, i)
		if err != nil {
			t.Fatalf("iter %d: error = %v", i, err)
		}
		want := strategyPanes[i%3].ID
		if paneID != want {
			t.Errorf("iter %d round_robin = %q, want %q", i, paneID, want)
		}
	}
	// Adjudicator history must remain empty after round_robin assignments.
	if got := e.snapshotAdjudicatorHistory(); len(got) != 0 {
		t.Fatalf("adjudicator history = %v, want empty after round_robin", got)
	}
}

func TestByModelFamilyDifferenceErrorsWhenAuthorFamilyMissing(t *testing.T) {
	// An item that lacks author_model/model_family/family/type yields an empty
	// author family. byModelFamilyDifference must reject that input rather than
	// silently routing to the first available pane, which would defeat the
	// cross-family adversarial contract used by brenner / devils-advocate flows.
	panes := []paneStrategyPane{
		{ID: "p1", ModelFamily: "cc"},
		{ID: "p2", ModelFamily: "codex"},
	}

	got, warned, err := byModelFamilyDifference(panes, "")
	if !errors.Is(err, errNoModelFamilyPane) {
		t.Fatalf("byModelFamilyDifference() error = %v, want %v", err, errNoModelFamilyPane)
	}
	if warned {
		t.Fatal("byModelFamilyDifference() warned = true, want false on missing-family error")
	}
	if got != "" {
		t.Fatalf("byModelFamilyDifference() = %q, want empty pane", got)
	}
}

func TestRoundRobinByDomainReturnsOwningPane(t *testing.T) {
	panes := []paneStrategyPane{
		{ID: "p2", Domains: []string{"H-001", "H-005"}},
		{ID: "p3", Domains: []string{"H-002"}},
	}

	got, err := roundRobinByDomain(panes, "H-005", 0)
	if err != nil {
		t.Fatalf("roundRobinByDomain() error = %v", err)
	}
	if got != "p2" {
		t.Fatalf("roundRobinByDomain() = %q, want p2", got)
	}
}

func TestRoundRobinByDomainFallsBackToRoundRobin(t *testing.T) {
	panes := []paneStrategyPane{
		{ID: "p2"},
		{ID: "p3"},
		{ID: "p4"},
	}

	got, err := roundRobinByDomain(panes, "H-999", 4)
	if err != nil {
		t.Fatalf("roundRobinByDomain() error = %v", err)
	}
	if got != "p3" {
		t.Fatalf("roundRobinByDomain() = %q, want p3", got)
	}
}

func TestRoundRobinPaneCyclesInDeclarationOrder(t *testing.T) {
	panes := []paneStrategyPane{
		{ID: "p1"},
		{ID: "p2"},
		{ID: "p3"},
	}

	var got []string
	for i := 0; i < 5; i++ {
		paneID, err := roundRobinPane(panes, i)
		if err != nil {
			t.Fatalf("roundRobinPane(%d) error = %v", i, err)
		}
		got = append(got, paneID)
	}
	want := []string{"p1", "p2", "p3", "p1", "p2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("roundRobinPane sequence = %v, want %v", got, want)
		}
	}
}

func TestRoundRobinPaneErrorsWhenNoPanesAvailable(t *testing.T) {
	got, err := roundRobinPane(nil, 0)
	if !errors.Is(err, errNoPaneForStrategy) {
		t.Fatalf("roundRobinPane() error = %v, want %v", err, errNoPaneForStrategy)
	}
	if got != "" {
		t.Fatalf("roundRobinPane() = %q, want empty pane", got)
	}
}

func TestRoundRobinPaneSkipsExcludedPanes(t *testing.T) {
	panes := []paneStrategyPane{
		{ID: "p0", Excluded: true},
		{ID: "p1"},
		{ID: "p2"},
	}

	got, err := roundRobinPane(panes, 2)
	if err != nil {
		t.Fatalf("roundRobinPane() error = %v", err)
	}
	if got != "p1" {
		t.Fatalf("roundRobinPane() = %q, want p1 after excluding p0", got)
	}
}
