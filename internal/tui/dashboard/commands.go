package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/cass"
	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tokens"
	"github.com/Dicklesworthstone/ntm/internal/tracker"
	"github.com/Dicklesworthstone/ntm/internal/tui/dashboard/panels"
)

// fetchBeadsCmd calls bv.GetBeadsSummary
func (m *Model) fetchBeadsCmd() tea.Cmd {
	gen := m.nextGen(refreshBeads)
	projectDir := m.projectDir
	return func() tea.Msg {
		if !bv.IsInstalled() {
			// bv not installed - return unavailable summary (not an error)
			return BeadsUpdateMsg{Summary: bv.BeadsSummary{Available: false, Reason: "bv not installed"}, Gen: gen}
		}
		summary := bv.GetBeadsSummary(projectDir, 5) // Get top 5 ready/in-progress
		// Return summary regardless of availability - let UI handle gracefully
		// "No beads" is not an error, just an unavailable state
		return BeadsUpdateMsg{Summary: *summary, Ready: summary.ReadyPreview, Gen: gen}
	}
}

// fetchAlertsCmd aggregates alerts
func (m *Model) fetchAlertsCmd() tea.Cmd {
	gen := m.nextGen(refreshAlerts)
	cfg := m.cfg
	return func() tea.Msg {
		var alertCfg alerts.Config
		if cfg != nil {
			alertCfg = alerts.ToConfigAlerts(
				cfg.Alerts.Enabled,
				cfg.Alerts.AgentStuckMinutes,
				cfg.Alerts.DiskLowThresholdGB,
				cfg.Alerts.MailBacklogThreshold,
				cfg.Alerts.BeadStaleHours,
				cfg.Alerts.ResolvedPruneMinutes,
				cfg.ProjectsBase,
			)
		} else {
			alertCfg = alerts.DefaultConfig()
		}

		// Use GenerateAndTrack to benefit from lifecycle management and error handling
		tracker := alerts.GenerateAndTrack(alertCfg)
		activeAlerts := tracker.GetActive()
		return AlertsUpdateMsg{Alerts: activeAlerts, Gen: gen}
	}
}

// fetchMetricsCmd calculates token usage
func (m *Model) fetchMetricsCmd() tea.Cmd {
	gen := m.nextGen(refreshMetrics)
	// Capture panes from model to avoid mutation during async fetch
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
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			out, err := tmux.CapturePaneOutputContext(ctx, p.ID, 2000)
			cancel()
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
			Gen: gen,
		}
	}
}

// fetchHistoryCmd reads recent history
func (m *Model) fetchHistoryCmd() tea.Cmd {
	gen := m.nextGen(refreshHistory)
	return func() tea.Msg {
		entries, err := history.ReadRecent(20)
		if err != nil {
			return HistoryUpdateMsg{Err: err, Gen: gen}
		}
		return HistoryUpdateMsg{Entries: entries, Gen: gen}
	}
}

// fetchFileChangesCmd queries tracker
func (m *Model) fetchFileChangesCmd() tea.Cmd {
	gen := m.nextGen(refreshFiles)
	return func() tea.Msg {
		// Get changes from last 5 minutes
		since := time.Now().Add(-5 * time.Minute)
		changes := tracker.RecordedChangesSince(since)
		return FileChangeMsg{Changes: changes, Gen: gen}
	}
}

// fetchCASSContextCmd searches CASS for recent context related to the session.
// We keep this generic: use the session name as the query and return top hits.
func (m *Model) fetchCASSContextCmd() tea.Cmd {
	gen := m.nextGen(refreshCass)
	session := m.session

	return func() tea.Msg {
		client := cass.NewClient()
		ctx := context.Background()

		// If CASS not installed/available, degrade gracefully.
		if !client.IsInstalled() {
			return CASSContextMsg{Err: fmt.Errorf("cass not installed"), Gen: gen}
		}

		resp, err := client.Search(ctx, cass.SearchOptions{
			Query: session,
			Limit: 5,
		})
		if err != nil {
			return CASSContextMsg{Err: err, Gen: gen}
		}

		return CASSContextMsg{Hits: resp.Hits, Gen: gen}
	}
}

