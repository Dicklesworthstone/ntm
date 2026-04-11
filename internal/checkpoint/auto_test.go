package checkpoint

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
	worker.Start(context.TODO())
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

func TestBackgroundWorker_RestartsAfterParentContextCancellation(t *testing.T) {
	config := AutoCheckpointConfig{
		Enabled:         true,
		IntervalMinutes: 0,
		OnRotation:      true,
		OnError:         true,
	}

	worker := NewBackgroundWorker("test-session", config)
	parentCtx, cancel := context.WithCancel(context.Background())

	worker.Start(parentCtx)
	cancel()

	deadline := time.Now().Add(time.Second)
	for {
		worker.mu.Lock()
		running := worker.started || worker.cancel != nil
		worker.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not fully stop after parent context cancellation")
		}
		time.Sleep(10 * time.Millisecond)
	}

	worker.Start(context.Background())

	worker.mu.Lock()
	restarted := worker.started && worker.cancel != nil
	worker.mu.Unlock()
	if !restarted {
		t.Fatal("worker did not restart after parent context cancellation")
	}

	worker.Stop()
}

func TestBackgroundWorker_SendEventDoesNothingWhenInactive(t *testing.T) {
	config := AutoCheckpointConfig{
		Enabled:         true,
		IntervalMinutes: 0,
		OnRotation:      true,
		OnError:         true,
	}

	worker := NewBackgroundWorker("test-session", config)
	worker.SendEvent(AutoEvent{Type: EventRotation, SessionName: "test-session"})

	if got := len(worker.events); got != 0 {
		t.Fatalf("expected no queued events for inactive worker, got %d", got)
	}
}

func TestBackgroundWorker_StartDrainsStaleEvents(t *testing.T) {
	config := AutoCheckpointConfig{
		Enabled:         true,
		IntervalMinutes: 0,
		OnRotation:      true,
	}

	worker := NewBackgroundWorker("missing-session", config)
	worker.events <- AutoEvent{
		Type:        EventRotation,
		SessionName: "missing-session",
		Description: "stale",
	}

	worker.Start(context.Background())
	time.Sleep(50 * time.Millisecond)
	worker.Stop()

	count, _, lastErr := worker.Stats()
	if count != 0 {
		t.Fatalf("expected stale event to be discarded, got checkpoint count %d", count)
	}
	if lastErr != nil {
		t.Fatalf("expected stale event to be drained before restart, got error %v", lastErr)
	}
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

func TestAutoCheckpointer_GetLastAutoCheckpoint_RejectsInvalidNewestAutoCheckpoint(t *testing.T) {
	t.Parallel()

	storage := NewStorageWithDir(t.TempDir())
	checkpointer := &AutoCheckpointer{
		capturer: NewCapturerWithStorage(storage),
		storage:  storage,
	}
	session := "auto-selection-session"

	valid := &Checkpoint{
		ID:          "20260101-120000-0001-auto-interval",
		Name:        "auto-interval",
		SessionName: session,
		CreatedAt:   time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		Session:     SessionState{},
	}
	if err := storage.Save(valid); err != nil {
		t.Fatalf("Save(%s): %v", valid.ID, err)
	}
	validDir := storage.CheckpointDir(session, valid.ID)
	validTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(validDir, validTime, validTime); err != nil {
		t.Fatalf("Chtimes(%s): %v", validDir, err)
	}

	sessionDir := filepath.Join(storage.BaseDir, session)
	invalidID := "20260101-130000-0002-auto-error"
	invalidDir := filepath.Join(sessionDir, invalidID)
	if err := os.MkdirAll(invalidDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", invalidDir, err)
	}
	if err := os.WriteFile(filepath.Join(invalidDir, MetadataFile), []byte("{"), 0o600); err != nil {
		t.Fatalf("WriteFile(metadata): %v", err)
	}
	invalidTime := time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC)
	if err := os.Chtimes(invalidDir, invalidTime, invalidTime); err != nil {
		t.Fatalf("Chtimes(%s): %v", invalidDir, err)
	}

	_, err := checkpointer.GetLastAutoCheckpoint(session)
	if err == nil {
		t.Fatal("GetLastAutoCheckpoint() error = nil, want invalid newer auto-checkpoint rejection")
	}
	if !strings.Contains(err.Error(), "latest auto-checkpoint blocked by invalid checkpoint") {
		t.Fatalf("GetLastAutoCheckpoint() error = %v, want invalid newer auto-checkpoint context", err)
	}
}

