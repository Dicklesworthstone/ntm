package dashboard

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/pipeline"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/dashboard/panels"
	"github.com/Dicklesworthstone/ntm/internal/workflow"
	"github.com/Dicklesworthstone/ntm/internal/worktrees"
)

func testDashboardWithPanes(width, height int) Model {
	m := New("ops-test", "/tmp/ops-test")
	m.fetchingSession = false
	m.panes = []tmux.Pane{
		{ID: "%1", Index: 1, Title: "ops-test__cc_1", Type: tmux.AgentClaude},
		{ID: "%2", Index: 2, Title: "ops-test__cod_1", Type: tmux.AgentCodex},
	}
	updated, _ := m.handleWindowSize(tea.WindowSizeMsg{Width: width, Height: height})
	return updated.(Model)
}

func TestDashboardOpsPipelinesPanelToggleCancelAndStates(t *testing.T) {
	oldFetch := dashboardFetchPipelinesData
	oldCancel := dashboardCancelPipeline
	defer func() {
		dashboardFetchPipelinesData = oldFetch
		dashboardCancelPipeline = oldCancel
	}()

	now := time.Now()
	fixture := panels.PipelinesData{
		Runs: []*pipeline.PipelineExecution{
			{
				RunID:       "run-active-42",
				WorkflowID:  "ship-release",
				Session:     "ops-test",
				Status:      "running",
				CurrentStep: "verify",
				StartedAt:   now.Add(-2 * time.Minute),
				Progress: pipeline.PipelineProgress{
					Completed: 2,
					Total:     4,
					Percent:   50,
				},
			},
			{
				RunID:      "run-fail-9",
				WorkflowID: "nightly-audit",
				Session:    "ops-test",
				Status:     "failed",
				StartedAt:  now.Add(-10 * time.Minute),
				Error:      "step lint exited 1",
				Progress: pipeline.PipelineProgress{
					Completed: 1,
					Total:     3,
					Percent:   33,
				},
			},
		},
		Warnings: []string{"corrupt.json unreadable"},
	}
	dashboardFetchPipelinesData = func(projectDir string) panels.PipelinesData {
		return fixture
	}
	var cancelledRunID string
	dashboardCancelPipeline = func(ctx context.Context, projectDir, runID string) error {
		cancelledRunID = runID
		return nil
	}

	m := testDashboardWithPanes(180, 50)

	// Toggle Pipelines panel on with 'P'.
	updated, cmd := m.Update(keyRune('P'))
	next := updated.(Model)
	if !next.showPipelinesPanel {
		t.Fatal("expected 'P' to show the pipelines panel")
	}
	if cmd == nil {
		t.Fatal("expected pipelines fetch command on toggle")
	}
	msg := cmd()
	pMsg, ok := msg.(PipelinesStatusMsg)
	if !ok {
		t.Fatalf("fetch returned %T, want PipelinesStatusMsg", msg)
	}
	updated, _ = next.Update(pMsg)
	next = updated.(Model)

	rendered := status.StripANSI(next.renderSidebar(72, 32))
	for _, want := range []string{"Pipelines", "ship-release", "2/4", "nightly-audit", "step lint exited 1"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("pipelines panel missing %q:\n%s", want, rendered)
		}
	}

	// Focus sidebar and press 'x' to cancel the selected running pipeline.
	next.focusedPanel = PanelSidebar
	next.sidebarActivePanel = next.pipelinesPanel.Config().ID
	next.syncSidebarPanelFocusState()

	updated, cmd = next.Update(keyRune('x'))
	next = updated.(Model)
	if cmd == nil {
		t.Fatal("expected cancel action cmd on 'x' for running pipeline")
	}
	cancelReq := cmd()
	actionMsg, ok := cancelReq.(panels.PipelineCancelActionMsg)
	if !ok || actionMsg.RunID != "run-active-42" {
		t.Fatalf("expected PipelineCancelActionMsg{RunID: run-active-42}, got %#v", cancelReq)
	}
	updated, cmd = next.Update(actionMsg)
	next = updated.(Model)
	if cmd == nil {
		t.Fatal("expected async cancel execution cmd")
	}
	resMsg := cmd()
	if cancelledRunID != "run-active-42" {
		t.Fatalf("dashboardCancelPipeline called with %q, want run-active-42", cancelledRunID)
	}
	updated, _ = next.Update(resMsg)
	next = updated.(Model)
	if next.toasts == nil || next.toasts.Count() == 0 {
		t.Fatal("expected toast confirmation after pipeline cancel result")
	}

	// Verify empty and error states render cleanly.
	next.pipelinesPanel.SetData(panels.PipelinesData{})
	emptyView := status.StripANSI(next.pipelinesPanel.View())
	if !strings.Contains(emptyView, "No pipeline runs") {
		t.Errorf("expected empty pipelines state, got:\n%s", emptyView)
	}
	next.pipelinesPanel.SetData(panels.PipelinesData{Error: errors.New("permission denied")})
	errView := status.StripANSI(next.pipelinesPanel.View())
	if !strings.Contains(errView, "permission denied") {
		t.Errorf("expected error pipelines state, got:\n%s", errView)
	}

	// Toggle off.
	updated, _ = next.Update(keyRune('P'))
	next = updated.(Model)
	if next.showPipelinesPanel {
		t.Fatal("expected second 'P' to hide pipelines panel")
	}
}

