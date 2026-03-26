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

import "time"

// =============================================================================
// Section Names (Canonical)
// =============================================================================

// Section name constants for use across renderers.
const (
	SectionSummary     = "summary"
	SectionSessions    = "sessions"
	SectionWork        = "work"
	SectionAlerts      = "alerts"
	SectionAttention   = "attention"
	SectionCoordination = "coordination"
	SectionQuota       = "quota"
	SectionIncidents   = "incidents"
	SectionHealth      = "health"
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
	Sessions         int
	WorkReady        int
	WorkBlocked      int
	WorkInProgress   int
	Alerts           int
	AttentionAction  int
	AttentionInteresting int
	AttentionBackground  int
	Incidents        int
}

// DefaultSectionLimits returns sensible default limits.
func DefaultSectionLimits() SectionLimits {
	return SectionLimits{
		Sessions:             20,
		WorkReady:            10,
		WorkBlocked:          5,
		WorkInProgress:       5,
		Alerts:               10,
		AttentionAction:      5,
		AttentionInteresting: 4,
		AttentionBackground:  3,
		Incidents:            5,
	}
}

// CompactSectionLimits returns limits suitable for compact mode.
func CompactSectionLimits() SectionLimits {
	return SectionLimits{
		Sessions:             10,
		WorkReady:            3,
		WorkBlocked:          2,
		WorkInProgress:       3,
		Alerts:               5,
		AttentionAction:      3,
		AttentionInteresting: 2,
		AttentionBackground:  1,
		Incidents:            3,
	}
}

