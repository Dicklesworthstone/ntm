package ideation

import "testing"

func TestNoveltyGuardPostTrancheDryQueueReviewsRecentWork(t *testing.T) {
	snapshot := NewIdeaEvidenceSnapshot("/repo")
	snapshot.Queue.CountsVerified = true
	snapshot.Queue.ReadyCount = 0
	snapshot.Queue.ActionableCount = 0
	snapshot.ExistingWork = closedWorkItems(4, "task")
	ranking := RankingResult{
		Decision:       RankingDecisionIdeate,
		Summary:        "selected candidates",
		CandidateCount: 1,
		Selected:       []RankedCandidate{rankedNovelCandidate("novel")},
		Suppressed:     []RankedCandidate{},
	}

	got := AssessNoveltyGuard(snapshot, ranking, NoveltyGuardOptions{
		RecentClosedThreshold: 3,
		CreationRequested:     true,
	})
	if got.Recommendation != GuardRecommendationReviewRecentWork {
		t.Fatalf("recommendation=%q, want review_recent_work", got.Recommendation)
	}
	if got.CreationAllowed {
		t.Fatalf("creation allowed despite post-tranche review recommendation")
	}
	if !hasReasonCode(got, "post_tranche_queue_dry") || !hasNoteCode(got, "creation_blocked") {
		t.Fatalf("assessment=%+v, want post_tranche and creation_blocked evidence", got)
	}
}

func TestNoveltyGuardDuplicateHeavyCandidatesReviewsRecentWork(t *testing.T) {
	snapshot := NewIdeaEvidenceSnapshot("/repo")
	snapshot.Queue.CountsVerified = true
	snapshot.Queue.ReadyCount = 0
	snapshot.Queue.ActionableCount = 0
	ranking := RankingResult{
		Decision:       RankingDecisionReviewRecentWork,
		Summary:        "duplicates",
		CandidateCount: 4,
		Suppressed: []RankedCandidate{
			rankedDuplicateCandidate("dup-1"),
			rankedDuplicateCandidate("dup-2"),
			rankedDuplicateCandidate("dup-3"),
		},
	}

	got := AssessNoveltyGuard(snapshot, ranking, NoveltyGuardOptions{
		DuplicateHeavyThreshold: 0.7,
	})
	if got.Recommendation != GuardRecommendationReviewRecentWork {
		t.Fatalf("recommendation=%q, want review_recent_work", got.Recommendation)
	}
	if got.DuplicateSuppressedCount != 3 {
		t.Fatalf("duplicate count=%d, want 3", got.DuplicateSuppressedCount)
	}
	if !hasReasonCode(got, "duplicate_heavy_candidates") {
		t.Fatalf("reason codes=%v, want duplicate_heavy_candidates", got.ReasonCodes)
	}
}

func TestNoveltyGuardDirtyOrReservationsWaitsForCoordination(t *testing.T) {
	ranking := RankingResult{
		Decision:       RankingDecisionIdeate,
		Summary:        "selected",
		CandidateCount: 1,
		Selected:       []RankedCandidate{rankedNovelCandidate("novel")},
	}

	for _, tc := range []struct {
		name string
		opts NoveltyGuardOptions
		code string
	}{
		{
			name: "dirty worktree",
			opts: NoveltyGuardOptions{DirtyWorktree: true},
			code: "dirty_worktree",
		},
		{
			name: "active reservations",
			opts: NoveltyGuardOptions{ActiveReservationCount: 2},
			code: "active_reservations",
		},
		{
			name: "unknown reservations",
			opts: NoveltyGuardOptions{ReservationStateUnknown: true},
			code: "reservation_state_unknown",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := AssessNoveltyGuard(NewIdeaEvidenceSnapshot("/repo"), ranking, tc.opts)
			if got.Recommendation != GuardRecommendationWaitForCoordination {
				t.Fatalf("recommendation=%q, want wait_for_coordination", got.Recommendation)
			}
			if !hasReasonCode(got, tc.code) {
				t.Fatalf("reason codes=%v, want %s", got.ReasonCodes, tc.code)
			}
		})
	}
}

