package metrics

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCollectorBasicOperations(t *testing.T) {
	// Create collector without store
	c := NewCollector(nil, "test-session")
	defer c.Close()

	// Test RecordAPICall
	recordAPICallForTest(c, "bv", "triage")
	recordAPICallForTest(c, "bv", "triage")
	recordAPICallForTest(c, "bd", "create")

	// Test RecordLatency
	recordLatencyForTest(c, "cm_query", 50*time.Millisecond)
	recordLatencyForTest(c, "cm_query", 100*time.Millisecond)
	recordLatencyForTest(c, "cm_query", 75*time.Millisecond)

	// Test RecordBlockedCommand
	c.RecordBlockedCommand("agent-1", "rm -rf /", "destructive")

	// Test RecordFileConflict
	recordFileConflictForTest(c, "agent-1", "agent-2", "*.go")

	// Generate report
	report, err := c.GenerateReport()
	if err != nil {
		t.Fatalf("GenerateReport failed: %v", err)
	}

	// Verify API calls
	if report.APICallCounts["bv:triage"] != 2 {
		t.Errorf("expected bv:triage=2, got %d", report.APICallCounts["bv:triage"])
	}
	if report.APICallCounts["bd:create"] != 1 {
		t.Errorf("expected bd:create=1, got %d", report.APICallCounts["bd:create"])
	}

	// Verify latency stats
	stats, ok := report.LatencyStats["cm_query"]
	if !ok {
		t.Fatal("expected cm_query latency stats")
	}
	if stats.Count != 3 {
		t.Errorf("expected count=3, got %d", stats.Count)
	}
	if stats.MinMs != 50 {
		t.Errorf("expected min=50, got %.1f", stats.MinMs)
	}
	if stats.MaxMs != 100 {
		t.Errorf("expected max=100, got %.1f", stats.MaxMs)
	}

	// Verify incidents
	if report.BlockedCommands != 1 {
		t.Errorf("expected blocked_commands=1, got %d", report.BlockedCommands)
	}
	if report.FileConflicts != 1 {
		t.Errorf("expected file_conflicts=1, got %d", report.FileConflicts)
	}
}

func TestLatencyStatistics(t *testing.T) {
	samples := []float64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	stats := calculateLatencyStats(samples)

	if stats.Count != 10 {
		t.Errorf("expected count=10, got %d", stats.Count)
	}
	if stats.MinMs != 10 {
		t.Errorf("expected min=10, got %.1f", stats.MinMs)
	}
	if stats.MaxMs != 100 {
		t.Errorf("expected max=100, got %.1f", stats.MaxMs)
	}
	if stats.AvgMs != 55 {
		t.Errorf("expected avg=55, got %.1f", stats.AvgMs)
	}
	// P50 should be around 50-60
	if stats.P50Ms < 50 || stats.P50Ms > 60 {
		t.Errorf("expected p50 around 50-60, got %.1f", stats.P50Ms)
	}
}

func TestTargetComparison(t *testing.T) {
	c := NewCollector(nil, "test-session")
	defer c.Close()

	// Should start meeting targets (no incidents)
	report, _ := c.GenerateReport()

	for _, tc := range report.TargetComparison {
		if tc.Metric == "destructive_cmd_incidents" && tc.Status != "met" {
			t.Errorf("expected destructive_cmd_incidents to be met with 0 incidents")
		}
		if tc.Metric == "file_conflicts" && tc.Status != "met" {
			t.Errorf("expected file_conflicts to be met with 0 conflicts")
		}
	}

	// Add an incident
	c.RecordBlockedCommand("agent", "rm", "policy")

	report, _ = c.GenerateReport()
	// Now should show regressing (if target is 0)
	// Note: The target is 0, so 1 incident means regressing
	found := false
	for _, tc := range report.TargetComparison {
		if tc.Metric == "destructive_cmd_incidents" {
			found = true
			if tc.Current != 1 {
				t.Errorf("expected current=1, got %.1f", tc.Current)
			}
		}
	}
	if !found {
		t.Error("expected destructive_cmd_incidents in target comparison")
	}
}