func TestDashboardOpsApprovalsPanelToggleApproveDenyAndDegraded(t *testing.T) {
	oldFetch := dashboardFetchApprovalsData
	oldDecide := dashboardDecideApproval
	defer func() {
		dashboardFetchApprovalsData = oldFetch
		dashboardDecideApproval = oldDecide
	}()

	now := time.Now()
	fixture := panels.ApprovalsData{
		Available: true,
		Pending: []state.Approval{
			{
				ID:          "apr-slb-01",
				Action:      "git.force_push",
				Resource:    "origin/main",
				RequestedBy: "codex-1",
				RequiresSLB: true,
				CreatedAt:   now.Add(-1 * time.Minute),
				ExpiresAt:   now.Add(15 * time.Minute),
				Status:      state.ApprovalPending,
			},
			{
				ID:          "apr-pol-02",
				Action:      "bead.force_release",
				Resource:    "bd-123",
				RequestedBy: "claude-1",
				RequiresSLB: false,
				CreatedAt:   now.Add(-30 * time.Second),
				ExpiresAt:   now.Add(10 * time.Minute),
				Status:      state.ApprovalPending,
			},
		},
	}
	dashboardFetchApprovalsData = func() panels.ApprovalsData { return fixture }

	type decisionCall struct {
		id      string
		approve bool
	}
	var calls []decisionCall
	dashboardDecideApproval = func(ctx context.Context, approvalID string, approve bool, approver string) error {
		calls = append(calls, decisionCall{id: approvalID, approve: approve})
		return nil
	}

	m := testDashboardWithPanes(180, 50)
	updated, cmd := m.Update(keyRune('S'))
	next := updated.(Model)
	if !next.showApprovalsPanel || cmd == nil {
		t.Fatal("expected 'S' to open approvals panel and issue fetch cmd")
	}
	updated, _ = next.Update(cmd())
	next = updated.(Model)

	rendered := status.StripANSI(next.renderSidebar(72, 32))
	for _, want := range []string{"Safety & Approvals", "SLB", "git.force_push", "bead.force_release"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("approvals panel missing %q:\n%s", want, rendered)
		}
	}

	next.focusedPanel = PanelSidebar
	next.sidebarActivePanel = next.approvalsPanel.Config().ID
	next.syncSidebarPanelFocusState()

	// Approve first item with 'y'.
	updated, cmd = next.Update(keyRune('y'))
	next = updated.(Model)
	if cmd == nil {
		t.Fatal("expected approve action cmd on 'y'")
	}
	updated, cmd = next.Update(cmd())
	next = updated.(Model)
	if cmd == nil {
		t.Fatal("expected async decision cmd")
	}
	updated, _ = next.Update(cmd())
	next = updated.(Model)

	// Move down with 'j' and deny second item with 'x'.
	updated, _ = next.Update(keyRune('j'))
	next = updated.(Model)
	updated, cmd = next.Update(keyRune('x'))
	next = updated.(Model)
	if cmd == nil {
		t.Fatal("expected deny action cmd on 'x'")
	}
	updated, cmd = next.Update(cmd())
	next = updated.(Model)
	updated, _ = next.Update(cmd())
	next = updated.(Model)

	if len(calls) != 2 {
		t.Fatalf("expected 2 approval decisions, got %d (%+v)", len(calls), calls)
	}
	if calls[0].id != "apr-slb-01" || !calls[0].approve {
		t.Errorf("first decision = %+v, want apr-slb-01 approve=true", calls[0])
	}
	if calls[1].id != "apr-pol-02" || calls[1].approve {
		t.Errorf("second decision = %+v, want apr-pol-02 approve=false", calls[1])
	}

	// Degraded (store unavailable) state.
	next.approvalsPanel.SetData(panels.ApprovalsData{Available: false})
	degradedView := status.StripANSI(next.approvalsPanel.View())
	if !strings.Contains(degradedView, "unavailable") {
		t.Errorf("expected unavailable approvals view, got:\n%s", degradedView)
	}
}

