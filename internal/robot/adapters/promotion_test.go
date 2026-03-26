package adapters

// promotion_test.go provides unit tests for incident promotion rules,
// incident creation, and incident summary computation.
//
// Bead: bd-j9jo3.9.8

import (
	"strings"
	"testing"
	"time"
)

func TestShouldPromote_CriticalSeverity(t *testing.T) {
	t.Parallel()

	alert := AlertItem{
		ID:       "alert-001",
		Type:     "minor_issue",
		Severity: "critical",
		Message:  "Critical alert",
	}

	shouldPromote, reason := ShouldPromote(alert, nil)

	if !shouldPromote {
		t.Error("expected critical severity to promote")
	}
	if reason != "critical_severity" {
		t.Errorf("expected reason=critical_severity, got %s", reason)
	}
	t.Logf("PROMOTION alert_id=%s severity=%s promoted=%v reason=%s", alert.ID, alert.Severity, shouldPromote, reason)
}

func TestShouldPromote_DurationExceeded(t *testing.T) {
	t.Parallel()

	alert := AlertItem{
		ID:         "alert-002",
		Type:       "stall_detected",
		Severity:   "warning",
		Message:    "Agent stalled",
		DurationMs: 35 * 60 * 1000, // 35 minutes
	}

	rule := &PromotionRule{
		MinDuration: 30 * time.Minute,
	}

	shouldPromote, reason := ShouldPromote(alert, rule)

	if !shouldPromote {
		t.Error("expected duration exceeded to promote")
	}
	if reason != "duration_exceeded" {
		t.Errorf("expected reason=duration_exceeded, got %s", reason)
	}
	t.Logf("PROMOTION alert_id=%s duration_ms=%d threshold_ms=%d promoted=%v",
		alert.ID, alert.DurationMs, rule.MinDuration.Milliseconds(), shouldPromote)
}

func TestShouldPromote_TypeMatch(t *testing.T) {
	t.Parallel()

	alert := AlertItem{
		ID:       "alert-003",
		Type:     "agent_crashed",
		Severity: "warning",
		Message:  "Agent crashed",
	}

	rule := DefaultPromotionRules()

	shouldPromote, reason := ShouldPromote(alert, rule)

	if !shouldPromote {
		t.Error("expected type match to promote")
	}
	if reason != "type_match" {
		t.Errorf("expected reason=type_match, got %s", reason)
	}
	t.Logf("PROMOTION alert_id=%s type=%s promoted=%v reason=%s", alert.ID, alert.Type, shouldPromote, reason)
}

func TestShouldPromote_RepeatedAlert(t *testing.T) {
	t.Parallel()

	alert := AlertItem{
		ID:         "alert-004",
		Type:       "minor_issue",
		Severity:   "info",
		Message:    "Repeated issue",
		Count:      5,
		DurationMs: 1000, // 1 second, below threshold
	}

	rule := &PromotionRule{
		MinDuration: 30 * time.Minute, // won't trigger
		RepeatCount: 3,
	}

	shouldPromote, reason := ShouldPromote(alert, rule)

	if !shouldPromote {
		t.Error("expected repeated alert to promote")
	}
	if reason != "repeated_alert" {
		t.Errorf("expected reason=repeated_alert, got %s", reason)
	}
	t.Logf("PROMOTION alert_id=%s count=%d threshold=%d promoted=%v", alert.ID, alert.Count, rule.RepeatCount, shouldPromote)
}

func TestShouldPromote_NoPromotion(t *testing.T) {
	t.Parallel()

	alert := AlertItem{
		ID:         "alert-005",
		Type:       "minor_issue",
		Severity:   "info",
		Message:    "Minor issue",
		DurationMs: 5 * 60 * 1000, // 5 minutes
		Count:      1,
	}

	rule := &PromotionRule{
		MinDuration: 30 * time.Minute,
		MinSeverity: SeverityCritical,
		RepeatCount: 3,
		Types:       []string{"agent_crashed"},
	}

	shouldPromote, reason := ShouldPromote(alert, rule)

	if shouldPromote {
		t.Errorf("expected no promotion, got reason=%s", reason)
	}
	if reason != "" {
		t.Errorf("expected empty reason, got %s", reason)
	}
	t.Logf("PROMOTION alert_id=%s promoted=%v (as expected)", alert.ID, shouldPromote)
}

