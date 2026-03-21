package panels

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/icons"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// attentionConfig returns the configuration for the attention panel
func attentionConfig() PanelConfig {
	return PanelConfig{
		ID:              "attention",
		Title:           "Attention",
		Priority:        PriorityCritical, // Attention items are high priority
		RefreshInterval: 5 * time.Second,  // Same as alerts
		MinWidth:        25,
		MinHeight:       6,
		Collapsible:     false, // Don't hide attention items
	}
}

// AttentionItem represents a single attention item for display.
type AttentionItem struct {
	Summary       string
	Actionability robot.Actionability
	Timestamp     time.Time
	SourcePane    int    // Pane index that generated the event
	SourceAgent   string // Agent type (e.g., "claude", "codex")
	Cursor        int64  // Event cursor for tracking
}

// AttentionPanel displays attention feed items requiring operator response.
type AttentionPanel struct {
	PanelBase
	items         []AttentionItem
	feedAvailable bool
	viewport      viewport.Model
	cursor        int // Selected item index

	now func() time.Time
}

// NewAttentionPanel creates a new attention panel.
func NewAttentionPanel() *AttentionPanel {
	vp := viewport.New(25, 6)
	return &AttentionPanel{
		PanelBase:     NewPanelBase(attentionConfig()),
		viewport:      vp,
		feedAvailable: false,
		now:           time.Now,
	}
}

