package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

// ─── SpawnJob pure method tests ────────────────────────────────────────────────

func TestSpawnJob_SetError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		err       error
		wantError string
	}{
		{"non-nil error", errors.New("something broke"), "something broke"},
		{"nil error", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			job := NewSpawnJob("j1", JobTypeSession, "sess")
			job.SetError(tt.err)
			if job.Error != tt.wantError {
				t.Errorf("Error = %q, want %q", job.Error, tt.wantError)
			}
		})
	}
}

func TestSpawnJob_SetError_PreservesExisting(t *testing.T) {
	t.Parallel()
	job := NewSpawnJob("j1", JobTypeSession, "sess")
	job.SetError(errors.New("first"))
	if job.Error != "first" {
		t.Fatalf("expected 'first', got %q", job.Error)
	}
	// Setting nil should NOT overwrite (the code only writes when err != nil)
	job.SetError(nil)
	if job.Error != "first" {
		t.Errorf("nil SetError should not overwrite; got %q", job.Error)
	}
}

func TestSpawnJob_TotalDuration_Completed(t *testing.T) {
	t.Parallel()
	job := NewSpawnJob("j1", JobTypeSession, "sess")
	start := time.Now().Add(-5 * time.Second)
	job.mu.Lock()
	job.CreatedAt = start
	job.CompletedAt = start.Add(3 * time.Second)
	job.mu.Unlock()

	dur := job.TotalDuration()
	if dur != 3*time.Second {
		t.Errorf("TotalDuration = %v, want 3s", dur)
	}
}

func TestSpawnJob_TotalDuration_InProgress(t *testing.T) {
	t.Parallel()
	job := NewSpawnJob("j1", JobTypeSession, "sess")
	job.mu.Lock()
	job.CreatedAt = time.Now().Add(-2 * time.Second)
	job.mu.Unlock()

	dur := job.TotalDuration()
	if dur < time.Second {
		t.Errorf("TotalDuration for in-progress job too short: %v", dur)
	}
}

func TestSpawnJob_QueueDuration_BeforeScheduled(t *testing.T) {
	t.Parallel()
	job := NewSpawnJob("j1", JobTypeSession, "sess")
	job.mu.Lock()
	job.CreatedAt = time.Now().Add(-500 * time.Millisecond)
	job.mu.Unlock()

	dur := job.QueueDuration()
	if dur < 400*time.Millisecond {
		t.Errorf("QueueDuration should reflect time since creation, got %v", dur)
	}
}

func TestSpawnJob_QueueDuration_AfterScheduled(t *testing.T) {
	t.Parallel()
	job := NewSpawnJob("j1", JobTypeSession, "sess")
	created := time.Now().Add(-5 * time.Second)
	scheduled := created.Add(2 * time.Second)
	job.mu.Lock()
	job.CreatedAt = created
	job.ScheduledAt = scheduled
	job.mu.Unlock()

	dur := job.QueueDuration()
	if dur != 2*time.Second {
		t.Errorf("QueueDuration = %v, want 2s", dur)
	}
}

func TestSpawnJob_ExecutionDuration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		started   time.Time
		completed time.Time
		minDur    time.Duration
		maxDur    time.Duration
	}{
		{"not started", time.Time{}, time.Time{}, 0, time.Millisecond},
		{
			"completed",
			time.Now().Add(-3 * time.Second),
			time.Now().Add(-1 * time.Second),
			2*time.Second - time.Millisecond,
			2*time.Second + time.Millisecond,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			job := NewSpawnJob("j1", JobTypeSession, "sess")
			job.mu.Lock()
			job.StartedAt = tt.started
			job.CompletedAt = tt.completed
			job.mu.Unlock()

			dur := job.ExecutionDuration()
			if dur < tt.minDur || dur > tt.maxDur {
				t.Errorf("ExecutionDuration = %v, want between %v and %v", dur, tt.minDur, tt.maxDur)
			}
		})
	}
}

func TestSpawnJob_ExecutionDuration_InProgress(t *testing.T) {
	t.Parallel()
	job := NewSpawnJob("j1", JobTypeSession, "sess")
	job.mu.Lock()
	job.StartedAt = time.Now().Add(-time.Second)
	job.mu.Unlock()

	dur := job.ExecutionDuration()
	if dur < 900*time.Millisecond {
		t.Errorf("expected running job to show ~1s duration, got %v", dur)
	}
}

func TestSpawnJob_Clone(t *testing.T) {
	t.Parallel()
	original := NewSpawnJob("orig", JobTypePaneSplit, "sess1")
	original.AgentType = "cc"
	original.PaneIndex = 3
	original.Directory = "/tmp"
	original.BatchID = "batch-1"
	original.ParentJobID = "parent-1"
	original.MaxRetries = 5
	original.RetryCount = 2
	original.RetryDelay = 500 * time.Millisecond
	original.Metadata["key1"] = "val1"
	original.Metadata["key2"] = 42
	original.Result = &SpawnResult{
		SessionName: "sess1",
		PaneID:      "pane-3",
		PaneIndex:   3,
		AgentType:   "cc",
		Duration:    2 * time.Second,
	}
	original.SetStatus(StatusRunning)
	original.SetError(errors.New("test error"))

	clone := original.Clone()

	// Verify all fields
	if clone.ID != original.ID {
		t.Errorf("ID = %q, want %q", clone.ID, original.ID)
	}
	if clone.Type != original.Type {
		t.Errorf("Type = %v, want %v", clone.Type, original.Type)
	}
	if clone.Priority != original.Priority {
		t.Errorf("Priority = %v, want %v", clone.Priority, original.Priority)
	}
	if clone.SessionName != original.SessionName {
		t.Errorf("SessionName = %q, want %q", clone.SessionName, original.SessionName)
	}
	if clone.AgentType != original.AgentType {
		t.Errorf("AgentType = %q, want %q", clone.AgentType, original.AgentType)
	}
	if clone.PaneIndex != original.PaneIndex {
		t.Errorf("PaneIndex = %d, want %d", clone.PaneIndex, original.PaneIndex)
	}
	if clone.Directory != original.Directory {
		t.Errorf("Directory = %q, want %q", clone.Directory, original.Directory)
	}
	if clone.Status != original.Status {
		t.Errorf("Status = %v, want %v", clone.Status, original.Status)
	}
	if clone.Error != original.Error {
		t.Errorf("Error = %q, want %q", clone.Error, original.Error)
	}
	if clone.RetryCount != original.RetryCount {
		t.Errorf("RetryCount = %d, want %d", clone.RetryCount, original.RetryCount)
	}
	if clone.MaxRetries != original.MaxRetries {
		t.Errorf("MaxRetries = %d, want %d", clone.MaxRetries, original.MaxRetries)
	}
	if clone.BatchID != original.BatchID {
		t.Errorf("BatchID = %q, want %q", clone.BatchID, original.BatchID)
	}
	if clone.ParentJobID != original.ParentJobID {
		t.Errorf("ParentJobID = %q, want %q", clone.ParentJobID, original.ParentJobID)
	}

	// Metadata should be a deep copy
	if clone.Metadata["key1"] != "val1" {
		t.Error("Metadata not cloned correctly")
	}
	clone.Metadata["key1"] = "modified"
	if original.Metadata["key1"] == "modified" {
		t.Error("Clone metadata is shared with original")
	}

	// Result should be a deep copy
	if clone.Result == nil {
		t.Fatal("Result should be cloned")
	}
	if clone.Result.SessionName != "sess1" {
		t.Errorf("Result.SessionName = %q, want sess1", clone.Result.SessionName)
	}
	clone.Result.PaneID = "modified"
	if original.Result.PaneID == "modified" {
		t.Error("Clone result is shared with original")
	}

	// Callback and context should NOT be cloned
	if clone.Callback != nil {
		t.Error("Callback should not be cloned")
	}
	if clone.ctx != nil {
		t.Error("ctx should not be cloned")
	}
}

