package serve

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func resourceWait(t *testing.T, ch <-chan string, want string) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("started %q, want %q", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("waiting for %q timed out", want)
	}
}

func resourceExecutor(t *testing.T, concurrency, capacity int) *jobExecutor {
	t.Helper()
	e := &jobExecutor{maxConcurrent: concurrency, maxQueued: capacity}
	t.Cleanup(func() {
		e.closeAdmission()
		select {
		case <-e.drained:
		case <-time.After(3 * time.Second):
			t.Error("resource executor did not drain")
		}
	})
	return e
}

func resourceGate(t *testing.T) (chan struct{}, func()) {
	t.Helper()
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	return gate, release
}

func resourceSubmit(t *testing.T, e *jobExecutor, id string, resources []string, events chan<- string, gate <-chan struct{}) *scheduledJob {
	t.Helper()
	var work *scheduledJob
	if err := e.submit(context.Background(), func(root context.Context) (*scheduledJob, error) {
		ctx, cancel := context.WithCancel(root)
		work = &scheduledJob{
			id: id, ctx: ctx, cancel: cancel, resources: resources,
			run: func() {
				events <- id
				select {
				case <-gate:
				case <-ctx.Done():
				}
			},
			discard: func() { events <- "discard:" + id },
		}
		return work, nil
	}); err != nil {
		t.Fatal(err)
	}
	return work
}

func TestJobResourcesIndependentSessionsBypassWaiters(t *testing.T) {
	e := resourceExecutor(t, 3, 5)
	gate, release := resourceGate(t)
	events := make(chan string, 8)
	resourceSubmit(t, e, "first-A", []string{"session:A"}, events, gate)
	resourceWait(t, events, "first-A")
	resourceSubmit(t, e, "second-A", []string{"session:A"}, events, gate)
	resourceSubmit(t, e, "first-B", []string{"session:B"}, events, gate)
	resourceWait(t, events, "first-B")
	if got := e.snapshot(); got.Running != 2 || got.Queued != 1 || got.WaitingForResources != 1 {
		t.Fatalf("resource-blocked queue not exposed: %+v", got)
	}
	release()
	resourceWait(t, events, "second-A")
}

func TestJobResourcesMultiScopeWaitersCannotBeStarved(t *testing.T) {
	e := resourceExecutor(t, 3, 8)
	aGate, releaseA := resourceGate(t)
	bothGate, releaseBoth := resourceGate(t)
	otherGate, _ := resourceGate(t)
	events := make(chan string, 8)
	resourceSubmit(t, e, "A", []string{"A"}, events, aGate)
	resourceWait(t, events, "A")
	resourceSubmit(t, e, "A+B", []string{"A", "B"}, events, bothGate)
	resourceSubmit(t, e, "B", []string{"B"}, events, otherGate)
	resourceSubmit(t, e, "C", []string{"C"}, events, otherGate)
	resourceWait(t, events, "C")
	if got := e.snapshot(); got.Running != 2 || got.Queued != 2 || got.WaitingForResources != 2 {
		t.Fatalf("younger job bypassed a conflicting waiter: %+v", got)
	}
	releaseA()
	resourceWait(t, events, "A+B")
	if got := e.snapshot(); got.Queued != 1 || got.WaitingForResources != 1 {
		t.Fatalf("partial resource acquisition allowed overlap: %+v", got)
	}
	releaseBoth()
	resourceWait(t, events, "B")
}

func TestJobResourcesCancellingWaiterUnblocksOtherScopes(t *testing.T) {
	e := resourceExecutor(t, 3, 8)
	gate, _ := resourceGate(t)
	events := make(chan string, 8)
	resourceSubmit(t, e, "A", []string{"A"}, events, gate)
	resourceWait(t, events, "A")
	resourceSubmit(t, e, "A+B", []string{"A", "B"}, events, gate)
	resourceSubmit(t, e, "B", []string{"B"}, events, gate)
	if !e.cancelQueued("A+B") {
		t.Fatal("resource waiter was not queued")
	}
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case event := <-events:
			seen[event] = true
		case <-time.After(3 * time.Second):
			t.Fatal("cancellation did not remove the queue barrier")
		}
	}
	if !seen["discard:A+B"] || !seen["B"] || len(seen) != 2 {
		t.Fatalf("cancelled waiter reached an engine: %v", seen)
	}
}