func TestCompareSnapshots(t *testing.T) {
	c := NewCollector(nil, "test-session")
	defer c.Close()

	// Baseline: latency 500ms
	baseline := &MetricsReport{
		SessionID: "baseline",
		LatencyStats: map[string]LatencyStats{
			"cm_query": {Count: 10, AvgMs: 500},
		},
		BlockedCommands: 0,
		FileConflicts:   0,
	}

	// Current: latency improved to 50ms
	recordLatencyForTest(c, "cm_query", 50*time.Millisecond)
	current, _ := c.GenerateReport()

	result := c.CompareSnapshots(baseline, current)

	// Should detect improvement in latency
	if len(result.Improvements) == 0 {
		t.Error("expected latency improvement to be detected")
	}

	// Should have no regressions
	if len(result.Regressions) != 0 {
		t.Errorf("expected no regressions, got %v", result.Regressions)
	}
}

func TestExportFormats(t *testing.T) {
	c := NewCollector(nil, "test-session")
	defer c.Close()

	recordLatencyForTest(c, "test_op", 100*time.Millisecond)

	report, err := c.GenerateReport()
	if err != nil {
		t.Fatalf("GenerateReport failed: %v", err)
	}

	// Test JSON export
	jsonData, err := report.ExportJSON()
	if err != nil {
		t.Fatalf("ExportJSON failed: %v", err)
	}
	if len(jsonData) == 0 {
		t.Error("JSON export should not be empty")
	}

	// Test CSV export
	csvData := report.ExportCSV()
	if csvData == "" {
		t.Error("CSV export should not be empty")
	}
	if !contains(csvData, "operation") || !contains(csvData, "test_op") {
		t.Error("CSV should contain header and test_op data")
	}
}

func TestSortFloat64s(t *testing.T) {
	input := []float64{5, 2, 8, 1, 9, 3}
	sortFloat64s(input)

	expected := []float64{1, 2, 3, 5, 8, 9}
	for i, v := range input {
		if v != expected[i] {
			t.Errorf("expected sorted[%d]=%f, got %f", i, expected[i], v)
		}
	}
}

func TestPercentile(t *testing.T) {
	sorted := []float64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}

	p50 := percentile(sorted, 50)
	if p50 != 50 && p50 != 60 { // P50 should be around 50-60
		t.Errorf("expected p50 around 50-60, got %.1f", p50)
	}

	p95 := percentile(sorted, 95)
	if p95 < 90 {
		t.Errorf("expected p95 >= 90, got %.1f", p95)
	}
}

func TestAverage(t *testing.T) {
	samples := []float64{10, 20, 30}
	avg := average(samples)
	if avg != 20 {
		t.Errorf("expected avg=20, got %.1f", avg)
	}

	// Empty slice
	emptyAvg := average([]float64{})
	if emptyAvg != 0 {
		t.Errorf("expected empty avg=0, got %.1f", emptyAvg)
	}
}

func TestGetTargetStatus(t *testing.T) {
	// Lower is better
	if getTargetStatus(0, 0, true) != "met" {
		t.Error("0 vs target 0 should be met")
	}
	if getTargetStatus(5, 0, true) != "regressing" {
		t.Error("5 vs target 0 should be regressing")
	}
	if getTargetStatus(10, 50, true) != "met" {
		t.Error("10 vs target 50 should be met (lower is better)")
	}

	// Higher is better
	if getTargetStatus(100, 50, false) != "met" {
		t.Error("100 vs target 50 should be met (higher is better)")
	}
	if getTargetStatus(30, 50, false) != "regressing" {
		t.Error("30 vs target 50 should be regressing (higher is better)")
	}
}

