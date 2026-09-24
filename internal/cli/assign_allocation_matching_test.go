package cli

import (
	"context"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/assign"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

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