func TestShouldPromote_NilRule(t *testing.T) {
	t.Parallel()

	alert := AlertItem{
		ID:       "alert-006",
		Type:     "agent_crashed", // in default rules
		Severity: "warning",
	}

	shouldPromote, reason := ShouldPromote(alert, nil)

	if !shouldPromote {
		t.Error("expected type match with default rules")
	}
	t.Logf("PROMOTION nil_rule alert_id=%s promoted=%v reason=%s", alert.ID, shouldPromote, reason)
}

func TestPromoteToIncident(t *testing.T) {
	t.Parallel()

	alert := AlertItem{
		ID:       "alert-007",
		Type:     "rotation_failed",
		Severity: "critical",
		Message:  "Agent rotation failed",
		Session:  "main",
		Pane:     "pane-1",
	}

	incident := PromoteToIncident(alert, "critical_severity")

	if incident == nil {
		t.Fatal("expected non-nil incident")
	}
	if !strings.HasPrefix(incident.ID, "inc-") {
		t.Errorf("expected incident ID to start with 'inc-', got %s", incident.ID)
	}
	if incident.Type != alert.Type {
		t.Errorf("expected type=%s, got %s", alert.Type, incident.Type)
	}
	if incident.Severity != "P0" { // critical -> P0
		t.Errorf("expected severity=P0 for critical, got %s", incident.Severity)
	}
	if incident.Status != "investigating" {
		t.Errorf("expected status=investigating, got %s", incident.Status)
	}
	if incident.Session != alert.Session {
		t.Errorf("expected session=%s, got %s", alert.Session, incident.Session)
	}
	if len(incident.RelatedAlerts) != 1 || incident.RelatedAlerts[0] != alert.ID {
		t.Errorf("expected related_alerts=[%s], got %v", alert.ID, incident.RelatedAlerts)
	}
	t.Logf("INCIDENT_CREATED id=%s type=%s severity=%s status=%s",
		incident.ID, incident.Type, incident.Severity, incident.Status)
}

func TestPromoteToIncident_SeverityMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		alertSeverity    string
		expectedIncident string
	}{
		{"critical", "P0"},
		{"error", "P1"},
		{"warning", "P2"},
		{"info", "P2"},
		{"", "P2"},
	}

	for _, tc := range tests {
		t.Run(tc.alertSeverity, func(t *testing.T) {
			alert := AlertItem{
				ID:       "alert-sev-" + tc.alertSeverity,
				Type:     "test_alert",
				Severity: tc.alertSeverity,
				Message:  "Test",
			}

			incident := PromoteToIncident(alert, "test")

			if incident.Severity != tc.expectedIncident {
				t.Errorf("alert_severity=%s: expected %s, got %s",
					tc.alertSeverity, tc.expectedIncident, incident.Severity)
			}
			t.Logf("SEVERITY_MAP alert=%s incident=%s", tc.alertSeverity, incident.Severity)
		})
	}
}

func TestGenerateIncidentID_Uniqueness(t *testing.T) {
	t.Parallel()

	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := GenerateIncidentID()
		if ids[id] {
			t.Errorf("duplicate incident ID generated: %s", id)
		}
		ids[id] = true
	}
	t.Logf("INCIDENT_ID generated %d unique IDs", len(ids))
}

func TestGenerateIncidentID_Format(t *testing.T) {
	t.Parallel()

	id := GenerateIncidentID()

	if !strings.HasPrefix(id, "inc-") {
		t.Errorf("expected prefix 'inc-', got %s", id)
	}
	parts := strings.Split(id, "-")
	if len(parts) != 3 {
		t.Errorf("expected format 'inc-YYYYMMDD-XXXX', got %s", id)
	}
	// Date part should be 8 digits
	if len(parts[1]) != 8 {
		t.Errorf("expected 8-digit date, got %s", parts[1])
	}
	t.Logf("INCIDENT_ID format=%s", id)
}

