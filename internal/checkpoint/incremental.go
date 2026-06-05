package checkpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
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
	// DiffSummary describes how the scrollback diff artifact was preserved.
	DiffSummary *ScrollbackArtifactSummary `json:"diff_summary,omitempty"`
	// DiffContent is the new scrollback content (lines after base)
	DiffContent string `json:"-"` // Not serialized, held in memory during processing
	// Compressed is the compressed diff content
	Compressed []byte `json:"-"`
	// AgentType may have changed
	AgentType *string `json:"agent_type,omitempty"`
	// Title may have changed
	Title *string `json:"title,omitempty"`
	// Command may have changed
	Command *string `json:"command,omitempty"`
	// Removed indicates pane was removed
	Removed bool `json:"removed,omitempty"`
	// Added indicates pane is new (not in base)
	Added bool `json:"added,omitempty"`
	// Pane captures added-pane metadata needed to reconstruct the pane on replay
	Pane *PaneState `json:"pane,omitempty"`
}

// GitChange represents changes to git state since the base checkpoint.
type GitChange struct {
	// FromCommit is the base checkpoint's commit
	FromCommit string `json:"from_commit"`
	// ToCommit is the current commit
	ToCommit string `json:"to_commit"`
	// Branch may have changed
	Branch *string `json:"branch,omitempty"`
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
	Layout *string `json:"layout,omitempty"`
	// WindowLayouts changed
	WindowLayouts []WindowLayoutState `json:"window_layouts,omitempty"`
	// ActivePaneIndex changed
	ActivePaneIndex *int `json:"active_pane_index,omitempty"`
	// ActivePaneID identifies the selected pane independent of slice order
	ActivePaneID *string `json:"active_pane_id,omitempty"`
	// PaneCount changed
	PaneCount *int `json:"pane_count,omitempty"`
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

	// Capture current state into the same storage backend used for diffs and cleanup.
	capturer := NewCapturerWithStorage(ic.storage)
	current, err := capturer.Create(sessionName, "temp-incremental")
	if err != nil {
		return nil, fmt.Errorf("capturing current state: %w", err)
	}
	defer func() {
		_ = ic.storage.Delete(sessionName, current.ID)
	}()

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

	// Compute git changes, including artifact-only drift that would otherwise
	// leave resolved incrementals pointing at stale base git files.
	inc.Changes.GitChange, err = ic.computeGitChangeForCheckpoints(sessionName, base, current)
	if err != nil {
		return nil, fmt.Errorf("computing git changes: %w", err)
	}

	// Compute session changes
	inc.Changes.SessionChange = ic.computeSessionChange(base.Session, current.Session)

	// Save the incremental checkpoint
	if err := ic.save(inc, base.WorkingDir); err != nil {
		return nil, fmt.Errorf("saving incremental checkpoint: %w", err)
	}

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
			metadataChanged := false

			// Check agent type change
			if basePane.AgentType != currentPane.AgentType {
				agentType := currentPane.AgentType
				change.AgentType = &agentType
				hasChanges = true
				metadataChanged = true
			}

			// Check title change
			if basePane.Title != currentPane.Title {
				title := currentPane.Title
				change.Title = &title
				hasChanges = true
				metadataChanged = true
			}

			// Check command change
			if basePane.Command != currentPane.Command {
				command := currentPane.Command
				change.Command = &command
				hasChanges = true
				metadataChanged = true
			}

			if basePane.Index != currentPane.Index ||
				basePane.WindowIndex != currentPane.WindowIndex ||
				basePane.Width != currentPane.Width ||
				basePane.Height != currentPane.Height {
				metadataChanged = true
				hasChanges = true
			}

			if metadataChanged {
				change.Pane = paneMetadataSnapshot(currentPane)
			}

			// Compute scrollback diff
			baseScrollback, err := ic.loadPaneScrollback(sessionName, base.ID, basePane)
			if err != nil {
				return nil, fmt.Errorf("loading base scrollback for pane %s: %w", paneID, err)
			}
			currentScrollback, err := ic.loadPaneScrollback(sessionName, current.ID, currentPane)
			if err != nil {
				return nil, fmt.Errorf("loading current scrollback for pane %s: %w", paneID, err)
			}

			if baseScrollback != currentScrollback {
				diff := computeScrollbackDiff(baseScrollback, currentScrollback)
				if diff != "" {
					change.NewLines = countLines(diff)
					change.DiffContent = diff
					change.DiffSummary = scrollbackDiffSummary(diff, change.NewLines, false, 0)
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
			currentScrollback, err := ic.loadPaneScrollback(sessionName, current.ID, currentPanes[paneID])
			if err != nil {
				return nil, fmt.Errorf("loading current scrollback for added pane %s: %w", paneID, err)
			}
			changes[paneID] = PaneChange{
				Added:       true,
				NewLines:    countLines(currentScrollback),
				DiffContent: currentScrollback,
				DiffSummary: scrollbackDiffSummary(currentScrollback, countLines(currentScrollback), false, 0),
				Pane:        paneMetadataSnapshot(currentPanes[paneID]),
			}
		}
	}

	return changes, nil
}

