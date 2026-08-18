package cli

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// TableStyle defines the visual style of a table
type TableStyle int

const (
	// TableStyleRounded uses rounded box-drawing corners
	TableStyleRounded TableStyle = iota
	// TableStyleSimple uses simple line separators
	TableStyleSimple
	// TableStyleMinimal uses dots and subtle lines
	TableStyleMinimal
)

// StyledTable renders beautiful terminal tables with box-drawing
type StyledTable struct {
	headers []string
	rows    [][]string
	widths  []int
	style   TableStyle
	title   string
	footer  string
}

// NewStyledTable creates a new styled table with headers
func NewStyledTable(headers ...string) *StyledTable {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = runeWidth(h)
	}
	return &StyledTable{
		headers: headers,
		rows:    [][]string{},
		widths:  widths,
		style:   TableStyleRounded,
	}
}

// WithTitle adds a title to the table
func (t *StyledTable) WithTitle(title string) *StyledTable {
	t.title = title
	return t
}

// WithFooter adds a footer to the table
func (t *StyledTable) WithFooter(footer string) *StyledTable {
	t.footer = footer
	return t
}

// WithStyle sets the table style
func (t *StyledTable) WithStyle(style TableStyle) *StyledTable {
	t.style = style
	return t
}

// AddRow adds a row to the table
func (t *StyledTable) AddRow(cols ...string) {
	for i, c := range cols {
		if i < len(t.widths) {
			w := runeWidth(c)
			if w > t.widths[i] {
				t.widths[i] = w
			}
		}
	}
	t.rows = append(t.rows, cols)
}

// RowCount returns the number of rows
func (t *StyledTable) RowCount() int {
	return len(t.rows)
}

// Render returns the table as a styled string
func (t *StyledTable) Render() string {
	if len(t.headers) == 0 {
		return ""
	}

	th := theme.Current()
	var sb strings.Builder

	// Box-drawing characters based on style
	var topLeft, topRight, bottomLeft, bottomRight string
	var horizontal, vertical string
	var leftT, rightT, topT, bottomT, cross string

	switch t.style {
	case TableStyleRounded:
		topLeft, topRight = "╭", "╮"
		bottomLeft, bottomRight = "╰", "╯"
		horizontal, vertical = "─", "│"
		leftT, rightT = "├", "┤"
		topT, bottomT = "┬", "┴"
		cross = "┼"
	case TableStyleSimple:
		topLeft, topRight = "┌", "┐"
		bottomLeft, bottomRight = "└", "┘"
		horizontal, vertical = "─", "│"
		leftT, rightT = "├", "┤"
		topT, bottomT = "┬", "┴"
		cross = "┼"
	case TableStyleMinimal:
		topLeft, topRight = " ", " "
		bottomLeft, bottomRight = " ", " "
		horizontal, vertical = "─", " "
		leftT, rightT = " ", " "
		topT, bottomT = "─", "─"
		cross = "─"
	}

	// Colors
	borderColor := lipgloss.NewStyle().Foreground(th.Surface2)
	headerColor := lipgloss.NewStyle().Foreground(th.Primary).Bold(true)
	textColor := lipgloss.NewStyle().Foreground(th.Text)
	subtextColor := lipgloss.NewStyle().Foreground(th.Subtext)

	// Build horizontal lines
	buildHLine := func(left, mid, right, fill string) string {
		var line strings.Builder
		line.WriteString(borderColor.Render(left))
		for i, w := range t.widths {
			line.WriteString(borderColor.Render(strings.Repeat(fill, w+2)))
			if i < len(t.widths)-1 {
				line.WriteString(borderColor.Render(mid))
			}
		}
		line.WriteString(borderColor.Render(right))
		return line.String()
	}

	// Title
	if t.title != "" {
		titleStyle := lipgloss.NewStyle().
			Foreground(th.Primary).
			Bold(true)
		sb.WriteString(titleStyle.Render(t.title))
		sb.WriteString("\n")
	}

	// Top border
	sb.WriteString(buildHLine(topLeft, topT, topRight, horizontal))
	sb.WriteString("\n")

	// Header row
	sb.WriteString(borderColor.Render(vertical))
	for i, h := range t.headers {
		padded := padRight(h, t.widths[i])
		sb.WriteString(" ")
		sb.WriteString(headerColor.Render(padded))
		sb.WriteString(" ")
		sb.WriteString(borderColor.Render(vertical))
	}
	sb.WriteString("\n")

	// Header separator
	sb.WriteString(buildHLine(leftT, cross, rightT, horizontal))
	sb.WriteString("\n")

	// Data rows
	for _, row := range t.rows {
		sb.WriteString(borderColor.Render(vertical))
		for i := range t.headers {
			var cell string
			if i < len(row) {
				cell = row[i]
			}
			padded := padRight(cell, t.widths[i])
			sb.WriteString(" ")
			sb.WriteString(textColor.Render(padded))
			sb.WriteString(" ")
			sb.WriteString(borderColor.Render(vertical))
		}
		sb.WriteString("\n")
	}

	// Bottom border
	sb.WriteString(buildHLine(bottomLeft, bottomT, bottomRight, horizontal))
	sb.WriteString("\n")

	// Footer
	if t.footer != "" {
		sb.WriteString(subtextColor.Render(t.footer))
		sb.WriteString("\n")
	}

	return sb.String()
}

// String implements fmt.Stringer
func (t *StyledTable) String() string {
	return t.Render()
}

// runeWidth returns the visual display width of a string.
// Uses lipgloss.Width() which properly handles ANSI escape codes and
// double-width characters (CJK, emoji) that occupy 2 terminal columns.
func runeWidth(s string) int {
	return lipgloss.Width(s)
}

// padRight pads a string to the specified width
func padRight(s string, width int) string {
	currentWidth := runeWidth(s)
	if currentWidth >= width {
		return s
	}
	return s + strings.Repeat(" ", width-currentWidth)
}

// stripANSI removes ANSI escape codes from a string
func stripANSI(s string) string {
	var result strings.Builder
	inEscape := false
	for _, r := range s {
		if r == '\033' {
			inEscape = true
			continue
		}
		if inEscape {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		result.WriteRune(r)
	}
	return result.String()
}

// ErrorMessage renders an error message with icon
func ErrorMessage(msg string) string {
	th := theme.Current()
	style := lipgloss.NewStyle().Foreground(th.Error)
	return style.Render("✗ " + msg)
}

// InfoMessage renders an info message with icon
func InfoMessage(msg string) string {
	th := theme.Current()
	style := lipgloss.NewStyle().Foreground(th.Info)
	return style.Render("ℹ " + msg)
}

// Badge renders a small colored badge
func Badge(text string, color lipgloss.Color) string {
	th := theme.Current()
	style := lipgloss.NewStyle().
		Background(color).
		Foreground(th.Base).
		Padding(0, 1).
		Bold(true)
	return style.Render(text)
}
