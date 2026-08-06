package robot

import (
	"context"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// =============================================================================
// Unit tests for pane activity indicators (bd-3v1w7)
// =============================================================================

func TestClassifyActivity_Active(t *testing.T) {
	status := ClassifyActivity(5*time.Second, 30*time.Second, 2*time.Minute)
	if status != StatusActive {
		t.Errorf("expected StatusActive, got %s", status)
	}
}

func TestClassifyActivity_ActiveAtBoundary(t *testing.T) {
	status := ClassifyActivity(30*time.Second, 30*time.Second, 2*time.Minute)
	if status != StatusActive {
		t.Errorf("expected StatusActive at exact boundary, got %s", status)
	}
}

func TestClassifyActivity_Idle(t *testing.T) {
	status := ClassifyActivity(60*time.Second, 30*time.Second, 2*time.Minute)
	if status != StatusIdle {
		t.Errorf("expected StatusIdle, got %s", status)
	}
}

func TestClassifyActivity_Stalled(t *testing.T) {
	status := ClassifyActivity(3*time.Minute, 30*time.Second, 2*time.Minute)
	if status != StatusStalled {
		t.Errorf("expected StatusStalled, got %s", status)
	}
}

func TestClassifyActivity_StalledAtBoundary(t *testing.T) {
	status := ClassifyActivity(2*time.Minute, 30*time.Second, 2*time.Minute)
	if status != StatusStalled {
		t.Errorf("expected StatusStalled at exact boundary, got %s", status)
	}
}

func TestClassifyActivity_JustBelowStalled(t *testing.T) {
	status := ClassifyActivity(2*time.Minute-time.Second, 30*time.Second, 2*time.Minute)
	if status != StatusIdle {
		t.Errorf("expected StatusIdle just below stalled threshold, got %s", status)
	}
}

func TestClassifyActivity_ZeroDuration(t *testing.T) {
	status := ClassifyActivity(0, 30*time.Second, 2*time.Minute)
	if status != StatusActive {
		t.Errorf("expected StatusActive for zero duration, got %s", status)
	}
}

func TestDefaultIndicatorConfig(t *testing.T) {
	cfg := DefaultIndicatorConfig()

	if cfg.PollInterval != 10*time.Second {
		t.Errorf("expected PollInterval 10s, got %v", cfg.PollInterval)
	}
	if cfg.ActiveThreshold != 30*time.Second {
		t.Errorf("expected ActiveThreshold 30s, got %v", cfg.ActiveThreshold)
	}
	if cfg.StalledThreshold != 2*time.Minute {
		t.Errorf("expected StalledThreshold 2m, got %v", cfg.StalledThreshold)
	}
	if cfg.ColorActive != "#00ff00" {
		t.Errorf("expected ColorActive #00ff00, got %s", cfg.ColorActive)
	}
	if cfg.ColorIdle != "#ffff00" {
		t.Errorf("expected ColorIdle #ffff00, got %s", cfg.ColorIdle)
	}
	if cfg.ColorStalled != "#ff0000" {
		t.Errorf("expected ColorStalled #ff0000, got %s", cfg.ColorStalled)
	}
}

func TestNewPaneIndicator_DefaultFill(t *testing.T) {
	pi := NewPaneIndicator(IndicatorConfig{Session: "test"})

	if pi.config.PollInterval != 10*time.Second {
		t.Errorf("expected default PollInterval 10s, got %v", pi.config.PollInterval)
	}
	if pi.config.ActiveThreshold != 30*time.Second {
		t.Errorf("expected default ActiveThreshold 30s, got %v", pi.config.ActiveThreshold)
	}
	if pi.config.StalledThreshold != 2*time.Minute {
		t.Errorf("expected default StalledThreshold 2m, got %v", pi.config.StalledThreshold)
	}
}

func TestNewPaneIndicator_CustomThresholds(t *testing.T) {
	pi := NewPaneIndicator(IndicatorConfig{
		Session:          "test",
		PollInterval:     5 * time.Second,
		ActiveThreshold:  15 * time.Second,
		StalledThreshold: 1 * time.Minute,
	})

	if pi.config.PollInterval != 5*time.Second {
		t.Errorf("expected PollInterval 5s, got %v", pi.config.PollInterval)
	}
	if pi.config.ActiveThreshold != 15*time.Second {
		t.Errorf("expected ActiveThreshold 15s, got %v", pi.config.ActiveThreshold)
	}
	if pi.config.StalledThreshold != 1*time.Minute {
		t.Errorf("expected StalledThreshold 1m, got %v", pi.config.StalledThreshold)
	}
}

func TestNewPaneIndicator_EnforcesInvariant(t *testing.T) {
	// If ActiveThreshold >= StalledThreshold, StalledThreshold gets adjusted.
	pi := NewPaneIndicator(IndicatorConfig{
		Session:          "test",
		ActiveThreshold:  5 * time.Minute,
		StalledThreshold: 1 * time.Minute, // less than active
	})

	if pi.config.StalledThreshold <= pi.config.ActiveThreshold {
		t.Errorf("expected StalledThreshold > ActiveThreshold; got stalled=%v active=%v",
			pi.config.StalledThreshold, pi.config.ActiveThreshold)
	}
}

func TestNewPaneIndicator_MinPollInterval(t *testing.T) {
	pi := NewPaneIndicator(IndicatorConfig{
		Session:      "test",
		PollInterval: 100 * time.Millisecond, // below 1s minimum
	})

	if pi.config.PollInterval < time.Second {
		t.Errorf("expected PollInterval >= 1s, got %v", pi.config.PollInterval)
	}
}

func TestPaneIndicator_ColorForStatus(t *testing.T) {
	pi := NewPaneIndicator(IndicatorConfig{Session: "test"})

	tests := []struct {
		status ActivityStatus
		want   string
	}{
		{StatusActive, "#00ff00"},
		{StatusIdle, "#ffff00"},
		{StatusStalled, "#ff0000"},
		{ActivityStatus("unknown"), "#ffff00"}, // default to idle color
	}

	for _, tt := range tests {
		got := pi.ColorForStatus(tt.status)
		if got != tt.want {
			t.Errorf("ColorForStatus(%s) = %s, want %s", tt.status, got, tt.want)
		}
	}
}

func TestPaneIndicator_CustomColors(t *testing.T) {
	pi := NewPaneIndicator(IndicatorConfig{
		Session:      "test",
		ColorActive:  "#00ff88",
		ColorIdle:    "#ffaa00",
		ColorStalled: "#ff0088",
	})

	if pi.ColorForStatus(StatusActive) != "#00ff88" {
		t.Error("custom active color not applied")
	}
	if pi.ColorForStatus(StatusIdle) != "#ffaa00" {
		t.Error("custom idle color not applied")
	}
	if pi.ColorForStatus(StatusStalled) != "#ff0088" {
		t.Error("custom stalled color not applied")
	}
}

func TestPaneIndicator_GetStatus_UntrackedPane(t *testing.T) {
	pi := NewPaneIndicator(IndicatorConfig{Session: "test"})

	// Untracked panes should report StatusActive (optimistic default).
	status := pi.GetStatus("%99")
	if status != StatusActive {
		t.Errorf("expected StatusActive for untracked pane, got %s", status)
	}
}

func TestPaneIndicator_GetAllStatuses_Empty(t *testing.T) {
	pi := NewPaneIndicator(IndicatorConfig{Session: "test"})

	statuses := pi.GetAllStatuses()
	if len(statuses) != 0 {
		t.Errorf("expected empty map for fresh indicator, got %d entries", len(statuses))
	}
}

func TestPaneIndicator_RunOnceCanceledDoesNotEnumeratePanes(t *testing.T) {
	originalGetPanes := indicatorGetPanes
	t.Cleanup(func() { indicatorGetPanes = originalGetPanes })

	calls := 0
	indicatorGetPanes = func(string) ([]tmux.Pane, error) {
		calls++
		return nil, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	NewPaneIndicator(IndicatorConfig{Session: "project"}).RunOnce(ctx)
	if calls != 0 {
		t.Fatalf("canceled RunOnce enumerated panes %d time(s)", calls)
	}
}

func TestHashContent_Deterministic(t *testing.T) {
	h1 := hashContent("hello world")
	h2 := hashContent("hello world")
	if h1 != h2 {
		t.Errorf("hashContent not deterministic: %s != %s", h1, h2)
	}
}

func TestHashContent_Different(t *testing.T) {
	h1 := hashContent("hello")
	h2 := hashContent("world")
	if h1 == h2 {
		t.Error("hashContent produced same hash for different inputs")
	}
}

func TestHashContent_Empty(t *testing.T) {
	h := hashContent("")
	if h == "" {
		t.Error("hashContent returned empty string for empty input")
	}
}

func TestActivityStatus_StringValues(t *testing.T) {
	if string(StatusActive) != "active" {
		t.Errorf("StatusActive = %q, want 'active'", StatusActive)
	}
	if string(StatusIdle) != "idle" {
		t.Errorf("StatusIdle = %q, want 'idle'", StatusIdle)
	}
	if string(StatusStalled) != "stalled" {
		t.Errorf("StatusStalled = %q, want 'stalled'", StatusStalled)
	}
}

func TestColorConstants(t *testing.T) {
	if ColorActive != "#00ff00" {
		t.Errorf("ColorActive = %q, want '#00ff00'", ColorActive)
	}
	if ColorIdle != "#ffff00" {
		t.Errorf("ColorIdle = %q, want '#ffff00'", ColorIdle)
	}
	if ColorStalled != "#ff0000" {
		t.Errorf("ColorStalled = %q, want '#ff0000'", ColorStalled)
	}
}

func TestClassifyActivity_CustomThresholds(t *testing.T) {
	// With 10s active, 60s stalled
	cases := []struct {
		name   string
		since  time.Duration
		active time.Duration
		stall  time.Duration
		want   ActivityStatus
	}{
		{"active_custom", 5 * time.Second, 10 * time.Second, 60 * time.Second, StatusActive},
		{"idle_custom", 30 * time.Second, 10 * time.Second, 60 * time.Second, StatusIdle},
		{"stalled_custom", 90 * time.Second, 10 * time.Second, 60 * time.Second, StatusStalled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyActivity(tc.since, tc.active, tc.stall)
			if got != tc.want {
				t.Errorf("ClassifyActivity(%v, %v, %v) = %s, want %s",
					tc.since, tc.active, tc.stall, got, tc.want)
			}
		})
	}
}

func TestPaneIndicator_ConfigPreservesExplicitValues(t *testing.T) {
	pi := NewPaneIndicator(IndicatorConfig{
		Session:          "mysession",
		PollInterval:     3 * time.Second,
		ActiveThreshold:  20 * time.Second,
		StalledThreshold: 3 * time.Minute,
		ColorActive:      "#11ff11",
		ColorIdle:        "#ffff11",
		ColorStalled:     "#ff1111",
		LinesCaptured:    50,
	})

	if pi.config.Session != "mysession" {
		t.Errorf("session not preserved: %s", pi.config.Session)
	}
	if pi.config.PollInterval != 3*time.Second {
		t.Errorf("PollInterval not preserved: %v", pi.config.PollInterval)
	}
	if pi.config.ActiveThreshold != 20*time.Second {
		t.Errorf("ActiveThreshold not preserved: %v", pi.config.ActiveThreshold)
	}
	if pi.config.StalledThreshold != 3*time.Minute {
		t.Errorf("StalledThreshold not preserved: %v", pi.config.StalledThreshold)
	}
	if pi.config.ColorActive != "#11ff11" {
		t.Errorf("ColorActive not preserved: %s", pi.config.ColorActive)
	}
	if pi.config.ColorIdle != "#ffff11" {
		t.Errorf("ColorIdle not preserved: %s", pi.config.ColorIdle)
	}
	if pi.config.ColorStalled != "#ff1111" {
		t.Errorf("ColorStalled not preserved: %s", pi.config.ColorStalled)
	}
	if pi.config.LinesCaptured != 50 {
		t.Errorf("LinesCaptured not preserved: %d", pi.config.LinesCaptured)
	}
}
