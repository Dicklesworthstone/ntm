package bv

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// ReadySnapshot binds the complete direct br-ready count to the capped preview
// derived from that same command result. Stats and bv may summarize or rank
// work, but only br ready is allowed to positively establish readiness.
type ReadySnapshot struct {
	Total   int
	Preview []BeadPreview
}

// GetReadySnapshotContext reads direct local readiness exactly once and returns
// both the complete count and a caller-sized preview. A zero limit requests the
// full preview; negative limits are rejected.
func GetReadySnapshotContext(ctx context.Context, dir string, limit int) (*ReadySnapshot, error) {
	if ctx == nil {
		return nil, fmt.Errorf("ready snapshot context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit < 0 {
		return nil, fmt.Errorf("ready snapshot limit must not be negative: %d", limit)
	}

	all, err := GetReadyPreviewContext(ctx, dir, int(^uint(0)>>1))
	if err != nil {
		return nil, err
	}
	preview := all
	if limit > 0 && len(preview) > limit {
		preview = preview[:limit]
	}
	return &ReadySnapshot{
		Total:   len(all),
		Preview: append([]BeadPreview(nil), preview...),
	}, nil
}

// BlockedBeadPreview is the direct br-blocked evidence needed to preserve a
// blocked item through normalization and durable robot projection.
type BlockedBeadPreview struct {
	ID             string
	Title          string
	Priority       int
	Type           string
	BlockedBy      []string
	BlockedByCount int
}

// GetBlockedSnapshotContext returns blocked work with its authoritative blocker
// evidence. A zero limit requests every row; negative limits are rejected.
func GetBlockedSnapshotContext(ctx context.Context, dir string, limit int) ([]BlockedBeadPreview, error) {
	if ctx == nil {
		return nil, fmt.Errorf("blocked snapshot context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit < 0 {
		return nil, fmt.Errorf("blocked snapshot limit must not be negative: %d", limit)
	}

	output, err := RunBdContext(ctx, dir, "blocked", "--json")
	if err != nil {
		return nil, fmt.Errorf("list blocked beads: %w", err)
	}
	type blockedRow struct {
		ID             string   `json:"id"`
		Title          string   `json:"title"`
		Priority       int      `json:"priority"`
		IssueType      string   `json:"issue_type"`
		Type           string   `json:"type"`
		BlockedBy      []string `json:"blocked_by"`
		BlockedByCount int      `json:"blocked_by_count"`
	}
	rows, err := UnmarshalBdList[blockedRow](output)
	if err != nil {
		return nil, fmt.Errorf("parse blocked beads: %w", err)
	}

	items := make([]BlockedBeadPreview, 0, len(rows))
	for index, row := range rows {
		id := strings.TrimSpace(row.ID)
		if id == "" {
			return nil, fmt.Errorf("parse blocked beads: row %d has no id", index)
		}
		blockers := compactSortedStrings(row.BlockedBy)
		blockedByCount := row.BlockedByCount
		if blockedByCount < len(blockers) {
			blockedByCount = len(blockers)
		}
		// Membership in br blocked is itself authoritative restrictive evidence.
		// Keep that evidence even if an older br build omits blocker details.
		if blockedByCount < 1 {
			blockedByCount = 1
		}
		issueType := strings.TrimSpace(row.IssueType)
		if issueType == "" {
			issueType = strings.TrimSpace(row.Type)
		}
		items = append(items, BlockedBeadPreview{
			ID:             id,
			Title:          strings.TrimSpace(row.Title),
			Priority:       row.Priority,
			Type:           issueType,
			BlockedBy:      blockers,
			BlockedByCount: blockedByCount,
		})
		if limit > 0 && len(items) >= limit {
			break
		}
	}
	return items, nil
}

func compactSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
