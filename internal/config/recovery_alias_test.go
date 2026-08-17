package config

// WS6-wire behavior proof (bd-ws6-config-truth-ienmd.1): the deprecated
// memory.* recovery-shadowed keys act through [recovery] (single section),
// with one deprecation warning per aliased key; explicit [recovery] keys win.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func decodeForAlias(t *testing.T, tomlSrc string) (*Config, *toml.MetaData) {
	t.Helper()
	cfg := Default()
	md, err := toml.Decode(tomlSrc, cfg)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return cfg, &md
}

func TestMemoryRecoveryAliases(t *testing.T) {
	t.Run("memory keys alias into recovery with warnings", func(t *testing.T) {
		cfg, md := decodeForAlias(t, `
[memory]
include_in_recovery = false
max_rules = 7
query_timeout_seconds = 9
`)
		var warnings bytes.Buffer
		applyMemoryRecoveryAliases(cfg, md, &warnings)

		if cfg.SessionRecovery.IncludeCMMemories {
			t.Error("memory.include_in_recovery=false must flip recovery.include_cm_memories")
		}
		if cfg.SessionRecovery.MaxCMRules != 7 {
			t.Errorf("recovery.max_cm_rules = %d, want aliased 7", cfg.SessionRecovery.MaxCMRules)
		}
		if cfg.SessionRecovery.TimeoutSeconds != 9 {
			t.Errorf("recovery.timeout_seconds = %d, want aliased 9", cfg.SessionRecovery.TimeoutSeconds)
		}
		w := warnings.String()
		for _, want := range []string{
			"memory.include_in_recovery is deprecated",
			"memory.max_rules is deprecated",
			"memory.query_timeout_seconds is deprecated",
			"removed in v1.27.0",
		} {
			if !strings.Contains(w, want) {
				t.Errorf("warnings missing %q; got:\n%s", want, w)
			}
		}
	})

	t.Run("explicit recovery keys beat memory aliases", func(t *testing.T) {
		cfg, md := decodeForAlias(t, `
[memory]
max_rules = 7
query_timeout_seconds = 9

[recovery]
max_cm_rules = 21
timeout_seconds = 33
`)
		var warnings bytes.Buffer
		applyMemoryRecoveryAliases(cfg, md, &warnings)

		if cfg.SessionRecovery.MaxCMRules != 21 {
			t.Errorf("recovery.max_cm_rules = %d, want explicit 21 over alias", cfg.SessionRecovery.MaxCMRules)
		}
		if cfg.SessionRecovery.TimeoutSeconds != 33 {
			t.Errorf("recovery.timeout_seconds = %d, want explicit 33 over alias", cfg.SessionRecovery.TimeoutSeconds)
		}
		// Deprecation warnings still fire for the deprecated keys.
		if !strings.Contains(warnings.String(), "memory.max_rules is deprecated") {
			t.Error("expected deprecation warning even when the recovery key wins")
		}
	})

	t.Run("memory.enabled=false disables only the cm slice of recovery", func(t *testing.T) {
		cfg, md := decodeForAlias(t, `
[memory]
enabled = false
`)
		var warnings bytes.Buffer
		applyMemoryRecoveryAliases(cfg, md, &warnings)

		if cfg.SessionRecovery.IncludeCMMemories {
			t.Error("memory.enabled=false must disable recovery.include_cm_memories")
		}
		if !cfg.SessionRecovery.Enabled {
			t.Error("memory.enabled=false must NOT disable the whole [recovery] pipeline")
		}
	})

	t.Run("untouched config emits no warnings and changes nothing", func(t *testing.T) {
		cfg, md := decodeForAlias(t, `theme = "dark"`)
		var warnings bytes.Buffer
		before := cfg.SessionRecovery
		applyMemoryRecoveryAliases(cfg, md, &warnings)
		if warnings.Len() != 0 {
			t.Errorf("unexpected warnings: %s", warnings.String())
		}
		if cfg.SessionRecovery != before {
			t.Error("recovery config changed without any memory.* keys set")
		}
	})
}

// TestLoadAppliesMemoryRecoveryAlias proves the alias runs on the real Load
// path end to end.
func TestLoadAppliesMemoryRecoveryAlias(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[memory]\nmax_rules = 4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SessionRecovery.MaxCMRules != 4 {
		t.Errorf("Load: recovery.max_cm_rules = %d, want aliased 4", cfg.SessionRecovery.MaxCMRules)
	}
}
