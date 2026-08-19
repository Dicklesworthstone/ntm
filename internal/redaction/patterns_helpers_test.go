package redaction

import "sync"

// ResetPatterns resets compiled patterns between tests (test-only helper).
func ResetPatterns() {
	compileOnce = sync.Once{}
	compiledPatterns = nil
}
