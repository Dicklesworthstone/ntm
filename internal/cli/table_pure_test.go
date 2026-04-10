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
	if tbl.RowCount() != 0 {
		t.Errorf("RowCount = %d, want 0", tbl.RowCount())
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

func TestStyledTable_WithStyle(t *testing.T) {

	tbl := NewStyledTable("Col").WithStyle(TableStyleMinimal)
	if tbl.style != TableStyleMinimal {
		t.Errorf("style = %v, want TableStyleMinimal", tbl.style)
	}
}

func TestStyledTable_AddRow(t *testing.T) {

	tbl := NewStyledTable("Name", "Value")
	tbl.AddRow("foo", "bar")
	tbl.AddRow("baz", "longer value here")

	if tbl.RowCount() != 2 {
		t.Errorf("RowCount = %d, want 2", tbl.RowCount())
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

func TestStyledTable_String(t *testing.T) {

	tbl := NewStyledTable("H")
	tbl.AddRow("R")
	got := tbl.String()
	if got == "" {
		t.Error("String() should return non-empty")
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

func TestSuccessMessage(t *testing.T) {
	got := SuccessMessage("done")
	if got == "" {
		t.Error("expected non-empty")
	}
	if !strings.Contains(stripANSI(got), "done") {
		t.Error("should contain message text")
	}
}

func TestErrorMessage(t *testing.T) {
	got := ErrorMessage("failed")
	if !strings.Contains(stripANSI(got), "failed") {
		t.Error("should contain message text")
	}
}

func TestWarningMessage(t *testing.T) {
	got := WarningMessage("caution")
	if !strings.Contains(stripANSI(got), "caution") {
		t.Error("should contain message text")
	}
}

func TestInfoMessage(t *testing.T) {
	got := InfoMessage("note")
	if !strings.Contains(stripANSI(got), "note") {
		t.Error("should contain message text")
	}
}

func TestSectionHeader(t *testing.T) {
	got := SectionHeader("Overview")
	if got == "" {
		t.Error("expected non-empty")
	}
	if !strings.Contains(stripANSI(got), "Overview") {
		t.Error("should contain title")
	}
}

func TestSectionDivider(t *testing.T) {
	got := SectionDivider(40)
	if got == "" {
		t.Error("expected non-empty divider")
	}
}

func TestKeyValue(t *testing.T) {
	got := KeyValue("Status", "running", 10)
	stripped := stripANSI(got)
	if !strings.Contains(stripped, "Status") || !strings.Contains(stripped, "running") {
		t.Errorf("KeyValue output missing expected content: %q", stripped)
	}
}

func TestBadge(t *testing.T) {
	got := Badge("OK", "46")
	if got == "" {
		t.Error("expected non-empty badge")
	}
}

func TestSubtleText(t *testing.T) {
	got := SubtleText("muted")
	if !strings.Contains(stripANSI(got), "muted") {
		t.Error("should contain text")
	}
}

func TestBoldText(t *testing.T) {
	got := BoldText("important")
	if !strings.Contains(stripANSI(got), "important") {
		t.Error("should contain text")
	}
}

func TestAccentText(t *testing.T) {
	got := AccentText("highlight")
	if !strings.Contains(stripANSI(got), "highlight") {
		t.Error("should contain text")
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
