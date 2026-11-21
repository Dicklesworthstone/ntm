package panels

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// PipelinesData holds the durable pipeline run snapshot rendered by PipelinesPanel.
type PipelinesData struct {
	Runs     []*pipeline.PipelineExecution
	Warnings []string
	Error    error
}

// PipelineCancelActionMsg is emitted when the operator requests cancelling the
// selected running pipeline from the TUI dashboard.
type PipelineCancelActionMsg struct {
	RunID string
}

// PipelinesPanel renders active and recent pipeline runs, step progress, and failures.
type PipelinesPanel struct {
	PanelBase
	data     PipelinesData
	selected int
	theme    theme.Theme
}

func pipelinesConfig() PanelConfig {
	return PanelConfig{
		ID:              "pipelines",
		Title:           "Pipelines",
		Priority:        PriorityHigh,
		RefreshInterval: 3 * time.Second,
		MinWidth:        30,
		MinHeight:       7,
		Collapsible:     true,
	}
}

// NewPipelinesPanel creates a new Pipelines operational panel.
func NewPipelinesPanel() *PipelinesPanel {
	return &PipelinesPanel{
		PanelBase: NewPanelBase(pipelinesConfig()),
		theme:     theme.Current(),
	}
}

func (p *PipelinesPanel) Init() tea.Cmd { return nil }

func (p *PipelinesPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok || !p.IsFocused() {
		return p, nil
	}
	switch keyMsg.String() {
	case "j", "down":
		if p.selected < len(p.data.Runs)-1 {
			p.selected++
		}
	case "k", "up":
		if p.selected > 0 {
			p.selected--
		}
	case "x":
		if run := p.SelectedRun(); run != nil && run.Status == "running" {
			runID := run.RunID
			return p, func() tea.Msg {
				return PipelineCancelActionMsg{RunID: runID}
			}
		}
	}
	return p, nil
}

// SetData updates the panel snapshot while preserving selection bounds.
func (p *PipelinesPanel) SetData(data PipelinesData) {
	p.data = data
	if p.selected >= len(p.data.Runs) {
		p.selected = max(0, len(p.data.Runs)-1)
	}
	if data.Error == nil {
		p.SetLastUpdate(time.Now())
	}
}

// SelectedRun returns the currently highlighted pipeline execution, if any.
func (p *PipelinesPanel) SelectedRun() *pipeline.PipelineExecution {
	if p.selected < 0 || p.selected >= len(p.data.Runs) {
		return nil
	}
	return p.data.Runs[p.selected]
}

// HasError reports whether the latest refresh failed.
func (p *PipelinesPanel) HasError() bool {
	return p.data.Error != nil
}

func (p *PipelinesPanel) Keybindings() []Keybinding {
	return []Keybinding{
		{Key: key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("j/k", "select run")), Description: "Navigate runs", Action: "navigate"},
		{Key: key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "cancel run")), Description: "Cancel running pipeline", Action: "cancel"},
	}
}

func (p *PipelinesPanel) View() string {
	t := p.theme
	w, h := p.Width(), p.Height()
	if w <= 0 || h <= 0 {
		return ""
	}

	borderColor := t.Surface1
	bgColor := t.Base
	if p.IsFocused() {
		borderColor = t.Primary
		bgColor = t.Surface0
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Background(bgColor).
		Width(max(1, w-2)).
		Height(max(1, h-2)).
		Padding(0, 1)

	innerW := max(1, w-4)
	var content strings.Builder

	runningCount := 0
	failedCount := 0
	for _, r := range p.data.Runs {
		switch r.Status {
		case "running":
			runningCount++
		case "failed":
			failedCount++
		}
	}
	title := p.Config().Title
	if runningCount > 0 {
		title += " " + lipgloss.NewStyle().Foreground(t.Green).Bold(true).Render(fmt.Sprintf("(%d active)", runningCount))
	} else if failedCount > 0 {
		title += " " + lipgloss.NewStyle().Foreground(t.Red).Bold(true).Render(fmt.Sprintf("(%d failed)", failedCount))
	}

	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(t.Lavender).
		Border(lipgloss.NormalBorder(), false, false, true, false).
		BorderForeground(t.Surface1).
		Width(innerW).
		Align(lipgloss.Center)
	content.WriteString(headerStyle.Render(title) + "\n")

	if p.data.Error != nil {
		content.WriteString(components.ErrorState(p.data.Error.Error(), "Press r to retry", innerW) + "\n")
		return boxStyle.Render(FitToHeight(content.String(), max(1, h-4)))
	}
	if len(p.data.Runs) == 0 {
		content.WriteString(components.RenderEmptyState(components.EmptyStateOptions{
			Icon:        components.IconWaiting,
			Title:       "No pipeline runs",
			Description: "Run 'ntm pipeline run <file>' to execute",
			Width:       innerW,
			Centered:    true,
		}))
		return boxStyle.Render(FitToHeight(content.String(), max(1, h-4)))
	}

	maxRows := max(1, h-6)
	start := 0
	if p.selected >= maxRows {
		start = p.selected - maxRows + 1
	}
	end := min(len(p.data.Runs), start+maxRows)

	for i := start; i < end; i++ {
		run := p.data.Runs[i]
		icon, color := pipelineStatusVisual(run.Status, t)
		stepInfo := fmt.Sprintf("%d/%d", run.Progress.Completed, run.Progress.Total)
		if run.CurrentStep != "" && run.Status == "running" {
			stepInfo += " " + run.CurrentStep
		}
		line := fmt.Sprintf("%s %s [%s] %s", icon, run.WorkflowID, run.Status, stepInfo)
		if run.Error != "" {
			line += " — " + run.Error
		}
		line = layout.TruncateWidthDefault(line, innerW-2)

		prefix := "  "
		style := lipgloss.NewStyle().Foreground(color)
		if i == p.selected && p.IsFocused() {
			prefix = "▸ "
			style = style.Bold(true)
		}
		content.WriteString(style.Render(prefix+line) + "\n")
	}

	hint := "j/k: nav"
	if sel := p.SelectedRun(); sel != nil && sel.Status == "running" {
		hint += " • x: cancel"
	}
	content.WriteString(lipgloss.NewStyle().Foreground(t.Subtext).Render(layout.TruncateWidthDefault(hint, innerW)))
	return boxStyle.Render(FitToHeight(content.String(), max(1, h-4)))
}

func pipelineStatusVisual(status string, t theme.Theme) (string, lipgloss.Color) {
	switch status {
	case "running":
		return "●", t.Green
	case "completed":
		return "✓", t.Blue
	case "failed":
		return "✗", t.Red
	case "cancelled":
		return "⊘", t.Yellow
	default:
		return "○", t.Subtext
	}
}
