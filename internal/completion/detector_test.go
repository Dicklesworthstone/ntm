package completion

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()

	_ = w.Close()
	os.Stdout = oldStdout
	return <-done
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.PollInterval != 5*time.Second {
		t.Errorf("PollInterval = %v, want %v", cfg.PollInterval, 5*time.Second)
	}
	if cfg.IdleThreshold != 120*time.Second {
		t.Errorf("IdleThreshold = %v, want %v", cfg.IdleThreshold, 120*time.Second)
	}
	if !cfg.RetryOnError {
		t.Error("RetryOnError should be true by default")
	}
	if !cfg.GracefulDegrading {
		t.Error("GracefulDegrading should be true by default")
	}
	if cfg.CaptureLines != 50 {
		t.Errorf("CaptureLines = %d, want 50", cfg.CaptureLines)
	}
}

func TestNew(t *testing.T) {
	store := assignment.NewStore("test-session")
	d := New("test-session", store)

	if d.Session != "test-session" {
		t.Errorf("Session = %q, want %q", d.Session, "test-session")
	}
	if d.Store != store {
		t.Error("Store not set correctly")
	}
	if len(d.Patterns) == 0 {
		t.Error("Default completion patterns not loaded")
	}
	if len(d.FailPattern) == 0 {
		t.Error("Default failure patterns not loaded")
	}
}

func TestAddPattern(t *testing.T) {
	d := New("test-session", nil)
	initialCount := len(d.Patterns)

	err := d.AddPattern(`(?i)custom\s+complete`)
	if err != nil {
		t.Fatalf("AddPattern failed: %v", err)
	}

	if len(d.Patterns) != initialCount+1 {
		t.Errorf("Pattern count = %d, want %d", len(d.Patterns), initialCount+1)
	}
}

func TestAddPatternInvalid(t *testing.T) {
	d := New("test-session", nil)

	err := d.AddPattern(`[invalid`)
	if err == nil {
		t.Error("AddPattern should fail for invalid regex")
	}
}

func TestAddFailurePattern(t *testing.T) {
	d := New("test-session", nil)
	initialCount := len(d.FailPattern)

	err := d.AddFailurePattern(`(?i)custom\s+failure`)
	if err != nil {
		t.Fatalf("AddFailurePattern failed: %v", err)
	}

	if len(d.FailPattern) != initialCount+1 {
		t.Errorf("Pattern count = %d, want %d", len(d.FailPattern), initialCount+1)
	}
}

func TestMatchCompletionPatterns(t *testing.T) {
	d := New("test-session", nil)

	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{"bead complete", "I've finished the bead bd-1234 complete", true},
		{"task done", "The task bd-1234 done successfully", true},
		{"task finished", "task xyz finished successfully", true},
		{"closing bead", "closing bead bd-5678", true},
		{"br close", "Running br close bd-1234", true},
		{"marked complete", "The work was marked as complete", true},
		{"successfully completed", "Task successfully completed!", true},
		{"work complete", "My work complete for this bead", true},
		{"no match", "Just regular output without keywords", false},
		{"partial match", "The bead is still in progress", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := d.matchCompletionPatterns(tt.output)
			if got != tt.want {
				t.Errorf("matchCompletionPatterns(%q) = %v, want %v", tt.output, got, tt.want)
			}
		})
	}
}

func TestMatchFailurePatterns(t *testing.T) {
	d := New("test-session", nil)

	tests := []struct {
		name      string
		output    string
		wantMatch bool
	}{
		{"unable to complete", "I'm unable to complete this task", true},
		{"cannot proceed", "Cannot proceed due to missing dependencies", true},
		{"blocked by", "This is blocked by another issue", true},
		{"giving up", "I'm giving up on this approach", true},
		{"need help", "I need help with this problem", true},
		{"failed to", "Failed to compile the code", true},
		{"error fatal", "Error: fatal exception occurred", true},
		{"aborting", "Aborting the operation", true},
		{"no match", "Everything is working fine", false},
		{"success message", "Successfully deployed the feature", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := d.matchFailurePatterns(tt.output)
			if (got != "") != tt.wantMatch {
				t.Errorf("matchFailurePatterns(%q) = %q, wantMatch=%v", tt.output, got, tt.wantMatch)
			}
		})
	}
}

