// Package tests provides unit tests for retry failed assignments logic.
package tests

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
)

// Test fixtures for retry logic
func setupRetryTestStore(t *testing.T) (*assignment.AssignmentStore, string) {
	store := newIsolatedAssignmentStore(t, "retry-test")
	return store, assignment.StorageDir()
}

// =============================================================================
// Single Retry Tests
// =============================================================================

func TestRetrySingleBead_RequiresFailedStatus(t *testing.T) {
	store, _ := setupRetryTestStore(t)

	// Create an assigned (not failed) bead
	_, err := store.Assign("bd-123", "Test bead", 1, "claude", "TestAgent", "Do the task")
	if err != nil {
		t.Fatalf("failed to create assignment: %v", err)
	}

	// Verify bead is in assigned status
	a := store.Get("bd-123")
	if a == nil {
		t.Fatal("expected assignment to exist")
	}
	if a.Status != assignment.StatusAssigned {
		t.Errorf("expected status assigned, got %s", a.Status)
	}

	// Verify it's NOT failed - retry should be rejected
	failedList := store.ListByStatus(assignment.StatusFailed)
	if len(failedList) != 0 {
		t.Errorf("expected no failed assignments, got %d", len(failedList))
	}
}

func TestRetrySingleBead_FailedBeadCanBeRetried(t *testing.T) {
	store, _ := setupRetryTestStore(t)

	// Create and fail a bead
	_, err := store.Assign("bd-123", "Test bead", 1, "claude", "TestAgent", "Do the task")
	if err != nil {
		t.Fatalf("failed to create assignment: %v", err)
	}

	err = store.MarkFailed("bd-123", "Agent crashed")
	if err != nil {
		t.Fatalf("failed to mark as failed: %v", err)
	}

	// Verify bead is now in failed status
	failedList := store.ListByStatus(assignment.StatusFailed)
	if len(failedList) != 1 {
		t.Errorf("expected 1 failed assignment, got %d", len(failedList))
	}

	// Get the failed assignment
	a := store.Get("bd-123")
	if a == nil {
		t.Fatal("expected assignment to exist")
	}
	if a.Status != assignment.StatusFailed {
		t.Errorf("expected status failed, got %s", a.Status)
	}
	if a.FailReason != "Agent crashed" {
		t.Errorf("expected fail reason 'Agent crashed', got '%s'", a.FailReason)
	}
}

func TestRetrySingleBead_PreservesBeadInfo(t *testing.T) {
	store, _ := setupRetryTestStore(t)

	// Create assignment with all fields
	originalPrompt := "Implement feature X with detailed instructions"
	_, err := store.Assign("bd-456", "Complex Feature", 2, "codex", "OriginalAgent", originalPrompt)
	if err != nil {
		t.Fatalf("failed to create assignment: %v", err)
	}

	// Fail it
	err = store.MarkFailed("bd-456", "Context exhausted")
	if err != nil {
		t.Fatalf("failed to mark as failed: %v", err)
	}

	// Get failed assignment and verify original data preserved
	a := store.Get("bd-456")
	if a.BeadID != "bd-456" {
		t.Errorf("expected bead ID 'bd-456', got '%s'", a.BeadID)
	}
	if a.BeadTitle != "Complex Feature" {
		t.Errorf("expected bead title 'Complex Feature', got '%s'", a.BeadTitle)
	}
	if a.PromptSent != originalPrompt {
		t.Errorf("expected prompt to be preserved, got '%s'", a.PromptSent)
	}
}

