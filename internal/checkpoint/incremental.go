package checkpoint

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	// IncrementalVersion is the current incremental checkpoint format version
	IncrementalVersion = 1
	// IncrementalMetadataFile is the filename for incremental checkpoint metadata
	IncrementalMetadataFile = "incremental.json"
	// IncrementalPatchFile is the filename for git diff from base
	IncrementalPatchFile = "incremental.patch"
	// DiffPanesDir is the subdirectory for pane scrollback diffs
	DiffPanesDir = "pane_diffs"
)

// IncrementalCheckpoint represents a differential checkpoint that stores only
// changes from a base checkpoint. This saves storage space for frequent checkpoints.
type IncrementalCheckpoint struct {
	// Version is the incremental checkpoint format version
	Version int `json:"version"`
	// ID is the unique identifier for this incremental
	ID string `json:"id"`
	// SessionName is the tmux session this belongs to
	SessionName string `json:"session_name"`
	// BaseCheckpointID is the ID of the full checkpoint this is based on
	BaseCheckpointID string `json:"base_checkpoint_id"`
	// BaseTimestamp is when the base checkpoint was created
	BaseTimestamp time.Time `json:"base_timestamp"`
	// CreatedAt is when this incremental was created
	CreatedAt time.Time `json:"created_at"`
	// Description is an optional description
	Description string `json:"description,omitempty"`
	// Changes holds the differential data
	Changes IncrementalChanges `json:"changes"`
}

// IncrementalChanges holds all the changed data since the base checkpoint.
type IncrementalChanges struct {
	// PaneChanges maps pane ID to its changes
	PaneChanges map[string]PaneChange `json:"pane_changes,omitempty"`
	// GitChange holds changes to git state
	GitChange *GitChange `json:"git_change,omitempty"`
	// SessionChange holds changes to session layout
	SessionChange *SessionChange `json:"session_change,omitempty"`
}

// PaneChange represents changes to a single pane since the base checkpoint.
type PaneChange struct {
	// NewLines is the number of new lines added since base
	NewLines int `json:"new_lines"`
	// DiffFile is the relative path to the scrollback diff file
	DiffFile string `json:"diff_file,omitempty"`
	// DiffContent is the new scrollback content (lines after base)
	DiffContent string `json:"-"` // Not serialized, held in memory during processing
	// Compressed is the compressed diff content
	Compressed []byte `json:"-"`
	// AgentType may have changed
	AgentType string `json:"agent_type,omitempty"`
	// Title may have changed
	Title string `json:"title,omitempty"`
	// Removed indicates pane was removed
	Removed bool `json:"removed,omitempty"`
	// Added indicates pane is new (not in base)
	Added bool `json:"added,omitempty"`
}

// GitChange represents changes to git state since the base checkpoint.
type GitChange struct {
	// FromCommit is the base checkpoint's commit
	FromCommit string `json:"from_commit"`
	// ToCommit is the current commit
	ToCommit string `json:"to_commit"`
	// Branch may have changed
	Branch string `json:"branch,omitempty"`
	// PatchFile is the relative path to the incremental patch
	PatchFile string `json:"patch_file,omitempty"`
	// IsDirty indicates uncommitted changes
	IsDirty bool `json:"is_dirty"`
	// StagedCount changed
	StagedCount int `json:"staged_count"`
	// UnstagedCount changed
	UnstagedCount int `json:"unstaged_count"`
	// UntrackedCount changed
	UntrackedCount int `json:"untracked_count"`
}

// SessionChange represents changes to session layout.
type SessionChange struct {
	// Layout changed
	Layout string `json:"layout,omitempty"`
	// ActivePaneIndex changed
	ActivePaneIndex int `json:"active_pane_index,omitempty"`
	// PaneCount changed
	PaneCount int `json:"pane_count,omitempty"`
}

// IncrementalCreator creates incremental checkpoints from a base.
type IncrementalCreator struct {
	storage *Storage
}

// NewIncrementalCreator creates a new incremental checkpoint creator.
func NewIncrementalCreator() *IncrementalCreator {
	return &IncrementalCreator{
		storage: NewStorage(),
	}
}

