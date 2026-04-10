package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Scheduler is the global spawn scheduler that serializes and paces
// all pane and agent creation operations.
type Scheduler struct {
	mu sync.RWMutex

	// config is the scheduler configuration.
	config Config

	// queue is the priority queue for pending jobs.
	queue *FairScheduler

	// globalLimiter is the global rate limiter.
	globalLimiter *RateLimiter

	// agentLimiters provides per-agent-type rate limiting.
	agentLimiters *PerAgentLimiter

	// agentCaps provides per-agent concurrency caps.
	agentCaps *AgentCaps

	// running tracks currently executing jobs.
	running map[string]*SpawnJob

	// completed tracks recently completed jobs for status queries.
	completed []*SpawnJob

	// maxCompleted is the maximum number of completed jobs to retain.
	maxCompleted int

	// workers is the number of concurrent execution workers.
	workers int

	// executor is the function that executes spawn jobs.
	executor SpawnExecutor

	// hooks are callbacks for job lifecycle events.
	hooks Hooks

	// backoff is the backoff controller for resource errors.
	backoff *BackoffController

	// headroom is the pre-spawn resource headroom guard.
	headroom *HeadroomGuard

	// running state
	started   atomic.Bool
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	jobNotify chan struct{}

	// stats tracks scheduler statistics.
	stats Stats

	// paused indicates if the scheduler is paused.
	paused atomic.Bool
}

type schedulerContext struct {
	done     chan struct{}
	canceled atomic.Bool
	once     sync.Once
}

func newSchedulerContext() (*schedulerContext, context.CancelFunc) {
	ctx := &schedulerContext{done: make(chan struct{})}
	cancel := func() {
		ctx.once.Do(func() {
			ctx.canceled.Store(true)
			close(ctx.done)
		})
	}
	return ctx, cancel
}

