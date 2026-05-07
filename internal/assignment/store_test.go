package assignment

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
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
	if store.Version != 1 {
		t.Errorf("expected version 1, got %d", store.Version)
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

func TestListByPane(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	store := NewStore("test-session")
	_, _ = store.Assign("bd-1", "Bead 1", 1, "claude", "", "")
	_, _ = store.Assign("bd-2", "Bead 2", 1, "claude", "", "")
	_, _ = store.Assign("bd-3", "Bead 3", 2, "codex", "", "")

	pane1 := store.ListByPane(1)
	if len(pane1) != 2 {
		t.Errorf("expected 2 assignments for pane 1, got %d", len(pane1))
	}

	pane2 := store.ListByPane(2)
	if len(pane2) != 1 {
		t.Errorf("expected 1 assignment for pane 2, got %d", len(pane2))
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
	_, _ = store.Assign("bd-123", "Test bead", 1, "claude", "", "")

	// Invalid transition: completed -> assigned (terminal -> any).
	// `assigned -> completed` is now valid (#124) because beads can close
	// externally before the store ever observed a `working` transition.
	_ = store.UpdateStatus("bd-123", StatusCompleted)
	err := store.UpdateStatus("bd-123", StatusAssigned)
	if err == nil {
		t.Error("expected error for invalid transition completed->assigned, got nil")
	}
	if _, ok := err.(*InvalidTransitionError); !ok {
		t.Errorf("expected InvalidTransitionError, got %T", err)
	}

	// Status should remain completed
	a := store.Get("bd-123")
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

	newAssignment, err := store.Reassign("bd-123", 2, "codex", "Agent2")
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
	_, err = store.Reassign("bd-nonexistent", 2, "codex", "")
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
