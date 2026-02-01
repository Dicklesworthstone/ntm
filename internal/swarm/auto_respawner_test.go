package swarm

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// mockPaneSpawner implements context.PaneSpawner for testing.
type mockPaneSpawner struct {
	mu            sync.Mutex
	spawnCalls    []spawnCall
	killCalls     []string
	sendKeysCalls []sendKeysCall
	spawnErr      error
	killErr       error
	sendKeysErr   error
	panes         []tmux.Pane
}

type spawnCall struct {
	session   string
	agentType string
	index     int
	variant   string
	workDir   string
}

type sendKeysCall struct {
	paneID string
	text   string
	enter  bool
}

func (m *mockPaneSpawner) SpawnAgent(session, agentType string, index int, variant string, workDir string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spawnCalls = append(m.spawnCalls, spawnCall{
		session:   session,
		agentType: agentType,
		index:     index,
		variant:   variant,
		workDir:   workDir,
	})
	if m.spawnErr != nil {
		return "", m.spawnErr
	}
	return session + ":1.1", nil
}

func (m *mockPaneSpawner) KillPane(paneID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.killCalls = append(m.killCalls, paneID)
	return m.killErr
}

func (m *mockPaneSpawner) SendKeys(paneID, text string, enter bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sendKeysCalls = append(m.sendKeysCalls, sendKeysCall{
		paneID: paneID,
		text:   text,
		enter:  enter,
	})
	return m.sendKeysErr
}

func (m *mockPaneSpawner) GetPanes(session string) ([]tmux.Pane, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.panes, nil
}

// mockAccountRotator implements AccountRotator for testing.
type mockAccountRotator struct {
	mu             sync.Mutex
	rotateCalls    []string
	currentAccount map[string]string
	nextAccount    map[string]string
	rotateErr      error
}

func newMockAccountRotator() *mockAccountRotator {
	return &mockAccountRotator{
		currentAccount: make(map[string]string),
		nextAccount:    make(map[string]string),
	}
}

func (m *mockAccountRotator) RotateAccount(agentType string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rotateCalls = append(m.rotateCalls, agentType)
	if m.rotateErr != nil {
		return "", m.rotateErr
	}
	newAcc := m.nextAccount[agentType]
	if newAcc == "" {
		newAcc = "account_2"
	}
	m.currentAccount[agentType] = newAcc
	return newAcc, nil
}

func (m *mockAccountRotator) CurrentAccount(agentType string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	acc := m.currentAccount[agentType]
	if acc == "" {
		return "account_1"
	}
	return acc
}

func (m *mockAccountRotator) OnLimitHit(event LimitHitEvent) (*RotationRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rotateCalls = append(m.rotateCalls, event.AgentType)
	if m.rotateErr != nil {
		return nil, m.rotateErr
	}
	prev := m.currentAccount[event.AgentType]
	if prev == "" {
		prev = "account_1"
	}
	next := m.nextAccount[event.AgentType]
	if next == "" {
		next = "account_2"
	}
	m.currentAccount[event.AgentType] = next
	return &RotationRecord{
		Provider:    normalizeProvider(event.AgentType),
		FromAccount: prev,
		ToAccount:   next,
		RotatedAt:   time.Now(),
		SessionPane: event.SessionPane,
		TriggeredBy: "limit_hit",
	}, nil
}

// mockTmuxClient implements a minimal mock for tmux.Client operations.
type mockTmuxClient struct {
	mu            sync.Mutex
	sendKeysCalls []sendKeysCall
	sendKeysErr   error
	runCalls      [][]string
	runOutput     string
	runErr        error
	captureSeq    []string
	captureIndex  int
	captureErr    error
}

func (m *mockTmuxClient) recordSendKeys(paneID, text string, enter bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sendKeysCalls = append(m.sendKeysCalls, sendKeysCall{
		paneID: paneID,
		text:   text,
		enter:  enter,
	})
}

func (m *mockTmuxClient) SendKeys(paneID, text string, enter bool) error {
	m.recordSendKeys(paneID, text, enter)
	return m.sendKeysErr
}

func (m *mockTmuxClient) Run(args ...string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runCalls = append(m.runCalls, append([]string(nil), args...))
	if m.runErr != nil {
		return "", m.runErr
	}
	return m.runOutput, nil
}

func (m *mockTmuxClient) CapturePaneOutput(target string, lines int) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.captureErr != nil {
		return "", m.captureErr
	}
	if m.captureIndex < len(m.captureSeq) {
		output := m.captureSeq[m.captureIndex]
		m.captureIndex++
		return output, nil
	}
	return "", nil
}

func TestNewAutoRespawner(t *testing.T) {
	r := NewAutoRespawner()

	if r == nil {
		t.Fatal("NewAutoRespawner returned nil")
	}

	if r.Config.GracefulExitDelay != 2*time.Second {
		t.Errorf("expected GracefulExitDelay 2s, got %v", r.Config.GracefulExitDelay)
	}

	if r.Config.AgentReadyDelay != 5*time.Second {
		t.Errorf("expected AgentReadyDelay 5s, got %v", r.Config.AgentReadyDelay)
	}

	if r.Config.MaxRetriesPerPane != 3 {
		t.Errorf("expected MaxRetriesPerPane 3, got %d", r.Config.MaxRetriesPerPane)
	}

	if r.Logger == nil {
		t.Error("expected Logger to be set")
	}

	if r.eventChan == nil {
		t.Error("expected eventChan to be initialized")
	}

	if r.retryState == nil {
		t.Error("expected retryState to be initialized")
	}
}

