package integrations

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

func TestNewCoordinator(t *testing.T) {
	coord := NewCoordinator(nil)
	if coord == nil {
		t.Fatal("NewCoordinator returned nil")
	}

	// Verify default config was applied
	if coord.config.ProactiveThreshold != 90.0 {
		t.Errorf("Expected ProactiveThreshold 90.0, got %f", coord.config.ProactiveThreshold)
	}

	if coord.config.AutoRotate != true {
		t.Error("Expected AutoRotate to be true by default")
	}
}

func TestNewCoordinatorWithConfig(t *testing.T) {
	cfg := &CoordinatorConfig{
		CAAMEnabled:        true,
		CautEnabled:        true,
		AutoRotate:         false, // Override default
		ProactiveThreshold: 85.0,
		SwitchCooldown:     10 * time.Minute,
		CheckInterval:      1 * time.Minute,
	}

	coord := NewCoordinator(cfg)
	if coord == nil {
		t.Fatal("NewCoordinator returned nil")
	}

	if coord.config.ProactiveThreshold != 85.0 {
		t.Errorf("Expected ProactiveThreshold 85.0, got %f", coord.config.ProactiveThreshold)
	}

	if coord.config.AutoRotate != false {
		t.Error("Expected AutoRotate to be false")
	}

	if coord.switchCooldown != 10*time.Minute {
		t.Errorf("Expected SwitchCooldown 10m, got %v", coord.switchCooldown)
	}
}

func TestDefaultCoordinatorConfig(t *testing.T) {
	cfg := DefaultCoordinatorConfig()

	if cfg.ProactiveThreshold != 90.0 {
		t.Errorf("Expected ProactiveThreshold 90.0, got %f", cfg.ProactiveThreshold)
	}

	if cfg.SwitchCooldown != 5*time.Minute {
		t.Errorf("Expected SwitchCooldown 5m, got %v", cfg.SwitchCooldown)
	}

	if cfg.CheckInterval != 30*time.Second {
		t.Errorf("Expected CheckInterval 30s, got %v", cfg.CheckInterval)
	}

	if !cfg.AutoRotate {
		t.Error("Expected AutoRotate to be true")
	}
}

func TestCoordinator_InCooldown(t *testing.T) {
	cfg := &CoordinatorConfig{
		SwitchCooldown: 1 * time.Second,
	}
	coord := NewCoordinator(cfg)

	// Provider should not be in cooldown initially
	if coord.inCooldown("claude") {
		t.Error("Provider should not be in cooldown initially")
	}

	// Record a switch
	coord.recordSwitch("claude")

	// Provider should now be in cooldown
	if !coord.inCooldown("claude") {
		t.Error("Provider should be in cooldown after switch")
	}

	// Wait for cooldown to expire
	time.Sleep(1100 * time.Millisecond)

	// Provider should no longer be in cooldown
	if coord.inCooldown("claude") {
		t.Error("Provider should not be in cooldown after cooldown period")
	}
}

func TestCoordinator_ResetCooldown(t *testing.T) {
	cfg := &CoordinatorConfig{
		SwitchCooldown: 1 * time.Hour, // Long cooldown
	}
	coord := NewCoordinator(cfg)

	// Record a switch
	coord.recordSwitch("openai")

	// Should be in cooldown
	if !coord.inCooldown("openai") {
		t.Error("Provider should be in cooldown")
	}

	// Reset cooldown
	coord.ResetCooldown("openai")

	// Should no longer be in cooldown
	if coord.inCooldown("openai") {
		t.Error("Provider should not be in cooldown after reset")
	}
}

func TestCoordinator_GetLastSwitchTime(t *testing.T) {
	coord := NewCoordinator(nil)

	// No switch recorded yet
	_, ok := coord.GetLastSwitchTime("gemini")
	if ok {
		t.Error("Expected no last switch time for untracked provider")
	}

	// Record a switch
	before := time.Now()
	coord.recordSwitch("gemini")
	after := time.Now()

	// Get the switch time
	switchTime, ok := coord.GetLastSwitchTime("gemini")
	if !ok {
		t.Error("Expected last switch time to be recorded")
	}

	if switchTime.Before(before) || switchTime.After(after) {
		t.Error("Switch time should be between before and after")
	}
}

func TestCoordinator_SetSwitchCallback(t *testing.T) {
	coord := NewCoordinator(nil)

	callbackInvoked := false
	coord.SetSwitchCallback(func(event SwitchEvent) {
		callbackInvoked = true
		_ = event // Use the event
	})

	// Verify callback is set (indirectly through lock/unlock)
	coord.mu.RLock()
	hasCallback := coord.onSwitch != nil
	coord.mu.RUnlock()

	if !hasCallback {
		t.Error("Expected callback to be set")
	}

	// callbackInvoked would be set true if callback were invoked
	_ = callbackInvoked
}

