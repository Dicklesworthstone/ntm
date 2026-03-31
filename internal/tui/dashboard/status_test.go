package dashboard

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	ctxmon "github.com/Dicklesworthstone/ntm/internal/context"
	pt "github.com/Dicklesworthstone/ntm/internal/integrations/pt"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
)

func TestStatusUpdateSetsPaneStateAndTimestamp(t *testing.T) {
	t.Parallel()

	m := New("session", "")
	m.panes = []tmux.Pane{
		{ID: "%1", Index: 0, Title: "session__cod_1", Type: tmux.AgentCodex},
	}
	m.paneStatus[0] = PaneStatus{}

	now := time.Now()
	msg := StatusUpdateMsg{
		Statuses: []status.AgentStatus{
			{
				PaneID:     "%1",
				State:      status.StateIdle,
				LastActive: now.Add(-1 * time.Minute),
				LastOutput: strings.Repeat("token ", 24),
				UpdatedAt:  now,
			},
		},
		Time: now,
	}

	updated, _ := m.Update(msg)
	m2 := updated.(Model)

	if got := m2.paneStatus[0].State; got != "idle" {
		t.Fatalf("expected pane state idle, got %q", got)
	}
	if m2.lastRefresh.IsZero() {
		t.Fatalf("expected lastRefresh to be set")
	}
	if len(m2.aggregateVelocityHistory) == 0 {
		t.Fatalf("expected aggregate velocity history to be recorded")
	}
	if got := m2.aggregateVelocityHistory[len(m2.aggregateVelocityHistory)-1]; got <= 0 {
		t.Fatalf("expected aggregate velocity sample > 0, got %v", got)
	}
	if got := len(m2.velocityByType[string(tmux.AgentCodex)]); got == 0 {
		t.Fatalf("expected per-type velocity history to be recorded")
	}
}

func TestStatusUpdateBatchesOllamaRefreshCmd(t *testing.T) {
	t.Parallel()

	m := New("session", "")
	m.panes = []tmux.Pane{
		{ID: "%1", Index: 0, Title: "session__ollama_1", Type: tmux.AgentOllama},
	}
	m.paneStatus[0] = PaneStatus{}

	now := time.Now()
	msg := StatusUpdateMsg{
		Statuses: []status.AgentStatus{
			{
				PaneID:    "%1",
				State:     status.StateIdle,
				UpdatedAt: now,
			},
		},
		Time: now,
	}

	updated, cmd := m.Update(msg)
	if cmd == nil {
		t.Fatal("expected batched follow-up command")
	}
	m2 := updated.(Model)
	if !m2.fetchingOllamaPS {
		t.Fatal("expected status update to schedule an Ollama refresh")
	}

	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected tea.BatchMsg, got %T", cmd())
	}
	if len(batch) != 2 {
		t.Fatalf("expected health and Ollama refresh commands, got %d", len(batch))
	}
}

