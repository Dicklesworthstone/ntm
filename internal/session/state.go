// Package session provides session state capture and restoration.
package session

import (
	"time"
)

// StateVersion is the schema version for migrations
const StateVersion = 1

// SessionState represents a complete session snapshot for restoration.
type SessionState struct {
	// Identity
	Name    string    `json:"name"`
	SavedAt time.Time `json:"saved_at"`

	// Context
	WorkDir   string `json:"cwd"`
	GitBranch string `json:"git_branch,omitempty"`
	GitRemote string `json:"git_remote,omitempty"`
	GitCommit string `json:"git_commit,omitempty"`

	// Agent Configuration (counts by type)
	Agents AgentConfig `json:"agents"`

	// Pane Details (for exact recreation)
	Panes []PaneState `json:"panes"`

	// Layout
	Layout string `json:"layout"` // "tiled", "even-horizontal", etc.

	// Metadata
	CreatedAt time.Time `json:"created_at,omitempty"` // Original session creation
	Version   int       `json:"version"`              // Schema version for migrations

	// Optional: Configuration snapshot
	Config *ConfigSnapshot `json:"config,omitempty"`
}

// AgentConfig represents agent counts by type.
type AgentConfig struct {
	Claude   int `json:"cc"`
	Codex    int `json:"cod"`
	Gemini   int `json:"gmi"`
	Cursor   int `json:"cursor"`
	Windsurf int `json:"windsurf"`
	Aider    int `json:"aider"`
	Opencode int `json:"oc"`
	Ollama   int `json:"ollama"`
	User     int `json:"user"`
}

// Total returns the total number of agents.
func (a AgentConfig) Total() int {
	return a.Claude + a.Codex + a.Gemini + a.Cursor + a.Windsurf + a.Aider + a.Opencode + a.Ollama + a.User
}

// PaneState represents the state of a single pane.
type PaneState struct {
	Title       string `json:"title"`             // e.g., "myproject__cc_1"
	Index       int    `json:"index"`             // Pane index
	WindowIndex int    `json:"window_index"`      // Window index
	AgentType   string `json:"agent_type"`        // "cc", "cod", "gmi", "user"
	Model       string `json:"model,omitempty"`   // Model variant if any
	Command     string `json:"command,omitempty"` // The agent launch command
	Active      bool   `json:"active"`            // Was this the active pane?
	Width       int    `json:"width,omitempty"`   // Pane width
	Height      int    `json:"height,omitempty"`  // Pane height
	PaneID      string `json:"pane_id,omitempty"` // Original pane ID
}

// ConfigSnapshot captures relevant config at save time.
type ConfigSnapshot struct {
	ClaudeCmd   string `json:"claude_cmd,omitempty"`
	CodexCmd    string `json:"codex_cmd,omitempty"`
	GeminiCmd   string `json:"gemini_cmd,omitempty"`
	CursorCmd   string `json:"cursor_cmd,omitempty"`
	WindsurfCmd string `json:"windsurf_cmd,omitempty"`
	AiderCmd    string `json:"aider_cmd,omitempty"`
	OpencodeCmd string `json:"opencode_cmd,omitempty"`
	OllamaCmd   string `json:"ollama_cmd,omitempty"`
}

// AgentCommands defines the launch commands for agents.
type AgentCommands struct {
	Claude   string
	Codex    string
	Gemini   string
	Cursor   string
	Windsurf string
	Aider    string
	Opencode string
	Ollama   string
}

// SaveOptions configures how a session is saved.
type SaveOptions struct {
	Name        string // Custom name (defaults to session name)
	Overwrite   bool   // Overwrite existing save
	IncludeGit  bool   // Include git context
	Description string // Optional description
}

// RestoreOptions configures how a session is restored.
type RestoreOptions struct {
	Name         string // Name to restore as (defaults to saved name)
	SkipGitCheck bool   // Skip git branch verification
	Force        bool   // Force restore even if session exists
}
