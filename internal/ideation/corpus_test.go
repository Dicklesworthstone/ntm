package ideation

// corpus_test.go pins the queue-dry ideation pipeline against a curated set
// of named scenarios so the duplicate-aware ranker, dry-run renderer, and
// novelty guard cannot silently regress into duplicate-spam or skip the
// "review recent work" path.
//
// Bead: bd-e7xm1.7
//
// Each scenario produces four golden artifacts under
//   testdata/golden/<scenario_id>/
//     evidence.json    -- the IdeaEvidenceSnapshot input contract
//     ranked.json      -- RankCandidates(snapshot) result
//     roadmap.json     -- RenderRoadmap(ranking) result
//     refinement.json  -- BuildRefinementReport(snapshot, ranking, plan, guard)
//
// The artifacts are stable, redacted, and small. Each scenario emits a single
// log line with the structured counters required by the bead spec:
//   scenario_id=<id> candidate_count=<n> suppressed_duplicate_count=<n>
//   rendered_bead_count=<n>
//
// To regenerate goldens after an intentional contract change:
//   go test -run TestQueueDryIdeationGoldenCorpus ./internal/ideation/... \
//     -update-ideation-goldens
//
// CI is short-mode by default: no network, no real model calls, no real br
// mutation. Tests do not write outside testdata/.

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

var updateIdeationGoldens = flag.Bool("update-ideation-goldens", false, "Update queue-dry ideation golden corpus files")

const corpusGoldenDir = "testdata/golden"

type corpusScenario struct {
	id          string
	description string
	build       func() corpusInputs
	rank        RankOptions
	render      RoadmapRenderOptions
	guard       NoveltyGuardOptions
	refinement  RefinementOptions
}

type corpusInputs struct {
	snapshot IdeaEvidenceSnapshot
}

func TestQueueDryIdeationGoldenCorpus(t *testing.T) {
	scenarios := []corpusScenario{
		dryMatureClosedFamiliesScenario(),
		dryWithStaleInProgressScenario(),
		nonDryReadyWorkWinsScenario(),
		degradedOptionalSourcesScenario(),
		duplicateCandidateFamilyScenario(),
		adjacentFollowUpCandidateScenario(),
		partialBRCreationFailureScenario(),
	}

	seen := make(map[string]struct{}, len(scenarios))
	for _, scenario := range scenarios {
		if _, dup := seen[scenario.id]; dup {
			t.Fatalf("duplicate scenario id %q", scenario.id)
		}
		seen[scenario.id] = struct{}{}
	}

	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario.id, func(t *testing.T) {
			runCorpusScenario(t, scenario)
		})
	}
}

func runCorpusScenario(t *testing.T, scenario corpusScenario) {
	t.Helper()
	inputs := scenario.build()
	snapshot := inputs.snapshot
	ranking := RankCandidates(snapshot, scenario.rank)
	plan := RenderRoadmap(ranking, scenario.render)
	guard := AssessNoveltyGuard(snapshot, ranking, scenario.guard)
	refinement := BuildRefinementReport(snapshot, ranking, plan, guard, scenario.refinement)

	t.Logf("scenario_id=%s candidate_count=%d suppressed_duplicate_count=%d rendered_bead_count=%d",
		scenario.id,
		ranking.CandidateCount,
		duplicateSuppressedCount(ranking),
		plan.RenderedCount,
	)

	artifacts := []struct {
		name  string
		value any
	}{
		{name: "evidence.json", value: snapshot},
		{name: "ranked.json", value: ranking},
		{name: "roadmap.json", value: plan},
		{name: "refinement.json", value: refinement},
	}
	for _, artifact := range artifacts {
		actual := mustMarshalGolden(t, artifact.value)
		path := filepath.Join(corpusGoldenDir, scenario.id, artifact.name)
		assertGoldenMatches(t, path, actual)
	}
}

func mustMarshalGolden(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden: %v", err)
	}
	data = append(data, '\n')
	return data
}

