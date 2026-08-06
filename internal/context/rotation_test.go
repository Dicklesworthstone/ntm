package context

import (
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// MockPaneSpawner is a test double for PaneSpawner.
type MockPaneSpawner struct {
	spawnedPanes []string
	killedPanes  []string
	sentKeys     map[string][]string
	sentBuffers  map[string][]string
	getPanesFor  []string
	panes        []tmux.Pane
	spawnError   error
	killError    error
	sendError    error
	panesError   error
	getPanesFunc func(string) ([]tmux.Pane, error)
}

func NewMockPaneSpawner() *MockPaneSpawner {
	return &MockPaneSpawner{
		sentKeys:    make(map[string][]string),
		sentBuffers: make(map[string][]string),
		panes:       []tmux.Pane{},
	}
}

func (m *MockPaneSpawner) SpawnAgent(session, agentType string, index int, variant string, workDir string) (string, error) {
	if m.spawnError != nil {
		return "", m.spawnError
	}
	paneID := "%new-pane"
	m.spawnedPanes = append(m.spawnedPanes, paneID)
	return paneID, nil
}

func (m *MockPaneSpawner) KillPane(paneID string) error {
	if m.killError != nil {
		return m.killError
	}
	m.killedPanes = append(m.killedPanes, paneID)
	return nil
}

func (m *MockPaneSpawner) SendKeys(paneID, text string, enter bool) error {
	if m.sendError != nil {
		return m.sendError
	}
	m.sentKeys[paneID] = append(m.sentKeys[paneID], text)
	return nil
}

func (m *MockPaneSpawner) SendBuffer(paneID, text string, enter bool) error {
	if m.sendError != nil {
		return m.sendError
	}
	m.sentBuffers[paneID] = append(m.sentBuffers[paneID], text)
	return nil
}

func (m *MockPaneSpawner) GetPanes(session string) ([]tmux.Pane, error) {
	m.getPanesFor = append(m.getPanesFor, session)
	if m.getPanesFunc != nil {
		return m.getPanesFunc(session)
	}
	if m.panesError != nil {
		return nil, m.panesError
	}
	return m.panes, nil
}

func TestNewRotator(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	spawner := NewMockPaneSpawner()

	cfg := RotatorConfig{
		Monitor: monitor,
		Spawner: spawner,
		Config:  config.DefaultContextRotationConfig(),
	}

	r := NewRotator(cfg)

	if r.monitor != monitor {
		t.Error("monitor not set correctly")
	}
	if r.spawner != spawner {
		t.Error("spawner not set correctly")
	}
	if r.compactor == nil {
		t.Error("compactor should be created automatically when monitor is provided")
	}
	if r.summary == nil {
		t.Error("summary generator should be created automatically")
	}
}

func TestCheckAndRotate_NoMonitor(t *testing.T) {
	t.Parallel()

	r := NewRotator(RotatorConfig{
		Config: config.DefaultContextRotationConfig(),
	})

	_, err := r.CheckAndRotate("test-session", "/tmp")
	if err == nil || !strings.Contains(err.Error(), "no monitor") {
		t.Errorf("expected 'no monitor' error, got: %v", err)
	}
}

func TestCheckAndRotate_NoSpawner(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Config:  config.DefaultContextRotationConfig(),
	})

	_, err := r.CheckAndRotate("test-session", "/tmp")
	if err == nil || !strings.Contains(err.Error(), "no spawner") {
		t.Errorf("expected 'no spawner' error, got: %v", err)
	}
}

func TestCheckAndRotate_Disabled(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	spawner := NewMockPaneSpawner()

	cfg := config.DefaultContextRotationConfig()
	cfg.Enabled = false

	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Spawner: spawner,
		Config:  cfg,
	})

	results, err := r.CheckAndRotate("test-session", "/tmp")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if results != nil {
		t.Error("expected nil results when disabled")
	}
}

func TestCheckAndRotate_NoAgentsAboveThreshold(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	spawner := NewMockPaneSpawner()

	// Register an agent but don't add enough messages to exceed threshold
	monitor.RegisterAgent("test__cc_1", "%0", "claude-opus-4")
	monitor.RecordMessage("test__cc_1", 100, 100)

	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Spawner: spawner,
		Config:  config.DefaultContextRotationConfig(),
	})

	results, err := r.CheckAndRotate("test", "/tmp")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestCheckAndRotateEmitsWarningAtConfiguredThreshold(t *testing.T) {
	tracker := alerts.GetGlobalTracker()
	tracker.Clear()
	t.Cleanup(tracker.Clear)

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("test__cc_1", "%0", "claude-opus-4")
	for i := 0; i < 100; i++ {
		monitor.RecordMessage("test__cc_1", 1000, 1000)
	}

	cfg := config.DefaultContextRotationConfig()
	cfg.WarningThreshold = 0.30
	cfg.RotateThreshold = 0.90
	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Spawner: NewMockPaneSpawner(),
		Config:  cfg,
	})

	results, err := r.CheckAndRotate("test-session", "/tmp")
	if err != nil {
		t.Fatalf("CheckAndRotate() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("rotation results = %d, want none below rotate threshold", len(results))
	}

	active := tracker.GetActive()
	if len(active) != 1 {
		t.Fatalf("active alerts = %d, want 1", len(active))
	}
	if active[0].Type != alerts.AlertContextWarning {
		t.Errorf("alert type = %s, want %s", active[0].Type, alerts.AlertContextWarning)
	}
	if active[0].Session != "test-session" {
		t.Errorf("alert session = %q, want test-session", active[0].Session)
	}
}

