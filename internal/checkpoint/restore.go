package checkpoint

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Restore errors
var (
	ErrSessionExists     = errors.New("session already exists (use Force option to override)")
	ErrDirectoryNotFound = errors.New("checkpoint working directory not found")
	ErrNoAgentsToRestore = errors.New("checkpoint contains no agents to restore")
)

// RestoreOptions configures how a checkpoint is restored.
type RestoreOptions struct {
	// Force kills any existing session with the same name
	Force bool
	// SkipGitCheck skips warning about git state mismatch
	SkipGitCheck bool
	// InjectContext sends scrollback/summary to agents after spawning
	InjectContext bool
	// DryRun shows what would be done without making changes
	DryRun bool
	// CustomDirectory overrides the checkpoint's working directory
	CustomDirectory string
	// ScrollbackLines is how many lines of scrollback to inject (0 = all captured)
	ScrollbackLines int
}

// RestoreResult contains details about what was restored.
type RestoreResult struct {
	// SessionName is the restored session name
	SessionName string
	// PanesRestored is the number of panes created
	PanesRestored int
	// ContextInjected indicates if scrollback was sent to agents
	ContextInjected bool
	// Warnings contains non-fatal issues encountered
	Warnings []string
	// DryRun indicates this was a simulation
	DryRun bool

	// Assignments contains bead-to-agent assignment state from the checkpoint (bd-32ck).
	// Empty if no assignments were captured.
	Assignments []AssignmentSnapshot
	// BVSummary contains BV triage summary from the checkpoint (bd-32ck).
	// Nil if no BV snapshot was captured.
	BVSummary *BVSnapshot
}

// Restorer handles checkpoint restoration.
type Restorer struct {
	storage *Storage
}

// NewRestorer creates a new Restorer with default storage.
func NewRestorer() *Restorer {
	return &Restorer{
		storage: NewStorage(),
	}
}

// NewRestorerWithStorage creates a Restorer with custom storage.
func NewRestorerWithStorage(storage *Storage) *Restorer {
	return &Restorer{
		storage: storage,
	}
}

// Restore restores a session from a checkpoint.
func (r *Restorer) Restore(sessionName, checkpointID string, opts RestoreOptions) (*RestoreResult, error) {
	// Load checkpoint
	cp, err := r.storage.Load(sessionName, checkpointID)
	if err != nil {
		return nil, fmt.Errorf("loading checkpoint: %w", err)
	}

	return r.RestoreFromCheckpoint(cp, opts)
}

// RestoreFromCheckpoint restores a session from a loaded checkpoint.
func (r *Restorer) RestoreFromCheckpoint(cp *Checkpoint, opts RestoreOptions) (*RestoreResult, error) {
	result := &RestoreResult{
		SessionName: cp.SessionName,
		DryRun:      opts.DryRun,
		Assignments: cp.Assignments,
		BVSummary:   cp.BVSummary,
	}

	// Surface assignment and BV summary from checkpoint (bd-32ck)
	if len(cp.Assignments) > 0 {
		slog.Info("checkpoint contains assignments",
			"session", cp.SessionName,
			"assignment_count", len(cp.Assignments))
	}
	if cp.BVSummary != nil {
		slog.Info("checkpoint contains BV summary",
			"session", cp.SessionName,
			"actionable", cp.BVSummary.ActionableCount,
			"blocked", cp.BVSummary.BlockedCount,
			"in_progress", cp.BVSummary.InProgressCount)
	}

	// Determine working directory
	workDir := cp.WorkingDir
	if opts.CustomDirectory != "" {
		workDir = opts.CustomDirectory
	}

	// Validate working directory exists
	if workDir != "" {
		if _, err := os.Stat(workDir); os.IsNotExist(err) {
			if opts.DryRun {
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("working directory %q does not exist", workDir))
			} else {
				return nil, fmt.Errorf("%w: %s", ErrDirectoryNotFound, workDir)
			}
		}
	}

	// Check for existing session
	if tmux.SessionExists(cp.SessionName) {
		if !opts.Force {
			return nil, ErrSessionExists
		}
		if !opts.DryRun {
			if err := tmux.KillSession(cp.SessionName); err != nil {
				return nil, fmt.Errorf("killing existing session: %w", err)
			}
			// Wait for session to be fully killed
			time.Sleep(100 * time.Millisecond)
		} else {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("would kill existing session %q", cp.SessionName))
		}
	}

	// Validate we have panes to restore
	if len(cp.Session.Panes) == 0 {
		return nil, ErrNoAgentsToRestore
	}

	// Check git state if requested
	if !opts.SkipGitCheck && cp.Git.Commit != "" && workDir != "" {
		if warning := r.checkGitState(cp, workDir); warning != "" {
			result.Warnings = append(result.Warnings, warning)
		}
	}

	if opts.DryRun {
		// Simulate what would happen
		result.PanesRestored = len(cp.Session.Panes)
		result.ContextInjected = opts.InjectContext
		return result, nil
	}

	// Create the session
	if err := r.createSession(cp, workDir); err != nil {
		return nil, fmt.Errorf("creating session: %w", err)
	}

	// Create additional panes to match checkpoint layout
	panesCreated, err := r.restoreLayout(cp, workDir)
	if err != nil {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("layout restoration incomplete: %v", err))
	}
	result.PanesRestored = panesCreated

	// Inject context if requested
	if opts.InjectContext {
		if err := r.injectContext(cp, opts.ScrollbackLines); err != nil {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("context injection failed: %v", err))
		} else {
			result.ContextInjected = true
		}
	}

	return result, nil
}

