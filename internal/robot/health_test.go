package robot

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// =============================================================================
// Tests for calculateOutputRate
// =============================================================================

func TestCalculateOutputRate(t *testing.T) {
	tests := []struct {
		name            string
		lastActivitySec int
		expected        string
	}{
		{"negative value", -1, "unknown"},
		{"zero seconds - high rate", 0, "high"},
		{"1 second - high rate", 1, "high"},
		{"2 seconds - medium rate", 2, "medium"},
		{"5 seconds - medium rate", 5, "medium"},
		{"10 seconds - medium rate", 10, "medium"},
		{"11 seconds - low rate", 11, "low"},
		{"30 seconds - low rate", 30, "low"},
		{"60 seconds - low rate", 60, "low"},
		{"61 seconds - no output", 61, "none"},
		{"300 seconds - no output", 300, "none"},
		{"large value - no output", 3600, "none"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateOutputRate(tt.lastActivitySec)
			if got != tt.expected {
				t.Errorf("calculateOutputRate(%d) = %q, want %q", tt.lastActivitySec, got, tt.expected)
			}
		})
	}
}

func TestDetectAgentTypeFromPane(t *testing.T) {
	tests := []struct {
		name string
		pane tmux.Pane
		want string
	}{
		{name: "claude", pane: tmux.Pane{Type: tmux.AgentClaude}, want: "claude"},
		{name: "zai", pane: tmux.Pane{Type: tmux.AgentZAI}, want: "zai"},
		{name: "ollama", pane: tmux.Pane{Type: tmux.AgentOllama}, want: "ollama"},
		{name: "unknown", pane: tmux.Pane{Type: tmux.AgentType("mystery")}, want: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectAgentTypeFromPane(tt.pane); got != tt.want {
				t.Fatalf("detectAgentTypeFromPane(%+v) = %q, want %q", tt.pane, got, tt.want)
			}
		})
	}
}

// =============================================================================
// Tests for parseRateLimitWait
// =============================================================================

func TestParseRateLimitWait(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected int
	}{
		{"empty output", "", 0},
		{"no wait time", "rate limit exceeded", 0},
		{"wait X seconds", "Rate limit exceeded. Please wait 60 seconds before retrying.", 60},
		{"retry in Xs", "Error: retry in 30s", 30},
		{"retry after", "retry after 45 seconds", 45},
		{"try again in", "Please try again in 120 seconds", 120},
		{"second cooldown", "30 second cooldown in effect", 30},
		{"sec delay", "Please observe 15 sec delay", 15},
		{"mixed case", "WAIT 90 SECONDS PLEASE", 90},
		{"number before indicator", "60 second wait required", 60},
		{"large wait time capped", "wait 7200 seconds", 0}, // > 3600 is filtered out
		{"reasonable large wait", "wait 3600 seconds", 3600},
		{"multiple numbers - first valid wins", "wait 30 or 60 seconds", 30},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRateLimitWait(tt.output)
			if got != tt.expected {
				t.Errorf("parseRateLimitWait(%q) = %d, want %d", tt.output, got, tt.expected)
			}
		})
	}
}

// =============================================================================
// Tests for calculateHealthState
// =============================================================================

