package metrics

import (
	"fmt"
	"sort"
	"strings"
)

// ExportPrometheus renders the MetricsReport in Prometheus exposition format.
// Each metric uses the "ntm_" prefix for namespacing.
func (r *MetricsReport) ExportPrometheus() string {
	var b strings.Builder

	// Session label for all metrics
	session := sanitizeLabel(r.SessionID)

	// API call counts as counters
	if len(r.APICallCounts) > 0 {
		b.WriteString("# HELP ntm_api_calls_total Total API calls by operation.\n")
		b.WriteString("# TYPE ntm_api_calls_total counter\n")
		for _, op := range sortedKeys(r.APICallCounts) {
			count := r.APICallCounts[op]
			b.WriteString(fmt.Sprintf("ntm_api_calls_total{session=%q,operation=%q} %d\n",
				session, sanitizeLabel(op), count))
		}
		b.WriteByte('\n')
	}

	// Latency stats as summaries
	if len(r.LatencyStats) > 0 {
		b.WriteString("# HELP ntm_operation_duration_ms Operation latency in milliseconds.\n")
		b.WriteString("# TYPE ntm_operation_duration_ms summary\n")
		for _, op := range sortedStatKeys(r.LatencyStats) {
			s := r.LatencyStats[op]
			label := sanitizeLabel(op)
			b.WriteString(fmt.Sprintf("ntm_operation_duration_ms{session=%q,operation=%q,quantile=\"0.5\"} %.2f\n",
				session, label, s.P50Ms))
			b.WriteString(fmt.Sprintf("ntm_operation_duration_ms{session=%q,operation=%q,quantile=\"0.95\"} %.2f\n",
				session, label, s.P95Ms))
			b.WriteString(fmt.Sprintf("ntm_operation_duration_ms{session=%q,operation=%q,quantile=\"0.99\"} %.2f\n",
				session, label, s.P99Ms))
			b.WriteString(fmt.Sprintf("ntm_operation_duration_ms_count{session=%q,operation=%q} %d\n",
				session, label, s.Count))
			b.WriteString(fmt.Sprintf("ntm_operation_duration_ms_min{session=%q,operation=%q} %.2f\n",
				session, label, s.MinMs))
			b.WriteString(fmt.Sprintf("ntm_operation_duration_ms_max{session=%q,operation=%q} %.2f\n",
				session, label, s.MaxMs))
			b.WriteString(fmt.Sprintf("ntm_operation_duration_ms_avg{session=%q,operation=%q} %.2f\n",
				session, label, s.AvgMs))
		}
		b.WriteByte('\n')
	}

	// Blocked commands gauge
	b.WriteString("# HELP ntm_blocked_commands_total Total blocked destructive commands.\n")
	b.WriteString("# TYPE ntm_blocked_commands_total counter\n")
	b.WriteString(fmt.Sprintf("ntm_blocked_commands_total{session=%q} %d\n", session, r.BlockedCommands))
	b.WriteByte('\n')

	// File conflicts gauge
	b.WriteString("# HELP ntm_file_conflicts_total Total file reservation conflicts.\n")
	b.WriteString("# TYPE ntm_file_conflicts_total counter\n")
	b.WriteString(fmt.Sprintf("ntm_file_conflicts_total{session=%q} %d\n", session, r.FileConflicts))
	b.WriteByte('\n')

	// Target comparison as gauges
	if len(r.TargetComparison) > 0 {
		b.WriteString("# HELP ntm_target_current Current value of a tracked target metric.\n")
		b.WriteString("# TYPE ntm_target_current gauge\n")
		b.WriteString("# HELP ntm_target_goal Target threshold for a tracked metric.\n")
		b.WriteString("# TYPE ntm_target_goal gauge\n")
		for _, tc := range r.TargetComparison {
			metric := sanitizeLabel(tc.Metric)
			status := tc.Status
			b.WriteString(fmt.Sprintf("ntm_target_current{session=%q,metric=%q,status=%q} %.2f\n",
				session, metric, status, tc.Current))
			b.WriteString(fmt.Sprintf("ntm_target_goal{session=%q,metric=%q} %.2f\n",
				session, metric, tc.Target))
		}
	}

	return b.String()
}

// sanitizeLabel replaces characters invalid in Prometheus labels.
func sanitizeLabel(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' || r == '/' || r == ':' || r == ' ' {
			return r
		}
		return '_'
	}, s)
}

// sortedKeys returns map keys sorted alphabetically for deterministic output.
func sortedKeys(m map[string]int64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sortedStatKeys returns LatencyStats map keys sorted alphabetically.
func sortedStatKeys(m map[string]LatencyStats) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
