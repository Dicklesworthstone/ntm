package swarm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// MockTmuxClient is a mock implementation for testing AgentLauncher.
// It records all operations for verification without executing real tmux commands.
type MockTmuxClient struct {
	t  *testing.T
	mu sync.Mutex

	// Recorded operations
	SendKeysCalls   []SendKeysCall
	GetPanesCalls   []string // session names
	CaptureSequence []string // sequence of captured pane outputs for WaitForReady tests

	// Return values
	Panes   []tmux.Pane
	PaneErr error
	SendErr error

	// State tracking
	captureIndex int
	lastCommand  string
}

// SendKeysCall records a call to SendKeys
type SendKeysCall struct {
	Target string
	Keys   string
	Enter  bool
}

// SendKeys records the call and optionally returns an error
func (m *MockTmuxClient) SendKeys(target, keys string, enter bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	call := SendKeysCall{Target: target, Keys: keys, Enter: enter}
	m.SendKeysCalls = append(m.SendKeysCalls, call)
	m.lastCommand = keys

	if m.t != nil {
		m.t.Logf("[TEST] MockTmuxClient.SendKeys: target=%s keys=%q enter=%v", target, keys, enter)
	}

	return m.SendErr
}

// GetPanes returns mock panes
func (m *MockTmuxClient) GetPanes(session string) ([]tmux.Pane, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.GetPanesCalls = append(m.GetPanesCalls, session)

	if m.t != nil {
		m.t.Logf("[TEST] MockTmuxClient.GetPanes: session=%s pane_count=%d", session, len(m.Panes))
	}

	return m.Panes, m.PaneErr
}

// CapturePaneOutput returns the next item from CaptureSequence
func (m *MockTmuxClient) CapturePaneOutput(target string, lines int) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.captureIndex < len(m.CaptureSequence) {
		output := m.CaptureSequence[m.captureIndex]
		m.captureIndex++
		if m.t != nil {
			m.t.Logf("[TEST] MockTmuxClient.CapturePaneOutput: target=%s output=%q", target, output)
		}
		return output, nil
	}
	return "", nil
}

// LastCommand returns the last command sent via SendKeys
func (m *MockTmuxClient) LastCommand() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastCommand
}

// Reset clears all recorded calls
func (m *MockTmuxClient) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SendKeysCalls = nil
	m.GetPanesCalls = nil
	m.captureIndex = 0
	m.lastCommand = ""
}

func TestNewAgentLauncher(t *testing.T) {
	t.Log("[TEST] TestNewAgentLauncher: creating launcher with default settings")
	launcher := NewAgentLauncher()

	if launcher == nil {
		t.Fatal("[TEST] FAIL: NewAgentLauncher returned nil")
	}
	t.Log("[TEST] PASS: launcher is not nil")

	if launcher.TmuxClient != nil {
		t.Error("[TEST] FAIL: expected TmuxClient to be nil for default client")
	}
	t.Log("[TEST] PASS: TmuxClient is nil (uses default)")

	if launcher.LaunchDelay != 200*time.Millisecond {
		t.Errorf("[TEST] FAIL: expected LaunchDelay of 200ms, got %v", launcher.LaunchDelay)
	}
	t.Logf("[TEST] PASS: LaunchDelay = %v", launcher.LaunchDelay)

	if launcher.PostLaunchDelay != 50*time.Millisecond {
		t.Errorf("[TEST] FAIL: expected PostLaunchDelay of 50ms, got %v", launcher.PostLaunchDelay)
	}
	t.Logf("[TEST] PASS: PostLaunchDelay = %v", launcher.PostLaunchDelay)
}

func TestFormatPaneTarget(t *testing.T) {
	tests := []struct {
		session  string
		pane     int
		expected string
	}{
		{"myproject", 1, "myproject:1.1"},
		{"cc_agents_1", 5, "cc_agents_1:1.5"},
		{"test-session", 10, "test-session:1.10"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := formatPaneTarget(tt.session, tt.pane)
			if result != tt.expected {
				t.Errorf("formatPaneTarget(%q, %d) = %q, want %q",
					tt.session, tt.pane, result, tt.expected)
			}
		})
	}
}

