package metrics

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

// setupTestStore creates a temporary state.Store with migrations applied.
func setupTestStore(t *testing.T) *state.Store {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_metrics.db")

	store, err := state.Open(dbPath)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}

	t.Cleanup(func() {
		store.Close()
		os.Remove(dbPath)
	})

	return store
}

// createTestSession inserts a session row to satisfy foreign key constraints.
func createTestSession(t *testing.T, store *state.Store, sessionID string) {
	t.Helper()
	err := store.CreateSession(&state.Session{
		ID:          sessionID,
		Name:        sessionID,
		ProjectPath: "/tmp/test",
		CreatedAt:   time.Now(),
		Status:      state.SessionActive,
	})
	if err != nil {
		t.Fatalf("CreateSession(%q): %v", sessionID, err)
	}
}

// createTestAgents inserts agent rows to satisfy foreign key constraints on blocked_commands/file_conflicts.
func createTestAgents(t *testing.T, store *state.Store, sessionID string, agentIDs ...string) {
	t.Helper()
	db := store.DB()
	for _, id := range agentIDs {
		_, err := db.Exec(`INSERT INTO agents (id, session_id, name, type, status) VALUES (?, ?, ?, 'cc', 'idle')`,
			id, sessionID, id)
		if err != nil {
			t.Fatalf("insert agent %q: %v", id, err)
		}
	}
}

func TestCollectorWithStore_GetDB(t *testing.T) {
	t.Parallel()
	store := setupTestStore(t)
	createTestSession(t, store, "db-test")

	c := NewCollector(store, "db-test")
	defer c.Close()

	// getDB should return a non-nil DB
	db := c.getDB()
	if db == nil {
		t.Fatal("getDB() returned nil with a valid state.Store")
	}
}

func TestCollectorWithStore_InsertBlockedCommand(t *testing.T) {
	t.Parallel()
	store := setupTestStore(t)
	createTestSession(t, store, "db-blocked-test")
	createTestAgents(t, store, "db-blocked-test", "agent-1", "agent-2")

	c := NewCollector(store, "db-blocked-test")
	defer c.Close()

	// Record blocked commands — exercises insertBlockedCommand
	c.RecordBlockedCommand("agent-1", "rm -rf /", "destructive")
	c.RecordBlockedCommand("agent-2", "git reset --hard", "safety")

	db := c.getDB()
	var rowCount int
	err := db.QueryRow(`
		SELECT COUNT(*) FROM blocked_commands
		WHERE session_id = ?`,
		"db-blocked-test").Scan(&rowCount)
	if err != nil {
		t.Fatalf("query blocked_commands: %v", err)
	}
	if rowCount != 2 {
		t.Errorf("blocked_commands rows = %d, want 2", rowCount)
	}

	// Verify specific fields
	var agentID, command, reason string
	err = db.QueryRow(`
		SELECT agent_id, command, reason FROM blocked_commands
		WHERE session_id = ? ORDER BY blocked_at LIMIT 1`,
		"db-blocked-test").Scan(&agentID, &command, &reason)
	if err != nil {
		t.Fatalf("query blocked_commands detail: %v", err)
	}
	if agentID != "agent-1" {
		t.Errorf("agent_id = %q, want %q", agentID, "agent-1")
	}
	if command != "rm -rf /" {
		t.Errorf("command = %q, want %q", command, "rm -rf /")
	}
	if reason != "destructive" {
		t.Errorf("reason = %q, want %q", reason, "destructive")
	}
}

func TestCollectorWithStore_SaveAndLoadSnapshot(t *testing.T) {
	t.Parallel()
	store := setupTestStore(t)
	createTestSession(t, store, "db-snapshot-test")
	createTestAgents(t, store, "db-snapshot-test", "agent-1", "a", "b")

	c := NewCollector(store, "db-snapshot-test")
	defer c.Close()

	// Record some data
	recordAPICallForTest(c, "bv", "triage")
	recordAPICallForTest(c, "bv", "triage")
	recordLatencyForTest(c, "cm_query", 50*time.Millisecond)
	c.RecordBlockedCommand("agent-1", "rm", "policy")
	recordFileConflictForTest(c, "a", "b", "*.go")

	// Save snapshot — exercises insertSnapshot
	err := c.SaveSnapshot("baseline")
	if err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// Load snapshot — exercises querySnapshot
	loaded, err := c.LoadSnapshot("baseline")
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}

	// Verify loaded data matches
	if loaded.SessionID != "db-snapshot-test" {
		t.Errorf("SessionID = %q, want %q", loaded.SessionID, "db-snapshot-test")
	}
	if loaded.APICallCounts["bv:triage"] != 2 {
		t.Errorf("APICallCounts[bv:triage] = %d, want 2", loaded.APICallCounts["bv:triage"])
	}
	if loaded.BlockedCommands != 1 {
		t.Errorf("BlockedCommands = %d, want 1", loaded.BlockedCommands)
	}
	if loaded.FileConflicts != 1 {
		t.Errorf("FileConflicts = %d, want 1", loaded.FileConflicts)
	}

	// Verify latency stats survived round-trip
	cmStats, ok := loaded.LatencyStats["cm_query"]
	if !ok {
		t.Fatal("expected cm_query in LatencyStats")
	}
	if cmStats.Count != 1 {
		t.Errorf("cm_query Count = %d, want 1", cmStats.Count)
	}
}

