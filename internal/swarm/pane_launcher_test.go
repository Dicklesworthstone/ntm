package swarm

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestNewPaneLauncher(t *testing.T) {
	launcher := NewPaneLauncher()

	if launcher == nil {
		t.Fatal("NewPaneLauncher returned nil")
	}

	if launcher.TmuxClient != nil {
		t.Error("expected TmuxClient to be nil for default client")
	}

	if launcher.CmdBuilder != nil {
		t.Error("expected CmdBuilder to be nil for default builder")
	}

	if launcher.CDDelay != 100*time.Millisecond {
		t.Errorf("expected CDDelay of 100ms, got %v", launcher.CDDelay)
	}

	if !launcher.ValidatePaths {
		t.Error("expected ValidatePaths to be true by default")
	}

	if launcher.Logger == nil {
		t.Error("expected non-nil Logger")
	}
}

func TestPaneLauncherChaining(t *testing.T) {
	launcher := NewPaneLauncher()

	// Test WithCDDelay
	result := launcher.WithCDDelay(200 * time.Millisecond)
	if result != launcher {
		t.Error("WithCDDelay should return the same launcher for chaining")
	}
	if launcher.CDDelay != 200*time.Millisecond {
		t.Errorf("expected CDDelay of 200ms, got %v", launcher.CDDelay)
	}

	// Test WithValidatePaths
	result = launcher.WithValidatePaths(false)
	if result != launcher {
		t.Error("WithValidatePaths should return the same launcher for chaining")
	}
	if launcher.ValidatePaths {
		t.Error("expected ValidatePaths to be false")
	}

	// Test WithLogger
	result = launcher.WithLogger(nil)
	if result != launcher {
		t.Error("WithLogger should return the same launcher for chaining")
	}

	// Test WithCmdBuilder
	builder := NewLaunchCommandBuilder()
	result = launcher.WithCmdBuilder(builder)
	if result != launcher {
		t.Error("WithCmdBuilder should return the same launcher for chaining")
	}
	if launcher.CmdBuilder != builder {
		t.Error("expected CmdBuilder to be set")
	}
}

func TestPaneLauncherTmuxClientHelper(t *testing.T) {
	launcher := NewPaneLauncher()
	client := launcher.tmuxClient()

	if client == nil {
		t.Error("expected non-nil client from tmuxClient()")
	}
}

func TestPaneLauncherCmdBuilderHelper(t *testing.T) {
	launcher := NewPaneLauncher()
	builder := launcher.cmdBuilder()

	if builder == nil {
		t.Error("expected non-nil builder from cmdBuilder()")
	}
}

func TestPaneLauncherLoggerHelper(t *testing.T) {
	launcher := NewPaneLauncher()
	logger := launcher.logger()

	if logger == nil {
		t.Error("expected non-nil logger from logger()")
	}
}

func TestPaneLaunchResult(t *testing.T) {
	result := PaneLaunchResult{
		SessionName: "test-session",
		PaneIndex:   1,
		PaneTarget:  "test-session:1.1",
		AgentType:   "cc",
		Project:     "/projects/foo",
		Command:     "cc --dangerously-skip-permissions",
		Success:     true,
		Duration:    100 * time.Millisecond,
	}

	if result.SessionName != "test-session" {
		t.Errorf("unexpected SessionName: %s", result.SessionName)
	}
	if result.PaneIndex != 1 {
		t.Errorf("unexpected PaneIndex: %d", result.PaneIndex)
	}
	if result.PaneTarget != "test-session:1.1" {
		t.Errorf("unexpected PaneTarget: %s", result.PaneTarget)
	}
	if !result.Success {
		t.Error("expected Success to be true")
	}
	if result.Error != "" {
		t.Errorf("expected empty Error, got %q", result.Error)
	}
}

func TestBatchLaunchResult(t *testing.T) {
	result := BatchLaunchResult{
		TotalPanes: 3,
		Successful: 2,
		Failed:     1,
		Results: []PaneLaunchResult{
			{SessionName: "s", PaneIndex: 1, Success: true},
			{SessionName: "s", PaneIndex: 2, Success: true},
			{SessionName: "s", PaneIndex: 3, Success: false, Error: "test error"},
		},
		Duration: 500 * time.Millisecond,
	}

	if result.TotalPanes != 3 {
		t.Errorf("expected TotalPanes of 3, got %d", result.TotalPanes)
	}
	if result.Successful != 2 {
		t.Errorf("expected Successful of 2, got %d", result.Successful)
	}
	if result.Failed != 1 {
		t.Errorf("expected Failed of 1, got %d", result.Failed)
	}
	if len(result.Results) != 3 {
		t.Errorf("expected 3 results, got %d", len(result.Results))
	}
}

func TestGetPaneTarget(t *testing.T) {
	tests := []struct {
		session  string
		window   int
		pane     int
		expected string
	}{
		{"test", 1, 1, "test:1.1"},
		{"my-session", 1, 5, "my-session:1.5"},
		{"cc_agents_1", 1, 10, "cc_agents_1:1.10"},
		{"test", 0, 1, "test:0.1"}, // Window 0 case
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			// Test the pure formatting helper since GetPaneTarget now queries tmux
			// for the actual window index and may fail without a live session.
			result := tmux.FormatPaneTargetWithWindow(tt.session, tt.window, tt.pane)
			if result != tt.expected {
				t.Errorf("FormatPaneTargetWithWindow(%q, %d, %d) = %q, want %q",
					tt.session, tt.window, tt.pane, result, tt.expected)
			}
		})
	}
}

