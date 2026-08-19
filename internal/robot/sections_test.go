package robot

import (
	"testing"
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
	section := GetDashboardAttentionSection(DashboardSectionLimits())

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
	section := GetDashboardAttentionSection(DashboardSectionLimits())

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
