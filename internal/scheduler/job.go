// Package scheduler provides a global spawn scheduler with paced pane/agent creation.
// It serializes and paces all pane + agent creation to prevent resource exhaustion
// and rate limit errors.
package scheduler

import (
	"context"
	"sync"
	"time"
)

// JobType represents the type of spawn operation.
type JobType string

const (
	JobTypeSession  JobType = "session"  // Create a new tmux session
	JobTypePaneSplit JobType = "pane_split" // Split an existing pane
	JobTypeAgentLaunch JobType = "agent_launch" // Launch an agent in a pane
)

// JobPriority determines processing order within the queue.
type JobPriority int

const (
	PriorityUrgent   JobPriority = 0 // Respawns, recovery
	PriorityHigh     JobPriority = 1 // User-initiated spawns
	PriorityNormal   JobPriority = 2 // Regular batch spawns
	PriorityLow      JobPriority = 3 // Background/deferred spawns
)

// JobStatus represents the current state of a spawn job.
type JobStatus string

const (
	StatusPending    JobStatus = "pending"    // Waiting in queue
	StatusScheduled  JobStatus = "scheduled"  // Rate limiter approved, waiting for execution slot
	StatusRunning    JobStatus = "running"    // Currently executing
	StatusCompleted  JobStatus = "completed"  // Successfully finished
	StatusFailed     JobStatus = "failed"     // Failed with error
	StatusCancelled  JobStatus = "cancelled"  // Cancelled by user/system
	StatusRetrying   JobStatus = "retrying"   // Failed but will retry
)

// SpawnJob represents a single spawn operation in the queue.
type SpawnJob struct {
	// ID is a unique identifier for this job.
	ID string `json:"id"`

	// Type is the kind of spawn operation.
	Type JobType `json:"type"`

	// Priority determines processing order.
	Priority JobPriority `json:"priority"`

	// SessionName is the target session.
	SessionName string `json:"session_name"`

	// AgentType is the type of agent (cc, cod, gmi) if applicable.
	AgentType string `json:"agent_type,omitempty"`

	// PaneIndex is the target pane index if applicable.
	PaneIndex int `json:"pane_index,omitempty"`

	// Directory is the working directory for the spawn.
	Directory string `json:"directory,omitempty"`

	// Status is the current job status.
	Status JobStatus `json:"status"`

	// CreatedAt is when the job was created.
	CreatedAt time.Time `json:"created_at"`

	// ScheduledAt is when the job was approved by rate limiter.
	ScheduledAt time.Time `json:"scheduled_at,omitempty"`

	// StartedAt is when execution began.
	StartedAt time.Time `json:"started_at,omitempty"`

	// CompletedAt is when the job finished (success or failure).
	CompletedAt time.Time `json:"completed_at,omitempty"`

	// Error contains any error message if failed.
	Error string `json:"error,omitempty"`

	// RetryCount is the number of retry attempts.
	RetryCount int `json:"retry_count"`

	// MaxRetries is the maximum number of retries allowed.
	MaxRetries int `json:"max_retries"`

	// RetryDelay is the delay before next retry.
	RetryDelay time.Duration `json:"retry_delay,omitempty"`

	// Metadata contains additional context for the job.
	Metadata map[string]interface{} `json:"metadata,omitempty"`

	// Result contains the result of the spawn operation.
	Result *SpawnResult `json:"result,omitempty"`

	// BatchID groups related jobs for fairness tracking.
	BatchID string `json:"batch_id,omitempty"`

	// ParentJobID is the ID of the parent job if this is a sub-job.
	ParentJobID string `json:"parent_job_id,omitempty"`

	// Callback is called when the job completes (success or failure).
	Callback func(*SpawnJob) `json:"-"`

	// Context for cancellation.
	ctx    context.Context
	cancel context.CancelFunc

	// mu protects status updates.
	mu sync.RWMutex
}

// SpawnResult contains the result of a successful spawn operation.
type SpawnResult struct {
	// SessionName is the created/target session.
	SessionName string `json:"session_name"`

	// PaneID is the created pane ID.
	PaneID string `json:"pane_id,omitempty"`

	// PaneIndex is the index of the pane.
	PaneIndex int `json:"pane_index,omitempty"`

	// AgentType is the launched agent type.
	AgentType string `json:"agent_type,omitempty"`

	// Title is the pane title.
	Title string `json:"title,omitempty"`

	// Duration is how long the spawn took.
	Duration time.Duration `json:"duration"`
}