// SetData updates the panel with attention items.
func (m *AttentionPanel) SetData(items []AttentionItem, feedAvailable bool) {
	m.items = append(m.items[:0], items...)
	sort.SliceStable(m.items, func(i, j int) bool {
		return attentionItemLess(m.items[i], m.items[j])
	})
	m.feedAvailable = feedAvailable
	// Clamp cursor to valid range
	if m.cursor >= len(m.items) {
		m.cursor = len(m.items) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// SelectedItem returns the currently selected attention item, or nil if none.
func (m *AttentionPanel) SelectedItem() *AttentionItem {
	if len(m.items) == 0 || m.cursor < 0 || m.cursor >= len(m.items) {
		return nil
	}
	return &m.items[m.cursor]
}

// SelectCursor focuses the item with the requested event cursor.
func (m *AttentionPanel) SelectCursor(cursor int64) bool {
	if cursor <= 0 {
		return false
	}
	for i := range m.items {
		if m.items[i].Cursor == cursor {
			m.cursor = i
			return true
		}
	}
	return false
}

// SelectNearestCursor focuses the surviving item closest to the requested cursor.
func (m *AttentionPanel) SelectNearestCursor(cursor int64) bool {
	if cursor <= 0 || len(m.items) == 0 {
		return false
	}

	bestIdx := -1
	var bestDelta int64
	var bestCursor int64
	for i := range m.items {
		delta := absInt64(m.items[i].Cursor - cursor)
		if bestIdx == -1 ||
			delta < bestDelta ||
			(delta == bestDelta && m.items[i].Cursor > bestCursor) {
			bestIdx = i
			bestDelta = delta
			bestCursor = m.items[i].Cursor
		}
	}
	if bestIdx < 0 {
		return false
	}
	m.cursor = bestIdx
	return true
}

// HasItems returns true if there are attention items.
func (m *AttentionPanel) HasItems() bool {
	return len(m.items) > 0
}

// ItemCount returns the number of attention items.
func (m *AttentionPanel) ItemCount() int {
	return len(m.items)
}

// ActionRequiredCount returns the count of action_required items.
func (m *AttentionPanel) ActionRequiredCount() int {
	count := 0
	for _, item := range m.items {
		if item.Actionability == robot.ActionabilityActionRequired {
			count++
		}
	}
	return count
}

// InterestingCount returns the count of interesting items.
func (m *AttentionPanel) InterestingCount() int {
	count := 0
	for _, item := range m.items {
		if item.Actionability == robot.ActionabilityInteresting {
			count++
		}
	}
	return count
}

// IsFeedAvailable returns whether the attention feed is available.
func (m *AttentionPanel) IsFeedAvailable() bool {
	return m.feedAvailable
}

func (m *AttentionPanel) Init() tea.Cmd {
	return nil
}

func (m *AttentionPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if !m.IsFocused() {
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
		case "home", "g":
			m.cursor = 0
		case "end", "G":
			if len(m.items) > 0 {
				m.cursor = len(m.items) - 1
			}
		}
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

// Keybindings returns attention panel specific shortcuts.
func (m *AttentionPanel) Keybindings() []Keybinding {
	return []Keybinding{
		{
			Key:         key.NewBinding(key.WithKeys("enter", "z"), key.WithHelp("enter/z", "zoom")),
			Description: "Zoom to source pane",
			Action:      "zoom_to_source",
		},
	}
}

// HandlesOwnHeight returns true since we use a viewport.
func (m *AttentionPanel) HandlesOwnHeight() bool {
	return true
}

func (m *AttentionPanel) View() string {
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
		borderColor = t.Pink
		bgColor = t.Surface0
	}

	boxStyle := lipgloss.NewStyle().
		Background(bgColor).
		Width(w).
		Height(h)

	// Build header
	title := m.Config().Title
	if warning := icons.Current().Warning; warning != "" {
		title = warning + " " + title
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

	// Handle feed not available
	if !m.feedAvailable {
		content.WriteString("\n" + components.RenderEmptyState(components.EmptyStateOptions{
			Icon:        components.IconWaiting,
			Title:       "Feed not active",
			Description: "Attention Feed not active",
			Width:       w,
			Centered:    true,
		}))
		return boxStyle.Render(FitToHeight(content.String(), h))
	}

	// Handle empty items
	if len(m.items) == 0 {
		content.WriteString("\n" + components.RenderEmptyState(components.EmptyStateOptions{
			Icon:        components.IconSuccess,
			Title:       "All clear",
			Description: "No attention items",
			Width:       w,
			Centered:    true,
		}))
		return boxStyle.Render(FitToHeight(content.String(), h))
	}

	// Stats row
	actionCount := m.ActionRequiredCount()
	interestingCount := m.InterestingCount()
	stats := fmt.Sprintf("Action: %d  Interesting: %d", actionCount, interestingCount)
	statsStyled := lipgloss.NewStyle().Foreground(t.Subtext).Padding(0, 1).Render(stats)
	content.WriteString(statsStyled + "\n\n")

	// Render items
	var body strings.Builder
	for i, item := range m.items {
		line := m.renderItem(item, i == m.cursor, w, now, t)
		body.WriteString(line + "\n")
	}

	// Update viewport content
	m.viewport.SetContent(body.String())
	m.viewport.Width = w
	m.viewport.Height = h - 4 // Account for header and stats

	content.WriteString(m.viewport.View())

	return boxStyle.Render(FitToHeight(content.String(), h))
}

func (m *AttentionPanel) renderItem(item AttentionItem, selected bool, width int, now time.Time, t theme.Theme) string {
	// Icon based on actionability
	var icon string
	var color lipgloss.Color
	switch item.Actionability {
	case robot.ActionabilityActionRequired:
		icon = "●" // Red circle
		color = t.Red
	case robot.ActionabilityInteresting:
		icon = "▲" // Yellow triangle
		color = t.Yellow
	default:
		icon = "○" // Background
		color = t.Subtext
	}

	// Format age
	age := formatRelativeTime(now.Sub(item.Timestamp))

	// Source info
	source := ""
	if item.SourcePane >= 0 {
		if item.SourceAgent != "" {
			source = fmt.Sprintf("pane %d (%s)", item.SourcePane, item.SourceAgent)
		} else {
			source = fmt.Sprintf("pane %d", item.SourcePane)
		}
	}

	// Keep most of the first line available for the summary. Source + age are
	// rendered on a second line, so the first line only needs room for the
	// cursor marker, icon, and a single separating space.
	maxSummaryWidth := width - 4
	if maxSummaryWidth < 0 {
		maxSummaryWidth = 0
	}
	summary := layout.TruncateWidthDefault(item.Summary, maxSummaryWidth)

	// Build line
	var line strings.Builder
	if selected && m.IsFocused() {
		line.WriteString("▶ ")
	} else {
		line.WriteString("  ")
	}
	line.WriteString(icon)
	line.WriteString(" ")
	line.WriteString(summary)

	// Add source and age on a second line if space permits
	if source != "" || age != "" {
		meta := ""
		if source != "" && age != "" {
			meta = fmt.Sprintf("  %s • %s", source, age)
		} else if source != "" {
			meta = fmt.Sprintf("  %s", source)
		} else {
			meta = fmt.Sprintf("  %s", age)
		}
		line.WriteString("\n")
		line.WriteString(lipgloss.NewStyle().Foreground(t.Subtext).Render(meta))
	}

	style := lipgloss.NewStyle().Foreground(color)
	if selected && m.IsFocused() {
		style = style.Bold(true).Background(t.Surface1)
	}

	return style.Render(line.String())
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// formatRelativeTime formats a duration as a relative time string.
func formatRelativeTime(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		mins := int(d.Minutes())
		if mins == 1 {
			return "1m ago"
		}
		return fmt.Sprintf("%dm ago", mins)
	case d < 24*time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "1h ago"
		}
		return fmt.Sprintf("%dh ago", hours)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1d ago"
		}
		return fmt.Sprintf("%dd ago", days)
	}
}

func attentionItemLess(a, b AttentionItem) bool {
	aRank := attentionActionabilityRank(a.Actionability)
	bRank := attentionActionabilityRank(b.Actionability)
	if aRank != bRank {
		return aRank > bRank
	}
	if !a.Timestamp.Equal(b.Timestamp) {
		return a.Timestamp.After(b.Timestamp)
	}
	return a.Cursor > b.Cursor
}

func attentionActionabilityRank(level robot.Actionability) int {
	switch level {
	case robot.ActionabilityActionRequired:
		return 3
	case robot.ActionabilityInteresting:
		return 2
	default:
		return 1
	}
}
