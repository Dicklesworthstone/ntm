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
	"github.com/Dicklesworthstone/ntm/internal/agentsession"
	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/swarm"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Restore recreates a session from saved state.
func Restore(state *SessionState, opts RestoreOptions) (err error) {
	if state == nil {
		return fmt.Errorf("session state is nil")
	}

	name := opts.Name
	if name == "" {
		name = state.Name
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

	// Check if session already exists
	if tmux.SessionExists(name) {
		if !opts.Force {
			return fmt.Errorf("session '%s' already exists (use --force to overwrite)", name)
		}
		if err := tmux.KillSession(name); err != nil {
			return fmt.Errorf("killing existing session: %w", err)
		}
		killedExisting = true
	}

	// Validate and prepare working directory
	workDir := state.WorkDir
	if workDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("getting home directory: %w", err)
		}
		workDir = home
	}

	// Check if directory exists
	if _, err := os.Stat(workDir); os.IsNotExist(err) {
		// Try to create it if it looks like a project path
		if shouldCreateDir(workDir) {
			if err := os.MkdirAll(workDir, 0755); err != nil {
				return fmt.Errorf("creating directory %s: %w", workDir, err)
			}
		} else {
			// Fall back to home directory
			home, err := os.UserHomeDir()
			if err != nil {
				workDir = os.TempDir()
			} else {
				workDir = home
			}
		}
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
		if err := tmux.CreateSession(name, workDir); err != nil {
			return fmt.Errorf("creating session: %w", err)
		}
		sessionCreated = true
	} else {
		lastWindowIndex := -1
		for i, p := range panes {
			if i == 0 {
				// First pane of first window -> Create Session
				if err := tmux.CreateSession(name, workDir); err != nil {
					return fmt.Errorf("creating session: %w", err)
				}
				sessionCreated = true
				lastWindowIndex = p.WindowIndex
				continue
			}

			if p.WindowIndex != lastWindowIndex {
				// New window
				if err := tmux.DefaultClient.RunSilent("new-window", "-t", name, "-c", workDir); err != nil {
					return fmt.Errorf("creating window for pane %d: %w", i+1, err)
				}
				lastWindowIndex = p.WindowIndex
			} else {
				// Split window
				// We target the session, which defaults to the active window (the one we just created or split)
				if _, err := tmux.DefaultClient.Run("split-window", "-t", name, "-c", workDir); err != nil {
					return fmt.Errorf("creating pane %d: %w", i+1, err)
				}
			}
		}
	}

	// Get pane list
	tmuxPanes, err := tmux.GetPanes(name)
	if err != nil {
		return fmt.Errorf("getting panes: %w", err)
	}

	// Set pane titles
	for i, paneState := range panes {
		if i >= len(tmuxPanes) {
			break
		}
		if paneState.Title != "" {
			if err := tmux.SetPaneTitle(tmuxPanes[i].ID, paneState.Title); err != nil {
				// Non-fatal - continue with other panes
				continue
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

	return nil
}

// countLaunchableAgents reports how many saved panes would actually launch an
// agent, using the same predicates as the launch loop: user panes are skipped,
// and so is any pane with neither a saved command nor a type default.
//
// Counting only these avoids a false capacity error for a saved session whose
// extra panes were never going to launch anything.
func countLaunchableAgents(paneStates []PaneState, cmds AgentCommands) int {
	count := 0
	for _, paneState := range paneStates {
		if paneState.AgentType == string(tmux.AgentUser) || paneState.AgentType == "user" {
			continue
		}
		agentCmd := paneState.Command
		if agentCmd == "" {
			agentCmd = getAgentCommand(paneState.AgentType, cmds)
		}
		if agentCmd == "" {
			continue
		}
		count++
	}
	return count
}

// applyClaudeIsolation prefixes a Claude pane's launch command with its
// per-pane credential isolation environment (GH#237, bd-4tz2d), and returns
// non-Claude commands unchanged.
//
// It is applied to the RENDERED command whether that came from the saved pane
// command or the type default: a saved command was captured before this pane
// had an isolated config dir, so neither source carries one. Restore is the
// most important path to cover because it recreates a whole saved swarm at
// once — relaunching all of its Claude panes onto the shared rotating
// credential puts every one of them back into the refresh-token race.
func applyClaudeIsolation(cfg *config.Config, workDir, sessionName string, paneState PaneState, agentCmd string) (string, error) {
	if agent.AgentType(paneState.AgentType).Canonical() != agent.AgentTypeClaudeCode {
		return agentCmd, nil
	}
	claudeEnv, err := swarm.ProvisionClaudeIsolation(cfg, workDir, sessionName, paneState.Index)
	if err != nil {
		return "", err
	}
	return claudeEnv.ApplyToCommand(agentCmd), nil
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
	if state == nil {
		return fmt.Errorf("session state is nil")
	}
	if err := ValidateAutomatedRelaunch(state); err != nil {
		return err
	}

	correlationID := audit.NewCorrelationID()
	auditStart := time.Now()
	attempted := 0
	launched := 0
	planned := len(state.Panes)
	_ = audit.LogEvent(sessionName, audit.EventTypeSpawn, audit.ActorSystem, "session.restore.agents", map[string]interface{}{
		"phase":          "start",
		"session":        sessionName,
		"agents_planned": planned,
		"correlation_id": correlationID,
	}, nil)
	defer func() {
		payload := map[string]interface{}{
			"phase":            "finish",
			"session":          sessionName,
			"agents_planned":   planned,
			"agents_attempted": attempted,
			"agents_launched":  launched,
			"success":          err == nil,
			"duration_ms":      time.Since(auditStart).Milliseconds(),
			"correlation_id":   correlationID,
		}
		if err != nil {
			payload["error"] = err.Error()
		}
		_ = audit.LogEvent(sessionName, audit.EventTypeSpawn, audit.ActorSystem, "session.restore.agents", payload, nil)
	}()

	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		return fmt.Errorf("getting panes: %w", err)
	}

	// Sort panes by WindowIndex, then Index to ensure mapping matches creation order.
	// Copy to avoid mutating the caller's slice.
	sortedPaneStates := make([]PaneState, len(state.Panes))
	copy(sortedPaneStates, state.Panes)
	sort.Slice(sortedPaneStates, func(i, j int) bool {
		if sortedPaneStates[i].WindowIndex != sortedPaneStates[j].WindowIndex {
			return sortedPaneStates[i].WindowIndex < sortedPaneStates[j].WindowIndex
		}
		return sortedPaneStates[i].Index < sortedPaneStates[j].Index
	})

	// A pane grid smaller than the saved agent list means some agents cannot be
	// launched at all. Silently breaking out of the loop discarded them with no
	// error, no audit event, and no effect on the return value, so the caller
	// reported a clean restore while N agents never started — the same
	// silently-dropped-work failure ba13c058 fixed on the swarm side. Name the
	// arithmetic instead.
	if launchable := countLaunchableAgents(sortedPaneStates, cmds); launchable > len(panes) {
		_ = audit.LogEvent(sessionName, audit.EventTypeError, audit.ActorSystem, "session.restore.agents", map[string]interface{}{
			"phase":             "capacity",
			"session":           sessionName,
			"agents_launchable": launchable,
			"panes_available":   len(panes),
			"correlation_id":    correlationID,
		}, nil)
		return fmt.Errorf(
			"restored session %q has %d pane(s) but %d agent(s) to launch; %d would be silently dropped (topology restore likely failed)",
			sessionName, len(panes), launchable, launchable-len(panes))
	}

	for i, paneState := range sortedPaneStates {
		if i >= len(panes) {
			break
		}

		// Skip user panes
		if paneState.AgentType == string(tmux.AgentUser) || paneState.AgentType == "user" {
			continue
		}

		// Prefer the pane's pre-rendered command (carries the captured model;
		// see ntm-boi0), falling back to the type-default command. An empty
		// result means there is nothing to launch for this pane.
		agentCmd := paneState.Command
		if agentCmd == "" {
			agentCmd = getAgentCommand(paneState.AgentType, cmds)
		}
		if agentCmd == "" {
			continue
		}

		// Per-pane Claude credential isolation (GH#237, bd-4tz2d).
		isolatedCmd, isoErr := applyClaudeIsolation(cfg, state.WorkDir, sessionName, paneState, agentCmd)
		if isoErr != nil {
			_ = audit.LogEvent(sessionName, audit.EventTypeError, audit.ActorSystem, "agent.restore", map[string]interface{}{
				"agent_type":     paneState.AgentType,
				"pane_index":     paneState.Index,
				"pane_title":     paneState.Title,
				"error":          fmt.Sprintf("claude credential isolation: %v", isoErr),
				"correlation_id": correlationID,
			}, nil)
			return fmt.Errorf("isolating credentials for claude pane %d: %w", paneState.Index, isoErr)
		}
		agentCmd = isolatedCmd

		attempted++

		// Launch agent
		safeAgentCmd, err := tmux.SanitizePaneCommand(agentCmd)
		if err != nil {
			_ = audit.LogEvent(sessionName, audit.EventTypeError, audit.ActorSystem, "agent.restore", map[string]interface{}{
				"agent_type":     paneState.AgentType,
				"pane_index":     paneState.Index,
				"pane_title":     paneState.Title,
				"error":          err.Error(),
				"correlation_id": correlationID,
			}, nil)
			continue
		}

		cmd, err := tmux.BuildPaneCommand(state.WorkDir, safeAgentCmd)
		if err != nil {
			_ = audit.LogEvent(sessionName, audit.EventTypeError, audit.ActorSystem, "agent.restore", map[string]interface{}{
				"agent_type":     paneState.AgentType,
				"pane_index":     paneState.Index,
				"pane_title":     paneState.Title,
				"error":          err.Error(),
				"correlation_id": correlationID,
			}, nil)
			continue
		}

		if err := tmux.SendKeysForAgent(panes[i].ID, cmd, true, tmux.AgentType(paneState.AgentType)); err != nil {
			_ = audit.LogEvent(sessionName, audit.EventTypeError, audit.ActorSystem, "agent.restore", map[string]interface{}{
				"agent_type":     paneState.AgentType,
				"pane_index":     paneState.Index,
				"pane_title":     paneState.Title,
				"error":          err.Error(),
				"correlation_id": correlationID,
			}, nil)
			// Non-fatal - continue with other agents
			continue
		}
		launched++
		_ = audit.LogEvent(sessionName, audit.EventTypeSpawn, audit.ActorSystem, "agent.restore", map[string]interface{}{
			"agent_type":     paneState.AgentType,
			"pane_index":     paneState.Index,
			"pane_title":     paneState.Title,
			"correlation_id": correlationID,
		}, nil)
	}

	if launched == 0 && attempted > 0 {
		return fmt.Errorf("all %d agent launch attempts failed", attempted)
	}
	return nil
}

// ResumeOptions configures session resume (topology restore + agent relaunch
// with provider-session resume delegated to casr / native --resume).
type ResumeOptions struct {
	Name       string // Name to resume as (defaults to saved name)
	Force      bool   // Force restore even if a tmux session exists
	PreferCASR bool   // Prefer `casr` over the native --resume flag when available
}

// ResumeResult reports per-pane outcomes of a Resume operation.
type ResumeResult struct {
	Session  string       `json:"session"`
	Panes    []ResumePane `json:"panes"`
	Resumed  int          `json:"resumed"`
	Launched int          `json:"launched"`
	Skipped  int          `json:"skipped"`
}

// ResumePane reports how a single pane was handled during resume.
type ResumePane struct {
	Index     int    `json:"index"`
	Title     string `json:"title,omitempty"`
	AgentType string `json:"agent_type"`
	SessionID string `json:"session_id,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Command   string `json:"command,omitempty"`
	// Action is one of: "resumed" (provider session id replayed),
	// "launched" (fresh agent, no id), "skipped" (user/unknown pane).
	Action string `json:"action"`
}

// Resume reconstructs the tmux topology for state, then relaunches each agent
// pane. Panes that captured a provider session id are resumed via casr / native
// --resume; panes without an id are launched fresh. The topology recreation is
// owned by ntm (Restore); per-pane provider-session resume is delegated to casr
// (see internal/agentsession), keeping ntm out of provider-specific formats.
func Resume(state *SessionState, cmds AgentCommands, opts ResumeOptions) (*ResumeResult, error) {
	if state == nil {
		return nil, fmt.Errorf("session state is nil")
	}
	if err := ValidateAutomatedRelaunch(state); err != nil {
		return nil, err
	}

	name := opts.Name
	if name == "" {
		name = state.Name
	}

	// Recreate windows/panes/splits/cwd/layout (ntm-owned topology restore).
	if err := Restore(state, RestoreOptions{Name: name, Force: opts.Force, SkipGitCheck: true}); err != nil {
		return nil, err
	}

	panes, err := tmux.GetPanes(name)
	if err != nil {
		return nil, fmt.Errorf("getting panes: %w", err)
	}

	// Sort saved pane states to match Restore's creation order.
	sorted := make([]PaneState, len(state.Panes))
	copy(sorted, state.Panes)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].WindowIndex != sorted[j].WindowIndex {
			return sorted[i].WindowIndex < sorted[j].WindowIndex
		}
		return sorted[i].Index < sorted[j].Index
	})

	result := &ResumeResult{Session: name}

	for i, ps := range sorted {
		if i >= len(panes) {
			break
		}
		rp := ResumePane{Index: ps.Index, Title: ps.Title, AgentType: ps.AgentType}

		// Skip user / non-agent panes.
		if ps.AgentType == string(tmux.AgentUser) || ps.AgentType == "user" ||
			agentsession.ResumeProvider(ps.AgentType) == "" {
			rp.Action = "skipped"
			result.Skipped++
			result.Panes = append(result.Panes, rp)
			continue
		}

		var launchCmd string
		if ps.HasFreshSessionBinding() {
			provider := ps.SessionProvider
			if provider == "" {
				provider = agentsession.ResumeProvider(ps.AgentType)
			}
			launchCmd = agentsession.ResumeCommand(provider, ps.SessionID, opts.PreferCASR)
			rp.SessionID = ps.SessionID
			rp.Provider = provider
		}

		if launchCmd == "" {
			// No captured id (or no resume path) -> fresh agent launch. Prefer
			// the pane's pre-rendered command (carries the captured model; see
			// ntm-boi0), falling back to the type-default.
			launchCmd = ps.Command
			if launchCmd == "" {
				launchCmd = getAgentCommand(ps.AgentType, cmds)
			}
			rp.Action = "launched"
		} else {
			rp.Action = "resumed"
		}

		if launchCmd == "" {
			rp.Action = "skipped"
			result.Skipped++
			result.Panes = append(result.Panes, rp)
			continue
		}

		safeCmd, err := tmux.SanitizePaneCommand(launchCmd)
		if err != nil {
			rp.Action = "skipped"
			result.Skipped++
			result.Panes = append(result.Panes, rp)
			continue
		}
		fullCmd, err := tmux.BuildPaneCommand(state.WorkDir, safeCmd)
		if err != nil {
			rp.Action = "skipped"
			result.Skipped++
			result.Panes = append(result.Panes, rp)
			continue
		}

		if err := tmux.SendKeysForAgent(panes[i].ID, fullCmd, true, tmux.AgentType(ps.AgentType)); err != nil {
			rp.Action = "skipped"
			result.Skipped++
			result.Panes = append(result.Panes, rp)
			continue
		}

		rp.Command = launchCmd
		if rp.Action == "resumed" {
			result.Resumed++
		} else {
			result.Launched++
		}
		result.Panes = append(result.Panes, rp)
	}

	return result, nil
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
	newOut, err := tmux.DefaultClient.Run("list-windows", "-t", session, "-F", "#{window_index}")
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
			_ = tmux.DefaultClient.RunSilent("rename-window", "-t", target, w.Name)
		}
		layout := w.Layout
		if layout == "" {
			layout = state.Layout
		}
		if layout != "" {
			_ = tmux.DefaultClient.RunSilent("select-layout", "-t", target, layout)
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
		_ = tmux.DefaultClient.RunSilent("select-pane", "-t", paneID)
		if w, ok := winByIdx[panes[i].WindowIndex]; ok {
			if w.Zoomed {
				_ = tmux.DefaultClient.RunSilent("resize-pane", "-Z", "-t", paneID)
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
		_ = tmux.DefaultClient.RunSilent("select-window", "-t", activeWindowTarget)
	}
}

// applyLayout applies a tmux layout to the session.
func applyLayout(session, layout string) error {
	if layout == "" {
		layout = "tiled"
	}

	// Get first window
	output, err := tmux.DefaultClient.Run("list-windows", "-t", session, "-F", "#{window_index}")
	if err != nil {
		return err
	}

	windows := strings.Split(strings.TrimSpace(output), "\n")
	for _, win := range windows {
		if win == "" {
			continue
		}
		target := fmt.Sprintf("%s:%s", session, win)
		_ = tmux.DefaultClient.RunSilent("select-layout", "-t", target, layout)
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

	// Must be under home directory
	if !strings.HasPrefix(path, home) {
		return false
	}

	// Should be at least 2 levels deep from home
	// e.g., ~/Developer/project is ok, ~/project is not
	rel, err := filepath.Rel(home, path)
	if err != nil {
		return false
	}

	parts := strings.Split(rel, string(filepath.Separator))
	return len(parts) >= 2
}
