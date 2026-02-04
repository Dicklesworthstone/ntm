package scoring

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/util"
)

func TestScoreMetrics_ComputeOverall(t *testing.T) {
	tests := []struct {
		name     string
		metrics  ScoreMetrics
		expected float64
	}{
		{
			name: "all metrics set",
			metrics: ScoreMetrics{
				Completion: 1.0,
				Quality:    0.9,
				Efficiency: 0.8,
			},
			expected: 0.91, // 1.0*0.4 + 0.9*0.3 + 0.8*0.3
		},
		{
			name: "only completion",
			metrics: ScoreMetrics{
				Completion: 0.8,
			},
			expected: 0.8, // defaults quality and efficiency to completion
		},
		{
			name: "zero completion",
			metrics: ScoreMetrics{
				Completion: 0,
				Quality:    0.5,
				Efficiency: 0.5,
			},
			expected: 0.3, // 0*0.4 + 0.5*0.3 + 0.5*0.3
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.metrics.ComputeOverall()
			// Allow small floating point tolerance
			if diff := result - tc.expected; diff < -0.01 || diff > 0.01 {
				t.Errorf("ComputeOverall() = %v, want %v", result, tc.expected)
			}
		})
	}
}

func TestTracker_RecordAndQuery(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()
	scorePath := filepath.Join(tmpDir, "scores.jsonl")

	tracker, err := NewTracker(TrackerOptions{
		Path:    scorePath,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("NewTracker() error: %v", err)
	}
	defer tracker.Close()

	// Record some scores
	now := time.Now().UTC()
	scores := []Score{
		{
			Timestamp: now.Add(-2 * time.Hour),
			Session:   "test-session",
			AgentType: "claude",
			TaskType:  "bug_fix",
			Metrics: ScoreMetrics{
				Completion: 1.0,
				Quality:    0.9,
				Efficiency: 0.85,
			},
		},
		{
			Timestamp: now.Add(-1 * time.Hour),
			Session:   "test-session",
			AgentType: "codex",
			TaskType:  "feature",
			Metrics: ScoreMetrics{
				Completion: 0.8,
				Quality:    0.7,
				Efficiency: 0.9,
			},
		},
		{
			Timestamp: now,
			Session:   "other-session",
			AgentType: "claude",
			TaskType:  "refactor",
			Metrics: ScoreMetrics{
				Completion: 0.95,
				Quality:    0.85,
				Efficiency: 0.8,
			},
		},
	}

	for i := range scores {
		if err := tracker.Record(&scores[i]); err != nil {
			t.Fatalf("Record() error: %v", err)
		}
	}

	// Close and reopen to ensure persistence
	tracker.Close()
	tracker, err = NewTracker(TrackerOptions{Path: scorePath, Enabled: true})
	if err != nil {
		t.Fatalf("NewTracker() reopen error: %v", err)
	}
	defer tracker.Close()

	// Query all scores
	t.Run("query all", func(t *testing.T) {
		results, err := tracker.QueryScores(Query{})
		if err != nil {
			t.Fatalf("QueryScores() error: %v", err)
		}
		if len(results) != 3 {
			t.Errorf("QueryScores() returned %d scores, want 3", len(results))
		}
	})

	// Query by agent type
	t.Run("query by agent type", func(t *testing.T) {
		results, err := tracker.QueryScores(Query{AgentType: "claude"})
		if err != nil {
			t.Fatalf("QueryScores() error: %v", err)
		}
		if len(results) != 2 {
			t.Errorf("QueryScores(claude) returned %d scores, want 2", len(results))
		}
	})

	// Query by session
	t.Run("query by session", func(t *testing.T) {
		results, err := tracker.QueryScores(Query{Session: "test-session"})
		if err != nil {
			t.Fatalf("QueryScores() error: %v", err)
		}
		if len(results) != 2 {
			t.Errorf("QueryScores(test-session) returned %d scores, want 2", len(results))
		}
	})

	// Query by task type
	t.Run("query by task type", func(t *testing.T) {
		results, err := tracker.QueryScores(Query{TaskType: "bug_fix"})
		if err != nil {
			t.Fatalf("QueryScores() error: %v", err)
		}
		if len(results) != 1 {
			t.Errorf("QueryScores(bug_fix) returned %d scores, want 1", len(results))
		}
	})

	// Query with limit
	t.Run("query with limit", func(t *testing.T) {
		results, err := tracker.QueryScores(Query{Limit: 2})
		if err != nil {
			t.Fatalf("QueryScores() error: %v", err)
		}
		if len(results) != 2 {
			t.Errorf("QueryScores(limit=2) returned %d scores, want 2", len(results))
		}
	})

	// Query since timestamp
	t.Run("query since timestamp", func(t *testing.T) {
		results, err := tracker.QueryScores(Query{Since: now.Add(-90 * time.Minute)})
		if err != nil {
			t.Fatalf("QueryScores() error: %v", err)
		}
		if len(results) != 2 {
			t.Errorf("QueryScores(since) returned %d scores, want 2", len(results))
		}
	})
}

func TestTracker_RollingAverage(t *testing.T) {
	tmpDir := t.TempDir()
	scorePath := filepath.Join(tmpDir, "scores.jsonl")

	tracker, err := NewTracker(TrackerOptions{Path: scorePath, Enabled: true})
	if err != nil {
		t.Fatalf("NewTracker() error: %v", err)
	}
	defer tracker.Close()

	// Record scores with known averages
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		score := Score{
			Timestamp: now.Add(-time.Duration(i) * time.Hour),
			Session:   "test",
			AgentType: "claude",
			Metrics: ScoreMetrics{
				Overall: 0.8, // All same for easy testing
			},
		}
		if err := tracker.Record(&score); err != nil {
			t.Fatalf("Record() error: %v", err)
		}
	}

	avg, err := tracker.RollingAverage(Query{AgentType: "claude"}, 7)
	if err != nil {
		t.Fatalf("RollingAverage() error: %v", err)
	}

	if avg != 0.8 {
		t.Errorf("RollingAverage() = %v, want 0.8", avg)
	}
}

