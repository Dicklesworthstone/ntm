// Package util provides shared utility functions for ntm.
package util

import (
	"fmt"
	"log/slog"
	"strconv"
	"time"
)

// ParseDuration parses human-friendly duration strings.
// Supports: 30s, 5m, 1h, 1d, 1w and standard Go durations (e.g., 1h30m).
//
// Examples:
//   - "30s"  -> 30 seconds
//   - "5m"   -> 5 minutes
//   - "1h"   -> 1 hour
//   - "1d"   -> 24 hours
//   - "1w"   -> 7 days
//   - "1h30m" -> 1 hour 30 minutes (standard Go format)
func ParseDuration(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("invalid duration: %s", s)
	}

	unit := s[len(s)-1]
	value, err := strconv.Atoi(s[:len(s)-1])
	if err != nil {
		// Not a simple unit, try standard Go duration
		return time.ParseDuration(s)
	}

	switch unit {
	case 's':
		return time.Duration(value) * time.Second, nil
	case 'm':
		return time.Duration(value) * time.Minute, nil
	case 'h':
		return time.Duration(value) * time.Hour, nil
	case 'd':
		return time.Duration(value) * 24 * time.Hour, nil
	case 'w':
		return time.Duration(value) * 7 * 24 * time.Hour, nil
	default:
		// Try standard Go duration as fallback
		return time.ParseDuration(s)
	}
}

// ParseDurationWithDefault parses a duration string, handling bare numbers with
// a default unit for backward compatibility. Emits a deprecation warning to stderr
// when bare numbers are used.
//
// Parameters:
//   - s: the duration string to parse
//   - defaultUnit: the unit to use for bare numbers (e.g., time.Second, time.Millisecond)
//   - flagName: the flag name for the deprecation warning message
//
// Examples:
//
//	ParseDurationWithDefault("30s", time.Second, "timeout")     -> 30s, no warning
//	ParseDurationWithDefault("5000ms", time.Millisecond, "timeout") -> 5s, no warning
//	ParseDurationWithDefault("30", time.Second, "timeout")      -> 30s, warning printed
func ParseDurationWithDefault(s string, defaultUnit time.Duration, flagName string) (time.Duration, error) {
	// Try standard parse first
	if d, err := ParseDuration(s); err == nil {
		return d, nil
	}

	// Try bare number with deprecation warning
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration: %s (use units like 30s, 5m, 1h)", s)
	}

	// Emit deprecation warning
	unitName := suggestUnit(defaultUnit)
	slog.Warn("bare number duration is deprecated",
		"value", s, "flag", flagName, "suggested", fmt.Sprintf("--%s=%d%s", flagName, n, unitName))

	return time.Duration(n) * defaultUnit, nil
}

// MustParseDuration parses a duration string or panics.
// Use only for compile-time constants or values that are guaranteed to be valid.
func MustParseDuration(s string) time.Duration {
	d, err := ParseDuration(s)
	if err != nil {
		panic(fmt.Sprintf("invalid duration %q: %v", s, err))
	}
	return d
}

// suggestUnit returns the short unit suffix for a time.Duration.
func suggestUnit(d time.Duration) string {
	switch d {
	case time.Millisecond:
		return "ms"
	case time.Second:
		return "s"
	case time.Minute:
		return "m"
	case time.Hour:
		return "h"
	default:
		return "s" // Default to seconds
	}
}
