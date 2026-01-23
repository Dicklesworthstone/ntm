package swarm

import (
	"testing"
	"time"
)

func TestNewAccountRotator(t *testing.T) {
	rotator := NewAccountRotator()

	if rotator == nil {
		t.Fatal("NewAccountRotator returned nil")
	}

	if rotator.caamPath != "caam" {
		t.Errorf("expected caamPath 'caam', got %q", rotator.caamPath)
	}

	if rotator.CommandTimeout != 5*time.Second {
		t.Errorf("expected CommandTimeout 5s, got %v", rotator.CommandTimeout)
	}

	if rotator.Logger == nil {
		t.Error("expected non-nil Logger")
	}

	if rotator.rotationHistory == nil {
		t.Error("expected rotationHistory to be initialized")
	}
}

func TestAccountRotatorWithMethods(t *testing.T) {
	rotator := NewAccountRotator()

	// Test WithCaamPath
	result := rotator.WithCaamPath("/custom/path/caam")
	if result != rotator {
		t.Error("WithCaamPath should return the same rotator for chaining")
	}
	if rotator.caamPath != "/custom/path/caam" {
		t.Errorf("expected caamPath '/custom/path/caam', got %q", rotator.caamPath)
	}

	// Test WithLogger
	result = rotator.WithLogger(nil)
	if result != rotator {
		t.Error("WithLogger should return the same rotator for chaining")
	}

	// Test WithCommandTimeout
	customTimeout := 10 * time.Second
	result = rotator.WithCommandTimeout(customTimeout)
	if result != rotator {
		t.Error("WithCommandTimeout should return the same rotator for chaining")
	}
	if rotator.CommandTimeout != customTimeout {
		t.Errorf("expected CommandTimeout %v, got %v", customTimeout, rotator.CommandTimeout)
	}
}

func TestNormalizeProvider(t *testing.T) {
	tests := []struct {
		agentType string
		expected  string
	}{
		{"cc", "claude"},
		{"claude", "claude"},
		{"claude-code", "claude"},
		{"cod", "openai"},
		{"codex", "openai"},
		{"gmi", "google"},
		{"gemini", "google"},
		{"unknown", "unknown"},
		{"claude", "claude"},
		{"openai", "openai"},
		{"google", "google"},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			result := normalizeProvider(tt.agentType)
			if result != tt.expected {
				t.Errorf("normalizeProvider(%q) = %q, want %q", tt.agentType, result, tt.expected)
			}
		})
	}
}

func TestAccountRotatorLogger(t *testing.T) {
	rotator := NewAccountRotator()
	logger := rotator.logger()

	if logger == nil {
		t.Error("expected non-nil logger from logger()")
	}
}

func TestAccountRotatorRotationHistory(t *testing.T) {
	rotator := NewAccountRotator()

	// Initially empty
	history := rotator.GetRotationHistory(10)
	if len(history) != 0 {
		t.Errorf("expected empty history, got %d records", len(history))
	}

	if rotator.RotationCount() != 0 {
		t.Errorf("expected 0 rotation count, got %d", rotator.RotationCount())
	}

	// Add some records manually for testing
	rotator.mu.Lock()
	rotator.rotationHistory = append(rotator.rotationHistory, RotationRecord{
		Provider:    "claude",
		FromAccount: "account1",
		ToAccount:   "account2",
		RotatedAt:   time.Now(),
		TriggeredBy: "limit_hit",
	})
	rotator.rotationHistory = append(rotator.rotationHistory, RotationRecord{
		Provider:    "openai",
		FromAccount: "work",
		ToAccount:   "personal",
		RotatedAt:   time.Now(),
		TriggeredBy: "manual",
	})
	rotator.mu.Unlock()

	// Check count
	if rotator.RotationCount() != 2 {
		t.Errorf("expected 2 rotation count, got %d", rotator.RotationCount())
	}

	// Get all history
	history = rotator.GetRotationHistory(10)
	if len(history) != 2 {
		t.Errorf("expected 2 records, got %d", len(history))
	}

	// Get limited history
	history = rotator.GetRotationHistory(1)
	if len(history) != 1 {
		t.Errorf("expected 1 record with limit, got %d", len(history))
	}

	// Clear history
	rotator.ClearRotationHistory()
	if rotator.RotationCount() != 0 {
		t.Errorf("expected 0 after clear, got %d", rotator.RotationCount())
	}
}

