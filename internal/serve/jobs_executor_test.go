package serve

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func executorTestWork(root context.Context, id string, run, discard func()) *scheduledJob {
	ctx, cancel := context.WithCancel(root)
	return &scheduledJob{id: id, ctx: ctx, cancel: cancel, run: run, discard: discard}
}

func awaitExecutorSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("executor rendezvous timed out")
	}
}

func awaitExecutorDrain(t *testing.T, e *jobExecutor) {
	t.Helper()
	e.closeAdmission()
	e.mu.Lock()
	done := e.drained
	e.mu.Unlock()
	awaitExecutorSignal(t, done)
	if !e.idle() {
		t.Fatal("drain signalled while execution was still owned")
	}
}

func TestJobExecutorBoundsActiveAndQueuedWork(t *testing.T) {
	e := &jobExecutor{maxConcurrent: 2, maxQueued: 2}
	gate := make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(gate) }); awaitExecutorDrain(t, e) })
	started := make(chan string, 4)
	var running, peak, prepared atomic.Int32
	prepare := func(id string) func(context.Context) (*scheduledJob, error) {
		return func(root context.Context) (*scheduledJob, error) {
			prepared.Add(1)
			return executorTestWork(root, id, func() {
				n := running.Add(1)
				defer running.Add(-1)
				for old := peak.Load(); n > old; old = peak.Load() {
					if peak.CompareAndSwap(old, n) {
						break
					}
				}
				started <- id
				<-gate
			}, func() {}), nil
		}
	}
	for i := 0; i < 4; i++ {
		if err := e.submit(context.Background(), prepare(fmt.Sprint(i))); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("worker did not start")
		}
	}
	if err := e.submit(context.Background(), prepare("overflow")); !errors.Is(err, errJobQueueFull) {
		t.Fatalf("unbounded admission: %v", err)
	}
	if prepared.Load() != 4 {
		t.Fatal("overloaded request reached preparation")
	}
	if got := e.snapshot(); got.Running != 2 || got.Queued != 2 || got.Owned != 4 {
		t.Fatalf("wrong execution snapshot: %+v", got)
	}
	select {
	case id := <-started:
		t.Fatalf("queued job %s started above concurrency bound", id)
	default:
	}
	release.Do(func() { close(gate) })
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("queued work did not execute")
		}
	}
	awaitExecutorDrain(t, e)
	if peak.Load() > 2 {
		t.Fatalf("peak executions = %d", peak.Load())
	}
}

func TestJobExecutorWaitingJobsRunFIFO(t *testing.T) {
	e := &jobExecutor{maxConcurrent: 1, maxQueued: 4}
	gate := make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(gate) }); awaitExecutorDrain(t, e) })
	order := make(chan string, 5)
	for _, id := range []string{"first", "second", "third", "fourth", "last"} {
		if err := e.submit(context.Background(), func(root context.Context) (*scheduledJob, error) {
			return executorTestWork(root, id, func() { <-gate; order <- id }, func() {}), nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	release.Do(func() { close(gate) })
	var got []string
	for i := 0; i < 5; i++ {
		select {
		case id := <-order:
			got = append(got, id)
		case <-time.After(3 * time.Second):
			t.Fatal("FIFO queue stalled")
		}
	}
	if !reflect.DeepEqual(got, []string{"first", "second", "third", "fourth", "last"}) {
		t.Fatalf("FIFO violated: %v", got)
	}
}

func TestJobExecutorReceiptPreparationPrecedesExecution(t *testing.T) {
	e := &jobExecutor{maxConcurrent: 1, maxQueued: 1}
	preparing, allowReceipt, ran := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(allowReceipt) }); awaitExecutorDrain(t, e) })
	done := make(chan error, 1)
	go func() {
		done <- e.submit(context.Background(), func(root context.Context) (*scheduledJob, error) {
			close(preparing)
			<-allowReceipt
			return executorTestWork(root, "receipt", func() { close(ran) }, func() {}), nil
		})
	}()
	awaitExecutorSignal(t, preparing)
	if got := e.snapshot(); got.Preparing != 1 || got.Owned != 1 || got.Running != 0 {
		t.Fatalf("preparing receipt not accounted for: %+v", got)
	}
	select {
	case <-ran:
		t.Fatal("operation preceded receipt")
	default:
	}
	select {
	case err := <-done:
		t.Fatalf("accepted before durable receipt: %v", err)
	default:
	}
	release.Do(func() { close(allowReceipt) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	awaitExecutorSignal(t, ran)
}

func TestJobExecutorShutdownDuringPreparationKeepsOwnership(t *testing.T) {
	e := &jobExecutor{maxConcurrent: 1, maxQueued: 1}
	preparing, allowReceipt, discarded := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(allowReceipt) }); awaitExecutorDrain(t, e) })
	var acts atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- e.submit(context.Background(), func(root context.Context) (*scheduledJob, error) {
			close(preparing)
			<-allowReceipt
			return executorTestWork(root, "slow-receipt", func() { acts.Add(1) }, func() { close(discarded) }), nil
		})
	}()
	awaitExecutorSignal(t, preparing)
	closed := make(chan struct{})
	go func() { e.closeAdmission(); close(closed) }()
	awaitExecutorSignal(t, closed) // Receipt I/O must not hold the executor mutex.
	if e.idle() {
		t.Fatal("shutdown lost a preparing request")
	}
	select {
	case <-e.drained:
		t.Fatal("journal fence could be released during receipt write")
	default:
	}
	if err := e.submit(context.Background(), func(context.Context) (*scheduledJob, error) {
		t.Fatal("shutdown request reached preparation")
		return nil, nil
	}); !errors.Is(err, errJobAdmissionClosed) {
		t.Fatalf("closed admission: %v", err)
	}
	release.Do(func() { close(allowReceipt) })
	if err := <-done; !errors.Is(err, errJobAdmissionClosed) {
		t.Fatalf("late admission = %v", err)
	}
	awaitExecutorSignal(t, discarded)
	awaitExecutorDrain(t, e)
	if acts.Load() != 0 {
		t.Fatal("late receipt executed after shutdown")
	}
}

