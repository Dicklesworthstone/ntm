package checkpoint

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Restore errors
var (
	ErrSessionExists     = errors.New("session already exists (use Force option to override)")
	ErrDirectoryNotFound = errors.New("checkpoint working directory not found")
	ErrWorkingDirInvalid = errors.New("checkpoint working directory is invalid")
	ErrNoAgentsToRestore = errors.New("checkpoint contains no agents to restore")
	ErrNilCheckpoint     = errors.New("checkpoint is nil")
)

var errWorkingDirNotDirectory = errors.New("not a directory")

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
	if cp == nil {
		return nil, ErrNilCheckpoint
	}

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

	// Validate working directory exists and is usable.
	if workDir != "" {
		if err := validateWorkingDirectory(workDir); err != nil {
			if opts.DryRun {
				result.Warnings = append(result.Warnings, workingDirectoryIssue(workDir, err))
			} else {
				if errors.Is(err, os.ErrNotExist) {
					return nil, fmt.Errorf("%w: %s", ErrDirectoryNotFound, workDir)
				}
				return nil, fmt.Errorf("%w: %s: %v", ErrWorkingDirInvalid, workDir, err)
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
	restoreDir := effectiveRestoreDir(workDir)

	if opts.DryRun {
		// Simulate what would happen
		result.PanesRestored = len(cp.Session.Panes)
		result.ContextInjected = opts.InjectContext
		return result, nil
	}

	// Create the session
	if err := r.createSession(cp, restoreDir); err != nil {
		return nil, fmt.Errorf("creating session: %w", err)
	}

	// Create additional panes to match checkpoint layout
	panesCreated, err := r.restoreLayout(cp, restoreDir)
	if err != nil {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("layout restoration incomplete: %v", err))
	}
	result.PanesRestored = panesCreated

	if err := r.restoreAgents(cp, restoreDir); err != nil {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("agent restoration incomplete: %v", err))
	}

	// Inject context if requested
	if opts.InjectContext {
		if err := r.injectContext(cp, opts.ScrollbackLines); err != nil {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("context injection failed: %v", err))
		} else {
			result.ContextInjected = true
		}
	}

	if err := r.restoreActivePane(cp); err != nil {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("active pane restoration failed: %v", err))
	}

	return result, nil
}

