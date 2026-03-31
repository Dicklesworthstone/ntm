package panels

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wordwrap"

	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// historyConfig returns the configuration for the history panel
func historyConfig() PanelConfig {
	return PanelConfig{
		ID:              "history",
		Title:           "Command History",
		Priority:        PriorityNormal,
		RefreshInterval: 30 * time.Second, // Slow refresh, history doesn't change often
		MinWidth:        35,
		MinHeight:       8,
		Collapsible:     true,
	}
}

type historyStatusFilter int

const (
	historyStatusAll historyStatusFilter = iota
	historyStatusSuccessOnly
	historyStatusFailureOnly
)

func (f historyStatusFilter) Next() historyStatusFilter {
	switch f {
	case historyStatusAll:
		return historyStatusSuccessOnly
	case historyStatusSuccessOnly:
		return historyStatusFailureOnly
	default:
		return historyStatusAll
	}
}

func (f historyStatusFilter) Label() string {
	switch f {
	case historyStatusSuccessOnly:
		return "✓ only"
	case historyStatusFailureOnly:
		return "✗ only"
	default:
		return "all"
	}
}

type historyAgentFilter int

const (
	historyAgentAll historyAgentFilter = iota
	historyAgentClaude
	historyAgentCodex
	historyAgentGemini
)

func (f historyAgentFilter) Next() historyAgentFilter {
	switch f {
	case historyAgentAll:
		return historyAgentClaude
	case historyAgentClaude:
		return historyAgentCodex
	case historyAgentCodex:
		return historyAgentGemini
	default:
		return historyAgentAll
	}
}

func (f historyAgentFilter) Label() string {
	switch f {
	case historyAgentClaude:
		return "Claude"
	case historyAgentCodex:
		return "Codex"
	case historyAgentGemini:
		return "Gemini"
	default:
		return "any"
	}
}

func (f historyAgentFilter) AgentType() tmux.AgentType {
	switch f {
	case historyAgentClaude:
		return tmux.AgentClaude
	case historyAgentCodex:
		return tmux.AgentCodex
	case historyAgentGemini:
		return tmux.AgentGemini
	default:
		return ""
	}
}

type historyTimeFilter int

const (
	historyTimeAll historyTimeFilter = iota
	historyTime1h
	historyTime24h
	historyTime7d
)

func (f historyTimeFilter) Next() historyTimeFilter {
	switch f {
	case historyTimeAll:
		return historyTime1h
	case historyTime1h:
		return historyTime24h
	case historyTime24h:
		return historyTime7d
	default:
		return historyTimeAll
	}
}

func (f historyTimeFilter) Label() string {
	switch f {
	case historyTime1h:
		return "1h"
	case historyTime24h:
		return "24h"
	case historyTime7d:
		return "7d"
	default:
		return "all-time"
	}
}

func (f historyTimeFilter) Cutoff(now time.Time) (time.Time, bool) {
	switch f {
	case historyTime1h:
		return now.Add(-1 * time.Hour), true
	case historyTime24h:
		return now.Add(-24 * time.Hour), true
	case historyTime7d:
		return now.Add(-7 * 24 * time.Hour), true
	default:
		return time.Time{}, false
	}
}

type historyPaneMeta struct {
	Label     string
	AgentType tmux.AgentType
}

// HistoryPanel displays command history
type HistoryPanel struct {
	PanelBase
	entries        []history.HistoryEntry
	visibleEntries []history.HistoryEntry
	cursor         int
	offset         int
	theme          theme.Theme
	err            error

	panesByIndex map[string]historyPaneMeta

	statusFilter historyStatusFilter
	agentFilter  historyAgentFilter
	timeFilter   historyTimeFilter

	showPreview   bool
	previewScroll int

	// bubbles/table for rendering
	table     table.Model
	tableInit bool // true after first table setup
}

// NewHistoryPanel creates a new history panel
func NewHistoryPanel() *HistoryPanel {
	return &HistoryPanel{
		PanelBase:    NewPanelBase(historyConfig()),
		theme:        theme.Current(),
		statusFilter: historyStatusAll,
		agentFilter:  historyAgentAll,
		timeFilter:   historyTimeAll,
	}
}

