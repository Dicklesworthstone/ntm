package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
)

const (
	DefaultJobConcurrency   = 4
	DefaultJobQueueCapacity = 64
	maxJobConcurrency       = 128
	maxJobQueueCapacity     = 4096
)

var (
	errJobQueueFull       = errors.New("job execution queue is full")
	errJobAdmissionClosed = errors.New("job admission is closed")
)

// scheduledJob owns an accepted operation through its final checkpoint. Both
// callbacks must release the store's cancellation registration only after that
// checkpoint. discard must never invoke an operation engine.
type scheduledJob struct {
	id        string
	ctx       context.Context
	cancel    context.CancelFunc
	run       func()
	discard   func()
	resources []string // Exclusive execution scopes; "*" conflicts with all jobs.
}

// jobExecutor bounds active operations AND waiting/preparing requests. Its zero
// value starts no goroutines. Receipt I/O runs outside mu, but preparation keeps
// a capacity reservation so shutdown cannot release the journal underneath it.
// Jobs sharing resources run FIFO in receipt-preparation order. Independent jobs
// may pass a resource-blocked job, but never an earlier waiter on the same scope.
// The total owned/preparing bound remains maxConcurrent+maxQueued; resource
// waiters can use capacity that would otherwise belong to idle workers.
type jobExecutor struct {
	mu            sync.Mutex
	maxConcurrent int
	maxQueued     int
	active        int
	preparing     int
	queue         []*scheduledJob
	owned         map[string]*scheduledJob
	running       map[string]*scheduledJob
	stopped       bool
	root          context.Context
	cancelRoot    context.CancelFunc
	drained       chan struct{}
	drainClosed   bool
}

// JobExecutionStatus is exposed by GET /api/v1/jobs. Owned includes preparation
// and callbacks still checkpointing an otherwise terminal outcome.
type JobExecutionStatus struct {
	MaxConcurrent       int  `json:"max_concurrent"`
	QueueCapacity       int  `json:"queue_capacity"`
	Running             int  `json:"running"`
	Queued              int  `json:"queued"`
	WaitingForResources int  `json:"waiting_for_resources"`
	Preparing           int  `json:"preparing"`
	Owned               int  `json:"owned"`
	Accepting           bool `json:"accepting"`
}

func validateJobLimits(concurrency, capacity int) error {
	if concurrency < 0 || concurrency > maxJobConcurrency {
		return fmt.Errorf("job concurrency must be between 1 and %d (0 selects the default)", maxJobConcurrency)
	}
	if capacity < 0 || capacity > maxJobQueueCapacity {
		return fmt.Errorf("job queue capacity must be between 1 and %d (0 selects the default)", maxJobQueueCapacity)
	}
	return nil
}

func (e *jobExecutor) initLocked() {
	if e.maxConcurrent == 0 {
		e.maxConcurrent = DefaultJobConcurrency
	}
	if e.maxQueued == 0 {
		e.maxQueued = DefaultJobQueueCapacity
	}
	if e.root == nil {
		e.root, e.cancelRoot = context.WithCancel(context.Background())
		e.drained = make(chan struct{})
		e.owned = make(map[string]*scheduledJob)
		e.running = make(map[string]*scheduledJob)
	}
}

