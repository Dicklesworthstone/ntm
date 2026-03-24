package alerts

import (
	"strings"
	"testing"
	"time"
)

func TestEmitCompactionTriggered(t *testing.T) {
	// Clear global tracker first
	tracker := GetGlobalTracker()
	tracker.Clear()

	data := CompactionAlertData{
		AgentID:        "test-agent",
		Session:        "test-session",
		Pane:           "test-pane",
		ContextUsage:   85.0,
		MinutesToLimit: 12.5,
	}

	EmitCompactionTriggered(data)

	// Check alert was created
	active := tracker.GetActive()
	if len(active) != 1 {
		t.Fatalf("expected 1 active alert, got %d", len(active))
	}

	alert := active[0]
	if alert.Type != AlertCompactionTriggered {
		t.Errorf("expected type %s, got %s", AlertCompactionTriggered, alert.Type)
	}
	if alert.Severity != SeverityInfo {
		t.Errorf("expected severity %s, got %s", SeverityInfo, alert.Severity)
	}
	if alert.Source != "context_compaction" {
		t.Errorf("expected source 'context_compaction', got %s", alert.Source)
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
	if !strings.Contains(alert.Message, "12.5") {
		t.Errorf("expected message to contain '12.5', got %s", alert.Message)
	}
	if alert.Context["agent_id"] != "test-agent" {
		t.Errorf("expected context agent_id 'test-agent', got %v", alert.Context["agent_id"])
	}
	if alert.Context["context_usage"] != 85.0 {
		t.Errorf("expected context context_usage 85.0, got %v", alert.Context["context_usage"])
	}
	if alert.Context["minutes_to_limit"] != 12.5 {
		t.Errorf("expected context minutes_to_limit 12.5, got %v", alert.Context["minutes_to_limit"])
	}
}

func TestEmitCompactionComplete(t *testing.T) {
	tracker := GetGlobalTracker()
	tracker.Clear()

	// First emit a context warning and compaction triggered
	warningData := RotationAlertData{
		AgentID:      "compacting-agent",
		Session:      "session-1",
		ContextUsage: 85.0,
	}
	EmitContextWarning(warningData)

	triggeredData := CompactionAlertData{
		AgentID:        "compacting-agent",
		Session:        "session-1",
		ContextUsage:   87.0,
		MinutesToLimit: 10.0,
	}
	EmitCompactionTriggered(triggeredData)

	// Verify both alerts are active
	active := tracker.GetActive()
	if len(active) != 2 {
		t.Fatalf("expected 2 active alerts before complete, got %d", len(active))
	}

	// Now emit compaction complete
	completeData := CompactionAlertData{
		AgentID:      "compacting-agent",
		Session:      "session-1",
		Pane:         "pane-1",
		ContextUsage: 87.0,
		Method:       "builtin",
		TokensBefore: 180000,
		TokensAfter:  50000,
		UsageBefore:  90.0,
		UsageAfter:   25.0,
		DurationMs:   2500,
	}
	EmitCompactionComplete(completeData)

	// The triggered and warning alerts should be resolved
	active = tracker.GetActive()
	if len(active) != 1 {
		t.Fatalf("expected 1 active alert after complete (the complete alert), got %d", len(active))
	}

	// Check the complete alert
	alert := active[0]
	if alert.Type != AlertCompactionComplete {
		t.Errorf("expected type %s, got %s", AlertCompactionComplete, alert.Type)
	}
	if alert.Severity != SeverityInfo {
		t.Errorf("expected severity %s, got %s", SeverityInfo, alert.Severity)
	}
	if !strings.Contains(alert.Message, "90%") {
		t.Errorf("expected message to contain usage before '90%%', got %s", alert.Message)
	}
	if !strings.Contains(alert.Message, "25%") {
		t.Errorf("expected message to contain usage after '25%%', got %s", alert.Message)
	}
	if alert.Context["agent_id"] != "compacting-agent" {
		t.Errorf("expected context agent_id 'compacting-agent', got %v", alert.Context["agent_id"])
	}
	if alert.Context["method"] != "builtin" {
		t.Errorf("expected context method 'builtin', got %v", alert.Context["method"])
	}
	if alert.Context["tokens_before"] != int64(180000) {
		t.Errorf("expected context tokens_before 180000, got %v", alert.Context["tokens_before"])
	}
	if alert.Context["tokens_after"] != int64(50000) {
		t.Errorf("expected context tokens_after 50000, got %v", alert.Context["tokens_after"])
	}

	// Check resolved alerts
	resolved := tracker.GetResolved()
	if len(resolved) != 2 {
		t.Errorf("expected 2 resolved alerts, got %d", len(resolved))
	}
}

func TestEmitCompactionFailed(t *testing.T) {
	tracker := GetGlobalTracker()
	tracker.Clear()

	// First emit a compaction triggered
	triggeredData := CompactionAlertData{
		AgentID:        "failing-agent",
		Session:        "session-1",
		ContextUsage:   90.0,
		MinutesToLimit: 8.0,
	}
	EmitCompactionTriggered(triggeredData)

	// Verify triggered alert is active
	active := tracker.GetActive()
	if len(active) != 1 {
		t.Fatalf("expected 1 active alert before failure, got %d", len(active))
	}

	// Now emit compaction failed
	failedData := CompactionAlertData{
		AgentID:      "failing-agent",
		Session:      "session-1",
		Pane:         "pane-1",
		ContextUsage: 90.0,
		Method:       "builtin",
		Error:        "command not supported",
		DurationMs:   1500,
	}
	EmitCompactionFailed(failedData)

	// The triggered alert should be resolved, failed alert should be active
	active = tracker.GetActive()
	if len(active) != 1 {
		t.Fatalf("expected 1 active alert after failure (the failed alert), got %d", len(active))
	}

	alert := active[0]
	if alert.Type != AlertCompactionFailed {
		t.Errorf("expected type %s, got %s", AlertCompactionFailed, alert.Type)
	}
	if alert.Severity != SeverityWarning {
		t.Errorf("expected severity %s, got %s", SeverityWarning, alert.Severity)
	}
	if !strings.Contains(alert.Message, "command not supported") {
		t.Errorf("expected message to contain error, got %s", alert.Message)
	}
	if alert.Context["error"] != "command not supported" {
		t.Errorf("expected context error 'command not supported', got %v", alert.Context["error"])
	}
	if alert.Context["method"] != "builtin" {
		t.Errorf("expected context method 'builtin', got %v", alert.Context["method"])
	}

	// Check resolved alerts
	resolved := tracker.GetResolved()
	if len(resolved) != 1 {
		t.Errorf("expected 1 resolved alert (triggered), got %d", len(resolved))
	}
}

func TestNewCompactionEventOutput(t *testing.T) {
	data := CompactionAlertData{
		AgentID:      "agent-1",
		Session:      "test-session",
		Pane:         "pane-1",
		ContextUsage: 88.5,
		Method:       "builtin",
		TokensBefore: 180000,
		TokensAfter:  50000,
		UsageBefore:  90.0,
		UsageAfter:   25.0,
		DurationMs:   3500,
	}

	output := NewCompactionEventOutput(data, "completed")

	if output.Type != "context_compaction" {
		t.Errorf("expected type 'context_compaction', got %s", output.Type)
	}
	if output.AgentID != "agent-1" {
		t.Errorf("expected AgentID 'agent-1', got %s", output.AgentID)
	}
	if output.Method != "builtin" {
		t.Errorf("expected Method 'builtin', got %s", output.Method)
	}
	if output.UsageBefore != 90.0 {
		t.Errorf("expected UsageBefore 90.0, got %f", output.UsageBefore)
	}
	if output.UsageAfter != 25.0 {
		t.Errorf("expected UsageAfter 25.0, got %f", output.UsageAfter)
	}
	if output.TokensBefore != 180000 {
		t.Errorf("expected TokensBefore 180000, got %d", output.TokensBefore)
	}
	if output.TokensAfter != 50000 {
		t.Errorf("expected TokensAfter 50000, got %d", output.TokensAfter)
	}
	if output.TokensReclaimed != 130000 {
		t.Errorf("expected TokensReclaimed 130000, got %d", output.TokensReclaimed)
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

func TestNewCompactionEventOutputWithError(t *testing.T) {
	data := CompactionAlertData{
		AgentID:      "agent-1",
		Session:      "test-session",
		ContextUsage: 95.0,
		Method:       "summarize",
		TokensBefore: 180000,
		TokensAfter:  50000,
		Error:        "failed to summarize context",
		DurationMs:   1500,
	}

	output := NewCompactionEventOutput(data, "failed")

	if output.Status != "failed" {
		t.Errorf("expected Status 'failed', got %s", output.Status)
	}
	if output.Error != "failed to summarize context" {
		t.Errorf("expected Error 'failed to summarize context', got %s", output.Error)
	}
	if output.Method != "summarize" {
		t.Errorf("expected Method 'summarize', got %s", output.Method)
	}
	if output.TokensReclaimed != 0 {
		t.Errorf("expected TokensReclaimed 0 on failed output, got %d", output.TokensReclaimed)
	}
}

func TestCompactionAlertIDConsistency(t *testing.T) {
	// Verify that the same alert type/session/agent produces the same ID
	// This is important for alert resolution to work correctly
	tracker := GetGlobalTracker()
	tracker.Clear()

	data1 := CompactionAlertData{
		AgentID:        "agent-x",
		Session:        "session-y",
		ContextUsage:   85.0,
		MinutesToLimit: 10.0,
	}
	EmitCompactionTriggered(data1)

	active1 := tracker.GetActive()
	if len(active1) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(active1))
	}
	id1 := active1[0].ID

	// Clear and emit again - should get same ID
	tracker.Clear()
	EmitCompactionTriggered(data1)

	active2 := tracker.GetActive()
	if len(active2) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(active2))
	}
	id2 := active2[0].ID

	if id1 != id2 {
		t.Errorf("expected consistent alert ID, got %s vs %s", id1, id2)
	}
}

func TestCompactionTriggeredMessageFormat(t *testing.T) {
	tracker := GetGlobalTracker()
	tracker.Clear()

	data := CompactionAlertData{
		AgentID:        "claude-agent-1",
		Session:        "main-session",
		Pane:           "pane-0",
		ContextUsage:   82.0,
		MinutesToLimit: 15.5,
	}

	EmitCompactionTriggered(data)

	active := tracker.GetActive()
	if len(active) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(active))
	}

	// Verify message contains expected info
	msg := active[0].Message
	if !strings.Contains(msg, "claude-agent-1") {
		t.Errorf("expected message to contain agent ID, got: %s", msg)
	}
	if !strings.Contains(msg, "82%") {
		t.Errorf("expected message to contain context usage, got: %s", msg)
	}
	if !strings.Contains(msg, "15.5") {
		t.Errorf("expected message to contain minutes to limit, got: %s", msg)
	}
}
