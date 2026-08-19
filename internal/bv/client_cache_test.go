package bv

import (
	"testing"
	"time"
)

func TestBVClientCacheBackedMethods(t *testing.T) {
	workspace := t.TempDir()

	client := NewBVClientWithOptions(workspace, 10*time.Minute, 10*time.Second)
	dir, err := client.workDir()
	if err != nil {
		t.Fatalf("workDir: %v", err)
	}

	client.triageCache = &TriageResponse{
		Triage: TriageData{
			Recommendations: []TriageRecommendation{
				{ID: "bd-a", Title: "A", Type: "task", Priority: 1},
				{ID: "bd-b", Title: "B", Type: "task", Priority: 1, BlockedBy: []string{"bd-x"}},
				{ID: "bd-c", Title: "C", Type: "task", Priority: 1},
			},
			QuickWins: []TriageRecommendation{
				{ID: "bd-q1", Title: "Quick 1", Type: "task", Priority: 2},
				{ID: "bd-q2", Title: "Quick 2", Type: "task", Priority: 2},
			},
			BlockersToClear: []BlockerToClear{
				{ID: "bd-blk", Title: "Blocker", UnblocksCount: 2, UnblocksIDs: []string{"bd-u1", "bd-u2"}, Actionable: true},
			},
		},
	}
	client.triageCacheDir = dir
	client.triageCacheAt = time.Now()

	t.Run("GetRecommendations_filters_ready_and_limits", func(t *testing.T) {
		recs, err := client.GetRecommendations(RecommendationOpts{Limit: 2, FilterReady: true})
		if err != nil {
			t.Fatalf("GetRecommendations: %v", err)
		}
		if len(recs) != 2 {
			t.Fatalf("len(recs) = %d, want 2", len(recs))
		}
		if recs[0].ID != "bd-a" || recs[1].ID != "bd-c" {
			t.Fatalf("recs IDs = [%s %s], want [bd-a bd-c]", recs[0].ID, recs[1].ID)
		}
		for _, rec := range recs {
			if !rec.IsActionable {
				t.Fatalf("expected actionable recommendation, got %+v", rec)
			}
		}
	})
}
