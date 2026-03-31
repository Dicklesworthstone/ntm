package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/cass"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tracker"
	"github.com/Dicklesworthstone/ntm/internal/tui/dashboard/panels"
	"github.com/Dicklesworthstone/ntm/internal/watcher"
)

func TestFetchBeadsCmd_NoBv(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)
	cmd := m.fetchBeadsCmd()

	msg := cmd()
	beadsMsg, ok := msg.(BeadsUpdateMsg)
	if !ok {
		t.Fatalf("expected BeadsUpdateMsg, got %T", msg)
	}

	// When bv is not installed or not available, we expect an error
	// This is the expected behavior for environments without bv
	if beadsMsg.Err == nil {
		// bv might be installed in test environment - verify Summary is valid
		if !beadsMsg.Summary.Available && beadsMsg.Summary.Reason == "" {
			t.Error("expected either error or available summary")
		}
	}
}

func TestFetchAlertsCmd(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)
	cmd := m.fetchAlertsCmd()

	msg := cmd()
	alertsMsg, ok := msg.(AlertsUpdateMsg)
	if !ok {
		t.Fatalf("expected AlertsUpdateMsg, got %T", msg)
	}

	// Should return without panic; Alerts may be nil or empty in test env
	// We just verify the command completes and returns correct message type
	_ = alertsMsg.Alerts
}

func TestFetchAlertsCmd_WithConfig(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)
	// Set a minimal config
	m.cfg = &config.Config{
		Alerts: config.AlertsConfig{
			Enabled:              true,
			AgentStuckMinutes:    15,
			DiskLowThresholdGB:   5.0,
			MailBacklogThreshold: 50,
			BeadStaleHours:       48,
			ResolvedPruneMinutes: 60,
		},
		ProjectsBase: "/tmp",
	}

	cmd := m.fetchAlertsCmd()
	msg := cmd()
	alertsMsg, ok := msg.(AlertsUpdateMsg)
	if !ok {
		t.Fatalf("expected AlertsUpdateMsg, got %T", msg)
	}

	// Should return without panic; Alerts may be nil or empty in test env
	// Config should influence alert checking, but we just verify completion
	_ = alertsMsg.Alerts
}

func TestFetchAttentionCmd_FeedUnavailable(t *testing.T) {
	lockAttentionFeedForTest(t)

	oldFeed := robot.PeekAttentionFeed()
	robot.SetAttentionFeed(nil)
	t.Cleanup(func() {
		robot.SetAttentionFeed(oldFeed)
	})

	m := newTestModel(200)
	cmd := m.fetchAttentionCmd()
	msg := cmd()

	attentionMsg, ok := msg.(AttentionUpdateMsg)
	if !ok {
		t.Fatalf("expected AttentionUpdateMsg, got %T", msg)
	}
	if attentionMsg.FeedAvailable {
		t.Fatal("expected feedAvailable=false when no feed is initialized")
	}
	if len(attentionMsg.Items) != 0 {
		t.Fatalf("expected no items when feed is unavailable, got %d", len(attentionMsg.Items))
	}
}

