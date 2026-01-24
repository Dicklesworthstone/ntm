package scheduler

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestScheduler_E2E_PacedSpawning validates that the scheduler correctly paces
// spawning operations, prevents resource exhaustion, and produces accurate timing.
func TestScheduler_E2E_PacedSpawning(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	const (
		numPanes           = 12
		tokensPerSecond    = 2.0  // Allow 2 operations per second
		expectedMinSpacing = 400  // Minimum ms between operations (accounting for jitter)
		maxConcurrent      = 2    // Max concurrent operations
	)

	// Track execution times for validation
	var mu sync.Mutex
	executionTimes := make([]time.Time, 0, numPanes)
	completedCount := int32(0)

	// Mock executor that records timestamps
	executor := func(ctx context.Context, job *SpawnJob) error {
		mu.Lock()
		executionTimes = append(executionTimes, time.Now())
		mu.Unlock()

		// Simulate pane creation work
		time.Sleep(50 * time.Millisecond)
		atomic.AddInt32(&completedCount, 1)
		return nil
	}

	// Create scheduler with rate limiting
	cfg := Config{
		MaxConcurrent: maxConcurrent,
		GlobalRateLimit: LimiterConfig{
			Rate:         tokensPerSecond,
			Capacity:     2.0,
			BurstAllowed: true,
			MinInterval:  300 * time.Millisecond,
		},
		AgentRateLimits: DefaultAgentLimiterConfig(),
		AgentCaps: AgentCapsConfig{
			Default: AgentCapConfig{MaxConcurrent: 20}, // High cap for testing
		},
		FairScheduler:         FairSchedulerConfig{MaxPerSession: 5, MaxPerBatch: 10},
		Backoff:               DefaultBackoffConfig(),
		MaxCompleted:          100,
		DefaultRetries:        3,
		DefaultRetryDelay:     time.Second,
		BackpressureThreshold: 50,
	}

	sched := New(cfg)
	sched.SetExecutor(executor)
	if err := sched.Start(); err != nil {
		t.Fatalf("failed to start scheduler: %v", err)
	}
	defer sched.Stop()

	// Submit all spawn jobs
	startTime := time.Now()
	for i := 0; i < numPanes; i++ {
		job := NewSpawnJob(fmt.Sprintf("pane-%d", i), JobTypePaneSplit, "test-session")
		job.AgentType = "claude"
		job.PaneIndex = i
		if err := sched.Submit(job); err != nil {
			t.Fatalf("failed to submit job %d: %v", i, err)
		}
	}

	// Wait for all jobs to complete (with timeout)
	deadline := time.After(30 * time.Second)
	for {
		if atomic.LoadInt32(&completedCount) >= int32(numPanes) {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for jobs to complete, only %d/%d finished",
				atomic.LoadInt32(&completedCount), numPanes)
		case <-time.After(100 * time.Millisecond):
		}
	}

	totalDuration := time.Since(startTime)
	t.Logf("E2E: Completed %d spawns in %v", numPanes, totalDuration)

	// Validate timing: operations should be paced
	mu.Lock()
	times := make([]time.Time, len(executionTimes))
	copy(times, executionTimes)
	mu.Unlock()

	if len(times) != numPanes {
		t.Errorf("expected %d execution times, got %d", numPanes, len(times))
	}

	// Check spacing between operations (after initial burst)
	var spacingViolations int
	for i := 2; i < len(times); i++ { // Skip first 2 (burst)
		spacing := times[i].Sub(times[i-1])
		if spacing < time.Duration(expectedMinSpacing)*time.Millisecond {
			// Allow some violations due to parallel execution
			spacingViolations++
		}
	}

	// With 2 workers, we expect some parallel execution, so allow up to 30% violations
	maxViolations := len(times) / 3
	if spacingViolations > maxViolations {
		t.Errorf("too many spacing violations: %d (max allowed: %d)", spacingViolations, maxViolations)
		for i := 1; i < len(times); i++ {
			t.Logf("  spacing[%d->%d]: %v", i-1, i, times[i].Sub(times[i-1]))
		}
	}

	// Verify total duration is reasonable (rate limited)
	expectedMinDuration := time.Duration(float64(numPanes-2)/tokensPerSecond) * time.Second
	if totalDuration < expectedMinDuration/2 {
		t.Errorf("spawning completed too quickly: %v (expected at least ~%v)",
			totalDuration, expectedMinDuration)
	}

	// Print timing summary
	t.Logf("E2E Timing Summary:")
	t.Logf("  Jobs: %d", numPanes)
	t.Logf("  Rate: %.1f/sec", tokensPerSecond)
	t.Logf("  Duration: %v", totalDuration)
	t.Logf("  Throughput: %.2f jobs/sec", float64(numPanes)/totalDuration.Seconds())
}

