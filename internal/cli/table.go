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

// InfoMessage renders an info message with icon
func InfoMessage(msg string) string {
	th := theme.Current()
	style := lipgloss.NewStyle().Foreground(th.Info)
	return style.Render("ℹ " + msg)
}
