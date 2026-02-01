package redaction

import (
	"regexp"
	"sync"
)

// patternDef defines a pattern with its category and priority.
type patternDef struct {
	category Category
	pattern  string
	priority int // higher = more specific, takes precedence
}

// pattern represents a compiled detection pattern.
type pattern struct {
	category Category
	regex    *regexp.Regexp
	priority int // higher priority patterns take precedence
}

// defaultPatterns contains all built-in detection patterns.
// Higher priority patterns are checked first and take precedence.
var defaultPatterns = []patternDef{
	// Provider-specific API keys (high priority)
	{CategoryOpenAIKey, `sk-[a-zA-Z0-9]{10,}T3BlbkFJ[a-zA-Z0-9]{10,}`, 100},
	{CategoryOpenAIKey, `sk-proj-[a-zA-Z0-9_-]{40,}`, 100},
	{CategoryOpenAIKey, `sk-[a-zA-Z0-9]{48}`, 95}, // legacy (checkpoint export regression)
	{CategoryAnthropicKey, `sk-ant-[a-zA-Z0-9_-]{40,}`, 100},
	{CategoryGitHubToken, `gh[pousr]_[a-zA-Z0-9]{30,}`, 100},
	{CategoryGitHubToken, `github_pat_[a-zA-Z0-9]{20,}_[a-zA-Z0-9]{40,}`, 100},
	{CategoryGoogleAPIKey, `AIza[a-zA-Z0-9_-]{35}`, 100},

	// Cloud provider credentials
	{CategoryAWSAccessKey, `AKIA[0-9A-Z]{16}`, 90},
	{CategoryAWSAccessKey, `ASIA[0-9A-Z]{16}`, 90},
	{CategoryAWSSecretKey, `(?i)(aws_secret|secret_access_key|secret_key)\s*[=:]\s*["']?[a-zA-Z0-9/+=]{40}["']?`, 90},

	// Authentication tokens
	{CategoryJWT, `eyJ[a-zA-Z0-9_-]*\.eyJ[a-zA-Z0-9_-]*\.[a-zA-Z0-9_-]+`, 85},
	{CategoryBearerToken, `(?i)bearer\s+[a-zA-Z0-9._-]{20,}`, 80},

	// Private keys
	{CategoryPrivateKey, `-----BEGIN\s+(RSA\s+|DSA\s+|EC\s+|OPENSSH\s+)?PRIVATE KEY-----`, 95},

	// Database URLs with credentials
	{CategoryDatabaseURL, `(?i)(postgres|mysql|mongodb|redis)://[^:]+:[^@]+@[^\s]+`, 85},

	// Generic patterns (lower priority)
	{CategoryPassword, `(?i)(password|passwd|pwd)\s*[=:]\s*["']?[^\s"']{8,}["']?`, 50},
	{CategoryGenericAPIKey, `(?i)([a-z_]*api[_]?key)\s*[=:]\s*["']?[a-zA-Z0-9_-]{16,}["']?`, 40},
	{CategoryGenericSecret, `(?i)(secret|private[_]?key|token)\s*[=:]\s*["']?[a-zA-Z0-9/+=_-]{16,}["']?`, 30},
}

// compiledPatterns holds the compiled regex patterns.
var compiledPatterns []pattern

// compileOnce ensures patterns are compiled exactly once.
var compileOnce sync.Once

// ResetPatterns resets compiled patterns (for testing only).
func ResetPatterns() {
	compileOnce = sync.Once{}
	compiledPatterns = nil
}

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
				category: def.category,
				regex:    re,
				priority: def.priority,
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
