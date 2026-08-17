package robot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	assignmentstore "github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// =============================================================================
// Task Type Inference Tests
// =============================================================================

func TestAssignAgentsFromObservationFailsClosedOnCaptureFailureAndStalePane(t *testing.T) {
	now := time.Now().UTC()
	observation := statuspkg.SessionObservation{
		Session: "proj", ObservedAt: now, Complete: false,
		Panes: []statuspkg.PaneObservation{
			{
				Pane: tmux.PaneRef{ID: "%41", WindowIndex: 0, PaneIndex: 1}, PaneName: "Codex One", AgentType: "codex",
				Current: statuspkg.StateObservation{
					Status: statuspkg.AgentStatus{State: statuspkg.StateIdle}, ObservedAt: now,
					Freshness: statuspkg.FreshnessFresh, Confidence: 0.95,
				},
			},
			{
				Pane: tmux.PaneRef{ID: "%42", WindowIndex: 1, PaneIndex: 1}, PaneName: "Codex Two", AgentType: "codex",
				Current: statuspkg.StateObservation{
					Status: statuspkg.AgentStatus{State: statuspkg.StateUnknown}, ObservedAt: now,
					Freshness: statuspkg.FreshnessUnavailable, Error: "capture failed",
				},
				LastKnown: &statuspkg.StateObservation{
					Status: statuspkg.AgentStatus{State: statuspkg.StateIdle}, ObservedAt: now.Add(-time.Minute),
					Freshness: statuspkg.FreshnessStale, Confidence: 0.95,
				},
			},
			{
				Pane: tmux.PaneRef{ID: "%43", WindowIndex: 2, PaneIndex: 1}, PaneName: "Codex Three", AgentType: "codex",
				Current: statuspkg.StateObservation{
					Status: statuspkg.AgentStatus{State: statuspkg.StateIdle}, ObservedAt: now.Add(-statuspkg.DispatchObservationMaxAge - time.Second),
					Freshness: statuspkg.FreshnessFresh, Confidence: 0.95,
				},
			},
		},
	}

	agents, idle, err := assignAgentsFromObservation(observation, now)
	if err != nil {
		t.Fatalf("assignAgentsFromObservation: %v", err)
	}
	if len(agents) != 3 || !reflect.DeepEqual(idle, []string{"%41"}) {
		t.Fatalf("agents=%+v idle=%v, want only fresh pane idle", agents, idle)
	}
	if agents[1].state != string(statuspkg.StateUnknown) {
		t.Fatalf("capture-failed pane state=%q, want unknown", agents[1].state)
	}
}

func TestAssignAgentsFromObservationRejectsStaleSessionSnapshot(t *testing.T) {
	now := time.Now().UTC()
	observation := statuspkg.SessionObservation{
		Session: "proj", ObservedAt: now.Add(-statuspkg.DispatchObservationMaxAge - time.Second),
	}
	agents, idle, err := assignAgentsFromObservation(observation, now)
	if err == nil || !strings.Contains(err.Error(), "stale") || agents != nil || idle != nil {
		t.Fatalf("stale observation agents=%+v idle=%v error=%v", agents, idle, err)
	}
}

func TestFilterDurablyOccupiedAssignAgentsUsesPaneIDAcrossDuplicateLocalIndexes(t *testing.T) {
	idle := []string{"%41", "%42", "%43"}
	active := []*assignmentstore.Assignment{{
		BeadID: "ntm-window-one", Pane: 1, OccupancyKey: "%42", DispatchTarget: "%42",
	}}
	available, err := filterDurablyOccupiedAssignAgents(idle, active)
	if err != nil || !reflect.DeepEqual(available, []string{"%41", "%43"}) {
		t.Fatalf("available=%v error=%v, want exact occupied pane removed", available, err)
	}

	available, err = filterDurablyOccupiedAssignAgents(idle, []*assignmentstore.Assignment{{BeadID: "legacy", Pane: 1}})
	if !errors.Is(err, assignmentstore.ErrPaneIdentityMigrationRequired) || available != nil {
		t.Fatalf("legacy available=%v error=%v, want typed migration error", available, err)
	}
}

func TestInferTaskType_BugKeywords(t *testing.T) {
	tests := []struct {
		title    string
		expected string
	}{
		{"Fix authentication bug", "bug"},
		{"Bug: login fails on retry", "bug"},
		{"broken pagination in status", "bug"},
		{"Error handling in tmux client", "bug"},
		{"Crash on empty session name", "bug"},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			bead := bv.BeadPreview{ID: "bd-test", Title: tt.title, Priority: "P1"}
			got := inferTaskType(bead)
			if got != tt.expected {
				t.Errorf("inferTaskType(%q) = %q, want %q", tt.title, got, tt.expected)
			}
		})
	}
}

func TestInferTaskType_TestKeywords(t *testing.T) {
	tests := []struct {
		title    string
		expected string
	}{
		{"Write unit tests for assign module", "testing"},
		{"Add test coverage for robot pkg", "testing"},
		{"Spec: robot env output format", "testing"},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			bead := bv.BeadPreview{ID: "bd-test", Title: tt.title, Priority: "P2"}
			got := inferTaskType(bead)
			if got != tt.expected {
				t.Errorf("inferTaskType(%q) = %q, want %q", tt.title, got, tt.expected)
			}
		})
	}
}

