package errsig

import "regexp"

// Test-only helpers moved out of errsig.go for the G1 dead-code gate.
//
// Production consumers compile the exported pattern constants themselves —
// internal/robot/patterns.go and internal/robot/health.go use
// NonzeroExitPattern/FatalSignalPattern, as does internal/alerts/generator.go —
// so these compiled copies and the predicates over them had no caller reachable
// from ./cmd/ntm. Only this package's tests use them, so they live here.

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
