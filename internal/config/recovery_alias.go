package config

// WS6-wire (bd-ws6-config-truth-ienmd.1) made [recovery] the SINGLE section
// for session-recovery tuning, aliasing the overlapping memory.* keys into
// their [recovery] counterparts for one release with a loud per-key
// deprecation warning. v1.27.0 was supposed to remove those aliases and did
// not: they were still emitted by the default template through v1.33.1, so
// `ntm config init` produced a file the same binary warned about on every
// invocation, while `config migrate` called it clean (ntm#323).
//
// The aliases are now gone. memory.include_in_recovery and memory.max_rules
// are removed keys (see removed_knobs.go) — `ntm config migrate` strips them
// and the strict loader names them. recovery.include_cm_memories,
// recovery.max_cm_rules and recovery.timeout_seconds are the knobs.
//
// memory.query_timeout_seconds stayed, un-deprecated: it was never purely a
// recovery alias — internal/cli reads it to bound the cm query behind
// send-time injection (--with-memory). It no longer touches [recovery].
//
// What remains here is one live cross-section rule, which is a documented
// semantic and not a deprecation: turning the memory integration off also
// turns off the CM slice of recovery context.

// memoryAliasDefinedChecker is the slice of toml.MetaData the fold needs; a
// tiny interface keeps it unit-testable without crafting real decoder
// metadata.
type memoryAliasDefinedChecker interface {
	IsDefined(key ...string) bool
}

// applyMemoryRecoveryLink folds `memory.enabled = false` into
// recovery.include_cm_memories.
//
// Disabling the memory integration disables the CM slice of recovery context,
// but never the whole [recovery] pipeline — Agent Mail and beads context are
// not memory features. An explicitly-set recovery.include_cm_memories always
// wins, so an operator can keep CM out of sends while keeping it in recovery,
// or the reverse.
func applyMemoryRecoveryLink(cfg *Config, md memoryAliasDefinedChecker) {
	if cfg == nil || md == nil {
		return
	}

	if md.IsDefined("memory", "enabled") && !cfg.Memory.Enabled {
		if !md.IsDefined("recovery", "include_cm_memories") {
			cfg.SessionRecovery.IncludeCMMemories = false
		}
	}
}

func init() {
	// G2 liveness claim. memory.enabled is additionally read directly by the
	// send injection path (internal/cli), but a key has exactly one claim.
	RegisterReader("memory.enabled", applyMemoryRecoveryLink)
}
