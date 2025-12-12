// Package agentmail provides a Go HTTP client for the MCP Agent Mail API.
// Agent Mail enables coordination between AI coding agents through messaging,
// file reservations, and project management.
package agentmail

import "time"

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
	ID          int       `json:"id"`
	Subject     string    `json:"subject"`
	From        string    `json:"from"`
	CreatedTS   time.Time `json:"created_ts"`
	ThreadID    *string   `json:"thread_id,omitempty"`
	Importance  string    `json:"importance"`
	AckRequired bool      `json:"ack_required"`
	Kind        string    `json:"kind"`
	BodyMD      string    `json:"body_md,omitempty"` // Only if include_bodies=true
}

// ContactLink represents a contact relationship between agents.
type ContactLink struct {
	FromAgent string    `json:"from_agent"`
	ToAgent   string    `json:"to_agent"`
	Approved  bool      `json:"approved"`
	ExpiresTS time.Time `json:"expires_ts"`
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
	Success    bool      `json:"success"`
	MessageID  int       `json:"message_id"`
	Recipients []string  `json:"recipients"`
	SentAt     time.Time `json:"sent_at"`
}