func TestDashboardOpsWorktreesCheckpointsPanelToggleSaveRestoreAndStates(t *testing.T) {
	oldFetch := dashboardFetchWorktreesCheckpointsData
	oldRestore := dashboardRestoreCheckpoint
	defer func() {
		dashboardFetchWorktreesCheckpointsData = oldFetch
		dashboardRestoreCheckpoint = oldRestore
	}()

	now := time.Now()
	fixture := panels.WorktreesCheckpointsData{
		GitAvailable: true,
		Worktrees: []*worktrees.WorktreeInfo{
			{AgentName: "cc_1", BranchName: "ntm/ops-test/cc_1", Created: true, Path: "/tmp/wt/cc_1"},
			{AgentName: "cod_1", BranchName: "ntm/ops-test/cod_1", Created: true, Error: "dirty index"},
		},
		Checkpoints: []*checkpoint.Checkpoint{
			{ID: "cp-2026-01", Name: "before-refactor", CreatedAt: now.Add(-5 * time.Minute), PaneCount: 2},
			{ID: "cp-2026-00", Name: "baseline", CreatedAt: now.Add(-30 * time.Minute), PaneCount: 2},
		},
	}
	dashboardFetchWorktreesCheckpointsData = func(ctx context.Context, projectDir, session string) panels.WorktreesCheckpointsData {
		return fixture
	}
	var restoredID string
	dashboardRestoreCheckpoint = func(ctx context.Context, session, checkpointID string) (string, error) {
		restoredID = checkpointID
		return session + "-restored-copy", nil
	}

	m := testDashboardWithPanes(180, 50)
	updated, cmd := m.Update(keyRune('W'))
	next := updated.(Model)
	if !next.showWorktreesPanel || cmd == nil {
		t.Fatal("expected 'W' to open worktrees/checkpoints panel and fetch")
	}
	updated, _ = next.Update(cmd())
	next = updated.(Model)

	rendered := status.StripANSI(next.renderSidebar(72, 32))
	for _, want := range []string{"Worktrees & Checkpoints", "cc_1", "before-refactor"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("worktrees/checkpoints panel missing %q:\n%s", want, rendered)
		}
	}

	next.focusedPanel = PanelSidebar
	next.sidebarActivePanel = next.worktreesPanel.Config().ID
	next.syncSidebarPanelFocusState()

	// 'c' emits save action rather than triggering global ContextRefresh.
	updated, cmd = next.Update(keyRune('c'))
	next = updated.(Model)
	if cmd == nil {
		t.Fatal("expected save checkpoint action cmd on 'c'")
	}
	saveMsg, ok := cmd().(panels.WorktreeCheckpointActionMsg)
	if !ok || saveMsg.Action != "save" {
		t.Fatalf("expected WorktreeCheckpointActionMsg{Action: save}, got %#v", saveMsg)
	}

	// 'enter' emits restore action for selected checkpoint and runs restoreCheckpointCmd.
	updated, cmd = next.Update(tea.KeyMsg{Type: tea.KeyEnter})
	next = updated.(Model)
	if cmd == nil {
		t.Fatal("expected restore action cmd on enter")
	}
	restoreReq := cmd()
	updated, cmd = next.Update(restoreReq)
	next = updated.(Model)
	if cmd == nil {
		t.Fatal("expected async restore execution cmd")
	}
	resMsg := cmd()
	if restoredID != "cp-2026-01" {
		t.Fatalf("dashboardRestoreCheckpoint called with %q, want cp-2026-01", restoredID)
	}
	updated, _ = next.Update(resMsg)
	next = updated.(Model)
	if next.toasts == nil || next.toasts.Count() == 0 {
		t.Fatal("expected toast confirmation after checkpoint restore")
	}
}

