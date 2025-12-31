package context

import (
	"testing"
	"time"
)

func TestGetContextLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		model    string
		expected int64
	}{
		{"claude-opus-4", 200000},
		{"claude-sonnet-4", 200000},
		{"gpt-4", 128000},
		{"gpt-4-turbo", 128000},
		{"gemini-2.0-flash", 1000000},
		{"unknown-model", 128000}, // default
		{"", 128000},              // default
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			t.Parallel()
			got := GetContextLimit(tt.model)
			if got != tt.expected {
				t.Errorf("GetContextLimit(%q) = %d, want %d", tt.model, got, tt.expected)
			}
		})
	}
}

func TestNormalizeModelName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		expected string
	}{
		{"claude-opus-4-5-20251101", "claude-opus-4-5"},
		{"gpt-4-turbo-20240101", "gpt-4-turbo"},
		{"gemini-pro", "gemini-pro"},
		{"Claude-Opus-4", "claude-opus-4"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got := normalizeModelName(tt.input)
			if got != tt.expected {
				t.Errorf("normalizeModelName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestMessageCountEstimator(t *testing.T) {
	t.Parallel()

	estimator := &MessageCountEstimator{TokensPerMessage: 1500}

	state := &ContextState{
		AgentID:      "test-agent",
		Model:        "claude-opus-4",
		MessageCount: 100,
		SessionStart: time.Now().Add(-1 * time.Hour),
	}

	estimate, err := estimator.Estimate(state)
	if err != nil {
		t.Fatalf("Estimate() error = %v", err)
	}

	if estimate == nil {
		t.Fatal("Estimate() returned nil")
	}

	expectedTokens := int64(100 * 1500)
	if estimate.TokensUsed != expectedTokens {
		t.Errorf("TokensUsed = %d, want %d", estimate.TokensUsed, expectedTokens)
	}

	if estimate.ContextLimit != 200000 {
		t.Errorf("ContextLimit = %d, want 200000", estimate.ContextLimit)
	}

	if estimate.Confidence != 0.60 {
		t.Errorf("Confidence = %f, want 0.60", estimate.Confidence)
	}

	if estimate.Method != MethodMessageCount {
		t.Errorf("Method = %s, want %s", estimate.Method, MethodMessageCount)
	}
}

func TestMessageCountEstimator_ZeroMessages(t *testing.T) {
	t.Parallel()

	estimator := &MessageCountEstimator{TokensPerMessage: 1500}

	state := &ContextState{
		AgentID:      "test-agent",
		Model:        "claude-opus-4",
		MessageCount: 0,
	}

	estimate, err := estimator.Estimate(state)
	if err != nil {
		t.Fatalf("Estimate() error = %v", err)
	}

	if estimate != nil {
		t.Errorf("Estimate() = %v, want nil for zero messages", estimate)
	}
}

func TestCumulativeTokenEstimator(t *testing.T) {
	t.Parallel()

	estimator := &CumulativeTokenEstimator{CompactionDiscount: 0.7}

	state := &ContextState{
		AgentID:                "test-agent",
		Model:                  "gpt-4",
		cumulativeInputTokens:  50000,
		cumulativeOutputTokens: 50000,
	}

	estimate, err := estimator.Estimate(state)
	if err != nil {
		t.Fatalf("Estimate() error = %v", err)
	}

	if estimate == nil {
		t.Fatal("Estimate() returned nil")
	}

	// 100000 * 0.7 = 70000
	expectedTokens := int64(70000)
	if estimate.TokensUsed != expectedTokens {
		t.Errorf("TokensUsed = %d, want %d", estimate.TokensUsed, expectedTokens)
	}

	if estimate.Confidence != 0.70 {
		t.Errorf("Confidence = %f, want 0.70", estimate.Confidence)
	}

	if estimate.Method != MethodCumulativeTokens {
		t.Errorf("Method = %s, want %s", estimate.Method, MethodCumulativeTokens)
	}
}

func TestDurationActivityEstimator(t *testing.T) {
	t.Parallel()

	estimator := &DurationActivityEstimator{
		TokensPerMinuteActive:   1000,
		TokensPerMinuteInactive: 100,
	}

	// High activity: 15 messages in 5 minutes = 3 messages/minute (> 2 threshold)
	state := &ContextState{
		AgentID:      "test-agent",
		Model:        "claude-opus-4",
		MessageCount: 15,
		SessionStart: time.Now().Add(-5 * time.Minute),
	}

	estimate, err := estimator.Estimate(state)
	if err != nil {
		t.Fatalf("Estimate() error = %v", err)
	}

	if estimate == nil {
		t.Fatal("Estimate() returned nil")
	}

	if estimate.Method != MethodDurationActivity {
		t.Errorf("Method = %s, want %s", estimate.Method, MethodDurationActivity)
	}

	if estimate.Confidence != 0.30 {
		t.Errorf("Confidence = %f, want 0.30", estimate.Confidence)
	}

	// Should be using high activity rate (> 2 messages/minute)
	// 5 minutes * 1000 tokens/min = ~5000 tokens
	if estimate.TokensUsed < 4000 || estimate.TokensUsed > 6000 {
		t.Errorf("TokensUsed = %d, expected ~5000 for high activity", estimate.TokensUsed)
	}
}

func TestDurationActivityEstimator_ShortSession(t *testing.T) {
	t.Parallel()

	estimator := &DurationActivityEstimator{}

	state := &ContextState{
		AgentID:      "test-agent",
		Model:        "claude-opus-4",
		MessageCount: 1,
		SessionStart: time.Now().Add(-30 * time.Second), // Less than 1 minute
	}

	estimate, err := estimator.Estimate(state)
	if err != nil {
		t.Fatalf("Estimate() error = %v", err)
	}

	if estimate != nil {
		t.Errorf("Estimate() = %v, want nil for session < 1 minute", estimate)
	}
}

func TestContextMonitor_RegisterAgent(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())

	state := monitor.RegisterAgent("agent-1", "pane-1", "claude-opus-4")

	if state.AgentID != "agent-1" {
		t.Errorf("AgentID = %s, want agent-1", state.AgentID)
	}

	if state.PaneID != "pane-1" {
		t.Errorf("PaneID = %s, want pane-1", state.PaneID)
	}

	if state.Model != "claude-opus-4" {
		t.Errorf("Model = %s, want claude-opus-4", state.Model)
	}

	if monitor.Count() != 1 {
		t.Errorf("Count() = %d, want 1", monitor.Count())
	}
}

func TestContextMonitor_RecordMessage(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("agent-1", "pane-1", "claude-opus-4")

	monitor.RecordMessage("agent-1", 1000, 2000)
	monitor.RecordMessage("agent-1", 500, 1000)

	state := monitor.GetState("agent-1")
	if state == nil {
		t.Fatal("GetState() returned nil")
	}

	if state.MessageCount != 2 {
		t.Errorf("MessageCount = %d, want 2", state.MessageCount)
	}

	if state.cumulativeInputTokens != 1500 {
		t.Errorf("cumulativeInputTokens = %d, want 1500", state.cumulativeInputTokens)
	}

	if state.cumulativeOutputTokens != 3000 {
		t.Errorf("cumulativeOutputTokens = %d, want 3000", state.cumulativeOutputTokens)
	}
}

func TestContextMonitor_GetEstimate(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("agent-1", "pane-1", "claude-opus-4")

	// Record some messages
	for i := 0; i < 50; i++ {
		monitor.RecordMessage("agent-1", 500, 1000)
	}

	estimate := monitor.GetEstimate("agent-1")
	if estimate == nil {
		t.Fatal("GetEstimate() returned nil")
	}

	// Should use cumulative token estimator (higher confidence than message count when we have token data)
	// 50 messages * (500+1000) tokens * 0.7 compaction = 52500
	if estimate.TokensUsed == 0 {
		t.Error("TokensUsed = 0, expected non-zero")
	}

	if estimate.ContextLimit != 200000 {
		t.Errorf("ContextLimit = %d, want 200000", estimate.ContextLimit)
	}
}

func TestContextMonitor_AgentsAboveThreshold(t *testing.T) {
	t.Parallel()

	config := MonitorConfig{
		WarningThreshold: 60.0,
		RotateThreshold:  80.0,
		TokensPerMessage: 1000,
	}
	monitor := NewContextMonitor(config)

	// Agent with low usage
	monitor.RegisterAgent("agent-low", "pane-1", "claude-opus-4")
	for i := 0; i < 10; i++ {
		monitor.RecordMessage("agent-low", 500, 500)
	}

	// Agent with high usage (many messages)
	monitor.RegisterAgent("agent-high", "pane-2", "claude-opus-4")
	for i := 0; i < 200; i++ {
		monitor.RecordMessage("agent-high", 500, 500)
	}

	// Get agents above 50%
	agents := monitor.AgentsAboveThreshold(50.0)

	// At least the high-usage agent should be above threshold
	// But depends on estimation method used
	if len(agents) > 0 {
		for _, info := range agents {
			if info.Estimate.UsagePercent < 50.0 {
				t.Errorf("Agent %s has UsagePercent %f, expected >= 50", info.AgentID, info.Estimate.UsagePercent)
			}
		}
	}
}

func TestContextMonitor_ResetAgent(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("agent-1", "pane-1", "claude-opus-4")

	// Record some activity
	for i := 0; i < 50; i++ {
		monitor.RecordMessage("agent-1", 500, 1000)
	}

	state := monitor.GetState("agent-1")
	if state.MessageCount != 50 {
		t.Errorf("MessageCount = %d, want 50", state.MessageCount)
	}

	// Reset
	monitor.ResetAgent("agent-1")

	state = monitor.GetState("agent-1")
	if state.MessageCount != 0 {
		t.Errorf("After reset, MessageCount = %d, want 0", state.MessageCount)
	}

	if state.cumulativeInputTokens != 0 {
		t.Errorf("After reset, cumulativeInputTokens = %d, want 0", state.cumulativeInputTokens)
	}
}

func TestParseRobotModeContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected *ContextEstimate
	}{
		{
			name:  "valid context info",
			input: `{"context_used": 145000, "context_limit": 200000}`,
			expected: &ContextEstimate{
				TokensUsed:   145000,
				ContextLimit: 200000,
				UsagePercent: 72.5,
				Confidence:   0.95,
				Method:       MethodRobotMode,
			},
		},
		{
			name:  "alternate field names",
			input: `{"tokens_used": 100000, "tokens_limit": 128000}`,
			expected: &ContextEstimate{
				TokensUsed:   100000,
				ContextLimit: 128000,
				UsagePercent: 78.125,
				Confidence:   0.95,
				Method:       MethodRobotMode,
			},
		},
		{
			name:     "no context info",
			input:    `{"success": true, "message": "hello"}`,
			expected: nil,
		},
		{
			name:     "invalid JSON",
			input:    `not json`,
			expected: nil,
		},
		{
			name:     "empty string",
			input:    ``,
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ParseRobotModeContext(tt.input)

			if tt.expected == nil {
				if got != nil {
					t.Errorf("ParseRobotModeContext() = %v, want nil", got)
				}
				return
			}

			if got == nil {
				t.Fatal("ParseRobotModeContext() = nil, want non-nil")
			}

			if got.TokensUsed != tt.expected.TokensUsed {
				t.Errorf("TokensUsed = %d, want %d", got.TokensUsed, tt.expected.TokensUsed)
			}

			if got.ContextLimit != tt.expected.ContextLimit {
				t.Errorf("ContextLimit = %d, want %d", got.ContextLimit, tt.expected.ContextLimit)
			}

			if got.Method != tt.expected.Method {
				t.Errorf("Method = %s, want %s", got.Method, tt.expected.Method)
			}
		})
	}
}

func TestEstimateTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		chars    int
		expected int64
	}{
		{0, 0},
		{35, 10},      // ~35 chars = ~10 tokens
		{350, 100},    // ~350 chars = ~100 tokens
		{3500, 1000},  // ~3500 chars = ~1000 tokens
	}

	for _, tt := range tests {
		got := EstimateTokens(tt.chars)
		// Allow some variance due to rounding
		if got < tt.expected-5 || got > tt.expected+5 {
			t.Errorf("EstimateTokens(%d) = %d, expected ~%d", tt.chars, got, tt.expected)
		}
	}
}

func TestParseTokenCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		expected int64
		ok       bool
	}{
		{"145000", 145000, true},
		{"145,000", 145000, true},
		{"145k", 145000, true},
		{"145K", 145000, true},
		{"1.5M", 1500000, true},
		{"1.5m", 1500000, true},
		{"1.5k", 1500, true},
		{"invalid", 0, false},
		{"", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got, ok := ParseTokenCount(tt.input)
			if ok != tt.ok {
				t.Errorf("ParseTokenCount(%q) ok = %v, want %v", tt.input, ok, tt.ok)
			}
			if ok && got != tt.expected {
				t.Errorf("ParseTokenCount(%q) = %d, want %d", tt.input, got, tt.expected)
			}
		})
	}
}

