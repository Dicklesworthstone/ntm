package ensemble

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

func TestAdvisor_BudgetWarning(t *testing.T) {
	input := map[string]any{"mode": "formal-budget", "budget": "low"}
	logTestStartAdvisor(t, input)

	mode := ReasoningMode{
		ID:        "formal-budget",
		Code:      "A2",
		Name:      "Budget Mode",
		Category:  CategoryFormal,
		Tier:      TierCore,
		ShortDesc: "Budget-conscious reasoning",
	}
	catalog, err := NewModeCatalog([]ReasoningMode{mode}, "1.0")
	assertNoErrorAdvisor(t, "new catalog", err)

	estimator := NewEstimator(catalog, slog.New(slog.NewTextHandler(io.Discard, nil)))
	estimate, err := estimator.Estimate(context.Background(), EstimateInput{
		ModeIDs:  []string{mode.ID},
		Question: "Budget warning",
		Budget: BudgetConfig{
			MaxTokensPerMode: 500,
			MaxTotalTokens:   300,
		},
	}, EstimateOptions{ContextPack: &ContextPack{TokenEstimate: 200}})
	logTestResultAdvisor(t, estimate)
	assertNoErrorAdvisor(t, "estimate", err)
	assertTrueAdvisor(t, "over budget", estimate.OverBudget)
	assertTrueAdvisor(t, "warnings present", len(estimate.Warnings) > 0)
}

func TestAdvisor_ModeAlternatives(t *testing.T) {
	input := map[string]any{"mode": "formal-exp", "alternative": "formal-core"}
	logTestStartAdvisor(t, input)

	expensive := ReasoningMode{
		ID:        "formal-exp",
		Code:      "A3",
		Name:      "Formal Exp",
		Category:  CategoryFormal,
		Tier:      TierExperimental,
		ShortDesc: "Experimental formal reasoning",
	}
	cheaper := ReasoningMode{
		ID:        "formal-core",
		Code:      "A4",
		Name:      "Formal Core",
		Category:  CategoryFormal,
		Tier:      TierCore,
		ShortDesc: "Core formal reasoning",
	}
	catalog, err := NewModeCatalog([]ReasoningMode{expensive, cheaper}, "1.0")
	assertNoErrorAdvisor(t, "new catalog", err)

	estimator := NewEstimator(catalog, slog.New(slog.NewTextHandler(io.Discard, nil)))
	estimate, err := estimator.Estimate(context.Background(), EstimateInput{
		ModeIDs:       []string{expensive.ID},
		Question:      "Find alternatives",
		AllowAdvanced: true,
		Budget: BudgetConfig{
			MaxTokensPerMode: 4000,
			MaxTotalTokens:   1000,
		},
	}, EstimateOptions{DisableContext: true})
	logTestResultAdvisor(t, estimate)
	assertNoErrorAdvisor(t, "estimate", err)
	assertTrueAdvisor(t, "alternatives suggested", len(estimate.Modes[0].Alternatives) > 0)
}

func TestAdvisor_HistoricalCalibration(t *testing.T) {
	input := map[string]any{"mode": "formal-calibrate"}
	logTestStartAdvisor(t, input)

	mode := ReasoningMode{
		ID:        "formal-calibrate",
		Code:      "A5",
		Name:      "Calibrate",
		Category:  CategoryFormal,
		Tier:      TierCore,
		ShortDesc: "Calibration mode",
	}
	catalog, err := NewModeCatalog([]ReasoningMode{mode}, "1.0")
	assertNoErrorAdvisor(t, "new catalog", err)

	estimator := NewEstimator(catalog, slog.New(slog.NewTextHandler(io.Discard, nil)))
	estimate, err := estimator.Estimate(context.Background(), EstimateInput{
		ModeIDs:  []string{mode.ID},
		Question: "Calibration",
		Budget:   DefaultBudgetConfig(),
	}, EstimateOptions{DisableContext: true})
	logTestResultAdvisor(t, estimate)
	assertNoErrorAdvisor(t, "estimate", err)
	assertTrueAdvisor(t, "generated at set", !estimate.GeneratedAt.IsZero())
	assertTrueAdvisor(t, "value score positive", estimate.Modes[0].ValueScore > 0)
}

func logTestStartAdvisor(t *testing.T, input any) {
	t.Helper()
	t.Logf("TEST: %s - starting with input: %v", t.Name(), input)
}

func logTestResultAdvisor(t *testing.T, result any) {
	t.Helper()
	t.Logf("TEST: %s - got result: %v", t.Name(), result)
}

func assertNoErrorAdvisor(t *testing.T, desc string, err error) {
	t.Helper()
	t.Logf("TEST: %s - assertion: %s", t.Name(), desc)
	if err != nil {
		t.Fatalf("%s: %v", desc, err)
	}
}

func assertTrueAdvisor(t *testing.T, desc string, ok bool) {
	t.Helper()
	t.Logf("TEST: %s - assertion: %s", t.Name(), desc)
	if !ok {
		t.Fatalf("assertion failed: %s", desc)
	}
}

func assertEqualAdvisor(t *testing.T, desc string, got, want any) {
	t.Helper()
	t.Logf("TEST: %s - assertion: %s", t.Name(), desc)
	if got != want {
		t.Fatalf("%s: got %v want %v", desc, got, want)
	}
}
