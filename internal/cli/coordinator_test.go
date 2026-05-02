package cli

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/coordinator"
)

// TestCoordinatorConfigFromTOMLPropagatesValues — translator passes through
// every field when the TOML carries explicit values. Anchors the contract
// `coordinator status --json` relies on.
func TestCoordinatorConfigFromTOMLPropagatesValues(t *testing.T) {
	toml := config.CoordinatorConfig{
		PollInterval:      30 * time.Second,
		DigestInterval:    30 * time.Minute,
		AutoAssign:        true,
		IdleThreshold:     300,
		AssignOnlyIdle:    false,
		ConflictNotify:    false,
		ConflictNegotiate: true,
		SendDigests:       true,
		HumanAgent:        "Operator",
	}
	got := coordinatorConfigFromTOML(toml, coordinator.DefaultCoordinatorConfig())
	if got.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %s, want 30s", got.PollInterval)
	}
	if got.DigestInterval != 30*time.Minute {
		t.Errorf("DigestInterval = %s, want 30m", got.DigestInterval)
	}
	if !got.AutoAssign {
		t.Errorf("AutoAssign = false, want true")
	}
	if got.IdleThreshold != 300 {
		t.Errorf("IdleThreshold = %v, want 300", got.IdleThreshold)
	}
	if got.AssignOnlyIdle {
		t.Errorf("AssignOnlyIdle = true, want false")
	}
	if got.ConflictNotify {
		t.Errorf("ConflictNotify = true, want false")
	}
	if !got.ConflictNegotiate {
		t.Errorf("ConflictNegotiate = false, want true")
	}
	if !got.SendDigests {
		t.Errorf("SendDigests = false, want true")
	}
	if got.HumanAgent != "Operator" {
		t.Errorf("HumanAgent = %q, want %q", got.HumanAgent, "Operator")
	}
}

// TestCoordinatorConfigFromTOMLClampsBelowMinimumDurations — anything below
// the runtime minimums (which would otherwise panic time.NewTicker) is clamped
// up. This matches the validation inside SessionCoordinator.Start.
func TestCoordinatorConfigFromTOMLClampsBelowMinimumDurations(t *testing.T) {
	toml := config.CoordinatorConfig{
		PollInterval:   1 * time.Millisecond,        // below MinPollInterval
		DigestInterval: 1 * time.Second,             // below MinDigestInterval
		HumanAgent:     "X",
	}
	got := coordinatorConfigFromTOML(toml, coordinator.DefaultCoordinatorConfig())
	if got.PollInterval != coordinator.MinPollInterval {
		t.Errorf("PollInterval = %s, want clamped to %s", got.PollInterval, coordinator.MinPollInterval)
	}
	if got.DigestInterval != coordinator.MinDigestInterval {
		t.Errorf("DigestInterval = %s, want clamped to %s", got.DigestInterval, coordinator.MinDigestInterval)
	}
}

// TestCoordinatorConfigFromTOMLEmptyHumanAgentFallsBack — explicitly empty
// `human_agent = ""` in TOML must fall back to the runtime default. Otherwise
// digest delivery would silently target an empty agent name.
func TestCoordinatorConfigFromTOMLEmptyHumanAgentFallsBack(t *testing.T) {
	toml := config.CoordinatorConfig{
		PollInterval:   coordinator.MinPollInterval,
		DigestInterval: coordinator.MinDigestInterval,
		HumanAgent:     "  ", // whitespace-only also counts as empty
	}
	defaults := coordinator.DefaultCoordinatorConfig()
	got := coordinatorConfigFromTOML(toml, defaults)
	if got.HumanAgent != defaults.HumanAgent {
		t.Errorf("HumanAgent = %q, want fallback %q", got.HumanAgent, defaults.HumanAgent)
	}
}

