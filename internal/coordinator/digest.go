package coordinator

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

// DigestSummary contains a summary of session activity.
type DigestSummary struct {
	Session       string              `json:"session"`
	GeneratedAt   time.Time           `json:"generated_at"`
	AgentCount    int                 `json:"agent_count"`
	ActiveCount   int                 `json:"active_count"`
	IdleCount     int                 `json:"idle_count"`
	ErrorCount    int                 `json:"error_count"`
	AgentStatuses []AgentDigestStatus `json:"agent_statuses"`
	Alerts        []string            `json:"alerts,omitempty"`
	WorkSummary   WorkDigestSummary   `json:"work_summary"`
}

// AgentDigestStatus summarizes a single agent's status.
type AgentDigestStatus struct {
	PaneIndex    int     `json:"pane_index"`
	AgentType    string  `json:"agent_type"`
	Status       string  `json:"status"`
	ContextUsage float64 `json:"context_usage"`
	IdleFor      string  `json:"idle_for,omitempty"`
	Task         string  `json:"task,omitempty"`
}

// WorkDigestSummary summarizes work status.
type WorkDigestSummary struct {
	PendingTasks    int      `json:"pending_tasks"`
	InProgressTasks int      `json:"in_progress_tasks"`
	CompletedToday  int      `json:"completed_today"`
	BlockedTasks    int      `json:"blocked_tasks"`
	TopReady        []string `json:"top_ready,omitempty"`
}

// GenerateDigest creates a summary of the current session state.
func (c *SessionCoordinator) GenerateDigest() DigestSummary {
	c.mu.RLock()
	defer c.mu.RUnlock()

	digest := DigestSummary{
		Session:       c.session,
		GeneratedAt:   time.Now(),
		AgentCount:    len(c.agents),
		AgentStatuses: make([]AgentDigestStatus, 0, len(c.agents)),
	}

	for _, agent := range c.agents {
		status := AgentDigestStatus{
			PaneIndex:    agent.PaneIndex,
			AgentType:    agent.AgentType,
			Status:       string(agent.Status),
			ContextUsage: agent.ContextUsage,
			Task:         agent.CurrentTask,
		}

		// Count by status
		switch agent.Status {
		case robot.StateWaiting:
			digest.IdleCount++
			if !agent.LastActivity.IsZero() {
				status.IdleFor = formatDuration(time.Since(agent.LastActivity))
			}
		case robot.StateGenerating, robot.StateThinking:
			digest.ActiveCount++
		case robot.StateError:
			digest.ErrorCount++
			digest.Alerts = append(digest.Alerts, fmt.Sprintf("Agent %d (%s) in error state", agent.PaneIndex, agent.AgentType))
		case robot.StateStalled:
			digest.Alerts = append(digest.Alerts, fmt.Sprintf("Agent %d (%s) appears stalled", agent.PaneIndex, agent.AgentType))
		}

		// Alert for high context usage
		if agent.ContextUsage > 85 {
			digest.Alerts = append(digest.Alerts, fmt.Sprintf("Agent %d (%s) context at %.0f%%", agent.PaneIndex, agent.AgentType, agent.ContextUsage))
		}

		digest.AgentStatuses = append(digest.AgentStatuses, status)
	}

	// bd-c9wr1: c.agents is a map and Go iterates maps in randomized
	// order, so two GenerateDigest calls against the same state would
	// otherwise produce different AgentStatuses orderings and
	// different Alerts sequences. Sort both for byte-stable output —
	// PaneIndex for AgentStatuses (intuitive for human readers),
	// alphabetical for Alerts.
	sort.Slice(digest.AgentStatuses, func(i, j int) bool {
		return digest.AgentStatuses[i].PaneIndex < digest.AgentStatuses[j].PaneIndex
	})
	sort.Strings(digest.Alerts)

	return digest
}

// SendDigest sends a digest summary to the configured human agent.
func (c *SessionCoordinator) SendDigest(ctx context.Context) error {
	if c.mailClient == nil {
		return nil // No mail client configured
	}

	digest := c.GenerateDigest()

	// Format as markdown
	body := c.formatDigestMarkdown(digest)

	// Determine importance based on alerts
	importance := "normal"
	if len(digest.Alerts) > 0 {
		importance = "high"
	}
	if digest.ErrorCount > 0 {
		importance = "urgent"
	}

	// Send to human
	_, err := c.mailClient.SendMessage(ctx, agentmail.SendMessageOptions{
		ProjectKey:  c.projectKey,
		SenderName:  c.agentName,
		To:          []string{c.config.HumanAgent},
		Subject:     fmt.Sprintf("Session Digest: %s", c.session),
		BodyMD:      body,
		Importance:  importance,
		AckRequired: false,
	})
	if err != nil {
		return fmt.Errorf("sending digest: %w", err)
	}

	// Emit event
	select {
	case c.events <- CoordinatorEvent{
		Type:      EventDigestSent,
		Timestamp: time.Now(),
		Details: map[string]any{
			"agent_count":  digest.AgentCount,
			"active_count": digest.ActiveCount,
			"alert_count":  len(digest.Alerts),
		},
	}:
	default:
	}

	return nil
}

// formatDigestMarkdown formats a digest as markdown.
func (c *SessionCoordinator) formatDigestMarkdown(digest DigestSummary) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("# Session Digest: %s\n\n", digest.Session))
	sb.WriteString(fmt.Sprintf("**Generated:** %s\n\n", digest.GeneratedAt.Format(time.RFC3339)))

	// Summary stats
	sb.WriteString("## Summary\n\n")
	sb.WriteString(fmt.Sprintf("- **Total Agents:** %d\n", digest.AgentCount))
	sb.WriteString(fmt.Sprintf("- **Active:** %d\n", digest.ActiveCount))
	sb.WriteString(fmt.Sprintf("- **Idle:** %d\n", digest.IdleCount))
	if digest.ErrorCount > 0 {
		sb.WriteString(fmt.Sprintf("- **Errors:** %d ⚠️\n", digest.ErrorCount))
	}
	sb.WriteString("\n")

	// Alerts
	if len(digest.Alerts) > 0 {
		sb.WriteString("## Alerts\n\n")
		for _, alert := range digest.Alerts {
			sb.WriteString(fmt.Sprintf("- ⚠️ %s\n", alert))
		}
		sb.WriteString("\n")
	}

	// Agent statuses
	sb.WriteString("## Agent Status\n\n")
	sb.WriteString("| Pane | Type | Status | Context | Idle For |\n")
	sb.WriteString("|------|------|--------|---------|----------|\n")
	for _, agent := range digest.AgentStatuses {
		idleFor := "-"
		if agent.IdleFor != "" {
			idleFor = agent.IdleFor
		}
		sb.WriteString(fmt.Sprintf("| %d | %s | %s | %.0f%% | %s |\n",
			agent.PaneIndex, agent.AgentType, agent.Status, agent.ContextUsage, idleFor))
	}
	sb.WriteString("\n")

	// Footer
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("*Coordinator: %s*\n", c.agentName))

	return sb.String()
}

// formatDuration formats a duration in human-readable form.
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}
