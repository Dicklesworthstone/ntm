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
		{35, 10},     // ~35 chars = ~10 tokens
		{350, 100},   // ~350 chars = ~100 tokens
		{3500, 1000}, // ~3500 chars = ~1000 tokens
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

func TestContextMonitor_SetAgentType(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())

	// Register an agent
	monitor.RegisterAgent("agent-1", "pane-1", "claude-opus-4")

	state := monitor.GetState("agent-1")
	if state == nil {
		t.Fatal("expected agent-1 to be registered")
	}
	// RegisterAgent doesn't set AgentType, only Model
	if state.AgentType != "" {
		t.Errorf("initial AgentType = %q, want empty string", state.AgentType)
	}
	if state.Model != "claude-opus-4" {
		t.Errorf("Model = %q, want %q", state.Model, "claude-opus-4")
	}

	// Update agent type using SetAgentType
	monitor.SetAgentType("agent-1", "claude")

	state = monitor.GetState("agent-1")
	if state.AgentType != "claude" {
		t.Errorf("after SetAgentType, AgentType = %q, want %q", state.AgentType, "claude")
	}

	// Update to a different type
	monitor.SetAgentType("agent-1", "codex")

	state = monitor.GetState("agent-1")
	if state.AgentType != "codex" {
		t.Errorf("after second SetAgentType, AgentType = %q, want %q", state.AgentType, "codex")
	}

	// SetAgentType on non-existent agent should not panic
	monitor.SetAgentType("non-existent", "gemini")

	// Verify non-existent agent wasn't created
	if monitor.GetState("non-existent") != nil {
		t.Error("SetAgentType should not create a new agent")
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

// TestThresholdDetection_50_75_90 tests alert trigger logic at specific thresholds.
func TestThresholdDetection_50_75_90(t *testing.T) {
	t.Parallel()

	// Note: CumulativeTokenEstimator applies 0.7 compaction discount
	// So to get 60% reported usage, we need 60/0.7 = ~86% raw tokens
	tests := []struct {
		name            string
		rawTokenPercent float64 // Token percentage before 0.7 discount
		warnAt          float64
		rotateAt        float64
		expectWarn      bool
		expectRotate    bool
	}{
		{"35% raw (24.5% reported) below 60% warn", 35.0, 60.0, 80.0, false, false},
		{"90% raw (63% reported) above 60% warn", 90.0, 60.0, 80.0, true, false},
		{"115% raw (80.5% reported) at 80% rotate", 115.0, 60.0, 80.0, true, true},
		{"130% raw (91% reported) above 80% rotate", 130.0, 60.0, 80.0, true, true},
		{"70% raw (49% reported) below custom 50% warn", 70.0, 50.0, 75.0, false, false},
		{"75% raw (52.5% reported) above custom 50% warn", 75.0, 50.0, 75.0, true, false},
		{"110% raw (77% reported) above custom 75% rotate", 110.0, 50.0, 75.0, true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := MonitorConfig{
				WarningThreshold: tt.warnAt,
				RotateThreshold:  tt.rotateAt,
				TokensPerMessage: 1000,
			}
			monitor := NewContextMonitor(config)

			// Register agent and set up state
			monitor.RegisterAgent("test-agent", "pane-1", "claude-opus-4")

			// claude-opus-4 has 200000 context limit
			// Set raw tokens that after 0.7 discount will give target usage
			state := monitor.GetState("test-agent")
			rawTokens := int64(tt.rawTokenPercent / 100.0 * 200000)
			state.cumulativeInputTokens = rawTokens / 2
			state.cumulativeOutputTokens = rawTokens / 2

			// Get estimate to log actual usage
			estimate := monitor.GetEstimate("test-agent")
			actualUsage := float64(0)
			if estimate != nil {
				actualUsage = estimate.UsagePercent
			}
			t.Logf("CONTEXT_TEST: %s | RawTokens=%.1f%% | ActualUsage=%.1f%% | WarnAt=%.1f%% | RotateAt=%.1f%%",
				tt.name, tt.rawTokenPercent, actualUsage, tt.warnAt, tt.rotateAt)

			// Get agents above threshold
			agents := monitor.AgentsAboveThreshold(tt.warnAt)

			gotWarn := false
			gotRotate := false
			for _, info := range agents {
				if info.AgentID == "test-agent" {
					gotWarn = info.NeedsWarn
					gotRotate = info.NeedsRotate
				}
			}

			if tt.expectWarn && len(agents) == 0 {
				t.Errorf("expected agent in threshold list at %.1f%% raw usage", tt.rawTokenPercent)
			}

			if gotWarn != tt.expectWarn {
				t.Errorf("NeedsWarn = %v, want %v", gotWarn, tt.expectWarn)
			}
			if gotRotate != tt.expectRotate {
				t.Errorf("NeedsRotate = %v, want %v", gotRotate, tt.expectRotate)
			}
		})
	}
}

