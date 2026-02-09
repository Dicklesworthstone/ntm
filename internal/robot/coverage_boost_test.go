package robot

import (
	"encoding/json"
	"testing"
	"time"
)

// =============================================================================
// exit_sequences.go — HardKillResult JSON structure (0% → covered)
// =============================================================================

func TestHardKillResult_JSONStructure(t *testing.T) {
	t.Parallel()

	result := HardKillResult{
		ShellPID:   12345,
		ChildPID:   12346,
		KillMethod: "kill_9",
		Success:    true,
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal HardKillResult: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded["shell_pid"].(float64) != 12345 {
		t.Errorf("shell_pid = %v, want 12345", decoded["shell_pid"])
	}
	if decoded["child_pid"].(float64) != 12346 {
		t.Errorf("child_pid = %v, want 12346", decoded["child_pid"])
	}
	if decoded["kill_method"].(string) != "kill_9" {
		t.Errorf("kill_method = %v, want kill_9", decoded["kill_method"])
	}
	if decoded["success"].(bool) != true {
		t.Errorf("success = %v, want true", decoded["success"])
	}
}

func TestHardKillResult_OmitemptyPIDs(t *testing.T) {
	t.Parallel()

	result := HardKillResult{
		KillMethod: "no_child_process",
		Success:    true,
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// shell_pid and child_pid use omitempty, so zero values should be absent
	if _, ok := decoded["shell_pid"]; ok {
		t.Error("shell_pid with zero value should be omitted")
	}
	if _, ok := decoded["child_pid"]; ok {
		t.Error("child_pid with zero value should be omitted")
	}
	if _, ok := decoded["kill_method"]; !ok {
		t.Error("kill_method should be present")
	}
	if _, ok := decoded["success"]; !ok {
		t.Error("success should be present")
	}
}

// =============================================================================
// errors.go — isRobotErrorLine (pure function)
// =============================================================================

func TestIsRobotErrorLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		line      string
		wantMatch bool
		wantType  string
	}{
		{"empty line", "", false, ""},
		{"whitespace only", "   ", false, ""},
		{"normal output", "compiling main.go", false, ""},
		{"go error", "Error: something failed", true, "error"},
		{"panic", "panic: runtime error", true, "panic"},
		{"rate limit via prefix", "Error: rate limit exceeded", true, "error"},
		{"too many requests", "too many requests from API", true, "rate_limit"},
		{"429 status", "429 Too Many Requests", true, "rate_limit"},
		{"context limit", "context window exceeded for agent", true, "context_limit"},
		{"fatal error", "fatal error: goroutine stack", true, "fatal"},
		{"traceback", "Traceback (most recent call last):", true, "traceback"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotMatch, gotType := isRobotErrorLine(tt.line)
			if gotMatch != tt.wantMatch {
				t.Errorf("isRobotErrorLine(%q) match = %v, want %v", tt.line, gotMatch, tt.wantMatch)
			}
			if tt.wantMatch && gotType != tt.wantType {
				t.Errorf("isRobotErrorLine(%q) type = %q, want %q", tt.line, gotType, tt.wantType)
			}
		})
	}
}

// =============================================================================
// diagnose.go — additional branch coverage
// =============================================================================

func TestDetermineOverallHealth_EdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("only unknown panes", func(t *testing.T) {
		t.Parallel()
		summary := DiagnoseSummary{TotalPanes: 3, Unknown: 3}
		got := determineOverallHealth(summary)
		if got != "degraded" {
			t.Errorf("all unknown panes should be 'degraded', got %q", got)
		}
	})

	t.Run("only degraded panes", func(t *testing.T) {
		t.Parallel()
		summary := DiagnoseSummary{TotalPanes: 3, Degraded: 3}
		got := determineOverallHealth(summary)
		if got != "degraded" {
			t.Errorf("all degraded panes should be 'degraded', got %q", got)
		}
	})

	t.Run("only rate limited", func(t *testing.T) {
		t.Parallel()
		summary := DiagnoseSummary{TotalPanes: 4, RateLimited: 4}
		got := determineOverallHealth(summary)
		if got != "degraded" {
			t.Errorf("all rate limited should be 'degraded', got %q", got)
		}
	})

	t.Run("all unresponsive", func(t *testing.T) {
		t.Parallel()
		summary := DiagnoseSummary{TotalPanes: 4, Unresponsive: 4}
		got := determineOverallHealth(summary)
		if got != "critical" {
			t.Errorf("all unresponsive should be 'critical', got %q", got)
		}
	})

	t.Run("single pane crashed", func(t *testing.T) {
		t.Parallel()
		summary := DiagnoseSummary{TotalPanes: 1, Crashed: 1}
		got := determineOverallHealth(summary)
		if got != "critical" {
			t.Errorf("single crashed should be 'critical', got %q", got)
		}
	})

	t.Run("degraded plus unknown", func(t *testing.T) {
		t.Parallel()
		summary := DiagnoseSummary{TotalPanes: 4, Healthy: 2, Degraded: 1, Unknown: 1}
		got := determineOverallHealth(summary)
		if got != "degraded" {
			t.Errorf("degraded plus unknown should be 'degraded', got %q", got)
		}
	})
}

