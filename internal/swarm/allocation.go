package swarm

import (
	"path/filepath"
	"sort"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

// AllocationCalculator computes agent allocations for projects based on
// bead counts and tier thresholds from SwarmConfig.
type AllocationCalculator struct {
	// Config holds the swarm configuration with tier thresholds and allocations.
	Config *config.SwarmConfig
}

// NewAllocationCalculator creates a new AllocationCalculator with the given config.
func NewAllocationCalculator(cfg *config.SwarmConfig) *AllocationCalculator {
	return &AllocationCalculator{
		Config: cfg,
	}
}

// CalculateTier returns the tier number (1, 2, or 3) for a given bead count.
func (ac *AllocationCalculator) CalculateTier(beadCount int) int {
	if beadCount >= ac.Config.Tier1Threshold {
		return 1
	}
	if beadCount >= ac.Config.Tier2Threshold {
		return 2
	}
	return 3
}

// CalculateProjectAllocation computes the allocation for a single project.
func (ac *AllocationCalculator) CalculateProjectAllocation(project ProjectBeadCount) ProjectAllocation {
	tier := ac.CalculateTier(project.OpenBeads)
	alloc := ac.Config.GetAllocationForBeadCount(project.OpenBeads)

	// Update the project's tier if not already set
	project.Tier = tier

	return ProjectAllocation{
		Project:     project,
		CCAgents:    alloc.CC,
		CodAgents:   alloc.Cod,
		GmiAgents:   alloc.Gmi,
		TotalAgents: alloc.Total(),
	}
}

// CalculateAllocations computes allocations for all projects.
// Projects are sorted by bead count descending (highest priority first).
func (ac *AllocationCalculator) CalculateAllocations(projects []ProjectBeadCount) []ProjectAllocation {
	if len(projects) == 0 {
		return []ProjectAllocation{}
	}

	// Sort projects by bead count descending
	sorted := make([]ProjectBeadCount, len(projects))
	copy(sorted, projects)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].OpenBeads > sorted[j].OpenBeads
	})

	allocations := make([]ProjectAllocation, len(sorted))
	for i, project := range sorted {
		allocations[i] = ac.CalculateProjectAllocation(project)
	}

	return allocations
}

// AggregateTotals computes the total agents by type across all allocations.
type AggregateTotals struct {
	TotalCC     int
	TotalCod    int
	TotalGmi    int
	TotalAgents int
}

// CalculateTotals sums up agent counts across all allocations.
func (ac *AllocationCalculator) CalculateTotals(allocations []ProjectAllocation) AggregateTotals {
	var totals AggregateTotals
	for _, alloc := range allocations {
		totals.TotalCC += alloc.CCAgents
		totals.TotalCod += alloc.CodAgents
		totals.TotalGmi += alloc.GmiAgents
		totals.TotalAgents += alloc.TotalAgents
	}
	return totals
}

// ProjectBeadCountFromPath creates a ProjectBeadCount from a path and bead count.
// The name is extracted from the last component of the path.
func ProjectBeadCountFromPath(path string, openBeads int) ProjectBeadCount {
	return ProjectBeadCount{
		Path:      path,
		Name:      filepath.Base(path),
		OpenBeads: openBeads,
	}
}

// AllocationResult contains the complete allocation calculation result.
type AllocationResult struct {
	Allocations []ProjectAllocation
	Totals      AggregateTotals
}

// Calculate performs the complete allocation calculation for a set of projects.
// Returns allocations sorted by priority (highest bead count first) and totals.
func (ac *AllocationCalculator) Calculate(projects []ProjectBeadCount) AllocationResult {
	allocations := ac.CalculateAllocations(projects)
	totals := ac.CalculateTotals(allocations)

	return AllocationResult{
		Allocations: allocations,
		Totals:      totals,
	}
}