func TestCalculateHealthState(t *testing.T) {
	tests := []struct {
		name          string
		check         *HealthCheck
		expectedState HealthState
		expectReason  string
	}{
		{
			name:          "all nil - healthy",
			check:         &HealthCheck{},
			expectedState: HealthHealthy,
			expectReason:  "all checks passed",
		},
		{
			name: "process crashed - unhealthy",
			check: &HealthCheck{
				ProcessCheck: &ProcessCheckResult{
					Running: false,
					Crashed: true,
					Reason:  "exit code detected",
				},
			},
			expectedState: HealthUnhealthy,
			expectReason:  "agent crashed",
		},
		{
			name: "error without rate limit - unhealthy",
			check: &HealthCheck{
				ErrorCheck: &ErrorCheckResult{
					HasErrors:   true,
					RateLimited: false,
					Reason:      "auth error",
				},
			},
			expectedState: HealthUnhealthy,
			expectReason:  "error detected:",
		},
		{
			name: "rate limited",
			check: &HealthCheck{
				ErrorCheck: &ErrorCheckResult{
					HasErrors:   true,
					RateLimited: true,
					Reason:      "rate limit detected",
				},
			},
			expectedState: HealthRateLimited,
			expectReason:  "rate limit detected",
		},
		{
			name: "stalled - degraded",
			check: &HealthCheck{
				StallCheck: &StallCheckResult{
					Stalled: true,
					Reason:  "no output for extended period",
				},
			},
			expectedState: HealthDegraded,
			expectReason:  "agent stalled:",
		},
		{
			name: "extended idle - degraded",
			check: &HealthCheck{
				StallCheck: &StallCheckResult{
					Stalled:     false,
					IdleSeconds: 400, // > 300 seconds
				},
			},
			expectedState: HealthDegraded,
			expectReason:  "agent idle for extended period",
		},
		{
			name: "short idle - healthy",
			check: &HealthCheck{
				StallCheck: &StallCheckResult{
					Stalled:     false,
					IdleSeconds: 100, // < 300 seconds
				},
			},
			expectedState: HealthHealthy,
			expectReason:  "all checks passed",
		},
		{
			name: "crash takes priority over rate limit",
			check: &HealthCheck{
				ProcessCheck: &ProcessCheckResult{
					Running: false,
					Crashed: true,
				},
				ErrorCheck: &ErrorCheckResult{
					HasErrors:   true,
					RateLimited: true,
				},
			},
			expectedState: HealthUnhealthy,
			expectReason:  "agent crashed",
		},
		{
			name: "error takes priority over rate limit",
			check: &HealthCheck{
				ProcessCheck: &ProcessCheckResult{Running: true},
				ErrorCheck: &ErrorCheckResult{
					HasErrors:   true,
					RateLimited: false, // non-rate-limit error
				},
			},
			expectedState: HealthUnhealthy,
			expectReason:  "error detected:",
		},
		{
			name: "rate limit takes priority over stall",
			check: &HealthCheck{
				ErrorCheck: &ErrorCheckResult{
					HasErrors:   true,
					RateLimited: true,
				},
				StallCheck: &StallCheckResult{
					Stalled: true,
				},
			},
			expectedState: HealthRateLimited,
			expectReason:  "rate limit detected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotState, gotReason := calculateHealthState(tt.check)
			if gotState != tt.expectedState {
				t.Errorf("calculateHealthState() state = %v, want %v", gotState, tt.expectedState)
			}
			if tt.expectReason != "" && !containsSubstr(gotReason, tt.expectReason) {
				t.Errorf("calculateHealthState() reason = %q, want to contain %q", gotReason, tt.expectReason)
			}
		})
	}
}

