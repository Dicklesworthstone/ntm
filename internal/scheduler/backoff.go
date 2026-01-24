package scheduler

import (
	"errors"
	"log/slog"
	"math"
	"math/rand"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ResourceErrorType classifies the type of resource exhaustion error.
type ResourceErrorType string

const (
	ResourceErrorNone    ResourceErrorType = ""
	ResourceErrorEAGAIN  ResourceErrorType = "EAGAIN"
	ResourceErrorENOMEM  ResourceErrorType = "ENOMEM"
	ResourceErrorENFILE  ResourceErrorType = "ENFILE"
	ResourceErrorEMFILE  ResourceErrorType = "EMFILE"
	ResourceErrorRateLimit ResourceErrorType = "RATE_LIMIT"
)

// ResourceError wraps an error with resource exhaustion classification.
type ResourceError struct {
	Original    error
	Type        ResourceErrorType
	ExitCode    int
	StderrHint  string
	Retryable   bool
	Timestamp   time.Time
}

func (e *ResourceError) Error() string {
	if e.Original != nil {
		return e.Original.Error()
	}
	return string(e.Type) + ": resource exhausted"
}

func (e *ResourceError) Unwrap() error {
	return e.Original
}

// ClassifyError examines an error and classifies it as a resource exhaustion error.
func ClassifyError(err error, exitCode int, stderr string) *ResourceError {
	if err == nil {
		return nil
	}

	re := &ResourceError{
		Original:  err,
		ExitCode:  exitCode,
		Timestamp: time.Now(),
	}

	// Check for syscall errors first
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case syscall.EAGAIN: // EWOULDBLOCK is same as EAGAIN on Linux
			re.Type = ResourceErrorEAGAIN
			re.Retryable = true
			return re
		case syscall.ENOMEM:
			re.Type = ResourceErrorENOMEM
			re.Retryable = true
			return re
		case syscall.ENFILE:
			re.Type = ResourceErrorENFILE
			re.Retryable = true
			return re
		case syscall.EMFILE:
			re.Type = ResourceErrorEMFILE
			re.Retryable = true
			return re
		}
	}

	// Check error message patterns
	errStr := strings.ToLower(err.Error())
	if classifyFromString(errStr, re) {
		return re
	}

	// Check stderr patterns
	if stderr != "" {
		stderrLower := strings.ToLower(stderr)
		if classifyFromString(stderrLower, re) {
			re.StderrHint = truncateHint(stderr, 200)
			return re
		}
	}

	// Check exit codes
	if classifyFromExitCode(exitCode, re) {
		return re
	}

	// Not a resource error
	return nil
}

// Error patterns for resource exhaustion detection.
var (
	eagainPatterns = []*regexp.Regexp{
		regexp.MustCompile(`resource temporarily unavailable`),
		regexp.MustCompile(`eagain`),
		regexp.MustCompile(`try again`),
		regexp.MustCompile(`cannot allocate memory`),
		regexp.MustCompile(`fork: retry`),
		regexp.MustCompile(`fork failed`),
		regexp.MustCompile(`cannot fork`),
	}
	enomemPatterns = []*regexp.Regexp{
		regexp.MustCompile(`out of memory`),
		regexp.MustCompile(`enomem`),
		regexp.MustCompile(`memory allocation failed`),
		regexp.MustCompile(`not enough memory`),
		regexp.MustCompile(`insufficient memory`),
	}
	rateLimitPatterns = []*regexp.Regexp{
		regexp.MustCompile(`rate limit`),
		regexp.MustCompile(`too many requests`),
		regexp.MustCompile(`quota exceeded`),
		regexp.MustCompile(`429`),
		regexp.MustCompile(`throttled`),
	}
	fdLimitPatterns = []*regexp.Regexp{
		regexp.MustCompile(`too many open files`),
		regexp.MustCompile(`emfile`),
		regexp.MustCompile(`enfile`),
		regexp.MustCompile(`file table overflow`),
	}
)