func TestTracker_PruneRetention(t *testing.T) {
	tmpDir := t.TempDir()
	scorePath := filepath.Join(tmpDir, "scores.jsonl")

	tracker, err := NewTracker(TrackerOptions{Path: scorePath, Enabled: true, RetentionDays: 365})
	if err != nil {
		t.Fatalf("NewTracker() error: %v", err)
	}
	defer tracker.Close()

	now := time.Date(2026, 2, 4, 0, 0, 0, 0, time.UTC)
	scores := []Score{
		{
			Timestamp: now.Add(-48 * time.Hour),
			AgentType: "claude",
			Metrics:   ScoreMetrics{Overall: 0.5},
		},
		{
			Timestamp: now.Add(-12 * time.Hour),
			AgentType: "codex",
			Metrics:   ScoreMetrics{Overall: 0.7},
		},
		{
			Timestamp: now.Add(-1 * time.Hour),
			AgentType: "gemini",
			Metrics:   ScoreMetrics{Overall: 0.9},
		},
	}

	for i := range scores {
		if err := tracker.Record(&scores[i]); err != nil {
			t.Fatalf("Record() error: %v", err)
		}
	}

	// Tighten retention window after recording to test pruning behavior.
	tracker.retentionDays = 1
	if err := tracker.pruneAt(now); err != nil {
		t.Fatalf("pruneAt() error: %v", err)
	}

	results, err := tracker.QueryScores(Query{})
	if err != nil {
		t.Fatalf("QueryScores() error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 scores after pruning, got %d", len(results))
	}

	cutoff := now.Add(-24 * time.Hour)
	for _, s := range results {
		if s.Timestamp.Before(cutoff) {
			t.Errorf("expected old score to be pruned, found timestamp %v", s.Timestamp)
		}
	}
}

