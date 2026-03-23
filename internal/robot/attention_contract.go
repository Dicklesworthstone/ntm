// Package robot provides machine-readable output for AI agents.
// attention_contract.go defines the contract for the Attention Feed system.
//
// # Attention Feed Contract Specification v1.0.0
//
// The Attention Feed is a normalized event streaming system that provides
// operator agents with a unified view of session state, agent activity,
// and actionable signals. This contract defines the exact semantics that
// all implementations MUST follow.
//
// ## Design Philosophy
//
// The LLM is the driver; ntm is the nervous system. The Attention Feed
// provides sensing and actuation surfaces, not decision-making logic.
// Operator agents consume events to make decisions; ntm emits events
// and executes commands.
//
// ## Operator Loop
//
// The canonical operator loop is:
//  1. Bootstrap: --robot-snapshot → get current state
//  2. Stream: --robot-events --since=<cursor> → receive events since cursor
//  3. Digest: --robot-digest → get aggregated summary (optional)
//  4. Wait: --robot-wait --condition=<name> → block until condition met
//  5. Attention: --robot-attention → get actionable items requiring response
//  6. Act: --robot-send, --robot-interrupt, etc. → execute commands
//  7. Loop: goto step 2 with new cursor
//
// ## Cursor Semantics
//
// Cursors are monotonically increasing 64-bit integers that identify
// positions in the event stream. They enable replay and resumption.
//
//   - Cursors are scoped per-server (not globally unique)
//   - Cursors are strictly monotonic: cursor(n) < cursor(n+1)
//   - Gaps in cursor sequence are allowed (compaction, filtering)
//   - Cursor 0 is reserved: means "start from beginning" or "no cursor"
//   - Cursor -1 is reserved: means "start from now" (skip history)
//
// ## Cursor Expiration
//
// Events are retained for a configurable period (default: 1 hour).
// When a cursor references an expired event:
//
//   - Response includes "error_code": "CURSOR_EXPIRED"
//   - Response includes "resync_cursor": <earliest_valid_cursor>
//   - Client MUST resync by fetching --robot-snapshot, then resuming
//     from resync_cursor
//
// ## Transport Parity
//
// CLI, HTTP, SSE, and WebSocket clients receive identical event envelopes.
// The only differences are transport-level framing:
//   - CLI: newline-delimited JSON objects
//   - HTTP: JSON array in response body
//   - SSE: data: prefix, event: field for type
//   - WebSocket: JSON messages (no framing needed)
package robot

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// AttentionContractVersion is the semantic version of the attention feed contract.
// Increment major version for breaking changes, minor for additions, patch for fixes.
const AttentionContractVersion = "1.1.0"

// =============================================================================
// Event Categories and Types
// =============================================================================

// EventCategory classifies events by their domain.
// Categories are coarse-grained for filtering efficiency.
type EventCategory string

const (
	// EventCategorySession events relate to session lifecycle.
	EventCategorySession EventCategory = "session"

	// EventCategoryPane events relate to individual pane state.
	EventCategoryPane EventCategory = "pane"

	// EventCategoryAgent events relate to agent behavior and activity.
	EventCategoryAgent EventCategory = "agent"

	// EventCategoryActuation events relate to explicit operator-triggered actions.
	EventCategoryActuation EventCategory = "actuation"

	// EventCategoryFile events relate to file system changes.
	EventCategoryFile EventCategory = "file"

	// EventCategoryMail events relate to Agent Mail messages.
	EventCategoryMail EventCategory = "mail"

	// EventCategoryBead events relate to beads/issue tracker state.
	EventCategoryBead EventCategory = "bead"

	// EventCategorySystem events relate to ntm system state.
	EventCategorySystem EventCategory = "system"

	// EventCategoryAlert events are synthesized attention signals.
	EventCategoryAlert EventCategory = "alert"

	// EventCategoryHealth events relate to system health checks.
	EventCategoryHealth EventCategory = "health"
)

// EventType identifies specific event kinds within a category.
// Types are fine-grained for precise filtering.
type EventType string

// Session events
const (
	EventTypeSessionCreated   EventType = "session.created"
	EventTypeSessionDestroyed EventType = "session.destroyed"
	EventTypeSessionAttached  EventType = "session.attached"
	EventTypeSessionDetached  EventType = "session.detached"
)

// Pane events
const (
	EventTypePaneCreated   EventType = "pane.created"
	EventTypePaneDestroyed EventType = "pane.destroyed"
	EventTypePaneOutput    EventType = "pane.output"
	EventTypePaneResized   EventType = "pane.resized"
)

// Agent events
const (
	EventTypeAgentStarted     EventType = "agent.started"
	EventTypeAgentStopped     EventType = "agent.stopped"
	EventTypeAgentStateChange EventType = "agent.state_change"
	EventTypeAgentError       EventType = "agent.error"
	EventTypeAgentHandoff     EventType = "agent.handoff"
	EventTypeAgentStalled     EventType = "agent.stalled"
	EventTypeAgentRecovered   EventType = "agent.recovered"
	EventTypeAgentCompacted   EventType = "agent.compacted"
	EventTypeAgentIdle        EventType = "agent.idle"
	EventTypeAgentPromptWait  EventType = "agent.prompt_wait"

	// Add missing event types
	EventTypeMailUnread          EventType = "mail.unread"
	EventTypePaneContextHot      EventType = "pane.context_hot"
	EventTypeReservationConflict EventType = "reservation.conflict"
)

