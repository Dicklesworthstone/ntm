// Package state provides durable SQLite-backed storage for NTM orchestration state.
// This file implements agent state event tracking for timeline visualization.
package state

import (
	"fmt"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/audit"
)

// TimelineState represents the operational state of an agent in the timeline.
// This differs from AgentStatus as it includes additional states for timeline tracking.
type TimelineState string

const (
	// TimelineIdle indicates the agent is idle with no active work.
	TimelineIdle TimelineState = "idle"
	// TimelineWorking indicates the agent is actively processing a task.
	TimelineWorking TimelineState = "working"
	// TimelineWaiting indicates the agent is waiting for user input or external resource.
	TimelineWaiting TimelineState = "waiting"
	// TimelineError indicates the agent encountered an error.
	TimelineError TimelineState = "error"
	// TimelineStopped indicates the agent/pane was terminated.
	TimelineStopped TimelineState = "stopped"
)

// String returns the string representation of TimelineState.
func (s TimelineState) String() string {
	return string(s)
}

// MarkerType represents the type of discrete event marker on the timeline.
type MarkerType string

const (
	// MarkerPrompt indicates a prompt was sent to the agent (▶).
	MarkerPrompt MarkerType = "prompt"
	// MarkerCompletion indicates the agent finished a task (✓).
	MarkerCompletion MarkerType = "completion"
	// MarkerError indicates an error occurred (✗).
	MarkerError MarkerType = "error"
	// MarkerStart indicates session/agent start (◆).
	MarkerStart MarkerType = "start"
	// MarkerStop indicates session/agent stop (◆).
	MarkerStop MarkerType = "stop"
)

// String returns the string representation of MarkerType.
func (m MarkerType) String() string {
	return string(m)
}

// Symbol returns the Unicode symbol for the marker type.
func (m MarkerType) Symbol() string {
	switch m {
	case MarkerPrompt:
		return "▶"
	case MarkerCompletion:
		return "✓"
	case MarkerError:
		return "✗"
	case MarkerStart, MarkerStop:
		return "◆"
	default:
		return "•"
	}
}

// TimelineMarker represents a discrete event marker on the timeline.
type TimelineMarker struct {
	// ID uniquely identifies the marker.
	ID string `json:"id"`

	// AgentID identifies which agent this marker belongs to.
	AgentID string `json:"agent_id"`

	// SessionID is the session this marker belongs to.
	SessionID string `json:"session_id"`

	// Type is the kind of marker event.
	Type MarkerType `json:"type"`

	// Timestamp is when the event occurred.
	Timestamp time.Time `json:"timestamp"`

	// Message contains details about the event.
	// For prompts: first N chars of the prompt text.
	// For errors: error message.
	// For start/stop: reason if any.
	Message string `json:"message,omitempty"`

	// Details contains additional metadata about the event.
	Details map[string]string `json:"details,omitempty"`
}

// IsTerminal returns true if the state is a terminal state (stopped/error).
func (s TimelineState) IsTerminal() bool {
	return s == TimelineStopped || s == TimelineError
}

// AgentEvent represents a state transition event for an agent.
type AgentEvent struct {
	// AgentID uniquely identifies the agent (e.g., "cc_1", "cod_2").
	AgentID string `json:"agent_id"`

	// AgentType is the type of agent (cc, cod, gmi).
	AgentType AgentType `json:"agent_type"`

	// SessionID is the session this agent belongs to.
	SessionID string `json:"session_id"`

	// State is the new state the agent transitioned to.
	State TimelineState `json:"state"`

	// PreviousState is the state before this transition (empty for first event).
	PreviousState TimelineState `json:"previous_state,omitempty"`

	// Timestamp is when the state transition occurred.
	Timestamp time.Time `json:"timestamp"`

	// Duration is how long the agent was in the previous state (zero for first event).
	Duration time.Duration `json:"duration,omitempty"`

	// Details contains additional context about the state transition.
	Details map[string]string `json:"details,omitempty"`

	// Trigger describes what caused the state transition.
	Trigger string `json:"trigger,omitempty"`
}

// TimelineConfig configures the TimelineTracker behavior.
type TimelineConfig struct {
	// MaxEventsPerAgent is the maximum number of events to retain per agent.
	// Older events are pruned when this limit is exceeded.
	// Default: 1000
	MaxEventsPerAgent int

	// RetentionDuration is how long to keep events before pruning.
	// Default: 24 hours
	RetentionDuration time.Duration

	// PruneInterval is how often to run the background pruning goroutine.
	// Set to 0 to disable background pruning.
	// Default: 5 minutes
	PruneInterval time.Duration
}