func TestTruncateOutput(t *testing.T) {
	tests := []struct {
		name   string
		output string
		maxLen int
		want   string
	}{
		{"short", "hello", 10, "hello"},
		{"exact", "hello", 5, "hello"},
		{"truncate", "hello world", 5, "...world"},
		{"empty", "", 10, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateOutput(tt.output, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateOutput(%q, %d) = %q, want %q", tt.output, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestCompletionEventFields(t *testing.T) {
	event := CompletionEvent{
		Pane:       2,
		AgentType:  "claude",
		BeadID:     "bd-1234",
		Method:     MethodPatternMatch,
		Timestamp:  time.Now(),
		Duration:   5 * time.Minute,
		Output:     "task complete",
		IsFailed:   false,
		FailReason: "",
	}

	if event.Pane != 2 {
		t.Errorf("Pane = %d, want 2", event.Pane)
	}
	if event.AgentType != "claude" {
		t.Errorf("AgentType = %q, want %q", event.AgentType, "claude")
	}
	if event.Method != MethodPatternMatch {
		t.Errorf("Method = %v, want %v", event.Method, MethodPatternMatch)
	}
}

func TestDetectionMethods(t *testing.T) {
	tests := []struct {
		method DetectionMethod
		want   string
	}{
		{MethodBeadClosed, "bead_closed"},
		{MethodPatternMatch, "pattern_match"},
		{MethodIdle, "idle"},
		{MethodAgentMail, "agent_mail"},
		{MethodPaneLost, "pane_lost"},
	}

	for _, tt := range tests {
		t.Run(string(tt.method), func(t *testing.T) {
			if string(tt.method) != tt.want {
				t.Errorf("DetectionMethod = %q, want %q", tt.method, tt.want)
			}
		})
	}
}

func TestCheckNowNoStore(t *testing.T) {
	d := New("test-session", nil)

	_, err := d.CheckNow(0)
	if err == nil {
		t.Error("CheckNow should fail without assignment store")
	}
}

func TestCheckNowNoAssignment(t *testing.T) {
	store := assignment.NewStore("test-session")
	d := New("test-session", store)

	_, err := d.CheckNow(99)
	if err == nil {
		t.Error("CheckNow should fail for pane with no assignment")
	}
}

func TestIdleDetection(t *testing.T) {
	store := assignment.NewStore("test-session")
	cfg := DefaultConfig()
	cfg.IdleThreshold = 10 * time.Millisecond // Very short for testing
	d := NewWithConfig("test-session", store, cfg)

	now := time.Now()
	a := &assignment.Assignment{
		BeadID:     "bd-test",
		Pane:       0,
		AgentType:  "claude",
		AssignedAt: now,
	}

	// First check - initialize activity state
	event := d.checkIdle(a, "initial output", now)
	if event != nil {
		t.Error("First checkIdle should return nil (initializing)")
	}

	// Same output - should trigger burst detection but not complete yet
	event = d.checkIdle(a, "initial output", now)
	if event != nil {
		t.Error("Second checkIdle should return nil (no burst started)")
	}

	// Change output to start burst
	event = d.checkIdle(a, "new output", now)
	if event != nil {
		t.Error("After output change, checkIdle should return nil")
	}

	// Wait for idle threshold
	time.Sleep(15 * time.Millisecond)

	// Same output after threshold - should detect idle completion
	event = d.checkIdle(a, "new output", now)
	if event == nil {
		t.Error("After idle threshold, checkIdle should return completion event")
	}
	if event != nil && event.Method != MethodIdle {
		t.Errorf("Method = %v, want %v", event.Method, MethodIdle)
	}
}

func TestIdleDetectionResetsForNewAssignmentOnSamePane(t *testing.T) {
	store := assignment.NewStore("test-session")
	cfg := DefaultConfig()
	cfg.IdleThreshold = 10 * time.Millisecond
	d := NewWithConfig("test-session", store, cfg)

	now := time.Now()
	first := &assignment.Assignment{
		BeadID:     "bd-old",
		Pane:       0,
		AgentType:  "claude",
		AssignedAt: now,
	}
	second := &assignment.Assignment{
		BeadID:     "bd-new",
		Pane:       0,
		AgentType:  "claude",
		AssignedAt: now.Add(time.Second),
	}

	if event := d.checkIdle(first, "initial output", now); event != nil {
		t.Fatal("first assignment should initialize idle tracking without completing")
	}
	if event := d.checkIdle(first, "updated output", now); event != nil {
		t.Fatal("first assignment should not complete when output changes")
	}

	time.Sleep(15 * time.Millisecond)

	if event := d.checkIdle(second, "updated output", second.AssignedAt); event != nil {
		t.Fatalf("new assignment should not inherit stale idle state, got %+v", event)
	}
}

func TestWatchCancellation(t *testing.T) {
	store := assignment.NewStore("test-session")
	cfg := DefaultConfig()
	cfg.PollInterval = 10 * time.Millisecond
	d := NewWithConfig("test-session", store, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	events := d.Watch(ctx)

	// Cancel immediately
	cancel()

	// Channel should close
	select {
	case _, ok := <-events:
		if ok {
			// May receive events before close, that's fine
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Events channel should close after context cancellation")
	}
}

func TestDeduplication(t *testing.T) {
	d := New("test-session", nil)
	d.Config.DedupWindow = 100 * time.Millisecond

	// Record an event
	d.mu.Lock()
	d.recentEvents["bd-test:0:attempt"] = time.Now()
	d.mu.Unlock()

	// Check if within dedup window
	d.mu.RLock()
	lastEvent, exists := d.recentEvents["bd-test:0:attempt"]
	d.mu.RUnlock()

	if !exists {
		t.Error("Event should exist in recentEvents")
	}
	if time.Since(lastEvent) >= d.Config.DedupWindow {
		t.Error("Event should be within dedup window")
	}
}

func TestRecordEventLockedScopesDedupToAssignmentAttempt(t *testing.T) {
	d := New("test-session", nil)
	d.Config.DedupWindow = 100 * time.Millisecond

	now := time.Now()
	first := &assignment.Assignment{
		BeadID:     "bd-test",
		Pane:       0,
		AssignedAt: now,
	}
	retry := &assignment.Assignment{
		BeadID:     "bd-test",
		Pane:       0,
		AssignedAt: now.Add(time.Second),
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.recordEventLocked(first, now) {
		t.Fatal("first event should be recorded")
	}
	if d.recordEventLocked(first, now.Add(10*time.Millisecond)) {
		t.Fatal("same assignment attempt should be deduplicated within the window")
	}

	d.recentEvents["stale"] = now.Add(-time.Second)

	if !d.recordEventLocked(retry, now.Add(10*time.Millisecond)) {
		t.Fatal("retry attempt should not inherit the previous attempt's dedup state")
	}
	if _, exists := d.recentEvents["stale"]; exists {
		t.Fatal("expired dedup entries should be pruned")
	}
}

func TestBrAvailableCaching(t *testing.T) {
	d := New("test-session", nil)

	// First call checks availability
	result1 := d.isBrAvailable()

	// Second call should use cache
	result2 := d.isBrAvailable()

	if result1 != result2 {
		t.Error("isBrAvailable should return consistent results")
	}

	// Verify cache is set
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.brAvailable == nil {
		t.Error("brAvailable cache should be set after first call")
	}
}

func TestIsBrAvailable_DoesNotWriteStdoutWhenUnavailable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	d := New("test-session", nil)
	output := captureStdout(t, func() {
		if d.isBrAvailable() {
			t.Fatal("expected br to be unavailable in isolated PATH")
		}
	})

	if output != "" {
		t.Fatalf("expected no stdout output, got %q", output)
	}
}

// TestConcurrentDedup tests concurrent access to the deduplication mechanism
// to verify thread-safety under race conditions. Run with: go test -race
func TestConcurrentDedup(t *testing.T) {
	d := New("test-session", nil)
	d.Config.DedupWindow = 100 * time.Millisecond

	var wg sync.WaitGroup
	numGoroutines := 10
	eventsPerGoroutine := 20

	// Concurrent writes to recentEvents
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for j := 0; j < eventsPerGoroutine; j++ {
				beadID := fmt.Sprintf("bd-%d-%d", goroutineID, j)
				d.mu.Lock()
				d.recentEvents[beadID] = time.Now()
				d.mu.Unlock()

				// Also do some concurrent reads
				d.mu.RLock()
				_ = d.recentEvents[beadID]
				d.mu.RUnlock()
			}
		}(i)
	}

	// Concurrent reads
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < eventsPerGoroutine; j++ {
				d.mu.RLock()
				_ = len(d.recentEvents)
				d.mu.RUnlock()
			}
		}()
	}

	wg.Wait()

	// Verify all events were recorded
	d.mu.RLock()
	expectedCount := numGoroutines * eventsPerGoroutine
	actualCount := len(d.recentEvents)
	d.mu.RUnlock()

	if actualCount != expectedCount {
		t.Errorf("expected %d events, got %d", expectedCount, actualCount)
	}
}

// TestConcurrentActivityTracking tests concurrent access to activity tracker
func TestConcurrentActivityTracking(t *testing.T) {
	d := New("test-session", nil)

	var wg sync.WaitGroup
	numGoroutines := 10
	operationsPerGoroutine := 20

	// Concurrent activity tracker updates
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(pane int) {
			defer wg.Done()
			for j := 0; j < operationsPerGoroutine; j++ {
				d.mu.Lock()
				if d.activityTracker[pane] == nil {
					d.activityTracker[pane] = &activityState{}
				}
				d.activityTracker[pane].lastOutputTime = time.Now()
				d.activityTracker[pane].lastOutput = fmt.Sprintf("output-%d", j)
				d.mu.Unlock()

				// Concurrent read
				d.mu.RLock()
				_ = d.activityTracker[pane]
				d.mu.RUnlock()
			}
		}(i)
	}

	wg.Wait()

	// Verify all panes were tracked
	d.mu.RLock()
	actualPanes := len(d.activityTracker)
	d.mu.RUnlock()

	if actualPanes != numGoroutines {
		t.Errorf("expected %d panes tracked, got %d", numGoroutines, actualPanes)
	}
}

