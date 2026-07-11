package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestIsolationAuthorizationCannotBeForged(t *testing.T) {
	t.Setenv(isolatedTmuxMarker, "1")
	t.Setenv("TMUX_TMPDIR", "/tmp")
	t.Setenv("TMUX", "")
	if isolatedTmuxReady() {
		t.Fatal("environment-only isolation authorization must be rejected")
	}
}

func TestIsolationCommandsIgnoreRouteSwap(t *testing.T) {
	cleanup, err := SetupIsolatedTmuxTestProcess()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Error(err)
		}
	})

	ownedEnv, ok := isolatedTmuxCommandEnv()
	if !ok {
		t.Fatal("missing process-owned command environment")
	}
	ownedSession := "ntm_test_owned_create"

	alternateRoot := filepath.Join(t.TempDir(), "alternate")
	if err := os.MkdirAll(alternateRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	alternateEnv := filterEnv(os.Environ(), "TMUX")
	alternateEnv = filterEnv(alternateEnv, "TMUX_TMPDIR")
	alternateEnv = append(alternateEnv, "TMUX_TMPDIR="+alternateRoot)
	alternateSession := "ntm_test_route_swap"
	alternateCreate := exec.Command(tmux.BinaryPath(), "new-session", "-d", "-s", alternateSession)
	alternateCreate.Env = alternateEnv
	if out, err := alternateCreate.CombinedOutput(); err != nil {
		t.Fatalf("create alternate sentinel: %v: %s", err, out)
	}
	t.Cleanup(func() {
		cmd := exec.Command(tmux.BinaryPath(), "kill-session", "-t", alternateSession)
		cmd.Env = alternateEnv
		_ = cmd.Run()
	})

	// Swap the mutable process route after setup. All shared operations must
	// still execute with the stored process-owned environment.
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", alternateRoot)
	ownedCreate := exec.Command(tmux.BinaryPath(), "new-session", "-d", "-s", ownedSession)
	ownedCreate.Env = ownedEnv
	if out, err := ownedCreate.CombinedOutput(); err != nil {
		t.Fatalf("create owned session after route swap: %v: %s", err, out)
	}
	t.Cleanup(func() {
		cmd := exec.Command(tmux.BinaryPath(), "kill-session", "-t", ownedSession)
		cmd.Env = ownedEnv
		_ = cmd.Run()
	})
	KillAllTestSessionsSilent()
	killSession(NewTestLogger(t, t.TempDir()), alternateSession)

	alternateHas := exec.Command(tmux.BinaryPath(), "has-session", "-t", alternateSession)
	alternateHas.Env = alternateEnv
	if err := alternateHas.Run(); err != nil {
		t.Fatal("alternate-root sentinel was reached by shared cleanup")
	}
	alternateOwned := exec.Command(tmux.BinaryPath(), "has-session", "-t", ownedSession)
	alternateOwned.Env = alternateEnv
	if err := alternateOwned.Run(); err == nil {
		t.Fatal("owned session creation was redirected to alternate root")
	}
}

func TestIsolationRejectsRoutingChangeAfterSetup(t *testing.T) {
	cleanup, err := SetupIsolatedTmuxTestProcess()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Error(err)
		}
	})

	if !isolatedTmuxReady() {
		t.Fatal("process-owned isolation should be ready after setup")
	}
	originalRoot := os.Getenv("TMUX_TMPDIR")
	t.Setenv("TMUX_TMPDIR", "/tmp")
	if isolatedTmuxReady() {
		t.Fatal("routing change after setup must revoke mutation authorization")
	}
	t.Setenv("TMUX_TMPDIR", originalRoot)
	if !isolatedTmuxReady() {
		t.Fatal("restoring the exact process-owned root should restore authorization")
	}
}
