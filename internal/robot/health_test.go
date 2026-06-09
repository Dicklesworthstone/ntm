package robot

import (
	"errors"
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

func TestIsAgentIdlePrompt(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		agentType string
		expected  bool
	}{
		// Empty/whitespace
		{"empty output", "", "claude", false},
		{"whitespace only", "   \n\t  ", "claude", false},

		// Claude agent
		{"claude with > prompt", "Some output\n>", "claude", true},
		{"claude cc alias with > prompt", "Some output\n>", "cc", true},
		{"claude cli alias with > prompt", "Some output\n>", "claude_code", true},
		{"claude with question prompt", "What would you like to do?", "claude", true},
		{"claude no prompt", "Some output\nProcessing...", "claude", false},

		// Codex agent
		{"codex with > prompt", "Codex running\n>", "codex", true},
		{"codex cod alias with > prompt", "Codex running\n>", "cod", true},
		{"codex cli alias with > prompt", "Codex running\n>", "openai-codex", true},
		{"codex with $ prompt", "Codex running\n$", "codex", true},
		{"codex no prompt", "Codex running\nProcessing...", "codex", false},

		// Gemini agent
		{"gemini with > prompt", "Gemini output\n>", "gemini", true},
		{"gemini gmi alias with > prompt", "Gemini output\n>", "gmi", true},
		{"gemini cli alias with > prompt", "Gemini output\n>", "google-gemini", true},
		{"gemini no prompt", "Gemini output\nWorking...", "gemini", false},

		// Unknown/default agent
		{"unknown with > prompt", "Output\n>", "unknown", true},
		{"unknown with $ prompt", "Output\n$", "unknown", true},
		{"unknown with % prompt", "Output\n%", "unknown", true},
		{"unknown no prompt", "Output\nBusy...", "unknown", false},

		// Edge cases
		{"prompt in middle of output", "Some > text\nProcessing", "claude", false},
		{"multiple lines ending with prompt", "Line1\nLine2\nLine3\n>", "claude", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isAgentIdlePrompt(tt.output, tt.agentType)
			if got != tt.expected {
				t.Errorf("isAgentIdlePrompt(%q, %q) = %v, want %v", tt.output, tt.agentType, got, tt.expected)
			}
		})
	}
}

// =============================================================================
// Tests for isShellPrompt
// =============================================================================

func TestIsShellPrompt(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected bool
	}{
		// Empty/whitespace
		{"empty output", "", false},
		{"whitespace only", "   \n\t  ", false},

		// Valid shell prompts
		{"simple $", "command output\n$", true},
		{"simple %", "command output\n%", true},
		{"simple #", "command output\n#", true},
		{"user@host $", "command output\nuser@host:~$", true},
		{"user@host %", "command output\nuser@hostname%", true},
		{"bash prompt", "output\nbash-5.1$", true},
		{"zsh prompt", "output\n%", true},

		// Invalid - line too long
		{"line too long", "output\n" + string(make([]byte, 100)) + "$", false},

		// Invalid - no prompt character
		{"no prompt char", "output\nsome text", false},
		{"ends with >", "output\n>", false}, // > is not a shell prompt

		// Edge cases
		{"prompt in middle", "user$something\nnot a prompt", false},
		{"multiple lines", "line1\nline2\n$", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isShellPrompt(tt.output)
			if got != tt.expected {
				t.Errorf("isShellPrompt(%q) = %v, want %v", tt.output, got, tt.expected)
			}
		})
	}
}

// =============================================================================
// Tests for isAgentRunning
// =============================================================================