func TestDashboardOpsMailInboxPanelToggleUrgentSortAndPreview(t *testing.T) {
	m := testDashboardWithPanes(180, 50)
	now := time.Now()
	readTime := agentmail.FlexTime{Time: now.Add(-2 * time.Minute)}
	gen := m.nextGen(refreshAgentMailInbox)

	updated, _ := m.Update(AgentMailInboxSummaryMsg{
		ProjectKey: "/tmp/ops-test",
		AgentMap:   map[string]string{"%1": "BlueLake", "%2": "RedStone"},
		Inboxes: map[string][]agentmail.InboxMessage{
			"%1": {
				{ID: 1, From: "GreenCove", Subject: "Read status note", Importance: "normal", ReadAt: &readTime, CreatedTS: agentmail.FlexTime{Time: now.Add(-5 * time.Minute)}},
				{ID: 2, From: "AmberPeak", Subject: "URGENT lock conflict", Importance: "urgent", BodyMD: "Release internal/cli/root.go immediately", CreatedTS: agentmail.FlexTime{Time: now.Add(-1 * time.Minute)}},
			},
			"%2": {
				{ID: 3, From: "BlueLake", Subject: "Normal unread ping", Importance: "normal", CreatedTS: agentmail.FlexTime{Time: now}},
			},
		},
		Gen: gen,
	})
	next := updated.(Model)
	next.agentMailAvailable = true

	// Toggle Mail Inbox panel with 'I'.
	updated, _ = next.Update(keyRune('I'))
	next = updated.(Model)
	if !next.showMailInboxPanel {
		t.Fatal("expected 'I' to show mail inbox panel")
	}

	rendered := status.StripANSI(next.renderSidebar(72, 32))
	for _, want := range []string{"Agent Mail Inbox", "URGENT lock conflict", "AmberPeak", "Normal unread ping"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("mail inbox panel missing %q:\n%s", want, rendered)
		}
	}
	if strings.Index(rendered, "URGENT lock conflict") > strings.Index(rendered, "Normal unread ping") {
		t.Errorf("expected urgent message sorted before normal unread message:\n%s", rendered)
	}

	// Pressing enter expands inline preview snippet.
	next.focusedPanel = PanelSidebar
	next.sidebarActivePanel = next.mailInboxPanel.Config().ID
	next.syncSidebarPanelFocusState()
	updated, _ = next.Update(tea.KeyMsg{Type: tea.KeyEnter})
	next = updated.(Model)
	expanded := status.StripANSI(next.renderSidebar(72, 32))
	if !strings.Contains(expanded, "Release internal/cli/root.go") {
		t.Errorf("expected expanded message body preview after enter, got:\n%s", expanded)
	}
}

func TestDashboardOpsPanelsResponsiveLayouts(t *testing.T) {
	sizes := []struct {
		name   string
		width  int
		height int
	}{
		{name: "narrow_80x24", width: 80, height: 24},
		{name: "split_120x40", width: 120, height: 40},
		{name: "ultra_180x50", width: 180, height: 50},
	}

	for _, sz := range sizes {
		t.Run(sz.name, func(t *testing.T) {
			m := testDashboardWithPanes(sz.width, sz.height)
			m.showPipelinesPanel = true
			m.sidebarActivePanel = m.pipelinesPanel.Config().ID
			m.pipelinesPanel.SetData(panels.PipelinesData{
				Runs: []*pipeline.PipelineExecution{
					{
						RunID:      "run-resp-1",
						WorkflowID: "deploy-check",
						Status:     "running",
						Progress:   pipeline.PipelineProgress{Completed: 1, Total: 2, Percent: 50},
					},
				},
			})

			view := status.StripANSI(m.View())
			if strings.TrimSpace(view) == "" {
				t.Fatalf("View() returned empty string at %dx%d", sz.width, sz.height)
			}
			if !strings.Contains(view, "Pipelines") {
				t.Errorf("View() at %dx%d missing Pipelines panel:\n%s", sz.width, sz.height, view)
			}
			if h := lipgloss.Height(view); h > sz.height+4 {
				t.Errorf("View() height=%d grossly exceeded terminal height=%d", h, sz.height)
			}
			t.Logf("%s rendered %dx%d cleanly", sz.name, lipgloss.Width(view), lipgloss.Height(view))
		})
	}
}

