package scheduler

import (
	"encoding/json"
	"time"
)

// Progress represents the current spawn progress for TUI/robot output.
type Progress struct {
	// Timestamp is when this progress snapshot was taken.
	Timestamp time.Time `json:"timestamp"`

	// Status is the overall scheduler status.
	Status string `json:"status"` // running, paused, stopped

	// QueuedCount is the number of jobs waiting in queue.
	QueuedCount int `json:"queued_count"`

	// RunningCount is the number of jobs currently executing.
	RunningCount int `json:"running_count"`

	// CompletedCount is the number of completed jobs (session lifetime).
	CompletedCount int `json:"completed_count"`

	// FailedCount is the number of failed jobs.
	FailedCount int `json:"failed_count"`

	// EstimatedETASeconds is the estimated time until queue is empty.
	EstimatedETASeconds float64 `json:"estimated_eta_seconds,omitempty"`

	// RateLimitInfo contains current rate limiter state.
	RateLimitInfo RateLimitInfo `json:"rate_limit_info"`

	// Queued contains details of queued jobs.
	Queued []JobProgress `json:"queued,omitempty"`

	// Running contains details of running jobs.
	Running []JobProgress `json:"running,omitempty"`

	// RecentCompleted contains recently completed jobs.
	RecentCompleted []JobProgress `json:"recent_completed,omitempty"`

	// BySession contains progress grouped by session.
	BySession map[string]*SessionProgress `json:"by_session,omitempty"`
}

// RateLimitInfo contains rate limiter state for display.
type RateLimitInfo struct {
	// AvailableTokens is the current token count.
	AvailableTokens float64 `json:"available_tokens"`

	// Rate is tokens per second.
	Rate float64 `json:"rate"`

	// Capacity is the maximum tokens.
	Capacity float64 `json:"capacity"`

	// WaitingCount is requests waiting for tokens.
	WaitingCount int `json:"waiting_count"`

	// NextTokenInMs is milliseconds until next token.
	NextTokenInMs int64 `json:"next_token_in_ms"`
}

// JobProgress represents a single job's progress for display.
type JobProgress struct {
	// ID is the job ID.
	ID string `json:"id"`

	// Type is the job type.
	Type JobType `json:"type"`

	// Status is the current status.
	Status JobStatus `json:"status"`

	// SessionName is the target session.
	SessionName string `json:"session_name"`

	// AgentType is the agent type if applicable.
	AgentType string `json:"agent_type,omitempty"`

	// PaneIndex is the pane index if applicable.
	PaneIndex int `json:"pane_index,omitempty"`

	// Priority is the job priority.
	Priority JobPriority `json:"priority"`

	// QueuedFor is how long the job has been queued.
	QueuedFor time.Duration `json:"queued_for,omitempty"`

	// RunningFor is how long the job has been running.
	RunningFor time.Duration `json:"running_for,omitempty"`

	// Error is any error message.
	Error string `json:"error,omitempty"`

	// RetryCount is the current retry count.
	RetryCount int `json:"retry_count,omitempty"`

	// EstimatedETASeconds is estimated time until this job starts.
	EstimatedETASeconds float64 `json:"estimated_eta_seconds,omitempty"`
}

// SessionProgress groups progress by session.
type SessionProgress struct {
	// SessionName is the session name.
	SessionName string `json:"session_name"`

	// QueuedCount is queued jobs for this session.
	QueuedCount int `json:"queued_count"`

	// RunningCount is running jobs for this session.
	RunningCount int `json:"running_count"`

	// CompletedCount is completed jobs for this session.
	CompletedCount int `json:"completed_count"`

	// FailedCount is failed jobs for this session.
	FailedCount int `json:"failed_count"`

	// TotalPanes is the total panes being spawned.
	TotalPanes int `json:"total_panes"`

	// PanesReady is panes that are ready.
	PanesReady int `json:"panes_ready"`

	// ProgressPercent is the completion percentage.
	ProgressPercent float64 `json:"progress_percent"`
}

