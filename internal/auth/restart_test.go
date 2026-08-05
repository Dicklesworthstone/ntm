package auth

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/swarm"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// =============================================================================
// NewOrchestrator
// =============================================================================

func TestNewOrchestrator(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	orch := NewOrchestrator(cfg)

	if orch == nil {
		t.Fatal("NewOrchestrator returned nil")
	}
	if orch.cfg != cfg {
		t.Error("config not stored correctly")
	}
	if orch.authFlows == nil {
		t.Error("authFlows map should be initialized")
	}
	if len(orch.authFlows) != 0 {
		t.Errorf("authFlows should be empty, got %d entries", len(orch.authFlows))
	}
	if orch.captureOutput == nil {
		t.Error("captureOutput should be set")
	}
	if orch.sendKeys == nil {
		t.Error("sendKeys should be set")
	}
	if orch.sendInterrupt == nil {
		t.Error("sendInterrupt should be set")
	}
	if orch.buildPaneCommand == nil {
		t.Error("buildPaneCommand should be set")
	}
	if orch.sanitizePaneCommand == nil {
		t.Error("sanitizePaneCommand should be set")
	}
	if orch.promptBrowserAuth == nil {
		t.Error("promptBrowserAuth should be set")
	}
	if orch.sleep == nil {
		t.Error("sleep should be set")
	}
}

// =============================================================================
// RegisterAuthFlow
// =============================================================================

type mockAuthFlow struct{}

func (m *mockAuthFlow) InitiateAuth(paneID string) error { return nil }

func TestRegisterAuthFlow(t *testing.T) {
	t.Parallel()

	orch := NewOrchestrator(config.Default())

	t.Run("register single flow", func(t *testing.T) {
		flow := &mockAuthFlow{}
		orch.RegisterAuthFlow("claude", flow)
		if got, ok := orch.authFlows["claude"]; !ok {
			t.Error("flow not registered")
		} else if got != flow {
			t.Error("wrong flow stored")
		}
	})

	t.Run("register multiple flows", func(t *testing.T) {
		orch2 := NewOrchestrator(config.Default())
		orch2.RegisterAuthFlow("claude", &mockAuthFlow{})
		orch2.RegisterAuthFlow("codex", &mockAuthFlow{})
		orch2.RegisterAuthFlow("gemini", &mockAuthFlow{})
		if len(orch2.authFlows) != 3 {
			t.Errorf("got %d flows, want 3", len(orch2.authFlows))
		}
	})

	t.Run("overwrite existing flow", func(t *testing.T) {
		orch3 := NewOrchestrator(config.Default())
		flow1 := &mockAuthFlow{}
		flow2 := &mockAuthFlow{}
		orch3.RegisterAuthFlow("claude", flow1)
		orch3.RegisterAuthFlow("claude", flow2)
		if orch3.authFlows["claude"] != flow2 {
			t.Error("flow should be overwritten")
		}
		if len(orch3.authFlows) != 1 {
			t.Errorf("got %d flows, want 1", len(orch3.authFlows))
		}
	})
}

// =============================================================================
// Shell prompt regex matching
// =============================================================================

func TestShellPromptRegexps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		output  string
		matches bool
	}{
		{"bash dollar sign", "user@host:~$ ", true},
		{"bare dollar", "$ ", true},
		{"dollar end of line", "$", true},
		{"dollar with trailing space", "$  ", true},
		{"zsh percent", "user@host %", true},
		{"bare percent", "% ", true},
		{"percent end of line", "%", true},
		{"generic prompt", "> ", true},
		{"bare angle bracket", ">", true},
		{"no prompt - text only", "still running command", false},
		{"no prompt - empty", "", false},
		{"dollar mid-text", "cost is $5 for this", false},
		{"percent mid-text", "100% complete", false},
		{"angle mid-text", "a > b", false},
		{"multiline with prompt at end", "output line 1\noutput line 2\n$", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			matched := false
			for _, re := range shellPromptRegexps {
				if re.MatchString(tt.output) {
					matched = true
					break
				}
			}
			if matched != tt.matches {
				t.Errorf("shellPromptRegexps match(%q) = %v, want %v", tt.output, matched, tt.matches)
			}
		})
	}
}