// NewIncrementalCreatorWithStorage creates an incremental creator with custom storage.
func NewIncrementalCreatorWithStorage(storage *Storage) *IncrementalCreator {
	return &IncrementalCreator{
		storage: storage,
	}
}

// Create creates an incremental checkpoint based on the given base checkpoint.
// It computes the diff between the current session state and the base checkpoint.
func (ic *IncrementalCreator) Create(sessionName, name string, baseCheckpointID string) (*IncrementalCheckpoint, error) {
	// Load the base checkpoint
	base, err := ic.storage.Load(sessionName, baseCheckpointID)
	if err != nil {
		return nil, fmt.Errorf("loading base checkpoint: %w", err)
	}

	// Capture current state
	capturer := NewCapturer()
	current, err := capturer.Create(sessionName, "temp-incremental")
	if err != nil {
		return nil, fmt.Errorf("capturing current state: %w", err)
	}

	// Create the incremental checkpoint
	inc := &IncrementalCheckpoint{
		Version:          IncrementalVersion,
		ID:               GenerateID(name),
		SessionName:      sessionName,
		BaseCheckpointID: baseCheckpointID,
		BaseTimestamp:    base.CreatedAt,
		CreatedAt:        time.Now(),
		Description:      fmt.Sprintf("Incremental from %s", base.Name),
		Changes:          IncrementalChanges{},
	}

	// Compute pane changes
	inc.Changes.PaneChanges, err = ic.computePaneChanges(sessionName, base, current)
	if err != nil {
		return nil, fmt.Errorf("computing pane changes: %w", err)
	}

	// Compute git changes
	inc.Changes.GitChange = ic.computeGitChange(base.Git, current.Git)

	// Compute session changes
	inc.Changes.SessionChange = ic.computeSessionChange(base.Session, current.Session)

	// Save the incremental checkpoint
	if err := ic.save(inc, base.WorkingDir); err != nil {
		return nil, fmt.Errorf("saving incremental checkpoint: %w", err)
	}

	// Clean up temp checkpoint
	_ = ic.storage.Delete(sessionName, current.ID)

	return inc, nil
}

// computePaneChanges computes the changes between base and current pane states.
func (ic *IncrementalCreator) computePaneChanges(sessionName string, base, current *Checkpoint) (map[string]PaneChange, error) {
	changes := make(map[string]PaneChange)

	// Create lookup maps
	basePanes := make(map[string]PaneState)
	for _, p := range base.Session.Panes {
		basePanes[p.ID] = p
	}

	currentPanes := make(map[string]PaneState)
	for _, p := range current.Session.Panes {
		currentPanes[p.ID] = p
	}

	// Check for modified or removed panes
	for paneID, basePane := range basePanes {
		if currentPane, exists := currentPanes[paneID]; exists {
			// Pane exists in both - check for changes
			change := PaneChange{}
			hasChanges := false

			// Check agent type change
			if basePane.AgentType != currentPane.AgentType {
				change.AgentType = currentPane.AgentType
				hasChanges = true
			}

			// Check title change
			if basePane.Title != currentPane.Title {
				change.Title = currentPane.Title
				hasChanges = true
			}

			// Compute scrollback diff
			baseScrollback, _ := ic.storage.LoadCompressedScrollback(sessionName, base.ID, paneID)
			currentScrollback, _ := ic.storage.LoadCompressedScrollback(sessionName, current.ID, paneID)

			if baseScrollback != currentScrollback {
				diff := computeScrollbackDiff(baseScrollback, currentScrollback)
				if diff != "" {
					change.NewLines = countLines(diff)
					change.DiffContent = diff
					hasChanges = true
				}
			}

			if hasChanges {
				changes[paneID] = change
			}
		} else {
			// Pane was removed
			changes[paneID] = PaneChange{Removed: true}
		}
	}

	// Check for new panes
	for paneID := range currentPanes {
		if _, exists := basePanes[paneID]; !exists {
			// New pane
			currentScrollback, _ := ic.storage.LoadCompressedScrollback(sessionName, current.ID, paneID)
			changes[paneID] = PaneChange{
				Added:       true,
				NewLines:    countLines(currentScrollback),
				DiffContent: currentScrollback,
			}
		}
	}

	return changes, nil
}

