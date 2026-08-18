package output

import (
	"bytes"
	"strings"
	"testing"
)

// ============ diff.go tests ============

func TestComputeDiff_Identical(t *testing.T) {
	t.Parallel()

	content := "line 1\nline 2\nline 3\n"
	result := ComputeDiff("pane1", content, "pane2", content)

	if result.Pane1 != "pane1" {
		t.Errorf("Pane1 = %q, want %q", result.Pane1, "pane1")
	}
	if result.Pane2 != "pane2" {
		t.Errorf("Pane2 = %q, want %q", result.Pane2, "pane2")
	}
	if result.LineCount1 != 3 {
		t.Errorf("LineCount1 = %d, want 3", result.LineCount1)
	}
	if result.LineCount2 != 3 {
		t.Errorf("LineCount2 = %d, want 3", result.LineCount2)
	}
	if result.Similarity != 1.0 {
		t.Errorf("Similarity = %f, want 1.0", result.Similarity)
	}
}

func TestComputeDiff_CompletelyDifferent(t *testing.T) {
	t.Parallel()

	result := ComputeDiff("p1", "aaa", "p2", "bbb")
	if result.Similarity >= 1.0 {
		t.Errorf("completely different content should have similarity < 1.0, got %f", result.Similarity)
	}
}

func TestComputeDiff_PartialOverlap(t *testing.T) {
	t.Parallel()

	content1 := "line 1\nline 2\nline 3\n"
	content2 := "line 1\nline 2 modified\nline 3\n"

	result := ComputeDiff("p1", content1, "p2", content2)
	if result.Similarity <= 0 || result.Similarity >= 1.0 {
		t.Errorf("partial overlap should have 0 < similarity < 1, got %f", result.Similarity)
	}
	if result.UnifiedDiff == "" {
		t.Error("partial diff should produce a non-empty unified diff")
	}
}

func TestComputeDiff_EmptyStrings(t *testing.T) {
	t.Parallel()

	result := ComputeDiff("p1", "", "p2", "")
	if result.LineCount1 != 0 {
		t.Errorf("empty string LineCount1 = %d, want 0", result.LineCount1)
	}
	if result.LineCount2 != 0 {
		t.Errorf("empty string LineCount2 = %d, want 0", result.LineCount2)
	}
	// Both empty: similarity should be 0 (maxLen=0 branch)
	if result.Similarity != 0.0 {
		t.Errorf("both empty similarity = %f, want 0.0", result.Similarity)
	}
}

func TestComputeDiff_OneEmpty(t *testing.T) {
	t.Parallel()

	result := ComputeDiff("p1", "content", "p2", "")
	if result.Similarity >= 1.0 {
		t.Errorf("one empty should have low similarity, got %f", result.Similarity)
	}
}

func TestCountLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{"empty string", "", 0},
		{"just a newline", "\n", 0},
		{"single line no newline", "hello", 1},
		{"single line with newline", "hello\n", 1},
		{"two lines", "line1\nline2\n", 2},
		{"three lines no trailing", "a\nb\nc", 3},
		{"blank lines", "\n\n\n", 3}, // "\n\n\n" -> trim -> "\n\n" -> split -> ["","",""] = 3
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := countLines(tc.input)
			if result != tc.expected {
				t.Errorf("countLines(%q) = %d, want %d", tc.input, result, tc.expected)
			}
		})
	}
}

// ============ Error factory tests ============

// ============ StyledTable tests ============

func TestStyledTable_Basic(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	tbl := NewStyledTableWriter(&buf, "Name", "Status", "Count")
	tbl.AddRow("alice", "active", "5")
	tbl.AddRow("bob", "idle", "3")
	tbl.Render()

	output := buf.String()
	if !strings.Contains(output, "Name") {
		t.Error("output should contain header 'Name'")
	}
	if !strings.Contains(output, "alice") {
		t.Error("output should contain row data 'alice'")
	}
	if !strings.Contains(output, "bob") {
		t.Error("output should contain row data 'bob'")
	}
	if tbl.RowCount() != 2 {
		t.Errorf("RowCount() = %d, want 2", tbl.RowCount())
	}
}

func TestStyledTable_WithFooter(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	tbl := NewStyledTableWriter(&buf, "Col1")
	tbl.AddRow("data")
	tbl.WithFooter("Total: 1 item")
	tbl.Render()

	output := buf.String()
	if !strings.Contains(output, "Total: 1 item") {
		t.Error("output should contain footer text")
	}
}

func TestStyledTable_WithBorder(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	tbl := NewStyledTableWriter(&buf, "H1", "H2")
	tbl.WithBorder(true)
	if !tbl.ShowBorder {
		t.Error("ShowBorder should be true after WithBorder(true)")
	}
}

func TestStyledTable_EmptyHeaders(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	tbl := NewStyledTableWriter(&buf)
	tbl.Render()

	if buf.Len() != 0 {
		t.Error("empty headers should produce no output")
	}
}

func TestStyledTable_FluentAPI(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	tbl := NewStyledTableWriter(&buf, "A")

	// AddRow returns the table for chaining
	result := tbl.AddRow("1")
	if result != tbl {
		t.Error("AddRow should return the table for chaining")
	}

	result = tbl.WithFooter("footer")
	if result != tbl {
		t.Error("WithFooter should return the table for chaining")
	}

	result = tbl.WithBorder(true)
	if result != tbl {
		t.Error("WithBorder should return the table for chaining")
	}
}

func TestStyledTable_MissingColumns(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	tbl := NewStyledTableWriter(&buf, "A", "B", "C")
	tbl.AddRow("only-one") // Fewer columns than headers
	tbl.Render()

	output := buf.String()
	if !strings.Contains(output, "only-one") {
		t.Error("should render even with fewer columns")
	}
}

// ============ NewErrorWithDetails test ============

func TestNewErrorWithDetails(t *testing.T) {
	t.Parallel()

	resp := NewErrorWithDetails("something failed", "more info")
	if resp.Error != "something failed" {
		t.Errorf("Error = %q, want %q", resp.Error, "something failed")
	}
	if resp.Details != "more info" {
		t.Errorf("Details = %q, want %q", resp.Details, "more info")
	}
}

// ============ Print JSON helpers ============

func TestPrintJSONCompact(t *testing.T) {
	t.Parallel()

	// PrintJSONCompact writes to stdout; test it doesn't panic
	// (can't easily capture stdout in a test, but we can verify it doesn't error)
	err := PrintJSONCompact(map[string]string{"key": "value"})
	if err != nil {
		t.Errorf("PrintJSONCompact returned error: %v", err)
	}
}

func TestMarshalJSON(t *testing.T) {
	t.Parallel()

	data := map[string]int{"count": 42}

	// Pretty = false
	result, err := MarshalJSON(data, false)
	if err != nil {
		t.Fatalf("MarshalJSON(compact) error: %v", err)
	}
	if !strings.Contains(string(result), "count") {
		t.Error("compact JSON should contain key")
	}
	if strings.Contains(string(result), "\n") {
		t.Error("compact JSON should not contain newlines")
	}

	// Pretty = true
	result, err = MarshalJSON(data, true)
	if err != nil {
		t.Fatalf("MarshalJSON(pretty) error: %v", err)
	}
	if !strings.Contains(string(result), "\n") {
		t.Error("pretty JSON should contain newlines")
	}
}