func assertGoldenMatches(t *testing.T, path string, actual []byte) {
	t.Helper()
	if *updateIdeationGoldens {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir golden: %v", err)
		}
		if err := os.WriteFile(path, actual, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		t.Logf("updated golden %s", path)
		return
	}
	expected, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			t.Fatalf("golden %s missing: re-run with -update-ideation-goldens to seed it", path)
		}
		t.Fatalf("read golden %s: %v", path, err)
	}
	if string(expected) == string(actual) {
		return
	}
	t.Fatalf("golden %s mismatch\n--- expected\n%s\n--- actual\n%s", path, expected, actual)
}

// =============================================================================
// Scenario builders
// =============================================================================

// dryMatureClosedFamiliesScenario covers the canonical queue-dry case: a
// mature repo where the operator has already closed several known idea-wizard
// families. The ranker should still find one or more genuinely novel ideas
// while the guard reports a healthy-novel-candidates verdict.
func dryMatureClosedFamiliesScenario() corpusScenario {
	build := func() corpusInputs {
		snapshot := NewIdeaEvidenceSnapshot("/repo")
		snapshot.Queue.CountsVerified = true
		snapshot.Queue.OpenCount = 0
		snapshot.Queue.ReadyCount = 0
		snapshot.Queue.ActionableCount = 0
		snapshot.RecordSource(CandidateSource{ID: "br:ready", Kind: SourceBR, Available: true, Required: true, Evidence: []string{"br ready collector"}})
		snapshot.RecordSource(CandidateSource{ID: "bv:triage", Kind: SourceBV, Available: true, Required: true, Evidence: []string{"bv --robot-triage"}})
		snapshot.ExistingWork = []ExistingWorkFingerprint{
			closedFamilyWork("bd-2mb03.5", "bd-2mb03", "Queue-dry operator autopilot", []string{"queue", "dry", "autopilot"}),
			closedFamilyWork("bd-fxj4f.3", "bd-fxj4f", "Robot contract replay harness", []string{"robot", "contract", "replay"}),
			closedFamilyWork("bd-8kglp.4", "bd-8kglp", "RCH build storm backpressure", []string{"rch", "backpressure"}),
			closedFamilyWork("bd-3v1gs.2", "bd-3v1gs", "Idea wizard activity log", []string{"activity", "log"}),
		}
		snapshot.Candidates = []IdeaCandidate{
			{
				ID:        "novel-feedback-loop",
				Title:     "Idea effectiveness feedback loop",
				Summary:   "Track which queue-dry ideas became real beads to retire stale candidate generators.",
				Labels:    []string{"feedback", "queue-dry"},
				Keywords:  []string{"effectiveness", "feedback", "loop"},
				SourceIDs: []string{"br:closed", "bv:triage"},
				Evidence:  []string{"closed tranche shows recurring duplicate-spam without effectiveness signal"},
			},
			{
				ID:        "novel-temp-workspace",
				Title:     "End-to-end queue-dry temp-workspace gate",
				Summary:   "Run the dry-run pipeline against a synthetic temp workspace to keep mutation paths inert.",
				Labels:    []string{"queue-dry", "testing"},
				Keywords:  []string{"e2e", "temp", "workspace"},
				SourceIDs: []string{"br:closed"},
				Evidence:  []string{"need a regression seal beyond unit goldens"},
			},
		}
		return corpusInputs{snapshot: snapshot}
	}
	return corpusScenario{
		id:          "dry_mature_closed_families",
		description: "queue is dry; recent idea-wizard tranches closed; truly novel candidates remain",
		build:       build,
		rank:        DefaultRankOptions(),
		render:      RoadmapRenderOptions{PlanID: "dry-mature-roadmap", ParentID: "bd-e7xm1"},
		guard:       NoveltyGuardOptions{RecentClosedThreshold: 10, RecentBugThreshold: 5},
		refinement:  RefinementOptions{ScenarioID: "dry_mature_closed_families"},
	}
}

