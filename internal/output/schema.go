package output

import "time"

// ErrorResponse is the standard JSON error format
type ErrorResponse struct {
	Error   string `json:"error"`
	Code    string `json:"code,omitempty"`
	Details string `json:"details,omitempty"`
	Hint    string `json:"hint,omitempty"` // Remediation hint (suggested fix command)
}

// NewError creates a new error response
func NewError(msg string) ErrorResponse {
	return ErrorResponse{Error: msg}
}

// NewErrorWithCode creates a new error response with a code
func NewErrorWithCode(code, msg string) ErrorResponse {
	return ErrorResponse{Error: msg, Code: code}
}

// NewErrorWithDetails creates a new error response with details
func NewErrorWithDetails(msg, details string) ErrorResponse {
	return ErrorResponse{Error: msg, Details: details}
}

// NewErrorWithHint creates a new error response with a remediation hint
func NewErrorWithHint(msg, hint string) ErrorResponse {
	return ErrorResponse{Error: msg, Hint: hint}
}

// NewErrorFull creates a new error response with all fields
func NewErrorFull(code, msg, details, hint string) ErrorResponse {
	return ErrorResponse{Error: msg, Code: code, Details: details, Hint: hint}
}

// SuccessResponse is a simple success indicator
type SuccessResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// NewSuccess creates a success response
func NewSuccess(msg string) SuccessResponse {
	return SuccessResponse{Success: true, Message: msg}
}

// TimestampedResponse adds a timestamp to any response
type TimestampedResponse struct {
	GeneratedAt time.Time `json:"generated_at"`
}

// NewTimestamped creates a timestamped response base
func NewTimestamped() TimestampedResponse {
	return TimestampedResponse{GeneratedAt: Timestamp()}
}

// SessionResponse is the standard format for session-related output
type SessionResponse struct {
	Session  string `json:"session"`
	Exists   bool   `json:"exists"`
	Attached bool   `json:"attached,omitempty"`
}

// PaneResponse is the standard format for pane-related output
type PaneResponse struct {
	Index          int     `json:"index"`
	Title          string  `json:"title"`
	Type           string  `json:"type"`              // claude, codex, gemini, user
	Variant        string  `json:"variant,omitempty"` // model alias or persona name
	Active         bool    `json:"active,omitempty"`
	Width          int     `json:"width,omitempty"`
	Height         int     `json:"height,omitempty"`
	Command        string  `json:"command,omitempty"`
	Status         string  `json:"status,omitempty"`          // idle, working, error
	PromptDelayMs  int64   `json:"prompt_delay_ms,omitempty"` // Stagger delay in milliseconds
	ContextTokens  int     `json:"context_tokens,omitempty"`
	ContextLimit   int     `json:"context_limit,omitempty"`
	ContextPercent float64 `json:"context_percent,omitempty"`
	ContextModel   string  `json:"context_model,omitempty"`
}

// AgentCountsResponse is the standard format for agent counts
type AgentCountsResponse struct {
	Claude   int `json:"claude"`
	Codex    int `json:"codex"`
	Gemini   int `json:"gemini"`
	Ollama   int `json:"ollama"`
	Cursor   int `json:"cursor"`
	Windsurf int `json:"windsurf"`
	Aider    int `json:"aider"`
	User     int `json:"user,omitempty"`
	Other    int `json:"other,omitempty"`
	Total    int `json:"total"`
}

// StaggerConfig represents stagger settings in spawn response
type StaggerConfig struct {
	Enabled    bool  `json:"enabled"`
	IntervalMs int64 `json:"interval_ms,omitempty"`
}

// AgentMailSpawnStatus represents Agent Mail registration status for a spawn operation
type AgentMailSpawnStatus struct {
	Available         bool              `json:"available"`
	ProjectRegistered bool              `json:"project_registered"`
	AgentsRegistered  int               `json:"agents_registered"`
	AgentsFailed      int               `json:"agents_failed"`
	AgentMap          map[string]string `json:"agent_map,omitempty"` // pane index -> agent name
}

// SpawnResponse is the output format for spawn command (with agents)
type SpawnResponse struct {
	TimestampedResponse
	Session          string                `json:"session"`
	Created          bool                  `json:"created"`
	WorkingDirectory string                `json:"working_directory,omitempty"`
	Panes            []PaneResponse        `json:"panes"`
	AgentCounts      AgentCountsResponse   `json:"agent_counts"`
	Stagger          *StaggerConfig        `json:"stagger,omitempty"`
	AgentMail        *AgentMailSpawnStatus `json:"agent_mail,omitempty"`
}