// submit reserves capacity before preparation creates a job row or writes its
// receipt. A successful return means the receipt was prepared before execution
// became possible. The request context gates admission, not the accepted job's
// lifetime: closing the submitting HTTP connection must not cancel accepted work.
func (e *jobExecutor) submit(ctx context.Context, prepare func(context.Context) (*scheduledJob, error)) error {
	if ctx == nil || prepare == nil {
		return errors.New("job submission requires a context and preparation function")
	}
	e.mu.Lock()
	e.initLocked()
	if err := validateJobLimits(e.maxConcurrent, e.maxQueued); err != nil {
		e.mu.Unlock()
		return err
	}
	if e.stopped {
		e.mu.Unlock()
		return errJobAdmissionClosed
	}
	if err := ctx.Err(); err != nil {
		e.mu.Unlock()
		return err
	}
	if len(e.owned)+e.preparing >= e.maxConcurrent+e.maxQueued {
		e.mu.Unlock()
		return errJobQueueFull
	}
	e.preparing++
	root := e.root
	e.mu.Unlock()

	var work *scheduledJob
	var prepareErr error
	func() {
		// A preparation panic must not permanently consume capacity. The
		// preparation callback owns cleanup of any partially constructed row.
		defer func() {
			if value := recover(); value != nil {
				e.mu.Lock()
				e.preparing--
				e.notifyDrainedLocked()
				e.mu.Unlock()
				panic(value)
			}
		}()
		work, prepareErr = prepare(root)
	}()

	e.mu.Lock()
	e.preparing--
	if prepareErr != nil {
		e.notifyDrainedLocked()
		e.mu.Unlock()
		return prepareErr
	}
	if work == nil || work.id == "" || work.ctx == nil || work.cancel == nil || work.run == nil || work.discard == nil {
		e.notifyDrainedLocked()
		e.mu.Unlock()
		if work != nil && work.cancel != nil {
			work.cancel()
		}
		return errors.New("incomplete prepared job")
	}
	if _, exists := e.owned[work.id]; exists {
		e.notifyDrainedLocked()
		e.mu.Unlock()
		work.cancel()
		return fmt.Errorf("job %q already admitted", work.id)
	}
	// Own a detached descriptor as well as its resource slice. The preparation
	// caller must not be able to redirect a queued job's execution scopes.
	copy := *work
	copy.resources = append([]string(nil), work.resources...)
	sort.Strings(copy.resources)
	work = &copy
	e.owned[work.id] = work
	admissionErr := ctx.Err()
	if e.stopped {
		admissionErr = errJobAdmissionClosed
	}
	if admissionErr != nil {
		// Even rejected receipts remain owned until their cancellation is
		// checkpointed. Never hold the executor mutex across that write.
		e.mu.Unlock()
		work.cancel()
		e.invoke(work, true)
		e.mu.Lock()
		delete(e.owned, work.id)
		e.notifyDrainedLocked()
		e.mu.Unlock()
		return admissionErr
	}
	e.queue = append(e.queue, work)
	e.startReadyLocked()
	e.mu.Unlock()
	return nil
}

// jobResourceSet represents both running owners and earlier resource waiters.
// any also records unscoped jobs, which must not overlap a wildcard owner.
type jobResourceSet struct {
	keys map[string]bool
	any  bool
	all  bool
}

func (set *jobResourceSet) add(resources []string) {
	set.any = true
	for _, resource := range resources {
		if resource == "*" {
			set.all = true
		}
		if set.keys == nil {
			set.keys = make(map[string]bool)
		}
		set.keys[resource] = true
	}
}

func (set *jobResourceSet) conflicts(resources []string) bool {
	if !set.any {
		return false
	}
	if set.all {
		return true
	}
	for _, resource := range resources {
		if resource == "*" || set.keys[resource] {
			return true
		}
	}
	return false
}

func (e *jobExecutor) runningResourcesLocked() jobResourceSet {
	var held jobResourceSet
	for _, work := range e.running {
		held.add(work.resources)
	}
	return held
}

// startReadyLocked acquires all of a job's scopes together, before dispatch.
// No goroutine waits for a resource while occupying a worker slot. Earlier
// blocked multi-resource jobs reserve queue order on EVERY scope they need, so
// a stream of younger jobs cannot starve them. Cancelled jobs need no engine
// access and may bypass resource barriers to finalize their receipts promptly.
func (e *jobExecutor) startReadyLocked() {
	held := e.runningResourcesLocked()
	var earlier jobResourceSet
	for i := 0; i < len(e.queue) && e.active < e.maxConcurrent; {
		work := e.queue[i]
		discard := e.stopped || work.ctx.Err() != nil
		if !discard && (held.conflicts(work.resources) || earlier.conflicts(work.resources)) {
			earlier.add(work.resources)
			i++
			continue
		}
		copy(e.queue[i:], e.queue[i+1:])
		e.queue[len(e.queue)-1] = nil
		e.queue = e.queue[:len(e.queue)-1]
		e.running[work.id] = work
		e.active++
		held.add(work.resources)
		go e.worker(work, discard)
	}
}

