package swarm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// writeFakeCaam creates an executable fake caam that, when run, appends its
// arguments to a marker file and prints a successful JSON switch result. It
// returns the caam path and the marker path. The marker lets a test prove
// whether caam was actually invoked (i.e. the guard did NOT block).
func writeFakeCaam(t *testing.T) (caamPath, markerPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake caam shell script requires a POSIX shell")
	}
	dir := t.TempDir()
	markerPath = filepath.Join(dir, "caam_invocations.log")
	caamPath = filepath.Join(dir, "caam")
	// `robot status` advertises the safe-restore capability so the default
	// capability gate (caam #19) treats this fake caam as safe; the switch path
	// returns a successful rotation result.
	script := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %q
if [ "$1" = "robot" ] && [ "$2" = "status" ]; then
  printf '{"data":{"capabilities":["safe-restore","robot"]}}'
  exit 0
fi
printf '{"success":true,"previous_account":"acctA","new_account":"acctB","accounts_remaining":1}'
`, markerPath)
	if err := os.WriteFile(caamPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake caam: %v", err)
	}
	return caamPath, markerPath
}

func caamWasInvoked(t *testing.T, markerPath string) bool {
	t.Helper()
	data, err := os.ReadFile(markerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false
		}
		t.Fatalf("read marker: %v", err)
	}
	return len(data) > 0
}

func codexLimitEvent() LimitHitEvent {
	return LimitHitEvent{
		SessionPane: "swarm:1.1",
		AgentType:   "cod",
		Pattern:     "rate limit",
		DetectedAt:  time.Now(),
	}
}

// Acceptance: no config / non-Codex providers still rotate; Codex with isolated
// panes rotates; the caam binary IS invoked in the allowed case.
func TestGuard_IsolatedCodexPanesAllowsRotation(t *testing.T) {
	caamPath, marker := writeFakeCaam(t)
	rotator := NewAccountRotator().
		WithCaamPath(caamPath).
		WithCodexHomeInspector(func() ([]CodexPaneInfo, error) {
			return []CodexPaneInfo{
				{SessionPane: "swarm:1.1", CodexHome: "/home/u/.ntm/codex-homes/swarm/1"},
				{SessionPane: "swarm:1.2", CodexHome: "/home/u/.ntm/codex-homes/swarm/2"},
			}, nil
		})

	record, err := rotator.OnLimitHit(codexLimitEvent())
	if err != nil {
		t.Fatalf("expected rotation to be allowed for isolated Codex panes, got error: %v", err)
	}
	if record == nil {
		t.Fatal("expected a rotation record")
	}
	if !caamWasInvoked(t, marker) {
		t.Error("expected caam to be invoked when all Codex panes are isolated")
	}
}

// Acceptance: multiple panes sharing global ~/.codex => refuse automatic global rotation.
func TestGuard_SharedGlobalCodexRefusesRotation(t *testing.T) {
	caamPath, marker := writeFakeCaam(t)
	rotator := NewAccountRotator().
		WithCaamPath(caamPath).
		WithCodexHomeInspector(func() ([]CodexPaneInfo, error) {
			return []CodexPaneInfo{
				{SessionPane: "swarm:1.1", CodexHome: ""}, // shared global
				{SessionPane: "swarm:1.2", CodexHome: "/home/u/.ntm/codex-homes/swarm/2"},
			}, nil
		})

	record, err := rotator.OnLimitHit(codexLimitEvent())
	if err == nil {
		t.Fatal("expected rotation to be refused when a live Codex pane shares global ~/.codex")
	}
	if !errors.Is(err, ErrRotationBlocked) {
		t.Errorf("expected ErrRotationBlocked, got: %v", err)
	}
	if record != nil {
		t.Error("expected nil record when rotation is refused")
	}
	if !contains(err.Error(), "share global ~/.codex/auth.json") {
		t.Errorf("expected actionable shared-global message, got: %v", err)
	}
	if caamWasInvoked(t, marker) {
		t.Error("caam must NOT be invoked when rotation is refused")
	}
}

// Unknown isolation (no inspector wired) => fail closed for Codex.
func TestGuard_UnknownCodexIsolationRefusesRotation(t *testing.T) {
	caamPath, marker := writeFakeCaam(t)
	rotator := NewAccountRotator().WithCaamPath(caamPath) // no inspector

	_, err := rotator.OnLimitHit(codexLimitEvent())
	if err == nil {
		t.Fatal("expected rotation to be refused when Codex isolation is unknown")
	}
	if !errors.Is(err, ErrRotationBlocked) {
		t.Errorf("expected ErrRotationBlocked, got: %v", err)
	}
	if caamWasInvoked(t, marker) {
		t.Error("caam must NOT be invoked when Codex isolation is unknown")
	}
}

// Inspector error => fail closed.
func TestGuard_CodexInspectorErrorRefusesRotation(t *testing.T) {
	caamPath, marker := writeFakeCaam(t)
	rotator := NewAccountRotator().
		WithCaamPath(caamPath).
		WithCodexHomeInspector(func() ([]CodexPaneInfo, error) {
			return nil, fmt.Errorf("tmux unavailable")
		})

	_, err := rotator.OnLimitHit(codexLimitEvent())
	if err == nil {
		t.Fatal("expected rotation to be refused when inspector fails")
	}
	if !errors.Is(err, ErrRotationBlocked) {
		t.Errorf("expected ErrRotationBlocked, got: %v", err)
	}
	if caamWasInvoked(t, marker) {
		t.Error("caam must NOT be invoked when pane inspection fails")
	}
}

// No live Codex panes => the shared-global guard permits rotation.
func TestGuard_NoLiveCodexPanesAllowsRotation(t *testing.T) {
	caamPath, marker := writeFakeCaam(t)
	rotator := NewAccountRotator().
		WithCaamPath(caamPath).
		WithCodexHomeInspector(func() ([]CodexPaneInfo, error) {
			return []CodexPaneInfo{}, nil
		})

	record, err := rotator.OnLimitHit(codexLimitEvent())
	if err != nil {
		t.Fatalf("expected rotation to be allowed with no live Codex panes, got: %v", err)
	}
	if record == nil {
		t.Fatal("expected a rotation record")
	}
	if !caamWasInvoked(t, marker) {
		t.Error("expected caam to be invoked when no live Codex panes share global auth")
	}
}

// Acceptance: a pinned account blocks automatic rotation.
func TestGuard_PinBlocksRotation(t *testing.T) {
	caamPath, marker := writeFakeCaam(t)
	rotator := NewAccountRotator().
		WithCaamPath(caamPath).
		// Isolated panes would otherwise allow rotation; the pin must still block.
		WithCodexHomeInspector(func() ([]CodexPaneInfo, error) {
			return []CodexPaneInfo{{SessionPane: "swarm:1.1", CodexHome: "/iso/1"}}, nil
		})
	rotator.PinAccount("cod", "acctB")

	_, err := rotator.OnLimitHit(codexLimitEvent())
	if err == nil {
		t.Fatal("expected rotation to be refused when the provider is pinned")
	}
	if !errors.Is(err, ErrRotationBlocked) {
		t.Errorf("expected ErrRotationBlocked, got: %v", err)
	}
	if !contains(err.Error(), "pinned to") {
		t.Errorf("expected pin message, got: %v", err)
	}
	if caamWasInvoked(t, marker) {
		t.Error("caam must NOT be invoked when the provider is pinned")
	}
}

// Pin also protects non-Codex providers (e.g. Claude).
func TestGuard_PinBlocksNonCodexRotation(t *testing.T) {
	caamPath, marker := writeFakeCaam(t)
	rotator := NewAccountRotator().WithCaamPath(caamPath)
	rotator.PinAccount("cc", "work")

	event := LimitHitEvent{
		SessionPane: "swarm:2.1",
		AgentType:   "cc",
		Pattern:     "usage limit",
		DetectedAt:  time.Now(),
	}
	_, err := rotator.OnLimitHit(event)
	if err == nil {
		t.Fatal("expected rotation to be refused when Claude is pinned")
	}
	if !errors.Is(err, ErrRotationBlocked) {
		t.Errorf("expected ErrRotationBlocked, got: %v", err)
	}
	if caamWasInvoked(t, marker) {
		t.Error("caam must NOT be invoked when Claude is pinned")
	}
}

// Force escape hatch overrides both the pin and the shared-global guard.
func TestGuard_ForceOverridesPinAndSharedGlobal(t *testing.T) {
	caamPath, marker := writeFakeCaam(t)
	rotator := NewAccountRotator().
		WithCaamPath(caamPath).
		WithForceGlobalAuthClobber(true).
		WithCodexHomeInspector(func() ([]CodexPaneInfo, error) {
			return []CodexPaneInfo{{SessionPane: "swarm:1.1", CodexHome: ""}}, nil // shared global
		})
	rotator.PinAccount("cod", "acctB")

	record, err := rotator.OnLimitHit(codexLimitEvent())
	if err != nil {
		t.Fatalf("expected force to override the guard, got: %v", err)
	}
	if record == nil {
		t.Fatal("expected a rotation record under force")
	}
	if !caamWasInvoked(t, marker) {
		t.Error("expected caam to be invoked when force is set")
	}
}

// Non-Codex provider with no pin rotates without needing an inspector.
func TestGuard_NonCodexProviderAllowsRotationWithoutInspector(t *testing.T) {
	caamPath, marker := writeFakeCaam(t)
	rotator := NewAccountRotator().WithCaamPath(caamPath) // no inspector

	event := LimitHitEvent{
		SessionPane: "swarm:3.1",
		AgentType:   "cc",
		Pattern:     "usage limit",
		DetectedAt:  time.Now(),
	}
	record, err := rotator.OnLimitHit(event)
	if err != nil {
		t.Fatalf("expected non-Codex rotation to be allowed without an inspector, got: %v", err)
	}
	if record == nil {
		t.Fatal("expected a rotation record")
	}
	if !caamWasInvoked(t, marker) {
		t.Error("expected caam to be invoked for non-Codex rotation")
	}
}

// Pin state round-trips through disk so a CLI process and a running swarm agree.
func TestGuard_PinPersistence(t *testing.T) {
	dir := t.TempDir()

	writer := NewAccountRotator()
	writer.PinAccount("cod", "acctB")
	if err := writer.SavePins(dir); err != nil {
		t.Fatalf("SavePins: %v", err)
	}

	reader := NewAccountRotator()
	if err := reader.LoadPins(dir); err != nil {
		t.Fatalf("LoadPins: %v", err)
	}
	got, ok := reader.PinnedAccount("cod")
	if !ok || got != "acctB" {
		t.Fatalf("expected pin openai->acctB after reload, got %q ok=%v", got, ok)
	}

	// Unpin and confirm it clears on disk.
	writer.UnpinAccount("cod")
	if err := writer.SavePins(dir); err != nil {
		t.Fatalf("SavePins after unpin: %v", err)
	}
	reader2 := NewAccountRotator()
	if err := reader2.LoadPins(dir); err != nil {
		t.Fatalf("LoadPins after unpin: %v", err)
	}
	if _, ok := reader2.PinnedAccount("cod"); ok {
		t.Error("expected pin to be cleared after unpin + reload")
	}
}
