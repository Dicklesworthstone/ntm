package output

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// Text outputs plain text to the formatter's writer
func (f *Formatter) Text(format string, args ...interface{}) {
	fmt.Fprintf(f.writer, format, args...)
}

// Line outputs a blank line
func (f *Formatter) Line() {
	fmt.Fprintln(f.writer)
}

// Print writes text to the formatter's writer
func (f *Formatter) Print(v ...interface{}) {
	fmt.Fprint(f.writer, v...)
}

// Println writes text with newline to the formatter's writer
func (f *Formatter) Println(v ...interface{}) {
	fmt.Fprintln(f.writer, v...)
}

// Printf writes formatted text to the formatter's writer
func (f *Formatter) Printf(format string, v ...interface{}) {
	fmt.Fprintf(f.writer, format, v...)
}

// Table outputs tabular data in text format
type Table struct {
	writer  io.Writer
	headers []string
	rows    [][]string
	widths  []int
}

// NewTable creates a new table with headers
func NewTable(w io.Writer, headers ...string) *Table {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = lipgloss.Width(h)
	}
	return &Table{
		writer:  w,
		headers: headers,
		rows:    [][]string{},
		widths:  widths,
	}
}

// AddRow adds a row to the table
func (t *Table) AddRow(cols ...string) {
	for i, c := range cols {
		w := lipgloss.Width(c)
		if i < len(t.widths) && w > t.widths[i] {
			t.widths[i] = w
		}
	}
	t.rows = append(t.rows, cols)
}

// Render outputs the table
func (t *Table) Render() {
	// Build format string
	formats := make([]string, len(t.widths))
	for i, w := range t.widths {
		formats[i] = fmt.Sprintf("%%-%ds", w)
	}
	rowFmt := "  " + strings.Join(formats, "  ") + "\n"

	// Print headers
	headerArgs := make([]interface{}, len(t.headers))
	for i, h := range t.headers {
		headerArgs[i] = h
	}
	fmt.Fprintf(t.writer, rowFmt, headerArgs...)

	// Print separator
	seps := make([]interface{}, len(t.widths))
	for i, w := range t.widths {
		seps[i] = strings.Repeat("-", w)
	}
	fmt.Fprintf(t.writer, rowFmt, seps...)

	// Print rows
	for _, row := range t.rows {
		rowArgs := make([]interface{}, len(t.headers))
		for i := range t.headers {
			if i < len(row) {
				rowArgs[i] = row[i]
			} else {
				rowArgs[i] = ""
			}
		}
		fmt.Fprintf(t.writer, rowFmt, rowArgs...)
	}
}

// Truncate truncates a string to max length, adding "..." if needed, respecting UTF-8 boundaries.
func Truncate(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if len(s) <= maxLen {
		return s
	}
	// When maxLen too small for content + ellipsis, just return first maxLen chars
	if maxLen <= 3 {
		// Find last rune boundary at or before maxLen bytes
		lastValid := 0
		for i := range s {
			if i > maxLen {
				break
			}
			lastValid = i
		}
		if lastValid == 0 && len(s) > 0 {
			// First rune is larger than maxLen, return empty
			return ""
		}
		return s[:lastValid]
	}
	// Find the last rune boundary that allows for "..." suffix within maxLen bytes.
	targetLen := maxLen - 3
	prevI := 0
	for i := range s {
		if i > targetLen {
			return s[:prevI] + "..."
		}
		prevI = i
	}
	// All rune starts are <= targetLen, but string is > maxLen bytes.
	return s[:prevI] + "..."
}

// Pluralize returns singular or plural form based on count
func Pluralize(count int, singular, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
}

// CountStr returns "N item(s)" string
func CountStr(count int, singular, plural string) string {
	return fmt.Sprintf("%d %s", count, Pluralize(count, singular, plural))
}

// StyledTable provides a styled table with lipgloss rendering.
// Uses theme colors for headers, borders, and status badges.
type StyledTable struct {
	writer   io.Writer
	headers  []string
	rows     [][]string
	widths   []int
	useColor bool
	footer   string

	// Style options
	ShowBorder bool
	Compact    bool
}

// NewStyledTable creates a new styled table for stdout.
func NewStyledTable(headers ...string) *StyledTable {
	return NewStyledTableWriter(os.Stdout, headers...)
}

// NewStyledTableWriter creates a styled table with a custom writer.
func NewStyledTableWriter(w io.Writer, headers ...string) *StyledTable {
	useColor := false
	if f, ok := w.(*os.File); ok {
		useColor = term.IsTerminal(int(f.Fd())) && os.Getenv("NO_COLOR") == ""
	}

	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = lipgloss.Width(h)
	}

	return &StyledTable{
		writer:     w,
		headers:    headers,
		rows:       [][]string{},
		widths:     widths,
		useColor:   useColor,
		ShowBorder: false,
		Compact:    false,
	}
}

// AddRow adds a row to the styled table.
func (t *StyledTable) AddRow(cols ...string) *StyledTable {
	for i, c := range cols {
		w := lipgloss.Width(c)
		if i < len(t.widths) && w > t.widths[i] {
			t.widths[i] = w
		}
	}
	t.rows = append(t.rows, cols)
	return t
}

