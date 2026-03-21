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
	"github.com/Dicklesworthstone/ntm/internal/handoff"
	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/integrations/pt"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
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

// fetchAttentionCmd reads attention events from the feed.
func (m *Model) fetchAttentionCmd() tea.Cmd {
	gen := m.nextGen(refreshAttention)
	return func() tea.Msg {
		feed := robot.GetAttentionFeed()
		if feed == nil {
			return AttentionUpdateMsg{FeedAvailable: false, Gen: gen}
		}

		// Replay recent events (up to 20 items)
		events, _, err := feed.Replay(0, 20)
		if err != nil {
			return AttentionUpdateMsg{FeedAvailable: false, Gen: gen}
		}

		// Filter to action_required and interesting only, convert to AttentionItems
		items := make([]panels.AttentionItem, 0, len(events))
		for _, ev := range events {
			if ev.Actionability != robot.ActionabilityActionRequired &&
				ev.Actionability != robot.ActionabilityInteresting {
				continue
			}

			ts, _ := time.Parse(time.RFC3339, ev.Ts)
			item := panels.AttentionItem{
				Summary:       ev.Summary,
				Actionability: ev.Actionability,
				Timestamp:     ts,
				SourcePane:    ev.Pane,
				Cursor:        ev.Cursor,
			}
			// Extract agent type from details if available
			if ev.Details != nil {
				if agent, ok := ev.Details["agent_type"].(string); ok {
					item.SourceAgent = agent
				}
			}
			items = append(items, item)
		}

		// Sort by actionability (action_required first) then by recency (newest first)
		sortAttentionItems(items)

		return AttentionUpdateMsg{Items: items, FeedAvailable: true, Gen: gen}
	}
}

// sortAttentionItems sorts items by actionability desc, then timestamp desc.
func sortAttentionItems(items []panels.AttentionItem) {
	// Simple bubble sort for small N (typically <20 items)
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if shouldSwapAttentionItems(items[i], items[j]) {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
}

func shouldSwapAttentionItems(a, b panels.AttentionItem) bool {
	// action_required > interesting > background
	aRank := actionabilityRank(a.Actionability)
	bRank := actionabilityRank(b.Actionability)
	if aRank != bRank {
		return aRank < bRank // Higher rank should come first
	}
	// Same actionability: newer first
	return a.Timestamp.Before(b.Timestamp)
}

func actionabilityRank(a robot.Actionability) int {
	switch a {
	case robot.ActionabilityActionRequired:
		return 3
	case robot.ActionabilityInteresting:
		return 2
	default:
		return 1
	}
}

// fetchMetricsCmd refreshes observability metrics.
func (m *Model) fetchMetricsCmd() tea.Cmd {
	gen := m.nextGen(refreshMetrics)

	return func() tea.Msg {
		return MetricsUpdateMsg{
			Data: panels.MetricsData{},
			Gen:  gen,
		}
	}
}

// fetchHistoryCmd reads recent history
func (m *Model) fetchHistoryCmd() tea.Cmd {
	gen := m.nextGen(refreshHistory)
	session := m.session
	return func() tea.Msg {
		entries, err := history.ReadRecent(200)
		if err != nil {
			return HistoryUpdateMsg{Err: err, Gen: gen}
		}
		if session != "" && len(entries) > 0 {
			filtered := make([]history.HistoryEntry, 0, len(entries))
			for _, e := range entries {
				if e.Session == session {
					filtered = append(filtered, e)
				}
			}
			entries = filtered
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

// fetchTimelineCmd loads persisted timeline events for the session.
func (m *Model) fetchTimelineCmd() tea.Cmd {
	session := m.session
	return func() tea.Msg {
		if session == "" {
			return TimelineLoadMsg{Events: nil}
		}
		persister, err := state.GetDefaultTimelinePersister()
		if err != nil {
			return TimelineLoadMsg{Err: err}
		}
		events, err := persister.LoadTimeline(session)
		if err != nil {
			return TimelineLoadMsg{Err: err}
		}
		return TimelineLoadMsg{Events: events}
	}
}

// fetchHandoffCmd fetches the latest handoff goal/now + metadata for the session.
func (m *Model) fetchHandoffCmd() tea.Cmd {
	gen := m.nextGen(refreshHandoff)
	session := m.session
	projectDir := m.projectDir

	return func() tea.Msg {
		reader := handoff.NewReader(projectDir)
		goal, now, err := reader.ExtractGoalNow(session)
		if err != nil {
			return HandoffUpdateMsg{Goal: goal, Now: now, Err: err, Gen: gen}
		}

		h, path, err := reader.FindLatest(session)
		if err != nil {
			return HandoffUpdateMsg{Goal: goal, Now: now, Path: path, Err: err, Gen: gen}
		}

		msg := HandoffUpdateMsg{
			Goal: goal,
			Now:  now,
			Path: path,
			Gen:  gen,
		}
		if h != nil {
			msg.Age = time.Since(h.CreatedAt)
			msg.Status = h.Status
		}
		return msg
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

// fetchPTHealthStatesCmd fetches process_triage health states from the global monitor
func (m *Model) fetchPTHealthStatesCmd() tea.Cmd {
	gen := m.nextGen(refreshPTHealth)
	return func() tea.Msg {
		// Get the global monitor (created lazily if needed)
		monitor := pt.GetGlobalMonitor()
		if monitor == nil {
			return PTHealthStatesMsg{States: nil, Gen: gen}
		}

		// Get current states (thread-safe copy)
		states := monitor.GetAllStates()
		return PTHealthStatesMsg{States: states, Gen: gen}
	}
}
