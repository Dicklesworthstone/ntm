package assignment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewStore(t *testing.T) {
	store := NewStore("test-session")
	if store.SessionName != "test-session" {
		t.Errorf("expected session name 'test-session', got '%s'", store.SessionName)
	}
	if store.Assignments == nil {
		t.Error("expected assignments map to be initialized")
	}
	if len(store.Assignments) != 0 {
		t.Errorf("expected empty assignments, got %d", len(store.Assignments))
	}
	if store.Version != assignmentStoreVersion {
		t.Errorf("expected version %d, got %d", assignmentStoreVersion, store.Version)
	}
	if store.PersistenceGeneration != 0 {
		t.Errorf("expected unsaved persistence generation 0, got %d", store.PersistenceGeneration)
	}
}

func TestAssign(t *testing.T) {
	// Use temp directory
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store := NewStore("test-session")

	assignment, err := store.Assign("bd-123", "Test bead title", 1, "claude", "TestAgent", "Test prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if assignment.BeadID != "bd-123" {
		t.Errorf("expected bead ID 'bd-123', got '%s'", assignment.BeadID)
	}
	if assignment.BeadTitle != "Test bead title" {
		t.Errorf("expected bead title 'Test bead title', got '%s'", assignment.BeadTitle)
	}
	if assignment.Pane != 1 {
		t.Errorf("expected pane 1, got %d", assignment.Pane)
	}
	if assignment.AgentType != "claude" {
		t.Errorf("expected agent type 'claude', got '%s'", assignment.AgentType)
	}
	if assignment.AgentName != "TestAgent" {
		t.Errorf("expected agent name 'TestAgent', got '%s'", assignment.AgentName)
	}
	if assignment.Status != StatusAssigned {
		t.Errorf("expected status 'assigned', got '%s'", assignment.Status)
	}
	if assignment.PromptSent != "Test prompt" {
		t.Errorf("expected prompt 'Test prompt', got '%s'", assignment.PromptSent)
	}
}

func TestGet(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store := NewStore("test-session")
	_, _ = store.Assign("bd-123", "Test bead", 1, "claude", "", "")

	// Test getting existing assignment
	a := store.Get("bd-123")
	if a == nil {
		t.Fatal("expected assignment, got nil")
	}
	if a.BeadID != "bd-123" {
		t.Errorf("expected bead ID 'bd-123', got '%s'", a.BeadID)
	}

	// Test getting non-existent assignment
	a = store.Get("bd-nonexistent")
	if a != nil {
		t.Errorf("expected nil for non-existent assignment, got %v", a)
	}
}

func TestGetReturnsSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store := NewStore("test-session")
	assigned, _ := store.Assign("bd-123", "Test bead", 1, "claude", "", "prompt")
	assigned.Pane = 99

	if err := store.MarkWorking("bd-123"); err != nil {
		t.Fatalf("MarkWorking: %v", err)
	}

	got := store.Get("bd-123")
	if got == nil {
		t.Fatal("expected assignment, got nil")
	}
	if got.Pane != 1 {
		t.Fatalf("expected pane 1, got %d", got.Pane)
	}
	if got.StartedAt == nil {
		t.Fatal("expected StartedAt to be set")
	}

	mutated := got.StartedAt.Add(5 * time.Minute)
	*got.StartedAt = mutated
	got.PromptSent = "mutated"

	fresh := store.Get("bd-123")
	if fresh == nil {
		t.Fatal("expected assignment on second read, got nil")
	}
	if fresh.PromptSent != "prompt" {
		t.Fatalf("expected stored prompt to remain %q, got %q", "prompt", fresh.PromptSent)
	}
	if fresh.StartedAt == nil {
		t.Fatal("expected StartedAt on second read")
	}
	if fresh.StartedAt.Equal(mutated) {
		t.Fatalf("expected StartedAt snapshot to be isolated from caller mutation")
	}
}

func TestList(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store := NewStore("test-session")
	_, _ = store.Assign("bd-1", "Bead 1", 1, "claude", "", "")
	_, _ = store.Assign("bd-2", "Bead 2", 2, "codex", "", "")
	_, _ = store.Assign("bd-3", "Bead 3", 3, "gemini", "", "")

	assignments := store.List()
	if len(assignments) != 3 {
		t.Errorf("expected 3 assignments, got %d", len(assignments))
	}
}

func TestListByStatus(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store := NewStore("test-session")
	_, _ = store.Assign("bd-1", "Bead 1", 1, "claude", "", "")
	_, _ = store.Assign("bd-2", "Bead 2", 2, "codex", "", "")

	// Mark one as working
	_ = store.MarkWorking("bd-1")

	assigned := store.ListByStatus(StatusAssigned)
	if len(assigned) != 1 {
		t.Errorf("expected 1 assigned, got %d", len(assigned))
	}

	working := store.ListByStatus(StatusWorking)
	if len(working) != 1 {
		t.Errorf("expected 1 working, got %d", len(working))
	}
}

func TestListActive(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store := NewStore("test-session")
	_, _ = store.Assign("bd-1", "Bead 1", 1, "claude", "", "")
	_, _ = store.Assign("bd-2", "Bead 2", 2, "codex", "", "")
	_, _ = store.Assign("bd-3", "Bead 3", 3, "gemini", "", "")

	// Mark one as working and one as completed
	_ = store.MarkWorking("bd-1")
	_ = store.MarkWorking("bd-3")
	_ = store.MarkCompleted("bd-3")

	active := store.ListActive()
	if len(active) != 2 {
		t.Errorf("expected 2 active (assigned + working), got %d", len(active))
	}
}

func TestStateTransitions(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	tests := []struct {
		name  string
		from  AssignmentStatus
		to    AssignmentStatus
		valid bool
	}{
		{"assigned to working", StatusAssigned, StatusWorking, true},
		{"assigned to failed", StatusAssigned, StatusFailed, true},
		// `assigned -> completed` is permitted because beads can close externally
		// (br close from another agent) before the assignment store ever observed
		// a `working` transition. See ValidTransitions docs and #124.
		{"assigned to completed (external close)", StatusAssigned, StatusCompleted, true},
		{"working to completed", StatusWorking, StatusCompleted, true},
		{"working to failed", StatusWorking, StatusFailed, true},
		{"working to reassigned", StatusWorking, StatusReassigned, true},
		{"completed to anything", StatusCompleted, StatusAssigned, false},
		{"failed to assigned (retry)", StatusFailed, StatusAssigned, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidTransition(tt.from, tt.to)
			if result != tt.valid {
				t.Errorf("expected isValidTransition(%s, %s) = %v, got %v",
					tt.from, tt.to, tt.valid, result)
			}
		})
	}
}

func TestUpdateStatus(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store := NewStore("test-session")
	_, _ = store.Assign("bd-123", "Test bead", 1, "claude", "", "")

	// Valid transition: assigned -> working
	err := store.MarkWorking("bd-123")
	if err != nil {
		t.Errorf("unexpected error marking as working: %v", err)
	}

	a := store.Get("bd-123")
	if a.Status != StatusWorking {
		t.Errorf("expected status working, got %s", a.Status)
	}
	if a.StartedAt == nil {
		t.Error("expected StartedAt to be set")
	}

	// Valid transition: working -> completed
	err = store.MarkCompleted("bd-123")
	if err != nil {
		t.Errorf("unexpected error marking as completed: %v", err)
	}

	a = store.Get("bd-123")
	if a.Status != StatusCompleted {
		t.Errorf("expected status completed, got %s", a.Status)
	}
	if a.CompletedAt == nil {
		t.Error("expected CompletedAt to be set")
	}
}

func TestInvalidTransition(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store := NewStore("test-session")
	if _, err := store.Assign("bd-123", "Test bead", 1, "claude", "", ""); err != nil {
		t.Fatalf("Assign() error = %v", err)
	}

	// Move to a terminal state first. `assigned -> completed` is now valid
	// (#124) because beads can close externally before the store ever
	// observed a `working` transition; we use that valid transition here as
	// setup so we can then assert the *terminal -> non-terminal* transition
	// is still rejected.
	if err := store.UpdateStatus("bd-123", StatusCompleted); err != nil {
		t.Fatalf("setup: assigned -> completed should be valid, got: %v", err)
	}

	// Invalid transition: completed -> assigned (terminal -> any).
	err := store.UpdateStatus("bd-123", StatusAssigned)
	if err == nil {
		t.Fatal("expected error for invalid transition completed->assigned, got nil")
	}
	if _, ok := err.(*InvalidTransitionError); !ok {
		t.Errorf("expected InvalidTransitionError, got %T (%v)", err, err)
	}

	// Status should remain completed
	a := store.Get("bd-123")
	if a == nil {
		t.Fatal("Get(bd-123) returned nil")
	}
	if a.Status != StatusCompleted {
		t.Errorf("expected status to remain completed, got %s", a.Status)
	}
}

// TestExternalCloseTransitionsAssignedToCompleted verifies the #124 fix:
// when br close happens externally before the agent ever reported "working",
// the watch loop's correlation step must be able to mark the assignment as
// completed without an "Invalid transition assigned -> completed" error
// leaving the row stuck.
func TestExternalCloseTransitionsAssignedToCompleted(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store := NewStore("test-session-extclose")
	_, _ = store.Assign("bd-456", "External close bead", 2, "claude", "", "")

	// Skip Working — simulate br close fired externally before the agent
	// reported any progress that would have moved us to Working.
	if err := store.UpdateStatus("bd-456", StatusCompleted); err != nil {
		t.Fatalf("assigned -> completed (external close) should be valid: %v", err)
	}

	a := store.Get("bd-456")
	if a.Status != StatusCompleted {
		t.Fatalf("status = %s, want completed", a.Status)
	}
	if a.CompletedAt == nil {
		t.Error("CompletedAt should be set after assigned -> completed transition")
	}
}

func TestMarkFailed(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store := NewStore("test-session")
	_, _ = store.Assign("bd-123", "Test bead", 1, "claude", "", "")

	err := store.MarkFailed("bd-123", "Agent crashed")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	a := store.Get("bd-123")
	if a.Status != StatusFailed {
		t.Errorf("expected status failed, got %s", a.Status)
	}
	if a.FailReason != "Agent crashed" {
		t.Errorf("expected fail reason 'Agent crashed', got '%s'", a.FailReason)
	}
	if a.FailedAt == nil {
		t.Error("expected FailedAt to be set")
	}
}

func TestRetryTransitionClearsFailureMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store := NewStore("test-session")
	_, _ = store.Assign("bd-123", "Test bead", 1, "claude", "", "")
	if err := store.MarkWorking("bd-123"); err != nil {
		t.Fatalf("MarkWorking: %v", err)
	}
	if err := store.MarkFailed("bd-123", "Agent crashed"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if err := store.UpdateStatus("bd-123", StatusAssigned); err != nil {
		t.Fatalf("UpdateStatus retry: %v", err)
	}

	got := store.Get("bd-123")
	if got == nil {
		t.Fatal("expected assignment, got nil")
	}
	if got.Status != StatusAssigned {
		t.Fatalf("expected status assigned, got %s", got.Status)
	}
	if got.FailedAt != nil {
		t.Fatalf("expected FailedAt to be cleared on retry")
	}
	if got.StartedAt != nil {
		t.Fatalf("expected StartedAt to be cleared on retry")
	}
	if got.FailReason != "" {
		t.Fatalf("expected FailReason to be cleared on retry, got %q", got.FailReason)
	}
	if got.FailureReason != "" {
		t.Fatalf("expected FailureReason to be cleared on retry, got %q", got.FailureReason)
	}
}