func TestDashboardOpsPanelsJoinSidebarNavigationAndHelp(t *testing.T) {
	m := testDashboardWithPanes(180, 50)

	for _, ref := range m.sidebarInteractivePanels() {
		switch ref.id {
		case "pipelines", "approvals", "worktrees_checkpoints", "mail_inbox":
			t.Fatalf("operational panel %q registered while hidden", ref.id)
		}
	}

	m.showPipelinesPanel = true
	m.showApprovalsPanel = true
	m.showWorktreesPanel = true
	m.showMailInboxPanel = true
	m.focusedPanel = PanelSidebar

	refs := m.sidebarInteractivePanels()
	visited := map[string]bool{}
	next := m
	for range refs {
		updated, _ := next.Update(keyRune('J'))
		next = updated.(Model)
		visited[next.sidebarActivePanelID()] = true
	}
	for _, id := range []string{"pipelines", "approvals", "worktrees_checkpoints", "mail_inbox"} {
		if !visited[id] {
			t.Errorf("sidebar 'J' navigation never reached panel %q (visited=%v)", id, visited)
		}
	}

	listed := map[string]string{}
	for _, row := range dashKeys.FullHelp() {
		for _, b := range row {
			listed[b.Help().Key] = b.Help().Desc
		}
	}
	for _, k := range []string{"P", "S", "W", "I"} {
		if listed[k] == "" {
			t.Errorf("FullHelp() missing operational panel toggle %q", k)
		}
	}
}

