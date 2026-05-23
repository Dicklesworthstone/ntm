package config

import (
	"testing"
)

func TestDiff_NilConfig(t *testing.T) {
	t.Parallel()

	diffs := Diff(nil)
	if diffs != nil {
		t.Errorf("expected nil for nil config, got %d diffs", len(diffs))
	}
}

func TestDiff_DefaultConfig_NoDiffs(t *testing.T) {
	t.Parallel()

	cfg := Default()
	diffs := Diff(cfg)

	if len(diffs) != 0 {
		t.Errorf("expected 0 diffs for default config, got %d:", len(diffs))
		for _, d := range diffs {
			t.Logf("  %s: default=%v current=%v", d.Path, d.Default, d.Current)
		}
	}
}

func TestDiff_TopLevelTheme(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.Theme = "dark"

	diffs := Diff(cfg)

	found := false
	for _, d := range diffs {
		if d.Path == "theme" {
			found = true
			if d.Current != "dark" {
				t.Errorf("expected current='dark', got %v", d.Current)
			}
			if d.Source != "config" {
				t.Errorf("expected source='config', got %q", d.Source)
			}
			if d.Key != d.Path {
				t.Errorf("expected key==path, got key=%q path=%q", d.Key, d.Path)
			}
		}
	}
	if !found {
		t.Error("expected diff for theme, not found")
	}
}

func TestDiff_AgentsChanged(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.Agents.Claude = "/custom/path/to/claude"

	diffs := Diff(cfg)

	found := false
	for _, d := range diffs {
		if d.Path == "agents.claude" {
			found = true
			if d.Current != "/custom/path/to/claude" {
				t.Errorf("expected custom claude path, got %v", d.Current)
			}
		}
	}
	if !found {
		t.Error("expected diff for agents.claude")
	}
}

func TestDiff_TmuxSettings(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.Tmux.DefaultPanes = 20
	cfg.Tmux.PaletteKey = "F7"
	cfg.Tmux.PaneInitDelayMs = 500

	diffs := Diff(cfg)

	paths := make(map[string]bool)
	for _, d := range diffs {
		paths[d.Path] = true
	}

	for _, expected := range []string{
		"tmux.default_panes",
		"tmux.palette_key",
		"tmux.pane_init_delay_ms",
	} {
		if !paths[expected] {
			t.Errorf("expected diff for %q, not found", expected)
		}
	}
}

func TestDiff_AgentMailSettings(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.AgentMail.Enabled = false
	cfg.AgentMail.URL = "http://custom:9999"
	cfg.AgentMail.AutoRegister = false
	supervise := true
	cfg.AgentMail.SupervisorEnabled = &supervise

	diffs := Diff(cfg)

	paths := make(map[string]bool)
	for _, d := range diffs {
		paths[d.Path] = true
	}

	for _, expected := range []string{
		"agent_mail.enabled",
		"agent_mail.url",
		"agent_mail.auto_register",
		"agent_mail.supervisor_enabled",
	} {
		if !paths[expected] {
			t.Errorf("expected diff for %q, not found", expected)
		}
	}
}

func TestDiff_AlertsSettings(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.Alerts.Enabled = false
	cfg.Alerts.AgentStuckMinutes = 999

	diffs := Diff(cfg)

	found := 0
	for _, d := range diffs {
		if d.Path == "alerts.enabled" || d.Path == "alerts.agent_stuck_minutes" {
			found++
		}
	}
	if found != 2 {
		t.Errorf("expected 2 alert diffs, found %d", found)
	}
}

func TestDiff_CheckpointsSettings(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.Checkpoints.Enabled = !cfg.Checkpoints.Enabled

	diffs := Diff(cfg)

	found := false
	for _, d := range diffs {
		if d.Path == "checkpoints.enabled" {
			found = true
		}
	}
	if !found {
		t.Error("expected diff for checkpoints.enabled")
	}
}

func TestDiff_ResilienceSettings(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.Resilience.MaxRestarts = 999

	diffs := Diff(cfg)

	found := false
	for _, d := range diffs {
		if d.Path == "resilience.max_restarts" {
			found = true
			if d.Current != 999 {
				t.Errorf("expected current=999, got %v", d.Current)
			}
		}
	}
	if !found {
		t.Error("expected diff for resilience.max_restarts")
	}
}

func TestDiff_ContextRotationSettings(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.ContextRotation.Enabled = !cfg.ContextRotation.Enabled
	cfg.ContextRotation.WarningThreshold = 0.5
	cfg.ContextRotation.RotateThreshold = 0.99

	diffs := Diff(cfg)

	paths := make(map[string]bool)
	for _, d := range diffs {
		paths[d.Path] = true
	}

	for _, expected := range []string{
		"context_rotation.enabled",
		"context_rotation.warning_threshold",
		"context_rotation.rotate_threshold",
	} {
		if !paths[expected] {
			t.Errorf("expected diff for %q, not found", expected)
		}
	}
}

