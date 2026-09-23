package context

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPendingConfirmationRetainsChoiceAndReplaysReceipt(t *testing.T) {
	store := NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	pending := makePending("demo__cc_1", "demo", "%3", time.Now().Add(time.Hour))
	pending.PanePID = 123
	pending.PaneType = "cc"
	if err := store.Add(pending); err != nil {
		t.Fatal(err)
	}
	claimed, release, err := store.BeginConfirmation(t.Context(), pending.AgentID, ConfirmRotate, false)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.SelectedAction != ConfirmRotate || claimed.ExecutionState != RotationStateInProgress || claimed.PanePID != 123 {
		t.Fatalf("claim did not preserve request identity and choice: %+v", claimed)
	}
	if err := store.Remove(pending.AgentID); err == nil {
		t.Fatal("ordinary removal consumed owned execution")
	}
	if err := store.Add(pending); err == nil {
		t.Fatal("re-enqueue overwrote owned execution")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	failure := RotationResult{OldAgentID: pending.AgentID, OldPaneID: "%3", State: RotationStateFailed, Error: "handoff delivery canceled; original preserved"}
	if err := store.FinishConfirmation(canceled, claimed, failure); err != nil {
		t.Fatalf("cancellation lost outcome: %v", err)
	}
	release()
	stored, err := store.Get(pending.AgentID)
	if err != nil || stored == nil || stored.SelectedAction != ConfirmRotate || stored.Result == nil || stored.Result.Error != failure.Error {
		t.Fatalf("failure receipt = %+v, %v", stored, err)
	}
	if _, _, err := store.BeginConfirmation(t.Context(), pending.AgentID, ConfirmRotate, false); err == nil {
		t.Fatal("failed operation silently repeated")
	}
	if _, _, err := store.BeginConfirmation(t.Context(), pending.AgentID, ConfirmCompact, true); err == nil {
		t.Fatal("retry changed the selected action")
	}
	retried, release, err := store.BeginConfirmation(t.Context(), pending.AgentID, ConfirmRotate, true)
	if err != nil {
		t.Fatal(err)
	}
	if retried.ExecutionID == claimed.ExecutionID {
		t.Fatal("retry reused execution ownership")
	}
	success := RotationResult{Success: true, OldAgentID: pending.AgentID, OldPaneID: "%3", NewPaneID: "%9", State: RotationStateCompleted}
	if err := store.FinishConfirmation(t.Context(), retried, success); err != nil {
		t.Fatal(err)
	}
	release()
	if pending, err := store.Get(pending.AgentID); err != nil || pending != nil {
		t.Fatalf("completed receipt remained actionable: %+v, %v", pending, err)
	}
	replay, release, err := store.BeginConfirmation(t.Context(), retried.AgentID, ConfirmRotate, false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if replay.Result == nil || !replay.Result.Success || replay.Result.NewPaneID != "%9" || replay.ExecutionID != retried.ExecutionID {
		t.Fatalf("duplicate confirmation did not replay durable outcome: %+v", replay)
	}
}

func TestPendingConfirmationInterruptedClaimSurvivesTimeout(t *testing.T) {
	store := NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	pending := makePending("demo__cc_1", "demo", "%3", time.Now().Add(-time.Hour))
	pending.SelectedAction = ConfirmCompact
	pending.ExecutionState = RotationStateInProgress
	pending.ExecutionID = "interrupted-owner"
	if err := store.Add(pending); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(makePending("other__cc_1", "other", "%4", time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(pending.AgentID)
	if err != nil || stored == nil || stored.IsExpired() {
		t.Fatalf("interrupted choice expired: %+v, %v", stored, err)
	}
	if _, _, err := store.BeginConfirmation(t.Context(), pending.AgentID, ConfirmCompact, false); err == nil || !strings.Contains(err.Error(), "--retry") {
		t.Fatalf("interrupted attempt was not diagnosed: %v", err)
	}
}

func TestPendingConfirmationExpiryReconcilesOperatorChoice(t *testing.T) {
	store := NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	pending := makePending("demo__cc_1", "demo", "%3", time.Now().Add(time.Hour))
	if err := store.Add(pending); err != nil {
		t.Fatal(err)
	}
	claimed, release, err := store.BeginConfirmation(t.Context(), pending.AgentID, ConfirmCompact, false)
	if err != nil {
		t.Fatal(err)
	}
	result := RotationResult{Success: true, State: RotationStateAborted, OldAgentID: pending.AgentID, OldPaneID: pending.PaneID}
	if err := store.FinishConfirmation(t.Context(), claimed, result); err != nil {
		t.Fatal(err)
	}
	release()
	replayed, release, err := store.BeginExpiredConfirmation(t.Context(), pending.AgentID, ConfirmRotate)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.SelectedAction != ConfirmCompact || replayed.Result == nil || !replayed.Result.Success {
		t.Fatalf("expiry replaced acknowledged operator choice: %+v", replayed)
	}
	release()
	pending.AgentID = "demo__cc_2"
	pending.TimeoutAt = time.Now().Add(-time.Minute)
	pending.DefaultAction = ConfirmIgnore
	if err := store.Add(pending); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.BeginExpiredConfirmation(t.Context(), pending.AgentID, ConfirmRotate); err == nil {
		t.Fatal("stale in-memory default replaced durable timeout choice")
	}
}

func TestPendingConfirmationExpirySurvivesOtherSessionWrites(t *testing.T) {
	store := NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	pending := makePending("expired__cc_1", "expired", "%3", time.Now().Add(-time.Minute))
	if err := store.Add(pending); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(makePending("other__cc_1", "other", "%4", time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove("other__cc_1"); err != nil {
		t.Fatal(err)
	}
	claimed, release, err := store.BeginExpiredConfirmation(t.Context(), pending.AgentID, pending.DefaultAction)
	if err != nil {
		t.Fatalf("unrelated writes discarded a waiting timeout action: %v", err)
	}
	defer release()
	if claimed.SelectedAction != ConfirmRotate {
		t.Fatalf("wrong timeout action claimed: %+v", claimed)
	}
}

func TestPendingConfirmationProcessHelper(t *testing.T) {
	path := os.Getenv("NTM_PENDING_CLAIM_TEST")
	if path == "" {
		return
	}
	store := NewPendingRotationStoreWithPath(path)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, release, err := store.BeginConfirmation(ctx, "demo__cc_1", ConfirmRotate, false)
	if release != nil {
		release()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("independent process acquired live confirmation: %v", err)
	}
}

func TestPendingConfirmationExcludesAnotherProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending.jsonl")
	store := NewPendingRotationStoreWithPath(path)
	if err := store.Add(makePending("demo__cc_1", "demo", "%3", time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	_, release, err := store.BeginConfirmation(t.Context(), "demo__cc_1", ConfirmRotate, false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestPendingConfirmationProcessHelper$")
	child.Env = append(os.Environ(), "NTM_PENDING_CLAIM_TEST="+path)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("independent process claim check: %v\n%s", err, output)
	}
}

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
	if _, err := store.GetAll(); err == nil {
		t.Fatal("malformed durable state was accepted as an incomplete pending list")
	}
	if _, _, err := store.BeginConfirmation(t.Context(), "agent-1", ConfirmRotate, false); err == nil {
		t.Fatal("confirmation proceeded despite unobservable durable ownership")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != content {
		t.Fatalf("malformed durable state changed: %q, %v", data, err)
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
