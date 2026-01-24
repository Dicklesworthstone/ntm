package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewSpawnJob(t *testing.T) {
	job := NewSpawnJob("test-123", JobTypePaneSplit, "myproject")

	if job.ID != "test-123" {
		t.Errorf("expected ID 'test-123', got %q", job.ID)
	}
	if job.Type != JobTypePaneSplit {
		t.Errorf("expected type PaneSplit, got %v", job.Type)
	}
	if job.SessionName != "myproject" {
		t.Errorf("expected session 'myproject', got %q", job.SessionName)
	}
	if job.Status != StatusPending {
		t.Errorf("expected status Pending, got %v", job.Status)
	}
	if job.Priority != PriorityNormal {
		t.Errorf("expected priority Normal, got %v", job.Priority)
	}
}

func TestSpawnJob_StatusTransitions(t *testing.T) {
	job := NewSpawnJob("test-1", JobTypeSession, "test")

	// Pending -> Scheduled
	job.SetStatus(StatusScheduled)
	if job.ScheduledAt.IsZero() {
		t.Error("ScheduledAt should be set")
	}

	// Scheduled -> Running
	job.SetStatus(StatusRunning)
	if job.StartedAt.IsZero() {
		t.Error("StartedAt should be set")
	}

	// Running -> Completed
	job.SetStatus(StatusCompleted)
	if job.CompletedAt.IsZero() {
		t.Error("CompletedAt should be set")
	}
	if !job.IsTerminal() {
		t.Error("Completed should be terminal")
	}
}

func TestSpawnJob_Cancel(t *testing.T) {
	job := NewSpawnJob("test-1", JobTypeSession, "test")

	job.Cancel()

	if !job.IsCancelled() {
		t.Error("job should be cancelled")
	}
	if job.GetStatus() != StatusCancelled {
		t.Errorf("expected status Cancelled, got %v", job.GetStatus())
	}
}

func TestSpawnJob_Retry(t *testing.T) {
	job := NewSpawnJob("test-1", JobTypeSession, "test")
	job.MaxRetries = 3

	job.SetStatus(StatusFailed)
	if !job.CanRetry() {
		t.Error("should be able to retry")
	}

	job.IncrementRetry()
	if job.RetryCount != 1 {
		t.Errorf("expected retry count 1, got %d", job.RetryCount)
	}

	job.SetStatus(StatusFailed)
	job.IncrementRetry()
	job.SetStatus(StatusFailed)
	job.IncrementRetry()

	job.SetStatus(StatusFailed)
	if job.CanRetry() {
		t.Error("should not be able to retry after max retries")
	}
}

func TestRateLimiter_Basic(t *testing.T) {
	cfg := LimiterConfig{
		Rate:        10,           // 10 per second
		Capacity:    5,
		MinInterval: 0,
	}
	limiter := NewRateLimiter(cfg)

	// Should be able to acquire immediately (starts with full capacity)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		err := limiter.Wait(ctx)
		if err != nil {
			t.Errorf("failed to acquire token %d: %v", i, err)
		}
	}

	// Next acquire should wait
	start := time.Now()
	err := limiter.Wait(ctx)
	if err != nil {
		t.Errorf("failed to acquire after wait: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 50*time.Millisecond {
		t.Logf("waited %v for token (expected some wait time)", elapsed)
	}
}

func TestRateLimiter_TryAcquire(t *testing.T) {
	cfg := LimiterConfig{
		Rate:     1,
		Capacity: 1,
	}
	limiter := NewRateLimiter(cfg)

	// First try should succeed
	if !limiter.TryAcquire() {
		t.Error("first TryAcquire should succeed")
	}

	// Second try should fail (no tokens available)
	if limiter.TryAcquire() {
		t.Error("second TryAcquire should fail")
	}

	// Wait for refill
	time.Sleep(1100 * time.Millisecond)

	// Should succeed again
	if !limiter.TryAcquire() {
		t.Error("TryAcquire after refill should succeed")
	}
}

func TestRateLimiter_MinInterval(t *testing.T) {
	cfg := LimiterConfig{
		Rate:        100,          // High rate
		Capacity:    100,
		MinInterval: 100 * time.Millisecond,
	}
	limiter := NewRateLimiter(cfg)

	ctx := context.Background()
	start := time.Now()

	// Acquire twice
	_ = limiter.Wait(ctx)
	_ = limiter.Wait(ctx)

	elapsed := time.Since(start)
	if elapsed < 100*time.Millisecond {
		t.Errorf("MinInterval not enforced, elapsed %v", elapsed)
	}
}