type fakeTmuxRunner struct {
	windowsOutput string
	panesOutput   map[string]string
	errByCommand  map[string]error
}

func (f *fakeTmuxRunner) RunContext(ctx context.Context, args ...string) (string, error) {
	if ctx != nil && ctx.Err() != nil {
		return "", ctx.Err()
	}
	if len(args) == 0 {
		return "", errors.New("no tmux args")
	}

	cmd := args[0]
	if err := f.errByCommand[cmd]; err != nil {
		return "", err
	}

	switch cmd {
	case "list-windows":
		return f.windowsOutput, nil
	case "list-panes":
		target, ok := findTmuxFlagValue(args, "-t")
		if !ok {
			return "", errors.New("missing -t")
		}
		if f.panesOutput == nil {
			return "", errors.New("no panes output configured")
		}
		out, ok := f.panesOutput[target]
		if !ok {
			return "", fmt.Errorf("no panes output configured for target %q", target)
		}
		return out, nil
	default:
		return "", fmt.Errorf("unexpected tmux command %q", cmd)
	}
}

func findTmuxFlagValue(args []string, flag string) (string, bool) {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			return args[i+1], true
		}
	}
	return "", false
}

func TestResolveSwarmSessionTargeting_BaseIndexZero(t *testing.T) {
	t.Parallel()

	runner := &fakeTmuxRunner{
		windowsOutput: "0\n",
		panesOutput: map[string]string{
			"test:0": "0\n1\n2\n",
		},
		errByCommand: map[string]error{},
	}

	targeting, err := resolveSwarmSessionTargeting(context.Background(), runner, "test")
	if err != nil {
		t.Fatalf("resolveSwarmSessionTargeting returned error: %v", err)
	}
	if targeting.WindowIndex != 0 {
		t.Fatalf("WindowIndex = %d, want %d", targeting.WindowIndex, 0)
	}
	if targeting.BasePaneIndex != 0 {
		t.Fatalf("BasePaneIndex = %d, want %d", targeting.BasePaneIndex, 0)
	}
}

func TestResolveSwarmSessionTargeting_PrefersWindowOne(t *testing.T) {
	t.Parallel()

	runner := &fakeTmuxRunner{
		windowsOutput: "0\n1\n2\n",
		panesOutput: map[string]string{
			"test:1": "0\n1\n",
		},
		errByCommand: map[string]error{},
	}

	targeting, err := resolveSwarmSessionTargeting(context.Background(), runner, "test")
	if err != nil {
		t.Fatalf("resolveSwarmSessionTargeting returned error: %v", err)
	}
	if targeting.WindowIndex != 1 {
		t.Fatalf("WindowIndex = %d, want %d", targeting.WindowIndex, 1)
	}
}

func TestResolveSwarmSessionTargeting_BaseIndexOne(t *testing.T) {
	t.Parallel()

	runner := &fakeTmuxRunner{
		windowsOutput: "2\n1\n", // unsorted; resolver should pick min
		panesOutput: map[string]string{
			"test:1": "3\n1\n2\n", // unsorted; resolver should pick min
		},
		errByCommand: map[string]error{},
	}

	targeting, err := resolveSwarmSessionTargeting(context.Background(), runner, "test")
	if err != nil {
		t.Fatalf("resolveSwarmSessionTargeting returned error: %v", err)
	}
	if targeting.WindowIndex != 1 {
		t.Fatalf("WindowIndex = %d, want %d", targeting.WindowIndex, 1)
	}
	if targeting.BasePaneIndex != 1 {
		t.Fatalf("BasePaneIndex = %d, want %d", targeting.BasePaneIndex, 1)
	}
}

