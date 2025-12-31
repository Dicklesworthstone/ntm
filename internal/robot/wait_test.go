package robot

import (
	"testing"
	"time"
)

func TestIsValidWaitCondition(t *testing.T) {
	tests := []struct {
		name      string
		condition string
		want      bool
	}{
		{"idle valid", "idle", true},
		{"complete valid", "complete", true},
		{"generating valid", "generating", true},
		{"healthy valid", "healthy", true},
		{"composed valid", "idle,healthy", true},
		{"composed with spaces", "idle, healthy", true},
		{"three conditions", "idle,healthy,complete", true},
		{"invalid condition", "invalid", false},
		{"empty string", "", false},
		{"partial invalid", "idle,invalid", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidWaitCondition(tt.condition)
			if got != tt.want {
				t.Errorf("isValidWaitCondition(%q) = %v, want %v", tt.condition, got, tt.want)
			}
		})
	}
}

func TestDetectAgentTypeFromTitle(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  string
	}{
		{"claude agent", "myproject__cc_1", "cc"},
		{"codex agent", "myproject__cod_2", "cod"},
		{"gemini agent", "myproject__gmi_3", "gmi"},
		{"user pane", "myproject", ""},
		{"empty title", "", ""},
		{"no double underscore", "myproject_cc_1", ""},
		{"with variant", "myproject__cc_1_opus", "cc"},
		{"complex session name", "my-project-2025__cc_1", "cc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectAgentTypeFromTitle(tt.title)
			if got != tt.want {
				t.Errorf("detectAgentTypeFromTitle(%q) = %q, want %q", tt.title, got, tt.want)
			}
		})
	}
}

func TestMeetsSingleWaitCondition(t *testing.T) {
	tests := []struct {
		name      string
		state     AgentState
		condition string
		want      bool
	}{
		{"waiting meets idle", StateWaiting, WaitConditionIdle, true},
		{"generating meets generating", StateGenerating, WaitConditionGenerating, true},
		{"waiting meets healthy", StateWaiting, WaitConditionHealthy, true},
		{"thinking meets healthy", StateThinking, WaitConditionHealthy, true},
		{"generating meets healthy", StateGenerating, WaitConditionHealthy, true},
		{"unknown meets healthy", StateUnknown, WaitConditionHealthy, true},
		{"error does not meet healthy", StateError, WaitConditionHealthy, false},
		{"stalled does not meet healthy", StateStalled, WaitConditionHealthy, false},
		{"generating does not meet idle", StateGenerating, WaitConditionIdle, false},
		{"thinking does not meet idle", StateThinking, WaitConditionIdle, false},
		{"unknown does not meet idle", StateUnknown, WaitConditionIdle, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			activity := &AgentActivity{
				State: tt.state,
			}
			got := meetsSingleWaitCondition(activity, tt.condition)
			if got != tt.want {
				t.Errorf("meetsSingleWaitCondition(state=%s, condition=%s) = %v, want %v",
					tt.state, tt.condition, got, tt.want)
			}
		})
	}
}

func TestMeetsAllWaitConditions(t *testing.T) {
	tests := []struct {
		name       string
		state      AgentState
		conditions []string
		want       bool
	}{
		{"single condition met", StateWaiting, []string{"idle"}, true},
		{"single condition not met", StateGenerating, []string{"idle"}, false},
		{"both conditions met", StateWaiting, []string{"idle", "healthy"}, true},
		{"first met second not", StateError, []string{"healthy"}, false},
		{"empty conditions", StateWaiting, []string{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			activity := &AgentActivity{
				State: tt.state,
			}
			got := meetsAllWaitConditions(activity, tt.conditions)
			if got != tt.want {
				t.Errorf("meetsAllWaitConditions(state=%s, conditions=%v) = %v, want %v",
					tt.state, tt.conditions, got, tt.want)
			}
		})
	}
}

func TestCheckWaitConditionMet_AllMode(t *testing.T) {
	// Test with default (ALL) mode
	opts := WaitOptions{
		Condition:  "idle",
		WaitForAny: false,
	}

	t.Run("all agents idle", func(t *testing.T) {
		activities := []*AgentActivity{
			{PaneID: "test__cc_1", State: StateWaiting},
			{PaneID: "test__cc_2", State: StateWaiting},
		}
		met, matching, pending := checkWaitConditionMet(activities, opts)
		if !met {
			t.Error("Expected condition to be met when all agents are idle")
		}
		if len(matching) != 2 {
			t.Errorf("Expected 2 matching agents, got %d", len(matching))
		}
		if len(pending) != 0 {
			t.Errorf("Expected 0 pending agents, got %d", len(pending))
		}
	})

	t.Run("some agents not idle", func(t *testing.T) {
		activities := []*AgentActivity{
			{PaneID: "test__cc_1", State: StateWaiting},
			{PaneID: "test__cc_2", State: StateGenerating},
		}
		met, matching, pending := checkWaitConditionMet(activities, opts)
		if met {
			t.Error("Expected condition not to be met when some agents are generating")
		}
		if len(matching) != 1 {
			t.Errorf("Expected 1 matching agent, got %d", len(matching))
		}
		if len(pending) != 1 {
			t.Errorf("Expected 1 pending agent, got %d", len(pending))
		}
	})

	t.Run("no agents", func(t *testing.T) {
		activities := []*AgentActivity{}
		met, _, _ := checkWaitConditionMet(activities, opts)
		if met {
			t.Error("Expected condition not to be met with no agents")
		}
	})
}