// Actuation events
const (
	EventTypeActuationRequested EventType = "actuation.requested"
	EventTypeActuationOutcome   EventType = "actuation.outcome"
	EventTypeActuationVerified  EventType = "actuation.verified"
)

// File events
const (
	EventTypeFileChanged  EventType = "file.changed"
	EventTypeFileConflict EventType = "file.conflict"
	EventTypeFileReserved EventType = "file.reserved"
	EventTypeFileReleased EventType = "file.released"
)

// Mail events
const (
	EventTypeMailReceived     EventType = "mail.received"
	EventTypeMailAckRequired  EventType = "mail.ack_required"
	EventTypeMailAcknowledged EventType = "mail.acknowledged"
)

// Bead events
const (
	EventTypeBeadCreated   EventType = "bead.created"
	EventTypeBeadUpdated   EventType = "bead.updated"
	EventTypeBeadClosed    EventType = "bead.closed"
	EventTypeBeadUnblocked EventType = "bead.unblocked"
)

// System events
const (
	EventTypeSystemStartup      EventType = "system.startup"
	EventTypeSystemShutdown     EventType = "system.shutdown"
	EventTypeSystemHealthChange EventType = "system.health_change"
	EventTypeSystemCursorReset  EventType = "system.cursor_reset"
	EventTypeSpawn              EventType = "system.spawn"
)

// Alert events (synthesized signals)
const (
	EventTypeAlertAttentionRequired EventType = "alert.attention_required"
	EventTypeAlertWarning           EventType = "alert.warning"
	EventTypeAlertInfo              EventType = "alert.info"
	EventTypeAlert                  EventType = "alert.generic"
	EventTypeHealthChange           EventType = "health.change"
)

// =============================================================================
// Actionability and Severity
// =============================================================================

// Actionability indicates whether and how urgently an event requires response.
// This is distinct from severity: a critical error may need no action if
// auto-recovery is in progress.
type Actionability string

const (
	// ActionabilityBackground events are informational only.
	// No action required. Safe to batch or skip during high load.
	ActionabilityBackground Actionability = "background"

	// ActionabilityInteresting events may warrant attention.
	// No immediate action required, but operator may want to investigate.
	ActionabilityInteresting Actionability = "interesting"

	// ActionabilityActionRequired events need operator response.
	// The event cannot be resolved without operator intervention.
	ActionabilityActionRequired Actionability = "action_required"
)

// Severity indicates the impact level of an event.
// Severity is orthogonal to actionability: a high-severity event may
// resolve itself (no action needed), while a low-severity event may
// block progress (action required).
type Severity string

const (
	// SeverityDebug events are for troubleshooting only.
	// Not emitted in normal operation.
	SeverityDebug Severity = "debug"

	// SeverityInfo events are normal operational messages.
	SeverityInfo Severity = "info"

	// SeverityWarning events indicate potential issues.
	SeverityWarning Severity = "warning"

	// SeverityError events indicate failures that may need attention.
	SeverityError Severity = "error"

	// SeverityCritical events indicate severe failures affecting operation.
	SeverityCritical Severity = "critical"
)

// =============================================================================
// Event Envelope
// =============================================================================

// AttentionEvent is the normalized event envelope for all attention feed events.
// Every event in the feed uses this exact structure.
//
// Example successful event:
//
//	{
//	  "cursor": 42,
//	  "ts": "2026-03-20T15:30:45Z",
//	  "session": "myproject",
//	  "pane": 2,
//	  "category": "agent",
//	  "type": "agent.state_change",
//	  "source": "activity_tracker",
//	  "actionability": "interesting",
//	  "severity": "info",
//	  "summary": "Agent cc_1 transitioned from generating to idle",
//	  "details": {"from_state": "generating", "to_state": "idle", "duration_ms": 45000},
//	  "next_actions": [
//	    {"action": "robot-tail", "args": "--session=myproject --pane=2 --lines=50"}
//	  ]
//	}
type AttentionEvent struct {
	// Cursor is the monotonically increasing event sequence number.
	// Use this to resume streaming from a known position.
	// Required. Never zero for actual events.
	Cursor int64 `json:"cursor"`

	// Ts is the RFC3339 UTC timestamp when the event occurred.
	// Required.
	Ts string `json:"ts"`

	// Session is the session name where the event occurred.
	// Empty string for system-wide events.
	Session string `json:"session,omitempty"`

	// Pane is the pane index where the event occurred.
	// -1 or omitted for session-wide or system-wide events.
	Pane int `json:"pane,omitempty"`

	// Category is the coarse-grained event classification.
	// Required. One of: session, pane, agent, actuation, file, mail, bead, system, alert.
	Category EventCategory `json:"category"`

	// Type is the fine-grained event type within the category.
	// Required. Format: "category.specific_event".
	Type EventType `json:"type"`

	// Source identifies the component that generated this event.
	// Used for debugging and filtering. Optional but recommended.
	Source string `json:"source,omitempty"`

	// Actionability indicates whether operator response is needed.
	// Required. One of: background, interesting, action_required.
	Actionability Actionability `json:"actionability"`

	// Severity indicates the impact level of this event.
	// Required. One of: debug, info, warning, error, critical.
	Severity Severity `json:"severity"`

	// ReasonCode is the machine-readable reason for this event.
	// Optional, but strongly recommended for durable events so downstream
	// consumers can classify them without reparsing the summary text.
	ReasonCode string `json:"reason_code,omitempty"`

	// Summary is a one-line human-readable description.
	// Required. Should be suitable for display without additional context.
	Summary string `json:"summary"`

	// Details contains event-specific structured data.
	// Optional. Schema varies by event type. Use for machine consumption.
	Details map[string]any `json:"details,omitempty"`

	// NextActions are mechanical follow-up commands the operator can execute.
	// These must be real robot commands, not hypothetical future features.
	// Optional. Empty array if no actions are relevant.
	NextActions []NextAction `json:"next_actions,omitempty"`

	// DedupKey is an internal suppression key for repeated durable events.
	// It is intentionally excluded from the wire contract.
	DedupKey string `json:"-"`
}

