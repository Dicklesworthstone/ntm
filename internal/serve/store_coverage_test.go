package serve

import (
	"sort"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// ScannerStore: GetScan, UpdateScan, GetFindingsByScan
// ---------------------------------------------------------------------------

func TestScannerStore_GetScan_NotFound(t *testing.T) {
	t.Parallel()

	store := NewScannerStore()
	_, ok := store.GetScan("nonexistent")
	if ok {
		t.Error("expected not found for nonexistent scan")
	}
}

func TestScannerStore_GetScan_Found(t *testing.T) {
	t.Parallel()

	store := NewScannerStore()
	scan := &ScanRecord{
		ID:    "scan-1",
		State: ScanStateRunning,
		Path:  "/project",
	}
	store.AddScan(scan)

	got, ok := store.GetScan("scan-1")
	if !ok {
		t.Fatal("expected scan to be found")
	}
	if got.ID != "scan-1" {
		t.Errorf("expected ID=scan-1, got %q", got.ID)
	}
	if got.State != ScanStateRunning {
		t.Errorf("expected state=running, got %v", got.State)
	}
}

func TestScannerStore_UpdateScan(t *testing.T) {
	t.Parallel()

	store := NewScannerStore()
	scan := &ScanRecord{
		ID:    "scan-update",
		State: ScanStateRunning,
		Path:  "/project",
	}
	store.AddScan(scan)

	// Update the scan
	now := time.Now()
	updated := &ScanRecord{
		ID:          "scan-update",
		State:       ScanStateCompleted,
		Path:        "/project",
		CompletedAt: &now,
	}
	store.UpdateScan(updated)

	got, ok := store.GetScan("scan-update")
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

func TestScannerStore_GetFindingsByScan_Empty(t *testing.T) {
	t.Parallel()

	store := NewScannerStore()
	findings := store.GetFindingsByScan("no-such-scan")
	if len(findings) != 0 {
		t.Errorf("expected 0 findings, got %d", len(findings))
	}
}

func TestScannerStore_GetFindingsByScan_Filters(t *testing.T) {
	t.Parallel()

	store := NewScannerStore()

	// Add findings for two different scans
	store.AddFinding(&FindingRecord{ID: "f-1", ScanID: "scan-a", CreatedAt: time.Now()})
	store.AddFinding(&FindingRecord{ID: "f-2", ScanID: "scan-a", CreatedAt: time.Now()})
	store.AddFinding(&FindingRecord{ID: "f-3", ScanID: "scan-b", CreatedAt: time.Now()})

	findingsA := store.GetFindingsByScan("scan-a")
	if len(findingsA) != 2 {
		t.Errorf("expected 2 findings for scan-a, got %d", len(findingsA))
	}

	findingsB := store.GetFindingsByScan("scan-b")
	if len(findingsB) != 1 {
		t.Errorf("expected 1 finding for scan-b, got %d", len(findingsB))
	}
}

func TestScannerStore_GetFindingsByScan_AllSameScan(t *testing.T) {
	t.Parallel()

	store := NewScannerStore()
	for i := 0; i < 5; i++ {
		store.AddFinding(&FindingRecord{
			ID:        "f-" + string(rune('a'+i)),
			ScanID:    "scan-all",
			CreatedAt: time.Now(),
		})
	}

	findings := store.GetFindingsByScan("scan-all")
	if len(findings) != 5 {
		t.Errorf("expected 5 findings, got %d", len(findings))
	}
}

// ---------------------------------------------------------------------------
// JobStore: Delete
// ---------------------------------------------------------------------------

func TestJobStore_Delete_NotFound(t *testing.T) {
	t.Parallel()

	store := NewJobStore()
	ok := store.Delete("nonexistent")
	if ok {
		t.Error("expected false for deleting nonexistent job")
	}
}

func TestJobStore_Delete_Found(t *testing.T) {
	t.Parallel()

	store := NewJobStore()
	job := store.Create("test-job")

	ok := store.Delete(job.ID)
	if !ok {
		t.Error("expected true for deleting existing job")
	}

	// Verify it's gone
	got := store.Get(job.ID)
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestJobStore_Delete_Idempotent(t *testing.T) {
	t.Parallel()

	store := NewJobStore()
	job := store.Create("test-job")

	// First delete succeeds
	if !store.Delete(job.ID) {
		t.Error("expected first delete to succeed")
	}

	// Second delete returns false
	if store.Delete(job.ID) {
		t.Error("expected second delete to return false")
	}
}

func TestJobStore_CRUD_Lifecycle(t *testing.T) {
	t.Parallel()

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

	// Delete
	if !store.Delete(job.ID) {
		t.Error("expected delete to succeed")
	}
	if store.Get(job.ID) != nil {
		t.Error("expected nil after delete")
	}
}

// ---------------------------------------------------------------------------
// MemoryStore: GetDaemonInfo / SetDaemonInfo
// ---------------------------------------------------------------------------

func TestMemoryStore_DefaultDaemonInfo(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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

func TestMemoryStore_SetDaemonInfo_Overwrite(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

	client := &WSClient{
		topics: make(map[string]struct{}),
	}

	topics := client.Topics()
	if len(topics) != 0 {
		t.Errorf("expected 0 topics, got %d", len(topics))
	}
}

func TestWSClient_Topics_WithSubscriptions(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

	client := &WSClient{
		topics: map[string]struct{}{"events": {}},
	}

	topics := client.Topics()
	if len(topics) != 1 || topics[0] != "events" {
		t.Errorf("expected [events], got %v", topics)
	}
}
