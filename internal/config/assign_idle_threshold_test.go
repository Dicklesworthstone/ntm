package config

import (
	"testing"
	"time"
)

func TestAssignIdleThresholdDuration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
		want time.Duration
	}{
		{"empty uses default", "", DefaultAssignIdleThreshold},
		{"whitespace uses default", "  ", DefaultAssignIdleThreshold},
		{"explicit minutes", "15m", 15 * time.Minute},
		{"explicit seconds", "300s", 300 * time.Second},
		{"legacy 120s tuning still honored", "120s", 120 * time.Second},
		{"invalid falls back to default", "banana", DefaultAssignIdleThreshold},
		{"zero falls back to default", "0s", DefaultAssignIdleThreshold},
		{"negative falls back to default", "-5m", DefaultAssignIdleThreshold},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := AssignConfig{IdleThreshold: tt.raw}
			if got := cfg.IdleThresholdDuration(); got != tt.want {
				t.Errorf("IdleThresholdDuration(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestDefaultAssignIdleThresholdIsGenerous(t *testing.T) {
	t.Parallel()
	// GH#238: the historical 120s window falsely failed healthy agents that
	// were silent while an external subprocess ran (review CLIs, long builds,
	// remote verification), then injected a second prompt mid-task. The
	// default must stay comfortably above realistic silent stretches.
	if DefaultAssignIdleThreshold < 10*time.Minute {
		t.Errorf("DefaultAssignIdleThreshold = %v, want >= 10m", DefaultAssignIdleThreshold)
	}
}

func TestValidateAssignConfigIdleThreshold(t *testing.T) {
	t.Parallel()
	if err := ValidateAssignConfig(&AssignConfig{}); err != nil {
		t.Errorf("empty idle_threshold must validate, got %v", err)
	}
	if err := ValidateAssignConfig(&AssignConfig{IdleThreshold: "15m"}); err != nil {
		t.Errorf("valid idle_threshold rejected: %v", err)
	}
	if err := ValidateAssignConfig(&AssignConfig{IdleThreshold: "banana"}); err == nil {
		t.Error("invalid idle_threshold must be rejected")
	}
	if err := ValidateAssignConfig(&AssignConfig{IdleThreshold: "-2m"}); err == nil {
		t.Error("negative idle_threshold must be rejected")
	}
	if err := ValidateAssignConfig(nil); err != nil {
		t.Errorf("nil config must validate, got %v", err)
	}
}
