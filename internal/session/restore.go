package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Restore recreates a session from saved state.
func Restore(state *SessionState, opts RestoreOptions) error {
	name := opts.Name
	if name == "" {
		name = state.Name
	}

	// Check if session already exists
	if tmux.SessionExists(name) {
		if !opts.Force {
			return fmt.Errorf("session '%s' already exists (use --force to overwrite)", name)
		}
		if err := tmux.KillSession(name); err != nil {
			return fmt.Errorf("killing existing session: %w", err)
		}
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

	// Create the session
	if err := tmux.CreateSession(name, workDir); err != nil {
		return fmt.Errorf("creating session: %w", err)
	}

	// Create additional panes
	totalPanes := len(state.Panes)
	if totalPanes > 1 {
		for i := 1; i < totalPanes; i++ {
			if _, err := tmux.SplitWindow(name, workDir); err != nil {
				return fmt.Errorf("creating pane %d: %w", i+1, err)
			}
		}
	}

	// Get pane list
	panes, err := tmux.GetPanes(name)
	if err != nil {
		return fmt.Errorf("getting panes: %w", err)
	}

	// Set pane titles
	for i, paneState := range state.Panes {
		if i >= len(panes) {
			break
		}
		if paneState.Title != "" {
			if err := tmux.SetPaneTitle(panes[i].ID, paneState.Title); err != nil {
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
			fmt.Printf("Warning: Current branch '%s' differs from saved branch '%s'\n", currentBranch, state.GitBranch)
		}
	}

	return nil
}

// RestoreAgents launches the agents in the restored session.
// This is separated from Restore to allow for customization.
func RestoreAgents(sessionName string, state *SessionState, cfg AgentConfig) error {
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
		// Note: We use current config commands, not saved ones
		agentCmd := getAgentCommand(paneState.AgentType, cfg)
		if agentCmd == "" {
			continue
		}

		// Launch agent
		cmd := fmt.Sprintf("cd %q && %s", state.WorkDir, agentCmd)
		if err := tmux.SendKeys(panes[i].ID, cmd, true); err != nil {
			// Non-fatal - continue with other agents
			continue
		}
	}

	return nil
}

// AgentCommandConfig holds agent commands from config.
type AgentCommandConfig struct {
	Claude string
	Codex  string
	Gemini string
}

// getAgentCommand returns the command for an agent type.
func getAgentCommand(agentType string, cmds AgentConfig) string {
	// This returns empty - actual commands should be provided by the CLI
	// which has access to the full config
	return ""
}

// applyLayout applies a tmux layout to the session.
func applyLayout(session, layout string) error {
	if layout == "" {
		layout = "tiled"
	}

	// Get first window
	output, err := exec.Command("tmux", "list-windows", "-t", session, "-F", "#{window_index}").Output()
	if err != nil {
		return err
	}

	windows := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, win := range windows {
		if win == "" {
			continue
		}
		target := fmt.Sprintf("%s:%s", session, win)
		_ = exec.Command("tmux", "select-layout", "-t", target, layout).Run()
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
