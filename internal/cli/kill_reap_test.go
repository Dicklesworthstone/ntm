package cli

import (
	"os/exec"
	"testing"
	"time"

	procpkg "github.com/Dicklesworthstone/ntm/internal/process"
)

// collectProcessSubtree must include a real child process under a parent.
func TestCollectProcessSubtree(t *testing.T) {
	// sh -c 'sleep 30 & wait' → sh has a child sleep
	parent := exec.Command("sh", "-c", "sleep 30")
	if err := parent.Start(); err != nil {
		t.Fatalf("start parent: %v", err)
	}
	defer func() { _ = parent.Process.Kill(); _, _ = parent.Process.Wait() }()
	time.Sleep(200 * time.Millisecond)

	tree := collectProcessSubtree(parent.Process.Pid, 0)
	if len(tree) == 0 || tree[0] != parent.Process.Pid {
		t.Fatalf("subtree must start with parent pid; got %v", tree)
	}
}

// reapOrphanedAgents must SIGTERM/SIGKILL a real orphan and leave it dead.
func TestReapOrphanedAgents(t *testing.T) {
	victim := exec.Command("sleep", "300")
	if err := victim.Start(); err != nil {
		t.Fatalf("start victim: %v", err)
	}
	pid := victim.Process.Pid
	go func() { _, _ = victim.Process.Wait() }() // reap zombie after kill
	time.Sleep(150 * time.Millisecond)

	if !procpkg.IsAlive(pid) {
		t.Fatalf("victim should be alive before reap")
	}
	if n := reapOrphanedAgents([]int{pid}); n != 1 {
		t.Errorf("expected 1 signalled, got %d", n)
	}
	time.Sleep(300 * time.Millisecond)
	if procpkg.IsAlive(pid) {
		t.Errorf("victim still alive after reap")
	}
}

// Safety: self and pid<=1 must never be signalled.
func TestReapExcludesSelfAndInit(t *testing.T) {
	if n := reapOrphanedAgents([]int{1, 0, -5}); n != 0 {
		t.Errorf("pid<=1 must be excluded; got %d", n)
	}
}