func classifyFromString(s string, re *ResourceError) bool {
	for _, p := range eagainPatterns {
		if p.MatchString(s) {
			re.Type = ResourceErrorEAGAIN
			re.Retryable = true
			return true
		}
	}
	for _, p := range enomemPatterns {
		if p.MatchString(s) {
			re.Type = ResourceErrorENOMEM
			re.Retryable = true
			return true
		}
	}
	for _, p := range rateLimitPatterns {
		if p.MatchString(s) {
			re.Type = ResourceErrorRateLimit
			re.Retryable = true
			return true
		}
	}
	for _, p := range fdLimitPatterns {
		if p.MatchString(s) {
			re.Type = ResourceErrorEMFILE
			re.Retryable = true
			return true
		}
	}
	return false
}

func classifyFromExitCode(code int, re *ResourceError) bool {
	// Common exit codes for resource exhaustion
	switch code {
	case 11: // EAGAIN on some systems
		re.Type = ResourceErrorEAGAIN
		re.Retryable = true
		return true
	case 12: // ENOMEM
		re.Type = ResourceErrorENOMEM
		re.Retryable = true
		return true
	case 137: // OOM killed (128 + SIGKILL)
		re.Type = ResourceErrorENOMEM
		re.Retryable = true
		return true
	}
	return false
}

func truncateHint(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// BackoffConfig configures the exponential backoff behavior.
type BackoffConfig struct {
	// InitialDelay is the starting delay for backoff.
	InitialDelay time.Duration `json:"initial_delay"`

	// MaxDelay is the maximum backoff delay.
	MaxDelay time.Duration `json:"max_delay"`

	// Multiplier is the factor by which delay increases.
	Multiplier float64 `json:"multiplier"`

	// JitterFactor is the random jitter as a fraction of delay (0.0-1.0).
	JitterFactor float64 `json:"jitter_factor"`

	// MaxRetries is the maximum number of retry attempts.
	MaxRetries int `json:"max_retries"`

	// PauseQueueOnBackoff pauses the scheduler queue during backoff.
	PauseQueueOnBackoff bool `json:"pause_queue_on_backoff"`

	// ConsecutiveFailuresThreshold triggers global backoff after N failures.
	ConsecutiveFailuresThreshold int `json:"consecutive_failures_threshold"`
}

// DefaultBackoffConfig returns sensible default configuration.
func DefaultBackoffConfig() BackoffConfig {
	return BackoffConfig{
		InitialDelay:                 500 * time.Millisecond,
		MaxDelay:                     30 * time.Second,
		Multiplier:                   2.0,
		JitterFactor:                 0.3,
		MaxRetries:                   5,
		PauseQueueOnBackoff:         true,
		ConsecutiveFailuresThreshold: 3,
	}
}

// BackoffController manages exponential backoff with jitter for resource errors.
type BackoffController struct {
	mu sync.Mutex

	config BackoffConfig

	// consecutiveFailures tracks failures without success.
	consecutiveFailures int

	// currentDelay is the current backoff delay.
	currentDelay time.Duration

	// globalBackoffActive indicates global queue pause is active.
	globalBackoffActive atomic.Bool

	// globalBackoffUntil is when global backoff ends.
	globalBackoffUntil time.Time

	// stats tracks backoff statistics.
	stats BackoffStats

	// scheduler is a reference to pause/resume the queue.
	scheduler *Scheduler

	// hooks for events.
	onBackoffStart    func(delay time.Duration, reason ResourceErrorType)
	onBackoffEnd      func(totalDuration time.Duration)
	onRetryExhausted  func(job *SpawnJob, attempts int)
}

// BackoffStats contains backoff statistics.
type BackoffStats struct {
	TotalBackoffs     int64         `json:"total_backoffs"`
	TotalRetries      int64         `json:"total_retries"`
	TotalExhausted    int64         `json:"total_exhausted"`
	MaxConsecutive    int           `json:"max_consecutive"`
	TotalBackoffTime  time.Duration `json:"total_backoff_time"`
	LastBackoffReason ResourceErrorType `json:"last_backoff_reason,omitempty"`
	LastBackoffAt     time.Time     `json:"last_backoff_at,omitempty"`
}

// NewBackoffController creates a new backoff controller.
func NewBackoffController(cfg BackoffConfig) *BackoffController {
	return &BackoffController{
		config:       cfg,
		currentDelay: cfg.InitialDelay,
	}
}

// SetScheduler sets the scheduler reference for queue pause/resume.
func (bc *BackoffController) SetScheduler(s *Scheduler) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	bc.scheduler = s
}