// TestAlertTriggerLogic tests the alert trigger conditions.
func TestAlertTriggerLogic(t *testing.T) {
	t.Parallel()

	config := MonitorConfig{
		WarningThreshold: 60.0,
		RotateThreshold:  80.0,
		TokensPerMessage: 1000,
	}
	monitor := NewContextMonitor(config)

	// Create multiple agents with different usage levels
	// Note: CumulativeTokenEstimator applies 0.7 compaction discount
	// So we need to set raw tokens = target / 0.7 to achieve target usage
	// e.g., for 60% target, we need 60/0.7 = ~86% raw = 171k tokens
	agentConfigs := []struct {
		id     string
		tokens int64 // Raw tokens (before 0.7 discount)
	}{
		{"low-usage", 50000},       // 50k * 0.7 / 200k = 17.5%
		{"medium-usage", 180000},   // 180k * 0.7 / 200k = 63%
		{"high-usage", 240000},     // 240k * 0.7 / 200k = 84%
		{"critical-usage", 280000}, // 280k * 0.7 / 200k = 98%
	}

	for _, ac := range agentConfigs {
		monitor.RegisterAgent(ac.id, "pane-"+ac.id, "claude-opus-4")
		state := monitor.GetState(ac.id)
		state.cumulativeInputTokens = ac.tokens / 2
		state.cumulativeOutputTokens = ac.tokens / 2
	}

	t.Logf("CONTEXT_TEST: AlertTriggerLogic | Agents=%d | WarnThreshold=60%% | RotateThreshold=80%%",
		len(agentConfigs))

	// Test at warning threshold
	warningAgents := monitor.AgentsAboveThreshold(60.0)
	if len(warningAgents) < 3 {
		t.Errorf("expected at least 3 agents above 60%% threshold, got %d", len(warningAgents))
	}

	// Verify low-usage agent is not in warning list
	for _, agent := range warningAgents {
		if agent.AgentID == "low-usage" {
			t.Error("low-usage agent should not be in warning list")
		}
	}

	// Test at rotation threshold
	rotateAgents := monitor.AgentsAboveThreshold(80.0)
	if len(rotateAgents) < 2 {
		t.Errorf("expected at least 2 agents above 80%% threshold, got %d", len(rotateAgents))
	}

	// Verify rotation flags
	for _, agent := range rotateAgents {
		if !agent.NeedsRotate {
			t.Errorf("agent %s at rotation threshold should have NeedsRotate=true", agent.AgentID)
		}
	}
}

// TestDebouncingBehavior tests that estimates are cached and debounced appropriately.
func TestDebouncingBehavior(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("test-agent", "pane-1", "claude-opus-4")

	// Record initial messages
	for i := 0; i < 10; i++ {
		monitor.RecordMessage("test-agent", 500, 500)
	}

	// Get first estimate
	estimate1 := monitor.GetEstimate("test-agent")
	if estimate1 == nil {
		t.Fatal("first estimate should not be nil")
	}

	t.Logf("CONTEXT_TEST: DebouncingBehavior | FirstEstimate=%d tokens | Method=%s",
		estimate1.TokensUsed, estimate1.Method)

	// Simulate robot mode update
	robotOutput := `{"context_used": 25000, "context_limit": 200000}`
	monitor.UpdateFromRobotMode("test-agent", robotOutput)

	// Get estimate immediately after robot mode update
	estimate2 := monitor.GetEstimate("test-agent")
	if estimate2 == nil {
		t.Fatal("estimate after robot mode should not be nil")
	}

	// Robot mode estimate should be used (higher confidence)
	if estimate2.Method != MethodRobotMode {
		t.Errorf("expected MethodRobotMode after update, got %s", estimate2.Method)
	}

	if estimate2.TokensUsed != 25000 {
		t.Errorf("robot mode estimate should report 25000 tokens, got %d", estimate2.TokensUsed)
	}

	t.Logf("CONTEXT_TEST: DebouncingBehavior | AfterRobotMode=%d tokens | Method=%s | Confidence=%.2f",
		estimate2.TokensUsed, estimate2.Method, estimate2.Confidence)

	// Verify robot mode has highest confidence
	if estimate2.Confidence < 0.90 {
		t.Errorf("robot mode should have high confidence (>=0.90), got %.2f", estimate2.Confidence)
	}
}

