package redaction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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
	// Built-in sensitive value bounds retain bytes that a more specific
	// overlapping provider did not cover, excluding public assignment context.
	valueStart int
	valueEnd   int
	// Only actual JSON fields qualify for field normalization and decoded-value
	// suppression; standalone provider spans are not JSON field evidence.
	jsonField bool
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
			switch {
			case p.protectWholeMatch:
				m.valueStart, m.valueEnd = m.start, m.end
			case p.reject != nil:
				if start, end, ok := jsonCredentialValueBounds(matchStr); ok {
					m.valueStart, m.valueEnd = loc[0]+start, loc[0]+end
					m.jsonField = true
				} else if start, end, ok := assignmentValueBounds(matchStr); ok {
					m.valueStart, m.valueEnd = loc[0]+start, loc[0]+end
				}
			case p.builtin && p.category == CategoryBearerToken:
				value := strings.TrimLeft(matchStr[len("bearer"):], " \t\r\n\f")
				m.valueStart, m.valueEnd = m.end-len(value), m.end
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
	unfinished bool
	// An unfinished escape is not decoded or counted toward a minimum length.
	// decodedEnd is the source boundary of the completely decoded prefix.
	decodedEnd int
}

func (t jsonStringToken) contentEnd() int {
	if t.unfinished {
		return t.end
	}
	return t.end - 1
}

type jsonRecord struct {
	start, end  int
	completeEnd int
	keyStarts   map[int]bool
	unfinished  *jsonStringToken
}