func TestCoordinator_OnCautAlert_BelowThreshold(t *testing.T) {
	cfg := &CoordinatorConfig{
		CAAMEnabled:        true,
		CautEnabled:        true,
		AutoRotate:         true,
		ProactiveThreshold: 90.0,
		SwitchCooldown:     5 * time.Minute,
	}
	coord := NewCoordinator(cfg)

	// Track if switch was attempted
	switchAttempted := false
	coord.SetSwitchCallback(func(event SwitchEvent) {
		switchAttempted = true
	})

	// Call with usage below threshold - should not trigger switch
	coord.OnCautAlert("claude", 85.0)

	if switchAttempted {
		t.Error("Should not attempt switch below threshold")
	}
}

func TestCoordinator_OnCautAlert_AutoRotateDisabled(t *testing.T) {
	cfg := &CoordinatorConfig{
		CAAMEnabled:        true,
		CautEnabled:        true,
		AutoRotate:         false, // Disabled
		ProactiveThreshold: 90.0,
	}
	coord := NewCoordinator(cfg)

	switchAttempted := false
	coord.SetSwitchCallback(func(event SwitchEvent) {
		switchAttempted = true
	})

	// Call with high usage but auto-rotate disabled
	coord.OnCautAlert("claude", 95.0)

	if switchAttempted {
		t.Error("Should not attempt switch when auto-rotate disabled")
	}
}

func TestCoordinator_IsRunning(t *testing.T) {
	coord := NewCoordinator(nil)

	if coord.IsRunning() {
		t.Error("Coordinator should not be running initially")
	}
}

func TestSwitchEvent_Fields(t *testing.T) {
	event := SwitchEvent{
		Provider:          "claude",
		UsagePercent:      92.5,
		Reason:            "proactive",
		PreviousAccount:   "account1",
		NewAccount:        "account2",
		Success:           true,
		Timestamp:         time.Now(),
		AccountsRemaining: 3,
	}

	if event.Provider != "claude" {
		t.Errorf("Expected Provider 'claude', got %q", event.Provider)
	}

	if event.UsagePercent != 92.5 {
		t.Errorf("Expected UsagePercent 92.5, got %f", event.UsagePercent)
	}

	if event.Reason != "proactive" {
		t.Errorf("Expected Reason 'proactive', got %q", event.Reason)
	}

	if !event.Success {
		t.Error("Expected Success to be true")
	}

	if event.AccountsRemaining != 3 {
		t.Errorf("Expected AccountsRemaining 3, got %d", event.AccountsRemaining)
	}
}

func TestCoordinatorConfig_Validation(t *testing.T) {
	// Test that config values are reasonable
	cfg := DefaultCoordinatorConfig()

	if cfg.ProactiveThreshold < 0 || cfg.ProactiveThreshold > 100 {
		t.Errorf("ProactiveThreshold should be 0-100, got %f", cfg.ProactiveThreshold)
	}

	if cfg.SwitchCooldown <= 0 {
		t.Error("SwitchCooldown should be positive")
	}

	if cfg.CheckInterval <= 0 {
		t.Error("CheckInterval should be positive")
	}
}

func TestGetGlobalCoordinator(t *testing.T) {
	coord := GetGlobalCoordinator()
	if coord == nil {
		t.Fatal("GetGlobalCoordinator returned nil")
	}

	// Should return the same instance
	coord2 := GetGlobalCoordinator()
	if coord != coord2 {
		t.Error("GetGlobalCoordinator should return same instance")
	}
}

func TestInitGlobalCoordinator(t *testing.T) {
	cfg := &CoordinatorConfig{
		CAAMEnabled:        true,
		CautEnabled:        true,
		AutoRotate:         false,
		ProactiveThreshold: 80.0,
		SwitchCooldown:     2 * time.Minute,
		CheckInterval:      15 * time.Second,
	}

	coord := InitGlobalCoordinator(cfg)
	if coord == nil {
		t.Fatal("InitGlobalCoordinator returned nil")
	}

	if coord.config.ProactiveThreshold != 80.0 {
		t.Errorf("Expected ProactiveThreshold 80.0, got %f", coord.config.ProactiveThreshold)
	}

	if coord.config.AutoRotate != false {
		t.Error("Expected AutoRotate to be false")
	}

	// Verify it replaces the global coordinator
	coord2 := GetGlobalCoordinator()
	// Note: GetGlobalCoordinator uses sync.Once so it won't return our new one
	// unless we reset the once. This test just verifies InitGlobalCoordinator
	// creates a valid coordinator.
	_ = coord2
}