func TestInferTaskType_FeatureKeywords(t *testing.T) {
	tests := []struct {
		title    string
		expected string
	}{
		{"Feature: Add robot-env command", "feature"},
		{"Implement bead assignment engine", "feature"},
		{"Add tab completion support", "feature"},
		{"New ensemble mode catalog", "feature"},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			bead := bv.BeadPreview{ID: "bd-test", Title: tt.title, Priority: "P2"}
			got := inferTaskType(bead)
			if got != tt.expected {
				t.Errorf("inferTaskType(%q) = %q, want %q", tt.title, got, tt.expected)
			}
		})
	}
}

func TestInferTaskType_RefactorKeywords(t *testing.T) {
	tests := []struct {
		title    string
		expected string
	}{
		{"Refactor robot output types", "refactor"},
		{"Cleanup unused helpers in tmux pkg", "refactor"},
		{"Improve code structure patterns", "refactor"},
		{"Consolidate send command logic", "refactor"},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			bead := bv.BeadPreview{ID: "bd-test", Title: tt.title, Priority: "P2"}
			got := inferTaskType(bead)
			if got != tt.expected {
				t.Errorf("inferTaskType(%q) = %q, want %q", tt.title, got, tt.expected)
			}
		})
	}
}

func TestInferTaskType_DocumentationKeywords(t *testing.T) {
	tests := []struct {
		title    string
		expected string
	}{
		{"Update README with ensemble docs", "documentation"},
		{"Add documentation for robot flags", "documentation"},
		{"Comment complex assignment logic", "documentation"},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			bead := bv.BeadPreview{ID: "bd-test", Title: tt.title, Priority: "P3"}
			got := inferTaskType(bead)
			if got != tt.expected {
				t.Errorf("inferTaskType(%q) = %q, want %q", tt.title, got, tt.expected)
			}
		})
	}
}

func TestInferTaskType_AnalysisKeywords(t *testing.T) {
	tests := []struct {
		title    string
		expected string
	}{
		{"Analyze rate limit patterns", "analysis"},
		{"Investigate CI failure", "analysis"},
		{"Research WebSocket alternatives", "analysis"},
		{"Design plugin architecture", "analysis"},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			bead := bv.BeadPreview{ID: "bd-test", Title: tt.title, Priority: "P2"}
			got := inferTaskType(bead)
			if got != tt.expected {
				t.Errorf("inferTaskType(%q) = %q, want %q", tt.title, got, tt.expected)
			}
		})
	}
}

func TestInferTaskType_Default(t *testing.T) {
	// Titles with no matching keywords should default to "task"
	tests := []string{
		"EPIC: Something",
		"Phase 2 milestone",
		"Integration with external service",
		"",
	}

	for _, title := range tests {
		t.Run(title, func(t *testing.T) {
			bead := bv.BeadPreview{ID: "bd-test", Title: title, Priority: "P2"}
			got := inferTaskType(bead)
			if got != "task" {
				t.Errorf("inferTaskType(%q) = %q, want %q", title, got, "task")
			}
		})
	}
}

func TestInferTaskType_PriorityOrder(t *testing.T) {
	// "Fix" matches bug before feature ("fix" contains no feature keyword)
	// "Fix and add test coverage" should match "bug" because "fix" comes first in the rules
	bead := bv.BeadPreview{ID: "bd-test", Title: "Fix and add test coverage", Priority: "P1"}
	got := inferTaskType(bead)
	if got != "bug" {
		t.Errorf("inferTaskType('Fix and add test coverage') = %q, want %q (bug should match before testing)", got, "bug")
	}
}

func TestInferTaskType_CaseInsensitive(t *testing.T) {
	bead := bv.BeadPreview{ID: "bd-test", Title: "BUG: Session Not Found", Priority: "P1"}
	got := inferTaskType(bead)
	if got != "bug" {
		t.Errorf("inferTaskType should be case-insensitive, got %q", got)
	}
}

// =============================================================================
// Priority Parsing Tests
// =============================================================================

func TestParsePriority_Assign(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"P0", 0},
		{"P1", 1},
		{"P2", 2},
		{"P3", 3},
		{"P4", 4},
		{"P5", 2},  // Out of range, defaults to 2
		{"P9", 2},  // Out of range
		{"", 2},    // Empty
		{"Q1", 2},  // Wrong prefix
		{"p1", 2},  // Wrong case (lowercase p)
		{"PP", 2},  // Malformed
		{"P", 2},   // Too short
		{"P12", 2}, // Too long
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parsePriority(tt.input)
			if got != tt.expected {
				t.Errorf("parsePriority(%q) = %d, want %d", tt.input, got, tt.expected)
			}
		})
	}
}

// =============================================================================
// Confidence Calculation Tests
// =============================================================================

