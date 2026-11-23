package robot

import (
	"errors"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/swarm"
)

const (
	scaleSchemaID            = "ntm:robot:scale:v1"
	scaleSchemaVersion       = "1.0.0"
	rebalanceSchemaID        = "ntm:robot:rebalance:v1"
	rebalanceSchemaVersion   = "1.0.0"
	coordinatorSchemaID      = "ntm:robot:coordinator:v1"
	coordinatorSchemaVersion = "1.0.0"

	// Exported constants for cross-package CLI/Kernel envelope construction.
	ScaleSchemaID            = scaleSchemaID
	ScaleSchemaVersion       = scaleSchemaVersion
	RebalanceSchemaID        = rebalanceSchemaID
	RebalanceSchemaVersion   = rebalanceSchemaVersion
	CoordinatorSchemaID      = coordinatorSchemaID
	CoordinatorSchemaVersion = coordinatorSchemaVersion
)

// =============================================================================
// Scale Robot & Kernel Wire Types
// =============================================================================

// ScaleAction represents a single scale-up or scale-down action.
type ScaleAction struct {
	ActionType string   `json:"type"`
	AgentType  string   `json:"agent_type"`
	Count      int      `json:"count"`
	Agents     []string `json:"agents"`
}

// ScaleInput is the structured input for sessions.scale and --robot-scale.
type ScaleInput struct {
	Session string         `json:"session"`
	Targets map[string]int `json:"targets"` // keys: cc, cod, gmi, agy, ollama, cursor, windsurf, aider, grok, omp, oc
	DryRun  bool           `json:"dry_run,omitempty"`
	Force   bool           `json:"force,omitempty"`
}

// ScaleOutput is the canonical robot envelope for --robot-scale and sessions.scale.
type ScaleOutput struct {
	RobotResponse
	SchemaID      string         `json:"schema_id"`
	SchemaVersion string         `json:"schema_version"`
	Session       string         `json:"session"`
	Before        map[string]int `json:"before"`
	After         map[string]int `json:"after"`
	Actions       []ScaleAction  `json:"actions"`
	DryRun        bool           `json:"dry_run,omitempty"`
	Errors        []string       `json:"errors,omitempty"`
	AgentHints    *AgentHints    `json:"_agent_hints,omitempty"`
}

// PrintScaleOutput writes the terminal JSON/TOON response for --robot-scale.
func PrintScaleOutput(out *ScaleOutput) error {
	if out == nil {
		return errors.New("robot scale output is nil")
	}
	return encodeTerminalRobotOutput(out, out.RobotResponse, "robot scale failed")
}

// =============================================================================
// Rebalance Robot & Kernel Wire Types
// =============================================================================

// RebalanceInput is the structured input for sessions.rebalance and --robot-rebalance.
type RebalanceInput struct {
	Session              string  `json:"session"`
	DryRun               bool    `json:"dry_run,omitempty"`
	Apply                bool    `json:"apply,omitempty"`
	Filter               string  `json:"filter,omitempty"`
	Threshold            float64 `json:"threshold,omitempty"`
	CheckGeometry        bool    `json:"check_geometry,omitempty"`
	GeometryTolerance    int     `json:"geometry_tolerance,omitempty"`
	GeometryToleranceSet bool    `json:"geometry_tolerance_set,omitempty"`
}

// RebalanceTransferItem represents one planned or applied bead transfer in robot output.
type RebalanceTransferItem struct {
	BeadID     string `json:"bead_id"`
	BeadTitle  string `json:"bead_title"`
	FromPane   int    `json:"from_pane"`
	FromTarget string `json:"from_target"`
	FromPaneID string `json:"from_pane_id"`
	FromAgent  string `json:"from_agent"`
	ToPane     int    `json:"to_pane"`
	ToTarget   string `json:"to_target"`
	ToPaneID   string `json:"to_pane_id"`
	ToAgent    string `json:"to_agent"`
	Reason     string `json:"reason"`
}

