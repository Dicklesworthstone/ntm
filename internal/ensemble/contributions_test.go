package ensemble

import (
	"encoding/json"
	"testing"
)

func TestNewContributionTracker(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tracker := NewContributionTracker()
	if tracker == nil {
		t.Fatal("NewContributionTracker returned nil")
	}
	if tracker.modeScores == nil {
		t.Error("modeScores should be initialized")
	}
	if tracker.Config.FindingsWeight == 0 {
		t.Error("Config should have default weights")
	}

	t.Logf("TEST: %s - assertion: tracker created with defaults", t.Name())
}

func TestContributionTracker_RecordOriginalFinding(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tracker := NewContributionTracker()
	tracker.RecordOriginalFinding("mode-a")
	tracker.RecordOriginalFinding("mode-a")
	tracker.RecordOriginalFinding("mode-b")

	t.Logf("TEST: %s - recorded findings for mode-a and mode-b", t.Name())

	if tracker.modeScores["mode-a"].OriginalFindings != 2 {
		t.Errorf("mode-a OriginalFindings = %d, want 2", tracker.modeScores["mode-a"].OriginalFindings)
	}
	if tracker.modeScores["mode-b"].OriginalFindings != 1 {
		t.Errorf("mode-b OriginalFindings = %d, want 1", tracker.modeScores["mode-b"].OriginalFindings)
	}

	t.Logf("TEST: %s - assertion: original findings recorded correctly", t.Name())
}

func TestContributionTracker_RecordSurvivingFinding(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tracker := NewContributionTracker()
	tracker.RecordSurvivingFinding("mode-a", "Finding 1")
	tracker.RecordSurvivingFinding("mode-a", "Finding 2")

	t.Logf("TEST: %s - recorded surviving findings", t.Name())

	if tracker.modeScores["mode-a"].FindingsCount != 2 {
		t.Errorf("FindingsCount = %d, want 2", tracker.modeScores["mode-a"].FindingsCount)
	}

	t.Logf("TEST: %s - assertion: surviving findings recorded", t.Name())
}

func TestContributionTracker_RecordUniqueFinding(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tracker := NewContributionTracker()
	tracker.RecordUniqueFinding("mode-a", "This is a unique insight from mode-a")
	tracker.RecordUniqueFinding("mode-a", "Another unique finding")
	tracker.RecordUniqueFinding("mode-a", "Third unique finding")
	tracker.RecordUniqueFinding("mode-a", "Fourth unique finding") // Should exceed MaxHighlights

	score := tracker.modeScores["mode-a"]

	t.Logf("TEST: %s - uniqueInsights=%d highlights=%d", t.Name(), score.UniqueInsights, len(score.HighlightFindings))

	if score.UniqueInsights != 4 {
		t.Errorf("UniqueInsights = %d, want 4", score.UniqueInsights)
	}
	if len(score.HighlightFindings) != 3 { // MaxHighlights default is 3
		t.Errorf("HighlightFindings = %d, want 3 (capped)", len(score.HighlightFindings))
	}

	t.Logf("TEST: %s - assertion: unique findings tracked with highlight cap", t.Name())
}

func TestContributionTracker_RecordCitation(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tracker := NewContributionTracker()
	tracker.RecordCitation("mode-a")
	tracker.RecordCitation("mode-a")
	tracker.RecordCitation("mode-b")

	t.Logf("TEST: %s - recorded citations", t.Name())

	if tracker.modeScores["mode-a"].CitationCount != 2 {
		t.Errorf("mode-a CitationCount = %d, want 2", tracker.modeScores["mode-a"].CitationCount)
	}

	t.Logf("TEST: %s - assertion: citations recorded correctly", t.Name())
}