func TestGenerateTargetComparisons_WithLatency(t *testing.T) {
	c := NewCollector(nil, "test-session")
	defer c.Close()

	// Add CM query latency samples
	recordLatencyForTest(c, "cm_query", 40*time.Millisecond)
	recordLatencyForTest(c, "cm_query", 60*time.Millisecond)

	report, err := c.GenerateReport()
	if err != nil {
		t.Fatalf("GenerateReport failed: %v", err)
	}

	// Should have 3 comparisons: destructive_cmd_incidents, file_conflicts, cm_query_latency_ms
	if len(report.TargetComparison) != 3 {
		t.Errorf("expected 3 target comparisons, got %d", len(report.TargetComparison))
	}

	// Find cm_query_latency_ms comparison
	found := false
	for _, tc := range report.TargetComparison {
		if tc.Metric == "cm_query_latency_ms" {
			found = true
			if tc.Target != Tier0Targets["cm_query_latency_ms"] {
				t.Errorf("target = %v, want %v", tc.Target, Tier0Targets["cm_query_latency_ms"])
			}
			if tc.Baseline != Tier0Baselines["cm_query_latency_ms"] {
				t.Errorf("baseline = %v, want %v", tc.Baseline, Tier0Baselines["cm_query_latency_ms"])
			}
			// Average of 40 and 60 = 50, which meets the target of <=50
			if tc.Status != "met" {
				t.Errorf("status = %q, want %q", tc.Status, "met")
			}
		}
	}
	if !found {
		t.Error("expected cm_query_latency_ms in target comparisons")
	}
}

func TestGenerateTargetComparisons_NoLatency(t *testing.T) {
	c := NewCollector(nil, "test-session")
	defer c.Close()

	report, err := c.GenerateReport()
	if err != nil {
		t.Fatalf("GenerateReport failed: %v", err)
	}

	// Without cm_query latency, should have only 2 comparisons
	if len(report.TargetComparison) != 2 {
		t.Errorf("expected 2 target comparisons (no latency data), got %d", len(report.TargetComparison))
	}

	for _, tc := range report.TargetComparison {
		if tc.Metric == "cm_query_latency_ms" {
			t.Error("cm_query_latency_ms should not appear without latency data")
		}
	}
}

func TestCompareSnapshots_Regressions(t *testing.T) {
	c := NewCollector(nil, "test-session")
	defer c.Close()

	baseline := &MetricsReport{
		SessionID:       "baseline",
		APICallCounts:   map[string]int64{"bv:triage": 5},
		LatencyStats:    map[string]LatencyStats{"op1": {Count: 10, AvgMs: 100}},
		BlockedCommands: 0,
		FileConflicts:   0,
	}

	// Current has regressions
	current := &MetricsReport{
		SessionID:       "current",
		APICallCounts:   map[string]int64{"bv:triage": 10, "new:op": 3},
		LatencyStats:    map[string]LatencyStats{"op1": {Count: 10, AvgMs: 200}},
		BlockedCommands: 5,
		FileConflicts:   2,
	}

	result := c.CompareSnapshots(baseline, current)

	// API call deltas
	if result.APICallDeltas["bv:triage"] != 5 {
		t.Errorf("bv:triage delta = %d, want 5", result.APICallDeltas["bv:triage"])
	}
	if result.APICallDeltas["new:op"] != 3 {
		t.Errorf("new:op delta = %d, want 3", result.APICallDeltas["new:op"])
	}

	// Should detect regressions (latency doubled, blocked/conflicts increased)
	if len(result.Regressions) == 0 {
		t.Error("expected regressions to be detected")
	}

	// Verify specific regressions found
	hasLatencyRegression := false
	hasBlockedRegression := false
	hasConflictRegression := false
	for _, r := range result.Regressions {
		if containsHelper(r, "latency regressed") {
			hasLatencyRegression = true
		}
		if containsHelper(r, "blocked commands") {
			hasBlockedRegression = true
		}
		if containsHelper(r, "file conflicts") {
			hasConflictRegression = true
		}
	}
	if !hasLatencyRegression {
		t.Error("expected latency regression to be reported")
	}
	if !hasBlockedRegression {
		t.Error("expected blocked commands regression to be reported")
	}
	if !hasConflictRegression {
		t.Error("expected file conflicts regression to be reported")
	}
}

func TestCompareSnapshots_NoChanges(t *testing.T) {
	c := NewCollector(nil, "test-session")
	defer c.Close()

	report := &MetricsReport{
		SessionID:       "same",
		APICallCounts:   map[string]int64{"op": 5},
		LatencyStats:    map[string]LatencyStats{"op": {Count: 10, AvgMs: 100}},
		BlockedCommands: 0,
		FileConflicts:   0,
	}

	result := c.CompareSnapshots(report, report)

	if len(result.Improvements) != 0 {
		t.Errorf("expected no improvements, got %v", result.Improvements)
	}
	if len(result.Regressions) != 0 {
		t.Errorf("expected no regressions, got %v", result.Regressions)
	}
}

