package dashboard

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func TestFormatAgeShort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{"negative", -5 * time.Second, "0s"},
		{"zero", 0, "0s"},
		{"1 second", 1 * time.Second, "1s"},
		{"30 seconds", 30 * time.Second, "30s"},
		{"59 seconds", 59 * time.Second, "59s"},
		{"1 minute", 60 * time.Second, "1m"},
		{"90 seconds", 90 * time.Second, "1m"},
		{"5 minutes", 5 * time.Minute, "5m"},
		{"59 minutes", 59 * time.Minute, "59m"},
		{"1 hour", 60 * time.Minute, "1h"},
		{"2 hours", 2 * time.Hour, "2h"},
		{"24 hours", 24 * time.Hour, "24h"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatAgeShort(tc.duration)
			if got != tc.want {
				t.Errorf("formatAgeShort(%v) = %q, want %q", tc.duration, got, tc.want)
			}
		})
	}
}

func TestCopyTimeMap(t *testing.T) {
	t.Parallel()

	t.Run("nil map", func(t *testing.T) {
		got := copyTimeMap(nil)
		if got != nil {
			t.Errorf("copyTimeMap(nil) = %v, want nil", got)
		}
	})

	t.Run("empty map", func(t *testing.T) {
		got := copyTimeMap(map[string]time.Time{})
		if got != nil {
			t.Errorf("copyTimeMap(empty) = %v, want nil", got)
		}
	})

	t.Run("non-empty map", func(t *testing.T) {
		now := time.Now()
		src := map[string]time.Time{
			"a": now,
			"b": now.Add(-time.Hour),
		}

		got := copyTimeMap(src)

		if len(got) != len(src) {
			t.Errorf("len(copyTimeMap) = %d, want %d", len(got), len(src))
		}

		for k, v := range src {
			if gotV, ok := got[k]; !ok {
				t.Errorf("copied map missing key %q", k)
			} else if !gotV.Equal(v) {
				t.Errorf("copied map[%q] = %v, want %v", k, gotV, v)
			}
		}

		// Verify it's a copy, not the same map
		src["c"] = now.Add(time.Hour)
		if _, ok := got["c"]; ok {
			t.Error("modifying source should not affect copy")
		}
	})
}

func TestRefreshDue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		last     func(time.Time) time.Time
		interval time.Duration
		want     bool
	}{
		{"zero interval", func(now time.Time) time.Time { return now }, 0, false},
		{"negative interval", func(now time.Time) time.Time { return now }, -time.Second, false},
		{"zero last time", func(time.Time) time.Time { return time.Time{} }, time.Second, true},
		{"recent last time", func(now time.Time) time.Time { return now.Add(-500 * time.Millisecond) }, time.Second, false},
		{"old last time", func(now time.Time) time.Time { return now.Add(-2 * time.Second) }, time.Second, true},
		{"exact interval", func(now time.Time) time.Time { return now.Add(-time.Second) }, time.Second, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			last := tc.last(time.Now())
			got := refreshDue(last, tc.interval)
			if got != tc.want {
				t.Errorf("refreshDue(%v, %v) = %v, want %v", last, tc.interval, got, tc.want)
			}
		})
	}
}

func TestActivityLabelAndColor(t *testing.T) {
	t.Parallel()

	th := theme.Current()

	tests := []struct {
		name      string
		state     string
		wantLabel string
		wantColor bool // just check that we got a non-zero color
	}{
		{"working", "working", "WORK", true},
		{"Working uppercase", "Working", "WORK", true},
		{"WORKING all caps", "WORKING", "WORK", true},
		{"idle", "idle", "IDLE", true},
		{"error", "error", "ERR", true},
		{"compacted", "compacted", "CMP", true},
		{"rate_limited", "rate_limited", "RATE", true},
		{"unknown", "unknown_state", "UNK", true},
		{"empty", "", "UNK", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			label, color := activityLabelAndColor(tc.state, th)
			if label != tc.wantLabel {
				t.Errorf("activityLabelAndColor(%q).label = %q, want %q", tc.state, label, tc.wantLabel)
			}
			if tc.wantColor && color == "" {
				t.Errorf("activityLabelAndColor(%q).color should not be empty", tc.state)
			}
		})
	}
}

// TestTruncate already exists in dashboard_layout_test.go

func TestActivityBadge(t *testing.T) {
	t.Parallel()

	th := theme.Current()

	tests := []struct {
		name  string
		state string
	}{
		{"working", "working"},
		{"idle", "idle"},
		{"error", "error"},
		{"unknown", "unknown"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := activityBadge(tc.state, th)
			// Just verify it returns something (the badge rendering is complex)
			if result == "" {
				t.Errorf("activityBadge(%q) returned empty string", tc.state)
			}
		})
	}
}

func TestActivityCountBadge(t *testing.T) {
	t.Parallel()

	th := theme.Current()

	t.Run("zero count", func(t *testing.T) {
		result := activityCountBadge("working", 0, th)
		if result != "" {
			t.Errorf("activityCountBadge with count=0 should return empty, got %q", result)
		}
	})

	t.Run("negative count", func(t *testing.T) {
		result := activityCountBadge("working", -1, th)
		if result != "" {
			t.Errorf("activityCountBadge with count=-1 should return empty, got %q", result)
		}
	})

	t.Run("positive count", func(t *testing.T) {
		result := activityCountBadge("working", 5, th)
		if result == "" {
			t.Error("activityCountBadge with positive count should return non-empty")
		}
	})
}
