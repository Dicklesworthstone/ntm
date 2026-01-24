// Package state provides durable SQLite-backed storage for NTM orchestration state.
package state

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3" // SQLite driver
)

// Store provides SQLite-backed storage for NTM state.
type Store struct {
	db   *sql.DB
	mu   sync.RWMutex
	path string
}

// Open opens or creates a SQLite database at the given path.
// If the path is empty, it defaults to ~/.config/ntm/state.db.
func Open(path string) (*Store, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("get home dir: %w", err)
		}
		path = filepath.Join(home, ".config", "ntm", "state.db")
	}

	// Ensure parent directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}

	// Open with WAL mode and other optimizations
	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=ON", path)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Set connection pool settings
	// We allow multiple connections for concurrent reads (enabled by WAL mode).
	// Writes are serialized by the Store's mutex to avoid SQLITE_BUSY.
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
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

	if err := fn(&Tx{tx: tx}); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
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

	rows, _ := result.RowsAffected()
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

	rows, _ := result.RowsAffected()
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

// GetAgent retrieves an agent by ID.
func (s *Store) GetAgent(id string) (*Agent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	agent := &Agent{}
	err := s.db.QueryRow(`
		SELECT id, session_id, name, type, COALESCE(model, ''), COALESCE(tmux_pane_id, ''), last_seen, status, COALESCE(current_task_id, ''), COALESCE(performance_data, '')
		FROM agents WHERE id = ?`, id,
	).Scan(&agent.ID, &agent.SessionID, &agent.Name, &agent.Type, &agent.Model, &agent.TmuxPaneID, &agent.LastSeen, &agent.Status, &agent.CurrentTaskID, &agent.PerformanceData)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get agent: %w", err)
	}
	return agent, nil
}

// GetAgentByName retrieves an agent by session ID and name.
func (s *Store) GetAgentByName(sessionID, name string) (*Agent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	agent := &Agent{}
	err := s.db.QueryRow(`
		SELECT id, session_id, name, type, COALESCE(model, ''), COALESCE(tmux_pane_id, ''), last_seen, status, COALESCE(current_task_id, ''), COALESCE(performance_data, '')
		FROM agents WHERE session_id = ? AND name = ?`, sessionID, name,
	).Scan(&agent.ID, &agent.SessionID, &agent.Name, &agent.Type, &agent.Model, &agent.TmuxPaneID, &agent.LastSeen, &agent.Status, &agent.CurrentTaskID, &agent.PerformanceData)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get agent by name: %w", err)
	}
	return agent, nil
}

// UpdateAgent updates an existing agent.
func (s *Store) UpdateAgent(agent *Agent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec(`
		UPDATE agents SET name = ?, type = ?, model = ?, tmux_pane_id = ?, last_seen = ?, status = ?, current_task_id = ?, performance_data = ?
		WHERE id = ?`,
		agent.Name, agent.Type, agent.Model, agent.TmuxPaneID, agent.LastSeen, agent.Status, agent.CurrentTaskID, agent.PerformanceData, agent.ID,
	)
	if err != nil {
		return fmt.Errorf("update agent: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("agent not found: %s", agent.ID)
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
// Task Operations
// ========================

// CreateTask creates a new task.
func (s *Store) CreateTask(task *Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO tasks (id, session_id, agent_id, bead_id, correlation_id, context_pack_id, status, created_at, assigned_at, completed_at, result)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.ID, task.SessionID, task.AgentID, task.BeadID, task.CorrelationID, task.ContextPackID, task.Status, task.CreatedAt, task.AssignedAt, task.CompletedAt, task.Result,
	)
	if err != nil {
		return fmt.Errorf("create task: %w", err)
	}
	return nil
}

// GetTask retrieves a task by ID.
func (s *Store) GetTask(id string) (*Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	task := &Task{}
	err := s.db.QueryRow(`
		SELECT id, session_id, COALESCE(agent_id, ''), COALESCE(bead_id, ''), COALESCE(correlation_id, ''), COALESCE(context_pack_id, ''), status, created_at, assigned_at, completed_at, result
		FROM tasks WHERE id = ?`, id,
	).Scan(&task.ID, &task.SessionID, &task.AgentID, &task.BeadID, &task.CorrelationID, &task.ContextPackID, &task.Status, &task.CreatedAt, &task.AssignedAt, &task.CompletedAt, &task.Result)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get task: %w", err)
	}
	return task, nil
}

// GetTaskByCorrelation retrieves a task by correlation ID.
func (s *Store) GetTaskByCorrelation(correlationID string) (*Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	task := &Task{}
	err := s.db.QueryRow(`
		SELECT id, session_id, COALESCE(agent_id, ''), COALESCE(bead_id, ''), COALESCE(correlation_id, ''), COALESCE(context_pack_id, ''), status, created_at, assigned_at, completed_at, result
		FROM tasks WHERE correlation_id = ?`, correlationID,
	).Scan(&task.ID, &task.SessionID, &task.AgentID, &task.BeadID, &task.CorrelationID, &task.ContextPackID, &task.Status, &task.CreatedAt, &task.AssignedAt, &task.CompletedAt, &task.Result)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get task by correlation: %w", err)
	}
	return task, nil
}

