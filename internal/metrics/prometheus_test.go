package metrics

import (
	"strings"
	"testing"
)

func TestExportPrometheus_Empty(t *testing.T) {
	t.Parallel()
	report := &MetricsReport{
		SessionID:     "test-session",
		APICallCounts: map[string]int64{},
		LatencyStats:  map[string]LatencyStats{},
	}

	out := report.ExportPrometheus()

	// Should contain blocked commands and file conflicts (always emitted)
	if !strings.Contains(out, "ntm_blocked_commands_total") {
		t.Error("expected ntm_blocked_commands_total in output")
	}
	if !strings.Contains(out, "ntm_file_conflicts_total") {
		t.Error("expected ntm_file_conflicts_total in output")
	}
	// Should NOT contain API calls or latency sections
	if strings.Contains(out, "ntm_api_calls_total") {
		t.Error("empty report should not contain ntm_api_calls_total")
	}
	if strings.Contains(out, "ntm_operation_duration_ms") {
		t.Error("empty report should not contain ntm_operation_duration_ms")
	}
	// Should NOT contain target sections
	if strings.Contains(out, "ntm_target_current") {
		t.Error("empty report should not contain ntm_target_current")
	}
}

func TestExportPrometheus_APICallCounts(t *testing.T) {
	t.Parallel()
	report := &MetricsReport{
		SessionID: "proj-1",
		APICallCounts: map[string]int64{
			"bv:triage": 5,
			"bd:create": 3,
		},
		LatencyStats: map[string]LatencyStats{},
	}

	out := report.ExportPrometheus()

	// HELP and TYPE lines
	if !strings.Contains(out, "# HELP ntm_api_calls_total") {
		t.Error("expected HELP line for ntm_api_calls_total")
	}
	if !strings.Contains(out, "# TYPE ntm_api_calls_total counter") {
		t.Error("expected TYPE counter for ntm_api_calls_total")
	}
	// Verify sorted order: bd:create before bv:triage
	bdIdx := strings.Index(out, "bd:create")
	bvIdx := strings.Index(out, "bv:triage")
	if bdIdx < 0 || bvIdx < 0 {
		t.Fatal("expected both bd:create and bv:triage in output")
	}
	if bdIdx > bvIdx {
		t.Error("expected bd:create before bv:triage (sorted order)")
	}
	// Check values
	if !strings.Contains(out, `operation="bd:create"} 3`) {
		t.Error("expected bd:create count of 3")
	}
	if !strings.Contains(out, `operation="bv:triage"} 5`) {
		t.Error("expected bv:triage count of 5")
	}
}

func TestExportPrometheus_LatencyStats(t *testing.T) {
	t.Parallel()
	report := &MetricsReport{
		SessionID:     "proj-1",
		APICallCounts: map[string]int64{},
		LatencyStats: map[string]LatencyStats{
			"cm_query": {
				Count: 10,
				MinMs: 5.0,
				MaxMs: 200.0,
				AvgMs: 50.0,
				P50Ms: 45.0,
				P95Ms: 190.0,
				P99Ms: 198.0,
			},
		},
	}

	out := report.ExportPrometheus()

	// HELP and TYPE
	if !strings.Contains(out, "# HELP ntm_operation_duration_ms") {
		t.Error("expected HELP for ntm_operation_duration_ms")
	}
	if !strings.Contains(out, "# TYPE ntm_operation_duration_ms summary") {
		t.Error("expected TYPE summary for ntm_operation_duration_ms")
	}
	// Quantiles
	if !strings.Contains(out, `quantile="0.5"} 45.00`) {
		t.Error("expected P50 quantile 45.00")
	}
	if !strings.Contains(out, `quantile="0.95"} 190.00`) {
		t.Error("expected P95 quantile 190.00")
	}
	if !strings.Contains(out, `quantile="0.99"} 198.00`) {
		t.Error("expected P99 quantile 198.00")
	}
	// Count, min, max, avg
	if !strings.Contains(out, `_count{session="proj-1",operation="cm_query"} 10`) {
		t.Error("expected count 10")
	}
	if !strings.Contains(out, `_min{session="proj-1",operation="cm_query"} 5.00`) {
		t.Error("expected min 5.00")
	}
	if !strings.Contains(out, `_max{session="proj-1",operation="cm_query"} 200.00`) {
		t.Error("expected max 200.00")
	}
	if !strings.Contains(out, `_avg{session="proj-1",operation="cm_query"} 50.00`) {
		t.Error("expected avg 50.00")
	}
}