// HasError returns true if there's an active error
func (m *HistoryPanel) HasError() bool {
	return m.err != nil
}

// SetPanes provides pane metadata used for agent-type filtering and rendering targets.
func (m *HistoryPanel) SetPanes(panes []tmux.Pane) {
	if len(panes) == 0 {
		m.panesByIndex = nil
		m.applyFilters()
		return
	}

	meta := make(map[string]historyPaneMeta, len(panes))
	for _, p := range panes {
		key := strconv.Itoa(p.Index)
		meta[key] = historyPaneMeta{
			Label:     formatHistoryPaneLabel(p),
			AgentType: p.Type,
		}
	}
	m.panesByIndex = meta
	m.applyFilters()
}

// Init implements tea.Model
func (m *HistoryPanel) Init() tea.Cmd {
	return nil
}

// ReplayMsg is sent when user wants to replay a history entry
type ReplayMsg struct {
	Entry history.HistoryEntry
}

// CopyMsg is sent when user wants to copy the selected prompt.
type CopyMsg struct {
	Text string
}

// Update implements tea.Model
func (m *HistoryPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if !m.IsFocused() {
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		// Preview overlay consumes keys first.
		if m.showPreview {
			switch msg.String() {
			case "esc", "q":
				m.showPreview = false
				m.previewScroll = 0
				return m, nil
			case "up", "k":
				if m.previewScroll > 0 {
					m.previewScroll--
				}
				return m, nil
			case "down", "j":
				if m.previewScroll < m.previewMaxScroll() {
					m.previewScroll++
				}
				return m, nil
			case "y":
				if entry, ok := m.selectedEntry(); ok {
					return m, func() tea.Msg { return CopyMsg{Text: entry.Prompt} }
				}
				return m, nil
			case "enter":
				if entry, ok := m.selectedEntry(); ok {
					m.showPreview = false
					m.previewScroll = 0
					return m, func() tea.Msg { return ReplayMsg{Entry: entry} }
				}
				return m, nil
			default:
				return m, nil
			}
		}

		switch msg.String() {
		case "enter":
			// Replay selected entry.
			if entry, ok := m.selectedEntry(); ok {
				return m, func() tea.Msg { return ReplayMsg{Entry: entry} }
			}
		case "v":
			// Preview full prompt for the selected entry.
			if _, ok := m.selectedEntry(); ok {
				m.showPreview = true
				m.previewScroll = 0
			}
		case "y":
			// Copy selected prompt to clipboard.
			if entry, ok := m.selectedEntry(); ok {
				return m, func() tea.Msg { return CopyMsg{Text: entry.Prompt} }
			}
		case "f":
			// Cycle success/failure filter.
			m.statusFilter = m.statusFilter.Next()
			m.applyFilters()
		case "a":
			// Cycle agent-type filter.
			m.agentFilter = m.agentFilter.Next()
			m.applyFilters()
		case "t":
			// Cycle time window filter.
			m.timeFilter = m.timeFilter.Next()
			m.applyFilters()
		case "up", "k":
			// Move cursor up
			if m.tableInit {
				m.table.MoveUp(1)
				m.cursor = m.table.Cursor()
				m.syncOffsetFromTable()
			}
		case "down", "j":
			// Move cursor down
			if m.tableInit {
				m.table.MoveDown(1)
				m.cursor = m.table.Cursor()
				// Sync offset for backwards compatibility (table scrolls internally)
				m.syncOffsetFromTable()
			}
		case "pgup", "ctrl+u":
			// Page up
			if m.tableInit {
				m.table.MoveUp(m.contentHeight())
				m.cursor = m.table.Cursor()
				m.syncOffsetFromTable()
			}
		case "pgdown", "ctrl+d":
			// Page down
			if m.tableInit {
				m.table.MoveDown(m.contentHeight())
				m.cursor = m.table.Cursor()
				m.syncOffsetFromTable()
			}
		case "home", "g":
			// Go to first entry
			if m.tableInit {
				m.table.GotoTop()
				m.cursor = m.table.Cursor()
				m.syncOffsetFromTable()
			}
		case "end", "G":
			// Go to last entry
			if m.tableInit {
				m.table.GotoBottom()
				m.cursor = m.table.Cursor()
				m.syncOffsetFromTable()
			}
		}
	}
	return m, nil
}

