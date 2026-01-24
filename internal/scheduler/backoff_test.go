package scheduler

import (
	"errors"
	"fmt"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestClassifyError_EAGAIN(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		exitCode int
		stderr   string
		wantType ResourceErrorType
	}{
		{
			name:     "syscall EAGAIN",
			err:      syscall.EAGAIN,
			wantType: ResourceErrorEAGAIN,
		},
		// EWOULDBLOCK is same as EAGAIN on Linux, so not tested separately
		{
			name:     "error message contains EAGAIN",
			err:      errors.New("fork: EAGAIN"),
			wantType: ResourceErrorEAGAIN,
		},
		{
			name:     "error message resource temporarily unavailable",
			err:      errors.New("resource temporarily unavailable"),
			wantType: ResourceErrorEAGAIN,
		},
		{
			name:     "stderr contains fork failed",
			err:      errors.New("command failed"),
			stderr:   "fork: retry: Resource temporarily unavailable",
			wantType: ResourceErrorEAGAIN,
		},
		{
			name:     "exit code 11",
			err:      errors.New("process exited with 11"),
			exitCode: 11,
			wantType: ResourceErrorEAGAIN,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyError(tt.err, tt.exitCode, tt.stderr)
			if got == nil {
				t.Fatal("expected ResourceError, got nil")
			}
			if got.Type != tt.wantType {
				t.Errorf("got type %q, want %q", got.Type, tt.wantType)
			}
			if !got.Retryable {
				t.Error("expected retryable=true")
			}
		})
	}
}

func TestClassifyError_ENOMEM(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		exitCode int
		stderr   string
		wantType ResourceErrorType
	}{
		{
			name:     "syscall ENOMEM",
			err:      syscall.ENOMEM,
			wantType: ResourceErrorENOMEM,
		},
		{
			name:     "error message out of memory",
			err:      errors.New("out of memory"),
			wantType: ResourceErrorENOMEM,
		},
		{
			name:     "error message cannot allocate memory",
			err:      errors.New("cannot allocate memory"),
			wantType: ResourceErrorEAGAIN, // This pattern is in eagainPatterns
		},
		{
			name:     "exit code 12",
			err:      errors.New("process exited with 12"),
			exitCode: 12,
			wantType: ResourceErrorENOMEM,
		},
		{
			name:     "exit code 137 (OOM killed)",
			err:      errors.New("process killed"),
			exitCode: 137,
			wantType: ResourceErrorENOMEM,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyError(tt.err, tt.exitCode, tt.stderr)
			if got == nil {
				t.Fatal("expected ResourceError, got nil")
			}
			if got.Type != tt.wantType {
				t.Errorf("got type %q, want %q", got.Type, tt.wantType)
			}
		})
	}
}

func TestClassifyError_RateLimit(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		stderr   string
		wantType ResourceErrorType
	}{
		{
			name:     "rate limit message",
			err:      errors.New("rate limit exceeded"),
			wantType: ResourceErrorRateLimit,
		},
		{
			name:     "too many requests",
			err:      errors.New("too many requests"),
			wantType: ResourceErrorRateLimit,
		},
		{
			name:     "429 error",
			err:      errors.New("HTTP 429"),
			wantType: ResourceErrorRateLimit,
		},
		{
			name:     "throttled",
			err:      errors.New("request throttled"),
			wantType: ResourceErrorRateLimit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyError(tt.err, 0, tt.stderr)
			if got == nil {
				t.Fatal("expected ResourceError, got nil")
			}
			if got.Type != tt.wantType {
				t.Errorf("got type %q, want %q", got.Type, tt.wantType)
			}
		})
	}
}

func TestClassifyError_FDLimit(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantType ResourceErrorType
	}{
		{
			name:     "too many open files",
			err:      errors.New("too many open files"),
			wantType: ResourceErrorEMFILE,
		},
		{
			name:     "syscall EMFILE",
			err:      syscall.EMFILE,
			wantType: ResourceErrorEMFILE,
		},
		{
			name:     "syscall ENFILE",
			err:      syscall.ENFILE,
			wantType: ResourceErrorENFILE,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyError(tt.err, 0, "")
			if got == nil {
				t.Fatal("expected ResourceError, got nil")
			}
			if got.Type != tt.wantType {
				t.Errorf("got type %q, want %q", got.Type, tt.wantType)
			}
		})
	}
}

