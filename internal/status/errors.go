package status

import (
	"regexp"
	"strings"
)

// ErrorPattern defines a pattern for error detection in agent output
type ErrorPattern struct {
	// Type is the category of error this pattern detects
	Type ErrorType
	// Regex is a compiled regular expression for matching (optional)
	Regex *regexp.Regexp
	// Literal is a simple string to search for (optional, faster than regex)
	Literal string
	// Description explains what this pattern matches (for debugging)
	Description string
}

// errorPatterns contains all known error patterns, ordered by priority.
// More specific patterns should come first within each category.
var errorPatterns = []ErrorPattern{
	// Rate limiting (highest priority - transient errors)
	{Type: ErrorRateLimit, Regex: regexp.MustCompile(`(?i)rate[\s._-]?limit`), Description: "Rate limit message"},
	{Type: ErrorRateLimit, Regex: regexp.MustCompile(`(?i)(http|status|error|code).{0,10}\b429\b`), Description: "HTTP 429 status"},
	{Type: ErrorRateLimit, Regex: regexp.MustCompile(`(?i)\b429\b.{0,10}(too many|rate|limit)`), Description: "429 with message"},
	{Type: ErrorRateLimit, Regex: regexp.MustCompile(`(?i)too many requests`), Description: "Too many requests"},
	{Type: ErrorRateLimit, Regex: regexp.MustCompile(`(?i)quota exceeded`), Description: "Quota exceeded"},
	{Type: ErrorRateLimit, Regex: regexp.MustCompile(`(?i)try again (later|in)`), Description: "Retry message"},
	{Type: ErrorRateLimit, Regex: regexp.MustCompile(`(?i)requests per (minute|second|hour)`), Description: "Rate description"},
	{Type: ErrorRateLimit, Regex: regexp.MustCompile(`(?i)throttl(ed|ing)`), Description: "Throttling"},

	// Authentication errors (require context to avoid false positives)
	{Type: ErrorAuth, Regex: regexp.MustCompile(`(?i)(http|status|error|code).{0,10}\b401\b`), Description: "HTTP 401 Unauthorized"},
	{Type: ErrorAuth, Regex: regexp.MustCompile(`(?i)\b401\b.{0,10}(unauthorized|error|denied)`), Description: "401 with message"},
	{Type: ErrorAuth, Regex: regexp.MustCompile(`(?i)(http|status|error|code).{0,10}\b403\b`), Description: "HTTP 403 Forbidden"},
	{Type: ErrorAuth, Regex: regexp.MustCompile(`(?i)\b403\b.{0,10}(forbidden|error|denied)`), Description: "403 with message"},
	{Type: ErrorAuth, Regex: regexp.MustCompile(`(?i)\bunauthorized\b`), Description: "Unauthorized"},
	{Type: ErrorAuth, Regex: regexp.MustCompile(`(?i)\bforbidden\b`), Description: "Forbidden"},
	{Type: ErrorAuth, Regex: regexp.MustCompile(`(?i)(invalid|expired|missing)[\s._-]?(api[\s._-]?)?(key|token|credential)`), Description: "Invalid credentials"},
	{Type: ErrorAuth, Regex: regexp.MustCompile(`(?i)authentication (failed|error|required)`), Description: "Auth failure"},
	{Type: ErrorAuth, Regex: regexp.MustCompile(`(?i)access denied`), Description: "Access denied"},

	// Connection errors
	{Type: ErrorConnection, Regex: regexp.MustCompile(`(?i)connection (refused|reset|closed|timed?\s*out)`), Description: "Connection issue"},
	{Type: ErrorConnection, Literal: "ECONNREFUSED", Description: "ECONNREFUSED"},
	{Type: ErrorConnection, Literal: "ECONNRESET", Description: "ECONNRESET"},
	{Type: ErrorConnection, Literal: "ETIMEDOUT", Description: "ETIMEDOUT"},
	{Type: ErrorConnection, Literal: "ENOTFOUND", Description: "ENOTFOUND"},
	{Type: ErrorConnection, Regex: regexp.MustCompile(`(?i)network (error|unreachable)`), Description: "Network error"},
	{Type: ErrorConnection, Regex: regexp.MustCompile(`(?i)dns (error|resolution|lookup)`), Description: "DNS error"},
	{Type: ErrorConnection, Regex: regexp.MustCompile(`(?i)socket hang up`), Description: "Socket hang up"},
	{Type: ErrorConnection, Regex: regexp.MustCompile(`(?i)no route to host`), Description: "No route"},
	{Type: ErrorConnection, Regex: regexp.MustCompile(`(?i)host (not found|unreachable)`), Description: "Host unreachable"},

	// Crashes/Panics (most severe)
	{Type: ErrorCrash, Literal: "panic:", Description: "Go panic"},
	{Type: ErrorCrash, Literal: "fatal:", Description: "Fatal error"},
	{Type: ErrorCrash, Literal: "FATAL:", Description: "Fatal error uppercase"},
	{Type: ErrorCrash, Literal: "segmentation fault", Description: "Segfault"},
	{Type: ErrorCrash, Literal: "Segmentation fault", Description: "Segfault capitalized"},
	{Type: ErrorCrash, Literal: "SIGSEGV", Description: "SIGSEGV signal"},
	{Type: ErrorCrash, Literal: "SIGKILL", Description: "SIGKILL signal"},
	{Type: ErrorCrash, Literal: "SIGTERM", Description: "SIGTERM signal"},
	{Type: ErrorCrash, Literal: "Traceback (most recent", Description: "Python traceback"},
	{Type: ErrorCrash, Regex: regexp.MustCompile(`(?i)unhandled (exception|error|rejection)`), Description: "Unhandled exception"},
	{Type: ErrorCrash, Regex: regexp.MustCompile(`(?i)stack trace:`), Description: "Stack trace"},
	{Type: ErrorCrash, Regex: regexp.MustCompile(`at \S+:\d+:\d+`), Description: "JS stack frame"},

	// Generic errors (lowest priority - catch-all)
	{Type: ErrorGeneric, Regex: regexp.MustCompile(`(?i)^error:`), Description: "Error prefix"},
	{Type: ErrorGeneric, Regex: regexp.MustCompile(`(?i)\berror\b.*\bfailed\b`), Description: "Error failed"},
}

