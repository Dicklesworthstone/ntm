package workflow

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func fileRecoveryTemplate(kind TriggerType) *WorkflowTemplate {
	return &WorkflowTemplate{
		Name: "file-recovery", Coordination: CoordPingPong,
		Agents: []WorkflowAgent{{Profile: "cod", Role: "work"}},
		Flow: &FlowConfig{Initial: "work", Transitions: []Transition{
			{From: "work", To: "done", Trigger: Trigger{Type: kind, Pattern: "*.go"}},
		}},
	}
}

func fileRecoveryCoordinator(t *testing.T, template *WorkflowTemplate, registry *TriggerRegistry) *RuntimeCoordinator {
	t.Helper()
	coordinator, err := NewCoordinator(template, nil, registry)
	if err != nil {
		t.Fatal(err)
	}
	runtime := coordinator.(*PingPongCoordinator).RuntimeCoordinator
	t.Cleanup(func() { _ = runtime.Stop() })
	return runtime
}

func TestFileTriggerResumeRecoversOfflineChanges(t *testing.T) {
	for _, scenario := range []struct {
		name string
		kind TriggerType
		edit bool
	}{
		{"created while stopped", TriggerFileCreated, true},
		{"unchanged existing creation", TriggerFileCreated, false},
		{"same size and timestamp rewrite", TriggerFileModified, true},
		{"unchanged existing modification", TriggerFileModified, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "existing.go")
			if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			template := fileRecoveryTemplate(scenario.kind)
			ctx := &TriggerContext{ProjectRoot: root}
			original := fileRecoveryCoordinator(t, template, nil)
			if err := original.Start(ctx); err != nil {
				t.Fatal(err)
			}
			saved, err := original.FileTriggerStates()
			if err != nil || saved[0].Fired {
				t.Fatalf("initial evidence = %+v, %v", saved, err)
			}
			encoded, err := json.Marshal(saved)
			if err != nil {
				t.Fatal(err)
			}
			if err := original.Stop(); err != nil {
				t.Fatal(err)
			}
			if scenario.edit {
				if scenario.kind == TriggerFileCreated {
					if err := os.WriteFile(filepath.Join(root, "finished.go"), nil, 0o600); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.WriteFile(path, []byte("after!"), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
						t.Fatal(err)
					}
				}
			}
			var restored map[int]FileTriggerState
			if err := json.Unmarshal(encoded, &restored); err != nil {
				t.Fatal(err)
			}
			ctx.FileTriggerStates = restored
			resumed := fileRecoveryCoordinator(t, template, nil)
			if err := resumed.StartAt(ctx, "work", time.Now().Add(-time.Minute), nil); err != nil {
				t.Fatal(err)
			}
			fresh, err := resumed.FileTriggerStates()
			if err != nil || fresh[0].Fired != scenario.edit {
				t.Fatalf("resumed evidence = %+v, %v, want fired=%v", fresh, err, scenario.edit)
			}
			if len(fresh[0].Baseline) != 1 || fresh[0].Baseline["existing.go"].SHA256 != saved[0].Baseline["existing.go"].SHA256 {
				t.Fatal("resume replaced the original baseline with the current tree")
			}
			ctx.FileTriggerStates = fresh
			if advanced, err := resumed.Evaluate(ctx); err != nil || advanced != scenario.edit {
				t.Fatalf("resume advance = %v, %v, want %v", advanced, err, scenario.edit)
			}
		})
	}
}

