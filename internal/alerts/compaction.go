// Package alerts provides compaction-related alerts for context window management.
package alerts

import (
	"fmt"
	"time"
)

// CompactionAlertData contains details for a compaction-related alert.
type CompactionAlertData struct {
	AgentID        string  `json:"agent_id"`
	Session        string  `json:"session"`
	Pane           string  `json:"pane,omitempty"`
	ContextUsage   float64 `json:"context_usage"`
	MinutesToLimit float64 `json:"minutes_to_limit,omitempty"`
	Method         string  `json:"method,omitempty"` // builtin, summarize, clear_history
	TokensBefore   int64   `json:"tokens_before,omitempty"`
	TokensAfter    int64   `json:"tokens_after,omitempty"`
	UsageBefore    float64 `json:"usage_before,omitempty"`
	UsageAfter     float64 `json:"usage_after,omitempty"`
	DurationMs     int64   `json:"duration_ms,omitempty"`
	Error          string  `json:"error,omitempty"`
}

// EmitCompactionTriggered emits an info alert when proactive compaction is triggered.
func EmitCompactionTriggered(data CompactionAlertData) {
	alert := Alert{
		ID:       generateAlertID(AlertCompactionTriggered, data.Session, data.AgentID),
		Type:     AlertCompactionTriggered,
		Severity: SeverityInfo,
		Source:   "context_compaction",
		Message:  fmt.Sprintf("Compaction triggered for %s (context at %.0f%%, %.1f min to limit)", data.AgentID, data.ContextUsage, data.MinutesToLimit),
		Session:  data.Session,
		Pane:     data.Pane,
		Context: map[string]interface{}{
			"agent_id":         data.AgentID,
			"context_usage":    data.ContextUsage,
			"minutes_to_limit": data.MinutesToLimit,
		},
		CreatedAt:  time.Now(),
		LastSeenAt: time.Now(),
		Count:      1,
	}

	tracker := GetGlobalTracker()
	tracker.AddAlert(alert)
}

// EmitCompactionComplete emits a success alert when compaction completes successfully.
func EmitCompactionComplete(data CompactionAlertData) {
	// Resolve any pending compaction_triggered alert
	tracker := GetGlobalTracker()
	triggeredID := generateAlertID(AlertCompactionTriggered, data.Session, data.AgentID)
	tracker.ManualResolve(triggeredID)

	// Also resolve the context warning if it exists
	warningID := generateAlertID(AlertContextWarning, data.Session, data.AgentID)
	tracker.ManualResolve(warningID)

	alert := Alert{
		ID:       generateAlertID(AlertCompactionComplete, data.Session, data.AgentID),
		Type:     AlertCompactionComplete,
		Severity: SeverityInfo,
		Source:   "context_compaction",
		Message:  fmt.Sprintf("Compaction succeeded for %s: %.0f%% -> %.0f%% (reclaimed %d tokens)", data.AgentID, data.UsageBefore, data.UsageAfter, data.TokensBefore-data.TokensAfter),
		Session:  data.Session,
		Pane:     data.Pane,
		Context: map[string]interface{}{
			"agent_id":      data.AgentID,
			"method":        data.Method,
			"usage_before":  data.UsageBefore,
			"usage_after":   data.UsageAfter,
			"tokens_before": data.TokensBefore,
			"tokens_after":  data.TokensAfter,
			"duration_ms":   data.DurationMs,
		},
		CreatedAt:  time.Now(),
		LastSeenAt: time.Now(),
		Count:      1,
	}

	tracker.AddAlert(alert)
}

// EmitCompactionFailed emits an error alert when compaction fails.
func EmitCompactionFailed(data CompactionAlertData) {
	// Resolve any pending compaction_triggered alert
	tracker := GetGlobalTracker()
	triggeredID := generateAlertID(AlertCompactionTriggered, data.Session, data.AgentID)
	tracker.ManualResolve(triggeredID)

	alert := Alert{
		ID:       generateAlertID(AlertCompactionFailed, data.Session, data.AgentID),
		Type:     AlertCompactionFailed,
		Severity: SeverityWarning, // Warning, not error, since we may fall back to rotation
		Source:   "context_compaction",
		Message:  fmt.Sprintf("Compaction failed for %s: %s", data.AgentID, data.Error),
		Session:  data.Session,
		Pane:     data.Pane,
		Context: map[string]interface{}{
			"agent_id":      data.AgentID,
			"context_usage": data.ContextUsage,
			"method":        data.Method,
			"error":         data.Error,
			"duration_ms":   data.DurationMs,
		},
		CreatedAt:  time.Now(),
		LastSeenAt: time.Now(),
		Count:      1,
	}

	tracker.AddAlert(alert)
}

// CompactionEventOutput provides structured JSON output for compaction events.
// This is used for robot mode output.
type CompactionEventOutput struct {
	Type            string  `json:"type"`
	AgentID         string  `json:"agent_id"`
	Method          string  `json:"method,omitempty"`
	UsageBefore     float64 `json:"usage_before,omitempty"`
	UsageAfter      float64 `json:"usage_after,omitempty"`
	TokensBefore    int64   `json:"tokens_before,omitempty"`
	TokensAfter     int64   `json:"tokens_after,omitempty"`
	TokensReclaimed int64   `json:"tokens_reclaimed,omitempty"`
	Status          string  `json:"status"` // "triggered", "completed", "failed"
	Error           string  `json:"error,omitempty"`
	DurationMs      int64   `json:"duration_ms,omitempty"`
	GeneratedAt     string  `json:"generated_at"`
	SessionName     string  `json:"session_name,omitempty"`
}

// NewCompactionEventOutput creates a CompactionEventOutput for robot mode JSON.
func NewCompactionEventOutput(data CompactionAlertData, status string) CompactionEventOutput {
	output := CompactionEventOutput{
		Type:         "context_compaction",
		AgentID:      data.AgentID,
		Method:       data.Method,
		UsageBefore:  data.UsageBefore,
		UsageAfter:   data.UsageAfter,
		TokensBefore: data.TokensBefore,
		TokensAfter:  data.TokensAfter,
		Status:       status,
		Error:        data.Error,
		DurationMs:   data.DurationMs,
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		SessionName:  data.Session,
	}
	if status == "completed" && data.TokensBefore > data.TokensAfter {
		output.TokensReclaimed = data.TokensBefore - data.TokensAfter
	}
	return output
}
