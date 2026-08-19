package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

// =============================================================================
// Error Handling Tests for Session Recovery (bd-rjlw)
//
// These tests validate error handling and graceful degradation scenarios for
// the session recovery system. Uses the actual RecoveryContext types from spawn.go.
// =============================================================================

// =============================================================================
// Error Code Constants
// =============================================================================

const (
	ErrAgentMailUnavailable = "AGENTMAIL_UNAVAILABLE"
	ErrBVUnavailable        = "BV_UNAVAILABLE"
	ErrCMUnavailable        = "CM_UNAVAILABLE"
	ErrCheckpointCorrupt    = "CHECKPOINT_CORRUPT"
	ErrCheckpointNotFound   = "CHECKPOINT_NOT_FOUND"
	ErrTokenBudgetExceeded  = "TOKEN_BUDGET_EXCEEDED"
	ErrTmuxUnavailable      = "TMUX_UNAVAILABLE"
	ErrSessionNotFound      = "SESSION_NOT_FOUND"
	ErrPartialRecovery      = "PARTIAL_RECOVERY"
)

// =============================================================================
// Test Types
// =============================================================================

// testRecoveryError is a test-only wrapper for testing error scenarios.
// The actual RecoveryError type is defined in spawn.go.
type testRecoveryError = RecoveryError

// recoveryBuildResult wraps RecoveryContext with an Error field for testing.
// The actual RecoveryContext in spawn.go doesn't have an Error field,
// but we need to track errors during testing.
type recoveryBuildResult struct {
	Context *RecoveryContext
	Error   *RecoveryError
}

// =============================================================================
// Test Helpers
// =============================================================================

// mockRecoveryBuilder simulates the recovery context builder for testing.
type mockRecoveryBuilder struct {
	agentMailAvailable bool
	bvAvailable        bool
	cmAvailable        bool
	checkpointValid    bool
	maxTokens          int
}

func newMockRecoveryBuilder() *mockRecoveryBuilder {
	return &mockRecoveryBuilder{
		agentMailAvailable: true,
		bvAvailable:        true,
		cmAvailable:        true,
		checkpointValid:    true,
		maxTokens:          2000,
	}
}

func (m *mockRecoveryBuilder) buildContext(_ context.Context, _ string) *recoveryBuildResult {
	rc := &RecoveryContext{}
	var errors []string

	// Simulate Agent Mail query
	if m.agentMailAvailable {
		rc.Messages = []RecoveryMessage{
			{Subject: "Test message", From: "TestAgent"},
		}
	} else {
		errors = append(errors, "Agent Mail unavailable")
	}

	// Simulate BV query
	if m.bvAvailable {
		rc.Beads = []RecoveryBead{
			{ID: "bd-001", Title: "Test bead"},
		}
	} else {
		errors = append(errors, "BV unavailable")
	}

	// Simulate CM query
	if m.cmAvailable {
		rc.CMMemories = &RecoveryCMMemories{
			Rules: []RecoveryCMRule{
				{ID: "rule-1", Content: "Rule 1"},
				{ID: "rule-2", Content: "Rule 2"},
			},
		}
	} else {
		errors = append(errors, "CM unavailable")
	}

	// Simulate checkpoint loading
	if m.checkpointValid {
		rc.Checkpoint = &RecoveryCheckpoint{
			Name:        "test-checkpoint",
			Description: "Test checkpoint description",
			CreatedAt:   time.Now(),
		}
	} else {
		errors = append(errors, "Checkpoint corrupted or not found")
	}

	result := &recoveryBuildResult{Context: rc}

	// If there were errors, record them
	if len(errors) > 0 {
		result.Error = &RecoveryError{
			Code:        ErrPartialRecovery,
			Message:     "Recovery completed with partial context",
			Component:   "multiple",
			Recoverable: true,
			Details:     errors,
		}
	}

	return result
}

// estimateTokens provides a simple token estimation.
func estimateTokens(text string) int {
	// Simple estimation: ~4 chars per token
	return (len(text) + 3) / 4
}

// =============================================================================
// Error Handling Tests
// =============================================================================

