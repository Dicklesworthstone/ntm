package cli

import (
	"strings"
	"testing"
	"time"

	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/charmbracelet/lipgloss"
)

func freshObservation(state statuspkg.AgentState, confidence float64) statuspkg.PaneObservation {
	return statuspkg.PaneObservation{
		Current: statuspkg.StateObservation{
			Status:     statuspkg.AgentStatus{State: state},
			ObservedAt: time.Now(),
			Freshness:  statuspkg.FreshnessFresh,
			Confidence: confidence,
		},
	}
}

// dispatchRejectionReason must agree with SafeToDispatch on every input: a pane
// it calls placeable must have no reason, and a rejected pane must never be
// reported as rejected for "unknown".
func TestDispatchRejectionReasonAgreesWithSafeToDispatch(t *testing.T) {
	cases := []struct {
		name       string
		observed   statuspkg.PaneObservation
		wantReason string
	}{
		{"idle and confident is placeable", freshObservation(statuspkg.StateIdle, 0.95), ""},
		{"working is not idle", freshObservation(statuspkg.StateWorking, 0.95), AgentPoolReasonStateNotIdle},
		{"unknown is not idle", freshObservation(statuspkg.StateUnknown, 0.95), AgentPoolReasonStateNotIdle},
		{"error state is not idle", freshObservation(statuspkg.StateError, 0.95), AgentPoolReasonStateNotIdle},
		{"low confidence is rejected before state", freshObservation(statuspkg.StateIdle, 0.5), AgentPoolReasonLowConfidence},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := dispatchRejectionReason(tc.observed)
			safe := tc.observed.SafeToDispatch()
			if safe && got != "" {
				t.Fatalf("pane is SafeToDispatch but reported reason %q", got)
			}
			if !safe && got == "" {
				t.Fatal("pane is not SafeToDispatch but reported no reason")
			}
			if got == "unknown" {
				t.Fatal("dispatchRejectionReason fell through: it disagrees with SafeToDispatch")
			}
			if tc.wantReason != "" && got != tc.wantReason {
				t.Fatalf("reason = %q, want %q", got, tc.wantReason)
			}
		})
	}
}

func TestDispatchRejectionReasonReportsStaleAndErrored(t *testing.T) {
	stale := freshObservation(statuspkg.StateIdle, 0.95)
	stale.Current.Freshness = statuspkg.FreshnessStale
	if got := dispatchRejectionReason(stale); got != AgentPoolReasonObservationStale {
		t.Fatalf("stale reason = %q, want %q", got, AgentPoolReasonObservationStale)
	}

	errored := freshObservation(statuspkg.StateIdle, 0.95)
	errored.Current.Error = "capture failed"
	if got := dispatchRejectionReason(errored); got != AgentPoolReasonObservationError {
		t.Fatalf("errored reason = %q, want %q", got, AgentPoolReasonObservationError)
	}
}

// A pane dropped by persona role gating must be reported as role-gated rather
// than silently vanishing from a pool that still calls it placeable.
func TestMarkRoleGatedPoolNamesTheDroppedPane(t *testing.T) {
	pool := []AgentPoolEntry{
		{PaneID: "%11", AgentType: "claude", State: "idle", Placeable: true},
		{PaneID: "%12", AgentType: "codex", State: "idle", Placeable: true},
	}
	before := []assignAgentInfo{
		{pane: tmux.Pane{ID: "%11"}},
		{pane: tmux.Pane{ID: "%12"}},
	}
	after := []assignAgentInfo{{pane: tmux.Pane{ID: "%12"}}}

	got := markRoleGatedPool(pool, before, after, "review")

	if got[0].Placeable {
		t.Fatal("%11 was dropped by role gating but is still reported placeable")
	}
	if !strings.HasPrefix(got[0].Reason, AgentPoolReasonRoleIneligible) {
		t.Fatalf("%%11 reason = %q, want the role-ineligible reason", got[0].Reason)
	}
	if !strings.Contains(got[0].Reason, "review") {
		t.Fatalf("%%11 reason = %q, want the template named", got[0].Reason)
	}
	if !got[1].Placeable || got[1].Reason != "" {
		t.Fatalf("%%12 survived role gating but was marked %+v", got[1])
	}
}

func TestMarkRoleGatedPoolLeavesPoolAloneWhenNothingWasDropped(t *testing.T) {
	pool := []AgentPoolEntry{{PaneID: "%11", Placeable: true}}
	agents := []assignAgentInfo{{pane: tmux.Pane{ID: "%11"}}}
	got := markRoleGatedPool(pool, agents, agents, "impl")
	if !got[0].Placeable || got[0].Reason != "" {
		t.Fatalf("unchanged pool was mutated: %+v", got[0])
	}
}

// A pane already excluded for another reason must keep that reason; role gating
// only explains panes that cleared dispatch.
func TestMarkRoleGatedPoolKeepsTheEarlierReason(t *testing.T) {
	pool := []AgentPoolEntry{
		{PaneID: "%11", Placeable: false, Reason: AgentPoolReasonStateNotIdle},
		{PaneID: "%12", Placeable: true},
	}
	before := []assignAgentInfo{{pane: tmux.Pane{ID: "%12"}}}
	after := []assignAgentInfo{}
	got := markRoleGatedPool(pool, before, after, "")
	if got[0].Reason != AgentPoolReasonStateNotIdle {
		t.Fatalf("%%11 reason = %q, want it preserved", got[0].Reason)
	}
	if got[1].Reason != AgentPoolReasonRoleIneligible {
		t.Fatalf("%%12 reason = %q, want %q", got[1].Reason, AgentPoolReasonRoleIneligible)
	}
}

// The zero-placeable path must name the gate per pane. This is the whole point
// of the change: "no idle agents" reads as "the seats are busy", which is only
// one of the reasons a pane can be excluded.
func TestDisplayNamesEveryExclusionGate(t *testing.T) {
	out := &AssignOutputEnhanced{
		Strategy: "balanced",
		AgentPool: []AgentPoolEntry{
			{PaneID: "%11", AgentType: "claude", Placeable: false, Reason: AgentPoolReasonHoldsActiveAssignment},
			{PaneID: "%12", AgentType: "claude", State: "working", Confidence: 0.95, Placeable: false, Reason: AgentPoolReasonStateNotIdle},
		},
		Summary: AssignSummaryEnhanced{IdleAgents: 0, ActionableCount: 41},
	}
	fetch := captureStdoutForTest(t)
	displayAssignOutputEnhanced(out, false)
	rendered := fetch()

	if strings.Contains(rendered, "No idle agents available") {
		t.Fatal("output still says 'No idle agents available', which misreports non-busy exclusions")
	}
	for _, want := range []string{"%11", "%12", AgentPoolReasonHoldsActiveAssignment, AgentPoolReasonStateNotIdle} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("output is missing %q:\n%s", want, rendered)
		}
	}
}

func TestPrintAgentPoolReportsAnEmptyPool(t *testing.T) {
	fetch := captureStdoutForTest(t)
	printAgentPool(nil, lipgloss.NewStyle())
	rendered := fetch()
	if !strings.Contains(rendered, "no agent panes found") {
		t.Fatalf("empty pool was not explained:\n%s", rendered)
	}
}
