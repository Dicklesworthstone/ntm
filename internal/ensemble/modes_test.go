package ensemble

import (
	"sort"
	"testing"
)

func TestEmbeddedModes_Count(t *testing.T) {
	if got := len(EmbeddedModes); got != 80 {
		t.Errorf("EmbeddedModes count = %d, want 80", got)
	}
	logSampleModes(t, EmbeddedModes, 5)
}

func TestEmbeddedModes_CoreCount(t *testing.T) {
	core := 0
	for _, m := range EmbeddedModes {
		if m.Tier == TierCore {
			core++
		}
	}
	if core != 28 {
		t.Errorf("EmbeddedModes core count = %d, want 28", core)
	}
}

func TestEmbeddedModes_UniqueIDsAndCodes(t *testing.T) {
	ids := make(map[string]bool)
	codes := make(map[string]string)
	for _, m := range EmbeddedModes {
		if m.ID == "" {
			t.Error("embedded mode has empty ID")
			continue
		}
		if ids[m.ID] {
			t.Errorf("duplicate embedded mode ID: %q", m.ID)
		}
		ids[m.ID] = true
		if m.Code == "" {
			t.Errorf("embedded mode %q has empty code", m.ID)
			continue
		}
		if prev, exists := codes[m.Code]; exists {
			t.Errorf("duplicate embedded mode code %q: %q and %q", m.Code, prev, m.ID)
		}
		codes[m.Code] = m.ID
	}
}

func TestEmbeddedModes_RequiredFields(t *testing.T) {
	for i, m := range EmbeddedModes {
		if m.ID == "" || m.Name == "" || m.ShortDesc == "" || m.Description == "" || m.Outputs == "" {
			t.Errorf("EmbeddedModes[%d] (%q) missing required fields", i, m.ID)
		}
		if !m.Category.IsValid() {
			t.Errorf("EmbeddedModes[%d] (%q) has invalid category %q", i, m.ID, m.Category)
		}
		if len(m.BestFor) == 0 {
			t.Errorf("EmbeddedModes[%d] (%q) missing best_for entries", i, m.ID)
		}
		if len(m.FailureModes) == 0 {
			t.Errorf("EmbeddedModes[%d] (%q) missing failure_modes entries", i, m.ID)
		}
		if err := m.Validate(); err != nil {
			t.Errorf("EmbeddedModes[%d] (%q) validation failed: %v", i, m.ID, err)
		}
	}
}

func TestEmbeddedModes_ValidCategories(t *testing.T) {
	for _, m := range EmbeddedModes {
		if !m.Category.IsValid() {
			t.Errorf("mode %q has invalid category %q", m.ID, m.Category)
		}
	}
}

func TestEmbeddedModes_TierDefaults(t *testing.T) {
	for _, m := range EmbeddedModes {
		if m.Tier == "" {
			t.Errorf("mode %q has empty tier", m.ID)
			continue
		}
		if !m.Tier.IsValid() {
			t.Errorf("mode %q has invalid tier %q", m.ID, m.Tier)
		}
	}
}

func TestGetModeByID_Found(t *testing.T) {
	catalog, err := DefaultCatalog()
	if err != nil {
		t.Fatalf("DefaultCatalog() error: %v", err)
	}
	mode := catalog.GetMode("deductive")
	if mode == nil {
		t.Fatal("GetMode(deductive) returned nil")
	}
	if mode.Code != "A1" {
		t.Errorf("GetMode(deductive).Code = %q, want %q", mode.Code, "A1")
	}
}

func TestGetModeByID_NotFound(t *testing.T) {
	catalog, err := DefaultCatalog()
	if err != nil {
		t.Fatalf("DefaultCatalog() error: %v", err)
	}
	if catalog.GetMode("does-not-exist") != nil {
		t.Error("GetMode(does-not-exist) should return nil")
	}
}

func TestGetModeByCode_Found(t *testing.T) {
	catalog, err := DefaultCatalog()
	if err != nil {
		t.Fatalf("DefaultCatalog() error: %v", err)
	}
	mode := catalog.GetModeByCode("A1")
	if mode == nil {
		t.Fatal("GetModeByCode(A1) returned nil")
	}
	if mode.ID != "deductive" {
		t.Errorf("GetModeByCode(A1).ID = %q, want %q", mode.ID, "deductive")
	}
}

func TestGetModeByCode_NotFound(t *testing.T) {
	catalog, err := DefaultCatalog()
	if err != nil {
		t.Fatalf("DefaultCatalog() error: %v", err)
	}
	if catalog.GetModeByCode("Z99") != nil {
		t.Error("GetModeByCode(Z99) should return nil")
	}
}

func logSampleModes(t *testing.T, modes []ReasoningMode, count int) {
	if count <= 0 {
		return
	}
	sorted := make([]ReasoningMode, len(modes))
	copy(sorted, modes)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].ID < sorted[j].ID
	})
	limit := count
	if limit > len(sorted) {
		limit = len(sorted)
	}
	samples := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		samples = append(samples, sorted[i].ID+"("+sorted[i].Code+")")
	}
	t.Logf("embedded modes sample: %v", samples)
}
