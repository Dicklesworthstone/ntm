// Package agentmail provides a Go HTTP client for the MCP Agent Mail API.
// Agent Mail enables coordination between AI coding agents through messaging,
// file reservations, and project management.
package agentmail

import (
	"encoding/json"
	"time"
)

// FlexTime is a time.Time that can unmarshal from both RFC3339 (with timezone)
// and ISO8601 timestamps without timezone suffix (assumes UTC).
type FlexTime struct {
	time.Time
}

// UnmarshalJSON implements json.Unmarshaler for FlexTime.
func (ft *FlexTime) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if s == "" {
		ft.Time = time.Time{}
		return nil
	}

	// Try RFC3339 first (has timezone)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		ft.Time = t
		return nil
	}

	// Try RFC3339Nano (has timezone with nanoseconds)
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		ft.Time = t
		return nil
	}

	// Fall back to ISO8601 without timezone (assume UTC)
	// Format: "2006-01-02T15:04:05" or "2006-01-02T15:04:05.999999"
	layouts := []string{
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05.999999",
		"2006-01-02T15:04:05.999",
		"2006-01-02T15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			ft.Time = t.UTC()
			return nil
		}
	}

	return &time.ParseError{Layout: time.RFC3339, Value: s, LayoutElem: "", ValueElem: "", Message: ""}
}

// MarshalJSON implements json.Marshaler for FlexTime.
func (ft FlexTime) MarshalJSON() ([]byte, error) {
	return json.Marshal(ft.Time.Format(time.RFC3339Nano))
}

// Agent represents an AI coding agent registered with Agent Mail.
type Agent struct {
	ID              int       `json:"id"`
	Name            string    `json:"name"`             // e.g., "GreenCastle"
	Program         string    `json:"program"`          // e.g., "claude-code"
	Model           string    `json:"model"`            // e.g., "opus-4.5"
	TaskDescription string    `json:"task_description"` // Current task description
	InceptionTS     time.Time `json:"inception_ts"`     // When agent was first registered
	LastActiveTS    time.Time `json:"last_active_ts"`   // Last activity timestamp
	ProjectID       int       `json:"project_id"`       // Associated project ID
}

// Message represents an Agent Mail message.
type Message struct {
	ID          int       `json:"id"`
	ProjectID   int       `json:"project_id"`
	SenderID    int       `json:"sender_id"`
	ThreadID    *string   `json:"thread_id,omitempty"`
	Subject     string    `json:"subject"`
	BodyMD      string    `json:"body_md"` // Markdown body
	From        string    `json:"from"`    // Sender agent name
	To          []string  `json:"to"`
	CC          []string  `json:"cc,omitempty"`
	BCC         []string  `json:"bcc,omitempty"`
	Importance  string    `json:"importance"`   // normal, high, urgent
	AckRequired bool      `json:"ack_required"` // Whether recipient must acknowledge
	CreatedTS   time.Time `json:"created_ts"`
	Kind        string    `json:"kind,omitempty"` // to, cc, bcc
}

// Project represents an Agent Mail project.
type Project struct {
	ID        int       `json:"id"`
	Slug      string    `json:"slug"`
	HumanKey  string    `json:"human_key"` // Absolute path to project
	CreatedAt time.Time `json:"created_at"`
}

// FileReservation represents a file path reservation (advisory lock).
type FileReservation struct {
	ID          int        `json:"id"`
	PathPattern string     `json:"path_pattern"` // Path or glob pattern
	AgentName   string     `json:"agent_name"`
	ProjectID   int        `json:"project_id"`
	Exclusive   bool       `json:"exclusive"` // Exclusive or shared
	Reason      string     `json:"reason"`
	ExpiresTS   time.Time  `json:"expires_ts"`
	CreatedTS   time.Time  `json:"created_ts"`
	ReleasedTS  *time.Time `json:"released_ts,omitempty"`
}

// InboxMessage represents a message in an agent's inbox.
type InboxMessage struct {
	ID          int        `json:"id"`
	Subject     string     `json:"subject"`
	From        string     `json:"from"`
	CreatedTS   time.Time  `json:"created_ts"`
	ThreadID    *string    `json:"thread_id,omitempty"`
	Importance  string     `json:"importance"`
	AckRequired bool       `json:"ack_required"`
	Kind        string     `json:"kind"`
	BodyMD      string     `json:"body_md,omitempty"` // Only if include_bodies=true
	ReadAt      *time.Time `json:"read_at,omitempty"`
}

