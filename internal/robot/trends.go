// Package robot provides machine-readable output for AI agents.
// trends.go implements trend tracking for context usage analysis.
package robot

import (
	"sync"
	"time"
)

// =============================================================================
// Trend Tracking (bd-3gh5m)
// =============================================================================
//
// TrendTracker maintains a rolling window of context usage samples per pane.
// This enables detecting whether context is declining, stable, or rising.

// TrendType represents the direction of context usage change.
type TrendType string

const (
	TrendDeclining TrendType = "declining" // Context is decreasing (normal usage)
	TrendStable    TrendType = "stable"    // Context is staying roughly the same
	TrendRising    TrendType = "rising"    // Context is increasing (rare, after restart)
	TrendUnknown   TrendType = "unknown"   // Not enough samples
)

// TrendSample represents a single data point for trend analysis.
type TrendSample struct {
	Timestamp        time.Time
	ContextRemaining *float64
}

// TrendTracker maintains history for trend analysis.
type TrendTracker struct {
	mu         sync.RWMutex
	samples    map[int][]TrendSample // pane -> samples
	maxSamples int
}

// NewTrendTracker creates a new TrendTracker with the specified max samples per pane.
func NewTrendTracker(maxSamples int) *TrendTracker {
	if maxSamples < 2 {
		maxSamples = 2 // Need at least 2 samples for trend
	}
	return &TrendTracker{
		samples:    make(map[int][]TrendSample),
		maxSamples: maxSamples,
	}
}

// AddSample records a new sample for a pane.
func (t *TrendTracker) AddSample(pane int, sample TrendSample) {
	t.mu.Lock()
	defer t.mu.Unlock()

	samples := t.samples[pane]
	samples = append(samples, sample)

	// Keep only last N samples
	if len(samples) > t.maxSamples {
		samples = samples[len(samples)-t.maxSamples:]
	}
	t.samples[pane] = samples
}

// GetTrend calculates the trend for a pane based on collected samples.
// Returns the trend type and number of samples used.
func (t *TrendTracker) GetTrend(pane int) (TrendType, int) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	samples := t.samples[pane]
	if len(samples) < 2 {
		return TrendUnknown, len(samples)
	}

	// Calculate trend from samples with known context
	var deltas []float64
	for i := 1; i < len(samples); i++ {
		prev := samples[i-1].ContextRemaining
		curr := samples[i].ContextRemaining
		if prev != nil && curr != nil {
			deltas = append(deltas, *curr-*prev)
		}
	}

	if len(deltas) == 0 {
		return TrendUnknown, len(samples)
	}

	// Calculate average delta
	avgDelta := calculateAvgDelta(deltas)

	// Classify trend based on average change per sample
	// Threshold of 2.0% per sample distinguishes significant change
	return classifyTrend(avgDelta), len(samples)
}

// calculateAvgDelta computes the average of a slice of deltas.
func calculateAvgDelta(deltas []float64) float64 {
	if len(deltas) == 0 {
		return 0
	}
	var sum float64
	for _, d := range deltas {
		sum += d
	}
	return sum / float64(len(deltas))
}

// classifyTrend classifies the average delta into a TrendType.
// Threshold of 2.0% per sample distinguishes significant change.
func classifyTrend(avgDelta float64) TrendType {
	switch {
	case avgDelta < -2.0:
		return TrendDeclining
	case avgDelta > 2.0:
		return TrendRising // Rare, but possible after restart
	default:
		return TrendStable
	}
}

// TrendInfo contains full trend analysis for a pane.
type TrendInfo struct {
	Trend       TrendType
	SampleCount int
	AvgDelta    float64
	LastValue   *float64
	LastUpdate  time.Time
}
