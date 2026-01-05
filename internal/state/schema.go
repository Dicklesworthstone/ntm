// Package state provides durable SQLite-backed storage for NTM orchestration state.
// This enables crash recovery, session re-attach, and event-driven UIs.
package state

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrations embed.FS

// SessionStatus represents the state of an NTM session.
type SessionStatus string

const (
	SessionActive     SessionStatus = "active"
	SessionPaused     SessionStatus = "paused"
	SessionTerminated SessionStatus = "terminated"
)

// AgentStatus represents the operational state of an agent.
type AgentStatus string

const (
	AgentIdle    AgentStatus = "idle"
	AgentWorking AgentStatus = "working"
	AgentError   AgentStatus = "error"
	AgentCrashed AgentStatus = "crashed"
)

// AgentType represents the type of AI agent.
type AgentType string

const (
	AgentTypeClaude AgentType = "cc"
	AgentTypeCodex  AgentType = "cod"
	AgentTypeGemini AgentType = "gmi"
)

// TaskStatus represents the state of a task.
type TaskStatus string

const (
	TaskPending   TaskStatus = "pending"
	TaskAssigned  TaskStatus = "assigned"
	TaskWorking   TaskStatus = "working"
	TaskCompleted TaskStatus = "completed"
	TaskFailed    TaskStatus = "failed"
)

// TaskResult represents the outcome of a completed task.
type TaskResult string

const (
	TaskResultSuccess TaskResult = "success"
	TaskResultFailure TaskResult = "failure"
	TaskResultPartial TaskResult = "partial"
)

// ApprovalStatus represents the state of an approval request.
type ApprovalStatus string

const (
	ApprovalPending  ApprovalStatus = "pending"
	ApprovalApproved ApprovalStatus = "approved"
	ApprovalDenied   ApprovalStatus = "denied"
	ApprovalExpired  ApprovalStatus = "expired"
)

// Session represents an NTM orchestration session.
type Session struct {
	ID               string        `json:"id"`
	Name             string        `json:"name"`
	ProjectPath      string        `json:"project_path"`
	CreatedAt        time.Time     `json:"created_at"`
	Status           SessionStatus `json:"status"`
	ConfigSnapshot   string        `json:"config_snapshot,omitempty"`   // JSON of config at spawn time
	CoordinatorAgent string        `json:"coordinator_agent,omitempty"` // Agent Mail name
}

// Agent represents an AI agent within a session.
type Agent struct {
	ID              string      `json:"id"`
	SessionID       string      `json:"session_id"`
	Name            string      `json:"name"` // Agent Mail name (e.g., "GreenLake")
	Type            AgentType   `json:"type"` // cc, cod, gmi
	Model           string      `json:"model,omitempty"`
	TmuxPaneID      string      `json:"tmux_pane_id,omitempty"`
	LastSeen        *time.Time  `json:"last_seen,omitempty"`
	Status          AgentStatus `json:"status"`
	CurrentTaskID   string      `json:"current_task_id,omitempty"`
	PerformanceData string      `json:"performance_data,omitempty"` // JSON of performance stats
}

// Task represents a unit of work assigned to an agent.
type Task struct {
	ID            string      `json:"id"`
	SessionID     string      `json:"session_id"`
	AgentID       string      `json:"agent_id,omitempty"`
	BeadID        string      `json:"bead_id,omitempty"`
	CorrelationID string      `json:"correlation_id,omitempty"`
	ContextPackID string      `json:"context_pack_id,omitempty"`
	Status        TaskStatus  `json:"status"`
	CreatedAt     time.Time   `json:"created_at"`
	AssignedAt    *time.Time  `json:"assigned_at,omitempty"`
	CompletedAt   *time.Time  `json:"completed_at,omitempty"`
	Result        *TaskResult `json:"result,omitempty"`
}

// Reservation represents a file reservation (advisory lock) for an agent.
type Reservation struct {
	ID              int64      `json:"id"`
	SessionID       string     `json:"session_id"`
	AgentID         string     `json:"agent_id"`
	PathPattern     string     `json:"path_pattern"`
	Exclusive       bool       `json:"exclusive"`
	CorrelationID   string     `json:"correlation_id,omitempty"`
	Reason          string     `json:"reason,omitempty"`
	ExpiresAt       time.Time  `json:"expires_at"`
	ReleasedAt      *time.Time `json:"released_at,omitempty"`
	ForceReleasedBy string     `json:"force_released_by,omitempty"`
}

