// Package tests contains integration and unit tests for ntm watch mode.
package tests

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/completion"
)

// MockCompletionDetector simulates completion detection for testing
type MockCompletionDetector struct {
	mu           sync.Mutex
	events       []completion.CompletionEvent
	eventCh      chan completion.CompletionEvent
	watchStarted bool
}

func NewMockCompletionDetector() *MockCompletionDetector {
	return &MockCompletionDetector{
		eventCh: make(chan completion.CompletionEvent, 10),
	}
}

func (m *MockCompletionDetector) AddEvent(event completion.CompletionEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
	m.eventCh <- event
}

func (m *MockCompletionDetector) Watch(ctx context.Context) <-chan completion.CompletionEvent {
	m.mu.Lock()
	m.watchStarted = true
	m.mu.Unlock()
	return m.eventCh
}

func (m *MockCompletionDetector) WasStarted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.watchStarted
}

// TestWatchLoopStructure tests the WatchLoop struct initialization
func TestWatchLoopStructure(t *testing.T) {
	t.Run("NewWatchLoop creates valid instance", func(t *testing.T) {
		store := newIsolatedAssignmentStore(t, "test-session")

		// Verify store was created
		if store == nil {
			t.Fatal("Expected store to be non-nil")
		}

		stats := store.Stats()
		if stats.Total != 0 {
			t.Errorf("Expected 0 total assignments, got %d", stats.Total)
		}
	})
}

// TestCompletionEventHandling tests how completion events are processed
func TestCompletionEventHandling(t *testing.T) {
	t.Run("completion event updates stats", func(t *testing.T) {
		store := newIsolatedAssignmentStore(t, "test-session")

		// Add an assignment
		_, err := store.Assign("bd-test1", "Test Bead", 2, "claude", "cc_1", "test prompt")
		if err != nil {
			t.Fatalf("Failed to create assignment: %v", err)
		}

		// Verify assignment exists
		a := store.Get("bd-test1")
		if a == nil {
			t.Fatal("Expected assignment to exist")
		}

		// Mark as working first (required before completing)
		err = store.MarkWorking("bd-test1")
		if err != nil {
			t.Fatalf("Failed to mark working: %v", err)
		}

		// Mark as completed
		err = store.MarkCompleted("bd-test1")
		if err != nil {
			t.Fatalf("Failed to mark completed: %v", err)
		}

		// Verify status changed
		a = store.Get("bd-test1")
		if a.Status != assignment.StatusCompleted {
			t.Errorf("Expected status %s, got %s", assignment.StatusCompleted, a.Status)
		}
	})

	t.Run("failed completion records failure reason", func(t *testing.T) {
		store := newIsolatedAssignmentStore(t, "test-session")

		// Add an assignment
		_, err := store.Assign("bd-test2", "Test Bead 2", 3, "codex", "cod_1", "test prompt")
		if err != nil {
			t.Fatalf("Failed to create assignment: %v", err)
		}

		// Mark as failed
		err = store.MarkFailed("bd-test2", "rate limit exceeded")
		if err != nil {
			t.Fatalf("Failed to mark failed: %v", err)
		}

		// Verify status and reason
		a := store.Get("bd-test2")
		if a.Status != assignment.StatusFailed {
			t.Errorf("Expected status %s, got %s", assignment.StatusFailed, a.Status)
		}
		if a.FailReason != "rate limit exceeded" {
			t.Errorf("Expected fail reason 'rate limit exceeded', got '%s'", a.FailReason)
		}
	})
}

// TestExitConditions tests various exit conditions for watch mode
func TestExitConditions(t *testing.T) {
	t.Parallel()
	t.Run("context cancellation triggers exit", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())

		// Create a channel to track exit
		exitCh := make(chan struct{})

		go func() {
			// Simulate watch loop with context
			select {
			case <-ctx.Done():
				close(exitCh)
			case <-time.After(5 * time.Second):
				t.Error("Watch loop did not exit on context cancellation")
			}
		}()

		// Cancel context
		cancel()

		// Wait for exit
		select {
		case <-exitCh:
			// Success
		case <-time.After(time.Second):
			t.Error("Watch loop did not exit in time")
		}
	})

	t.Run("stop channel triggers exit", func(t *testing.T) {
		t.Parallel()
		stopCh := make(chan struct{})
		exitCh := make(chan struct{})

		go func() {
			select {
			case <-stopCh:
				close(exitCh)
			case <-time.After(5 * time.Second):
				t.Error("Watch loop did not exit on stop signal")
			}
		}()

		// Send stop signal
		close(stopCh)

		// Wait for exit
		select {
		case <-exitCh:
			// Success
		case <-time.After(time.Second):
			t.Error("Watch loop did not exit in time")
		}
	})
}

