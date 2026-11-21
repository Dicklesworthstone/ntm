package panels

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// ApprovalsData holds pending SLB/policy approval requests for ApprovalsPanel.
type ApprovalsData struct {
	Pending   []state.Approval
	Available bool
	Error     error
}

// ApprovalDecisionActionMsg requests an async approve or deny mutation for the
// selected pending approval.
type ApprovalDecisionActionMsg struct {
	ApprovalID string
	Approve    bool
}

// ApprovalsPanel surfaces pending SLB and safety policy approval requests with
// quick keyboard approve ('y') and deny ('x') actions.
type ApprovalsPanel struct {
	PanelBase
	data     ApprovalsData
	selected int
	theme    theme.Theme
}

func approvalsConfig() PanelConfig {
	return PanelConfig{
		ID:              "approvals",
		Title:           "Safety & Approvals",
		Priority:        PriorityCritical,
		RefreshInterval: 5 * time.Second,
		MinWidth:        30,
		MinHeight:       7,
		Collapsible:     true,
	}
}

// NewApprovalsPanel creates a new Safety & Approvals panel.
func NewApprovalsPanel() *ApprovalsPanel {
	return &ApprovalsPanel{
		PanelBase: NewPanelBase(approvalsConfig()),
		theme:     theme.Current(),
	}
}

func (p *ApprovalsPanel) Init() tea.Cmd { return nil }

func (p *ApprovalsPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok || !p.IsFocused() {
		return p, nil
	}
	switch keyMsg.String() {
	case "j", "down":
		if p.selected < len(p.data.Pending)-1 {
			p.selected++
		}
	case "k", "up":
		if p.selected > 0 {
			p.selected--
		}
	case "y":
		if item := p.SelectedApproval(); item != nil {
			id := item.ID
			return p, func() tea.Msg {
				return ApprovalDecisionActionMsg{ApprovalID: id, Approve: true}
			}
		}
	case "x":
		if item := p.SelectedApproval(); item != nil {
			id := item.ID
			return p, func() tea.Msg {
				return ApprovalDecisionActionMsg{ApprovalID: id, Approve: false}
			}
		}
	}
	return p, nil
}

// SetData updates the panel snapshot and clamps the selection cursor.
func (p *ApprovalsPanel) SetData(data ApprovalsData) {
	p.data = data
	if p.selected >= len(p.data.Pending) {
		p.selected = max(0, len(p.data.Pending)-1)
	}
	if data.Error == nil {
		p.SetLastUpdate(time.Now())
	}
}

// SelectedApproval returns the highlighted pending approval, if any.
func (p *ApprovalsPanel) SelectedApproval() *state.Approval {
	if p.selected < 0 || p.selected >= len(p.data.Pending) {
		return nil
	}
	item := p.data.Pending[p.selected]
	return &item
}

// HasPending reports whether at least one approval is awaiting review.
func (p *ApprovalsPanel) HasPending() bool {
	return len(p.data.Pending) > 0
}

// HasError reports whether the latest refresh returned an error.
func (p *ApprovalsPanel) HasError() bool {
	return p.data.Error != nil
}

func (p *ApprovalsPanel) Keybindings() []Keybinding {
	return []Keybinding{
		{Key: key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("j/k", "select")), Description: "Navigate approvals", Action: "navigate"},
		{Key: key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "approve")), Description: "Approve selected request", Action: "approve"},
		{Key: key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "deny")), Description: "Deny selected request", Action: "deny"},
	}
}

func (p *ApprovalsPanel) View() string {
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
	} else if p.HasPending() {
		borderColor = t.Yellow
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

	title := p.Config().Title
	if len(p.data.Pending) > 0 {
		title += " " + lipgloss.NewStyle().Foreground(t.Yellow).Bold(true).Render(fmt.Sprintf("(%d pending)", len(p.data.Pending)))
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
	if !p.data.Available {
		content.WriteString(components.RenderEmptyState(components.EmptyStateOptions{
			Icon:        components.IconWaiting,
			Title:       "Approval store unavailable",
			Description: "State store not initialized",
			Width:       innerW,
			Centered:    true,
		}))
		return boxStyle.Render(FitToHeight(content.String(), max(1, h-4)))
	}
	if len(p.data.Pending) == 0 {
		content.WriteString(components.RenderEmptyState(components.EmptyStateOptions{
			Icon:        components.IconSuccess,
			Title:       "No pending approvals",
			Description: "All SLB & safety checks clear",
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
	end := min(len(p.data.Pending), start+maxRows)

	for i := start; i < end; i++ {
		req := p.data.Pending[i]
		slbTag := ""
		if req.RequiresSLB {
			slbTag = "[SLB] "
		}
		line := fmt.Sprintf("%s%s %s (by %s)", slbTag, req.Action, req.Resource, req.RequestedBy)
		if req.Reason != "" {
			line += " — " + req.Reason
		}
		line = layout.TruncateWidthDefault(line, innerW-2)

		prefix := "  "
		style := lipgloss.NewStyle().Foreground(t.Yellow)
		if i == p.selected && p.IsFocused() {
			prefix = "▸ "
			style = style.Bold(true)
		}
		content.WriteString(style.Render(prefix+line) + "\n")
	}

	hint := "j/k: nav • y: approve • x: deny"
	content.WriteString(lipgloss.NewStyle().Foreground(t.Subtext).Render(layout.TruncateWidthDefault(hint, innerW)))
	return boxStyle.Render(FitToHeight(content.String(), max(1, h-4)))
}