// fetchRoutingCmd fetches routing scores for all agents in the session.
func (m *Model) fetchRoutingCmd() tea.Cmd {
	gen := m.nextGen(refreshRouting)
	session := m.session
	panes := m.panes

	return func() tea.Msg {
		scores := make(map[string]RoutingScore)

		// Skip if no panes
		if len(panes) == 0 {
			return RoutingUpdateMsg{Scores: scores, Gen: gen}
		}

		// Score agents using the robot package
		scorer := robot.NewAgentScorer(robot.DefaultRoutingConfig())
		scoredAgents, err := scorer.ScoreAgents(session, "")
		if err != nil {
			return RoutingUpdateMsg{Err: err, Gen: gen}
		}

		// Find the recommended agent (highest score, not excluded)
		var recommendedPaneID string
		var highestScore float64 = -1
		for _, sa := range scoredAgents {
			if !sa.Excluded && sa.Score > highestScore {
				highestScore = sa.Score
				recommendedPaneID = sa.PaneID
			}
		}

		// Map results to RoutingScore
		for _, sa := range scoredAgents {
			scores[sa.PaneID] = RoutingScore{
				Score:         sa.Score,
				IsRecommended: sa.PaneID == recommendedPaneID,
				State:         string(sa.State),
			}
		}

		return RoutingUpdateMsg{Scores: scores, Gen: gen}
	}
}

// fetchSpawnStateCmd reads spawn state from the project directory
func (m *Model) fetchSpawnStateCmd() tea.Cmd {
	gen := m.nextGen(refreshSpawn)
	projectDir := m.projectDir

	return func() tea.Msg {
		state, err := loadSpawnState(projectDir)
		if err != nil || state == nil {
			// No spawn state or error reading - return inactive
			return SpawnUpdateMsg{Data: panels.SpawnData{Active: false}, Gen: gen}
		}

		// Convert to SpawnData for the panel
		data := panels.SpawnData{
			Active:         true,
			BatchID:        state.BatchID,
			StartedAt:      state.StartedAt,
			StaggerSeconds: state.StaggerSeconds,
			TotalAgents:    state.TotalAgents,
			CompletedAt:    state.CompletedAt,
		}

		for _, p := range state.Prompts {
			data.Prompts = append(data.Prompts, panels.SpawnPromptStatus{
				Pane:        p.Pane,
				Order:       p.Order,
				ScheduledAt: p.ScheduledAt,
				Sent:        p.Sent,
				SentAt:      p.SentAt,
			})
		}

		return SpawnUpdateMsg{Data: data, Gen: gen}
	}
}

// spawnState mirrors cli.SpawnState for dashboard reading
// This avoids importing cli package which has many dependencies
type spawnState struct {
	BatchID        string              `json:"batch_id"`
	StartedAt      time.Time           `json:"started_at"`
	StaggerSeconds int                 `json:"stagger_seconds"`
	TotalAgents    int                 `json:"total_agents"`
	Prompts        []spawnPromptStatus `json:"prompts"`
	CompletedAt    time.Time           `json:"completed_at,omitempty"`
}

type spawnPromptStatus struct {
	Pane        string    `json:"pane"`
	PaneID      string    `json:"pane_id"`
	Order       int       `json:"order"`
	ScheduledAt time.Time `json:"scheduled"`
	Sent        bool      `json:"sent"`
	SentAt      time.Time `json:"sent_at,omitempty"`
}

// loadSpawnState reads spawn state from disk
func loadSpawnState(projectDir string) (*spawnState, error) {
	path := filepath.Join(projectDir, ".ntm", "spawn-state.json")

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No state file
		}
		return nil, err
	}

	var state spawnState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}

	return &state, nil
}
