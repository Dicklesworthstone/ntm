package testutil

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

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

func TestSharedHelpersIgnoreRouteSwap(t *testing.T) {
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
	ownedSession := "ntm_test_shared_owned"
	ownedCreate := exec.Command(tmux.BinaryPath(), "new-session", "-d", "-s", ownedSession)
	ownedCreate.Env = ownedEnv
	if out, err := ownedCreate.CombinedOutput(); err != nil {
		t.Fatalf("create owned sentinel: %v: %s", err, out)
	}
	t.Cleanup(func() {
		cmd := exec.Command(tmux.BinaryPath(), "kill-session", "-t", ownedSession)
		cmd.Env = ownedEnv
		_ = cmd.Run()
	})

	alternateRoot := filepath.Join(t.TempDir(), "alternate-shared")
	if err := os.MkdirAll(alternateRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	alternateEnv := filterEnv(os.Environ(), "TMUX")
	alternateEnv = filterEnv(alternateEnv, "TMUX_TMPDIR")
	alternateEnv = append(alternateEnv, "TMUX_TMPDIR="+alternateRoot)
	alternateSession := "ntm_test_shared_alternate"
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

	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", alternateRoot)
	RequireTmuxServer(t)
	probeInventory, err := tmuxSessionInventory(ownedEnv)
	if err != nil {
		t.Fatalf("owned-root server inventory: %v: %s", err, probeInventory)
	}
	if !inventoryHasSession(probeInventory, ownedSession) {
		t.Fatalf("RequireTmuxServer route oracle missed owned sentinel: %q", probeInventory)
	}
	if inventoryHasSession(probeInventory, alternateSession) {
		t.Fatalf("RequireTmuxServer route oracle reached alternate sentinel: %q", probeInventory)
	}

	// Seed the exact binding-removal mutant: a probe inheriting the swapped
	// process environment must be distinguishable from the owned-root probe.
	// Removing cmd.Env = env in tmuxSessionInventory makes the owned probe take
	// this route and causes the assertions above to fail deterministically.
	mutantInventory, err := tmuxSessionInventory(os.Environ())
	if err != nil {
		t.Fatalf("binding-removal mutant inventory: %v: %s", err, mutantInventory)
	}
	if !inventoryHasSession(mutantInventory, alternateSession) || inventoryHasSession(mutantInventory, ownedSession) {
		t.Fatalf("binding-removal mutant was not routed to alternate root: %q", mutantInventory)
	}
	logger := NewTestLoggerStdout(t)
	createdByLogger := "ntm_test_shared_logger_create"
	if _, err := logger.Exec(tmux.BinaryPath(), "new-session", "-d", "-s", createdByLogger); err != nil {
		t.Fatalf("logger create did not use owned root: %v", err)
	}
	if _, err := logger.Exec(tmux.BinaryPath(), "list-sessions", "-F", "#{session_name}"); err != nil {
		t.Fatalf("logger list did not use owned root: %v", err)
	}
	if _, err := logger.Exec(tmux.BinaryPath(), "capture-pane", "-p", "-t", createdByLogger); err != nil {
		t.Fatalf("logger capture did not use owned root: %v", err)
	}
	if _, err := logger.Exec(tmux.BinaryPath(), "has-session", "-t", ownedSession); err != nil {
		t.Fatalf("logger did not use owned root: %v", err)
	}
	if _, err := logger.ExecContext(time.Second, tmux.BinaryPath(), "has-session", "-t", alternateSession); err == nil {
		t.Fatal("logger ExecContext reached alternate-root sentinel")
	}
	AssertSessionExists(t, logger, ownedSession)
	AssertSessionNotExists(t, logger, alternateSession)
	if _, err := logger.Exec(tmux.BinaryPath(), "kill-session", "-t", createdByLogger); err != nil {
		t.Fatalf("logger exact kill did not use owned root: %v", err)
	}
	AssertSessionNotExists(t, logger, createdByLogger)
	// This name exists only on the alternate root. Both the ntm attempt and its
	// tmux fallback must stay on the process-owned root.
	killSession(logger, alternateSession)
	KillAllTestSessions(logger)

	alternateHas := exec.Command(tmux.BinaryPath(), "has-session", "-t", alternateSession)
	alternateHas.Env = alternateEnv
	if err := alternateHas.Run(); err != nil {
		t.Fatal("alternate-root sentinel did not survive shared helper probes")
	}
}

func inventoryHasSession(inventory []byte, session string) bool {
	for _, line := range bytes.Split(inventory, []byte{'\n'}) {
		if bytes.Equal(line, []byte(session)) {
			return true
		}
	}
	return false
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