func (c *schedulerContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func (c *schedulerContext) Done() <-chan struct{} {
	return c.done
}

func (c *schedulerContext) Err() error {
	if c.canceled.Load() {
		return context.Canceled
	}
	return nil
}

func (c *schedulerContext) Value(any) any {
	return nil
}

// SpawnExecutor is a function that executes a spawn job.
type SpawnExecutor func(ctx context.Context, job *SpawnJob) error

// Hooks contains callbacks for job lifecycle events.
type Hooks struct {
	// OnJobEnqueued is called when a job is added to the queue.
	OnJobEnqueued func(job *SpawnJob)

	// OnJobStarted is called when a job starts executing.
	OnJobStarted func(job *SpawnJob)

	// OnJobCompleted is called when a job completes successfully.
	OnJobCompleted func(job *SpawnJob)

	// OnJobFailed is called when a job fails.
	OnJobFailed func(job *SpawnJob, err error)

	// OnJobRetrying is called when a job is about to retry.
	OnJobRetrying func(job *SpawnJob, attempt int)

	// OnBackpressure is called when the queue is experiencing backpressure.
	OnBackpressure func(queueSize int, waitTime time.Duration)

	// OnGuardrailTriggered is called when a guardrail blocks a spawn.
	OnGuardrailTriggered func(job *SpawnJob, reason string)
}

// Config configures the scheduler.
type Config struct {
	// MaxConcurrent is the maximum number of concurrent spawn operations.
	MaxConcurrent int `json:"max_concurrent"`

	// GlobalRateLimit is the global rate limiter configuration.
	GlobalRateLimit LimiterConfig `json:"global_rate_limit"`

	// AgentRateLimits is the per-agent rate limiter configuration.
	AgentRateLimits AgentLimiterConfig `json:"agent_rate_limits"`

	// AgentCaps is the per-agent concurrency caps configuration.
	AgentCaps AgentCapsConfig `json:"agent_caps"`

	// FairScheduler is the fair scheduler configuration.
	FairScheduler FairSchedulerConfig `json:"fair_scheduler"`

	// Backoff is the backoff configuration for resource errors.
	Backoff BackoffConfig `json:"backoff"`

	// MaxCompleted is the number of completed jobs to retain for status.
	MaxCompleted int `json:"max_completed"`

	// DefaultRetries is the default number of retries for failed jobs.
	DefaultRetries int `json:"default_retries"`

	// DefaultRetryDelay is the default delay between retries.
	DefaultRetryDelay time.Duration `json:"default_retry_delay"`

	// BackpressureThreshold is the queue size that triggers backpressure alerts.
	BackpressureThreshold int `json:"backpressure_threshold"`

	// Headroom is the pre-spawn resource headroom configuration.
	Headroom HeadroomConfig `json:"headroom"`
}

// DefaultConfig returns sensible default configuration.
func DefaultConfig() Config {
	return Config{
		MaxConcurrent:         4,
		GlobalRateLimit:       DefaultLimiterConfig(),
		AgentRateLimits:       DefaultAgentLimiterConfig(),
		AgentCaps:             DefaultAgentCapsConfig(),
		FairScheduler:         DefaultFairSchedulerConfig(),
		Backoff:               DefaultBackoffConfig(),
		MaxCompleted:          100,
		DefaultRetries:        3,
		DefaultRetryDelay:     time.Second,
		BackpressureThreshold: 50,
		Headroom:              DefaultHeadroomConfig(),
	}
}

// Stats contains scheduler statistics.
type Stats struct {
	// TotalSubmitted is jobs submitted to scheduler.
	TotalSubmitted int64 `json:"total_submitted"`

	// TotalCompleted is jobs that completed successfully.
	TotalCompleted int64 `json:"total_completed"`

	// TotalFailed is jobs that failed after all retries.
	TotalFailed int64 `json:"total_failed"`

	// TotalRetried is the total number of retry attempts.
	TotalRetried int64 `json:"total_retried"`

	// CurrentQueueSize is the current queue size.
	CurrentQueueSize int `json:"current_queue_size"`

	// CurrentRunning is the number of currently running jobs.
	CurrentRunning int `json:"current_running"`

	// AvgQueueTime is the average time jobs spend in queue.
	AvgQueueTime time.Duration `json:"avg_queue_time"`

	// AvgExecutionTime is the average job execution time.
	AvgExecutionTime time.Duration `json:"avg_execution_time"`

	// IsPaused indicates if the scheduler is paused.
	IsPaused bool `json:"is_paused"`

	// StartedAt is when the scheduler started.
	StartedAt time.Time `json:"started_at"`

	// Uptime is how long the scheduler has been running.
	Uptime time.Duration `json:"uptime"`

	// LimiterStats contains rate limiter statistics.
	LimiterStats LimiterStats `json:"limiter_stats"`

	// QueueStats contains queue statistics.
	QueueStats QueueStats `json:"queue_stats"`

	// BackoffStats contains backoff statistics.
	BackoffStats BackoffStats `json:"backoff_stats"`

	// CapsStats contains per-agent concurrency cap statistics.
	CapsStats CapsStats `json:"caps_stats"`

	// InGlobalBackoff indicates if global backoff is active.
	InGlobalBackoff bool `json:"in_global_backoff"`

	// RemainingBackoff is the remaining backoff duration if in global backoff.
	RemainingBackoff time.Duration `json:"remaining_backoff,omitempty"`

	// HeadroomStatus contains resource headroom status.
	HeadroomStatus HeadroomStatus `json:"headroom_status"`
}

// New creates a new scheduler with the given configuration.
func New(cfg Config) *Scheduler {
	s := &Scheduler{
		config:        cfg,
		queue:         NewFairScheduler(cfg.FairScheduler),
		globalLimiter: NewRateLimiter(cfg.GlobalRateLimit),
		agentLimiters: NewPerAgentLimiter(cfg.AgentRateLimits),
		agentCaps:     NewAgentCaps(cfg.AgentCaps),
		backoff:       NewBackoffController(cfg.Backoff),
		running:       make(map[string]*SpawnJob),
		completed:     make([]*SpawnJob, 0, cfg.MaxCompleted),
		maxCompleted:  cfg.MaxCompleted,
		workers:       cfg.MaxConcurrent,
		jobNotify:     make(chan struct{}, 1),
	}

	s.resetRuntimeState()

	// Set scheduler reference for global backoff pause/resume
	s.backoff.SetScheduler(s)

	return s
}

func (s *Scheduler) resetRuntimeState() {
	s.ctx, s.cancel = newSchedulerContext()
	s.paused.Store(false)
	s.rebuildHeadroomGuard()
}

func (s *Scheduler) rebuildHeadroomGuard() {
	if s.headroom != nil {
		s.headroom.Stop()
	}

	s.headroom = NewHeadroomGuard(s.config.Headroom)
	s.headroom.SetCallbacks(
		// onBlocked
		func(reason string, limits *ResourceLimits, usage *ResourceUsage) {
			if s.hooks.OnGuardrailTriggered != nil {
				// Create a dummy job for the callback (guardrail affects all jobs)
				s.hooks.OnGuardrailTriggered(nil, reason)
			}
		},
		// onUnblocked - resume job processing
		func() {
			select {
			case s.jobNotify <- struct{}{}:
			default:
			}
		},
		// onWarning
		func(reason string, limits *ResourceLimits, usage *ResourceUsage) {
			// Warning is logged by the guard, no additional action needed
		},
	)
}

// SetExecutor sets the function that executes spawn jobs.
func (s *Scheduler) SetExecutor(executor SpawnExecutor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executor = executor
}

// SetHooks sets the lifecycle hooks.
func (s *Scheduler) SetHooks(hooks Hooks) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hooks = hooks
}

