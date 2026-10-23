package resilience

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/health"
	"github.com/Dicklesworthstone/ntm/internal/notify"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// RegisterAgent registers a pane with no launch binding: test shorthand for
// the production RegisterAgentWithBinding path (cli/monitor.go).
func (m *Monitor) RegisterAgent(paneID string, paneIndex int, shellPID int, agentType, model, command string) {
	m.RegisterAgentWithBinding(paneID, paneIndex, shellPID, agentType, model, command, nil)
}

// saveHooks saves all original hooks and returns a restore function.
// Uses hooksMu to synchronize with spawned goroutines that read hooks.
func saveHooks() func() {
	hooksMu.Lock()
	origSend := sendKeysFn
	origBuild := buildPaneCmdFn
	origPrepare := prepareLaunchCommandFn
	origReadSpec := readPaneLaunchSpecFn
	origPrepareSpec := prepareAgentLaunchSpecFn
	origWorkingDir := paneWorkingDirFn
	origSleep := sleepFn
	origCheckSession := checkSessionFn
	origDisplayMessage := displayMessageFn
	origIsChildAlive := isChildAliveFn
	origPaneIdleShell := paneIdleShellFn
	origFindPane := findPaneFn
	origRecordLifecycle := recordLifecycleEventFn
	// Default for tests: the pane exists. Tests exercising the stale-binding
	// guard override this explicitly.
	findPaneFn = func(_ context.Context, _, paneID string) (*tmux.Pane, error) {
		return &tmux.Pane{ID: paneID, Index: 1}, nil
	}
	// Default for tests: the crashed agent left its pane at an idle shell,
	// which is what the restart fixtures model. Tests for the keystroke
	// destination guard override this explicitly.
	paneIdleShellFn = func(tmux.Pane) bool { return true }
	// Crash/restart fixtures must never append to the developer's real
	// analytics event log; tests asserting the record install a recorder.
	recordLifecycleEventFn = func(events.EventType, string, interface{}) {}
	// Existing fixtures represent legacy sessions with no durable pane option.
	// Tests for recorded launches provide a physical pane ID and a saved spec.
	readPaneLaunchSpecFn = func(context.Context, string) (*tmux.AgentLaunchSpec, error) { return nil, nil }
	hooksMu.Unlock()

	return func() {
		hooksMu.Lock()
		sendKeysFn = origSend
		buildPaneCmdFn = origBuild
		prepareLaunchCommandFn = origPrepare
		readPaneLaunchSpecFn = origReadSpec
		prepareAgentLaunchSpecFn = origPrepareSpec
		paneWorkingDirFn = origWorkingDir
		sleepFn = origSleep
		checkSessionFn = origCheckSession
		displayMessageFn = origDisplayMessage
		isChildAliveFn = origIsChildAlive
		paneIdleShellFn = origPaneIdleShell
		findPaneFn = origFindPane
		recordLifecycleEventFn = origRecordLifecycle
		hooksMu.Unlock()
	}
}

// TestRestartAgentStalePaneBindingRetiredWithoutKeys: a pane ID that no longer
// exists in the session must never receive restart keystrokes (after a tmux
// server restart the same ID can belong to a different agent), and the dead
// binding is retired so the monitor stops retrying it.
func TestRestartAgentStalePaneBindingRetiredWithoutKeys(t *testing.T) {
	restore := saveHooks()
	defer restore()

	var mu sync.Mutex
	sendCalls := 0
	setHooksLocked(func() {
		findPaneFn = func(context.Context, string, string) (*tmux.Pane, error) { return nil, nil }
		sendKeysFn = func(context.Context, string, string, bool) error {
			mu.Lock()
			defer mu.Unlock()
			sendCalls++
			return nil
		}
		buildPaneCmdFn = func(string, string) (string, error) { return "codex", nil }
	})

	cfg := config.Default()
	cfg.Resilience.RestartDelaySeconds = 0
	m := NewMonitor("test-session", "/tmp/project", cfg, true)
	m.RegisterAgent("%404", 4, 0, "cod", "gpt-5", "codex")
	m.mu.Lock()
	agent := m.agents["%404"]
	agent.Healthy = false
	m.mu.Unlock()

	m.restartAgent(context.Background(), agent)

	mu.Lock()
	defer mu.Unlock()
	if sendCalls != 0 {
		t.Fatalf("sendKeys calls = %d, want 0 for a stale pane binding", sendCalls)
	}
	m.mu.RLock()
	_, retained := m.agents["%404"]
	m.mu.RUnlock()
	if retained {
		t.Fatal("stale pane binding still registered after retirement")
	}
}

// TestRestartAgentMembershipCheckErrorSkipsButKeepsBinding: when the pane
// query itself fails (tmux briefly unreachable), the restart is skipped
// fail-closed but the binding survives for the next cycle.
func TestRestartAgentMembershipCheckErrorSkipsButKeepsBinding(t *testing.T) {
	restore := saveHooks()
	defer restore()

	var mu sync.Mutex
	sendCalls := 0
	setHooksLocked(func() {
		findPaneFn = func(context.Context, string, string) (*tmux.Pane, error) { return nil, fmt.Errorf("tmux unreachable") }
		sendKeysFn = func(context.Context, string, string, bool) error {
			mu.Lock()
			defer mu.Unlock()
			sendCalls++
			return nil
		}
		buildPaneCmdFn = func(string, string) (string, error) { return "codex", nil }
	})

	cfg := config.Default()
	cfg.Resilience.RestartDelaySeconds = 0
	m := NewMonitor("test-session", "/tmp/project", cfg, true)
	m.RegisterAgent("%405", 5, 0, "cod", "gpt-5", "codex")
	m.mu.Lock()
	agent := m.agents["%405"]
	agent.Healthy = false
	m.mu.Unlock()

	m.restartAgent(context.Background(), agent)

	mu.Lock()
	defer mu.Unlock()
	if sendCalls != 0 {
		t.Fatalf("sendKeys calls = %d, want 0 when membership cannot be verified", sendCalls)
	}
	m.mu.RLock()
	_, retained := m.agents["%405"]
	m.mu.RUnlock()
	if !retained {
		t.Fatal("binding was retired on a transient membership-check error")
	}
}

