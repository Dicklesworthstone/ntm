// Package state provides durable SQLite-backed storage for NTM orchestration state.
package state

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/sqliteutil"
)

// Store provides SQLite-backed storage for NTM state.
type Store struct {
	db   *sql.DB
	mu   sync.RWMutex
	path string
}

func expandSelectedConfigPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || path[0] != '~' {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}

// DefaultPath returns the default state store path.
func DefaultPath() string {
	if cfgPath := expandSelectedConfigPath(os.Getenv("NTM_CONFIG")); cfgPath != "" {
		return filepath.Join(filepath.Dir(cfgPath), "state.db")
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "ntm", "state.db")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.TempDir()
	}
	return filepath.Join(home, ".config", "ntm", "state.db")
}

// Open opens or creates a SQLite database at the given path.
// If the path is empty, it defaults to the state DB next to the selected config path, or under XDG or ~/.config.
func Open(path string) (*Store, error) {
	if path == "" {
		path = DefaultPath()
	}

	pragmas := []string{
		"busy_timeout(5000)",
		"foreign_keys(1)",
		"synchronous(NORMAL)",
		"temp_store(MEMORY)",
		"mmap_size(268435456)",
	}
	dsn := ""
	inMemory := path == ":memory:"
	if inMemory {
		// In-memory databases cannot use WAL, and multiple pooled connections would
		// each see their own isolated database. Keep a single shared connection.
		dsn = sqliteutil.MemoryDSN(pragmas...)
	} else {
		// Ensure parent directory exists
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("create state dir: %w", err)
		}
		dsn = sqliteutil.ImmediateTransactionFileDSN(path, append(pragmas, "journal_mode(WAL)")...)
	}

	db, err := sql.Open(sqliteutil.DriverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Set connection pool settings.
	// File-backed DBs allow concurrent reads under WAL mode. In-memory DBs must
	// stay on a single connection so migrations and subsequent queries see the
	// same database instance.
	if inMemory {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	} else {
		db.SetMaxOpenConns(25)
		db.SetMaxIdleConns(5)
	}
	db.SetConnMaxLifetime(0) // Don't close idle connections

	// Verify connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return &Store{db: db, path: path}, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Close()
}

// Migrate applies all pending database migrations.
func (s *Store) Migrate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ApplyMigrations(s.db)
}

// Path returns the database file path.
func (s *Store) Path() string {
	return s.path
}

// DB returns the underlying database connection for direct access.
// Use with caution - prefer using Store methods when possible.
func (s *Store) DB() *sql.DB {
	return s.db
}

// Tx represents a transaction.
type Tx struct {
	tx *sql.Tx
}

