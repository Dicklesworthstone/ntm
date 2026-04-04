package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewSpawnState(t *testing.T) {
	state := NewSpawnState("batch-123", 90, 3)

	if state.BatchID != "batch-123" {
		t.Errorf("expected BatchID 'batch-123', got %s", state.BatchID)
	}
	if state.StaggerSeconds != 90 {
		t.Errorf("expected StaggerSeconds 90, got %d", state.StaggerSeconds)
	}
	if state.TotalAgents != 3 {
		t.Errorf("expected TotalAgents 3, got %d", state.TotalAgents)
	}
	if state.StartedAt.IsZero() {
		t.Error("expected non-zero StartedAt")
	}
}

func TestSpawnStateAddPrompt(t *testing.T) {
	state := NewSpawnState("batch-123", 90, 3)
	scheduledAt := time.Now().Add(90 * time.Second)

	state.AddPrompt("proj__cc_1", "pane-1", 1, scheduledAt)

	if len(state.Prompts) != 1 {
		t.Fatalf("expected 1 prompt, got %d", len(state.Prompts))
	}

	p := state.Prompts[0]
	if p.Pane != "proj__cc_1" {
		t.Errorf("expected pane 'proj__cc_1', got %s", p.Pane)
	}
	if p.PaneID != "pane-1" {
		t.Errorf("expected pane ID 'pane-1', got %s", p.PaneID)
	}
	if p.Order != 1 {
		t.Errorf("expected order 1, got %d", p.Order)
	}
	if p.Sent {
		t.Error("expected sent to be false")
	}
}

func TestSpawnStateMarkSent(t *testing.T) {
	state := NewSpawnState("batch-123", 90, 2)
	now := time.Now()

	state.AddPrompt("proj__cc_1", "pane-1", 1, now)
	state.AddPrompt("proj__cc_2", "pane-2", 2, now.Add(90*time.Second))

	// Mark first prompt as sent
	state.MarkSent("pane-1")

	if !state.Prompts[0].Sent {
		t.Error("expected first prompt to be marked as sent")
	}
	if state.Prompts[0].SentAt.IsZero() {
		t.Error("expected SentAt to be set")
	}
	if state.Prompts[1].Sent {
		t.Error("expected second prompt to not be sent yet")
	}

	// Mark second prompt as sent - should complete the spawn
	state.MarkSent("pane-2")

	if !state.Prompts[1].Sent {
		t.Error("expected second prompt to be marked as sent")
	}
	if state.CompletedAt.IsZero() {
		t.Error("expected CompletedAt to be set when all prompts sent")
	}
}

func TestSpawnStatePendingCount(t *testing.T) {
	state := NewSpawnState("batch-123", 90, 3)
	now := time.Now()

	state.AddPrompt("proj__cc_1", "pane-1", 1, now)
	state.AddPrompt("proj__cc_2", "pane-2", 2, now.Add(90*time.Second))
	state.AddPrompt("proj__cc_3", "pane-3", 3, now.Add(180*time.Second))

	if state.PendingCount() != 3 {
		t.Errorf("expected 3 pending, got %d", state.PendingCount())
	}

	state.MarkSent("pane-1")
	if state.PendingCount() != 2 {
		t.Errorf("expected 2 pending, got %d", state.PendingCount())
	}

	state.MarkSent("pane-2")
	state.MarkSent("pane-3")
	if state.PendingCount() != 0 {
		t.Errorf("expected 0 pending, got %d", state.PendingCount())
	}
}

func TestSpawnStateSaveAndLoad(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	// Create and populate spawn state
	state := NewSpawnState("batch-test", 60, 2)
	now := time.Now()
	state.AddPrompt("proj__cc_1", "pane-1", 1, now)
	state.AddPrompt("proj__cc_2", "pane-2", 2, now.Add(60*time.Second))
	state.MarkSent("pane-1")

	// Save state
	if err := state.Save(tmpDir); err != nil {
		t.Fatalf("failed to save state: %v", err)
	}

	// Verify file exists
	path := filepath.Join(tmpDir, ".ntm", "spawn-state.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("spawn state file not created")
	}

	// Load state
	loaded, err := LoadSpawnState(tmpDir)
	if err != nil {
		t.Fatalf("failed to load state: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded state is nil")
	}

	// Verify loaded state
	if loaded.BatchID != "batch-test" {
		t.Errorf("expected BatchID 'batch-test', got %s", loaded.BatchID)
	}
	if loaded.StaggerSeconds != 60 {
		t.Errorf("expected StaggerSeconds 60, got %d", loaded.StaggerSeconds)
	}
	if len(loaded.Prompts) != 2 {
		t.Fatalf("expected 2 prompts, got %d", len(loaded.Prompts))
	}
	if !loaded.Prompts[0].Sent {
		t.Error("expected first prompt to be sent")
	}
	if loaded.Prompts[1].Sent {
		t.Error("expected second prompt to not be sent")
	}
}