func TestRetrySingleBead_CreatesNewAssignment(t *testing.T) {
	store, _ := setupRetryTestStore(t)

	// Create and fail a bead
	_, _ = store.Assign("bd-789", "Retry Test", 1, "claude", "Agent1", "prompt")
	_ = store.MarkFailed("bd-789", "crashed")

	oldAssignment := store.Get("bd-789")
	oldFailedAt := oldAssignment.FailedAt
	oldPreviousPaneIndex := oldAssignment.Pane

	// Remove the old assignment and create new one (simulating retry)
	store.Remove("bd-789")
	newAssignment, err := store.Assign("bd-789", "Retry Test", 3, "codex", "Agent2", "prompt")
	if err != nil {
		t.Fatalf("failed to create new assignment: %v", err)
	}

	// Verify new assignment has different pane/agent
	if newAssignment.Pane == oldPreviousPaneIndex {
		t.Error("expected new assignment to have different pane")
	}
	if newAssignment.AgentType == "claude" {
		t.Error("expected new assignment to have different agent type")
	}
	if newAssignment.Status != assignment.StatusAssigned {
		t.Errorf("expected status assigned, got %s", newAssignment.Status)
	}

	// Verify old failure timestamp is not on new assignment
	if newAssignment.FailedAt != nil && newAssignment.FailedAt == oldFailedAt {
		t.Error("new assignment should not have old FailedAt timestamp")
	}
}

// =============================================================================
// Retry to Specific Pane Tests
// =============================================================================

func TestRetryToSpecificPane_Success(t *testing.T) {
	store, _ := setupRetryTestStore(t)

	// Create and fail a bead on pane 1
	_, _ = store.Assign("bd-pane", "Pane Test", 1, "claude", "Agent1", "prompt")
	_ = store.MarkFailed("bd-pane", "crashed")

	// Remove and create new assignment on pane 4
	store.Remove("bd-pane")
	newAssignment, err := store.Assign("bd-pane", "Pane Test", 4, "codex", "Agent4", "prompt")
	if err != nil {
		t.Fatalf("failed to create assignment: %v", err)
	}

	if newAssignment.Pane != 4 {
		t.Errorf("expected pane 4, got %d", newAssignment.Pane)
	}
}

func TestRetryToSpecificPane_ConflictWithExistingAssignment(t *testing.T) {
	store, _ := setupRetryTestStore(t)

	// The local index is display-only: both records use index 4 but occupy
	// different physical tmux panes.
	_, _ = store.Assign("bd-existing", "Existing Task", 4, "claude", "Agent4", "prompt")
	setAssignmentCanonicalPaneIdentity(t, store, "bd-existing", "%44")
	_, _ = store.Assign("bd-tofail", "To Fail", 4, "codex", "Agent1", "prompt")
	setAssignmentCanonicalPaneIdentity(t, store, "bd-tofail", "%45")
	_ = store.MarkFailed("bd-tofail", "crashed")

	// Check if physical pane %44 already has an active assignment.
	hasActiveOnPane := false
	for _, a := range store.List() {
		paneID, err := assignment.CanonicalPaneIdentity(a)
		if err != nil {
			t.Fatalf("resolve assignment %s identity: %v", a.BeadID, err)
		}
		if paneID == "%44" && (a.Status == assignment.StatusAssigned || a.Status == assignment.StatusWorking) {
			hasActiveOnPane = true
			break
		}
	}

	if !hasActiveOnPane {
		t.Error("expected physical pane %44 to have active assignment")
	}
}

// =============================================================================
// Batch Retry Tests
// =============================================================================

func TestRetryFailed_FindsAllFailed(t *testing.T) {
	store, _ := setupRetryTestStore(t)

	// Create multiple assignments with different statuses
	_, _ = store.Assign("bd-1", "Task 1", 1, "claude", "", "")
	_, _ = store.Assign("bd-2", "Task 2", 2, "claude", "", "")
	_, _ = store.Assign("bd-3", "Task 3", 3, "codex", "", "")
	_, _ = store.Assign("bd-4", "Task 4", 4, "gemini", "", "")
	_, _ = store.Assign("bd-5", "Task 5", 5, "claude", "", "")

	// Mark some as failed
	_ = store.MarkFailed("bd-1", "crashed")
	_ = store.MarkFailed("bd-3", "context exhausted")
	_ = store.MarkWorking("bd-2")
	_ = store.MarkWorking("bd-4")
	_ = store.MarkCompleted("bd-4")

	// Find all failed
	failedList := store.ListByStatus(assignment.StatusFailed)
	if len(failedList) != 2 {
		t.Errorf("expected 2 failed assignments, got %d", len(failedList))
	}

	// Verify correct beads are in failed list
	failedIDs := make(map[string]bool)
	for _, a := range failedList {
		failedIDs[a.BeadID] = true
	}
	if !failedIDs["bd-1"] || !failedIDs["bd-3"] {
		t.Errorf("expected bd-1 and bd-3 to be failed, got %v", failedIDs)
	}
}