// ContactLink represents a contact relationship between agents.
type ContactLink struct {
	FromAgent string     `json:"from_agent,omitempty"`
	ToAgent   string     `json:"to_agent,omitempty"`
	To        string     `json:"to,omitempty"`
	Status    string     `json:"status,omitempty"`
	Reason    string     `json:"reason,omitempty"`
	Approved  bool       `json:"approved,omitempty"`
	UpdatedTS *time.Time `json:"updated_ts,omitempty"`
	ExpiresTS *time.Time `json:"expires_ts,omitempty"`
}

// ThreadSummary contains summary information for a message thread.
type ThreadSummary struct {
	ThreadID     string   `json:"thread_id"`
	Participants []string `json:"participants"`
	KeyPoints    []string `json:"key_points"`
	ActionItems  []string `json:"action_items"`
}

// SendResult contains the result of sending a message.
type SendResult struct {
	Deliveries []MessageDelivery `json:"deliveries"`
	Count      int               `json:"count"`
}

// MessageDelivery represents a single delivery in SendResult.
type MessageDelivery struct {
	Project string   `json:"project"`
	Payload *Message `json:"payload"`
}

// HealthStatus represents the Agent Mail server health check response.
type HealthStatus struct {
	Status    string `json:"status"`
	Timestamp string `json:"timestamp,omitempty"`
}

// SessionStartResult contains the result of macro_start_session.
type SessionStartResult struct {
	Project          *Project           `json:"project"`
	Agent            *Agent             `json:"agent"`
	FileReservations *ReservationResult `json:"file_reservations"`
	Inbox            []InboxMessage     `json:"inbox"`
}

// ReservationResult contains the result of file path reservations.
type ReservationResult struct {
	Granted   []FileReservation     `json:"granted"`
	Conflicts []ReservationConflict `json:"conflicts"`
}

// ReservationConflict represents a file reservation conflict.
type ReservationConflict struct {
	Path    string   `json:"path"`
	Holders []string `json:"holders"`
}

// RegisterAgentOptions contains options for registering an agent.
type RegisterAgentOptions struct {
	ProjectKey      string
	Program         string
	Model           string
	Name            string // Optional; auto-generated if empty
	TaskDescription string
}

// SendMessageOptions contains options for sending a message.
type SendMessageOptions struct {
	ProjectKey    string
	SenderName    string
	To            []string
	Subject       string
	BodyMD        string
	CC            []string
	BCC           []string
	Importance    string // normal, high, urgent
	AckRequired   bool
	ThreadID      string
	ConvertImages *bool
}

// ReplyMessageOptions contains options for replying to a message.
type ReplyMessageOptions struct {
	ProjectKey    string
	MessageID     int
	SenderName    string
	BodyMD        string
	To            []string // Optional; defaults to original sender
	CC            []string
	BCC           []string
	SubjectPrefix string // Default: "Re:"
}

// FetchInboxOptions contains options for fetching inbox messages.
type FetchInboxOptions struct {
	ProjectKey    string
	AgentName     string
	UrgentOnly    bool
	SinceTS       *time.Time
	Limit         int
	IncludeBodies bool
}

// FileReservationOptions contains options for reserving file paths.
type FileReservationOptions struct {
	ProjectKey string
	AgentName  string
	Paths      []string
	TTLSeconds int
	Exclusive  bool
	Reason     string
}

// SearchOptions contains options for message search.
type SearchOptions struct {
	ProjectKey string
	Query      string
	Limit      int
}

// SearchResult represents a message search result.
type SearchResult struct {
	ID          int       `json:"id"`
	Subject     string    `json:"subject"`
	Importance  string    `json:"importance"`
	AckRequired bool      `json:"ack_required"`
	CreatedTS   time.Time `json:"created_ts"`
	ThreadID    *string   `json:"thread_id"`
	From        string    `json:"from"`
}

// OverseerMessageOptions contains options for sending a Human Overseer message.
// Human Overseer messages bypass contact policies and are auto-marked as high importance.
type OverseerMessageOptions struct {
	ProjectSlug string   // Project slug (derived from project path)
	Recipients  []string // Agent names to send to
	Subject     string   // Subject line (max 200 chars)
	BodyMD      string   // Markdown body (max 49,600 chars)
	ThreadID    string   // Optional thread ID for conversation continuity
}

// OverseerSendResult contains the result of sending a Human Overseer message.
type OverseerSendResult struct {
	Success    bool     `json:"success"`
	MessageID  int      `json:"message_id"`
	Recipients []string `json:"recipients"`
	SentAt     FlexTime `json:"sent_at"`
}

