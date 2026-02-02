package checkpoint

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/privacy"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Capturer handles capturing session state for checkpoints.
type Capturer struct {
	storage *Storage
}

// NewCapturer creates a new Capturer with the default storage.
func NewCapturer() *Capturer {
	return &Capturer{
		storage: NewStorage(),
	}
}

// NewCapturerWithStorage creates a Capturer with a custom storage.
func NewCapturerWithStorage(storage *Storage) *Capturer {
	return &Capturer{
		storage: storage,
	}
}

// Create creates a new checkpoint for the given session.
func (c *Capturer) Create(sessionName, name string, opts ...CheckpointOption) (*Checkpoint, error) {
	options := defaultOptions()
	for _, opt := range opts {
		opt(&options)
	}

	// Check privacy mode before creating checkpoint
	if err := privacy.GetDefaultManager().CanPersist(sessionName, privacy.OpCheckpoint); err != nil {
		return nil, fmt.Errorf("checkpoint blocked: %w", err)
	}

	// Check session exists
	if !tmux.SessionExists(sessionName) {
		return nil, fmt.Errorf("session %q does not exist", sessionName)
	}

	// Generate checkpoint ID
	checkpointID := GenerateID(name)

	// Get working directory from session
	workingDir, err := getSessionDir(sessionName)
	if err != nil {
		workingDir = ""
	}

	// Capture session state
	sessionState, err := c.captureSessionState(sessionName)
	if err != nil {
		return nil, fmt.Errorf("capturing session state: %w", err)
	}

	// Create checkpoint structure
	cp := &Checkpoint{
		Version:     CurrentVersion,
		ID:          checkpointID,
		Name:        name,
		Description: options.description,
		SessionName: sessionName,
		WorkingDir:  workingDir,
		CreatedAt:   time.Now(),
		Session:     sessionState,
		PaneCount:   len(sessionState.Panes),
	}

	// Save checkpoint first so directory exists
	if err := c.storage.Save(cp); err != nil {
		return nil, fmt.Errorf("saving checkpoint: %w", err)
	}

	// Capture pane scrollback with compression support
	scrollbackConfig := ScrollbackConfig{
		Lines:     options.scrollbackLines,
		Compress:  options.scrollbackCompress,
		MaxSizeMB: options.scrollbackMaxSizeMB,
		Timeout:   30 * time.Second,
	}
	if err := c.captureScrollbackEnhanced(cp, scrollbackConfig); err != nil {
		// Non-fatal, continue
		slog.Warn("failed to capture some scrollback", "error", err)
	}

	// Capture git state if enabled and in a git repo
	if options.captureGit && workingDir != "" {
		gitState, err := c.captureGitState(workingDir, sessionName, checkpointID)
		if err != nil {
			slog.Warn("failed to capture git state", "error", err)
		} else {
			cp.Git = gitState
		}
	}

	// Capture assignment state if enabled (bd-32ck)
	if options.captureAssignments {
		assignments, err := c.captureAssignments(sessionName)
		if err != nil {
			slog.Warn("failed to capture assignments", "error", err)
		} else if len(assignments) > 0 {
			cp.Assignments = assignments
		}
	}

	// Capture BV snapshot if enabled (bd-32ck)
	if options.captureBVSnapshot && workingDir != "" {
		bvSnapshot, err := c.captureBVSnapshot(workingDir)
		if err != nil {
			slog.Warn("failed to capture BV snapshot", "error", err)
		} else if bvSnapshot != nil {
			cp.BVSummary = bvSnapshot
		}
	}

	// Save updated checkpoint with all state
	if err := c.storage.Save(cp); err != nil {
		return nil, fmt.Errorf("saving final checkpoint: %w", err)
	}

	return cp, nil
}

// captureSessionState captures the current state of a tmux session.
func (c *Capturer) captureSessionState(sessionName string) (SessionState, error) {
	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		return SessionState{}, fmt.Errorf("getting panes: %w", err)
	}

	var paneStates []PaneState
	activeIndex := 0

	for i, p := range panes {
		state := FromTmuxPane(p)
		if p.Active {
			activeIndex = i
		}
		paneStates = append(paneStates, state)
	}

	// Get layout string
	layout, err := getSessionLayout(sessionName)
	if err != nil {
		slog.Warn("failed to capture session layout", "error", err)
	}

	return SessionState{
		Panes:           paneStates,
		Layout:          layout,
		ActivePaneIndex: activeIndex,
	}, nil
}

