package util

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// ExtractNewOutput isolates the text added to the pane after the before state.
// It handles cases where the buffer has scrolled (before is not a simple prefix).
func ExtractNewOutput(before, after string) string {
	if before == "" {
		return after
	}
	if after == "" {
		return ""
	}

	// Fast path: if 'after' starts with 'before', it's a simple append
	if len(after) >= len(before) && after[:len(before)] == before {
		return after[len(before):]
	}

	// Handle scrolled output (before is not a prefix of after)
	// We look for the longest suffix of 'before' that matches a prefix of 'after'.

	// Try to match using a chunk from the start of 'after'
	const chunkSize = 40
	var searchChunk string
	if len(after) >= chunkSize {
		searchChunk = after[:chunkSize]
	} else {
		// After is short, use all of it
		searchChunk = after
	}

	// We only need to search the end of 'before' that could possibly contain 'after'
	// The overlap cannot be longer than 'after'.
	scanStart := len(before) - len(after)
	if scanStart < 0 {
		scanStart = 0
	}

	searchRegion := before[scanStart:]

	// Find all occurrences of chunk in searchRegion
	// We want the *first* valid match in searchRegion because that gives the *longest* suffix of before.
	// (Earliest start index = longest suffix)

	remaining := searchRegion
	offset := 0

	for {
		idx := strings.Index(remaining, searchChunk)
		if idx == -1 {
			break
		}

		// Absolute index in 'before'
		absIdx := scanStart + offset + idx

		// Check if this starts a valid suffix match
		// Suffix of before: before[absIdx:]
		// Prefix of after: after[:len(suffix)]
		// We know before[absIdx:] starts with searchChunk.
		// We need to check if the rest matches.

		suffixLen := len(before) - absIdx
		if len(after) >= suffixLen && after[:suffixLen] == before[absIdx:] {
			return after[suffixLen:]
		}

		// Move past this match
		step := idx + 1
		remaining = remaining[step:]
		offset += step
	}

	// Fallback: check for overlaps smaller than searchChunk
	// This handles cases where the overlap is shorter than what we searched for
	maxOverlap := len(after) - 1
	if maxOverlap > len(before) {
		maxOverlap = len(before)
	}
	// Optimization: if fast path failed, we know overlap must be smaller than searchChunk
	if maxOverlap >= chunkSize {
		maxOverlap = chunkSize - 1
	}
	for k := maxOverlap; k > 0; k-- {
		if before[len(before)-k:] == after[:k] {
			return after[k:]
		}
	}

	// No overlap found - return everything
	return after
}

// Truncate shortens a string to maxLen with ellipsis.
// Uses three ASCII periods "..." to indicate truncation.
func Truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	// When n too small for content + ellipsis, just return first n chars
	if n <= 3 {
		// Find last rune boundary at or before n bytes
		lastValid := 0
		for i := range s {
			if i > n {
				break
			}
			lastValid = i
		}
		if lastValid == 0 && len(s) > 0 {
			return ""
		}
		return s[:lastValid]
	}
	// Find the last rune boundary that allows for "..." suffix within n bytes.
	targetLen := n - 3
	prevI := 0
	for i := range s {
		if i > targetLen {
			return s[:prevI] + "..."
		}
		prevI = i
	}
	// All rune starts are <= targetLen, but string is > n bytes.
	return s[:prevI] + "..."
}

// SanitizeFilename makes a string safe for use as a filename.
func SanitizeFilename(name string) string {
	// Replace unsafe characters
	replacer := strings.NewReplacer(
		"/", "-",
		"\\", "-",
		":", "-",
		"*", "-",
		"?", "-",
		"\"", "-",
		"<", "-",
		">", "-",
		"|", "-",
		"%", "_",
		" ", "_",
		".", "_", // Prevent dotfiles and directory traversal
	)
	safe := replacer.Replace(strings.TrimSpace(name))

	// Limit length while respecting UTF-8 boundaries
	if len(safe) > 50 {
		for i := 50; i >= 0; i-- {
			if utf8.RuneStart(safe[i]) {
				return safe[:i]
			}
		}
		return safe[:50]
	}
	return safe
}

// FormatBytes formats bytes in a human-readable way (e.g., "1.5 KB")
func FormatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// SafeSlice truncates a string to maxLen bytes, ensuring the cut is at a rune boundary.
// Unlike Truncate, it does not add an ellipsis.
func SafeSlice(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	// Find the last rune start that keeps the string within maxLen
	lastValid := 0
	for i := range s {
		if i > maxLen {
			break
		}
		lastValid = i
	}
	return s[:lastValid]
}

// SafeSliceFromEnd truncates a string to at most maxLen bytes from the end,
// ensuring the cut is at a rune boundary.
func SafeSliceFromEnd(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	start := len(s) - maxLen
	// Find the next valid rune start to avoid partial characters
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// GetLastNLines returns the last n lines of text efficiently.
// If the text has fewer than n lines, returns the entire text.
func GetLastNLines(text string, n int) string {
	if n <= 0 {
		return ""
	}

	// Search from the end for the n-th newline
	count := 0
	for i := len(text) - 1; i >= 0; i-- {
		if text[i] == '\n' {
			count++
			if count == n {
				return text[i+1:]
			}
		}
	}

	return text
}

// CountNonEmptyLines counts lines that have non-whitespace content.
func CountNonEmptyLines(content string) int {
	count := 0
	inLine := false
	hasContent := false

	for _, r := range content {
		if r == '\n' {
			if hasContent {
				count++
			}
			inLine = false
			hasContent = false
		} else {
			inLine = true
			if r != ' ' && r != '\t' && r != '\r' {
				hasContent = true
			}
		}
	}

	// Count last line if it has content and no trailing newline
	if inLine && hasContent {
		count++
	}

	return count
}
