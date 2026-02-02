package resilience

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/health"
)

// saveHooks saves all original hooks and returns a restore function.
// Uses hooksMu to synchronize with spawned goroutines that read hooks.
func saveHooks() func() {
	hooksMu.Lock()
	origSend := sendKeysFn
	origBuild := buildPaneCmdFn
	origSleep := sleepFn
	origCheckSession := checkSessionFn
	origDisplayMessage := displayMessageFn
	hooksMu.Unlock()

	return func() {
		hooksMu.Lock()
		sendKeysFn = origSend
		buildPaneCmdFn = origBuild
		sleepFn = origSleep
		checkSessionFn = origCheckSession
		displayMessageFn = origDisplayMessage
		hooksMu.Unlock()
	}
}

// setHooksLocked executes the provided function under hooksMu write lock.
// Use this to safely set hook functions in tests.
func setHooksLocked(fn func()) {
	hooksMu.Lock()
	defer hooksMu.Unlock()
	fn()
}

func TestRestartAgentUsesBuiltPaneCommandAndSendKeys(t *testing.T) {
	restore := saveHooks()
	defer restore()

	// Stub functions under lock for thread safety
	var mu sync.Mutex
	var capturedCmd string
	setHooksLocked(func() {
		sendKeysFn = func(paneID, cmd string, enter bool) error {
			mu.Lock()
			defer mu.Unlock()
			capturedCmd = cmd
			if paneID != "pane-1" {
				t.Fatalf("unexpected pane id: %s", paneID)
			}
			if !enter {
				t.Fatalf("expected enter=true")
			}
			return nil
		}
		buildPaneCmdFn = func(projectDir, agentCmd string) (string, error) {
			if projectDir != "/tmp/project with space" {
				return "", fmt.Errorf("unexpected dir: %s", projectDir)
			}
			return fmt.Sprintf("cd %q && %s", projectDir, agentCmd), nil
		}
		sleepFn = func(d time.Duration) {} // no-op for speed
	})

	cfg := config.Default()
	cfg.Resilience.AutoRestart = true
	cfg.Resilience.RestartDelaySeconds = 0

	m := NewMonitor("sess", "/tmp/project with space", cfg, true)
	m.agents["pane-1"] = &AgentState{
		PaneID:    "pane-1",
		PaneIndex: 1,
		AgentType: "cc",
		Command:   "claude --model 'safe-model'",
		Healthy:   false,
	}

	m.restartAgent(context.Background(), m.agents["pane-1"])

	mu.Lock()
	defer mu.Unlock()
	if capturedCmd == "" {
		t.Fatalf("sendKeys was not invoked")
	}
	if capturedCmd != "cd \"/tmp/project with space\" && claude --model 'safe-model'" {
		t.Fatalf("unexpected command sent: %s", capturedCmd)
	}
}

func TestRegisterAgent(t *testing.T) {
	cfg := config.Default()
	m := NewMonitor("test-session", "/tmp/project", cfg, true)

	m.RegisterAgent("pane-1", 1, "cc", "opus", "claude --model opus")
	m.RegisterAgent("pane-2", 2, "gmi", "pro", "gemini --model pro")

	states := m.GetAgentStates()
	if len(states) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(states))
	}

	agent1, ok := states["pane-1"]
	if !ok {
		t.Fatal("pane-1 not found")
	}
	if agent1.AgentType != "cc" {
		t.Errorf("expected agent type 'cc', got %s", agent1.AgentType)
	}
	if agent1.Model != "opus" {
		t.Errorf("expected model 'opus', got %s", agent1.Model)
	}
	if agent1.Command != "claude --model opus" {
		t.Errorf("unexpected command: %s", agent1.Command)
	}
	if !agent1.Healthy {
		t.Error("new agent should be healthy")
	}

	agent2, ok := states["pane-2"]
	if !ok {
		t.Fatal("pane-2 not found")
	}
	if agent2.AgentType != "gmi" {
		t.Errorf("expected agent type 'gmi', got %s", agent2.AgentType)
	}
}

