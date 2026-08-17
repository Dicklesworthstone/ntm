package state

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// RoutingState is the persisted per-session routing state that makes sticky
// and round-robin strategies real across sequential CLI invocations
// (bd-ws1-truth-safety-l5ddi.10). LastAgent records the pane ID the previous
// send routed to; RotationCursor records the rotation index that send
// advanced to (-1 = no rotation history).
type RoutingState struct {
	SessionName    string    `json:"session_name"`
	LastAgent      string    `json:"last_agent,omitempty"`
	RotationCursor int       `json:"rotation_cursor"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// GetRoutingState returns the persisted routing state for a session, or nil
// if the session has no routing history yet.
func (s *Store) GetRoutingState(session string) (*RoutingState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rs := &RoutingState{SessionName: session}
	err := s.db.QueryRow(`
		SELECT last_agent, rotation_cursor, updated_at
		FROM routing_state WHERE session_name = ?`, session).
		Scan(&rs.LastAgent, &rs.RotationCursor, &rs.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get routing state: %w", err)
	}
	return rs, nil
}

// SaveRoutingState upserts the routing state for a session.
func (s *Store) SaveRoutingState(rs *RoutingState) error {
	if rs == nil || rs.SessionName == "" {
		return fmt.Errorf("routing state requires a session name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	updated := rs.UpdatedAt
	if updated.IsZero() {
		updated = time.Now().UTC()
	}
	_, err := s.db.Exec(`
		INSERT INTO routing_state (session_name, last_agent, rotation_cursor, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(session_name) DO UPDATE SET
			last_agent = excluded.last_agent,
			rotation_cursor = excluded.rotation_cursor,
			updated_at = excluded.updated_at`,
		rs.SessionName, rs.LastAgent, rs.RotationCursor, updated)
	if err != nil {
		return fmt.Errorf("save routing state: %w", err)
	}
	return nil
}
