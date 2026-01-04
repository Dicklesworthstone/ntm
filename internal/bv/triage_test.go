package bv

import (
	"testing"
	"time"
)

func TestGetTriage(t *testing.T) {
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	projectRoot := getProjectRoot()
	if projectRoot == "" {
		t.Skip("No .beads directory found")
	}

	// Clear any existing cache
	InvalidateTriageCache()

	triage, err := GetTriage(projectRoot)
	if err != nil {
		t.Fatalf("GetTriage failed: %v", err)
	}

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

	projectRoot := getProjectRoot()
	if projectRoot == "" {
		t.Skip("No .beads directory found")
	}

	// Clear cache
	InvalidateTriageCache()

	// First call should populate cache
	triage1, err := GetTriage(projectRoot)
	if err != nil {
		t.Fatalf("First GetTriage failed: %v", err)
	}

	// Verify cache is valid
	if !IsCacheValid() {
		t.Error("Cache should be valid after GetTriage")
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

	// Cache age should be minimal
	age := GetCacheAge()
	if age > time.Second {
		t.Errorf("Cache age too high: %v", age)
	}
}

func TestInvalidateTriageCache(t *testing.T) {
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	projectRoot := getProjectRoot()
	if projectRoot == "" {
		t.Skip("No .beads directory found")
	}

	// Populate cache
	_, err := GetTriage(projectRoot)
	if err != nil {
		t.Fatalf("GetTriage failed: %v", err)
	}

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

	projectRoot := getProjectRoot()
	if projectRoot == "" {
		t.Skip("No .beads directory found")
	}
	quickRef, err := GetTriageQuickRef(projectRoot)
	if err != nil {
		t.Fatalf("GetTriageQuickRef failed: %v", err)
	}

	if quickRef == nil {
		t.Fatal("GetTriageQuickRef returned nil")
	}

	if quickRef.OpenCount == 0 && quickRef.BlockedCount == 0 && quickRef.InProgressCount == 0 {
		t.Log("All counts are 0 - might be an empty project")
	}
}

func TestGetTriageTopPicks(t *testing.T) {
	if !IsInstalled() {
		t.Skip("bv not installed")
	}

	projectRoot := getProjectRoot()
	if projectRoot == "" {
		t.Skip("No .beads directory found")
	}
	picks, err := GetTriageTopPicks(projectRoot, 3)
	if err != nil {
		t.Fatalf("GetTriageTopPicks failed: %v", err)
	}

	if len(picks) > 3 {
		t.Errorf("Expected at most 3 picks, got %d", len(picks))
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

	projectRoot := getProjectRoot()
	if projectRoot == "" {
		t.Skip("No .beads directory found")
	}
	rec, err := GetNextRecommendation(projectRoot)
	if err != nil {
		t.Fatalf("GetNextRecommendation failed: %v", err)
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

	projectRoot := getProjectRoot()
	if projectRoot == "" {
		t.Skip("No .beads directory found")
	}

	// Clear cache
	InvalidateTriageCache()

	// Get fresh data
	triage, err := GetTriageNoCache(projectRoot)
	if err != nil {
		t.Fatalf("GetTriageNoCache failed: %v", err)
	}

	if triage == nil {
		t.Fatal("GetTriageNoCache returned nil")
	}

	// Cache should be populated now
	if !IsCacheValid() {
		t.Error("Cache should be valid after GetTriageNoCache")
	}
}
