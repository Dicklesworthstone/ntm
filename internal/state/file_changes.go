package state

// file_changes.go — durable, cross-process ledger of agent file changes.
//
// The surfaces that report file changes and conflicts (`ntm changes`,
// `ntm conflicts`, --robot-status, the dashboard Files panel, the
// work-coordination adapter) each run in their own process, while the session
// monitor is what actually observes the changes. A per-process in-memory ring
// therefore could never carry data from the observer to any reader; this table
// is what does.

import (
	"fmt"
	"time"
)

// FileChangeRow is one recorded change to one path.
type FileChangeRow struct {
	ID         int64     `json:"id"`
	ProjectDir string    `json:"project_dir"`
	Session    string    `json:"session,omitempty"`
	Agent      string    `json:"agent,omitempty"`
	Path       string    `json:"path"`
	ChangeType string    `json:"change_type"`
	CreatedAt  time.Time `json:"created_at"`
}

// FileChangeRetention bounds how far back the ledger is kept. Conflict windows
// are measured in minutes and the widest reader window is 24h, so a week is
// generous while keeping the table from growing without limit.
const FileChangeRetention = 7 * 24 * time.Hour

// AppendFileChanges records a batch of changes in one transaction and prunes
// rows past FileChangeRetention.
//
// Pruning happens here, on the write path, so the ledger is self-limiting
// without adding a maintenance surface that has to be remembered and wired.
func (s *Store) AppendFileChanges(rows []FileChangeRow) error {
	if len(rows) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin file change append: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`
		INSERT INTO file_changes (project_dir, session, agent, path, change_type, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare file change insert: %w", err)
	}
	defer stmt.Close()

	for _, row := range rows {
		if row.ProjectDir == "" || row.Path == "" || row.ChangeType == "" {
			// A row with no project, path or type cannot be queried back
			// meaningfully; dropping it beats poisoning the ledger.
			continue
		}
		created := row.CreatedAt
		if created.IsZero() {
			created = time.Now().UTC()
		}
		if _, err := stmt.Exec(row.ProjectDir, row.Session, row.Agent, row.Path, row.ChangeType, created.UTC()); err != nil {
			return fmt.Errorf("insert file change for %s: %w", row.Path, err)
		}
	}

	if _, err := tx.Exec(`DELETE FROM file_changes WHERE created_at < ?`,
		time.Now().UTC().Add(-FileChangeRetention)); err != nil {
		return fmt.Errorf("prune file changes: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit file changes: %w", err)
	}
	return nil
}

// FileChangesSince returns the changes recorded for projectDir after since,
// oldest first. An empty projectDir returns nothing rather than every project's
// rows: state.db is machine-wide, and reporting another repository's conflicts
// to someone standing in this one would be worse than reporting none.
func (s *Store) FileChangesSince(projectDir string, since time.Time) ([]FileChangeRow, error) {
	if projectDir == "" {
		return nil, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT id, project_dir, session, agent, path, change_type, created_at
		FROM file_changes
		WHERE project_dir = ? AND created_at > ?
		ORDER BY created_at ASC, id ASC`, projectDir, since.UTC())
	if err != nil {
		return nil, fmt.Errorf("query file changes: %w", err)
	}
	defer rows.Close()

	var results []FileChangeRow
	for rows.Next() {
		var row FileChangeRow
		if err := rows.Scan(&row.ID, &row.ProjectDir, &row.Session, &row.Agent,
			&row.Path, &row.ChangeType, &row.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan file change: %w", err)
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate file changes: %w", err)
	}
	return results, nil
}
