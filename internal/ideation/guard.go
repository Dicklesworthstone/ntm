package ideation

import (
	"fmt"
	"strings"
)

type GuardRecommendation string

const (
	GuardRecommendationIdeate              GuardRecommendation = "ideate"
	GuardRecommendationReviewRecentWork    GuardRecommendation = "review_recent_work"
	GuardRecommendationValidateCloseout    GuardRecommendation = "validate_closeout"
	GuardRecommendationStandDown           GuardRecommendation = "stand_down"
	GuardRecommendationWaitForCoordination GuardRecommendation = "wait_for_coordination"
)

type NoveltyGuardOptions struct {
	CreationRequested       bool     `json:"creation_requested,omitempty"`
	OverrideCreation        bool     `json:"override_creation,omitempty"`
	OverrideReason          string   `json:"override_reason,omitempty"`
	DirtyWorktree           bool     `json:"dirty_worktree,omitempty"`
	ReservationStateUnknown bool     `json:"reservation_state_unknown,omitempty"`
	ActiveReservationCount  int      `json:"active_reservation_count,omitempty"`
	StaleInProgressIDs      []string `json:"stale_in_progress_ids,omitempty"`
	RecentClosedThreshold   int      `json:"recent_closed_threshold,omitempty"`
	RecentBugThreshold      int      `json:"recent_bug_threshold,omitempty"`
	DuplicateHeavyThreshold float64  `json:"duplicate_heavy_threshold,omitempty"`
}

type NoveltyGuardAssessment struct {
	Recommendation           GuardRecommendation `json:"recommendation"`
	CreationAllowed          bool                `json:"creation_allowed"`
	OverrideRecorded         bool                `json:"override_recorded,omitempty"`
	OverrideReason           string              `json:"override_reason,omitempty"`
	ReasonCodes              []string            `json:"reason_codes"`
	Evidence                 []string            `json:"evidence"`
	Notes                    []ValidationNote    `json:"notes"`
	CandidateCount           int                 `json:"candidate_count"`
	SelectedCount            int                 `json:"selected_count"`
	SuppressedCount          int                 `json:"suppressed_count"`
	DuplicateSuppressedCount int                 `json:"duplicate_suppressed_count"`
	RecentClosedCount        int                 `json:"recent_closed_count"`
	RecentClosedBugCount     int                 `json:"recent_closed_bug_count"`
}