// WithFooter adds a footer message below the table.
func (t *StyledTable) WithFooter(footer string) *StyledTable {
	t.footer = footer
	return t
}

// WithBorder enables border rendering.
func (t *StyledTable) WithBorder(show bool) *StyledTable {
	t.ShowBorder = show
	return t
}

// Render outputs the styled table.
func (t *StyledTable) Render() {
	if len(t.headers) == 0 {
		return
	}

	th := theme.Current()

	// Calculate padding between columns
	padding := "  "
	if t.Compact {
		padding = " "
	}

	// Build format strings for each column
	formats := make([]string, len(t.widths))
	for i, w := range t.widths {
		formats[i] = fmt.Sprintf("%%-%ds", w)
	}

	// Header styles
	headerStyle := lipgloss.NewStyle()
	if t.useColor {
		headerStyle = lipgloss.NewStyle().
			Foreground(th.Text).
			Bold(true)
	}

	sepStyle := lipgloss.NewStyle()
	if t.useColor {
		sepStyle = lipgloss.NewStyle().Foreground(th.Surface2)
	}

	// Render header
	var headerParts []string
	for i, h := range t.headers {
		cell := fmt.Sprintf(formats[i], h)
		if t.useColor {
			cell = headerStyle.Render(cell)
		}
		headerParts = append(headerParts, cell)
	}
	fmt.Fprintf(t.writer, "  %s\n", strings.Join(headerParts, padding))

	// Render separator
	var sepParts []string
	for _, w := range t.widths {
		sep := strings.Repeat("─", w)
		if t.useColor {
			sep = sepStyle.Render(sep)
		}
		sepParts = append(sepParts, sep)
	}
	fmt.Fprintf(t.writer, "  %s\n", strings.Join(sepParts, padding))

	// Render rows
	for _, row := range t.rows {
		var rowParts []string
		for i := range t.headers {
			var cell string
			if i < len(row) {
				cell = fmt.Sprintf(formats[i], row[i])
			} else {
				cell = fmt.Sprintf(formats[i], "")
			}
			rowParts = append(rowParts, cell)
		}
		fmt.Fprintf(t.writer, "  %s\n", strings.Join(rowParts, padding))
	}

	// Render footer if present
	if t.footer != "" {
		fmt.Fprintln(t.writer)
		if t.useColor {
			footerStyle := lipgloss.NewStyle().Foreground(th.Subtext)
			fmt.Fprintln(t.writer, footerStyle.Render(t.footer))
		} else {
			fmt.Fprintln(t.writer, t.footer)
		}
	}
}

// RowCount returns the number of data rows.
func (t *StyledTable) RowCount() int {
	return len(t.rows)
}

// StatusBadge returns a styled status indicator.
// Common statuses: "active", "idle", "busy", "error", "warning"
func StatusBadge(status string) string {
	th := theme.Current()

	var color lipgloss.Color
	switch strings.ToLower(status) {
	case "active", "running", "ok", "ready":
		color = th.Green
	case "idle", "waiting", "pending":
		color = th.Yellow
	case "busy", "working", "processing":
		color = th.Blue
	case "error", "failed", "stopped":
		color = th.Error
	case "warning", "warn":
		color = th.Warning
	default:
		color = th.Overlay
	}

	style := lipgloss.NewStyle().Foreground(color)
	return style.Render(status)
}

// AgentBadge returns a styled agent type badge with consistent colors.
func AgentBadge(agentType string) string {
	th := theme.Current()

	color := outputAgentBadgeColor(agentType, th)
	style := lipgloss.NewStyle().Foreground(color).Bold(true)
	return style.Render(outputAgentBadgeLabel(agentType))
}

func outputAgentBadgeColor(agentType string, th theme.Theme) lipgloss.Color {
	switch agent.AgentType(agentType).Canonical() {
	case agent.AgentTypeClaudeCode:
		return th.Claude
	case agent.AgentTypeCodex:
		return th.Codex
	case agent.AgentTypeGemini:
		return th.Gemini
	case agent.AgentTypeAntigravity:
		// Antigravity (agy) gets its own lavender accent, matching the semantic
		// theme palette, to distinguish it from the legacy Gemini CLI.
		return th.Lavender
	case agent.AgentTypeCursor:
		return th.Cursor
	case agent.AgentTypeWindsurf:
		return th.Windsurf
	case agent.AgentTypeAider:
		return th.Aider
	case agent.AgentTypeOllama:
		return th.Ollama
	case agent.AgentTypeUser:
		return th.User
	default:
		return th.Overlay
	}
}

func outputAgentBadgeLabel(agentType string) string {
	canonical := agent.AgentType(agentType).Canonical()
	if canonical.IsValid() || canonical == agent.AgentTypeUnknown {
		if label := strings.TrimSpace(canonical.ProfileName()); label != "" {
			return label
		}
	}

	label := strings.TrimSpace(agentType)
	if label == "" {
		return "unknown"
	}
	return label
}