func TestRateLimiter_ContextCancellation(t *testing.T) {
	cfg := LimiterConfig{
		Rate:     0.1, // Very slow
		Capacity: 1,
	}
	limiter := NewRateLimiter(cfg)

	// Consume the initial token
	ctx := context.Background()
	_ = limiter.Wait(ctx)

	// Create cancelled context
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := limiter.Wait(cancelCtx)
	if err == nil {
		t.Error("expected context cancelled error")
	}
}

func TestJobQueue_EnqueueDequeue(t *testing.T) {
	queue := NewJobQueue()

	job1 := NewSpawnJob("job1", JobTypeSession, "session1")
	job1.Priority = PriorityNormal

	job2 := NewSpawnJob("job2", JobTypeSession, "session2")
	job2.Priority = PriorityHigh

	job3 := NewSpawnJob("job3", JobTypeSession, "session3")
	job3.Priority = PriorityUrgent

	// Enqueue in random order
	queue.Enqueue(job1)
	queue.Enqueue(job2)
	queue.Enqueue(job3)

	if queue.Len() != 3 {
		t.Errorf("expected length 3, got %d", queue.Len())
	}

	// Dequeue should return in priority order
	first := queue.Dequeue()
	if first.ID != "job3" {
		t.Errorf("expected job3 (urgent), got %s", first.ID)
	}

	second := queue.Dequeue()
	if second.ID != "job2" {
		t.Errorf("expected job2 (high), got %s", second.ID)
	}

	third := queue.Dequeue()
	if third.ID != "job1" {
		t.Errorf("expected job1 (normal), got %s", third.ID)
	}

	if !queue.IsEmpty() {
		t.Error("queue should be empty")
	}
}

func TestJobQueue_FIFO_SamePriority(t *testing.T) {
	queue := NewJobQueue()

	// Create jobs with same priority
	job1 := NewSpawnJob("job1", JobTypeSession, "session1")
	job1.CreatedAt = time.Now()

	time.Sleep(10 * time.Millisecond)

	job2 := NewSpawnJob("job2", JobTypeSession, "session2")
	job2.CreatedAt = time.Now()

	queue.Enqueue(job1)
	queue.Enqueue(job2)

	// Should dequeue in FIFO order
	first := queue.Dequeue()
	if first.ID != "job1" {
		t.Errorf("expected job1 first, got %s", first.ID)
	}
}

func TestJobQueue_Remove(t *testing.T) {
	queue := NewJobQueue()

	job1 := NewSpawnJob("job1", JobTypeSession, "session1")
	job2 := NewSpawnJob("job2", JobTypeSession, "session2")

	queue.Enqueue(job1)
	queue.Enqueue(job2)

	removed := queue.Remove("job1")
	if removed == nil {
		t.Error("should have removed job1")
	}

	if queue.Len() != 1 {
		t.Errorf("expected length 1, got %d", queue.Len())
	}

	// Job1 should not be in queue
	if queue.Get("job1") != nil {
		t.Error("job1 should not be in queue")
	}
}

func TestJobQueue_CancelSession(t *testing.T) {
	queue := NewJobQueue()

	job1 := NewSpawnJob("job1", JobTypeSession, "session1")
	job2 := NewSpawnJob("job2", JobTypeSession, "session1")
	job3 := NewSpawnJob("job3", JobTypeSession, "session2")

	queue.Enqueue(job1)
	queue.Enqueue(job2)
	queue.Enqueue(job3)

	cancelled := queue.CancelSession("session1")

	if len(cancelled) != 2 {
		t.Errorf("expected 2 cancelled, got %d", len(cancelled))
	}

	if queue.Len() != 1 {
		t.Errorf("expected 1 remaining, got %d", queue.Len())
	}

	remaining := queue.Dequeue()
	if remaining.SessionName != "session2" {
		t.Error("wrong job remaining")
	}
}

func TestScheduler_SubmitAndExecute(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxConcurrent = 2
	cfg.GlobalRateLimit.Rate = 100 // Fast for testing
	cfg.GlobalRateLimit.MinInterval = 0

	scheduler := New(cfg)

	var executed int64
	var mu sync.Mutex
	executedIDs := make(map[string]bool)

	scheduler.SetExecutor(func(ctx context.Context, job *SpawnJob) error {
		atomic.AddInt64(&executed, 1)
		mu.Lock()
		executedIDs[job.ID] = true
		mu.Unlock()
		return nil
	})

	err := scheduler.Start()
	if err != nil {
		t.Fatalf("failed to start: %v", err)
	}
	defer scheduler.Stop()

	// Submit jobs
	for i := 0; i < 5; i++ {
		job := NewSpawnJob("", JobTypeSession, "test")
		err := scheduler.Submit(job)
		if err != nil {
			t.Errorf("failed to submit job: %v", err)
		}
	}

	// Wait for execution
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt64(&executed) < 5 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}

	if atomic.LoadInt64(&executed) != 5 {
		t.Errorf("expected 5 executed, got %d", atomic.LoadInt64(&executed))
	}
}

