package bv

import (
	"os"
	"testing"
	"time"
)

func requireBVIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("NTM_BV_TESTS") == "" {
		t.Skip("bv integration tests disabled, set NTM_BV_TESTS=1 to enable")
	}
}

func TestNewBVClient(t *testing.T) {
	client := NewBVClient()

	if client.CacheTTL != DefaultClientCacheTTL {
		t.Errorf("Expected CacheTTL %v, got %v", DefaultClientCacheTTL, client.CacheTTL)
	}

	if client.Timeout != DefaultClientTimeout {
		t.Errorf("Expected Timeout %v, got %v", DefaultClientTimeout, client.Timeout)
	}

	if client.WorkspacePath != "" {
		t.Errorf("Expected empty WorkspacePath, got %q", client.WorkspacePath)
	}
}

func TestNewBVClientWithOptions(t *testing.T) {
	tests := []struct {
		name          string
		workspacePath string
		cacheTTL      time.Duration
		timeout       time.Duration
		wantCacheTTL  time.Duration
		wantTimeout   time.Duration
	}{
		{
			name:          "custom values",
			workspacePath: "/custom/path",
			cacheTTL:      1 * time.Minute,
			timeout:       5 * time.Second,
			wantCacheTTL:  1 * time.Minute,
			wantTimeout:   5 * time.Second,
		},
		{
			name:          "zero values use defaults",
			workspacePath: "",
			cacheTTL:      0,
			timeout:       0,
			wantCacheTTL:  DefaultClientCacheTTL,
			wantTimeout:   DefaultClientTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewBVClientWithOptions(tt.workspacePath, tt.cacheTTL, tt.timeout)

			if client.WorkspacePath != tt.workspacePath {
				t.Errorf("WorkspacePath = %q, want %q", client.WorkspacePath, tt.workspacePath)
			}
			if client.CacheTTL != tt.wantCacheTTL {
				t.Errorf("CacheTTL = %v, want %v", client.CacheTTL, tt.wantCacheTTL)
			}
			if client.Timeout != tt.wantTimeout {
				t.Errorf("Timeout = %v, want %v", client.Timeout, tt.wantTimeout)
			}
		})
	}
}

func TestBVClientIsAvailable(t *testing.T) {
	client := NewBVClient()
	available := client.IsAvailable()

	// Just test that the method works without panicking
	t.Logf("bv available via client: %v", available)

	// Should match the package-level IsInstalled() at minimum
	if !IsInstalled() && available {
		t.Error("IsAvailable should be false when bv is not installed")
	}
}

func TestBVClientGetRecommendations(t *testing.T) {
	requireBVIntegration(t)
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	projectRoot := getProjectRoot()
	if projectRoot == "" {
		t.Skip("No .beads directory found")
	}

	client := NewBVClientWithOptions(projectRoot, 30*time.Second, 10*time.Second)

	// Test with default options
	recs, err := client.GetRecommendations(RecommendationOpts{})
	if err != nil {
		t.Fatalf("GetRecommendations failed: %v", err)
	}

	if len(recs) > 20 { // Default limit is 20
		t.Errorf("Expected at most 20 recommendations, got %d", len(recs))
	}

	for i, rec := range recs {
		if rec.ID == "" {
			t.Errorf("Recommendation %d has empty ID", i)
		}
		// Verify Recommendation struct fields are populated
		t.Logf("Rec %d: ID=%s, Score=%.2f, PageRank=%.4f, Betweenness=%.4f, Size=%s",
			i, rec.ID, rec.Score, rec.PageRank, rec.Betweenness, rec.EstimatedSize)
	}
}

func TestBVClientGetRecommendationsWithLimit(t *testing.T) {
	requireBVIntegration(t)
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	projectRoot := getProjectRoot()
	if projectRoot == "" {
		t.Skip("No .beads directory found")
	}

	client := NewBVClientWithOptions(projectRoot, 30*time.Second, 10*time.Second)

	recs, err := client.GetRecommendations(RecommendationOpts{Limit: 3})
	if err != nil {
		t.Fatalf("GetRecommendations failed: %v", err)
	}

	if len(recs) > 3 {
		t.Errorf("Expected at most 3 recommendations, got %d", len(recs))
	}
}