// scanJSONEncodedContent also recognizes credentials in serialized log messages
// and Unicode-escaped property names. Complete records and parser-validated
// container prefixes are independent, including a final unfinished field value.
// Shell-quoted text and definite syntax errors retain raw matching behavior.
func scanJSONEncodedContent(input string, disabled []Category, patterns []pattern, matches []match) []match {
	if !strings.Contains(input, `"`) {
		return matches
	}

	var tokens []jsonStringToken
	needsDecoding := false
	for _, record := range jsonRecordRanges(input) {
		for _, loc := range jsonStringPattern.FindAllStringIndex(input[record.start:record.completeEnd], -1) {
			token := jsonStringToken{start: record.start + loc[0], end: record.start + loc[1]}
			next := skipJSONSpace(input, token.end)
			token.key = record.keyStarts[token.start] || (next < record.end && input[next] == ':')
			raw := input[token.start:token.end]
			if strings.Contains(raw, "\\") {
				if err := json.Unmarshal([]byte(raw), &token.decoded); err != nil {
					return matches
				}
				token.escaped = true
				needsDecoding = true
			}
			tokens = append(tokens, token)
		}
		if record.unfinished != nil {
			tokens = append(tokens, *record.unfinished)
			needsDecoding = true
		}
	}
	if !needsDecoding {
		return matches
	}

	// An escaped quote inside a property name must not manufacture a shorter
	// credential key. Real field matches begin at the key token's opening quote.
	filtered := make([]match, 0, len(matches))
	for _, m := range matches {
		i := jsonTokenContaining(tokens, m.start)
		if i >= 0 && tokens[i].key && tokens[i].escaped && m.jsonField {
			continue
		}
		filtered = append(filtered, m)
	}
	matches = filtered

	protected := make(map[[2]int]bool)
	for i, key := range tokens {
		if !key.key || i+1 == len(tokens) {
			continue
		}
		value := tokens[i+1]
		if !key.escaped && !value.unfinished {
			continue
		}
		colon := skipJSONSpace(input, key.end)
		if value.key || colon >= len(input) || input[colon] != ':' || skipJSONSpace(input, colon+1) != value.start {
			continue
		}
		// Re-encode the key as data so a quote/colon in its decoded spelling
		// cannot inject a fictitious credential field into this representation.
		quotedKey := input[key.start:key.end]
		if key.escaped {
			encoded, _ := json.Marshal(key.decoded)
			quotedKey = string(encoded)
		}
		logical := quotedKey + input[key.end:value.end]
		normalized := logical
		if value.unfinished {
			// Close only the fully decoded prefix for the existing validators.
			// The quote and any unknown escape completion never become source
			// bytes or allowlist context, nor contribute to credential length.
			normalized = quotedKey + input[key.end:value.decodedEnd] + `"`
		}
		for _, m := range collectMatches(normalized, nil, patterns) {
			if m.start != 0 || m.end != len(normalized) || !m.jsonField {
				continue
			}
			protected[[2]int{value.start + 1, value.contentEnd()}] = true
			if isCategoryDisabled(m.category, disabled) {
				continue
			}
			m.logicalMatch = logical
			m.start, m.end = key.start, value.end
			m.match = input[m.start:m.end]
			m.valueStart, m.valueEnd = value.start+1, value.contentEnd()
			matches = append(matches, m)
		}
	}

	// Existing credential fields have their own decoded-value rules. Preserve
	// their provider priority and raw allowlist behavior even when the field's
	// category is disabled; only unknown string content needs another view.
	for _, m := range matches {
		if m.jsonField {
			protected[[2]int{m.valueStart, m.valueEnd}] = true
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
			if m.jsonField {
				protected[[2]int{m.valueStart, m.valueEnd}] = true
			}
		}
	}
	decodedTokens := make([]bool, len(tokens))
	var decodedMatches []match
	var privateKeyPatterns []pattern
	for _, p := range patterns {
		if p.category == CategoryPrivateKey {
			privateKeyPatterns = append(privateKeyPatterns, p)
		}
	}
	for i, token := range tokens {
		if token.key || (!token.escaped && !token.unfinished) {
			continue
		}
		isProtected := protected[[2]int{token.start + 1, token.contentEnd()}]
		tokenPatterns := patterns
		if isProtected {
			// A complete PEM body needs decoded newlines even inside a known
			// credential field, including one whose category is disabled.
			// Other providers retain their existing raw matches and allowlists.
			tokenPatterns = privateKeyPatterns
		}
		logical := collectMatches(token.decoded, disabled, tokenPatterns)
		mapped, ok := mapJSONMatches(input, token, logical)
		if !ok {
			continue
		}
		decodedTokens[i] = !isProtected
		decodedMatches = append(decodedMatches, mapped...)
	}

	filtered = matches[:0]
	for _, m := range matches {
		i := jsonTokenContaining(tokens, m.start)
		// Only replace raw matches fully represented by the decoded token.
		// A partial JSONL string may contain the beginning of a raw PEM block
		// or multiline assignment whose remaining bytes lie in later records.
		if i >= 0 && decodedTokens[i] && m.end <= tokens[i].end {
			continue
		}
		filtered = append(filtered, m)
	}
	return append(filtered, decodedMatches...)
}

// jsonRecordRanges returns source spans that can safely be decoded as JSON.
// Whole documents and open container prefixes take precedence over JSONL.
// Mixed logs recover object/array records independently. Scalar records retain
// their existing support only when every nonblank JSONL line is complete.
func jsonRecordRanges(input string) []jsonRecord {
	if json.Valid([]byte(input)) {
		return []jsonRecord{{start: 0, end: len(input), completeEnd: len(input)}}
	}
	if record, ok := jsonContainerPrefix(input, 0); ok {
		return []jsonRecord{record}
	}
	var records []jsonRecord
	allValid := true
	start := 0
	for line := range strings.SplitSeq(input, "\n") {
		end := start + len(line)
		// A CRLF record separator is outside an unfinished string value.
		content := strings.TrimSuffix(line, "\r")
		if strings.Trim(content, " \t") == "" {
			start = end + 1
			continue
		}
		if json.Valid([]byte(content)) {
			records = append(records, jsonRecord{start: start, end: start + len(content), completeEnd: start + len(content)})
		} else {
			allValid = false
			if record, ok := jsonContainerPrefix(content, start); ok {
				records = append(records, record)
			}
		}
		start = end + 1
	}
	if allValid {
		return records
	}
	filtered := records[:0]
	for _, record := range records {
		first := skipJSONSpace(input, record.start)
		if input[first] == '{' || input[first] == '[' {
			filtered = append(filtered, record)
		}
	}
	return filtered
}

