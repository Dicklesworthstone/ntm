package alerts

import (
	"strings"
	"testing"
	"time"
)

func TestEmitContextWarning(t *testing.T) {
	// Clear global tracker first
	tracker := GetGlobalTracker()
	tracker.Clear()

	data := RotationAlertData{
		AgentID:      "test-agent",
		Session:      "test-session",
		Pane:         "test-pane",
		ContextUsage: 85.0,
	}

	EmitContextWarning(data)

	// Check alert was created
	active := tracker.GetActive()
	if len(active) != 1 {
		t.Fatalf("expected 1 active alert, got %d", len(active))
	}

	alert := active[0]
	if alert.Type != AlertContextWarning {
		t.Errorf("expected type %s, got %s", AlertContextWarning, alert.Type)
	}
	if alert.Severity != SeverityWarning {
		t.Errorf("expected severity %s, got %s", SeverityWarning, alert.Severity)
	}
	if alert.Source != "context_rotation" {
		t.Errorf("expected source 'context_rotation', got %s", alert.Source)
	}
	if alert.Session != "test-session" {
		t.Errorf("expected session 'test-session', got %s", alert.Session)
	}
	if alert.Pane != "test-pane" {
		t.Errorf("expected pane 'test-pane', got %s", alert.Pane)
	}
	if !strings.Contains(alert.Message, "85%") {
		t.Errorf("expected message to contain '85%%', got %s", alert.Message)
	}
	if alert.Context["agent_id"] != "test-agent" {
		t.Errorf("expected context agent_id 'test-agent', got %v", alert.Context["agent_id"])
	}
}

func TestEmitRotationStarted(t *testing.T) {
	tracker := GetGlobalTracker()
	tracker.Clear()

	data := RotationAlertData{
		AgentID:      "rotating-agent",
		Session:      "session-1",
		Pane:         "pane-1",
		ContextUsage: 92.0,
	}

	EmitRotationStarted(data)

	active := tracker.GetActive()
	if len(active) != 1 {
		t.Fatalf("expected 1 active alert, got %d", len(active))
	}

	alert := active[0]
	if alert.Type != AlertRotationStarted {
		t.Errorf("expected type %s, got %s", AlertRotationStarted, alert.Type)
	}
	if alert.Severity != SeverityInfo {
		t.Errorf("expected severity %s, got %s", SeverityInfo, alert.Severity)
	}
	if !strings.Contains(alert.Message, "Rotating agent rotating-agent") {
		t.Errorf("expected message to contain agent name, got %s", alert.Message)
	}
	if !strings.Contains(alert.Message, "92%") {
		t.Errorf("expected message to contain '92%%', got %s", alert.Message)
	}
}

func TestEmitRotationComplete(t *testing.T) {
	tracker := GetGlobalTracker()
	tracker.Clear()

	// First emit a context warning and rotation started
	warningData := RotationAlertData{
		AgentID:      "old-agent",
		Session:      "session-1",
		ContextUsage: 90.0,
	}
	EmitContextWarning(warningData)

	startedData := RotationAlertData{
		AgentID:      "old-agent",
		Session:      "session-1",
		ContextUsage: 95.0,
	}
	EmitRotationStarted(startedData)

	// Verify both alerts are active
	active := tracker.GetActive()
	if len(active) != 2 {
		t.Fatalf("expected 2 active alerts before complete, got %d", len(active))
	}

	// Now emit rotation complete
	completeData := RotationAlertData{
		OldAgentID:    "old-agent",
		NewAgentID:    "new-agent",
		Session:       "session-1",
		SummaryTokens: 500,
		DurationMs:    2000,
	}
	EmitRotationComplete(completeData)

	// The started and warning alerts should be resolved
	active = tracker.GetActive()
	if len(active) != 1 {
		t.Fatalf("expected 1 active alert after complete (the complete alert), got %d", len(active))
	}

	// Check the complete alert
	alert := active[0]
	if alert.Type != AlertRotationComplete {
		t.Errorf("expected type %s, got %s", AlertRotationComplete, alert.Type)
	}
	if alert.Severity != SeverityInfo {
		t.Errorf("expected severity %s, got %s", SeverityInfo, alert.Severity)
	}
	if !strings.Contains(alert.Message, "old-agent") || !strings.Contains(alert.Message, "new-agent") {
		t.Errorf("expected message to contain both agent names, got %s", alert.Message)
	}
	if alert.Context["old_agent_id"] != "old-agent" {
		t.Errorf("expected context old_agent_id 'old-agent', got %v", alert.Context["old_agent_id"])
	}
	if alert.Context["new_agent_id"] != "new-agent" {
		t.Errorf("expected context new_agent_id 'new-agent', got %v", alert.Context["new_agent_id"])
	}
	if alert.Context["summary_tokens"] != 500 {
		t.Errorf("expected context summary_tokens 500, got %v", alert.Context["summary_tokens"])
	}

	// Check resolved alerts
	resolved := tracker.GetResolved()
	if len(resolved) != 2 {
		t.Errorf("expected 2 resolved alerts, got %d", len(resolved))
	}
}

