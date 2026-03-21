package robot

import "testing"

// =============================================================================
// categoryIndex tests
// =============================================================================

func TestCategoryIndex(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cat  string
		want int
	}{
		{"state", "state", 0},
		{"ensemble", "ensemble", 1},
		{"control", "control", 2},
		{"spawn", "spawn", 3},
		{"beads", "beads", 4},
		{"bv", "bv", 5},
		{"cass", "cass", 6},
		{"pipeline", "pipeline", 7},
		{"utility", "utility", 8},
		{"unknown category", "nonexistent", len(categoryOrder)},
		{"empty string", "", len(categoryOrder)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := categoryIndex(tc.cat)
			if got != tc.want {
				t.Errorf("categoryIndex(%q) = %d, want %d", tc.cat, got, tc.want)
			}
		})
	}
}

// =============================================================================
// buildCommandRegistry tests
// =============================================================================

func TestBuildCommandRegistry(t *testing.T) {
	t.Parallel()

	commands := buildCommandRegistry()

	if len(commands) == 0 {
		t.Fatal("buildCommandRegistry() returned empty slice")
	}

	// Verify all commands have required fields
	for i, cmd := range commands {
		if cmd.Name == "" {
			t.Errorf("command[%d] has empty Name", i)
		}
		if cmd.Flag == "" {
			t.Errorf("command[%d] (%s) has empty Flag", i, cmd.Name)
		}
		if cmd.Category == "" {
			t.Errorf("command[%d] (%s) has empty Category", i, cmd.Name)
		}
		if cmd.Description == "" {
			t.Errorf("command[%d] (%s) has empty Description", i, cmd.Name)
		}
	}
}

func TestBuildCommandRegistryUniqueNames(t *testing.T) {
	t.Parallel()

	commands := buildCommandRegistry()
	seen := make(map[string]bool)

	for _, cmd := range commands {
		if seen[cmd.Name] {
			t.Errorf("duplicate command name: %q", cmd.Name)
		}
		seen[cmd.Name] = true
	}
}

func TestBuildCommandRegistryUniqueFlags(t *testing.T) {
	t.Parallel()

	commands := buildCommandRegistry()
	seen := make(map[string]bool)

	for _, cmd := range commands {
		if seen[cmd.Flag] {
			t.Errorf("duplicate command flag: %q", cmd.Flag)
		}
		seen[cmd.Flag] = true
	}
}

func TestBuildCommandRegistryValidCategories(t *testing.T) {
	t.Parallel()

	commands := buildCommandRegistry()
	validCategories := make(map[string]bool)
	for _, cat := range categoryOrder {
		validCategories[cat] = true
	}

	for _, cmd := range commands {
		if !validCategories[cmd.Category] {
			t.Errorf("command %q has invalid category %q", cmd.Name, cmd.Category)
		}
	}
}

func TestBuildCommandRegistryExamples(t *testing.T) {
	t.Parallel()

	commands := buildCommandRegistry()

	for _, cmd := range commands {
		if len(cmd.Examples) == 0 {
			t.Errorf("command %q has no examples", cmd.Name)
		}
	}
}

func TestBuildCommandRegistryParameterFields(t *testing.T) {
	t.Parallel()

	commands := buildCommandRegistry()

	for _, cmd := range commands {
		for j, param := range cmd.Parameters {
			if param.Name == "" {
				t.Errorf("command %q param[%d] has empty Name", cmd.Name, j)
			}
			if param.Flag == "" {
				t.Errorf("command %q param[%d] has empty Flag", cmd.Name, j)
			}
			if param.Type == "" {
				t.Errorf("command %q param[%d] has empty Type", cmd.Name, j)
			}
			if param.Description == "" {
				t.Errorf("command %q param[%d] has empty Description", cmd.Name, j)
			}
		}
	}
}

// =============================================================================
// GetCapabilities tests
// =============================================================================

func TestGetCapabilities(t *testing.T) {
	t.Parallel()

	output, err := GetCapabilities()
	if err != nil {
		t.Fatalf("GetCapabilities() error: %v", err)
	}
	if output == nil {
		t.Fatal("GetCapabilities() returned nil")
	}

	if len(output.Commands) == 0 {
		t.Error("expected non-empty Commands")
	}
	if len(output.Categories) != len(categoryOrder) {
		t.Errorf("Categories length = %d, want %d", len(output.Categories), len(categoryOrder))
	}
	if output.Attention == nil {
		t.Fatal("expected attention capabilities")
	}
	if output.Attention.ContractVersion != AttentionContractVersion {
		t.Errorf("Attention.ContractVersion = %q, want %q", output.Attention.ContractVersion, AttentionContractVersion)
	}
	beadOrphaned, ok := output.Attention.SignalAvailability[AttentionSignalBeadOrphaned]
	if !ok {
		t.Fatalf("expected %q signal availability entry", AttentionSignalBeadOrphaned)
	}
	if beadOrphaned.Status != CapabilityUnavailable {
		t.Errorf("bead_orphaned status = %q, want %q", beadOrphaned.Status, CapabilityUnavailable)
	}
}

func TestGetCapabilitiesSortOrder(t *testing.T) {
	t.Parallel()

	output, err := GetCapabilities()
	if err != nil {
		t.Fatalf("GetCapabilities() error: %v", err)
	}

	// Verify commands are sorted by category then name
	for i := 1; i < len(output.Commands); i++ {
		prev := output.Commands[i-1]
		curr := output.Commands[i]

		prevIdx := categoryIndex(prev.Category)
		currIdx := categoryIndex(curr.Category)

		if prevIdx > currIdx {
			t.Errorf("commands not sorted by category: %q (%s) before %q (%s)",
				prev.Name, prev.Category, curr.Name, curr.Category)
		}
		if prevIdx == currIdx && prev.Name > curr.Name {
			t.Errorf("commands not sorted by name within category %q: %q before %q",
				prev.Category, prev.Name, curr.Name)
		}
	}
}

func TestDefaultAttentionCapabilities_BeadOrphanedUnsupported(t *testing.T) {
	t.Parallel()

	caps := DefaultAttentionCapabilities()
	if caps == nil {
		t.Fatal("DefaultAttentionCapabilities() returned nil")
	}
	if caps.ContractVersion != AttentionContractVersion {
		t.Errorf("ContractVersion = %q, want %q", caps.ContractVersion, AttentionContractVersion)
	}
	signal, ok := caps.SignalAvailability[AttentionSignalBeadOrphaned]
	if !ok {
		t.Fatalf("expected %q in SignalAvailability", AttentionSignalBeadOrphaned)
	}
	if signal.Status != CapabilityUnavailable {
		t.Errorf("Status = %q, want %q", signal.Status, CapabilityUnavailable)
	}
	if signal.Note == "" {
		t.Error("expected unsupported note for bead_orphaned")
	}
}

// =============================================================================
// categoryOrder tests
// =============================================================================

func TestCategoryOrderCompleteness(t *testing.T) {
	t.Parallel()

	commands := buildCommandRegistry()
	usedCategories := make(map[string]bool)
	for _, cmd := range commands {
		usedCategories[cmd.Category] = true
	}

	for cat := range usedCategories {
		found := false
		for _, c := range categoryOrder {
			if c == cat {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("category %q used in commands but not in categoryOrder", cat)
		}
	}
}
