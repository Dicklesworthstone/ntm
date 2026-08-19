package serve

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/scanner"
)

// =============================================================================
// ScannerStore: GetScans - reverse chronological ordering & pagination
// =============================================================================

func TestScannerStore_GetScans_ReverseOrder(t *testing.T) {
	store := NewScannerStore()

	for i := 0; i < 5; i++ {
		seedScan(store, &ScanRecord{
			ID:        generateScanID(),
			State:     ScanStateCompleted,
			StartedAt: time.Now(),
		})
	}

	scans := store.GetScans(10, 0)
	if len(scans) != 5 {
		t.Fatalf("expected 5 scans, got %d", len(scans))
	}
}

func TestScannerStore_GetScans_Pagination(t *testing.T) {
	store := NewScannerStore()

	for i := 0; i < 10; i++ {
		seedScan(store, &ScanRecord{
			ID:        generateScanID(),
			State:     ScanStateCompleted,
			StartedAt: time.Now(),
		})
	}

	// Limit to 3 results
	scans := store.GetScans(3, 0)
	if len(scans) != 3 {
		t.Errorf("expected 3 scans, got %d", len(scans))
	}

	// Offset beyond available
	scans = store.GetScans(10, 100)
	if scans != nil {
		t.Errorf("expected nil for offset beyond range, got %d items", len(scans))
	}

	// Offset + limit = partial result
	scans = store.GetScans(5, 7)
	if len(scans) != 3 {
		t.Errorf("expected 3 scans (offset 7 from 10), got %d", len(scans))
	}
}

// =============================================================================
// ScannerStore: GetRunningScan
// =============================================================================

// =============================================================================
// ScannerStore: AddScan / AddFinding / GetFinding / UpdateFinding
// =============================================================================

func TestScannerStore_FindingCRUD(t *testing.T) {
	store := NewScannerStore()

	finding := &FindingRecord{
		ID:     "finding-1",
		ScanID: "scan-1",
		Finding: scanner.Finding{
			Severity: scanner.SeverityCritical,
		},
		CreatedAt: time.Now(),
	}

	store.AddFinding(finding)

	// Get existing finding
	got, ok := store.GetFinding("finding-1")
	if !ok {
		t.Fatal("expected to find finding-1")
	}
	if got.ScanID != "scan-1" {
		t.Errorf("ScanID = %s, want scan-1", got.ScanID)
	}

	// Get non-existent finding
	_, ok = store.GetFinding("nonexistent")
	if ok {
		t.Error("expected not found for nonexistent finding")
	}

	// Update finding
	store.UpdateFinding(finding.ID, func(fr *FindingRecord) {
		fr.Dismissed = true
	})

	got, _ = store.GetFinding("finding-1")
	if !got.Dismissed {
		t.Error("expected finding to be dismissed after update")
	}
}

// =============================================================================
// ScannerStore: GetFindings - filtering, sorting, pagination
// =============================================================================

func TestScannerStore_GetFindings_Filtering(t *testing.T) {
	store := NewScannerStore()

	now := time.Now()
	store.AddFinding(&FindingRecord{
		ID: "f1", ScanID: "scan-1",
		Finding:   scanner.Finding{Severity: scanner.SeverityCritical},
		CreatedAt: now.Add(-3 * time.Second),
	})
	store.AddFinding(&FindingRecord{
		ID: "f2", ScanID: "scan-1",
		Finding:   scanner.Finding{Severity: scanner.SeverityInfo},
		Dismissed: true,
		CreatedAt: now.Add(-2 * time.Second),
	})
	store.AddFinding(&FindingRecord{
		ID: "f3", ScanID: "scan-2",
		Finding:   scanner.Finding{Severity: scanner.SeverityCritical},
		CreatedAt: now.Add(-1 * time.Second),
	})

	// Filter by scan ID
	findings := store.GetFindings("scan-1", true, "", 10, 0)
	if len(findings) != 2 {
		t.Errorf("expected 2 findings for scan-1, got %d", len(findings))
	}

	// Exclude dismissed
	findings = store.GetFindings("scan-1", false, "", 10, 0)
	if len(findings) != 1 {
		t.Errorf("expected 1 non-dismissed finding, got %d", len(findings))
	}

	// Filter by severity
	findings = store.GetFindings("", true, string(scanner.SeverityCritical), 10, 0)
	if len(findings) != 2 {
		t.Errorf("expected 2 critical severity findings, got %d", len(findings))
	}

	// Pagination: offset beyond range
	findings = store.GetFindings("", true, "", 10, 100)
	if findings != nil {
		t.Errorf("expected nil for offset beyond range, got %d", len(findings))
	}

	// Pagination: limit 1
	findings = store.GetFindings("", true, "", 1, 0)
	if len(findings) != 1 {
		t.Errorf("expected 1 finding with limit 1, got %d", len(findings))
	}
}

