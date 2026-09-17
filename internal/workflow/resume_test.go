package workflow

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestCoordinatorStartAtRestoresElapsedTimeAndRouting(t *testing.T) {
	now := time.Now().UTC()
	template := &WorkflowTemplate{
		Name: "restore-test", Coordination: CoordPipeline,
		Agents: []WorkflowAgent{{Profile: "x", Role: "review", Count: 2}},
		Flow: &FlowConfig{
			Initial: "build", Stages: []string{"build", "review", "wait", "done"},
			Transitions: []Transition{
				{From: "build", To: "review", Trigger: Trigger{Type: TriggerManual}},
				{From: "review", To: "wait", Trigger: Trigger{Type: TriggerTimeElapsed, Minutes: 1}},
				{From: "wait", To: "done", Trigger: Trigger{Type: TriggerTimeElapsed, Minutes: 1}},
			},
		},
	}
	coord, err := NewCoordinator(template, []CoordinatorAgent{{ID: "%1", Role: "review"}, {ID: "%2", Role: "review"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer coord.Stop()
	runtime := coord.(*PipelineCoordinator)
	ctx := &TriggerContext{Now: func() time.Time { return now }}
	if err := runtime.StartAt(ctx, "review", now.Add(-2*time.Minute), map[string]int{"review": 1}); err != nil {
		t.Fatal(err)
	}
	if coord.CurrentStage() != "review" || template.Flow.Initial != "build" {
		t.Fatal("resume restarted or modified the reusable template")
	}
	selected, err := coord.GetAgentForTask(Task{ID: "review"})
	if err != nil || selected.ID != "%2" {
		t.Fatalf("round robin reset: %+v %v", selected, err)
	}
	routing := runtime.RoutingState()
	routing["review"] = 100
	if runtime.RoutingState()["review"] != 2 {
		t.Fatal("routing snapshot aliases live state")
	}
	if fired, err := runtime.Evaluate(ctx); err != nil || !fired || coord.CurrentStage() != "wait" {
		t.Fatalf("elapsed trigger forgot downtime: fired=%v err=%v stage=%s", fired, err, coord.CurrentStage())
	}
	if fired, err := runtime.Evaluate(ctx); err != nil || fired {
		t.Fatalf("next stage inherited the checkpoint clock: fired=%v err=%v", fired, err)
	}
	now = now.Add(61 * time.Second)
	if fired, err := runtime.Evaluate(ctx); err != nil || !fired || coord.CurrentStage() != "done" {
		t.Fatalf("next stage failed to use live clock: fired=%v err=%v", fired, err)
	}
}

func TestWorkflowRunLeaseAcrossProcesses(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestWorkflowRunLeaseProcessHelper$")
	cmd.Env = append(os.Environ(), "NTM_WORKFLOW_LEASE_HELPER="+root)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "locked" {
		t.Fatalf("child did not acquire lease: %s (%v)", scanner.Text(), scanner.Err())
	}
	store := &StateStore{Dir: root}
	blocked, release := context.WithTimeout(ctx, 25*time.Millisecond)
	defer release()
	unlock, err := store.Acquire(blocked, "s")
	if err == nil {
		unlock()
		t.Fatal("two processes acquired the same run")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended lease error = %v", err)
	}
	if _, err := stdin.Write([]byte("release")); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	unlock, err = store.Acquire(ctx, "s")
	if err != nil {
		t.Fatalf("lease stranded after process exit: %v", err)
	}
	unlock()
}

func TestWorkflowRunLeaseProcessHelper(t *testing.T) {
	root := os.Getenv("NTM_WORKFLOW_LEASE_HELPER")
	if root == "" {
		return
	}
	unlock, err := (&StateStore{Dir: root}).Acquire(context.Background(), "s")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	fmt.Println("locked")
	var input [8]byte
	_, _ = os.Stdin.Read(input[:])
}

type resumeTimeoutActions struct {
	WorkflowErrorActions
	entered chan struct{}
	exited  chan struct{}
}

func (a *resumeTimeoutActions) RetryStage(ctx context.Context) error {
	close(a.entered)
	<-ctx.Done()
	close(a.exited)
	return ctx.Err()
}

func TestTimeoutMonitorDrainsActionBeforeLeaseRelease(t *testing.T) {
	actions := &resumeTimeoutActions{entered: make(chan struct{}), exited: make(chan struct{})}
	handler := NewErrorHandler(ErrorHandlingConfig{OnTimeout: ErrorActionRetry, MaxRetriesPerStage: 1}, actions)
	monitor := NewTimeoutMonitor(time.Millisecond, handler, func() string { return "review" })
	monitor.Start(context.Background(), "review")
	defer monitor.StopAndWait()
	select {
	case <-actions.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout action never started")
	}
	monitor.StopAndWait()
	select {
	case <-actions.exited:
	default:
		t.Fatal("StopAndWait returned with a checkpoint writer still active")
	}
}
