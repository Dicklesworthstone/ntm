package panels

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/cost"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

type CostTrend int

const (
	CostTrendFlat CostTrend = iota
	CostTrendUp
	CostTrendDown
)

func (t CostTrend) Arrow() string {
	switch t {
	case CostTrendUp:
		return "↑"
	case CostTrendDown:
		return "↓"
	default:
		return "→"
	}
}

type CostAgentRow struct {
	PaneTitle    string
	Model        string
	InputTokens  int
	OutputTokens int
	CostUSD      float64
	Trend        CostTrend
}

type CostPanelData struct {
	Agents          []CostAgentRow
	SessionTotalUSD float64
	LastHourUSD     float64

	DailyBudgetUSD float64
	BudgetUsedUSD  float64
}

type CostPanel struct {
	PanelBase
	data      CostPanelData
	theme     theme.Theme
	err       error
	table     table.Model
	tableInit bool
	scroll    *components.ScrollablePanel
	lastBody  string
}

func costConfig() PanelConfig {
	return PanelConfig{
		ID:              "cost",
		Title:           "Cost Tracking",
		Priority:        PriorityNormal,
		RefreshInterval: 10 * time.Second,
		MinWidth:        30,
		MinHeight:       8,
		Collapsible:     true,
	}
}

func NewCostPanel() *CostPanel {
	return &CostPanel{
		PanelBase: NewPanelBase(costConfig()),
		theme:     theme.Current(),
		scroll:    components.NewScrollablePanel(30, 8),
	}
}

func (c *CostPanel) Init() tea.Cmd { return nil }

func (c *CostPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if !c.IsFocused() || c.scroll == nil {
		return c, nil
	}
	var cmd tea.Cmd
	c.scroll, cmd = c.scroll.Update(msg)
	return c, cmd
}

func (c *CostPanel) SetData(data CostPanelData, err error) {
	// Keep a stable ordering even if callers don't sort.
	if len(data.Agents) > 1 {
		sort.SliceStable(data.Agents, func(i, j int) bool {
			if data.Agents[i].CostUSD == data.Agents[j].CostUSD {
				return strings.ToLower(data.Agents[i].PaneTitle) < strings.ToLower(data.Agents[j].PaneTitle)
			}
			return data.Agents[i].CostUSD > data.Agents[j].CostUSD
		})
	}

	c.data = data
	c.err = err
	c.tableInit = false // force table rebuild on next View()
	if err == nil {
		c.SetLastUpdate(time.Now())
	}
}

func (c *CostPanel) HasError() bool { return c.err != nil }

func (c *CostPanel) HasData() bool {
	if len(c.data.Agents) > 0 {
		return true
	}
	if c.data.SessionTotalUSD > 0 {
		return true
	}
	if c.data.DailyBudgetUSD > 0 {
		return true
	}
	return false
}

// HandlesOwnHeight returns true because the cost table is viewport-managed.
func (c *CostPanel) HandlesOwnHeight() bool {
	return true
}

