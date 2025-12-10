// Package checkpoint provides checkpoint/restore functionality for NTM sessions.
// Checkpoints capture the complete state of a session including git state,
// pane scrollback, and session layout for later restoration.
package checkpoint

import (
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Checkpoint represents a saved session state.
type Checkpoint struct {
	// ID is the unique identifier (timestamp-based)
	ID string `json:"id"`
	// Name is the user-provided checkpoint name
	Name string `json:"name"`
	// Description is an optional user description
	Description string `json:"description,omitempty"`
	// SessionName is the tmux session this checkpoint belongs to
	SessionName string `json:"session_name"`
	// WorkingDir is the working directory at checkpoint time
	WorkingDir string `json:"working_dir"`
	// CreatedAt is when the checkpoint was created
	CreatedAt time.Time `json:"created_at"`
	// Session contains the captured session state
	Session SessionState `json:"session"`
	// Git contains the captured git state
	Git GitState `json:"git,omitempty"`
	// PaneCount is the number of panes captured
	PaneCount int `json:"pane_count"`
}

// SessionState captures the tmux session layout and agents.
type SessionState struct {
	// Panes contains info about each pane in the session
	Panes []PaneState `json:"panes"`
	// Layout is the tmux layout string for restoration
	Layout string `json:"layout,omitempty"`
	// ActivePaneIndex is the currently selected pane
	ActivePaneIndex int `json:"active_pane_index"`
}

// PaneState captures the state of a single pane.
type PaneState struct {
	// Index is the pane index in the session
	Index int `json:"index"`
	// ID is the tmux pane ID (e.g., "%0")
	ID string `json:"id"`
	// Title is the pane title
	Title string `json:"title"`
	// AgentType is the detected agent type ("cc", "cod", "gmi", "user")
	AgentType string `json:"agent_type"`
	// Command is the running command
	Command string `json:"command,omitempty"`
	// Width is the pane width in columns
	Width int `json:"width"`
	// Height is the pane height in rows
	Height int `json:"height"`
	// ScrollbackFile is the relative path to scrollback capture
	ScrollbackFile string `json:"scrollback_file,omitempty"`
	// ScrollbackLines is the number of lines captured
	ScrollbackLines int `json:"scrollback_lines"`
}

// GitState captures the git repository state at checkpoint time.
type GitState struct {
	// Branch is the current branch name
	Branch string `json:"branch"`
	// Commit is the current HEAD commit SHA
	Commit string `json:"commit"`
	// IsDirty indicates uncommitted changes exist
	IsDirty bool `json:"is_dirty"`
	// PatchFile is the relative path to the git diff patch
	PatchFile string `json:"patch_file,omitempty"`
	// StagedCount is the number of staged files
	StagedCount int `json:"staged_count"`
	// UnstagedCount is the number of modified but unstaged files
	UnstagedCount int `json:"unstaged_count"`
	// UntrackedCount is the number of untracked files
	UntrackedCount int `json:"untracked_count"`
}

// Summary returns a brief summary of the checkpoint.
func (c *Checkpoint) Summary() string {
	return c.Name + " (" + c.ID + ")"
}

// Age returns how long ago the checkpoint was created.
func (c *Checkpoint) Age() time.Duration {
	return time.Since(c.CreatedAt)
}

// HasGitPatch returns true if a git patch file exists.
func (c *Checkpoint) HasGitPatch() bool {
	return c.Git.PatchFile != ""
}

// FromTmuxPane converts a tmux.Pane to PaneState.
func FromTmuxPane(p tmux.Pane) PaneState {
	return PaneState{
		Index:     p.Index,
		ID:        p.ID,
		Title:     p.Title,
		AgentType: string(p.Type),
		Command:   p.Command,
		Width:     p.Width,
		Height:    p.Height,
	}
}

// CheckpointOption configures checkpoint creation.
type CheckpointOption func(*checkpointOptions)

type checkpointOptions struct {
	description     string
	captureGit      bool
	scrollbackLines int
}

// WithDescription sets the checkpoint description.
func WithDescription(desc string) CheckpointOption {
	return func(o *checkpointOptions) {
		o.description = desc
	}
}

// WithGitCapture enables/disables git state capture.
func WithGitCapture(capture bool) CheckpointOption {
	return func(o *checkpointOptions) {
		o.captureGit = capture
	}
}

// WithScrollbackLines sets the number of scrollback lines to capture.
func WithScrollbackLines(lines int) CheckpointOption {
	return func(o *checkpointOptions) {
		o.scrollbackLines = lines
	}
}

func defaultOptions() checkpointOptions {
	return checkpointOptions{
		captureGit:      true,
		scrollbackLines: 1000,
	}
}
