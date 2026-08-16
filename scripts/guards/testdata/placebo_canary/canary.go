// Package canary is the G5 placebo-lint canary fixture (bd-ws0-guards-klz98.6).
// It intentionally carries unwaivered stub-marker language, including the
// VERBATIM D5 simulator comment; scripts/guards/placebo_lint.sh must fire on
// this directory before every real scan or it aborts as a placebo guard.
// Living under testdata/, the Go toolchain ignores this file.
package canary

// CanaryStub is deliberately unwaivered. For now, just log and pass.
func CanaryStub() {
	// Simulate job execution - in production, this would dispatch to actual handlers
}