func TestReassign(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store := NewStore("test-session")
	_, _ = store.Assign("bd-123", "Test bead", 1, "claude", "Agent1", "Do the thing")
	retryCount := 2
	if err := store.Update("bd-123", AssignmentUpdate{RetryCount: &retryCount}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Must be working to reassign
	_ = store.MarkWorking("bd-123")

	newAssignment, err := store.Reassign("bd-123", ReassignmentTarget{
		Pane: 2, AgentType: "codex", AgentName: "Agent2", DispatchTarget: "%22", OccupancyKey: "%22",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if newAssignment.Pane != 2 {
		t.Errorf("expected pane 2, got %d", newAssignment.Pane)
	}
	if newAssignment.AgentType != "codex" {
		t.Errorf("expected agent type 'codex', got '%s'", newAssignment.AgentType)
	}
	if newAssignment.AgentName != "Agent2" {
		t.Errorf("expected agent name 'Agent2', got '%s'", newAssignment.AgentName)
	}
	if newAssignment.Status != StatusAssigned {
		t.Errorf("expected status assigned, got %s", newAssignment.Status)
	}
	if newAssignment.PromptSent != "" {
		t.Errorf("expected prompt to be empty until resent, got '%s'", newAssignment.PromptSent)
	}
	if newAssignment.RetryCount != retryCount {
		t.Errorf("expected retry count %d to be preserved, got %d", retryCount, newAssignment.RetryCount)
	}
	if newAssignment.DispatchTarget != "%22" || newAssignment.OccupancyKey != "%22" {
		t.Fatalf("reassignment lost physical target identity: %+v", newAssignment)
	}
}

func TestUpdateAssignmentMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store := NewStore("test-session")
	_, _ = store.Assign("bd-123", "Test bead", 1, "claude", "", "")

	prompt := "Actual delivered prompt"
	retryCount := 3
	if err := store.Update("bd-123", AssignmentUpdate{
		PromptSent: &prompt,
		RetryCount: &retryCount,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got := store.Get("bd-123")
	if got == nil {
		t.Fatal("expected assignment, got nil")
	}
	if got.PromptSent != prompt {
		t.Fatalf("expected prompt %q, got %q", prompt, got.PromptSent)
	}
	if got.RetryCount != retryCount {
		t.Fatalf("expected retry count %d, got %d", retryCount, got.RetryCount)
	}
}

func TestRemove(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store := NewStore("test-session")
	_, _ = store.Assign("bd-123", "Test bead", 1, "claude", "", "")

	store.Remove("bd-123")

	a := store.Get("bd-123")
	if a != nil {
		t.Errorf("expected nil after remove, got %v", a)
	}
}

func TestClear(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store := NewStore("test-session")
	_, _ = store.Assign("bd-1", "Bead 1", 1, "claude", "", "")
	_, _ = store.Assign("bd-2", "Bead 2", 2, "codex", "", "")

	store.Clear()

	if len(store.List()) != 0 {
		t.Errorf("expected empty after clear, got %d", len(store.List()))
	}
}

func TestStats(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store := NewStore("test-session")
	_, _ = store.Assign("bd-1", "Bead 1", 1, "claude", "", "")
	_, _ = store.Assign("bd-2", "Bead 2", 2, "codex", "", "")
	_, _ = store.Assign("bd-3", "Bead 3", 3, "gemini", "", "")
	_, _ = store.Assign("bd-4", "Bead 4", 4, "claude", "", "")

	_ = store.MarkWorking("bd-2")
	_ = store.MarkWorking("bd-3")
	_ = store.MarkCompleted("bd-3")
	_ = store.MarkFailed("bd-4", "crashed")

	stats := store.Stats()
	if stats.Total != 4 {
		t.Errorf("expected total 4, got %d", stats.Total)
	}
	if stats.Assigned != 1 {
		t.Errorf("expected assigned 1, got %d", stats.Assigned)
	}
	if stats.Working != 1 {
		t.Errorf("expected working 1, got %d", stats.Working)
	}
	if stats.Completed != 1 {
		t.Errorf("expected completed 1, got %d", stats.Completed)
	}
	if stats.Failed != 1 {
		t.Errorf("expected failed 1, got %d", stats.Failed)
	}
}

func TestPersistenceSaveLoad(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Create and save
	store1 := NewStore("persist-test")
	_, _ = store1.Assign("bd-123", "Test bead", 1, "claude", "TestAgent", "Test prompt")
	_ = store1.MarkWorking("bd-123")

	// Load in new store
	store2, err := LoadStore("persist-test")
	if err != nil {
		t.Fatalf("unexpected error loading: %v", err)
	}

	a := store2.Get("bd-123")
	if a == nil {
		t.Fatal("expected assignment after load, got nil")
	}
	if a.BeadID != "bd-123" {
		t.Errorf("expected bead ID 'bd-123', got '%s'", a.BeadID)
	}
	if a.Status != StatusWorking {
		t.Errorf("expected status working, got %s", a.Status)
	}
	if a.AgentName != "TestAgent" {
		t.Errorf("expected agent name 'TestAgent', got '%s'", a.AgentName)
	}
	if store2.PersistenceGeneration < 2 {
		t.Fatalf("persistence generation=%d, want at least two completed saves", store2.PersistenceGeneration)
	}
	primaryData, err := os.ReadFile(store2.path)
	if err != nil {
		t.Fatalf("read primary: %v", err)
	}
	backupData, err := os.ReadFile(store2.path + ".bak")
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(primaryData) != string(backupData) {
		t.Fatal("completed save did not publish identical primary and backup snapshots")
	}
}

func TestPersistenceBackupRecovery(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Create valid backup file
	// assignments are now in ~/.ntm/sessions/<session>/assignments.json
	// So we need to create <tmpDir>/.ntm/sessions/backup-test/assignments.json.bak
	dir := filepath.Join(tmpDir, ".ntm", "sessions", "backup-test")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}

	backupStore := &AssignmentStore{
		SessionName: "backup-test",
		Assignments: map[string]*Assignment{
			"bd-backup": {
				BeadID:     "bd-backup",
				BeadTitle:  "Backup bead",
				Pane:       1,
				AgentType:  "claude",
				Status:     StatusAssigned,
				AssignedAt: time.Now().UTC(),
			},
		},
		UpdatedAt: time.Now().UTC(),
		Version:   1,
	}

	data, _ := json.MarshalIndent(backupStore, "", "  ")
	// Name is assignments.json (assignmentsDirName + fileExtension)
	bakPath := filepath.Join(dir, "assignments.json.bak")
	if err := os.WriteFile(bakPath, data, 0644); err != nil {
		t.Fatalf("failed to write backup: %v", err)
	}

	// Write corrupted main file
	mainPath := filepath.Join(dir, "assignments.json")
	if err := os.WriteFile(mainPath, []byte("invalid json"), 0644); err != nil {
		t.Fatalf("failed to write corrupted file: %v", err)
	}

	// Load should recover from backup
	store, err := LoadStore("backup-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	a := store.Get("bd-backup")
	if a == nil {
		t.Fatal("expected assignment from backup, got nil")
	}
	if a.BeadID != "bd-backup" {
		t.Errorf("expected bead ID 'bd-backup', got '%s'", a.BeadID)
	}
	if store.Version != assignmentStoreVersion {
		t.Fatalf("recovered store version=%d, want migrated version %d", store.Version, assignmentStoreVersion)
	}
}

func TestPersistenceRecoversReceiptPublishedOnlyToBackup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const (
		session = "receipt-backup-recovery"
		beadID  = "ntm-receipt-backup-recovery"
		key     = "receipt-backup-key"
	)
	now := time.Now().UTC()
	req := AtomicRequest{
		BeadID: beadID, BeadTitle: "Receipt recovery", Target: "%88", OccupancyKey: "%88",
		Pane: 1, AgentType: "codex", AgentName: "CodexOne", Actor: "CodexOne",
		Prompt: "recover exactly once", IdempotencyKey: key,
	}
	actor := StableClaimActor(req.Actor, key)
	store := NewStore(session)
	if _, err := store.RecordAtomicIntent(req, actor, now); err != nil {
		t.Fatalf("RecordAtomicIntent: %v", err)
	}
	if _, err := store.RecordAtomicClaim(req, ClaimReceipt{BeadID: beadID, Actor: actor, Status: "in_progress", ClaimedAt: now}); err != nil {
		t.Fatalf("RecordAtomicClaim: %v", err)
	}
	if err := store.RecordAtomicDispatchStarted(beadID, key, now); err != nil {
		t.Fatalf("RecordAtomicDispatchStarted: %v", err)
	}

	injected := errors.New("stop after backup publication")
	hookCalls := 0
	store.afterBackupPublished = func(snapshot *AssignmentStore) error {
		hookCalls++
		stored := snapshot.Assignments[beadID]
		if stored == nil || stored.DispatchState != DispatchSent || stored.DispatchReceiptID != "delivery-88" {
			t.Errorf("backup publication snapshot=%+v", stored)
		}
		return injected
	}
	if err := store.RecordAtomicDispatchSent(beadID, key, req.Prompt, DispatchReceipt{DeliveryID: "delivery-88", Duration: 25 * time.Millisecond}, now); !errors.Is(err, injected) {
		t.Fatalf("RecordAtomicDispatchSent error=%v, want injected failure", err)
	}
	if hookCalls != 1 {
		t.Fatalf("backup publication hook calls=%d, want 1", hookCalls)
	}

	primary, primaryData := mustReadAssignmentSnapshot(t, store.path, session)
	backup, backupData := mustReadAssignmentSnapshot(t, store.path+".bak", session)
	if primary.Assignments[beadID].DispatchState != DispatchSending {
		t.Fatalf("primary dispatch state=%s, want sending", primary.Assignments[beadID].DispatchState)
	}
	if stored := backup.Assignments[beadID]; stored == nil || stored.DispatchState != DispatchSent || stored.DispatchReceiptID != "delivery-88" {
		t.Fatalf("backup receipt snapshot=%+v", stored)
	}
	if backup.PersistenceGeneration != primary.PersistenceGeneration+1 {
		t.Fatalf("primary generation=%d backup generation=%d", primary.PersistenceGeneration, backup.PersistenceGeneration)
	}
	if string(primaryData) == string(backupData) {
		t.Fatal("fault injection did not leave the intended publication window")
	}

	restarted, err := LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("LoadStoreStrict: %v", err)
	}
	recovered := restarted.Get(beadID)
	if recovered == nil || recovered.DispatchState != DispatchSent || recovered.DispatchReceiptID != "delivery-88" {
		t.Fatalf("recovered assignment=%+v", recovered)
	}
	if restarted.PersistenceGeneration != backup.PersistenceGeneration {
		t.Fatalf("recovered generation=%d, want %d", restarted.PersistenceGeneration, backup.PersistenceGeneration)
	}
	promotedData, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatalf("read promoted primary: %v", err)
	}
	if string(promotedData) != string(backupData) {
		t.Fatal("strict recovery did not promote the exact durable backup bytes")
	}

	claimCalls := 0
	dispatchCalls := 0
	coordinator := NewAtomicCoordinator(
		restarted,
		ClaimFunc(func(context.Context, string, string) (ClaimReceipt, error) {
			claimCalls++
			return ClaimReceipt{}, errors.New("claim must not be replayed")
		}),
		nil,
		DispatchFunc(func(context.Context, DispatchRequest) (DispatchReceipt, error) {
			dispatchCalls++
			return DispatchReceipt{}, errors.New("dispatch must not be replayed")
		}),
	)
	result, err := coordinator.Execute(t.Context(), req)
	if err != nil {
		t.Fatalf("replay Execute: %v", err)
	}
	if !result.Sent || !result.Replayed || result.Dispatch.DeliveryID != "delivery-88" {
		t.Fatalf("replay result=%+v", result)
	}
	if claimCalls != 0 || dispatchCalls != 0 {
		t.Fatalf("replay crossed external boundaries: claims=%d dispatches=%d", claimCalls, dispatchCalls)
	}
}

func TestPersistenceRecoversBackupOnlyInitialPublication(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("backup-only-initial")
	store.Assignments["ntm-initial"] = &Assignment{BeadID: "ntm-initial", Status: StatusClaiming, AssignedAt: time.Now().UTC()}
	injected := errors.New("stop initial save after backup publication")
	store.afterBackupPublished = func(*AssignmentStore) error { return injected }
	if err := store.Save(); !errors.Is(err, injected) {
		t.Fatalf("Save error=%v, want injected failure", err)
	}
	if _, err := os.Stat(store.path); !os.IsNotExist(err) {
		t.Fatalf("initial primary stat error=%v, want missing file", err)
	}
	backup, backupData := mustReadAssignmentSnapshot(t, store.path+".bak", store.SessionName)
	if backup.PersistenceGeneration != 1 {
		t.Fatalf("initial backup generation=%d, want 1", backup.PersistenceGeneration)
	}

	restarted, err := LoadStoreStrict(store.SessionName)
	if err != nil {
		t.Fatalf("LoadStoreStrict: %v", err)
	}
	if restarted.Get("ntm-initial") == nil || restarted.PersistenceGeneration != 1 {
		t.Fatalf("restarted initial snapshot=%+v generation=%d", restarted.Get("ntm-initial"), restarted.PersistenceGeneration)
	}
	primaryData, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatalf("read promoted initial primary: %v", err)
	}
	if string(primaryData) != string(backupData) {
		t.Fatal("initial recovery did not promote the exact backup snapshot")
	}
}

func TestPersistenceLegacyV8PairLoadsAndUpgradesOnSave(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("legacy-v8-upgrade")
	legacy := &AssignmentStore{
		SessionName: store.SessionName,
		Assignments: map[string]*Assignment{
			"ntm-legacy": {BeadID: "ntm-legacy", Status: StatusAssigned, AssignedAt: time.Now().UTC()},
		},
		ClearedGenerations: map[string]uint64{},
		UpdatedAt:          time.Now().UTC(),
		Version:            assignmentStoreGenerationVersion - 1,
	}
	data, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatalf("marshal legacy snapshot: %v", err)
	}
	if err := os.WriteFile(store.path, data, 0600); err != nil {
		t.Fatalf("write legacy primary: %v", err)
	}
	if err := os.WriteFile(store.path+".bak", data, 0600); err != nil {
		t.Fatalf("write legacy backup: %v", err)
	}

	loaded, err := LoadStoreStrict(store.SessionName)
	if err != nil {
		t.Fatalf("LoadStoreStrict legacy v8: %v", err)
	}
	if loaded.PersistenceGeneration != 0 || loaded.Version != assignmentStoreVersion {
		t.Fatalf("loaded legacy generation=%d version=%d", loaded.PersistenceGeneration, loaded.Version)
	}
	if err := loaded.Save(); err != nil {
		t.Fatalf("upgrade Save: %v", err)
	}
	primary, primaryData := mustReadAssignmentSnapshot(t, store.path, store.SessionName)
	backup, backupData := mustReadAssignmentSnapshot(t, store.path+".bak", store.SessionName)
	if primary.Version != assignmentStoreVersion || primary.PersistenceGeneration != 1 {
		t.Fatalf("upgraded primary version=%d generation=%d", primary.Version, primary.PersistenceGeneration)
	}
	if !reflect.DeepEqual(primary, backup) || string(primaryData) != string(backupData) {
		t.Fatal("legacy upgrade did not publish one identical generation-bearing snapshot")
	}
}