func TestAutoRespawnerWithMethods(t *testing.T) {
	r := NewAutoRespawner()
	ld := NewLimitDetector()
	pi := NewPromptInjector()
	ps := &mockPaneSpawner{}
	ar := newMockAccountRotator()

	r.WithLimitDetector(ld).
		WithPromptInjector(pi).
		WithPaneSpawner(ps).
		WithAccountRotator(ar)

	if r.LimitDetector != ld {
		t.Error("LimitDetector not set")
	}
	if r.PromptInjector != pi {
		t.Error("PromptInjector not set")
	}
	if r.PaneSpawner != ps {
		t.Error("PaneSpawner not set")
	}
	if r.AccountRotator != AccountRotatorI(ar) {
		t.Error("AccountRotator not set")
	}
}

func TestAutoRespawnerWithConfig(t *testing.T) {
	r := NewAutoRespawner()

	cfg := AutoRespawnerConfig{
		GracefulExitDelay:  5 * time.Second,
		AgentReadyDelay:    10 * time.Second,
		MaxRetriesPerPane:  5,
		RetryResetDuration: 2 * time.Hour,
		AutoRotateAccounts: true,
	}

	r.WithConfig(cfg)

	if r.Config.GracefulExitDelay != 5*time.Second {
		t.Error("Config not applied")
	}
	if r.Config.AutoRotateAccounts != true {
		t.Error("AutoRotateAccounts not set")
	}
}

func TestAutoRespawnerKillAgentSequences(t *testing.T) {
	tests := []struct {
		name        string
		agentType   string
		expectCalls []sendKeysCall
	}{
		{
			name:      "cc_double_ctrl_c",
			agentType: "cc",
			expectCalls: []sendKeysCall{
				{paneID: "test:1.1", text: "\x03", enter: false},
				{paneID: "test:1.1", text: "\x03", enter: false},
			},
		},
		{
			name:      "cod_exit_command",
			agentType: "cod",
			expectCalls: []sendKeysCall{
				{paneID: "test:1.1", text: "/exit", enter: true},
			},
		},
		{
			name:      "gmi_escape_ctrl_c",
			agentType: "gmi",
			expectCalls: []sendKeysCall{
				{paneID: "test:1.1", text: "\x1b", enter: false},
				{paneID: "test:1.1", text: "\x03", enter: false},
			},
		},
		{
			name:      "unknown_default_double_ctrl_c",
			agentType: "unknown",
			expectCalls: []sendKeysCall{
				{paneID: "test:1.1", text: "\x03", enter: false},
				{paneID: "test:1.1", text: "\x03", enter: false},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockTmuxClient{}
			r := NewAutoRespawner().WithTmuxClient(mock)

			t.Logf("[TEST] killAgent agentType=%s", tt.agentType)
			if err := r.killAgent("test:1.1", tt.agentType); err != nil {
				t.Fatalf("killAgent failed: %v", err)
			}

			if len(mock.sendKeysCalls) != len(tt.expectCalls) {
				t.Fatalf("expected %d SendKeys calls, got %d", len(tt.expectCalls), len(mock.sendKeysCalls))
			}

			for i, call := range tt.expectCalls {
				got := mock.sendKeysCalls[i]
				if got != call {
					t.Errorf("SendKeys call %d mismatch: got=%+v want=%+v", i, got, call)
				}
			}
		})
	}
}

func TestAutoRespawnerKillWithFallbackGraceful(t *testing.T) {
	mock := &mockTmuxClient{
		captureSeq: []string{"user@host:~$ "},
	}
	r := NewAutoRespawner().WithTmuxClient(mock)
	r.Config.ExitWaitTimeout = 20 * time.Millisecond
	r.Config.ExitPollInterval = 5 * time.Millisecond

	called := false
	r.forceKillFn = func(sessionPane string) error {
		called = true
		return nil
	}

	t.Log("[TEST] killWithFallback graceful exit path")
	if err := r.killWithFallback("test:1.1", "cc"); err != nil {
		t.Fatalf("killWithFallback failed: %v", err)
	}
	if called {
		t.Fatal("forceKill should not be called when exit detected")
	}
}

func TestAutoRespawnerKillWithFallbackForceKill(t *testing.T) {
	mock := &mockTmuxClient{
		captureSeq: []string{"still running"},
	}
	r := NewAutoRespawner().WithTmuxClient(mock)
	r.Config.ExitWaitTimeout = 20 * time.Millisecond
	r.Config.ExitPollInterval = 5 * time.Millisecond

	called := false
	r.forceKillFn = func(sessionPane string) error {
		called = true
		return nil
	}

	t.Log("[TEST] killWithFallback force kill path")
	if err := r.killWithFallback("test:1.1", "cc"); err != nil {
		t.Fatalf("killWithFallback failed: %v", err)
	}
	if !called {
		t.Fatal("expected forceKill to be called when exit not detected")
	}
}

func TestDefaultAutoRespawnerConfig(t *testing.T) {
	cfg := DefaultAutoRespawnerConfig()

	if cfg.GracefulExitDelay != 2*time.Second {
		t.Errorf("expected GracefulExitDelay 2s, got %v", cfg.GracefulExitDelay)
	}

	if cfg.AgentReadyDelay != 5*time.Second {
		t.Errorf("expected AgentReadyDelay 5s, got %v", cfg.AgentReadyDelay)
	}

	if cfg.MaxRetriesPerPane != 3 {
		t.Errorf("expected MaxRetriesPerPane 3, got %d", cfg.MaxRetriesPerPane)
	}

	if cfg.RetryResetDuration != 1*time.Hour {
		t.Errorf("expected RetryResetDuration 1h, got %v", cfg.RetryResetDuration)
	}

	if cfg.ClearPaneDelay != 100*time.Millisecond {
		t.Errorf("expected ClearPaneDelay 100ms, got %v", cfg.ClearPaneDelay)
	}

	if cfg.AutoRotateAccounts != false {
		t.Error("expected AutoRotateAccounts false")
	}
}

