package state

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// RoutingState is the persisted per-(session, filter set) routing state that
// makes sticky and round-robin strategies real across sequential CLI
// invocations (bd-ws1-truth-safety-l5ddi.10). LastAgent records the pane ID
// the previous send routed to; RotationCursor records the index that pane held
// in the candidate list at selection time (-1 = no rotation history).
//
// FilterKey scopes the state to the filter set that produced the candidate
// list (agent-type filter + exclusions, bd-88um4): the cursor is an index into
// a FILTERED list, so alternating sends with different filters must each
// rotate independently instead of corrupting one shared cursor. The empty
// filter key is the unfiltered send path (and the pre-021 legacy rows).
type RoutingState struct {
	SessionName    string    `json:"session_name"`
	FilterKey      string    `json:"filter_key,omitempty"`
	LastAgent      string    `json:"last_agent,omitempty"`
	RotationCursor int       `json:"rotation_cursor"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// routingStateSchemaMissing reports whether err is the read-path signature of
// a state DB that migrations have not (yet) shaped for routing state: the
// table is absent, or predates the filter_key column. The advisory route
// surface opens the store WITHOUT migrating (a read-only path must not take
// the exclusive write lock, bd-88um4), so it treats both as "no history"
// rather than failing.
func routingStateSchemaMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no such table: routing_state") ||
		strings.Contains(msg, "no such column: filter_key")
}

// GetRoutingState returns the persisted routing state for a session + filter
// set, or nil if there is no routing history yet (including when the schema
// has not been migrated — see routingStateSchemaMissing).
func (s *Store) GetRoutingState(session, filterKey string) (*RoutingState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rs := &RoutingState{SessionName: session, FilterKey: filterKey}
	err := s.db.QueryRow(`
		SELECT last_agent, rotation_cursor, updated_at
		FROM routing_state WHERE session_name = ? AND filter_key = ?`, session, filterKey).
		Scan(&rs.LastAgent, &rs.RotationCursor, &rs.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) || routingStateSchemaMissing(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get routing state: %w", err)
	}
	return rs, nil
}

// SaveRoutingState upserts the routing state for a (session, filter set).
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
		INSERT INTO routing_state (session_name, filter_key, last_agent, rotation_cursor, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(session_name, filter_key) DO UPDATE SET
			last_agent = excluded.last_agent,
			rotation_cursor = excluded.rotation_cursor,
			updated_at = excluded.updated_at`,
		rs.SessionName, rs.FilterKey, rs.LastAgent, rs.RotationCursor, updated)
	if err != nil {
		return fmt.Errorf("save routing state: %w", err)
	}
	return nil
}

// DeleteRoutingState removes all routing state rows (every filter key) for a
// session. Used when a session is torn down so a recreated session with the
// same name does not inherit a stale last_agent/cursor (bd-88um4).
func (s *Store) DeleteRoutingState(session string) error {
	if session == "" {
		return fmt.Errorf("routing state deletion requires a session name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`DELETE FROM routing_state WHERE session_name = ?`, session)
	if err != nil && !routingStateSchemaMissing(err) {
		return fmt.Errorf("delete routing state: %w", err)
	}
	return nil
}

// PurgeRoutingStateOlderThan deletes routing state rows not updated since the
// given age. Routing state is only a rotation hint; rows for dead sessions
// otherwise accumulate forever and a recreated session inherits them
// (bd-88um4). Returns the number of rows purged.
func (s *Store) PurgeRoutingStateOlderThan(maxAge time.Duration) (int64, error) {
	if maxAge <= 0 {
		return 0, fmt.Errorf("routing state purge requires a positive max age")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().UTC().Add(-maxAge)
	res, err := s.db.Exec(`DELETE FROM routing_state WHERE updated_at < ?`, cutoff)
	if err != nil {
		if routingStateSchemaMissing(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("purge routing state: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return n, nil
}