// =============================================================================
// WaitForShellPrompt
// =============================================================================

func TestWaitForShellPrompt(t *testing.T) {
	orch := NewOrchestrator(config.Default())

	tests := []struct {
		name        string
		mockOutputs []string
		timeout     time.Duration
		wantErr     bool
	}{
		{
			name:        "detect bash prompt immediately",
			mockOutputs: []string{"user@host:~$"},
			timeout:     1 * time.Second,
			wantErr:     false,
		},
		{
			name:        "detect zsh prompt after delay",
			mockOutputs: []string{"output line 1", "output line 2", "user@host %"},
			timeout:     2 * time.Second,
			wantErr:     false,
		},
		{
			name:        "detect generic prompt",
			mockOutputs: []string{"> "},
			timeout:     1 * time.Second,
			wantErr:     false,
		},
		{
			name:        "timeout waiting for prompt",
			mockOutputs: []string{"still running...", "still running...", "still running..."},
			timeout:     100 * time.Millisecond,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx := 0
			orch.captureOutput = func(paneID string, lines int) (string, error) {
				if idx >= len(tt.mockOutputs) {
					return tt.mockOutputs[len(tt.mockOutputs)-1], nil
				}
				out := tt.mockOutputs[idx]
				idx++
				return out, nil
			}

			start := time.Now()
			err := orch.WaitForShellPrompt("dummy", tt.timeout)
			duration := time.Since(start)

			if (err != nil) != tt.wantErr {
				t.Errorf("WaitForShellPrompt() error = %v, wantErr %v", err, tt.wantErr)
			}

			if !tt.wantErr && duration > tt.timeout {
				t.Errorf("WaitForShellPrompt() took %v, want < %v", duration, tt.timeout)
			}
		})
	}
}

// =============================================================================
// RestartContext fields
// =============================================================================

func TestRestartContextFields(t *testing.T) {
	t.Parallel()

	ctx := RestartContext{
		PaneID:      "%42",
		Provider:    "claude",
		TargetEmail: "user@example.com",
		ModelAlias:  "opus",
		SessionName: "test-session",
		PaneIndex:   3,
		ProjectDir:  "/data/projects/myapp",
	}

	if ctx.PaneID != "%42" {
		t.Errorf("PaneID = %q", ctx.PaneID)
	}
	if ctx.Provider != "claude" {
		t.Errorf("Provider = %q", ctx.Provider)
	}
	if ctx.PaneIndex != 3 {
		t.Errorf("PaneIndex = %d", ctx.PaneIndex)
	}
}

// =============================================================================
// PromptBrowserAuth
// =============================================================================

func TestPromptBrowserAuth(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	err := promptBrowserAuth(strings.NewReader("\n"), &out, "backup@example.com")
	if err != nil {
		t.Fatalf("promptBrowserAuth error: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"Browser authentication required",
		"backup@example.com",
		"Press ENTER to continue",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt output missing %q: %q", want, got)
		}
	}
}

func TestPromptBrowserAuthRequiresExplicitEnter(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	err := promptBrowserAuth(strings.NewReader(""), &out, "backup@example.com")
	if err == nil {
		t.Fatal("expected promptBrowserAuth to fail on EOF without ENTER confirmation")
	}
	if !strings.Contains(err.Error(), "confirmation") {
		t.Fatalf("promptBrowserAuth error %q missing confirmation context", err)
	}
}

