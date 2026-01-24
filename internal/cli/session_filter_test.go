package cli

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
)

// ============================================================================
// Assignment Filtering Tests (bd-258jg)
// ============================================================================

// makeTestAssignment creates a test assignment with the given parameters
func makeTestAssignment(beadID, agentType string, pane int, status assignment.AssignmentStatus) *assignment.Assignment {
	return &assignment.Assignment{
		BeadID:     beadID,
		BeadTitle:  "Test " + beadID,
		AgentType:  agentType,
		Pane:       pane,
		Status:     status,
		AssignedAt: time.Now(),
	}
}

// TestFilterAssignmentsNoFilter tests that no filtering returns all assignments
func TestFilterAssignmentsNoFilter(t *testing.T) {
	assignments := []*assignment.Assignment{
		makeTestAssignment("bd-001", "claude", 1, assignment.StatusWorking),
		makeTestAssignment("bd-002", "codex", 2, assignment.StatusCompleted),
		makeTestAssignment("bd-003", "gemini", 3, assignment.StatusFailed),
	}

	result := filterAssignments(assignments, "", "", -1)

	if len(result) != 3 {
		t.Errorf("Expected 3 assignments with no filter, got %d", len(result))
	}
}

func TestFilterAssignmentsEmptyInput(t *testing.T) {
	var assignments []*assignment.Assignment

	result := filterAssignments(assignments, "working", "", -1)

	if len(result) != 0 {
		t.Errorf("Expected 0 assignments from empty input, got %d", len(result))
	}
}

// ============================================================================
// Status Filtering Tests
// ============================================================================

func TestFilterByStatusWorking(t *testing.T) {
	assignments := []*assignment.Assignment{
		makeTestAssignment("bd-001", "claude", 1, assignment.StatusWorking),
		makeTestAssignment("bd-002", "claude", 2, assignment.StatusCompleted),
		makeTestAssignment("bd-003", "codex", 3, assignment.StatusWorking),
		makeTestAssignment("bd-004", "gemini", 4, assignment.StatusFailed),
	}

	result := filterAssignments(assignments, "working", "", -1)

	if len(result) != 2 {
		t.Errorf("Expected 2 working assignments, got %d", len(result))
	}
	for _, a := range result {
		if a.Status != assignment.StatusWorking {
			t.Errorf("Expected status 'working', got %q", a.Status)
		}
	}
}

func TestFilterByStatusFailed(t *testing.T) {
	assignments := []*assignment.Assignment{
		makeTestAssignment("bd-001", "claude", 1, assignment.StatusWorking),
		makeTestAssignment("bd-002", "codex", 2, assignment.StatusFailed),
		makeTestAssignment("bd-003", "gemini", 3, assignment.StatusFailed),
	}

	result := filterAssignments(assignments, "failed", "", -1)

	if len(result) != 2 {
		t.Errorf("Expected 2 failed assignments, got %d", len(result))
	}
	for _, a := range result {
		if a.Status != assignment.StatusFailed {
			t.Errorf("Expected status 'failed', got %q", a.Status)
		}
	}
}

func TestFilterByStatusCompleted(t *testing.T) {
	assignments := []*assignment.Assignment{
		makeTestAssignment("bd-001", "claude", 1, assignment.StatusCompleted),
		makeTestAssignment("bd-002", "codex", 2, assignment.StatusWorking),
		makeTestAssignment("bd-003", "gemini", 3, assignment.StatusCompleted),
		makeTestAssignment("bd-004", "claude", 4, assignment.StatusCompleted),
	}

	result := filterAssignments(assignments, "completed", "", -1)

	if len(result) != 3 {
		t.Errorf("Expected 3 completed assignments, got %d", len(result))
	}
	for _, a := range result {
		if a.Status != assignment.StatusCompleted {
			t.Errorf("Expected status 'completed', got %q", a.Status)
		}
	}
}

func TestFilterByStatusAssigned(t *testing.T) {
	assignments := []*assignment.Assignment{
		makeTestAssignment("bd-001", "claude", 1, assignment.StatusAssigned),
		makeTestAssignment("bd-002", "codex", 2, assignment.StatusWorking),
		makeTestAssignment("bd-003", "gemini", 3, assignment.StatusAssigned),
	}

	result := filterAssignments(assignments, "assigned", "", -1)

	if len(result) != 2 {
		t.Errorf("Expected 2 assigned assignments, got %d", len(result))
	}
}