func TestCollectorWithStore_LoadSnapshot_NotFound(t *testing.T) {
	t.Parallel()
	store := setupTestStore(t)
	createTestSession(t, store, "db-notfound-test")

	c := NewCollector(store, "db-notfound-test")
	defer c.Close()

	// Loading a non-existent snapshot should error
	_, err := c.LoadSnapshot("nonexistent")
	if err == nil {
		t.Fatal("expected error loading nonexistent snapshot")
	}
}

func TestCollectorWithStore_SaveMultipleSnapshots(t *testing.T) {
	t.Parallel()
	store := setupTestStore(t)
	createTestSession(t, store, "db-multi-snap-test")

	c := NewCollector(store, "db-multi-snap-test")
	defer c.Close()

	// Save first snapshot
	recordAPICallForTest(c, "bv", "triage")
	if err := c.SaveSnapshot("snap1"); err != nil {
		t.Fatalf("SaveSnapshot snap1: %v", err)
	}

	// Record more data and save second snapshot
	recordAPICallForTest(c, "bd", "create")
	recordAPICallForTest(c, "bd", "create")
	if err := c.SaveSnapshot("snap2"); err != nil {
		t.Fatalf("SaveSnapshot snap2: %v", err)
	}

	// Load and verify each snapshot independently
	snap1, err := c.LoadSnapshot("snap1")
	if err != nil {
		t.Fatalf("LoadSnapshot snap1: %v", err)
	}
	if snap1.APICallCounts["bv:triage"] != 1 {
		t.Errorf("snap1 bv:triage = %d, want 1", snap1.APICallCounts["bv:triage"])
	}
	if _, hasCreate := snap1.APICallCounts["bd:create"]; hasCreate {
		t.Error("snap1 should not have bd:create")
	}

	snap2, err := c.LoadSnapshot("snap2")
	if err != nil {
		t.Fatalf("LoadSnapshot snap2: %v", err)
	}
	if snap2.APICallCounts["bv:triage"] != 1 {
		t.Errorf("snap2 bv:triage = %d, want 1", snap2.APICallCounts["bv:triage"])
	}
	if snap2.APICallCounts["bd:create"] != 2 {
		t.Errorf("snap2 bd:create = %d, want 2", snap2.APICallCounts["bd:create"])
	}
}

func TestCollectorWithStore_CompareSnapshots_RoundTrip(t *testing.T) {
	t.Parallel()
	store := setupTestStore(t)
	createTestSession(t, store, "db-compare-test")

	c := NewCollector(store, "db-compare-test")
	defer c.Close()

	// Create baseline
	recordLatencyForTest(c, "op1", 500*time.Millisecond)
	if err := c.SaveSnapshot("baseline"); err != nil {
		t.Fatalf("SaveSnapshot baseline: %v", err)
	}

	// Record improved metrics
	recordLatencyForTest(c, "op1", 50*time.Millisecond)
	recordLatencyForTest(c, "op1", 60*time.Millisecond)

	// Load baseline and generate current
	baseline, err := c.LoadSnapshot("baseline")
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	current, err := c.GenerateReport()
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}

	// Compare — should detect latency improvement
	result := c.CompareSnapshots(baseline, current)
	if len(result.Improvements) == 0 {
		t.Error("expected latency improvement to be detected from DB-loaded baseline")
	}
}

