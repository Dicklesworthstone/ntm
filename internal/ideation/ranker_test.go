package ideation

import (
	"encoding/json"
	"testing"
)

func TestRankCandidatesSelectsTopFiveAndNextTenFromThirty(t *testing.T) {
	snapshot := NewIdeaEvidenceSnapshot("/repo")
	snapshot.Queue.CountsVerified = true
	snapshot.RecordSource(CandidateSource{ID: "br", Kind: SourceBR, Available: true, Evidence: []string{"ready queue verified"}})
	snapshot.Candidates = syntheticCandidates(30)

	result := RankCandidates(snapshot, DefaultRankOptions())
	if result.Decision != RankingDecisionIdeate {
		t.Fatalf("decision=%q, want ideate", result.Decision)
	}
	if result.CandidateCount != 30 {
		t.Fatalf("candidate_count=%d, want 30", result.CandidateCount)
	}
	if len(result.Selected) != 5 {
		t.Fatalf("selected=%d, want 5", len(result.Selected))
	}
	if len(result.NextBest) != 10 {
		t.Fatalf("next_best=%d, want 10", len(result.NextBest))
	}
	if len(result.Suppressed) != 0 {
		t.Fatalf("suppressed=%d, want 0", len(result.Suppressed))
	}
	for i, item := range result.Selected {
		if item.Rank != i+1 {
			t.Fatalf("selected[%d].rank=%d, want %d", i, item.Rank, i+1)
		}
	}
	for i, item := range result.NextBest {
		wantRank := i + 6
		if item.Rank != wantRank {
			t.Fatalf("next_best[%d].rank=%d, want %d", i, item.Rank, wantRank)
		}
	}
}

func TestRankCandidatesSuppressesDuplicateClosedFamilies(t *testing.T) {
	snapshot := NewIdeaEvidenceSnapshot("/repo")
	snapshot.ExistingWork = []ExistingWorkFingerprint{
		{
			ID:       "bd-2mb03.5",
			FamilyID: "bd-2mb03",
			Title:    "Queue-dry operator autopilot",
			Status:   WorkStatusClosed,
			Labels:   []string{"queue-dry"},
			Keywords: []string{"queue", "dry", "autopilot"},
			Evidence: []string{"closed queue-dry autopilot work"},
		},
	}
	snapshot.Candidates = []IdeaCandidate{
		{
			ID:       "dup",
			Title:    "Queue dry operator autopilot",
			Labels:   []string{"queue-dry"},
			Keywords: []string{"queue", "dry", "autopilot"},
			Evidence: []string{"same work as closed tranche"},
		},
		{
			ID:       "follow-up",
			Title:    "Queue-dry autopilot remaining evidence gap",
			Summary:  "Follow-up for a gap left by the closed autopilot work.",
			Labels:   []string{"queue-dry", "planning"},
			Keywords: []string{"queue", "dry", "evidence"},
			Evidence: []string{"prior work left a gap in evidence rendering"},
			RelatedWork: []RelatedWorkReference{
				{ID: "bd-2mb03.5", Relationship: RelationshipFollowUp, Evidence: []string{"remaining evidence gap"}},
			},
		},
	}

	result := RankCandidates(snapshot, DefaultRankOptions())
	if len(result.Selected) != 1 {
		t.Fatalf("selected=%d, want 1", len(result.Selected))
	}
	if result.Selected[0].Candidate.ID != "follow-up" {
		t.Fatalf("selected=%s, want follow-up", result.Selected[0].Candidate.ID)
	}
	if len(result.Suppressed) != 1 || result.Suppressed[0].Candidate.ID != "dup" {
		t.Fatalf("suppressed=%v, want duplicate candidate", result.Suppressed)
	}
	if result.Suppressed[0].Candidate.Overlap.FamilyID != "bd-2mb03" {
		t.Fatalf("suppressed family=%q, want bd-2mb03", result.Suppressed[0].Candidate.Overlap.FamilyID)
	}
}

func TestRankCandidatesTieBreaksDeterministicallyAndJSONStable(t *testing.T) {
	snapshot := NewIdeaEvidenceSnapshot("/repo")
	snapshot.Candidates = []IdeaCandidate{
		stableTieCandidate("b", "Beta candidate"),
		stableTieCandidate("a", "Alpha candidate"),
	}

	first := RankCandidates(snapshot, RankOptions{TopLimit: 2, NextLimit: 1})
	second := RankCandidates(snapshot, RankOptions{TopLimit: 2, NextLimit: 1})
	if first.Selected[0].Candidate.ID != "a" {
		t.Fatalf("first selected ID=%s, want a", first.Selected[0].Candidate.ID)
	}
	firstJSON := mustMarshalRankJSON(t, first)
	secondJSON := mustMarshalRankJSON(t, second)
	if firstJSON != secondJSON {
		t.Fatalf("rank JSON not stable\nfirst:  %s\nsecond: %s", firstJSON, secondJSON)
	}
}