// createSession creates the initial tmux session.
func (r *Restorer) createSession(cp *Checkpoint, workDir string) error {
	// Default to temp dir if no workDir
	if workDir == "" {
		workDir = os.TempDir()
	}

	if err := tmux.CreateSession(cp.SessionName, workDir); err != nil {
		return err
	}

	// Wait for session to be ready
	time.Sleep(100 * time.Millisecond)

	// Set the title of the first pane if we have pane info
	if len(cp.Session.Panes) > 0 {
		firstPane := cp.Session.Panes[0]
		if firstPane.Title != "" {
			panes, err := tmux.GetPanes(cp.SessionName)
			if err == nil && len(panes) > 0 {
				_ = tmux.SetPaneTitle(panes[0].ID, firstPane.Title)
			}
		}
	}

	return nil
}

// restoreLayout creates additional panes to match the checkpoint layout.
func (r *Restorer) restoreLayout(cp *Checkpoint, workDir string) (int, error) {
	if workDir == "" {
		workDir = os.TempDir()
	}

	// First pane was created with the session, so we start at 1
	panesCreated := 1

	// Create additional panes
	for i := 1; i < len(cp.Session.Panes); i++ {
		paneState := cp.Session.Panes[i]

		// Split to create new pane
		paneID, err := tmux.SplitWindow(cp.SessionName, workDir)
		if err != nil {
			return panesCreated, fmt.Errorf("creating pane %d: %w", i, err)
		}

		// Set pane title to match checkpoint
		if paneState.Title != "" {
			_ = tmux.SetPaneTitle(paneID, paneState.Title)
		}

		panesCreated++

		// Small delay between pane creations for stability
		time.Sleep(50 * time.Millisecond)
	}

	// Apply layout string if available
	if cp.Session.Layout != "" {
		if err := r.applyLayout(cp.SessionName, cp.Session.Layout); err != nil {
			// Non-fatal - layout is a best-effort feature
			return panesCreated, fmt.Errorf("applying layout: %w", err)
		}
	}

	return panesCreated, nil
}

// applyLayout applies a tmux layout string to a session.
func (r *Restorer) applyLayout(sessionName, layout string) error {
	firstWin, err := tmux.GetFirstWindow(sessionName)
	if err != nil {
		return err
	}

	target := fmt.Sprintf("%s:%d", sessionName, firstWin)
	return tmux.DefaultClient.RunSilent("select-layout", "-t", target, layout)
}

// injectContext sends scrollback content to restored agents.
func (r *Restorer) injectContext(cp *Checkpoint, maxLines int) error {
	panes, err := tmux.GetPanes(cp.SessionName)
	if err != nil {
		return fmt.Errorf("getting panes: %w", err)
	}

	var lastErr error
	for i, paneState := range cp.Session.Panes {
		if paneState.ScrollbackFile == "" {
			continue
		}

		// Find corresponding restored pane
		if i >= len(panes) {
			continue
		}
		targetPane := panes[i]

		// Load scrollback content
		content, err := r.storage.LoadScrollback(cp.SessionName, cp.ID, paneState.ID)
		if err != nil {
			lastErr = err
			continue
		}

		// Truncate if maxLines specified
		if maxLines > 0 {
			content = truncateToLines(content, maxLines)
		}

		// Send as context message
		contextMsg := formatContextInjection(content, cp.CreatedAt)
		if err := tmux.SendKeys(targetPane.ID, contextMsg, true); err != nil {
			lastErr = err
		}
	}

	return lastErr
}