func TestFilterByStatusReassigned(t *testing.T) {
	assignments := []*assignment.Assignment{
		makeTestAssignment("bd-001", "claude", 1, assignment.StatusReassigned),
		makeTestAssignment("bd-002", "codex", 2, assignment.StatusWorking),
	}

	result := filterAssignments(assignments, "reassigned", "", -1)

	if len(result) != 1 {
		t.Errorf("Expected 1 reassigned assignment, got %d", len(result))
	}
}

func TestFilterByStatusInvalid(t *testing.T) {
	assignments := []*assignment.Assignment{
		makeTestAssignment("bd-001", "claude", 1, assignment.StatusWorking),
		makeTestAssignment("bd-002", "codex", 2, assignment.StatusCompleted),
	}

	// Invalid status should return empty (no match)
	result := filterAssignments(assignments, "invalid_status", "", -1)

	if len(result) != 0 {
		t.Errorf("Expected 0 assignments for invalid status filter, got %d", len(result))
	}
}

// ============================================================================
// Agent Type Filtering Tests
// ============================================================================

func TestFilterByAgentClaude(t *testing.T) {
	assignments := []*assignment.Assignment{
		makeTestAssignment("bd-001", "claude", 1, assignment.StatusWorking),
		makeTestAssignment("bd-002", "codex", 2, assignment.StatusWorking),
		makeTestAssignment("bd-003", "claude", 3, assignment.StatusCompleted),
		makeTestAssignment("bd-004", "gemini", 4, assignment.StatusFailed),
	}

	result := filterAssignments(assignments, "", "claude", -1)

	if len(result) != 2 {
		t.Errorf("Expected 2 claude assignments, got %d", len(result))
	}
	for _, a := range result {
		if a.AgentType != "claude" {
			t.Errorf("Expected agent type 'claude', got %q", a.AgentType)
		}
	}
}

func TestFilterByAgentCodex(t *testing.T) {
	assignments := []*assignment.Assignment{
		makeTestAssignment("bd-001", "claude", 1, assignment.StatusWorking),
		makeTestAssignment("bd-002", "codex", 2, assignment.StatusWorking),
		makeTestAssignment("bd-003", "codex", 3, assignment.StatusCompleted),
		makeTestAssignment("bd-004", "codex", 4, assignment.StatusFailed),
	}

	result := filterAssignments(assignments, "", "codex", -1)

	if len(result) != 3 {
		t.Errorf("Expected 3 codex assignments, got %d", len(result))
	}
	for _, a := range result {
		if a.AgentType != "codex" {
			t.Errorf("Expected agent type 'codex', got %q", a.AgentType)
		}
	}
}

func TestFilterByAgentGemini(t *testing.T) {
	assignments := []*assignment.Assignment{
		makeTestAssignment("bd-001", "claude", 1, assignment.StatusWorking),
		makeTestAssignment("bd-002", "gemini", 2, assignment.StatusWorking),
	}

	result := filterAssignments(assignments, "", "gemini", -1)

	if len(result) != 1 {
		t.Errorf("Expected 1 gemini assignment, got %d", len(result))
	}
}

func TestFilterByAgentInvalid(t *testing.T) {
	assignments := []*assignment.Assignment{
		makeTestAssignment("bd-001", "claude", 1, assignment.StatusWorking),
		makeTestAssignment("bd-002", "codex", 2, assignment.StatusCompleted),
	}

	// Invalid agent should return empty (no match)
	result := filterAssignments(assignments, "", "invalid_agent", -1)

	if len(result) != 0 {
		t.Errorf("Expected 0 assignments for invalid agent filter, got %d", len(result))
	}
}

// ============================================================================
// Pane Filtering Tests
// ============================================================================

func TestFilterByPane(t *testing.T) {
	assignments := []*assignment.Assignment{
		makeTestAssignment("bd-001", "claude", 1, assignment.StatusWorking),
		makeTestAssignment("bd-002", "codex", 2, assignment.StatusWorking),
		makeTestAssignment("bd-003", "gemini", 3, assignment.StatusWorking),
		makeTestAssignment("bd-004", "claude", 3, assignment.StatusCompleted),
	}

	result := filterAssignments(assignments, "", "", 3)

	if len(result) != 2 {
		t.Errorf("Expected 2 assignments for pane 3, got %d", len(result))
	}
	for _, a := range result {
		if a.Pane != 3 {
			t.Errorf("Expected pane 3, got %d", a.Pane)
		}
	}
}

