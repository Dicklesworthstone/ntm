// Package state provides durable SQLite-backed storage for NTM orchestration state.
// runtime_schema.go defines types for the runtime projection layer.
//
// These types represent the cached/derived state used by robot surfaces.
// They are projections from external sources (tmux, beads, mail, etc.) and
// support restart-safe operation and efficient robot queries.
//
// Bead: bd-j9jo3.2.2
package state

import (
	"time"
)

// =============================================================================
// Health and Status Enums
// =============================================================================

// HealthStatus represents the health state of a resource.
type HealthStatus string

const (
	HealthStatusHealthy  HealthStatus = "healthy"
	HealthStatusWarning  HealthStatus = "warning"
	HealthStatusCritical HealthStatus = "critical"
	HealthStatusUnknown  HealthStatus = "unknown"
)

// AgentState represents the operational state of an agent.
type AgentState string

const (
	AgentStateIdle       AgentState = "idle"
	AgentStateActive     AgentState = "active"
	AgentStateBusy       AgentState = "busy"
	AgentStateError      AgentState = "error"
	AgentStateCompacting AgentState = "compacting"
	AgentStateUnknown    AgentState = "unknown"
)

// SourceStatus represents the availability status of a data source.
type SourceStatus string

const (
	SourceStatusFresh       SourceStatus = "fresh"
	SourceStatusStale       SourceStatus = "stale"
	SourceStatusUnavailable SourceStatus = "unavailable"
	SourceStatusUnknown     SourceStatus = "unknown"
)

// Actionability represents how urgently an attention event needs response.
type Actionability string

const (
	ActionabilityUrgent         Actionability = "urgent"
	ActionabilityActionRequired Actionability = "action_required"
	ActionabilityInteresting    Actionability = "interesting"
	ActionabilityBackground     Actionability = "background"
)

// Severity represents the severity level of an event.
type Severity string

const (
	SeverityDebug    Severity = "debug"
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityError    Severity = "error"
	SeverityCritical Severity = "critical"
)

// IncidentStatus represents the lifecycle state of an incident.
type IncidentStatus string

const (
	IncidentStatusOpen          IncidentStatus = "open"
	IncidentStatusInvestigating IncidentStatus = "investigating"
	IncidentStatusResolved      IncidentStatus = "resolved"
	IncidentStatusMuted         IncidentStatus = "muted"
)

// RetentionClass determines how long an audit event is retained.
type RetentionClass string

const (
	RetentionClassStandard  RetentionClass = "standard"  // 7 days
	RetentionClassExtended  RetentionClass = "extended"  // 30 days
	RetentionClassPermanent RetentionClass = "permanent" // Never expires
)

// =============================================================================
// Runtime Session
// =============================================================================

// RuntimeSession is a cached projection of session state.
type RuntimeSession struct {
	// Identity
	Name        string `json:"name"`
	Label       string `json:"label,omitempty"`
	ProjectPath string `json:"project_path,omitempty"`

	// State
	Attached    bool `json:"attached"`
	WindowCount int  `json:"window_count"`
	PaneCount   int  `json:"pane_count"`

	// Agent summary
	AgentCount   int `json:"agent_count"`
	ActiveAgents int `json:"active_agents"`
	IdleAgents   int `json:"idle_agents"`
	ErrorAgents  int `json:"error_agents"`

	// Health
	HealthStatus HealthStatus `json:"health_status"`
	HealthReason string       `json:"health_reason,omitempty"`

	// Timestamps
	CreatedAt      *time.Time `json:"created_at,omitempty"`
	LastAttachedAt *time.Time `json:"last_attached_at,omitempty"`
	LastActivityAt *time.Time `json:"last_activity_at,omitempty"`

	// Freshness
	CollectedAt time.Time `json:"collected_at"`
	StaleAfter  time.Time `json:"stale_after"`
}

// IsFresh returns true if the projection is within its staleness threshold.
func (r *RuntimeSession) IsFresh() bool {
	return time.Now().Before(r.StaleAfter)
}

// =============================================================================
// Runtime Agent
// =============================================================================

