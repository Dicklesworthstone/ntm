package cli

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// StyledTable builder — 0% → 100%
// ---------------------------------------------------------------------------

func TestNewStyledTable(t *testing.T) {

	tbl := NewStyledTable("Name", "Status", "Age")
	if tbl == nil {
		t.Fatal("expected non-nil StyledTable")
	}
	if len(tbl.rows) != 0 {
		t.Errorf("len(rows) = %d, want 0", len(tbl.rows))
	}
}

func TestStyledTable_WithTitle(t *testing.T) {

	tbl := NewStyledTable("Col").WithTitle("My Table")
	if tbl.title != "My Table" {
		t.Errorf("title = %q, want %q", tbl.title, "My Table")
	}
}

func TestStyledTable_WithFooter(t *testing.T) {

	tbl := NewStyledTable("Col").WithFooter("Page 1 of 3")
	if tbl.footer != "Page 1 of 3" {
		t.Errorf("footer = %q", tbl.footer)
	}
}

func TestStyledTable_AddRow(t *testing.T) {

	tbl := NewStyledTable("Name", "Value")
	tbl.AddRow("foo", "bar")
	tbl.AddRow("baz", "longer value here")

	if len(tbl.rows) != 2 {
		t.Errorf("len(rows) = %d, want 2", len(tbl.rows))
	}
}

func TestStyledTable_Render_Empty(t *testing.T) {

	tbl := &StyledTable{}
	got := tbl.Render()
	if got != "" {
		t.Errorf("Render() with no headers = %q, want empty", got)
	}
}

func TestStyledTable_Render_WithData(t *testing.T) {

	tbl := NewStyledTable("Name", "Age")
	tbl.AddRow("Alice", "30")
	tbl.AddRow("Bob", "25")

	got := tbl.Render()
	if got == "" {
		t.Error("expected non-empty render output")
	}
	// Should contain the data
	stripped := stripANSI(got)
	if !strings.Contains(stripped, "Alice") {
		t.Error("render should contain 'Alice'")
	}
	if !strings.Contains(stripped, "Bob") {
		t.Error("render should contain 'Bob'")
	}
}

// ---------------------------------------------------------------------------
// padRight — 0% → 100%
// ---------------------------------------------------------------------------

func TestPadRight(t *testing.T) {

	tests := []struct {
		name  string
		s     string
		width int
		want  string
	}{
		{"exact", "abc", 3, "abc"},
		{"shorter", "ab", 5, "ab   "},
		{"longer", "abcdef", 3, "abcdef"},
		{"empty", "", 4, "    "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := padRight(tt.s, tt.width)
			if got != tt.want {
				t.Errorf("padRight(%q, %d) = %q, want %q", tt.s, tt.width, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Message formatters — 0% → 100%
// ---------------------------------------------------------------------------

func TestInfoMessage(t *testing.T) {
	got := InfoMessage("note")
	if !strings.Contains(stripANSI(got), "note") {
		t.Error("should contain message text")
	}
}

// ---------------------------------------------------------------------------
// runeWidth — 0% → 100%
// ---------------------------------------------------------------------------

func TestRuneWidth(t *testing.T) {

	tests := []struct {
		name string
		s    string
	}{
		{"empty", ""},
		{"ascii", "hello"},
		{"with_ansi", "\033[31mred\033[0m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runeWidth(tt.s)
			if tt.s == "" && got != 0 {
				t.Errorf("runeWidth(%q) = %d, want 0", tt.s, got)
			}
			if tt.name == "ascii" && got != 5 {
				t.Errorf("runeWidth(%q) = %d, want 5", tt.s, got)
			}
		})
	}
}