func TestExportPrometheus_BlockedAndConflicts(t *testing.T) {
	t.Parallel()
	report := &MetricsReport{
		SessionID:       "sess",
		APICallCounts:   map[string]int64{},
		LatencyStats:    map[string]LatencyStats{},
		BlockedCommands: 7,
		FileConflicts:   3,
	}

	out := report.ExportPrometheus()

	if !strings.Contains(out, `ntm_blocked_commands_total{session="sess"} 7`) {
		t.Errorf("expected blocked_commands=7, output:\n%s", out)
	}
	if !strings.Contains(out, `ntm_file_conflicts_total{session="sess"} 3`) {
		t.Errorf("expected file_conflicts=3, output:\n%s", out)
	}
}

func TestExportPrometheus_TargetComparison(t *testing.T) {
	t.Parallel()
	report := &MetricsReport{
		SessionID:     "sess",
		APICallCounts: map[string]int64{},
		LatencyStats:  map[string]LatencyStats{},
		TargetComparison: []TargetComparison{
			{Metric: "cm_query_latency_ms", Current: 45.0, Target: 50.0, Status: "met"},
			{Metric: "destructive_cmd_incidents", Current: 2.0, Target: 0.0, Status: "regressing"},
		},
	}

	out := report.ExportPrometheus()

	// HELP and TYPE for target metrics
	if !strings.Contains(out, "# HELP ntm_target_current") {
		t.Error("expected HELP for ntm_target_current")
	}
	if !strings.Contains(out, "# TYPE ntm_target_current gauge") {
		t.Error("expected TYPE gauge for ntm_target_current")
	}
	if !strings.Contains(out, "# HELP ntm_target_goal") {
		t.Error("expected HELP for ntm_target_goal")
	}
	if !strings.Contains(out, "# TYPE ntm_target_goal gauge") {
		t.Error("expected TYPE gauge for ntm_target_goal")
	}

	// Check values
	if !strings.Contains(out, `metric="cm_query_latency_ms",status="met"} 45.00`) {
		t.Error("expected cm_query current value 45.00 with status met")
	}
	if !strings.Contains(out, `metric="cm_query_latency_ms"} 50.00`) {
		t.Error("expected cm_query target 50.00")
	}
	if !strings.Contains(out, `metric="destructive_cmd_incidents",status="regressing"} 2.00`) {
		t.Error("expected destructive_cmd_incidents current 2.00 with status regressing")
	}
	if !strings.Contains(out, `metric="destructive_cmd_incidents"} 0.00`) {
		t.Error("expected destructive_cmd_incidents target 0.00")
	}
}

func TestExportPrometheus_FullReport(t *testing.T) {
	t.Parallel()
	report := &MetricsReport{
		SessionID: "full-test",
		APICallCounts: map[string]int64{
			"bv:triage": 10,
		},
		LatencyStats: map[string]LatencyStats{
			"cm_query": {Count: 5, MinMs: 10, MaxMs: 100, AvgMs: 55, P50Ms: 50, P95Ms: 95, P99Ms: 99},
		},
		BlockedCommands: 1,
		FileConflicts:   2,
		TargetComparison: []TargetComparison{
			{Metric: "file_conflicts", Current: 2, Target: 0, Status: "regressing"},
		},
	}

	out := report.ExportPrometheus()

	// All sections present
	sections := []string{
		"ntm_api_calls_total",
		"ntm_operation_duration_ms",
		"ntm_blocked_commands_total",
		"ntm_file_conflicts_total",
		"ntm_target_current",
		"ntm_target_goal",
	}
	for _, s := range sections {
		if !strings.Contains(out, s) {
			t.Errorf("full report missing section %q", s)
		}
	}

	// Session label consistent
	if strings.Count(out, `session="full-test"`) < 4 {
		t.Error("expected session label on multiple metrics")
	}
}

