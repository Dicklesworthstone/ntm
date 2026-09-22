package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
)

func TestRestoreJobProgressAccumulatesOnlyConfirmedEffects(t *testing.T) {
	var snapshots []map[string]interface{}
	ctx := context.WithValue(context.Background(), jobProgressContextKey{}, jobProgressReporter(func(snapshot map[string]interface{}) error {
		snapshots = append(snapshots, snapshot)
		return nil
	}))
	observer := restoreJobProgressObserver()
	events := []checkpoint.RestoreProgress{
		{Stage: "stop_session", Phase: "before", Outcome: "uncertain"},
		{Stage: "stop_session", Phase: "after", Outcome: "succeeded"},
		{Stage: "create_session", Phase: "after", Outcome: "succeeded"},
		{Stage: "pane_identified", Phase: "observed", Outcome: "succeeded", PaneID: "%7", SourcePaneID: "%100", AgentType: "cc", WindowIndex: 2, PaneIndex: 3},
		{Stage: "create_pane", Phase: "after", Outcome: "uncertain", PaneID: "%8"},
		{Stage: "launch_agent", Phase: "before", Outcome: "uncertain", PaneID: "%7"},
		{Stage: "launch_agent", Phase: "after", Outcome: "succeeded", PaneID: "%7"},
		{Stage: "agent_started", Phase: "observed", Outcome: "succeeded", PaneID: "%7"},
		{Stage: "inject_context", Phase: "after", Outcome: "succeeded", PaneID: "%7"},
		{Stage: "inject_context", Phase: "after", Outcome: "succeeded", PaneID: "%7"},
	}
	for i, event := range events {
		event.Session, event.SourceSession, event.CheckpointID = "target", "source", "saved"
		event.WorkingDir, event.Sequence = "/project", uint64(i+1)
		if err := observer(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	first := snapshots[0]["restore_progress"].(map[string]interface{})
	if first["session_created"] != false || first["previous_session_stopped"] != false || len(first["created_pane_ids"].([]interface{})) != 0 {
		t.Fatalf("before-event fabricated effects or earlier snapshot mutated: %+v", first)
	}
	launch := snapshots[6]["restore_progress"].(map[string]interface{})
	if len(launch["startup_verified_pane_ids"].([]interface{})) != 0 || !reflect.DeepEqual(launch["launch_accepted_pane_ids"], []interface{}{"%7"}) {
		t.Fatal("launch acceptance was confused with verified startup")
	}
	last := snapshots[len(snapshots)-1]
	if last["session_name"] != "target" || last["source_session"] != "source" || last["checkpoint_id"] != "saved" || last["working_dir"] != "/project" {
		t.Fatalf("recovery identities lost: %+v", last)
	}
	progress := last["restore_progress"].(map[string]interface{})
	for _, key := range []string{"created_pane_ids", "launch_accepted_pane_ids", "startup_verified_pane_ids", "context_injected_pane_ids"} {
		if !reflect.DeepEqual(progress[key], []interface{}{"%7"}) {
			t.Fatalf("unconfirmed/duplicate effect counted in %s: %+v", key, progress[key])
		}
	}
	binding := progress["pane_bindings"].(map[string]interface{})["%7"].(map[string]interface{})
	if binding["source_pane_id"] != "%100" || binding["window_index"] != float64(2) || binding["pane_index"] != float64(3) {
		t.Fatalf("historical/physical pane identities confused: %+v", binding)
	}
	if progress["session_created"] != true || progress["previous_session_stopped"] != true {
		t.Fatalf("session replacement evidence lost: %+v", progress)
	}
}

func TestRestoreJobProgressUsesSuppliedReporterAfterCancellation(t *testing.T) {
	observer := restoreJobProgressObserver()
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	cause := errors.New("late journal failure")
	ctx := context.WithValue(parent, jobProgressContextKey{}, jobProgressReporter(func(snapshot map[string]interface{}) error {
		calls++
		if parent.Err() == nil || snapshot["source_session"] != "original" {
			t.Fatal("wrong execution context or missing late evidence")
		}
		return cause
	}))
	cancel()
	err := observer(ctx, checkpoint.RestoreProgress{SourceSession: "original", Stage: "create_session", Phase: "after", Outcome: "succeeded"})
	if calls != 1 || !errors.Is(err, cause) || !errors.Is(err, errJobProgressCheckpoint) {
		t.Fatalf("failed/cancelled reporter was bypassed: %d %v", calls, err)
	}
}

func TestRestoreJobProgressSnapshotIsolation(t *testing.T) {
	var snapshot map[string]interface{}
	ctx := context.WithValue(context.Background(), jobProgressContextKey{}, jobProgressReporter(func(value map[string]interface{}) error { snapshot = value; return nil }))
	observer := restoreJobProgressObserver()
	if err := observer(ctx, checkpoint.RestoreProgress{Stage: "pane_identified", Phase: "observed", Outcome: "succeeded", PaneID: "%1", SourcePaneID: "%90"}); err != nil {
		t.Fatal(err)
	}
	prior := snapshot
	encoded, _ := json.Marshal(prior)
	if err := observer(ctx, checkpoint.RestoreProgress{Stage: "create_pane", Phase: "after", Outcome: "succeeded", PaneID: "%2", SourcePaneID: "%91"}); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(prior)
	if string(encoded) != string(after) {
		t.Fatal("new event rewrote already-published recovery evidence")
	}
	// Consumers also must not be able to mutate the observer's future state.
	prior["restore_progress"].(map[string]interface{})["pane_bindings"].(map[string]interface{})["%1"].(map[string]interface{})["source_pane_id"] = "tampered"
	if err := observer(ctx, checkpoint.RestoreProgress{Stage: "completed", Phase: "observed", Outcome: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	binding := snapshot["restore_progress"].(map[string]interface{})["pane_bindings"].(map[string]interface{})["%1"].(map[string]interface{})
	if binding["source_pane_id"] != "%90" {
		t.Fatal("consumer mutation poisoned future journal evidence")
	}
}

func TestRestoreJobProgressConcurrentEventsRetainAllPanes(t *testing.T) {
	var snapshot map[string]interface{}
	ctx := context.WithValue(context.Background(), jobProgressContextKey{}, jobProgressReporter(func(value map[string]interface{}) error { snapshot = value; return nil }))
	observer := restoreJobProgressObserver()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := observer(ctx, checkpoint.RestoreProgress{Stage: "create_pane", Phase: "after", Outcome: "succeeded", PaneID: fmt.Sprintf("%%%d", i)})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if len(snapshot["restore_progress"].(map[string]interface{})["created_pane_ids"].([]interface{})) != 100 {
		t.Fatal("concurrent progress lost known-created panes")
	}
}