func TestAutoCheckpointer_ListAutoCheckpoints_RejectsInvalidNewestAutoCheckpoint(t *testing.T) {
	t.Parallel()

	storage := NewStorageWithDir(t.TempDir())
	checkpointer := &AutoCheckpointer{
		capturer: NewCapturerWithStorage(storage),
		storage:  storage,
	}
	session := "auto-list-selection-session"

	valid := &Checkpoint{
		ID:          "20260101-120000-0001-auto-interval",
		Name:        "auto-interval",
		SessionName: session,
		CreatedAt:   time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		Session:     SessionState{},
	}
	if err := storage.Save(valid); err != nil {
		t.Fatalf("Save(%s): %v", valid.ID, err)
	}
	validDir := storage.CheckpointDir(session, valid.ID)
	validTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(validDir, validTime, validTime); err != nil {
		t.Fatalf("Chtimes(%s): %v", validDir, err)
	}

	sessionDir := filepath.Join(storage.BaseDir, session)
	invalidID := "20260101-130000-0002-auto-error"
	invalidDir := filepath.Join(sessionDir, invalidID)
	if err := os.MkdirAll(invalidDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", invalidDir, err)
	}
	if err := os.WriteFile(filepath.Join(invalidDir, MetadataFile), []byte("{"), 0o600); err != nil {
		t.Fatalf("WriteFile(metadata): %v", err)
	}
	invalidTime := time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC)
	if err := os.Chtimes(invalidDir, invalidTime, invalidTime); err != nil {
		t.Fatalf("Chtimes(%s): %v", invalidDir, err)
	}

	_, err := checkpointer.ListAutoCheckpoints(session)
	if err == nil {
		t.Fatal("ListAutoCheckpoints() error = nil, want invalid newer auto-checkpoint rejection")
	}
	if !strings.Contains(err.Error(), "listing auto-checkpoints blocked by invalid checkpoint") {
		t.Fatalf("ListAutoCheckpoints() error = %v, want invalid newer auto-checkpoint context", err)
	}
}

func TestAutoCheckpointer_GetLastAutoCheckpoint_IgnoresInvalidNewerManualCheckpoint(t *testing.T) {
	t.Parallel()

	storage := NewStorageWithDir(t.TempDir())
	checkpointer := &AutoCheckpointer{
		capturer: NewCapturerWithStorage(storage),
		storage:  storage,
	}
	session := "auto-selection-manual-session"

	valid := &Checkpoint{
		ID:          "20260101-120000-0001-auto-interval",
		Name:        "auto-interval",
		SessionName: session,
		CreatedAt:   time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		Session:     SessionState{},
	}
	if err := storage.Save(valid); err != nil {
		t.Fatalf("Save(%s): %v", valid.ID, err)
	}
	validDir := storage.CheckpointDir(session, valid.ID)
	validTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(validDir, validTime, validTime); err != nil {
		t.Fatalf("Chtimes(%s): %v", validDir, err)
	}

	sessionDir := filepath.Join(storage.BaseDir, session)
	invalidID := "20260101-130000-0002-manual-latest"
	invalidDir := filepath.Join(sessionDir, invalidID)
	if err := os.MkdirAll(invalidDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", invalidDir, err)
	}
	if err := os.WriteFile(filepath.Join(invalidDir, MetadataFile), []byte("{"), 0o600); err != nil {
		t.Fatalf("WriteFile(metadata): %v", err)
	}
	invalidTime := time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC)
	if err := os.Chtimes(invalidDir, invalidTime, invalidTime); err != nil {
		t.Fatalf("Chtimes(%s): %v", invalidDir, err)
	}

	got, err := checkpointer.GetLastAutoCheckpoint(session)
	if err != nil {
		t.Fatalf("GetLastAutoCheckpoint(): %v", err)
	}
	if got.ID != valid.ID {
		t.Fatalf("GetLastAutoCheckpoint() ID = %q, want %q", got.ID, valid.ID)
	}
}

func TestAutoCheckpointer_ListAutoCheckpoints_IgnoresInvalidNewerManualCheckpoint(t *testing.T) {
	t.Parallel()

	storage := NewStorageWithDir(t.TempDir())
	checkpointer := &AutoCheckpointer{
		capturer: NewCapturerWithStorage(storage),
		storage:  storage,
	}
	session := "auto-list-selection-manual-session"

	valid := &Checkpoint{
		ID:          "20260101-120000-0001-auto-interval",
		Name:        "auto-interval",
		SessionName: session,
		CreatedAt:   time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		Session:     SessionState{},
	}
	if err := storage.Save(valid); err != nil {
		t.Fatalf("Save(%s): %v", valid.ID, err)
	}
	validDir := storage.CheckpointDir(session, valid.ID)
	validTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(validDir, validTime, validTime); err != nil {
		t.Fatalf("Chtimes(%s): %v", validDir, err)
	}

	sessionDir := filepath.Join(storage.BaseDir, session)
	invalidID := "20260101-130000-0002-manual-latest"
	invalidDir := filepath.Join(sessionDir, invalidID)
	if err := os.MkdirAll(invalidDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", invalidDir, err)
	}
	if err := os.WriteFile(filepath.Join(invalidDir, MetadataFile), []byte("{"), 0o600); err != nil {
		t.Fatalf("WriteFile(metadata): %v", err)
	}
	invalidTime := time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC)
	if err := os.Chtimes(invalidDir, invalidTime, invalidTime); err != nil {
		t.Fatalf("Chtimes(%s): %v", invalidDir, err)
	}

	got, err := checkpointer.ListAutoCheckpoints(session)
	if err != nil {
		t.Fatalf("ListAutoCheckpoints(): %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListAutoCheckpoints() len = %d, want 1", len(got))
	}
	if got[0].ID != valid.ID {
		t.Fatalf("ListAutoCheckpoints()[0].ID = %q, want %q", got[0].ID, valid.ID)
	}
}

