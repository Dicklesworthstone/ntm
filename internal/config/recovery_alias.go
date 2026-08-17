package config

// WS6-wire (bd-ws6-config-truth-ienmd.1): resolve the memory.* / [recovery]
// shadowing by making [recovery] the SINGLE section for session-recovery
// tuning. The overlapping memory.* keys are ALIASED into their [recovery]
// counterparts for one release (v1.26.0) with a loud deprecation warning per
// key; v1.27.0 removes the aliases (WS6-remove-finalize flips warnings to
// strict-loader errors).
//
// Alias map (memory.* -> recovery.*):
//
//	memory.include_in_recovery   -> recovery.include_cm_memories
//	memory.max_rules             -> recovery.max_cm_rules
//	memory.query_timeout_seconds -> recovery.timeout_seconds
//	memory.enabled=false         -> recovery.include_cm_memories=false
//	                                (memory.enabled continues to gate
//	                                send-time rule injection directly)
//
// An explicitly-set [recovery] key always wins over its memory.* alias.
// The send-scoped memory keys (send_injection, send_max_rules,
// send_budget_tokens) are NOT recovery-shadowed; they keep their direct
// reader (internal/cli robot send injection). memory.include_anti_patterns
// and memory.include_history have no substrate and are removed by the
// sibling bead (bd-ws6-config-truth-ienmd.2).

import (
	"fmt"
	"io"

	"github.com/BurntSushi/toml"
)

// memoryAliasDefinedChecker is the slice of toml.MetaData the alias pass
// needs; a tiny interface keeps the pass unit-testable without crafting real
// decoder metadata.
type memoryAliasDefinedChecker interface {
	IsDefined(key ...string) bool
}

var _ memoryAliasDefinedChecker = &toml.MetaData{}

// applyMemoryRecoveryAliases folds explicitly-set deprecated memory.* keys
// into cfg.SessionRecovery and writes one deprecation warning per aliased key
// to warnDst. It is the registered reader for the aliased keys (G2 liveness).
func applyMemoryRecoveryAliases(cfg *Config, md memoryAliasDefinedChecker, warnDst io.Writer) {
	if cfg == nil || md == nil {
		return
	}
	warn := func(oldKey, newKey string) {
		if warnDst == nil {
			return
		}
		fmt.Fprintf(warnDst,
			"ntm: warning: config key %s is deprecated and aliased to %s for this release; move the setting to [recovery] — the alias is removed in v1.27.0\n",
			oldKey, newKey)
	}

	if md.IsDefined("memory", "include_in_recovery") {
		if !md.IsDefined("recovery", "include_cm_memories") {
			cfg.SessionRecovery.IncludeCMMemories = cfg.Memory.IncludeInRecovery
		}
		warn("memory.include_in_recovery", "recovery.include_cm_memories")
	}
	if md.IsDefined("memory", "max_rules") {
		if !md.IsDefined("recovery", "max_cm_rules") {
			cfg.SessionRecovery.MaxCMRules = cfg.Memory.MaxRules
		}
		warn("memory.max_rules", "recovery.max_cm_rules")
	}
	if md.IsDefined("memory", "query_timeout_seconds") {
		if !md.IsDefined("recovery", "timeout_seconds") {
			cfg.SessionRecovery.TimeoutSeconds = cfg.Memory.QueryTimeoutSeconds
		}
		warn("memory.query_timeout_seconds", "recovery.timeout_seconds")
	}
	if md.IsDefined("memory", "enabled") && !cfg.Memory.Enabled {
		// Disabling memory integration disables the CM slice of recovery
		// context, but never the whole [recovery] pipeline (agent mail and
		// beads context are not memory features).
		if !md.IsDefined("recovery", "include_cm_memories") {
			cfg.SessionRecovery.IncludeCMMemories = false
		}
		warn("memory.enabled", "recovery.include_cm_memories (recovery side; memory.enabled still gates send-time rule injection)")
	}
}

func init() {
	// G2 liveness claims for the aliased recovery-shadowed keys. The reader
	// is the alias fold itself: these keys now act exclusively through
	// [recovery]. memory.enabled is additionally read directly by the send
	// injection path (internal/cli), but a key has exactly one claim.
	RegisterReader("memory.include_in_recovery", applyMemoryRecoveryAliases)
	RegisterReader("memory.max_rules", applyMemoryRecoveryAliases)
	RegisterReader("memory.query_timeout_seconds", applyMemoryRecoveryAliases)
	RegisterReader("memory.enabled", applyMemoryRecoveryAliases)
}