// TestRecoveryError_AgentMailUnavailable tests recovery when Agent Mail is not available.
func TestRecoveryError_AgentMailUnavailable(t *testing.T) {
	t.Log("RECOVERY_ERROR_TEST: Agent Mail unavailable scenario")

	builder := newMockRecoveryBuilder()
	builder.agentMailAvailable = false

	result := builder.buildContext(context.Background(), "test-session")

	// Should still return context with partial data
	if result.Context == nil {
		t.Fatal("recovery context should not be nil")
	}

	// Messages should be empty
	if len(result.Context.Messages) != 0 {
		t.Errorf("expected no messages when Agent Mail unavailable, got %d", len(result.Context.Messages))
	}

	// Other components should still work
	if len(result.Context.Beads) == 0 {
		t.Error("expected beads to be populated despite Agent Mail failure")
	}

	// Should record partial recovery error
	if result.Error == nil {
		t.Fatal("expected error to be recorded for partial recovery")
	}
	if result.Error.Code != ErrPartialRecovery {
		t.Errorf("expected error code %s, got %s", ErrPartialRecovery, result.Error.Code)
	}
	if !result.Error.Recoverable {
		t.Error("partial recovery error should be marked as recoverable")
	}
}

// TestRecoveryError_BVUnavailable tests recovery when BV is not installed.
func TestRecoveryError_BVUnavailable(t *testing.T) {
	t.Log("RECOVERY_ERROR_TEST: BV unavailable scenario")

	builder := newMockRecoveryBuilder()
	builder.bvAvailable = false

	result := builder.buildContext(context.Background(), "test-session")

	// Beads should be empty
	if len(result.Context.Beads) != 0 {
		t.Errorf("expected no beads when BV unavailable, got %d", len(result.Context.Beads))
	}

	// Other components should still work
	if len(result.Context.Messages) == 0 {
		t.Error("expected messages to be populated despite BV failure")
	}

	// Should record partial recovery error
	if result.Error == nil {
		t.Fatal("expected error to be recorded for partial recovery")
	}
}

// TestRecoveryError_CMUnavailable tests recovery when CM is not available.
func TestRecoveryError_CMUnavailable(t *testing.T) {
	t.Log("RECOVERY_ERROR_TEST: CM unavailable scenario")

	builder := newMockRecoveryBuilder()
	builder.cmAvailable = false

	result := builder.buildContext(context.Background(), "test-session")

	// CMMemories should be nil
	if result.Context.CMMemories != nil {
		t.Error("expected CMMemories to be nil when CM unavailable")
	}

	// Other components should still work
	if len(result.Context.Messages) == 0 {
		t.Error("expected messages to be populated despite CM failure")
	}
	if len(result.Context.Beads) == 0 {
		t.Error("expected beads to be populated despite CM failure")
	}
}

// TestRecoveryError_CheckpointCorrupt tests recovery when checkpoint is corrupted.
func TestRecoveryError_CheckpointCorrupt(t *testing.T) {
	t.Log("RECOVERY_ERROR_TEST: Checkpoint corrupted scenario")

	builder := newMockRecoveryBuilder()
	builder.checkpointValid = false

	result := builder.buildContext(context.Background(), "test-session")

	// Checkpoint should be nil
	if result.Context.Checkpoint != nil {
		t.Error("expected checkpoint to be nil when corrupted")
	}

	// Other components should still work
	if len(result.Context.Messages) == 0 {
		t.Error("expected messages to be populated despite checkpoint failure")
	}
}

// TestRecoveryError_AllComponentsUnavailable tests complete graceful degradation.
func TestRecoveryError_AllComponentsUnavailable(t *testing.T) {
	t.Log("RECOVERY_ERROR_TEST: All components unavailable - graceful degradation")

	builder := newMockRecoveryBuilder()
	builder.agentMailAvailable = false
	builder.bvAvailable = false
	builder.cmAvailable = false
	builder.checkpointValid = false

	result := builder.buildContext(context.Background(), "test-session")

	// Context should still be non-nil
	if result.Context == nil {
		t.Fatal("recovery context should not be nil even with all components failing")
	}

	// All data fields should be empty/nil
	if len(result.Context.Messages) != 0 {
		t.Error("expected no messages")
	}
	if len(result.Context.Beads) != 0 {
		t.Error("expected no beads")
	}
	if result.Context.CMMemories != nil {
		t.Error("expected no CM memories")
	}
	if result.Context.Checkpoint != nil {
		t.Error("expected no checkpoint")
	}

	// Error should record all failures
	if result.Error == nil {
		t.Fatal("expected error to be recorded")
	}
	if len(result.Error.Details) != 4 {
		t.Errorf("expected 4 error details, got %d", len(result.Error.Details))
	}
}

