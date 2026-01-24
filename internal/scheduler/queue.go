package scheduler

import (
	"container/heap"
	"sync"
	"time"
)

// JobQueue is a priority queue for spawn jobs with fairness tracking.
type JobQueue struct {
	mu sync.RWMutex

	// heap is the underlying priority heap.
	jobs jobHeap

	// byID maps job IDs to jobs for O(1) lookup.
	byID map[string]*SpawnJob

	// batchCounts tracks jobs per batch for fairness.
	batchCounts map[string]int

	// sessionCounts tracks jobs per session for fairness.
	sessionCounts map[string]int

	// stats tracks queue statistics.
	stats QueueStats
}

// QueueStats contains queue statistics.
type QueueStats struct {
	// TotalEnqueued is the total number of jobs ever enqueued.
	TotalEnqueued int64 `json:"total_enqueued"`

	// TotalDequeued is the total number of jobs ever dequeued.
	TotalDequeued int64 `json:"total_dequeued"`

	// CurrentSize is the current queue size.
	CurrentSize int `json:"current_size"`

	// MaxSize is the maximum queue size ever observed.
	MaxSize int `json:"max_size"`

	// ByPriority is the count per priority level.
	ByPriority map[JobPriority]int `json:"by_priority"`

	// ByType is the count per job type.
	ByType map[JobType]int `json:"by_type"`

	// AvgWaitTime is the average time jobs spend in the queue.
	AvgWaitTime time.Duration `json:"avg_wait_time"`

	// MaxWaitTime is the maximum time a job spent in the queue.
	MaxWaitTime time.Duration `json:"max_wait_time"`

	// totalWaitTime is used to calculate average.
	totalWaitTime time.Duration
}

// NewJobQueue creates a new job queue.
func NewJobQueue() *JobQueue {
	return &JobQueue{
		jobs:          make(jobHeap, 0),
		byID:          make(map[string]*SpawnJob),
		batchCounts:   make(map[string]int),
		sessionCounts: make(map[string]int),
		stats: QueueStats{
			ByPriority: make(map[JobPriority]int),
			ByType:     make(map[JobType]int),
		},
	}
}

// Enqueue adds a job to the queue.
func (q *JobQueue) Enqueue(job *SpawnJob) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if _, exists := q.byID[job.ID]; exists {
		// Job already in queue, update it
		q.updateJobLocked(job)
		return
	}

	heap.Push(&q.jobs, job)
	q.byID[job.ID] = job

	// Track batch and session counts
	if job.BatchID != "" {
		q.batchCounts[job.BatchID]++
	}
	q.sessionCounts[job.SessionName]++

	// Update stats
	q.stats.TotalEnqueued++
	q.stats.CurrentSize = len(q.jobs)
	if q.stats.CurrentSize > q.stats.MaxSize {
		q.stats.MaxSize = q.stats.CurrentSize
	}
	q.stats.ByPriority[job.Priority]++
	q.stats.ByType[job.Type]++
}

// Dequeue removes and returns the highest priority job.
// Returns nil if the queue is empty.
func (q *JobQueue) Dequeue() *SpawnJob {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.jobs) == 0 {
		return nil
	}

	job := heap.Pop(&q.jobs).(*SpawnJob)
	delete(q.byID, job.ID)

	// Update counts
	if job.BatchID != "" {
		q.batchCounts[job.BatchID]--
		if q.batchCounts[job.BatchID] <= 0 {
			delete(q.batchCounts, job.BatchID)
		}
	}
	q.sessionCounts[job.SessionName]--
	if q.sessionCounts[job.SessionName] <= 0 {
		delete(q.sessionCounts, job.SessionName)
	}

	// Update stats
	q.stats.TotalDequeued++
	q.stats.CurrentSize = len(q.jobs)
	q.stats.ByPriority[job.Priority]--
	if q.stats.ByPriority[job.Priority] <= 0 {
		delete(q.stats.ByPriority, job.Priority)
	}
	q.stats.ByType[job.Type]--
	if q.stats.ByType[job.Type] <= 0 {
		delete(q.stats.ByType, job.Type)
	}

	// Track wait time
	waitTime := time.Since(job.CreatedAt)
	q.stats.totalWaitTime += waitTime
	if waitTime > q.stats.MaxWaitTime {
		q.stats.MaxWaitTime = waitTime
	}
	if q.stats.TotalDequeued > 0 {
		q.stats.AvgWaitTime = q.stats.totalWaitTime / time.Duration(q.stats.TotalDequeued)
	}

	return job
}