// Transaction executes fn within a transaction.
// If fn returns an error, the transaction is rolled back.
func (s *Store) Transaction(fn func(tx *Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if err := fn(&Tx{tx: tx}); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	committed = true
	return nil
}

// ========================
// Session Operations
// ========================

// CreateSession creates a new session.
func (s *Store) CreateSession(sess *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO sessions (id, name, project_path, created_at, status, config_snapshot, coordinator_agent)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.Name, sess.ProjectPath, sess.CreatedAt, sess.Status, sess.ConfigSnapshot, sess.CoordinatorAgent,
	)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// GetSession retrieves a session by ID.
func (s *Store) GetSession(id string) (*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sess := &Session{}
	err := s.db.QueryRow(`
		SELECT id, name, project_path, created_at, status, COALESCE(config_snapshot, ''), COALESCE(coordinator_agent, '')
		FROM sessions WHERE id = ?`, id,
	).Scan(&sess.ID, &sess.Name, &sess.ProjectPath, &sess.CreatedAt, &sess.Status, &sess.ConfigSnapshot, &sess.CoordinatorAgent)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	return sess, nil
}

// UpdateSession updates an existing session.
func (s *Store) UpdateSession(sess *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec(`
		UPDATE sessions SET name = ?, project_path = ?, status = ?, config_snapshot = ?, coordinator_agent = ?
		WHERE id = ?`,
		sess.Name, sess.ProjectPath, sess.Status, sess.ConfigSnapshot, sess.CoordinatorAgent, sess.ID,
	)
	if err != nil {
		return fmt.Errorf("update session: %w", err)
	}

	rows, rowsErr := result.RowsAffected()
	if rowsErr != nil {
		return fmt.Errorf("update session rows affected: %w", rowsErr)
	}
	if rows == 0 {
		return fmt.Errorf("session not found: %s", sess.ID)
	}
	return nil
}

// ListSessions returns sessions filtered by status (empty = all).
func (s *Store) ListSessions(status string) ([]Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var rows *sql.Rows
	var err error

	if status == "" {
		rows, err = s.db.Query(`
			SELECT id, name, project_path, created_at, status, COALESCE(config_snapshot, ''), COALESCE(coordinator_agent, '')
			FROM sessions ORDER BY created_at DESC`)
	} else {
		rows, err = s.db.Query(`
			SELECT id, name, project_path, created_at, status, COALESCE(config_snapshot, ''), COALESCE(coordinator_agent, '')
			FROM sessions WHERE status = ? ORDER BY created_at DESC`, status)
	}

	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		var sess Session
		if err := rows.Scan(&sess.ID, &sess.Name, &sess.ProjectPath, &sess.CreatedAt, &sess.Status, &sess.ConfigSnapshot, &sess.CoordinatorAgent); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		sessions = append(sessions, sess)
	}
	return sessions, rows.Err()
}