func TestCalculateConfidence_SimpleIsCapabilityScore(t *testing.T) {
	bead := bv.BeadPreview{ID: "bd-test", Title: "Fix login bug", Priority: "P1"}
	conf := calculateConfidence("claude", bead)

	if conf < 0 || conf > 1.0 {
		t.Errorf("confidence %f should be between 0.0 and 1.0", conf)
	}
	// Simple strategy confidence is the raw capability score, no adjustments.
	if want := AgentStrength("claude", "bug"); conf != want {
		t.Errorf("confidence %f should equal capability score %f", conf, want)
	}
}

func TestCalculateConfidence_PriorityDoesNotAdjustSimple(t *testing.T) {
	// Simple strategy applies no strategy scoring: priority never shifts confidence.
	p0Bead := bv.BeadPreview{ID: "bd-test", Title: "Some task", Priority: "P0"}
	p3Bead := bv.BeadPreview{ID: "bd-test", Title: "Some task", Priority: "P3"}

	if p0 := calculateConfidence("claude", p0Bead); p0 != calculateConfidence("claude", p3Bead) {
		t.Errorf("simple confidence should ignore priority, P0 gave %f", p0)
	}
}

// =============================================================================
// Reasoning Generation Tests
// =============================================================================

func TestGenerateReasoning_HighStrengthAgent(t *testing.T) {
	bead := bv.BeadPreview{ID: "bd-test", Title: "Fix login bug", Priority: "P0"}
	reasoning := generateReasoning("claude", bead, "balanced")

	if reasoning == "" {
		t.Error("reasoning should not be empty")
	}

	// Should contain strategy reasoning
	if !strings.Contains(reasoning, "balanced") {
		t.Errorf("reasoning %q should mention balanced strategy", reasoning)
	}

	// P0 should get "critical priority" reasoning
	if !strings.Contains(reasoning, "critical priority") {
		t.Errorf("reasoning %q should mention critical priority for P0 bead", reasoning)
	}
}

func TestGenerateReasoning_P1Priority(t *testing.T) {
	bead := bv.BeadPreview{ID: "bd-test", Title: "Some task", Priority: "P1"}
	reasoning := generateReasoning("claude", bead, "speed")

	if !strings.Contains(reasoning, "high priority") {
		t.Errorf("reasoning %q should mention high priority for P1", reasoning)
	}
	if !strings.Contains(reasoning, "speed") {
		t.Errorf("reasoning %q should mention speed strategy", reasoning)
	}
}

func TestGenerateReasoning_AllStrategies(t *testing.T) {
	bead := bv.BeadPreview{ID: "bd-test", Title: "Some task", Priority: "P2"}
	strategies := []string{"balanced", "speed", "quality", "dependency"}

	for _, strategy := range strategies {
		t.Run(strategy, func(t *testing.T) {
			reasoning := generateReasoning("claude", bead, strategy)
			if reasoning == "" {
				t.Error("reasoning should not be empty")
			}
			if !strings.Contains(reasoning, strategy) {
				t.Errorf("reasoning %q should mention strategy %q", reasoning, strategy)
			}
		})
	}
}

func TestGenerateReasoning_DefaultFallback(t *testing.T) {
	// P2 bead with unknown strategy should still produce output
	bead := bv.BeadPreview{ID: "bd-test", Title: "Integration work", Priority: "P2"}
	reasoning := generateReasoning("claude", bead, "unknown")

	if reasoning == "" {
		t.Error("reasoning should not be empty even with unknown strategy")
	}
}

// =============================================================================
// Agent Hints Generation Tests
// =============================================================================

func TestGenerateAssignHints_NoWork(t *testing.T) {
	hints := generateAssignHints("test", nil, nil, nil, nil)

	if hints.Summary != "No work available to assign" {
		t.Errorf("Summary = %q, want %q", hints.Summary, "No work available to assign")
	}
}

func TestGenerateAssignHints_NoIdleAgents(t *testing.T) {
	beads := []bv.BeadPreview{{ID: "bd-1", Title: "Task 1", Priority: "P1"}}
	hints := generateAssignHints("test", nil, nil, beads, nil)

	if !strings.Contains(hints.Summary, "no idle agents") {
		t.Errorf("Summary %q should mention no idle agents", hints.Summary)
	}
}

func TestGenerateAssignHints_WithRecommendations(t *testing.T) {
	recs := []AssignRecommend{
		{PaneID: "%12", PaneTarget: "0.2", AssignBead: "bd-abc", AgentType: "claude"},
		{PaneID: "%23", PaneTarget: "1.3", AssignBead: "bd-def", AgentType: "codex"},
	}
	idleAgents := []string{"%12", "%23"}
	beads := []bv.BeadPreview{
		{ID: "bd-abc", Title: "Task A"},
		{ID: "bd-def", Title: "Task B"},
	}

	hints := generateAssignHints("test", recs, idleAgents, beads, nil)

	if !strings.Contains(hints.Summary, "2 assignments") {
		t.Errorf("Summary %q should mention 2 assignments", hints.Summary)
	}
	if len(hints.SuggestedCommands) != 2 {
		t.Errorf("SuggestedCommands count = %d, want 2", len(hints.SuggestedCommands))
	}
}