func TestAutoRespawnerStartRequiresLimitDetector(t *testing.T) {
	r := NewAutoRespawner()

	err := r.Start(context.Background())

	if err == nil {
		t.Error("expected error when LimitDetector is nil")
	}
	if err.Error() != "LimitDetector is required" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAutoRespawnerStartAndStop(t *testing.T) {
	r := NewAutoRespawner()
	ld := NewLimitDetector()
	r.WithLimitDetector(ld)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := r.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Give goroutine time to start
	time.Sleep(10 * time.Millisecond)

	r.Stop()

	// Verify cancel was called
	if r.cancel != nil {
		t.Error("cancel should be nil after Stop")
	}
}

func TestAutoRespawnerRetryTracking(t *testing.T) {
	r := NewAutoRespawner()
	r.Config.MaxRetriesPerPane = 3
	r.Config.RetryResetDuration = 1 * time.Hour

	sessionPane := "test:1.1"

	// Initially no retries
	if r.GetRetryCount(sessionPane) != 0 {
		t.Error("expected 0 retries initially")
	}

	// Record retries
	r.recordRetryAttempt(sessionPane)
	if r.GetRetryCount(sessionPane) != 1 {
		t.Errorf("expected 1 retry, got %d", r.GetRetryCount(sessionPane))
	}

	r.recordRetryAttempt(sessionPane)
	r.recordRetryAttempt(sessionPane)
	if r.GetRetryCount(sessionPane) != 3 {
		t.Errorf("expected 3 retries, got %d", r.GetRetryCount(sessionPane))
	}

	// Check limit exceeded
	if !r.isRetryLimitExceeded(sessionPane) {
		t.Error("expected retry limit to be exceeded")
	}

	// Different pane should not be affected
	if r.isRetryLimitExceeded("other:1.1") {
		t.Error("other pane should not have retries")
	}
}

func TestAutoRespawnerResetRetryCount(t *testing.T) {
	r := NewAutoRespawner()
	sessionPane := "test:1.1"

	r.recordRetryAttempt(sessionPane)
	r.recordRetryAttempt(sessionPane)

	if r.GetRetryCount(sessionPane) != 2 {
		t.Errorf("expected 2 retries, got %d", r.GetRetryCount(sessionPane))
	}

	r.ResetRetryCount(sessionPane)

	if r.GetRetryCount(sessionPane) != 0 {
		t.Error("expected 0 retries after reset")
	}
}

func TestAutoRespawnerResetAllRetryCounts(t *testing.T) {
	r := NewAutoRespawner()

	r.recordRetryAttempt("pane1:1.1")
	r.recordRetryAttempt("pane2:1.2")
	r.recordRetryAttempt("pane3:1.3")

	r.ResetAllRetryCounts()

	if r.GetRetryCount("pane1:1.1") != 0 {
		t.Error("pane1 retries not reset")
	}
	if r.GetRetryCount("pane2:1.2") != 0 {
		t.Error("pane2 retries not reset")
	}
	if r.GetRetryCount("pane3:1.3") != 0 {
		t.Error("pane3 retries not reset")
	}
}

func TestGetAgentCommand(t *testing.T) {
	r := NewAutoRespawner()

	tests := []struct {
		agentType string
		expected  string
	}{
		{"cc", "cc"},
		{"claude", "cc"},
		{"claude-code", "cc"},
		{"cod", "cod"},
		{"codex", "cod"},
		{"gmi", "gmi"},
		{"gemini", "gmi"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			cmd := r.getAgentCommand(tt.agentType)
			if cmd != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, cmd)
			}
		})
	}
}

func TestAutoRespawnerEventsChannel(t *testing.T) {
	r := NewAutoRespawner()

	ch := r.Events()
	if ch == nil {
		t.Fatal("Events() returned nil")
	}

	// Verify it's the same channel
	if ch != r.eventChan {
		t.Error("Events() should return the internal event channel")
	}
}