func TestSpawnJob_Clone_NilMetadataAndResult(t *testing.T) {
	t.Parallel()
	job := NewSpawnJob("j1", JobTypeSession, "sess")
	job.Metadata = nil
	job.Result = nil

	clone := job.Clone()
	if clone.Metadata != nil {
		t.Error("nil Metadata should clone as nil")
	}
	if clone.Result != nil {
		t.Error("nil Result should clone as nil")
	}
}

// ─── JobQueue pure method tests ────────────────────────────────────────────────

func TestJobQueue_Peek(t *testing.T) {
	t.Parallel()

	t.Run("empty queue", func(t *testing.T) {
		t.Parallel()
		q := NewJobQueue()
		if q.Peek() != nil {
			t.Error("Peek on empty queue should return nil")
		}
	})

	t.Run("returns highest priority", func(t *testing.T) {
		t.Parallel()
		q := NewJobQueue()
		low := NewSpawnJob("low", JobTypeSession, "s")
		low.Priority = PriorityLow
		high := NewSpawnJob("high", JobTypeSession, "s")
		high.Priority = PriorityHigh

		q.Enqueue(low)
		q.Enqueue(high)

		got := q.Peek()
		if got == nil || got.ID != "high" {
			t.Errorf("Peek = %v, want 'high'", got)
		}
		// Peek should not remove
		if q.Len() != 2 {
			t.Errorf("Peek should not remove; Len = %d, want 2", q.Len())
		}
	})
}

func TestJobQueue_Get(t *testing.T) {
	t.Parallel()
	q := NewJobQueue()
	job := NewSpawnJob("find-me", JobTypeSession, "s")
	q.Enqueue(job)

	if q.Get("find-me") == nil {
		t.Error("Get should find existing job")
	}
	if q.Get("not-here") != nil {
		t.Error("Get should return nil for missing job")
	}
}

func TestJobQueue_ListAll(t *testing.T) {
	t.Parallel()
	q := NewJobQueue()
	for i := 0; i < 5; i++ {
		q.Enqueue(NewSpawnJob(fmt.Sprintf("j%d", i), JobTypeSession, "s"))
	}

	all := q.ListAll()
	if len(all) != 5 {
		t.Errorf("ListAll returned %d items, want 5", len(all))
	}
}

func TestJobQueue_ListAllReturnsPriorityOrder(t *testing.T) {
	t.Parallel()

	q := NewJobQueue()
	now := time.Now()
	blocked := NewSpawnJob("blocked", JobTypeSession, "blocked-session")
	blocked.Priority = PriorityUrgent
	blocked.CreatedAt = now
	low := NewSpawnJob("low", JobTypeSession, "low-session")
	low.Priority = PriorityLow
	low.CreatedAt = now.Add(time.Second)
	high := NewSpawnJob("high", JobTypeSession, "high-session")
	high.Priority = PriorityHigh
	high.CreatedAt = now.Add(2 * time.Second)

	q.Enqueue(blocked)
	q.Enqueue(low)
	q.Enqueue(high)

	all := q.ListAll()
	if len(all) != 3 {
		t.Fatalf("ListAll returned %d items, want 3", len(all))
	}
	got := []string{all[0].ID, all[1].ID, all[2].ID}
	want := []string{"blocked", "high", "low"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ListAll order=%v, want %v", got, want)
		}
	}

	if first := q.Dequeue(); first.ID != "blocked" {
		t.Fatalf("ListAll mutated queue heap: first Dequeue()=%q, want blocked", first.ID)
	}
}

func TestJobQueue_ListBySession(t *testing.T) {
	t.Parallel()
	q := NewJobQueue()
	q.Enqueue(NewSpawnJob("j1", JobTypeSession, "alpha"))
	q.Enqueue(NewSpawnJob("j2", JobTypeSession, "alpha"))
	q.Enqueue(NewSpawnJob("j3", JobTypeSession, "beta"))

	alphaJobs := q.ListBySession("alpha")
	if len(alphaJobs) != 2 {
		t.Errorf("ListBySession('alpha') = %d, want 2", len(alphaJobs))
	}

	betaJobs := q.ListBySession("beta")
	if len(betaJobs) != 1 {
		t.Errorf("ListBySession('beta') = %d, want 1", len(betaJobs))
	}

	noneJobs := q.ListBySession("missing")
	if len(noneJobs) != 0 {
		t.Errorf("ListBySession('missing') = %d, want 0", len(noneJobs))
	}
}

func TestJobQueue_ListByBatch(t *testing.T) {
	t.Parallel()
	q := NewJobQueue()

	j1 := NewSpawnJob("j1", JobTypeSession, "s")
	j1.BatchID = "batch-A"
	j2 := NewSpawnJob("j2", JobTypeSession, "s")
	j2.BatchID = "batch-A"
	j3 := NewSpawnJob("j3", JobTypeSession, "s")
	j3.BatchID = "batch-B"

	q.Enqueue(j1)
	q.Enqueue(j2)
	q.Enqueue(j3)

	aJobs := q.ListByBatch("batch-A")
	if len(aJobs) != 2 {
		t.Errorf("ListByBatch('batch-A') = %d, want 2", len(aJobs))
	}

	bJobs := q.ListByBatch("batch-B")
	if len(bJobs) != 1 {
		t.Errorf("ListByBatch('batch-B') = %d, want 1", len(bJobs))
	}
}

func TestJobQueue_CountBySession(t *testing.T) {
	t.Parallel()
	q := NewJobQueue()
	q.Enqueue(NewSpawnJob("j1", JobTypeSession, "alpha"))
	q.Enqueue(NewSpawnJob("j2", JobTypeSession, "alpha"))
	q.Enqueue(NewSpawnJob("j3", JobTypeSession, "beta"))

	if c := q.CountBySession("alpha"); c != 2 {
		t.Errorf("CountBySession('alpha') = %d, want 2", c)
	}
	if c := q.CountBySession("beta"); c != 1 {
		t.Errorf("CountBySession('beta') = %d, want 1", c)
	}
	if c := q.CountBySession("missing"); c != 0 {
		t.Errorf("CountBySession('missing') = %d, want 0", c)
	}
}

func TestJobQueue_CountByBatch(t *testing.T) {
	t.Parallel()
	q := NewJobQueue()

	j1 := NewSpawnJob("j1", JobTypeSession, "s")
	j1.BatchID = "b1"
	j2 := NewSpawnJob("j2", JobTypeSession, "s")
	j2.BatchID = "b1"
	j3 := NewSpawnJob("j3", JobTypeSession, "s")
	// no batch ID

	q.Enqueue(j1)
	q.Enqueue(j2)
	q.Enqueue(j3)

	if c := q.CountByBatch("b1"); c != 2 {
		t.Errorf("CountByBatch('b1') = %d, want 2", c)
	}
	if c := q.CountByBatch("missing"); c != 0 {
		t.Errorf("CountByBatch('missing') = %d, want 0", c)
	}
}

func TestJobQueue_Clear(t *testing.T) {
	t.Parallel()
	q := NewJobQueue()
	for i := 0; i < 10; i++ {
		j := NewSpawnJob(fmt.Sprintf("j%d", i), JobTypeSession, "s")
		j.BatchID = "batch"
		q.Enqueue(j)
	}

	removed := q.Clear()
	if len(removed) != 10 {
		t.Errorf("Clear returned %d, want 10", len(removed))
	}
	if q.Len() != 0 {
		t.Errorf("queue Len after Clear = %d, want 0", q.Len())
	}
	if !q.IsEmpty() {
		t.Error("queue should be empty after Clear")
	}
	stats := q.Stats()
	if stats.CurrentSize != 0 {
		t.Errorf("CurrentSize after Clear = %d", stats.CurrentSize)
	}
}

