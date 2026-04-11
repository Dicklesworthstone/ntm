package coordinator

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestNewSessionCoordinator(t *testing.T) {
	c := New("test-session", "/tmp/test", nil, "TestAgent")

	if c.session != "test-session" {
		t.Errorf("expected session 'test-session', got %q", c.session)
	}
	if c.projectKey != "/tmp/test" {
		t.Errorf("expected projectKey '/tmp/test', got %q", c.projectKey)
	}
	if c.agentName != "TestAgent" {
		t.Errorf("expected agentName 'TestAgent', got %q", c.agentName)
	}
	if c.agents == nil {
		t.Error("expected agents map to be initialized")
	}
}

func TestDefaultCoordinatorConfig(t *testing.T) {
	cfg := DefaultCoordinatorConfig()

	if cfg.PollInterval != 5*time.Second {
		t.Errorf("expected PollInterval 5s, got %v", cfg.PollInterval)
	}
	if cfg.DigestInterval != 5*time.Minute {
		t.Errorf("expected DigestInterval 5m, got %v", cfg.DigestInterval)
	}
	if cfg.AutoAssign {
		t.Error("expected AutoAssign to be false by default")
	}
	if cfg.IdleThreshold != 30.0 {
		t.Errorf("expected IdleThreshold 30.0, got %f", cfg.IdleThreshold)
	}
	if !cfg.ConflictNotify {
		t.Error("expected ConflictNotify to be true by default")
	}
	if cfg.ConflictNegotiate {
		t.Error("expected ConflictNegotiate to be false by default")
	}
}

func TestWithConfig(t *testing.T) {
	c := New("test-session", "/tmp/test", nil, "TestAgent")
	cfg := CoordinatorConfig{
		PollInterval: 10 * time.Second,
		AutoAssign:   true,
	}

	result := c.WithConfig(cfg)

	if result != c {
		t.Error("expected WithConfig to return self for chaining")
	}
	if c.config.PollInterval != 10*time.Second {
		t.Errorf("expected PollInterval 10s, got %v", c.config.PollInterval)
	}
	if !c.config.AutoAssign {
		t.Error("expected AutoAssign to be true")
	}
}

func TestGetAgents(t *testing.T) {
	c := New("test-session", "/tmp/test", nil, "TestAgent")

	// Add some agents directly for testing
	c.mu.Lock()
	c.agents["%0"] = &AgentState{
		PaneID:    "%0",
		PaneIndex: 0,
		AgentType: "cc",
		Status:    robot.StateWaiting,
		Healthy:   true,
	}
	c.agents["%1"] = &AgentState{
		PaneID:    "%1",
		PaneIndex: 1,
		AgentType: "cod",
		Status:    robot.StateGenerating,
		Healthy:   true,
	}
	c.mu.Unlock()

	agents := c.GetAgents()

	if len(agents) != 2 {
		t.Errorf("expected 2 agents, got %d", len(agents))
	}
	if agents["%0"].AgentType != "cc" {
		t.Errorf("expected agent %%0 type 'cc', got %q", agents["%0"].AgentType)
	}
	if agents["%1"].AgentType != "cod" {
		t.Errorf("expected agent %%1 type 'cod', got %q", agents["%1"].AgentType)
	}
}

func TestGetAgentByPaneID(t *testing.T) {
	c := New("test-session", "/tmp/test", nil, "TestAgent")

	c.mu.Lock()
	c.agents["%0"] = &AgentState{
		PaneID:    "%0",
		AgentType: "cc",
	}
	c.mu.Unlock()

	agent := c.GetAgentByPaneID("%0")
	if agent == nil {
		t.Fatal("expected to find agent %0")
	}
	if agent.AgentType != "cc" {
		t.Errorf("expected AgentType 'cc', got %q", agent.AgentType)
	}

	missing := c.GetAgentByPaneID("%99")
	if missing != nil {
		t.Error("expected nil for non-existent agent")
	}
}

