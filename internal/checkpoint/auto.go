package checkpoint

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

const (
	// AutoCheckpointPrefix is the prefix for auto-generated checkpoint names
	AutoCheckpointPrefix = "auto"
)

// AutoCheckpointReason describes why an auto-checkpoint was triggered
type AutoCheckpointReason string

const (
	ReasonBroadcast AutoCheckpointReason = "broadcast"  // Before sending to all agents
	ReasonAddAgents AutoCheckpointReason = "add_agents" // Before adding many agents
	ReasonSpawn     AutoCheckpointReason = "spawn"      // After spawning session
	ReasonRiskyOp   AutoCheckpointReason = "risky_op"   // Before other risky operation
	ReasonInterval  AutoCheckpointReason = "interval"   // Periodic interval checkpoint
	ReasonRotation  AutoCheckpointReason = "rotation"   // Before context rotation
	ReasonError     AutoCheckpointReason = "error"      // On agent error
)

// AutoEventType describes the type of event that triggered an auto-checkpoint
type AutoEventType int

const (
	EventRotation AutoEventType = iota // Context rotation is about to happen
	EventError                         // Agent error detected
)

// AutoEvent represents an event that can trigger an auto-checkpoint
type AutoEvent struct {
	Type        AutoEventType
	SessionName string
	AgentID     string // Which agent triggered the event
	Description string // Additional context
}

// AutoCheckpointConfig configures the background auto-checkpoint worker
type AutoCheckpointConfig struct {
	Enabled         bool // Master toggle
	IntervalMinutes int  // Periodic checkpoint interval (0 = disabled)
	MaxCheckpoints  int  // Max auto-checkpoints per session
	OnRotation      bool // Checkpoint before rotation
	OnError         bool // Checkpoint on error
	ScrollbackLines int  // Lines of scrollback to capture
	IncludeGit      bool // Capture git state
}

// AutoCheckpointOptions configures auto-checkpoint creation
type AutoCheckpointOptions struct {
	SessionName     string
	Reason          AutoCheckpointReason
	Description     string // Additional context
	ScrollbackLines int
	IncludeGit      bool
	MaxCheckpoints  int // Max auto-checkpoints to keep (rotation)
}

// AutoCheckpointer handles automatic checkpoint creation with rotation
type AutoCheckpointer struct {
	capturer *Capturer
	storage  *Storage
}

// NewAutoCheckpointer creates a new auto-checkpointer
func NewAutoCheckpointer() *AutoCheckpointer {
	return &AutoCheckpointer{
		capturer: NewCapturer(),
		storage:  NewStorage(),
	}
}

// Create creates an auto-checkpoint with the given options
// It returns the created checkpoint and any error encountered
func (a *AutoCheckpointer) Create(opts AutoCheckpointOptions) (*Checkpoint, error) {
	// Build checkpoint name from reason
	name := fmt.Sprintf("%s-%s", AutoCheckpointPrefix, opts.Reason)

	// Build description
	desc := fmt.Sprintf("Auto-checkpoint: %s", opts.Reason)
	if opts.Description != "" {
		desc = fmt.Sprintf("%s (%s)", desc, opts.Description)
	}

	// Build checkpoint options
	cpOpts := []CheckpointOption{
		WithDescription(desc),
		WithGitCapture(opts.IncludeGit),
	}
	if opts.ScrollbackLines > 0 {
		cpOpts = append(cpOpts, WithScrollbackLines(opts.ScrollbackLines))
	}

	// Create the checkpoint
	cp, err := a.capturer.Create(opts.SessionName, name, cpOpts...)
	if err != nil {
		return nil, fmt.Errorf("creating auto-checkpoint: %w", err)
	}

	// Apply rotation policy
	if opts.MaxCheckpoints > 0 {
		if err := a.rotateAutoCheckpoints(opts.SessionName, opts.MaxCheckpoints); err != nil {
			// Log but don't fail - checkpoint was created successfully
			log.Printf("Warning: failed to rotate auto-checkpoints: %v", err)
		}
	}

	return cp, nil
}

// rotateAutoCheckpoints ensures we don't exceed the max auto-checkpoints
// by deleting the oldest auto-checkpoints
func (a *AutoCheckpointer) rotateAutoCheckpoints(sessionName string, maxCount int) error {
	// List all checkpoints for the session
	checkpoints, err := a.storage.List(sessionName)
	if err != nil {
		return err
	}

	// Filter to auto-checkpoints only
	var autoCheckpoints []*Checkpoint
	for _, cp := range checkpoints {
		if isAutoCheckpoint(cp) {
			autoCheckpoints = append(autoCheckpoints, cp)
		}
	}

	// If under limit, nothing to do
	if len(autoCheckpoints) <= maxCount {
		return nil
	}

	// Delete oldest auto-checkpoints (list is sorted newest first)
	toDelete := autoCheckpoints[maxCount:]
	for _, cp := range toDelete {
		if err := a.storage.Delete(sessionName, cp.ID); err != nil {
			// Log but continue
			log.Printf("Warning: failed to delete old auto-checkpoint %s: %v", cp.ID, err)
		}
	}

	return nil
}

