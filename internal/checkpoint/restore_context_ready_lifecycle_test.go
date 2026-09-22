package checkpoint

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRestoreReadinessLifecycleWaitsForActualComposer(t *testing.T) {
	log, _ := restoreLifecycleFixture(t)
	t.Setenv("NTM_RESTORE_TEST_BOOT_CAPTURES", "3")
	cp := lifecycleCheckpoint(t, 1)
	storage := saveLifecycleScrollback(t, cp, []string{"restored context payload"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := NewRestorerWithStorage(storage).RestoreFromCheckpointContext(ctx, cp, RestoreOptions{InjectContext: true, SkipGitCheck: true})
	if err != nil || out == nil || out.ContextPanesInjected != 1 || out.Stage != "completed" {
		t.Fatalf("ready restore failed: %+v %v", out, err)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	before, _, ok := strings.Cut(string(calls), "load-buffer")
	if !ok || strings.Count(before, "capture-pane") < 5 || strings.Count(string(calls), "paste-buffer") != 1 {
		t.Fatalf("context preceded stable UI readiness or was repeated: %s", calls)
	}
	captures, err := os.ReadFile(os.Getenv("NTM_RESTORE_TEST_CAPTURE_LOG"))
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range strings.Split(strings.TrimSpace(string(captures)), "\n") {
		if !strings.Contains(call, "-t %0") || !strings.Contains(call, "-S 0") {
			t.Fatalf("readiness captured another pane or historical scrollback: %s", call)
		}
	}
}

func TestRestoreReadinessLifecycleWithholdsContextFromBlockedUI(t *testing.T) {
	for _, tc := range []struct{ name, screen string }{
		{"blank", "\n"},
		{"booting", "Loading agent UI...\n"},
		{"working", "✻ Thinking… (esc to interrupt)\n❯ \n"},
		{"draft", "❯ do not erase my draft\n"},
		{"queued", "❯ \nPress up to edit queued messages\n"},
		{"trust", "Do you trust the contents of this project?\n❯ Yes, I trust this folder\n  No, exit\nEnter to confirm · Esc to cancel\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log, _ := restoreLifecycleFixture(t)
			cp := lifecycleCheckpoint(t, 1)
			storage := saveLifecycleScrollback(t, cp, []string{"must never reach the blocked UI"})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var timer *time.Timer
			defer func() {
				if timer != nil {
					timer.Stop()
				}
			}()
			ctx = WithRestoreProgress(ctx, func(_ context.Context, event RestoreProgress) error {
				if event.Stage == "inject_context" && event.Phase == "before" {
					// A ready pane can become blocked during a slow journal write.
					if err := os.WriteFile(os.Getenv("NTM_RESTORE_TEST_SCREEN"), []byte(tc.screen), 0600); err != nil {
						return err
					}
					timer = time.AfterFunc(750*time.Millisecond, cancel)
				}
				return nil
			})
			out, err := NewRestorerWithStorage(storage).RestoreFromCheckpointContext(ctx, cp, RestoreOptions{InjectContext: true, SkipGitCheck: true})
			if !errors.Is(err, ErrRestoreContextNotReady) || !errors.Is(err, context.Canceled) || out == nil || out.ContextInjected || !out.Interrupted {
				t.Fatalf("blocked UI accepted or cancellation lost: %+v %v", out, err)
			}
			calls, readErr := os.ReadFile(log)
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, forbidden := range []string{"load-buffer", "paste-buffer", "send-keys", "kill-session"} {
				if strings.Contains(string(calls), forbidden) {
					t.Fatalf("blocked restore issued %s: %s", forbidden, calls)
				}
			}
			if strings.Count(string(calls), "respawn-pane") != 1 || strings.Count(string(calls), "capture-pane") < 2 {
				t.Fatalf("readiness bypassed or agent relaunched: %s", calls)
			}
		})
	}
}

func TestRestoreReadinessLifecycleCaptureFailureDoesNotSend(t *testing.T) {
	log, _ := restoreLifecycleFixture(t)
	t.Setenv("NTM_RESTORE_TEST_FAIL", "capture-pane")
	cp := lifecycleCheckpoint(t, 1)
	storage := saveLifecycleScrollback(t, cp, []string{"private checkpoint context"})
	out, err := NewRestorerWithStorage(storage).RestoreFromCheckpointContext(context.Background(), cp, RestoreOptions{InjectContext: true, SkipGitCheck: true})
	if !errors.Is(err, ErrRestoreContextNotReady) || out == nil || out.ContextInjected || out.Stage != "injecting_context" {
		t.Fatalf("capture failure treated as readiness: %+v %v", out, err)
	}
	calls, readErr := os.ReadFile(log)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(calls), "load-buffer") || strings.Contains(string(calls), "send-keys") {
		t.Fatalf("capture failure actuated UI: %s", calls)
	}
}
