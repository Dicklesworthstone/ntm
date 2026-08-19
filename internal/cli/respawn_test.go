package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestNormalizeAgentType(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"cc", "claude"},
		{"CC", "claude"},
		{"claude", "claude"},
		{"Claude", "claude"},
		{"claude_code", "claude"},
		{"claude-code", "claude"},
		{"cod", "codex"},
		{"codex", "codex"},
		{"openai-codex", "codex"},
		{"codex-cli", "codex"},
		{"codex_cli", "codex"},
		{"gmi", "gemini"},
		{"gemini", "gemini"},
		{"google-gemini", "gemini"},
		{"gemini-cli", "gemini"},
		{"gemini_cli", "gemini"},
		{"grok-build", "grok"},
		{"unknown", "unknown"},
		{"aider", "aider"},
		{"  cc  ", "claude"}, // whitespace handling
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeAgentType(tt.input)
			if got != tt.expected {
				t.Errorf("normalizeAgentType(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestRespawnHelpIncludesGrokTypeFilter(t *testing.T) {
	flag := newRespawnCmd().Flags().Lookup("type")
	if flag == nil {
		t.Fatal("respawn --type flag is not registered")
	}
	if !strings.Contains(flag.Usage, "grok") || !strings.Contains(flag.Usage, "grok-build") {
		t.Fatalf("respawn --type help = %q, want Grok aliases", flag.Usage)
	}
}

func TestRespawnCommandPropagatesCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cmd := newRespawnCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"ignored", "--force"})

	if err := cmd.Execute(); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled respawn command error=%v, want context.Canceled", err)
	}
}

func TestRespawnPaneAgentTypePrefersParsedPaneType(t *testing.T) {

	pane := tmux.Pane{
		Title:   "custom status title",
		Type:    tmux.AgentCodex,
		Command: "codex --unsafe-mode",
	}

	if got := respawnPaneAgentType(pane); got != "codex" {
		t.Fatalf("respawnPaneAgentType() = %q, want %q", got, "codex")
	}
}

func TestSelectRespawnTargetsUsesParsedPaneTypeForFilters(t *testing.T) {

	panes := []tmux.Pane{
		{ID: "%0", Index: 0, Title: "shell", Type: tmux.AgentUser, Command: "zsh"},
		{ID: "%1", Index: 1, Title: "build monitor", Type: tmux.AgentClaude, Command: "claude"},
		{ID: "%2", Index: 2, Title: "notes", Type: tmux.AgentCodex, Command: "codex"},
	}

	targets := selectRespawnTargets(panes, nil, "codex", false)
	if len(targets) != 1 {
		t.Fatalf("selectRespawnTargets() returned %d panes, want 1", len(targets))
	}
	if targets[0].ID != "%2" {
		t.Fatalf("selectRespawnTargets() picked %s, want %%2", targets[0].ID)
	}
}

func TestSelectRespawnTargetsSkipsUserPaneByDefault(t *testing.T) {

	panes := []tmux.Pane{
		{ID: "%0", Index: 0, Title: "shell", Type: tmux.AgentUser, Command: "zsh"},
		{ID: "%1", Index: 1, Title: "agent output", Type: tmux.AgentClaude, Command: "claude"},
	}

	targets := selectRespawnTargets(panes, nil, "", false)
	if len(targets) != 1 {
		t.Fatalf("selectRespawnTargets() returned %d panes, want 1", len(targets))
	}
	if targets[0].ID != "%1" {
		t.Fatalf("selectRespawnTargets() picked %s, want %%1", targets[0].ID)
	}
}

// GH#251 phase 2: Grok Build relaunch is implemented, so mixed batches with
// grok panes validate like claude/codex batches.
func TestValidateRespawnTargetsAcceptsGrokInMixedBatch(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%1", Index: 1, Type: tmux.AgentClaude},
		{ID: "%2", Index: 2, Type: tmux.AgentGrok},
	}
	if err := validateRespawnTargets(panes); err != nil {
		t.Fatalf("validateRespawnTargets() error = %v, want nil", err)
	}
	if err := validateRespawnTargets(panes[:1]); err != nil {
		t.Fatalf("supported target unexpectedly rejected: %v", err)
	}
}

func TestRespawnResultErrorRejectsTerminalRobotResponse(t *testing.T) {
	out := &robot.RestartPaneOutput{
		RobotResponse: robot.NewErrorResponse(
			agent.ErrAutomatedRelaunchNotImplemented,
			robot.ErrCodeNotImplemented,
			agent.GrokPhaseOneCapabilityHint,
		),
	}
	err := respawnResultError(out)
	if err == nil || !strings.Contains(err.Error(), robot.ErrCodeNotImplemented) {
		t.Fatalf("respawnResultError() error = %v, want typed terminal failure", err)
	}
	if err := respawnResultError(&robot.RestartPaneOutput{RobotResponse: robot.NewRobotResponse(true)}); err != nil {
		t.Fatalf("successful response rejected: %v", err)
	}
}

