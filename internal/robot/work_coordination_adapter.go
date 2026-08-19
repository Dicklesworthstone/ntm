// Package robot provides machine-readable output for AI agents.
// work_coordination_adapter.go normalizes work and coordination signals into canonical projection shapes.
//
// This adapter transforms beads/bv state, Agent Mail state, file conflicts,
// reservation conflicts, and handoff context into stable WorkSection and
// CoordinationSection structures. It hides the peculiarities of each source
// tool so robot surfaces can reason about coordination with one vocabulary.
//
// Bead: bd-j9jo3.3.2
package robot

import (
	"time"
)

// =============================================================================
// Section Types (from projection-section-model.md)
// =============================================================================

// WorkSection represents beads/task state.
type WorkSection struct {
	// Counts
	Total      int `json:"total"`
	Open       int `json:"open"`
	Ready      int `json:"ready"` // No blockers, claimable
	InProgress int `json:"in_progress"`
	Blocked    int `json:"blocked"`

	// Ready queue (top N)
	ReadyQueue []BeadRef `json:"ready_queue"` // Limit 5

	// In-flight work
	InFlight []InFlightWork `json:"in_flight,omitempty"`

	// Recent activity
	RecentlyClosed int `json:"recently_closed"` // Last 24h

	// Health
	StaleCount int `json:"stale_count"` // In-progress > threshold
	CycleCount int `json:"cycle_count"` // Dependency cycles
}

// BeadRef is a lightweight reference to a bead.
type BeadRef struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Priority int    `json:"priority"`
	Type     string `json:"type"` // task, bug, feature, epic
}

// InFlightWork represents work currently being done.
type InFlightWork struct {
	BeadID      string `json:"bead_id"`
	BeadTitle   string `json:"bead_title"`
	Agent       string `json:"agent,omitempty"` // session:pane if known
	StartedAt   string `json:"started_at"`
	DurationSec int    `json:"duration_sec"`
}

// CoordinationSection represents agent coordination state.
type CoordinationSection struct {
	// Mail
	Mail MailSummary `json:"mail"`

	// File reservations
	Reservations []ReservationInfo `json:"reservations,omitempty"`

	// Conflicts (if any)
	Conflicts []ConflictInfo `json:"conflicts,omitempty"`

	// Handoff state
	Handoff *HandoffInfo `json:"handoff,omitempty"`
}

// MailSummary provides Agent Mail metrics.
type MailSummary struct {
	Unread      int `json:"unread"`
	Urgent      int `json:"urgent"`
	AckRequired int `json:"ack_required"`
	ThreadCount int `json:"thread_count"`

	// Recent messages (top N)
	Recent []MailRef `json:"recent,omitempty"` // Limit 3
}

// MailRef is a lightweight reference to a mail message.
type MailRef struct {
	ID         string `json:"id"`
	From       string `json:"from"`
	Subject    string `json:"subject"`
	Urgent     bool   `json:"urgent"`
	ReceivedAt string `json:"received_at"`
}

// ReservationInfo describes a file reservation.
type ReservationInfo struct {
	Agent     string   `json:"agent"`
	Patterns  []string `json:"patterns"`
	Exclusive bool     `json:"exclusive"`
	ExpiresAt string   `json:"expires_at"`
	Reason    string   `json:"reason,omitempty"` // Usually bead ID
}

// ConflictInfo describes a detected conflict.
type ConflictInfo struct {
	ID         string   `json:"id"`
	Type       string   `json:"type"` // file_conflict, reservation_conflict
	Files      []string `json:"files,omitempty"`
	Agents     []string `json:"agents"`
	DetectedAt string   `json:"detected_at"`
	Resolved   bool     `json:"resolved"`
}

// HandoffInfo provides handoff context.
type HandoffInfo struct {
	Session    string `json:"session"`
	Goal       string `json:"goal,omitempty"`
	Now        string `json:"now,omitempty"`
	Path       string `json:"path,omitempty"`
	AgeSeconds int64  `json:"age_seconds,omitempty"`
	Status     string `json:"status,omitempty"`
}

// =============================================================================
// Configuration
// =============================================================================

// WorkCoordinationAdapterConfig controls the adapter behavior.
type WorkCoordinationAdapterConfig struct {
	// ReadyQueueLimit is the max number of ready beads to include.
	// Default: 5
	ReadyQueueLimit int

	// RecentMailLimit is the max number of recent mail items.
	// Default: 3
	RecentMailLimit int

	// StaleThresholdMinutes is when in-progress work is considered stale.
	// Default: 120 (2 hours)
	StaleThresholdMinutes int
}

// =============================================================================
// Adapter
// =============================================================================

// WorkCoordinationAdapter normalizes work and coordination data.
type WorkCoordinationAdapter struct {
	config WorkCoordinationAdapterConfig
}

// =============================================================================
// Work Section Normalization
// =============================================================================

// =============================================================================
// Coordination Section Normalization
// =============================================================================

// AgentMailData represents raw Agent Mail data for normalization.
type AgentMailData struct {
	Inbox []InboxMessage
}

// InboxMessage represents a message in the inbox.
type InboxMessage struct {
	ID          string    `json:"id"`
	Subject     string    `json:"subject"`
	From        string    `json:"from"`
	Importance  string    `json:"importance"`
	AckRequired bool      `json:"ack_required"`
	CreatedTS   time.Time `json:"created_ts"`
	ThreadID    string    `json:"thread_id"`
}

// ReservationData represents raw reservation data.
type ReservationData struct {
	Agent     string
	Patterns  []string
	Exclusive bool
	ExpiresAt time.Time
	Reason    string
}

// =============================================================================
// Combined Normalization
// =============================================================================

// WorkCoordinationSnapshot holds both sections.
type WorkCoordinationSnapshot struct {
	Work         WorkSection         `json:"work"`
	Coordination CoordinationSection `json:"coordination"`
	CollectedAt  time.Time           `json:"collected_at"`
}

// =============================================================================
// Helpers
// =============================================================================