func TestOrchestrator_ExecuteRestartStrategyPromptsBeforeStarting(t *testing.T) {
	cfg := config.Default()
	cfg.Agents.Claude = "claude --model {{.Model}}"

	orch := NewOrchestrator(cfg)
	orch.sleep = func(time.Duration) {}
	orch.captureOutput = func(string, int) (string, error) {
		return "$ ", nil
	}

	var events []string
	orch.sendKeys = func(paneID, keys string, enter bool) error {
		events = append(events, "exit")
		return nil
	}
	orch.sendInterrupt = func(paneID string) error {
		events = append(events, "interrupt")
		return nil
	}
	orch.promptBrowserAuth = func(email string) error {
		if email != "backup@example.com" {
			t.Errorf("prompt email = %q, want backup@example.com", email)
		}
		events = append(events, "prompt")
		return nil
	}
	orch.sanitizePaneCommand = func(cmd string) (string, error) {
		return cmd, nil
	}
	orch.buildPaneCommand = func(dir, cmd string) (string, error) {
		return cmd, nil
	}
	orch.sendKeysForAgent = func(paneID, keys string, enter bool, agentType tmux.AgentType) error {
		events = append(events, "start")
		return nil
	}

	err := orch.ExecuteRestartStrategy(RestartContext{
		PaneID:      "%1",
		Provider:    "claude",
		TargetEmail: "backup@example.com",
		SessionName: "proj",
		PaneIndex:   1,
		ProjectDir:  "/data/projects/ntm",
	})
	if err != nil {
		t.Fatalf("ExecuteRestartStrategy error: %v", err)
	}

	promptIndex := -1
	startIndex := -1
	for i, event := range events {
		switch event {
		case "prompt":
			promptIndex = i
		case "start":
			startIndex = i
		}
	}
	if promptIndex < 0 {
		t.Fatalf("events missing prompt: %v", events)
	}
	if startIndex < 0 {
		t.Fatalf("events missing start: %v", events)
	}
	if promptIndex > startIndex {
		t.Fatalf("prompt should happen before start, events: %v", events)
	}
}

func TestOrchestrator_ExecuteRestartStrategyPromptFailureStopsRestart(t *testing.T) {
	cfg := config.Default()
	cfg.Agents.Claude = "claude --model {{.Model}}"

	orch := NewOrchestrator(cfg)
	orch.sleep = func(time.Duration) {}
	orch.captureOutput = func(string, int) (string, error) {
		return "$ ", nil
	}
	orch.sendKeys = func(string, string, bool) error { return nil }
	orch.sendInterrupt = func(string) error { return nil }
	orch.promptBrowserAuth = func(string) error {
		return io.EOF
	}
	orch.sanitizePaneCommand = func(cmd string) (string, error) { return cmd, nil }
	orch.buildPaneCommand = func(_, cmd string) (string, error) { return cmd, nil }

	started := false
	orch.sendKeysForAgent = func(string, string, bool, tmux.AgentType) error {
		started = true
		return nil
	}

	err := orch.ExecuteRestartStrategy(RestartContext{
		PaneID:      "%1",
		Provider:    "claude",
		TargetEmail: "backup@example.com",
		SessionName: "proj",
		PaneIndex:   1,
		ProjectDir:  "/data/projects/ntm",
	})
	if err == nil {
		t.Fatal("expected ExecuteRestartStrategy to fail when browser confirmation fails")
	}
	if !strings.Contains(err.Error(), "browser auth prompt") {
		t.Fatalf("unexpected error: %v", err)
	}
	if started {
		t.Fatal("replacement agent started despite prompt failure")
	}
}