func TestNoveltyGuardValidatesCloseoutSignals(t *testing.T) {
	ranking := RankingResult{
		Decision:       RankingDecisionIdeate,
		Summary:        "selected",
		CandidateCount: 1,
		Selected:       []RankedCandidate{rankedNovelCandidate("novel")},
	}
	snapshot := NewIdeaEvidenceSnapshot("/repo")
	snapshot.CloseoutProof = []CloseoutProofEvidence{
		{ID: "proof", Summary: "closeout proof", Signals: []string{"go test skipped verification for flaky package"}},
	}

	got := AssessNoveltyGuard(snapshot, ranking, NoveltyGuardOptions{})
	if got.Recommendation != GuardRecommendationValidateCloseout {
		t.Fatalf("recommendation=%q, want validate_closeout", got.Recommendation)
	}
	if !hasReasonCode(got, "closeout_verification_failed") {
		t.Fatalf("reason codes=%v, want closeout_verification_failed", got.ReasonCodes)
	}
}

func TestHasFailedCloseoutProofRedTokenDoesNotMatchSubstring(t *testing.T) {
	// bd-sra1j: pre-fix, hasFailedCloseoutProof matched the marker
	// "red" via strings.Contains, so summaries containing words like
	// "credentials", "required", "redacted", "predicted", or
	// "deferred" silently flipped the novelty guard onto
	// validate_closeout. Pin both the negative cases (no failure
	// signal) and the positive cases ("status: red", bare "red"
	// token) so any reintroduction of a substring match is caught.
	negativeSummaries := []string{
		"credentials renewed for next sprint",
		"required environment variable documented",
		"sensitive contents redacted from output",
		"predicted memory usage within budget",
		"deferred follow-up captured in bd-zzzz",
	}
	for _, summary := range negativeSummaries {
		got := hasFailedCloseoutProof([]CloseoutProofEvidence{
			{ID: "proof", Summary: summary},
		})
		if got {
			t.Errorf("hasFailedCloseoutProof(%q) = true, want false (no real failure marker)", summary)
		}
	}

	positiveSummaries := []string{
		"verification status: red after retry",
		"red",
		"closeout proof red flag raised",
	}
	for _, summary := range positiveSummaries {
		got := hasFailedCloseoutProof([]CloseoutProofEvidence{
			{ID: "proof", Summary: summary},
		})
		if !got {
			t.Errorf("hasFailedCloseoutProof(%q) = false, want true (whole-token red)", summary)
		}
	}

	if !hasFailedCloseoutProof([]CloseoutProofEvidence{
		{ID: "proof", Summary: "everything fine", Signals: []string{"go test failed for package"}},
	}) {
		t.Fatalf("substring marker 'failed' should still trigger via Signals")
	}
}

func TestNoveltyGuardAdditionalDeclaredSignals(t *testing.T) {
	ranking := RankingResult{
		Decision:       RankingDecisionIdeate,
		Summary:        "selected",
		CandidateCount: 1,
		Selected:       []RankedCandidate{rankedNovelCandidate("novel")},
	}

	tests := []struct {
		name string
		snap IdeaEvidenceSnapshot
		opts NoveltyGuardOptions
		want GuardRecommendation
		code string
	}{
		{
			name: "stale in progress reviews recent work",
			snap: NewIdeaEvidenceSnapshot("/repo"),
			opts: NoveltyGuardOptions{StaleInProgressIDs: []string{"bd-stale"}},
			want: GuardRecommendationReviewRecentWork,
			code: "stale_in_progress",
		},
		{
			name: "recent bug density validates closeout",
			snap: snapshotWithClosedBugs(3),
			opts: NoveltyGuardOptions{RecentBugThreshold: 3},
			want: GuardRecommendationValidateCloseout,
			code: "recent_bug_density",
		},
		{
			name: "graph cycles validate closeout",
			snap: snapshotWithGraphCycles(),
			opts: NoveltyGuardOptions{},
			want: GuardRecommendationValidateCloseout,
			code: "graph_unhealthy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AssessNoveltyGuard(tt.snap, ranking, tt.opts)
			if got.Recommendation != tt.want {
				t.Fatalf("recommendation=%q, want %q", got.Recommendation, tt.want)
			}
			if !hasReasonCode(got, tt.code) {
				t.Fatalf("reason codes=%v, want %s", got.ReasonCodes, tt.code)
			}
		})
	}
}

