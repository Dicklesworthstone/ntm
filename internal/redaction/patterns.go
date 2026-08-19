package redaction

import (
	"regexp"
	"sort"
	"strings"
	"sync"
)

const (
	openAIPrefixPattern     = "s" + "k\\-"
	openAIProjPrefixPattern = openAIPrefixPattern + "proj\\-"
	openAIPrefixLiteral     = "s" + "k-"
	openAIProjPrefixLiteral = openAIPrefixLiteral + "proj-"
	openAIMarker            = "T3Blbk" + "FJ"
)

type literalRequirement struct {
	anyOf      []string
	ignoreCase bool
}

// patternDef defines a pattern with its category and priority.
type patternDef struct {
	category         Category
	pattern          string
	priority         int // higher = more specific, takes precedence
	requiredLiterals []literalRequirement
}

// pattern represents a compiled detection pattern.
type pattern struct {
	category         Category
	regex            *regexp.Regexp
	priority         int // higher priority patterns take precedence
	requiredLiterals []literalRequirement
}

// defaultPatterns contains all built-in detection patterns.
// Higher priority patterns are checked first and take precedence.
var defaultPatterns = []patternDef{
	// Provider-specific API keys (high priority)
	// NOTE: We escape literal '-' (e.g. `sk\-`) to avoid GitHub push-protection false positives
	// on docs/code that include these regexes (even when not real secrets).
	{CategoryOpenAIKey, openAIPrefixPattern + `[a-zA-Z0-9]{10,}` + openAIMarker + `[a-zA-Z0-9]{10,}`, 100, []literalRequirement{
		{anyOf: []string{openAIPrefixLiteral}},
		{anyOf: []string{openAIMarker}},
	}},
	{CategoryOpenAIKey, openAIProjPrefixPattern + `[a-zA-Z0-9_-]{40,}`, 100, []literalRequirement{{anyOf: []string{openAIProjPrefixLiteral}}}},
	{CategoryOpenAIKey, openAIPrefixPattern + `[a-zA-Z0-9]{48}`, 95, []literalRequirement{{anyOf: []string{openAIPrefixLiteral}}}}, // legacy (checkpoint export regression)
	{CategoryAnthropicKey, `sk\-ant\-[a-zA-Z0-9_-]{40,}`, 100, []literalRequirement{{anyOf: []string{"sk-ant-"}}}},
	{CategoryGitHubToken, `gh[pousr]_[a-zA-Z0-9]{30,}`, 100, []literalRequirement{{anyOf: []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_"}}}},
	{CategoryGitHubToken, `github_pat_[a-zA-Z0-9]{20,}_[a-zA-Z0-9]{40,}`, 100, []literalRequirement{{anyOf: []string{"github_pat_"}}}},
	{CategoryGoogleAPIKey, `AIza[a-zA-Z0-9_-]{35}`, 100, []literalRequirement{{anyOf: []string{"AIza"}}}},

	// Cloud provider credentials
	{CategoryAWSAccessKey, `AKIA[0-9A-Z]{16}`, 90, []literalRequirement{{anyOf: []string{"AKIA"}}}},
	{CategoryAWSAccessKey, `ASIA[0-9A-Z]{16}`, 90, []literalRequirement{{anyOf: []string{"ASIA"}}}},
	{CategoryAWSSecretKey, `(?i)(aws_secret|secret_access_key|secret_key)\s*[=:]\s*["']?[a-zA-Z0-9/+=]{40}["']?`, 90, []literalRequirement{{anyOf: []string{"aws_secret", "secret_access_key", "secret_key"}, ignoreCase: true}}},

	// Authentication tokens
	{CategoryJWT, `eyJ[a-zA-Z0-9_-]*\.eyJ[a-zA-Z0-9_-]*\.[a-zA-Z0-9_-]+`, 85, []literalRequirement{{anyOf: []string{"eyJ"}}}},
	{CategoryBearerToken, `(?i)bearer\s+[a-zA-Z0-9._-]{20,}`, 80, []literalRequirement{{anyOf: []string{"bearer"}, ignoreCase: true}}},

	// Private keys
	{CategoryPrivateKey, `-----BEGIN\s+(RSA\s+|DSA\s+|EC\s+|OPENSSH\s+)?PRIVATE KEY-----`, 95, []literalRequirement{{anyOf: []string{"private key"}, ignoreCase: true}}},

	// Database URLs with credentials
	{CategoryDatabaseURL, `(?i)(postgres|mysql|mongodb|redis)://[^:]+:[^@]+@[^\s]+`, 85, []literalRequirement{{anyOf: []string{"postgres://", "mysql://", "mongodb://", "redis://"}, ignoreCase: true}}},

	// Generic patterns (lower priority)
	{CategoryPassword, `(?i)(password|passwd|pwd)\s*[=:]\s*["']?[^\s"']{8,}["']?`, 50, []literalRequirement{{anyOf: []string{"password", "passwd", "pwd"}, ignoreCase: true}}},
	{CategoryGenericAPIKey, `(?i)([a-z_]*api[_]?key)\s*[=:]\s*["']?[a-zA-Z0-9_-]{16,}["']?`, 40, []literalRequirement{{anyOf: []string{"api_key", "apikey"}, ignoreCase: true}}},
	{CategoryGenericSecret, `(?i)(secret|private[_]?key|token)\s*[=:]\s*["']?[a-zA-Z0-9/+=_-]{16,}["']?`, 30, []literalRequirement{{anyOf: []string{"secret", "private_key", "privatekey", "token"}, ignoreCase: true}}},
}

// compiledPatterns holds the compiled regex patterns.
var compiledPatterns []pattern

// compileOnce ensures patterns are compiled exactly once.
var compileOnce sync.Once

// compilePatterns compiles all default patterns.
func compilePatterns() {
	compileOnce.Do(func() {
		compiledPatterns = make([]pattern, 0, len(defaultPatterns))
		for _, def := range defaultPatterns {
			re, err := regexp.Compile(def.pattern)
			if err != nil {
				// Pattern compilation errors should be caught during development.
				continue
			}
			compiledPatterns = append(compiledPatterns, pattern{
				category:         def.category,
				regex:            re,
				priority:         def.priority,
				requiredLiterals: normalizeLiteralRequirements(def.requiredLiterals),
			})
		}
		// Sort by priority (descending) for deterministic matching.
		sortPatternsByPriority(compiledPatterns)
	})
}

// sortPatternsByPriority sorts patterns by priority descending.
// Uses insertion sort since the list is small.
func sortPatternsByPriority(patterns []pattern) {
	for i := 1; i < len(patterns); i++ {
		j := i
		for j > 0 && patterns[j].priority > patterns[j-1].priority {
			patterns[j], patterns[j-1] = patterns[j-1], patterns[j]
			j--
		}
	}
}

// extraPatternPriority is the deterministic priority assigned to user-
// configured ExtraPatterns. Set below every built-in default-patterns
// priority (the lowest is CategoryGenericSecret at 30) so a string that
// matches BOTH a built-in and an extra resolves to the built-in via the
// existing deduplicateMatches priority sort. bd-ztb6a.
const extraPatternPriority = 25

// compileExtraPatterns compiles user-configured ExtraPatterns from a
// Config into the runtime []pattern shape. Each extra pattern uses the
// caller's Category as its category, no literal-prefilter requirement
// (so the regex always runs), and a fixed sub-default priority. Compile
// errors on individual patterns are skipped silently — the matching
// rules from the user's other patterns and the built-ins still run.
//
// Returned slice is empty when extras is nil/empty.
func compileExtraPatterns(extras map[Category][]string) []pattern {
	if len(extras) == 0 {
		return nil
	}
	// Iterate in deterministic key order so the resulting pattern slice
	// is identical across calls with the same input — matters for the
	// deduplicate path, where ties at the same priority resolve by
	// earliest start (independent of order), but lock-step output keeps
	// debug logs stable.
	keys := make([]Category, 0, len(extras))
	for k := range extras {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	out := make([]pattern, 0, len(extras))
	for _, cat := range keys {
		for _, raw := range extras[cat] {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			re, err := regexp.Compile(raw)
			if err != nil {
				// Skip silently — a malformed user pattern must not
				// disable the rest of the user's patterns or the
				// built-ins. A future enhancement could surface this
				// as a Result.Warnings entry.
				continue
			}
			out = append(out, pattern{
				category: cat,
				regex:    re,
				priority: extraPatternPriority,
			})
		}
	}
	return out
}

// getPatterns returns the compiled patterns, initializing if needed.
func getPatterns() []pattern {
	compilePatterns()
	return compiledPatterns
}

// compileAllowlist compiles allowlist patterns.
func compileAllowlist(allowlist []string) []*regexp.Regexp {
	if len(allowlist) == 0 {
		return nil
	}
	compiled := make([]*regexp.Regexp, 0, len(allowlist))
	for _, pat := range allowlist {
		re, err := regexp.Compile(pat)
		if err != nil {
			continue
		}
		compiled = append(compiled, re)
	}
	return compiled
}

// isAllowlisted checks if a match should be ignored.
func isAllowlisted(match string, allowlist []*regexp.Regexp) bool {
	for _, re := range allowlist {
		if re.MatchString(match) {
			return true
		}
	}
	return false
}

// isCategoryDisabled checks if a category is in the disabled list.
func isCategoryDisabled(cat Category, disabled []Category) bool {
	for _, d := range disabled {
		if d == cat {
			return true
		}
	}
	return false
}

func normalizeLiteralRequirements(reqs []literalRequirement) []literalRequirement {
	if len(reqs) == 0 {
		return nil
	}
	normalized := make([]literalRequirement, 0, len(reqs))
	for _, req := range reqs {
		group := literalRequirement{
			anyOf:      make([]string, 0, len(req.anyOf)),
			ignoreCase: req.ignoreCase,
		}
		for _, needle := range req.anyOf {
			if needle == "" {
				continue
			}
			if req.ignoreCase {
				group.anyOf = append(group.anyOf, strings.ToLower(needle))
				continue
			}
			group.anyOf = append(group.anyOf, needle)
		}
		if len(group.anyOf) > 0 {
			normalized = append(normalized, group)
		}
	}
	return normalized
}

func (p pattern) passesLiteralPrefilter(input string, lowerInput *string, lowerReady *bool) bool {
	if len(p.requiredLiterals) == 0 {
		return true
	}
	for _, req := range p.requiredLiterals {
		haystack := input
		if req.ignoreCase {
			if !*lowerReady {
				*lowerInput = strings.ToLower(input)
				*lowerReady = true
			}
			haystack = *lowerInput
		}
		matched := false
		for _, needle := range req.anyOf {
			if strings.Contains(haystack, needle) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
