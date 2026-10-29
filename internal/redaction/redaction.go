package redaction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
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

	// bd-ztb6a: also compile any user-configured ExtraPatterns so they
	// participate in the scan alongside the built-in defaultPatterns.
	// Pre-fix this slice was silently dropped — Config.ExtraPatterns
	// flowed through every layer (TOML config, deep-copy Get/Set) but
	// never reached the matcher. They share the existing
	// deduplicate / allowlist / DisabledCategories machinery.
	extra := compileExtraPatterns(cfg.ExtraPatterns)

	// Scan for all matches.
	matches := scan(input, allowlist, cfg.DisabledCategories, extra)

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
	// JSON value bounds are set for validated built-in credential fields and
	// quoted assignments discovered inside decoded JSON message content.
	// They let lower-priority fields retain value bytes a provider did not cover.
	jsonValueStart int
	jsonValueEnd   int
	// Residual value matches retain the original field's allowlist context.
	allowlistSource string
	// JSON-encoded matches can also be allowlisted by their logical content.
	logicalMatch string
}

// scan finds all sensitive content in input.
func scan(input string, allowlist []*regexp.Regexp, disabled []Category, extra []pattern) []match {
	builtin := getPatterns()
	allMatches := collectMatches(input, disabled, builtin)
	allMatches = scanJSONEncodedContent(input, disabled, builtin, allMatches)
	// Custom patterns keep their raw-input semantics while sharing the same
	// category priority, deduplication, and allowlist handling as built-ins.
	allMatches = append(allMatches, collectMatches(input, disabled, extra)...)

	// Remove overlapping matches, keeping higher priority ones.
	// This must happen BEFORE allowlist checking so that higher-priority
	// matches can be allowlisted even when lower-priority patterns match
	// different substrings of the same region.
	deduplicated := deduplicateMatches(allMatches)

	// Filter out allowlisted matches.
	if len(allowlist) > 0 {
		var filtered []match
		for _, m := range deduplicated {
			allowlistSource := m.match
			if m.allowlistSource != "" {
				allowlistSource = m.allowlistSource
			}
			if isAllowlisted(allowlistSource, allowlist) ||
				(m.logicalMatch != "" && isAllowlisted(m.logicalMatch, allowlist)) {
				continue
			}
			filtered = append(filtered, m)
		}
		return filtered
	}

	return deduplicated
}

// collectMatches applies patterns to one representation of the input without
// recursively decoding strings, resolving overlaps, or applying allowlists.
func collectMatches(input string, disabled []Category, patterns []pattern) []match {
	var matches []match
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
			if p.reject != nil && p.reject(input, loc[0], loc[1]) {
				continue
			}
			matchStr := input[loc[0]:loc[1]]

			m := match{
				category: p.category,
				match:    matchStr,
				start:    loc[0],
				end:      loc[1],
				priority: p.priority,
			}
			if p.reject != nil {
				if start, end, ok := jsonCredentialValueBounds(matchStr); ok {
					m.jsonValueStart = loc[0] + start
					m.jsonValueEnd = loc[0] + end
				}
			}
			matches = append(matches, m)
		}
	}
	return matches
}

// jsonCredentialValueBounds identifies the raw string interior of an already
// validated built-in JSON credential match. Bare assignments and user-supplied
// patterns do not receive these bounds.
func jsonCredentialValueBounds(m string) (start, end int, ok bool) {
	if len(m) < 2 || m[0] != '"' {
		return 0, 0, false
	}
	sep := strings.IndexByte(m, ':')
	if sep < 0 {
		return 0, 0, false
	}
	value := strings.TrimLeft(m[sep+1:], " \t\r\n")
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, 0, false
	}
	return len(m) - len(value) + 1, len(m) - 1, true
}

var jsonStringPattern = regexp.MustCompile("\"(?:\\\\.|[^\"\\\\\\r\\n])*\"")

type jsonStringToken struct {
	start, end int
	key        bool
	escaped    bool
	decoded    string
}