// Start starts the scheduler workers.
func (s *Scheduler) Start() error {
	if s.started.Load() {
		return fmt.Errorf("scheduler already started")
	}

	s.mu.Lock()
	if s.executor == nil {
		s.mu.Unlock()
		return fmt.Errorf("executor not set")
	}
	s.resetRuntimeState()
	s.stats.StartedAt = time.Now()
	s.mu.Unlock()

	s.started.Store(true)

	// Start worker goroutines
	for i := 0; i < s.workers; i++ {
		s.wg.Add(1)
		go s.worker(i)
	}

	slog.Info("scheduler started", "workers", s.workers)
	return nil
}

// Stop gracefully stops the scheduler.
func (s *Scheduler) Stop() {
	if !s.started.Load() {
		return
	}

	s.cancel()
	s.wg.Wait()
	s.started.Store(false)

	// Stop the headroom guard
	if s.headroom != nil {
		s.headroom.Stop()
	}

	slog.Info("scheduler stopped")
}

// Submit submits a new spawn job to the scheduler.
func (s *Scheduler) Submit(job *SpawnJob) error {
	if !s.started.Load() {
		return fmt.Errorf("scheduler not started")
	}

	if job.ID == "" {
		job.ID = generateID()
	}
	if job.MaxRetries == 0 {
		job.MaxRetries = s.config.DefaultRetries
	}
	if job.RetryDelay == 0 {
		job.RetryDelay = s.config.DefaultRetryDelay
	}

	job.SetStatus(StatusPending)
	s.queue.Enqueue(job)

	atomic.AddInt64(&s.stats.TotalSubmitted, 1)

	// Check for backpressure
	queueSize := s.queue.Queue().Len()
	if queueSize >= s.config.BackpressureThreshold {
		if s.hooks.OnBackpressure != nil {
			waitTime := s.globalLimiter.TimeUntilNextToken()
			s.hooks.OnBackpressure(queueSize, waitTime)
		}
	}

	if s.hooks.OnJobEnqueued != nil {
		s.hooks.OnJobEnqueued(job)
	}

	// Notify workers
	select {
	case s.jobNotify <- struct{}{}:
	default:
	}

	return nil
}

// SubmitBatch submits multiple jobs as a batch.
func (s *Scheduler) SubmitBatch(jobs []*SpawnJob) (string, error) {
	if len(jobs) == 0 {
		return "", nil
	}

	batchID := generateID()
	for _, job := range jobs {
		job.BatchID = batchID
		if err := s.Submit(job); err != nil {
			// Cancel already-submitted jobs on error
			s.CancelBatch(batchID)
			return "", err
		}
	}

	return batchID, nil
}