func TestSwarmPaneTargetFromPlanIndex(t *testing.T) {
	t.Parallel()

	targeting := swarmSessionTargeting{WindowIndex: 0, BasePaneIndex: 0}
	target, err := swarmPaneTargetFromPlanIndex("test", targeting, 1)
	if err != nil {
		t.Fatalf("swarmPaneTargetFromPlanIndex returned error: %v", err)
	}
	if target != "test:0.1" {
		t.Fatalf("target = %q, want %q", target, "test:0.1")
	}

	targeting = swarmSessionTargeting{WindowIndex: 1, BasePaneIndex: 1}
	target, err = swarmPaneTargetFromPlanIndex("test", targeting, 1)
	if err != nil {
		t.Fatalf("swarmPaneTargetFromPlanIndex returned error: %v", err)
	}
	if target != "test:1.2" {
		t.Fatalf("target = %q, want %q", target, "test:1.2")
	}

	_, err = swarmPaneTargetFromPlanIndex("test", targeting, -1)
	if err == nil {
		t.Fatalf("expected error for negative plan pane index")
	}
}

func TestValidateAgentType(t *testing.T) {
	tests := []struct {
		agentType string
		wantErr   bool
	}{
		{AgentCC, false},
		{AgentCOD, false},
		{AgentGMI, false},
		{"invalid", true},
		{"", true},
		{"CC", true}, // Case sensitive
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			err := ValidateAgentType(tt.agentType)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateAgentType(%q) error = %v, wantErr %v",
					tt.agentType, err, tt.wantErr)
			}
		})
	}
}

func TestAgentConstants(t *testing.T) {
	if AgentCC != "cc" {
		t.Errorf("AgentCC = %q, want %q", AgentCC, "cc")
	}
	if AgentCOD != "cod" {
		t.Errorf("AgentCOD = %q, want %q", AgentCOD, "cod")
	}
	if AgentGMI != "gmi" {
		t.Errorf("AgentGMI = %q, want %q", AgentGMI, "gmi")
	}
}

func TestLaunchSwarmNilPlan(t *testing.T) {
	launcher := NewAgentLauncher()
	result, err := launcher.LaunchSwarm(nil)

	if err == nil {
		t.Error("expected error for nil plan")
	}
	if result != nil {
		t.Error("expected nil result for nil plan")
	}
}

func TestLaunchSwarmEmptyPlan(t *testing.T) {
	launcher := NewAgentLauncher()
	plan := &SwarmPlan{
		Sessions: []SessionSpec{},
	}

	result, err := launcher.LaunchSwarm(plan)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.TotalLaunched != 0 {
		t.Errorf("expected 0 launched, got %d", result.TotalLaunched)
	}
	if result.TotalFailed != 0 {
		t.Errorf("expected 0 failed, got %d", result.TotalFailed)
	}
}

func TestAgentLauncherLaunchAgent(t *testing.T) {
	mock := &MockTmuxClient{t: t}
	launcher := NewAgentLauncherWithClient(mock)
	launcher.PostLaunchDelay = 0

	t.Log("[TEST] launching agent in pane")
	if err := launcher.LaunchAgent("test_session", 1, AgentCC); err != nil {
		t.Fatalf("LaunchAgent failed: %v", err)
	}

	if len(mock.SendKeysCalls) != 2 {
		t.Fatalf("expected 2 SendKeys calls, got %d", len(mock.SendKeysCalls))
	}

	first := mock.SendKeysCalls[0]
	if first.Target != "test_session:1.1" || first.Keys != AgentCC || first.Enter {
		t.Errorf("unexpected first SendKeys call: %+v", first)
	}

	second := mock.SendKeysCalls[1]
	if second.Target != "test_session:1.1" || second.Keys != "" || !second.Enter {
		t.Errorf("unexpected second SendKeys call: %+v", second)
	}
}

