package status

import (
	"testing"
	"time"
)

func TestRecoveryManager_CanSendRecovery(t *testing.T) {
	rm := NewRecoveryManagerDefault()

	// First recovery should be allowed
	can, reason := rm.CanSendRecovery("test:0")
	if !can {
		t.Errorf("first recovery should be allowed, got: %s", reason)
	}

	// Simulate a recovery
	rm.mu.Lock()
	rm.lastRecovery["test:0"] = time.Now()
	rm.mu.Unlock()

	// Second immediate recovery should be blocked by cooldown
	can, reason = rm.CanSendRecovery("test:0")
	if can {
		t.Error("recovery should be blocked by cooldown")
	}
	if reason == "" {
		t.Error("reason should explain cooldown")
	}
}

func TestRecoveryManager_MaxRecoveries(t *testing.T) {
	config := RecoveryConfig{
		Cooldown:      1 * time.Millisecond, // Fast cooldown for testing
		Prompt:        "test prompt",
		MaxRecoveries: 3,
	}
	rm := NewRecoveryManager(config)

	// Simulate max recoveries
	rm.mu.Lock()
	rm.recoveryCount["test:0"] = 3
	rm.mu.Unlock()

	can, reason := rm.CanSendRecovery("test:0")
	if can {
		t.Error("recovery should be blocked by max recoveries")
	}
	if reason == "" {
		t.Error("reason should explain max recoveries")
	}
}

func TestRecoveryManager_ResetPane(t *testing.T) {
	rm := NewRecoveryManagerDefault()

	// Set some state
	rm.mu.Lock()
	rm.lastRecovery["test:0"] = time.Now()
	rm.recoveryCount["test:0"] = 5
	rm.mu.Unlock()

	// Reset
	rm.ResetPane("test:0")

	// Should be allowed again
	can, _ := rm.CanSendRecovery("test:0")
	if !can {
		t.Error("recovery should be allowed after reset")
	}

	count := rm.GetRecoveryCount("test:0")
	if count != 0 {
		t.Errorf("count should be 0 after reset, got %d", count)
	}
}

func TestRecoveryManager_GetRecoveryCount(t *testing.T) {
	rm := NewRecoveryManagerDefault()

	// Initial count should be 0
	count := rm.GetRecoveryCount("test:0")
	if count != 0 {
		t.Errorf("initial count should be 0, got %d", count)
	}

	// Simulate recoveries
	rm.mu.Lock()
	rm.recoveryCount["test:0"] = 3
	rm.mu.Unlock()

	count = rm.GetRecoveryCount("test:0")
	if count != 3 {
		t.Errorf("count should be 3, got %d", count)
	}
}

func TestRecoveryManager_GetLastRecoveryTime(t *testing.T) {
	rm := NewRecoveryManagerDefault()

	// No recovery yet
	_, ok := rm.GetLastRecoveryTime("test:0")
	if ok {
		t.Error("should not have last recovery time yet")
	}

	// Set recovery time
	now := time.Now()
	rm.mu.Lock()
	rm.lastRecovery["test:0"] = now
	rm.mu.Unlock()

	lastTime, ok := rm.GetLastRecoveryTime("test:0")
	if !ok {
		t.Error("should have last recovery time")
	}
	if !lastTime.Equal(now) {
		t.Errorf("last time should match, got %v want %v", lastTime, now)
	}
}

func TestRecoveryManager_SetPrompt(t *testing.T) {
	rm := NewRecoveryManagerDefault()

	rm.SetPrompt("custom prompt")
	if rm.prompt != "custom prompt" {
		t.Errorf("prompt should be 'custom prompt', got %q", rm.prompt)
	}
}

func TestRecoveryManager_SetCooldown(t *testing.T) {
	rm := NewRecoveryManagerDefault()

	rm.SetCooldown(5 * time.Minute)
	if rm.cooldown != 5*time.Minute {
		t.Errorf("cooldown should be 5m, got %v", rm.cooldown)
	}
}

func TestRecoveryManager_HandleCompactionEvent(t *testing.T) {
	config := RecoveryConfig{
		Cooldown:      1 * time.Second,
		MaxRecoveries: 5,
	}
	rm := NewRecoveryManager(config)

	event := &CompactionEvent{
		AgentType:   "claude",
		MatchedText: "Conversation compacted",
		DetectedAt:  time.Now(),
	}

	// Note: This won't actually send keys since tmux isn't running in tests
	// It will fail with "failed to send recovery prompt"
	// but the logic should work
	_, err := rm.HandleCompactionEvent(event, "testsession", 0)

	// We expect an error because tmux isn't available in tests
	if err == nil {
		t.Log("HandleCompactionEvent succeeded (tmux available)")
	} else {
		t.Logf("HandleCompactionEvent failed as expected without tmux: %v", err)
	}

	// Test with nil event
	sent, err := rm.HandleCompactionEvent(nil, "testsession", 0)
	if sent {
		t.Error("should not send for nil event")
	}
	if err != nil {
		t.Error("should not error for nil event")
	}
}

func TestRecoveryEvent(t *testing.T) {
	event := RecoveryEvent{
		PaneID:      "test:0",
		Session:     "test",
		PaneIndex:   0,
		SentAt:      time.Now(),
		Prompt:      "test prompt",
		TriggerText: "Conversation compacted",
	}

	if event.PaneID != "test:0" {
		t.Errorf("PaneID should be test:0, got %s", event.PaneID)
	}
	if event.TriggerText != "Conversation compacted" {
		t.Errorf("TriggerText should be set")
	}
}