func TestPersistenceStrictSelectionRejectsAmbiguousArtifacts(t *testing.T) {
	t.Run("equal generation divergent payload", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		store := NewStore("split-brain-generation")
		if _, err := store.Assign("ntm-split", "Primary", 1, "codex", "", ""); err != nil {
			t.Fatalf("Assign: %v", err)
		}
		backup, _ := mustReadAssignmentSnapshot(t, store.path+".bak", store.SessionName)
		backup.Assignments["ntm-split"].BeadTitle = "Divergent backup"
		data, err := json.MarshalIndent(backup, "", "  ")
		if err != nil {
			t.Fatalf("marshal divergent backup: %v", err)
		}
		if err := os.WriteFile(store.path+".bak", data, 0600); err != nil {
			t.Fatalf("write divergent backup: %v", err)
		}
		if _, err := LoadStoreStrict(store.SessionName); err == nil || !strings.Contains(err.Error(), "diverge") {
			t.Fatalf("strict split-brain error=%v", err)
		}
	})

	t.Run("equal generation unknown field divergence", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		store := NewStore("split-brain-unknown-field")
		if _, err := store.Assign("ntm-split-unknown", "Primary", 1, "codex", "", ""); err != nil {
			t.Fatalf("Assign: %v", err)
		}
		_, backupData := mustReadAssignmentSnapshot(t, store.path+".bak", store.SessionName)
		var payload map[string]any
		if err := json.Unmarshal(backupData, &payload); err != nil {
			t.Fatalf("decode backup payload: %v", err)
		}
		payload["unknown_extension"] = map[string]any{"value": "must not be ignored"}
		divergent, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			t.Fatalf("marshal unknown-field backup: %v", err)
		}
		if err := os.WriteFile(store.path+".bak", divergent, 0600); err != nil {
			t.Fatalf("write unknown-field backup: %v", err)
		}
		if _, err := LoadStoreStrict(store.SessionName); err == nil || !strings.Contains(err.Error(), "diverge") {
			t.Fatalf("strict unknown-field split-brain error=%v", err)
		}
	})

	t.Run("backup generation jump", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		store := NewStore("backup-generation-jump")
		if _, err := store.Assign("ntm-jump", "Jump", 1, "codex", "", ""); err != nil {
			t.Fatalf("Assign: %v", err)
		}
		backup, _ := mustReadAssignmentSnapshot(t, store.path+".bak", store.SessionName)
		backup.PersistenceGeneration += 2
		data, err := json.MarshalIndent(backup, "", "  ")
		if err != nil {
			t.Fatalf("marshal jumped backup: %v", err)
		}
		if err := os.WriteFile(store.path+".bak", data, 0600); err != nil {
			t.Fatalf("write jumped backup: %v", err)
		}
		if _, err := LoadStoreStrict(store.SessionName); err == nil || !strings.Contains(err.Error(), "immediate successor") {
			t.Fatalf("strict generation-jump error=%v", err)
		}
	})

	t.Run("malformed backup", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		store := NewStore("malformed-backup")
		if _, err := store.Assign("ntm-malformed", "Malformed", 1, "codex", "", ""); err != nil {
			t.Fatalf("Assign: %v", err)
		}
		if err := os.WriteFile(store.path+".bak", []byte("{not-json"), 0600); err != nil {
			t.Fatalf("write malformed backup: %v", err)
		}
		if _, err := LoadStoreStrict(store.SessionName); err == nil || !strings.Contains(err.Error(), "invalid backup") {
			t.Fatalf("strict malformed-backup error=%v", err)
		}
	})

	for _, artifact := range []string{"primary", "backup"} {
		artifact := artifact
		t.Run("future schema "+artifact, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			store := NewStore("future-schema-" + artifact)
			if _, err := store.Assign("ntm-future", "Future", 1, "codex", "", ""); err != nil {
				t.Fatalf("Assign: %v", err)
			}
			path := store.path
			if artifact == "backup" {
				path += ".bak"
			}
			future, _ := mustReadAssignmentSnapshot(t, path, store.SessionName)
			future.Version = assignmentStoreVersion + 1
			data, err := json.MarshalIndent(future, "", "  ")
			if err != nil {
				t.Fatalf("marshal future %s: %v", artifact, err)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatalf("write future %s: %v", artifact, err)
			}
			if _, err := LoadStoreStrict(store.SessionName); err == nil || !strings.Contains(err.Error(), "newer than supported") {
				t.Fatalf("strict future-%s error=%v", artifact, err)
			}
		})
	}
}

func TestPersistenceStrictV9RejectsMalformedCompletionOutboxArtifacts(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*Assignment)
		wantError string
	}{
		{
			name: "pending event without detection timestamp",
			mutate: func(current *Assignment) {
				current.CompletionDetectedAt = nil
			},
			wantError: "must appear together",
		},
		{
			name: "detection timestamp without pending event",
			mutate: func(current *Assignment) {
				current.PendingCompletionEventID = ""
			},
			wantError: "must appear together",
		},
		{
			name: "blank pending event",
			mutate: func(current *Assignment) {
				current.PendingCompletionEventID = "   "
			},
			wantError: "event ID is blank",
		},
		{
			name: "pending event on nonterminal assignment",
			mutate: func(current *Assignment) {
				current.Status = StatusAssigned
			},
			wantError: "requires a terminal assignment outcome",
		},
		{
			name: "zero detection timestamp",
			mutate: func(current *Assignment) {
				zero := time.Time{}
				current.CompletionDetectedAt = &zero
			},
			wantError: "detection timestamp is invalid",
		},
		{
			name: "non UTC detection timestamp",
			mutate: func(current *Assignment) {
				nonUTC := current.CompletionDetectedAt.In(time.FixedZone("completion-test", 3600))
				current.CompletionDetectedAt = &nonUTC
			},
			wantError: "detection timestamp must be UTC",
		},
		{
			name: "consumer token without lease expiry",
			mutate: func(current *Assignment) {
				current.CompletionConsumerToken = "consumer-a"
			},
			wantError: "must appear together",
		},
		{
			name: "lease expiry without consumer token",
			mutate: func(current *Assignment) {
				expiresAt := time.Now().UTC().Add(time.Minute)
				current.CompletionLeaseExpiresAt = &expiresAt
			},
			wantError: "must appear together",
		},
		{
			name: "blank consumer token",
			mutate: func(current *Assignment) {
				current.CompletionConsumerToken = "   "
				expiresAt := time.Now().UTC().Add(time.Minute)
				current.CompletionLeaseExpiresAt = &expiresAt
			},
			wantError: "consumer token is blank",
		},
		{
			name: "lease without pending event",
			mutate: func(current *Assignment) {
				current.PendingCompletionEventID = ""
				current.CompletionDetectedAt = nil
				current.CompletionConsumerToken = "consumer-a"
				expiresAt := time.Now().UTC().Add(time.Minute)
				current.CompletionLeaseExpiresAt = &expiresAt
			},
			wantError: "requires a pending completion event",
		},
		{
			name: "lease on terminal reconciliation barrier",
			mutate: func(current *Assignment) {
				current.Status = StatusAssigned
				current.ClearState = ClearStateReservationReleasing
				current.PendingTerminalStatus = StatusCompleted
				current.CompletionConsumerToken = "consumer-a"
				expiresAt := time.Now().UTC().Add(time.Minute)
				current.CompletionLeaseExpiresAt = &expiresAt
			},
			wantError: "requires a terminal assignment status",
		},
		{
			name: "lease before terminal reconciliation finishes",
			mutate: func(current *Assignment) {
				current.ClearState = ClearStateLeasesReleased
				current.CompletionConsumerToken = "consumer-a"
				expiresAt := time.Now().UTC().Add(time.Minute)
				current.CompletionLeaseExpiresAt = &expiresAt
			},
			wantError: "requires completed terminal reconciliation",
		},
		{
			name: "zero lease expiry",
			mutate: func(current *Assignment) {
				current.CompletionConsumerToken = "consumer-a"
				zero := time.Time{}
				current.CompletionLeaseExpiresAt = &zero
			},
			wantError: "lease expiry timestamp is invalid",
		},
		{
			name: "non UTC lease expiry",
			mutate: func(current *Assignment) {
				current.CompletionConsumerToken = "consumer-a"
				expiresAt := time.Now().UTC().Add(time.Minute).In(time.FixedZone("completion-test", 3600))
				current.CompletionLeaseExpiresAt = &expiresAt
			},
			wantError: "lease expiry timestamp must be UTC",
		},
	}

	for _, artifact := range []string{"primary", "backup"} {
		artifact := artifact
		for _, test := range tests {
			test := test
			t.Run(artifact+"/"+test.name, func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				store := NewStore("completion-outbox-validation-" + artifact + "-" + strings.ReplaceAll(test.name, " ", "-"))
				if _, err := store.Assign("ntm-completion-validation", "Completion validation", 1, "codex", "", ""); err != nil {
					t.Fatalf("Assign: %v", err)
				}
				path := store.path
				if artifact == "backup" {
					path += ".bak"
				}
				snapshot, _ := mustReadAssignmentSnapshot(t, path, store.SessionName)
				current := snapshot.Assignments["ntm-completion-validation"]
				detectedAt := time.Now().UTC()
				current.Status = StatusCompleted
				current.PendingCompletionEventID = "completion-event-valid"
				current.CompletionDetectedAt = &detectedAt
				test.mutate(current)
				data, err := json.MarshalIndent(snapshot, "", "  ")
				if err != nil {
					t.Fatalf("marshal malformed %s completion snapshot: %v", artifact, err)
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatalf("write malformed %s completion snapshot: %v", artifact, err)
				}
				_, err = LoadStoreStrict(store.SessionName)
				if err == nil || !strings.Contains(err.Error(), "invalid "+artifact+" ledger") || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("strict malformed-%s error=%v, want %q", artifact, err, test.wantError)
				}
			})
		}
	}
}

func TestPersistenceLegacyVersionCannotBypassCompletionOutboxValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("legacy-completion-outbox-validation")
	detectedAt := time.Now().UTC()
	legacy := &AssignmentStore{
		SessionName: store.SessionName,
		Assignments: map[string]*Assignment{
			"ntm-legacy-completion": {
				BeadID: "ntm-legacy-completion", Status: StatusCompleted, AssignedAt: detectedAt,
				PendingCompletionEventID: "legacy-event", CompletionDetectedAt: &detectedAt,
				CompletionConsumerToken: "unpaired-legacy-consumer",
			},
		},
		ClearedGenerations: map[string]uint64{},
		UpdatedAt:          detectedAt,
		Version:            assignmentStoreGenerationVersion - 1,
	}
	data, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatalf("marshal legacy completion snapshot: %v", err)
	}
	if err := os.WriteFile(store.path, data, 0600); err != nil {
		t.Fatalf("write legacy completion primary: %v", err)
	}
	if err := os.WriteFile(store.path+".bak", data, 0600); err != nil {
		t.Fatalf("write legacy completion backup: %v", err)
	}
	if _, err := LoadStoreStrict(store.SessionName); err == nil ||
		!strings.Contains(err.Error(), "invalid primary ledger") ||
		!strings.Contains(err.Error(), "consumer token and lease expiry must appear together") {
		t.Fatalf("legacy completion validation error=%v", err)
	}
}

func TestPersistenceSelectsNewerPrimaryOverStaleBackup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("newer-primary")
	if _, err := store.Assign("ntm-newer-primary", "Newer primary", 1, "codex", "", ""); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	firstGeneration, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatalf("read first generation: %v", err)
	}
	if err := store.MarkWorking("ntm-newer-primary"); err != nil {
		t.Fatalf("MarkWorking: %v", err)
	}
	if err := os.WriteFile(store.path+".bak", firstGeneration, 0600); err != nil {
		t.Fatalf("restore stale backup: %v", err)
	}

	loaded, err := LoadStoreStrict(store.SessionName)
	if err != nil {
		t.Fatalf("LoadStoreStrict: %v", err)
	}
	if loaded.PersistenceGeneration != 2 || loaded.Get("ntm-newer-primary").Status != StatusWorking {
		t.Fatalf("selected generation=%d assignment=%+v", loaded.PersistenceGeneration, loaded.Get("ntm-newer-primary"))
	}
}

func TestPersistenceNonStrictLoadUsesNewerBackupGeneration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("nonstrict-newer-backup")
	if _, err := store.Assign("ntm-nonstrict", "Before", 1, "codex", "", ""); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	store.mutex.Lock()
	store.Assignments["ntm-nonstrict"].BeadTitle = "After"
	store.mutex.Unlock()
	injected := errors.New("stop after newer backup")
	store.afterBackupPublished = func(*AssignmentStore) error { return injected }
	if err := store.Save(); !errors.Is(err, injected) {
		t.Fatalf("Save error=%v, want injected failure", err)
	}
	_, backupData := mustReadAssignmentSnapshot(t, store.path+".bak", store.SessionName)

	loaded, err := LoadStore(store.SessionName)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if loaded.PersistenceGeneration != 2 || loaded.Get("ntm-nonstrict").BeadTitle != "After" {
		t.Fatalf("nonstrict selected generation=%d assignment=%+v", loaded.PersistenceGeneration, loaded.Get("ntm-nonstrict"))
	}
	primaryData, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatalf("read promoted primary: %v", err)
	}
	if string(primaryData) != string(backupData) {
		t.Fatal("nonstrict load did not promote the selected newer backup")
	}
}

func mustReadAssignmentSnapshot(t *testing.T, path, session string) (*AssignmentStore, []byte) {
	t.Helper()
	candidate := readAssignmentSnapshotCandidate(path, session)
	if !candidate.exists || candidate.err != nil || candidate.snapshot == nil {
		t.Fatalf("read assignment snapshot %s: exists=%v error=%v", path, candidate.exists, candidate.err)
	}
	return candidate.snapshot, candidate.data
}

func TestLoadStoreStrictReadOnlyDoesNotCreateOrRepairDurableState(t *testing.T) {
	t.Run("absent ledger does not create session directory or lock", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		const session = "strict-read-only-absent"
		sessionDir := filepath.Join(StorageDir(), session)

		loaded, err := LoadStoreStrictReadOnly(session)
		if err != nil {
			t.Fatalf("LoadStoreStrictReadOnly: %v", err)
		}
		if loaded.SessionName != session || len(loaded.Assignments) != 0 {
			t.Fatalf("read-only empty store=%+v", loaded)
		}
		if _, err := os.Stat(sessionDir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read-only load created session directory %s: %v", sessionDir, err)
		}
	})

	t.Run("newer backup is selected without lock or primary promotion", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		const (
			session = "strict-read-only-newer-backup"
			beadID  = "ntm-read-only-conflict"
		)
		sessionDir := filepath.Join(StorageDir(), session)
		if err := os.MkdirAll(sessionDir, 0o700); err != nil {
			t.Fatalf("create ledger fixture directory: %v", err)
		}
		ledgerPath := filepath.Join(sessionDir, assignmentsDirName+fileExtension)
		now := time.Now().UTC()
		primaryAssignment := &Assignment{
			BeadID: beadID, BeadTitle: "primary", Pane: 1, AgentType: "codex", AgentName: "CodexOne",
			Status: StatusAssigned, AssignedAt: now, DispatchTarget: "%7", OccupancyKey: "%7",
		}
		backupAssignment := cloneAssignment(primaryAssignment)
		backupAssignment.BeadTitle = "newer backup"
		primary := &AssignmentStore{
			SessionName: session, Assignments: map[string]*Assignment{beadID: primaryAssignment},
			ClearedGenerations: map[string]uint64{}, PersistenceGeneration: 1, UpdatedAt: now, Version: assignmentStoreVersion,
		}
		backup := &AssignmentStore{
			SessionName: session, Assignments: map[string]*Assignment{beadID: backupAssignment},
			ClearedGenerations: map[string]uint64{}, PersistenceGeneration: 2, UpdatedAt: now.Add(time.Second), Version: assignmentStoreVersion,
		}
		primaryData, err := json.MarshalIndent(primary, "", "  ")
		if err != nil {
			t.Fatalf("marshal primary fixture: %v", err)
		}
		backupData, err := json.MarshalIndent(backup, "", "  ")
		if err != nil {
			t.Fatalf("marshal backup fixture: %v", err)
		}
		if err := os.WriteFile(ledgerPath, primaryData, 0o600); err != nil {
			t.Fatalf("write primary fixture: %v", err)
		}
		if err := os.WriteFile(ledgerPath+".bak", backupData, 0o600); err != nil {
			t.Fatalf("write backup fixture: %v", err)
		}
		lockPath := ledgerPath + ".lock"
		if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("fixture unexpectedly has lock file %s: %v", lockPath, err)
		}

		loaded, err := LoadStoreStrictReadOnly(session)
		if err != nil {
			t.Fatalf("LoadStoreStrictReadOnly: %v", err)
		}
		if loaded.PersistenceGeneration != 2 || loaded.Get(beadID) == nil || loaded.Get(beadID).BeadTitle != "newer backup" ||
			loaded.Get(beadID).OccupancyKey != "%7" {
			t.Fatalf("selected read-only snapshot generation=%d assignment=%+v", loaded.PersistenceGeneration, loaded.Get(beadID))
		}
		afterPrimary, err := os.ReadFile(ledgerPath)
		if err != nil {
			t.Fatalf("read primary after read-only load: %v", err)
		}
		afterBackup, err := os.ReadFile(ledgerPath + ".bak")
		if err != nil {
			t.Fatalf("read backup after read-only load: %v", err)
		}
		if string(afterPrimary) != string(primaryData) || string(afterBackup) != string(backupData) {
			t.Fatal("read-only strict load promoted or rewrote a durable ledger replica")
		}
		if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read-only strict load created lock file %s: %v", lockPath, err)
		}
	})
}

