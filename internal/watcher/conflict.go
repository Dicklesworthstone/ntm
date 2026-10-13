// Package watcher provides file watching with debouncing using fsnotify.
// conflict.go defines types and callbacks for file reservation conflicts.
package watcher

import (
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/events"
)

// FileConflictEventType identifies an observed reservation conflict on the bus.
const FileConflictEventType = "file_reservation.conflict"

// FileConflictEvent is an observation, not authority to release a lease.
// Scope includes the project as well as the session: a shared event bus must
// never deliver another project's reservation handles to a dashboard.
type FileConflictEvent struct {
	events.BaseEvent
	ProjectKey string       `json:"project_key"`
	Conflict   FileConflict `json:"conflict"`
}

// Snapshot gives each consumer its own slices and timestamp pointers. Neither
// the caller's callback nor a UI model may mutate the bus history's payload.
func (e FileConflictEvent) Snapshot() FileConflict {
	c := e.Conflict
	c.Holders = append([]string(nil), c.Holders...)
	c.HolderReservationIDs = append([]int(nil), c.HolderReservationIDs...)
	if c.ReservedSince != nil {
		value := *c.ReservedSince
		c.ReservedSince = &value
	}
	if c.ExpiresAt != nil {
		value := *c.ExpiresAt
		c.ExpiresAt = &value
	}
	return c
}

func publishFileConflict(projectKey string, conflict FileConflict) {
	event := FileConflictEvent{
		BaseEvent: events.BaseEvent{
			Type: FileConflictEventType, Timestamp: conflict.DetectedAt, Session: conflict.SessionName,
		},
		ProjectKey: agentmail.CanonicalProjectKey(projectKey),
		Conflict:   conflict,
	}
	event.Conflict = event.Snapshot()
	events.Publish(event)
}

// FileConflict represents a detected file reservation conflict.
// This occurs when an agent attempts to edit a file that is reserved by another agent.
type FileConflict struct {
	// Path is the file path that caused the conflict
	Path string `json:"path"`

	// RequestorAgent is the agent that tried to edit the file
	RequestorAgent string `json:"requestor_agent"`

	// RequestorPane is the pane ID of the requestor
	RequestorPane string `json:"requestor_pane"`

	// SessionName is the tmux session name
	SessionName string `json:"session_name"`

	// Holders are the agents currently holding the reservation
	Holders []string `json:"holders"`

	// HolderReservationIDs are the reservation IDs held by the holders (for force-release)
	HolderReservationIDs []int `json:"holder_reservation_ids,omitempty"`

	// ReservedSince is when the file was reserved (if known)
	ReservedSince *time.Time `json:"reserved_since,omitempty"`

	// ExpiresAt is when the reservation expires (if known)
	ExpiresAt *time.Time `json:"expires_at,omitempty"`

	// DetectedAt is when this conflict was detected
	DetectedAt time.Time `json:"detected_at"`
}

// TimeRemaining returns how much time is left until the reservation expires.
// Returns 0 if the reservation has expired or expiry time is unknown.
func (c *FileConflict) TimeRemaining() time.Duration {
	if c.ExpiresAt == nil {
		return 0
	}
	remaining := time.Until(*c.ExpiresAt)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// TimeSinceReserved returns how long the file has been reserved.
// Returns 0 if the reservation start time is unknown.
func (c *FileConflict) TimeSinceReserved() time.Duration {
	if c.ReservedSince == nil {
		return 0
	}
	return time.Since(*c.ReservedSince)
}

// IsExpired returns true if the reservation has expired.
// Returns false if expiry time is unknown (assume still active).
func (c *FileConflict) IsExpired() bool {
	if c.ExpiresAt == nil {
		return false
	}
	return time.Now().After(*c.ExpiresAt)
}

// ConflictAction represents the user's chosen action for a conflict.
type ConflictAction int

const (
	// ConflictActionWait indicates the user chose to wait for the reservation to expire
	ConflictActionWait ConflictAction = iota
	// ConflictActionRequest indicates the user requested a handoff from the holder
	ConflictActionRequest
	// ConflictActionForce indicates the user chose to force-release the reservation
	ConflictActionForce
	// ConflictActionDismiss indicates the user dismissed the conflict without action
	ConflictActionDismiss
)

// String returns a human-readable name for the action.
func (a ConflictAction) String() string {
	switch a {
	case ConflictActionWait:
		return "wait"
	case ConflictActionRequest:
		return "request"
	case ConflictActionForce:
		return "force"
	case ConflictActionDismiss:
		return "dismiss"
	default:
		return "unknown"
	}
}

// ConflictCallback is called when a file reservation conflict is detected.
// Implementations should handle the conflict notification to the user.
type ConflictCallback func(conflict FileConflict)

// ConflictActionHandler is called when the user selects an action for a conflict.
// Returns an error if the action could not be performed.
type ConflictActionHandler func(conflict FileConflict, action ConflictAction) error
