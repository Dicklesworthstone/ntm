package output

import (
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
)

// DiffResult holds the result of a comparison
type DiffResult struct {
	Pane1       string  `json:"pane1"`
	Pane2       string  `json:"pane2"`
	LineCount1  int     `json:"lines1"`
	LineCount2  int     `json:"lines2"`
	Similarity  float64 `json:"similarity"`
	UnifiedDiff string  `json:"diff,omitempty"`
}

// ComputeDiff compares two output strings
func ComputeDiff(pane1, content1, pane2, content2 string) *DiffResult {
	dmp := diffmatchpatch.New()

	// Compute diffs
	// Using character-based diff for precision, but could use line-based if performance is an issue
	diffs := dmp.DiffMain(content1, content2, true)

	// Compute similarity (0-1)
	dist := dmp.DiffLevenshtein(diffs)
	maxLen := len(content1)
	if len(content2) > maxLen {
		maxLen = len(content2)
	}
	similarity := 0.0
	if maxLen > 0 {
		similarity = 1.0 - (float64(dist) / float64(maxLen))
	}

	// Create unified diff (patches)
	patches := dmp.PatchMake(content1, diffs)
	unified := dmp.PatchToText(patches)

	return &DiffResult{
		Pane1:       pane1,
		Pane2:       pane2,
		LineCount1:  countLines(content1),
		LineCount2:  countLines(content2),
		Similarity:  similarity,
		UnifiedDiff: unified,
	}
}

// countLines counts the number of lines in a string.
// Empty strings return 0, trailing newlines don't count as extra lines.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	// Remove trailing newline to avoid counting an empty final line
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return 0 // String was just a newline
	}
	return len(strings.Split(s, "\n"))
}
