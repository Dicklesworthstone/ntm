package config

import (
	"fmt"
	"sort"
	"time"
)

// Spawn stagger modes: how `ntm spawn` and `--robot-spawn` pace prompt
// delivery between agents so they do not all select work at the same instant
// (thundering-herd prevention, docs/ORCHESTRATION_FEATURES.md Feature 7). The
// vocabulary and bounds live here so config validation, both flag surfaces,
// and the shared planner (robot.ResolveSpawnStagger) agree on one definition.
const (
	// SpawnStaggerNone delivers every agent's prompt without pacing.
	SpawnStaggerNone = "none"
	// SpawnStaggerFixed spaces consecutive agents by a fixed delay.
	SpawnStaggerFixed = "fixed"
	// SpawnStaggerSmart spaces consecutive agents by the learned rate-limit
	// delay of the strictest provider in the batch.
	SpawnStaggerSmart = "smart"

	// MaxSpawnStaggerDelay bounds every per-agent stagger interval.
	MaxSpawnStaggerDelay = 5 * time.Minute
	// DefaultSpawnStaggerDelay is the fixed-mode interval when none is set.
	DefaultSpawnStaggerDelay = 30 * time.Second
)

// SpawnConfig holds the [spawn] defaults shared by `ntm spawn` and
// `--robot-spawn`. Each value applies only when the corresponding flag is not
// given; an explicit flag always wins.
type SpawnConfig struct {
	// StaggerMode paces prompt delivery between spawned agents: none (the
	// default), fixed (StaggerDelay apart), or smart (learned rate-limit delay).
	StaggerMode string `toml:"stagger_mode"`
	// StaggerDelay is the fixed-mode interval between consecutive agents
	// (0 to 5m, e.g. "30s").
	StaggerDelay time.Duration `toml:"stagger_delay"`
}

// DefaultSpawnConfig returns the built-in [spawn] defaults: no pacing unless
// asked, and a 30s fixed-mode interval.
func DefaultSpawnConfig() SpawnConfig {
	return SpawnConfig{
		StaggerMode:  SpawnStaggerNone,
		StaggerDelay: DefaultSpawnStaggerDelay,
	}
}

// ValidateSpawnStaggerMode accepts none (or empty), fixed, or smart. The error
// omits the setting name so each surface can prefix its own flag or key.
func ValidateSpawnStaggerMode(mode string) error {
	switch mode {
	case "", SpawnStaggerNone, SpawnStaggerFixed, SpawnStaggerSmart:
		return nil
	default:
		return fmt.Errorf("must be one of none, fixed, or smart; got %q", mode)
	}
}

// ValidateSpawnStaggerDelay bounds a stagger interval to [0, MaxSpawnStaggerDelay].
func ValidateSpawnStaggerDelay(delay time.Duration) error {
	if delay < 0 || delay > MaxSpawnStaggerDelay {
		return fmt.Errorf("must be between 0 and %s", MaxSpawnStaggerDelay)
	}
	return nil
}

// ValidateSpawnConfig validates the [spawn] table.
func ValidateSpawnConfig(cfg *SpawnConfig) error {
	if cfg == nil {
		return nil
	}
	if err := ValidateSpawnStaggerMode(cfg.StaggerMode); err != nil {
		return fmt.Errorf("stagger_mode %w", err)
	}
	if err := ValidateSpawnStaggerDelay(cfg.StaggerDelay); err != nil {
		return fmt.Errorf("stagger_delay %w", err)
	}
	return nil
}

// SpawnPacingConfig configures the spawn admission control consulted by the
// robot spawn surface (internal/robot/spawn.go).
//
// Dead-knob cleanup (bd-6otuk, deprecated v1.28.0): the original pacing
// surface exposed a full rate-limiter/backoff/headroom configuration
// (max_spawns_per_sec, burst_size, default_retries, retry_delay_ms,
// backpressure_threshold, per-agent rate/ramp-up/cooldown/recovery knobs,
// [spawn_pacing.headroom], [spawn_pacing.backoff]), but no runtime pacing
// engine ever consumed those values — only the concurrency caps below are
// read. The dead keys take the deprecated-knob path (see removed_knobs.go)
// and are hard load errors since v1.29.0.
type SpawnPacingConfig struct {
	// Enabled controls whether spawn admission control is active.
	Enabled bool `toml:"enabled"`

	// MaxConcurrentSpawns is the maximum number of concurrent spawn operations.
	MaxConcurrentSpawns int `toml:"max_concurrent_spawns"`

	// AgentCaps contains per-agent-type concurrency caps.
	AgentCaps AgentPacingConfig `toml:"agent_caps"`

	// AgentTypeLimits optionally bounds each canonical agent type across the
	// observed tmux fleet. These limits are independent of AgentCaps' shared
	// budget. Zero or an omitted type means no additional per-type limit.
	AgentTypeLimits map[string]int `toml:"agent_type_limits"`
}

