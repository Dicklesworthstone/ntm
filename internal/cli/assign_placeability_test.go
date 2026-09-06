package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func placeabilityPane(id string, index int, agentType tmux.AgentType, tags ...string) tmux.Pane {
	return tmux.Pane{ID: id, Index: index, WindowIndex: 0, Type: agentType, Tags: tags}
}

func placeabilityObservation(now time.Time, panes ...statuspkg.PaneObservation) statuspkg.SessionObservation {
	return statuspkg.SessionObservation{Session: "demo", ObservedAt: now, Complete: true, Panes: panes}
}

func observedPane(id string, state statuspkg.AgentState, confidence float64) statuspkg.PaneObservation {
	return statuspkg.PaneObservation{
		Pane: tmux.PaneRef{ID: id},
		Current: statuspkg.StateObservation{
			Status:     statuspkg.AgentStatus{State: state},
			Freshness:  statuspkg.FreshnessFresh,
			Confidence: confidence,
		},
		RawOutput: "output for " + id,
	}
}

// TestEvaluateAssignPanePlaceabilityNamesEveryGate pins ntm#306: each gate
// that used to collapse into "no idle agents" now yields its own verdict, in
// evaluation order, and only the pane that clears all of them becomes an agent.
func TestEvaluateAssignPanePlaceabilityNamesEveryGate(t *testing.T) {
	now := time.Now().UTC()
	panes := []tmux.Pane{
		placeabilityPane("%1", 1, tmux.AgentUser),
		placeabilityPane("%2", 2, tmux.AgentClaude),
		placeabilityPane("%3", 3, tmux.AgentCodex),
		placeabilityPane("%4", 4, tmux.AgentCodex),
		placeabilityPane("%5", 5, tmux.AgentCodex),
		placeabilityPane("%6", 6, tmux.AgentCodex),
		placeabilityPane("%7", 7, tmux.AgentCodex),
	}
	stale := observedPane("%6", statuspkg.StateIdle, 0.95)
	stale.Current.Freshness = statuspkg.FreshnessStale
	observation := placeabilityObservation(now,
		observedPane("%1", statuspkg.StateIdle, 0.95),
		observedPane("%2", statuspkg.StateIdle, 0.95),
		observedPane("%3", statuspkg.StateIdle, 0.95),
		observedPane("%4", statuspkg.StateWorking, 0.95),
		observedPane("%5", statuspkg.StateIdle, 0.40),
		stale,
		observedPane("%7", statuspkg.StateIdle, 0.95),
	)
	active := map[string]string{"%3": "ntm-held"}

	agents, verdicts, err := evaluateAssignPanePlaceability(panes, active, observation, "codex", now)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(agents) != 1 || agents[0].pane.ID != "%7" || agents[0].agentType != "codex" || agents[0].state != "idle" || agents[0].scrollback != "output for %7" {
		t.Fatalf("agents = %+v, want only pane %%7", agents)
	}
	if len(verdicts) != len(panes) {
		t.Fatalf("got %d verdicts for %d panes", len(verdicts), len(panes))
	}

	want := []struct {
		verdict   string
		detail    string
		state     string
		placeable bool
	}{
		{verdict: PlaceabilityNotAgentPane},
		{verdict: PlaceabilityAgentTypeFiltered, detail: "filter=codex"},
		{verdict: PlaceabilityActiveAssignment, detail: "held by ntm-held", state: "idle"},
		{verdict: PlaceabilityNotIdle, detail: "state=working", state: "working"},
		{verdict: PlaceabilityLowConfidence, detail: "confidence 0.40 outside [0.75, 1.00]", state: "idle"},
		{verdict: PlaceabilityObservationStale, detail: "freshness=stale", state: "idle"},
		{verdict: PlaceabilityPlaceable, state: "idle", placeable: true},
	}
	for i, expected := range want {
		got := verdicts[i]
		if got.PaneID != panes[i].ID || got.PaneTarget != panes[i].Ref().Physical() {
			t.Errorf("verdict %d identity = %s/%s, want %s/%s", i, got.PaneID, got.PaneTarget, panes[i].ID, panes[i].Ref().Physical())
		}
		if got.Verdict != expected.verdict || got.Detail != expected.detail || got.State != expected.state || got.Placeable != expected.placeable {
			t.Errorf("verdict %d = %+v, want verdict=%q detail=%q state=%q placeable=%v", i, got, expected.verdict, expected.detail, expected.state, expected.placeable)
		}
	}
	// The fenced pane reports its observed idle state: "idle and held" is the
	// leaked-claim signature the old output could not show.
	if verdicts[2].Confidence != 0.95 {
		t.Errorf("fenced pane confidence = %.2f, want the observed 0.95", verdicts[2].Confidence)
	}
	// Panes rejected before observation carry no state.
	if verdicts[0].State != "" || verdicts[0].Confidence != 0 || verdicts[1].State != "" {
		t.Errorf("pre-observation rejections must not carry state: %+v %+v", verdicts[0], verdicts[1])
	}
	if verdicts[1].AgentType != "claude" || verdicts[0].AgentType != "user" {
		t.Errorf("agent types = %q/%q, want user/claude", verdicts[0].AgentType, verdicts[1].AgentType)
	}
}