// SetEntries updates the history entries
func (m *HistoryPanel) SetEntries(entries []history.HistoryEntry, err error) {
	m.entries = entries
	m.err = err
	m.initTable() // Initialize table before applying filters
	m.applyFilters()
}

// Keybindings returns history panel specific shortcuts
func (m *HistoryPanel) Keybindings() []Keybinding {
	return []Keybinding{
		{
			Key:         key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "replay")),
			Description: "Replay selected command",
			Action:      "replay",
		},
		{
			Key:         key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "view")),
			Description: "Preview full prompt",
			Action:      "view",
		},
		{
			Key:         key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "copy")),
			Description: "Copy command to clipboard",
			Action:      "copy",
		},
		{
			Key:         key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "filter result")),
			Description: "Filter by success/failure",
			Action:      "filter_status",
		},
		{
			Key:         key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "filter agent")),
			Description: "Filter by agent type",
			Action:      "filter_agent",
		},
		{
			Key:         key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "time window")),
			Description: "Filter by time window",
			Action:      "filter_time",
		},
		{
			Key:         key.NewBinding(key.WithKeys("j"), key.WithHelp("j", "down")),
			Description: "Move cursor down",
			Action:      "down",
		},
		{
			Key:         key.NewBinding(key.WithKeys("k"), key.WithHelp("k", "up")),
			Description: "Move cursor up",
			Action:      "up",
		},
	}
}

// HandlesOwnHeight returns true because the history table manages its own
// visible window and now renders explicit scroll state.
func (m *HistoryPanel) HandlesOwnHeight() bool {
	return true
}

func (m *HistoryPanel) contentHeight() int {
	return m.Height() - 4 // borders + header
}

func (m *HistoryPanel) selectedEntry() (history.HistoryEntry, bool) {
	if len(m.visibleEntries) == 0 {
		return history.HistoryEntry{}, false
	}
	if m.cursor < 0 || m.cursor >= len(m.visibleEntries) {
		return history.HistoryEntry{}, false
	}
	return m.visibleEntries[m.cursor], true
}

func (m *HistoryPanel) selectedEntryID() string {
	entry, ok := m.selectedEntry()
	if !ok {
		return ""
	}
	return entry.ID
}

func historyEntryIndexByID(entries []history.HistoryEntry, id string) int {
	if id == "" {
		return -1
	}
	for i, entry := range entries {
		if entry.ID == id {
			return i
		}
	}
	return -1
}

func (m *HistoryPanel) moveCursor(delta int) {
	if len(m.visibleEntries) == 0 {
		m.cursor = 0
		m.offset = 0
		if m.tableInit {
			m.table.SetCursor(0)
		}
		return
	}

	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.visibleEntries) {
		m.cursor = len(m.visibleEntries) - 1
	}

	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+m.contentHeight() {
		m.offset = m.cursor - m.contentHeight() + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
	if m.tableInit {
		m.table.SetCursor(m.cursor)
	}
}

