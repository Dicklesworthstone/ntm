package adapters

// normalize_test.go provides unit tests for signal normalization, source health
// computation, alert processing, and degraded feature detection.
//
// Bead: bd-j9jo3.9.8

import (
	"errors"
	"testing"
	"time"
)

func TestComputeSourceHealth_AllHealthy(t *testing.T) {
	t.Parallel()

	now := time.Now()
	results := []AdapterResult{
		{Name: "beads", Available: true, CollectedAt: now},
		{Name: "quota", Available: true, CollectedAt: now},
		{Name: "alerts", Available: true, CollectedAt: now},
	}

	config := DefaultSourceHealthConfig()
	health := ComputeSourceHealth(results, config, now)

	if health == nil {
		t.Fatal("expected non-nil health section")
	}
	if !health.AllFresh {
		t.Error("expected AllFresh=true for all healthy sources")
	}
	if len(health.Degraded) != 0 {
		t.Errorf("expected no degraded sources, got %v", health.Degraded)
	}
	if len(health.Sources) != 3 {
		t.Errorf("expected 3 sources, got %d", len(health.Sources))
	}
	for name, info := range health.Sources {
		if !info.Available {
			t.Errorf("source %s: expected Available=true", name)
		}
		if !info.Fresh {
			t.Errorf("source %s: expected Fresh=true", name)
		}
	}
	t.Logf("SOURCE_HEALTH all_fresh=%v sources=%d", health.AllFresh, len(health.Sources))
}

func TestComputeSourceHealth_OneDegraded(t *testing.T) {
	t.Parallel()

	now := time.Now()
	staleTime := now.Add(-2 * time.Minute) // stale after 30s default
	results := []AdapterResult{
		{Name: "beads", Available: true, CollectedAt: now},
		{Name: "quota", Available: true, CollectedAt: staleTime}, // stale
		{Name: "alerts", Available: true, CollectedAt: now},
	}

	config := DefaultSourceHealthConfig()
	health := ComputeSourceHealth(results, config, now)

	if health == nil {
		t.Fatal("expected non-nil health section")
	}
	if health.AllFresh {
		t.Error("expected AllFresh=false when one source is stale")
	}
	if len(health.Degraded) != 1 {
		t.Errorf("expected 1 degraded source, got %d", len(health.Degraded))
	}
	if len(health.Degraded) > 0 && health.Degraded[0] != "quota" {
		t.Errorf("expected 'quota' to be degraded, got %v", health.Degraded)
	}
	quotaInfo := health.Sources["quota"]
	if quotaInfo.Fresh {
		t.Error("expected quota source to be not fresh")
	}
	t.Logf("SOURCE_HEALTH degraded=%v stale_source=%s", health.Degraded, "quota")
}

func TestComputeSourceHealth_OneUnavailable(t *testing.T) {
	t.Parallel()

	now := time.Now()
	results := []AdapterResult{
		{Name: "beads", Available: true, CollectedAt: now},
		{Name: "quota", Available: false, Error: errors.New("connection refused"), CollectedAt: now},
		{Name: "alerts", Available: true, CollectedAt: now},
	}

	config := DefaultSourceHealthConfig()
	health := ComputeSourceHealth(results, config, now)

	if health == nil {
		t.Fatal("expected non-nil health section")
	}
	if health.AllFresh {
		t.Error("expected AllFresh=false when one source unavailable")
	}
	if len(health.Degraded) != 1 {
		t.Errorf("expected 1 degraded source, got %d", len(health.Degraded))
	}
	quotaInfo := health.Sources["quota"]
	if quotaInfo.Available {
		t.Error("expected quota source to be unavailable")
	}
	if quotaInfo.LastError == "" {
		t.Error("expected last_error to be set")
	}
	t.Logf("SOURCE_HEALTH unavailable=%v error=%s", health.Degraded, quotaInfo.LastError)
}

func TestComputeSourceHealth_AllUnavailable(t *testing.T) {
	t.Parallel()

	now := time.Now()
	results := []AdapterResult{
		{Name: "beads", Available: false, Error: errors.New("db locked")},
		{Name: "quota", Available: false, Error: errors.New("connection refused")},
	}

	config := DefaultSourceHealthConfig()
	health := ComputeSourceHealth(results, config, now)

	if health == nil {
		t.Fatal("expected non-nil health section")
	}
	if health.AllFresh {
		t.Error("expected AllFresh=false when all sources unavailable")
	}
	if len(health.Degraded) != 2 {
		t.Errorf("expected 2 degraded sources, got %d", len(health.Degraded))
	}
	t.Logf("SOURCE_HEALTH all_unavailable degraded=%v", health.Degraded)
}