func TestAccountRotatorGetRotationHistoryZeroLimit(t *testing.T) {
	rotator := NewAccountRotator()

	// Add a record
	rotator.mu.Lock()
	rotator.rotationHistory = append(rotator.rotationHistory, RotationRecord{
		Provider:    "claude",
		FromAccount: "a",
		ToAccount:   "b",
		RotatedAt:   time.Now(),
	})
	rotator.mu.Unlock()

	// Zero limit should return all
	history := rotator.GetRotationHistory(0)
	if len(history) != 1 {
		t.Errorf("expected all records with 0 limit, got %d", len(history))
	}

	// Negative limit should return all
	history = rotator.GetRotationHistory(-5)
	if len(history) != 1 {
		t.Errorf("expected all records with negative limit, got %d", len(history))
	}
}

func TestAccountRotatorIsAvailableWithInvalidPath(t *testing.T) {
	rotator := NewAccountRotator().WithCaamPath("/nonexistent/path/to/caam")

	// Should return false for invalid path
	if rotator.IsAvailable() {
		t.Error("expected IsAvailable to return false for invalid path")
	}

	// Should cache the result
	if rotator.IsAvailable() {
		t.Error("expected cached result to be false")
	}
}

func TestAccountRotatorResetAvailabilityCheck(t *testing.T) {
	rotator := NewAccountRotator().WithCaamPath("/nonexistent/path/to/caam")

	// Check availability (will be false and cached)
	_ = rotator.IsAvailable()

	// Reset and check internal state
	rotator.ResetAvailabilityCheck()

	if rotator.availabilityChecked {
		t.Error("expected availabilityChecked to be false after reset")
	}
	if rotator.availabilityResult {
		t.Error("expected availabilityResult to be false after reset")
	}
}

func TestAccountRotatorGracefulDegradation(t *testing.T) {
	rotator := NewAccountRotator().WithCaamPath("/nonexistent/path/to/caam")

	// GetCurrentAccount should return error
	_, err := rotator.GetCurrentAccount("cc")
	if err == nil {
		t.Error("expected error when caam is unavailable")
	}

	// ListAccounts should return error
	_, err = rotator.ListAccounts("cc")
	if err == nil {
		t.Error("expected error when caam is unavailable")
	}

	// SwitchAccount should return error
	_, err = rotator.SwitchAccount("cc")
	if err == nil {
		t.Error("expected error when caam is unavailable")
	}

	// SwitchToAccount should return error
	_, err = rotator.SwitchToAccount("cc", "test")
	if err == nil {
		t.Error("expected error when caam is unavailable")
	}

	// RotateAccount should return error
	_, err = rotator.RotateAccount("cc")
	if err == nil {
		t.Error("expected error when caam is unavailable")
	}

	// CurrentAccount should return empty string
	account := rotator.CurrentAccount("cc")
	if account != "" {
		t.Errorf("expected empty account when caam unavailable, got %q", account)
	}
}

func TestAccountInfoFields(t *testing.T) {
	now := time.Now()
	info := AccountInfo{
		Provider:    "claude",
		AccountName: "personal",
		IsActive:    true,
		LastUsed:    now,
	}

	if info.Provider != "claude" {
		t.Errorf("unexpected Provider: %s", info.Provider)
	}
	if info.AccountName != "personal" {
		t.Errorf("unexpected AccountName: %s", info.AccountName)
	}
	if !info.IsActive {
		t.Error("expected IsActive to be true")
	}
	if !info.LastUsed.Equal(now) {
		t.Errorf("unexpected LastUsed: %v", info.LastUsed)
	}
}

func TestRotationRecordFields(t *testing.T) {
	now := time.Now()
	record := RotationRecord{
		Provider:    "openai",
		FromAccount: "work",
		ToAccount:   "personal",
		RotatedAt:   now,
		SessionPane: "test:1.1",
		TriggeredBy: "limit_hit",
	}

	if record.Provider != "openai" {
		t.Errorf("unexpected Provider: %s", record.Provider)
	}
	if record.FromAccount != "work" {
		t.Errorf("unexpected FromAccount: %s", record.FromAccount)
	}
	if record.ToAccount != "personal" {
		t.Errorf("unexpected ToAccount: %s", record.ToAccount)
	}
	if record.SessionPane != "test:1.1" {
		t.Errorf("unexpected SessionPane: %s", record.SessionPane)
	}
	if record.TriggeredBy != "limit_hit" {
		t.Errorf("unexpected TriggeredBy: %s", record.TriggeredBy)
	}
}

func TestAgentToProviderMap(t *testing.T) {
	// Verify the map contains expected entries
	expectedMappings := map[string]string{
		"cc":          "claude",
		"claude":      "claude",
		"claude-code": "claude",
		"cod":         "openai",
		"codex":       "openai",
		"gmi":         "google",
		"gemini":      "google",
	}

	for agent, expected := range expectedMappings {
		if provider, ok := agentToProvider[agent]; !ok {
			t.Errorf("agentToProvider missing entry for %q", agent)
		} else if provider != expected {
			t.Errorf("agentToProvider[%q] = %q, want %q", agent, provider, expected)
		}
	}
}