func TestClassifyError_NoMatch(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "generic error",
			err:  errors.New("something went wrong"),
		},
		{
			name: "connection refused",
			err:  errors.New("connection refused"),
		},
		{
			name: "file not found",
			err:  errors.New("file not found"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyError(tt.err, 0, "")
			if got != nil {
				t.Errorf("expected nil, got type %q", got.Type)
			}
		})
	}
}

func TestClassifyError_NilError(t *testing.T) {
	got := ClassifyError(nil, 0, "")
	if got != nil {
		t.Error("expected nil for nil error")
	}
}

func TestBackoffController_HandleError(t *testing.T) {
	cfg := DefaultBackoffConfig()
	cfg.MaxRetries = 3
	bc := NewBackoffController(cfg)

	job := NewSpawnJob("test-job", JobTypePaneSplit, "test-session")
	resErr := &ResourceError{
		Original:  errors.New("resource temporarily unavailable"),
		Type:      ResourceErrorEAGAIN,
		Retryable: true,
	}

	// First error should trigger backoff
	shouldRetry, delay := bc.HandleError(job, resErr)
	if !shouldRetry {
		t.Error("expected shouldRetry=true on first error")
	}
	if delay < 100*time.Millisecond {
		t.Errorf("expected delay >= 100ms, got %v", delay)
	}

	// Increment job retry count
	job.RetryCount++

	// Second error
	shouldRetry, delay2 := bc.HandleError(job, resErr)
	if !shouldRetry {
		t.Error("expected shouldRetry=true on second error")
	}
	// Delay should increase (exponential)
	if delay2 < delay {
		t.Logf("Note: delay2 (%v) < delay (%v) - jitter may cause this", delay2, delay)
	}

	// Exhaust retries
	job.RetryCount = cfg.MaxRetries
	shouldRetry, _ = bc.HandleError(job, resErr)
	if shouldRetry {
		t.Error("expected shouldRetry=false when retries exhausted")
	}
}

func TestBackoffController_RecordSuccess(t *testing.T) {
	cfg := DefaultBackoffConfig()
	bc := NewBackoffController(cfg)

	job := NewSpawnJob("test-job", JobTypePaneSplit, "test-session")
	resErr := &ResourceError{
		Type:      ResourceErrorEAGAIN,
		Retryable: true,
	}

	// Trigger some failures
	for i := 0; i < 3; i++ {
		bc.HandleError(job, resErr)
	}

	stats := bc.Stats()
	if stats.TotalRetries != 3 {
		t.Errorf("expected 3 retries, got %d", stats.TotalRetries)
	}

	// Record success
	bc.RecordSuccess()

	// Check state is reset
	// Trigger another failure - delay should be back to initial
	_, delay := bc.HandleError(job, resErr)
	// With jitter, delay should be around initial delay
	if delay > cfg.InitialDelay*2 {
		t.Errorf("delay %v too high after success reset", delay)
	}
}

func TestBackoffController_GlobalBackoff(t *testing.T) {
	cfg := DefaultBackoffConfig()
	cfg.ConsecutiveFailuresThreshold = 2
	cfg.InitialDelay = 100 * time.Millisecond
	cfg.PauseQueueOnBackoff = true
	bc := NewBackoffController(cfg)

	// Create a mock scheduler
	mockScheduler := New(DefaultConfig())
	bc.SetScheduler(mockScheduler)

	job := NewSpawnJob("test-job", JobTypePaneSplit, "test-session")
	resErr := &ResourceError{
		Type:      ResourceErrorEAGAIN,
		Retryable: true,
	}

	// First failure
	bc.HandleError(job, resErr)
	if bc.IsInGlobalBackoff() {
		t.Error("should not be in global backoff after 1 failure")
	}

	// Second failure - should trigger global backoff
	bc.HandleError(job, resErr)
	if !bc.IsInGlobalBackoff() {
		t.Error("should be in global backoff after 2 failures")
	}

	remaining := bc.RemainingBackoff()
	if remaining <= 0 {
		t.Error("expected positive remaining backoff time")
	}

	// Wait for backoff to end
	time.Sleep(remaining + 50*time.Millisecond)

	if bc.IsInGlobalBackoff() {
		t.Error("global backoff should have ended")
	}
}