func TestEmitRotationFailed(t *testing.T) {
	tracker := GetGlobalTracker()
	tracker.Clear()

	// First emit a rotation started
	startedData := RotationAlertData{
		AgentID:      "failing-agent",
		Session:      "session-1",
		ContextUsage: 95.0,
	}
	EmitRotationStarted(startedData)

	// Verify started alert is active
	active := tracker.GetActive()
	if len(active) != 1 {
		t.Fatalf("expected 1 active alert before failure, got %d", len(active))
	}

	// Now emit rotation failed
	failedData := RotationAlertData{
		AgentID:      "failing-agent",
		Session:      "session-1",
		ContextUsage: 95.0,
		Error:        "connection timeout",
		DurationMs:   5000,
	}
	EmitRotationFailed(failedData)

	// The started alert should be resolved, failed alert should be active
	active = tracker.GetActive()
	if len(active) != 1 {
		t.Fatalf("expected 1 active alert after failure (the failed alert), got %d", len(active))
	}

	alert := active[0]
	if alert.Type != AlertRotationFailed {
		t.Errorf("expected type %s, got %s", AlertRotationFailed, alert.Type)
	}
	if alert.Severity != SeverityError {
		t.Errorf("expected severity %s, got %s", SeverityError, alert.Severity)
	}
	if !strings.Contains(alert.Message, "connection timeout") {
		t.Errorf("expected message to contain error, got %s", alert.Message)
	}
	if alert.Context["error"] != "connection timeout" {
		t.Errorf("expected context error 'connection timeout', got %v", alert.Context["error"])
	}

	// Check resolved alerts
	resolved := tracker.GetResolved()
	if len(resolved) != 1 {
		t.Errorf("expected 1 resolved alert (started), got %d", len(resolved))
	}
}

func TestNewRotationEventOutput(t *testing.T) {
	data := RotationAlertData{
		AgentID:       "agent-1",
		OldAgentID:    "old-agent",
		NewAgentID:    "new-agent",
		Session:       "test-session",
		ContextUsage:  88.5,
		SummaryTokens: 1000,
		DurationMs:    3500,
		Error:         "",
	}

	output := NewRotationEventOutput(data, "completed")

	if output.Type != "context_rotation" {
		t.Errorf("expected type 'context_rotation', got %s", output.Type)
	}
	if output.OldAgent != "old-agent" {
		t.Errorf("expected OldAgent 'old-agent', got %s", output.OldAgent)
	}
	if output.NewAgent != "new-agent" {
		t.Errorf("expected NewAgent 'new-agent', got %s", output.NewAgent)
	}
	if output.UsagePercent != 88.5 {
		t.Errorf("expected UsagePercent 88.5, got %f", output.UsagePercent)
	}
	if output.SummaryTokens != 1000 {
		t.Errorf("expected SummaryTokens 1000, got %d", output.SummaryTokens)
	}
	if output.Status != "completed" {
		t.Errorf("expected Status 'completed', got %s", output.Status)
	}
	if output.DurationMs != 3500 {
		t.Errorf("expected DurationMs 3500, got %d", output.DurationMs)
	}
	if output.SessionName != "test-session" {
		t.Errorf("expected SessionName 'test-session', got %s", output.SessionName)
	}
	if output.GeneratedAt == "" {
		t.Error("expected GeneratedAt to be set")
	}

	// Verify GeneratedAt is valid RFC3339
	_, err := time.Parse(time.RFC3339, output.GeneratedAt)
	if err != nil {
		t.Errorf("expected GeneratedAt to be valid RFC3339, got parse error: %v", err)
	}
}