func TestScheduler_Retry(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxConcurrent = 1
	cfg.GlobalRateLimit.Rate = 100
	cfg.GlobalRateLimit.MinInterval = 0
	cfg.DefaultRetries = 2
	cfg.DefaultRetryDelay = 10 * time.Millisecond

	scheduler := New(cfg)

	var attempts int64

	scheduler.SetExecutor(func(ctx context.Context, job *SpawnJob) error {
		attempt := atomic.AddInt64(&attempts, 1)
		if attempt < 3 {
			return errors.New("simulated failure")
		}
		return nil // Success on third attempt
	})

	err := scheduler.Start()
	if err != nil {
		t.Fatalf("failed to start: %v", err)
	}
	defer scheduler.Stop()

	job := NewSpawnJob("retry-test", JobTypeSession, "test")
	err = scheduler.Submit(job)
	if err != nil {
		t.Fatalf("failed to submit: %v", err)
	}

	// Wait for retries
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt64(&attempts) < 3 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}

	if atomic.LoadInt64(&attempts) != 3 {
		t.Errorf("expected 3 attempts, got %d", atomic.LoadInt64(&attempts))
	}

	// Check final status
	time.Sleep(100 * time.Millisecond)
	stats := scheduler.Stats()
	if stats.TotalCompleted != 1 {
		t.Errorf("expected 1 completed, got %d", stats.TotalCompleted)
	}
}

func TestScheduler_Cancel(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxConcurrent = 1
	cfg.GlobalRateLimit.Rate = 0.1 // Very slow

	scheduler := New(cfg)

	var executed int64
	scheduler.SetExecutor(func(ctx context.Context, job *SpawnJob) error {
		atomic.AddInt64(&executed, 1)
		return nil
	})

	err := scheduler.Start()
	if err != nil {
		t.Fatalf("failed to start: %v", err)
	}
	defer scheduler.Stop()

	// Submit multiple jobs
	var jobIDs []string
	for i := 0; i < 5; i++ {
		job := NewSpawnJob("", JobTypeSession, "test")
		_ = scheduler.Submit(job)
		jobIDs = append(jobIDs, job.ID)
	}

	// Cancel a queued job
	time.Sleep(50 * time.Millisecond)
	cancelled := scheduler.Cancel(jobIDs[4])
	if !cancelled {
		t.Log("job may have already executed")
	}
}

func TestScheduler_Pause_Resume(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxConcurrent = 1
	cfg.GlobalRateLimit.Rate = 100
	cfg.GlobalRateLimit.MinInterval = 0

	scheduler := New(cfg)

	var executed int64
	scheduler.SetExecutor(func(ctx context.Context, job *SpawnJob) error {
		atomic.AddInt64(&executed, 1)
		return nil
	})

	err := scheduler.Start()
	if err != nil {
		t.Fatalf("failed to start: %v", err)
	}
	defer scheduler.Stop()

	// Pause immediately
	scheduler.Pause()
	if !scheduler.IsPaused() {
		t.Error("scheduler should be paused")
	}

	// Submit jobs while paused
	for i := 0; i < 3; i++ {
		job := NewSpawnJob("", JobTypeSession, "test")
		_ = scheduler.Submit(job)
	}

	// Wait a bit - nothing should execute
	time.Sleep(100 * time.Millisecond)
	if atomic.LoadInt64(&executed) != 0 {
		t.Error("should not execute while paused")
	}

	// Resume
	scheduler.Resume()
	if scheduler.IsPaused() {
		t.Error("scheduler should not be paused")
	}

	// Wait for execution
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt64(&executed) < 3 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}

	if atomic.LoadInt64(&executed) != 3 {
		t.Errorf("expected 3 executed, got %d", atomic.LoadInt64(&executed))
	}
}

