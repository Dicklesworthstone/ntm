// Package robot provides machine-readable output for AI agents.
// sections.go defines the unified section projection model that all renderers
// (JSON, TOON, markdown, terse) share.
//
// # Section Projection Model (bd-j9jo3.6.5)
//
// This model ensures that format changes do not create semantic drift.
// All renderers project from the same underlying section model, making
// truncation, omission, and ordering explicit rather than hidden in
// formatter-specific behavior.
//
// # Section Types
//
// Sections are divided into categories based on scope and mutability:
//   - Global: System-wide state (summary, sessions, work, alerts, attention)
//   - Session-scoped: Per-session state (agents, panes)
//   - Diagnostic: Deep inspection state (incidents, quotas)
//
// # Truncation Semantics
//
// When a section's content is truncated:
//   - TruncatedCount reports how many items were omitted
//   - TruncationReason explains why (limit, budget, relevance)
//   - ResumptionHint provides a command or cursor to fetch more
//
// # Stable Ordering
//
// Section order is defined by the SectionOrderWeight constants.
// All renderers must respect this ordering unless the format
// semantically requires a different presentation.
package robot

import (
	"time"
)

// =============================================================================
// Section Names (Canonical)
// =============================================================================

// Section name constants for use across renderers.
const (
	SectionSummary      = "summary"
	SectionSessions     = "sessions"
	SectionWork         = "work"
	SectionAlerts       = "alerts"
	SectionAttention    = "attention"
	SectionCoordination = "coordination"
	SectionQuota        = "quota"
	SectionIncidents    = "incidents"
	SectionHealth       = "health"
	SectionReplayWindow = "replay_window"
)

// =============================================================================
// Section Ordering
// =============================================================================

// SectionOrderWeight defines the canonical ordering of sections.
// Lower weights appear first. Renderers should preserve this ordering.
var SectionOrderWeight = map[string]int{
	SectionSummary:      10,
	SectionSessions:     20,
	SectionWork:         30,
	SectionAlerts:       40,
	SectionAttention:    50,
	SectionCoordination: 60,
	SectionHealth:       70,
	SectionQuota:        80,
	SectionIncidents:    90,
	SectionReplayWindow: 100,
}

// =============================================================================
// Truncation Model
// =============================================================================

// SectionTruncation describes how a section was truncated.
type SectionTruncation struct {
	// Applied indicates whether truncation was applied to this section.
	Applied bool `json:"applied"`

	// OriginalCount is the total number of items before truncation.
	OriginalCount int `json:"original_count,omitempty"`

	// TruncatedCount is the number of items omitted.
	TruncatedCount int `json:"truncated_count,omitempty"`

	// Reason explains why truncation was applied.
	// Common values: "limit", "token_budget", "relevance", "staleness"
	Reason string `json:"reason,omitempty"`

	// ResumptionHint provides guidance for fetching more items.
	// For cursor-based pagination: "use since_cursor=12345"
	// For offset pagination: "use offset=50"
	ResumptionHint string `json:"resumption_hint,omitempty"`
}

// SectionOmission describes why a section is completely omitted.
type SectionOmission struct {
	// Omitted indicates the section was intentionally omitted.
	Omitted bool `json:"omitted"`

	// Reason explains why the section was omitted.
	// Common values: "not_requested", "unavailable", "empty", "format_limit"
	Reason string `json:"reason,omitempty"`

	// AlternativeSurface names a surface that provides this data.
	AlternativeSurface string `json:"alternative_surface,omitempty"`
}

// =============================================================================
// Projected Section Model
// =============================================================================

// ProjectedSection represents a section with its data and metadata.
// All renderers project from this shared model.
type ProjectedSection struct {
	// Name is the canonical section name (e.g., "summary", "sessions").
	Name string `json:"name"`

	// OrderWeight determines rendering order (lower = earlier).
	OrderWeight int `json:"order_weight"`

	// Data is the section content. Type varies by section.
	// summary -> *SnapshotSummary
	// sessions -> []SnapshotSession
	// work -> *adapters.WorkSection
	// alerts -> *adapters.AlertsSection
	// attention -> *SnapshotAttentionSummary
	Data any `json:"data,omitempty"`

	// Truncation describes how this section was truncated (if at all).
	Truncation *SectionTruncation `json:"truncation,omitempty"`

	// Omission describes why this section was omitted (if applicable).
	Omission *SectionOmission `json:"omission,omitempty"`

	// FormatHints provides renderer-specific guidance.
	FormatHints *SectionFormatHints `json:"format_hints,omitempty"`
}

