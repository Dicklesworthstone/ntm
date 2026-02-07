package context

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewHandoffTrigger(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	predictor := NewContextPredictor(DefaultPredictorConfig())

	trigger := NewHandoffTrigger(
		DefaultHandoffTriggerConfig(),
		monitor,
		predictor,
	)

	if trigger == nil {
		t.Fatal("NewHandoffTrigger() returned nil")
	}

	if trigger.config.WarnThreshold != 70.0 {
		t.Errorf("WarnThreshold = %f, want 70.0", trigger.config.WarnThreshold)
	}

	if trigger.config.TriggerThreshold != 75.0 {
		t.Errorf("TriggerThreshold = %f, want 75.0", trigger.config.TriggerThreshold)
	}
}

func TestNewHandoffTrigger_ZeroConfig(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	predictor := NewContextPredictor(DefaultPredictorConfig())

	// Zero-value config: all defaults should be filled in
	trigger := NewHandoffTrigger(HandoffTriggerConfig{}, monitor, predictor)

	if trigger == nil {
		t.Fatal("NewHandoffTrigger() returned nil")
	}

	if trigger.config.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %v, want 30s", trigger.config.PollInterval)
	}
	if trigger.config.WarnThreshold != 70.0 {
		t.Errorf("WarnThreshold = %f, want 70.0", trigger.config.WarnThreshold)
	}
	if trigger.config.TriggerThreshold != 75.0 {
		t.Errorf("TriggerThreshold = %f, want 75.0", trigger.config.TriggerThreshold)
	}
	if trigger.config.Cooldown != 5*time.Minute {
		t.Errorf("Cooldown = %v, want 5m", trigger.config.Cooldown)
	}
}

func TestHandoffTrigger_SetCallbacks(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	predictor := NewContextPredictor(DefaultPredictorConfig())
	trigger := NewHandoffTrigger(DefaultHandoffTriggerConfig(), monitor, predictor)

	warningCalled := false
	triggeredCalled := false

	trigger.SetWarningHandler(func(event HandoffTriggerEvent) {
		warningCalled = true
	})

	trigger.SetTriggeredHandler(func(event HandoffTriggerEvent) {
		triggeredCalled = true
	})

	// Verify handlers are set (they'll be called internally)
	if trigger.onWarning == nil {
		t.Error("onWarning handler not set")
	}
	if trigger.onTriggered == nil {
		t.Error("onTriggered handler not set")
	}

	// We can't easily test callback invocation without more setup,
	// but we verified they were set
	_ = warningCalled
	_ = triggeredCalled
}

func TestHandoffTrigger_CanTrigger(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	predictor := NewContextPredictor(DefaultPredictorConfig())

	config := DefaultHandoffTriggerConfig()
	config.Cooldown = 1 * time.Second
	trigger := NewHandoffTrigger(config, monitor, predictor)

	// First trigger should be allowed
	if !trigger.canTrigger("agent-1") {
		t.Error("first trigger should be allowed")
	}

	// Record a trigger
	trigger.mu.Lock()
	trigger.lastHandoff["agent-1"] = time.Now()
	trigger.mu.Unlock()

	// Immediate second trigger should be blocked
	if trigger.canTrigger("agent-1") {
		t.Error("immediate second trigger should be blocked by cooldown")
	}

	// Wait for cooldown
	time.Sleep(1100 * time.Millisecond)

	// Now trigger should be allowed
	if !trigger.canTrigger("agent-1") {
		t.Error("trigger should be allowed after cooldown")
	}
}

func TestHandoffTrigger_GetStatus(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	predictor := NewContextPredictor(DefaultPredictorConfig())
	trigger := NewHandoffTrigger(DefaultHandoffTriggerConfig(), monitor, predictor)

	// Set some state
	now := time.Now()
	trigger.mu.Lock()
	trigger.lastHandoff["agent-1"] = now
	trigger.activeAgents["agent-2"] = true
	trigger.mu.Unlock()

	status := trigger.GetStatus()

	if status["agent-1"].LastHandoff != now {
		t.Error("LastHandoff not correctly reported")
	}

	// agent-2 doesn't have a lastHandoff entry, so won't be in status
	// Only agents with lastHandoff are tracked
}

func TestShouldTriggerHandoff_NoEstimate(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())

	// Agent not registered
	rec := monitor.ShouldTriggerHandoff("unknown-agent", nil)

	if rec.ShouldTrigger {
		t.Error("ShouldTrigger should be false for unknown agent")
	}
	if rec.Reason != "no context estimate available" {
		t.Errorf("Reason = %q, want 'no context estimate available'", rec.Reason)
	}
}

func TestShouldTriggerHandoff_BelowThreshold(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("agent-1", "pane-1", "claude-opus-4")

	// Set low usage (50k tokens out of 200k = 25%)
	state := monitor.GetState("agent-1")
	state.cumulativeInputTokens = 25000
	state.cumulativeOutputTokens = 25000

	rec := monitor.ShouldTriggerHandoff("agent-1", nil)

	if rec.ShouldTrigger {
		t.Error("ShouldTrigger should be false for low usage")
	}
	if rec.ShouldWarn {
		t.Error("ShouldWarn should be false for low usage")
	}
}

