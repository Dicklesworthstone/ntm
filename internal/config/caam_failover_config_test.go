package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCAAMConfigFailoverDefaults pins the doubly-opt-in defaults for the
// coordinator auto-failover keys (bd-um3uy).
func TestCAAMConfigFailoverDefaults(t *testing.T) {
	cfg := DefaultCAAMConfig()
	t.Logf("defaults: auto_failover=%v reset_horizon_minutes=%d failover_providers=%v",
		cfg.AutoFailover, cfg.ResetHorizonMinutes, cfg.FailoverProviders)
	if cfg.AutoFailover {
		t.Error("auto_failover must default to false")
	}
	if cfg.ResetHorizonMinutes != 30 {
		t.Errorf("reset_horizon_minutes default = %d, want 30", cfg.ResetHorizonMinutes)
	}
	if len(cfg.FailoverProviders) != 0 {
		t.Errorf("failover_providers default = %v, want empty (no providers)", cfg.FailoverProviders)
	}
}

// TestCAAMConfigFailoverParse verifies the keys round-trip through a TOML
// config file, and that absent keys keep the defaults (merge-with-defaults
// decode path).
func TestCAAMConfigFailoverParse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	contents := `
[integrations.caam]
auto_failover = true
reset_horizon_minutes = 45
failover_providers = ["claude", "openai"]
`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	caam := cfg.Integrations.CAAM
	t.Logf("parsed: auto_failover=%v reset_horizon_minutes=%d failover_providers=%v",
		caam.AutoFailover, caam.ResetHorizonMinutes, caam.FailoverProviders)
	if !caam.AutoFailover {
		t.Error("auto_failover = false, want true")
	}
	if caam.ResetHorizonMinutes != 45 {
		t.Errorf("reset_horizon_minutes = %d, want 45", caam.ResetHorizonMinutes)
	}
	if len(caam.FailoverProviders) != 2 || caam.FailoverProviders[0] != "claude" || caam.FailoverProviders[1] != "openai" {
		t.Errorf("failover_providers = %v, want [claude openai]", caam.FailoverProviders)
	}

	// Absent keys keep defaults.
	minimal := filepath.Join(dir, "minimal.toml")
	// integrations.caam.enabled is a hard error since v1.29.0 (bd-6otuk flip),
	// so the minimal fixture uses a live caam key instead.
	if err := os.WriteFile(minimal, []byte("[integrations.caam]\nbinary_path = \"/opt/caam\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg2, err := Load(minimal)
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.Integrations.CAAM.AutoFailover {
		t.Error("auto_failover flipped on without being configured")
	}
	if cfg2.Integrations.CAAM.ResetHorizonMinutes != 30 {
		t.Errorf("reset_horizon_minutes = %d, want default 30", cfg2.Integrations.CAAM.ResetHorizonMinutes)
	}
}

// TestValidateCAAMConfig covers the horizon >= 0 rule.
func TestValidateCAAMConfig(t *testing.T) {
	cfg := DefaultCAAMConfig()
	if err := ValidateCAAMConfig(&cfg); err != nil {
		t.Errorf("defaults must validate, got %v", err)
	}
	cfg.ResetHorizonMinutes = 0
	if err := ValidateCAAMConfig(&cfg); err != nil {
		t.Errorf("horizon 0 must validate, got %v", err)
	}
	cfg.ResetHorizonMinutes = -1
	err := ValidateCAAMConfig(&cfg)
	t.Logf("negative horizon: %v", err)
	if err == nil {
		t.Error("negative reset_horizon_minutes must fail validation")
	}
	if err := ValidateCAAMConfig(nil); err != nil {
		t.Errorf("nil config must validate, got %v", err)
	}

	// The top-level Validate must surface the error too.
	full := Default()
	full.Integrations.CAAM.ResetHorizonMinutes = -5
	errs := Validate(full)
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "integrations.caam") {
			found = true
			t.Logf("Validate surfaced: %v", e)
		}
	}
	if !found {
		t.Errorf("Validate(cfg) = %v, want an integrations.caam error", errs)
	}
}

// TestCAAMConfigFailoverGetValue verifies dotted-path lookups for the new keys.
func TestCAAMConfigFailoverGetValue(t *testing.T) {
	cfg := Default()
	cfg.Integrations.CAAM.AutoFailover = true
	cfg.Integrations.CAAM.ResetHorizonMinutes = 15
	cfg.Integrations.CAAM.FailoverProviders = []string{"claude"}

	cases := []struct {
		path string
		want interface{}
	}{
		{"integrations.caam.auto_failover", true},
		{"integrations.caam.reset_horizon_minutes", 15},
	}
	for _, tc := range cases {
		got, err := GetValue(cfg, tc.path)
		if err != nil {
			t.Errorf("GetValue(%q): %v", tc.path, err)
			continue
		}
		t.Logf("GetValue(%q) = %v", tc.path, got)
		if got != tc.want {
			t.Errorf("GetValue(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
	got, err := GetValue(cfg, "integrations.caam.failover_providers")
	if err != nil {
		t.Fatalf("GetValue(failover_providers): %v", err)
	}
	providers, ok := got.([]string)
	if !ok || len(providers) != 1 || providers[0] != "claude" {
		t.Errorf("GetValue(failover_providers) = %#v, want [claude]", got)
	}
}

// TestCAAMConfigFailoverTemplateAndDiff verifies the rendered config template
// carries the new keys and Diff reports non-default values.
func TestCAAMConfigFailoverTemplateAndDiff(t *testing.T) {
	var buf bytes.Buffer
	if err := Print(Default(), &buf); err != nil {
		t.Fatal(err)
	}
	rendered := buf.String()
	for _, key := range []string{"auto_failover = false", "reset_horizon_minutes = 30", "failover_providers = "} {
		if !strings.Contains(rendered, key) {
			t.Errorf("rendered template missing %q", key)
		}
	}

	cfg := Default()
	cfg.Integrations.CAAM.AutoFailover = true
	cfg.Integrations.CAAM.ResetHorizonMinutes = 90
	cfg.Integrations.CAAM.FailoverProviders = []string{"openai"}
	diffs := Diff(cfg)
	want := map[string]bool{
		"integrations.caam.auto_failover":         false,
		"integrations.caam.reset_horizon_minutes": false,
		"integrations.caam.failover_providers":    false,
	}
	for _, d := range diffs {
		if _, ok := want[d.Key]; ok {
			want[d.Key] = true
			t.Logf("diff: %s current=%v default=%v", d.Key, d.Current, d.Default)
		}
	}
	for key, seen := range want {
		if !seen {
			t.Errorf("Diff missing %s", key)
		}
	}
}
