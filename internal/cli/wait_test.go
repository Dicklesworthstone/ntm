package cli

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func TestIsValidCondition(t *testing.T) {
	tests := []struct {
		name      string
		condition WaitCondition
		want      bool
	}{
		{"idle valid", ConditionIdle, true},
		{"complete valid", ConditionComplete, true},
		{"generating valid", ConditionGenerating, true},
		{"healthy valid", ConditionHealthy, true},
		{"composed valid", "idle,healthy", true},
		{"invalid condition", "invalid", false},
		{"empty string", "", false},
		{"partial invalid", "idle,invalid", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidCondition(tt.condition)
			if got != tt.want {
				t.Errorf("isValidCondition(%q) = %v, want %v", tt.condition, got, tt.want)
			}
		})
	}
}

func TestDetectAgentType(t *testing.T) {
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectAgentType(tt.title)
			if got != tt.want {
				t.Errorf("detectAgentType(%q) = %q, want %q", tt.title, got, tt.want)
			}
		})
	}
}

func TestMeetsSingleCondition(t *testing.T) {
	tests := []struct {
		name      string
		state     robot.AgentState
		condition WaitCondition
		want      bool
	}{
		{"waiting meets idle", robot.StateWaiting, ConditionIdle, true},
		{"generating meets generating", robot.StateGenerating, ConditionGenerating, true},
		{"waiting meets healthy", robot.StateWaiting, ConditionHealthy, true},
		{"thinking meets healthy", robot.StateThinking, ConditionHealthy, true},
		{"generating meets healthy", robot.StateGenerating, ConditionHealthy, true},
		{"error does not meet healthy", robot.StateError, ConditionHealthy, false},
		{"stalled does not meet healthy", robot.StateStalled, ConditionHealthy, false},
		{"generating does not meet idle", robot.StateGenerating, ConditionIdle, false},
		{"thinking does not meet idle", robot.StateThinking, ConditionIdle, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			activity := &robot.AgentActivity{
				State: tt.state,
			}
			got := meetsSingleCondition(activity, tt.condition)
			if got != tt.want {
				t.Errorf("meetsSingleCondition(state=%s, condition=%s) = %v, want %v",
					tt.state, tt.condition, got, tt.want)
			}
		})
	}
}

func TestWaitErrorTypes(t *testing.T) {
	t.Run("WaitTimeoutError", func(t *testing.T) {
		err := &WaitTimeoutError{Duration: 5000000000} // 5s
		if err.ExitCode() != 1 {
			t.Errorf("WaitTimeoutError.ExitCode() = %d, want 1", err.ExitCode())
		}
		if err.Error() == "" {
			t.Error("WaitTimeoutError.Error() should not be empty")
		}
	})

	t.Run("WaitErrorStateError", func(t *testing.T) {
		err := &WaitErrorStateError{Pane: "test__cc_1"}
		if err.ExitCode() != 3 {
			t.Errorf("WaitErrorStateError.ExitCode() = %d, want 3", err.ExitCode())
		}
		if err.Error() == "" {
			t.Error("WaitErrorStateError.Error() should not be empty")
		}
	})
}

func TestWaitConditionConstants(t *testing.T) {
	// Ensure condition constants have expected string values
	if string(ConditionIdle) != "idle" {
		t.Errorf("ConditionIdle = %q, want %q", ConditionIdle, "idle")
	}
	if string(ConditionComplete) != "complete" {
		t.Errorf("ConditionComplete = %q, want %q", ConditionComplete, "complete")
	}
	if string(ConditionGenerating) != "generating" {
		t.Errorf("ConditionGenerating = %q, want %q", ConditionGenerating, "generating")
	}
	if string(ConditionHealthy) != "healthy" {
		t.Errorf("ConditionHealthy = %q, want %q", ConditionHealthy, "healthy")
	}
}