func TestBuildRateLimitRecommendation_WaitSecondsVariations(t *testing.T) {
	t.Parallel()

	t.Run("large wait seconds", func(t *testing.T) {
		t.Parallel()
		check := &HealthCheck{
			ErrorCheck: &ErrorCheckResult{
				RateLimited: true,
				WaitSeconds: 3600,
			},
		}
		rec := buildRateLimitRecommendation(5, "prod-session", check)
		if rec.Action != "wait" {
			t.Errorf("expected action 'wait', got %q", rec.Action)
		}
		if rec.Pane != 5 {
			t.Errorf("expected pane 5, got %d", rec.Pane)
		}
	})

	t.Run("negative wait seconds treated as no wait", func(t *testing.T) {
		t.Parallel()
		check := &HealthCheck{
			ErrorCheck: &ErrorCheckResult{
				RateLimited: true,
				WaitSeconds: -1,
			},
		}
		rec := buildRateLimitRecommendation(0, "session", check)
		if rec.Action != "wait_or_switch" {
			t.Errorf("expected action 'wait_or_switch' for negative wait, got %q", rec.Action)
		}
	})
}

// =============================================================================
// interrupt.go — getLastMeaningfulOutput (pure function), InterruptOutput struct
// =============================================================================

func TestGetLastMeaningfulOutput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		lines     []string
		maxLen    int
		agentType string
		wantEmpty bool
	}{
		{"empty lines", []string{}, 200, "cc", true},
		{"all empty strings", []string{"", "", ""}, 200, "cc", true},
		{"single line", []string{"hello world"}, 200, "cc", false},
		{"multiple lines", []string{"line1", "line2", "line3"}, 200, "cc", false},
		{"with empty lines mixed", []string{"", "content", "", "more", ""}, 200, "cc", false},
		{"maxLen zero", []string{"hello"}, 0, "cc", true},
		{"maxLen very small", []string{"hello world"}, 3, "cc", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := getLastMeaningfulOutput(tt.lines, tt.maxLen, tt.agentType)
			if tt.wantEmpty && got != "" {
				t.Errorf("getLastMeaningfulOutput() = %q, want empty", got)
			}
			if !tt.wantEmpty && got == "" {
				t.Errorf("getLastMeaningfulOutput() = empty, want non-empty")
			}
			if tt.maxLen > 0 && len(got) > tt.maxLen {
				t.Errorf("getLastMeaningfulOutput() length %d exceeds maxLen %d", len(got), tt.maxLen)
			}
		})
	}
}

func TestGetLastMeaningfulOutput_Truncation(t *testing.T) {
	t.Parallel()

	lines := []string{"this is a very long line that should be truncated when maxLen is small"}
	got := getLastMeaningfulOutput(lines, 20, "cc")
	if len(got) > 20 {
		t.Errorf("output length %d exceeds maxLen 20", len(got))
	}
}

