package checkpoint

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// The fixture emits the real list-panes schema and recognized absence errors.
// Blocking a tmux subprocess proves cancellation reaches an in-flight command,
// rather than merely changing an in-memory status after all mutations finish.
func restoreLifecycleFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	log, ready := filepath.Join(dir, "calls"), filepath.Join(dir, "ready")
	t.Setenv("NTM_RESTORE_TEST_LOG", log)
	t.Setenv("NTM_RESTORE_TEST_READY", ready)
	t.Setenv("NTM_RESTORE_TEST_COUNT", filepath.Join(dir, "count"))
	t.Setenv("NTM_RESTORE_TEST_BLOCK", "")
	t.Setenv("NTM_RESTORE_TEST_FAIL", "")
	t.Setenv("NTM_RESTORE_TEST_EXISTS", "")
	bin := filepath.Join(dir, "tmux")
	const script = `#!/bin/sh
printf '%s\n' "$1" >> "$NTM_RESTORE_TEST_LOG"
if [ "$1" = "$NTM_RESTORE_TEST_BLOCK" ]; then
  echo ready > "$NTM_RESTORE_TEST_READY"
  exec sleep 30
fi
if [ "$1" = "$NTM_RESTORE_TEST_FAIL" ]; then
  echo 'fixture operation failed' >&2
  exit 2
fi
case "$1" in
  has-session)
    if [ "$NTM_RESTORE_TEST_EXISTS" = 1 ]; then exit 0; fi
    echo "can't find session: restore_lifecycle" >&2
    exit 1 ;;
  new-session) echo 1 > "$NTM_RESTORE_TEST_COUNT" ;;
  split-window|new-window)
    n=$(cat "$NTM_RESTORE_TEST_COUNT")
    echo $((n + 1)) > "$NTM_RESTORE_TEST_COUNT"
    printf '%%%s\n' "$n" ;;
  list-windows) echo 0 ;;
  list-panes)
    n=$(cat "$NTM_RESTORE_TEST_COUNT")
    i=0
    while [ "$i" -lt "$n" ]; do
      printf '%%%s_NTM_SEP_%s_NTM_SEP__NTM_SEP_claude_NTM_SEP_80_NTM_SEP_24_NTM_SEP_1_NTM_SEP_0_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0\n' "$i" "$i"
      i=$((i + 1))
    done ;;
  display-message) echo claude ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", bin)
	old := tmux.DefaultClient
	tmux.DefaultClient = tmux.NewClient("")
	t.Cleanup(func() { tmux.DefaultClient = old })
	return log, ready
}

func lifecycleCheckpoint(t *testing.T, count int) *Checkpoint {
	t.Helper()
	cp := &Checkpoint{SessionName: "restore_lifecycle", WorkingDir: t.TempDir()}
	for i := 0; i < count; i++ {
		cp.Session.Panes = append(cp.Session.Panes, PaneState{Index: i, WindowIndex: 0, AgentType: "cc", Command: "claude"})
	}
	return cp
}

func TestRestoreLifecyclePreflightDoesNotReplaceSession(t *testing.T) {
	log, _ := restoreLifecycleFixture(t)
	t.Setenv("NTM_RESTORE_TEST_EXISTS", "1")
	r := NewRestorer()
	for _, tc := range []struct {
		name string
		cp   *Checkpoint
		opts RestoreOptions
	}{
		{"empty", lifecycleCheckpoint(t, 0), RestoreOptions{Force: true}},
		{"invalid name", &Checkpoint{SessionName: "bad:name", Session: SessionState{Panes: []PaneState{{AgentType: "user"}}}}, RestoreOptions{Force: true}},
		{"invalid command", &Checkpoint{SessionName: "restore_lifecycle", Session: SessionState{Panes: []PaneState{{AgentType: "cc", Command: "claude\x00bad"}}}}, RestoreOptions{Force: true}},
		{"negative scrollback", lifecycleCheckpoint(t, 1), RestoreOptions{Force: true, ScrollbackLines: -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.RestoreFromCheckpointContext(context.Background(), tc.cp, tc.opts); err == nil {
				t.Fatal("invalid restore request was accepted")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.RestoreFromCheckpointContext(ctx, lifecycleCheckpoint(t, 1), RestoreOptions{Force: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancellation lost: %v", err)
	}
	if data, err := os.ReadFile(log); err == nil && len(data) != 0 {
		t.Fatalf("preflight failure reached tmux: %s", data)
	}
}

func TestRestoreLifecycleCancelsInFlightOperations(t *testing.T) {
	for _, operation := range []string{"new-session", "split-window", "respawn-pane", "display-message"} {
		t.Run(operation, func(t *testing.T) {
			log, ready := restoreLifecycleFixture(t)
			t.Setenv("NTM_RESTORE_TEST_BLOCK", operation)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cp := lifecycleCheckpoint(t, 2)
			type outcome struct {
				result *RestoreResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				result, err := NewRestorer().RestoreFromCheckpointContext(ctx, cp, RestoreOptions{SkipGitCheck: true, InjectContext: true})
				done <- outcome{result, err}
			}()
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				select {
				case out := <-done:
					t.Fatalf("restore returned before %s rendezvous: %+v, %v", operation, out.result, out.err)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatalf("%s did not start", operation)
				}
				time.Sleep(5 * time.Millisecond)
			}
			cancel()
			select {
			case out := <-done:
				if !errors.Is(out.err, context.Canceled) || out.result == nil || !out.result.Interrupted {
					t.Fatalf("missing interruption/error/partial result: %+v, %v", out.result, out.err)
				}
				if out.result.ContextInjected || out.result.Stage == "completed" {
					t.Fatalf("cancelled restore claimed completion: %+v", out.result)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("cancellation did not stop the in-flight tmux command")
			}
			data, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			calls := strings.Fields(string(data))
			if len(calls) == 0 || calls[len(calls)-1] != operation {
				t.Fatalf("restore performed more operations after cancellation: %v", calls)
			}
			if strings.Contains(string(data), "kill-session") {
				t.Fatalf("cancelled restore destroyed partially restored session: %s", data)
			}
		})
	}
}

func TestRestoreLifecycleReportsPartialFailure(t *testing.T) {
	for _, tc := range []struct {
		operation, stage string
		panes            int
	}{
		{"split-window", "restoring_layout", 1},
		{"respawn-pane", "starting_agents", 2},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			log, _ := restoreLifecycleFixture(t)
			t.Setenv("NTM_RESTORE_TEST_FAIL", tc.operation)
			out, err := NewRestorer().RestoreFromCheckpointContext(context.Background(), lifecycleCheckpoint(t, 2), RestoreOptions{SkipGitCheck: true, InjectContext: true})
			if err == nil || out == nil || out.PanesRestored != tc.panes || out.Stage != tc.stage {
				t.Fatalf("partial failure was hidden: %+v, %v", out, err)
			}
			if out.ContextInjected || out.Interrupted {
				t.Fatalf("incorrect partial outcome: %+v", out)
			}
			calls, _ := os.ReadFile(log)
			if strings.Contains(string(calls), "paste-buffer") || strings.Contains(string(calls), "kill-session") {
				t.Fatalf("failed restore injected context or destroyed partial work: %s", calls)
			}
		})
	}
}

func TestRestoreLifecycleDryRunAndOperationIsolation(t *testing.T) {
	log, _ := restoreLifecycleFixture(t)
	r := NewRestorer()
	cp := lifecycleCheckpoint(t, 2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = r.RestoreFromCheckpointContext(ctx, cp, RestoreOptions{})
	t.Setenv("NTM_RESTORE_TEST_EXISTS", "1")
	out, err := r.RestoreFromCheckpointContext(context.Background(), cp, RestoreOptions{DryRun: true, Force: true})
	if err != nil || out == nil || out.Stage != "validated" || out.PanesRestored != 2 {
		t.Fatalf("operation-local cancellation leaked: %+v, %v", out, err)
	}
	calls, _ := os.ReadFile(log)
	if strings.TrimSpace(string(calls)) != "has-session" {
		t.Fatalf("dry run mutated tmux: %s", calls)
	}
}

func TestRestoreLifecycleCompletesRealStages(t *testing.T) {
	log, _ := restoreLifecycleFixture(t)
	out, err := NewRestorer().RestoreFromCheckpointContext(context.Background(), lifecycleCheckpoint(t, 2), RestoreOptions{SkipGitCheck: true})
	if err != nil || out == nil || out.Stage != "completed" || out.PanesRestored != 2 || out.Interrupted {
		t.Fatalf("restore failed: %+v, %v", out, err)
	}
	calls, _ := os.ReadFile(log)
	for _, op := range []string{"new-session", "split-window", "respawn-pane", "display-message"} {
		if !strings.Contains(string(calls), op) {
			t.Fatalf("restore skipped %s: %s", op, calls)
		}
	}
}

func TestWaitForRestoreInterruptsDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForRestore(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait = %v", err)
	}
	if err := waitForRestore(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
}