func TestLoadStoreStrictRejectsCorruptLedgerWithoutBackup(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	store := NewStore("strict-corrupt")
	if err := os.WriteFile(store.path, []byte("{not-json"), 0644); err != nil {
		t.Fatalf("write corrupt ledger: %v", err)
	}

	if _, err := LoadStoreStrict("strict-corrupt"); err == nil {
		t.Fatal("expected strict load to reject corrupt primary without backup")
	}
}

func TestLoadStoreStrictNeverRollsBackToBackup(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	store := NewStore("strict-stale-backup")

	stale := &AssignmentStore{
		SessionName: "strict-stale-backup",
		Assignments: map[string]*Assignment{
			"bd-ambiguous": {
				BeadID:         "bd-ambiguous",
				Status:         StatusClaimed,
				DispatchState:  DispatchPending,
				IdempotencyKey: "attempt-1",
			},
		},
		Version: assignmentStoreGenerationVersion - 1,
	}
	backup, err := json.Marshal(stale)
	if err != nil {
		t.Fatalf("marshal stale backup: %v", err)
	}
	if err := os.WriteFile(store.path+".bak", backup, 0644); err != nil {
		t.Fatalf("write stale backup: %v", err)
	}
	if err := os.WriteFile(store.path, []byte("{not-json"), 0644); err != nil {
		t.Fatalf("write corrupt primary: %v", err)
	}

	if _, err := LoadStoreStrict("strict-stale-backup"); err == nil {
		t.Fatal("expected strict load to reject corrupt primary instead of restoring retryable backup")
	}
}

func TestLoadStoreStrictRejectsOrphanedBackup(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	store := NewStore("strict-orphaned-backup")
	if err := os.WriteFile(store.path+".bak", []byte(`{"session_name":"strict-orphaned-backup","assignments":{}}`), 0644); err != nil {
		t.Fatalf("write orphaned backup: %v", err)
	}

	if _, err := LoadStoreStrict("strict-orphaned-backup"); err == nil {
		t.Fatal("expected strict load to reject a backup with no primary ledger")
	}
}

func TestLoadStoreStrictRejectsLaterGenerationOrphanedBackup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("strict-later-orphaned-backup")
	orphan := &AssignmentStore{
		SessionName:           store.SessionName,
		Assignments:           map[string]*Assignment{},
		ClearedGenerations:    map[string]uint64{},
		PersistenceGeneration: 2,
		UpdatedAt:             time.Now().UTC(),
		Version:               assignmentStoreVersion,
	}
	data, err := json.MarshalIndent(orphan, "", "  ")
	if err != nil {
		t.Fatalf("marshal later-generation orphan: %v", err)
	}
	if err := os.WriteFile(store.path+".bak", data, 0600); err != nil {
		t.Fatalf("write later-generation orphan: %v", err)
	}
	if _, err := LoadStoreStrict(store.SessionName); err == nil || !strings.Contains(err.Error(), "want initial generation 1") {
		t.Fatalf("later-generation orphan error=%v", err)
	}
}

func TestStaleLifecycleWriterPreservesAtomicDispatchBarrierAndReceipt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const (
		session = "same-bead-field-merge"
		beadID  = "ntm-same-bead"
		key     = "same-bead-attempt"
	)

	seed := NewStore(session)
	req := AtomicRequest{
		BeadID:         beadID,
		BeadTitle:      "Same bead merge",
		Target:         "%17",
		Pane:           2,
		AgentType:      "codex",
		AgentName:      "CodexOne",
		Actor:          "CodexOne",
		Prompt:         "continue",
		IdempotencyKey: key,
	}
	claimedAt := time.Now().UTC().Add(-time.Minute)
	if _, err := seed.RecordAtomicIntent(req, StableClaimActor(req.Actor, key), claimedAt); err != nil {
		t.Fatalf("RecordAtomicIntent: %v", err)
	}
	if _, err := seed.RecordAtomicClaim(req, ClaimReceipt{
		BeadID: beadID, Actor: StableClaimActor(req.Actor, key), Status: "in_progress", ClaimedAt: claimedAt,
	}); err != nil {
		t.Fatalf("RecordAtomicClaim: %v", err)
	}
	seed.Assignments[beadID].Status = StatusAssigned
	if err := seed.Save(); err != nil {
		t.Fatalf("persist assigned seed: %v", err)
	}

	staleLifecycle, err := LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load stale lifecycle writer: %v", err)
	}
	dispatchWriter, err := LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load dispatch writer: %v", err)
	}
	startedAt := time.Now().UTC()
	if err := dispatchWriter.RecordAtomicDispatchStarted(beadID, key, startedAt); err != nil {
		t.Fatalf("RecordAtomicDispatchStarted: %v", err)
	}
	if err := staleLifecycle.MarkCompleted(beadID); err != nil {
		t.Fatalf("stale MarkCompleted: %v", err)
	}

	afterCompletion, err := LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("reload after stale completion: %v", err)
	}
	barrier := afterCompletion.Get(beadID)
	if barrier == nil || barrier.Status != StatusCompleted || barrier.DispatchState != DispatchSending ||
		barrier.DispatchAttempts != 1 || barrier.DispatchStartedAt == nil || !barrier.DispatchStartedAt.Equal(startedAt) {
		t.Fatalf("stale lifecycle merge erased sending barrier: %+v", barrier)
	}

	dispatchedAt := startedAt.Add(time.Second)
	if err := dispatchWriter.RecordAtomicDispatchSent(beadID, key, req.Prompt, DispatchReceipt{
		DeliveryID: "delivery-17", Duration: 25 * time.Millisecond,
	}, dispatchedAt); err != nil {
		t.Fatalf("RecordAtomicDispatchSent: %v", err)
	}
	finalStore, err := LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("reload final store: %v", err)
	}
	final := finalStore.Get(beadID)
	if final == nil || final.Status != StatusCompleted || final.DispatchState != DispatchSent ||
		final.DispatchAttempts != 1 || final.DispatchReceiptID != "delivery-17" ||
		final.DispatchedAt == nil || !final.DispatchedAt.Equal(dispatchedAt) {
		t.Fatalf("dispatch receipt/lifecycle merge = %+v", final)
	}
}

func TestSaveMergeRejectsCorruptPrimaryInsteadOfUsingBackup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("strict-save-merge")
	if _, err := store.Assign("ntm-corrupt", "Corrupt", 1, "codex", "CodexOne", "prompt"); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	if err := os.WriteFile(store.path, []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("corrupt primary: %v", err)
	}
	if err := store.Save(); err == nil {
		t.Fatal("Save succeeded by rolling back to a stale backup")
	}
}

func TestReassignExplicitlyReplacesAtomicRecord(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("reassign-atomic-record")
	req := AtomicRequest{
		BeadID: "ntm-reassign", BeadTitle: "Reassign", Target: "%31", Pane: 1,
		AgentType: "codex", AgentName: "CodexOne", Actor: "CodexOne",
		Prompt: "work", IdempotencyKey: "old-atomic-key",
	}
	now := time.Now().UTC()
	if _, err := store.RecordAtomicIntent(req, StableClaimActor(req.Actor, req.IdempotencyKey), now); err != nil {
		t.Fatalf("RecordAtomicIntent: %v", err)
	}
	if _, err := store.RecordAtomicClaim(req, ClaimReceipt{
		BeadID: req.BeadID, Actor: StableClaimActor(req.Actor, req.IdempotencyKey), Status: "in_progress", ClaimedAt: now,
	}); err != nil {
		t.Fatalf("RecordAtomicClaim: %v", err)
	}
	if err := store.RecordAtomicDispatchStarted(req.BeadID, req.IdempotencyKey, now); err != nil {
		t.Fatalf("RecordAtomicDispatchStarted: %v", err)
	}
	if err := store.RecordAtomicDispatchSent(req.BeadID, req.IdempotencyKey, req.Prompt, DispatchReceipt{DeliveryID: "old-receipt"}, now); err != nil {
		t.Fatalf("RecordAtomicDispatchSent: %v", err)
	}
	if err := store.MarkWorking(req.BeadID); err != nil {
		t.Fatalf("MarkWorking: %v", err)
	}
	if _, err := store.Reassign(req.BeadID, ReassignmentTarget{Pane: 4, AgentType: "claude", AgentName: "ClaudeFour"}); err != nil {
		t.Fatalf("Reassign: %v", err)
	}

	reloaded, err := LoadStoreStrict("reassign-atomic-record")
	if err != nil {
		t.Fatalf("LoadStoreStrict: %v", err)
	}
	got := reloaded.Get(req.BeadID)
	if got == nil || got.Pane != 4 || got.AgentType != "claude" || got.AgentName != "ClaudeFour" || got.Status != StatusAssigned {
		t.Fatalf("reassigned record = %+v", got)
	}
	if got.IdempotencyKey != "" || got.DispatchState != "" || got.DispatchTarget != "" ||
		got.DispatchReceiptID != "" || got.DispatchAttempts != 0 || got.PromptSent != "" {
		t.Fatalf("reassign retained stale atomic metadata: %+v", got)
	}
}

func TestDestructiveStoreMutationsRejectStaleAtomicBarrier(t *testing.T) {
	operations := []struct {
		name string
		run  func(*AssignmentStore, string) error
	}{
		{name: "remove", run: func(store *AssignmentStore, beadID string) error {
			return store.Remove(beadID)
		}},
		{name: "clear", run: func(store *AssignmentStore, _ string) error {
			return store.Clear()
		}},
		{name: "assign", run: func(store *AssignmentStore, beadID string) error {
			_, err := store.Assign(beadID, "replacement", 9, "claude", "ClaudeNine", "replacement prompt")
			return err
		}},
		{name: "reassign", run: func(store *AssignmentStore, beadID string) error {
			_, err := store.Reassign(beadID, ReassignmentTarget{Pane: 9, AgentType: "claude", AgentName: "ClaudeNine"})
			return err
		}},
	}

	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			session := "stale-destructive-" + operation.name
			beadID := "ntm-stale-" + operation.name
			key := "stale-key-" + operation.name
			seed := NewStore(session)
			req := AtomicRequest{
				BeadID: beadID, BeadTitle: "Stale mutation", Target: "%17", OccupancyKey: "%17", Pane: 2,
				AgentType: "codex", AgentName: "CodexTwo", Actor: "CodexTwo", Prompt: "continue", IdempotencyKey: key,
			}
			now := time.Now().UTC()
			actor := StableClaimActor(req.Actor, key)
			if _, err := seed.RecordAtomicIntent(req, actor, now); err != nil {
				t.Fatalf("RecordAtomicIntent: %v", err)
			}
			if _, err := seed.RecordAtomicClaim(req, ClaimReceipt{BeadID: beadID, Actor: actor, Status: "in_progress", ClaimedAt: now}); err != nil {
				t.Fatalf("RecordAtomicClaim: %v", err)
			}
			seed.Assignments[beadID].Status = StatusWorking
			if err := seed.Save(); err != nil {
				t.Fatalf("persist working seed: %v", err)
			}

			stale, err := LoadStoreStrict(session)
			if err != nil {
				t.Fatalf("load stale writer: %v", err)
			}
			barrierWriter, err := LoadStoreStrict(session)
			if err != nil {
				t.Fatalf("load barrier writer: %v", err)
			}
			startedAt := now.Add(time.Second)
			if err := barrierWriter.RecordAtomicDispatchStarted(beadID, key, startedAt); err != nil {
				t.Fatalf("RecordAtomicDispatchStarted: %v", err)
			}

			mutationErr := operation.run(stale, beadID)
			var conflict *ConcurrentMutationError
			if !errors.As(mutationErr, &conflict) || conflict.BeadID != beadID {
				t.Fatalf("stale %s error=%v, want ConcurrentMutationError for %s", operation.name, mutationErr, beadID)
			}
			for label, stored := range map[string]*Assignment{
				"stale writer": stale.Get(beadID),
				"durable":      mustLoadAssignment(t, session, beadID),
			} {
				if stored == nil || stored.DispatchState != DispatchSending || stored.DispatchAttempts != 1 ||
					stored.DispatchStartedAt == nil || !stored.DispatchStartedAt.Equal(startedAt) || stored.IdempotencyKey != key {
					t.Fatalf("%s after stale %s = %+v", label, operation.name, stored)
				}
			}
		})
	}
}

func TestStaleLifecycleUpdateCannotResurrectRemovedAssignment(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "stale-update-after-remove"
	const beadID = "ntm-removed-generation"

	seed := NewStore(session)
	if _, err := seed.Assign(beadID, "Removed assignment", 2, "codex", "CodexTwo", "work"); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	if err := seed.MarkWorking(beadID); err != nil {
		t.Fatalf("MarkWorking: %v", err)
	}
	stale, err := LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load stale writer: %v", err)
	}
	remover, err := LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load remover: %v", err)
	}
	if err := remover.Remove(beadID); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	updateErr := stale.MarkCompleted(beadID)
	var conflict *ConcurrentMutationError
	if !errors.As(updateErr, &conflict) || conflict.BeadID != beadID {
		t.Fatalf("stale MarkCompleted error=%v, want ConcurrentMutationError", updateErr)
	}
	if got := stale.Get(beadID); got != nil {
		t.Fatalf("losing store resurrected removed assignment in memory: %+v", got)
	}
	if got := mustLoadAssignment(t, session, beadID); got != nil {
		t.Fatalf("stale lifecycle update resurrected durable assignment: %+v", got)
	}
}

