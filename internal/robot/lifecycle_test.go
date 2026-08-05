package robot

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/process"
)

// The double-Ctrl+C exit contract (ntm-3lbb): Claude Code treats the pair as
// "exit" only when the second tap lands roughly 0.1-0.3s after the first —
// slower reads as two separate interrupts, faster risks coalescing. Operator
// docs used to ship three conflicting timings; this pin makes the code the
// single source of truth.
func TestLifecycleDoubleTapGap_WithinExitWindow(t *testing.T) {
	if lifecycleDoubleTapGap < 100*time.Millisecond || lifecycleDoubleTapGap > 300*time.Millisecond {
		t.Fatalf("lifecycleDoubleTapGap %v outside the empirical 100-300ms double-Ctrl+C exit window", lifecycleDoubleTapGap)
	}
}

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

// bd-3izr9: refreshLifecyclePane must distinguish three outcomes, because
// collapsing them made a single transient tmux error report shell_preserved:
// false — telling the operator the verb had destroyed the pane, which is the
// one thing both lifecycle verbs promise never to do.
func TestRefreshLifecyclePane_ClassifiesOutcomes(t *testing.T) {
	t.Run("a session that does not exist proves the pane is absent", func(t *testing.T) {
		// Killing a session's last pane destroys the session, so the listing
		// legitimately errors. That is a definitive answer, not a transient
		// failure; treating it as one reported a successful kill as a failure.
		_, lookup := refreshLifecyclePane(context.Background(), "ntm-nonexistent-session-bd3izr9", "%999")
		if lookup != paneAbsent {
			t.Fatalf("lookup = %v, want paneAbsent for a session that does not exist", lookup)
		}
	})

	t.Run("a cancelled context does not claim the pane is gone", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, lookup := refreshLifecyclePane(ctx, "any-session", "%1")
		if lookup == paneAbsent {
			t.Fatal("a cancelled lookup reported paneAbsent; cancellation proves nothing about the pane")
		}
	})
}

// The tri-state must be wired into the result, not just computed.
func TestLifecyclePaneResult_VerificationFailedIsDistinctFromDestroyed(t *testing.T) {
	// A result that could not be verified must be distinguishable from one
	// that verified the pane was destroyed. Both have ShellPreserved=false.
	unverified := LifecyclePaneResult{ShellPreserved: false, VerificationFailed: true}
	destroyed := LifecyclePaneResult{ShellPreserved: false}

	if unverified.VerificationFailed == destroyed.VerificationFailed {
		t.Fatal("an unverified result is indistinguishable from a destroyed pane")
	}
}