func TestBVClientGetRecommendationsFilterReady(t *testing.T) {
	requireBVIntegration(t)
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	projectRoot := getProjectRoot()
	if projectRoot == "" {
		t.Skip("No .beads directory found")
	}

	client := NewBVClientWithOptions(projectRoot, 30*time.Second, 10*time.Second)

	recs, err := client.GetRecommendations(RecommendationOpts{FilterReady: true})
	if err != nil {
		t.Fatalf("GetRecommendations with FilterReady failed: %v", err)
	}

	// All returned recommendations should be actionable
	for i, rec := range recs {
		if !rec.IsActionable {
			t.Errorf("Recommendation %d (%s) is not actionable but FilterReady was true", i, rec.ID)
		}
		if len(rec.BlockedByIDs) > 0 {
			t.Errorf("Recommendation %d (%s) has blockers but FilterReady was true", i, rec.ID)
		}
	}
}

func TestBVClientGetInsights(t *testing.T) {
	requireBVIntegration(t)
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	projectRoot := getProjectRoot()
	if projectRoot == "" {
		t.Skip("No .beads directory found")
	}

	client := NewBVClientWithOptions(projectRoot, 30*time.Second, 10*time.Second)

	insights, err := client.GetInsights()
	if err != nil {
		t.Fatalf("GetInsights failed: %v", err)
	}

	if insights == nil {
		t.Fatal("GetInsights returned nil")
	}

	t.Logf("Insights: ReadyCount=%d, TotalCount=%d, Cycles=%d, Bottlenecks=%d",
		insights.ReadyCount, insights.TotalCount, len(insights.Cycles), len(insights.Bottlenecks))

	// Verify bottleneck structure
	for _, b := range insights.Bottlenecks {
		if b.ID == "" {
			t.Error("Bottleneck has empty ID")
		}
	}
}

func TestBVClientGetQuickWins(t *testing.T) {
	requireBVIntegration(t)
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	projectRoot := getProjectRoot()
	if projectRoot == "" {
		t.Skip("No .beads directory found")
	}

	client := NewBVClientWithOptions(projectRoot, 30*time.Second, 10*time.Second)

	wins, err := client.GetQuickWins(3)
	if err != nil {
		t.Fatalf("GetQuickWins failed: %v", err)
	}

	if len(wins) > 3 {
		t.Errorf("Expected at most 3 quick wins, got %d", len(wins))
	}

	for i, win := range wins {
		t.Logf("Quick win %d: ID=%s, Title=%s", i, win.ID, win.Title)
	}
}

func TestBVClientGetBlockersToClear(t *testing.T) {
	requireBVIntegration(t)
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	projectRoot := getProjectRoot()
	if projectRoot == "" {
		t.Skip("No .beads directory found")
	}

	client := NewBVClientWithOptions(projectRoot, 30*time.Second, 10*time.Second)

	blockers, err := client.GetBlockersToClear(5)
	if err != nil {
		t.Fatalf("GetBlockersToClear failed: %v", err)
	}

	if len(blockers) > 5 {
		t.Errorf("Expected at most 5 blockers, got %d", len(blockers))
	}

	for i, blocker := range blockers {
		t.Logf("Blocker %d: ID=%s, UnblocksCount=%d", i, blocker.ID, blocker.UnblocksCount)
	}
}

func TestBVClientCaching(t *testing.T) {
	requireBVIntegration(t)
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	projectRoot := getProjectRoot()
	if projectRoot == "" {
		t.Skip("No .beads directory found")
	}

	client := NewBVClientWithOptions(projectRoot, 30*time.Second, 10*time.Second)

	// Clear cache first
	client.InvalidateCache()

	// First call should populate cache
	recs1, err := client.GetRecommendations(RecommendationOpts{})
	if err != nil {
		t.Fatalf("First GetRecommendations failed: %v", err)
	}

	// Second call should use cache (we can't directly verify this, but it should not error)
	recs2, err := client.GetRecommendations(RecommendationOpts{})
	if err != nil {
		t.Fatalf("Second GetRecommendations failed: %v", err)
	}

	// Results should be consistent
	if len(recs1) != len(recs2) {
		t.Errorf("Cached results length mismatch: %d vs %d", len(recs1), len(recs2))
	}
}