// TerseSectionLimits returns minimal limits for terse mode.
func TerseSectionLimits() SectionLimits {
	return SectionLimits{
		Sessions:             5,
		WorkReady:            1,
		WorkBlocked:          1,
		WorkInProgress:       1,
		Alerts:               3,
		AttentionAction:      1,
		AttentionInteresting: 1,
		AttentionBackground:  0,
		Incidents:            1,
	}
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

// ItemCount returns the number of items in this section (if applicable).
func (s ProjectedSection) ItemCount() int {
	if s.Data == nil {
		return 0
	}
	switch data := s.Data.(type) {
	case []SnapshotSession:
		return len(data)
	case []any:
		return len(data)
	default:
		return 1 // Scalar sections count as 1
	}
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

// ProjectSections builds a SectionProjection from a SnapshotOutput.
// This is the single entry point that all renderers should use.
func ProjectSections(snapshot *SnapshotOutput, opts SectionProjectionOptions) *SectionProjection {
	if snapshot == nil {
		snapshot = &SnapshotOutput{}
	}

	// Determine which sections to include
	includedSet := buildIncludedSet(opts)

	// Initialize result
	projection := &SectionProjection{
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		SourceVersion: EnvelopeVersion,
		Options:       &opts,
		Metadata: &SectionProjectionMetadata{
			SectionsIncluded: []string{},
			SectionsOmitted:  make(map[string]string),
		},
	}

	if !opts.Timestamp.IsZero() {
		projection.Timestamp = opts.Timestamp.Format(time.RFC3339)
	}

	// Project each section
	sections := make([]ProjectedSection, 0, 10)

	// Summary section (always included unless explicitly excluded)
	if includedSet[SectionSummary] {
		section := projectSummarySection(snapshot, opts)
		sections = append(sections, section)
		projection.Metadata.SectionsIncluded = append(projection.Metadata.SectionsIncluded, SectionSummary)
	} else {
		projection.Metadata.SectionsOmitted[SectionSummary] = "not_requested"
	}

	// Sessions section
	if includedSet[SectionSessions] {
		section := projectSessionsSection(snapshot, opts)
		sections = append(sections, section)
		projection.Metadata.SectionsIncluded = append(projection.Metadata.SectionsIncluded, SectionSessions)
		if section.IsTruncated() {
			projection.Metadata.TotalTruncated += section.Truncation.TruncatedCount
		}
	} else {
		projection.Metadata.SectionsOmitted[SectionSessions] = "not_requested"
	}

	// Work section
	if includedSet[SectionWork] {
		section := projectWorkSection(snapshot, opts)
		sections = append(sections, section)
		projection.Metadata.SectionsIncluded = append(projection.Metadata.SectionsIncluded, SectionWork)
		if section.IsTruncated() {
			projection.Metadata.TotalTruncated += section.Truncation.TruncatedCount
		}
	} else {
		projection.Metadata.SectionsOmitted[SectionWork] = "not_requested"
	}

	// Alerts section
	if includedSet[SectionAlerts] {
		section := projectAlertsSection(snapshot, opts)
		sections = append(sections, section)
		projection.Metadata.SectionsIncluded = append(projection.Metadata.SectionsIncluded, SectionAlerts)
		if section.IsTruncated() {
			projection.Metadata.TotalTruncated += section.Truncation.TruncatedCount
		}
	} else {
		projection.Metadata.SectionsOmitted[SectionAlerts] = "not_requested"
	}

	// Attention section
	if includedSet[SectionAttention] {
		section := projectAttentionSection(snapshot, opts)
		sections = append(sections, section)
		projection.Metadata.SectionsIncluded = append(projection.Metadata.SectionsIncluded, SectionAttention)
	} else {
		projection.Metadata.SectionsOmitted[SectionAttention] = "not_requested"
	}

	projection.Sections = sections
	return projection
}

// buildIncludedSet determines which sections should be included.
func buildIncludedSet(opts SectionProjectionOptions) map[string]bool {
	// Default to all standard sections
	allSections := []string{
		SectionSummary,
		SectionSessions,
		SectionWork,
		SectionAlerts,
		SectionAttention,
	}

	// If specific sections requested, use those
	if len(opts.IncludeSections) > 0 {
		included := make(map[string]bool)
		for _, s := range opts.IncludeSections {
			included[s] = true
		}
		// Remove excluded
		for _, s := range opts.ExcludeSections {
			delete(included, s)
		}
		return included
	}

	// Start with all, remove excluded
	included := make(map[string]bool)
	for _, s := range allSections {
		included[s] = true
	}
	for _, s := range opts.ExcludeSections {
		delete(included, s)
	}
	return included
}

// projectSummarySection projects the summary section from snapshot.
func projectSummarySection(snapshot *SnapshotOutput, opts SectionProjectionOptions) ProjectedSection {
	hints := DefaultSectionFormatHints(SectionSummary)
	section := NewProjectedSection(SectionSummary, snapshot.Summary).WithFormatHints(hints)
	return section
}

// projectSessionsSection projects the sessions section from snapshot.
func projectSessionsSection(snapshot *SnapshotOutput, opts SectionProjectionOptions) ProjectedSection {
	hints := DefaultSectionFormatHints(SectionSessions)

	sessions := snapshot.Sessions
	if sessions == nil {
		sessions = []SnapshotSession{}
	}

	// Apply session filter if specified
	if opts.SessionFilter != "" {
		filtered := make([]SnapshotSession, 0, 1)
		for _, s := range sessions {
			if s.Name == opts.SessionFilter {
				filtered = append(filtered, s)
			}
		}
		sessions = filtered
	}

	// Apply limit
	limit := opts.Limits.Sessions
	if limit <= 0 {
		limit = DefaultSectionLimits().Sessions
	}

	original := len(sessions)
	truncated := 0
	if len(sessions) > limit {
		truncated = len(sessions) - limit
		sessions = sessions[:limit]
	}

	section := NewProjectedSection(SectionSessions, sessions).WithFormatHints(hints)
	if truncated > 0 {
		section = section.WithTruncation(original, truncated, "limit", "use offset/limit parameters")
	}
	return section
}

// projectWorkSection projects the work section from snapshot.
func projectWorkSection(snapshot *SnapshotOutput, opts SectionProjectionOptions) ProjectedSection {
	hints := DefaultSectionFormatHints(SectionWork)

	if snapshot.Work == nil {
		section := NewProjectedSection(SectionWork, nil).WithFormatHints(hints)
		return section.WithOmission("empty", "")
	}

	// The work section comes pre-truncated from the snapshot,
	// but we track the truncation for format hints
	section := NewProjectedSection(SectionWork, snapshot.Work).WithFormatHints(hints)
	return section
}

// projectAlertsSection projects the alerts section from snapshot.
func projectAlertsSection(snapshot *SnapshotOutput, opts SectionProjectionOptions) ProjectedSection {
	hints := DefaultSectionFormatHints(SectionAlerts)

	// Use AlertsDetailed (rich alert objects) if available, fall back to Alerts (strings)
	if len(snapshot.AlertsDetailed) == 0 && len(snapshot.Alerts) == 0 {
		section := NewProjectedSection(SectionAlerts, snapshot.AlertSummary).WithFormatHints(hints)
		return section
	}

	// Apply limit to detailed alerts
	limit := opts.Limits.Alerts
	if limit <= 0 {
		limit = DefaultSectionLimits().Alerts
	}

	alerts := snapshot.AlertsDetailed
	original := len(alerts)
	truncated := 0

	if len(alerts) > limit {
		truncated = len(alerts) - limit
		alerts = alerts[:limit]
	}

	// Build result with both detailed and summary
	result := struct {
		Alerts  []AlertInfo       `json:"alerts"`
		Summary *AlertSummaryInfo `json:"summary,omitempty"`
	}{
		Alerts:  alerts,
		Summary: snapshot.AlertSummary,
	}

	section := NewProjectedSection(SectionAlerts, result).WithFormatHints(hints)
	if truncated > 0 {
		section = section.WithTruncation(original, truncated, "limit", "use include_resolved=true for all")
	}
	return section
}

// projectAttentionSection projects the attention section from snapshot.
func projectAttentionSection(snapshot *SnapshotOutput, opts SectionProjectionOptions) ProjectedSection {
	hints := DefaultSectionFormatHints(SectionAttention)

	if snapshot.AttentionSummary == nil {
		section := NewProjectedSection(SectionAttention, nil).WithFormatHints(hints)
		return section.WithOmission("unavailable", "attention feed not running")
	}

	section := NewProjectedSection(SectionAttention, snapshot.AttentionSummary).WithFormatHints(hints)
	return section
}

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
// For alerts, use ProjectSections with the snapshot's AlertsDetailed field -
// the existing projectAlertsSection handles alerts appropriately.
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

	events, _, err := feed.Replay(0, replayLimit)
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

// GetTerseProjection returns a section projection optimized for terse output.
// This provides the same data as GetTerse but wrapped in the section model
// for consistent access patterns.
func GetTerseProjection(snapshot *SnapshotOutput) *SectionProjection {
	return ProjectSections(snapshot, SectionProjectionOptions{
		Limits: TerseSectionLimits(),
	})
}
