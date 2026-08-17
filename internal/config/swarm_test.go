package config

import (
	"testing"

	"github.com/BurntSushi/toml"
)

func TestDefaultSwarmConfig(t *testing.T) {
	cfg := DefaultSwarmConfig()

	// Check disabled by default
	if cfg.Enabled {
		t.Error("swarm should be disabled by default")
	}

	// Check default scan dir
	if cfg.DefaultScanDir != "/dp" {
		t.Errorf("expected default_scan_dir /dp, got %s", cfg.DefaultScanDir)
	}

	// Check tier thresholds
	if cfg.Tier1Threshold != 400 {
		t.Errorf("expected tier1_threshold 400, got %d", cfg.Tier1Threshold)
	}
	if cfg.Tier2Threshold != 100 {
		t.Errorf("expected tier2_threshold 100, got %d", cfg.Tier2Threshold)
	}
	if cfg.Tier1Threshold <= cfg.Tier2Threshold {
		t.Errorf("tier1_threshold (%d) should be > tier2_threshold (%d)",
			cfg.Tier1Threshold, cfg.Tier2Threshold)
	}

	// Check allocations
	if cfg.Tier1Allocation.CC != 4 {
		t.Errorf("expected tier1_allocation.cc=4, got %d", cfg.Tier1Allocation.CC)
	}
	if cfg.Tier1Allocation.Cod != 4 {
		t.Errorf("expected tier1_allocation.cod=4, got %d", cfg.Tier1Allocation.Cod)
	}
	if cfg.Tier1Allocation.Gmi != 2 {
		t.Errorf("expected tier1_allocation.gmi=2, got %d", cfg.Tier1Allocation.Gmi)
	}

	// Check sessions per type
	if cfg.SessionsPerType != 3 {
		t.Errorf("expected sessions_per_type 3, got %d", cfg.SessionsPerType)
	}

	// Check stagger delay
	if cfg.StaggerDelayMs != 300 {
		t.Errorf("expected stagger_delay_ms 300, got %d", cfg.StaggerDelayMs)
	}

}