// SectionFormatHints provides guidance for how renderers should handle a section.
type SectionFormatHints struct {
	// CompactLabel is a short label for compact/terse modes.
	CompactLabel string `json:"compact_label,omitempty"`

	// MarkdownHeading overrides the default markdown heading.
	MarkdownHeading string `json:"markdown_heading,omitempty"`

	// TOONSchema is the TOON schema ID for this section.
	TOONSchema string `json:"toon_schema,omitempty"`

	// TerseFormat is a format string for terse mode (e.g., "S:%d").
	TerseFormat string `json:"terse_format,omitempty"`

	// CanOmitInTerse indicates this section can be omitted in terse mode.
	CanOmitInTerse bool `json:"can_omit_in_terse,omitempty"`

	// CompactThreshold is the item count above which compact mode is used.
	CompactThreshold int `json:"compact_threshold,omitempty"`
}

// =============================================================================
// Section Projection Options
// =============================================================================

// SectionProjectionOptions configures how sections are projected from snapshots.
type SectionProjectionOptions struct {
	// IncludeSections lists sections to include (empty = all available).
	IncludeSections []string

	// ExcludeSections lists sections to exclude.
	ExcludeSections []string

	// SessionFilter limits session-scoped sections to a specific session.
	SessionFilter string

	// Limits controls per-section item limits.
	Limits SectionLimits

	// TokenBudget is the maximum token budget (0 = unlimited).
	// When set, sections are truncated to stay within budget.
	TokenBudget int

	// Format indicates the target format (affects format hints).
	Format RobotFormat

	// Verbosity controls detail level.
	Verbosity RobotVerbosity

	// Timestamp overrides the projection timestamp (empty = now).
	Timestamp time.Time
}

// SectionLimits controls per-section item limits.
type SectionLimits struct {
	Sessions             int
	WorkReady            int
	WorkBlocked          int
	WorkInProgress       int
	Alerts               int
	AttentionAction      int
	AttentionInteresting int
	AttentionBackground  int
	Incidents            int
}

// DashboardSectionLimits returns limits suitable for interactive TUI dashboard.
// These limits are higher than robot mode to support scrollable panels.
func DashboardSectionLimits() SectionLimits {
	return SectionLimits{
		Sessions:             50,
		WorkReady:            25,
		WorkBlocked:          15,
		WorkInProgress:       20,
		Alerts:               50,
		AttentionAction:      30,
		AttentionInteresting: 20,
		AttentionBackground:  10,
		Incidents:            20,
	}
}

// =============================================================================
// Section Projection Result
// =============================================================================

// SectionProjection is the result of projecting a snapshot into sections.
// This is the shared model that all renderers consume.
type SectionProjection struct {
	// Sections contains the projected sections in canonical order.
	Sections []ProjectedSection `json:"sections"`

	// Timestamp is when this projection was created.
	Timestamp string `json:"timestamp"`

	// SourceVersion identifies the snapshot version used.
	SourceVersion string `json:"source_version,omitempty"`

	// Options records the projection options used.
	Options *SectionProjectionOptions `json:"options,omitempty"`

	// Metadata provides additional projection metadata.
	Metadata *SectionProjectionMetadata `json:"metadata,omitempty"`
}

// SectionProjectionMetadata provides metadata about the projection.
type SectionProjectionMetadata struct {
	// SectionsIncluded lists the sections that were included.
	SectionsIncluded []string `json:"sections_included,omitempty"`

	// SectionsOmitted lists sections that were omitted with reasons.
	SectionsOmitted map[string]string `json:"sections_omitted,omitempty"`

	// TotalTruncated is the total number of items truncated across all sections.
	TotalTruncated int `json:"total_truncated,omitempty"`

	// TokenEstimate is the estimated token count (if budget was applied).
	TokenEstimate int `json:"token_estimate,omitempty"`
}

// =============================================================================
// Helper Functions
// =============================================================================

