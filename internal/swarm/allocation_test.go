package swarm

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

func testSwarmConfig() *config.SwarmConfig {
	cfg := config.DefaultSwarmConfig()
	cfg.Enabled = true
	return &cfg
}

func TestNewAllocationCalculator(t *testing.T) {
	cfg := testSwarmConfig()
	ac := NewAllocationCalculator(cfg)

	if ac == nil {
		t.Fatal("expected non-nil AllocationCalculator")
	}

	if ac.Config != cfg {
		t.Error("expected Config to match provided config")
	}
}

func TestAllocationCalculator_CalculateTier(t *testing.T) {
	cfg := testSwarmConfig()
	ac := NewAllocationCalculator(cfg)

	tests := []struct {
		beadCount int
		expected  int
	}{
		{500, 1}, // >= 400
		{400, 1}, // == 400 (boundary)
		{399, 2}, // < 400, >= 100
		{150, 2}, // < 400, >= 100
		{100, 2}, // == 100 (boundary)
		{99, 3},  // < 100
		{50, 3},  // < 100
		{0, 3},   // zero beads
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			got := ac.CalculateTier(tt.beadCount)
			if got != tt.expected {
				t.Errorf("CalculateTier(%d) = %d, want %d", tt.beadCount, got, tt.expected)
			}
		})
	}
}

func TestAllocationCalculator_CalculateProjectAllocation(t *testing.T) {
	cfg := testSwarmConfig()
	ac := NewAllocationCalculator(cfg)

	tests := []struct {
		name        string
		project     ProjectBeadCount
		expectedCC  int
		expectedCod int
		expectedGmi int
	}{
		{
			name:        "tier1 project",
			project:     ProjectBeadCount{Path: "/dp/proj1", Name: "proj1", OpenBeads: 500},
			expectedCC:  4,
			expectedCod: 4,
			expectedGmi: 2,
		},
		{
			name:        "tier2 project",
			project:     ProjectBeadCount{Path: "/dp/proj2", Name: "proj2", OpenBeads: 200},
			expectedCC:  3,
			expectedCod: 3,
			expectedGmi: 2,
		},
		{
			name:        "tier3 project",
			project:     ProjectBeadCount{Path: "/dp/proj3", Name: "proj3", OpenBeads: 50},
			expectedCC:  1,
			expectedCod: 1,
			expectedGmi: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ac.CalculateProjectAllocation(tt.project)

			if got.CCAgents != tt.expectedCC {
				t.Errorf("CCAgents = %d, want %d", got.CCAgents, tt.expectedCC)
			}
			if got.CodAgents != tt.expectedCod {
				t.Errorf("CodAgents = %d, want %d", got.CodAgents, tt.expectedCod)
			}
			if got.GmiAgents != tt.expectedGmi {
				t.Errorf("GmiAgents = %d, want %d", got.GmiAgents, tt.expectedGmi)
			}
			if got.TotalAgents != tt.expectedCC+tt.expectedCod+tt.expectedGmi {
				t.Errorf("TotalAgents = %d, want %d", got.TotalAgents, tt.expectedCC+tt.expectedCod+tt.expectedGmi)
			}
		})
	}
}

func TestAllocationCalculator_CalculateAllocations(t *testing.T) {
	cfg := testSwarmConfig()
	ac := NewAllocationCalculator(cfg)

	projects := []ProjectBeadCount{
		{Path: "/dp/proj1", Name: "proj1", OpenBeads: 50},  // tier3
		{Path: "/dp/proj2", Name: "proj2", OpenBeads: 500}, // tier1
		{Path: "/dp/proj3", Name: "proj3", OpenBeads: 200}, // tier2
	}

	allocations := ac.CalculateAllocations(projects)

	// Should be sorted by bead count descending
	if len(allocations) != 3 {
		t.Fatalf("expected 3 allocations, got %d", len(allocations))
	}

	// First should be the tier1 project (500 beads)
	if allocations[0].Project.OpenBeads != 500 {
		t.Errorf("expected first allocation to have 500 beads, got %d", allocations[0].Project.OpenBeads)
	}

	// Second should be tier2 (200 beads)
	if allocations[1].Project.OpenBeads != 200 {
		t.Errorf("expected second allocation to have 200 beads, got %d", allocations[1].Project.OpenBeads)
	}

	// Third should be tier3 (50 beads)
	if allocations[2].Project.OpenBeads != 50 {
		t.Errorf("expected third allocation to have 50 beads, got %d", allocations[2].Project.OpenBeads)
	}
}

