package ideation

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

// RefinementOptions tune scenario-level metadata without changing pipeline
// inputs. ScenarioID is included verbatim in the report; CreationRequested
// mirrors the equivalent flag from NoveltyGuardOptions so the report can
// describe partial-failure semantics without re-reading the guard input.
type RefinementOptions struct {
	ScenarioID        string `json:"scenario_id,omitempty"`
	CreationRequested bool   `json:"creation_requested,omitempty"`
}
