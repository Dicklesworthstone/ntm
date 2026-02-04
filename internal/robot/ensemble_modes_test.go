package robot

import (
	"testing"
)

func TestGetEnsembleModes_Default(t *testing.T) {
	t.Log("TEST: TestGetEnsembleModes_Default - starting")

	output, err := GetEnsembleModes(EnsembleModesOptions{})
	if err != nil {
		t.Fatalf("GetEnsembleModes failed: %v", err)
	}

	if !output.Success {
		t.Errorf("expected success=true, got false: %s", output.Error)
	}

	if output.Action != "ensemble_modes" {
		t.Errorf("expected action=ensemble_modes, got %s", output.Action)
	}

	// Default tier is core, so should have some core modes
	if output.CoreModes == 0 {
		t.Error("expected some core modes in catalog")
	}

	// TotalModes should be >= CoreModes
	if output.TotalModes < output.CoreModes {
		t.Errorf("total modes (%d) < core modes (%d)", output.TotalModes, output.CoreModes)
	}

	// Default tier is core
	if output.DefaultTier != "core" {
		t.Errorf("expected default_tier=core, got %s", output.DefaultTier)
	}

	t.Logf("TEST: TestGetEnsembleModes_Default - modes: %d, core: %d, advanced: %d",
		len(output.Modes), output.CoreModes, output.AdvancedModes)
	t.Log("TEST: TestGetEnsembleModes_Default - assertion: default query returns core modes")
}

func TestGetEnsembleModes_CategoryFilter(t *testing.T) {
	t.Log("TEST: TestGetEnsembleModes_CategoryFilter - starting")

	// Filter by category letter
	output, err := GetEnsembleModes(EnsembleModesOptions{
		Category: "A",
		Tier:     "all",
	})
	if err != nil {
		t.Fatalf("GetEnsembleModes failed: %v", err)
	}

	if !output.Success {
		t.Errorf("expected success=true, got false: %s", output.Error)
	}

	// All returned modes should be in category A (Formal)
	for _, mode := range output.Modes {
		if mode.Category.Code != "A" {
			t.Errorf("mode %s has category %s, expected A", mode.ID, mode.Category.Code)
		}
	}

	// Also test by category name
	output2, err := GetEnsembleModes(EnsembleModesOptions{
		Category: "Formal",
		Tier:     "all",
	})
	if err != nil {
		t.Fatalf("GetEnsembleModes failed: %v", err)
	}

	if len(output2.Modes) != len(output.Modes) {
		t.Errorf("filter by name vs code gave different results: %d vs %d",
			len(output2.Modes), len(output.Modes))
	}

	t.Logf("TEST: TestGetEnsembleModes_CategoryFilter - found %d Formal modes", len(output.Modes))
	t.Log("TEST: TestGetEnsembleModes_CategoryFilter - assertion: category filter works")
}

func TestGetEnsembleModes_TierFilter(t *testing.T) {
	t.Log("TEST: TestGetEnsembleModes_TierFilter - starting")

	// Get core modes
	coreOutput, _ := GetEnsembleModes(EnsembleModesOptions{Tier: "core"})

	// Get all modes
	allOutput, _ := GetEnsembleModes(EnsembleModesOptions{Tier: "all"})

	// All modes should be >= core modes
	if len(allOutput.Modes) < len(coreOutput.Modes) {
		t.Errorf("all modes (%d) < core modes (%d)", len(allOutput.Modes), len(coreOutput.Modes))
	}

	// All core output modes should have tier=core
	for _, mode := range coreOutput.Modes {
		if mode.Tier != "core" {
			t.Errorf("core filter returned non-core mode: %s (%s)", mode.ID, mode.Tier)
		}
	}

	t.Logf("TEST: TestGetEnsembleModes_TierFilter - core: %d, all: %d",
		len(coreOutput.Modes), len(allOutput.Modes))
	t.Log("TEST: TestGetEnsembleModes_TierFilter - assertion: tier filter works")
}

