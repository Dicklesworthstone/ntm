package config

import (
	"os"
	"path/filepath"
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
	}
	if got != want {
		t.Errorf("config.DefaultCoordinatorConfig drift; got %+v, want %+v", got, want)
	}
}