// computeScrollbackDiff returns the new lines in current that aren't in base.
// This is a simple approach that assumes scrollback only appends new lines.
func computeScrollbackDiff(base, current string) string {
	if base == "" {
		return current
	}
	if current == "" {
		return ""
	}

	// Find where current diverges from base
	// Try to find the end of base content in current
	baseLines := strings.Split(base, "\n")
	currentLines := strings.Split(current, "\n")

	if len(currentLines) <= len(baseLines) {
		// No new lines (or fewer lines due to truncation)
		return ""
	}

	// Simple heuristic: assume new lines are appended at the end
	// This works for typical scrollback where new content appears at bottom
	newLines := currentLines[len(baseLines):]
	return strings.Join(newLines, "\n")
}

// computeGitChange computes changes between base and current git state.
func (ic *IncrementalCreator) computeGitChange(base, current GitState) *GitChange {
	// If nothing changed, return nil
	if base.Commit == current.Commit &&
		base.Branch == current.Branch &&
		base.IsDirty == current.IsDirty &&
		base.StagedCount == current.StagedCount &&
		base.UnstagedCount == current.UnstagedCount {
		return nil
	}

	change := &GitChange{
		FromCommit:     base.Commit,
		ToCommit:       current.Commit,
		IsDirty:        current.IsDirty,
		StagedCount:    current.StagedCount,
		UnstagedCount:  current.UnstagedCount,
		UntrackedCount: current.UntrackedCount,
	}

	// Only include branch if it changed
	if base.Branch != current.Branch {
		change.Branch = current.Branch
	}

	return change
}

// computeSessionChange computes changes to session layout.
func (ic *IncrementalCreator) computeSessionChange(base, current SessionState) *SessionChange {
	change := &SessionChange{}
	hasChanges := false

	if base.Layout != current.Layout {
		change.Layout = current.Layout
		hasChanges = true
	}

	if base.ActivePaneIndex != current.ActivePaneIndex {
		change.ActivePaneIndex = current.ActivePaneIndex
		hasChanges = true
	}

	if len(base.Panes) != len(current.Panes) {
		change.PaneCount = len(current.Panes)
		hasChanges = true
	}

	if hasChanges {
		return change
	}
	return nil
}

// save persists the incremental checkpoint to disk.
func (ic *IncrementalCreator) save(inc *IncrementalCheckpoint, repoDir string) error {
	dir := ic.incrementalDir(inc.SessionName, inc.ID)

	// Create directory
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating incremental directory: %w", err)
	}

	// Save pane diffs
	diffDir := filepath.Join(dir, DiffPanesDir)
	if err := os.MkdirAll(diffDir, 0755); err != nil {
		return fmt.Errorf("creating diff panes directory: %w", err)
	}

	for paneID, change := range inc.Changes.PaneChanges {
		if change.DiffContent != "" {
			filename := fmt.Sprintf("pane_%s_diff.txt.gz", sanitizeName(paneID))
			fullPath := filepath.Join(diffDir, filename)

			// Compress the diff
			compressed, err := gzipCompress([]byte(change.DiffContent))
			if err != nil {
				return fmt.Errorf("compressing pane diff: %w", err)
			}

			if err := util.AtomicWriteFile(fullPath, compressed, 0600); err != nil {
				return fmt.Errorf("saving pane diff: %w", err)
			}

			// Update the change with file path
			change.DiffFile = filepath.Join(DiffPanesDir, filename)
			change.DiffContent = "" // Clear content after saving
			inc.Changes.PaneChanges[paneID] = change
		}
	}

	// Save git patch if commits differ
	if inc.Changes.GitChange != nil && inc.Changes.GitChange.FromCommit != inc.Changes.GitChange.ToCommit {
		patch, err := generateGitPatch(repoDir, inc.Changes.GitChange.FromCommit, inc.Changes.GitChange.ToCommit)
		if err == nil && patch != "" {
			patchPath := filepath.Join(dir, IncrementalPatchFile)
			if err := util.AtomicWriteFile(patchPath, []byte(patch), 0600); err != nil {
				return fmt.Errorf("saving git patch: %w", err)
			}
			inc.Changes.GitChange.PatchFile = IncrementalPatchFile
		}
	}

	// Save metadata
	metaPath := filepath.Join(dir, IncrementalMetadataFile)
	data, err := json.MarshalIndent(inc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling incremental metadata: %w", err)
	}

	if err := util.AtomicWriteFile(metaPath, data, 0600); err != nil {
		return fmt.Errorf("saving incremental metadata: %w", err)
	}

	return nil
}

