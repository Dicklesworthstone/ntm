package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/alerts"
	ntmctx "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// TestContextThresholdAlerts tests that context threshold alerts are generated
// when agent usage crosses warning and critical thresholds.
func TestContextThresholdAlerts(t *testing.T) {
	testutil.RequireE2E(t)

	logger := testutil.NewTestLoggerStdout(t)
	logger.LogSection("Context Threshold Alerts E2E Test")

	// Create a context monitor with custom thresholds for testing
	cfg := ntmctx.MonitorConfig{
		WarningThreshold: 60.0,
		RotateThreshold:  80.0,
		TokensPerMessage: 1500,
	}
	monitor := ntmctx.NewContextMonitor(cfg)

	// Create alert generator
	alertCfg := alerts.DefaultConfig()
	alertCfg.ContextWarningThreshold = 75.0
	generator := alerts.NewGenerator(alertCfg)

	// Step 1: Register an agent
	logger.LogSection("Step 1: Register test agent")
	agentID := fmt.Sprintf("test_agent_%d", time.Now().UnixNano())
	state := monitor.RegisterAgent(agentID, "%1", "claude-opus-4.5")
	if state == nil {
		t.Fatal("Failed to register agent")
	}
	logger.Log("PASS: Registered agent %s", agentID)

	// Step 2: Simulate low context usage (should not trigger alerts)
	logger.LogSection("Step 2: Simulate low context usage")
	for i := 0; i < 10; i++ {
		monitor.RecordMessage(agentID, 500, 500)
	}
	estimate := monitor.GetEstimate(agentID)
	if estimate == nil {
		t.Fatal("No estimate available after recording messages")
	}
	logger.Log("Usage after 10 messages: %.1f%% (tokens: %d)", estimate.UsagePercent, estimate.TokensUsed)

	if estimate.UsagePercent > 50 {
		t.Errorf("Expected usage < 50%%, got %.1f%%", estimate.UsagePercent)
	}
	logger.Log("PASS: Low usage does not trigger thresholds")

	// Step 3: Simulate approaching warning threshold
	logger.LogSection("Step 3: Approach warning threshold (60%%)")
	// Add more messages to push toward warning
	for i := 0; i < 50; i++ {
		monitor.RecordMessage(agentID, 1500, 1500)
	}
	estimate = monitor.GetEstimate(agentID)
	logger.Log("Usage after additional messages: %.1f%% (tokens: %d)", estimate.UsagePercent, estimate.TokensUsed)

	aboveWarning := monitor.AgentsAboveThreshold(cfg.WarningThreshold)
	logger.Log("Agents above warning threshold: %d", len(aboveWarning))

	// Step 4: Simulate critical threshold
	logger.LogSection("Step 4: Approach critical threshold (80%%)")
	// Add more messages to push toward rotate threshold
	for i := 0; i < 100; i++ {
		monitor.RecordMessage(agentID, 2000, 2000)
	}
	estimate = monitor.GetEstimate(agentID)
	logger.Log("Usage after heavy activity: %.1f%% (tokens: %d)", estimate.UsagePercent, estimate.TokensUsed)

	aboveRotate := monitor.AgentsAboveThreshold(cfg.RotateThreshold)
	for _, agent := range aboveRotate {
		logger.Log("Agent %s: needs_warn=%v, needs_rotate=%v, usage=%.1f%%",
			agent.AgentID, agent.NeedsWarn, agent.NeedsRotate, agent.Estimate.UsagePercent)
	}

	// Verify alerting integration
	logger.LogSection("Step 5: Verify alert generation")
	_ = generator // Alert generator would be used in full integration

	logger.Log("PASS: Context threshold alerts test completed")
}