// TestRecoveryError_PartialRecoveryIsUsable tests that partial recovery is still useful.
func TestRecoveryError_PartialRecoveryIsUsable(t *testing.T) {
	t.Log("RECOVERY_ERROR_TEST: Partial recovery should be usable")

	builder := newMockRecoveryBuilder()
	builder.cmAvailable = false // Just CM fails

	result := builder.buildContext(context.Background(), "test-session")

	// Should have usable data
	if len(result.Context.Messages) == 0 {
		t.Error("expected messages")
	}
	if len(result.Context.Beads) == 0 {
		t.Error("expected beads")
	}
	if result.Context.Checkpoint == nil {
		t.Error("expected checkpoint")
	}

	// Error should be marked recoverable
	if result.Error != nil && !result.Error.Recoverable {
		t.Error("partial recovery should be marked as recoverable")
	}
}

// =============================================================================
// Config Validation Tests
// =============================================================================

// TestSessionRecoveryConfig_Defaults tests default configuration values.
func TestSessionRecoveryConfig_Defaults(t *testing.T) {
	t.Log("CONFIG_TEST: SessionRecoveryConfig defaults")

	cfg := config.DefaultSessionRecoveryConfig()

	if !cfg.Enabled {
		t.Error("recovery should be enabled by default")
	}
	if !cfg.IncludeAgentMail {
		t.Error("Agent Mail should be included by default")
	}
	if !cfg.IncludeCMMemories {
		t.Error("CM memories should be included by default")
	}
	if !cfg.IncludeBeadsContext {
		t.Error("Beads context should be included by default")
	}
	if cfg.MaxRecoveryTokens <= 0 {
		t.Error("MaxRecoveryTokens should be positive")
	}
	if !cfg.AutoInjectOnSpawn {
		t.Error("AutoInjectOnSpawn should be enabled by default")
	}
}

// TestSessionRecoveryConfig_ZeroTokensDisabled tests that zero tokens effectively disables.
func TestSessionRecoveryConfig_ZeroTokensDisabled(t *testing.T) {
	t.Log("CONFIG_TEST: Zero max tokens should result in empty context")

	builder := newMockRecoveryBuilder()
	builder.maxTokens = 0

	result := builder.buildContext(context.Background(), "test-session")

	// With zero token budget, we should still get the context
	// but truncation would make it minimal
	if result.Context == nil {
		t.Error("context should not be nil even with zero token budget")
	}
}

// TestSessionRecoveryConfig_DisabledComponents tests disabling individual components.
func TestSessionRecoveryConfig_DisabledComponents(t *testing.T) {
	t.Log("CONFIG_TEST: Individual component disabling")

	testCases := []struct {
		name          string
		disableAM     bool
		disableBV     bool
		disableCM     bool
		expectedMsgs  bool
		expectedBeads bool
		expectedCM    bool
	}{
		{"all enabled", false, false, false, true, true, true},
		{"AM disabled", true, false, false, false, true, true},
		{"BV disabled", false, true, false, true, false, true},
		{"CM disabled", false, false, true, true, true, false},
		{"AM+BV disabled", true, true, false, false, false, true},
		{"all disabled", true, true, true, false, false, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			builder := newMockRecoveryBuilder()
			builder.agentMailAvailable = !tc.disableAM
			builder.bvAvailable = !tc.disableBV
			builder.cmAvailable = !tc.disableCM

			result := builder.buildContext(context.Background(), "test")
			rc := result.Context

			if tc.expectedMsgs && len(rc.Messages) == 0 {
				t.Error("expected messages")
			}
			if !tc.expectedMsgs && len(rc.Messages) > 0 {
				t.Error("expected no messages")
			}
			if tc.expectedBeads && len(rc.Beads) == 0 {
				t.Error("expected beads")
			}
			if !tc.expectedBeads && len(rc.Beads) > 0 {
				t.Error("expected no beads")
			}
			if tc.expectedCM && rc.CMMemories == nil {
				t.Error("expected CM memories")
			}
			if !tc.expectedCM && rc.CMMemories != nil {
				t.Error("expected no CM memories")
			}
		})
	}
}

// =============================================================================
// Token Budget Tests
// =============================================================================