func TestRotateAgentFailureEmitsRotationAlert(t *testing.T) {
	tracker := alerts.GetGlobalTracker()
	tracker.Clear()
	t.Cleanup(tracker.Clear)

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("test__cc_1", "%0", "claude-opus-4")
	spawner := NewMockPaneSpawner()
	spawner.panesError = errors.New("tmux unavailable")
	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Spawner: spawner,
		Config:  config.DefaultContextRotationConfig(),
	})

	result := r.rotateAgent("test-session", "test__cc_1", "/tmp")
	if result.State != RotationStateFailed {
		t.Fatalf("rotation state = %s, want failed", result.State)
	}

	active := tracker.GetActive()
	if len(active) != 1 {
		t.Fatalf("active alerts = %d, want 1", len(active))
	}
	if active[0].Type != alerts.AlertRotationFailed {
		t.Errorf("alert type = %s, want %s", active[0].Type, alerts.AlertRotationFailed)
	}
	if active[0].Session != "test-session" {
		t.Errorf("alert session = %q, want test-session", active[0].Session)
	}
}

func TestRotateAgentSuccessEmitsCompletionAlert(t *testing.T) {
	tracker := alerts.GetGlobalTracker()
	tracker.Clear()
	t.Cleanup(tracker.Clear)

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("test__cc_1", "%0", "claude-opus-4")
	monitor.RecordMessage("test__cc_1", 1000, 1000)
	spawner := NewMockPaneSpawner()
	spawner.panes = []tmux.Pane{{
		ID:    "%0",
		Title: "test__cc_1",
		Type:  tmux.AgentClaude,
	}}
	cfg := config.DefaultContextRotationConfig()
	cfg.TryCompactFirst = false
	r := NewRotator(RotatorConfig{Monitor: monitor, Spawner: spawner, Config: cfg})

	result := r.rotateAgent("test-session", "test__cc_1", "/tmp")
	if !result.Success || result.State != RotationStateCompleted {
		t.Fatalf("rotation result = %+v, want completed success", result)
	}

	active := tracker.GetActive()
	if len(active) != 1 {
		t.Fatalf("active alerts = %d, want 1", len(active))
	}
	if active[0].Type != alerts.AlertRotationComplete {
		t.Errorf("alert type = %s, want %s", active[0].Type, alerts.AlertRotationComplete)
	}
	if active[0].Context["old_agent_id"] != "test__cc_1" {
		t.Errorf("old agent = %v, want test__cc_1", active[0].Context["old_agent_id"])
	}
	if active[0].Context["new_agent_id"] != result.NewAgentID {
		t.Errorf("new agent = %v, want %s", active[0].Context["new_agent_id"], result.NewAgentID)
	}
}

func TestCheckAndRotate_LongSessionResetsReplacementMonitorState(t *testing.T) {
	monitor := NewContextMonitor(DefaultMonitorConfig())
	const agentID = "long-session__cc_1"
	monitor.RegisterAgent(agentID, "%0", "claude-opus-4")
	for i := 0; i < 200; i++ {
		monitor.RecordMessage(agentID, 1000, 1000)
	}

	spawner := NewMockPaneSpawner()
	spawner.panes = []tmux.Pane{{
		ID:    "%0",
		Title: agentID,
		Type:  tmux.AgentClaude,
	}}
	cfg := config.DefaultContextRotationConfig()
	cfg.RotateThreshold = 0.50
	cfg.TryCompactFirst = false
	r := NewRotator(RotatorConfig{Monitor: monitor, Spawner: spawner, Config: cfg})

	results, err := r.CheckAndRotate("long-session", t.TempDir())
	if err != nil {
		t.Fatalf("CheckAndRotate() error = %v", err)
	}
	if len(results) != 1 || !results[0].Success || results[0].State != RotationStateCompleted {
		t.Fatalf("CheckAndRotate() results = %+v, want one completed rotation", results)
	}

	replacement := monitor.GetState(results[0].NewAgentID)
	if replacement == nil {
		t.Fatal("replacement agent was not registered with the monitor")
	}
	if replacement.PaneID != results[0].NewPaneID {
		t.Errorf("replacement pane = %q, want %q", replacement.PaneID, results[0].NewPaneID)
	}
	if replacement.MessageCount != 0 || replacement.Estimate != nil {
		t.Errorf("replacement monitor state = %+v, want fresh context state", replacement)
	}

	results, err = r.CheckAndRotate("long-session", t.TempDir())
	if err != nil {
		t.Fatalf("second CheckAndRotate() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("second CheckAndRotate() results = %+v, want no immediate re-rotation", results)
	}
}

func TestCheckAndRotate_MixedGrokBatchIsAtomic(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("custom-claude-pane", "%1", "claude-opus-4")
	monitor.RegisterAgent("custom-grok-pane", "%2", "grok-build")
	for i := 0; i < 200; i++ {
		monitor.RecordMessage("custom-claude-pane", 1000, 1000)
		monitor.RecordMessage("custom-grok-pane", 1000, 1000)
	}

	spawner := NewMockPaneSpawner()
	spawner.panes = []tmux.Pane{
		{ID: "%1", Index: 1, Title: "custom-claude-pane", Type: tmux.AgentClaude},
		{ID: "%2", Index: 2, Title: "custom-grok-pane", Type: tmux.AgentGrok},
	}
	cfg := config.DefaultContextRotationConfig()
	cfg.RotateThreshold = 0.50
	r := NewRotator(RotatorConfig{Monitor: monitor, Spawner: spawner, Config: cfg})

	results, err := r.CheckAndRotate("test", "/tmp")
	if !errors.Is(err, agent.ErrAutomatedRelaunchNotImplemented) {
		t.Fatalf("CheckAndRotate() error = %v, want relaunch sentinel", err)
	}
	if results != nil {
		t.Fatalf("CheckAndRotate() results = %+v, want nil rejected batch", results)
	}
	if len(spawner.sentKeys) != 0 || len(spawner.sentBuffers) != 0 || len(spawner.spawnedPanes) != 0 || len(spawner.killedPanes) != 0 {
		t.Fatalf("mixed Grok batch mutated panes: keys=%v buffers=%v spawned=%v killed=%v", spawner.sentKeys, spawner.sentBuffers, spawner.spawnedPanes, spawner.killedPanes)
	}
}

func TestNeedsRotation(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	spawner := NewMockPaneSpawner()

	// Register an agent and add enough messages to exceed threshold
	monitor.RegisterAgent("test__cc_1", "%0", "claude-opus-4")
	for i := 0; i < 200; i++ {
		monitor.RecordMessage("test__cc_1", 1000, 1000)
	}

	cfg := config.DefaultContextRotationConfig()
	cfg.RotateThreshold = 0.50 // 50%

	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Spawner: spawner,
		Config:  cfg,
	})

	agents, reason := r.NeedsRotation()
	if len(agents) == 0 {
		t.Errorf("expected agents needing rotation, got none. Reason: %s", reason)
	}
	if !strings.Contains(reason, "above") && !strings.Contains(reason, "threshold") {
		t.Errorf("expected threshold reason, got: %s", reason)
	}
}