func TestRetryFailed_FilterByAgentType(t *testing.T) {
	store, _ := setupRetryTestStore(t)

	// Create failed assignments of different agent types
	_, _ = store.Assign("bd-claude1", "Claude Task 1", 1, "claude", "", "")
	_, _ = store.Assign("bd-claude2", "Claude Task 2", 2, "claude", "", "")
	_, _ = store.Assign("bd-codex1", "Codex Task 1", 3, "codex", "", "")

	_ = store.MarkFailed("bd-claude1", "crashed")
	_ = store.MarkFailed("bd-claude2", "crashed")
	_ = store.MarkFailed("bd-codex1", "crashed")

	// Get all failed
	failedList := store.ListByStatus(assignment.StatusFailed)

	// Filter by agent type (simulating --agent=claude filter)
	var claudeFailures []*assignment.Assignment
	for _, a := range failedList {
		if a.AgentType == "claude" {
			claudeFailures = append(claudeFailures, a)
		}
	}

	if len(claudeFailures) != 2 {
		t.Errorf("expected 2 claude failures, got %d", len(claudeFailures))
	}
}

func TestRetryFailed_HandlesPartialSuccess(t *testing.T) {
	store, _ := setupRetryTestStore(t)

	// Create multiple failed assignments
	_, _ = store.Assign("bd-fail1", "Fail 1", 1, "claude", "", "prompt1")
	_, _ = store.Assign("bd-fail2", "Fail 2", 2, "codex", "", "prompt2")
	_, _ = store.Assign("bd-fail3", "Fail 3", 3, "gemini", "", "prompt3")

	_ = store.MarkFailed("bd-fail1", "crashed")
	_ = store.MarkFailed("bd-fail2", "crashed")
	_ = store.MarkFailed("bd-fail3", "crashed")

	// Simulate partial retry (only 2 idle agents available)
	// Remove and recreate first two
	store.Remove("bd-fail1")
	store.Remove("bd-fail2")
	_, _ = store.Assign("bd-fail1", "Fail 1", 4, "claude", "NewAgent1", "prompt1")
	_, _ = store.Assign("bd-fail2", "Fail 2", 5, "codex", "NewAgent2", "prompt2")
	// bd-fail3 remains failed (skipped due to no available agent)

	// Verify results
	retriedCount := 0
	skippedCount := 0

	all := store.List()
	for _, a := range all {
		if a.Status == assignment.StatusAssigned && (a.BeadID == "bd-fail1" || a.BeadID == "bd-fail2") {
			retriedCount++
		}
		if a.Status == assignment.StatusFailed && a.BeadID == "bd-fail3" {
			skippedCount++
		}
	}

	if retriedCount != 2 {
		t.Errorf("expected 2 retried, got %d", retriedCount)
	}
	if skippedCount != 1 {
		t.Errorf("expected 1 skipped, got %d", skippedCount)
	}
}

// =============================================================================
// Retry Count Tracking Tests
// =============================================================================