// Peek returns the highest priority job without removing it.
func (q *JobQueue) Peek() *SpawnJob {
	q.mu.RLock()
	defer q.mu.RUnlock()

	if len(q.jobs) == 0 {
		return nil
	}
	return q.jobs[0]
}

// Get returns a job by ID without removing it.
func (q *JobQueue) Get(id string) *SpawnJob {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.byID[id]
}

// Remove removes a job by ID.
func (q *JobQueue) Remove(id string) *SpawnJob {
	q.mu.Lock()
	defer q.mu.Unlock()

	job, ok := q.byID[id]
	if !ok {
		return nil
	}

	// Find and remove from heap
	for i, j := range q.jobs {
		if j.ID == id {
			heap.Remove(&q.jobs, i)
			break
		}
	}

	delete(q.byID, id)

	// Update counts
	if job.BatchID != "" {
		q.batchCounts[job.BatchID]--
		if q.batchCounts[job.BatchID] <= 0 {
			delete(q.batchCounts, job.BatchID)
		}
	}
	q.sessionCounts[job.SessionName]--
	if q.sessionCounts[job.SessionName] <= 0 {
		delete(q.sessionCounts, job.SessionName)
	}

	q.stats.CurrentSize = len(q.jobs)
	q.stats.ByPriority[job.Priority]--
	q.stats.ByType[job.Type]--

	return job
}

// updateJobLocked updates an existing job in place.
func (q *JobQueue) updateJobLocked(job *SpawnJob) {
	for i, j := range q.jobs {
		if j.ID == job.ID {
			q.jobs[i] = job
			heap.Fix(&q.jobs, i)
			break
		}
	}
	q.byID[job.ID] = job
}

// Len returns the number of jobs in the queue.
func (q *JobQueue) Len() int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return len(q.jobs)
}

// IsEmpty returns true if the queue is empty.
func (q *JobQueue) IsEmpty() bool {
	return q.Len() == 0
}

// Stats returns a copy of queue statistics.
func (q *JobQueue) Stats() QueueStats {
	q.mu.RLock()
	defer q.mu.RUnlock()

	stats := q.stats
	stats.ByPriority = make(map[JobPriority]int)
	for k, v := range q.stats.ByPriority {
		stats.ByPriority[k] = v
	}
	stats.ByType = make(map[JobType]int)
	for k, v := range q.stats.ByType {
		stats.ByType[k] = v
	}
	return stats
}

// ListAll returns all jobs in priority order.
func (q *JobQueue) ListAll() []*SpawnJob {
	q.mu.RLock()
	defer q.mu.RUnlock()

	jobs := make([]*SpawnJob, len(q.jobs))
	copy(jobs, q.jobs)
	return jobs
}

// ListBySession returns jobs for a specific session.
func (q *JobQueue) ListBySession(sessionName string) []*SpawnJob {
	q.mu.RLock()
	defer q.mu.RUnlock()

	var result []*SpawnJob
	for _, job := range q.jobs {
		if job.SessionName == sessionName {
			result = append(result, job)
		}
	}
	return result
}

// ListByBatch returns jobs for a specific batch.
func (q *JobQueue) ListByBatch(batchID string) []*SpawnJob {
	q.mu.RLock()
	defer q.mu.RUnlock()

	var result []*SpawnJob
	for _, job := range q.jobs {
		if job.BatchID == batchID {
			result = append(result, job)
		}
	}
	return result
}

// CountBySession returns the number of jobs for a session.
func (q *JobQueue) CountBySession(sessionName string) int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.sessionCounts[sessionName]
}

// CountByBatch returns the number of jobs in a batch.
func (q *JobQueue) CountByBatch(batchID string) int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.batchCounts[batchID]
}

// Clear removes all jobs from the queue.
func (q *JobQueue) Clear() []*SpawnJob {
	q.mu.Lock()
	defer q.mu.Unlock()

	removed := make([]*SpawnJob, len(q.jobs))
	copy(removed, q.jobs)

	q.jobs = make(jobHeap, 0)
	q.byID = make(map[string]*SpawnJob)
	q.batchCounts = make(map[string]int)
	q.sessionCounts = make(map[string]int)
	q.stats.CurrentSize = 0

	return removed
}

// CancelSession cancels all jobs for a session.
func (q *JobQueue) CancelSession(sessionName string) []*SpawnJob {
	q.mu.Lock()
	defer q.mu.Unlock()

	var cancelled []*SpawnJob
	var toRemove []int

	for i, job := range q.jobs {
		if job.SessionName == sessionName {
			job.Cancel()
			cancelled = append(cancelled, job)
			toRemove = append(toRemove, i)
		}
	}

	// Remove in reverse order to maintain indices
	for i := len(toRemove) - 1; i >= 0; i-- {
		idx := toRemove[i]
		heap.Remove(&q.jobs, idx)
	}

	for _, job := range cancelled {
		delete(q.byID, job.ID)
	}

	delete(q.sessionCounts, sessionName)
	q.stats.CurrentSize = len(q.jobs)

	return cancelled
}

