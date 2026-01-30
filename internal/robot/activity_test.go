package robot

import (
	"strings"
	"testing"
	"time"
)

func TestNewVelocityTracker(t *testing.T) {
	tracker := NewVelocityTracker("test-pane")

	if tracker.PaneID != "test-pane" {
		t.Errorf("expected PaneID 'test-pane', got %q", tracker.PaneID)
	}
	if tracker.MaxSamples != DefaultMaxSamples {
		t.Errorf("expected MaxSamples %d, got %d", DefaultMaxSamples, tracker.MaxSamples)
	}
	if len(tracker.Samples) != 0 {
		t.Errorf("expected empty samples, got %d", len(tracker.Samples))
	}
}

func TestNewVelocityTrackerWithSize(t *testing.T) {
	// Test with valid size
	tracker := NewVelocityTrackerWithSize("test-pane", 5)
	if tracker.MaxSamples != 5 {
		t.Errorf("expected MaxSamples 5, got %d", tracker.MaxSamples)
	}

	// Test with zero size (should default)
	tracker = NewVelocityTrackerWithSize("test-pane", 0)
	if tracker.MaxSamples != DefaultMaxSamples {
		t.Errorf("expected default MaxSamples %d, got %d", DefaultMaxSamples, tracker.MaxSamples)
	}

	// Test with negative size (should default)
	tracker = NewVelocityTrackerWithSize("test-pane", -5)
	if tracker.MaxSamples != DefaultMaxSamples {
		t.Errorf("expected default MaxSamples %d, got %d", DefaultMaxSamples, tracker.MaxSamples)
	}
}

func TestVelocityTracker_AddSample(t *testing.T) {
	tracker := NewVelocityTrackerWithSize("test", 3)

	// Add samples directly for testing
	samples := []VelocitySample{
		{Timestamp: time.Now(), CharsAdded: 10, Velocity: 5.0},
		{Timestamp: time.Now(), CharsAdded: 20, Velocity: 10.0},
		{Timestamp: time.Now(), CharsAdded: 30, Velocity: 15.0},
	}

	for _, s := range samples {
		tracker.mu.Lock()
		tracker.addSampleLocked(s)
		tracker.mu.Unlock()
	}

	if len(tracker.Samples) != 3 {
		t.Errorf("expected 3 samples, got %d", len(tracker.Samples))
	}

	// Add one more to test circular buffer behavior
	newSample := VelocitySample{Timestamp: time.Now(), CharsAdded: 40, Velocity: 20.0}
	tracker.mu.Lock()
	tracker.addSampleLocked(newSample)
	tracker.mu.Unlock()

	if len(tracker.Samples) != 3 {
		t.Errorf("expected 3 samples after overflow, got %d", len(tracker.Samples))
	}

	// First sample should now be the second original
	if tracker.Samples[0].Velocity != 10.0 {
		t.Errorf("expected first sample velocity 10.0, got %f", tracker.Samples[0].Velocity)
	}

	// Last sample should be the new one
	if tracker.Samples[2].Velocity != 20.0 {
		t.Errorf("expected last sample velocity 20.0, got %f", tracker.Samples[2].Velocity)
	}
}

func TestVelocityTracker_CurrentVelocity(t *testing.T) {
	tracker := NewVelocityTracker("test")

	// Empty tracker should return 0
	if v := tracker.CurrentVelocity(); v != 0 {
		t.Errorf("expected 0 velocity for empty tracker, got %f", v)
	}

	// Add samples
	tracker.mu.Lock()
	tracker.addSampleLocked(VelocitySample{Velocity: 5.0})
	tracker.addSampleLocked(VelocitySample{Velocity: 10.0})
	tracker.mu.Unlock()

	// Should return last sample's velocity
	if v := tracker.CurrentVelocity(); v != 10.0 {
		t.Errorf("expected velocity 10.0, got %f", v)
	}
}

func TestVelocityTracker_AverageVelocity(t *testing.T) {
	tracker := NewVelocityTracker("test")

	// Empty tracker should return 0
	if v := tracker.AverageVelocity(); v != 0 {
		t.Errorf("expected 0 average for empty tracker, got %f", v)
	}

	// Add samples with known velocities
	tracker.mu.Lock()
	tracker.addSampleLocked(VelocitySample{Velocity: 5.0})
	tracker.addSampleLocked(VelocitySample{Velocity: 10.0})
	tracker.addSampleLocked(VelocitySample{Velocity: 15.0})
	tracker.mu.Unlock()

	// Average should be (5 + 10 + 15) / 3 = 10
	expected := 10.0
	if v := tracker.AverageVelocity(); v != expected {
		t.Errorf("expected average %f, got %f", expected, v)
	}
}

func TestVelocityTracker_RecentVelocity(t *testing.T) {
	tracker := NewVelocityTracker("test")

	// Empty tracker should return 0
	if v := tracker.RecentVelocity(2); v != 0 {
		t.Errorf("expected 0 for empty tracker, got %f", v)
	}

	// Add samples
	tracker.mu.Lock()
	tracker.addSampleLocked(VelocitySample{Velocity: 5.0})
	tracker.addSampleLocked(VelocitySample{Velocity: 10.0})
	tracker.addSampleLocked(VelocitySample{Velocity: 15.0})
	tracker.addSampleLocked(VelocitySample{Velocity: 20.0})
	tracker.mu.Unlock()

	// Recent 2 should be (15 + 20) / 2 = 17.5
	if v := tracker.RecentVelocity(2); v != 17.5 {
		t.Errorf("expected 17.5, got %f", v)
	}

	// Recent 0 or negative should use all samples
	expected := (5.0 + 10.0 + 15.0 + 20.0) / 4.0
	if v := tracker.RecentVelocity(0); v != expected {
		t.Errorf("expected %f for n=0, got %f", expected, v)
	}

	// Recent larger than samples should use all
	if v := tracker.RecentVelocity(100); v != expected {
		t.Errorf("expected %f for n=100, got %f", expected, v)
	}
}

func TestVelocityTracker_SampleCount(t *testing.T) {
	tracker := NewVelocityTrackerWithSize("test", 5)

	if c := tracker.SampleCount(); c != 0 {
		t.Errorf("expected 0 samples, got %d", c)
	}

	tracker.mu.Lock()
	tracker.addSampleLocked(VelocitySample{Velocity: 1.0})
	tracker.addSampleLocked(VelocitySample{Velocity: 2.0})
	tracker.mu.Unlock()

	if c := tracker.SampleCount(); c != 2 {
		t.Errorf("expected 2 samples, got %d", c)
	}
}

func TestVelocityTracker_GetSamples(t *testing.T) {
	tracker := NewVelocityTracker("test")

	tracker.mu.Lock()
	tracker.addSampleLocked(VelocitySample{Velocity: 5.0})
	tracker.addSampleLocked(VelocitySample{Velocity: 10.0})
	tracker.mu.Unlock()

	samples := tracker.GetSamples()

	// Should be a copy
	if len(samples) != 2 {
		t.Errorf("expected 2 samples, got %d", len(samples))
	}

	// Modifying copy shouldn't affect original
	samples[0].Velocity = 999.0
	if tracker.Samples[0].Velocity == 999.0 {
		t.Error("GetSamples should return a copy, not the original")
	}
}

