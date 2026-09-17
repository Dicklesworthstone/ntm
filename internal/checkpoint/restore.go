package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
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
	// Stage identifies the last attempted restoration phase, including on error.
	Stage string
	// Interrupted means cancellation stopped further work, not that prior
	// mutations were undone. A partially restored session is left for inspection.
	Interrupted bool

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
	// ctx is set only on an operation-local copy, never on a shared Restorer.
	// This keeps one implementation for the context-aware and convenience APIs.
	ctx context.Context
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

// RestoreFromCheckpoint restores a session from a loaded checkpoint.
func (r *Restorer) RestoreFromCheckpoint(cp *Checkpoint, opts RestoreOptions) (*RestoreResult, error) {
	return r.RestoreFromCheckpointContext(context.Background(), cp, opts)
}

// RestoreFromCheckpointContext restores under the caller's cancellation and
// deadline. Cancellation prevents subsequent mutations; it does not kill agents
// already restored or roll back the operator-authorized replacement of a session.
// On a partial failure, the result describes progress and the error remains fatal.
func (r *Restorer) RestoreFromCheckpointContext(ctx context.Context, cp *Checkpoint, opts RestoreOptions) (*RestoreResult, error) {
	if ctx == nil {
		return nil, errors.New("restore context is required")
	}
	op := *r
	op.ctx = ctx
	return op.restoreFromCheckpoint(cp, opts)
}

func (r *Restorer) operationContext() context.Context {
	if r.ctx != nil {
		return r.ctx
	}
	return context.Background()
}

