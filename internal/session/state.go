// Package session provides session state capture and restoration.
package session

import (
	"fmt"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/agentsession"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
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

	// Layout is the whole-session fallback layout (the active window's
	// #{window_layout} at save time). Per-window exact geometry lives in
	// Windows; Layout is retained as a fallback for windows without captured
	// per-window geometry and for display (ntm sessions show).
	Layout string `json:"layout"` // "tiled", "even-horizontal", or a serialized geometry string

	// Windows captures per-window metadata for faithful restore: exact
	// geometry, window names, active-window flag, and zoom state. Empty for
	// states saved before this field existed (restore falls back to Layout).
	Windows []WindowState `json:"windows,omitempty"`

	// Metadata
	CreatedAt time.Time `json:"created_at,omitempty"` // Original session creation
	Version   int       `json:"version"`              // Schema version for migrations

	// Optional: Configuration snapshot
	Config *ConfigSnapshot `json:"config,omitempty"`
}

// AgentConfig represents agent counts by type.
type AgentConfig struct {
	Claude      int `json:"cc"`
	Codex       int `json:"cod"`
	Gemini      int `json:"gmi"`
	Antigravity int `json:"agy,omitempty"`
	Grok        int `json:"grok,omitempty"`
	Cursor      int `json:"cursor"`
	Windsurf    int `json:"windsurf"`
	Aider       int `json:"aider"`
	Opencode    int `json:"oc"`
	Omp         int `json:"omp,omitempty"`
	Ollama      int `json:"ollama"`
	User        int `json:"user"`
}

// Total returns the total number of agents.
func (a AgentConfig) Total() int {
	return a.Claude + a.Codex + a.Gemini + a.Antigravity + a.Grok + a.Cursor + a.Windsurf + a.Aider + a.Opencode + a.Omp + a.Ollama + a.User
}

// ErrAutomatedRelaunchNotImplemented is the sentinel returned when a saved
// session contains an agent whose automated lifecycle is not supported.
var ErrAutomatedRelaunchNotImplemented = agent.ErrAutomatedRelaunchNotImplemented

// ValidateAutomatedRelaunch checks the entire saved pane batch before any
// restore/resume caller mutates tmux state.
func ValidateAutomatedRelaunch(state *SessionState) error {
	if state == nil {
		return nil
	}
	for _, pane := range state.Panes {
		if err := agent.AgentType(pane.AgentType).ValidateAutomatedRelaunch(); err != nil {
			return fmt.Errorf("saved pane %d (%s): %w", pane.Index, pane.AgentType, err)
		}
	}
	return nil
}

// PaneState represents the state of a single pane.
type PaneState struct {
	Title       string                `json:"title"`                 // e.g., "myproject__cc_1"
	Index       int                   `json:"index"`                 // Pane index
	WindowIndex int                   `json:"window_index"`          // Window index
	AgentType   string                `json:"agent_type"`            // "cc", "cod", "gmi", "grok", "omp", "user"
	Model       string                `json:"model,omitempty"`       // Model variant if any
	Command     string                `json:"command,omitempty"`     // The agent launch command
	Active      bool                  `json:"active"`                // Was this the active pane?
	Width       int                   `json:"width,omitempty"`       // Pane width
	Height      int                   `json:"height,omitempty"`      // Pane height
	PaneID      string                `json:"pane_id,omitempty"`     // Original pane ID
	WorkDir     string                `json:"cwd,omitempty"`         // Pane-specific project/worktree directory
	LaunchSpec  *tmux.AgentLaunchSpec `json:"launch_spec,omitempty"` // Exact rendered launch and recovery settings

	// Agent CLI session linkage (for resume). Captured best-effort by
	// discovering the most-recent provider session for the pane's cwd+agent.
	// Resume is delegated to casr / native `--resume <id>` (see internal/agentsession),
	// not reimplemented here.
	SessionID            string                        `json:"session_id,omitempty"`       // Provider session id (e.g. Claude UUID)
	SessionProvider      string                        `json:"session_provider,omitempty"` // casr provider name ("claude", "codex", "gemini")
	SessionFile          string                        `json:"-"`                          // Process-private discovery path; never serialized
	SessionSource        agentsession.DiscoverySource  `json:"session_source,omitempty"`
	SessionObservedAt    time.Time                     `json:"session_observed_at,omitempty"`
	SessionSourceUpdated time.Time                     `json:"session_source_updated_at,omitempty"`
	SessionFreshness     agentsession.BindingFreshness `json:"session_freshness,omitempty"`
	SessionConfidence    float64                       `json:"session_confidence,omitempty"`
	SessionFailureCode   string                        `json:"session_failure_code,omitempty"`
}

const minimumSessionBindingConfidence = 0.75

// HasFreshSessionBinding fails closed for stale, unavailable, low-confidence,
// and legacy unqualified session IDs.
func (p PaneState) HasFreshSessionBinding() bool {
	return p.SessionID != "" &&
		p.SessionFreshness == agentsession.BindingFresh &&
		p.SessionConfidence >= minimumSessionBindingConfidence &&
		p.SessionConfidence <= 1
}

// WindowState captures per-window metadata so a restored session reproduces
// not just pane counts but exact split geometry (ntm-r3k0) plus window names,
// the active window, and zoom state (ntm-fphu).
type WindowState struct {
	Index int    `json:"index"`          // tmux window index at save time
	Name  string `json:"name,omitempty"` // window name (rename-window on restore)
	// Layout is the tmux #{window_layout} string for this window. It encodes the
	// exact nested split geometry and per-pane sizes; applying it via
	// select-layout reproduces the geometry precisely when the pane count matches.
	Layout string `json:"layout,omitempty"`
	Active bool   `json:"active,omitempty"` // was this the active window
	Zoomed bool   `json:"zoomed,omitempty"` // was this window zoomed (resize-pane -Z)
}

// ConfigSnapshot captures relevant config at save time.
type ConfigSnapshot struct {
	ClaudeCmd      string `json:"claude_cmd,omitempty"`
	CodexCmd       string `json:"codex_cmd,omitempty"`
	GeminiCmd      string `json:"gemini_cmd,omitempty"`
	AntigravityCmd string `json:"antigravity_cmd,omitempty"`
	CursorCmd      string `json:"cursor_cmd,omitempty"`
	WindsurfCmd    string `json:"windsurf_cmd,omitempty"`
	AiderCmd       string `json:"aider_cmd,omitempty"`
	OpencodeCmd    string `json:"opencode_cmd,omitempty"`
	OmpCmd         string `json:"omp_cmd,omitempty"`
	OllamaCmd      string `json:"ollama_cmd,omitempty"`
}

// AgentCommands defines the launch commands for agents.
type AgentCommands struct {
	Claude      string
	Codex       string
	Gemini      string
	Antigravity string
	Cursor      string
	Windsurf    string
	Aider       string
	Opencode    string
	Omp         string
	Ollama      string
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
