package ensemble

import "testing"

func TestValidateModeIDs_EmptyAndDuplicate(t *testing.T) {
	catalog := testModeCatalog(t)
	report := NewValidationReport()
	validateModeIDs(nil, catalog, false, report)
	if len(report.Errors) == 0 {
		t.Fatal("expected errors for empty mode list")
	}

	report = NewValidationReport()
	validateModeIDs([]string{"deductive", "deductive"}, catalog, false, report)
	if len(report.Errors) == 0 {
		t.Fatal("expected errors for duplicate modes")
	}
}

func TestSuggestModeCodes(t *testing.T) {
	catalog := testModeCatalog(t)
	suggestions := suggestModeCodes("A", catalog)
	if len(suggestions) == 0 {
		t.Fatal("expected suggestions for mode codes")
	}
}

func TestValidateEnsemblePresets_Extensions(t *testing.T) {
	catalog := testModeCatalog(t)
	presets := []EnsemblePreset{
		{
			Name:        "base",
			Description: "base",
			Modes:       []ModeRef{ModeRefFromID("deductive"), ModeRefFromID("abductive")},
		},
		{
			Name:        "child",
			Description: "child",
			Extends:     "bas",
			Modes:       []ModeRef{ModeRefFromID("deductive"), ModeRefFromID("abductive")},
		},
	}

	report := ValidateEnsemblePresets(presets, catalog)
	if report == nil || len(report.Errors) == 0 {
		t.Fatal("expected validation errors for missing extends")
	}
}

func TestValidateBudgetConfig_TooHigh(t *testing.T) {
	report := NewValidationReport()
	validateBudgetConfig(BudgetConfig{
		MaxTokensPerMode: 500000,
		MaxTotalTokens:   2000000,
	}, report)
	if len(report.Errors) == 0 {
		t.Fatal("expected budget validation errors")
	}
}

func TestValidateEnsemblePreset_ModeRefsByCode(t *testing.T) {
	catalog := testModeCatalog(t)
	preset := EnsemblePreset{
		Name:        "code-refs",
		Description: "uses mode codes",
		Modes: []ModeRef{
			ModeRefFromCode("A1"),
			ModeRefFromCode("C1"),
		},
	}

	report := ValidateEnsemblePreset(&preset, catalog, nil)
	if len(report.Errors) > 0 {
		t.Fatalf("expected no errors, got: %+v", report.Errors)
	}
}

func TestValidateEnsemblePreset_TierEnforcement(t *testing.T) {
	catalog := testModeCatalog(t)
	preset := EnsemblePreset{
		Name:        "tier-blocked",
		Description: "includes advanced mode without allow_advanced",
		Modes: []ModeRef{
			ModeRefFromID("advanced-mode"),
			ModeRefFromID("deductive"),
		},
		AllowAdvanced: false,
	}

	report := ValidateEnsemblePreset(&preset, catalog, nil)
	if len(report.Errors) == 0 {
		t.Fatal("expected errors for advanced mode without allow_advanced")
	}
	if !hasErrorCode(report, "TIER_NOT_ALLOWED") {
		t.Fatalf("expected TIER_NOT_ALLOWED error, got: %+v", report.Errors)
	}
}

func TestValidateEnsemblePreset_AllowAdvanced(t *testing.T) {
	catalog := testModeCatalog(t)
	preset := EnsemblePreset{
		Name:        "tier-allowed",
		Description: "advanced allowed",
		Modes: []ModeRef{
			ModeRefFromID("advanced-mode"),
			ModeRefFromID("deductive"),
		},
		AllowAdvanced: true,
	}

	report := ValidateEnsemblePreset(&preset, catalog, nil)
	if len(report.Errors) > 0 {
		t.Fatalf("expected no errors, got: %+v", report.Errors)
	}
}

func TestValidateEnsemblePresets_ExtendsCycle(t *testing.T) {
	catalog := testModeCatalog(t)
	presets := []EnsemblePreset{
		{
			Name:        "alpha",
			Description: "alpha",
			Extends:     "beta",
			Modes:       []ModeRef{ModeRefFromID("deductive"), ModeRefFromID("abductive")},
		},
		{
			Name:        "beta",
			Description: "beta",
			Extends:     "alpha",
			Modes:       []ModeRef{ModeRefFromID("deductive"), ModeRefFromID("abductive")},
		},
	}

	report := ValidateEnsemblePresets(presets, catalog)
	if len(report.Errors) == 0 {
		t.Fatal("expected errors for extends cycle")
	}
	if !hasErrorCode(report, "EXTENDS_CYCLE") {
		t.Fatalf("expected EXTENDS_CYCLE error, got: %+v", report.Errors)
	}
}

func TestValidateEnsemblePreset_ModeCodeNotFound(t *testing.T) {
	catalog := testModeCatalog(t)
	preset := EnsemblePreset{
		Name:        "missing-code",
		Description: "bad mode code",
		Modes: []ModeRef{
			ModeRefFromCode("A9"),
			ModeRefFromID("deductive"),
		},
	}

	report := ValidateEnsemblePreset(&preset, catalog, nil)
	if len(report.Errors) == 0 {
		t.Fatal("expected errors for unknown mode code")
	}
	if !hasErrorCode(report, "MODE_CODE_NOT_FOUND") {
		t.Fatalf("expected MODE_CODE_NOT_FOUND error, got: %+v", report.Errors)
	}
}

func hasErrorCode(report *ValidationReport, code string) bool {
	if report == nil {
		return false
	}
	for _, issue := range report.Errors {
		if issue.Code == code {
			return true
		}
	}
	return false
}