func TestRespawnRelaunchDisplayStatusUsesTriStateBeforeLegacyBoolean(t *testing.T) {
	tests := []struct {
		name      string
		status    robot.RestartAgentRelaunchStatus
		alive     bool
		legacy    bool
		want      string
		wantExact bool
	}{
		{name: "ready", status: robot.RestartAgentRelaunchReady, want: "agent relaunched and ready", wantExact: true},
		{name: "not ready with child", status: robot.RestartAgentRelaunchNotReady, alive: true, want: "alive but not ready"},
		{name: "unknown with child", status: robot.RestartAgentRelaunchUnknown, alive: true, want: "UNKNOWN"},
		{name: "failed", status: robot.RestartAgentRelaunchFailed, want: "agent relaunch FAILED", wantExact: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := &robot.RestartPaneOutput{
				AgentRelaunched:     map[string]bool{"1": tt.legacy},
				AgentRelaunchStatus: map[string]robot.RestartAgentRelaunchStatus{"1": tt.status},
				ProcessAlive:        map[string]bool{"1": tt.alive},
			}
			got, ok := respawnRelaunchDisplayStatus(out, "1")
			if !ok || (tt.wantExact && got != tt.want) || (!tt.wantExact && !strings.Contains(got, tt.want)) {
				t.Fatalf("display status=%q ok=%t, want %q", got, ok, tt.want)
			}
			if tt.status == robot.RestartAgentRelaunchUnknown && strings.Contains(got, "pane left at a shell") {
				t.Fatalf("unknown live-child status used legacy false rendering: %q", got)
			}
		})
	}

	legacy, ok := respawnRelaunchDisplayStatus(&robot.RestartPaneOutput{
		AgentRelaunched: map[string]bool{"1": false},
	}, "1")
	if !ok || !strings.Contains(legacy, "pane left at a shell") {
		t.Fatalf("legacy fallback=%q ok=%t", legacy, ok)
	}
}

func TestRespawnRequiresSession(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	// Test respawning a non-existent session should fail
	err := runRespawn(t.Context(), "nonexistent-session-12345", true, "", "", false, false)
	if err == nil {
		t.Error("expected error for non-existent session")
	}
}

func TestRespawnDryRun(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	// Setup temp dir
	tmpDir, err := os.MkdirTemp("", "ntm-test-respawn")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Save and restore global config
	oldCfg := cfg
	oldJsonOutput := jsonOutput
	defer func() {
		cfg = oldCfg
		jsonOutput = oldJsonOutput
	}()

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Agents.Claude = "printf ready; sleep 300"

	// Create unique session
	sessionName := fmt.Sprintf("ntm-test-respawn-%d", time.Now().UnixNano())

	// Pre-create project directory
	projectDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// Spawn a test session
	agents := []FlatAgent{
		{Type: AgentTypeClaude, Index: 1},
	}
	opts := SpawnOptions{
		Session:  sessionName,
		Agents:   agents,
		CCCount:  1,
		UserPane: true,
	}

	err = spawnSessionLogicContext(t.Context(), opts)
	if err != nil {
		t.Fatalf("spawnSessionLogic failed: %v", err)
	}

	// Clean up session after test
	defer func() {
		_ = tmux.KillSession(sessionName)
	}()

	// Wait for session to be ready
	time.Sleep(500 * time.Millisecond)

	// Test dry-run mode (should not error and not actually restart)
	err = runRespawn(t.Context(), sessionName, true, "", "", false, true)
	if err != nil {
		t.Errorf("dry-run respawn failed: %v", err)
	}
}

func TestRespawnWithPaneFilter(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	// Setup temp dir
	tmpDir, err := os.MkdirTemp("", "ntm-test-respawn-filter")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Save and restore global config
	oldCfg := cfg
	oldJsonOutput := jsonOutput
	defer func() {
		cfg = oldCfg
		jsonOutput = oldJsonOutput
	}()

	cfg = newTmuxIntegrationTestConfig(tmpDir)
	cfg.Agents.Claude = "printf ready; sleep 300"

	// Create unique session
	sessionName := fmt.Sprintf("ntm-test-respawn-filter-%d", time.Now().UnixNano())

	// Pre-create project directory
	projectDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project dir: %v", err)
	}

	// Spawn a test session with 2 agents
	agents := []FlatAgent{
		{Type: AgentTypeClaude, Index: 1},
		{Type: AgentTypeClaude, Index: 2},
	}
	opts := SpawnOptions{
		Session:  sessionName,
		Agents:   agents,
		CCCount:  2,
		UserPane: true,
	}

	err = spawnSessionLogicContext(t.Context(), opts)
	if err != nil {
		t.Fatalf("spawnSessionLogic failed: %v", err)
	}

	// Clean up session after test
	defer func() {
		_ = tmux.KillSession(sessionName)
	}()

	// Wait for session to be ready
	time.Sleep(500 * time.Millisecond)

	// Test respawning specific pane with force flag
	err = runRespawn(t.Context(), sessionName, true, "1", "", false, false)
	if err != nil {
		t.Errorf("respawn with pane filter failed: %v", err)
	}
}