func TestCheckWaitConditionMet_AnyMode(t *testing.T) {
	// Test with ANY mode
	opts := WaitOptions{
		Condition:  "idle",
		WaitForAny: true,
		CountN:     1,
	}

	t.Run("one agent idle", func(t *testing.T) {
		activities := []*AgentActivity{
			{PaneID: "test__cc_1", State: StateWaiting},
			{PaneID: "test__cc_2", State: StateGenerating},
		}
		met, matching, _ := checkWaitConditionMet(activities, opts)
		if !met {
			t.Error("Expected condition to be met when at least one agent is idle")
		}
		if len(matching) != 1 {
			t.Errorf("Expected 1 matching agent, got %d", len(matching))
		}
	})

	t.Run("no agents idle", func(t *testing.T) {
		activities := []*AgentActivity{
			{PaneID: "test__cc_1", State: StateGenerating},
			{PaneID: "test__cc_2", State: StateGenerating},
		}
		met, _, _ := checkWaitConditionMet(activities, opts)
		if met {
			t.Error("Expected condition not to be met when no agents are idle")
		}
	})

	t.Run("count N requirement", func(t *testing.T) {
		opts := WaitOptions{
			Condition:  "idle",
			WaitForAny: true,
			CountN:     2,
		}
		activities := []*AgentActivity{
			{PaneID: "test__cc_1", State: StateWaiting},
			{PaneID: "test__cc_2", State: StateGenerating},
			{PaneID: "test__cc_3", State: StateWaiting},
		}
		met, matching, _ := checkWaitConditionMet(activities, opts)
		if !met {
			t.Error("Expected condition to be met when 2 agents are idle and CountN=2")
		}
		if len(matching) != 2 {
			t.Errorf("Expected 2 matching agents, got %d", len(matching))
		}
	})
}

func TestCompleteCondition(t *testing.T) {
	t.Run("waiting with no recent output", func(t *testing.T) {
		activity := &AgentActivity{
			State:      StateWaiting,
			LastOutput: time.Time{}, // Zero time - no output recorded
		}
		got := meetsSingleWaitCondition(activity, WaitConditionComplete)
		if !got {
			t.Error("Expected 'complete' condition to be met for waiting agent with no output")
		}
	})

	t.Run("waiting with recent output", func(t *testing.T) {
		activity := &AgentActivity{
			State:      StateWaiting,
			LastOutput: time.Now(), // Just now
		}
		got := meetsSingleWaitCondition(activity, WaitConditionComplete)
		if got {
			t.Error("Expected 'complete' condition not to be met for waiting agent with recent output")
		}
	})

	t.Run("waiting with old output", func(t *testing.T) {
		activity := &AgentActivity{
			State:      StateWaiting,
			LastOutput: time.Now().Add(-10 * time.Second), // 10 seconds ago
		}
		got := meetsSingleWaitCondition(activity, WaitConditionComplete)
		if !got {
			t.Error("Expected 'complete' condition to be met for waiting agent with old output")
		}
	})

	t.Run("generating does not meet complete", func(t *testing.T) {
		activity := &AgentActivity{
			State: StateGenerating,
		}
		got := meetsSingleWaitCondition(activity, WaitConditionComplete)
		if got {
			t.Error("Expected 'complete' condition not to be met for generating agent")
		}
	})
}

func TestWaitConditionConstants(t *testing.T) {
	// Ensure condition constants have expected string values
	if WaitConditionIdle != "idle" {
		t.Errorf("WaitConditionIdle = %q, want %q", WaitConditionIdle, "idle")
	}
	if WaitConditionComplete != "complete" {
		t.Errorf("WaitConditionComplete = %q, want %q", WaitConditionComplete, "complete")
	}
	if WaitConditionGenerating != "generating" {
		t.Errorf("WaitConditionGenerating = %q, want %q", WaitConditionGenerating, "generating")
	}
	if WaitConditionHealthy != "healthy" {
		t.Errorf("WaitConditionHealthy = %q, want %q", WaitConditionHealthy, "healthy")
	}
}

func TestWaitOptionsDefaults(t *testing.T) {
	opts := WaitOptions{
		Session:   "test",
		Condition: "idle",
	}

	// Check that zero values are handled correctly
	if opts.CountN != 0 {
		t.Errorf("Default CountN should be 0, got %d", opts.CountN)
	}
	if opts.WaitForAny {
		t.Error("Default WaitForAny should be false")
	}
	if opts.ExitOnError {
		t.Error("Default ExitOnError should be false")
	}
}