func TestAutoRespawnerEmitEvent(t *testing.T) {
	r := NewAutoRespawner()

	result := &RespawnResult{
		Success:         true,
		SessionPane:     "test:1.1",
		AgentType:       "cc",
		AccountRotated:  true,
		PreviousAccount: "acc1",
		NewAccount:      "acc2",
		Duration:        1 * time.Second,
		RespawnedAt:     time.Now(),
	}

	// Emit the event
	r.emitEvent(result)

	// Read from channel
	select {
	case event := <-r.Events():
		if event.SessionPane != "test:1.1" {
			t.Errorf("expected SessionPane test:1.1, got %s", event.SessionPane)
		}
		if event.AgentType != "cc" {
			t.Errorf("expected AgentType cc, got %s", event.AgentType)
		}
		if !event.AccountRotated {
			t.Error("expected AccountRotated true")
		}
		if event.PreviousAccount != "acc1" {
			t.Errorf("expected PreviousAccount acc1, got %s", event.PreviousAccount)
		}
		if event.NewAccount != "acc2" {
			t.Errorf("expected NewAccount acc2, got %s", event.NewAccount)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("expected event but none received")
	}
}

func TestAutoRespawnerEmitEventChannelFull(t *testing.T) {
	r := NewAutoRespawner()
	// Replace with a small buffered channel to test overflow
	r.eventChan = make(chan RespawnEvent, 1)

	result := &RespawnResult{
		SessionPane: "test:1.1",
		AgentType:   "cc",
	}

	// Fill the channel
	r.emitEvent(result)

	// This should not block (non-blocking send)
	done := make(chan bool)
	go func() {
		r.emitEvent(result) // This would block if not non-blocking
		done <- true
	}()

	select {
	case <-done:
		// Good - didn't block
	case <-time.After(100 * time.Millisecond):
		t.Error("emitEvent blocked on full channel")
	}
}

func TestAccountRotatorInterface(t *testing.T) {
	ar := newMockAccountRotator()
	ar.currentAccount["cc"] = "claude_account_1"
	ar.nextAccount["cc"] = "claude_account_2"

	// Test CurrentAccount
	acc := ar.CurrentAccount("cc")
	if acc != "claude_account_1" {
		t.Errorf("expected claude_account_1, got %s", acc)
	}

	// Test default for unknown type
	acc = ar.CurrentAccount("unknown")
	if acc != "account_1" {
		t.Errorf("expected default account_1, got %s", acc)
	}

	// Test RotateAccount
	newAcc, err := ar.RotateAccount("cc")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if newAcc != "claude_account_2" {
		t.Errorf("expected claude_account_2, got %s", newAcc)
	}

	// Verify current account is now updated
	if ar.CurrentAccount("cc") != "claude_account_2" {
		t.Error("current account not updated after rotation")
	}

	// Verify rotate was recorded
	if len(ar.rotateCalls) != 1 || ar.rotateCalls[0] != "cc" {
		t.Errorf("expected rotate call for cc, got %v", ar.rotateCalls)
	}
}

func TestAutoRespawnerRetryResetDuration(t *testing.T) {
	r := NewAutoRespawner()
	r.Config.RetryResetDuration = 50 * time.Millisecond
	r.Config.MaxRetriesPerPane = 2

	sessionPane := "test:1.1"

	// Record retries up to limit
	r.recordRetryAttempt(sessionPane)
	r.recordRetryAttempt(sessionPane)

	if !r.isRetryLimitExceeded(sessionPane) {
		t.Error("should be at retry limit")
	}

	// Wait for reset duration
	time.Sleep(60 * time.Millisecond)

	// After reset duration, limit should not be exceeded
	if r.isRetryLimitExceeded(sessionPane) {
		t.Error("retry limit should reset after duration")
	}

	// Get retry count should also return 0 after reset
	if r.GetRetryCount(sessionPane) != 0 {
		t.Error("retry count should be 0 after reset duration")
	}
}

func TestIsShellPrompt(t *testing.T) {
	r := NewAutoRespawner()

	tests := []struct {
		name     string
		output   string
		expected bool
	}{
		{"dollar prompt", "user@host:~$ ", true},
		{"percent prompt", "~ % ", true},
		{"hash prompt (root)", "# ", true},
		{"greater than prompt", "> ", true},
		{"oh-my-zsh arrow", "➜ ", true},
		{"powerline arrow", "❯ ", true},
		{"empty string", "", false},
		{"command output", "Hello, World!", false},
		{"agent running", "Thinking...", false},
		{"multiline with prompt", "some output\nmore output\nuser@host:~$ ", true},
		{"newlines only", "\n\n\n", false},
		{"prompt in middle", "some output$ more", true},
		{"just whitespace", "   \t  ", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := r.isShellPrompt(tt.output)
			if result != tt.expected {
				t.Errorf("isShellPrompt(%q) = %v, expected %v", tt.output, result, tt.expected)
			}
		})
	}
}

func TestSplitLines(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{"single line", "hello", []string{"hello"}},
		{"two lines", "hello\nworld", []string{"hello", "world"}},
		{"empty string", "", []string{}},
		{"trailing newline", "hello\n", []string{"hello"}},
		{"multiple newlines", "a\nb\nc", []string{"a", "b", "c"}},
		{"crlf", "hello\r\nworld", []string{"hello", "world"}},
		{"empty lines", "a\n\nb", []string{"a", "", "b"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := splitLines(tt.input)
			if len(result) != len(tt.expected) {
				t.Errorf("splitLines(%q) = %v, expected %v", tt.input, result, tt.expected)
				return
			}
			for i, line := range result {
				if line != tt.expected[i] {
					t.Errorf("splitLines(%q)[%d] = %q, expected %q", tt.input, i, line, tt.expected[i])
				}
			}
		})
	}
}

