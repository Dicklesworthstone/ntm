package metrics

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// A series that can fall must not be exported as a counter: PromQL reads every
// decline as a counter reset and invents traffic that never happened.
// ntm_file_conflicts_total did exactly that — it counts conflicts inside the
// tracker's rolling window, so it drops as old conflicts age out.
func TestExportedMetricTypesMatchSemantics(t *testing.T) {
	report := &MetricsReport{
		SessionID:       "sess",
		GeneratedAt:     time.Now(),
		APICallCounts:   map[string]int64{"bv:triage": 2},
		LatencyStats:    map[string]LatencyStats{"cm_query": {Count: 1, AvgMs: 5}},
		BlockedCommands: 3,
		FileConflicts:   4,
		TargetComparison: []TargetComparison{
			{Metric: "file_conflicts", Current: 4, Target: 0, Status: "not_met"},
		},
	}

	out := report.ExportPrometheus()

	declared := map[string]string{}
	for _, m := range regexp.MustCompile(`# TYPE (\S+) (\S+)`).FindAllStringSubmatch(out, -1) {
		declared[m[1]] = m[2]
	}

	want := map[string]string{
		"ntm_api_calls_total":        "counter", // cumulative per session
		"ntm_operation_duration_ms":  "summary",
		"ntm_blocked_commands_total": "counter", // rows only ever added
		"ntm_file_conflicts_recent":  "gauge",   // windowed: can fall
		"ntm_target_current":         "gauge",
		"ntm_target_goal":            "gauge",
	}

	for metric, wantType := range want {
		got, ok := declared[metric]
		if !ok {
			t.Errorf("%s was not exported", metric)
			continue
		}
		if got != wantType {
			t.Errorf("%s declared %q, want %q", metric, got, wantType)
		}
	}

	// Nothing windowed may carry the counter-implying _total suffix.
	if strings.Contains(out, "ntm_file_conflicts_total") {
		t.Error("the windowed conflict series still uses the _total suffix, which implies a counter")
	}
}

// Every exported sample must belong to a declared family, or scrapers see
// untyped series.
func TestEveryExportedSampleHasAType(t *testing.T) {
	report := &MetricsReport{
		SessionID:       "sess",
		GeneratedAt:     time.Now(),
		BlockedCommands: 1,
		FileConflicts:   1,
	}

	out := report.ExportPrometheus()

	declared := map[string]bool{}
	for _, m := range regexp.MustCompile(`# TYPE (\S+) \S+`).FindAllStringSubmatch(out, -1) {
		declared[m[1]] = true
	}

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name := line
		if i := strings.IndexAny(name, "{ "); i >= 0 {
			name = name[:i]
		}
		// Summary families legitimately export _count/_min/_max/_avg children.
		base := name
		for _, suffix := range []string{"_count", "_min", "_max", "_avg"} {
			base = strings.TrimSuffix(base, suffix)
		}
		if !declared[name] && !declared[base] {
			t.Errorf("sample %q has no declared # TYPE", name)
		}
	}
}