// NextAction describes a mechanical follow-up command.
// Actions MUST point to real robot surfaces, not hypothetical commands.
type NextAction struct {
	// Action is the robot command name (e.g., "robot-tail", "robot-send").
	// Required.
	Action string `json:"action"`

	// Args are the command arguments as a single string.
	// Required. Should be copy-paste ready.
	Args string `json:"args"`

	// Reason explains why this action is suggested.
	// Optional but recommended for operator context.
	Reason string `json:"reason,omitempty"`
}

// =============================================================================
// Wait Conditions (Extensions to base conditions in wait.go)
// =============================================================================

// Additional wait condition names beyond those in wait.go.
// The base conditions (idle, complete, generating, healthy) are defined in wait.go.
const (
	// WaitConditionEvent waits until any event matches the filter criteria.
	WaitConditionEvent = "event"

	// WaitConditionMail waits until new mail arrives for the specified agent.
	WaitConditionMail = "mail"

	// WaitConditionBeadReady waits until an unblocked bead becomes available.
	WaitConditionBeadReady = "bead_ready"
)

// AllWaitConditions lists all valid wait condition names including base and extended.
var AllWaitConditions = []string{
	WaitConditionIdle,       // from wait.go
	WaitConditionComplete,   // from wait.go
	WaitConditionGenerating, // from wait.go
	WaitConditionHealthy,    // from wait.go
	WaitConditionEvent,      // extended
	WaitConditionMail,       // extended
	WaitConditionBeadReady,  // extended
}

// IsValidAttentionWaitCondition checks if a condition name is valid.
// Includes both base conditions from wait.go and extended conditions.
func IsValidAttentionWaitCondition(name string) bool {
	for _, c := range AllWaitConditions {
		if c == name {
			return true
		}
	}
	return false
}

// =============================================================================
// Command Cluster
// =============================================================================

// AttentionCommand describes a robot command in the attention feed cluster.
type AttentionCommand struct {
	// Name is the flag name (e.g., "--robot-events").
	Name string `json:"name"`

	// Synopsis is a one-line description.
	Synopsis string `json:"synopsis"`

	// Args describes the arguments.
	Args []CommandArg `json:"args,omitempty"`

	// Returns describes the output schema.
	Returns string `json:"returns"`

	// Example is a copy-paste ready usage example.
	Example string `json:"example"`
}

// CommandArg describes a command argument.
type CommandArg struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
}

// AttentionCommands defines the minimal command cluster.
// These are the commands that form the attention feed interface.
var AttentionCommands = []AttentionCommand{
	{
		Name:     "--robot-snapshot",
		Synopsis: "Get current system state (bootstrap entry point)",
		Args: []CommandArg{
			{Name: "--since", Type: "RFC3339", Required: false, Description: "Only include changes since timestamp"},
		},
		Returns: "StatusOutput with sessions, agents, beads, alerts",
		Example: "ntm --robot-snapshot",
	},
	{
		Name:     "--robot-events",
		Synopsis: "Stream events since cursor",
		Args: []CommandArg{
			{Name: "--since", Type: "cursor|RFC3339", Required: false, Description: "Start position (cursor int or timestamp)"},
			{Name: "--limit", Type: "int", Required: false, Description: "Max events to return (default: 100)"},
			{Name: "--category", Type: "string", Required: false, Description: "Filter by category"},
			{Name: "--type", Type: "string", Required: false, Description: "Filter by event type"},
			{Name: "--session", Type: "string", Required: false, Description: "Filter by session"},
		},
		Returns: "EventsOutput with events array and next_cursor",
		Example: "ntm --robot-events --since=42 --limit=50",
	},
	{
		Name:     "--robot-digest",
		Synopsis: "Get aggregated summary of recent activity",
		Args: []CommandArg{
			{Name: "--window", Type: "duration", Required: false, Description: "Aggregation window (default: 5m)"},
			{Name: "--session", Type: "string", Required: false, Description: "Scope to session"},
		},
		Returns: "DigestOutput with counts, highlights, and attention items",
		Example: "ntm --robot-digest --window=10m",
	},
	{
		Name:     "--robot-wait",
		Synopsis: "Block until condition is met",
		Args: []CommandArg{
			{Name: "--condition", Type: "string", Required: true, Description: "Condition name (idle, complete, generating, healthy, event, mail, bead_ready)"},
			{Name: "--timeout", Type: "duration", Required: false, Description: "Max wait time (default: 60s)"},
			{Name: "--session", Type: "string", Required: false, Description: "Scope to session"},
			{Name: "--pane", Type: "int", Required: false, Description: "Scope to pane"},
		},
		Returns: "WaitOutput with condition, waited_seconds, and matched agents",
		Example: "ntm --robot-wait --condition=idle --session=myproject --timeout=30s",
	},
	{
		Name:     "--robot-attention",
		Synopsis: "Get items requiring operator response",
		Args: []CommandArg{
			{Name: "--session", Type: "string", Required: false, Description: "Scope to session"},
			{Name: "--min-severity", Type: "string", Required: false, Description: "Minimum severity (default: warning)"},
		},
		Returns: "AttentionOutput with action_required items and suggested actions",
		Example: "ntm --robot-attention --min-severity=error",
	},
}