func TestIsAgentRunning(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		agentType string
		expected  bool
	}{
		// Empty
		{"empty output", "", "claude", false},
		{"whitespace only", "   ", "claude", false},

		// Claude
		{"claude mentioned", "Claude Code v1.0\nStarting...", "claude", true},
		{"claude cc alias", "claude running", "cc", true},
		{"claude cli alias", "What would you like to do?", "claude_code", true},
		{"claude with prompt", "What would you like to do?", "claude", true},
		{"claude short prompt", "Output\n>", "claude", true},
		{"claude long last line no prompt", "Output\n" + string(make([]byte, 60)), "claude", false},

		// Codex
		{"codex mentioned", "codex agent starting", "codex", true},
		{"codex cod alias", "codex v2", "cod", true},
		{"codex cli alias", "codex v2", "openai-codex", true},
		{"codex short prompt", "Some output\n>", "codex", true},

		// Gemini
		{"gemini mentioned", "GEMINI AI Ready", "gemini", true},
		{"gemini gmi alias", "Gemini starting", "gmi", true},
		{"gemini cli alias", "Gemini starting", "google-gemini", true},
		{"gemini short prompt", "Output\n>", "gemini", true},

		// Newer agent types
		{"cursor mentioned", "Cursor agent starting", "cursor", true},
		{"windsurf alias", "windsurf ready", "ws", true},
		{"aider mentioned", "Aider watching repo", "aider", true},
		{"ollama mentioned", "ollama serving model", "ollama", true},

		// Unknown type
		{"unknown with > prompt", "Output\n>", "unknown", true},
		{"unknown with $ prompt", "Output\n$", "unknown", true},
		{"unknown with % prompt", "Output\n%", "unknown", true},
		{"unknown no prompt", "Just some text", "unknown", false},
		{"unknown long line", "Output\n" + string(make([]byte, 60)) + ">", "unknown", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isAgentRunning(tt.output, tt.agentType)
			if got != tt.expected {
				t.Errorf("isAgentRunning(%q, %q) = %v, want %v", tt.output, tt.agentType, got, tt.expected)
			}
		})
	}
}

// =============================================================================
// Tests for getAgentCommand
// =============================================================================

func TestGetAgentCommand(t *testing.T) {
	tests := []struct {
		agentType string
		expected  string
	}{
		{"claude", "claude"},
		{"cc", "claude"},
		{"claude_code", "claude"},
		{"codex", "codex"},
		{"cod", "codex"},
		{"openai-codex", "codex"},
		{"gemini", "gemini"},
		{"gmi", "gemini"},
		{"google-gemini", "gemini"},
		{"cursor", "cursor"},
		{"ws", "windsurf"},
		{"aider", "aider"},
		{"ollama", "ollama"},
		{"unknown", ""},
		{"", ""},
		{"other", ""},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			got := getAgentCommand(tt.agentType)
			if got != tt.expected {
				t.Errorf("getAgentCommand(%q) = %q, want %q", tt.agentType, got, tt.expected)
			}
		})
	}
}

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

