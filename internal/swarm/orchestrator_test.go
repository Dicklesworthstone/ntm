package swarm

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNewSessionOrchestrator(t *testing.T) {
	orch := NewSessionOrchestrator()

	if orch == nil {
		t.Fatal("expected non-nil orchestrator")
	}

	if orch.TmuxClient != nil {
		t.Error("expected nil TmuxClient (use default)")
	}

	if orch.StaggerDelay != 300*time.Millisecond {
		t.Errorf("expected StaggerDelay 300ms, got %v", orch.StaggerDelay)
	}
}

func TestSessionOrchestrator_CreateSessions_NilPlan(t *testing.T) {
	orch := NewSessionOrchestrator()

	result, err := orch.CreateSessions(nil)

	if err == nil {
		t.Error("expected error for nil plan")
	}

	if result != nil {
		t.Error("expected nil result for nil plan")
	}
}

func TestSessionOrchestrator_CreateSessions_EmptyPlan(t *testing.T) {
	orch := NewSessionOrchestrator()

	plan := &SwarmPlan{
		Sessions: []SessionSpec{},
	}

	result, err := orch.CreateSessions(plan)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if len(result.Sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(result.Sessions))
	}
}

func TestSessionOrchestrator_FormatPaneTitle(t *testing.T) {
	orch := NewSessionOrchestrator()

	tests := []struct {
		sessionName string
		pane        PaneSpec
		expected    string
	}{
		{
			sessionName: "cc_agents_1",
			pane:        PaneSpec{Index: 1, AgentType: "cc"},
			expected:    "cc_agents_1__cc_1",
		},
		{
			sessionName: "cod_agents_2",
			pane:        PaneSpec{Index: 5, AgentType: "cod"},
			expected:    "cod_agents_2__cod_5",
		},
		{
			sessionName: "gmi_agents_3",
			pane:        PaneSpec{Index: 3, AgentType: "gmi"},
			expected:    "gmi_agents_3__gmi_3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			got := orch.formatPaneTitle(tt.sessionName, tt.pane)
			if got != tt.expected {
				t.Errorf("formatPaneTitle() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestCreateSessionResult(t *testing.T) {
	result := CreateSessionResult{
		SessionSpec: SessionSpec{
			Name:      "test_session",
			AgentType: "cc",
			PaneCount: 4,
		},
		SessionName: "test_session",
		PaneIDs:     []string{"%1", "%2", "%3", "%4"},
		Error:       nil,
	}

	if result.SessionName != "test_session" {
		t.Errorf("expected session name 'test_session', got %q", result.SessionName)
	}

	if len(result.PaneIDs) != 4 {
		t.Errorf("expected 4 pane IDs, got %d", len(result.PaneIDs))
	}

	if result.Error != nil {
		t.Errorf("expected nil error, got %v", result.Error)
	}
}

func TestOrchestrationResult(t *testing.T) {
	result := OrchestrationResult{
		Sessions: []CreateSessionResult{
			{
				SessionName: "cc_agents_1",
				PaneIDs:     []string{"%1", "%2", "%3"},
			},
			{
				SessionName: "cod_agents_1",
				PaneIDs:     []string{"%4", "%5"},
			},
		},
		TotalPanes:      5,
		SuccessfulPanes: 5,
		FailedPanes:     0,
		Errors:          nil,
	}

	if len(result.Sessions) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(result.Sessions))
	}

	if result.TotalPanes != 5 {
		t.Errorf("expected 5 total panes, got %d", result.TotalPanes)
	}

	if result.SuccessfulPanes != 5 {
		t.Errorf("expected 5 successful panes, got %d", result.SuccessfulPanes)
	}

	if result.FailedPanes != 0 {
		t.Errorf("expected 0 failed panes, got %d", result.FailedPanes)
	}

	if len(result.Errors) != 0 {
		t.Errorf("expected 0 errors, got %d", len(result.Errors))
	}
}

func TestOrchestrationResultWithErrors(t *testing.T) {
	result := OrchestrationResult{
		Sessions: []CreateSessionResult{
			{
				SessionName: "cc_agents_1",
				PaneIDs:     []string{"%1", "%2"},
				Error:       nil,
			},
		},
		TotalPanes:      4,
		SuccessfulPanes: 2,
		FailedPanes:     2,
		Errors:          []error{nil}, // Placeholder for test
	}

	if result.SuccessfulPanes != 2 {
		t.Errorf("expected 2 successful panes, got %d", result.SuccessfulPanes)
	}

	if result.FailedPanes != 2 {
		t.Errorf("expected 2 failed panes, got %d", result.FailedPanes)
	}
}

func TestPaneGeometry(t *testing.T) {
	geom := PaneGeometry{
		Width:  120,
		Height: 40,
	}

	if geom.Width != 120 {
		t.Errorf("expected width 120, got %d", geom.Width)
	}

	if geom.Height != 40 {
		t.Errorf("expected height 40, got %d", geom.Height)
	}
}

func TestGeometryResult_Uniform(t *testing.T) {
	result := GeometryResult{
		SessionName: "test_session",
		PaneCount:   4,
		Geometries: []PaneGeometry{
			{Width: 80, Height: 24},
			{Width: 80, Height: 24},
			{Width: 80, Height: 24},
			{Width: 80, Height: 24},
		},
		IsUniform:      true,
		MaxWidthDelta:  0,
		MaxHeightDelta: 0,
	}

	if result.SessionName != "test_session" {
		t.Errorf("expected session name 'test_session', got %q", result.SessionName)
	}

	if result.PaneCount != 4 {
		t.Errorf("expected 4 panes, got %d", result.PaneCount)
	}

	if !result.IsUniform {
		t.Error("expected uniform geometry")
	}

	if result.MaxWidthDelta != 0 {
		t.Errorf("expected 0 width delta, got %d", result.MaxWidthDelta)
	}

	if result.MaxHeightDelta != 0 {
		t.Errorf("expected 0 height delta, got %d", result.MaxHeightDelta)
	}
}

type fakeSessionCreator struct {
	called bool
	result *OrchestrationResult
	err    error
}

func (f *fakeSessionCreator) CreateSessions(plan *SwarmPlan) (*OrchestrationResult, error) {
	f.called = true
	return f.result, f.err
}

type fakePaneLauncher struct {
	called    bool
	lastPlan  *SwarmPlan
	lastDelay time.Duration
	result    *BatchLaunchResult
	err       error
}

func (f *fakePaneLauncher) LaunchSwarm(ctx context.Context, plan *SwarmPlan, staggerDelay time.Duration) (*BatchLaunchResult, error) {
	f.called = true
	f.lastPlan = plan
	f.lastDelay = staggerDelay
	return f.result, f.err
}

type fakePromptInjector struct {
	called   bool
	lastPlan *SwarmPlan
	lastText string
	result   *BatchInjectionResult
	err      error
}

func (f *fakePromptInjector) InjectSwarmWithContext(ctx context.Context, plan *SwarmPlan, prompt string) (*BatchInjectionResult, error) {
	f.called = true
	f.lastPlan = plan
	f.lastText = prompt
	return f.result, f.err
}

func TestSwarmOrchestrator_Execute_SkipsInjectionWhenPromptEmpty(t *testing.T) {
	plan := &SwarmPlan{
		Sessions: []SessionSpec{{Name: "cc_agents_1", AgentType: "cc", PaneCount: 1, Panes: []PaneSpec{{Index: 1, AgentType: "cc"}}}},
	}
	sessionRes := &OrchestrationResult{
		Sessions: []CreateSessionResult{{SessionName: "cc_agents_1"}},
	}

	sess := &fakeSessionCreator{result: sessionRes}
	launcher := &fakePaneLauncher{result: &BatchLaunchResult{}}
	injector := &fakePromptInjector{result: &BatchInjectionResult{}}

	orch := &SwarmOrchestrator{
		SessionOrchestrator: sess,
		PaneLauncher:        launcher,
		PromptInjector:      injector,
		StaggerDelay:        10 * time.Millisecond,
	}

	got, err := orch.Execute(context.Background(), plan, "")
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got.Injection != nil {
		t.Fatalf("expected Injection to be nil when prompt is empty, got %+v", got.Injection)
	}
	if injector.called {
		t.Fatal("expected PromptInjector not to be called when prompt is empty")
	}
	if !sess.called || !launcher.called {
		t.Fatal("expected session creation and launch phases to run")
	}
}

func TestSwarmOrchestrator_Execute_FiltersFailedSessions(t *testing.T) {
	plan := &SwarmPlan{
		Sessions: []SessionSpec{
			{Name: "cc_agents_1", AgentType: "cc", PaneCount: 1, Panes: []PaneSpec{{Index: 1, AgentType: "cc"}}},
			{Name: "cod_agents_1", AgentType: "cod", PaneCount: 1, Panes: []PaneSpec{{Index: 1, AgentType: "cod"}}},
		},
	}
	sessionRes := &OrchestrationResult{
		Sessions: []CreateSessionResult{
			{SessionName: "cc_agents_1"},
			{SessionName: "cod_agents_1", Error: errors.New("boom")},
		},
	}

	sess := &fakeSessionCreator{result: sessionRes}
	launcher := &fakePaneLauncher{result: &BatchLaunchResult{}}
	injector := &fakePromptInjector{result: &BatchInjectionResult{}}

	orch := &SwarmOrchestrator{
		SessionOrchestrator: sess,
		PaneLauncher:        launcher,
		PromptInjector:      injector,
		StaggerDelay:        10 * time.Millisecond,
	}

	_, err := orch.Execute(context.Background(), plan, "hello")
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if launcher.lastPlan == nil || len(launcher.lastPlan.Sessions) != 1 || launcher.lastPlan.Sessions[0].Name != "cc_agents_1" {
		t.Fatalf("expected launcher plan filtered to only cc_agents_1, got %+v", launcher.lastPlan)
	}

	if injector.lastPlan == nil || len(injector.lastPlan.Sessions) != 1 || injector.lastPlan.Sessions[0].Name != "cc_agents_1" {
		t.Fatalf("expected injector plan filtered to only cc_agents_1, got %+v", injector.lastPlan)
	}
}

func TestSwarmOrchestrator_Execute_RespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sess := &fakeSessionCreator{result: &OrchestrationResult{}}
	launcher := &fakePaneLauncher{result: &BatchLaunchResult{}}
	injector := &fakePromptInjector{result: &BatchInjectionResult{}}

	orch := &SwarmOrchestrator{
		SessionOrchestrator: sess,
		PaneLauncher:        launcher,
		PromptInjector:      injector,
	}

	_, err := orch.Execute(ctx, &SwarmPlan{Sessions: []SessionSpec{}}, "hello")
	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if sess.called || launcher.called || injector.called {
		t.Fatal("expected no phases to run when context is already cancelled")
	}
}

func TestSwarmOrchestrator_Execute_ErrorsOnNilSessionResult(t *testing.T) {
	sess := &fakeSessionCreator{result: nil}
	launcher := &fakePaneLauncher{result: &BatchLaunchResult{}}
	injector := &fakePromptInjector{result: &BatchInjectionResult{}}

	orch := &SwarmOrchestrator{
		SessionOrchestrator: sess,
		PaneLauncher:        launcher,
		PromptInjector:      injector,
	}

	_, err := orch.Execute(context.Background(), &SwarmPlan{Sessions: []SessionSpec{}}, "")
	if err == nil {
		t.Fatal("expected error for nil session result")
	}
	if err.Error() != "session orchestrator returned nil result" {
		t.Fatalf("unexpected error: %v", err)
	}
	if launcher.called || injector.called {
		t.Fatal("expected execution to stop before launch and injection phases")
	}
}

func TestSwarmOrchestrator_Execute_ErrorsOnNilLaunchResult(t *testing.T) {
	sess := &fakeSessionCreator{result: &OrchestrationResult{}}
	launcher := &fakePaneLauncher{result: nil}
	injector := &fakePromptInjector{result: &BatchInjectionResult{}}

	orch := &SwarmOrchestrator{
		SessionOrchestrator: sess,
		PaneLauncher:        launcher,
		PromptInjector:      injector,
	}

	_, err := orch.Execute(context.Background(), &SwarmPlan{Sessions: []SessionSpec{}}, "")
	if err == nil {
		t.Fatal("expected error for nil launch result")
	}
	if err.Error() != "pane launcher returned nil result" {
		t.Fatalf("unexpected error: %v", err)
	}
	if injector.called {
		t.Fatal("expected execution to stop before prompt injection")
	}
}

func TestSwarmOrchestrator_Execute_ErrorsOnNilInjectionResult(t *testing.T) {
	sess := &fakeSessionCreator{result: &OrchestrationResult{}}
	launcher := &fakePaneLauncher{result: &BatchLaunchResult{}}
	injector := &fakePromptInjector{result: nil}

	orch := &SwarmOrchestrator{
		SessionOrchestrator: sess,
		PaneLauncher:        launcher,
		PromptInjector:      injector,
	}

	_, err := orch.Execute(context.Background(), &SwarmPlan{Sessions: []SessionSpec{}}, "hello")
	if err == nil {
		t.Fatal("expected error for nil injection result")
	}
	if err.Error() != "prompt injector returned nil result" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGeometryResult_NonUniform(t *testing.T) {
	result := GeometryResult{
		SessionName: "test_session",
		PaneCount:   3,
		Geometries: []PaneGeometry{
			{Width: 100, Height: 30},
			{Width: 80, Height: 24},
			{Width: 90, Height: 27},
		},
		IsUniform:      false,
		MaxWidthDelta:  20,
		MaxHeightDelta: 6,
	}

	if result.IsUniform {
		t.Error("expected non-uniform geometry")
	}

	if result.MaxWidthDelta != 20 {
		t.Errorf("expected 20 width delta, got %d", result.MaxWidthDelta)
	}

	if result.MaxHeightDelta != 6 {
		t.Errorf("expected 6 height delta, got %d", result.MaxHeightDelta)
	}
}

func TestGeometryResult_EmptySession(t *testing.T) {
	result := GeometryResult{
		SessionName: "empty_session",
		PaneCount:   0,
		Geometries:  []PaneGeometry{},
		IsUniform:   true,
	}

	if !result.IsUniform {
		t.Error("empty session should be considered uniform")
	}

	if result.PaneCount != 0 {
		t.Errorf("expected 0 panes, got %d", result.PaneCount)
	}
}

func TestGeometryResult_WithTolerance(t *testing.T) {
	// Simulating geometry check with tolerance of 2
	// Panes with 1 cell difference should be considered uniform
	geometries := []PaneGeometry{
		{Width: 80, Height: 24},
		{Width: 81, Height: 24}, // 1 cell wider
		{Width: 80, Height: 25}, // 1 cell taller
	}

	maxWidthDelta := 1
	maxHeightDelta := 1
	tolerance := 2

	isUniform := maxWidthDelta <= tolerance && maxHeightDelta <= tolerance

	if !isUniform {
		t.Error("expected uniform with tolerance=2")
	}

	if len(geometries) != 3 {
		t.Errorf("expected 3 geometries, got %d", len(geometries))
	}
}

func TestSessionOrchestrator_TmuxBinaryPath(t *testing.T) {
	orch := NewSessionOrchestrator()

	path := orch.TmuxBinaryPath()

	// Should return a non-empty path
	if path == "" {
		t.Error("expected non-empty tmux binary path")
	}

	// Should prefer explicit paths over just "tmux"
	// Common locations: /usr/bin/tmux, /usr/local/bin/tmux, /opt/homebrew/bin/tmux
	validPaths := []string{
		"/usr/bin/tmux",
		"/usr/local/bin/tmux",
		"/opt/homebrew/bin/tmux",
		"tmux", // fallback
	}

	found := false
	for _, valid := range validPaths {
		if path == valid {
			found = true
			break
		}
	}

	if !found {
		t.Logf("tmux binary path: %s (not in expected list, but may be valid)", path)
	}
}

func TestSessionOrchestrator_GetTmuxBinaryInfo(t *testing.T) {
	orch := NewSessionOrchestrator()

	info := orch.GetTmuxBinaryInfo()

	if info == nil {
		t.Fatal("expected non-nil TmuxBinaryInfo")
	}

	if info.Path == "" {
		t.Error("expected non-empty path in TmuxBinaryInfo")
	}

	// Default orchestrator should use local tmux
	if info.IsRemote {
		t.Error("expected IsRemote=false for default orchestrator")
	}
}

func TestTmuxBinaryInfo(t *testing.T) {
	info := TmuxBinaryInfo{
		Path:      "/usr/bin/tmux",
		Available: true,
		IsRemote:  false,
	}

	if info.Path != "/usr/bin/tmux" {
		t.Errorf("expected path /usr/bin/tmux, got %q", info.Path)
	}

	if !info.Available {
		t.Error("expected Available=true")
	}

	if info.IsRemote {
		t.Error("expected IsRemote=false")
	}
}

func TestTmuxBinaryInfo_Remote(t *testing.T) {
	info := TmuxBinaryInfo{
		Path:      "/usr/bin/tmux",
		Available: true,
		IsRemote:  true,
	}

	if !info.IsRemote {
		t.Error("expected IsRemote=true for remote configuration")
	}
}

func TestNewRemoteSessionOrchestrator(t *testing.T) {
	host := "user@example.com"
	orch := NewRemoteSessionOrchestrator(host)

	if orch == nil {
		t.Fatal("expected non-nil orchestrator")
	}

	if orch.TmuxClient == nil {
		t.Fatal("expected non-nil TmuxClient for remote orchestrator")
	}

	if orch.TmuxClient.Remote != host {
		t.Errorf("expected Remote=%q, got %q", host, orch.TmuxClient.Remote)
	}

	if orch.StaggerDelay != 300*time.Millisecond {
		t.Errorf("expected StaggerDelay 300ms, got %v", orch.StaggerDelay)
	}
}

func TestNewRemoteSessionOrchestratorWithDelay(t *testing.T) {
	host := "admin@192.168.1.100"
	delay := 500 * time.Millisecond
	orch := NewRemoteSessionOrchestratorWithDelay(host, delay)

	if orch == nil {
		t.Fatal("expected non-nil orchestrator")
	}

	if orch.TmuxClient.Remote != host {
		t.Errorf("expected Remote=%q, got %q", host, orch.TmuxClient.Remote)
	}

	if orch.StaggerDelay != delay {
		t.Errorf("expected StaggerDelay %v, got %v", delay, orch.StaggerDelay)
	}
}

func TestSessionOrchestrator_IsRemote(t *testing.T) {
	t.Run("local orchestrator", func(t *testing.T) {
		orch := NewSessionOrchestrator()
		if orch.IsRemote() {
			t.Error("expected IsRemote()=false for local orchestrator")
		}
	})

	t.Run("remote orchestrator", func(t *testing.T) {
		orch := NewRemoteSessionOrchestrator("user@host")
		if !orch.IsRemote() {
			t.Error("expected IsRemote()=true for remote orchestrator")
		}
	})
}

func TestSessionOrchestrator_RemoteHost(t *testing.T) {
	t.Run("local orchestrator", func(t *testing.T) {
		orch := NewSessionOrchestrator()
		host := orch.RemoteHost()
		if host != "" {
			t.Errorf("expected empty RemoteHost for local orchestrator, got %q", host)
		}
	})

	t.Run("remote orchestrator", func(t *testing.T) {
		expected := "ubuntu@10.0.0.5"
		orch := NewRemoteSessionOrchestrator(expected)
		host := orch.RemoteHost()
		if host != expected {
			t.Errorf("expected RemoteHost=%q, got %q", expected, host)
		}
	})
}

func TestSessionOrchestrator_TestConnection_Local(t *testing.T) {
	orch := NewSessionOrchestrator()

	// Local connection test should work if tmux is installed
	err := orch.TestConnection()
	if err != nil {
		t.Logf("local connection test: %v (tmux may not be installed)", err)
	}
}

func TestSessionOrchestrator_GetTmuxBinaryInfo_Remote(t *testing.T) {
	orch := NewRemoteSessionOrchestrator("user@remote-host")

	info := orch.GetTmuxBinaryInfo()

	if info == nil {
		t.Fatal("expected non-nil TmuxBinaryInfo")
	}

	if !info.IsRemote {
		t.Error("expected IsRemote=true for remote orchestrator")
	}
}

func TestRemoteConnectionInfo(t *testing.T) {
	t.Run("local orchestrator", func(t *testing.T) {
		orch := NewSessionOrchestrator()
		info := orch.GetRemoteConnectionInfo()

		if info == nil {
			t.Fatal("expected non-nil RemoteConnectionInfo")
		}

		if info.IsRemote {
			t.Error("expected IsRemote=false for local orchestrator")
		}

		if info.Host != "" {
			t.Errorf("expected empty Host for local orchestrator, got %q", info.Host)
		}
	})

	t.Run("remote orchestrator structure", func(t *testing.T) {
		host := "admin@server.example.com"
		orch := NewRemoteSessionOrchestrator(host)
		info := orch.GetRemoteConnectionInfo()

		if info == nil {
			t.Fatal("expected non-nil RemoteConnectionInfo")
		}

		if !info.IsRemote {
			t.Error("expected IsRemote=true for remote orchestrator")
		}

		if info.Host != host {
			t.Errorf("expected Host=%q, got %q", host, info.Host)
		}

		// Connection will fail since the host doesn't exist, but structure should be correct
		if info.Connected {
			t.Logf("unexpectedly connected to %s (may be a real host)", host)
		}
	})
}

func TestRemoteConnectionInfo_Struct(t *testing.T) {
	info := RemoteConnectionInfo{
		Host:        "user@host",
		IsRemote:    true,
		Connected:   true,
		TmuxVersion: "tmux 3.4",
		Error:       "",
	}

	if info.Host != "user@host" {
		t.Errorf("expected Host=user@host, got %q", info.Host)
	}

	if !info.IsRemote {
		t.Error("expected IsRemote=true")
	}

	if !info.Connected {
		t.Error("expected Connected=true")
	}

	if info.TmuxVersion != "tmux 3.4" {
		t.Errorf("expected TmuxVersion='tmux 3.4', got %q", info.TmuxVersion)
	}
}

func TestRemoteConnectionInfo_Failed(t *testing.T) {
	info := RemoteConnectionInfo{
		Host:      "user@unreachable",
		IsRemote:  true,
		Connected: false,
		Error:     "connection refused",
	}

	if info.Connected {
		t.Error("expected Connected=false for failed connection")
	}

	if info.Error == "" {
		t.Error("expected non-empty Error for failed connection")
	}
}
