package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for [alerts] disk_full_horizon_hours (ntm-1k9g): parse, default,
// validation, and project-level merge.

func TestDiskFullHorizonHoursDefaultOff(t *testing.T) {
	cfg := Default()
	if cfg.Alerts.DiskFullHorizonHours != 0 {
		t.Fatalf("default disk_full_horizon_hours = %v, want 0 (disabled)", cfg.Alerts.DiskFullHorizonHours)
	}
}

func TestDiskFullHorizonHoursParse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := "[alerts]\nenabled = true\ndisk_full_horizon_hours = 12.5\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Logf("parsed disk_full_horizon_hours = %v", cfg.Alerts.DiskFullHorizonHours)
	if cfg.Alerts.DiskFullHorizonHours != 12.5 {
		t.Fatalf("disk_full_horizon_hours = %v, want 12.5", cfg.Alerts.DiskFullHorizonHours)
	}
}

func TestDiskFullHorizonHoursValidation(t *testing.T) {
	cfg := Default()
	cfg.Alerts.DiskFullHorizonHours = -1

	errs := Validate(cfg)
	found := false
	for _, err := range errs {
		if strings.Contains(err.Error(), "disk_full_horizon_hours") {
			found = true
			t.Logf("validation error (expected): %v", err)
		}
	}
	if !found {
		t.Fatalf("expected validation error for negative disk_full_horizon_hours, got %v", errs)
	}

	cfg.Alerts.DiskFullHorizonHours = 24
	for _, err := range Validate(cfg) {
		if strings.Contains(err.Error(), "disk_full_horizon_hours") {
			t.Fatalf("unexpected validation error for positive horizon: %v", err)
		}
	}
}

func TestDiskFullHorizonHoursProjectMerge(t *testing.T) {
	global := Default()
	global.Alerts.DiskFullHorizonHours = 6

	horizon := 48.0
	project := &ProjectConfig{Alerts: &ProjectAlerts{DiskFullHorizonHours: &horizon}}

	merged := MergeConfig(global, project, t.TempDir())
	t.Logf("merged disk_full_horizon_hours = %v (global 6, project 48)", merged.Alerts.DiskFullHorizonHours)
	if merged.Alerts.DiskFullHorizonHours != 48 {
		t.Fatalf("merged horizon = %v, want project override 48", merged.Alerts.DiskFullHorizonHours)
	}

	// Unset in project: global wins. MergeConfig mutates the global in
	// place, so start from a fresh one.
	global = Default()
	global.Alerts.DiskFullHorizonHours = 6
	merged = MergeConfig(global, &ProjectConfig{Alerts: &ProjectAlerts{}}, t.TempDir())
	if merged.Alerts.DiskFullHorizonHours != 6 {
		t.Fatalf("merged horizon = %v, want global 6 when project unset", merged.Alerts.DiskFullHorizonHours)
	}
}

func TestDiskFullHorizonHoursGetValue(t *testing.T) {
	cfg := Default()
	cfg.Alerts.DiskFullHorizonHours = 9

	got, err := GetValue(cfg, "alerts.disk_full_horizon_hours")
	if err != nil {
		t.Fatalf("GetValue: %v", err)
	}
	if got != 9.0 {
		t.Fatalf("GetValue = %v, want 9", got)
	}
}
