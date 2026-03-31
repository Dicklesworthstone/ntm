// Package panels provides dashboard panel components.
// rotation_confirm.go implements the pending rotation confirmation panel.
package panels

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// RotationConfirmPanelData holds the data for the rotation confirmation panel.
type RotationConfirmPanelData struct {
	Pending  []*context.PendingRotation
	Selected int // Index of selected pending rotation
}

// RotationConfirmActionMsg is sent when the user confirms a rotation action.
type RotationConfirmActionMsg struct {
	AgentID string
	Action  context.ConfirmAction
}

// rotationConfirmConfig returns the configuration for the rotation confirmation panel.
func rotationConfirmConfig() PanelConfig {
	return PanelConfig{
		ID:              "rotation_confirm",
		Title:           "Pending Rotations",
		Priority:        PriorityCritical, // Pending rotations need immediate attention
		RefreshInterval: 2 * time.Second,  // Fast refresh for timeout countdown
		MinWidth:        30,
		MinHeight:       5,
		Collapsible:     false, // Don't hide pending rotations
	}
}

// RotationConfirmPanel displays pending context rotation confirmations.
type RotationConfirmPanel struct {
	PanelBase
	data  RotationConfirmPanelData
	err   error
	theme theme.Theme
	now   func() time.Time // For testing
}

// NewRotationConfirmPanel creates a new rotation confirmation panel.
func NewRotationConfirmPanel() *RotationConfirmPanel {
	return &RotationConfirmPanel{
		PanelBase: NewPanelBase(rotationConfirmConfig()),
		theme:     theme.Current(),
		now:       time.Now,
	}
}

// Init implements tea.Model.
func (p *RotationConfirmPanel) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (p *RotationConfirmPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if !p.IsFocused() {
			return p, nil
		}
		switch msg.String() {
		case "j", "down":
			if p.data.Selected < len(p.data.Pending)-1 {
				p.data.Selected++
			}
		case "k", "up":
			if p.data.Selected > 0 {
				p.data.Selected--
			}
		case "r":
			// Confirm rotation for selected pending
			if pending := p.SelectedPending(); pending != nil {
				return p, func() tea.Msg {
					return RotationConfirmActionMsg{
						AgentID: pending.AgentID,
						Action:  context.ConfirmRotate,
					}
				}
			}
		case "c":
			// Compact instead of rotating
			if pending := p.SelectedPending(); pending != nil {
				return p, func() tea.Msg {
					return RotationConfirmActionMsg{
						AgentID: pending.AgentID,
						Action:  context.ConfirmCompact,
					}
				}
			}
		case "i":
			// Ignore/cancel the rotation
			if pending := p.SelectedPending(); pending != nil {
				return p, func() tea.Msg {
					return RotationConfirmActionMsg{
						AgentID: pending.AgentID,
						Action:  context.ConfirmIgnore,
					}
				}
			}
		case "p":
			// Postpone the rotation
			if pending := p.SelectedPending(); pending != nil {
				return p, func() tea.Msg {
					return RotationConfirmActionMsg{
						AgentID: pending.AgentID,
						Action:  context.ConfirmPostpone,
					}
				}
			}
		}
	}
	return p, nil
}

// SetData updates the panel with pending rotation data.
func (p *RotationConfirmPanel) SetData(pending []*context.PendingRotation, err error) {
	selectedAgentID, hasSelection := p.selectedAgentID()
	if err != nil {
		pending = nil
	}
	p.data.Pending = append([]*context.PendingRotation(nil), pending...)
	p.err = err
	if err == nil {
		p.SetLastUpdate(time.Now())
	}

	if hasSelection && p.selectPendingByAgentID(selectedAgentID) {
		return
	}
	p.clampSelection()
}

// HasPending returns true if there are pending rotations.
func (p *RotationConfirmPanel) HasPending() bool {
	return len(p.data.Pending) > 0
}

// HasError returns true if there's an active error.
func (p *RotationConfirmPanel) HasError() bool {
	return p.err != nil
}

// SelectedPending returns the currently selected pending rotation.
func (p *RotationConfirmPanel) SelectedPending() *context.PendingRotation {
	if p.data.Selected >= 0 && p.data.Selected < len(p.data.Pending) {
		return p.data.Pending[p.data.Selected]
	}
	return nil
}

