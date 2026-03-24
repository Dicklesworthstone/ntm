// Package state provides durable SQLite-backed storage for NTM orchestration state.
package state

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// EnsembleSession represents persisted ensemble state.
type EnsembleSession struct {
	ID                int64            `json:"id"`
	SessionName       string           `json:"session_name"`
	Question          string           `json:"question"`
	PresetUsed        string           `json:"preset_used,omitempty"`
	Status            string           `json:"status"`
	SynthesisStrategy string           `json:"synthesis_strategy,omitempty"`
	CreatedAt         time.Time        `json:"created_at"`
	SynthesizedAt     *time.Time       `json:"synthesized_at,omitempty"`
	SynthesisOutput   string           `json:"synthesis_output,omitempty"`
	Error             string           `json:"error,omitempty"`
	Assignments       []ModeAssignment `json:"assignments,omitempty"`
}

// ModeAssignment represents a persisted mode assignment for an ensemble session.
type ModeAssignment struct {
	ID          int64      `json:"id"`
	EnsembleID  int64      `json:"ensemble_id"`
	ModeID      string     `json:"mode_id"`
	PaneName    string     `json:"pane_name"`
	AgentType   string     `json:"agent_type"`
	Status      string     `json:"status"`
	OutputPath  string     `json:"output_path,omitempty"`
	AssignedAt  time.Time  `json:"assigned_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	Error       string     `json:"error,omitempty"`
}

// EnsembleStore provides persistence for ensemble sessions.
type EnsembleStore struct {
	store *Store
}

// NewEnsembleStore returns a new EnsembleStore bound to the provided Store.
func NewEnsembleStore(store *Store) *EnsembleStore {
	if store == nil {
		return nil
	}
	return &EnsembleStore{store: store}
}

// SaveEnsemble inserts or updates an ensemble session and its assignments.
func (s *EnsembleStore) SaveEnsemble(e *EnsembleSession) error {
	if s == nil || s.store == nil {
		return errors.New("ensemble store is nil")
	}
	if e == nil {
		return errors.New("ensemble session is nil")
	}
	if e.SessionName == "" {
		return errors.New("session name is required")
	}
	if e.Question == "" {
		return errors.New("question is required")
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}

	s.store.mu.Lock()
	defer s.store.mu.Unlock()

	tx, err := s.store.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit; guards against panics

	if err := func() error {
		result, err := tx.Exec(`
			UPDATE ensemble_sessions
			SET question = ?, preset_used = ?, status = ?, synthesis_strategy = ?, synthesized_at = ?, synthesis_output = ?, error = ?
			WHERE session_name = ?`,
			e.Question, e.PresetUsed, e.Status, e.SynthesisStrategy, e.SynthesizedAt, e.SynthesisOutput, e.Error, e.SessionName,
		)
		if err != nil {
			return fmt.Errorf("update ensemble session: %w", err)
		}

		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return fmt.Errorf("rows affected: %w", rowsErr)
		}
		if rows == 0 {
			_, err := tx.Exec(`
				INSERT INTO ensemble_sessions
					(session_name, question, preset_used, status, synthesis_strategy, created_at, synthesized_at, synthesis_output, error)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				e.SessionName, e.Question, e.PresetUsed, e.Status, e.SynthesisStrategy, e.CreatedAt, e.SynthesizedAt, e.SynthesisOutput, e.Error,
			)
			if err != nil {
				return fmt.Errorf("insert ensemble session: %w", err)
			}
		}

		var ensembleID int64
		if err := tx.QueryRow(`SELECT id FROM ensemble_sessions WHERE session_name = ?`, e.SessionName).Scan(&ensembleID); err != nil {
			return fmt.Errorf("fetch ensemble id: %w", err)
		}
		e.ID = ensembleID

		if _, err := tx.Exec(`DELETE FROM mode_assignments WHERE ensemble_id = ?`, ensembleID); err != nil {
			return fmt.Errorf("clear mode assignments: %w", err)
		}

		if len(e.Assignments) == 0 {
			return nil
		}

		stmt, err := tx.Prepare(`
			INSERT INTO mode_assignments
				(ensemble_id, mode_id, pane_name, agent_type, status, output_path, assigned_at, completed_at, error)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return fmt.Errorf("prepare assignment insert: %w", err)
		}
		defer stmt.Close()

		for i := range e.Assignments {
			assignment := &e.Assignments[i]
			if assignment.ModeID == "" {
				return fmt.Errorf("assignment mode_id is required")
			}
			if assignment.PaneName == "" {
				return fmt.Errorf("assignment pane_name is required")
			}
			status := assignment.Status
			if status == "" {
				status = "pending"
			}
			assignedAt := sql.NullTime{Valid: false}
			if !assignment.AssignedAt.IsZero() {
				assignedAt = sql.NullTime{Time: assignment.AssignedAt, Valid: true}
			}
			completedAt := sql.NullTime{Valid: false}
			if assignment.CompletedAt != nil && !assignment.CompletedAt.IsZero() {
				completedAt = sql.NullTime{Time: *assignment.CompletedAt, Valid: true}
			}
			if _, err := stmt.Exec(
				ensembleID,
				assignment.ModeID,
				assignment.PaneName,
				assignment.AgentType,
				status,
				assignment.OutputPath,
				assignedAt,
				completedAt,
				assignment.Error,
			); err != nil {
				return fmt.Errorf("insert assignment: %w", err)
			}
		}

		return nil
	}(); err != nil {
		_ = tx.Rollback()
		return err
	}

	return tx.Commit()
}

// GetEnsemble retrieves an ensemble session and its assignments by session name.
func (s *EnsembleStore) GetEnsemble(sessionName string) (*EnsembleSession, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("ensemble store is nil")
	}
	if sessionName == "" {
		return nil, errors.New("session name is required")
	}

	s.store.mu.RLock()
	defer s.store.mu.RUnlock()

	var (
		session       EnsembleSession
		synthesizedAt sql.NullTime
	)

	err := s.store.db.QueryRow(`
		SELECT id, session_name, question, COALESCE(preset_used, ''), status,
		       COALESCE(synthesis_strategy, ''), created_at, synthesized_at,
		       COALESCE(synthesis_output, ''), COALESCE(error, '')
		FROM ensemble_sessions
		WHERE session_name = ?`, sessionName,
	).Scan(
		&session.ID,
		&session.SessionName,
		&session.Question,
		&session.PresetUsed,
		&session.Status,
		&session.SynthesisStrategy,
		&session.CreatedAt,
		&synthesizedAt,
		&session.SynthesisOutput,
		&session.Error,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get ensemble session: %w", err)
	}
	if synthesizedAt.Valid {
		session.SynthesizedAt = &synthesizedAt.Time
	}

	assignments, err := s.fetchAssignments(session.ID)
	if err != nil {
		return nil, err
	}
	session.Assignments = assignments

	return &session, nil
}

// UpdateStatus updates the status of an ensemble session.
func (s *EnsembleStore) UpdateStatus(sessionName string, status string) error {
	if s == nil || s.store == nil {
		return errors.New("ensemble store is nil")
	}
	if sessionName == "" {
		return errors.New("session name is required")
	}

	s.store.mu.Lock()
	defer s.store.mu.Unlock()

	result, err := s.store.db.Exec(`
		UPDATE ensemble_sessions
		SET status = ?
		WHERE session_name = ?`, status, sessionName)
	if err != nil {
		return fmt.Errorf("update ensemble status: %w", err)
	}

	rows, rowsErr := result.RowsAffected()
	if rowsErr != nil {
		return fmt.Errorf("rows affected: %w", rowsErr)
	}
	if rows == 0 {
		return fmt.Errorf("ensemble session not found: %s", sessionName)
	}
	return nil
}

// UpdateAssignmentStatus updates the status of a mode assignment.
func (s *EnsembleStore) UpdateAssignmentStatus(sessionName, modeID, status string) error {
	if s == nil || s.store == nil {
		return errors.New("ensemble store is nil")
	}
	if sessionName == "" || modeID == "" {
		return errors.New("session name and mode id are required")
	}

	s.store.mu.Lock()
	defer s.store.mu.Unlock()

	result, err := s.store.db.Exec(`
		UPDATE mode_assignments
		SET status = ?
		WHERE ensemble_id = (SELECT id FROM ensemble_sessions WHERE session_name = ?)
		  AND mode_id = ?`, status, sessionName, modeID)
	if err != nil {
		return fmt.Errorf("update assignment status: %w", err)
	}

	rows, rowsErr := result.RowsAffected()
	if rowsErr != nil {
		return fmt.Errorf("rows affected: %w", rowsErr)
	}
	if rows == 0 {
		return fmt.Errorf("assignment not found: %s/%s", sessionName, modeID)
	}
	return nil
}

// ListEnsembles returns all ensemble sessions and their assignments.
func (s *EnsembleStore) ListEnsembles() ([]*EnsembleSession, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("ensemble store is nil")
	}

	s.store.mu.RLock()
	defer s.store.mu.RUnlock()

	rows, err := s.store.db.Query(`
		SELECT id, session_name, question, COALESCE(preset_used, ''), status,
		       COALESCE(synthesis_strategy, ''), created_at, synthesized_at,
		       COALESCE(synthesis_output, ''), COALESCE(error, '')
		FROM ensemble_sessions
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list ensembles: %w", err)
	}
	defer rows.Close()

	var sessions []*EnsembleSession
	for rows.Next() {
		var (
			session       EnsembleSession
			synthesizedAt sql.NullTime
		)
		if err := rows.Scan(
			&session.ID,
			&session.SessionName,
			&session.Question,
			&session.PresetUsed,
			&session.Status,
			&session.SynthesisStrategy,
			&session.CreatedAt,
			&synthesizedAt,
			&session.SynthesisOutput,
			&session.Error,
		); err != nil {
			return nil, fmt.Errorf("scan ensemble: %w", err)
		}
		if synthesizedAt.Valid {
			session.SynthesizedAt = &synthesizedAt.Time
		}

		assignments, err := s.fetchAssignments(session.ID)
		if err != nil {
			return nil, err
		}
		session.Assignments = assignments
		sessions = append(sessions, &session)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list ensembles: %w", err)
	}

	return sessions, nil
}