func TestCollectorWithStore_FullCycle(t *testing.T) {
	t.Parallel()
	store := setupTestStore(t)
	createTestSession(t, store, "full-cycle-test")
	createTestAgents(t, store, "full-cycle-test", "agent-1", "agent-2")

	c := NewCollector(store, "full-cycle-test")
	defer c.Close()

	// Exercise all recording functions with store
	recordAPICallForTest(c, "bv", "triage")
	recordAPICallForTest(c, "bv", "triage")
	recordAPICallForTest(c, "bv", "triage")
	recordAPICallForTest(c, "bd", "create")
	recordLatencyForTest(c, "cm_query", 40*time.Millisecond)
	recordLatencyForTest(c, "cm_query", 60*time.Millisecond)
	recordLatencyForTest(c, "api_call", 100*time.Millisecond)
	c.RecordBlockedCommand("agent-1", "rm -rf /", "destructive")
	c.RecordBlockedCommand("agent-1", "git reset --hard", "safety")
	recordFileConflictForTest(c, "agent-1", "agent-2", "*.go")

	// Generate report
	report, err := c.GenerateReport()
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}

	// Verify in-memory report is correct
	if report.APICallCounts["bv:triage"] != 3 {
		t.Errorf("bv:triage = %d, want 3", report.APICallCounts["bv:triage"])
	}
	if report.BlockedCommands != 2 {
		t.Errorf("BlockedCommands = %d, want 2", report.BlockedCommands)
	}
	if report.FileConflicts != 1 {
		t.Errorf("FileConflicts = %d, want 1", report.FileConflicts)
	}

	// Save and reload snapshot
	if err := c.SaveSnapshot("full-cycle"); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	loaded, err := c.LoadSnapshot("full-cycle")
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}

	// Verify snapshot preserves all data
	if loaded.APICallCounts["bv:triage"] != 3 {
		t.Errorf("loaded bv:triage = %d, want 3", loaded.APICallCounts["bv:triage"])
	}
	if loaded.APICallCounts["bd:create"] != 1 {
		t.Errorf("loaded bd:create = %d, want 1", loaded.APICallCounts["bd:create"])
	}
	if loaded.BlockedCommands != 2 {
		t.Errorf("loaded BlockedCommands = %d, want 2", loaded.BlockedCommands)
	}
	if loaded.FileConflicts != 1 {
		t.Errorf("loaded FileConflicts = %d, want 1", loaded.FileConflicts)
	}

	// Verify DB has the right number of rows for the surviving persistence
	// path (counter/latency/conflict persistence was removed as dead code).
	db := c.getDB()
	var blockedRows int
	db.QueryRow(`SELECT COUNT(*) FROM blocked_commands WHERE session_id = ?`, "full-cycle-test").Scan(&blockedRows)

	if blockedRows != 2 {
		t.Errorf("blocked_commands rows = %d, want 2", blockedRows)
	}
}

func TestCollectorWithStore_ListSnapshots(t *testing.T) {
	t.Parallel()
	store := setupTestStore(t)
	createTestSession(t, store, "db-list-snap-test")
	createTestSession(t, store, "db-list-snap-other")

	c := NewCollector(store, "db-list-snap-test")
	defer c.Close()

	// Empty DB: empty non-nil slice, no error.
	snaps, err := c.ListSnapshots()
	if err != nil {
		t.Fatalf("ListSnapshots (empty): %v", err)
	}
	if snaps == nil {
		t.Fatal("ListSnapshots (empty) returned nil slice, want empty non-nil")
	}
	if len(snaps) != 0 {
		t.Fatalf("ListSnapshots (empty) len = %d, want 0", len(snaps))
	}

	// Save two for this session and one for another session.
	if err := c.SaveSnapshot("first"); err != nil {
		t.Fatalf("SaveSnapshot first: %v", err)
	}
	if err := c.SaveSnapshot("second"); err != nil {
		t.Fatalf("SaveSnapshot second: %v", err)
	}
	other := NewCollector(store, "db-list-snap-other")
	defer other.Close()
	if err := other.SaveSnapshot("foreign"); err != nil {
		t.Fatalf("SaveSnapshot foreign: %v", err)
	}

	snaps, err = c.ListSnapshots()
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("ListSnapshots len = %d, want 2 (session-scoped)", len(snaps))
	}
	if snaps[0].Name != "first" || snaps[1].Name != "second" {
		t.Errorf("order = [%q, %q], want [first, second]", snaps[0].Name, snaps[1].Name)
	}
	for i, s := range snaps {
		if s.SessionID != "db-list-snap-test" {
			t.Errorf("snaps[%d].SessionID = %q, want db-list-snap-test", i, s.SessionID)
		}
		if s.CreatedAt.IsZero() {
			t.Errorf("snaps[%d].CreatedAt is zero", i)
		}
	}
	if snaps[0].ID >= snaps[1].ID {
		t.Errorf("ids not ascending: %d then %d", snaps[0].ID, snaps[1].ID)
	}
}

func TestCollectorNoStore_ListSnapshots(t *testing.T) {
	t.Parallel()
	c := NewCollector(nil, "no-store")
	defer c.Close()

	if _, err := c.ListSnapshots(); err == nil {
		t.Fatal("ListSnapshots with nil store: want error, got nil")
	}
}
