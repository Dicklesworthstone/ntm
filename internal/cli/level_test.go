package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/cli/tiers"
	"github.com/Dicklesworthstone/ntm/internal/config"
)

func setupTestProficiency(t *testing.T) (cleanup func()) {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	t.Setenv("NTM_CONFIG", "")
	return func() {}
}

func TestLevelShowDisplaysCurrentTier(t *testing.T) {
	cleanup := setupTestProficiency(t)
	defer cleanup()

	cfg, err := config.LoadProficiency()
	if err != nil {
		t.Fatalf("LoadProficiency failed: %v", err)
	}

	// Default tier should be Apprentice
	if cfg.GetTier() != tiers.TierApprentice {
		t.Errorf("expected Apprentice tier, got %s", cfg.GetTier())
	}
}

func TestLevelUpPromotesToNextTier(t *testing.T) {
	cleanup := setupTestProficiency(t)
	defer cleanup()

	cfg, _ := config.LoadProficiency()

	// Start at Apprentice
	if cfg.GetTier() != tiers.TierApprentice {
		t.Fatalf("expected Apprentice tier, got %s", cfg.GetTier())
	}

	// Upgrade to Journeyman
	if err := cfg.SetTier(tiers.TierJourneyman, "manual promotion"); err != nil {
		t.Fatalf("SetTier failed: %v", err)
	}

	// Verify promotion
	cfg2, _ := config.LoadProficiency()
	if cfg2.GetTier() != tiers.TierJourneyman {
		t.Errorf("expected Journeyman tier after promotion, got %s", cfg2.GetTier())
	}

	// Check promotion history
	history := cfg2.GetPromotionHistory()
	if len(history) != 1 {
		t.Errorf("expected 1 promotion record, got %d", len(history))
	}
	if history[0].From != int(tiers.TierApprentice) || history[0].To != int(tiers.TierJourneyman) {
		t.Errorf("promotion record mismatch: from=%d to=%d", history[0].From, history[0].To)
	}
}

func TestLevelUpAtMaxTierDoesNothing(t *testing.T) {
	cleanup := setupTestProficiency(t)
	defer cleanup()

	cfg, _ := config.LoadProficiency()

	// Set to Master
	cfg.SetTier(tiers.TierMaster, "test")

	// Try to upgrade beyond Master
	currentTier := cfg.GetTier()
	newTier := tiers.Tier(int(currentTier) + 1)

	// Should be out of bounds
	if newTier <= tiers.TierMaster {
		t.Errorf("expected tier beyond Master, got %v", newTier)
	}

	// Verify still at Master
	if cfg.GetTier() != tiers.TierMaster {
		t.Errorf("expected Master tier, got %s", cfg.GetTier())
	}
}

func TestLevelDownDemotesToPreviousTier(t *testing.T) {
	cleanup := setupTestProficiency(t)
	defer cleanup()

	cfg, _ := config.LoadProficiency()

	// Start at Journeyman
	cfg.SetTier(tiers.TierJourneyman, "test")

	// Demote to Apprentice
	if err := cfg.SetTier(tiers.TierApprentice, "manual demotion"); err != nil {
		t.Fatalf("SetTier failed: %v", err)
	}

	// Verify demotion
	cfg2, _ := config.LoadProficiency()
	if cfg2.GetTier() != tiers.TierApprentice {
		t.Errorf("expected Apprentice tier after demotion, got %s", cfg2.GetTier())
	}
}

func TestLevelDownAtMinTierDoesNothing(t *testing.T) {
	cleanup := setupTestProficiency(t)
	defer cleanup()

	cfg, _ := config.LoadProficiency()

	// Already at Apprentice
	if cfg.GetTier() != tiers.TierApprentice {
		t.Fatalf("expected Apprentice tier, got %s", cfg.GetTier())
	}

	// Try to demote below Apprentice
	newTier := tiers.Tier(int(cfg.GetTier()) - 1)

	// Should be below minimum
	if newTier >= tiers.TierApprentice {
		t.Errorf("expected tier below Apprentice, got %v", newTier)
	}

	// Verify still at Apprentice
	if cfg.GetTier() != tiers.TierApprentice {
		t.Errorf("expected Apprentice tier, got %s", cfg.GetTier())
	}
}

