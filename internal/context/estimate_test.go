package context

import (
	"testing"
)

// TestMultiModelEstimation tests token counting accuracy across different model families.
func TestMultiModelEstimation(t *testing.T) {
	t.Parallel()

	// Test model families with their expected context limits
	models := []struct {
		name  string
		limit int64
	}{
		// Claude models
		{"claude-opus-4", 200000},
		{"claude-sonnet-4", 200000},
		{"claude-opus-4-5-20251101", 200000}, // With date suffix
		{"claude-3.5-sonnet", 200000},
		{"claude-haiku", 200000},

		// GPT models
		{"gpt-4", 128000},
		{"gpt-4-turbo", 128000},
		{"gpt-4o", 128000},
		{"gpt-5", 256000},
		{"gpt-5-codex", 256000},

		// Gemini models
		{"gemini-2.0-flash", 1000000},
		{"gemini-1.5-pro", 1000000},
		{"gemini-pro", 32000},
	}

	for _, m := range models {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()

			limit := GetContextLimit(m.name)
			if limit != m.limit {
				t.Errorf("GetContextLimit(%q) = %d, want %d", m.name, limit, m.limit)
			}

			t.Logf("CONTEXT_TEST: MultiModelEstimation | Model=%s | Limit=%d", m.name, limit)
		})
	}
}

// TestModelNormalization tests that model name variations are handled correctly.
func TestModelNormalization(t *testing.T) {
	t.Parallel()

	// Test various model name formats that should normalize to same limit
	testCases := []struct {
		name     string
		variants []string
		limit    int64
	}{
		{
			name:     "claude-opus-4",
			variants: []string{"claude-opus-4", "Claude-Opus-4", "CLAUDE-OPUS-4", "claude-opus-4-20251101"},
			limit:    200000,
		},
		{
			name:     "gpt-4",
			variants: []string{"gpt-4", "GPT-4", "gpt-4-turbo-20240101"},
			limit:    128000,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			for _, variant := range tc.variants {
				limit := GetContextLimit(variant)
				if limit != tc.limit {
					t.Errorf("GetContextLimit(%q) = %d, want %d", variant, limit, tc.limit)
				}
			}
		})
	}
}

// TestEstimatorConfidenceLevels verifies confidence ordering across estimators.
func TestEstimatorConfidenceLevels(t *testing.T) {
	t.Parallel()

	estimators := []ContextEstimator{
		&RobotModeEstimator{},
		&CumulativeTokenEstimator{},
		&MessageCountEstimator{},
		&DurationActivityEstimator{},
	}

	// Get all confidences
	confidences := make(map[string]float64)
	for _, e := range estimators {
		confidences[e.Name()] = e.Confidence()
		t.Logf("CONTEXT_TEST: EstimatorConfidence | Name=%s | Confidence=%.2f",
			e.Name(), e.Confidence())
	}

	// Verify expected ordering: robot_mode > cumulative > message_count > duration
	if confidences["robot_mode"] <= confidences["cumulative_tokens"] {
		t.Error("robot_mode should have higher confidence than cumulative_tokens")
	}
	if confidences["cumulative_tokens"] <= confidences["message_count"] {
		t.Error("cumulative_tokens should have higher confidence than message_count")
	}
	if confidences["message_count"] <= confidences["duration_activity"] {
		t.Error("message_count should have higher confidence than duration_activity")
	}
}

// TestScrollbackParsing tests parsing of context info from scrollback/output.
func TestScrollbackParsing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		output   string
		wantUsed int64
		wantOK   bool
	}{
		{
			name:     "robot mode JSON",
			output:   `{"context_used": 145000, "context_limit": 200000}`,
			wantUsed: 145000,
			wantOK:   true,
		},
		{
			name:     "alternate field names",
			output:   `{"tokens_used": 80000, "tokens_limit": 128000}`,
			wantUsed: 80000,
			wantOK:   true,
		},
		{
			name: "embedded in noise",
			output: `Starting analysis...
Processing data...
{"context_used": 50000, "context_limit": 200000}
Done.`,
			wantUsed: 50000,
			wantOK:   true,
		},
		{
			name:     "plain text",
			output:   "Just some regular output with no context info",
			wantUsed: 0,
			wantOK:   false,
		},
		{
			name:     "malformed JSON",
			output:   `{context_used: 1000}`,
			wantUsed: 0,
			wantOK:   false,
		},
		{
			name:     "empty output",
			output:   "",
			wantUsed: 0,
			wantOK:   false,
		},
		{
			name:     "binary-like content",
			output:   "\x00\x01\x02\x03\x04",
			wantUsed: 0,
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			estimate := ParseRobotModeContext(tt.output)

			if tt.wantOK {
				if estimate == nil {
					t.Fatal("expected estimate, got nil")
				}
				if estimate.TokensUsed != tt.wantUsed {
					t.Errorf("TokensUsed = %d, want %d", estimate.TokensUsed, tt.wantUsed)
				}
				t.Logf("CONTEXT_TEST: ScrollbackParsing | TestName=%s | TokensUsed=%d | Method=%s",
					tt.name, estimate.TokensUsed, estimate.Method)
			} else {
				if estimate != nil {
					t.Errorf("expected nil estimate, got %+v", estimate)
				}
			}
		})
	}
}