func (p *RotationConfirmPanel) selectedAgentID() (string, bool) {
	selected := p.SelectedPending()
	if selected == nil {
		return "", false
	}
	return selected.AgentID, true
}

func (p *RotationConfirmPanel) selectPendingByAgentID(agentID string) bool {
	if agentID == "" {
		return false
	}
	for i, pending := range p.data.Pending {
		if pending != nil && pending.AgentID == agentID {
			p.data.Selected = i
			return true
		}
	}
	return false
}

func (p *RotationConfirmPanel) clampSelection() {
	switch {
	case len(p.data.Pending) == 0:
		p.data.Selected = 0
	case p.data.Selected < 0:
		p.data.Selected = 0
	case p.data.Selected >= len(p.data.Pending):
		p.data.Selected = len(p.data.Pending) - 1
	}
}

// Keybindings returns rotation confirmation panel specific shortcuts.
func (p *RotationConfirmPanel) Keybindings() []Keybinding {
	return []Keybinding{
		{
			Key:         key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "rotate")),
			Description: "Confirm rotation for selected agent",
			Action:      "confirm_rotate",
		},
		{
			Key:         key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "compact")),
			Description: "Compact context instead of rotating",
			Action:      "confirm_compact",
		},
		{
			Key:         key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "ignore")),
			Description: "Cancel rotation and continue as-is",
			Action:      "confirm_ignore",
		},
		{
			Key:         key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "postpone")),
			Description: "Postpone rotation by 30 minutes",
			Action:      "confirm_postpone",
		},
		{
			Key:         key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("j/↓", "next")),
			Description: "Select next pending rotation",
			Action:      "select_next",
		},
		{
			Key:         key.NewBinding(key.WithKeys("k", "up"), key.WithHelp("k/↑", "prev")),
			Description: "Select previous pending rotation",
			Action:      "select_prev",
		},
	}
}