// CancelBatch cancels all jobs in a batch.
func (q *JobQueue) CancelBatch(batchID string) []*SpawnJob {
	q.mu.Lock()
	defer q.mu.Unlock()

	var cancelled []*SpawnJob
	var toRemove []int

	for i, job := range q.jobs {
		if job.BatchID == batchID {
			job.Cancel()
			cancelled = append(cancelled, job)
			toRemove = append(toRemove, i)
		}
	}

	// Remove in reverse order
	for i := len(toRemove) - 1; i >= 0; i-- {
		idx := toRemove[i]
		heap.Remove(&q.jobs, idx)
	}

	for _, job := range cancelled {
		delete(q.byID, job.ID)
	}

	delete(q.batchCounts, batchID)
	q.stats.CurrentSize = len(q.jobs)

	return cancelled
}

// jobHeap implements heap.Interface for SpawnJobs.
type jobHeap []*SpawnJob

func (h jobHeap) Len() int { return len(h) }

func (h jobHeap) Less(i, j int) bool {
	// Lower priority value = higher priority
	if h[i].Priority != h[j].Priority {
		return h[i].Priority < h[j].Priority
	}
	// Same priority: FIFO order
	return h[i].CreatedAt.Before(h[j].CreatedAt)
}

func (h jobHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

func (h *jobHeap) Push(x interface{}) {
	*h = append(*h, x.(*SpawnJob))
}

func (h *jobHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[0 : n-1]
	return item
}

// FairScheduler wraps JobQueue with fairness guarantees.
type FairScheduler struct {
	queue *JobQueue

	// maxPerSession limits jobs that can run per session at once.
	maxPerSession int

	// maxPerBatch limits jobs that can run per batch at once.
	maxPerBatch int

	// running tracks currently running jobs by session.
	running map[string]int

	// mu protects running map.
	mu sync.RWMutex
}

// FairSchedulerConfig configures the fair scheduler.
type FairSchedulerConfig struct {
	MaxPerSession int `json:"max_per_session"`
	MaxPerBatch   int `json:"max_per_batch"`
}

// DefaultFairSchedulerConfig returns sensible defaults.
func DefaultFairSchedulerConfig() FairSchedulerConfig {
	return FairSchedulerConfig{
		MaxPerSession: 3,
		MaxPerBatch:   5,
	}
}

// NewFairScheduler creates a new fair scheduler.
func NewFairScheduler(cfg FairSchedulerConfig) *FairScheduler {
	return &FairScheduler{
		queue:         NewJobQueue(),
		maxPerSession: cfg.MaxPerSession,
		maxPerBatch:   cfg.MaxPerBatch,
		running:       make(map[string]int),
	}
}

// Enqueue adds a job to the queue.
func (f *FairScheduler) Enqueue(job *SpawnJob) {
	f.queue.Enqueue(job)
}

// TryDequeue returns the next job that can run without violating fairness.
// Returns nil if no eligible job is available.
func (f *FairScheduler) TryDequeue() *SpawnJob {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Iterate through jobs to find one that can run
	for _, job := range f.queue.ListAll() {
		// Check per-session limit
		if f.maxPerSession > 0 && f.running[job.SessionName] >= f.maxPerSession {
			continue
		}

		// Check per-batch limit
		if f.maxPerBatch > 0 && job.BatchID != "" {
			// Count running jobs in this batch
			batchRunning := 0
			for _, j := range f.queue.ListAll() {
				if j.BatchID == job.BatchID && j.GetStatus() == StatusRunning {
					batchRunning++
				}
			}
			if batchRunning >= f.maxPerBatch {
				continue
			}
		}

		// This job can run, remove and return it
		removed := f.queue.Remove(job.ID)
		if removed != nil {
			f.running[job.SessionName]++
		}
		return removed
	}

	return nil
}

// MarkComplete marks a job as complete for fairness tracking.
func (f *FairScheduler) MarkComplete(job *SpawnJob) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.running[job.SessionName]--
	if f.running[job.SessionName] <= 0 {
		delete(f.running, job.SessionName)
	}
}

// Queue returns the underlying queue for direct access.
func (f *FairScheduler) Queue() *JobQueue {
	return f.queue
}

// RunningCount returns the number of running jobs for a session.
func (f *FairScheduler) RunningCount(sessionName string) int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.running[sessionName]
}
