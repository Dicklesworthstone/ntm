package output

import (
	"fmt"
	"io"
	"strings"
)

// Text outputs plain text to the formatter's writer
func (f *Formatter) Text(format string, args ...interface{}) {
	fmt.Fprintf(f.writer, format, args...)
}

// Textln outputs plain text with a newline to the formatter's writer
func (f *Formatter) Textln(format string, args ...interface{}) {
	fmt.Fprintf(f.writer, format+"\n", args...)
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
		widths[i] = len(h)
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
		if i < len(t.widths) && len(c) > t.widths[i] {
			t.widths[i] = len(c)
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
	// Find first rune boundary at or after maxLen-3 bytes
	targetLen := maxLen - 3
	for i := range s {
		if i >= targetLen {
			return s[:i] + "..."
		}
	}
	return s
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