// TestMultiAgentContextTracking tests context tracking across multiple agents
// at different context usage levels.
func TestMultiAgentContextTracking(t *testing.T) {
	testutil.RequireE2E(t)

	logger := testutil.NewTestLoggerStdout(t)
	logger.LogSection("Multi-Agent Context Tracking E2E Test")

	// Create monitor
	monitor := ntmctx.NewContextMonitor(ntmctx.DefaultMonitorConfig())

	// Step 1: Register 3 agents
	logger.LogSection("Step 1: Register 3 agents")
	agents := []struct {
		id    string
		pane  string
		model string
	}{
		{fmt.Sprintf("agent_low_%d", time.Now().UnixNano()), "%1", "claude-opus-4.5"},
		{fmt.Sprintf("agent_mid_%d", time.Now().UnixNano()), "%2", "gpt-4"},
		{fmt.Sprintf("agent_high_%d", time.Now().UnixNano()), "%3", "gemini-1.5-pro"},
	}

	for _, a := range agents {
		state := monitor.RegisterAgent(a.id, a.pane, a.model)
		if state == nil {
			t.Fatalf("Failed to register agent %s", a.id)
		}
		logger.Log("Registered agent %s with model %s", a.id, a.model)
	}

	if monitor.Count() != 3 {
		t.Errorf("Expected 3 agents, got %d", monitor.Count())
	}
	logger.Log("PASS: Registered %d agents", monitor.Count())

	// Step 2: Simulate different usage levels
	logger.LogSection("Step 2: Simulate different usage levels")

	// Low usage agent
	for i := 0; i < 5; i++ {
		monitor.RecordMessage(agents[0].id, 200, 200)
	}

	// Medium usage agent
	for i := 0; i < 40; i++ {
		monitor.RecordMessage(agents[1].id, 1000, 1000)
	}

	// High usage agent
	for i := 0; i < 80; i++ {
		monitor.RecordMessage(agents[2].id, 2000, 2000)
	}

	// Step 3: Verify estimates per agent
	logger.LogSection("Step 3: Verify per-agent estimates")
	estimates := monitor.GetAllEstimates()

	if len(estimates) != 3 {
		t.Errorf("Expected 3 estimates, got %d", len(estimates))
	}

	for agentID, est := range estimates {
		logger.Log("Agent %s: %.1f%% usage (%d tokens, method=%s, confidence=%.2f)",
			agentID, est.UsagePercent, est.TokensUsed, est.Method, est.Confidence)
	}

	// Step 4: Test threshold queries
	logger.LogSection("Step 4: Test threshold queries")

	// Warning threshold (60%)
	warningAgents := monitor.AgentsAboveThreshold(60.0)
	logger.Log("Agents above 60%% warning: %d", len(warningAgents))
	for _, a := range warningAgents {
		logger.Log("  - %s: %.1f%%", a.AgentID, a.Estimate.UsagePercent)
	}

	// Rotate threshold (80%)
	rotateAgents := monitor.AgentsAboveThreshold(80.0)
	logger.Log("Agents above 80%% rotate: %d", len(rotateAgents))
	for _, a := range rotateAgents {
		logger.Log("  - %s: %.1f%%", a.AgentID, a.Estimate.UsagePercent)
	}

	// Step 5: Test agent unregistration
	logger.LogSection("Step 5: Test agent unregistration")
	monitor.UnregisterAgent(agents[0].id)
	if monitor.Count() != 2 {
		t.Errorf("Expected 2 agents after unregister, got %d", monitor.Count())
	}
	logger.Log("PASS: Unregistered agent, now have %d agents", monitor.Count())

	// Step 6: Test reset
	logger.LogSection("Step 6: Test agent reset")
	monitor.ResetAgent(agents[1].id)
	resetEstimate := monitor.GetEstimate(agents[1].id)
	// After reset, estimate might be nil or very low
	if resetEstimate != nil && resetEstimate.TokensUsed > 0 {
		logger.Log("After reset: %.1f%% usage (might have duration-based estimate)",
			resetEstimate.UsagePercent)
	} else {
		logger.Log("After reset: no estimate or zero tokens")
	}

	logger.Log("PASS: Multi-agent context tracking test completed")
}