func (ic *IncrementalCreator) loadPaneScrollback(sessionName, checkpointID string, pane PaneState) (string, error) {
	scrollback, err := ic.storage.LoadPaneScrollback(sessionName, checkpointID, pane)
	if err == nil {
		return scrollback, nil
	}
	if pane.ScrollbackFile == "" && pane.ScrollbackLines == 0 && errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return "", err
}

func scrollbackDiffSummary(content string, lines int, artifactPreserved bool, storedBytes int64) *ScrollbackArtifactSummary {
	if content == "" {
		return nil
	}
	return &ScrollbackArtifactSummary{
		Captured:          content != "",
		ArtifactPreserved: artifactPreserved,
		Compacted:         artifactPreserved,
		Compression:       mapCompressionName(artifactPreserved),
		LineCount:         lines,
		RawBytes:          len(content),
		StoredBytes:       storedBytes,
	}
}

func mapCompressionName(compacted bool) string {
	if !compacted {
		return ""
	}
	return scrollbackCompressionGzip
}

// computeScrollbackDiff returns the new lines in current that aren't in base.
// It prefers the largest suffix/prefix overlap so rollover-truncated scrollback
// still preserves newly appended lines after older lines are dropped.
func computeScrollbackDiff(base, current string) string {
	if base == "" {
		return current
	}
	if current == "" {
		return ""
	}
	if base == current {
		return ""
	}

	baseLines := strings.Split(base, "\n")
	currentLines := strings.Split(current, "\n")

	if len(currentLines) <= len(baseLines) && containsContiguousSlice(baseLines, currentLines) {
		return ""
	}

	maxOverlap := min(len(baseLines), len(currentLines))

	for overlap := maxOverlap; overlap > 0; overlap-- {
		if !equalStringSlices(baseLines[len(baseLines)-overlap:], currentLines[:overlap]) {
			continue
		}
		if overlap == len(currentLines) {
			return ""
		}
		return strings.Join(currentLines[overlap:], "\n")
	}

	return current
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsContiguousSlice(haystack, needle []string) bool {
	if len(needle) == 0 {
		return true
	}
	if len(needle) > len(haystack) {
		return false
	}
	for start := 0; start+len(needle) <= len(haystack); start++ {
		if equalStringSlices(haystack[start:start+len(needle)], needle) {
			return true
		}
	}
	return false
}

// computeGitChange computes changes between base and current git state.
func (ic *IncrementalCreator) computeGitChange(base, current GitState) *GitChange {
	// If nothing changed, return nil
	if base.Commit == current.Commit &&
		base.Branch == current.Branch &&
		base.IsDirty == current.IsDirty &&
		base.StagedCount == current.StagedCount &&
		base.UnstagedCount == current.UnstagedCount &&
		base.UntrackedCount == current.UntrackedCount {
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
		branch := current.Branch
		change.Branch = &branch
	}

	return change
}

func (ic *IncrementalCreator) computeGitChangeForCheckpoints(sessionName string, base, current *Checkpoint) (*GitChange, error) {
	change := ic.computeGitChange(base.Git, current.Git)

	patchChanged, err := ic.storedGitArtifactChanged(sessionName, base.ID, base.Git, current.ID, current.Git, (*Storage).loadGitPatchForState)
	if err != nil {
		return nil, err
	}

	statusChanged, err := ic.storedGitArtifactChanged(sessionName, base.ID, base.Git, current.ID, current.Git, (*Storage).loadGitStatusForState)
	if err != nil {
		return nil, err
	}

	if change == nil && !patchChanged && !statusChanged {
		return nil, nil
	}
	if change != nil {
		return change, nil
	}

	return newGitChangeFromCurrent(base.Git, current.Git), nil
}

func (ic *IncrementalCreator) storedGitArtifactChanged(
	sessionName, baseCheckpointID string,
	baseGit GitState,
	currentCheckpointID string,
	currentGit GitState,
	load func(*Storage, string, string, GitState) (string, error),
) (bool, error) {
	baseContent, err := load(ic.storage, sessionName, baseCheckpointID, baseGit)
	if err != nil {
		return false, err
	}

	currentContent, err := load(ic.storage, sessionName, currentCheckpointID, currentGit)
	if err != nil {
		return false, err
	}

	return baseContent != currentContent, nil
}

func newGitChangeFromCurrent(base, current GitState) *GitChange {
	change := &GitChange{
		FromCommit:     base.Commit,
		ToCommit:       current.Commit,
		IsDirty:        current.IsDirty,
		StagedCount:    current.StagedCount,
		UnstagedCount:  current.UnstagedCount,
		UntrackedCount: current.UntrackedCount,
	}

	if base.Branch != current.Branch {
		branch := current.Branch
		change.Branch = &branch
	}

	return change
}

// computeSessionChange computes changes to session layout.
func (ic *IncrementalCreator) computeSessionChange(base, current SessionState) *SessionChange {
	change := &SessionChange{}
	hasChanges := false

	if base.Layout != current.Layout {
		layout := current.Layout
		change.Layout = &layout
		hasChanges = true
	}

	baseWindowLayouts := effectiveSessionWindowLayouts(base)
	currentWindowLayouts := effectiveSessionWindowLayouts(current)
	if !windowLayoutsEqual(baseWindowLayouts, currentWindowLayouts) {
		change.WindowLayouts = cloneWindowLayouts(currentWindowLayouts)
		hasChanges = true
	}

	baseActivePaneID := sessionActivePaneID(base)
	currentActivePaneID := sessionActivePaneID(current)
	if baseActivePaneID != currentActivePaneID {
		activePaneIndex := current.ActivePaneIndex
		activePaneID := currentActivePaneID
		change.ActivePaneIndex = &activePaneIndex
		change.ActivePaneID = &activePaneID
		hasChanges = true
	}

	if len(base.Panes) != len(current.Panes) {
		paneCount := len(current.Panes)
		change.PaneCount = &paneCount
		hasChanges = true
	}

	if hasChanges {
		return change
	}
	return nil
}

// save persists the incremental checkpoint to disk.
func (ic *IncrementalCreator) save(inc *IncrementalCheckpoint, repoDir string) error {
	dir, err := ic.incrementalDir(inc.SessionName, inc.ID)
	if err != nil {
		return err
	}

	// Create directory
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating incremental directory: %w", err)
	}

	// Save pane diffs
	diffDir, err := ensureCheckpointSubdir(dir, DiffPanesDir, "diff panes")
	if err != nil {
		return err
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
			change.DiffSummary = scrollbackDiffSummary(change.DiffContent, change.NewLines, true, int64(len(compressed)))
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
func (ic *IncrementalCreator) incrementalBaseDir(sessionName string) (string, error) {
	sessionDir, err := ic.storage.safeSessionDir(sessionName)
	if err != nil {
		return "", err
	}
	baseDir := filepath.Join(sessionDir, "incremental")
	if err := validateExistingDirectoryPath(baseDir, "incremental"); err != nil {
		return "", err
	}
	return baseDir, nil
}

func (ic *IncrementalCreator) incrementalDir(sessionName, incrementalID string) (string, error) {
	baseDir, err := ic.incrementalBaseDir(sessionName)
	if err != nil {
		return "", err
	}
	if err := validateCheckpointID(incrementalID); err != nil {
		return "", err
	}
	incDir := filepath.Join(baseDir, incrementalID)
	if err := validateExistingDirectoryPath(incDir, "incremental"); err != nil {
		return "", err
	}
	return incDir, nil
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
	resolved := cloneCheckpointForMutation(base)
	resolved.ID = fmt.Sprintf("resolved-%s", incrementalID)
	resolved.CreatedAt = inc.CreatedAt
	resolved.Description = fmt.Sprintf("Resolved from incremental %s (base: %s)", incrementalID, inc.BaseCheckpointID)

	var (
		desiredActivePaneID    *string
		desiredActivePaneIndex *int
	)

	// Apply session changes
	if inc.Changes.SessionChange != nil {
		if inc.Changes.SessionChange.Layout != nil {
			resolved.Session.Layout = *inc.Changes.SessionChange.Layout
		}
		if inc.Changes.SessionChange.WindowLayouts != nil {
			resolved.Session.WindowLayouts = cloneWindowLayouts(inc.Changes.SessionChange.WindowLayouts)
		}
		if inc.Changes.SessionChange.ActivePaneID != nil {
			desiredActivePaneID = inc.Changes.SessionChange.ActivePaneID
		}
		if inc.Changes.SessionChange.ActivePaneIndex != nil {
			desiredActivePaneIndex = inc.Changes.SessionChange.ActivePaneIndex
		}
	}

	activePaneID := sessionActivePaneID(resolved.Session)

	// Apply pane changes
	for paneID, change := range inc.Changes.PaneChanges {
		if change.Removed {
			// Remove pane from resolved
			resolved.Session.Panes = removePaneByID(resolved.Session.Panes, paneID)
			continue
		}

		if change.Added {
			newPane := paneStateFromAddedChange(paneID, change)
			resolved.Session.Panes = append(resolved.Session.Panes, newPane)
			continue
		}

		// Update existing pane
		for i := range resolved.Session.Panes {
			if resolved.Session.Panes[i].ID == paneID {
				applyPaneChange(&resolved.Session.Panes[i], change)
				break
			}
		}
	}

	sortCheckpointPanes(resolved.Session.Panes)
	resolved.Session.ActivePaneIndex = resolvedActivePaneIndex(
		resolved.Session.Panes,
		activePaneID,
		desiredActivePaneID,
		desiredActivePaneIndex,
	)

	// Apply git changes
	if inc.Changes.GitChange != nil {
		// Incrementals do not currently carry checkpoint-local git artifacts, so
		// any git change must clear inherited base pointers instead of lying.
		resolved.Git.PatchFile = ""
		resolved.Git.StatusFile = ""
		resolved.Git.Commit = inc.Changes.GitChange.ToCommit
		resolved.Git.IsDirty = inc.Changes.GitChange.IsDirty
		resolved.Git.StagedCount = inc.Changes.GitChange.StagedCount
		resolved.Git.UnstagedCount = inc.Changes.GitChange.UnstagedCount
		resolved.Git.UntrackedCount = inc.Changes.GitChange.UntrackedCount
		if inc.Changes.GitChange.Branch != nil {
			resolved.Git.Branch = *inc.Changes.GitChange.Branch
		}
	}

	resolved.PaneCount = len(resolved.Session.Panes)

	return &resolved, nil
}

// loadIncremental loads an incremental checkpoint from disk.
func (ir *IncrementalResolver) loadIncremental(sessionName, incrementalID string) (*IncrementalCheckpoint, error) {
	metaPath, err := ir.resolveExistingIncrementalMetadataPath(sessionName, incrementalID)
	if err != nil {
		return nil, fmt.Errorf("reading incremental metadata: %w", err)
	}

	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, fmt.Errorf("reading incremental metadata: %w", err)
	}

	var inc IncrementalCheckpoint
	if err := json.Unmarshal(data, &inc); err != nil {
		return nil, fmt.Errorf("parsing incremental metadata: %w", err)
	}
	if err := validateLoadedIncrementalMetadata(&inc, sessionName, incrementalID); err != nil {
		return nil, err
	}

	return &inc, nil
}

func (ir *IncrementalResolver) incrementalBaseDir(sessionName string) (string, error) {
	sessionDir, err := ir.storage.safeSessionDir(sessionName)
	if err != nil {
		return "", err
	}
	baseDir := filepath.Join(sessionDir, "incremental")
	if err := validateExistingDirectoryPath(baseDir, "incremental"); err != nil {
		return "", err
	}
	return baseDir, nil
}

func (ir *IncrementalResolver) incrementalDir(sessionName, incrementalID string) (string, error) {
	baseDir, err := ir.incrementalBaseDir(sessionName)
	if err != nil {
		return "", err
	}
	if err := validateCheckpointID(incrementalID); err != nil {
		return "", err
	}
	incDir := filepath.Join(baseDir, incrementalID)
	if err := validateExistingDirectoryPath(incDir, "incremental"); err != nil {
		return "", err
	}
	return incDir, nil
}

func (ir *IncrementalResolver) incrementalMetadataPath(sessionName, incrementalID string) (string, error) {
	incDir, err := ir.incrementalDir(sessionName, incrementalID)
	if err != nil {
		return "", err
	}
	return filepath.Join(incDir, IncrementalMetadataFile), nil
}

func (ir *IncrementalResolver) resolveExistingIncrementalMetadataPath(sessionName, incrementalID string) (string, error) {
	metaPath, err := ir.incrementalMetadataPath(sessionName, incrementalID)
	if err != nil {
		return "", err
	}

	return resolveExistingCheckpointArtifactPath(filepath.Dir(metaPath), IncrementalMetadataFile)
}

func (ir *IncrementalResolver) incrementalExists(sessionName, incrementalID string) (bool, error) {
	metaPath, err := ir.incrementalMetadataPath(sessionName, incrementalID)
	if err != nil {
		return false, err
	}
	_, err = os.Lstat(metaPath)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("stating incremental metadata: %w", err)
}

func validateLoadedIncrementalMetadata(inc *IncrementalCheckpoint, sessionName, incrementalID string) error {
	if inc == nil {
		return fmt.Errorf("incremental metadata is nil")
	}
	if err := tmux.ValidateSessionName(inc.SessionName); err != nil {
		return fmt.Errorf("invalid incremental metadata: invalid session name: %w", err)
	}
	if inc.ID != incrementalID {
		return fmt.Errorf("incremental metadata ID mismatch: expected %q, got %q", incrementalID, inc.ID)
	}
	if inc.SessionName != sessionName {
		return fmt.Errorf("incremental metadata session mismatch: expected %q, got %q", sessionName, inc.SessionName)
	}
	if inc.BaseCheckpointID == "" {
		return fmt.Errorf("incremental metadata missing base checkpoint ID")
	}
	if err := validateCheckpointID(inc.BaseCheckpointID); err != nil {
		return fmt.Errorf("invalid incremental metadata base checkpoint ID: %w", err)
	}
	return nil
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
	sessionDir, err := ir.storage.safeSessionDir(sessionName)
	if err != nil {
		return nil, err
	}
	incDir := filepath.Join(sessionDir, "incremental")

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

	sort.Slice(incrementals, func(i, j int) bool {
		return incrementals[i].CreatedAt.After(incrementals[j].CreatedAt)
	})

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
	exists, err := ir.incrementalExists(sessionName, currentID)
	if err != nil {
		return nil, fmt.Errorf("checking incremental checkpoint %s: %w", currentID, err)
	}
	if !exists {
		return nil, fmt.Errorf("incremental checkpoint not found: %s", currentID)
	}
	for {
		if visited[currentID] {
			return nil, fmt.Errorf("cycle detected in incremental chain at %s", currentID)
		}
		visited[currentID] = true
		inc, err := ir.loadIncremental(sessionName, currentID)
		if err != nil {
			return nil, fmt.Errorf("loading incremental checkpoint %s: %w", currentID, err)
		}

		chain = append([]*IncrementalCheckpoint{inc}, chain...) // Prepend

		// Check if base is another incremental or a full checkpoint
		exists, err := ir.incrementalExists(sessionName, inc.BaseCheckpointID)
		if err != nil {
			return nil, fmt.Errorf("checking base incremental %s: %w", inc.BaseCheckpointID, err)
		}
		if !exists {
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
	resolved := cloneCheckpointForMutation(base)
	resolved.CreatedAt = inc.CreatedAt
	resolved.Description = fmt.Sprintf("Applied incremental %s", inc.ID)

	var (
		desiredActivePaneID    *string
		desiredActivePaneIndex *int
	)

	// Apply session changes
	if inc.Changes.SessionChange != nil {
		if inc.Changes.SessionChange.Layout != nil {
			resolved.Session.Layout = *inc.Changes.SessionChange.Layout
		}
		if inc.Changes.SessionChange.WindowLayouts != nil {
			resolved.Session.WindowLayouts = cloneWindowLayouts(inc.Changes.SessionChange.WindowLayouts)
		}
		if inc.Changes.SessionChange.ActivePaneID != nil {
			desiredActivePaneID = inc.Changes.SessionChange.ActivePaneID
		}
		if inc.Changes.SessionChange.ActivePaneIndex != nil {
			desiredActivePaneIndex = inc.Changes.SessionChange.ActivePaneIndex
		}
	}

	activePaneID := sessionActivePaneID(resolved.Session)

	// Apply pane changes
	for paneID, change := range inc.Changes.PaneChanges {
		if change.Removed {
			resolved.Session.Panes = removePaneByID(resolved.Session.Panes, paneID)
			continue
		}

		if change.Added {
			newPane := paneStateFromAddedChange(paneID, change)
			resolved.Session.Panes = append(resolved.Session.Panes, newPane)
			continue
		}

		for i := range resolved.Session.Panes {
			if resolved.Session.Panes[i].ID == paneID {
				applyPaneChange(&resolved.Session.Panes[i], change)
				break
			}
		}
	}

	sortCheckpointPanes(resolved.Session.Panes)
	resolved.Session.ActivePaneIndex = resolvedActivePaneIndex(
		resolved.Session.Panes,
		activePaneID,
		desiredActivePaneID,
		desiredActivePaneIndex,
	)

	// Apply git changes
	if inc.Changes.GitChange != nil {
		// Incrementals do not currently carry checkpoint-local git artifacts, so
		// any git change must clear inherited base pointers instead of lying.
		resolved.Git.PatchFile = ""
		resolved.Git.StatusFile = ""
		resolved.Git.Commit = inc.Changes.GitChange.ToCommit
		resolved.Git.IsDirty = inc.Changes.GitChange.IsDirty
		resolved.Git.StagedCount = inc.Changes.GitChange.StagedCount
		resolved.Git.UnstagedCount = inc.Changes.GitChange.UnstagedCount
		resolved.Git.UntrackedCount = inc.Changes.GitChange.UntrackedCount
		if inc.Changes.GitChange.Branch != nil {
			resolved.Git.Branch = *inc.Changes.GitChange.Branch
		}
	}

	resolved.PaneCount = len(resolved.Session.Panes)

	return &resolved, nil
}

func cloneCheckpointForMutation(base *Checkpoint) Checkpoint {
	cloned := *base
	if base.Session.Panes != nil {
		cloned.Session.Panes = make([]PaneState, len(base.Session.Panes))
		copy(cloned.Session.Panes, base.Session.Panes)
	}
	cloned.Session.WindowLayouts = cloneWindowLayouts(base.Session.WindowLayouts)
	normalizeSessionPaneOrder(&cloned.Session)
	return cloned
}

func effectiveSessionWindowLayouts(state SessionState) []WindowLayoutState {
	if len(state.WindowLayouts) > 0 {
		return cloneWindowLayouts(state.WindowLayouts)
	}
	if state.Layout == "" {
		return nil
	}

	windowIndexes := sessionWindowIndexesFromPanes(state.Panes)
	switch len(windowIndexes) {
	case 0:
		return []WindowLayoutState{{WindowIndex: 0, Layout: state.Layout}}
	case 1:
		return []WindowLayoutState{{WindowIndex: windowIndexes[0], Layout: state.Layout}}
	default:
		return nil
	}
}

func paneStateFromAddedChange(paneID string, change PaneChange) PaneState {
	newPane := PaneState{
		ID:              paneID,
		ScrollbackLines: change.NewLines,
	}
	if change.Pane != nil {
		newPane = *change.Pane
	}
	if newPane.ID == "" {
		newPane.ID = paneID
	}
	newPane.ScrollbackFile = ""
	newPane.Scrollback = nil
	if change.NewLines != 0 || newPane.ScrollbackLines == 0 {
		newPane.ScrollbackLines = change.NewLines
	}
	return newPane
}

func paneMetadataSnapshot(pane PaneState) *PaneState {
	snapshot := pane
	// Resolved incrementals do not materialize scrollback files from temporary checkpoints.
	snapshot.ScrollbackFile = ""
	snapshot.Scrollback = nil
	return &snapshot
}

func applyPaneMetadata(target *PaneState, source PaneState) {
	target.Index = source.Index
	target.WindowIndex = source.WindowIndex
	target.Title = source.Title
	target.AgentType = source.AgentType
	target.Command = source.Command
	target.Width = source.Width
	target.Height = source.Height
}

func applyPaneChange(target *PaneState, change PaneChange) {
	if change.AgentType != nil {
		target.AgentType = *change.AgentType
	}
	if change.Title != nil {
		target.Title = *change.Title
	}
	if change.Command != nil {
		target.Command = *change.Command
	}
	if change.Pane != nil {
		applyPaneMetadata(target, *change.Pane)
	}
	if change.NewLines > 0 {
		target.ScrollbackLines += change.NewLines
	}
	if change.NewLines > 0 || change.DiffFile != "" {
		// Resolved incrementals do not materialize a merged scrollback artifact,
		// so retaining the base path would point at stale content.
		target.ScrollbackFile = ""
		target.Scrollback = nil
	}
}

func normalizeSessionPaneOrder(session *SessionState) {
	if session == nil {
		return
	}
	activePaneID := sessionActivePaneID(*session)
	sortCheckpointPanes(session.Panes)
	sortWindowLayouts(session.WindowLayouts)
	session.ActivePaneIndex = resolvedActivePaneIndex(session.Panes, activePaneID, nil, nil)
}

func sortCheckpointPanes(panes []PaneState) {
	sort.SliceStable(panes, func(i, j int) bool {
		if panes[i].WindowIndex != panes[j].WindowIndex {
			return panes[i].WindowIndex < panes[j].WindowIndex
		}
		if panes[i].Index != panes[j].Index {
			return panes[i].Index < panes[j].Index
		}
		return panes[i].ID < panes[j].ID
	})
}

func sessionActivePaneID(session SessionState) string {
	if session.ActivePaneIndex < 0 || session.ActivePaneIndex >= len(session.Panes) {
		return ""
	}
	return session.Panes[session.ActivePaneIndex].ID
}

func resolvedActivePaneIndex(panes []PaneState, activePaneID string, desiredID *string, desiredIndex *int) int {
	if desiredID != nil && *desiredID != "" {
		for i := range panes {
			if panes[i].ID == *desiredID {
				return i
			}
		}
	}
	if desiredIndex != nil {
		if *desiredIndex < 0 || *desiredIndex >= len(panes) {
			if len(panes) == 0 {
				return 0
			}
			return 0
		}
		return *desiredIndex
	}
	if activePaneID != "" {
		for i := range panes {
			if panes[i].ID == activePaneID {
				return i
			}
		}
	}
	if len(panes) == 0 {
		return 0
	}
	return 0
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
		if pane.ScrollbackFile != "" || pane.ScrollbackLines > 0 {
			scrollback, err := storage.LoadPaneScrollback(inc.SessionName, inc.BaseCheckpointID, pane)
			if err != nil {
				return 0, 0, err
			}
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
