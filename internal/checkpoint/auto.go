package checkpoint

import (
	"fmt"
	"log"
	"strings"
)

const (
	// AutoCheckpointPrefix is the prefix for auto-generated checkpoint names
	AutoCheckpointPrefix = "auto"
)

// AutoCheckpointReason describes why an auto-checkpoint was triggered
type AutoCheckpointReason string

const (
	ReasonBroadcast AutoCheckpointReason = "broadcast"  // Before sending to all agents
	ReasonAddAgents AutoCheckpointReason = "add_agents" // Before adding many agents
	ReasonSpawn     AutoCheckpointReason = "spawn"      // After spawning session
	ReasonRiskyOp   AutoCheckpointReason = "risky_op"   // Before other risky operation
	ReasonInterval  AutoCheckpointReason = "interval"   // Periodic interval checkpoint
	ReasonRotation  AutoCheckpointReason = "rotation"   // Before context rotation
	ReasonError     AutoCheckpointReason = "error"      // On agent error
)

// AutoEventType describes the type of event that triggered an auto-checkpoint
type AutoEventType int

const (
	EventRotation AutoEventType = iota // Context rotation is about to happen
	EventError                         // Agent error detected
)

// AutoEvent represents an event that can trigger an auto-checkpoint
type AutoEvent struct {
	Type        AutoEventType
	SessionName string
	AgentID     string // Which agent triggered the event
	Description string // Additional context
}

// AutoCheckpointConfig configures the background auto-checkpoint worker
type AutoCheckpointConfig struct {
	Enabled         bool // Top-level toggle
	IntervalMinutes int  // Periodic checkpoint interval (0 = disabled)
	MaxCheckpoints  int  // Max auto-checkpoints per session
	OnRotation      bool // Checkpoint before rotation
	OnError         bool // Checkpoint on error
	ScrollbackLines int  // Lines of scrollback to capture
	IncludeGit      bool // Capture git state
}

// AutoCheckpointOptions configures auto-checkpoint creation
type AutoCheckpointOptions struct {
	SessionName     string
	Reason          AutoCheckpointReason
	Description     string // Additional context
	ScrollbackLines int
	IncludeGit      bool
	MaxCheckpoints  int // Max auto-checkpoints to keep (rotation)
}

// AutoCheckpointer handles automatic checkpoint creation with rotation
type AutoCheckpointer struct {
	capturer *Capturer
	storage  *Storage
}

// NewAutoCheckpointer creates a new auto-checkpointer
func NewAutoCheckpointer() *AutoCheckpointer {
	return &AutoCheckpointer{
		capturer: NewCapturer(),
		storage:  NewStorage(),
	}
}

// Create creates an auto-checkpoint with the given options
// It returns the created checkpoint and any error encountered
func (a *AutoCheckpointer) Create(opts AutoCheckpointOptions) (*Checkpoint, error) {
	// Build checkpoint name from reason
	name := fmt.Sprintf("%s-%s", AutoCheckpointPrefix, opts.Reason)

	// Build description
	desc := fmt.Sprintf("Auto-checkpoint: %s", opts.Reason)
	if opts.Description != "" {
		desc = fmt.Sprintf("%s (%s)", desc, opts.Description)
	}

	// Build checkpoint options
	cpOpts := []CheckpointOption{
		WithDescription(desc),
		WithGitCapture(opts.IncludeGit),
	}
	if opts.ScrollbackLines > 0 {
		cpOpts = append(cpOpts, WithScrollbackLines(opts.ScrollbackLines))
	}

	// Create the checkpoint
	cp, err := a.capturer.Create(opts.SessionName, name, cpOpts...)
	if err != nil {
		return nil, fmt.Errorf("creating auto-checkpoint: %w", err)
	}

	// Apply rotation policy
	if opts.MaxCheckpoints > 0 {
		if err := a.rotateAutoCheckpoints(opts.SessionName, opts.MaxCheckpoints); err != nil {
			// Log but don't fail - checkpoint was created successfully
			log.Printf("Warning: failed to rotate auto-checkpoints: %v", err)
		}
	}

	return cp, nil
}

// rotateAutoCheckpoints ensures we don't exceed the max auto-checkpoints
// by deleting the oldest auto-checkpoints
func (a *AutoCheckpointer) rotateAutoCheckpoints(sessionName string, maxCount int) error {
	autoCheckpoints, err := a.autoCheckpointCandidates(sessionName)
	if err != nil {
		return err
	}

	// If under limit, nothing to do
	if len(autoCheckpoints) <= maxCount {
		return nil
	}

	// Validate the checkpoints we intend to keep before deleting anything older.
	for _, candidate := range autoCheckpoints[:maxCount] {
		cp, err := a.storage.Load(sessionName, candidate.name)
		if err != nil {
			return fmt.Errorf("auto-checkpoint rotation blocked by invalid retained checkpoint %q: %w", candidate.name, err)
		}
		if !isAutoCheckpoint(cp) {
			return fmt.Errorf("auto-checkpoint rotation blocked by inconsistent retained checkpoint %q", candidate.name)
		}
	}

	// Delete oldest auto-checkpoints (candidates are sorted newest first).
	for _, candidate := range autoCheckpoints[maxCount:] {
		if err := a.storage.Delete(sessionName, candidate.name); err != nil {
			return fmt.Errorf("deleting old auto-checkpoint %q: %w", candidate.name, err)
		}
	}

	return nil
}

// isAutoCheckpoint checks if a checkpoint was auto-generated
func isAutoCheckpoint(cp *Checkpoint) bool {
	// Check by name prefix (use "auto-" to avoid matching names like "automatic")
	if strings.HasPrefix(cp.Name, AutoCheckpointPrefix+"-") {
		return true
	}
	// Also check description as fallback
	if strings.Contains(cp.Description, "Auto-checkpoint:") {
		return true
	}
	return false
}

func autoCheckpointNameFromID(checkpointID string) string {
	parts := strings.SplitN(checkpointID, "-", 4)
	if len(parts) < 4 {
		return ""
	}
	return parts[3]
}

func isAutoCheckpointID(checkpointID string) bool {
	return strings.HasPrefix(strings.ToLower(autoCheckpointNameFromID(checkpointID)), AutoCheckpointPrefix+"-")
}

func (a *AutoCheckpointer) autoCheckpointCandidates(sessionName string) ([]checkpointSelectionEntry, error) {
	candidates, err := a.storage.selectionEntries(sessionName)
	if err != nil {
		return nil, err
	}

	var autoCandidates []checkpointSelectionEntry
	for _, candidate := range candidates {
		if isAutoCheckpointID(candidate.name) {
			autoCandidates = append(autoCandidates, candidate)
			continue
		}

		cp, err := a.storage.Load(sessionName, candidate.name)
		if err != nil {
			continue
		}
		if isAutoCheckpoint(cp) {
			autoCandidates = append(autoCandidates, candidate)
		}
	}

	return autoCandidates, nil
}
