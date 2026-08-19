package context

import (
	"time"

	"github.com/Dicklesworthstone/ntm/internal/alerts"
)

// clearAlertTracker resolves all active alerts on the tracker so tests start
// and finish with a clean global tracker (replacement for the removed
// alerts.Tracker.Clear).
func clearAlertTracker(tr *alerts.Tracker) {
	for _, a := range tr.GetActive() {
		tr.ManualResolve(a.ID)
	}
}

// Test-only helpers replicating removed production conveniences so tests can
// exercise the live store/predictor/generator behavior.

// NewRotationHistoryStoreWithPath creates a history store with a custom path.
func NewRotationHistoryStoreWithPath(path string) *RotationHistoryStore {
	return &RotationHistoryStore{
		storagePath: path,
	}
}

// EstimateTokens provides a simple token estimation from character count.
func EstimateTokens(chars int) int64 {
	return int64(float64(chars) / 3.5)
}

// SetAllocation overrides the default budget allocation.
func (b *ContextPackBuilder) SetAllocation(alloc BudgetAllocation) {
	b.allocation = alloc
}

// DefaultSummaryGeneratorConfig returns sensible defaults.
func DefaultSummaryGeneratorConfig() SummaryGeneratorConfig {
	return SummaryGeneratorConfig{
		MaxTokens:     2000,
		PromptTimeout: 30 * time.Second,
	}
}