// Cancel cancels a job by ID.
func (s *Scheduler) Cancel(jobID string) bool {
	// Check queue first
	if job := s.queue.Queue().Remove(jobID); job != nil {
		job.Cancel()
		return true
	}

	// Check running
	s.mu.Lock()
	if job, ok := s.running[jobID]; ok {
		job.Cancel()
		s.mu.Unlock()
		return true
	}
	s.mu.Unlock()

	return false
}

// CancelSession cancels all jobs for a session.
func (s *Scheduler) CancelSession(sessionName string) int {
	cancelled := s.queue.Queue().CancelSession(sessionName)

	s.mu.Lock()
	for _, job := range s.running {
		if job.SessionName == sessionName {
			job.Cancel()
			cancelled = append(cancelled, job)
		}
	}
	s.mu.Unlock()

	return len(cancelled)
}

// CancelBatch cancels all jobs in a batch.
func (s *Scheduler) CancelBatch(batchID string) int {
	cancelled := s.queue.Queue().CancelBatch(batchID)

	s.mu.Lock()
	for _, job := range s.running {
		if job.BatchID == batchID {
			job.Cancel()
			cancelled = append(cancelled, job)
		}
	}
	s.mu.Unlock()

	return len(cancelled)
}

// Pause pauses job processing.
func (s *Scheduler) Pause() {
	s.paused.Store(true)
	slog.Info("scheduler paused")
}

// Resume resumes job processing.
func (s *Scheduler) Resume() {
	s.paused.Store(false)
	slog.Info("scheduler resumed")

	// Notify workers
	select {
	case s.jobNotify <- struct{}{}:
	default:
	}
}

// IsPaused returns true if the scheduler is paused.
func (s *Scheduler) IsPaused() bool {
	return s.paused.Load()
}

// GetJob returns a job by ID.
func (s *Scheduler) GetJob(jobID string) *SpawnJob {
	// Check queue
	if job := s.queue.Queue().Get(jobID); job != nil {
		return job.Clone()
	}

	// Check running
	s.mu.RLock()
	if job, ok := s.running[jobID]; ok {
		s.mu.RUnlock()
		return job.Clone()
	}
	s.mu.RUnlock()

	// Check completed
	s.mu.RLock()
	for _, job := range s.completed {
		if job.ID == jobID {
			s.mu.RUnlock()
			return job.Clone()
		}
	}
	s.mu.RUnlock()

	return nil
}

// GetQueuedJobs returns all queued jobs.
func (s *Scheduler) GetQueuedJobs() []*SpawnJob {
	jobs := s.queue.Queue().ListAll()
	result := make([]*SpawnJob, len(jobs))
	for i, job := range jobs {
		result[i] = job.Clone()
	}
	return result
}

// GetRunningJobs returns all currently running jobs.
func (s *Scheduler) GetRunningJobs() []*SpawnJob {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*SpawnJob, 0, len(s.running))
	for _, job := range s.running {
		result = append(result, job.Clone())
	}
	return result
}

// GetRecentCompleted returns recently completed jobs.
func (s *Scheduler) GetRecentCompleted(limit int) []*SpawnJob {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 || limit > len(s.completed) {
		limit = len(s.completed)
	}

	// Return most recent first
	result := make([]*SpawnJob, limit)
	for i := 0; i < limit; i++ {
		result[i] = s.completed[len(s.completed)-1-i].Clone()
	}
	return result
}

// Stats returns scheduler statistics.
func (s *Scheduler) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := s.stats
	stats.CurrentQueueSize = s.queue.Queue().Len()
	stats.CurrentRunning = len(s.running)
	stats.IsPaused = s.paused.Load()
	if !stats.StartedAt.IsZero() {
		stats.Uptime = time.Since(stats.StartedAt)
	}
	stats.LimiterStats = s.globalLimiter.Stats()
	stats.QueueStats = s.queue.Queue().Stats()
	stats.BackoffStats = s.backoff.Stats()
	stats.CapsStats = s.agentCaps.Stats()
	stats.InGlobalBackoff = s.backoff.IsInGlobalBackoff()
	stats.RemainingBackoff = s.backoff.RemainingBackoff()
	if s.headroom != nil {
		stats.HeadroomStatus = s.headroom.Status()
	}

	return stats
}

