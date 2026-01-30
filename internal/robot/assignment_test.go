package robot

import (
	"reflect"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

func TestAssignAgentsToPanes_VariantPriority(t *testing.T) {
	// Setup agents
	// AgentA: Pro model (older)
	// AgentB: Sonnet model (newer)
	now := time.Now()
	agentA := agentmail.Agent{
		Name:        "AgentA",
		Model:       "pro",
		InceptionTS: agentmail.FlexTime{Time: now.Add(-2 * time.Hour)},
	}
	agentB := agentmail.Agent{
		Name:        "AgentB",
		Model:       "sonnet",
		InceptionTS: agentmail.FlexTime{Time: now.Add(-1 * time.Hour)},
	}
	agents := []agentmail.Agent{agentA, agentB}

	// Setup panes
	// Pane 1: No variant (generic)
	// Pane 2: Variant "pro"
	panes := []ntmPaneInfo{
		{Label: "cc_1", Variant: ""},
		{Label: "cc_2", Variant: "pro"},
	}

	// Expected: cc_2 gets AgentA (pro), cc_1 gets AgentB
	expected := map[string]string{
		"cc_2": "AgentA",
		"cc_1": "AgentB",
	}

	mapping := assignAgentsToPanes(panes, agents)

	if !reflect.DeepEqual(mapping, expected) {
		t.Errorf("Assignment mismatch.\nGot: %v\nWant: %v", mapping, expected)
	}
}