// DefaultTimelineConfig returns the default configuration.
func DefaultTimelineConfig() TimelineConfig {
	return TimelineConfig{
		MaxEventsPerAgent: 1000,
		RetentionDuration: 24 * time.Hour,
		PruneInterval:     5 * time.Minute,
	}
}

// agentTimeline holds the event history for a single agent.
type agentTimeline struct {
	events       []AgentEvent
	currentState TimelineState
	lastSeen     time.Time
}

// TimelineTracker accumulates and manages agent state transition events.
// It is safe for concurrent use.
type TimelineTracker struct {
	mu        sync.RWMutex
	config    TimelineConfig
	timelines map[string]*agentTimeline // keyed by agentID
	allEvents []AgentEvent              // all events in chronological order
	markers   []TimelineMarker          // discrete event markers
	markerSeq int                       // sequence number for marker IDs

	// Callbacks for state change notifications
	onStateChange []func(event AgentEvent)
	onMarkerAdd   []func(marker TimelineMarker)

	// Background pruning
	stopPrune chan struct{}
	pruneWg   sync.WaitGroup
	stopOnce  sync.Once
}

// NewTimelineTracker creates a new TimelineTracker with the given configuration.
// If config is nil, DefaultTimelineConfig() is used.
func NewTimelineTracker(config *TimelineConfig) *TimelineTracker {
	cfg := DefaultTimelineConfig()
	if config != nil {
		if config.MaxEventsPerAgent > 0 {
			cfg.MaxEventsPerAgent = config.MaxEventsPerAgent
		}
		if config.RetentionDuration > 0 {
			cfg.RetentionDuration = config.RetentionDuration
		}
		cfg.PruneInterval = config.PruneInterval
	}

	t := &TimelineTracker{
		config:    cfg,
		timelines: make(map[string]*agentTimeline),
		allEvents: make([]AgentEvent, 0, 1000),
		stopPrune: make(chan struct{}),
	}

	// Start background pruning if interval is set
	if cfg.PruneInterval > 0 {
		t.pruneWg.Add(1)
		go t.backgroundPrune()
	}

	return t
}

// RecordEvent records a new state transition event for an agent.
// If the agent doesn't exist in the tracker, it will be created.
// Returns the recorded event with computed fields (Duration, PreviousState).
func (t *TimelineTracker) RecordEvent(event AgentEvent) AgentEvent {
	t.mu.Lock()

	// Ensure timestamp is set
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	// Get or create agent timeline
	timeline, exists := t.timelines[event.AgentID]
	if !exists {
		timeline = &agentTimeline{
			events:       make([]AgentEvent, 0, 100),
			currentState: "",
		}
		t.timelines[event.AgentID] = timeline
	}

	// Compute previous state and duration
	if len(timeline.events) > 0 {
		lastEvent := timeline.events[len(timeline.events)-1]
		event.PreviousState = lastEvent.State
		event.Duration = event.Timestamp.Sub(lastEvent.Timestamp)
	}

	// Update timeline
	timeline.events = append(timeline.events, event)
	timeline.currentState = event.State
	timeline.lastSeen = event.Timestamp

	// Add to global event list
	t.allEvents = append(t.allEvents, event)

	// Prune if over limit (per agent)
	if len(timeline.events) > t.config.MaxEventsPerAgent {
		excess := len(timeline.events) - t.config.MaxEventsPerAgent
		timeline.events = timeline.events[excess:]
	}

	// Notify callbacks (copy to avoid holding lock during callback)
	callbacks := make([]func(AgentEvent), len(t.onStateChange))
	copy(callbacks, t.onStateChange)

	// Release lock before calling callbacks to avoid deadlock
	t.mu.Unlock()

	// Call callbacks outside the lock; recover from panics to prevent crashes
	for _, cb := range callbacks {
		func() {
			defer func() { _ = recover() }()
			cb(event)
		}()
	}

	return event
}

// GetEvents returns events matching the given criteria.
// If since is zero, all events within retention period are returned.
// Results are sorted chronologically (oldest first).
func (t *TimelineTracker) GetEvents(since time.Time) []AgentEvent {
	t.mu.RLock()
	defer t.mu.RUnlock()

	// Use retention cutoff if since is zero
	cutoff := since
	if cutoff.IsZero() {
		cutoff = time.Now().Add(-t.config.RetentionDuration)
	}

	result := make([]AgentEvent, 0, len(t.allEvents))
	for _, event := range t.allEvents {
		if event.Timestamp.After(cutoff) || event.Timestamp.Equal(cutoff) {
			result = append(result, event)
		}
	}

	return result
}

