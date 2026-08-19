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
