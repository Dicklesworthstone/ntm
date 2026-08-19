package ensemble

import (
	"testing"
)

// ---------------------------------------------------------------------------
// issueString — missing branch: empty field
// ---------------------------------------------------------------------------

func TestIssueString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		issue ValidationIssue
		want  string
	}{
		{
			name:  "with_field",
			issue: ValidationIssue{Field: "budget.max", Message: "too high"},
			want:  "budget.max: too high",
		},
		{
			name:  "empty_field",
			issue: ValidationIssue{Message: "something went wrong"},
			want:  "something went wrong",
		},
		{
			name:  "both_empty",
			issue: ValidationIssue{},
			want:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := issueString(tc.issue)
			if got != tc.want {
				t.Errorf("issueString(%+v) = %q, want %q", tc.issue, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ValidationReport.add — missing branches: nil receiver, SeverityInfo
// ---------------------------------------------------------------------------

func TestValidationReport_Add_NilReceiver(t *testing.T) {
	t.Parallel()

	// Should not panic on nil receiver.
	var r *ValidationReport
	r.add(ValidationIssue{Code: "X", Message: "ignored"})
}

func TestValidationReport_Add_SeverityInfo(t *testing.T) {
	t.Parallel()

	r := NewValidationReport()
	r.add(ValidationIssue{Code: "INFO1", Severity: SeverityInfo, Message: "fyi"})
	if len(r.Infos) != 1 {
		t.Fatalf("expected 1 info, got %d", len(r.Infos))
	}
	if r.Infos[0].Code != "INFO1" {
		t.Errorf("expected code INFO1, got %q", r.Infos[0].Code)
	}
}

func TestValidationReport_Add_DefaultSeverity(t *testing.T) {
	t.Parallel()

	r := NewValidationReport()
	// Empty severity should default to SeverityError.
	r.add(ValidationIssue{Code: "BOOM", Message: "bad"})
	if len(r.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(r.Errors))
	}
	if r.Errors[0].Severity != SeverityError {
		t.Errorf("expected severity error, got %q", r.Errors[0].Severity)
	}
}

// ---------------------------------------------------------------------------
// ValidationReport.Merge — missing branches: nil cases
// ---------------------------------------------------------------------------

func TestValidationReport_Merge_NilReceiver(t *testing.T) {
	t.Parallel()

	other := NewValidationReport()
	other.add(ValidationIssue{Code: "A", Severity: SeverityError, Message: "err"})

	var r *ValidationReport
	r.Merge(other) // Should not panic.
}

func TestValidationReport_Merge_NilOther(t *testing.T) {
	t.Parallel()

	r := NewValidationReport()
	r.add(ValidationIssue{Code: "A", Severity: SeverityError, Message: "err"})
	r.Merge(nil) // Should not panic or modify.
	if len(r.Errors) != 1 {
		t.Fatalf("expected 1 error after Merge(nil), got %d", len(r.Errors))
	}
}

func TestValidationReport_Merge_CombinesAll(t *testing.T) {
	t.Parallel()

	r := NewValidationReport()
	r.add(ValidationIssue{Severity: SeverityError, Message: "e1"})

	other := NewValidationReport()
	other.add(ValidationIssue{Severity: SeverityWarning, Message: "w1"})
	other.add(ValidationIssue{Severity: SeverityInfo, Message: "i1"})

	r.Merge(other)
	if len(r.Errors) != 1 {
		t.Errorf("expected 1 error, got %d", len(r.Errors))
	}
	if len(r.Warnings) != 1 {
		t.Errorf("expected 1 warning, got %d", len(r.Warnings))
	}
	if len(r.Infos) != 1 {
		t.Errorf("expected 1 info, got %d", len(r.Infos))
	}
}

// ---------------------------------------------------------------------------
// validateBudgetConfig — missing branches
// ---------------------------------------------------------------------------

func TestValidateBudgetConfig_NegativeValues(t *testing.T) {
	t.Parallel()

	report := NewValidationReport()
	validateBudgetConfig(BudgetConfig{MaxTokensPerMode: -100}, report)
	if len(report.Errors) == 0 {
		t.Fatal("expected errors for negative budget values")
	}
	if !hasErrorCode(report, "BUDGET_NEGATIVE") {
		t.Fatalf("expected BUDGET_NEGATIVE, got: %+v", report.Errors)
	}
}

func TestValidateBudgetConfig_PerModeExceedsTotal(t *testing.T) {
	t.Parallel()

	report := NewValidationReport()
	validateBudgetConfig(BudgetConfig{
		MaxTokensPerMode: 5000,
		MaxTotalTokens:   2000,
	}, report)
	if len(report.Errors) == 0 {
		t.Fatal("expected errors for per-mode exceeding total")
	}
	if !hasErrorCode(report, "BUDGET_PER_MODE_EXCEEDS_TOTAL") {
		t.Fatalf("expected BUDGET_PER_MODE_EXCEEDS_TOTAL, got: %+v", report.Errors)
	}
}

func TestValidateBudgetConfig_ReserveExceedsTotal(t *testing.T) {
	t.Parallel()

	report := NewValidationReport()
	validateBudgetConfig(BudgetConfig{
		MaxTotalTokens:         10000,
		MaxTokensPerMode:       5000,
		SynthesisReserveTokens: 6000,
		ContextReserveTokens:   6000,
	}, report)
	if len(report.Errors) == 0 {
		t.Fatal("expected errors for reserve exceeding total")
	}
	if !hasErrorCode(report, "BUDGET_RESERVE_EXCEEDS_TOTAL") {
		t.Fatalf("expected BUDGET_RESERVE_EXCEEDS_TOTAL, got: %+v", report.Errors)
	}
}

func TestValidateBudgetConfig_Valid(t *testing.T) {
	t.Parallel()

	report := NewValidationReport()
	validateBudgetConfig(BudgetConfig{
		MaxTokensPerMode: 5000,
		MaxTotalTokens:   50000,
	}, report)
	if len(report.Errors) > 0 {
		t.Fatalf("expected no errors, got: %+v", report.Errors)
	}
}

func TestValidateBudgetConfig_ZeroValues(t *testing.T) {
	t.Parallel()

	// All zeros should be valid (no budget constraints).
	report := NewValidationReport()
	validateBudgetConfig(BudgetConfig{}, report)
	if len(report.Errors) > 0 {
		t.Fatalf("expected no errors for zero config, got: %+v", report.Errors)
	}
}

// ---------------------------------------------------------------------------
// editDistance — missing branches
// ---------------------------------------------------------------------------

func TestEditDistance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a, b string
		want int
	}{
		{"identical", "abc", "abc", 0},
		{"empty_a", "", "abc", 3},
		{"empty_b", "abc", "", 3},
		{"both_empty", "", "", 0},
		{"single_char_diff", "a", "b", 1},
		{"insertion", "abc", "abcd", 1},
		{"deletion", "abcd", "abc", 1},
		{"substitution", "abc", "axc", 1},
		{"completely_different", "abc", "xyz", 3},
		{"unicode", "café", "cafe", 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := editDistance(tc.a, tc.b)
			if got != tc.want {
				t.Errorf("editDistance(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// closestMatches
// ---------------------------------------------------------------------------

func TestClosestMatches(t *testing.T) {
	t.Parallel()

	candidates := []string{"deductive", "abductive", "inductive", "bayesian"}

	t.Run("exact_match_first", func(t *testing.T) {
		t.Parallel()
		matches := closestMatches("deductive", candidates, 3)
		if len(matches) == 0 {
			t.Fatal("expected matches")
		}
		if matches[0] != "deductive" {
			t.Errorf("expected first match to be 'deductive', got %q", matches[0])
		}
	})

	t.Run("similar_match", func(t *testing.T) {
		t.Parallel()
		matches := closestMatches("deductve", candidates, 3)
		if len(matches) == 0 {
			t.Fatal("expected matches")
		}
		if matches[0] != "deductive" {
			t.Errorf("expected closest to be 'deductive', got %q", matches[0])
		}
	})

	t.Run("limit_applied", func(t *testing.T) {
		t.Parallel()
		matches := closestMatches("x", candidates, 2)
		if len(matches) > 2 {
			t.Errorf("expected at most 2 matches, got %d", len(matches))
		}
	})

	t.Run("empty_input", func(t *testing.T) {
		t.Parallel()
		matches := closestMatches("", candidates, 3)
		// Empty input still returns results sorted by distance.
		if len(matches) > 3 {
			t.Errorf("expected at most 3 matches, got %d", len(matches))
		}
	})

	t.Run("case_insensitive", func(t *testing.T) {
		t.Parallel()
		matches := closestMatches("DEDUCTIVE", candidates, 3)
		if len(matches) == 0 || matches[0] != "deductive" {
			t.Errorf("expected case-insensitive match, got %v", matches)
		}
	})
}

// ---------------------------------------------------------------------------
// ValidateEnsemblePreset — nil preset/catalog
// ---------------------------------------------------------------------------

func TestValidateEnsemblePreset_NilPreset(t *testing.T) {
	t.Parallel()

	report := ValidateEnsemblePreset(nil, nil, nil)
	if len(report.Errors) == 0 {
		t.Fatal("expected errors for nil preset")
	}
	if !hasErrorCode(report, "NIL_PRESET") {
		t.Fatalf("expected NIL_PRESET, got: %+v", report.Errors)
	}
}

func TestValidateEnsemblePreset_NilCatalog(t *testing.T) {
	t.Parallel()

	preset := &EnsemblePreset{
		Name:        "test",
		Description: "test",
		Modes:       []ModeRef{ModeRefFromID("x")},
	}
	report := ValidateEnsemblePreset(preset, nil, nil)
	if len(report.Errors) == 0 {
		t.Fatal("expected errors for nil catalog")
	}
	if !hasErrorCode(report, "MISSING_CATALOG") {
		t.Fatalf("expected MISSING_CATALOG, got: %+v", report.Errors)
	}
}

// ---------------------------------------------------------------------------
// validateBudgetConfig — per-mode too high (distinct from total too high)
// ---------------------------------------------------------------------------

func TestValidateBudgetConfig_PerModeTooHigh(t *testing.T) {
	t.Parallel()

	report := NewValidationReport()
	validateBudgetConfig(BudgetConfig{
		MaxTokensPerMode: 300000, // > maxReasonablePerMode (200000)
		MaxTotalTokens:   500000,
	}, report)
	if len(report.Errors) == 0 {
		t.Fatal("expected errors for per-mode too high")
	}
	if !hasErrorCode(report, "BUDGET_PER_MODE_TOO_HIGH") {
		t.Fatalf("expected BUDGET_PER_MODE_TOO_HIGH, got: %+v", report.Errors)
	}
}

// ---------------------------------------------------------------------------
// Render — VelocityTracker
// ---------------------------------------------------------------------------