// DeleteSession deletes a session and all related data (cascading).
func (s *Store) DeleteSession(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec("DELETE FROM sessions WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}

	rows, rowsErr := result.RowsAffected()
	if rowsErr != nil {
		return fmt.Errorf("rows affected: %w", rowsErr)
	}
	if rows == 0 {
		return fmt.Errorf("session not found: %s", id)
	}
	return nil
}

// ========================
// Agent Operations
// ========================

// CreateAgent creates a new agent.
func (s *Store) CreateAgent(agent *Agent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO agents (id, session_id, name, type, model, tmux_pane_id, last_seen, status, current_task_id, performance_data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		agent.ID, agent.SessionID, agent.Name, agent.Type, agent.Model, agent.TmuxPaneID, agent.LastSeen, agent.Status, agent.CurrentTaskID, agent.PerformanceData,
	)
	if err != nil {
		return fmt.Errorf("create agent: %w", err)
	}
	return nil
}

// ListAgents returns agents for a session.
func (s *Store) ListAgents(sessionID string) ([]Agent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT id, session_id, name, type, COALESCE(model, ''), COALESCE(tmux_pane_id, ''), last_seen, status, COALESCE(current_task_id, ''), COALESCE(performance_data, '')
		FROM agents WHERE session_id = ? ORDER BY name`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()

	var agents []Agent
	for rows.Next() {
		var agent Agent
		if err := rows.Scan(&agent.ID, &agent.SessionID, &agent.Name, &agent.Type, &agent.Model, &agent.TmuxPaneID, &agent.LastSeen, &agent.Status, &agent.CurrentTaskID, &agent.PerformanceData); err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		agents = append(agents, agent)
	}
	return agents, rows.Err()
}

// ========================
// Approval Operations
// ========================

// CreateApproval creates a new approval request.
func (s *Store) CreateApproval(appr *Approval) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO approvals (id, action, resource, reason, requested_by, requested_provenance, correlation_id, requires_slb, created_at, expires_at, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		appr.ID, appr.Action, appr.Resource, appr.Reason, appr.RequestedBy, appr.RequestedProvenance, appr.CorrelationID, appr.RequiresSLB, appr.CreatedAt, appr.ExpiresAt, appr.Status,
	)
	if err != nil {
		return fmt.Errorf("create approval: %w", err)
	}
	return nil
}

// GetApproval retrieves an approval by ID.
func (s *Store) GetApproval(id string) (*Approval, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	appr := &Approval{}
	err := s.db.QueryRow(`
		SELECT id, action, resource, COALESCE(reason, ''), requested_by, COALESCE(requested_provenance, ''), COALESCE(correlation_id, ''), requires_slb, created_at, expires_at, status, COALESCE(approved_by, ''), COALESCE(approved_provenance, ''), approved_at, COALESCE(denied_reason, '')
		FROM approvals WHERE id = ?`, id,
	).Scan(&appr.ID, &appr.Action, &appr.Resource, &appr.Reason, &appr.RequestedBy, &appr.RequestedProvenance, &appr.CorrelationID, &appr.RequiresSLB, &appr.CreatedAt, &appr.ExpiresAt, &appr.Status, &appr.ApprovedBy, &appr.ApprovedProvenance, &appr.ApprovedAt, &appr.DeniedReason)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get approval: %w", err)
	}
	return appr, nil
}

// ConsumeApproval atomically transitions an approval from approved to
// consumed, reporting whether this call performed the transition. The status
// guard runs inside the UPDATE itself (bd-2y2on): two concurrent gate
// invocations — separate processes, so the engine's in-process mutex cannot
// serialize them — may both read status=approved, but exactly one UPDATE
// matches `status = 'approved'`; the loser matches zero rows and must treat
// the approval as already spent.
func (s *Store) ConsumeApproval(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec(
		`UPDATE approvals SET status = ? WHERE id = ? AND status = ? AND expires_at > ?`,
		ApprovalConsumed, id, ApprovalApproved, time.Now().UTC(),
	)
	if err != nil {
		return false, fmt.Errorf("consume approval: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected: %w", err)
	}
	return rows > 0, nil
}

// UpdateApprovalFrom applies UpdateApproval's write only while the record's
// status in the database still equals from, reporting whether this call
// performed the transition. Status transitions (pending->approved,
// pending->denied, pending->expired) MUST use this guard rather than the
// blind UpdateApproval: like ConsumeApproval above, the deciding callers are
// independent `ntm` processes sharing state.db, so an unguarded UPDATE would
// let a racing approver silently overwrite a concurrent denial (or vice
// versa) after both read status=pending.
func (s *Store) UpdateApprovalFrom(appr *Approval, from ApprovalStatus) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec(`
		UPDATE approvals SET status = ?, approved_by = ?, approved_provenance = ?, approved_at = ?, denied_reason = ?
		WHERE id = ? AND status = ?`,
		appr.Status, appr.ApprovedBy, appr.ApprovedProvenance, appr.ApprovedAt, appr.DeniedReason, appr.ID, from,
	)
	if err != nil {
		return false, fmt.Errorf("update approval: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected: %w", err)
	}
	return rows > 0, nil
}

// UpdateApproval updates an existing approval.
func (s *Store) UpdateApproval(appr *Approval) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec(`
		UPDATE approvals SET status = ?, approved_by = ?, approved_provenance = ?, approved_at = ?, denied_reason = ?
		WHERE id = ?`,
		appr.Status, appr.ApprovedBy, appr.ApprovedProvenance, appr.ApprovedAt, appr.DeniedReason, appr.ID,
	)
	if err != nil {
		return fmt.Errorf("update approval: %w", err)
	}

	rows, rowsErr := result.RowsAffected()
	if rowsErr != nil {
		return fmt.Errorf("rows affected: %w", rowsErr)
	}
	if rows == 0 {
		return fmt.Errorf("approval not found: %s", appr.ID)
	}
	return nil
}

// ListPendingApprovals returns all pending approval requests that haven't expired.
func (s *Store) ListPendingApprovals() ([]Approval, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT id, action, resource, COALESCE(reason, ''), requested_by, COALESCE(requested_provenance, ''), COALESCE(correlation_id, ''), requires_slb, created_at, expires_at, status, COALESCE(approved_by, ''), COALESCE(approved_provenance, ''), approved_at, COALESCE(denied_reason, '')
		FROM approvals WHERE status = 'pending' AND expires_at > ?
		ORDER BY created_at`, time.Now().UTC())

	if err != nil {
		return nil, fmt.Errorf("list pending approvals: %w", err)
	}
	defer rows.Close()

	var approvals []Approval
	for rows.Next() {
		var appr Approval
		if err := rows.Scan(&appr.ID, &appr.Action, &appr.Resource, &appr.Reason, &appr.RequestedBy, &appr.RequestedProvenance, &appr.CorrelationID, &appr.RequiresSLB, &appr.CreatedAt, &appr.ExpiresAt, &appr.Status, &appr.ApprovedBy, &appr.ApprovedProvenance, &appr.ApprovedAt, &appr.DeniedReason); err != nil {
			return nil, fmt.Errorf("scan approval: %w", err)
		}
		approvals = append(approvals, appr)
	}
	return approvals, rows.Err()
}

