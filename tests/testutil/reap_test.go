package testutil

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

func isolatedTmuxSessionExists(bin, socketDir, session string) bool {
	cmd := exec.Command(bin, "has-session", "-t", session)
	cmd.Env = isolatedTmuxEnvironment(socketDir)
	return cmd.Run() == nil
}

func startIsolatedTmuxSession(t *testing.T, bin, socketDir, session string) {
	t.Helper()
	cmd := exec.Command(bin, "new-session", "-d", "-s", session, "-x", "80", "-y", "24")
	cmd.Env = isolatedTmuxEnvironment(socketDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("start isolated tmux session %q: %v: %s", session, err, out)
	}
}

// TestReapStaleTmuxTestServersRemovesLeakedServerButSparesFresh exercises the
// startup reaper the e2e TestMain relies on: a socket dir older than the floor,
// with a live ntm-e2e-* server on it, is killed and removed; a freshly created
// one is left alone. This is the AC2 invariant for the mid-run-kill case — no
// ntm-e2e-* session survives past the reap floor — and pins the age guard that
// keeps a concurrent in-flight run safe.
func TestReapStaleTmuxTestServersRemovesLeakedServerButSparesFresh(t *testing.T) {
	RequireTmux(t)
	bin := findSystemTmuxBinary()
	if bin == "" {
		t.Skip("no system tmux binary available")
	}

	// A leaked server: real ntm-e2e-* session whose socket dir has aged past the floor.
	staleDir, err := createShortTmuxTempDir()
	if err != nil {
		t.Fatalf("create stale socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(staleDir) })
	const staleSession = "ntm-e2e-reap-stale"
	startIsolatedTmuxSession(t, bin, staleDir, staleSession)
	if !isolatedTmuxSessionExists(bin, staleDir, staleSession) {
		t.Fatalf("precondition: stale session should exist before reaping")
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(staleDir, old, old); err != nil {
		t.Fatalf("age the stale socket dir: %v", err)
	}

	// A fresh server: created just now; the age guard must spare it.
	freshDir, err := createShortTmuxTempDir()
	if err != nil {
		t.Fatalf("create fresh socket dir: %v", err)
	}
	t.Cleanup(func() {
		cmd := exec.Command(bin, "kill-server")
		cmd.Env = isolatedTmuxEnvironment(freshDir)
		_ = cmd.Run()
		_ = os.RemoveAll(freshDir)
	})
	const freshSession = "ntm-e2e-reap-fresh"
	startIsolatedTmuxSession(t, bin, freshDir, freshSession)

	reaped := ReapStaleTmuxTestServers(time.Hour)
	if reaped < 1 {
		t.Fatalf("expected the reaper to remove at least the aged socket dir, got %d", reaped)
	}

	if isolatedTmuxSessionExists(bin, staleDir, staleSession) {
		t.Errorf("stale ntm-e2e session %q survived the reaper", staleSession)
	}
	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Errorf("stale socket dir was not removed (stat err = %v)", err)
	}

	if _, err := os.Stat(freshDir); err != nil {
		t.Errorf("fresh socket dir was removed despite the age guard: %v", err)
	}
	if !isolatedTmuxSessionExists(bin, freshDir, freshSession) {
		t.Errorf("fresh ntm-e2e session %q was killed despite the age guard", freshSession)
	}
}