// GetProgress returns the current spawn progress.
func (s *Scheduler) GetProgress() *Progress {
	stats := s.Stats()

	status := "running"
	if !s.started.Load() {
		status = "stopped"
	} else if s.paused.Load() {
		status = "paused"
	}

	progress := &Progress{
		Timestamp:      time.Now(),
		Status:         status,
		QueuedCount:    stats.CurrentQueueSize,
		RunningCount:   stats.CurrentRunning,
		CompletedCount: int(stats.TotalCompleted),
		FailedCount:    int(stats.TotalFailed),
		RateLimitInfo: RateLimitInfo{
			AvailableTokens: stats.LimiterStats.CurrentTokens,
			Rate:            s.globalLimiter.rate,
			Capacity:        s.globalLimiter.capacity,
			WaitingCount:    stats.LimiterStats.Waiting,
			NextTokenInMs:   s.globalLimiter.TimeUntilNextToken().Milliseconds(),
		},
		BySession: make(map[string]*SessionProgress),
	}

	// Estimate total ETA
	if progress.QueuedCount > 0 {
		tokensNeeded := float64(progress.QueuedCount) / float64(s.workers)
		etaSeconds := tokensNeeded / progress.RateLimitInfo.Rate
		if etaSeconds < 0 {
			etaSeconds = 0
		}
		progress.EstimatedETASeconds = etaSeconds
	}

	// Collect queued jobs
	for _, job := range s.GetQueuedJobs() {
		jp := JobProgress{
			ID:          job.ID,
			Type:        job.Type,
			Status:      job.Status,
			SessionName: job.SessionName,
			AgentType:   job.AgentType,
			PaneIndex:   job.PaneIndex,
			Priority:    job.Priority,
			QueuedFor:   job.QueueDuration(),
			RetryCount:  job.RetryCount,
		}

		eta, err := s.EstimateETA(job.ID)
		if err == nil {
			jp.EstimatedETASeconds = eta.Seconds()
		}

		progress.Queued = append(progress.Queued, jp)

		// Update session progress
		sp := progress.BySession[job.SessionName]
		if sp == nil {
			sp = &SessionProgress{SessionName: job.SessionName}
			progress.BySession[job.SessionName] = sp
		}
		sp.QueuedCount++
		sp.TotalPanes++
	}

	// Collect running jobs
	for _, job := range s.GetRunningJobs() {
		jp := JobProgress{
			ID:          job.ID,
			Type:        job.Type,
			Status:      job.Status,
			SessionName: job.SessionName,
			AgentType:   job.AgentType,
			PaneIndex:   job.PaneIndex,
			Priority:    job.Priority,
			RunningFor:  job.ExecutionDuration(),
			RetryCount:  job.RetryCount,
		}
		progress.Running = append(progress.Running, jp)

		sp := progress.BySession[job.SessionName]
		if sp == nil {
			sp = &SessionProgress{SessionName: job.SessionName}
			progress.BySession[job.SessionName] = sp
		}
		sp.RunningCount++
		sp.TotalPanes++
	}

	// Collect recent completed
	for _, job := range s.GetRecentCompleted(10) {
		jp := JobProgress{
			ID:          job.ID,
			Type:        job.Type,
			Status:      job.Status,
			SessionName: job.SessionName,
			AgentType:   job.AgentType,
			PaneIndex:   job.PaneIndex,
			Priority:    job.Priority,
			Error:       job.Error,
			RetryCount:  job.RetryCount,
		}
		progress.RecentCompleted = append(progress.RecentCompleted, jp)

		sp := progress.BySession[job.SessionName]
		if sp == nil {
			sp = &SessionProgress{SessionName: job.SessionName}
			progress.BySession[job.SessionName] = sp
		}
		if job.Status == StatusCompleted {
			sp.CompletedCount++
			sp.PanesReady++
		} else if job.Status == StatusFailed {
			sp.FailedCount++
		}
		sp.TotalPanes++
	}

	// Calculate progress percentages
	for _, sp := range progress.BySession {
		if sp.TotalPanes > 0 {
			sp.ProgressPercent = float64(sp.PanesReady) / float64(sp.TotalPanes) * 100
		}
	}

	return progress
}