func TestStopGlobalCoordinator_NilSafe(t *testing.T) {
	// StopGlobalCoordinator should be safe to call even if coordinator is nil
	// This tests the nil check path
	StopGlobalCoordinator()
	// No panic = success
}

func TestCoordinator_StopWhenNotRunning(t *testing.T) {
	coord := NewCoordinator(nil)

	// Stop when not running should be a no-op
	coord.Stop()

	// Should still not be running
	if coord.IsRunning() {
		t.Error("Coordinator should not be running")
	}
}

func TestCoordinator_StartWaitsForConcurrentStop(t *testing.T) {
	cfg := &CoordinatorConfig{
		CAAMEnabled: false,
		CautEnabled: false,
	}
	coord := NewCoordinator(cfg)

	coord.mu.Lock()
	coord.running = true
	coord.stopCh = make(chan struct{})
	coord.doneCh = make(chan struct{})
	coord.mu.Unlock()

	stopped := make(chan struct{})
	go func() {
		coord.Stop()
		close(stopped)
	}()

	deadline := time.Now().Add(time.Second)
	for coord.IsRunning() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if coord.IsRunning() {
		t.Fatal("stop did not begin before timeout")
	}

	started := make(chan error, 1)
	go func() {
		started <- coord.Start()
	}()

	select {
	case err := <-started:
		t.Fatalf("Start returned before Stop completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(coord.doneCh)

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not return after done channel closed")
	}

	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("Start returned error after Stop completed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start did not resume after Stop completed")
	}

	if coord.IsRunning() {
		t.Fatal("Coordinator should remain stopped when integrations are disabled")
	}
}

func TestCoordinator_OnCautAlert_InCooldown(t *testing.T) {
	cfg := &CoordinatorConfig{
		CAAMEnabled:        true,
		CautEnabled:        true,
		AutoRotate:         true,
		ProactiveThreshold: 90.0,
		SwitchCooldown:     1 * time.Hour, // Very long cooldown
	}
	coord := NewCoordinator(cfg)

	// Record a switch to put provider in cooldown
	coord.recordSwitch("claude")

	// Track if switch was attempted
	switchAttempted := false
	coord.SetSwitchCallback(func(event SwitchEvent) {
		switchAttempted = true
	})

	// Call with high usage while in cooldown - should not trigger switch
	coord.OnCautAlert("claude", 95.0)

	if switchAttempted {
		t.Error("Should not attempt switch when in cooldown")
	}
}

func TestCoordinator_StartRequiresBothIntegrations(t *testing.T) {
	tests := []struct {
		name        string
		caamEnabled bool
		cautEnabled bool
	}{
		{"CAAM disabled", false, true},
		{"Caut disabled", true, false},
		{"Both disabled", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &CoordinatorConfig{
				CAAMEnabled: tt.caamEnabled,
				CautEnabled: tt.cautEnabled,
			}
			coord := NewCoordinator(cfg)

			// Start should return nil (no error) but coordinator should not be running
			err := coord.Start()
			if err != nil {
				t.Fatalf("Start returned error: %v", err)
			}

			// Coordinator should not be running when integrations are not enabled
			if coord.IsRunning() {
				t.Error("Coordinator should not be running when integrations are disabled")
			}
		})
	}
}

func TestSwitchEvent_Error(t *testing.T) {
	event := SwitchEvent{
		Provider:     "openai",
		UsagePercent: 95.0,
		Reason:       "proactive",
		Success:      false,
		Error:        "connection timeout",
		Timestamp:    time.Now(),
	}

	if event.Success {
		t.Error("Expected Success to be false")
	}

	if event.Error != "connection timeout" {
		t.Errorf("Expected Error 'connection timeout', got %q", event.Error)
	}

	// PreviousAccount and NewAccount should be empty on failure
	if event.PreviousAccount != "" || event.NewAccount != "" {
		t.Error("Expected empty accounts on failed switch")
	}
}

func TestCoordinatorConfig_ZeroValues(t *testing.T) {
	// Test with zero/nil config values
	cfg := &CoordinatorConfig{}

	coord := NewCoordinator(cfg)
	if coord == nil {
		t.Fatal("NewCoordinator returned nil")
	}

	// Zero values should be preserved
	if coord.config.ProactiveThreshold != 0 {
		t.Errorf("Expected ProactiveThreshold 0, got %f", coord.config.ProactiveThreshold)
	}

	if coord.config.SwitchCooldown != 0 {
		t.Errorf("Expected SwitchCooldown 0, got %v", coord.config.SwitchCooldown)
	}
}