// UpdateTask updates an existing task.
func (s *Store) UpdateTask(task *Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec(`
		UPDATE tasks SET agent_id = ?, status = ?, assigned_at = ?, completed_at = ?, result = ?
		WHERE id = ?`,
		task.AgentID, task.Status, task.AssignedAt, task.CompletedAt, task.Result, task.ID,
	)
	if err != nil {
		return fmt.Errorf("update task: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("task not found: %s", task.ID)
	}
	return nil
}

// ListTasks returns tasks for a session, optionally filtered by status.
func (s *Store) ListTasks(sessionID string, status string) ([]Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var rows *sql.Rows
	var err error

	if status == "" {
		rows, err = s.db.Query(`
			SELECT id, session_id, COALESCE(agent_id, ''), COALESCE(bead_id, ''), COALESCE(correlation_id, ''), COALESCE(context_pack_id, ''), status, created_at, assigned_at, completed_at, result
			FROM tasks WHERE session_id = ? ORDER BY created_at DESC`, sessionID)
	} else {
		rows, err = s.db.Query(`
			SELECT id, session_id, COALESCE(agent_id, ''), COALESCE(bead_id, ''), COALESCE(correlation_id, ''), COALESCE(context_pack_id, ''), status, created_at, assigned_at, completed_at, result
			FROM tasks WHERE session_id = ? AND status = ? ORDER BY created_at DESC`, sessionID, status)
	}

	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		var task Task
		if err := rows.Scan(&task.ID, &task.SessionID, &task.AgentID, &task.BeadID, &task.CorrelationID, &task.ContextPackID, &task.Status, &task.CreatedAt, &task.AssignedAt, &task.CompletedAt, &task.Result); err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

// ========================
// Reservation Operations
// ========================

// CreateReservation creates a new file reservation.
func (s *Store) CreateReservation(res *Reservation) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec(`
		INSERT INTO reservations (session_id, agent_id, path_pattern, exclusive, correlation_id, reason, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		res.SessionID, res.AgentID, res.PathPattern, res.Exclusive, res.CorrelationID, res.Reason, res.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("create reservation: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("get reservation id: %w", err)
	}
	res.ID = id
	return nil
}

// GetReservation retrieves a reservation by ID.
func (s *Store) GetReservation(id int64) (*Reservation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	res := &Reservation{}
	err := s.db.QueryRow(`
		SELECT id, session_id, agent_id, path_pattern, exclusive, COALESCE(correlation_id, ''), COALESCE(reason, ''), expires_at, released_at, COALESCE(force_released_by, '')
		FROM reservations WHERE id = ?`, id,
	).Scan(&res.ID, &res.SessionID, &res.AgentID, &res.PathPattern, &res.Exclusive, &res.CorrelationID, &res.Reason, &res.ExpiresAt, &res.ReleasedAt, &res.ForceReleasedBy)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get reservation: %w", err)
	}
	return res, nil
}

// UpdateReservation updates an existing reservation.
func (s *Store) UpdateReservation(res *Reservation) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec(`
		UPDATE reservations SET expires_at = ?, released_at = ?, force_released_by = ?
		WHERE id = ?`,
		res.ExpiresAt, res.ReleasedAt, res.ForceReleasedBy, res.ID,
	)
	if err != nil {
		return fmt.Errorf("update reservation: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("reservation not found: %d", res.ID)
	}
	return nil
}

// ListReservations returns reservations for a session, optionally only active ones.
func (s *Store) ListReservations(sessionID string, activeOnly bool) ([]Reservation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var rows *sql.Rows
	var err error

	if activeOnly {
		rows, err = s.db.Query(`
			SELECT id, session_id, agent_id, path_pattern, exclusive, COALESCE(correlation_id, ''), COALESCE(reason, ''), expires_at, released_at, COALESCE(force_released_by, '')
			FROM reservations WHERE session_id = ? AND released_at IS NULL AND expires_at > ?
			ORDER BY expires_at`, sessionID, time.Now())
	} else {
		rows, err = s.db.Query(`
			SELECT id, session_id, agent_id, path_pattern, exclusive, COALESCE(correlation_id, ''), COALESCE(reason, ''), expires_at, released_at, COALESCE(force_released_by, '')
			FROM reservations WHERE session_id = ? ORDER BY expires_at DESC`, sessionID)
	}

	if err != nil {
		return nil, fmt.Errorf("list reservations: %w", err)
	}
	defer rows.Close()

	var reservations []Reservation
	for rows.Next() {
		var res Reservation
		if err := rows.Scan(&res.ID, &res.SessionID, &res.AgentID, &res.PathPattern, &res.Exclusive, &res.CorrelationID, &res.Reason, &res.ExpiresAt, &res.ReleasedAt, &res.ForceReleasedBy); err != nil {
			return nil, fmt.Errorf("scan reservation: %w", err)
		}
		reservations = append(reservations, res)
	}
	return reservations, rows.Err()
}

// FindConflicts finds active exclusive reservations that conflict with a path pattern.
func (s *Store) FindConflicts(sessionID, pattern string) ([]Reservation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// SQLite GLOB for pattern matching
	rows, err := s.db.Query(`
		SELECT id, session_id, agent_id, path_pattern, exclusive, COALESCE(correlation_id, ''), COALESCE(reason, ''), expires_at, released_at, COALESCE(force_released_by, '')
		FROM reservations
		WHERE session_id = ? AND exclusive = 1 AND released_at IS NULL AND expires_at > ?
		AND (path_pattern = ? OR ? GLOB path_pattern OR path_pattern GLOB ?)
		ORDER BY expires_at`, sessionID, time.Now(), pattern, pattern, pattern)

	if err != nil {
		return nil, fmt.Errorf("find conflicts: %w", err)
	}
	defer rows.Close()

	var conflicts []Reservation
	for rows.Next() {
		var res Reservation
		if err := rows.Scan(&res.ID, &res.SessionID, &res.AgentID, &res.PathPattern, &res.Exclusive, &res.CorrelationID, &res.Reason, &res.ExpiresAt, &res.ReleasedAt, &res.ForceReleasedBy); err != nil {
			return nil, fmt.Errorf("scan conflict: %w", err)
		}
		conflicts = append(conflicts, res)
	}
	return conflicts, rows.Err()
}

// ========================
// Approval Operations
// ========================

// CreateApproval creates a new approval request.
func (s *Store) CreateApproval(appr *Approval) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO approvals (id, action, resource, reason, requested_by, correlation_id, requires_slb, created_at, expires_at, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		appr.ID, appr.Action, appr.Resource, appr.Reason, appr.RequestedBy, appr.CorrelationID, appr.RequiresSLB, appr.CreatedAt, appr.ExpiresAt, appr.Status,
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
		SELECT id, action, resource, COALESCE(reason, ''), requested_by, COALESCE(correlation_id, ''), requires_slb, created_at, expires_at, status, COALESCE(approved_by, ''), approved_at, COALESCE(denied_reason, '')
		FROM approvals WHERE id = ?`, id,
	).Scan(&appr.ID, &appr.Action, &appr.Resource, &appr.Reason, &appr.RequestedBy, &appr.CorrelationID, &appr.RequiresSLB, &appr.CreatedAt, &appr.ExpiresAt, &appr.Status, &appr.ApprovedBy, &appr.ApprovedAt, &appr.DeniedReason)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get approval: %w", err)
	}
	return appr, nil
}