// JSON returns the progress as JSON.
func (p *Progress) JSON() ([]byte, error) {
	return json.MarshalIndent(p, "", "  ")
}

// FormatETA formats duration as human-readable ETA.
func FormatETA(d time.Duration) string {
	if d <= 0 {
		return "now"
	}
	if d < time.Second {
		return "<1s"
	}
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	if d < time.Hour {
		return d.Round(time.Second).String()
	}
	return d.Round(time.Minute).String()
}

// ProgressEvent is emitted for progress updates.
type ProgressEvent struct {
	// Type is the event type.
	Type string `json:"type"` // job_enqueued, job_started, job_completed, job_failed, progress_update

	// JobID is the job ID if applicable.
	JobID string `json:"job_id,omitempty"`

	// SessionName is the session name if applicable.
	SessionName string `json:"session_name,omitempty"`

	// Progress is the current progress snapshot.
	Progress *Progress `json:"progress,omitempty"`

	// Message is a human-readable message.
	Message string `json:"message,omitempty"`

	// Timestamp is when this event occurred.
	Timestamp time.Time `json:"timestamp"`
}

// ProgressSubscriber receives progress events.
type ProgressSubscriber func(event ProgressEvent)

// ProgressBroadcaster broadcasts progress events to subscribers.
type ProgressBroadcaster struct {
	subscribers []ProgressSubscriber
}

// NewProgressBroadcaster creates a new broadcaster.
func NewProgressBroadcaster() *ProgressBroadcaster {
	return &ProgressBroadcaster{
		subscribers: make([]ProgressSubscriber, 0),
	}
}

// Subscribe adds a subscriber.
func (b *ProgressBroadcaster) Subscribe(sub ProgressSubscriber) {
	b.subscribers = append(b.subscribers, sub)
}

// Broadcast sends an event to all subscribers.
func (b *ProgressBroadcaster) Broadcast(event ProgressEvent) {
	for _, sub := range b.subscribers {
		sub(event)
	}
}

// CreateProgressHooks creates scheduler hooks that broadcast progress events.
func CreateProgressHooks(broadcaster *ProgressBroadcaster, scheduler *Scheduler) Hooks {
	return Hooks{
		OnJobEnqueued: func(job *SpawnJob) {
			broadcaster.Broadcast(ProgressEvent{
				Type:        "job_enqueued",
				JobID:       job.ID,
				SessionName: job.SessionName,
				Message:     "Job enqueued",
				Timestamp:   time.Now(),
			})
		},
		OnJobStarted: func(job *SpawnJob) {
			broadcaster.Broadcast(ProgressEvent{
				Type:        "job_started",
				JobID:       job.ID,
				SessionName: job.SessionName,
				Message:     "Job started",
				Timestamp:   time.Now(),
			})
		},
		OnJobCompleted: func(job *SpawnJob) {
			broadcaster.Broadcast(ProgressEvent{
				Type:        "job_completed",
				JobID:       job.ID,
				SessionName: job.SessionName,
				Progress:    scheduler.GetProgress(),
				Message:     "Job completed",
				Timestamp:   time.Now(),
			})
		},
		OnJobFailed: func(job *SpawnJob, err error) {
			broadcaster.Broadcast(ProgressEvent{
				Type:        "job_failed",
				JobID:       job.ID,
				SessionName: job.SessionName,
				Progress:    scheduler.GetProgress(),
				Message:     "Job failed: " + err.Error(),
				Timestamp:   time.Now(),
			})
		},
		OnBackpressure: func(queueSize int, waitTime time.Duration) {
			broadcaster.Broadcast(ProgressEvent{
				Type:      "backpressure",
				Progress:  scheduler.GetProgress(),
				Message:   "Queue backpressure detected",
				Timestamp: time.Now(),
			})
		},
	}
}