func (m *HistoryPanel) applyFilters() {
	selectedID := m.selectedEntryID()
	m.visibleEntries = m.visibleEntries[:0]

	// Preserve existing UX: show error state even if data is missing.
	if m.err != nil {
		m.visibleEntries = append(m.visibleEntries, m.entries...)
		if idx := historyEntryIndexByID(m.visibleEntries, selectedID); idx >= 0 {
			m.cursor = idx
			m.offset = idx
			if m.tableInit {
				m.rebuildTableRows()
				m.syncOffsetFromTable()
			}
			return
		}
		m.cursor = 0
		m.offset = 0
		if m.tableInit {
			m.rebuildTableRows()
			m.syncOffsetFromTable()
		}
		return
	}

	now := time.Now().UTC()
	cutoff, ok := m.timeFilter.Cutoff(now)

	for _, e := range m.entries {
		if ok && e.Timestamp.Before(cutoff) {
			continue
		}

		switch m.statusFilter {
		case historyStatusSuccessOnly:
			if !e.Success {
				continue
			}
		case historyStatusFailureOnly:
			if e.Success {
				continue
			}
		}

		if !m.matchesAgentFilter(e) {
			continue
		}

		m.visibleEntries = append(m.visibleEntries, e)
	}

	// Keep cursor within bounds.
	if len(m.visibleEntries) == 0 {
		m.cursor = 0
		m.offset = 0
		if m.tableInit {
			m.rebuildTableRows()
			m.syncOffsetFromTable()
		}
		return
	}

	if idx := historyEntryIndexByID(m.visibleEntries, selectedID); idx >= 0 {
		m.cursor = idx
	} else if m.cursor >= len(m.visibleEntries) {
		m.cursor = len(m.visibleEntries) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.offset > m.cursor {
		m.offset = m.cursor
	}
	if m.offset < 0 {
		m.offset = 0
	}

	if m.tableInit {
		m.rebuildTableRows()
		m.syncOffsetFromTable()
	}
}

func (m *HistoryPanel) historyTableHeight() int {
	height := m.Height() - 6
	if height < 3 {
		height = 3
	}
	return height
}

func (m *HistoryPanel) effectiveTableHeight() int {
	height := m.historyTableHeight()
	if len(m.visibleEntries) > max(1, height-1) && height > 3 {
		height--
	}
	return height
}

// historyTableColumns returns adaptive columns based on panel width.
func (m *HistoryPanel) historyTableColumns() []table.Column {
	w := m.Width()
	if w < 4 {
		w = 40
	}
	// Reserve space for borders/padding
	contentWidth := w - 6

	// Define column widths based on available space
	if contentWidth < 50 {
		// Narrow: Status + Prompt only
		return []table.Column{
			{Title: "St", Width: 2},
			{Title: "Prompt", Width: contentWidth - 4},
		}
	}
	if contentWidth < 70 {
		// Medium: Status + ID + Prompt
		return []table.Column{
			{Title: "St", Width: 2},
			{Title: "ID", Width: 4},
			{Title: "Prompt", Width: contentWidth - 10},
		}
	}
	// Wide: Status + ID + Target + Prompt
	targetWidth := 12
	idWidth := 4
	statusWidth := 2
	promptWidth := contentWidth - targetWidth - idWidth - statusWidth - 6
	if promptWidth < 20 {
		promptWidth = 20
	}
	return []table.Column{
		{Title: "St", Width: statusWidth},
		{Title: "ID", Width: idWidth},
		{Title: "Target", Width: targetWidth},
		{Title: "Prompt", Width: promptWidth},
	}
}

// rebuildTableRows converts visibleEntries to table rows.
func (m *HistoryPanel) rebuildTableRows() {
	cols := m.historyTableColumns()
	rows := make([]table.Row, len(m.visibleEntries))

	for i, e := range m.visibleEntries {
		// Status indicator
		status := "✓"
		if !e.Success {
			status = "✗"
		}

		// ID (truncated to 4 chars)
		id := e.ID
		if len(id) > 4 {
			id = id[:4]
		}

		// Target
		target := "all"
		if len(e.Targets) > 0 {
			target = m.formatTargets(e, 10)
		}

		// Prompt (single line)
		prompt := strings.ReplaceAll(e.Prompt, "\n", " ")
		maxPrompt := cols[len(cols)-1].Width
		if len(prompt) > maxPrompt {
			prompt = prompt[:maxPrompt-1] + "…"
		}

		// Build row based on column count
		switch len(cols) {
		case 2:
			rows[i] = table.Row{status, prompt}
		case 3:
			rows[i] = table.Row{status, id, prompt}
		default:
			rows[i] = table.Row{status, id, target, prompt}
		}
	}

	// Set columns BEFORE rows - rows reference column indices during render
	m.table.SetColumns(cols)
	m.table.SetRows(rows)

	// Sync cursor position with table
	if len(rows) > 0 {
		if m.cursor < len(rows) {
			m.table.SetCursor(m.cursor)
		} else {
			m.table.SetCursor(len(rows) - 1)
		}
	}
}

// initTable initializes the table model with styling.
func (m *HistoryPanel) initTable() {
	if m.tableInit {
		return
	}
	t := m.theme

	// Build table styles using Catppuccin theme
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
		Bold(true)
	s.Cell = s.Cell.
		Foreground(t.Subtext)

	m.table = table.New(
		table.WithColumns(m.historyTableColumns()),
		table.WithRows([]table.Row{}),
		table.WithFocused(m.IsFocused()),
		table.WithWidth(max(10, m.Width()-6)),
		table.WithHeight(m.historyTableHeight()),
		table.WithStyles(s),
	)

	m.tableInit = true
	m.rebuildTableRows()
}

// syncOffsetFromTable syncs the panel's offset field from the table's internal scroll position.
// This maintains backwards compatibility with tests that check panel.offset.
func (m *HistoryPanel) syncOffsetFromTable() {
	// The table's offset is calculated from cursor position and visible height
	// If cursor > visibleHeight, there's scrolling happening
	visibleHeight := m.contentHeight()
	if visibleHeight < 1 {
		visibleHeight = 1
	}
	if m.cursor >= visibleHeight {
		m.offset = m.cursor - visibleHeight + 1
	} else {
		m.offset = 0
	}
}

func (m *HistoryPanel) matchesAgentFilter(e history.HistoryEntry) bool {
	want := m.agentFilter.AgentType()
	if want == "" {
		return true
	}

	// If targets are empty, this entry was a broadcast; include it regardless.
	if len(e.Targets) == 0 {
		return true
	}

	for _, target := range e.Targets {
		// Fast path: pane index mapping.
		if meta, ok := m.panesByIndex[target]; ok {
			if meta.AgentType == want {
				return true
			}
			continue
		}

		// Heuristic fallback: parse "cc_1"/"cod_2" style labels.
		if at, ok := parseAgentTypePrefix(target); ok && at == want {
			return true
		}
	}

	return false
}

func parseAgentTypePrefix(target string) (tmux.AgentType, bool) {
	if target == "" {
		return "", false
	}
	parts := strings.SplitN(target, "_", 2)
	if len(parts) != 2 {
		return "", false
	}
	t := tmux.AgentType(parts[0])
	if !t.IsValid() || t == tmux.AgentUser {
		return "", false
	}
	return t, true
}

func formatHistoryPaneLabel(p tmux.Pane) string {
	if p.NTMIndex > 0 && p.Type != tmux.AgentUnknown && p.Type != tmux.AgentUser && p.Type != "" {
		return fmt.Sprintf("%s_%d", p.Type, p.NTMIndex)
	}
	if p.Title != "" {
		if parts := strings.SplitN(p.Title, "__", 2); len(parts) == 2 && parts[1] != "" {
			return parts[1]
		}
		return p.Title
	}
	return strconv.Itoa(p.Index)
}

func (m *HistoryPanel) formatTargets(entry history.HistoryEntry, maxLen int) string {
	if len(entry.Targets) == 0 {
		return "all"
	}

	out := make([]string, 0, len(entry.Targets))
	for _, target := range entry.Targets {
		if meta, ok := m.panesByIndex[target]; ok && meta.Label != "" {
			out = append(out, meta.Label)
			continue
		}
		out = append(out, target)
	}
	s := strings.Join(out, ",")
	if maxLen > 0 && len(s) > maxLen {
		return s[:maxLen-1] + "…"
	}
	return s
}

func (m *HistoryPanel) previewMaxScroll() int {
	if !m.showPreview {
		return 0
	}
	entry, ok := m.selectedEntry()
	if !ok {
		return 0
	}

	overlayWidth, overlayHeight := m.previewOverlaySize(m.Width(), m.Height())
	contentWidth := overlayWidth - 6
	if contentWidth < 20 {
		contentWidth = 20
	}

	innerHeight := overlayHeight - 4
	if innerHeight < 6 {
		innerHeight = 6
	}

	fixedLines := 7 // header + spacer + 3 meta lines + spacer + hint
	promptAvail := innerHeight - fixedLines
	if promptAvail < 1 {
		promptAvail = 1
	}

	wrapped := wordwrap.String(strings.TrimSpace(entry.Prompt), contentWidth)
	if wrapped == "" {
		wrapped = "(empty prompt)"
	}
	lines := strings.Split(wrapped, "\n")
	if len(lines) <= promptAvail {
		return 0
	}
	return len(lines) - promptAvail
}

func (m *HistoryPanel) previewOverlaySize(width, height int) (int, int) {
	overlayWidth := width - 6
	if overlayWidth > 90 {
		overlayWidth = 90
	}
	if overlayWidth < 40 {
		overlayWidth = 40
	}

	overlayHeight := height - 4
	if overlayHeight > 20 {
		overlayHeight = 20
	}
	if overlayHeight < 10 {
		overlayHeight = 10
	}

	return overlayWidth, overlayHeight
}

func (m *HistoryPanel) renderPreviewOverlay(width, height int) string {
	if !m.showPreview {
		return ""
	}
	entry, ok := m.selectedEntry()
	if !ok {
		return ""
	}

	t := m.theme

	overlayWidth, overlayHeight := m.previewOverlaySize(width, height)
	contentWidth := overlayWidth - 6 // 2 border + 4 padding
	if contentWidth < 20 {
		contentWidth = 20
	}
	innerHeight := overlayHeight - 4 // 2 border + 2 padding lines
	if innerHeight < 6 {
		innerHeight = 6
	}

	// Fixed lines: header + spacer + 3 meta lines + spacer + hint.
	fixedLines := 7
	promptAvail := innerHeight - fixedLines
	if promptAvail < 1 {
		promptAvail = 1
	}

	wrapped := wordwrap.String(strings.TrimSpace(entry.Prompt), contentWidth)
	if wrapped == "" {
		wrapped = "(empty prompt)"
	}
	promptLines := strings.Split(wrapped, "\n")
	maxScroll := 0
	if len(promptLines) > promptAvail {
		maxScroll = len(promptLines) - promptAvail
	}
	if m.previewScroll > maxScroll {
		m.previewScroll = maxScroll
	}
	start := m.previewScroll
	end := start + promptAvail
	if end > len(promptLines) {
		end = len(promptLines)
	}

	titleStyle := lipgloss.NewStyle().Foreground(t.Lavender).Bold(true)
	metaStyle := lipgloss.NewStyle().Foreground(t.Subtext)
	hintStyle := lipgloss.NewStyle().Foreground(t.Overlay).Italic(true)

	status := "✓ success"
	statusColor := t.Green
	if !entry.Success {
		status = "✗ failed"
		statusColor = t.Red
	}
	statusStyled := lipgloss.NewStyle().Foreground(statusColor).Bold(true).Render(status)

	var b strings.Builder
	b.WriteString(titleStyle.Render("Prompt Preview") + "\n\n")
	b.WriteString(metaStyle.Render("Time: ") + entry.Timestamp.Format("2006-01-02 15:04:05") + "\n")
	b.WriteString(metaStyle.Render("Targets: ") + m.formatTargets(entry, contentWidth) + "\n")
	b.WriteString(metaStyle.Render("Status: ") + statusStyled + "\n\n")

	for _, line := range promptLines[start:end] {
		b.WriteString(line + "\n")
	}

	b.WriteString("\n")
	hint := "Esc close • Enter replay • y copy • f/a/t filters"
	if maxScroll > 0 {
		hint = fmt.Sprintf("%d-%d/%d • %s", start+1, end, len(promptLines), hint)
	}
	b.WriteString(hintStyle.Render(hint))

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Primary).
		Background(t.Base).
		Padding(1, 2).
		Width(overlayWidth).
		Height(overlayHeight)

	return boxStyle.Render(b.String())
}

