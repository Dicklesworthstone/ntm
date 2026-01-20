// Package robot provides machine-readable output for AI agents and automation.
// activity.go implements output velocity tracking for agent activity detection.
package robot

import (
	"context"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// VelocitySample represents a single velocity measurement at a point in time.
type VelocitySample struct {
	Timestamp  time.Time `json:"timestamp"`
	CharsAdded int       `json:"chars_added"`
	Velocity   float64   `json:"velocity"` // chars/sec
}

// VelocityTracker tracks output velocity for a single pane.
// It maintains a sliding window of samples for smoothed velocity calculation.
type VelocityTracker struct {
	PaneID        string           `json:"pane_id"`
	Samples       []VelocitySample `json:"samples"`     // circular buffer, last N samples
	MaxSamples    int              `json:"max_samples"` // size of circular buffer
	LastCapture   string           `json:"-"`           // previous capture (not serialized)
	LastCaptureAt time.Time        `json:"last_capture_at"`

	mu sync.Mutex
}

// DefaultMaxSamples is the default number of samples to keep in the sliding window.
const DefaultMaxSamples = 10

// NewVelocityTracker creates a new velocity tracker for a pane.
func NewVelocityTracker(paneID string) *VelocityTracker {
	return &VelocityTracker{
		PaneID:     paneID,
		Samples:    make([]VelocitySample, 0, DefaultMaxSamples),
		MaxSamples: DefaultMaxSamples,
	}
}

// NewVelocityTrackerWithSize creates a tracker with a custom buffer size.
func NewVelocityTrackerWithSize(paneID string, maxSamples int) *VelocityTracker {
	if maxSamples <= 0 {
		maxSamples = DefaultMaxSamples
	}
	return &VelocityTracker{
		PaneID:     paneID,
		Samples:    make([]VelocitySample, 0, maxSamples),
		MaxSamples: maxSamples,
	}
}

// Update captures the current pane output and calculates velocity.
// It compares the new output to the previous capture to determine chars added.
// Returns the new sample and any error from capture.
func (vt *VelocityTracker) Update() (*VelocitySample, error) {
	vt.mu.Lock()
	defer vt.mu.Unlock()

	return vt.updateLocked()
}

// updateLocked performs the update with the lock already held.
func (vt *VelocityTracker) updateLocked() (*VelocitySample, error) {
	// Capture current pane output
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := tmux.CaptureForStatusDetectionContext(ctx, vt.PaneID)
	if err != nil {
		return nil, err
	}

	now := time.Now()

	// Strip ANSI escape sequences before counting
	cleanOutput := status.StripANSI(output)

	// Count runes (Unicode characters), not bytes
	currentRunes := utf8.RuneCountInString(cleanOutput)
	previousRunes := utf8.RuneCountInString(vt.LastCapture)

	// Calculate chars added
	// Handle shrinking buffer (scroll, clear) by treating negative delta as 0
	charsAdded := currentRunes - previousRunes
	if charsAdded < 0 {
		charsAdded = 0
	}

	// Calculate velocity (chars/sec)
	var velocity float64
	if !vt.LastCaptureAt.IsZero() {
		elapsed := now.Sub(vt.LastCaptureAt).Seconds()
		if elapsed > 0 {
			velocity = float64(charsAdded) / elapsed
		}
	}

	sample := VelocitySample{
		Timestamp:  now,
		CharsAdded: charsAdded,
		Velocity:   velocity,
	}

	// Add sample to circular buffer
	vt.addSampleLocked(sample)

	// Update last capture state
	vt.LastCapture = cleanOutput
	vt.LastCaptureAt = now

	return &sample, nil
}

// addSampleLocked adds a sample to the circular buffer.
// Must be called with mu held.
func (vt *VelocityTracker) addSampleLocked(sample VelocitySample) {
	if len(vt.Samples) >= vt.MaxSamples {
		// Remove oldest sample (shift left)
		copy(vt.Samples, vt.Samples[1:])
		vt.Samples = vt.Samples[:len(vt.Samples)-1]
	}
	vt.Samples = append(vt.Samples, sample)
}

// CurrentVelocity returns the most recent velocity measurement.
// Returns 0 if no samples are available.
func (vt *VelocityTracker) CurrentVelocity() float64 {
	vt.mu.Lock()
	defer vt.mu.Unlock()

	if len(vt.Samples) == 0 {
		return 0
	}
	return vt.Samples[len(vt.Samples)-1].Velocity
}

// AverageVelocity returns the average velocity over all samples in the window.
// This provides a smoothed velocity that's less sensitive to momentary fluctuations.
func (vt *VelocityTracker) AverageVelocity() float64 {
	vt.mu.Lock()
	defer vt.mu.Unlock()

	if len(vt.Samples) == 0 {
		return 0
	}

	var sum float64
	for _, s := range vt.Samples {
		sum += s.Velocity
	}
	return sum / float64(len(vt.Samples))
}

// RecentVelocity returns the average velocity over the last n samples.
// If n is larger than available samples, uses all samples.
func (vt *VelocityTracker) RecentVelocity(n int) float64 {
	vt.mu.Lock()
	defer vt.mu.Unlock()

	if len(vt.Samples) == 0 {
		return 0
	}

	if n <= 0 || n > len(vt.Samples) {
		n = len(vt.Samples)
	}

	var sum float64
	start := len(vt.Samples) - n
	for i := start; i < len(vt.Samples); i++ {
		sum += vt.Samples[i].Velocity
	}
	return sum / float64(n)
}

// LastOutputAge returns the duration since the last output was added.
// Returns 0 if no captures have been made.
func (vt *VelocityTracker) LastOutputAge() time.Duration {
	vt.mu.Lock()
	defer vt.mu.Unlock()

	return vt.lastOutputAgeLocked()
}

// lastOutputAgeLocked returns age without locking (caller must hold lock).
func (vt *VelocityTracker) lastOutputAgeLocked() time.Duration {
	if vt.LastCaptureAt.IsZero() {
		return 0
	}

	// Find the last sample that had output
	for i := len(vt.Samples) - 1; i >= 0; i-- {
		if vt.Samples[i].CharsAdded > 0 {
			return time.Since(vt.Samples[i].Timestamp)
		}
	}

	// No output in any sample - return time since OLDEST sample
	// This approximates "how long we've been monitoring without seeing output"
	// Note: This is limited by MaxSamples buffer size
	if len(vt.Samples) > 0 {
		return time.Since(vt.Samples[0].Timestamp)
	}

	return time.Since(vt.LastCaptureAt)
}

// LastOutputTime returns the timestamp of the most recent output.
// Returns zero time if no output has been captured.
func (vt *VelocityTracker) LastOutputTime() time.Time {
	vt.mu.Lock()
	defer vt.mu.Unlock()

	// Find the last sample that had output
	for i := len(vt.Samples) - 1; i >= 0; i-- {
		if vt.Samples[i].CharsAdded > 0 {
			return vt.Samples[i].Timestamp
		}
	}

	return time.Time{}
}

// SampleCount returns the number of samples currently in the buffer.
func (vt *VelocityTracker) SampleCount() int {
	vt.mu.Lock()
	defer vt.mu.Unlock()
	return len(vt.Samples)
}

// GetSamples returns a copy of all samples in the buffer.
func (vt *VelocityTracker) GetSamples() []VelocitySample {
	vt.mu.Lock()
	defer vt.mu.Unlock()

	result := make([]VelocitySample, len(vt.Samples))
	copy(result, vt.Samples)
	return result
}

// Reset clears all samples and capture state.
func (vt *VelocityTracker) Reset() {
	vt.mu.Lock()
	defer vt.mu.Unlock()

	vt.Samples = vt.Samples[:0]
	vt.LastCapture = ""
	vt.LastCaptureAt = time.Time{}
}

// VelocityManager manages velocity trackers for multiple panes.
type VelocityManager struct {
	trackers map[string]*VelocityTracker
	mu       sync.RWMutex
}

// NewVelocityManager creates a new velocity manager.
func NewVelocityManager() *VelocityManager {
	return &VelocityManager{
		trackers: make(map[string]*VelocityTracker),
	}
}

// GetOrCreate returns the tracker for a pane, creating one if needed.
func (vm *VelocityManager) GetOrCreate(paneID string) *VelocityTracker {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	if tracker, ok := vm.trackers[paneID]; ok {
		return tracker
	}

	tracker := NewVelocityTracker(paneID)
	vm.trackers[paneID] = tracker
	return tracker
}

// Get returns the tracker for a pane, or nil if not found.
func (vm *VelocityManager) Get(paneID string) *VelocityTracker {
	vm.mu.RLock()
	defer vm.mu.RUnlock()
	return vm.trackers[paneID]
}

// Remove removes the tracker for a pane.
func (vm *VelocityManager) Remove(paneID string) {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	delete(vm.trackers, paneID)
}

// UpdateAll updates all registered trackers.
// Returns a map of pane IDs to their current samples, and any errors.
func (vm *VelocityManager) UpdateAll() (map[string]*VelocitySample, map[string]error) {
	vm.mu.RLock()
	paneIDs := make([]string, 0, len(vm.trackers))
	for id := range vm.trackers {
		paneIDs = append(paneIDs, id)
	}
	vm.mu.RUnlock()

	samples := make(map[string]*VelocitySample)
	errors := make(map[string]error)

	for _, paneID := range paneIDs {
		tracker := vm.Get(paneID)
		if tracker == nil {
			continue
		}

		sample, err := tracker.Update()
		if err != nil {
			errors[paneID] = err
		} else {
			samples[paneID] = sample
		}
	}

	return samples, errors
}

// GetAllVelocities returns the current velocity for all tracked panes.
func (vm *VelocityManager) GetAllVelocities() map[string]float64 {
	vm.mu.RLock()
	defer vm.mu.RUnlock()

	result := make(map[string]float64, len(vm.trackers))
	for paneID, tracker := range vm.trackers {
		result[paneID] = tracker.CurrentVelocity()
	}
	return result
}

// Clear removes all trackers.
func (vm *VelocityManager) Clear() {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	vm.trackers = make(map[string]*VelocityTracker)
}

// TrackerCount returns the number of active trackers.
func (vm *VelocityManager) TrackerCount() int {
	vm.mu.RLock()
	defer vm.mu.RUnlock()
	return len(vm.trackers)
}

// =============================================================================
// State Classification
// =============================================================================

// Velocity thresholds for state classification
const (
	// VelocityHighThreshold indicates active generation (chars/sec)
	VelocityHighThreshold = 10.0

	// VelocityMediumThreshold indicates some activity
	VelocityMediumThreshold = 2.0

	// VelocityIdleThreshold below this is considered idle
	VelocityIdleThreshold = 1.0

	// DefaultStallThreshold is the default duration to consider stalled
	DefaultStallThreshold = 30 * time.Second

	// DefaultHysteresisDuration is the minimum time a state must be stable
	DefaultHysteresisDuration = 2 * time.Second

	// MaxStateHistory is the maximum number of state transitions to keep
	MaxStateHistory = 20
)

// StateTransition records a state change for debugging.
type StateTransition struct {
	From       AgentState `json:"from"`
	To         AgentState `json:"to"`
	At         time.Time  `json:"at"`
	Confidence float64    `json:"confidence"`
	Trigger    string     `json:"trigger"` // what caused the transition
}

// AgentActivity represents the current activity state of an agent pane.
type AgentActivity struct {
	PaneID           string            `json:"pane_id"`
	AgentType        string            `json:"agent_type"` // "claude", "codex", "gemini", "*"
	State            AgentState        `json:"state"`
	Confidence       float64           `json:"confidence"` // 0.0-1.0
	Velocity         float64           `json:"velocity"`   // current chars/sec
	StateSince       time.Time         `json:"state_since"`
	DetectedPatterns []string          `json:"detected_patterns,omitempty"`
	LastOutput       time.Time         `json:"last_output,omitempty"`
	StateHistory     []StateTransition `json:"state_history,omitempty"`

	// Hysteresis tracking - prevents rapid state flapping
	PendingState AgentState `json:"pending_state,omitempty"`
	PendingSince time.Time  `json:"pending_since,omitempty"`
}

// StateClassifier combines velocity and pattern signals to classify agent state.
type StateClassifier struct {
	velocityTracker    *VelocityTracker
	patternLibrary     *PatternLibrary
	agentType          string
	stallThreshold     time.Duration
	hysteresisDuration time.Duration

	// Current state tracking
	currentState      AgentState
	stateSince        time.Time
	stateHistory      []StateTransition
	lastPatterns      []string
	lastOutputContent string

	// Hysteresis
	pendingState AgentState
	pendingSince time.Time

	mu sync.Mutex
}

// ClassifierConfig holds configuration for state classification.
type ClassifierConfig struct {
	AgentType          string
	StallThreshold     time.Duration
	HysteresisDuration time.Duration
	PatternLibrary     *PatternLibrary
}

// NewStateClassifier creates a new state classifier for a pane.
func NewStateClassifier(paneID string, cfg *ClassifierConfig) *StateClassifier {
	if cfg == nil {
		cfg = &ClassifierConfig{}
	}

	patternLib := cfg.PatternLibrary
	if patternLib == nil {
		patternLib = DefaultLibrary
	}

	stallThreshold := cfg.StallThreshold
	if stallThreshold <= 0 {
		stallThreshold = DefaultStallThreshold
	}

	hysteresis := cfg.HysteresisDuration
	if hysteresis <= 0 {
		hysteresis = DefaultHysteresisDuration
	}

	return &StateClassifier{
		velocityTracker:    NewVelocityTracker(paneID),
		patternLibrary:     patternLib,
		agentType:          cfg.AgentType,
		stallThreshold:     stallThreshold,
		hysteresisDuration: hysteresis,
		currentState:       StateUnknown,
		stateSince:         time.Now(),
		stateHistory:       make([]StateTransition, 0, MaxStateHistory),
	}
}

// Classify analyzes current pane output and returns the agent's activity state.
func (sc *StateClassifier) Classify() (*AgentActivity, error) {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	// Update velocity tracker
	sample, err := sc.velocityTracker.Update()
	if err != nil {
		return nil, err
	}

	// Get current content for pattern matching
	content := sc.velocityTracker.LastCapture
	velocity := sample.Velocity

	// Detect patterns in content
	var detectedPatterns []string
	matches := sc.patternLibrary.Match(content, sc.agentType)
	for _, m := range matches {
		detectedPatterns = append(detectedPatterns, m.Pattern)
	}
	sc.lastPatterns = detectedPatterns
	sc.lastOutputContent = content

	// Calculate proposed state and confidence
	proposedState, confidence, trigger := sc.classifyState(velocity, matches)

	// Apply hysteresis
	finalState := sc.applyHysteresis(proposedState, confidence, trigger)

	// Build result
	activity := &AgentActivity{
		PaneID:           sc.velocityTracker.PaneID,
		AgentType:        sc.agentType,
		State:            finalState,
		Confidence:       confidence,
		Velocity:         velocity,
		StateSince:       sc.stateSince,
		DetectedPatterns: detectedPatterns,
		StateHistory:     sc.getHistoryCopy(),
		LastOutput:       sc.velocityTracker.LastOutputTime(),
	}

	return activity, nil
}

// classifyState determines state based on velocity and patterns.
// Returns state, confidence, and trigger description.
func (sc *StateClassifier) classifyState(velocity float64, matches []PatternMatch) (AgentState, float64, string) {
	// Error patterns take priority
	for _, m := range matches {
		if m.Category == CategoryError {
			return StateError, 0.95, "error_pattern:" + m.Pattern
		}
	}

	// Check for idle prompt with low velocity
	hasIdlePrompt := false
	for _, m := range matches {
		if m.Category == CategoryIdle {
			hasIdlePrompt = true
			break
		}
	}

	if hasIdlePrompt && velocity < VelocityIdleThreshold {
		return StateWaiting, 0.90, "idle_prompt"
	}

	// Check for thinking indicator
	for _, m := range matches {
		if m.Category == CategoryThinking {
			return StateThinking, 0.80, "thinking_pattern:" + m.Pattern
		}
	}

	// High velocity = generating
	if velocity > VelocityHighThreshold {
		return StateGenerating, 0.85, "high_velocity"
	} else if velocity > VelocityMediumThreshold {
		return StateGenerating, 0.70, "medium_velocity"
	}

	// Stall detection (no output when expected)
	lastOutputAge := sc.velocityTracker.LastOutputAge()
	if velocity == 0 && lastOutputAge > sc.stallThreshold {
		if sc.currentState == StateGenerating {
			return StateStalled, 0.75, "stalled_after_generating"
		}
		return StateWaiting, 0.60, "idle_no_output"
	}

	// Default to unknown
	return StateUnknown, 0.50, "insufficient_signals"
}

// applyHysteresis prevents rapid state flapping.
// ERROR transitions immediately; other states require stability.
func (sc *StateClassifier) applyHysteresis(proposed AgentState, confidence float64, trigger string) AgentState {
	now := time.Now()

	// ERROR state transitions immediately (safety)
	if proposed == StateError {
		if sc.currentState != StateError {
			sc.recordTransition(sc.currentState, StateError, confidence, trigger)
			sc.currentState = StateError
			sc.stateSince = now
		}
		sc.pendingState = ""
		sc.pendingSince = time.Time{}
		return StateError
	}

	// First classification - transition immediately to establish baseline
	// This ensures single-shot queries (like PrintActivity) get useful results
	// rather than always returning UNKNOWN due to hysteresis delay
	if len(sc.stateHistory) == 0 && sc.currentState == StateUnknown && proposed != StateUnknown {
		sc.recordTransition(sc.currentState, proposed, confidence, trigger)
		sc.currentState = proposed
		sc.stateSince = now
		return proposed
	}

	// If state matches current, reset pending
	if proposed == sc.currentState {
		sc.pendingState = ""
		sc.pendingSince = time.Time{}
		return sc.currentState
	}

	// If this is a new pending state, start tracking
	if proposed != sc.pendingState {
		sc.pendingState = proposed
		sc.pendingSince = now
		return sc.currentState
	}

	// Check if pending state has been stable long enough
	if now.Sub(sc.pendingSince) >= sc.hysteresisDuration {
		oldState := sc.currentState
		sc.recordTransition(oldState, proposed, confidence, trigger)
		sc.currentState = proposed
		sc.stateSince = now
		sc.pendingState = ""
		sc.pendingSince = time.Time{}
		return proposed
	}

	// Not stable long enough, keep current state
	return sc.currentState
}

// recordTransition adds a state transition to history.
func (sc *StateClassifier) recordTransition(from, to AgentState, confidence float64, trigger string) {
	transition := StateTransition{
		From:       from,
		To:         to,
		At:         time.Now(),
		Confidence: confidence,
		Trigger:    trigger,
	}

	// Add to history, keeping max size
	if len(sc.stateHistory) >= MaxStateHistory {
		copy(sc.stateHistory, sc.stateHistory[1:])
		sc.stateHistory = sc.stateHistory[:MaxStateHistory-1]
	}
	sc.stateHistory = append(sc.stateHistory, transition)
}

// getHistoryCopy returns a copy of state history.
func (sc *StateClassifier) getHistoryCopy() []StateTransition {
	result := make([]StateTransition, len(sc.stateHistory))
	copy(result, sc.stateHistory)
	return result
}

// CurrentState returns the current classified state.
func (sc *StateClassifier) CurrentState() AgentState {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.currentState
}

// Reset clears all state and history.
func (sc *StateClassifier) Reset() {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	sc.velocityTracker.Reset()
	sc.currentState = StateUnknown
	sc.stateSince = time.Now()
	sc.stateHistory = sc.stateHistory[:0]
	sc.pendingState = ""
	sc.pendingSince = time.Time{}
	sc.lastPatterns = nil
	sc.lastOutputContent = ""
}

// SetAgentType sets the agent type for pattern matching.
func (sc *StateClassifier) SetAgentType(agentType string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.agentType = agentType
}

// GetStateHistory returns the state transition history.
func (sc *StateClassifier) GetStateHistory() []StateTransition {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.getHistoryCopy()
}

// StateDuration returns how long the current state has been active.
func (sc *StateClassifier) StateDuration() time.Duration {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return time.Since(sc.stateSince)
}

// ActivityMonitor manages state classifiers for multiple panes.
type ActivityMonitor struct {
	classifiers map[string]*StateClassifier
	config      *ClassifierConfig
	mu          sync.RWMutex
}

// NewActivityMonitor creates a new activity monitor.
func NewActivityMonitor(cfg *ClassifierConfig) *ActivityMonitor {
	return &ActivityMonitor{
		classifiers: make(map[string]*StateClassifier),
		config:      cfg,
	}
}

// GetOrCreate returns the classifier for a pane, creating one if needed.
func (am *ActivityMonitor) GetOrCreate(paneID string) *StateClassifier {
	am.mu.Lock()
	defer am.mu.Unlock()

	if classifier, ok := am.classifiers[paneID]; ok {
		return classifier
	}

	cfg := am.config
	if cfg == nil {
		cfg = &ClassifierConfig{}
	}

	classifier := NewStateClassifier(paneID, cfg)
	am.classifiers[paneID] = classifier
	return classifier
}

// Get returns the classifier for a pane, or nil if not found.
func (am *ActivityMonitor) Get(paneID string) *StateClassifier {
	am.mu.RLock()
	defer am.mu.RUnlock()
	return am.classifiers[paneID]
}

// Remove removes the classifier for a pane.
func (am *ActivityMonitor) Remove(paneID string) {
	am.mu.Lock()
	defer am.mu.Unlock()
	delete(am.classifiers, paneID)
}

// ClassifyAll updates all classifiers and returns current activities.
func (am *ActivityMonitor) ClassifyAll() (map[string]*AgentActivity, map[string]error) {
	am.mu.RLock()
	paneIDs := make([]string, 0, len(am.classifiers))
	for id := range am.classifiers {
		paneIDs = append(paneIDs, id)
	}
	am.mu.RUnlock()

	activities := make(map[string]*AgentActivity)
	errors := make(map[string]error)

	for _, paneID := range paneIDs {
		classifier := am.Get(paneID)
		if classifier == nil {
			continue
		}

		activity, err := classifier.Classify()
		if err != nil {
			errors[paneID] = err
		} else {
			activities[paneID] = activity
		}
	}

	return activities, errors
}

// GetAllStates returns the current state for all monitored panes.
func (am *ActivityMonitor) GetAllStates() map[string]AgentState {
	am.mu.RLock()
	defer am.mu.RUnlock()

	result := make(map[string]AgentState, len(am.classifiers))
	for paneID, classifier := range am.classifiers {
		result[paneID] = classifier.CurrentState()
	}
	return result
}

// Clear removes all classifiers.
func (am *ActivityMonitor) Clear() {
	am.mu.Lock()
	defer am.mu.Unlock()
	am.classifiers = make(map[string]*StateClassifier)
}

// Count returns the number of active classifiers.
func (am *ActivityMonitor) Count() int {
	am.mu.RLock()
	defer am.mu.RUnlock()
	return len(am.classifiers)
}