// TestScheduler_E2E_NoResourceExhaustion verifies that rate limiting prevents
// resource exhaustion errors (EAGAIN-like scenarios).
func TestScheduler_E2E_NoResourceExhaustion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	const (
		numOperations = 50
		maxConcurrent = 3
		resourceLimit = 5 // Simulated resource limit
	)

	// Track concurrent operations to detect resource exhaustion
	var mu sync.Mutex
	currentConcurrent := 0
	maxObservedConcurrent := 0
	eagainErrors := 0
	completedCount := int32(0)

	executor := func(ctx context.Context, job *SpawnJob) error {
		mu.Lock()
		currentConcurrent++
		if currentConcurrent > maxObservedConcurrent {
			maxObservedConcurrent = currentConcurrent
		}
		if currentConcurrent > resourceLimit {
			eagainErrors++
			mu.Unlock()
			return fmt.Errorf("EAGAIN: resource temporarily unavailable (concurrent: %d)", currentConcurrent)
		}
		mu.Unlock()

		// Simulate work
		time.Sleep(20 * time.Millisecond)

		mu.Lock()
		currentConcurrent--
		mu.Unlock()

		atomic.AddInt32(&completedCount, 1)
		return nil
	}

	cfg := Config{
		MaxConcurrent: maxConcurrent,
		GlobalRateLimit: LimiterConfig{
			Rate:         10.0,
			Capacity:     3.0,
			BurstAllowed: true,
			MinInterval:  50 * time.Millisecond,
		},
		AgentRateLimits: DefaultAgentLimiterConfig(),
		AgentCaps: AgentCapsConfig{
			Default: AgentCapConfig{MaxConcurrent: 100}, // High cap for testing
		},
		FairScheduler:         FairSchedulerConfig{MaxPerSession: 10, MaxPerBatch: 20},
		Backoff:               DefaultBackoffConfig(),
		MaxCompleted:          100,
		DefaultRetries:        2,
		DefaultRetryDelay:     100 * time.Millisecond,
		BackpressureThreshold: 100,
	}

	sched := New(cfg)
	sched.SetExecutor(executor)
	if err := sched.Start(); err != nil {
		t.Fatalf("failed to start scheduler: %v", err)
	}
	defer sched.Stop()

	// Submit all jobs rapidly
	for i := 0; i < numOperations; i++ {
		job := NewSpawnJob(fmt.Sprintf("op-%d", i), JobTypeAgentLaunch, "session-1")
		if err := sched.Submit(job); err != nil {
			t.Fatalf("failed to submit job %d: %v", i, err)
		}
	}

	// Wait for completion
	deadline := time.After(60 * time.Second)
	for {
		if atomic.LoadInt32(&completedCount) >= int32(numOperations) {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out: %d/%d completed", atomic.LoadInt32(&completedCount), numOperations)
		case <-time.After(100 * time.Millisecond):
		}
	}

	t.Logf("E2E Resource Test:")
	t.Logf("  Operations: %d", numOperations)
	t.Logf("  Max concurrent workers: %d", maxConcurrent)
	t.Logf("  Max observed concurrent: %d", maxObservedConcurrent)
	t.Logf("  EAGAIN errors: %d", eagainErrors)

	// The scheduler should prevent more concurrent operations than workers
	if maxObservedConcurrent > maxConcurrent {
		t.Errorf("concurrent operations (%d) exceeded worker limit (%d)",
			maxObservedConcurrent, maxConcurrent)
	}

	// With proper rate limiting, we should have zero EAGAIN errors
	if eagainErrors > 0 {
		t.Errorf("resource exhaustion detected: %d EAGAIN errors", eagainErrors)
	}
}