// DeleteEnsemble deletes an ensemble session and its assignments.
func (s *EnsembleStore) DeleteEnsemble(sessionName string) error {
	if s == nil || s.store == nil {
		return errors.New("ensemble store is nil")
	}
	if sessionName == "" {
		return errors.New("session name is required")
	}

	s.store.mu.Lock()
	defer s.store.mu.Unlock()

	tx, err := s.store.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit; guards against panics

	if err := func() error {
		var ensembleID int64
		if err := tx.QueryRow(`SELECT id FROM ensemble_sessions WHERE session_name = ?`, sessionName).Scan(&ensembleID); err != nil {
			if err == sql.ErrNoRows {
				return fmt.Errorf("ensemble session not found: %s", sessionName)
			}
			return fmt.Errorf("lookup ensemble session: %w", err)
		}

		if _, err := tx.Exec(`DELETE FROM mode_assignments WHERE ensemble_id = ?`, ensembleID); err != nil {
			return fmt.Errorf("delete assignments: %w", err)
		}

		result, err := tx.Exec(`DELETE FROM ensemble_sessions WHERE id = ?`, ensembleID)
		if err != nil {
			return fmt.Errorf("delete ensemble: %w", err)
		}

		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return fmt.Errorf("rows affected: %w", rowsErr)
		}
		if rows == 0 {
			return fmt.Errorf("ensemble session not found: %s", sessionName)
		}
		return nil
	}(); err != nil {
		_ = tx.Rollback()
		return err
	}

	return tx.Commit()
}