// SetHooks sets event callbacks.
func (bc *BackoffController) SetHooks(
	onStart func(time.Duration, ResourceErrorType),
	onEnd func(time.Duration),
	onExhausted func(*SpawnJob, int),
) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	bc.onBackoffStart = onStart
	bc.onBackoffEnd = onEnd
	bc.onRetryExhausted = onExhausted
}

// HandleError processes a resource error and returns the backoff action.
// Returns (shouldRetry, delay) where delay is 0 if no backoff needed.
func (bc *BackoffController) HandleError(job *SpawnJob, resErr *ResourceError) (bool, time.Duration) {
	if resErr == nil || !resErr.Retryable {
		bc.recordSuccess()
		return false, 0
	}

	bc.mu.Lock()
	defer bc.mu.Unlock()

	bc.consecutiveFailures++
	bc.stats.TotalRetries++
	bc.stats.LastBackoffReason = resErr.Type
	bc.stats.LastBackoffAt = time.Now()

	if bc.consecutiveFailures > bc.stats.MaxConsecutive {
		bc.stats.MaxConsecutive = bc.consecutiveFailures
	}

	// Check if retries exhausted for this job
	if job.RetryCount >= bc.config.MaxRetries {
		bc.stats.TotalExhausted++
		slog.Warn("retry attempts exhausted",
			"job_id", job.ID,
			"attempts", job.RetryCount,
			"error_type", resErr.Type,
		)
		if bc.onRetryExhausted != nil {
			bc.onRetryExhausted(job, job.RetryCount)
		}
		return false, 0
	}

	// Calculate delay with exponential backoff
	delay := bc.calculateDelay()

	slog.Info("resource error detected, backing off",
		"job_id", job.ID,
		"error_type", resErr.Type,
		"consecutive_failures", bc.consecutiveFailures,
		"backoff_delay", delay,
		"retry_count", job.RetryCount,
		"max_retries", bc.config.MaxRetries,
	)

	// Check if we should trigger global backoff
	if bc.consecutiveFailures >= bc.config.ConsecutiveFailuresThreshold {
		bc.triggerGlobalBackoff(delay, resErr.Type)
	}

	bc.stats.TotalBackoffs++
	bc.stats.TotalBackoffTime += delay

	return true, delay
}

// calculateDelay returns the next backoff delay with jitter.
func (bc *BackoffController) calculateDelay() time.Duration {
	// Calculate base delay with exponential growth
	delay := bc.currentDelay

	// Apply jitter: delay ± (jitterFactor * delay)
	jitter := float64(delay) * bc.config.JitterFactor
	jitterRange := jitter * 2
	randomJitter := (rand.Float64() * jitterRange) - jitter
	delayWithJitter := time.Duration(float64(delay) + randomJitter)

	// Update for next call (exponential increase)
	bc.currentDelay = time.Duration(float64(bc.currentDelay) * bc.config.Multiplier)
	if bc.currentDelay > bc.config.MaxDelay {
		bc.currentDelay = bc.config.MaxDelay
	}

	// Ensure minimum delay
	if delayWithJitter < 100*time.Millisecond {
		delayWithJitter = 100 * time.Millisecond
	}

	return delayWithJitter
}