// UpdateApproval updates an existing approval.
func (s *Store) UpdateApproval(appr *Approval) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec(`
		UPDATE approvals SET status = ?, approved_by = ?, approved_at = ?, denied_reason = ?
		WHERE id = ?`,
		appr.Status, appr.ApprovedBy, appr.ApprovedAt, appr.DeniedReason, appr.ID,
	)
	if err != nil {
		return fmt.Errorf("update approval: %w", err)
	}

	rows, _ := result.RowsAffected()
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
		SELECT id, action, resource, COALESCE(reason, ''), requested_by, COALESCE(correlation_id, ''), requires_slb, created_at, expires_at, status, COALESCE(approved_by, ''), approved_at, COALESCE(denied_reason, '')
		FROM approvals WHERE status = 'pending' AND expires_at > ?
		ORDER BY created_at`, time.Now().UTC())

	if err != nil {
		return nil, fmt.Errorf("list pending approvals: %w", err)
	}
	defer rows.Close()

	var approvals []Approval
	for rows.Next() {
		var appr Approval
		if err := rows.Scan(&appr.ID, &appr.Action, &appr.Resource, &appr.Reason, &appr.RequestedBy, &appr.CorrelationID, &appr.RequiresSLB, &appr.CreatedAt, &appr.ExpiresAt, &appr.Status, &appr.ApprovedBy, &appr.ApprovedAt, &appr.DeniedReason); err != nil {
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
		SELECT id, action, resource, COALESCE(reason, ''), requested_by, COALESCE(correlation_id, ''), requires_slb, created_at, expires_at, status, COALESCE(approved_by, ''), approved_at, COALESCE(denied_reason, '')
		FROM approvals WHERE status = 'pending' AND expires_at <= ?
		ORDER BY created_at`, time.Now().UTC())

	if err != nil {
		return nil, fmt.Errorf("list expired pending approvals: %w", err)
	}
	defer rows.Close()

	var approvals []Approval
	for rows.Next() {
		var appr Approval
		if err := rows.Scan(&appr.ID, &appr.Action, &appr.Resource, &appr.Reason, &appr.RequestedBy, &appr.CorrelationID, &appr.RequiresSLB, &appr.CreatedAt, &appr.ExpiresAt, &appr.Status, &appr.ApprovedBy, &appr.ApprovedAt, &appr.DeniedReason); err != nil {
			return nil, fmt.Errorf("scan approval: %w", err)
		}
		approvals = append(approvals, appr)
	}
	return approvals, rows.Err()
}