func TestJobQueue_CancelBatch(t *testing.T) {
	t.Parallel()
	q := NewJobQueue()

	j1 := NewSpawnJob("j1", JobTypeSession, "s")
	j1.BatchID = "kill-me"
	j2 := NewSpawnJob("j2", JobTypeSession, "s")
	j2.BatchID = "kill-me"
	j3 := NewSpawnJob("j3", JobTypeSession, "s")
	j3.BatchID = "keep-me"

	q.Enqueue(j1)
	q.Enqueue(j2)
	q.Enqueue(j3)

	cancelled := q.CancelBatch("kill-me")
	if len(cancelled) != 2 {
		t.Errorf("CancelBatch returned %d, want 2", len(cancelled))
	}
	for _, j := range cancelled {
		if !j.IsCancelled() {
			t.Errorf("job %s should be cancelled", j.ID)
		}
	}
	if q.Len() != 1 {
		t.Errorf("Len after CancelBatch = %d, want 1", q.Len())
	}
	remaining := q.Dequeue()
	if remaining.BatchID != "keep-me" {
		t.Errorf("remaining job batch = %q, want 'keep-me'", remaining.BatchID)
	}
}

// bd-s8sex: CancelSession must decrement the cross-axis batchCounts
// for every cancelled job, not just delete its own sessionCounts entry.
// Pre-fix CountByBatch returned phantom counts after CancelSession
// because the cancelled jobs' batch entries were never decremented.
func TestJobQueue_CancelSession_DecrementsBatchCounts(t *testing.T) {
	t.Parallel()
	q := NewJobQueue()

	mk := func(id, sess, batch string) *SpawnJob {
		j := NewSpawnJob(id, JobTypeSession, sess)
		j.BatchID = batch
		return j
	}
	q.Enqueue(mk("j1", "foo", "batch-A"))
	q.Enqueue(mk("j2", "foo", "batch-A"))
	q.Enqueue(mk("j3", "foo", "batch-B"))
	q.Enqueue(mk("j4", "bar", "batch-C"))

	if got := q.CountByBatch("batch-A"); got != 2 {
		t.Fatalf("pre-cancel: CountByBatch(batch-A) = %d, want 2", got)
	}
	if got := q.CountByBatch("batch-B"); got != 1 {
		t.Fatalf("pre-cancel: CountByBatch(batch-B) = %d, want 1", got)
	}

	q.CancelSession("foo")

	if got := q.CountBySession("foo"); got != 0 {
		t.Errorf("post-cancel: CountBySession(foo) = %d, want 0", got)
	}
	if got := q.CountByBatch("batch-A"); got != 0 {
		t.Errorf("post-cancel: CountByBatch(batch-A) = %d, want 0 (all 2 jobs cancelled)", got)
	}
	if got := q.CountByBatch("batch-B"); got != 0 {
		t.Errorf("post-cancel: CountByBatch(batch-B) = %d, want 0 (1 job cancelled)", got)
	}
	if got := q.CountByBatch("batch-C"); got != 1 {
		t.Errorf("post-cancel: CountByBatch(batch-C) = %d, want 1 (untouched)", got)
	}
	if got := q.CountBySession("bar"); got != 1 {
		t.Errorf("post-cancel: CountBySession(bar) = %d, want 1 (untouched)", got)
	}
}

// bd-s8sex: CancelBatch must decrement the cross-axis sessionCounts
// for every cancelled job, not just delete its own batchCounts entry.
func TestJobQueue_CancelBatch_DecrementsSessionCounts(t *testing.T) {
	t.Parallel()
	q := NewJobQueue()

	mk := func(id, sess, batch string) *SpawnJob {
		j := NewSpawnJob(id, JobTypeSession, sess)
		j.BatchID = batch
		return j
	}
	q.Enqueue(mk("j1", "foo", "batch-A"))
	q.Enqueue(mk("j2", "bar", "batch-A"))
	q.Enqueue(mk("j3", "foo", "batch-B"))

	if got := q.CountBySession("foo"); got != 2 {
		t.Fatalf("pre-cancel: CountBySession(foo) = %d, want 2", got)
	}
	if got := q.CountBySession("bar"); got != 1 {
		t.Fatalf("pre-cancel: CountBySession(bar) = %d, want 1", got)
	}

	q.CancelBatch("batch-A")

	if got := q.CountByBatch("batch-A"); got != 0 {
		t.Errorf("post-cancel: CountByBatch(batch-A) = %d, want 0", got)
	}
	if got := q.CountBySession("foo"); got != 1 {
		t.Errorf("post-cancel: CountBySession(foo) = %d, want 1 (j3 still in batch-B)", got)
	}
	if got := q.CountBySession("bar"); got != 0 {
		t.Errorf("post-cancel: CountBySession(bar) = %d, want 0 (only job was in batch-A)", got)
	}
	if got := q.CountByBatch("batch-B"); got != 1 {
		t.Errorf("post-cancel: CountByBatch(batch-B) = %d, want 1 (untouched)", got)
	}
}

func TestJobQueue_Stats(t *testing.T) {
	t.Parallel()
	q := NewJobQueue()

	j1 := NewSpawnJob("j1", JobTypeSession, "s")
	j1.Priority = PriorityHigh
	j2 := NewSpawnJob("j2", JobTypePaneSplit, "s")
	j2.Priority = PriorityNormal

	q.Enqueue(j1)
	q.Enqueue(j2)

	stats := q.Stats()
	if stats.TotalEnqueued != 2 {
		t.Errorf("TotalEnqueued = %d, want 2", stats.TotalEnqueued)
	}
	if stats.CurrentSize != 2 {
		t.Errorf("CurrentSize = %d, want 2", stats.CurrentSize)
	}
	if stats.MaxSize != 2 {
		t.Errorf("MaxSize = %d, want 2", stats.MaxSize)
	}
	if stats.ByPriority[PriorityHigh] != 1 {
		t.Errorf("ByPriority[High] = %d, want 1", stats.ByPriority[PriorityHigh])
	}
	if stats.ByType[JobTypeSession] != 1 {
		t.Errorf("ByType[Session] = %d, want 1", stats.ByType[JobTypeSession])
	}
	if stats.ByType[JobTypePaneSplit] != 1 {
		t.Errorf("ByType[PaneSplit] = %d, want 1", stats.ByType[JobTypePaneSplit])
	}

	// Dequeue and check wait time tracking
	q.Dequeue()
	stats2 := q.Stats()
	if stats2.TotalDequeued != 1 {
		t.Errorf("TotalDequeued = %d, want 1", stats2.TotalDequeued)
	}
	if stats2.CurrentSize != 1 {
		t.Errorf("CurrentSize after dequeue = %d, want 1", stats2.CurrentSize)
	}
}

func TestJobQueue_Stats_IsCopy(t *testing.T) {
	t.Parallel()
	q := NewJobQueue()
	j := NewSpawnJob("j1", JobTypeSession, "s")
	j.Priority = PriorityHigh
	q.Enqueue(j)

	stats := q.Stats()
	stats.ByPriority[PriorityHigh] = 999 // mutate copy
	stats.ByType[JobTypeSession] = 999

	origStats := q.Stats()
	if origStats.ByPriority[PriorityHigh] != 1 {
		t.Error("Stats() should return a deep copy; mutation leaked")
	}
}

func TestJobQueue_Enqueue_Update(t *testing.T) {
	t.Parallel()
	q := NewJobQueue()
	j1 := NewSpawnJob("j1", JobTypeSession, "s")
	j1.Priority = PriorityNormal
	q.Enqueue(j1)

	// Enqueue same ID with different priority should update
	j1updated := NewSpawnJob("j1", JobTypeSession, "s")
	j1updated.Priority = PriorityUrgent
	q.Enqueue(j1updated)

	if q.Len() != 1 {
		t.Errorf("duplicate enqueue should update, not add; Len = %d", q.Len())
	}
	peek := q.Peek()
	if peek.Priority != PriorityUrgent {
		t.Errorf("updated priority = %v, want Urgent", peek.Priority)
	}
}

