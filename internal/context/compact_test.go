package context

import (
	"strings"
	"testing"
	"time"
)

func TestNewCompactor(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())

	tests := []struct {
		name             string
		cfg              CompactorConfig
		wantMinReduction float64
	}{
		{
			name:             "default config",
			cfg:              DefaultCompactorConfig(),
			wantMinReduction: 0.10,
		},
		{
			name:             "custom config",
			cfg:              CompactorConfig{MinReduction: 0.20, BuiltinTimeout: 20 * time.Second},
			wantMinReduction: 0.20,
		},
		{
			name:             "zero values get defaults",
			cfg:              CompactorConfig{},
			wantMinReduction: 0.10,
		},
		{
			name:             "negative values get defaults",
			cfg:              CompactorConfig{MinReduction: -0.5},
			wantMinReduction: 0.10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := NewCompactor(monitor, tt.cfg)
			if c.minReduction != tt.wantMinReduction {
				t.Errorf("minReduction = %f, want %f", c.minReduction, tt.wantMinReduction)
			}
		})
	}
}

func TestGetAgentCapabilities(t *testing.T) {
	t.Parallel()

	tests := []struct {
		agentType      string
		wantBuiltin    bool
		wantClear      bool
		wantBuiltinCmd string
	}{
		{"claude", true, true, "/compact"},
		{"cc", true, true, "/compact"},
		{"claude-code", true, true, "/compact"},
		{"claude_code", true, true, "/compact"},
		{"codex", false, false, ""},
		{"cod", false, false, ""},
		{"openai-codex", false, false, ""},
		{"gemini", false, true, ""},
		{"gmi", false, true, ""},
		{"google-gemini", false, true, ""},
		{"ws", false, false, ""},
		{"ollama", false, false, ""},
		{"unknown", false, false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			t.Parallel()
			caps := GetAgentCapabilities(tt.agentType)
			if caps.SupportsBuiltinCompact != tt.wantBuiltin {
				t.Errorf("SupportsBuiltinCompact = %v, want %v", caps.SupportsBuiltinCompact, tt.wantBuiltin)
			}
			if caps.SupportsHistoryClear != tt.wantClear {
				t.Errorf("SupportsHistoryClear = %v, want %v", caps.SupportsHistoryClear, tt.wantClear)
			}
			if caps.BuiltinCompactCommand != tt.wantBuiltinCmd {
				t.Errorf("BuiltinCompactCommand = %q, want %q", caps.BuiltinCompactCommand, tt.wantBuiltinCmd)
			}
		})
	}
}

func TestGetCompactionCommands(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	c := NewCompactor(monitor, DefaultCompactorConfig())

	tests := []struct {
		agentType    string
		wantCommands []string
		wantMethods  []CompactionMethod
	}{
		{
			agentType:    "claude",
			wantCommands: []string{"/compact"},
			wantMethods:  []CompactionMethod{CompactionBuiltin},
		},
		{
			agentType: "codex",
		},
		{
			agentType: "gemini",
		},
		{
			agentType:    "omp",
			wantCommands: []string{"/compact"},
			wantMethods:  []CompactionMethod{CompactionBuiltin},
		},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			t.Parallel()
			cmds := c.GetCompactionCommands(tt.agentType)
			if len(cmds) != len(tt.wantCommands) {
				t.Fatalf("got %d commands, want %d", len(cmds), len(tt.wantCommands))
			}
			for i, cmd := range cmds {
				if cmd.Command != tt.wantCommands[i] {
					t.Fatalf("command[%d] = %q, want %q", i, cmd.Command, tt.wantCommands[i])
				}
				if cmd.Method != tt.wantMethods[i] {
					t.Fatalf("method[%d] = %s, want %s", i, cmd.Method, tt.wantMethods[i])
				}
			}
		})
	}
}

func TestShouldTryCompaction(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	c := NewCompactor(monitor, DefaultCompactorConfig())

	// Register an agent
	monitor.RegisterAgent("test-agent", "pane1", "claude-opus-4")

	// Record enough messages to get above threshold
	for i := 0; i < 100; i++ {
		monitor.RecordMessage("test-agent", 1000, 2000)
	}

	// Test with low threshold (should trigger - usage is above threshold)
	shouldCompact, reason := c.ShouldTryCompaction("test-agent", 0.50) // 50% threshold
	if !shouldCompact {
		t.Errorf("expected shouldCompact=true at low threshold, reason: %s", reason)
	}

	// Test with very high threshold (should not trigger - usage is below threshold)
	// With 100 messages * 3000 tokens = 300k, usage is ~105%, so use >105%
	shouldCompact, reason = c.ShouldTryCompaction("test-agent", 2.0) // 200% threshold
	if shouldCompact {
		t.Errorf("expected shouldCompact=false at high threshold, reason: %s", reason)
	}

	// Test with unregistered agent
	shouldCompact, reason = c.ShouldTryCompaction("nonexistent", 0.5)
	if shouldCompact {
		t.Errorf("expected shouldCompact=false for unregistered agent, reason: %s", reason)
	}
}