// RebalanceWorkload represents workload for a single physical pane/agent.
type RebalanceWorkload struct {
	Pane       int      `json:"pane"`
	PaneTarget string   `json:"pane_target"`
	PaneID     string   `json:"pane_id"`
	AgentType  string   `json:"agent_type"`
	AgentName  string   `json:"agent_name,omitempty"`
	TaskCount  int      `json:"task_count"`
	TaskIDs    []string `json:"task_ids,omitempty"`
	IsHealthy  bool     `json:"is_healthy"`
	IsIdle     bool     `json:"is_idle"`
	Status     string   `json:"status,omitempty"`
}

// RebalanceGeometryReport captures pane layout uniformity and optional tiling rebalance.
type RebalanceGeometryReport struct {
	Result     *swarm.GeometryResult `json:"result,omitempty"`
	Average    *swarm.PaneGeometry   `json:"average,omitempty"`
	Rebalanced bool                  `json:"rebalanced,omitempty"`
}

// RebalanceOutput is the canonical robot envelope for --robot-rebalance and sessions.rebalance.
type RebalanceOutput struct {
	RobotResponse
	SchemaID       string                   `json:"schema_id"`
	SchemaVersion  string                   `json:"schema_version"`
	Session        string                   `json:"session"`
	ImbalanceScore float64                  `json:"imbalance_score"`
	Recommendation string                   `json:"recommendation"`
	Transfers      []RebalanceTransferItem  `json:"transfers"`
	Workloads      []RebalanceWorkload      `json:"workloads"`
	Before         map[string]int           `json:"before"`
	After          map[string]int           `json:"after"`
	Geometry       *RebalanceGeometryReport `json:"geometry,omitempty"`
	Applied        bool                     `json:"applied,omitempty"`
	DryRun         bool                     `json:"dry_run,omitempty"`
	AgentHints     *AgentHints              `json:"_agent_hints,omitempty"`
}

// PrintRebalanceOutput writes the terminal JSON/TOON response for --robot-rebalance.
func PrintRebalanceOutput(out *RebalanceOutput) error {
	if out == nil {
		return errors.New("robot rebalance output is nil")
	}
	return encodeTerminalRobotOutput(out, out.RobotResponse, "robot rebalance failed")
}

// =============================================================================
// Coordinator Robot & Kernel Wire Types
// =============================================================================

// CoordinatorInput is the structured input for sessions.coordinator* and --robot-coordinator.
type CoordinatorInput struct {
	Session    string `json:"session"`
	Action     string `json:"action,omitempty"` // "status" (default), "digest", "conflicts"
	SendDigest bool   `json:"send_digest,omitempty"`
}

// CoordinatorRuntimeView reports where, if anywhere, the session's coordinator is running.
type CoordinatorRuntimeView struct {
	Host              string     `json:"host"`
	State             string     `json:"state"`
	Summary           string     `json:"summary"`
	PID               int        `json:"pid,omitempty"`
	Mode              string     `json:"mode,omitempty"`
	Features          []string   `json:"features"`
	ConfigPath        string     `json:"config_path,omitempty"`
	Healthy           bool       `json:"healthy"`
	StateSince        *time.Time `json:"state_since,omitempty"`
	HeartbeatAt       *time.Time `json:"heartbeat_at,omitempty"`
	LastMaintenanceAt *time.Time `json:"last_maintenance_at,omitempty"`
	MaintenanceError  string     `json:"maintenance_error,omitempty"`
	Error             string     `json:"error,omitempty"`
}

// CoordinatorConfigView mirrors the active runtime [coordinator] settings.
type CoordinatorConfigView struct {
	AutoAssign        bool    `json:"auto_assign"`
	AssignOnlyIdle    bool    `json:"assign_only_idle"`
	SendDigests       bool    `json:"send_digests"`
	ConflictNotify    bool    `json:"conflict_notify"`
	ConflictNegotiate bool    `json:"conflict_negotiate"`
	PollInterval      string  `json:"poll_interval"`
	DigestInterval    string  `json:"digest_interval"`
	IdleThreshold     float64 `json:"idle_threshold"`
	HumanAgent        string  `json:"human_agent"`
}