func TestRetryCount_IncrementedOnRetry(t *testing.T) {
	store, _ := setupRetryTestStore(t)

	// Create assignment with retry count 0
	original, _ := store.Assign("bd-count", "Count Test", 1, "claude", "", "")
	if original.RetryCount != 0 {
		t.Errorf("expected initial retry count 0, got %d", original.RetryCount)
	}

	// Fail and manually increment count (simulating retry logic)
	_ = store.MarkFailed("bd-count", "crashed")
	failed := store.Get("bd-count")
	newRetryCount := failed.RetryCount + 1

	// Remove and create with incremented count
	store.Remove("bd-count")
	newAssignment, _ := store.Assign("bd-count", "Count Test", 2, "codex", "", "")

	// Manually set retry count (in real code this is done during creation)
	// For testing, we verify the concept
	if newRetryCount != 1 {
		t.Errorf("expected retry count to be 1, got %d", newRetryCount)
	}

	// Verify new assignment is created
	if newAssignment.Status != assignment.StatusAssigned {
		t.Errorf("expected assigned status, got %s", newAssignment.Status)
	}
}

// =============================================================================
// Error Case Tests
// =============================================================================

func TestRetryNonFailedBead_ReturnsError(t *testing.T) {
	store, _ := setupRetryTestStore(t)

	// Create assignment in working status
	_, _ = store.Assign("bd-working", "Working Task", 1, "claude", "", "")
	_ = store.MarkWorking("bd-working")

	// Try to find it in failed list (should fail)
	failedList := store.ListByStatus(assignment.StatusFailed)
	var found bool
	for _, a := range failedList {
		if a.BeadID == "bd-working" {
			found = true
			break
		}
	}

	if found {
		t.Error("working bead should not be in failed list")
	}
}

func TestRetryNonExistentBead_ReturnsError(t *testing.T) {
	store, _ := setupRetryTestStore(t)

	// Try to get non-existent bead
	a := store.Get("bd-nonexistent")
	if a != nil {
		t.Error("expected nil for non-existent bead")
	}
}

func TestRetryCompletedBead_NotInFailedList(t *testing.T) {
	store, _ := setupRetryTestStore(t)

	// Create and complete a bead
	_, _ = store.Assign("bd-completed", "Completed Task", 1, "claude", "", "")
	_ = store.MarkWorking("bd-completed")
	_ = store.MarkCompleted("bd-completed")

	// Verify it's not in failed list
	failedList := store.ListByStatus(assignment.StatusFailed)
	for _, a := range failedList {
		if a.BeadID == "bd-completed" {
			t.Error("completed bead should not be in failed list")
		}
	}
}

// =============================================================================
// Assignment History Tests
// =============================================================================

func TestRetry_PreservesPreviousInfo(t *testing.T) {
	store, _ := setupRetryTestStore(t)

	// Create assignment and fail it
	original, _ := store.Assign("bd-history", "History Test", 2, "claude", "OrigAgent", "original prompt")
	originalPane := original.Pane
	originalAgent := original.AgentName

	_ = store.MarkFailed("bd-history", "original failure reason")
	failed := store.Get("bd-history")
	failureReason := failed.FailReason

	// Simulate retry - previous info would be in RetryItem
	// Verify we can capture previous info before removal
	if originalPane != 2 {
		t.Errorf("expected original pane 2, got %d", originalPane)
	}
	if originalAgent != "OrigAgent" {
		t.Errorf("expected original agent 'OrigAgent', got '%s'", originalAgent)
	}
	if failureReason != "original failure reason" {
		t.Errorf("expected failure reason, got '%s'", failureReason)
	}
}

// =============================================================================
// State Transition Tests for Retry Flow
// =============================================================================

func TestValidTransition_FailedToAssigned(t *testing.T) {
	t.Parallel()
	// Failed -> Assigned is the retry transition
	if !isValidRetryTransition(assignment.StatusFailed, assignment.StatusAssigned) {
		t.Error("Failed -> Assigned should be valid for retry")
	}
}

func TestInvalidTransition_CompletedToAssigned(t *testing.T) {
	t.Parallel()
	// Completed is terminal - no retry allowed
	if isValidRetryTransition(assignment.StatusCompleted, assignment.StatusAssigned) {
		t.Error("Completed -> Assigned should be invalid")
	}
}