func TestGetRestartCount(t *testing.T) {
	cfg := config.Default()
	m := NewMonitor("test-session", "/tmp/project", cfg, true)

	// Non-existent agent should return 0
	if count := m.GetRestartCount("nonexistent"); count != 0 {
		t.Errorf("expected 0 for nonexistent, got %d", count)
	}

	m.RegisterAgent("pane-1", 1, "cc", "opus", "claude")

	// Initial restart count should be 0
	if count := m.GetRestartCount("pane-1"); count != 0 {
		t.Errorf("expected 0, got %d", count)
	}

	// Manually increment to test getter
	m.mu.Lock()
	m.agents["pane-1"].RestartCount = 3
	m.mu.Unlock()

	if count := m.GetRestartCount("pane-1"); count != 3 {
		t.Errorf("expected 3, got %d", count)
	}
}

func TestGetAgentStatesReturnsCopy(t *testing.T) {
	cfg := config.Default()
	m := NewMonitor("test-session", "/tmp/project", cfg, true)

	m.RegisterAgent("pane-1", 1, "cc", "opus", "claude")

	states := m.GetAgentStates()
	// Modify the copy
	states["pane-1"] = AgentState{PaneID: "modified"}

	// Original should be unchanged
	original := m.GetAgentStates()
	if original["pane-1"].PaneID != "pane-1" {
		t.Error("GetAgentStates should return a copy, not the original")
	}
}

func TestStartAndStop(t *testing.T) {
	restore := saveHooks()
	defer restore()

	cfg := config.Default()
	cfg.Resilience.HealthCheckSeconds = 1 // Fast for testing

	// Mock checkSessionFn to avoid actual tmux calls
	setHooksLocked(func() {
		checkSessionFn = func(session string) (*health.SessionHealth, error) {
			return &health.SessionHealth{
				Session: session,
				Agents:  []health.AgentHealth{},
			}, nil
		}
	})

	m := NewMonitor("test-session", "/tmp/project", cfg, true)

	ctx := context.Background()
	m.Start(ctx)

	// Give monitor a moment to start
	time.Sleep(50 * time.Millisecond)

	// Stop should not hang
	done := make(chan struct{})
	go func() {
		m.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() timed out")
	}
}

func TestStopWithoutStart(t *testing.T) {
	cfg := config.Default()
	m := NewMonitor("test-session", "/tmp/project", cfg, true)

	// Should not panic or hang
	done := make(chan struct{})
	go func() {
		m.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(time.Second):
		t.Fatal("Stop() without Start() should return immediately")
	}
}

func TestCheckHealthWithHealthyAgent(t *testing.T) {
	restore := saveHooks()
	defer restore()

	setHooksLocked(func() {
		checkSessionFn = func(session string) (*health.SessionHealth, error) {
			return &health.SessionHealth{
				Session: session,
				Agents: []health.AgentHealth{
					{
						PaneID:        "pane-1",
						Status:        health.StatusOK,
						ProcessStatus: health.ProcessRunning,
					},
				},
			}, nil
		}
	})

	cfg := config.Default()
	m := NewMonitor("test-session", "/tmp/project", cfg, true)
	m.RegisterAgent("pane-1", 1, "cc", "opus", "claude")

	// Mark as unhealthy first
	m.mu.Lock()
	m.agents["pane-1"].Healthy = false
	m.mu.Unlock()

	m.checkHealth(context.Background())

	// Should be marked healthy again
	m.mu.RLock()
	healthy := m.agents["pane-1"].Healthy
	m.mu.RUnlock()

	if !healthy {
		t.Error("agent should be marked healthy after OK status")
	}
}