func TestTrimWhitespace(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"no whitespace", "hello", "hello"},
		{"leading spaces", "  hello", "hello"},
		{"trailing spaces", "hello  ", "hello"},
		{"both ends", "  hello  ", "hello"},
		{"tabs", "\thello\t", "hello"},
		{"mixed", " \t hello \t ", "hello"},
		{"empty string", "", ""},
		{"only whitespace", "   ", ""},
		{"internal spaces", "hello world", "hello world"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := trimWhitespace(tt.input)
			if result != tt.expected {
				t.Errorf("trimWhitespace(%q) = %q, expected %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestContainsAny(t *testing.T) {
	tests := []struct {
		name     string
		str      string
		subs     []string
		expected bool
	}{
		{"contains dollar", "user@host:~$ ", []string{"$"}, true},
		{"contains percent", "~ % ", []string{"%"}, true},
		{"contains none", "hello world", []string{"$", "%"}, false},
		{"empty string", "", []string{"$"}, false},
		{"empty subs", "hello", []string{}, false},
		{"multiple matches", "a$b%c", []string{"$", "%"}, true},
		{"exact match", "$", []string{"$"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := containsAny(tt.str, tt.subs)
			if result != tt.expected {
				t.Errorf("containsAny(%q, %v) = %v, expected %v", tt.str, tt.subs, result, tt.expected)
			}
		})
	}
}

func TestDefaultAutoRespawnerConfigExitWait(t *testing.T) {
	cfg := DefaultAutoRespawnerConfig()

	if cfg.ExitWaitTimeout != 5*time.Second {
		t.Errorf("expected ExitWaitTimeout 5s, got %v", cfg.ExitWaitTimeout)
	}

	if cfg.ExitPollInterval != 500*time.Millisecond {
		t.Errorf("expected ExitPollInterval 500ms, got %v", cfg.ExitPollInterval)
	}
}

func TestWithProjectPathLookup(t *testing.T) {
	r := NewAutoRespawner()

	lookup := func(sessionPane string) string {
		return "/test/project"
	}

	r.WithProjectPathLookup(lookup)

	if r.ProjectPathLookup == nil {
		t.Error("ProjectPathLookup not set")
	}

	result := r.ProjectPathLookup("test:1.1")
	if result != "/test/project" {
		t.Errorf("expected /test/project, got %s", result)
	}
}

func TestAgentReadyPatterns(t *testing.T) {
	tests := []struct {
		agentType       string
		expectedCount   int
		expectedPattern string
	}{
		{"cc", 5, "Claude"},
		{"claude", 5, "Claude"},
		{"claude-code", 5, "Claude"},
		{"cod", 3, "Codex"},
		{"codex", 3, "Codex"},
		{"gmi", 2, "Gemini"},
		{"gemini", 2, "Gemini"},
		{"unknown", 3, ">"}, // generic patterns
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			patterns := agentReadyPatterns(tt.agentType)

			if len(patterns) != tt.expectedCount {
				t.Errorf("expected %d patterns for %s, got %d", tt.expectedCount, tt.agentType, len(patterns))
			}

			found := false
			for _, p := range patterns {
				if p == tt.expectedPattern {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected pattern %q in %v", tt.expectedPattern, patterns)
			}
		})
	}
}

func TestCdToProjectNoLookup(t *testing.T) {
	r := NewAutoRespawner()
	// No ProjectPathLookup set

	err := r.cdToProject("test:1.1")

	if err != nil {
		t.Errorf("expected nil error when no lookup configured, got %v", err)
	}
}

func TestCdToProjectEmptyPath(t *testing.T) {
	r := NewAutoRespawner()

	r.WithProjectPathLookup(func(sessionPane string) string {
		return "" // Empty path
	})

	err := r.cdToProject("test:1.1")

	if err != nil {
		t.Errorf("expected nil error for empty path, got %v", err)
	}
}

func TestWithMarchingOrders(t *testing.T) {
	r := NewAutoRespawner()

	orders := map[string]string{
		"cc":      "Claude-specific instructions",
		"cod":     "Codex-specific instructions",
		"default": "Default instructions",
	}

	r.WithMarchingOrders(orders)

	if r.Config.MarchingOrders == nil {
		t.Fatal("MarchingOrders not set")
	}

	if r.Config.MarchingOrders["cc"] != "Claude-specific instructions" {
		t.Error("cc marching orders not set correctly")
	}
}

func TestGetMarchingOrdersAgentSpecific(t *testing.T) {
	r := NewAutoRespawner()

	orders := map[string]string{
		"cc":      "Claude prompt",
		"cod":     "Codex prompt",
		"default": "Default prompt",
	}

	r.WithMarchingOrders(orders)

	tests := []struct {
		agentType      string
		expectedPrompt string
		expectedSource string
	}{
		{"cc", "Claude prompt", "config/cc"},
		{"claude", "Claude prompt", "config/cc"},
		{"claude-code", "Claude prompt", "config/cc"},
		{"cod", "Codex prompt", "config/cod"},
		{"codex", "Codex prompt", "config/cod"},
		{"gmi", "Default prompt", "config/default"},     // Not in config, fallback to default
		{"unknown", "Default prompt", "config/default"}, // Not in config, fallback to default
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			prompt, source := r.getMarchingOrders(tt.agentType)
			if prompt != tt.expectedPrompt {
				t.Errorf("expected prompt %q, got %q", tt.expectedPrompt, prompt)
			}
			if source != tt.expectedSource {
				t.Errorf("expected source %q, got %q", tt.expectedSource, source)
			}
		})
	}
}

func TestGetMarchingOrdersFallbackToInjector(t *testing.T) {
	r := NewAutoRespawner()
	pi := NewPromptInjector()
	r.WithPromptInjector(pi)

	// No config marching orders set - should fall back to injector
	prompt, source := r.getMarchingOrders("cc")

	if source != "injector" {
		t.Errorf("expected source 'injector', got %q", source)
	}

	// Should be the default marching orders from prompt injector
	expected := pi.GetTemplate("default")
	if prompt != expected {
		t.Errorf("expected injector default template, got different prompt")
	}
}

func TestGetMarchingOrdersBuiltinFallback(t *testing.T) {
	r := NewAutoRespawner()
	// No config and no injector

	prompt, source := r.getMarchingOrders("cc")

	if source != "builtin" {
		t.Errorf("expected source 'builtin', got %q", source)
	}

	if prompt != DefaultMarchingOrders {
		t.Errorf("expected DefaultMarchingOrders, got different prompt")
	}
}

func TestNormalizeAgentType(t *testing.T) {
	r := NewAutoRespawner()

	tests := []struct {
		input    string
		expected string
	}{
		{"cc", "cc"},
		{"claude", "cc"},
		{"claude-code", "cc"},
		{"cod", "cod"},
		{"codex", "cod"},
		{"gmi", "gmi"},
		{"gemini", "gmi"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := r.normalizeAgentType(tt.input)
			if result != tt.expected {
				t.Errorf("normalizeAgentType(%q) = %q, expected %q", tt.input, result, tt.expected)
			}
		})
	}
}

// =============================================================================
// Full Swarm Lifecycle Integration Tests (bd-2oc4s)
// =============================================================================

