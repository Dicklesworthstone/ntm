package ensemble

import "testing"

func logTestStartContribution(t *testing.T, input any) {
	t.Helper()
	t.Logf("TEST: %s - starting with input: %v", t.Name(), input)
}

func logTestResultContribution(t *testing.T, result any) {
	t.Helper()
	t.Logf("TEST: %s - got result: %v", t.Name(), result)
}

func assertTrueContribution(t *testing.T, desc string, ok bool) {
	t.Helper()
	t.Logf("TEST: %s - assertion: %s", t.Name(), desc)
	if !ok {
		t.Fatalf("assertion failed: %s", desc)
	}
}

func assertEqualContribution(t *testing.T, desc string, got, want any) {
	t.Helper()
	t.Logf("TEST: %s - assertion: %s", t.Name(), desc)
	if got != want {
		t.Fatalf("%s: got %v want %v", desc, got, want)
	}
}

func scoreByMode(report *ContributionReport, modeID string) ContributionScore {
	if report == nil {
		return ContributionScore{}
	}
	for _, score := range report.Scores {
		if score.ModeID == modeID {
			return score
		}
	}
	return ContributionScore{}
}

func TestContribution_SingleMode(t *testing.T) {
	input := map[string]any{
		"mode":     "mode-a",
		"findings": 2,
	}
	logTestStartContribution(t, input)

	tracker := NewContributionTracker()
	tracker.RecordOriginalFinding("mode-a")
	tracker.RecordOriginalFinding("mode-a")
	tracker.RecordSurvivingFinding("mode-a", "Finding A")
	tracker.RecordUniqueFinding("mode-a", "Finding A")
	tracker.RecordCitation("mode-a")

	report := tracker.GenerateReport()
	logTestResultContribution(t, report)

	assertTrueContribution(t, "report present", report != nil)
	assertEqualContribution(t, "score count", len(report.Scores), 1)
	assertEqualContribution(t, "total findings", report.TotalFindings, 2)
	assertEqualContribution(t, "deduped findings", report.DedupedFindings, 1)
}

func TestContribution_MultiMode(t *testing.T) {
	input := map[string]any{
		"modes": []string{"mode-a", "mode-b"},
	}
	logTestStartContribution(t, input)

	tracker := NewContributionTracker()
	tracker.RecordOriginalFinding("mode-a")
	tracker.RecordOriginalFinding("mode-b")
	tracker.RecordSurvivingFinding("mode-a", "Finding A")
	tracker.RecordSurvivingFinding("mode-b", "Finding B")
	tracker.RecordUniqueFinding("mode-a", "Finding A")
	tracker.RecordUniqueFinding("mode-b", "Finding B")

	report := tracker.GenerateReport()
	logTestResultContribution(t, report)

	assertEqualContribution(t, "score count", len(report.Scores), 2)
	assertTrueContribution(t, "ranks assigned", report.Scores[0].Rank > 0 && report.Scores[1].Rank > 0)
}

func TestContribution_UniqueInsights(t *testing.T) {
	input := map[string]any{"mode": "mode-unique"}
	logTestStartContribution(t, input)

	tracker := NewContributionTracker()
	tracker.RecordUniqueFinding("mode-unique", "Unique insight")

	report := tracker.GenerateReport()
	logTestResultContribution(t, report)

	assertEqualContribution(t, "unique insights", report.Scores[0].UniqueInsights, 1)
	assertTrueContribution(t, "highlight stored", len(report.Scores[0].HighlightFindings) > 0)
}

func TestContribution_DeduplicatedFindings(t *testing.T) {
	input := map[string]any{"merged": "multi-source"}
	logTestStartContribution(t, input)

	tracker := NewContributionTracker()
	merged := &MergedOutput{
		Findings: []MergedFinding{
			{
				Finding:     Finding{Finding: "Shared finding"},
				SourceModes: []string{"mode-a", "mode-b"},
			},
		},
	}
	TrackContributionsFromMerge(tracker, merged)

	report := tracker.GenerateReport()
	logTestResultContribution(t, report)

	modeA := scoreByMode(report, "mode-a")
	modeB := scoreByMode(report, "mode-b")
	assertEqualContribution(t, "unique insights mode-a", modeA.UniqueInsights, 0)
	assertEqualContribution(t, "unique insights mode-b", modeB.UniqueInsights, 0)
}
