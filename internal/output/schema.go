package output

import "time"

// ErrorResponse is the standard JSON error format
type ErrorResponse struct {
	Error   string `json:"error"`
	Code    string `json:"code,omitempty"`
	Details string `json:"details,omitempty"`
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
	Index   int    `json:"index"`
	Title   string `json:"title"`
	Type    string `json:"type"` // claude, codex, gemini, user
	Active  bool   `json:"active,omitempty"`
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
	Command string `json:"command,omitempty"`
}

// AgentCountsResponse is the standard format for agent counts
type AgentCountsResponse struct {
	Claude  int `json:"claude"`
	Codex   int `json:"codex"`
	Gemini  int `json:"gemini"`
	User    int `json:"user,omitempty"`
	Total   int `json:"total"`
}

// SpawnResponse is the output format for spawn/create commands
type SpawnResponse struct {
	TimestampedResponse
	Session          string              `json:"session"`
	Created          bool                `json:"created"`
	WorkingDirectory string              `json:"working_directory,omitempty"`
	Panes            []PaneResponse      `json:"panes"`
	AgentCounts      AgentCountsResponse `json:"agent_counts"`
}

// SendResponse is the output format for send command
type SendResponse struct {
	TimestampedResponse
	Session       string   `json:"session"`
	PromptPreview string   `json:"prompt_preview"` // First N chars
	Targets       []int    `json:"targets"`        // Pane indices
	Delivered     int      `json:"delivered"`
	Failed        int      `json:"failed"`
	FailedPanes   []int    `json:"failed_panes,omitempty"`
}

// ListResponse is the output format for list command
type ListResponse struct {
	TimestampedResponse
	Sessions []SessionListItem `json:"sessions"`
	Count    int               `json:"count"`
}

// SessionListItem is a single session in list output
type SessionListItem struct {
	Name             string              `json:"name"`
	Windows          int                 `json:"windows"`
	PaneCount        int                 `json:"pane_count"`
	Attached         bool                `json:"attached"`
	WorkingDirectory string              `json:"working_directory,omitempty"`
	AgentCounts      *AgentCountsResponse `json:"agents,omitempty"`
}

// StatusResponse is the output format for status command
type StatusResponse struct {
	TimestampedResponse
	Session          string              `json:"session"`
	Exists           bool                `json:"exists"`
	Attached         bool                `json:"attached"`
	WorkingDirectory string              `json:"working_directory"`
	Panes            []PaneResponse      `json:"panes"`
	AgentCounts      AgentCountsResponse `json:"agent_counts"`
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
