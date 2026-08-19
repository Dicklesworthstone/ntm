package ideation

import (
	"strings"
	"testing"
)

func TestBuildEffectivenessReportCleanClosedGeneratedBeads(t *testing.T) {
	events := []EffectivenessEvent{
		{Kind: EffectivenessEventCandidateGenerated, CandidateID: "queue-proof", CandidateFamily: "queue-dry", SourceID: "br:closed", Evidence: []string{"generated from queue dry"}},
		{Kind: EffectivenessEventCandidateRendered, CandidateID: "queue-proof", CandidateFamily: "queue-dry", SourceID: "br:closed", Evidence: []string{"rendered preview"}},
		{Kind: EffectivenessEventBeadCreated, CandidateID: "queue-proof", CandidateFamily: "queue-dry", SourceID: "br:closed", BeadID: "bd-clean"},
		{Kind: EffectivenessEventBeadClosed, CandidateID: "queue-proof", CandidateFamily: "queue-dry", SourceID: "br:closed", BeadID: "bd-clean", DurationHours: 6.5, Evidence: []string{"verification passed"}},
	}

	report := BuildEffectivenessReport(events, EffectivenessOptions{ScenarioID: "clean"})

	if !report.HistoryAvailable || !report.AdvisoryOnly {
		t.Fatalf("availability/advisory mismatch: %+v", report)
	}
	if report.CandidateGeneratedCount != 1 || report.RenderedCount != 1 || report.CreatedCount != 1 || report.ClosedCount != 1 {
		t.Fatalf("counts mismatch: %+v", report)
	}
	if report.AverageTimeToCloseHours != 6.5 {
		t.Fatalf("average close hours=%f, want 6.5", report.AverageTimeToCloseHours)
	}
	if !containsString(report.CleanFamilyIDs, "queue-dry") {
		t.Fatalf("clean families=%v, want queue-dry", report.CleanFamilyIDs)
	}
	if len(report.Families) != 1 {
		t.Fatalf("families=%+v, want one family", report.Families)
	}
	if !effectivenessOutcomeIs(report.Families[0].Outcome, EffectivenessOutcomeClean) {
		t.Fatalf("families=%+v, want clean outcome", report.Families)
	}
	if len(report.Signals) == 0 || strings.Compare(report.Signals[0].Kind, "effectiveness_family") != 0 {
		t.Fatalf("signals=%+v, want clean family signal", report.Signals)
	}
}

func TestBuildEffectivenessReportReopenedBuggyGeneratedBeads(t *testing.T) {
	events := []EffectivenessEvent{
		{Kind: EffectivenessEventCandidateGenerated, CandidateID: "buggy", CandidateFamily: "robot-replay", SourceID: "cass:context"},
		{Kind: EffectivenessEventBeadCreated, CandidateID: "buggy", CandidateFamily: "robot-replay", SourceID: "cass:context", BeadID: "bd-buggy"},
		{Kind: EffectivenessEventBeadClosed, CandidateID: "buggy", CandidateFamily: "robot-replay", SourceID: "cass:context", BeadID: "bd-buggy", DurationHours: 2},
		{Kind: EffectivenessEventBeadReopened, CandidateID: "buggy", CandidateFamily: "robot-replay", SourceID: "cass:context", BeadID: "bd-buggy", Evidence: []string{"regression reopened"}},
		{Kind: EffectivenessEventFollowUpBug, CandidateID: "buggy", CandidateFamily: "robot-replay", SourceID: "cass:context", BeadID: "bd-follow"},
		{Kind: EffectivenessEventVerificationFailed, CandidateID: "buggy", CandidateFamily: "robot-replay", SourceID: "cass:context", Reason: "go test failed"},
	}

	report := BuildEffectivenessReport(events, EffectivenessOptions{ScenarioID: "buggy"})

	if !containsString(report.ChurnFamilyIDs, "robot-replay") {
		t.Fatalf("churn families=%v, want robot-replay", report.ChurnFamilyIDs)
	}
	if containsString(report.CleanFamilyIDs, "robot-replay") {
		t.Fatalf("clean families=%v should not include churn family", report.CleanFamilyIDs)
	}
	if report.ReopenedCount != 1 || report.FollowUpBugCount != 1 || report.VerificationFailureCount != 1 {
		t.Fatalf("churn counts mismatch: %+v", report)
	}
	if len(report.Families) != 1 {
		t.Fatalf("family summary=%+v, want one family", report.Families)
	}
	if !effectivenessOutcomeIs(report.Families[0].Outcome, EffectivenessOutcomeChurn) || report.Families[0].Score >= 0 {
		t.Fatalf("family summary=%+v, want negative churn score", report.Families)
	}
	if len(report.Signals) == 0 || !strings.Contains(report.Signals[0].Summary, "verification_failures=1") {
		t.Fatalf("signals=%+v, want verification failure summary", report.Signals)
	}
}

