package cost

import (
	"strings"
	"testing"
)

// The dashboard multiplied a ~3.5-chars-per-token estimate of scraped pane
// output by this table and rendered the result as "$0.0137" under a panel
// titled "Cost Tracking". Two separate claims the numbers cannot support:
// that the amount is tracked, and that it is precise to a hundredth of a cent.

func TestGetModelPricingInfoReportsUnknownModels(t *testing.T) {
	cases := map[string]bool{
		// In the table (May 2025), directly or by prefix.
		"claude-opus":     true,
		"claude-sonnet-4": true,
		"gpt-4o":          true,
		"claude-opus-5":   true, // prefix match on claude-opus
		// Not in the table: these get the default row.
		"gpt-6-astra":             false,
		"gemini-3.1-pro-preview":  false,
		"some-model-nobody-ships": false,
		"":                        false,
	}

	for model, wantKnown := range cases {
		t.Run(model, func(t *testing.T) {
			_, known := GetModelPricingInfo(model)
			if known != wantKnown {
				t.Errorf("GetModelPricingInfo(%q) known = %v, want %v", model, known, wantKnown)
			}
		})
	}
}

// Asking for "default" explicitly is still not knowledge of a model's price.
func TestDefaultRowIsNeverReportedAsKnown(t *testing.T) {
	if _, known := GetModelPricingInfo("default"); known {
		t.Error(`the "default" row reported itself as a known model price`)
	}
}

// GetModelPricing keeps its old signature and its old answers.
func TestGetModelPricingMatchesInfo(t *testing.T) {
	for _, model := range []string{"claude-opus", "gpt-6-astra", "gemini-flash", ""} {
		want, _ := GetModelPricingInfo(model)
		if got := GetModelPricing(model); got != want {
			t.Errorf("GetModelPricing(%q) = %+v, want %+v", model, got, want)
		}
	}
}

// An estimate accurate to roughly a factor of two must not be rendered with
// four decimal places.
func TestFormatCostEstimateDropsFalsePrecision(t *testing.T) {
	cases := map[float64]string{
		0:        "~$0",
		0.000012: "<$0.01",
		0.0099:   "<$0.01",
		0.01:     "~$0.01",
		1.239:    "~$1.24",
		1234.5:   "~$1234.50",
	}

	for amount, want := range cases {
		if got := FormatCostEstimate(amount); got != want {
			t.Errorf("FormatCostEstimate(%v) = %q, want %q", amount, got, want)
		}
	}

	// No estimate may render sub-cent digits, which is what claimed precision
	// the estimate never had.
	for _, amount := range []float64{0.0001, 0.00567, 0.12345} {
		got := FormatCostEstimate(amount)
		if strings.Count(got, "0") > 2 && strings.Contains(got, "0.00") && got != "<$0.01" {
			t.Errorf("FormatCostEstimate(%v) = %q still shows sub-cent precision", amount, got)
		}
	}
}

// FormatCost stays precise: provider-reported amounts are real.
func TestFormatCostKeepsPrecisionForRealAmounts(t *testing.T) {
	if got := FormatCost(0.0001); got != "$0.0001" {
		t.Errorf("FormatCost(0.0001) = %q, want $0.0001", got)
	}
}