func TestGenerateAssignHints_MoreBeadsThanAgents(t *testing.T) {
	recs := []AssignRecommend{{PaneID: "%12", PaneTarget: "0.2", AssignBead: "bd-abc"}}
	idleAgents := []string{"%12"}
	beads := []bv.BeadPreview{
		{ID: "bd-abc", Title: "A"},
		{ID: "bd-def", Title: "B"},
		{ID: "bd-ghi", Title: "C"},
	}

	hints := generateAssignHints("test", recs, idleAgents, beads, nil)

	if len(hints.Warnings) == 0 {
		t.Error("should warn about unassigned beads")
	}
	found := false
	for _, w := range hints.Warnings {
		if strings.Contains(w, "won't be assigned") {
			found = true
			break
		}
	}
	if !found {
		t.Error("warnings should mention beads that won't be assigned")
	}
}

func TestGenerateAssignHints_StaleInProgress(t *testing.T) {
	inProgress := []bv.BeadInProgress{
		{ID: "bd-old", Title: "Stale task", UpdatedAt: time.Now().Add(-48 * time.Hour)},
		{ID: "bd-new", Title: "Fresh task", UpdatedAt: time.Now().Add(-1 * time.Hour)},
	}

	hints := generateAssignHints("test", nil, nil, nil, inProgress)

	found := false
	for _, w := range hints.Warnings {
		if strings.Contains(w, "stale") {
			found = true
			break
		}
	}
	if !found {
		t.Error("should warn about stale in-progress beads")
	}
}

// =============================================================================
// Assignment Generation Tests
// =============================================================================

func TestGenerateAssignments_BasicFlow(t *testing.T) {
	agents := []assignAgentInfo{
		{paneID: "%12", paneTarget: "0.2", agentType: "claude", model: "opus", state: "idle"},
		{paneID: "%22", paneTarget: "1.2", agentType: "codex", model: "gpt4", state: "idle"},
	}
	beads := []bv.BeadPreview{
		{ID: "bd-abc", Title: "Fix bug", Priority: "P1"},
		{ID: "bd-def", Title: "Add feature", Priority: "P2"},
	}
	idleAgents := []string{"%12", "%22"}

	recs := generateAssignments(agents, beads, "balanced", idleAgents)

	if len(recs) != 2 {
		t.Fatalf("expected 2 recommendations, got %d", len(recs))
	}

	if recs[0].AssignBead != "bd-abc" {
		t.Errorf("first recommendation bead = %q, want %q", recs[0].AssignBead, "bd-abc")
	}
	if recs[0].PaneID != "%12" || recs[0].PaneTarget != "0.2" {
		t.Errorf("first recommendation pane = %s (%s), want 0.2 (%%12)", recs[0].PaneTarget, recs[0].PaneID)
	}
	if recs[1].AssignBead != "bd-def" {
		t.Errorf("second recommendation bead = %q, want %q", recs[1].AssignBead, "bd-def")
	}
	if recs[1].PaneID != "%22" || recs[1].PaneTarget != "1.2" {
		t.Errorf("second recommendation pane = %s (%s), want 1.2 (%%22)", recs[1].PaneTarget, recs[1].PaneID)
	}
}

func TestGenerateAssignments_MoreAgentsThanBeads(t *testing.T) {
	agents := []assignAgentInfo{
		{paneID: "%12", paneTarget: "0.2", agentType: "claude", state: "idle"},
		{paneID: "%13", paneTarget: "0.3", agentType: "codex", state: "idle"},
		{paneID: "%14", paneTarget: "0.4", agentType: "gemini", state: "idle"},
	}
	beads := []bv.BeadPreview{
		{ID: "bd-abc", Title: "Fix bug", Priority: "P1"},
	}
	idleAgents := []string{"%12", "%13", "%14"}

	recs := generateAssignments(agents, beads, "balanced", idleAgents)

	if len(recs) != 1 {
		t.Fatalf("expected 1 recommendation (only 1 bead), got %d", len(recs))
	}
}

func TestGenerateAssignments_MoreBeadsThanAgents(t *testing.T) {
	agents := []assignAgentInfo{
		{paneID: "%12", paneTarget: "0.2", agentType: "claude", state: "idle"},
	}
	beads := []bv.BeadPreview{
		{ID: "bd-abc", Title: "Task A", Priority: "P1"},
		{ID: "bd-def", Title: "Task B", Priority: "P2"},
		{ID: "bd-ghi", Title: "Task C", Priority: "P3"},
	}
	idleAgents := []string{"%12"}

	recs := generateAssignments(agents, beads, "balanced", idleAgents)

	if len(recs) != 1 {
		t.Fatalf("expected 1 recommendation (only 1 idle agent), got %d", len(recs))
	}
	if recs[0].AssignBead != "bd-abc" {
		t.Errorf("should assign first bead, got %q", recs[0].AssignBead)
	}
}

func TestGenerateAssignments_NoIdleAgents(t *testing.T) {
	agents := []assignAgentInfo{
		{paneID: "%12", paneTarget: "0.2", agentType: "claude", state: "working"},
	}
	beads := []bv.BeadPreview{
		{ID: "bd-abc", Title: "Task A", Priority: "P1"},
	}
	idleAgents := []string{} // None idle

	recs := generateAssignments(agents, beads, "balanced", idleAgents)

	if len(recs) != 0 {
		t.Errorf("expected 0 recommendations with no idle agents, got %d", len(recs))
	}
}

