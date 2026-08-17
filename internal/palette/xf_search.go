package palette

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tools"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// XFSearchResultsMsg carries xf search results back to the model.
type XFSearchResultsMsg struct {
	Query   string
	Results []tools.XFSearchResult
	Err     error
}

// xfSearchCmd runs xf search as a Bubble Tea command.
func xfSearchCmd(query string, limit int) tea.Cmd {
	return func() tea.Msg {
		adapter := tools.NewXFAdapter()
		if _, installed := adapter.Detect(); !installed {
			return XFSearchResultsMsg{
				Query: query,
				Err:   fmt.Errorf("xf is not installed — run 'brew install xf' or see https://github.com/xf-sh/xf"),
			}
		}
		ctx := context.Background()
		results, err := adapter.Search(ctx, query, limit)
		return XFSearchResultsMsg{
			Query:   query,
			Results: results,
			Err:     err,
		}
	}
}

// initXFQuery creates the xf search text input.
func initXFQuery(t theme.Theme) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "Search X/Twitter archive..."
	ti.CharLimit = 200
	ti.Width = 40
	ti.PromptStyle = lipgloss.NewStyle().Foreground(t.Blue)
	ti.TextStyle = lipgloss.NewStyle().Foreground(t.Text)
	ti.PlaceholderStyle = lipgloss.NewStyle().Foreground(t.Overlay)
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(t.Pink)
	return ti
}

// enterXFSearch transitions the model to the xf search phase.
func (m *Model) enterXFSearch() {
	m.phase = PhaseXFSearch
	m.xfResults = nil
	m.xfCursor = 0
	m.xfSearching = false
	m.xfErr = nil
	m.filter.Blur()
	if m.xfQuery.Value() == "" {
		m.xfQuery = initXFQuery(m.theme)
	}
	m.xfQuery.Focus()
}

// updateXFSearchPhase handles input in the xf search query phase.
func (m *Model) updateXFSearchPhase(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, xfSearchKeys.Quit):
		m.quitting = true
		return *m, tea.Quit

	case key.Matches(msg, xfSearchKeys.Back):
		m.phase = PhaseCommand
		m.filter.Focus()
		m.xfQuery.Blur()
		return *m, nil

	case key.Matches(msg, xfSearchKeys.Select):
		query := strings.TrimSpace(m.xfQuery.Value())
		if query == "" {
			return *m, nil
		}
		m.xfSearching = true
		m.xfErr = nil
		return *m, xfSearchCmd(query, 20)

	default:
		var cmd tea.Cmd
		m.xfQuery, cmd = m.xfQuery.Update(msg)
		return *m, cmd
	}
}

// updateXFResultsPhase handles input when viewing xf search results.
func (m *Model) updateXFResultsPhase(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, xfResultsKeys.Quit):
		m.quitting = true
		return *m, tea.Quit

	case key.Matches(msg, xfResultsKeys.Back):
		m.phase = PhaseXFSearch
		m.xfQuery.Focus()
		return *m, nil

	case key.Matches(msg, xfResultsKeys.Up):
		if m.xfCursor > 0 {
			m.xfCursor--
		}

	case key.Matches(msg, xfResultsKeys.Down):
		if m.xfCursor < len(m.xfResults)-1 {
			m.xfCursor++
		}

	case key.Matches(msg, xfResultsKeys.Select):
		if len(m.xfResults) > 0 && m.xfCursor < len(m.xfResults) {
			result := m.xfResults[m.xfCursor]
			prompt := formatXFResultPrompt(result)
			m.selected = &config.PaletteCmd{
				Key:      "xf-result",
				Label:    "XF Search Result",
				Category: "xf",
				Prompt:   prompt,
			}
			m.phase = PhaseTarget
		}
	}
	return *m, nil
}

// formatXFResultPrompt formats an xf search result as a prompt to send to agents.
func formatXFResultPrompt(r tools.XFSearchResult) string {
	var b strings.Builder
	b.WriteString("Here is a relevant tweet from X/Twitter archive")
	if r.CreatedAt != "" {
		b.WriteString(fmt.Sprintf(" (%s)", r.CreatedAt))
	}
	b.WriteString(":\n\n")
	b.WriteString(r.Content)
	if r.ID != "" {
		b.WriteString(fmt.Sprintf("\n\n[Tweet ID: %s", r.ID))
		if r.Type != "" {
			b.WriteString(fmt.Sprintf(", Type: %s", r.Type))
		}
		if r.Score > 0 {
			b.WriteString(fmt.Sprintf(", Score: %.2f", r.Score))
		}
		b.WriteString("]")
	}
	return b.String()
}