// CoordinatorAgentState tracks the current state of an agent pane observed by the coordinator.
type CoordinatorAgentState struct {
	PaneID                string                      `json:"pane_id"`
	PaneIndex             int                         `json:"pane_index"`
	AgentType             string                      `json:"agent_type"`
	AgentMailName         string                      `json:"agent_mail_name,omitempty"`
	Status                AgentState                  `json:"status"`
	LastKnownStatus       AgentState                  `json:"last_known_status,omitempty"`
	ContextUsage          float64                     `json:"context_usage"`
	LastActivity          time.Time                   `json:"last_activity"`
	ObservedAt            time.Time                   `json:"observed_at"`
	LastKnownObservedAt   time.Time                   `json:"last_known_observed_at,omitempty"`
	ObservationFreshness  status.ObservationFreshness `json:"observation_freshness"`
	ObservationConfidence float64                     `json:"observation_confidence"`
	ObservationError      string                      `json:"observation_error,omitempty"`
	SafeToDispatch        bool                        `json:"safe_to_dispatch"`
	CurrentTask           string                      `json:"current_task,omitempty"`
	Reservations          []string                    `json:"reservations,omitempty"`
	Healthy               bool                        `json:"healthy"`
	Profile               *persona.Persona            `json:"profile,omitempty"`
	Assignments           int                         `json:"assignments"`
	LastAssignedAt        time.Time                   `json:"last_assigned_at,omitempty"`
}

// CoordinatorAgentDigestStatus summarizes a single agent's status in a coordinator digest.
type CoordinatorAgentDigestStatus struct {
	PaneID       string  `json:"pane_id"`
	PaneIndex    int     `json:"pane_index"`
	AgentType    string  `json:"agent_type"`
	Status       string  `json:"status"`
	ContextUsage float64 `json:"context_usage"`
	IdleFor      string  `json:"idle_for,omitempty"`
	Task         string  `json:"task,omitempty"`
}

// CoordinatorWorkDigestSummary summarizes work queue status in a coordinator digest.
type CoordinatorWorkDigestSummary struct {
	PendingTasks    int      `json:"pending_tasks"`
	InProgressTasks int      `json:"in_progress_tasks"`
	CompletedToday  int      `json:"completed_today"`
	BlockedTasks    int      `json:"blocked_tasks"`
	TopReady        []string `json:"top_ready,omitempty"`
	Status          string   `json:"status"`
}

// CoordinatorDeadlockCycle represents one reservation wait-for cycle in canonical order.
type CoordinatorDeadlockCycle struct {
	Participants []string  `json:"participants"`
	Resources    []string  `json:"resources,omitempty"`
	Reasons      []string  `json:"reasons,omitempty"`
	OldestSince  time.Time `json:"oldest_since"`
	Suggestion   string    `json:"suggestion,omitempty"`
}

// CoordinatorDigestSummary contains a coordinator digest summary of session activity.
type CoordinatorDigestSummary struct {
	Session       string                         `json:"session"`
	GeneratedAt   time.Time                      `json:"generated_at"`
	AgentCount    int                            `json:"agent_count"`
	ActiveCount   int                            `json:"active_count"`
	IdleCount     int                            `json:"idle_count"`
	ErrorCount    int                            `json:"error_count"`
	AgentStatuses []CoordinatorAgentDigestStatus `json:"agent_statuses"`
	Alerts        []string                       `json:"alerts,omitempty"`
	WorkSummary   CoordinatorWorkDigestSummary   `json:"work_summary"`
	Deadlocks     []CoordinatorDeadlockCycle     `json:"deadlocks,omitempty"`
}