// ========================
// Tool Health Operations
// ========================

// UpsertToolHealth creates or updates tool health record.
func (s *Store) UpsertToolHealth(th *ToolHealth) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO tool_health (tool, version, capabilities, last_ok, last_error)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(tool) DO UPDATE SET
			version = excluded.version,
			capabilities = excluded.capabilities,
			last_ok = excluded.last_ok,
			last_error = excluded.last_error`,
		th.Tool, th.Version, th.Capabilities, th.LastOK, th.LastError,
	)
	if err != nil {
		return fmt.Errorf("upsert tool health: %w", err)
	}
	return nil
}

// GetToolHealth retrieves tool health by name.
func (s *Store) GetToolHealth(tool string) (*ToolHealth, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	th := &ToolHealth{}
	err := s.db.QueryRow(`
		SELECT tool, COALESCE(version, ''), COALESCE(capabilities, ''), last_ok, COALESCE(last_error, '')
		FROM tool_health WHERE tool = ?`, tool,
	).Scan(&th.Tool, &th.Version, &th.Capabilities, &th.LastOK, &th.LastError)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get tool health: %w", err)
	}
	return th, nil
}

// ListToolHealth returns all tool health records.
func (s *Store) ListToolHealth() ([]ToolHealth, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT tool, COALESCE(version, ''), COALESCE(capabilities, ''), last_ok, COALESCE(last_error, '')
		FROM tool_health ORDER BY tool`)
	if err != nil {
		return nil, fmt.Errorf("list tool health: %w", err)
	}
	defer rows.Close()

	var health []ToolHealth
	for rows.Next() {
		var th ToolHealth
		if err := rows.Scan(&th.Tool, &th.Version, &th.Capabilities, &th.LastOK, &th.LastError); err != nil {
			return nil, fmt.Errorf("scan tool health: %w", err)
		}
		health = append(health, th)
	}
	return health, rows.Err()
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
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT id, COALESCE(session_id, ''), event_type, event_data, COALESCE(correlation_id, ''), created_at
		FROM event_log WHERE session_id = ? AND id > ?
		ORDER BY id ASC`, sessionID, fromID)
	if err != nil {
		return fmt.Errorf("query events for replay: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var entry EventLogEntry
		if err := rows.Scan(&entry.ID, &entry.SessionID, &entry.EventType, &entry.EventData, &entry.CorrelationID, &entry.CreatedAt); err != nil {
			return fmt.Errorf("scan event for replay: %w", err)
		}
		if err := handler(entry); err != nil {
			return fmt.Errorf("replay handler error at event %d: %w", entry.ID, err)
		}
	}
	return rows.Err()
}

// ========================
// Bead History Operations
// ========================

// nullString returns sql.NullString, treating empty strings as NULL.
func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{Valid: false}
	}
	return sql.NullString{String: s, Valid: true}
}

// RecordBeadHistory records a bead state transition.
func (s *Store) RecordBeadHistory(entry *BeadHistoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entry.TransitionAt.IsZero() {
		entry.TransitionAt = time.Now().UTC()
	}

	result, err := s.db.Exec(`
		INSERT INTO bead_history (session_id, bead_id, bead_title, from_status, to_status, agent_id, agent_type, agent_name, pane, trigger, reason, prompt_sent, retry_count, transition_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nullString(entry.SessionID), entry.BeadID, entry.BeadTitle, entry.FromStatus, entry.ToStatus,
		nullString(entry.AgentID), entry.AgentType, entry.AgentName, entry.Pane,
		entry.Trigger, entry.Reason, entry.PromptSent, entry.RetryCount, entry.TransitionAt,
	)
	if err != nil {
		return fmt.Errorf("record bead history: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("get bead history id: %w", err)
	}
	entry.ID = id
	return nil
}