// dryWithStaleInProgressScenario verifies that stale in-progress IDs flip the
// guard to review_recent_work even when the ranker has selected a novel
// candidate. The ranker is allowed to pick an idea but the operator must
// resolve the stale claim before mutating beads.
func dryWithStaleInProgressScenario() corpusScenario {
	build := func() corpusInputs {
		snapshot := NewIdeaEvidenceSnapshot("/repo")
		snapshot.Queue.CountsVerified = true
		snapshot.Queue.OpenCount = 1
		snapshot.Queue.ReadyCount = 0
		snapshot.Queue.ActionableCount = 0
		snapshot.Queue.InProgressCount = 1
		snapshot.RecordSource(CandidateSource{ID: "br:ready", Kind: SourceBR, Available: true, Required: true, Evidence: []string{"br ready collector"}})
		snapshot.ExistingWork = []ExistingWorkFingerprint{
			{
				ID:        "bd-stale.1",
				FamilyID:  "bd-stale",
				Title:     "Stale work that should be resolved",
				Status:    WorkStatusInProgress,
				Labels:    []string{"stale"},
				Keywords:  []string{"stale", "work"},
				SourceIDs: []string{"br:in_progress"},
				Evidence:  []string{"in_progress > 1h without commits"},
				UpdatedAt: "2026-04-01T00:00:00Z",
			},
		}
		snapshot.Candidates = []IdeaCandidate{
			{
				ID:        "novel-pause-gate",
				Title:     "Pause-gate for stale in-progress beads",
				Summary:   "Detect stale in-progress beads and surface them before queue-dry ideation.",
				Labels:    []string{"queue-dry", "guard"},
				Keywords:  []string{"stale", "guard", "pause"},
				SourceIDs: []string{"br:in_progress"},
				Evidence:  []string{"stale in_progress bead detected by collector"},
			},
		}
		return corpusInputs{snapshot: snapshot}
	}
	return corpusScenario{
		id:          "dry_with_stale_in_progress",
		description: "queue is dry but stale in-progress IDs require review before ideating",
		build:       build,
		rank:        DefaultRankOptions(),
		render:      RoadmapRenderOptions{PlanID: "dry-stale-roadmap", ParentID: "bd-e7xm1"},
		guard:       NoveltyGuardOptions{StaleInProgressIDs: []string{"bd-stale.1"}, CreationRequested: true},
		refinement:  RefinementOptions{ScenarioID: "dry_with_stale_in_progress", CreationRequested: true},
	}
}

