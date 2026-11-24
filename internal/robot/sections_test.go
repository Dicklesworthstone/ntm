package robot

import (
	"testing"
	"time"
)

func TestNewProjectedSection(t *testing.T) {
	section := NewProjectedSection(SectionSummary, "test data")

	if section.Name != SectionSummary {
		t.Errorf("expected name %s, got %s", SectionSummary, section.Name)
	}
	if section.OrderWeight != SectionOrderWeight[SectionSummary] {
		t.Errorf("expected weight %d, got %d",
			SectionOrderWeight[SectionSummary], section.OrderWeight)
	}
	if section.Data != "test data" {
		t.Errorf("unexpected data: %v", section.Data)
	}
}

func TestProjectedSection_WithTruncation(t *testing.T) {
	section := NewProjectedSection(SectionSessions, nil)
	section = section.WithTruncation(100, 50, "limit", "use offset=50")

	if !section.IsTruncated() {
		t.Error("expected section to be truncated")
	}
	if section.Truncation.OriginalCount != 100 {
		t.Errorf("expected original 100, got %d", section.Truncation.OriginalCount)
	}
	if section.Truncation.TruncatedCount != 50 {
		t.Errorf("expected truncated 50, got %d", section.Truncation.TruncatedCount)
	}
	if section.Truncation.Reason != "limit" {
		t.Errorf("expected reason 'limit', got %s", section.Truncation.Reason)
	}
}

func TestProjectedSection_WithOmission(t *testing.T) {
	section := NewProjectedSection(SectionAttention, nil)
	section = section.WithOmission("unavailable", "use --robot-attention")

	if !section.IsOmitted() {
		t.Error("expected section to be omitted")
	}
	if section.Omission.Reason != "unavailable" {
		t.Errorf("expected reason 'unavailable', got %s", section.Omission.Reason)
	}
}

func TestDefaultSectionFormatHints(t *testing.T) {
	hints := DefaultSectionFormatHints(SectionSummary)

	if hints.CompactLabel == "" {
		t.Error("expected non-empty compact label")
	}
	if hints.MarkdownHeading == "" {
		t.Error("expected non-empty markdown heading")
	}
}

// =============================================================================
// Dashboard Section Tests (bd-j9jo3.8.2 alignment)
// =============================================================================

func TestGetDashboardAttentionSection_FeedUnavailable(t *testing.T) {
	// When attention feed is not running, section should be omitted
	section := GetDashboardAttentionSection(DashboardSectionLimits(), "")

	// Either omitted or has FeedAvailable=false in data
	if section.IsOmitted() {
		if section.Omission.Reason != "unavailable" {
			t.Errorf("expected reason 'unavailable', got %s", section.Omission.Reason)
		}
		return
	}

	// If not omitted, check data
	data, ok := section.Data.(DashboardAttentionData)
	if !ok {
		t.Fatalf("expected DashboardAttentionData, got %T", section.Data)
	}
	if data.FeedAvailable {
		t.Skip("feed is running; cannot test unavailable case")
	}
}

func TestGetDashboardAttentionSection_DataType(t *testing.T) {
	section := GetDashboardAttentionSection(DashboardSectionLimits(), "")

	// Skip if omitted (feed not running)
	if section.IsOmitted() {
		t.Skip("attention feed not available")
	}

	// Verify correct data type
	data, ok := section.Data.(DashboardAttentionData)
	if !ok {
		t.Fatalf("expected DashboardAttentionData, got %T", section.Data)
	}

	// Events should be a slice (possibly empty)
	if data.Events == nil {
		t.Error("expected Events slice to be non-nil")
	}

	// FeedAvailable should be true if we got here
	if !data.FeedAvailable {
		t.Error("expected FeedAvailable to be true")
	}
}