// CoordinatorConflictHolder represents an agent holding a conflicting reservation.
type CoordinatorConflictHolder struct {
	AgentName  string    `json:"agent_name"`
	PaneID     string    `json:"pane_id,omitempty"`
	ReservedAt time.Time `json:"reserved_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Reason     string    `json:"reason,omitempty"`
	Priority   int       `json:"priority"`
}

// CoordinatorConflict represents a file reservation conflict between agents.
type CoordinatorConflict struct {
	ID         string                      `json:"id"`
	FilePath   string                      `json:"file_path"`
	Pattern    string                      `json:"pattern"`
	Holders    []CoordinatorConflictHolder `json:"holders"`
	DetectedAt time.Time                   `json:"detected_at"`
	ResolvedAt *time.Time                  `json:"resolved_at,omitempty"`
	Resolution string                      `json:"resolution,omitempty"`
}

// CoordinatorOutput is the canonical robot envelope for --robot-coordinator and sessions.coordinator*.
type CoordinatorOutput struct {
	RobotResponse
	SchemaID      string                            `json:"schema_id"`
	SchemaVersion string                            `json:"schema_version"`
	Session       string                            `json:"session"`
	Action        string                            `json:"action"`
	AgentCount    int                               `json:"agent_count,omitempty"`
	IdleCount     int                               `json:"idle_count,omitempty"`
	Agents        map[string]*CoordinatorAgentState `json:"agents,omitempty"`
	Runtime       *CoordinatorRuntimeView           `json:"runtime,omitempty"`
	Config        *CoordinatorConfigView            `json:"config,omitempty"`
	Digest        *CoordinatorDigestSummary         `json:"digest,omitempty"`
	DigestSent    bool                              `json:"digest_sent,omitempty"`
	Conflicts     []CoordinatorConflict             `json:"conflicts,omitempty"`
	ConflictCount int                               `json:"conflict_count,omitempty"`
	AgentHints    *AgentHints                       `json:"_agent_hints,omitempty"`
}

// PrintCoordinatorOutput writes the terminal JSON/TOON response for --robot-coordinator.
func PrintCoordinatorOutput(out *CoordinatorOutput) error {
	if out == nil {
		return errors.New("robot coordinator output is nil")
	}
	return encodeTerminalRobotOutput(out, out.RobotResponse, "robot coordinator failed")
}

func scaleCapabilityCommand() RobotCommandInfo {
	return RobotCommandInfo{
		Name:        "scale",
		Flag:        "--robot-scale",
		Category:    "spawn",
		Summary:     "Scale session agent counts per harness.",
		Description: "Scale agent counts per harness up or down across all 12 built-in harnesses without interactive prompts.",
		Parameters: []RobotParameter{
			{Name: "session", Flag: "--robot-scale", Type: "string", Required: true, Description: "Session name to scale"},
			{Name: "scale-cc", Flag: "--scale-cc", Type: "int", Required: false, Description: "Target Claude (cc) agent count"},
			{Name: "scale-cod", Flag: "--scale-cod", Type: "int", Required: false, Description: "Target Codex (cod) agent count"},
			{Name: "scale-gmi", Flag: "--scale-gmi", Type: "int", Required: false, Description: "Target Gemini (gmi) agent count"},
			{Name: "scale-agy", Flag: "--scale-agy", Type: "int", Required: false, Description: "Target Antigravity (agy) agent count"},
			{Name: "scale-ollama", Flag: "--scale-ollama", Type: "int", Required: false, Description: "Target Ollama local agent count"},
			{Name: "scale-cursor", Flag: "--scale-cursor", Type: "int", Required: false, Description: "Target Cursor agent count"},
			{Name: "scale-windsurf", Flag: "--scale-windsurf", Type: "int", Required: false, Description: "Target Windsurf agent count"},
			{Name: "scale-aider", Flag: "--scale-aider", Type: "int", Required: false, Description: "Target Aider agent count"},
			{Name: "scale-grok", Flag: "--scale-grok", Type: "int", Required: false, Description: "Target Grok Build agent count"},
			{Name: "scale-omp", Flag: "--scale-omp", Type: "int", Required: false, Description: "Target Oh My Pi (omp) agent count"},
			{Name: "scale-pi", Flag: "--scale-pi", Type: "int", Required: false, Description: "Target Pi (pi) agent count"},
			{Name: "scale-oc", Flag: "--scale-oc", Type: "int", Required: false, Description: "Target OpenCode (oc) agent count"},
			{Name: "dry-run", Flag: "--dry-run", Type: "bool", Required: false, Default: "false", Description: "Preview scale plan without adding or killing panes"},
			{Name: "force", Flag: "--force", Type: "bool", Required: false, Default: "false", Description: "Force scale-down without safety confirmation"},
		},
		Examples: []string{
			"ntm --robot-scale=myproject --scale-cc=4 --scale-cod=2",
			"ntm --robot-scale=myproject --scale-ollama=1 --scale-aider=1 --dry-run",
			"ntm --robot-scale=myproject --scale-cod=0 --force",
		},
	}
}

func rebalanceCapabilityCommand() RobotCommandInfo {
	return RobotCommandInfo{
		Name:        "rebalance",
		Flag:        "--robot-rebalance",
		Category:    "control",
		Summary:     "Rebalance session workload and geometry.",
		Description: "Analyze bead assignment imbalance across session panes, optionally check tile geometry, and apply suggested transfers.",
		Parameters: []RobotParameter{
			{Name: "session", Flag: "--robot-rebalance", Type: "string", Required: true, Description: "Session name to analyze or rebalance"},
			{Name: "rebalance-apply", Flag: "--rebalance-apply", Type: "bool", Required: false, Default: "false", Description: "Apply suggested bead transfers non-interactively"},
			{Name: "rebalance-threshold", Flag: "--rebalance-threshold", Type: "float", Required: false, Default: "0", Description: "Minimum imbalance score threshold before suggesting transfers"},
			{Name: "rebalance-geometry", Flag: "--rebalance-geometry", Type: "bool", Required: false, Default: "false", Description: "Verify pane geometry uniformity and retile when --rebalance-apply is set"},
			{Name: "rebalance-geometry-tolerance", Flag: "--rebalance-geometry-tolerance", Type: "int", Required: false, Default: "2", Description: "Maximum allowed width/height delta in cells for --rebalance-geometry"},
			{Name: "type", Flag: "--type", Type: "string", Required: false, Description: "Filter rebalance candidates by agent type"},
			{Name: "dry-run", Flag: "--dry-run", Type: "bool", Required: false, Default: "false", Description: "Preview transfers without mutating assignments"},
		},
		Examples: []string{
			"ntm --robot-rebalance=myproject --dry-run",
			"ntm --robot-rebalance=myproject --rebalance-apply --rebalance-threshold=0.5",
			"ntm --robot-rebalance=myproject --rebalance-geometry --rebalance-geometry-tolerance=2",
		},
	}
}

func coordinatorCapabilityCommand() RobotCommandInfo {
	return RobotCommandInfo{
		Name:        "coordinator",
		Flag:        "--robot-coordinator",
		Category:    "control",
		Summary:     "Inspect session coordinator and digests.",
		Description: "Inspect session coordinator agent states, generate or send coordination digests, or detect active file reservation conflicts.",
		Parameters: []RobotParameter{
			{Name: "session", Flag: "--robot-coordinator", Type: "string", Required: true, Description: "Session name"},
			{Name: "coordinator-action", Flag: "--coordinator-action", Type: "string", Required: false, Default: "status", Description: "Coordinator action: status, digest, conflicts"},
			{Name: "coordinator-send-digest", Flag: "--coordinator-send-digest", Type: "bool", Required: false, Default: "false", Description: "Send generated coordinator digest via Agent Mail when --coordinator-action=digest"},
		},
		Examples: []string{
			"ntm --robot-coordinator=myproject",
			"ntm --robot-coordinator=myproject --coordinator-action=digest",
			"ntm --robot-coordinator=myproject --coordinator-action=conflicts",
		},
	}
}
