package ideation

import (
	"fmt"
	"sort"
	"strings"
)

// RefinementReport is the stable, golden-friendly summary of the queue-dry
// ideation pipeline (collect → rank → render → guard). It composes the
// novelty guard verdict with cross-stage signals so a single artifact captures
// whether ideation should proceed, what was suppressed, and which refinement
// follow-ups the operator (or CI gate) should consider.
//
// The shape is intentionally compact and deterministic so it can be pinned as
// a regression golden alongside the evidence snapshot, ranking result, and
// dry-run roadmap plan.
type RefinementReport struct {
	ScenarioID               string              `json:"scenario_id,omitempty"`
	Recommendation           GuardRecommendation `json:"recommendation"`
	RankingDecision          RankingDecision     `json:"ranking_decision"`
	RankingSummary           string              `json:"ranking_summary,omitempty"`
	CreationRequested        bool                `json:"creation_requested,omitempty"`
	CreationAllowed          bool                `json:"creation_allowed"`
	OverrideRecorded         bool                `json:"override_recorded,omitempty"`
	OverrideReason           string              `json:"override_reason,omitempty"`
	PartialCreationFailure   bool                `json:"partial_creation_failure,omitempty"`
	ReadyWorkExists          bool                `json:"ready_work_exists,omitempty"`
	CandidateCount           int                 `json:"candidate_count"`
	SelectedCount            int                 `json:"selected_count"`
	NextBestCount            int                 `json:"next_best_count"`
	SuppressedCount          int                 `json:"suppressed_count"`
	DuplicateSuppressedCount int                 `json:"duplicate_suppressed_count"`
	RenderedBeadCount        int                 `json:"rendered_bead_count"`
	OpenCount                int                 `json:"open_count"`
	ReadyCount               int                 `json:"ready_count"`
	ActionableCount          int                 `json:"actionable_count"`
	InProgressCount          int                 `json:"in_progress_count"`
	BlockedCount             int                 `json:"blocked_count"`
	QueueCountsVerified      bool                `json:"queue_counts_verified"`
	RecentClosedCount        int                 `json:"recent_closed_count"`
	RecentClosedBugCount     int                 `json:"recent_closed_bug_count"`
	SelectedCandidateIDs     []string            `json:"selected_candidate_ids"`
	NextBestCandidateIDs     []string            `json:"next_best_candidate_ids"`
	SuppressedCandidateIDs   []string            `json:"suppressed_candidate_ids"`
	RenderedBeadRefs         []string            `json:"rendered_bead_refs"`
	DegradedSourceIDs        []string            `json:"degraded_source_ids"`
	ReasonCodes              []string            `json:"reason_codes"`
	Evidence                 []string            `json:"evidence"`
	Notes                    []ValidationNote    `json:"notes"`
	NextActions              []string            `json:"next_actions"`
}

