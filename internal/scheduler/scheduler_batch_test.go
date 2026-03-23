package scheduler

import (
	"context"
	"testing"
	"time"
)

func newTestScheduler(t *testing.T) *Scheduler {
	t.Helper()
	cfg := DefaultConfig()
	cfg.MaxConcurrent = 2
	cfg.GlobalRateLimit.Rate = 100
	cfg.GlobalRateLimit.MinInterval = 0
	cfg.Headroom.Enabled = false

	s := New(cfg)
	// Set a no-op executor to prevent panics
	s.SetExecutor(func(ctx context.Context, job *SpawnJob) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
			return nil
		}
	})
	return s
}

func TestScheduler_SubmitBatch_Empty(t *testing.T) {
	t.Parallel()
	s := newTestScheduler(t)
	defer s.Stop()

	batchID, err := s.SubmitBatch(nil)
	if err != nil {
		t.Fatalf("SubmitBatch(nil) error: %v", err)
	}
	if batchID != "" {
		t.Errorf("SubmitBatch(nil) = %q; want empty", batchID)
	}
}

func TestScheduler_SubmitBatch_AssignsBatchID(t *testing.T) {
	t.Parallel()
	s := newTestScheduler(t)
	err := s.Start()
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	jobs := []*SpawnJob{
		NewSpawnJob("", JobTypeSession, "test-session"),
		NewSpawnJob("", JobTypeSession, "test-session"),
		NewSpawnJob("", JobTypeSession, "test-session"),
	}

	batchID, err := s.SubmitBatch(jobs)
	if err != nil {
		t.Fatalf("SubmitBatch error: %v", err)
	}
	if batchID == "" {
		t.Fatal("SubmitBatch returned empty batch ID")
	}

	for _, job := range jobs {
		if job.BatchID != batchID {
			t.Errorf("job %s has BatchID=%q; want %q", job.ID, job.BatchID, batchID)
		}
	}
}

func TestScheduler_CancelSession(t *testing.T) {
	t.Parallel()
	s := newTestScheduler(t)

	// Pause so jobs stay in queue
	s.Pause()

	err := s.Start()
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	// Submit jobs for two sessions
	for i := 0; i < 3; i++ {
		j := NewSpawnJob("", JobTypeSession, "session-A")
		if err := s.Submit(j); err != nil {
			t.Fatalf("Submit error: %v", err)
		}
	}
	for i := 0; i < 2; i++ {
		j := NewSpawnJob("", JobTypeSession, "session-B")
		if err := s.Submit(j); err != nil {
			t.Fatalf("Submit error: %v", err)
		}
	}

	// Cancel session-A
	cancelled := s.CancelSession("session-A")
	if cancelled != 3 {
		t.Errorf("CancelSession(%q) = %d; want 3", "session-A", cancelled)
	}

	// session-B should still be in queue
	remaining := s.GetQueuedJobs()
	for _, j := range remaining {
		if j.SessionName == "session-A" {
			t.Errorf("found session-A job %s still in queue after cancel", j.ID)
		}
	}
}

func TestScheduler_CancelBatch(t *testing.T) {
	t.Parallel()
	s := newTestScheduler(t)

	// Pause so jobs stay in queue
	s.Pause()

	err := s.Start()
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	// Submit a batch
	jobs := []*SpawnJob{
		NewSpawnJob("", JobTypeSession, "test"),
		NewSpawnJob("", JobTypeSession, "test"),
	}
	batchID, err := s.SubmitBatch(jobs)
	if err != nil {
		t.Fatalf("SubmitBatch error: %v", err)
	}

	// Submit a non-batch job
	extra := NewSpawnJob("", JobTypeSession, "test")
	if err := s.Submit(extra); err != nil {
		t.Fatalf("Submit error: %v", err)
	}

	// Cancel the batch
	cancelled := s.CancelBatch(batchID)
	if cancelled != 2 {
		t.Errorf("CancelBatch(%q) = %d; want 2", batchID, cancelled)
	}

	// The non-batch job should remain
	remaining := s.GetQueuedJobs()
	if len(remaining) != 1 {
		t.Errorf("expected 1 remaining job, got %d", len(remaining))
	}
}

func TestScheduler_GetJob_FromQueue(t *testing.T) {
	t.Parallel()
	s := newTestScheduler(t)

	// Pause so jobs stay in queue
	s.Pause()

	err := s.Start()
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	job := NewSpawnJob("", JobTypeSession, "test-session")
	if err := s.Submit(job); err != nil {
		t.Fatalf("Submit error: %v", err)
	}
	// Submit assigns an ID if empty
	jobID := job.ID

	got := s.GetJob(jobID)
	if got == nil {
		t.Fatalf("GetJob(%q) = nil; want job", jobID)
	}
	if got.ID != jobID {
		t.Errorf("GetJob(%q).ID = %q", jobID, got.ID)
	}
	if got.SessionName != "test-session" {
		t.Errorf("GetJob(%q).SessionName = %q; want %q", jobID, got.SessionName, "test-session")
	}
}

func TestScheduler_GetJob_Missing(t *testing.T) {
	t.Parallel()
	s := newTestScheduler(t)
	defer s.Stop()

	got := s.GetJob("nonexistent-id")
	if got != nil {
		t.Errorf("GetJob(%q) = %v; want nil", "nonexistent-id", got)
	}
}

func TestScheduler_GetJob_FromCompleted(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	cfg.MaxConcurrent = 1
	cfg.GlobalRateLimit.Rate = 100
	cfg.GlobalRateLimit.MinInterval = 0
	cfg.Headroom.Enabled = false

	s := New(cfg)

	// Quick executor that completes immediately
	s.SetExecutor(func(ctx context.Context, job *SpawnJob) error {
		return nil
	})

	err := s.Start()
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer s.Stop()

	job := NewSpawnJob("", JobTypeSession, "test")
	if err := s.Submit(job); err != nil {
		t.Fatalf("Submit error: %v", err)
	}
	// Submit assigns an ID if empty
	jobID := job.ID

	// Wait for the job to complete
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got := s.GetJob(jobID)
		if got != nil && got.Status == StatusCompleted {
			return // Success
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("job %s did not complete within deadline", jobID)
}

func TestScheduler_CancelSession_None(t *testing.T) {
	t.Parallel()
	s := newTestScheduler(t)
	defer s.Stop()

	cancelled := s.CancelSession("nonexistent-session")
	if cancelled != 0 {
		t.Errorf("CancelSession(%q) = %d; want 0", "nonexistent-session", cancelled)
	}
}