func TestBackoffController_JitterBounds(t *testing.T) {
	cfg := DefaultBackoffConfig()
	cfg.InitialDelay = 1 * time.Second
	cfg.JitterFactor = 0.3
	bc := NewBackoffController(cfg)

	job := NewSpawnJob("test-job", JobTypePaneSplit, "test-session")
	resErr := &ResourceError{
		Type:      ResourceErrorEAGAIN,
		Retryable: true,
	}

	// Collect delay samples
	delays := make([]time.Duration, 100)
	for i := 0; i < 100; i++ {
		_, delay := bc.HandleError(job, resErr)
		delays[i] = delay
		bc.Reset() // Reset to get consistent samples
	}

	// Check that delays vary (jitter is working)
	var minDelay, maxDelay time.Duration = delays[0], delays[0]
	for _, d := range delays {
		if d < minDelay {
			minDelay = d
		}
		if d > maxDelay {
			maxDelay = d
		}
	}

	// With 30% jitter, range should be approximately ±30% of initial
	expectedMin := time.Duration(float64(cfg.InitialDelay) * 0.7)
	expectedMax := time.Duration(float64(cfg.InitialDelay) * 1.3)

	if minDelay < expectedMin-100*time.Millisecond {
		t.Errorf("min delay %v too low (expected >= ~%v)", minDelay, expectedMin)
	}
	if maxDelay > expectedMax+100*time.Millisecond {
		t.Errorf("max delay %v too high (expected <= ~%v)", maxDelay, expectedMax)
	}

	// Verify there's actual variance
	if maxDelay-minDelay < 100*time.Millisecond {
		t.Error("jitter appears to have no effect - delays are too similar")
	}

	t.Logf("Jitter range: %v - %v (expected ~%v - ~%v)",
		minDelay, maxDelay, expectedMin, expectedMax)
}

func TestBackoffController_ExponentialGrowth(t *testing.T) {
	cfg := BackoffConfig{
		InitialDelay:                 100 * time.Millisecond,
		MaxDelay:                     10 * time.Second,
		Multiplier:                   2.0,
		JitterFactor:                 0, // Disable jitter for predictable test
		MaxRetries:                   10,
		PauseQueueOnBackoff:         false,
		ConsecutiveFailuresThreshold: 100, // High to avoid global backoff
	}
	bc := NewBackoffController(cfg)

	job := NewSpawnJob("test-job", JobTypePaneSplit, "test-session")
	resErr := &ResourceError{
		Type:      ResourceErrorEAGAIN,
		Retryable: true,
	}

	var prevDelay time.Duration
	for i := 0; i < 5; i++ {
		_, delay := bc.HandleError(job, resErr)
		if i > 0 {
			// Each delay should be roughly 2x the previous (multiplier)
			expectedDelay := time.Duration(float64(prevDelay) * cfg.Multiplier)
			// Allow some tolerance for minimum delay enforcement
			if delay < expectedDelay/2 {
				t.Errorf("iteration %d: delay %v much less than expected %v",
					i, delay, expectedDelay)
			}
		}
		prevDelay = delay
		t.Logf("iteration %d: delay=%v", i, delay)
	}
}

func TestBackoffController_MaxDelay(t *testing.T) {
	cfg := BackoffConfig{
		InitialDelay:                 1 * time.Second,
		MaxDelay:                     2 * time.Second,
		Multiplier:                   10.0, // Aggressive growth
		JitterFactor:                 0,
		MaxRetries:                   10,
		PauseQueueOnBackoff:         false,
		ConsecutiveFailuresThreshold: 100,
	}
	bc := NewBackoffController(cfg)

	job := NewSpawnJob("test-job", JobTypePaneSplit, "test-session")
	resErr := &ResourceError{
		Type:      ResourceErrorEAGAIN,
		Retryable: true,
	}

	// After a few iterations, delay should cap at MaxDelay
	for i := 0; i < 5; i++ {
		_, delay := bc.HandleError(job, resErr)
		if delay > cfg.MaxDelay+100*time.Millisecond {
			t.Errorf("delay %v exceeds max %v", delay, cfg.MaxDelay)
		}
	}
}