func TestCheckHealthDetectsCrash(t *testing.T) {
	restore := saveHooks()
	defer restore()

	setHooksLocked(func() {
		checkSessionFn = func(session string) (*health.SessionHealth, error) {
			return &health.SessionHealth{
				Session: session,
				Agents: []health.AgentHealth{
					{
						PaneID:        "pane-1",
						Status:        health.StatusError,
						ProcessStatus: health.ProcessExited,
						Issues:        []health.Issue{{Type: "crash", Message: "Process exited"}},
					},
				},
			}, nil
		}

		// Don't actually restart
		sleepFn = func(d time.Duration) {}
		sendKeysFn = func(paneID, cmd string, enter bool) error { return nil }
		buildPaneCmdFn = func(projectDir, agentCmd string) (string, error) {
			return agentCmd, nil
		}
	})

	cfg := config.Default()
	cfg.Resilience.AutoRestart = true
	cfg.Resilience.MaxRestarts = 3
	cfg.Resilience.RestartDelaySeconds = 0

	m := NewMonitor("test-session", "/tmp/project", cfg, true)
	m.RegisterAgent("pane-1", 1, "cc", "opus", "claude")

	m.checkHealth(context.Background())

	// Give async restart goroutine time to run
	time.Sleep(50 * time.Millisecond)

	m.mu.RLock()
	agent := m.agents["pane-1"]
	wasUnhealthy := !agent.Healthy || agent.RestartCount > 0
	m.mu.RUnlock()

	// Either marked unhealthy or restart attempted
	if !wasUnhealthy && agent.RestartCount == 0 {
		t.Error("agent crash should have been handled")
	}
}

func TestCheckHealthDetectsPaneMissing(t *testing.T) {
	restore := saveHooks()
	defer restore()

	setHooksLocked(func() {
		// Return empty agents list - pane doesn't exist
		checkSessionFn = func(session string) (*health.SessionHealth, error) {
			return &health.SessionHealth{
				Session: session,
				Agents:  []health.AgentHealth{},
			}, nil
		}

		sleepFn = func(d time.Duration) {}
		sendKeysFn = func(paneID, cmd string, enter bool) error { return nil }
		buildPaneCmdFn = func(projectDir, agentCmd string) (string, error) {
			return agentCmd, nil
		}
	})

	cfg := config.Default()
	cfg.Resilience.AutoRestart = true
	cfg.Resilience.MaxRestarts = 3
	cfg.Resilience.RestartDelaySeconds = 0

	m := NewMonitor("test-session", "/tmp/project", cfg, true)
	m.RegisterAgent("pane-1", 1, "cc", "opus", "claude")

	m.checkHealth(context.Background())

	// Give async restart goroutine time to run
	time.Sleep(50 * time.Millisecond)

	m.mu.RLock()
	restartCount := m.agents["pane-1"].RestartCount
	m.mu.RUnlock()

	// When pane is missing, it triggers a crash which triggers a restart
	if restartCount != 1 {
		t.Errorf("expected restart count 1 when pane missing, got %d", restartCount)
	}
}

func TestCheckHealthDetectsRateLimit(t *testing.T) {
	restore := saveHooks()
	defer restore()

	setHooksLocked(func() {
		checkSessionFn = func(session string) (*health.SessionHealth, error) {
			return &health.SessionHealth{
				Session: session,
				Agents: []health.AgentHealth{
					{
						PaneID:        "pane-1",
						Status:        health.StatusWarning,
						ProcessStatus: health.ProcessRunning,
						RateLimited:   true,
						WaitSeconds:   60,
					},
				},
			}, nil
		}

		displayMessageFn = func(session, msg string, durationMs int) error {
			return nil
		}
	})

	cfg := config.Default()
	cfg.Resilience.RateLimit.Detect = true
	m := NewMonitor("test-session", "/tmp/project", cfg, true)
	m.RegisterAgent("pane-1", 1, "cc", "opus", "claude")

	m.checkHealth(context.Background())

	// Give async goroutine time to run
	time.Sleep(50 * time.Millisecond)

	m.mu.RLock()
	rateLimited := m.agents["pane-1"].RateLimited
	waitSeconds := m.agents["pane-1"].WaitSeconds
	m.mu.RUnlock()

	if !rateLimited {
		t.Error("agent should be marked as rate limited")
	}
	if waitSeconds != 60 {
		t.Errorf("expected wait seconds 60, got %d", waitSeconds)
	}
}