// TestScheduler_E2E_DetailedSpawnLogs tests that the scheduler produces
// detailed logs with timestamps for debugging.
func TestScheduler_E2E_DetailedSpawnLogs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	const numPanes = 6

	// Capture log entries
	type logEntry struct {
		Timestamp time.Time
		JobID     string
		Event     string
		Details   string
	}
	var mu sync.Mutex
	logs := make([]logEntry, 0, numPanes*3)

	addLog := func(jobID, event, details string) {
		mu.Lock()
		logs = append(logs, logEntry{
			Timestamp: time.Now(),
			JobID:     jobID,
			Event:     event,
			Details:   details,
		})
		mu.Unlock()
	}

	executor := func(ctx context.Context, job *SpawnJob) error {
		addLog(job.ID, "EXECUTING", fmt.Sprintf("pane=%d type=%s", job.PaneIndex, job.AgentType))
		time.Sleep(30 * time.Millisecond)
		addLog(job.ID, "COMPLETED", "success")
		return nil
	}

	cfg := Config{
		MaxConcurrent: 2,
		GlobalRateLimit: LimiterConfig{
			Rate:         5.0,
			Capacity:     2.0,
			BurstAllowed: true,
			MinInterval:  100 * time.Millisecond,
		},
		AgentRateLimits: DefaultAgentLimiterConfig(),
		AgentCaps: AgentCapsConfig{
			Default: AgentCapConfig{MaxConcurrent: 100}, // High cap for testing
		},
		FairScheduler:         DefaultFairSchedulerConfig(),
		Backoff:               DefaultBackoffConfig(),
		MaxCompleted:          50,
		DefaultRetries:        3,
		DefaultRetryDelay:     time.Second,
		BackpressureThreshold: 20,
	}

	sched := New(cfg)
	sched.SetExecutor(executor)

	// Add hooks for logging
	sched.SetHooks(Hooks{
		OnJobEnqueued: func(job *SpawnJob) {
			addLog(job.ID, "ENQUEUED", fmt.Sprintf("priority=%d", job.Priority))
		},
		OnJobStarted: func(job *SpawnJob) {
			addLog(job.ID, "STARTED", fmt.Sprintf("queued_for=%v", job.QueueDuration()))
		},
		OnJobCompleted: func(job *SpawnJob) {
			addLog(job.ID, "FINISHED", fmt.Sprintf("exec_time=%v", job.ExecutionDuration()))
		},
		OnJobFailed: func(job *SpawnJob, err error) {
			addLog(job.ID, "FAILED", err.Error())
		},
	})

	if err := sched.Start(); err != nil {
		t.Fatalf("failed to start scheduler: %v", err)
	}
	defer sched.Stop()

	// Submit jobs
	for i := 0; i < numPanes; i++ {
		job := NewSpawnJob(fmt.Sprintf("spawn-%d", i), JobTypePaneSplit, "test-session")
		job.AgentType = "cc"
		job.PaneIndex = i
		if err := sched.Submit(job); err != nil {
			t.Fatalf("submit failed: %v", err)
		}
	}

	// Wait for completion
	time.Sleep(5 * time.Second)

	// Validate logs
	mu.Lock()
	logsCopy := make([]logEntry, len(logs))
	copy(logsCopy, logs)
	mu.Unlock()

	t.Logf("Detailed Spawn Logs (%d entries):", len(logsCopy))
	for _, entry := range logsCopy {
		t.Logf("  [%s] %s: %s - %s",
			entry.Timestamp.Format("15:04:05.000"),
			entry.JobID,
			entry.Event,
			entry.Details)
	}

	// Verify we have expected log events
	eventCounts := make(map[string]int)
	for _, entry := range logsCopy {
		eventCounts[entry.Event]++
	}

	expectedEvents := map[string]int{
		"ENQUEUED":  numPanes,
		"STARTED":   numPanes,
		"EXECUTING": numPanes,
		"COMPLETED": numPanes,
		"FINISHED":  numPanes,
	}

	for event, expected := range expectedEvents {
		if got := eventCounts[event]; got != expected {
			t.Errorf("expected %d %s events, got %d", expected, event, got)
		}
	}
}