func TestOrchestrator_GrokRestartRejectsBeforeAnyMutation(t *testing.T) {
	orch := NewOrchestrator(config.Default())
	calls := map[string]int{}
	orch.sendKeys = func(string, string, bool) error {
		calls["send_keys"]++
		return nil
	}
	orch.sendInterrupt = func(string) error {
		calls["interrupt"]++
		return nil
	}
	orch.captureOutput = func(string, int) (string, error) {
		calls["capture"]++
		return "$ ", nil
	}
	orch.promptBrowserAuth = func(string) error {
		calls["browser_prompt"]++
		return nil
	}
	orch.sanitizePaneCommand = func(command string) (string, error) {
		calls["sanitize"]++
		return command, nil
	}
	orch.buildPaneCommand = func(_, command string) (string, error) {
		calls["build"]++
		return command, nil
	}
	orch.sendKeysForAgent = func(string, string, bool, tmux.AgentType) error {
		calls["send_for_agent"]++
		return nil
	}
	orch.sleep = func(time.Duration) {
		calls["sleep"]++
	}
	ctx := RestartContext{
		PaneID:      "%9",
		Provider:    "claude",
		AgentType:   "grok-build",
		TargetEmail: "backup@example.com",
		SessionName: "proj",
		PaneIndex:   9,
		ProjectDir:  "/data/projects/ntm",
	}

	err := orch.ExecuteRestartStrategy(ctx)
	if !errors.Is(err, agent.ErrAutomatedRelaunchNotImplemented) {
		t.Fatalf("ExecuteRestartStrategy() error = %v, want relaunch sentinel", err)
	}
	if len(calls) != 0 {
		t.Fatalf("ExecuteRestartStrategy() mutated before Grok rejection: %v", calls)
	}

	err = orch.StartNewAgentSession(ctx)
	if !errors.Is(err, agent.ErrAutomatedRelaunchNotImplemented) {
		t.Fatalf("StartNewAgentSession() error = %v, want relaunch sentinel", err)
	}
	if len(calls) != 0 {
		t.Fatalf("StartNewAgentSession() mutated before Grok rejection: %v", calls)
	}

	ctx.AgentType = ""
	ctx.Provider = "xai-grok-build"
	err = orch.ExecuteRestartStrategy(ctx)
	if !errors.Is(err, agent.ErrAutomatedRelaunchNotImplemented) {
		t.Fatalf("ExecuteRestartStrategy(provider-only Grok) error = %v, want relaunch sentinel", err)
	}
	if len(calls) != 0 {
		t.Fatalf("provider-only Grok restart mutated before rejection: %v", calls)
	}
}

// =============================================================================
// TerminateSession
// =============================================================================