func (m *HistoryPanel) scrollState() components.ScrollState {
	total := len(m.visibleEntries)
	if total == 0 {
		return components.ScrollState{}
	}

	visible := m.effectiveTableHeight() - 1 // account for header row
	if visible < 1 {
		visible = 1
	}
	first := m.offset
	if first < 0 {
		first = 0
	}
	if first >= total {
		first = total - 1
	}
	last := first + visible - 1
	if last >= total {
		last = total - 1
	}
	return components.ScrollState{
		FirstVisible: first,
		LastVisible:  last,
		TotalItems:   total,
	}
}

// View renders the panel
func (m *HistoryPanel) View() string {
	t := m.theme
	w, h := m.Width(), m.Height()

	borderColor := t.Surface1
	bgColor := t.Base
	if m.IsFocused() {
		borderColor = t.Primary
		bgColor = t.Surface0 // Subtle tint for focused panel
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Background(bgColor).
		Width(w-2).
		Height(h-2).
		Padding(0, 1)

	var content strings.Builder

	// Build header with error badge if needed
	title := m.Config().Title
	title = fmt.Sprintf("%s  [%s] [%s] [%s]", title, m.statusFilter.Label(), m.agentFilter.Label(), m.timeFilter.Label())
	if m.err != nil {
		errorBadge := lipgloss.NewStyle().
			Background(t.Red).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render("⚠ Error")
		title = title + " " + errorBadge
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
	if m.err != nil {
		content.WriteString(components.ErrorState(m.err.Error(), "Press r to retry", w-4) + "\n")
	}

	if len(m.entries) == 0 {
		content.WriteString("\n" + components.RenderEmptyState(components.EmptyStateOptions{
			Icon:        components.IconEmpty,
			Title:       "No command history",
			Description: "Send prompts to build history",
			Action:      "Press 's' to send a prompt",
			Width:       w - 4,
			Centered:    true,
		}))
		mainContent := boxStyle.Render(FitToHeight(content.String(), h-4))
		return mainContent
	}

	if len(m.visibleEntries) == 0 {
		content.WriteString("\n" + components.RenderEmptyState(components.EmptyStateOptions{
			Icon:        components.IconEmpty,
			Title:       "No matching history",
			Description: "Adjust filters (f/a/t) to broaden results",
			Action:      "Press 'f' to cycle success/failure",
			Width:       w - 4,
			Centered:    true,
		}))
		mainContent := boxStyle.Render(FitToHeight(content.String(), h-4))
		return mainContent
	}

	// Initialize table if needed
	m.initTable()
	if m.IsFocused() {
		m.table.Focus()
	} else {
		m.table.Blur()
	}

	// Update table dimensions for rendering
	m.table.SetHeight(m.effectiveTableHeight())
	m.table.SetWidth(max(10, w-6))
	m.rebuildTableRows()

	// Render the table
	content.WriteString(m.table.View() + "\n")
	if footer := components.ScrollFooter(m.scrollState(), w-4); footer != "" {
		content.WriteString(footer + "\n")
	}

	mainContent := boxStyle.Render(FitToHeight(content.String(), h-4))

	// Overlay on top if shown.
	if m.showPreview {
		overlay := m.renderPreviewOverlay(w, h)
		if overlay != "" {
			overlayLines := strings.Split(overlay, "\n")
			mainLines := strings.Split(mainContent, "\n")

			overlayStartY := (len(mainLines) - len(overlayLines)) / 2
			if overlayStartY < 0 {
				overlayStartY = 0
			}

			for i, overlayLine := range overlayLines {
				targetLine := overlayStartY + i
				if targetLine >= len(mainLines) {
					break
				}
				overlayWidth := lipgloss.Width(overlayLine)
				mainWidth := lipgloss.Width(mainLines[targetLine])
				padLeft := (mainWidth - overlayWidth) / 2
				if padLeft < 0 {
					padLeft = 0
				}
				mainLines[targetLine] = strings.Repeat(" ", padLeft) + overlayLine
			}
			mainContent = strings.Join(mainLines, "\n")
		}
	}

	return mainContent
}
