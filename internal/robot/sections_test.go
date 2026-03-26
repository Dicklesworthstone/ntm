package robot

import (
	"testing"
)

func TestProjectSections_EmptySnapshot(t *testing.T) {
	snapshot := &SnapshotOutput{}
	opts := SectionProjectionOptions{}

	proj := ProjectSections(snapshot, opts)

	if proj == nil {
		t.Fatal("expected non-nil projection")
	}
	if len(proj.Sections) == 0 {
		t.Fatal("expected at least one section")
	}

	// Summary should always be included
	found := false
	for _, s := range proj.Sections {
		if s.Name == SectionSummary {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected summary section")
	}
}

func TestProjectSections_WithSessions(t *testing.T) {
	snapshot := &SnapshotOutput{
		Sessions: []SnapshotSession{
			{Name: "proj-a"},
			{Name: "proj-b"},
			{Name: "proj-c"},
		},
	}
	opts := SectionProjectionOptions{
		Limits: SectionLimits{Sessions: 2},
	}

	proj := ProjectSections(snapshot, opts)

	var sessSection *ProjectedSection
	for i := range proj.Sections {
		if proj.Sections[i].Name == SectionSessions {
			sessSection = &proj.Sections[i]
			break
		}
	}

	if sessSection == nil {
		t.Fatal("expected sessions section")
	}

	if !sessSection.IsTruncated() {
		t.Error("expected sessions to be truncated")
	}

	if sessSection.Truncation.OriginalCount != 3 {
		t.Errorf("expected original count 3, got %d", sessSection.Truncation.OriginalCount)
	}

	if sessSection.Truncation.TruncatedCount != 1 {
		t.Errorf("expected truncated count 1, got %d", sessSection.Truncation.TruncatedCount)
	}
}

func TestProjectSections_SessionFilter(t *testing.T) {
	snapshot := &SnapshotOutput{
		Sessions: []SnapshotSession{
			{Name: "proj-a"},
			{Name: "proj-b"},
			{Name: "proj-c"},
		},
	}
	opts := SectionProjectionOptions{
		SessionFilter: "proj-b",
	}

	proj := ProjectSections(snapshot, opts)

	var sessSection *ProjectedSection
	for i := range proj.Sections {
		if proj.Sections[i].Name == SectionSessions {
			sessSection = &proj.Sections[i]
			break
		}
	}

	if sessSection == nil {
		t.Fatal("expected sessions section")
	}

	sessions, ok := sessSection.Data.([]SnapshotSession)
	if !ok {
		t.Fatalf("expected []SnapshotSession, got %T", sessSection.Data)
	}

	if len(sessions) != 1 {
		t.Errorf("expected 1 filtered session, got %d", len(sessions))
	}

	if sessions[0].Name != "proj-b" {
		t.Errorf("expected session proj-b, got %s", sessions[0].Name)
	}
}

func TestProjectSections_IncludeExclude(t *testing.T) {
	snapshot := &SnapshotOutput{}

	// Include only summary and sessions
	opts := SectionProjectionOptions{
		IncludeSections: []string{SectionSummary, SectionSessions},
	}

	proj := ProjectSections(snapshot, opts)

	if len(proj.Sections) != 2 {
		t.Errorf("expected 2 sections, got %d", len(proj.Sections))
	}

	// Verify work, alerts, attention are omitted
	if _, ok := proj.Metadata.SectionsOmitted[SectionWork]; !ok {
		t.Error("expected work section to be in omitted list")
	}
}

func TestProjectSections_SectionOrdering(t *testing.T) {
	snapshot := &SnapshotOutput{}
	opts := SectionProjectionOptions{}

	proj := ProjectSections(snapshot, opts)

	// Verify sections are in order by weight
	prevWeight := -1
	for _, s := range proj.Sections {
		if s.OrderWeight < prevWeight {
			t.Errorf("sections out of order: %s (weight %d) after weight %d",
				s.Name, s.OrderWeight, prevWeight)
		}
		prevWeight = s.OrderWeight
	}
}

func TestDefaultSectionLimits(t *testing.T) {
	limits := DefaultSectionLimits()

	if limits.Sessions <= 0 {
		t.Error("expected positive session limit")
	}
	if limits.Alerts <= 0 {
		t.Error("expected positive alerts limit")
	}
}

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

func TestDashboardSectionLimits(t *testing.T) {
	limits := DashboardSectionLimits()

	// Dashboard limits should be higher than default
	defaults := DefaultSectionLimits()

	if limits.Sessions <= defaults.Sessions {
		t.Errorf("dashboard sessions limit (%d) should exceed default (%d)",
			limits.Sessions, defaults.Sessions)
	}
	if limits.Alerts <= defaults.Alerts {
		t.Errorf("dashboard alerts limit (%d) should exceed default (%d)",
			limits.Alerts, defaults.Alerts)
	}
	if limits.AttentionAction <= defaults.AttentionAction {
		t.Errorf("dashboard attention_action limit (%d) should exceed default (%d)",
			limits.AttentionAction, defaults.AttentionAction)
	}
}

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

func TestGetTerseProjection(t *testing.T) {
	snapshot := &SnapshotOutput{
		Sessions: []SnapshotSession{
			{Name: "proj-a"},
			{Name: "proj-b"},
		},
	}

	proj := GetTerseProjection(snapshot)

	if proj == nil {
		t.Fatal("expected non-nil projection")
	}

	// Should use terse limits
	var sessSection *ProjectedSection
	for i := range proj.Sections {
		if proj.Sections[i].Name == SectionSessions {
			sessSection = &proj.Sections[i]
			break
		}
	}

	if sessSection == nil {
		t.Fatal("expected sessions section")
	}

	// Terse limits are lower, so with 2 sessions it should not truncate
	// (TerseSectionLimits().Sessions = 5)
	sessions, ok := sessSection.Data.([]SnapshotSession)
	if !ok {
		t.Fatalf("expected []SnapshotSession, got %T", sessSection.Data)
	}
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(sessions))
	}
}