func TestTracker_AnalyzeTrend(t *testing.T) {
	tmpDir := t.TempDir()
	scorePath := filepath.Join(tmpDir, "scores.jsonl")

	tracker, err := NewTracker(TrackerOptions{Path: scorePath, Enabled: true})
	if err != nil {
		t.Fatalf("NewTracker() error: %v", err)
	}
	defer tracker.Close()

	now := time.Now().UTC()

	t.Run("improving trend", func(t *testing.T) {
		// Clear and add improving scores
		os.WriteFile(scorePath, nil, 0644)
		tracker.Close()
		tracker, _ = NewTracker(TrackerOptions{Path: scorePath, Enabled: true})

		// Earlier scores lower, recent scores higher
		values := []float64{0.5, 0.55, 0.6, 0.7, 0.75, 0.8}
		for i, v := range values {
			score := Score{
				Timestamp: now.Add(-time.Duration(len(values)-i) * 24 * time.Hour),
				AgentType: "claude",
				Metrics:   ScoreMetrics{Overall: v},
			}
			tracker.Record(&score)
		}

		analysis, err := tracker.AnalyzeTrend(Query{AgentType: "claude"}, 30)
		if err != nil {
			t.Fatalf("AnalyzeTrend() error: %v", err)
		}

		if analysis.Trend != TrendImproving {
			t.Errorf("AnalyzeTrend() trend = %v, want improving", analysis.Trend)
		}
		if analysis.RecentAvg <= analysis.EarlierAvg {
			t.Errorf("RecentAvg (%v) should be > EarlierAvg (%v)", analysis.RecentAvg, analysis.EarlierAvg)
		}
	})

	t.Run("declining trend", func(t *testing.T) {
		os.WriteFile(scorePath, nil, 0644)
		tracker.Close()
		tracker, _ = NewTracker(TrackerOptions{Path: scorePath, Enabled: true})

		// Earlier scores higher, recent scores lower
		values := []float64{0.9, 0.85, 0.8, 0.7, 0.6, 0.5}
		for i, v := range values {
			score := Score{
				Timestamp: now.Add(-time.Duration(len(values)-i) * 24 * time.Hour),
				AgentType: "claude",
				Metrics:   ScoreMetrics{Overall: v},
			}
			tracker.Record(&score)
		}

		analysis, err := tracker.AnalyzeTrend(Query{AgentType: "claude"}, 30)
		if err != nil {
			t.Fatalf("AnalyzeTrend() error: %v", err)
		}

		if analysis.Trend != TrendDeclining {
			t.Errorf("AnalyzeTrend() trend = %v, want declining", analysis.Trend)
		}
	})

	t.Run("insufficient samples", func(t *testing.T) {
		os.WriteFile(scorePath, nil, 0644)
		tracker.Close()
		tracker, _ = NewTracker(TrackerOptions{Path: scorePath, Enabled: true})

		// Only 2 samples
		for i := 0; i < 2; i++ {
			score := Score{
				Timestamp: now.Add(-time.Duration(i) * time.Hour),
				AgentType: "claude",
				Metrics:   ScoreMetrics{Overall: 0.8},
			}
			tracker.Record(&score)
		}

		analysis, err := tracker.AnalyzeTrend(Query{AgentType: "claude"}, 7)
		if err != nil {
			t.Fatalf("AnalyzeTrend() error: %v", err)
		}

		if analysis.Trend != TrendUnknown {
			t.Errorf("AnalyzeTrend() trend = %v, want unknown (insufficient samples)", analysis.Trend)
		}
	})
}