func TestEvaluateCompactionResult(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	c := NewCompactor(monitor, CompactorConfig{MinReduction: 0.10})

	tests := []struct {
		name        string
		before      *ContextEstimate
		after       *ContextEstimate
		wantSuccess bool
	}{
		{
			name: "significant reduction",
			before: &ContextEstimate{
				TokensUsed:   150000,
				ContextLimit: 200000,
				UsagePercent: 75.0, // 75% - UsagePercent is 0-100 scale
			},
			after: &ContextEstimate{
				TokensUsed:   100000,
				ContextLimit: 200000,
				UsagePercent: 50.0, // 50% - 25 percentage point reduction
			},
			wantSuccess: true,
		},
		{
			name: "minimal reduction",
			before: &ContextEstimate{
				TokensUsed:   150000,
				ContextLimit: 200000,
				UsagePercent: 75.0,
			},
			after: &ContextEstimate{
				TokensUsed:   145000,
				ContextLimit: 200000,
				UsagePercent: 72.5, // Only 2.5 percentage point reduction < 10% minimum
			},
			wantSuccess: false,
		},
		{
			name: "no reduction",
			before: &ContextEstimate{
				TokensUsed:   150000,
				ContextLimit: 200000,
				UsagePercent: 75.0,
			},
			after: &ContextEstimate{
				TokensUsed:   155000,
				ContextLimit: 200000,
				UsagePercent: 77.5, // Actually increased
			},
			wantSuccess: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := c.EvaluateCompactionResult(tt.before, tt.after)
			if result.Success != tt.wantSuccess {
				t.Errorf("Success = %v, want %v", result.Success, tt.wantSuccess)
			}
			if result.TokensBefore != tt.before.TokensUsed {
				t.Errorf("TokensBefore = %d, want %d", result.TokensBefore, tt.before.TokensUsed)
			}
			if result.TokensAfter != tt.after.TokensUsed {
				t.Errorf("TokensAfter = %d, want %d", result.TokensAfter, tt.after.TokensUsed)
			}
		})
	}
}

func TestCompactionState(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	c := NewCompactor(monitor, DefaultCompactorConfig())

	// Register an agent
	monitor.RegisterAgent("test-agent", "pane1", "claude-opus-4")
	monitor.RecordMessage("test-agent", 500, 500)

	// Create state
	state, err := c.NewCompactionState("test-agent")
	if err != nil {
		t.Fatalf("NewCompactionState failed: %v", err)
	}

	if state.AgentID != "test-agent" {
		t.Errorf("AgentID = %q, want test-agent", state.AgentID)
	}
	if state.EstimateBefore == nil {
		t.Error("EstimateBefore should not be nil")
	}
	if state.StartedAt.IsZero() {
		t.Error("StartedAt should not be zero")
	}

	// Update state
	cmd := CompactionCommand{
		Command:  "/compact",
		WaitTime: 10 * time.Second,
	}
	state.UpdateState(cmd, CompactionBuiltin)

	if state.CommandsSent != 1 {
		t.Errorf("CommandsSent = %d, want 1", state.CommandsSent)
	}
	if state.Method != CompactionBuiltin {
		t.Errorf("Method = %s, want %s", state.Method, CompactionBuiltin)
	}
}

func TestPreRotationCheck(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	c := NewCompactor(monitor, DefaultCompactorConfig())

	// Register an agent
	monitor.RegisterAgent("test-agent", "pane1", "claude-opus-4")

	tests := []struct {
		name            string
		messageCount    int
		rotateThreshold float64
		wantRotate      bool
	}{
		{
			name:            "below threshold",
			messageCount:    10,
			rotateThreshold: 0.95,
			wantRotate:      false,
		},
		{
			name:            "at very low threshold",
			messageCount:    100,
			rotateThreshold: 0.01,
			wantRotate:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset and add messages
			monitor.ResetAgent("test-agent")
			for i := 0; i < tt.messageCount; i++ {
				monitor.RecordMessage("test-agent", 500, 500)
			}

			shouldRotate, reason := c.PreRotationCheck("test-agent", tt.rotateThreshold, nil)
			if shouldRotate != tt.wantRotate {
				t.Errorf("shouldRotate = %v, want %v, reason: %s", shouldRotate, tt.wantRotate, reason)
			}
		})
	}
}

