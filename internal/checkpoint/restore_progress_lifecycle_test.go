package checkpoint

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// Exercise the production Restorer, with the same tmux subprocess fixture as
// the existing lifecycle tests. No separate restore engine is used here.
func TestRestoreProgressLifecycleStopsAtFailedCheckpoint(t *testing.T) {
	for _, tc := range []struct {
		stage, phase, forbidden string
		panes                   int
		force                   bool
	}{
		{"stop_session", "before", "kill-session", 0, true},
		{"stop_session", "after", "new-session", 0, true},
		{"create_session", "before", "new-session", 0, false},
		{"create_session", "after", "list-panes", 0, false},
		{"pane_identified", "observed", "split-window", 1, false},
		{"create_pane", "before", "split-window", 1, false},
		{"create_pane", "after", "respawn-pane", 2, false},
		{"launch_agent", "before", "respawn-pane", 2, false},
		{"launch_agent", "after", "load-buffer", 2, false},
	} {
		t.Run(tc.stage+"/"+tc.phase, func(t *testing.T) {
			log, _ := restoreLifecycleFixture(t)
			if tc.force {
				t.Setenv("NTM_RESTORE_TEST_EXISTS", "1")
			}
			cause := errors.New("checkpoint device failed")
			var failedEvent RestoreProgress
			ctx := WithRestoreProgress(context.Background(), func(_ context.Context, event RestoreProgress) error {
				if event.Stage == tc.stage && event.Phase == tc.phase {
					failedEvent = event
					return cause
				}
				return nil
			})
			out, err := NewRestorer().RestoreFromCheckpointContext(ctx, lifecycleCheckpoint(t, 2), RestoreOptions{Force: tc.force, SkipGitCheck: true})
			if !errors.Is(err, cause) || !errors.Is(err, ErrRestoreProgress) || out == nil || !out.Interrupted || out.PanesRestored != tc.panes {
				t.Fatalf("checkpoint failure lost/continued: %+v %v", out, err)
			}
			calls, _ := os.ReadFile(log)
			if strings.Contains(string(calls), tc.forbidden) {
				t.Fatalf("checkpoint failure reached %s: %s", tc.forbidden, calls)
			}
			if strings.Count(string(calls), "respawn-pane") > 1 || (!tc.force && strings.Contains(string(calls), "kill-session")) {
				t.Fatalf("failure relaunched or destroyed partial work: %s", calls)
			}
			if tc.stage == "create_pane" && tc.phase == "after" && (failedEvent.PaneID != "%1" || failedEvent.Outcome != "succeeded") {
				t.Fatalf("after-write failure lost completed pane: %+v", failedEvent)
			}
		})
	}
}

func TestRestoreProgressLifecycleTracksPhysicalTargetsWithoutSecrets(t *testing.T) {
	restoreLifecycleFixture(t)
	cp := lifecycleCheckpoint(t, 2)
	cp.Session.Panes[0].AgentType, cp.Session.Panes[0].Command = "user", "bash"
	cp.Session.Panes[1].Command = "env PRIVATE_SECRET=keep-private claude"
	storage := saveLifecycleScrollback(t, cp, []string{"never submit this shell transcript", "private agent transcript"})
	var events []RestoreProgress
	ctx := WithRestoreProgress(context.Background(), func(_ context.Context, event RestoreProgress) error { events = append(events, event); return nil })
	out, err := NewRestorerWithStorage(storage).RestoreFromCheckpointContext(ctx, cp, RestoreOptions{TargetSession: "recovered", SkipGitCheck: true, InjectContext: true})
	if err != nil || out == nil || out.Stage != "completed" || out.ContextPanesInjected != 1 {
		t.Fatalf("restore failed: %+v %v", out, err)
	}
	stages := map[string]int{}
	for i, event := range events {
		if event.Sequence != uint64(i+1) || event.SourceSession != cp.SessionName || event.Session != "recovered" || event.CheckpointID != cp.ID {
			t.Fatalf("progress identity drift: %+v", event)
		}
		if event.Outcome == "succeeded" {
			stages[event.Stage]++
		}
		if (event.Stage == "launch_agent" || event.Stage == "agent_started" || event.Stage == "inject_context") && event.PaneID != "%1" {
			t.Fatalf("non-agent/positional target reported: %+v", event)
		}
		if event.Stage == "create_pane" && event.Phase == "after" && (event.SourcePaneID != "%101" || event.PaneID != "%1") {
			t.Fatalf("source/destination pane binding lost: %+v", event)
		}
	}
	for _, stage := range []string{"create_session", "pane_identified", "create_pane", "launch_agent", "agent_started", "inject_context", "completed"} {
		if stages[stage] != 1 {
			t.Fatalf("stage %s not recorded exactly once: %v", stage, stages)
		}
	}
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"PRIVATE_SECRET", "keep-private", "private agent transcript", "shell transcript"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("progress leaked executable/private content: %s", encoded)
		}
	}
	if cp.SessionName == "recovered" {
		t.Fatal("restore changed the source checkpoint")
	}
}

