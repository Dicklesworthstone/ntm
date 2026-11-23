package pipeline

// Race-detector regression suite for parallel + foreach + nested topologies.
//
// Documented invocations (per bd-7ramj.16 acceptance):
//
//	go test -race -run '^TestRace_' ./internal/pipeline/...
//	go test -race -count=10 -run '^TestRace_' ./internal/pipeline/...
//
// CI is expected to run the race-targeted suite separately from the default
// `go test -short` path so the race overhead does not slow normal runs.

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newRaceExecutor(t *testing.T, workflowName string) *Executor {
	t.Helper()
	cfg := DefaultExecutorConfig("race-session")
	cfg.DryRun = true
	e := NewExecutor(cfg)
	e.state = &ExecutionState{
		RunID:      fmt.Sprintf("race-%s-%d", workflowName, time.Now().UnixNano()),
		WorkflowID: workflowName,
		Status:     StatusRunning,
		StartedAt:  time.Now(),
		Steps:      make(map[string]StepResult),
		Variables:  make(map[string]interface{}),
	}
	return e
}

// stressSnapshots runs concurrent snapshotState + substituteVariables readers
// until stop is closed. Readers are ready and blocked on start before this
// returns, so closing start immediately before dispatch guarantees pressure is
// present while the worker runs. Each pass yields to avoid monopolizing a CPU
// under the race detector.
func stressSnapshots(t *testing.T, e *Executor, readers int, start, stop <-chan struct{}, templates ...string) *sync.WaitGroup {
	t.Helper()
	var wg sync.WaitGroup
	var ready sync.WaitGroup
	if len(templates) == 0 {
		templates = []string{"${vars.absent | \"x\"}"}
	}
	ready.Add(readers)
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ready.Done()
			<-start
			for {
				select {
				case <-stop:
					return
				default:
				}
				if snap := e.snapshotState(); snap == nil {
					t.Errorf("reader %d: snapshotState returned nil", idx)
					return
				}
				_ = e.substituteVariables(templates[idx%len(templates)])
				runtime.Gosched()
			}
		}(i)
	}
	ready.Wait()
	return &wg
}

// TestRace_ParallelInline_50steps fans out 50 inline steps via executeParallel
// while concurrent readers stress varMu/stateMu.
func TestRace_ParallelInline_50steps(t *testing.T) {
	const stepCount = 50
	parallelSteps := make([]Step, stepCount)
	for i := range parallelSteps {
		parallelSteps[i] = Step{
			ID:     fmt.Sprintf("inline_%02d", i),
			Prompt: fmt.Sprintf("task %d", i),
		}
	}

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "race-parallel-inline",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{ID: "parallel_group", Parallel: ParallelSpec{Steps: parallelSteps}},
		},
	}

	e := newRaceExecutor(t, "race-parallel-inline")
	e.graph = NewDependencyGraph(workflow)

	step := &Step{
		ID:       "parallel_group",
		Parallel: ParallelSpec{Steps: parallelSteps},
	}

	start := make(chan struct{})
	stop := make(chan struct{})
	wg := stressSnapshots(t, e, 4, start, stop, "ping ${vars.absent | \"x\"}")

	close(start)
	runtime.Gosched()
	result := e.executeParallel(context.Background(), step, workflow)

	close(stop)
	wg.Wait()

	if result.Status != StatusCompleted {
		t.Fatalf("expected StatusCompleted, got %s (err=%+v)", result.Status, result.Error)
	}
	e.stateMu.RLock()
	gotSteps := len(e.state.Steps)
	e.stateMu.RUnlock()
	if gotSteps != stepCount {
		t.Fatalf("expected %d step results, got %d", stepCount, gotSteps)
	}
}

// TestRace_ForeachLoopUnderSnapshotPressure runs a 100-iteration foreach loop
// (sequential dispatch — the parallel iteration runtime in bd-2ubxp.12 is still
// open) while concurrent readers exercise the loop-scope stack via substitution.
// Detects races while local loop bindings coexist with shared-state readers.
func TestRace_ForeachLoopUnderSnapshotPressure(t *testing.T) {
	const itemCount = 100
	items := make([]interface{}, itemCount)
	for i := range items {
		items[i] = fmt.Sprintf("item-%03d", i)
	}

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "race-foreach",
		Settings:      DefaultWorkflowSettings(),
	}
	workflow.Settings.OnError = ErrorActionContinue

	e := newRaceExecutor(t, "race-foreach")
	e.graph = NewDependencyGraph(workflow)
	e.varMu.Lock()
	e.state.Variables["data"] = items
	e.varMu.Unlock()

	step := &Step{
		ID: "foreach_root",
		Loop: &LoopConfig{
			Items:         "${data}",
			As:            "row",
			MaxIterations: IntOrExpr{Value: itemCount + 1},
			Steps: []Step{
				{ID: "iter_substep", Prompt: "process ${loop.row}"},
			},
		},
	}

	start := make(chan struct{})
	stop := make(chan struct{})
	wg := stressSnapshots(t, e, 8, start, stop,
		"loop ${loop.row | \"none\"}",
		"index ${loop.index | \"-\"}",
		"data ${data | \"none\"}",
	)

	close(start)
	runtime.Gosched()
	res := e.executeLoop(context.Background(), step, workflow)
	close(stop)
	wg.Wait()

	if res.Status != StatusCompleted {
		t.Fatalf("foreach loop expected StatusCompleted, got %s (err=%+v)", res.Status, res.Error)
	}
}