func TestBuildEffectivenessReportAbsentHistoryDegrades(t *testing.T) {
	report := BuildEffectivenessReport(nil, EffectivenessOptions{ScenarioID: "absent"})

	if report.HistoryAvailable {
		t.Fatalf("HistoryAvailable=true, want false")
	}
	if !report.AdvisoryOnly {
		t.Fatalf("AdvisoryOnly=false, want true")
	}
	if !hasNoteCodeIn(report.Notes, "effectiveness_history_absent") {
		t.Fatalf("notes=%+v, want absent-history note", report.Notes)
	}
}

func TestBuildEffectivenessReportStableAggregateOrdering(t *testing.T) {
	events := []EffectivenessEvent{
		{Kind: EffectivenessEventOperatorRejected, CandidateID: "z", CandidateFamily: "z-family", SourceID: "z-source", Reason: "operator said no"},
		{Kind: EffectivenessEventBeadClosed, CandidateID: "a", CandidateFamily: "a-family", SourceID: "a-source", DurationHours: 3},
		{Kind: EffectivenessEventCandidateGenerated, CandidateID: "a", CandidateFamily: "a-family", SourceID: "a-source"},
		{Kind: EffectivenessEventDuplicateSuppressed, CandidateID: "m", CandidateFamily: "m-family", SourceID: "m-source"},
	}

	first := BuildEffectivenessReport(events, EffectivenessOptions{ScenarioID: "stable"})
	second := BuildEffectivenessReport([]EffectivenessEvent{events[3], events[1], events[0], events[2]}, EffectivenessOptions{ScenarioID: "stable"})

	firstJSON := mustMarshalJSON(t, first)
	secondJSON := mustMarshalJSON(t, second)
	if !textIs(firstJSON, secondJSON) {
		t.Fatalf("effectiveness report JSON not stable\nfirst:  %s\nsecond: %s", firstJSON, secondJSON)
	}
	if len(first.Families) != 3 {
		t.Fatalf("family order=%+v, want three families", first.Families)
	}
	if !textIs(first.Families[0].FamilyID, "a-family") || !textIs(first.Families[1].FamilyID, "m-family") || !textIs(first.Families[2].FamilyID, "z-family") {
		t.Fatalf("family order=%+v, want clean, low-yield sorted by family", first.Families)
	}
	if !containsString(first.LowYieldSourceIDs, "m-source") || !containsString(first.LowYieldSourceIDs, "z-source") {
		t.Fatalf("low-yield sources=%v, want duplicate/rejected sources", first.LowYieldSourceIDs)
	}
}

func effectivenessOutcomeIs(got, want EffectivenessOutcome) bool {
	return strings.Compare(string(got), string(want)) == 0
}

func textIs(got, want string) bool {
	return strings.Compare(got, want) == 0
}

func hasNoteCodeIn(notes []ValidationNote, code string) bool {
	for _, n := range notes {
		if n.Code == code {
			return true
		}
	}
	return false
}