func TestGetIdleAgents(t *testing.T) {
	c := New("test-session", "/tmp/test", nil, "TestAgent")
	c.config.IdleThreshold = 0 // Immediate idle for testing

	c.mu.Lock()
	c.agents["%0"] = &AgentState{
		PaneID:       "%0",
		Status:       robot.StateWaiting,
		Healthy:      true,
		LastActivity: time.Now().Add(-1 * time.Minute),
	}
	c.agents["%1"] = &AgentState{
		PaneID:       "%1",
		Status:       robot.StateGenerating, // Not idle
		Healthy:      true,
		LastActivity: time.Now(),
	}
	c.agents["%2"] = &AgentState{
		PaneID:       "%2",
		Status:       robot.StateWaiting,
		Healthy:      false, // Not healthy
		LastActivity: time.Now().Add(-1 * time.Minute),
	}
	c.mu.Unlock()

	idle := c.GetIdleAgents()

	if len(idle) != 1 {
		t.Errorf("expected 1 idle agent, got %d", len(idle))
	}
	if len(idle) > 0 && idle[0].PaneID != "%0" {
		t.Errorf("expected idle agent to be %%0, got %s", idle[0].PaneID)
	}
}

func TestUpdateAgentStates_RetainsExistingAgentWhenCaptureFails(t *testing.T) {
	origGetPanesWithActivity := getPanesWithActivity
	origCaptureForHealthCheckWithCtx := captureForHealthCheckWithCtx
	t.Cleanup(func() {
		getPanesWithActivity = origGetPanesWithActivity
		captureForHealthCheckWithCtx = origCaptureForHealthCheckWithCtx
	})

	lastActivity := time.Now().Add(-1 * time.Minute).UTC()
	getPanesWithActivity = func(session string) ([]tmux.PaneActivity, error) {
		return []tmux.PaneActivity{
			{
				Pane: tmux.Pane{
					ID:    "%0",
					Index: 0,
					Title: "test-session__cc_1",
					Type:  tmux.AgentClaude,
				},
				LastActivity: lastActivity,
			},
		}, nil
	}
	captureForHealthCheckWithCtx = func(ctx context.Context, paneID string) (string, error) {
		return "", errors.New("capture failed")
	}

	c := New("test-session", "/tmp/test", nil, "TestAgent")
	c.monitor = NewAgentMonitor(c.session, nil, c.projectKey)
	c.mu.Lock()
	c.agents["%0"] = &AgentState{
		PaneID:       "%0",
		PaneIndex:    0,
		AgentType:    string(tmux.AgentClaude),
		Status:       robot.StateWaiting,
		Healthy:      true,
		LastActivity: lastActivity,
	}
	c.mu.Unlock()

	c.updateAgentStates()

	agent := c.GetAgentByPaneID("%0")
	if agent == nil {
		t.Fatal("expected existing agent to remain tracked when capture fails")
	}
	if agent.Status != robot.StateWaiting {
		t.Fatalf("status = %v, want %v", agent.Status, robot.StateWaiting)
	}
	if !agent.LastActivity.Equal(lastActivity) {
		t.Fatalf("last activity changed unexpectedly: got %v want %v", agent.LastActivity, lastActivity)
	}
}

func TestDetectAgentType(t *testing.T) {
	tests := []struct {
		title    string
		expected string
	}{
		{"myproject__cc_1", "cc"},
		{"myproject__claude_1", "cc"},
		{"myproject__claude_code_1", "cc"},
		{"myproject__cod_1", "cod"},
		{"myproject__codex_1", "cod"},
		{"myproject__openai-codex_1", "cod"},
		{"myproject__gmi_1", "gmi"},
		{"myproject__gemini_1", "gmi"},
		{"myproject__google_gemini_1", "gmi"},
		{"myproject__cursor_1", "cursor"},
		{"myproject__ws_1", "windsurf"},
		{"myproject__aider_1", "aider"},
		{"myproject__ollama_1", "ollama"},
		{"myproject__user_1", ""},
		{"bash", ""},
		{"", ""},
	}

	for _, tt := range tests {
		result := detectAgentType(tt.title)
		if result != tt.expected {
			t.Errorf("detectAgentType(%q) = %q, expected %q", tt.title, result, tt.expected)
		}
	}
}

