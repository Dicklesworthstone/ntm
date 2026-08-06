package panels

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/workflow"
)

func TestWorkflowPanelRendersWorkflowStateAndStageDetails(t *testing.T) {
	panel := NewWorkflowPanel()
	panel.SetSize(90, 20)
	panel.SetData(WorkflowPanelData{State: &workflow.WorkflowState{
		WorkflowName:   "review",
		CurrentStage:   "implement",
		StageStartedAt: time.Now().Add(-2 * time.Minute),
		Agents: map[string]string{
			"author":   "codex-1",
			"reviewer": "claude-1",
		},
		StageHistory: []workflow.StageRecord{
			{Stage: "design", Result: "passed"},
			{Stage: "test", Result: "failed"},
		},
	}}, nil)

	view := status.StripANSI(panel.View())
	for _, want := range []string{"Workflow: review", "✓ design", "○ test", "● implement", "Roles: author: codex-1, reviewer: claude-1", "Recent:", "! test: failed"} {
		if !strings.Contains(view, want) {
			t.Errorf("workflow panel missing %q:\n%s", want, view)
		}
	}

	updated, _ := panel.Update(tea.KeyMsg{Type: tea.KeyEnter})
	panel = updated.(*WorkflowPanel)
	if got := status.StripANSI(panel.View()); !strings.Contains(got, "Stage details: implement") {
		t.Fatalf("stage detail view missing:\n%s", got)
	}
}

func TestWorkflowPanelGracefullyHandlesNoWorkflowAndErrors(t *testing.T) {
	panel := NewWorkflowPanel()
	panel.SetSize(50, 10)

	if got := status.StripANSI(panel.View()); !strings.Contains(got, "No workflow active") {
		t.Fatalf("empty workflow view = %q", got)
	}

	panel.SetData(WorkflowPanelData{}, assertError("state store unavailable"))
	if got := status.StripANSI(panel.View()); !strings.Contains(got, "Workflow unavailable: state store unavailable") {
		t.Fatalf("error workflow view = %q", got)
	}
}

type assertError string

func (e assertError) Error() string { return string(e) }
