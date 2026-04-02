// Package events provides an event logging framework for NTM session analytics.
// Events are logged to a JSONL file with automatic rotation.
package events

import (
	"time"
)

// EventType represents the type of event being logged.
type EventType string

const (
	// Session lifecycle events
	EventSessionCreate EventType = "session_create"
	EventSessionKill   EventType = "session_kill"
	EventSessionAttach EventType = "session_attach"

	// Agent lifecycle events
	EventAgentSpawn   EventType = "agent_spawn"
	EventAgentAdd     EventType = "agent_add"
	EventAgentCrash   EventType = "agent_crash"
	EventAgentRestart EventType = "agent_restart"

	// Communication events
	EventPromptSend      EventType = "prompt_send"
	EventPromptBroadcast EventType = "prompt_broadcast"
	EventInterrupt       EventType = "interrupt"

	// State management events
	EventCheckpointCreate  EventType = "checkpoint_create"
	EventCheckpointRestore EventType = "checkpoint_restore"
	EventSessionSave       EventType = "session_save"
	EventSessionRestore    EventType = "session_restore"

	// Template events
	EventTemplateUse EventType = "template_use"

	// Error events
	EventError EventType = "error"
)

// Event represents a single logged event.
type Event struct {
	// Timestamp when the event occurred
	Timestamp time.Time `json:"timestamp"`

	// Type of the event
	Type EventType `json:"type"`

	// Session name (if applicable)
	Session string `json:"session,omitempty"`

	// AgentName identifies which agent emitted this event (if applicable)
	AgentName string `json:"agent_name,omitempty"`

	// CorrelationID enables tracing related events across operations
	CorrelationID string `json:"correlation_id,omitempty"`

	// Additional data specific to the event type
	Data map[string]interface{} `json:"data,omitempty"`
}

// NewEvent creates a new event with the current timestamp.
func NewEvent(eventType EventType, session string, data map[string]interface{}) *Event {
	return &Event{
		Timestamp: time.Now().UTC(),
		Type:      eventType,
		Session:   session,
		Data:      data,
	}
}

// NewEventWithCorrelation creates a new event with a correlation ID for tracing.
func NewEventWithCorrelation(eventType EventType, session, agentName, correlationID string, data map[string]interface{}) *Event {
	return &Event{
		Timestamp:     time.Now().UTC(),
		Type:          eventType,
		Session:       session,
		AgentName:     agentName,
		CorrelationID: correlationID,
		Data:          data,
	}
}

// SessionCreateData contains data for session_create events.
type SessionCreateData struct {
	ClaudeCount   int    `json:"claude_count,omitempty"`
	CodexCount    int    `json:"codex_count,omitempty"`
	GeminiCount   int    `json:"gemini_count,omitempty"`
	CursorCount   int    `json:"cursor_count,omitempty"`
	WindsurfCount int    `json:"windsurf_count,omitempty"`
	AiderCount    int    `json:"aider_count,omitempty"`
	OllamaCount   int    `json:"ollama_count,omitempty"`
	WorkDir       string `json:"work_dir,omitempty"`
	Recipe        string `json:"recipe,omitempty"`
}

// AgentSpawnData contains data for agent_spawn events.
type AgentSpawnData struct {
	AgentType string `json:"agent_type"`
	Model     string `json:"model,omitempty"`
	Variant   string `json:"variant,omitempty"`
	PaneIndex int    `json:"pane_index,omitempty"`
}

// PromptSendData contains data for prompt_send events.
type PromptSendData struct {
	TargetCount     int    `json:"target_count"`
	PromptLength    int    `json:"prompt_length"`
	Template        string `json:"template,omitempty"`
	HasContext      bool   `json:"has_context,omitempty"`
	TargetTypes     string `json:"target_types,omitempty"`
	EstimatedTokens int    `json:"estimated_tokens,omitempty"`
}

// CheckpointData contains data for checkpoint events.
type CheckpointData struct {
	CheckpointID string `json:"checkpoint_id"`
	Description  string `json:"description,omitempty"`
	IncludesGit  bool   `json:"includes_git,omitempty"`
}

// ErrorData contains data for error events.
type ErrorData struct {
	ErrorType string `json:"error_type"`
	Message   string `json:"message"`
	Stack     string `json:"stack,omitempty"`
}

// ToMap converts a struct to a map[string]interface{} for event data.
func ToMap(v interface{}) map[string]interface{} {
	switch d := v.(type) {
	case SessionCreateData:
		return map[string]interface{}{
			"claude_count":   d.ClaudeCount,
			"codex_count":    d.CodexCount,
			"gemini_count":   d.GeminiCount,
			"cursor_count":   d.CursorCount,
			"windsurf_count": d.WindsurfCount,
			"aider_count":    d.AiderCount,
			"ollama_count":   d.OllamaCount,
			"work_dir":       d.WorkDir,
			"recipe":         d.Recipe,
		}
	case AgentSpawnData:
		return map[string]interface{}{
			"agent_type": d.AgentType,
			"model":      d.Model,
			"variant":    d.Variant,
			"pane_index": d.PaneIndex,
		}
	case PromptSendData:
		return map[string]interface{}{
			"target_count":     d.TargetCount,
			"prompt_length":    d.PromptLength,
			"template":         d.Template,
			"has_context":      d.HasContext,
			"target_types":     d.TargetTypes,
			"estimated_tokens": d.EstimatedTokens,
		}
	case CheckpointData:
		return map[string]interface{}{
			"checkpoint_id": d.CheckpointID,
			"description":   d.Description,
			"includes_git":  d.IncludesGit,
		}
	case ErrorData:
		return map[string]interface{}{
			"error_type": d.ErrorType,
			"message":    d.Message,
			"stack":      d.Stack,
		}
	case map[string]interface{}:
		return d
	default:
		return nil
	}
}