func TestAgentLauncherLaunchAgentSendError(t *testing.T) {
	mock := &MockTmuxClient{t: t, SendErr: errors.New("send failed")}
	launcher := NewAgentLauncherWithClient(mock)
	launcher.PostLaunchDelay = 0

	t.Log("[TEST] launching agent with send error")
	err := launcher.LaunchAgent("test_session", 1, AgentCC)
	if err == nil {
		t.Fatal("expected error from LaunchAgent")
	}
	if len(mock.SendKeysCalls) != 1 {
		t.Fatalf("expected 1 SendKeys call on error, got %d", len(mock.SendKeysCalls))
	}
}

func TestLaunchAllInSession(t *testing.T) {
	mock := &MockTmuxClient{
		t: t,
		Panes: []tmux.Pane{
			{Index: 0}, // user pane
			{Index: 1},
			{Index: 2},
		},
	}
	launcher := NewAgentLauncherWithClient(mock)
	launcher.LaunchDelay = 0
	launcher.PostLaunchDelay = 0

	t.Log("[TEST] launching agents in session with user pane skipped")
	if err := launcher.LaunchAllInSession("test_session", AgentCOD); err != nil {
		t.Fatalf("LaunchAllInSession failed: %v", err)
	}

	if len(mock.GetPanesCalls) != 1 || mock.GetPanesCalls[0] != "test_session" {
		t.Fatalf("expected GetPanes called once with session name, got %v", mock.GetPanesCalls)
	}

	if len(mock.SendKeysCalls) != 4 {
		t.Fatalf("expected 4 SendKeys calls (2 panes x 2 calls), got %d", len(mock.SendKeysCalls))
	}
}

func TestLaunchAllInSessionEmptyPanes(t *testing.T) {
	mock := &MockTmuxClient{t: t}
	launcher := NewAgentLauncherWithClient(mock)

	t.Log("[TEST] launching agents with empty pane list")
	if err := launcher.LaunchAllInSession("test_session", AgentCC); err == nil {
		t.Fatal("expected error for empty pane list")
	}
}

func TestLaunchAllInSessionGetPanesError(t *testing.T) {
	mock := &MockTmuxClient{t: t, PaneErr: errors.New("get panes failed")}
	launcher := NewAgentLauncherWithClient(mock)

	t.Log("[TEST] launching agents with GetPanes error")
	if err := launcher.LaunchAllInSession("test_session", AgentCC); err == nil {
		t.Fatal("expected error from GetPanes")
	}
}

func TestLaunchSwarmSuccess(t *testing.T) {
	mock := &MockTmuxClient{t: t}
	launcher := NewAgentLauncherWithClient(mock)
	launcher.LaunchDelay = 0
	launcher.PostLaunchDelay = 0

	plan := &SwarmPlan{
		Sessions: []SessionSpec{
			{
				Name: "cc_agents_1",
				Panes: []PaneSpec{
					{Index: 1, AgentType: AgentCC},
					{Index: 2, AgentType: AgentCC},
				},
			},
		},
	}

	t.Log("[TEST] launching swarm with two panes")
	result, err := launcher.LaunchSwarm(plan)
	if err != nil {
		t.Fatalf("LaunchSwarm failed: %v", err)
	}
	if result.TotalLaunched != 2 || result.TotalFailed != 0 {
		t.Fatalf("unexpected counts: launched=%d failed=%d", result.TotalLaunched, result.TotalFailed)
	}
	if len(result.LaunchResults) != 2 {
		t.Fatalf("expected 2 launch results, got %d", len(result.LaunchResults))
	}
}