// bd-m0n8c: pre-fix, an Enqueue that updated an existing job-ID with a
// changed Priority/Type/SessionName/BatchID left the old buckets'
// counts dangling and never bumped the new bucket. Stats() then
// reported the wrong distribution; CountBySession / CountByBatch
// returned phantom counts. Same family as bd-o35sn (Stats drift on
// Dequeue/Cancel) and bd-s8sex (cross-axis drift on Cancel).
func TestJobQueue_Enqueue_Update_RebucketsStats(t *testing.T) {
	t.Parallel()

	t.Run("priority change", func(t *testing.T) {
		t.Parallel()
		q := NewJobQueue()
		j1 := NewSpawnJob("j1", JobTypeSession, "s")
		j1.Priority = PriorityNormal
		q.Enqueue(j1)

		j1u := NewSpawnJob("j1", JobTypeSession, "s")
		j1u.Priority = PriorityUrgent
		q.Enqueue(j1u)

		stats := q.Stats()
		if got := stats.ByPriority[PriorityUrgent]; got != 1 {
			t.Errorf("ByPriority[Urgent] = %d, want 1", got)
		}
		if _, present := stats.ByPriority[PriorityNormal]; present {
			t.Errorf("ByPriority[Normal] should be deleted (drift), got = %v", stats.ByPriority)
		}
		if stats.CurrentSize != 1 {
			t.Errorf("CurrentSize = %d, want 1", stats.CurrentSize)
		}
	})

	t.Run("type change", func(t *testing.T) {
		t.Parallel()
		q := NewJobQueue()
		j1 := NewSpawnJob("j1", JobTypeSession, "s")
		q.Enqueue(j1)

		j1u := NewSpawnJob("j1", JobTypePaneSplit, "s")
		q.Enqueue(j1u)

		stats := q.Stats()
		if got := stats.ByType[JobTypePaneSplit]; got != 1 {
			t.Errorf("ByType[PaneSplit] = %d, want 1", got)
		}
		if _, present := stats.ByType[JobTypeSession]; present {
			t.Errorf("ByType[Session] should be deleted (drift), got = %v", stats.ByType)
		}
	})

	t.Run("session name change", func(t *testing.T) {
		t.Parallel()
		q := NewJobQueue()
		j1 := NewSpawnJob("j1", JobTypeSession, "s1")
		q.Enqueue(j1)

		j1u := NewSpawnJob("j1", JobTypeSession, "s2")
		q.Enqueue(j1u)

		if got := q.CountBySession("s2"); got != 1 {
			t.Errorf("CountBySession(s2) = %d, want 1", got)
		}
		if got := q.CountBySession("s1"); got != 0 {
			t.Errorf("CountBySession(s1) = %d, want 0 (phantom drift)", got)
		}
	})

	t.Run("batch id change", func(t *testing.T) {
		t.Parallel()
		q := NewJobQueue()
		j1 := NewSpawnJob("j1", JobTypeSession, "s")
		j1.BatchID = "b1"
		q.Enqueue(j1)

		j1u := NewSpawnJob("j1", JobTypeSession, "s")
		j1u.BatchID = "b2"
		q.Enqueue(j1u)

		if got := q.CountByBatch("b2"); got != 1 {
			t.Errorf("CountByBatch(b2) = %d, want 1", got)
		}
		if got := q.CountByBatch("b1"); got != 0 {
			t.Errorf("CountByBatch(b1) = %d, want 0 (phantom drift)", got)
		}
	})

	t.Run("no-change update keeps stats stable", func(t *testing.T) {
		t.Parallel()
		q := NewJobQueue()
		j1 := NewSpawnJob("j1", JobTypeSession, "s")
		j1.Priority = PriorityNormal
		q.Enqueue(j1)

		j1same := NewSpawnJob("j1", JobTypeSession, "s")
		j1same.Priority = PriorityNormal
		q.Enqueue(j1same)

		stats := q.Stats()
		if stats.ByPriority[PriorityNormal] != 1 {
			t.Errorf("ByPriority[Normal] = %d after no-op update, want 1", stats.ByPriority[PriorityNormal])
		}
		if stats.CurrentSize != 1 {
			t.Errorf("CurrentSize = %d, want 1", stats.CurrentSize)
		}
	})
}

func TestJobQueue_Dequeue_Empty(t *testing.T) {
	t.Parallel()
	q := NewJobQueue()
	if q.Dequeue() != nil {
		t.Error("Dequeue on empty queue should return nil")
	}
}

func TestJobQueue_Remove_Missing(t *testing.T) {
	t.Parallel()
	q := NewJobQueue()
	if q.Remove("nonexistent") != nil {
		t.Error("Remove on missing ID should return nil")
	}
}

// ─── FairScheduler pure method tests ───────────────────────────────────────────

func TestFairScheduler_RunningCount(t *testing.T) {
	t.Parallel()
	fs := NewFairScheduler(FairSchedulerConfig{MaxPerSession: 5, MaxPerBatch: 5})

	if fs.RunningCount("sess1") != 0 {
		t.Errorf("initial RunningCount should be 0")
	}

	j := NewSpawnJob("j1", JobTypeSession, "sess1")
	fs.Enqueue(j)
	dequeued := fs.TryDequeue()
	if dequeued == nil {
		t.Fatal("TryDequeue returned nil")
	}

	if fs.RunningCount("sess1") != 1 {
		t.Errorf("RunningCount after dequeue = %d, want 1", fs.RunningCount("sess1"))
	}

	fs.MarkComplete(dequeued)
	if fs.RunningCount("sess1") != 0 {
		t.Errorf("RunningCount after complete = %d, want 0", fs.RunningCount("sess1"))
	}
}

func TestFairScheduler_Queue(t *testing.T) {
	t.Parallel()
	fs := NewFairScheduler(DefaultFairSchedulerConfig())
	if fs.Queue() == nil {
		t.Error("Queue() should not return nil")
	}
}

func TestFairScheduler_TryDequeue_Empty(t *testing.T) {
	t.Parallel()
	fs := NewFairScheduler(DefaultFairSchedulerConfig())
	if fs.TryDequeue() != nil {
		t.Error("TryDequeue on empty scheduler should return nil")
	}
}

func TestFairScheduler_MultiSession(t *testing.T) {
	t.Parallel()
	fs := NewFairScheduler(FairSchedulerConfig{MaxPerSession: 1, MaxPerBatch: 0})

	j1 := NewSpawnJob("j1", JobTypeSession, "sess-a")
	j2 := NewSpawnJob("j2", JobTypeSession, "sess-b")
	j3 := NewSpawnJob("j3", JobTypeSession, "sess-a") // second for sess-a

	fs.Enqueue(j1)
	fs.Enqueue(j2)
	fs.Enqueue(j3)

	d1 := fs.TryDequeue()
	d2 := fs.TryDequeue()
	if d1 == nil || d2 == nil {
		t.Fatal("should dequeue from two different sessions")
	}

	// Third should fail: sess-a already running, sess-b already running
	d3 := fs.TryDequeue()
	if d3 != nil {
		t.Error("third TryDequeue should fail (both sessions at max)")
	}

	// Mark one complete and retry
	fs.MarkComplete(d1)
	d3 = fs.TryDequeue()
	if d3 == nil {
		t.Error("should dequeue after marking session complete")
	}
}

func TestFairScheduler_TryDequeueSkipsBlockedRootInPriorityOrder(t *testing.T) {
	t.Parallel()

	fs := NewFairScheduler(FairSchedulerConfig{MaxPerSession: 1, MaxPerBatch: 0})
	blocked := NewSpawnJob("blocked", JobTypeSession, "blocked-session")
	blocked.Priority = PriorityUrgent
	low := NewSpawnJob("low", JobTypeSession, "low-session")
	low.Priority = PriorityLow
	high := NewSpawnJob("high", JobTypeSession, "high-session")
	high.Priority = PriorityHigh

	fs.Enqueue(blocked)
	fs.Enqueue(low)
	fs.Enqueue(high)
	fs.running["blocked-session"] = 1

	got := fs.TryDequeue()
	if got == nil {
		t.Fatal("TryDequeue returned nil")
	}
	if got.ID != "high" {
		t.Fatalf("TryDequeue()=%q, want high-priority eligible sibling before low", got.ID)
	}
}

// ─── RateLimiter pure method tests ─────────────────────────────────────────────