// NewProjectedSection creates a new section with the given name and data.
func NewProjectedSection(name string, data any) ProjectedSection {
	weight, ok := SectionOrderWeight[name]
	if !ok {
		weight = 999 // Unknown sections go last
	}
	return ProjectedSection{
		Name:        name,
		OrderWeight: weight,
		Data:        data,
	}
}

// WithTruncation adds truncation metadata to a section.
func (s ProjectedSection) WithTruncation(original, truncated int, reason, hint string) ProjectedSection {
	s.Truncation = &SectionTruncation{
		Applied:        truncated > 0,
		OriginalCount:  original,
		TruncatedCount: truncated,
		Reason:         reason,
		ResumptionHint: hint,
	}
	return s
}

// WithOmission marks a section as omitted.
func (s ProjectedSection) WithOmission(reason, alternative string) ProjectedSection {
	s.Omission = &SectionOmission{
		Omitted:            true,
		Reason:             reason,
		AlternativeSurface: alternative,
	}
	return s
}

// WithFormatHints adds format hints to a section.
func (s ProjectedSection) WithFormatHints(hints SectionFormatHints) ProjectedSection {
	s.FormatHints = &hints
	return s
}

// IsOmitted returns true if this section was omitted.
func (s ProjectedSection) IsOmitted() bool {
	return s.Omission != nil && s.Omission.Omitted
}

// IsTruncated returns true if this section was truncated.
func (s ProjectedSection) IsTruncated() bool {
	return s.Truncation != nil && s.Truncation.Applied
}

// =============================================================================
// Format Hints Defaults
// =============================================================================

// DefaultSectionFormatHints returns default format hints for a section.
func DefaultSectionFormatHints(name string) SectionFormatHints {
	switch name {
	case SectionSummary:
		return SectionFormatHints{
			CompactLabel:    "SUM",
			MarkdownHeading: "Summary",
			TerseFormat:     "S:%d|A:%d",
			CanOmitInTerse:  false,
		}
	case SectionSessions:
		return SectionFormatHints{
			CompactLabel:     "SESS",
			MarkdownHeading:  "Sessions",
			TerseFormat:      "S:%d",
			CanOmitInTerse:   true,
			CompactThreshold: 5,
		}
	case SectionWork:
		return SectionFormatHints{
			CompactLabel:     "WORK",
			MarkdownHeading:  "Work",
			TerseFormat:      "W:R%d/I%d/B%d",
			CanOmitInTerse:   true,
			CompactThreshold: 10,
		}
	case SectionAlerts:
		return SectionFormatHints{
			CompactLabel:    "ALRT",
			MarkdownHeading: "Alerts",
			TerseFormat:     "!:%d",
			CanOmitInTerse:  true,
		}
	case SectionAttention:
		return SectionFormatHints{
			CompactLabel:    "ATTN",
			MarkdownHeading: "Attention",
			TerseFormat:     "^:%d!/%d?/%dB",
			CanOmitInTerse:  true,
		}
	case SectionCoordination:
		return SectionFormatHints{
			CompactLabel:    "COORD",
			MarkdownHeading: "Coordination",
			TerseFormat:     "M:%d",
			CanOmitInTerse:  true,
		}
	case SectionHealth:
		return SectionFormatHints{
			CompactLabel:    "HLTH",
			MarkdownHeading: "Health",
			TerseFormat:     "H:%s",
			CanOmitInTerse:  true,
		}
	default:
		return SectionFormatHints{
			CompactLabel:    name[:min(4, len(name))],
			MarkdownHeading: name,
			CanOmitInTerse:  true,
		}
	}
}

// =============================================================================
// Section Projection Functions
// =============================================================================

// =============================================================================
// Dashboard Section Projections
// =============================================================================
//
// Dashboard projections provide richer data than robot mode summaries, suitable
// for interactive TUI panels with scrolling and selection. They use the same
// section model and truncation semantics to ensure conceptual alignment.

// DashboardAttentionData contains full attention events for dashboard use.
// Unlike SnapshotAttentionSummary which provides only counts and top 3 items,
// this provides the full event list with consistent truncation metadata.
type DashboardAttentionData struct {
	// Events is the list of attention events (may be truncated).
	Events []AttentionEvent `json:"events"`

	// Summary provides aggregate counts for orientation.
	Summary *SnapshotAttentionSummary `json:"summary,omitempty"`

	// FeedAvailable indicates whether the attention feed is running.
	FeedAvailable bool `json:"feed_available"`
}