func TestShouldTriggerHandoff_AtWarningThreshold(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("agent-1", "pane-1", "claude-opus-4")

	// Set usage to ~72% (need raw tokens that give 72% after 0.7 discount)
	// 72% after discount means ~103% raw, so ~205k raw tokens for 200k limit
	// But actually the cumulative estimator applies 0.7, so:
	// To get 72% usage: 72% * 200k / 0.7 = ~205k raw tokens
	state := monitor.GetState("agent-1")
	state.cumulativeInputTokens = 103000
	state.cumulativeOutputTokens = 103000

	rec := monitor.ShouldTriggerHandoff("agent-1", nil)

	// 206k * 0.7 / 200k = 72.1%
	if !rec.ShouldWarn {
		t.Errorf("ShouldWarn should be true at ~72%% usage (actual: %.1f%%)", rec.UsagePercent)
	}
	if rec.ShouldTrigger {
		t.Error("ShouldTrigger should be false at warning threshold")
	}
}

func TestShouldTriggerHandoff_AtTriggerThreshold(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("agent-1", "pane-1", "claude-opus-4")

	// Set usage to ~76% (above 75% trigger threshold)
	// 76% after 0.7 discount means ~109% raw = ~217k tokens
	state := monitor.GetState("agent-1")
	state.cumulativeInputTokens = 109000
	state.cumulativeOutputTokens = 109000

	rec := monitor.ShouldTriggerHandoff("agent-1", nil)

	// 218k * 0.7 / 200k = 76.3%
	if !rec.ShouldTrigger {
		t.Errorf("ShouldTrigger should be true at ~76%% usage (actual: %.1f%%)", rec.UsagePercent)
	}
	if !rec.ShouldWarn {
		t.Error("ShouldWarn should also be true above trigger threshold")
	}
}

func TestUpdateFromTranscript(t *testing.T) {
	t.Parallel()

	// Create a temp file to simulate a transcript
	tmpDir := t.TempDir()
	transcriptPath := filepath.Join(tmpDir, "session.jsonl")

	// Create a file with 4000 bytes (~1000 tokens at 4 bytes/token)
	content := make([]byte, 4000)
	for i := range content {
		content[i] = 'a'
	}
	if err := os.WriteFile(transcriptPath, content, 0644); err != nil {
		t.Fatalf("failed to write test transcript: %v", err)
	}

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgentWithTranscript(
		"agent-1", "pane-1", "claude-opus-4",
		"cc", "test-session", transcriptPath,
	)

	tokens, err := monitor.UpdateFromTranscript("agent-1")
	if err != nil {
		t.Fatalf("UpdateFromTranscript() error = %v", err)
	}

	// 4000 bytes / 3.5 ≈ 1142 tokens (conservative estimate)
	fileSize := float64(4000)
	expectedTokens := int64(fileSize / 3.5)
	if tokens != expectedTokens {
		t.Errorf("tokens = %d, want %d", tokens, expectedTokens)
	}

	// Verify state was updated
	state := monitor.GetState("agent-1")
	if state.cumulativeInputTokens+state.cumulativeOutputTokens != expectedTokens {
		t.Errorf("state tokens = %d, want %d",
			state.cumulativeInputTokens+state.cumulativeOutputTokens, expectedTokens)
	}
}

func TestUpdateFromTranscript_NoPath(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("agent-1", "pane-1", "claude-opus-4")

	// No transcript path configured
	tokens, err := monitor.UpdateFromTranscript("agent-1")
	if err != nil {
		t.Fatalf("UpdateFromTranscript() error = %v", err)
	}

	if tokens != 0 {
		t.Errorf("tokens = %d, want 0 for no transcript path", tokens)
	}
}

func TestUpdateFromTranscript_NonExistent(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgentWithTranscript(
		"agent-1", "pane-1", "claude-opus-4",
		"cc", "test-session", "/nonexistent/path/session.jsonl",
	)

	// Non-existent file should not error
	tokens, err := monitor.UpdateFromTranscript("agent-1")
	if err != nil {
		t.Fatalf("UpdateFromTranscript() error = %v", err)
	}

	if tokens != 0 {
		t.Errorf("tokens = %d, want 0 for non-existent file", tokens)
	}
}

func TestRegisterAgentWithTranscript(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())

	state := monitor.RegisterAgentWithTranscript(
		"agent-1", "pane-1", "claude-opus-4",
		"cc", "test-session", "/path/to/transcript.jsonl",
	)

	if state.AgentID != "agent-1" {
		t.Errorf("AgentID = %s, want agent-1", state.AgentID)
	}
	if state.AgentType != "cc" {
		t.Errorf("AgentType = %s, want cc", state.AgentType)
	}
	if state.SessionName != "test-session" {
		t.Errorf("SessionName = %s, want test-session", state.SessionName)
	}
	if state.TranscriptPath != "/path/to/transcript.jsonl" {
		t.Errorf("TranscriptPath = %s, want /path/to/transcript.jsonl", state.TranscriptPath)
	}
}

