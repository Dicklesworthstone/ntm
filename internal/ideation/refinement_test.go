package ideation

import (
	"strings"
	"testing"
)

func TestBuildRefinementReportDeterministicAndSortedNotes(t *testing.T) {
	snapshot := NewIdeaEvidenceSnapshot("/repo")
	snapshot.RecordSource(CandidateSource{ID: "cass", Kind: SourceCASS, Available: false, Error: "cass missing", Evidence: []string{"timeout"}})
	snapshot.RecordSource(CandidateSource{ID: "agent_mail", Kind: SourceAgentMail, Available: false, Error: "agent_mail unreachable"})
	snapshot.ValidationNotes = []ValidationNote{
		{Code: "snapshot_warning", Severity: ValidationWarning, Message: "snapshot warning", Evidence: []string{"snapshot evidence"}},
	}

	ranking := RankingResult{
		Decision:       RankingDecisionIdeate,
		Summary:        "selected one candidate",
		CandidateCount: 1,
		Selected:       []RankedCandidate{rankedNovelCandidate("novel")},
		NextBest:       []RankedCandidate{},
		Suppressed:     []RankedCandidate{},
		Notes: []ValidationNote{
			{Code: "ranker_note", Severity: ValidationInfo, Message: "ranker info"},
		},
	}
	plan := RoadmapPlan{
		PlanID:        "queue-dry-roadmap",
		DryRun:        true,
		Decision:      RankingDecisionIdeate,
		RenderedCount: 1,
		ProposedBeads: []ProposedBead{{Ref: "${BEAD_ID_NOVEL}", CandidateID: "novel"}},
	}
	guard := NoveltyGuardAssessment{
		Recommendation:    GuardRecommendationIdeate,
		CreationAllowed:   true,
		ReasonCodes:       []string{"healthy_novel_candidates"},
		Evidence:          []string{"novel candidates remain"},
		Notes:             []ValidationNote{},
		CandidateCount:    1,
		SelectedCount:     1,
		RecentClosedCount: 0,
	}

	first := BuildRefinementReport(snapshot, ranking, plan, guard, RefinementOptions{ScenarioID: "deterministic"})
	second := BuildRefinementReport(snapshot, ranking, plan, guard, RefinementOptions{ScenarioID: "deterministic"})

	if first.ScenarioID != "deterministic" {
		t.Fatalf("scenario id=%q, want deterministic", first.ScenarioID)
	}
	if first.SelectedCount != 1 || first.RenderedBeadCount != 1 {
		t.Fatalf("counts mismatch: selected=%d rendered=%d", first.SelectedCount, first.RenderedBeadCount)
	}
	if got := mustMarshalJSON(t, first); got != mustMarshalJSON(t, second) {
		t.Fatalf("RefinementReport JSON not stable across calls: %s", got)
	}
	if len(first.DegradedSourceIDs) != 2 || first.DegradedSourceIDs[0] != "agent_mail" {
		t.Fatalf("degraded ids=%v, want sorted agent_mail/cass", first.DegradedSourceIDs)
	}
	for _, code := range []string{"snapshot_warning", "ranker_note", "source_degraded"} {
		if !hasNoteCodeIn(first.Notes, code) {
			t.Fatalf("notes missing %s: %+v", code, first.Notes)
		}
	}
	for i := 1; i < len(first.Notes); i++ {
		if first.Notes[i-1].Code > first.Notes[i].Code {
			t.Fatalf("notes not sorted by code: %+v", first.Notes)
		}
	}
	if !containsString(first.NextActions, "review rendered dry-run roadmap before mutating beads") {
		t.Fatalf("next_actions missing roadmap review: %v", first.NextActions)
	}
	if !containsString(first.NextActions, "treat degraded optional sources as best-effort context") {
		t.Fatalf("next_actions missing degraded marker: %v", first.NextActions)
	}
}