// GetDashboardAttentionSection returns an attention section with full events
// suitable for TUI dashboard display. Returns a ProjectedSection with consistent
// truncation metadata.
//
// Unlike projectAttentionSection which returns SnapshotAttentionSummary (counts
// and top 3 items), this returns full events for interactive scrolling.
//
// For alerts, use ProjectSections with the snapshot's AlertsDetailed field.
func GetDashboardAttentionSection(limits SectionLimits) ProjectedSection {
	hints := DefaultSectionFormatHints(SectionAttention)

	feed := PeekAttentionFeed()
	if feed == nil {
		section := NewProjectedSection(SectionAttention, DashboardAttentionData{
			FeedAvailable: false,
		}).WithFormatHints(hints)
		return section.WithOmission("unavailable", "attention feed not running")
	}

	// Get events from feed
	stats := feed.Stats()
	maxEvents := limits.AttentionAction + limits.AttentionInteresting + limits.AttentionBackground
	if maxEvents <= 0 {
		maxEvents = 60 // Default for dashboard
	}

	replayLimit := stats.Count
	if replayLimit > maxEvents {
		replayLimit = maxEvents
	}

	// Replay walks the journal from the head and stops at the limit, so it
	// returns the OLDEST events after the cursor. A dashboard panel needs the
	// newest: with Replay(0, 60) the panel showed events 1..60 forever once the
	// journal held more than 60, and no amount of newer action-required activity
	// could ever appear. Start the window near the tail instead, clamped to what
	// is still retained so the request cannot be rejected as expired.
	since := int64(0)
	if stats.Count > replayLimit {
		since = stats.NewestCursor - int64(replayLimit)
		if oldest := stats.OldestCursor; oldest > 0 && since < oldest-1 {
			since = oldest - 1
		}
		if since < 0 {
			since = 0
		}
	}

	events, _, err := feed.Replay(since, replayLimit)
	if err != nil {
		section := NewProjectedSection(SectionAttention, DashboardAttentionData{
			FeedAvailable: true,
		}).WithFormatHints(hints)
		return section.WithOmission("error", "failed to replay attention events")
	}

	// Filter to action_required and interesting (exclude background for dashboard)
	filtered := make([]AttentionEvent, 0, len(events))
	for _, ev := range events {
		if ev.Actionability == ActionabilityActionRequired ||
			ev.Actionability == ActionabilityInteresting {
			filtered = append(filtered, ev)
		}
	}

	// Build summary from feed
	summary := buildSnapshotAttentionSummary(feed)

	data := DashboardAttentionData{
		Events:        filtered,
		Summary:       summary,
		FeedAvailable: true,
	}

	section := NewProjectedSection(SectionAttention, data).WithFormatHints(hints)

	// Report truncation if we limited the results
	if stats.Count > maxEvents {
		section = section.WithTruncation(stats.Count, stats.Count-len(filtered),
			"limit", "use --robot-attention for full event replay")
	}

	return section
}

// =============================================================================
// Terse and TOON Alignment
// =============================================================================
//
// Terse and TOON renderers are aligned with the section model:
//
// 1. DATA ALIGNMENT: Both use GetSnapshot() which provides the same data
//    that section projections consume. The data path is unified at the
//    snapshot level.
//
// 2. FORMAT HINTS: SectionFormatHints includes TerseFormat and TOONSchema
//    fields that guide how each section should be rendered in these formats.
//
// 3. TRUNCATION: While terse shows only summary counts (not individual items),
//    the section model's truncation metadata is available if needed.
//
// For rendering with explicit section model usage:
//
//   projection := ProjectSections(snapshot, SectionProjectionOptions{
//       Limits: TerseSectionLimits(),
//   })
//   for _, section := range projection.Sections {
//       hints := section.FormatHints
//       if hints != nil && hints.TerseFormat != "" {
//           // Use TerseFormat to render this section
//       }
//   }

// DashboardAlertsData contains alert data for TUI dashboard display.
type DashboardAlertsData struct {
	Alerts  []AlertInfo       `json:"alerts"`
	Summary *AlertSummaryInfo `json:"summary,omitempty"`
}
