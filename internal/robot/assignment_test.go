package robot

import (
	"reflect"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
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
		{Key: "%1", Label: "cc_1", Variant: ""},
		{Key: "%2", Label: "cc_2", Variant: "pro"},
	}

	// Expected: cc_2 gets AgentA (pro), cc_1 gets AgentB
	expected := map[string]string{
		"%2": "AgentA",
		"%1": "AgentB",
	}

	mapping := assignAgentsToPanes(panes, agents)

	if !reflect.DeepEqual(mapping, expected) {
		t.Errorf("Assignment mismatch.\nGot: %v\nWant: %v", mapping, expected)
	}
}

func TestResolveAgentsForSessionUsesPaneIDForCustomTitles(t *testing.T) {
	now := time.Now()
	agents := []agentmail.Agent{
		{
			Name:        "BlueLake",
			Program:     "claude-code",
			InceptionTS: agentmail.FlexTime{Time: now.Add(-2 * time.Hour)},
		},
		{
			Name:        "GreenStone",
			Program:     "claude-code",
			InceptionTS: agentmail.FlexTime{Time: now.Add(-1 * time.Hour)},
		},
	}

	panes := []tmux.Pane{
		{ID: "%1", Index: 1, NTMIndex: 0, Type: tmux.AgentClaude, Title: "notes"},
		{ID: "%2", Index: 2, NTMIndex: 0, Type: tmux.AgentClaude, Title: "logs"},
	}

	mapping := resolveAgentsForSession(panes, agents)
	if len(mapping) != 2 {
		t.Fatalf("mapping len = %d, want 2 (%v)", len(mapping), mapping)
	}
	if mapping["%1"] != "BlueLake" {
		t.Fatalf("mapping[%%1] = %q, want %q", mapping["%1"], "BlueLake")
	}
	if mapping["%2"] != "GreenStone" {
		t.Fatalf("mapping[%%2] = %q, want %q", mapping["%2"], "GreenStone")
	}
}

func TestResolveAgentsForSessionSupportsCursorPanes(t *testing.T) {
	now := time.Now()
	agents := []agentmail.Agent{
		{
			Name:        "SilverField",
			Program:     "cursor",
			InceptionTS: agentmail.FlexTime{Time: now.Add(-2 * time.Hour)},
		},
		{
			Name:        "AmberRiver",
			Program:     "cursor",
			InceptionTS: agentmail.FlexTime{Time: now.Add(-1 * time.Hour)},
		},
	}

	panes := []tmux.Pane{
		{ID: "%7", Index: 7, NTMIndex: 1, Type: tmux.AgentCursor, Title: "proj__cursor_1"},
		{ID: "%8", Index: 8, NTMIndex: 2, Type: tmux.AgentCursor, Title: "proj__cursor_2"},
	}

	mapping := resolveAgentsForSession(panes, agents)
	if len(mapping) != 2 {
		t.Fatalf("mapping len = %d, want 2 (%v)", len(mapping), mapping)
	}
	if mapping["%7"] != "SilverField" {
		t.Fatalf("mapping[%%7] = %q, want %q", mapping["%7"], "SilverField")
	}
	if mapping["%8"] != "AmberRiver" {
		t.Fatalf("mapping[%%8] = %q, want %q", mapping["%8"], "AmberRiver")
	}
}

func TestNormalizedProgramTypeSupportsModernAgents(t *testing.T) {
	tests := []struct {
		program string
		want    string
	}{
		{program: "cursor", want: "cursor"},
		{program: "windsurf", want: "windsurf"},
		{program: "aider", want: "aider"},
		{program: "ollama", want: "ollama"},
		{program: "mystery-cli", want: "unknown"},
	}

	for _, tt := range tests {
		if got := normalizedProgramType(tt.program); got != tt.want {
			t.Fatalf("normalizedProgramType(%q) = %q, want %q", tt.program, got, tt.want)
		}
	}
}