func (e *jobExecutor) invoke(work *scheduledJob, discard bool) {
	defer work.cancel()
	// Production callbacks record their own failures. This last guard ensures
	// an unexpected panic cannot abandon every other accepted job in the queue.
	defer func() {
		if value := recover(); value != nil {
			slog.Error("job executor callback panicked", "job_id", work.id, "panic", value)
		}
	}()
	if discard || work.ctx.Err() != nil {
		work.discard()
		return
	}
	work.run()
}

func (e *jobExecutor) worker(work *scheduledJob, discard bool) {
	e.invoke(work, discard)
	e.mu.Lock()
	// Keep resource ownership through all callback finalizers, not merely
	// through the engine returning or the visible job status becoming terminal.
	delete(e.running, work.id)
	delete(e.owned, work.id)
	e.active--
	e.startReadyLocked()
	e.notifyDrainedLocked()
	e.mu.Unlock()
}

// cancelQueued reclaims a waiting slot after its cancellation receipt is saved,
// without waiting behind a long-running operation. A concurrently selected job
// observes the same context; it remains owned by its worker, not this callback.
func (e *jobExecutor) cancelQueued(id string) bool {
	e.mu.Lock()
	var work *scheduledJob
	for i, queued := range e.queue {
		if queued.id == id {
			work = queued
			copy(e.queue[i:], e.queue[i+1:])
			e.queue[len(e.queue)-1] = nil
			e.queue = e.queue[:len(e.queue)-1]
			break
		}
	}
	// Removing a multi-resource waiter may unblock independent queued jobs.
	e.startReadyLocked()
	e.mu.Unlock()
	if work == nil {
		return false
	}
	work.cancel()
	e.invoke(work, true)
	e.mu.Lock()
	delete(e.owned, id)
	e.notifyDrainedLocked()
	e.mu.Unlock()
	return true
}

// closeAdmission is permanent and nonblocking. It cancels active/preparing
// requests; waiting jobs are discarded by bounded workers, not by creating
// another goroutine for each cancellation.
func (e *jobExecutor) closeAdmission() {
	e.mu.Lock()
	e.initLocked()
	e.stopped = true
	// Publish cancellation before a worker can select another queued job.
	// This is our own standard-library context, not a user callback or I/O.
	e.cancelRoot()
	e.startReadyLocked()
	e.notifyDrainedLocked()
	e.mu.Unlock()
}

func (e *jobExecutor) notifyDrainedLocked() {
	if e.stopped && e.preparing == 0 && len(e.owned) == 0 && !e.drainClosed {
		close(e.drained)
		e.drainClosed = true
	}
}

func (e *jobExecutor) idle() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.preparing == 0 && len(e.owned) == 0
}

func (e *jobExecutor) snapshot() JobExecutionStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	concurrency, capacity := e.maxConcurrent, e.maxQueued
	if concurrency == 0 {
		concurrency = DefaultJobConcurrency
	}
	if capacity == 0 {
		capacity = DefaultJobQueueCapacity
	}
	held := e.runningResourcesLocked()
	var earlier jobResourceSet
	waiting := 0
	for _, work := range e.queue {
		if e.stopped || work.ctx.Err() != nil {
			continue
		}
		if held.conflicts(work.resources) || earlier.conflicts(work.resources) {
			waiting++
		}
		earlier.add(work.resources)
	}
	return JobExecutionStatus{
		MaxConcurrent: concurrency, QueueCapacity: capacity,
		Running: e.active, Queued: len(e.queue), WaitingForResources: waiting,
		Preparing: e.preparing, Owned: len(e.owned) + e.preparing, Accepting: !e.stopped,
	}
}