// EstimateETA estimates when a queued job will start.
func (s *Scheduler) EstimateETA(jobID string) (time.Duration, error) {
	job := s.queue.Queue().Get(jobID)
	if job == nil {
		return 0, fmt.Errorf("job not found in queue")
	}

	// Count jobs ahead in queue
	jobsAhead := 0
	for _, j := range s.queue.Queue().ListAll() {
		if j.Priority < job.Priority {
			jobsAhead++
		} else if j.Priority == job.Priority && j.CreatedAt.Before(job.CreatedAt) {
			jobsAhead++
		}
	}

	// Estimate based on rate limit and concurrency
	tokensNeeded := float64(jobsAhead) / float64(s.workers)
	currentTokens := s.globalLimiter.Stats().CurrentTokens
	var etaSeconds float64
	if currentTokens > 0 {
		etaSeconds = tokensNeeded / currentTokens
	} else {
		etaSeconds = tokensNeeded * 2 // conservative estimate when bucket is empty
	}
	if etaSeconds < 0 {
		etaSeconds = 0
	}

	// Add time until next token
	eta := time.Duration(etaSeconds*1000)*time.Millisecond + s.globalLimiter.TimeUntilNextToken()

	return eta, nil
}

// worker is a goroutine that processes jobs from the queue.
func (s *Scheduler) worker(id int) {
	defer s.wg.Done()

	slog.Debug("worker started", "worker_id", id)

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			slog.Debug("worker stopping", "worker_id", id)
			return
		case <-s.jobNotify:
			s.processJobs(id)
		case <-ticker.C:
			// Periodic check
			s.processJobs(id)
		}
	}
}

// processJobs processes available jobs.
func (s *Scheduler) processJobs(workerID int) {
	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		if s.paused.Load() {
			return
		}

		// Check resource headroom before processing
		if s.headroom != nil {
			if allowed, reason := s.headroom.CheckHeadroom(); !allowed {
				// Headroom exhausted, don't process jobs
				slog.Debug("job processing blocked by headroom guard",
					"worker_id", workerID,
					"reason", reason,
				)
				return
			}
		}

		// Try to get a job with available agent caps
		job := s.queue.TryDequeueWithCallbacks(
			func(job *SpawnJob) bool {
				if job.AgentType == "" {
					return true
				}
				return s.agentCaps.TryAcquire(job.AgentType)
			},
			func(job *SpawnJob) {
				if job.AgentType != "" {
					s.agentCaps.Release(job.AgentType)
				}
			},
		)
		if job == nil {
			return // No jobs available
		}

		// Wait for rate limit
		if job.AgentType != "" {
			if err := s.agentLimiters.Wait(s.ctx, job.AgentType); err != nil {
				// Context cancelled, put job back and release cap
				s.agentCaps.Release(job.AgentType)
				s.queue.Enqueue(job)
				return
			}
		}

		if err := s.globalLimiter.Wait(s.ctx); err != nil {
			// Context cancelled, put job back and release cap
			if job.AgentType != "" {
				s.agentCaps.Release(job.AgentType)
			}
			s.queue.Enqueue(job)
			return
		}

		// Execute the job
		s.executeJob(workerID, job)
	}
}