// NewSpawnJob creates a new spawn job with sensible defaults.
func NewSpawnJob(id string, jobType JobType, sessionName string) *SpawnJob {
	ctx, cancel := context.WithCancel(context.Background())
	return &SpawnJob{
		ID:          id,
		Type:        jobType,
		Priority:    PriorityNormal,
		SessionName: sessionName,
		Status:      StatusPending,
		CreatedAt:   time.Now(),
		MaxRetries:  3,
		RetryDelay:  time.Second,
		Metadata:    make(map[string]interface{}),
		ctx:         ctx,
		cancel:      cancel,
	}
}

// Cancel cancels this job.
func (j *SpawnJob) Cancel() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.cancel != nil {
		j.cancel()
	}
	if j.Status == StatusPending || j.Status == StatusScheduled {
		j.Status = StatusCancelled
		j.CompletedAt = time.Now()
	}
}

// Context returns the job's context.
func (j *SpawnJob) Context() context.Context {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.ctx
}

// IsCancelled returns true if the job was cancelled.
func (j *SpawnJob) IsCancelled() bool {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.Status == StatusCancelled || j.ctx.Err() != nil
}

// IsTerminal returns true if the job is in a terminal state.
func (j *SpawnJob) IsTerminal() bool {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.Status == StatusCompleted || j.Status == StatusFailed || j.Status == StatusCancelled
}

// SetStatus updates the job status with proper timestamps.
func (j *SpawnJob) SetStatus(status JobStatus) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Status = status
	now := time.Now()
	switch status {
	case StatusScheduled:
		j.ScheduledAt = now
	case StatusRunning:
		j.StartedAt = now
	case StatusCompleted, StatusFailed, StatusCancelled:
		j.CompletedAt = now
	}
}

// SetError sets an error on the job.
func (j *SpawnJob) SetError(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err != nil {
		j.Error = err.Error()
	}
}

// GetStatus returns the current status.
func (j *SpawnJob) GetStatus() JobStatus {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.Status
}

// QueueDuration returns how long the job has been in the queue.
func (j *SpawnJob) QueueDuration() time.Duration {
	j.mu.RLock()
	defer j.mu.RUnlock()
	if j.ScheduledAt.IsZero() {
		return time.Since(j.CreatedAt)
	}
	return j.ScheduledAt.Sub(j.CreatedAt)
}

// ExecutionDuration returns how long the job took to execute.
func (j *SpawnJob) ExecutionDuration() time.Duration {
	j.mu.RLock()
	defer j.mu.RUnlock()
	if j.StartedAt.IsZero() {
		return 0
	}
	if j.CompletedAt.IsZero() {
		return time.Since(j.StartedAt)
	}
	return j.CompletedAt.Sub(j.StartedAt)
}

// TotalDuration returns the total time from creation to completion.
func (j *SpawnJob) TotalDuration() time.Duration {
	j.mu.RLock()
	defer j.mu.RUnlock()
	if j.CompletedAt.IsZero() {
		return time.Since(j.CreatedAt)
	}
	return j.CompletedAt.Sub(j.CreatedAt)
}

// CanRetry returns true if the job can be retried.
func (j *SpawnJob) CanRetry() bool {
	j.mu.RLock()
	defer j.mu.RUnlock()
	// Only check retry count - status check happens in executeJob
	return j.RetryCount < j.MaxRetries
}

// IncrementRetry increments the retry count and updates status.
func (j *SpawnJob) IncrementRetry() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.RetryCount++
	j.Status = StatusRetrying
	j.Error = ""
}

// Clone creates a copy of the job for reporting (without callbacks/context).
func (j *SpawnJob) Clone() *SpawnJob {
	j.mu.RLock()
	defer j.mu.RUnlock()

	clone := &SpawnJob{
		ID:          j.ID,
		Type:        j.Type,
		Priority:    j.Priority,
		SessionName: j.SessionName,
		AgentType:   j.AgentType,
		PaneIndex:   j.PaneIndex,
		Directory:   j.Directory,
		Status:      j.Status,
		CreatedAt:   j.CreatedAt,
		ScheduledAt: j.ScheduledAt,
		StartedAt:   j.StartedAt,
		CompletedAt: j.CompletedAt,
		Error:       j.Error,
		RetryCount:  j.RetryCount,
		MaxRetries:  j.MaxRetries,
		RetryDelay:  j.RetryDelay,
		BatchID:     j.BatchID,
		ParentJobID: j.ParentJobID,
	}

	if j.Metadata != nil {
		clone.Metadata = make(map[string]interface{})
		for k, v := range j.Metadata {
			clone.Metadata[k] = v
		}
	}

	if j.Result != nil {
		result := *j.Result
		clone.Result = &result
	}

	return clone
}
