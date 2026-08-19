package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GH#253: [integrations.bv] timeout_seconds + NTM_BV_TIMEOUT env override.

func TestBVConfigDefault(t *testing.T) {
	cfg := Default()
	if got := cfg.Integrations.BV.TimeoutSeconds; got != 30 {
		t.Fatalf("default integrations.bv.timeout_seconds = %d, want 30 (historical hard-coded cap)", got)
	}
}

func TestBVConfigTOMLParse(t *testing.T) {
	t.Setenv("NTM_BV_TIMEOUT", "")
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(`
[integrations.bv]
timeout_seconds = 90
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if got := cfg.Integrations.BV.TimeoutSeconds; got != 90 {
		t.Fatalf("integrations.bv.timeout_seconds = %d, want 90 from TOML", got)
	}

	// GetValue exposes the key for `ntm config get`.
	val, err := GetValue(cfg, "integrations.bv.timeout_seconds")
	if err != nil {
		t.Fatalf("GetValue(integrations.bv.timeout_seconds): %v", err)
	}
	if val != 90 {
		t.Fatalf("GetValue(integrations.bv.timeout_seconds) = %v, want 90", val)
	}
}

func TestBVConfigEnvOverrideWinsOverTOML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(`
[integrations.bv]
timeout_seconds = 90
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("NTM_BV_TIMEOUT", "5")
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if got := cfg.Integrations.BV.TimeoutSeconds; got != 5 {
		t.Fatalf("integrations.bv.timeout_seconds = %d, want 5 (NTM_BV_TIMEOUT wins over TOML)", got)
	}

	// Invalid env values are ignored: TOML value survives.
	for _, raw := range []string{"abc", "0", "-3"} {
		t.Setenv("NTM_BV_TIMEOUT", raw)
		cfg, err = Load(configPath)
		if err != nil {
			t.Fatalf("Load with NTM_BV_TIMEOUT=%q failed: %v", raw, err)
		}
		if got := cfg.Integrations.BV.TimeoutSeconds; got != 90 {
			t.Fatalf("integrations.bv.timeout_seconds with NTM_BV_TIMEOUT=%q = %d, want TOML 90", raw, got)
		}
	}
}

func TestBVConfigValidation(t *testing.T) {
	cfg := Default()
	cfg.Integrations.BV.TimeoutSeconds = -1
	errs := Validate(cfg)
	found := false
	for _, err := range errs {
		if strings.Contains(err.Error(), "integrations.bv") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Validate did not flag negative integrations.bv.timeout_seconds; errs = %v", errs)
	}
}