// =============================================================================
// Output Types
// =============================================================================

// EventsOutput is defined in attention_feed.go (the runtime implementation).
// It includes: Events, LatestCursor, FeedCursor, SinceCursor, EventCount,
// Truncated, CursorExpired, and ResyncHint fields.

// DigestOutput is the response for --robot-digest.
type DigestOutput struct {
	RobotResponse

	// Window is the aggregation time window.
	Window string `json:"window"`

	// Counts summarizes event counts by category.
	Counts map[EventCategory]int `json:"counts"`

	// Highlights are notable events worth surfacing.
	Highlights []AttentionEvent `json:"highlights"`

	// AttentionItems are items requiring operator response.
	AttentionItems []AttentionEvent `json:"attention_items"`

	// AgentStates summarizes current agent states.
	AgentStates map[string]string `json:"agent_states"`

	// ActiveBeads lists beads currently being worked.
	ActiveBeads []string `json:"active_beads,omitempty"`

	// Profile is the name of the profile used (if any).
	Profile string `json:"profile,omitempty"`
}

// AttentionOutput is the response for --robot-attention.
type AttentionOutput struct {
	RobotResponse

	// Items are events requiring operator response.
	// Sorted by severity (critical first) then by timestamp.
	Items []AttentionEvent `json:"items"`

	// SuggestedActions are aggregate actions the operator might take.
	SuggestedActions []NextAction `json:"suggested_actions,omitempty"`

	// Counts summarizes attention items by severity.
	Counts map[Severity]int `json:"counts"`
}

// =============================================================================
// Snapshot Attention Summary (br-slg9g: Attention Feed Phase 2a2)
// =============================================================================

// SnapshotAttentionSummary provides a compact orientation summary from the
// attention feed at snapshot time. Helps operators choose the next targeted
// command without rereading the entire snapshot or waiting for a digest.
type SnapshotAttentionSummary struct {
	// TotalEvents is the number of events currently in the journal.
	TotalEvents int `json:"total_events"`

	// ActionRequiredCount is events needing operator action.
	ActionRequiredCount int `json:"action_required_count"`

	// InterestingCount is events worth noting but not urgent.
	InterestingCount int `json:"interesting_count"`

	// TopItems surfaces the most recent action_required events (up to 3)
	// so operators can immediately see what needs attention.
	TopItems []SnapshotAttentionItem `json:"top_items,omitempty"`

	// ByCategoryCount groups events by category for orientation.
	ByCategoryCount map[string]int `json:"by_category,omitempty"`

	// UnsupportedSignals lists signals that were considered but are
	// deliberately not supported, so operators know what to expect.
	UnsupportedSignals []string `json:"unsupported_signals,omitempty"`

	// NextSteps are mechanical suggestions for what to do next.
	NextSteps []NextAction `json:"next_steps,omitempty"`
}

// SnapshotAttentionItem is a compact representation of a top attention item.
type SnapshotAttentionItem struct {
	Cursor        int64  `json:"cursor"`
	Category      string `json:"category"`
	Actionability string `json:"actionability"`
	Severity      string `json:"severity"`
	Summary       string `json:"summary"`
}

// =============================================================================
// Cursor Expiration
// =============================================================================

// ErrCodeCursorExpired indicates the requested cursor has expired.
// Client must resync via --robot-snapshot.
const ErrCodeCursorExpired = "CURSOR_EXPIRED"

// CursorExpiredDetails provides information for cursor expiration recovery.
type CursorExpiredDetails struct {
	// RequestedCursor is the cursor that was requested.
	RequestedCursor int64 `json:"requested_cursor"`

	// EarliestCursor is the earliest valid cursor available.
	EarliestCursor int64 `json:"earliest_cursor"`

	// RetentionPeriod is the configured event retention period.
	RetentionPeriod string `json:"retention_period"`

	// ResyncCommand is the copy-paste ready command to resync.
	ResyncCommand string `json:"resync_command"`
}

// =============================================================================
// Profiles and Defaults
// =============================================================================

// AttentionProfile defines filter presets for common operator patterns.
type AttentionProfile struct {
	// Name is the profile identifier (e.g., "operator", "debug", "minimal").
	Name string `json:"name"`

	// Description explains when to use this profile.
	Description string `json:"description"`

	// Filters are the default filters applied when this profile is active.
	Filters ProfileFilters `json:"filters"`
}

// ProfileFilters defines the filter settings in a profile.
type ProfileFilters struct {
	// Categories limits events to these categories.
	// Empty means all categories.
	Categories []EventCategory `json:"categories,omitempty"`

	// MinSeverity is the minimum severity to include.
	// Default: "info".
	MinSeverity Severity `json:"min_severity,omitempty"`

	// MinActionability is the minimum actionability to include.
	// Default: "background".
	MinActionability Actionability `json:"min_actionability,omitempty"`

	// ExcludeTypes lists event types to exclude.
	ExcludeTypes []EventType `json:"exclude_types,omitempty"`
}

// DefaultProfile is the profile used when none is specified.
const DefaultProfile = "operator"