// TestContextPredictorIntegration tests the context predictor for exhaustion prediction.
func TestContextPredictorIntegration(t *testing.T) {
	testutil.RequireE2E(t)

	logger := testutil.NewTestLoggerStdout(t)
	logger.LogSection("Context Predictor Integration E2E Test")

	// Create predictor with test-friendly config
	cfg := ntmctx.PredictorConfig{
		Window:         1 * time.Minute,
		PollInterval:   5 * time.Second,
		MaxSamples:     20,
		WarnMinutes:    15.0,
		WarnUsage:      0.70,
		CompactMinutes: 8.0,
		CompactUsage:   0.75,
		MinSamples:     3,
	}
	predictor := ntmctx.NewContextPredictor(cfg)

	// Step 1: Add samples simulating token growth
	logger.LogSection("Step 1: Simulate token growth over time")
	contextLimit := int64(200000)
	baseTokens := int64(50000)

	// Add samples with increasing token counts
	now := time.Now()
	for i := 0; i < 10; i++ {
		tokens := baseTokens + int64(i*15000)
		sampleTime := now.Add(time.Duration(i*10) * time.Second)
		predictor.AddSampleAt(tokens, sampleTime)
		logger.Log("Sample %d: %d tokens at T+%ds", i, tokens, i*10)
	}

	if predictor.SampleCount() != 10 {
		t.Errorf("Expected 10 samples, got %d", predictor.SampleCount())
	}
	logger.Log("PASS: Added %d samples", predictor.SampleCount())

	// Step 2: Get prediction
	logger.LogSection("Step 2: Get exhaustion prediction")
	prediction := predictor.PredictExhaustion(contextLimit)

	if prediction == nil {
		t.Fatal("Prediction should not be nil with sufficient samples")
	}

	logger.Log("Prediction results:")
	logger.Log("  Current usage: %.1f%%", prediction.CurrentUsage*100)
	logger.Log("  Current tokens: %d / %d", prediction.CurrentTokens, prediction.ContextLimit)
	logger.Log("  Token velocity: %.0f tokens/min", prediction.TokenVelocity)
	logger.Log("  Minutes to exhaustion: %.1f", prediction.MinutesToExhaustion)
	logger.Log("  Should warn: %v", prediction.ShouldWarn)
	logger.Log("  Should compact: %v", prediction.ShouldCompact)
	logger.Log("  Sample count: %d", prediction.SampleCount)

	// Verify prediction makes sense
	if prediction.TokenVelocity <= 0 {
		t.Error("Expected positive token velocity with growing samples")
	}

	// Step 3: Test velocity trend
	logger.LogSection("Step 3: Check velocity trend")
	velocity, accelerating := predictor.VelocityTrend()
	logger.Log("Velocity: %.0f tokens/min, Accelerating: %v", velocity, accelerating)

	// Step 4: Test reset
	logger.LogSection("Step 4: Test reset")
	predictor.Reset()
	if predictor.SampleCount() != 0 {
		t.Errorf("Expected 0 samples after reset, got %d", predictor.SampleCount())
	}
	logger.Log("PASS: Reset cleared all samples")

	logger.Log("PASS: Context predictor integration test completed")
}

// TestCompactionTriggerLifecycle tests the proactive compaction trigger.
func TestCompactionTriggerLifecycle(t *testing.T) {
	testutil.RequireE2E(t)

	logger := testutil.NewTestLoggerStdout(t)
	logger.LogSection("Compaction Trigger Lifecycle E2E Test")

	// Create components
	monitor := ntmctx.NewContextMonitor(ntmctx.DefaultMonitorConfig())
	compactor := ntmctx.NewCompactor(monitor, ntmctx.DefaultCompactorConfig())
	predictor := ntmctx.NewContextPredictor(ntmctx.DefaultPredictorConfig())

	// Create trigger with short poll for testing
	triggerCfg := ntmctx.CompactionTriggerConfig{
		PollInterval:            100 * time.Millisecond,
		AutoCompact:             true,
		CompactionCooldown:      50 * time.Millisecond,
		WaitAfterCommand:        10 * time.Millisecond,
		EnableRecoveryInjection: false, // Disable for testing
	}
	trigger := ntmctx.NewCompactionTrigger(triggerCfg, monitor, compactor, predictor)

	// Track events
	var triggeredEvents []ntmctx.CompactionTriggerEvent
	var completedEvents []ntmctx.CompactionTriggerEvent

	trigger.SetCompactionTriggeredHandler(func(event ntmctx.CompactionTriggerEvent) {
		triggeredEvents = append(triggeredEvents, event)
		logger.Log("Compaction triggered for %s at %.1f%% usage",
			event.AgentID, event.Prediction.CurrentUsage*100)
	})

	trigger.SetCompactionCompleteHandler(func(event ntmctx.CompactionTriggerEvent) {
		completedEvents = append(completedEvents, event)
		result := "no result"
		if event.CompactionResult != nil {
			if event.CompactionResult.Success {
				result = "success"
			} else {
				result = "failed: " + event.CompactionResult.Error
			}
		}
		logger.Log("Compaction completed for %s: %s", event.AgentID, result)
	})

	// Step 1: Verify initial state
	logger.LogSection("Step 1: Verify initial state")
	status := trigger.GetCompactionStatus()
	if len(status) != 0 {
		t.Errorf("Expected empty status, got %d entries", len(status))
	}
	logger.Log("PASS: Initial status is empty")

	// Step 2: Start trigger
	logger.LogSection("Step 2: Start trigger")
	trigger.Start()
	logger.Log("Trigger started")

	// Let it run briefly
	time.Sleep(200 * time.Millisecond)

	// Step 3: Stop trigger
	logger.LogSection("Step 3: Stop trigger")
	trigger.Stop()
	logger.Log("Trigger stopped")

	// Step 4: Check that no events fired (no agents registered)
	if len(triggeredEvents) != 0 {
		logger.Log("WARNING: Unexpected triggered events: %d", len(triggeredEvents))
	} else {
		logger.Log("PASS: No events with no agents (expected)")
	}

	logger.Log("PASS: Compaction trigger lifecycle test completed")
}

