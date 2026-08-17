package state

// guard_events.go — degraded-mode ledger for the pre-commit guard hook
// (bd-ws1-truth-safety-l5ddi.1). The hook fails OPEN when Agent Mail is
// unreachable (blocking every commit while a daemon is down trains users to
// `git commit --no-verify`), but an unobservable WARN in commit scrollback is
// a mini-placebo — so every fail-open event lands here, where `ntm doctor`
// and the dashboard doctor endpoint surface it.

import (
	"fmt"
	"time"
)

// GuardDegradedEvent records one fail-open run of the pre-commit guard hook.
type GuardDegradedEvent struct {
	ID         int64     `json:"id"`
	RepoPath   string    `json:"repo_path"`
	ProjectKey string    `json:"project_key,omitempty"`
	Reason     string    `json:"reason"`
	Detail     string    `json:"detail,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// GuardDegradedStats summarizes the degraded-event ledger.
type GuardDegradedStats struct {
	Count   int       `json:"count"`
	FirstAt time.Time `json:"first_at,omitzero"`
	LastAt  time.Time `json:"last_at,omitzero"`
}

// RecordGuardDegradedEvent appends a degraded-mode event row.
func (s *Store) RecordGuardDegradedEvent(ev *GuardDegradedEvent) error {
	if ev == nil || ev.Reason == "" {
		return fmt.Errorf("guard degraded event requires a reason")
	}
	created := ev.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`
		INSERT INTO guard_degraded_events (repo_path, project_key, reason, detail, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		ev.RepoPath, ev.ProjectKey, ev.Reason, ev.Detail, created)
	if err != nil {
		return fmt.Errorf("record guard degraded event: %w", err)
	}
	if id, err := res.LastInsertId(); err == nil {
		ev.ID = id
	}
	ev.CreatedAt = created
	return nil
}

// GuardDegradedEventStats returns the count plus first/last timestamps of
// degraded events recorded at or after since (zero = all events).
func (s *Store) GuardDegradedEventStats(since time.Time) (*GuardDegradedStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := &GuardDegradedStats{}
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM guard_degraded_events WHERE created_at >= ?`, since).
		Scan(&stats.Count)
	if err != nil {
		return nil, fmt.Errorf("guard degraded stats: %w", err)
	}
	if stats.Count == 0 {
		return stats, nil
	}

	// Aggregate MIN/MAX strip the column's declared type, which breaks the
	// driver's time.Time scanning — select the boundary rows directly instead.
	var first, last time.Time
	if err := s.db.QueryRow(`
		SELECT created_at FROM guard_degraded_events
		WHERE created_at >= ? ORDER BY created_at ASC, id ASC LIMIT 1`, since).
		Scan(&first); err != nil {
		return nil, fmt.Errorf("guard degraded first event: %w", err)
	}
	if err := s.db.QueryRow(`
		SELECT created_at FROM guard_degraded_events
		WHERE created_at >= ? ORDER BY created_at DESC, id DESC LIMIT 1`, since).
		Scan(&last); err != nil {
		return nil, fmt.Errorf("guard degraded last event: %w", err)
	}
	stats.FirstAt = first
	stats.LastAt = last
	return stats, nil
}

// ListGuardDegradedEvents returns the most recent degraded events, newest first.
func (s *Store) ListGuardDegradedEvents(limit int) ([]GuardDegradedEvent, error) {
	if limit <= 0 {
		limit = 20
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT id, repo_path, project_key, reason, detail, created_at
		FROM guard_degraded_events
		ORDER BY created_at DESC, id DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list guard degraded events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var events []GuardDegradedEvent
	for rows.Next() {
		var ev GuardDegradedEvent
		if err := rows.Scan(&ev.ID, &ev.RepoPath, &ev.ProjectKey, &ev.Reason, &ev.Detail, &ev.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan guard degraded event: %w", err)
		}
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate guard degraded events: %w", err)
	}
	return events, nil
}