func TestFormatCompactionResult(t *testing.T) {
	t.Parallel()

	successResult := &CompactionResult{
		Success:         true,
		Method:          CompactionBuiltin,
		TokensBefore:    150000,
		TokensAfter:     100000,
		TokensReclaimed: 50000,
		UsageBefore:     75.0, // UsagePercent is 0-100 scale
		UsageAfter:      50.0,
		Duration:        5 * time.Second,
	}

	output := successResult.FormatForDisplay()

	if !strings.Contains(output, "✓") {
		t.Error("success result should have checkmark")
	}
	if !strings.Contains(output, "75.0%") {
		t.Error("should show before percentage")
	}
	if !strings.Contains(output, "50.0%") {
		t.Error("should show after percentage")
	}
	if !strings.Contains(output, "50000") {
		t.Error("should show tokens reclaimed")
	}

	failResult := &CompactionResult{
		Success: false,
		Error:   "insufficient reduction",
	}

	output = failResult.FormatForDisplay()
	if !strings.Contains(output, "✗") {
		t.Error("failure result should have X mark")
	}
	if !strings.Contains(output, "insufficient reduction") {
		t.Error("should show error message")
	}
}

func TestCompactorWithNilMonitor(t *testing.T) {
	t.Parallel()

	c := NewCompactor(nil, DefaultCompactorConfig())

	// Test ShouldTryCompaction with nil monitor
	shouldCompact, reason := c.ShouldTryCompaction("any-agent", 0.5)
	if shouldCompact {
		t.Error("should not try compaction with nil monitor")
	}
	if !strings.Contains(reason, "no monitor") {
		t.Errorf("reason should mention no monitor: %s", reason)
	}

	// Test NewCompactionState with nil monitor
	state, err := c.NewCompactionState("any-agent")
	if state != nil {
		t.Error("NewCompactionState should return nil state with nil monitor")
	}
	if err == nil || !strings.Contains(err.Error(), "no monitor") {
		t.Errorf("NewCompactionState error should mention no monitor: %v", err)
	}

	// Test FinishCompaction with nil monitor
	// Create a dummy state for testing
	dummyState := &CompactionState{
		AgentID: "test",
		Method:  CompactionSummarize,
	}
	result, err := c.FinishCompaction(dummyState)
	if err == nil || !strings.Contains(err.Error(), "no monitor") {
		t.Errorf("FinishCompaction error should mention no monitor: %v", err)
	}
	if result == nil || result.Success {
		t.Error("FinishCompaction result should indicate failure with nil monitor")
	}

	// Test PreRotationCheck with nil monitor
	shouldRotate, reason := c.PreRotationCheck("any-agent", 0.95, nil)
	if !shouldRotate {
		t.Error("PreRotationCheck should return true (proceed with rotation) when no monitor")
	}
	if !strings.Contains(reason, "no monitor") {
		t.Errorf("reason should mention no monitor: %s", reason)
	}
}

func TestFinishCompaction(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	c := NewCompactor(monitor, DefaultCompactorConfig())

	// Register an agent
	monitor.RegisterAgent("test-agent", "pane1", "claude-opus-4")
	for i := 0; i < 50; i++ {
		monitor.RecordMessage("test-agent", 250, 250)
	}

	// Create state
	state, err := c.NewCompactionState("test-agent")
	if err != nil {
		t.Fatalf("NewCompactionState failed: %v", err)
	}
	state.Method = CompactionSummarize

	// Simulate some time passing
	time.Sleep(10 * time.Millisecond)

	// Finish compaction
	result, err := c.FinishCompaction(state)
	if err != nil {
		t.Fatalf("FinishCompaction failed: %v", err)
	}

	if result.Method != CompactionSummarize {
		t.Errorf("Method = %s, want %s", result.Method, CompactionSummarize)
	}
	if result.Duration < 10*time.Millisecond {
		t.Errorf("Duration = %v, expected >= 10ms", result.Duration)
	}
	if result.UsageBefore <= 0 {
		t.Error("UsageBefore should be > 0")
	}
}