func AssessNoveltyGuard(snapshot IdeaEvidenceSnapshot, ranking RankingResult, opts NoveltyGuardOptions) NoveltyGuardAssessment {
	opts = normalizeNoveltyGuardOptions(opts)
	assessment := NoveltyGuardAssessment{
		Recommendation:           GuardRecommendationStandDown,
		CreationAllowed:          false,
		ReasonCodes:              []string{},
		Evidence:                 []string{},
		Notes:                    []ValidationNote{},
		CandidateCount:           ranking.CandidateCount,
		SelectedCount:            len(ranking.Selected),
		SuppressedCount:          len(ranking.Suppressed),
		DuplicateSuppressedCount: duplicateSuppressedCount(ranking),
		RecentClosedCount:        closedWorkCount(snapshot.ExistingWork),
		RecentClosedBugCount:     closedBugWorkCount(snapshot.ExistingWork),
	}
	if assessment.CandidateCount == 0 {
		assessment.CandidateCount = len(ranking.Selected) + len(ranking.NextBest) + len(ranking.Suppressed)
	}

	addReason := func(code, evidence string) {
		assessment.ReasonCodes = append(assessment.ReasonCodes, code)
		if strings.TrimSpace(evidence) != "" {
			assessment.Evidence = append(assessment.Evidence, evidence)
		}
	}

	switch {
	case opts.DirtyWorktree:
		assessment.Recommendation = GuardRecommendationWaitForCoordination
		addReason("dirty_worktree", "local worktree has uncommitted changes; coordinate before creating more beads")
	case opts.ReservationStateUnknown:
		assessment.Recommendation = GuardRecommendationWaitForCoordination
		addReason("reservation_state_unknown", "active reservation state could not be verified")
	case opts.ActiveReservationCount > 0:
		assessment.Recommendation = GuardRecommendationWaitForCoordination
		addReason("active_reservations", fmt.Sprintf("%d active reservation(s) may already cover current work", opts.ActiveReservationCount))
	case len(opts.StaleInProgressIDs) > 0:
		assessment.Recommendation = GuardRecommendationReviewRecentWork
		addReason("stale_in_progress", fmt.Sprintf("stale in-progress work exists: %s", strings.Join(stableStrings(opts.StaleInProgressIDs), ",")))
	case hasUnhealthyGraph(snapshot.Triage.GraphHealth):
		assessment.Recommendation = GuardRecommendationValidateCloseout
		addReason("graph_unhealthy", "bv graph health reports cycles or unhealthy graph metrics")
	case hasFailedCloseoutProof(snapshot.CloseoutProof):
		assessment.Recommendation = GuardRecommendationValidateCloseout
		addReason("closeout_verification_failed", "closeout proof contains failed or skipped verification signals")
	case assessment.RecentClosedBugCount >= opts.RecentBugThreshold:
		assessment.Recommendation = GuardRecommendationValidateCloseout
		addReason("recent_bug_density", fmt.Sprintf("%d recently closed bug-like items meet validation threshold %d", assessment.RecentClosedBugCount, opts.RecentBugThreshold))
	case queueDryAfterLargeTranche(snapshot, assessment.RecentClosedCount, opts.RecentClosedThreshold):
		assessment.Recommendation = GuardRecommendationReviewRecentWork
		addReason("post_tranche_queue_dry", fmt.Sprintf("%d recently closed items with no ready/actionable work", assessment.RecentClosedCount))
	case duplicateHeavy(ranking, opts.DuplicateHeavyThreshold):
		assessment.Recommendation = GuardRecommendationReviewRecentWork
		addReason("duplicate_heavy_candidates", fmt.Sprintf("%d duplicate-suppressed candidates out of %d", assessment.DuplicateSuppressedCount, assessment.CandidateCount))
	case len(ranking.Selected) > 0 && ranking.Decision == RankingDecisionIdeate:
		assessment.Recommendation = GuardRecommendationIdeate
		addReason("healthy_novel_candidates", fmt.Sprintf("%d ranked candidate(s) remain after duplicate checks", len(ranking.Selected)))
	case ranking.Decision == RankingDecisionReviewRecentWork:
		assessment.Recommendation = GuardRecommendationReviewRecentWork
		addReason("ranker_review_recent_work", ranking.Summary)
	default:
		assessment.Recommendation = GuardRecommendationStandDown
		addReason("no_useful_candidates", ranking.Summary)
	}

	assessment.CreationAllowed = assessment.Recommendation == GuardRecommendationIdeate
	if opts.CreationRequested && !assessment.CreationAllowed && !opts.OverrideCreation {
		assessment.Notes = append(assessment.Notes, ValidationNote{
			Code:     "creation_blocked",
			Severity: ValidationError,
			Message:  "mutating bead creation is blocked by novelty guard recommendation " + string(assessment.Recommendation),
			Evidence: stableStrings(append([]string{}, assessment.Evidence...)),
		})
	}
	if opts.CreationRequested && opts.OverrideCreation && assessment.Recommendation != GuardRecommendationIdeate {
		assessment.CreationAllowed = true
		assessment.OverrideRecorded = true
		assessment.OverrideReason = strings.TrimSpace(opts.OverrideReason)
		if assessment.OverrideReason == "" {
			assessment.OverrideReason = "override requested without reason"
		}
		assessment.ReasonCodes = append(assessment.ReasonCodes, "creation_override_recorded")
		assessment.Evidence = append(assessment.Evidence, "creation override recorded: "+assessment.OverrideReason)
		assessment.Notes = append(assessment.Notes, ValidationNote{
			Code:     "creation_override_recorded",
			Severity: ValidationWarning,
			Message:  "explicit override allows creation despite novelty guard recommendation " + string(assessment.Recommendation),
			Evidence: []string{assessment.OverrideReason},
		})
	}

	assessment.ReasonCodes = stableStrings(assessment.ReasonCodes)
	assessment.Evidence = stableStrings(assessment.Evidence)
	return assessment
}

