// Package checkpoint provides checkpoint/restore functionality for NTM sessions.
// Checkpoints capture the complete state of a session including git state,
// pane scrollback, and session layout for later restoration.
package checkpoint

import (
	"sort"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Checkpoint represents a saved session state.
type Checkpoint struct {
	// Version is the checkpoint format version (for compatibility)
	Version int `json:"version"`
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

	// Assignments contains bead-to-agent assignment state at checkpoint time (bd-32ck)
	// This field is optional for backward compatibility with older checkpoints.
	Assignments []AssignmentSnapshot `json:"assignments,omitempty"`

	// BVSummary contains BV triage summary at checkpoint time (bd-32ck)
	// This field is optional for backward compatibility with older checkpoints.
	BVSummary *BVSnapshot `json:"bv_summary,omitempty"`
}

// AssignmentSnapshot captures bead assignment state for checkpointing.
// This is a simplified view of assignment.Assignment for checkpoint storage.
type AssignmentSnapshot struct {
	// BeadID is the assigned bead identifier
	BeadID string `json:"bead_id"`
	// BeadTitle is the bead title for display
	BeadTitle string `json:"bead_title"`
	// Pane is the pane index where the agent is working
	Pane int `json:"pane"`
	// AgentType is the agent type (cc, cod, gmi)
	AgentType string `json:"agent_type"`
	// AgentName is the Agent Mail name if registered
	AgentName string `json:"agent_name,omitempty"`
	// Status is the assignment status (assigned, working, completed, failed)
	Status string `json:"status"`
	// AssignedAt is when the bead was assigned
	AssignedAt time.Time `json:"assigned_at"`
}

// BVSnapshot captures BV triage state at checkpoint time.
type BVSnapshot struct {
	// OpenCount is the number of open beads
	OpenCount int `json:"open_count"`
	// ActionableCount is beads ready for work (unblocked)
	ActionableCount int `json:"actionable_count"`
	// BlockedCount is beads blocked by dependencies
	BlockedCount int `json:"blocked_count"`
	// InProgressCount is beads currently being worked
	InProgressCount int `json:"in_progress_count"`
	// TopPicks contains IDs of top recommended beads
	TopPicks []string `json:"top_picks,omitempty"`
	// CapturedAt is when the BV snapshot was taken
	CapturedAt time.Time `json:"captured_at"`
}

// SessionState captures the tmux session layout and agents.
type SessionState struct {
	// Panes contains info about each pane in the session
	Panes []PaneState `json:"panes"`
	// Layout is the legacy single-window tmux layout string.
	// Multi-window checkpoints should use WindowLayouts instead.
	Layout string `json:"layout,omitempty"`
	// WindowLayouts captures per-window tmux layout strings for restoration.
	WindowLayouts []WindowLayoutState `json:"window_layouts,omitempty"`
	// ActivePaneIndex is the currently selected pane
	ActivePaneIndex int `json:"active_pane_index"`
}

// WindowLayoutState captures the layout string for a specific tmux window.
type WindowLayoutState struct {
	// WindowIndex is the tmux window index.
	WindowIndex int `json:"window_index"`
	// Layout is the tmux layout string for this window.
	Layout string `json:"layout"`
}

// ScrollbackArtifactSummary records how a pane scrollback-like artifact was stored.
type ScrollbackArtifactSummary struct {
	// Captured indicates content was successfully captured before persistence.
	Captured bool `json:"captured"`
	// ArtifactPreserved indicates the complete content remains available by file path.
	ArtifactPreserved bool `json:"artifact_preserved"`
	// Compacted indicates the persisted artifact is compressed or otherwise compacted.
	Compacted bool `json:"compacted"`
	// Compression names the compaction format when Compacted is true.
	Compression string `json:"compression,omitempty"`
	// LineCount is the number of logical lines represented by the artifact.
	LineCount int `json:"line_count,omitempty"`
	// RawBytes is the uncompressed content size in bytes.
	RawBytes int `json:"raw_bytes,omitempty"`
	// StoredBytes is the persisted artifact size in bytes.
	StoredBytes int64 `json:"stored_bytes,omitempty"`
	// RequestedLines is the configured capture window when applicable.
	RequestedLines int `json:"requested_lines,omitempty"`
	// MaxSizeMB is the configured artifact size limit when applicable.
	MaxSizeMB int `json:"max_size_mb,omitempty"`
	// Skipped indicates capture or persistence was intentionally skipped.
	Skipped bool `json:"skipped,omitempty"`
	// Degraded indicates the artifact metadata describes incomplete capture.
	Degraded bool `json:"degraded,omitempty"`
	// Reason explains skipped or degraded artifact handling.
	Reason string `json:"reason,omitempty"`
}

// PaneState captures the state of a single pane.
type PaneState struct {
	// Index is the pane index in the session
	Index int `json:"index"`
	// WindowIndex is the window index containing this pane
	WindowIndex int `json:"window_index"`
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
	// Scrollback describes how the scrollback artifact was preserved.
	Scrollback *ScrollbackArtifactSummary `json:"scrollback,omitempty"`
}

// GitState captures the git repository state at checkpoint time.
type GitState struct {
	// Branch is the current branch name
	Branch string `json:"branch"`
	// Commit is the current HEAD commit SHA
	Commit string `json:"commit"`
	// IsDirty indicates uncommitted changes exist
	IsDirty bool `json:"is_dirty"`
	// StatusFile is the relative path to the captured git status text
	StatusFile string `json:"status_file,omitempty"`
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
		Index:       p.Index,
		WindowIndex: p.WindowIndex,
		ID:          p.ID,
		Title:       p.Title,
		AgentType:   string(p.Type),
		Command:     p.Command,
		Width:       p.Width,
		Height:      p.Height,
	}
}