// isAutoCheckpoint checks if a checkpoint was auto-generated
func isAutoCheckpoint(cp *Checkpoint) bool {
	// Check by name prefix (use "auto-" to avoid matching names like "automatic")
	if strings.HasPrefix(cp.Name, AutoCheckpointPrefix+"-") {
		return true
	}
	// Also check description as fallback
	if strings.Contains(cp.Description, "Auto-checkpoint:") {
		return true
	}
	return false
}

// GetLastAutoCheckpoint returns the most recent auto-checkpoint for a session
func (a *AutoCheckpointer) GetLastAutoCheckpoint(sessionName string) (*Checkpoint, error) {
	checkpoints, err := a.storage.List(sessionName)
	if err != nil {
		return nil, err
	}

	for _, cp := range checkpoints {
		if isAutoCheckpoint(cp) {
			return cp, nil
		}
	}

	return nil, fmt.Errorf("no auto-checkpoints found for session: %s", sessionName)
}

// ListAutoCheckpoints returns all auto-checkpoints for a session
func (a *AutoCheckpointer) ListAutoCheckpoints(sessionName string) ([]*Checkpoint, error) {
	checkpoints, err := a.storage.List(sessionName)
	if err != nil {
		return nil, err
	}

	var autoCheckpoints []*Checkpoint
	for _, cp := range checkpoints {
		if isAutoCheckpoint(cp) {
			autoCheckpoints = append(autoCheckpoints, cp)
		}
	}

	return autoCheckpoints, nil
}

// TimeSinceLastAutoCheckpoint returns the duration since the last auto-checkpoint
// Returns 0 if no auto-checkpoint exists
func (a *AutoCheckpointer) TimeSinceLastAutoCheckpoint(sessionName string) time.Duration {
	cp, err := a.GetLastAutoCheckpoint(sessionName)
	if err != nil {
		return 0
	}
	return time.Since(cp.CreatedAt)
}

// BackgroundWorker runs automatic checkpoints in the background based on
// interval and event triggers.
type BackgroundWorker struct {
	checkpointer *AutoCheckpointer
	config       AutoCheckpointConfig
	sessionName  string

	events  chan AutoEvent
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	mu      sync.Mutex
	started bool // tracks if the worker goroutine was actually started

	// Statistics
	checkpointCount int
	lastCheckpoint  time.Time
	lastError       error
}

// NewBackgroundWorker creates a new background checkpoint worker.
func NewBackgroundWorker(sessionName string, config AutoCheckpointConfig) *BackgroundWorker {
	return &BackgroundWorker{
		checkpointer: NewAutoCheckpointer(),
		config:       config,
		sessionName:  sessionName,
		events:       make(chan AutoEvent, 10), // Buffered to prevent blocking
	}
}

// Start begins the background checkpoint worker.
// It will run until Stop is called or the context is cancelled.
func (w *BackgroundWorker) Start(ctx context.Context) {
	if !w.config.Enabled {
		log.Printf("Auto-checkpoint disabled for session %s", w.sessionName)
		return
	}

	if ctx == nil {
		ctx = context.Background()
	}

	w.mu.Lock()
	if w.cancel != nil {
		w.mu.Unlock()
		return
	}

	ctx, w.cancel = context.WithCancel(ctx)
	w.started = true
	w.wg.Add(1)
	w.mu.Unlock()
	go w.run(ctx)

	log.Printf("Started auto-checkpoint worker for session %s (interval: %dm)",
		w.sessionName, w.config.IntervalMinutes)
}

// Stop stops the background checkpoint worker gracefully.
func (w *BackgroundWorker) Stop() {
	w.mu.Lock()
	wasStarted := w.started
	cancel := w.cancel
	w.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	w.wg.Wait()

	w.mu.Lock()
	w.cancel = nil
	w.started = false
	w.mu.Unlock()

	if wasStarted {
		log.Printf("Stopped auto-checkpoint worker for session %s", w.sessionName)
	}
}

// SendEvent sends an event to the worker to potentially trigger a checkpoint.
func (w *BackgroundWorker) SendEvent(event AutoEvent) {
	select {
	case w.events <- event:
	default:
		// Channel full, log and drop
		log.Printf("Warning: auto-checkpoint event channel full, dropping event: %v", event.Type)
	}
}

// Stats returns statistics about the worker.
func (w *BackgroundWorker) Stats() (checkpointCount int, lastCheckpoint time.Time, lastError error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.checkpointCount, w.lastCheckpoint, w.lastError
}