func TestCheckHealthRateLimitUpdatesTracker(t *testing.T) {
	restore := saveHooks()
	defer restore()

	setHooksLocked(func() {
		checkSessionFn = func(session string) (*health.SessionHealth, error) {
			return &health.SessionHealth{
				Session: session,
				Agents: []health.AgentHealth{
					{
						PaneID:        "pane-1",
						Status:        health.StatusWarning,
						ProcessStatus: health.ProcessRunning,
						RateLimited:   true,
						WaitSeconds:   45,
					},
				},
			}, nil
		}
	})

	cfg := config.Default()
	cfg.Resilience.RateLimit.Detect = true
	projectDir := t.TempDir()
	m := NewMonitor("test-session", projectDir, cfg, true)
	m.RegisterAgent("pane-1", 1, "cod", "gpt-4", "codex")

	m.checkHealth(context.Background())

	state := m.rateLimitTracker.GetProviderState("openai")
	if state == nil {
		t.Fatal("expected rate limit tracker state for openai provider")
	}
	if state.TotalRateLimits != 1 {
		t.Errorf("expected TotalRateLimits=1, got %d", state.TotalRateLimits)
	}
	if state.CooldownUntil.IsZero() {
		t.Error("expected cooldown window to be set")
	}
}

func TestCheckHealthRateLimitCleared(t *testing.T) {
	restore := saveHooks()
	defer restore()

	setHooksLocked(func() {
		checkSessionFn = func(session string) (*health.SessionHealth, error) {
			return &health.SessionHealth{
				Session: session,
				Agents: []health.AgentHealth{
					{
						PaneID:        "pane-1",
						Status:        health.StatusOK,
						ProcessStatus: health.ProcessRunning,
						RateLimited:   false,
					},
				},
			}, nil
		}
	})

	cfg := config.Default()
	m := NewMonitor("test-session", "/tmp/project", cfg, true)
	m.RegisterAgent("pane-1", 1, "cc", "opus", "claude")

	// Pre-set as rate limited
	m.mu.Lock()
	m.agents["pane-1"].RateLimited = true
	m.agents["pane-1"].WaitSeconds = 60
	m.mu.Unlock()

	m.checkHealth(context.Background())

	m.mu.RLock()
	rateLimited := m.agents["pane-1"].RateLimited
	waitSeconds := m.agents["pane-1"].WaitSeconds
	m.mu.RUnlock()

	if rateLimited {
		t.Error("rate limit should be cleared")
	}
	if waitSeconds != 0 {
		t.Errorf("wait seconds should be 0, got %d", waitSeconds)
	}
}

func TestCheckHealthRateLimitClearedRecordsSuccess(t *testing.T) {
	restore := saveHooks()
	defer restore()

	setHooksLocked(func() {
		checkSessionFn = func(session string) (*health.SessionHealth, error) {
			return &health.SessionHealth{
				Session: session,
				Agents: []health.AgentHealth{
					{
						PaneID:        "pane-1",
						Status:        health.StatusOK,
						ProcessStatus: health.ProcessRunning,
						RateLimited:   false,
					},
				},
			}, nil
		}
	})

	cfg := config.Default()
	cfg.Resilience.RateLimit.Detect = true
	projectDir := t.TempDir()
	m := NewMonitor("test-session", projectDir, cfg, true)
	m.RegisterAgent("pane-1", 1, "cod", "gpt-4", "codex")

	m.mu.Lock()
	m.agents["pane-1"].RateLimited = true
	m.mu.Unlock()

	m.checkHealth(context.Background())

	state := m.rateLimitTracker.GetProviderState("openai")
	if state == nil {
		t.Fatal("expected rate limit tracker state for openai provider")
	}
	if state.TotalSuccesses != 1 {
		t.Errorf("expected TotalSuccesses=1, got %d", state.TotalSuccesses)
	}
}

func TestCheckHealthError(t *testing.T) {
	restore := saveHooks()
	defer restore()

	setHooksLocked(func() {
		checkSessionFn = func(session string) (*health.SessionHealth, error) {
			return nil, fmt.Errorf("session check failed")
		}
	})

	cfg := config.Default()
	m := NewMonitor("test-session", "/tmp/project", cfg, true)
	m.RegisterAgent("pane-1", 1, "cc", "opus", "claude")

	// Should not panic
	m.checkHealth(context.Background())

	// Agent should remain unchanged
	m.mu.RLock()
	healthy := m.agents["pane-1"].Healthy
	m.mu.RUnlock()

	if !healthy {
		t.Error("agent should remain healthy on check error")
	}
}