// CreateResponse is the output format for create command (basic session)
type CreateResponse struct {
	TimestampedResponse
	Session          string         `json:"session"`
	Created          bool           `json:"created"`
	AlreadyExisted   bool           `json:"already_existed,omitempty"`
	WorkingDirectory string         `json:"working_directory,omitempty"`
	PaneCount        int            `json:"pane_count"`
	Panes            []PaneResponse `json:"panes,omitempty"`
}

// AddResponse is the output format for add command (adding agents to session)
type AddResponse struct {
	TimestampedResponse
	Session       string         `json:"session"`
	AddedClaude   int            `json:"added_claude"`
	AddedCodex    int            `json:"added_codex"`
	AddedGemini   int            `json:"added_gemini"`
	AddedOllama   int            `json:"added_ollama"`
	AddedCursor   int            `json:"added_cursor"`
	AddedWindsurf int            `json:"added_windsurf"`
	AddedAider    int            `json:"added_aider"`
	TotalAdded    int            `json:"total_added"`
	NewPanes      []PaneResponse `json:"new_panes,omitempty"`
}

// SendResponse is the output format for send command
type SendResponse struct {
	TimestampedResponse
	Session       string `json:"session"`
	PromptPreview string `json:"prompt_preview"` // First N chars
	Targets       []int  `json:"targets"`        // Pane indices
	Delivered     int    `json:"delivered"`
	Failed        int    `json:"failed"`
	FailedPanes   []int  `json:"failed_panes,omitempty"`
}

// ListResponse is the output format for list command
type ListResponse struct {
	TimestampedResponse
	Sessions []SessionListItem `json:"sessions"`
	Count    int               `json:"count"`
}

// SessionListItem is a single session in list output
type SessionListItem struct {
	Name             string               `json:"name"`
	BaseProject      string               `json:"base_project"`
	Label            string               `json:"label,omitempty"`
	Windows          int                  `json:"windows"`
	PaneCount        int                  `json:"pane_count"`
	Attached         bool                 `json:"attached"`
	WorkingDirectory string               `json:"working_directory,omitempty"`
	AgentCounts      *AgentCountsResponse `json:"agents,omitempty"`
}

// StatusResponse is the output format for status command
type StatusResponse struct {
	TimestampedResponse
	Session           string               `json:"session"`
	Exists            bool                 `json:"exists"`
	Attached          bool                 `json:"attached"`
	WorkingDirectory  string               `json:"working_directory"`
	Panes             []PaneResponse       `json:"panes"`
	AgentCounts       AgentCountsResponse  `json:"agent_counts"`
	AgentMail         *AgentMailStatus     `json:"agent_mail,omitempty"`
	Handoff           *HandoffStatus       `json:"handoff,omitempty"`
	Assignments       []AssignmentResponse `json:"assignments,omitempty"`
	AssignmentStats   *AssignmentStats     `json:"assignment_stats,omitempty"`
	AssignmentFilters *AssignmentFilters   `json:"assignment_filters,omitempty"`
	AssignmentSummary *AssignmentSummary   `json:"assignment_summary,omitempty"`
}

// HandoffStatus represents the latest handoff for a session.
type HandoffStatus struct {
	Session    string `json:"session,omitempty"`
	Goal       string `json:"goal,omitempty"`
	Now        string `json:"now,omitempty"`
	Path       string `json:"path,omitempty"`
	AgeSeconds int64  `json:"age_seconds,omitempty"`
	Status     string `json:"status,omitempty"`
}

// AgentMailStatus represents Agent Mail integration status for a session
type AgentMailStatus struct {
	Available    bool                  `json:"available"`
	Connected    bool                  `json:"connected"`
	ServerURL    string                `json:"server_url,omitempty"`
	UnreadCount  int                   `json:"unread_count,omitempty"`
	UrgentCount  int                   `json:"urgent_count,omitempty"`
	ActiveLocks  int                   `json:"active_locks,omitempty"`
	Reservations []FileReservationInfo `json:"reservations,omitempty"`
}

