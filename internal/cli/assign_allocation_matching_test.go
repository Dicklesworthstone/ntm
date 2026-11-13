package cli

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/assign"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestAssignRelaxFlagReachesBalancedPlanningAndJSON(t *testing.T) {
	previousPressure := collectAssignAllocationPressure
	previousRelax, previousStrategy := assignRelax, assignStrategy
	pressure := assign.AllocationPressure{Available: true, Level: "high", AgentHeadroom: 1}
	collectAssignAllocationPressure = func(context.Context) assign.AllocationPressure { return pressure }
	t.Cleanup(func() {
		collectAssignAllocationPressure = previousPressure
		assignRelax, assignStrategy = previousRelax, previousStrategy
	})
	cmd := newAssignCmd()
	if err := cmd.ParseFlags([]string{"--strategy=balanced", "--relax"}); err != nil {
		t.Fatal(err)
	}
	if !assignRelax || assignStrategy != "balanced" {
		t.Fatal("assign flags did not populate command options")
	}
	agents := []assignAgentInfo{{
		pane: tmux.Pane{ID: "%41", Index: 1}, agentType: "codex", state: "idle", resourceHeadroom: 0.01,
	}}
	beads := []bv.BeadPreview{{ID: "low-score", Title: "Implement feature", Priority: "P4"}}
	opts := &AssignCommandOptions{Strategy: assignStrategy}
	strict, strictPlan := generateAssignmentsEnhancedWithPlan(t.Context(), agents, beads, opts, true)
	if len(strict) != 0 || strictPlan == nil {
		t.Fatalf("fixture needs an empty strict plan: items=%+v plan=%+v", strict, strictPlan)
	}
	opts.Relax = assignRelax
	items, plan := generateAssignmentsEnhancedWithPlan(t.Context(), agents, beads, opts, true)
	if len(items) != 1 || plan == nil || len(plan.Recommendations) != 1 || items[0].PaneID != "%41" || items[0].PromptSent {
		t.Fatalf("relaxed command path did not produce one reviewable assignment: items=%+v plan=%+v", items, plan)
	}
	receipt := items[0].Relaxation
	if receipt == nil || receipt.OriginalThreshold != 0.25 || receipt.RelaxedThreshold != 0.125 ||
		receipt.OriginalScore != items[0].Score || receipt.RelaxedScore != items[0].Score ||
		!reflect.DeepEqual(receipt, plan.Recommendations[0].Relaxation) {
		t.Fatalf("assignment item lost planner provenance: item=%+v plan=%+v", items[0], plan)
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []AssignmentItem
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded[0].Relaxation, receipt) {
		t.Fatalf("CLI JSON lost relaxation provenance: %s", encoded)
	}

	for _, tc := range []struct {
		name   string
		change func(*assignAgentInfo)
	}{
		{"working", func(a *assignAgentInfo) { a.state = "working" }},
		{"context full", func(a *assignAgentInfo) { a.contextUsage = 0.95 }},
		{"negative context", func(a *assignAgentInfo) { a.contextUsage = -0.1 }},
		{"NaN context", func(a *assignAgentInfo) { a.contextUsage = math.NaN() }},
		{"infinite context", func(a *assignAgentInfo) { a.contextUsage = math.Inf(1) }},
		{"negative infinite context", func(a *assignAgentInfo) { a.contextUsage = math.Inf(-1) }},
		{"occupied", func(a *assignAgentInfo) { a.activeAssignments = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := agents[0]
			tc.change(&candidate)
			if got, _ := generateAssignmentsEnhancedWithPlan(t.Context(), []assignAgentInfo{candidate}, beads, opts, true); len(got) != 0 {
				t.Fatalf("relaxation bypassed %s: %+v", tc.name, got)
			}
		})
	}
	pressure = assign.AllocationPressure{Available: true, Level: "critical", AgentHeadroom: 0}
	if got, _ := generateAssignmentsEnhancedWithPlan(t.Context(), agents, beads, opts, true); len(got) != 0 {
		t.Fatalf("relaxation bypassed critical resource pressure: %+v", got)
	}
}

// Exercise the planning path used by the assign command, including conversion
// of real pane and bead metadata into the balanced allocation model and back
// into dispatchable assignment items. No pane input is sent by this test.
func TestAssignBalancedPlanningUsesWholeBatchMatching(t *testing.T) {
	previous := collectAssignAllocationPressure
	collectAssignAllocationPressure = func(context.Context) assign.AllocationPressure {
		return assign.AllocationPressure{Available: true, Level: "normal", AgentHeadroom: 2}
	}
	t.Cleanup(func() { collectAssignAllocationPressure = previous })

	agents := []assignAgentInfo{
		{pane: tmux.Pane{ID: "%41", Index: 1}, agentType: "claude", state: "idle", resourceHeadroom: 0.90},
		{pane: tmux.Pane{ID: "%42", Index: 2}, agentType: "codex", state: "idle", resourceHeadroom: 0.90},
	}
	beads := []bv.BeadPreview{
		{ID: "feature", Title: "Implement feature", Priority: "P0"},
		{ID: "bug", Title: "Fix bug", Priority: "P1"},
	}
	// The urgent feature is individually the highest scoring Codex pairing.
	// But Codex's advantage over Claude is larger on the bug. Both tasks fit,
	// so the best total allocation gives the feature to Claude and bug to Codex.
	items, plan := generateAssignmentsEnhancedWithPlan(context.Background(), agents, beads, &AssignCommandOptions{Strategy: "balanced"}, false)
	if plan == nil || len(items) != 2 || plan.Summary.Recommended != 2 {
		t.Fatalf("balanced command path did not return a complete plan: items=%+v plan=%+v", items, plan)
	}
	byBead := make(map[string]AssignmentItem)
	for _, item := range items {
		byBead[item.BeadID] = item
		if item.PromptSent {
			t.Fatal("planning must not report a dispatched prompt")
		}
	}
	if byBead["feature"].AgentType != string(tmux.AgentClaude) || byBead["feature"].PaneID != "%41" {
		t.Fatalf("feature lost its globally selected Claude target: %+v", byBead["feature"])
	}
	if byBead["bug"].AgentType != string(tmux.AgentCodex) || byBead["bug"].PaneID != "%42" {
		t.Fatalf("bug lost its specialist Codex target: %+v", byBead["bug"])
	}
}