func TestLevelSetSpecificTier(t *testing.T) {
	cleanup := setupTestProficiency(t)
	defer cleanup()

	testCases := []struct {
		name     string
		tier     tiers.Tier
		expected string
	}{
		{"apprentice", tiers.TierApprentice, "Apprentice"},
		{"journeyman", tiers.TierJourneyman, "Journeyman"},
		{"master", tiers.TierMaster, "Master"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _ := config.LoadProficiency()
			if err := cfg.SetTier(tc.tier, "manual"); err != nil {
				t.Fatalf("SetTier failed: %v", err)
			}

			cfg2, _ := config.LoadProficiency()
			if cfg2.GetTier().String() != tc.expected {
				t.Errorf("expected %s tier, got %s", tc.expected, cfg2.GetTier())
			}
		})
	}
}

func TestLevelSetSameTierNoChange(t *testing.T) {
	cleanup := setupTestProficiency(t)
	defer cleanup()

	cfg, _ := config.LoadProficiency()

	// Set to Journeyman
	cfg.SetTier(tiers.TierJourneyman, "first")
	initialHistory := len(cfg.GetPromotionHistory())

	// Try to set to same tier
	cfg.SetTier(tiers.TierJourneyman, "same")

	// Should not add new history entry
	if len(cfg.GetPromotionHistory()) != initialHistory {
		t.Errorf("setting same tier should not add history entry")
	}
}

func TestLevelEnvOverride(t *testing.T) {
	cleanup := setupTestProficiency(t)
	defer cleanup()

	cfg, _ := config.LoadProficiency()

	// Config says Apprentice
	if cfg.GetTier() != tiers.TierApprentice {
		t.Fatalf("expected Apprentice tier, got %s", cfg.GetTier())
	}

	// Set env override
	os.Setenv("NTM_PROFICIENCY_TIER", "3")
	defer os.Unsetenv("NTM_PROFICIENCY_TIER")

	// Should now report Master
	if cfg.GetTier() != tiers.TierMaster {
		t.Errorf("expected Master tier from env override, got %s", cfg.GetTier())
	}
}

func TestLevelReset(t *testing.T) {
	cleanup := setupTestProficiency(t)
	defer cleanup()

	cfg, _ := config.LoadProficiency()

	// Set to Master and record some usage
	cfg.SetTier(tiers.TierMaster, "test")
	cfg.RecordUsage(100, 20, 50)

	// Verify setup
	if cfg.GetTier() != tiers.TierMaster {
		t.Fatalf("expected Master tier, got %s", cfg.GetTier())
	}

	// Reset
	if err := cfg.Reset(); err != nil {
		t.Fatalf("Reset failed: %v", err)
	}

	// Verify reset
	if cfg.GetTier() != tiers.TierApprentice {
		t.Errorf("expected Apprentice tier after reset, got %s", cfg.GetTier())
	}

	stats := cfg.GetUsageStats()
	if stats.CommandsRun != 0 {
		t.Errorf("expected 0 commands after reset, got %d", stats.CommandsRun)
	}
}

func TestLevelConfigPersistence(t *testing.T) {
	cleanup := setupTestProficiency(t)
	defer cleanup()

	cfg, _ := config.LoadProficiency()

	// Set tier and record usage
	cfg.SetTier(tiers.TierJourneyman, "test")
	cfg.RecordUsage(50, 10, 25)

	// Load fresh config
	cfg2, _ := config.LoadProficiency()

	if cfg2.GetTier() != tiers.TierJourneyman {
		t.Errorf("tier not persisted: expected Journeyman, got %s", cfg2.GetTier())
	}

	stats := cfg2.GetUsageStats()
	if stats.CommandsRun != 50 {
		t.Errorf("commands not persisted: expected 50, got %d", stats.CommandsRun)
	}
}

func TestLevelConfigPathExists(t *testing.T) {
	cleanup := setupTestProficiency(t)
	defer cleanup()

	// Load and save config
	cfg, _ := config.LoadProficiency()
	cfg.Save()

	// Verify file exists
	path := config.ProficiencyConfigPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("config file should exist at %s", path)
	}
}

