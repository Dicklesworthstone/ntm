// Package robot provides machine-readable output for AI agents and automation.
// pane_indicators.go implements visual stall/activity indicators for tmux pane borders.
//
// Pane borders are color-coded to indicate activity status:
//   - Green (#00ff00): active — output detected within ActiveThreshold (default 30s)
//   - Yellow (#ffff00): idle — no output between ActiveThreshold and StalledThreshold (default 30s–2min)
//   - Red (#ff0000): stalled — no output beyond StalledThreshold (default >2min)
//
// The indicator loop polls pane content hashes at a configurable interval and
// updates tmux border colors only when the status actually changes, minimizing
// both CPU overhead and tmux IPC calls.
package robot

import (
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// =============================================================================
// Visual Pane Activity Indicators (bd-3v1w7)
// =============================================================================

// ActivityStatus represents the activity level of a pane.
type ActivityStatus string

const (
	// StatusActive means the pane has produced output recently.
	StatusActive ActivityStatus = "active"
	// StatusIdle means the pane has not produced output for a moderate period.
	StatusIdle ActivityStatus = "idle"
	// StatusStalled means the pane has not produced output for an extended period.
	StatusStalled ActivityStatus = "stalled"
)

// Border color constants for each activity status.
const (
	ColorActive  = "#00ff00" // green
	ColorIdle    = "#ffff00" // yellow
	ColorStalled = "#ff0000" // red
)

// IndicatorConfig holds configuration for the pane activity indicator system.
type IndicatorConfig struct {
	// Session is the tmux session to monitor (required).
	Session string

	// PollInterval controls how often pane content is checked.
	// Default: 10s. Minimum: 1s.
	PollInterval time.Duration

	// ActiveThreshold is the maximum age of last activity to be considered active.
	// Default: 30s.
	ActiveThreshold time.Duration

	// StalledThreshold is the minimum age of last activity to be considered stalled.
	// Default: 2m.
	StalledThreshold time.Duration

	// ColorActive is the border color for active panes. Default: "#00ff00".
	ColorActive string
	// ColorIdle is the border color for idle panes. Default: "#ffff00".
	ColorIdle string
	// ColorStalled is the border color for stalled panes. Default: "#ff0000".
	ColorStalled string

	// LinesCaptured controls how many lines are captured per poll for hashing.
	// Default: 20 (status detection budget).
	LinesCaptured int

	// Panes restricts monitoring to specific pane indices.
	// Empty means all non-control (non-first) panes.
	Panes []int
}

// paneIndicatorState tracks the per-pane state needed by the indicator loop.
type paneIndicatorState struct {
	lastContentHash string
	lastChangeTime  time.Time
	currentStatus   ActivityStatus
}

// PaneIndicator manages activity indicators for panes in a tmux session.
// It is safe for concurrent use.
type PaneIndicator struct {
	config IndicatorConfig
	states map[string]*paneIndicatorState // keyed by pane target (e.g., "%42")
	mu     sync.Mutex
}

// indicatorGetPanes is kept at the tmux boundary so cancellation behavior can
// be tested without launching a real tmux subprocess.
var indicatorGetPanes = tmux.GetPanes