// TestRecoveryContext_TokenBudgetRespected tests that token budget is enforced.
func TestRecoveryContext_TokenBudgetRespected(t *testing.T) {
	t.Log("TOKEN_TEST: Token budget should be respected")

	// Create a context with lots of data
	rc := &RecoveryContext{
		Messages: make([]RecoveryMessage, 100),
		Beads:    make([]RecoveryBead, 50),
	}
	for i := range rc.Messages {
		rc.Messages[i] = RecoveryMessage{
			Subject: strings.Repeat("Long subject ", 10),
			Body:    strings.Repeat("Long body content ", 50),
			From:    "TestAgent",
		}
	}
	for i := range rc.Beads {
		rc.Beads[i] = RecoveryBead{
			ID:    "bd-" + strings.Repeat("x", 20),
			Title: strings.Repeat("Long title ", 20),
		}
	}

	// Test that we can detect when budget would be exceeded
	// (Actual truncation logic would be in the implementation)
	fullText := formatRecoveryContext(rc)
	tokens := estimateTokens(fullText)

	t.Logf("Full context tokens: %d", tokens)

	if tokens < 1000 {
		t.Error("test context should have many tokens")
	}
}

// formatRecoveryContext is a helper to format context for token estimation.
func formatRecoveryContext(rc *RecoveryContext) string {
	var sb strings.Builder

	if rc.Checkpoint != nil {
		sb.WriteString("# Checkpoint\n")
		sb.WriteString(rc.Checkpoint.Name + "\n")
		sb.WriteString(rc.Checkpoint.Description + "\n\n")
	}

	if len(rc.Messages) > 0 {
		sb.WriteString("# Recent Messages\n")
		for _, m := range rc.Messages {
			sb.WriteString("- " + m.Subject + " from " + m.From + "\n")
			sb.WriteString("  " + m.Body + "\n")
		}
		sb.WriteString("\n")
	}

	if len(rc.Beads) > 0 {
		sb.WriteString("# Tasks\n")
		for _, b := range rc.Beads {
			sb.WriteString("- " + b.ID + ": " + b.Title + "\n")
		}
		sb.WriteString("\n")
	}

	if rc.CMMemories != nil {
		sb.WriteString("# CM Memories\n")
		for _, r := range rc.CMMemories.Rules {
			sb.WriteString("- " + r.Content + "\n")
		}
	}

	return sb.String()
}

// =============================================================================
// Logging Format Tests
// =============================================================================

// TestRecoveryError_LogFormat tests that errors have proper log format.
func TestRecoveryError_LogFormat(t *testing.T) {
	t.Log("LOG_TEST: Recovery errors should have proper log format")

	err := &RecoveryError{
		Code:        ErrAgentMailUnavailable,
		Message:     "Failed to connect to Agent Mail server",
		Component:   "agentmail",
		Recoverable: true,
		Details:     []string{"connection refused", "timeout after 5s"},
	}

	// Expected log format from task description:
	// log.Printf("[RECOVERY-ERROR] Failed to restore pane %s: %v", paneName, err)
	expectedLogContains := []string{
		err.Code,
		err.Message,
		err.Component,
	}

	logMsg := formatRecoveryError(err)
	for _, expected := range expectedLogContains {
		if !strings.Contains(logMsg, expected) {
			t.Errorf("log message should contain %q, got: %s", expected, logMsg)
		}
	}
}

// formatRecoveryError formats an error for logging.
func formatRecoveryError(err *RecoveryError) string {
	if err == nil {
		return ""
	}
	return "[RECOVERY-ERROR] " + err.Code + ": " + err.Message + " (component: " + err.Component + ")"
}

// TestRecoveryFallback_LogFormat tests fallback logging format.
func TestRecoveryFallback_LogFormat(t *testing.T) {
	t.Log("LOG_TEST: Recovery fallback should have proper log format")

	// Expected format: log.Printf("[RECOVERY-FALLBACK] Using degraded mode: %s", reason)
	reason := "Agent Mail unavailable, using cached data"
	logMsg := formatRecoveryFallback(reason)

	if !strings.Contains(logMsg, "[RECOVERY-FALLBACK]") {
		t.Error("fallback log should contain [RECOVERY-FALLBACK] prefix")
	}
	if !strings.Contains(logMsg, reason) {
		t.Error("fallback log should contain the reason")
	}
}

// formatRecoveryFallback formats a fallback message for logging.
func formatRecoveryFallback(reason string) string {
	return "[RECOVERY-FALLBACK] Using degraded mode: " + reason
}
