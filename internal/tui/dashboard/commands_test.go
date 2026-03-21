package dashboard

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
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
	t.Parallel()

	m := newTestModel(120)
	cmd := m.fetchFileChangesCmd()

	msg := cmd()
	fileChangeMsg, ok := msg.(FileChangeMsg)
	if !ok {
		t.Fatalf("expected FileChangeMsg, got %T", msg)
	}

	// Should return a slice (possibly empty) of changes
	// We don't expect a panic
	_ = fileChangeMsg
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