func TestRestoreProgressLifecycleRechecksRecipientAfterJournalWrite(t *testing.T) {
	log, _ := restoreLifecycleFixture(t)
	cp := lifecycleCheckpoint(t, 1)
	storage := saveLifecycleScrollback(t, cp, []string{"must not reach a shell"})
	ctx := WithRestoreProgress(context.Background(), func(_ context.Context, event RestoreProgress) error {
		if event.Stage == "inject_context" && event.Phase == "before" {
			// Simulate a target changing while the journal flush blocks.
			t.Setenv("NTM_RESTORE_TEST_INJECT_STATE", "shell")
		}
		return nil
	})
	out, err := NewRestorerWithStorage(storage).RestoreFromCheckpointContext(ctx, cp, RestoreOptions{SkipGitCheck: true, InjectContext: true})
	if err == nil || out == nil || out.ContextPanesInjected != 0 {
		t.Fatalf("stale target accepted after checkpoint: %+v %v", out, err)
	}
	calls, _ := os.ReadFile(log)
	if strings.Contains(string(calls), "load-buffer") || strings.Contains(string(calls), "paste-buffer") {
		t.Fatalf("retagged target received context: %s", calls)
	}
}

func TestRestoreProgressLifecycleRetainsDeliveryAfterCheckpointFailure(t *testing.T) {
	log, _ := restoreLifecycleFixture(t)
	cp := lifecycleCheckpoint(t, 2)
	storage := saveLifecycleScrollback(t, cp, []string{"first context", "second context"})
	ctx := WithRestoreProgress(context.Background(), func(_ context.Context, event RestoreProgress) error {
		if event.Stage == "inject_context" && event.Phase == "after" {
			return errors.New("progress journal full")
		}
		return nil
	})
	out, err := NewRestorerWithStorage(storage).RestoreFromCheckpointContext(ctx, cp, RestoreOptions{SkipGitCheck: true, InjectContext: true})
	if !errors.Is(err, ErrRestoreProgress) || out == nil || !out.ContextInjected || out.ContextPanesInjected != 1 || !out.Interrupted {
		t.Fatalf("successful delivery lost on after-write failure: %+v %v", out, err)
	}
	calls, _ := os.ReadFile(log)
	if strings.Count(string(calls), "paste-buffer") != 1 {
		t.Fatalf("continued context delivery after journal failure: %s", calls)
	}
}

func TestRestoreProgressLifecycleDryRunDoesNotPublishExecution(t *testing.T) {
	log, _ := restoreLifecycleFixture(t)
	t.Setenv("NTM_RESTORE_TEST_EXISTS", "1")
	ctx := WithRestoreProgress(context.Background(), func(context.Context, RestoreProgress) error {
		t.Fatal("preview emitted execution evidence")
		return nil
	})
	out, err := NewRestorer().RestoreFromCheckpointContext(ctx, lifecycleCheckpoint(t, 2), RestoreOptions{DryRun: true, Force: true, InjectContext: true})
	if err != nil || out == nil || out.Stage != "validated" {
		t.Fatalf("dry run failed: %+v %v", out, err)
	}
	calls, _ := os.ReadFile(log)
	if strings.TrimSpace(string(calls)) != "has-session" {
		t.Fatalf("dry run mutated tmux: %s", calls)
	}
}