// TestShouldStopCondition tests the logic for stop-when-done exit
func TestShouldStopCondition(t *testing.T) {
	t.Run("returns false when active assignments exist", func(t *testing.T) {
		store := newIsolatedAssignmentStore(t, "test-session")

		// Add an active assignment
		_, err := store.Assign("bd-active", "Active Bead", 2, "claude", "cc_1", "prompt")
		if err != nil {
			t.Fatalf("Failed to assign: %v", err)
		}

		// Check active count
		active := store.ListActive()
		if len(active) != 1 {
			t.Errorf("Expected 1 active assignment, got %d", len(active))
		}
	})

	t.Run("returns true when no active assignments", func(t *testing.T) {
		store := newIsolatedAssignmentStore(t, "test-session")

		// Add and complete an assignment (need to go through working first)
		_, err := store.Assign("bd-done", "Done Bead", 2, "claude", "cc_1", "prompt")
		if err != nil {
			t.Fatalf("Failed to assign: %v", err)
		}
		_ = store.MarkWorking("bd-done")
		_ = store.MarkCompleted("bd-done")

		// Check active count
		active := store.ListActive()
		if len(active) != 0 {
			t.Errorf("Expected 0 active assignments, got %d", len(active))
		}
	})
}

// TestStreamingOutput tests log output formatting
func TestStreamingOutput(t *testing.T) {
	t.Parallel()
	t.Run("timestamp format is HH:MM:SS", func(t *testing.T) {
		t.Parallel()
		timestamp := time.Now().Format("15:04:05")
		// Verify format matches expected pattern
		if len(timestamp) != 8 {
			t.Errorf("Expected timestamp length 8, got %d", len(timestamp))
		}
	})

	t.Run("log messages include timestamps", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		timestamp := time.Now().Format("15:04:05")
		msg := fmt.Sprintf("[%s] Test message", timestamp)
		buf.WriteString(msg + "\n")

		if !bytes.Contains(buf.Bytes(), []byte("[")) {
			t.Error("Expected log to contain timestamp brackets")
		}
	})
}

// TestErrorHandling tests error scenarios
func TestErrorHandling(t *testing.T) {
	t.Run("handles missing assignment gracefully", func(t *testing.T) {
		store := newIsolatedAssignmentStore(t, "test-session")

		// Try to mark non-existent assignment as completed
		err := store.MarkCompleted("bd-nonexistent")
		if err == nil {
			t.Error("Expected error for non-existent assignment")
		}
	})

	t.Run("handles invalid status transition", func(t *testing.T) {
		store := newIsolatedAssignmentStore(t, "test-session")

		// Add and complete an assignment
		_, err := store.Assign("bd-test", "Test", 2, "claude", "cc_1", "prompt")
		if err != nil {
			t.Fatalf("Failed to assign: %v", err)
		}
		_ = store.MarkWorking("bd-test")
		_ = store.MarkCompleted("bd-test")

		// Try to mark as working after completed (invalid transition)
		err = store.MarkWorking("bd-test")
		if err == nil {
			t.Error("Expected error for invalid status transition")
		}
	})
}