func TestInterruptOutput_JSONStructure(t *testing.T) {
	t.Parallel()

	output := InterruptOutput{
		RobotResponse:  NewRobotResponse(true),
		Session:        "test-session",
		InterruptedAt:  time.Now().UTC(),
		CompletedAt:    time.Now().UTC(),
		Interrupted:    []string{"1", "2"},
		PreviousStates: map[string]PaneState{"1": {State: "active", AgentType: "claude"}},
		Method:         "ctrl_c",
		MessageSent:    false,
		ReadyForInput:  []string{"1"},
		Failed:         []InterruptError{},
		TimeoutMs:      10000,
		TimedOut:       false,
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("Failed to marshal InterruptOutput: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	requiredFields := []string{
		"success", "timestamp", "session", "interrupted_at", "completed_at",
		"interrupted", "previous_states", "method", "message_sent",
		"ready_for_input", "failed", "timeout_ms", "timed_out",
	}
	for _, field := range requiredFields {
		if _, ok := decoded[field]; !ok {
			t.Errorf("Missing required field %q in InterruptOutput", field)
		}
	}
}

func TestInterruptOptions_Defaults(t *testing.T) {
	t.Parallel()

	opts := InterruptOptions{Session: "test"}
	if opts.TimeoutMs != 0 {
		t.Errorf("Default TimeoutMs should be 0 (set in GetInterrupt), got %d", opts.TimeoutMs)
	}
	if opts.PollMs != 0 {
		t.Errorf("Default PollMs should be 0 (set in GetInterrupt), got %d", opts.PollMs)
	}
	if opts.Force {
		t.Error("Default Force should be false")
	}
	if opts.DryRun {
		t.Error("Default DryRun should be false")
	}
}

func TestPaneState_JSONStructure(t *testing.T) {
	t.Parallel()

	state := PaneState{
		State:      "active",
		LastOutput: "compiling...",
		AgentType:  "claude",
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("Failed to marshal PaneState: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded["state"].(string) != "active" {
		t.Errorf("state = %v, want 'active'", decoded["state"])
	}
	if decoded["last_output"].(string) != "compiling..." {
		t.Errorf("last_output = %v, want 'compiling...'", decoded["last_output"])
	}
	if decoded["agent_type"].(string) != "claude" {
		t.Errorf("agent_type = %v, want 'claude'", decoded["agent_type"])
	}
}

func TestInterruptError_JSONStructure(t *testing.T) {
	t.Parallel()

	ie := InterruptError{
		Pane:   "3",
		Reason: "send-keys failed",
	}

	data, err := json.Marshal(ie)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded["pane"].(string) != "3" {
		t.Errorf("pane = %v, want '3'", decoded["pane"])
	}
	if decoded["reason"].(string) != "send-keys failed" {
		t.Errorf("reason = %v, want 'send-keys failed'", decoded["reason"])
	}
}

// =============================================================================
// trends.go — TrendTracker MaxSamples eviction
// =============================================================================

func TestTrendTracker_MaxSamples(t *testing.T) {
	t.Parallel()

	tracker := NewTrendTracker(3) // Max 3 samples

	ctx1, ctx2, ctx3, ctx4 := 10.0, 20.0, 30.0, 40.0

	tracker.AddSample(1, TrendSample{Timestamp: time.Now(), ContextRemaining: &ctx1})
	tracker.AddSample(1, TrendSample{Timestamp: time.Now(), ContextRemaining: &ctx2})
	tracker.AddSample(1, TrendSample{Timestamp: time.Now(), ContextRemaining: &ctx3})
	tracker.AddSample(1, TrendSample{Timestamp: time.Now(), ContextRemaining: &ctx4}) // Should evict oldest

	info := tracker.GetTrendInfo(1)
	if info.SampleCount != 3 {
		t.Errorf("SampleCount = %d, want 3 (max samples)", info.SampleCount)
	}
}

// =============================================================================
// diagnose.go — DiagnoseOutput with mixed states
// =============================================================================

func TestDiagnoseOutput_MixedStates(t *testing.T) {
	t.Parallel()

	output := DiagnoseOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       "production",
		OverallHealth: "degraded",
		Summary: DiagnoseSummary{
			TotalPanes:   8,
			Healthy:      5,
			Degraded:     1,
			RateLimited:  1,
			Unresponsive: 1,
		},
		Panes: DiagnosePanes{
			Healthy:      []int{0, 1, 2, 3, 4},
			Degraded:     []int{5},
			RateLimited:  []int{6},
			Unresponsive: []int{7},
			Crashed:      []int{},
			Unknown:      []int{},
		},
		Recommendations: []DiagnoseRecommendation{
			{Pane: 6, Status: "rate_limited", Action: "wait", Reason: "Rate limited", AutoFixable: false},
			{Pane: 7, Status: "unresponsive", Action: "interrupt", Reason: "Stalled", AutoFixable: true},
		},
		AutoFixAvail:   true,
		AutoFixCommand: "ntm --robot-diagnose=production --fix",
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	summary := decoded["summary"].(map[string]interface{})
	if summary["total_panes"].(float64) != 8 {
		t.Errorf("total_panes = %v, want 8", summary["total_panes"])
	}
	if summary["healthy"].(float64) != 5 {
		t.Errorf("healthy = %v, want 5", summary["healthy"])
	}

	recs := decoded["recommendations"].([]interface{})
	if len(recs) != 2 {
		t.Errorf("recommendations count = %d, want 2", len(recs))
	}

	if decoded["auto_fix_available"].(bool) != true {
		t.Error("auto_fix_available should be true")
	}
	if decoded["auto_fix_command"].(string) != "ntm --robot-diagnose=production --fix" {
		t.Errorf("auto_fix_command = %v", decoded["auto_fix_command"])
	}
}

// =============================================================================
// Monitor config — MonitorConfig struct tests
// =============================================================================

func TestMonitorConfig_Defaults(t *testing.T) {
	t.Parallel()

	config := MonitorConfig{
		Session:  "test",
		Interval: 30 * time.Second,
	}

	if config.Session != "test" {
		t.Errorf("Session = %q, want 'test'", config.Session)
	}
	if config.Interval != 30*time.Second {
		t.Errorf("Interval = %v, want 30s", config.Interval)
	}
	if config.IncludeCaut {
		t.Error("IncludeCaut should default to false")
	}
}