func TestNeedsWarning(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	spawner := NewMockPaneSpawner()

	// Register an agent and add enough messages to exceed warning threshold
	monitor.RegisterAgent("test__cc_1", "%0", "claude-opus-4")
	for i := 0; i < 100; i++ {
		monitor.RecordMessage("test__cc_1", 1000, 1000)
	}

	cfg := config.DefaultContextRotationConfig()
	cfg.WarningThreshold = 0.30 // 30%

	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Spawner: spawner,
		Config:  cfg,
	})

	agents, reason := r.NeedsWarning()
	if len(agents) == 0 {
		t.Errorf("expected agents needing warning, got none. Reason: %s", reason)
	}
}

func TestNeedsRotation_Disabled(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())

	cfg := config.DefaultContextRotationConfig()
	cfg.Enabled = false

	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Config:  cfg,
	})

	agents, reason := r.NeedsRotation()
	if len(agents) != 0 {
		t.Error("expected no agents when rotation disabled")
	}
	if !strings.Contains(reason, "disabled") {
		t.Errorf("expected disabled reason, got: %s", reason)
	}
}

func TestNeedsRotation_NoMonitor(t *testing.T) {
	t.Parallel()

	r := NewRotator(RotatorConfig{
		Config: config.DefaultContextRotationConfig(),
	})

	agents, reason := r.NeedsRotation()
	if len(agents) != 0 {
		t.Error("expected no agents when no monitor")
	}
	if !strings.Contains(reason, "no monitor") {
		t.Errorf("expected 'no monitor' reason, got: %s", reason)
	}
}

func TestGetHistory(t *testing.T) {
	t.Parallel()

	r := NewRotator(RotatorConfig{
		Config: config.DefaultContextRotationConfig(),
	})

	history := r.GetHistory()
	if len(history) != 0 {
		t.Error("expected empty history initially")
	}
}

func TestClearHistory(t *testing.T) {
	t.Parallel()

	r := NewRotator(RotatorConfig{
		Config: config.DefaultContextRotationConfig(),
	})

	// Manually add an event to history
	r.history = append(r.history, RotationEvent{
		SessionName: "test",
		OldAgentID:  "cc_1",
		NewAgentID:  "cc_1",
		Timestamp:   time.Now(),
	})

	if len(r.GetHistory()) != 1 {
		t.Error("expected 1 event in history")
	}

	r.ClearHistory()

	if len(r.GetHistory()) != 0 {
		t.Error("expected empty history after clear")
	}
}

func TestExtractAgentIndex(t *testing.T) {
	t.Parallel()

	tests := []struct {
		agentID string
		want    int
	}{
		{"myproject__cc_1", 1},
		{"myproject__cc_2", 2},
		{"myproject__cod_10", 10},
		{"myproject__gmi_3_variant", 3},
		{"invalid", 1},
		{"", 1},
	}

	for _, tt := range tests {
		t.Run(tt.agentID, func(t *testing.T) {
			t.Parallel()
			got := extractAgentIndex(tt.agentID)
			if got != tt.want {
				t.Errorf("extractAgentIndex(%q) = %d, want %d", tt.agentID, got, tt.want)
			}
		})
	}
}

func TestDeriveAgentTypeFromID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		agentID string
		want    string
	}{
		{"myproject__cc_1", "claude"},
		{"myproject__cod_2", "codex"},
		{"myproject__gmi_3", "gemini"},
		{"myproject__cc_1_opus", "claude"},
		{"my__project__cursor_4", "cursor"},
		{"invalid", "unknown"},
		{"", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.agentID, func(t *testing.T) {
			t.Parallel()
			got := deriveAgentTypeFromID(tt.agentID)
			if got != tt.want {
				t.Errorf("deriveAgentTypeFromID(%q) = %q, want %q", tt.agentID, got, tt.want)
			}
		})
	}
}

func TestAgentTypeShort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		agentType string
		want      string
	}{
		{"claude", "cc"},
		{"Claude", "cc"},
		{"cc", "cc"},
		{"claude_code", "cc"},
		{"codex", "cod"},
		{"cod", "cod"},
		{"openai-codex", "cod"},
		{"gemini", "gmi"},
		{"gmi", "gmi"},
		{"google-gemini", "gmi"},
		{"ws", "windsurf"},
		{"ollama", "ollama"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			t.Parallel()
			got := agentTypeShort(tt.agentType)
			if got != tt.want {
				t.Errorf("agentTypeShort(%q) = %q, want %q", tt.agentType, got, tt.want)
			}
		})
	}
}

func TestAgentTypeLong(t *testing.T) {
	t.Parallel()

	tests := []struct {
		shortType string
		want      string
	}{
		{"cc", "claude"},
		{"claude_code", "claude"},
		{"cod", "codex"},
		{"openai-codex", "codex"},
		{"gmi", "gemini"},
		{"google-gemini", "gemini"},
		{"ws", "windsurf"},
		{"ollama", "ollama"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.shortType, func(t *testing.T) {
			t.Parallel()
			got := agentTypeLong(tt.shortType)
			if got != tt.want {
				t.Errorf("agentTypeLong(%q) = %q, want %q", tt.shortType, got, tt.want)
			}
		})
	}
}

