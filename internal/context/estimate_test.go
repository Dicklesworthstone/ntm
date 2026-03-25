package context

import (
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/models"
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

// TestTokenCountingAccuracy tests various token counting utilities.
func TestTokenCountingAccuracy(t *testing.T) {
	t.Parallel()

	// Test EstimateTokens with known character counts
	tests := []struct {
		chars        int
		expectedLow  int64
		expectedHigh int64
	}{
		{0, 0, 0},
		{35, 8, 12},           // ~10 tokens at 3.5 chars/token
		{350, 90, 110},        // ~100 tokens
		{3500, 950, 1050},     // ~1000 tokens
		{70000, 19000, 21000}, // ~20000 tokens
	}

	for _, tt := range tests {
		tokens := EstimateTokens(tt.chars)
		if tokens < tt.expectedLow || tokens > tt.expectedHigh {
			t.Errorf("EstimateTokens(%d) = %d, expected range [%d, %d]",
				tt.chars, tokens, tt.expectedLow, tt.expectedHigh)
		}
		t.Logf("CONTEXT_TEST: TokenCounting | Chars=%d | Tokens=%d | Range=[%d,%d]",
			tt.chars, tokens, tt.expectedLow, tt.expectedHigh)
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

// TestParseTokenCountFormats tests parsing of various token count formats.
func TestParseTokenCountFormats(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		expected int64
		valid    bool
	}{
		// Simple numbers
		{"145000", 145000, true},
		{"0", 0, true},
		{"1", 1, true},

		// With commas
		{"145,000", 145000, true},
		{"1,000,000", 1000000, true},

		// With K suffix
		{"145k", 145000, true},
		{"145K", 145000, true},
		{"1.5k", 1500, true},

		// With M suffix
		{"1M", 1000000, true},
		{"1.5M", 1500000, true},
		{"1.5m", 1500000, true},

		// Invalid
		{"invalid", 0, false},
		{"", 0, false},
		{"abc123", 0, false},

		// Note: Negative numbers are parsed successfully by strconv.ParseFloat/ParseInt
		// This documents current behavior - semantically meaningless for token counts
		{"-1000", -1000, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()

			count, ok := ParseTokenCount(tt.input)

			if ok != tt.valid {
				t.Errorf("ParseTokenCount(%q) ok = %v, want %v", tt.input, ok, tt.valid)
			}

			if ok && count != tt.expected {
				t.Errorf("ParseTokenCount(%q) = %d, want %d", tt.input, count, tt.expected)
			}

			t.Logf("CONTEXT_TEST: ParseTokenCount | Input=%q | Result=%d | Valid=%v",
				tt.input, count, ok)
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

// TestEdgeCases tests edge cases in estimation.
func TestEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("empty scrollback", func(t *testing.T) {
		t.Parallel()
		estimate := ParseRobotModeContext("")
		if estimate != nil {
			t.Error("empty scrollback should return nil estimate")
		}
	})

	t.Run("very long scrollback", func(t *testing.T) {
		t.Parallel()
		// Create very long content
		longContent := strings.Repeat("x", 100000)
		tokens := EstimateTokens(len(longContent))
		if tokens < 25000 || tokens > 35000 {
			t.Errorf("EstimateTokens for 100k chars = %d, expected ~28571", tokens)
		}
		t.Logf("CONTEXT_TEST: EdgeCase | VeryLongScrollback | Chars=%d | Tokens=%d",
			len(longContent), tokens)
	})

	t.Run("unknown model", func(t *testing.T) {
		t.Parallel()
		limit := GetContextLimit("totally-unknown-model-xyz")
		if limit != int64(models.DefaultContextLimit) {
			t.Errorf("unknown model should return default limit, got %d", limit)
		}
	})

	t.Run("fresh agent no data", func(t *testing.T) {
		t.Parallel()
		monitor := NewContextMonitor(DefaultMonitorConfig())
		monitor.RegisterAgent("fresh-agent", "pane-1", "claude-opus-4")

		// Get estimate with no messages recorded
		estimate := monitor.GetEstimate("fresh-agent")
		// Should return nil or very low estimate
		if estimate != nil && estimate.TokensUsed > 1000 {
			t.Errorf("fresh agent should have low/zero estimate, got %d", estimate.TokensUsed)
		}
	})

	t.Run("agent with session duration only", func(t *testing.T) {
		t.Parallel()
		monitor := NewContextMonitor(DefaultMonitorConfig())
		state := monitor.RegisterAgent("duration-agent", "pane-1", "claude-opus-4")

		// Fake session start to 10 minutes ago
		state.SessionStart = time.Now().Add(-10 * time.Minute)
		state.MessageCount = 5 // Some messages for activity

		estimate := monitor.GetEstimate("duration-agent")
		if estimate == nil {
			t.Log("CONTEXT_TEST: EdgeCase | DurationOnly - no estimate (expected)")
		} else {
			t.Logf("CONTEXT_TEST: EdgeCase | DurationOnly | Tokens=%d | Method=%s",
				estimate.TokensUsed, estimate.Method)
		}
	})
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