// Approval represents a pending approval request for a sensitive action.
type Approval struct {
	ID            string         `json:"id"`
	Action        string         `json:"action"`
	Resource      string         `json:"resource"`
	Reason        string         `json:"reason,omitempty"`
	RequestedBy   string         `json:"requested_by"`
	CorrelationID string         `json:"correlation_id,omitempty"`
	RequiresSLB   bool           `json:"requires_slb"` // "Stop, Look, Broadcast"
	CreatedAt     time.Time      `json:"created_at"`
	ExpiresAt     time.Time      `json:"expires_at"`
	Status        ApprovalStatus `json:"status"`
	ApprovedBy    string         `json:"approved_by,omitempty"`
	ApprovedAt    *time.Time     `json:"approved_at,omitempty"`
	DeniedReason  string         `json:"denied_reason,omitempty"`
}

// ContextPack represents a pre-built context prompt for a task.
type ContextPack struct {
	ID             string    `json:"id"`
	BeadID         string    `json:"bead_id"`
	AgentType      AgentType `json:"agent_type"`
	RepoRev        string    `json:"repo_rev"`
	CorrelationID  string    `json:"correlation_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	TokenCount     int       `json:"token_count,omitempty"`
	RenderedPrompt string    `json:"rendered_prompt,omitempty"`
}

// ToolHealth records the health status of ecosystem tools.
type ToolHealth struct {
	Tool         string     `json:"tool"`
	Version      string     `json:"version,omitempty"`
	Capabilities string     `json:"capabilities,omitempty"` // JSON array
	LastOK       *time.Time `json:"last_ok,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
}

// MigrationInfo tracks applied migrations.
type MigrationInfo struct {
	Version   int       `json:"version"`
	Name      string    `json:"name"`
	AppliedAt time.Time `json:"applied_at"`
}

// GetMigrationFiles returns a sorted list of migration file names.
func GetMigrationFiles() ([]string, error) {
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	return files, nil
}

// ReadMigration reads the content of a migration file.
func ReadMigration(filename string) (string, error) {
	data, err := migrations.ReadFile(filepath.Join("migrations", filename))
	if err != nil {
		return "", fmt.Errorf("read migration %s: %w", filename, err)
	}
	return string(data), nil
}

// ApplyMigrations applies all pending migrations to the database.
func ApplyMigrations(db *sql.DB) error {
	// Create migrations table if not exists
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS _migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	// Get list of migration files
	files, err := GetMigrationFiles()
	if err != nil {
		return err
	}

	// Get applied migrations
	rows, err := db.Query("SELECT version FROM _migrations ORDER BY version")
	if err != nil {
		return fmt.Errorf("query applied migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	applied := make(map[int]bool)
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return fmt.Errorf("scan migration version: %w", err)
		}
		applied[version] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate migrations: %w", err)
	}

	// Apply pending migrations
	for _, filename := range files {
		// Parse version from filename (e.g., "001_initial.sql" -> 1)
		var version int
		n, _ := fmt.Sscanf(filename, "%03d_", &version)
		if n != 1 {
			return fmt.Errorf("parse migration version from %s: invalid format", filename)
		}

		if applied[version] {
			continue // Already applied
		}

		// Read and execute migration
		content, err := ReadMigration(filename)
		if err != nil {
			return err
		}

		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin transaction for migration %s: %w", filename, err)
		}

		if _, err := tx.Exec(content); err != nil {
			_ = tx.Rollback() // best-effort rollback; we're returning the exec error
			return fmt.Errorf("execute migration %s: %w", filename, err)
		}

		// Record migration
		if _, err := tx.Exec(
			"INSERT INTO _migrations (version, name) VALUES (?, ?)",
			version, filename,
		); err != nil {
			_ = tx.Rollback() // best-effort rollback; we're returning the insert error
			return fmt.Errorf("record migration %s: %w", filename, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", filename, err)
		}
	}

	return nil
}