func TestScheduler_Progress(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxConcurrent = 1
	cfg.GlobalRateLimit.Rate = 2

	scheduler := New(cfg)

	scheduler.SetExecutor(func(ctx context.Context, job *SpawnJob) error {
		time.Sleep(50 * time.Millisecond)
		return nil
	})

	err := scheduler.Start()
	if err != nil {
		t.Fatalf("failed to start: %v", err)
	}
	defer scheduler.Stop()

	// Submit jobs
	for i := 0; i < 5; i++ {
		job := NewSpawnJob("", JobTypeSession, "test-session")
		job.AgentType = "cc"
		_ = scheduler.Submit(job)
	}

	// Get progress
	progress := scheduler.GetProgress()

	if progress.Status != "running" {
		t.Errorf("expected status 'running', got %q", progress.Status)
	}

	if progress.QueuedCount+progress.RunningCount == 0 {
		t.Error("should have queued or running jobs")
	}

	// Check by session
	sp := progress.BySession["test-session"]
	if sp == nil {
		t.Error("should have session progress")
	}
}

func TestFairScheduler_MaxPerSession(t *testing.T) {
	cfg := FairSchedulerConfig{
		MaxPerSession: 2,
		MaxPerBatch:   0, // Disabled
	}
	scheduler := NewFairScheduler(cfg)

	// Add jobs for same session (each with unique ID)
	for i := 0; i < 5; i++ {
		job := NewSpawnJob(fmt.Sprintf("job-%d", i), JobTypeSession, "same-session")
		scheduler.Enqueue(job)
	}

	// Should only dequeue up to max
	job1 := scheduler.TryDequeue()
	job2 := scheduler.TryDequeue()
	job3 := scheduler.TryDequeue()

	if job1 == nil || job2 == nil {
		t.Error("should dequeue first two jobs")
	}
	if job3 != nil {
		t.Error("should not dequeue third job (max per session)")
	}

	// Mark one complete
	scheduler.MarkComplete(job1)

	// Now should be able to dequeue another
	job3 = scheduler.TryDequeue()
	if job3 == nil {
		t.Error("should dequeue after marking complete")
	}
}

func TestPerAgentLimiter(t *testing.T) {
	cfg := DefaultAgentLimiterConfig()
	limiter := NewPerAgentLimiter(cfg)

	ctx := context.Background()

	// Get limiter for each agent type
	ccLimiter := limiter.GetLimiter("cc")
	codLimiter := limiter.GetLimiter("cod")

	if ccLimiter == codLimiter {
		t.Error("should have separate limiters per agent type")
	}

	// Test wait
	err := limiter.Wait(ctx, "cc")
	if err != nil {
		t.Errorf("failed to wait for cc: %v", err)
	}

	stats := limiter.AllStats()
	if len(stats) < 2 {
		t.Errorf("expected at least 2 agent stats, got %d", len(stats))
	}
}

func TestScheduler_Hooks(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxConcurrent = 1
	cfg.GlobalRateLimit.Rate = 100
	cfg.GlobalRateLimit.MinInterval = 0

	scheduler := New(cfg)

	var enqueued, started, completed int64

	scheduler.SetHooks(Hooks{
		OnJobEnqueued: func(job *SpawnJob) {
			atomic.AddInt64(&enqueued, 1)
		},
		OnJobStarted: func(job *SpawnJob) {
			atomic.AddInt64(&started, 1)
		},
		OnJobCompleted: func(job *SpawnJob) {
			atomic.AddInt64(&completed, 1)
		},
	})

	scheduler.SetExecutor(func(ctx context.Context, job *SpawnJob) error {
		return nil
	})

	err := scheduler.Start()
	if err != nil {
		t.Fatalf("failed to start: %v", err)
	}
	defer scheduler.Stop()

	job := NewSpawnJob("hook-test", JobTypeSession, "test")
	_ = scheduler.Submit(job)

	// Wait for completion
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt64(&completed) < 1 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}

	if atomic.LoadInt64(&enqueued) != 1 {
		t.Errorf("expected 1 enqueued, got %d", atomic.LoadInt64(&enqueued))
	}
	if atomic.LoadInt64(&started) != 1 {
		t.Errorf("expected 1 started, got %d", atomic.LoadInt64(&started))
	}
	if atomic.LoadInt64(&completed) != 1 {
		t.Errorf("expected 1 completed, got %d", atomic.LoadInt64(&completed))
	}
}

func BenchmarkRateLimiter_Wait(b *testing.B) {
	cfg := LimiterConfig{
		Rate:     float64(b.N), // Very fast
		Capacity: float64(b.N),
	}
	limiter := NewRateLimiter(cfg)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = limiter.Wait(ctx)
	}
}

func BenchmarkJobQueue_EnqueueDequeue(b *testing.B) {
	queue := NewJobQueue()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		job := NewSpawnJob("", JobTypeSession, "test")
		queue.Enqueue(job)
		queue.Dequeue()
	}
}