// FileReservationInfo represents a file reservation summary
type FileReservationInfo struct {
	PathPattern string `json:"path_pattern"`
	AgentName   string `json:"agent_name"`
	Exclusive   bool   `json:"exclusive"`
	Reason      string `json:"reason,omitempty"`
	ExpiresIn   string `json:"expires_in,omitempty"`
}

// DepsResponse is the output format for deps command
type DepsResponse struct {
	TimestampedResponse
	AllInstalled bool              `json:"all_installed"`
	Dependencies []DependencyCheck `json:"dependencies"`
}

// DependencyCheck represents a single dependency status
type DependencyCheck struct {
	Name      string `json:"name"`
	Required  bool   `json:"required"`
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	Path      string `json:"path,omitempty"`
}

// VersionResponse is the output format for version command
type VersionResponse struct {
	TimestampedResponse
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuiltAt   string `json:"built_at,omitempty"`
	BuiltBy   string `json:"built_by,omitempty"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

// AnalyticsResponse is the output format for analytics command
type AnalyticsResponse struct {
	TimestampedResponse
	Period         string `json:"period"`
	TotalSessions  int    `json:"total_sessions"`
	TotalAgents    int    `json:"total_agents"`
	TotalPrompts   int    `json:"total_prompts"`
	TotalCharsSent int    `json:"total_chars_sent"`
	TotalTokensEst int    `json:"total_tokens_estimated"`
	ErrorCount     int    `json:"error_count"`
}

// AssignmentResponse represents a single bead-to-agent assignment
type AssignmentResponse struct {
	BeadID      string  `json:"bead_id"`
	BeadTitle   string  `json:"bead_title"`
	Pane        int     `json:"pane"`
	AgentType   string  `json:"agent_type"`
	AgentName   string  `json:"agent_name,omitempty"`
	Status      string  `json:"status"`
	AssignedAt  string  `json:"assigned_at"`
	StartedAt   *string `json:"started_at,omitempty"`
	CompletedAt *string `json:"completed_at,omitempty"`
	FailedAt    *string `json:"failed_at,omitempty"`
	FailReason  string  `json:"fail_reason,omitempty"`
}

// AssignmentsResponse is the output format for assignment tracking
type AssignmentsResponse struct {
	TimestampedResponse
	Session     string               `json:"session"`
	Assignments []AssignmentResponse `json:"assignments"`
	Stats       AssignmentStats      `json:"stats"`
}

// AssignmentStats contains summary statistics for assignments
type AssignmentStats struct {
	Total      int `json:"total"`
	Assigned   int `json:"assigned"`
	Working    int `json:"working"`
	Completed  int `json:"completed"`
	Failed     int `json:"failed"`
	Reassigned int `json:"reassigned"`
}

// AssignmentFilters represents active filters on assignment output
type AssignmentFilters struct {
	Status    string `json:"status,omitempty"`
	AgentType string `json:"agent_type,omitempty"`
	Pane      *int   `json:"pane,omitempty"`
}

// AssignmentStatsByAgent contains per-agent-type stats
type AssignmentStatsByAgent struct {
	AgentType string `json:"agent_type"`
	Total     int    `json:"total"`
	Working   int    `json:"working"`
	Completed int    `json:"completed"`
	Failed    int    `json:"failed"`
}

// AssignmentSummary provides comprehensive summary statistics
type AssignmentSummary struct {
	Total          int                      `json:"total"`
	ByStatus       map[string]int           `json:"by_status"`
	ByAgent        []AssignmentStatsByAgent `json:"by_agent"`
	CompletionRate float64                  `json:"completion_rate"`
	AvgDurationSec float64                  `json:"avg_duration_seconds,omitempty"`
}

// InterruptResponse is the output format for interrupt command
type InterruptResponse struct {
	TimestampedResponse
	Session       string `json:"session"`
	Interrupted   int    `json:"interrupted"`
	Skipped       int    `json:"skipped,omitempty"`
	TargetedPanes []int  `json:"targeted_panes,omitempty"`
}

// KillResponse is the output format for kill command
type KillResponse struct {
	TimestampedResponse
	Session string      `json:"session"`
	Killed  bool        `json:"killed"`
	Message string      `json:"message,omitempty"`
	Summary interface{} `json:"summary,omitempty"` // *summary.SessionSummary when --summarize is used
}