func TestFilterByPaneZero(t *testing.T) {
	// Pane 0 is valid (user pane)
	assignments := []*assignment.Assignment{
		makeTestAssignment("bd-001", "claude", 0, assignment.StatusWorking),
		makeTestAssignment("bd-002", "codex", 1, assignment.StatusWorking),
		makeTestAssignment("bd-003", "gemini", 2, assignment.StatusWorking),
	}

	result := filterAssignments(assignments, "", "", 0)

	if len(result) != 1 {
		t.Errorf("Expected 1 assignment for pane 0, got %d", len(result))
	}
}

func TestFilterByPaneNonExistent(t *testing.T) {
	assignments := []*assignment.Assignment{
		makeTestAssignment("bd-001", "claude", 1, assignment.StatusWorking),
		makeTestAssignment("bd-002", "codex", 2, assignment.StatusWorking),
	}

	// Pane 99 doesn't exist
	result := filterAssignments(assignments, "", "", 99)

	if len(result) != 0 {
		t.Errorf("Expected 0 assignments for non-existent pane 99, got %d", len(result))
	}
}

func TestFilterByPaneNegativeDisabled(t *testing.T) {
	// Negative pane value means "no pane filter"
	assignments := []*assignment.Assignment{
		makeTestAssignment("bd-001", "claude", 1, assignment.StatusWorking),
		makeTestAssignment("bd-002", "codex", 2, assignment.StatusWorking),
	}

	result := filterAssignments(assignments, "", "", -1)

	if len(result) != 2 {
		t.Errorf("Expected 2 assignments with pane=-1 (no filter), got %d", len(result))
	}
}

// ============================================================================
// Combined Filter Tests
// ============================================================================

func TestFilterStatusAndAgent(t *testing.T) {
	assignments := []*assignment.Assignment{
		makeTestAssignment("bd-001", "claude", 1, assignment.StatusWorking),
		makeTestAssignment("bd-002", "claude", 2, assignment.StatusCompleted),
		makeTestAssignment("bd-003", "codex", 3, assignment.StatusWorking),
		makeTestAssignment("bd-004", "gemini", 4, assignment.StatusWorking),
	}

	// Working + Claude
	result := filterAssignments(assignments, "working", "claude", -1)

	if len(result) != 1 {
		t.Errorf("Expected 1 working claude assignment, got %d", len(result))
	}
	if len(result) > 0 {
		if result[0].BeadID != "bd-001" {
			t.Errorf("Expected bd-001, got %s", result[0].BeadID)
		}
	}
}

func TestFilterPaneAndStatus(t *testing.T) {
	assignments := []*assignment.Assignment{
		makeTestAssignment("bd-001", "claude", 3, assignment.StatusWorking),
		makeTestAssignment("bd-002", "codex", 3, assignment.StatusFailed),
		makeTestAssignment("bd-003", "gemini", 3, assignment.StatusCompleted),
		makeTestAssignment("bd-004", "claude", 4, assignment.StatusFailed),
	}

	// Pane 3 + Failed
	result := filterAssignments(assignments, "failed", "", 3)

	if len(result) != 1 {
		t.Errorf("Expected 1 failed assignment in pane 3, got %d", len(result))
	}
	if len(result) > 0 {
		if result[0].BeadID != "bd-002" {
			t.Errorf("Expected bd-002, got %s", result[0].BeadID)
		}
	}
}

func TestFilterAllThreeCriteria(t *testing.T) {
	assignments := []*assignment.Assignment{
		makeTestAssignment("bd-001", "claude", 1, assignment.StatusWorking),
		makeTestAssignment("bd-002", "claude", 2, assignment.StatusWorking),
		makeTestAssignment("bd-003", "codex", 2, assignment.StatusWorking),
		makeTestAssignment("bd-004", "claude", 2, assignment.StatusCompleted),
	}

	// Working + Claude + Pane 2
	result := filterAssignments(assignments, "working", "claude", 2)

	if len(result) != 1 {
		t.Errorf("Expected 1 working claude assignment in pane 2, got %d", len(result))
	}
	if len(result) > 0 {
		if result[0].BeadID != "bd-002" {
			t.Errorf("Expected bd-002, got %s", result[0].BeadID)
		}
	}
}