func TestAutoCheckpointer_RotateAutoCheckpoints_RejectsInvalidRetainedAutoCheckpoint(t *testing.T) {
	t.Parallel()

	storage := NewStorageWithDir(t.TempDir())
	checkpointer := &AutoCheckpointer{
		capturer: NewCapturerWithStorage(storage),
		storage:  storage,
	}
	session := "auto-rotate-retained-session"

	valid := &Checkpoint{
		ID:          "20260101-120000-0001-auto-interval",
		Name:        "auto-interval",
		SessionName: session,
		CreatedAt:   time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		Session:     SessionState{},
	}
	if err := storage.Save(valid); err != nil {
		t.Fatalf("Save(%s): %v", valid.ID, err)
	}
	validDir := storage.CheckpointDir(session, valid.ID)
	validTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(validDir, validTime, validTime); err != nil {
		t.Fatalf("Chtimes(%s): %v", validDir, err)
	}

	sessionDir := filepath.Join(storage.BaseDir, session)
	invalidID := "20260101-130000-0002-auto-error"
	invalidDir := filepath.Join(sessionDir, invalidID)
	if err := os.MkdirAll(invalidDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", invalidDir, err)
	}
	if err := os.WriteFile(filepath.Join(invalidDir, MetadataFile), []byte("{"), 0o600); err != nil {
		t.Fatalf("WriteFile(metadata): %v", err)
	}
	invalidTime := time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC)
	if err := os.Chtimes(invalidDir, invalidTime, invalidTime); err != nil {
		t.Fatalf("Chtimes(%s): %v", invalidDir, err)
	}

	err := checkpointer.rotateAutoCheckpoints(session, 1)
	if err == nil {
		t.Fatal("rotateAutoCheckpoints() error = nil, want invalid retained auto-checkpoint rejection")
	}
	if !strings.Contains(err.Error(), "auto-checkpoint rotation blocked by invalid retained checkpoint") {
		t.Fatalf("rotateAutoCheckpoints() error = %v, want retained invalid checkpoint context", err)
	}
	if !storage.Exists(session, valid.ID) {
		t.Fatal("valid older auto-checkpoint was deleted despite retained invalid checkpoint error")
	}
}

func TestAutoCheckpointer_RotateAutoCheckpoints_DeletesInvalidOverflowAutoCheckpoint(t *testing.T) {
	t.Parallel()

	storage := NewStorageWithDir(t.TempDir())
	checkpointer := &AutoCheckpointer{
		capturer: NewCapturerWithStorage(storage),
		storage:  storage,
	}
	session := "auto-rotate-overflow-session"

	newest := &Checkpoint{
		ID:          "20260101-140000-0001-auto-interval",
		Name:        "auto-interval",
		SessionName: session,
		CreatedAt:   time.Date(2026, 1, 1, 14, 0, 0, 0, time.UTC),
		Session:     SessionState{},
	}
	if err := storage.Save(newest); err != nil {
		t.Fatalf("Save(%s): %v", newest.ID, err)
	}
	newestDir := storage.CheckpointDir(session, newest.ID)
	newestTime := time.Date(2026, 1, 1, 14, 0, 0, 0, time.UTC)
	if err := os.Chtimes(newestDir, newestTime, newestTime); err != nil {
		t.Fatalf("Chtimes(%s): %v", newestDir, err)
	}

	mid := &Checkpoint{
		ID:          "20260101-130000-0002-auto-error",
		Name:        "auto-error",
		SessionName: session,
		CreatedAt:   time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC),
		Session:     SessionState{},
	}
	if err := storage.Save(mid); err != nil {
		t.Fatalf("Save(%s): %v", mid.ID, err)
	}
	midDir := storage.CheckpointDir(session, mid.ID)
	midTime := time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC)
	if err := os.Chtimes(midDir, midTime, midTime); err != nil {
		t.Fatalf("Chtimes(%s): %v", midDir, err)
	}

	sessionDir := filepath.Join(storage.BaseDir, session)
	overflowID := "20260101-120000-0003-auto-rotation"
	overflowPath := filepath.Join(sessionDir, overflowID)
	if err := os.WriteFile(overflowPath, []byte("broken auto-checkpoint path"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", overflowPath, err)
	}
	overflowTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(overflowPath, overflowTime, overflowTime); err != nil {
		t.Fatalf("Chtimes(%s): %v", overflowPath, err)
	}

	if err := checkpointer.rotateAutoCheckpoints(session, 2); err != nil {
		t.Fatalf("rotateAutoCheckpoints(): %v", err)
	}

	exists, err := storage.HasCheckpointPath(session, overflowID)
	if err != nil {
		t.Fatalf("HasCheckpointPath(%s): %v", overflowID, err)
	}
	if exists {
		t.Fatal("invalid overflow auto-checkpoint path still exists after rotation")
	}
	if !storage.Exists(session, newest.ID) || !storage.Exists(session, mid.ID) {
		t.Fatal("valid retained auto-checkpoints were not preserved")
	}
}