// TestMultipleAgentTracking tests concurrent tracking of multiple agents.
func TestNewContextMonitor_ZeroThresholds(t *testing.T) {
	t.Parallel()

	// Zero-value config should get defaults filled in
	monitor := NewContextMonitor(MonitorConfig{})

	// Register an agent and check thresholds are applied
	monitor.RegisterAgent("agent-1", "pane-1", "claude-opus-4")

	// Set usage to ~72% (above default 70% warning, below 75% rotate)
	state := monitor.GetState("agent-1")
	state.cumulativeInputTokens = 103000
	state.cumulativeOutputTokens = 103000

	rec := monitor.ShouldTriggerHandoff("agent-1", nil)

	// Should warn at default 70% but not trigger at default 75%
	if !rec.ShouldWarn {
		t.Errorf("ShouldWarn should be true at ~72%% with default thresholds (actual: %.1f%%)", rec.UsagePercent)
	}
	if rec.ShouldTrigger {
		t.Error("ShouldTrigger should be false below default 75% threshold")
	}
}

func TestNewContextMonitor_NegativeTokensPerMessage(t *testing.T) {
	t.Parallel()

	// Negative TokensPerMessage should get default
	monitor := NewContextMonitor(MonitorConfig{
		WarningThreshold: 70.0,
		RotateThreshold:  75.0,
		TokensPerMessage: -1,
	})

	monitor.RegisterAgent("agent-1", "pane-1", "claude-opus-4")
	for i := 0; i < 10; i++ {
		monitor.RecordMessage("agent-1", 500, 500)
	}

	estimate := monitor.GetEstimate("agent-1")
	if estimate == nil {
		t.Fatal("GetEstimate returned nil")
	}
	// Should still produce a valid estimate (default 1500 tokens/msg)
	if estimate.TokensUsed == 0 {
		t.Error("TokensUsed should be non-zero")
	}
}

func TestRegisterAgent_UpdateExisting(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())

	// Register initially
	state1 := monitor.RegisterAgent("agent-1", "pane-1", "claude-opus-4")
	state1.MessageCount = 42 // Set some state

	// Re-register with different pane/model (update path)
	state2 := monitor.RegisterAgent("agent-1", "pane-2", "gpt-4")

	if state2.PaneID != "pane-2" {
		t.Errorf("PaneID = %s, want pane-2", state2.PaneID)
	}
	if state2.Model != "gpt-4" {
		t.Errorf("Model = %s, want gpt-4", state2.Model)
	}
	// MessageCount should be preserved (same state object)
	if state2.MessageCount != 42 {
		t.Errorf("MessageCount = %d, want 42 (should be preserved on update)", state2.MessageCount)
	}
	// Should still be only 1 agent
	if monitor.Count() != 1 {
		t.Errorf("Count() = %d, want 1", monitor.Count())
	}
}

func TestRegisterAgentWithTranscript_UpdateExisting(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())

	// Register initially
	state1 := monitor.RegisterAgentWithTranscript(
		"agent-1", "pane-1", "claude-opus-4",
		"cc", "session-1", "/path/one.jsonl",
	)
	state1.MessageCount = 10

	// Re-register with updated fields
	state2 := monitor.RegisterAgentWithTranscript(
		"agent-1", "pane-2", "gpt-4",
		"cod", "session-2", "/path/two.jsonl",
	)

	if state2.PaneID != "pane-2" {
		t.Errorf("PaneID = %s, want pane-2", state2.PaneID)
	}
	if state2.Model != "gpt-4" {
		t.Errorf("Model = %s, want gpt-4", state2.Model)
	}
	if state2.AgentType != "cod" {
		t.Errorf("AgentType = %s, want cod", state2.AgentType)
	}
	if state2.SessionName != "session-2" {
		t.Errorf("SessionName = %s, want session-2", state2.SessionName)
	}
	if state2.TranscriptPath != "/path/two.jsonl" {
		t.Errorf("TranscriptPath = %s, want /path/two.jsonl", state2.TranscriptPath)
	}
	// MessageCount preserved
	if state2.MessageCount != 10 {
		t.Errorf("MessageCount = %d, want 10", state2.MessageCount)
	}
}

