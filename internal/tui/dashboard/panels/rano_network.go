package panels

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// RanoNetworkRow is an aggregated per-agent row for the network activity panel.
type RanoNetworkRow struct {
	Label        string
	AgentType    string
	RequestCount int
	BytesOut     int64
	BytesIn      int64
	LastRequest  time.Time
}

// RanoNetworkPanelData holds the data for the rano network activity panel.
type RanoNetworkPanelData struct {
	Loaded    bool
	Enabled   bool
	Available bool
	Version   string

	// PollInterval is used for activity bucketing (best-effort).
	PollInterval time.Duration

	Rows []RanoNetworkRow

	TotalRequests int
	TotalBytesOut int64
	TotalBytesIn  int64

	Error error
}

// RanoNetworkPanel displays per-agent network activity sourced from rano (best-effort).
type RanoNetworkPanel struct {
	PanelBase
	data  RanoNetworkPanelData
	theme theme.Theme
}

func ranoNetworkConfig() PanelConfig {
	return PanelConfig{
		ID:              "rano_network",
		Title:           "Network Activity",
		Priority:        PriorityNormal,
		RefreshInterval: 1 * time.Second,
		MinWidth:        30,
		MinHeight:       8,
		Collapsible:     true,
	}
}

func NewRanoNetworkPanel() *RanoNetworkPanel {
	return &RanoNetworkPanel{
		PanelBase: NewPanelBase(ranoNetworkConfig()),
		theme:     theme.Current(),
	}
}

func (p *RanoNetworkPanel) Init() tea.Cmd { return nil }

func (p *RanoNetworkPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) { return p, nil }

func (p *RanoNetworkPanel) SetData(data RanoNetworkPanelData) {
	p.data = data
	if data.Error == nil && data.Loaded {
		p.SetLastUpdate(time.Now())
	}
}

func (p *RanoNetworkPanel) HasData() bool {
	return p.data.Loaded || p.data.Error != nil
}

func (p *RanoNetworkPanel) View() string {
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
	} else if p.data.Available && len(p.data.Rows) > 0 {
		borderColor = t.Green
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Background(bgColor).
		Width(w-2).
		Height(h-2).
		Padding(0, 1)

	var content strings.Builder

	title := lipgloss.NewStyle().Bold(true).Foreground(t.Text).Render("Network Activity")
	if p.data.Version != "" {
		title = fmt.Sprintf("%s %s%s%s", title, "\033[2m", p.data.Version, "\033[0m")
	}
	content.WriteString(title + "\n")

	if p.data.Error != nil {
		content.WriteString("\n" + components.RenderErrorState(components.ErrorStateOptions{
			Title:       "Network stats unavailable",
			Description: p.data.Error.Error(),
			Width:       w - 4,
		}))
		return boxStyle.Render(FitToHeight(content.String(), h-4))
	}

	if !p.data.Enabled {
		content.WriteString("\n" + components.RenderEmptyState(components.EmptyStateOptions{
			Icon:        components.IconExternal,
			Title:       "rano disabled",
			Description: "Enable integrations.rano in config",
			Width:       w - 4,
			Centered:    true,
		}))
		return boxStyle.Render(FitToHeight(content.String(), h-4))
	}

	if !p.data.Available {
		content.WriteString("\n" + components.RenderEmptyState(components.EmptyStateOptions{
			Icon:        components.IconWaiting,
			Title:       "rano not available",
			Description: "Install rano and grant required permissions",
			Width:       w - 4,
			Centered:    true,
		}))
		return boxStyle.Render(FitToHeight(content.String(), h-4))
	}

	if len(p.data.Rows) == 0 {
		content.WriteString("\n" + components.RenderEmptyState(components.EmptyStateOptions{
			Icon:        components.IconWaiting,
			Title:       "No agent traffic",
			Description: "No recent requests observed",
			Width:       w - 4,
			Centered:    true,
		}))
		return boxStyle.Render(FitToHeight(content.String(), h-4))
	}

	rows := append([]RanoNetworkRow(nil), p.data.Rows...)
	sort.Slice(rows, func(i, j int) bool {
		// Most-recent traffic first; fall back to higher bytes out.
		if !rows[i].LastRequest.Equal(rows[j].LastRequest) {
			return rows[i].LastRequest.After(rows[j].LastRequest)
		}
		return rows[i].BytesOut > rows[j].BytesOut
	})

	expanded := h >= 14

	content.WriteString("\n")
	content.WriteString(renderRanoTable(t, w-4, rows, p.data.PollInterval))
	if expanded {
		content.WriteString("\n")
		content.WriteString(fmt.Sprintf("Total: %d req  %s out  %s in\n",
			p.data.TotalRequests,
			formatBytesShort(p.data.TotalBytesOut),
			formatBytesShort(p.data.TotalBytesIn),
		))
		content.WriteString(renderRanoProviderBreakdown(t, w-4, rows))
	}

	return boxStyle.Render(FitToHeight(content.String(), h-4))
}