// jsonContainerPrefix accepts only a single unfinished JSON object or array.
// The standard decoder validates separators, escapes, and nesting; only an EOF
// with an open container can grant additional detection. Its key/value state is
// retained because a final property name may not have received its colon yet.
func jsonContainerPrefix(input string, base int) (jsonRecord, bool) {
	first := skipJSONSpace(input, 0)
	if first == len(input) || (input[first] != '{' && input[first] != '[') {
		return jsonRecord{}, false
	}
	record := jsonRecord{start: base, end: base + len(input), completeEnd: base + len(input), keyStarts: make(map[int]bool)}
	type container struct {
		kind      json.Delim
		expectKey bool
	}
	var stack []container
	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.UseNumber() // Validation must not depend on float64 range.
	for {
		before := int(decoder.InputOffset())
		isKey := len(stack) > 0 && stack[len(stack)-1].kind == '{' && stack[len(stack)-1].expectKey
		token, err := decoder.Token()
		if err != nil {
			if len(stack) == 0 || (err != io.EOF && err != io.ErrUnexpectedEOF) {
				return jsonRecord{}, false
			}
			pos := skipJSONSpace(input, int(decoder.InputOffset()))
			if err == io.ErrUnexpectedEOF && pos < len(input) && input[pos] == '"' {
				// Never tokenize inside an unfinished key or array element.
				record.completeEnd = base + pos
				previous := pos - 1
				for previous >= 0 && strings.ContainsRune(" \t\r\n", rune(input[previous])) {
					previous--
				}
				if !isKey && stack[len(stack)-1].kind == '{' && previous >= 0 && input[previous] == ':' {
					decoded, prefixEnd, ok := decodeJSONPrefixString(input[pos:])
					if !ok {
						return jsonRecord{}, false
					}
					record.unfinished = &jsonStringToken{
						start: base + pos, end: record.end, unfinished: true,
						escaped: strings.Contains(input[pos:], "\\"), decoded: decoded,
						decodedEnd: base + pos + prefixEnd,
					}
				}
			}
			return record, true
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{', '[':
				stack = append(stack, container{kind: delimiter, expectKey: delimiter == '{'})
				if len(stack) > 10000 { // Match encoding/json's document nesting limit.
					return jsonRecord{}, false
				}
			case '}', ']':
				stack = stack[:len(stack)-1]
				if len(stack) == 0 {
					// A complete root was already rejected by json.Valid. Extra
					// top-level bytes cannot be recovered as its unfinished tail.
					return jsonRecord{}, false
				}
				stack[len(stack)-1].expectKey = stack[len(stack)-1].kind == '{'
			}
		} else if isKey {
			pos := skipJSONSpace(input, before)
			if pos < len(input) && input[pos] == ',' {
				pos = skipJSONSpace(input, pos+1)
			}
			record.keyStarts[base+pos] = true
			stack[len(stack)-1].expectKey = false
		} else if len(stack) > 0 {
			stack[len(stack)-1].expectKey = stack[len(stack)-1].kind == '{'
		}
	}
}

// decodeJSONPrefixString validates an unfinished string without inventing value
// characters. A pending backslash or Unicode escape is excluded from decoding;
// callers may still protect those source bytes after a real credential prefix.
func decodeJSONPrefixString(raw string) (decoded string, prefixEnd int, ok bool) {
	prefixEnd = len(raw)
	if err := json.Unmarshal([]byte(raw+`"`), &decoded); err == nil {
		return decoded, prefixEnd, true
	}
	escape := strings.LastIndexByte(raw, '\\')
	if escape < 0 {
		return "", 0, false
	}
	tail := raw[escape:]
	if tail != `\` {
		if !strings.HasPrefix(tail, `\u`) || len(tail) > 5 {
			return "", 0, false
		}
		for _, c := range tail[2:] {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return "", 0, false
			}
		}
	}
	if err := json.Unmarshal([]byte(raw[:escape]+`"`), &decoded); err != nil {
		return "", 0, false
	}
	return decoded, escape, true
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
	for _, m := range matches {
		offsets = append(offsets, m.start, m.end)
		if m.valueStart < m.valueEnd {
			offsets = append(offsets, m.valueStart, m.valueEnd)
		}
	}
	sort.Ints(offsets)
	mapped := make(map[int]int, len(offsets))
	rawPos, decodedPos := token.start+1, 0
	rawEnd := token.contentEnd()
	if token.unfinished {
		rawEnd = token.decodedEnd
	}
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
		extendTail := token.unfinished && token.decodedEnd < token.end &&
			m.end == len(token.decoded) && m.valueEnd == m.end && m.valueStart < m.valueEnd
		m.logicalMatch = m.match
		m.start, m.end = mapped[m.start], mapped[m.end]
		if m.valueStart < m.valueEnd {
			m.valueStart, m.valueEnd = mapped[m.valueStart], mapped[m.valueEnd]
		}
		if extendTail {
			m.end, m.valueEnd = token.end, token.end
			// An allowlist for the known decoded prefix cannot authorize the
			// unknown escape suffix. The actual complete raw span still can.
			m.logicalMatch = ""
		}
		m.match = input[m.start:m.end]
	}
	return matches, true
}