func TestAccountRotatorImplementsInterface(t *testing.T) {
	// Verify AccountRotator implements the AccountRotator interface used by AutoRespawner
	rotator := NewAccountRotator()

	var _ AccountRotatorI = rotator
}

func TestCaamStatusStruct(t *testing.T) {
	status := caamStatus{
		Provider:      "claude",
		ActiveAccount: "personal",
		AccountCount:  3,
	}

	if status.Provider != "claude" {
		t.Errorf("unexpected Provider: %s", status.Provider)
	}
	if status.ActiveAccount != "personal" {
		t.Errorf("unexpected ActiveAccount: %s", status.ActiveAccount)
	}
	if status.AccountCount != 3 {
		t.Errorf("unexpected AccountCount: %d", status.AccountCount)
	}
}

func TestCaamAccountStruct(t *testing.T) {
	account := caamAccount{
		Name:   "work",
		Active: false,
	}

	if account.Name != "work" {
		t.Errorf("unexpected Name: %s", account.Name)
	}
	if account.Active {
		t.Error("expected Active to be false")
	}
}

func TestRotationStateStruct(t *testing.T) {
	now := time.Now()
	state := RotationState{
		CurrentAccount:   "personal",
		PreviousAccounts: []string{"work", "team"},
		RotationCount:    2,
		LastRotation:     now,
	}

	if state.CurrentAccount != "personal" {
		t.Errorf("unexpected CurrentAccount: %s", state.CurrentAccount)
	}
	if len(state.PreviousAccounts) != 2 {
		t.Errorf("expected 2 previous accounts, got %d", len(state.PreviousAccounts))
	}
	if state.RotationCount != 2 {
		t.Errorf("expected RotationCount 2, got %d", state.RotationCount)
	}
	if !state.LastRotation.Equal(now) {
		t.Errorf("unexpected LastRotation: %v", state.LastRotation)
	}
}

func TestAccountRotatorWithCooldown(t *testing.T) {
	rotator := NewAccountRotator()

	result := rotator.WithCooldown(30 * time.Second)
	if result != rotator {
		t.Error("WithCooldown should return the same rotator for chaining")
	}
	if rotator.CooldownDuration != 30*time.Second {
		t.Errorf("expected CooldownDuration 30s, got %v", rotator.CooldownDuration)
	}
}

func TestAccountRotatorDefaultCooldown(t *testing.T) {
	rotator := NewAccountRotator()

	if rotator.CooldownDuration != 60*time.Second {
		t.Errorf("expected default CooldownDuration 60s, got %v", rotator.CooldownDuration)
	}
}

func TestAccountRotatorRotationStatesInitialized(t *testing.T) {
	rotator := NewAccountRotator()

	if rotator.rotationStates == nil {
		t.Error("expected rotationStates to be initialized")
	}
	if len(rotator.rotationStates) != 0 {
		t.Errorf("expected empty rotationStates, got %d", len(rotator.rotationStates))
	}
}

func TestAccountRotatorGetPaneStateNil(t *testing.T) {
	rotator := NewAccountRotator()

	state := rotator.GetPaneState("nonexistent:1.1")
	if state != nil {
		t.Error("expected nil for non-existent pane state")
	}
}

func TestAccountRotatorGetOrCreateState(t *testing.T) {
	rotator := NewAccountRotator()

	// First call creates new state
	rotator.mu.Lock()
	state := rotator.getOrCreateState("test:1.1")
	rotator.mu.Unlock()

	if state == nil {
		t.Fatal("expected non-nil state")
	}
	if state.CurrentAccount != "" {
		t.Errorf("expected empty CurrentAccount, got %q", state.CurrentAccount)
	}
	if state.PreviousAccounts == nil {
		t.Error("expected PreviousAccounts to be initialized")
	}
	if state.RotationCount != 0 {
		t.Errorf("expected 0 RotationCount, got %d", state.RotationCount)
	}

	// Second call returns same state
	rotator.mu.Lock()
	state2 := rotator.getOrCreateState("test:1.1")
	rotator.mu.Unlock()

	if state2 != state {
		t.Error("expected same state on second call")
	}

	// Different pane gets different state
	rotator.mu.Lock()
	state3 := rotator.getOrCreateState("test:2.2")
	rotator.mu.Unlock()

	if state3 == state {
		t.Error("expected different state for different pane")
	}
}

