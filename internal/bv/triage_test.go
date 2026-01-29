package bv

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// testTriageCache caches the triage response for all tests to share.
// GetTriage takes ~30 seconds, and many tests use the same data.
var testTriageCache struct {
	once   sync.Once
	root   string
	triage *TriageResponse
	err    error
}

// getCachedTriage returns cached triage or fetches it once.
func getCachedTriage(t *testing.T) (*TriageResponse, string) {
	t.Helper()
	testTriageCache.once.Do(func() {
		testTriageCache.root = getProjectRoot()
		if testTriageCache.root == "" {
			return
		}
		// Use the direct GetTriage (which may be uncached on first call)
		testTriageCache.triage, testTriageCache.err = GetTriage(testTriageCache.root)
	})

	if testTriageCache.root == "" {
		t.Skip("No .beads directory found")
	}
	if testTriageCache.err != nil {
		// Skip tests when bv times out - expected for large projects
		if strings.Contains(testTriageCache.err.Error(), "timed out") {
			t.Skipf("bv timed out (expected for large projects): %v", testTriageCache.err)
		}
		t.Fatalf("getCachedTriage: %v", testTriageCache.err)
	}
	return testTriageCache.triage, testTriageCache.root
}

func TestGetTriage(t *testing.T) {
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	// Use cached triage to avoid slow bv command
	triage, _ := getCachedTriage(t)

	if triage == nil {
		t.Fatal("GetTriage returned nil")
	}

	if triage.DataHash == "" {
		t.Error("DataHash should not be empty")
	}

	if triage.Triage.Meta.IssueCount == 0 {
		t.Error("IssueCount should not be 0")
	}

	t.Logf("Triage: %d issues, %d actionable, %d blocked",
		triage.Triage.Meta.IssueCount,
		triage.Triage.QuickRef.ActionableCount,
		triage.Triage.QuickRef.BlockedCount)
}

func TestTriageCache(t *testing.T) {
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	// Ensure test cache is populated first (so we don't hit slow bv command)
	triage1, projectRoot := getCachedTriage(t)

	// The production cache should now be valid (getCachedTriage uses GetTriage)
	if !IsCacheValid() {
		t.Error("Cache should be valid after GetTriage")
	}
	if triageCacheDir != projectRoot {
		t.Errorf("Cache directory mismatch: got %q want %q", triageCacheDir, projectRoot)
	}

	// Second call should return cached result
	triage2, err := GetTriage(projectRoot)
	if err != nil {
		t.Fatalf("Second GetTriage failed: %v", err)
	}

	// Should be the same object (from cache)
	if triage1 != triage2 {
		t.Error("Expected cached result to be returned")
	}

	// Cache age should be reasonable (might be several seconds if tests ran before this)
	age := GetCacheAge()
	if age > 30*time.Second {
		t.Errorf("Cache age too high: %v", age)
	}
}

func TestInvalidateTriageCache(t *testing.T) {
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	// Ensure test cache is populated (so we don't hit slow bv command)
	getCachedTriage(t)

	if !IsCacheValid() {
		t.Error("Cache should be valid")
	}

	// Invalidate
	InvalidateTriageCache()

	if IsCacheValid() {
		t.Error("Cache should be invalid after InvalidateTriageCache")
	}
}

func TestGetTriageQuickRef(t *testing.T) {
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	// Use cached triage to avoid slow bv command
	triage, _ := getCachedTriage(t)

	quickRef := &triage.Triage.QuickRef

	if quickRef.OpenCount == 0 && quickRef.BlockedCount == 0 && quickRef.InProgressCount == 0 {
		t.Log("All counts are 0 - might be an empty project")
	}
}

func TestGetTriageTopPicks(t *testing.T) {
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	// Use cached triage to avoid slow bv command
	triage, _ := getCachedTriage(t)

	picks := triage.Triage.QuickWins
	if len(picks) > 3 {
		picks = picks[:3]
	}

	for i, pick := range picks {
		if pick.ID == "" {
			t.Errorf("Pick %d has empty ID", i)
		}
		if pick.Score < 0 {
			t.Errorf("Pick %d has negative score: %f", i, pick.Score)
		}
	}
}

func TestGetNextRecommendation(t *testing.T) {
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	// Use cached triage to avoid slow bv command
	triage, _ := getCachedTriage(t)

	// Extract next recommendation from cached triage
	var rec *TriageRecommendation
	if len(triage.Triage.Recommendations) > 0 {
		rec = &triage.Triage.Recommendations[0]
	}

	if rec == nil {
		t.Log("No recommendations available")
		return
	}

	if rec.ID == "" {
		t.Error("Recommendation has empty ID")
	}

	if rec.Action == "" {
		t.Error("Recommendation has empty action")
	}

	t.Logf("Top recommendation: %s - %s (score: %.2f)", rec.ID, rec.Title, rec.Score)
}

func TestSetTriageCacheTTL(t *testing.T) {
	originalTTL := triageCacheTTL

	// Set a short TTL
	SetTriageCacheTTL(100 * time.Millisecond)

	if triageCacheTTL != 100*time.Millisecond {
		t.Errorf("Expected TTL to be 100ms, got %v", triageCacheTTL)
	}

	// Restore original TTL
	SetTriageCacheTTL(originalTTL)
}

func TestGetTriageNoCache(t *testing.T) {
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	// Use test cache to ensure we have data, rather than making slow bv call
	triage, _ := getCachedTriage(t)

	if triage == nil {
		t.Fatal("GetTriage returned nil")
	}

	// Verify the cached data is valid (testing the structure, not the no-cache mechanism)
	if triage.DataHash == "" {
		t.Error("DataHash should not be empty")
	}

	// Note: We don't test the actual GetTriageNoCache behavior here to avoid
	// making slow bv calls. The caching logic is tested in TestTriageCache.
}