func TestLoadSpawnStateNotExists(t *testing.T) {
	tmpDir := t.TempDir()

	state, err := LoadSpawnState(tmpDir)
	if err != nil {
		t.Errorf("expected no error for missing file, got %v", err)
	}
	if state != nil {
		t.Error("expected nil state for missing file")
	}
}

func TestLoadSpawnState_ExpiresCompletedStateAfterGracePeriod(t *testing.T) {
	tmpDir := t.TempDir()

	state := NewSpawnState("batch-test", 60, 1)
	state.MarkComplete()
	state.CompletedAt = time.Now().Add(-(spawnStateCompletionGracePeriod + time.Second))
	if err := state.Save(tmpDir); err != nil {
		t.Fatalf("failed to save expired state: %v", err)
	}

	loaded, err := LoadSpawnState(tmpDir)
	if err != nil {
		t.Fatalf("LoadSpawnState() error = %v", err)
	}
	if loaded != nil {
		t.Fatalf("LoadSpawnState() = %#v, want nil for expired state", loaded)
	}
	if SpawnStateExists(tmpDir) {
		t.Fatal("expected expired spawn state file to be removed")
	}
}

func TestClearSpawnState(t *testing.T) {
	tmpDir := t.TempDir()

	// Create state file
	state := NewSpawnState("batch-test", 60, 1)
	if err := state.Save(tmpDir); err != nil {
		t.Fatalf("failed to save state: %v", err)
	}

	// Verify file exists
	if !SpawnStateExists(tmpDir) {
		t.Fatal("spawn state should exist")
	}

	// Clear state
	if err := ClearSpawnState(tmpDir); err != nil {
		t.Fatalf("failed to clear state: %v", err)
	}

	// Verify file is gone
	if SpawnStateExists(tmpDir) {
		t.Error("spawn state should not exist after clear")
	}
}

func TestSpawnStateIsComplete(t *testing.T) {
	state := NewSpawnState("batch-test", 60, 1)
	state.AddPrompt("proj__cc_1", "pane-1", 1, time.Now())

	if state.IsComplete() {
		t.Error("expected incomplete before marking sent")
	}

	state.MarkSent("pane-1")

	if !state.IsComplete() {
		t.Error("expected complete after marking all sent")
	}
}

func TestSpawnStateMarkComplete(t *testing.T) {
	state := NewSpawnState("batch-test", 60, 2)
	state.AddPrompt("proj__cc_1", "pane-1", 1, time.Now())
	state.AddPrompt("proj__cc_2", "pane-2", 2, time.Now())

	if state.IsComplete() {
		t.Error("expected incomplete before MarkComplete")
	}

	state.MarkComplete()

	if !state.IsComplete() {
		t.Error("expected complete after MarkComplete")
	}
}

func TestTimeUntilNextPrompt(t *testing.T) {
	state := NewSpawnState("batch-test", 60, 2)
	now := time.Now()

	// All sent - should return 0
	state.AddPrompt("proj__cc_1", "pane-1", 1, now.Add(-10*time.Second)) // Already past
	state.AddPrompt("proj__cc_2", "pane-2", 2, now.Add(30*time.Second))  // 30s from now

	state.MarkSent("pane-1") // Mark first as sent

	// Second prompt is still pending, 30s from now
	remaining := state.TimeUntilNextPrompt()
	if remaining <= 0 || remaining > 31*time.Second {
		t.Errorf("expected remaining ~30s, got %v", remaining)
	}

	state.MarkSent("pane-2")

	// All sent - should return 0
	remaining = state.TimeUntilNextPrompt()
	if remaining != 0 {
		t.Errorf("expected 0 when all sent, got %v", remaining)
	}
}

func TestGetPromptStatuses(t *testing.T) {
	state := NewSpawnState("batch-test", 60, 2)
	now := time.Now()

	state.AddPrompt("proj__cc_1", "pane-1", 1, now)
	state.AddPrompt("proj__cc_2", "pane-2", 2, now.Add(60*time.Second))

	statuses := state.GetPromptStatuses()

	if len(statuses) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(statuses))
	}

	// Verify it's a copy
	state.MarkSent("pane-1")
	if statuses[0].Sent {
		t.Error("copy should not be affected by original changes")
	}
}