func TestJobResourcesOwnershipSurvivesCancellationAndFinalization(t *testing.T) {
	e := resourceExecutor(t, 3, 8)
	finalize, release := resourceGate(t)
	otherGate, _ := resourceGate(t)
	events := make(chan string, 8)
	var cancel context.CancelFunc
	if err := e.submit(context.Background(), func(root context.Context) (*scheduledJob, error) {
		var ctx context.Context
		ctx, cancel = context.WithCancel(root)
		return &scheduledJob{
			id: "owner", ctx: ctx, cancel: cancel, resources: []string{"A"},
			run: func() {
				events <- "owner"
				<-ctx.Done()
				events <- "checkpointing"
				<-finalize
			}, discard: func() {},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	resourceWait(t, events, "owner")
	cancel()
	resourceWait(t, events, "checkpointing")
	resourceSubmit(t, e, "next-A", []string{"A"}, events, otherGate)
	resourceSubmit(t, e, "B", []string{"B"}, events, otherGate)
	resourceWait(t, events, "B")
	if got := e.snapshot(); got.Running != 2 || got.WaitingForResources != 1 {
		t.Fatalf("cancellation released scopes before checkpoint: %+v", got)
	}
	release()
	resourceWait(t, events, "next-A")
}

func TestJobResourcesImplicitTargetIsAnExclusiveBarrier(t *testing.T) {
	for _, resources := range [][]string{nil, {"A"}} {
		t.Run(fmt.Sprint(resources), func(t *testing.T) {
			e := resourceExecutor(t, 4, 8)
			firstGate, releaseFirst := resourceGate(t)
			barrierGate, releaseBarrier := resourceGate(t)
			lastGate, _ := resourceGate(t)
			events := make(chan string, 8)
			resourceSubmit(t, e, "first", resources, events, firstGate)
			resourceWait(t, events, "first")
			resourceSubmit(t, e, "implicit", []string{"*"}, events, barrierGate)
			resourceSubmit(t, e, "last", nil, events, lastGate)
			if got := e.snapshot(); got.Running != 1 || got.WaitingForResources != 2 {
				t.Fatalf("exclusive barrier was bypassed: %+v", got)
			}
			releaseFirst()
			resourceWait(t, events, "implicit")
			if got := e.snapshot(); got.Running != 1 || got.Queued != 1 {
				t.Fatalf("implicit target overlaps other execution: %+v", got)
			}
			releaseBarrier()
			resourceWait(t, events, "last")
		})
	}
}

func TestJobResourcesAdmissionOwnsScopeSliceAndDescriptor(t *testing.T) {
	e := resourceExecutor(t, 3, 8)
	gate, release := resourceGate(t)
	events := make(chan string, 8)
	resourceSubmit(t, e, "A", []string{"A"}, events, gate)
	resourceWait(t, events, "A")
	scopes := []string{"A"}
	queued := resourceSubmit(t, e, "next-A", scopes, events, gate)
	scopes[0] = "B"
	queued.resources = []string{"C"}
	queued.run = func() { events <- "redirected" }
	resourceSubmit(t, e, "B", []string{"B"}, events, gate)
	resourceWait(t, events, "B")
	if got := e.snapshot(); got.WaitingForResources != 1 {
		t.Fatalf("caller changed admitted scopes: %+v", got)
	}
	release()
	resourceWait(t, events, "next-A")
}

func TestJobResourcesWaitersRemainAdmissionBounded(t *testing.T) {
	e := resourceExecutor(t, 4, 2)
	gate, _ := resourceGate(t)
	events := make(chan string, 10)
	for i := 0; i < 6; i++ {
		resourceSubmit(t, e, fmt.Sprint(i), []string{"A"}, events, gate)
	}
	resourceWait(t, events, "0")
	if got := e.snapshot(); got.Running != 1 || got.Queued != 5 || got.Owned != 6 || got.WaitingForResources != 5 {
		t.Fatalf("wrong resource backlog accounting: %+v", got)
	}
	prepared := false
	err := e.submit(context.Background(), func(context.Context) (*scheduledJob, error) {
		prepared = true
		return nil, nil
	})
	if !errors.Is(err, errJobQueueFull) || prepared {
		t.Fatalf("resource waits bypassed admission: err=%v prepared=%v", err, prepared)
	}
}

func TestJobResourcesShutdownDrainsBlockedWaitersWithIdleWorkers(t *testing.T) {
	e := resourceExecutor(t, 3, 8)
	gate, release := resourceGate(t)
	events := make(chan string, 12)
	if err := e.submit(context.Background(), func(root context.Context) (*scheduledJob, error) {
		ctx, cancel := context.WithCancel(root)
		return &scheduledJob{
			id: "owner", ctx: ctx, cancel: cancel, resources: []string{"A"},
			run: func() { events <- "owner"; <-gate }, discard: func() {},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	resourceWait(t, events, "owner")
	for i := 0; i < 5; i++ {
		resourceSubmit(t, e, fmt.Sprint(i), []string{"A"}, events, gate)
	}
	e.closeAdmission()
	for i := 0; i < 5; i++ {
		select {
		case event := <-events:
			if len(event) < 8 || event[:8] != "discard:" {
				t.Fatalf("shutdown executed waiting operation: %s", event)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("idle workers did not finalize resource waiters")
		}
	}
	select {
	case <-e.drained:
		t.Fatal("shutdown forgot the unwinding resource owner")
	default:
	}
	release()
}

func TestJobResourcesPanicReleasesOwnership(t *testing.T) {
	e := resourceExecutor(t, 2, 4)
	gate, release := resourceGate(t)
	events := make(chan string, 4)
	if err := e.submit(context.Background(), func(root context.Context) (*scheduledJob, error) {
		ctx, cancel := context.WithCancel(root)
		return &scheduledJob{
			id: "panic", ctx: ctx, cancel: cancel, resources: []string{"A"},
			run: func() { events <- "panic"; <-gate; panic("engine failed") }, discard: func() {},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	resourceWait(t, events, "panic")
	resourceSubmit(t, e, "next", []string{"A"}, events, gate)
	release()
	resourceWait(t, events, "next")
}

func TestJobResourcesConcurrentExecutionNeverOverlapsScopes(t *testing.T) {
	e := resourceExecutor(t, 8, 600)
	startGate, release := resourceGate(t)
	const jobs = 400
	done := make(chan struct{}, jobs)
	var mu sync.Mutex
	held := map[string]bool{}
	var overlap atomic.Bool
	for i := 0; i < jobs; i++ {
		resources := []string{fmt.Sprintf("session:%d", i%11), fmt.Sprintf("run:%d", i%17)}
		if err := e.submit(context.Background(), func(root context.Context) (*scheduledJob, error) {
			ctx, cancel := context.WithCancel(root)
			return &scheduledJob{
				id: fmt.Sprint(i), ctx: ctx, cancel: cancel, resources: resources,
				run: func() {
					<-startGate
					mu.Lock()
					for _, resource := range resources {
						if held[resource] {
							overlap.Store(true)
						}
						held[resource] = true
					}
					mu.Unlock()
					runtime.Gosched()
					mu.Lock()
					for _, resource := range resources {
						delete(held, resource)
					}
					mu.Unlock()
					done <- struct{}{}
				}, discard: func() { done <- struct{}{} },
			}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	release()
	for i := 0; i < jobs; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("multi-resource scheduling stalled")
		}
	}
	if overlap.Load() {
		t.Fatal("two engines held the same scope")
	}
}

func TestJobExecutionResourcesMatchEngineTargets(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		params     map[string]interface{}
		want       []string
	}{
		{"run", JobTypePipelineRun, map[string]interface{}{"session": "project"}, []string{"session:project"}},
		{"inline", JobTypePipelineExec, map[string]interface{}{"session": "project--dev"}, []string{"session:project--dev"}},
		{"spawn", JobTypeSwarmSpawn, map[string]interface{}{"session": "project"}, []string{"session:project"}},
		{"labelled spawn", JobTypeSwarmSpawn, map[string]interface{}{"session": "project", "label": "dev"}, []string{"session:project--dev"}},
		{"empty label", JobTypeSwarmSpawn, map[string]interface{}{"session": "project", "label": ""}, []string{"session:project"}},
		{"invalid label", JobTypeSwarmSpawn, map[string]interface{}{"session": "project", "label": 1}, []string{"*"}},
		{"restore destination", JobTypeCheckpointRestore, map[string]interface{}{"session": "archive", "target_session": "destination"}, []string{"session:destination"}},
		{"implicit restore", JobTypeCheckpointRestore, map[string]interface{}{"session": "archive"}, []string{"*"}},
		{"resume", JobTypePipelineResume, map[string]interface{}{"session": "project", "run_id": "run"}, []string{"*"}},
		{"implicit resume", JobTypePipelineResume, map[string]interface{}{"run_id": "run"}, []string{"*"}},
		{"empty request", JobTypePipelineRun, nil, []string{"*"}},
		{"non-string target", JobTypePipelineRun, map[string]interface{}{"session": 1}, []string{"*"}},
		{"unknown operation", "future-operation", map[string]interface{}{"session": "project"}, []string{"*"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := jobExecutionResources(tc.kind, tc.params); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("resources = %v, want %v", got, tc.want)
			}
		})
	}
}
