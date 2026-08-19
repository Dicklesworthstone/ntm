package health

import (
	"testing"
	"time"
)

// =============================================================================
// detectActivity edge cases
// =============================================================================

func TestDetectActivity_ZeroTimestampNoPrompt(t *testing.T) {
	t.Parallel()
	// Zero lastActivity + no idle prompt → ActivityUnknown
	got := detectActivity("some random output without prompt", time.Time{}, "unknown")
	if got != ActivityUnknown {
		t.Errorf("detectActivity(no prompt, zero time) = %v, want ActivityUnknown", got)
	}
}

func TestDetectActivity_ZeroTimestampWithPrompt(t *testing.T) {
	t.Parallel()
	// Zero lastActivity + idle prompt → ActivityIdle (prompt detection wins)
	got := detectActivity("> ", time.Time{}, "user")
	if got != ActivityIdle {
		t.Errorf("detectActivity(prompt, zero time) = %v, want ActivityIdle", got)
	}
}

func TestDetectActivity_RecentWithNoPrompt(t *testing.T) {
	t.Parallel()
	got := detectActivity("building code...", time.Now().Add(-30*time.Second), "cc")
	if got != ActivityActive {
		t.Errorf("detectActivity(recent, no prompt) = %v, want ActivityActive", got)
	}
}

func TestDetectActivity_StaleNoPrompt(t *testing.T) {
	t.Parallel()
	got := detectActivity("building code...", time.Now().Add(-10*time.Minute), "cc")
	if got != ActivityStale {
		t.Errorf("detectActivity(stale, no prompt) = %v, want ActivityStale", got)
	}
}

func TestDetectActivity_ExactlyFiveMinutes(t *testing.T) {
	t.Parallel()
	// Exactly at 5-minute boundary should be Stale (>= 5 min)
	got := detectActivity("output", time.Now().Add(-5*time.Minute-time.Second), "user")
	if got != ActivityStale {
		t.Errorf("detectActivity(5min+1s) = %v, want ActivityStale", got)
	}
}

func TestDetectActivity_JustUnderFiveMinutes(t *testing.T) {
	t.Parallel()
	got := detectActivity("output", time.Now().Add(-4*time.Minute-59*time.Second), "user")
	if got != ActivityActive {
		t.Errorf("detectActivity(4m59s) = %v, want ActivityActive", got)
	}
}

// =============================================================================
// detectProcessStatus edge cases
// =============================================================================

// =============================================================================
// detectErrors edge cases
// =============================================================================

func TestDetectErrors_EmptyOutput(t *testing.T) {
	t.Parallel()
	issues := detectErrors("")
	if len(issues) != 0 {
		t.Errorf("detectErrors(\"\") returned %d issues, want 0", len(issues))
	}
}

func TestDetectErrors_MultipleErrors(t *testing.T) {
	t.Parallel()
	// Output with both rate limit and authentication errors
	output := "Rate limit exceeded\nAuthentication failed\npanic: nil pointer"
	issues := detectErrors(output)
	if len(issues) < 2 {
		t.Errorf("detectErrors with multiple errors returned %d issues, want >= 2", len(issues))
	}
}

func TestDetectErrors_ConnectionError(t *testing.T) {
	t.Parallel()
	output := "connection refused"
	issues := detectErrors(output)
	found := false
	for _, issue := range issues {
		if issue.Type == "network_error" {
			found = true
			break
		}
	}
	if !found {
		t.Error("detectErrors should detect connection error as network_error")
	}
}

func TestDetectErrors_GenericError(t *testing.T) {
	t.Parallel()
	output := "npm ERR! something went wrong"
	issues := detectErrors(output)
	found := false
	for _, issue := range issues {
		if issue.Type == "error" {
			found = true
			break
		}
	}
	if !found {
		t.Error("detectErrors should detect npm error as generic error")
	}
}

func TestDetectErrors_CrashOnly(t *testing.T) {
	t.Parallel()
	output := "panic: runtime error: index out of range"
	issues := detectErrors(output)
	found := false
	for _, issue := range issues {
		if issue.Type == "crash" {
			found = true
			break
		}
	}
	if !found {
		t.Error("detectErrors should detect panic as crash")
	}
}

func TestDetectErrors_AuthOnly(t *testing.T) {
	t.Parallel()
	output := "Authentication failed: invalid API key"
	issues := detectErrors(output)
	found := false
	for _, issue := range issues {
		if issue.Type == "auth_error" {
			found = true
			break
		}
	}
	if !found {
		t.Error("detectErrors should detect auth failure")
	}
}

// =============================================================================
// calculateStatus edge cases
// =============================================================================

func TestCalculateStatus_ExitedWithIssues(t *testing.T) {
	t.Parallel()
	h := AgentHealth{
		ProcessStatus: ProcessExited,
		Activity:      ActivityActive,
		Issues:        []Issue{{Type: "crash"}},
	}
	got := calculateStatus(h)
	if got != StatusError {
		t.Errorf("calculateStatus(exited+crash) = %v, want StatusError", got)
	}
}

func TestCalculateStatus_RunningActiveNoIssues(t *testing.T) {
	t.Parallel()
	h := AgentHealth{
		ProcessStatus: ProcessRunning,
		Activity:      ActivityActive,
		Issues:        nil,
	}
	got := calculateStatus(h)
	if got != StatusOK {
		t.Errorf("calculateStatus(running+active) = %v, want StatusOK", got)
	}
}

func TestCalculateStatus_RateLimitOnly(t *testing.T) {
	t.Parallel()
	h := AgentHealth{
		ProcessStatus: ProcessRunning,
		Activity:      ActivityActive,
		Issues:        []Issue{{Type: "rate_limit", Message: "429"}},
	}
	got := calculateStatus(h)
	if got != StatusWarning {
		t.Errorf("calculateStatus(rate_limit) = %v, want StatusWarning", got)
	}
}

// =============================================================================
// parseWaitTime edge cases
// =============================================================================

// =============================================================================
// SessionNotFoundError
// =============================================================================

func TestSessionNotFoundError_Is(t *testing.T) {
	t.Parallel()
	err := &SessionNotFoundError{Session: "test"}
	msg := err.Error()
	if msg != "session 'test' not found" {
		t.Errorf("Error() = %q, want \"session 'test' not found\"", msg)
	}
}

// =============================================================================
// AgentHealth struct fields
// =============================================================================

func TestAgentHealth_ShellPID(t *testing.T) {
	t.Parallel()
	h := AgentHealth{ShellPID: 12345}
	if h.ShellPID != 12345 {
		t.Errorf("ShellPID = %d, want 12345", h.ShellPID)
	}
}

func TestAgentHealth_DefaultValues(t *testing.T) {
	t.Parallel()
	var h AgentHealth
	if h.Status != "" {
		t.Errorf("default Status = %q, want empty", h.Status)
	}
	if h.ProcessStatus != "" {
		t.Errorf("default ProcessStatus = %q, want empty", h.ProcessStatus)
	}
	if h.Activity != "" {
		t.Errorf("default Activity = %q, want empty", h.Activity)
	}
	if h.ShellPID != 0 {
		t.Errorf("default ShellPID = %d, want 0", h.ShellPID)
	}
}

// =============================================================================
// detectProcessStatus with current process PID
// =============================================================================