func TestCoordinator_MultipleProviders(t *testing.T) {
	cfg := &CoordinatorConfig{
		SwitchCooldown: 1 * time.Hour,
	}
	coord := NewCoordinator(cfg)

	// Record switches for multiple providers
	coord.recordSwitch("claude")
	coord.recordSwitch("openai")
	coord.recordSwitch("gemini")

	// All should be in cooldown
	if !coord.inCooldown("claude") {
		t.Error("claude should be in cooldown")
	}
	if !coord.inCooldown("openai") {
		t.Error("openai should be in cooldown")
	}
	if !coord.inCooldown("gemini") {
		t.Error("gemini should be in cooldown")
	}

	// Reset one
	coord.ResetCooldown("openai")

	// Only openai should be out of cooldown
	if !coord.inCooldown("claude") {
		t.Error("claude should still be in cooldown")
	}
	if coord.inCooldown("openai") {
		t.Error("openai should not be in cooldown after reset")
	}
	if !coord.inCooldown("gemini") {
		t.Error("gemini should still be in cooldown")
	}
}

func TestCoordinator_GetLastSwitchTimeMultipleProviders(t *testing.T) {
	coord := NewCoordinator(nil)

	// Record switches at different times
	coord.recordSwitch("claude")
	time.Sleep(10 * time.Millisecond)
	coord.recordSwitch("openai")

	claudeTime, ok := coord.GetLastSwitchTime("claude")
	if !ok {
		t.Error("Expected claude switch time to be recorded")
	}

	openaiTime, ok := coord.GetLastSwitchTime("openai")
	if !ok {
		t.Error("Expected openai switch time to be recorded")
	}

	// OpenAI switch should be after Claude
	if !openaiTime.After(claudeTime) {
		t.Error("OpenAI switch time should be after Claude")
	}

	// Unknown provider should not have switch time
	_, ok = coord.GetLastSwitchTime("unknown")
	if ok {
		t.Error("Expected no switch time for unknown provider")
	}
}

func TestNewCoordinatorFromConfig(t *testing.T) {
	intCfg := &config.IntegrationsConfig{
		CAAM: config.CAAMConfig{
			Enabled:         true,
			AutoRotate:      true,
			AccountCooldown: 120, // 2 minutes in seconds
		},
		Caut: config.CautConfig{
			Enabled: true,
		},
	}

	coord := NewCoordinatorFromConfig(intCfg)
	if coord == nil {
		t.Fatal("NewCoordinatorFromConfig returned nil")
	}

	if !coord.config.CAAMEnabled {
		t.Error("Expected CAAMEnabled to be true")
	}

	if !coord.config.CautEnabled {
		t.Error("Expected CautEnabled to be true")
	}

	if !coord.config.AutoRotate {
		t.Error("Expected AutoRotate to be true")
	}

	// Cooldown should be converted from seconds to duration
	expectedCooldown := 120 * time.Second
	if coord.config.SwitchCooldown != expectedCooldown {
		t.Errorf("Expected SwitchCooldown %v, got %v", expectedCooldown, coord.config.SwitchCooldown)
	}

	// ProactiveThreshold should be fixed at 90%
	if coord.config.ProactiveThreshold != 90.0 {
		t.Errorf("Expected ProactiveThreshold 90.0, got %f", coord.config.ProactiveThreshold)
	}
}

func TestNewCoordinatorFromConfig_Disabled(t *testing.T) {
	intCfg := &config.IntegrationsConfig{
		CAAM: config.CAAMConfig{
			Enabled:    false,
			AutoRotate: false,
		},
		Caut: config.CautConfig{
			Enabled: false,
		},
	}

	coord := NewCoordinatorFromConfig(intCfg)
	if coord == nil {
		t.Fatal("NewCoordinatorFromConfig returned nil")
	}

	if coord.config.CAAMEnabled {
		t.Error("Expected CAAMEnabled to be false")
	}

	if coord.config.CautEnabled {
		t.Error("Expected CautEnabled to be false")
	}

	if coord.config.AutoRotate {
		t.Error("Expected AutoRotate to be false")
	}
}

func TestStartGlobalCoordinator(t *testing.T) {
	// Initialize with disabled integrations so Start returns quickly
	cfg := &CoordinatorConfig{
		CAAMEnabled: false,
		CautEnabled: false,
	}
	InitGlobalCoordinator(cfg)

	// StartGlobalCoordinator should not return error when integrations are disabled
	err := StartGlobalCoordinator()
	if err != nil {
		t.Errorf("StartGlobalCoordinator returned error: %v", err)
	}

	// Coordinator should not be running when integrations are disabled
	if GetGlobalCoordinator().IsRunning() {
		t.Error("Coordinator should not be running when integrations are disabled")
	}
}