func TestFileTriggerSnapshotsDetachAndGateEvaluation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "existing.go"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := &TriggerContext{ProjectRoot: root, FileTriggerExcludes: []string{"control"}}
	coordinator := fileRecoveryCoordinator(t, fileRecoveryTemplate(TriggerFileCreated), nil)
	if err := coordinator.Start(ctx); err != nil {
		t.Fatal(err)
	}
	copy, err := coordinator.FileTriggerStates()
	if err != nil {
		t.Fatal(err)
	}
	delete(copy[0].Baseline, "existing.go")
	copy[0].Excludes[0] = "different"
	saved, err := coordinator.FileTriggerStates()
	if err != nil || len(saved[0].Baseline) != 1 || saved[0].Excludes[0] != "control" {
		t.Fatalf("caller mutated runtime evidence: %+v, %v", saved, err)
	}
	if err := os.WriteFile(filepath.Join(root, "finished.go"), []byte("new work"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx.FileTriggerStates = saved // This is the snapshot already made durable.
	if advanced, err := coordinator.Evaluate(ctx); err != nil || advanced {
		t.Fatalf("unsaved event advanced workflow: %v, %v", advanced, err)
	}
	fresh, err := coordinator.FileTriggerStates()
	if err != nil || !fresh[0].Fired {
		t.Fatalf("new evidence was not retained: %+v, %v", fresh, err)
	}
	ctx.FileTriggerStates = fresh
	if advanced, err := coordinator.Evaluate(ctx); err != nil || !advanced || coordinator.CurrentStage() != "done" {
		t.Fatalf("saved event did not advance: %v, %v", advanced, err)
	}
}

func TestFileTriggerStoppedSnapshotRetainsDeletedCompletion(t *testing.T) {
	root := t.TempDir()
	ctx := &TriggerContext{ProjectRoot: root}
	template := fileRecoveryTemplate(TriggerFileCreated)
	coordinator := fileRecoveryCoordinator(t, template, nil)
	if err := coordinator.Start(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := coordinator.FileTriggerStates()
	if err != nil || before[0].Fired {
		t.Fatal("initial snapshot was not pending")
	}
	path := filepath.Join(root, "temporary.go")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitForTrigger(t, coordinator.active[0].trigger, ctx)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	stopped, err := coordinator.StopWithFileTriggerStates()
	if err != nil || !stopped[0].Fired {
		t.Fatalf("shutdown erased completion after last snapshot: %+v, %v", stopped, err)
	}
	resumed := fileRecoveryCoordinator(t, template, nil)
	ctx.FileTriggerStates = stopped
	if err := resumed.StartAt(ctx, "work", time.Now().Add(-time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	if advanced, err := resumed.Evaluate(ctx); err != nil || !advanced {
		t.Fatalf("latched completion was lost after file deletion: %v, %v", advanced, err)
	}
}

func TestFileTriggerRollbackRetainsCompletion(t *testing.T) {
	root := t.TempDir()
	template := fileRecoveryTemplate(TriggerFileCreated)
	template.Flow.Transitions[0].To = "review"
	template.Flow.Transitions = append(template.Flow.Transitions, Transition{From: "review", To: "done", Trigger: Trigger{Type: TriggerTimeElapsed, Minutes: 1}})
	registry := NewTriggerRegistry()
	destinationErr := errors.New("review trigger unavailable")
	registry.Register(TriggerTimeElapsed, func(Trigger) (RuntimeTrigger, error) { return nil, destinationErr })
	coordinator := fileRecoveryCoordinator(t, template, registry)
	ctx := &TriggerContext{ProjectRoot: root}
	if err := coordinator.Start(ctx); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "completed.go")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitForTrigger(t, coordinator.active[0].trigger, ctx)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if advanced, err := coordinator.Evaluate(ctx); advanced || !errors.Is(err, destinationErr) {
		t.Fatalf("failed destination unexpectedly advanced: %v, %v", advanced, err)
	}
	saved, err := coordinator.FileTriggerStates()
	if err != nil || !saved[0].Fired || coordinator.CurrentStage() != "work" {
		t.Fatalf("rollback erased source evidence: %+v, %v", saved, err)
	}
	registry.Register(TriggerTimeElapsed, newTimeElapsedTrigger)
	ctx.FileTriggerStates = saved
	if advanced, err := coordinator.Evaluate(ctx); err != nil || !advanced || coordinator.CurrentStage() != "review" {
		t.Fatalf("recovered destination could not consume saved completion: %v, %v", advanced, err)
	}
}

func TestFileTriggerRetryStartsNewBaseline(t *testing.T) {
	root := t.TempDir()
	ctx := &TriggerContext{ProjectRoot: root}
	coordinator := fileRecoveryCoordinator(t, fileRecoveryTemplate(TriggerFileCreated), nil)
	if err := coordinator.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "old-attempt.go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	prior, err := coordinator.FileTriggerStates()
	if err != nil || !prior[0].Fired {
		t.Fatalf("initial attempt not observed: %+v, %v", prior, err)
	}
	ctx.FileTriggerStates = prior
	if err := coordinator.RestartStage(ctx, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	fresh, err := coordinator.FileTriggerStates()
	if err != nil || fresh[0].Fired || len(fresh[0].Baseline) != 1 {
		t.Fatalf("retry inherited old completion: %+v, %v", fresh, err)
	}
	if err := os.WriteFile(filepath.Join(root, "new-attempt.go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fresh, err = coordinator.FileTriggerStates()
	if err != nil || !fresh[0].Fired {
		t.Fatalf("retry did not observe new completion: %+v, %v", fresh, err)
	}
}

func TestFileTriggerCheckpointRejectsMismatchedEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "existing.go"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	template := fileRecoveryTemplate(TriggerFileCreated)
	coordinator := fileRecoveryCoordinator(t, template, nil)
	ctx := &TriggerContext{ProjectRoot: root, FileTriggerExcludes: []string{"control"}}
	if err := coordinator.Start(ctx); err != nil {
		t.Fatal(err)
	}
	saved, err := coordinator.FileTriggerStates()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(map[int]FileTriggerState)
	}{
		{"missing", func(s map[int]FileTriggerState) { delete(s, 0) }},
		{"extra", func(s map[int]FileTriggerState) { s[1] = s[0] }},
		{"root", func(s map[int]FileTriggerState) { v := s[0]; v.ProjectRoot = t.TempDir(); s[0] = v }},
		{"stage", func(s map[int]FileTriggerState) { v := s[0]; v.Stage = "old-stage"; s[0] = v }},
		{"index", func(s map[int]FileTriggerState) { v := s[0]; v.TransitionIndex = 9; s[0] = v }},
		{"kind", func(s map[int]FileTriggerState) { v := s[0]; v.Kind = TriggerFileModified; s[0] = v }},
		{"pattern", func(s map[int]FileTriggerState) { v := s[0]; v.Pattern = "*.txt"; s[0] = v }},
		{"version", func(s map[int]FileTriggerState) { v := s[0]; v.Version++; s[0] = v }},
		{"exclusion", func(s map[int]FileTriggerState) { s[0].Excludes[0] = "other" }},
		{"missing baseline", func(s map[int]FileTriggerState) { v := s[0]; v.Baseline = nil; s[0] = v }},
		{"traversal", func(s map[int]FileTriggerState) { s[0].Baseline["../outside.go"] = s[0].Baseline["existing.go"] }},
		{"excluded path", func(s map[int]FileTriggerState) { s[0].Baseline["control/state.go"] = s[0].Baseline["existing.go"] }},
		{"nonmatching path", func(s map[int]FileTriggerState) { s[0].Baseline["unrelated.txt"] = s[0].Baseline["existing.go"] }},
		{"negative size", func(s map[int]FileTriggerState) {
			v := s[0].Baseline["existing.go"]
			v.Size = -1
			s[0].Baseline["existing.go"] = v
		}},
		{"invalid digest", func(s map[int]FileTriggerState) {
			v := s[0].Baseline["existing.go"]
			v.SHA256 = "not-a-digest"
			s[0].Baseline["existing.go"] = v
		}},
		{"missing time", func(s map[int]FileTriggerState) {
			v := s[0].Baseline["existing.go"]
			v.ModTime = time.Time{}
			s[0].Baseline["existing.go"] = v
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			bad := cloneFileTriggerStates(saved)
			test.mutate(bad)
			if err := ValidateFileTriggerStates(template, root, "work", bad, ctx.FileTriggerExcludes); err == nil {
				t.Fatal("invalid checkpoint accepted")
			}
		})
	}
}

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

func TestReconcileDeliveryRejectsStaleOrUnsafeRequests(t *testing.T) {
	store := &StateStore{Dir: t.TempDir()}
	state := &WorkflowState{
		SessionName: "s", WorkflowName: "flow", CurrentStage: "review", ResumeVersion: 1,
		Agents: map[string]string{"%1": "review"}, Turn: 7,
		Dispatches: []StageDispatch{{Pane: "%1", Role: "review", Turn: 7, Status: "sending"}},
	}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	path, _ := store.path("s")
	before, _ := os.ReadFile(path)
	token, err := state.CheckpointToken()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		pane, token string
		outcome     string
	}{
		{"%1", "old-checkpoint", "delivered"},
		{"%1", "", "not-sent"},
		{"%9", token, "delivered"},
		{"%1", token, "guess"},
		{"", token, "delivered"},
	} {
		if _, err := store.ReconcileDelivery(context.Background(), "s", tc.pane, tc.token, tc.outcome); err == nil {
			t.Fatalf("accepted stale/unsafe recovery: %+v", tc)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.ReconcileDelivery(ctx, "s", "%1", token, "delivered"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled recovery = %v", err)
	}
	unlock, err := store.Acquire(context.Background(), "s")
	if err != nil {
		t.Fatal(err)
	}
	blocked, release := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer release()
	_, err = store.ReconcileDelivery(blocked, "s", "%1", token, "not-sent")
	unlock()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("recovery ignored active runner: %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("rejected recovery mutated state")
	}
	if _, err := store.ReconcileDelivery(context.Background(), "s", "%1", token, "delivered"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcileDelivery(context.Background(), "s", "%1", token, "not-sent"); err == nil {
		t.Fatal("conflicting resolution made a confirmed prompt replayable")
	}
}

func TestRecoveryTokenRejectsReusedStageAndTurnWithChangedTask(t *testing.T) {
	store := &StateStore{Dir: t.TempDir()}
	state := &WorkflowState{
		SessionName: "s", WorkflowName: "flow", CurrentStage: "review", ResumeVersion: 1,
		Agents: map[string]string{"%1": "review"}, Turn: 1,
		Variables:  map[string]string{"task": "original"},
		Dispatches: []StageDispatch{{Pane: "%1", Role: "review", Turn: 1, Status: "sending"}},
	}
	oldToken, err := state.CheckpointToken()
	if err != nil {
		t.Fatal(err)
	}
	state.Variables["task"] = "replacement"
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcileDelivery(context.Background(), "s", "%1", oldToken, "delivered"); err == nil {
		t.Fatal("an old recovery command changed a replacement task with the same pane/stage/turn")
	}
	loaded, err := store.Load("s")
	if err != nil || loaded.Dispatches[0].Status != "sending" || loaded.Dispatches[0].Resolution != "" {
		t.Fatalf("stale recovery changed state: %+v %v", loaded, err)
	}
}
