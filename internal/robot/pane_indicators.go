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
	"context"
	"fmt"
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

// DefaultIndicatorConfig returns sensible defaults for the indicator system.
func DefaultIndicatorConfig() IndicatorConfig {
	return IndicatorConfig{
		PollInterval:     10 * time.Second,
		ActiveThreshold:  30 * time.Second,
		StalledThreshold: 2 * time.Minute,
		ColorActive:      ColorActive,
		ColorIdle:        ColorIdle,
		ColorStalled:     ColorStalled,
		LinesCaptured:    tmux.LinesStatusDetection,
	}
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

// NewPaneIndicator creates a PaneIndicator with the given configuration.
// Missing fields in config are filled from DefaultIndicatorConfig.
func NewPaneIndicator(config IndicatorConfig) *PaneIndicator {
	defaults := DefaultIndicatorConfig()

	if config.PollInterval < time.Second {
		config.PollInterval = defaults.PollInterval
	}
	if config.ActiveThreshold <= 0 {
		config.ActiveThreshold = defaults.ActiveThreshold
	}
	if config.StalledThreshold <= 0 {
		config.StalledThreshold = defaults.StalledThreshold
	}
	if config.ColorActive == "" {
		config.ColorActive = defaults.ColorActive
	}
	if config.ColorIdle == "" {
		config.ColorIdle = defaults.ColorIdle
	}
	if config.ColorStalled == "" {
		config.ColorStalled = defaults.ColorStalled
	}
	if config.LinesCaptured <= 0 {
		config.LinesCaptured = defaults.LinesCaptured
	}
	// Enforce invariant: active < stalled
	if config.ActiveThreshold >= config.StalledThreshold {
		config.StalledThreshold = config.ActiveThreshold + time.Minute
	}

	return &PaneIndicator{
		config: config,
		states: make(map[string]*paneIndicatorState),
	}
}

// Run starts the indicator polling loop. Blocks until ctx is cancelled.
func (pi *PaneIndicator) Run(ctx context.Context) error {
	ticker := time.NewTicker(pi.config.PollInterval)
	defer ticker.Stop()

	// Perform an initial check immediately.
	pi.updateAll(ctx)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			pi.updateAll(ctx)
		}
	}
}

// RunOnce performs a single indicator update pass (useful for testing).
func (pi *PaneIndicator) RunOnce(ctx context.Context) {
	pi.updateAll(ctx)
}

// GetStatus returns the current status for a pane target, or StatusActive
// if the pane is not yet tracked.
func (pi *PaneIndicator) GetStatus(target string) ActivityStatus {
	pi.mu.Lock()
	defer pi.mu.Unlock()

	st, ok := pi.states[target]
	if !ok {
		return StatusActive
	}
	return st.currentStatus
}

// GetAllStatuses returns a snapshot of all tracked pane statuses.
func (pi *PaneIndicator) GetAllStatuses() map[string]ActivityStatus {
	pi.mu.Lock()
	defer pi.mu.Unlock()

	result := make(map[string]ActivityStatus, len(pi.states))
	for target, st := range pi.states {
		result[target] = st.currentStatus
	}
	return result
}

// ClassifyActivity determines the ActivityStatus based on how long ago
// the pane last changed content.
func ClassifyActivity(sinceLastChange time.Duration, activeThreshold, stalledThreshold time.Duration) ActivityStatus {
	switch {
	case sinceLastChange <= activeThreshold:
		return StatusActive
	case sinceLastChange >= stalledThreshold:
		return StatusStalled
	default:
		return StatusIdle
	}
}

// ColorForStatus returns the configured color for the given status.
func (pi *PaneIndicator) ColorForStatus(status ActivityStatus) string {
	switch status {
	case StatusActive:
		return pi.config.ColorActive
	case StatusIdle:
		return pi.config.ColorIdle
	case StatusStalled:
		return pi.config.ColorStalled
	default:
		return pi.config.ColorIdle
	}
}

// updateAll enumerates panes and updates their indicators.
func (pi *PaneIndicator) updateAll(ctx context.Context) {
	// getPanes ultimately uses the default tmux command timeout, not this
	// caller's context. Do not start that uncancelable enumeration after the
	// caller has already abandoned the update pass.
	if ctx == nil || ctx.Err() != nil {
		return
	}

	panes, err := pi.getPanes()
	if err != nil {
		return // best-effort; skip this cycle
	}

	for _, pane := range panes {
		if ctx.Err() != nil {
			return
		}
		pi.updatePane(ctx, pane)
	}
}

// getPanes returns the pane list to monitor.
func (pi *PaneIndicator) getPanes() ([]tmux.Pane, error) {
	allPanes, err := indicatorGetPanes(pi.config.Session)
	if err != nil {
		return nil, err
	}

	if len(pi.config.Panes) > 0 {
		indexSet := make(map[int]struct{}, len(pi.config.Panes))
		for _, idx := range pi.config.Panes {
			indexSet[idx] = struct{}{}
		}
		var selected []tmux.Pane
		for _, p := range allPanes {
			if _, ok := indexSet[p.Index]; ok {
				selected = append(selected, p)
			}
		}
		return selected, nil
	}

	// Default: all non-control panes (skip first/lowest index).
	minIdx := -1
	for _, p := range allPanes {
		if minIdx == -1 || p.Index < minIdx {
			minIdx = p.Index
		}
	}
	var agentPanes []tmux.Pane
	for _, p := range allPanes {
		if p.Index != minIdx {
			agentPanes = append(agentPanes, p)
		}
	}
	return agentPanes, nil
}

// updatePane captures content, detects changes, and updates the border color.
func (pi *PaneIndicator) updatePane(ctx context.Context, pane tmux.Pane) {
	target := pane.ID
	if target == "" {
		firstWin, err := tmux.GetFirstWindow(pi.config.Session)
		if err != nil {
			return
		}
		target = fmt.Sprintf("%s:%d.%d", pi.config.Session, firstWin, pane.Index)
	}

	// Capture pane content.
	output, err := tmux.CapturePaneOutputContext(ctx, target, pi.config.LinesCaptured)
	if err != nil {
		return // best-effort
	}

	now := time.Now()
	// Reuse the existing hashContent from probe.go (same package).
	contentHash := hashContent(output)

	pi.mu.Lock()
	st, exists := pi.states[target]
	if !exists {
		st = &paneIndicatorState{
			lastContentHash: contentHash,
			lastChangeTime:  now,
			currentStatus:   StatusActive,
		}
		pi.states[target] = st
	}

	// Detect content change.
	if contentHash != st.lastContentHash {
		st.lastContentHash = contentHash
		st.lastChangeTime = now
	}

	sinceChange := now.Sub(st.lastChangeTime)
	newStatus := ClassifyActivity(sinceChange, pi.config.ActiveThreshold, pi.config.StalledThreshold)
	changed := newStatus != st.currentStatus
	st.currentStatus = newStatus
	pi.mu.Unlock()

	// Update tmux border only if the status actually changed or on first observation.
	if changed || !exists {
		color := pi.ColorForStatus(newStatus)
		_ = tmux.SetPaneBorderStyleContext(ctx, target, color)
	}
}

// ResetAll resets the border style for all tracked panes back to default.
func (pi *PaneIndicator) ResetAll(ctx context.Context) {
	pi.mu.Lock()
	targets := make([]string, 0, len(pi.states))
	for t := range pi.states {
		targets = append(targets, t)
	}
	pi.states = make(map[string]*paneIndicatorState)
	pi.mu.Unlock()

	for _, t := range targets {
		_ = tmux.ResetPaneBorderStyleContext(ctx, t)
	}
}
