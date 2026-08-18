//go:build darwin

package process

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestNativeChildStatesSeesSpawnedChild proves the sysctl snapshot path finds
// a real child of this process and agrees with the portable helpers, so the
// subprocess-free fast path (bd-4479y) answers the same liveness question the
// pgrep/ps fallback did.
func TestNativeChildStatesSeesSpawnedChild(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	childPID := cmd.Process.Pid
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	self := os.Getpid()

	pids, ok := nativeChildPIDs(self)
	if !ok {
		t.Fatal("nativeChildPIDs reported no native snapshot on darwin")
	}
	found := false
	for _, pid := range pids {
		if pid == childPID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("nativeChildPIDs(%d) = %v, missing spawned child %d", self, pids, childPID)
	}

	alive, ok := nativeHasChildAlive(self)
	if !ok || !alive {
		t.Fatalf("nativeHasChildAlive(%d) = (%v, %v), want (true, true)", self, alive, ok)
	}
	if !HasChildAlive(self) {
		t.Fatalf("HasChildAlive(%d) = false with live child %d", self, childPID)
	}

	if alive, ok := nativeIsAlive(childPID); !ok || !alive {
		t.Fatalf("nativeIsAlive(%d) = (%v, %v), want (true, true)", childPID, alive, ok)
	}
	if !IsAlive(childPID) {
		t.Fatalf("IsAlive(%d) = false for running child", childPID)
	}

	// Kill and reap: a fully-reaped child must read as gone.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill child: %v", err)
	}
	if _, err := cmd.Process.Wait(); err != nil {
		t.Fatalf("wait child: %v", err)
	}
	// The kernel removes the reaped process promptly; poll briefly to avoid
	// asserting on a scheduler race.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !IsAlive(childPID) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("IsAlive(%d) still true after child was killed and reaped", childPID)
}
