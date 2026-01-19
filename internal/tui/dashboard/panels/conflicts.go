package panels

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/Dicklesworthstone/ntm/internal/watcher"
)

// conflictsConfig returns the configuration for the conflicts panel
func conflictsConfig() PanelConfig {
	return PanelConfig{
		ID:              "conflicts",
		Title:           "File Conflicts",
		Priority:        PriorityCritical, // Conflicts need immediate attention
		RefreshInterval: 1 * time.Second,  // Fast refresh for conflicts
		MinWidth:        30,
		MinHeight:       8,
		Collapsible:     false, // Don't hide conflicts
	}
}

// ConflictsPanel displays file reservation conflicts and action options.
type ConflictsPanel struct {
	PanelBase
	conflicts      []watcher.FileConflict
	selectedIndex  int
	selectedAction int                         // 0=Wait, 1=Request, 2=Force
	actionHandler  watcher.ConflictActionHandler // Callback to handle actions
	now            func() time.Time
}

// NewConflictsPanel creates a new conflicts panel.
func NewConflictsPanel() *ConflictsPanel {
	return &ConflictsPanel{
		PanelBase: NewPanelBase(conflictsConfig()),
		now:       time.Now,
	}
}

// SetActionHandler sets the callback for handling conflict actions.
func (m *ConflictsPanel) SetActionHandler(handler watcher.ConflictActionHandler) {
	m.actionHandler = handler
}

// SetConflicts updates the list of active conflicts.
func (m *ConflictsPanel) SetConflicts(conflicts []watcher.FileConflict) {
	m.conflicts = conflicts
	// Reset selection if out of bounds
	if m.selectedIndex >= len(conflicts) {
		m.selectedIndex = 0
	}
}

// AddConflict adds a new conflict to the list if not already present.
func (m *ConflictsPanel) AddConflict(conflict watcher.FileConflict) {
	// Check if conflict already exists (same path and requestor)
	for i, c := range m.conflicts {
		if c.Path == conflict.Path && c.RequestorAgent == conflict.RequestorAgent {
			// Update existing conflict
			m.conflicts[i] = conflict
			return
		}
	}
	m.conflicts = append(m.conflicts, conflict)
}

// RemoveConflict removes a conflict by path and requestor.
func (m *ConflictsPanel) RemoveConflict(path, requestorAgent string) {
	for i, c := range m.conflicts {
		if c.Path == path && c.RequestorAgent == requestorAgent {
			m.conflicts = append(m.conflicts[:i], m.conflicts[i+1:]...)
			if m.selectedIndex >= len(m.conflicts) && m.selectedIndex > 0 {
				m.selectedIndex--
			}
			return
		}
	}
}

// HasConflicts returns true if there are any active conflicts.
func (m *ConflictsPanel) HasConflicts() bool {
	return len(m.conflicts) > 0
}

// ConflictCount returns the number of active conflicts.
func (m *ConflictsPanel) ConflictCount() int {
	return len(m.conflicts)
}

// GetConflicts returns a copy of the current conflicts.
func (m *ConflictsPanel) GetConflicts() []watcher.FileConflict {
	result := make([]watcher.FileConflict, len(m.conflicts))
	copy(result, m.conflicts)
	return result
}

// SelectedConflict returns the currently selected conflict, or nil if none.
func (m *ConflictsPanel) SelectedConflict() *watcher.FileConflict {
	if len(m.conflicts) == 0 || m.selectedIndex >= len(m.conflicts) {
		return nil
	}
	return &m.conflicts[m.selectedIndex]
}