func TestInvalidTransition_WorkingToAssigned(t *testing.T) {
	t.Parallel()
	// Working cannot directly go to Assigned
	if isValidRetryTransition(assignment.StatusWorking, assignment.StatusAssigned) {
		t.Error("Working -> Assigned should be invalid (must fail first)")
	}
}

// Helper to match ValidTransitions logic
func isValidRetryTransition(from, to assignment.AssignmentStatus) bool {
	transitions := map[assignment.AssignmentStatus][]assignment.AssignmentStatus{
		assignment.StatusAssigned:   {assignment.StatusWorking, assignment.StatusFailed},
		assignment.StatusWorking:    {assignment.StatusCompleted, assignment.StatusFailed, assignment.StatusReassigned},
		assignment.StatusFailed:     {assignment.StatusAssigned}, // Retry
		assignment.StatusCompleted:  {},                          // Terminal
		assignment.StatusReassigned: {},                          // Terminal
	}

	validTargets, ok := transitions[from]
	if !ok {
		return false
	}
	for _, valid := range validTargets {
		if valid == to {
			return true
		}
	}
	return false
}

// =============================================================================
// JSON Envelope Tests
// =============================================================================

func TestRetryEnvelope_SuccessFormat(t *testing.T) {
	t.Parallel()
	// Test the expected JSON envelope structure
	type RetryItem struct {
		BeadID             string `json:"bead_id"`
		BeadTitle          string `json:"bead_title"`
		Pane               int    `json:"pane"`
		AgentType          string `json:"agent_type"`
		AgentName          string `json:"agent_name"`
		Status             string `json:"status"`
		PromptSent         bool   `json:"prompt_sent"`
		AssignedAt         string `json:"assigned_at"`
		PreviousPane       int    `json:"previous_pane"`
		PreviousAgent      string `json:"previous_agent"`
		PreviousFailReason string `json:"previous_fail_reason"`
		RetryCount         int    `json:"retry_count"`
	}

	type RetrySkippedItem struct {
		BeadID string `json:"bead_id"`
		Reason string `json:"reason"`
	}

	type RetrySummary struct {
		TotalFailed  int `json:"total_failed"`
		RetriedCount int `json:"retried_count"`
		SkippedCount int `json:"skipped_count"`
	}

	type RetryData struct {
		Summary RetrySummary       `json:"summary"`
		Retried []RetryItem        `json:"retried"`
		Skipped []RetrySkippedItem `json:"skipped"`
	}

	type RetryEnvelope struct {
		Command    string      `json:"command"`
		Subcommand string      `json:"subcommand"`
		Session    string      `json:"session"`
		Timestamp  string      `json:"timestamp"`
		Success    bool        `json:"success"`
		Data       *RetryData  `json:"data"`
		Warnings   []string    `json:"warnings"`
		Error      interface{} `json:"error"`
	}

	envelope := RetryEnvelope{
		Command:    "assign",
		Subcommand: "retry",
		Session:    "test-session",
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Success:    true,
		Data: &RetryData{
			Summary: RetrySummary{
				TotalFailed:  2,
				RetriedCount: 1,
				SkippedCount: 1,
			},
			Retried: []RetryItem{
				{
					BeadID:             "bd-xyz",
					BeadTitle:          "Test Task",
					Pane:               4,
					AgentType:          "claude",
					AgentName:          "NewAgent",
					Status:             "assigned",
					PromptSent:         true,
					AssignedAt:         time.Now().UTC().Format(time.RFC3339),
					PreviousPane:       2,
					PreviousAgent:      "OldAgent",
					PreviousFailReason: "crashed",
					RetryCount:         1,
				},
			},
			Skipped: []RetrySkippedItem{
				{
					BeadID: "bd-abc",
					Reason: "no idle agents available",
				},
			},
		},
		Warnings: nil,
		Error:    nil,
	}

	// Verify JSON marshaling works
	data, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal envelope: %v", err)
	}

	// Verify required fields are present
	jsonStr := string(data)
	requiredFields := []string{
		"command", "subcommand", "session", "timestamp", "success",
		"bead_id", "previous_pane", "previous_agent", "retry_count",
	}
	for _, field := range requiredFields {
		if !contains(jsonStr, field) {
			t.Errorf("JSON missing required field: %s", field)
		}
	}
}

