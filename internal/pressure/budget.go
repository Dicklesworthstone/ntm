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

// MergeBudgets returns the more conservative merge of base + override.
// "More conservative" means: tighter (smaller) caps win; lower
// (less-tolerant) defer/deny levels win; longer scanner intervals win.
//
// A zero numeric field on either side is treated as "unspecified" so it
// does not pull the merged value to zero.
func MergeBudgets(base, override Budget) Budget {
	out := base
	out.MaxConcurrentSends = mergeMin(base.MaxConcurrentSends, override.MaxConcurrentSends)
	out.MaxPipelineFanout = mergeMin(base.MaxPipelineFanout, override.MaxPipelineFanout)
	out.MaxBuildSlots = mergeMin(base.MaxBuildSlots, override.MaxBuildSlots)
	if override.DeferAtLevel != 0 && override.DeferAtLevel < out.DeferAtLevel {
		out.DeferAtLevel = override.DeferAtLevel
	}
	if override.DenyAtLevel != 0 && override.DenyAtLevel < out.DenyAtLevel {
		out.DenyAtLevel = override.DenyAtLevel
	}
	if override.ScannerInterval > out.ScannerInterval {
		out.ScannerInterval = override.ScannerInterval
	}
	return out
}

// mergeMin returns the smaller positive of a, b. If one is zero the
// other wins; if both are zero the result is zero (= unspecified).
func mergeMin(a, b int) int {
	switch {
	case a == 0:
		return b
	case b == 0:
		return a
	case a < b:
		return a
	default:
		return b
	}
}