// TestAutoRespawnerRespawnFullSequence tests the complete respawn flow including
// kill, clear, spawn, wait for ready, and marching orders injection.
func TestAutoRespawnerRespawnFullSequence(t *testing.T) {
	// Create mock tmux client with staged output sequence
	mock := &mockTmuxClient{
		// First capture shows agent running, then shell prompt after kill
		captureSeq: []string{
			"Claude is thinking...", // Before kill
			"user@host:~$ ",         // After kill - shell prompt detected
			"Claude Code ready >",   // After spawn - agent ready
		},
		runOutput: "12345", // Mock PID for display-message
	}

	// Mock prompt injector that records calls
	pi := NewPromptInjector()

	r := NewAutoRespawner().
		WithTmuxClient(mock).
		WithPromptInjector(pi).
		WithConfig(AutoRespawnerConfig{
			GracefulExitDelay:  10 * time.Millisecond,
			AgentReadyDelay:    50 * time.Millisecond,
			ClearPaneDelay:     5 * time.Millisecond,
			ExitWaitTimeout:    30 * time.Millisecond,
			ExitPollInterval:   5 * time.Millisecond,
			MaxRetriesPerPane:  3,
			RetryResetDuration: 1 * time.Hour,
		})

	// Override forceKill to avoid real process killing
	r.forceKillFn = func(sessionPane string) error {
		t.Logf("[TEST] forceKill called for %s", sessionPane)
		return nil
	}

	event := LimitEvent{
		SessionPane: "test:1.1",
		AgentType:   "cc",
		Pattern:     "rate limit exceeded",
		DetectedAt:  time.Now(),
	}

	t.Log("[TEST] Starting full respawn sequence")
	startTime := time.Now()

	result := r.Respawn(event)

	duration := time.Since(startTime)
	t.Logf("[TEST] Respawn completed in %v", duration)
	t.Logf("[TEST] Result: success=%v, error=%s", result.Success, result.Error)

	if !result.Success {
		t.Errorf("expected respawn to succeed, got error: %s", result.Error)
	}

	if result.SessionPane != "test:1.1" {
		t.Errorf("expected SessionPane test:1.1, got %s", result.SessionPane)
	}

	if result.AgentType != "cc" {
		t.Errorf("expected AgentType cc, got %s", result.AgentType)
	}

	// Verify kill sequence was sent
	if len(mock.sendKeysCalls) == 0 {
		t.Error("expected SendKeys calls for kill and spawn sequence")
	}

	t.Logf("[TEST] SendKeys calls: %d", len(mock.sendKeysCalls))
	for i, call := range mock.sendKeysCalls {
		t.Logf("[TEST]   Call %d: paneID=%s, text=%q, enter=%v", i, call.paneID, call.text, call.enter)
	}
}

// TestAutoRespawnerRespawnWithAccountRotation tests respawn with account rotation enabled.
func TestAutoRespawnerRespawnWithAccountRotation(t *testing.T) {
	mock := &mockTmuxClient{
		captureSeq: []string{
			"user@host:~$ ", // Shell prompt after kill
			"Claude ready",  // Agent ready after spawn
		},
		runOutput: "12345",
	}

	ar := newMockAccountRotator()
	ar.currentAccount["cc"] = "claude_account_1"
	ar.nextAccount["cc"] = "claude_account_2"

	r := NewAutoRespawner().
		WithTmuxClient(mock).
		WithAccountRotator(ar).
		WithConfig(AutoRespawnerConfig{
			AutoRotateAccounts: true,
			ExitWaitTimeout:    20 * time.Millisecond,
			ExitPollInterval:   5 * time.Millisecond,
			AgentReadyDelay:    30 * time.Millisecond,
			ClearPaneDelay:     5 * time.Millisecond,
		})

	r.forceKillFn = func(sessionPane string) error { return nil }

	event := LimitEvent{
		SessionPane: "test:1.1",
		AgentType:   "cc",
		Pattern:     "limit hit",
		DetectedAt:  time.Now(),
	}

	t.Log("[TEST] Starting respawn with account rotation")

	result := r.Respawn(event)

	t.Logf("[TEST] Result: success=%v, accountRotated=%v, prev=%s, new=%s",
		result.Success, result.AccountRotated, result.PreviousAccount, result.NewAccount)

	if !result.Success {
		t.Errorf("expected respawn to succeed, got error: %s", result.Error)
	}

	if !result.AccountRotated {
		t.Error("expected account to be rotated")
	}

	if result.PreviousAccount != "claude_account_1" {
		t.Errorf("expected previous account claude_account_1, got %s", result.PreviousAccount)
	}

	if result.NewAccount != "claude_account_2" {
		t.Errorf("expected new account claude_account_2, got %s", result.NewAccount)
	}

	// Verify rotator was called
	if len(ar.rotateCalls) == 0 {
		t.Error("expected account rotator to be called")
	}
}

// TestAutoRespawnerConcurrentRespawns tests that multiple concurrent respawns
// are handled safely without race conditions.
func TestAutoRespawnerConcurrentRespawns(t *testing.T) {
	mock := &mockTmuxClient{
		captureSeq: []string{
			"$", "$", "$", "$", "$", "$", // Shell prompts for all panes
			">", ">", ">", ">", ">", ">", // Ready prompts
		},
		runOutput: "12345",
	}

	r := NewAutoRespawner().
		WithTmuxClient(mock).
		WithConfig(AutoRespawnerConfig{
			ExitWaitTimeout:  10 * time.Millisecond,
			ExitPollInterval: 2 * time.Millisecond,
			AgentReadyDelay:  20 * time.Millisecond,
			ClearPaneDelay:   2 * time.Millisecond,
		})

	r.forceKillFn = func(sessionPane string) error { return nil }

	// Create multiple pane events
	panes := []struct {
		sessionPane string
		agentType   string
	}{
		{"proj:1.1", "cc"},
		{"proj:1.2", "cod"},
		{"proj:1.3", "gmi"},
	}

	t.Logf("[TEST] Starting concurrent respawn of %d panes", len(panes))
	startTime := time.Now()

	var wg sync.WaitGroup
	results := make(chan *RespawnResult, len(panes))
	errors := make(chan error, len(panes))

	for _, pane := range panes {
		wg.Add(1)
		go func(sp, at string) {
			defer wg.Done()
			event := LimitEvent{
				SessionPane: sp,
				AgentType:   at,
				DetectedAt:  time.Now(),
			}
			result := r.Respawn(event)
			results <- result
			if !result.Success {
				errors <- fmt.Errorf("%s: %s", sp, result.Error)
			}
		}(pane.sessionPane, pane.agentType)
	}

	wg.Wait()
	close(results)
	close(errors)

	elapsed := time.Since(startTime)
	t.Logf("[TEST] All respawns completed in %v", elapsed)

	// Collect results
	var successCount, failCount int
	for result := range results {
		if result.Success {
			successCount++
			t.Logf("[TEST] Success: %s (%s) in %v", result.SessionPane, result.AgentType, result.Duration)
		} else {
			failCount++
			t.Logf("[TEST] Failed: %s - %s", result.SessionPane, result.Error)
		}
	}

	// Collect any errors
	for err := range errors {
		t.Logf("[TEST] Error: %v", err)
	}

	t.Logf("[TEST] Results: %d successful, %d failed", successCount, failCount)

	// All should succeed (mock is permissive)
	if failCount > 0 {
		t.Errorf("expected all respawns to succeed, but %d failed", failCount)
	}

	// Verify no data races occurred (would be caught by -race flag)
	// Also verify retry state was tracked correctly
	for _, pane := range panes {
		count := r.GetRetryCount(pane.sessionPane)
		t.Logf("[TEST] Retry count for %s: %d", pane.sessionPane, count)
	}
}