// BuildRefinementReport composes a RefinementReport from the four pipeline
// stages. The inputs must be the same artifacts produced by the live pipeline:
// the evidence snapshot (collectors), the ranking result, the rendered
// roadmap plan, and the novelty guard assessment.
//
// The returned report is deterministic: all string slices are deduplicated and
// sorted, and the ValidationNote slice is copied so callers can mutate inputs
// freely. It is safe to marshal directly into a regression golden file.
func BuildRefinementReport(snapshot IdeaEvidenceSnapshot, ranking RankingResult, plan RoadmapPlan, guard NoveltyGuardAssessment, opts RefinementOptions) RefinementReport {
	report := RefinementReport{
		ScenarioID:               opts.ScenarioID,
		Recommendation:           guard.Recommendation,
		RankingDecision:          ranking.Decision,
		RankingSummary:           ranking.Summary,
		CreationRequested:        opts.CreationRequested,
		CreationAllowed:          guard.CreationAllowed,
		OverrideRecorded:         guard.OverrideRecorded,
		OverrideReason:           guard.OverrideReason,
		CandidateCount:           ranking.CandidateCount,
		SelectedCount:            len(ranking.Selected),
		NextBestCount:            len(ranking.NextBest),
		SuppressedCount:          len(ranking.Suppressed),
		DuplicateSuppressedCount: duplicateSuppressedCount(ranking),
		RenderedBeadCount:        plan.RenderedCount,
		OpenCount:                snapshot.Queue.OpenCount,
		ReadyCount:               snapshot.Queue.ReadyCount,
		ActionableCount:          snapshot.Queue.ActionableCount,
		InProgressCount:          snapshot.Queue.InProgressCount,
		BlockedCount:             snapshot.Queue.BlockedCount,
		QueueCountsVerified:      snapshot.Queue.CountsVerified,
		RecentClosedCount:        guard.RecentClosedCount,
		RecentClosedBugCount:     guard.RecentClosedBugCount,
		ReasonCodes:              stableStrings(append([]string{}, guard.ReasonCodes...)),
		Evidence:                 stableStrings(append([]string{}, guard.Evidence...)),
	}
	if report.CandidateCount == 0 {
		report.CandidateCount = report.SelectedCount + report.NextBestCount + report.SuppressedCount
	}
	report.SelectedCandidateIDs = candidateIDsFromRanked(ranking.Selected)
	report.NextBestCandidateIDs = candidateIDsFromRanked(ranking.NextBest)
	report.SuppressedCandidateIDs = candidateIDsFromRanked(ranking.Suppressed)
	report.RenderedBeadRefs = renderedBeadRefs(plan)
	report.DegradedSourceIDs = degradedSourceIDs(snapshot)
	report.PartialCreationFailure = isPartialCreationFailure(report, opts)
	report.ReadyWorkExists = isReadyWorkExists(snapshot)
	if report.ReadyWorkExists && opts.CreationRequested {
		report.CreationAllowed = false
	}
	report.Notes = combinedRefinementNotes(snapshot, ranking, guard, report, opts)
	report.NextActions = refinementNextActions(report)
	return report
}

// RefinementOptions tune scenario-level metadata without changing pipeline
// inputs. ScenarioID is included verbatim in the report; CreationRequested
// mirrors the equivalent flag from NoveltyGuardOptions so the report can
// describe partial-failure semantics without re-reading the guard input.
type RefinementOptions struct {
	ScenarioID        string `json:"scenario_id,omitempty"`
	CreationRequested bool   `json:"creation_requested,omitempty"`
}

func candidateIDsFromRanked(items []RankedCandidate) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		id := strings.TrimSpace(item.Candidate.ID)
		if id == "" {
			continue
		}
		out = append(out, id)
	}
	return stableStrings(out)
}

func renderedBeadRefs(plan RoadmapPlan) []string {
	out := make([]string, 0, len(plan.ProposedBeads))
	for _, bead := range plan.ProposedBeads {
		ref := strings.TrimSpace(bead.Ref)
		if ref == "" {
			continue
		}
		out = append(out, ref)
	}
	return stableStrings(out)
}

func degradedSourceIDs(snapshot IdeaEvidenceSnapshot) []string {
	out := make([]string, 0, len(snapshot.DegradedSources))
	for _, note := range snapshot.DegradedSources {
		id := strings.TrimSpace(note.SourceID)
		if id == "" {
			continue
		}
		out = append(out, id)
	}
	return stableStrings(out)
}