func TestStaleLifecycleUpdateCannotCrossAssignmentGeneration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "stale-update-cross-generation"
	const beadID = "ntm-cross-generation"
	now := time.Now().UTC()
	oldRequest := AtomicRequest{
		BeadID: beadID, BeadTitle: "Old generation", Target: "%41", OccupancyKey: "%41", Pane: 1,
		AgentType: "codex", AgentName: "CodexOne", Actor: "CodexOne", Prompt: "old work", IdempotencyKey: "old-generation-key",
	}
	seed := NewStore(session)
	oldActor := StableClaimActor(oldRequest.Actor, oldRequest.IdempotencyKey)
	if _, err := seed.RecordAtomicIntent(oldRequest, oldActor, now); err != nil {
		t.Fatalf("RecordAtomicIntent(old): %v", err)
	}
	if _, err := seed.RecordAtomicClaim(oldRequest, ClaimReceipt{BeadID: beadID, Actor: oldActor, Status: "in_progress", ClaimedAt: now}); err != nil {
		t.Fatalf("RecordAtomicClaim(old): %v", err)
	}
	if err := seed.UpdateStatus(beadID, StatusAssigned); err != nil {
		t.Fatalf("mark old assigned: %v", err)
	}
	if err := seed.MarkWorking(beadID); err != nil {
		t.Fatalf("mark old working: %v", err)
	}
	stale, err := LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load old-generation writer: %v", err)
	}
	winner, err := LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load winner: %v", err)
	}
	if err := winner.MarkCompleted(beadID); err != nil {
		t.Fatalf("complete old generation: %v", err)
	}
	newRequest := oldRequest
	newRequest.BeadTitle = "New generation"
	newRequest.Prompt = "new work"
	newRequest.IdempotencyKey = "new-generation-key"
	newActor := StableClaimActor(newRequest.Actor, newRequest.IdempotencyKey)
	if _, err := winner.RecordAtomicIntent(newRequest, newActor, now.Add(time.Minute)); err != nil {
		t.Fatalf("RecordAtomicIntent(new): %v", err)
	}

	updateErr := stale.MarkCompleted(beadID)
	var conflict *ConcurrentMutationError
	if !errors.As(updateErr, &conflict) || conflict.BeadID != beadID {
		t.Fatalf("old-generation MarkCompleted error=%v, want ConcurrentMutationError", updateErr)
	}
	for label, stored := range map[string]*Assignment{
		"losing store": stale.Get(beadID),
		"durable":      mustLoadAssignment(t, session, beadID),
	} {
		if stored == nil || stored.IdempotencyKey != newRequest.IdempotencyKey || stored.Status != StatusClaiming || stored.PendingPrompt != newRequest.Prompt {
			t.Fatalf("%s after generation conflict = %+v", label, stored)
		}
	}
}

func TestRecordAtomicIntentConflictKeepsDurableWinnerInMemory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "atomic-intent-concurrent-generation"
	const beadID = "ntm-intent-winner"
	now := time.Now().UTC()
	seed := NewStore(session)
	old := AtomicRequest{
		BeadID: beadID, BeadTitle: "Terminal", Target: "%51", OccupancyKey: "%51", Pane: 1,
		AgentType: "codex", AgentName: "CodexOne", Actor: "CodexOne", Prompt: "old", IdempotencyKey: "terminal-key",
	}
	if _, err := seed.RecordAtomicIntent(old, StableClaimActor(old.Actor, old.IdempotencyKey), now); err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	seed.Assignments[beadID].Status = StatusCompleted
	if err := seed.Save(); err != nil {
		t.Fatalf("persist terminal seed: %v", err)
	}
	winner, err := LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load winner: %v", err)
	}
	loser, err := LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load loser: %v", err)
	}
	winnerRequest := old
	winnerRequest.Prompt = "winner"
	winnerRequest.IdempotencyKey = "winner-key"
	if _, err := winner.RecordAtomicIntent(winnerRequest, StableClaimActor(winnerRequest.Actor, winnerRequest.IdempotencyKey), now.Add(time.Minute)); err != nil {
		t.Fatalf("winner RecordAtomicIntent: %v", err)
	}
	loserRequest := old
	loserRequest.Prompt = "loser"
	loserRequest.IdempotencyKey = "loser-key"
	_, loserErr := loser.RecordAtomicIntent(loserRequest, StableClaimActor(loserRequest.Actor, loserRequest.IdempotencyKey), now.Add(2*time.Minute))
	var conflict *ConcurrentMutationError
	if !errors.As(loserErr, &conflict) || conflict.BeadID != beadID {
		t.Fatalf("loser RecordAtomicIntent error=%v, want ConcurrentMutationError", loserErr)
	}
	for label, stored := range map[string]*Assignment{
		"loser memory": loser.Get(beadID),
		"durable":      mustLoadAssignment(t, session, beadID),
	} {
		if stored == nil || stored.IdempotencyKey != winnerRequest.IdempotencyKey || stored.PendingPrompt != winnerRequest.Prompt {
			t.Fatalf("%s did not retain durable winner: %+v", label, stored)
		}
	}
}

func TestRecordAtomicIntentCannotReplaceTerminalLeaseHandles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "atomic-intent-terminal-lease"
	const beadID = "ntm-terminal-lease"
	now := time.Now().UTC()
	store := NewStore(session)
	store.Assignments[beadID] = &Assignment{
		BeadID: beadID, BeadTitle: "Terminal with lease", Pane: 4,
		AgentType: "codex", AgentName: "CodexFour", Status: StatusCompleted, AssignedAt: now,
		IdempotencyKey: "old-generation", ClaimActor: "old-actor",
		DispatchTarget: "%54", OccupancyKey: "%54", DispatchState: DispatchSent,
		DispatchReceiptID: "old-receipt", ReservationState: ReservationReserved,
		ReservationCompleted: true, ReservedPaths: []string{"internal/assignment/**"}, ReservationIDs: []int{541},
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed terminal lease: %v", err)
	}
	request := AtomicRequest{
		BeadID: beadID, BeadTitle: "Replacement", Target: "%54", OccupancyKey: "%54", Pane: 4,
		AgentType: "codex", AgentName: "CodexFour", Actor: "CodexFour", Prompt: "new work", IdempotencyKey: "new-generation",
	}
	_, err := store.RecordAtomicIntent(request, StableClaimActor(request.Actor, request.IdempotencyKey), now.Add(time.Minute))
	if !errors.Is(err, ErrReservationReleaseRequired) {
		t.Fatalf("RecordAtomicIntent error=%v, want ErrReservationReleaseRequired", err)
	}
	stored := mustLoadAssignment(t, session, beadID)
	if stored == nil || stored.IdempotencyKey != "old-generation" || len(stored.ReservationIDs) != 1 || stored.DispatchReceiptID != "old-receipt" {
		t.Fatalf("refused replacement lost terminal lease: %+v", stored)
	}
}

func TestAssignmentClearBarrierRetainsLeaseAndBlocksNewWork(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "assignment-clear-barrier"
	const beadID = "ntm-clear-barrier"
	now := time.Now().UTC()
	store := NewStore(session)
	store.Assignments[beadID] = &Assignment{
		BeadID: beadID, BeadTitle: "Clear barrier", Pane: 3,
		AgentType: "codex", AgentName: "CodexThree", Status: StatusAssigned, AssignedAt: now,
		IdempotencyKey: "clear-generation", ClaimActor: "clear-actor",
		DispatchTarget: "%63", OccupancyKey: "%63", DispatchState: DispatchSent,
		DispatchReceiptID: "clear-receipt", ReservationCompleted: true,
		ReservationAgent: "CodexThree", ReservationTarget: "%63",
		ReservedPaths: []string{"internal/assignment/**"}, ReservationIDs: []int{631, 632},
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed clear assignment: %v", err)
	}
	clearing, err := store.BeginClear(t.Context(), beadID, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("BeginClear: %v", err)
	}
	if clearing.ClearState != ClearStateReservationReleasing || len(clearing.ReservationIDs) != 2 || clearing.DispatchReceiptID != "clear-receipt" {
		t.Fatalf("clear barrier lost durable lease or receipt: %+v", clearing)
	}

	claimer := &atomicClaimLedger{}
	dispatcher := &atomicDispatchRecorder{}
	request := atomicTestRequest("replacement-generation", "%63")
	request.BeadID = beadID
	result, assignErr := NewAtomicCoordinator(store, claimer, nil, dispatcher).Execute(t.Context(), request)
	if !errors.Is(assignErr, ErrClaimConflict) || result.Sent || claimer.calls != 0 || dispatcher.calls.Load() != 0 {
		t.Fatalf("assignment crossed clear barrier: result=%+v err=%v claims=%d dispatch=%d", result, assignErr, claimer.calls, dispatcher.calls.Load())
	}

	releaseErr := errors.New("Agent Mail unavailable")
	if err := store.RecordClearReleaseFailed(t.Context(), beadID, releaseErr); err != nil {
		t.Fatalf("RecordClearReleaseFailed: %v", err)
	}
	reloaded, err := LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("reload failed clear: %v", err)
	}
	failed := reloaded.Get(beadID)
	if failed == nil || failed.ClearState != ClearStateReservationReleasing || failed.ClearError != releaseErr.Error() || len(failed.ReservationIDs) != 2 {
		t.Fatalf("failed clear did not retain retry metadata: %+v", failed)
	}
	if err := reloaded.CompleteClear(t.Context(), beadID); err == nil || !strings.Contains(err.Error(), "has not durably completed reservation release") {
		t.Fatalf("CompleteClear before durable release error = %v", err)
	}
	released, err := reloaded.RecordClearLeasesReleased(t.Context(), beadID)
	if err != nil {
		t.Fatalf("RecordClearLeasesReleased: %v", err)
	}
	if released.ClearState != ClearStateLeasesReleased || released.ReservationState != ReservationReleased || len(released.ReservationIDs) != 0 || len(released.ReservedPaths) != 0 {
		t.Fatalf("durable lease release checkpoint = %+v", released)
	}
	if err := reloaded.CompleteClear(t.Context(), beadID); err != nil {
		t.Fatalf("CompleteClear: %v", err)
	}
	if got := mustLoadAssignment(t, session, beadID); got != nil {
		t.Fatalf("confirmed clear left assignment: %+v", got)
	}
	clearedStore, err := LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("reload cleared generation: %v", err)
	}
	if generation := clearedStore.ClearedGeneration(beadID); generation != 1 {
		t.Fatalf("cleared generation=%d, want 1", generation)
	}
}

func TestExplicitClearRejectsPendingCompletionOutboxWithoutMutation(t *testing.T) {
	for _, leased := range []bool{false, true} {
		t.Run(fmt.Sprintf("leased_%t", leased), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			const (
				beadID  = "ntm-pending-completion-clear"
				eventID = "pending-completion-clear-event"
			)
			now := time.Now().UTC()
			store := NewStore("pending-completion-clear")
			store.Assignments[beadID] = &Assignment{
				BeadID: beadID, Status: StatusFailed, AssignedAt: now,
				DispatchTarget: "%64", OccupancyKey: "%64",
				PendingCompletionEventID: eventID, CompletionDetectedAt: cloneTimePtr(&now),
			}
			if leased {
				expiresAt := now.Add(time.Minute)
				store.Assignments[beadID].CompletionConsumerToken = "pending-clear-consumer"
				store.Assignments[beadID].CompletionLeaseExpiresAt = &expiresAt
			}
			if err := store.Save(); err != nil {
				t.Fatalf("seed pending completion clear: %v", err)
			}
			before := mustLoadAssignment(t, store.SessionName, beadID)

			_, err := store.BeginClearIfStatus(t.Context(), beadID, now.Add(time.Second), StatusFailed)
			if !errors.Is(err, ErrCompletionEventPending) {
				t.Fatalf("BeginClearIfStatus error=%v, want ErrCompletionEventPending", err)
			}
			after := mustLoadAssignment(t, store.SessionName, beadID)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("refused pending-event clear mutated assignment:\nbefore=%+v\nafter=%+v", before, after)
			}
			if _, err := LoadStoreStrict(store.SessionName); err != nil {
				t.Fatalf("strict reload after refused pending-event clear: %v", err)
			}
		})
	}
}

func TestCompleteClearRejectsPendingCompletionOutboxWithoutMutation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const (
		session = "pending-completion-complete-clear"
		beadID  = "ntm-pending-completion-complete-clear"
		eventID = "pending-completion-complete-clear-event"
	)
	now := time.Now().UTC()
	store := NewStore(session)
	store.Assignments[beadID] = &Assignment{
		BeadID: beadID, Status: StatusFailed, AssignedAt: now,
		DispatchTarget: "%65", OccupancyKey: "%65",
		ClearState: ClearStateLeasesReleased, ClearStartedAt: cloneTimePtr(&now),
		PendingCompletionEventID: eventID, CompletionDetectedAt: cloneTimePtr(&now),
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed pending completion complete-clear: %v", err)
	}
	before := mustLoadAssignment(t, session, beadID)

	err := store.CompleteClear(t.Context(), beadID)
	if !errors.Is(err, ErrCompletionEventPending) {
		t.Fatalf("CompleteClear error=%v, want ErrCompletionEventPending", err)
	}
	after := mustLoadAssignment(t, session, beadID)
	if !reflect.DeepEqual(after, before) || store.ClearedGeneration(beadID) != 0 {
		t.Fatalf("refused pending-event complete-clear mutated assignment: before=%+v after=%+v generation=%d", before, after, store.ClearedGeneration(beadID))
	}
}