// Helper for partial string matching
func containsSubstr(s, substr string) bool {
	if substr == "" {
		return true
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// =============================================================================
// Tests for calculateHealthConfidence
// =============================================================================

func TestCalculateHealthConfidence(t *testing.T) {
	tests := []struct {
		name     string
		check    *HealthCheck
		expected float64
	}{
		{
			name:     "all nil checks - reduced confidence",
			check:    &HealthCheck{},
			expected: 0.8, // 1.0 * 0.8 for missing checks
		},
		{
			name: "all checks present - full confidence",
			check: &HealthCheck{
				ProcessCheck: &ProcessCheckResult{},
				StallCheck:   &StallCheckResult{Confidence: 0.9},
				ErrorCheck:   &ErrorCheckResult{},
			},
			expected: 1.0,
		},
		{
			name: "low stall confidence",
			check: &HealthCheck{
				ProcessCheck: &ProcessCheckResult{},
				StallCheck:   &StallCheckResult{Confidence: 0.5},
				ErrorCheck:   &ErrorCheckResult{},
			},
			expected: 0.5, // 1.0 * 0.5
		},
		{
			name: "very low stall confidence",
			check: &HealthCheck{
				ProcessCheck: &ProcessCheckResult{},
				StallCheck:   &StallCheckResult{Confidence: 0.3},
				ErrorCheck:   &ErrorCheckResult{},
			},
			expected: 0.3, // 1.0 * 0.3
		},
		{
			name: "missing some checks with low stall confidence",
			check: &HealthCheck{
				StallCheck: &StallCheckResult{Confidence: 0.5},
			},
			expected: 0.4, // 1.0 * 0.5 * 0.8
		},
		{
			name: "stall confidence exactly at threshold",
			check: &HealthCheck{
				ProcessCheck: &ProcessCheckResult{},
				StallCheck:   &StallCheckResult{Confidence: 0.7},
				ErrorCheck:   &ErrorCheckResult{},
			},
			expected: 1.0, // 0.7 is not < 0.7, so no reduction
		},
		{
			name: "stall confidence just below threshold",
			check: &HealthCheck{
				ProcessCheck: &ProcessCheckResult{},
				StallCheck:   &StallCheckResult{Confidence: 0.69},
				ErrorCheck:   &ErrorCheckResult{},
			},
			expected: 0.69,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateHealthConfidence(tt.check)
			// Allow small floating point differences
			if got < tt.expected-0.01 || got > tt.expected+0.01 {
				t.Errorf("calculateHealthConfidence() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// =============================================================================
// Tests for isAgentIdlePrompt
// =============================================================================

// =============================================================================
// Tests for isShellPrompt
// =============================================================================

// =============================================================================
// Tests for isAgentRunning
// =============================================================================

// =============================================================================
// Tests for getAgentCommand
// =============================================================================

// =============================================================================
// Tests for HealthCheck struct construction
// =============================================================================

func TestHealthCheckConstruction(t *testing.T) {
	check := &HealthCheck{
		PaneID:      "%1",
		AgentType:   "claude",
		HealthState: HealthHealthy,
		ProcessCheck: &ProcessCheckResult{
			Running: true,
			Crashed: false,
		},
		StallCheck: &StallCheckResult{
			Stalled:       false,
			ActivityState: "active",
			Velocity:      10.5,
			IdleSeconds:   5,
			Confidence:    0.9,
		},
		ErrorCheck: &ErrorCheckResult{
			HasErrors:   false,
			RateLimited: false,
		},
		Confidence: 0.95,
		Reason:     "all checks passed",
		CheckedAt:  time.Now().UTC(),
	}

	if check.PaneID != "%1" {
		t.Errorf("PaneID = %q, want %q", check.PaneID, "%1")
	}
	if check.AgentType != "claude" {
		t.Errorf("AgentType = %q, want %q", check.AgentType, "claude")
	}
	if check.HealthState != HealthHealthy {
		t.Errorf("HealthState = %v, want %v", check.HealthState, HealthHealthy)
	}
	if !check.ProcessCheck.Running {
		t.Error("Expected ProcessCheck.Running to be true")
	}
	if check.StallCheck.Stalled {
		t.Error("Expected StallCheck.Stalled to be false")
	}
	if check.ErrorCheck.HasErrors {
		t.Error("Expected ErrorCheck.HasErrors to be false")
	}
}

// =============================================================================
// Tests for ProcessCheckResult
// =============================================================================

func TestProcessCheckResult(t *testing.T) {
	tests := []struct {
		name    string
		result  *ProcessCheckResult
		running bool
		crashed bool
	}{
		{
			name: "running normally",
			result: &ProcessCheckResult{
				Running: true,
				Crashed: false,
			},
			running: true,
			crashed: false,
		},
		{
			name: "crashed",
			result: &ProcessCheckResult{
				Running:    false,
				Crashed:    true,
				ExitStatus: "exit code 1",
				Reason:     "exit code detected",
			},
			running: false,
			crashed: true,
		},
		{
			name: "stopped but not crashed",
			result: &ProcessCheckResult{
				Running: false,
				Crashed: false,
				Reason:  "graceful shutdown",
			},
			running: false,
			crashed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.result.Running != tt.running {
				t.Errorf("Running = %v, want %v", tt.result.Running, tt.running)
			}
			if tt.result.Crashed != tt.crashed {
				t.Errorf("Crashed = %v, want %v", tt.result.Crashed, tt.crashed)
			}
		})
	}
}

// =============================================================================
// Tests for StallCheckResult
// =============================================================================

func TestStallCheckResult(t *testing.T) {
	tests := []struct {
		name       string
		result     *StallCheckResult
		stalled    bool
		confidence float64
	}{
		{
			name: "active - not stalled",
			result: &StallCheckResult{
				Stalled:       false,
				ActivityState: "active",
				Velocity:      15.0,
				IdleSeconds:   2,
				Confidence:    0.95,
			},
			stalled:    false,
			confidence: 0.95,
		},
		{
			name: "stalled with reason",
			result: &StallCheckResult{
				Stalled:       true,
				ActivityState: "stalled",
				Velocity:      0,
				IdleSeconds:   300,
				Confidence:    0.8,
				Reason:        "no output for extended period",
			},
			stalled:    true,
			confidence: 0.8,
		},
		{
			name: "error state",
			result: &StallCheckResult{
				Stalled:       true,
				ActivityState: "error",
				Confidence:    0.9,
				Reason:        "agent in error state",
			},
			stalled:    true,
			confidence: 0.9,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.result.Stalled != tt.stalled {
				t.Errorf("Stalled = %v, want %v", tt.result.Stalled, tt.stalled)
			}
			if tt.result.Confidence != tt.confidence {
				t.Errorf("Confidence = %v, want %v", tt.result.Confidence, tt.confidence)
			}
		})
	}
}

// =============================================================================
// Tests for ErrorCheckResult
// =============================================================================

func TestErrorCheckResult(t *testing.T) {
	tests := []struct {
		name        string
		result      *ErrorCheckResult
		hasErrors   bool
		rateLimited bool
	}{
		{
			name: "no errors",
			result: &ErrorCheckResult{
				HasErrors:   false,
				RateLimited: false,
				Patterns:    []string{},
			},
			hasErrors:   false,
			rateLimited: false,
		},
		{
			name: "rate limited",
			result: &ErrorCheckResult{
				HasErrors:   true,
				RateLimited: true,
				Patterns:    []string{"rate_limit"},
				WaitSeconds: 60,
				Reason:      "rate limit detected",
			},
			hasErrors:   true,
			rateLimited: true,
		},
		{
			name: "auth error",
			result: &ErrorCheckResult{
				HasErrors:   true,
				RateLimited: false,
				Patterns:    []string{"auth_error"},
				Reason:      "detected: [auth_error]",
			},
			hasErrors:   true,
			rateLimited: false,
		},
		{
			name: "multiple errors",
			result: &ErrorCheckResult{
				HasErrors:   true,
				RateLimited: false,
				Patterns:    []string{"crash", "network_error"},
				Reason:      "detected: [crash network_error]",
			},
			hasErrors:   true,
			rateLimited: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.result.HasErrors != tt.hasErrors {
				t.Errorf("HasErrors = %v, want %v", tt.result.HasErrors, tt.hasErrors)
			}
			if tt.result.RateLimited != tt.rateLimited {
				t.Errorf("RateLimited = %v, want %v", tt.result.RateLimited, tt.rateLimited)
			}
		})
	}
}

// =============================================================================
// Tests for healthErrorPatterns coverage
// =============================================================================

func TestHealthErrorPatterns(t *testing.T) {
	// Verify the error patterns structure is populated
	if len(healthErrorPatterns) == 0 {
		t.Fatal("healthErrorPatterns should not be empty")
	}

	// Track pattern types
	types := make(map[string]int)
	for _, ep := range healthErrorPatterns {
		types[ep.Type]++
		if ep.Pattern == "" {
			t.Error("Found empty pattern")
		}
		if ep.Type == "" {
			t.Error("Found empty type")
		}
	}

	// Verify expected types exist
	expectedTypes := []string{"rate_limit", "auth_error", "crash", "network_error"}
	for _, expectedType := range expectedTypes {
		if types[expectedType] == 0 {
			t.Errorf("Expected pattern type %q not found", expectedType)
		}
	}
}

// =============================================================================
// Tests for SessionHealthOutput and related structs
// =============================================================================

func TestSessionHealthOutputStruct(t *testing.T) {
	output := SessionHealthOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       "test-session",
		CheckedAt:     time.Now().UTC(),
		Agents: []SessionAgentHealth{
			{
				Pane:             1,
				AgentType:        "claude",
				Health:           "healthy",
				IdleSinceSeconds: 10,
				Restarts:         0,
				RateLimitCount:   0,
				BackoffRemaining: 0,
				Confidence:       0.95,
			},
		},
		Summary: SessionHealthSummary{
			Total:       1,
			Healthy:     1,
			Degraded:    0,
			Unhealthy:   0,
			RateLimited: 0,
		},
	}

	if !output.Success {
		t.Error("Expected Success to be true")
	}
	if output.Session != "test-session" {
		t.Errorf("Session = %q, want %q", output.Session, "test-session")
	}
	if len(output.Agents) != 1 {
		t.Errorf("Expected 1 agent, got %d", len(output.Agents))
	}
	if output.Summary.Total != 1 {
		t.Errorf("Summary.Total = %d, want 1", output.Summary.Total)
	}
}

func TestSessionAgentHealth(t *testing.T) {
	agent := SessionAgentHealth{
		Pane:             2,
		AgentType:        "codex",
		Health:           "degraded",
		IdleSinceSeconds: 120,
		Restarts:         1,
		LastError:        "timeout",
		RateLimitCount:   2,
		BackoffRemaining: 30,
		Confidence:       0.7,
	}

	if agent.Pane != 2 {
		t.Errorf("Pane = %d, want 2", agent.Pane)
	}
	if agent.AgentType != "codex" {
		t.Errorf("AgentType = %q, want %q", agent.AgentType, "codex")
	}
	if agent.Health != "degraded" {
		t.Errorf("Health = %q, want %q", agent.Health, "degraded")
	}
	if agent.Restarts != 1 {
		t.Errorf("Restarts = %d, want 1", agent.Restarts)
	}
	if agent.LastError != "timeout" {
		t.Errorf("LastError = %q, want %q", agent.LastError, "timeout")
	}
	if agent.BackoffRemaining != 30 {
		t.Errorf("BackoffRemaining = %d, want 30", agent.BackoffRemaining)
	}
}

func TestSessionHealthSummary(t *testing.T) {
	summary := SessionHealthSummary{
		Total:       5,
		Healthy:     2,
		Degraded:    1,
		Unhealthy:   1,
		RateLimited: 1,
	}

	if summary.Total != 5 {
		t.Errorf("Total = %d, want 5", summary.Total)
	}
	// Verify counts add up
	if summary.Healthy+summary.Degraded+summary.Unhealthy+summary.RateLimited != summary.Total {
		t.Error("Health counts don't add up to total")
	}
}

// =============================================================================
// Integration-style tests for health state calculations
// =============================================================================

func TestHealthStateTransitionScenarios(t *testing.T) {
	tests := []struct {
		name          string
		processResult *ProcessCheckResult
		stallResult   *StallCheckResult
		errorResult   *ErrorCheckResult
		expectedState HealthState
	}{
		{
			name:          "healthy agent",
			processResult: &ProcessCheckResult{Running: true},
			stallResult:   &StallCheckResult{Stalled: false, IdleSeconds: 10},
			errorResult:   &ErrorCheckResult{HasErrors: false},
			expectedState: HealthHealthy,
		},
		{
			name:          "crashed agent",
			processResult: &ProcessCheckResult{Running: false, Crashed: true},
			stallResult:   &StallCheckResult{Stalled: false},
			errorResult:   &ErrorCheckResult{HasErrors: false},
			expectedState: HealthUnhealthy,
		},
		{
			name:          "rate limited agent",
			processResult: &ProcessCheckResult{Running: true},
			stallResult:   &StallCheckResult{Stalled: false},
			errorResult:   &ErrorCheckResult{HasErrors: true, RateLimited: true, WaitSeconds: 60},
			expectedState: HealthRateLimited,
		},
		{
			name:          "stalled agent",
			processResult: &ProcessCheckResult{Running: true},
			stallResult:   &StallCheckResult{Stalled: true, Reason: "no output"},
			errorResult:   &ErrorCheckResult{HasErrors: false},
			expectedState: HealthDegraded,
		},
		{
			name:          "idle for extended period",
			processResult: &ProcessCheckResult{Running: true},
			stallResult:   &StallCheckResult{Stalled: false, IdleSeconds: 600},
			errorResult:   &ErrorCheckResult{HasErrors: false},
			expectedState: HealthDegraded,
		},
		{
			name:          "network error",
			processResult: &ProcessCheckResult{Running: true},
			stallResult:   &StallCheckResult{Stalled: false},
			errorResult:   &ErrorCheckResult{HasErrors: true, RateLimited: false, Patterns: []string{"network_error"}},
			expectedState: HealthUnhealthy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check := &HealthCheck{
				ProcessCheck: tt.processResult,
				StallCheck:   tt.stallResult,
				ErrorCheck:   tt.errorResult,
			}

			gotState, _ := calculateHealthState(check)
			if gotState != tt.expectedState {
				t.Errorf("calculateHealthState() = %v, want %v", gotState, tt.expectedState)
			}
		})
	}
}

// =============================================================================
// Tests for edge cases and boundary conditions
// =============================================================================

func TestCalculateOutputRateBoundaries(t *testing.T) {
	// Test exact boundary values
	boundaries := []struct {
		sec      int
		expected string
	}{
		{-1, "unknown"},
		{0, "high"},
		{1, "high"},
		{2, "medium"},
		{10, "medium"},
		{11, "low"},
		{60, "low"},
		{61, "none"},
	}

	for _, b := range boundaries {
		got := calculateOutputRate(b.sec)
		if got != b.expected {
			t.Errorf("calculateOutputRate(%d) = %q, want %q (boundary test)", b.sec, got, b.expected)
		}
	}
}

func TestParseRateLimitWaitEdgeCases(t *testing.T) {
	// Test edge cases
	edgeCases := []struct {
		name     string
		output   string
		expected int
	}{
		{"boundary - exactly 3600", "wait 3600 seconds", 3600},
		{"boundary - 3601 filtered", "wait 3601 seconds", 0},
		{"boundary - 1 second", "wait 1 second", 1},
		{"multiple indicators", "retry in 30s, wait 60 seconds", 30}, // First valid wins
		{"number at start of region", "30 second delay", 30},
		{"number at end of region", "delay of 45 seconds", 45},
	}

	for _, tc := range edgeCases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseRateLimitWait(tc.output)
			if got != tc.expected {
				t.Errorf("parseRateLimitWait(%q) = %d, want %d", tc.output, got, tc.expected)
			}
		})
	}
}

// =============================================================================
// Tests for concurrent access patterns (smoke test)
// =============================================================================

func TestHealthCheckConcurrentAccess(t *testing.T) {
	// Create multiple health checks concurrently
	done := make(chan bool, 10)

	for i := 0; i < 10; i++ {
		go func(idx int) {
			check := &HealthCheck{
				PaneID:       "%" + string(rune('0'+idx)),
				AgentType:    "claude",
				ProcessCheck: &ProcessCheckResult{Running: true},
				StallCheck:   &StallCheckResult{Stalled: false, Confidence: 0.9},
				ErrorCheck:   &ErrorCheckResult{HasErrors: false},
			}

			// Run calculations
			state, _ := calculateHealthState(check)
			confidence := calculateHealthConfidence(check)

			// Verify results are sensible
			if state != HealthHealthy {
				t.Errorf("goroutine %d: expected HealthHealthy, got %v", idx, state)
			}
			if confidence < 0.8 {
				t.Errorf("goroutine %d: expected confidence >= 0.8, got %v", idx, confidence)
			}

			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}
}