func TestRateLimiter_SetRate(t *testing.T) {
	t.Parallel()
	rl := NewRateLimiter(LimiterConfig{Rate: 1, Capacity: 5})

	rl.SetRate(10)
	stats := rl.Stats()
	if stats.CurrentTokens > 5 {
		t.Errorf("tokens should not exceed capacity after SetRate")
	}

	// Non-positive rate should be ignored
	rl.SetRate(-1)
	rl.SetRate(0)
}

func TestRateLimiter_SetCapacity(t *testing.T) {
	t.Parallel()

	t.Run("reduce capacity trims tokens", func(t *testing.T) {
		t.Parallel()
		rl := NewRateLimiter(LimiterConfig{Rate: 1, Capacity: 10})
		rl.SetCapacity(3)
		avail := rl.AvailableTokens()
		if avail > 3 {
			t.Errorf("AvailableTokens = %f, want <= 3", avail)
		}
	})

	t.Run("increase capacity", func(t *testing.T) {
		t.Parallel()
		rl := NewRateLimiter(LimiterConfig{Rate: 1, Capacity: 2})
		rl.SetCapacity(20)
		// tokens should still be at old level but capacity increased
	})

	t.Run("non-positive ignored", func(t *testing.T) {
		t.Parallel()
		rl := NewRateLimiter(LimiterConfig{Rate: 1, Capacity: 5})
		rl.SetCapacity(0)
		rl.SetCapacity(-1)
		if rl.AvailableTokens() < 4 {
			t.Error("non-positive SetCapacity should not change state")
		}
	})
}

func TestRateLimiter_SetMinInterval(t *testing.T) {
	t.Parallel()
	rl := NewRateLimiter(LimiterConfig{Rate: 100, Capacity: 100, MinInterval: 0})
	rl.SetMinInterval(time.Second)
	// Just verify no panic; actual enforcement is tested via Wait
}

func TestRateLimiter_Reset(t *testing.T) {
	t.Parallel()
	rl := NewRateLimiter(LimiterConfig{Rate: 1, Capacity: 5})

	// Drain tokens
	for rl.TryAcquire() {
	}
	if rl.AvailableTokens() >= 1 {
		t.Fatal("tokens should be drained")
	}

	rl.Reset()

	avail := rl.AvailableTokens()
	if avail < 4.5 {
		t.Errorf("after Reset, AvailableTokens = %f, want ~5", avail)
	}
	stats := rl.Stats()
	if stats.TotalRequests != 0 {
		t.Errorf("after Reset, TotalRequests = %d, want 0", stats.TotalRequests)
	}
	if stats.Waiting != 0 {
		t.Errorf("after Reset, Waiting = %d, want 0", stats.Waiting)
	}
}

func TestRateLimiter_AvailableTokens(t *testing.T) {
	t.Parallel()
	rl := NewRateLimiter(LimiterConfig{Rate: 10, Capacity: 5})
	avail := rl.AvailableTokens()
	if avail < 4.5 || avail > 5.5 {
		t.Errorf("initial AvailableTokens = %f, want ~5", avail)
	}
}

func TestRateLimiter_Waiting(t *testing.T) {
	t.Parallel()
	rl := NewRateLimiter(LimiterConfig{Rate: 10, Capacity: 5})
	if rl.Waiting() != 0 {
		t.Errorf("initial Waiting = %d, want 0", rl.Waiting())
	}
}

func TestRateLimiter_Wait_CanceledRequestUpdatesStatsAndWaiting(t *testing.T) {
	t.Parallel()

	rl := NewRateLimiter(LimiterConfig{Rate: 1, Capacity: 1, MinInterval: 0})
	if !rl.TryAcquire() {
		t.Fatal("expected initial token acquisition to succeed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	if err := rl.Wait(ctx); err == nil {
		t.Fatal("expected Wait to fail after context cancellation")
	}

	stats := rl.Stats()
	if stats.DeniedRequests != 1 {
		t.Fatalf("DeniedRequests = %d, want 1", stats.DeniedRequests)
	}
	if stats.Waiting != 0 {
		t.Fatalf("Waiting = %d, want 0 after canceled Wait", stats.Waiting)
	}
}

func TestRateLimiter_Stats(t *testing.T) {
	t.Parallel()
	rl := NewRateLimiter(LimiterConfig{Rate: 100, Capacity: 3, MinInterval: 0})

	rl.TryAcquire()
	rl.TryAcquire()

	stats := rl.Stats()
	if stats.TotalRequests != 2 {
		t.Errorf("TotalRequests = %d, want 2", stats.TotalRequests)
	}
	if stats.AllowedRequests != 2 {
		t.Errorf("AllowedRequests = %d, want 2", stats.AllowedRequests)
	}
	if stats.CurrentTokens > 1.5 {
		t.Errorf("CurrentTokens = %f, want ~1", stats.CurrentTokens)
	}
}

func TestRateLimiter_TimeUntilNextToken(t *testing.T) {
	t.Parallel()

	t.Run("tokens available", func(t *testing.T) {
		t.Parallel()
		rl := NewRateLimiter(LimiterConfig{Rate: 10, Capacity: 5, MinInterval: 0})
		d := rl.TimeUntilNextToken()
		if d != 0 {
			t.Errorf("TimeUntilNextToken with available tokens = %v, want 0", d)
		}
	})

	t.Run("no tokens", func(t *testing.T) {
		t.Parallel()
		rl := NewRateLimiter(LimiterConfig{Rate: 1, Capacity: 1, MinInterval: 0})
		rl.TryAcquire() // drain
		d := rl.TimeUntilNextToken()
		if d <= 0 {
			t.Errorf("TimeUntilNextToken with no tokens should be positive, got %v", d)
		}
	})
}

func TestRateLimiter_DefaultConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultLimiterConfig()
	if cfg.Rate != 2.0 {
		t.Errorf("Rate = %f, want 2.0", cfg.Rate)
	}
	if cfg.Capacity != 5.0 {
		t.Errorf("Capacity = %f, want 5.0", cfg.Capacity)
	}
	if !cfg.BurstAllowed {
		t.Error("BurstAllowed should be true")
	}
	if cfg.MinInterval != 300*time.Millisecond {
		t.Errorf("MinInterval = %v, want 300ms", cfg.MinInterval)
	}
}

func TestNewRateLimiter_InvalidConfig(t *testing.T) {
	t.Parallel()
	rl := NewRateLimiter(LimiterConfig{Rate: -5, Capacity: -10})
	// Should use fallback values
	avail := rl.AvailableTokens()
	if avail < 4 {
		t.Errorf("with invalid config, AvailableTokens = %f, expected default ~5", avail)
	}
}

// ─── FormatETA tests ───────────────────────────────────────────────────────────

func TestFormatETA(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		dur  time.Duration
		want string
	}{
		{"zero", 0, "now"},
		{"negative", -5 * time.Second, "now"},
		{"sub-second", 500 * time.Millisecond, "<1s"},
		{"5 seconds", 5 * time.Second, "5s"},
		{"30 seconds", 30 * time.Second, "30s"},
		{"90 seconds", 90 * time.Second, "1m30s"},
		{"5 minutes", 5 * time.Minute, "5m0s"},
		{"90 minutes", 90 * time.Minute, "1h30m0s"},
		{"2 hours", 2 * time.Hour, "2h0m0s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := FormatETA(tt.dur)
			if got != tt.want {
				t.Errorf("FormatETA(%v) = %q, want %q", tt.dur, got, tt.want)
			}
		})
	}
}

// ─── Progress.JSON tests ───────────────────────────────────────────────────────

func TestProgress_JSON(t *testing.T) {
	t.Parallel()
	p := &Progress{
		Timestamp:      time.Date(2026, 2, 5, 12, 0, 0, 0, time.UTC),
		Status:         "running",
		QueuedCount:    3,
		RunningCount:   2,
		CompletedCount: 10,
		FailedCount:    1,
	}

	data, err := p.JSON()
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}

	// Verify it's valid JSON
	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if decoded["status"] != "running" {
		t.Errorf("status = %v, want running", decoded["status"])
	}
	if int(decoded["queued_count"].(float64)) != 3 {
		t.Errorf("queued_count = %v, want 3", decoded["queued_count"])
	}
}