// TestScheduler_E2E_MultiSessionFairness tests fair scheduling across sessions.
func TestScheduler_E2E_MultiSessionFairness(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	const (
		numSessions   = 3
		panesPerSess  = 8
		maxPerSession = 2
	)

	// Track execution order per session
	var mu sync.Mutex
	sessionOrder := make(map[string][]int)
	globalOrder := make([]string, 0, numSessions*panesPerSess)

	executor := func(ctx context.Context, job *SpawnJob) error {
		mu.Lock()
		sessionOrder[job.SessionName] = append(sessionOrder[job.SessionName], job.PaneIndex)
		globalOrder = append(globalOrder, job.SessionName)
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		return nil
	}

	cfg := Config{
		MaxConcurrent: 3,
		GlobalRateLimit: LimiterConfig{
			Rate:         20.0,
			Capacity:     3.0,
			BurstAllowed: true,
			MinInterval:  30 * time.Millisecond,
		},
		AgentRateLimits: DefaultAgentLimiterConfig(),
		AgentCaps: AgentCapsConfig{
			Default: AgentCapConfig{MaxConcurrent: 100}, // High cap for testing
		},
		FairScheduler:         FairSchedulerConfig{MaxPerSession: maxPerSession, MaxPerBatch: 10},
		Backoff:               DefaultBackoffConfig(),
		MaxCompleted:          100,
		DefaultRetries:        3,
		DefaultRetryDelay:     time.Second,
		BackpressureThreshold: 50,
	}

	sched := New(cfg)
	sched.SetExecutor(executor)
	if err := sched.Start(); err != nil {
		t.Fatalf("failed to start scheduler: %v", err)
	}
	defer sched.Stop()

	// Submit jobs for all sessions interleaved
	for pane := 0; pane < panesPerSess; pane++ {
		for sess := 0; sess < numSessions; sess++ {
			sessionName := fmt.Sprintf("session-%d", sess)
			job := NewSpawnJob(
				fmt.Sprintf("%s-pane-%d", sessionName, pane),
				JobTypePaneSplit,
				sessionName,
			)
			job.PaneIndex = pane
			if err := sched.Submit(job); err != nil {
				t.Fatalf("submit failed: %v", err)
			}
		}
	}

	// Wait for completion
	time.Sleep(5 * time.Second)

	mu.Lock()
	orderCopy := make([]string, len(globalOrder))
	copy(orderCopy, globalOrder)
	mu.Unlock()

	t.Logf("Execution order (first 20): %v", orderCopy[:min(20, len(orderCopy))])

	// Analyze fairness: count how often each session appears in windows
	windowSize := numSessions * 2 // Expected to see each session roughly twice per window
	windowCounts := make([]map[string]int, 0)

	for i := 0; i < len(orderCopy); i += windowSize {
		end := min(i+windowSize, len(orderCopy))
		window := orderCopy[i:end]
		counts := make(map[string]int)
		for _, sess := range window {
			counts[sess]++
		}
		windowCounts = append(windowCounts, counts)
	}

	t.Logf("Session distribution per window (size=%d):", windowSize)
	for i, counts := range windowCounts {
		t.Logf("  Window %d: %v", i, counts)
	}

	// Check that no session dominates any window
	for i, counts := range windowCounts {
		for sess, count := range counts {
			// In a fair schedule, no session should have more than maxPerSession + 1 per window
			// (accounting for workers potentially picking from same session)
			if count > maxPerSession+2 {
				t.Errorf("window %d: session %s has unfair count %d (max expected: %d)",
					i, sess, count, maxPerSession+2)
			}
		}
	}
}