func TestComputeIncidentsSummary_Basic(t *testing.T) {
	t.Parallel()

	incidents := []IncidentItem{
		{ID: "inc-1", Severity: "P0", Type: "crash", Status: "investigating"},
		{ID: "inc-2", Severity: "P1", Type: "stall", Status: "mitigating"},
		{ID: "inc-3", Severity: "P0", Type: "crash", Status: "resolved"},
		{ID: "inc-4", Severity: "P2", Type: "quota", Status: "investigating"},
	}

	summary := ComputeIncidentsSummary(incidents)

	if summary == nil {
		t.Fatal("expected non-nil summary")
	}
	if summary.TotalActive != 3 { // 3 non-resolved
		t.Errorf("expected total_active=3, got %d", summary.TotalActive)
	}
	if summary.BySeverity["P0"] != 2 {
		t.Errorf("expected P0=2, got %d", summary.BySeverity["P0"])
	}
	if summary.BySeverity["P1"] != 1 {
		t.Errorf("expected P1=1, got %d", summary.BySeverity["P1"])
	}
	if summary.ByType["crash"] != 2 {
		t.Errorf("expected crash=2, got %d", summary.ByType["crash"])
	}
	if summary.ByStatus["investigating"] != 2 {
		t.Errorf("expected investigating=2, got %d", summary.ByStatus["investigating"])
	}
	t.Logf("INCIDENT_SUMMARY total_active=%d by_severity=%v by_type=%v by_status=%v",
		summary.TotalActive, summary.BySeverity, summary.ByType, summary.ByStatus)
}

func TestComputeIncidentsSummary_Empty(t *testing.T) {
	t.Parallel()

	summary := ComputeIncidentsSummary([]IncidentItem{})

	if summary == nil {
		t.Fatal("expected non-nil summary for empty input")
	}
	if summary.TotalActive != 0 {
		t.Errorf("expected total_active=0, got %d", summary.TotalActive)
	}
	if len(summary.BySeverity) != 0 {
		t.Errorf("expected empty by_severity, got %v", summary.BySeverity)
	}
	t.Logf("INCIDENT_SUMMARY empty_input total_active=%d", summary.TotalActive)
}

func TestComputeIncidentsSummary_AllResolved(t *testing.T) {
	t.Parallel()

	incidents := []IncidentItem{
		{ID: "inc-1", Severity: "P0", Status: "resolved"},
		{ID: "inc-2", Severity: "P1", Status: "resolved"},
	}

	summary := ComputeIncidentsSummary(incidents)

	if summary.TotalActive != 0 {
		t.Errorf("expected total_active=0 for all resolved, got %d", summary.TotalActive)
	}
	if summary.ByStatus["resolved"] != 2 {
		t.Errorf("expected resolved=2, got %d", summary.ByStatus["resolved"])
	}
	t.Logf("INCIDENT_SUMMARY all_resolved total_active=%d", summary.TotalActive)
}

func TestDefaultPromotionRules(t *testing.T) {
	t.Parallel()

	rules := DefaultPromotionRules()

	if rules == nil {
		t.Fatal("expected non-nil default rules")
	}
	if rules.MinDuration <= 0 {
		t.Error("expected positive min_duration")
	}
	if rules.MinSeverity == "" {
		t.Error("expected non-empty min_severity")
	}
	if len(rules.Types) == 0 {
		t.Error("expected non-empty types list")
	}
	if rules.RepeatCount <= 0 {
		t.Error("expected positive repeat_count")
	}
	t.Logf("DEFAULT_RULES min_duration=%s min_severity=%s types=%v repeat_count=%d",
		rules.MinDuration, rules.MinSeverity, rules.Types, rules.RepeatCount)
}