func TestGenerateAssignments_NoBeads(t *testing.T) {
	agents := []assignAgentInfo{
		{paneID: "%12", paneTarget: "0.2", agentType: "claude", state: "idle"},
	}
	idleAgents := []string{"%12"}

	recs := generateAssignments(agents, nil, "balanced", idleAgents)

	if len(recs) != 0 {
		t.Errorf("expected 0 recommendations with no beads, got %d", len(recs))
	}
}

func TestGenerateAssignments_RecommendationFields(t *testing.T) {
	agents := []assignAgentInfo{
		{paneID: "%25", paneTarget: "1.5", agentType: "claude", model: "opus-4.5", state: "idle"},
	}
	beads := []bv.BeadPreview{
		{ID: "bd-xyz", Title: "Fix critical bug", Priority: "P0"},
	}
	idleAgents := []string{"%25"}

	recs := generateAssignments(agents, beads, "quality", idleAgents)

	if len(recs) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(recs))
	}

	rec := recs[0]
	if rec.PaneID != "%25" || rec.PaneTarget != "1.5" {
		t.Errorf("pane = %s (%s), want 1.5 (%%25)", rec.PaneTarget, rec.PaneID)
	}
	if rec.AgentType != "claude" {
		t.Errorf("AgentType = %q, want %q", rec.AgentType, "claude")
	}
	if rec.Model != "opus-4.5" {
		t.Errorf("Model = %q, want %q", rec.Model, "opus-4.5")
	}
	if rec.AssignBead != "bd-xyz" {
		t.Errorf("AssignBead = %q, want %q", rec.AssignBead, "bd-xyz")
	}
	if rec.BeadTitle != "Fix critical bug" {
		t.Errorf("BeadTitle = %q, want %q", rec.BeadTitle, "Fix critical bug")
	}
	if rec.Priority != "P0" {
		t.Errorf("Priority = %q, want %q", rec.Priority, "P0")
	}
	if rec.Confidence <= 0 || rec.Confidence > 1.0 {
		t.Errorf("Confidence = %f, should be (0, 1.0]", rec.Confidence)
	}
	if rec.Reasoning == "" {
		t.Error("Reasoning should not be empty")
	}
}

func TestResolveAssignBlockedBeadsPreservesDependencies(t *testing.T) {
	blocked, err := resolveAssignBlockedBeads(t.Context(), "/project", []bv.BeadPreview{
		{ID: "bd-child", Title: "Child task"},
	}, func(_ context.Context, projectDir, beadID string) (*bv.BeadAssignmentDetails, error) {
		if projectDir != "/project" || beadID != "bd-child" {
			t.Fatalf("detail lookup = (%q, %q), want (/project, bd-child)", projectDir, beadID)
		}
		return &bv.BeadAssignmentDetails{
			ID:        beadID,
			Title:     "Canonical child task",
			BlockedBy: []string{"bd-parent", "bd-prerequisite"},
		}, nil
	})
	if err != nil {
		t.Fatalf("resolveAssignBlockedBeads: %v", err)
	}
	if want := []BlockedBead{{
		ID: "bd-child", Title: "Canonical child task", BlockedBy: []string{"bd-parent", "bd-prerequisite"},
	}}; !reflect.DeepEqual(blocked, want) {
		t.Fatalf("blocked beads = %+v, want %+v", blocked, want)
	}
}

func TestResolveAssignBlockedBeadsReturnsDetailErrors(t *testing.T) {
	_, err := resolveAssignBlockedBeads(t.Context(), "/project", []bv.BeadPreview{{ID: "bd-child"}}, func(context.Context, string, string) (*bv.BeadAssignmentDetails, error) {
		return nil, errors.New("database unavailable")
	})
	if err == nil || !strings.Contains(err.Error(), "bd-child") || !strings.Contains(err.Error(), "database unavailable") {
		t.Fatalf("detail error = %v, want contextual lookup error", err)
	}
}

func TestClassifyAssignableActionableRecommendationsExplainsExclusions(t *testing.T) {
	assignable, unassignable := classifyAssignableActionableRecommendationsWithGate([]bv.TriageRecommendation{
		{ID: "bd-ready", Title: "Ready", Status: "open"},
		{ID: "bd-blocked", Title: "Blocked", Status: "open", BlockedBy: []string{"bd-parent"}},
		{ID: "bd-gated", Title: "Gated", Status: "open", Labels: []string{"operator-gated"}},
	}, 0, func(label string) bool { return label == "operator-gated" })
	if got := []string{assignable[0].ID}; !reflect.DeepEqual(got, []string{"bd-ready"}) {
		t.Fatalf("assignable IDs = %v, want [bd-ready]", got)
	}
	if want := []UnassignableBead{
		{ID: "bd-blocked", Title: "Blocked", Reason: "blocked by bd-parent"},
		{ID: "bd-gated", Title: "Gated", Reason: "operator-gated label: operator-gated"},
	}; !reflect.DeepEqual(unassignable, want) {
		t.Fatalf("unassignable beads = %+v, want %+v", unassignable, want)
	}
}