func TestValidateProjectPath(t *testing.T) {
	// Create a temp directory for testing
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "testfile")
	if err := os.WriteFile(tmpFile, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{
			name:    "empty path",
			path:    "",
			wantErr: false,
		},
		{
			name:    "existing directory",
			path:    tmpDir,
			wantErr: false,
		},
		{
			name:    "non-existent path",
			path:    "/nonexistent/path/12345",
			wantErr: true,
		},
		{
			name:    "path is file not directory",
			path:    tmpFile,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateProjectPath(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateProjectPath(%q) error = %v, wantErr %v",
					tt.path, err, tt.wantErr)
			}
		})
	}
}

func TestPaneLauncherLaunchSwarmNilPlan(t *testing.T) {
	launcher := NewPaneLauncher()
	result, err := launcher.LaunchSwarm(context.Background(), nil, 0)

	if err == nil {
		t.Error("expected error for nil plan")
	}
	if result != nil {
		t.Error("expected nil result for nil plan")
	}
}

func TestPaneLauncherLaunchSwarmEmptyPlan(t *testing.T) {
	launcher := NewPaneLauncher().WithValidatePaths(false)
	plan := &SwarmPlan{
		Sessions:    []SessionSpec{},
		TotalAgents: 0,
	}

	result, err := launcher.LaunchSwarm(context.Background(), plan, 0)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.TotalPanes != 0 {
		t.Errorf("expected TotalPanes of 0, got %d", result.TotalPanes)
	}
	if result.Successful != 0 {
		t.Errorf("expected Successful of 0, got %d", result.Successful)
	}
}

func TestLaunchAgentInPaneContextCancellation(t *testing.T) {
	launcher := NewPaneLauncher().WithValidatePaths(false)

	// Create a cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	paneSpec := PaneSpec{
		Index:     1,
		AgentType: "cc",
		Project:   "/tmp",
	}

	result, err := launcher.LaunchAgentInPane(ctx, "test", paneSpec)

	if err == nil {
		t.Error("expected error for cancelled context")
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Success {
		t.Error("expected Success to be false")
	}
}

func TestLaunchAgentInPaneInvalidPath(t *testing.T) {
	launcher := NewPaneLauncher().WithValidatePaths(true)

	paneSpec := PaneSpec{
		Index:     1,
		AgentType: "cc",
		Project:   "/nonexistent/path/12345",
	}

	result, err := launcher.LaunchAgentInPane(context.Background(), "test", paneSpec)

	if err == nil {
		t.Error("expected error for invalid path")
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Success {
		t.Error("expected Success to be false")
	}
	if result.Error == "" {
		t.Error("expected non-empty Error")
	}
}

func TestLaunchSessionEmptyPanes(t *testing.T) {
	launcher := NewPaneLauncher().WithValidatePaths(false)

	sessionSpec := SessionSpec{
		Name:      "test",
		AgentType: "cc",
		PaneCount: 0,
		Panes:     []PaneSpec{},
	}

	result, err := launcher.LaunchSession(context.Background(), sessionSpec, 0)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.TotalPanes != 0 {
		t.Errorf("expected TotalPanes of 0, got %d", result.TotalPanes)
	}
}

func TestLaunchSessionContextCancellation(t *testing.T) {
	launcher := NewPaneLauncher().WithValidatePaths(false)

	// Create a context that cancels after a short time
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	sessionSpec := SessionSpec{
		Name:      "test",
		AgentType: "cc",
		PaneCount: 2,
		Panes: []PaneSpec{
			{Index: 1, AgentType: "cc", Project: "/tmp"},
			{Index: 2, AgentType: "cc", Project: "/tmp"},
		},
	}

	result, err := launcher.LaunchSession(ctx, sessionSpec, 100*time.Millisecond)

	// Should return with context error
	if err == nil {
		t.Error("expected error for cancelled context")
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestNewPaneLauncherWithClient(t *testing.T) {
	launcher := NewPaneLauncherWithClient(nil)

	if launcher == nil {
		t.Fatal("NewPaneLauncherWithClient returned nil")
	}

	if launcher.CDDelay != 100*time.Millisecond {
		t.Errorf("expected CDDelay of 100ms, got %v", launcher.CDDelay)
	}

	if !launcher.ValidatePaths {
		t.Error("expected ValidatePaths to be true by default")
	}
}

func TestIsCodexProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		agentType string
		want      bool
	}{
		{"cod", "cod", true},
		{"codex", "codex", true},
		{"openai", "openai", true},
		{"gpt", "gpt", true},
		{"claude", "claude", false},
		{"cc", "cc", false},
		{"gemini", "gemini", false},
		{"gmi", "gmi", false},
		{"empty", "", false},
		{"unknown", "unknown", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isCodexProvider(tc.agentType); got != tc.want {
				t.Errorf("isCodexProvider(%q) = %v, want %v", tc.agentType, got, tc.want)
			}
		})
	}
}
