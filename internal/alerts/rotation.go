// Package alerts provides rotation-related alerts for context window management.
package alerts

import (
	"fmt"
	"strings"
	"time"
)

// RotationAlertData contains details for a rotation-related alert.
type RotationAlertData struct {
	AgentID       string  `json:"agent_id"`
	Session       string  `json:"session"`
	Pane          string  `json:"pane,omitempty"`
	ContextUsage  float64 `json:"context_usage"`
	OldAgentID    string  `json:"old_agent_id,omitempty"`
	NewAgentID    string  `json:"new_agent_id,omitempty"`
	SummaryTokens int     `json:"summary_tokens,omitempty"`
	DurationMs    int64   `json:"duration_ms,omitempty"`
	Error         string  `json:"error,omitempty"`
}

func rotatingAgentID(data RotationAlertData) string {
	if agentID := strings.TrimSpace(data.OldAgentID); agentID != "" {
		return agentID
	}
	return strings.TrimSpace(data.AgentID)
}

// EmitContextWarning emits a warning alert when an agent approaches context threshold.
func EmitContextWarning(data RotationAlertData) {
	alert := Alert{
		ID:       generateAlertID(AlertContextWarning, data.Session, data.AgentID),
		Type:     AlertContextWarning,
		Severity: SeverityWarning,
		Source:   "context_rotation",
		Message:  fmt.Sprintf("Agent %s context at %.0f%% - rotation soon", data.AgentID, data.ContextUsage),
		Session:  data.Session,
		Pane:     data.Pane,
		Context: map[string]interface{}{
			"agent_id":      data.AgentID,
			"context_usage": data.ContextUsage,
		},
		CreatedAt:  time.Now(),
		LastSeenAt: time.Now(),
		Count:      1,
	}

	tracker := GetGlobalTracker()
	tracker.AddAlert(alert)
}

// EmitRotationStarted emits an info alert when rotation begins.
func EmitRotationStarted(data RotationAlertData) {
	alert := Alert{
		ID:       generateAlertID(AlertRotationStarted, data.Session, data.AgentID),
		Type:     AlertRotationStarted,
		Severity: SeverityInfo,
		Source:   "context_rotation",
		Message:  fmt.Sprintf("Rotating agent %s (context at %.0f%%)", data.AgentID, data.ContextUsage),
		Session:  data.Session,
		Pane:     data.Pane,
		Context: map[string]interface{}{
			"agent_id":      data.AgentID,
			"context_usage": data.ContextUsage,
		},
		CreatedAt:  time.Now(),
		LastSeenAt: time.Now(),
		Count:      1,
	}

	tracker := GetGlobalTracker()
	tracker.AddAlert(alert)
}

// EmitRotationComplete emits a success alert when rotation completes successfully.
func EmitRotationComplete(data RotationAlertData) {
	agentID := rotatingAgentID(data)

	// Resolve any pending rotation_started alert
	tracker := GetGlobalTracker()
	startedID := generateAlertID(AlertRotationStarted, data.Session, agentID)
	tracker.ManualResolve(startedID)

	// Also resolve the context warning if it exists
	warningID := generateAlertID(AlertContextWarning, data.Session, agentID)
	tracker.ManualResolve(warningID)

	alert := Alert{
		ID:       generateAlertID(AlertRotationComplete, data.Session, agentID),
		Type:     AlertRotationComplete,
		Severity: SeverityInfo,
		Source:   "context_rotation",
		Message:  fmt.Sprintf("Agent %s rotated to %s successfully", agentID, data.NewAgentID),
		Session:  data.Session,
		Pane:     data.Pane,
		Context: map[string]interface{}{
			"old_agent_id":   agentID,
			"new_agent_id":   data.NewAgentID,
			"summary_tokens": data.SummaryTokens,
			"duration_ms":    data.DurationMs,
		},
		CreatedAt:  time.Now(),
		LastSeenAt: time.Now(),
		Count:      1,
	}

	tracker.AddAlert(alert)
}

// EmitRotationFailed emits an error alert when rotation fails.
func EmitRotationFailed(data RotationAlertData) {
	agentID := rotatingAgentID(data)

	// Resolve any pending rotation_started alert
	tracker := GetGlobalTracker()
	startedID := generateAlertID(AlertRotationStarted, data.Session, agentID)
	tracker.ManualResolve(startedID)

	alert := Alert{
		ID:       generateAlertID(AlertRotationFailed, data.Session, agentID),
		Type:     AlertRotationFailed,
		Severity: SeverityError,
		Source:   "context_rotation",
		Message:  fmt.Sprintf("Failed to rotate agent %s: %s", agentID, data.Error),
		Session:  data.Session,
		Pane:     data.Pane,
		Context: map[string]interface{}{
			"agent_id":      agentID,
			"context_usage": data.ContextUsage,
			"error":         data.Error,
			"duration_ms":   data.DurationMs,
		},
		CreatedAt:  time.Now(),
		LastSeenAt: time.Now(),
		Count:      1,
	}

	tracker.AddAlert(alert)
}

// RotationEventOutput provides structured JSON output for rotation events.
// This is used for robot mode output.
type RotationEventOutput struct {
	Type          string  `json:"type"`
	OldAgent      string  `json:"old_agent"`
	NewAgent      string  `json:"new_agent,omitempty"`
	UsagePercent  float64 `json:"usage_percent"`
	SummaryTokens int     `json:"summary_tokens,omitempty"`
	Status        string  `json:"status"` // "started", "completed", "failed"
	Error         string  `json:"error,omitempty"`
	DurationMs    int64   `json:"duration_ms,omitempty"`
	GeneratedAt   string  `json:"generated_at"`
	SessionName   string  `json:"session_name,omitempty"`
}

// NewRotationEventOutput creates a RotationEventOutput for robot mode JSON.
func NewRotationEventOutput(data RotationAlertData, status string) RotationEventOutput {
	return RotationEventOutput{
		Type:          "context_rotation",
		OldAgent:      rotatingAgentID(data),
		NewAgent:      data.NewAgentID,
		UsagePercent:  data.ContextUsage,
		SummaryTokens: data.SummaryTokens,
		Status:        status,
		Error:         data.Error,
		DurationMs:    data.DurationMs,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		SessionName:   data.Session,
	}
}