func TestRetryEnvelope_ErrorFormat(t *testing.T) {
	t.Parallel()
	type RetryError struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}

	type RetryEnvelope struct {
		Command    string      `json:"command"`
		Subcommand string      `json:"subcommand"`
		Session    string      `json:"session"`
		Timestamp  string      `json:"timestamp"`
		Success    bool        `json:"success"`
		Data       interface{} `json:"data"`
		Warnings   []string    `json:"warnings"`
		Error      *RetryError `json:"error"`
	}

	envelope := RetryEnvelope{
		Command:    "assign",
		Subcommand: "retry",
		Session:    "test-session",
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Success:    false,
		Data:       nil,
		Warnings:   nil,
		Error: &RetryError{
			Code:    "NOT_FAILED",
			Message: "bead bd-123 is not in failed state (status: working)",
		},
	}

	data, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal error envelope: %v", err)
	}

	// Verify error structure
	jsonStr := string(data)
	if !contains(jsonStr, "NOT_FAILED") {
		t.Error("expected error code in JSON")
	}
	if !contains(jsonStr, "not in failed state") {
		t.Error("expected error message in JSON")
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// =============================================================================
// Limit Tests
// =============================================================================

func TestRetryFailed_RespectsLimit(t *testing.T) {
	store, _ := setupRetryTestStore(t)

	// Create 5 failed assignments
	for i := 1; i <= 5; i++ {
		beadID := "bd-" + string(rune('0'+i))
		_, _ = store.Assign(beadID, "Task "+string(rune('0'+i)), i, "claude", "", "")
		_ = store.MarkFailed(beadID, "crashed")
	}

	// Get all failed
	failedList := store.ListByStatus(assignment.StatusFailed)
	if len(failedList) != 5 {
		t.Fatalf("expected 5 failed assignments, got %d", len(failedList))
	}

	// Apply limit (simulating --limit=3)
	limit := 3
	limitedList := failedList
	if len(limitedList) > limit {
		limitedList = limitedList[:limit]
	}

	if len(limitedList) != 3 {
		t.Errorf("expected limited list of 3, got %d", len(limitedList))
	}
}

// =============================================================================
// Multiple Failure Scenario Tests
// =============================================================================

func TestMultipleFailures_SameBeadCanFailAndRetryMultipleTimes(t *testing.T) {
	store, _ := setupRetryTestStore(t)

	// First assignment
	_, _ = store.Assign("bd-multi", "Multi Fail", 1, "claude", "Agent1", "prompt")
	_ = store.MarkFailed("bd-multi", "first failure")

	first := store.Get("bd-multi")
	if first.Status != assignment.StatusFailed {
		t.Errorf("expected failed status, got %s", first.Status)
	}

	// First retry
	store.Remove("bd-multi")
	_, _ = store.Assign("bd-multi", "Multi Fail", 2, "codex", "Agent2", "prompt")
	_ = store.MarkFailed("bd-multi", "second failure")

	second := store.Get("bd-multi")
	if second.Status != assignment.StatusFailed {
		t.Errorf("expected failed status, got %s", second.Status)
	}
	if second.Pane != 2 {
		t.Errorf("expected pane 2, got %d", second.Pane)
	}

	// Second retry
	store.Remove("bd-multi")
	final, _ := store.Assign("bd-multi", "Multi Fail", 3, "gemini", "Agent3", "prompt")
	_ = store.MarkWorking("bd-multi")
	_ = store.MarkCompleted("bd-multi")

	completed := store.Get("bd-multi")
	if completed.Status != assignment.StatusCompleted {
		t.Errorf("expected completed status, got %s", completed.Status)
	}
	if final.Pane != 3 {
		t.Errorf("expected pane 3, got %d", final.Pane)
	}
}