// BuiltinProfiles defines the available filter profiles.
var BuiltinProfiles = []AttentionProfile{
	{
		Name:        "operator",
		Description: "Default profile for operator agents. Shows actionable events and important state changes.",
		Filters: ProfileFilters{
			MinSeverity:      SeverityInfo,
			MinActionability: ActionabilityInteresting,
		},
	},
	{
		Name:        "debug",
		Description: "Full verbosity for debugging. Shows all events including debug level.",
		Filters: ProfileFilters{
			MinSeverity:      SeverityDebug,
			MinActionability: ActionabilityBackground,
		},
	},
	{
		Name:        "minimal",
		Description: "Only critical items requiring immediate action.",
		Filters: ProfileFilters{
			MinSeverity:      SeverityError,
			MinActionability: ActionabilityActionRequired,
		},
	},
	{
		Name:        "alerts",
		Description: "Only synthesized alert events.",
		Filters: ProfileFilters{
			Categories: []EventCategory{EventCategoryAlert},
		},
	},
}

func cloneProfileFilters(filters ProfileFilters) ProfileFilters {
	cloned := filters
	if len(filters.Categories) > 0 {
		cloned.Categories = append([]EventCategory(nil), filters.Categories...)
	}
	if len(filters.ExcludeTypes) > 0 {
		cloned.ExcludeTypes = append([]EventType(nil), filters.ExcludeTypes...)
	}
	return cloned
}

func cloneAttentionProfile(profile AttentionProfile) AttentionProfile {
	cloned := profile
	cloned.Filters = cloneProfileFilters(profile.Filters)
	return cloned
}

// GetProfile returns the named profile, or nil if not found.
func GetProfile(name string) *AttentionProfile {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	for i := range BuiltinProfiles {
		if strings.EqualFold(BuiltinProfiles[i].Name, name) {
			profile := cloneAttentionProfile(BuiltinProfiles[i])
			return &profile
		}
	}
	return nil
}

// ListProfiles returns all available profiles.
func ListProfiles() []AttentionProfile {
	profiles := make([]AttentionProfile, len(BuiltinProfiles))
	for i := range BuiltinProfiles {
		profiles[i] = cloneAttentionProfile(BuiltinProfiles[i])
	}
	return profiles
}

// ResolvedFilters represents the effective filters after profile and explicit filter resolution.
type ResolvedFilters struct {
	// Categories to include (empty = all).
	Categories []EventCategory `json:"categories,omitempty"`

	// MinSeverity is the minimum severity to include.
	MinSeverity Severity `json:"min_severity"`

	// MinActionability is the minimum actionability to include.
	MinActionability Actionability `json:"min_actionability"`

	// ExcludeTypes lists event types to exclude.
	ExcludeTypes []EventType `json:"exclude_types,omitempty"`

	// SourceProfile is the profile name used (empty if none).
	SourceProfile string `json:"source_profile,omitempty"`

	// ExplicitOverrides lists which fields were explicitly overridden.
	ExplicitOverrides []string `json:"explicit_overrides,omitempty"`
}

// ResolveEffectiveFilters merges a profile with explicit filter overrides.
// Explicit filters always take precedence over profile defaults.
// If profileName is empty, no profile is applied.
func ResolveEffectiveFilters(profileName string, explicit ProfileFilters) ResolvedFilters {
	result := ResolvedFilters{
		MinSeverity:      SeverityInfo,
		MinActionability: ActionabilityBackground,
	}

	// Apply profile if specified
	if profileName != "" {
		profile := GetProfile(profileName)
		if profile != nil {
			result.SourceProfile = profileName
			if len(profile.Filters.Categories) > 0 {
				result.Categories = profile.Filters.Categories
			}
			if profile.Filters.MinSeverity != "" {
				result.MinSeverity = profile.Filters.MinSeverity
			}
			if profile.Filters.MinActionability != "" {
				result.MinActionability = profile.Filters.MinActionability
			}
			if len(profile.Filters.ExcludeTypes) > 0 {
				result.ExcludeTypes = profile.Filters.ExcludeTypes
			}
		}
	}

	// Apply explicit overrides
	overrides := []string{}
	if len(explicit.Categories) > 0 {
		result.Categories = explicit.Categories
		overrides = append(overrides, "categories")
	}
	if explicit.MinSeverity != "" {
		result.MinSeverity = explicit.MinSeverity
		overrides = append(overrides, "min_severity")
	}
	if explicit.MinActionability != "" {
		result.MinActionability = explicit.MinActionability
		overrides = append(overrides, "min_actionability")
	}
	if len(explicit.ExcludeTypes) > 0 {
		result.ExcludeTypes = explicit.ExcludeTypes
		overrides = append(overrides, "exclude_types")
	}
	if len(overrides) > 0 {
		result.ExplicitOverrides = overrides
	}

	return result
}