func TestTracker_SummarizeByAgent(t *testing.T) {
	tmpDir := t.TempDir()
	scorePath := filepath.Join(tmpDir, "scores.jsonl")

	tracker, err := NewTracker(TrackerOptions{Path: scorePath, Enabled: true})
	if err != nil {
		t.Fatalf("NewTracker() error: %v", err)
	}
	defer tracker.Close()

	now := time.Now().UTC()

	// Add scores for different agents
	agents := []struct {
		agentType string
		scores    []float64
	}{
		{"claude", []float64{0.9, 0.85, 0.88, 0.92}},
		{"codex", []float64{0.75, 0.78, 0.80}},
		{"gemini", []float64{0.82, 0.85}},
	}

	for _, agent := range agents {
		for i, overall := range agent.scores {
			score := Score{
				Timestamp: now.Add(-time.Duration(i) * time.Hour),
				AgentType: agent.agentType,
				Metrics:   ScoreMetrics{Overall: overall, Completion: overall},
			}
			tracker.Record(&score)
		}
	}

	summaries, err := tracker.SummarizeByAgent(now.Add(-7 * 24 * time.Hour))
	if err != nil {
		t.Fatalf("SummarizeByAgent() error: %v", err)
	}

	if len(summaries) != 3 {
		t.Errorf("SummarizeByAgent() returned %d summaries, want 3", len(summaries))
	}

	if claude, ok := summaries["claude"]; ok {
		if claude.TotalScores != 4 {
			t.Errorf("claude TotalScores = %d, want 4", claude.TotalScores)
		}
		// Average of 0.9, 0.85, 0.88, 0.92 = 0.8875
		expectedAvg := 0.8875
		if diff := claude.AvgOverall - expectedAvg; diff < -0.01 || diff > 0.01 {
			t.Errorf("claude AvgOverall = %v, want ~%v", claude.AvgOverall, expectedAvg)
		}
	} else {
		t.Error("claude summary missing")
	}
}

func TestTracker_SummarizeByAgentList(t *testing.T) {
	tmpDir := t.TempDir()
	scorePath := filepath.Join(tmpDir, "scores.jsonl")

	tracker, err := NewTracker(TrackerOptions{Path: scorePath, Enabled: true})
	if err != nil {
		t.Fatalf("NewTracker() error: %v", err)
	}
	defer tracker.Close()

	now := time.Now().UTC()
	entries := []Score{
		{Timestamp: now, AgentType: "gemini", Metrics: ScoreMetrics{Overall: 0.8}},
		{Timestamp: now, AgentType: "claude", Metrics: ScoreMetrics{Overall: 0.9}},
		{Timestamp: now, AgentType: "codex", Metrics: ScoreMetrics{Overall: 0.7}},
	}
	for i := range entries {
		if err := tracker.Record(&entries[i]); err != nil {
			t.Fatalf("Record() error: %v", err)
		}
	}

	ordered, err := tracker.SummarizeByAgentList(now.Add(-24 * time.Hour))
	if err != nil {
		t.Fatalf("SummarizeByAgentList() error: %v", err)
	}
	if len(ordered) != 3 {
		t.Fatalf("SummarizeByAgentList() returned %d summaries, want 3", len(ordered))
	}

	got := []string{ordered[0].AgentType, ordered[1].AgentType, ordered[2].AgentType}
	want := []string{"claude", "codex", "gemini"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ordered[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestTracker_Export(t *testing.T) {
	tmpDir := t.TempDir()
	scorePath := filepath.Join(tmpDir, "scores.jsonl")
	exportPath := filepath.Join(tmpDir, "export.json")

	tracker, err := NewTracker(TrackerOptions{Path: scorePath, Enabled: true})
	if err != nil {
		t.Fatalf("NewTracker() error: %v", err)
	}
	defer tracker.Close()

	// Record some scores
	for i := 0; i < 3; i++ {
		score := Score{
			Timestamp: time.Now().UTC(),
			Session:   "test",
			AgentType: "claude",
			Metrics:   ScoreMetrics{Overall: 0.8},
		}
		tracker.Record(&score)
	}

	// Export
	if err := tracker.Export(exportPath, time.Time{}); err != nil {
		t.Fatalf("Export() error: %v", err)
	}

	// Verify export file exists and has content
	data, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatalf("reading export: %v", err)
	}

	if len(data) == 0 {
		t.Error("export file is empty")
	}
}

func TestTracker_Disabled(t *testing.T) {
	tracker, err := NewTracker(TrackerOptions{Enabled: false})
	if err != nil {
		t.Fatalf("NewTracker() error: %v", err)
	}

	// Should not error when disabled
	err = tracker.Record(&Score{AgentType: "claude"})
	if err != nil {
		t.Errorf("Record() with disabled tracker should not error: %v", err)
	}

	scores, err := tracker.QueryScores(Query{})
	if err != nil {
		t.Errorf("QueryScores() with disabled tracker should not error: %v", err)
	}
	if scores != nil {
		t.Errorf("QueryScores() with disabled tracker should return nil, got %v", scores)
	}
}

func TestTracker_RecordSessionEnd(t *testing.T) {
	tmpDir := t.TempDir()
	scorePath := filepath.Join(tmpDir, "scores.jsonl")

	tracker, err := NewTracker(TrackerOptions{Path: scorePath, Enabled: true})
	if err != nil {
		t.Fatalf("NewTracker() error: %v", err)
	}
	defer tracker.Close()

	scores := []Score{
		{AgentType: "claude", Metrics: ScoreMetrics{Overall: 0.9}},
		{AgentType: "codex", Metrics: ScoreMetrics{Overall: 0.8}},
	}

	if err := tracker.RecordSessionEnd("my-session", scores); err != nil {
		t.Fatalf("RecordSessionEnd() error: %v", err)
	}

	// Query and verify session was set
	results, err := tracker.QueryScores(Query{Session: "my-session"})
	if err != nil {
		t.Fatalf("QueryScores() error: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("RecordSessionEnd() recorded %d scores, want 2", len(results))
	}
}

func TestSqrt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     float64
		expected  float64
		tolerance float64
	}{
		{"zero", 0, 0, 0.001},
		{"negative", -4, 0, 0.001},
		{"one", 1, 1, 0.0001},
		{"four", 4, 2, 0.0001},
		{"nine", 9, 3, 0.0001},
		{"two", 2, math.Sqrt(2), 0.0001},
		{"large", 10000, 100, 0.0001},
		{"small fraction", 0.25, 0.5, 0.0001},
		{"pi", math.Pi, math.Sqrt(math.Pi), 0.0001},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := sqrt(tc.input)
			if diff := got - tc.expected; diff < -tc.tolerance || diff > tc.tolerance {
				t.Errorf("sqrt(%v) = %v, want %v (diff=%v)", tc.input, got, tc.expected, diff)
			}
		})
	}
}