func TestEventsChannel(t *testing.T) {
	c := New("test-session", "/tmp/test", nil, "TestAgent")

	eventsChan := c.Events()
	if eventsChan == nil {
		t.Fatal("expected events channel")
	}

	// Test we can send to the channel
	go func() {
		c.events <- CoordinatorEvent{
			Type:      EventAgentIdle,
			Timestamp: time.Now(),
			AgentID:   "%0",
		}
	}()

	select {
	case event := <-eventsChan:
		if event.Type != EventAgentIdle {
			t.Errorf("expected EventAgentIdle, got %v", event.Type)
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for event")
	}
}

func TestEmitEvent_PublishesToEventsBus(t *testing.T) {
	c := New("test-session", "/tmp/test", nil, "TestAgent")

	var (
		mu         sync.Mutex
		eventsSeen []events.BusEvent
	)
	unsub := events.Subscribe("agent.idle", func(e events.BusEvent) {
		mu.Lock()
		eventsSeen = append(eventsSeen, e)
		mu.Unlock()
	})
	defer unsub()

	agent := &AgentState{
		PaneID:    "%0",
		PaneIndex: 2,
		AgentType: "cc",
		Status:    robot.StateWaiting,
	}
	c.emitEvent(agent, robot.StateGenerating)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(eventsSeen)
		var last events.BusEvent
		if n > 0 {
			last = eventsSeen[n-1]
		}
		mu.Unlock()

		if last != nil {
			if last.EventType() != "agent.idle" {
				t.Fatalf("EventType=%q, want %q", last.EventType(), "agent.idle")
			}
			if last.EventSession() != "test-session" {
				t.Fatalf("EventSession=%q, want %q", last.EventSession(), "test-session")
			}

			typed, ok := last.(busCoordinatorEvent)
			if !ok {
				t.Fatalf("event type=%T, want %T", last, busCoordinatorEvent{})
			}
			if typed.AgentID != "%0" {
				t.Fatalf("AgentID=%q, want %q", typed.AgentID, "%0")
			}
			if typed.PrevType != string(robot.StateGenerating) {
				t.Fatalf("PrevType=%q, want %q", typed.PrevType, string(robot.StateGenerating))
			}
			if typed.NewType != string(robot.StateWaiting) {
				t.Fatalf("NewType=%q, want %q", typed.NewType, string(robot.StateWaiting))
			}
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("timeout waiting for agent.idle bus event")
}

func TestStartStop(t *testing.T) {
	c := New("test-session", "/tmp/test", nil, "TestAgent")
	c.config.PollInterval = 100 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := c.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Give the monitor loop a moment to start
	time.Sleep(50 * time.Millisecond)

	c.Stop()

	// Verify stop completed without hanging
	time.Sleep(50 * time.Millisecond)
}

func TestStart_DoubleStartRejected(t *testing.T) {
	c := New("test-session", "/tmp/test", nil, "TestAgent")
	c.config.PollInterval = 100 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := c.Start(ctx); err != nil {
		t.Fatalf("first Start failed: %v", err)
	}
	defer c.Stop()

	if err := c.Start(ctx); err == nil {
		t.Fatal("expected second Start to fail while coordinator is already running")
	}
}

func TestStop_WaitsForInFlightMonitorCycle(t *testing.T) {
	c := New("test-session", "/tmp/test", nil, "TestAgent")
	c.config.PollInterval = MinPollInterval
	c.config.SendDigests = false

	originalGetPanes := getPanesWithActivity
	defer func() {
		getPanesWithActivity = originalGetPanes
	}()

	var calls int
	blocked := make(chan struct{})
	release := make(chan struct{})

	getPanesWithActivity = func(session string) ([]tmux.PaneActivity, error) {
		calls++
		if calls == 1 {
			return nil, nil
		}
		close(blocked)
		<-release
		return nil, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("monitor loop did not enter blocked update cycle")
	}

	stopped := make(chan struct{})
	go func() {
		c.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
		t.Fatal("Stop returned before blocked monitor cycle was released")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not wait for blocked monitor cycle to finish")
	}
}

func TestGenerateDigest(t *testing.T) {
	c := New("test-session", "/tmp/test", nil, "TestAgent")

	c.mu.Lock()
	c.agents["%0"] = &AgentState{
		PaneID:       "%0",
		PaneIndex:    0,
		AgentType:    "cc",
		Status:       robot.StateWaiting,
		ContextUsage: 50,
		Healthy:      true,
	}
	c.agents["%1"] = &AgentState{
		PaneID:       "%1",
		PaneIndex:    1,
		AgentType:    "cod",
		Status:       robot.StateError,
		ContextUsage: 90,
		Healthy:      false,
	}
	c.mu.Unlock()

	digest := c.GenerateDigest()

	if digest.Session != "test-session" {
		t.Errorf("expected session 'test-session', got %q", digest.Session)
	}
	if digest.AgentCount != 2 {
		t.Errorf("expected AgentCount 2, got %d", digest.AgentCount)
	}
	if digest.IdleCount != 1 {
		t.Errorf("expected IdleCount 1, got %d", digest.IdleCount)
	}
	if digest.ErrorCount != 1 {
		t.Errorf("expected ErrorCount 1, got %d", digest.ErrorCount)
	}
	if len(digest.Alerts) == 0 {
		t.Error("expected alerts for error state and high context")
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d        time.Duration
		expected string
	}{
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m"},
		{3600 * time.Second, "1h0m"},
		{3660 * time.Second, "1h1m"},
	}

	for _, tt := range tests {
		result := formatDuration(tt.d)
		if result != tt.expected {
			t.Errorf("formatDuration(%v) = %q, expected %q", tt.d, result, tt.expected)
		}
	}
}

// =============================================================================
// mapStatusToRobotState tests
// =============================================================================

func TestMapStatusToRobotState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input status.AgentState
		want  robot.AgentState
	}{
		{"idle maps to waiting", status.StateIdle, robot.StateWaiting},
		{"working maps to generating", status.StateWorking, robot.StateGenerating},
		{"error maps to error", status.StateError, robot.StateError},
		{"unknown maps to unknown", status.StateUnknown, robot.StateUnknown},
		{"arbitrary string maps to unknown", status.AgentState("something_else"), robot.StateUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := mapStatusToRobotState(tt.input)
			if got != tt.want {
				t.Errorf("mapStatusToRobotState(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// =============================================================================
// formatDigestMarkdown tests
// =============================================================================

func TestFormatDigestMarkdown(t *testing.T) {
	t.Parallel()

	c := New("my-session", "/tmp/test", nil, "OrangeFox")

	now := time.Now()
	digest := DigestSummary{
		Session:     "my-session",
		GeneratedAt: now,
		AgentCount:  3,
		ActiveCount: 2,
		IdleCount:   1,
		ErrorCount:  0,
		AgentStatuses: []AgentDigestStatus{
			{PaneIndex: 0, AgentType: "cc", Status: "generating", ContextUsage: 45.0},
			{PaneIndex: 1, AgentType: "cod", Status: "waiting", ContextUsage: 30.0, IdleFor: "5m"},
			{PaneIndex: 2, AgentType: "gmi", Status: "generating", ContextUsage: 60.0},
		},
	}

	body := c.formatDigestMarkdown(digest)

	checks := []string{
		"# Session Digest: my-session",
		"**Total Agents:** 3",
		"**Active:** 2",
		"**Idle:** 1",
		"## Agent Status",
		"| Pane | Type | Status | Context | Idle For |",
		"OrangeFox",
		"cc",
		"cod",
		"gmi",
		"5m",
	}
	for _, want := range checks {
		if !strings.Contains(body, want) {
			t.Errorf("expected body to contain %q", want)
		}
	}

	// No errors, so should NOT contain the error emoji line
	if strings.Contains(body, "⚠️") {
		t.Error("expected no error warning emoji when ErrorCount is 0")
	}
}

func TestFormatDigestMarkdown_WithErrors(t *testing.T) {
	t.Parallel()

	c := New("err-session", "/tmp/test", nil, "CoordBot")

	digest := DigestSummary{
		Session:     "err-session",
		GeneratedAt: time.Now(),
		AgentCount:  2,
		ActiveCount: 0,
		IdleCount:   1,
		ErrorCount:  1,
		AgentStatuses: []AgentDigestStatus{
			{PaneIndex: 0, AgentType: "cc", Status: "error", ContextUsage: 90.0},
		},
		Alerts: []string{"Agent 0 (cc) in error state", "Agent 0 (cc) context at 90%"},
	}

	body := c.formatDigestMarkdown(digest)

	// Verify error count appears with warning
	if !strings.Contains(body, "**Errors:** 1 ⚠️") {
		t.Error("expected error count with warning emoji")
	}

	// Verify alerts section present
	if !strings.Contains(body, "## Alerts") {
		t.Error("expected Alerts section")
	}
	if !strings.Contains(body, "Agent 0 (cc) in error state") {
		t.Error("expected error alert text")
	}
}

func TestEmitEvent_AgentBusy(t *testing.T) {
	c := New("test-session", "/tmp/test", nil, "TestAgent")

	var (
		mu    sync.Mutex
		found bool
	)
	unsub := events.Subscribe("agent.busy", func(e events.BusEvent) {
		mu.Lock()
		found = true
		mu.Unlock()
	})
	defer unsub()

	agent := &AgentState{
		PaneID:    "%0",
		PaneIndex: 1,
		AgentType: "cc",
		Status:    robot.StateGenerating,
	}
	c.emitEvent(agent, robot.StateWaiting)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := found
		mu.Unlock()
		if done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timeout waiting for agent.busy bus event")
}

func TestEmitEvent_AgentError(t *testing.T) {
	c := New("test-session", "/tmp/test", nil, "TestAgent")

	agent := &AgentState{
		PaneID:    "%0",
		PaneIndex: 1,
		AgentType: "cc",
		Status:    robot.StateError,
	}
	// Should emit agent.error event (channel event)
	c.emitEvent(agent, robot.StateWaiting)

	select {
	case ev := <-c.Events():
		if ev.Type != EventAgentError {
			t.Errorf("expected EventAgentError, got %v", ev.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for error event")
	}
}

func TestEmitEvent_AgentRecovered(t *testing.T) {
	c := New("test-session", "/tmp/test", nil, "TestAgent")

	var (
		mu    sync.Mutex
		found bool
	)
	unsub := events.Subscribe("agent.recovered", func(e events.BusEvent) {
		mu.Lock()
		found = true
		mu.Unlock()
	})
	defer unsub()

	agent := &AgentState{
		PaneID:    "%0",
		PaneIndex: 1,
		AgentType: "cc",
		Status:    robot.StateStalled, // Not Waiting/Generating/Thinking/Error
	}
	// Previous was error, now stalled → recovered (not caught by earlier cases)
	c.emitEvent(agent, robot.StateError)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := found
		mu.Unlock()
		if done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timeout waiting for agent.recovered bus event")
}

func TestEmitEvent_AgentRecoveredTakesPrecedenceOverIdle(t *testing.T) {
	c := New("test-session", "/tmp/test", nil, "TestAgent")

	agent := &AgentState{
		PaneID:    "%0",
		PaneIndex: 1,
		AgentType: "cc",
		Status:    robot.StateWaiting,
	}

	c.emitEvent(agent, robot.StateError)

	select {
	case ev := <-c.Events():
		if ev.Type != EventAgentRecovered {
			t.Fatalf("expected EventAgentRecovered, got %v", ev.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for recovered event")
	}
}

func TestEmitEvent_NoEventForSameStatus(t *testing.T) {
	c := New("test-session", "/tmp/test", nil, "TestAgent")

	agent := &AgentState{
		PaneID:    "%0",
		PaneIndex: 1,
		AgentType: "cc",
		Status:    robot.StateWaiting,
	}
	// Same status → default case → no event
	c.emitEvent(agent, robot.StateWaiting)

	select {
	case <-c.Events():
		t.Error("should not emit event for same status")
	case <-time.After(50 * time.Millisecond):
		// Expected: no event
	}
}

func TestFormatDigestMarkdown_IdleForDash(t *testing.T) {
	t.Parallel()

	c := New("test-session", "/tmp/test", nil, "Bot")

	digest := DigestSummary{
		Session:     "test-session",
		GeneratedAt: time.Now(),
		AgentStatuses: []AgentDigestStatus{
			{PaneIndex: 0, AgentType: "cc", Status: "generating", ContextUsage: 50.0, IdleFor: ""},
		},
	}

	body := c.formatDigestMarkdown(digest)

	// Empty IdleFor should render as "-"
	if !strings.Contains(body, "| - |") {
		t.Error("expected '-' for empty IdleFor in table")
	}
}