// RuntimeAgent is a cached projection of agent state.
type RuntimeAgent struct {
	// Identity
	ID          string `json:"id"` // session:pane format
	SessionName string `json:"session_name"`
	Pane        string `json:"pane"`

	// Type detection
	AgentType      string  `json:"agent_type"` // claude, codex, gemini, user, unknown
	Variant        string  `json:"variant,omitempty"`
	TypeConfidence float64 `json:"type_confidence"`
	TypeMethod     string  `json:"type_method"` // process, title, output, manual

	// State
	State          AgentState `json:"state"`
	StateReason    string     `json:"state_reason,omitempty"`
	PreviousState  string     `json:"previous_state,omitempty"`
	StateChangedAt *time.Time `json:"state_changed_at,omitempty"`

	// Activity
	LastOutputAt     *time.Time `json:"last_output_at,omitempty"`
	LastOutputAgeSec int        `json:"last_output_age_sec"`
	OutputTailLines  int        `json:"output_tail_lines"`

	// Coordination
	CurrentBead   string `json:"current_bead,omitempty"`
	PendingMail   int    `json:"pending_mail"`
	AgentMailName string `json:"agent_mail_name,omitempty"`

	// Health
	HealthStatus HealthStatus `json:"health_status"`
	HealthReason string       `json:"health_reason,omitempty"`

	// Freshness
	CollectedAt time.Time `json:"collected_at"`
	StaleAfter  time.Time `json:"stale_after"`
}

// IsFresh returns true if the projection is within its staleness threshold.
func (r *RuntimeAgent) IsFresh() bool {
	return time.Now().Before(r.StaleAfter)
}

// =============================================================================
// Runtime Work (Beads)
// =============================================================================

// RuntimeWork is a cached projection of bead/work state.
type RuntimeWork struct {
	// Identity
	BeadID string `json:"bead_id"`

	// Content
	Title           string `json:"title"`
	TitleDisclosure string `json:"title_disclosure,omitempty"` // JSON DisclosureMetadata
	Status          string `json:"status"`                     // open, in_progress, closed
	Priority        int    `json:"priority"`
	BeadType        string `json:"bead_type"`

	// Assignment
	Assignee  string     `json:"assignee,omitempty"`
	ClaimedAt *time.Time `json:"claimed_at,omitempty"`

	// Dependencies
	BlockedByCount int `json:"blocked_by_count"`
	UnblocksCount  int `json:"unblocks_count"`

	// Labels (JSON array string)
	Labels string `json:"labels,omitempty"`

	// Triage
	Score       *float64 `json:"score,omitempty"`
	ScoreReason string   `json:"score_reason,omitempty"`

	// Freshness
	CollectedAt time.Time `json:"collected_at"`
	StaleAfter  time.Time `json:"stale_after"`
}

// IsFresh returns true if the projection is within its staleness threshold.
func (r *RuntimeWork) IsFresh() bool {
	return time.Now().Before(r.StaleAfter)
}

// =============================================================================
// Runtime Coordination (Agent Mail)
// =============================================================================

// RuntimeCoordination is a cached projection of Agent Mail state.
type RuntimeCoordination struct {
	// Identity
	AgentName string `json:"agent_name"`

	// Pane association
	SessionName string `json:"session_name,omitempty"`
	Pane        string `json:"pane,omitempty"`

	// Mail state
	UnreadCount                  int    `json:"unread_count"`
	PendingAckCount              int    `json:"pending_ack_count"`
	UrgentCount                  int    `json:"urgent_count"`
	LastMessageSubject           string `json:"last_message_subject,omitempty"`
	LastMessageSubjectDisclosure string `json:"last_message_subject_disclosure,omitempty"` // JSON DisclosureMetadata
	LastMessagePreview           string `json:"last_message_preview,omitempty"`
	LastMessagePreviewDisclosure string `json:"last_message_preview_disclosure,omitempty"` // JSON DisclosureMetadata

	// Last activity
	LastMessageAt  *time.Time `json:"last_message_at,omitempty"`
	LastSentAt     *time.Time `json:"last_sent_at,omitempty"`
	LastReceivedAt *time.Time `json:"last_received_at,omitempty"`

	// Freshness
	CollectedAt time.Time `json:"collected_at"`
	StaleAfter  time.Time `json:"stale_after"`
}

// IsFresh returns true if the projection is within its staleness threshold.
func (r *RuntimeCoordination) IsFresh() bool {
	return time.Now().Before(r.StaleAfter)
}

// =============================================================================
// Runtime Handoff
// =============================================================================