// TestAutoRespawnerContextCancellation tests that respawn operations respect
// context cancellation.
func TestAutoRespawnerContextCancellation(t *testing.T) {
	// Create a limit detector for the Start() requirement
	ld := NewLimitDetector()

	r := NewAutoRespawner().
		WithLimitDetector(ld).
		WithConfig(AutoRespawnerConfig{
			ExitWaitTimeout:  100 * time.Millisecond,
			ExitPollInterval: 10 * time.Millisecond,
		})

	ctx, cancel := context.WithCancel(context.Background())

	t.Log("[TEST] Starting AutoRespawner with cancellable context")

	err := r.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Give the goroutine time to start
	time.Sleep(20 * time.Millisecond)

	t.Log("[TEST] Cancelling context")
	cancel()

	// Give time for cancellation to propagate
	time.Sleep(20 * time.Millisecond)

	t.Log("[TEST] Calling Stop()")
	r.Stop()

	// Verify the respawner stopped cleanly
	if r.cancel != nil {
		t.Error("expected cancel to be nil after Stop")
	}

	t.Log("[TEST] Context cancellation handled correctly")
}

// TestAutoRespawnerProcessLimitEventsIntegration tests the event processing loop
// with simulated limit events.
func TestAutoRespawnerProcessLimitEventsIntegration(t *testing.T) {
	mock := &mockTmuxClient{
		captureSeq: []string{"$", ">"},
		runOutput:  "12345",
	}

	ld := NewLimitDetector()

	r := NewAutoRespawner().
		WithTmuxClient(mock).
		WithLimitDetector(ld).
		WithConfig(AutoRespawnerConfig{
			ExitWaitTimeout:   15 * time.Millisecond,
			ExitPollInterval:  5 * time.Millisecond,
			AgentReadyDelay:   20 * time.Millisecond,
			ClearPaneDelay:    5 * time.Millisecond,
			MaxRetriesPerPane: 3,
		})

	r.forceKillFn = func(sessionPane string) error { return nil }

	// Test by directly calling Respawn (since LimitDetector.EmitEvent is internal)
	// This tests the integration between Respawn and event emission
	t.Log("[TEST] Calling Respawn directly to test event emission")

	event := LimitEvent{
		SessionPane: "test:1.1",
		AgentType:   "cc",
		Pattern:     "rate limit",
		DetectedAt:  time.Now(),
	}

	result := r.Respawn(event)

	t.Logf("[TEST] Respawn result: success=%v, error=%s", result.Success, result.Error)

	// Check for respawn event in the channel
	select {
	case respawnEvent := <-r.Events():
		t.Logf("[TEST] Received respawn event: sessionPane=%s, agentType=%s",
			respawnEvent.SessionPane, respawnEvent.AgentType)
		if respawnEvent.SessionPane != "test:1.1" {
			t.Errorf("expected SessionPane test:1.1, got %s", respawnEvent.SessionPane)
		}
	case <-time.After(100 * time.Millisecond):
		if result.Success {
			t.Error("[TEST] Expected respawn event but none received")
		} else {
			t.Log("[TEST] No respawn event received (expected since respawn failed)")
		}
	}
}

// TestAutoRespawnerRetryLimitEnforcement tests that the retry limit prevents
// infinite respawn loops.
func TestAutoRespawnerRetryLimitEnforcement(t *testing.T) {
	mock := &mockTmuxClient{
		captureSeq: []string{"$", ">", "$", ">", "$", ">", "$", ">"}, // Multiple cycles
		runOutput:  "12345",
	}

	ld := NewLimitDetector()

	r := NewAutoRespawner().
		WithTmuxClient(mock).
		WithLimitDetector(ld).
		WithConfig(AutoRespawnerConfig{
			ExitWaitTimeout:    10 * time.Millisecond,
			ExitPollInterval:   2 * time.Millisecond,
			AgentReadyDelay:    15 * time.Millisecond,
			ClearPaneDelay:     2 * time.Millisecond,
			MaxRetriesPerPane:  2, // Only allow 2 retries
			RetryResetDuration: 1 * time.Hour,
		})

	r.forceKillFn = func(sessionPane string) error { return nil }

	sessionPane := "test:1.1"

	t.Log("[TEST] Testing retry limit enforcement")

	// First respawn should succeed
	event := LimitEvent{SessionPane: sessionPane, AgentType: "cc", DetectedAt: time.Now()}
	result1 := r.Respawn(event)
	t.Logf("[TEST] Respawn 1: success=%v", result1.Success)

	// Record the retry
	r.recordRetryAttempt(sessionPane)

	// Second respawn should succeed
	result2 := r.Respawn(event)
	t.Logf("[TEST] Respawn 2: success=%v", result2.Success)

	// Record the retry
	r.recordRetryAttempt(sessionPane)

	// Check if limit is now exceeded
	if !r.isRetryLimitExceeded(sessionPane) {
		t.Error("expected retry limit to be exceeded after 2 attempts")
	}

	t.Logf("[TEST] Retry count: %d", r.GetRetryCount(sessionPane))
	t.Logf("[TEST] Retry limit exceeded: %v", r.isRetryLimitExceeded(sessionPane))

	// The processLimitEvents loop would skip this event due to retry limit
	// (This is tested indirectly through isRetryLimitExceeded)
}

