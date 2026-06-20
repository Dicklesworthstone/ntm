package config

import "fmt"

// SwarmConfig configures the weighted multi-project agent swarm system.
// This enables automatic agent allocation across multiple projects based on
// bead counts and configurable tier thresholds.
type SwarmConfig struct {
	// Enabled controls whether swarm orchestration is active
	Enabled bool `toml:"enabled"`

	// DefaultScanDir is the base directory to scan for projects (e.g., "/dp")
	DefaultScanDir string `toml:"default_scan_dir"`

	// Tier thresholds for bead-based allocation
	// Projects with beads >= Tier1Threshold get Tier1Allocation
	// Projects with beads >= Tier2Threshold get Tier2Allocation
	// Projects with beads < Tier2Threshold get Tier3Allocation
	Tier1Threshold int `toml:"tier1_threshold"` // Default: 400
	Tier2Threshold int `toml:"tier2_threshold"` // Default: 100

	// Allocation per tier: {cc, cod, gmi}
	Tier1Allocation AllocationSpec `toml:"tier1_allocation"`
	Tier2Allocation AllocationSpec `toml:"tier2_allocation"`
	Tier3Allocation AllocationSpec `toml:"tier3_allocation"`

	// Session structure
	SessionsPerType int `toml:"sessions_per_type"` // Default: 3

	// PanesPerSession overrides auto-calculated panes per session.
	// 0 means auto-calculate: ceil(maxAgentsOfAnyType / sessionsPerType)
	// Values > 0 are used as manual override.
	PanesPerSession int `toml:"panes_per_session"` // Default: 0 (auto)

	// Timing
	StaggerDelayMs int `toml:"stagger_delay_ms"` // Default: 300

	// Account rotation
	AutoRotateAccounts bool `toml:"auto_rotate_accounts"`

	// ForceGlobalAuthClobber permits automatic *global* ~/.codex/auth.json
	// rotation even when live Codex panes share global auth or caam lacks the
	// safe-restore capability, and bypasses account pins. DANGEROUS escape hatch
	// for #194; off by default. Maps to the --force-global-auth-clobber flag.
	ForceGlobalAuthClobber bool `toml:"force_global_auth_clobber"`

	// Limit detection patterns per agent type
	LimitPatterns map[string][]string `toml:"limit_patterns"`

	// Marching orders templates
	MarchingOrders MarchingOrdersConfig `toml:"marching_orders"`
}

// AllocationSpec defines agent counts per type for a tier.
type AllocationSpec struct {
	CC  int `toml:"cc"`  // Claude Code agents
	Cod int `toml:"cod"` // Codex agents
	Gmi int `toml:"gmi"` // Gemini agents
}

// Total returns the total number of agents in this allocation.
func (a AllocationSpec) Total() int {
	return a.CC + a.Cod + a.Gmi
}

// MarchingOrdersConfig holds templates for initial agent instructions.
type MarchingOrdersConfig struct {
	Default string `toml:"default"` // Default marching orders template
	Review  string `toml:"review"`  // Review-focused marching orders
}

// DefaultSwarmConfig returns SwarmConfig with sensible defaults.
func DefaultSwarmConfig() SwarmConfig {
	return SwarmConfig{
		Enabled:        false, // Disabled by default, opt-in feature
		DefaultScanDir: "/dp",
		Tier1Threshold: 400,
		Tier2Threshold: 100,
		Tier1Allocation: AllocationSpec{
			CC:  4,
			Cod: 4,
			Gmi: 2,
		},
		Tier2Allocation: AllocationSpec{
			CC:  3,
			Cod: 3,
			Gmi: 2,
		},
		Tier3Allocation: AllocationSpec{
			CC:  1,
			Cod: 1,
			Gmi: 1,
		},
		SessionsPerType:    3,
		StaggerDelayMs:     300,
		AutoRotateAccounts: false,
		LimitPatterns: map[string][]string{
			"cc":  {"You've hit your limit", "usage limit", "rate limit"},
			"cod": {"You've hit your usage limit", "rate limit exceeded"},
			"gmi": {"quota exceeded", "rate limit"},
		},
		MarchingOrders: MarchingOrdersConfig{
			Default: "",
			Review:  "",
		},
	}
}

// ValidateSwarmConfig validates the swarm configuration.
func ValidateSwarmConfig(cfg *SwarmConfig) error {
	if !cfg.Enabled {
		// Skip validation if swarm is disabled
		return nil
	}

	// Validate tier thresholds
	if cfg.Tier1Threshold <= 0 {
		return fmt.Errorf("tier1_threshold must be positive, got %d", cfg.Tier1Threshold)
	}
	if cfg.Tier2Threshold <= 0 {
		return fmt.Errorf("tier2_threshold must be positive, got %d", cfg.Tier2Threshold)
	}
	if cfg.Tier1Threshold <= cfg.Tier2Threshold {
		return fmt.Errorf("tier1_threshold (%d) must be greater than tier2_threshold (%d)",
			cfg.Tier1Threshold, cfg.Tier2Threshold)
	}

	// Validate allocations have at least one agent
	if cfg.Tier1Allocation.Total() == 0 {
		return fmt.Errorf("tier1_allocation must have at least one agent")
	}
	if cfg.Tier2Allocation.Total() == 0 {
		return fmt.Errorf("tier2_allocation must have at least one agent")
	}
	if cfg.Tier3Allocation.Total() == 0 {
		return fmt.Errorf("tier3_allocation must have at least one agent")
	}

	// Validate non-negative agent counts
	if err := validateAllocationSpec("tier1_allocation", cfg.Tier1Allocation); err != nil {
		return err
	}
	if err := validateAllocationSpec("tier2_allocation", cfg.Tier2Allocation); err != nil {
		return err
	}
	if err := validateAllocationSpec("tier3_allocation", cfg.Tier3Allocation); err != nil {
		return err
	}

	// Validate sessions per type
	if cfg.SessionsPerType < 1 {
		return fmt.Errorf("sessions_per_type must be at least 1, got %d", cfg.SessionsPerType)
	}

	// Validate stagger delay
	if cfg.StaggerDelayMs < 0 {
		return fmt.Errorf("stagger_delay_ms must be non-negative, got %d", cfg.StaggerDelayMs)
	}

	return nil
}

// validateAllocationSpec validates an individual allocation spec.
func validateAllocationSpec(name string, spec AllocationSpec) error {
	if spec.CC < 0 {
		return fmt.Errorf("%s.cc must be non-negative, got %d", name, spec.CC)
	}
	if spec.Cod < 0 {
		return fmt.Errorf("%s.cod must be non-negative, got %d", name, spec.Cod)
	}
	if spec.Gmi < 0 {
		return fmt.Errorf("%s.gmi must be non-negative, got %d", name, spec.Gmi)
	}
	return nil
}

// GetAllocationForBeadCount returns the appropriate allocation spec based on bead count.
func (c *SwarmConfig) GetAllocationForBeadCount(beadCount int) AllocationSpec {
	if beadCount >= c.Tier1Threshold {
		return c.Tier1Allocation
	}
	if beadCount >= c.Tier2Threshold {
		return c.Tier2Allocation
	}
	return c.Tier3Allocation
}

// GetTierName returns the tier name for a given bead count.
func (c *SwarmConfig) GetTierName(beadCount int) string {
	if beadCount >= c.Tier1Threshold {
		return "tier1"
	}
	if beadCount >= c.Tier2Threshold {
		return "tier2"
	}
	return "tier3"
}
