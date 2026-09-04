// Package errsig defines what counts as *evidence* that an agent actually
// failed, as opposed to an agent talking about failure.
//
// Agent panes are full of prose. A reviewer writes "the planted negative
// failed as expected", a plan says "the diagnostics timeout is 20000ms", a
// finding notes that "cleanup catches BaseException", a transcript quotes a
// SHA-256 that happens to contain "429". Unanchored keyword scanning turned
// every one of those into an operational incident: healthy panes were marked
// ERROR, sessions were promoted to degraded, and orchestration lost usable
// capacity (ntm#297, ntm#299).
//
// The contract here is deliberately narrow: a keyword is evidence only when it
// appears in a position a runtime actually puts it in.
//
//   - Anchored signatures must start the line, after decoration only
//     (indentation, TUI glyphs, bracketed tags, timestamps, log levels).
//     "Failed to connect to host" qualifies; "…it failed naming the seven"
//     does not.
//   - Numeric codes must appear in a structured position — "HTTP 429",
//     "429 Too Many Requests", `"status": 429` — never as a bare number, which
//     is what made hashes and IDs look like provider limits.
//   - Process outcomes (nonzero exit, fatal signals) are structured by
//     construction and need no anchoring.
//
// Everything in this package is a pure predicate over already-ANSI-stripped
// text, so it is cheap and hermetically testable.
package errsig

import (
	"regexp"
	"strings"
)

// LineLead matches the decoration that agent TUIs and log formats put in front
// of a line's substantive content. It is a regex FRAGMENT intended to be
// spliced immediately after a `^` anchor; see Anchored.
//
// Covered: indentation and NBSP, box-drawing/bullet/status glyphs, quote and
// diff markers, a bracketed tag ("[stderr]"), an ISO-8601 or wall-clock
// timestamp, a log level, and a stream tag. Each is optional, so a bare line
// still anchors.
const LineLead = `[ \t\x{00a0}>|*+#·•◦⎿⏺⚠✗✘×⨯│┃┆┊╎▌▏└├─\-]*` +
	`(?:\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?\s+)?` +
	`(?:\d{1,2}:\d{2}:\d{2}(?:[.,]\d+)?\s+)?` +
	`(?:\[[^\]\n]{0,64}\]\s*)?` +
	`(?:(?i:trace|debug|info|warn|warning|error|err|fatal|critical|crit)\s*[:|]?\s+)?` +
	`(?:(?i:stderr|stdout)\s*[:>]\s*)?` +
	`[ \t]*`

// Anchored wraps a regex body so it matches only at the start of a line's
// content, tolerating leading decoration. The result is multiline, so it can
// be applied to a whole capture buffer.
func Anchored(body string) string {
	return `(?m)^` + LineLead + `(?:` + body + `)`
}

// HTTPStatus returns a pattern that matches an HTTP status code only where a
// runtime would actually emit it. code must be a literal decimal status.
//
// A bare number is never enough: "429" occurs inside hashes, IDs, byte counts
// and ordinary prose, and treating it as authoritative rate-limit evidence
// walled off healthy agents (ntm#299).
func HTTPStatus(code string, reasons ...string) string {
	alts := []string{
		`\bHTTP/\d(?:\.\d)?\s+` + code + `\b`,
		`\bHTTP\s+` + code + `\b`,
		`\b(?:status|statuscode|status_code|statusCode|http_status|httpstatus|code|error)["']?\s*[:=]\s*["']?` + code + `\b`,
		`\berror\s+` + code + `\b`,
		`\b` + code + `\s*[:-]\s*(?i:` + strings.Join(reasons, "|") + `)\b`,
		`\b` + code + `\s+(?i:` + strings.Join(reasons, "|") + `)\b`,
	}
	if len(reasons) == 0 {
		alts = alts[:len(alts)-2]
	}
	// Case-insensitive as a whole: real logs write "http/1.1 429",
	// `"Status": 429` and "Error 429" in every casing.
	return `(?i:` + strings.Join(alts, "|") + `)`
}

// NonzeroExitPattern matches a process outcome line reporting a nonzero exit.
// The status itself is the evidence, so no anchoring is required.
const NonzeroExitPattern = `(?i)\b(?:exit(?:ed)?\s+(?:with\s+)?(?:status|code)|exit\s+status|exit\s+code|returned\s+(?:exit\s+)?code)\s*[:=]?\s*(?:[1-9]\d*|0*[1-9]\d*)\b`

// FatalSignalPattern matches a fatal POSIX signal by name. Signal names do not
// occur in ordinary agent prose the way "failed" and "timeout" do, and a
// runtime that prints one has genuinely died.
const FatalSignalPattern = `\b(?:SIGSEGV|SIGABRT|SIGBUS|SIGILL|SIGFPE|SIGKILL)\b`

var (
	nonzeroExit  = regexp.MustCompile(NonzeroExitPattern)
	fatalSignal  = regexp.MustCompile(FatalSignalPattern)
	leadStripper = regexp.MustCompile(`^` + LineLead)
)

// TrimLead removes decoration from the front of a single line, exposing its
// substantive content. Useful for callers that classify with literal string
// comparisons rather than regexes.
func TrimLead(line string) string {
	return leadStripper.ReplaceAllString(line, "")
}

// HasNonzeroExit reports whether text contains a nonzero process-exit report.
func HasNonzeroExit(text string) bool { return nonzeroExit.MatchString(text) }

// HasFatalSignal reports whether text names a fatal POSIX signal.
func HasFatalSignal(text string) bool { return fatalSignal.MatchString(text) }