func TestHandleCrashMaxRestartsExceeded(t *testing.T) {
	restore := saveHooks()
	defer restore()

	var restartAttempted bool
	setHooksLocked(func() {
		sendKeysFn = func(paneID, cmd string, enter bool) error {
			restartAttempted = true
			return nil
		}
		sleepFn = func(d time.Duration) {}
		buildPaneCmdFn = func(projectDir, agentCmd string) (string, error) {
			return agentCmd, nil
		}
	})

	cfg := config.Default()
	cfg.Resilience.MaxRestarts = 3
	m := NewMonitor("test-session", "/tmp/project", cfg, true)
	m.RegisterAgent("pane-1", 1, "cc", "opus", "claude")

	// Set restart count at max
	m.mu.Lock()
	m.agents["pane-1"].RestartCount = 3
	m.mu.Unlock()

	m.handleCrash(context.Background(), m.agents["pane-1"], "test crash")

	// Give time for any goroutine
	time.Sleep(50 * time.Millisecond)

	if restartAttempted {
		t.Error("should not restart when max restarts exceeded")
	}
}

func TestHandleCrashSuggestsManualRespawnWhenAutoRestartDisabled(t *testing.T) {
	restore := saveHooks()
	defer restore()

	var capturedSession, capturedMsg string
	setHooksLocked(func() {
		displayMessageFn = func(session, msg string, durationMs int) error {
			capturedSession = session
			capturedMsg = msg
			return nil
		}
	})

	cfg := config.Default()
	cfg.Resilience.AutoRestart = false

	m := NewMonitor("test-session", "/tmp/project", cfg, false)
	m.RegisterAgent("pane-1", 1, "cc", "opus", "claude")

	m.handleCrash(context.Background(), m.agents["pane-1"], "test crash")

	if capturedSession != "test-session" {
		t.Fatalf("expected session 'test-session', got %s", capturedSession)
	}
	if !strings.Contains(capturedMsg, "ntm respawn test-session --panes=1") {
		t.Fatalf("expected respawn hint in message, got %q", capturedMsg)
	}
}

func TestRestartAgentIncreasesCount(t *testing.T) {
	restore := saveHooks()
	defer restore()

	setHooksLocked(func() {
		sleepFn = func(d time.Duration) {}
		sendKeysFn = func(paneID, cmd string, enter bool) error { return nil }
		buildPaneCmdFn = func(projectDir, agentCmd string) (string, error) {
			return agentCmd, nil
		}
	})

	cfg := config.Default()
	cfg.Resilience.RestartDelaySeconds = 0
	m := NewMonitor("test-session", "/tmp/project", cfg, true)
	m.RegisterAgent("pane-1", 1, "cc", "opus", "claude")

	m.mu.Lock()
	m.agents["pane-1"].Healthy = false
	m.mu.Unlock()

	m.restartAgent(context.Background(), m.agents["pane-1"])

	m.mu.RLock()
	count := m.agents["pane-1"].RestartCount
	healthy := m.agents["pane-1"].Healthy
	m.mu.RUnlock()

	if count != 1 {
		t.Errorf("expected restart count 1, got %d", count)
	}
	if !healthy {
		t.Error("agent should be marked healthy after restart")
	}
}

func TestRestartAgentSkipsIfHealthy(t *testing.T) {
	restore := saveHooks()
	defer restore()

	var sendKeysCalled bool
	setHooksLocked(func() {
		sleepFn = func(d time.Duration) {}
		sendKeysFn = func(paneID, cmd string, enter bool) error {
			sendKeysCalled = true
			return nil
		}
		buildPaneCmdFn = func(projectDir, agentCmd string) (string, error) {
			return agentCmd, nil
		}
	})

	cfg := config.Default()
	cfg.Resilience.RestartDelaySeconds = 0
	m := NewMonitor("test-session", "/tmp/project", cfg, true)
	m.RegisterAgent("pane-1", 1, "cc", "opus", "claude")

	// Agent is healthy by default
	m.restartAgent(context.Background(), m.agents["pane-1"])

	if sendKeysCalled {
		t.Error("should not restart healthy agent")
	}
}

