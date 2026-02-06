package cass

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// FilterResults — comprehensive tests for the scoring/filtering pipeline
// ---------------------------------------------------------------------------

func TestFilterResults_SingleHitNoDate(t *testing.T) {
	t.Parallel()

	hits := []CASSHit{{SourcePath: "sessions/no-date.jsonl", Content: "context"}}
	config := FilterConfig{MaxItems: 10}

	result := FilterResults(hits, config)

	if result.OriginalCount != 1 {
		t.Errorf("OriginalCount = %d, want 1", result.OriginalCount)
	}
	if result.FilteredCount != 1 {
		t.Errorf("FilteredCount = %d, want 1", result.FilteredCount)
	}
	if len(result.Hits) != 1 {
		t.Fatalf("Hits len = %d, want 1", len(result.Hits))
	}
	// Single hit → BaseScore = 1.0 (len(hits) == 1 branch)
	if result.Hits[0].ScoreDetail.BaseScore != 1.0 {
		t.Errorf("BaseScore = %v, want 1.0", result.Hits[0].ScoreDetail.BaseScore)
	}
	if result.Hits[0].ComputedScore != 1.0 {
		t.Errorf("ComputedScore = %v, want 1.0", result.Hits[0].ComputedScore)
	}
}

func TestFilterResults_MultipleHitsPositionScoring(t *testing.T) {
	t.Parallel()

	hits := []CASSHit{
		{SourcePath: "sessions/first.jsonl"},
		{SourcePath: "sessions/second.jsonl"},
		{SourcePath: "sessions/third.jsonl"},
	}
	config := FilterConfig{MaxItems: 10}

	result := FilterResults(hits, config)

	if result.FilteredCount != 3 {
		t.Fatalf("FilteredCount = %d, want 3", result.FilteredCount)
	}

	// Position-based: score[i] = 1.0 - (i * 0.5 / (n-1))
	// i=0: 1.0, i=1: 0.75, i=2: 0.5
	// After sorting: [1.0, 0.75, 0.5]
	wantScores := []float64{1.0, 0.75, 0.5}
	for i, want := range wantScores {
		got := result.Hits[i].ComputedScore
		if diff := got - want; diff < -0.01 || diff > 0.01 {
			t.Errorf("Hits[%d].ComputedScore = %v, want %v", i, got, want)
		}
	}
}

func TestFilterResults_ExplicitScoreOverridesPosition(t *testing.T) {
	t.Parallel()

	hits := []CASSHit{
		{SourcePath: "sessions/a.jsonl", Score: 0.3},
		{SourcePath: "sessions/b.jsonl", Score: 0.9},
	}
	config := FilterConfig{MaxItems: 10}

	result := FilterResults(hits, config)

	if result.FilteredCount != 2 {
		t.Fatalf("FilteredCount = %d, want 2", result.FilteredCount)
	}
	// Sorted descending by score, so 0.9 first
	if result.Hits[0].ScoreDetail.BaseScore != 0.9 {
		t.Errorf("first hit BaseScore = %v, want 0.9", result.Hits[0].ScoreDetail.BaseScore)
	}
	if result.Hits[1].ScoreDetail.BaseScore != 0.3 {
		t.Errorf("second hit BaseScore = %v, want 0.3", result.Hits[1].ScoreDetail.BaseScore)
	}
}

func TestFilterResults_PercentageScoreNormalized(t *testing.T) {
	t.Parallel()

	// Score > 1.0 is treated as percentage
	hits := []CASSHit{
		{SourcePath: "sessions/pct.jsonl", Score: 85.0},
	}
	config := FilterConfig{MaxItems: 10}

	result := FilterResults(hits, config)

	if len(result.Hits) != 1 {
		t.Fatalf("Hits len = %d, want 1", len(result.Hits))
	}
	got := result.Hits[0].ScoreDetail.BaseScore
	want := 0.85
	if diff := got - want; diff < -0.01 || diff > 0.01 {
		t.Errorf("BaseScore = %v, want %v (85%% normalized)", got, want)
	}
}