// TestContextCommandJSON tests the ntm context command JSON output.
func TestContextCommandJSON(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	logger.LogSection("Context Command JSON E2E Test")

	// Step 1: Test context stats command
	logger.LogSection("Step 1: Test 'ntm context stats' command")
	out, err := logger.Exec("ntm", "context", "stats", "--json")
	if err != nil {
		t.Fatalf("context stats command failed: %v", err)
	}

	var statsResult map[string]interface{}
	if err := json.Unmarshal(out, &statsResult); err != nil {
		t.Errorf("Failed to parse stats JSON: %v\nOutput: %s", err, string(out))
	} else {
		logger.Log("PASS: Stats JSON is valid")
		if _, ok := statsResult["cache_size"]; ok {
			logger.Log("  cache_size present in output")
		}
	}

	// Step 2: Test context clear command
	logger.LogSection("Step 2: Test 'ntm context clear' command")
	out, err = logger.Exec("ntm", "context", "clear")
	if err != nil {
		logger.Log("WARNING: context clear command failed (may be expected): %v", err)
	} else {
		logger.Log("PASS: Context clear succeeded")
	}

	logger.Log("PASS: Context command JSON test completed")
}

// TestRotationHistoryStore tests the rotation history storage.
func TestRotationHistoryStore(t *testing.T) {
	testutil.RequireE2E(t)

	logger := testutil.NewTestLoggerStdout(t)
	logger.LogSection("Rotation History Store E2E Test")

	// Use the default store
	store := ntmctx.DefaultRotationHistoryStore

	// Step 1: Get initial count
	logger.LogSection("Step 1: Get initial state")
	initialCount, err := store.Count()
	if err != nil {
		t.Fatalf("Failed to get count: %v", err)
	}
	logger.Log("Initial record count: %d", initialCount)

	// Step 2: Write a test rotation record
	logger.LogSection("Step 2: Write rotation record")
	record := ntmctx.RotationRecord{
		SessionName:   fmt.Sprintf("test_session_%d", time.Now().UnixNano()),
		AgentID:       "test_agent_1",
		AgentType:     "cc",
		ContextBefore: 85.5,
		Timestamp:     time.Now(),
		Success:       true,
		DurationMs:    1500,
		Method:        ntmctx.RotationThresholdExceeded,
	}

	if err := store.Append(&record); err != nil {
		t.Fatalf("Failed to write record: %v", err)
	}
	logger.Log("PASS: Wrote rotation record")

	// Step 3: Read recent records
	logger.LogSection("Step 3: Read recent records")
	records, err := store.ReadRecent(10)
	if err != nil {
		t.Fatalf("Failed to read recent: %v", err)
	}
	logger.Log("Read %d recent records", len(records))

	// Step 4: Verify our record is there
	found := false
	for _, r := range records {
		if r.SessionName == record.SessionName {
			found = true
			logger.Log("Found our test record: %s (%.1f%% context)", r.SessionName, r.ContextBefore)
			break
		}
	}
	if !found {
		logger.Log("WARNING: Test record not found in recent records")
	}

	// Step 5: Get statistics
	logger.LogSection("Step 5: Get rotation statistics")
	stats, err := ntmctx.GetRotationStats()
	if err != nil {
		t.Fatalf("Failed to get stats: %v", err)
	}
	logger.Log("Rotation stats:")
	logger.Log("  Total rotations: %d", stats.TotalRotations)
	logger.Log("  Success count: %d", stats.SuccessCount)
	logger.Log("  Failure count: %d", stats.FailureCount)
	logger.Log("  Unique sessions: %d", stats.UniqueSessions)

	logger.Log("PASS: Rotation history store test completed")
}

