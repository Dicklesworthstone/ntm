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

func TestCollectorWithStore_UpsertCounter(t *testing.T) {
	t.Parallel()
	store := setupTestStore(t)
	createTestSession(t, store, "db-upsert-test")

	c := NewCollector(store, "db-upsert-test")
	defer c.Close()

	// Record API calls — exercises upsertCounter
	c.RecordAPICall("bv", "triage")
	c.RecordAPICall("bv", "triage")
	c.RecordAPICall("bd", "create")

	// Verify data was written to DB
	db := c.getDB()
	var count int64
	err := db.QueryRow(`
		SELECT count FROM metric_counters
		WHERE session_id = ? AND tool = ? AND operation = ?`,
		"db-upsert-test", "bv", "triage").Scan(&count)
	if err != nil {
		t.Fatalf("query metric_counters: %v", err)
	}
	if count != 2 {
		t.Errorf("bv:triage count = %d, want 2", count)
	}

	err = db.QueryRow(`
		SELECT count FROM metric_counters
		WHERE session_id = ? AND tool = ? AND operation = ?`,
		"db-upsert-test", "bd", "create").Scan(&count)
	if err != nil {
		t.Fatalf("query metric_counters: %v", err)
	}
	if count != 1 {
		t.Errorf("bd:create count = %d, want 1", count)
	}
}

