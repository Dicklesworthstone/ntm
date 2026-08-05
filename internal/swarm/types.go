package swarm

import "time"

// ProjectBeadCount represents a project and its open bead count.
type ProjectBeadCount struct {
	Path      string `json:"path"`       // Absolute path to project
	Name      string `json:"name"`       // Project directory name
	OpenBeads int    `json:"open_beads"` // Count of open beads
	Tier      int    `json:"tier"`       // Calculated tier (1, 2, or 3)
}

// ProjectAllocation represents the agent allocation for a single project.
type ProjectAllocation struct {
	Project     ProjectBeadCount `json:"project"`
	CCAgents    int              `json:"cc_agents"`
	CodAgents   int              `json:"cod_agents"`
	GmiAgents   int              `json:"gmi_agents"`
	AgyAgents   int              `json:"agy_agents"`
	TotalAgents int              `json:"total_agents"`
}

// SwarmPlan is the complete execution plan for the weighted swarm.
type SwarmPlan struct {
	// Metadata
	CreatedAt time.Time `json:"created_at"`
	ScanDir   string    `json:"scan_dir"`

	// Per-project allocations
	Allocations []ProjectAllocation `json:"allocations"`

	// Aggregate totals
	TotalCC     int `json:"total_cc"`
	TotalCod    int `json:"total_cod"`
	TotalGmi    int `json:"total_gmi"`
	TotalAgy    int `json:"total_agy"`
	TotalAgents int `json:"total_agents"`

	// PlannedAgents is how many panes the generated sessions actually hold.
	// It normally equals TotalAgents, but a manual --panes-per-session that
	// cannot hold the allocation makes the session grid smaller than the
	// allocation, and the leftover agents are dropped. Reporting only
	// TotalAgents let the plan claim 20 agents while launching 6, with no
	// surface anywhere saying 14 were discarded.
	PlannedAgents int `json:"planned_agents"`

	// AutoRotateAccounts enables automatic account rotation on limit hit (requires caam).
	AutoRotateAccounts bool `json:"auto_rotate_accounts"`

	// Session structure
	SessionsPerType int `json:"sessions_per_type"`
	PanesPerSession int `json:"panes_per_session"` // Calculated: ceil(total/sessions)

	// Session names to create
	Sessions []SessionSpec `json:"sessions"`

	// Ensemble config for ensemble-aware session creation (optional).
	Ensemble *EnsemblePlan `json:"ensemble,omitempty"`
}

// EnsemblePlan describes an ensemble configuration embedded in a swarm plan.
type EnsemblePlan struct {
	Question  string         `json:"question"`
	Preset    string         `json:"preset,omitempty"`
	Modes     []string       `json:"modes,omitempty"`
	AgentMix  map[string]int `json:"agent_mix,omitempty"`
	Synthesis string         `json:"synthesis,omitempty"`
}

// SessionSpec describes a tmux session to create.
type SessionSpec struct {
	Name      string     `json:"name"`       // e.g., "cc_agents_1"
	AgentType string     `json:"agent_type"` // "cc", "cod", or "gmi"
	PaneCount int        `json:"pane_count"`
	Panes     []PaneSpec `json:"panes"`
}

// PaneSpec describes a pane within a session.
type PaneSpec struct {
	Index      int    `json:"index"`   // 1-based pane index
	Project    string `json:"project"` // Which project this pane works on
	AgentType  string `json:"agent_type"`
	AgentIndex int    `json:"agent_index"` // Agent number within project
	LaunchCmd  string `json:"launch_cmd"`  // "cc", "cod", or "gmi"
}

// SwarmState tracks the runtime state of a running swarm.
type SwarmState struct {
	Plan       *SwarmPlan           `json:"plan"`
	StartedAt  time.Time            `json:"started_at"`
	PaneStates map[string]PaneState `json:"pane_states"` // key: "session:pane"
	LimitHits  []LimitHitEvent      `json:"limit_hits"`
	Respawns   []RespawnEvent       `json:"respawns"`
}

// PaneState tracks individual pane runtime state.
type PaneState struct {
	SessionPane  string     `json:"session_pane"` // "cc_agents_1:1.5"
	AgentType    string     `json:"agent_type"`
	Project      string     `json:"project"`
	Status       string     `json:"status"` // "running", "limit_hit", "respawning"
	LastActivity time.Time  `json:"last_activity"`
	LimitHitAt   *time.Time `json:"limit_hit_at,omitempty"`
	RespawnCount int        `json:"respawn_count"`
}

// LimitHitEvent records when an agent hits usage limits.
type LimitHitEvent struct {
	SessionPane string    `json:"session_pane"`
	AgentType   string    `json:"agent_type"`
	Project     string    `json:"project"`
	DetectedAt  time.Time `json:"detected_at"`
	Pattern     string    `json:"pattern"` // Which pattern matched
}

// RespawnEvent records agent respawns.
type RespawnEvent struct {
	SessionPane     string    `json:"session_pane"`
	AgentType       string    `json:"agent_type"`
	RespawnedAt     time.Time `json:"respawned_at"`
	AccountRotated  bool      `json:"account_rotated"`
	PreviousAccount string    `json:"previous_account,omitempty"`
	NewAccount      string    `json:"new_account,omitempty"`
}