func TestRestartAgentRefusesRotationQuarantineBeforeActuation(t *testing.T) {
	for _, fenceOnRead := range []int{1, 2} {
		t.Run(fmt.Sprintf("fence-on-read-%d", fenceOnRead), func(t *testing.T) {
			restore := saveHooks()
			defer restore()
			reads, sends := 0, 0
			setHooksLocked(func() {
				findPaneFn = func(context.Context, string, string) (*tmux.Pane, error) {
					reads++
					pane := &tmux.Pane{ID: "%405", Index: 5, Type: tmux.AgentCodex}
					if reads >= fenceOnRead {
						pane.RotationToken = "active-handoff"
					}
					return pane, nil
				}
				prepareLaunchCommandFn = func(context.Context, string, string, *LaunchBinding, string) (string, LaunchAffinity, error) {
					return "codex", LaunchAffinityUnknown, nil
				}
				buildPaneCmdFn = func(string, string) (string, error) { return "codex", nil }
				sendKeysFn = func(context.Context, string, string, bool) error { sends++; return nil }
			})
			cfg := config.Default()
			cfg.Resilience.RestartDelaySeconds = 0
			m := NewMonitor("rotation-test", "/tmp/project", cfg, true)
			m.RegisterAgent("%405", 5, 0, "cod", "gpt-5", "codex")
			tracked := m.agents["%405"]
			tracked.Healthy = false
			m.restartAgent(t.Context(), tracked)
			if sends != 0 || reads != fenceOnRead || tracked.RestartCount != 0 || m.agents["%405"] != tracked {
				t.Fatalf("quarantined restart changed ownership or sent keys: reads=%d sends=%d agent=%+v", reads, sends, tracked)
			}
		})
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
		sendKeysFn = func(_ context.Context, paneID, cmd string, enter bool) error {
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

// TestRestartAgentRefusesWhenPaneIsNotAnIdleShell is the ntm#359 keystroke
// guard: the restart types its launch line into the pane, so it must refuse
// when the pane's foreground is anything but an idle shell (here, an agent
// that is still running as the pane's own process) instead of injecting the
// command into that program's input. No attempt is consumed.
func TestRestartAgentRefusesWhenPaneIsNotAnIdleShell(t *testing.T) {
	restore := saveHooks()
	defer restore()

	var mu sync.Mutex
	sendCalls := 0
	var guarded []tmux.Pane
	setHooksLocked(func() {
		findPaneFn = func(_ context.Context, _, paneID string) (*tmux.Pane, error) {
			return &tmux.Pane{ID: paneID, Index: 1, Command: "claude"}, nil
		}
		paneIdleShellFn = func(pane tmux.Pane) bool {
			mu.Lock()
			defer mu.Unlock()
			guarded = append(guarded, pane)
			return false
		}
		sendKeysFn = func(context.Context, string, string, bool) error {
			mu.Lock()
			defer mu.Unlock()
			sendCalls++
			return nil
		}
		buildPaneCmdFn = func(dir, command string) (string, error) { return "cd " + dir + " && " + command, nil }
	})

	cfg := config.Default()
	cfg.Resilience.AutoRestart = true
	cfg.Resilience.RestartDelaySeconds = 0
	m := NewMonitor("sess", "/tmp/project", cfg, true)
	m.agents["%7"] = &AgentState{PaneID: "%7", PaneIndex: 1, AgentType: "cc", Command: "claude", Healthy: false}

	m.restartAgent(context.Background(), m.agents["%7"])

	mu.Lock()
	defer mu.Unlock()
	if sendCalls != 0 {
		t.Fatalf("sendKeys calls = %d, want 0: the launch line must never be typed into a non-shell foreground", sendCalls)
	}
	if len(guarded) == 0 || guarded[len(guarded)-1].Command != "claude" {
		t.Fatalf("idle-shell guard saw %+v, want the freshly observed destination pane", guarded)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if state := m.agents["%7"]; state.RestartCount != 0 || state.Healthy {
		t.Fatalf("refused restart changed state: restarts=%d healthy=%v, want 0/false", state.RestartCount, state.Healthy)
	}
}

// TestCheckHealthAgentRootPaneIsNotACrash is the ntm#359 regression guard at
// the monitor, using the production liveness rule against a real process. The
// pane was created with the agent as its own command, so #{pane_pid} is the
// agent and it has no children between tool calls. The health rows below are
// what that pane produced before the fix (ProcessExited, no issues), which the
// monitor turned into an undebounced "Agent cc crashed: Agent unhealthy" on
// every tick. The live root process must now veto that verdict.
func TestCheckHealthAgentRootPaneIsNotACrash(t *testing.T) {
	restore := saveHooks()
	defer restore()

	agent := exec.CommandContext(t.Context(), "sleep", "30")
	if err := agent.Start(); err != nil {
		t.Fatalf("start stand-in agent: %v", err)
	}
	t.Cleanup(func() {
		_ = agent.Process.Kill()
		_ = agent.Wait()
	})
	agentPID := agent.Process.Pid

	var mu sync.Mutex
	var recorded []events.EventType
	setHooksLocked(func() {
		checkSessionFn = func(_ context.Context, session string) (*health.SessionHealth, error) {
			return &health.SessionHealth{
				Session: session,
				Agents: []health.AgentHealth{{
					PaneID:        "%1",
					ShellPID:      agentPID,
					Status:        health.StatusError,
					ProcessStatus: health.ProcessExited,
				}},
			}, nil
		}
		recordLifecycleEventFn = func(eventType events.EventType, _ string, _ interface{}) {
			mu.Lock()
			defer mu.Unlock()
			recorded = append(recorded, eventType)
		}
		// isChildAliveFn is deliberately left at its production default.
	})

	cfg := config.Default()
	m := NewMonitor("god", "/tmp/project", cfg, false)
	m.RegisterAgent("%1", 1, 0, "cc", "", "claude")
	for i := 0; i < 3; i++ {
		m.checkHealth(t.Context())
	}
	m.wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(recorded) != 0 {
		t.Fatalf("lifecycle records = %v, want none: a live agent that is its pane's own process is not a crash", recorded)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if state := m.agents["%1"]; !state.Healthy || !state.LastCrash.IsZero() {
		t.Fatalf("agent state = healthy:%v lastCrash:%v, want healthy with no crash", state.Healthy, state.LastCrash)
	}
}

// TestCheckHealthDeadPaneIgnoresReusedPID: once a pane is dead (its own
// process exited and remain-on-exit kept it), tmux keeps reporting the old
// #{pane_pid}. If that number now belongs to an unrelated live process, the
// liveness probe must not resurrect the agent: tmux's pane_dead is
// authoritative and the crash is reported.
func TestCheckHealthDeadPaneIgnoresReusedPID(t *testing.T) {
	restore := saveHooks()
	defer restore()

	unrelated := exec.CommandContext(t.Context(), "sleep", "30")
	if err := unrelated.Start(); err != nil {
		t.Fatalf("start unrelated process: %v", err)
	}
	t.Cleanup(func() {
		_ = unrelated.Process.Kill()
		_ = unrelated.Wait()
	})

	var mu sync.Mutex
	var recorded []events.EventType
	setHooksLocked(func() {
		checkSessionFn = func(_ context.Context, session string) (*health.SessionHealth, error) {
			return &health.SessionHealth{
				Session: session,
				Agents: []health.AgentHealth{{
					PaneID:        "%1",
					ShellPID:      unrelated.Process.Pid,
					PaneDead:      true,
					Status:        health.StatusError,
					ProcessStatus: health.ProcessExited,
				}},
			}, nil
		}
		recordLifecycleEventFn = func(eventType events.EventType, _ string, _ interface{}) {
			mu.Lock()
			defer mu.Unlock()
			recorded = append(recorded, eventType)
		}
	})

	m := NewMonitor("god", "/tmp/project", config.Default(), false)
	m.RegisterAgent("%1", 1, 0, "cc", "", "claude")
	m.checkHealth(t.Context())
	m.wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(recorded) != 1 || recorded[0] != events.EventAgentCrash {
		t.Fatalf("lifecycle records = %v, want one agent_crash for the dead pane", recorded)
	}
}

// GH#251 phase 2: grok agents are now valid automated-restart targets, so
// restartAgent rebuilds and re-sends the grok launch command like claude/codex.
func TestRestartAgentRestartsGrokLikeOtherAgents(t *testing.T) {
	restore := saveHooks()
	defer restore()

	var mu sync.Mutex
	buildCalls := 0
	sendCalls := 0
	setHooksLocked(func() {
		buildPaneCmdFn = func(string, string) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			buildCalls++
			return "grok --always-approve", nil
		}
		sendKeysFn = func(_ context.Context, paneID, cmd string, enter bool) error {
			mu.Lock()
			defer mu.Unlock()
			sendCalls++
			if paneID != "pane-1" || cmd != "grok --always-approve" || !enter {
				t.Errorf("sendKeys(%q, %q, %v), want grok relaunch on pane-1", paneID, cmd, enter)
			}
			return nil
		}
		sleepFn = func(time.Duration) {}
	})

	cfg := config.Default()
	cfg.Resilience.AutoRestart = true
	cfg.Resilience.RestartDelaySeconds = 0
	m := NewMonitor("test-session", "/tmp/project", cfg, true)
	m.RegisterAgent("pane-1", 1, 0, "grok-build", "grok-3", "grok --always-approve")
	m.mu.Lock()
	m.agents["pane-1"].Healthy = false
	m.mu.Unlock()

	if err := validateAutomatedMonitorRestart("xai_grok_build"); err != nil {
		t.Fatalf("validateAutomatedMonitorRestart() error = %v, want grok accepted", err)
	}
	m.restartAgent(context.Background(), m.agents["pane-1"])

	mu.Lock()
	gotBuild, gotSend := buildCalls, sendCalls
	mu.Unlock()
	if gotBuild != 1 || gotSend != 1 {
		t.Fatalf("restart hooks: build=%d send=%d, want 1 and 1", gotBuild, gotSend)
	}
	m.mu.RLock()
	state := *m.agents["pane-1"]
	m.mu.RUnlock()
	if state.RestartCount != 1 || !state.Healthy {
		t.Fatalf("Grok restart state: count=%d healthy=%v, want restarted and healthy", state.RestartCount, state.Healthy)
	}
}

// GH#251 phase 2: a crashed grok agent is now scheduled for auto-restart
// exactly like claude/codex agents.
func TestHandleCrashSchedulesGrokRestart(t *testing.T) {
	restore := saveHooks()
	defer restore()

	var mu sync.Mutex
	buildCalls := 0
	sendCalls := 0
	setHooksLocked(func() {
		buildPaneCmdFn = func(string, string) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			buildCalls++
			return "grok --always-approve", nil
		}
		sendKeysFn = func(_ context.Context, paneID, cmd string, enter bool) error {
			mu.Lock()
			defer mu.Unlock()
			sendCalls++
			if paneID != "pane-1" || cmd != "grok --always-approve" || !enter {
				t.Errorf("sendKeys(%q, %q, %v), want grok relaunch on pane-1", paneID, cmd, enter)
			}
			return nil
		}
		sleepFn = func(time.Duration) {}
	})

	cfg := config.Default()
	cfg.Resilience.AutoRestart = true
	cfg.Resilience.MaxRestarts = 3
	cfg.Resilience.RestartDelaySeconds = 0
	m := NewMonitor("test-session", "/tmp/project", cfg, true)
	m.RegisterAgent("pane-1", 1, 0, "grok", "grok-3", "grok --always-approve")

	m.handleCrash(context.Background(), m.agents["pane-1"], "test crash")
	m.wg.Wait()

	mu.Lock()
	gotBuild, gotSend := buildCalls, sendCalls
	mu.Unlock()
	if gotBuild != 1 || gotSend != 1 {
		t.Fatalf("Grok crash restart hooks: build=%d send=%d, want 1 and 1", gotBuild, gotSend)
	}
	m.mu.RLock()
	count := m.agents["pane-1"].RestartCount
	m.mu.RUnlock()
	if count != 1 {
		t.Fatalf("Grok crash restart count = %d, want 1", count)
	}
}

// A crash and the restart that follows are recorded durably in the analytics
// event log, attributed to the tmux pane ID: --robot-metrics, ntm health and
// the dashboard count restarts and crashes from these records, since the
// webhook and attention-feed copies do not outlive the hour.
func TestHandleCrashRecordsCrashAndRestartLifecycleEvents(t *testing.T) {
	restore := saveHooks()
	defer restore()

	type recorded struct {
		eventType events.EventType
		session   string
		data      events.AgentLifecycleData
	}
	var mu sync.Mutex
	var got []recorded
	setHooksLocked(func() {
		buildPaneCmdFn = func(_, agentCmd string) (string, error) { return agentCmd, nil }
		sendKeysFn = func(context.Context, string, string, bool) error { return nil }
		sleepFn = func(time.Duration) {}
		recordLifecycleEventFn = func(eventType events.EventType, session string, data interface{}) {
			lifecycle, ok := data.(events.AgentLifecycleData)
			if !ok {
				t.Errorf("lifecycle record carried %T, want events.AgentLifecycleData", data)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			got = append(got, recorded{eventType: eventType, session: session, data: lifecycle})
		}
	})

	cfg := config.Default()
	cfg.Resilience.AutoRestart = true
	cfg.Resilience.MaxRestarts = 3
	cfg.Resilience.RestartDelaySeconds = 0
	m := NewMonitor("metrics-session", "/tmp/project", cfg, true)
	m.RegisterAgent("%7", 2, 0, "cc", "opus", "claude")

	m.handleCrash(context.Background(), m.agents["%7"], "process exited")
	m.wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("recorded %d lifecycle events (%+v), want a crash then a restart", len(got), got)
	}
	crash, restart := got[0], got[1]
	if crash.eventType != events.EventAgentCrash || crash.session != "metrics-session" ||
		crash.data.PaneID != "%7" || crash.data.AgentType != "cc" || crash.data.Reason != "process exited" {
		t.Errorf("crash record = %+v", crash)
	}
	if restart.eventType != events.EventAgentRestart || restart.session != "metrics-session" ||
		restart.data.PaneID != "%7" || restart.data.RestartCount != 1 {
		t.Errorf("restart record = %+v", restart)
	}
}

// A restart that never reached the pane (send failed) is not a restart.
func TestRestartAgentSendFailureRecordsNoRestart(t *testing.T) {
	restore := saveHooks()
	defer restore()

	var recordedTypes []events.EventType
	setHooksLocked(func() {
		buildPaneCmdFn = func(_, agentCmd string) (string, error) { return agentCmd, nil }
		sendKeysFn = func(context.Context, string, string, bool) error { return fmt.Errorf("pane gone") }
		sleepFn = func(time.Duration) {}
		recordLifecycleEventFn = func(eventType events.EventType, _ string, _ interface{}) {
			recordedTypes = append(recordedTypes, eventType)
		}
	})

	cfg := config.Default()
	cfg.Resilience.RestartDelaySeconds = 0
	m := NewMonitor("metrics-session", "/tmp/project", cfg, true)
	m.RegisterAgent("%8", 1, 0, "cc", "opus", "claude")
	m.mu.Lock()
	m.agents["%8"].Healthy = false
	m.mu.Unlock()

	m.restartAgent(context.Background(), m.agents["%8"])
	if len(recordedTypes) != 0 {
		t.Fatalf("a failed restart recorded %v", recordedTypes)
	}
}

func TestRegisterAgent(t *testing.T) {
	cfg := config.Default()
	m := NewMonitor("test-session", "/tmp/project", cfg, true)

	m.RegisterAgentWithBinding("pane-1", 1, 0, "cc", "opus", "claude --model opus", nil)
	m.RegisterAgentWithBinding("pane-2", 2, 0, "gmi", "pro", "gemini --model pro", nil)

	m.mu.RLock()
	states := m.agents
	m.mu.RUnlock()
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

func TestStartAndStop(t *testing.T) {
	restore := saveHooks()
	defer restore()

	cfg := config.Default()
	cfg.Resilience.HealthCheckSeconds = 1 // Fast for testing

	// Mock checkSessionFn to avoid actual tmux calls
	setHooksLocked(func() {
		checkSessionFn = func(ctx context.Context, session string) (*health.SessionHealth, error) {
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

// TestStopReleasesNotifierConnections: the monitor owns its notifier, so
// stopping the monitor closes the webhook client's idle keep-alive
// connections instead of leaving them to the server's idle timeout.
func TestStopReleasesNotifierConnections(t *testing.T) {
	restore := saveHooks()
	defer restore()
	setHooksLocked(func() {
		checkSessionFn = func(_ context.Context, session string) (*health.SessionHealth, error) {
			return &health.SessionHealth{Session: session}, nil
		}
	})

	closed := make(chan struct{}, 1)
	hits := make(chan struct{}, 1)
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		select {
		case hits <- struct{}{}:
		default:
		}
	}))
	ts.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			select {
			case closed <- struct{}{}:
			default:
			}
		}
	}
	ts.Start()
	defer ts.Close()

	cfg := config.Default()
	cfg.Resilience.HealthCheckSeconds = 1
	cfg.Notifications = notify.Config{
		Enabled: true,
		Events:  []string{string(notify.EventAgentCrashed)},
		Webhook: notify.WebhookConfig{Enabled: true, URL: ts.URL},
	}
	m := NewMonitor("test-session", t.TempDir(), cfg, false)
	if m.notifier == nil {
		t.Fatal("notifications enabled but the monitor built no notifier")
	}

	m.Start(context.Background())
	if err := m.notifier.Notify(notify.NewAgentCrashedEvent("test-session", "%1", "cc")); err != nil {
		t.Fatalf("webhook notify: %v", err)
	}
	select {
	case <-hits:
	case <-time.After(5 * time.Second):
		t.Fatal("the webhook was never delivered")
	}
	select {
	case <-closed:
		t.Fatal("the webhook connection closed before Stop; keep-alive premise does not hold")
	case <-time.After(100 * time.Millisecond):
	}

	m.Stop()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop left the notifier's keep-alive connection open")
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
		checkSessionFn = func(ctx context.Context, session string) (*health.SessionHealth, error) {
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
	m.RegisterAgent("pane-1", 1, 0, "cc", "opus", "claude")

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
		checkSessionFn = func(ctx context.Context, session string) (*health.SessionHealth, error) {
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
		sendKeysFn = func(_ context.Context, paneID, cmd string, enter bool) error { return nil }
		buildPaneCmdFn = func(projectDir, agentCmd string) (string, error) {
			return agentCmd, nil
		}
	})

	cfg := config.Default()
	cfg.Resilience.AutoRestart = true
	cfg.Resilience.MaxRestarts = 3
	cfg.Resilience.RestartDelaySeconds = 0
	cfg.Resilience.CrashThreshold = 1 // Single text-based failure triggers crash (no PID available)

	m := NewMonitor("test-session", "/tmp/project", cfg, true)
	m.RegisterAgent("pane-1", 1, 0, "cc", "opus", "claude")

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
		checkSessionFn = func(ctx context.Context, session string) (*health.SessionHealth, error) {
			return &health.SessionHealth{
				Session: session,
				Agents:  []health.AgentHealth{},
			}, nil
		}

		sleepFn = func(d time.Duration) {}
		sendKeysFn = func(_ context.Context, paneID, cmd string, enter bool) error { return nil }
		buildPaneCmdFn = func(projectDir, agentCmd string) (string, error) {
			return agentCmd, nil
		}
	})

	cfg := config.Default()
	cfg.Resilience.AutoRestart = true
	cfg.Resilience.MaxRestarts = 3
	cfg.Resilience.RestartDelaySeconds = 0

	m := NewMonitor("test-session", "/tmp/project", cfg, true)
	m.RegisterAgent("pane-1", 1, 0, "cc", "opus", "claude")

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
		checkSessionFn = func(ctx context.Context, session string) (*health.SessionHealth, error) {
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
	m.RegisterAgent("pane-1", 1, 0, "cc", "opus", "claude")

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
		checkSessionFn = func(ctx context.Context, session string) (*health.SessionHealth, error) {
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
	m.RegisterAgent("pane-1", 1, 0, "cod", "gpt-4", "codex")

	m.checkHealth(context.Background())
	m.wg.Wait()

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
		checkSessionFn = func(ctx context.Context, session string) (*health.SessionHealth, error) {
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
	m.RegisterAgent("pane-1", 1, 0, "cc", "opus", "claude")

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
		checkSessionFn = func(ctx context.Context, session string) (*health.SessionHealth, error) {
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
	m.RegisterAgent("pane-1", 1, 0, "cod", "gpt-4", "codex")

	m.mu.Lock()
	m.agents["pane-1"].RateLimited = true
	m.mu.Unlock()

	m.checkHealth(context.Background())
	m.wg.Wait()

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
		checkSessionFn = func(ctx context.Context, session string) (*health.SessionHealth, error) {
			return nil, fmt.Errorf("session check failed")
		}
	})

	cfg := config.Default()
	m := NewMonitor("test-session", "/tmp/project", cfg, true)
	m.RegisterAgent("pane-1", 1, 0, "cc", "opus", "claude")

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
		sendKeysFn = func(_ context.Context, paneID, cmd string, enter bool) error {
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
	m.RegisterAgent("pane-1", 1, 0, "cc", "opus", "claude")

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
	m.RegisterAgent("pane-1", 1, 0, "cc", "opus", "claude")

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
		sendKeysFn = func(_ context.Context, paneID, cmd string, enter bool) error { return nil }
		buildPaneCmdFn = func(projectDir, agentCmd string) (string, error) {
			return agentCmd, nil
		}
	})

	cfg := config.Default()
	cfg.Resilience.RestartDelaySeconds = 0
	m := NewMonitor("test-session", "/tmp/project", cfg, true)
	m.RegisterAgent("pane-1", 1, 0, "cc", "opus", "claude")

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
		sendKeysFn = func(_ context.Context, paneID, cmd string, enter bool) error {
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
	m.RegisterAgent("pane-1", 1, 0, "cc", "opus", "claude")

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
		sendKeysFn = func(_ context.Context, paneID, cmd string, enter bool) error {
			sendKeysCalled = true
			return nil
		}
	})

	cfg := config.Default()
	cfg.Resilience.RestartDelaySeconds = 0

	m := NewMonitor("test-session", "/tmp/project", cfg, true)
	m.RegisterAgent("pane-1", 1, 0, "cc", "opus", "claude")

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
		sendKeysFn = func(_ context.Context, paneID, cmd string, enter bool) error {
			return fmt.Errorf("send keys error")
		}
	})

	cfg := config.Default()
	cfg.Resilience.RestartDelaySeconds = 0

	m := NewMonitor("test-session", "/tmp/project", cfg, true)
	m.RegisterAgent("pane-1", 1, 0, "cc", "opus", "claude")

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
		checkSessionFn = func(ctx context.Context, session string) (*health.SessionHealth, error) {
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
	done := make(chan struct{})

	go m.monitorLoop(ctx, done)

	// Wait less than minimum interval
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

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
	m.RegisterAgent("pane-1", 1, 0, "cc", "opus", "claude")

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

func TestEnsureRateLimitTracker_LazyInit(t *testing.T) {
	cfg := config.Default()
	cfg.Resilience.RateLimit.Detect = true
	projectDir := t.TempDir()

	m := NewMonitor("test-session", projectDir, cfg, true)
	// NewMonitor already creates the tracker when Detect=true.
	// Nil it to exercise the lazy init path in ensureRateLimitTracker.
	m.rateLimitTracker = nil

	tracker := m.ensureRateLimitTracker()
	if tracker == nil {
		t.Fatal("ensureRateLimitTracker should create tracker when Detect=true")
	}
	// Verify it was cached
	if m.rateLimitTracker != tracker {
		t.Error("ensureRateLimitTracker should cache the tracker")
	}
	// Calling again should return cached instance
	tracker2 := m.ensureRateLimitTracker()
	if tracker2 != tracker {
		t.Error("expected same tracker on second call")
	}
}

func TestEnsureRateLimitTracker_DisabledReturnsNil(t *testing.T) {
	cfg := config.Default()
	cfg.Resilience.RateLimit.Detect = false
	m := NewMonitor("test-session", t.TempDir(), cfg, true)
	m.rateLimitTracker = nil

	tracker := m.ensureRateLimitTracker()
	if tracker != nil {
		t.Error("ensureRateLimitTracker should return nil when Detect=false")
	}
}

func TestRecordRateLimitHit_Direct(t *testing.T) {
	cfg := config.Default()
	cfg.Resilience.RateLimit.Detect = true
	projectDir := t.TempDir()
	m := NewMonitor("test-session", projectDir, cfg, true)

	m.recordRateLimitHit("cod", 30)
	m.wg.Wait()

	state := m.rateLimitTracker.GetProviderState("openai")
	if state == nil {
		t.Fatal("expected rate limit state for openai")
	}
	if state.TotalRateLimits != 1 {
		t.Errorf("TotalRateLimits = %d, want 1", state.TotalRateLimits)
	}
}

func TestRecordRateLimitHit_DirectAlias(t *testing.T) {
	cfg := config.Default()
	cfg.Resilience.RateLimit.Detect = true
	projectDir := t.TempDir()
	m := NewMonitor("test-session", projectDir, cfg, true)

	m.recordRateLimitHit("openai-codex", 30)
	m.wg.Wait()

	state := m.rateLimitTracker.GetProviderState("openai")
	if state == nil {
		t.Fatal("expected rate limit state for openai")
	}
	if state.TotalRateLimits != 1 {
		t.Errorf("TotalRateLimits = %d, want 1", state.TotalRateLimits)
	}
}

func TestRecordRateLimitHit_DisabledIsNoOp(t *testing.T) {
	cfg := config.Default()
	cfg.Resilience.RateLimit.Detect = false
	m := NewMonitor("test-session", t.TempDir(), cfg, true)

	// Should not panic
	m.recordRateLimitHit("cc", 60)
}

func TestRecordRateLimitSuccess_Direct(t *testing.T) {
	cfg := config.Default()
	cfg.Resilience.RateLimit.Detect = true
	projectDir := t.TempDir()
	m := NewMonitor("test-session", projectDir, cfg, true)

	m.recordRateLimitSuccess("cod")
	m.wg.Wait()

	state := m.rateLimitTracker.GetProviderState("openai")
	if state == nil {
		t.Fatal("expected rate limit state for openai")
	}
	if state.TotalSuccesses != 1 {
		t.Errorf("TotalSuccesses = %d, want 1", state.TotalSuccesses)
	}
}

func TestRecordRateLimitSuccess_DirectAlias(t *testing.T) {
	cfg := config.Default()
	cfg.Resilience.RateLimit.Detect = true
	projectDir := t.TempDir()
	m := NewMonitor("test-session", projectDir, cfg, true)

	m.recordRateLimitSuccess("codex")
	m.wg.Wait()

	state := m.rateLimitTracker.GetProviderState("openai")
	if state == nil {
		t.Fatal("expected rate limit state for openai")
	}
	if state.TotalSuccesses != 1 {
		t.Errorf("TotalSuccesses = %d, want 1", state.TotalSuccesses)
	}
}

func TestRecordRateLimitSuccess_DisabledIsNoOp(t *testing.T) {
	cfg := config.Default()
	cfg.Resilience.RateLimit.Detect = false
	m := NewMonitor("test-session", t.TempDir(), cfg, true)

	// Should not panic
	m.recordRateLimitSuccess("cc")
}

func TestMonitorStart_NilContextAndDoubleStartAreSafe(t *testing.T) {
	cfg := config.Default()
	cfg.Resilience.AutoRestart = false

	m := NewMonitor("test-session", t.TempDir(), cfg, false)

	// Should not panic on nil context.
	m.Start(context.TODO())

	// Calling Start twice while already running should be a no-op.
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

func TestMonitorStart_CanRestartAfterStop(t *testing.T) {
	cfg := config.Default()
	cfg.Resilience.AutoRestart = false

	m := NewMonitor("test-session", t.TempDir(), cfg, false)

	m.Start(context.Background())
	m.Stop()

	restarted := make(chan struct{})
	go func() {
		m.Start(context.Background())
		m.Stop()
		close(restarted)
	}()

	select {
	case <-restarted:
	case <-time.After(2 * time.Second):
		t.Fatal("monitor failed to restart cleanly after Stop()")
	}
}

func TestMonitorStartWaitsForConcurrentStop(t *testing.T) {
	cfg := config.Default()
	cfg.Resilience.AutoRestart = false

	m := NewMonitor("test-session", t.TempDir(), cfg, false)

	stopEntered := make(chan struct{}, 1)
	blockedDone := make(chan struct{})
	m.cancel = func() {
		select {
		case stopEntered <- struct{}{}:
		default:
		}
	}
	m.done = blockedDone

	stopped := make(chan struct{})
	go func() {
		m.Stop()
		close(stopped)
	}()

	select {
	case <-stopEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not enter cancellation path")
	}

	started := make(chan struct{})
	go func() {
		m.Start(context.Background())
		close(started)
	}()

	select {
	case <-started:
		t.Fatal("Start() returned before concurrent Stop() finished")
	case <-time.After(50 * time.Millisecond):
	}

	close(blockedDone)

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return after done channel closed")
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("Start() did not resume after Stop() completed")
	}

	m.mu.RLock()
	running := m.cancel != nil && m.done != nil
	m.mu.RUnlock()
	if !running {
		t.Fatal("monitor should be running after Start() resumes")
	}

	m.Stop()
}

func TestCheckHealthIsWorkingGuardSkipsCrash(t *testing.T) {
	// When the health check reports StatusError/ProcessExited but the agent
	// is still actively producing output (ActivityActive), the IsWorking
	// guard should prevent handleCrash from being called. This addresses
	// issue #48 where AI agents printing "exit status" in normal output
	// caused false-positive crash detection.
	restore := saveHooks()
	defer restore()

	var restartAttempted bool
	setHooksLocked(func() {
		checkSessionFn = func(ctx context.Context, session string) (*health.SessionHealth, error) {
			return &health.SessionHealth{
				Session: session,
				Agents: []health.AgentHealth{
					{
						PaneID:        "pane-1",
						Status:        health.StatusError,
						ProcessStatus: health.ProcessExited,
						Activity:      health.ActivityActive, // Agent is working!
						Issues:        []health.Issue{{Type: "crash", Message: "Process exited"}},
					},
				},
			}, nil
		}

		sleepFn = func(d time.Duration) {}
		sendKeysFn = func(_ context.Context, paneID, cmd string, enter bool) error {
			restartAttempted = true
			return nil
		}
		buildPaneCmdFn = func(projectDir, agentCmd string) (string, error) {
			return agentCmd, nil
		}
	})

	cfg := config.Default()
	cfg.Resilience.AutoRestart = true
	cfg.Resilience.MaxRestarts = 3
	cfg.Resilience.RestartDelaySeconds = 0

	m := NewMonitor("test-session", "/tmp/project", cfg, true)
	m.RegisterAgent("pane-1", 1, 0, "cc", "opus", "claude")

	m.checkHealth(context.Background())

	// Give async goroutine time (should NOT run)
	time.Sleep(100 * time.Millisecond)

	m.mu.RLock()
	healthy := m.agents["pane-1"].Healthy
	restartCount := m.agents["pane-1"].RestartCount
	m.mu.RUnlock()

	if !healthy {
		t.Error("agent should remain healthy when IsWorking guard fires")
	}
	if restartCount != 0 {
		t.Errorf("expected restart count 0 (IsWorking guard), got %d", restartCount)
	}
	if restartAttempted {
		t.Error("restart should not have been attempted for actively working agent")
	}
}

func TestCheckHealthKnownDeadPIDBypassesActiveOutputGuard(t *testing.T) {
	restore := saveHooks()
	defer restore()

	var probedPID int
	var restartMu sync.Mutex
	restartAttempts := 0
	setHooksLocked(func() {
		checkSessionFn = func(ctx context.Context, session string) (*health.SessionHealth, error) {
			return &health.SessionHealth{
				Session: session,
				Agents: []health.AgentHealth{
					{
						PaneID:        "pane-1",
						ShellPID:      4242,
						Status:        health.StatusError,
						ProcessStatus: health.ProcessExited,
						Activity:      health.ActivityActive,
						Issues:        []health.Issue{{Type: "crash", Message: "Process exited"}},
					},
				},
			}, nil
		}
		isChildAliveFn = func(pid int) bool {
			probedPID = pid
			return false
		}
		sleepFn = func(time.Duration) {}
		buildPaneCmdFn = func(_ string, agentCmd string) (string, error) {
			return agentCmd, nil
		}
		sendKeysFn = func(_ context.Context, _ string, _ string, _ bool) error {
			restartMu.Lock()
			restartAttempts++
			restartMu.Unlock()
			return nil
		}
	})

	cfg := config.Default()
	cfg.Resilience.AutoRestart = true
	cfg.Resilience.MaxRestarts = 3
	cfg.Resilience.RestartDelaySeconds = 0

	m := NewMonitor("test-session", "/tmp/project", cfg, true)
	m.RegisterAgent("pane-1", 1, 4242, "cc", "opus", "claude")

	m.checkHealth(context.Background())
	m.wg.Wait()

	if probedPID != 4242 {
		t.Fatalf("probed PID = %d, want 4242", probedPID)
	}
	restartMu.Lock()
	gotRestartAttempts := restartAttempts
	restartMu.Unlock()
	if gotRestartAttempts != 1 {
		t.Fatalf("restart attempts = %d, want 1 for authoritatively dead PID", gotRestartAttempts)
	}
	m.mu.RLock()
	got := m.agents["pane-1"].RestartCount
	m.mu.RUnlock()
	if got != 1 {
		t.Fatalf("restart count = %d, want 1", got)
	}
}

func TestCheckHealthFallsBackToRegisteredShellPID(t *testing.T) {
	restore := saveHooks()
	defer restore()

	var probedPID int
	setHooksLocked(func() {
		checkSessionFn = func(ctx context.Context, session string) (*health.SessionHealth, error) {
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
		isChildAliveFn = func(pid int) bool {
			probedPID = pid
			return true
		}
		displayMessageFn = func(session, msg string, durationMs int) error {
			return nil
		}
	})

	cfg := config.Default()
	cfg.Resilience.AutoRestart = false
	cfg.Resilience.MaxRestarts = 3
	cfg.Resilience.RestartDelaySeconds = 0
	cfg.Resilience.CrashThreshold = 1

	m := NewMonitor("test-session", "/tmp/project", cfg, false)
	m.RegisterAgent("pane-1", 1, 4242, "cc", "opus", "claude")

	m.checkHealth(context.Background())

	m.mu.RLock()
	healthy := m.agents["pane-1"].Healthy
	restartCount := m.agents["pane-1"].RestartCount
	consecutiveFailures := m.agents["pane-1"].ConsecutiveFailures
	m.mu.RUnlock()

	if probedPID != 4242 {
		t.Fatalf("probed PID = %d, want registered shell PID 4242", probedPID)
	}
	if !healthy {
		t.Fatal("agent should remain healthy when registered shell PID is still alive")
	}
	if restartCount != 0 {
		t.Fatalf("restart count = %d, want 0", restartCount)
	}
	if consecutiveFailures != 0 {
		t.Fatalf("consecutive failures = %d, want 0", consecutiveFailures)
	}
}

// TestReconcileFromManifestAdoptsLaterAgents is the auto-restart coverage gap
// regression. The monitored set is populated once, at monitor-process start,
// from the manifest; `ntm add` appends a new agent to that manifest but cannot
// reach the running monitor (no signal, no watch, no reload). The added pane
// was therefore never health-checked and never auto-restarted — silently, and
// for the life of the session.
func TestReconcileFromManifestAdoptsLaterAgents(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	const session = "reconcile-adopt"
	if err := SaveManifest(&SpawnManifest{
		Session:     session,
		ProjectDir:  t.TempDir(),
		AutoRestart: true,
		Agents: []AgentConfig{
			{PaneID: "%1", PaneIndex: 1, Type: "cc", Model: "opus", Command: "claude"},
		},
	}); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	m := NewMonitor(session, t.TempDir(), config.Default(), true)
	m.RegisterAgent("%1", 1, 0, "cc", "opus", "claude")

	// `ntm add` appends a second agent to the manifest while the monitor runs.
	manifest, err := LoadManifest(session)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	manifest.Agents = append(manifest.Agents, AgentConfig{
		PaneID: "%2", PaneIndex: 2, Type: "cod", Model: "gpt", Command: "codex",
		LaunchBinding: &LaunchBinding{Provider: "codex", Launcher: "caam", Identifier: "codex-b"},
	})
	if err := SaveManifest(manifest); err != nil {
		t.Fatalf("SaveManifest(update): %v", err)
	}

	m.reconcileFromManifest()

	m.mu.RLock()
	adopted, ok := m.agents["%2"]
	total := len(m.agents)
	m.mu.RUnlock()

	if !ok {
		t.Fatal("agent added after monitor start was never adopted; it would never be auto-restarted")
	}
	if total != 2 {
		t.Errorf("monitored agents = %d, want 2", total)
	}
	if adopted.AgentType != "cod" || adopted.Command != "codex" {
		t.Errorf("adopted agent = %+v, want type cod / command codex", adopted)
	}
	if adopted.LaunchBinding == nil || adopted.LaunchBinding.Identifier != "codex-b" {
		t.Errorf("adopted agent lost its launch binding: %+v; a restart would use the wrong account",
			adopted.LaunchBinding)
	}
	if !adopted.Healthy {
		t.Error("a freshly adopted agent must start healthy, not pre-failed")
	}
}

// TestReconcileFromManifestPreservesCrashAccounting guards the reason
// reconciliation only adds: re-registering a known pane would reset its
// restart bookkeeping, so a crash-looping agent would never reach
// max-restarts and would be relaunched forever.
func TestReconcileFromManifestPreservesCrashAccounting(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	const session = "reconcile-preserve"
	if err := SaveManifest(&SpawnManifest{
		Session:     session,
		ProjectDir:  t.TempDir(),
		AutoRestart: true,
		Agents: []AgentConfig{
			{PaneID: "%1", PaneIndex: 1, Type: "cc", Model: "opus", Command: "claude"},
		},
	}); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	m := NewMonitor(session, t.TempDir(), config.Default(), true)
	m.RegisterAgent("%1", 1, 0, "cc", "opus", "claude")

	m.mu.Lock()
	m.agents["%1"].RestartCount = 4
	m.agents["%1"].ConsecutiveFailures = 3
	m.agents["%1"].Healthy = false
	m.mu.Unlock()

	m.reconcileFromManifest()

	m.mu.RLock()
	got := *m.agents["%1"]
	m.mu.RUnlock()

	if got.RestartCount != 4 || got.ConsecutiveFailures != 3 {
		t.Errorf("reconcile reset crash accounting: RestartCount=%d ConsecutiveFailures=%d, want 4/3",
			got.RestartCount, got.ConsecutiveFailures)
	}
	if got.Healthy {
		t.Error("reconcile flipped an unhealthy agent back to healthy")
	}
}

// TestReconcileFromManifestToleratesMissingManifest: a monitor whose manifest
// is absent or unreadable must keep health-checking what it already watches.
func TestReconcileFromManifestToleratesMissingManifest(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	m := NewMonitor("no-manifest-here", t.TempDir(), config.Default(), true)
	m.RegisterAgent("%1", 1, 0, "cc", "opus", "claude")

	m.reconcileFromManifest() // must not panic or drop anything

	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.agents) != 1 {
		t.Errorf("monitored agents = %d, want the pre-existing 1", len(m.agents))
	}
}

// TestReconcileFromManifestDoesNotResurrectRetiredPanes guards the interaction
// between manifest reconciliation and the stale-binding retirement.
//
// When a tracked pane is verifiably gone from the session the monitor deletes
// it "so the monitor stops retrying a pane that will never come back". Nothing
// prunes the manifest, so reconciliation would re-adopt that pane on the very
// next tick, health-check it, fail, retire it again — forever — turning a
// one-time retirement into a permanent loop.
func TestReconcileFromManifestDoesNotResurrectRetiredPanes(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	const session = "reconcile-retired"
	if err := SaveManifest(&SpawnManifest{
		Session:     session,
		ProjectDir:  t.TempDir(),
		AutoRestart: true,
		Agents: []AgentConfig{
			{PaneID: "%1", PaneIndex: 1, Type: "cc", Model: "opus", Command: "claude"},
			{PaneID: "%dead", PaneIndex: 2, Type: "cod", Model: "gpt", Command: "codex"},
		},
	}); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	m := NewMonitor(session, t.TempDir(), config.Default(), true)
	m.RegisterAgent("%1", 1, 0, "cc", "opus", "claude")
	m.RegisterAgent("%dead", 2, 0, "cod", "gpt", "codex")

	// The monitor verifies %dead is gone from the session and retires it.
	m.mu.Lock()
	delete(m.agents, "%dead")
	m.retired = map[string]struct{}{"%dead": {}}
	m.mu.Unlock()

	// Several ticks must not bring it back, even though the manifest still
	// lists it.
	for i := 0; i < 3; i++ {
		m.reconcileFromManifest()
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, back := m.agents["%dead"]; back {
		t.Error("reconciliation resurrected a deliberately retired pane; the monitor would retry it every tick forever")
	}
	if _, ok := m.agents["%1"]; !ok {
		t.Error("reconciliation dropped a live agent")
	}
	if len(m.agents) != 1 {
		t.Errorf("monitored agents = %d, want 1", len(m.agents))
	}
}