func TestUnassignedBeadsForAgentCapacity(t *testing.T) {
	ready := []bv.BeadPreview{
		{ID: "bd-1", Title: "First"},
		{ID: "bd-2", Title: "Second"},
		{ID: "bd-3", Title: "Third"},
	}
	if got, want := unassignedBeadsForAgentCapacity(ready, 1), []UnassignableBead{
		{ID: "bd-2", Title: "Second", Reason: "no idle agent available"},
		{ID: "bd-3", Title: "Third", Reason: "no idle agent available"},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unassignable capacity beads = %+v, want %+v", got, want)
	}
	if got := unassignedBeadsForAgentCapacity(ready, len(ready)); got != nil {
		t.Fatalf("unassignable capacity beads = %+v, want nil when every bead is recommended", got)
	}
}

// =============================================================================
// AgentStrength Tests
// =============================================================================

func TestAgentStrength_ReturnsValidRange(t *testing.T) {
	agentTypes := []string{"claude", "codex", "gemini", "unknown"}
	taskTypes := []string{"bug", "testing", "feature", "refactor", "documentation", "analysis", "task"}

	for _, agent := range agentTypes {
		for _, task := range taskTypes {
			t.Run(fmt.Sprintf("%s_%s", agent, task), func(t *testing.T) {
				score := AgentStrength(agent, task)
				if score < 0 || score > 1.0 {
					t.Errorf("AgentStrength(%q, %q) = %f, should be [0, 1.0]", agent, task, score)
				}
			})
		}
	}
}

// =============================================================================
// Output Struct JSON Serialization Tests
// =============================================================================

func TestAssignOutput_JSONStructure(t *testing.T) {
	output := AssignOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       "test-session",
		Strategy:      "balanced",
		GeneratedAt:   time.Now().UTC(),
		Recommendations: []AssignRecommend{
			{PaneID: "%12", PaneTarget: "0.2", AgentType: "claude", AssignBead: "bd-abc", BeadTitle: "Fix bug", Priority: "P1", Confidence: 0.85, Reasoning: "test"},
		},
		BlockedBeads: []BlockedBead{
			{ID: "bd-xyz", Title: "Blocked task", BlockedBy: []string{"bd-dep1"}},
		},
		IdleAgents: []string{"%12", "%13"},
		Summary: AssignSummary{
			TotalAgents:     4,
			IdleAgents:      2,
			WorkingAgents:   2,
			ReadyBeads:      5,
			Recommendations: 1,
		},
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	// Check top-level fields
	requiredFields := []string{
		"success", "session", "strategy", "generated_at",
		"recommendations", "blocked_beads", "idle_agents", "summary",
	}
	for _, field := range requiredFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("Missing field %q in JSON output", field)
		}
	}
}

func TestAssignOutput_OmitEmpty(t *testing.T) {
	output := AssignOutput{
		RobotResponse:   NewRobotResponse(true),
		Session:         "test",
		Recommendations: []AssignRecommend{},
		BlockedBeads:    []BlockedBead{},
		IdleAgents:      []string{},
		// UnassignableBeads is nil - should be omitted from top level
		// AgentHints is nil - should be omitted
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	// Parse to check top-level fields only (summary.unassignable_beads is always present)
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	// Top-level unassignable_beads should be omitted when nil (omitempty)
	if _, ok := parsed["unassignable_beads"]; ok {
		t.Error("top-level unassignable_beads should be omitted when nil")
	}
	// _agent_hints should be omitted when nil
	if _, ok := parsed["_agent_hints"]; ok {
		t.Error("_agent_hints should be omitted when nil")
	}
}

func TestAssignRecommend_JSONFields(t *testing.T) {
	rec := AssignRecommend{
		PaneID:     "%12",
		PaneTarget: "0.2",
		AgentType:  "claude",
		Model:      "opus-4.5",
		AssignBead: "bd-abc",
		BeadTitle:  "Fix bug",
		Priority:   "P1",
		Confidence: 0.85,
		Reasoning:  "excels at bug tasks",
	}

	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if parsed["pane_id"] != "%12" || parsed["pane_target"] != "0.2" {
		t.Errorf("pane identity = %v at %v, want %%12 at 0.2", parsed["pane_id"], parsed["pane_target"])
	}
	if _, ok := parsed["agent"]; ok {
		t.Error("legacy agent field must not be serialized")
	}
	if parsed["confidence"].(float64) != 0.85 {
		t.Errorf("confidence = %v, want 0.85", parsed["confidence"])
	}
}

func TestAssignOptions_Defaults(t *testing.T) {
	opts := AssignOptions{
		Session: "test",
	}

	if opts.Strategy != "" {
		t.Error("Strategy should default to empty (normalized to 'balanced' at runtime)")
	}
	if len(opts.Beads) != 0 {
		t.Error("Beads should default to empty")
	}
}

func TestFilterAssignableActionableRecommendationsAppliesSafetyBeforeLimit(t *testing.T) {
	recommendations := []bv.TriageRecommendation{
		{ID: "blocked", Status: "open", BlockedBy: []string{"dependency"}},
		{ID: "gated", Status: "open", Labels: []string{"  OPERATOR-GATED  "}},
		{ID: "closed", Status: "closed"},
		{ID: "   ", Status: "open"},
		{ID: "missing-status", Title: "Must not assign", Priority: 0},
		{ID: " first ", Title: " First ", Type: " task ", Status: "ready", Priority: 1},
		{ID: "second", Title: "Second", Status: " OPEN ", Priority: 2},
		{ID: " second ", Title: "Duplicate", Status: "ready", Priority: 0},
		{ID: "third", Title: "Third", Status: "ready", Priority: 3},
	}

	filtered := filterAssignableActionableRecommendations(recommendations, 2)
	if len(filtered) != 2 {
		t.Fatalf("filtered recommendation count = %d, want 2", len(filtered))
	}
	if got := []string{filtered[0].ID, filtered[1].ID}; !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("filtered IDs = %v, want eligible rows after safety filtering", got)
	}
	if filtered[0].Title != "First" || filtered[0].Type != "task" {
		t.Fatalf("trimmed first recommendation = %+v", filtered[0])
	}

	all := filterAssignableActionableRecommendations(recommendations, 0)
	if len(all) != 3 {
		t.Fatalf("uncapped filtered recommendation count = %d, want 3", len(all))
	}
	if got := []string{all[0].ID, all[1].ID, all[2].ID}; !reflect.DeepEqual(got, []string{"first", "second", "third"}) {
		t.Fatalf("uncapped filtered IDs = %v", got)
	}
	for _, recommendation := range all {
		if recommendation.ID == "missing-status" {
			t.Fatalf("blank-status recommendation escaped robot assignment safety boundary: %+v", recommendation)
		}
	}
	previews := filterAssignableBeadPreviews(recommendations, 2)
	if got := []bv.BeadPreview{
		{ID: "first", Title: "First", Priority: "P1", Type: "task"},
		{ID: "second", Title: "Second", Priority: "P2"},
	}; !reflect.DeepEqual(previews, got) {
		t.Fatalf("assignable previews = %+v, want %+v", previews, got)
	}
}