func (s *EnsembleStore) fetchAssignments(ensembleID int64) ([]ModeAssignment, error) {
	rows, err := s.store.db.Query(`
		SELECT id, ensemble_id, mode_id, pane_name, agent_type, status, COALESCE(output_path, ''),
		       assigned_at, completed_at, COALESCE(error, '')
		FROM mode_assignments
		WHERE ensemble_id = ?
		ORDER BY id`, ensembleID)
	if err != nil {
		return nil, fmt.Errorf("list assignments: %w", err)
	}
	defer rows.Close()

	var assignments []ModeAssignment
	for rows.Next() {
		var (
			assignment  ModeAssignment
			assignedAt  sql.NullTime
			completedAt sql.NullTime
		)
		if err := rows.Scan(
			&assignment.ID,
			&assignment.EnsembleID,
			&assignment.ModeID,
			&assignment.PaneName,
			&assignment.AgentType,
			&assignment.Status,
			&assignment.OutputPath,
			&assignedAt,
			&completedAt,
			&assignment.Error,
		); err != nil {
			return nil, fmt.Errorf("scan assignment: %w", err)
		}
		if assignedAt.Valid {
			assignment.AssignedAt = assignedAt.Time
		}
		if completedAt.Valid {
			assignment.CompletedAt = &completedAt.Time
		}
		assignments = append(assignments, assignment)
	}

	return assignments, rows.Err()
}