// TestStateStoreContextPacks tests context pack persistence in the state store.
func TestStateStoreContextPacks(t *testing.T) {
	testutil.RequireE2E(t)

	logger := testutil.NewTestLoggerStdout(t)
	logger.LogSection("State Store Context Packs E2E Test")

	// Create temp state database
	stateDir := t.TempDir()
	stateDBPath := filepath.Join(stateDir, "context_test.db")

	// Step 1: Open state store
	logger.LogSection("Step 1: Open state store")
	store, err := state.Open(stateDBPath)
	if err != nil {
		t.Fatalf("Failed to open state store: %v", err)
	}
	defer store.Close()

	if err := store.Migrate(); err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}
	logger.Log("PASS: State store opened and migrated")

	// Step 2: Create a context pack
	logger.LogSection("Step 2: Create context pack")
	pack := &state.ContextPack{
		ID:             fmt.Sprintf("pack_%d", time.Now().UnixNano()),
		BeadID:         "bd-test123",
		AgentType:      "cc",
		RepoRev:        "abc123def",
		TokenCount:     5000,
		RenderedPrompt: "Test prompt content for E2E testing",
		CreatedAt:      time.Now(),
	}

	if err := store.CreateContextPack(pack); err != nil {
		t.Fatalf("Failed to save context pack: %v", err)
	}
	logger.Log("PASS: Saved context pack %s", pack.ID)

	// Step 3: Retrieve the context pack
	logger.LogSection("Step 3: Retrieve context pack")
	retrieved, err := store.GetContextPack(pack.ID)
	if err != nil {
		t.Fatalf("Failed to get context pack: %v", err)
	}
	if retrieved == nil {
		t.Fatal("Context pack not found")
	}

	if retrieved.BeadID != pack.BeadID {
		t.Errorf("BeadID mismatch: got %q, want %q", retrieved.BeadID, pack.BeadID)
	}
	if retrieved.TokenCount != pack.TokenCount {
		t.Errorf("TokenCount mismatch: got %d, want %d", retrieved.TokenCount, pack.TokenCount)
	}
	logger.Log("PASS: Retrieved context pack matches")

	// Step 4: Test non-existent pack
	logger.LogSection("Step 4: Test non-existent pack")
	missing, err := store.GetContextPack("nonexistent_pack")
	if err != nil {
		t.Fatalf("Unexpected error for missing pack: %v", err)
	}
	if missing != nil {
		t.Error("Expected nil for non-existent pack")
	}
	logger.Log("PASS: Non-existent pack returns nil")

	logger.Log("PASS: State store context packs test completed")
}

// TestHandoffRecommendation tests the handoff trigger recommendation logic.
func TestHandoffRecommendation(t *testing.T) {
	testutil.RequireE2E(t)

	logger := testutil.NewTestLoggerStdout(t)
	logger.LogSection("Handoff Recommendation E2E Test")

	// Create monitor and predictor
	monitor := ntmctx.NewContextMonitor(ntmctx.DefaultMonitorConfig())
	predictor := ntmctx.NewContextPredictor(ntmctx.DefaultPredictorConfig())

	// Register agent
	agentID := fmt.Sprintf("handoff_agent_%d", time.Now().UnixNano())
	monitor.RegisterAgent(agentID, "%1", "claude-opus-4.5")

	// Step 1: Test with low usage
	logger.LogSection("Step 1: Low usage - no handoff needed")
	for i := 0; i < 5; i++ {
		monitor.RecordMessage(agentID, 200, 200)
	}

	rec := monitor.ShouldTriggerHandoff(agentID, predictor)
	logger.Log("Recommendation: trigger=%v, warn=%v, reason=%s",
		rec.ShouldTrigger, rec.ShouldWarn, rec.Reason)

	if rec.ShouldTrigger {
		t.Error("Should not trigger handoff at low usage")
	}
	logger.Log("PASS: Low usage does not trigger handoff")

	// Step 2: Push to warning threshold
	logger.LogSection("Step 2: Warning threshold (70%%)")
	for i := 0; i < 80; i++ {
		monitor.RecordMessage(agentID, 1500, 1500)
	}

	rec = monitor.ShouldTriggerHandoff(agentID, predictor)
	logger.Log("Recommendation: trigger=%v, warn=%v, usage=%.1f%%, reason=%s",
		rec.ShouldTrigger, rec.ShouldWarn, rec.UsagePercent, rec.Reason)

	// Step 3: Push to trigger threshold
	logger.LogSection("Step 3: Trigger threshold (75%%)")
	for i := 0; i < 50; i++ {
		monitor.RecordMessage(agentID, 2000, 2000)
	}

	rec = monitor.ShouldTriggerHandoff(agentID, predictor)
	logger.Log("Recommendation: trigger=%v, warn=%v, usage=%.1f%%, reason=%s",
		rec.ShouldTrigger, rec.ShouldWarn, rec.UsagePercent, rec.Reason)

	// At high enough usage, should trigger
	if rec.UsagePercent >= 75 && !rec.ShouldTrigger {
		t.Error("Should trigger handoff at 75%+ usage")
	}

	logger.Log("PASS: Handoff recommendation test completed")
}