func TestGetEnsembleModes_Pagination(t *testing.T) {
	t.Log("TEST: TestGetEnsembleModes_Pagination - starting")

	// Get first page
	page1, _ := GetEnsembleModes(EnsembleModesOptions{
		Tier:   "all",
		Limit:  5,
		Offset: 0,
	})

	// Get second page
	page2, _ := GetEnsembleModes(EnsembleModesOptions{
		Tier:   "all",
		Limit:  5,
		Offset: 5,
	})

	if page1.Pagination == nil {
		t.Error("expected pagination info on first page")
	} else {
		if page1.Pagination.Limit != 5 {
			t.Errorf("expected limit=5, got %d", page1.Pagination.Limit)
		}
		if page1.Pagination.Offset != 0 {
			t.Errorf("expected offset=0, got %d", page1.Pagination.Offset)
		}
	}

	if page1.Pagination != nil {
		expectedNext, expectedPages := paginationHintOffsets(page1.Pagination)
		if page1.AgentHints == nil {
			t.Fatal("expected agent hints when pagination is active")
		}
		if page1.AgentHints.NextOffset == nil || expectedNext == nil ||
			*page1.AgentHints.NextOffset != *expectedNext {
			t.Fatalf("expected next_offset=%v, got %v", expectedNext, page1.AgentHints.NextOffset)
		}
		if page1.AgentHints.PagesRemaining == nil || expectedPages == nil ||
			*page1.AgentHints.PagesRemaining != *expectedPages {
			t.Fatalf("expected pages_remaining=%v, got %v", expectedPages, page1.AgentHints.PagesRemaining)
		}
	}

	// Pages should have different modes
	if len(page1.Modes) > 0 && len(page2.Modes) > 0 {
		if page1.Modes[0].ID == page2.Modes[0].ID {
			t.Error("expected different modes on different pages")
		}
	}

	t.Logf("TEST: TestGetEnsembleModes_Pagination - page1: %d, page2: %d",
		len(page1.Modes), len(page2.Modes))
	t.Log("TEST: TestGetEnsembleModes_Pagination - assertion: pagination works")
}

func TestBuildModesHints_PaginationOnly(t *testing.T) {
	t.Log("TEST: TestBuildModesHints_PaginationOnly - starting")

	output := &EnsembleModesOutput{
		Pagination: &PaginationInfo{
			Limit:  5,
			Offset: 10,
			Count:  5,
			Total:  25,
		},
	}

	hints := buildModesHints(output)
	if hints == nil {
		t.Fatal("expected hints when pagination is present")
	}
	if hints.NextOffset == nil || *hints.NextOffset != 15 {
		t.Fatalf("expected next_offset=15, got %v", hints.NextOffset)
	}
	if hints.PagesRemaining == nil || *hints.PagesRemaining != 2 {
		t.Fatalf("expected pages_remaining=2, got %v", hints.PagesRemaining)
	}
	t.Log("TEST: TestBuildModesHints_PaginationOnly - assertion: pagination hints set")
}

func TestGetEnsembleModes_Categories(t *testing.T) {
	t.Log("TEST: TestGetEnsembleModes_Categories - starting")

	output, _ := GetEnsembleModes(EnsembleModesOptions{Tier: "all"})

	if len(output.Categories) == 0 {
		t.Error("expected category summary in output")
	}

	// Each category should have a valid code and name
	for _, cat := range output.Categories {
		if cat.Code == "" || cat.Name == "" {
			t.Errorf("invalid category: code=%s, name=%s", cat.Code, cat.Name)
		}
		if cat.ModeCount <= 0 {
			t.Errorf("category %s has invalid mode count: %d", cat.Code, cat.ModeCount)
		}
	}

	t.Logf("TEST: TestGetEnsembleModes_Categories - found %d categories", len(output.Categories))
	t.Log("TEST: TestGetEnsembleModes_Categories - assertion: category summary works")
}

func TestGetEnsembleModes_ModeInfo(t *testing.T) {
	t.Log("TEST: TestGetEnsembleModes_ModeInfo - starting")

	output, _ := GetEnsembleModes(EnsembleModesOptions{Limit: 1, Tier: "all"})

	if len(output.Modes) == 0 {
		t.Fatal("expected at least one mode")
	}

	mode := output.Modes[0]

	// Verify required fields
	if mode.ID == "" {
		t.Error("mode ID is empty")
	}
	if mode.Code == "" {
		t.Error("mode Code is empty")
	}
	if mode.Name == "" {
		t.Error("mode Name is empty")
	}
	if mode.Tier == "" {
		t.Error("mode Tier is empty")
	}
	if mode.Category.Code == "" || mode.Category.Name == "" {
		t.Error("mode Category is incomplete")
	}
	if mode.ShortDesc == "" {
		t.Error("mode ShortDesc is empty")
	}

	t.Logf("TEST: TestGetEnsembleModes_ModeInfo - mode: %s (%s), category: %s",
		mode.ID, mode.Code, mode.Category.Name)
	t.Log("TEST: TestGetEnsembleModes_ModeInfo - assertion: mode info is complete")
}