func TestFilterAssignableActionableRecommendationsIsolatesConcurrentProjectPolicies(t *testing.T) {
	projectA := t.TempDir()
	projectB := t.TempDir()
	if err := bv.ConfigureProjectOperatorGatedLabels(projectA, []string{"project-a-gate"}); err != nil {
		t.Fatalf("configure project A gates: %v", err)
	}
	if err := bv.ConfigureProjectOperatorGatedLabels(projectB, []string{"project-b-gate"}); err != nil {
		t.Fatalf("configure project B gates: %v", err)
	}
	recommendations := []bv.TriageRecommendation{
		{ID: "a-only", Status: "open", Labels: []string{"project-a-gate"}},
		{ID: "b-only", Status: "open", Labels: []string{"project-b-gate"}},
		{ID: "safe", Status: "open"},
	}

	assertIDs := func(projectDir string, want []string) error {
		for range 100 {
			filtered := filterAssignableActionableRecommendationsForProject(projectDir, recommendations, 0)
			got := make([]string, 0, len(filtered))
			for _, recommendation := range filtered {
				got = append(got, recommendation.ID)
			}
			if !reflect.DeepEqual(got, want) {
				return fmt.Errorf("project %s filtered IDs = %v, want %v", projectDir, got, want)
			}
		}
		return nil
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, check := range []struct {
		project string
		want    []string
	}{
		{project: projectA, want: []string{"b-only", "safe"}},
		{project: projectB, want: []string{"a-only", "safe"}},
	} {
		check := check
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- assertIDs(check.project, check.want)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadAuthoritativeAssignmentPolicyInstallsConfiguredGatesAndFailsClosed(t *testing.T) {
	previousLabels := bv.OperatorGatedLabels()
	t.Cleanup(func() { bv.ConfigureOperatorGatedLabels(previousLabels) })
	t.Setenv("NTM_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	projectDir := t.TempDir()
	if effective, err := loadAuthoritativeAssignmentPolicy(projectDir, "", false); err != nil || effective == nil {
		t.Fatalf("load default authoritative assignment policy: config=%v error=%v", effective, err)
	}
	globalPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(globalPath, []byte("[assign]\noperator_gated_labels = [\"release-approval\"]\n"), 0o600); err != nil {
		t.Fatalf("write global assignment policy: %v", err)
	}
	effective, err := loadAuthoritativeAssignmentPolicy(projectDir, globalPath, true)
	if err != nil {
		t.Fatalf("load authoritative assignment policy: %v", err)
	}
	if effective == nil || !bv.IsOperatorGatedLabel("release-approval") || !bv.IsOperatorGatedLabel("operator-gated") {
		t.Fatalf("installed operator gates = %v", bv.OperatorGatedLabels())
	}
	if !bv.IsOperatorGatedLabelForProject(projectDir, "release-approval") || !bv.IsOperatorGatedLabelForProject(projectDir, "operator-gated") {
		t.Fatalf("installed project operator gates = %v", bv.OperatorGatedLabelsForProject(projectDir))
	}

	missing := filepath.Join(t.TempDir(), "missing.toml")
	if _, err := loadAuthoritativeAssignmentPolicy(projectDir, missing, true); err == nil || !strings.Contains(err.Error(), "explicitly selected config") {
		t.Fatalf("missing required global policy error = %v", err)
	}
	if !bv.IsOperatorGatedLabel("release-approval") {
		t.Fatal("failed policy load mutated the previously installed gate vocabulary")
	}
	if !bv.IsOperatorGatedLabelForProject(projectDir, "release-approval") {
		t.Fatal("failed policy load mutated the previously installed project gate vocabulary")
	}
}

func TestPrintAssignFailureForcesJSONAndNonzeroExitUnderTOON(t *testing.T) {
	originalFormat := GetOutputFormat()
	SetOutputFormat(FormatTOON)
	t.Cleanup(func() { SetOutputFormat(originalFormat) })

	stdout, err := captureStdout(t, func() error {
		return PrintAssign(t.Context(), AssignOptions{})
	})
	var exitErr *ProcessExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || !exitErr.JSONWritten() {
		t.Fatalf("PrintAssign error = %T %v, want written exit-1 ProcessExitError", err, err)
	}

	var output AssignOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("PrintAssign failure is not JSON: %v\noutput=%q", err, stdout)
	}
	if output.Success || output.ErrorCode != ErrCodeInvalidFlag || output.OutputFormat != string(FormatJSON) {
		t.Fatalf("PrintAssign response = %+v, want INVALID_FLAG JSON failure", output.RobotResponse)
	}
}

func TestGetAssignCanceledContextReturnsStructuredFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	output, err := GetAssign(ctx, AssignOptions{Session: "unused"})
	if err != nil {
		t.Fatalf("GetAssign returned transport error: %v", err)
	}
	if output == nil || output.Success || output.ErrorCode != ErrCodeTimeout || !strings.Contains(output.Error, "canceled") {
		t.Fatalf("canceled assignment output = %+v", output)
	}
}

func TestSetAssignErrorClassifiesWrappedCancellationAsTimeout(t *testing.T) {
	for _, cause := range []error{
		context.Canceled,
		context.DeadlineExceeded,
		errors.Join(context.Canceled, bv.ErrNotInstalled),
	} {
		output := &AssignOutput{RobotResponse: NewRobotResponse(true)}
		setAssignError(output, fmt.Errorf("observe candidates: %w", cause), "internal hint")
		if output.Success || output.ErrorCode != ErrCodeTimeout || output.Hint != "Retry the command after cancellation" {
			t.Fatalf("setAssignError(%v) = %+v, want TIMEOUT failure", cause, output.RobotResponse)
		}
	}
}

func TestSetAssignErrorClassifiesMissingDependenciesAndInternalFailures(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode string
	}{
		{
			name:     "bv missing",
			err:      fmt.Errorf("triage failed: %w", bv.ErrNotInstalled),
			wantCode: ErrCodeDependencyMissing,
		},
		{
			name:     "br missing",
			err:      fmt.Errorf("ready scan failed: %w", &exec.Error{Name: "br", Err: exec.ErrNotFound}),
			wantCode: ErrCodeDependencyMissing,
		},
		{
			name:     "malformed output",
			err:      errors.New("malformed bv output"),
			wantCode: ErrCodeInternalError,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := &AssignOutput{RobotResponse: NewRobotResponse(true)}
			setAssignError(output, test.err, "repair assignment dependencies")
			if output.Success || output.ErrorCode != test.wantCode || output.Hint != "repair assignment dependencies" {
				t.Fatalf("setAssignError(%v) = %+v, want %s failure", test.err, output.RobotResponse, test.wantCode)
			}
		})
	}
}