func TestGetDashboardAttentionSection_SessionScopedPreTruncation(t *testing.T) {
	oldFeed := PeekAttentionFeed()
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       256,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	SetAttentionFeed(feed)
	t.Cleanup(func() {
		feed.Stop()
		SetAttentionFeed(oldFeed)
	})

	// Emit one action_required event for projA and one global interesting event,
	// then flood the feed with >60 foreign-session events so naive tail slicing
	// would drop projA before session filtering.
	feed.Append(AttentionEvent{
		Session:       "projA",
		Pane:          1,
		Category:      EventCategoryAgent,
		Actionability: ActionabilityActionRequired,
		Summary:       "projA needs approval",
	})
	feed.Append(AttentionEvent{
		Session:       "",
		Category:      EventCategoryAlert,
		Actionability: ActionabilityInteresting,
		Summary:       "global system notice",
	})
	for i := 0; i < 75; i++ {
		feed.Append(AttentionEvent{
			Session:       "projB",
			Pane:          2,
			Category:      EventCategoryAgent,
			Actionability: ActionabilityActionRequired,
			Summary:       "foreign flood",
		})
	}

	section := GetDashboardAttentionSection(DashboardSectionLimits(), "projA")
	if section.IsOmitted() {
		t.Fatalf("unexpected omission: %+v", section.Omission)
	}
	if section.IsTruncated() {
		t.Fatalf("projA has only 2 matching events; should not be marked truncated: %+v", section.Truncation)
	}
	data, ok := section.Data.(DashboardAttentionData)
	if !ok {
		t.Fatalf("expected DashboardAttentionData, got %T", section.Data)
	}
	if len(data.Events) != 2 {
		t.Fatalf("projA events = %d, want 2 (projA + global): %+v", len(data.Events), data.Events)
	}
	if data.Events[0].Session != "projA" || data.Events[1].Session != "" {
		t.Fatalf("unexpected event sessions: %+v", data.Events)
	}
	if data.Summary == nil {
		t.Fatal("expected non-nil session-scoped Summary")
	}
	if data.Summary.TotalEvents != 2 || data.Summary.ActionRequiredCount != 1 || data.Summary.InterestingCount != 1 {
		t.Fatalf("projA summary leaked foreign events: %+v, want total=2 action=1 interesting=1", data.Summary)
	}
	if len(data.Summary.TopItems) != 1 || data.Summary.TopItems[0].Summary != "projA needs approval" {
		t.Fatalf("projA summary TopItems leaked foreign items: %+v", data.Summary.TopItems)
	}

	// Verify projB (75 matching events > limit 60) IS truncated accurately.
	sectionB := GetDashboardAttentionSection(DashboardSectionLimits(), "projB")
	dataB, ok := sectionB.Data.(DashboardAttentionData)
	if !ok {
		t.Fatalf("expected DashboardAttentionData for projB, got %T", sectionB.Data)
	}
	if len(dataB.Events) != 60 {
		t.Fatalf("projB events = %d, want 60", len(dataB.Events))
	}
	if !sectionB.IsTruncated() || sectionB.Truncation.OriginalCount != 76 || sectionB.Truncation.TruncatedCount != 16 {
		t.Fatalf("projB truncation = %+v, want original=76 truncated=16", sectionB.Truncation)
	}

	// Verify wrapped journals (OldestCursor > 1) replay cleanly via cursor 0
	// without tripping CursorExpiredError.
	wrapped := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       16,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	SetAttentionFeed(wrapped)
	t.Cleanup(func() { wrapped.Stop() })
	for i := 0; i < 35; i++ {
		wrapped.Append(AttentionEvent{
			Session:       "projA",
			Pane:          1,
			Category:      EventCategoryAgent,
			Actionability: ActionabilityActionRequired,
			Summary:       "wrapped event",
		})
	}
	if wrapped.Stats().OldestCursor <= 1 {
		t.Fatalf("expected wrapped OldestCursor > 1, got %d", wrapped.Stats().OldestCursor)
	}
	wrappedSection := GetDashboardAttentionSection(DashboardSectionLimits(), "projA")
	if wrappedSection.IsOmitted() {
		t.Fatalf("wrapped journal should not omit section: %+v", wrappedSection.Omission)
	}
	wrappedData, ok := wrappedSection.Data.(DashboardAttentionData)
	if !ok || len(wrappedData.Events) != 16 {
		t.Fatalf("wrapped journal events = (%T, %d), want 16 events", wrappedSection.Data, len(wrappedData.Events))
	}
}

func TestGetDashboardAttentionSectionFiltersForeignPaneRefFromSummary(t *testing.T) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       32,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	prev := PeekAttentionFeed()
	SetAttentionFeed(feed)
	t.Cleanup(func() {
		feed.Stop()
		SetAttentionFeed(prev)
	})

	feed.Append(AttentionEvent{
		Session:       "projA",
		Category:      EventCategoryAlert,
		Type:          EventTypeAlertWarning,
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityError,
		Summary:       "projA error",
	})
	// Session-less event whose pane_ref targets foreign session projB must not
	// leak into projA's events or summary counts.
	feed.Append(AttentionEvent{
		Session:       "",
		Category:      EventCategoryAlert,
		Type:          EventTypeAlertWarning,
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityError,
		Summary:       "foreign projB pane_ref error",
		Details:       map[string]any{"pane_ref": "=projB:1.0"},
	})
	// Event claiming projA in Session but carrying a foreign pane_ref must also
	// be rejected (including whitespace after '=').
	feed.Append(AttentionEvent{
		Session:       "projA",
		Category:      EventCategoryAlert,
		Type:          EventTypeAlertWarning,
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityError,
		Summary:       "mismatched session+pane_ref error",
		Details:       map[string]any{"pane_ref": "= projB:1.0"},
	})

	section := GetDashboardAttentionSection(DashboardSectionLimits(), "projA")
	data, ok := section.Data.(DashboardAttentionData)
	if !ok {
		t.Fatalf("unexpected section data type %T", section.Data)
	}
	if len(data.Events) != 1 || data.Events[0].Summary != "projA error" {
		t.Fatalf("expected only projA event, got %+v", data.Events)
	}
	if data.Summary == nil || data.Summary.TotalEvents != 1 || data.Summary.ActionRequiredCount != 1 {
		t.Fatalf("expected Summary counts=1 (no foreign pane_ref leak), got %+v", data.Summary)
	}
}

// =============================================================================
// Section Limit Tier Tests
// =============================================================================

// =============================================================================
// Format Hints Tests
// =============================================================================

func TestFormatHints_AllSections(t *testing.T) {
	sections := []string{
		SectionSummary,
		SectionSessions,
		SectionWork,
		SectionAlerts,
		SectionAttention,
	}

	for _, name := range sections {
		t.Run(name, func(t *testing.T) {
			hints := DefaultSectionFormatHints(name)

			if hints.CompactLabel == "" {
				t.Error("expected non-empty CompactLabel")
			}
			if hints.MarkdownHeading == "" {
				t.Error("expected non-empty MarkdownHeading")
			}
		})
	}
}

func TestFormatHints_TerseFormat(t *testing.T) {
	// Verify key sections have terse format hints
	sections := []string{
		SectionSummary,
		SectionSessions,
		SectionWork,
		SectionAlerts,
		SectionAttention,
	}

	for _, name := range sections {
		t.Run(name, func(t *testing.T) {
			hints := DefaultSectionFormatHints(name)
			if hints.TerseFormat == "" {
				t.Errorf("section %s should have TerseFormat hint", name)
			}
		})
	}
}

// =============================================================================
// Empty Array Semantics Tests
// =============================================================================

// =============================================================================
// Alerts Section Tests
// =============================================================================