func TestAllocationSpecTotal(t *testing.T) {
	tests := []struct {
		name     string
		spec     AllocationSpec
		expected int
	}{
		{"zero", AllocationSpec{}, 0},
		{"cc only", AllocationSpec{CC: 3}, 3},
		{"all types", AllocationSpec{CC: 4, Cod: 4, Gmi: 2}, 10},
		{"cod and gmi", AllocationSpec{Cod: 2, Gmi: 3}, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.spec.Total()
			if got != tt.expected {
				t.Errorf("Total() = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestValidateSwarmConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     SwarmConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "disabled config passes",
			cfg:     SwarmConfig{Enabled: false},
			wantErr: false,
		},
		{
			name:    "valid enabled config",
			cfg:     validSwarmConfig(),
			wantErr: false,
		},
		{
			name: "tier1 threshold not positive",
			cfg: func() SwarmConfig {
				c := validSwarmConfig()
				c.Tier1Threshold = 0
				return c
			}(),
			wantErr: true,
			errMsg:  "tier1_threshold must be positive",
		},
		{
			name: "tier2 threshold not positive",
			cfg: func() SwarmConfig {
				c := validSwarmConfig()
				c.Tier2Threshold = 0
				return c
			}(),
			wantErr: true,
			errMsg:  "tier2_threshold must be positive",
		},
		{
			name: "tier1 not greater than tier2",
			cfg: func() SwarmConfig {
				c := validSwarmConfig()
				c.Tier1Threshold = 100
				c.Tier2Threshold = 100
				return c
			}(),
			wantErr: true,
			errMsg:  "tier1_threshold (100) must be greater than tier2_threshold (100)",
		},
		{
			name: "tier1 less than tier2",
			cfg: func() SwarmConfig {
				c := validSwarmConfig()
				c.Tier1Threshold = 50
				c.Tier2Threshold = 100
				return c
			}(),
			wantErr: true,
			errMsg:  "must be greater than",
		},
		{
			name: "tier1 allocation empty",
			cfg: func() SwarmConfig {
				c := validSwarmConfig()
				c.Tier1Allocation = AllocationSpec{}
				return c
			}(),
			wantErr: true,
			errMsg:  "tier1_allocation must have at least one agent",
		},
		{
			name: "tier2 allocation empty",
			cfg: func() SwarmConfig {
				c := validSwarmConfig()
				c.Tier2Allocation = AllocationSpec{}
				return c
			}(),
			wantErr: true,
			errMsg:  "tier2_allocation must have at least one agent",
		},
		{
			name: "tier3 allocation empty",
			cfg: func() SwarmConfig {
				c := validSwarmConfig()
				c.Tier3Allocation = AllocationSpec{}
				return c
			}(),
			wantErr: true,
			errMsg:  "tier3_allocation must have at least one agent",
		},
		{
			name: "negative cc in allocation",
			cfg: func() SwarmConfig {
				c := validSwarmConfig()
				c.Tier1Allocation.CC = -1
				return c
			}(),
			wantErr: true,
			errMsg:  "tier1_allocation.cc must be non-negative",
		},
		{
			name: "sessions per type zero",
			cfg: func() SwarmConfig {
				c := validSwarmConfig()
				c.SessionsPerType = 0
				return c
			}(),
			wantErr: true,
			errMsg:  "sessions_per_type must be at least 1",
		},
		{
			name: "negative stagger delay",
			cfg: func() SwarmConfig {
				c := validSwarmConfig()
				c.StaggerDelayMs = -1
				return c
			}(),
			wantErr: true,
			errMsg:  "stagger_delay_ms must be non-negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSwarmConfig(&tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				} else if tt.errMsg != "" && err.Error() != tt.errMsg {
					// Check if error contains expected message
					if !containsString(err.Error(), tt.errMsg) {
						t.Errorf("error %q does not contain %q", err.Error(), tt.errMsg)
					}
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestGetAllocationForBeadCount(t *testing.T) {
	cfg := validSwarmConfig()

	tests := []struct {
		beadCount int
		expected  AllocationSpec
	}{
		{500, cfg.Tier1Allocation}, // >= 400 (tier1)
		{400, cfg.Tier1Allocation}, // == 400 (tier1 boundary)
		{399, cfg.Tier2Allocation}, // < 400, >= 100 (tier2)
		{100, cfg.Tier2Allocation}, // == 100 (tier2 boundary)
		{99, cfg.Tier3Allocation},  // < 100 (tier3)
		{0, cfg.Tier3Allocation},   // 0 (tier3)
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			got := cfg.GetAllocationForBeadCount(tt.beadCount)
			if got != tt.expected {
				t.Errorf("GetAllocationForBeadCount(%d) = %+v, want %+v",
					tt.beadCount, got, tt.expected)
			}
		})
	}
}

func TestGetTierName(t *testing.T) {
	cfg := validSwarmConfig()

	tests := []struct {
		beadCount int
		expected  string
	}{
		{500, "tier1"},
		{400, "tier1"},
		{399, "tier2"},
		{100, "tier2"},
		{99, "tier3"},
		{0, "tier3"},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			got := cfg.GetTierName(tt.beadCount)
			if got != tt.expected {
				t.Errorf("GetTierName(%d) = %s, want %s", tt.beadCount, got, tt.expected)
			}
		})
	}
}

func TestSwarmConfigTOMLUnmarshal(t *testing.T) {
	input := `
enabled = true
default_scan_dir = "/data/projects"
tier1_threshold = 500
tier2_threshold = 200
sessions_per_type = 5
stagger_delay_ms = 500
auto_rotate_accounts = true

[tier1_allocation]
cc = 6
cod = 6
gmi = 3

[tier2_allocation]
cc = 4
cod = 4
gmi = 2

[tier3_allocation]
cc = 2
cod = 2
gmi = 1

`

	var cfg SwarmConfig
	if _, err := toml.Decode(input, &cfg); err != nil {
		t.Fatalf("failed to decode TOML: %v", err)
	}

	// Verify parsed values
	if !cfg.Enabled {
		t.Error("expected enabled=true")
	}
	if cfg.DefaultScanDir != "/data/projects" {
		t.Errorf("expected default_scan_dir=/data/projects, got %s", cfg.DefaultScanDir)
	}
	if cfg.Tier1Threshold != 500 {
		t.Errorf("expected tier1_threshold=500, got %d", cfg.Tier1Threshold)
	}
	if cfg.Tier2Threshold != 200 {
		t.Errorf("expected tier2_threshold=200, got %d", cfg.Tier2Threshold)
	}
	if cfg.Tier1Allocation.CC != 6 {
		t.Errorf("expected tier1_allocation.cc=6, got %d", cfg.Tier1Allocation.CC)
	}
	if cfg.SessionsPerType != 5 {
		t.Errorf("expected sessions_per_type=5, got %d", cfg.SessionsPerType)
	}
	if !cfg.AutoRotateAccounts {
		t.Error("expected auto_rotate_accounts=true")
	}
}

// validSwarmConfig returns a valid enabled SwarmConfig for testing
func validSwarmConfig() SwarmConfig {
	cfg := DefaultSwarmConfig()
	cfg.Enabled = true
	return cfg
}

// containsString checks if s contains substr
func containsString(s, substr string) bool {
	return len(substr) > 0 && len(s) >= len(substr) && (s == substr || contains(s, substr))
}

// =============================================================================
// validateAllocationSpec — all negative branches (bd-4b4zf)
// =============================================================================

func TestValidateAllocationSpec_AllBranches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		spec    AllocationSpec
		wantErr bool
	}{
		{"all zero valid", AllocationSpec{}, false},
		{"all positive valid", AllocationSpec{CC: 2, Cod: 3, Gmi: 1}, false},
		{"negative CC", AllocationSpec{CC: -1, Cod: 0, Gmi: 0}, true},
		{"negative Cod", AllocationSpec{CC: 0, Cod: -1, Gmi: 0}, true},
		{"negative Gmi", AllocationSpec{CC: 0, Cod: 0, Gmi: -1}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateAllocationSpec("test", tc.spec)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateAllocationSpec() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