func TestRotationResultFormatForDisplay(t *testing.T) {
	t.Parallel()

	successResult := &RotationResult{
		Success:       true,
		OldAgentID:    "test__cc_1",
		NewAgentID:    "test__cc_1",
		Method:        RotationThresholdExceeded,
		State:         RotationStateCompleted,
		SummaryTokens: 500,
		Duration:      5 * time.Second,
	}

	output := successResult.FormatForDisplay()
	if !strings.Contains(output, "✓") {
		t.Error("success output should contain checkmark")
	}
	if !strings.Contains(output, "test__cc_1") {
		t.Error("output should contain agent ID")
	}
	if !strings.Contains(output, "completed") {
		t.Error("output should contain state")
	}

	failResult := &RotationResult{
		Success:    false,
		OldAgentID: "test__cc_1",
		State:      RotationStateFailed,
		Error:      "test error",
	}

	output = failResult.FormatForDisplay()
	if !strings.Contains(output, "✗") {
		t.Error("failure output should contain X mark")
	}
	if !strings.Contains(output, "test error") {
		t.Error("output should contain error message")
	}
}

func TestManualRotate_NoMonitor(t *testing.T) {
	t.Parallel()

	r := NewRotator(RotatorConfig{
		Config: config.DefaultContextRotationConfig(),
	})

	result := r.ManualRotate("test-session", "test__cc_1", "/tmp")
	if result.Success {
		t.Error("expected failure when no monitor")
	}
	if !strings.Contains(result.Error, "no monitor") {
		t.Errorf("expected 'no monitor' error, got: %s", result.Error)
	}
	if result.Method != RotationManual {
		t.Errorf("expected RotationManual method, got: %s", result.Method)
	}
}

func TestManualRotate_NoSpawner(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Config:  config.DefaultContextRotationConfig(),
	})

	result := r.ManualRotate("test-session", "test__cc_1", "/tmp")
	if result.Success {
		t.Error("expected failure when no spawner")
	}
	if !strings.Contains(result.Error, "no spawner") {
		t.Errorf("expected 'no spawner' error, got: %s", result.Error)
	}
	if result.Method != RotationManual {
		t.Errorf("expected RotationManual method, got: %s", result.Method)
	}
}

func TestManualRotate_GrokFailsBeforeAnyLifecycleMutation(t *testing.T) {
	t.Parallel()

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("test__grok_1", "%7", "grok-build")
	monitor.RecordMessage("test__grok_1", 1000, 1000)

	spawner := NewMockPaneSpawner()
	spawner.panes = []tmux.Pane{{
		ID:       "%7",
		Title:    "test__grok_1",
		Type:     agent.AgentTypeGrok,
		NTMIndex: 1,
	}}

	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Spawner: spawner,
		Config:  config.DefaultContextRotationConfig(),
	})
	result := r.ManualRotate("test", "test__grok_1", "/tmp")

	if result.Success || result.State != RotationStateFailed {
		t.Fatalf("ManualRotate() result = %+v, want failed", result)
	}
	if !strings.Contains(result.Error, agent.GrokPhaseOneCapabilityHint) {
		t.Fatalf("ManualRotate() error = %q, want Grok capability hint", result.Error)
	}
	if len(spawner.sentKeys) != 0 || len(spawner.sentBuffers) != 0 {
		t.Fatalf("Grok rotation sent input: keys=%v buffers=%v", spawner.sentKeys, spawner.sentBuffers)
	}
	if len(spawner.spawnedPanes) != 0 {
		t.Fatalf("Grok rotation spawned panes: %v", spawner.spawnedPanes)
	}
	if len(spawner.killedPanes) != 0 {
		t.Fatalf("Grok rotation killed panes: %v", spawner.killedPanes)
	}
}

func TestDefaultPaneSpawnerRejectsGrokBeforeCreatingPane(t *testing.T) {
	t.Parallel()

	spawner := NewDefaultPaneSpawner(nil)
	if _, err := spawner.SpawnAgent("test", "grok-build", 1, "", "/tmp"); !errors.Is(err, agent.ErrAutomatedRelaunchNotImplemented) {
		t.Fatalf("SpawnAgent(grok-build) error = %v, want relaunch sentinel", err)
	}
	if got := spawner.getAgentCommand("grok-build"); got != "" {
		t.Fatalf("getAgentCommand(grok-build) = %q, want fail-closed empty command", got)
	}
}

func TestDefaultPaneSpawnerGetAgentCommand(t *testing.T) {
	t.Parallel()

	// Without config
	spawner := NewDefaultPaneSpawner(nil)

	tests := []struct {
		agentType string
		want      string
	}{
		{"claude", "claude"},
		{"claude_code", "claude"},
		{"codex", "codex"},
		{"openai-codex", "codex"},
		{"gemini", "gemini"},
		{"google-gemini", "gemini"},
		{"ws", "windsurf"},
		{"ollama", "ollama"},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			got := spawner.getAgentCommand(tt.agentType)
			if got != tt.want {
				t.Errorf("getAgentCommand(%q) = %q, want %q", tt.agentType, got, tt.want)
			}
		})
	}

	// With custom config
	cfg := &config.Config{}
	cfg.Agents.Claude = "custom-claude"
	cfg.Agents.Codex = "custom-codex"
	cfg.Agents.Gemini = "custom-gemini"
	cfg.Agents.Cursor = "custom-cursor"
	cfg.Agents.Windsurf = "custom-windsurf"
	cfg.Agents.Aider = "custom-aider"
	cfg.Agents.Ollama = "custom-ollama"

	spawner2 := NewDefaultPaneSpawner(cfg)

	if got := spawner2.getAgentCommand("claude"); got != "custom-claude" {
		t.Errorf("expected custom-claude, got %q", got)
	}
	if got := spawner2.getAgentCommand("codex"); got != "custom-codex" {
		t.Errorf("expected custom-codex, got %q", got)
	}
	if got := spawner2.getAgentCommand("gemini"); got != "custom-gemini" {
		t.Errorf("expected custom-gemini, got %q", got)
	}
	if got := spawner2.getAgentCommand("cursor"); got != "custom-cursor" {
		t.Errorf("expected custom-cursor, got %q", got)
	}
	if got := spawner2.getAgentCommand("ws"); got != "custom-windsurf" {
		t.Errorf("expected custom-windsurf, got %q", got)
	}
	if got := spawner2.getAgentCommand("aider"); got != "custom-aider" {
		t.Errorf("expected custom-aider, got %q", got)
	}
	if got := spawner2.getAgentCommand("ollama"); got != "custom-ollama" {
		t.Errorf("expected custom-ollama, got %q", got)
	}
}