// viewXFSearchPhase renders the xf search query input.
func (m Model) viewXFSearchPhase() string {
	t := m.theme
	ic := m.icons
	var b strings.Builder

	// Header
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(t.Blue).Padding(1, 2)
	b.WriteString(headerStyle.Render(fmt.Sprintf("%s XF Archive Search", ic.Search)))
	b.WriteString("\n\n")

	// Search input
	inputStyle := lipgloss.NewStyle().Padding(0, 2)
	b.WriteString(inputStyle.Render(m.xfQuery.View()))
	b.WriteString("\n\n")

	if m.xfSearching {
		spinnerStyle := lipgloss.NewStyle().Foreground(t.Mauve).Padding(0, 2)
		b.WriteString(spinnerStyle.Render("Searching..."))
		b.WriteString("\n")
	}

	if m.xfErr != nil {
		errStyle := lipgloss.NewStyle().Foreground(t.Error).Padding(0, 2)
		b.WriteString(errStyle.Render(fmt.Sprintf("%s %v", ic.Cross, m.xfErr)))
		b.WriteString("\n")
	}

	// Help (generated from the phase keymap; see keymap.go)
	helpStyle := lipgloss.NewStyle().Foreground(t.Subtext).Padding(1, 2)
	b.WriteString(helpStyle.Render(renderHelpEntriesPlain(xfSearchHelpEntries())))

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, b.String())
}

// viewXFResultsPhase renders xf search results.
func (m Model) viewXFResultsPhase() string {
	t := m.theme
	ic := m.icons
	var b strings.Builder

	// Header with query
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(t.Blue).Padding(1, 2)
	queryStyle := lipgloss.NewStyle().Foreground(t.Mauve).Italic(true)
	b.WriteString(headerStyle.Render(fmt.Sprintf("%s Results for %s", ic.Search, queryStyle.Render(m.xfQuery.Value()))))
	b.WriteString("\n\n")

	if len(m.xfResults) == 0 {
		emptyStyle := lipgloss.NewStyle().Foreground(t.Subtext).Padding(0, 2)
		b.WriteString(emptyStyle.Render("No results found."))
		b.WriteString("\n")
	} else {
		// Available height for results
		maxResults := m.height - 10
		if maxResults < 3 {
			maxResults = 3
		}

		// Calculate scroll window
		startIdx := 0
		if m.xfCursor >= maxResults {
			startIdx = m.xfCursor - maxResults + 1
		}
		endIdx := startIdx + maxResults
		if endIdx > len(m.xfResults) {
			endIdx = len(m.xfResults)
		}

		countStyle := lipgloss.NewStyle().Foreground(t.Subtext).Padding(0, 2)
		b.WriteString(countStyle.Render(fmt.Sprintf("%d results", len(m.xfResults))))
		b.WriteString("\n\n")

		for i := startIdx; i < endIdx; i++ {
			result := m.xfResults[i]
			isCursor := i == m.xfCursor

			// Truncate content for display
			content := strings.ReplaceAll(result.Content, "\n", " ")
			maxLen := m.width - 12
			if maxLen < 30 {
				maxLen = 30
			}
			if len(content) > maxLen {
				content = content[:maxLen-3] + "..."
			}

			var line string
			if isCursor {
				cursorStyle := lipgloss.NewStyle().Foreground(t.Pink).Bold(true)
				contentStyle := lipgloss.NewStyle().Foreground(t.Text)
				metaStyle := lipgloss.NewStyle().Foreground(t.Subtext)
				line = fmt.Sprintf("  %s %s %s",
					cursorStyle.Render(ic.Pointer),
					contentStyle.Render(content),
					metaStyle.Render(fmt.Sprintf("[%s]", result.CreatedAt)),
				)
			} else {
				contentStyle := lipgloss.NewStyle().Foreground(t.Overlay)
				metaStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
				line = fmt.Sprintf("    %s %s",
					contentStyle.Render(content),
					metaStyle.Render(fmt.Sprintf("[%s]", result.CreatedAt)),
				)
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")

	// Preview of selected result
	if len(m.xfResults) > 0 && m.xfCursor < len(m.xfResults) {
		result := m.xfResults[m.xfCursor]
		previewStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.Surface1).
			Padding(1, 2).
			Width(m.width - 8)
		previewContent := result.Content
		if result.Type != "" {
			previewContent += fmt.Sprintf("\n\nType: %s", result.Type)
		}
		if result.Score > 0 {
			previewContent += fmt.Sprintf(" | Score: %.2f", result.Score)
		}
		b.WriteString(lipgloss.NewStyle().Padding(0, 2).Render(previewStyle.Render(previewContent)))
		b.WriteString("\n")
	}

	// Help (generated from the phase keymap; see keymap.go)
	helpStyle := lipgloss.NewStyle().Foreground(t.Subtext).Padding(1, 2)
	b.WriteString(helpStyle.Render(renderHelpEntriesPlain(xfResultsHelpEntries())))

	return b.String()
}