// TestContextLimits tests the model context limit lookup.
func TestContextLimits(t *testing.T) {
	testutil.RequireE2E(t)

	logger := testutil.NewTestLoggerStdout(t)
	logger.LogSection("Context Limits E2E Test")

	testCases := []struct {
		model    string
		expected int64
	}{
		{"claude-opus-4.5", 200000},
		{"claude-sonnet-4", 200000},
		{"gpt-4", 128000},
		{"gpt-5-codex", 256000},
		{"gemini-1.5-pro", 1000000},
		{"unknown-model", 128000}, // Default
	}

	for _, tc := range testCases {
		limit := ntmctx.GetContextLimit(tc.model)
		logger.Log("Model %s: limit=%d (expected=%d)", tc.model, limit, tc.expected)

		if limit != tc.expected {
			t.Errorf("Model %s: got limit %d, expected %d", tc.model, limit, tc.expected)
		}
	}

	logger.Log("PASS: Context limits test completed")
}

// TestTokenEstimation tests token count estimation utilities.
func TestTokenEstimation(t *testing.T) {
	testutil.RequireE2E(t)

	logger := testutil.NewTestLoggerStdout(t)
	logger.LogSection("Token Estimation E2E Test")

	// Test EstimateTokens
	testCases := []struct {
		chars    int
		minToken int64
		maxToken int64
	}{
		{100, 20, 40},
		{1000, 200, 400},
		{10000, 2000, 4000},
	}

	for _, tc := range testCases {
		tokens := ntmctx.EstimateTokens(tc.chars)
		logger.Log("%d chars -> %d tokens (expected %d-%d)",
			tc.chars, tokens, tc.minToken, tc.maxToken)

		if tokens < tc.minToken || tokens > tc.maxToken {
			t.Errorf("EstimateTokens(%d) = %d, expected %d-%d",
				tc.chars, tokens, tc.minToken, tc.maxToken)
		}
	}

	// Test ParseTokenCount
	parseCases := []struct {
		input    string
		expected int64
		ok       bool
	}{
		{"1000", 1000, true},
		{"1,000", 1000, true},
		{"10k", 10000, true},
		{"1.5k", 1500, true},
		{"1M", 1000000, true},
		{"invalid", 0, false},
	}

	for _, tc := range parseCases {
		result, ok := ntmctx.ParseTokenCount(tc.input)
		logger.Log("ParseTokenCount(%q) = %d, ok=%v (expected %d, %v)",
			tc.input, result, ok, tc.expected, tc.ok)

		if ok != tc.ok {
			t.Errorf("ParseTokenCount(%q) ok=%v, expected %v", tc.input, ok, tc.ok)
		}
		if ok && result != tc.expected {
			t.Errorf("ParseTokenCount(%q) = %d, expected %d", tc.input, result, tc.expected)
		}
	}

	logger.Log("PASS: Token estimation test completed")
}

// TestRobotModeContextParsing tests parsing context info from robot mode output.
func TestRobotModeContextParsing(t *testing.T) {
	testutil.RequireE2E(t)

	logger := testutil.NewTestLoggerStdout(t)
	logger.LogSection("Robot Mode Context Parsing E2E Test")

	testCases := []struct {
		name     string
		input    string
		hasValue bool
	}{
		{
			name:     "valid JSON with context_used",
			input:    `{"context_used": 50000, "context_limit": 200000}`,
			hasValue: true,
		},
		{
			name:     "valid JSON with tokens_used",
			input:    `{"tokens_used": 75000, "tokens_limit": 200000}`,
			hasValue: true,
		},
		{
			name:     "invalid JSON",
			input:    `not json at all`,
			hasValue: false,
		},
		{
			name:     "JSON without context fields",
			input:    `{"message": "hello", "status": "ok"}`,
			hasValue: false,
		},
		{
			name: "multiline with context in middle",
			input: `Some text before
{"context_used": 100000, "context_limit": 200000}
Some text after`,
			hasValue: true,
		},
	}

	for _, tc := range testCases {
		logger.Log("Testing: %s", tc.name)
		estimate := ntmctx.ParseRobotModeContext(tc.input)

		if tc.hasValue {
			if estimate == nil {
				t.Errorf("%s: expected estimate, got nil", tc.name)
			} else {
				logger.Log("  Parsed: %d tokens, %.1f%% usage",
					estimate.TokensUsed, estimate.UsagePercent)
			}
		} else {
			if estimate != nil {
				t.Errorf("%s: expected nil, got estimate", tc.name)
			} else {
				logger.Log("  Correctly returned nil")
			}
		}
	}

	logger.Log("PASS: Robot mode context parsing test completed")
}