func TestSendCompactionCommandToPane_UsesBufferForPrompts(t *testing.T) {
	t.Parallel()

	spawner := NewMockPaneSpawner()
	cmd := CompactionCommand{
		Command:  CompactionPromptTemplate,
		IsPrompt: true,
	}

	if err := sendCompactionCommandToPane(spawner, "%1", cmd); err != nil {
		t.Fatalf("sendCompactionCommandToPane() error = %v", err)
	}

	if got := len(spawner.sentBuffers["%1"]); got != 1 {
		t.Fatalf("buffer sends = %d, want 1", got)
	}
	if got := len(spawner.sentKeys["%1"]); got != 0 {
		t.Fatalf("key sends = %d, want 0", got)
	}
	if got := spawner.sentBuffers["%1"][0]; got != CompactionPromptTemplate {
		t.Fatalf("buffer payload mismatch: got %q", got)
	}
}

func TestSendCompactionCommandToPane_UsesKeysForCommands(t *testing.T) {
	t.Parallel()

	spawner := NewMockPaneSpawner()
	cmd := CompactionCommand{
		Command: "/compact",
	}

	if err := sendCompactionCommandToPane(spawner, "%2", cmd); err != nil {
		t.Fatalf("sendCompactionCommandToPane() error = %v", err)
	}

	if got := len(spawner.sentKeys["%2"]); got != 1 {
		t.Fatalf("key sends = %d, want 1", got)
	}
	if got := len(spawner.sentBuffers["%2"]); got != 0 {
		t.Fatalf("buffer sends = %d, want 0", got)
	}
	if got := spawner.sentKeys["%2"][0]; got != "/compact" {
		t.Fatalf("key payload mismatch: got %q", got)
	}
}

func TestSendRotationPrompt_UsesBuffer(t *testing.T) {
	t.Parallel()

	spawner := NewMockPaneSpawner()
	prompt := SummaryPromptTemplate

	if err := sendRotationPrompt(spawner, "%3", prompt); err != nil {
		t.Fatalf("sendRotationPrompt() error = %v", err)
	}

	if got := len(spawner.sentBuffers["%3"]); got != 1 {
		t.Fatalf("buffer sends = %d, want 1", got)
	}
	if got := len(spawner.sentKeys["%3"]); got != 0 {
		t.Fatalf("key sends = %d, want 0", got)
	}
	if got := spawner.sentBuffers["%3"][0]; got != prompt {
		t.Fatalf("buffer payload mismatch: got %q", got)
	}
}

func TestTryCompaction_ExhaustsFallbackCommandsViaSpawner(t *testing.T) {
	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("test__cc_1", "%0", "claude-opus-4")
	monitor.RecordMessage("test__cc_1", 500, 500)

	spawner := NewMockPaneSpawner()
	compactor := NewCompactor(monitor, CompactorConfig{
		MinReduction:     0.10,
		BuiltinTimeout:   time.Millisecond,
		SummarizeTimeout: time.Millisecond,
	})

	r := NewRotator(RotatorConfig{
		Monitor:   monitor,
		Spawner:   spawner,
		Compactor: compactor,
		Config:    config.DefaultContextRotationConfig(),
	})

	result := r.tryCompaction("test__cc_1", "%0", tmux.AgentClaude)
	if result == nil {
		t.Fatal("expected compaction result, got nil")
	}
	if result.Success {
		t.Fatal("expected compaction to fail without any context reduction")
	}
	if result.Method != CompactionFailed {
		t.Fatalf("Method = %s, want %s", result.Method, CompactionFailed)
	}
	if !strings.Contains(result.Error, "exhausted") {
		t.Fatalf("Error = %q, want exhausted message", result.Error)
	}

	if got := spawner.sentKeys["%0"]; len(got) != 2 || got[0] != "/compact" || got[1] != "/clear" {
		t.Fatalf("sentKeys = %#v, want [/compact /clear]", got)
	}
	if got := len(spawner.sentBuffers["%0"]); got != 1 {
		t.Fatalf("buffer sends = %d, want 1", got)
	}
	if got := spawner.sentBuffers["%0"][0]; got != CompactionPromptTemplate {
		t.Fatalf("buffer payload mismatch: got %q", got)
	}
}

// =============================================================================
// ToPendingRotation / FromPendingRotation
// =============================================================================

func TestToPendingRotation(t *testing.T) {
	t.Parallel()

	now := time.Now()
	timeout := now.Add(5 * time.Minute)

	stored := &StoredPendingRotation{
		AgentID:        "agent-1",
		SessionName:    "session-1",
		PaneID:         "pane-1",
		ContextPercent: 85.5,
		CreatedAt:      now,
		TimeoutAt:      timeout,
		DefaultAction:  ConfirmRotate,
		WorkDir:        "/data/project",
	}

	pending := stored.ToPendingRotation()

	if pending.AgentID != stored.AgentID {
		t.Errorf("AgentID = %q, want %q", pending.AgentID, stored.AgentID)
	}
	if pending.SessionName != stored.SessionName {
		t.Errorf("SessionName = %q, want %q", pending.SessionName, stored.SessionName)
	}
	if pending.PaneID != stored.PaneID {
		t.Errorf("PaneID = %q, want %q", pending.PaneID, stored.PaneID)
	}
	if pending.ContextPercent != stored.ContextPercent {
		t.Errorf("ContextPercent = %f, want %f", pending.ContextPercent, stored.ContextPercent)
	}
	if !pending.CreatedAt.Equal(stored.CreatedAt) {
		t.Errorf("CreatedAt mismatch")
	}
	if !pending.TimeoutAt.Equal(stored.TimeoutAt) {
		t.Errorf("TimeoutAt mismatch")
	}
	if pending.DefaultAction != stored.DefaultAction {
		t.Errorf("DefaultAction = %q, want %q", pending.DefaultAction, stored.DefaultAction)
	}
	if pending.WorkDir != stored.WorkDir {
		t.Errorf("WorkDir = %q, want %q", pending.WorkDir, stored.WorkDir)
	}
}