// TestEvaluateAssignPanePlaceabilityFailsClosedOnCandidateEvidence keeps the
// assigner from acting on evidence it cannot trust: a capture error or a missing
// observation on a candidate pane aborts, while the same defect on a pane that
// is excluded anyway (held by an assignment) is reported, not fatal.
func TestEvaluateAssignPanePlaceabilityFailsClosedOnCandidateEvidence(t *testing.T) {
	now := time.Now().UTC()
	panes := []tmux.Pane{placeabilityPane("%1", 1, tmux.AgentClaude)}

	errored := observedPane("%1", statuspkg.StateIdle, 0.95)
	errored.Current.Error = "capture unavailable"
	if _, _, err := evaluateAssignPanePlaceability(panes, nil, placeabilityObservation(now, errored), "", now); err == nil || !strings.Contains(err.Error(), "capture unavailable") {
		t.Fatalf("capture error on a candidate must abort, got %v", err)
	}
	if _, _, err := evaluateAssignPanePlaceability(panes, nil, placeabilityObservation(now), "", now); err == nil || !strings.Contains(err.Error(), "no unique pane") {
		t.Fatalf("missing observation on a candidate must abort, got %v", err)
	}
	if _, _, err := evaluateAssignPanePlaceability(panes, nil, placeabilityObservation(now.Add(-statuspkg.DispatchObservationMaxAge-time.Second), observedPane("%1", statuspkg.StateIdle, 0.95)), "", now); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale session observation must abort, got %v", err)
	}

	agents, verdicts, err := evaluateAssignPanePlaceability(panes, map[string]string{"%1": "ntm-held"}, placeabilityObservation(now, errored), "", now)
	if err != nil {
		t.Fatalf("held pane with a capture error must not abort: %v", err)
	}
	if len(agents) != 0 || len(verdicts) != 1 || verdicts[0].Verdict != PlaceabilityActiveAssignment || verdicts[0].State != "idle" {
		t.Fatalf("held pane verdict = %+v agents=%d", verdicts, len(agents))
	}
}

// TestEvaluateAssignPanePlaceabilityMatchesFenceByPhysicalTarget covers stores
// whose active rows are keyed by window.pane rather than pane ID.
func TestEvaluateAssignPanePlaceabilityMatchesFenceByPhysicalTarget(t *testing.T) {
	now := time.Now().UTC()
	pane := placeabilityPane("%9", 2, tmux.AgentClaude)
	_, verdicts, err := evaluateAssignPanePlaceability([]tmux.Pane{pane}, map[string]string{pane.Ref().Physical(): "ntm-phys"}, placeabilityObservation(now, observedPane("%9", statuspkg.StateIdle, 0.95)), "", now)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if verdicts[0].Verdict != PlaceabilityActiveAssignment || verdicts[0].Detail != "held by ntm-phys" {
		t.Fatalf("verdict = %+v, want active_assignment held by ntm-phys", verdicts[0])
	}
}