// GetEventsForAgent returns events for a specific agent since the given time.
func (t *TimelineTracker) GetEventsForAgent(agentID string, since time.Time) []AgentEvent {
	t.mu.RLock()
	defer t.mu.RUnlock()

	timeline, exists := t.timelines[agentID]
	if !exists {
		return nil
	}

	cutoff := since
	if cutoff.IsZero() {
		cutoff = time.Now().Add(-t.config.RetentionDuration)
	}

	result := make([]AgentEvent, 0, len(timeline.events))
	for _, event := range timeline.events {
		if event.Timestamp.After(cutoff) || event.Timestamp.Equal(cutoff) {
			result = append(result, event)
		}
	}

	return result
}

// GetEventsForSession returns events for all agents in a session since the given time.
func (t *TimelineTracker) GetEventsForSession(sessionID string, since time.Time) []AgentEvent {
	t.mu.RLock()
	defer t.mu.RUnlock()

	cutoff := since
	if cutoff.IsZero() {
		cutoff = time.Now().Add(-t.config.RetentionDuration)
	}

	result := make([]AgentEvent, 0)
	for _, event := range t.allEvents {
		if event.SessionID == sessionID && (event.Timestamp.After(cutoff) || event.Timestamp.Equal(cutoff)) {
			result = append(result, event)
		}
	}

	return result
}

// GetCurrentState returns the current state of an agent.
// Returns empty string if the agent is not tracked.
func (t *TimelineTracker) GetCurrentState(agentID string) TimelineState {
	t.mu.RLock()
	defer t.mu.RUnlock()

	timeline, exists := t.timelines[agentID]
	if !exists {
		return ""
	}
	return timeline.currentState
}

// GetAgentStates returns the current state of all tracked agents.
func (t *TimelineTracker) GetAgentStates() map[string]TimelineState {
	t.mu.RLock()
	defer t.mu.RUnlock()

	states := make(map[string]TimelineState, len(t.timelines))
	for agentID, timeline := range t.timelines {
		states[agentID] = timeline.currentState
	}
	return states
}

// GetLastSeen returns when the agent was last seen (last event timestamp).
func (t *TimelineTracker) GetLastSeen(agentID string) time.Time {
	t.mu.RLock()
	defer t.mu.RUnlock()

	timeline, exists := t.timelines[agentID]
	if !exists {
		return time.Time{}
	}
	return timeline.lastSeen
}

// OnStateChange registers a callback to be called when an agent's state changes.
// The callback is called synchronously during RecordEvent.
func (t *TimelineTracker) OnStateChange(callback func(event AgentEvent)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.onStateChange = append(t.onStateChange, callback)
}

// Stats returns statistics about the tracked timelines.
type TimelineStats struct {
	TotalAgents   int            `json:"total_agents"`
	TotalEvents   int            `json:"total_events"`
	EventsByAgent map[string]int `json:"events_by_agent"`
	EventsByState map[string]int `json:"events_by_state"`
	OldestEvent   time.Time      `json:"oldest_event,omitempty"`
	NewestEvent   time.Time      `json:"newest_event,omitempty"`
}

// Stats returns statistics about the timeline tracker.
func (t *TimelineTracker) Stats() TimelineStats {
	t.mu.RLock()
	defer t.mu.RUnlock()

	stats := TimelineStats{
		TotalAgents:   len(t.timelines),
		TotalEvents:   len(t.allEvents),
		EventsByAgent: make(map[string]int),
		EventsByState: make(map[string]int),
	}

	for agentID, timeline := range t.timelines {
		stats.EventsByAgent[agentID] = len(timeline.events)
	}

	for _, event := range t.allEvents {
		stats.EventsByState[string(event.State)]++
	}

	if len(t.allEvents) > 0 {
		stats.OldestEvent = t.allEvents[0].Timestamp
		stats.NewestEvent = t.allEvents[len(t.allEvents)-1].Timestamp
	}

	return stats
}

// Prune removes events older than the retention duration.
// This is called automatically by the background goroutine if PruneInterval > 0.
func (t *TimelineTracker) Prune() int {
	t.mu.Lock()
	defer t.mu.Unlock()

	cutoff := time.Now().Add(-t.config.RetentionDuration)
	pruned := 0

	// Prune per-agent timelines
	for _, timeline := range t.timelines {
		newEvents := make([]AgentEvent, 0, len(timeline.events))
		for _, event := range timeline.events {
			if event.Timestamp.After(cutoff) {
				newEvents = append(newEvents, event)
			} else {
				pruned++
			}
		}
		timeline.events = newEvents
	}

	// Prune global event list
	newAllEvents := make([]AgentEvent, 0, len(t.allEvents))
	for _, event := range t.allEvents {
		if event.Timestamp.After(cutoff) {
			newAllEvents = append(newAllEvents, event)
		}
	}
	t.allEvents = newAllEvents

	return pruned
}