func TestJobExecutorCapacityIncludesPreparingRequests(t *testing.T) {
	e := &jobExecutor{maxConcurrent: 1, maxQueued: 1}
	entered, gate := make(chan struct{}, 2), make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(gate) }); awaitExecutorDrain(t, e) })
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			done <- e.submit(context.Background(), func(context.Context) (*scheduledJob, error) {
				entered <- struct{}{}
				<-gate
				return nil, errors.New("receipt failed")
			})
		}()
	}
	for i := 0; i < 2; i++ {
		awaitExecutorSignal(t, entered)
	}
	if err := e.submit(context.Background(), func(context.Context) (*scheduledJob, error) {
		t.Fatal("overload reached receipt I/O")
		return nil, nil
	}); !errors.Is(err, errJobQueueFull) {
		t.Fatalf("preparation bypassed bound: %v", err)
	}
	release.Do(func() { close(gate) })
	for i := 0; i < 2; i++ {
		if err := <-done; err == nil {
			t.Fatal("failed receipt accepted")
		}
	}
	if !e.idle() {
		t.Fatal("failed preparation leaked capacity")
	}
}

func TestJobExecutorQueuedCancellationReclaimsCapacityAfterCheckpoint(t *testing.T) {
	e := &jobExecutor{maxConcurrent: 1, maxQueued: 1}
	gate, checkpoint, allowCheckpoint := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var release, releaseCheckpoint sync.Once
	t.Cleanup(func() {
		release.Do(func() { close(gate) })
		releaseCheckpoint.Do(func() { close(allowCheckpoint) })
		awaitExecutorDrain(t, e)
	})
	var acts atomic.Int32
	if err := e.submit(context.Background(), func(root context.Context) (*scheduledJob, error) {
		return executorTestWork(root, "running", func() { <-gate }, func() {}), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.submit(context.Background(), func(root context.Context) (*scheduledJob, error) {
		return executorTestWork(root, "cancel-me", func() { acts.Add(1) }, func() { close(checkpoint); <-allowCheckpoint }), nil
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() { done <- e.cancelQueued("cancel-me") }()
	awaitExecutorSignal(t, checkpoint)
	if got := e.snapshot(); got.Queued != 0 || got.Owned != 2 {
		t.Fatalf("lost finalizing cancellation: %+v", got)
	}
	if err := e.submit(context.Background(), func(context.Context) (*scheduledJob, error) {
		t.Fatal("uncheckpointed cancellation freed ownership")
		return nil, nil
	}); !errors.Is(err, errJobQueueFull) {
		t.Fatalf("premature capacity release: %v", err)
	}
	releaseCheckpoint.Do(func() { close(allowCheckpoint) })
	if !<-done {
		t.Fatal("queued cancellation not found")
	}
	if err := e.submit(context.Background(), func(root context.Context) (*scheduledJob, error) {
		return executorTestWork(root, "replacement", func() {}, func() {}), nil
	}); err != nil {
		t.Fatalf("cancellation did not return capacity: %v", err)
	}
	if acts.Load() != 0 {
		t.Fatal("queued cancellation entered operation")
	}
	if e.cancelQueued("cancel-me") {
		t.Fatal("cancelled receipt finalized twice")
	}
}

func TestJobExecutorRequestCancellationDuringPreparation(t *testing.T) {
	e := &jobExecutor{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var ran, discarded atomic.Int32
	err := e.submit(ctx, func(root context.Context) (*scheduledJob, error) {
		cancel()
		return executorTestWork(root, "unaccepted", func() { ran.Add(1) }, func() { discarded.Add(1) }), nil
	})
	if !errors.Is(err, context.Canceled) || ran.Load() != 0 || discarded.Load() != 1 || !e.idle() {
		t.Fatalf("abandoned admission = %v run=%d discard=%d snapshot=%+v", err, ran.Load(), discarded.Load(), e.snapshot())
	}
	awaitExecutorDrain(t, e)
}

func TestJobExecutorShutdownCancelsRunningAndDiscardsQueue(t *testing.T) {
	e := &jobExecutor{maxConcurrent: 1, maxQueued: 2}
	started, cancelled, releaseWorker := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(releaseWorker) }); awaitExecutorDrain(t, e) })
	var discarded, queuedActs atomic.Int32
	if err := e.submit(context.Background(), func(root context.Context) (*scheduledJob, error) {
		return executorTestWork(root, "running", func() {
			close(started)
			<-root.Done()
			close(cancelled)
			<-releaseWorker
		}, func() {}), nil
	}); err != nil {
		t.Fatal(err)
	}
	awaitExecutorSignal(t, started)
	for i := 0; i < 2; i++ {
		if err := e.submit(context.Background(), func(root context.Context) (*scheduledJob, error) {
			return executorTestWork(root, fmt.Sprint(i), func() { queuedActs.Add(1) }, func() { discarded.Add(1) }), nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	e.closeAdmission()
	awaitExecutorSignal(t, cancelled)
	if e.idle() {
		t.Fatal("shutdown reported success before final checkpoint")
	}
	release.Do(func() { close(releaseWorker) })
	awaitExecutorDrain(t, e)
	if discarded.Load() != 2 || queuedActs.Load() != 0 {
		t.Fatalf("queued work ran during shutdown: %d %d", discarded.Load(), queuedActs.Load())
	}
}

func TestJobExecutorPreparationPanicDoesNotLeakOwnership(t *testing.T) {
	e := &jobExecutor{}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("preparation panic lost")
			}
		}()
		_ = e.submit(context.Background(), func(context.Context) (*scheduledJob, error) { panic("receipt panic") })
	}()
	if !e.idle() {
		t.Fatal("preparation panic leaked reservation")
	}
	awaitExecutorDrain(t, e)
}

func TestJobExecutorCallbackPanicDoesNotStrandQueue(t *testing.T) {
	e := &jobExecutor{maxConcurrent: 1, maxQueued: 1}
	gate, next := make(chan struct{}), make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(gate) }); awaitExecutorDrain(t, e) })
	if err := e.submit(context.Background(), func(root context.Context) (*scheduledJob, error) {
		return executorTestWork(root, "panic", func() { <-gate; panic("unexpected callback") }, func() {}), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.submit(context.Background(), func(root context.Context) (*scheduledJob, error) {
		return executorTestWork(root, "next", func() { close(next) }, func() {}), nil
	}); err != nil {
		t.Fatal(err)
	}
	release.Do(func() { close(gate) })
	awaitExecutorSignal(t, next)
}

func TestJobExecutorConcurrentCancellationAndShutdownExactlyOnce(t *testing.T) {
	for round := 0; round < 100; round++ {
		e := &jobExecutor{maxConcurrent: 2, maxQueued: 8}
		var finals [10]atomic.Int32
		for i := range finals {
			index := i
			if err := e.submit(context.Background(), func(root context.Context) (*scheduledJob, error) {
				return executorTestWork(root, fmt.Sprint(index), func() { <-root.Done(); finals[index].Add(1) }, func() { finals[index].Add(1) }), nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		var wg sync.WaitGroup
		for i := range finals {
			wg.Add(1)
			go func(id string) { defer wg.Done(); e.cancelQueued(id) }(fmt.Sprint(i))
		}
		e.closeAdmission()
		wg.Wait()
		awaitExecutorDrain(t, e)
		for i := range finals {
			if finals[i].Load() != 1 {
				t.Fatalf("round %d job %d finalized %d times", round, i, finals[i].Load())
			}
		}
	}
}

func TestJobExecutorLimitsAndPrecancelledAdmission(t *testing.T) {
	for _, limits := range [][2]int{{-1, 1}, {1, -1}, {maxJobConcurrency + 1, 1}, {1, maxJobQueueCapacity + 1}} {
		if err := validateJobLimits(limits[0], limits[1]); err == nil {
			t.Fatalf("invalid bounds accepted: %v", limits)
		}
	}
	for _, limits := range [][2]int{{0, 0}, {1, 1}, {maxJobConcurrency, maxJobQueueCapacity}} {
		if err := validateJobLimits(limits[0], limits[1]); err != nil {
			t.Fatal(err)
		}
	}
	e := &jobExecutor{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.submit(ctx, func(context.Context) (*scheduledJob, error) { t.Fatal("cancelled admission prepared"); return nil, nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled admission = %v", err)
	}
	if got := e.snapshot(); got.MaxConcurrent != DefaultJobConcurrency || got.QueueCapacity != DefaultJobQueueCapacity || got.Owned != 0 {
		t.Fatalf("defaults = %+v", got)
	}
	awaitExecutorDrain(t, e)
}

func TestJobExecutorShutdownPublishesCancellationBeforeQueueSelection(t *testing.T) {
	e := &jobExecutor{maxConcurrent: 1, maxQueued: 1}
	finishFirst := make(chan struct{})
	firstDone := make(chan struct{})
	cancelEntered := make(chan struct{})
	allowCancel := make(chan struct{})
	closeDone := make(chan struct{})
	ran := make(chan struct{}, 1)
	var discarded atomic.Int32
	var finishOnce, cancelOnce sync.Once
	var originalCancel context.CancelFunc
	t.Cleanup(func() {
		finishOnce.Do(func() { close(finishFirst) })
		cancelOnce.Do(func() { close(allowCancel) })
		if originalCancel != nil {
			e.mu.Lock()
			e.cancelRoot = originalCancel
			e.mu.Unlock()
		}
		awaitExecutorDrain(t, e)
	})
	if err := e.submit(context.Background(), func(root context.Context) (*scheduledJob, error) {
		return executorTestWork(root, "first", func() {
			<-finishFirst
			close(firstDone)
		}, func() {}), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.submit(context.Background(), func(root context.Context) (*scheduledJob, error) {
		return executorTestWork(root, "queued", func() { ran <- struct{}{} }, func() { discarded.Add(1) }), nil
	}); err != nil {
		t.Fatal(err)
	}
	// Hold the root-cancellation boundary long enough to expose a queue pick
	// between closing admission and making queued contexts observe shutdown.
	e.mu.Lock()
	cancelRoot := e.cancelRoot
	originalCancel = cancelRoot
	e.cancelRoot = func() {
		close(cancelEntered)
		<-allowCancel
		cancelRoot()
	}
	e.mu.Unlock()
	go func() { e.closeAdmission(); close(closeDone) }()
	awaitExecutorSignal(t, cancelEntered)
	finishOnce.Do(func() { close(finishFirst) })
	awaitExecutorSignal(t, firstDone)
	select {
	case <-ran:
		t.Fatal("queued engine started after admission closed but before cancellation propagated")
	case <-time.After(20 * time.Millisecond):
	}
	cancelOnce.Do(func() { close(allowCancel) })
	awaitExecutorSignal(t, closeDone)
	// Restore the ordinary idempotent cancellation callback before cleanup.
	e.mu.Lock()
	e.cancelRoot = cancelRoot
	e.mu.Unlock()
	awaitExecutorDrain(t, e)
	if discarded.Load() != 1 {
		t.Fatalf("queued work was not discarded exactly once: %d", discarded.Load())
	}
}

