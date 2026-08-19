package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCoordinatorTOMLRoundTrip — exact repro from ntm#111. With the [coordinator]
// section now wired into Config, a user-supplied config.toml must materialize
// into Config.Coordinator with the values they wrote, instead of "unknown field"
// or silently defaulting.
func TestCoordinatorTOMLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(`[coordinator]
auto_assign = false
send_digests = true
digest_interval = "30m"
conflict_notify = true
conflict_negotiate = true
idle_threshold = 300
poll_interval = "30s"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() rejected coordinator section: %v", err)
	}

	if cfg.Coordinator.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %s, want 30s", cfg.Coordinator.PollInterval)
	}
	if cfg.Coordinator.DigestInterval != 30*time.Minute {
		t.Errorf("DigestInterval = %s, want 30m", cfg.Coordinator.DigestInterval)
	}
	if cfg.Coordinator.AutoAssign {
		t.Errorf("AutoAssign = true, want false")
	}
	if !cfg.Coordinator.SendDigests {
		t.Errorf("SendDigests = false, want true (TOML set it true)")
	}
	if !cfg.Coordinator.ConflictNotify {
		t.Errorf("ConflictNotify = false, want true")
	}
	if !cfg.Coordinator.ConflictNegotiate {
		t.Errorf("ConflictNegotiate = false, want true (TOML set it true)")
	}
	if cfg.Coordinator.IdleThreshold != 300 {
		t.Errorf("IdleThreshold = %v, want 300", cfg.Coordinator.IdleThreshold)
	}
}

// TestCoordinatorDefaultsWithoutTOML — when no [coordinator] section is present,
// runtime defaults must survive. Previously the bug surfaced as "default" no
// matter what; this test pins the OPPOSITE: defaults survive when the section
// is genuinely absent.
func TestCoordinatorDefaultsWithoutTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("# no coordinator section\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	want := DefaultCoordinatorConfig()
	if cfg.Coordinator != want {
		t.Errorf("missing-section coordinator = %+v, want defaults %+v",
			cfg.Coordinator, want)
	}
}

// TestCoordinatorDefaultMatchesRuntime — the config-mirror defaults MUST match
// the runtime defaults from the coordinator package, otherwise users would see
// a TOML default that disagrees with `coordinator status` after Start.
//
// This test does NOT import internal/coordinator (which would create a cycle);
// it pins the expected values from coordinator.DefaultCoordinatorConfig as of
// 2026-05-02. If you change defaults in either place, update both AND this
// test.
func TestCoordinatorDefaultMatchesRuntime(t *testing.T) {
	got := DefaultCoordinatorConfig()
	want := CoordinatorConfig{
		PollInterval:      5 * time.Second,
		DigestInterval:    5 * time.Minute,
		AutoAssign:        false,
		IdleThreshold:     30.0,
		AssignOnlyIdle:    true,
		ConflictNotify:    true,
		ConflictNegotiate: false,
		SendDigests:       false,
		HumanAgent:        "Human",
		// Mail nudge (GH#231): MailNudge=false zero value, 60s cooldown,
		// built-in message.
		NudgeCooldownSeconds: 60,
	}
	if got != want {
		t.Errorf("config.DefaultCoordinatorConfig drift; got %+v, want %+v", got, want)
	}
}

// TestCoordinatorMailNudgeKeys — the GH#231 mail-nudge knobs must load from
// TOML and be visible through GetValue, Diff, and Print.
func TestCoordinatorMailNudgeKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(`[coordinator]
mail_nudge = true
nudge_cooldown_seconds = 120
nudge_message = "check your inbox"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() rejected mail-nudge keys: %v", err)
	}
	if !cfg.Coordinator.MailNudge {
		t.Errorf("MailNudge = false, want true")
	}
	if cfg.Coordinator.NudgeCooldownSeconds != 120 {
		t.Errorf("NudgeCooldownSeconds = %d, want 120", cfg.Coordinator.NudgeCooldownSeconds)
	}
	if cfg.Coordinator.NudgeMessage != "check your inbox" {
		t.Errorf("NudgeMessage = %q, want override", cfg.Coordinator.NudgeMessage)
	}

	// GetValue exposes the keys.
	for key, want := range map[string]interface{}{
		"coordinator.mail_nudge":             true,
		"coordinator.nudge_cooldown_seconds": 120,
		"coordinator.nudge_message":          "check your inbox",
	} {
		got, err := GetValue(cfg, key)
		if err != nil {
			t.Fatalf("GetValue(%s): %v", key, err)
		}
		if got != want {
			t.Errorf("GetValue(%s) = %v, want %v", key, got, want)
		}
	}

	// Diff reports the non-default values.
	wantDiffs := map[string]bool{
		"coordinator.mail_nudge":             false,
		"coordinator.nudge_cooldown_seconds": false,
		"coordinator.nudge_message":          false,
	}
	for _, diff := range Diff(cfg) {
		if _, tracked := wantDiffs[diff.Path]; tracked {
			wantDiffs[diff.Path] = true
		}
	}
	for path, seen := range wantDiffs {
		if !seen {
			t.Errorf("Diff missing changed key %s", path)
		}
	}

	// Print emits the section with the keys.
	var buf strings.Builder
	if err := Print(cfg, &buf); err != nil {
		t.Fatalf("Print: %v", err)
	}
	output := buf.String()
	for _, want := range []string{"[coordinator]", "mail_nudge = true", "nudge_cooldown_seconds = 120", `nudge_message = "check your inbox"`} {
		if !strings.Contains(output, want) {
			t.Errorf("Print output missing %q", want)
		}
	}
}