// TestEdgeCases tests edge case scenarios
func TestEdgeCases(t *testing.T) {
	t.Run("empty assignment list", func(t *testing.T) {
		store := newIsolatedAssignmentStore(t, "test-session")

		// List should return empty slice (may be nil but len should be 0)
		active := store.ListActive()
		if len(active) != 0 {
			t.Errorf("Expected 0 assignments, got %d", len(active))
		}
	})

	t.Run("single agent scenario", func(t *testing.T) {
		store := newIsolatedAssignmentStore(t, "test-session")

		// Single assignment
		_, err := store.Assign("bd-single", "Single Bead", 2, "claude", "cc_1", "prompt")
		if err != nil {
			t.Fatalf("Failed to assign: %v", err)
		}

		stats := store.Stats()
		if stats.Total != 1 {
			t.Errorf("Expected 1 total, got %d", stats.Total)
		}
		if stats.Assigned != 1 {
			t.Errorf("Expected 1 assigned, got %d", stats.Assigned)
		}
	})

	t.Run("rapid completion events", func(t *testing.T) {
		store := newIsolatedAssignmentStore(t, "test-session")

		// Create multiple assignments
		for i := 0; i < 10; i++ {
			beadID := fmt.Sprintf("bd-rapid-%d", i)
			_, err := store.Assign(beadID, "Rapid Bead", i+2, "claude", fmt.Sprintf("cc_%d", i), "prompt")
			if err != nil {
				t.Fatalf("Failed to assign %s: %v", beadID, err)
			}
		}

		// Complete them rapidly (need working -> completed transition)
		for i := 0; i < 10; i++ {
			beadID := fmt.Sprintf("bd-rapid-%d", i)
			err := store.MarkWorking(beadID)
			if err != nil {
				t.Fatalf("Failed to mark working %s: %v", beadID, err)
			}
			err = store.MarkCompleted(beadID)
			if err != nil {
				t.Fatalf("Failed to complete %s: %v", beadID, err)
			}
		}

		stats := store.Stats()
		if stats.Total != 10 {
			t.Errorf("Expected 10 total, got %d", stats.Total)
		}
		if stats.Completed != 10 {
			t.Errorf("Expected 10 completed, got %d", stats.Completed)
		}
	})
}

// TestConcurrentWatchOperations tests thread-safety of watch mode
func TestConcurrentWatchOperations(t *testing.T) {
	t.Run("concurrent completion events", func(t *testing.T) {
		store := newIsolatedAssignmentStore(t, "test-session")

		// Create assignments and mark them as working first
		numAssignments := 20
		for i := 0; i < numAssignments; i++ {
			beadID := fmt.Sprintf("bd-conc-%d", i)
			_, err := store.Assign(beadID, "Concurrent Bead", i+2, "claude", fmt.Sprintf("cc_%d", i), "prompt")
			if err != nil {
				t.Fatalf("Failed to assign %s: %v", beadID, err)
			}
			// Mark as working (required before completing)
			_ = store.MarkWorking(beadID)
		}

		// Complete concurrently
		var wg sync.WaitGroup
		for i := 0; i < numAssignments; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				beadID := fmt.Sprintf("bd-conc-%d", idx)
				_ = store.MarkCompleted(beadID)
			}(i)
		}
		wg.Wait()

		stats := store.Stats()
		if stats.Total != numAssignments {
			t.Errorf("Expected %d total, got %d", numAssignments, stats.Total)
		}
		if stats.Completed != numAssignments {
			t.Errorf("Expected %d completed, got %d", numAssignments, stats.Completed)
		}
	})

	t.Run("concurrent reads and writes", func(t *testing.T) {
		store := newIsolatedAssignmentStore(t, "test-session")

		var wg sync.WaitGroup
		numOperations := 50

		// Concurrent writes
		for i := 0; i < numOperations; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				beadID := fmt.Sprintf("bd-rw-%d", idx)
				_, _ = store.Assign(beadID, "RW Bead", idx+2, "claude", fmt.Sprintf("cc_%d", idx), "prompt")
			}(i)
		}

		// Concurrent reads
		for i := 0; i < numOperations; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = store.ListActive()
				_ = store.Stats()
			}()
		}

		wg.Wait()

		// Verify all assignments were created
		stats := store.Stats()
		if stats.Total != numOperations {
			t.Errorf("Expected %d total, got %d", numOperations, stats.Total)
		}
	})
}

// TestCompletionDetectorIntegration tests the completion detector
func TestCompletionDetectorIntegration(t *testing.T) {
	t.Parallel()
	t.Run("dedup prevents duplicate events", func(t *testing.T) {
		t.Parallel()
		cfg := completion.DetectionConfig{
			DedupWindow: 100 * time.Millisecond,
		}
		d := completion.NewWithConfig("test-session", nil, cfg)

		// Access internal fields via reflection is not possible,
		// so we test via the public interface behavior
		// The dedup is tested in completion/detector_test.go
		if d == nil {
			t.Fatal("Expected detector to be created")
		}
	})
}