func TestLaunchSwarmFailures(t *testing.T) {
	mock := &MockTmuxClient{t: t, SendErr: errors.New("send failed")}
	launcher := NewAgentLauncherWithClient(mock)
	launcher.LaunchDelay = 0
	launcher.PostLaunchDelay = 0

	plan := &SwarmPlan{
		Sessions: []SessionSpec{
			{
				Name: "cc_agents_1",
				Panes: []PaneSpec{
					{Index: 1, AgentType: AgentCC},
					{Index: 2, AgentType: AgentCC},
				},
			},
		},
	}

	t.Log("[TEST] launching swarm with SendKeys failures")
	result, err := launcher.LaunchSwarm(plan)
	if err != nil {
		t.Fatalf("LaunchSwarm returned error: %v", err)
	}
	if result.TotalFailed != 2 {
		t.Fatalf("expected 2 failures, got %d", result.TotalFailed)
	}
	if result.TotalLaunched != 0 {
		t.Fatalf("expected 0 launched, got %d", result.TotalLaunched)
	}
	for _, res := range result.LaunchResults {
		if res.Success {
			t.Fatalf("expected failure result, got success for %s", res.SessionPane)
		}
		if res.Error == "" {
			t.Fatalf("expected error message for %s", res.SessionPane)
		}
	}
}

func TestAgentLauncherResult(t *testing.T) {
	result := &AgentLauncherResult{
		LaunchResults: []LaunchResult{
			{SessionPane: "sess:1.1", AgentType: AgentCC, Success: true},
			{SessionPane: "sess:1.2", AgentType: AgentCOD, Success: true},
			{SessionPane: "sess:1.3", AgentType: AgentGMI, Success: false, Error: "test error"},
		},
		TotalLaunched: 2,
		TotalFailed:   1,
	}

	if len(result.LaunchResults) != 3 {
		t.Errorf("expected 3 launch results, got %d", len(result.LaunchResults))
	}

	if result.TotalLaunched != 2 {
		t.Errorf("expected TotalLaunched of 2, got %d", result.TotalLaunched)
	}

	if result.TotalFailed != 1 {
		t.Errorf("expected TotalFailed of 1, got %d", result.TotalFailed)
	}
}

func TestLaunchResult(t *testing.T) {
	success := LaunchResult{
		SessionPane: "test:1.5",
		AgentType:   AgentCC,
		Success:     true,
	}

	if success.SessionPane != "test:1.5" {
		t.Errorf("unexpected SessionPane: %s", success.SessionPane)
	}
	if success.AgentType != AgentCC {
		t.Errorf("unexpected AgentType: %s", success.AgentType)
	}
	if !success.Success {
		t.Error("expected Success to be true")
	}
	if success.Error != "" {
		t.Errorf("expected empty Error, got %q", success.Error)
	}

	failure := LaunchResult{
		SessionPane: "test:1.6",
		AgentType:   AgentCOD,
		Success:     false,
		Error:       "connection refused",
	}

	if failure.Success {
		t.Error("expected Success to be false")
	}
	if failure.Error != "connection refused" {
		t.Errorf("expected Error 'connection refused', got %q", failure.Error)
	}
}

func TestTmuxClientHelper(t *testing.T) {
	// With nil client, should return default
	launcher := NewAgentLauncher()
	client := launcher.tmuxClient()
	if client == nil {
		t.Error("expected non-nil client from tmuxClient()")
	}
}

func TestDefaultAgentCommands(t *testing.T) {
	tests := []struct {
		agentType string
		expected  string
	}{
		{"cc", "claude"},
		{"cod", "codex"},
		{"gmi", "gemini"},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			cmd, ok := DefaultAgentCommands[tt.agentType]
			if !ok {
				t.Errorf("DefaultAgentCommands missing entry for %q", tt.agentType)
				return
			}
			if cmd != tt.expected {
				t.Errorf("DefaultAgentCommands[%q] = %q, want %q", tt.agentType, cmd, tt.expected)
			}
		})
	}
}

