package robot

import (
	"testing"
	"time"
)

func TestBackoffManager_Basic(t *testing.T) {
	manager := NewBackoffManager("test-session")

	// Initially, no backoff
	if manager.IsInBackoff("%1") {
		t.Error("Expected no backoff initially")
	}

	if remaining := manager.GetBackoffRemaining("%1"); remaining != 0 {
		t.Errorf("Expected 0 remaining, got %v", remaining)
	}
}

func TestBackoffManager_RecordRateLimit(t *testing.T) {
	manager := NewBackoffManager("test-session")
	paneID := "%1"

	// First rate limit should return base backoff (30s)
	duration := manager.RecordRateLimit(paneID)
	if duration != BackoffBase {
		t.Errorf("First backoff = %v, want %v", duration, BackoffBase)
	}

	// Should now be in backoff
	if !manager.IsInBackoff(paneID) {
		t.Error("Expected to be in backoff after rate limit")
	}

	// Remaining should be close to 30s
	remaining := manager.GetBackoffRemaining(paneID)
	if remaining < 29*time.Second || remaining > 30*time.Second {
		t.Errorf("Backoff remaining = %v, expected ~30s", remaining)
	}

	// Count should be 1
	if count := manager.GetRateLimitCount(paneID); count != 1 {
		t.Errorf("Rate limit count = %d, want 1", count)
	}
}

func TestBackoffManager_ExponentialBackoff(t *testing.T) {
	manager := NewBackoffManager("test-session")
	paneID := "%1"

	// Record multiple rate limits - should escalate
	durations := []time.Duration{}
	for i := 0; i < 5; i++ {
		d := manager.RecordRateLimit(paneID)
		durations = append(durations, d)
	}

	// Expected: 30s, 60s, 120s, 240s, 300s (capped at 5m)
	expected := []time.Duration{
		30 * time.Second,
		60 * time.Second,
		120 * time.Second,
		240 * time.Second,
		300 * time.Second, // capped at 5 minutes
	}

	for i, exp := range expected {
		if durations[i] != exp {
			t.Errorf("backoff[%d] = %v, want %v", i, durations[i], exp)
		}
	}

	// Count should be 5
	if count := manager.GetRateLimitCount(paneID); count != 5 {
		t.Errorf("Rate limit count = %d, want 5", count)
	}
}

func TestBackoffManager_MaxCap(t *testing.T) {
	manager := NewBackoffManager("test-session")
	paneID := "%1"

	// Record many rate limits - should cap at 5 minutes
	for i := 0; i < 10; i++ {
		manager.RecordRateLimit(paneID)
	}

	// Check the backoff info
	backoff := manager.GetBackoff(paneID)
	if backoff == nil {
		t.Fatal("Expected backoff info")
	}

	// The last backoff duration should be capped at 5 minutes
	remaining := manager.GetBackoffRemaining(paneID)
	if remaining > BackoffMax {
		t.Errorf("Backoff remaining %v exceeds max %v", remaining, BackoffMax)
	}
}

func TestBackoffManager_ClearBackoff(t *testing.T) {
	manager := NewBackoffManager("test-session")
	paneID := "%1"

	manager.RecordRateLimit(paneID)
	if !manager.IsInBackoff(paneID) {
		t.Fatal("Expected to be in backoff")
	}

	manager.ClearBackoff(paneID)
	if manager.IsInBackoff(paneID) {
		t.Error("Expected backoff to be cleared")
	}
}

func TestBackoffManager_ClearExpiredBackoffs(t *testing.T) {
	manager := NewBackoffManager("test-session")

	// Record rate limits for multiple agents
	manager.RecordRateLimit("%1")
	manager.RecordRateLimit("%2")
	manager.RecordRateLimit("%3")

	// All should be in backoff now
	if len(manager.GetAllBackoffs()) != 3 {
		t.Errorf("Expected 3 active backoffs, got %d", len(manager.GetAllBackoffs()))
	}

	// Clear expired (none should be expired yet)
	cleared := manager.ClearExpiredBackoffs()
	if cleared != 0 {
		t.Errorf("Expected 0 cleared, got %d", cleared)
	}
}

func TestBackoffManager_GetAllBackoffs(t *testing.T) {
	manager := NewBackoffManager("test-session")

	manager.RecordRateLimit("%1")
	manager.RecordRateLimit("%2")

	all := manager.GetAllBackoffs()
	if len(all) != 2 {
		t.Errorf("Expected 2 backoffs, got %d", len(all))
	}

	if _, ok := all["%1"]; !ok {
		t.Error("Expected backoff for %1")
	}
	if _, ok := all["%2"]; !ok {
		t.Error("Expected backoff for %2")
	}
}

func TestCalculateBackoffDuration(t *testing.T) {
	tests := []struct {
		count    int
		expected time.Duration
	}{
		{0, 30 * time.Second},
		{1, 60 * time.Second},
		{2, 120 * time.Second},
		{3, 240 * time.Second},
		{4, 300 * time.Second},  // capped
		{5, 300 * time.Second},  // still capped
		{10, 300 * time.Second}, // still capped
	}

	for _, tt := range tests {
		got := calculateBackoffDuration(tt.count)
		if got != tt.expected {
			t.Errorf("calculateBackoffDuration(%d) = %v, want %v", tt.count, got, tt.expected)
		}
	}
}

func TestGlobalBackoffManager(t *testing.T) {
	session := "test-global-session"

	// Get manager (should create)
	manager1 := GetBackoffManager(session)
	if manager1 == nil {
		t.Fatal("Expected manager to be created")
	}

	// Get again (should be same instance)
	manager2 := GetBackoffManager(session)
	if manager1 != manager2 {
		t.Error("Expected same manager instance")
	}

	// Record a rate limit
	manager1.RecordRateLimit("%1")

	// Verify via manager2
	if !manager2.IsInBackoff("%1") {
		t.Error("Expected rate limit to be visible in second reference")
	}

	// Clear manager
	ClearBackoffManager(session)

	// Get again (should be new instance)
	manager3 := GetBackoffManager(session)
	if manager3 == manager1 {
		t.Error("Expected new manager after clear")
	}

	// Old rate limit should be gone
	if manager3.IsInBackoff("%1") {
		t.Error("Expected no backoff in new manager")
	}
}

func TestCheckSendAllowed(t *testing.T) {
	session := "test-send-session"
	paneID := "%test"

	// Clean up first
	ClearBackoffManager(session)

	// Initially allowed
	allowed, remaining, count := CheckSendAllowed(session, paneID)
	if !allowed {
		t.Error("Expected send to be allowed initially")
	}
	if remaining != 0 {
		t.Errorf("Expected 0 remaining, got %v", remaining)
	}
	if count != 0 {
		t.Errorf("Expected 0 count, got %d", count)
	}

	// Record rate limit
	RecordAgentRateLimit(session, paneID)

	// Should not be allowed
	allowed, remaining, count = CheckSendAllowed(session, paneID)
	if allowed {
		t.Error("Expected send to NOT be allowed after rate limit")
	}
	if remaining == 0 {
		t.Error("Expected remaining > 0")
	}
	if count != 1 {
		t.Errorf("Expected count 1, got %d", count)
	}

	// Clear backoff
	ClearAgentBackoff(session, paneID)

	// Should be allowed again
	allowed, _, _ = CheckSendAllowed(session, paneID)
	if !allowed {
		t.Error("Expected send to be allowed after clear")
	}

	// Cleanup
	ClearBackoffManager(session)
}