// ─── ProgressBroadcaster tests ─────────────────────────────────────────────────

func TestProgressBroadcaster(t *testing.T) {
	t.Parallel()
	b := NewProgressBroadcaster()

	var received []ProgressEvent
	b.Subscribe(func(e ProgressEvent) {
		received = append(received, e)
	})

	event := ProgressEvent{
		Type:    "test",
		Message: "hello",
	}
	b.Broadcast(event)

	if len(received) != 1 {
		t.Fatalf("subscriber received %d events, want 1", len(received))
	}
	if received[0].Message != "hello" {
		t.Errorf("Message = %q, want 'hello'", received[0].Message)
	}
}

func TestProgressBroadcaster_MultipleSubscribers(t *testing.T) {
	t.Parallel()
	b := NewProgressBroadcaster()

	var count1, count2 int
	b.Subscribe(func(e ProgressEvent) { count1++ })
	b.Subscribe(func(e ProgressEvent) { count2++ })

	b.Broadcast(ProgressEvent{Type: "test"})
	b.Broadcast(ProgressEvent{Type: "test"})

	if count1 != 2 || count2 != 2 {
		t.Errorf("counts = %d, %d; want 2, 2", count1, count2)
	}
}

// ─── AgentCaps pure method tests ───────────────────────────────────────────────

func TestAgentCaps_GetAvailable(t *testing.T) {
	t.Parallel()
	cfg := AgentCapsConfig{
		Default: AgentCapConfig{MaxConcurrent: 5},
	}
	caps := NewAgentCaps(cfg)

	if avail := caps.GetAvailable("cc"); avail != 5 {
		t.Errorf("initial GetAvailable = %d, want 5", avail)
	}

	caps.TryAcquire("cc")
	caps.TryAcquire("cc")

	if avail := caps.GetAvailable("cc"); avail != 3 {
		t.Errorf("GetAvailable after 2 acquires = %d, want 3", avail)
	}
}

func TestAgentCaps_Reset(t *testing.T) {
	t.Parallel()
	cfg := AgentCapsConfig{
		PerAgent: map[string]AgentCapConfig{
			"cc":  {MaxConcurrent: 3},
			"cod": {MaxConcurrent: 2, RampUpEnabled: true, RampUpInitial: 1, RampUpStep: 1, RampUpInterval: time.Hour},
		},
	}
	caps := NewAgentCaps(cfg)

	caps.TryAcquire("cc")
	caps.TryAcquire("cc")
	caps.TryAcquire("cod")

	if caps.GetRunning("cc") != 2 {
		t.Fatalf("pre-reset: cc running = %d, want 2", caps.GetRunning("cc"))
	}

	caps.Reset()

	if caps.GetRunning("cc") != 0 {
		t.Errorf("after Reset, cc running = %d, want 0", caps.GetRunning("cc"))
	}
	if caps.GetRunning("cod") != 0 {
		t.Errorf("after Reset, cod running = %d, want 0", caps.GetRunning("cod"))
	}

	// Caps should be re-initialized from config
	if caps.GetCurrentCap("cc") != 3 {
		t.Errorf("after Reset, cc cap = %d, want 3", caps.GetCurrentCap("cc"))
	}
	if caps.GetCurrentCap("cod") != 1 {
		t.Errorf("after Reset, cod cap = %d, want 1 (ramp-up initial)", caps.GetCurrentCap("cod"))
	}

	stats := caps.Stats()
	if stats.TotalRunning != 0 {
		t.Errorf("after Reset, TotalRunning = %d, want 0", stats.TotalRunning)
	}
}

func TestAgentCaps_GlobalCapExceeded(t *testing.T) {
	t.Parallel()

	t.Run("no global max", func(t *testing.T) {
		t.Parallel()
		cfg := AgentCapsConfig{
			Default:   AgentCapConfig{MaxConcurrent: 5},
			GlobalMax: 0,
		}
		caps := NewAgentCaps(cfg)
		caps.TryAcquire("cc")
		caps.TryAcquire("cc")

		// With GlobalMax=0, should never exceed
		if avail := caps.GetAvailable("cc"); avail != 3 {
			t.Errorf("GetAvailable = %d, want 3", avail)
		}
	})

	t.Run("at global max", func(t *testing.T) {
		t.Parallel()
		cfg := AgentCapsConfig{
			Default:   AgentCapConfig{MaxConcurrent: 5},
			GlobalMax: 2,
		}
		caps := NewAgentCaps(cfg)
		caps.TryAcquire("cc")
		caps.TryAcquire("cod")

		// At global max, no more should be acquirable
		if caps.TryAcquire("gmi") {
			t.Error("should be blocked by global max")
		}
	})
}

func TestAgentCaps_RecordSuccess_ClearsEarlyCooldown(t *testing.T) {
	t.Parallel()
	cfg := AgentCapsConfig{
		PerAgent: map[string]AgentCapConfig{
			"cc": {
				MaxConcurrent:     3,
				CooldownOnFailure: true,
				CooldownReduction: 1,
				CooldownRecovery:  10 * time.Second,
			},
		},
	}
	caps := NewAgentCaps(cfg)

	caps.TryAcquire("cc")
	caps.RecordFailure("cc")
	caps.Release("cc")

	// Cap should be reduced
	if cap := caps.GetCurrentCap("cc"); cap != 2 {
		t.Errorf("cap after failure = %d, want 2", cap)
	}

	// RecordSuccess resets cooldown timer
	caps.RecordSuccess("cc")
	// Verify no panic — the actual effect is to reset cooldownAt,
	// allowing recovery to proceed immediately on the next check
}

// ─── DefaultFairSchedulerConfig tests ──────────────────────────────────────────

func TestDefaultFairSchedulerConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultFairSchedulerConfig()
	if cfg.MaxPerSession != 3 {
		t.Errorf("MaxPerSession = %d, want 3", cfg.MaxPerSession)
	}
	if cfg.MaxPerBatch != 5 {
		t.Errorf("MaxPerBatch = %d, want 5", cfg.MaxPerBatch)
	}
}

// ─── DefaultAgentLimiterConfig tests ───────────────────────────────────────────

func TestDefaultAgentLimiterConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultAgentLimiterConfig()

	if cfg.Default.Rate != 2.0 {
		t.Errorf("Default.Rate = %f, want 2.0", cfg.Default.Rate)
	}

	ccCfg, ok := cfg.PerAgent["cc"]
	if !ok {
		t.Fatal("expected cc in PerAgent")
	}
	if ccCfg.Rate != 1.5 {
		t.Errorf("cc.Rate = %f, want 1.5", ccCfg.Rate)
	}

	codCfg, ok := cfg.PerAgent["cod"]
	if !ok {
		t.Fatal("expected cod in PerAgent")
	}
	if codCfg.Rate != 1.0 {
		t.Errorf("cod.Rate = %f, want 1.0", codCfg.Rate)
	}

	gmiCfg, ok := cfg.PerAgent["gmi"]
	if !ok {
		t.Fatal("expected gmi in PerAgent")
	}
	if gmiCfg.Rate != 2.0 {
		t.Errorf("gmi.Rate = %f, want 2.0", gmiCfg.Rate)
	}
}

// ─── PerAgentLimiter pure method tests ─────────────────────────────────────────

func TestPerAgentLimiter_GetLimiter_DefaultForUnknown(t *testing.T) {
	t.Parallel()
	cfg := AgentLimiterConfig{
		Default:  LimiterConfig{Rate: 5, Capacity: 5},
		PerAgent: map[string]LimiterConfig{},
	}
	pal := NewPerAgentLimiter(cfg)

	// Unknown agent type should get a limiter with default config
	limiter := pal.GetLimiter("unknown-agent")
	if limiter == nil {
		t.Fatal("GetLimiter for unknown type returned nil")
	}

	// Getting same type again should return same limiter
	limiter2 := pal.GetLimiter("unknown-agent")
	if limiter != limiter2 {
		t.Error("GetLimiter should return the same instance for repeated calls")
	}
}

