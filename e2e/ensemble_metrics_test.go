//go:build e2e
// +build e2e

// Package e2e contains end-to-end tests for NTM observability metrics.
// These tests use deterministic fixtures (no model calls).
package e2e

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
)

func buildMetricsReport(
	coverage *ensemble.CoverageReport,
	redundancy *ensemble.RedundancyAnalysis,
	velocity *ensemble.VelocityReport,
	conflicts *ensemble.ConflictDensity,
) string {
	var b strings.Builder
	if coverage != nil {
		covered := 0
		for _, category := range ensemble.AllCategories() {
			if cov, ok := coverage.PerCategory[category]; ok && len(cov.UsedModes) > 0 {
				covered++
			}
		}
		fmt.Fprintf(&b, "Category Coverage: %.2f (%d/%d categories)\n", coverage.Overall, covered, len(ensemble.AllCategories()))
		if len(coverage.Suggestions) > 0 {
			fmt.Fprintf(&b, "Coverage Suggestions: %s\n", strings.Join(coverage.Suggestions, "; "))
		}
	}
	if redundancy != nil {
		b.WriteString(redundancy.Render())
		b.WriteString("\n")
	}
	if velocity != nil {
		fmt.Fprintf(&b, "Findings Velocity: %.2f\n", velocity.Overall)
		if len(velocity.Suggestions) > 0 {
			fmt.Fprintf(&b, "Velocity Suggestions: %s\n", strings.Join(velocity.Suggestions, "; "))
		}
	}
	if conflicts != nil {
		fmt.Fprintf(&b, "Conflicts: %d detected\n", conflicts.TotalConflicts)
	}
	return b.String()
}

func testMetricsCatalog(t *testing.T) *ensemble.ModeCatalog {
	modes := []ensemble.ReasoningMode{
		{
			ID:        "formal",
			Code:      "A1",
			Name:      "Formal Mode",
			Category:  ensemble.CategoryFormal,
			Tier:      ensemble.TierCore,
			ShortDesc: "formal test",
		},
		{
			ID:        "ampliative",
			Code:      "B1",
			Name:      "Ampliative Mode",
			Category:  ensemble.CategoryAmpliative,
			Tier:      ensemble.TierCore,
			ShortDesc: "ampliative test",
		},
		{
			ID:        "uncertainty",
			Code:      "C1",
			Name:      "Uncertainty Mode",
			Category:  ensemble.CategoryUncertainty,
			Tier:      ensemble.TierCore,
			ShortDesc: "uncertainty test",
		},
	}

	catalog, err := ensemble.NewModeCatalog(modes, "test")
	if err != nil {
		if t != nil {
			t.Fatalf("NewModeCatalog: %v", err)
		}
		return nil
	}
	return catalog
}

func modeOutput(modeID, thesis string, findings []ensemble.Finding) ensemble.ModeOutput {
	return ensemble.ModeOutput{
		ModeID:      modeID,
		Thesis:      thesis,
		TopFindings: findings,
	}
}

func finding(text, evidence string) ensemble.Finding {
	return ensemble.Finding{
		Finding:         text,
		Impact:          ensemble.ImpactLow,
		Confidence:      0.5,
		EvidencePointer: evidence,
	}
}

func containsCategory(categories []ensemble.ModeCategory, target ensemble.ModeCategory) bool {
	for _, category := range categories {
		if category == target {
			return true
		}
	}
	return false
}
