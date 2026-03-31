// Package panels provides dashboard panel components.
// context.go implements the context prediction status panel.
package panels

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// AgentContextStatus represents context prediction info for a single agent.
type AgentContextStatus struct {
	PaneID              string  // Pane identifier (e.g., "myproject__cc_1")
	AgentType           string  // "cc", "cod", "gmi"
	AgentName           string  // Agent Mail name if available
	CurrentUsage        float64 // 0.0-1.0
	CurrentTokens       int64
	ContextLimit        int64
	TokenVelocity       float64 // Tokens per minute
	MinutesToExhaustion float64 // 0 if stable/decreasing
	ShouldWarn          bool
	ShouldCompact       bool
	SampleCount         int
}

// ContextPanelData holds the data for the context prediction panel.
type ContextPanelData struct {
	Agents      []AgentContextStatus
	HighUsage   int // Count of agents with high usage (>75%)
	Warning     int // Count of agents with warning
	NeedCompact int // Count of agents needing compaction
}

// ContextPanel displays context prediction status for agents.
type ContextPanel struct {
	PanelBase
	data  ContextPanelData
	theme theme.Theme
	err   error
}

// contextConfig returns the configuration for the context panel.
func contextConfig() PanelConfig {
	return PanelConfig{
		ID:              "context",
		Title:           "Context Status",
		Priority:        PriorityHigh, // Context exhaustion is important
		RefreshInterval: 10 * time.Second,
		MinWidth:        35,
		MinHeight:       6,
		Collapsible:     true,
	}
}

// NewContextPanel creates a new context prediction panel.
func NewContextPanel() *ContextPanel {
	return &ContextPanel{
		PanelBase: NewPanelBase(contextConfig()),
		theme:     theme.Current(),
	}
}

// Init implements tea.Model.
func (c *ContextPanel) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (c *ContextPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return c, nil
}

// SetData updates the panel data.
func (c *ContextPanel) SetData(data ContextPanelData, err error) {
	c.data = data
	c.err = err
	if err == nil {
		c.SetLastUpdate(time.Now())
	}
}

// SetDataFromPredictions populates panel data from context predictions.
func (c *ContextPanel) SetDataFromPredictions(predictions map[string]*context.Prediction, paneInfo map[string]PaneContextInfo) {
	agents := make([]AgentContextStatus, 0, len(predictions))

	highUsage := 0
	warning := 0
	needCompact := 0

	for paneID, pred := range predictions {
		if pred == nil {
			continue
		}

		info := paneInfo[paneID]
		status := AgentContextStatus{
			PaneID:              paneID,
			AgentType:           info.AgentType,
			AgentName:           info.AgentName,
			CurrentUsage:        pred.CurrentUsage,
			CurrentTokens:       pred.CurrentTokens,
			ContextLimit:        pred.ContextLimit,
			TokenVelocity:       pred.TokenVelocity,
			MinutesToExhaustion: pred.MinutesToExhaustion,
			ShouldWarn:          pred.ShouldWarn,
			ShouldCompact:       pred.ShouldCompact,
			SampleCount:         pred.SampleCount,
		}
		agents = append(agents, status)

		if pred.CurrentUsage > 0.75 {
			highUsage++
		}
		if pred.ShouldWarn {
			warning++
		}
		if pred.ShouldCompact {
			needCompact++
		}
	}

	c.data = ContextPanelData{
		Agents:      agents,
		HighUsage:   highUsage,
		Warning:     warning,
		NeedCompact: needCompact,
	}
	c.err = nil
	c.SetLastUpdate(time.Now())
}

// PaneContextInfo provides additional info about a pane.
type PaneContextInfo struct {
	AgentType string
	AgentName string
}

// HasError returns true if there's an active error.
func (c *ContextPanel) HasError() bool {
	return c.err != nil
}

// HasWarning returns true if any agent has a warning.
func (c *ContextPanel) HasWarning() bool {
	return c.data.Warning > 0
}

// HasCritical returns true if any agent needs compaction.
func (c *ContextPanel) HasCritical() bool {
	return c.data.NeedCompact > 0
}

// Keybindings returns context panel specific shortcuts.
func (c *ContextPanel) Keybindings() []Keybinding {
	return []Keybinding{
		{
			Key:         key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
			Description: "Refresh context data",
			Action:      "refresh",
		},
		{
			Key:         key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "compact")),
			Description: "Trigger compaction for selected agent",
			Action:      "compact",
		},
		{
			Key:         key.NewBinding(key.WithKeys("C"), key.WithHelp("C", "compact all")),
			Description: "Compact all warning agents",
			Action:      "compact_all",
		},
	}
}

