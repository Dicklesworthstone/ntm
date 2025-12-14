package dashboard

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/cass"
	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tokens"
	"github.com/Dicklesworthstone/ntm/internal/tracker"
	"github.com/Dicklesworthstone/ntm/internal/tui/dashboard/panels"
)

// fetchBeadsCmd calls bv.GetBeadsSummary
func (m Model) fetchBeadsCmd() tea.Cmd {
	return func() tea.Msg {
		if !bv.IsInstalled() {
			return BeadsUpdateMsg{Err: fmt.Errorf("bv not installed")}
		}
		summary := bv.GetBeadsSummary(m.projectDir, 5) // Get top 5 ready/in-progress
		if !summary.Available {
			return BeadsUpdateMsg{Err: fmt.Errorf("%s", summary.Reason)}
		}
		return BeadsUpdateMsg{Summary: *summary, Ready: summary.ReadyPreview}
	}
}

// fetchAlertsCmd aggregates alerts
func (m Model) fetchAlertsCmd() tea.Cmd {
	return func() tea.Msg {
		var cfg alerts.Config
		if m.cfg != nil {
			cfg = alerts.ToConfigAlerts(
				m.cfg.Alerts.Enabled,
				m.cfg.Alerts.AgentStuckMinutes,
				m.cfg.Alerts.DiskLowThresholdGB,
				m.cfg.Alerts.MailBacklogThreshold,
				m.cfg.Alerts.BeadStaleHours,
				m.cfg.Alerts.ResolvedPruneMinutes,
				m.cfg.ProjectsBase,
			)
		} else {
			cfg = alerts.DefaultConfig()
		}

		gen := alerts.NewGenerator(cfg)
		activeAlerts := gen.GenerateAll()
		return AlertsUpdateMsg{Alerts: activeAlerts}
	}
}

// fetchMetricsCmd calculates token usage
func (m Model) fetchMetricsCmd() tea.Cmd {
	// Capture panes from model to avoid race (Model is value receiver so copy)
	panes := m.panes

	return func() tea.Msg {
		var totalTokens int
		var totalCost float64
		var agentMetrics []panels.AgentMetric

		for _, p := range panes {
			// Skip user panes
			if p.Type == tmux.AgentUser {
				continue
			}

			// Capture more context for better estimate
			out, err := tmux.CapturePaneOutput(p.ID, 2000)
			if err != nil {
				continue
			}

			// Estimate
			modelName := "gpt-4" // default
			if p.Variant != "" {
				modelName = p.Variant
			}

			usage := tokens.GetUsageInfo(out, modelName)
			tokensCount := usage.EstimatedTokens

			totalTokens += tokensCount

			// Rough cost calculation (very approximate placeholders)
			// $10 per 1M tokens input (blended)
			cost := float64(tokensCount) / 1_000_000.0 * 10.0
			totalCost += cost

			agentMetrics = append(agentMetrics, panels.AgentMetric{
				Name:       p.Title,
				Type:       string(p.Type),
				Tokens:     tokensCount,
				Cost:       cost,
				ContextPct: usage.UsagePercent,
			})
		}

		return MetricsUpdateMsg{
			Data: panels.MetricsData{
				TotalTokens: totalTokens,
				TotalCost:   totalCost,
				Agents:      agentMetrics,
			},
		}
	}
}

// fetchHistoryCmd reads recent history
func (m Model) fetchHistoryCmd() tea.Cmd {
	return func() tea.Msg {
		entries, err := history.ReadRecent(20)
		if err != nil {
			return HistoryUpdateMsg{Err: err}
		}
		return HistoryUpdateMsg{Entries: entries}
	}
}

// fetchFileChangesCmd queries tracker
func (m Model) fetchFileChangesCmd() tea.Cmd {
	return func() tea.Msg {
		// Get changes from last 5 minutes
		since := time.Now().Add(-5 * time.Minute)
		changes := tracker.RecordedChangesSince(since)
		return FileChangeMsg{Changes: changes}
	}
}

// fetchCASSContextCmd searches CASS for recent context related to the session.
// We keep this generic: use the session name as the query and return top hits.
func (m Model) fetchCASSContextCmd() tea.Cmd {
	session := m.session

	return func() tea.Msg {
		client := cass.NewClient()
		ctx := context.Background()

		// If CASS not installed/available, degrade gracefully.
		if !client.IsInstalled() {
			return CASSContextMsg{Err: fmt.Errorf("cass not installed")}
		}

		resp, err := client.Search(ctx, cass.SearchOptions{
			Query: session,
			Limit: 5,
		})
		if err != nil {
			return CASSContextMsg{Err: err}
		}

		return CASSContextMsg{Hits: resp.Hits}
	}
}