// Init implements tea.Model
func (m *ConflictsPanel) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model
func (m *ConflictsPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if !m.IsFocused() || len(m.conflicts) == 0 {
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.selectedIndex > 0 {
				m.selectedIndex--
			}
		case "down", "j":
			if m.selectedIndex < len(m.conflicts)-1 {
				m.selectedIndex++
			}
		case "left", "h":
			if m.selectedAction > 0 {
				m.selectedAction--
			}
		case "right", "l":
			if m.selectedAction < 2 { // 3 actions: Wait, Request, Force
				m.selectedAction++
			}
		case "1":
			m.selectedAction = 0 // Wait
			return m, m.executeAction()
		case "2":
			m.selectedAction = 1 // Request
			return m, m.executeAction()
		case "3":
			m.selectedAction = 2 // Force
			return m, m.executeAction()
		case "enter":
			return m, m.executeAction()
		case "esc", "q":
			return m, m.dismissConflict()
		}
	}

	return m, nil
}

// executeAction triggers the action handler for the selected conflict.
func (m *ConflictsPanel) executeAction() tea.Cmd {
	if m.actionHandler == nil || len(m.conflicts) == 0 {
		return nil
	}

	conflict := m.conflicts[m.selectedIndex]
	action := watcher.ConflictActionWait
	switch m.selectedAction {
	case 0:
		action = watcher.ConflictActionWait
	case 1:
		action = watcher.ConflictActionRequest
	case 2:
		action = watcher.ConflictActionForce
	}

	return func() tea.Msg {
		err := m.actionHandler(conflict, action)
		return ConflictActionResultMsg{
			Conflict: conflict,
			Action:   action,
			Err:      err,
		}
	}
}

// dismissConflict removes the selected conflict.
func (m *ConflictsPanel) dismissConflict() tea.Cmd {
	if len(m.conflicts) == 0 {
		return nil
	}

	conflict := m.conflicts[m.selectedIndex]
	m.RemoveConflict(conflict.Path, conflict.RequestorAgent)

	if m.actionHandler != nil {
		return func() tea.Msg {
			err := m.actionHandler(conflict, watcher.ConflictActionDismiss)
			return ConflictActionResultMsg{
				Conflict: conflict,
				Action:   watcher.ConflictActionDismiss,
				Err:      err,
			}
		}
	}
	return nil
}

// ConflictActionResultMsg is sent when a conflict action completes.
type ConflictActionResultMsg struct {
	Conflict watcher.FileConflict
	Action   watcher.ConflictAction
	Err      error
}

// Keybindings returns conflicts panel specific shortcuts.
func (m *ConflictsPanel) Keybindings() []Keybinding {
	return []Keybinding{
		{
			Key:         key.NewBinding(key.WithKeys("1"), key.WithHelp("1", "wait")),
			Description: "Wait for reservation to expire",
			Action:      "wait",
		},
		{
			Key:         key.NewBinding(key.WithKeys("2"), key.WithHelp("2", "request")),
			Description: "Request handoff from holder",
			Action:      "request",
		},
		{
			Key:         key.NewBinding(key.WithKeys("3"), key.WithHelp("3", "force")),
			Description: "Force-release reservation",
			Action:      "force",
		},
		{
			Key:         key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "dismiss")),
			Description: "Dismiss conflict",
			Action:      "dismiss",
		},
	}
}