func combinedRefinementNotes(snapshot IdeaEvidenceSnapshot, ranking RankingResult, guard NoveltyGuardAssessment, report RefinementReport, opts RefinementOptions) []ValidationNote {
	notes := make([]ValidationNote, 0)
	notes = append(notes, snapshot.DegradedSources...)
	notes = append(notes, snapshot.ValidationNotes...)
	notes = append(notes, ranking.Notes...)
	notes = append(notes, guard.Notes...)

	if report.PartialCreationFailure {
		evidence := []string{}
		if report.SelectedCount > 0 {
			evidence = append(evidence, "selected candidates would have been created")
		}
		if report.SuppressedCount > 0 {
			evidence = append(evidence, "duplicate-suppressed candidates would not have been created")
		}
		if report.OverrideRecorded {
			evidence = append(evidence, "creation override recorded over guard recommendation "+string(report.Recommendation))
		}
		notes = append(notes, ValidationNote{
			Code:     "partial_creation_failure",
			Severity: ValidationWarning,
			Message:  "creation request partially fulfilled: at least one candidate would be skipped",
			Evidence: stableStrings(evidence),
		})
	}

	if report.ReadyWorkExists {
		evidence := []string{}
		if report.ReadyCount > 0 {
			evidence = append(evidence, fmt.Sprintf("br ready count=%d", report.ReadyCount))
		}
		if report.ActionableCount > 0 && report.ActionableCount != report.ReadyCount {
			evidence = append(evidence, fmt.Sprintf("bv actionable count=%d", report.ActionableCount))
		}
		message := "actionable queue work exists; do that work before ideating"
		severity := ValidationWarning
		if opts.CreationRequested {
			message = "creation blocked: actionable queue work exists; resolve it before ideating"
			severity = ValidationError
		}
		notes = append(notes, ValidationNote{
			Code:     "ready_work_exists",
			Severity: severity,
			Message:  message,
			Evidence: stableStrings(evidence),
		})
	}

	out := make([]ValidationNote, 0, len(notes))
	for _, note := range notes {
		note.Evidence = stableStrings(append([]string{}, note.Evidence...))
		out = append(out, note)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Code != out[j].Code {
			return out[i].Code < out[j].Code
		}
		if out[i].Severity != out[j].Severity {
			return out[i].Severity < out[j].Severity
		}
		if out[i].SourceID != out[j].SourceID {
			return out[i].SourceID < out[j].SourceID
		}
		return out[i].Message < out[j].Message
	})
	return out
}

func isReadyWorkExists(snapshot IdeaEvidenceSnapshot) bool {
	if !snapshot.Queue.CountsVerified {
		return false
	}
	return snapshot.Queue.ReadyCount > 0 || snapshot.Queue.ActionableCount > 0
}

func isPartialCreationFailure(report RefinementReport, opts RefinementOptions) bool {
	if !opts.CreationRequested {
		return false
	}
	if report.SelectedCount == 0 {
		return false
	}
	if report.SuppressedCount == 0 {
		return false
	}
	if report.DuplicateSuppressedCount == 0 {
		return false
	}
	return true
}

func refinementNextActions(report RefinementReport) []string {
	actions := make([]string, 0, 4)
	if report.ReadyWorkExists {
		actions = append(actions, "do existing ready work before ideating")
	}
	switch report.Recommendation {
	case GuardRecommendationIdeate:
		if report.RenderedBeadCount > 0 && !report.ReadyWorkExists {
			actions = append(actions, "review rendered dry-run roadmap before mutating beads")
		}
		if report.SuppressedCount > 0 {
			actions = append(actions, "inspect suppressed duplicate candidates for missed prior work")
		}
	case GuardRecommendationReviewRecentWork:
		actions = append(actions, "review recently closed work instead of creating new beads")
		if report.SuppressedCount > 0 {
			actions = append(actions, "inspect suppressed duplicate candidates for missed prior work")
		}
	case GuardRecommendationValidateCloseout:
		actions = append(actions, "validate closeout proof and graph health before ideating")
	case GuardRecommendationWaitForCoordination:
		actions = append(actions, "coordinate with peers before requesting creation")
	default:
		if !report.ReadyWorkExists {
			actions = append(actions, "stand down: no useful candidates remain")
		}
	}
	if len(report.DegradedSourceIDs) > 0 {
		actions = append(actions, "treat degraded optional sources as best-effort context")
	}
	if report.PartialCreationFailure {
		actions = append(actions, "expect partial creation: re-run with refreshed evidence after addressing duplicates")
	}
	return stableStrings(actions)
}