// TestMarkRoleIneligiblePanesOnlyDowngradesDroppedPlaceablePanes: role gating
// explains the panes it removed and never rewrites an earlier gate's verdict.
func TestMarkRoleIneligiblePanesOnlyDowngradesDroppedPlaceablePanes(t *testing.T) {
	reviewer := placeabilityPane("%1", 1, tmux.AgentClaude, "reviewer")
	implementer := placeabilityPane("%2", 2, tmux.AgentClaude, "implementer")
	untagged := placeabilityPane("%3", 3, tmux.AgentCodex)
	agents := []assignAgentInfo{
		{pane: reviewer, agentType: "claude"},
		{pane: implementer, agentType: "claude"},
		{pane: untagged, agentType: "codex"},
	}
	verdicts := []AssignPanePlaceability{
		{PaneID: "%1", Placeable: true, Verdict: PlaceabilityPlaceable},
		{PaneID: "%2", Placeable: true, Verdict: PlaceabilityPlaceable},
		{PaneID: "%3", Placeable: true, Verdict: PlaceabilityPlaceable},
		{PaneID: "%4", Verdict: PlaceabilityNotIdle, Detail: "state=working"},
	}

	eligible := filterAssignAgentsForTemplate(agents, nil, "impl")
	if len(eligible) != 2 {
		t.Fatalf("impl template kept %d agents, want 2", len(eligible))
	}
	markRoleIneligiblePanes(verdicts, eligible, " IMPL ")

	if verdicts[0].Placeable || verdicts[0].Verdict != PlaceabilityRoleIneligible || verdicts[0].Detail != "template=impl" {
		t.Errorf("reviewer-only pane = %+v, want role_ineligible template=impl", verdicts[0])
	}
	for _, i := range []int{1, 2} {
		if !verdicts[i].Placeable || verdicts[i].Verdict != PlaceabilityPlaceable {
			t.Errorf("surviving pane %d = %+v, want placeable", i, verdicts[i])
		}
	}
	if verdicts[3].Verdict != PlaceabilityNotIdle || verdicts[3].Detail != "state=working" {
		t.Errorf("earlier verdict was rewritten: %+v", verdicts[3])
	}

	// No template filter: nothing is dropped, nothing changes.
	unchanged := []AssignPanePlaceability{{PaneID: "%1", Placeable: true, Verdict: PlaceabilityPlaceable}}
	markRoleIneligiblePanes(unchanged, filterAssignAgentsForTemplate(agents[:1], nil, "custom"), "custom")
	if !unchanged[0].Placeable {
		t.Errorf("pass-through template downgraded a pane: %+v", unchanged[0])
	}
}

func TestPlaceabilityBreakdownFollowsGateOrder(t *testing.T) {
	counts := placeabilityCounts([]AssignPanePlaceability{
		{Verdict: PlaceabilityNotIdle},
		{Verdict: PlaceabilityActiveAssignment},
		{Verdict: PlaceabilityNotIdle},
		{Verdict: PlaceabilityNotAgentPane},
		{Verdict: "future_gate"},
	})
	if got := placeabilityBreakdown(counts); got != "1 not_agent_pane, 1 active_assignment, 2 not_idle, 1 future_gate" {
		t.Fatalf("breakdown = %q", got)
	}
	if placeabilityCounts(nil) != nil {
		t.Fatal("no panes must yield no counts (omitted from JSON)")
	}
	if placeabilityBreakdown(nil) != "" {
		t.Fatal("empty breakdown must render as empty")
	}
}

// TestDisplayAssignOutputEnhancedNamesPlaceabilityGates: the human zero-agent
// path names the gate breakdown and every pane instead of "No idle agents".
func TestDisplayAssignOutputEnhancedNamesPlaceabilityGates(t *testing.T) {
	out := &AssignOutputEnhanced{
		Strategy: "balanced",
		Panes: []AssignPanePlaceability{
			{PaneID: "%11", PaneTarget: "1.1", AgentType: "claude", State: "idle", Confidence: 0.95, Verdict: PlaceabilityActiveAssignment, Detail: "held by ntm-42"},
			{PaneID: "%12", PaneTarget: "1.2", AgentType: "codex", State: "working", Confidence: 0.90, Verdict: PlaceabilityNotIdle, Detail: "state=working"},
			{PaneID: "%13", PaneTarget: "1.3", AgentType: "user", Verdict: PlaceabilityNotAgentPane},
		},
		Summary: AssignSummaryEnhanced{
			TotalBeadCount:  3,
			ActionableCount: 3,
			SkippedCount:    3,
			PlaceabilityCounts: map[string]int{
				PlaceabilityActiveAssignment: 1, PlaceabilityNotIdle: 1, PlaceabilityNotAgentPane: 1,
			},
		},
		Skipped: []SkippedItem{{BeadID: "ntm-1", Reason: SkipReasonNoPlaceableAgents}},
	}

	text, err := captureStdout(t, func() error {
		displayAssignOutputEnhanced(out, false)
		return nil
	})
	if err != nil {
		t.Fatalf("display: %v", err)
	}
	if strings.Contains(text, "No idle agents available") {
		t.Fatalf("output still uses the opaque reason:\n%s", text)
	}
	for _, want := range []string{
		"Reason: No placeable agents (1 not_agent_pane, 1 active_assignment, 1 not_idle)",
		"Panes:",
		"1.1   %11    claude   idle     0.95 active_assignment (held by ntm-42)",
		"1.2   %12    codex    working  0.90 not_idle (state=working)",
		"1.3   %13    user     -           - not_agent_pane",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q:\n%s", want, text)
		}
	}

	// With placeable agents but no beads, the pane list is verbose-only.
	withAgents := &AssignOutputEnhanced{
		Strategy: "balanced",
		Panes:    []AssignPanePlaceability{{PaneID: "%11", PaneTarget: "1.1", AgentType: "claude", State: "idle", Confidence: 0.95, Placeable: true, Verdict: PlaceabilityPlaceable}},
		Summary:  AssignSummaryEnhanced{IdleAgents: 1, PlaceabilityCounts: map[string]int{PlaceabilityPlaceable: 1}},
	}
	quiet, _ := captureStdout(t, func() error { displayAssignOutputEnhanced(withAgents, false); return nil })
	if !strings.Contains(quiet, "No ready beads to assign") || strings.Contains(quiet, "Panes:") {
		t.Errorf("non-verbose output with agents should not list panes:\n%s", quiet)
	}
	loud, _ := captureStdout(t, func() error { displayAssignOutputEnhanced(withAgents, true); return nil })
	if !strings.Contains(loud, "ok 1.1   %11    claude   idle     0.95 placeable") {
		t.Errorf("verbose output should list placeable panes:\n%s", loud)
	}
}