// =============================================================================
// bd-1u5g: Additional coverage tests
// =============================================================================

func TestDefaultTrackerOptions(t *testing.T) {
	t.Parallel()

	opts := DefaultTrackerOptions()

	if opts.RetentionDays != DefaultRetentionDays {
		t.Errorf("RetentionDays = %d, want %d", opts.RetentionDays, DefaultRetentionDays)
	}
	if !opts.Enabled {
		t.Error("Enabled should be true by default")
	}
	if opts.Path == "" {
		t.Error("Path should not be empty")
	}
	t.Logf("SCORE_TEST: DefaultTrackerOptions | Path=%s | Retention=%d | Enabled=%v",
		opts.Path, opts.RetentionDays, opts.Enabled)
}

func TestTracker_Prune(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	scorePath := filepath.Join(tmpDir, "scores.jsonl")

	tracker, err := NewTracker(TrackerOptions{Path: scorePath, Enabled: true, RetentionDays: 1})
	if err != nil {
		t.Fatalf("NewTracker() error: %v", err)
	}
	defer tracker.Close()

	// Write an old score directly
	now := time.Now().UTC()
	old := Score{
		Timestamp: now.AddDate(0, 0, -5),
		AgentType: "claude",
		Metrics:   ScoreMetrics{Overall: 0.5},
	}
	recent := Score{
		Timestamp: now.Add(-1 * time.Hour),
		AgentType: "codex",
		Metrics:   ScoreMetrics{Overall: 0.9},
	}
	for _, s := range []*Score{&old, &recent} {
		if err := tracker.Record(s); err != nil {
			t.Fatalf("Record() error: %v", err)
		}
	}

	// Public Prune should remove the old one
	if err := tracker.Prune(); err != nil {
		t.Fatalf("Prune() error: %v", err)
	}

	results, err := tracker.QueryScores(Query{})
	if err != nil {
		t.Fatalf("QueryScores() error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 score after Prune(), got %d", len(results))
	}
	if results[0].AgentType != "codex" {
		t.Errorf("expected codex score to survive, got %s", results[0].AgentType)
	}
	t.Logf("SCORE_TEST: Prune | kept=%d | removed=1", len(results))
}