func renderRanoTable(t theme.Theme, width int, rows []RanoNetworkRow, pollInterval time.Duration) string {
	if width <= 0 {
		return ""
	}

	// Columns: Agent | Req | Out | In | Activity
	// Keep this simple and stable; don't try to fully auto-fit.
	reqW := 5
	outW := 8
	inW := 8
	actW := 9
	sep := "  "

	agentW := width - (reqW + outW + inW + actW + len(sep)*4)
	if agentW < 10 {
		agentW = 10
	}

	header := fmt.Sprintf("%-*s%s%*s%s%*s%s%*s%s%-*s",
		agentW, "Agent",
		sep, reqW, "Req",
		sep, outW, "Out",
		sep, inW, "In",
		sep, actW, "Activity",
	)
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Foreground(t.Subtext).Render(header) + "\n")

	for _, row := range rows {
		label := row.Label
		if label == "" {
			label = "(unknown)"
		}
		label = truncateWidth(label, agentW)

		activity := renderActivity(row.LastRequest, pollInterval)
		line := fmt.Sprintf("%-*s%s%*d%s%*s%s%*s%s%-*s",
			agentW, label,
			sep, reqW, row.RequestCount,
			sep, outW, formatBytesShort(row.BytesOut),
			sep, inW, formatBytesShort(row.BytesIn),
			sep, actW, activity,
		)
		b.WriteString(line + "\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

func renderRanoProviderBreakdown(t theme.Theme, width int, rows []RanoNetworkRow) string {
	// The underlying rano stats are per-process; provider here is best-effort based on agent type:
	// Claude -> anthropic, Codex -> openai, Gemini -> google.
	type agg struct {
		req int
		out int64
		in  int64
	}
	byProvider := map[string]*agg{
		"anthropic": {},
		"openai":    {},
		"google":    {},
		"unknown":   {},
	}
	for _, row := range rows {
		prov := providerFromAgentType(row.AgentType)
		a := byProvider[prov]
		if a == nil {
			a = &agg{}
			byProvider[prov] = a
		}
		a.req += row.RequestCount
		a.out += row.BytesOut
		a.in += row.BytesIn
	}

	order := []string{"anthropic", "openai", "google", "unknown"}
	var parts []string
	for _, key := range order {
		a := byProvider[key]
		if a == nil {
			continue
		}
		if a.req == 0 && a.out == 0 && a.in == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %d req (%s out)", key, a.req, formatBytesShort(a.out)))
	}
	if len(parts) == 0 {
		return ""
	}
	prefix := lipgloss.NewStyle().Foreground(t.Subtext).Render("By provider: ")
	line := prefix + strings.Join(parts, "  ")
	return truncateWidth(line, width) + "\n"
}

func providerFromAgentType(agentType string) string {
	switch agent.AgentType(agentType).Canonical() {
	case agent.AgentTypeClaudeCode:
		return "anthropic"
	case agent.AgentTypeCodex:
		return "openai"
	case agent.AgentTypeGemini:
		return "google"
	default:
		return "unknown"
	}
}

func renderActivity(last time.Time, pollInterval time.Duration) string {
	if last.IsZero() {
		return "(idle)"
	}
	age := time.Since(last)
	if pollInterval <= 0 {
		pollInterval = 1 * time.Second
	}
	switch {
	case age <= pollInterval:
		return "▲▲▲"
	case age <= 5*pollInterval:
		return "▲▲"
	case age <= 30*pollInterval:
		return "▲"
	default:
		return "(idle)"
	}
}

func truncateWidth(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	// Reserve space for ellipsis.
	if w <= 3 {
		return s[:w]
	}
	return lipgloss.NewStyle().MaxWidth(w).Render(s)
}

func formatBytesShort(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div := int64(unit)
	exp := 0
	for n := b / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	value := float64(b) / float64(div)
	suffixes := [...]string{"KB", "MB", "GB", "TB", "PB"}
	if exp >= len(suffixes) {
		exp = len(suffixes) - 1
	}
	suffix := suffixes[exp]
	if value >= 10 {
		return fmt.Sprintf("%.0f%s", value, suffix)
	}
	return fmt.Sprintf("%.1f%s", value, suffix)
}