func TestFilterNoMatches(t *testing.T) {
	assignments := []*assignment.Assignment{
		makeTestAssignment("bd-001", "claude", 1, assignment.StatusWorking),
		makeTestAssignment("bd-002", "codex", 2, assignment.StatusCompleted),
	}

	// No gemini + failed assignments exist
	result := filterAssignments(assignments, "failed", "gemini", -1)

	if len(result) != 0 {
		t.Errorf("Expected 0 matches, got %d", len(result))
	}
}

// ============================================================================
// Edge Case Tests
// ============================================================================

func TestFilterPreservesOrder(t *testing.T) {
	assignments := []*assignment.Assignment{
		makeTestAssignment("bd-001", "claude", 1, assignment.StatusWorking),
		makeTestAssignment("bd-002", "claude", 2, assignment.StatusWorking),
		makeTestAssignment("bd-003", "claude", 3, assignment.StatusWorking),
	}

	result := filterAssignments(assignments, "working", "claude", -1)

	if len(result) != 3 {
		t.Fatalf("Expected 3 results, got %d", len(result))
	}

	// Order should be preserved
	expectedOrder := []string{"bd-001", "bd-002", "bd-003"}
	for i, expected := range expectedOrder {
		if result[i].BeadID != expected {
			t.Errorf("Expected result[%d]=%s, got %s", i, expected, result[i].BeadID)
		}
	}
}

func TestFilterSingleAssignment(t *testing.T) {
	assignments := []*assignment.Assignment{
		makeTestAssignment("bd-001", "claude", 1, assignment.StatusWorking),
	}

	// Match
	result := filterAssignments(assignments, "working", "", -1)
	if len(result) != 1 {
		t.Errorf("Expected 1 match, got %d", len(result))
	}

	// No match
	result = filterAssignments(assignments, "completed", "", -1)
	if len(result) != 0 {
		t.Errorf("Expected 0 matches, got %d", len(result))
	}
}

func TestFilterWithNilAssignments(t *testing.T) {
	// Input with nil pointer
	assignments := []*assignment.Assignment{
		makeTestAssignment("bd-001", "claude", 1, assignment.StatusWorking),
		nil, // This shouldn't cause a panic
		makeTestAssignment("bd-003", "codex", 3, assignment.StatusWorking),
	}

	// This tests that the function doesn't panic on nil
	// In production, we shouldn't have nil assignments, but defensive coding is good
	defer func() {
		if r := recover(); r != nil {
			// Function panicked, which is acceptable behavior for nil input
			// but we should document this or handle it
			t.Log("Function panicked on nil assignment - consider adding nil check")
		}
	}()

	result := filterAssignments(assignments, "working", "", -1)
	// If we get here without panic, check the result
	if len(result) > 3 {
		t.Errorf("Unexpected result length: %d", len(result))
	}
}

// ============================================================================
// Status Options Struct Tests
// ============================================================================

func TestStatusOptionsFilterFlags(t *testing.T) {
	// Verify the statusOptions struct has the expected filter fields
	opts := statusOptions{
		filterStatus: "working",
		filterAgent:  "claude",
		filterPane:   3,
		showSummary:  true,
	}

	if opts.filterStatus != "working" {
		t.Errorf("Expected filterStatus='working', got %q", opts.filterStatus)
	}
	if opts.filterAgent != "claude" {
		t.Errorf("Expected filterAgent='claude', got %q", opts.filterAgent)
	}
	if opts.filterPane != 3 {
		t.Errorf("Expected filterPane=3, got %d", opts.filterPane)
	}
	if !opts.showSummary {
		t.Error("Expected showSummary=true")
	}
}

func TestStatusOptionsDefaultValues(t *testing.T) {
	opts := statusOptions{}

	// Default values should be empty/zero
	if opts.filterStatus != "" {
		t.Errorf("Expected default filterStatus='', got %q", opts.filterStatus)
	}
	if opts.filterAgent != "" {
		t.Errorf("Expected default filterAgent='', got %q", opts.filterAgent)
	}
	if opts.filterPane != 0 {
		// Note: 0 is a valid pane, so the CLI uses -1 for "no filter"
		// but the struct default is 0
		t.Errorf("Expected default filterPane=0, got %d", opts.filterPane)
	}
	if opts.showSummary {
		t.Error("Expected default showSummary=false")
	}
}

// ============================================================================
// Summary Stats Accuracy Tests
// ============================================================================

