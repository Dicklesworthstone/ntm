package session

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Restore recreates a session from saved state.
func Restore(state *SessionState, opts RestoreOptions) (err error) {
	return RestoreContext(context.Background(), state, opts)
}

// RestoreContext restores topology with cancellation, without launching agents.
func RestoreContext(ctx context.Context, state *SessionState, opts RestoreOptions) error {
	return restoreSession(ctx, state, opts, nil)
}

// launch receives the physical panes captured before restoring active windows
// and zoom. Recovery must not rediscover positional targets after layout changes.
func restoreSession(ctx context.Context, state *SessionState, opts RestoreOptions, launch func([]tmux.Pane) error) (err error) {
	if ctx == nil {
		return fmt.Errorf("session restore requires a context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if state == nil {
		return fmt.Errorf("session state is nil")
	}

	name := opts.Name
	if name == "" {
		name = state.Name
	}
	if err := tmux.ValidateSessionName(name); err != nil {
		return fmt.Errorf("invalid session name: %w", err)
	}
	// Validate the complete topology before --force can replace a live session.
	// Duplicate coordinates make the positional pane/agent mapping ambiguous.
	seen := make(map[[2]int]bool, len(state.Panes))
	for _, pane := range state.Panes {
		coord := [2]int{pane.WindowIndex, pane.Index}
		if pane.WindowIndex < 0 || pane.Index < 0 || seen[coord] {
			return fmt.Errorf("invalid saved pane coordinates %d.%d", pane.WindowIndex, pane.Index)
		}
		seen[coord] = true
	}
	workDir, err := restoreWorkingDirectory(state.WorkDir)
	if err != nil {
		return err
	}
	for _, pane := range state.Panes {
		if _, err := restorePaneDirectory(pane, workDir); err != nil {
			return fmt.Errorf("saved pane %d.%d: %w", pane.WindowIndex, pane.Index, err)
		}
		if pane.LaunchSpec != nil {
			if err := pane.LaunchSpec.Validate(tmux.ParsePaneAgentTypeOption(pane.AgentType)); err != nil {
				return fmt.Errorf("saved pane %d.%d launch settings: %w", pane.WindowIndex, pane.Index, err)
			}
		}
	}

	correlationID := audit.NewCorrelationID()
	auditStart := time.Now()
	sessionCreated := false
	killedExisting := false
	panesPlanned := len(state.Panes)
	_ = audit.LogEvent(name, audit.EventTypeCommand, audit.ActorSystem, "session.restore", map[string]interface{}{
		"phase":          "start",
		"session":        name,
		"force":          opts.Force,
		"skip_git_check": opts.SkipGitCheck,
		"panes_planned":  panesPlanned,
		"correlation_id": correlationID,
	}, nil)
	defer func() {
		payload := map[string]interface{}{
			"phase":           "finish",
			"session":         name,
			"force":           opts.Force,
			"skip_git_check":  opts.SkipGitCheck,
			"panes_planned":   panesPlanned,
			"session_created": sessionCreated,
			"killed_existing": killedExisting,
			"success":         err == nil,
			"duration_ms":     time.Since(auditStart).Milliseconds(),
			"correlation_id":  correlationID,
		}
		payload["work_dir"] = state.WorkDir
		payload["layout"] = state.Layout
		if err != nil {
			payload["error"] = err.Error()
		}
		_ = audit.LogEvent(name, audit.EventTypeCommand, audit.ActorSystem, "session.restore", payload, nil)
	}()

	// A probe failure is not proof that the session is absent. In particular,
	// permission/socket errors must not let restore proceed with a mutation.
	exists, err := tmux.SessionExistsContext(ctx, name)
	if err != nil {
		return fmt.Errorf("checking existing session: %w", err)
	}
	if exists {
		if !opts.Force {
			return fmt.Errorf("session '%s' already exists (use --force to overwrite)", name)
		}
		if err := tmux.DefaultClient.RunSilentContext(ctx, "kill-session", "-t", tmux.TargetSession(name)); err != nil {
			return fmt.Errorf("killing existing session: %w", err)
		}
		killedExisting = true
	}

	// Sort panes by WindowIndex, then Index to ensure creation order matches structure.
	// Copy to avoid mutating the caller's slice.
	panes := make([]PaneState, len(state.Panes))
	copy(panes, state.Panes)
	sort.Slice(panes, func(i, j int) bool {
		if panes[i].WindowIndex != panes[j].WindowIndex {
			return panes[i].WindowIndex < panes[j].WindowIndex
		}
		return panes[i].Index < panes[j].Index
	})

	if len(panes) == 0 {
		// Create empty session if no panes
		if err := tmux.CreateSessionContext(ctx, name, workDir); err != nil {
			return fmt.Errorf("creating session: %w", err)
		}
		sessionCreated = true
	} else {
		lastWindowIndex := -1
		for i, p := range panes {
			paneDir, err := restorePaneDirectory(p, workDir)
			if err != nil {
				return fmt.Errorf("saved pane %d.%d working directory changed: %w", p.WindowIndex, p.Index, err)
			}
			if i == 0 {
				// First pane of first window -> Create Session
				if err := tmux.CreateSessionContext(ctx, name, paneDir); err != nil {
					return fmt.Errorf("creating session: %w", err)
				}
				sessionCreated = true
				lastWindowIndex = p.WindowIndex
				continue
			}

			if p.WindowIndex != lastWindowIndex {
				// New window
				if err := tmux.DefaultClient.RunSilentContext(ctx, "new-window", "-t", tmux.TargetSession(name), "-c", paneDir); err != nil {
					return fmt.Errorf("creating window for pane %d: %w", i+1, err)
				}
				lastWindowIndex = p.WindowIndex
			} else {
				// Split window
				// We target the session, which defaults to the active window (the one we just created or split)
				if _, err := tmux.DefaultClient.RunContext(ctx, "split-window", "-t", tmux.TargetSession(name), "-c", paneDir); err != nil {
					return fmt.Errorf("creating pane %d: %w", i+1, err)
				}
			}
		}
	}

	// Get pane list
	tmuxPanes, err := tmux.GetPanesContext(ctx, name)
	if err != nil {
		return fmt.Errorf("getting panes: %w", err)
	}

	// Restore visible titles and durable agent types before relaunching. The
	// pane option remains authoritative after wrappers or TUIs rewrite titles.
	// User panes deliberately remain title-only so later agent processes can be
	// discovered from their title, command, or process tree.
	for i, paneState := range panes {
		if i >= len(tmuxPanes) {
			break
		}
		agentType := tmux.ParsePaneAgentTypeOption(paneState.AgentType)
		if agentType != tmux.AgentUnknown && agentType != tmux.AgentUser {
			if err := tmux.SetPaneAgentIdentityContext(ctx, tmuxPanes[i].ID, paneState.Title, agentType); err != nil {
				return fmt.Errorf("setting restored pane %d identity: %w", i, err)
			}
			tmuxPanes[i].Type = agentType
		} else if paneState.Title != "" {
			if err := tmux.SetPaneTitle(tmuxPanes[i].ID, paneState.Title); err != nil {
				return fmt.Errorf("setting restored pane %d title: %w", i, err)
			}
		}
		if paneState.LaunchSpec != nil {
			if err := tmux.SetPaneLaunchSpecContext(ctx, tmuxPanes[i].ID, *paneState.LaunchSpec); err != nil {
				return fmt.Errorf("setting restored pane %d launch settings: %w", i, err)
			}
		}
	}

	// Restore per-window fidelity: exact geometry, window names, active window,
	// active pane, and zoom. Falls back to the whole-session layout for states
	// saved without per-window metadata. All steps are best-effort (non-fatal).
	restoreWindowFidelity(name, state, panes, tmuxPanes)

	// Check git branch if requested
	if !opts.SkipGitCheck && state.GitBranch != "" {
		currentBranch := getCurrentGitBranch(workDir)
		if currentBranch != "" && currentBranch != state.GitBranch {
			// Just warn, don't fail
			log.Printf("restore: current branch '%s' differs from saved branch '%s'", currentBranch, state.GitBranch)
		}
	}

	if launch != nil {
		return launch(tmuxPanes)
	}
	return ctx.Err()
}

// Explicit pane directories identify worktrees, so their disappearance is an
// error. Creating an empty directory would discard the saved worktree context.
func restorePaneDirectory(pane PaneState, sessionDir string) (string, error) {
	if pane.WorkDir == "" {
		return sessionDir, nil
	}
	if _, err := tmux.SanitizePaneCommand(pane.WorkDir); err != nil {
		return "", fmt.Errorf("invalid saved pane working directory: %w", err)
	}
	if !filepath.IsAbs(pane.WorkDir) {
		return "", fmt.Errorf("saved pane working directory must be absolute: %q", pane.WorkDir)
	}
	info, err := os.Stat(pane.WorkDir)
	if err != nil {
		return "", fmt.Errorf("checking saved pane working directory %q: %w", pane.WorkDir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("saved pane working directory %q is not a directory", pane.WorkDir)
	}
	return pane.WorkDir, nil
}

// restoreWorkingDirectory resolves the same directory for both the tmux
// topology and agent commands. An explicit missing/unusable directory must not
// silently move a recovery into another project, especially before --force.
func restoreWorkingDirectory(workDir string) (string, error) {
	if workDir == "" {
		var err error
		workDir, err = os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("getting home directory: %w", err)
		}
	}
	if _, err := tmux.SanitizePaneCommand(workDir); err != nil {
		return "", fmt.Errorf("invalid saved working directory: %w", err)
	}
	workDir, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("resolving saved working directory: %w", err)
	}
	info, err := os.Stat(workDir)
	if os.IsNotExist(err) && shouldCreateDir(workDir) {
		if err := os.MkdirAll(workDir, 0755); err != nil {
			return "", fmt.Errorf("creating saved working directory %q: %w", workDir, err)
		}
		info, err = os.Stat(workDir)
	}
	if err != nil {
		return "", fmt.Errorf("checking saved working directory %q: %w", workDir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("saved working directory %q is not a directory", workDir)
	}
	return workDir, nil
}

// buildRestoreCommand validates the final payload, including the quoted cwd.
// Validating the executable alone would allow control bytes in a saved path to
// become terminal input when the complete command is sent to a pane.
func buildRestoreCommand(workDir, command string) (string, error) {
	full, err := tmux.BuildPaneCommand(workDir, command)
	if err != nil {
		return "", err
	}
	return tmux.SanitizePaneCommand(full)
}

// ValidateRestoreAgentCommands checks the full saved batch before a launching
// restore replaces a session. RestoreAgents repeats the check at its own public
// boundary so no preceding agent launches hide a malformed later command.
func ValidateRestoreAgentCommands(state *SessionState, cmds AgentCommands) error {
	if state == nil {
		return fmt.Errorf("session state is nil")
	}
	if err := ValidateAutomatedRelaunch(state); err != nil {
		return err
	}
	for _, pane := range state.Panes {
		if pane.AgentType == string(tmux.AgentUser) {
			continue
		}
		command := pane.Command
		if pane.LaunchSpec != nil {
			if err := pane.LaunchSpec.ValidateReplay(tmux.ParsePaneAgentTypeOption(pane.AgentType)); err != nil {
				return fmt.Errorf("saved pane %d.%d launch settings: %w", pane.WindowIndex, pane.Index, err)
			}
			command = pane.LaunchSpec.Command
		} else if command == "" {
			command = getAgentCommand(pane.AgentType, cmds)
		}
		if command == "" {
			continue
		}
		workDir := state.WorkDir
		if pane.WorkDir != "" {
			workDir = pane.WorkDir
		}
		if _, err := buildRestoreCommand(workDir, command); err != nil {
			return fmt.Errorf("saved pane %d.%d launch command: %w", pane.WindowIndex, pane.Index, err)
		}
	}
	return nil
}

// droppedLaunchableAgents reports how many saved panes WOULD launch an agent
// but sit at a position the live pane grid cannot reach.
//
// The mapping is POSITIONAL: the launch loop pairs sortedPaneStates[i] with
// panes[i] and stops at `i >= len(panes)`, so a non-launchable state (a user
// pane, or one with no command from either source) still consumes a slot. A
// count of launchable agents is therefore the wrong comparison — a saved
// session of [user, cc, cc, cc] restored into 3 panes has 3 launchable agents
// and 3 panes, yet the third cc sits at index 3 and is dropped. Only the
// INDEX of each launchable pane answers the question.
//
// Non-launchable states past the cutoff are ignored: they were never going to
// launch anything, so they must not trip a false capacity error.
func droppedLaunchableAgents(paneStates []PaneState, cmds AgentCommands, paneCount int) int {
	dropped := 0
	for i, paneState := range paneStates {
		if i < paneCount {
			continue
		}
		if paneState.AgentType == string(tmux.AgentUser) || paneState.AgentType == "user" {
			continue
		}
		agentCmd := paneState.Command
		if paneState.LaunchSpec != nil {
			agentCmd = paneState.LaunchSpec.Command
		} else if agentCmd == "" {
			agentCmd = getAgentCommand(paneState.AgentType, cmds)
		}
		if agentCmd == "" {
			continue
		}
		dropped++
	}
	return dropped
}

// RestoreAgents launches the agents in the restored session.
// This is separated from Restore to allow for customization.
//
// cfg carries the launch configuration and may be nil. It is required for
// per-pane Claude credential isolation (GH#237): restore recreates a whole
// saved swarm at once, so relaunching its Claude panes onto the shared
// rotating credential puts every one of them back into the refresh-token race
// the isolated config dir exists to prevent.
func RestoreAgents(sessionName string, state *SessionState, cmds AgentCommands, cfg *config.Config) (err error) {
	ctx := context.Background()
	launches, err := preflightSavedLaunches(ctx, state, cmds, cfg, nil)
	if err != nil {
		return err
	}
	panes, err := tmux.GetPanesContext(ctx, sessionName)
	if err != nil {
		return fmt.Errorf("getting panes: %w", err)
	}
	if dropped := droppedLaunchableAgents(sortedSavedPanes(state), cmds, len(panes)); dropped > 0 {
		return fmt.Errorf(
			"restored session %q has %d pane(s) but %d saved pane state(s); %d agent(s) would be silently dropped (topology restore likely failed)",
			sessionName, len(panes), len(launches), dropped)
	}
	_, err = dispatchSavedLaunches(ctx, sessionName, launches, panes)
	return err
}

// ResumeOptions configures session resume (topology restore + agent relaunch
// with provider-session resume delegated to casr / native --resume).
type ResumeOptions struct {
	Name       string         // Name to resume as (defaults to saved name)
	Force      bool           // Force restore even if a tmux session exists
	PreferCASR bool           // Prefer `casr` over the native --resume flag when available
	Config     *config.Config // Used only for legacy settings and replay dependencies
}

// ResumeResult reports per-pane outcomes of a Resume operation.
type ResumeResult struct {
	Session  string       `json:"session"`
	Panes    []ResumePane `json:"panes"`
	Resumed  int          `json:"resumed"`
	Launched int          `json:"launched"`
	Skipped  int          `json:"skipped"`
	Failed   int          `json:"failed"`
}

// ResumePane reports how a single pane was handled during resume.
type ResumePane struct {
	Index       int    `json:"index"`
	WindowIndex int    `json:"window_index"`
	PaneID      string `json:"pane_id,omitempty"`
	Title       string `json:"title,omitempty"`
	AgentType   string `json:"agent_type"`
	SessionID   string `json:"session_id,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Command     string `json:"command,omitempty"`
	Error       string `json:"error,omitempty"`
	// Action is one of: "resumed" (provider session id replayed),
	// "launched" (fresh agent, no id), "skipped" (user/unknown pane),
	// or "failed" (an intended agent could not be dispatched).
	Action string `json:"action"`
}

// Resume reconstructs saved topology and resumes fresh provider bindings with
// native commands preserving recorded settings. Legacy saves without commands
// may use casr. Panes without a fresh binding receive their saved launch command.
func Resume(state *SessionState, cmds AgentCommands, opts ResumeOptions) (*ResumeResult, error) {
	return ResumeContext(context.Background(), state, cmds, opts)
}

// ResumeContext preflights and resumes the complete saved batch with the
// caller's cancellation. Durable commands retain all recorded launch settings.
func ResumeContext(ctx context.Context, state *SessionState, cmds AgentCommands, opts ResumeOptions) (*ResumeResult, error) {
	launches, err := preflightSavedLaunches(ctx, state, cmds, opts.Config, &opts)
	if err != nil {
		return nil, err
	}
	name := opts.Name
	if name == "" {
		name = state.Name
	}
	var result *ResumeResult
	err = restoreSession(ctx, state, RestoreOptions{Name: name, Force: opts.Force, SkipGitCheck: true}, func(panes []tmux.Pane) error {
		var launchErr error
		result, launchErr = dispatchSavedLaunches(ctx, name, launches, panes)
		return launchErr
	})
	return result, err
}

// getAgentCommand returns the command for an agent type.
func getAgentCommand(agentType string, cmds AgentCommands) string {
	switch agent.AgentType(agentType).Canonical() {
	case tmux.AgentClaude:
		return cmds.Claude
	case tmux.AgentCodex:
		return cmds.Codex
	case tmux.AgentGemini:
		return cmds.Gemini
	case tmux.AgentAntigravity:
		return cmds.Antigravity
	case tmux.AgentGrok:
		return ""
	case tmux.AgentCursor:
		return cmds.Cursor
	case tmux.AgentWindsurf:
		return cmds.Windsurf
	case tmux.AgentAider:
		return cmds.Aider
	case tmux.AgentOpencode:
		return cmds.Opencode
	case tmux.AgentOmp:
		return cmds.Omp
	case tmux.AgentOllama:
		return cmds.Ollama
	default:
		return ""
	}
}

// windowCreationOrder returns the distinct window indices in the order Restore
// creates them — i.e. the order they first appear when panes are sorted by
// (WindowIndex, Index). This matches the sequence of CreateSession/new-window
// calls, so the k-th entry corresponds to the k-th window tmux assigns.
func windowCreationOrder(panes []PaneState) []int {
	var order []int
	seen := make(map[int]bool, len(panes))
	for _, p := range panes {
		if seen[p.WindowIndex] {
			continue
		}
		seen[p.WindowIndex] = true
		order = append(order, p.WindowIndex)
	}
	return order
}

// restoreWindowFidelity re-applies per-window names, exact geometry, the active
// pane in each window, window zoom, and the active window. It maps each saved
// window to the freshly-created tmux window by creation order, and maps saved
// panes to new panes positionally (panes[i] <-> tmuxPanes[i], the same mapping
// Restore uses for titles). With no per-window metadata it falls back to the
// legacy whole-session layout. Every tmux call is best-effort.
func restoreWindowFidelity(session string, state *SessionState, panes []PaneState, tmuxPanes []tmux.Pane) {
	if len(state.Windows) == 0 {
		_ = applyLayout(session, state.Layout)
		return
	}

	// Map saved window indices (in creation order) -> new tmux window indices.
	newOut, err := tmux.DefaultClient.Run("list-windows", "-t", tmux.TargetSession(session), "-F", "#{window_index}")
	if err != nil {
		_ = applyLayout(session, state.Layout)
		return
	}
	var newWins []string
	for _, w := range strings.Split(strings.TrimSpace(newOut), "\n") {
		if w = strings.TrimSpace(w); w != "" {
			newWins = append(newWins, w)
		}
	}
	savedToNew := make(map[int]string)
	for i, sIdx := range windowCreationOrder(panes) {
		if i < len(newWins) {
			savedToNew[sIdx] = newWins[i]
		}
	}

	winByIdx := make(map[int]WindowState, len(state.Windows))
	for _, w := range state.Windows {
		winByIdx[w.Index] = w
	}

	// Apply window name + exact layout per window.
	for sIdx, newIdx := range savedToNew {
		w, ok := winByIdx[sIdx]
		if !ok {
			continue
		}
		target := fmt.Sprintf("%s:%s", session, newIdx)
		if w.Name != "" {
			_ = tmux.DefaultClient.RunSilent("rename-window", "-t", tmux.ExactTarget(target), w.Name)
		}
		layout := w.Layout
		if layout == "" {
			layout = state.Layout
		}
		if layout != "" {
			_ = tmux.DefaultClient.RunSilent("select-layout", "-t", tmux.ExactTarget(target), layout)
		}
	}

	// Restore the active pane in each window (and zoom). The active pane is the
	// per-window #{pane_active}, captured into PaneState.Active.
	var activeWindowTarget string
	for i := range panes {
		if i >= len(tmuxPanes) || !panes[i].Active {
			continue
		}
		paneID := tmuxPanes[i].ID
		_ = tmux.DefaultClient.RunSilent("select-pane", "-t", tmux.ExactTarget(paneID))
		if w, ok := winByIdx[panes[i].WindowIndex]; ok {
			if w.Zoomed {
				_ = tmux.DefaultClient.RunSilent("resize-pane", "-Z", "-t", tmux.ExactTarget(paneID))
			}
			if w.Active {
				if newIdx, ok := savedToNew[panes[i].WindowIndex]; ok {
					activeWindowTarget = fmt.Sprintf("%s:%s", session, newIdx)
				}
			}
		}
	}

	// Fallback: select the active window even if its active pane wasn't flagged.
	if activeWindowTarget == "" {
		for _, w := range state.Windows {
			if w.Active {
				if newIdx, ok := savedToNew[w.Index]; ok {
					activeWindowTarget = fmt.Sprintf("%s:%s", session, newIdx)
				}
				break
			}
		}
	}
	if activeWindowTarget != "" {
		_ = tmux.DefaultClient.RunSilent("select-window", "-t", tmux.ExactTarget(activeWindowTarget))
	}
}

// applyLayout applies a tmux layout to the session.
func applyLayout(session, layout string) error {
	if layout == "" {
		layout = "tiled"
	}

	// Get first window
	output, err := tmux.DefaultClient.Run("list-windows", "-t", tmux.TargetSession(session), "-F", "#{window_index}")
	if err != nil {
		return err
	}

	windows := strings.Split(strings.TrimSpace(output), "\n")
	for _, win := range windows {
		if win == "" {
			continue
		}
		target := fmt.Sprintf("%s:%s", session, win)
		_ = tmux.DefaultClient.RunSilent("select-layout", "-t", tmux.ExactTarget(target), layout)
	}

	return nil
}

// getCurrentGitBranch returns the current git branch for a directory.
func getCurrentGitBranch(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// shouldCreateDir determines if a path should be auto-created.
func shouldCreateDir(path string) bool {
	// Don't create root or home-level directories
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}

	// Must be below the home directory. A lexical prefix is not sufficient:
	// /Users/alice-restore shares the prefix /Users/alice but is a sibling,
	// not a child, of the home directory.
	rel, err := filepath.Rel(home, path)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}

	// Should be at least 2 levels deep from home
	// e.g., ~/Developer/project is ok, ~/project is not

	parts := strings.Split(rel, string(filepath.Separator))
	return len(parts) >= 2
}
