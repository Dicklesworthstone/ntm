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

func ageTree(t *testing.T, dir string, at time.Time) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read socket root %s: %v", dir, err)
	}
	for _, entry := range entries {
		if err := os.Chtimes(dir+string(os.PathSeparator)+entry.Name(), at, at); err != nil {
			t.Fatalf("age %s: %v", entry.Name(), err)
		}
	}
	if err := os.Chtimes(dir, at, at); err != nil {
		t.Fatalf("age socket root %s: %v", dir, err)
	}
}

// TestReapStaleTmuxTestServersRemovesLeakedServerButSparesFresh exercises the
// startup reaper the e2e TestMain relies on: a socket root aged past the floor,
// with a live server on it, is killed and removed, while a freshly created one
// is left alone. The fresh case pins the age guard that keeps a concurrent
// in-flight run safe — the whole reason the reaper can run unattended.
func TestReapStaleTmuxTestServersRemovesLeakedServerButSparesFresh(t *testing.T) {
	RequireTmux(t)
	bin := findSystemTmuxBinary()
	if bin == "" {
		t.Skip("no system tmux binary available")
	}

	// A leaked server: a real session whose socket root has aged past the floor.
	staleDir, err := createShortTmuxTempDir()
	if err != nil {
		t.Fatalf("create stale socket root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(staleDir) })
	const staleSession = "ntm-reap-stale"
	startIsolatedTmuxSession(t, bin, staleDir, staleSession)
	if !isolatedTmuxSessionExists(bin, staleDir, staleSession) {
		t.Fatalf("precondition: stale session should exist before reaping")
	}
	// Age the socket too: the reaper takes the newest mtime in the root.
	ageTree(t, staleDir, time.Now().Add(-2*time.Hour))

	// A fresh server: created just now; the age guard must spare it.
	freshDir, err := createShortTmuxTempDir()
	if err != nil {
		t.Fatalf("create fresh socket root: %v", err)
	}
	t.Cleanup(func() {
		cmd := exec.Command(bin, "kill-server")
		cmd.Env = isolatedTmuxEnvironment(freshDir)
		_ = cmd.Run()
		_ = os.RemoveAll(freshDir)
	})
	const freshSession = "ntm-reap-fresh"
	startIsolatedTmuxSession(t, bin, freshDir, freshSession)

	if reaped := ReapStaleTmuxTestServers(time.Hour); reaped < 1 {
		t.Fatalf("expected the reaper to remove at least the aged socket root, got %d", reaped)
	}

	if isolatedTmuxSessionExists(bin, staleDir, staleSession) {
		t.Errorf("stale session %q survived the reaper", staleSession)
	}
	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Errorf("stale socket root was not removed (stat err = %v)", err)
	}

	if _, err := os.Stat(freshDir); err != nil {
		t.Errorf("fresh socket root was removed despite the age guard: %v", err)
	}
	if !isolatedTmuxSessionExists(bin, freshDir, freshSession) {
		t.Errorf("fresh session %q was killed despite the age guard", freshSession)
	}
}

// TestReapStaleTmuxTestServersSparesOwnSocketRoot pins the second guard: the
// socket root this process isolated for itself is skipped by name, so a reaper
// call cannot kill the server the calling suite is about to use — even if the
// clock or filesystem makes it look old.
func TestReapStaleTmuxTestServersSparesOwnSocketRoot(t *testing.T) {
	RequireTmux(t)
	bin := findSystemTmuxBinary()
	if bin == "" {
		t.Skip("no system tmux binary available")
	}

	ownDir, err := createShortTmuxTempDir()
	if err != nil {
		t.Fatalf("create own socket root: %v", err)
	}
	t.Cleanup(func() {
		cmd := exec.Command(bin, "kill-server")
		cmd.Env = isolatedTmuxEnvironment(ownDir)
		_ = cmd.Run()
		_ = os.RemoveAll(ownDir)
	})
	const ownSession = "ntm-reap-own"
	startIsolatedTmuxSession(t, bin, ownDir, ownSession)
	ageTree(t, ownDir, time.Now().Add(-48*time.Hour))

	t.Setenv("TMUX_TMPDIR", ownDir)
	ReapStaleTmuxTestServers(time.Hour)

	if _, err := os.Stat(ownDir); err != nil {
		t.Fatalf("the reaper removed the socket root named by TMUX_TMPDIR: %v", err)
	}
	if !isolatedTmuxSessionExists(bin, ownDir, ownSession) {
		t.Fatalf("the reaper killed the server on this process's own socket root")
	}
}

// TestTmuxSocketRootIsStaleUsesNewestEntry pins the age rule itself: a socket
// root whose directory mtime is ancient but whose socket was created moments
// ago belongs to a live run, not to a leak.
func TestTmuxSocketRootIsStaleUsesNewestEntry(t *testing.T) {
	dir := t.TempDir()
	socket := dir + string(os.PathSeparator) + "default"
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatalf("write fake socket: %v", err)
	}

	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(socket, old, old); err != nil {
		t.Fatalf("age socket: %v", err)
	}
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatalf("age dir: %v", err)
	}
	if !tmuxSocketRootIsStale(dir, time.Now().Add(-time.Hour)) {
		t.Fatal("a root whose contents are all old must be stale")
	}

	now := time.Now()
	if err := os.Chtimes(socket, now, now); err != nil {
		t.Fatalf("refresh socket: %v", err)
	}
	if tmuxSocketRootIsStale(dir, time.Now().Add(-time.Hour)) {
		t.Fatal("a root with a recent socket must not be stale, however old the directory is")
	}
}
