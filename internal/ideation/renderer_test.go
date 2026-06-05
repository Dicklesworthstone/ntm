package ideation

import (
	"strings"
	"testing"
)

func TestRenderRoadmapJSONGoldenStable(t *testing.T) {
	plan := fixtureRoadmapPlan(t)
	data, err := RenderRoadmapJSON(plan)
	if err != nil {
		t.Fatalf("RenderRoadmapJSON failed: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		`"plan_id": "bd-e7xm1-dry-run"`,
		`"dry_run": true`,
		`"title": "Operator's adjacent follow-up"`,
		`"priority": 1`,
		`"overlap verdict: adjacent_follow_up"`,
		`"br create --dry-run --title 'Operator'\"'\"'s adjacent follow-up'`,
		`"br dep add --type 'related' '${BEAD_ID_ADJ}' 'bd-2mb03.5'"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("JSON output missing %q\n%s", want, got)
		}
	}

	data2, err := RenderRoadmapJSON(plan)
	if err != nil {
		t.Fatalf("second RenderRoadmapJSON failed: %v", err)
	}
	if string(data2) != got {
		t.Fatalf("JSON output was not stable")
	}
}

func TestRenderRoadmapMarkdownGolden(t *testing.T) {
	plan := fixtureRoadmapPlan(t)
	got := RenderRoadmapMarkdown(plan)
	want := "# bd-e7xm1-dry-run\n\n" +
		"- Dry run: true\n" +
		"- Decision: ideate\n" +
		"- Rendered candidates: 1\n" +
		"- Parent: bd-e7xm1\n" +
		"- Summary: selected 1 candidate\n" +
		"\n## Proposed Beads\n" +
		"\n### 1. Operator's adjacent follow-up\n\n" +
		"- Ref: ${BEAD_ID_ADJ}\n" +
		"- Candidate: adj\n" +
		"- Priority: P1\n" +
		"- Labels: idea-wizard, operator, queue-dry\n" +
		"- Dependencies: parent-child:bd-e7xm1, related:bd-2mb03.5\n"
	if !strings.HasPrefix(got, want) {
		t.Fatalf("markdown prefix mismatch\n got:\n%s\nwant prefix:\n%s", got, want)
	}
	if !strings.Contains(got, "Source evidence:\n- matched recently closed idea-wizard family bd-2mb03\n- prior work left a gap") {
		t.Fatalf("markdown missing source evidence section:\n%s", got)
	}
}

func TestRenderRoadmapCommandPreviewEscapesShell(t *testing.T) {
	plan := fixtureRoadmapPlan(t)
	if len(plan.CommandPreview) != 1 {
		t.Fatalf("commands=%d, want 1", len(plan.CommandPreview))
	}
	if !strings.Contains(plan.CommandPreview[0], "Operator'\"'\"'s adjacent follow-up") {
		t.Fatalf("command did not shell-escape apostrophe: %s", plan.CommandPreview[0])
	}
	if strings.Contains(plan.CommandPreview[0], "\n") {
		t.Fatalf("command contains newline: %q", plan.CommandPreview[0])
	}
}

func TestRenderRoadmapDependencyPreview(t *testing.T) {
	plan := fixtureRoadmapPlan(t)
	if len(plan.DependencyPreview) != 2 {
		t.Fatalf("dependency preview len=%d, want 2: %v", len(plan.DependencyPreview), plan.DependencyPreview)
	}
	if plan.DependencyPreview[0] != "br dep add --type 'parent-child' '${BEAD_ID_ADJ}' 'bd-e7xm1'" {
		t.Fatalf("first dependency command=%q", plan.DependencyPreview[0])
	}
	if plan.DependencyPreview[1] != "br dep add --type 'related' '${BEAD_ID_ADJ}' 'bd-2mb03.5'" {
		t.Fatalf("second dependency command=%q", plan.DependencyPreview[1])
	}
}

func TestRenderRoadmapHonorsExplicitDefaultPriority(t *testing.T) {
	cases := []struct {
		name     string
		priority *int
		rank     int
		want     int
	}{
		{name: "explicit P0", priority: intPtr(0), rank: 5, want: 0},
		{name: "explicit P3", priority: intPtr(3), rank: 1, want: 3},
		{name: "unset falls back to rank-based", priority: nil, rank: 1, want: 1},
		{name: "unset rank 6 falls back to medium", priority: nil, rank: 6, want: 2},
		{name: "out-of-range -1 is treated as unset", priority: intPtr(-1), rank: 5, want: 2},
		{name: "out-of-range 9 is treated as unset", priority: intPtr(9), rank: 12, want: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := RankingResult{
				Decision: RankingDecisionIdeate,
				Selected: []RankedCandidate{{Rank: tc.rank, Candidate: IdeaCandidate{ID: "x", Title: "x", Overlap: OverlapVerdict{Kind: OverlapNovel}}, Score: 1}},
			}
			plan := RenderRoadmap(result, RoadmapRenderOptions{DefaultPriority: tc.priority})
			if len(plan.ProposedBeads) != 1 {
				t.Fatalf("expected 1 proposed bead, got %d", len(plan.ProposedBeads))
			}
			if got := plan.ProposedBeads[0].Priority; got != tc.want {
				t.Fatalf("priority=%d, want %d", got, tc.want)
			}
		})
	}
}

func intPtr(v int) *int { return &v }

func TestRenderRoadmapCanIncludeNextBest(t *testing.T) {
	first := RankedCandidate{Rank: 1, Candidate: IdeaCandidate{ID: "top", Title: "Top", Labels: []string{"queue-dry"}, Evidence: []string{"top evidence"}, Overlap: OverlapVerdict{Kind: OverlapNovel}}, Score: 4}
	next := RankedCandidate{Rank: 6, Candidate: IdeaCandidate{ID: "next", Title: "Next", Labels: []string{"queue-dry"}, Evidence: []string{"next evidence"}, Overlap: OverlapVerdict{Kind: OverlapNovel}}, Score: 3}
	result := RankingResult{Decision: RankingDecisionIdeate, Summary: "selected", Selected: []RankedCandidate{first}, NextBest: []RankedCandidate{next}, Suppressed: []RankedCandidate{}}

	withoutNext := RenderRoadmap(result, RoadmapRenderOptions{})
	if withoutNext.RenderedCount != 1 {
		t.Fatalf("without next rendered=%d, want 1", withoutNext.RenderedCount)
	}
	withNext := RenderRoadmap(result, RoadmapRenderOptions{IncludeNextBest: true})
	if withNext.RenderedCount != 2 {
		t.Fatalf("with next rendered=%d, want 2", withNext.RenderedCount)
	}
	if withNext.ProposedBeads[1].CandidateID != "next" {
		t.Fatalf("second candidate=%q, want next", withNext.ProposedBeads[1].CandidateID)
	}
}

func fixtureRoadmapPlan(t *testing.T) RoadmapPlan {
	t.Helper()
	candidate := IdeaCandidate{
		ID:       "adj",
		Title:    "Operator's adjacent follow-up",
		Summary:  "Implement an adjacent follow-up without duplicating prior work.",
		Labels:   []string{"operator", "queue-dry"},
		Evidence: []string{"prior work left a gap"},
		RelatedWork: []RelatedWorkReference{
			{ID: "bd-2mb03.5", Relationship: RelationshipFollowUp, Evidence: []string{"remaining evidence gap"}},
		},
		Overlap: OverlapVerdict{
			Kind:       OverlapAdjacentFollowUp,
			WorkID:     "bd-2mb03.5",
			FamilyID:   "bd-2mb03",
			Confidence: 0.85,
			Evidence:   []string{"matched recently closed idea-wizard family bd-2mb03", "prior work left a gap"},
		},
	}
	result := RankingResult{
		Decision: RankingDecisionIdeate,
		Summary:  "selected 1 candidate",
		Selected: []RankedCandidate{
			{Rank: 1, Candidate: candidate, Score: 4.2, Included: true},
		},
		NextBest:   []RankedCandidate{},
		Suppressed: []RankedCandidate{{Candidate: IdeaCandidate{ID: "dup"}}},
	}
	return RenderRoadmap(result, RoadmapRenderOptions{
		PlanID:   "bd-e7xm1-dry-run",
		ParentID: "bd-e7xm1",
		AcceptanceCriteria: []string{
			"candidate is self-contained",
			"duplicate evidence is visible",
		},
		VerificationCommands: []string{"go test -short ./internal/ideation/..."},
		NonGoals:             []string{"do not mutate beads in dry-run mode"},
	})
}