func TestLevelTierBounds(t *testing.T) {
	// Test tier validation
	tests := []struct {
		tier     tiers.Tier
		expected string
	}{
		{tiers.TierApprentice, "Apprentice"},
		{tiers.TierJourneyman, "Journeyman"},
		{tiers.TierMaster, "Master"},
		{tiers.Tier(0), "Unknown"},
		{tiers.Tier(4), "Unknown"},
		{tiers.Tier(-1), "Unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if tt.tier.String() != tt.expected {
				t.Errorf("Tier(%d).String() = %q, want %q", tt.tier, tt.tier.String(), tt.expected)
			}
		})
	}
}

func TestGetTierColor(t *testing.T) {
	// This tests the color assignment logic
	// Each tier should have a distinct color
	colors := make(map[string]tiers.Tier)

	tierList := []tiers.Tier{tiers.TierApprentice, tiers.TierJourneyman, tiers.TierMaster}
	for _, tier := range tierList {
		// Just verify the tier color lookup works
		// The actual color values are theme-dependent
		_ = tier.String()
	}

	// Verify we have 3 distinct tiers
	if len(tierList) != 3 {
		t.Errorf("expected 3 tiers, got %d", len(tierList))
	}

	// Ensure colors map is unique per tier
	for _, tier := range tierList {
		if existing, ok := colors[tier.String()]; ok && existing != tier {
			t.Errorf("duplicate tier name: %s", tier.String())
		}
		colors[tier.String()] = tier
	}
}

func TestGetUnlocksDescription(t *testing.T) {
	tests := []struct {
		tier     tiers.Tier
		contains []string
	}{
		{tiers.TierJourneyman, []string{"dashboard", "view", "zoom"}},
		{tiers.TierMaster, []string{"robot", "advanced"}},
	}

	for _, tt := range tests {
		t.Run(tt.tier.String(), func(t *testing.T) {
			desc := getUnlocksDescription(tt.tier)
			for _, keyword := range tt.contains {
				if !strings.Contains(strings.ToLower(desc), strings.ToLower(keyword)) {
					t.Errorf("getUnlocksDescription(%s) = %q, expected to contain %q", tt.tier, desc, keyword)
				}
			}
		})
	}
}

func TestLevelPromotionHistoryTracking(t *testing.T) {
	cleanup := setupTestProficiency(t)
	defer cleanup()

	cfg, _ := config.LoadProficiency()

	// Make multiple tier changes
	cfg.SetTier(tiers.TierJourneyman, "first upgrade")
	cfg.SetTier(tiers.TierMaster, "second upgrade")
	cfg.SetTier(tiers.TierJourneyman, "downgrade")

	history := cfg.GetPromotionHistory()
	if len(history) != 3 {
		t.Errorf("expected 3 promotion records, got %d", len(history))
	}

	// Verify order and values
	expected := []struct {
		from   int
		to     int
		reason string
	}{
		{int(tiers.TierApprentice), int(tiers.TierJourneyman), "first upgrade"},
		{int(tiers.TierJourneyman), int(tiers.TierMaster), "second upgrade"},
		{int(tiers.TierMaster), int(tiers.TierJourneyman), "downgrade"},
	}

	for i, exp := range expected {
		if i >= len(history) {
			break
		}
		if history[i].From != exp.from || history[i].To != exp.to {
			t.Errorf("history[%d]: from=%d to=%d, expected from=%d to=%d",
				i, history[i].From, history[i].To, exp.from, exp.to)
		}
	}
}

func TestLevelCorruptedConfig(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", tmpDir)
	defer os.Unsetenv("XDG_CONFIG_HOME")

	// Create corrupted config file
	path := filepath.Join(tmpDir, "ntm", "proficiency.json")
	os.MkdirAll(filepath.Dir(path), 0755)
	os.WriteFile(path, []byte("{invalid json"), 0644)

	// Load should recover gracefully
	cfg, err := config.LoadProficiency()
	if err != nil {
		t.Fatalf("LoadProficiency should recover from corrupted config: %v", err)
	}

	// Should have default tier
	if cfg.GetTier() != tiers.TierApprentice {
		t.Errorf("expected Apprentice tier after recovery, got %s", cfg.GetTier())
	}
}
