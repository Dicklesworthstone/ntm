package panels

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/Dicklesworthstone/ntm/internal/workflow"
)

// WorkflowPanelData is the durable, coordination-type-agnostic workflow
// snapshot rendered by WorkflowPanel.
type WorkflowPanelData struct {
	State *workflow.WorkflowState
}

func workflowConfig() PanelConfig {
	return PanelConfig{
		ID:              "workflow",
		Title:           "Workflow",
		Priority:        PriorityHigh,
		RefreshInterval: 2 * time.Second,
		MinWidth:        30,
		MinHeight:       8,
		Collapsible:     true,
	}
}

// WorkflowPanel renders the active stage, agent roles, and recent durable
// stage activity. It relies solely on WorkflowState, so it works uniformly
// for every workflow coordination type.
type WorkflowPanel struct {
	PanelBase
	data    WorkflowPanelData
	err     error
	theme   theme.Theme
	details bool
}

func NewWorkflowPanel() *WorkflowPanel {
	return &WorkflowPanel{
		PanelBase: NewPanelBase(workflowConfig()),
		theme:     theme.Current(),
	}
}

func (p *WorkflowPanel) Init() tea.Cmd { return nil }

func (p *WorkflowPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok && keyMsg.String() == "enter" && p.data.State != nil {
		p.details = !p.details
	}
	return p, nil
}

func (p *WorkflowPanel) SetData(data WorkflowPanelData, err error) {
	p.data = data
	p.err = err
	if err == nil {
		p.SetLastUpdate(time.Now())
	}
}

func (p *WorkflowPanel) HasData() bool { return p.data.State != nil || p.err != nil }

func (p *WorkflowPanel) View() string {
	if p.Width() <= 0 {
		return ""
	}
	if p.err != nil {
		return workflowFrame(p.Config().Title, "Workflow unavailable: "+p.err.Error(), p.Width(), p.Height(), p.IsFocused(), p.theme.Red)
	}
	if p.data.State == nil {
		return workflowFrame(p.Config().Title, "No workflow active", p.Width(), p.Height(), p.IsFocused(), p.theme.Overlay)
	}

	state := p.data.State
	t := p.theme
	width := max(1, p.Width()-4)
	lines := []string{
		lipgloss.NewStyle().Bold(true).Foreground(t.Lavender).Render("Workflow: " + state.WorkflowName),
		fmt.Sprintf("Stage: %s", lipgloss.NewStyle().Bold(true).Foreground(t.Green).Render(state.CurrentStage)),
	}
	if state.Paused {
		lines = append(lines, lipgloss.NewStyle().Foreground(t.Yellow).Render("Paused: "+state.PauseReason))
	}

	stages := workflowStages(state)
	if len(stages) > 0 {
		flow := make([]string, 0, len(stages))
		for _, stage := range stages {
			marker := "○"
			if stage == state.CurrentStage {
				marker = "●"
			} else if workflowStageCompleted(state, stage) {
				marker = "✓"
			}
			flow = append(flow, marker+" "+stage)
		}
		lines = append(lines, lipgloss.NewStyle().Foreground(t.Subtext).Render(strings.Join(flow, " → ")))
	}

	roles := make([]string, 0, len(state.Agents))
	for role := range state.Agents {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	if len(roles) > 0 {
		agents := make([]string, 0, len(roles))
		for _, role := range roles {
			agents = append(agents, role+": "+state.Agents[role])
		}
		lines = append(lines, "Roles: "+strings.Join(agents, ", "))
	}

	if len(state.StageHistory) > 0 {
		lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(t.Text).Render("Recent:"))
		start := max(0, len(state.StageHistory)-3)
		for _, record := range state.StageHistory[start:] {
			icon := "✓"
			if record.Result != "success" && record.Result != "passed" {
				icon = "!"
			}
			lines = append(lines, fmt.Sprintf("%s %s: %s", icon, record.Stage, record.Result))
		}
	}
	if p.details {
		elapsed := time.Since(state.StageStartedAt).Round(time.Second)
		if state.StageStartedAt.IsZero() {
			elapsed = 0
		}
		lines = append(lines, fmt.Sprintf("Stage details: %s active for %s", state.CurrentStage, elapsed))
		if len(state.Errors) > 0 {
			lines = append(lines, fmt.Sprintf("Errors: %d", len(state.Errors)))
		}
	} else {
		lines = append(lines, "Enter: stage details")
	}
	lines = append(lines, fmt.Sprintf("Cycle: %d | Roles: %d", len(state.StageHistory)+1, len(roles)))
	return workflowFrame(p.Config().Title, strings.Join(lines, "\n"), width, p.Height(), p.IsFocused(), t.Surface1)
}

func workflowStages(state *workflow.WorkflowState) []string {
	if state == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(state.StageHistory)+1)
	stages := make([]string, 0, len(state.StageHistory)+1)
	for _, record := range state.StageHistory {
		if record.Stage == "" {
			continue
		}
		if _, ok := seen[record.Stage]; ok {
			continue
		}
		seen[record.Stage] = struct{}{}
		stages = append(stages, record.Stage)
	}
	if state.CurrentStage != "" {
		if _, ok := seen[state.CurrentStage]; !ok {
			stages = append(stages, state.CurrentStage)
		}
	}
	return stages
}

func workflowStageCompleted(state *workflow.WorkflowState, stage string) bool {
	for _, record := range state.StageHistory {
		if record.Stage == stage && (record.Result == "success" || record.Result == "passed") {
			return true
		}
	}
	return false
}

func workflowFrame(title, content string, width, height int, focused bool, border lipgloss.Color) string {
	if focused {
		border = theme.Current().Pink
	}
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Padding(0, 1).
		Width(max(1, width))
	if height > 0 {
		style = style.Height(height)
	}
	return style.Render(lipgloss.NewStyle().Bold(true).Render(title) + "\n" + content)
}