// run is the main worker loop.
func (w *BackgroundWorker) run(ctx context.Context) {
	defer w.wg.Done()

	// Set up interval ticker (nil if disabled)
	var ticker *time.Ticker
	var tickerC <-chan time.Time

	if w.config.IntervalMinutes > 0 {
		interval := time.Duration(w.config.IntervalMinutes) * time.Minute
		ticker = time.NewTicker(interval)
		tickerC = ticker.C
		defer ticker.Stop()
	}

	for {
		select {
		case <-ctx.Done():
			return

		case <-tickerC:
			w.handleIntervalCheckpoint()

		case event := <-w.events:
			w.handleEvent(event)
		}
	}
}

// handleIntervalCheckpoint creates a periodic interval checkpoint.
func (w *BackgroundWorker) handleIntervalCheckpoint() {
	log.Printf("Creating interval checkpoint for session %s", w.sessionName)

	opts := AutoCheckpointOptions{
		SessionName:     w.sessionName,
		Reason:          ReasonInterval,
		Description:     "periodic interval checkpoint",
		ScrollbackLines: w.config.ScrollbackLines,
		IncludeGit:      w.config.IncludeGit,
		MaxCheckpoints:  w.config.MaxCheckpoints,
	}

	w.createCheckpoint(opts)
}

// handleEvent processes an incoming event and creates a checkpoint if configured.
func (w *BackgroundWorker) handleEvent(event AutoEvent) {
	var reason AutoCheckpointReason
	var shouldCheckpoint bool

	switch event.Type {
	case EventRotation:
		if w.config.OnRotation {
			reason = ReasonRotation
			shouldCheckpoint = true
		}
	case EventError:
		if w.config.OnError {
			reason = ReasonError
			shouldCheckpoint = true
		}
	}

	if !shouldCheckpoint {
		return
	}

	log.Printf("Creating %s checkpoint for session %s (agent: %s)",
		reason, w.sessionName, event.AgentID)

	desc := event.Description
	if event.AgentID != "" {
		desc = fmt.Sprintf("agent %s: %s", event.AgentID, desc)
	}

	opts := AutoCheckpointOptions{
		SessionName:     w.sessionName,
		Reason:          reason,
		Description:     desc,
		ScrollbackLines: w.config.ScrollbackLines,
		IncludeGit:      w.config.IncludeGit,
		MaxCheckpoints:  w.config.MaxCheckpoints,
	}

	w.createCheckpoint(opts)
}

// createCheckpoint creates a checkpoint and updates statistics.
func (w *BackgroundWorker) createCheckpoint(opts AutoCheckpointOptions) {
	cp, err := w.checkpointer.Create(opts)

	w.mu.Lock()
	defer w.mu.Unlock()

	if err != nil {
		w.lastError = err
		log.Printf("Error creating auto-checkpoint: %v", err)
		return
	}

	w.checkpointCount++
	w.lastCheckpoint = cp.CreatedAt
	w.lastError = nil
}

// WorkerRegistry manages background workers for multiple sessions.
type WorkerRegistry struct {
	workers map[string]*BackgroundWorker
	mu      sync.RWMutex
}

// NewWorkerRegistry creates a new worker registry.
func NewWorkerRegistry() *WorkerRegistry {
	return &WorkerRegistry{
		workers: make(map[string]*BackgroundWorker),
	}
}

// StartWorker starts a background worker for a session.
// If a worker already exists for the session, it is stopped first.
func (r *WorkerRegistry) StartWorker(ctx context.Context, sessionName string, config AutoCheckpointConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Stop existing worker if any
	if existing, ok := r.workers[sessionName]; ok {
		existing.Stop()
	}

	worker := NewBackgroundWorker(sessionName, config)
	worker.Start(ctx)
	r.workers[sessionName] = worker
}

// StopWorker stops the background worker for a session.
func (r *WorkerRegistry) StopWorker(sessionName string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if worker, ok := r.workers[sessionName]; ok {
		worker.Stop()
		delete(r.workers, sessionName)
	}
}

// StopAll stops all background workers.
func (r *WorkerRegistry) StopAll() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for name, worker := range r.workers {
		worker.Stop()
		delete(r.workers, name)
	}
}

// GetWorker returns the worker for a session, or nil if not found.
func (r *WorkerRegistry) GetWorker(sessionName string) *BackgroundWorker {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.workers[sessionName]
}

// SendEvent sends an event to the worker for a specific session.
// Does nothing if no worker exists for the session.
func (r *WorkerRegistry) SendEvent(sessionName string, event AutoEvent) {
	r.mu.RLock()
	worker := r.workers[sessionName]
	r.mu.RUnlock()

	if worker != nil {
		event.SessionName = sessionName
		worker.SendEvent(event)
	}
}