func TestDashboardWaveF5ReliabilityAndParityFixes(t *testing.T) {
	t.Run("D1_scheduleRefreshes_runs_metrics_routing_history_files_when_contextDue", func(t *testing.T) {
		m := testDashboardWithPanes(160, 48)
		m.fetchingContext = false
		m.fetchingMetrics = false
		m.fetchingRouting = false
		m.fetchingHistory = false
		m.fetchingFileChanges = false
		past := time.Now().Add(-time.Hour)
		m.lastContextFetch = past
		m.lastPaneFetch = time.Now()

		cmds := m.scheduleRefreshes(time.Now())
		if len(cmds) == 0 {
			t.Fatal("scheduleRefreshes returned no commands when contextDue")
		}
		if !m.fetchingContext || !m.fetchingMetrics || !m.fetchingRouting || !m.fetchingHistory || !m.fetchingFileChanges {
			t.Fatalf("D1 bug: secondary context refreshes were skipped: context=%v metrics=%v routing=%v history=%v files=%v",
				m.fetchingContext, m.fetchingMetrics, m.fetchingRouting, m.fetchingHistory, m.fetchingFileChanges)
		}
	})

	t.Run("D2_and_D6_session_update_rebuilds_paneList_and_prunes_stale_caches", func(t *testing.T) {
		m := testDashboardWithPanes(160, 48)
		m.ensureLocalPerfTracker("%999")
		m.velocityByPaneID["%999"] = velocitySample{tokens: 100}
		m.costInputTokens["%999"] = 500
		m.routingScores = map[string]RoutingScore{"%999": {Score: 80}}

		gen := m.nextGen(refreshSession)
		updated, _ := m.Update(SessionDataWithOutputMsg{
			Gen: gen,
			Panes: []tmux.Pane{
				{ID: "%1", Index: 1, Title: "ops-test__pi_1", Type: tmux.AgentPi},
				{ID: "%2", Index: 2, Title: "ops-test__cc_1", Type: tmux.AgentClaude},
			},
		})
		next := updated.(Model)
		if got := len(next.paneList.Items()); got != 2 {
			t.Fatalf("paneList.Items() len = %d, want 2 after SessionDataWithOutputMsg", got)
		}
		if next.piCount != 1 || next.claudeCount != 1 {
			t.Fatalf("counts = (pi=%d, cc=%d), want (1, 1)", next.piCount, next.claudeCount)
		}
		if _, leaked := next.localPerfByPaneID["%999"]; leaked {
			t.Fatal("stale pane %999 leaked in localPerfByPaneID")
		}
		if _, leaked := next.velocityByPaneID["%999"]; leaked {
			t.Fatal("stale pane %999 leaked in velocityByPaneID")
		}
		if _, leaked := next.costInputTokens["%999"]; leaked {
			t.Fatal("stale pane %999 leaked in costInputTokens")
		}
		if _, leaked := next.routingScores["%999"]; leaked {
			t.Fatal("stale pane %999 leaked in routingScores")
		}
	})

	t.Run("D3_config_watcher_single_start_and_cleanup_unblocks_subscriber", func(t *testing.T) {
		m := New("cfg-test", t.TempDir())
		if cmd1 := m.startConfigWatcher(); cmd1 == nil {
			t.Fatal("first startConfigWatcher returned nil")
		}
		if cmd2 := m.startConfigWatcher(); cmd2 != nil {
			t.Fatal("second startConfigWatcher must be a no-op")
		}
		subCmd := m.subscribeToConfig()
		done := make(chan tea.Msg, 1)
		go func() { done <- subCmd() }()
		m.cleanup()
		select {
		case msg := <-done:
			if msg != nil {
				t.Fatalf("expected nil msg after cleanup, got %#v", msg)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("subscribeToConfig leaked goroutine after cleanup()")
		}
	})

	t.Run("D4_requestedAttentionItem_nil_feed_safe", func(t *testing.T) {
		if got := requestedAttentionItem(nil, nil, 5); got != nil {
			t.Fatalf("requestedAttentionItem(nil, ...) = %+v, want nil", got)
		}
	})

	t.Run("D6_localPerfTracker_bounds_pendingPromptTimes", func(t *testing.T) {
		tr := newLocalPerfTracker(10 * time.Second)
		now := time.Now()
		for i := 0; i < 300; i++ {
			tr.addPrompt(now.Add(time.Duration(i) * time.Millisecond))
		}
		if len(tr.pendingPromptTimes) > maxPendingPromptTimes {
			t.Fatalf("pendingPromptTimes grew unbounded: %d > %d", len(tr.pendingPromptTimes), maxPendingPromptTimes)
		}
	})

	t.Run("ops_restore_target_name_fullRefresh_and_workflow_enter_scope", func(t *testing.T) {
		name1 := dashboardRestoreTargetName("myproj", "20261121-123001.100-1111111111111111")
		name2 := dashboardRestoreTargetName("myproj", "20261121-123559.200-2222222222222222")
		if !strings.HasPrefix(name1, "myproj-restored-") || name1 == name2 {
			t.Fatalf("dashboardRestoreTargetName collided within same 10-min window: %q vs %q", name1, name2)
		}

		// Live unread mail IDs must be preserved when pruning seenMailAttentionIDs > 1024.
		mMail := testDashboardWithPanes(160, 48)
		mMail.agentMailInbox = map[string][]agentmail.InboxMessage{
			"%1": {{ID: 42, Subject: "live"}},
		}
		mMail.seenMailAttentionIDs[42] = struct{}{}
		for i := 1000; i < 2100; i++ {
			mMail.seenMailAttentionIDs[i] = struct{}{}
		}
		gen := mMail.nextGen(refreshSession)
		updatedMail, _ := mMail.Update(SessionDataWithOutputMsg{
			Gen:   gen,
			Panes: mMail.panes,
			Outputs: []PaneOutputData{{
				PaneID: "%1",
				Observation: status.PaneObservation{
					Current: status.StateObservation{
						Freshness: status.FreshnessFresh,
						Status:    status.AgentStatus{PaneID: "%1", State: status.StateWorking},
					},
				},
			}},
		})
		nextMail := updatedMail.(Model)
		if _, ok := nextMail.seenMailAttentionIDs[42]; !ok {
			t.Fatal("pruning wiped live unread mail ID 42 from seenMailAttentionIDs")
		}
		if _, stale := nextMail.seenMailAttentionIDs[1500]; stale {
			t.Fatal("pruning failed to evict stale seenMailAttentionIDs entry 1500")
		}
		if items := nextMail.paneList.Items(); len(items) > 0 {
			if pi, ok := items[0].(paneItem); ok && pi.row.Status != string(status.StateWorking) {
				t.Fatalf("paneList row status = %q, want %q in same SessionDataWithOutputMsg cycle", pi.row.Status, status.StateWorking)
			}
		}

		m := testDashboardWithPanes(160, 48)
		m.showPipelinesPanel = true
		m.showApprovalsPanel = true
		m.showWorktreesPanel = true
		m.showWorkflowPanel = true
		m.showQuotaPanel = true
		m.showRateLimitPanel = true
		m.showAccountsPanel = true
		_ = m.fullRefresh(false)
		if !m.fetchingPipelines || !m.fetchingApprovals || !m.fetchingWorktrees || !m.fetchingWorkflow ||
			!m.fetchingQuota || !m.fetchingOAuthHealth || !m.fetchingAccounts {
			t.Fatalf("fullRefresh omitted visible operational/C6 panels: pipelines=%v approvals=%v worktrees=%v workflow=%v quota=%v oauth=%v accounts=%v",
				m.fetchingPipelines, m.fetchingApprovals, m.fetchingWorktrees, m.fetchingWorkflow, m.fetchingQuota, m.fetchingOAuthHealth, m.fetchingAccounts)
		}

		// Workflow enter must NOT hijack enter when focus is on PaneList; stub zoom so no live tmux runs.
		oldZoom := dashboardZoomPane
		defer func() { dashboardZoomPane = oldZoom }()
		zoomCalled := false
		dashboardZoomPane = func(session string, paneIndex int) error {
			zoomCalled = true
			return nil
		}
		m2 := testDashboardWithPanes(160, 48)
		m2.showWorkflowPanel = true
		m2.workflowPanel.SetData(panels.WorkflowPanelData{State: &workflow.WorkflowState{WorkflowName: "wf", CurrentStage: "s1"}}, nil)
		m2.focusedPanel = PanelPaneList
		m2.sidebarActivePanel = ""
		updated, _ := m2.Update(tea.KeyMsg{Type: tea.KeyEnter})
		next := updated.(Model)
		if !zoomCalled {
			t.Fatal("expected Enter on PanelPaneList to reach dashboardZoomPane")
		}
		if strings.Contains(status.StripANSI(next.renderSidebar(90, 30)), "Stage details: s1") {
			t.Fatal("workflow panel hijacked Enter while PanelPaneList was focused")
		}
	})

	t.Run("WaveJ_split_view_and_readonly_panels_focus_routing", func(t *testing.T) {
		// Split tier (140x40, past TierSplit hysteresis margin): PanelPaneList stays focusable even when Pipelines is open.
		ms := testDashboardWithPanes(140, 40)
		ms.showPipelinesPanel = true
		ms.sidebarActivePanel = ms.pipelinesPanel.Config().ID
		ms.focusedPanel = PanelPaneList
		if got := ms.focusedOperationalPanelForKeys(); got != nil {
			t.Fatalf("TierSplit with PanelPaneList focused returned %v, want nil", got.Config().ID)
		}
		ms.focusedPanel = PanelDetail
		if got := ms.focusedOperationalPanelForKeys(); got != ms.pipelinesPanel {
			t.Fatalf("TierSplit with PanelDetail focused returned %v, want pipelinesPanel", got)
		}

		// Non-interactive panels (quota/ratelimit/accounts) must never swallow keys.
		mq := testDashboardWithPanes(140, 40)
		mq.showQuotaPanel = true
		mq.sidebarActivePanel = mq.quotaPanel.Config().ID
		mq.focusedPanel = PanelDetail
		if got := mq.focusedOperationalPanelForKeys(); got != nil {
			t.Fatalf("non-interactive quota panel captured keys: %v", got.Config().ID)
		}
	})
}