func TestAllocationCalculator_CalculateAllocations_Empty(t *testing.T) {
	cfg := testSwarmConfig()
	ac := NewAllocationCalculator(cfg)

	allocations := ac.CalculateAllocations(nil)

	if len(allocations) != 0 {
		t.Errorf("expected empty allocations, got %d", len(allocations))
	}
}

func TestAllocationCalculator_CalculateTotals(t *testing.T) {
	cfg := testSwarmConfig()
	ac := NewAllocationCalculator(cfg)

	allocations := []ProjectAllocation{
		{CCAgents: 4, CodAgents: 4, GmiAgents: 2, TotalAgents: 10},
		{CCAgents: 3, CodAgents: 3, GmiAgents: 2, TotalAgents: 8},
		{CCAgents: 1, CodAgents: 1, GmiAgents: 1, TotalAgents: 3},
	}

	totals := ac.CalculateTotals(allocations)

	if totals.TotalCC != 8 {
		t.Errorf("TotalCC = %d, want 8", totals.TotalCC)
	}
	if totals.TotalCod != 8 {
		t.Errorf("TotalCod = %d, want 8", totals.TotalCod)
	}
	if totals.TotalGmi != 5 {
		t.Errorf("TotalGmi = %d, want 5", totals.TotalGmi)
	}
	if totals.TotalAgents != 21 {
		t.Errorf("TotalAgents = %d, want 21", totals.TotalAgents)
	}
}

func TestAllocationCalculator_Calculate(t *testing.T) {
	cfg := testSwarmConfig()
	ac := NewAllocationCalculator(cfg)

	projects := []ProjectBeadCount{
		{Path: "/dp/proj1", Name: "proj1", OpenBeads: 500}, // tier1: 4+4+2=10
		{Path: "/dp/proj2", Name: "proj2", OpenBeads: 150}, // tier2: 3+3+2=8
	}

	result := ac.Calculate(projects)

	if len(result.Allocations) != 2 {
		t.Fatalf("expected 2 allocations, got %d", len(result.Allocations))
	}

	// Totals should be 7 CC, 7 Cod, 4 Gmi = 18 total
	if result.Totals.TotalCC != 7 {
		t.Errorf("TotalCC = %d, want 7", result.Totals.TotalCC)
	}
	if result.Totals.TotalCod != 7 {
		t.Errorf("TotalCod = %d, want 7", result.Totals.TotalCod)
	}
	if result.Totals.TotalGmi != 4 {
		t.Errorf("TotalGmi = %d, want 4", result.Totals.TotalGmi)
	}
	if result.Totals.TotalAgents != 18 {
		t.Errorf("TotalAgents = %d, want 18", result.Totals.TotalAgents)
	}
}

func TestProjectBeadCountFromPath(t *testing.T) {
	pbc := ProjectBeadCountFromPath("/dp/my-project", 150)

	if pbc.Path != "/dp/my-project" {
		t.Errorf("Path = %q, want /dp/my-project", pbc.Path)
	}
	if pbc.Name != "my-project" {
		t.Errorf("Name = %q, want my-project", pbc.Name)
	}
	if pbc.OpenBeads != 150 {
		t.Errorf("OpenBeads = %d, want 150", pbc.OpenBeads)
	}
}

func TestAllocationCalculator_GenerateSwarmPlan(t *testing.T) {
	cfg := testSwarmConfig()
	cfg.AutoRotateAccounts = true
	ac := NewAllocationCalculator(cfg)

	projects := []ProjectBeadCount{
		{Path: "/dp/proj1", Name: "proj1", OpenBeads: 500}, // tier1: 4+4+2
		{Path: "/dp/proj2", Name: "proj2", OpenBeads: 150}, // tier2: 3+3+2
	}

	plan := ac.GenerateSwarmPlan("/dp", projects)

	if plan == nil {
		t.Fatal("expected non-nil SwarmPlan")
	}

	if plan.ScanDir != "/dp" {
		t.Errorf("ScanDir = %q, want /dp", plan.ScanDir)
	}

	if plan.TotalCC != 7 {
		t.Errorf("TotalCC = %d, want 7", plan.TotalCC)
	}

	if plan.TotalCod != 7 {
		t.Errorf("TotalCod = %d, want 7", plan.TotalCod)
	}

	if plan.TotalGmi != 4 {
		t.Errorf("TotalGmi = %d, want 4", plan.TotalGmi)
	}

	if !plan.AutoRotateAccounts {
		t.Error("AutoRotateAccounts = false, want true")
	}

	if plan.SessionsPerType != 3 {
		t.Errorf("SessionsPerType = %d, want 3", plan.SessionsPerType)
	}

	// Verify sessions were created
	if len(plan.Sessions) == 0 {
		t.Error("expected sessions to be created")
	}

	// Check for expected session names
	sessionNames := make(map[string]bool)
	for _, s := range plan.Sessions {
		sessionNames[s.Name] = true
	}

	// Should have cc_agents_*, cod_agents_*, gmi_agents_*
	hasCC := false
	hasCod := false
	hasGmi := false
	for name := range sessionNames {
		if len(name) > 10 && name[:10] == "cc_agents_" {
			hasCC = true
		}
		if len(name) > 11 && name[:11] == "cod_agents_" {
			hasCod = true
		}
		if len(name) > 11 && name[:11] == "gmi_agents_" {
			hasGmi = true
		}
	}

	if !hasCC {
		t.Error("expected cc_agents_* sessions")
	}
	if !hasCod {
		t.Error("expected cod_agents_* sessions")
	}
	if !hasGmi {
		t.Error("expected gmi_agents_* sessions")
	}
}