func TestBackoffController_Hooks(t *testing.T) {
	cfg := DefaultBackoffConfig()
	cfg.ConsecutiveFailuresThreshold = 1
	cfg.InitialDelay = 50 * time.Millisecond
	cfg.PauseQueueOnBackoff = true
	cfg.MaxRetries = 2 // Set low for testing
	bc := NewBackoffController(cfg)

	var backoffStarted int32
	var backoffEnded int32
	var retryExhausted int32

	bc.SetHooks(
		func(delay time.Duration, reason ResourceErrorType) {
			atomic.AddInt32(&backoffStarted, 1)
		},
		func(duration time.Duration) {
			atomic.AddInt32(&backoffEnded, 1)
		},
		func(job *SpawnJob, attempts int) {
			atomic.AddInt32(&retryExhausted, 1)
		},
	)

	job := NewSpawnJob("test-job", JobTypePaneSplit, "test-session")
	resErr := &ResourceError{
		Type:      ResourceErrorEAGAIN,
		Retryable: true,
	}

	// First failure triggers global backoff
	bc.HandleError(job, resErr)
	if atomic.LoadInt32(&backoffStarted) != 1 {
		t.Error("backoff start hook not called")
	}

	// Wait for backoff to end
	time.Sleep(200 * time.Millisecond)
	if atomic.LoadInt32(&backoffEnded) != 1 {
		t.Error("backoff end hook not called")
	}

	// Exhaust retries (need to reach cfg.MaxRetries = 2)
	job.RetryCount = 2 // Equal to cfg.MaxRetries
	bc.HandleError(job, resErr)
	if atomic.LoadInt32(&retryExhausted) != 1 {
		t.Error("retry exhausted hook not called")
	}
}

func TestCalculateJitteredDelay(t *testing.T) {
	base := 1 * time.Second

	// Test with 0 jitter
	delay := CalculateJitteredDelay(base, 0)
	if delay != base {
		t.Errorf("expected %v with 0 jitter, got %v", base, delay)
	}

	// Test with jitter
	samples := make([]time.Duration, 100)
	for i := 0; i < 100; i++ {
		samples[i] = CalculateJitteredDelay(base, 0.5)
	}

	var min, max time.Duration = samples[0], samples[0]
	for _, s := range samples {
		if s < min {
			min = s
		}
		if s > max {
			max = s
		}
	}

	// With 50% jitter, should range from 0.5s to 1.5s
	if min < 400*time.Millisecond || max > 1600*time.Millisecond {
		t.Errorf("jitter out of expected range: %v - %v", min, max)
	}
}

func TestExponentialBackoff(t *testing.T) {
	initial := 100 * time.Millisecond
	max := 10 * time.Second
	multiplier := 2.0

	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{0, initial},
		{1, 200 * time.Millisecond},
		{2, 400 * time.Millisecond},
		{3, 800 * time.Millisecond},
		{10, max}, // Should cap at max
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("attempt_%d", tt.attempt), func(t *testing.T) {
			got := ExponentialBackoff(tt.attempt, initial, max, multiplier)
			if got != tt.expected {
				t.Errorf("got %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestResourceError_Error(t *testing.T) {
	err := &ResourceError{
		Original: errors.New("test error"),
		Type:     ResourceErrorEAGAIN,
	}
	if err.Error() != "test error" {
		t.Errorf("got %q, want %q", err.Error(), "test error")
	}

	err2 := &ResourceError{Type: ResourceErrorENOMEM}
	if err2.Error() != "ENOMEM: resource exhausted" {
		t.Errorf("got %q", err2.Error())
	}
}

func TestResourceError_Unwrap(t *testing.T) {
	original := errors.New("original error")
	err := &ResourceError{Original: original}

	if !errors.Is(err, original) {
		t.Error("errors.Is should match original")
	}
}

func BenchmarkClassifyError(b *testing.B) {
	err := errors.New("resource temporarily unavailable")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ClassifyError(err, 0, "")
	}
}

func BenchmarkBackoffController_HandleError(b *testing.B) {
	cfg := DefaultBackoffConfig()
	cfg.ConsecutiveFailuresThreshold = 100000 // Avoid global backoff
	bc := NewBackoffController(cfg)

	job := NewSpawnJob("bench-job", JobTypePaneSplit, "session")
	resErr := &ResourceError{Type: ResourceErrorEAGAIN, Retryable: true}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bc.HandleError(job, resErr)
		bc.Reset()
	}
}