func TestFetchAttentionCmd_SortsAndLimitsItems(t *testing.T) {
	lockAttentionFeedForTest(t)

	oldFeed := robot.PeekAttentionFeed()
	feed := robot.NewAttentionFeed(robot.AttentionFeedConfig{
		JournalSize:       64,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	robot.SetAttentionFeed(feed)
	t.Cleanup(func() {
		feed.Stop()
		robot.SetAttentionFeed(oldFeed)
	})

	base := time.Date(2026, 3, 21, 12, 0, 0, 123456789, time.UTC)
	for i := 0; i < 11; i++ {
		feed.Append(robot.AttentionEvent{
			Summary:       "interesting-" + strconv.Itoa(i),
			Ts:            base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339Nano),
			Actionability: robot.ActionabilityInteresting,
			Pane:          1,
		})
	}
	feed.Append(robot.AttentionEvent{
		Summary:       "action-now",
		Ts:            base.Add(30 * time.Minute).Format(time.RFC3339Nano),
		Actionability: robot.ActionabilityActionRequired,
		Pane:          2,
	})

	m := newTestModel(200)
	m.panes = []tmux.Pane{
		{ID: "pane-1", Index: 1, Type: tmux.AgentClaude},
		{ID: "pane-2", Index: 2, Type: tmux.AgentCodex},
	}

	cmd := m.fetchAttentionCmd()
	msg := cmd()

	attentionMsg, ok := msg.(AttentionUpdateMsg)
	if !ok {
		t.Fatalf("expected AttentionUpdateMsg, got %T", msg)
	}
	if !attentionMsg.FeedAvailable {
		t.Fatal("expected feedAvailable=true with initialized feed")
	}
	if got := len(attentionMsg.Items); got != attentionPanelMaxItems {
		t.Fatalf("expected %d items after trimming, got %d", attentionPanelMaxItems, got)
	}
	if attentionMsg.Items[0].Summary != "action-now" {
		t.Fatalf("expected action-required item first, got %q", attentionMsg.Items[0].Summary)
	}
	if attentionMsg.Items[0].SourceAgent != "Codex" {
		t.Fatalf("expected pane-derived source agent, got %q", attentionMsg.Items[0].SourceAgent)
	}
	if attentionMsg.Items[1].Summary != "interesting-10" {
		t.Fatalf("expected newest interesting item next, got %q", attentionMsg.Items[1].Summary)
	}
	if attentionMsg.Items[len(attentionMsg.Items)-1].Summary != "interesting-2" {
		t.Fatalf("expected oldest retained interesting item last, got %q", attentionMsg.Items[len(attentionMsg.Items)-1].Summary)
	}
}

func TestFetchMetricsCmd_NoPanes(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)
	m.panes = nil // No panes

	cmd := m.fetchMetricsCmd()
	msg := cmd()
	metricsMsg, ok := msg.(MetricsUpdateMsg)
	if !ok {
		t.Fatalf("expected MetricsUpdateMsg, got %T", msg)
	}

	// With no panes, metrics should be empty
	if metricsMsg.Data.Coverage != nil || metricsMsg.Data.Redundancy != nil || metricsMsg.Data.Velocity != nil || metricsMsg.Data.Conflicts != nil {
		t.Error("expected empty metrics data with no panes")
	}
}

func TestFetchMetricsCmd_SkipsUserPanes(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)
	m.panes = []tmux.Pane{
		{ID: "user-pane", Type: tmux.AgentUser, Title: "user"},
	}

	cmd := m.fetchMetricsCmd()
	msg := cmd()
	metricsMsg, ok := msg.(MetricsUpdateMsg)
	if !ok {
		t.Fatalf("expected MetricsUpdateMsg, got %T", msg)
	}

	// User panes should be skipped (no metrics computed here)
	if metricsMsg.Data.Coverage != nil || metricsMsg.Data.Redundancy != nil || metricsMsg.Data.Velocity != nil || metricsMsg.Data.Conflicts != nil {
		t.Error("expected no metrics with user panes only")
	}
}

func TestFetchHistoryCmd(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)
	cmd := m.fetchHistoryCmd()

	msg := cmd()
	historyMsg, ok := msg.(HistoryUpdateMsg)
	if !ok {
		t.Fatalf("expected HistoryUpdateMsg, got %T", msg)
	}

	// Should return entries or an error (if history file doesn't exist)
	// We don't expect a panic either way
	_ = historyMsg
}

func TestFetchFileChangesCmd(t *testing.T) {
	origStore := tracker.GlobalFileChanges
	store := tracker.NewFileChangeStore(100)
	tracker.GlobalFileChanges = store
	t.Cleanup(func() { tracker.GlobalFileChanges = origStore })

	now := time.Now()
	store.Add(tracker.RecordedFileChange{
		Timestamp: now.Add(-30 * time.Minute),
		Session:   "s",
		Change:    tracker.FileChange{Path: "/older.go", Type: tracker.FileModified},
	})
	store.Add(tracker.RecordedFileChange{
		Timestamp: now.Add(-2 * time.Minute),
		Session:   "s",
		Change:    tracker.FileChange{Path: "/recent.go", Type: tracker.FileModified},
	})

	m := newTestModel(120)
	cmd := m.fetchFileChangesCmd()

	msg := cmd()
	fileChangeMsg, ok := msg.(FileChangeMsg)
	if !ok {
		t.Fatalf("expected FileChangeMsg, got %T", msg)
	}

	if len(fileChangeMsg.Changes) != 2 {
		t.Fatalf("expected full bounded file-change buffer, got %d entries", len(fileChangeMsg.Changes))
	}
	if fileChangeMsg.Changes[0].Change.Path != "/older.go" {
		t.Fatalf("expected older buffered change to be returned for wider panel windows, got %q first", fileChangeMsg.Changes[0].Change.Path)
	}
	if fileChangeMsg.Changes[1].Change.Path != "/recent.go" {
		t.Fatalf("expected recent change second, got %q", fileChangeMsg.Changes[1].Change.Path)
	}
}