func TestAllocationCalculator_GenerateSwarmPlan_Empty(t *testing.T) {
	cfg := testSwarmConfig()
	ac := NewAllocationCalculator(cfg)

	plan := ac.GenerateSwarmPlan("/dp", nil)

	if plan == nil {
		t.Fatal("expected non-nil SwarmPlan even for empty projects")
	}

	if plan.TotalAgents != 0 {
		t.Errorf("TotalAgents = %d, want 0", plan.TotalAgents)
	}

	if len(plan.Sessions) != 0 {
		t.Errorf("expected no sessions, got %d", len(plan.Sessions))
	}
}

func TestGenerateSessionName(t *testing.T) {
	tests := []struct {
		agentType  string
		sessionNum int
		expected   string
	}{
		{"cc", 1, "cc_agents_1"},
		{"cc", 2, "cc_agents_2"},
		{"cod", 1, "cod_agents_1"},
		{"gmi", 3, "gmi_agents_3"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			got := generateSessionName(tt.agentType, tt.sessionNum)
			if got != tt.expected {
				t.Errorf("generateSessionName(%q, %d) = %q, want %q",
					tt.agentType, tt.sessionNum, got, tt.expected)
			}
		})
	}
}

func TestItoa(t *testing.T) {
	tests := []struct {
		input    int
		expected string
	}{
		{0, "0"},
		{1, "1"},
		{10, "10"},
		{123, "123"},
		{-1, "-1"},
		{-42, "-42"},
	}

	for _, tt := range tests {
		got := itoa(tt.input)
		if got != tt.expected {
			t.Errorf("itoa(%d) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestAllocationCalculator_SessionPaneDistribution(t *testing.T) {
	cfg := testSwarmConfig()
	cfg.SessionsPerType = 2 // Reduce to 2 sessions for easier testing
	ac := NewAllocationCalculator(cfg)

	// Single tier1 project: 4 CC agents
	projects := []ProjectBeadCount{
		{Path: "/dp/proj1", Name: "proj1", OpenBeads: 500},
	}

	plan := ac.GenerateSwarmPlan("/dp", projects)

	// Count CC agents across all sessions
	totalCCPanes := 0
	for _, session := range plan.Sessions {
		if session.AgentType == "cc" {
			totalCCPanes += len(session.Panes)
		}
	}

	if totalCCPanes != 4 {
		t.Errorf("expected 4 CC panes total, got %d", totalCCPanes)
	}
}

func TestAllocationCalculator_CustomThresholds(t *testing.T) {
	cfg := testSwarmConfig()
	cfg.Tier1Threshold = 1000
	cfg.Tier2Threshold = 500
	ac := NewAllocationCalculator(cfg)

	tests := []struct {
		beadCount int
		expected  int
	}{
		{1500, 1},
		{1000, 1},
		{999, 2},
		{500, 2},
		{499, 3},
		{100, 3},
	}

	for _, tt := range tests {
		got := ac.CalculateTier(tt.beadCount)
		if got != tt.expected {
			t.Errorf("CalculateTier(%d) with custom thresholds = %d, want %d",
				tt.beadCount, got, tt.expected)
		}
	}
}

func TestAllocationCalculator_CustomAllocations(t *testing.T) {
	cfg := testSwarmConfig()
	cfg.Tier1Allocation = config.AllocationSpec{CC: 10, Cod: 5, Gmi: 3}
	ac := NewAllocationCalculator(cfg)

	project := ProjectBeadCount{Path: "/dp/big", Name: "big", OpenBeads: 500}
	alloc := ac.CalculateProjectAllocation(project)

	if alloc.CCAgents != 10 {
		t.Errorf("CCAgents = %d, want 10", alloc.CCAgents)
	}
	if alloc.CodAgents != 5 {
		t.Errorf("CodAgents = %d, want 5", alloc.CodAgents)
	}
	if alloc.GmiAgents != 3 {
		t.Errorf("GmiAgents = %d, want 3", alloc.GmiAgents)
	}
	if alloc.TotalAgents != 18 {
		t.Errorf("TotalAgents = %d, want 18", alloc.TotalAgents)
	}
}

func TestAllocationCalculator_PanesPerSessionOverride(t *testing.T) {
	// Test that PanesPerSession > 0 uses manual override
	cfg := testSwarmConfig()
	cfg.SessionsPerType = 3
	cfg.PanesPerSession = 5 // Manual override
	ac := NewAllocationCalculator(cfg)

	projects := []ProjectBeadCount{
		{Path: "/dp/proj1", Name: "proj1", OpenBeads: 500}, // tier1: 4+4+2
	}

	plan := ac.GenerateSwarmPlan("/dp", projects)

	if plan.PanesPerSession != 5 {
		t.Errorf("PanesPerSession = %d, want 5 (manual override)", plan.PanesPerSession)
	}
}

func TestAllocationCalculator_PanesPerSessionAutoCalculate(t *testing.T) {
	// Test that PanesPerSession = 0 triggers auto-calculation
	cfg := testSwarmConfig()
	cfg.SessionsPerType = 2
	cfg.PanesPerSession = 0 // Auto-calculate
	ac := NewAllocationCalculator(cfg)

	// Tier1 has 4 CC, 4 Cod, 2 Gmi
	// Max is 4, with 2 sessions: ceil(4/2) = 2 panes per session
	projects := []ProjectBeadCount{
		{Path: "/dp/proj1", Name: "proj1", OpenBeads: 500},
	}

	plan := ac.GenerateSwarmPlan("/dp", projects)

	if plan.PanesPerSession != 2 {
		t.Errorf("PanesPerSession = %d, want 2 (auto-calculated: ceil(4/2))", plan.PanesPerSession)
	}
}

// A manual --panes-per-session smaller than the allocation used to silently
// drop the leftover agents while the plan still reported the full count, so
// the operator saw "Total Agents: N", launched, and got fewer with no
// indication that any were discarded. The plan must now report what it will
// actually launch so callers can detect the shortfall.
func TestGenerateSwarmPlan_PlannedAgentsReflectsActualPanes(t *testing.T) {
	cfg := config.DefaultSwarmConfig()
	cfg.SessionsPerType = 3
	cfg.PanesPerSession = 2 // far too small for the allocation below

	calc := NewAllocationCalculator(&cfg)
	projects := []ProjectBeadCount{
		{Path: "/tmp/p1", Name: "p1", OpenBeads: 400},
		{Path: "/tmp/p2", Name: "p2", OpenBeads: 400},
		{Path: "/tmp/p3", Name: "p3", OpenBeads: 400},
		{Path: "/tmp/p4", Name: "p4", OpenBeads: 400},
	}
	plan := calc.GenerateSwarmPlan("/tmp", projects)

	actual := 0
	for _, session := range plan.Sessions {
		actual += len(session.Panes)
	}
	if plan.PlannedAgents != actual {
		t.Fatalf("PlannedAgents=%d but sessions hold %d panes", plan.PlannedAgents, actual)
	}
	t.Logf("total=%d planned=%d sessions=%d", plan.TotalAgents, plan.PlannedAgents, len(plan.Sessions))
	if plan.TotalAgents <= plan.PlannedAgents {
		t.Fatalf("expected the tiny grid to force a shortfall; total=%d planned=%d", plan.TotalAgents, plan.PlannedAgents)
	}
	t.Logf("allocation=%d planned=%d — the shortfall is now visible to callers",
		plan.TotalAgents, plan.PlannedAgents)
}

// The auto-sized path must never drop anything.
func TestGenerateSwarmPlan_AutoSizedPlanLosesNoAgents(t *testing.T) {
	cfg := config.DefaultSwarmConfig()
	cfg.SessionsPerType = 3
	cfg.PanesPerSession = 0 // auto

	calc := NewAllocationCalculator(&cfg)
	projects := []ProjectBeadCount{
		{Path: "/tmp/p1", Name: "p1", OpenBeads: 400},
		{Path: "/tmp/p2", Name: "p2", OpenBeads: 120},
	}
	plan := calc.GenerateSwarmPlan("/tmp", projects)
	if plan.PlannedAgents != plan.TotalAgents {
		t.Fatalf("auto-sized plan dropped agents: planned=%d total=%d", plan.PlannedAgents, plan.TotalAgents)
	}
}