// RuntimeHandoff is a cached projection of the latest normalized handoff state.
type RuntimeHandoff struct {
	SessionName        string     `json:"session_name"`
	Status             string     `json:"status,omitempty"`
	Goal               string     `json:"goal,omitempty"`
	GoalDisclosure     string     `json:"goal_disclosure,omitempty"` // JSON DisclosureMetadata
	NowText            string     `json:"now,omitempty"`
	NowDisclosure      string     `json:"now_disclosure,omitempty"` // JSON DisclosureMetadata
	UpdatedAt          *time.Time `json:"updated_at,omitempty"`
	ActiveBeads        string     `json:"active_beads,omitempty"`        // JSON []string
	AgentMailThreads   string     `json:"agent_mail_threads,omitempty"`  // JSON []string
	Blockers           string     `json:"blockers,omitempty"`            // JSON []string
	BlockerDisclosures string     `json:"blocker_disclosures,omitempty"` // JSON []DisclosureMetadata
	Files              string     `json:"files,omitempty"`               // JSON []string
	CollectedAt        time.Time  `json:"collected_at"`
	StaleAfter         time.Time  `json:"stale_after"`
}

// IsFresh returns true if the handoff projection is within its staleness threshold.
func (r *RuntimeHandoff) IsFresh() bool {
	return time.Now().Before(r.StaleAfter)
}

// =============================================================================
// Runtime Quota
// =============================================================================

// RuntimeQuotaUsedPctSource identifies where runtime_quota.used_pct came from.
type RuntimeQuotaUsedPctSource string

const (
	RuntimeQuotaUsedPctSourceUnknown  RuntimeQuotaUsedPctSource = "unknown"
	RuntimeQuotaUsedPctSourceProvider RuntimeQuotaUsedPctSource = "provider"
	RuntimeQuotaUsedPctSourceTokens   RuntimeQuotaUsedPctSource = "tokens"
	RuntimeQuotaUsedPctSourceRequests RuntimeQuotaUsedPctSource = "requests"
)

// RuntimeQuota is a cached projection of API rate limit state.
type RuntimeQuota struct {
	// Identity
	Provider string `json:"provider"` // anthropic, openai, google
	Account  string `json:"account"`

	// State
	LimitHit      bool                      `json:"limit_hit"`
	UsedPct       float64                   `json:"used_pct"`
	UsedPctKnown  bool                      `json:"used_pct_known"`
	UsedPctSource RuntimeQuotaUsedPctSource `json:"used_pct_source"`
	ResetsAt      *time.Time                `json:"resets_at,omitempty"`

	// Active account
	IsActive bool `json:"is_active"`

	// Health
	Healthy      bool   `json:"healthy"`
	HealthReason string `json:"health_reason,omitempty"`

	// Freshness
	CollectedAt time.Time `json:"collected_at"`
	StaleAfter  time.Time `json:"stale_after"`
}

// IsFresh returns true if the projection is within its staleness threshold.
func (r *RuntimeQuota) IsFresh() bool {
	return time.Now().Before(r.StaleAfter)
}

// =============================================================================
// Source Health
// =============================================================================

// SourceHealth records the availability status of a data source.
type SourceHealth struct {
	// Identity
	SourceName string `json:"source_name"` // beads, cass, mail, caut, tmux, rch

	// Availability
	Available bool   `json:"available"`
	Healthy   bool   `json:"healthy"`
	Reason    string `json:"reason,omitempty"`

	// Timing
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
	LastFailureAt *time.Time `json:"last_failure_at,omitempty"`
	LastCheckAt   time.Time  `json:"last_check_at"`

	// Latency
	LatencyMs    *int `json:"latency_ms,omitempty"`
	AvgLatencyMs *int `json:"avg_latency_ms,omitempty"`

	// Version
	Version string `json:"version,omitempty"`

	// Error tracking
	ConsecutiveFailures int    `json:"consecutive_failures"`
	LastError           string `json:"last_error,omitempty"`
	LastErrorCode       string `json:"last_error_code,omitempty"`
}

// Status returns the computed SourceStatus based on availability and freshness.
func (s *SourceHealth) Status(staleAfter time.Duration) SourceStatus {
	if !s.Available {
		return SourceStatusUnavailable
	}
	if time.Since(s.LastCheckAt) > staleAfter {
		return SourceStatusStale
	}
	if s.Healthy {
		return SourceStatusFresh
	}
	return SourceStatusStale
}

// =============================================================================
// Attention Event (Stored)
// =============================================================================