// backgroundPrune runs periodic pruning.
func (t *TimelineTracker) backgroundPrune() {
	defer t.pruneWg.Done()

	ticker := time.NewTicker(t.config.PruneInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			t.Prune()
			t.PruneMarkers()
		case <-t.stopPrune:
			return
		}
	}
}

// Stop stops the background pruning goroutine and cleans up resources.
func (t *TimelineTracker) Stop() {
	t.stopOnce.Do(func() {
		close(t.stopPrune)
	})
	t.pruneWg.Wait()
}

// Clear removes all tracked events and resets the tracker.
func (t *TimelineTracker) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.timelines = make(map[string]*agentTimeline)
	t.allEvents = make([]AgentEvent, 0, 1000)
}

// RemoveAgent removes all events for a specific agent.
func (t *TimelineTracker) RemoveAgent(agentID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.timelines, agentID)

	// Remove from global list
	newAllEvents := make([]AgentEvent, 0, len(t.allEvents))
	for _, event := range t.allEvents {
		if event.AgentID != agentID {
			newAllEvents = append(newAllEvents, event)
		}
	}
	t.allEvents = newAllEvents
}

// ComputeStateDurations calculates how long each agent spent in each state
// within the given time range.
func (t *TimelineTracker) ComputeStateDurations(agentID string, since, until time.Time) map[TimelineState]time.Duration {
	t.mu.RLock()
	defer t.mu.RUnlock()

	timeline, exists := t.timelines[agentID]
	if !exists {
		return nil
	}

	durations := make(map[TimelineState]time.Duration)

	if until.IsZero() {
		until = time.Now()
	}

	events := timeline.events
	for i, event := range events {
		// Skip events before our window
		if event.Timestamp.Before(since) {
			continue
		}

		// Determine the end time for this state
		var endTime time.Time
		if i+1 < len(events) {
			endTime = events[i+1].Timestamp
		} else {
			endTime = until
		}

		// Clamp to window
		if event.Timestamp.Before(since) {
			continue
		}
		if endTime.After(until) {
			endTime = until
		}

		duration := endTime.Sub(event.Timestamp)
		if duration > 0 {
			durations[event.State] += duration
		}
	}

	return durations
}

// GetStateTransitions returns the count of transitions between states.
func (t *TimelineTracker) GetStateTransitions(agentID string) map[string]int {
	t.mu.RLock()
	defer t.mu.RUnlock()

	timeline, exists := t.timelines[agentID]
	if !exists {
		return nil
	}

	transitions := make(map[string]int)
	for _, event := range timeline.events {
		if event.PreviousState != "" {
			key := string(event.PreviousState) + "->" + string(event.State)
			transitions[key]++
		}
	}

	return transitions
}

// AddMarker adds a discrete event marker to the timeline.
// Returns the marker with its assigned ID.
func (t *TimelineTracker) AddMarker(marker TimelineMarker) TimelineMarker {
	t.mu.Lock()

	// Ensure timestamp is set
	if marker.Timestamp.IsZero() {
		marker.Timestamp = time.Now()
	}

	// Assign unique ID if not set
	if marker.ID == "" {
		t.markerSeq++
		marker.ID = fmt.Sprintf("m%d", t.markerSeq)
	}

	t.markers = append(t.markers, marker)

	// Notify callbacks (copy to avoid holding lock during callback)
	callbacks := make([]func(TimelineMarker), len(t.onMarkerAdd))
	copy(callbacks, t.onMarkerAdd)

	// Release lock before calling callbacks to avoid deadlock
	t.mu.Unlock()

	// Call callbacks outside the lock; recover from panics to prevent crashes
	for _, cb := range callbacks {
		func() {
			defer func() { _ = recover() }()
			cb(marker)
		}()
	}

	return marker
}

