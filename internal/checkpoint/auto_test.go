package checkpoint

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestAutoCheckpointReason_String(t *testing.T) {
	tests := []struct {
		reason AutoCheckpointReason
		want   string
	}{
		{ReasonBroadcast, "broadcast"},
		{ReasonAddAgents, "add_agents"},
		{ReasonSpawn, "spawn"},
		{ReasonRiskyOp, "risky_op"},
		{ReasonInterval, "interval"},
		{ReasonRotation, "rotation"},
		{ReasonError, "error"},
	}

	for _, tt := range tests {
		if string(tt.reason) != tt.want {
			t.Errorf("AutoCheckpointReason = %q, want %q", tt.reason, tt.want)
		}
	}
}

func TestIsAutoCheckpoint(t *testing.T) {
	tests := []struct {
		name       string
		checkpoint *Checkpoint
		want       bool
	}{
		{
			name:       "auto prefix in name",
			checkpoint: &Checkpoint{Name: "auto-broadcast"},
			want:       true,
		},
		{
			name:       "auto-checkpoint in description",
			checkpoint: &Checkpoint{Name: "manual", Description: "Auto-checkpoint: test"},
			want:       true,
		},
		{
			name:       "manual checkpoint",
			checkpoint: &Checkpoint{Name: "before-refactor", Description: "Manual save"},
			want:       false,
		},
		{
			name:       "empty checkpoint",
			checkpoint: &Checkpoint{},
			want:       false,
		},
		{
			name:       "automation name should not match",
			checkpoint: &Checkpoint{Name: "automation-backup", Description: "User created"},
			want:       false,
		},
		{
			name:       "automatic name should not match",
			checkpoint: &Checkpoint{Name: "automatic", Description: "User created"},
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAutoCheckpoint(tt.checkpoint); got != tt.want {
				t.Errorf("isAutoCheckpoint() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBackgroundWorker_DisabledConfig(t *testing.T) {
	config := AutoCheckpointConfig{
		Enabled: false,
	}

	worker := NewBackgroundWorker("test-session", config)
	ctx := context.Background()

	// Start should return immediately when disabled
	worker.Start(ctx)

	// Worker should not be running (no goroutine started)
	// This is verified by the fact that Stop doesn't block
	worker.Stop()
}

func TestBackgroundWorker_StartStop(t *testing.T) {
	config := AutoCheckpointConfig{
		Enabled:         true,
		IntervalMinutes: 0, // Disabled interval
		OnRotation:      true,
		OnError:         true,
	}

	worker := NewBackgroundWorker("test-session", config)
	ctx := context.Background()

	worker.Start(ctx)

	// Give worker time to start
	time.Sleep(10 * time.Millisecond)

	// Stop should complete without blocking
	done := make(chan struct{})
	go func() {
		worker.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(time.Second):
		t.Fatal("Stop() blocked for too long")
	}
}

func TestBackgroundWorkerStart_NilContextAndDoubleStartAreSafe(t *testing.T) {
	config := AutoCheckpointConfig{
		Enabled:         true,
		IntervalMinutes: 0,
		OnRotation:      true,
		OnError:         true,
	}

	worker := NewBackgroundWorker("test-session", config)

	// Should not panic.
	worker.Start(nil)
	worker.Start(context.Background()) // idempotent

	done := make(chan struct{})
	go func() {
		worker.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop() blocked for too long after Start(nil) + Start()")
	}

	// Stop should be safe to call again.
	worker.Stop()
}

func TestBackgroundWorker_EventChannel(t *testing.T) {
	config := AutoCheckpointConfig{
		Enabled:    true,
		OnRotation: false, // Disabled - events should be ignored
		OnError:    false,
	}

	worker := NewBackgroundWorker("test-session", config)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	worker.Start(ctx)
	defer worker.Stop()

	// Send events - they should not block even if not processed
	for i := 0; i < 20; i++ {
		worker.SendEvent(AutoEvent{
			Type:        EventRotation,
			SessionName: "test-session",
		})
	}

	// Give time for events to be processed
	time.Sleep(50 * time.Millisecond)

	// Stats should show no checkpoints created (events were ignored)
	count, _, _ := worker.Stats()
	if count != 0 {
		t.Errorf("Expected 0 checkpoints, got %d", count)
	}
}

func TestBackgroundWorker_Stats(t *testing.T) {
	config := AutoCheckpointConfig{
		Enabled: true,
	}

	worker := NewBackgroundWorker("test-session", config)

	// Initial stats should be zero
	count, lastTime, lastErr := worker.Stats()
	if count != 0 {
		t.Errorf("Initial count = %d, want 0", count)
	}
	if !lastTime.IsZero() {
		t.Errorf("Initial lastTime should be zero")
	}
	if lastErr != nil {
		t.Errorf("Initial lastErr = %v, want nil", lastErr)
	}
}

func TestWorkerRegistry_Basic(t *testing.T) {
	registry := NewWorkerRegistry()

	config := AutoCheckpointConfig{
		Enabled: true,
	}

	ctx := context.Background()

	// Start a worker
	registry.StartWorker(ctx, "session1", config)

	// Should be able to get the worker
	worker := registry.GetWorker("session1")
	if worker == nil {
		t.Fatal("Expected to get worker for session1")
	}

	// Non-existent session should return nil
	if registry.GetWorker("nonexistent") != nil {
		t.Error("Expected nil for non-existent session")
	}

	// Stop the worker
	registry.StopWorker("session1")

	// Worker should be removed
	if registry.GetWorker("session1") != nil {
		t.Error("Expected nil after stopping worker")
	}
}

func TestWorkerRegistry_ReplaceWorker(t *testing.T) {
	registry := NewWorkerRegistry()

	config := AutoCheckpointConfig{
		Enabled: true,
	}

	ctx := context.Background()

	// Start first worker
	registry.StartWorker(ctx, "session1", config)
	worker1 := registry.GetWorker("session1")

	// Start second worker for same session (should replace)
	registry.StartWorker(ctx, "session1", config)
	worker2 := registry.GetWorker("session1")

	// Should be different worker instances
	if worker1 == worker2 {
		t.Error("Expected different worker instance after replacement")
	}

	registry.StopAll()
}

func TestWorkerRegistry_StopAll(t *testing.T) {
	registry := NewWorkerRegistry()

	config := AutoCheckpointConfig{
		Enabled: true,
	}

	ctx := context.Background()

	// Start multiple workers
	registry.StartWorker(ctx, "session1", config)
	registry.StartWorker(ctx, "session2", config)
	registry.StartWorker(ctx, "session3", config)

	// Stop all
	registry.StopAll()

	// All workers should be removed
	for _, name := range []string{"session1", "session2", "session3"} {
		if registry.GetWorker(name) != nil {
			t.Errorf("Expected nil for %s after StopAll", name)
		}
	}
}

func TestWorkerRegistry_SendEvent(t *testing.T) {
	registry := NewWorkerRegistry()

	config := AutoCheckpointConfig{
		Enabled:    true,
		OnRotation: true,
	}

	ctx := context.Background()
	registry.StartWorker(ctx, "session1", config)
	defer registry.StopAll()

	// Send event to existing session - should not panic
	registry.SendEvent("session1", AutoEvent{
		Type:        EventRotation,
		Description: "test rotation",
	})

	// Send event to non-existent session - should not panic
	registry.SendEvent("nonexistent", AutoEvent{
		Type: EventError,
	})
}

func TestAutoCheckpointOptions_ReasonNaming(t *testing.T) {
	// Test that checkpoint names are generated correctly from reasons
	tests := []struct {
		reason   AutoCheckpointReason
		wantName string
	}{
		{ReasonBroadcast, "auto-broadcast"},
		{ReasonInterval, "auto-interval"},
		{ReasonRotation, "auto-rotation"},
		{ReasonError, "auto-error"},
	}

	for _, tt := range tests {
		t.Run(string(tt.reason), func(t *testing.T) {
			name := AutoCheckpointPrefix + "-" + string(tt.reason)
			if name != tt.wantName {
				t.Errorf("Name = %q, want %q", name, tt.wantName)
			}
		})
	}
}

func TestAutoCheckpointer_Integration(t *testing.T) {
	// Skip if no tmux available (CI environment)
	if os.Getenv("CI") != "" {
		t.Skip("Skipping integration test in CI")
	}

	tmpDir, err := os.MkdirTemp("", "ntm-auto-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)
	checkpointer := &AutoCheckpointer{
		capturer: NewCapturerWithStorage(storage),
		storage:  storage,
	}

	// Test ListAutoCheckpoints with empty storage
	autos, err := checkpointer.ListAutoCheckpoints("test-session")
	if err != nil {
		t.Fatalf("ListAutoCheckpoints failed: %v", err)
	}
	if len(autos) != 0 {
		t.Errorf("Expected 0 auto-checkpoints, got %d", len(autos))
	}

	// Test GetLastAutoCheckpoint with no checkpoints
	_, err = checkpointer.GetLastAutoCheckpoint("test-session")
	if err == nil {
		t.Error("Expected error for GetLastAutoCheckpoint with no checkpoints")
	}

	// Test TimeSinceLastAutoCheckpoint with no checkpoints
	dur := checkpointer.TimeSinceLastAutoCheckpoint("test-session")
	if dur != 0 {
		t.Errorf("Expected 0 duration, got %v", dur)
	}
}