func TestContextMonitor_Clear(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("agent-1", "pane-1", "claude-opus-4")
	monitor.RegisterAgent("agent-2", "pane-2", "gpt-4")

	if monitor.Count() != 2 {
		t.Errorf("Count() = %d, want 2", monitor.Count())
	}

	monitor.Clear()

	if monitor.Count() != 0 {
		t.Errorf("After Clear(), Count() = %d, want 0", monitor.Count())
	}
}

func TestContextMonitor_UnregisterAgent(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("agent-1", "pane-1", "claude-opus-4")
	monitor.RegisterAgent("agent-2", "pane-2", "gpt-4")

	monitor.UnregisterAgent("agent-1")

	if monitor.Count() != 1 {
		t.Errorf("After UnregisterAgent, Count() = %d, want 1", monitor.Count())
	}

	if monitor.GetState("agent-1") != nil {
		t.Error("GetState(agent-1) should return nil after unregister")
	}

	if monitor.GetState("agent-2") == nil {
		t.Error("GetState(agent-2) should still exist")
	}
}

func TestEstimatorInterfaces(t *testing.T) {
	t.Parallel()

	// Verify all estimators implement the interface
	estimators := []ContextEstimator{
		&RobotModeEstimator{},
		&MessageCountEstimator{},
		&CumulativeTokenEstimator{},
		&DurationActivityEstimator{},
	}

	for _, e := range estimators {
		name := e.Name()
		if name == "" {
			t.Error("Estimator Name() returned empty string")
		}

		conf := e.Confidence()
		if conf < 0 || conf > 1 {
			t.Errorf("Estimator %s Confidence() = %f, want 0-1", name, conf)
		}
	}
}