// scanJSONEncodedContent also recognizes credentials in serialized log messages
// and Unicode-escaped property names. Restrict decoding to a valid JSON document
// or complete JSONL records: shell-quoted text and malformed documents retain
// their original raw matching behavior.
func scanJSONEncodedContent(input string, disabled []Category, patterns []pattern, matches []match) []match {
	if !strings.Contains(input, "\\") || !validJSONRecords(input) {
		return matches
	}

	var tokens []jsonStringToken
	for _, loc := range jsonStringPattern.FindAllStringIndex(input, -1) {
		token := jsonStringToken{start: loc[0], end: loc[1]}
		next := skipJSONSpace(input, token.end)
		token.key = next < len(input) && input[next] == ':'
		raw := input[token.start:token.end]
		if strings.Contains(raw, "\\") {
			if err := json.Unmarshal([]byte(raw), &token.decoded); err != nil {
				return matches
			}
			token.escaped = true
		}
		tokens = append(tokens, token)
	}

	// An escaped quote inside a property name must not manufacture a shorter
	// credential key. Real field matches begin at the key token's opening quote.
	filtered := make([]match, 0, len(matches))
	for _, m := range matches {
		i := jsonTokenContaining(tokens, m.start)
		if i >= 0 && tokens[i].key && tokens[i].escaped && m.jsonValueStart < m.jsonValueEnd {
			continue
		}
		filtered = append(filtered, m)
	}
	matches = filtered

	protected := make(map[[2]int]bool)
	for i, key := range tokens {
		if !key.key || !key.escaped || i+1 == len(tokens) {
			continue
		}
		value := tokens[i+1]
		colon := skipJSONSpace(input, key.end)
		if value.key || skipJSONSpace(input, colon+1) != value.start {
			continue
		}
		// Re-encode the key as data so a quote/colon in its decoded spelling
		// cannot inject a fictitious credential field into this representation.
		quotedKey, _ := json.Marshal(key.decoded)
		normalized := string(quotedKey) + input[key.end:value.end]
		for _, m := range collectMatches(normalized, nil, patterns) {
			if m.start != 0 || m.end != len(normalized) || m.jsonValueStart >= m.jsonValueEnd {
				continue
			}
			protected[[2]int{value.start + 1, value.end - 1}] = true
			if isCategoryDisabled(m.category, disabled) {
				continue
			}
			m.logicalMatch = m.match
			m.start, m.end = key.start, value.end
			m.match = input[m.start:m.end]
			m.jsonValueStart, m.jsonValueEnd = value.start+1, value.end-1
			matches = append(matches, m)
		}
	}

	// Existing credential fields have their own decoded-value rules. Preserve
	// their provider priority and raw allowlist behavior even when the field's
	// category is disabled; only unknown string content needs another view.
	for _, m := range matches {
		if m.jsonValueStart < m.jsonValueEnd {
			protected[[2]int{m.jsonValueStart, m.jsonValueEnd}] = true
		}
	}
	if len(disabled) > 0 {
		var disabledFields []pattern
		for _, p := range patterns {
			if p.reject != nil && isCategoryDisabled(p.category, disabled) {
				disabledFields = append(disabledFields, p)
			}
		}
		for _, m := range collectMatches(input, nil, disabledFields) {
			i := jsonTokenContaining(tokens, m.start)
			if i >= 0 && tokens[i].key && tokens[i].escaped {
				continue
			}
			if m.jsonValueStart < m.jsonValueEnd {
				protected[[2]int{m.jsonValueStart, m.jsonValueEnd}] = true
			}
		}
	}
	decodedTokens := make([]bool, len(tokens))
	var decodedMatches []match
	for i, token := range tokens {
		if token.key || !token.escaped || protected[[2]int{token.start + 1, token.end - 1}] {
			continue
		}
		logical := collectMatches(token.decoded, disabled, patterns)
		mapped, ok := mapJSONMatches(input, token, logical)
		if !ok {
			continue
		}
		decodedTokens[i] = true
		decodedMatches = append(decodedMatches, mapped...)
	}

	filtered = matches[:0]
	for _, m := range matches {
		i := jsonTokenContaining(tokens, m.start)
		if i >= 0 && decodedTokens[i] {
			continue
		}
		filtered = append(filtered, m)
	}
	return append(filtered, decodedMatches...)
}

func validJSONRecords(input string) bool {
	if json.Valid([]byte(input)) {
		return true
	}
	found := false
	for line := range strings.SplitSeq(input, "\n") {
		line = strings.Trim(line, " \t\r")
		if line == "" {
			continue
		}
		if !json.Valid([]byte(line)) {
			return false
		}
		found = true
	}
	return found
}

func skipJSONSpace(input string, pos int) int {
	for pos < len(input) && strings.ContainsRune(" \t\r\n", rune(input[pos])) {
		pos++
	}
	return pos
}

func jsonTokenContaining(tokens []jsonStringToken, pos int) int {
	i := sort.Search(len(tokens), func(i int) bool { return tokens[i].end > pos })
	if i < len(tokens) && tokens[i].start < pos {
		return i
	}
	return -1
}