func TestBVClientInvalidateCache(t *testing.T) {
	client := NewBVClient()

	// This should not panic even without data
	client.InvalidateCache()

	// Verify cache is nil after invalidation
	client.mu.RLock()
	if client.triageCache != nil {
		t.Error("Cache should be nil after InvalidateCache")
	}
	client.mu.RUnlock()
}

func TestRecommendationOptsDefaults(t *testing.T) {
	opts := RecommendationOpts{}

	// Defaults should be applied in GetRecommendations
	if opts.Limit != 0 {
		t.Errorf("Expected default Limit 0 (will be set to 20), got %d", opts.Limit)
	}
	if opts.FilterReady {
		t.Error("Expected FilterReady to be false by default")
	}
	if opts.Strategy != "" {
		t.Errorf("Expected empty Strategy by default, got %q", opts.Strategy)
	}
}

func TestEstimateSize(t *testing.T) {
	client := NewBVClient()

	tests := []struct {
		name     string
		rec      TriageRecommendation
		expected string
	}{
		{
			name:     "epic is large",
			rec:      TriageRecommendation{Type: "epic"},
			expected: "large",
		},
		{
			name: "high betweenness is large",
			rec: TriageRecommendation{
				Type:      "task",
				Breakdown: &ScoreBreakdown{Betweenness: 0.15},
			},
			expected: "large",
		},
		{
			name: "many unblocks is large",
			rec: TriageRecommendation{
				Type:        "task",
				UnblocksIDs: []string{"a", "b", "c", "d"},
				Breakdown:   &ScoreBreakdown{Betweenness: 0.05},
			},
			expected: "large",
		},
		{
			name: "leaf node with low betweenness is small",
			rec: TriageRecommendation{
				Type:        "task",
				UnblocksIDs: []string{},
				Breakdown:   &ScoreBreakdown{Betweenness: 0.01},
			},
			expected: "small",
		},
		{
			name: "average task is medium",
			rec: TriageRecommendation{
				Type:        "task",
				UnblocksIDs: []string{"a"},
				Breakdown:   &ScoreBreakdown{Betweenness: 0.05},
			},
			expected: "medium",
		},
		{
			name: "no breakdown data is medium",
			rec: TriageRecommendation{
				Type: "task",
			},
			expected: "medium",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			size := client.estimateSize(tt.rec)
			if size != tt.expected {
				t.Errorf("estimateSize() = %q, want %q", size, tt.expected)
			}
		})
	}
}

func TestConvertRecommendation(t *testing.T) {
	client := NewBVClient()

	triageRec := TriageRecommendation{
		ID:          "test-123",
		Title:       "Test Task",
		Type:        "task",
		Status:      "open",
		Priority:    1,
		Labels:      []string{"bug", "high-priority"},
		Score:       0.85,
		Breakdown:   &ScoreBreakdown{Pagerank: 0.5, Betweenness: 0.1},
		Action:      "Start working",
		Reasons:     []string{"High impact", "No blockers"},
		UnblocksIDs: []string{"other-1", "other-2"},
		BlockedBy:   []string{},
	}

	rec := client.convertRecommendation(triageRec)

	if rec.ID != triageRec.ID {
		t.Errorf("ID = %q, want %q", rec.ID, triageRec.ID)
	}
	if rec.Title != triageRec.Title {
		t.Errorf("Title = %q, want %q", rec.Title, triageRec.Title)
	}
	if rec.Priority != triageRec.Priority {
		t.Errorf("Priority = %d, want %d", rec.Priority, triageRec.Priority)
	}
	if rec.Score != triageRec.Score {
		t.Errorf("Score = %f, want %f", rec.Score, triageRec.Score)
	}
	if rec.PageRank != triageRec.Breakdown.Pagerank {
		t.Errorf("PageRank = %f, want %f", rec.PageRank, triageRec.Breakdown.Pagerank)
	}
	if rec.Betweenness != triageRec.Breakdown.Betweenness {
		t.Errorf("Betweenness = %f, want %f", rec.Betweenness, triageRec.Breakdown.Betweenness)
	}
	if rec.UnblocksCount != len(triageRec.UnblocksIDs) {
		t.Errorf("UnblocksCount = %d, want %d", rec.UnblocksCount, len(triageRec.UnblocksIDs))
	}
	if !rec.IsActionable {
		t.Error("Expected IsActionable to be true when BlockedBy is empty")
	}
	if len(rec.Tags) != len(triageRec.Labels) {
		t.Errorf("Tags length = %d, want %d", len(rec.Tags), len(triageRec.Labels))
	}
}