func TestCompleteTerminalReconciliationRetainsReceiptAndClearsLeaseHandles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "assignment-terminal-reconciliation"
	const beadID = "ntm-terminal-reconciliation"
	now := time.Now().UTC()
	store := NewStore(session)
	store.Assignments[beadID] = &Assignment{
		BeadID: beadID, BeadTitle: "Completed work", Pane: 3,
		AgentType: "codex", AgentName: "CodexThree", Status: StatusAssigned, AssignedAt: now,
		IdempotencyKey: "terminal-generation", ClaimActor: "terminal-actor",
		DispatchTarget: "%63", OccupancyKey: "%63", DispatchState: DispatchSent,
		DispatchReceiptID: "receipt-63", ReservationRequired: true,
		ReservationState: ReservationReserved, ReservationCompleted: true,
		ReservationAgent: "CodexThree", ReservationTarget: "%63",
		ReservationRequested: []string{"internal/assignment/**"},
		ReservedPaths:        []string{"internal/assignment/**"}, ReservationIDs: []int{631},
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed terminal assignment: %v", err)
	}
	observed := store.Get(beadID)
	barrier, applied, err := store.BeginTerminalReconciliationWithCompletionEventIfCurrent(t.Context(), observed, StatusCompleted, "")
	if err != nil || !applied {
		t.Fatalf("BeginTerminalReconciliationIfCurrent barrier=%+v applied=%v error=%v", barrier, applied, err)
	}
	durableBarrier := mustLoadAssignment(t, session, beadID)
	if durableBarrier == nil || durableBarrier.PendingTerminalStatus != StatusCompleted || durableBarrier.ClearState != ClearStateReservationReleasing || durableBarrier.TerminalClaimReleased ||
		durableBarrier.PendingCompletionEventID == "" || durableBarrier.CompletionDetectedAt == nil {
		t.Fatalf("durable terminal barrier = %+v", durableBarrier)
	}
	eventID := durableBarrier.PendingCompletionEventID
	if _, err := store.RecordClearLeasesReleased(t.Context(), beadID); err != nil {
		t.Fatalf("RecordClearLeasesReleased: %v", err)
	}
	if _, err := store.RecordTerminalClaimReleased(t.Context(), beadID); err != nil {
		t.Fatalf("RecordTerminalClaimReleased: %v", err)
	}
	if err := store.CompleteTerminalReconciliation(t.Context(), beadID, StatusCompleted, ""); err != nil {
		t.Fatalf("CompleteTerminalReconciliation: %v", err)
	}

	stored := mustLoadAssignment(t, session, beadID)
	if stored == nil || stored.Status != StatusCompleted || stored.CompletedAt == nil || stored.ClearState != ClearStateNone ||
		stored.ReservationState != ReservationReleased || stored.ReservationCompleted || len(stored.ReservationIDs) != 0 ||
		len(stored.ReservedPaths) != 0 || stored.DispatchReceiptID != "receipt-63" || stored.PendingTerminalStatus != "" ||
		stored.PendingTerminalReason != "" || stored.TerminalClaimReleased || stored.PendingCompletionEventID != eventID || stored.CompletionDetectedAt == nil {
		t.Fatalf("terminal reconciliation result = %+v", stored)
	}
	pendingEvents := store.ListPendingCompletionEvents()
	if len(pendingEvents) != 1 || pendingEvents[0].PendingCompletionEventID != eventID {
		t.Fatalf("pending completion outbox=%+v", pendingEvents)
	}
	const consumerToken = "store-test-consumer"
	claimed, acquired, err := store.ClaimPendingCompletionEvent(t.Context(), beadID, eventID, consumerToken, time.Minute)
	if err != nil || !acquired || claimed == nil || claimed.CompletionConsumerToken != consumerToken || claimed.CompletionLeaseExpiresAt == nil {
		t.Fatalf("claim completion outbox acquired=%v row=%+v error=%v", acquired, claimed, err)
	}
	if acknowledged, err := store.AcknowledgeCompletionEvent(t.Context(), beadID, eventID+"-stale", consumerToken); err != nil || acknowledged {
		t.Fatalf("stale acknowledgement applied=%v error=%v", acknowledged, err)
	}
	if acknowledged, err := store.AcknowledgeCompletionEvent(t.Context(), beadID, eventID, "other-consumer"); err != nil || acknowledged {
		t.Fatalf("foreign acknowledgement applied=%v error=%v", acknowledged, err)
	}
	if acknowledged, err := store.AcknowledgeCompletionEvent(t.Context(), beadID, eventID, consumerToken); err != nil || !acknowledged {
		t.Fatalf("exact acknowledgement applied=%v error=%v", acknowledged, err)
	}
	acknowledged := mustLoadAssignment(t, session, beadID)
	if acknowledged == nil || acknowledged.PendingCompletionEventID != "" || acknowledged.CompletionDetectedAt != nil ||
		acknowledged.CompletionConsumerToken != "" || acknowledged.CompletionLeaseExpiresAt != nil || len(store.ListPendingCompletionEvents()) != 0 {
		t.Fatalf("acknowledged completion outbox=%+v", acknowledged)
	}
	if generation := store.ClearedGeneration(beadID); generation != 0 {
		t.Fatalf("terminal reconciliation incremented explicit clear generation to %d", generation)
	}
}

func TestBeginTerminalReconciliationBackfillsCompletionEventOnMatchingBarrier(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const (
		session = "assignment-terminal-event-backfill"
		beadID  = "ntm-terminal-event-backfill"
	)

	store := NewStore(session)
	if _, err := store.Assign(beadID, "Backfill completion event", 4, "codex", "CodexFour", "prompt"); err != nil {
		t.Fatalf("seed assignment: %v", err)
	}
	observed := store.Get(beadID)
	withoutEvent, applied, err := store.BeginTerminalReconciliationIfCurrent(t.Context(), observed, StatusCompleted, "closed externally")
	if err != nil || !applied {
		t.Fatalf("begin no-event barrier row=%+v applied=%v error=%v", withoutEvent, applied, err)
	}
	if withoutEvent.PendingCompletionEventID != "" || withoutEvent.CompletionDetectedAt != nil {
		t.Fatalf("no-event barrier unexpectedly created completion event: %+v", withoutEvent)
	}

	withEvent, applied, err := store.BeginTerminalReconciliationWithCompletionEventIfCurrent(t.Context(), observed, StatusCompleted, "closed externally")
	if err != nil || !applied {
		t.Fatalf("backfill completion event row=%+v applied=%v error=%v", withEvent, applied, err)
	}
	if withEvent.PendingCompletionEventID == "" || withEvent.CompletionDetectedAt == nil {
		t.Fatalf("matching barrier did not receive completion event: %+v", withEvent)
	}
	durable := mustLoadAssignment(t, session, beadID)
	if durable.PendingCompletionEventID != withEvent.PendingCompletionEventID || durable.CompletionDetectedAt == nil {
		t.Fatalf("completion event was not durable: returned=%+v durable=%+v", withEvent, durable)
	}

	preserved, applied, err := store.BeginTerminalReconciliationIfCurrent(t.Context(), observed, StatusCompleted, "closed externally")
	if err != nil || !applied || preserved.PendingCompletionEventID != withEvent.PendingCompletionEventID {
		t.Fatalf("no-event retry did not preserve backfilled event: row=%+v applied=%v error=%v", preserved, applied, err)
	}
}

func TestBeginTerminalReconciliationBackfillsEventAfterNoEventCompletionWins(t *testing.T) {
	tests := []struct {
		name   string
		status AssignmentStatus
		reason string
	}{
		{name: "completed", status: StatusCompleted},
		{name: "failed", status: StatusFailed, reason: "worker failed"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			session := "terminal-event-after-complete-" + test.name
			beadID := "ntm-terminal-event-after-complete-" + test.name
			store := NewStore(session)
			if _, err := store.Assign(beadID, "Terminal event race", 5, "codex", "CodexFive", "prompt"); err != nil {
				t.Fatalf("seed assignment: %v", err)
			}
			observed := store.Get(beadID)
			barrier, applied, err := store.BeginTerminalReconciliationIfCurrent(t.Context(), observed, test.status, test.reason)
			if err != nil || !applied || barrier == nil {
				t.Fatalf("begin no-event barrier=%+v applied=%v error=%v", barrier, applied, err)
			}
			if _, err := store.RecordClearLeasesReleased(t.Context(), beadID); err != nil {
				t.Fatalf("record leases released: %v", err)
			}
			if _, err := store.RecordTerminalClaimReleased(t.Context(), beadID); err != nil {
				t.Fatalf("record claim released: %v", err)
			}
			if err := store.CompleteTerminalReconciliation(t.Context(), beadID, test.status, test.reason); err != nil {
				t.Fatalf("complete no-event reconciliation: %v", err)
			}
			completed := mustLoadAssignment(t, session, beadID)
			if completed.Status != test.status || completed.PendingCompletionEventID != "" {
				t.Fatalf("no-event completion=%+v", completed)
			}
			if test.status == StatusFailed {
				if row, applied, err := store.BeginTerminalReconciliationWithCompletionEventIfCurrent(t.Context(), observed, test.status, "different reason"); err != nil || applied || row != nil {
					t.Fatalf("mismatched failed outcome row=%+v applied=%v error=%v", row, applied, err)
				}
			}

			withEvent, applied, err := store.BeginTerminalReconciliationWithCompletionEventIfCurrent(t.Context(), observed, test.status, test.reason)
			if err != nil || !applied || withEvent == nil || withEvent.PendingCompletionEventID == "" || withEvent.CompletionDetectedAt == nil {
				t.Fatalf("post-completion event row=%+v applied=%v error=%v", withEvent, applied, err)
			}
			durable := mustLoadAssignment(t, session, beadID)
			if durable.PendingCompletionEventID != withEvent.PendingCompletionEventID || durable.CompletionDetectedAt == nil {
				t.Fatalf("post-completion event not durable: returned=%+v durable=%+v", withEvent, durable)
			}
		})
	}
}

func TestListPendingCompletionEventsUsesDeterministicReplayOrder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("assignment-completion-outbox-order")
	early := time.Date(2026, time.July, 13, 9, 30, 0, 0, time.UTC)
	later := early.Add(time.Minute)
	store.Assignments = map[string]*Assignment{
		"later": {
			BeadID: "ntm-z", Status: StatusCompleted,
			PendingCompletionEventID: "event-later", CompletionDetectedAt: &later,
		},
		"tie-b": {
			BeadID: "ntm-a", Status: StatusFailed,
			PendingCompletionEventID: "event-b", CompletionDetectedAt: &early,
		},
		"first": {
			BeadID: "ntm-0", Status: StatusCompleted,
			PendingCompletionEventID: "event-first", CompletionDetectedAt: &early,
		},
		"tie-a": {
			BeadID: "ntm-a", Status: StatusFailed,
			PendingCompletionEventID: "event-a", CompletionDetectedAt: &early,
		},
	}

	got := store.ListPendingCompletionEvents()
	want := []string{"event-first", "event-a", "event-b", "event-later"}
	if len(got) != len(want) {
		t.Fatalf("pending completion events=%+v, want %d rows", got, len(want))
	}
	for index, eventID := range want {
		if got[index].PendingCompletionEventID != eventID {
			t.Fatalf("pending completion event %d=%q, want %q (all=%+v)", index, got[index].PendingCompletionEventID, eventID, got)
		}
	}
}

func TestCompletionEventLeaseOwnershipRenewalAndExpiryAcrossStores(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const (
		session = "assignment-completion-lease"
		beadID  = "ntm-completion-lease"
		eventID = "completion-event-generation"
	)
	base := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
	seed := NewStore(session)
	seed.Assignments[beadID] = &Assignment{
		BeadID: beadID, Status: StatusCompleted, AssignedAt: base,
		DispatchTarget: "%81", OccupancyKey: "%81",
		PendingCompletionEventID: eventID, CompletionDetectedAt: &base,
	}
	if err := seed.Save(); err != nil {
		t.Fatalf("seed completion lease: %v", err)
	}
	first, err := LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load first completion consumer: %v", err)
	}
	second, err := LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load second completion consumer: %v", err)
	}
	leaseNow := base
	first.completionLeaseClock = func() time.Time { return leaseNow }
	second.completionLeaseClock = func() time.Time { return leaseNow }

	claimed, acquired, err := first.ClaimPendingCompletionEvent(t.Context(), beadID, eventID, "consumer-a", time.Minute)
	if err != nil || !acquired || claimed.CompletionLeaseExpiresAt == nil || !claimed.CompletionLeaseExpiresAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("first claim acquired=%v row=%+v error=%v", acquired, claimed, err)
	}
	leaseNow = base.Add(30 * time.Second)
	if row, acquired, err := second.ClaimPendingCompletionEvent(t.Context(), beadID, eventID, "consumer-b", time.Minute); err != nil || acquired || row == nil || row.CompletionConsumerToken != "consumer-a" {
		t.Fatalf("live foreign claim acquired=%v row=%+v error=%v", acquired, row, err)
	}
	leaseNow = base.Add(45 * time.Second)
	if renewed, err := first.RenewPendingCompletionEventLease(t.Context(), beadID, eventID, "consumer-a", time.Minute); err != nil || !renewed {
		t.Fatalf("owner renewal applied=%v error=%v", renewed, err)
	}
	leaseNow = base.Add(75 * time.Second)
	if _, acquired, err := second.ClaimPendingCompletionEvent(t.Context(), beadID, eventID, "consumer-b", time.Minute); err != nil || acquired {
		t.Fatalf("claim during renewed lease acquired=%v error=%v", acquired, err)
	}
	leaseNow = base.Add(106 * time.Second)
	recovered, acquired, err := second.ClaimPendingCompletionEvent(t.Context(), beadID, eventID, "consumer-b", time.Minute)
	if err != nil || !acquired || recovered == nil || recovered.CompletionConsumerToken != "consumer-b" {
		t.Fatalf("expired lease takeover acquired=%v row=%+v error=%v", acquired, recovered, err)
	}
	leaseNow = base.Add(107 * time.Second)
	if renewed, err := first.RenewPendingCompletionEventLease(t.Context(), beadID, eventID, "consumer-a", time.Minute); err != nil || renewed {
		t.Fatalf("stale owner renewal applied=%v error=%v", renewed, err)
	}
	if acknowledged, err := first.AcknowledgeCompletionEvent(t.Context(), beadID, eventID, "consumer-a"); err != nil || acknowledged {
		t.Fatalf("stale owner acknowledgement applied=%v error=%v", acknowledged, err)
	}
	if acknowledged, err := second.AcknowledgeCompletionEvent(t.Context(), beadID, eventID, "consumer-b"); err != nil || !acknowledged {
		t.Fatalf("recovery owner acknowledgement applied=%v error=%v", acknowledged, err)
	}
}