func TestCalculateLatencyStats_Empty(t *testing.T) {
	stats := calculateLatencyStats([]float64{})
	if stats.Count != 0 {
		t.Errorf("expected count=0, got %d", stats.Count)
	}
}

func TestCalculateLatencyStats_SingleSample(t *testing.T) {
	stats := calculateLatencyStats([]float64{42.0})
	if stats.Count != 1 {
		t.Errorf("expected count=1, got %d", stats.Count)
	}
	if stats.MinMs != 42.0 || stats.MaxMs != 42.0 {
		t.Errorf("min/max should be 42.0, got %.1f/%.1f", stats.MinMs, stats.MaxMs)
	}
	if stats.AvgMs != 42.0 {
		t.Errorf("avg should be 42.0, got %.1f", stats.AvgMs)
	}
}

func TestTier0Constants(t *testing.T) {
	t.Parallel()

	// Verify targets and baselines have matching keys
	for key := range Tier0Targets {
		if _, ok := Tier0Baselines[key]; !ok {
			t.Errorf("Tier0Targets has key %q not found in Tier0Baselines", key)
		}
	}
	for key := range Tier0Baselines {
		if _, ok := Tier0Targets[key]; !ok {
			t.Errorf("Tier0Baselines has key %q not found in Tier0Targets", key)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// =============================================================================
// Additional Pure Function Tests for Coverage Improvement
// =============================================================================

func TestPercentile_EdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		sorted []float64
		p      int
		want   float64
	}{
		{"empty slice", []float64{}, 50, 0},
		{"p=0 returns first", []float64{10, 20, 30}, 0, 10},
		{"p=100 returns last", []float64{10, 20, 30}, 100, 30},
		{"p>100 clamped to last", []float64{10, 20, 30}, 200, 30},
		{"single element p=0", []float64{42}, 0, 42},
		{"single element p=50", []float64{42}, 50, 42},
		{"single element p=100", []float64{42}, 100, 42},
		{"two elements p=0", []float64{10, 20}, 0, 10},
		{"two elements p=50", []float64{10, 20}, 50, 20},
		{"two elements p=100", []float64{10, 20}, 100, 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := percentile(tt.sorted, tt.p)
			if got != tt.want {
				t.Errorf("percentile(%v, %d) = %v, want %v", tt.sorted, tt.p, got, tt.want)
			}
		})
	}
}