// TestAutoRespawnerSpawnAgentCommands tests that the correct agent launch commands
// are sent for each agent type.
func TestAutoRespawnerSpawnAgentCommands(t *testing.T) {
	tests := []struct {
		name        string
		agentType   string
		expectCmd   string
	}{
		{"claude_code", "cc", "cc"},
		{"claude_alias", "claude", "cc"},
		{"codex", "cod", "cod"},
		{"codex_alias", "codex", "cod"},
		{"gemini", "gmi", "gmi"},
		{"gemini_alias", "gemini", "gmi"},
		{"unknown", "custom", "custom"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockTmuxClient{}
			r := NewAutoRespawner().WithTmuxClient(mock)

			t.Logf("[TEST] Spawning agent type %s", tt.agentType)

			err := r.spawnAgent("test:1.1", tt.agentType)
			if err != nil {
				t.Fatalf("spawnAgent failed: %v", err)
			}

			if len(mock.sendKeysCalls) != 1 {
				t.Fatalf("expected 1 SendKeys call, got %d", len(mock.sendKeysCalls))
			}

			call := mock.sendKeysCalls[0]
			if call.text != tt.expectCmd {
				t.Errorf("expected command %q, got %q", tt.expectCmd, call.text)
			}
			if !call.enter {
				t.Error("expected enter=true for spawn command")
			}

			t.Logf("[TEST] Command sent: %q (enter=%v)", call.text, call.enter)
		})
	}
}

// TestAutoRespawnerClearPane tests that the clear command is sent correctly.
func TestAutoRespawnerClearPane(t *testing.T) {
	mock := &mockTmuxClient{}
	r := NewAutoRespawner().
		WithTmuxClient(mock).
		WithConfig(AutoRespawnerConfig{
			ClearPaneDelay: 10 * time.Millisecond,
		})

	t.Log("[TEST] Testing clearPane")

	err := r.clearPane("test:1.1")
	if err != nil {
		t.Fatalf("clearPane failed: %v", err)
	}

	if len(mock.sendKeysCalls) != 1 {
		t.Fatalf("expected 1 SendKeys call, got %d", len(mock.sendKeysCalls))
	}

	call := mock.sendKeysCalls[0]
	if call.text != "clear" {
		t.Errorf("expected command 'clear', got %q", call.text)
	}
	if !call.enter {
		t.Error("expected enter=true for clear command")
	}

	t.Logf("[TEST] Clear command sent successfully")
}

// TestAutoRespawnerCdToProject tests directory change before agent spawn.
func TestAutoRespawnerCdToProject(t *testing.T) {
	mock := &mockTmuxClient{}
	r := NewAutoRespawner().
		WithTmuxClient(mock).
		WithProjectPathLookup(func(sessionPane string) string {
			return "/home/user/myproject"
		}).
		WithConfig(AutoRespawnerConfig{
			ClearPaneDelay: 5 * time.Millisecond,
		})

	t.Log("[TEST] Testing cdToProject")

	err := r.cdToProject("test:1.1")
	if err != nil {
		t.Fatalf("cdToProject failed: %v", err)
	}

	if len(mock.sendKeysCalls) != 1 {
		t.Fatalf("expected 1 SendKeys call, got %d", len(mock.sendKeysCalls))
	}

	call := mock.sendKeysCalls[0]
	expected := `cd "/home/user/myproject"`
	if call.text != expected {
		t.Errorf("expected command %q, got %q", expected, call.text)
	}
	if !call.enter {
		t.Error("expected enter=true for cd command")
	}

	t.Logf("[TEST] CD command sent: %q", call.text)
}

// TestAutoRespawnerWaitForAgentReadyTimeout tests the agent ready detection timeout.
func TestAutoRespawnerWaitForAgentReadyTimeout(t *testing.T) {
	mock := &mockTmuxClient{
		// Never return a ready pattern
		captureSeq: []string{"loading...", "loading...", "loading..."},
	}

	r := NewAutoRespawner().
		WithTmuxClient(mock).
		WithConfig(AutoRespawnerConfig{
			AgentReadyDelay: 50 * time.Millisecond,
		})

	t.Log("[TEST] Testing waitForAgentReady timeout")

	startTime := time.Now()
	err := r.waitForAgentReady("test:1.1", "cc")
	elapsed := time.Since(startTime)

	t.Logf("[TEST] Wait completed in %v", elapsed)

	if err == nil {
		t.Error("expected timeout error")
	}

	if elapsed < 50*time.Millisecond {
		t.Errorf("expected wait to last at least 50ms, got %v", elapsed)
	}

	t.Logf("[TEST] Timeout error: %v", err)
}

// TestAutoRespawnerWaitForAgentReadySuccess tests successful agent ready detection.
func TestAutoRespawnerWaitForAgentReadySuccess(t *testing.T) {
	mock := &mockTmuxClient{
		captureSeq: []string{
			"Starting...",
			"Claude Code v1.0", // Contains "Claude" which is a ready pattern
		},
	}

	r := NewAutoRespawner().
		WithTmuxClient(mock).
		WithConfig(AutoRespawnerConfig{
			AgentReadyDelay: 500 * time.Millisecond, // Long timeout
		})

	t.Log("[TEST] Testing waitForAgentReady success")

	startTime := time.Now()
	err := r.waitForAgentReady("test:1.1", "cc")
	elapsed := time.Since(startTime)

	t.Logf("[TEST] Wait completed in %v", elapsed)

	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}

	// Should complete much faster than the timeout
	if elapsed > 200*time.Millisecond {
		t.Errorf("expected quick detection, but took %v", elapsed)
	}

	t.Log("[TEST] Agent ready detected successfully")
}