// GenerateSwarmPlan creates a complete SwarmPlan from projects.
// This includes allocations, totals, and session specifications.
func (ac *AllocationCalculator) GenerateSwarmPlan(scanDir string, projects []ProjectBeadCount) *SwarmPlan {
	result := ac.Calculate(projects)

	// Calculate panes per session
	maxAgentsPerType := max(result.Totals.TotalCC, result.Totals.TotalCod, result.Totals.TotalGmi)
	sessionsPerType := ac.Config.SessionsPerType
	panesPerSession := 0
	if sessionsPerType > 0 && maxAgentsPerType > 0 {
		panesPerSession = (maxAgentsPerType + sessionsPerType - 1) / sessionsPerType // Ceiling division
	}

	// Generate session specifications
	sessions := ac.generateSessions(result.Allocations, sessionsPerType, panesPerSession)

	return &SwarmPlan{
		CreatedAt:       time.Now().UTC(),
		ScanDir:         scanDir,
		Allocations:     result.Allocations,
		TotalCC:         result.Totals.TotalCC,
		TotalCod:        result.Totals.TotalCod,
		TotalGmi:        result.Totals.TotalGmi,
		TotalAgents:     result.Totals.TotalAgents,
		SessionsPerType: sessionsPerType,
		PanesPerSession: panesPerSession,
		Sessions:        sessions,
	}
}

// generateSessions creates session specifications for all agent types.
func (ac *AllocationCalculator) generateSessions(allocations []ProjectAllocation, sessionsPerType, panesPerSession int) []SessionSpec {
	var sessions []SessionSpec

	// Generate CC sessions
	ccSessions := ac.generateSessionsForType("cc", allocations, sessionsPerType, panesPerSession,
		func(a ProjectAllocation) int { return a.CCAgents })
	sessions = append(sessions, ccSessions...)

	// Generate Codex sessions
	codSessions := ac.generateSessionsForType("cod", allocations, sessionsPerType, panesPerSession,
		func(a ProjectAllocation) int { return a.CodAgents })
	sessions = append(sessions, codSessions...)

	// Generate Gemini sessions
	gmiSessions := ac.generateSessionsForType("gmi", allocations, sessionsPerType, panesPerSession,
		func(a ProjectAllocation) int { return a.GmiAgents })
	sessions = append(sessions, gmiSessions...)

	return sessions
}

// generateSessionsForType creates sessions for a specific agent type.
func (ac *AllocationCalculator) generateSessionsForType(
	agentType string,
	allocations []ProjectAllocation,
	sessionsPerType int,
	panesPerSession int,
	getAgentCount func(ProjectAllocation) int,
) []SessionSpec {
	// Build a flat list of panes by iterating through projects
	var panes []PaneSpec
	globalAgentIndex := 0

	for _, alloc := range allocations {
		agentCount := getAgentCount(alloc)
		for i := 0; i < agentCount; i++ {
			globalAgentIndex++
			panes = append(panes, PaneSpec{
				Index:      globalAgentIndex,
				Project:    alloc.Project.Path,
				AgentType:  agentType,
				AgentIndex: i + 1,
				LaunchCmd:  agentType,
			})
		}
	}

	if len(panes) == 0 {
		return nil
	}

	// Distribute panes across sessions
	sessions := make([]SessionSpec, 0, sessionsPerType)
	paneIdx := 0

	for sessionNum := 1; sessionNum <= sessionsPerType && paneIdx < len(panes); sessionNum++ {
		sessionName := generateSessionName(agentType, sessionNum)
		sessionPanes := make([]PaneSpec, 0, panesPerSession)

		// Fill this session with panes
		for j := 0; j < panesPerSession && paneIdx < len(panes); j++ {
			pane := panes[paneIdx]
			pane.Index = j + 1 // 1-based index within session
			sessionPanes = append(sessionPanes, pane)
			paneIdx++
		}

		if len(sessionPanes) > 0 {
			sessions = append(sessions, SessionSpec{
				Name:      sessionName,
				AgentType: agentType,
				PaneCount: len(sessionPanes),
				Panes:     sessionPanes,
			})
		}
	}

	return sessions
}

// generateSessionName creates a session name in the format "{type}_agents_{num}".
func generateSessionName(agentType string, sessionNum int) string {
	return agentType + "_agents_" + itoa(sessionNum)
}

// itoa converts an int to a string without importing strconv.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}

	neg := i < 0
	if neg {
		i = -i
	}

	// Max int64 is 19 digits
	buf := make([]byte, 20)
	pos := len(buf)

	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}

	if neg {
		pos--
		buf[pos] = '-'
	}

	return string(buf[pos:])
}
