package pressure

import "time"

// Action enumerates the gated swarm operations the governor knows about.
type Action string

const (
	ActionAgentSend      Action = "agent_send"
	ActionAgentInterrupt Action = "agent_interrupt"
	ActionSwarmSpawn     Action = "swarm_spawn"
	ActionPipelineFanout Action = "pipeline_fanout"
	ActionBuildOrTest    Action = "build_or_test"
	ActionScannerScan    Action = "scanner_scan"
)

// Decision is the gating outcome for a single Action attempt.
type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionDefer Decision = "defer"
	DecisionDeny  Decision = "deny"
)

// Budget caps swarm operations at a particular scope. The zero value is
// "no caps" — callers always merge with DefaultBudget before checking.
//
// DeferAtLevel and DenyAtLevel apply to non-urgent actions: anything at
// or above DeferAtLevel becomes Decision="defer"; anything at or above
// DenyAtLevel becomes Decision="deny". Urgent actions bypass these.
type Budget struct {
	MaxConcurrentSends int           `json:"max_concurrent_sends,omitempty"`
	MaxPipelineFanout  int           `json:"max_pipeline_fanout,omitempty"`
	MaxBuildSlots      int           `json:"max_build_slots,omitempty"`
	DeferAtLevel       Level         `json:"defer_at_level"`
	DenyAtLevel        Level         `json:"deny_at_level"`
	ScannerInterval    time.Duration `json:"scanner_interval,omitempty"`
}

// DefaultBudget is the conservative global default.
func DefaultBudget() Budget {
	return Budget{
		MaxConcurrentSends: 16,
		MaxPipelineFanout:  16,
		MaxBuildSlots:      8,
		DeferAtLevel:       LevelHigh,
		DenyAtLevel:        LevelCritical,
		ScannerInterval:    5 * time.Second,
	}
}