func TestDefaultAgentArgs(t *testing.T) {
	tests := []struct {
		agentType    string
		expectedArgs []string
	}{
		{"cc", []string{"--dangerously-skip-permissions"}},
		{"cod", []string{"--quiet", "--auto-approve"}},
		{"gmi", []string{"--non-interactive"}},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			args, ok := DefaultAgentArgs[tt.agentType]
			if !ok {
				t.Errorf("DefaultAgentArgs missing entry for %q", tt.agentType)
				return
			}
			if len(args) != len(tt.expectedArgs) {
				t.Errorf("DefaultAgentArgs[%q] has %d args, want %d", tt.agentType, len(args), len(tt.expectedArgs))
				return
			}
			for i, arg := range args {
				if arg != tt.expectedArgs[i] {
					t.Errorf("DefaultAgentArgs[%q][%d] = %q, want %q", tt.agentType, i, arg, tt.expectedArgs[i])
				}
			}
		})
	}
}

func TestNewLaunchCommandBuilder(t *testing.T) {
	builder := NewLaunchCommandBuilder()

	if builder == nil {
		t.Fatal("NewLaunchCommandBuilder returned nil")
	}

	if builder.AgentPaths == nil {
		t.Error("expected non-nil AgentPaths map")
	}

	if builder.AgentArgs == nil {
		t.Error("expected non-nil AgentArgs map")
	}

	if builder.EnvVars == nil {
		t.Error("expected non-nil EnvVars map")
	}

	if builder.UseFullPaths {
		t.Error("expected UseFullPaths to be false by default")
	}

	if builder.Logger == nil {
		t.Error("expected non-nil Logger")
	}
}

func TestLaunchCommandBuilderChaining(t *testing.T) {
	builder := NewLaunchCommandBuilder()

	// Test WithAgentPath chaining
	result := builder.WithAgentPath("cc", "/usr/local/bin/claude")
	if result != builder {
		t.Error("WithAgentPath should return the same builder for chaining")
	}
	if builder.AgentPaths["cc"] != "/usr/local/bin/claude" {
		t.Errorf("expected AgentPaths[cc] to be /usr/local/bin/claude, got %q", builder.AgentPaths["cc"])
	}

	// Test WithAgentArgs chaining
	result = builder.WithAgentArgs("cc", []string{"--custom-arg"})
	if result != builder {
		t.Error("WithAgentArgs should return the same builder for chaining")
	}
	if len(builder.AgentArgs["cc"]) != 1 || builder.AgentArgs["cc"][0] != "--custom-arg" {
		t.Errorf("expected AgentArgs[cc] to be [--custom-arg], got %v", builder.AgentArgs["cc"])
	}

	// Test WithEnvVars chaining
	result = builder.WithEnvVars("cc", map[string]string{"FOO": "bar"})
	if result != builder {
		t.Error("WithEnvVars should return the same builder for chaining")
	}
	if builder.EnvVars["cc"]["FOO"] != "bar" {
		t.Errorf("expected EnvVars[cc][FOO] to be bar, got %q", builder.EnvVars["cc"]["FOO"])
	}

	// Test WithFullPaths chaining
	result = builder.WithFullPaths(true)
	if result != builder {
		t.Error("WithFullPaths should return the same builder for chaining")
	}
	if !builder.UseFullPaths {
		t.Error("expected UseFullPaths to be true")
	}

	// Test WithLogger chaining
	result = builder.WithLogger(nil)
	if result != builder {
		t.Error("WithLogger should return the same builder for chaining")
	}
}