func TestPerAgentLimiter_GetLimiter_AliasSharesCanonicalLimiter(t *testing.T) {
	t.Parallel()
	cfg := DefaultAgentLimiterConfig()
	pal := NewPerAgentLimiter(cfg)

	canonical := pal.GetLimiter("cod")
	alias := pal.GetLimiter("codex")
	providerAlias := pal.GetLimiter("openai-codex")

	if canonical != alias {
		t.Fatal("expected cod and codex to share the same limiter instance")
	}
	if canonical != providerAlias {
		t.Fatal("expected provider alias to share the canonical cod limiter instance")
	}
}

func TestPerAgentLimiter_ConfigAliasNormalizesToCanonicalKey(t *testing.T) {
	t.Parallel()
	cfg := AgentLimiterConfig{
		Default: LimiterConfig{Rate: 5, Capacity: 5},
		PerAgent: map[string]LimiterConfig{
			"codex": {Rate: 1, Capacity: 1},
		},
	}
	pal := NewPerAgentLimiter(cfg)

	stats := pal.AllStats()
	if _, ok := stats["cod"]; !ok {
		t.Fatal("expected canonical cod limiter stats from alias config")
	}
	if _, ok := stats["codex"]; ok {
		t.Fatal("did not expect separate raw codex limiter stats entry")
	}

	if pal.GetLimiter("cod") != pal.GetLimiter("codex") {
		t.Fatal("expected canonical and alias codex keys to share a limiter")
	}
}

func TestPerAgentLimiter_AllStats(t *testing.T) {
	t.Parallel()
	cfg := DefaultAgentLimiterConfig()
	pal := NewPerAgentLimiter(cfg)

	stats := pal.AllStats()
	// Should have stats for preconfigured agents
	if _, ok := stats["cc"]; !ok {
		t.Error("expected cc in AllStats")
	}
	if _, ok := stats["cod"]; !ok {
		t.Error("expected cod in AllStats")
	}
	if _, ok := stats["gmi"]; !ok {
		t.Error("expected gmi in AllStats")
	}
}

// ─── jobHeap tests ─────────────────────────────────────────────────────────────

func TestJobHeap_Less(t *testing.T) {
	t.Parallel()
	now := time.Now()

	tests := []struct {
		name string
		a, b *SpawnJob
		want bool
	}{
		{
			"lower priority wins",
			&SpawnJob{Priority: PriorityHigh, CreatedAt: now},
			&SpawnJob{Priority: PriorityNormal, CreatedAt: now},
			true,
		},
		{
			"higher priority loses",
			&SpawnJob{Priority: PriorityNormal, CreatedAt: now},
			&SpawnJob{Priority: PriorityHigh, CreatedAt: now},
			false,
		},
		{
			"same priority FIFO earlier wins",
			&SpawnJob{Priority: PriorityNormal, CreatedAt: now},
			&SpawnJob{Priority: PriorityNormal, CreatedAt: now.Add(time.Second)},
			true,
		},
		{
			"same priority FIFO later loses",
			&SpawnJob{Priority: PriorityNormal, CreatedAt: now.Add(time.Second)},
			&SpawnJob{Priority: PriorityNormal, CreatedAt: now},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := jobHeap{tt.a, tt.b}
			if got := h.Less(0, 1); got != tt.want {
				t.Errorf("Less = %v, want %v", got, tt.want)
			}
		})
	}
}

// ─── DefaultConfig tests ───────────────────────────────────────────────────────

func TestDefaultAgentCapConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultAgentCapConfig()
	if cfg.MaxConcurrent != 4 {
		t.Errorf("MaxConcurrent = %d, want 4", cfg.MaxConcurrent)
	}
	if cfg.RampUpEnabled {
		t.Error("default should not have ramp-up enabled")
	}
	if !cfg.CooldownOnFailure {
		t.Error("default should have cooldown on failure")
	}
}

// ─── Global scheduler singleton tests ────────────────────────────────────────

func TestGlobal_ReturnsNonNil(t *testing.T) {
	// Not parallel: depends on global scheduler state
	// Must run before TestSetGlobal to test sync.Once initialization
	got := Global()
	if got == nil {
		t.Error("Global() should return non-nil scheduler")
	}
}

func TestSetGlobal(t *testing.T) {
	// Not parallel: modifies package-level globalScheduler

	// Save original (Global() already called above, so sync.Once has fired)
	orig := Global()

	custom := New(DefaultConfig())
	SetGlobal(custom)
	t.Cleanup(func() { SetGlobal(orig) })

	got := Global()
	if got != custom {
		t.Error("Global() should return the scheduler set via SetGlobal")
	}
}

// ─── CreateProgressHooks tests ───────────────────────────────────────────────

func TestCreateProgressHooks_HooksNotNil(t *testing.T) {
	t.Parallel()

	broadcaster := NewProgressBroadcaster()
	sched := New(DefaultConfig())

	hooks := CreateProgressHooks(broadcaster, sched)

	if hooks.OnJobEnqueued == nil {
		t.Error("expected OnJobEnqueued hook to be set")
	}
	if hooks.OnJobStarted == nil {
		t.Error("expected OnJobStarted hook to be set")
	}
	if hooks.OnJobCompleted == nil {
		t.Error("expected OnJobCompleted hook to be set")
	}
	if hooks.OnJobFailed == nil {
		t.Error("expected OnJobFailed hook to be set")
	}
	if hooks.OnBackpressure == nil {
		t.Error("expected OnBackpressure hook to be set")
	}
}

func TestCreateProgressHooks_EnqueueBroadcasts(t *testing.T) {
	t.Parallel()

	broadcaster := NewProgressBroadcaster()
	sched := New(DefaultConfig())

	var received ProgressEvent
	broadcaster.Subscribe(func(event ProgressEvent) {
		received = event
	})
	hooks := CreateProgressHooks(broadcaster, sched)

	job := NewSpawnJob("job-enqueue-1", JobTypeAgentLaunch, "test-sess")
	hooks.OnJobEnqueued(job)

	if received.Type != "job_enqueued" {
		t.Errorf("event type = %q, want job_enqueued", received.Type)
	}
	if received.JobID != job.ID {
		t.Errorf("event JobID = %q, want %q", received.JobID, job.ID)
	}
}

func TestCreateProgressHooks_FailedBroadcasts(t *testing.T) {
	t.Parallel()

	broadcaster := NewProgressBroadcaster()
	sched := New(DefaultConfig())

	var received ProgressEvent
	broadcaster.Subscribe(func(event ProgressEvent) {
		received = event
	})
	hooks := CreateProgressHooks(broadcaster, sched)

	job := NewSpawnJob("job-fail-1", JobTypeAgentLaunch, "fail-sess")
	hooks.OnJobFailed(job, errors.New("test error"))

	if received.Type != "job_failed" {
		t.Errorf("event type = %q, want job_failed", received.Type)
	}
	if received.Message != "Job failed: test error" {
		t.Errorf("event Message = %q, want 'Job failed: test error'", received.Message)
	}
}

// ─── NewSpawnJob defaults ────────────────────────────────────────────────────

func TestNewSpawnJob_Defaults(t *testing.T) {
	t.Parallel()
	job := NewSpawnJob("test-id", JobTypeAgentLaunch, "my-session")

	if job.ID != "test-id" {
		t.Errorf("ID = %q, want %q", job.ID, "test-id")
	}
	if job.Type != JobTypeAgentLaunch {
		t.Errorf("Type = %v, want %v", job.Type, JobTypeAgentLaunch)
	}
	if job.SessionName != "my-session" {
		t.Errorf("SessionName = %q, want %q", job.SessionName, "my-session")
	}
	if job.Priority != PriorityNormal {
		t.Errorf("Priority = %v, want %v", job.Priority, PriorityNormal)
	}
	if job.Status != StatusPending {
		t.Errorf("Status = %v, want %v", job.Status, StatusPending)
	}
	if job.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d, want 3", job.MaxRetries)
	}
	if job.RetryDelay != time.Second {
		t.Errorf("RetryDelay = %v, want %v", job.RetryDelay, time.Second)
	}
	if job.Metadata == nil {
		t.Error("Metadata should be initialized, not nil")
	}
	if job.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
	if job.ctx == nil {
		t.Error("ctx should be set")
	}
	if job.cancel == nil {
		t.Error("cancel should be set")
	}
}

