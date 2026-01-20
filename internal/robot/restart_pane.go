package robot

import (
	"fmt"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// RestartPaneOutput is the structured output for --robot-restart-pane
type RestartPaneOutput struct {
	Session     string         `json:"session"`
	RestartedAt time.Time      `json:"restarted_at"`
	Restarted   []string       `json:"restarted"`
	Failed      []RestartError `json:"failed"`
	DryRun      bool           `json:"dry_run,omitempty"`
	WouldAffect []string       `json:"would_affect,omitempty"`
}

// RestartError represents a failed restart attempt
type RestartError struct {
	Pane   string `json:"pane"`
	Reason string `json:"reason"`
}

// RestartPaneOptions configures the PrintRestartPane operation
type RestartPaneOptions struct {
	Session string   // Target session name
	Panes   []string // Specific pane indices to restart (empty = all agents)
	Type    string   // Filter by agent type (e.g., "claude", "cc")
	All     bool     // Include all panes (including user)
	DryRun  bool     // Preview mode
}

// PrintRestartPane restarts panes (respawn-pane -k)
func PrintRestartPane(opts RestartPaneOptions) error {
	output := RestartPaneOutput{
		Session:     opts.Session,
		RestartedAt: time.Now().UTC(),
		Restarted:   []string{},
		Failed:      []RestartError{},
	}

	if !tmux.SessionExists(opts.Session) {
		output.Failed = append(output.Failed, RestartError{
			Pane:   "session",
			Reason: fmt.Sprintf("session '%s' not found", opts.Session),
		})
		return encodeJSON(output)
	}

	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		output.Failed = append(output.Failed, RestartError{
			Pane:   "panes",
			Reason: fmt.Sprintf("failed to get panes: %v", err),
		})
		return encodeJSON(output)
	}

	// Build pane filter map
	paneFilterMap := make(map[string]bool)
	for _, p := range opts.Panes {
		paneFilterMap[p] = true
	}
	hasPaneFilter := len(paneFilterMap) > 0

	// Determine which panes to restart
	var targetPanes []tmux.Pane
	for _, pane := range panes {
		paneKey := fmt.Sprintf("%d", pane.Index)

		// Check specific pane filter
		if hasPaneFilter && !paneFilterMap[paneKey] && !paneFilterMap[pane.ID] {
			continue
		}

		// Filter by type if specified
		if opts.Type != "" {
			agentType := detectAgentType(pane.Title)
			// Normalize type for comparison (handle aliases like cc vs claude)
			targetType := translateAgentTypeForStatus(opts.Type)
			currentType := translateAgentTypeForStatus(agentType)
			if targetType != currentType {
				continue
			}
		}

		// Skip user panes by default unless --all or specific pane filter
		if !opts.All && !hasPaneFilter && opts.Type == "" {
			agentType := detectAgentType(pane.Title)
			if pane.Index == 0 && agentType == "unknown" {
				continue
			}
			if agentType == "user" {
				continue
			}
		}

		targetPanes = append(targetPanes, pane)
	}

	if len(targetPanes) == 0 {
		return encodeJSON(output)
	}

	// Dry-run mode
	if opts.DryRun {
		output.DryRun = true
		for _, pane := range targetPanes {
			paneKey := fmt.Sprintf("%d", pane.Index)
			output.WouldAffect = append(output.WouldAffect, paneKey)
		}
		return encodeJSON(output)
	}

	// Restart targets
	for _, pane := range targetPanes {
		paneKey := fmt.Sprintf("%d", pane.Index)
		
		// Always use kill=true for restart to ensure process is cycled
		err := tmux.RespawnPane(pane.ID, true)
		if err != nil {
			output.Failed = append(output.Failed, RestartError{
				Pane:   paneKey,
				Reason: fmt.Sprintf("failed to respawn: %v", err),
			})
		} else {
			output.Restarted = append(output.Restarted, paneKey)
		}
	}

	return encodeJSON(output)
}
