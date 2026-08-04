package robot

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/process"
)

func TestJoinLifecycleDetail(t *testing.T) {
	if got := joinLifecycleDetail("", "a"); got != "a" {
		t.Fatalf("empty existing: got %q", got)
	}
	if got := joinLifecycleDetail("a", "b"); got != "a; b" {
		t.Fatalf("joined: got %q", got)
	}
}

func TestKillProcessesGracefully_TermsCooperativeProcess(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	killed := killProcessesGracefully(context.Background(), []int{pid})
	// Reap the zombie so IsAlive-based assertions see the true state.
	_, _ = cmd.Process.Wait()

	if len(killed) != 1 || killed[0] != pid {
		t.Fatalf("expected killed=[%d], got %v", pid, killed)
	}
	deadline := time.Now().Add(2 * time.Second)
	for process.IsAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if process.IsAlive(pid) {
		t.Fatalf("pid %d still alive after graceful kill", pid)
	}
}

func TestLifecycleVerbs_RequireSession(t *testing.T) {
	out, err := GetExitCLI(context.Background(), LifecycleOptions{})
	if err != nil {
		t.Fatalf("GetExitCLI: %v", err)
	}
	if out.Success || out.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("expected INVALID_FLAG for empty session, got success=%v code=%s", out.Success, out.ErrorCode)
	}
	kout, err := GetKillAgent(context.Background(), LifecycleOptions{})
	if err != nil {
		t.Fatalf("GetKillAgent: %v", err)
	}
	if kout.Success || kout.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("expected INVALID_FLAG for empty session, got success=%v code=%s", kout.Success, kout.ErrorCode)
	}
}