// nonDryReadyWorkWinsScenario verifies that when the queue has actionable
// ready work, the refinement report makes the ready_count visible and the
// guard does not allow creation unless overridden. This is the regression
// gate that prevents queue-dry ideation from running against a busy queue.
func nonDryReadyWorkWinsScenario() corpusScenario {
	build := func() corpusInputs {
		snapshot := NewIdeaEvidenceSnapshot("/repo")
		snapshot.Queue.CountsVerified = true
		snapshot.Queue.OpenCount = 5
		snapshot.Queue.ReadyCount = 3
		snapshot.Queue.ActionableCount = 3
		snapshot.RecordSource(CandidateSource{ID: "br:ready", Kind: SourceBR, Available: true, Required: true, Evidence: []string{"br ready collector"}})
		snapshot.ExistingWork = []ExistingWorkFingerprint{
			{ID: "bd-ready-01", Title: "Implement priority queue alarm", Status: WorkStatusOpen, Labels: []string{"alarm"}, Keywords: []string{"priority", "queue"}, SourceIDs: []string{"br:open"}, Evidence: []string{"ready bead 01"}},
			{ID: "bd-ready-02", Title: "Investigate flake in bv graph health", Status: WorkStatusOpen, Labels: []string{"bv"}, Keywords: []string{"flake", "graph"}, SourceIDs: []string{"br:open"}, Evidence: []string{"ready bead 02"}},
			{ID: "bd-ready-03", Title: "Wire activity feed to overlay", Status: WorkStatusOpen, Labels: []string{"feed"}, Keywords: []string{"activity", "overlay"}, SourceIDs: []string{"br:open"}, Evidence: []string{"ready bead 03"}},
		}
		snapshot.Candidates = []IdeaCandidate{
			{
				ID:        "tempting-new-feature",
				Title:     "Glamour cursor sparkles for the activity feed",
				Summary:   "Cosmetic upgrade unrelated to the ready queue.",
				Labels:    []string{"ux"},
				Keywords:  []string{"sparkles", "cursor"},
				SourceIDs: []string{"br:closed"},
				Evidence:  []string{"operator request from screenshot review"},
			},
		}
		return corpusInputs{snapshot: snapshot}
	}
	return corpusScenario{
		id:          "non_dry_ready_work_wins",
		description: "actionable queue work exists; refinement report must not allow creation without override",
		build:       build,
		rank:        DefaultRankOptions(),
		render:      RoadmapRenderOptions{PlanID: "non-dry-roadmap"},
		guard:       NoveltyGuardOptions{CreationRequested: true},
		refinement:  RefinementOptions{ScenarioID: "non_dry_ready_work_wins", CreationRequested: true},
	}
}

// degradedOptionalSourcesScenario covers Agent Mail, CASS, and CM all
// reporting unavailable. The pipeline must keep working with the sources it
// has, label degraded sources, and surface a degraded-source next action.
func degradedOptionalSourcesScenario() corpusScenario {
	build := func() corpusInputs {
		snapshot := NewIdeaEvidenceSnapshot("/repo")
		snapshot.Queue.CountsVerified = true
		snapshot.Queue.ReadyCount = 0
		snapshot.Queue.ActionableCount = 0
		snapshot.RecordSource(CandidateSource{ID: "br:ready", Kind: SourceBR, Available: true, Required: true, Evidence: []string{"br ready collector"}})
		snapshot.RecordSource(CandidateSource{ID: "bv:triage", Kind: SourceBV, Available: true, Required: true, Evidence: []string{"bv --robot-triage"}})
		snapshot.RecordSource(CandidateSource{ID: "agent_mail", Kind: SourceAgentMail, Available: false, Required: false, Error: "agent_mail unreachable"})
		snapshot.RecordSource(CandidateSource{ID: "cass", Kind: SourceCASS, Available: false, Required: false, Error: "cass binary missing"})
		snapshot.RecordSource(CandidateSource{ID: "cm", Kind: SourceCM, Available: false, Required: false, Error: "cm binary missing"})
		snapshot.Candidates = []IdeaCandidate{
			{
				ID:        "novel-degraded-aware",
				Title:     "Render degraded-source warnings in queue-dry output",
				Summary:   "Make optional source unavailability visible in operator output.",
				Labels:    []string{"queue-dry", "operator"},
				Keywords:  []string{"degraded", "source", "warning"},
				SourceIDs: []string{"br:closed"},
				Evidence:  []string{"agent mail and cass unavailable for this run"},
			},
		}
		return corpusInputs{snapshot: snapshot}
	}
	return corpusScenario{
		id:          "degraded_optional_sources",
		description: "agent mail, cass, and cm unavailable; pipeline degrades gracefully",
		build:       build,
		rank:        DefaultRankOptions(),
		render:      RoadmapRenderOptions{PlanID: "degraded-roadmap", ParentID: "bd-e7xm1"},
		guard:       NoveltyGuardOptions{},
		refinement:  RefinementOptions{ScenarioID: "degraded_optional_sources"},
	}
}