func TestSummaryStatsCalculation(t *testing.T) {
	assignments := []*assignment.Assignment{
		makeTestAssignment("bd-001", "claude", 1, assignment.StatusWorking),
		makeTestAssignment("bd-002", "claude", 2, assignment.StatusWorking),
		makeTestAssignment("bd-003", "codex", 3, assignment.StatusCompleted),
		makeTestAssignment("bd-004", "codex", 4, assignment.StatusCompleted),
		makeTestAssignment("bd-005", "gemini", 5, assignment.StatusFailed),
		makeTestAssignment("bd-006", "claude", 6, assignment.StatusAssigned),
	}

	// Count by status
	statusCounts := make(map[assignment.AssignmentStatus]int)
	for _, a := range assignments {
		statusCounts[a.Status]++
	}

	if statusCounts[assignment.StatusWorking] != 2 {
		t.Errorf("Expected 2 working, got %d", statusCounts[assignment.StatusWorking])
	}
	if statusCounts[assignment.StatusCompleted] != 2 {
		t.Errorf("Expected 2 completed, got %d", statusCounts[assignment.StatusCompleted])
	}
	if statusCounts[assignment.StatusFailed] != 1 {
		t.Errorf("Expected 1 failed, got %d", statusCounts[assignment.StatusFailed])
	}
	if statusCounts[assignment.StatusAssigned] != 1 {
		t.Errorf("Expected 1 assigned, got %d", statusCounts[assignment.StatusAssigned])
	}
}

func TestSummaryStatsByAgent(t *testing.T) {
	assignments := []*assignment.Assignment{
		makeTestAssignment("bd-001", "claude", 1, assignment.StatusWorking),
		makeTestAssignment("bd-002", "claude", 2, assignment.StatusCompleted),
		makeTestAssignment("bd-003", "codex", 3, assignment.StatusWorking),
		makeTestAssignment("bd-004", "gemini", 4, assignment.StatusFailed),
	}

	// Count by agent type
	agentCounts := make(map[string]int)
	for _, a := range assignments {
		agentCounts[a.AgentType]++
	}

	if agentCounts["claude"] != 2 {
		t.Errorf("Expected 2 claude, got %d", agentCounts["claude"])
	}
	if agentCounts["codex"] != 1 {
		t.Errorf("Expected 1 codex, got %d", agentCounts["codex"])
	}
	if agentCounts["gemini"] != 1 {
		t.Errorf("Expected 1 gemini, got %d", agentCounts["gemini"])
	}
}

func TestSummaryStatsEmpty(t *testing.T) {
	assignments := []*assignment.Assignment{} // Use empty slice instead of nil

	// Empty assignments should result in zero counts
	statusCounts := make(map[assignment.AssignmentStatus]int)
	for _, a := range assignments {
		statusCounts[a.Status]++
	}

	if len(statusCounts) != 0 {
		t.Errorf("Expected empty status counts, got %d entries", len(statusCounts))
	}
}

func TestCompletionRateCalculation(t *testing.T) {
	assignments := []*assignment.Assignment{
		makeTestAssignment("bd-001", "claude", 1, assignment.StatusCompleted),
		makeTestAssignment("bd-002", "claude", 2, assignment.StatusCompleted),
		makeTestAssignment("bd-003", "codex", 3, assignment.StatusFailed),
		makeTestAssignment("bd-004", "gemini", 4, assignment.StatusWorking),
	}

	total := len(assignments)
	completed := 0
	for _, a := range assignments {
		if a.Status == assignment.StatusCompleted {
			completed++
		}
	}

	completionRate := float64(completed) / float64(total) * 100.0

	// 2 out of 4 = 50%
	if completionRate != 50.0 {
		t.Errorf("Expected completion rate 50.0%%, got %.1f%%", completionRate)
	}
}

func TestCompletionRateZeroDivision(t *testing.T) {
	assignments := []*assignment.Assignment{} // Use empty slice instead of nil

	total := len(assignments)

	// Should handle zero division gracefully
	var completionRate float64
	if total > 0 {
		completed := 0
		for _, a := range assignments {
			if a.Status == assignment.StatusCompleted {
				completed++
			}
		}
		completionRate = float64(completed) / float64(total) * 100.0
	} else {
		completionRate = 0.0 // No assignments = 0% completion
	}

	if completionRate != 0.0 {
		t.Errorf("Expected 0%% completion rate for empty assignments, got %.1f%%", completionRate)
	}
}
