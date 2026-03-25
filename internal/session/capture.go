package session

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Capture captures the current state of a tmux session.
func Capture(sessionName string) (state *SessionState, err error) {
	correlationID := audit.NewCorrelationID()
	auditStart := time.Now()
	_ = audit.LogEvent(sessionName, audit.EventTypeCommand, audit.ActorSystem, "session.capture", map[string]interface{}{
		"phase":          "start",
		"session":        sessionName,
		"correlation_id": correlationID,
	}, nil)
	defer func() {
		payload := map[string]interface{}{
			"phase":          "finish",
			"session":        sessionName,
			"success":        err == nil,
			"duration_ms":    time.Since(auditStart).Milliseconds(),
			"correlation_id": correlationID,
		}
		if state != nil {
			payload["panes"] = len(state.Panes)
			payload["agents"] = state.Agents.Total()
			payload["layout"] = state.Layout
			payload["work_dir"] = state.WorkDir
		}
		if err != nil {
			payload["error"] = err.Error()
		}
		_ = audit.LogEvent(sessionName, audit.EventTypeCommand, audit.ActorSystem, "session.capture", payload, nil)
	}()

	session, err := tmux.GetSession(sessionName)
	if err != nil {
		return nil, err
	}

	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		return nil, err
	}

	// Count agents by type
	agents := countAgents(panes)

	// Map pane states
	paneStates := mapPaneStates(panes)

	// Detect working directory from first pane or session
	cwd := detectWorkDir(sessionName, panes)

	// Get git info if in a repo
	gitBranch, gitRemote, gitCommit := getGitInfo(cwd)

	// Get layout
	layout := getLayout(sessionName)

	// Parse session creation time (tmux format varies, try common formats)
	var createdAt time.Time
	if session.Created != "" {
		// Try parsing various tmux date formats
		formats := []string{
			"Mon Jan 2 15:04:05 2006",
			"Mon Jan _2 15:04:05 2006",
			time.UnixDate,
			time.ANSIC,
		}
		for _, format := range formats {
			if t, err := time.Parse(format, session.Created); err == nil {
				createdAt = t.UTC()
				break
			}
		}
	}

	state = &SessionState{
		Name:      sessionName,
		SavedAt:   time.Now().UTC(),
		WorkDir:   cwd,
		GitBranch: gitBranch,
		GitRemote: gitRemote,
		GitCommit: gitCommit,
		Agents:    agents,
		Panes:     paneStates,
		Layout:    layout,
		CreatedAt: createdAt,
		Version:   StateVersion,
	}

	return state, nil
}

// countAgents counts agents by type from pane list.
func countAgents(panes []tmux.Pane) AgentConfig {
	config := AgentConfig{}
	for _, p := range panes {
		switch p.Type {
		case tmux.AgentClaude:
			config.Claude++
		case tmux.AgentCodex:
			config.Codex++
		case tmux.AgentGemini:
			config.Gemini++
		case tmux.AgentCursor:
			config.Cursor++
		case tmux.AgentWindsurf:
			config.Windsurf++
		case tmux.AgentAider:
			config.Aider++
		case tmux.AgentOllama:
			config.Ollama++
		case tmux.AgentUser:
			config.User++
		}
	}
	return config
}

// mapPaneStates converts tmux panes to PaneState.
func mapPaneStates(panes []tmux.Pane) []PaneState {
	states := make([]PaneState, len(panes))
	for i, p := range panes {
		states[i] = PaneState{
			Title:       p.Title,
			Index:       p.Index,
			WindowIndex: p.WindowIndex,
			AgentType:   string(p.Type),
			Model:       p.Variant,
			Active:      p.Active,
			Width:       p.Width,
			Height:      p.Height,
			PaneID:      p.ID,
		}
	}
	return states
}

// detectWorkDir attempts to detect the working directory for the session.
func detectWorkDir(sessionName string, panes []tmux.Pane) string {
	// Try to get the pane's current path via tmux
	if len(panes) > 0 {
		// Use tmux display-message to get the pane path
		output, err := tmux.DefaultClient.Run("display-message", "-t", panes[0].ID, "-p", "#{pane_current_path}")
		if err == nil && len(output) > 0 {
			path := strings.TrimSpace(output)
			if path != "" {
				return path
			}
		}
	}

	// Fallback: try to determine from current process working directory
	// This is often correct if ntm is run from the project root
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}

	// Final fallback: user home directory
	if homeDir, err := os.UserHomeDir(); err == nil {
		return homeDir
	}

	return ""
}

// getGitInfo extracts git branch, remote, and commit from a directory.
func getGitInfo(dir string) (branch, remote, commit string) {
	return getGitInfoWithTimeout(dir, 5*time.Second)
}

func getGitInfoWithTimeout(dir string, timeout time.Duration) (branch, remote, commit string) {
	if dir == "" {
		return "", "", ""
	}

	branch = runGitInfoCommand(dir, timeout, "rev-parse", "--abbrev-ref", "HEAD")
	remote = runGitInfoCommand(dir, timeout, "remote", "get-url", "origin")
	commit = runGitInfoCommand(dir, timeout, "rev-parse", "--short", "HEAD")

	return branch, remote, commit
}

func runGitInfoCommand(dir string, timeout time.Duration, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	output, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// getLayout gets the current tmux layout for the session.
func getLayout(sessionName string) string {
	output, err := tmux.DefaultClient.Run("display-message", "-t", sessionName, "-p", "#{window_layout}")
	if err != nil {
		return "tiled" // Default
	}
	// tmux layouts can be complex strings, but we'll use simplified versions
	layout := strings.TrimSpace(output)

	// Map to simple layout names if possible
	switch {
	case strings.HasPrefix(layout, "even-horizontal"):
		return "even-horizontal"
	case strings.HasPrefix(layout, "even-vertical"):
		return "even-vertical"
	case strings.HasPrefix(layout, "main-horizontal"):
		return "main-horizontal"
	case strings.HasPrefix(layout, "main-vertical"):
		return "main-vertical"
	case strings.HasPrefix(layout, "tiled"):
		return "tiled"
	default:
		// Return as-is for custom layouts
		return layout
	}
}