func TestFilterResults_RecencyBoost(t *testing.T) {
	t.Parallel()

	// Create a hit dated yesterday
	yesterday := time.Now().AddDate(0, 0, -1)
	path := fmt.Sprintf("sessions/%d/%02d/%02d/recent.jsonl", yesterday.Year(), yesterday.Month(), yesterday.Day())

	hits := []CASSHit{
		{SourcePath: path, Score: 0.5},
	}
	config := FilterConfig{
		MaxItems:     10,
		MaxAgeDays:   30,
		RecencyBoost: 0.3,
	}

	result := FilterResults(hits, config)

	if len(result.Hits) != 1 {
		t.Fatalf("Hits len = %d, want 1", len(result.Hits))
	}
	bonus := result.Hits[0].ScoreDetail.RecencyBonus
	if bonus <= 0 {
		t.Errorf("RecencyBonus = %v, want > 0 for recent hit", bonus)
	}
	// 1 day ago out of 30 days → recencyFactor ≈ 29/30 ≈ 0.967 → bonus ≈ 0.29
	if bonus < 0.25 || bonus > 0.31 {
		t.Errorf("RecencyBonus = %v, want ~0.29 (1-day-old, 30-day window, 0.3 boost)", bonus)
	}
}

func TestFilterResults_ProjectBonus(t *testing.T) {
	t.Parallel()

	hits := []CASSHit{
		{SourcePath: "sessions/myproject/data.jsonl", Score: 0.5},
		{SourcePath: "sessions/otherproject/data.jsonl", Score: 0.5},
	}
	config := FilterConfig{
		MaxItems:          10,
		PreferSameProject: true,
		CurrentWorkspace:  "/home/user/myproject",
	}

	result := FilterResults(hits, config)

	if result.FilteredCount != 2 {
		t.Fatalf("FilteredCount = %d, want 2", result.FilteredCount)
	}

	// First hit (sorted by score) should be the one with project bonus
	found := false
	for _, h := range result.Hits {
		if h.ScoreDetail.ProjectBonus == 0.15 {
			found = true
			if !strings.Contains(h.SourcePath, "myproject") {
				t.Errorf("project bonus applied to wrong hit: %s", h.SourcePath)
			}
		}
	}
	if !found {
		t.Error("expected one hit to have ProjectBonus = 0.15")
	}
}

func TestFilterResults_AgeFiltering(t *testing.T) {
	t.Parallel()

	// One recent hit, one old hit
	recent := time.Now().AddDate(0, 0, -5)
	old := time.Now().AddDate(0, 0, -60)

	recentPath := fmt.Sprintf("sessions/%d/%02d/%02d/recent.jsonl", recent.Year(), recent.Month(), recent.Day())
	oldPath := fmt.Sprintf("sessions/%d/%02d/%02d/old.jsonl", old.Year(), old.Month(), old.Day())

	hits := []CASSHit{
		{SourcePath: recentPath},
		{SourcePath: oldPath},
	}
	config := FilterConfig{
		MaxItems:   10,
		MaxAgeDays: 30,
	}

	result := FilterResults(hits, config)

	if result.RemovedByAge != 1 {
		t.Errorf("RemovedByAge = %d, want 1", result.RemovedByAge)
	}
	if result.FilteredCount != 1 {
		t.Errorf("FilteredCount = %d, want 1", result.FilteredCount)
	}
}

