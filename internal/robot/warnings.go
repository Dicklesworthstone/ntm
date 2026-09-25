// Package robot provides machine-readable output for AI agents.
// warnings.go defines warning types and levels for the proactive monitoring system.
package robot

import (
	"fmt"
	"time"
)

// =============================================================================
// Proactive Warning System (bd-3gh5m)
// =============================================================================
//
// Warning levels and types for proactive usage limit detection.
// Warnings are emitted as JSONL for easy parsing by controllers.

// WarningLevel indicates the severity of a monitoring event.
type WarningLevel string

const (
	LevelInfo     WarningLevel = "INFO"     // Context < 40% - awareness
	LevelWarning  WarningLevel = "WARNING"  // Context < 25% - prepare for restart
	LevelCritical WarningLevel = "CRITICAL" // Context < 15% - restart soon
	LevelAlert    WarningLevel = "ALERT"    // Rate limit hit or provider > 80%
)

// Warning represents a single monitoring event emitted as JSONL.
type Warning struct {
	Level            WarningLevel `json:"level"`
	Timestamp        string       `json:"timestamp"`
	Session          string       `json:"session"`
	Pane             int          `json:"pane"`
	AgentType        string       `json:"agent_type"`
	Message          string       `json:"message"`
	ContextRemaining *float64     `json:"context_remaining,omitempty"`
	ContextTrend     string       `json:"context_trend,omitempty"` // declining, stable, rising, unknown
	TrendSamples     int          `json:"trend_samples,omitempty"`
	ProviderUsedPct  *float64     `json:"provider_used_percent,omitempty"`
	Provider         string       `json:"provider,omitempty"`
	SuggestedAction  string       `json:"suggested_action"`
}

// MonitorConfig holds configuration for the monitoring loop.
type MonitorConfig struct {
	Session        string        // Tmux session name (required)
	PaneSelectors  []string      // N, W.P, or %N selectors; takes precedence over Panes
	Panes          []int         // Legacy bare selectors N (empty with no PaneSelectors = all agent panes)
	Interval       time.Duration // Polling interval
	InfoThreshold  float64       // Context % to emit INFO (default: 40)
	WarnThreshold  float64       // Context % to emit WARNING (default: 25)
	CritThreshold  float64       // Context % to emit CRITICAL (default: 15)
	AlertThreshold float64       // Provider % to emit ALERT (default: 80)
	IncludeCaut    bool          // Query caut for provider data
	CautInterval   time.Duration // Caut query interval (slower than main interval)
	OutputFile     string        // Output file path (empty = stdout)
	Daemon         bool          // Run in background
	LinesCaptured  int           // Lines to capture per pane
	Verbose        bool          // Include extra debug info
}

// DefaultMonitorConfig returns sensible defaults for monitoring.
func DefaultMonitorConfig() MonitorConfig {
	return MonitorConfig{
		Interval:       30 * time.Second,
		InfoThreshold:  40.0,
		WarnThreshold:  25.0,
		CritThreshold:  15.0,
		AlertThreshold: 80.0,
		IncludeCaut:    false,
		CautInterval:   2 * time.Minute,
		LinesCaptured:  100,
		Verbose:        false,
	}
}

// getWarningLevel determines the appropriate warning level for a context percentage.
// Returns empty string if no warning should be emitted.
func getWarningLevel(contextPct float64, config MonitorConfig) WarningLevel {
	switch {
	case contextPct < config.CritThreshold:
		return LevelCritical
	case contextPct < config.WarnThreshold:
		return LevelWarning
	case contextPct < config.InfoThreshold:
		return LevelInfo
	default:
		return "" // No warning
	}
}

// getWarningMessage returns the appropriate message for a warning level.
func getWarningMessage(level WarningLevel, threshold float64) string {
	switch level {
	case LevelCritical:
		return formatWarningMessage("Context below %.0f%% threshold", threshold)
	case LevelWarning:
		return formatWarningMessage("Context below %.0f%% threshold", threshold)
	case LevelInfo:
		return formatWarningMessage("Context below %.0f%% threshold", threshold)
	default:
		return ""
	}
}

// getSuggestedAction returns the suggested action for a warning level.
func getSuggestedAction(level WarningLevel) string {
	switch level {
	case LevelCritical:
		return "Restart agent soon"
	case LevelWarning:
		return "Prepare restart, let current task finish"
	case LevelInfo:
		return "Monitor context usage"
	case LevelAlert:
		return "Consider caam account switch"
	default:
		return ""
	}
}

// formatWarningMessage formats a warning message with the threshold value.
func formatWarningMessage(format string, threshold float64) string {
	return fmt.Sprintf(format, threshold)
}

// NewWarning creates a new Warning with the current timestamp.
func NewWarning(level WarningLevel, session string, pane int, agentType, message, suggestedAction string) Warning {
	return Warning{
		Level:           level,
		Timestamp:       time.Now().UTC().Format(time.RFC3339),
		Session:         session,
		Pane:            pane,
		AgentType:       agentType,
		Message:         message,
		SuggestedAction: suggestedAction,
	}
}

// WithContext adds context information to a Warning.
func (w Warning) WithContext(remaining *float64, trend string, samples int) Warning {
	w.ContextRemaining = remaining
	w.ContextTrend = trend
	w.TrendSamples = samples
	return w
}

// WithProvider adds provider information to a Warning.
func (w Warning) WithProvider(provider string, usedPct *float64) Warning {
	w.Provider = provider
	w.ProviderUsedPct = usedPct
	return w
}