// TestRace_NestedForeachParallel covers the bd-7ramj.16 spec scenario "outer
// foreach + inner parallel substeps". Each iteration dispatches a parallel
// group; outer readers spin snapshots so the test fails under -race if any
// shared map is touched without holding the right mutex.
//
// Note: nested parallel-IN-parallel groups are explicitly rejected by
// executeParallelStep at runtime today (see executor.go) so this exercises
// the supported foreach->parallel topology rather than parallel->parallel.
func TestRace_NestedForeachParallel(t *testing.T) {
	const iterations = 6
	const innerWidth = 12
	items := make([]interface{}, iterations)
	for i := range items {
		items[i] = i
	}

	innerParallel := make([]Step, innerWidth)
	for i := range innerParallel {
		innerParallel[i] = Step{
			ID:     fmt.Sprintf("inner_%d", i),
			Prompt: fmt.Sprintf("inner work ${loop.row} #%d", i),
		}
	}

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "race-loop-parallel",
		Settings:      DefaultWorkflowSettings(),
	}

	e := newRaceExecutor(t, "race-loop-parallel")
	e.graph = NewDependencyGraph(workflow)
	e.varMu.Lock()
	e.state.Variables["rows"] = items
	e.varMu.Unlock()

	step := &Step{
		ID: "loop_with_parallel",
		Loop: &LoopConfig{
			Items:         "${rows}",
			As:            "row",
			MaxIterations: IntOrExpr{Value: iterations + 1},
			Steps: []Step{
				{ID: "fanout", Parallel: ParallelSpec{Steps: innerParallel}},
			},
		},
	}

	start := make(chan struct{})
	stop := make(chan struct{})
	wg := stressSnapshots(t, e, 4, start, stop,
		"snap ${loop.row | \"-\"}",
		"snap2 ${loop.index | \"-\"}",
	)

	close(start)
	runtime.Gosched()
	res := e.executeLoop(context.Background(), step, workflow)
	close(stop)
	wg.Wait()

	if res.Status != StatusCompleted {
		t.Fatalf("loop+parallel expected StatusCompleted, got %s (err=%+v)", res.Status, res.Error)
	}
}

// TestRace_PostPipelineDuringMain is reserved for the tight overlap between
// the main step graph and post_pipeline_steps. The runtime hook for
// post_pipeline_steps lives in bd-w6nth.5 (still open) — the field is parsed
// but not yet dispatched. When that bead lands this test should be filled in
// with a workflow that finishes a parallel group exactly as the post-pipeline
// dispatch begins.
func TestRace_PostPipelineDuringMain(t *testing.T) {
	t.Skip("blocked on bd-w6nth.5: post_pipeline_steps runtime not yet wired")
}

// TestRace_VariableScopeStackUnderForeach exercises local iteration bindings
// under concurrent substitution and snapshot pressure. Every worker must see
// its own values while readers of shared state see no iteration variables.
func TestRace_VariableScopeStackUnderForeach(t *testing.T) {
	e := newRaceExecutor(t, "race-scope")
	e.varMu.Lock()
	e.state.Variables["base"] = "stable"
	e.varMu.Unlock()

	const writers = 4
	const readers = 8
	const itersPerWriter = 200

	var writerWG sync.WaitGroup
	var readerWG sync.WaitGroup
	var ready sync.WaitGroup
	start := make(chan struct{})
	stop := make(chan struct{})
	var pushes int64

	ready.Add(readers + writers)
	for i := 0; i < readers; i++ {
		readerWG.Add(1)
		go func() {
			defer readerWG.Done()
			ready.Done()
			<-start
			for {
				select {
				case <-stop:
					return
				default:
				}
				if got := e.substituteVariables("${vars.base} ${loop.row_w0 | \"-\"} ${loop.index | \"-\"}"); got != "stable - -" {
					t.Errorf("shared reader observed a local iteration: %q", got)
					return
				}
				_ = e.snapshotState()
				runtime.Gosched()
			}
		}()
	}

	for w := 0; w < writers; w++ {
		writerWG.Add(1)
		go func(id int) {
			defer writerWG.Done()
			ready.Done()
			<-start
			varName := fmt.Sprintf("row_w%d", id)
			for i := 0; i < itersPerWriter; i++ {
				value := fmt.Sprintf("v-%d-%d", id, i)
				ctx := withLoopVars(context.Background(), varName, value, i, itersPerWriter)
				atomic.AddInt64(&pushes, 1)
				runtime.Gosched()
				got, err := e.substituteVariablesStrictCtx(ctx, "${loop."+varName+"}:${item}:${loop.index}")
				want := fmt.Sprintf("%s:%s:%d", value, value, i)
				if err != nil || got != want {
					t.Errorf("worker %d iteration %d: %q, %v; want %q", id, i, got, err, want)
					return
				}
			}
		}(w)
	}

	ready.Wait()
	close(start)
	runtime.Gosched()
	writerWG.Wait()
	close(stop)
	readerWG.Wait()

	if got := atomic.LoadInt64(&pushes); got != int64(writers*itersPerWriter) {
		t.Fatalf("expected %d isolated iteration cycles, got %d", writers*itersPerWriter, got)
	}

	// Local contexts must leave the original global variables intact.
	e.varMu.RLock()
	got := e.state.Variables["base"]
	e.varMu.RUnlock()
	if got != "stable" {
		t.Fatalf("base variable corrupted by scope stack: got %v want \"stable\"", got)
	}
	if len(e.state.Variables) != 1 || len(e.state.ScopeStack) != 0 {
		t.Fatal("iteration bindings leaked into shared state")
	}
}