// View renders the panel.
func (p *RotationConfirmPanel) View() string {
	t := p.theme
	w, h := p.Width(), p.Height()

	if w <= 0 {
		return ""
	}

	nowFn := p.now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn()
	tick := int((now.UnixMilli() / 100) % 10_000)

	borderColor := t.Surface1
	bgColor := t.Base
	if p.IsFocused() {
		borderColor = t.Pink
		bgColor = t.Surface0
	}

	// Use urgent border color if any pending rotation has low timeout
	if p.hasUrgentTimeout() {
		borderColor = t.Red
	} else if len(p.data.Pending) > 0 {
		borderColor = t.Yellow
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Background(bgColor).
		Width(w-2).
		Height(h-2).
		Padding(0, 1)

	var content strings.Builder

	// Build header with count badge
	title := p.Config().Title
	if p.err != nil {
		errorBadge := lipgloss.NewStyle().
			Background(t.Red).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render("Error")
		title = title + " " + errorBadge
	} else if len(p.data.Pending) > 0 {
		// Use shimmering badge for urgent items
		badgeText := fmt.Sprintf("%d pending", len(p.data.Pending))
		var badge string
		if p.hasUrgentTimeout() {
			// Shimmer effect for urgent items
			badge = shimmerBadge(badgeText, tick, t)
		} else {
			badge = lipgloss.NewStyle().
				Background(t.Yellow).
				Foreground(t.Base).
				Bold(true).
				Padding(0, 1).
				Render(badgeText)
		}
		title = title + " " + badge
	} else if staleBadge := components.RenderStaleBadge(p.LastUpdate(), p.Config().RefreshInterval); staleBadge != "" {
		title = title + " " + staleBadge
	}

	// Header
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(t.Lavender).
		Border(lipgloss.NormalBorder(), false, false, true, false).
		BorderForeground(t.Surface1).
		Width(w - 4).
		Align(lipgloss.Center)

	content.WriteString(headerStyle.Render(title) + "\n")

	// Show error message if present
	if p.err != nil {
		content.WriteString(components.ErrorState(p.err.Error(), "Press r to retry", w-4) + "\n")
		return boxStyle.Render(FitToHeight(content.String(), h-4))
	}

	// Empty state: no pending rotations
	if len(p.data.Pending) == 0 {
		content.WriteString("\n" + components.RenderEmptyState(components.EmptyStateOptions{
			Icon:        components.IconSuccess,
			Title:       "No pending rotations",
			Description: "Context rotations happen automatically",
			Width:       w - 4,
			Centered:    true,
		}))
		return boxStyle.Render(FitToHeight(content.String(), h-4))
	}

	content.WriteString("\n")

	// Display pending rotations
	availHeight := h - 6 // header + footer
	if availHeight < 0 {
		availHeight = 0
	}

	for i, pending := range p.data.Pending {
		if i >= availHeight/3 { // ~3 lines per entry
			remaining := len(p.data.Pending) - i
			content.WriteString(lipgloss.NewStyle().
				Foreground(t.Overlay).
				Render(fmt.Sprintf("...and %d more", remaining)) + "\n")
			break
		}

		// Selection indicator
		isSelected := i == p.data.Selected
		selectChar := "  "
		if isSelected {
			selectChar = lipgloss.NewStyle().Foreground(t.Pink).Bold(true).Render("▸ ")
		}

		// Calculate remaining time
		remaining := pending.TimeoutAt.Sub(now)
		timeoutSec := int(remaining.Seconds())
		if timeoutSec < 0 {
			timeoutSec = 0
		}

		// Color based on urgency
		var timeColor lipgloss.Color
		if timeoutSec < 15 {
			timeColor = t.Red
		} else if timeoutSec < 30 {
			timeColor = t.Peach
		} else if timeoutSec < 60 {
			timeColor = t.Yellow
		} else {
			timeColor = t.Green
		}

		// Agent name and context
		agentStyle := lipgloss.NewStyle().Bold(true).Foreground(t.Text)
		if isSelected {
			agentStyle = agentStyle.Background(t.Surface0)
		}

		// First line: agent ID and timeout
		timeoutStr := formatTimeout(timeoutSec)
		timeStyle := lipgloss.NewStyle().Foreground(timeColor).Bold(true)

		line1 := fmt.Sprintf("%s%s  %s",
			selectChar,
			agentStyle.Render(truncateAgent(pending.AgentID, w-20)),
			timeStyle.Render(timeoutStr),
		)

		// Second line: context % and default action
		contextStr := fmt.Sprintf("%.0f%% context", pending.ContextPercent)
		actionStr := fmt.Sprintf("→ %s", pending.DefaultAction)
		line2 := fmt.Sprintf("   %s  %s",
			lipgloss.NewStyle().Foreground(t.Subtext).Render(contextStr),
			lipgloss.NewStyle().Foreground(t.Overlay).Italic(true).Render(actionStr),
		)

		content.WriteString(line1 + "\n")
		content.WriteString(line2 + "\n")
	}

	// Keyboard shortcuts hint
	if p.IsFocused() {
		hintStyle := lipgloss.NewStyle().
			Foreground(t.Overlay).
			Italic(true)
		content.WriteString("\n" + hintStyle.Render("[r]otate [c]ompact [i]gnore [p]ostpone"))
	}

	return boxStyle.Render(FitToHeight(content.String(), h-4))
}

// hasUrgentTimeout returns true if any pending rotation has less than 30 seconds.
func (p *RotationConfirmPanel) hasUrgentTimeout() bool {
	nowFn := p.now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn()

	for _, pending := range p.data.Pending {
		if pending.TimeoutAt.Sub(now) < 30*time.Second {
			return true
		}
	}
	return false
}

// formatTimeout formats seconds into a human-readable timeout string.
func formatTimeout(seconds int) string {
	if seconds <= 0 {
		return "⚠ expired"
	}
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	secs := seconds % 60
	if secs > 0 {
		return fmt.Sprintf("%dm%ds", minutes, secs)
	}
	return fmt.Sprintf("%dm", minutes)
}

// truncateAgent truncates an agent ID to fit the given width.
func truncateAgent(agentID string, maxWidth int) string {
	if len(agentID) <= maxWidth {
		return agentID
	}
	if maxWidth <= 3 {
		return "..."
	}
	return agentID[:maxWidth-3] + "..."
}

// shimmerBadge creates a shimmering badge for urgent items.
func shimmerBadge(text string, tick int, t theme.Theme) string {
	// Alternate between red and orange for urgency
	colors := []lipgloss.Color{t.Red, t.Maroon, t.Peach}
	colorIdx := (tick / 3) % len(colors)

	return lipgloss.NewStyle().
		Background(colors[colorIdx]).
		Foreground(t.Base).
		Bold(true).
		Padding(0, 1).
		Render(text)
}