func normalizeNoveltyGuardOptions(opts NoveltyGuardOptions) NoveltyGuardOptions {
	if opts.RecentClosedThreshold <= 0 {
		opts.RecentClosedThreshold = 20
	}
	if opts.RecentBugThreshold <= 0 {
		opts.RecentBugThreshold = 5
	}
	if opts.DuplicateHeavyThreshold <= 0 {
		opts.DuplicateHeavyThreshold = 0.75
	}
	if opts.DuplicateHeavyThreshold > 1 {
		opts.DuplicateHeavyThreshold = 1
	}
	return opts
}

func duplicateSuppressedCount(ranking RankingResult) int {
	count := 0
	for _, item := range ranking.Suppressed {
		switch item.Candidate.Overlap.Kind {
		case OverlapExactDuplicate, OverlapLikelyDuplicate:
			count++
		}
	}
	return count
}

func closedWorkCount(items []ExistingWorkFingerprint) int {
	count := 0
	for _, item := range items {
		if isClosedWorkStatus(item.Status) {
			count++
		}
	}
	return count
}

func closedBugWorkCount(items []ExistingWorkFingerprint) int {
	count := 0
	for _, item := range items {
		if !isClosedWorkStatus(item.Status) {
			continue
		}
		words := tokenSet(item.Title + " " + item.Summary + " " + strings.Join(item.Labels, " ") + " " + strings.Join(item.Keywords, " "))
		if hasAny(words, "bug", "bugs", "fix", "failure", "failed", "regression", "panic") {
			count++
		}
	}
	return count
}

func isClosedWorkStatus(status WorkStatus) bool {
	switch status {
	case WorkStatusClosed:
		return true
	default:
		return false
	}
}

func queueDryAfterLargeTranche(snapshot IdeaEvidenceSnapshot, closedCount, threshold int) bool {
	return snapshot.Queue.CountsVerified &&
		snapshot.Queue.ReadyCount == 0 &&
		snapshot.Queue.ActionableCount == 0 &&
		closedCount >= threshold
}

func duplicateHeavy(ranking RankingResult, threshold float64) bool {
	candidateCount := ranking.CandidateCount
	if candidateCount == 0 {
		candidateCount = len(ranking.Selected) + len(ranking.NextBest) + len(ranking.Suppressed)
	}
	if candidateCount == 0 {
		return false
	}
	duplicateCount := duplicateSuppressedCount(ranking)
	if duplicateCount == 0 {
		return false
	}
	return float64(duplicateCount)/float64(candidateCount) >= threshold
}

func hasUnhealthyGraph(health GraphHealth) bool {
	if strings.EqualFold(health.Metrics["has_cycles"], "true") {
		return true
	}
	if health.Metrics["cycle_count"] != "" && health.Metrics["cycle_count"] != "0" {
		return true
	}
	for _, item := range health.Evidence {
		text := strings.ToLower(item)
		if strings.Contains(text, "cycle") && !strings.Contains(text, "no cycle") {
			return true
		}
	}
	return false
}

func hasFailedCloseoutProof(items []CloseoutProofEvidence) bool {
	// "red" is matched as a whole token (via tokenSet) to avoid false
	// positives in common words like "credentials", "required",
	// "redacted", "predicted", and "deferred". The remaining markers
	// are long enough that substring matching does not collide with
	// unrelated English. (bd-sra1j)
	substringMarkers := []string{"failed", "failure", "skipped", "missing verification", "not verified"}
	for _, item := range items {
		text := strings.ToLower(strings.Join(append(append([]string{}, item.Signals...), item.Summary, item.Path), " "))
		for _, marker := range substringMarkers {
			if strings.Contains(text, marker) {
				return true
			}
		}
		if _, ok := tokenSet(text)["red"]; ok {
			return true
		}
	}
	return false
}