func TestConvertRecommendationWithBlockers(t *testing.T) {
	client := NewBVClient()

	triageRec := TriageRecommendation{
		ID:        "blocked-task",
		Title:     "Blocked Task",
		BlockedBy: []string{"blocker-1", "blocker-2"},
	}

	rec := client.convertRecommendation(triageRec)

	if rec.IsActionable {
		t.Error("Expected IsActionable to be false when BlockedBy is not empty")
	}
	if len(rec.BlockedByIDs) != 2 {
		t.Errorf("BlockedByIDs length = %d, want 2", len(rec.BlockedByIDs))
	}
}

// A bead with status=blocked but an empty blocked_by list must still be
// reported as non-actionable: bv --robot-triage is permissive and can surface
// blocked/in_progress/closed beads with no dependency blockers, and the serve
// API + FilterReady gate key off IsActionable.
func TestConvertRecommendationStatusAware(t *testing.T) {
	client := NewBVClient()

	cases := []struct {
		status string
		want   bool
	}{
		{"", true},
		{"open", true},
		{"ready", true},
		{"blocked", false},
		{"in_progress", false},
		{"in-progress", false},
		{"In Progress", false},
		{"closed", false},
		{"resolved", false},
		{"done", false},
		// Unknown statuses are NOT actionable — the allow-list mirrors the
		// assignment classifier's `default` arm, so serve/FilterReady agree
		// with `ntm assign` (a deny-list would wrongly mark these actionable).
		{"review", false},
		{"todo", false},
		{"in_review", false},
	}
	for _, tc := range cases {
		rec := client.convertRecommendation(TriageRecommendation{
			ID:     "t-" + tc.status,
			Status: tc.status,
		})
		if rec.IsActionable != tc.want {
			t.Errorf("status=%q: IsActionable=%v, want %v", tc.status, rec.IsActionable, tc.want)
		}
	}
}

func TestBVClientErrorSentinels(t *testing.T) {
	if ErrTimeout == nil {
		t.Error("ErrTimeout should not be nil")
	}
	if ErrInvalidJSON == nil {
		t.Error("ErrInvalidJSON should not be nil")
	}
}

func TestBVClientConstants(t *testing.T) {
	if DefaultClientCacheTTL != 30*time.Second {
		t.Errorf("DefaultClientCacheTTL = %v, want 30s", DefaultClientCacheTTL)
	}
	if DefaultClientTimeout != 10*time.Second {
		t.Errorf("DefaultClientTimeout = %v, want 10s", DefaultClientTimeout)
	}
}