func TestFromPendingRotation(t *testing.T) {
	t.Parallel()

	now := time.Now()
	timeout := now.Add(10 * time.Minute)

	pending := &PendingRotation{
		AgentID:        "agent-2",
		SessionName:    "session-2",
		PaneID:         "pane-2",
		ContextPercent: 92.0,
		CreatedAt:      now,
		TimeoutAt:      timeout,
		DefaultAction:  ConfirmCompact,
		WorkDir:        "/home/user/project",
	}

	stored := FromPendingRotation(pending)

	if stored.AgentID != pending.AgentID {
		t.Errorf("AgentID = %q, want %q", stored.AgentID, pending.AgentID)
	}
	if stored.ContextPercent != pending.ContextPercent {
		t.Errorf("ContextPercent = %f, want %f", stored.ContextPercent, pending.ContextPercent)
	}
	if stored.DefaultAction != pending.DefaultAction {
		t.Errorf("DefaultAction = %q, want %q", stored.DefaultAction, pending.DefaultAction)
	}
}

func TestCheckAndRotate_RequireConfirmCreatesPendingRotation(t *testing.T) {
	oldStore := DefaultPendingRotationStore
	DefaultPendingRotationStore = NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	t.Cleanup(func() {
		DefaultPendingRotationStore = oldStore
	})

	monitor := NewContextMonitor(DefaultMonitorConfig())
	spawner := NewMockPaneSpawner()
	spawner.panes = []tmux.Pane{{ID: "%0", Title: "test__cc_1", Type: tmux.AgentClaude}}

	monitor.RegisterAgent("test__cc_1", "%0", "claude-opus-4")
	for i := 0; i < 200; i++ {
		monitor.RecordMessage("test__cc_1", 1000, 1000)
	}

	cfg := config.DefaultContextRotationConfig()
	cfg.RotateThreshold = 0.50
	cfg.RequireConfirm = true

	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Spawner: spawner,
		Config:  cfg,
	})

	results, err := r.CheckAndRotate("test-session", "/tmp/project")
	if err != nil {
		t.Fatalf("CheckAndRotate() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("CheckAndRotate() returned %d results, want 1", len(results))
	}
	if results[0].State != RotationStatePending {
		t.Fatalf("rotation state = %s, want %s", results[0].State, RotationStatePending)
	}
	if !r.HasPendingRotation("test__cc_1") {
		t.Fatal("expected pending rotation to be tracked in memory")
	}

	pending := r.GetPendingRotation("test__cc_1")
	if pending == nil {
		t.Fatal("expected pending rotation to be retrievable")
	}
	if pending.SessionName != "test-session" {
		t.Fatalf("pending session = %q, want %q", pending.SessionName, "test-session")
	}
	if pending.WorkDir != "/tmp/project" {
		t.Fatalf("pending workdir = %q, want %q", pending.WorkDir, "/tmp/project")
	}
}

func TestProcessExpiredPending_UsesStoredSession(t *testing.T) {
	oldStore := DefaultPendingRotationStore
	DefaultPendingRotationStore = NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	t.Cleanup(func() {
		DefaultPendingRotationStore = oldStore
	})

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("test__cc_1", "%0", "claude-opus-4")

	spawner := NewMockPaneSpawner()
	spawner.panesError = errors.New("boom")

	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Spawner: spawner,
		Config:  config.DefaultContextRotationConfig(),
	})

	pending := &PendingRotation{
		AgentID:       "test__cc_1",
		SessionName:   "stored-session",
		PaneID:        "%0",
		TimeoutAt:     time.Now().Add(-time.Minute),
		DefaultAction: ConfirmRotate,
		WorkDir:       "/stored/workdir",
	}
	r.pending[pending.AgentID] = pending

	r.processExpiredPending("caller-session", "/caller/workdir")

	if len(spawner.getPanesFor) != 1 {
		t.Fatalf("GetPanes called %d times, want 1", len(spawner.getPanesFor))
	}
	if spawner.getPanesFor[0] != "stored-session" {
		t.Fatalf("GetPanes session = %q, want %q", spawner.getPanesFor[0], "stored-session")
	}
	if !r.HasPendingRotation(pending.AgentID) {
		t.Fatal("failed live-pane preflight removed the expired pending rotation")
	}
}

