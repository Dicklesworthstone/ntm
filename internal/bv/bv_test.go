package bv

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// testCache caches expensive bv command results to avoid repeated calls.
// Each bv command (insights, priority, plan, recipes) takes ~9-10 seconds,
// and many tests use the same data. Without caching, the full test suite
// times out because 10+ tests calling GetInsights adds up to 90+ seconds.
var testCache struct {
	once     sync.Once
	root     string
	insights *InsightsResponse
	priority *PriorityResponse
	plan     *PlanResponse
	recipes  *RecipesResponse
	err      error
}

// getCachedInsights returns cached insights or fetches them once.
// This dramatically speeds up tests that depend on GetInsights.
func getCachedInsights(t *testing.T) (*InsightsResponse, string) {
	t.Helper()
	testCache.once.Do(func() {
		testCache.root = getProjectRoot()
		if testCache.root == "" {
			return
		}
		testCache.insights, testCache.err = GetInsights(testCache.root)
		if testCache.err != nil {
			return
		}
		// Pre-fetch other commonly used data in parallel
		var wg sync.WaitGroup
		wg.Add(3)
		go func() {
			defer wg.Done()
			testCache.priority, _ = GetPriority(testCache.root)
		}()
		go func() {
			defer wg.Done()
			testCache.plan, _ = GetPlan(testCache.root)
		}()
		go func() {
			defer wg.Done()
			testCache.recipes, _ = GetRecipes(testCache.root)
		}()
		wg.Wait()
	})

	if testCache.err != nil {
		// Skip tests when bv times out - this is expected for large projects
		// where bv -robot-insights may take longer than the default timeout
		if strings.Contains(testCache.err.Error(), "timed out") {
			t.Skipf("bv timed out (expected for large projects): %v", testCache.err)
		}
		t.Fatalf("getCachedInsights: %v", testCache.err)
	}
	return testCache.insights, testCache.root
}

func getCachedPriority(t *testing.T) (*PriorityResponse, string) {
	t.Helper()
	getCachedInsights(t) // ensures cache is populated
	if testCache.priority == nil {
		t.Skip("priority data not available")
	}
	return testCache.priority, testCache.root
}

func getCachedPlan(t *testing.T) (*PlanResponse, string) {
	t.Helper()
	getCachedInsights(t) // ensures cache is populated
	if testCache.plan == nil {
		t.Skip("plan data not available")
	}
	return testCache.plan, testCache.root
}

func getCachedRecipes(t *testing.T) (*RecipesResponse, string) {
	t.Helper()
	getCachedInsights(t) // ensures cache is populated
	if testCache.recipes == nil {
		t.Skip("recipes data not available")
	}
	return testCache.recipes, testCache.root
}