func TestLaunchCommandToShellCommand(t *testing.T) {
	tests := []struct {
		name     string
		cmd      LaunchCommand
		expected string
	}{
		{
			name: "binary only",
			cmd: LaunchCommand{
				Binary: "claude",
			},
			expected: "claude",
		},
		{
			name: "binary with single arg",
			cmd: LaunchCommand{
				Binary: "claude",
				Args:   []string{"--dangerously-skip-permissions"},
			},
			expected: "claude --dangerously-skip-permissions",
		},
		{
			name: "binary with multiple args",
			cmd: LaunchCommand{
				Binary: "codex",
				Args:   []string{"--quiet", "--auto-approve"},
			},
			expected: "codex --quiet --auto-approve",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.cmd.ToShellCommand()
			if result != tt.expected {
				t.Errorf("ToShellCommand() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestLaunchCommandToSimpleCommand(t *testing.T) {
	cmd := LaunchCommand{
		Binary: "claude",
		Args:   []string{"--dangerously-skip-permissions"},
	}

	result := cmd.ToSimpleCommand()
	if result != "claude" {
		t.Errorf("ToSimpleCommand() = %q, want %q", result, "claude")
	}
}

func TestBuildLaunchCommand(t *testing.T) {
	tests := []struct {
		name           string
		agentType      string
		useFullPaths   bool
		expectedBinary string
		expectedArgs   []string
	}{
		{
			name:           "cc with shell alias",
			agentType:      "cc",
			useFullPaths:   false,
			expectedBinary: "cc",
			expectedArgs:   []string{"--dangerously-skip-permissions"},
		},
		{
			name:           "cc with full path",
			agentType:      "cc",
			useFullPaths:   true,
			expectedBinary: "claude",
			expectedArgs:   []string{"--dangerously-skip-permissions"},
		},
		{
			name:           "cod with shell alias",
			agentType:      "cod",
			useFullPaths:   false,
			expectedBinary: "cod",
			expectedArgs:   []string{"--quiet", "--auto-approve"},
		},
		{
			name:           "cod with full path",
			agentType:      "cod",
			useFullPaths:   true,
			expectedBinary: "codex",
			expectedArgs:   []string{"--quiet", "--auto-approve"},
		},
		{
			name:           "gmi with shell alias",
			agentType:      "gmi",
			useFullPaths:   false,
			expectedBinary: "gmi",
			expectedArgs:   []string{"--non-interactive"},
		},
		{
			name:           "gmi with full path",
			agentType:      "gmi",
			useFullPaths:   true,
			expectedBinary: "gemini",
			expectedArgs:   []string{"--non-interactive"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := NewLaunchCommandBuilder().WithFullPaths(tt.useFullPaths)
			spec := PaneSpec{
				Index:     1,
				AgentType: tt.agentType,
			}

			cmd := builder.BuildLaunchCommand(spec, "/tmp")

			if cmd.Binary != tt.expectedBinary {
				t.Errorf("BuildLaunchCommand().Binary = %q, want %q", cmd.Binary, tt.expectedBinary)
			}

			if cmd.AgentType != tt.agentType {
				t.Errorf("BuildLaunchCommand().AgentType = %q, want %q", cmd.AgentType, tt.agentType)
			}

			if cmd.WorkDir != "/tmp" {
				t.Errorf("BuildLaunchCommand().WorkDir = %q, want %q", cmd.WorkDir, "/tmp")
			}

			if len(cmd.Args) != len(tt.expectedArgs) {
				t.Errorf("BuildLaunchCommand().Args has %d elements, want %d", len(cmd.Args), len(tt.expectedArgs))
				return
			}

			for i, arg := range cmd.Args {
				if arg != tt.expectedArgs[i] {
					t.Errorf("BuildLaunchCommand().Args[%d] = %q, want %q", i, arg, tt.expectedArgs[i])
				}
			}
		})
	}
}

func TestBuildLaunchCommandWithCustomPath(t *testing.T) {
	builder := NewLaunchCommandBuilder().
		WithFullPaths(true).
		WithAgentPath("cc", "/custom/path/to/claude")

	spec := PaneSpec{
		Index:     1,
		AgentType: "cc",
	}

	cmd := builder.BuildLaunchCommand(spec, "/tmp")

	if cmd.Binary != "/custom/path/to/claude" {
		t.Errorf("BuildLaunchCommand().Binary = %q, want %q", cmd.Binary, "/custom/path/to/claude")
	}
}

func TestBuildLaunchCommandWithCustomArgs(t *testing.T) {
	customArgs := []string{"--custom-flag", "--another-flag"}
	builder := NewLaunchCommandBuilder().
		WithAgentArgs("cc", customArgs)

	spec := PaneSpec{
		Index:     1,
		AgentType: "cc",
	}

	cmd := builder.BuildLaunchCommand(spec, "/tmp")

	if len(cmd.Args) != len(customArgs) {
		t.Errorf("BuildLaunchCommand().Args has %d elements, want %d", len(cmd.Args), len(customArgs))
		return
	}

	for i, arg := range cmd.Args {
		if arg != customArgs[i] {
			t.Errorf("BuildLaunchCommand().Args[%d] = %q, want %q", i, arg, customArgs[i])
		}
	}
}

func TestBuildLaunchCommandWithEnvVars(t *testing.T) {
	builder := NewLaunchCommandBuilder().
		WithEnvVars("cc", map[string]string{"API_KEY": "secret", "DEBUG": "true"})

	spec := PaneSpec{
		Index:     1,
		AgentType: "cc",
	}

	cmd := builder.BuildLaunchCommand(spec, "/tmp")

	if len(cmd.Env) != 2 {
		t.Errorf("BuildLaunchCommand().Env has %d elements, want 2", len(cmd.Env))
		return
	}

	// Check that env vars are in the format KEY=value
	envMap := make(map[string]bool)
	for _, env := range cmd.Env {
		envMap[env] = true
	}

	if !envMap["API_KEY=secret"] {
		t.Error("expected API_KEY=secret in Env")
	}
	if !envMap["DEBUG=true"] {
		t.Error("expected DEBUG=true in Env")
	}
}

func TestBuildSwarmCommands(t *testing.T) {
	builder := NewLaunchCommandBuilder()

	plan := &SwarmPlan{
		ScanDir: "/projects",
		Sessions: []SessionSpec{
			{
				Name:      "test-session",
				AgentType: "cc",
				PaneCount: 2,
				Panes: []PaneSpec{
					{Index: 1, AgentType: "cc", Project: "/projects/foo"},
					{Index: 2, AgentType: "cc", Project: "/projects/bar"},
				},
			},
		},
	}

	commands := builder.BuildSwarmCommands(plan)

	if len(commands) != 2 {
		t.Errorf("BuildSwarmCommands() returned %d commands, want 2", len(commands))
		return
	}

	// Check first command
	if commands[0].WorkDir != "/projects/foo" {
		t.Errorf("commands[0].WorkDir = %q, want %q", commands[0].WorkDir, "/projects/foo")
	}

	// Check second command
	if commands[1].WorkDir != "/projects/bar" {
		t.Errorf("commands[1].WorkDir = %q, want %q", commands[1].WorkDir, "/projects/bar")
	}
}

func TestBuildSwarmCommandsNilPlan(t *testing.T) {
	builder := NewLaunchCommandBuilder()
	commands := builder.BuildSwarmCommands(nil)

	if commands != nil {
		t.Errorf("BuildSwarmCommands(nil) returned %v, want nil", commands)
	}
}

func TestBuildSwarmCommandsFallbackToScanDir(t *testing.T) {
	builder := NewLaunchCommandBuilder()

	plan := &SwarmPlan{
		ScanDir: "/default/dir",
		Sessions: []SessionSpec{
			{
				Name:      "test-session",
				AgentType: "cc",
				PaneCount: 1,
				Panes: []PaneSpec{
					{Index: 1, AgentType: "cc", Project: ""}, // Empty project
				},
			},
		},
	}

	commands := builder.BuildSwarmCommands(plan)

	if len(commands) != 1 {
		t.Errorf("BuildSwarmCommands() returned %d commands, want 1", len(commands))
		return
	}

	// Should fall back to ScanDir
	if commands[0].WorkDir != "/default/dir" {
		t.Errorf("commands[0].WorkDir = %q, want %q (should fallback to ScanDir)", commands[0].WorkDir, "/default/dir")
	}
}

func TestLoggerBHelper(t *testing.T) {
	builder := NewLaunchCommandBuilder()
	logger := builder.loggerB()

	if logger == nil {
		t.Error("expected non-nil logger from loggerB()")
	}
}