func TestNewRotationEventOutputStartedUsesAgentIDFallback(t *testing.T) {
	data := RotationAlertData{
		AgentID:      "agent-1",
		Session:      "test-session",
		ContextUsage: 88.5,
	}

	output := NewRotationEventOutput(data, "started")

	if output.OldAgent != "agent-1" {
		t.Errorf("expected OldAgent fallback 'agent-1', got %s", output.OldAgent)
	}
	if output.NewAgent != "" {
		t.Errorf("expected NewAgent to be empty for started event, got %s", output.NewAgent)
	}
}

func TestNewRotationEventOutputWithError(t *testing.T) {
	data := RotationAlertData{
		AgentID:      "agent-1",
		OldAgentID:   "failing-agent",
		Session:      "test-session",
		ContextUsage: 95.0,
		Error:        "failed to connect",
		DurationMs:   1500,
	}

	output := NewRotationEventOutput(data, "failed")

	if output.Status != "failed" {
		t.Errorf("expected Status 'failed', got %s", output.Status)
	}
	if output.Error != "failed to connect" {
		t.Errorf("expected Error 'failed to connect', got %s", output.Error)
	}
	if output.NewAgent != "" {
		t.Errorf("expected NewAgent to be empty on failure, got %s", output.NewAgent)
	}
}

func TestEmitRotationFailedUsesOldAgentFallback(t *testing.T) {
	tracker := GetGlobalTracker()
	tracker.Clear()

	EmitRotationStarted(RotationAlertData{
		AgentID:      "old-agent",
		Session:      "session-1",
		ContextUsage: 94.0,
	})

	EmitRotationFailed(RotationAlertData{
		OldAgentID:   "old-agent",
		Session:      "session-1",
		ContextUsage: 95.0,
		Error:        "handoff timeout",
	})

	active := tracker.GetActive()
	if len(active) != 1 {
		t.Fatalf("expected 1 active alert after failure, got %d", len(active))
	}
	if active[0].Type != AlertRotationFailed {
		t.Fatalf("expected active alert type %s, got %s", AlertRotationFailed, active[0].Type)
	}
	if active[0].Context["agent_id"] != "old-agent" {
		t.Fatalf("expected fallback context agent_id 'old-agent', got %v", active[0].Context["agent_id"])
	}

	resolved := tracker.GetResolved()
	if len(resolved) != 1 {
		t.Fatalf("expected started alert to resolve via fallback agent id, got %d resolved alerts", len(resolved))
	}
	if resolved[0].Type != AlertRotationStarted {
		t.Fatalf("expected resolved alert type %s, got %s", AlertRotationStarted, resolved[0].Type)
	}
}

func TestContextWarningThresholdConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.ContextWarningThreshold != 75.0 {
		t.Errorf("expected default ContextWarningThreshold 75.0, got %f", cfg.ContextWarningThreshold)
	}
}

func TestRotationAlertIDConsistency(t *testing.T) {
	// Verify that the same alert type/session/agent produces the same ID
	// This is important for alert resolution to work correctly
	tracker := GetGlobalTracker()
	tracker.Clear()

	data1 := RotationAlertData{
		AgentID: "agent-x",
		Session: "session-y",
	}
	EmitRotationStarted(data1)

	active1 := tracker.GetActive()
	if len(active1) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(active1))
	}
	id1 := active1[0].ID

	// Clear and emit again - should get same ID
	tracker.Clear()
	EmitRotationStarted(data1)

	active2 := tracker.GetActive()
	if len(active2) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(active2))
	}
	id2 := active2[0].ID

	if id1 != id2 {
		t.Errorf("expected consistent alert ID, got %s vs %s", id1, id2)
	}
}
