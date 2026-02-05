package components

import (
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/status"
)

func TestIsStale(t *testing.T) {
	tests := []struct {
		name     string
		elapsed  time.Duration
		interval time.Duration
		expected bool
	}{
		{
			name:     "zero lastUpdate not stale",
			elapsed:  0, // will use time.Time{}
			interval: 10 * time.Second,
			expected: false,
		},
		{
			name:     "fresh data",
			elapsed:  5 * time.Second,
			interval: 10 * time.Second,
			expected: false,
		},
		{
			name:     "just under 2x interval not stale",
			elapsed:  19 * time.Second,
			interval: 10 * time.Second,
			expected: false,
		},
		{
			name:     "stale data (>2x interval)",
			elapsed:  25 * time.Second,
			interval: 10 * time.Second,
			expected: true,
		},
		{
			name:     "zero interval not stale",
			elapsed:  100 * time.Second,
			interval: 0,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var lastUpdate time.Time
			if tt.elapsed > 0 {
				lastUpdate = time.Now().Add(-tt.elapsed)
			}
			got := IsStale(lastUpdate, tt.interval)
			if got != tt.expected {
				t.Errorf("IsStale() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestRenderFreshnessIndicator(t *testing.T) {
	t.Run("zero lastUpdate returns empty", func(t *testing.T) {
		got := RenderFreshnessIndicator(FreshnessOptions{
			LastUpdate:      time.Time{},
			RefreshInterval: 10 * time.Second,
			Width:           30,
		})
		if got != "" {
			t.Errorf("expected empty string for zero time, got %q", got)
		}
	})

	t.Run("fresh data shows Updated", func(t *testing.T) {
		got := RenderFreshnessIndicator(FreshnessOptions{
			LastUpdate:      time.Now().Add(-5 * time.Second),
			RefreshInterval: 10 * time.Second,
			Width:           30,
		})
		if !strings.Contains(got, "Updated") {
			t.Errorf("expected 'Updated' in output, got %q", got)
		}
	})

	t.Run("shows seconds for recent update", func(t *testing.T) {
		got := RenderFreshnessIndicator(FreshnessOptions{
			LastUpdate:      time.Now().Add(-5 * time.Second),
			RefreshInterval: 10 * time.Second,
			Width:           30,
		})
		if !strings.Contains(got, "5s") && !strings.Contains(got, "ago") {
			t.Errorf("expected time indication, got %q", got)
		}
	})
}

func TestRenderStaleBadge(t *testing.T) {
	t.Run("fresh data returns empty", func(t *testing.T) {
		got := RenderStaleBadge(time.Now().Add(-5*time.Second), 10*time.Second)
		if got != "" {
			t.Errorf("expected empty string for fresh data, got %q", got)
		}
	})

	t.Run("stale data returns badge", func(t *testing.T) {
		got := RenderStaleBadge(time.Now().Add(-25*time.Second), 10*time.Second)
		if !strings.Contains(got, "STALE") {
			t.Errorf("expected STALE badge, got %q", got)
		}
	})
}

func TestRenderFreshnessFooter(t *testing.T) {
	opts := FreshnessOptions{
		LastUpdate:      time.Now().Add(-5 * time.Second),
		RefreshInterval: 10 * time.Second,
		Width:           40,
	}
	indicator := RenderFreshnessIndicator(opts)
	if indicator == "" {
		t.Fatal("expected non-empty indicator")
	}

	out := RenderFreshnessFooter(opts)
	if out == "" {
		t.Fatal("expected non-empty footer")
	}

	indicatorPlain := status.StripANSI(indicator)
	footerPlain := status.StripANSI(out)

	if !strings.Contains(footerPlain, indicatorPlain) {
		t.Fatalf("expected footer to include indicator text, got %q", footerPlain)
	}
	if strings.HasPrefix(footerPlain, indicatorPlain) {
		t.Fatalf("expected footer to be right-aligned with padding, got %q", footerPlain)
	}
}

func TestRenderFreshnessFooterNarrow(t *testing.T) {
	opts := FreshnessOptions{
		LastUpdate:      time.Now().Add(-5 * time.Second),
		RefreshInterval: 10 * time.Second,
		Width:           10,
	}
	indicator := RenderFreshnessIndicator(opts)
	out := RenderFreshnessFooter(opts)

	indicatorPlain := status.StripANSI(indicator)
	footerPlain := status.StripANSI(out)
	if footerPlain != indicatorPlain {
		t.Fatalf("expected narrow footer to equal indicator, got %q", footerPlain)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{name: "now", d: 500 * time.Millisecond, want: "now"},
		{name: "seconds", d: 5 * time.Second, want: "5s"},
		{name: "minutes", d: 2 * time.Minute, want: "2m"},
		{name: "hours", d: 3 * time.Hour, want: "3h"},
		{name: "days", d: 48 * time.Hour, want: "2d"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatDuration(tt.d); got != tt.want {
				t.Fatalf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}