func TestOrchestrator_TerminateSession(t *testing.T) {
	t.Parallel()

	t.Run("uses provider exit command and interrupts twice", func(t *testing.T) {
		t.Parallel()

		orch := NewOrchestrator(config.Default())
		orch.sleep = func(time.Duration) {}

		var gotExit []string
		orch.sendKeys = func(paneID, keys string, enter bool) error {
			gotExit = append(gotExit, keys)
			return nil
		}

		interrupts := 0
		orch.sendInterrupt = func(paneID string) error {
			interrupts++
			return nil
		}

		if err := orch.TerminateSession("pane-1", "claude"); err != nil {
			t.Fatalf("TerminateSession error: %v", err)
		}
		if len(gotExit) != 1 || gotExit[0] != "/exit" {
			t.Errorf("exit commands = %v, want [/exit]", gotExit)
		}
		if interrupts != 2 {
			t.Errorf("interrupts = %d, want 2", interrupts)
		}
	})

	t.Run("unknown provider skips exit command", func(t *testing.T) {
		t.Parallel()

		orch := NewOrchestrator(config.Default())
		orch.sleep = func(time.Duration) {}

		calledExit := false
		orch.sendKeys = func(paneID, keys string, enter bool) error {
			calledExit = true
			return nil
		}
		interrupts := 0
		orch.sendInterrupt = func(paneID string) error {
			interrupts++
			return nil
		}

		if err := orch.TerminateSession("pane-2", "unknown"); err != nil {
			t.Fatalf("TerminateSession error: %v", err)
		}
		if calledExit {
			t.Error("expected no exit command for unknown provider")
		}
		if interrupts != 2 {
			t.Errorf("interrupts = %d, want 2", interrupts)
		}
	})

	t.Run("interrupt error surfaces", func(t *testing.T) {
		t.Parallel()

		orch := NewOrchestrator(config.Default())
		orch.sleep = func(time.Duration) {}

		orch.sendInterrupt = func(paneID string) error {
			return errors.New("boom")
		}

		if err := orch.TerminateSession("pane-3", "claude"); err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

// =============================================================================
// StartNewAgentSession
// =============================================================================

func TestOrchestrator_StartNewAgentSession(t *testing.T) {
	t.Parallel()

	t.Run("unknown provider", func(t *testing.T) {
		t.Parallel()

		orch := NewOrchestrator(config.Default())
		if err := orch.StartNewAgentSession(RestartContext{Provider: "unknown"}); err == nil {
			t.Fatal("expected error for unknown provider")
		}
	})

	t.Run("build and send command", func(t *testing.T) {
		t.Parallel()

		cfg := config.Default()
		cfg.Agents.Claude = "claude --model {{.Model}} --session {{.SessionName}} --pane {{.PaneIndex}}"

		orch := NewOrchestrator(cfg)
		orch.sleep = func(time.Duration) {}

		var sanitizeInput string
		orch.sanitizePaneCommand = func(cmd string) (string, error) {
			sanitizeInput = cmd
			return "sanitized-cmd", nil
		}
		var buildDir string
		var buildCmd string
		orch.buildPaneCommand = func(dir, cmd string) (string, error) {
			buildDir = dir
			buildCmd = cmd
			return "built-cmd", nil
		}
		var sent string
		var sentPane string
		var sentEnter bool
		var sentAgentType tmux.AgentType
		orch.sendKeysForAgent = func(paneID, keys string, enter bool, agentType tmux.AgentType) error {
			sentPane = paneID
			sent = keys
			sentEnter = enter
			sentAgentType = agentType
			return nil
		}

		ctx := RestartContext{
			PaneID:      "%1",
			Provider:    "claude",
			ModelAlias:  "",
			SessionName: "proj",
			PaneIndex:   2,
			ProjectDir:  "/data/projects/ntm",
		}

		if err := orch.StartNewAgentSession(ctx); err != nil {
			t.Fatalf("StartNewAgentSession error: %v", err)
		}
		if sanitizeInput == "" {
			t.Fatal("expected sanitize to receive a command")
		}
		if !strings.Contains(sanitizeInput, "claude --model") || !strings.Contains(sanitizeInput, "proj") {
			t.Errorf("sanitize input = %q, want template fields expanded", sanitizeInput)
		}
		if buildDir != ctx.ProjectDir {
			t.Errorf("build dir = %q, want %q", buildDir, ctx.ProjectDir)
		}
		if buildCmd != "sanitized-cmd" {
			t.Errorf("build cmd = %q, want %q", buildCmd, "sanitized-cmd")
		}
		if sentPane != ctx.PaneID {
			t.Errorf("sent pane = %q, want %q", sentPane, ctx.PaneID)
		}
		if sent != "built-cmd" {
			t.Errorf("sent keys = %q, want %q", sent, "built-cmd")
		}
		if !sentEnter {
			t.Error("expected sendKeysForAgent enter=true")
		}
		if sentAgentType != tmux.AgentClaude {
			t.Errorf("sent agent type = %q, want %q", sentAgentType, tmux.AgentClaude)
		}
	})

	t.Run("sanitize error surfaces", func(t *testing.T) {
		t.Parallel()

		cfg := config.Default()
		orch := NewOrchestrator(cfg)
		orch.sanitizePaneCommand = func(cmd string) (string, error) {
			return "", errors.New("bad cmd")
		}

		ctx := RestartContext{
			PaneID:      "%2",
			Provider:    "claude",
			SessionName: "proj",
			PaneIndex:   1,
			ProjectDir:  "/data/projects/ntm",
		}
		if err := orch.StartNewAgentSession(ctx); err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("build error surfaces", func(t *testing.T) {
		t.Parallel()

		cfg := config.Default()
		orch := NewOrchestrator(cfg)
		orch.sanitizePaneCommand = func(cmd string) (string, error) {
			return "ok", nil
		}
		orch.buildPaneCommand = func(dir, cmd string) (string, error) {
			return "", errors.New("build fail")
		}

		ctx := RestartContext{
			PaneID:      "%3",
			Provider:    "claude",
			SessionName: "proj",
			PaneIndex:   1,
			ProjectDir:  "/data/projects/ntm",
		}
		if err := orch.StartNewAgentSession(ctx); err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

// GH#237 / bd-uljcm: the auth restarter fires exactly when a pane has hit an
// auth or limit error — the symptom of the shared-credential cascade. If it
// relaunches without the isolated CLAUDE_CONFIG_DIR, the pane rejoins the
// rotating credential and its next refresh invalidates every other pane's
// token, so isolation would evaporate at the precise moment it was working.
func TestStartNewAgentSession_ReappliesClaudeCredentialIsolation(t *testing.T) {
	projectDir := t.TempDir()

	// Declare the file-based credential store so this exercises the isolation
	// path on every platform, including macOS where the real machine's
	// Keychain would otherwise make it fail closed and skip.
	t.Setenv(swarm.ClaudeCredentialStoreEnvVar, "file")

	cfg := config.Default()
	cfg.Agents.ClaudeIsolateCredentials = true
	cfg.Agents.ClaudeTokenFile = ""

	orch := NewOrchestrator(cfg)
	var sent string
	orch.sanitizePaneCommand = func(cmd string) (string, error) { return cmd, nil }
	orch.buildPaneCommand = func(dir, cmd string) (string, error) { return cmd, nil }
	orch.sendKeysForAgent = func(_, cmd string, _ bool, _ tmux.AgentType) error {
		sent = cmd
		return nil
	}

	ctx := RestartContext{
		PaneID:      "%3",
		Provider:    "Claude",
		AgentType:   "cc",
		SessionName: "proj",
		PaneIndex:   2,
		ProjectDir:  projectDir,
	}
	err := orch.StartNewAgentSession(ctx)
	if err != nil {
		// On a platform where the credential is not in the config dir (macOS
		// Keychain), isolation cannot be enforced and the restart must fail
		// closed rather than silently relaunch unisolated.
		if strings.Contains(err.Error(), "per-pane isolation cannot be enforced") {
			t.Skipf("credential store is not isolable on this platform: %v", err)
		}
		t.Fatalf("StartNewAgentSession: %v", err)
	}

	if !strings.Contains(sent, "CLAUDE_CONFIG_DIR=") {
		t.Fatalf("relaunch command has no isolated config dir: %q", sent)
	}
	if !strings.Contains(sent, "claude-homes") {
		t.Fatalf("relaunch command does not point at the pane-private config dir: %q", sent)
	}
}

// A non-Claude relaunch must be untouched by the isolation logic.
func TestStartNewAgentSession_LeavesNonClaudeRelaunchAlone(t *testing.T) {
	cfg := config.Default()
	cfg.Agents.ClaudeIsolateCredentials = true

	orch := NewOrchestrator(cfg)
	var sent string
	orch.sanitizePaneCommand = func(cmd string) (string, error) { return cmd, nil }
	orch.buildPaneCommand = func(dir, cmd string) (string, error) { return cmd, nil }
	orch.sendKeysForAgent = func(_, cmd string, _ bool, _ tmux.AgentType) error {
		sent = cmd
		return nil
	}

	ctx := RestartContext{
		PaneID:      "%4",
		Provider:    "Codex",
		AgentType:   "cod",
		SessionName: "proj",
		PaneIndex:   1,
		ProjectDir:  t.TempDir(),
	}
	if err := orch.StartNewAgentSession(ctx); err != nil {
		t.Fatalf("StartNewAgentSession: %v", err)
	}
	if strings.Contains(sent, "CLAUDE_CONFIG_DIR") {
		t.Fatalf("codex relaunch picked up claude isolation env: %q", sent)
	}
}