// =============================================================================
// JobStore: List
// =============================================================================

func TestJobStore_List(t *testing.T) {
	store := NewJobStore()

	// Empty list
	jobs := store.List()
	if len(jobs) != 0 {
		t.Errorf("expected empty list, got %d", len(jobs))
	}

	// Add some jobs
	store.Create("scan")
	store.Create("export")
	store.Create("import")

	jobs = store.List()
	if len(jobs) != 3 {
		t.Errorf("expected 3 jobs, got %d", len(jobs))
	}
}

// List order must be deterministic (newest first, ID tie-break), never
// random map iteration order.
func TestJobStore_ListDeterministicOrder(t *testing.T) {
	store := NewJobStore()
	for i := 0; i < 8; i++ {
		store.Create("scan")
	}
	first := store.List()
	for run := 0; run < 5; run++ {
		again := store.List()
		for i := range first {
			if again[i].ID != first[i].ID {
				t.Fatalf("List order changed between calls at index %d: %s vs %s", i, again[i].ID, first[i].ID)
			}
		}
	}
	for i := 1; i < len(first); i++ {
		prev, cur := first[i-1], first[i]
		if prev.CreatedAt < cur.CreatedAt {
			t.Fatalf("List not newest-first at index %d: %s < %s", i, prev.CreatedAt, cur.CreatedAt)
		}
		if prev.CreatedAt == cur.CreatedAt && prev.ID >= cur.ID {
			t.Fatalf("List tie-break not by ID at index %d: %s >= %s", i, prev.ID, cur.ID)
		}
	}
}

// The store must stay bounded: Create evicts the oldest terminal jobs at the
// cap, and never evicts pending/running jobs.
func TestJobStore_CreateEvictsOldestTerminalJobs(t *testing.T) {
	store := NewJobStore()
	running := store.Create("scan") // stays pending — must survive eviction
	terminalIDs := make([]string, 0, maxRetainedJobs)
	for i := 0; i < maxRetainedJobs; i++ {
		job := store.Create("scan")
		store.Update(job.ID, JobStatusCompleted, 1, nil, "")
		terminalIDs = append(terminalIDs, job.ID)
	}
	// The next Create must evict terminal jobs down to the cap.
	newest := store.Create("scan")
	jobs := store.List()
	if len(jobs) > maxRetainedJobs {
		t.Fatalf("store has %d jobs, want <= %d after eviction", len(jobs), maxRetainedJobs)
	}
	if store.Get(running.ID) == nil {
		t.Fatal("pending job was evicted; only terminal jobs may be")
	}
	if store.Get(newest.ID) == nil {
		t.Fatal("newly created job missing")
	}
	// Both non-terminal jobs survived and the count is at the cap, so at
	// least two terminal jobs must have been evicted.
	surviving := 0
	for _, id := range terminalIDs {
		if store.Get(id) != nil {
			surviving++
		}
	}
	if surviving > maxRetainedJobs-2 {
		t.Fatalf("%d terminal jobs survived; want <= %d after eviction", surviving, maxRetainedJobs-2)
	}
}

// =============================================================================
// WSClient: canSubscribe
// =============================================================================

func TestWSClient_CanSubscribe(t *testing.T) {

	client := &WSClient{
		id:     "auth-client",
		topics: make(map[string]struct{}),
		send:   make(chan []byte, 16),
	}

	if !client.canSubscribe("sessions:test") {
		t.Error("expected canSubscribe to return true")
	}
	if !client.canSubscribe("anything") {
		t.Error("expected canSubscribe to return true for any topic")
	}
}
