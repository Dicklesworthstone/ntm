package panels

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/tools"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// QuotaData holds the data for the quota panel
type QuotaData struct {
	Status    *tools.CautStatus
	Usages    []tools.CautUsage
	Available bool
	Error     error
}

// QuotaPanel displays caut usage/quota information
type QuotaPanel struct {
	PanelBase
	data  QuotaData
	theme theme.Theme
}

// quotaConfig returns the configuration for the quota panel
func quotaConfig() PanelConfig {
	return PanelConfig{
		ID:              "quota",
		Title:           "Usage & Costs",
		Priority:        PriorityNormal,
		RefreshInterval: 30 * time.Second,
		MinWidth:        30,
		MinHeight:       6,
		Collapsible:     true,
	}
}

// NewQuotaPanel creates a new quota panel
func NewQuotaPanel() *QuotaPanel {
	return &QuotaPanel{
		PanelBase: NewPanelBase(quotaConfig()),
		theme:     theme.Current(),
	}
}

// Init implements tea.Model
func (q *QuotaPanel) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model
func (q *QuotaPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return q, nil
}

// SetData updates the panel data directly
func (q *QuotaPanel) SetData(data QuotaData) {
	q.data = data
	if data.Error == nil {
		q.SetLastUpdate(time.Now())
	}
}

// HasError returns true if there's an active error
func (q *QuotaPanel) HasError() bool {
	return q.data.Error != nil
}

// Keybindings returns quota panel specific shortcuts
func (q *QuotaPanel) Keybindings() []Keybinding {
	return []Keybinding{
		{
			Key:         key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
			Description: "Refresh quota data",
			Action:      "refresh",
		},
	}
}

// View renders the panel
func (q *QuotaPanel) View() string {
	t := q.theme
	w, h := q.Width(), q.Height()

	// Create border style based on focus
	borderColor := t.Surface1
	bgColor := t.Base
	if q.IsFocused() {
		borderColor = t.Primary
		bgColor = t.Surface0
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Background(bgColor).
		Width(w-2).
		Height(h-2).
		Padding(0, 1)

	var content strings.Builder

	// Build header with stale/error badge if needed
	title := q.Config().Title
	if q.data.Error != nil {
		errorBadge := lipgloss.NewStyle().
			Background(t.Red).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render("!")
		title = title + " " + errorBadge
	} else if staleBadge := components.RenderStaleBadge(q.LastUpdate(), q.Config().RefreshInterval); staleBadge != "" {
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
	if q.data.Error != nil {
		content.WriteString(components.ErrorState(q.data.Error.Error(), "Press r to retry", w-4) + "\n")
		return boxStyle.Render(FitToHeight(content.String(), h-4))
	}

	// Empty state: no data available
	if !q.data.Available && q.data.Status == nil && len(q.data.Usages) == 0 {
		content.WriteString("\n" + components.RenderEmptyState(components.EmptyStateOptions{
			Icon:        components.IconWaiting,
			Title:       "No usage data",
			Description: "caut not available",
			Width:       w - 4,
			Centered:    true,
		}))
		return boxStyle.Render(FitToHeight(content.String(), h-4))
	}

	content.WriteString("\n")

	// Show total usage if status is available
	sem := t.Semantic()
	if q.data.Status != nil {
		// Overall quota bar
		quotaPct := q.data.Status.QuotaPercent / 100.0
		if quotaPct > 1.0 {
			quotaPct = 1.0
		}

		statusKind := "success"
		statusIcon := "●"
		if q.data.Status.QuotaPercent >= 95 {
			statusKind = "error"
			statusIcon = "!"
		} else if q.data.Status.QuotaPercent >= 80 {
			statusKind = "warning"
			statusIcon = "⚠"
		}
		statusColor := sem.StatusColor(statusKind)
		barColor := string(statusColor)

		bar := styles.ProgressBar(quotaPct, w-6, "█", "░", barColor)

		statusBadge := lipgloss.NewStyle().Foreground(statusColor).Render(statusIcon)
		quotaLabel := fmt.Sprintf("%.1f%% used", q.data.Status.QuotaPercent)
		content.WriteString(statusBadge + " " + lipgloss.NewStyle().Foreground(t.Text).Render(quotaLabel) + "\n")
		content.WriteString(bar + "\n")

		// Total spend
		if q.data.Status.TotalSpend > 0 {
			spendStr := fmt.Sprintf("$%.2f today", q.data.Status.TotalSpend)
			content.WriteString(lipgloss.NewStyle().Foreground(t.Subtext).Align(lipgloss.Right).Width(w-6).Render(spendStr) + "\n")
		}
		content.WriteString("\n")
	}

	// Per-provider usage
	availHeight := h - 10
	if availHeight < 2 {
		availHeight = 2
	}

	providerCount := 0
	for _, usage := range q.data.Usages {
		if providerCount >= availHeight/2 {
			remaining := len(q.data.Usages) - providerCount
			if remaining > 0 {
				content.WriteString(lipgloss.NewStyle().Foreground(t.Overlay).Render(fmt.Sprintf("+%d more", remaining)) + "\n")
			}
			break
		}

		// Provider name with semantic color
		providerColor := sem.AgentColor(semanticProviderAgentKey(usage.Provider))
		if providerColor == sem.AgentUnknown {
			providerColor = t.Green
		}

		name := lipgloss.NewStyle().Foreground(providerColor).Bold(true).Render(usage.Provider)

		// Cost and token info
		costStr := fmt.Sprintf("$%.2f", usage.Cost)
		tokenStr := fmt.Sprintf("%dk tok", (usage.TokensIn+usage.TokensOut)/1000)
		info := lipgloss.NewStyle().Foreground(t.Overlay).Render(tokenStr + " " + costStr)

		gap := w - 6 - lipgloss.Width(name) - lipgloss.Width(info)
		if gap < 1 {
			gap = 1
		}

		line := name + strings.Repeat(" ", gap) + info
		content.WriteString(line + "\n")

		providerCount++
	}

	// Add freshness indicator at the bottom
	if footer := components.RenderFreshnessFooter(components.FreshnessOptions{
		LastUpdate:      q.LastUpdate(),
		RefreshInterval: q.Config().RefreshInterval,
		Width:           w - 4,
	}); footer != "" {
		content.WriteString(footer + "\n")
	}

	return boxStyle.Render(FitToHeight(content.String(), h-4))
}
