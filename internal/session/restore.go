package session

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Restore recreates a session from saved state.
func Restore(state *SessionState, opts RestoreOptions) (err error) {
	name := opts.Name
	if name == "" {
		name = state.Name
	}

	correlationID := audit.NewCorrelationID()
	auditStart := time.Now()
	sessionCreated := false
	killedExisting := false
	panesPlanned := 0
	if state != nil {
		panesPlanned = len(state.Panes)
	}
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
		if state != nil {
			payload["work_dir"] = state.WorkDir
			payload["layout"] = state.Layout
		}
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
			home, _ := os.UserHomeDir()
			workDir = home
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

	// Apply layout
	if err := applyLayout(name, state.Layout); err != nil {
		// Non-fatal - tiled layout will be used
	}

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

// RestoreAgents launches the agents in the restored session.
// This is separated from Restore to allow for customization.
func RestoreAgents(sessionName string, state *SessionState, cmds AgentCommands) (err error) {
	correlationID := audit.NewCorrelationID()
	auditStart := time.Now()
	attempted := 0
	launched := 0
	planned := 0
	if state != nil {
		planned = len(state.Panes)
	}
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

	for i, paneState := range state.Panes {
		if i >= len(panes) {
			break
		}

		// Skip user panes
		if paneState.AgentType == string(tmux.AgentUser) || paneState.AgentType == "user" {
			continue
		}

		// Get agent command based on type
		agentCmd := getAgentCommand(paneState.AgentType, cmds)
		if agentCmd == "" {
			continue
		}

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

		if err := tmux.SendKeys(panes[i].ID, cmd, true); err != nil {
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

	return nil
}

// getAgentCommand returns the command for an agent type.
func getAgentCommand(agentType string, cmds AgentCommands) string {
	switch agentType {
	case "cc", "claude":
		return cmds.Claude
	case "cod", "codex":
		return cmds.Codex
	case "gmi", "gemini":
		return cmds.Gemini
	case "cursor":
		return cmds.Cursor
	case "windsurf":
		return cmds.Windsurf
	case "aider":
		return cmds.Aider
	case "ollama":
		return cmds.Ollama
	default:
		return ""
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
	output, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
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
