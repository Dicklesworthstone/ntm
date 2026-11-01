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
	return (&Scanner{cfg: cfg}).scanAndRedact(input, false)
}

// ScanJSONAndRedact inspects JSON application data, including decoded property
// names, embedded JSON strings, and configured patterns in decoded strings.
// Findings retain offsets in the original input; no document is reserialized.
// Non-JSON input retains ScanAndRedact's text semantics. ModeRedact still uses
// textual replacements, which need not preserve JSON structure: request callers
// must use block mode and output callers must validate their JSON replacements.
func ScanJSONAndRedact(input string, cfg Config) Result {
	return (&Scanner{cfg: cfg}).scanAndRedact(input, true)
}

// Scanner shares a finite inspection budget and prepared patterns across an
// operation, such as all entries in one archive. Use it serially; it is not safe
// for concurrent calls. An exhausted scanner remains exhausted. The one-shot
// APIs below use the same implementation with a fresh budget.
type Scanner struct {
	cfg         Config
	budget      *scanBudget
	allowlist   []*regexp.Regexp
	extra       []pattern
	initialized bool
}

// NewScanner prepares one bounded operation. It copies a configuration only
// after validating its size; regex compilation is deferred until input passes
// the size check. Invalid limits produce an exhausted scanner, never unlimited
// inspection. Off remains an explicit passthrough.
func NewScanner(cfg Config, limits ScanLimits) *Scanner {
	b := &scanBudget{limits: limits, candidates: limits.MaxCandidates, tokens: limits.MaxJSONTokens, work: limits.MaxWorkBytes}
	b.exceeded = limits.MaxInputBytes <= 0 || limits.MaxResultBytes <= 0 || limits.MaxCandidates <= 0 ||
		limits.MaxJSONTokens <= 0 || limits.MaxWorkBytes <= 0 || limits.MaxDepth <= 0 ||
		limits.MaxConfigEntries <= 0 || limits.MaxConfigBytes <= 0
	s := &Scanner{cfg: cfg, budget: b}
	// Validate configuration before copying maps, sorting category keys, or
	// compiling regexes. Empty/disabled entries consume the count budget too.
	if cfg.Mode != ModeOff && b.checkConfig(cfg) {
		s.cfg = cfg.DeepCopy()
	}
	return s
}

// ScanAndRedactWithLimits performs one bounded text inspection. Exhaustion
// returns LimitExceeded and Blocked with no partial findings or usable output.
func ScanAndRedactWithLimits(input string, cfg Config, limits ScanLimits) Result {
	return NewScanner(cfg, limits).ScanAndRedact(input)
}

// ScanJSONAndRedactWithLimits performs one bounded semantic JSON inspection.
func ScanJSONAndRedactWithLimits(input string, cfg Config, limits ScanLimits) Result {
	return NewScanner(cfg, limits).ScanJSONAndRedact(input)
}

// ScanAndRedact inspects text using this operation's remaining budget.
func (s *Scanner) ScanAndRedact(input string) Result {
	return s.scanAndRedact(input, false)
}

// ScanJSONAndRedact inspects JSON using this operation's remaining budget.
func (s *Scanner) ScanJSONAndRedact(input string) Result {
	return s.scanAndRedact(input, true)
}

type scanBudget struct {
	limits     ScanLimits
	candidates int
	tokens     int
	work       int64
	depth      int
	exceeded   bool
}

func (b *scanBudget) fail() bool {
	b.exceeded = true
	return false
}

func (b *scanBudget) checkConfig(cfg Config) bool {
	if b.exceeded {
		return false
	}
	entries, remaining := b.limits.MaxConfigEntries, b.limits.MaxConfigBytes
	consume := func(value string) bool {
		if entries == 0 || len(value) > remaining {
			return b.fail()
		}
		entries--
		remaining -= len(value)
		return true
	}
	for _, value := range cfg.Allowlist {
		if !consume(value) {
			return false
		}
	}
	for category, patterns := range cfg.ExtraPatterns {
		if !consume(string(category)) {
			return false
		}
		for _, value := range patterns {
			if !consume(value) {
				return false
			}
		}
	}
	for _, value := range cfg.DisabledCategories {
		if !consume(string(value)) {
			return false
		}
	}
	return true
}

