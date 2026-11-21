package panels

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/Dicklesworthstone/ntm/internal/worktrees"
)

// WorktreesCheckpointsData holds git worktree and session checkpoint status.
type WorktreesCheckpointsData struct {
	Worktrees       []*worktrees.WorktreeInfo
	Checkpoints     []*checkpoint.Checkpoint
	GitAvailable    bool
	WorktreeError   error
	CheckpointError error
}

// WorktreeCheckpointActionMsg requests saving a new checkpoint or restoring a
// selected checkpoint from the TUI panel.
type WorktreeCheckpointActionMsg struct {
	Action       string // "save" or "restore"
	CheckpointID string
}

// WorktreesCheckpointsPanel combines isolated agent git worktrees with recent
// session checkpoints in one focused operational panel.
type WorktreesCheckpointsPanel struct {
	PanelBase
	data     WorktreesCheckpointsData
	selected int
	theme    theme.Theme
}

func worktreesCheckpointsConfig() PanelConfig {
	return PanelConfig{
		ID:              "worktrees_checkpoints",
		Title:           "Worktrees & Checkpoints",
		Priority:        PriorityNormal,
		RefreshInterval: 10 * time.Second,
		MinWidth:        30,
		MinHeight:       7,
		Collapsible:     true,
	}
}

// NewWorktreesCheckpointsPanel creates a new Worktrees & Checkpoints panel.
func NewWorktreesCheckpointsPanel() *WorktreesCheckpointsPanel {
	return &WorktreesCheckpointsPanel{
		PanelBase: NewPanelBase(worktreesCheckpointsConfig()),
		theme:     theme.Current(),
	}
}

func (p *WorktreesCheckpointsPanel) Init() tea.Cmd { return nil }

func (p *WorktreesCheckpointsPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok || !p.IsFocused() {
		return p, nil
	}
	switch keyMsg.String() {
	case "j", "down":
		if p.selected < len(p.data.Checkpoints)-1 {
			p.selected++
		}
	case "k", "up":
		if p.selected > 0 {
			p.selected--
		}
	case "c":
		return p, func() tea.Msg {
			return WorktreeCheckpointActionMsg{Action: "save"}
		}
	case "enter":
		if cp := p.SelectedCheckpoint(); cp != nil {
			cpID := cp.ID
			return p, func() tea.Msg {
				return WorktreeCheckpointActionMsg{Action: "restore", CheckpointID: cpID}
			}
		}
	}
	return p, nil
}

// SetData updates the panel snapshot.
func (p *WorktreesCheckpointsPanel) SetData(data WorktreesCheckpointsData) {
	p.data = data
	if p.selected >= len(p.data.Checkpoints) {
		p.selected = max(0, len(p.data.Checkpoints)-1)
	}
	if data.WorktreeError == nil && data.CheckpointError == nil {
		p.SetLastUpdate(time.Now())
	}
}

// SelectedCheckpoint returns the currently highlighted checkpoint, if any.
func (p *WorktreesCheckpointsPanel) SelectedCheckpoint() *checkpoint.Checkpoint {
	if p.selected < 0 || p.selected >= len(p.data.Checkpoints) {
		return nil
	}
	return p.data.Checkpoints[p.selected]
}

// HasError reports whether either data source failed.
func (p *WorktreesCheckpointsPanel) HasError() bool {
	return p.data.WorktreeError != nil || p.data.CheckpointError != nil
}

func (p *WorktreesCheckpointsPanel) Keybindings() []Keybinding {
	return []Keybinding{
		{Key: key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("j/k", "select checkpoint")), Description: "Navigate checkpoints", Action: "navigate"},
		{Key: key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "save checkpoint")), Description: "Save checkpoint", Action: "save"},
		{Key: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "restore hint")), Description: "Restore checkpoint", Action: "restore"},
	}
}

func (p *WorktreesCheckpointsPanel) View() string {
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

	title := fmt.Sprintf("%s (%d wt / %d cp)", p.Config().Title, len(p.data.Worktrees), len(p.data.Checkpoints))
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(t.Lavender).
		Border(lipgloss.NormalBorder(), false, false, true, false).
		BorderForeground(t.Surface1).
		Width(innerW).
		Align(lipgloss.Center)
	content.WriteString(headerStyle.Render(layout.TruncateWidthDefault(title, innerW)) + "\n")

	// Worktrees section
	switch {
	case p.data.WorktreeError != nil:
		content.WriteString(lipgloss.NewStyle().Foreground(t.Yellow).Render(
			layout.TruncateWidthDefault("Worktrees: "+p.data.WorktreeError.Error(), innerW),
		) + "\n")
	case !p.data.GitAvailable:
		content.WriteString(lipgloss.NewStyle().Foreground(t.Subtext).Render("Worktrees: not a git repository") + "\n")
	case len(p.data.Worktrees) == 0:
		content.WriteString(lipgloss.NewStyle().Foreground(t.Subtext).Render("Worktrees: none provisioned") + "\n")
	default:
		wtLimit := min(len(p.data.Worktrees), max(1, (h-6)/2))
		for i := 0; i < wtLimit; i++ {
			wt := p.data.Worktrees[i]
			statusIcon := "●"
			color := t.Green
			if wt.Error != "" || !wt.Created {
				statusIcon = "!"
				color = t.Red
			}
			line := fmt.Sprintf("%s %s (%s)", statusIcon, wt.AgentName, wt.BranchName)
			content.WriteString(lipgloss.NewStyle().Foreground(color).Render(layout.TruncateWidthDefault(line, innerW)) + "\n")
		}
	}

	// Checkpoints section
	if p.data.CheckpointError != nil {
		content.WriteString(components.ErrorState(p.data.CheckpointError.Error(), "Press r to retry", innerW) + "\n")
	} else if len(p.data.Checkpoints) == 0 {
		content.WriteString(lipgloss.NewStyle().Foreground(t.Subtext).Render("Checkpoints: none saved (c: save)") + "\n")
	} else {
		cpLimit := max(1, h-7-min(len(p.data.Worktrees), 2))
		start := 0
		if p.selected >= cpLimit {
			start = p.selected - cpLimit + 1
		}
		end := min(len(p.data.Checkpoints), start+cpLimit)
		for i := start; i < end; i++ {
			cp := p.data.Checkpoints[i]
			label := cp.Name
			if label == "" {
				label = cp.ID
			}
			patch := ""
			if cp.Git.IsDirty {
				patch = " [dirty]"
			}
			line := fmt.Sprintf("%s (%d panes)%s", label, cp.PaneCount, patch)
			line = layout.TruncateWidthDefault(line, innerW-2)
			prefix := "  "
			style := lipgloss.NewStyle().Foreground(t.Text)
			if i == p.selected && p.IsFocused() {
				prefix = "▸ "
				style = style.Foreground(t.Blue).Bold(true)
			}
			content.WriteString(style.Render(prefix+line) + "\n")
		}
	}

	hint := "j/k: nav • c: save checkpoint • enter: restore"
	content.WriteString(lipgloss.NewStyle().Foreground(t.Subtext).Render(layout.TruncateWidthDefault(hint, innerW)))
	return boxStyle.Render(FitToHeight(content.String(), max(1, h-4)))
}