// executeJob executes a single job.
func (s *Scheduler) executeJob(workerID int, job *SpawnJob) {
	job.SetStatus(StatusRunning)

	s.mu.Lock()
	s.running[job.ID] = job
	s.mu.Unlock()

	if s.hooks.OnJobStarted != nil {
		s.hooks.OnJobStarted(job)
	}

	slog.Debug("executing job",
		"worker_id", workerID,
		"job_id", job.ID,
		"type", job.Type,
		"session", job.SessionName,
	)

	// Execute with job context
	s.mu.RLock()
	executor := s.executor
	s.mu.RUnlock()

	err := executor(job.Context(), job)

	s.mu.Lock()
	delete(s.running, job.ID)
	s.mu.Unlock()

	s.queue.MarkComplete(job)

	if err != nil {
		if job.IsCancelled() {
			job.SetStatus(StatusCancelled)
			// Release agent cap
			if job.AgentType != "" {
				s.agentCaps.Release(job.AgentType)
			}
		} else {
			// Classify the error to check for resource exhaustion
			// Get stderr hint from job metadata if available
			stderrHint := ""
			if hint, ok := job.Metadata["stderr"].(string); ok {
				stderrHint = hint
			}
			exitCode := 0
			if code, ok := job.Metadata["exit_code"].(int); ok {
				exitCode = code
			}

			resErr := ClassifyError(err, exitCode, stderrHint)

			// Record failure for cap cooldown on resource errors
			if resErr != nil && resErr.Retryable && job.AgentType != "" {
				s.agentCaps.RecordFailure(job.AgentType)
			}

			// Use backoff controller for resource errors
			shouldRetry, backoffDelay := s.backoff.HandleError(job, resErr)

			// Release agent cap before retry delay
			if job.AgentType != "" {
				s.agentCaps.Release(job.AgentType)
			}

			if shouldRetry && job.CanRetry() {
				job.IncrementRetry()
				atomic.AddInt64(&s.stats.TotalRetried, 1)

				if s.hooks.OnJobRetrying != nil {
					s.hooks.OnJobRetrying(job, job.RetryCount)
				}

				// Use backoff delay for resource errors, otherwise use job's retry delay
				delay := job.RetryDelay
				if backoffDelay > 0 {
					delay = backoffDelay
				}

				slog.Info("retrying job after delay",
					"job_id", job.ID,
					"retry_count", job.RetryCount,
					"delay", delay,
					"resource_error", resErr != nil && resErr.Retryable,
				)

				// Re-enqueue after delay
				time.AfterFunc(delay, func() {
					job.SetStatus(StatusPending)
					s.queue.Enqueue(job)
					select {
					case s.jobNotify <- struct{}{}:
					default:
					}
				})
				return
			} else if job.CanRetry() {
				// Non-resource error that can still retry
				job.IncrementRetry()
				atomic.AddInt64(&s.stats.TotalRetried, 1)

				if s.hooks.OnJobRetrying != nil {
					s.hooks.OnJobRetrying(job, job.RetryCount)
				}

				// Re-enqueue after standard delay
				time.AfterFunc(job.RetryDelay, func() {
					job.SetStatus(StatusPending)
					s.queue.Enqueue(job)
					select {
					case s.jobNotify <- struct{}{}:
					default:
					}
				})
				return
			} else {
				job.SetStatus(StatusFailed)
				job.SetError(err)
				atomic.AddInt64(&s.stats.TotalFailed, 1)

				if s.hooks.OnJobFailed != nil {
					s.hooks.OnJobFailed(job, err)
				}
			}
		}
	} else {
		job.SetStatus(StatusCompleted)
		atomic.AddInt64(&s.stats.TotalCompleted, 1)

		// Record success to reset backoff state
		s.backoff.RecordSuccess()

		// Record success for cap recovery
		if job.AgentType != "" {
			s.agentCaps.RecordSuccess(job.AgentType)
			s.agentCaps.Release(job.AgentType)
		}

		if s.hooks.OnJobCompleted != nil {
			s.hooks.OnJobCompleted(job)
		}
	}

	// Add to completed list
	s.mu.Lock()
	s.completed = append(s.completed, job.Clone())
	if len(s.completed) > s.maxCompleted {
		s.completed = s.completed[len(s.completed)-s.maxCompleted:]
	}
	s.mu.Unlock()

	// Call job callback if set
	if job.Callback != nil {
		job.Callback(job)
	}
}

// generateID generates a random hex ID.
func generateID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Global scheduler instance.
var globalScheduler *Scheduler
var globalSchedulerOnce sync.Once

// Global returns the global scheduler instance, creating it if necessary.
func Global() *Scheduler {
	globalSchedulerOnce.Do(func() {
		globalScheduler = New(DefaultConfig())
	})
	return globalScheduler
}

// SetGlobal sets the global scheduler instance.
func SetGlobal(s *Scheduler) {
	globalScheduler = s
}
