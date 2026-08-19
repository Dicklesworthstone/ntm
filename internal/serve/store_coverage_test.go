package serve

import (
	"sort"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// ScannerStore: GetScan, UpdateScan, GetFindingsByScan
// ---------------------------------------------------------------------------

func TestScannerStore_UpdateScan(t *testing.T) {

	store := NewScannerStore()
	scan := &ScanRecord{
		ID:    "scan-update",
		State: ScanStateRunning,
		Path:  "/project",
	}
	seedScan(store, scan)

	// Update the scan
	now := time.Now()
	store.UpdateScan("scan-update", func(sr *ScanRecord) {
		sr.State = ScanStateCompleted
		sr.CompletedAt = &now
	})

	store.mu.RLock()
	got, ok := store.scans["scan-update"]
	store.mu.RUnlock()
	if !ok {
		t.Fatal("expected scan after update")
	}
	if got.State != ScanStateCompleted {
		t.Errorf("expected completed state, got %v", got.State)
	}
	if got.CompletedAt == nil {
		t.Error("expected CompletedAt to be set")
	}
}

// ---------------------------------------------------------------------------
// JobStore: Delete
// ---------------------------------------------------------------------------

func TestJobStore_CRUD_Lifecycle(t *testing.T) {

	store := NewJobStore()

	// Create
	job := store.Create("build")
	if job.Status != JobStatusPending {
		t.Errorf("expected pending status, got %v", job.Status)
	}
	if job.Type != "build" {
		t.Errorf("expected type=build, got %q", job.Type)
	}

	// Get
	got := store.Get(job.ID)
	if got == nil {
		t.Fatal("expected job to be found")
	}

	// Update
	result := map[string]interface{}{"output": "success"}
	store.Update(job.ID, JobStatusCompleted, 1.0, result, "")
	got = store.Get(job.ID)
	if got.Status != JobStatusCompleted {
		t.Errorf("expected completed, got %v", got.Status)
	}
	if got.Progress != 1.0 {
		t.Errorf("expected progress=1.0, got %f", got.Progress)
	}
}

// ---------------------------------------------------------------------------
// MemoryStore: GetDaemonInfo / SetDaemonInfo
// ---------------------------------------------------------------------------

func TestMemoryStore_DefaultDaemonInfo(t *testing.T) {

	store := NewMemoryStore()
	info := store.GetDaemonInfo()

	if info == nil {
		t.Fatal("expected non-nil default daemon info")
	}
	if info.State != DaemonStateStopped {
		t.Errorf("expected default state=stopped, got %q", info.State)
	}
}

func TestMemoryStore_SetAndGetDaemonInfo(t *testing.T) {

	store := NewMemoryStore()
	newInfo := &MemoryDaemonInfo{
		State:     DaemonStateRunning,
		PID:       12345,
		SessionID: "sess-abc",
	}
	store.SetDaemonInfo(newInfo)

	got := store.GetDaemonInfo()
	if got.State != DaemonStateRunning {
		t.Errorf("expected running, got %q", got.State)
	}
	if got.PID != 12345 {
		t.Errorf("expected PID=12345, got %d", got.PID)
	}
	if got.SessionID != "sess-abc" {
		t.Errorf("expected session=sess-abc, got %q", got.SessionID)
	}
}

func TestMemoryStore_GetDaemonInfoReturnsCopy(t *testing.T) {

	store := NewMemoryStore()
	startedAt := time.Now().UTC()
	expectedStartedAt := startedAt
	input := &MemoryDaemonInfo{
		State:     DaemonStateRunning,
		PID:       4321,
		Port:      8200,
		StartedAt: &startedAt,
		SessionID: "sess-copy",
	}
	store.SetDaemonInfo(input)

	input.State = DaemonStateStopped
	input.PID = 9999
	if input.StartedAt != nil {
		*input.StartedAt = input.StartedAt.Add(time.Hour)
	}

	got := store.GetDaemonInfo()
	got.State = DaemonStateStopped
	got.PID = 1111
	if got.StartedAt != nil {
		*got.StartedAt = got.StartedAt.Add(2 * time.Hour)
	}

	refetched := store.GetDaemonInfo()
	if refetched.State != DaemonStateRunning {
		t.Fatalf("state = %q, want %q", refetched.State, DaemonStateRunning)
	}
	if refetched.PID != 4321 {
		t.Fatalf("pid = %d, want 4321", refetched.PID)
	}
	if refetched.Port != 8200 {
		t.Fatalf("port = %d, want 8200", refetched.Port)
	}
	if refetched.StartedAt == nil || !refetched.StartedAt.Equal(expectedStartedAt) {
		t.Fatalf("started_at = %v, want %v", refetched.StartedAt, expectedStartedAt)
	}
	if refetched.SessionID != "sess-copy" {
		t.Fatalf("session_id = %q, want sess-copy", refetched.SessionID)
	}
}

func TestMemoryStore_SetDaemonInfo_Overwrite(t *testing.T) {

	store := NewMemoryStore()

	store.SetDaemonInfo(&MemoryDaemonInfo{State: DaemonStateRunning, PID: 111})
	store.SetDaemonInfo(&MemoryDaemonInfo{State: DaemonStateStopped, PID: 222})

	got := store.GetDaemonInfo()
	if got.State != DaemonStateStopped {
		t.Errorf("expected stopped after overwrite, got %q", got.State)
	}
	if got.PID != 222 {
		t.Errorf("expected PID=222 after overwrite, got %d", got.PID)
	}
}

// ---------------------------------------------------------------------------
// WSClient.Topics
// ---------------------------------------------------------------------------

func TestWSClient_Topics_Empty(t *testing.T) {

	client := &WSClient{
		topics: make(map[string]struct{}),
	}

	topics := client.Topics()
	if len(topics) != 0 {
		t.Errorf("expected 0 topics, got %d", len(topics))
	}
}

func TestWSClient_Topics_WithSubscriptions(t *testing.T) {

	client := &WSClient{
		topics: map[string]struct{}{
			"pane:output":   {},
			"session:state": {},
			"alerts":        {},
		},
	}

	topics := client.Topics()
	if len(topics) != 3 {
		t.Fatalf("expected 3 topics, got %d", len(topics))
	}

	sort.Strings(topics)
	expected := []string{"alerts", "pane:output", "session:state"}
	for i, exp := range expected {
		if topics[i] != exp {
			t.Errorf("topics[%d]: expected %q, got %q", i, exp, topics[i])
		}
	}
}

func TestWSClient_Topics_SingleTopic(t *testing.T) {

	client := &WSClient{
		topics: map[string]struct{}{"events": {}},
	}

	topics := client.Topics()
	if len(topics) != 1 || topics[0] != "events" {
		t.Errorf("expected [events], got %v", topics)
	}
}