func TestSortFloat64s_EdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input []float64
		want  []float64
	}{
		{"empty", []float64{}, []float64{}},
		{"single", []float64{42}, []float64{42}},
		{"already sorted", []float64{1, 2, 3}, []float64{1, 2, 3}},
		{"reverse sorted", []float64{3, 2, 1}, []float64{1, 2, 3}},
		{"with duplicates", []float64{3, 1, 2, 1, 3}, []float64{1, 1, 2, 3, 3}},
		{"negative numbers", []float64{-1, -5, 0, 2, -3}, []float64{-5, -3, -1, 0, 2}},
		{"with zero", []float64{0, -1, 1}, []float64{-1, 0, 1}},
		{"all same", []float64{5, 5, 5, 5}, []float64{5, 5, 5, 5}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Make a copy to not modify test data
			got := make([]float64, len(tt.input))
			copy(got, tt.input)
			sortFloat64s(got)

			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("sorted[%d] = %v, want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestAverage_EdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input []float64
		want  float64
	}{
		{"single element", []float64{42}, 42},
		{"negative numbers", []float64{-10, -20, -30}, -20},
		{"mixed signs", []float64{-10, 10}, 0},
		{"with zero", []float64{0, 10, 20}, 10},
		{"large numbers", []float64{1000000, 2000000, 3000000}, 2000000},
		{"small fractions", []float64{3, 6, 9}, 6}, // use integers to avoid fp precision issues
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := average(tt.input)
			if got != tt.want {
				t.Errorf("average(%v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestCalculateLatencyStats_EdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		samples []float64
		wantMin float64
		wantMax float64
		wantAvg float64
	}{
		{
			name:    "all same values",
			samples: []float64{50, 50, 50, 50, 50},
			wantMin: 50,
			wantMax: 50,
			wantAvg: 50,
		},
		{
			name:    "two values",
			samples: []float64{10, 90},
			wantMin: 10,
			wantMax: 90,
			wantAvg: 50,
		},
		{
			name:    "unsorted input",
			samples: []float64{50, 10, 90, 30, 70},
			wantMin: 10,
			wantMax: 90,
			wantAvg: 50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stats := calculateLatencyStats(tt.samples)
			if stats.MinMs != tt.wantMin {
				t.Errorf("MinMs = %v, want %v", stats.MinMs, tt.wantMin)
			}
			if stats.MaxMs != tt.wantMax {
				t.Errorf("MaxMs = %v, want %v", stats.MaxMs, tt.wantMax)
			}
			if stats.AvgMs != tt.wantAvg {
				t.Errorf("AvgMs = %v, want %v", stats.AvgMs, tt.wantAvg)
			}
			if stats.Count != len(tt.samples) {
				t.Errorf("Count = %v, want %v", stats.Count, len(tt.samples))
			}
		})
	}
}

func TestSaveSnapshot_NilStore(t *testing.T) {
	c := NewCollector(nil, "test-session")
	defer c.Close()

	// Should return nil (no-op) when store is nil
	err := c.SaveSnapshot("test-snapshot")
	if err != nil {
		t.Errorf("SaveSnapshot with nil store should return nil, got %v", err)
	}
}

func TestLoadSnapshot_NilStore(t *testing.T) {
	c := NewCollector(nil, "test-session")
	defer c.Close()

	// Should return error when store is nil
	_, err := c.LoadSnapshot("test-snapshot")
	if err == nil {
		t.Error("LoadSnapshot with nil store should return error")
	}
	if !containsHelper(err.Error(), "no store configured") {
		t.Errorf("expected 'no store configured' error, got %v", err)
	}
}

func TestClose_MultipleCallsSafe(t *testing.T) {
	c := NewCollector(nil, "test-session")

	// First close
	c.Close()

	// Second close should not panic
	c.Close()

	// Third close should not panic
	c.Close()
}

func TestGetTargetStatus_EdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		current       float64
		target        float64
		lowerIsBetter bool
		want          string
	}{
		{"exactly at target (lower)", 50, 50, true, "met"},
		{"exactly at target (higher)", 50, 50, false, "met"},
		{"slightly below (lower is better)", 49.9, 50, true, "met"},
		{"slightly above (lower is better)", 50.1, 50, true, "regressing"},
		{"slightly below (higher is better)", 49.9, 50, false, "regressing"},
		{"slightly above (higher is better)", 50.1, 50, false, "met"},
		{"zero target met with zero", 0, 0, true, "met"},
		{"negative below zero target", -1, 0, true, "met"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getTargetStatus(tt.current, tt.target, tt.lowerIsBetter)
			if got != tt.want {
				t.Errorf("getTargetStatus(%v, %v, %v) = %q, want %q",
					tt.current, tt.target, tt.lowerIsBetter, got, tt.want)
			}
		})
	}
}

func TestExportCSV_Empty(t *testing.T) {
	report := &MetricsReport{
		SessionID:    "test",
		LatencyStats: map[string]LatencyStats{},
	}

	csv := report.ExportCSV()

	// Should have header only
	if csv != "operation,count,min_ms,max_ms,avg_ms,p50_ms,p95_ms,p99_ms\n" {
		t.Errorf("empty CSV should only have header, got %q", csv)
	}
}

func TestExportCSV_MultipleOperations(t *testing.T) {
	report := &MetricsReport{
		SessionID: "test",
		LatencyStats: map[string]LatencyStats{
			"op1": {Count: 10, MinMs: 1, MaxMs: 100, AvgMs: 50, P50Ms: 45, P95Ms: 95, P99Ms: 99},
			"op2": {Count: 5, MinMs: 10, MaxMs: 50, AvgMs: 30, P50Ms: 28, P95Ms: 48, P99Ms: 50},
		},
	}

	csv := report.ExportCSV()

	// Should contain both operations
	if !containsHelper(csv, "op1,10,") {
		t.Error("CSV should contain op1 data")
	}
	if !containsHelper(csv, "op2,5,") {
		t.Error("CSV should contain op2 data")
	}
}

