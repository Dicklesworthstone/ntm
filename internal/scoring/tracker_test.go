package scoring

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
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
			got := expandPath(tc.input)
			if got != tc.input {
				t.Errorf("expandPath(%q) = %q, want %q", tc.input, got, tc.input)
			}
		})
	}

	// Tilde expansion should produce a different path
	t.Run("tilde expansion", func(t *testing.T) {
		got := expandPath("~/foo")
		if got == "~/foo" {
			t.Error("expandPath(\"~/foo\") should expand tilde")
		}
		if !filepath.IsAbs(got) {
			t.Errorf("expandPath(\"~/foo\") = %q, expected absolute path", got)
		}
	})
}