func waitForRestore(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

func (r *Restorer) restoreFromCheckpoint(cp *Checkpoint, opts RestoreOptions) (result *RestoreResult, retErr error) {
	ctx := r.operationContext()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cp == nil {
		return nil, ErrNilCheckpoint
	}
	// Validate required identity, panes, and launch commands before --force can
	// kill an existing session. An empty checkpoint is not a restore.
	if err := tmux.ValidateSessionName(cp.SessionName); err != nil {
		return nil, fmt.Errorf("invalid checkpoint session name: %w", err)
	}
	if len(cp.Session.Panes) == 0 {
		return nil, ErrNoAgentsToRestore
	}
	if opts.ScrollbackLines < 0 {
		return nil, errors.New("scrollback lines must be non-negative")
	}
	if err := validateCheckpointAutomatedRelaunch(cp); err != nil {
		return nil, err
	}
	for _, pane := range cp.Session.Panes {
		if command := restorableAgentCommand(pane); command != "" {
			if _, err := tmux.SanitizePaneCommand(command); err != nil {
				return nil, fmt.Errorf("checkpoint pane %d command: %w", pane.Index, err)
			}
			if expectedPaneCommand(command) == "" {
				return nil, fmt.Errorf("checkpoint pane %d has no executable command", pane.Index)
			}
		}
	}

	result = &RestoreResult{
		SessionName: cp.SessionName,
		DryRun:      opts.DryRun,
		Assignments: cp.Assignments,
		BVSummary:   cp.BVSummary,
		Stage:       "validating",
	}
	defer func() {
		if err := ctx.Err(); err != nil {
			retErr = errors.Join(retErr, err)
			if result != nil {
				result.Interrupted = true
			}
		}
	}()

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
	// Advisory git inspection is read-only and also precedes replacement.
	if !opts.SkipGitCheck && cp.Git.Commit != "" && workDir != "" {
		if warning := r.checkGitState(cp, workDir); warning != "" {
			result.Warnings = append(result.Warnings, warning)
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	// Do not mistake an unavailable tmux server/probe error for absence.
	exists, err := tmux.DefaultClient.SessionExistsContext(ctx, cp.SessionName)
	if err != nil {
		if !opts.DryRun || ctx.Err() != nil {
			return result, fmt.Errorf("checking existing session: %w", err)
		}
		result.Warnings = append(result.Warnings, fmt.Sprintf("could not check existing session: %v", err))
	}
	if exists {
		if !opts.Force {
			return nil, ErrSessionExists
		}
		if !opts.DryRun {
			result.Stage = "stopping_existing_session"
			if err := tmux.DefaultClient.RunSilentContext(ctx, "kill-session", "-t", tmux.TargetSession(cp.SessionName)); err != nil {
				return result, fmt.Errorf("killing existing session: %w", err)
			}
			if err := waitForRestore(ctx, 100*time.Millisecond); err != nil {
				return result, err
			}
		} else {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("would kill existing session %q", cp.SessionName))
		}
	}
	restoreDir := effectiveRestoreDir(workDir)

	if opts.DryRun {
		result.PanesRestored = len(cp.Session.Panes)
		result.ContextInjected = opts.InjectContext
		result.Stage = "validated"
		return result, nil
	}

	result.Stage = "creating_session"
	if err := r.createSession(cp, restoreDir); err != nil {
		return result, fmt.Errorf("creating session (inspect for partial creation): %w", err)
	}
	result.PanesRestored = 1
	if err := ctx.Err(); err != nil {
		return result, err
	}

	result.Stage = "restoring_layout"
	panesCreated, err := r.restoreLayout(cp, restoreDir)
	result.PanesRestored = panesCreated
	if err != nil {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("layout restoration incomplete: %v", err))
		// Layout aesthetics are best-effort, but missing panes or cancellation
		// cannot be reported as a fully restored session.
		if ctx.Err() != nil || panesCreated != len(cp.Session.Panes) {
			return result, err
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	result.Stage = "starting_agents"
	if err := r.restoreAgents(cp, restoreDir); err != nil {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("agent restoration incomplete: %v", err))
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	if opts.InjectContext {
		result.Stage = "injecting_context"
		if err := r.injectContext(cp, opts.ScrollbackLines); err != nil {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("context injection failed: %v", err))
			return result, err
		}
		result.ContextInjected = true
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	result.Stage = "selecting_active_pane"
	if err := r.restoreActivePane(cp); err != nil {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("active pane restoration failed: %v", err))
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	result.Stage = "completed"
	return result, nil
}

func validateCheckpointAutomatedRelaunch(cp *Checkpoint) error {
	for _, pane := range cp.Session.Panes {
		if err := agent.AgentType(pane.AgentType).ValidateAutomatedRelaunch(); err != nil {
			return fmt.Errorf("checkpoint pane %d (%s): %w", pane.Index, pane.AgentType, err)
		}
	}
	return nil
}

// createSession creates the initial tmux session.
func (r *Restorer) createSession(cp *Checkpoint, workDir string) error {
	ctx := r.operationContext()
	if err := tmux.DefaultClient.CreateSessionContext(ctx, cp.SessionName, workDir); err != nil {
		return err
	}
	if err := waitForRestore(ctx, 100*time.Millisecond); err != nil {
		return err
	}

	// Restore the first pane's title and durable type before relaunching it.
	panes := sortedCheckpointPanes(cp.Session.Panes)
	if len(panes) > 0 {
		if err := moveInitialWindow(ctx, cp.SessionName, panes[0].WindowIndex); err != nil {
			return err
		}
		firstPane := panes[0]
		firstAgentType := tmux.ParsePaneAgentTypeOption(firstPane.AgentType)
		if firstPane.Title == "" && (firstAgentType == tmux.AgentUnknown || firstAgentType == tmux.AgentUser) {
			return nil
		}
		livePanes, err := tmux.DefaultClient.GetPanesContext(ctx, cp.SessionName)
		if err != nil {
			return fmt.Errorf("getting initial restored pane: %w", err)
		}
		if len(livePanes) == 0 {
			return fmt.Errorf("restored session %q has no initial pane", cp.SessionName)
		}
		if err := setRestoredPaneIdentity(ctx, livePanes[0].ID, firstPane.Title, firstPane.AgentType); err != nil {
			return fmt.Errorf("setting initial restored pane identity: %w", err)
		}
	}

	return nil
}

// restoreLayout creates additional panes to match the checkpoint layout.
func (r *Restorer) restoreLayout(cp *Checkpoint, workDir string) (int, error) {
	ctx := r.operationContext()
	paneStates := sortedCheckpointPanes(cp.Session.Panes)
	if len(paneStates) == 0 {
		return 0, nil
	}

	// First pane was created with the session, so we start at 1.
	panesCreated := 1
	lastWindowIndex := paneStates[0].WindowIndex

	for i := 1; i < len(paneStates); i++ {
		if err := ctx.Err(); err != nil {
			return panesCreated, err
		}
		paneState := paneStates[i]
		windowTarget := fmt.Sprintf("%s:%d", cp.SessionName, paneState.WindowIndex)

		var (
			paneID string
			err    error
		)

		if paneState.WindowIndex != lastWindowIndex {
			paneID, err = tmux.DefaultClient.RunContext(ctx,
				"new-window", "-P", "-F", "#{pane_id}",
				"-t", tmux.ExactTarget(windowTarget), "-c", workDir,
			)
			lastWindowIndex = paneState.WindowIndex
		} else {
			paneID, err = tmux.DefaultClient.RunContext(ctx,
				"split-window", "-t", tmux.ExactTarget(windowTarget), "-c", workDir,
				"-P", "-F", "#{pane_id}",
			)
		}
		if err != nil {
			return panesCreated, fmt.Errorf("creating pane %d: %w", i, err)
		}

		if err := setRestoredPaneIdentity(ctx, paneID, paneState.Title, paneState.AgentType); err != nil {
			return panesCreated, fmt.Errorf("setting pane %d identity (pane was created): %w", i, err)
		}

		panesCreated++
		if err := waitForRestore(ctx, 50*time.Millisecond); err != nil {
			return panesCreated, err
		}
	}

	// Apply captured layouts after panes exist.
	if len(cp.Session.WindowLayouts) > 0 {
		if err := r.applyWindowLayouts(cp.SessionName, cp.Session.WindowLayouts); err != nil {
			return panesCreated, fmt.Errorf("applying window layouts: %w", err)
		}
	} else if cp.Session.Layout != "" {
		if !canRestoreLegacySessionLayout(cp.Session) {
			return panesCreated, fmt.Errorf("skipping legacy single layout for multi-window checkpoint; per-window layouts are missing")
		}
		if err := r.applyLayout(cp.SessionName, cp.Session.Layout); err != nil {
			return panesCreated, fmt.Errorf("applying layout: %w", err)
		}
	}

	return panesCreated, ctx.Err()
}

func setRestoredPaneIdentity(ctx context.Context, paneID, title, rawAgentType string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	agentType := tmux.ParsePaneAgentTypeOption(rawAgentType)
	if agentType == tmux.AgentUnknown || agentType == tmux.AgentUser {
		if title == "" {
			return nil
		}
		return tmux.DefaultClient.RunSilentContext(ctx, "select-pane", "-t", tmux.ExactTarget(paneID), "-T", title)
	}
	return tmux.DefaultClient.SetPaneAgentIdentityContext(ctx, paneID, title, agentType)
}

func (r *Restorer) restoreAgents(cp *Checkpoint, workDir string) error {
	ctx := r.operationContext()
	panes, err := tmux.DefaultClient.GetPanesContext(ctx, cp.SessionName)
	if err != nil {
		return fmt.Errorf("getting panes: %w", err)
	}

	sortedStates := sortedCheckpointPanes(cp.Session.Panes)
	sortedPanes := sortedTmuxPanes(panes)
	attempted := 0
	launched := 0

	for i, paneState := range sortedStates {
		if err := ctx.Err(); err != nil {
			return err
		}
		if i >= len(sortedPanes) {
			return fmt.Errorf("restored session has %d panes, expected %d", len(sortedPanes), len(sortedStates))
		}

		agentCmd := restorableAgentCommand(paneState)
		if agentCmd == "" {
			continue
		}

		attempted++
		if err := relaunchRestoredPane(ctx, sortedPanes[i].ID, workDir, agentCmd); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
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
	return ctx.Err()
}

func relaunchRestoredPane(ctx context.Context, paneID, workDir, agentCmd string) error {
	safeCommand, err := tmux.SanitizePaneCommand(agentCmd)
	if err != nil {
		return err
	}

	expected := expectedPaneCommand(agentCmd)
	if expected == "" {
		return fmt.Errorf("determine expected pane command for %q", agentCmd)
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if attempt > 0 {
			if err := waitForRestore(ctx, 200*time.Millisecond); err != nil {
				return err
			}
		}

		// Respawn the pane directly into the target command instead of typing into
		// a shell prompt. This avoids lost-input races while panes are still initializing.
		if err := tmux.DefaultClient.RunSilentContext(ctx, "respawn-pane", "-k", "-c", workDir, "-t", tmux.ExactTarget(paneID), safeCommand); err != nil {
			lastErr = err
			continue
		}
		if err := waitForPaneCommand(ctx, paneID, expected, 2*time.Second); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("pane %s did not start %q", paneID, expected)
}

func waitForPaneCommand(ctx context.Context, paneID, expected string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := currentPaneCommand(ctx, paneID)
		if err == nil && current == expected {
			return ctx.Err()
		}
		if err := waitForRestore(ctx, 100*time.Millisecond); err != nil {
			return fmt.Errorf("pane %s did not start %q: %w", paneID, expected, err)
		}
	}
}

func currentPaneCommand(ctx context.Context, paneID string) (string, error) {
	output, err := tmux.DefaultClient.RunContext(ctx, "display-message", "-p", "-t", tmux.ExactTarget(paneID), "#{pane_current_command}")
	if err != nil {
		return "", fmt.Errorf("getting pane current command: %w", err)
	}
	return strings.TrimSpace(output), nil
}

func expectedPaneCommand(agentCmd string) string {
	rest := strings.TrimSpace(agentCmd)
	inEnvPrefix := false
	for rest != "" {
		token, remaining := nextShellToken(rest)
		if token == "" {
			return ""
		}
		rest = strings.TrimSpace(remaining)
		baseToken := strings.ToLower(filepath.Base(trimMatchingQuotes(token)))
		if baseToken == "env" {
			inEnvPrefix = true
			continue
		}
		if baseToken == "exec" {
			continue
		}
		if inEnvPrefix && strings.HasPrefix(token, "-") {
			continue
		}
		if isShellEnvAssignment(token) {
			continue
		}
		return baseToken
	}
	return ""
}

func nextShellToken(command string) (string, string) {
	const (
		singleQuote = byte(39)
		doubleQuote = byte(34)
		backtick    = byte(96)
		backslash   = byte(92)
	)

	start := 0
	for start < len(command) && isShellWhitespace(command[start]) {
		start++
	}
	command = command[start:]
	if command == "" {
		return "", ""
	}

	inSingle := false
	inDouble := false
	inBacktick := false
	escaped := false
	commandSubstDepth := 0

	for i := 0; i < len(command); i++ {
		ch := command[i]

		if escaped {
			escaped = false
			continue
		}

		if ch == backslash && !inSingle {
			escaped = true
			continue
		}

		if inSingle {
			if ch == singleQuote {
				inSingle = false
			}
			continue
		}

		if inDouble {
			if ch == doubleQuote {
				inDouble = false
				continue
			}
		} else if inBacktick {
			if ch == backtick {
				inBacktick = false
			}
			continue
		} else {
			switch ch {
			case singleQuote:
				inSingle = true
				continue
			case doubleQuote:
				inDouble = true
				continue
			case backtick:
				inBacktick = true
				continue
			}
		}

		if !inSingle && !inBacktick && ch == '$' && i+1 < len(command) && command[i+1] == '(' {
			commandSubstDepth++
			i++
			continue
		}
		if commandSubstDepth > 0 && !inSingle && !inBacktick && ch == ')' {
			commandSubstDepth--
			continue
		}

		if commandSubstDepth == 0 && !inDouble && !inBacktick && isShellWhitespace(ch) {
			return command[:i], command[i:]
		}
	}

	return command, ""
}

func isShellWhitespace(ch byte) bool {
	return ch == ' ' || ch == byte(9) || ch == byte(10) || ch == byte(13)
}

func isShellEnvAssignment(token string) bool {
	idx := strings.IndexByte(token, '=')
	if idx <= 0 {
		return false
	}
	name := token[:idx]
	for i := 0; i < len(name); i++ {
		ch := name[i]
		if i == 0 {
			if (ch < 'A' || ch > 'Z') && (ch < 'a' || ch > 'z') && ch != '_' {
				return false
			}
			continue
		}
		if (ch < 'A' || ch > 'Z') && (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '_' {
			return false
		}
	}
	return true
}

func trimMatchingQuotes(token string) string {
	if len(token) >= 2 {
		if (token[0] == byte(39) && token[len(token)-1] == byte(39)) || (token[0] == byte(34) && token[len(token)-1] == byte(34)) {
			return token[1 : len(token)-1]
		}
	}
	return token
}

func moveInitialWindow(ctx context.Context, sessionName string, targetWindowIndex int) error {
	if targetWindowIndex < 0 {
		return nil
	}

	currentWindowIndex, err := tmux.DefaultClient.GetFirstWindowContext(ctx, sessionName)
	if err != nil {
		return fmt.Errorf("getting initial window index: %w", err)
	}
	if currentWindowIndex == targetWindowIndex {
		return nil
	}

	source := fmt.Sprintf("%s:%d", sessionName, currentWindowIndex)
	target := fmt.Sprintf("%s:%d", sessionName, targetWindowIndex)
	if err := tmux.DefaultClient.RunSilentContext(ctx, "move-window", "-s", tmux.ExactTarget(source), "-t", tmux.ExactTarget(target)); err != nil {
		return fmt.Errorf("moving initial window from %s to %s: %w", source, target, err)
	}
	return nil
}

func (r *Restorer) restoreActivePane(cp *Checkpoint) error {
	ctx := r.operationContext()
	if cp.Session.ActivePaneIndex < 0 || cp.Session.ActivePaneIndex >= len(cp.Session.Panes) {
		return nil
	}

	panes, err := tmux.DefaultClient.GetPanesContext(ctx, cp.SessionName)
	if err != nil {
		return fmt.Errorf("getting panes: %w", err)
	}
	targetPane, ok := restoredPaneForCheckpointIndex(cp, panes, cp.Session.ActivePaneIndex)
	if !ok {
		return nil
	}
	return tmux.DefaultClient.RunSilentContext(ctx, "select-pane", "-t", tmux.ExactTarget(targetPane.ID))
}

// applyLayout applies a tmux layout string to a session.
func (r *Restorer) applyLayout(sessionName, layout string) error {
	ctx := r.operationContext()
	if layout == "" {
		layout = "tiled"
	}

	output, err := tmux.DefaultClient.RunContext(ctx, "list-windows", "-t", tmux.TargetSession(sessionName), "-F", "#{window_index}")
	if err != nil {
		return err
	}

	for _, win := range strings.Split(strings.TrimSpace(output), "\n") {
		if win == "" {
			continue
		}
		target := fmt.Sprintf("%s:%s", sessionName, win)
		if err := tmux.DefaultClient.RunSilentContext(ctx, "select-layout", "-t", tmux.ExactTarget(target), layout); err != nil {
			return err
		}
	}

	return ctx.Err()
}

func (r *Restorer) applyWindowLayouts(sessionName string, windowLayouts []WindowLayoutState) error {
	ctx := r.operationContext()
	for _, windowLayout := range cloneWindowLayouts(windowLayouts) {
		layout := strings.TrimSpace(windowLayout.Layout)
		if layout == "" {
			layout = "tiled"
		}
		target := fmt.Sprintf("%s:%d", sessionName, windowLayout.WindowIndex)
		if err := tmux.DefaultClient.RunSilentContext(ctx, "select-layout", "-t", tmux.ExactTarget(target), layout); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// injectContext sends scrollback content to restored agents.
func (r *Restorer) injectContext(cp *Checkpoint, maxLines int) error {
	ctx := r.operationContext()
	panes, err := tmux.DefaultClient.GetPanesContext(ctx, cp.SessionName)
	if err != nil {
		return fmt.Errorf("getting panes: %w", err)
	}

	var lastErr error
	for i, paneState := range cp.Session.Panes {
		if err := ctx.Err(); err != nil {
			return err
		}
		if paneState.ScrollbackFile == "" {
			continue
		}

		targetPane, ok := restoredPaneForCheckpointIndex(cp, panes, i)
		if !ok {
			lastErr = fmt.Errorf("no restored target for checkpoint pane %d", paneState.Index)
			continue
		}

		// Load scrollback content
		content, err := r.loadPaneScrollbackForPane(cp.SessionName, cp.ID, paneState)
		if err != nil {
			lastErr = err
			continue
		}

		// Truncate if maxLines specified
		if maxLines > 0 {
			content = truncateToLines(content, maxLines)
		}

		// Send as context message. The caller's context also reaches tmux's
		// buffer upload, paste, and submit delays, not just the outer loop.
		contextMsg := formatContextInjection(content, cp.CreatedAt)
		if err := tmux.DefaultClient.SendBufferContext(ctx, targetPane.ID, contextMsg, true); err != nil {
			lastErr = err
		}
	}

	return errors.Join(lastErr, ctx.Err())
}

func (r *Restorer) loadPaneScrollbackForPane(sessionName, checkpointID string, pane PaneState) (string, error) {
	return r.storage.LoadPaneScrollback(sessionName, checkpointID, pane)
}

// checkGitState compares current git state with checkpoint and returns a warning if different.
func (r *Restorer) checkGitState(cp *Checkpoint, workDir string) string {
	git := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(r.operationContext(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = workDir
		return runGitCommand(cmd, ctx, args...)
	}
	// Check if current branch matches
	branch, err := git("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "could not determine current git branch"
	}

	currentBranch := trimSpace(branch)
	if currentBranch != cp.Git.Branch {
		return fmt.Sprintf("git branch mismatch: current=%s, checkpoint=%s",
			currentBranch, cp.Git.Branch)
	}

	// Check if commit matches
	commit, err := git("rev-parse", "HEAD")
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

func canRestoreLegacySessionLayout(session SessionState) bool {
	return len(sessionWindowIndexesFromPanes(session.Panes)) <= 1
}

func validateSessionWindowLayouts(session SessionState) ([]string, []string) {
	windowIndexes := sessionWindowIndexesFromPanes(session.Panes)
	if len(windowIndexes) == 0 {
		return nil, nil
	}

	if len(session.WindowLayouts) == 0 {
		if session.Layout != "" && len(windowIndexes) > 1 {
			return nil, []string{"multi-window checkpoint only has a legacy single layout string; per-window layout fidelity will be lost"}
		}
		return nil, nil
	}

	validWindows := make(map[int]struct{}, len(windowIndexes))
	for _, windowIndex := range windowIndexes {
		validWindows[windowIndex] = struct{}{}
	}

	seen := make(map[int]struct{}, len(session.WindowLayouts))
	var issues []string
	for _, windowLayout := range session.WindowLayouts {
		if _, ok := seen[windowLayout.WindowIndex]; ok {
			issues = append(issues, fmt.Sprintf("duplicate window layout entry for window %d", windowLayout.WindowIndex))
			continue
		}
		seen[windowLayout.WindowIndex] = struct{}{}
		if _, ok := validWindows[windowLayout.WindowIndex]; !ok {
			issues = append(issues, fmt.Sprintf("window layout references missing window %d", windowLayout.WindowIndex))
		}
	}

	if len(windowIndexes) <= 1 {
		return issues, nil
	}
	for _, windowIndex := range windowIndexes {
		if _, ok := seen[windowIndex]; !ok {
			issues = append(issues, fmt.Sprintf("window layout missing for window %d", windowIndex))
		}
	}

	return issues, nil
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
	case agent.AgentTypeAntigravity:
		// agy's launch binary is "agy" (distinct from the gemini CLI).
		return "agy"
	case agent.AgentTypeGrok:
		// Relaunch with the official autonomous approval flag so the restored
		// pane does not block on tool approvals (GH#251 phase 2).
		return "grok --always-approve"
	case agent.AgentTypeOmp:
		// Relaunch with omp's skip-approval flag so the restored pane does
		// not block on tool approvals; omp picks its own default model.
		return "omp --auto-approve"
	case agent.AgentTypeCursor:
		return "cursor"
	case agent.AgentTypeWindsurf:
		return "windsurf"
	case agent.AgentTypeAider:
		return "aider"
	case agent.AgentTypeOpencode:
		return "opencode"
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