// GetBeadHistory retrieves the full history of a bead.
func (s *Store) GetBeadHistory(beadID string) ([]BeadHistoryEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT id, COALESCE(session_id, ''), bead_id, COALESCE(bead_title, ''), COALESCE(from_status, ''), to_status,
		       COALESCE(agent_id, ''), COALESCE(agent_type, ''), COALESCE(agent_name, ''), COALESCE(pane, 0),
		       COALESCE(trigger, ''), COALESCE(reason, ''), COALESCE(prompt_sent, ''), COALESCE(retry_count, 0), transition_at
		FROM bead_history WHERE bead_id = ?
		ORDER BY transition_at ASC`, beadID)
	if err != nil {
		return nil, fmt.Errorf("get bead history: %w", err)
	}
	defer rows.Close()

	var history []BeadHistoryEntry
	for rows.Next() {
		var entry BeadHistoryEntry
		if err := rows.Scan(&entry.ID, &entry.SessionID, &entry.BeadID, &entry.BeadTitle, &entry.FromStatus, &entry.ToStatus,
			&entry.AgentID, &entry.AgentType, &entry.AgentName, &entry.Pane,
			&entry.Trigger, &entry.Reason, &entry.PromptSent, &entry.RetryCount, &entry.TransitionAt); err != nil {
			return nil, fmt.Errorf("scan bead history: %w", err)
		}
		history = append(history, entry)
	}
	return history, rows.Err()
}

// GetBeadHistoryBySession retrieves bead history entries for a session.
// If sessionID is empty, returns entries with NULL session_id.
func (s *Store) GetBeadHistoryBySession(sessionID string, limit int) ([]BeadHistoryEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 100
	}

	var rows *sql.Rows
	var err error

	if sessionID == "" {
		rows, err = s.db.Query(`
			SELECT id, COALESCE(session_id, ''), bead_id, COALESCE(bead_title, ''), COALESCE(from_status, ''), to_status,
			       COALESCE(agent_id, ''), COALESCE(agent_type, ''), COALESCE(agent_name, ''), COALESCE(pane, 0),
			       COALESCE(trigger, ''), COALESCE(reason, ''), COALESCE(prompt_sent, ''), COALESCE(retry_count, 0), transition_at
			FROM bead_history WHERE session_id IS NULL
			ORDER BY transition_at DESC LIMIT ?`, limit)
	} else {
		rows, err = s.db.Query(`
			SELECT id, COALESCE(session_id, ''), bead_id, COALESCE(bead_title, ''), COALESCE(from_status, ''), to_status,
			       COALESCE(agent_id, ''), COALESCE(agent_type, ''), COALESCE(agent_name, ''), COALESCE(pane, 0),
			       COALESCE(trigger, ''), COALESCE(reason, ''), COALESCE(prompt_sent, ''), COALESCE(retry_count, 0), transition_at
			FROM bead_history WHERE session_id = ?
			ORDER BY transition_at DESC LIMIT ?`, sessionID, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("get bead history by session: %w", err)
	}
	defer rows.Close()

	var history []BeadHistoryEntry
	for rows.Next() {
		var entry BeadHistoryEntry
		if err := rows.Scan(&entry.ID, &entry.SessionID, &entry.BeadID, &entry.BeadTitle, &entry.FromStatus, &entry.ToStatus,
			&entry.AgentID, &entry.AgentType, &entry.AgentName, &entry.Pane,
			&entry.Trigger, &entry.Reason, &entry.PromptSent, &entry.RetryCount, &entry.TransitionAt); err != nil {
			return nil, fmt.Errorf("scan bead history: %w", err)
		}
		history = append(history, entry)
	}
	return history, rows.Err()
}

// GetBeadHistoryByStatus retrieves bead history entries filtered by to_status.
// If sessionID is empty, returns entries with NULL session_id.
func (s *Store) GetBeadHistoryByStatus(sessionID string, status BeadStatus, limit int) ([]BeadHistoryEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 100
	}

	var rows *sql.Rows
	var err error

	if sessionID == "" {
		rows, err = s.db.Query(`
			SELECT id, COALESCE(session_id, ''), bead_id, COALESCE(bead_title, ''), COALESCE(from_status, ''), to_status,
			       COALESCE(agent_id, ''), COALESCE(agent_type, ''), COALESCE(agent_name, ''), COALESCE(pane, 0),
			       COALESCE(trigger, ''), COALESCE(reason, ''), COALESCE(prompt_sent, ''), COALESCE(retry_count, 0), transition_at
			FROM bead_history WHERE session_id IS NULL AND to_status = ?
			ORDER BY transition_at DESC LIMIT ?`, status, limit)
	} else {
		rows, err = s.db.Query(`
			SELECT id, COALESCE(session_id, ''), bead_id, COALESCE(bead_title, ''), COALESCE(from_status, ''), to_status,
			       COALESCE(agent_id, ''), COALESCE(agent_type, ''), COALESCE(agent_name, ''), COALESCE(pane, 0),
			       COALESCE(trigger, ''), COALESCE(reason, ''), COALESCE(prompt_sent, ''), COALESCE(retry_count, 0), transition_at
			FROM bead_history WHERE session_id = ? AND to_status = ?
			ORDER BY transition_at DESC LIMIT ?`, sessionID, status, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("get bead history by status: %w", err)
	}
	defer rows.Close()

	var history []BeadHistoryEntry
	for rows.Next() {
		var entry BeadHistoryEntry
		if err := rows.Scan(&entry.ID, &entry.SessionID, &entry.BeadID, &entry.BeadTitle, &entry.FromStatus, &entry.ToStatus,
			&entry.AgentID, &entry.AgentType, &entry.AgentName, &entry.Pane,
			&entry.Trigger, &entry.Reason, &entry.PromptSent, &entry.RetryCount, &entry.TransitionAt); err != nil {
			return nil, fmt.Errorf("scan bead history: %w", err)
		}
		history = append(history, entry)
	}
	return history, rows.Err()
}

// GetLatestBeadStatus returns the most recent status for a bead.
func (s *Store) GetLatestBeadStatus(beadID string) (*BeadHistoryEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry := &BeadHistoryEntry{}
	err := s.db.QueryRow(`
		SELECT id, COALESCE(session_id, ''), bead_id, COALESCE(bead_title, ''), COALESCE(from_status, ''), to_status,
		       COALESCE(agent_id, ''), COALESCE(agent_type, ''), COALESCE(agent_name, ''), COALESCE(pane, 0),
		       COALESCE(trigger, ''), COALESCE(reason, ''), COALESCE(prompt_sent, ''), COALESCE(retry_count, 0), transition_at
		FROM bead_history WHERE bead_id = ?
		ORDER BY transition_at DESC LIMIT 1`, beadID,
	).Scan(&entry.ID, &entry.SessionID, &entry.BeadID, &entry.BeadTitle, &entry.FromStatus, &entry.ToStatus,
		&entry.AgentID, &entry.AgentType, &entry.AgentName, &entry.Pane,
		&entry.Trigger, &entry.Reason, &entry.PromptSent, &entry.RetryCount, &entry.TransitionAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get latest bead status: %w", err)
	}
	return entry, nil
}

// CountBeadTransitions counts the number of state transitions for a bead.
func (s *Store) CountBeadTransitions(beadID string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM bead_history WHERE bead_id = ?`, beadID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count bead transitions: %w", err)
	}
	return count, nil
}

