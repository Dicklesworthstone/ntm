package panels

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// spawnConfig returns the configuration for the spawn panel
func spawnConfig() PanelConfig {
	return PanelConfig{
		ID:              "spawn",
		Title:           "Spawn Progress",
		Priority:        PriorityHigh, // Show spawn progress prominently
		RefreshInterval: 1 * time.Second,
		MinWidth:        30,
		MinHeight:       5,
		Collapsible:     true,
	}
}

// SpawnPromptStatus represents the status of a single agent's prompt
type SpawnPromptStatus struct {
	Pane        string
	Order       int
	ScheduledAt time.Time
	Sent        bool
	SentAt      time.Time
}

// SpawnData holds data for the spawn panel
type SpawnData struct {
	// Active indicates whether a spawn is in progress
	Active bool

	// BatchID is the unique identifier for this spawn batch
	BatchID string

	// StartedAt is when the spawn began
	StartedAt time.Time

	// StaggerSeconds is the interval between agent prompts
	StaggerSeconds int

	// TotalAgents is the total number of agents in this spawn
	TotalAgents int

	// Prompts tracks the status of each agent's prompt delivery
	Prompts []SpawnPromptStatus

	// CompletedAt is when all prompts were delivered (zero if in progress)
	CompletedAt time.Time
}

// IsComplete returns whether the spawn is complete
func (d SpawnData) IsComplete() bool {
	return !d.CompletedAt.IsZero()
}

// SentCount returns the number of prompts that have been sent
func (d SpawnData) SentCount() int {
	count := 0
	for _, p := range d.Prompts {
		if p.Sent {
			count++
		}
	}
	return count
}

// PendingCount returns the number of prompts not yet sent
func (d SpawnData) PendingCount() int {
	return len(d.Prompts) - d.SentCount()
}

// SpawnPanel displays the staggered spawn progress
type SpawnPanel struct {
	PanelBase
	data SpawnData
	now  func() time.Time
}

// NewSpawnPanel creates a new spawn panel
func NewSpawnPanel() *SpawnPanel {
	return &SpawnPanel{
		PanelBase: NewPanelBase(spawnConfig()),
		now:       time.Now,
	}
}

// SetData updates the panel data
func (m *SpawnPanel) SetData(data SpawnData) {
	m.data = data
}

// IsActive returns whether there's an active spawn
func (m *SpawnPanel) IsActive() bool {
	return m.data.Active && !m.data.IsComplete()
}

// Init implements tea.Model
func (m *SpawnPanel) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model
func (m *SpawnPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return m, nil
}

// Keybindings returns spawn panel specific shortcuts
func (m *SpawnPanel) Keybindings() []Keybinding {
	return nil // The spawn panel defines no panel-specific shortcuts.
}

// View renders the panel
func (m *SpawnPanel) View() string {
	t := theme.Current()
	w, h := m.Width(), m.Height()

	if w <= 0 {
		return ""
	}

	nowFn := m.now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn()

	borderColor := t.Surface1
	bgColor := t.Base
	if m.IsFocused() {
		borderColor = t.Blue
		bgColor = t.Surface0
	}

	boxStyle := lipgloss.NewStyle().
		Background(bgColor).
		Width(w).
		Height(h)

	// Build header
	title := m.Config().Title
	if m.data.Active && !m.data.IsComplete() {
		// Add live indicator
		tick := int((now.UnixMilli() / 500) % 4)
		dots := strings.Repeat(".", tick+1)
		dots = dots + strings.Repeat(" ", 3-tick)
		liveIndicator := lipgloss.NewStyle().
			Foreground(t.Green).
			Bold(true).
			Render(dots)
		title = title + " " + liveIndicator
	}

	header := lipgloss.NewStyle().
		Bold(true).
		Foreground(t.Text).
		BorderStyle(lipgloss.NormalBorder()).
		BorderBottom(true).
		BorderForeground(borderColor).
		Width(w).
		Padding(0, 1).
		Render(title)

	var content strings.Builder
	content.WriteString(header + "\n")

	// If no active spawn, show empty state
	if !m.data.Active {
		emptyStyle := lipgloss.NewStyle().
			Foreground(t.Overlay).
			Italic(true).
			Padding(0, 1)
		content.WriteString("\n" + emptyStyle.Render("No active spawn") + "\n")
		return boxStyle.Render(FitToHeight(content.String(), h))
	}

	// Stats row
	staggerStr := fmt.Sprintf("%ds", m.data.StaggerSeconds)
	stats := fmt.Sprintf("Stagger: %s  Sent: %d/%d", staggerStr, m.data.SentCount(), len(m.data.Prompts))
	if m.data.IsComplete() {
		duration := m.data.CompletedAt.Sub(m.data.StartedAt)
		stats = fmt.Sprintf("Completed in %v", duration.Round(time.Second))
	}
	statsStyled := lipgloss.NewStyle().
		Foreground(t.Subtext).
		Padding(0, 1).
		Render(stats)
	content.WriteString(statsStyled + "\n\n")

	// Calculate display limit based on height
	availableLines := h - 4
	if availableLines < 0 {
		availableLines = 0
	}

	// Render prompt statuses
	for i, p := range m.data.Prompts {
		if i >= availableLines {
			// Show truncation indicator
			remaining := len(m.data.Prompts) - i
			truncStyle := lipgloss.NewStyle().Foreground(t.Overlay).Italic(true)
			content.WriteString(truncStyle.Render(fmt.Sprintf("  ... %d more", remaining)) + "\n")
			break
		}

		pane := layout.TruncateWidth(p.Pane, w-20, "...")
		var line string
		var style lipgloss.Style

		if p.Sent {
			// Sent - green checkmark
			line = fmt.Sprintf("  %s %s", "\u2713", pane)
			style = lipgloss.NewStyle().Foreground(t.Green)
		} else {
			// Pending - show countdown
			remaining := p.ScheduledAt.Sub(now)
			if remaining < 0 {
				remaining = 0
			}
			countdown := formatDuration(remaining)
			line = fmt.Sprintf("  %s %s  %s", "\u25CB", pane, countdown)
			style = lipgloss.NewStyle().Foreground(t.Yellow)

			// Pulse if about to send (within 5 seconds)
			if remaining > 0 && remaining < 5*time.Second {
				tick := int((now.UnixMilli() / 200) % 2)
				if tick == 0 {
					style = style.Bold(true)
				}
			}
		}

		content.WriteString(style.Render(line) + "\n")
	}

	return boxStyle.Render(FitToHeight(content.String(), h))
}

// formatDuration formats a duration for display (e.g., "1m 30s", "45s")
func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "now"
	}

	d = d.Round(time.Second)
	minutes := int(d.Minutes())
	seconds := int(d.Seconds()) % 60

	if minutes > 0 {
		return fmt.Sprintf("%dm %02ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}
