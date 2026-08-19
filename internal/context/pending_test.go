package context

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func makePending(agentID, session, pane string, timeout time.Time) *PendingRotation {
	return &PendingRotation{
		AgentID:        agentID,
		SessionName:    session,
		PaneID:         pane,
		ContextPercent: 85.5,
		CreatedAt:      time.Now(),
		TimeoutAt:      timeout,
		DefaultAction:  ConfirmRotate,
		WorkDir:        "/tmp/test",
	}
}

func TestNewPendingRotationStoreWithPath(t *testing.T) {
	t.Parallel()
	store := NewPendingRotationStoreWithPath("/tmp/test.jsonl")
	if store.storagePath != "/tmp/test.jsonl" {
		t.Errorf("StoragePath() = %q, want /tmp/test.jsonl", store.storagePath)
	}
}

func TestPendingRotationStore_AddAndGet(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := NewPendingRotationStoreWithPath(filepath.Join(dir, "pending.jsonl"))

	// Get on empty store returns nil
	got, err := store.Get("agent-1")
	if err != nil {
		t.Fatalf("Get empty: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for empty store, got %+v", got)
	}

	// Add a pending rotation
	timeout := time.Now().Add(10 * time.Minute)
	p := makePending("agent-1", "sess-1", "1.1", timeout)
	if err := store.Add(p); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Get it back
	got, err = store.Get("agent-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if got.AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want agent-1", got.AgentID)
	}
	if got.SessionName != "sess-1" {
		t.Errorf("SessionName = %q, want sess-1", got.SessionName)
	}
	if got.DefaultAction != ConfirmRotate {
		t.Errorf("DefaultAction = %q, want rotate", got.DefaultAction)
	}
}

func TestPendingRotationStore_AddUpdatesExisting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := NewPendingRotationStoreWithPath(filepath.Join(dir, "pending.jsonl"))

	timeout := time.Now().Add(10 * time.Minute)
	p1 := makePending("agent-1", "sess-1", "1.1", timeout)
	p1.ContextPercent = 80.0
	if err := store.Add(p1); err != nil {
		t.Fatalf("Add 1: %v", err)
	}

	// Add again with different context percent — should replace
	p2 := makePending("agent-1", "sess-1", "1.1", timeout)
	p2.ContextPercent = 95.0
	if err := store.Add(p2); err != nil {
		t.Fatalf("Add 2: %v", err)
	}

	// Should only have one entry
	all, err := store.GetAll()
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 entry after update, got %d", len(all))
	}
	if all[0].ContextPercent != 95.0 {
		t.Errorf("ContextPercent = %f, want 95.0", all[0].ContextPercent)
	}
}

func TestPendingRotationStore_Remove(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := NewPendingRotationStoreWithPath(filepath.Join(dir, "pending.jsonl"))

	timeout := time.Now().Add(10 * time.Minute)
	_ = store.Add(makePending("agent-1", "sess-1", "1.1", timeout))
	_ = store.Add(makePending("agent-2", "sess-1", "1.2", timeout))

	// Remove agent-1
	if err := store.Remove("agent-1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// agent-1 should be gone
	got, err := store.Get("agent-1")
	if err != nil {
		t.Fatalf("Get after remove: %v", err)
	}
	if got != nil {
		t.Error("expected nil after remove")
	}

	// agent-2 should still be there
	got, err = store.Get("agent-2")
	if err != nil {
		t.Fatalf("Get agent-2: %v", err)
	}
	if got == nil {
		t.Error("expected agent-2 to still exist")
	}
}

func TestPendingRotationStore_GetAll(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := NewPendingRotationStoreWithPath(filepath.Join(dir, "pending.jsonl"))

	timeout := time.Now().Add(10 * time.Minute)
	_ = store.Add(makePending("agent-1", "sess-1", "1.1", timeout))
	_ = store.Add(makePending("agent-2", "sess-2", "2.1", timeout))
	_ = store.Add(makePending("agent-3", "sess-1", "1.3", timeout))

	all, err := store.GetAll()
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(all))
	}
}

