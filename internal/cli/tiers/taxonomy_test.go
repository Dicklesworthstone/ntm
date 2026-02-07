package tiers

import (
	"testing"
)

func TestTierString(t *testing.T) {
	tests := []struct {
		tier     Tier
		expected string
	}{
		{TierApprentice, "Apprentice"},
		{TierJourneyman, "Journeyman"},
		{TierMaster, "Master"},
		{Tier(99), "Unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.tier.String(); got != tt.expected {
				t.Errorf("Tier(%d).String() = %q, want %q", tt.tier, got, tt.expected)
			}
		})
	}
}

func TestTierDescription(t *testing.T) {
	if desc := TierApprentice.Description(); desc == "" {
		t.Error("TierApprentice.Description() should not be empty")
	}
	if desc := TierJourneyman.Description(); desc == "" {
		t.Error("TierJourneyman.Description() should not be empty")
	}
	if desc := TierMaster.Description(); desc == "" {
		t.Error("TierMaster.Description() should not be empty")
	}
}

func TestRegistryContainsEssentialCommands(t *testing.T) {
	essentialCommands := []string{"spawn", "send", "status", "kill", "version"}

	for _, cmd := range essentialCommands {
		info, ok := Registry[cmd]
		if !ok {
			t.Errorf("Registry missing essential command %q", cmd)
			continue
		}
		if info.Tier != TierApprentice {
			t.Errorf("Essential command %q should be TierApprentice, got %v", cmd, info.Tier)
		}
	}
}

func TestRegistryAllCommandsHaveRequiredFields(t *testing.T) {
	for name, info := range Registry {
		if info.Name == "" {
			t.Errorf("Command %q has empty Name field", name)
		}
		if info.Name != name {
			t.Errorf("Command %q has mismatched Name field: %q", name, info.Name)
		}
		if info.Tier < TierApprentice || info.Tier > TierMaster {
			t.Errorf("Command %q has invalid Tier: %d", name, info.Tier)
		}
		if info.Category == "" {
			t.Errorf("Command %q has empty Category", name)
		}
		if info.Description == "" {
			t.Errorf("Command %q has empty Description", name)
		}
	}
}

func TestGetByTier(t *testing.T) {
	apprentice := GetByTier(TierApprentice)
	journeyman := GetByTier(TierJourneyman)
	master := GetByTier(TierMaster)

	// Each higher tier should include all commands from lower tiers
	if len(apprentice) > len(journeyman) {
		t.Error("Journeyman tier should have at least as many commands as Apprentice")
	}
	if len(journeyman) > len(master) {
		t.Error("Master tier should have at least as many commands as Journeyman")
	}

	// Apprentice should have essential commands
	if len(apprentice) < 4 {
		t.Errorf("Expected at least 4 Apprentice commands, got %d", len(apprentice))
	}

	// Master should have all commands
	if len(master) != len(Registry) {
		t.Errorf("Master tier should have all %d commands, got %d", len(Registry), len(master))
	}
}

func TestGetByCategory(t *testing.T) {
	sessionCreation := GetByCategory(CategorySessionCreation)
	if len(sessionCreation) == 0 {
		t.Error("Expected at least one command in SessionCreation category")
	}

	// Verify spawn is in session creation
	found := false
	for _, cmd := range sessionCreation {
		if cmd.Name == "spawn" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected 'spawn' in SessionCreation category")
	}
}

func TestGetApprenticeCommands(t *testing.T) {
	commands := GetApprenticeCommands()

	// All returned commands should be TierApprentice
	for _, cmd := range commands {
		if cmd.Tier != TierApprentice {
			t.Errorf("GetApprenticeCommands() returned %q with Tier %v", cmd.Name, cmd.Tier)
		}
	}
}

func TestGetJourneymanCommands(t *testing.T) {
	commands := GetJourneymanCommands()

	// Should include both Apprentice and Journeyman
	hasApprentice := false
	hasJourneyman := false
	for _, cmd := range commands {
		if cmd.Tier == TierApprentice {
			hasApprentice = true
		}
		if cmd.Tier == TierJourneyman {
			hasJourneyman = true
		}
		if cmd.Tier > TierJourneyman {
			t.Errorf("GetJourneymanCommands() returned %q with Tier %v", cmd.Name, cmd.Tier)
		}
	}

	if !hasApprentice {
		t.Error("GetJourneymanCommands() should include Apprentice commands")
	}
	if !hasJourneyman {
		t.Error("GetJourneymanCommands() should include Journeyman commands")
	}
}