func TestVelocityTracker_LastOutputAge(t *testing.T) {
	tracker := NewVelocityTracker("test")

	// Before any captures, should return 0
	if age := tracker.LastOutputAge(); age != 0 {
		t.Errorf("expected 0 age before captures, got %v", age)
	}

	// Add samples with no output (CharsAdded = 0)
	oldTime := time.Now().Add(-5 * time.Second)
	tracker.mu.Lock()
	tracker.Samples = append(tracker.Samples, VelocitySample{
		Timestamp:  oldTime,
		CharsAdded: 0,
		Velocity:   0,
	})
	tracker.LastCaptureAt = time.Now()
	tracker.mu.Unlock()

	// With no output, should return time since oldest sample (approx 5 seconds)
	age := tracker.LastOutputAge()
	if age < 4*time.Second || age > 6*time.Second {
		t.Errorf("expected ~5s age with no output, got %v", age)
	}

	// Now add a sample with output
	recentTime := time.Now().Add(-1 * time.Second)
	tracker.mu.Lock()
	tracker.Samples = append(tracker.Samples, VelocitySample{
		Timestamp:  recentTime,
		CharsAdded: 10,
		Velocity:   5.0,
	})
	tracker.mu.Unlock()

	// Should now return time since the sample with output (approx 1 second)
	age = tracker.LastOutputAge()
	if age > 2*time.Second {
		t.Errorf("expected ~1s age after output, got %v", age)
	}
}

func TestVelocityTracker_LastOutputTime(t *testing.T) {
	tracker := NewVelocityTracker("test")

	// Before any output, should return zero time
	if lt := tracker.LastOutputTime(); !lt.IsZero() {
		t.Errorf("expected zero time before output, got %v", lt)
	}

	// Add sample with output
	outputTime := time.Now().Add(-2 * time.Second)
	tracker.mu.Lock()
	tracker.Samples = append(tracker.Samples, VelocitySample{
		Timestamp:  outputTime,
		CharsAdded: 10,
		Velocity:   5.0,
	})
	tracker.mu.Unlock()

	// Should return the timestamp of that sample
	lt := tracker.LastOutputTime()
	if lt != outputTime {
		t.Errorf("expected %v, got %v", outputTime, lt)
	}
}

func TestVelocityTracker_Reset(t *testing.T) {
	tracker := NewVelocityTracker("test")

	// Add some state
	tracker.mu.Lock()
	tracker.addSampleLocked(VelocitySample{Velocity: 5.0})
	tracker.LastCapture = "some content"
	tracker.LastCaptureAt = time.Now()
	tracker.mu.Unlock()

	tracker.Reset()

	if len(tracker.Samples) != 0 {
		t.Errorf("expected empty samples after reset, got %d", len(tracker.Samples))
	}
	if tracker.LastCapture != "" {
		t.Errorf("expected empty LastCapture after reset")
	}
	if !tracker.LastCaptureAt.IsZero() {
		t.Errorf("expected zero LastCaptureAt after reset")
	}
}

func TestVelocityManager_GetOrCreate(t *testing.T) {
	vm := NewVelocityManager()

	// First call should create
	tracker1 := vm.GetOrCreate("pane1")
	if tracker1 == nil {
		t.Fatal("expected tracker, got nil")
	}
	if tracker1.PaneID != "pane1" {
		t.Errorf("expected pane1, got %s", tracker1.PaneID)
	}

	// Second call should return same tracker
	tracker2 := vm.GetOrCreate("pane1")
	if tracker1 != tracker2 {
		t.Error("expected same tracker instance")
	}

	// Different pane should create new tracker
	tracker3 := vm.GetOrCreate("pane2")
	if tracker3 == tracker1 {
		t.Error("expected different tracker for different pane")
	}
}

func TestVelocityManager_Get(t *testing.T) {
	vm := NewVelocityManager()

	// Non-existent should return nil
	if tracker := vm.Get("nonexistent"); tracker != nil {
		t.Error("expected nil for non-existent pane")
	}

	// Create and get
	vm.GetOrCreate("pane1")
	if tracker := vm.Get("pane1"); tracker == nil {
		t.Error("expected tracker, got nil")
	}
}

func TestVelocityManager_Remove(t *testing.T) {
	vm := NewVelocityManager()

	vm.GetOrCreate("pane1")
	vm.GetOrCreate("pane2")

	if vm.TrackerCount() != 2 {
		t.Errorf("expected 2 trackers, got %d", vm.TrackerCount())
	}

	vm.Remove("pane1")

	if vm.TrackerCount() != 1 {
		t.Errorf("expected 1 tracker after remove, got %d", vm.TrackerCount())
	}

	if vm.Get("pane1") != nil {
		t.Error("expected nil after remove")
	}

	if vm.Get("pane2") == nil {
		t.Error("expected pane2 to still exist")
	}
}

func TestVelocityManager_GetAllVelocities(t *testing.T) {
	vm := NewVelocityManager()

	tracker1 := vm.GetOrCreate("pane1")
	tracker2 := vm.GetOrCreate("pane2")

	// Add samples with known velocities
	tracker1.mu.Lock()
	tracker1.addSampleLocked(VelocitySample{Velocity: 5.0})
	tracker1.mu.Unlock()

	tracker2.mu.Lock()
	tracker2.addSampleLocked(VelocitySample{Velocity: 10.0})
	tracker2.mu.Unlock()

	velocities := vm.GetAllVelocities()

	if len(velocities) != 2 {
		t.Errorf("expected 2 velocities, got %d", len(velocities))
	}

	if velocities["pane1"] != 5.0 {
		t.Errorf("expected pane1 velocity 5.0, got %f", velocities["pane1"])
	}

	if velocities["pane2"] != 10.0 {
		t.Errorf("expected pane2 velocity 10.0, got %f", velocities["pane2"])
	}
}

func TestVelocityManager_Clear(t *testing.T) {
	vm := NewVelocityManager()

	vm.GetOrCreate("pane1")
	vm.GetOrCreate("pane2")
	vm.GetOrCreate("pane3")

	vm.Clear()

	if vm.TrackerCount() != 0 {
		t.Errorf("expected 0 trackers after clear, got %d", vm.TrackerCount())
	}
}

func TestVelocityManager_TrackerCount(t *testing.T) {
	vm := NewVelocityManager()

	if vm.TrackerCount() != 0 {
		t.Errorf("expected 0, got %d", vm.TrackerCount())
	}

	vm.GetOrCreate("pane1")
	if vm.TrackerCount() != 1 {
		t.Errorf("expected 1, got %d", vm.TrackerCount())
	}

	vm.GetOrCreate("pane2")
	vm.GetOrCreate("pane3")
	if vm.TrackerCount() != 3 {
		t.Errorf("expected 3, got %d", vm.TrackerCount())
	}
}

// =============================================================================
// State Classification Tests
// =============================================================================

