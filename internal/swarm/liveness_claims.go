package swarm

import (
	"github.com/Dicklesworthstone/ntm/internal/config"
)

// WS0-G2 config-key liveness claims for keys consumed by internal/swarm
// (bd-g2-claims-backlog-o787y). See internal/config/liveness.go.
func init() {
	// Tiered allocation (allocation.go).
	config.RegisterReader("swarm.tier1_threshold", (*AllocationCalculator).CalculateTier)
	config.RegisterReader("swarm.tier2_threshold", (*AllocationCalculator).CalculateTier)
	for _, key := range []string{
		"swarm.tier1_allocation.cc",
		"swarm.tier1_allocation.cod",
		"swarm.tier1_allocation.gmi",
		"swarm.tier2_allocation.cc",
		"swarm.tier2_allocation.cod",
		"swarm.tier2_allocation.gmi",
		"swarm.tier3_allocation.cc",
		"swarm.tier3_allocation.cod",
		"swarm.tier3_allocation.gmi",
	} {
		config.RegisterReader(key, (*AllocationCalculator).CalculateProjectAllocation)
	}
	config.RegisterReader("swarm.sessions_per_type", (*AllocationCalculator).GenerateSwarmPlan)
	config.RegisterReader("swarm.panes_per_session", (*AllocationCalculator).GenerateSwarmPlan)
	config.RegisterReader("swarm.auto_rotate_accounts", (*AllocationCalculator).GenerateSwarmPlan)

	// Claude credential isolation (claude_config_home.go).
	config.RegisterReader("agents.claude_isolate_credentials", ProvisionClaudeIsolation)
	config.RegisterReader("agents.claude_token_file", ProvisionClaudeIsolation)
}