func TestCompletionEventLeaseRenewalSamplesClockAfterOperationLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const (
		session       = "assignment-completion-lease-contended"
		beadID        = "ntm-completion-lease-contended"
		eventID       = "completion-event-contended"
		consumerToken = "consumer-contended"
	)
	base := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
	detectedAt := base.Add(-time.Minute)
	store := NewStore(session)
	store.Assignments[beadID] = &Assignment{
		BeadID: beadID, Status: StatusCompleted, AssignedAt: detectedAt,
		DispatchTarget: "%82", OccupancyKey: "%82",
		PendingCompletionEventID: eventID, CompletionDetectedAt: &detectedAt,
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed contended completion lease: %v", err)
	}

	clockValues := make(chan time.Time, 1)
	store.completionLeaseClock = func() time.Time { return <-clockValues }
	leaseDuration := time.Minute
	clockValues <- base
	claimed, acquired, err := store.ClaimPendingCompletionEvent(
		t.Context(), beadID, eventID, consumerToken, leaseDuration,
	)
	if err != nil || !acquired || claimed == nil || claimed.CompletionLeaseExpiresAt == nil {
		t.Fatalf("claim contended completion lease acquired=%v row=%+v error=%v", acquired, claimed, err)
	}

	lockEntered := make(chan struct{})
	allowLock := make(chan struct{})
	store.completionLeaseLock = func(context.Context, string, string) (func(), error) {
		close(lockEntered)
		<-allowLock
		return func() {}, nil
	}
	var lockAcquired atomic.Bool
	store.completionLeaseClock = func() time.Time {
		if lockAcquired.Load() {
			return base.Add(leaseDuration)
		}
		return base
	}
	result := make(chan struct {
		renewed bool
		err     error
	}, 1)
	go func() {
		renewed, renewErr := store.RenewPendingCompletionEventLease(
			t.Context(), beadID, eventID, consumerToken, leaseDuration,
		)
		result <- struct {
			renewed bool
			err     error
		}{renewed: renewed, err: renewErr}
	}()
	<-lockEntered
	lockAcquired.Store(true)
	close(allowLock)

	select {
	case got := <-result:
		if got.err != nil || got.renewed {
			t.Fatalf("post-expiry contended renewal applied=%v error=%v", got.renewed, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("contended completion lease renewal did not finish")
	}

	reloaded, err := LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("reload contended completion lease: %v", err)
	}
	current := reloaded.Get(beadID)
	if current == nil || current.CompletionLeaseExpiresAt == nil || !current.CompletionLeaseExpiresAt.Equal(base.Add(leaseDuration)) {
		t.Fatalf("contended renewal changed durable expiry: %+v", current)
	}
}

func TestOwnsLiveCompletionEventLease(t *testing.T) {
	now := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
	expired := now.Add(-time.Nanosecond)
	boundary := now
	live := now.Add(time.Nanosecond)

	tests := []struct {
		name       string
		assignment *Assignment
		eventID    string
		token      string
		want       bool
	}{
		{
			name:       "live lease",
			assignment: &Assignment{PendingCompletionEventID: "event", CompletionConsumerToken: "consumer", CompletionLeaseExpiresAt: &live},
			eventID:    "event",
			token:      "consumer",
			want:       true,
		},
		{
			name:       "expired lease",
			assignment: &Assignment{PendingCompletionEventID: "event", CompletionConsumerToken: "consumer", CompletionLeaseExpiresAt: &expired},
			eventID:    "event",
			token:      "consumer",
		},
		{
			name:       "expiry boundary is not live",
			assignment: &Assignment{PendingCompletionEventID: "event", CompletionConsumerToken: "consumer", CompletionLeaseExpiresAt: &boundary},
			eventID:    "event",
			token:      "consumer",
		},
		{
			name:       "nil lease",
			assignment: &Assignment{PendingCompletionEventID: "event", CompletionConsumerToken: "consumer"},
			eventID:    "event",
			token:      "consumer",
		},
		{
			name:       "different event",
			assignment: &Assignment{PendingCompletionEventID: "event", CompletionConsumerToken: "consumer", CompletionLeaseExpiresAt: &live},
			eventID:    "other-event",
			token:      "consumer",
		},
		{
			name:       "different consumer",
			assignment: &Assignment{PendingCompletionEventID: "event", CompletionConsumerToken: "consumer", CompletionLeaseExpiresAt: &live},
			eventID:    "event",
			token:      "other-consumer",
		},
		{
			name:    "missing assignment",
			eventID: "event",
			token:   "consumer",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ownsLiveCompletionEventLease(test.assignment, test.eventID, test.token, now); got != test.want {
				t.Fatalf("ownsLiveCompletionEventLease()=%v, want %v", got, test.want)
			}
		})
	}
}

func TestAcknowledgeCompletionEventRejectsExpiredLeaseWithoutMutation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const (
		session       = "assignment-expired-completion-ack"
		beadID        = "ntm-expired-completion-ack"
		eventID       = "expired-completion-event"
		consumerToken = "expired-completion-consumer"
	)
	detectedAt := time.Now().UTC().Add(-2 * time.Hour)
	expiresAt := detectedAt.Add(time.Hour)
	store := NewStore(session)
	store.Assignments[beadID] = &Assignment{
		BeadID: beadID, Status: StatusCompleted, AssignedAt: detectedAt,
		DispatchTarget: "%82", OccupancyKey: "%82",
		PendingCompletionEventID: eventID, CompletionDetectedAt: &detectedAt,
		CompletionConsumerToken: consumerToken, CompletionLeaseExpiresAt: &expiresAt,
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed expired completion lease: %v", err)
	}
	before := mustLoadAssignment(t, session, beadID)

	acknowledged, err := store.AcknowledgeCompletionEvent(t.Context(), beadID, eventID, consumerToken)
	if err != nil || acknowledged {
		t.Fatalf("expired acknowledgement applied=%v error=%v", acknowledged, err)
	}
	after := mustLoadAssignment(t, session, beadID)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("expired acknowledgement mutated assignment:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestCompleteTerminalReconciliationRequiresBarrierAndTerminalStatus(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const beadID = "ntm-terminal-barrier-required"
	store := NewStore("assignment-terminal-barrier-required")
	store.Assignments[beadID] = &Assignment{BeadID: beadID, Status: StatusClaimed, AssignedAt: time.Now().UTC()}
	if err := store.Save(); err != nil {
		t.Fatalf("seed assignment: %v", err)
	}
	if err := store.CompleteTerminalReconciliation(t.Context(), beadID, StatusFailed, "closed early"); err == nil || !strings.Contains(err.Error(), "has not durably completed reservation release") {
		t.Fatalf("missing barrier error = %v", err)
	}
	if err := store.CompleteTerminalReconciliation(t.Context(), beadID, StatusWorking, ""); err == nil || !strings.Contains(err.Error(), "must be completed or failed") {
		t.Fatalf("nonterminal status error = %v", err)
	}
	if _, applied, err := store.BeginTerminalReconciliationIfCurrent(t.Context(), store.Get(beadID), StatusWorking, ""); err == nil || applied {
		t.Fatalf("nonterminal begin applied=%v error=%v", applied, err)
	}
}

func TestBeginTerminalReconciliationRejectsSupersededGenerationBeforeBarrier(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "terminal-reconciliation-stale-generation"
	const beadID = "ntm-terminal-reconciliation-stale"
	store := NewStore(session)
	observed, err := store.Assign(beadID, "Old", 1, "codex", "OldAgent", "old work")
	if err != nil {
		t.Fatalf("Assign old generation: %v", err)
	}
	winner, err := LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("LoadStoreStrict: %v", err)
	}
	replacement, err := winner.Assign(beadID, "Winner", 2, "codex", "WinnerAgent", "winner work")
	if err != nil {
		t.Fatalf("Assign winner: %v", err)
	}

	barrier, applied, err := store.BeginTerminalReconciliationIfCurrent(t.Context(), observed, StatusCompleted, "")
	if err != nil || applied || barrier != nil {
		t.Fatalf("stale begin barrier=%+v applied=%v error=%v", barrier, applied, err)
	}
	current := mustLoadAssignment(t, session, beadID)
	if current == nil || current.IdempotencyKey != replacement.IdempotencyKey || current.Status != StatusAssigned ||
		current.ClearState != ClearStateNone || current.PendingTerminalStatus != "" {
		t.Fatalf("stale begin mutated winner: %+v", current)
	}
}

func TestBeginTerminalReconciliationConcurrentDifferentReasonsHasOneWinner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "terminal-reconciliation-reason-race"
	const beadID = "ntm-terminal-reconciliation-reason-race"
	seed := NewStore(session)
	if _, err := seed.Assign(beadID, "Reason race", 1, "codex", "CodexOne", "work"); err != nil {
		t.Fatalf("Assign: %v", err)
	}

	type attempt struct {
		reason  string
		barrier *Assignment
		applied bool
		err     error
	}
	stores := make([]*AssignmentStore, 2)
	observed := make([]*Assignment, 2)
	for i := range stores {
		var err error
		stores[i], err = LoadStoreStrict(session)
		if err != nil {
			t.Fatalf("LoadStoreStrict(%d): %v", i, err)
		}
		observed[i] = stores[i].Get(beadID)
	}

	start := make(chan struct{})
	results := make(chan attempt, 2)
	var wg sync.WaitGroup
	for i, reason := range []string{"first detector reason", "second detector reason"} {
		wg.Add(1)
		go func(store *AssignmentStore, generation *Assignment, reason string) {
			defer wg.Done()
			<-start
			barrier, applied, err := store.BeginTerminalReconciliationIfCurrent(t.Context(), generation, StatusFailed, reason)
			results <- attempt{reason: reason, barrier: barrier, applied: applied, err: err}
		}(stores[i], observed[i], reason)
	}
	close(start)
	wg.Wait()
	close(results)

	var winner attempt
	appliedCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("begin reason %q: %v", result.reason, result.err)
		}
		if result.applied {
			appliedCount++
			winner = result
		} else if result.barrier != nil {
			t.Fatalf("losing reason %q returned barrier %+v", result.reason, result.barrier)
		}
	}
	if appliedCount != 1 {
		t.Fatalf("applied attempts=%d, want exactly one", appliedCount)
	}
	current := mustLoadAssignment(t, session, beadID)
	if current == nil || current.PendingTerminalStatus != StatusFailed || current.PendingTerminalReason != winner.reason ||
		winner.barrier == nil || winner.barrier.PendingTerminalReason != winner.reason {
		t.Fatalf("durable winner=%+v attempt=%+v", current, winner)
	}
}

func TestAssignmentEventIdentityAndIdleUsePhysicalPaneIDsAcrossWindows(t *testing.T) {
	completed := &Assignment{
		BeadID: "ntm-window-zero", Pane: 1, Status: StatusCompleted,
		OccupancyKey: "%41", DispatchTarget: "%41",
	}
	otherWindow := &Assignment{
		BeadID: "ntm-window-one", Pane: 1, Status: StatusWorking,
		OccupancyKey: "%42", DispatchTarget: "%42",
	}
	store := NewStore("event-physical-pane-identity")
	store.Assignments[completed.BeadID] = completed
	store.Assignments[otherWindow.BeadID] = otherWindow

	paneID, err := assignmentEventPaneID(completed)
	if err != nil || paneID != "%41" {
		t.Fatalf("assignmentEventPaneID()=%q error=%v", paneID, err)
	}
	if !store.shouldEmitAgentIdleLocked(completed, StatusWorking, StatusCompleted) {
		t.Fatal("same local pane index in a different window suppressed physical-pane idle event")
	}

	otherWindow.OccupancyKey = "%41"
	otherWindow.DispatchTarget = "%41"
	if store.shouldEmitAgentIdleLocked(completed, StatusWorking, StatusCompleted) {
		t.Fatal("working assignment on the same physical pane allowed idle event")
	}
	if _, err := assignmentEventPaneID(&Assignment{BeadID: "legacy", Pane: 1}); !errors.Is(err, ErrPaneIdentityMigrationRequired) {
		t.Fatalf("legacy event identity error=%v, want typed migration error", err)
	}
}

func TestAssignmentClearRejectsUnknownDispatchOutcome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const beadID = "ntm-clear-sending"
	store := NewStore("assignment-clear-sending")
	request := AtomicRequest{
		BeadID: beadID, BeadTitle: "Sending", Target: "%71", OccupancyKey: "%71", Pane: 1,
		AgentType: "codex", AgentName: "CodexOne", Actor: "CodexOne", Prompt: "work", IdempotencyKey: "sending-key",
	}
	now := time.Now().UTC()
	actor := StableClaimActor(request.Actor, request.IdempotencyKey)
	if _, err := store.RecordAtomicIntent(request, actor, now); err != nil {
		t.Fatalf("RecordAtomicIntent: %v", err)
	}
	if err := store.RecordAtomicDispatchStarted(beadID, request.IdempotencyKey, now); err != nil {
		t.Fatalf("RecordAtomicDispatchStarted: %v", err)
	}
	_, err := store.BeginClear(t.Context(), beadID, now.Add(time.Second))
	if !errors.Is(err, ErrDispatchOutcomeUnknown) {
		t.Fatalf("BeginClear error=%v, want ErrDispatchOutcomeUnknown", err)
	}
	stored := store.Get(beadID)
	if stored == nil || stored.ClearState != ClearStateNone || stored.DispatchState != DispatchSending || stored.DispatchAttempts != 1 {
		t.Fatalf("rejected clear mutated dispatch barrier: %+v", stored)
	}

	// The explicit operator escape hatch does establish the barrier: dispatch
	// returns to pending (retryable) and the reason is recorded.
	forced, err := store.BeginClearForce(t.Context(), beadID, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("BeginClearForce: %v", err)
	}
	if forced.ClearState != ClearStateReservationReleasing || forced.DispatchState != DispatchPending ||
		!strings.Contains(forced.LastDispatchError, "force-cleared") {
		t.Fatalf("forced clear did not terminalize unknown dispatch: %+v", forced)
	}
}

func TestBeginClearForceIfStatusStillGuardsStatus(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const beadID = "ntm-clear-force-status"
	store := NewStore("assignment-clear-force-status")
	request := AtomicRequest{
		BeadID: beadID, BeadTitle: "Sending", Target: "%72", OccupancyKey: "%72", Pane: 1,
		AgentType: "codex", AgentName: "CodexOne", Actor: "CodexOne", Prompt: "work", IdempotencyKey: "force-status-key",
	}
	now := time.Now().UTC()
	if _, err := store.RecordAtomicIntent(request, StableClaimActor(request.Actor, request.IdempotencyKey), now); err != nil {
		t.Fatalf("RecordAtomicIntent: %v", err)
	}
	if err := store.RecordAtomicDispatchStarted(beadID, request.IdempotencyKey, now); err != nil {
		t.Fatalf("RecordAtomicDispatchStarted: %v", err)
	}
	// Force bypasses only the dispatch-outcome barrier, never the status guard.
	if _, err := store.BeginClearForceIfStatus(t.Context(), beadID, now.Add(time.Second), StatusFailed); !errors.Is(err, ErrAssignmentStatusMismatch) {
		t.Fatalf("BeginClearForceIfStatus error=%v, want ErrAssignmentStatusMismatch", err)
	}
	if _, err := store.BeginClearForceIfStatus(t.Context(), beadID, now.Add(time.Second)); err == nil {
		t.Fatal("BeginClearForceIfStatus with no expected statuses must fail")
	}
}