func TestNewStateClassifier(t *testing.T) {
	sc := NewStateClassifier("test-pane", nil)

	if sc.velocityTracker == nil {
		t.Error("velocity tracker should be initialized")
	}
	if sc.patternLibrary == nil {
		t.Error("pattern library should be initialized")
	}
	if sc.currentState != StateUnknown {
		t.Errorf("expected initial state UNKNOWN, got %s", sc.currentState)
	}
	if sc.stallThreshold != DefaultStallThreshold {
		t.Errorf("expected default stall threshold")
	}
	if sc.hysteresisDuration != DefaultHysteresisDuration {
		t.Errorf("expected default hysteresis duration")
	}
}

func TestNewStateClassifierWithConfig(t *testing.T) {
	cfg := &ClassifierConfig{
		AgentType:          "claude",
		StallThreshold:     time.Minute,
		HysteresisDuration: 5 * time.Second,
	}

	sc := NewStateClassifier("test-pane", cfg)

	if sc.agentType != "claude" {
		t.Errorf("expected agent type 'claude', got %s", sc.agentType)
	}
	if sc.stallThreshold != time.Minute {
		t.Errorf("expected stall threshold 1m, got %v", sc.stallThreshold)
	}
	if sc.hysteresisDuration != 5*time.Second {
		t.Errorf("expected hysteresis 5s, got %v", sc.hysteresisDuration)
	}
}

func TestStateClassifier_classifyState(t *testing.T) {
	sc := NewStateClassifier("test", nil)

	tests := []struct {
		name        string
		velocity    float64
		matches     []PatternMatch
		wantState   AgentState
		wantMinConf float64
	}{
		{
			name:        "error_pattern",
			velocity:    0,
			matches:     []PatternMatch{{Pattern: "rate_limit", Category: CategoryError}},
			wantState:   StateError,
			wantMinConf: 0.95,
		},
		{
			name:        "idle_prompt_low_velocity",
			velocity:    0.5,
			matches:     []PatternMatch{{Pattern: "claude_prompt", Category: CategoryIdle}},
			wantState:   StateWaiting,
			wantMinConf: 0.90,
		},
		{
			name:        "thinking_pattern",
			velocity:    0,
			matches:     []PatternMatch{{Pattern: "thinking_text", Category: CategoryThinking}},
			wantState:   StateThinking,
			wantMinConf: 0.80,
		},
		{
			name:        "high_velocity",
			velocity:    15.0,
			matches:     nil,
			wantState:   StateGenerating,
			wantMinConf: 0.85,
		},
		{
			name:        "medium_velocity",
			velocity:    5.0,
			matches:     nil,
			wantState:   StateGenerating,
			wantMinConf: 0.70,
		},
		{
			name:        "unknown_insufficient_signals",
			velocity:    0.5,
			matches:     nil,
			wantState:   StateUnknown,
			wantMinConf: 0.50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, conf, _ := sc.classifyState(tt.velocity, tt.matches)

			if state != tt.wantState {
				t.Errorf("expected state %s, got %s", tt.wantState, state)
			}
			if conf < tt.wantMinConf {
				t.Errorf("expected confidence >= %f, got %f", tt.wantMinConf, conf)
			}
		})
	}
}

func TestStateClassifier_applyHysteresis_ErrorImmediate(t *testing.T) {
	sc := NewStateClassifier("test", nil)

	// Set initial state
	sc.currentState = StateGenerating
	sc.stateSince = time.Now().Add(-time.Hour)

	// Error should transition immediately
	result := sc.applyHysteresis(StateError, 0.95, "test_error")

	if result != StateError {
		t.Errorf("expected immediate ERROR transition, got %s", result)
	}
	if sc.currentState != StateError {
		t.Errorf("current state should be ERROR")
	}
	if len(sc.stateHistory) != 1 {
		t.Errorf("expected 1 transition in history, got %d", len(sc.stateHistory))
	}
}

func TestStateClassifier_applyHysteresis_FirstClassificationImmediate(t *testing.T) {
	sc := NewStateClassifier("test", nil)

	// First classification should transition immediately (except to UNKNOWN)
	// This ensures single-shot queries like PrintActivity get useful results
	result := sc.applyHysteresis(StateWaiting, 0.90, "idle_prompt")

	if result != StateWaiting {
		t.Errorf("expected immediate transition to WAITING on first classification, got %s", result)
	}
	if sc.currentState != StateWaiting {
		t.Errorf("current state should be WAITING, got %s", sc.currentState)
	}
	if len(sc.stateHistory) != 1 {
		t.Errorf("expected 1 transition in history, got %d", len(sc.stateHistory))
	}

	// After first classification, hysteresis should apply normally
	// Reset for second test
	sc2 := NewStateClassifier("test2", nil)

	// First call with GENERATING
	result = sc2.applyHysteresis(StateGenerating, 0.85, "high_velocity")
	if result != StateGenerating {
		t.Errorf("expected immediate GENERATING on first call, got %s", result)
	}

	// Second call with WAITING should NOT transition immediately (hysteresis)
	result = sc2.applyHysteresis(StateWaiting, 0.90, "idle_prompt")
	if result != StateGenerating {
		t.Errorf("expected hysteresis to keep GENERATING, got %s", result)
	}
	if sc2.pendingState != StateWaiting {
		t.Errorf("expected WAITING as pending state, got %s", sc2.pendingState)
	}
}

func TestStateClassifier_applyHysteresis_RequiresStability(t *testing.T) {
	sc := NewStateClassifier("test", &ClassifierConfig{
		HysteresisDuration: 2 * time.Second,
	})

	// Set initial state
	sc.currentState = StateGenerating

	// First call starts pending
	result := sc.applyHysteresis(StateWaiting, 0.90, "idle")
	if result != StateGenerating {
		t.Error("should stay in GENERATING until stable")
	}
	if sc.pendingState != StateWaiting {
		t.Error("should have WAITING as pending")
	}

	// Should still be GENERATING
	result = sc.applyHysteresis(StateWaiting, 0.90, "idle")
	if result != StateGenerating {
		t.Error("should still be GENERATING (hysteresis not elapsed)")
	}

	// Simulate time passing (set pendingSince to past)
	sc.pendingSince = time.Now().Add(-3 * time.Second)

	// Now should transition
	result = sc.applyHysteresis(StateWaiting, 0.90, "idle")
	if result != StateWaiting {
		t.Errorf("expected WAITING after hysteresis, got %s", result)
	}
}

func TestStateClassifier_recordTransition(t *testing.T) {
	sc := NewStateClassifier("test", nil)

	// Record transitions up to max
	for i := 0; i < MaxStateHistory+5; i++ {
		sc.recordTransition(StateGenerating, StateWaiting, 0.90, "test")
	}

	// Should be capped at MaxStateHistory
	if len(sc.stateHistory) != MaxStateHistory {
		t.Errorf("expected %d transitions, got %d", MaxStateHistory, len(sc.stateHistory))
	}
}

func TestStateClassifier_CurrentState(t *testing.T) {
	sc := NewStateClassifier("test", nil)

	if sc.CurrentState() != StateUnknown {
		t.Errorf("expected UNKNOWN, got %s", sc.CurrentState())
	}

	sc.currentState = StateGenerating
	if sc.CurrentState() != StateGenerating {
		t.Errorf("expected GENERATING, got %s", sc.CurrentState())
	}
}