func TestBlockedBeadRequiredDependencyArrayIsNeverNull(t *testing.T) {
	data, err := json.Marshal(BlockedBead{ID: "ntm-blocked", Title: "Blocked", BlockedBy: []string{}})
	if err != nil {
		t.Fatalf("marshal blocked bead: %v", err)
	}
	if strings.Contains(string(data), `"blocked_by":null`) || !strings.Contains(string(data), `"blocked_by":[]`) {
		t.Fatalf("blocked bead JSON = %s, want required empty array", data)
	}
}

func TestDistributeRecommendation_JSONFields(t *testing.T) {
	rec := DistributeRecommendation{
		BeadID:     "bd-abc",
		Title:      "Fix bug",
		PaneID:     "%12",
		PaneTarget: "0.2",
		AgentType:  "claude",
		Reason:     "high priority",
	}

	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if parsed["bead_id"] != "bd-abc" {
		t.Errorf("bead_id = %v, want %q", parsed["bead_id"], "bd-abc")
	}
	if parsed["pane_id"] != "%12" || parsed["pane_target"] != "0.2" {
		t.Errorf("pane identity = %v at %v, want %%12 at 0.2", parsed["pane_id"], parsed["pane_target"])
	}
	if _, ok := parsed["pane_index"]; ok {
		t.Error("legacy pane_index field must not be serialized")
	}
}