func TestNewSpawnJob_DifferentJobTypes(t *testing.T) {
	t.Parallel()
	types := []JobType{JobTypeSession, JobTypePaneSplit, JobTypeAgentLaunch}
	for _, jt := range types {
		t.Run(string(jt), func(t *testing.T) {
			t.Parallel()
			job := NewSpawnJob("id-"+string(jt), jt, "sess")
			if job.Type != jt {
				t.Errorf("Type = %v, want %v", job.Type, jt)
			}
		})
	}
}

// ─── SpawnJob Cancel / IsCancelled / IsTerminal ─────────────────────────────

func TestSpawnJob_Cancel_FromPending(t *testing.T) {
	t.Parallel()
	job := NewSpawnJob("cancel-pending", JobTypeAgentLaunch, "sess")
	job.Cancel()

	if got := job.GetStatus(); got != StatusCancelled {
		t.Errorf("status = %v, want %v", got, StatusCancelled)
	}
	if job.CompletedAt.IsZero() {
		t.Error("CompletedAt should be set when pending job is cancelled")
	}
	if !job.IsCancelled() {
		t.Error("IsCancelled() = false, want true")
	}
	if !job.IsTerminal() {
		t.Error("IsTerminal() = false, want true")
	}
	if err := job.Context().Err(); err == nil {
		t.Error("Context() should be cancelled after Cancel()")
	}
}

func TestSpawnJob_Cancel_FromRunning(t *testing.T) {
	t.Parallel()
	job := NewSpawnJob("cancel-running", JobTypeAgentLaunch, "sess")
	job.SetStatus(StatusRunning)
	job.Cancel()

	if got := job.GetStatus(); got != StatusRunning {
		t.Errorf("status = %v, want %v (Cancel should not rewrite running status)", got, StatusRunning)
	}
	if job.IsTerminal() {
		t.Error("IsTerminal() = true, want false for running job")
	}
	if !job.IsCancelled() {
		t.Error("IsCancelled() should be true after context cancellation")
	}
}

func TestSpawnJob_RetryLifecycle(t *testing.T) {
	t.Parallel()
	job := NewSpawnJob("retry-job", JobTypeAgentLaunch, "sess")
	job.MaxRetries = 2
	job.Error = "old error"

	if !job.CanRetry() {
		t.Fatal("CanRetry() = false at retry count 0, want true")
	}

	job.IncrementRetry()
	if job.RetryCount != 1 {
		t.Errorf("RetryCount = %d, want 1", job.RetryCount)
	}
	if job.GetStatus() != StatusRetrying {
		t.Errorf("status = %v, want %v", job.GetStatus(), StatusRetrying)
	}
	if job.Error != "" {
		t.Errorf("Error = %q, want empty after IncrementRetry()", job.Error)
	}

	job.IncrementRetry()
	if job.CanRetry() {
		t.Error("CanRetry() = true at retry count == MaxRetries, want false")
	}
}

// bd-o35sn: CancelSession / CancelBatch / Clear / Remove must keep
// stats.ByPriority and stats.ByType in sync — pre-fix the cancel paths
// didn't decrement either map and Clear didn't reset them, so Stats()
// reported phantom counts after queue mutations.
func TestJobQueue_CancelSession_DecrementsStatsByPriorityAndType(t *testing.T) {
	t.Parallel()
	q := NewJobQueue()

	mk := func(id, sess string, pri JobPriority) *SpawnJob {
		j := NewSpawnJob(id, JobTypeSession, sess)
		j.Priority = pri
		return j
	}
	q.Enqueue(mk("j1", "foo", PriorityHigh))
	q.Enqueue(mk("j2", "foo", PriorityHigh))
	q.Enqueue(mk("j3", "foo", PriorityNormal))
	q.Enqueue(mk("j4", "bar", PriorityNormal))

	pre := q.Stats()
	if pre.ByPriority[PriorityHigh] != 2 || pre.ByPriority[PriorityNormal] != 2 {
		t.Fatalf("pre-cancel stats = %+v", pre.ByPriority)
	}

	q.CancelSession("foo")

	post := q.Stats()
	if got := post.ByPriority[PriorityHigh]; got != 0 {
		t.Errorf("post-cancel ByPriority[High] = %d, want 0 (j1+j2 cancelled)", got)
	}
	if _, ok := post.ByPriority[PriorityHigh]; ok {
		t.Errorf("post-cancel ByPriority[High] entry should have been deleted, still present")
	}
	if got := post.ByPriority[PriorityNormal]; got != 1 {
		t.Errorf("post-cancel ByPriority[Normal] = %d, want 1 (j4 remains)", got)
	}
	if got := post.ByType[JobTypeSession]; got != 1 {
		t.Errorf("post-cancel ByType[session] = %d, want 1", got)
	}
}

func TestJobQueue_CancelBatch_DecrementsStatsByPriorityAndType(t *testing.T) {
	t.Parallel()
	q := NewJobQueue()

	mk := func(id, sess, batch string, pri JobPriority) *SpawnJob {
		j := NewSpawnJob(id, JobTypeSession, sess)
		j.BatchID = batch
		j.Priority = pri
		return j
	}
	q.Enqueue(mk("j1", "foo", "batch-A", PriorityHigh))
	q.Enqueue(mk("j2", "bar", "batch-A", PriorityNormal))
	q.Enqueue(mk("j3", "foo", "batch-B", PriorityNormal))

	q.CancelBatch("batch-A")

	post := q.Stats()
	if got := post.ByPriority[PriorityHigh]; got != 0 {
		t.Errorf("post-cancel ByPriority[High] = %d, want 0", got)
	}
	if _, ok := post.ByPriority[PriorityHigh]; ok {
		t.Errorf("post-cancel ByPriority[High] entry should have been deleted")
	}
	if got := post.ByPriority[PriorityNormal]; got != 1 {
		t.Errorf("post-cancel ByPriority[Normal] = %d, want 1 (j3 remains)", got)
	}
}

func TestJobQueue_Clear_ResetsStatsByPriorityAndType(t *testing.T) {
	t.Parallel()
	q := NewJobQueue()

	mk := func(id string, pri JobPriority) *SpawnJob {
		j := NewSpawnJob(id, JobTypeSession, "s")
		j.Priority = pri
		return j
	}
	q.Enqueue(mk("j1", PriorityHigh))
	q.Enqueue(mk("j2", PriorityNormal))

	q.Clear()

	post := q.Stats()
	if post.CurrentSize != 0 {
		t.Errorf("CurrentSize = %d, want 0", post.CurrentSize)
	}
	if len(post.ByPriority) != 0 {
		t.Errorf("ByPriority = %+v, want empty after Clear", post.ByPriority)
	}
	if len(post.ByType) != 0 {
		t.Errorf("ByType = %+v, want empty after Clear", post.ByType)
	}
}

func TestJobQueue_Remove_DeletesZeroEntriesFromStats(t *testing.T) {
	t.Parallel()
	q := NewJobQueue()

	j := NewSpawnJob("j1", JobTypeSession, "s")
	j.Priority = PriorityHigh
	q.Enqueue(j)

	q.Remove("j1")

	post := q.Stats()
	if _, ok := post.ByPriority[PriorityHigh]; ok {
		t.Errorf("post-remove ByPriority[High] entry should have been deleted, still present with value %d", post.ByPriority[PriorityHigh])
	}
	if _, ok := post.ByType[JobTypeSession]; ok {
		t.Errorf("post-remove ByType[session] entry should have been deleted")
	}
}