func TestExportJSON_ValidJSON(t *testing.T) {
	c := NewCollector(nil, "test-session")
	defer c.Close()

	recordAPICallForTest(c, "test", "op")
	recordLatencyForTest(c, "test_op", 100*time.Millisecond)

	report, err := c.GenerateReport()
	if err != nil {
		t.Fatalf("GenerateReport failed: %v", err)
	}

	jsonData, err := report.ExportJSON()
	if err != nil {
		t.Fatalf("ExportJSON failed: %v", err)
	}

	// Verify it's valid JSON by unmarshaling
	var parsed MetricsReport
	if err := json.Unmarshal(jsonData, &parsed); err != nil {
		t.Errorf("ExportJSON produced invalid JSON: %v", err)
	}

	// Verify key fields survived round-trip
	if parsed.SessionID != "test-session" {
		t.Errorf("SessionID = %q, want %q", parsed.SessionID, "test-session")
	}
	if parsed.APICallCounts["test:op"] != 1 {
		t.Errorf("APICallCounts[test:op] = %d, want 1", parsed.APICallCounts["test:op"])
	}
}

func TestCompareSnapshots_MissingBaselineOperation(t *testing.T) {
	c := NewCollector(nil, "test-session")
	defer c.Close()

	// Baseline has no operations
	baseline := &MetricsReport{
		SessionID:     "baseline",
		APICallCounts: map[string]int64{},
		LatencyStats:  map[string]LatencyStats{},
	}

	// Current has new operations
	current := &MetricsReport{
		SessionID:     "current",
		APICallCounts: map[string]int64{"new:op": 10},
		LatencyStats:  map[string]LatencyStats{"new_op": {Count: 5, AvgMs: 100}},
	}

	result := c.CompareSnapshots(baseline, current)

	// New operation should show full count as delta
	if result.APICallDeltas["new:op"] != 10 {
		t.Errorf("new:op delta = %d, want 10", result.APICallDeltas["new:op"])
	}
}

func TestGenerateReport_EmptyCollector(t *testing.T) {
	c := NewCollector(nil, "empty-session")
	defer c.Close()

	report, err := c.GenerateReport()
	if err != nil {
		t.Fatalf("GenerateReport failed: %v", err)
	}

	if report.SessionID != "empty-session" {
		t.Errorf("SessionID = %q, want %q", report.SessionID, "empty-session")
	}
	if len(report.APICallCounts) != 0 {
		t.Errorf("APICallCounts should be empty, got %v", report.APICallCounts)
	}
	if len(report.LatencyStats) != 0 {
		t.Errorf("LatencyStats should be empty, got %v", report.LatencyStats)
	}
	if report.BlockedCommands != 0 {
		t.Errorf("BlockedCommands = %d, want 0", report.BlockedCommands)
	}
	if report.FileConflicts != 0 {
		t.Errorf("FileConflicts = %d, want 0", report.FileConflicts)
	}
}

func TestMetricsReport_GeneratedAtIsRecent(t *testing.T) {
	c := NewCollector(nil, "test-session")
	defer c.Close()

	before := time.Now().UTC()
	report, err := c.GenerateReport()
	if err != nil {
		t.Fatalf("GenerateReport failed: %v", err)
	}
	after := time.Now().UTC()

	if report.GeneratedAt.Before(before) || report.GeneratedAt.After(after) {
		t.Errorf("GeneratedAt = %v, expected between %v and %v",
			report.GeneratedAt, before, after)
	}
}

// Test-local recorders standing in for removed production record methods;
// they seed the collector's in-memory aggregates for report/snapshot tests.
func recordAPICallForTest(c *Collector, tool, operation string) {
	c.mu.Lock()
	c.apiCalls[tool+":"+operation]++
	c.mu.Unlock()
}

func recordLatencyForTest(c *Collector, operation string, d time.Duration) {
	ms := float64(d.Nanoseconds()) / 1e6
	c.mu.Lock()
	c.latencies[operation] = append(c.latencies[operation], ms)
	c.mu.Unlock()
}

func recordFileConflictForTest(c *Collector, _, _, _ string) {
	c.mu.Lock()
	c.fileConflicts++
	c.mu.Unlock()
}