func TestAddFailurePatternInvalid(t *testing.T) {
	t.Parallel()

	d := New("test-session", nil)

	err := d.AddFailurePattern(`[invalid`)
	if err == nil {
		t.Error("AddFailurePattern should fail for invalid regex")
	}
}

func TestCheckAllNilStore(t *testing.T) {
	t.Parallel()

	d := New("test-session", nil)
	d.Store = nil

	// Should not panic, just return early
	events := make(chan CompletionEvent, 10)
	ctx := context.Background()
	d.checkAll(ctx, events)

	// Channel should be empty
	select {
	case <-events:
		t.Error("checkAll with nil store should not emit events")
	default:
		// Expected - no events emitted
	}
}

func TestCheckAllEmptyStore(t *testing.T) {
	t.Parallel()

	store := assignment.NewStore("test-session")
	d := New("test-session", store)

	events := make(chan CompletionEvent, 10)
	ctx := context.Background()
	d.checkAll(ctx, events)

	// Channel should be empty (no active assignments)
	select {
	case <-events:
		t.Error("checkAll with empty store should not emit events")
	default:
		// Expected - no events emitted
	}
}

func TestCheckAllContextCancelled(t *testing.T) {
	t.Parallel()

	store := assignment.NewStore("test-session")
	// Add an assignment that will be checked
	store.Assign("bd-test", "Test Bead", 0, "claude", "agent-1", "test prompt")

	d := New("test-session", store)

	events := make(chan CompletionEvent, 10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// Should return early without processing
	d.checkAll(ctx, events)

	// Give a moment for any processing
	time.Sleep(10 * time.Millisecond)

	// Should not block or panic
	select {
	case <-events:
		// May or may not receive event depending on timing
	default:
		// Expected in most cases
	}
}

func TestNewWithConfigCustomSettings(t *testing.T) {
	t.Parallel()

	cfg := DetectionConfig{
		PollInterval:      1 * time.Second,
		IdleThreshold:     60 * time.Second,
		RetryOnError:      false,
		RetryInterval:     5 * time.Second,
		MaxRetries:        5,
		DedupWindow:       10 * time.Second,
		GracefulDegrading: false,
		CaptureLines:      100,
	}

	d := NewWithConfig("custom-session", nil, cfg)

	if d.Config.PollInterval != 1*time.Second {
		t.Errorf("PollInterval = %v, want 1s", d.Config.PollInterval)
	}
	if d.Config.IdleThreshold != 60*time.Second {
		t.Errorf("IdleThreshold = %v, want 60s", d.Config.IdleThreshold)
	}
	if d.Config.RetryOnError {
		t.Error("RetryOnError should be false")
	}
	if d.Config.GracefulDegrading {
		t.Error("GracefulDegrading should be false")
	}
	if d.Config.CaptureLines != 100 {
		t.Errorf("CaptureLines = %d, want 100", d.Config.CaptureLines)
	}
}

func TestCheckNowWithActiveAssignment(t *testing.T) {
	t.Parallel()

	store := assignment.NewStore("test-session")
	store.Assign("bd-test", "Test Bead", 5, "claude", "agent-1", "test prompt")

	d := New("test-session", store)

	// CheckNow will fail because we can't query real tmux panes,
	// but it should find the assignment and attempt to check it
	event, err := d.CheckNow(5)
	// The error comes from tmux.GetPanes failing, not from assignment lookup
	// In test environment without tmux, this returns nil event
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	// Event may be nil if tmux isn't available
	_ = event
}

func TestIdleDetectionNoBurstActive(t *testing.T) {
	t.Parallel()

	store := assignment.NewStore("test-session")
	cfg := DefaultConfig()
	cfg.IdleThreshold = 10 * time.Millisecond
	d := NewWithConfig("test-session", store, cfg)

	now := time.Now()
	a := &assignment.Assignment{
		BeadID:     "bd-test",
		Pane:       0,
		AgentType:  "claude",
		AssignedAt: now,
	}

	// Initialize state
	d.checkIdle(a, "initial", now)

	// Same output - should trigger completion because burstActive is initialized to true
	time.Sleep(15 * time.Millisecond)
	event := d.checkIdle(a, "initial", now)
	if event == nil {
		t.Error("Idle detection should trigger since burstActive is initialized to true")
	}
}

func TestActivityStateFields(t *testing.T) {
	t.Parallel()

	state := &activityState{
		lastOutputTime: time.Now(),
		lastOutput:     "test output",
		burstStarted:   time.Now().Add(-1 * time.Minute),
		burstActive:    true,
	}

	if state.lastOutput != "test output" {
		t.Errorf("lastOutput = %q, want 'test output'", state.lastOutput)
	}
	if !state.burstActive {
		t.Error("burstActive should be true")
	}
}

func TestDetectionConfigFields(t *testing.T) {
	t.Parallel()

	cfg := DetectionConfig{
		PollInterval:      1 * time.Second,
		IdleThreshold:     30 * time.Second,
		RetryOnError:      true,
		RetryInterval:     5 * time.Second,
		MaxRetries:        10,
		DedupWindow:       3 * time.Second,
		GracefulDegrading: true,
		CaptureLines:      25,
	}

	if cfg.MaxRetries != 10 {
		t.Errorf("MaxRetries = %d, want 10", cfg.MaxRetries)
	}
	if cfg.DedupWindow != 3*time.Second {
		t.Errorf("DedupWindow = %v, want 3s", cfg.DedupWindow)
	}
}

func TestCompletionEventWithFailure(t *testing.T) {
	t.Parallel()

	event := CompletionEvent{
		Pane:       1,
		AgentType:  "codex",
		BeadID:     "bd-fail",
		Method:     MethodPaneLost,
		Timestamp:  time.Now(),
		Duration:   10 * time.Minute,
		Output:     "last output before crash",
		IsFailed:   true,
		FailReason: "agent crashed unexpectedly",
	}

	if !event.IsFailed {
		t.Error("IsFailed should be true")
	}
	if event.FailReason != "agent crashed unexpectedly" {
		t.Errorf("FailReason = %q, want 'agent crashed unexpectedly'", event.FailReason)
	}
	if event.Method != MethodPaneLost {
		t.Errorf("Method = %v, want %v", event.Method, MethodPaneLost)
	}
}

func TestMatchCompletionPatternsConcurrent(t *testing.T) {
	t.Parallel()

	d := New("test-session", nil)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				d.matchCompletionPatterns("task bd-1234 done successfully")
				d.matchCompletionPatterns("no match here")
			}
		}()
	}
	wg.Wait()
}

func TestMatchFailurePatternsConcurrent(t *testing.T) {
	t.Parallel()

	d := New("test-session", nil)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				d.matchFailurePatterns("unable to complete task")
				d.matchFailurePatterns("everything is fine")
			}
		}()
	}
	wg.Wait()
}