// incrementalDir returns the directory path for an incremental checkpoint.
func (ic *IncrementalCreator) incrementalDir(sessionName, incrementalID string) string {
	return filepath.Join(ic.storage.BaseDir, sessionName, "incremental", incrementalID)
}

// generateGitPatch generates a git diff patch between two commits.
func generateGitPatch(repoDir, fromCommit, toCommit string) (string, error) {
	if fromCommit == "" || toCommit == "" {
		return "", nil
	}

	var cmd *exec.Cmd
	if repoDir != "" {
		cmd = exec.Command("git", "-C", repoDir, "diff", fromCommit+".."+toCommit)
	} else {
		cmd = exec.Command("git", "diff", fromCommit+".."+toCommit)
	}
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("generating git diff: %w", err)
	}

	return string(output), nil
}

// IncrementalResolver resolves an incremental checkpoint to a full checkpoint.
type IncrementalResolver struct {
	storage *Storage
}

// NewIncrementalResolver creates a new resolver.
func NewIncrementalResolver() *IncrementalResolver {
	return &IncrementalResolver{
		storage: NewStorage(),
	}
}

// NewIncrementalResolverWithStorage creates a resolver with custom storage.
func NewIncrementalResolverWithStorage(storage *Storage) *IncrementalResolver {
	return &IncrementalResolver{
		storage: storage,
	}
}

// Resolve applies an incremental checkpoint to its base to produce a full checkpoint.
func (ir *IncrementalResolver) Resolve(sessionName, incrementalID string) (*Checkpoint, error) {
	// Load the incremental checkpoint
	inc, err := ir.loadIncremental(sessionName, incrementalID)
	if err != nil {
		return nil, fmt.Errorf("loading incremental checkpoint: %w", err)
	}

	// Load the base checkpoint
	base, err := ir.storage.Load(sessionName, inc.BaseCheckpointID)
	if err != nil {
		return nil, fmt.Errorf("loading base checkpoint: %w", err)
	}

	// Create a deep copy of base and apply changes.
	// Shallow copy shares the Panes slice — deep copy to avoid corrupting the original.
	resolved := *base
	if base.Session.Panes != nil {
		resolved.Session.Panes = make([]PaneState, len(base.Session.Panes))
		copy(resolved.Session.Panes, base.Session.Panes)
	}
	resolved.ID = fmt.Sprintf("resolved-%s", incrementalID)
	resolved.CreatedAt = inc.CreatedAt
	resolved.Description = fmt.Sprintf("Resolved from incremental %s (base: %s)", incrementalID, inc.BaseCheckpointID)

	// Apply session changes
	if inc.Changes.SessionChange != nil {
		if inc.Changes.SessionChange.Layout != "" {
			resolved.Session.Layout = inc.Changes.SessionChange.Layout
		}
		if inc.Changes.SessionChange.ActivePaneIndex != 0 {
			resolved.Session.ActivePaneIndex = inc.Changes.SessionChange.ActivePaneIndex
		}
	}

	// Apply pane changes
	for paneID, change := range inc.Changes.PaneChanges {
		if change.Removed {
			// Remove pane from resolved
			resolved.Session.Panes = removePaneByID(resolved.Session.Panes, paneID)
			continue
		}

		if change.Added {
			// Add new pane (basic info only for now)
			// Full scrollback would need to be loaded from the diff file
			newPane := PaneState{
				ID:              paneID,
				ScrollbackLines: change.NewLines,
			}
			resolved.Session.Panes = append(resolved.Session.Panes, newPane)
			continue
		}

		// Update existing pane
		for i := range resolved.Session.Panes {
			if resolved.Session.Panes[i].ID == paneID {
				if change.AgentType != "" {
					resolved.Session.Panes[i].AgentType = change.AgentType
				}
				if change.Title != "" {
					resolved.Session.Panes[i].Title = change.Title
				}
				if change.NewLines > 0 {
					resolved.Session.Panes[i].ScrollbackLines += change.NewLines
				}
				break
			}
		}
	}

	// Apply git changes
	if inc.Changes.GitChange != nil {
		resolved.Git.Commit = inc.Changes.GitChange.ToCommit
		resolved.Git.IsDirty = inc.Changes.GitChange.IsDirty
		resolved.Git.StagedCount = inc.Changes.GitChange.StagedCount
		resolved.Git.UnstagedCount = inc.Changes.GitChange.UnstagedCount
		resolved.Git.UntrackedCount = inc.Changes.GitChange.UntrackedCount
		if inc.Changes.GitChange.Branch != "" {
			resolved.Git.Branch = inc.Changes.GitChange.Branch
		}
	}

	resolved.PaneCount = len(resolved.Session.Panes)

	return &resolved, nil
}