// Every attempted read pays for its entire subject plus a fixed unit, even
// when a literal prefilter rejects it or the subject is empty. The budget is
// deterministic work accounting, not a wall-clock timeout.
func (b *scanBudget) readBytes(n int) bool {
	if b == nil {
		return true
	}
	if b.exceeded || n < 0 || b.work <= 0 || int64(n) > b.work-1 {
		return b.fail()
	}
	b.work -= int64(n) + 1
	return true
}

func (b *scanBudget) take(n int, tokens bool) bool {
	if b == nil {
		return true
	}
	remaining := &b.candidates
	if tokens {
		remaining = &b.tokens
	}
	if b.exceeded || n < 0 || n > *remaining {
		return b.fail()
	}
	*remaining -= n
	return true
}

// Request at most one extra location to prove overflow before allocating an
// unbounded regexp match list. Keep the unlimited legacy path unchanged.
func (b *scanBudget) findLimit(tokens bool) int {
	if b == nil {
		return -1
	}
	remaining := b.candidates
	if tokens {
		remaining = b.tokens
	}
	if remaining < int(^uint(0)>>1) {
		return remaining + 1
	}
	return remaining
}

func (b *scanBudget) enter() bool {
	if b == nil {
		return true
	}
	if b.exceeded || b.depth == b.limits.MaxDepth {
		return b.fail()
	}
	b.depth++
	return true
}

func (b *scanBudget) leave() {
	if b != nil {
		b.depth--
	}
}

func (b *scanBudget) checkResult(inputBytes int, matches []match, mode Mode) bool {
	if b == nil {
		return true
	}
	outputBytes, findingBytes := int64(inputBytes), int64(len(matches))*128
	for _, m := range matches {
		if !b.readBytes(len(m.match)) || !b.readBytes(len(m.category)) {
			return false
		}
		placeholderBytes := int64(len("[REDACTED::]")) + 8 + int64(len(m.category))
		findingBytes += placeholderBytes
		if mode == ModeRedact {
			outputBytes += placeholderBytes - int64(m.end-m.start)
		}
	}
	if b.exceeded || outputBytes > int64(b.limits.MaxResultBytes) || findingBytes > int64(b.limits.MaxResultBytes) {
		return b.fail()
	}
	return true
}

func (s *Scanner) scanAndRedact(input string, semanticJSON bool) Result {
	cfg, budget := s.cfg, s.budget
	result := Result{
		Mode:           cfg.Mode,
		OriginalLength: len(input),
	}

	// Fast path: if mode is off, skip scanning.
	if cfg.Mode == ModeOff {
		result.Output = input
		return result
	}
	limited := func() Result {
		return Result{Mode: cfg.Mode, OriginalLength: len(input), Blocked: true, LimitExceeded: true}
	}
	if budget != nil && (budget.exceeded || len(input) > budget.limits.MaxInputBytes) {
		budget.fail()
		return limited()
	}

	if !s.initialized {
		s.allowlist = compileAllowlist(cfg.Allowlist)
		s.extra = compileExtraPatterns(cfg.ExtraPatterns)
		s.initialized = true
	}
	if semanticJSON {
		if !budget.readBytes(len(input)) {
			return limited()
		}
		semanticJSON = json.Valid([]byte(input))
	}

	// Scan for all matches.
	matches := scan(input, s.allowlist, cfg.DisabledCategories, s.extra, semanticJSON, budget)
	if !budget.checkResult(len(input), matches, cfg.Mode) {
		return limited()
	}

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
	// Keep intermediate representations when JSON transports nest strings.
	// They are allowlist evidence only, never returned as diagnostics.
	allowlistContexts []string
}