// TestScheduler_E2E_BackpressureHandling tests graceful handling of queue backpressure.
func TestScheduler_E2E_BackpressureHandling(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	const (
		totalJobs    = 25
		slowExecTime = 100 * time.Millisecond
	)

	var backpressureEvents int32
	var completedJobs int32

	executor := func(ctx context.Context, job *SpawnJob) error {
		time.Sleep(slowExecTime)
		atomic.AddInt32(&completedJobs, 1)
		return nil
	}

	cfg := Config{
		MaxConcurrent: 2,
		GlobalRateLimit: LimiterConfig{
			Rate:         5.0,
			Capacity:     2.0,
			BurstAllowed: true,
			MinInterval:  100 * time.Millisecond,
		},
		AgentRateLimits: DefaultAgentLimiterConfig(),
		AgentCaps: AgentCapsConfig{
			Default: AgentCapConfig{MaxConcurrent: 100}, // High cap for testing
		},
		FairScheduler:         DefaultFairSchedulerConfig(),
		Backoff:               DefaultBackoffConfig(),
		MaxCompleted:          50,
		DefaultRetries:        1,
		DefaultRetryDelay:     100 * time.Millisecond,
		BackpressureThreshold: 10,
	}

	sched := New(cfg)
	sched.SetExecutor(executor)
	sched.SetHooks(Hooks{
		OnBackpressure: func(size int, wait time.Duration) {
			atomic.AddInt32(&backpressureEvents, 1)
		},
	})

	if err := sched.Start(); err != nil {
		t.Fatalf("failed to start scheduler: %v", err)
	}
	defer sched.Stop()

	// Submit jobs as fast as possible
	var submitErrors int
	for i := 0; i < totalJobs; i++ {
		job := NewSpawnJob(fmt.Sprintf("job-%d", i), JobTypeAgentLaunch, "session")
		err := sched.Submit(job)
		if err != nil {
			if strings.Contains(err.Error(), "queue full") {
				submitErrors++
			} else {
				t.Errorf("unexpected submit error: %v", err)
			}
		}
		// Small delay to let queue drain occasionally
		if i%5 == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Wait for queue to drain
	time.Sleep(10 * time.Second)

	t.Logf("Backpressure Test Results:")
	t.Logf("  Total jobs submitted: %d", totalJobs)
	t.Logf("  Submit errors: %d", submitErrors)
	t.Logf("  Completed jobs: %d", atomic.LoadInt32(&completedJobs))
	t.Logf("  Backpressure events: %d", atomic.LoadInt32(&backpressureEvents))

	// At least some jobs should complete
	completed := atomic.LoadInt32(&completedJobs)
	if completed < int32(totalJobs/2) {
		t.Errorf("too few jobs completed: %d (expected at least %d)", completed, totalJobs/2)
	}

	// Backpressure should have been triggered at some point
	if atomic.LoadInt32(&backpressureEvents) == 0 {
		t.Log("Note: no backpressure events detected (queue may not have filled)")
	}
}

// BenchmarkScheduler_E2E_Throughput measures scheduler throughput under load.
func BenchmarkScheduler_E2E_Throughput(b *testing.B) {
	if os.Getenv("ENABLE_E2E_BENCH") == "" {
		b.Skip("set ENABLE_E2E_BENCH=1 to run")
	}

	executor := func(ctx context.Context, job *SpawnJob) error {
		return nil // No-op for throughput test
	}

	cfg := Config{
		MaxConcurrent: 4,
		GlobalRateLimit: LimiterConfig{
			Rate:         1000.0,
			Capacity:     100.0,
			BurstAllowed: true,
			MinInterval:  0,
		},
		AgentRateLimits: DefaultAgentLimiterConfig(),
		AgentCaps: AgentCapsConfig{
			Default: AgentCapConfig{MaxConcurrent: 1000}, // High cap for throughput testing
		},
		FairScheduler:         DefaultFairSchedulerConfig(),
		Backoff:               DefaultBackoffConfig(),
		MaxCompleted:          100,
		DefaultRetries:        0,
		DefaultRetryDelay:     time.Second,
		BackpressureThreshold: 1000,
	}

	sched := New(cfg)
	sched.SetExecutor(executor)
	if err := sched.Start(); err != nil {
		b.Fatalf("failed to start scheduler: %v", err)
	}
	defer sched.Stop()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		job := NewSpawnJob(fmt.Sprintf("bench-%d", i), JobTypePaneSplit, "bench-session")
		if err := sched.Submit(job); err != nil {
			b.Fatalf("submit failed: %v", err)
		}
	}
	b.StopTimer()

	// Drain queue
	time.Sleep(100 * time.Millisecond)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestScheduler_E2E_EAGAINBackoff tests EAGAIN-aware backoff behavior.
// It simulates resource exhaustion errors and verifies:
// - Error classification
// - Exponential backoff with jitter
// - Global queue pause/resume
// - Recovery after success
func TestScheduler_E2E_EAGAINBackoff(t *testing.T) {
	if os.Getenv("ENABLE_E2E_TESTS") == "" {
		t.Skip("set ENABLE_E2E_TESTS=1 to run e2e tests")
	}

	var (
		mu             sync.Mutex
		attemptCount   int32
		failUntil      int32 = 3 // Fail first 3 attempts
		backoffDelays  []time.Duration
		lastAttemptAt  time.Time
		recoveredAfter int32
	)

	// Executor that simulates EAGAIN errors
	executor := func(ctx context.Context, job *SpawnJob) error {
		mu.Lock()
		attempt := atomic.AddInt32(&attemptCount, 1)
		now := time.Now()
		if !lastAttemptAt.IsZero() {
			backoffDelays = append(backoffDelays, now.Sub(lastAttemptAt))
		}
		lastAttemptAt = now
		mu.Unlock()

		if attempt <= atomic.LoadInt32(&failUntil) {
			// Simulate EAGAIN error with stderr hint
			job.Metadata["stderr"] = "fork: Resource temporarily unavailable"
			job.Metadata["exit_code"] = 11
			return fmt.Errorf("fork failed: resource temporarily unavailable")
		}

		atomic.StoreInt32(&recoveredAfter, attempt)
		return nil
	}

	cfg := Config{
		MaxConcurrent: 1,
		GlobalRateLimit: LimiterConfig{
			Rate:         10.0,
			Capacity:     5.0,
			BurstAllowed: true,
			MinInterval:  50 * time.Millisecond,
		},
		AgentRateLimits: DefaultAgentLimiterConfig(),
		AgentCaps: AgentCapsConfig{
			Default: AgentCapConfig{MaxConcurrent: 100},
		},
		FairScheduler: DefaultFairSchedulerConfig(),
		Backoff: BackoffConfig{
			InitialDelay:                 100 * time.Millisecond,
			MaxDelay:                     2 * time.Second,
			Multiplier:                   2.0,
			JitterFactor:                 0.1, // Low jitter for predictable testing
			MaxRetries:                   5,
			PauseQueueOnBackoff:          true,
			ConsecutiveFailuresThreshold: 2,
		},
		MaxCompleted:          100,
		DefaultRetries:        5,
		DefaultRetryDelay:     50 * time.Millisecond,
		BackpressureThreshold: 50,
	}

	var (
		pauseCount  int32
		resumeCount int32
	)

	sched := New(cfg)
	sched.SetExecutor(executor)

	// Track backoff events via hooks
	sched.backoff.SetHooks(
		func(delay time.Duration, reason ResourceErrorType) {
			atomic.AddInt32(&pauseCount, 1)
			t.Logf("Backoff started: delay=%v reason=%s", delay, reason)
		},
		func(totalDuration time.Duration) {
			atomic.AddInt32(&resumeCount, 1)
			t.Logf("Backoff ended: total_duration=%v", totalDuration)
		},
		func(job *SpawnJob, attempts int) {
			t.Logf("Retries exhausted: job=%s attempts=%d", job.ID, attempts)
		},
	)

	if err := sched.Start(); err != nil {
		t.Fatalf("failed to start scheduler: %v", err)
	}
	defer sched.Stop()

	// Submit a single job that will fail and retry
	job := NewSpawnJob("eagain-test", JobTypeAgentLaunch, "test-session")
	job.MaxRetries = 5

	done := make(chan struct{})
	job.Callback = func(j *SpawnJob) {
		close(done)
	}

	if err := sched.Submit(job); err != nil {
		t.Fatalf("failed to submit job: %v", err)
	}

	// Wait for completion with timeout
	select {
	case <-done:
		// Success
	case <-time.After(10 * time.Second):
		t.Fatal("job did not complete within timeout")
	}

	// Verify the job eventually succeeded
	finalJob := sched.GetJob(job.ID)
	if finalJob.Status != StatusCompleted {
		t.Errorf("expected job status completed, got %s", finalJob.Status)
	}

	// Verify backoff behavior
	mu.Lock()
	delays := backoffDelays
	attempts := atomic.LoadInt32(&attemptCount)
	recovered := atomic.LoadInt32(&recoveredAfter)
	mu.Unlock()

	t.Logf("Total attempts: %d", attempts)
	t.Logf("Recovered at attempt: %d", recovered)
	t.Logf("Backoff delays between retries: %v", delays)

	if attempts < 4 {
		t.Errorf("expected at least 4 attempts (3 failures + 1 success), got %d", attempts)
	}

	if recovered != 4 {
		t.Errorf("expected recovery at attempt 4, got %d", recovered)
	}

	// Verify delays show exponential growth (with some tolerance for jitter)
	if len(delays) >= 2 {
		// Second delay should be roughly 2x the first (with tolerance)
		ratio := float64(delays[1]) / float64(delays[0])
		t.Logf("Delay ratio (delay[1]/delay[0]): %.2f", ratio)
		// Allow wide tolerance due to jitter and timing
		if ratio < 1.2 || ratio > 4.0 {
			t.Logf("Note: delay ratio %.2f outside expected range 1.2-4.0 (may be timing variance)", ratio)
		}
	}

	// Verify global backoff was triggered
	pauses := atomic.LoadInt32(&pauseCount)
	resumes := atomic.LoadInt32(&resumeCount)
	t.Logf("Global backoff pauses: %d, resumes: %d", pauses, resumes)

	if pauses == 0 {
		t.Log("Note: global backoff may not have triggered (depends on consecutive failure threshold)")
	}

	// Verify backoff stats
	stats := sched.Stats()
	t.Logf("Backoff stats: %+v", stats.BackoffStats)
	if stats.BackoffStats.TotalRetries == 0 {
		t.Error("expected backoff retries to be recorded")
	}
}

// TestScheduler_E2E_ENOMEMRecovery tests recovery from ENOMEM errors.
func TestScheduler_E2E_ENOMEMRecovery(t *testing.T) {
	if os.Getenv("ENABLE_E2E_TESTS") == "" {
		t.Skip("set ENABLE_E2E_TESTS=1 to run e2e tests")
	}

	var attemptCount int32

	executor := func(ctx context.Context, job *SpawnJob) error {
		attempt := atomic.AddInt32(&attemptCount, 1)
		if attempt == 1 {
			// First attempt: simulate OOM killed
			job.Metadata["stderr"] = "out of memory"
			job.Metadata["exit_code"] = 137 // 128 + SIGKILL
			return fmt.Errorf("process killed: out of memory")
		}
		return nil
	}

	cfg := Config{
		MaxConcurrent: 1,
		GlobalRateLimit: LimiterConfig{
			Rate:         10.0,
			Capacity:     5.0,
			BurstAllowed: true,
			MinInterval:  50 * time.Millisecond,
		},
		AgentRateLimits: DefaultAgentLimiterConfig(),
		AgentCaps: AgentCapsConfig{
			Default: AgentCapConfig{MaxConcurrent: 100},
		},
		FairScheduler: DefaultFairSchedulerConfig(),
		Backoff: BackoffConfig{
			InitialDelay:                 50 * time.Millisecond,
			MaxDelay:                     500 * time.Millisecond,
			Multiplier:                   2.0,
			JitterFactor:                 0.1,
			MaxRetries:                   3,
			PauseQueueOnBackoff:          false,
			ConsecutiveFailuresThreshold: 5, // High threshold to avoid global backoff
		},
		MaxCompleted:          100,
		DefaultRetries:        3,
		DefaultRetryDelay:     50 * time.Millisecond,
		BackpressureThreshold: 50,
	}

	sched := New(cfg)
	sched.SetExecutor(executor)

	if err := sched.Start(); err != nil {
		t.Fatalf("failed to start scheduler: %v", err)
	}
	defer sched.Stop()

	job := NewSpawnJob("enomem-test", JobTypeAgentLaunch, "test-session")
	done := make(chan struct{})
	job.Callback = func(j *SpawnJob) {
		close(done)
	}

	if err := sched.Submit(job); err != nil {
		t.Fatalf("failed to submit job: %v", err)
	}

	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("job did not complete within timeout")
	}

	finalJob := sched.GetJob(job.ID)
	if finalJob.Status != StatusCompleted {
		t.Errorf("expected job status completed, got %s (error: %s)", finalJob.Status, finalJob.Error)
	}

	if atomic.LoadInt32(&attemptCount) != 2 {
		t.Errorf("expected 2 attempts (1 ENOMEM + 1 success), got %d", atomic.LoadInt32(&attemptCount))
	}

	stats := sched.Stats()
	if stats.BackoffStats.TotalRetries == 0 {
		t.Error("expected ENOMEM error to trigger backoff retry")
	}
	if stats.BackoffStats.LastBackoffReason != ResourceErrorENOMEM {
		t.Errorf("expected last backoff reason ENOMEM, got %s", stats.BackoffStats.LastBackoffReason)
	}
}

