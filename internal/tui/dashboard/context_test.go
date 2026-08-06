package dashboard

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func TestFormatTokenDisplay(t *testing.T) {

	tests := []struct {
		name     string
		used     int
		limit    int
		expected string
	}{
		{
			name:     "small numbers",
			used:     500,
			limit:    1000,
			expected: "500 / 1.0K",
		},
		{
			name:     "thousands",
			used:     142500,
			limit:    200000,
			expected: "142.5K / 200.0K",
		},
		{
			name:     "millions",
			used:     1500000,
			limit:    2000000,
			expected: "1.5M / 2.0M",
		},
		{
			name:     "mixed",
			used:     50000,
			limit:    1000000,
			expected: "50.0K / 1.0M",
		},
		{
			name:     "under 1K",
			used:     100,
			limit:    500,
			expected: "100 / 500",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			result := formatTokenDisplay(tc.used, tc.limit)
			if result != tc.expected {
				t.Errorf("formatTokenDisplay(%d, %d) = %q, want %q", tc.used, tc.limit, result, tc.expected)
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {

	tests := []struct {
		name     string
		duration time.Duration
		expected string
	}{
		{
			name:     "seconds",
			duration: 45 * time.Second,
			expected: "45s",
		},
		{
			name:     "minutes",
			duration: 2 * time.Minute,
			expected: "2m",
		},
		{
			name:     "hours",
			duration: 3 * time.Hour,
			expected: "3h",
		},
		{
			name:     "under a minute",
			duration: 30 * time.Second,
			expected: "30s",
		},
		{
			name:     "minute boundary",
			duration: 59 * time.Second,
			expected: "59s",
		},
		{
			name:     "just over a minute",
			duration: 61 * time.Second,
			expected: "1m",
		},
		{
			name:     "hour boundary",
			duration: 59 * time.Minute,
			expected: "59m",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			result := formatDuration(tc.duration)
			if result != tc.expected {
				t.Errorf("formatDuration(%v) = %q, want %q", tc.duration, result, tc.expected)
			}
		})
	}
}

func TestPaneStatusRotationFields(t *testing.T) {

	// Test that PaneStatus has the rotation fields
	ps := PaneStatus{
		State:          "running",
		ContextTokens:  150000,
		ContextLimit:   200000,
		ContextPercent: 75.0,
		IsRotating:     true,
	}

	if !ps.IsRotating {
		t.Error("expected IsRotating to be true")
	}

	now := time.Now()
	ps.RotatedAt = &now
	if ps.RotatedAt == nil {
		t.Error("expected RotatedAt to be set")
	}
}

func TestRenderContextBar(t *testing.T) {

	m := New("session", "")
	m.width = 120
	m.height = 30
	m.tier = layout.TierForWidth(m.width)

	tests := []struct {
		name    string
		percent float64
		width   int
	}{
		{
			name:    "low usage",
			percent: 25.0,
			width:   20,
		},
		{
			name:    "medium usage",
			percent: 50.0,
			width:   20,
		},
		{
			name:    "high usage",
			percent: 75.0,
			width:   20,
		},
		{
			name:    "critical usage",
			percent: 90.0,
			width:   20,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			result := m.renderContextBar(tc.percent, tc.width)
			// Just verify it returns a non-empty string
			if result == "" {
				t.Errorf("renderContextBar(%.1f, %d) returned empty string", tc.percent, tc.width)
			}
		})
	}
}

func TestContextUsagePaletteThresholds(t *testing.T) {
	currentTheme := theme.CatppuccinMocha
	tests := []struct {
		name    string
		percent float64
		want    []string
	}{
		{name: "green below sixty", percent: 59.9, want: []string{string(currentTheme.Green), string(currentTheme.Green)}},
		{name: "yellow at sixty", percent: 60, want: []string{string(currentTheme.Yellow), string(currentTheme.Yellow)}},
		{name: "orange at eighty", percent: 80, want: []string{string(currentTheme.Peach), string(currentTheme.Peach)}},
		{name: "orange at ninety-five", percent: 95, want: []string{string(currentTheme.Peach), string(currentTheme.Peach)}},
		{name: "red above ninety-five", percent: 95.1, want: []string{string(currentTheme.Red), string(currentTheme.Maroon)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := contextUsagePalette(tt.percent, currentTheme); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("contextUsagePalette(%v) = %v, want %v", tt.percent, got, tt.want)
			}
		})
	}
}

func TestRenderContextBarNarrowWidth(t *testing.T) {
	m := New("session", "")

	got := status.StripANSI(m.renderContextBar(71, 1))
	if !strings.Contains(got, "71%") {
		t.Fatalf("narrow context bar = %q, want percentage", got)
	}
}

func TestPaneStatusWithContextData(t *testing.T) {

	// Test that PaneStatus correctly stores context data
	ps := PaneStatus{
		State:          "running",
		ContextTokens:  142500,
		ContextLimit:   200000,
		ContextPercent: 71.25,
	}

	if ps.ContextTokens != 142500 {
		t.Errorf("expected ContextTokens=142500, got %d", ps.ContextTokens)
	}
	if ps.ContextLimit != 200000 {
		t.Errorf("expected ContextLimit=200000, got %d", ps.ContextLimit)
	}
	if ps.ContextPercent != 71.25 {
		t.Errorf("expected ContextPercent=71.25, got %f", ps.ContextPercent)
	}

	// Verify formatTokenDisplay works with these values
	display := formatTokenDisplay(ps.ContextTokens, ps.ContextLimit)
	expected := "142.5K / 200.0K"
	if display != expected {
		t.Errorf("formatTokenDisplay(%d, %d) = %q, want %q",
			ps.ContextTokens, ps.ContextLimit, display, expected)
	}
}

func TestPaneStatusRotationState(t *testing.T) {

	// Test rotation in progress
	psRotating := PaneStatus{
		State:      "running",
		IsRotating: true,
	}
	if !psRotating.IsRotating {
		t.Error("expected IsRotating=true")
	}

	// Test recently rotated
	rotatedTime := time.Now().Add(-2 * time.Minute)
	psRotated := PaneStatus{
		State:      "running",
		IsRotating: false,
		RotatedAt:  &rotatedTime,
	}
	if psRotated.IsRotating {
		t.Error("expected IsRotating=false after rotation completes")
	}
	if psRotated.RotatedAt == nil {
		t.Error("expected RotatedAt to be set")
	}

	// Verify formatDuration works for the elapsed time
	elapsed := time.Since(*psRotated.RotatedAt)
	formatted := formatDuration(elapsed)
	// Should be around "2m" (with slight timing variance)
	if !strings.HasSuffix(formatted, "m") && !strings.HasSuffix(formatted, "s") {
		t.Errorf("formatDuration(%v) = %q, expected minutes or seconds suffix", elapsed, formatted)
	}
}