func TestTrackContributionsFromMerge(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tracker := NewContributionTracker()

	merged := &MergedOutput{
		Findings: []MergedFinding{
			{
				Finding:     Finding{Finding: "Shared finding", Impact: ImpactHigh, Confidence: 0.9},
				SourceModes: []string{"mode-a", "mode-b"},
			},
			{
				Finding:     Finding{Finding: "Unique to mode-a", Impact: ImpactMedium, Confidence: 0.8},
				SourceModes: []string{"mode-a"},
			},
		},
		Risks: []MergedRisk{
			{
				Risk:        Risk{Risk: "Risk 1", Impact: ImpactHigh, Likelihood: 0.7},
				SourceModes: []string{"mode-a"},
			},
		},
		Recommendations: []MergedRecommendation{
			{
				Recommendation: Recommendation{Recommendation: "Rec 1", Priority: ImpactHigh},
				SourceModes:    []string{"mode-b"},
			},
		},
	}

	TrackContributionsFromMerge(tracker, merged)

	t.Logf("TEST: %s - tracked contributions from merge", t.Name())

	// mode-a: 2 surviving (shared + unique), 1 unique, 1 risk
	if tracker.modeScores["mode-a"].FindingsCount != 2 {
		t.Errorf("mode-a FindingsCount = %d, want 2", tracker.modeScores["mode-a"].FindingsCount)
	}
	if tracker.modeScores["mode-a"].UniqueInsights != 1 {
		t.Errorf("mode-a UniqueInsights = %d, want 1", tracker.modeScores["mode-a"].UniqueInsights)
	}
	if tracker.modeScores["mode-a"].RisksCount != 1 {
		t.Errorf("mode-a RisksCount = %d, want 1", tracker.modeScores["mode-a"].RisksCount)
	}

	// mode-b: 1 surviving (shared), 0 unique, 1 rec
	if tracker.modeScores["mode-b"].FindingsCount != 1 {
		t.Errorf("mode-b FindingsCount = %d, want 1", tracker.modeScores["mode-b"].FindingsCount)
	}
	if tracker.modeScores["mode-b"].UniqueInsights != 0 {
		t.Errorf("mode-b UniqueInsights = %d, want 0", tracker.modeScores["mode-b"].UniqueInsights)
	}
	if tracker.modeScores["mode-b"].RecommendationsCount != 1 {
		t.Errorf("mode-b RecommendationsCount = %d, want 1", tracker.modeScores["mode-b"].RecommendationsCount)
	}

	t.Logf("TEST: %s - assertion: merge contributions tracked correctly", t.Name())
}

func TestTrackOriginalFindings(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tracker := NewContributionTracker()

	outputs := []ModeOutput{
		{
			ModeID:      "mode-a",
			Thesis:      "A",
			Confidence:  0.9,
			TopFindings: []Finding{{Finding: "F1"}, {Finding: "F2"}},
		},
		{
			ModeID:      "mode-b",
			Thesis:      "B",
			Confidence:  0.8,
			TopFindings: []Finding{{Finding: "F1"}},
		},
	}

	TrackOriginalFindings(tracker, outputs)

	t.Logf("TEST: %s - tracked original findings", t.Name())

	if tracker.modeScores["mode-a"].OriginalFindings != 2 {
		t.Errorf("mode-a OriginalFindings = %d, want 2", tracker.modeScores["mode-a"].OriginalFindings)
	}
	if tracker.modeScores["mode-b"].OriginalFindings != 1 {
		t.Errorf("mode-b OriginalFindings = %d, want 1", tracker.modeScores["mode-b"].OriginalFindings)
	}

	t.Logf("TEST: %s - assertion: original findings tracked from outputs", t.Name())
}

func TestDefaultContributionConfig_WeightsSumToOne(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	cfg := DefaultContributionConfig()
	total := cfg.FindingsWeight + cfg.UniqueWeight + cfg.CitationWeight + cfg.RisksWeight + cfg.RecommendationsWeight

	t.Logf("TEST: %s - total weight = %.2f", t.Name(), total)

	if total < 0.99 || total > 1.01 {
		t.Errorf("Weights should sum to 1.0, got %.2f", total)
	}

	t.Logf("TEST: %s - assertion: weights sum to 1.0", t.Name())
}

func TestContributionTracker_GenerateReport_Empty(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tracker := NewContributionTracker()
	report := tracker.GenerateReport()

	t.Logf("TEST: %s - generated empty report", t.Name())

	if report == nil {
		t.Fatal("Report should not be nil")
	}
	if len(report.Scores) != 0 {
		t.Errorf("Scores = %d, want 0", len(report.Scores))
	}

	t.Logf("TEST: %s - assertion: empty report generated", t.Name())
}