func cloneWindowLayouts(layouts []WindowLayoutState) []WindowLayoutState {
	if len(layouts) == 0 {
		return nil
	}

	cloned := make([]WindowLayoutState, len(layouts))
	copy(cloned, layouts)
	sortWindowLayouts(cloned)
	return cloned
}

func sortWindowLayouts(layouts []WindowLayoutState) {
	sort.Slice(layouts, func(i, j int) bool {
		if layouts[i].WindowIndex != layouts[j].WindowIndex {
			return layouts[i].WindowIndex < layouts[j].WindowIndex
		}
		return layouts[i].Layout < layouts[j].Layout
	})
}

func windowLayoutsEqual(a, b []WindowLayoutState) bool {
	if len(a) != len(b) {
		return false
	}
	left := cloneWindowLayouts(a)
	right := cloneWindowLayouts(b)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func legacyLayoutFromWindowLayouts(layouts []WindowLayoutState) string {
	if len(layouts) != 1 {
		return ""
	}
	return layouts[0].Layout
}

func sessionWindowIndexesFromPanes(panes []PaneState) []int {
	if len(panes) == 0 {
		return nil
	}

	seen := make(map[int]struct{}, len(panes))
	var indexes []int
	for _, pane := range panes {
		if _, ok := seen[pane.WindowIndex]; ok {
			continue
		}
		seen[pane.WindowIndex] = struct{}{}
		indexes = append(indexes, pane.WindowIndex)
	}
	sort.Ints(indexes)
	return indexes
}

// CheckpointOption configures checkpoint creation.
type CheckpointOption func(*checkpointOptions)

type checkpointOptions struct {
	description         string
	captureGit          bool
	scrollbackLines     int
	scrollbackCompress  bool
	scrollbackMaxSizeMB int
	captureAssignments  bool // bd-32ck: capture bead-to-agent assignments
	captureBVSnapshot   bool // bd-32ck: capture BV triage summary
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

// WithScrollbackCompress enables/disables gzip compression for scrollback.
func WithScrollbackCompress(compress bool) CheckpointOption {
	return func(o *checkpointOptions) {
		o.scrollbackCompress = compress
	}
}

// WithScrollbackMaxSizeMB sets the maximum compressed scrollback size in MB.
// Scrollback larger than this will be skipped. 0 = no limit.
func WithScrollbackMaxSizeMB(sizeMB int) CheckpointOption {
	return func(o *checkpointOptions) {
		o.scrollbackMaxSizeMB = sizeMB
	}
}

// WithAssignments enables/disables capturing bead-to-agent assignments (bd-32ck).
func WithAssignments(capture bool) CheckpointOption {
	return func(o *checkpointOptions) {
		o.captureAssignments = capture
	}
}

// WithBVSnapshot enables/disables capturing BV triage summary (bd-32ck).
func WithBVSnapshot(capture bool) CheckpointOption {
	return func(o *checkpointOptions) {
		o.captureBVSnapshot = capture
	}
}

func defaultOptions() checkpointOptions {
	return checkpointOptions{
		captureGit:          true,
		scrollbackLines:     5000,
		scrollbackCompress:  true,
		scrollbackMaxSizeMB: 10,
		captureAssignments:  true, // bd-32ck: enabled by default
		captureBVSnapshot:   true, // bd-32ck: enabled by default
	}
}
