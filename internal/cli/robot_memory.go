package cli

import (
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

// robotSendMemoryOptions resolves the effective --with-memory state and the
// config-derived injection parameters for a robot send (bd-3j6hm).
//
// Semantics mirror the CASS send precedent (SendOptions.WithCASS): the
// per-call flag alone drives injection, and config supplies parameter
// defaults rather than vetoing the call. [memory] send_injection=true makes
// injection the default for robot sends (the flag is then redundant);
// [memory] enabled=false degrades an explicit --with-memory to a recorded
// skip inside the robot layer instead of failing the send.
func init() {
	// G2 config-key liveness claims (WS6-wire, bd-ws6-config-truth-ienmd.1):
	// the send-scoped [memory] keys are read here. They are NOT
	// recovery-shadowed and stay in [memory]; the recovery-overlapping keys
	// are aliased into [recovery] by internal/config (recovery_alias.go).
	config.RegisterReader("memory.send_injection", robotSendMemoryOptions)
	config.RegisterReader("memory.send_max_rules", robotSendMemoryOptions)
	config.RegisterReader("memory.send_budget_tokens", robotSendMemoryOptions)
}

func robotSendMemoryOptions(flagEnabled bool, cfg *config.Config) (bool, *robot.CMInjectConfig) {
	enabled := flagEnabled
	if cfg == nil {
		return enabled, nil
	}
	if !enabled && cfg.Memory.Enabled && cfg.Memory.SendInjection {
		enabled = true
	}

	inject := robot.DefaultCMInjectConfig()
	inject.Enabled = cfg.Memory.Enabled
	if cfg.Memory.SendMaxRules > 0 {
		inject.MaxRules = cfg.Memory.SendMaxRules
	}
	if cfg.Memory.SendBudgetTokens > 0 {
		inject.BudgetTokens = cfg.Memory.SendBudgetTokens
	}
	if cfg.Memory.QueryTimeoutSeconds > 0 {
		inject.QueryTimeout = time.Duration(cfg.Memory.QueryTimeoutSeconds) * time.Second
	}
	return enabled, &inject
}