func TestRestartAgentHandlesBuildError(t *testing.T) {
	restore := saveHooks()
	defer restore()

	var sendKeysCalled bool
	setHooksLocked(func() {
		sleepFn = func(d time.Duration) {}
		buildPaneCmdFn = func(projectDir, agentCmd string) (string, error) {
			return "", fmt.Errorf("build error")
		}
		sendKeysFn = func(paneID, cmd string, enter bool) error {
			sendKeysCalled = true
			return nil
		}
	})

	cfg := config.Default()
	cfg.Resilience.RestartDelaySeconds = 0

	m := NewMonitor("test-session", "/tmp/project", cfg, true)
	m.RegisterAgent("pane-1", 1, "cc", "opus", "claude")

	m.mu.Lock()
	m.agents["pane-1"].Healthy = false
	m.mu.Unlock()

	m.restartAgent(context.Background(), m.agents["pane-1"])

	if sendKeysCalled {
		t.Error("should not send keys when build fails")
	}
}

func TestRestartAgentHandlesSendKeysError(t *testing.T) {
	restore := saveHooks()
	defer restore()

	setHooksLocked(func() {
		sleepFn = func(d time.Duration) {}
		buildPaneCmdFn = func(projectDir, agentCmd string) (string, error) {
			return agentCmd, nil
		}
		sendKeysFn = func(paneID, cmd string, enter bool) error {
			return fmt.Errorf("send keys error")
		}
	})

	cfg := config.Default()
	cfg.Resilience.RestartDelaySeconds = 0

	m := NewMonitor("test-session", "/tmp/project", cfg, true)
	m.RegisterAgent("pane-1", 1, "cc", "opus", "claude")

	m.mu.Lock()
	m.agents["pane-1"].Healthy = false
	m.mu.Unlock()

	// Should not panic
	m.restartAgent(context.Background(), m.agents["pane-1"])

	// Restart count should still be incremented
	m.mu.RLock()
	count := m.agents["pane-1"].RestartCount
	m.mu.RUnlock()

	if count != 1 {
		t.Errorf("restart count should be incremented even on error, got %d", count)
	}
}

func TestMonitorLoopRespectsMinCheckInterval(t *testing.T) {
	restore := saveHooks()
	defer restore()

	var checkCount int
	var mu sync.Mutex
	setHooksLocked(func() {
		checkSessionFn = func(session string) (*health.SessionHealth, error) {
			mu.Lock()
			checkCount++
			mu.Unlock()
			return &health.SessionHealth{Session: session, Agents: []health.AgentHealth{}}, nil
		}
	})

	cfg := config.Default()
	cfg.Resilience.HealthCheckSeconds = 0 // Should become 10 seconds minimum

	m := NewMonitor("test-session", "/tmp/project", cfg, true)

	ctx, cancel := context.WithCancel(context.Background())

	go m.monitorLoop(ctx)

	// Wait less than minimum interval
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-m.done

	mu.Lock()
	count := checkCount
	mu.Unlock()

	// Should have done at most 1 check (immediate check at start is not done)
	if count > 1 {
		t.Errorf("expected at most 1 check with minimum interval, got %d", count)
	}
}

func TestNewMonitorWithNotifications(t *testing.T) {
	cfg := config.Default()
	cfg.Notifications.Enabled = true

	m := NewMonitor("test-session", "/tmp/project", cfg, true)

	if m.notifier == nil {
		t.Error("notifier should be created when notifications enabled")
	}
}

func TestNewMonitorWithoutNotifications(t *testing.T) {
	cfg := config.Default()
	cfg.Notifications.Enabled = false

	m := NewMonitor("test-session", "/tmp/project", cfg, true)

	if m.notifier != nil {
		t.Error("notifier should be nil when notifications disabled")
	}
}