func (c *CostPanel) View() string {
	t := c.theme
	w, h := c.Width(), c.Height()

	borderColor := t.Surface1
	bgColor := t.Base
	if c.IsFocused() {
		borderColor = t.Primary
		bgColor = t.Surface0
	}

	if c.data.DailyBudgetUSD > 0 && c.data.BudgetUsedUSD > 0 {
		usedPct := (c.data.BudgetUsedUSD / c.data.DailyBudgetUSD) * 100.0
		if usedPct >= 95.0 || c.data.BudgetUsedUSD >= c.data.DailyBudgetUSD {
			borderColor = t.Red
		} else if usedPct >= 80.0 {
			borderColor = t.Yellow
		}
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Background(bgColor).
		Width(w-2).
		Height(h-2).
		Padding(0, 1)

	var content strings.Builder

	title := c.Config().Title
	if c.err != nil {
		errorBadge := lipgloss.NewStyle().
			Background(t.Red).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render("!")
		title = title + " " + errorBadge
	} else if staleBadge := components.RenderStaleBadge(c.LastUpdate(), c.Config().RefreshInterval); staleBadge != "" {
		title = title + " " + staleBadge
	}

	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(t.Lavender).
		Border(lipgloss.NormalBorder(), false, false, true, false).
		BorderForeground(t.Surface1).
		Width(w - 4).
		Align(lipgloss.Center)

	content.WriteString(headerStyle.Render(title) + "\n")

	if c.err != nil {
		content.WriteString(components.ErrorState(c.err.Error(), "Waiting for refresh", w-4) + "\n")
		return boxStyle.Render(FitToHeight(content.String(), h-4))
	}

	if len(c.data.Agents) == 0 {
		content.WriteString("\n" + components.RenderEmptyState(components.EmptyStateOptions{
			Icon:        components.IconWaiting,
			Title:       "No cost data",
			Description: "Send prompts to record usage",
			Width:       w - 4,
			Centered:    true,
		}))
		return boxStyle.Render(FitToHeight(content.String(), h-4))
	}

	innerWidth := w - 4
	tableWidth := innerWidth
	if tableWidth < 0 {
		tableWidth = 0
	}

	// Totals
	totalLine := fmt.Sprintf("Session Total: %s", cost.FormatCost(c.data.SessionTotalUSD))
	if c.data.LastHourUSD > 0 {
		totalLine += fmt.Sprintf("  (1h: %s)", cost.FormatCost(c.data.LastHourUSD))
	}
	totals := []string{
		lipgloss.NewStyle().Foreground(t.Text).Bold(true).Render(totalLine),
	}

	if c.data.DailyBudgetUSD > 0 {
		remaining := c.data.DailyBudgetUSD - c.data.BudgetUsedUSD
		remainingStr := cost.FormatCost(remaining)

		budgetColor := t.Green
		if remaining <= 0 {
			budgetColor = t.Red
		} else {
			usedPct := (c.data.BudgetUsedUSD / c.data.DailyBudgetUSD) * 100.0
			if usedPct >= 95.0 {
				budgetColor = t.Red
			} else if usedPct >= 80.0 {
				budgetColor = t.Yellow
			}
		}

		budgetLine := fmt.Sprintf("Budget Left: %s  (limit: %s)", remainingStr, cost.FormatCost(c.data.DailyBudgetUSD))
		totals = append(totals, lipgloss.NewStyle().Foreground(budgetColor).Bold(true).Render(budgetLine))
	}

	footer := components.RenderFreshnessFooter(components.FreshnessOptions{
		LastUpdate:      c.LastUpdate(),
		RefreshInterval: c.Config().RefreshInterval,
		Width:           w - 4,
	})

	innerHeight := h - 4
	headerHeight := lipgloss.Height(headerStyle.Render(title)) + 1
	reservedHeight := headerHeight + len(totals) + 1
	if footer != "" {
		reservedHeight += lipgloss.Height(footer)
	}
	bodyHeight := innerHeight - reservedHeight
	if bodyHeight < 3 {
		bodyHeight = 3
	}

	// Build the full table and let the shared scrollable viewport handle overflow.
	// Only reinitialize when data changes (SetData resets tableInit to false).
	if !c.tableInit {
		c.initCostTable(tableWidth, len(c.data.Agents))
		c.tableInit = true
	}
	if c.scroll == nil {
		c.scroll = components.NewScrollablePanel(innerWidth, bodyHeight)
	}
	c.scroll.SetSize(innerWidth, bodyHeight)
	body := c.table.View()
	if body != c.lastBody {
		c.scroll.SetContent(body)
		c.lastBody = body
	}
	content.WriteString(c.scroll.RenderWithIndicators(innerWidth) + "\n")

	for _, line := range totals {
		content.WriteString(line + "\n")
	}

	if footer != "" {
		content.WriteString(footer + "\n")
	}

	return boxStyle.Render(FitToHeight(content.String(), h-4))
}

// initCostTable initializes or reconfigures the bubbles/table for cost data.
func (c *CostPanel) initCostTable(tableWidth, maxRows int) {
	t := c.theme

	cols := c.costTableColumns(tableWidth)
	rows := c.costTableRows(cols, maxRows)

	s := table.DefaultStyles()
	s.Header = s.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(t.Surface1).
		BorderBottom(true).
		Bold(true).
		Foreground(t.Lavender)
	s.Selected = s.Selected.
		Foreground(t.Text).
		Background(t.Surface0).
		Bold(false)
	s.Cell = s.Cell.
		Foreground(t.Subtext)

	tableHeight := len(rows)
	if tableHeight < 1 {
		tableHeight = 1
	}
	if tableHeight > maxRows {
		tableHeight = maxRows
	}

	c.table = table.New(
		table.WithColumns(cols),
		table.WithRows(rows),
		table.WithFocused(false),
		table.WithWidth(max(10, tableWidth)),
		table.WithHeight(tableHeight),
		table.WithStyles(s),
	)
	c.tableInit = true
}

// costTableColumns returns adaptive table columns based on available width.
func (c *CostPanel) costTableColumns(tableWidth int) []table.Column {
	showTokens := tableWidth >= 44
	showOut := tableWidth >= 36

	inW := 7
	outW := 7
	costW := 8
	trendW := 2

	fixedW := costW + trendW
	if showTokens {
		fixedW += inW
	}
	if showOut {
		fixedW += outW
	}
	nameW := tableWidth - fixedW
	if nameW < 8 {
		nameW = 8
	}

	var cols []table.Column
	cols = append(cols, table.Column{Title: "Agent", Width: nameW})
	if showTokens {
		cols = append(cols, table.Column{Title: "In", Width: inW})
	}
	if showOut {
		cols = append(cols, table.Column{Title: "Out", Width: outW})
	}
	cols = append(cols, table.Column{Title: "Cost", Width: costW})
	cols = append(cols, table.Column{Title: "", Width: trendW})
	return cols
}

// costTableRows builds table rows from the current data.
func (c *CostPanel) costTableRows(cols []table.Column, maxRows int) []table.Row {
	showTokens := len(cols) >= 4 // Agent + In + ... (4+ columns means In is present)
	showOut := len(cols) >= 4    // Check by column title presence instead
	for _, col := range cols {
		if col.Title == "In" {
			showTokens = true
		}
		if col.Title == "Out" {
			showOut = true
		}
	}

	nameW := 8
	if len(cols) > 0 {
		nameW = cols[0].Width
	}

	var rows []table.Row
	for i, agent := range c.data.Agents {
		if i >= maxRows {
			break
		}
		name := layout.TruncatePaneTitle(agent.PaneTitle, nameW)
		row := []string{name}
		if showTokens {
			row = append(row, formatTokenShort(agent.InputTokens))
		}
		if showOut {
			row = append(row, formatTokenShort(agent.OutputTokens))
		}
		row = append(row, cost.FormatCost(agent.CostUSD))
		row = append(row, agent.Trend.Arrow())
		rows = append(rows, row)
	}
	return rows
}

func formatTokenShort(tokens int) string {
	if tokens <= 0 {
		return "0"
	}
	if tokens < 1000 {
		return fmt.Sprintf("%d", tokens)
	}
	if tokens < 1000000 {
		return fmt.Sprintf("%.1fK", float64(tokens)/1000.0)
	}
	return fmt.Sprintf("%.1fM", float64(tokens)/1000000.0)
}