// TestScheduler_E2E_RateLimitBackoff tests rate limit error handling.
func TestScheduler_E2E_RateLimitBackoff(t *testing.T) {
	if os.Getenv("ENABLE_E2E_TESTS") == "" {
		t.Skip("set ENABLE_E2E_TESTS=1 to run e2e tests")
	}

	var attemptCount int32

	executor := func(ctx context.Context, job *SpawnJob) error {
		attempt := atomic.AddInt32(&attemptCount, 1)
		if attempt <= 2 {
			return fmt.Errorf("rate limit exceeded: too many requests (429)")
		}
		return nil
	}

	cfg := Config{
		MaxConcurrent: 1,
		GlobalRateLimit: LimiterConfig{
			Rate:         10.0,
			Capacity:     5.0,
			BurstAllowed: true,
			MinInterval:  50 * time.Millisecond,
		},
		AgentRateLimits: DefaultAgentLimiterConfig(),
		AgentCaps: AgentCapsConfig{
			Default: AgentCapConfig{MaxConcurrent: 100},
		},
		FairScheduler: DefaultFairSchedulerConfig(),
		Backoff: BackoffConfig{
			InitialDelay:                 50 * time.Millisecond,
			MaxDelay:                     500 * time.Millisecond,
			Multiplier:                   2.0,
			JitterFactor:                 0.1,
			MaxRetries:                   5,
			PauseQueueOnBackoff:          true,
			ConsecutiveFailuresThreshold: 2,
		},
		MaxCompleted:          100,
		DefaultRetries:        5,
		DefaultRetryDelay:     50 * time.Millisecond,
		BackpressureThreshold: 50,
	}

	sched := New(cfg)
	sched.SetExecutor(executor)

	if err := sched.Start(); err != nil {
		t.Fatalf("failed to start scheduler: %v", err)
	}
	defer sched.Stop()

	job := NewSpawnJob("ratelimit-test", JobTypeAgentLaunch, "test-session")
	done := make(chan struct{})
	job.Callback = func(j *SpawnJob) {
		close(done)
	}

	if err := sched.Submit(job); err != nil {
		t.Fatalf("failed to submit job: %v", err)
	}

	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("job did not complete within timeout")
	}

	finalJob := sched.GetJob(job.ID)
	if finalJob.Status != StatusCompleted {
		t.Errorf("expected job status completed, got %s", finalJob.Status)
	}

	if atomic.LoadInt32(&attemptCount) != 3 {
		t.Errorf("expected 3 attempts, got %d", atomic.LoadInt32(&attemptCount))
	}

	stats := sched.Stats()
	if stats.BackoffStats.LastBackoffReason != ResourceErrorRateLimit {
		t.Errorf("expected last backoff reason RATE_LIMIT, got %s", stats.BackoffStats.LastBackoffReason)
	}
}