func TestCollectorWithStore_InsertLatency(t *testing.T) {
	t.Parallel()
	store := setupTestStore(t)
	createTestSession(t, store, "db-latency-test")

	c := NewCollector(store, "db-latency-test")
	defer c.Close()

	// Record latencies — exercises insertLatency
	c.RecordLatency("cm_query", 50*time.Millisecond)
	c.RecordLatency("cm_query", 100*time.Millisecond)
	c.RecordLatency("api_call", 200*time.Millisecond)

	// Verify data was written to DB
	db := c.getDB()
	var rowCount int
	err := db.QueryRow(`
		SELECT COUNT(*) FROM metric_latencies
		WHERE session_id = ? AND operation = ?`,
		"db-latency-test", "cm_query").Scan(&rowCount)
	if err != nil {
		t.Fatalf("query metric_latencies: %v", err)
	}
	if rowCount != 2 {
		t.Errorf("cm_query latency rows = %d, want 2", rowCount)
	}

	err = db.QueryRow(`
		SELECT COUNT(*) FROM metric_latencies
		WHERE session_id = ? AND operation = ?`,
		"db-latency-test", "api_call").Scan(&rowCount)
	if err != nil {
		t.Fatalf("query metric_latencies: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("api_call latency rows = %d, want 1", rowCount)
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

func TestCollectorWithStore_InsertFileConflict(t *testing.T) {
	t.Parallel()
	store := setupTestStore(t)
	createTestSession(t, store, "db-conflict-test")
	createTestAgents(t, store, "db-conflict-test", "agent-1", "agent-2", "agent-3")

	c := NewCollector(store, "db-conflict-test")
	defer c.Close()

	// Record file conflicts — exercises insertFileConflict
	c.RecordFileConflict("agent-1", "agent-2", "*.go")
	c.RecordFileConflict("agent-3", "agent-1", "internal/serve/*.go")

	db := c.getDB()
	var rowCount int
	err := db.QueryRow(`
		SELECT COUNT(*) FROM file_conflicts
		WHERE session_id = ?`,
		"db-conflict-test").Scan(&rowCount)
	if err != nil {
		t.Fatalf("query file_conflicts: %v", err)
	}
	if rowCount != 2 {
		t.Errorf("file_conflicts rows = %d, want 2", rowCount)
	}

	// Verify specific fields
	var reqAgent, holdAgent, pattern string
	err = db.QueryRow(`
		SELECT requesting_agent_id, holding_agent_id, path_pattern
		FROM file_conflicts
		WHERE session_id = ? ORDER BY conflict_at LIMIT 1`,
		"db-conflict-test").Scan(&reqAgent, &holdAgent, &pattern)
	if err != nil {
		t.Fatalf("query file_conflicts detail: %v", err)
	}
	if reqAgent != "agent-1" {
		t.Errorf("requesting_agent_id = %q, want %q", reqAgent, "agent-1")
	}
	if holdAgent != "agent-2" {
		t.Errorf("holding_agent_id = %q, want %q", holdAgent, "agent-2")
	}
	if pattern != "*.go" {
		t.Errorf("path_pattern = %q, want %q", pattern, "*.go")
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
	c.RecordAPICall("bv", "triage")
	c.RecordAPICall("bv", "triage")
	c.RecordLatency("cm_query", 50*time.Millisecond)
	c.RecordBlockedCommand("agent-1", "rm", "policy")
	c.RecordFileConflict("a", "b", "*.go")

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
	c.RecordAPICall("bv", "triage")
	if err := c.SaveSnapshot("snap1"); err != nil {
		t.Fatalf("SaveSnapshot snap1: %v", err)
	}

	// Record more data and save second snapshot
	c.RecordAPICall("bd", "create")
	c.RecordAPICall("bd", "create")
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
	c.RecordLatency("op1", 500*time.Millisecond)
	if err := c.SaveSnapshot("baseline"); err != nil {
		t.Fatalf("SaveSnapshot baseline: %v", err)
	}

	// Record improved metrics
	c.RecordLatency("op1", 50*time.Millisecond)
	c.RecordLatency("op1", 60*time.Millisecond)

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

func TestCollectorWithStore_NilStoreRecordsSafe(t *testing.T) {
	t.Parallel()
	c := NewCollector(nil, "nil-store-test")
	defer c.Close()

	// All Record* functions should be safe with nil store (no panic)
	c.RecordAPICall("bv", "triage")
	c.RecordLatency("op", 10*time.Millisecond)
	c.RecordBlockedCommand("agent", "rm", "policy")
	c.RecordFileConflict("a", "b", "*.go")

	// SaveSnapshot should be a no-op
	if err := c.SaveSnapshot("test"); err != nil {
		t.Errorf("SaveSnapshot with nil store should return nil, got %v", err)
	}

	// In-memory counters should still work
	report, err := c.GenerateReport()
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}
	if report.APICallCounts["bv:triage"] != 1 {
		t.Errorf("bv:triage = %d, want 1", report.APICallCounts["bv:triage"])
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
	c.RecordAPICall("bv", "triage")
	c.RecordAPICall("bv", "triage")
	c.RecordAPICall("bv", "triage")
	c.RecordAPICall("bd", "create")
	c.RecordLatency("cm_query", 40*time.Millisecond)
	c.RecordLatency("cm_query", 60*time.Millisecond)
	c.RecordLatency("api_call", 100*time.Millisecond)
	c.RecordBlockedCommand("agent-1", "rm -rf /", "destructive")
	c.RecordBlockedCommand("agent-1", "git reset --hard", "safety")
	c.RecordFileConflict("agent-1", "agent-2", "*.go")

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

	// Verify DB has the right number of rows
	db := c.getDB()
	var counterRows, latencyRows, blockedRows, conflictRows int
	db.QueryRow(`SELECT COUNT(*) FROM metric_counters WHERE session_id = ?`, "full-cycle-test").Scan(&counterRows)
	db.QueryRow(`SELECT COUNT(*) FROM metric_latencies WHERE session_id = ?`, "full-cycle-test").Scan(&latencyRows)
	db.QueryRow(`SELECT COUNT(*) FROM blocked_commands WHERE session_id = ?`, "full-cycle-test").Scan(&blockedRows)
	db.QueryRow(`SELECT COUNT(*) FROM file_conflicts WHERE session_id = ?`, "full-cycle-test").Scan(&conflictRows)

	if counterRows != 2 { // bv:triage and bd:create
		t.Errorf("metric_counters rows = %d, want 2", counterRows)
	}
	if latencyRows != 3 { // 2 cm_query + 1 api_call
		t.Errorf("metric_latencies rows = %d, want 3", latencyRows)
	}
	if blockedRows != 2 {
		t.Errorf("blocked_commands rows = %d, want 2", blockedRows)
	}
	if conflictRows != 1 {
		t.Errorf("file_conflicts rows = %d, want 1", conflictRows)
	}
}