func TestPaneCommandImpliesLiveProcess(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    bool
	}{
		{"empty", "", false},
		{"zsh shell", "zsh", false},
		{"bash path", "/bin/bash", false},
		{"tmux", "tmux", false},
		{"node wrapper", "node", true},
		{"absolute node", "/opt/homebrew/bin/node", true},
		{"claude direct", "claude", true},
		{"codex direct", "codex", true},
		{"numeric claude wrapper", "2.1.168", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := paneCommandImpliesLiveProcess(tt.command); got != tt.want {
				t.Fatalf("paneCommandImpliesLiveProcess(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}

func TestReconcilePIDNoChildWithLivePaneCommand(t *testing.T) {
	tests := []struct {
		name          string
		result        *ProcessCheckResult
		command       string
		wantChanged   bool
		wantRunning   bool
		wantCrashed   bool
		wantReasonHas string
	}{
		{
			name: "alive-as-alive when raw pane command is live",
			result: &ProcessCheckResult{
				Running: false,
				Crashed: true,
				Reason:  pidNoChildReason,
			},
			command:       "node",
			wantChanged:   true,
			wantRunning:   true,
			wantCrashed:   false,
			wantReasonHas: pidProjectionStaleLiveCommandReason,
		},
		{
			name: "dead-as-dead when raw pane command is shell",
			result: &ProcessCheckResult{
				Running: false,
				Crashed: true,
				Reason:  pidNoChildReason,
			},
			command:       "zsh",
			wantChanged:   false,
			wantRunning:   false,
			wantCrashed:   true,
			wantReasonHas: pidNoChildReason,
		},
		{
			name: "dead-as-dead when explicit crash evidence already won",
			result: &ProcessCheckResult{
				Running:    false,
				Crashed:    true,
				ExitStatus: "shell prompt",
				Reason:     "detected shell prompt - agent may have crashed",
			},
			command:       "node",
			wantChanged:   false,
			wantRunning:   false,
			wantCrashed:   true,
			wantReasonHas: "detected shell prompt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := reconcilePIDNoChildWithLivePaneCommand(tt.result, tt.command)
			if changed != tt.wantChanged {
				t.Fatalf("changed = %v, want %v", changed, tt.wantChanged)
			}
			if tt.result.Running != tt.wantRunning {
				t.Fatalf("Running = %v, want %v", tt.result.Running, tt.wantRunning)
			}
			if tt.result.Crashed != tt.wantCrashed {
				t.Fatalf("Crashed = %v, want %v", tt.result.Crashed, tt.wantCrashed)
			}
			if tt.wantReasonHas != "" && !containsSubstr(tt.result.Reason, tt.wantReasonHas) {
				t.Fatalf("Reason = %q, want to contain %q", tt.result.Reason, tt.wantReasonHas)
			}
		})
	}
}

func TestHealthProjectionFreshnessFromProcess(t *testing.T) {
	check := &ProcessCheckResult{
		Running: true,
		Crashed: false,
		Reason:  pidProjectionStaleLiveCommandReason + ": node",
	}

	projection := healthProjectionFreshnessFromProcess(check, "skillos", 6, "%191")
	if projection == nil {
		t.Fatal("expected projection freshness metadata")
	}
	if projection.Status != "reconciled_stale_projection" {
		t.Fatalf("Status = %q", projection.Status)
	}
	if projection.ReasonCode != pidProjectionStaleLiveCommandReason {
		t.Fatalf("ReasonCode = %q", projection.ReasonCode)
	}
	if projection.Source != "tmux.pane_current_command" {
		t.Fatalf("Source = %q", projection.Source)
	}
	if projection.StaleAfterSec != healthProjectionStaleAfterSec {
		t.Fatalf("StaleAfterSec = %d", projection.StaleAfterSec)
	}
	if !containsSubstr(projection.RawSurfaceCommand, "%191") {
		t.Fatalf("RawSurfaceCommand = %q, want pane id", projection.RawSurfaceCommand)
	}
	if projection.RecommendedNextCommand != "ntm --robot-inspect-pane=skillos --inspect-index=6" {
		t.Fatalf("RecommendedNextCommand = %q", projection.RecommendedNextCommand)
	}

	if got := healthProjectionFreshnessFromProcess(&ProcessCheckResult{Reason: pidNoChildReason}, "skillos", 6, "%191"); got != nil {
		t.Fatalf("expected nil projection for unreconciled PID failure, got %+v", got)
	}
}

func TestClassifyPaneDeliverability(t *testing.T) {
	tests := []struct {
		name           string
		status         *paneInputProbeStatus
		statusErr      error
		probeErr       error
		wantStatus     string
		wantReasonCode string
		wantRecovery   string
	}{
		{
			name:           "deliverable",
			status:         &paneInputProbeStatus{},
			wantStatus:     "deliverable",
			wantReasonCode: "",
		},
		{
			name:           "dead pane",
			status:         &paneInputProbeStatus{Dead: true},
			wantStatus:     "non_deliverable",
			wantReasonCode: "pane_dead",
			wantRecovery:   "Restart",
		},
		{
			name:           "input off",
			status:         &paneInputProbeStatus{InputOff: true},
			wantStatus:     "non_deliverable",
			wantReasonCode: "pane_input_off",
			wantRecovery:   "Re-enable",
		},
		{
			name:           "probe not in mode failure",
			status:         &paneInputProbeStatus{},
			probeErr:       errors.New("tmux send-keys failed: not in a mode"),
			wantStatus:     "non_deliverable",
			wantReasonCode: SendFailureClassPaneInputMode,
			wantRecovery:   "Exit tmux mode",
		},
		{
			name:           "status probe failed",
			statusErr:      errors.New("display-message failed"),
			wantStatus:     "unknown",
			wantReasonCode: "pane_input_status_probe_failed",
			wantRecovery:   "Check tmux pane metadata",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyPaneDeliverability("flywheel", 1, "%1", tt.status, tt.statusErr, tt.probeErr)
			if got.Status != tt.wantStatus {
				t.Fatalf("Status = %q, want %q", got.Status, tt.wantStatus)
			}
			if got.ReasonCode != tt.wantReasonCode {
				t.Fatalf("ReasonCode = %q, want %q", got.ReasonCode, tt.wantReasonCode)
			}
			if tt.wantRecovery != "" && !containsSubstr(got.RecoveryHint, tt.wantRecovery) {
				t.Fatalf("RecoveryHint = %q, want to contain %q", got.RecoveryHint, tt.wantRecovery)
			}
			if !containsSubstr(got.RawSurfaceCommand, "%1") {
				t.Fatalf("RawSurfaceCommand = %q, want pane id", got.RawSurfaceCommand)
			}
			if !containsSubstr(got.RecommendedNextCommand, "ntm send flywheel --pane=1 --json --dry-run") {
				t.Fatalf("RecommendedNextCommand = %q, want dry-run send command", got.RecommendedNextCommand)
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