// View renders the panel.
func (c *ContextPanel) View() string {
	t := c.theme
	w, h := c.Width(), c.Height()

	// Create border style based on focus and alert state
	borderColor := t.Surface1
	bgColor := t.Base
	if c.IsFocused() {
		borderColor = t.Primary
		bgColor = t.Surface0
	}
	// Override border color for critical states
	if c.data.NeedCompact > 0 {
		borderColor = t.Red
	} else if c.data.Warning > 0 {
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

	// Build header with status badges
	title := c.Config().Title
	if c.err != nil {
		errorBadge := lipgloss.NewStyle().
			Background(t.Red).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render("⚠ Error")
		title = title + " " + errorBadge
	} else if c.data.NeedCompact > 0 {
		criticalBadge := lipgloss.NewStyle().
			Background(t.Red).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render(fmt.Sprintf("⚠ %d critical", c.data.NeedCompact))
		title = title + " " + criticalBadge
	} else if c.data.Warning > 0 {
		warnBadge := lipgloss.NewStyle().
			Background(t.Yellow).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render(fmt.Sprintf("⚡ %d warning", c.data.Warning))
		title = title + " " + warnBadge
	} else if staleBadge := components.RenderStaleBadge(c.LastUpdate(), c.Config().RefreshInterval); staleBadge != "" {
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
	if c.err != nil {
		content.WriteString(components.ErrorState(c.err.Error(), "Press r to retry", w-4) + "\n")
		return boxStyle.Render(FitToHeight(content.String(), h-4))
	}

	// Empty state: no agents
	if len(c.data.Agents) == 0 {
		content.WriteString("\n" + components.RenderEmptyState(components.EmptyStateOptions{
			Icon:        components.IconWaiting,
			Title:       "No context data",
			Description: "Predictions appear after agents run",
			Width:       w - 4,
			Centered:    true,
		}))
		return boxStyle.Render(FitToHeight(content.String(), h-4))
	}

	content.WriteString("\n")

	// Per-Agent Context Status
	availHeight := h - 6 // header + footer
	if availHeight < 0 {
		availHeight = 0
	}

	for i, agent := range c.data.Agents {
		if i >= availHeight/3 { // 3 lines per agent
			remaining := len(c.data.Agents) - i
			content.WriteString(lipgloss.NewStyle().
				Foreground(t.Overlay).
				Render(fmt.Sprintf("...and %d more", remaining)) + "\n")
			break
		}

		// Determine color based on state
		var statusColor lipgloss.Color
		var statusIcon string
		switch {
		case agent.ShouldCompact:
			statusColor = t.Red
			statusIcon = "⚠️"
		case agent.ShouldWarn:
			statusColor = t.Yellow
			statusIcon = "⚡"
		case agent.CurrentUsage > 0.60:
			statusColor = t.Peach
			statusIcon = "●"
		default:
			statusColor = t.Green
			statusIcon = "●"
		}

		typeColor := contextAgentTypeColor(agent.AgentType, t)

		// Name (use pane ID if no agent name)
		displayName := agent.AgentName
		if displayName == "" {
			displayName = agent.PaneID
		}

		// First line: status icon + name + usage percentage
		nameStyle := lipgloss.NewStyle().Foreground(typeColor).Bold(true)
		usagePct := fmt.Sprintf("%.0f%%", agent.CurrentUsage*100)
		usageStyle := lipgloss.NewStyle().Foreground(statusColor).Bold(true)

		line1 := fmt.Sprintf("%s %s  %s",
			statusIcon,
			nameStyle.Render(displayName),
			usageStyle.Render(usagePct),
		)

		// Second line: time remaining or velocity info
		var line2 string
		if agent.MinutesToExhaustion > 0 {
			minutesStr := formatMinutes(agent.MinutesToExhaustion)
			line2 = fmt.Sprintf("   Est. exhaustion in %s", minutesStr)
			if agent.ShouldCompact {
				line2 = lipgloss.NewStyle().Foreground(t.Red).Render(line2)
			} else if agent.ShouldWarn {
				line2 = lipgloss.NewStyle().Foreground(t.Yellow).Render(line2)
			} else {
				line2 = lipgloss.NewStyle().Foreground(t.Subtext).Render(line2)
			}
		} else if agent.TokenVelocity > 0 {
			line2 = lipgloss.NewStyle().Foreground(t.Subtext).
				Render(fmt.Sprintf("   %.0f tok/min", agent.TokenVelocity))
		} else {
			line2 = lipgloss.NewStyle().Foreground(t.Overlay).
				Render("   Stable")
		}

		content.WriteString(line1 + "\n")
		content.WriteString(line2 + "\n")

		// Third line: mini progress bar
		barColor := string(statusColor)
		miniBar := styles.ProgressBar(agent.CurrentUsage, w-6, "━", "┄", barColor)
		content.WriteString(miniBar + "\n")
	}

	// Freshness footer
	if footer := components.RenderFreshnessFooter(components.FreshnessOptions{
		LastUpdate:      c.LastUpdate(),
		RefreshInterval: c.Config().RefreshInterval,
		Width:           w - 4,
	}); footer != "" {
		content.WriteString(footer + "\n")
	}

	return boxStyle.Render(FitToHeight(content.String(), h-4))
}

func contextAgentTypeColor(agentType string, t theme.Theme) lipgloss.Color {
	switch agent.AgentType(agentType).Canonical() {
	case agent.AgentTypeClaudeCode:
		return t.Claude
	case agent.AgentTypeCodex:
		return t.Codex
	case agent.AgentTypeGemini:
		return t.Gemini
	case agent.AgentTypeCursor:
		return t.Cursor
	case agent.AgentTypeWindsurf:
		return t.Windsurf
	case agent.AgentTypeAider:
		return t.Aider
	case agent.AgentTypeOllama:
		return t.Ollama
	case agent.AgentTypeUser:
		return t.User
	default:
		return t.Text
	}
}

// formatMinutes formats a duration in minutes to a human-readable string.
func formatMinutes(minutes float64) string {
	if minutes < 1 {
		return fmt.Sprintf("%.0f sec", minutes*60)
	}
	if minutes < 60 {
		return fmt.Sprintf("%.0f min", minutes)
	}
	hours := minutes / 60
	if hours < 24 {
		return fmt.Sprintf("%.1f hr", hours)
	}
	return fmt.Sprintf("%.1f days", hours/24)
}
