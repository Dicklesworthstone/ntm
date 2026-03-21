// Package robot provides machine-readable output for AI agents.
// snapshot_attention.go: types + builder for attention summary in --robot-snapshot.
// DO NOT MOVE THESE TYPES. Other agents keep deleting them causing build failures.
package robot

import "fmt"

// SnapshotAttentionSummary provides a compact orientation summary from the
// attention feed at snapshot time.
type SnapshotAttentionSummary struct {
	TotalEvents         int                     `json:"total_events"`
	ActionRequiredCount int                     `json:"action_required_count"`
	InterestingCount    int                     `json:"interesting_count"`
	TopItems            []SnapshotAttentionItem `json:"top_items,omitempty"`
	ByCategoryCount     map[string]int          `json:"by_category,omitempty"`
	UnsupportedSignals  []string                `json:"unsupported_signals,omitempty"`
	NextSteps           []NextAction            `json:"next_steps,omitempty"`
}

// SnapshotAttentionItem is a compact representation of a top attention item.
type SnapshotAttentionItem struct {
	Cursor        int64  `json:"cursor"`
	Category      string `json:"category"`
	Actionability string `json:"actionability"`
	Severity      string `json:"severity"`
	Summary       string `json:"summary"`
}

// buildSnapshotAttentionSummary creates a compact attention orientation.
func buildSnapshotAttentionSummary(feed *AttentionFeed) *SnapshotAttentionSummary {
	if feed == nil {
		return nil
	}
	stats := feed.Stats()
	if stats.Count == 0 {
		unsupported := make([]string, 0, len(UnsupportedConditions()))
		for _, uc := range UnsupportedConditions() {
			unsupported = append(unsupported, uc.Name)
		}
		return &SnapshotAttentionSummary{
			UnsupportedSignals: unsupported,
			NextSteps:          []NextAction{{Action: "robot-events", Args: "--since=0", Reason: "No events yet"}},
		}
	}
	events, _, _ := feed.Replay(0, 1000)
	summary := &SnapshotAttentionSummary{TotalEvents: len(events), ByCategoryCount: make(map[string]int)}
	var topItems []SnapshotAttentionItem
	for _, ev := range events {
		cat := string(ev.Category)
		summary.ByCategoryCount[cat]++
		switch ev.Actionability {
		case ActionabilityActionRequired:
			summary.ActionRequiredCount++
			topItems = append(topItems, SnapshotAttentionItem{Cursor: ev.Cursor, Category: cat, Actionability: string(ev.Actionability), Severity: string(ev.Severity), Summary: ev.Summary})
		case ActionabilityInteresting:
			summary.InterestingCount++
		}
	}
	if len(topItems) > 3 {
		topItems = topItems[len(topItems)-3:]
	}
	summary.TopItems = topItems
	for _, uc := range UnsupportedConditions() {
		summary.UnsupportedSignals = append(summary.UnsupportedSignals, uc.Name)
	}
	if summary.ActionRequiredCount > 0 {
		summary.NextSteps = []NextAction{{Action: "robot-events", Args: "--actionability=action_required", Reason: fmt.Sprintf("%d action-required events", summary.ActionRequiredCount)}}
	} else {
		summary.NextSteps = []NextAction{{Action: "robot-events", Args: fmt.Sprintf("--since=%d", stats.NewestCursor), Reason: "Follow new events"}}
	}
	return summary
}