// TestEstimatorWithSampleData tests estimators with realistic sample data.
func TestEstimatorWithSampleData(t *testing.T) {
	t.Parallel()

	// Create sample scrollback data at various lengths
	sampleScrollbacks := []struct {
		name         string
		messageLen   int
		messageCount int
	}{
		{"short", 100, 5},
		{"medium", 500, 20},
		{"long", 1000, 50},
		{"very_long", 2000, 100},
	}

	monitor := NewContextMonitor(DefaultMonitorConfig())

	for _, sample := range sampleScrollbacks {
		t.Run(sample.name, func(t *testing.T) {
			agentID := "agent-" + sample.name
			monitor.RegisterAgent(agentID, "pane-"+sample.name, "claude-opus-4")

			// Record messages of specified length
			tokensPerMessage := int64(sample.messageLen / 4) // rough char-to-token
			for i := 0; i < sample.messageCount; i++ {
				monitor.RecordMessage(agentID, tokensPerMessage, tokensPerMessage)
			}

			estimate := monitor.GetEstimate(agentID)
			if estimate == nil {
				t.Fatal("expected estimate, got nil")
			}

			t.Logf("CONTEXT_TEST: SampleData | Name=%s | Messages=%d | MsgLen=%d | EstTokens=%d | Usage=%.1f%%",
				sample.name, sample.messageCount, sample.messageLen,
				estimate.TokensUsed, estimate.UsagePercent)

			// Verify estimate is reasonable
			if estimate.TokensUsed == 0 {
				t.Error("TokensUsed should not be zero")
			}
			if estimate.UsagePercent < 0 || estimate.UsagePercent > 100 {
				t.Errorf("UsagePercent = %.1f, expected 0-100", estimate.UsagePercent)
			}
		})
	}
}

// TestCrossModelComparison compares estimation behavior across model types.
func TestCrossModelComparison(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())

	// Register agents with different models
	models := []struct {
		id    string
		model string
		limit int64
	}{
		{"claude", "claude-opus-4", 200000},
		{"gpt", "gpt-4", 128000},
		{"gemini", "gemini-2.0-flash", 1000000},
	}

	// Same activity for all agents
	messagesPerAgent := 50
	tokensPerMessage := int64(500)

	for _, m := range models {
		monitor.RegisterAgent(m.id, "pane-"+m.id, m.model)
		for i := 0; i < messagesPerAgent; i++ {
			monitor.RecordMessage(m.id, tokensPerMessage, tokensPerMessage)
		}
	}

	// Compare estimates
	for _, m := range models {
		estimate := monitor.GetEstimate(m.id)
		if estimate == nil {
			t.Errorf("missing estimate for %s", m.id)
			continue
		}

		// Same absolute usage should result in different percentages
		// because of different context limits
		t.Logf("CONTEXT_TEST: CrossModel | Model=%s | Limit=%d | Tokens=%d | Usage=%.2f%%",
			m.model, estimate.ContextLimit, estimate.TokensUsed, estimate.UsagePercent)

		if estimate.ContextLimit != m.limit {
			t.Errorf("%s: ContextLimit = %d, want %d", m.id, estimate.ContextLimit, m.limit)
		}
	}

	// Gemini (1M context) should have lowest usage percentage
	geminiEst := monitor.GetEstimate("gemini")
	claudeEst := monitor.GetEstimate("claude")
	gptEst := monitor.GetEstimate("gpt")

	if geminiEst != nil && claudeEst != nil {
		if geminiEst.UsagePercent >= claudeEst.UsagePercent {
			t.Error("Gemini (1M limit) should have lower usage% than Claude (200k limit)")
		}
	}
	if claudeEst != nil && gptEst != nil {
		if claudeEst.UsagePercent >= gptEst.UsagePercent {
			t.Error("Claude (200k limit) should have lower usage% than GPT-4 (128k limit)")
		}
	}
}