func TestComputeSourceHealth_EmptyResults(t *testing.T) {
	t.Parallel()

	now := time.Now()
	results := []AdapterResult{}

	config := DefaultSourceHealthConfig()
	health := ComputeSourceHealth(results, config, now)

	if health == nil {
		t.Fatal("expected non-nil health section")
	}
	if len(health.Sources) != 0 {
		t.Errorf("expected 0 sources for empty results, got %d", len(health.Sources))
	}
	if !health.AllFresh {
		t.Error("expected AllFresh=true for empty (vacuously true)")
	}
	t.Logf("SOURCE_HEALTH empty_results sources=%d", len(health.Sources))
}

func TestComputeDegradedFeatures_Beads(t *testing.T) {
	t.Parallel()

	health := &SourceHealthSection{
		Sources:  map[string]SourceInfo{"beads": {Name: "beads", Degraded: true}},
		Degraded: []string{"beads"},
		AllFresh: false,
	}

	features := ComputeDegradedFeatures(health)

	if len(features) == 0 {
		t.Fatal("expected at least one degraded feature")
	}

	foundWorkSection := false
	for _, f := range features {
		if f.Feature == "work_section" || f.Feature == "bead_triage" || f.Feature == "dependency_graph" {
			foundWorkSection = true
			if len(f.AffectedBy) == 0 {
				t.Errorf("feature %s: expected affected_by to be non-empty", f.Feature)
			}
		}
	}
	if !foundWorkSection {
		t.Error("expected work-related feature to be degraded when beads source is degraded")
	}
	t.Logf("DEGRADED_FEATURES count=%d", len(features))
}

func TestComputeDegradedFeatures_Healthy(t *testing.T) {
	t.Parallel()

	health := &SourceHealthSection{
		Sources:  map[string]SourceInfo{"beads": {Name: "beads", Available: true, Fresh: true}},
		Degraded: []string{},
		AllFresh: true,
	}

	features := ComputeDegradedFeatures(health)

	if len(features) != 0 {
		t.Errorf("expected no degraded features for healthy status, got %d", len(features))
	}
	t.Logf("DEGRADED_FEATURES healthy_source count=%d", len(features))
}

func TestComputeDegradedFeatures_NilHealth(t *testing.T) {
	t.Parallel()

	features := ComputeDegradedFeatures(nil)

	if features != nil && len(features) != 0 {
		t.Errorf("expected nil or empty slice for nil health, got %d", len(features))
	}
	t.Logf("DEGRADED_FEATURES nil_health")
}

func TestComputeDegradedFeatures_TmuxDegraded(t *testing.T) {
	t.Parallel()

	health := &SourceHealthSection{
		Sources:  map[string]SourceInfo{"tmux": {Name: "tmux", Degraded: true}},
		Degraded: []string{"tmux"},
		AllFresh: false,
	}

	features := ComputeDegradedFeatures(health)

	foundCritical := false
	for _, f := range features {
		if f.Feature == "session_list" || f.Feature == "agent_detection" {
			foundCritical = true
			if f.Severity != "error" {
				t.Errorf("feature %s: expected severity=error for critical features, got %s", f.Feature, f.Severity)
			}
		}
	}
	if !foundCritical {
		t.Error("expected critical features to be degraded when tmux source is degraded")
	}
	t.Logf("DEGRADED_FEATURES tmux_degraded count=%d", len(features))
}

func TestComputeQuotaReasonCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		usagePercent float64
		expected     ReasonCode
	}{
		{"zero_usage", 0, ReasonQuotaOK},
		{"low_usage", 50, ReasonQuotaOK},
		{"warning_threshold", 80, ReasonQuotaWarningTokens},
		{"high_usage", 90, ReasonQuotaWarningTokens},
		{"critical_threshold", 95, ReasonQuotaCriticalTokens},
		{"exceeded", 100, ReasonQuotaExceededTokens},
		{"over_limit", 105, ReasonQuotaExceededTokens},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := computeQuotaReasonCode(tc.usagePercent)
			if result != tc.expected {
				t.Errorf("usagePercent=%.1f: expected %q, got %q", tc.usagePercent, tc.expected, result)
			}
			t.Logf("QUOTA_REASON usage=%.1f code=%q", tc.usagePercent, result)
		})
	}
}

func TestReasonToStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		code     ReasonCode
		expected string
	}{
		{ReasonQuotaExceededTokens, "exceeded"},
		{ReasonQuotaExceededRequests, "exceeded"},
		{ReasonQuotaSuspended, "exceeded"},
		{ReasonQuotaCriticalTokens, "critical"},
		{ReasonQuotaWarningTokens, "warning"},
		{ReasonQuotaWarningRequests, "warning"},
		{ReasonQuotaOK, "ok"},
		{"", "ok"},
		{"unknown_code", "ok"},
	}

	for _, tc := range tests {
		t.Run(string(tc.code), func(t *testing.T) {
			result := reasonToStatus(tc.code)
			if result != tc.expected {
				t.Errorf("code=%q: expected %q, got %q", tc.code, tc.expected, result)
			}
		})
	}
}

func TestFormatTimestamp(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 3, 26, 15, 30, 45, 0, time.UTC)
	result := FormatTimestamp(ts)

	if result != "2026-03-26T15:30:45Z" {
		t.Errorf("expected RFC3339 format, got %s", result)
	}
	t.Logf("FORMAT_TIMESTAMP input=%v output=%s", ts, result)
}

func TestMatchesPattern(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pattern  string
		value    string
		expected bool
	}{
		{"*", "anything", true},
		{"beads", "beads", true},
		{"beads", "quota", false},
		{"beads*", "beads_v2", true},
		{"*beads", "my_beads", true},
		{"", "", false},          // empty pattern returns false
		{"", "something", false}, // empty pattern returns false
		{"pattern", "", false},   // empty value returns false
	}

	for _, tc := range tests {
		t.Run(tc.pattern+"_"+tc.value, func(t *testing.T) {
			result := matchesPattern(tc.pattern, tc.value)
			if result != tc.expected {
				t.Errorf("pattern=%q value=%q: expected %v, got %v", tc.pattern, tc.value, tc.expected, result)
			}
		})
	}
}

func TestSourceHealthReasonCodes_Unavailable(t *testing.T) {
	t.Parallel()

	now := time.Now()
	config := DefaultSourceHealthConfig()

	// Test unavailable source gets correct reason code
	health := ComputeSourceHealth([]AdapterResult{
		{Name: "test", Available: false},
	}, config, now)

	if info, ok := health.Sources["test"]; !ok {
		t.Fatal("missing test source")
	} else if info.ReasonCode != ReasonHealthSourceUnavailable {
		t.Errorf("ReasonCode = %q, want %q", info.ReasonCode, ReasonHealthSourceUnavailable)
	}
	t.Logf("SOURCE_HEALTH_REASON unavailable reason_code=%s", health.Sources["test"].ReasonCode)
}

func TestSourceHealthReasonCodes_Stale(t *testing.T) {
	t.Parallel()

	now := time.Now()
	staleTime := now.Add(-2 * time.Minute)
	config := DefaultSourceHealthConfig()

	health := ComputeSourceHealth([]AdapterResult{
		{Name: "test", Available: true, CollectedAt: staleTime},
	}, config, now)

	if info, ok := health.Sources["test"]; !ok {
		t.Fatal("missing test source")
	} else if info.ReasonCode != ReasonHealthSourceStale {
		t.Errorf("ReasonCode = %q, want %q", info.ReasonCode, ReasonHealthSourceStale)
	}
	t.Logf("SOURCE_HEALTH_REASON stale reason_code=%s", health.Sources["test"].ReasonCode)
}

func TestSourceHealthReasonCodes_CollectionError(t *testing.T) {
	t.Parallel()

	now := time.Now()
	config := DefaultSourceHealthConfig()

	// When there's an error, the source becomes unavailable
	health := ComputeSourceHealth([]AdapterResult{
		{Name: "test", Available: true, Error: errors.New("timeout"), CollectedAt: now},
	}, config, now)

	if info, ok := health.Sources["test"]; !ok {
		t.Fatal("missing test source")
	} else if info.ReasonCode != ReasonHealthSourceUnavailable {
		// Collection error sets Available=false, so gets unavailable code
		t.Errorf("ReasonCode = %q, want %q", info.ReasonCode, ReasonHealthSourceUnavailable)
	}
	if info := health.Sources["test"]; info.LastError == "" {
		t.Error("expected LastError to be set")
	}
	t.Logf("SOURCE_HEALTH_REASON collection_error reason_code=%s last_error=%s",
		health.Sources["test"].ReasonCode, health.Sources["test"].LastError)
}