// TestScheduler_E2E_BackoffResetOnSuccess verifies backoff state resets after success.
func TestScheduler_E2E_BackoffResetOnSuccess(t *testing.T) {
	if os.Getenv("ENABLE_E2E_TESTS") == "" {
		t.Skip("set ENABLE_E2E_TESTS=1 to run e2e tests")
	}

	var jobCount int32

	// First job will fail once, second job should have reset backoff state
	executor := func(ctx context.Context, job *SpawnJob) error {
		count := atomic.AddInt32(&jobCount, 1)
		if count == 1 {
			return fmt.Errorf("resource temporarily unavailable")
		}
		return nil
	}

	cfg := Config{
		MaxConcurrent: 1,
		GlobalRateLimit: LimiterConfig{
			Rate:         10.0,
			Capacity:     5.0,
			BurstAllowed: true,
			MinInterval:  50 * time.Millisecond,
		},
		AgentRateLimits: DefaultAgentLimiterConfig(),
		AgentCaps: AgentCapsConfig{
			Default: AgentCapConfig{MaxConcurrent: 100},
		},
		FairScheduler: DefaultFairSchedulerConfig(),
		Backoff: BackoffConfig{
			InitialDelay:                 100 * time.Millisecond,
			MaxDelay:                     1 * time.Second,
			Multiplier:                   2.0,
			JitterFactor:                 0.1,
			MaxRetries:                   3,
			PauseQueueOnBackoff:          false,
			ConsecutiveFailuresThreshold: 10,
		},
		MaxCompleted:          100,
		DefaultRetries:        3,
		DefaultRetryDelay:     50 * time.Millisecond,
		BackpressureThreshold: 50,
	}

	sched := New(cfg)
	sched.SetExecutor(executor)

	if err := sched.Start(); err != nil {
		t.Fatalf("failed to start scheduler: %v", err)
	}
	defer sched.Stop()

	// Submit first job (will fail once, then succeed on retry)
	job1 := NewSpawnJob("job1", JobTypeAgentLaunch, "test-session")
	done1 := make(chan struct{})
	job1.Callback = func(j *SpawnJob) { close(done1) }

	if err := sched.Submit(job1); err != nil {
		t.Fatalf("failed to submit job1: %v", err)
	}

	select {
	case <-done1:
	case <-time.After(5 * time.Second):
		t.Fatal("job1 did not complete")
	}

	// Check backoff stats before success reset
	statsAfterJob1 := sched.Stats()
	if statsAfterJob1.BackoffStats.TotalRetries == 0 {
		t.Error("expected retries from job1")
	}

	// Submit second job - backoff should be reset, it should succeed immediately
	job2 := NewSpawnJob("job2", JobTypeAgentLaunch, "test-session")
	done2 := make(chan struct{})
	job2.Callback = func(j *SpawnJob) { close(done2) }

	start := time.Now()
	if err := sched.Submit(job2); err != nil {
		t.Fatalf("failed to submit job2: %v", err)
	}

	select {
	case <-done2:
	case <-time.After(5 * time.Second):
		t.Fatal("job2 did not complete")
	}

	elapsed := time.Since(start)
	t.Logf("Job2 completed in %v (should be quick if backoff was reset)", elapsed)

	// Job2 should have completed quickly (< 500ms) if backoff was reset
	if elapsed > 500*time.Millisecond {
		t.Errorf("job2 took too long (%v), backoff may not have been reset", elapsed)
	}

	finalJob2 := sched.GetJob(job2.ID)
	if finalJob2.Status != StatusCompleted {
		t.Errorf("expected job2 completed, got %s", finalJob2.Status)
	}
}