func TestRankCandidatesDemotesLowEvidenceCandidate(t *testing.T) {
	snapshot := NewIdeaEvidenceSnapshot("/repo")
	snapshot.RecordSource(CandidateSource{ID: "br", Kind: SourceBR, Available: true, Evidence: []string{"queue evidence"}})
	snapshot.Candidates = []IdeaCandidate{
		{
			ID:       "thin",
			Title:    "Queue-dry operator workflow",
			Labels:   []string{"queue-dry", "operator"},
			Keywords: []string{"queue", "operator"},
		},
		{
			ID:        "evidenced",
			Title:     "Queue-dry operator workflow with evidence",
			Summary:   "Use br and bv evidence to explain operator actions.",
			Labels:    []string{"queue-dry", "operator"},
			Keywords:  []string{"queue", "operator", "deterministic"},
			SourceIDs: []string{"br"},
			Evidence:  []string{"ready queue verified", "operator output needs reasons"},
		},
	}

	result := RankCandidates(snapshot, RankOptions{TopLimit: 2, NextLimit: 1})
	if len(result.Selected) != 2 {
		t.Fatalf("selected=%d, want 2", len(result.Selected))
	}
	if result.Selected[0].Candidate.ID != "evidenced" {
		t.Fatalf("top=%s, want evidenced", result.Selected[0].Candidate.ID)
	}
	if result.Selected[1].Factors.DegradedSourceRisk <= result.Selected[0].Factors.DegradedSourceRisk {
		t.Fatalf("thin risk=%f evidenced risk=%f, want thin greater", result.Selected[1].Factors.DegradedSourceRisk, result.Selected[0].Factors.DegradedSourceRisk)
	}
}

func TestNormalizeRankOptionsHonorsExplicitNextLimitZero(t *testing.T) {
	snapshot := NewIdeaEvidenceSnapshot("/repo")
	snapshot.RecordSource(CandidateSource{ID: "br", Kind: SourceBR, Available: true, Evidence: []string{"queue ok"}})
	snapshot.Candidates = []IdeaCandidate{
		{ID: "a", Title: "Alpha", Labels: []string{"queue-dry"}, Evidence: []string{"a"}},
		{ID: "b", Title: "Beta", Labels: []string{"queue-dry"}, Evidence: []string{"b"}},
		{ID: "c", Title: "Gamma", Labels: []string{"queue-dry"}, Evidence: []string{"c"}},
		{ID: "d", Title: "Delta", Labels: []string{"queue-dry"}, Evidence: []string{"d"}},
	}

	zero := RankCandidates(snapshot, RankOptions{TopLimit: 2, NextLimit: 0})
	if len(zero.Selected) != 2 {
		t.Fatalf("zero next: selected=%d, want 2", len(zero.Selected))
	}
	if len(zero.NextBest) != 0 {
		t.Fatalf("zero next: next_best=%d, want 0 (caller asked for none)", len(zero.NextBest))
	}

	negative := RankCandidates(snapshot, RankOptions{TopLimit: 2, NextLimit: -1})
	if len(negative.NextBest) == 0 {
		t.Fatalf("negative next: expected default fill, got %d", len(negative.NextBest))
	}
}

func TestRankCandidatesAllDuplicatesSuggestsReview(t *testing.T) {
	snapshot := NewIdeaEvidenceSnapshot("/repo")
	snapshot.ExistingWork = []ExistingWorkFingerprint{
		{ID: "bd-fxj4f.3", FamilyID: "bd-fxj4f", Title: "Robot contract replay harness", Status: WorkStatusClosed, Keywords: []string{"robot", "contract", "replay"}},
		{ID: "bd-8kglp.4", FamilyID: "bd-8kglp", Title: "RCH build storm backpressure", Status: WorkStatusClosed, Keywords: []string{"rch", "backpressure"}},
	}
	snapshot.Candidates = []IdeaCandidate{
		{ID: "dup-1", Title: "Robot contract replay harness", Keywords: []string{"robot", "contract", "replay"}},
		{ID: "dup-2", Title: "RCH build storm backpressure", Keywords: []string{"rch", "backpressure"}},
	}

	result := RankCandidates(snapshot, DefaultRankOptions())
	if result.Decision != RankingDecisionReviewRecentWork {
		t.Fatalf("decision=%q, want review_recent_work", result.Decision)
	}
	if len(result.Selected) != 0 {
		t.Fatalf("selected=%d, want 0", len(result.Selected))
	}
	if len(result.Suppressed) != 2 {
		t.Fatalf("suppressed=%d, want 2", len(result.Suppressed))
	}
}

func syntheticCandidates(count int) []IdeaCandidate {
	out := make([]IdeaCandidate, 0, count)
	keywords := [][]string{
		{"queue", "operator", "deterministic", "test"},
		{"performance", "scale", "memory"},
		{"reliability", "contract", "golden"},
		{"cli", "workflow", "planning"},
		{"safety", "guard", "resilience"},
	}
	for i := 0; i < count; i++ {
		out = append(out, IdeaCandidate{
			ID:        "candidate-" + twoDigit(i+1),
			Title:     "Candidate " + twoDigit(i+1),
			Summary:   "Synthetic candidate with inspectable evidence and deterministic ordering.",
			Labels:    []string{"queue-dry", "operator"},
			Keywords:  keywords[i%len(keywords)],
			Paths:     []string{"internal/ideation"},
			SourceIDs: []string{"br"},
			Evidence:  []string{"evidence item " + twoDigit(i+1), "fixture source"},
		})
	}
	return out
}

func stableTieCandidate(id, title string) IdeaCandidate {
	return IdeaCandidate{
		ID:        id,
		Title:     title,
		Summary:   "Same score fixture.",
		Labels:    []string{"queue-dry"},
		Keywords:  []string{"queue"},
		SourceIDs: []string{"br"},
		Evidence:  []string{"same evidence"},
	}
}

func twoDigit(value int) string {
	if value < 10 {
		return "0" + string(rune('0'+value))
	}
	return string(rune('0'+value/10)) + string(rune('0'+value%10))
}

func mustMarshalRankJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	return string(data)
}