// DetectErrorInOutput scans output for known error patterns.
// It only scans the last N lines for performance and relevance.
// Returns ErrorNone if no errors detected.
func DetectErrorInOutput(output string) ErrorType {
	// Strip ANSI codes for cleaner matching
	output = StripANSI(output)

	// Check recent output only (last 50 lines) for performance
	lines := strings.Split(output, "\n")
	start := len(lines) - 50
	if start < 0 {
		start = 0
	}
	recent := strings.Join(lines[start:], "\n")

	// Patterns are ordered by priority, so first match wins
	for _, p := range errorPatterns {
		if p.Regex != nil && p.Regex.MatchString(recent) {
			return p.Type
		}
		if p.Literal != "" && strings.Contains(recent, p.Literal) {
			return p.Type
		}
	}

	return ErrorNone
}

// DetectAllErrorsInOutput returns all error types found in output.
// Useful for debugging or detailed error reporting.
func DetectAllErrorsInOutput(output string) []ErrorType {
	output = StripANSI(output)

	lines := strings.Split(output, "\n")
	start := len(lines) - 50
	if start < 0 {
		start = 0
	}
	recent := strings.Join(lines[start:], "\n")

	seen := make(map[ErrorType]bool)
	var errors []ErrorType

	for _, p := range errorPatterns {
		matched := false
		if p.Regex != nil && p.Regex.MatchString(recent) {
			matched = true
		}
		if p.Literal != "" && strings.Contains(recent, p.Literal) {
			matched = true
		}
		if matched && !seen[p.Type] {
			seen[p.Type] = true
			errors = append(errors, p.Type)
		}
	}

	return errors
}

// IsError returns true if the error type represents an actual error
func IsError(e ErrorType) bool {
	return e != ErrorNone
}

// AddErrorPattern allows adding custom error patterns at runtime
func AddErrorPattern(errorType ErrorType, pattern string, description string) error {
	regex, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}

	errorPatterns = append(errorPatterns, ErrorPattern{
		Type:        errorType,
		Regex:       regex,
		Description: description,
	})

	return nil
}
