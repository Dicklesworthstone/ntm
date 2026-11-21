package panels

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// MailInboxItem pairs an Agent Mail inbox message with its recipient agent/pane.
type MailInboxItem struct {
	AgentName string
	PaneID    string
	Message   agentmail.InboxMessage
}

// MailInboxData holds the aggregated Agent Mail inbox state for the session.
type MailInboxData struct {
	Items       []MailInboxItem
	UnreadCount int
	UrgentCount int
	Available   bool
	Error       error
}

// MailInboxPanel renders unread and urgent Agent Mail threads across session
// agents with keyboard navigation and inline snippet preview.
type MailInboxPanel struct {
	PanelBase
	data     MailInboxData
	selected int
	expanded bool
	theme    theme.Theme
}

func mailInboxConfig() PanelConfig {
	return PanelConfig{
		ID:              "mail_inbox",
		Title:           "Agent Mail Inbox",
		Priority:        PriorityHigh,
		RefreshInterval: 5 * time.Second,
		MinWidth:        30,
		MinHeight:       7,
		Collapsible:     true,
	}
}

// NewMailInboxPanel creates a new Agent Mail Inbox operational panel.
func NewMailInboxPanel() *MailInboxPanel {
	return &MailInboxPanel{
		PanelBase: NewPanelBase(mailInboxConfig()),
		theme:     theme.Current(),
	}
}

func (p *MailInboxPanel) Init() tea.Cmd { return nil }

func (p *MailInboxPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok || !p.IsFocused() {
		return p, nil
	}
	switch keyMsg.String() {
	case "j", "down":
		if p.selected < len(p.data.Items)-1 {
			p.selected++
		}
	case "k", "up":
		if p.selected > 0 {
			p.selected--
		}
	case "enter":
		if len(p.data.Items) > 0 {
			p.expanded = !p.expanded
		}
	}
	return p, nil
}

// SetData stores a sorted copy of inbox items (urgent first, then unread, then newest).
func (p *MailInboxPanel) SetData(data MailInboxData) {
	items := append([]MailInboxItem(nil), data.Items...)
	sort.SliceStable(items, func(i, j int) bool {
		ui := isUrgentImportance(items[i].Message.Importance)
		uj := isUrgentImportance(items[j].Message.Importance)
		if ui != uj {
			return ui
		}
		if items[i].Message.ReadAt == nil && items[j].Message.ReadAt != nil {
			return true
		}
		if items[i].Message.ReadAt != nil && items[j].Message.ReadAt == nil {
			return false
		}
		return items[i].Message.CreatedTS.After(items[j].Message.CreatedTS.Time)
	})
	data.Items = items
	unread := 0
	urgent := 0
	for _, item := range items {
		if item.Message.ReadAt == nil {
			unread++
		}
		if isUrgentImportance(item.Message.Importance) {
			urgent++
		}
	}
	data.UnreadCount = unread
	data.UrgentCount = urgent
	p.data = data
	if p.selected >= len(p.data.Items) {
		p.selected = max(0, len(p.data.Items)-1)
	}
	if data.Error == nil {
		p.SetLastUpdate(time.Now())
	}
}

// HasError reports whether Agent Mail retrieval failed.
func (p *MailInboxPanel) HasError() bool {
	return p.data.Error != nil
}

func (p *MailInboxPanel) Keybindings() []Keybinding {
	return []Keybinding{
		{Key: key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("j/k", "select message")), Description: "Navigate inbox", Action: "navigate"},
		{Key: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "toggle body")), Description: "Preview message", Action: "toggle"},
	}
}

func (p *MailInboxPanel) View() string {
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
	} else if p.data.UrgentCount > 0 {
		borderColor = t.Red
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
	if p.data.UrgentCount > 0 {
		title += " " + lipgloss.NewStyle().Foreground(t.Red).Bold(true).Render(fmt.Sprintf("(%d urgent)", p.data.UrgentCount))
	} else if p.data.UnreadCount > 0 {
		title += " " + lipgloss.NewStyle().Foreground(t.Yellow).Bold(true).Render(fmt.Sprintf("(%d unread)", p.data.UnreadCount))
	}

	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(t.Lavender).
		Border(lipgloss.NormalBorder(), false, false, true, false).
		BorderForeground(t.Surface1).
		Width(innerW).
		Align(lipgloss.Center)
	content.WriteString(headerStyle.Render(layout.TruncateWidthDefault(title, innerW)) + "\n")

	if p.HasError() {
		content.WriteString(components.ErrorState(p.data.Error.Error(), "Press m to retry", innerW) + "\n")
		return boxStyle.Render(FitToHeight(content.String(), max(1, h-4)))
	}
	if !p.data.Available {
		content.WriteString(components.RenderEmptyState(components.EmptyStateOptions{
			Icon:        components.IconWaiting,
			Title:       "Agent Mail unavailable",
			Description: "Start Agent Mail server to view inbox",
			Width:       innerW,
			Centered:    true,
		}))
		return boxStyle.Render(FitToHeight(content.String(), max(1, h-4)))
	}
	if len(p.data.Items) == 0 {
		content.WriteString(components.RenderEmptyState(components.EmptyStateOptions{
			Icon:        components.IconSuccess,
			Title:       "Inbox clear",
			Description: "No messages for session agents",
			Width:       innerW,
			Centered:    true,
		}))
		return boxStyle.Render(FitToHeight(content.String(), max(1, h-4)))
	}

	maxRows := max(1, h-6)
	if p.expanded && maxRows > 2 {
		maxRows -= 2
	}
	start := 0
	if p.selected >= maxRows {
		start = p.selected - maxRows + 1
	}
	end := min(len(p.data.Items), start+maxRows)

	for i := start; i < end; i++ {
		item := p.data.Items[i]
		badge := "○"
		color := t.Subtext
		if isUrgentImportance(item.Message.Importance) {
			badge = "!"
			color = t.Red
		} else if item.Message.ReadAt == nil {
			badge = "●"
			color = t.Yellow
		}
		line := fmt.Sprintf("%s %s→%s: %s", badge, item.Message.From, item.AgentName, item.Message.Subject)
		line = layout.TruncateWidthDefault(line, innerW-2)

		prefix := "  "
		style := lipgloss.NewStyle().Foreground(color)
		if i == p.selected && p.IsFocused() {
			prefix = "▸ "
			style = style.Bold(true)
		}
		content.WriteString(style.Render(prefix+line) + "\n")
	}

	if p.expanded && p.selected >= 0 && p.selected < len(p.data.Items) {
		body := strings.TrimSpace(strings.ReplaceAll(p.data.Items[p.selected].Message.BodyMD, "\n", " "))
		if body == "" {
			body = "(no body cached)"
		}
		content.WriteString(lipgloss.NewStyle().Foreground(t.Text).Render("  "+layout.TruncateWidthDefault(body, innerW-2)) + "\n")
	}

	hint := "j/k: nav • enter: preview • m: refresh"
	content.WriteString(lipgloss.NewStyle().Foreground(t.Subtext).Render(layout.TruncateWidthDefault(hint, innerW)))
	return boxStyle.Render(FitToHeight(content.String(), max(1, h-4)))
}

func isUrgentImportance(importance string) bool {
	imp := strings.ToLower(strings.TrimSpace(importance))
	return imp == "urgent" || imp == "high"
}