// MatchesFilters checks if an event passes the resolved filters.
func (r *ResolvedFilters) MatchesFilters(event *AttentionEvent) bool {
	// Category check
	if len(r.Categories) > 0 {
		found := false
		for _, cat := range r.Categories {
			if event.Category == cat {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Severity check
	if !severityMeetsMinimum(event.Severity, r.MinSeverity) {
		return false
	}

	// Actionability check
	if !actionabilityMeetsMinimum(event.Actionability, r.MinActionability) {
		return false
	}

	// Exclude types check
	for _, et := range r.ExcludeTypes {
		if event.Type == et {
			return false
		}
	}

	return true
}

// severityMeetsMinimum checks if a severity meets the minimum threshold.
func severityMeetsMinimum(sev, min Severity) bool {
	if min == "" {
		return true
	}

	order := map[Severity]int{
		SeverityDebug:    0,
		SeverityInfo:     1,
		SeverityWarning:  2,
		SeverityError:    3,
		SeverityCritical: 4,
	}

	sevRank, ok := order[sev]
	if !ok {
		return false
	}
	minRank, ok := order[min]
	if !ok {
		return false
	}
	return sevRank >= minRank
}

// actionabilityMeetsMinimum checks if actionability meets the minimum threshold.
func actionabilityMeetsMinimum(act, min Actionability) bool {
	order := map[Actionability]int{
		ActionabilityBackground:     0,
		ActionabilityInteresting:    1,
		ActionabilityActionRequired: 2,
	}
	return order[act] >= order[min]
}

// =============================================================================
// Degraded State and Capability Markers
// =============================================================================

// CapabilityStatus indicates whether a feature is available.
type CapabilityStatus string

const (
	// CapabilityAvailable means the feature is fully functional.
	CapabilityAvailable CapabilityStatus = "available"

	// CapabilityDegraded means the feature works but with reduced functionality.
	CapabilityDegraded CapabilityStatus = "degraded"

	// CapabilityUnavailable means the feature is not working.
	CapabilityUnavailable CapabilityStatus = "unavailable"
)

// AttentionCapabilities reports the status of attention feed features.
type AttentionCapabilities struct {
	// ContractVersion is the attention feed contract version.
	ContractVersion string `json:"contract_version"`

	// DefaultProfile is the explicit default noise model for tending surfaces.
	DefaultProfile string `json:"default_profile,omitempty"`

	// Profiles enumerates the discoverable built-in profile presets.
	Profiles []AttentionProfile `json:"profiles,omitempty"`

	// Features reports availability of specific features.
	Features map[string]CapabilityFeature `json:"features"`

	// SignalAvailability reports signal-specific support or unsupported status.
	// This is used when ntm can say something precise about a named signal
	// without pretending the whole feed implementation is complete.
	SignalAvailability map[string]CapabilityFeature `json:"signal_availability,omitempty"`

	// Degraded lists features that are in degraded state with reasons.
	Degraded []DegradedFeature `json:"degraded,omitempty"`

	// UnsupportedConditions lists conditions that were deliberately considered
	// and rejected with rationale. This allows operators and future contributors
	// to understand what was decided and what would need to change.
	UnsupportedConditions []UnsupportedCondition `json:"unsupported_conditions,omitempty"`

	// Guardrails records the intended operator-loop boundary so future changes
	// do not quietly turn ntm into a planner or hidden coordinator.
	Guardrails []OperatorLoopGuardrail `json:"guardrails,omitempty"`
}

// CapabilityFeature describes a single feature's status.
type CapabilityFeature struct {
	Status CapabilityStatus `json:"status"`
	Note   string           `json:"note,omitempty"`
}

// DegradedFeature describes a feature operating in degraded mode.
type DegradedFeature struct {
	Feature string           `json:"feature"`
	Status  CapabilityStatus `json:"status"`
	Reason  string           `json:"reason"`
	Impact  string           `json:"impact"`
	Hint    string           `json:"hint,omitempty"`
}

// OperatorLoopGuardrail captures a non-negotiable product boundary for the
// attention feed and tending loop surfaces.
type OperatorLoopGuardrail struct {
	Name      string `json:"name"`
	Rule      string `json:"rule"`
	Rationale string `json:"rationale,omitempty"`
}

const (
	// AttentionSignalBeadOrphaned identifies the proposed signal for work that
	// appears abandoned. It remains unsupported until ntm can prove the state
	// from observable evidence instead of heuristic guesswork.
	//
	// Feasibility analysis (br-3qs46, 2026-03-21):
	//
	// Observable state available to ntm:
	//   - Bead status (open, in_progress, closed) via br/bv integration
	//   - Agent identity and pane liveness (alive, stalled, dead)
	//   - Agent Mail message history and reservations
	//
	// Why this is NOT enough to prove orphaned:
	//   1. An in-progress bead with a dead agent pane does NOT mean the work is
	//      orphaned — the agent may have been intentionally killed, may be
	//      respawning, or may have finished but not closed the bead yet.
	//   2. An in-progress bead with a stalled agent does NOT mean the work is
	//      abandoned — the agent may be compacting, waiting for rch compilation,
	//      doing deep research, or simply thinking on a hard problem.
	//   3. Heuristic time-based detection (e.g., "no progress for 30 minutes")
	//      conflates legitimate long-running work with abandonment and would
	//      produce false positives on complex tasks.
	//   4. ntm has no way to distinguish "agent chose to stop working on this
	//      bead" from "agent crashed mid-work" — both look like silence.
	//
	// The honest conclusion: bead_orphaned requires coordination-level knowledge
	// that only the operator agent or the working agent can provide. ntm should
	// surface the raw observables (agent state, bead status, time since last
	// activity) and let the operator draw conclusions.
	//
	// To revisit: if Agent Mail gains "bead handoff" or "explicit abandon"
	// messages, ntm could detect orphaned work from that evidence. Until then,
	// emitting bead_orphaned would be inventing conclusions from insufficient data.
	AttentionSignalBeadOrphaned = "bead_orphaned"
)

// UnsupportedCondition describes a condition that was considered but is not
// implemented, with rationale for why and what would need to change.
type UnsupportedCondition struct {
	// Name is the condition identifier (e.g., "bead_orphaned").
	Name string `json:"name"`

	// Status is always CapabilityUnavailable for this type.
	Status CapabilityStatus `json:"status"`

	// Reason explains why this condition is not supported.
	Reason string `json:"reason"`

	// ObservablesAvailable lists what ntm CAN see that relates to this condition.
	ObservablesAvailable []string `json:"observables_available"`

	// WhatWouldChange describes what needs to happen for this to become supported.
	WhatWouldChange string `json:"what_would_change"`
}

// UnsupportedConditions returns conditions that were considered but are not
// implemented. This is exposed in capabilities and help output so operators
// and future contributors know what was decided and why.
func UnsupportedConditions() []UnsupportedCondition {
	return []UnsupportedCondition{
		{
			Name:   AttentionSignalBeadOrphaned,
			Status: CapabilityUnavailable,
			Reason: "ntm cannot prove work is orphaned from observable state alone. " +
				"Agent silence may indicate compaction, deep thinking, rch compilation, " +
				"or intentional pause — not abandonment. Emitting this signal would " +
				"produce false positives that erode operator trust.",
			ObservablesAvailable: []string{
				"bead status (open/in_progress/closed) via br/bv",
				"agent pane liveness (alive/stalled/dead)",
				"time since last pane output",
				"Agent Mail message history",
			},
			WhatWouldChange: "Agent Mail gains explicit 'bead abandon' or 'bead handoff' " +
				"messages, giving ntm positive evidence of intent rather than inferring " +
				"abandonment from silence.",
		},
	}
}

// DefaultAttentionCapabilities returns the current machine-readable attention
// contract status. This is intentionally conservative: when ntm cannot defend a
// signal from real observable state, it must say so explicitly.
func DefaultAttentionCapabilities() *AttentionCapabilities {
	return &AttentionCapabilities{
		ContractVersion: AttentionContractVersion,
		DefaultProfile:  DefaultProfile,
		Profiles:        ListProfiles(),
		Features: map[string]CapabilityFeature{
			"cursor_replay": {
				Status: CapabilityAvailable,
				Note:   "Replay uses monotonic cursors with explicit resync instructions on expiration.",
			},
			"profile_presets": {
				Status: CapabilityAvailable,
				Note:   "Named attention profiles resolve first, then explicit filters narrow or override the effective filter set.",
			},
			"operator_boundary": {
				Status: CapabilityAvailable,
				Note:   "Attention feed remains a sensing/actuation surface only; it does not assign work, infer intent, or replace beads, bv, or Agent Mail.",
			},
		},
		SignalAvailability: map[string]CapabilityFeature{
			AttentionSignalBeadOrphaned: {
				Status: CapabilityUnavailable,
				Note:   "Unsupported: ntm cannot yet prove orphaned work from observable beads, bv, and agent state without inventing coordination conclusions.",
			},
		},
		UnsupportedConditions: UnsupportedConditions(),
		Guardrails: []OperatorLoopGuardrail{
			{
				Name:      "nervous_system_not_planner",
				Rule:      "ntm emits observable state and executes commands, but it must not invent plans, hidden task graphs, or coordinator conclusions.",
				Rationale: "The operator agent remains the decision-maker; ntm should sense and act, not pretend to think on the operator's behalf.",
			},
			{
				Name:      "one_obvious_tending_loop",
				Rule:      "Prefer --robot-attention for the steady-state operator loop; use --robot-events for replay/debug and --robot-digest for non-blocking summaries.",
				Rationale: "This keeps tending simpler than repeated snapshot polling and prevents multiple competing loop patterns.",
			},
			{
				Name:      "cursor_resync_is_explicit",
				Rule:      "When replay is stale or CURSOR_EXPIRED occurs, resync with --robot-snapshot instead of guessing missed history.",
				Rationale: "Cursor expiry is a retention boundary with explicit recovery semantics, not a reason to fabricate state.",
			},
			{
				Name:      "unsupported_conditions_stay_explicit",
				Rule:      "Unsupported conditions such as bead_orphaned must stay explicit in help and capabilities until ntm can prove them from observable state.",
				Rationale: "Honest unsupported markers prevent false positives and coordinator-style overreach.",
			},
		},
	}
}

// =============================================================================
// Transport Liveness
// =============================================================================

// TransportLiveness defines expectations for long-lived connections.
type TransportLiveness struct {
	// HeartbeatIntervalMs is the expected interval between heartbeats.
	// Clients should reconnect if no message arrives within 2x this interval.
	HeartbeatIntervalMs int `json:"heartbeat_interval_ms"`

	// HeartbeatType is the event type used for heartbeats.
	HeartbeatType EventType `json:"heartbeat_type"`

	// ReconnectBackoffMs is the base backoff for reconnection attempts.
	ReconnectBackoffMs int `json:"reconnect_backoff_ms"`

	// MaxReconnectBackoffMs is the maximum backoff duration.
	MaxReconnectBackoffMs int `json:"max_reconnect_backoff_ms"`
}

// DefaultTransportLiveness provides recommended liveness settings.
var DefaultTransportLiveness = TransportLiveness{
	HeartbeatIntervalMs:   30000, // 30 seconds
	HeartbeatType:         "system.heartbeat",
	ReconnectBackoffMs:    1000,  // 1 second initial
	MaxReconnectBackoffMs: 30000, // 30 seconds max
}

// =============================================================================
// Golden Fixture Examples
// =============================================================================

// These are example payloads for testing and documentation.
// They represent the canonical format for each scenario.

// ExampleBootstrapSnapshot demonstrates --robot-snapshot output.
var ExampleBootstrapSnapshot = `{
  "success": true,
  "timestamp": "2026-03-20T15:30:45Z",
  "version": "1.0.0",
  "sessions": [
    {
      "name": "myproject",
      "panes": [
        {"index": 0, "type": "user", "state": "active"},
        {"index": 1, "type": "claude", "state": "idle", "agent": "cc_1"},
        {"index": 2, "type": "claude", "state": "generating", "agent": "cc_2"}
      ]
    }
  ],
  "latest_cursor": 142,
  "attention_contract_version": "1.0.0"
}`

// ExampleEventReplay demonstrates --robot-events output.
var ExampleEventReplay = `{
  "success": true,
  "timestamp": "2026-03-20T15:30:50Z",
  "version": "1.0.0",
  "events": [
    {
      "cursor": 143,
      "ts": "2026-03-20T15:30:46Z",
      "session": "myproject",
      "pane": 2,
      "category": "agent",
      "type": "agent.state_change",
      "source": "activity_tracker",
      "actionability": "background",
      "severity": "info",
      "summary": "Agent cc_2 started generating",
      "details": {"from_state": "idle", "to_state": "generating"}
    }
  ],
  "next_cursor": 144,
  "has_more": false
}`

// ExampleCursorExpired demonstrates CURSOR_EXPIRED error.
var ExampleCursorExpired = `{
  "success": false,
  "timestamp": "2026-03-20T15:30:45Z",
  "version": "1.0.0",
  "error": "Cursor 42 has expired",
  "error_code": "CURSOR_EXPIRED",
  "hint": "Run --robot-snapshot to resync, then resume from resync_cursor",
  "details": {
    "requested_cursor": 42,
    "earliest_cursor": 100,
    "retention_period": "1h",
    "resync_command": "ntm --robot-snapshot"
  }
}`

// ExampleDigest demonstrates --robot-digest output.
var ExampleDigest = `{
  "success": true,
  "timestamp": "2026-03-20T15:30:45Z",
  "version": "1.0.0",
  "window": "5m",
  "counts": {
    "agent": 12,
    "file": 5,
    "alert": 1
  },
  "highlights": [
    {
      "cursor": 140,
      "ts": "2026-03-20T15:28:00Z",
      "session": "myproject",
      "pane": 1,
      "category": "agent",
      "type": "agent.compacted",
      "actionability": "interesting",
      "severity": "warning",
      "summary": "Agent cc_1 context compacted (85% -> 40%)"
    }
  ],
  "attention_items": [],
  "agent_states": {
    "cc_1": "idle",
    "cc_2": "generating"
  }
}`

// ExampleAttention demonstrates --robot-attention output.
var ExampleAttention = `{
  "success": true,
  "timestamp": "2026-03-20T15:30:45Z",
  "version": "1.0.0",
  "items": [
    {
      "cursor": 138,
      "ts": "2026-03-20T15:25:00Z",
      "session": "myproject",
      "category": "file",
      "type": "file.conflict",
      "actionability": "action_required",
      "severity": "error",
      "summary": "File conflict detected: internal/robot/types.go modified by cc_1 and cc_2",
      "details": {
        "file": "internal/robot/types.go",
        "agents": ["cc_1", "cc_2"]
      },
      "next_actions": [
        {"action": "robot-diff", "args": "--session=myproject cc_1 cc_2", "reason": "Compare agent outputs"},
        {"action": "robot-locks", "args": "--session=myproject", "reason": "Check file reservations"}
      ]
    }
  ],
  "suggested_actions": [
    {"action": "robot-interrupt", "args": "--session=myproject --pane=2", "reason": "Stop conflicting agent"}
  ],
  "counts": {
    "error": 1,
    "warning": 0,
    "info": 0
  }
}`

// =============================================================================
// Validation Helpers
// =============================================================================

// ValidateEvent checks if an event conforms to the contract.
// Returns an error describing any violations.
func ValidateEvent(e *AttentionEvent) error {
	if e.Cursor <= 0 {
		return fmt.Errorf("cursor must be positive, got %d", e.Cursor)
	}
	if e.Ts == "" {
		return fmt.Errorf("ts is required")
	}
	if _, err := time.Parse(time.RFC3339, e.Ts); err != nil {
		return fmt.Errorf("ts must be RFC3339: %w", err)
	}
	if e.Category == "" {
		return fmt.Errorf("category is required")
	}
	if e.Type == "" {
		return fmt.Errorf("type is required")
	}
	if e.Actionability == "" {
		return fmt.Errorf("actionability is required")
	}
	if e.Severity == "" {
		return fmt.Errorf("severity is required")
	}
	if e.Summary == "" {
		return fmt.Errorf("summary is required")
	}
	return nil
}

// MarshalEvent serializes an event to JSON.
func MarshalEvent(e *AttentionEvent) ([]byte, error) {
	return json.Marshal(e)
}

// UnmarshalEvent deserializes an event from JSON.
func UnmarshalEvent(data []byte) (*AttentionEvent, error) {
	var e AttentionEvent
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// NewEvent creates a new event with required fields.
func NewEvent(cursor int64, category EventCategory, eventType EventType, summary string) *AttentionEvent {
	return &AttentionEvent{
		Cursor:        cursor,
		Ts:            FormatTimestamp(time.Now()),
		Category:      category,
		Type:          eventType,
		Actionability: ActionabilityBackground,
		Severity:      SeverityInfo,
		Summary:       summary,
		NextActions:   []NextAction{}, // Always initialize
	}
}