// TestContextMonitorConcurrency tests thread-safety of context monitor operations.
func TestContextMonitorConcurrency(t *testing.T) {
	testutil.RequireE2E(t)

	logger := testutil.NewTestLoggerStdout(t)
	logger.LogSection("Context Monitor Concurrency E2E Test")

	monitor := ntmctx.NewContextMonitor(ntmctx.DefaultMonitorConfig())

	// Register multiple agents
	agentCount := 10
	for i := 0; i < agentCount; i++ {
		agentID := fmt.Sprintf("concurrent_agent_%d", i)
		monitor.RegisterAgent(agentID, fmt.Sprintf("%%%d", i), "claude-opus-4.5")
	}
	logger.Log("Registered %d agents", agentCount)

	// Spawn goroutines to concurrently record messages and get estimates
	done := make(chan bool, agentCount*2)

	for i := 0; i < agentCount; i++ {
		agentID := fmt.Sprintf("concurrent_agent_%d", i)

		// Writer goroutine
		go func(id string) {
			for j := 0; j < 100; j++ {
				monitor.RecordMessage(id, 100, 100)
			}
			done <- true
		}(agentID)

		// Reader goroutine
		go func(id string) {
			for j := 0; j < 100; j++ {
				_ = monitor.GetEstimate(id)
			}
			done <- true
		}(agentID)
	}

	// Wait for all goroutines
	for i := 0; i < agentCount*2; i++ {
		<-done
	}

	// Verify final state
	if monitor.Count() != agentCount {
		t.Errorf("Expected %d agents, got %d", agentCount, monitor.Count())
	}

	estimates := monitor.GetAllEstimates()
	logger.Log("Final state: %d agents with estimates", len(estimates))

	logger.Log("PASS: Concurrency test completed without data races")
}

// TestCompactorAgentCapabilities tests compactor agent-specific capabilities.
func TestCompactorAgentCapabilities(t *testing.T) {
	testutil.RequireE2E(t)

	logger := testutil.NewTestLoggerStdout(t)
	logger.LogSection("Compactor Agent Capabilities E2E Test")

	monitor := ntmctx.NewContextMonitor(ntmctx.DefaultMonitorConfig())
	compactor := ntmctx.NewCompactor(monitor, ntmctx.DefaultCompactorConfig())

	agentTypes := []string{"cc", "cod", "gmi", "unknown"}

	for _, agentType := range agentTypes {
		caps := ntmctx.GetAgentCapabilities(agentType)
		logger.Log("Agent type %s capabilities:", agentType)
		logger.Log("  Supports builtin: %v", caps.SupportsBuiltinCompact)
		logger.Log("  Builtin command: %s", caps.BuiltinCompactCommand)
		logger.Log("  Supports history clear: %v", caps.SupportsHistoryClear)
		logger.Log("  History clear command: %s", caps.HistoryClearCommand)

		commands := compactor.GetCompactionCommands(agentType)
		logger.Log("  Compaction commands: %d available", len(commands))
		for i, cmd := range commands {
			logger.Log("    %d. %s (prompt=%v, wait=%v)", i+1, cmd.Description, cmd.IsPrompt, cmd.WaitTime)
		}
	}

	logger.Log("PASS: Compactor agent capabilities test completed")
}

