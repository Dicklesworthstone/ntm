package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRateLimitAutoRotateAlias — repro from ntm#113 Gap 3. The TOML key
// `[resilience.rate_limit] auto_rotate = true` must (a) parse cleanly through
// the strict loader instead of being rejected as `unknown field`, and (b)
// flip BOTH `Rotation.Enabled` and `Rotation.AutoTrigger` so the runtime
// monitor in internal/resilience/monitor.go (which gates on
// `rotateConfig.Enabled && rotateConfig.AutoTrigger`) actually acts on it.
// Flipping only AutoTrigger would silently no-op because Rotation.Enabled
// defaults to false.
func TestRateLimitAutoRotateAlias(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(`projects_base = "/tmp"

[resilience.rate_limit]
detect = true
notify = true
auto_rotate = true
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() rejected resilience.rate_limit.auto_rotate: %v", err)
	}

	if !cfg.Resilience.RateLimit.AutoRotate {
		t.Errorf("RateLimit.AutoRotate = false, want true (TOML set it true)")
	}
	if !cfg.Rotation.AutoTrigger {
		t.Errorf("Rotation.AutoTrigger = false, want true (alias should fold into canonical knob)")
	}
	if !cfg.Rotation.Enabled {
		t.Errorf("Rotation.Enabled = false, want true (alias must enable rotation or AutoTrigger silently no-ops)")
	}
}

// TestRateLimitAutoRotateAliasIsAdditive — the alias must NOT clobber an
// explicit `[rotation] auto_trigger = true` to false when the alias is unset.
// If the user has only `[rotation]` configured, the loader must leave it
// alone (and not flip Rotation.Enabled either, since the alias is the
// trigger for the dual-flip).
func TestRateLimitAutoRotateAliasIsAdditive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(`projects_base = "/tmp"

[rotation]
auto_trigger = true
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Resilience.RateLimit.AutoRotate {
		t.Errorf("RateLimit.AutoRotate = true unexpectedly; alias should default false")
	}
	if !cfg.Rotation.AutoTrigger {
		t.Errorf("Rotation.AutoTrigger = false, want true (TOML set it directly)")
	}
	// Rotation.Enabled was not set in TOML and the alias is unset, so it
	// must keep its default (false). The dual-flip path must only fire on
	// the alias.
	if cfg.Rotation.Enabled {
		t.Errorf("Rotation.Enabled = true unexpectedly; default is false and the alias is unset, so dual-flip must not fire")
	}
}