// View renders the conflicts panel.
func (m *ConflictsPanel) View() string {
	t := theme.Current()
	w, h := m.Width(), m.Height()

	if w <= 0 {
		return ""
	}

	borderColor := t.Surface1
	bgColor := t.Base
	if m.IsFocused() {
		borderColor = t.Red // Red border for conflicts
		bgColor = t.Surface0
	}

	boxStyle := lipgloss.NewStyle().
		Background(bgColor).
		Width(w).
		Height(h)

	// Header
	header := lipgloss.NewStyle().
		Bold(true).
		Foreground(t.Text).
		BorderStyle(lipgloss.NormalBorder()).
		BorderBottom(true).
		BorderForeground(borderColor).
		Width(w).
		Padding(0, 1).
		Render(m.Config().Title)

	var content strings.Builder
	content.WriteString(header + "\n")

	if len(m.conflicts) == 0 {
		emptyStyle := lipgloss.NewStyle().
			Foreground(t.Subtext).
			Italic(true).
			Padding(1, 1)
		content.WriteString(emptyStyle.Render("No file conflicts"))
		return boxStyle.Render(FitToHeight(content.String(), h))
	}

	nowFn := m.now
	if nowFn == nil {
		nowFn = time.Now
	}

	// Show conflict count
	countStyle := lipgloss.NewStyle().
		Foreground(t.Red).
		Bold(true).
		Padding(0, 1)
	content.WriteString(countStyle.Render(fmt.Sprintf("⚠ %d Conflict(s)", len(m.conflicts))) + "\n\n")

	// Available lines for conflict display
	availableLines := h - 5 // header + count + actions
	if availableLines < 3 {
		availableLines = 3
	}

	// Display selected conflict details
	conflict := m.conflicts[m.selectedIndex]

	// Conflict path
	pathStyle := lipgloss.NewStyle().Foreground(t.Yellow).Bold(true)
	pathLine := layout.TruncateWidthDefault(conflict.Path, w-4)
	content.WriteString("  " + pathStyle.Render(pathLine) + "\n")

	// Requestor info
	reqStyle := lipgloss.NewStyle().Foreground(t.Text)
	reqLine := fmt.Sprintf("  %s tried to edit", conflict.RequestorAgent)
	content.WriteString(reqStyle.Render(layout.TruncateWidthDefault(reqLine, w-2)) + "\n")

	// Holder info
	holderStyle := lipgloss.NewStyle().Foreground(t.Peach)
	holders := strings.Join(conflict.Holders, ", ")
	if conflict.TimeSinceReserved() > 0 {
		holders = fmt.Sprintf("%s (reserved %s ago)", holders, formatConflictDuration(conflict.TimeSinceReserved()))
	}
	holderLine := fmt.Sprintf("  Reserved by: %s", holders)
	content.WriteString(holderStyle.Render(layout.TruncateWidthDefault(holderLine, w-2)) + "\n")

	// Time remaining
	if conflict.TimeRemaining() > 0 {
		timeStyle := lipgloss.NewStyle().Foreground(t.Subtext)
		timeLine := fmt.Sprintf("  Expires in: %s", formatConflictDuration(conflict.TimeRemaining()))
		content.WriteString(timeStyle.Render(layout.TruncateWidthDefault(timeLine, w-2)) + "\n")
	}

	content.WriteString("\n")

	// Action buttons
	actionLabels := []string{"[1] Wait", "[2] Request", "[3] Force"}
	var actions strings.Builder
	actions.WriteString("  ")

	for i, label := range actionLabels {
		style := lipgloss.NewStyle().Padding(0, 1)
		if m.IsFocused() && i == m.selectedAction {
			style = style.
				Background(t.Blue).
				Foreground(t.Base).
				Bold(true)
		} else {
			style = style.
				Foreground(t.Subtext)
		}
		actions.WriteString(style.Render(label))
		if i < len(actionLabels)-1 {
			actions.WriteString(" ")
		}
	}
	actions.WriteString("  ")

	dismissStyle := lipgloss.NewStyle().Foreground(t.Subtext)
	actions.WriteString(dismissStyle.Render("[Esc] Dismiss"))

	content.WriteString(actions.String() + "\n")

	// Navigation hint if multiple conflicts
	if len(m.conflicts) > 1 {
		navStyle := lipgloss.NewStyle().Foreground(t.Subtext).Italic(true)
		navHint := fmt.Sprintf("  ↑/↓ to navigate (%d/%d)", m.selectedIndex+1, len(m.conflicts))
		content.WriteString(navStyle.Render(navHint) + "\n")
	}

	return boxStyle.Render(FitToHeight(content.String(), h))
}

// formatConflictDuration formats a duration in a human-readable way.
func formatConflictDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	if mins == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh%dm", hours, mins)
}