func TestPendingRotationStore_GetAll_SkipsExpired(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := NewPendingRotationStoreWithPath(filepath.Join(dir, "pending.jsonl"))

	future := time.Now().Add(10 * time.Minute)
	past := time.Now().Add(-1 * time.Minute) // already expired

	_ = store.Add(makePending("agent-1", "sess-1", "1.1", future))
	_ = store.Add(makePending("agent-2", "sess-1", "1.2", past))

	all, err := store.GetAll()
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	// Only future entry should remain
	if len(all) != 1 {
		t.Fatalf("expected 1 non-expired entry, got %d", len(all))
	}
	if all[0].AgentID != "agent-1" {
		t.Errorf("expected agent-1, got %q", all[0].AgentID)
	}
}

func TestPendingRotationStore_GetForSession(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := NewPendingRotationStoreWithPath(filepath.Join(dir, "pending.jsonl"))

	timeout := time.Now().Add(10 * time.Minute)
	_ = store.Add(makePending("agent-1", "sess-1", "1.1", timeout))
	_ = store.Add(makePending("agent-2", "sess-2", "2.1", timeout))
	_ = store.Add(makePending("agent-3", "sess-1", "1.3", timeout))

	result, err := store.GetForSession("sess-1")
	if err != nil {
		t.Fatalf("GetForSession: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 entries for sess-1, got %d", len(result))
	}

	// None for non-existent session
	result, err = store.GetForSession("sess-99")
	if err != nil {
		t.Fatalf("GetForSession empty: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected 0 entries for sess-99, got %d", len(result))
	}
}

func TestPendingRotationStore_Clear(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "pending.jsonl")
	store := NewPendingRotationStoreWithPath(path)

	timeout := time.Now().Add(10 * time.Minute)
	_ = store.Add(makePending("agent-1", "sess-1", "1.1", timeout))

	// Verify file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("expected file to exist before clear")
	}

	if err := store.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	// File should be gone
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected file to be removed after clear")
	}

	// Clear on already-cleared store should be no-op
	if err := store.Clear(); err != nil {
		t.Fatalf("Clear on empty: %v", err)
	}
}