// PrepareThreadOptions contains options for the macro_prepare_thread call.
// Boolean pointers allow distinguishing "not set" from "explicitly set to false".
// When nil, server defaults are used (include_examples=true, llm_mode=true, register_if_missing=true).
type PrepareThreadOptions struct {
	ProjectKey         string // Absolute path to project
	ThreadID           string // Thread to prepare for (e.g., "FEAT-123")
	Program            string // e.g., "claude-code"
	Model              string // e.g., "opus-4.5"
	AgentName          string // Optional; auto-generated if empty
	IncludeExamples    *bool  // Include sample messages from thread (default: true)
	IncludeInboxBodies *bool  // Include full body in inbox messages (default: false)
	InboxLimit         int    // Max inbox messages to fetch
	LLMMode            *bool  // Use LLM to refine summary (default: true)
	LLMModel           string // Override LLM model for summary
	RegisterIfMissing  *bool  // Register agent if not already registered (default: true)
	TaskDescription    string // Current task description
}

// PrepareThreadResult contains the result of macro_prepare_thread.
type PrepareThreadResult struct {
	Agent         *Agent         `json:"agent"`
	ThreadSummary *ThreadSummary `json:"thread_summary"`
	Examples      []InboxMessage `json:"examples,omitempty"`
	Inbox         []InboxMessage `json:"inbox"`
}

// ContactHandshakeOptions contains options for the macro_contact_handshake call.
type ContactHandshakeOptions struct {
	ProjectKey      string // Absolute path to project
	AgentName       string // Your agent name (optional, auto-generated if empty)
	ToAgent         string // Target agent to contact
	ToProject       string // Target project (optional, same project if empty)
	Reason          string // Reason for contact request
	Program         string // Your program (optional, for auto-registration)
	Model           string // Your model (optional, for auto-registration)
	TaskDescription string // Your task description (optional)
	AutoAccept      bool   // Auto-accept the contact (requires mutual)
	WelcomeSubject  string // Subject for welcome message (optional)
	WelcomeBody     string // Body for welcome message (optional)
	TTLSeconds      int    // TTL for contact approval
}

// ContactHandshakeResult contains the result of macro_contact_handshake.
type ContactHandshakeResult struct {
	Agent         *Agent       `json:"agent,omitempty"`
	ContactStatus string       `json:"contact_status"` // "approved", "pending", "denied"
	Link          *ContactLink `json:"link,omitempty"`
	WelcomeMsg    *Message     `json:"welcome_message,omitempty"`
}

// RequestContactOptions contains options for requesting contact with an agent.
type RequestContactOptions struct {
	ProjectKey string
	FromAgent  string
	ToAgent    string
	ToProject  string
	Reason     string
	TTLSeconds int
}

// RespondContactOptions contains options for responding to a contact request.
type RespondContactOptions struct {
	ProjectKey string
	ToAgent    string
	FromAgent  string
	Accept     bool
	TTLSeconds int
}

// SummarizeThreadOptions contains options for summarizing a thread.
type SummarizeThreadOptions struct {
	ProjectKey      string
	ThreadID        string
	IncludeExamples bool
	LLMMode         bool
	LLMModel        string
}

// ThreadSummaryResponse contains the summarize_thread response for a single thread.
type ThreadSummaryResponse struct {
	ThreadID string        `json:"thread_id"`
	Summary  ThreadSummary `json:"summary"`
	Examples []InboxMessage `json:"examples,omitempty"`
}

// RenewReservationsOptions contains options for renewing file reservations.
type RenewReservationsOptions struct {
	ProjectKey     string
	AgentName      string
	ExtendSeconds  int
	ReservationIDs []int
	Paths          []string
}

// RenewReservationsResult contains the result of renewing reservations.
type RenewReservationsResult struct {
	Renewed      int                 `json:"renewed"`
	Reservations []RenewedReservation `json:"reservations"`
}

// RenewedReservation contains info about a renewed reservation.
type RenewedReservation struct {
	ID           int       `json:"id"`
	PathPattern  string    `json:"path_pattern"`
	OldExpiresTS time.Time `json:"old_expires_ts"`
	NewExpiresTS time.Time `json:"new_expires_ts"`
}

// ForceReleaseOptions contains options for forcibly releasing a stale reservation.
type ForceReleaseOptions struct {
	ProjectKey     string
	AgentName      string // The agent requesting the force-release
	ReservationID  int    // ID of the reservation to force-release
	Note           string // Explanation for the force-release
	NotifyPrevious bool   // Whether to notify the previous holder
}

// ForceReleaseResult contains the result of a force-release operation.
type ForceReleaseResult struct {
	Success        bool       `json:"success"`
	ReleasedAt     *time.Time `json:"released_at,omitempty"`
	PreviousHolder string     `json:"previous_holder,omitempty"`
	PathPattern    string     `json:"path_pattern,omitempty"`
	Notified       bool       `json:"notified,omitempty"`
}