func TestShouldTriggerHandoff_WithPredictor_ExhaustionSoon(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("agent-1", "pane-1", "claude-opus-4")

	// Set usage to ~50% (below thresholds, so trigger should come from velocity)
	state := monitor.GetState("agent-1")
	state.cumulativeInputTokens = 50000
	state.cumulativeOutputTokens = 50000

	// Create predictor with samples showing high velocity
	cfg := DefaultPredictorConfig()
	cfg.MinSamples = 3
	cfg.Window = 10 * time.Minute
	predictor := NewContextPredictor(cfg)

	// Add samples: rapidly growing token usage (simulate high velocity)
	now := time.Now()
	predictor.AddSampleAt(150000, now.Add(-2*time.Minute))
	predictor.AddSampleAt(170000, now.Add(-1*time.Minute))
	predictor.AddSampleAt(190000, now)

	rec := monitor.ShouldTriggerHandoff("agent-1", predictor)

	// The predictor shows 20k tokens/min, context limit ~200k, ~190k used → ~0.5 min to exhaustion
	// This should trigger handoff
	if !rec.ShouldTrigger {
		t.Errorf("ShouldTrigger should be true with high velocity; usage=%.1f%% velocity=%.0f", rec.UsagePercent, rec.TokenVelocity)
	}
	if rec.TokenVelocity <= 0 {
		t.Errorf("TokenVelocity should be positive, got %.0f", rec.TokenVelocity)
	}
}

func TestShouldTriggerHandoff_WithPredictor_NoExhaustion(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("agent-1", "pane-1", "claude-opus-4")

	// Set usage to ~30%
	state := monitor.GetState("agent-1")
	state.cumulativeInputTokens = 30000
	state.cumulativeOutputTokens = 30000

	// Create predictor with low velocity
	cfg := DefaultPredictorConfig()
	cfg.MinSamples = 3
	cfg.Window = 10 * time.Minute
	predictor := NewContextPredictor(cfg)

	// Slow growth: 1k tokens/min
	now := time.Now()
	predictor.AddSampleAt(58000, now.Add(-2*time.Minute))
	predictor.AddSampleAt(59000, now.Add(-1*time.Minute))
	predictor.AddSampleAt(60000, now)

	rec := monitor.ShouldTriggerHandoff("agent-1", predictor)

	// 1k/min velocity, 140k remaining → 140 min → no trigger
	if rec.ShouldTrigger {
		t.Errorf("ShouldTrigger should be false with low velocity; usage=%.1f%% velocity=%.0f", rec.UsagePercent, rec.TokenVelocity)
	}
	if rec.ShouldWarn {
		t.Errorf("ShouldWarn should be false below warning threshold; usage=%.1f%%", rec.UsagePercent)
	}
}

func TestHandoffRecommendation_Thresholds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		rawTokens     int64 // Total tokens before 0.7 discount
		expectWarn    bool
		expectTrigger bool
	}{
		// claude-opus-4 has 200k limit
		// After 0.7 discount: reported = raw * 0.7 / 200k * 100
		{"50% usage (142k raw)", 142000, false, false}, // 142k * 0.7 / 200k = 49.7%
		{"65% usage (186k raw)", 186000, false, false}, // 186k * 0.7 / 200k = 65.1%
		{"70% usage (200k raw)", 200000, true, false},  // 200k * 0.7 / 200k = 70%
		{"72% usage (206k raw)", 206000, true, false},  // 206k * 0.7 / 200k = 72.1%
		{"75% usage (215k raw)", 215000, true, true},   // 215k * 0.7 / 200k = 75.25%
		{"80% usage (228k raw)", 228000, true, true},   // 228k * 0.7 / 200k = 79.8%
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			monitor := NewContextMonitor(DefaultMonitorConfig())
			monitor.RegisterAgent("test-agent", "pane-1", "claude-opus-4")

			state := monitor.GetState("test-agent")
			state.cumulativeInputTokens = tt.rawTokens / 2
			state.cumulativeOutputTokens = tt.rawTokens / 2

			rec := monitor.ShouldTriggerHandoff("test-agent", nil)

			t.Logf("HANDOFF_TEST: %s | RawTokens=%d | Usage=%.1f%% | Warn=%v | Trigger=%v",
				tt.name, tt.rawTokens, rec.UsagePercent, rec.ShouldWarn, rec.ShouldTrigger)

			if rec.ShouldWarn != tt.expectWarn {
				t.Errorf("ShouldWarn = %v, want %v", rec.ShouldWarn, tt.expectWarn)
			}
			if rec.ShouldTrigger != tt.expectTrigger {
				t.Errorf("ShouldTrigger = %v, want %v", rec.ShouldTrigger, tt.expectTrigger)
			}
		})
	}
}