func TestPendingRotationStore_GetOnNonExistentFile(t *testing.T) {
	t.Parallel()
	store := NewPendingRotationStoreWithPath("/tmp/nonexistent-test-pending.jsonl")

	// Get should return nil, nil
	got, err := store.Get("agent-1")
	if err != nil {
		t.Fatalf("Get nonexistent file: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent file")
	}

	// GetAll should return empty
	all, err := store.GetAll()
	if err != nil {
		t.Fatalf("GetAll nonexistent file: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("expected 0 entries, got %d", len(all))
	}
}

func TestPendingRotationStore_MalformedLines(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "pending.jsonl")

	// Write a mix of valid and malformed JSONL
	timeout := time.Now().Add(10 * time.Minute).Format(time.RFC3339Nano)
	content := `not-json
{"agent_id":"agent-1","session_name":"sess-1","pane_id":"1.1","context_percent":85.5,"created_at":"2026-01-01T00:00:00Z","timeout_at":"` + timeout + `","default_action":"rotate"}
{broken json
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	store := NewPendingRotationStoreWithPath(path)
	all, err := store.GetAll()
	if err != nil {
		t.Fatalf("GetAll with malformed: %v", err)
	}
	// Should skip malformed lines and return the valid one
	if len(all) != 1 {
		t.Fatalf("expected 1 valid entry, got %d", len(all))
	}
	if all[0].AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want agent-1", all[0].AgentID)
	}
}

func TestPendingRotationStore_AddFailsClosedOnReadError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "pending.jsonl")
	store := NewPendingRotationStoreWithPath(path)

	timeout := time.Now().Add(10 * time.Minute)
	if err := store.Add(makePending("agent-1", "sess-1", "1.1", timeout)); err != nil {
		t.Fatalf("Add initial entry: %v", err)
	}
	initial, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile initial: %v", err)
	}

	corrupted := append(append([]byte{}, initial...), []byte(strings.Repeat("x", 1024*1024+1))...)
	if err := os.WriteFile(path, corrupted, 0o600); err != nil {
		t.Fatalf("WriteFile corrupted: %v", err)
	}

	err = store.Add(makePending("agent-2", "sess-1", "1.2", timeout))
	if err == nil {
		t.Fatal("expected Add to fail on scanner read error")
	}
	if !strings.Contains(err.Error(), "reading pending rotations") {
		t.Fatalf("expected read error context, got %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after failed Add: %v", err)
	}
	if !bytes.Equal(after, corrupted) {
		t.Fatal("Add rewrote pending rotations after a read error")
	}
}

func TestPendingRotationStore_AddReturnsMarshalError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "pending.jsonl")
	store := NewPendingRotationStoreWithPath(path)

	pending := makePending("agent-1", "sess-1", "1.1", time.Now().Add(10*time.Minute))
	pending.ContextPercent = math.NaN()

	err := store.Add(pending)
	if err == nil {
		t.Fatal("expected Add to fail when pending rotation cannot be marshaled")
	}
	if !strings.Contains(err.Error(), "marshaling pending rotation") {
		t.Fatalf("expected marshal error context, got %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("expected no committed pending file after marshal error, stat err=%v", statErr)
	}
}

func TestPendingRotationStore_AddRejectsNil(t *testing.T) {
	t.Parallel()
	store := NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	if err := store.Add(nil); err == nil {
		t.Fatal("expected Add(nil) to return an error")
	}
}

func TestPendingRotationStore_Persistence(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "pending.jsonl")

	timeout := time.Now().Add(10 * time.Minute)

	// Write with one store instance
	store1 := NewPendingRotationStoreWithPath(path)
	_ = store1.Add(makePending("agent-1", "sess-1", "1.1", timeout))
	_ = store1.Add(makePending("agent-2", "sess-2", "2.1", timeout))

	// Read with a new store instance
	store2 := NewPendingRotationStoreWithPath(path)
	all, err := store2.GetAll()
	if err != nil {
		t.Fatalf("GetAll from new store: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 entries from persisted store, got %d", len(all))
	}
}

func TestStoredPendingRotation_ToPendingRotation(t *testing.T) {
	t.Parallel()
	now := time.Now()
	timeout := now.Add(5 * time.Minute)

	stored := &StoredPendingRotation{
		AgentID:        "agent-1",
		SessionName:    "sess-1",
		PaneID:         "1.1",
		ContextPercent: 90.0,
		CreatedAt:      now,
		TimeoutAt:      timeout,
		DefaultAction:  ConfirmCompact,
		WorkDir:        "/projects/test",
	}

	p := stored.ToPendingRotation()
	if p.AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want agent-1", p.AgentID)
	}
	if p.DefaultAction != ConfirmCompact {
		t.Errorf("DefaultAction = %q, want compact", p.DefaultAction)
	}
	if p.WorkDir != "/projects/test" {
		t.Errorf("WorkDir = %q, want /projects/test", p.WorkDir)
	}
}

func TestFromPendingRotation_Fields(t *testing.T) {
	t.Parallel()
	now := time.Now()
	timeout := now.Add(5 * time.Minute)

	p := &PendingRotation{
		AgentID:        "agent-2",
		SessionName:    "sess-2",
		PaneID:         "2.2",
		ContextPercent: 75.5,
		CreatedAt:      now,
		TimeoutAt:      timeout,
		DefaultAction:  ConfirmIgnore,
		WorkDir:        "/projects/other",
	}

	stored := FromPendingRotation(p)
	if stored.AgentID != "agent-2" {
		t.Errorf("AgentID = %q, want agent-2", stored.AgentID)
	}
	if stored.DefaultAction != ConfirmIgnore {
		t.Errorf("DefaultAction = %q, want ignore", stored.DefaultAction)
	}
	if stored.WorkDir != "/projects/other" {
		t.Errorf("WorkDir = %q, want /projects/other", stored.WorkDir)
	}
}

// =============================================================================
// Global wrapper function tests
// =============================================================================

func TestAddPendingRotation_Global(t *testing.T) {
	// Not parallel: modifies package-level DefaultPendingRotationStore
	origStore := DefaultPendingRotationStore
	tmpStore := NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	DefaultPendingRotationStore = tmpStore
	t.Cleanup(func() { DefaultPendingRotationStore = origStore })

	pr := makePending("agent-global-1", "sess-g", "1.0", time.Now().Add(5*time.Minute))
	if err := AddPendingRotation(pr); err != nil {
		t.Fatalf("AddPendingRotation: %v", err)
	}

	got, err := GetPendingRotationByID("agent-global-1")
	if err != nil {
		t.Fatalf("GetPendingRotationByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil pending rotation")
	}
	if got.SessionName != "sess-g" {
		t.Errorf("SessionName = %q, want sess-g", got.SessionName)
	}
}

func TestRemovePendingRotation_Global(t *testing.T) {
	origStore := DefaultPendingRotationStore
	tmpStore := NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	DefaultPendingRotationStore = tmpStore
	t.Cleanup(func() { DefaultPendingRotationStore = origStore })

	pr := makePending("agent-rm-1", "sess-rm", "2.0", time.Now().Add(5*time.Minute))
	if err := AddPendingRotation(pr); err != nil {
		t.Fatalf("AddPendingRotation: %v", err)
	}

	if err := RemovePendingRotation("agent-rm-1"); err != nil {
		t.Fatalf("RemovePendingRotation: %v", err)
	}

	got, err := GetPendingRotationByID("agent-rm-1")
	if err != nil {
		t.Fatalf("GetPendingRotationByID after remove: %v", err)
	}
	if got != nil {
		t.Error("expected nil after removal")
	}
}

func TestGetAllPendingRotations_Global(t *testing.T) {
	origStore := DefaultPendingRotationStore
	tmpStore := NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	DefaultPendingRotationStore = tmpStore
	t.Cleanup(func() { DefaultPendingRotationStore = origStore })

	pr1 := makePending("agent-all-1", "sess-1", "1.0", time.Now().Add(5*time.Minute))
	pr2 := makePending("agent-all-2", "sess-2", "2.0", time.Now().Add(5*time.Minute))
	_ = AddPendingRotation(pr1)
	_ = AddPendingRotation(pr2)

	all, err := GetAllPendingRotations()
	if err != nil {
		t.Fatalf("GetAllPendingRotations: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 pending rotations, got %d", len(all))
	}
}

func TestGetPendingRotationsForSession_Global(t *testing.T) {
	origStore := DefaultPendingRotationStore
	tmpStore := NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	DefaultPendingRotationStore = tmpStore
	t.Cleanup(func() { DefaultPendingRotationStore = origStore })

	pr1 := makePending("agent-sess-1", "target-sess", "1.0", time.Now().Add(5*time.Minute))
	pr2 := makePending("agent-sess-2", "other-sess", "2.0", time.Now().Add(5*time.Minute))
	pr3 := makePending("agent-sess-3", "target-sess", "3.0", time.Now().Add(5*time.Minute))
	_ = AddPendingRotation(pr1)
	_ = AddPendingRotation(pr2)
	_ = AddPendingRotation(pr3)

	forSession, err := GetPendingRotationsForSession("target-sess")
	if err != nil {
		t.Fatalf("GetPendingRotationsForSession: %v", err)
	}
	if len(forSession) != 2 {
		t.Errorf("expected 2 rotations for target-sess, got %d", len(forSession))
	}
}