func TestNoveltyGuardHealthyNovelQueueIdeates(t *testing.T) {
	snapshot := NewIdeaEvidenceSnapshot("/repo")
	snapshot.Queue.CountsVerified = true
	snapshot.Queue.ReadyCount = 0
	snapshot.Queue.ActionableCount = 0
	snapshot.ExistingWork = closedWorkItems(1, "task")
	ranking := RankingResult{
		Decision:       RankingDecisionIdeate,
		Summary:        "selected one candidate",
		CandidateCount: 1,
		Selected:       []RankedCandidate{rankedNovelCandidate("novel")},
	}

	got := AssessNoveltyGuard(snapshot, ranking, NoveltyGuardOptions{
		RecentClosedThreshold: 5,
		CreationRequested:     true,
	})
	if got.Recommendation != GuardRecommendationIdeate {
		t.Fatalf("recommendation=%q, want ideate", got.Recommendation)
	}
	if !got.CreationAllowed {
		t.Fatalf("creation not allowed for healthy novel queue")
	}
	if !hasReasonCode(got, "healthy_novel_candidates") {
		t.Fatalf("reason codes=%v, want healthy_novel_candidates", got.ReasonCodes)
	}
}

func TestNoveltyGuardExplicitOverrideRecordsReasonWithoutUnsuppressingDuplicates(t *testing.T) {
	ranking := RankingResult{
		Decision:       RankingDecisionReviewRecentWork,
		Summary:        "duplicates",
		CandidateCount: 2,
		Suppressed: []RankedCandidate{
			rankedDuplicateCandidate("dup-1"),
			rankedDuplicateCandidate("dup-2"),
		},
	}

	got := AssessNoveltyGuard(NewIdeaEvidenceSnapshot("/repo"), ranking, NoveltyGuardOptions{
		CreationRequested: true,
		OverrideCreation:  true,
		OverrideReason:    "human asked to create an adjacent follow-up anyway",
	})
	if got.Recommendation != GuardRecommendationReviewRecentWork {
		t.Fatalf("recommendation=%q, want review_recent_work", got.Recommendation)
	}
	if !got.CreationAllowed || !got.OverrideRecorded {
		t.Fatalf("override not recorded or creation not allowed: %+v", got)
	}
	if got.DuplicateSuppressedCount != 2 {
		t.Fatalf("duplicate count=%d, want 2", got.DuplicateSuppressedCount)
	}
	if !hasReasonCode(got, "creation_override_recorded") || !hasNoteCode(got, "creation_override_recorded") {
		t.Fatalf("assessment=%+v, want override reason and note", got)
	}
}

func rankedNovelCandidate(id string) RankedCandidate {
	return RankedCandidate{
		Candidate: IdeaCandidate{
			ID: id,
			Overlap: OverlapVerdict{
				Kind:       OverlapNovel,
				Confidence: 0.9,
				Evidence:   []string{"novel candidate"},
			},
		},
		Included: true,
		Score:    1,
	}
}

func rankedDuplicateCandidate(id string) RankedCandidate {
	return RankedCandidate{
		Candidate: IdeaCandidate{
			ID: id,
			Overlap: OverlapVerdict{
				Kind:       OverlapLikelyDuplicate,
				WorkID:     "bd-old",
				Confidence: 0.8,
				Evidence:   []string{"likely duplicate"},
			},
		},
		Included: false,
		Score:    -1,
	}
}

func closedWorkItems(count int, label string) []ExistingWorkFingerprint {
	items := make([]ExistingWorkFingerprint, 0, count)
	for i := 0; i < count; i++ {
		items = append(items, ExistingWorkFingerprint{
			ID:       "bd-closed-" + twoDigit(i+1),
			Title:    "Closed work " + twoDigit(i+1),
			Status:   WorkStatusClosed,
			Labels:   []string{label},
			Keywords: []string{label},
		})
	}
	return items
}

func snapshotWithClosedBugs(count int) IdeaEvidenceSnapshot {
	snapshot := NewIdeaEvidenceSnapshot("/repo")
	snapshot.ExistingWork = closedWorkItems(count, "bug")
	return snapshot
}

func snapshotWithGraphCycles() IdeaEvidenceSnapshot {
	snapshot := NewIdeaEvidenceSnapshot("/repo")
	snapshot.Triage.GraphHealth.Metrics["has_cycles"] = "true"
	snapshot.Triage.GraphHealth.Evidence = []string{"cycle detected by bv graph health"}
	return snapshot
}

func hasReasonCode(assessment NoveltyGuardAssessment, code string) bool {
	for _, item := range assessment.ReasonCodes {
		if item == code {
			return true
		}
	}
	return false
}

func hasNoteCode(assessment NoveltyGuardAssessment, code string) bool {
	for _, item := range assessment.Notes {
		if item.Code == code {
			return true
		}
	}
	return false
}