func TestTracker_PruneMalformedLines(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	scorePath := filepath.Join(tmpDir, "scores.jsonl")

	// Write a mix of valid and malformed lines
	content := `{"timestamp":"2020-01-01T00:00:00Z","agent_type":"old","metrics":{"overall":0.5}}
not-valid-json
{"timestamp":"` + time.Now().UTC().Format(time.RFC3339) + `","agent_type":"recent","metrics":{"overall":0.9}}
`
	if err := os.WriteFile(scorePath, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	tracker, err := NewTracker(TrackerOptions{Path: scorePath, Enabled: true, RetentionDays: 1})
	if err != nil {
		t.Fatalf("NewTracker() error: %v", err)
	}
	defer tracker.Close()

	// Prune should keep malformed lines and recent scores, remove old
	if err := tracker.Prune(); err != nil {
		t.Fatalf("Prune() error: %v", err)
	}

	results, err := tracker.QueryScores(Query{})
	if err != nil {
		t.Fatalf("QueryScores() error: %v", err)
	}
	// The recent score should survive; old one pruned; malformed skipped by query
	if len(results) != 1 {
		t.Errorf("expected 1 queryable score after pruning malformed file, got %d", len(results))
	}
}

func TestTracker_RecordAutoFields(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	scorePath := filepath.Join(tmpDir, "scores.jsonl")

	tracker, err := NewTracker(TrackerOptions{Path: scorePath, Enabled: true})
	if err != nil {
		t.Fatalf("NewTracker() error: %v", err)
	}
	defer tracker.Close()

	// Record with zero timestamp and zero Overall
	score := Score{
		AgentType: "claude",
		Metrics: ScoreMetrics{
			Completion: 0.8,
			Quality:    0.7,
			Efficiency: 0.6,
		},
	}
	if err := tracker.Record(&score); err != nil {
		t.Fatalf("Record() error: %v", err)
	}

	// Timestamp should be auto-set
	if score.Timestamp.IsZero() {
		t.Error("Record() should auto-set timestamp")
	}

	// Overall should be auto-computed
	if score.Metrics.Overall == 0 {
		t.Error("Record() should auto-compute Overall when zero")
	}

	t.Logf("SCORE_TEST: RecordAutoFields | Timestamp=%v | Overall=%.3f",
		score.Timestamp, score.Metrics.Overall)
}

func TestTracker_RollingAverageDefaultWindow(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	scorePath := filepath.Join(tmpDir, "scores.jsonl")

	tracker, err := NewTracker(TrackerOptions{Path: scorePath, Enabled: true})
	if err != nil {
		t.Fatalf("NewTracker() error: %v", err)
	}
	defer tracker.Close()

	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		s := Score{
			Timestamp: now.Add(-time.Duration(i) * time.Hour),
			AgentType: "claude",
			Metrics:   ScoreMetrics{Overall: 0.75},
		}
		tracker.Record(&s)
	}

	// Pass windowDays=0 to trigger default
	avg, err := tracker.RollingAverage(Query{AgentType: "claude"}, 0)
	if err != nil {
		t.Fatalf("RollingAverage() error: %v", err)
	}
	if avg != 0.75 {
		t.Errorf("RollingAverage(window=0) = %v, want 0.75", avg)
	}
}