func TestStateClassifier_Reset(t *testing.T) {
	sc := NewStateClassifier("test", nil)

	// Add some state
	sc.currentState = StateGenerating
	sc.recordTransition(StateUnknown, StateGenerating, 0.85, "test")
	sc.pendingState = StateWaiting
	sc.lastPatterns = []string{"pattern1"}

	sc.Reset()

	if sc.currentState != StateUnknown {
		t.Errorf("expected UNKNOWN after reset")
	}
	if len(sc.stateHistory) != 0 {
		t.Error("expected empty history after reset")
	}
	if sc.pendingState != "" {
		t.Error("expected empty pending state after reset")
	}
	if sc.lastPatterns != nil {
		t.Error("expected nil patterns after reset")
	}
}

func TestStateClassifier_SetAgentType(t *testing.T) {
	sc := NewStateClassifier("test", nil)

	sc.SetAgentType("codex")
	if sc.agentType != "codex" {
		t.Errorf("expected 'codex', got %s", sc.agentType)
	}
}

func TestStateClassifier_GetStateHistory(t *testing.T) {
	sc := NewStateClassifier("test", nil)

	sc.recordTransition(StateUnknown, StateGenerating, 0.85, "test1")
	sc.recordTransition(StateGenerating, StateWaiting, 0.90, "test2")

	history := sc.GetStateHistory()

	if len(history) != 2 {
		t.Errorf("expected 2 transitions, got %d", len(history))
	}

	// Modifying copy shouldn't affect original
	history = nil
	if len(sc.stateHistory) != 2 {
		t.Error("GetStateHistory should return a copy")
	}
}

func TestStateClassifier_StateDuration(t *testing.T) {
	sc := NewStateClassifier("test", nil)
	sc.stateSince = time.Now().Add(-10 * time.Second)

	duration := sc.StateDuration()

	// Should be approximately 10 seconds (allow some margin)
	if duration < 9*time.Second || duration > 11*time.Second {
		t.Errorf("expected ~10s duration, got %v", duration)
	}
}

func TestActivityMonitor_GetOrCreate(t *testing.T) {
	am := NewActivityMonitor(nil)

	sc1 := am.GetOrCreate("pane1")
	if sc1 == nil {
		t.Fatal("expected classifier, got nil")
	}

	sc2 := am.GetOrCreate("pane1")
	if sc1 != sc2 {
		t.Error("should return same classifier")
	}

	sc3 := am.GetOrCreate("pane2")
	if sc3 == sc1 {
		t.Error("should create new classifier for different pane")
	}
}

func TestActivityMonitor_Get(t *testing.T) {
	am := NewActivityMonitor(nil)

	// Non-existent
	if am.Get("nonexistent") != nil {
		t.Error("expected nil for non-existent")
	}

	am.GetOrCreate("pane1")
	if am.Get("pane1") == nil {
		t.Error("expected classifier")
	}
}

func TestActivityMonitor_Remove(t *testing.T) {
	am := NewActivityMonitor(nil)

	am.GetOrCreate("pane1")
	am.GetOrCreate("pane2")

	am.Remove("pane1")

	if am.Get("pane1") != nil {
		t.Error("pane1 should be removed")
	}
	if am.Get("pane2") == nil {
		t.Error("pane2 should still exist")
	}
}

func TestActivityMonitor_GetAllStates(t *testing.T) {
	am := NewActivityMonitor(nil)

	sc1 := am.GetOrCreate("pane1")
	sc2 := am.GetOrCreate("pane2")

	sc1.mu.Lock()
	sc1.currentState = StateGenerating
	sc1.mu.Unlock()

	sc2.mu.Lock()
	sc2.currentState = StateWaiting
	sc2.mu.Unlock()

	states := am.GetAllStates()

	if states["pane1"] != StateGenerating {
		t.Errorf("expected GENERATING, got %s", states["pane1"])
	}
	if states["pane2"] != StateWaiting {
		t.Errorf("expected WAITING, got %s", states["pane2"])
	}
}

func TestActivityMonitor_Clear(t *testing.T) {
	am := NewActivityMonitor(nil)

	am.GetOrCreate("pane1")
	am.GetOrCreate("pane2")

	am.Clear()

	if am.Count() != 0 {
		t.Errorf("expected 0 after clear, got %d", am.Count())
	}
}

func TestActivityMonitor_Count(t *testing.T) {
	am := NewActivityMonitor(nil)

	if am.Count() != 0 {
		t.Errorf("expected 0, got %d", am.Count())
	}

	am.GetOrCreate("pane1")
	am.GetOrCreate("pane2")

	if am.Count() != 2 {
		t.Errorf("expected 2, got %d", am.Count())
	}
}

// =============================================================================
// Activity API Tests
// =============================================================================

func TestActivityOptions(t *testing.T) {
	opts := ActivityOptions{
		Session:    "test-session",
		Panes:      []string{"1", "2"},
		AgentTypes: []string{"claude", "codex"},
	}

	if opts.Session != "test-session" {
		t.Errorf("expected session test-session, got %s", opts.Session)
	}
	if len(opts.Panes) != 2 {
		t.Errorf("expected 2 panes, got %d", len(opts.Panes))
	}
	if len(opts.AgentTypes) != 2 {
		t.Errorf("expected 2 agent types, got %d", len(opts.AgentTypes))
	}
}

func TestActivityOutput(t *testing.T) {
	output := ActivityOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       "test",
		Agents:        []AgentActivityInfo{},
		Summary: ActivitySummary{
			TotalAgents: 0,
			ByState:     make(map[string]int),
		},
	}

	if !output.Success {
		t.Error("expected success to be true")
	}
	if output.Session != "test" {
		t.Errorf("expected session test, got %s", output.Session)
	}
}

func TestAgentActivityInfo(t *testing.T) {
	info := AgentActivityInfo{
		Pane:             "1",
		PaneIdx:          1,
		AgentType:        "claude",
		State:            "WAITING",
		Confidence:       0.85,
		Velocity:         0.0,
		DetectedPatterns: []string{"claude_prompt"},
	}

	if info.Pane != "1" {
		t.Errorf("expected pane 1, got %s", info.Pane)
	}
	if info.State != "WAITING" {
		t.Errorf("expected state WAITING, got %s", info.State)
	}
	if info.Confidence != 0.85 {
		t.Errorf("expected confidence 0.85, got %f", info.Confidence)
	}
}

func TestActivitySummary(t *testing.T) {
	summary := ActivitySummary{
		TotalAgents: 4,
		ByState: map[string]int{
			"WAITING":    2,
			"GENERATING": 1,
			"ERROR":      1,
		},
	}

	if summary.TotalAgents != 4 {
		t.Errorf("expected 4 total agents, got %d", summary.TotalAgents)
	}
	if summary.ByState["WAITING"] != 2 {
		t.Errorf("expected 2 waiting, got %d", summary.ByState["WAITING"])
	}
}

