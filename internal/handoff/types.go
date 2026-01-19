// Package handoff provides the canonical YAML handoff format for context preservation.
// Handoffs are compact (~400 tokens vs ~2000 for markdown) representations of session
// state that can be passed between agent sessions for continuity.
package handoff

import "time"

// HandoffVersion tracks the format version for backwards compatibility and migrations.
const HandoffVersion = "1.0"

// Handoff represents a complete context handoff between sessions.
// The Goal and Now fields are REQUIRED and used by the status line integration.
type Handoff struct {
	// Metadata
	Version   string    `yaml:"version"`    // Format version for migrations
	Session   string    `yaml:"session"`    // Session identifier
	Date      string    `yaml:"date"`       // Date in YYYY-MM-DD format
	CreatedAt time.Time `yaml:"created_at"` // Precise creation timestamp
	UpdatedAt time.Time `yaml:"updated_at"` // Last update timestamp

	// Status tracking
	Status  string `yaml:"status"`  // complete|partial|blocked
	Outcome string `yaml:"outcome"` // SUCCEEDED|PARTIAL_PLUS|PARTIAL_MINUS|FAILED

	// Core fields (REQUIRED for status line)
	Goal string `yaml:"goal"` // What this session accomplished - REQUIRED
	Now  string `yaml:"now"`  // What next session should do first - REQUIRED
	Test string `yaml:"test"` // Command to verify this work

	// Work tracking
	DoneThisSession []TaskRecord `yaml:"done_this_session,omitempty"`

	// Context for future self
	Blockers  []string          `yaml:"blockers,omitempty"`
	Questions []string          `yaml:"questions,omitempty"`
	Decisions map[string]string `yaml:"decisions,omitempty"`
	Findings  map[string]string `yaml:"findings,omitempty"`

	// What worked and what didn't
	Worked []string `yaml:"worked,omitempty"`
	Failed []string `yaml:"failed,omitempty"`

	// Next steps
	Next []string `yaml:"next,omitempty"`

	// File tracking
	Files FileChanges `yaml:"files,omitempty"`

	// Integration fields - populated during recovery
	ActiveBeads      []string `yaml:"active_beads,omitempty"`       // From BV
	AgentMailThreads []string `yaml:"agent_mail_threads,omitempty"` // From Agent Mail
	CMMemories       []string `yaml:"cm_memories,omitempty"`        // From CM

	// Agent info for multi-agent sessions
	AgentID   string `yaml:"agent_id,omitempty"`
	AgentType string `yaml:"agent_type,omitempty"` // cc, cod, gmi
	PaneID    string `yaml:"pane_id,omitempty"`

	// Token context at time of handoff
	TokensUsed int     `yaml:"tokens_used,omitempty"`
	TokensMax  int     `yaml:"tokens_max,omitempty"`
	TokensPct  float64 `yaml:"tokens_pct,omitempty"`
}

// TaskRecord represents a completed task with associated file changes.
type TaskRecord struct {
	Task  string   `yaml:"task"`
	Files []string `yaml:"files,omitempty"`
}

// FileChanges tracks file modifications during a session.
type FileChanges struct {
	Created  []string `yaml:"created,omitempty"`
	Modified []string `yaml:"modified,omitempty"`
	Deleted  []string `yaml:"deleted,omitempty"`
}

// New creates a new Handoff with defaults populated.
func New(session string) *Handoff {
	now := time.Now()
	return &Handoff{
		Version:   HandoffVersion,
		Session:   session,
		Date:      now.Format("2006-01-02"),
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// WithGoalAndNow is a convenience method for setting the required fields.
func (h *Handoff) WithGoalAndNow(goal, now string) *Handoff {
	h.Goal = goal
	h.Now = now
	return h
}

// WithStatus sets the status and outcome fields.
func (h *Handoff) WithStatus(status, outcome string) *Handoff {
	h.Status = status
	h.Outcome = outcome
	return h
}

// AddTask adds a completed task record.
func (h *Handoff) AddTask(task string, files ...string) *Handoff {
	h.DoneThisSession = append(h.DoneThisSession, TaskRecord{
		Task:  task,
		Files: files,
	})
	return h
}

// AddBlocker adds a blocker to the list.
func (h *Handoff) AddBlocker(blocker string) *Handoff {
	h.Blockers = append(h.Blockers, blocker)
	return h
}

// AddDecision records a key decision.
func (h *Handoff) AddDecision(key, value string) *Handoff {
	if h.Decisions == nil {
		h.Decisions = make(map[string]string)
	}
	h.Decisions[key] = value
	return h
}

// AddFinding records an important discovery.
func (h *Handoff) AddFinding(key, value string) *Handoff {
	if h.Findings == nil {
		h.Findings = make(map[string]string)
	}
	h.Findings[key] = value
	return h
}

// MarkCreated adds a file to the created list.
func (h *Handoff) MarkCreated(files ...string) *Handoff {
	h.Files.Created = append(h.Files.Created, files...)
	return h
}

// MarkModified adds a file to the modified list.
func (h *Handoff) MarkModified(files ...string) *Handoff {
	h.Files.Modified = append(h.Files.Modified, files...)
	return h
}

// MarkDeleted adds a file to the deleted list.
func (h *Handoff) MarkDeleted(files ...string) *Handoff {
	h.Files.Deleted = append(h.Files.Deleted, files...)
	return h
}

// SetAgentInfo sets the agent-related fields.
func (h *Handoff) SetAgentInfo(agentID, agentType, paneID string) *Handoff {
	h.AgentID = agentID
	h.AgentType = agentType
	h.PaneID = paneID
	return h
}

// SetTokenInfo sets the token context fields.
func (h *Handoff) SetTokenInfo(used, max int) *Handoff {
	h.TokensUsed = used
	h.TokensMax = max
	if max > 0 {
		h.TokensPct = float64(used) / float64(max) * 100
	}
	return h
}

// IsComplete returns true if the handoff is marked as complete.
func (h *Handoff) IsComplete() bool {
	return h.Status == StatusComplete
}

// IsBlocked returns true if the handoff is marked as blocked.
func (h *Handoff) IsBlocked() bool {
	return h.Status == StatusBlocked
}

// HasChanges returns true if any file changes were recorded.
func (h *Handoff) HasChanges() bool {
	return len(h.Files.Created) > 0 ||
		len(h.Files.Modified) > 0 ||
		len(h.Files.Deleted) > 0
}

// TotalFileChanges returns the total number of file changes.
func (h *Handoff) TotalFileChanges() int {
	return len(h.Files.Created) + len(h.Files.Modified) + len(h.Files.Deleted)
}
