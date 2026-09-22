package checkpoint

import (
	"context"
	"errors"
	"fmt"
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
	t.Setenv("NTM_RESTORE_TEST_LAUNCHED", filepath.Join(dir, "launched"))
	t.Setenv("NTM_RESTORE_TEST_PAYLOAD", filepath.Join(dir, "payload"))
	t.Setenv("NTM_RESTORE_TEST_DELIVERIES", filepath.Join(dir, "deliveries"))
	t.Setenv("NTM_RESTORE_TEST_LAUNCH_LOG", filepath.Join(dir, "launches"))
	t.Setenv("NTM_RESTORE_TEST_OBSERVATIONS", filepath.Join(dir, "observations"))
	t.Setenv("NTM_RESTORE_TEST_START_COMMAND", "claude")
	t.Setenv("NTM_RESTORE_TEST_START_DEAD", "0")
	t.Setenv("NTM_RESTORE_TEST_INJECT_STATE", "")
	t.Setenv("NTM_RESTORE_TEST_BLOCK", "")
	t.Setenv("NTM_RESTORE_TEST_FAIL", "")
	t.Setenv("NTM_RESTORE_TEST_EXISTS", "")
	bin := filepath.Join(dir, "tmux")
	const script = `#!/bin/sh
printf '%s\n' "$1" >> "$NTM_RESTORE_TEST_LOG"
if [ "$1" = "$NTM_RESTORE_TEST_BLOCK" ] && { [ "$1" != list-panes ] || [ -f "$NTM_RESTORE_TEST_LAUNCHED" ]; }; then
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
    state=''
    if [ -f "$NTM_RESTORE_TEST_LAUNCHED" ]; then
      observations=$(cat "$NTM_RESTORE_TEST_OBSERVATIONS")
      observations=$((observations + 1))
      echo "$observations" > "$NTM_RESTORE_TEST_OBSERVATIONS"
      if [ "$observations" -gt 2 ]; then state="$NTM_RESTORE_TEST_INJECT_STATE"; fi
    fi
    if [ "$state" = read-error ]; then echo 'fixture read failed' >&2; exit 2; fi
    i=0
    while [ "$i" -lt "$n" ]; do
      id="$i"
      index="$i"
      command="$NTM_RESTORE_TEST_START_COMMAND"
      type=cc
      dead="$NTM_RESTORE_TEST_START_DEAD"
      case "$state" in
        shell) command=bash ;;
        dead) dead=1 ;;
        empty) command='' ;;
        starting) command=tmux ;;
        retagged) type=cod ;;
        missing) i=$((i + 1)); continue ;;
        replacement) id=$((i + 99)) ;;
        reordered) index=$((n - i - 1)) ;;
      esac
      printf '%%%s_NTM_SEP_%s_NTM_SEP__NTM_SEP_%s_NTM_SEP_80_NTM_SEP_24_NTM_SEP_1_NTM_SEP_0_NTM_SEP_0_NTM_SEP_%s_NTM_SEP__NTM_SEP__NTM_SEP_%s\n' "$id" "$index" "$command" "$type" "$dead"
      i=$((i + 1))
    done ;;
  respawn-pane)
    printf '%s\n' "$*" >> "$NTM_RESTORE_TEST_LAUNCH_LOG"
    echo launched > "$NTM_RESTORE_TEST_LAUNCHED"
    echo 0 > "$NTM_RESTORE_TEST_OBSERVATIONS" ;;
  load-buffer) cat >> "$NTM_RESTORE_TEST_PAYLOAD" ;;
  paste-buffer|send-keys) printf '%s\n' "$*" >> "$NTM_RESTORE_TEST_DELIVERIES" ;;
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
	for _, operation := range []string{"new-session", "split-window", "respawn-pane", "list-panes"} {
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
		titleIndex       int
	}{
		{"split-window", "restoring_layout", 1, -1},
		{"respawn-pane", "starting_agents", 2, -1},
		{"select-pane", "creating_session", 1, 0},
		{"select-pane", "restoring_layout", 2, 1},
	} {
		t.Run(tc.stage+"/"+tc.operation, func(t *testing.T) {
			log, _ := restoreLifecycleFixture(t)
			t.Setenv("NTM_RESTORE_TEST_FAIL", tc.operation)
			cp := lifecycleCheckpoint(t, 2)
			if tc.titleIndex >= 0 {
				cp.Session.Panes[tc.titleIndex].Title = "restore_lifecycle__cc_1"
			}
			out, err := NewRestorer().RestoreFromCheckpointContext(context.Background(), cp, RestoreOptions{SkipGitCheck: true, InjectContext: true})
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
	for _, op := range []string{"new-session", "split-window", "respawn-pane", "list-panes"} {
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

func saveLifecycleScrollback(t *testing.T, cp *Checkpoint, contents []string) *Storage {
	t.Helper()
	cp.ID = "context-safety"
	cp.Version = CurrentVersion
	cp.CreatedAt = time.Now()
	cp.PaneCount = len(cp.Session.Panes)
	storage := NewStorageWithDir(t.TempDir())
	if err := storage.Save(cp); err != nil {
		t.Fatal(err)
	}
	for i, content := range contents {
		cp.Session.Panes[i].ID = fmt.Sprintf("%%%d", i+100)
		path, err := storage.SaveScrollback(cp.SessionName, cp.ID, cp.Session.Panes[i].ID, content)
		if err != nil {
			t.Fatal(err)
		}
		cp.Session.Panes[i].ScrollbackFile = path
	}
	return storage
}

func TestRestoreLifecycleInjectsContextOnlyIntoAgents(t *testing.T) {
	log, _ := restoreLifecycleFixture(t)
	cp := lifecycleCheckpoint(t, 4)
	cp.Session.Panes[0].AgentType = "user"
	cp.Session.Panes[0].Command = "bash"
	cp.Session.Panes[2].AgentType = "unknown"
	cp.Session.Panes[2].Command = "claude"
	cp.Session.Panes[3].AgentType = "unrecognized-type"
	cp.Session.Panes[3].Command = "claude"
	storage := saveLifecycleScrollback(t, cp, []string{
		"printf 'shell transcript must never run'\n",
		"Only this agent context should be delivered",
		"unknown pane transcript must never be submitted",
		"unrecognized pane transcript must never be submitted",
	})
	out, err := NewRestorerWithStorage(storage).RestoreFromCheckpointContext(context.Background(), cp, RestoreOptions{
		SkipGitCheck: true, InjectContext: true,
	})
	if err != nil || out == nil || out.Stage != "completed" || !out.ContextInjected || out.ContextPanesInjected != 1 {
		t.Fatalf("mixed pane restore failed: %+v, %v", out, err)
	}
	payload, err := os.ReadFile(os.Getenv("NTM_RESTORE_TEST_PAYLOAD"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "Only this agent context") || strings.Contains(string(payload), "transcript") {
		t.Fatalf("non-agent scrollback reached transport: %s", payload)
	}
	deliveries, err := os.ReadFile(os.Getenv("NTM_RESTORE_TEST_DELIVERIES"))
	if err != nil {
		t.Fatal(err)
	}
	for _, delivery := range strings.Split(strings.TrimSpace(string(deliveries)), "\n") {
		if !strings.Contains(delivery, "-t %1") {
			t.Fatalf("context was sent to a non-agent pane: %s", deliveries)
		}
	}
	calls, _ := os.ReadFile(log)
	if strings.Count(string(calls), "load-buffer") != 1 || strings.Count(string(calls), "paste-buffer") != 1 {
		t.Fatalf("expected exactly one context delivery: %s", calls)
	}
}

func TestRestoreLifecycleRechecksContextRecipient(t *testing.T) {
	for _, state := range []string{"shell", "dead", "empty", "starting", "retagged", "missing", "replacement", "read-error"} {
		t.Run(state, func(t *testing.T) {
			log, _ := restoreLifecycleFixture(t)
			t.Setenv("NTM_RESTORE_TEST_INJECT_STATE", state)
			cp := lifecycleCheckpoint(t, 1)
			storage := saveLifecycleScrollback(t, cp, []string{"agent context"})
			out, err := NewRestorerWithStorage(storage).RestoreFromCheckpointContext(context.Background(), cp, RestoreOptions{
				SkipGitCheck: true, InjectContext: true,
			})
			if err == nil || out == nil || out.Stage != "injecting_context" || out.ContextInjected || out.ContextPanesInjected != 0 {
				t.Fatalf("unsafe/unavailable recipient was accepted: %+v, %v", out, err)
			}
			calls, _ := os.ReadFile(log)
			if strings.Contains(string(calls), "load-buffer") || strings.Contains(string(calls), "paste-buffer") || strings.Contains(string(calls), "send-keys") {
				t.Fatalf("unsafe/unavailable pane received context: %s", calls)
			}
		})
	}
}

func TestRestoreLifecycleContextFollowsCreatedPaneIDs(t *testing.T) {
	restoreLifecycleFixture(t)
	t.Setenv("NTM_RESTORE_TEST_INJECT_STATE", "reordered")
	cp := lifecycleCheckpoint(t, 2)
	storage := saveLifecycleScrollback(t, cp, []string{"first agent context", "second agent context"})
	out, err := NewRestorerWithStorage(storage).RestoreFromCheckpointContext(context.Background(), cp, RestoreOptions{
		SkipGitCheck: true, InjectContext: true,
	})
	if err != nil || out == nil || out.ContextPanesInjected != 2 {
		t.Fatalf("restore with reordered panes failed: %+v, %v", out, err)
	}
	deliveries, err := os.ReadFile(os.Getenv("NTM_RESTORE_TEST_DELIVERIES"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(deliveries)), "\n")
	if len(lines) != 4 || !strings.Contains(lines[0], "-t %0") || !strings.Contains(lines[2], "-t %1") {
		t.Fatalf("reordered pane indexes redirected saved context: %s", deliveries)
	}
}

func TestRestoreLifecycleReportsOnlyCompletedContextDeliveries(t *testing.T) {
	t.Run("no captured agent context", func(t *testing.T) {
		log, _ := restoreLifecycleFixture(t)
		cp := lifecycleCheckpoint(t, 1)
		out, err := NewRestorer().RestoreFromCheckpointContext(context.Background(), cp, RestoreOptions{SkipGitCheck: true, InjectContext: true})
		if err != nil || out == nil || out.ContextInjected || out.ContextPanesInjected != 0 {
			t.Fatalf("restore claimed a nonexistent delivery: %+v, %v", out, err)
		}
		calls, _ := os.ReadFile(log)
		if strings.Contains(string(calls), "load-buffer") {
			t.Fatalf("restore sent uncaptured context: %s", calls)
		}
	})
	t.Run("dry run", func(t *testing.T) {
		restoreLifecycleFixture(t)
		cp := lifecycleCheckpoint(t, 1)
		storage := saveLifecycleScrollback(t, cp, []string{"agent context"})
		out, err := NewRestorerWithStorage(storage).RestoreFromCheckpointContext(context.Background(), cp, RestoreOptions{DryRun: true, InjectContext: true})
		if err != nil || out == nil || out.ContextInjected || out.ContextPanesInjected != 0 {
			t.Fatalf("dry run claimed delivery: %+v, %v", out, err)
		}
	})
	t.Run("partial failure", func(t *testing.T) {
		restoreLifecycleFixture(t)
		cp := lifecycleCheckpoint(t, 3)
		storage := saveLifecycleScrollback(t, cp, []string{"delivered agent context"})
		cp.Session.Panes[1].ScrollbackFile = "panes/missing-first.txt"
		cp.Session.Panes[2].ScrollbackFile = "panes/missing-second.txt"
		out, err := NewRestorerWithStorage(storage).RestoreFromCheckpointContext(context.Background(), cp, RestoreOptions{SkipGitCheck: true, InjectContext: true})
		if err == nil || out == nil || !out.ContextInjected || out.ContextPanesInjected != 1 || out.Stage != "injecting_context" {
			t.Fatalf("partial delivery was lost: %+v, %v", out, err)
		}
		if !strings.Contains(err.Error(), "checkpoint pane 1") || !strings.Contains(err.Error(), "checkpoint pane 2") {
			t.Fatalf("restore discarded individual delivery failures: %v", err)
		}
	})
}

func TestRestoreLifecycleReconstructsBareRuntimeAgentLaunch(t *testing.T) {
	for _, tc := range []struct {
		name, agentType, captured, launched string
		fallback                            bool
	}{
		{"node-backed codex", "codex", "node", "codex", true},
		{"bun-backed claude", "cc", "/usr/bin/bun", "claude", true},
		{"versioned python", "aider", `"/opt/python3.13"`, "aider", true},
		{"node-backed cursor", "cursor", "node", "cursor-agent", true},
		{"explicit node script", "codex", `node "/opt/my agents/codex.js" --model o3`, `node "/opt/my agents/codex.js" --model o3`, false},
		{"explicit runtime flags and script", "cc", `bun --cwd "/my project" /opt/agent.js`, `bun --cwd "/my project" /opt/agent.js`, false},
		{"explicit env and script", "cc", `env FOO=bar node /opt/agent.js`, `env FOO=bar node /opt/agent.js`, false},
		{"custom launcher", "cc", `/opt/custom-launcher --fast`, `/opt/custom-launcher --fast`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log, _ := restoreLifecycleFixture(t)
			// The launched CLI is observed under its interpreter. It need not
			// retain the basename of the command that started it.
			t.Setenv("NTM_RESTORE_TEST_START_COMMAND", "node")
			cp := lifecycleCheckpoint(t, 1)
			cp.Session.Panes[0].AgentType, cp.Session.Panes[0].Command = tc.agentType, tc.captured
			out, err := NewRestorer().RestoreFromCheckpointContext(context.Background(), cp, RestoreOptions{SkipGitCheck: true})
			if err != nil || out == nil || out.Stage != "completed" {
				t.Fatalf("runtime-backed restoration failed: %+v, %v", out, err)
			}
			launches, err := os.ReadFile(os.Getenv("NTM_RESTORE_TEST_LAUNCH_LOG"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(strings.TrimSpace(string(launches)), " "+tc.launched) {
				t.Fatalf("restored command changed or omitted launch argv: %s; want %s", launches, tc.launched)
			}
			fallbackWarned := false
			for _, warning := range out.Warnings {
				fallbackWarned = fallbackWarned || strings.Contains(warning, "without launch arguments")
			}
			if fallbackWarned != tc.fallback {
				t.Fatalf("fallback warning = %v, want %v: %v", fallbackWarned, tc.fallback, out.Warnings)
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(calls), "respawn-pane") != 1 {
				t.Fatalf("startup observation killed a healthy wrapped agent: %s", calls)
			}
		})
	}
}

func TestRestoreLifecyclePreservesPaneAfterUncertainStartup(t *testing.T) {
	for _, failure := range []string{"launch error", "idle shell", "empty command", "dead process"} {
		t.Run(failure, func(t *testing.T) {
			log, _ := restoreLifecycleFixture(t)
			switch failure {
			case "launch error":
				t.Setenv("NTM_RESTORE_TEST_FAIL", "respawn-pane")
			case "idle shell":
				t.Setenv("NTM_RESTORE_TEST_START_COMMAND", "bash")
			case "empty command":
				t.Setenv("NTM_RESTORE_TEST_START_COMMAND", "")
			case "dead process":
				t.Setenv("NTM_RESTORE_TEST_START_DEAD", "1")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			out, err := NewRestorer().RestoreFromCheckpointContext(ctx, lifecycleCheckpoint(t, 1), RestoreOptions{SkipGitCheck: true, InjectContext: true})
			if err == nil || out == nil || out.Stage != "starting_agents" || out.ContextInjected {
				t.Fatalf("startup failure claimed success: %+v, %v", out, err)
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(calls), "respawn-pane") != 1 || strings.Contains(string(calls), "kill-session") || strings.Contains(string(calls), "paste-buffer") {
				t.Fatalf("uncertain startup was retried destructively or received context: %s", calls)
			}
		})
	}
}