func TestBuildInsightsFromResponse(t *testing.T) {
	t.Parallel()

	t.Run("empty response", func(t *testing.T) {
		t.Parallel()
		client := NewBVClient()
		resp := &InsightsResponse{}
		insights := client.buildInsightsFromResponse(resp, "")

		if insights == nil {
			t.Fatal("expected non-nil insights")
		}
		if len(insights.Cycles) != 0 {
			t.Errorf("expected 0 cycles, got %d", len(insights.Cycles))
		}
		if len(insights.Bottlenecks) != 0 {
			t.Errorf("expected 0 bottlenecks, got %d", len(insights.Bottlenecks))
		}
	})

	t.Run("with cycles and bottlenecks", func(t *testing.T) {
		t.Parallel()
		client := NewBVClient()
		resp := &InsightsResponse{
			Cycles: []Cycle{
				{Nodes: []string{"auth", "db", "auth"}},
				{Nodes: []string{"ui", "api", "ui"}},
			},
			Bottlenecks: []NodeScore{
				{ID: "db-migration", Value: 0.85},
				{ID: "auth-service", Value: 0.65},
			},
		}
		insights := client.buildInsightsFromResponse(resp, "")

		if insights == nil {
			t.Fatal("expected non-nil insights")
		}
		if len(insights.Cycles) != 2 {
			t.Fatalf("expected 2 cycles, got %d", len(insights.Cycles))
		}
		if len(insights.Cycles[0]) != 3 {
			t.Errorf("expected first cycle to have 3 nodes, got %d", len(insights.Cycles[0]))
		}
		if len(insights.Bottlenecks) != 2 {
			t.Fatalf("expected 2 bottlenecks, got %d", len(insights.Bottlenecks))
		}
		if insights.Bottlenecks[0].ID != "db-migration" {
			t.Errorf("expected first bottleneck ID=db-migration, got %s", insights.Bottlenecks[0].ID)
		}
		if insights.Bottlenecks[0].Betweenness != 0.85 {
			t.Errorf("expected betweenness=0.85, got %f", insights.Bottlenecks[0].Betweenness)
		}
	})
}

func TestBuildInsightsFromTriage(t *testing.T) {
	t.Parallel()

	t.Run("empty triage", func(t *testing.T) {
		t.Parallel()
		client := &BVClient{}
		triage := &TriageResponse{}
		insights := client.buildInsightsFromTriage(triage)

		if insights == nil {
			t.Fatal("expected non-nil insights")
		}
		if insights.ReadyCount != 0 {
			t.Errorf("expected 0 ready count, got %d", insights.ReadyCount)
		}
	})

	t.Run("with project health and recommendations", func(t *testing.T) {
		t.Parallel()
		client := &BVClient{}
		triage := &TriageResponse{
			Triage: TriageData{
				QuickRef: TriageQuickRef{
					ActionableCount: 5,
				},
				Recommendations: []TriageRecommendation{
					{ID: "rec-1", Breakdown: &ScoreBreakdown{Betweenness: 0.1}},
					{ID: "rec-2", Breakdown: &ScoreBreakdown{Betweenness: 0.01}}, // below threshold
					{ID: "rec-3", Breakdown: nil},                                // no breakdown
				},
				ProjectHealth: &ProjectHealth{
					StatusDistribution: map[string]int{"total": 42},
					GraphMetrics:       &GraphMetrics{CycleCount: 3},
				},
			},
		}
		insights := client.buildInsightsFromTriage(triage)

		if insights.ReadyCount != 5 {
			t.Errorf("ReadyCount = %d, want 5", insights.ReadyCount)
		}
		if insights.TotalCount != 42 {
			t.Errorf("TotalCount = %d, want 42", insights.TotalCount)
		}
		if len(insights.Cycles) != 3 {
			t.Errorf("expected 3 cycle slots, got %d", len(insights.Cycles))
		}
		// Only rec-1 has betweenness > 0.05
		if len(insights.Bottlenecks) != 1 {
			t.Fatalf("expected 1 bottleneck, got %d", len(insights.Bottlenecks))
		}
		if insights.Bottlenecks[0].ID != "rec-1" {
			t.Errorf("expected bottleneck ID=rec-1, got %s", insights.Bottlenecks[0].ID)
		}
	})

	t.Run("no graph metrics", func(t *testing.T) {
		t.Parallel()
		client := &BVClient{}
		triage := &TriageResponse{
			Triage: TriageData{
				ProjectHealth: &ProjectHealth{
					StatusDistribution: map[string]int{"total": 10},
				},
			},
		}
		insights := client.buildInsightsFromTriage(triage)

		if insights.TotalCount != 10 {
			t.Errorf("TotalCount = %d, want 10", insights.TotalCount)
		}
		if len(insights.Cycles) != 0 {
			t.Errorf("expected 0 cycles without graph metrics, got %d", len(insights.Cycles))
		}
	})
}