// TestAssignOutputEnhancedJSONCarriesPanePlaceability pins the JSON shape:
// existing fields unchanged, one stable new array, and counts in the summary.
func TestAssignOutputEnhancedJSONCarriesPanePlaceability(t *testing.T) {
	out := &AssignOutputEnhanced{
		Strategy:    "balanced",
		Assignments: []AssignmentItem{},
		Skipped:     []SkippedItem{{BeadID: "ntm-1", Reason: SkipReasonNoPlaceableAgents}},
		Panes: []AssignPanePlaceability{
			{PaneID: "%11", PaneTarget: "1.1", AgentType: "claude", State: "idle", Confidence: 0.95, Verdict: PlaceabilityActiveAssignment, Detail: "held by ntm-42"},
			{PaneID: "%13", PaneTarget: "1.3", AgentType: "user", Verdict: PlaceabilityNotAgentPane},
		},
		Summary: AssignSummaryEnhanced{TotalBeadCount: 1, ActionableCount: 1, SkippedCount: 1, PlaceabilityCounts: map[string]int{PlaceabilityActiveAssignment: 1, PlaceabilityNotAgentPane: 1}},
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"strategy", "assignments", "skipped", "summary", "pane_placeability"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("JSON missing %q: %s", key, raw)
		}
	}
	panes, ok := doc["pane_placeability"].([]any)
	if !ok || len(panes) != 2 {
		t.Fatalf("pane_placeability = %#v, want 2 entries", doc["pane_placeability"])
	}
	first := panes[0].(map[string]any)
	for key, want := range map[string]any{
		"pane_id": "%11", "pane_target": "1.1", "agent_type": "claude", "state": "idle", "confidence": 0.95,
		"placeable": false, "verdict": "active_assignment", "detail": "held by ntm-42",
	} {
		if first[key] != want {
			t.Errorf("pane_placeability[0].%s = %#v, want %#v", key, first[key], want)
		}
	}
	second := panes[1].(map[string]any)
	for _, absent := range []string{"state", "confidence", "detail"} {
		if _, present := second[absent]; present {
			t.Errorf("pane_placeability[1] should omit %q: %#v", absent, second)
		}
	}
	if second["placeable"] != false || second["verdict"] != "not_agent_pane" {
		t.Errorf("pane_placeability[1] = %#v", second)
	}
	summary := doc["summary"].(map[string]any)
	counts, ok := summary["placeability_counts"].(map[string]any)
	if !ok || counts["active_assignment"] != float64(1) || counts["not_agent_pane"] != float64(1) {
		t.Errorf("summary.placeability_counts = %#v", summary["placeability_counts"])
	}
	if summary["idle_agent_count"] != float64(0) {
		t.Errorf("idle_agent_count must remain: %#v", summary)
	}
	skipped := doc["skipped"].([]any)[0].(map[string]any)
	if skipped["reason"] != "no_placeable_agents" {
		t.Errorf("skipped reason = %#v", skipped["reason"])
	}

	// A result built without pane evaluation omits the new fields entirely,
	// so older-shaped documents stay byte-compatible.
	legacy, _ := json.Marshal(&AssignOutputEnhanced{Strategy: "balanced", Assignments: []AssignmentItem{}, Skipped: []SkippedItem{}})
	if strings.Contains(string(legacy), "pane_placeability") || strings.Contains(string(legacy), "placeability_counts") {
		t.Errorf("unevaluated output must omit placeability fields: %s", legacy)
	}
}