func TestTracker_RollingAverageEmpty(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	scorePath := filepath.Join(tmpDir, "scores.jsonl")

	tracker, err := NewTracker(TrackerOptions{Path: scorePath, Enabled: true})
	if err != nil {
		t.Fatalf("NewTracker() error: %v", err)
	}
	defer tracker.Close()

	avg, err := tracker.RollingAverage(Query{AgentType: "nonexistent"}, 7)
	if err != nil {
		t.Fatalf("RollingAverage() error: %v", err)
	}
	if avg != 0 {
		t.Errorf("RollingAverage(empty) = %v, want 0", avg)
	}
}

func TestTracker_QueryScoresFileNotFound(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	scorePath := filepath.Join(tmpDir, "nonexistent.jsonl")

	tracker, err := NewTracker(TrackerOptions{Path: scorePath, Enabled: true})
	if err != nil {
		t.Fatalf("NewTracker() error: %v", err)
	}
	defer tracker.Close()

	results, err := tracker.QueryScores(Query{})
	if err != nil {
		t.Fatalf("QueryScores() should not error for missing file: %v", err)
	}
	if results != nil {
		t.Errorf("QueryScores() should return nil for missing file, got %d scores", len(results))
	}
}

func TestTracker_PruneNoFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	scorePath := filepath.Join(tmpDir, "nonexistent.jsonl")

	tracker, err := NewTracker(TrackerOptions{Path: scorePath, Enabled: true, RetentionDays: 30})
	if err != nil {
		t.Fatalf("NewTracker() error: %v", err)
	}

	// Should not error when file doesn't exist
	if err := tracker.Prune(); err != nil {
		t.Errorf("Prune() should not error for missing file: %v", err)
	}
}

func TestTracker_PruneDisabled(t *testing.T) {
	t.Parallel()

	tracker, err := NewTracker(TrackerOptions{Enabled: false})
	if err != nil {
		t.Fatalf("NewTracker() error: %v", err)
	}

	if err := tracker.Prune(); err != nil {
		t.Errorf("Prune() with disabled tracker should not error: %v", err)
	}
}

func TestLoadWeightsConfig_Errors(t *testing.T) {
	t.Parallel()

	t.Run("file not found", func(t *testing.T) {
		t.Parallel()
		_, err := LoadWeightsConfig("/nonexistent/path.json")
		if err == nil {
			t.Error("expected error for missing config file")
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		t.Parallel()
		tmpPath := filepath.Join(t.TempDir(), "bad.json")
		os.WriteFile(tmpPath, []byte("{invalid"), 0644)
		_, err := LoadWeightsConfig(tmpPath)
		if err == nil {
			t.Error("expected error for invalid JSON config")
		}
	})
}

func TestTracker_ExportNoScores(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	scorePath := filepath.Join(tmpDir, "scores.jsonl")
	exportPath := filepath.Join(tmpDir, "export.json")

	tracker, err := NewTracker(TrackerOptions{Path: scorePath, Enabled: true})
	if err != nil {
		t.Fatalf("NewTracker() error: %v", err)
	}
	defer tracker.Close()

	if err := tracker.Export(exportPath, time.Time{}); err != nil {
		t.Fatalf("Export() error: %v", err)
	}

	data, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatalf("reading export: %v", err)
	}
	// Empty export should still be valid JSON
	if string(data) != "null" && string(data) != "[]" {
		t.Logf("empty export content: %s", string(data))
	}
}

func TestExpandPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{"empty string", ""},
		{"no tilde", "/usr/local/bin"},
		{"relative path", "relative/path"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := util.ExpandPath(tc.input)
			if got != tc.input {
				t.Errorf("ExpandPath(%q) = %q, want %q", tc.input, got, tc.input)
			}
		})
	}

	// Tilde expansion should produce a different path
	t.Run("tilde expansion", func(t *testing.T) {
		got := util.ExpandPath("~/foo")
		if got == "~/foo" {
			t.Error("ExpandPath(\"~/foo\") should expand tilde")
		}
		if !filepath.IsAbs(got) {
			t.Errorf("ExpandPath(\"~/foo\") = %q, expected absolute path", got)
		}
	})
}
