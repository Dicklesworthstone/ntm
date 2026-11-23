package bv

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBVClientEstimateSize(t *testing.T) {
	t.Parallel()

	client := NewBVClient()

	tests := []struct {
		name string
		rec  TriageRecommendation
		want string
	}{
		{
			name: "epic_is_large",
			rec: TriageRecommendation{
				Type: "epic",
			},
			want: "large",
		},
		{
			name: "high_betweenness_is_large",
			rec: TriageRecommendation{
				Type: "task",
				Breakdown: &ScoreBreakdown{
					Betweenness: 0.1001,
				},
			},
			want: "large",
		},
		{
			name: "unblocks_many_is_large",
			rec: TriageRecommendation{
				Type:        "task",
				UnblocksIDs: []string{"a", "b", "c", "d"},
				Breakdown: &ScoreBreakdown{
					Betweenness: 0,
				},
			},
			want: "large",
		},
		{
			name: "leaf_low_betweenness_is_small",
			rec: TriageRecommendation{
				Type:        "task",
				UnblocksIDs: nil,
				Breakdown: &ScoreBreakdown{
					Betweenness: 0.01,
				},
			},
			want: "small",
		},
		{
			name: "leaf_without_breakdown_is_medium",
			rec: TriageRecommendation{
				Type:        "task",
				UnblocksIDs: nil,
				Breakdown:   nil,
			},
			want: "medium",
		},
		{
			name: "default_is_medium",
			rec: TriageRecommendation{
				Type:        "task",
				UnblocksIDs: []string{"a"},
				Breakdown: &ScoreBreakdown{
					Betweenness: 0.01,
				},
			},
			want: "medium",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := client.estimateSize(tt.rec); got != tt.want {
				t.Fatalf("estimateSize() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBVClientConvertRecommendation(t *testing.T) {
	t.Parallel()

	client := NewBVClient()

	t.Run("maps_fields_and_infers_actionable", func(t *testing.T) {
		t.Parallel()

		in := TriageRecommendation{
			ID:          "bd-1",
			Title:       "Test task",
			Type:        "task",
			Priority:    2,
			Labels:      []string{"go", "backend"},
			Score:       0.42,
			Action:      "Do the thing",
			Reasons:     []string{"because"},
			UnblocksIDs: []string{"x", "y"},
			BlockedBy:   []string{"b1"},
			Breakdown: &ScoreBreakdown{
				Pagerank:    0.123,
				Betweenness: 0.2, // large (critical path)
			},
		}

		out := client.convertRecommendation(in)

		if out.ID != in.ID {
			t.Fatalf("ID = %q, want %q", out.ID, in.ID)
		}
		if out.Title != in.Title {
			t.Fatalf("Title = %q, want %q", out.Title, in.Title)
		}
		if out.Priority != in.Priority {
			t.Fatalf("Priority = %d, want %d", out.Priority, in.Priority)
		}
		if out.UnblocksCount != 2 {
			t.Fatalf("UnblocksCount = %d, want 2", out.UnblocksCount)
		}
		if out.IsActionable {
			t.Fatalf("IsActionable = true, want false")
		}
		if out.PageRank != 0.123 {
			t.Fatalf("PageRank = %v, want %v", out.PageRank, 0.123)
		}
		if out.Betweenness != 0.2 {
			t.Fatalf("Betweenness = %v, want %v", out.Betweenness, 0.2)
		}
		if out.EstimatedSize != "large" {
			t.Fatalf("EstimatedSize = %q, want %q", out.EstimatedSize, "large")
		}
	})

	t.Run("leaf_with_low_betweenness_is_small_and_actionable", func(t *testing.T) {
		t.Parallel()

		out := client.convertRecommendation(TriageRecommendation{
			ID:          "bd-2",
			Title:       "Leaf",
			Type:        "task",
			UnblocksIDs: nil,
			BlockedBy:   nil,
			Breakdown: &ScoreBreakdown{
				Betweenness: 0.01,
			},
		})

		if !out.IsActionable {
			t.Fatalf("IsActionable = false, want true")
		}
		if out.EstimatedSize != "small" {
			t.Fatalf("EstimatedSize = %q, want %q", out.EstimatedSize, "small")
		}
	})

	t.Run("leaf_without_breakdown_defaults_to_medium", func(t *testing.T) {
		t.Parallel()

		out := client.convertRecommendation(TriageRecommendation{
			ID:          "bd-3",
			Title:       "Leaf without breakdown",
			Type:        "task",
			UnblocksIDs: nil,
			BlockedBy:   nil,
			Breakdown:   nil,
		})

		if out.EstimatedSize != "medium" {
			t.Fatalf("EstimatedSize = %q, want %q", out.EstimatedSize, "medium")
		}
	})
}

func TestBVClientBuildInsightsFromResponse(t *testing.T) {
	t.Parallel()

	client := NewBVClient()

	resp := &InsightsResponse{
		Cycles: [][]string{
			{"A", "B", "C"},
		},
	}

	insights := client.buildInsightsFromResponse(resp, "")

	if insights == nil {
		t.Fatal("expected non-nil insights")
	}
}

// bvCycleInsights is from real `bv --robot-insights` output (bv v0.25.2) on a
// two-issue graph where each issue blocks the other: bv's Insights.Cycles is
// a list of ID lists.
const bvCycleInsights = `{"Bottlenecks":[{"ID":"cy-a","Value":0}],"Cycles":[["cy-a","cy-b","cy-a"]]}`

// ntm decoded Cycles as objects, so --robot-insights failed to parse whenever
// the graph had a cycle: --robot-graph errored and the cycle alert never fired.
func TestInsightsResponseDecodesBVCycles(t *testing.T) {
	var resp InsightsResponse
	if err := json.Unmarshal([]byte(bvCycleInsights), &resp); err != nil {
		t.Fatalf("decode bv insights: %v", err)
	}
	if len(resp.Cycles) != 1 || strings.Join(resp.Cycles[0], ",") != "cy-a,cy-b,cy-a" {
		t.Fatalf("Cycles = %v, want [[cy-a cy-b cy-a]]", resp.Cycles)
	}
	if insights := NewBVClient().buildInsightsFromResponse(&resp, ""); len(insights.Cycles) != 1 {
		t.Fatalf("insights cycles = %v, want the bv cycle", insights.Cycles)
	}
}

// Captured from bv v0.25.2: `bv --robot-suggest` on two near-duplicate issues
// (trimmed to one suggestion), and the project_health block of
// `bv --robot-triage` on a two-issue workspace.
const (
	bvSuggestFragment      = `{"suggestions":{"suggestions":[{"type":"potential_duplicate","target_bead":"sg-a","related_bead":"sg-b","summary":"Potential duplicate of sg-b","reason":"93% keyword similarity; common: during, expires, fix, form, get","confidence":0.9285714285714286,"generated_at":"2026-10-06T20:09:05.387498439Z","metadata":{"action_unavailable_reason":"source has no verified live tracker route","method":"jaccard"}}],"stats":{"total":2,"by_type":{"missing_dependency":1,"potential_duplicate":1},"by_confidence":{"high":1,"medium":1},"high_confidence_count":1,"actionable_count":0}}}`
	bvTriageHealthFragment = `{"counts":{"total":2,"open":2,"closed":0,"blocked":0,"actionable":0,"not_closed":2,"dependency_blocked":2,"by_status":{"open":2},"by_type":{"task":2},"by_priority":{"2":2}},"graph":{"node_count":2,"edge_count":2,"density":1,"has_cycles":false,"phase2_ready":true}}`
)

// bv wraps suggestions in a SuggestionSet object; decoding it as a list made
// every --robot-suggest call fail with INTERNAL_ERROR.
func TestSuggestionsResponseDecodesBVSuggestionSet(t *testing.T) {
	var resp SuggestionsResponse
	if err := json.Unmarshal([]byte(bvSuggestFragment), &resp); err != nil {
		t.Fatalf("decode bv suggestions: %v", err)
	}
	set := resp.Suggestions
	if len(set.Suggestions) != 1 || set.Stats.Total != 2 || set.Stats.HighConfidenceCount != 1 {
		t.Fatalf("suggestion set = %+v", set)
	}
	if s := set.Suggestions[0]; s.Type != "potential_duplicate" || s.TargetBead != "sg-a" || s.RelatedBead != "sg-b" || s.Confidence < 0.9 {
		t.Fatalf("suggestion = %+v", s)
	}
}

// Triage health reads bv's counts/graph layout (and still the older
// status_distribution/graph_metrics one); before, it was always empty.
func TestProjectHealthDecodesBVTriageShape(t *testing.T) {
	var h ProjectHealth
	if err := json.Unmarshal([]byte(bvTriageHealthFragment), &h); err != nil {
		t.Fatalf("decode bv project_health: %v", err)
	}
	if h.Total != 2 || h.StatusDistribution["open"] != 2 || h.TypeDistribution["task"] != 2 || h.PriorityDistribution["2"] != 2 {
		t.Fatalf("counts = total %d status %v type %v priority %v", h.Total, h.StatusDistribution, h.TypeDistribution, h.PriorityDistribution)
	}
	if g := h.GraphMetrics; g == nil || g.TotalNodes != 2 || g.TotalEdges != 2 || g.Density != 1 {
		t.Fatalf("graph metrics = %+v", g)
	}

	var legacy ProjectHealth
	if err := json.Unmarshal([]byte(`{"status_distribution":{"open":1},"graph_metrics":{"total_nodes":3,"total_edges":1,"cycle_count":1}}`), &legacy); err != nil {
		t.Fatalf("decode legacy project_health: %v", err)
	}
	if legacy.StatusDistribution["open"] != 1 || legacy.GraphMetrics == nil || legacy.GraphMetrics.TotalNodes != 3 || legacy.GraphMetrics.CycleCount != 1 {
		t.Fatalf("legacy health = %+v / %+v", legacy, legacy.GraphMetrics)
	}
}

// The --robot-file-* item shapes follow bv's correlation types (BeadReference,
// FileHotspot, CoChangeEntry); the envelopes were captured from bv v0.25.2.
func TestFileResponsesDecodeBVShapes(t *testing.T) {
	var beads FileBeadsResponse
	if err := json.Unmarshal([]byte(`{"file_path":"app.go","total_beads":1,"open_beads":[{"bead_id":"sg-a","title":"Fix login","status":"open","commit_shas":["abc123"],"last_touch":"2026-10-06T20:00:00Z","total_changes":4}],"closed_beads":[]}`), &beads); err != nil {
		t.Fatalf("decode file beads: %v", err)
	}
	if beads.FilePath != "app.go" || beads.TotalBeads != 1 || len(beads.OpenBeads) != 1 || beads.OpenBeads[0].BeadID != "sg-a" || beads.OpenBeads[0].TotalChanges != 4 {
		t.Fatalf("file beads = %+v", beads)
	}

	var hotspots FileHotspotsResponse
	if err := json.Unmarshal([]byte(`{"hotspots":[{"file_path":"app.go","total_beads":3,"open_beads":1,"closed_beads":2}],"stats":{"total_files":1,"total_bead_links":3,"files_with_multiple_beads":1}}`), &hotspots); err != nil {
		t.Fatalf("decode hotspots: %v", err)
	}
	if len(hotspots.Hotspots) != 1 || hotspots.Hotspots[0].FilePath != "app.go" || hotspots.Hotspots[0].TotalBeads != 3 || hotspots.Stats["total_files"] != float64(1) {
		t.Fatalf("hotspots = %+v", hotspots)
	}

	var relations FileRelationsResponse
	if err := json.Unmarshal([]byte(`{"file_path":"app.go","total_commits":3,"threshold":0.5,"related_files":[{"file_path":"util.go","co_change_count":2,"total_commits":3,"correlation":0.67,"sample_commits":["abc123"]}]}`), &relations); err != nil {
		t.Fatalf("decode relations: %v", err)
	}
	if relations.FilePath != "app.go" || len(relations.RelatedFiles) != 1 || relations.RelatedFiles[0].FilePath != "util.go" || relations.RelatedFiles[0].CoChangeCount != 2 {
		t.Fatalf("relations = %+v", relations)
	}
}

// br blocked --json answers in an {"issues":[...]} envelope; decoding it as a
// bare array left the recovery prompt's and --robot-health's Top Blockers
// empty. Run the installed br on a workspace with one blocked issue.
func TestGetDependencyContextReadsBrBlockedEnvelope(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("br"); err != nil {
		t.Skip("br not installed")
	}
	dir := t.TempDir()
	brRun := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("br", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("br %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	brRun("init", "--prefix", "zz")
	newID := func(title string) string {
		var created struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(brRun("create", "--title", title, "--type", "task", "--json")), &created); err != nil || created.ID == "" {
			t.Fatalf("br create %q: %v", title, err)
		}
		return created.ID
	}
	blocked, blocker := newID("Blocked work"), newID("Blocker")
	brRun("dep", "add", blocked, blocker)

	ctx, err := GetDependencyContext(dir, 5)
	if err != nil {
		t.Fatalf("GetDependencyContext: %v", err)
	}
	if len(ctx.TopBlockers) != 1 || ctx.TopBlockers[0].ID != blocked || len(ctx.TopBlockers[0].BlockedBy) != 1 || ctx.TopBlockers[0].BlockedBy[0] != blocker {
		t.Fatalf("TopBlockers = %+v, want %s blocked by %s", ctx.TopBlockers, blocked, blocker)
	}
}

// The live contract: run the installed bv on a workspace with a cycle.
func TestGetInsightsReadsCyclesFromInstalledBV(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("bv"); err != nil {
		t.Skip("bv not installed")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	issue := func(id, blockedBy string) string {
		return `{"id":"` + id + `","title":"` + id + `","status":"open","priority":2,"issue_type":"task","created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-01T00:00:00Z","dependencies":[{"issue_id":"` + id + `","depends_on_id":"` + blockedBy + `","type":"blocks","created_at":"2026-10-01T00:00:00Z"}]}`
	}
	jsonl := issue("cy-a", "cy-b") + "\n" + issue("cy-b", "cy-a") + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".beads", "issues.jsonl"), []byte(jsonl), 0o644); err != nil {
		t.Fatal(err)
	}
	resp, err := GetInsights(dir)
	if err != nil {
		t.Fatalf("GetInsights with a cyclic graph: %v", err)
	}
	if len(resp.Cycles) == 0 {
		t.Fatalf("Cycles = %v, want the cy-a/cy-b cycle", resp.Cycles)
	}
}

func TestBVClientBuildInsightsFromTriage(t *testing.T) {
	t.Parallel()

	client := NewBVClient()

	triage := &TriageResponse{
		Triage: TriageData{
			QuickRef: TriageQuickRef{
				ActionableCount: 7,
			},
			ProjectHealth: &ProjectHealth{
				StatusDistribution: map[string]int{"total": 123},
				GraphMetrics: &GraphMetrics{
					CycleCount: 2,
				},
			},
			Recommendations: []TriageRecommendation{
				{ID: "bd-a", Breakdown: &ScoreBreakdown{Betweenness: 0.06}},
				{ID: "bd-b", Breakdown: &ScoreBreakdown{Betweenness: 0.05}}, // threshold is strictly greater
				{ID: "bd-c", Breakdown: &ScoreBreakdown{Betweenness: 0.2}},
				{ID: "bd-d"},
			},
		},
	}

	insights := client.buildInsightsFromTriage(triage)

	if insights.TotalCount != 123 {
		t.Fatalf("TotalCount = %d, want 123", insights.TotalCount)
	}
	if insights.ReadyCount != 7 {
		t.Fatalf("ReadyCount = %d, want 7", insights.ReadyCount)
	}
	if len(insights.Cycles) != 2 {
		t.Fatalf("Cycles len = %d, want 2", len(insights.Cycles))
	}

	// Should include only bd-a and bd-c (strictly > 0.05)
	if len(insights.Bottlenecks) != 2 {
		t.Fatalf("Bottlenecks len = %d, want 2", len(insights.Bottlenecks))
	}
	if insights.Bottlenecks[0].ID != "bd-a" || insights.Bottlenecks[0].Betweenness != 0.06 {
		t.Fatalf("Bottlenecks[0] = %#v, want id=bd-a betweenness=0.06", insights.Bottlenecks[0])
	}
	if insights.Bottlenecks[1].ID != "bd-c" || insights.Bottlenecks[1].Betweenness != 0.2 {
		t.Fatalf("Bottlenecks[1] = %#v, want id=bd-c betweenness=0.2", insights.Bottlenecks[1])
	}
}

func TestBVClientBuildInsightsFromTriage_NoHealthMetrics(t *testing.T) {
	t.Parallel()

	client := NewBVClient()
	triage := &TriageResponse{
		Triage: TriageData{
			QuickRef: TriageQuickRef{
				ActionableCount: 3,
			},
			ProjectHealth: nil,
			Recommendations: []TriageRecommendation{
				{ID: "bd-x", Breakdown: &ScoreBreakdown{Betweenness: 0.01}},
			},
		},
	}

	insights := client.buildInsightsFromTriage(triage)

	if insights.TotalCount != 0 {
		t.Fatalf("TotalCount = %d, want 0 when no project health", insights.TotalCount)
	}
	if insights.ReadyCount != 3 {
		t.Fatalf("ReadyCount = %d, want 3", insights.ReadyCount)
	}
	if len(insights.Cycles) != 0 {
		t.Fatalf("Cycles len = %d, want 0", len(insights.Cycles))
	}
	if len(insights.Bottlenecks) != 0 {
		t.Fatalf("Bottlenecks len = %d, want 0", len(insights.Bottlenecks))
	}
}

func TestBVClientBuildInsightsFromTriage_NoRecommendations(t *testing.T) {
	t.Parallel()

	client := NewBVClient()
	triage := &TriageResponse{
		Triage: TriageData{
			QuickRef: TriageQuickRef{
				ActionableCount: 2,
			},
			ProjectHealth: &ProjectHealth{
				StatusDistribution: map[string]int{"total": 42},
				GraphMetrics:       &GraphMetrics{CycleCount: 0},
			},
			Recommendations: nil,
		},
	}

	insights := client.buildInsightsFromTriage(triage)

	if insights.TotalCount != 42 {
		t.Fatalf("TotalCount = %d, want 42", insights.TotalCount)
	}
	if insights.ReadyCount != 2 {
		t.Fatalf("ReadyCount = %d, want 2", insights.ReadyCount)
	}
	if len(insights.Bottlenecks) != 0 {
		t.Fatalf("Bottlenecks len = %d, want 0", len(insights.Bottlenecks))
	}
	if len(insights.Cycles) != 0 {
		t.Fatalf("Cycles len = %d, want 0", len(insights.Cycles))
	}
}