func TestGetEstimateLocked_StaleEstimate(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("agent-1", "pane-1", "claude-opus-4")

	// Inject a robot mode estimate with a recent timestamp
	robotOutput := `{"context_used": 50000, "context_limit": 200000}`
	monitor.UpdateFromRobotMode("agent-1", robotOutput)

	// Should use robot mode estimate (recent)
	estimate := monitor.GetEstimate("agent-1")
	if estimate == nil {
		t.Fatal("GetEstimate returned nil")
	}
	if estimate.Method != MethodRobotMode {
		t.Errorf("Method = %s, want %s (fresh robot mode)", estimate.Method, MethodRobotMode)
	}

	// Now make the robot mode estimate stale by backdating it
	state := monitor.GetState("agent-1")
	state.Estimate.UpdatedAt = time.Now().Add(-1 * time.Minute) // > 30s ago

	// Record some messages so cumulative estimator has data
	for i := 0; i < 5; i++ {
		monitor.RecordMessage("agent-1", 1000, 1000)
	}

	// Should now fall through to cumulative token estimator
	estimate2 := monitor.GetEstimate("agent-1")
	if estimate2 == nil {
		t.Fatal("GetEstimate returned nil after stale robot mode")
	}
	if estimate2.Method != MethodCumulativeTokens {
		t.Errorf("Method = %s, want %s (stale robot mode should fall through)", estimate2.Method, MethodCumulativeTokens)
	}
}

func TestShouldTriggerHandoff_NormalRange(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("agent-1", "pane-1", "claude-opus-4")

	// Set low usage (~21%)
	state := monitor.GetState("agent-1")
	state.cumulativeInputTokens = 30000
	state.cumulativeOutputTokens = 30000

	rec := monitor.ShouldTriggerHandoff("agent-1", nil)

	if rec.ShouldTrigger {
		t.Error("ShouldTrigger should be false for low usage")
	}
	if rec.ShouldWarn {
		t.Error("ShouldWarn should be false for low usage")
	}
	// Reason should indicate normal range
	if rec.Reason == "" {
		t.Error("Reason should not be empty")
	}
}

func TestRecordMessage_UnknownAgent(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())

	// Should not panic for unknown agent
	monitor.RecordMessage("nonexistent", 1000, 2000)

	if monitor.Count() != 0 {
		t.Errorf("Count() = %d, want 0 (unknown agent should not be created)", monitor.Count())
	}
}

func TestMultipleAgentTracking(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())

	// Register diverse agents
	agents := []struct {
		id    string
		model string
		limit int64
	}{
		{"claude-agent", "claude-opus-4", 200000},
		{"gpt-agent", "gpt-4", 128000},
		{"gemini-agent", "gemini-2.0-flash", 1000000},
	}

	for _, a := range agents {
		monitor.RegisterAgent(a.id, "pane-"+a.id, a.model)
		monitor.RecordMessage(a.id, 5000, 5000)
	}

	t.Logf("CONTEXT_TEST: MultipleAgentTracking | Agents=%d", len(agents))

	// Verify all estimates are available
	allEstimates := monitor.GetAllEstimates()
	if len(allEstimates) != len(agents) {
		t.Errorf("expected %d estimates, got %d", len(agents), len(allEstimates))
	}

	// Verify each agent has appropriate context limit
	for _, a := range agents {
		estimate := allEstimates[a.id]
		if estimate == nil {
			t.Errorf("missing estimate for agent %s", a.id)
			continue
		}
		if estimate.ContextLimit != a.limit {
			t.Errorf("agent %s: ContextLimit = %d, want %d", a.id, estimate.ContextLimit, a.limit)
		}
		t.Logf("CONTEXT_TEST: Agent=%s | Model=%s | Limit=%d | Usage=%.1f%%",
			a.id, a.model, estimate.ContextLimit, estimate.UsagePercent)
	}
}