// captureGitState captures the git repository state.
func (c *Capturer) captureGitState(workingDir, sessionName, checkpointID string) (GitState, error) {
	state := GitState{}

	// Check if it's a git repository
	if !isGitRepo(workingDir) {
		return state, nil
	}

	// Get current branch
	branch, err := gitCommand(workingDir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return state, fmt.Errorf("getting git branch: %w", err)
	}
	state.Branch = strings.TrimSpace(branch)

	// Get current commit
	commit, err := gitCommand(workingDir, "rev-parse", "HEAD")
	if err != nil {
		return state, fmt.Errorf("getting git commit: %w", err)
	}
	state.Commit = strings.TrimSpace(commit)

	// Get status counts
	status, err := gitCommand(workingDir, "status", "--porcelain")
	if err != nil {
		return state, fmt.Errorf("getting git status: %w", err)
	}
	state.StagedCount, state.UnstagedCount, state.UntrackedCount = parseGitStatus(status)
	state.IsDirty = (state.StagedCount + state.UnstagedCount + state.UntrackedCount) > 0

	// Save git status text
	statusText, _ := gitCommand(workingDir, "status")
	if statusText != "" {
		c.storage.SaveGitStatus(sessionName, checkpointID, statusText)
	}

	// Capture uncommitted changes as patch
	if state.IsDirty {
		// Warn about untracked files if any
		if state.UntrackedCount > 0 {
			slog.Warn("untracked files will not be captured in git patch", "count", state.UntrackedCount)
		}

		// Get diff of tracked changes (both staged and unstaged)
		patch, err := gitCommand(workingDir, "diff", "HEAD")
		if err != nil {
			return state, fmt.Errorf("getting git diff: %w", err)
		}
		if patch != "" {
			if err := c.storage.SaveGitPatch(sessionName, checkpointID, patch); err == nil {
				state.PatchFile = GitPatchFile
			}
		}
	}

	return state, nil
}

// getSessionDir gets the working directory for a session.
func getSessionDir(sessionName string) (string, error) {
	cmd := exec.Command(tmux.BinaryPath(), "display-message", "-p", "-t", sessionName, "#{pane_current_path}")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

// getSessionLayout gets the tmux layout string for a session.
func getSessionLayout(sessionName string) (string, error) {
	cmd := exec.Command(tmux.BinaryPath(), "display-message", "-p", "-t", sessionName, "#{window_layout}")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

// isGitRepo checks if a directory is a git repository.
func isGitRepo(dir string) bool {
	gitDir := filepath.Join(dir, ".git")
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--git-dir")
	return cmd.Run() == nil || fileExists(gitDir)
}

// MaxGitOutputBytes limits the size of git command output to prevent OOM.
// 10MB is sufficient for most status/diff operations while preventing abuse.
const MaxGitOutputBytes = 10 * 1024 * 1024

// gitCommand runs a git command in the specified directory with output size limit.
func gitCommand(dir string, args ...string) (string, error) {
	allArgs := append([]string{"-C", dir}, args...)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", allArgs...)

	// Capture stderr separately for error reporting
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("starting git %s: %w", strings.Join(args, " "), err)
	}

	// Read up to limit + 1 byte to detect truncation
	data, err := io.ReadAll(io.LimitReader(stdoutPipe, MaxGitOutputBytes+1))
	if err != nil {
		return "", fmt.Errorf("reading git output: %w", err)
	}

	// Check for truncation
	if len(data) > MaxGitOutputBytes {
		_ = cmd.Process.Kill() // Kill process if it's spewing too much data
		return "", fmt.Errorf("git output exceeded limit of %d bytes", MaxGitOutputBytes)
	}

	if err := cmd.Wait(); err != nil {
		// Verify context error first
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("git command timed out")
		}
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}

	return string(data), nil
}

// parseGitStatus parses git status --porcelain output.
func parseGitStatus(status string) (staged, unstaged, untracked int) {
	// Only trim trailing newlines, not leading spaces which are significant in porcelain format
	lines := strings.Split(strings.TrimRight(status, "\n"), "\n")
	for _, line := range lines {
		if len(line) < 2 {
			continue
		}
		// First char is index status, second is worktree status
		indexStatus := line[0]
		worktreeStatus := line[1]

		switch {
		case line[0:2] == "??":
			untracked++
		case indexStatus != ' ' && indexStatus != '?':
			staged++
		}

		if worktreeStatus != ' ' && worktreeStatus != '?' && indexStatus != '?' {
			unstaged++
		}
	}
	return
}

// countLines counts the number of lines in a string.
// Empty strings return 0, trailing newlines don't count as extra lines.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	// Remove trailing newline to avoid counting an empty final line
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return 0 // String was just a newline
	}
	return len(strings.Split(s, "\n"))
}

// fileExists checks if a path exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// FindByPattern finds checkpoints matching a pattern (prefix match or exact).
func (c *Capturer) FindByPattern(sessionName, pattern string) ([]*Checkpoint, error) {
	all, err := c.storage.List(sessionName)
	if err != nil {
		return nil, err
	}

	var matches []*Checkpoint
	for _, cp := range all {
		// Match by ID prefix or name
		if strings.HasPrefix(cp.ID, pattern) ||
			strings.EqualFold(cp.Name, pattern) ||
			matchWildcard(cp.Name, pattern) {
			matches = append(matches, cp)
		}
	}

	return matches, nil
}

