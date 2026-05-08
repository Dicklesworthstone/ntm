package pipeline

import (
	"sync"
	"testing"
	"time"
)

// TestLockOrder_StateMuVarMuCanonicalOrder is the bd-8wo27 regression test.
//
// Before the fix, applyStartFrom acquired stateMu→varMu (canonical) while
// resolveForeachMaxRounds acquired varMu→stateMu (inverted) — a classic
// AB-BA pair. Under -race + concurrent execution the two patterns
// deadlock the goroutines inside sync.RWMutex's writer-starvation guard:
//
//	goroutine A: holds stateMu.Lock(), waiting for varMu.Lock()
//	goroutine B: holds varMu.RLock(), waiting for stateMu.RLock()
//
// After the fix both call sites use stateMu before varMu, so a
// concurrent writer + many concurrent readers run cleanly.
//
// We do not invoke applyStartFrom from multiple goroutines because its
// graph.MarkExecuted side-effect was never designed to be re-entrant
// (separate concurrency contract). Instead we exercise a synthetic
// stateMu.Lock + varMu.Lock writer that *mirrors* applyStartFrom's
// lock pattern verbatim, while resolveForeachMaxRounds runs in parallel
// across the other goroutines. That isolates the lock-ordering question
// from the graph-mutation question.
//
// Run with `go test -race -run TestLockOrder` to also catch any
// remaining unsynchronized access to e.state.
func TestLockOrder_StateMuVarMuCanonicalOrder(t *testing.T) {
	t.Parallel()

	workflow := &Workflow{
		Name: "lock-order",
		Steps: []Step{
			{
				ID: "fanout",
				Foreach: &ForeachConfig{
					Items:     `["a","b"]`,
					MaxRounds: IntOrExpr{Expr: "${vars.rounds}"},
					Steps:     []Step{{ID: "fanout-body"}},
				},
			},
		},
	}

	cfg := DefaultExecutorConfig("session")
	executor := NewExecutor(cfg)
	executor.state = &ExecutionState{
		RunID:      "lock-order-run",
		WorkflowID: workflow.Name,
		Status:     StatusRunning,
		Steps:      map[string]StepResult{},
		Variables:  map[string]interface{}{"rounds": "3"},
	}
	executor.graph = NewDependencyGraph(workflow)
	executor.defaults = workflow.Defaults
	executor.limits = workflow.Settings.Limits.EffectiveLimits()

	const iterations = 500
	const writers = 4 // mimicking applyStartFrom's stateMu→varMu pattern
	const readers = 8 // resolveForeachMaxRounds (now stateMu→varMu too)

	timeout := time.AfterFunc(15*time.Second, func() {
		t.Errorf("bd-8wo27 regression: lock-order test deadlocked; goroutines did not finish within 15s")
	})
	defer timeout.Stop()

	var wg sync.WaitGroup

	// Writers: acquire stateMu.Lock + varMu.Lock the way applyStartFrom
	// does, mutate one entry from each protected map, then release in
	// reverse. Pre-fix, this pattern interleaved with readers below
	// produces an AB-BA deadlock the race detector spots within ~ms.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			key := "writer-" + intToString(id) // shared helper in foreach_max_rounds_test.go
			for j := 0; j < iterations; j++ {
				executor.stateMu.Lock()
				executor.varMu.Lock()
				executor.state.Steps[key] = StepResult{StepID: key, Status: StatusCompleted}
				executor.state.Variables[key] = j
				executor.varMu.Unlock()
				executor.stateMu.Unlock()
			}
		}(w)
	}

	// Readers: resolveForeachMaxRounds (now stateMu→varMu after the
	// bd-8wo27 fix). Pre-fix this used varMu→stateMu and would deadlock
	// against the writer pattern above.
	parent := &workflow.Steps[0]
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				if _, err := executor.resolveForeachMaxRounds(parent); err != nil {
					t.Errorf("resolveForeachMaxRounds: %v", err)
					return
				}
			}
		}()
	}

	wg.Wait()
}

// TestLockOrder_LoopSubstituteIntExprCanonicalOrder is the bd-eslpu
// regression test. bd-8wo27 fixed the same AB-BA pattern in
// foreach_max_rounds.go::resolveForeachMaxRounds but missed
// loops.go::LoopExecutor.substituteIntExpr because the deferred-form
// `defer le.executor.varMu.RUnlock()` line between the two RLocks
// fooled regex-based scans. This test exercises the substituteIntExpr
// path concurrently with a stateMu→varMu writer pattern; pre-fix it
// deadlocks within milliseconds, post-fix it runs cleanly.
//
// Run with `go test -race -run TestLockOrder_LoopSubstituteIntExpr`.
func TestLockOrder_LoopSubstituteIntExprCanonicalOrder(t *testing.T) {
	t.Parallel()

	cfg := DefaultExecutorConfig("session")
	executor := NewExecutor(cfg)
	executor.state = &ExecutionState{
		RunID:      "lock-order-eslpu-run",
		WorkflowID: "loop-substitute",
		Status:     StatusRunning,
		Steps:      map[string]StepResult{},
		Variables:  map[string]interface{}{"limit": "5"},
	}
	// Minimal graph + defaults so substitutor + runtime vars resolve.
	executor.graph = NewDependencyGraph(&Workflow{Name: "lock-order-eslpu", Steps: []Step{{ID: "noop"}}})
	executor.defaults = nil
	executor.limits = LimitsConfig{}.EffectiveLimits()

	le := &LoopExecutor{executor: executor}

	const iterations = 500
	const writers = 4
	const readers = 8

	timeout := time.AfterFunc(15*time.Second, func() {
		t.Errorf("bd-eslpu regression: lock-order test deadlocked; goroutines did not finish within 15s")
	})
	defer timeout.Stop()

	var wg sync.WaitGroup

	// Writers mirror applyStartFrom's stateMu.Lock + varMu.Lock pattern.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			key := "w-eslpu-" + intToString(id) // helper from foreach_max_rounds_test.go
			for j := 0; j < iterations; j++ {
				executor.stateMu.Lock()
				executor.varMu.Lock()
				executor.state.Steps[key] = StepResult{StepID: key, Status: StatusCompleted}
				executor.state.Variables[key] = j
				executor.varMu.Unlock()
				executor.stateMu.Unlock()
			}
		}(w)
	}

	// Readers exercise substituteIntExpr (now stateMu→varMu after the
	// bd-eslpu fix; pre-fix it was varMu→stateMu and deadlocked).
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				if _, err := le.substituteIntExpr("${vars.limit}"); err != nil {
					t.Errorf("substituteIntExpr: %v", err)
					return
				}
			}
		}()
	}

	wg.Wait()
}