func TestContributionTracker_GenerateReport(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tracker := NewContributionTracker()

	// Mode A: 5 original, 3 surviving, 2 unique
	for i := 0; i < 5; i++ {
		tracker.RecordOriginalFinding("mode-a")
	}
	tracker.RecordSurvivingFinding("mode-a", "F1")
	tracker.RecordSurvivingFinding("mode-a", "F2")
	tracker.RecordSurvivingFinding("mode-a", "F3")
	tracker.RecordUniqueFinding("mode-a", "U1")
	tracker.RecordUniqueFinding("mode-a", "U2")
	tracker.RecordCitation("mode-a")
	tracker.RecordCitation("mode-a")

	// Mode B: 3 original, 1 surviving, 0 unique
	for i := 0; i < 3; i++ {
		tracker.RecordOriginalFinding("mode-b")
	}
	tracker.RecordSurvivingFinding("mode-b", "F1")
	tracker.RecordCitation("mode-b")

	tracker.SetModeName("mode-a", "Deductive Logic")
	tracker.SetModeName("mode-b", "Bayesian Reasoning")

	report := tracker.GenerateReport()

	t.Logf("TEST: %s - report: total=%d deduped=%d overlap=%.2f diversity=%.2f",
		t.Name(), report.TotalFindings, report.DedupedFindings, report.OverlapRate, report.DiversityScore)

	if report.TotalFindings != 8 {
		t.Errorf("TotalFindings = %d, want 8", report.TotalFindings)
	}
	if report.DedupedFindings != 4 {
		t.Errorf("DedupedFindings = %d, want 4", report.DedupedFindings)
	}
	if len(report.Scores) != 2 {
		t.Errorf("Scores = %d, want 2", len(report.Scores))
	}

	// Mode A should rank higher
	if report.Scores[0].ModeID != "mode-a" {
		t.Errorf("Top mode = %s, want mode-a", report.Scores[0].ModeID)
	}
	if report.Scores[0].Rank != 1 {
		t.Errorf("Top rank = %d, want 1", report.Scores[0].Rank)
	}
	if report.Scores[0].ModeName != "Deductive Logic" {
		t.Errorf("ModeName = %s, want Deductive Logic", report.Scores[0].ModeName)
	}

	t.Logf("TEST: %s - assertion: report computed with rankings", t.Name())
}

func TestContributionTracker_GenerateReport_Scores(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tracker := NewContributionTracker()

	// Equal contribution from two modes
	tracker.RecordOriginalFinding("mode-a")
	tracker.RecordOriginalFinding("mode-b")
	tracker.RecordSurvivingFinding("mode-a", "F1")
	tracker.RecordSurvivingFinding("mode-b", "F1")
	tracker.RecordUniqueFinding("mode-a", "U1")
	tracker.RecordUniqueFinding("mode-b", "U1")

	report := tracker.GenerateReport()

	t.Logf("TEST: %s - mode-a score=%.2f mode-b score=%.2f",
		t.Name(), report.Scores[0].Score, report.Scores[1].Score)

	// With equal contributions, scores should be equal (or very close)
	diff := report.Scores[0].Score - report.Scores[1].Score
	if diff < -1 || diff > 1 {
		t.Errorf("Scores should be equal for equal contributions, got diff=%.2f", diff)
	}

	// Scores should sum to 100 (with only findings and unique components)
	total := report.Scores[0].Score + report.Scores[1].Score
	// Expected: findings weight (0.4) + unique weight (0.3) = 0.7 * 100 = 70
	if total < 60 || total > 80 {
		t.Errorf("Total scores = %.2f, expected around 70 (findings + unique weights)", total)
	}

	t.Logf("TEST: %s - assertion: scores computed correctly", t.Name())
}

func TestContributionTracker_NilSafe(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	var tracker *ContributionTracker

	// All methods should be nil-safe
	tracker.RecordOriginalFinding("mode-a")
	tracker.RecordSurvivingFinding("mode-a", "F")
	tracker.RecordUniqueFinding("mode-a", "U")
	tracker.RecordCitation("mode-a")
	tracker.RecordRisk("mode-a")
	tracker.RecordRecommendation("mode-a")
	tracker.SetModeName("mode-a", "Test")

	report := tracker.GenerateReport()
	if report != nil {
		t.Error("GenerateReport on nil should return nil")
	}

	t.Logf("TEST: %s - assertion: nil receiver is safe", t.Name())
}

func TestContributionReport_JSON(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tracker := NewContributionTracker()
	tracker.RecordOriginalFinding("mode-a")
	tracker.RecordSurvivingFinding("mode-a", "F")
	report := tracker.GenerateReport()

	data, err := report.JSON()
	if err != nil {
		t.Fatalf("JSON error: %v", err)
	}

	t.Logf("TEST: %s - JSON: %s", t.Name(), string(data))

	// Verify valid JSON
	var decoded ContributionReport
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Errorf("JSON should be valid: %v", err)
	}

	t.Logf("TEST: %s - assertion: JSON round-trips correctly", t.Name())
}