func TestStatusUpdateErrorClearsStaleLiveStatus(t *testing.T) {
	t.Parallel()

	m := New("session", "")
	m.panes = []tmux.Pane{
		{ID: "%1", Index: 0, Title: "session__cod_1", Type: tmux.AgentCodex},
	}
	m.paneStatus[0] = PaneStatus{
		State:                "working",
		TokenVelocity:        120,
		LocalTokensPerSecond: 4.5,
		LocalTotalTokens:     300,
		LocalLastLatency:     150 * time.Millisecond,
		LocalAvgLatency:      200 * time.Millisecond,
		LocalMemoryBytes:     1 << 20,
		LocalTPSHistory:      []float64{2.5, 4.5},
	}
	m.agentStatuses["%1"] = status.AgentStatus{
		PaneID:     "%1",
		State:      status.StateWorking,
		LastOutput: "stale output",
	}

	updated, _ := m.Update(StatusUpdateMsg{
		Err:  errors.New("status refresh failed"),
		Time: time.Now(),
	})
	m2 := updated.(Model)

	if m2.statusFetchErr == nil {
		t.Fatal("expected status fetch error to be recorded")
	}
	ps := m2.paneStatus[0]
	if ps.State != "" {
		t.Fatalf("expected stale pane state to clear, got %q", ps.State)
	}
	if ps.TokenVelocity != 0 {
		t.Fatalf("expected stale token velocity to clear, got %v", ps.TokenVelocity)
	}
	if ps.LocalTokensPerSecond != 0 || ps.LocalTotalTokens != 0 || ps.LocalLastLatency != 0 || ps.LocalAvgLatency != 0 || ps.LocalMemoryBytes != 0 || len(ps.LocalTPSHistory) != 0 {
		t.Fatalf("expected stale local perf snapshot to clear, got %+v", ps)
	}
	if _, ok := m2.agentStatuses["%1"]; ok {
		t.Fatal("expected stale agent status snapshot to clear")
	}
	rows := BuildPaneTableRows(m2.panes, m2.agentStatuses, m2.paneStatus, nil, nil, nil, 0, m2.theme)
	if got := rows[0].Status; got != "unknown" {
		t.Fatalf("expected stale row status to clear, got %q", got)
	}
	if len(m2.aggregateVelocityHistory) == 0 {
		t.Fatal("expected aggregate velocity history sample to be recorded")
	}
	if got := m2.aggregateVelocityHistory[len(m2.aggregateVelocityHistory)-1]; got != 0 {
		t.Fatalf("expected cleared aggregate velocity sample to be 0, got %v", got)
	}
}

func TestStatusUpdateSuccessClearsMissingPaneSnapshots(t *testing.T) {
	t.Parallel()

	m := New("session", "")
	m.panes = []tmux.Pane{
		{ID: "%1", Index: 0, Title: "session__cc_1", Type: tmux.AgentClaude},
		{ID: "%2", Index: 1, Title: "session__cod_1", Type: tmux.AgentCodex},
	}
	m.paneStatus[0] = PaneStatus{State: "working", TokenVelocity: 100}
	m.paneStatus[1] = PaneStatus{
		State:                "idle",
		TokenVelocity:        80,
		LocalTokensPerSecond: 3.5,
		LocalTotalTokens:     200,
		LocalTPSHistory:      []float64{3.5},
	}
	m.agentStatuses["%1"] = status.AgentStatus{PaneID: "%1", State: status.StateWorking}
	m.agentStatuses["%2"] = status.AgentStatus{PaneID: "%2", State: status.StateIdle, LastOutput: "stale"}

	now := time.Now()
	updated, _ := m.Update(StatusUpdateMsg{
		Statuses: []status.AgentStatus{
			{
				PaneID:    "%1",
				State:     status.StateIdle,
				UpdatedAt: now,
			},
		},
		Time: now,
	})
	m2 := updated.(Model)

	if got := m2.paneStatus[0].State; got != "idle" {
		t.Fatalf("expected pane 0 state to refresh, got %q", got)
	}
	if _, ok := m2.agentStatuses["%1"]; !ok {
		t.Fatal("expected pane 0 status snapshot to remain present")
	}
	if ps := m2.paneStatus[1]; ps.State != "" || ps.TokenVelocity != 0 || ps.LocalTokensPerSecond != 0 || ps.LocalTotalTokens != 0 || len(ps.LocalTPSHistory) != 0 {
		t.Fatalf("expected missing pane status snapshot to clear, got %+v", ps)
	}
	if _, ok := m2.agentStatuses["%2"]; ok {
		t.Fatal("expected missing pane agent status snapshot to clear")
	}
	rows := BuildPaneTableRows(m2.panes, m2.agentStatuses, m2.paneStatus, nil, nil, nil, 0, m2.theme)
	if got := rows[1].Status; got != "unknown" {
		t.Fatalf("expected missing pane row status to clear, got %q", got)
	}
}