// duplicateCandidateFamilyScenario seals the duplicate-aware suppression path.
// One candidate matches a recently closed idea-wizard family by exact title;
// the other is a genuinely novel idea. The duplicate must be suppressed and
// the guard must surface the duplicate-heavy reason once the suppression
// passes the configured threshold.
func duplicateCandidateFamilyScenario() corpusScenario {
	build := func() corpusInputs {
		snapshot := NewIdeaEvidenceSnapshot("/repo")
		snapshot.Queue.CountsVerified = true
		snapshot.Queue.ReadyCount = 0
		snapshot.Queue.ActionableCount = 0
		snapshot.RecordSource(CandidateSource{ID: "br:ready", Kind: SourceBR, Available: true, Required: true, Evidence: []string{"br ready collector"}})
		snapshot.ExistingWork = []ExistingWorkFingerprint{
			closedFamilyWork("bd-2mb03.5", "bd-2mb03", "Queue-dry operator autopilot", []string{"queue", "dry", "autopilot"}),
		}
		snapshot.Candidates = []IdeaCandidate{
			{
				ID:        "dup-autopilot",
				Title:     "Queue dry operator autopilot",
				Summary:   "Reintroduce the queue-dry operator autopilot; same scope as bd-2mb03.5.",
				Labels:    []string{"queue-dry", "operator"},
				Keywords:  []string{"queue", "dry", "autopilot"},
				SourceIDs: []string{"br:closed"},
				Evidence:  []string{"candidate generator did not check closed families"},
			},
			{
				ID:        "novel-feedback-effects",
				Title:     "Idea effectiveness feedback for closed candidates",
				Summary:   "Track candidate-to-bead conversions to retire stale generators.",
				Labels:    []string{"feedback", "queue-dry"},
				Keywords:  []string{"feedback", "effectiveness"},
				SourceIDs: []string{"br:closed"},
				Evidence:  []string{"closed tranche lacked an effectiveness signal"},
			},
		}
		return corpusInputs{snapshot: snapshot}
	}
	return corpusScenario{
		id:          "duplicate_candidate_family",
		description: "one candidate exactly duplicates a closed family; suppression must hold",
		build:       build,
		rank:        DefaultRankOptions(),
		render:      RoadmapRenderOptions{PlanID: "duplicate-roadmap", ParentID: "bd-e7xm1"},
		guard:       NoveltyGuardOptions{DuplicateHeavyThreshold: 0.5},
		refinement:  RefinementOptions{ScenarioID: "duplicate_candidate_family"},
	}
}

// adjacentFollowUpCandidateScenario verifies that explicit follow-up
// references to a closed family route to the adjacent follow-up overlap
// kind, are kept in the rendered roadmap, and emit a related dependency
// command rather than a duplicate suppression.
func adjacentFollowUpCandidateScenario() corpusScenario {
	build := func() corpusInputs {
		snapshot := NewIdeaEvidenceSnapshot("/repo")
		snapshot.Queue.CountsVerified = true
		snapshot.Queue.ReadyCount = 0
		snapshot.Queue.ActionableCount = 0
		snapshot.RecordSource(CandidateSource{ID: "br:closed", Kind: SourceBR, Available: true, Required: false, Evidence: []string{"br closed collector"}})
		snapshot.ExistingWork = []ExistingWorkFingerprint{
			closedFamilyWork("bd-8kglp.4", "bd-8kglp", "RCH build storm backpressure", []string{"rch", "backpressure"}),
		}
		snapshot.Candidates = []IdeaCandidate{
			{
				ID:        "follow-up-rch-evidence",
				Title:     "Use RCH backpressure history as queue-dry evidence",
				Summary:   "Reuse shipped RCH backpressure metrics to feed queue-dry ranking instead of duplicating it.",
				Labels:    []string{"queue-dry", "rch"},
				Keywords:  []string{"rch", "backpressure", "evidence"},
				SourceIDs: []string{"br:closed"},
				Evidence:  []string{"prior work left a gap in feeding metrics into queue-dry ranking"},
				RelatedWork: []RelatedWorkReference{
					{ID: "bd-8kglp.4", Relationship: RelationshipFollowUp, Evidence: []string{"remaining evidence gap from closed RCH bead"}},
				},
			},
		}
		return corpusInputs{snapshot: snapshot}
	}
	return corpusScenario{
		id:          "adjacent_follow_up_candidate",
		description: "candidate references a closed family as follow-up; must keep adjacent overlap and related dep",
		build:       build,
		rank:        DefaultRankOptions(),
		render:      RoadmapRenderOptions{PlanID: "adjacent-roadmap", ParentID: "bd-e7xm1"},
		guard:       NoveltyGuardOptions{},
		refinement:  RefinementOptions{ScenarioID: "adjacent_follow_up_candidate"},
	}
}

