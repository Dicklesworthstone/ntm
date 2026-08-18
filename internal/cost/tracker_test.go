// Package cost provides API cost tracking for AI agent sessions.
package cost

import (
	"testing"
)

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		text     string
		expected int
	}{
		{"", 0},
		{"a", 1},
		{"test", 1},
		{"hello world", 3},           // 11 chars -> 3 tokens
		{"This is a longer text", 6}, // 21 chars -> 6 tokens
		{"A very long sentence that should result in many tokens being counted", 19}, // 68 chars -> 19 tokens
	}

	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			got := EstimateTokens(tt.text)
			if got != tt.expected {
				t.Errorf("EstimateTokens(%q) = %d, want %d", tt.text, got, tt.expected)
			}
		})
	}
}

func TestGetModelPricing(t *testing.T) {
	tests := []struct {
		model      string
		wantInput  float64
		wantOutput float64
	}{
		{"claude-opus", 0.015, 0.075},
		{"claude-sonnet", 0.003, 0.015},
		{"claude-haiku", 0.00025, 0.00125},
		{"claude-opus-4-5-20251101", 0.015, 0.075}, // date-suffixed variant
		{"gpt-4o", 0.005, 0.015},
		{"gpt-4o-mini", 0.00015, 0.0006},
		{"gpt-4o-20250101", 0.005, 0.015}, // date-suffixed variant
		{"gemini-flash", 0.000075, 0.0003},
		{"unknown-model", 0.003, 0.015}, // Falls back to default
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			pricing := GetModelPricing(tt.model)
			if pricing.InputPer1K != tt.wantInput {
				t.Errorf("GetModelPricing(%q).InputPer1K = %v, want %v", tt.model, pricing.InputPer1K, tt.wantInput)
			}
			if pricing.OutputPer1K != tt.wantOutput {
				t.Errorf("GetModelPricing(%q).OutputPer1K = %v, want %v", tt.model, pricing.OutputPer1K, tt.wantOutput)
			}
		})
	}
}

func TestFormatCost(t *testing.T) {
	tests := []struct {
		usd  float64
		want string
	}{
		{0.0001, "$0.0001"},
		{0.001, "$0.0010"},
		{0.01, "$0.010"},
		{0.1, "$0.100"},
		{1.0, "$1.00"},
		{10.5, "$10.50"},
		{100.99, "$100.99"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := FormatCost(tt.usd)
			if got != tt.want {
				t.Errorf("FormatCost(%v) = %q, want %q", tt.usd, got, tt.want)
			}
		})
	}
}

// =============================================================================
// bd-25o0: Additional Cost Estimation Tests
// =============================================================================

func TestPerModelPricingComprehensive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		model       string
		inputPer1K  float64
		outputPer1K float64
	}{
		// Claude models
		{"claude-opus", 0.015, 0.075},
		{"claude-opus-4", 0.015, 0.075},
		{"claude-opus-4-5", 0.015, 0.075},
		{"claude-sonnet", 0.003, 0.015},
		{"claude-sonnet-4", 0.003, 0.015},
		{"claude-haiku", 0.00025, 0.00125},
		{"claude-haiku-3-5", 0.00025, 0.00125},
		{"claude-3-opus", 0.015, 0.075},
		{"claude-3-sonnet", 0.003, 0.015},
		{"claude-3-haiku", 0.00025, 0.00125},
		{"claude-3-5-sonnet", 0.003, 0.015},
		{"claude-3-5-haiku", 0.00025, 0.00125},

		// OpenAI models
		{"gpt-4o", 0.005, 0.015},
		{"gpt-4o-mini", 0.00015, 0.0006},
		{"gpt-4-turbo", 0.01, 0.03},
		{"gpt-4", 0.03, 0.06},
		{"o1", 0.015, 0.06},
		{"o1-mini", 0.003, 0.012},
		{"o1-preview", 0.015, 0.06},

		// Google models
		{"gemini-pro", 0.00025, 0.0005},
		{"gemini-pro-1.5", 0.00025, 0.0005},
		{"gemini-ultra", 0.00125, 0.00375},
		{"gemini-flash", 0.000075, 0.0003},
		{"gemini-flash-1.5", 0.000075, 0.0003},
		{"gemini-2.0-flash", 0.000075, 0.0003},

		// Unknown model defaults
		{"unknown-model", 0.003, 0.015},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			t.Parallel()

			pricing := GetModelPricing(tt.model)

			t.Logf("COST_TEST: Pricing | Model=%s | InputPer1K=$%.5f | OutputPer1K=$%.5f",
				tt.model, pricing.InputPer1K, pricing.OutputPer1K)

			if pricing.InputPer1K != tt.inputPer1K {
				t.Errorf("InputPer1K = %v, want %v", pricing.InputPer1K, tt.inputPer1K)
			}
			if pricing.OutputPer1K != tt.outputPer1K {
				t.Errorf("OutputPer1K = %v, want %v", pricing.OutputPer1K, tt.outputPer1K)
			}
		})
	}
}

func TestCostFormatting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		cost     float64
		expected string
	}{
		{0.00001, "$0.0000"},
		{0.0001, "$0.0001"},
		{0.001, "$0.0010"},
		{0.005, "$0.0050"},
		{0.01, "$0.010"},
		{0.05, "$0.050"},
		{0.10, "$0.100"},
		{0.50, "$0.500"},
		{1.00, "$1.00"},
		{5.00, "$5.00"},
		{10.00, "$10.00"},
		{99.99, "$99.99"},
		{100.00, "$100.00"},
		{1000.50, "$1000.50"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			t.Parallel()

			formatted := FormatCost(tt.cost)

			t.Logf("COST_TEST: Format | Cost=%.6f | Formatted=%s", tt.cost, formatted)

			if formatted != tt.expected {
				t.Errorf("FormatCost(%.6f) = %q, want %q", tt.cost, formatted, tt.expected)
			}
		})
	}
}