// triggerGlobalBackoff pauses the scheduler queue during backoff.
func (bc *BackoffController) triggerGlobalBackoff(delay time.Duration, reason ResourceErrorType) {
	if !bc.config.PauseQueueOnBackoff {
		return
	}

	if bc.globalBackoffActive.Load() {
		// Already in global backoff, extend it
		bc.globalBackoffUntil = time.Now().Add(delay)
		return
	}

	bc.globalBackoffActive.Store(true)
	bc.globalBackoffUntil = time.Now().Add(delay)

	slog.Warn("triggering global queue backoff",
		"reason", reason,
		"consecutive_failures", bc.consecutiveFailures,
		"backoff_duration", delay,
	)

	if bc.onBackoffStart != nil {
		bc.onBackoffStart(delay, reason)
	}

	// Pause the scheduler
	if bc.scheduler != nil {
		bc.scheduler.Pause()
	}

	// Schedule resume
	go func(resumeAt time.Time) {
		sleepDuration := time.Until(resumeAt)
		if sleepDuration > 0 {
			time.Sleep(sleepDuration)
		}
		bc.endGlobalBackoff()
	}(bc.globalBackoffUntil)
}

// endGlobalBackoff resumes normal operation.
func (bc *BackoffController) endGlobalBackoff() {
	bc.mu.Lock()
	defer bc.mu.Unlock()

	if !bc.globalBackoffActive.Load() {
		return
	}

	totalDuration := time.Since(bc.stats.LastBackoffAt)
	bc.globalBackoffActive.Store(false)

	slog.Info("global queue backoff ended",
		"total_duration", totalDuration,
	)

	if bc.onBackoffEnd != nil {
		bc.onBackoffEnd(totalDuration)
	}

	// Resume the scheduler
	if bc.scheduler != nil {
		bc.scheduler.Resume()
	}
}

// recordSuccess records a successful operation and resets backoff state.
func (bc *BackoffController) recordSuccess() {
	bc.mu.Lock()
	defer bc.mu.Unlock()

	if bc.consecutiveFailures > 0 {
		slog.Debug("backoff reset after success",
			"previous_consecutive_failures", bc.consecutiveFailures,
		)
	}

	bc.consecutiveFailures = 0
	bc.currentDelay = bc.config.InitialDelay

	// End global backoff if active
	if bc.globalBackoffActive.Load() {
		bc.globalBackoffActive.Store(false)
		if bc.scheduler != nil {
			bc.scheduler.Resume()
		}
	}
}

// RecordSuccess is the public method to record success.
func (bc *BackoffController) RecordSuccess() {
	bc.recordSuccess()
}

// IsInGlobalBackoff returns true if global backoff is active.
func (bc *BackoffController) IsInGlobalBackoff() bool {
	return bc.globalBackoffActive.Load()
}

// RemainingBackoff returns remaining time in global backoff.
func (bc *BackoffController) RemainingBackoff() time.Duration {
	if !bc.globalBackoffActive.Load() {
		return 0
	}
	bc.mu.Lock()
	defer bc.mu.Unlock()
	remaining := time.Until(bc.globalBackoffUntil)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// Stats returns backoff statistics.
func (bc *BackoffController) Stats() BackoffStats {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	return bc.stats
}

// Reset resets the backoff controller to initial state.
func (bc *BackoffController) Reset() {
	bc.mu.Lock()
	defer bc.mu.Unlock()

	bc.consecutiveFailures = 0
	bc.currentDelay = bc.config.InitialDelay
	bc.globalBackoffActive.Store(false)
	bc.globalBackoffUntil = time.Time{}
}

// CalculateJitteredDelay calculates a delay with jitter for a given base.
// Useful for external callers that need jittered timing.
func CalculateJitteredDelay(base time.Duration, jitterFactor float64) time.Duration {
	if jitterFactor <= 0 {
		return base
	}
	if jitterFactor > 1 {
		jitterFactor = 1
	}
	jitter := float64(base) * jitterFactor
	jitterRange := jitter * 2
	randomJitter := (rand.Float64() * jitterRange) - jitter
	return time.Duration(float64(base) + randomJitter)
}

// ExponentialBackoff returns the delay for the nth retry attempt.
func ExponentialBackoff(attempt int, initial, max time.Duration, multiplier float64) time.Duration {
	if attempt <= 0 {
		return initial
	}
	delay := float64(initial) * math.Pow(multiplier, float64(attempt))
	if delay > float64(max) {
		return max
	}
	return time.Duration(delay)
}