func TestGenerateActivityHints(t *testing.T) {
	tests := []struct {
		name            string
		available       []string
		busy            []string
		problem         []string
		summary         ActivitySummary
		wantSummaryHas  string
		wantSuggestions int
	}{
		{
			name:      "no_agents",
			available: []string{},
			busy:      []string{},
			problem:   []string{},
			summary: ActivitySummary{
				TotalAgents: 0,
				ByState:     map[string]int{},
			},
			wantSummaryHas:  "No agents",
			wantSuggestions: 1,
		},
		{
			name:      "all_available",
			available: []string{"1", "2"},
			busy:      []string{},
			problem:   []string{},
			summary: ActivitySummary{
				TotalAgents: 2,
				ByState:     map[string]int{"WAITING": 2},
			},
			wantSummaryHas:  "2 available",
			wantSuggestions: 2, // "all idle" + "send work"
		},
		{
			name:      "all_busy",
			available: []string{},
			busy:      []string{"1", "2"},
			problem:   []string{},
			summary: ActivitySummary{
				TotalAgents: 2,
				ByState:     map[string]int{"GENERATING": 2},
			},
			wantSummaryHas:  "2 busy",
			wantSuggestions: 1, // "all busy"
		},
		{
			name:      "with_problems",
			available: []string{"1"},
			busy:      []string{"2"},
			problem:   []string{"3"},
			summary: ActivitySummary{
				TotalAgents: 3,
				ByState:     map[string]int{"WAITING": 1, "GENERATING": 1, "ERROR": 1},
			},
			wantSummaryHas:  "1 problems",
			wantSuggestions: 2, // "check error" + "send work"
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hints := generateActivityHints(tt.available, tt.busy, tt.problem, tt.summary)

			if hints == nil {
				t.Fatal("expected hints, got nil")
			}

			if tt.wantSummaryHas != "" && !strings.Contains(hints.Summary, tt.wantSummaryHas) {
				t.Errorf("expected summary to contain %q, got %q", tt.wantSummaryHas, hints.Summary)
			}

			if len(hints.SuggestedActions) < tt.wantSuggestions {
				t.Errorf("expected at least %d suggestions, got %d: %v",
					tt.wantSuggestions, len(hints.SuggestedActions), hints.SuggestedActions)
			}
		})
	}
}

