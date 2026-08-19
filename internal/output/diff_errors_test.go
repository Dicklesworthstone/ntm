package output

import (
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