// matchWildcard performs simple wildcard matching (* only).
func matchWildcard(s, pattern string) bool {
	if !strings.Contains(pattern, "*") {
		return strings.EqualFold(s, pattern)
	}

	// Convert to regex
	regexPattern := "(?i)^" + regexp.QuoteMeta(pattern) + "$"
	regexPattern = strings.ReplaceAll(regexPattern, `\*`, ".*")

	matched, _ := regexp.MatchString(regexPattern, s)
	return matched
}

// GetLatest returns the most recent checkpoint for a session.
func (c *Capturer) GetLatest(sessionName string) (*Checkpoint, error) {
	return c.storage.GetLatest(sessionName)
}

// List returns all checkpoints for a session.
func (c *Capturer) List(sessionName string) ([]*Checkpoint, error) {
	return c.storage.List(sessionName)
}

// GetByIndex returns the Nth most recent checkpoint (1-indexed, 1 = latest).
func (c *Capturer) GetByIndex(sessionName string, index int) (*Checkpoint, error) {
	checkpoints, err := c.storage.List(sessionName)
	if err != nil {
		return nil, err
	}

	if index < 1 || index > len(checkpoints) {
		return nil, fmt.Errorf("checkpoint index %d out of range (1-%d)", index, len(checkpoints))
	}

	return checkpoints[index-1], nil
}

// ParseCheckpointRef parses a checkpoint reference which can be:
// - A checkpoint ID (timestamp-name)
// - A checkpoint name
// - "~N" for Nth most recent (e.g., "~1" = latest, "~2" = second latest)
// - "last" or "latest" for the most recent
func (c *Capturer) ParseCheckpointRef(sessionName, ref string) (*Checkpoint, error) {
	ref = strings.TrimSpace(ref)

	// Handle special keywords
	switch strings.ToLower(ref) {
	case "last", "latest", "~1", "~":
		return c.GetLatest(sessionName)
	}

	// Handle ~N notation
	if strings.HasPrefix(ref, "~") {
		indexStr := strings.TrimPrefix(ref, "~")
		index, err := strconv.Atoi(indexStr)
		if err != nil {
			return nil, fmt.Errorf("invalid checkpoint reference: %s", ref)
		}
		return c.GetByIndex(sessionName, index)
	}

	// Try exact match by ID
	if c.storage.Exists(sessionName, ref) {
		return c.storage.Load(sessionName, ref)
	}

	// Try pattern match
	matches, err := c.FindByPattern(sessionName, ref)
	if err != nil {
		return nil, err
	}

	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no checkpoint found matching: %s", ref)
	case 1:
		return matches[0], nil
	default:
		return nil, fmt.Errorf("ambiguous checkpoint reference %q matches %d checkpoints", ref, len(matches))
	}
}

// captureAssignments captures bead-to-agent assignment state for a session (bd-32ck).
func (c *Capturer) captureAssignments(sessionName string) ([]AssignmentSnapshot, error) {
	store, err := assignment.LoadStore(sessionName)
	if err != nil {
		return nil, fmt.Errorf("loading assignment store: %w", err)
	}

	active := store.ListActive()
	if len(active) == 0 {
		return nil, nil
	}

	snapshots := make([]AssignmentSnapshot, 0, len(active))
	for _, a := range active {
		snapshots = append(snapshots, AssignmentSnapshot{
			BeadID:     a.BeadID,
			BeadTitle:  a.BeadTitle,
			Pane:       a.Pane,
			AgentType:  a.AgentType,
			AgentName:  a.AgentName,
			Status:     string(a.Status),
			AssignedAt: a.AssignedAt,
		})
	}

	return snapshots, nil
}

// captureBVSnapshot captures BV triage summary at checkpoint time (bd-32ck).
func (c *Capturer) captureBVSnapshot(workingDir string) (*BVSnapshot, error) {
	quickRef, err := bv.GetTriageQuickRef(workingDir)
	if err != nil {
		return nil, fmt.Errorf("getting triage quick ref: %w", err)
	}
	if quickRef == nil {
		return nil, nil
	}

	// Extract top pick IDs
	var topPicks []string
	for _, pick := range quickRef.TopPicks {
		topPicks = append(topPicks, pick.ID)
	}

	return &BVSnapshot{
		OpenCount:       quickRef.OpenCount,
		ActionableCount: quickRef.ActionableCount,
		BlockedCount:    quickRef.BlockedCount,
		InProgressCount: quickRef.InProgressCount,
		TopPicks:        topPicks,
		CapturedAt:      time.Now(),
	}, nil
}