func TestBuildRefinementReportFlagsPartialCreationFailure(t *testing.T) {
	snapshot := NewIdeaEvidenceSnapshot("/repo")
	dup := rankedDuplicateCandidate("dup")
	novel := rankedNovelCandidate("novel")
	novel.Rank = 1
	novel.Included = true

	ranking := RankingResult{
		Decision:       RankingDecisionIdeate,
		Summary:        "selected one and suppressed one",
		CandidateCount: 2,
		Selected:       []RankedCandidate{novel},
		Suppressed:     []RankedCandidate{dup},
	}
	plan := RoadmapPlan{
		PlanID:        "queue-dry-roadmap",
		DryRun:        true,
		Decision:      RankingDecisionIdeate,
		RenderedCount: 1,
		ProposedBeads: []ProposedBead{{Ref: "${BEAD_ID_NOVEL}", CandidateID: "novel"}},
	}
	guard := NoveltyGuardAssessment{
		Recommendation:           GuardRecommendationIdeate,
		CreationAllowed:          true,
		ReasonCodes:              []string{"healthy_novel_candidates"},
		Evidence:                 []string{"novel candidates remain"},
		CandidateCount:           2,
		SelectedCount:            1,
		SuppressedCount:          1,
		DuplicateSuppressedCount: 1,
	}

	report := BuildRefinementReport(snapshot, ranking, plan, guard, RefinementOptions{
		ScenarioID:        "partial",
		CreationRequested: true,
	})

	if !report.PartialCreationFailure {
		t.Fatalf("expected partial_creation_failure=true: %+v", report)
	}
	if !hasNoteCodeIn(report.Notes, "partial_creation_failure") {
		t.Fatalf("notes missing partial_creation_failure: %+v", report.Notes)
	}
	if !containsString(report.NextActions, "expect partial creation: re-run with refreshed evidence after addressing duplicates") {
		t.Fatalf("next_actions missing partial follow-up: %v", report.NextActions)
	}
}

func TestBuildRefinementReportStandsDownWhenNoCandidates(t *testing.T) {
	snapshot := NewIdeaEvidenceSnapshot("/repo")
	snapshot.Queue.CountsVerified = true
	ranking := RankingResult{
		Decision:       RankingDecisionStandDown,
		Summary:        "no candidates available",
		CandidateCount: 0,
	}
	plan := RoadmapPlan{PlanID: "queue-dry-roadmap", DryRun: true, Decision: RankingDecisionStandDown}
	guard := NoveltyGuardAssessment{
		Recommendation: GuardRecommendationStandDown,
		ReasonCodes:    []string{"no_useful_candidates"},
		Evidence:       []string{"empty"},
	}

	report := BuildRefinementReport(snapshot, ranking, plan, guard, RefinementOptions{ScenarioID: "stand_down"})
	if report.RenderedBeadCount != 0 || report.SelectedCount != 0 {
		t.Fatalf("expected zero counts: %+v", report)
	}
	if !containsString(report.NextActions, "stand down: no useful candidates remain") {
		t.Fatalf("next_actions missing stand-down: %v", report.NextActions)
	}
	if strings.Contains(mustMarshalJSON(t, report), "\"partial_creation_failure\":true") {
		t.Fatalf("stand-down report should never flag partial_creation_failure: %+v", report)
	}
}

func TestBuildRefinementReportBlocksCreationWhenReadyWorkExists(t *testing.T) {
	snapshot := NewIdeaEvidenceSnapshot("/repo")
	snapshot.Queue.CountsVerified = true
	snapshot.Queue.OpenCount = 4
	snapshot.Queue.ReadyCount = 2
	snapshot.Queue.ActionableCount = 2

	ranking := RankingResult{
		Decision:       RankingDecisionIdeate,
		Summary:        "selected one candidate",
		CandidateCount: 1,
		Selected:       []RankedCandidate{rankedNovelCandidate("novel")},
	}
	plan := RoadmapPlan{PlanID: "ready-roadmap", DryRun: true, Decision: RankingDecisionIdeate, RenderedCount: 1, ProposedBeads: []ProposedBead{{Ref: "${BEAD_ID_NOVEL}", CandidateID: "novel"}}}
	guard := NoveltyGuardAssessment{
		Recommendation:  GuardRecommendationIdeate,
		CreationAllowed: true,
		ReasonCodes:     []string{"healthy_novel_candidates"},
		Evidence:        []string{"novel candidates remain"},
		CandidateCount:  1,
		SelectedCount:   1,
	}

	report := BuildRefinementReport(snapshot, ranking, plan, guard, RefinementOptions{ScenarioID: "ready_work", CreationRequested: true})
	if !report.ReadyWorkExists {
		t.Fatalf("expected ready_work_exists=true: %+v", report)
	}
	if report.CreationAllowed {
		t.Fatalf("creation should be blocked when ready work exists: %+v", report)
	}
	if !hasNoteCodeIn(report.Notes, "ready_work_exists") {
		t.Fatalf("notes missing ready_work_exists: %+v", report.Notes)
	}
	if !containsString(report.NextActions, "do existing ready work before ideating") {
		t.Fatalf("next_actions missing ready-work guidance: %v", report.NextActions)
	}
	if containsString(report.NextActions, "review rendered dry-run roadmap before mutating beads") {
		t.Fatalf("next_actions should suppress roadmap review when ready work exists: %v", report.NextActions)
	}
}

func hasNoteCodeIn(notes []ValidationNote, code string) bool {
	for _, n := range notes {
		if n.Code == code {
			return true
		}
	}
	return false
}