// partialBRCreationFailureScenario simulates an operator requesting creation
// across a mixed candidate set: one novel candidate would be created, one
// duplicate would be suppressed. The refinement report must label this as a
// partial creation failure and emit a follow-up next-action.
func partialBRCreationFailureScenario() corpusScenario {
	build := func() corpusInputs {
		snapshot := NewIdeaEvidenceSnapshot("/repo")
		snapshot.Queue.CountsVerified = true
		snapshot.Queue.ReadyCount = 0
		snapshot.Queue.ActionableCount = 0
		snapshot.RecordSource(CandidateSource{ID: "br:ready", Kind: SourceBR, Available: true, Required: true, Evidence: []string{"br ready collector"}})
		snapshot.ExistingWork = []ExistingWorkFingerprint{
			closedFamilyWork("bd-fxj4f.3", "bd-fxj4f", "Robot contract replay harness", []string{"robot", "contract", "replay"}),
		}
		snapshot.Candidates = []IdeaCandidate{
			{
				ID:        "dup-replay",
				Title:     "Robot contract replay harness",
				Summary:   "Recreate the closed robot contract replay harness.",
				Labels:    []string{"robot", "queue-dry"},
				Keywords:  []string{"robot", "contract", "replay"},
				SourceIDs: []string{"br:closed"},
				Evidence:  []string{"candidate generator did not check fxj4f closure"},
			},
			{
				ID:        "novel-temp-workspace-gate",
				Title:     "Queue-dry temp-workspace creation gate",
				Summary:   "Sandbox creation flow against a synthetic temp workspace to keep dry-run inert.",
				Labels:    []string{"queue-dry", "testing"},
				Keywords:  []string{"temp", "workspace", "gate"},
				SourceIDs: []string{"br:closed"},
				Evidence:  []string{"creation flow lacked an isolation gate"},
			},
		}
		return corpusInputs{snapshot: snapshot}
	}
	return corpusScenario{
		id:          "partial_br_creation_failure",
		description: "creation requested with mixed novel/duplicate candidates; partial creation must be flagged",
		build:       build,
		rank:        DefaultRankOptions(),
		render:      RoadmapRenderOptions{PlanID: "partial-roadmap", ParentID: "bd-e7xm1"},
		guard: NoveltyGuardOptions{
			CreationRequested: true,
			OverrideCreation:  true,
			OverrideReason:    "operator wants to create the novel candidate even though one duplicate is suppressed",
		},
		refinement: RefinementOptions{ScenarioID: "partial_br_creation_failure", CreationRequested: true},
	}
}

func closedFamilyWork(id, family, title string, keywords []string) ExistingWorkFingerprint {
	sortedKeywords := append([]string(nil), keywords...)
	sort.Strings(sortedKeywords)
	return ExistingWorkFingerprint{
		ID:        id,
		FamilyID:  family,
		Title:     title,
		Status:    WorkStatusClosed,
		Labels:    []string{"queue-dry"},
		Keywords:  sortedKeywords,
		SourceIDs: []string{"br:closed"},
		Evidence:  []string{fmt.Sprintf("known closed idea-wizard family %s", family)},
		UpdatedAt: "2026-04-15T00:00:00Z",
	}
}