func TestDisplayTmuxMessage(t *testing.T) {
	restore := saveHooks()
	defer restore()

	var capturedSession, capturedMsg string
	var capturedDuration int
	setHooksLocked(func() {
		displayMessageFn = func(session, msg string, durationMs int) error {
			capturedSession = session
			capturedMsg = msg
			capturedDuration = durationMs
			return nil
		}
	})

	displayTmuxMessage("test-session", "Hello World")

	if capturedSession != "test-session" {
		t.Errorf("expected session 'test-session', got %s", capturedSession)
	}
	if capturedMsg != "Hello World" {
		t.Errorf("expected msg 'Hello World', got %s", capturedMsg)
	}
	if capturedDuration != 10000 {
		t.Errorf("expected duration 10000, got %d", capturedDuration)
	}
}

func TestDisplayTmuxMessageError(t *testing.T) {
	restore := saveHooks()
	defer restore()

	setHooksLocked(func() {
		displayMessageFn = func(session, msg string, durationMs int) error {
			return fmt.Errorf("tmux error")
		}
	})

	// Should not panic
	displayTmuxMessage("test-session", "Hello")
}

func TestHandleRateLimitTriggersRotationAssistance(t *testing.T) {
	restore := saveHooks()
	defer restore()

	var mu sync.Mutex
	var displayCalled bool
	setHooksLocked(func() {
		displayMessageFn = func(session, msg string, durationMs int) error {
			mu.Lock()
			displayCalled = true
			mu.Unlock()
			return nil
		}
	})

	cfg := config.Default()
	cfg.Resilience.RateLimit.Detect = true
	cfg.Resilience.RateLimit.Notify = false // Disable to avoid notification errors
	cfg.Rotation.Enabled = true
	cfg.Rotation.AutoTrigger = true

	m := NewMonitor("test-session", "/tmp/project", cfg, true)
	m.RegisterAgent("pane-1", 1, "cc", "opus", "claude")

	agent := m.agents["pane-1"]
	m.handleRateLimit(agent, 60)

	// Give async goroutine time to run
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	called := displayCalled
	mu.Unlock()
	if !called {
		t.Error("expected tmux message to be displayed for rotation assistance")
	}
}

func TestTriggerRotationAssistanceWithNotifier(t *testing.T) {
	restore := saveHooks()
	defer restore()

	var displayCalled bool
	setHooksLocked(func() {
		displayMessageFn = func(session, msg string, durationMs int) error {
			displayCalled = true
			return nil
		}
	})

	cfg := config.Default()
	cfg.Notifications.Enabled = true
	cfg.Rotation.AutoInitiate = true // Test this branch even though it's a no-op

	m := NewMonitor("test-session", "/tmp/project", cfg, true)

	m.triggerRotationAssistance("test-session", 1, "cc", cfg.Rotation)

	if !displayCalled {
		t.Error("expected tmux message to be displayed")
	}
}

func TestTriggerRotationAssistanceEmptySession(t *testing.T) {
	restore := saveHooks()
	defer restore()

	var displayCalled bool
	setHooksLocked(func() {
		displayMessageFn = func(session, msg string, durationMs int) error {
			displayCalled = true
			return nil
		}
	})

	cfg := config.Default()
	m := NewMonitor("", "/tmp/project", cfg, true)

	// With empty session, should not call displayTmuxMessage
	m.triggerRotationAssistance("", 1, "cc", cfg.Rotation)

	if displayCalled {
		t.Error("should not display tmux message for empty session")
	}
}

func TestMonitorStart_NilContextAndDoubleStartAreSafe(t *testing.T) {
	cfg := config.Default()
	cfg.Resilience.AutoRestart = false

	m := NewMonitor("test-session", t.TempDir(), cfg, false)

	// Should not panic on nil context.
	m.Start(nil)

	// Calling Start twice should be a no-op (monitor is single-use due to done channel).
	m.Start(context.Background())

	done := make(chan struct{})
	go func() {
		m.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() hung after Start(nil) + Start()")
	}
}