func TestProcessExpiredPending_MixedGrokBatchIsAtomic(t *testing.T) {
	oldStore := DefaultPendingRotationStore
	DefaultPendingRotationStore = NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	t.Cleanup(func() {
		DefaultPendingRotationStore = oldStore
	})

	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent("custom-claude-pane", "%1", "claude-opus-4")
	monitor.RegisterAgent("custom-grok-pane", "%2", "grok-3")
	spawner := NewMockPaneSpawner()
	spawner.panes = []tmux.Pane{
		{ID: "%1", Index: 1, Title: "custom-claude-pane", Type: tmux.AgentClaude},
		{ID: "%2", Index: 2, Title: "custom-grok-pane", Type: tmux.AgentGrok},
	}
	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Spawner: spawner,
		Config:  config.DefaultContextRotationConfig(),
	})
	now := time.Now()
	pending := []*PendingRotation{
		{
			AgentID:       "custom-claude-pane",
			SessionName:   "test",
			PaneID:        "%1",
			TimeoutAt:     now.Add(-2 * time.Minute),
			DefaultAction: ConfirmRotate,
			WorkDir:       "/tmp",
		},
		{
			AgentID:       "custom-grok-pane",
			SessionName:   "test",
			PaneID:        "%2",
			TimeoutAt:     now.Add(-time.Minute),
			DefaultAction: ConfirmCompact,
			WorkDir:       "/tmp",
		},
	}
	for _, item := range pending {
		r.pending[item.AgentID] = item
		stored := clonePendingRotation(item)
		stored.TimeoutAt = now.Add(time.Hour)
		if err := AddPendingRotation(stored); err != nil {
			t.Fatalf("AddPendingRotation(%s) error = %v", item.AgentID, err)
		}
	}

	r.processExpiredPending("caller-session", "/caller/workdir")

	for _, item := range pending {
		got := r.GetPendingRotation(item.AgentID)
		if got == nil {
			t.Fatalf("pending rotation %s was deleted before batch preflight completed", item.AgentID)
		}
		if !got.TimeoutAt.Equal(item.TimeoutAt) {
			t.Fatalf("pending rotation %s timeout changed from %s to %s", item.AgentID, item.TimeoutAt, got.TimeoutAt)
		}
		stored, err := GetPendingRotationByID(item.AgentID)
		if err != nil {
			t.Fatalf("GetPendingRotationByID(%s) error = %v", item.AgentID, err)
		}
		if stored == nil {
			t.Fatalf("persisted pending rotation %s was deleted", item.AgentID)
		}
	}
	if len(spawner.sentKeys) != 0 || len(spawner.sentBuffers) != 0 || len(spawner.spawnedPanes) != 0 || len(spawner.killedPanes) != 0 {
		t.Fatalf("expired mixed batch mutated panes: keys=%v buffers=%v spawned=%v killed=%v", spawner.sentKeys, spawner.sentBuffers, spawner.spawnedPanes, spawner.killedPanes)
	}
}

func TestProcessExpiredPendingDoesNotActAfterConcurrentPostpone(t *testing.T) {
	oldStore := DefaultPendingRotationStore
	DefaultPendingRotationStore = NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	t.Cleanup(func() {
		DefaultPendingRotationStore = oldStore
	})

	const agentID = "test__cc_1"
	monitor := NewContextMonitor(DefaultMonitorConfig())
	monitor.RegisterAgent(agentID, "%1", "claude-opus-4")

	preflightStarted := make(chan struct{})
	releasePreflight := make(chan struct{})
	var getPanesCalls atomic.Int32
	spawner := NewMockPaneSpawner()
	spawner.panes = []tmux.Pane{{
		ID:    "%1",
		Index: 1,
		Title: agentID,
		Type:  tmux.AgentClaude,
	}}
	spawner.sendError = errors.New("stale rotation reached pane mutation")
	spawner.getPanesFunc = func(string) ([]tmux.Pane, error) {
		if getPanesCalls.Add(1) == 1 {
			close(preflightStarted)
			<-releasePreflight
		}
		return spawner.panes, nil
	}

	r := NewRotator(RotatorConfig{
		Monitor: monitor,
		Spawner: spawner,
		Config:  config.DefaultContextRotationConfig(),
	})
	pending := &PendingRotation{
		AgentID:       agentID,
		SessionName:   "test-session",
		PaneID:        "%1",
		TimeoutAt:     time.Now().Add(-time.Minute),
		DefaultAction: ConfirmRotate,
		WorkDir:       "/tmp",
	}
	r.pending[agentID] = pending
	if err := AddPendingRotation(clonePendingRotation(pending)); err != nil {
		t.Fatalf("AddPendingRotation error = %v", err)
	}

	expiredDone := make(chan struct{})
	go func() {
		defer close(expiredDone)
		r.processExpiredPending("caller-session", "/caller/workdir")
	}()

	select {
	case <-preflightStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("expired rotation preflight did not start")
	}
	result := r.ConfirmRotation(agentID, ConfirmPostpone, 10)
	if !result.Success || result.State != RotationStatePending {
		t.Fatalf("ConfirmRotation(postpone) result = %+v", result)
	}
	close(releasePreflight)
	select {
	case <-expiredDone:
	case <-time.After(2 * time.Second):
		t.Fatal("expired rotation processing did not finish")
	}

	if got := getPanesCalls.Load(); got != 1 {
		t.Fatalf("GetPanes calls = %d, want only the blocked preflight", got)
	}
	if len(spawner.sentKeys) != 0 || len(spawner.sentBuffers) != 0 || len(spawner.spawnedPanes) != 0 || len(spawner.killedPanes) != 0 {
		t.Fatalf("stale expired action mutated panes: keys=%v buffers=%v spawned=%v killed=%v",
			spawner.sentKeys, spawner.sentBuffers, spawner.spawnedPanes, spawner.killedPanes)
	}
	inMemory := r.GetPendingRotation(agentID)
	if inMemory == nil || !inMemory.TimeoutAt.After(time.Now().Add(9*time.Minute)) {
		t.Fatalf("postponed in-memory rotation = %+v, want timeout about 10 minutes ahead", inMemory)
	}
	stored, err := GetPendingRotationByID(agentID)
	if err != nil {
		t.Fatalf("GetPendingRotationByID error = %v", err)
	}
	if stored == nil || !stored.TimeoutAt.Equal(inMemory.TimeoutAt) {
		t.Fatalf("persisted postponed rotation = %+v, want timeout %s", stored, inMemory.TimeoutAt)
	}
}