// assignmentValueBounds excludes the public key, separator, whitespace and
// optional quote delimiters from a validated built-in assignment match.
func assignmentValueBounds(m string) (start, end int, ok bool) {
	sep := strings.IndexAny(m, "=:")
	if sep < 0 {
		return 0, 0, false
	}
	value := strings.TrimLeft(m[sep+1:], " \t\r\n\f")
	if len(value) == 0 {
		return 0, 0, false
	}
	start, end = len(m)-len(value), len(m)
	if m[start] == '"' || m[start] == '\'' {
		start++
	}
	if start < end && (m[end-1] == '"' || m[end-1] == '\'') {
		end--
	}
	return start, end, start < end
}

// deduplicateMatches removes overlapping matches, preferring higher priority.
func deduplicateMatches(matches []match) []match {
	if len(matches) == 0 {
		return matches
	}

	// Sort by priority (descending) so higher priority matches get first pick.
	// For same priority, prefer earlier matches, then complete matches over
	// shorter fallbacks at the same position (for example a PEM header).
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].priority != matches[j].priority {
			return matches[i].priority > matches[j].priority
		}
		if matches[i].start != matches[j].start {
			return matches[i].start < matches[j].start
		}
		return matches[i].end > matches[j].end
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
		} else if m.valueStart < m.valueEnd {
			// A provider pattern can cover only part of another credential.
			// Keep its category and exact match while protecting every other
			// byte already recognized as part of the surrounding secret.
			result = append(result, uncoveredValueMatches(m, result)...)
		}
	}

	// Sort result by start position for consistent output order.
	sort.Slice(result, func(i, j int) bool {
		return result[i].start < result[j].start
	})

	return result
}

func uncoveredValueMatches(m match, kept []match) []match {
	fragments := []match{{
		category:        m.category,
		start:           m.valueStart,
		end:             m.valueEnd,
		priority:        m.priority,
		allowlistSource: m.match,
		logicalMatch:    m.logicalMatch,
	}}
	for _, other := range kept {
		if other.start == other.end {
			continue // Zero-width patterns do not protect any credential bytes.
		}
		if other.end <= m.valueStart || other.start >= m.valueEnd {
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