func TestFetchCASSContextCmd_NoCass(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)
	m.session = "test-session"
	cmd := m.fetchCASSContextCmd()

	msg := cmd()
	cassMsg, ok := msg.(CASSContextMsg)
	if !ok {
		t.Fatalf("expected CASSContextMsg, got %T", msg)
	}

	// If CASS is not installed, we expect an error
	// If CASS is installed, we may get hits or empty hits
	_ = cassMsg
}

func TestFetchAttentionCmdParsesRFC3339NanoAndSorts(t *testing.T) {
	lockAttentionFeedForTest(t)

	oldFeed := robot.GetAttentionFeed()
	t.Cleanup(func() {
		robot.SetAttentionFeed(oldFeed)
	})

	feed := robot.NewAttentionFeed(robot.AttentionFeedConfig{
		JournalSize:       16,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	feed.Append(robot.AttentionEvent{
		Ts:            "2026-03-21T07:00:00.123456Z",
		Summary:       "interesting but older",
		Actionability: robot.ActionabilityInteresting,
		Pane:          3,
		Details:       map[string]any{"agent_type": "codex"},
	})
	feed.Append(robot.AttentionEvent{
		Ts:            "2026-03-21T07:01:00.654321Z",
		Summary:       "action required newer",
		Actionability: robot.ActionabilityActionRequired,
		Pane:          1,
		Details:       map[string]any{"agent_type": "claude"},
	})
	feed.Append(robot.AttentionEvent{
		Ts:            "2026-03-21T07:02:00Z",
		Summary:       "background noise",
		Actionability: robot.ActionabilityBackground,
		Pane:          9,
	})
	robot.SetAttentionFeed(feed)

	m := newTestModel(120)
	m.panes = []tmux.Pane{
		{ID: "pane-1", Index: 1, Type: tmux.AgentClaude},
		{ID: "pane-3", Index: 3, Type: tmux.AgentCodex},
	}
	msg := m.fetchAttentionCmd()()
	update, ok := msg.(AttentionUpdateMsg)
	if !ok {
		t.Fatalf("expected AttentionUpdateMsg, got %T", msg)
	}
	if !update.FeedAvailable {
		t.Fatal("expected attention feed to be available")
	}
	if len(update.Items) != 2 {
		t.Fatalf("expected 2 actionable items, got %d", len(update.Items))
	}
	if update.Items[0].Summary != "action required newer" {
		t.Fatalf("first item = %q, want action-required item first", update.Items[0].Summary)
	}
	if update.Items[0].Timestamp.IsZero() || update.Items[1].Timestamp.IsZero() {
		t.Fatal("expected timestamps to parse successfully")
	}
	if update.Items[0].SourceAgent != "Claude" || update.Items[1].SourceAgent != "Codex" {
		t.Fatalf("unexpected source agents: %+v", update.Items)
	}
}

func TestFetchAttentionCmdReturnsTopTenNewestActionableItems(t *testing.T) {
	lockAttentionFeedForTest(t)

	oldFeed := robot.GetAttentionFeed()
	t.Cleanup(func() {
		robot.SetAttentionFeed(oldFeed)
	})

	feed := robot.NewAttentionFeed(robot.AttentionFeedConfig{
		JournalSize:       32,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	for i := 0; i < 12; i++ {
		feed.Append(robot.AttentionEvent{
			Ts:            time.Date(2026, 3, 21, 7, i, 0, 0, time.UTC).Format(time.RFC3339Nano),
			Summary:       fmt.Sprintf("item-%02d", i),
			Actionability: robot.ActionabilityInteresting,
			Pane:          i + 1,
		})
	}
	robot.SetAttentionFeed(feed)

	m := newTestModel(120)
	msg := m.fetchAttentionCmd()()
	update, ok := msg.(AttentionUpdateMsg)
	if !ok {
		t.Fatalf("expected AttentionUpdateMsg, got %T", msg)
	}
	if len(update.Items) != attentionPanelMaxItems {
		t.Fatalf("expected %d attention items, got %d", attentionPanelMaxItems, len(update.Items))
	}
	if update.Items[0].Summary != "item-11" {
		t.Fatalf("first item = %q, want newest actionable item", update.Items[0].Summary)
	}
	if update.Items[len(update.Items)-1].Summary != "item-02" {
		t.Fatalf("last item = %q, want oldest retained actionable item", update.Items[len(update.Items)-1].Summary)
	}
}

func TestFetchAttentionCmdRetainsRequestedCursorOutsideTopWindow(t *testing.T) {
	lockAttentionFeedForTest(t)

	oldFeed := robot.GetAttentionFeed()
	t.Cleanup(func() {
		robot.SetAttentionFeed(oldFeed)
	})

	feed := robot.NewAttentionFeed(robot.AttentionFeedConfig{
		JournalSize:       32,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	var targetCursor int64
	for i := 0; i < 12; i++ {
		event := feed.Append(robot.AttentionEvent{
			Ts:            time.Date(2026, 3, 21, 7, i, 0, 0, time.UTC).Format(time.RFC3339Nano),
			Summary:       fmt.Sprintf("item-%02d", i),
			Actionability: robot.ActionabilityInteresting,
			Pane:          i + 1,
		})
		if i == 0 {
			targetCursor = event.Cursor
		}
	}
	robot.SetAttentionFeed(feed)

	m := newTestModel(120)
	m.requestedAttentionCursor = targetCursor
	msg := m.fetchAttentionCmd()()
	update, ok := msg.(AttentionUpdateMsg)
	if !ok {
		t.Fatalf("expected AttentionUpdateMsg, got %T", msg)
	}
	if len(update.Items) != attentionPanelMaxItems {
		t.Fatalf("expected %d attention items, got %d", attentionPanelMaxItems, len(update.Items))
	}
	found := false
	for _, item := range update.Items {
		if item.Cursor == targetCursor {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected requested cursor %d to be retained in the trimmed attention window", targetCursor)
	}
}

func TestFetchAttentionCmdRetainsRequestedCursorOutsideReplayWindow(t *testing.T) {
	lockAttentionFeedForTest(t)

	oldFeed := robot.GetAttentionFeed()
	t.Cleanup(func() {
		robot.SetAttentionFeed(oldFeed)
	})

	feed := robot.NewAttentionFeed(robot.AttentionFeedConfig{
		JournalSize:       128,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	target := feed.Append(robot.AttentionEvent{
		Ts:            time.Date(2026, 3, 21, 6, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Summary:       "wake-human",
		Actionability: robot.ActionabilityActionRequired,
		Pane:          1,
	})
	for i := 0; i < attentionPanelReplayHint+5; i++ {
		feed.Append(robot.AttentionEvent{
			Ts:            time.Date(2026, 3, 21, 7, i, 0, 0, time.UTC).Format(time.RFC3339Nano),
			Summary:       fmt.Sprintf("background-%02d", i),
			Actionability: robot.ActionabilityBackground,
			Pane:          2,
		})
	}
	robot.SetAttentionFeed(feed)

	m := newTestModel(120)
	m.requestedAttentionCursor = target.Cursor
	msg := m.fetchAttentionCmd()()
	update, ok := msg.(AttentionUpdateMsg)
	if !ok {
		t.Fatalf("expected AttentionUpdateMsg, got %T", msg)
	}
	if len(update.Items) != 1 {
		t.Fatalf("expected only the requested actionable item to survive, got %d items", len(update.Items))
	}
	if got := update.Items[0].Cursor; got != target.Cursor {
		t.Fatalf("cursor = %d, want %d", got, target.Cursor)
	}
}

func TestHandleConflictAction_ForceRegistersDashboardAgent(t *testing.T) {
	var (
		mu    sync.Mutex
		calls []string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		params, _ := req["params"].(map[string]any)
		toolName, _ := params["name"].(string)
		args, _ := params["arguments"].(map[string]any)

		mu.Lock()
		calls = append(calls, toolName)
		mu.Unlock()

		var result any
		switch toolName {
		case "register_agent":
			result = agentmail.Agent{
				ID:      1,
				Name:    "sess_dashboard",
				Program: "ntm-dashboard",
				Model:   "local",
			}
		case "force_release_file_reservation":
			if args["agent_name"] != "sess_dashboard" {
				t.Fatalf("force_release agent_name = %#v, want sess_dashboard", args["agent_name"])
			}
			result = agentmail.ForceReleaseResult{
				Success:        true,
				PreviousHolder: "holder-a",
				PathPattern:    "internal/robot/attention_feed.go",
				Notified:       true,
			}
		default:
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req["id"],
			"result":  result,
		})
	}))
	defer server.Close()

	t.Setenv("AGENT_MAIL_URL", server.URL+"/")

	model := &Model{
		session:    "sess",
		projectDir: "/tmp/ntm",
	}

	err := model.handleConflictAction(watcher.FileConflict{
		Path:                 "internal/robot/attention_feed.go",
		RequestorAgent:       "worker-a",
		HolderReservationIDs: []int{42},
	}, watcher.ConflictActionForce)
	if err != nil {
		t.Fatalf("handleConflictAction(force) error: %v", err)
	}

	mu.Lock()
	gotCalls := append([]string(nil), calls...)
	mu.Unlock()

	wantCalls := []string{"register_agent", "force_release_file_reservation"}
	if !slices.Equal(gotCalls, wantCalls) {
		t.Fatalf("tool call order = %v, want %v", gotCalls, wantCalls)
	}
}

// Test that commands return tea.Cmd (not nil)
func TestCommandsReturnTeaCmd(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)

	tests := []struct {
		name string
		cmd  func() tea.Cmd
	}{
		{"fetchBeadsCmd", func() tea.Cmd { return m.fetchBeadsCmd() }},
		{"fetchAlertsCmd", func() tea.Cmd { return m.fetchAlertsCmd() }},
		{"fetchMetricsCmd", func() tea.Cmd { return m.fetchMetricsCmd() }},
		{"fetchHistoryCmd", func() tea.Cmd { return m.fetchHistoryCmd() }},
		{"fetchFileChangesCmd", func() tea.Cmd { return m.fetchFileChangesCmd() }},
		{"fetchCASSContextCmd", func() tea.Cmd { return m.fetchCASSContextCmd() }},
		{"fetchTimelineCmd", func() tea.Cmd { return m.fetchTimelineCmd() }},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := tc.cmd()
			if cmd == nil {
				t.Errorf("%s returned nil tea.Cmd", tc.name)
			}
		})
	}
}

// Test message types are correct
func TestMessageTypes(t *testing.T) {
	t.Parallel()

	// Verify message types can be created and have expected fields
	t.Run("BeadsUpdateMsg", func(t *testing.T) {
		msg := BeadsUpdateMsg{
			Ready: []bv.BeadPreview{{ID: "task-1", Title: "Task 1"}, {ID: "task-2", Title: "Task 2"}},
		}
		if len(msg.Ready) != 2 {
			t.Errorf("expected 2 ready items, got %d", len(msg.Ready))
		}
	})

	t.Run("AlertsUpdateMsg", func(t *testing.T) {
		msg := AlertsUpdateMsg{
			Alerts: []alerts.Alert{
				{Type: alerts.AlertAgentStuck, Message: "test"},
			},
		}
		if len(msg.Alerts) != 1 {
			t.Errorf("expected 1 alert, got %d", len(msg.Alerts))
		}
	})

	t.Run("MetricsUpdateMsg", func(t *testing.T) {
		msg := MetricsUpdateMsg{
			Data: panels.MetricsData{
				Coverage: &ensemble.CoverageReport{Overall: 0.5},
			},
		}
		if msg.Data.Coverage == nil {
			t.Error("expected coverage report to be set")
		}
	})

	t.Run("HistoryUpdateMsg", func(t *testing.T) {
		msg := HistoryUpdateMsg{
			Entries: []history.HistoryEntry{
				{ID: "abc123", Prompt: "test prompt"},
			},
		}
		if len(msg.Entries) != 1 {
			t.Errorf("expected 1 entry, got %d", len(msg.Entries))
		}
	})

	t.Run("FileChangeMsg", func(t *testing.T) {
		msg := FileChangeMsg{
			Changes: []tracker.RecordedFileChange{
				{
					Timestamp: time.Now(),
					Session:   "test",
					Change:    tracker.FileChange{Path: "/tmp/file.go"},
				},
			},
		}
		if len(msg.Changes) != 1 {
			t.Errorf("expected 1 change, got %d", len(msg.Changes))
		}
	})

	t.Run("CASSContextMsg", func(t *testing.T) {
		msg := CASSContextMsg{
			Hits: []cass.SearchHit{
				{Title: "test hit", Score: 0.9},
			},
		}
		if len(msg.Hits) != 1 {
			t.Errorf("expected 1 hit, got %d", len(msg.Hits))
		}
	})
}

// Test error handling in messages
func TestMessageErrorHandling(t *testing.T) {
	t.Parallel()

	t.Run("BeadsUpdateMsg_WithError", func(t *testing.T) {
		msg := BeadsUpdateMsg{
			Err: errors.New("test error"),
		}
		// Error type check
		if msg.Err == nil {
			t.Error("expected error to be set")
		}
	})

	t.Run("HistoryUpdateMsg_WithError", func(t *testing.T) {
		msg := HistoryUpdateMsg{
			Err: errors.New("test error"),
		}
		if msg.Err == nil {
			t.Error("expected error to be set")
		}
	})

	t.Run("CASSContextMsg_WithError", func(t *testing.T) {
		msg := CASSContextMsg{
			Err: errors.New("test error"),
		}
		if msg.Err == nil {
			t.Error("expected error to be set")
		}
	})
}