func TestDefaultRecoveryConfig(t *testing.T) {
	config := DefaultRecoveryConfig()

	if config.Cooldown != DefaultCooldown {
		t.Errorf("Cooldown should be %v, got %v", DefaultCooldown, config.Cooldown)
	}
	if config.Prompt != DefaultRecoveryPrompt {
		t.Errorf("Prompt should be default")
	}
	if config.MaxRecoveries != DefaultMaxRecoveriesPerPane {
		t.Errorf("MaxRecoveries should be %d, got %d", DefaultMaxRecoveriesPerPane, config.MaxRecoveries)
	}
}

func TestCompactionRecoveryIntegration(t *testing.T) {
	cri := NewCompactionRecoveryIntegrationDefault()

	if cri.Detector() == nil {
		t.Error("detector should not be nil")
	}
	if cri.Recovery() == nil {
		t.Error("recovery should not be nil")
	}
}

func TestCompactionRecoveryIntegration_CheckAndRecover_NoCompaction(t *testing.T) {
	cri := NewCompactionRecoveryIntegrationDefault()

	event, sent, err := cri.CheckAndRecover("normal output", "claude", "test", 0)
	if event != nil {
		t.Error("should not detect compaction in normal output")
	}
	if sent {
		t.Error("should not send recovery")
	}
	if err != nil {
		t.Errorf("should not error: %v", err)
	}
}

func TestMakePaneID(t *testing.T) {
	id := makePaneID("mysession", 5)
	expected := "mysession:5"
	if id != expected {
		t.Errorf("makePaneID = %q, want %q", id, expected)
	}
}

func TestRecoveryManager_GetRecoveryEvents(t *testing.T) {
	rm := NewRecoveryManagerDefault()

	// Initially should be empty
	events := rm.GetRecoveryEvents()
	if len(events) != 0 {
		t.Errorf("initial events should be empty, got %d", len(events))
	}

	// Add some events
	rm.mu.Lock()
	rm.recoveryEvents = []RecoveryEvent{
		{PaneID: "test:0", SentAt: time.Now()},
		{PaneID: "test:1", SentAt: time.Now()},
	}
	rm.mu.Unlock()

	events = rm.GetRecoveryEvents()
	if len(events) != 2 {
		t.Errorf("should have 2 events, got %d", len(events))
	}
}

func TestRecoveryManager_ResetAll(t *testing.T) {
	rm := NewRecoveryManagerDefault()

	// Set some state
	rm.mu.Lock()
	rm.lastRecovery["test:0"] = time.Now()
	rm.lastRecovery["test:1"] = time.Now()
	rm.recoveryCount["test:0"] = 5
	rm.recoveryCount["test:1"] = 3
	rm.recoveryEvents = []RecoveryEvent{
		{PaneID: "test:0", SentAt: time.Now()},
	}
	rm.mu.Unlock()

	// Reset all
	rm.ResetAll()

	// Verify all cleared
	rm.mu.RLock()
	if len(rm.lastRecovery) != 0 {
		t.Errorf("lastRecovery should be empty, got %d entries", len(rm.lastRecovery))
	}
	if len(rm.recoveryCount) != 0 {
		t.Errorf("recoveryCount should be empty, got %d entries", len(rm.recoveryCount))
	}
	if len(rm.recoveryEvents) != 0 {
		t.Errorf("recoveryEvents should be empty, got %d entries", len(rm.recoveryEvents))
	}
	rm.mu.RUnlock()

	// Recovery should be allowed for all panes now
	can, _ := rm.CanSendRecovery("test:0")
	if !can {
		t.Error("recovery should be allowed after ResetAll")
	}
}

func TestRecoveryManager_pruneEvents(t *testing.T) {
	config := RecoveryConfig{
		Cooldown:      30 * time.Second,
		MaxRecoveries: 10,
		MaxEventAge:   1 * time.Minute,
	}
	rm := NewRecoveryManager(config)

	// Add old and new events
	oldTime := time.Now().Add(-2 * time.Minute) // Older than maxEventAge
	newTime := time.Now()

	rm.mu.Lock()
	rm.recoveryEvents = []RecoveryEvent{
		{PaneID: "test:0", SentAt: oldTime}, // Should be pruned
		{PaneID: "test:1", SentAt: newTime}, // Should be kept
	}
	rm.mu.Unlock()

	// GetRecoveryEvents calls pruneEvents internally
	events := rm.GetRecoveryEvents()

	// Only the new event should remain
	if len(events) != 1 {
		t.Errorf("should have 1 event after pruning, got %d", len(events))
	}
	if len(events) > 0 && events[0].PaneID != "test:1" {
		t.Errorf("remaining event should be test:1, got %s", events[0].PaneID)
	}
}

func TestRecoveryManager_SendRecoveryPrompt_NoTmux(t *testing.T) {
	rm := NewRecoveryManagerDefault()

	// This will fail since we're not in a real tmux session
	// but we're testing the code path
	sent, err := rm.SendRecoveryPrompt("fake_session", 999)

	// Should fail - either tmux not available or session doesn't exist
	if err == nil && sent {
		t.Log("SendRecoveryPrompt succeeded (tmux available)")
	} else if err != nil {
		t.Logf("SendRecoveryPrompt failed as expected: %v", err)
	} else {
		t.Log("SendRecoveryPrompt returned false (skipped)")
	}
}
