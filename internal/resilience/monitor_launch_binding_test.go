package resilience

import (
	"context"
	"errors"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

func TestMonitorLaunchBindingFailureSendsNoKeysAndConsumesNoAttempt(t *testing.T) {
	restore := saveHooks()
	defer restore()

	prepareCalls := 0
	sendCalls := 0
	setHooksLocked(func() {
		buildPaneCmdFn = func(string, string) (string, error) { return "claude", nil }
		isChildAliveFn = func(int) bool { return false }
		panePresentFn = func(string, string) (bool, error) { return true, nil }
		prepareLaunchCommandFn = func(
			_ context.Context,
			provider, _ string,
			binding *LaunchBinding,
			command string,
		) (string, LaunchAffinity, error) {
			prepareCalls++
			if provider != "cc" || binding == nil || binding.Identifier != "profile-a" || command != "claude" {
				t.Fatalf("unexpected preflight provider=%q binding=%+v command=%q", provider, binding, command)
			}
			return "", "", errors.New("cannot resolve caam:cc/profile-a")
		}
		sendKeysFn = func(string, string, bool) error {
			sendCalls++
			return nil
		}
	})

	cfg := config.Default()
	cfg.Resilience.RestartDelaySeconds = 0
	monitor := NewMonitor("session", "/tmp/project", cfg, true)
	monitor.RegisterAgentWithBinding("%1", 1, 0, "cc", "", "claude", &LaunchBinding{
		Provider: "cc", Launcher: "caam", Identifier: "profile-a",
	})
	monitor.mu.Lock()
	state := monitor.agents["%1"]
	state.Healthy = false
	monitor.mu.Unlock()

	monitor.restartAgent(context.Background(), state)

	if prepareCalls != 1 {
		t.Fatalf("prepare calls = %d, want 1", prepareCalls)
	}
	if sendCalls != 0 {
		t.Fatalf("send calls = %d, want 0", sendCalls)
	}
	monitor.mu.RLock()
	defer monitor.mu.RUnlock()
	if got := monitor.agents["%1"].RestartCount; got != 0 {
		t.Fatalf("restart count = %d, want 0 after failed affinity preflight", got)
	}
	if monitor.agents["%1"].Healthy {
		t.Fatal("failed affinity preflight incorrectly marked agent healthy")
	}
}