// StoredAttentionEvent is the SQLite-persisted form of an attention event.
type StoredAttentionEvent struct {
	// Cursor is the monotonic sequence number (PRIMARY KEY).
	Cursor int64 `json:"cursor"`

	// Timestamp
	Ts time.Time `json:"ts"`

	// Scope
	SessionName string `json:"session_name,omitempty"`
	Pane        string `json:"pane,omitempty"`

	// Classification
	Category  string `json:"category"`   // agent, session, bead, alert, mail, incident
	EventType string `json:"event_type"` // state_change, error_detected, ready, etc.
	Source    string `json:"source"`     // activity_detector, health_monitor, etc.

	// Priority
	Actionability Actionability `json:"actionability"`
	Severity      Severity      `json:"severity"`
	ReasonCode    string        `json:"reason_code,omitempty"`

	// Content
	Summary     string `json:"summary"`
	Details     string `json:"details,omitempty"`      // JSON
	NextActions string `json:"next_actions,omitempty"` // JSON array

	// Dedup
	DedupKey   string `json:"dedup_key,omitempty"`
	DedupCount int    `json:"dedup_count"`

	// Retention
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// AttentionState represents durable operator state for an attention item.
type AttentionState string

const (
	AttentionStateNew          AttentionState = "new"
	AttentionStateSeen         AttentionState = "seen"
	AttentionStateAcknowledged AttentionState = "acknowledged"
	AttentionStateSnoozed      AttentionState = "snoozed"
	AttentionStateDismissed    AttentionState = "dismissed"
)

// AttentionItemState is durable operator-controlled state for an attention item.
// Item identity is stable across recurrence when the source event has a dedup key;
// otherwise it falls back to a cursor-scoped key for one-off events.
type AttentionItemState struct {
	ItemKey  string `json:"item_key"`
	DedupKey string `json:"dedup_key,omitempty"`

	State AttentionState `json:"state"`

	Fingerprint string `json:"fingerprint,omitempty"`

	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
	AcknowledgedBy string     `json:"acknowledged_by,omitempty"`

	SnoozedUntil *time.Time `json:"snoozed_until,omitempty"`

	DismissedAt *time.Time `json:"dismissed_at,omitempty"`
	DismissedBy string     `json:"dismissed_by,omitempty"`

	Pinned   bool       `json:"pinned"`
	PinnedAt *time.Time `json:"pinned_at,omitempty"`
	PinnedBy string     `json:"pinned_by,omitempty"`

	Muted   bool       `json:"muted"`
	MutedAt *time.Time `json:"muted_at,omitempty"`
	MutedBy string     `json:"muted_by,omitempty"`

	OverridePriority  string     `json:"override_priority,omitempty"`
	OverrideReason    string     `json:"override_reason,omitempty"`
	OverrideExpiresAt *time.Time `json:"override_expires_at,omitempty"`

	ResurfacingPolicy string `json:"resurfacing_policy,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AttentionReplayWindow describes the currently replayable cursor window.
// It excludes expired rows so callers can make explicit cursor-expiry decisions.
type AttentionReplayWindow struct {
	OldestCursor int64 `json:"oldest_cursor"`
	NewestCursor int64 `json:"newest_cursor"`
	EventCount   int   `json:"event_count"`
}

// EarliestReplayCursor returns the earliest cursor a caller can safely replay
// from without losing events in the current retention window.
func (w AttentionReplayWindow) EarliestReplayCursor() int64 {
	if w.EventCount == 0 {
		return w.NewestCursor
	}
	return w.OldestCursor
}

// CursorExpired reports whether a replay request from sinceCursor has fallen
// behind the retained attention-event window.
func (w AttentionReplayWindow) CursorExpired(sinceCursor int64) bool {
	if sinceCursor <= 0 || w.NewestCursor == 0 {
		return false
	}
	if w.EventCount == 0 {
		return sinceCursor < w.NewestCursor
	}
	return sinceCursor < w.OldestCursor-1
}

// =============================================================================
// Incident
// =============================================================================

// Incident represents a tracked incident with lifecycle.
type Incident struct {
	// Identity
	ID          string `json:"id"`
	Title       string `json:"title"`
	Fingerprint string `json:"fingerprint"`
	Family      string `json:"family"`
	Category    string `json:"category"`

	// State
	Status   IncidentStatus `json:"status"`
	Severity Severity       `json:"severity"`

	// Scope (JSON arrays)
	SessionNames string `json:"session_names,omitempty"` // JSON array
	AgentIDs     string `json:"agent_ids,omitempty"`     // JSON array

	// Aggregation
	AlertCount       int    `json:"alert_count"`
	EventCount       int    `json:"event_count"`
	FirstEventCursor *int64 `json:"first_event_cursor,omitempty"`
	LastEventCursor  *int64 `json:"last_event_cursor,omitempty"`

	// Timeline
	StartedAt      time.Time  `json:"started_at"`
	LastEventAt    time.Time  `json:"last_event_at"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
	AcknowledgedBy string     `json:"acknowledged_by,omitempty"`
	ResolvedAt     *time.Time `json:"resolved_at,omitempty"`
	ResolvedBy     string     `json:"resolved_by,omitempty"`
	MutedAt        *time.Time `json:"muted_at,omitempty"`
	MutedBy        string     `json:"muted_by,omitempty"`
	MutedReason    string     `json:"muted_reason,omitempty"`

	// Investigation
	RootCause  string `json:"root_cause,omitempty"`
	Resolution string `json:"resolution,omitempty"`
	Notes      string `json:"notes,omitempty"`
}

// =============================================================================
// Output Watermark
// =============================================================================

// OutputWatermark tracks cursor positions for incremental queries.
type OutputWatermark struct {
	// Identity
	WatermarkType string `json:"watermark_type"` // events, alerts, incidents
	Scope         string `json:"scope"`          // global, session:name, agent:id

	// Cursor
	LastCursor int64      `json:"last_cursor"`
	LastTs     *time.Time `json:"last_ts,omitempty"`

	// Baseline
	BaselineCursor *int64     `json:"baseline_cursor,omitempty"`
	BaselineTs     *time.Time `json:"baseline_ts,omitempty"`
	BaselineHash   string     `json:"baseline_hash,omitempty"`

	// Metadata
	Consumer  string    `json:"consumer,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// =============================================================================
// Audit Event
// =============================================================================

// AuditEvent represents a logged audit entry with actor attribution.
type AuditEvent struct {
	// Identity
	ID int64 `json:"id"`

	// Timestamp
	Ts time.Time `json:"ts"`

	// Actor attribution
	ActorType   string `json:"actor_type"` // agent, user, system, api
	ActorID     string `json:"actor_id,omitempty"`
	ActorOrigin string `json:"actor_origin,omitempty"`

	// Request correlation
	RequestID     string `json:"request_id,omitempty"`
	CorrelationID string `json:"correlation_id,omitempty"`

	// Event classification
	Category  string   `json:"category"`   // attention, actuation, incident, disclosure
	EventType string   `json:"event_type"` // ack, pin, snooze, override, mute, resolve
	Severity  Severity `json:"severity"`

	// Affected entity
	EntityType string `json:"entity_type"` // session, agent, bead, alert, incident
	EntityID   string `json:"entity_id"`

	// State change
	PreviousState string `json:"previous_state,omitempty"` // JSON
	NewState      string `json:"new_state,omitempty"`      // JSON
	ChangeSummary string `json:"change_summary"`

	// Decision context
	Reason          string `json:"reason,omitempty"`
	Evidence        string `json:"evidence,omitempty"` // JSON
	DisclosureState string `json:"disclosure_state,omitempty"`

	// Retention
	RetentionClass RetentionClass `json:"retention_class"`
	ExpiresAt      *time.Time     `json:"expires_at,omitempty"`
}

// =============================================================================
// Audit Actor
// =============================================================================

// AuditActor records known actors for correlation.
type AuditActor struct {
	// Identity
	ActorType string `json:"actor_type"`
	ActorID   string `json:"actor_id"`

	// Display
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`

	// Metadata
	FirstSeenAt  time.Time `json:"first_seen_at"`
	LastSeenAt   time.Time `json:"last_seen_at"`
	EventCount   int       `json:"event_count"`
	KnownOrigins string    `json:"known_origins,omitempty"` // JSON array
}

// =============================================================================
// Audit Decision
// =============================================================================

// AuditDecision is a compact summary of an operator decision.
type AuditDecision struct {
	// Identity
	ID int64 `json:"id"`

	// Decision
	DecisionType string    `json:"decision_type"` // ack, pin, snooze, override, mute, resolve
	DecisionAt   time.Time `json:"decision_at"`

	// Actor
	ActorType string `json:"actor_type"`
	ActorID   string `json:"actor_id,omitempty"`

	// Target
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`

	// Context
	Reason    string     `json:"reason,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`

	// Reference
	AuditEventID *int64 `json:"audit_event_id,omitempty"`
}
