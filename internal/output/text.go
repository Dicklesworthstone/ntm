package output

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

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
