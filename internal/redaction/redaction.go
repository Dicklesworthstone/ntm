package redaction

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ScanAndRedact scans input for sensitive content and optionally redacts it.
// The behavior depends on the mode in cfg:
//   - ModeOff: returns input unchanged with no findings
//   - ModeWarn: scans and reports findings but doesn't modify output
//   - ModeRedact: replaces sensitive content with placeholders
//   - ModeBlock: scans and sets Blocked=true if findings exist
func ScanAndRedact(input string, cfg Config) Result {
	result := Result{
		Mode:           cfg.Mode,
		OriginalLength: len(input),
	}

	// Fast path: if mode is off, skip scanning.
	if cfg.Mode == ModeOff {
		result.Output = input
		return result
	}

	// Compile allowlist if provided.
	allowlist := compileAllowlist(cfg.Allowlist)

	// Scan for all matches.
	matches := scan(input, allowlist, cfg.DisabledCategories)

	// No findings: return input unchanged.
	if len(matches) == 0 {
		result.Output = input
		return result
	}

	// Convert matches to findings.
	result.Findings = make([]Finding, len(matches))
	for i, m := range matches {
		result.Findings[i] = Finding{
			Category: m.category,
			Match:    m.match,
			Redacted: generatePlaceholder(m.category, m.match),
			Start:    m.start,
			End:      m.end,
		}
	}

	// Handle based on mode.
	switch cfg.Mode {
	case ModeWarn:
		result.Output = input
	case ModeRedact:
		result.Output = applyRedactions(input, result.Findings)
	case ModeBlock:
		result.Output = input
		result.Blocked = true
	}

	return result
}

// match represents an internal match during scanning.
type match struct {
	category Category
	match    string
	start    int
	end      int
	priority int
}

// scan finds all sensitive content in input.
func scan(input string, allowlist []*regexp.Regexp, disabled []Category) []match {
	patterns := getPatterns()
	var allMatches []match
	var lowerInput string
	lowerReady := false

	for _, p := range patterns {
		if isCategoryDisabled(p.category, disabled) {
			continue
		}
		if !p.passesLiteralPrefilter(input, &lowerInput, &lowerReady) {
			continue
		}

		// Find all matches for this pattern.
		locs := p.regex.FindAllStringIndex(input, -1)
		for _, loc := range locs {
			matchStr := input[loc[0]:loc[1]]

			allMatches = append(allMatches, match{
				category: p.category,
				match:    matchStr,
				start:    loc[0],
				end:      loc[1],
				priority: p.priority,
			})
		}
	}

	// Remove overlapping matches, keeping higher priority ones.
	// This must happen BEFORE allowlist checking so that higher-priority
	// matches can be allowlisted even when lower-priority patterns match
	// different substrings of the same region.
	deduplicated := deduplicateMatches(allMatches)

	// Filter out allowlisted matches.
	if len(allowlist) > 0 {
		var filtered []match
		for _, m := range deduplicated {
			if !isAllowlisted(m.match, allowlist) {
				filtered = append(filtered, m)
			}
		}
		return filtered
	}

	return deduplicated
}

// deduplicateMatches removes overlapping matches, preferring higher priority.
func deduplicateMatches(matches []match) []match {
	if len(matches) == 0 {
		return matches
	}

	// Sort by priority (descending) so higher priority matches get first pick.
	// For same priority, prefer earlier matches.
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].priority != matches[j].priority {
			return matches[i].priority > matches[j].priority
		}
		return matches[i].start < matches[j].start
	})

	var result []match
	for _, m := range matches {
		overlaps := false
		for _, kept := range result {
			// Check for overlap with already kept higher-priority matches.
			// Overlap exists if max(m.start, kept.start) < min(m.end, kept.end)
			start := m.start
			if kept.start > start {
				start = kept.start
			}
			end := m.end
			if kept.end < end {
				end = kept.end
			}
			if start < end {
				overlaps = true
				break
			}
		}

		if !overlaps {
			result = append(result, m)
		}
	}

	// Sort result by start position for consistent output order.
	sort.Slice(result, func(i, j int) bool {
		return result[i].start < result[j].start
	})

	return result
}

// generatePlaceholder creates a redaction placeholder for a match.
// Format: [REDACTED:CATEGORY:hash8]
func generatePlaceholder(cat Category, content string) string {
	// Generate deterministic hash.
	data := string(cat) + ":" + content
	hash := sha256.Sum256([]byte(data))
	hashStr := hex.EncodeToString(hash[:4]) // First 4 bytes = 8 hex chars.
	return fmt.Sprintf("[REDACTED:%s:%s]", cat, hashStr)
}

// applyRedactions replaces matched content with placeholders.
func applyRedactions(input string, findings []Finding) string {
	if len(findings) == 0 {
		return input
	}

	// Sort findings by start position (ascending).
	sorted := make([]Finding, len(findings))
	copy(sorted, findings)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Start < sorted[j].Start
	})

	var sb strings.Builder
	sb.Grow(len(input)) // Estimate capacity

	lastPos := 0
	for _, f := range sorted {
		if f.Start < lastPos {
			continue // Overlap (should already be handled by deduplicateMatches)
		}
		if f.Start > len(input) || f.End > len(input) || f.Start > f.End {
			continue // Invalid finding
		}

		// Write text before the match
		sb.WriteString(input[lastPos:f.Start])
		// Write the redacted placeholder
		sb.WriteString(f.Redacted)
		lastPos = f.End
	}

	// Write remaining text
	if lastPos < len(input) {
		sb.WriteString(input[lastPos:])
	}

	return sb.String()
}

// Scan performs read-only detection without redaction.
// Equivalent to ScanAndRedact with ModeWarn.
func Scan(input string, cfg Config) []Finding {
	cfg.Mode = ModeWarn
	result := ScanAndRedact(input, cfg)
	return result.Findings
}

// Redact is a convenience function that performs redaction.
// Equivalent to ScanAndRedact with ModeRedact.
func Redact(input string, cfg Config) (string, []Finding) {
	cfg.Mode = ModeRedact
	result := ScanAndRedact(input, cfg)
	return result.Output, result.Findings
}

// ContainsSensitive checks if input contains any sensitive content.
func ContainsSensitive(input string, cfg Config) bool {
	cfg.Mode = ModeWarn
	result := ScanAndRedact(input, cfg)
	return len(result.Findings) > 0
}

// AddLineInfo enriches findings with line and column information.
func AddLineInfo(input string, findings []Finding) {
	if len(findings) == 0 {
		return
	}

	// Build line index.
	lineStarts := []int{0}
	for i, c := range input {
		if c == '\n' {
			lineStarts = append(lineStarts, i+1)
		}
	}

	// Find line/column for each finding.
	for i := range findings {
		pos := findings[i].Start
		// Binary search for the line.
		line := sort.Search(len(lineStarts), func(j int) bool {
			return lineStarts[j] > pos
		}) - 1
		if line < 0 {
			line = 0
		}
		findings[i].Line = line + 1 // 1-indexed
		findings[i].Column = pos - lineStarts[line] + 1
	}
}