func TestFilterResults_MinRelevanceFiltering(t *testing.T) {
	t.Parallel()

	hits := []CASSHit{
		{SourcePath: "sessions/high.jsonl", Score: 0.8},
		{SourcePath: "sessions/low.jsonl", Score: 0.2},
	}
	config := FilterConfig{
		MaxItems:     10,
		MinRelevance: 0.5,
	}

	result := FilterResults(hits, config)

	if result.RemovedByScore != 1 {
		t.Errorf("RemovedByScore = %d, want 1", result.RemovedByScore)
	}
	if result.FilteredCount != 1 {
		t.Errorf("FilteredCount = %d, want 1", result.FilteredCount)
	}
	if result.Hits[0].ScoreDetail.BaseScore != 0.8 {
		t.Errorf("remaining hit BaseScore = %v, want 0.8", result.Hits[0].ScoreDetail.BaseScore)
	}
}

func TestFilterResults_MaxItemsTruncation(t *testing.T) {
	t.Parallel()

	hits := make([]CASSHit, 10)
	for i := range hits {
		hits[i] = CASSHit{SourcePath: fmt.Sprintf("sessions/hit%d.jsonl", i), Score: float64(10-i) / 10.0}
	}
	config := FilterConfig{MaxItems: 3}

	result := FilterResults(hits, config)

	if result.OriginalCount != 10 {
		t.Errorf("OriginalCount = %d, want 10", result.OriginalCount)
	}
	if result.FilteredCount != 3 {
		t.Errorf("FilteredCount = %d, want 3", result.FilteredCount)
	}
	// Top 3 scores should be 1.0, 0.9, 0.8
	for i, want := range []float64{1.0, 0.9, 0.8} {
		got := result.Hits[i].ComputedScore
		if diff := got - want; diff < -0.01 || diff > 0.01 {
			t.Errorf("Hits[%d].ComputedScore = %v, want %v", i, got, want)
		}
	}
}

func TestFilterResults_ScoreClampedToRange(t *testing.T) {
	t.Parallel()

	// A hit with high explicit score + project bonus + recency bonus
	// should clamp to 1.0
	yesterday := time.Now().AddDate(0, 0, -1)
	path := fmt.Sprintf("sessions/myproject/%d/%02d/%02d/data.jsonl",
		yesterday.Year(), yesterday.Month(), yesterday.Day())

	hits := []CASSHit{
		{SourcePath: path, Score: 0.95},
	}
	config := FilterConfig{
		MaxItems:          10,
		MaxAgeDays:        30,
		RecencyBoost:      0.5,
		PreferSameProject: true,
		CurrentWorkspace:  "/home/user/myproject",
	}

	result := FilterResults(hits, config)

	if len(result.Hits) != 1 {
		t.Fatalf("Hits len = %d, want 1", len(result.Hits))
	}
	// 0.95 base + ~0.48 recency + 0.15 project = ~1.58 → clamped to 1.0
	if result.Hits[0].ComputedScore != 1.0 {
		t.Errorf("ComputedScore = %v, want 1.0 (clamped)", result.Hits[0].ComputedScore)
	}
}

func TestFilterResults_ZeroMaxAgeDaysSkipsAgeFilter(t *testing.T) {
	t.Parallel()

	// Old date but MaxAgeDays=0 → should not be filtered
	hits := []CASSHit{
		{SourcePath: "sessions/2020/01/01/ancient.jsonl"},
	}
	config := FilterConfig{
		MaxItems:   10,
		MaxAgeDays: 0, // Disabled
	}

	result := FilterResults(hits, config)

	if result.RemovedByAge != 0 {
		t.Errorf("RemovedByAge = %d, want 0 (age filtering disabled)", result.RemovedByAge)
	}
	if result.FilteredCount != 1 {
		t.Errorf("FilteredCount = %d, want 1", result.FilteredCount)
	}
}

func TestFilterResults_PromptTopicsPassthrough(t *testing.T) {
	t.Parallel()

	topics := []Topic{TopicAuth, TopicAPI}
	hits := []CASSHit{{SourcePath: "sessions/a.jsonl"}}
	config := FilterConfig{
		MaxItems:     10,
		PromptTopics: topics,
	}

	result := FilterResults(hits, config)

	if len(result.PromptTopics) != 2 {
		t.Errorf("PromptTopics len = %d, want 2", len(result.PromptTopics))
	}
}