func TestNormalizeAgentType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"claude", "claude"},
		{"Claude", "claude"},
		{"cc", "claude"},
		{"claude-code", "claude"},
		{"codex", "codex"},
		{"cod", "codex"},
		{"codex-cli", "codex"},
		{"gemini", "gemini"},
		{"gmi", "gemini"},
		{"gemini-cli", "gemini"},
		{"cursor", "cursor"},
		{"Unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeAgentType(tt.input)
			if got != tt.want {
				t.Errorf("normalizeAgentType(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestActivityAgentHints(t *testing.T) {
	hints := &ActivityAgentHints{
		Summary:          "3 agents: 1 available, 1 busy, 1 problems",
		AvailableAgents:  []string{"1"},
		BusyAgents:       []string{"2"},
		ProblemAgents:    []string{"3"},
		SuggestedActions: []string{"Check error agents", "Send work"},
	}

	if hints.Summary == "" {
		t.Error("expected summary to be set")
	}
	if len(hints.AvailableAgents) != 1 {
		t.Errorf("expected 1 available agent, got %d", len(hints.AvailableAgents))
	}
	if len(hints.BusyAgents) != 1 {
		t.Errorf("expected 1 busy agent, got %d", len(hints.BusyAgents))
	}
	if len(hints.ProblemAgents) != 1 {
		t.Errorf("expected 1 problem agent, got %d", len(hints.ProblemAgents))
	}
}

// =============================================================================
// Edge Case Tests for Activity Detection
// =============================================================================

func TestVelocitySampleStruct(t *testing.T) {
	t.Parallel()

	now := time.Now()
	sample := VelocitySample{
		Timestamp:  now,
		CharsAdded: 100,
		Velocity:   15.5,
	}

	if sample.Timestamp != now {
		t.Error("timestamp not set correctly")
	}
	if sample.CharsAdded != 100 {
		t.Errorf("expected 100 chars added, got %d", sample.CharsAdded)
	}
	if sample.Velocity != 15.5 {
		t.Errorf("expected velocity 15.5, got %f", sample.Velocity)
	}
}

func TestVelocitySampleZeroValues(t *testing.T) {
	t.Parallel()

	var sample VelocitySample

	if !sample.Timestamp.IsZero() {
		t.Error("zero sample should have zero timestamp")
	}
	if sample.CharsAdded != 0 {
		t.Errorf("zero sample should have 0 chars, got %d", sample.CharsAdded)
	}
	if sample.Velocity != 0 {
		t.Errorf("zero sample should have 0 velocity, got %f", sample.Velocity)
	}
}

func TestStateTransitionStruct(t *testing.T) {
	t.Parallel()

	transition := StateTransition{
		From:       StateGenerating,
		To:         StateWaiting,
		At:         time.Now(),
		Confidence: 0.95,
		Trigger:    "idle_prompt",
	}

	if transition.From != StateGenerating {
		t.Errorf("expected From GENERATING, got %s", transition.From)
	}
	if transition.To != StateWaiting {
		t.Errorf("expected To WAITING, got %s", transition.To)
	}
	if transition.Confidence != 0.95 {
		t.Errorf("expected confidence 0.95, got %f", transition.Confidence)
	}
	if transition.Trigger != "idle_prompt" {
		t.Errorf("expected trigger idle_prompt, got %s", transition.Trigger)
	}
}

func TestAgentStateConstants(t *testing.T) {
	t.Parallel()

	// Verify all state constants have expected values
	states := map[AgentState]string{
		StateGenerating: "GENERATING",
		StateWaiting:    "WAITING",
		StateThinking:   "THINKING",
		StateError:      "ERROR",
		StateStalled:    "STALLED",
		StateUnknown:    "UNKNOWN",
	}

	for state, expected := range states {
		if string(state) != expected {
			t.Errorf("state %v should equal %q", state, expected)
		}
	}
}

func TestPatternCategoryConstants(t *testing.T) {
	t.Parallel()

	categories := map[PatternCategory]string{
		CategoryIdle:       "idle",
		CategoryError:      "error",
		CategoryThinking:   "thinking",
		CategoryCompletion: "completion",
	}

	for cat, expected := range categories {
		if string(cat) != expected {
			t.Errorf("category %v should equal %q", cat, expected)
		}
	}
}

func TestVelocityTrackerCircularBufferEdgeCases(t *testing.T) {
	t.Parallel()

	// Test with size 1 - single element buffer
	tracker := NewVelocityTrackerWithSize("test", 1)

	tracker.mu.Lock()
	tracker.addSampleLocked(VelocitySample{Velocity: 1.0})
	tracker.addSampleLocked(VelocitySample{Velocity: 2.0})
	tracker.addSampleLocked(VelocitySample{Velocity: 3.0})
	tracker.mu.Unlock()

	if len(tracker.Samples) != 1 {
		t.Errorf("expected 1 sample, got %d", len(tracker.Samples))
	}
	if tracker.Samples[0].Velocity != 3.0 {
		t.Errorf("expected last velocity 3.0, got %f", tracker.Samples[0].Velocity)
	}
}

func TestVelocityTrackerExactMaxSamples(t *testing.T) {
	t.Parallel()

	tracker := NewVelocityTrackerWithSize("test", 3)

	// Add exactly MaxSamples samples
	for i := 1; i <= 3; i++ {
		tracker.mu.Lock()
		tracker.addSampleLocked(VelocitySample{Velocity: float64(i)})
		tracker.mu.Unlock()
	}

	if len(tracker.Samples) != 3 {
		t.Errorf("expected 3 samples, got %d", len(tracker.Samples))
	}

	// Verify order: 1.0, 2.0, 3.0
	for i, s := range tracker.Samples {
		expected := float64(i + 1)
		if s.Velocity != expected {
			t.Errorf("sample %d: expected velocity %f, got %f", i, expected, s.Velocity)
		}
	}
}

func TestVelocityTrackerRecentVelocityEdgeCases(t *testing.T) {
	t.Parallel()

	tracker := NewVelocityTrackerWithSize("test", 10)

	// Test with exactly n=1
	tracker.mu.Lock()
	tracker.addSampleLocked(VelocitySample{Velocity: 10.0})
	tracker.addSampleLocked(VelocitySample{Velocity: 20.0})
	tracker.addSampleLocked(VelocitySample{Velocity: 30.0})
	tracker.mu.Unlock()

	// Recent 1 should be just the last sample
	if v := tracker.RecentVelocity(1); v != 30.0 {
		t.Errorf("RecentVelocity(1) = %f, want 30.0", v)
	}

	// Test with negative n (should use all)
	if v := tracker.RecentVelocity(-1); v != 20.0 {
		t.Errorf("RecentVelocity(-1) = %f, want 20.0 (average of all)", v)
	}
}

func TestLastOutputAgeLocked_AllSamplesNoOutput(t *testing.T) {
	t.Parallel()

	tracker := NewVelocityTrackerWithSize("test", 5)

	// Set LastCaptureAt but add samples with no output
	oldTime := time.Now().Add(-10 * time.Second)
	tracker.mu.Lock()
	tracker.LastCaptureAt = time.Now()
	tracker.Samples = []VelocitySample{
		{Timestamp: oldTime.Add(-5 * time.Second), CharsAdded: 0, Velocity: 0},
		{Timestamp: oldTime, CharsAdded: 0, Velocity: 0},
	}
	tracker.mu.Unlock()

	// Should return time since oldest sample
	age := tracker.LastOutputAge()
	if age < 14*time.Second || age > 16*time.Second {
		t.Errorf("expected ~15s age (since oldest sample), got %v", age)
	}
}

func TestLastOutputAgeLocked_MixedSamples(t *testing.T) {
	t.Parallel()

	tracker := NewVelocityTrackerWithSize("test", 5)

	// Mix of samples with and without output
	now := time.Now()
	tracker.mu.Lock()
	tracker.LastCaptureAt = now
	tracker.Samples = []VelocitySample{
		{Timestamp: now.Add(-10 * time.Second), CharsAdded: 50, Velocity: 5.0}, // Has output
		{Timestamp: now.Add(-5 * time.Second), CharsAdded: 0, Velocity: 0},     // No output
		{Timestamp: now.Add(-2 * time.Second), CharsAdded: 0, Velocity: 0},     // No output
	}
	tracker.mu.Unlock()

	// Should find the sample with output (10s ago)
	age := tracker.LastOutputAge()
	if age < 9*time.Second || age > 11*time.Second {
		t.Errorf("expected ~10s age (sample with output), got %v", age)
	}
}

func TestClassifyState_StalledAfterGenerating(t *testing.T) {
	t.Parallel()

	sc := NewStateClassifier("test", &ClassifierConfig{
		StallThreshold: 100 * time.Millisecond, // Very short for testing
	})

	// Set current state to GENERATING
	sc.mu.Lock()
	sc.currentState = StateGenerating

	// Mock the velocity tracker's last output time to be old
	oldTime := time.Now().Add(-200 * time.Millisecond)
	sc.velocityTracker.Samples = []VelocitySample{
		{Timestamp: oldTime, CharsAdded: 100, Velocity: 50.0}, // Had output
	}
	sc.velocityTracker.LastCaptureAt = time.Now()
	sc.mu.Unlock()

	// Classify with 0 velocity and no patterns - should detect stall after generating
	state, conf, trigger := sc.classifyState(0, nil)

	if state != StateStalled {
		t.Errorf("expected STALLED state, got %s", state)
	}
	if conf < 0.7 {
		t.Errorf("expected confidence >= 0.7, got %f", conf)
	}
	if trigger != "stalled_after_generating" {
		t.Errorf("expected trigger 'stalled_after_generating', got %s", trigger)
	}
}

func TestClassifyState_IdleNoOutputNotGenerating(t *testing.T) {
	t.Parallel()

	sc := NewStateClassifier("test", &ClassifierConfig{
		StallThreshold: 100 * time.Millisecond,
	})

	// Set current state to WAITING (not GENERATING)
	sc.mu.Lock()
	sc.currentState = StateWaiting

	// Mock old last output
	oldTime := time.Now().Add(-200 * time.Millisecond)
	sc.velocityTracker.Samples = []VelocitySample{
		{Timestamp: oldTime, CharsAdded: 100, Velocity: 50.0},
	}
	sc.velocityTracker.LastCaptureAt = time.Now()
	sc.mu.Unlock()

	// Classify with 0 velocity - should return WAITING, not STALLED
	state, _, trigger := sc.classifyState(0, nil)

	if state != StateWaiting {
		t.Errorf("expected WAITING state, got %s", state)
	}
	if trigger != "idle_no_output" {
		t.Errorf("expected trigger 'idle_no_output', got %s", trigger)
	}
}

func TestClassifyState_IdlePromptWithHighVelocity(t *testing.T) {
	t.Parallel()

	sc := NewStateClassifier("test", nil)

	// Idle prompt pattern detected but high velocity
	matches := []PatternMatch{{Pattern: "claude_prompt", Category: CategoryIdle}}

	// High velocity should mean GENERATING, not WAITING
	state, _, _ := sc.classifyState(20.0, matches)

	if state != StateGenerating {
		t.Errorf("expected GENERATING state with high velocity, got %s", state)
	}
}

func TestClassifyState_ErrorTakesPriority(t *testing.T) {
	t.Parallel()

	sc := NewStateClassifier("test", nil)

	// Multiple patterns including error
	matches := []PatternMatch{
		{Pattern: "claude_prompt", Category: CategoryIdle},
		{Pattern: "rate_limit", Category: CategoryError},
		{Pattern: "thinking_text", Category: CategoryThinking},
	}

	// Error should take priority even with high velocity
	state, conf, _ := sc.classifyState(50.0, matches)

	if state != StateError {
		t.Errorf("expected ERROR state, got %s", state)
	}
	if conf < 0.95 {
		t.Errorf("expected high confidence for error, got %f", conf)
	}
}

func TestClassifyState_IdleTakesPriorityOverHistoricalErrors(t *testing.T) {
	t.Parallel()

	sc := NewStateClassifier("test", nil)

	// Simulate Codex agent showing idle prompt but with "failed" in historical output.
	// This is the false positive bug: agent is ready for input but shows ERROR
	// because older error patterns are still visible in captured output.
	matches := []PatternMatch{
		{Pattern: "codex_context_left", Category: CategoryIdle}, // Agent is at prompt
		{Pattern: "failed_text", Category: CategoryError},       // Historical error message
	}

	// Low velocity indicates agent is idle/waiting
	state, _, trigger := sc.classifyState(0.5, matches)

	if state != StateWaiting {
		t.Errorf("expected WAITING state when idle prompt present with low velocity, got %s (trigger: %s)", state, trigger)
	}
}

func TestApplyHysteresis_SameStateResetsPending(t *testing.T) {
	t.Parallel()

	sc := NewStateClassifier("test", &ClassifierConfig{
		HysteresisDuration: time.Hour, // Long duration
	})

	// Set initial state
	sc.mu.Lock()
	sc.currentState = StateGenerating
	sc.pendingState = StateWaiting
	sc.pendingSince = time.Now().Add(-time.Minute)
	sc.mu.Unlock()

	// Apply hysteresis with same state as current
	result := sc.applyHysteresis(StateGenerating, 0.85, "test")

	if result != StateGenerating {
		t.Errorf("expected GENERATING, got %s", result)
	}
	if sc.pendingState != "" {
		t.Errorf("pending state should be reset, got %s", sc.pendingState)
	}
}

func TestApplyHysteresis_DifferentPendingState(t *testing.T) {
	t.Parallel()

	sc := NewStateClassifier("test", &ClassifierConfig{
		HysteresisDuration: time.Hour,
	})

	// Set initial state
	sc.mu.Lock()
	sc.currentState = StateGenerating
	sc.pendingState = StateWaiting
	sc.pendingSince = time.Now()
	// Add a transition so we're past first classification
	sc.stateHistory = []StateTransition{{From: StateUnknown, To: StateGenerating}}
	sc.mu.Unlock()

	// Apply hysteresis with DIFFERENT state than pending
	result := sc.applyHysteresis(StateThinking, 0.80, "thinking")

	if result != StateGenerating {
		t.Errorf("expected GENERATING (unchanged), got %s", result)
	}
	if sc.pendingState != StateThinking {
		t.Errorf("pending should be THINKING, got %s", sc.pendingState)
	}
}

func TestRecordTransition_MaxHistoryBoundary(t *testing.T) {
	t.Parallel()

	sc := NewStateClassifier("test", nil)

	// Fill history to exactly MaxStateHistory - 1
	for i := 0; i < MaxStateHistory-1; i++ {
		sc.recordTransition(StateGenerating, StateWaiting, 0.90, "test")
	}

	if len(sc.stateHistory) != MaxStateHistory-1 {
		t.Errorf("expected %d transitions, got %d", MaxStateHistory-1, len(sc.stateHistory))
	}

	// Add one more (should be exactly at max)
	sc.recordTransition(StateWaiting, StateGenerating, 0.85, "boundary")

	if len(sc.stateHistory) != MaxStateHistory {
		t.Errorf("expected %d transitions at boundary, got %d", MaxStateHistory, len(sc.stateHistory))
	}

	// Add one more (should still be at max, oldest removed)
	sc.recordTransition(StateGenerating, StateError, 0.95, "overflow")

	if len(sc.stateHistory) != MaxStateHistory {
		t.Errorf("expected %d transitions after overflow, got %d", MaxStateHistory, len(sc.stateHistory))
	}
}

func TestAgentActivityStruct(t *testing.T) {
	t.Parallel()

	now := time.Now()
	activity := AgentActivity{
		PaneID:           "cc_1",
		AgentType:        "claude",
		State:            StateGenerating,
		Confidence:       0.85,
		Velocity:         15.5,
		StateSince:       now,
		DetectedPatterns: []string{"pattern1", "pattern2"},
		LastOutput:       now.Add(-5 * time.Second),
		StateHistory: []StateTransition{
			{From: StateUnknown, To: StateGenerating},
		},
		PendingState: StateWaiting,
		PendingSince: now.Add(-time.Second),
	}

	if activity.PaneID != "cc_1" {
		t.Errorf("expected pane cc_1, got %s", activity.PaneID)
	}
	if activity.State != StateGenerating {
		t.Errorf("expected state GENERATING, got %s", activity.State)
	}
	if len(activity.DetectedPatterns) != 2 {
		t.Errorf("expected 2 patterns, got %d", len(activity.DetectedPatterns))
	}
	if len(activity.StateHistory) != 1 {
		t.Errorf("expected 1 history entry, got %d", len(activity.StateHistory))
	}
}

func TestClassifierConfigDefaults(t *testing.T) {
	t.Parallel()

	// Nil config should use defaults
	sc := NewStateClassifier("test", nil)

	if sc.stallThreshold != DefaultStallThreshold {
		t.Errorf("expected default stall threshold, got %v", sc.stallThreshold)
	}
	if sc.hysteresisDuration != DefaultHysteresisDuration {
		t.Errorf("expected default hysteresis, got %v", sc.hysteresisDuration)
	}
	if sc.patternLibrary != DefaultLibrary {
		t.Error("expected default pattern library")
	}
}

func TestClassifierConfigCustomPatternLibrary(t *testing.T) {
	t.Parallel()

	customLib := NewPatternLibrary()
	cfg := &ClassifierConfig{
		PatternLibrary: customLib,
	}

	sc := NewStateClassifier("test", cfg)

	if sc.patternLibrary != customLib {
		t.Error("expected custom pattern library")
	}
}

func TestActivityMonitorWithConfig(t *testing.T) {
	t.Parallel()

	cfg := &ClassifierConfig{
		AgentType:          "claude",
		StallThreshold:     time.Minute,
		HysteresisDuration: 5 * time.Second,
	}

	am := NewActivityMonitor(cfg)
	sc := am.GetOrCreate("pane1")

	if sc.agentType != "claude" {
		t.Errorf("expected agent type claude, got %s", sc.agentType)
	}
	if sc.stallThreshold != time.Minute {
		t.Errorf("expected stall threshold 1m, got %v", sc.stallThreshold)
	}
}

func TestVelocityThresholdConstants(t *testing.T) {
	t.Parallel()

	// Verify threshold ordering
	if VelocityHighThreshold <= VelocityMediumThreshold {
		t.Error("high threshold should be > medium threshold")
	}
	if VelocityMediumThreshold <= VelocityIdleThreshold {
		t.Error("medium threshold should be > idle threshold")
	}
}

func TestDefaultConstantValues(t *testing.T) {
	t.Parallel()

	if DefaultMaxSamples != 10 {
		t.Errorf("expected DefaultMaxSamples=10, got %d", DefaultMaxSamples)
	}
	if DefaultStallThreshold != 30*time.Second {
		t.Errorf("expected DefaultStallThreshold=30s, got %v", DefaultStallThreshold)
	}
	if DefaultHysteresisDuration != 2*time.Second {
		t.Errorf("expected DefaultHysteresisDuration=2s, got %v", DefaultHysteresisDuration)
	}
	if MaxStateHistory != 20 {
		t.Errorf("expected MaxStateHistory=20, got %d", MaxStateHistory)
	}
}

func TestGenerateActivityHintsEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		available    []string
		busy         []string
		problem      []string
		summary      ActivitySummary
		checkSummary func(string) bool
	}{
		{
			name:      "single_available",
			available: []string{"1"},
			busy:      []string{},
			problem:   []string{},
			summary: ActivitySummary{
				TotalAgents: 1,
				ByState:     map[string]int{"WAITING": 1},
			},
			checkSummary: func(s string) bool { return strings.Contains(s, "1 available") },
		},
		{
			name:      "single_busy",
			available: []string{},
			busy:      []string{"1"},
			problem:   []string{},
			summary: ActivitySummary{
				TotalAgents: 1,
				ByState:     map[string]int{"GENERATING": 1},
			},
			checkSummary: func(s string) bool { return strings.Contains(s, "1 busy") },
		},
		{
			name:      "mixed_agents",
			available: []string{"1", "2"},
			busy:      []string{"3", "4", "5"},
			problem:   []string{"6"},
			summary: ActivitySummary{
				TotalAgents: 6,
				ByState:     map[string]int{"WAITING": 2, "GENERATING": 3, "ERROR": 1},
			},
			checkSummary: func(s string) bool {
				return strings.Contains(s, "2 available") &&
					strings.Contains(s, "3 busy") &&
					strings.Contains(s, "1 problems")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hints := generateActivityHints(tt.available, tt.busy, tt.problem, tt.summary)
			if !tt.checkSummary(hints.Summary) {
				t.Errorf("summary check failed: %s", hints.Summary)
			}
		})
	}
}

func TestNormalizeAgentTypeEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"", ""}, // Empty string stays empty
		{"CLAUDE", "claude"},
		{"CODEX", "codex"},
		{"GEMINI", "gemini"},
		{"CC", "claude"},
		{"COD", "codex"},
		{"GMI", "gemini"},
		{"  claude  ", "  claude  "}, // Whitespace not trimmed - passthrough
		{"user", "user"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeAgentType(tt.input)
			if got != tt.want {
				t.Errorf("normalizeAgentType(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestActivityMonitorNilConfig(t *testing.T) {
	t.Parallel()

	am := NewActivityMonitor(nil)

	// Should still work with nil config
	sc := am.GetOrCreate("pane1")
	if sc == nil {
		t.Fatal("expected classifier even with nil config")
	}

	// Should use defaults
	if sc.stallThreshold != DefaultStallThreshold {
		t.Errorf("expected default stall threshold")
	}
}

func TestVelocityManagerConcurrentAccess(t *testing.T) {
	t.Parallel()

	vm := NewVelocityManager()

	// Simulate concurrent access
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(idx int) {
			paneID := "pane_" + string(rune('0'+idx))
			tracker := vm.GetOrCreate(paneID)
			_ = tracker.CurrentVelocity()
			_ = vm.GetAllVelocities()
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	if vm.TrackerCount() != 10 {
		t.Errorf("expected 10 trackers, got %d", vm.TrackerCount())
	}
}

func TestActivityMonitorConcurrentAccess(t *testing.T) {
	t.Parallel()

	am := NewActivityMonitor(nil)

	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(idx int) {
			paneID := "pane_" + string(rune('0'+idx))
			sc := am.GetOrCreate(paneID)
			_ = sc.CurrentState()
			_ = am.GetAllStates()
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	if am.Count() != 10 {
		t.Errorf("expected 10 classifiers, got %d", am.Count())
	}
}

func TestStateClassifierConcurrentAccess(t *testing.T) {
	t.Parallel()

	sc := NewStateClassifier("test", nil)

	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			_ = sc.CurrentState()
			_ = sc.GetStateHistory()
			_ = sc.StateDuration()
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestVelocityTrackerResetClearsAll(t *testing.T) {
	t.Parallel()

	tracker := NewVelocityTrackerWithSize("test", 5)

	// Add state
	tracker.mu.Lock()
	tracker.Samples = []VelocitySample{
		{Velocity: 1.0}, {Velocity: 2.0}, {Velocity: 3.0},
	}
	tracker.LastCapture = "some content"
	tracker.LastCaptureAt = time.Now()
	tracker.mu.Unlock()

	// Verify state exists
	if tracker.SampleCount() != 3 {
		t.Fatalf("expected 3 samples before reset, got %d", tracker.SampleCount())
	}

	// Reset
	tracker.Reset()

	// Verify all cleared
	if tracker.SampleCount() != 0 {
		t.Errorf("expected 0 samples after reset, got %d", tracker.SampleCount())
	}
	if tracker.LastCapture != "" {
		t.Error("LastCapture should be empty after reset")
	}
	if !tracker.LastCaptureAt.IsZero() {
		t.Error("LastCaptureAt should be zero after reset")
	}
}

func TestClassifyStateVelocityBoundaries(t *testing.T) {
	t.Parallel()

	sc := NewStateClassifier("test", nil)

	// Test exactly at VelocityHighThreshold
	// At boundary: 10.0 > 10.0 is false, but 10.0 > 2.0 (medium) is true
	// So it's GENERATING but via medium threshold (0.70 conf, not 0.85)
	state, conf, _ := sc.classifyState(VelocityHighThreshold, nil)
	if state != StateGenerating {
		t.Errorf("at exact high threshold, expected GENERATING via medium path, got %s", state)
	}
	if conf != 0.70 {
		t.Errorf("at exact high threshold, expected 0.70 confidence (medium), got %f", conf)
	}

	// Test just above VelocityHighThreshold
	state, _, _ = sc.classifyState(VelocityHighThreshold+0.1, nil)
	if state != StateGenerating {
		t.Errorf("expected GENERATING above high threshold, got %s", state)
	}

	// Test exactly at VelocityMediumThreshold
	// At exactly medium, should not be generating (need > not >=)
	state, _, _ = sc.classifyState(VelocityMediumThreshold, nil)
	if state == StateGenerating {
		t.Errorf("at exact medium threshold, should not be GENERATING (uses > not >=), got %s", state)
	}

	// Test just above VelocityMediumThreshold
	state, _, _ = sc.classifyState(VelocityMediumThreshold+0.1, nil)
	if state != StateGenerating {
		t.Errorf("expected GENERATING above medium threshold, got %s", state)
	}
}

func TestClassifyStateIdleAtBoundary(t *testing.T) {
	t.Parallel()

	sc := NewStateClassifier("test", nil)

	// Idle prompt pattern with velocity exactly at idle threshold
	matches := []PatternMatch{{Pattern: "claude_prompt", Category: CategoryIdle}}

	// At exact boundary WITH idle prompt, should NOT be waiting
	// The condition is `velocity < VelocityIdleThreshold` (strictly less than)
	// So at exactly 1.0, with 1.0 < 1.0 being false, we should not get WAITING
	state, _, _ := sc.classifyState(VelocityIdleThreshold, matches)
	if state == StateWaiting {
		t.Errorf("at exact idle threshold with prompt, should not be WAITING (uses < not <=), got %s", state)
	}

	// With idle prompt and below threshold
	state, _, _ = sc.classifyState(VelocityIdleThreshold-0.1, matches)
	if state != StateWaiting {
		t.Errorf("expected WAITING with idle prompt below threshold, got %s", state)
	}
}