// createSession creates the initial tmux session.
func (r *Restorer) createSession(cp *Checkpoint, workDir string) error {
	if err := tmux.CreateSession(cp.SessionName, workDir); err != nil {
		return err
	}

	// Wait for session to be ready
	time.Sleep(100 * time.Millisecond)

	// Set the title of the first pane if we have pane info
	panes := sortedCheckpointPanes(cp.Session.Panes)
	if len(panes) > 0 {
		firstPane := panes[0]
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
	paneStates := sortedCheckpointPanes(cp.Session.Panes)
	if len(paneStates) == 0 {
		return 0, nil
	}

	// First pane was created with the session, so we start at 1.
	panesCreated := 1
	lastWindowIndex := paneStates[0].WindowIndex

	for i := 1; i < len(paneStates); i++ {
		paneState := paneStates[i]

		var (
			paneID string
			err    error
		)

		if paneState.WindowIndex != lastWindowIndex {
			paneID, err = tmux.DefaultClient.Run(
				"new-window",
				"-P",
				"-F",
				"#{pane_id}",
				"-t",
				cp.SessionName,
				"-c",
				workDir,
			)
			lastWindowIndex = paneState.WindowIndex
		} else {
			paneID, err = tmux.DefaultClient.Run(
				"split-window",
				"-t",
				cp.SessionName,
				"-c",
				workDir,
				"-P",
				"-F",
				"#{pane_id}",
			)
		}
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

func (r *Restorer) restoreAgents(cp *Checkpoint, workDir string) error {
	panes, err := tmux.GetPanes(cp.SessionName)
	if err != nil {
		return fmt.Errorf("getting panes: %w", err)
	}

	sortedStates := sortedCheckpointPanes(cp.Session.Panes)
	sortedPanes := sortedTmuxPanes(panes)
	attempted := 0
	launched := 0

	for i, paneState := range sortedStates {
		if i >= len(sortedPanes) {
			break
		}

		agentCmd := restorableAgentCommand(paneState)
		if agentCmd == "" {
			continue
		}

		attempted++
		cmd, err := tmux.BuildPaneCommand(workDir, agentCmd)
		if err != nil {
			slog.Warn("checkpoint restore: failed to build pane command",
				"session", cp.SessionName,
				"pane_index", paneState.Index,
				"window_index", paneState.WindowIndex,
				"agent_type", paneState.AgentType,
				"error", err)
			continue
		}

		if err := tmux.SendKeysForAgent(sortedPanes[i].ID, cmd, true, tmux.AgentType(paneState.AgentType)); err != nil {
			slog.Warn("checkpoint restore: failed to relaunch pane command",
				"session", cp.SessionName,
				"pane_index", paneState.Index,
				"window_index", paneState.WindowIndex,
				"agent_type", paneState.AgentType,
				"command", agentCmd,
				"error", err)
			continue
		}
		launched++
	}

	if attempted > 0 && launched == 0 {
		return fmt.Errorf("all %d agent launch attempts failed", attempted)
	}
	if launched != attempted {
		return fmt.Errorf("launched %d of %d agent panes", launched, attempted)
	}
	return nil
}

func (r *Restorer) restoreActivePane(cp *Checkpoint) error {
	if cp.Session.ActivePaneIndex < 0 || cp.Session.ActivePaneIndex >= len(cp.Session.Panes) {
		return nil
	}

	panes, err := tmux.GetPanes(cp.SessionName)
	if err != nil {
		return fmt.Errorf("getting panes: %w", err)
	}
	targetPane, ok := restoredPaneForCheckpointIndex(cp, panes, cp.Session.ActivePaneIndex)
	if !ok {
		return nil
	}
	return tmux.DefaultClient.RunSilent("select-pane", "-t", targetPane.ID)
}

// applyLayout applies a tmux layout string to a session.
func (r *Restorer) applyLayout(sessionName, layout string) error {
	if layout == "" {
		layout = "tiled"
	}

	output, err := tmux.DefaultClient.Run("list-windows", "-t", sessionName, "-F", "#{window_index}")
	if err != nil {
		return err
	}

	for _, win := range strings.Split(strings.TrimSpace(output), "\n") {
		if win == "" {
			continue
		}
		target := fmt.Sprintf("%s:%s", sessionName, win)
		if err := tmux.DefaultClient.RunSilent("select-layout", "-t", target, layout); err != nil {
			return err
		}
	}

	return nil
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

		targetPane, ok := restoredPaneForCheckpointIndex(cp, panes, i)
		if !ok {
			continue
		}

		// Load scrollback content
		content, err := r.loadPaneScrollback(cp.SessionName, cp.ID, paneState.ID)
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
		if err := tmux.SendBuffer(targetPane.ID, contextMsg, true); err != nil {
			lastErr = err
		}
	}

	return lastErr
}

func (r *Restorer) loadPaneScrollback(sessionName, checkpointID, paneID string) (string, error) {
	return r.storage.LoadCompressedScrollback(sessionName, checkpointID, paneID)
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
	return strings.Join(lines, "\n")
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

func validateWorkingDirectory(workDir string) error {
	info, err := os.Stat(workDir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errWorkingDirNotDirectory
	}
	return nil
}

func workingDirectoryIssue(workDir string, err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Sprintf("working directory not found: %s", workDir)
	case errors.Is(err, errWorkingDirNotDirectory):
		return fmt.Sprintf("working directory is not a directory: %s", workDir)
	default:
		return fmt.Sprintf("working directory inaccessible: %s (%v)", workDir, err)
	}
}

func effectiveRestoreDir(workDir string) string {
	if strings.TrimSpace(workDir) == "" {
		return os.TempDir()
	}
	return workDir
}

func sortedCheckpointPanes(panes []PaneState) []PaneState {
	sorted := make([]PaneState, len(panes))
	copy(sorted, panes)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].WindowIndex != sorted[j].WindowIndex {
			return sorted[i].WindowIndex < sorted[j].WindowIndex
		}
		return sorted[i].Index < sorted[j].Index
	})
	return sorted
}