func TestFilterResults_TopicMultiplierDefault(t *testing.T) {
	t.Parallel()

	hits := []CASSHit{{SourcePath: "sessions/a.jsonl", Score: 0.5}}
	config := FilterConfig{MaxItems: 10}

	result := FilterResults(hits, config)

	if len(result.Hits) != 1 {
		t.Fatalf("Hits len = %d, want 1", len(result.Hits))
	}
	if result.Hits[0].ScoreDetail.TopicMultiplier != 1.0 {
		t.Errorf("TopicMultiplier = %v, want 1.0 (default)", result.Hits[0].ScoreDetail.TopicMultiplier)
	}
}

func TestFilterResults_NoDateNoRecencyBonus(t *testing.T) {
	t.Parallel()

	hits := []CASSHit{{SourcePath: "sessions/no-date.jsonl", Score: 0.5}}
	config := FilterConfig{
		MaxItems:     10,
		MaxAgeDays:   30,
		RecencyBoost: 0.5,
	}

	result := FilterResults(hits, config)

	if len(result.Hits) != 1 {
		t.Fatalf("Hits len = %d, want 1", len(result.Hits))
	}
	if result.Hits[0].ScoreDetail.RecencyBonus != 0 {
		t.Errorf("RecencyBonus = %v, want 0 (no date in path)", result.Hits[0].ScoreDetail.RecencyBonus)
	}
}

func TestFilterResults_ZeroMaxItemsNoTruncation(t *testing.T) {
	t.Parallel()

	hits := make([]CASSHit, 5)
	for i := range hits {
		hits[i] = CASSHit{SourcePath: fmt.Sprintf("sessions/hit%d.jsonl", i)}
	}
	config := FilterConfig{MaxItems: 0} // Disabled

	result := FilterResults(hits, config)

	if result.FilteredCount != 5 {
		t.Errorf("FilteredCount = %d, want 5 (no truncation)", result.FilteredCount)
	}
}

// ---------------------------------------------------------------------------
// countInjectedItems — structured format branch
// ---------------------------------------------------------------------------

func TestCountInjectedItems_Structured(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ctx  string
		want int
	}{
		{"three items", "1. Session: A\n2. Session: B\n3. Session: C\n", 3},
		{"one item", "1. Session: X\n", 1},
		{"no items", "no sessions here", 0},
		{"empty", "", 0},
		{"non-sequential numbering", "1. Session: A\n5. Session: B\n", 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := countInjectedItems(tc.ctx, FormatStructured)
			if got != tc.want {
				t.Errorf("countInjectedItems(%q, structured) = %d, want %d", tc.ctx, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// formatAge — today / yesterday / N days ago
// ---------------------------------------------------------------------------

func TestFormatAge(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	today := now
	yesterday := now.AddDate(0, 0, -1)
	threeDaysAgo := now.AddDate(0, 0, -3)

	tests := []struct {
		name string
		path string
		want string
	}{
		{
			"today",
			fmt.Sprintf("sessions/%d/%02d/%02d/session.jsonl", today.Year(), today.Month(), today.Day()),
			"today",
		},
		{
			"yesterday",
			fmt.Sprintf("sessions/%d/%02d/%02d/session.jsonl", yesterday.Year(), yesterday.Month(), yesterday.Day()),
			"yesterday",
		},
		{
			"three_days_ago",
			fmt.Sprintf("sessions/%d/%02d/%02d/session.jsonl", threeDaysAgo.Year(), threeDaysAgo.Month(), threeDaysAgo.Day()),
			"3 days ago",
		},
		{
			"no_date",
			"sessions/no-date.jsonl",
			"",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := formatAge(tc.path)
			if got != tc.want {
				t.Errorf("formatAge(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}