func TestBeginClearIfStatusRejectsConcurrentLifecycleChange(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "assignment-clear-status-guard"
	const beadID = "ntm-clear-status-guard"
	now := time.Now().UTC()
	seed := NewStore(session)
	seed.Assignments[beadID] = &Assignment{
		BeadID: beadID, BeadTitle: "Failed assignment", Pane: 1,
		AgentType: "codex", AgentName: "CodexOne", Status: StatusFailed, AssignedAt: now,
	}
	if err := seed.Save(); err != nil {
		t.Fatalf("seed failed assignment: %v", err)
	}
	staleFilter, err := LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load failed-only filter: %v", err)
	}
	retry, err := LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load concurrent retry: %v", err)
	}
	if err := retry.UpdateStatus(beadID, StatusAssigned); err != nil {
		t.Fatalf("concurrent retry: %v", err)
	}
	_, clearErr := staleFilter.BeginClearIfStatus(t.Context(), beadID, now.Add(time.Minute), StatusFailed)
	if !errors.Is(clearErr, ErrAssignmentStatusMismatch) {
		t.Fatalf("BeginClearIfStatus error=%v, want ErrAssignmentStatusMismatch", clearErr)
	}
	stored := mustLoadAssignment(t, session, beadID)
	if stored == nil || stored.Status != StatusAssigned || stored.ClearState != ClearStateNone {
		t.Fatalf("failed-only clear crossed lifecycle guard: %+v", stored)
	}
}

func mustLoadAssignment(t *testing.T, session, beadID string) *Assignment {
	t.Helper()
	store, err := LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("LoadStoreStrict(%s): %v", session, err)
	}
	return store.Get(beadID)
}

func TestGuardedLifecycleTransitionsApplyOnlyToObservedGeneration(t *testing.T) {
	t.Run("working then completed", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		store := NewStore("guarded-lifecycle-success")
		observed, err := store.Assign("ntm-guarded-success", "Guarded", 1, "codex", "CodexOne", "work")
		if err != nil {
			t.Fatalf("Assign: %v", err)
		}
		applied, err := store.MarkWorkingIfCurrent(t.Context(), observed)
		if err != nil || !applied {
			t.Fatalf("MarkWorkingIfCurrent applied=%v error=%v", applied, err)
		}
		working := store.Get(observed.BeadID)
		if working == nil || working.Status != StatusWorking || working.StartedAt == nil {
			t.Fatalf("working assignment=%+v", working)
		}
		applied, err = store.MarkCompletedIfCurrent(t.Context(), observed)
		if err != nil || !applied {
			t.Fatalf("MarkCompletedIfCurrent applied=%v error=%v", applied, err)
		}
		completed := store.Get(observed.BeadID)
		if completed == nil || completed.Status != StatusCompleted || completed.CompletedAt == nil {
			t.Fatalf("completed assignment=%+v", completed)
		}
	})

	t.Run("failed with reason", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		store := NewStore("guarded-lifecycle-failed")
		observed, err := store.Assign("ntm-guarded-failed", "Guarded", 1, "codex", "CodexOne", "work")
		if err != nil {
			t.Fatalf("Assign: %v", err)
		}
		applied, err := store.MarkFailedIfCurrent(t.Context(), observed, "agent stopped")
		if err != nil || !applied {
			t.Fatalf("MarkFailedIfCurrent applied=%v error=%v", applied, err)
		}
		failed := store.Get(observed.BeadID)
		if failed == nil || failed.Status != StatusFailed || failed.FailedAt == nil || failed.FailReason != "agent stopped" || failed.FailureReason != "" {
			t.Fatalf("failed assignment=%+v", failed)
		}
	})
}

func TestGuardedLifecycleTransitionsRejectSupersededAtomicGeneration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "guarded-lifecycle-superseded"
	const beadID = "ntm-guarded-superseded"
	now := time.Now().UTC()
	store := NewStore(session)
	store.Assignments[beadID] = &Assignment{
		BeadID: beadID, BeadTitle: "Old", Pane: 1, AgentType: "codex", AgentName: "OldAgent",
		Status: StatusAssigned, AssignedAt: now, IdempotencyKey: "old-generation",
		DispatchState: DispatchSent, DispatchReceiptID: "old-receipt", DispatchTarget: "%71", OccupancyKey: "%71",
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed old generation: %v", err)
	}
	observed := store.Get(beadID)
	winner, err := LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load winner: %v", err)
	}
	winner.mutex.Lock()
	winner.Assignments[beadID] = &Assignment{
		BeadID: beadID, BeadTitle: "New", Pane: 2, AgentType: "codex", AgentName: "NewAgent",
		Status: StatusAssigned, AssignedAt: now.Add(time.Minute), IdempotencyKey: "new-generation",
		DispatchState: DispatchSent, DispatchReceiptID: "new-receipt", DispatchTarget: "%72", OccupancyKey: "%72",
	}
	winner.replace[beadID] = struct{}{}
	winner.mutex.Unlock()
	if err := winner.Save(); err != nil {
		t.Fatalf("persist winner: %v", err)
	}

	for name, transition := range map[string]func() (bool, error){
		"working":   func() (bool, error) { return store.MarkWorkingIfCurrent(t.Context(), observed) },
		"completed": func() (bool, error) { return store.MarkCompletedIfCurrent(t.Context(), observed) },
		"failed":    func() (bool, error) { return store.MarkFailedIfCurrent(t.Context(), observed, "stale") },
	} {
		t.Run(name, func(t *testing.T) {
			applied, err := transition()
			if err != nil || applied {
				t.Fatalf("stale transition applied=%v error=%v", applied, err)
			}
			stored := mustLoadAssignment(t, session, beadID)
			if stored == nil || stored.IdempotencyKey != "new-generation" || stored.Status != StatusAssigned || stored.DispatchReceiptID != "new-receipt" {
				t.Fatalf("stale transition mutated winner: %+v", stored)
			}
		})
	}
}

func TestGuardedLifecycleTransitionRejectsClearingGeneration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("guarded-lifecycle-clearing")
	observed, err := store.Assign("ntm-guarded-clearing", "Guarded", 1, "codex", "CodexOne", "work")
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}
	if _, err := store.BeginClearIfStatus(t.Context(), observed.BeadID, time.Now().UTC(), StatusAssigned); err != nil {
		t.Fatalf("BeginClearIfStatus: %v", err)
	}
	applied, err := store.MarkCompletedIfCurrent(t.Context(), observed)
	if err != nil || applied {
		t.Fatalf("clearing transition applied=%v error=%v", applied, err)
	}
	stored := store.Get(observed.BeadID)
	if stored == nil || stored.Status != StatusAssigned || stored.ClearState != ClearStateReservationReleasing || stored.CompletedAt != nil {
		t.Fatalf("completion crossed clear barrier: %+v", stored)
	}
}

func TestGuardedLifecycleTransitionRequiresObservedGeneration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := NewStore("guarded-lifecycle-required-observation")
	for name, observed := range map[string]*Assignment{
		"nil":        nil,
		"missing id": {},
	} {
		t.Run(name, func(t *testing.T) {
			if applied, err := store.MarkCompletedIfCurrent(t.Context(), observed); err == nil || applied {
				t.Fatalf("MarkCompletedIfCurrent applied=%v error=%v, want validation error", applied, err)
			}
		})
	}
}

func TestLoadNormalizesLegacyFailureReason(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	dir := filepath.Join(tmpDir, ".ntm", "sessions", "legacy-failure-test")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}

	raw := []byte(`{
  "session_name": "legacy-failure-test",
  "assignments": {
    "bd-legacy": {
      "bead_id": "bd-legacy",
      "bead_title": "Legacy failure bead",
      "pane": 1,
      "agent_type": "claude",
      "status": "failed",
      "assigned_at": "2026-01-01T00:00:00Z",
      "failed_at": "2026-01-01T00:05:00Z",
      "failure_reason": "legacy reason"
    }
  },
  "updated_at": "2026-01-01T00:05:00Z",
  "version": 1
}`)
	mainPath := filepath.Join(dir, "assignments.json")
	if err := os.WriteFile(mainPath, raw, 0644); err != nil {
		t.Fatalf("failed to write legacy file: %v", err)
	}

	store, err := LoadStore("legacy-failure-test")
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}

	got := store.Get("bd-legacy")
	if got == nil {
		t.Fatal("expected legacy assignment, got nil")
	}
	if got.FailReason != "legacy reason" {
		t.Fatalf("expected FailReason to be normalized, got %q", got.FailReason)
	}
	if got.FailureReason != "" {
		t.Fatalf("expected FailureReason to be cleared after normalization, got %q", got.FailureReason)
	}
}

func TestPersistenceMissingDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Don't create directory - Load should handle it gracefully
	store, err := LoadStore("missing-dir-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have empty store
	if len(store.List()) != 0 {
		t.Errorf("expected empty store, got %d assignments", len(store.List()))
	}

	// Save should create directory
	_, err = store.Assign("bd-123", "Test", 1, "claude", "", "")
	if err != nil {
		t.Errorf("unexpected error assigning: %v", err)
	}

	// Verify directory was created
	dir := filepath.Join(tmpDir, ".ntm", "sessions", "missing-dir-test")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Error("expected directory to be created")
	}
}

func TestConcurrentAccess(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store := NewStore("concurrent-test")

	var wg sync.WaitGroup
	numGoroutines := 10
	assignmentsPerGoroutine := 5

	// Concurrent writes
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for j := 0; j < assignmentsPerGoroutine; j++ {
				beadID := "bd-" + string(rune('A'+goroutineID)) + string(rune('0'+j))
				_, _ = store.Assign(beadID, "Test", goroutineID, "claude", "", "")
			}
		}(i)
	}

	// Concurrent reads
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < assignmentsPerGoroutine; j++ {
				_ = store.List()
				_ = store.Stats()
			}
		}()
	}

	wg.Wait()

	// Verify all assignments were created
	assignments := store.List()
	expectedCount := numGoroutines * assignmentsPerGoroutine
	if len(assignments) != expectedCount {
		t.Errorf("expected %d assignments, got %d", expectedCount, len(assignments))
	}
}

func TestNonExistentAssignment(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store := NewStore("test-session")

	// Try to update non-existent assignment
	err := store.MarkWorking("bd-nonexistent")
	if err == nil {
		t.Error("expected error for non-existent assignment")
	}

	// Try to reassign non-existent assignment
	_, err = store.Reassign("bd-nonexistent", ReassignmentTarget{Pane: 2, AgentType: "codex"})
	if err == nil {
		t.Error("expected error for non-existent assignment")
	}
}

func TestStorageDir(t *testing.T) {
	// Test with HOME set
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	dir := StorageDir()
	// New behavior: ~/.ntm/sessions
	expected := filepath.Join(tmpDir, ".ntm", "sessions")
	if dir != expected {
		t.Errorf("expected %s, got %s", expected, dir)
	}
}

func TestPersistenceErrorTypes(t *testing.T) {
	// Test PersistenceError
	cause := os.ErrPermission
	perr := &PersistenceError{
		Operation: "save",
		Path:      "/test/path",
		Cause:     cause,
	}

	if perr.Unwrap() != cause {
		t.Error("expected Unwrap to return cause")
	}

	errStr := perr.Error()
	if errStr == "" {
		t.Error("expected non-empty error string")
	}

	// Test InvalidTransitionError
	iterr := &InvalidTransitionError{
		BeadID: "bd-123",
		From:   StatusAssigned,
		To:     StatusCompleted,
	}

	errStr = iterr.Error()
	if errStr == "" {
		t.Error("expected non-empty error string")
	}
}

// bd-fresh-eyes-audit .15: saveLocked merges this process's delta against the
// file, and mergeAssignmentDelta refuses a delta whose on-disk record is
// already terminal. UpdateStatus/MarkFailed used to return nil anyway and emit
// a webhook announcing a status that exists nowhere on disk, while cloning the
// in-memory pointer saveLocked had just orphaned.
func TestUpdateStatusFailsWhenMergeDiscardsTheTransition(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "assignment-discarded-status"
	const beadID = "ntm-discarded-status"
	now := time.Now().UTC()

	seed := NewStore(session)
	seed.Assignments[beadID] = &Assignment{
		BeadID: beadID, BeadTitle: "Work", Pane: 1,
		AgentType: "claude", AgentName: "ClaudeOne", Status: StatusAssigned, AssignedAt: now,
	}
	if err := seed.Save(); err != nil {
		t.Fatalf("seed assignment: %v", err)
	}

	// Process 1 holds a view of the assignment...
	stale, err := LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load stale view: %v", err)
	}
	// ...while process 2 completes it.
	winner, err := LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load winner view: %v", err)
	}
	if err := winner.MarkCompleted(beadID); err != nil {
		t.Fatalf("winner MarkCompleted: %v", err)
	}

	// Process 1 now tries to move it to working. The merge must refuse, and
	// the caller must be TOLD, not handed a silent success.
	err = stale.MarkWorking(beadID)
	if !errors.Is(err, ErrAssignmentStatusMismatch) {
		t.Fatalf("MarkWorking after concurrent completion: err=%v, want ErrAssignmentStatusMismatch", err)
	}

	stored := mustLoadAssignment(t, session, beadID)
	if stored == nil || stored.Status != StatusCompleted {
		t.Fatalf("on-disk status = %+v, want completed to be preserved", stored)
	}
}

func TestMarkFailedFailsWhenMergeDiscardsTheTransition(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "assignment-discarded-failure"
	const beadID = "ntm-discarded-failure"
	now := time.Now().UTC()

	seed := NewStore(session)
	seed.Assignments[beadID] = &Assignment{
		BeadID: beadID, BeadTitle: "Work", Pane: 2,
		AgentType: "codex", AgentName: "CodexOne", Status: StatusWorking, AssignedAt: now,
	}
	if err := seed.Save(); err != nil {
		t.Fatalf("seed assignment: %v", err)
	}

	stale, err := LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load stale view: %v", err)
	}
	winner, err := LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load winner view: %v", err)
	}
	if err := winner.MarkCompleted(beadID); err != nil {
		t.Fatalf("winner MarkCompleted: %v", err)
	}

	err = stale.MarkFailed(beadID, "timed out")
	if !errors.Is(err, ErrAssignmentStatusMismatch) {
		t.Fatalf("MarkFailed after concurrent completion: err=%v, want ErrAssignmentStatusMismatch", err)
	}
	stored := mustLoadAssignment(t, session, beadID)
	if stored == nil || stored.Status != StatusCompleted {
		t.Fatalf("on-disk status = %+v, want completed to be preserved", stored)
	}
}
