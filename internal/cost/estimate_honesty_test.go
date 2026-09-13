package cost

import (
	"strings"
	"testing"
)

// The dashboard multiplied a ~3.5-chars-per-token estimate of scraped pane
// output by this table and rendered the result as "$0.0137" under a panel
// titled "Cost Tracking". Two separate claims the numbers cannot support:
// that the amount is tracked, and that it is precise to a hundredth of a cent.

func TestGetModelPricingInfoClassifiesTheMatch(t *testing.T) {
	cases := map[string]PricingMatch{
		// Named in the table (vintage May 2025).
		"claude-opus":     PricingExact,
		"claude-sonnet-4": PricingExact,
		"gpt-4o":          PricingExact,
		// Not named; priced from a shorter family prefix. A new generation
		// routinely reprices, so this is not knowledge of its rate.
		"claude-opus-5":   PricingFamily,
		"gpt-4o-20240513": PricingExact, // 8-digit date suffix stripped, then exact
		"claude-haiku-99": PricingFamily,
		// Nothing matched: the default row.
		"gpt-6-astra":             PricingUnknown,
		"gemini-3.1-pro-preview":  PricingUnknown,
		"some-model-nobody-ships": PricingUnknown,
		"":                        PricingUnknown,
	}

	for model, want := range cases {
		t.Run(model, func(t *testing.T) {
			_, got := GetModelPricingInfo(model)
			if got != want {
				t.Errorf("GetModelPricingInfo(%q) match = %v, want %v", model, got, want)
			}
		})
	}
}

// A family match must never be mistaken for knowing this model's price — that
// was the original defect: claude-opus-5 reported as a known rate while being
// charged at the claude-opus (Opus-4 era) figure.
func TestFamilyMatchIsNotAnExactMatch(t *testing.T) {
	_, match := GetModelPricingInfo("claude-opus-5")
	if match == PricingExact {
		t.Error("claude-opus-5 is not in the table; reporting an exact match claims a verified price")
	}
	if !match.Known() {
		t.Error("a family match should still count as priced, just not exactly")
	}
}

// Asking for "default" explicitly is still not knowledge of a model's price.
func TestDefaultRowIsNeverReportedAsKnown(t *testing.T) {
	if _, match := GetModelPricingInfo("default"); match.Known() {
		t.Error(`the "default" row reported itself as a known model price`)
	}
}

// The vintage has to be stated, since it is what tells an operator the table
// predates the models it is pricing.
func TestPricingTableVintageIsDeclared(t *testing.T) {
	if PricingTableVintage == "" {
		t.Error("the price table vintage is empty; prices must carry their age")
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
