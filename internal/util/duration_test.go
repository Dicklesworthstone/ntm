package util

import (
	"bytes"
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
		wantErr  bool
	}{
		// Simple units
		{"30s", 30 * time.Second, false},
		{"5m", 5 * time.Minute, false},
		{"2h", 2 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"2w", 14 * 24 * time.Hour, false},

		// Milliseconds (standard Go format)
		{"500ms", 500 * time.Millisecond, false},
		{"5000ms", 5 * time.Second, false},

		// Standard Go compound durations
		{"1h30m", 90 * time.Minute, false},
		{"2h30m15s", 2*time.Hour + 30*time.Minute + 15*time.Second, false},

		// Edge cases
		{"0s", 0, false},
		{"1s", time.Second, false},

		// Errors
		{"", 0, true},
		{"s", 0, true},
		{"abc", 0, true},
		{"-1s", -time.Second, false}, // Go allows negative durations
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got, err := ParseDuration(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("ParseDuration(%q) expected error, got %v", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Errorf("ParseDuration(%q) unexpected error: %v", tc.input, err)
				return
			}
			if got != tc.expected {
				t.Errorf("ParseDuration(%q) = %v, want %v", tc.input, got, tc.expected)
			}
		})
	}
}

func TestParseDurationWithDefault(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		defaultUnit time.Duration
		flagName    string
		expected    time.Duration
		wantErr     bool
		wantWarning bool
	}{
		{
			name:        "explicit seconds",
			input:       "30s",
			defaultUnit: time.Second,
			flagName:    "timeout",
			expected:    30 * time.Second,
			wantWarning: false,
		},
		{
			name:        "explicit milliseconds",
			input:       "5000ms",
			defaultUnit: time.Millisecond,
			flagName:    "ack-timeout",
			expected:    5 * time.Second,
			wantWarning: false,
		},
		{
			name:        "bare number with seconds default",
			input:       "30",
			defaultUnit: time.Second,
			flagName:    "spawn-timeout",
			expected:    30 * time.Second,
			wantWarning: true,
		},
		{
			name:        "bare number with milliseconds default",
			input:       "5000",
			defaultUnit: time.Millisecond,
			flagName:    "ack-timeout",
			expected:    5 * time.Second,
			wantWarning: true,
		},
		{
			name:        "invalid string",
			input:       "abc",
			defaultUnit: time.Second,
			flagName:    "timeout",
			wantErr:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Capture slog output for deprecation warning tests
			r, w, _ := os.Pipe()
			oldLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelWarn})))

			got, err := ParseDurationWithDefault(tc.input, tc.defaultUnit, tc.flagName)

			// Restore logger before reading pipe
			slog.SetDefault(oldLogger)

			// Close write end and read output
			w.Close()
			var buf bytes.Buffer
			buf.ReadFrom(r)
			warning := buf.String()

			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if got != tc.expected {
				t.Errorf("got %v, want %v", got, tc.expected)
			}
			if tc.wantWarning && warning == "" {
				t.Error("expected deprecation warning, got none")
			}
			if !tc.wantWarning && warning != "" {
				t.Errorf("unexpected warning: %s", warning)
			}
		})
	}
}

func TestMustParseDuration(t *testing.T) {
	t.Run("valid duration", func(t *testing.T) {
		d := MustParseDuration("30s")
		if d != 30*time.Second {
			t.Errorf("got %v, want 30s", d)
		}
	})

	t.Run("panics on invalid", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic for invalid duration")
			}
		}()
		MustParseDuration("invalid")
	})
}

func TestSuggestUnit(t *testing.T) {
	tests := []struct {
		input    time.Duration
		expected string
	}{
		{time.Millisecond, "ms"},
		{time.Second, "s"},
		{time.Minute, "m"},
		{time.Hour, "h"},
		{24 * time.Hour, "s"}, // Unknown, defaults to seconds
	}

	for _, tc := range tests {
		got := suggestUnit(tc.input)
		if got != tc.expected {
			t.Errorf("suggestUnit(%v) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}