func TestSessionDataUpdateBatchesOllamaRefreshCmd(t *testing.T) {
	t.Parallel()

	m := New("session", "")
	m.startupWarmupDone = true
	m.panes = []tmux.Pane{
		{ID: "%1", Index: 0, Title: "session__ollama_1_mistral", Type: tmux.AgentOllama, Variant: "mistral:7b"},
	}
	m.paneStatus[0] = PaneStatus{}

	now := time.Now()
	msg := SessionDataWithOutputMsg{
		Panes: []tmux.Pane{
			{ID: "%1", Index: 0, Title: "session__ollama_1_mistral", Type: tmux.AgentOllama, Variant: "mistral:7b"},
		},
		Outputs: []PaneOutputData{
			{
				PaneID:       "%1",
				PaneIndex:    0,
				LastActivity: now,
				Output:       "hello from ollama",
				AgentType:    string(tmux.AgentOllama),
			},
		},
		Duration: 10 * time.Millisecond,
	}

	updated, cmd := m.Update(msg)
	if cmd == nil {
		t.Fatal("expected batched follow-up command")
	}
	m2 := updated.(Model)
	if !m2.fetchingOllamaPS {
		t.Fatal("expected session data update to schedule an Ollama refresh")
	}

	switch result := cmd().(type) {
	case tea.BatchMsg:
		if len(result) != 1 {
			t.Fatalf("expected Ollama refresh command to survive warmup batching, got %d", len(result))
		}
	case OllamaPSResultMsg:
		// A single-command batch may collapse to the direct command result.
	default:
		t.Fatalf("expected Ollama refresh result, got %T", result)
	}
}

func TestPendingRotationsUpdateErrorClearsStalePendingData(t *testing.T) {
	t.Parallel()

	m := New("session", "")
	stale := []*ctxmon.PendingRotation{
		{AgentID: "agent1", TimeoutAt: time.Now().Add(time.Minute)},
	}
	m.pendingRotations = stale
	m.rotationConfirmPanel.SetData(stale, nil)

	updated, _ := m.Update(PendingRotationsUpdateMsg{
		Err: errors.New("pending rotations fetch failed"),
	})
	m2 := updated.(Model)

	if m2.pendingRotationsErr == nil {
		t.Fatal("expected pending rotations error to be recorded")
	}
	if len(m2.pendingRotations) != 0 {
		t.Fatalf("expected stale pending rotations to be cleared, got %d", len(m2.pendingRotations))
	}
	if !m2.rotationConfirmPanel.HasError() {
		t.Fatal("expected rotation confirm panel to surface the error")
	}
	if m2.rotationConfirmPanel.HasPending() {
		t.Fatal("expected rotation confirm panel pending state to be cleared on error")
	}
	if pending := m2.rotationConfirmPanel.SelectedPending(); pending != nil {
		t.Fatalf("expected no selected pending rotation on error, got %v", pending.AgentID)
	}
}

func TestHealthUpdateErrorClearsStalePaneHealthDetails(t *testing.T) {
	t.Parallel()

	m := New("session", "")
	m.panes = []tmux.Pane{
		{ID: "%1", Index: 0, Title: "session__cc_1", Type: tmux.AgentClaude},
		{ID: "%2", Index: 1, Title: "session__cod_1", Type: tmux.AgentCodex},
	}
	m.paneStatus[0] = PaneStatus{
		HealthStatus:  "error",
		HealthIssues:  []string{"rate limit"},
		RestartCount:  3,
		UptimeSeconds: 120,
	}
	m.paneStatus[1] = PaneStatus{
		HealthStatus:  "warning",
		HealthIssues:  []string{"slow response"},
		RestartCount:  1,
		UptimeSeconds: 60,
	}

	updated, _ := m.Update(HealthUpdateMsg{Err: errors.New("health refresh failed")})
	m2 := updated.(Model)

	for _, idx := range []int{0, 1} {
		ps := m2.paneStatus[idx]
		if ps.HealthStatus != "" {
			t.Fatalf("expected pane %d health status to clear, got %q", idx, ps.HealthStatus)
		}
		if len(ps.HealthIssues) != 0 {
			t.Fatalf("expected pane %d health issues to clear, got %v", idx, ps.HealthIssues)
		}
		if ps.RestartCount != 0 {
			t.Fatalf("expected pane %d restart count to clear, got %d", idx, ps.RestartCount)
		}
		if ps.UptimeSeconds != 0 {
			t.Fatalf("expected pane %d uptime to clear, got %d", idx, ps.UptimeSeconds)
		}
	}
}