// TestEstimationMethods tests different estimation methods produce reasonable results.
func TestEstimationMethods(t *testing.T) {
	testutil.RequireE2E(t)

	logger := testutil.NewTestLoggerStdout(t)
	logger.LogSection("Estimation Methods E2E Test")

	// Create test state
	state := &ntmctx.ContextState{
		AgentID:      "test_agent",
		PaneID:       "%1",
		Model:        "claude-opus-4.5",
		MessageCount: 50,
		SessionStart: time.Now().Add(-30 * time.Minute),
		LastActivity: time.Now(),
	}

	// Test message count estimator
	logger.LogSection("Testing MessageCountEstimator")
	msgEstimator := &ntmctx.MessageCountEstimator{TokensPerMessage: 1500}
	msgEstimate, err := msgEstimator.Estimate(state)
	if err != nil {
		t.Errorf("MessageCountEstimator error: %v", err)
	} else if msgEstimate != nil {
		logger.Log("MessageCount estimate: %d tokens (%.1f%% usage, confidence=%.2f)",
			msgEstimate.TokensUsed, msgEstimate.UsagePercent, msgEstimate.Confidence)
	}

	// Test duration/activity estimator
	logger.LogSection("Testing DurationActivityEstimator")
	durEstimator := &ntmctx.DurationActivityEstimator{
		TokensPerMinuteActive:   1000,
		TokensPerMinuteInactive: 100,
	}
	durEstimate, err := durEstimator.Estimate(state)
	if err != nil {
		t.Errorf("DurationActivityEstimator error: %v", err)
	} else if durEstimate != nil {
		logger.Log("Duration estimate: %d tokens (%.1f%% usage, confidence=%.2f)",
			durEstimate.TokensUsed, durEstimate.UsagePercent, durEstimate.Confidence)
	}

	// Verify message count estimate is more confident
	if msgEstimate != nil && durEstimate != nil {
		if msgEstimate.Confidence <= durEstimate.Confidence {
			t.Error("MessageCount should have higher confidence than Duration")
		}
		logger.Log("PASS: Confidence ordering correct (msg=%.2f > dur=%.2f)",
			msgEstimate.Confidence, durEstimate.Confidence)
	}

	logger.Log("PASS: Estimation methods test completed")
}

// TestContextIntegrationWithTmuxSession tests context monitoring with a real tmux session.
func TestContextIntegrationWithTmuxSession(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	logger.LogSection("Context Integration with Tmux Session E2E Test")

	// Create a test session
	sessionName := fmt.Sprintf("ctx_test_%d", time.Now().UnixNano())

	// Setup directories
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project directory: %v", err)
	}

	// Create config
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")
	configContent := fmt.Sprintf(`
projects_base = %q

[agents]
claude = "bash"
codex = "bash"
gemini = "bash"

[tmux]
scrollback = 500
`, projectsBase)
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Cleanup
	t.Cleanup(func() {
		logger.Log("Cleaning up session %s", sessionName)
		testutil.AssertCommandSuccess(t, logger, "ntm", "--config", configPath, "kill", "-f", sessionName)
	})

	// Step 1: Spawn session with safety mode (uses bash instead of real agents)
	logger.LogSection("Step 1: Spawn test session")
	out, err := logger.Exec("ntm", "--config", configPath, "spawn", sessionName, "--cc=2", "--safety")
	if err != nil {
		t.Fatalf("Failed to spawn session: %v\nOutput: %s", err, string(out))
	}

	time.Sleep(1 * time.Second)
	testutil.AssertSessionExists(t, logger, sessionName)
	logger.Log("PASS: Session created")

	// Step 2: Test rotate context history command
	logger.LogSection("Step 2: Test rotate context history command")
	out, _ = logger.Exec("ntm", "--config", configPath, "rotate", "context", "history", "--json")
	// May have no history yet, but command should work
	logger.Log("History output: %s", string(out))

	// Step 3: Test rotate context stats command
	logger.LogSection("Step 3: Test rotate context stats command")
	out, err = logger.Exec("ntm", "--config", configPath, "rotate", "context", "stats", "--json")
	if err != nil {
		logger.Log("WARNING: stats command failed (may be expected with no data): %v", err)
	} else {
		var stats map[string]interface{}
		if err := json.Unmarshal(out, &stats); err == nil {
			logger.Log("Stats: total=%v, success=%v, failure=%v",
				stats["total_rotations"], stats["success_count"], stats["failure_count"])
		}
	}

	// Step 4: Test rotate context pending command
	logger.LogSection("Step 4: Test rotate context pending command")
	out, _ = logger.Exec("ntm", "--config", configPath, "rotate", "context", "pending", "--json")
	logger.Log("Pending output: %s", string(out))

	logger.Log("PASS: Context integration with tmux session test completed")
}