func TestGetTier(t *testing.T) {
	tests := []struct {
		command  string
		expected Tier
	}{
		{"spawn", TierApprentice},
		{"send", TierApprentice},
		{"dashboard", TierJourneyman},
		{"attach", TierJourneyman},
		{"assign", TierMaster},
		{"policy", TierMaster},
		{"unknown-command", TierMaster}, // Unknown defaults to Master
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			if got := GetTier(tt.command); got != tt.expected {
				t.Errorf("GetTier(%q) = %v, want %v", tt.command, got, tt.expected)
			}
		})
	}
}

func TestIsEssential(t *testing.T) {
	if !IsEssential("spawn") {
		t.Error("spawn should be essential")
	}
	if !IsEssential("send") {
		t.Error("send should be essential")
	}
	if IsEssential("dashboard") {
		t.Error("dashboard should not be essential")
	}
	if IsEssential("assign") {
		t.Error("assign should not be essential")
	}
}

func TestAllCategories(t *testing.T) {
	categories := AllCategories()

	if len(categories) < 5 {
		t.Errorf("Expected at least 5 categories, got %d", len(categories))
	}

	// Verify expected categories exist
	expectedCategories := []string{
		CategorySessionCreation,
		CategoryAgentManagement,
		CategorySessionNav,
		CategoryUtilities,
	}

	for _, expected := range expectedCategories {
		found := false
		for _, cat := range categories {
			if cat == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("AllCategories() missing expected category %q", expected)
		}
	}
}

func TestCountByTier(t *testing.T) {
	counts := CountByTier()

	// Should have counts for all three tiers
	if counts[TierApprentice] < 4 {
		t.Errorf("Expected at least 4 Apprentice commands, got %d", counts[TierApprentice])
	}
	if counts[TierJourneyman] < 10 {
		t.Errorf("Expected at least 10 Journeyman commands, got %d", counts[TierJourneyman])
	}
	if counts[TierMaster] < 20 {
		t.Errorf("Expected at least 20 Master commands, got %d", counts[TierMaster])
	}

	// Total should match registry
	total := counts[TierApprentice] + counts[TierJourneyman] + counts[TierMaster]
	if total != len(Registry) {
		t.Errorf("CountByTier() total %d doesn't match Registry size %d", total, len(Registry))
	}
}

func TestTierProgression(t *testing.T) {
	// Verify tier values are in order
	if TierApprentice >= TierJourneyman {
		t.Error("TierApprentice should be less than TierJourneyman")
	}
	if TierJourneyman >= TierMaster {
		t.Error("TierJourneyman should be less than TierMaster")
	}
}

func TestRegistryHasExamples(t *testing.T) {
	// At least essential commands should have examples
	essentialCommands := []string{"spawn", "send", "status", "kill"}

	for _, cmd := range essentialCommands {
		info, ok := Registry[cmd]
		if !ok {
			continue
		}
		if len(info.Examples) == 0 {
			t.Errorf("Essential command %q should have at least one example", cmd)
		}
	}
}

func TestRegistryAliasesAreConsistent(t *testing.T) {
	// Verify known aliases
	aliasTests := []struct {
		command string
		alias   string
	}{
		{"spawn", "sat"},
		{"send", "bp"},
		{"status", "snt"},
		{"kill", "knt"},
		{"attach", "rnt"},
		{"list", "lnt"},
		{"view", "vnt"},
		{"zoom", "znt"},
		{"copy", "cpnt"},
		{"save", "svnt"},
		{"palette", "ncp"},
		{"deps", "cad"},
	}

	for _, tt := range aliasTests {
		info, ok := Registry[tt.command]
		if !ok {
			t.Errorf("Registry missing command %q", tt.command)
			continue
		}
		if info.Alias != tt.alias {
			t.Errorf("Command %q has alias %q, expected %q", tt.command, info.Alias, tt.alias)
		}
	}
}

func TestGetMasterCommands(t *testing.T) {
	t.Parallel()
	cmds := GetMasterCommands()
	if len(cmds) == 0 {
		t.Fatal("GetMasterCommands returned empty list")
	}
	// Master commands should be a superset of all tiers
	apprenticeCmds := GetByTier(TierApprentice)
	if len(cmds) < len(apprenticeCmds) {
		t.Errorf("master commands (%d) should include all apprentice commands (%d)", len(cmds), len(apprenticeCmds))
	}
}
