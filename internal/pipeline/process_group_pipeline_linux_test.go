package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Exercise the foreground CLI/robot entry rather than only the OS helper:
// external cancellation must keep exclusive ownership through descendant
// cleanup, then allow another owner without any old command still executing.
func TestControlledPipelineCancellationOwnsCommandDescendants(t *testing.T) {
	root := t.TempDir()
	groupTestFile(t, root, "child.sh", `trap '' TERM
printf ready > child.ready
while [ ! -f release ]; do sleep 0.01; done
printf orphan > late-effect
`)
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "owned-descendants", Steps: []Step{
		{ID: "command", Command: "trap 'exit 0' TERM; /bin/sh child.sh >/dev/null 2>&1 & wait"},
	}}
	cfg := DefaultExecutorConfig("owned-descendants")
	cfg.ProjectDir, cfg.RunID = root, "run-owned-descendants"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	type outcome struct {
		state *ExecutionState
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		state, err := RunControlledPipeline(ctx, workflow, nil, cfg, nil)
		done <- outcome{state, err}
	}()
	awaitGroupTestFile(t, filepath.Join(root, "child.ready"))
	if err := RequestRunCancellation(ctx, root, cfg.RunID); err != nil {
		t.Fatal(err)
	}
	owner, err := AcquireRunControl(ctx, root, cfg.RunID)
	if owner != nil {
		owner.Close()
		t.Error("run released ownership before TERM-ignoring child settled")
	} else if !errors.Is(err, ErrRunAlreadyOwned) {
		t.Errorf("unexpected ownership error during cleanup: %v", err)
	}
	var result outcome
	select {
	case result = <-done:
	case <-ctx.Done():
		t.Fatal("cancelled workflow failed to finish cleanup")
	}
	if result.err == nil || result.state == nil || result.state.Status != StatusCancelled {
		t.Fatalf("cancelled foreground run: %+v %v", result.state, result.err)
	}
	owner, err = AcquireRunControl(ctx, root, cfg.RunID)
	if err != nil {
		t.Fatalf("settled run did not release ownership: %v", err)
	}
	defer owner.Close()
	groupTestFile(t, root, "release", "new owner working")
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(root, "late-effect")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old descendant performed work during new ownership: %v", err)
	}
}