// scan finds all sensitive content in input.
func scan(input string, allowlist []*regexp.Regexp, disabled []Category, extra []pattern, semanticJSON bool, budget *scanBudget) []match {
	builtin := getPatterns()
	allMatches := collectMatches(input, disabled, builtin, budget)
	var decodedExtra []pattern
	if semanticJSON {
		decodedExtra = extra
	}
	allMatches = scanJSONEncodedContent(input, disabled, builtin, allMatches, semanticJSON, decodedExtra, budget)
	// Always retain custom patterns' raw-input matches. JSON application data
	// additionally checks their decoded string values through the same matcher.
	allMatches = append(allMatches, collectMatches(input, disabled, extra, budget)...)
	if budget != nil && budget.exceeded {
		return nil
	}

	// Remove overlapping matches, keeping higher priority ones.
	// This must happen BEFORE allowlist checking so that higher-priority
	// matches can be allowlisted even when lower-priority patterns match
	// different substrings of the same region.
	deduplicated := deduplicateMatches(allMatches)

	// Filter out allowlisted matches.
	if len(allowlist) > 0 {
		allowedSource := func(value string) bool {
			if budget == nil {
				return isAllowlisted(value, allowlist)
			}
			for _, re := range allowlist {
				// Residual findings may share a long original field. Each
				// attempted allowlist read must still consume the same budget.
				if !budget.readBytes(len(value)) {
					return false
				}
				if re.MatchString(value) {
					return true
				}
			}
			return false
		}
		var filtered []match
		for _, m := range deduplicated {
			allowlistSource := m.match
			if m.allowlistSource != "" {
				allowlistSource = m.allowlistSource
			}
			allowed := allowedSource(allowlistSource) ||
				(m.logicalMatch != "" && allowedSource(m.logicalMatch))
			for _, source := range m.allowlistContexts {
				if allowed {
					break
				}
				allowed = allowedSource(source)
			}
			if budget != nil && budget.exceeded {
				return nil
			}
			if allowed {
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
func collectMatches(input string, disabled []Category, patterns []pattern, budget *scanBudget) []match {
	var matches []match
	var lowerInput string
	lowerReady := false

	for _, p := range patterns {
		if isCategoryDisabled(p.category, disabled) {
			continue
		}
		if !budget.readBytes(len(input)) {
			return nil
		}
		if !p.passesLiteralPrefilter(input, &lowerInput, &lowerReady) {
			continue
		}

		// Find all matches for this pattern.
		locs := p.regex.FindAllStringIndex(input, budget.findLimit(false))
		if !budget.take(len(locs), false) {
			return nil
		}
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
func scanJSONEncodedContent(input string, disabled []Category, patterns []pattern, matches []match, semanticJSON bool, extra []pattern, budget *scanBudget) []match {
	if !budget.readBytes(len(input)) {
		return nil
	}
	if !strings.Contains(input, `"`) {
		return matches
	}
	if !budget.enter() {
		return nil
	}
	defer budget.leave()

	var tokens []jsonStringToken
	needsDecoding := len(extra) > 0
	for _, record := range jsonRecordRanges(input, budget) {
		if !budget.readBytes(record.completeEnd - record.start) {
			return nil
		}
		locations := jsonStringPattern.FindAllStringIndex(input[record.start:record.completeEnd], budget.findLimit(true))
		if !budget.take(len(locations), true) {
			return nil
		}
		for _, loc := range locations {
			token := jsonStringToken{start: record.start + loc[0], end: record.start + loc[1]}
			next := skipJSONSpace(input, token.end)
			token.key = record.keyStarts[token.start] || (next < record.end && input[next] == ':')
			raw := input[token.start:token.end]
			if strings.Contains(raw, "\\") {
				if !budget.readBytes(len(raw)) {
					return nil
				}
				if err := json.Unmarshal([]byte(raw), &token.decoded); err != nil {
					return matches
				}
				token.escaped = true
				needsDecoding = true
			} else if len(extra) > 0 {
				// Anchored custom patterns apply to a handler-visible string
				// even when its original JSON spelling needed no escapes.
				if !budget.readBytes(len(raw)) {
					return nil
				}
				if err := json.Unmarshal([]byte(raw), &token.decoded); err != nil {
					return matches
				}
			}
			tokens = append(tokens, token)
		}
		if record.unfinished != nil {
			if !budget.take(1, true) {
				return nil
			}
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
			if !budget.readBytes(len(key.decoded)) {
				return nil
			}
			encoded, _ := json.Marshal(key.decoded)
			quotedKey = string(encoded)
		}
		if !budget.readBytes(len(quotedKey) + value.end - key.end) {
			return nil
		}
		logical := quotedKey + input[key.end:value.end]
		normalized := logical
		if value.unfinished {
			// Close only the fully decoded prefix for the existing validators.
			// The quote and any unknown escape completion never become source
			// bytes or allowlist context, nor contribute to credential length.
			normalized = quotedKey + input[key.end:value.decodedEnd] + `"`
		}
		for _, m := range collectMatches(normalized, nil, patterns, budget) {
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
		for _, m := range collectMatches(input, nil, disabledFields, budget) {
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
		if token.key && !semanticJSON {
			continue
		}
		if !token.escaped && !token.unfinished {
			if len(extra) > 0 {
				logical := collectMatches(token.decoded, disabled, extra, budget)
				if mapped, ok := mapJSONMatches(input, token, logical, budget); ok {
					decodedMatches = append(decodedMatches, mapped...)
				}
			}
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
		logical := collectMatches(token.decoded, disabled, tokenPatterns, budget)
		if semanticJSON && !isProtected {
			// Each recursive step consumes a real JSON string encoding and
			// maps back through that token. Ordinary object/array nesting is
			// handled by the parser, not recursion over remarshal operations.
			logical = scanJSONEncodedContent(token.decoded, disabled, tokenPatterns, logical, true, extra, budget)
		}
		logical = append(logical, collectMatches(token.decoded, disabled, extra, budget)...)
		mapped, ok := mapJSONMatches(input, token, logical, budget)
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
func jsonRecordRanges(input string, budget *scanBudget) []jsonRecord {
	if !budget.readBytes(len(input)) {
		return nil
	}
	if json.Valid([]byte(input)) {
		if !budget.take(1, true) {
			return nil
		}
		return []jsonRecord{{start: 0, end: len(input), completeEnd: len(input)}}
	}
	if record, ok := jsonContainerPrefix(input, 0, budget); ok {
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
		if !budget.readBytes(len(content)) || !budget.take(1, true) {
			return nil
		}
		if json.Valid([]byte(content)) {
			records = append(records, jsonRecord{start: start, end: start + len(content), completeEnd: start + len(content)})
		} else {
			allValid = false
			if record, ok := jsonContainerPrefix(content, start, budget); ok {
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
func jsonContainerPrefix(input string, base int, budget *scanBudget) (jsonRecord, bool) {
	if !budget.readBytes(len(input)) {
		return jsonRecord{}, false
	}
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
		if !budget.take(1, true) {
			return jsonRecord{}, false
		}
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
					if !budget.readBytes(len(input) - pos) {
						return jsonRecord{}, false
					}
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
func mapJSONMatches(input string, token jsonStringToken, matches []match, budget *scanBudget) ([]match, bool) {
	if len(matches) == 0 {
		return nil, true
	}
	if !budget.readBytes(token.end - token.start) {
		return nil, false
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
		m.allowlistContexts = append(m.allowlistContexts, m.match)
		if m.logicalMatch == "" {
			m.logicalMatch = m.match
		}
		m.start, m.end = mapped[m.start], mapped[m.end]
		if m.valueStart < m.valueEnd {
			m.valueStart, m.valueEnd = mapped[m.valueStart], mapped[m.valueEnd]
		}
		if extendTail {
			m.end, m.valueEnd = token.end, token.end
			// An allowlist for the known decoded prefix cannot authorize the
			// unknown escape suffix. The actual complete raw span still can.
			m.logicalMatch = ""
			m.allowlistContexts = nil
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

	coverage := newMatchCoverage(matches)
	var result []match
	for _, m := range matches {
		if !coverage.overlaps(m.start, m.end) {
			result = append(result, m)
			coverage.cover(m.start, m.end)
		} else if m.valueStart < m.valueEnd {
			// A provider pattern can cover only part of another credential.
			// Keep its category and exact match while protecting every other
			// byte already recognized as part of the surrounding secret.
			result = append(result, coverage.uncoveredValueMatches(m)...)
		}
	}

	// Sort result by start position for consistent output order.
	sort.Slice(result, func(i, j int) bool {
		return result[i].start < result[j].start
	})

	return result
}

const (
	matchCoverageEmpty uint8 = iota
	matchCoveragePartial
	matchCoverageFull
)

// matchCoverage indexes the union of accepted, nonempty spans. Coordinate
// compression keeps its size proportional to the candidates, not input bytes.
// Coverage only grows: fully covered subtrees can be skipped for both overlap
// queries and residual values, including repeated surrounding credentials.
type matchCoverage struct {
	bounds []int
	nodes  []uint8
}

func newMatchCoverage(matches []match) matchCoverage {
	nonempty := 0
	for _, m := range matches {
		if m.start < m.end {
			nonempty++
		}
	}
	if nonempty == 0 {
		return matchCoverage{}
	}
	bounds := make([]int, 0, nonempty*4)
	for _, m := range matches {
		if m.start >= m.end {
			continue
		}
		bounds = append(bounds, m.start, m.end)
		if m.valueStart < m.valueEnd {
			bounds = append(bounds, m.valueStart, m.valueEnd)
		}
	}
	sort.Ints(bounds)
	unique := 1
	for _, offset := range bounds[1:] {
		if offset != bounds[unique-1] {
			bounds[unique] = offset
			unique++
		}
	}
	bounds = bounds[:unique]
	return matchCoverage{bounds: bounds, nodes: make([]uint8, 4*(len(bounds)-1))}
}

func (c *matchCoverage) overlaps(start, end int) bool {
	if start >= end {
		return false // Zero-width patterns never occupy credential bytes.
	}
	return c.any(1, 0, len(c.bounds)-1,
		sort.SearchInts(c.bounds, start), sort.SearchInts(c.bounds, end))
}

func (c *matchCoverage) any(node, lo, hi, start, end int) bool {
	if end <= lo || hi <= start || c.nodes[node] == matchCoverageEmpty {
		return false
	}
	if c.nodes[node] == matchCoverageFull || (start <= lo && hi <= end) {
		return true
	}
	mid := lo + (hi-lo)/2
	return c.any(node*2, lo, mid, start, end) || c.any(node*2+1, mid, hi, start, end)
}

func (c *matchCoverage) cover(start, end int) {
	if start >= end {
		return
	}
	c.mark(1, 0, len(c.bounds)-1,
		sort.SearchInts(c.bounds, start), sort.SearchInts(c.bounds, end))
}

func (c *matchCoverage) mark(node, lo, hi, start, end int) {
	if end <= lo || hi <= start || c.nodes[node] == matchCoverageFull {
		return
	}
	if start <= lo && hi <= end {
		c.nodes[node] = matchCoverageFull
		return
	}
	mid := lo + (hi-lo)/2
	c.mark(node*2, lo, mid, start, end)
	c.mark(node*2+1, mid, hi, start, end)
	c.nodes[node] = matchCoveragePartial
	if c.nodes[node*2] == matchCoverageFull && c.nodes[node*2+1] == matchCoverageFull {
		c.nodes[node] = matchCoverageFull
	}
}

func (c *matchCoverage) uncoveredValueMatches(m match) []match {
	start := sort.SearchInts(c.bounds, m.valueStart)
	end := sort.SearchInts(c.bounds, m.valueEnd)
	fragments := c.appendUncovered(nil, 1, 0, len(c.bounds)-1, start, end)
	for i, fragment := range fragments {
		fragments[i] = match{
			category:          m.category,
			match:             m.match[fragment.start-m.start : fragment.end-m.start],
			start:             fragment.start,
			end:               fragment.end,
			priority:          m.priority,
			allowlistSource:   m.match,
			logicalMatch:      m.logicalMatch,
			allowlistContexts: m.allowlistContexts,
		}
	}
	// The previous coverage plus these accepted gaps covers the entire value.
	c.mark(1, 0, len(c.bounds)-1, start, end)
	return fragments
}

func (c *matchCoverage) appendUncovered(fragments []match, node, lo, hi, start, end int) []match {
	if end <= lo || hi <= start || c.nodes[node] == matchCoverageFull {
		return fragments
	}
	if c.nodes[node] == matchCoverageEmpty {
		if lo < start {
			lo = start
		}
		if hi > end {
			hi = end
		}
		// Adjacent empty subtrees are one residual, not separate findings.
		if n := len(fragments); n > 0 && fragments[n-1].end == c.bounds[lo] {
			fragments[n-1].end = c.bounds[hi]
			return fragments
		}
		return append(fragments, match{start: c.bounds[lo], end: c.bounds[hi]})
	}
	mid := lo + (hi-lo)/2
	fragments = c.appendUncovered(fragments, node*2, lo, mid, start, end)
	return c.appendUncovered(fragments, node*2+1, mid, hi, start, end)
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