// checkGitState compares current git state with checkpoint and returns a warning if different.
func (r *Restorer) checkGitState(cp *Checkpoint, workDir string) string {
	// Check if current branch matches
	branch, err := gitCommand(workDir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "could not determine current git branch"
	}

	currentBranch := trimSpace(branch)
	if currentBranch != cp.Git.Branch {
		return fmt.Sprintf("git branch mismatch: current=%s, checkpoint=%s",
			currentBranch, cp.Git.Branch)
	}

	// Check if commit matches
	commit, err := gitCommand(workDir, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}

	currentCommit := trimSpace(commit)
	if currentCommit != cp.Git.Commit {
		return fmt.Sprintf("git commit mismatch: current=%s, checkpoint=%s",
			shortHash(currentCommit), shortHash(cp.Git.Commit))
	}

	return ""
}

// truncateToLines returns the last N lines of content.
func truncateToLines(content string, maxLines int) string {
	lines := splitLines(content)
	if len(lines) <= maxLines {
		return content
	}
	return joinLines(lines[len(lines)-maxLines:])
}

// splitLines splits content into lines.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// joinLines joins lines back together.
func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	result := lines[0]
	for i := 1; i < len(lines); i++ {
		result += "\n" + lines[i]
	}
	return result
}

// trimSpace removes leading/trailing whitespace.
func trimSpace(s string) string {
	start := 0
	end := len(s)
	for start < end && (s[start] == ' ' || s[start] == '\n' || s[start] == '\r' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\n' || s[end-1] == '\r' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

// formatContextInjection formats scrollback for injection.
func formatContextInjection(content string, checkpointTime time.Time) string {
	header := fmt.Sprintf("# Context from checkpoint (%s ago)\n\n",
		formatDuration(time.Since(checkpointTime)))
	return header + content
}

// shortHash returns the first 8 characters of a hash, or the whole string if shorter.
func shortHash(h string) string {
	if len(h) <= 8 {
		return h
	}
	return h[:8]
}

// formatDuration returns a human-readable duration.
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// RestoreLatest restores the most recent checkpoint for a session.
func (r *Restorer) RestoreLatest(sessionName string, opts RestoreOptions) (*RestoreResult, error) {
	cp, err := r.storage.GetLatest(sessionName)
	if err != nil {
		return nil, fmt.Errorf("getting latest checkpoint: %w", err)
	}
	return r.RestoreFromCheckpoint(cp, opts)
}

// ValidateCheckpoint checks if a checkpoint can be restored.
func (r *Restorer) ValidateCheckpoint(cp *Checkpoint, opts RestoreOptions) []string {
	var issues []string

	// Check working directory
	workDir := cp.WorkingDir
	if opts.CustomDirectory != "" {
		workDir = opts.CustomDirectory
	}
	if workDir != "" {
		if _, err := os.Stat(workDir); os.IsNotExist(err) {
			issues = append(issues, fmt.Sprintf("working directory not found: %s", workDir))
		}
	}

	// Check session existence
	if tmux.SessionExists(cp.SessionName) {
		if opts.Force {
			issues = append(issues, fmt.Sprintf("session %q exists and will be killed", cp.SessionName))
		} else {
			issues = append(issues, fmt.Sprintf("session %q already exists", cp.SessionName))
		}
	}

	// Check panes
	if len(cp.Session.Panes) == 0 {
		issues = append(issues, "checkpoint contains no panes")
	}

	// Check git state
	if !opts.SkipGitCheck && cp.Git.Commit != "" && workDir != "" {
		if warning := r.checkGitState(cp, workDir); warning != "" {
			issues = append(issues, warning)
		}
	}

	// Check scrollback files if context injection is requested
	if opts.InjectContext {
		for _, pane := range cp.Session.Panes {
			if pane.ScrollbackFile != "" {
				baseDir := r.storage.CheckpointDir(cp.SessionName, cp.ID)
				scrollbackPath, err := resolveCheckpointRelativePath(baseDir, pane.ScrollbackFile)
				if err != nil {
					issues = append(issues,
						fmt.Sprintf("invalid scrollback path for pane %s: %v", pane.ID, err))
					continue
				}
				if _, err := os.Stat(scrollbackPath); os.IsNotExist(err) {
					issues = append(issues,
						fmt.Sprintf("scrollback file missing for pane %s", pane.ID))
				}
			}
		}
	}

	return issues
}