func sortedTmuxPanes(panes []tmux.Pane) []tmux.Pane {
	sorted := make([]tmux.Pane, len(panes))
	copy(sorted, panes)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].WindowIndex != sorted[j].WindowIndex {
			return sorted[i].WindowIndex < sorted[j].WindowIndex
		}
		return sorted[i].Index < sorted[j].Index
	})
	return sorted
}

func restoredPaneForCheckpointIndex(cp *Checkpoint, panes []tmux.Pane, checkpointIndex int) (tmux.Pane, bool) {
	if checkpointIndex < 0 || checkpointIndex >= len(cp.Session.Panes) {
		return tmux.Pane{}, false
	}

	sortedPanes := sortedTmuxPanes(panes)
	restoredIndex := restoredPaneIndexForCheckpointIndex(cp.Session.Panes, checkpointIndex)
	if restoredIndex < 0 || restoredIndex >= len(sortedPanes) {
		return tmux.Pane{}, false
	}
	return sortedPanes[restoredIndex], true
}

func restoredPaneIndexForCheckpointIndex(checkpointPanes []PaneState, checkpointIndex int) int {
	if checkpointIndex < 0 || checkpointIndex >= len(checkpointPanes) {
		return -1
	}

	target := checkpointPanes[checkpointIndex]
	sorted := sortedCheckpointPanes(checkpointPanes)
	for i, candidate := range sorted {
		if sameCheckpointPane(target, candidate) {
			return i
		}
	}
	return -1
}

func sameCheckpointPane(a, b PaneState) bool {
	if a.ID != "" && b.ID != "" {
		return a.ID == b.ID
	}
	return a.WindowIndex == b.WindowIndex &&
		a.Index == b.Index &&
		a.Title == b.Title &&
		a.AgentType == b.AgentType
}

func restorableAgentCommand(pane PaneState) string {
	agentType := agent.AgentType(pane.AgentType).Canonical()
	if !agentType.IsValid() || agentType == agent.AgentTypeUser || agentType == agent.AgentTypeUnknown {
		return ""
	}

	command := strings.TrimSpace(pane.Command)
	if command != "" && !looksLikeShellCommand(command) {
		return command
	}

	switch agentType {
	case agent.AgentTypeClaudeCode:
		return "claude"
	case agent.AgentTypeCodex:
		return "codex"
	case agent.AgentTypeGemini:
		return "gemini"
	case agent.AgentTypeCursor:
		return "cursor"
	case agent.AgentTypeWindsurf:
		return "windsurf"
	case agent.AgentTypeAider:
		return "aider"
	case agent.AgentTypeOllama:
		return "ollama"
	default:
		return ""
	}
}

func looksLikeShellCommand(command string) bool {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}

	name := strings.ToLower(filepath.Base(fields[0]))
	switch name {
	case "sh", "bash", "zsh", "fish", "dash", "ksh", "csh", "tcsh":
		return true
	default:
		return false
	}
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
	if cp == nil {
		return []string{ErrNilCheckpoint.Error()}
	}

	var issues []string

	// Check working directory
	workDir := cp.WorkingDir
	if opts.CustomDirectory != "" {
		workDir = opts.CustomDirectory
	}
	if workDir != "" {
		if err := validateWorkingDirectory(workDir); err != nil {
			issues = append(issues, workingDirectoryIssue(workDir, err))
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
