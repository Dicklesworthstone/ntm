package config

import (
	"fmt"
)

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
}

// AgentPacingConfig holds per-agent-type concurrency caps.
type AgentPacingConfig struct {
	ClaudeMaxConcurrent int `toml:"claude_max_concurrent"` // Max concurrent claude spawns
	CodexMaxConcurrent  int `toml:"codex_max_concurrent"`  // Max concurrent codex spawns
	GeminiMaxConcurrent int `toml:"gemini_max_concurrent"` // Max concurrent gemini spawns
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
	return nil
}