func TestGetTerseProjection_Truncation(t *testing.T) {
	// Create more sessions than terse limit
	sessions := make([]SnapshotSession, TerseSectionLimits().Sessions+3)
	for i := range sessions {
		sessions[i] = SnapshotSession{Name: "proj-" + string(rune('a'+i))}
	}

	snapshot := &SnapshotOutput{Sessions: sessions}
	proj := GetTerseProjection(snapshot)

	var sessSection *ProjectedSection
	for i := range proj.Sections {
		if proj.Sections[i].Name == SectionSessions {
			sessSection = &proj.Sections[i]
			break
		}
	}

	if sessSection == nil {
		t.Fatal("expected sessions section")
	}

	if !sessSection.IsTruncated() {
		t.Error("expected sessions to be truncated in terse projection")
	}

	if sessSection.Truncation.OriginalCount != len(sessions) {
		t.Errorf("expected original count %d, got %d",
			len(sessions), sessSection.Truncation.OriginalCount)
	}
}

// =============================================================================
// Section Limit Tier Tests
// =============================================================================

func TestSectionLimitTiers(t *testing.T) {
	// Verify limit tiers are properly ordered: terse < compact < default < dashboard
	terse := TerseSectionLimits()
	compact := CompactSectionLimits()
	defaults := DefaultSectionLimits()
	dashboard := DashboardSectionLimits()

	testCases := []struct {
		name    string
		getTier func(SectionLimits) int
	}{
		{"Sessions", func(l SectionLimits) int { return l.Sessions }},
		{"Alerts", func(l SectionLimits) int { return l.Alerts }},
		{"AttentionAction", func(l SectionLimits) int { return l.AttentionAction }},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tVal := tc.getTier(terse)
			cVal := tc.getTier(compact)
			dVal := tc.getTier(defaults)
			dashVal := tc.getTier(dashboard)

			if tVal >= cVal {
				t.Errorf("%s: terse (%d) should be < compact (%d)", tc.name, tVal, cVal)
			}
			if cVal >= dVal {
				t.Errorf("%s: compact (%d) should be < default (%d)", tc.name, cVal, dVal)
			}
			if dVal >= dashVal {
				t.Errorf("%s: default (%d) should be < dashboard (%d)", tc.name, dVal, dashVal)
			}
		})
	}
}

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

func TestProjectSections_EmptyArraySemantics(t *testing.T) {
	snapshot := &SnapshotOutput{
		Sessions:       []SnapshotSession{}, // Empty, not nil
		Alerts:         []string{},          // Empty, not nil
		AlertsDetailed: []AlertInfo{},       // Empty, not nil
	}
	opts := SectionProjectionOptions{}

	proj := ProjectSections(snapshot, opts)

	// Find sessions section
	var sessSection *ProjectedSection
	for i := range proj.Sections {
		if proj.Sections[i].Name == SectionSessions {
			sessSection = &proj.Sections[i]
			break
		}
	}

	if sessSection == nil {
		t.Fatal("expected sessions section")
	}

	// Data should be empty slice, not nil
	sessions, ok := sessSection.Data.([]SnapshotSession)
	if !ok {
		t.Fatalf("expected []SnapshotSession, got %T", sessSection.Data)
	}
	if sessions == nil {
		t.Error("expected empty slice, got nil")
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(sessions))
	}

	// Empty arrays should NOT be truncated
	if sessSection.IsTruncated() {
		t.Error("empty section should not be marked as truncated")
	}
}

// =============================================================================
// Alerts Section Tests
// =============================================================================

func TestProjectSections_AlertsTruncation(t *testing.T) {
	// Create more alerts than default limit
	alertCount := DefaultSectionLimits().Alerts + 5
	alerts := make([]AlertInfo, alertCount)
	for i := range alerts {
		alerts[i] = AlertInfo{
			ID:       "alert-" + string(rune('a'+i)),
			Severity: "warning",
			Message:  "test alert",
		}
	}

	snapshot := &SnapshotOutput{
		AlertsDetailed: alerts,
	}
	opts := SectionProjectionOptions{}

	proj := ProjectSections(snapshot, opts)

	var alertSection *ProjectedSection
	for i := range proj.Sections {
		if proj.Sections[i].Name == SectionAlerts {
			alertSection = &proj.Sections[i]
			break
		}
	}

	if alertSection == nil {
		t.Fatal("expected alerts section")
	}

	if !alertSection.IsTruncated() {
		t.Error("expected alerts to be truncated")
	}

	if alertSection.Truncation.OriginalCount != alertCount {
		t.Errorf("expected original count %d, got %d",
			alertCount, alertSection.Truncation.OriginalCount)
	}
}
