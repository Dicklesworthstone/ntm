package robot

import (
	"testing"
	"time"
)

func TestRestartResult_Fields(t *testing.T) {
	result := &RestartResult{
		Success:        true,
		Type:           RestartSoft,
		PaneID:         "%1",
		AgentType:      "claude",
		BackoffApplied: 30 * time.Second,
		ContextLost:    false,
		Reason:         "soft restart successful",
		AttemptedAt:    time.Now(),
	}

	if !result.Success {
		t.Error("Expected success")
	}
	if result.Type != RestartSoft {
		t.Errorf("Type = %v, want RestartSoft", result.Type)
	}
	if result.ContextLost {
		t.Error("Expected no context loss for soft restart")
	}
}

func TestRestartTypes(t *testing.T) {
	if RestartSoft != "soft" {
		t.Errorf("RestartSoft = %q, want %q", RestartSoft, "soft")
	}
	if RestartHard != "hard" {
		t.Errorf("RestartHard = %q, want %q", RestartHard, "hard")
	}
	if RestartNone != "none" {
		t.Errorf("RestartNone = %q, want %q", RestartNone, "none")
	}
}