func TestHealthUpdateSuccessClearsMissingPaneHealthDetails(t *testing.T) {
	t.Parallel()

	m := New("session", "")
	m.panes = []tmux.Pane{
		{ID: "%1", Index: 0, Title: "session__cc_1", Type: tmux.AgentClaude},
		{ID: "%2", Index: 1, Title: "session__cod_1", Type: tmux.AgentCodex},
	}
	m.paneStatus[0] = PaneStatus{
		HealthStatus:  "warning",
		HealthIssues:  []string{"old warning"},
		RestartCount:  2,
		UptimeSeconds: 45,
	}
	m.paneStatus[1] = PaneStatus{
		HealthStatus:  "error",
		HealthIssues:  []string{"old error"},
		RestartCount:  5,
		UptimeSeconds: 90,
	}

	updated, _ := m.Update(HealthUpdateMsg{
		Health: map[string]PaneHealthInfo{
			"%1": {
				Status:       "ok",
				Issues:       []string{"fresh"},
				RestartCount: 1,
				Uptime:       300,
			},
		},
	})
	m2 := updated.(Model)

	if got := m2.paneStatus[0].HealthStatus; got != "ok" {
		t.Fatalf("expected pane 0 health status to update, got %q", got)
	}
	if got := m2.paneStatus[0].HealthIssues; len(got) != 1 || got[0] != "fresh" {
		t.Fatalf("expected pane 0 health issues to update, got %v", got)
	}
	if got := m2.paneStatus[0].RestartCount; got != 1 {
		t.Fatalf("expected pane 0 restart count to update, got %d", got)
	}
	if got := m2.paneStatus[0].UptimeSeconds; got != 300 {
		t.Fatalf("expected pane 0 uptime to update, got %d", got)
	}

	if ps := m2.paneStatus[1]; ps.HealthStatus != "" || len(ps.HealthIssues) != 0 || ps.RestartCount != 0 || ps.UptimeSeconds != 0 {
		t.Fatalf("expected pane 1 stale health state to clear, got %+v", ps)
	}
}

func TestPTHealthStatesNilClearsStaleClassification(t *testing.T) {
	t.Parallel()

	m := New("session", "")
	m.panes = []tmux.Pane{
		{ID: "%1", Index: 0, Title: "session__cc_1", Type: tmux.AgentClaude},
	}
	m.healthStates = map[string]*pt.AgentState{
		"session__cc_1": {
			Pane:           "session__cc_1",
			Classification: pt.ClassStuck,
			Since:          time.Now().Add(-10 * time.Minute),
		},
	}

	before := BuildPaneTableRows(m.panes, nil, m.paneStatus, nil, nil, m.healthStates, 0, m.theme)
	if got := before[0].HealthClass; got != pt.ClassStuck {
		t.Fatalf("expected initial health class stuck, got %q", got)
	}

	updated, _ := m.Update(PTHealthStatesMsg{States: nil})
	m2 := updated.(Model)
	if m2.healthStates != nil {
		t.Fatalf("expected PT health states to clear, got %#v", m2.healthStates)
	}

	after := BuildPaneTableRows(m2.panes, nil, m2.paneStatus, nil, nil, m2.healthStates, 0, m2.theme)
	if got := after[0].HealthClass; got != pt.ClassUnknown {
		t.Fatalf("expected stale PT classification to clear, got %q", got)
	}
}