// loadIncremental loads an incremental checkpoint from disk.
func (ir *IncrementalResolver) loadIncremental(sessionName, incrementalID string) (*IncrementalCheckpoint, error) {
	dir := filepath.Join(ir.storage.BaseDir, sessionName, "incremental", incrementalID)
	metaPath := filepath.Join(dir, IncrementalMetadataFile)

	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, fmt.Errorf("reading incremental metadata: %w", err)
	}

	var inc IncrementalCheckpoint
	if err := json.Unmarshal(data, &inc); err != nil {
		return nil, fmt.Errorf("parsing incremental metadata: %w", err)
	}

	return &inc, nil
}

// removePaneByID removes a pane from a slice by ID.
func removePaneByID(panes []PaneState, paneID string) []PaneState {
	result := make([]PaneState, 0, len(panes))
	for _, p := range panes {
		if p.ID != paneID {
			result = append(result, p)
		}
	}
	return result
}

// ListIncrementals returns all incremental checkpoints for a session.
func (ir *IncrementalResolver) ListIncrementals(sessionName string) ([]*IncrementalCheckpoint, error) {
	incDir := filepath.Join(ir.storage.BaseDir, sessionName, "incremental")

	entries, err := os.ReadDir(incDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading incremental directory: %w", err)
	}

	var incrementals []*IncrementalCheckpoint
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		inc, err := ir.loadIncremental(sessionName, entry.Name())
		if err != nil {
			continue // Skip invalid incrementals
		}
		incrementals = append(incrementals, inc)
	}

	return incrementals, nil
}

// ChainResolve resolves a chain of incremental checkpoints.
// Given an incremental that may be based on another incremental (not a full checkpoint),
// this function walks the chain back to find the base full checkpoint and applies
// all incrementals in order.
func (ir *IncrementalResolver) ChainResolve(sessionName, incrementalID string) (*Checkpoint, error) {
	// Build the chain of incrementals (with cycle detection)
	chain := []*IncrementalCheckpoint{}
	visited := make(map[string]bool)

	currentID := incrementalID
	for {
		if visited[currentID] {
			return nil, fmt.Errorf("cycle detected in incremental chain at %s", currentID)
		}
		visited[currentID] = true
		inc, err := ir.loadIncremental(sessionName, currentID)
		if err != nil {
			// Not an incremental - might be the base checkpoint
			break
		}

		chain = append([]*IncrementalCheckpoint{inc}, chain...) // Prepend

		// Check if base is another incremental or a full checkpoint
		_, loadErr := ir.loadIncremental(sessionName, inc.BaseCheckpointID)
		if loadErr != nil {
			// Base is a full checkpoint, stop here
			break
		}

		// Base is another incremental, continue walking
		currentID = inc.BaseCheckpointID
	}

	if len(chain) == 0 {
		return nil, fmt.Errorf("no incremental checkpoints found in chain")
	}

	// Load the base full checkpoint
	baseID := chain[0].BaseCheckpointID
	resolved, err := ir.storage.Load(sessionName, baseID)
	if err != nil {
		return nil, fmt.Errorf("loading base checkpoint: %w", err)
	}

	// Apply each incremental in order
	for _, inc := range chain {
		resolved, err = ir.applyIncremental(resolved, inc)
		if err != nil {
			return nil, fmt.Errorf("applying incremental %s: %w", inc.ID, err)
		}
	}

	return resolved, nil
}