// ListApprovalHistory returns ALL approval records regardless of status —
// pending, approved, denied, consumed, and expired — newest first. This is
// the backing query for `ntm approve history` (bd-ws7-docs-ux-truth-tqh3l.9):
// history means resolved records too, each row carrying its status, decider,
// and decision timestamp, not a pending-only list relabeled as history.
func (s *Store) ListApprovalHistory() ([]Approval, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT id, action, resource, COALESCE(reason, ''), requested_by, COALESCE(requested_provenance, ''), COALESCE(correlation_id, ''), requires_slb, created_at, expires_at, status, COALESCE(approved_by, ''), COALESCE(approved_provenance, ''), approved_at, COALESCE(denied_reason, '')
		FROM approvals
		ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list approval history: %w", err)
	}
	defer rows.Close()

	var approvals []Approval
	for rows.Next() {
		var appr Approval
		if err := rows.Scan(&appr.ID, &appr.Action, &appr.Resource, &appr.Reason, &appr.RequestedBy, &appr.RequestedProvenance, &appr.CorrelationID, &appr.RequiresSLB, &appr.CreatedAt, &appr.ExpiresAt, &appr.Status, &appr.ApprovedBy, &appr.ApprovedProvenance, &appr.ApprovedAt, &appr.DeniedReason); err != nil {
			return nil, fmt.Errorf("scan approval: %w", err)
		}
		approvals = append(approvals, appr)
	}
	return approvals, rows.Err()
}

