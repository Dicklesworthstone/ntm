package cli

import (
	"testing"
	"time"
)

func TestFormatIdleDuration(t *testing.T) {

	tests := []struct {
		name     string
		duration time.Duration
		expected string
	}{
		// Less than 1 minute - seconds
		{name: "0 seconds", duration: 0, expected: "0s"},
		{name: "1 second", duration: 1 * time.Second, expected: "1s"},
		{name: "30 seconds", duration: 30 * time.Second, expected: "30s"},
		{name: "59 seconds", duration: 59 * time.Second, expected: "59s"},

		// 1 minute to less than 1 hour - minutes
		{name: "1 minute", duration: 1 * time.Minute, expected: "1m"},
		{name: "5 minutes", duration: 5 * time.Minute, expected: "5m"},
		{name: "30 minutes", duration: 30 * time.Minute, expected: "30m"},
		{name: "59 minutes", duration: 59 * time.Minute, expected: "59m"},
		{name: "59 min 59 sec", duration: 59*time.Minute + 59*time.Second, expected: "59m"},

		// 1+ hours - hours and minutes
		{name: "1 hour", duration: 1 * time.Hour, expected: "1h0m"},
		{name: "1 hour 30 min", duration: 1*time.Hour + 30*time.Minute, expected: "1h30m"},
		{name: "2 hours", duration: 2 * time.Hour, expected: "2h0m"},
		{name: "2 hours 15 min", duration: 2*time.Hour + 15*time.Minute, expected: "2h15m"},
		{name: "24 hours", duration: 24 * time.Hour, expected: "24h0m"},
		{name: "48 hours", duration: 48 * time.Hour, expected: "48h0m"},
		{name: "100 hours 45 min", duration: 100*time.Hour + 45*time.Minute, expected: "100h45m"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := formatIdleDuration(tc.duration)
			if result != tc.expected {
				t.Errorf("formatIdleDuration(%v) = %q; want %q", tc.duration, result, tc.expected)
			}
		})
	}
}