func TestViewShowsLoadingWhenSessionFetchInFlight(t *testing.T) {
	t.Parallel()

	m := New("session", "")
	m.width = 80
	m.height = 30
	m.tier = layout.TierForWidth(m.width)

	// Simulate initial load: no panes yet, fetch in flight.
	m.panes = nil
	m.err = nil
	m.fetchingSession = true
	m.lastPaneFetch = time.Now().Add(-750 * time.Millisecond)

	plain := status.StripANSI(m.View())
	if strings.Contains(plain, "No panes found") {
		t.Fatalf("expected loading state, got empty state")
	}
	if !strings.Contains(plain, "Fetching panes") {
		t.Fatalf("expected loading copy to mention fetching panes")
	}
}

func TestViewRendersPanesEvenWhenLastSessionFetchErrored(t *testing.T) {
	t.Parallel()

	m := New("session", "")
	m.width = 120
	m.height = 30
	m.tier = layout.TierForWidth(m.width)

	m.panes = []tmux.Pane{
		{ID: "%1", Index: 0, Title: "session__cod_1", Type: tmux.AgentCodex},
	}
	m.err = errors.New("tmux refresh failed")

	plain := status.StripANSI(m.View())
	if !strings.Contains(plain, "tmux refresh failed") {
		t.Fatalf("expected error to be surfaced in view")
	}
	if !strings.Contains(plain, "session__cod_1") {
		t.Fatalf("expected panes to still render when session fetch errors")
	}
}

func TestPlanPaneCaptures_PrioritizesSelectedAndNewActivity(t *testing.T) {
	t.Parallel()

	now := time.Now()
	panes := []tmux.PaneActivity{
		{Pane: tmux.Pane{ID: "%0", Index: 0, Type: tmux.AgentUser}, LastActivity: now},
		{Pane: tmux.Pane{ID: "%1", Index: 1, Type: tmux.AgentCodex}, LastActivity: now.Add(-10 * time.Second)},
		{Pane: tmux.Pane{ID: "%2", Index: 2, Type: tmux.AgentClaude}, LastActivity: now.Add(-1 * time.Second)},
		{Pane: tmux.Pane{ID: "%3", Index: 3, Type: tmux.AgentGemini}, LastActivity: now.Add(-5 * time.Second)},
	}

	lastCaptured := map[string]time.Time{
		"%1": now,                        // no new activity
		"%2": now,                        // selected, no new activity
		"%3": now.Add(-20 * time.Second), // new activity since last capture
	}

	plan := planPaneCaptures(panes, "%2", lastCaptured, 2, 0)
	if len(plan.Targets) != 2 {
		t.Fatalf("expected 2 capture targets, got %d", len(plan.Targets))
	}
	if plan.Targets[0].Pane.ID != "%2" {
		t.Fatalf("expected selected pane first, got %s", plan.Targets[0].Pane.ID)
	}
	if plan.Targets[1].Pane.ID != "%3" {
		t.Fatalf("expected pane with new activity second, got %s", plan.Targets[1].Pane.ID)
	}
}

