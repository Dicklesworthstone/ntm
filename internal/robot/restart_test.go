package robot

import (
	"context"
	"testing"
	"time"
)

func TestRestartConfig_Defaults(t *testing.T) {
	cfg := DefaultRestartConfig()

	if !cfg.Enabled {
		t.Error("Expected restarts to be enabled by default")
	}
	if cfg.MaxRestartsPerHour != 3 {
		t.Errorf("MaxRestartsPerHour = %d, want 3", cfg.MaxRestartsPerHour)
	}
	if cfg.BackoffBase != 30*time.Second {
		t.Errorf("BackoffBase = %v, want 30s", cfg.BackoffBase)
	}
	if cfg.BackoffMax != 5*time.Minute {
		t.Errorf("BackoffMax = %v, want 5m", cfg.BackoffMax)
	}
	if cfg.SoftRestartTimeout != 10*time.Second {
		t.Errorf("SoftRestartTimeout = %v, want 10s", cfg.SoftRestartTimeout)
	}
	if !cfg.NotifyContextLoss {
		t.Error("Expected NotifyContextLoss to be true by default")
	}
}

func TestNewRestartManager(t *testing.T) {
	manager := NewRestartManager("test-session", nil, nil)

	if manager.session != "test-session" {
		t.Errorf("session = %q, want %q", manager.session, "test-session")
	}
	if !manager.config.Enabled {
		t.Error("Expected config to use defaults")
	}
}

func TestNewRestartManager_WithConfig(t *testing.T) {
	config := &RestartConfig{
		Enabled:            false,
		MaxRestartsPerHour: 5,
		BackoffBase:        1 * time.Minute,
		BackoffMax:         10 * time.Minute,
		SoftRestartTimeout: 30 * time.Second,
		NotifyContextLoss:  false,
	}

	manager := NewRestartManager("test-session", config, nil)

	if manager.config.Enabled {
		t.Error("Expected Enabled to be false")
	}
	if manager.config.MaxRestartsPerHour != 5 {
		t.Errorf("MaxRestartsPerHour = %d, want 5", manager.config.MaxRestartsPerHour)
	}
	if manager.config.BackoffBase != 1*time.Minute {
		t.Errorf("BackoffBase = %v, want 1m", manager.config.BackoffBase)
	}
	if manager.config.BackoffMax != 10*time.Minute {
		t.Errorf("BackoffMax = %v, want 10m", manager.config.BackoffMax)
	}
	if manager.config.SoftRestartTimeout != 30*time.Second {
		t.Errorf("SoftRestartTimeout = %v, want 30s", manager.config.SoftRestartTimeout)
	}
	if manager.config.NotifyContextLoss {
		t.Error("Expected NotifyContextLoss to be false")
	}
}

func TestRestartManager_RestartsInLastHour(t *testing.T) {
	manager := NewRestartManager("test-session", nil, nil)
	paneID := "%1"

	// Initially 0
	count := manager.getRestartsInLastHour(paneID)
	if count != 0 {
		t.Errorf("getRestartsInLastHour = %d, want 0", count)
	}

	// Record some restarts
	manager.recordRestart(paneID)
	manager.recordRestart(paneID)

	count = manager.getRestartsInLastHour(paneID)
	if count != 2 {
		t.Errorf("getRestartsInLastHour = %d, want 2", count)
	}
}

func TestRestartManager_CalculateBackoff(t *testing.T) {
	manager := NewRestartManager("test-session", nil, nil)

	tests := []struct {
		restartCount int
		expected     time.Duration
	}{
		{0, 0},                  // No restarts, no backoff
		{1, 30 * time.Second},   // First restart: base
		{2, 60 * time.Second},   // Second: base * 2
		{3, 120 * time.Second},  // Third: base * 4
		{4, 240 * time.Second},  // Fourth: base * 8
		{5, 300 * time.Second},  // Fifth: capped at max (5m)
		{10, 300 * time.Second}, // Many: still capped
	}

	for _, tt := range tests {
		got := manager.calculateBackoff(tt.restartCount)
		if got != tt.expected {
			t.Errorf("calculateBackoff(%d) = %v, want %v", tt.restartCount, got, tt.expected)
		}
	}
}

func TestRestartManager_TryRestart_Disabled(t *testing.T) {
	config := &RestartConfig{Enabled: false}
	manager := NewRestartManager("test-session", config, nil)

	result := manager.TryRestart(context.Background(), "%1", "claude", HealthUnhealthy)

	if result.Type != RestartNone {
		t.Errorf("Type = %v, want RestartNone", result.Type)
	}
	if result.Reason != "restarts disabled" {
		t.Errorf("Reason = %q, want %q", result.Reason, "restarts disabled")
	}
}

func TestRestartManager_TryRestart_MaxRestartsExceeded(t *testing.T) {
	config := &RestartConfig{
		Enabled:            true,
		MaxRestartsPerHour: 2,
	}
	manager := NewRestartManager("test-session", config, nil)
	paneID := "%1"

	// Record 2 restarts
	manager.recordRestart(paneID)
	manager.recordRestart(paneID)

	result := manager.TryRestart(context.Background(), paneID, "claude", HealthUnhealthy)

	if result.Type != RestartNone {
		t.Errorf("Type = %v, want RestartNone", result.Type)
	}
	if result.Reason == "" {
		t.Error("Expected a reason for max restarts exceeded")
	}
}

func TestRestartManager_GlobalRegistry(t *testing.T) {
	session := "test-global-restart"

	// Clear first
	ClearRestartManager(session)

	// Get manager (should create)
	manager1 := GetRestartManager(session, nil)
	if manager1 == nil {
		t.Fatal("Expected manager to be created")
	}

	// Get again (should be same instance)
	manager2 := GetRestartManager(session, nil)
	if manager1 != manager2 {
		t.Error("Expected same manager instance")
	}

	// Clear manager
	ClearRestartManager(session)

	// Get again (should be new instance)
	manager3 := GetRestartManager(session, nil)
	if manager3 == manager1 {
		t.Error("Expected new manager after clear")
	}
}

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