// BeadHistoryStats contains statistics about bead history.
type BeadHistoryStats struct {
	TotalTransitions int            `json:"total_transitions"`
	ByStatus         map[string]int `json:"by_status"`
	ByAgent          map[string]int `json:"by_agent"`
	FailureReasons   map[string]int `json:"failure_reasons,omitempty"`
}

// GetBeadHistoryStats returns statistics about bead history for a session.
func (s *Store) GetBeadHistoryStats(sessionID string) (*BeadHistoryStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := &BeadHistoryStats{
		ByStatus:       make(map[string]int),
		ByAgent:        make(map[string]int),
		FailureReasons: make(map[string]int),
	}

	// Build session filter - handle NULL for empty sessionID
	var sessionFilter string
	var args []interface{}
	if sessionID == "" {
		sessionFilter = "session_id IS NULL"
	} else {
		sessionFilter = "session_id = ?"
		args = []interface{}{sessionID}
	}

	// Count total
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM bead_history WHERE `+sessionFilter, args...).Scan(&stats.TotalTransitions); err != nil {
		return nil, fmt.Errorf("count total transitions: %w", err)
	}

	// Count by status
	// #nosec G202 -- sessionFilter is internally generated, not user input
	rows, err := s.db.Query(`SELECT to_status, COUNT(*) FROM bead_history WHERE `+sessionFilter+` GROUP BY to_status`, args...)
	if err != nil {
		return nil, fmt.Errorf("count by status: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("scan status count: %w", err)
		}
		stats.ByStatus[status] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate status rows: %w", err)
	}

	// Count by agent
	// #nosec G202 -- sessionFilter is internally generated, not user input
	rows2, err := s.db.Query(`SELECT COALESCE(agent_name, agent_type), COUNT(*) FROM bead_history WHERE `+sessionFilter+` GROUP BY COALESCE(agent_name, agent_type)`, args...)
	if err != nil {
		return nil, fmt.Errorf("count by agent: %w", err)
	}
	defer rows2.Close()
	for rows2.Next() {
		var agent string
		var count int
		if err := rows2.Scan(&agent, &count); err != nil {
			return nil, fmt.Errorf("scan agent count: %w", err)
		}
		if agent != "" {
			stats.ByAgent[agent] = count
		}
	}
	if err := rows2.Err(); err != nil {
		return nil, fmt.Errorf("iterate agent rows: %w", err)
	}

	// Count failure reasons
	failedArgs := append(args[:0:0], args...) // Copy args to avoid modifying original
	// #nosec G202 -- sessionFilter is internally generated, not user input
	rows3, err := s.db.Query(`SELECT reason, COUNT(*) FROM bead_history WHERE `+sessionFilter+` AND to_status = 'failed' AND reason != '' GROUP BY reason`, failedArgs...)
	if err != nil {
		return nil, fmt.Errorf("count failure reasons: %w", err)
	}
	defer rows3.Close()
	for rows3.Next() {
		var reason string
		var count int
		if err := rows3.Scan(&reason, &count); err != nil {
			return nil, fmt.Errorf("scan reason count: %w", err)
		}
		stats.FailureReasons[reason] = count
	}
	if err := rows3.Err(); err != nil {
		return nil, fmt.Errorf("iterate reason rows: %w", err)
	}

	return stats, nil
}