// mapJSONMatches maps only the requested decoded UTF-8 byte boundaries. A long
// log message with one secret does not need an offset table for every byte.
// Escape sequences, including surrogate pairs, map to indivisible source spans.
func mapJSONMatches(input string, token jsonStringToken, matches []match) ([]match, bool) {
	if len(matches) == 0 {
		return nil, true
	}
	offsets := []int{0, len(token.decoded)}
	for i := range matches {
		m := &matches[i]
		if m.jsonValueStart >= m.jsonValueEnd && builtinMatchRejecters[m.category] != nil {
			if start, end, ok := quotedAssignmentValueBounds(m.match); ok {
				m.jsonValueStart, m.jsonValueEnd = m.start+start, m.start+end
			}
		}
		offsets = append(offsets, m.start, m.end)
		if m.jsonValueStart < m.jsonValueEnd {
			offsets = append(offsets, m.jsonValueStart, m.jsonValueEnd)
		}
	}
	sort.Ints(offsets)
	mapped := make(map[int]int, len(offsets))
	rawPos, decodedPos := token.start+1, 0
	rawEnd := token.end - 1
	for _, offset := range offsets {
		if offset < 0 || offset > len(token.decoded) {
			return nil, false
		}
		for decodedPos < offset {
			if rawPos >= rawEnd {
				return nil, false
			}
			_, rawWidth := utf8.DecodeRuneInString(input[rawPos:rawEnd])
			if input[rawPos] == '\\' {
				if rawPos+2 > rawEnd {
					return nil, false
				}
				rawWidth = 2
				if input[rawPos+1] == 'u' {
					if rawPos+6 > rawEnd {
						return nil, false
					}
					rawWidth = 6
					first, err := strconv.ParseUint(input[rawPos+2:rawPos+6], 16, 16)
					if err != nil {
						return nil, false
					}
					if rawPos+12 <= rawEnd && input[rawPos+6:rawPos+8] == "\\u" {
						second, err := strconv.ParseUint(input[rawPos+8:rawPos+12], 16, 16)
						if err != nil {
							return nil, false
						}
						if utf16.DecodeRune(rune(first), rune(second)) != utf8.RuneError {
							rawWidth = 12
						}
					}
				}
			}
			_, decodedWidth := utf8.DecodeRuneInString(token.decoded[decodedPos:])
			rawPos += rawWidth
			decodedPos += decodedWidth
		}
		if decodedPos != offset {
			return nil, false
		}
		mapped[offset] = rawPos
	}
	if rawPos != rawEnd {
		return nil, false
	}
	for i := range matches {
		m := &matches[i]
		m.logicalMatch = m.match
		m.start, m.end = mapped[m.start], mapped[m.end]
		m.match = input[m.start:m.end]
		if m.jsonValueStart < m.jsonValueEnd {
			m.jsonValueStart, m.jsonValueEnd = mapped[m.jsonValueStart], mapped[m.jsonValueEnd]
		}
	}
	return matches, true
}

// Decoded log messages can contain quoted assignments around provider tokens.
// Preserve their uncovered value bytes without changing raw shell scanning.
func quotedAssignmentValueBounds(m string) (start, end int, ok bool) {
	sep := strings.IndexAny(m, "=:")
	if sep < 0 {
		return 0, 0, false
	}
	value := strings.TrimLeft(m[sep+1:], " \t\r\n\f")
	if len(value) < 2 || (value[0] != '"' && value[0] != '\'') || value[len(value)-1] != value[0] {
		return 0, 0, false
	}
	return len(m) - len(value) + 1, len(m) - 1, true
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
		} else if m.jsonValueStart < m.jsonValueEnd {
			// A provider pattern can cover only the prefix of a JSON string,
			// particularly when an escape interrupts the provider's raw regex.
			// Preserve its category and exact match, but also redact every
			// uncovered byte of the validated credential value.
			result = append(result, uncoveredJSONValueMatches(m, result)...)
		}
	}

	// Sort result by start position for consistent output order.
	sort.Slice(result, func(i, j int) bool {
		return result[i].start < result[j].start
	})

	return result
}

func uncoveredJSONValueMatches(m match, kept []match) []match {
	fragments := []match{{
		category:        m.category,
		start:           m.jsonValueStart,
		end:             m.jsonValueEnd,
		priority:        m.priority,
		allowlistSource: m.match,
		logicalMatch:    m.logicalMatch,
	}}
	for _, other := range kept {
		if other.start == other.end {
			continue // Zero-width patterns do not protect any credential bytes.
		}
		if other.end <= m.jsonValueStart || other.start >= m.jsonValueEnd {
			continue
		}
		var remaining []match
		for _, fragment := range fragments {
			if fragment.end <= other.start || fragment.start >= other.end {
				remaining = append(remaining, fragment)
				continue
			}
			if fragment.start < other.start {
				left := fragment
				left.end = other.start
				remaining = append(remaining, left)
			}
			if other.end < fragment.end {
				right := fragment
				right.start = other.end
				remaining = append(remaining, right)
			}
		}
		fragments = remaining
		if len(fragments) == 0 {
			break
		}
	}
	for i := range fragments {
		fragments[i].match = m.match[fragments[i].start-m.start : fragments[i].end-m.start]
	}
	return fragments
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