func TestDiff_EnsembleSettings(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.Ensemble.DefaultEnsemble = "custom-ensemble"
	cfg.Ensemble.AllowAdvanced = !cfg.Ensemble.AllowAdvanced

	diffs := Diff(cfg)

	paths := make(map[string]bool)
	for _, d := range diffs {
		paths[d.Path] = true
	}

	if !paths["ensemble.default_ensemble"] {
		t.Error("expected diff for ensemble.default_ensemble")
	}
	if !paths["ensemble.allow_advanced"] {
		t.Error("expected diff for ensemble.allow_advanced")
	}
}

func TestDiff_EnsembleSynthesisSettings(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.Ensemble.Synthesis.Strategy = "custom"
	cfg.Ensemble.Synthesis.MinConfidence = 0.99

	diffs := Diff(cfg)

	paths := make(map[string]bool)
	for _, d := range diffs {
		paths[d.Path] = true
	}

	if !paths["ensemble.synthesis.strategy"] {
		t.Error("expected diff for ensemble.synthesis.strategy")
	}
	if !paths["ensemble.synthesis.min_confidence"] {
		t.Error("expected diff for ensemble.synthesis.min_confidence")
	}
}

func TestDiff_EnsembleCacheSettings(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.Ensemble.Cache.Enabled = !cfg.Ensemble.Cache.Enabled
	cfg.Ensemble.Cache.TTLMinutes = 999

	diffs := Diff(cfg)

	paths := make(map[string]bool)
	for _, d := range diffs {
		paths[d.Path] = true
	}

	if !paths["ensemble.cache.enabled"] {
		t.Error("expected diff for ensemble.cache.enabled")
	}
	if !paths["ensemble.cache.ttl_minutes"] {
		t.Error("expected diff for ensemble.cache.ttl_minutes")
	}
}

func TestDiff_EnsembleBudgetSettings(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.Ensemble.Budget.Total = 999999

	diffs := Diff(cfg)

	found := false
	for _, d := range diffs {
		if d.Path == "ensemble.budget.total" {
			found = true
		}
	}
	if !found {
		t.Error("expected diff for ensemble.budget.total")
	}
}

func TestDiff_EnsembleEarlyStopSettings(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.Ensemble.EarlyStop.Enabled = !cfg.Ensemble.EarlyStop.Enabled

	diffs := Diff(cfg)

	found := false
	for _, d := range diffs {
		if d.Path == "ensemble.early_stop.enabled" {
			found = true
		}
	}
	if !found {
		t.Error("expected diff for ensemble.early_stop.enabled")
	}
}

func TestDiff_CASSSettings(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.CASS.Enabled = !cfg.CASS.Enabled
	cfg.CASS.Context.MaxSessions = 999

	diffs := Diff(cfg)

	paths := make(map[string]bool)
	for _, d := range diffs {
		paths[d.Path] = true
	}

	if !paths["cass.enabled"] {
		t.Error("expected diff for cass.enabled")
	}
	if !paths["cass.context.max_sessions"] {
		t.Error("expected diff for cass.context.max_sessions")
	}
}

func TestDiff_HealthSettings(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.Health.Enabled = !cfg.Health.Enabled
	cfg.Health.MaxRestarts = 999

	diffs := Diff(cfg)

	paths := make(map[string]bool)
	for _, d := range diffs {
		paths[d.Path] = true
	}

	if !paths["health.enabled"] {
		t.Error("expected diff for health.enabled")
	}
	if !paths["health.max_restarts"] {
		t.Error("expected diff for health.max_restarts")
	}
}

func TestDiff_DCGIntegration(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.Integrations.DCG.Enabled = !cfg.Integrations.DCG.Enabled
	cfg.Integrations.DCG.BinaryPath = "/custom/dcg"

	diffs := Diff(cfg)

	paths := make(map[string]bool)
	for _, d := range diffs {
		paths[d.Path] = true
	}

	if !paths["integrations.dcg.enabled"] {
		t.Error("expected diff for integrations.dcg.enabled")
	}
	if !paths["integrations.dcg.binary_path"] {
		t.Error("expected diff for integrations.dcg.binary_path")
	}
}

func TestDiff_MultipleChanges(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.Theme = "solarized"
	cfg.Tmux.DefaultPanes = 99
	cfg.Alerts.Enabled = false
	cfg.Health.MaxRestarts = 42

	diffs := Diff(cfg)

	if len(diffs) < 4 {
		t.Errorf("expected at least 4 diffs, got %d", len(diffs))
	}

	// Verify all source fields are "config"
	for _, d := range diffs {
		if d.Source != "config" {
			t.Errorf("expected source='config' for %s, got %q", d.Path, d.Source)
		}
	}
}

func TestDiff_OnlyChangedFieldsReported(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.Theme = "monokai" // Only change one field

	diffs := Diff(cfg)

	if len(diffs) != 1 {
		t.Errorf("expected exactly 1 diff, got %d:", len(diffs))
		for _, d := range diffs {
			t.Logf("  %s: default=%v current=%v", d.Path, d.Default, d.Current)
		}
	}

	if len(diffs) == 1 && diffs[0].Path != "theme" {
		t.Errorf("expected diff for 'theme', got %q", diffs[0].Path)
	}
}
