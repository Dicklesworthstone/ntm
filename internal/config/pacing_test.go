package config

import (
	"strings"
	"testing"
)

// Spawn pacing narrowed to the admission-control surface in v1.28.0
// (bd-6otuk): only enabled, max_concurrent_spawns, and the per-agent
// *_max_concurrent caps remain — the rest of the historical pacing surface
// (rate limiter, backoff, headroom) never had a runtime consumer and now
// takes the deprecated-knob warn path (see removed_knobs_test.go).

func TestDefaultSpawnPacingConfig(t *testing.T) {
	cfg := DefaultSpawnPacingConfig()

	if !cfg.Enabled {
		t.Error("Enabled should default to true")
	}
	if cfg.MaxConcurrentSpawns != 4 {
		t.Errorf("MaxConcurrentSpawns = %d, want 4", cfg.MaxConcurrentSpawns)
	}
	if cfg.AgentCaps.ClaudeMaxConcurrent != 3 {
		t.Errorf("ClaudeMaxConcurrent = %d, want 3", cfg.AgentCaps.ClaudeMaxConcurrent)
	}
	if cfg.AgentCaps.CodexMaxConcurrent != 2 {
		t.Errorf("CodexMaxConcurrent = %d, want 2", cfg.AgentCaps.CodexMaxConcurrent)
	}
	if cfg.AgentCaps.GeminiMaxConcurrent != 2 {
		t.Errorf("GeminiMaxConcurrent = %d, want 2", cfg.AgentCaps.GeminiMaxConcurrent)
	}

	if err := ValidateSpawnPacingConfig(&cfg); err != nil {
		t.Errorf("defaults must validate: %v", err)
	}
}

func TestValidateSpawnPacingConfig(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*SpawnPacingConfig)
		wantErr string
	}{
		{"disabled skips validation", func(c *SpawnPacingConfig) { c.Enabled = false; c.MaxConcurrentSpawns = 0 }, ""},
		{"max_concurrent_spawns zero", func(c *SpawnPacingConfig) { c.MaxConcurrentSpawns = 0 }, "max_concurrent_spawns"},
		{"claude cap negative", func(c *SpawnPacingConfig) { c.AgentCaps.ClaudeMaxConcurrent = -1 }, "claude_max_concurrent"},
		{"codex cap negative", func(c *SpawnPacingConfig) { c.AgentCaps.CodexMaxConcurrent = -1 }, "codex_max_concurrent"},
		{"gemini cap negative", func(c *SpawnPacingConfig) { c.AgentCaps.GeminiMaxConcurrent = -1 }, "gemini_max_concurrent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultSpawnPacingConfig()
			tt.mutate(&cfg)
			err := ValidateSpawnPacingConfig(&cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want mention of %q", err, tt.wantErr)
			}
		})
	}
}

func TestSpawnPacingFromTOML(t *testing.T) {
	path := createTempConfig(t, `
[spawn_pacing]
enabled = true
max_concurrent_spawns = 8

[spawn_pacing.agent_caps]
claude_max_concurrent = 5
codex_max_concurrent = 4
gemini_max_concurrent = 3
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SpawnPacing.MaxConcurrentSpawns != 8 {
		t.Errorf("MaxConcurrentSpawns = %d, want 8", cfg.SpawnPacing.MaxConcurrentSpawns)
	}
	if cfg.SpawnPacing.AgentCaps.ClaudeMaxConcurrent != 5 {
		t.Errorf("ClaudeMaxConcurrent = %d, want 5", cfg.SpawnPacing.AgentCaps.ClaudeMaxConcurrent)
	}
	if cfg.SpawnPacing.AgentCaps.CodexMaxConcurrent != 4 {
		t.Errorf("CodexMaxConcurrent = %d, want 4", cfg.SpawnPacing.AgentCaps.CodexMaxConcurrent)
	}
	if cfg.SpawnPacing.AgentCaps.GeminiMaxConcurrent != 3 {
		t.Errorf("GeminiMaxConcurrent = %d, want 3", cfg.SpawnPacing.AgentCaps.GeminiMaxConcurrent)
	}
}