// GetMarkers returns all markers within the given time range.
// If since is zero, uses retention cutoff. If until is zero, uses now.
func (t *TimelineTracker) GetMarkers(since, until time.Time) []TimelineMarker {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if since.IsZero() {
		since = time.Now().Add(-t.config.RetentionDuration)
	}
	if until.IsZero() {
		until = time.Now()
	}

	result := make([]TimelineMarker, 0, len(t.markers))
	for _, m := range t.markers {
		if (m.Timestamp.After(since) || m.Timestamp.Equal(since)) &&
			(m.Timestamp.Before(until) || m.Timestamp.Equal(until)) {
			result = append(result, m)
		}
	}
	return result
}

// GetMarkersForAgent returns markers for a specific agent within the time range.
func (t *TimelineTracker) GetMarkersForAgent(agentID string, since, until time.Time) []TimelineMarker {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if since.IsZero() {
		since = time.Now().Add(-t.config.RetentionDuration)
	}
	if until.IsZero() {
		until = time.Now()
	}

	result := make([]TimelineMarker, 0)
	for _, m := range t.markers {
		if m.AgentID == agentID &&
			(m.Timestamp.After(since) || m.Timestamp.Equal(since)) &&
			(m.Timestamp.Before(until) || m.Timestamp.Equal(until)) {
			result = append(result, m)
		}
	}
	return result
}

// GetMarkersForSession returns markers for all agents in a session within the time range.
func (t *TimelineTracker) GetMarkersForSession(sessionID string, since, until time.Time) []TimelineMarker {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if since.IsZero() {
		since = time.Now().Add(-t.config.RetentionDuration)
	}
	if until.IsZero() {
		until = time.Now()
	}

	result := make([]TimelineMarker, 0)
	for _, m := range t.markers {
		if m.SessionID == sessionID &&
			(m.Timestamp.After(since) || m.Timestamp.Equal(since)) &&
			(m.Timestamp.Before(until) || m.Timestamp.Equal(until)) {
			result = append(result, m)
		}
	}
	return result
}

// OnMarkerAdd registers a callback to be called when a marker is added.
func (t *TimelineTracker) OnMarkerAdd(callback func(marker TimelineMarker)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.onMarkerAdd = append(t.onMarkerAdd, callback)
}

// PruneMarkers removes markers older than the retention duration.
func (t *TimelineTracker) PruneMarkers() int {
	t.mu.Lock()
	defer t.mu.Unlock()

	cutoff := time.Now().Add(-t.config.RetentionDuration)
	pruned := 0

	newMarkers := make([]TimelineMarker, 0, len(t.markers))
	for _, m := range t.markers {
		if m.Timestamp.After(cutoff) {
			newMarkers = append(newMarkers, m)
		} else {
			pruned++
		}
	}
	t.markers = newMarkers

	return pruned
}

// ClearMarkers removes all markers.
func (t *TimelineTracker) ClearMarkers() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.markers = make([]TimelineMarker, 0)
}

// StateFromAgentStatus converts an AgentStatus to TimelineState.
func StateFromAgentStatus(status AgentStatus) TimelineState {
	switch status {
	case AgentIdle:
		return TimelineIdle
	case AgentWorking:
		return TimelineWorking
	case AgentError:
		return TimelineError
	case AgentCrashed:
		return TimelineStopped
	default:
		return TimelineIdle
	}
}

// Global singleton TimelineTracker for session-wide event tracking.
var (
	globalTimelineTracker     *TimelineTracker
	globalTimelineTrackerOnce sync.Once
)

// GetGlobalTimelineTracker returns the singleton TimelineTracker instance.
// The tracker is initialized on first call with default configuration.
func GetGlobalTimelineTracker() *TimelineTracker {
	globalTimelineTrackerOnce.Do(func() {
		globalTimelineTracker = NewTimelineTracker(&TimelineConfig{
			MaxEventsPerAgent: 10000, // Allow many events for multi-agent sessions
			RetentionDuration: 48 * time.Hour,
			PruneInterval:     15 * time.Minute,
		})
		globalTimelineTracker.OnStateChange(func(event AgentEvent) {
			payload := map[string]interface{}{
				"agent_id":        event.AgentID,
				"agent_type":      string(event.AgentType),
				"state":           event.State.String(),
				"previous_state":  event.PreviousState.String(),
				"duration_ms":     event.Duration.Milliseconds(),
				"trigger":         event.Trigger,
				"event_timestamp": event.Timestamp.Format(time.RFC3339Nano),
			}
			if len(event.Details) > 0 {
				details := make(map[string]interface{}, len(event.Details))
				for k, v := range event.Details {
					details[k] = v
				}
				payload["details"] = details
			}
			_ = audit.LogEvent(event.SessionID, audit.EventTypeStateChange, audit.ActorSystem, event.AgentID, payload, nil)
		})
	})
	return globalTimelineTracker
}