// TestCoordinatorMirrorMatchesRuntime — config.DefaultCoordinatorConfig() must
// stay in lock-step with coordinator.DefaultCoordinatorConfig(). Drift here
// means a user with no [coordinator] TOML section sees one set of "defaults"
// reflected in `am config validate`, and a different set actually enforced at
// runtime — exactly the symptom #111 was filed for. This test lives in the cli
// package because it can import both internal/config and internal/coordinator
// without forming a cycle (config → coordinator → robot → config).
func TestCoordinatorMirrorMatchesRuntime(t *testing.T) {
	mirror := config.DefaultCoordinatorConfig()
	runtime := coordinator.DefaultCoordinatorConfig()

	if mirror.PollInterval != runtime.PollInterval {
		t.Errorf("PollInterval drift: mirror=%s runtime=%s", mirror.PollInterval, runtime.PollInterval)
	}
	if mirror.DigestInterval != runtime.DigestInterval {
		t.Errorf("DigestInterval drift: mirror=%s runtime=%s", mirror.DigestInterval, runtime.DigestInterval)
	}
	if mirror.AutoAssign != runtime.AutoAssign {
		t.Errorf("AutoAssign drift: mirror=%v runtime=%v", mirror.AutoAssign, runtime.AutoAssign)
	}
	if mirror.IdleThreshold != runtime.IdleThreshold {
		t.Errorf("IdleThreshold drift: mirror=%v runtime=%v", mirror.IdleThreshold, runtime.IdleThreshold)
	}
	if mirror.AssignOnlyIdle != runtime.AssignOnlyIdle {
		t.Errorf("AssignOnlyIdle drift: mirror=%v runtime=%v", mirror.AssignOnlyIdle, runtime.AssignOnlyIdle)
	}
	if mirror.ConflictNotify != runtime.ConflictNotify {
		t.Errorf("ConflictNotify drift: mirror=%v runtime=%v", mirror.ConflictNotify, runtime.ConflictNotify)
	}
	if mirror.ConflictNegotiate != runtime.ConflictNegotiate {
		t.Errorf("ConflictNegotiate drift: mirror=%v runtime=%v", mirror.ConflictNegotiate, runtime.ConflictNegotiate)
	}
	if mirror.SendDigests != runtime.SendDigests {
		t.Errorf("SendDigests drift: mirror=%v runtime=%v", mirror.SendDigests, runtime.SendDigests)
	}
	if mirror.HumanAgent != runtime.HumanAgent {
		t.Errorf("HumanAgent drift: mirror=%q runtime=%q", mirror.HumanAgent, runtime.HumanAgent)
	}
}

func TestFormatIdleDuration(t *testing.T) {

	tests := []struct {
		name     string
		duration time.Duration
		expected string
	}{
		// Less than 1 minute - seconds
		{name: "0 seconds", duration: 0, expected: "0s"},
		{name: "1 second", duration: 1 * time.Second, expected: "1s"},
		{name: "30 seconds", duration: 30 * time.Second, expected: "30s"},
		{name: "59 seconds", duration: 59 * time.Second, expected: "59s"},

		// 1 minute to less than 1 hour - minutes
		{name: "1 minute", duration: 1 * time.Minute, expected: "1m"},
		{name: "5 minutes", duration: 5 * time.Minute, expected: "5m"},
		{name: "30 minutes", duration: 30 * time.Minute, expected: "30m"},
		{name: "59 minutes", duration: 59 * time.Minute, expected: "59m"},
		{name: "59 min 59 sec", duration: 59*time.Minute + 59*time.Second, expected: "59m"},

		// 1+ hours - hours and minutes
		{name: "1 hour", duration: 1 * time.Hour, expected: "1h0m"},
		{name: "1 hour 30 min", duration: 1*time.Hour + 30*time.Minute, expected: "1h30m"},
		{name: "2 hours", duration: 2 * time.Hour, expected: "2h0m"},
		{name: "2 hours 15 min", duration: 2*time.Hour + 15*time.Minute, expected: "2h15m"},
		{name: "24 hours", duration: 24 * time.Hour, expected: "24h0m"},
		{name: "48 hours", duration: 48 * time.Hour, expected: "48h0m"},
		{name: "100 hours 45 min", duration: 100*time.Hour + 45*time.Minute, expected: "100h45m"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := formatIdleDuration(tc.duration)
			if result != tc.expected {
				t.Errorf("formatIdleDuration(%v) = %q; want %q", tc.duration, result, tc.expected)
			}
		})
	}
}