func TestProcessExpiredPending_AllowsIgnoreAndPostpone(t *testing.T) {
	oldStore := DefaultPendingRotationStore
	DefaultPendingRotationStore = NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	t.Cleanup(func() {
		DefaultPendingRotationStore = oldStore
	})

	spawner := NewMockPaneSpawner()
	r := NewRotator(RotatorConfig{Spawner: spawner, Config: config.DefaultContextRotationConfig()})
	now := time.Now()
	r.pending["test__grok_1"] = &PendingRotation{
		AgentID:       "test__grok_1",
		SessionName:   "test",
		TimeoutAt:     now.Add(-time.Minute),
		DefaultAction: ConfirmIgnore,
	}
	r.pending["test__grok_2"] = &PendingRotation{
		AgentID:       "test__grok_2",
		SessionName:   "test",
		TimeoutAt:     now.Add(-time.Minute),
		DefaultAction: ConfirmPostpone,
	}

	r.processExpiredPending("test", "/tmp")

	if r.HasPendingRotation("test__grok_1") {
		t.Fatal("expired ignore action should remove the pending rotation")
	}
	postponed := r.GetPendingRotation("test__grok_2")
	if postponed == nil {
		t.Fatal("expired postpone action should retain the pending rotation")
	}
	if !postponed.TimeoutAt.After(now.Add(29 * time.Minute)) {
		t.Fatalf("postponed timeout = %s, want about 30 minutes in the future", postponed.TimeoutAt)
	}
	if len(spawner.sentKeys) != 0 || len(spawner.sentBuffers) != 0 || len(spawner.spawnedPanes) != 0 || len(spawner.killedPanes) != 0 {
		t.Fatalf("ignore/postpone mutated panes: keys=%v buffers=%v spawned=%v killed=%v", spawner.sentKeys, spawner.sentBuffers, spawner.spawnedPanes, spawner.killedPanes)
	}
}

func TestConfirmRotation_PostponeUpdatesTimeout(t *testing.T) {
	oldStore := DefaultPendingRotationStore
	DefaultPendingRotationStore = NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	t.Cleanup(func() {
		DefaultPendingRotationStore = oldStore
	})

	r := NewRotator(RotatorConfig{})
	originalTimeout := time.Now().Add(2 * time.Minute)
	r.pending["agent-1"] = &PendingRotation{
		AgentID:       "agent-1",
		SessionName:   "test-session",
		TimeoutAt:     originalTimeout,
		DefaultAction: ConfirmRotate,
	}

	result := r.ConfirmRotation("agent-1", ConfirmPostpone, 10)
	if !result.Success {
		t.Fatalf("ConfirmRotation(postpone) success = false, error = %q", result.Error)
	}
	if result.State != RotationStatePending {
		t.Fatalf("ConfirmRotation(postpone) state = %s, want %s", result.State, RotationStatePending)
	}

	pending := r.GetPendingRotation("agent-1")
	if pending == nil {
		t.Fatal("expected pending rotation to remain after postpone")
	}
	if !pending.TimeoutAt.After(originalTimeout) {
		t.Fatalf("postponed timeout = %s, want after %s", pending.TimeoutAt, originalTimeout)
	}
}

func TestConfirmRotation_CompactWithoutPaneKeepsPending(t *testing.T) {
	t.Parallel()

	r := NewRotator(RotatorConfig{})
	r.pending["agent-1"] = &PendingRotation{
		AgentID:     "agent-1",
		SessionName: "test-session",
	}

	result := r.ConfirmRotation("agent-1", ConfirmCompact, 0)
	if result.State != RotationStateFailed {
		t.Fatalf("ConfirmRotation(compact) state = %s, want %s", result.State, RotationStateFailed)
	}
	if !strings.Contains(result.Error, "pane ID unknown") {
		t.Fatalf("ConfirmRotation(compact) error = %q", result.Error)
	}
	if !r.HasPendingRotation("agent-1") {
		t.Fatal("expected pending rotation to remain when compaction cannot run")
	}
}

func TestConfirmRotation_CustomTitleGrokKeepsPending(t *testing.T) {
	for _, tt := range []struct {
		action ConfirmAction
		hint   string
	}{
		{action: ConfirmRotate, hint: agent.GrokPhaseOneCapabilityHint},
		{action: ConfirmCompact, hint: agent.GrokPromptDeliveryCapabilityHint},
	} {
		t.Run(string(tt.action), func(t *testing.T) {
			spawner := NewMockPaneSpawner()
			spawner.panes = []tmux.Pane{{
				ID: "%7", Index: 7, Title: "operator-selected-title", Type: tmux.AgentGrok,
			}}
			r := NewRotator(RotatorConfig{Spawner: spawner})
			r.pending["operator-selected-title"] = &PendingRotation{
				AgentID: "operator-selected-title", SessionName: "test-session", PaneID: "%7",
			}

			result := r.ConfirmRotation("operator-selected-title", tt.action, 0)
			if result.State != RotationStateFailed || !strings.Contains(result.Error, tt.hint) {
				t.Fatalf("ConfirmRotation(%s) result = %+v, want Grok capability failure", tt.action, result)
			}
			if !r.HasPendingRotation("operator-selected-title") {
				t.Fatalf("ConfirmRotation(%s) removed pending state before capability admission", tt.action)
			}
			if len(spawner.sentKeys) != 0 || len(spawner.sentBuffers) != 0 || len(spawner.spawnedPanes) != 0 || len(spawner.killedPanes) != 0 {
				t.Fatalf("ConfirmRotation(%s) mutated panes: %+v", tt.action, spawner)
			}
		})
	}
}

func TestPendingRotationRoundTrip(t *testing.T) {
	t.Parallel()

	now := time.Now().Truncate(time.Second) // Truncate for comparison safety
	timeout := now.Add(30 * time.Minute)

	original := &PendingRotation{
		AgentID:        "round-trip-agent",
		SessionName:    "round-trip-session",
		PaneID:         "round-trip-pane",
		ContextPercent: 77.3,
		CreatedAt:      now,
		TimeoutAt:      timeout,
		DefaultAction:  ConfirmIgnore,
		WorkDir:        "/tmp/round-trip",
	}

	stored := FromPendingRotation(original)
	restored := stored.ToPendingRotation()

	if restored.AgentID != original.AgentID {
		t.Errorf("AgentID mismatch after round trip")
	}
	if restored.SessionName != original.SessionName {
		t.Errorf("SessionName mismatch after round trip")
	}
	if restored.ContextPercent != original.ContextPercent {
		t.Errorf("ContextPercent mismatch after round trip")
	}
	if restored.DefaultAction != original.DefaultAction {
		t.Errorf("DefaultAction mismatch after round trip")
	}
	if restored.WorkDir != original.WorkDir {
		t.Errorf("WorkDir mismatch after round trip")
	}
}