func TestPlanPaneCaptures_RoundRobinAdvancesCursor(t *testing.T) {
	t.Parallel()

	now := time.Now()
	panes := []tmux.PaneActivity{
		{Pane: tmux.Pane{ID: "%1", Index: 1, Type: tmux.AgentCodex}, LastActivity: now.Add(-10 * time.Second)},
		{Pane: tmux.Pane{ID: "%2", Index: 2, Type: tmux.AgentClaude}, LastActivity: now.Add(-10 * time.Second)},
		{Pane: tmux.Pane{ID: "%3", Index: 3, Type: tmux.AgentGemini}, LastActivity: now.Add(-10 * time.Second)},
	}

	lastCaptured := map[string]time.Time{
		"%1": now,
		"%2": now,
		"%3": now,
	}

	plan := planPaneCaptures(panes, "", lastCaptured, 2, 1)
	if len(plan.Targets) != 2 {
		t.Fatalf("expected 2 capture targets, got %d", len(plan.Targets))
	}
	if plan.Targets[0].Pane.ID != "%2" || plan.Targets[1].Pane.ID != "%3" {
		t.Fatalf("unexpected round-robin targets: %s, %s", plan.Targets[0].Pane.ID, plan.Targets[1].Pane.ID)
	}
	if plan.NextCursor != 0 {
		t.Fatalf("expected NextCursor=0, got %d", plan.NextCursor)
	}
}

func TestSessionDataUpdate_SortsPanesAndKeepsSelection(t *testing.T) {
	t.Parallel()

	m := New("session", "")
	m.panes = []tmux.Pane{
		{ID: "%0", Index: 0, Title: "session__user_0", Type: tmux.AgentUser},
		{ID: "%1", Index: 1, Title: "session__cod_1", Type: tmux.AgentCodex},
		{ID: "%2", Index: 2, Title: "session__cc_1", Type: tmux.AgentClaude},
	}
	m.cursor = 2

	msg := SessionDataWithOutputMsg{
		Panes: []tmux.Pane{
			{ID: "%2", Index: 2, Title: "session__cc_1", Type: tmux.AgentClaude},
			{ID: "%0", Index: 0, Title: "session__user_0", Type: tmux.AgentUser},
			{ID: "%1", Index: 1, Title: "session__cod_1", Type: tmux.AgentCodex},
		},
		Duration:          10 * time.Millisecond,
		NextCaptureCursor: 0,
	}

	updated, _ := m.Update(msg)
	m2 := updated.(Model)

	if len(m2.panes) != 3 {
		t.Fatalf("expected 3 panes, got %d", len(m2.panes))
	}
	if m2.panes[0].ID != "%0" || m2.panes[1].ID != "%1" || m2.panes[2].ID != "%2" {
		t.Fatalf("expected panes sorted by index, got %s %s %s", m2.panes[0].ID, m2.panes[1].ID, m2.panes[2].ID)
	}
	if m2.panes[m2.cursor].ID != "%2" {
		t.Fatalf("expected selection to remain on %%2, got %s", m2.panes[m2.cursor].ID)
	}
}

func TestSeedInitialPanes_SortsAndMarksSnapshotComplete(t *testing.T) {
	t.Parallel()

	m := New("session", "")
	m.seedInitialPanes([]tmux.PaneActivity{
		{Pane: tmux.Pane{ID: "%2", Index: 2, Title: "session__cc_1", Type: tmux.AgentClaude}},
		{Pane: tmux.Pane{ID: "%0", Index: 0, Title: "session__user_0", Type: tmux.AgentUser}},
		{Pane: tmux.Pane{ID: "%1", Index: 1, Title: "session__cod_1", Type: tmux.AgentCodex}},
	})

	if !m.initialPaneSnapshotDone {
		t.Fatalf("expected initialPaneSnapshotDone to be true")
	}
	if len(m.panes) != 3 {
		t.Fatalf("expected 3 panes, got %d", len(m.panes))
	}
	if m.panes[0].ID != "%0" || m.panes[1].ID != "%1" || m.panes[2].ID != "%2" {
		t.Fatalf("expected seeded panes sorted by index, got %s %s %s", m.panes[0].ID, m.panes[1].ID, m.panes[2].ID)
	}
	if m.claudeCount != 1 || m.codexCount != 1 || m.userCount != 1 {
		t.Fatalf("expected seeded counts Claude=1 Codex=1 User=1, got C=%d O=%d U=%d", m.claudeCount, m.codexCount, m.userCount)
	}
}