// AgentPacingConfig holds per-agent-type concurrency caps.
//
// Enforcement note: robot spawn admission sums these caps into ONE host-wide
// agent budget (running agents + requested agents must not exceed the sum;
// see robot.spawnAdmissionAgentLimit and pressure.EvaluateSpawnAdmission). No
// runtime paces or serializes launches per agent type, so each cap is that
// type's contribution to the shared budget.
type AgentPacingConfig struct {
	ClaudeMaxConcurrent int `toml:"claude_max_concurrent"` // Max concurrent claude spawns
	CodexMaxConcurrent  int `toml:"codex_max_concurrent"`  // Max concurrent codex spawns
	GeminiMaxConcurrent int `toml:"gemini_max_concurrent"` // Max concurrent gemini spawns
	OmpMaxConcurrent    int `toml:"omp_max_concurrent"`    // Max concurrent omp (Oh My Pi) spawns
}

// DefaultSpawnPacingConfig returns sensible spawn pacing defaults.
func DefaultSpawnPacingConfig() SpawnPacingConfig {
	return SpawnPacingConfig{
		Enabled:             true, // Enabled by default for safety
		MaxConcurrentSpawns: 4,
		AgentCaps: AgentPacingConfig{
			ClaudeMaxConcurrent: 3,
			CodexMaxConcurrent:  2,
			GeminiMaxConcurrent: 2,
			// omp's term covers one full canonical omp swarm (`--omp=8`),
			// so an 8-pane omp spawn is admitted alongside the default
			// Claude/Codex/Gemini budget (3+2+2) rather than refused as
			// agent_limit_exceeded once a couple of other agents run. omp
			// routes each pane through its own configured providers, so no
			// single subscription seat bounds it the way cc/cod/gmi are.
			OmpMaxConcurrent: 8,
		},
	}
}

// ValidateSpawnPacingConfig validates the spawn pacing configuration.
func ValidateSpawnPacingConfig(cfg *SpawnPacingConfig) error {
	if !cfg.Enabled {
		// Skip validation if pacing is disabled
		return nil
	}

	if cfg.MaxConcurrentSpawns < 1 {
		return fmt.Errorf("max_concurrent_spawns must be at least 1, got %d", cfg.MaxConcurrentSpawns)
	}
	if cfg.AgentCaps.ClaudeMaxConcurrent < 0 {
		return fmt.Errorf("agent_caps: claude_max_concurrent must be non-negative, got %d", cfg.AgentCaps.ClaudeMaxConcurrent)
	}
	if cfg.AgentCaps.CodexMaxConcurrent < 0 {
		return fmt.Errorf("agent_caps: codex_max_concurrent must be non-negative, got %d", cfg.AgentCaps.CodexMaxConcurrent)
	}
	if cfg.AgentCaps.GeminiMaxConcurrent < 0 {
		return fmt.Errorf("agent_caps: gemini_max_concurrent must be non-negative, got %d", cfg.AgentCaps.GeminiMaxConcurrent)
	}
	if cfg.AgentCaps.OmpMaxConcurrent < 0 {
		return fmt.Errorf("agent_caps: omp_max_concurrent must be non-negative, got %d", cfg.AgentCaps.OmpMaxConcurrent)
	}
	// Sort keys so multiple invalid settings have a deterministic diagnostic.
	types := make([]string, 0, len(cfg.AgentTypeLimits))
	for kind := range cfg.AgentTypeLimits {
		types = append(types, kind)
	}
	sort.Strings(types)
	for _, kind := range types {
		switch kind {
		case "claude", "codex", "gemini", "antigravity", "grok", "omp", "pi", "opencode", "cursor", "windsurf", "aider", "ollama":
		default:
			return fmt.Errorf("agent_type_limits: unknown agent type %q; use claude, codex, gemini, antigravity, grok, omp, pi, opencode, cursor, windsurf, aider, or ollama", kind)
		}
		if cfg.AgentTypeLimits[kind] < 0 {
			return fmt.Errorf("agent_type_limits: %s must be non-negative, got %d", kind, cfg.AgentTypeLimits[kind])
		}
	}
	return nil
}