func TestExportPrometheus_MultipleLatencyOps_Sorted(t *testing.T) {
	t.Parallel()
	report := &MetricsReport{
		SessionID:     "test",
		APICallCounts: map[string]int64{},
		LatencyStats: map[string]LatencyStats{
			"zebra_op": {Count: 1},
			"alpha_op": {Count: 2},
			"middle":   {Count: 3},
		},
	}

	out := report.ExportPrometheus()

	alphaIdx := strings.Index(out, "alpha_op")
	middleIdx := strings.Index(out, "middle")
	zebraIdx := strings.Index(out, "zebra_op")

	if alphaIdx < 0 || middleIdx < 0 || zebraIdx < 0 {
		t.Fatal("expected all three operations in output")
	}
	if alphaIdx > middleIdx || middleIdx > zebraIdx {
		t.Error("expected operations in alphabetical order")
	}
}

func TestSanitizeLabel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"alphanumeric", "hello123", "hello123"},
		{"with underscores", "foo_bar", "foo_bar"},
		{"with dashes", "foo-bar", "foo-bar"},
		{"with dots", "foo.bar", "foo.bar"},
		{"with slashes", "foo/bar", "foo/bar"},
		{"with colons", "bv:triage", "bv:triage"},
		{"with spaces", "foo bar", "foo bar"},
		{"special chars replaced", "foo@bar#baz", "foo_bar_baz"},
		{"empty string", "", ""},
		{"all special", "@#$%", "____"},
		{"mixed", "abc!def", "abc_def"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeLabel(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeLabel(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSortedKeys(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		m    map[string]int64
		want []string
	}{
		{"empty", map[string]int64{}, nil},
		{"single", map[string]int64{"a": 1}, []string{"a"}},
		{"already sorted", map[string]int64{"a": 1, "b": 2, "c": 3}, []string{"a", "b", "c"}},
		{"reverse order", map[string]int64{"c": 1, "b": 2, "a": 3}, []string{"a", "b", "c"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sortedKeys(tt.m)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("sortedKeys[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSortedStatKeys(t *testing.T) {
	t.Parallel()
	m := map[string]LatencyStats{
		"zebra": {Count: 1},
		"alpha": {Count: 2},
		"mid":   {Count: 3},
	}

	got := sortedStatKeys(m)
	want := []string{"alpha", "mid", "zebra"}

	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("sortedStatKeys[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestExportPrometheus_ZeroValues(t *testing.T) {
	t.Parallel()
	report := &MetricsReport{
		SessionID:       "zero",
		APICallCounts:   map[string]int64{},
		LatencyStats:    map[string]LatencyStats{},
		BlockedCommands: 0,
		FileConflicts:   0,
	}

	out := report.ExportPrometheus()

	if !strings.Contains(out, `ntm_blocked_commands_total{session="zero"} 0`) {
		t.Error("expected blocked_commands=0")
	}
	if !strings.Contains(out, `ntm_file_conflicts_total{session="zero"} 0`) {
		t.Error("expected file_conflicts=0")
	}
}

func TestSortedKeys_Empty(t *testing.T) {
	t.Parallel()
	got := sortedKeys(map[string]int64{})
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}

func TestSortedStatKeys_Empty(t *testing.T) {
	t.Parallel()
	got := sortedStatKeys(map[string]LatencyStats{})
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}