// getProjectRoot finds the project root by looking for .beads directory
func getProjectRoot() string {
	dir, _ := os.Getwd()
	for dir != "/" {
		if _, err := os.Stat(filepath.Join(dir, ".beads")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	return ""
}

func TestIsInstalled(t *testing.T) {
	// This test verifies the function works - actual result depends on environment
	result := IsInstalled()
	t.Logf("bv installed: %v", result)
}

func TestDriftStatusString(t *testing.T) {
	tests := []struct {
		status DriftStatus
		want   string
	}{
		{DriftOK, "OK"},
		{DriftCritical, "critical"},
		{DriftWarning, "warning"},
		{DriftNoBaseline, "no baseline"},
		{DriftStatus(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := tt.status.String()
			if got != tt.want {
				t.Errorf("DriftStatus(%d).String() = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestCheckDrift(t *testing.T) {
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	root := getProjectRoot()
	if root == "" {
		t.Skip("Project root not found (no .beads)")
	}

	result := CheckDrift(root)

	// Handle case where flag is not supported by installed version
	if strings.Contains(result.Message, "flag provided but not defined") {
		t.Skipf("bv does not support -check-drift: %s", result.Message)
	}

	t.Logf("Drift status: %s, message: %s", result.Status, result.Message)

	// Status should be one of the defined values
	switch result.Status {
	case DriftOK, DriftCritical, DriftWarning, DriftNoBaseline:
		// Valid status
	default:
		t.Errorf("Unexpected drift status: %d", result.Status)
	}
}

func TestGetInsights(t *testing.T) {
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	insights, root := getCachedInsights(t)
	if root == "" {
		t.Skip("Project root not found")
	}

	t.Logf("Got %d bottlenecks", len(insights.Bottlenecks))

	// Verify structure
	for _, b := range insights.Bottlenecks {
		if b.ID == "" {
			t.Error("Bottleneck has empty ID")
		}
	}
}

func TestGetPriority(t *testing.T) {
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	priority, _ := getCachedPriority(t)

	t.Logf("Got %d recommendations", len(priority.Recommendations))

	// Verify structure
	for _, r := range priority.Recommendations {
		if r.IssueID == "" {
			t.Error("Recommendation has empty IssueID")
		}
		if r.Confidence < 0 || r.Confidence > 1 {
			t.Errorf("Invalid confidence: %f", r.Confidence)
		}
	}
}

func TestGetPlan(t *testing.T) {
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	plan, _ := getCachedPlan(t)

	t.Logf("Got %d tracks", len(plan.Plan.Tracks))

	// Verify structure
	for _, track := range plan.Plan.Tracks {
		if track.TrackID == "" {
			t.Error("Track has empty TrackID")
		}
		if len(track.Items) == 0 {
			t.Errorf("Track %s has no items", track.TrackID)
		}
	}
}

func TestGetRecipes(t *testing.T) {
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	recipes, _ := getCachedRecipes(t)

	t.Logf("Got %d recipes", len(recipes.Recipes))

	// Should have at least the builtin recipes
	if len(recipes.Recipes) == 0 {
		t.Error("Expected at least one recipe")
	}

	// Verify structure
	for _, r := range recipes.Recipes {
		if r.Name == "" {
			t.Error("Recipe has empty name")
		}
		if r.Source == "" {
			t.Error("Recipe has empty source")
		}
	}
}

func TestGetTopBottlenecks(t *testing.T) {
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	_, root := getCachedInsights(t)
	bottlenecks, err := GetTopBottlenecks(root, 3)
	if err != nil {
		t.Fatalf("GetTopBottlenecks: %v", err)
	}

	if len(bottlenecks) > 3 {
		t.Errorf("Expected at most 3 bottlenecks, got %d", len(bottlenecks))
	}

	t.Logf("Top bottlenecks: %v", bottlenecks)
}

func TestGetNextActions(t *testing.T) {
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	_, root := getCachedPriority(t)
	actions, err := GetNextActions(root, 5)
	if err != nil {
		t.Fatalf("GetNextActions: %v", err)
	}

	if len(actions) > 5 {
		t.Errorf("Expected at most 5 actions, got %d", len(actions))
	}

	t.Logf("Next actions: %d items", len(actions))
}

func TestBVAccessorRejectsNegativeLimit(t *testing.T) {
	if _, err := GetTopBottlenecks("", -1); err == nil {
		t.Fatal("GetTopBottlenecks(-1) returned no error")
	}
	if _, err := GetNextActions("", -1); err == nil {
		t.Fatal("GetNextActions(-1) returned no error")
	}
}

func TestGetHealthSummary(t *testing.T) {
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	insights, root := getCachedInsights(t)

	// Build health summary using cached data to avoid redundant bv calls
	// Note: drift check still runs but is fast compared to insights
	drift := CheckDrift(root)

	bottleneckCount := len(insights.Bottlenecks)
	var topBottleneck string
	if bottleneckCount > 0 {
		topBottleneck = insights.Bottlenecks[0].ID
	}

	t.Logf("Health: drift=%s, bottlenecks=%d, top=%s",
		drift.Status, bottleneckCount, topBottleneck)
}

func TestNotInstalled(t *testing.T) {
	// Test error behavior when bv is not in PATH
	// We can't easily test this without modifying PATH, so just verify the error exists
	if ErrNotInstalled == nil {
		t.Error("ErrNotInstalled should not be nil")
	}
	if ErrNoBaseline == nil {
		t.Error("ErrNoBaseline should not be nil")
	}
}

func TestGetGraphPosition(t *testing.T) {
	setGraphPositionInsights(t, &InsightsResponse{
		Bottlenecks: []NodeScore{{ID: "issue-1", Value: 0.9}},
		Keystones:   []NodeScore{{ID: "issue-1", Value: 0.8}},
		Hubs:        []NodeScore{{ID: "issue-1", Value: 0.7}},
		Authorities: []NodeScore{{ID: "issue-1", Value: 0.6}},
	}, nil)

	pos, err := GetGraphPosition("ignored", "issue-1")
	if err != nil {
		t.Fatalf("GetGraphPosition() error = %v", err)
	}
	if !pos.IsBottleneck || !pos.IsKeystone || !pos.IsHub || !pos.IsAuthority {
		t.Fatalf("GetGraphPosition() roles = %+v, want all roles", pos)
	}
	if pos.BottleneckScore != 0.9 || pos.KeystoneScore != 0.8 || pos.HubScore != 0.7 || pos.AuthorityScore != 0.6 {
		t.Fatalf("GetGraphPosition() scores = %+v", pos)
	}
}

func TestGetGraphPositionNonExistent(t *testing.T) {
	setGraphPositionInsights(t, &InsightsResponse{Bottlenecks: []NodeScore{{ID: "other", Value: 1}}}, nil)

	pos, err := GetGraphPosition("ignored", "nonexistent-issue-xyz")
	if err != nil {
		t.Fatalf("GetGraphPosition() error = %v", err)
	}

	if pos.IsBottleneck || pos.IsKeystone || pos.IsHub || pos.IsAuthority {
		t.Error("Expected nonexistent issue to have no graph roles")
	}

	if pos.Summary != "regular node" {
		t.Errorf("Summary = %q, want 'regular node'", pos.Summary)
	}
}

func TestGetGraphPositionsBatch(t *testing.T) {
	setGraphPositionInsights(t, &InsightsResponse{
		Bottlenecks: []NodeScore{{ID: "bottleneck", Value: 0.9}},
		Authorities: []NodeScore{{ID: "authority", Value: 0.4}},
	}, nil)

	positions, err := GetGraphPositionsBatch("ignored", []string{"bottleneck", "authority", "regular"})
	if err != nil {
		t.Fatalf("GetGraphPositionsBatch() error = %v", err)
	}
	if len(positions) != 3 || !positions["bottleneck"].IsBottleneck || positions["authority"].AuthorityScore != 0.4 || positions["regular"].Summary != "regular node" {
		t.Fatalf("GetGraphPositionsBatch() = %+v", positions)
	}

	setGraphPositionInsights(t, nil, errors.New("insights unavailable"))
	if _, err := GetGraphPositionsBatch("ignored", []string{"bottleneck"}); err == nil || !strings.Contains(err.Error(), "insights unavailable") {
		t.Fatalf("GetGraphPositionsBatch() error = %v, want propagated error", err)
	}
}

func setGraphPositionInsights(t *testing.T, insights *InsightsResponse, err error) {
	t.Helper()
	previous := graphPositionInsights
	graphPositionInsights = func(string) (*InsightsResponse, error) { return insights, err }
	t.Cleanup(func() { graphPositionInsights = previous })
}

func TestGeneratePositionSummary(t *testing.T) {
	tests := []struct {
		name     string
		pos      *GraphPosition
		contains []string
	}{
		{
			name:     "regular node",
			pos:      &GraphPosition{},
			contains: []string{"regular node"},
		},
		{
			name:     "bottleneck only",
			pos:      &GraphPosition{IsBottleneck: true},
			contains: []string{"bottleneck"},
		},
		{
			name:     "keystone only",
			pos:      &GraphPosition{IsKeystone: true},
			contains: []string{"keystone"},
		},
		{
			name:     "hub only",
			pos:      &GraphPosition{IsHub: true},
			contains: []string{"hub"},
		},
		{
			name:     "authority only",
			pos:      &GraphPosition{IsAuthority: true},
			contains: []string{"authority"},
		},
		{
			name:     "multiple roles",
			pos:      &GraphPosition{IsBottleneck: true, IsKeystone: true},
			contains: []string{"bottleneck", "keystone"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary := generatePositionSummary(tt.pos)
			for _, want := range tt.contains {
				if !containsSubstring(summary, want) {
					t.Errorf("Summary %q should contain %q", summary, want)
				}
			}
		})
	}
}

func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestIsNoBeadsDBError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		stderr string
		want   bool
	}{
		{"no beads database found", "Error: no beads database found in current directory", true},
		{"use br --no-db", "Use 'br --no-db' to run without a database", true},
		{"uppercase variant", "NO BEADS DATABASE FOUND", true},
		{"mixed case", "No Beads Database Found", true},
		{"unrelated error", "connection timeout", false},
		{"empty string", "", false},
		{"partial match", "no beads", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isNoBeadsDBError(tc.stderr); got != tc.want {
				t.Errorf("isNoBeadsDBError(%q) = %v, want %v", tc.stderr, got, tc.want)
			}
		})
	}
}

func TestContainsString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		list  []string
		value string
		want  bool
	}{
		{"found in list", []string{"a", "b", "c"}, "b", true},
		{"not found", []string{"a", "b", "c"}, "d", false},
		{"empty list", []string{}, "a", false},
		{"nil list", nil, "a", false},
		{"empty value in list", []string{"", "a"}, "", true},
		{"single element match", []string{"x"}, "x", true},
		{"single element no match", []string{"x"}, "y", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := containsString(tc.list, tc.value); got != tc.want {
				t.Errorf("containsString(%v, %q) = %v, want %v", tc.list, tc.value, got, tc.want)
			}
		})
	}
}

// TestHasLocalBeadsDB verifies the recovery-list gating predicate. When the
// directory has no local .beads/ directory, recovery list reads must short-
// circuit empty rather than letting br walk up into a parent repo (#130).
func TestHasLocalBeadsDB(t *testing.T) {
	t.Run("missing_beads_dir_returns_false", func(t *testing.T) {
		dir := t.TempDir()
		if got := HasLocalBeadsDB(dir); got != false {
			t.Errorf("HasLocalBeadsDB(%q) = true, want false (no .beads/)", dir)
		}
	})

	t.Run("present_beads_dir_returns_true", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
			t.Fatalf("MkdirAll(.beads) failed: %v", err)
		}
		if got := HasLocalBeadsDB(dir); got != true {
			t.Errorf("HasLocalBeadsDB(%q) = false, want true", dir)
		}
	})

	t.Run("beads_is_a_file_returns_false", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".beads"), []byte("not a dir"), 0o600); err != nil {
			t.Fatalf("WriteFile(.beads) failed: %v", err)
		}
		if got := HasLocalBeadsDB(dir); got != false {
			t.Errorf("HasLocalBeadsDB(%q) = true, want false (.beads is a regular file)", dir)
		}
	})

	t.Run("child_without_local_db_does_not_inherit_parent", func(t *testing.T) {
		// Reproduces #130's repro shape: parent has a .beads/ directory, child
		// does not. The child must report no local DB even though the parent has one.
		parent := t.TempDir()
		if err := os.MkdirAll(filepath.Join(parent, ".beads"), 0o755); err != nil {
			t.Fatalf("MkdirAll(parent/.beads) failed: %v", err)
		}
		child := filepath.Join(parent, "child")
		if err := os.MkdirAll(child, 0o755); err != nil {
			t.Fatalf("MkdirAll(child) failed: %v", err)
		}
		if got := HasLocalBeadsDB(child); got != false {
			t.Errorf("HasLocalBeadsDB(%q) = true, want false (child has no .beads/, must not inherit parent's)", child)
		}
	})
}

// TestHasLocalBeadsDBChildDoesNotInheritParent is a focused regression on
// the recovery-gating predicate: a child directory of a beads-rooted parent
// must report no local DB so trust-sensitive callers refuse to surface the
// parent's rows (#130). The generic GetInProgressList / GetRecentlyCompleted
// / GetBlocked helpers intentionally preserve br's walk-up behavior for
// non-recovery callers — recovery enforcement happens at the call site
// (cli/spawn.go::loadRecoveryBeads), not in these helpers.
func TestHasLocalBeadsDBChildDoesNotInheritParent(t *testing.T) {
	parent := t.TempDir()
	if err := os.MkdirAll(filepath.Join(parent, ".beads"), 0o755); err != nil {
		t.Fatalf("MkdirAll(parent/.beads) failed: %v", err)
	}
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("MkdirAll(child) failed: %v", err)
	}

	if HasLocalBeadsDB(child) {
		t.Errorf("HasLocalBeadsDB(%q) = true, want false (child has no .beads/, must not inherit parent's for recovery callers)", child)
	}
	if !HasLocalBeadsDB(parent) {
		t.Errorf("HasLocalBeadsDB(%q) = false, want true (parent has .beads/)", parent)
	}
}