// ListExpiredPendingApprovals returns pending approvals that have expired.
func (s *Store) ListExpiredPendingApprovals() ([]Approval, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT id, action, resource, COALESCE(reason, ''), requested_by, COALESCE(requested_provenance, ''), COALESCE(correlation_id, ''), requires_slb, created_at, expires_at, status, COALESCE(approved_by, ''), COALESCE(approved_provenance, ''), approved_at, COALESCE(denied_reason, '')
		FROM approvals WHERE status = 'pending' AND expires_at <= ?
		ORDER BY created_at`, time.Now().UTC())

	if err != nil {
		return nil, fmt.Errorf("list expired pending approvals: %w", err)
	}
	defer rows.Close()

	var approvals []Approval
	for rows.Next() {
		var appr Approval
		if err := rows.Scan(&appr.ID, &appr.Action, &appr.Resource, &appr.Reason, &appr.RequestedBy, &appr.RequestedProvenance, &appr.CorrelationID, &appr.RequiresSLB, &appr.CreatedAt, &appr.ExpiresAt, &appr.Status, &appr.ApprovedBy, &appr.ApprovedProvenance, &appr.ApprovedAt, &appr.DeniedReason); err != nil {
			return nil, fmt.Errorf("scan approval: %w", err)
		}
		approvals = append(approvals, appr)
	}
	return approvals, rows.Err()
}

// ========================
// Context Pack Operations
// ========================

// CreateContextPack creates a new context pack.
func (s *Store) CreateContextPack(cp *ContextPack) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO context_packs (id, bead_id, agent_type, repo_rev, correlation_id, created_at, token_count, rendered_prompt)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		cp.ID, cp.BeadID, cp.AgentType, cp.RepoRev, cp.CorrelationID, cp.CreatedAt, cp.TokenCount, cp.RenderedPrompt,
	)
	if err != nil {
		return fmt.Errorf("create context pack: %w", err)
	}
	return nil
}

// GetContextPack retrieves a context pack by ID.
func (s *Store) GetContextPack(id string) (*ContextPack, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cp := &ContextPack{}
	err := s.db.QueryRow(`
		SELECT id, bead_id, agent_type, repo_rev, COALESCE(correlation_id, ''), created_at, COALESCE(token_count, 0), COALESCE(rendered_prompt, '')
		FROM context_packs WHERE id = ?`, id,
	).Scan(&cp.ID, &cp.BeadID, &cp.AgentType, &cp.RepoRev, &cp.CorrelationID, &cp.CreatedAt, &cp.TokenCount, &cp.RenderedPrompt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get context pack: %w", err)
	}
	return cp, nil
}

// ========================
// Event Log Operations
// ========================

// EventLogEntry represents a logged event.
type EventLogEntry struct {
	ID            int64     `json:"id"`
	SessionID     string    `json:"session_id,omitempty"`
	EventType     string    `json:"event_type"`
	EventData     string    `json:"event_data"` // JSON payload
	CorrelationID string    `json:"correlation_id,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// LogEvent logs an event to the event log.
func (s *Store) LogEvent(entry *EventLogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec(`
		INSERT INTO event_log (session_id, event_type, event_data, correlation_id, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		entry.SessionID, entry.EventType, entry.EventData, entry.CorrelationID, time.Now(),
	)
	if err != nil {
		return fmt.Errorf("log event: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("get event id: %w", err)
	}
	entry.ID = id
	return nil
}

// ListEvents returns recent events for a session.
func (s *Store) ListEvents(sessionID string, limit int) ([]EventLogEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 100
	}

	rows, err := s.db.Query(`
		SELECT id, COALESCE(session_id, ''), event_type, event_data, COALESCE(correlation_id, ''), created_at
		FROM event_log WHERE session_id = ?
		ORDER BY id DESC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	var events []EventLogEntry
	for rows.Next() {
		var entry EventLogEntry
		if err := rows.Scan(&entry.ID, &entry.SessionID, &entry.EventType, &entry.EventData, &entry.CorrelationID, &entry.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		events = append(events, entry)
	}
	return events, rows.Err()
}

// ReplayEvents replays events from a given ID for crash recovery.
func (s *Store) ReplayEvents(sessionID string, fromID int64, handler func(EventLogEntry) error) error {
	s.mu.RLock()
	entries, err := func() ([]EventLogEntry, error) {
		rows, err := s.db.Query(`
			SELECT id, COALESCE(session_id, ''), event_type, event_data, COALESCE(correlation_id, ''), created_at
			FROM event_log WHERE session_id = ? AND id > ?
		ORDER BY id ASC`, sessionID, fromID)
		if err != nil {
			return nil, fmt.Errorf("query events for replay: %w", err)
		}
		defer rows.Close()

		var entries []EventLogEntry
		for rows.Next() {
			var entry EventLogEntry
			if err := rows.Scan(&entry.ID, &entry.SessionID, &entry.EventType, &entry.EventData, &entry.CorrelationID, &entry.CreatedAt); err != nil {
				return nil, fmt.Errorf("scan event for replay: %w", err)
			}
			entries = append(entries, entry)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return entries, nil
	}()
	s.mu.RUnlock()
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if err := handler(entry); err != nil {
			return fmt.Errorf("replay handler error at event %d: %w", entry.ID, err)
		}
	}
	return nil
}