// applyIncremental applies an incremental's changes to a checkpoint.
func (ir *IncrementalResolver) applyIncremental(base *Checkpoint, inc *IncrementalCheckpoint) (*Checkpoint, error) {
	resolved := *base
	resolved.CreatedAt = inc.CreatedAt
	resolved.Description = fmt.Sprintf("Applied incremental %s", inc.ID)

	// Apply session changes
	if inc.Changes.SessionChange != nil {
		if inc.Changes.SessionChange.Layout != "" {
			resolved.Session.Layout = inc.Changes.SessionChange.Layout
		}
		if inc.Changes.SessionChange.ActivePaneIndex != 0 {
			resolved.Session.ActivePaneIndex = inc.Changes.SessionChange.ActivePaneIndex
		}
	}

	// Apply pane changes
	for paneID, change := range inc.Changes.PaneChanges {
		if change.Removed {
			resolved.Session.Panes = removePaneByID(resolved.Session.Panes, paneID)
			continue
		}

		if change.Added {
			newPane := PaneState{
				ID:              paneID,
				ScrollbackLines: change.NewLines,
			}
			resolved.Session.Panes = append(resolved.Session.Panes, newPane)
			continue
		}

		for i := range resolved.Session.Panes {
			if resolved.Session.Panes[i].ID == paneID {
				if change.AgentType != "" {
					resolved.Session.Panes[i].AgentType = change.AgentType
				}
				if change.Title != "" {
					resolved.Session.Panes[i].Title = change.Title
				}
				if change.NewLines > 0 {
					resolved.Session.Panes[i].ScrollbackLines += change.NewLines
				}
				break
			}
		}
	}

	// Apply git changes
	if inc.Changes.GitChange != nil {
		resolved.Git.Commit = inc.Changes.GitChange.ToCommit
		resolved.Git.IsDirty = inc.Changes.GitChange.IsDirty
		resolved.Git.StagedCount = inc.Changes.GitChange.StagedCount
		resolved.Git.UnstagedCount = inc.Changes.GitChange.UnstagedCount
		resolved.Git.UntrackedCount = inc.Changes.GitChange.UntrackedCount
		if inc.Changes.GitChange.Branch != "" {
			resolved.Git.Branch = inc.Changes.GitChange.Branch
		}
	}

	resolved.PaneCount = len(resolved.Session.Panes)

	return &resolved, nil
}

// StorageSavings calculates the approximate storage savings of an incremental checkpoint.
func (inc *IncrementalCheckpoint) StorageSavings(storage *Storage) (savedBytes int64, percentSaved float64, err error) {
	// Estimate full checkpoint size (sum of all scrollback)
	base, err := storage.Load(inc.SessionName, inc.BaseCheckpointID)
	if err != nil {
		return 0, 0, err
	}

	var fullSize int64
	for _, pane := range base.Session.Panes {
		if pane.ScrollbackFile != "" {
			scrollback, _ := storage.LoadCompressedScrollback(inc.SessionName, inc.BaseCheckpointID, pane.ID)
			fullSize += int64(len(scrollback))
		}
	}

	// Estimate incremental size (sum of diffs)
	var incSize int64
	for _, change := range inc.Changes.PaneChanges {
		incSize += int64(change.NewLines * 80) // Rough estimate: 80 chars per line
	}

	if fullSize == 0 {
		return 0, 0, nil
	}

	savedBytes = fullSize - incSize
	percentSaved = float64(savedBytes) / float64(fullSize) * 100

	return savedBytes, percentSaved, nil
}