func TestAccountRotatorIsCooldownActive(t *testing.T) {
	rotator := NewAccountRotator().WithCooldown(1 * time.Second)

	// No rotations yet - cooldown not active
	state := &RotationState{RotationCount: 0}
	if rotator.isCooldownActive(state) {
		t.Error("expected cooldown inactive with 0 rotations")
	}

	// Recent rotation - cooldown active
	state = &RotationState{
		RotationCount: 1,
		LastRotation:  time.Now(),
	}
	if !rotator.isCooldownActive(state) {
		t.Error("expected cooldown active for recent rotation")
	}

	// Old rotation - cooldown not active
	state = &RotationState{
		RotationCount: 1,
		LastRotation:  time.Now().Add(-2 * time.Second),
	}
	if rotator.isCooldownActive(state) {
		t.Error("expected cooldown inactive for old rotation")
	}
}

func TestAccountRotatorOnLimitHitCaamUnavailable(t *testing.T) {
	rotator := NewAccountRotator().WithCaamPath("/nonexistent/caam")

	event := LimitHitEvent{
		SessionPane: "test:1.1",
		AgentType:   "cc",
		Pattern:     "rate limit",
		DetectedAt:  time.Now(),
	}

	record, err := rotator.OnLimitHit(event)
	if err == nil {
		t.Error("expected error when caam is unavailable")
	}
	if record != nil {
		t.Error("expected nil record when caam is unavailable")
	}
}

func TestAccountRotatorOnLimitHitCooldown(t *testing.T) {
	rotator := NewAccountRotator().
		WithCaamPath("/nonexistent/caam").
		WithCooldown(10 * time.Second)

	// Set up state with recent rotation
	rotator.mu.Lock()
	state := rotator.getOrCreateState("test:1.1")
	state.RotationCount = 1
	state.LastRotation = time.Now()
	rotator.mu.Unlock()

	event := LimitHitEvent{
		SessionPane: "test:1.1",
		AgentType:   "cc",
		Pattern:     "rate limit",
		DetectedAt:  time.Now(),
	}

	record, err := rotator.OnLimitHit(event)
	if err == nil {
		t.Error("expected error when cooldown is active")
	}
	if record != nil {
		t.Error("expected nil record when cooldown is active")
	}
	if err != nil && !contains(err.Error(), "cooldown active") {
		t.Errorf("expected cooldown error, got: %v", err)
	}
}

func TestAccountRotatorOnLimitHitCreatesState(t *testing.T) {
	rotator := NewAccountRotator().WithCaamPath("/nonexistent/caam")

	event := LimitHitEvent{
		SessionPane: "newsession:3.3",
		AgentType:   "cod",
		Pattern:     "quota exceeded",
		DetectedAt:  time.Now(),
	}

	// This will fail due to caam unavailable, but state should still be created
	_, _ = rotator.OnLimitHit(event)

	state := rotator.GetPaneState("newsession:3.3")
	if state == nil {
		t.Error("expected state to be created even on failure")
	}
}

func TestAccountRotatorGetPaneStateAfterManualSetup(t *testing.T) {
	rotator := NewAccountRotator()

	// Set up a pane state manually
	rotator.mu.Lock()
	rotator.rotationStates["session:1.1"] = &RotationState{
		CurrentAccount:   "work",
		PreviousAccounts: []string{"personal"},
		RotationCount:    1,
		LastRotation:     time.Now().Add(-5 * time.Minute),
	}
	rotator.mu.Unlock()

	state := rotator.GetPaneState("session:1.1")
	if state == nil {
		t.Fatal("expected non-nil state")
	}
	if state.CurrentAccount != "work" {
		t.Errorf("expected CurrentAccount 'work', got %q", state.CurrentAccount)
	}
	if state.RotationCount != 1 {
		t.Errorf("expected RotationCount 1, got %d", state.RotationCount)
	}
}

func TestAccountRotatorCooldownDurationRespected(t *testing.T) {
	rotator := NewAccountRotator().
		WithCaamPath("/nonexistent/caam").
		WithCooldown(50 * time.Millisecond)

	// Set up state with very recent rotation
	rotator.mu.Lock()
	state := rotator.getOrCreateState("test:1.1")
	state.RotationCount = 1
	state.LastRotation = time.Now()
	rotator.mu.Unlock()

	event := LimitHitEvent{
		SessionPane: "test:1.1",
		AgentType:   "cc",
		Pattern:     "rate limit",
		DetectedAt:  time.Now(),
	}

	// Should be blocked by cooldown
	_, err := rotator.OnLimitHit(event)
	if err == nil || !contains(err.Error(), "cooldown active") {
		t.Errorf("expected cooldown error, got: %v", err)
	}

	// Wait for cooldown to expire
	time.Sleep(60 * time.Millisecond)

	// Should now attempt rotation (will fail due to caam path, but not cooldown)
	_, err = rotator.OnLimitHit(event)
	if err == nil {
		t.Error("expected error (caam unavailable), but not cooldown error")
	}
	if contains(err.Error(), "cooldown active") {
		t.Error("cooldown should have expired")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
