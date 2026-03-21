package robot

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestIsRobotErrorLine_PythonErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		line      string
		wantMatch bool
		wantType  string
	}{
		{"Traceback (most recent call last)", true, "traceback"},
		{"traceback (most recent call last)", true, "traceback"},
		{"FileNotFoundError: [Errno 2] No such file", true, "exception"},
		{"ImportError: No module named 'foo'", true, "exception"},
		{"  TypeError: unsupported operand type", true, "exception"},
		{"ValueError: invalid literal", true, "exception"},
		{"KeyError: 'missing_key'", true, "exception"},
		{"IndexError: list index out of range", true, "exception"},
		{"RuntimeError: maximum recursion depth", true, "exception"},
		{"NameError: name 'x' is not defined", true, "exception"},
		{"SyntaxError: unexpected EOF", true, "exception"},
		{"ZeroDivisionError: division by zero", true, "exception"},
		{"PermissionError: [Errno 13]", true, "exception"},
		{"ConnectionError: connection refused", true, "exception"},
		{"TimeoutError: timed out", true, "exception"},
	}

	for _, tc := range tests {
		t.Run(tc.line, func(t *testing.T) {
			match, matchType := isRobotErrorLine(tc.line)
			if match != tc.wantMatch {
				t.Errorf("isRobotErrorLine(%q) match = %v, want %v", tc.line, match, tc.wantMatch)
			}
			if matchType != tc.wantType {
				t.Errorf("isRobotErrorLine(%q) type = %q, want %q", tc.line, matchType, tc.wantType)
			}
		})
	}
}

func TestIsRobotErrorLine_GoErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		line      string
		wantMatch bool
		wantType  string
	}{
		{"panic: runtime error: index out of range", true, "panic"},
		{"goroutine 1 [running]:", true, "panic"},
		{"fatal error: concurrent map writes", true, "fatal"},
	}

	for _, tc := range tests {
		t.Run(tc.line, func(t *testing.T) {
			match, matchType := isRobotErrorLine(tc.line)
			if match != tc.wantMatch {
				t.Errorf("isRobotErrorLine(%q) match = %v, want %v", tc.line, match, tc.wantMatch)
			}
			if matchType != tc.wantType {
				t.Errorf("isRobotErrorLine(%q) type = %q, want %q", tc.line, matchType, tc.wantType)
			}
		})
	}
}

func TestIsRobotErrorLine_JSErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		line      string
		wantMatch bool
		wantType  string
	}{
		{"TypeError: Cannot read properties of undefined", true, "exception"},
		{"ReferenceError: x is not defined", true, "exception"},
		{"SyntaxError: Unexpected token", true, "exception"},
		{"Error: ENOENT: no such file or directory", true, "error"},
		{"Error: EACCES: permission denied", true, "error"},
	}

	for _, tc := range tests {
		t.Run(tc.line, func(t *testing.T) {
			match, matchType := isRobotErrorLine(tc.line)
			if match != tc.wantMatch {
				t.Errorf("isRobotErrorLine(%q) match = %v, want %v", tc.line, match, tc.wantMatch)
			}
			if matchType != tc.wantType {
				t.Errorf("isRobotErrorLine(%q) type = %q, want %q", tc.line, matchType, tc.wantType)
			}
		})
	}
}

func TestIsRobotErrorLine_RustErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		line      string
		wantMatch bool
		wantType  string
	}{
		{"thread 'main' panicked at 'index out of bounds'", true, "panic"},
		{"error[E0308]: mismatched types", true, "error"},
	}

	for _, tc := range tests {
		t.Run(tc.line, func(t *testing.T) {
			match, matchType := isRobotErrorLine(tc.line)
			if match != tc.wantMatch {
				t.Errorf("isRobotErrorLine(%q) match = %v, want %v", tc.line, match, tc.wantMatch)
			}
			if matchType != tc.wantType {
				t.Errorf("isRobotErrorLine(%q) type = %q, want %q", tc.line, matchType, tc.wantType)
			}
		})
	}
}

func TestIsRobotErrorLine_GenericPatterns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		line      string
		wantMatch bool
		wantType  string
	}{
		{"error: something went wrong", true, "error"},
		{"Error: bad request", true, "error"},
		{"ERROR: disk full", true, "error"},
		{"ERROR disk full", true, "error"},
		{"FATAL: out of memory", true, "fatal"},
		{"FATAL out of memory", true, "fatal"},
		{"CRITICAL: service down", true, "critical"},
		{"CRITICAL service down", true, "critical"},
	}

	for _, tc := range tests {
		t.Run(tc.line, func(t *testing.T) {
			match, matchType := isRobotErrorLine(tc.line)
			if match != tc.wantMatch {
				t.Errorf("isRobotErrorLine(%q) match = %v, want %v", tc.line, match, tc.wantMatch)
			}
			if matchType != tc.wantType {
				t.Errorf("isRobotErrorLine(%q) type = %q, want %q", tc.line, matchType, tc.wantType)
			}
		})
	}
}

func TestIsRobotErrorLine_BuildTestFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		line      string
		wantMatch bool
		wantType  string
	}{
		{"FAIL\tgithub.com/foo/bar\t0.5s", true, "failed"},
		{"--- FAIL: TestSomething (0.01s)", true, "failed"},
		{"build failed", true, "failed"},
		{"compilation failed", true, "failed"},
		{"FAILED", true, "failed"},
	}

	for _, tc := range tests {
		t.Run(tc.line, func(t *testing.T) {
			match, matchType := isRobotErrorLine(tc.line)
			if match != tc.wantMatch {
				t.Errorf("isRobotErrorLine(%q) match = %v, want %v", tc.line, match, tc.wantMatch)
			}
			if matchType != tc.wantType {
				t.Errorf("isRobotErrorLine(%q) type = %q, want %q", tc.line, matchType, tc.wantType)
			}
		})
	}
}

func TestIsRobotErrorLine_ExitCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		line      string
		wantMatch bool
		wantType  string
	}{
		{"exit code 1", true, "exit"},
		{"exited with code 127", true, "exit"},
		{"exit status 2", true, "exit"},
		{"Process exited with code 1", true, "exit"},
		// Exit code 0 should NOT match
		{"exit code 0", false, ""},
		{"exit status 0", false, ""},
	}

	for _, tc := range tests {
		t.Run(tc.line, func(t *testing.T) {
			match, matchType := isRobotErrorLine(tc.line)
			if match != tc.wantMatch {
				t.Errorf("isRobotErrorLine(%q) match = %v, want %v", tc.line, match, tc.wantMatch)
			}
			if matchType != tc.wantType {
				t.Errorf("isRobotErrorLine(%q) type = %q, want %q", tc.line, matchType, tc.wantType)
			}
		})
	}
}

func TestIsRobotErrorLine_AgentErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		line      string
		wantMatch bool
		wantType  string
	}{
		{"rate limit exceeded", true, "rate_limit"},
		{"too many requests", true, "rate_limit"},
		{"429 Too Many Requests", true, "rate_limit"},
		{"context window exceeded", true, "context_limit"},
		{"context limit reached", true, "context_limit"},
		{"claude error: api timeout", true, "agent_error"},
	}

	for _, tc := range tests {
		t.Run(tc.line, func(t *testing.T) {
			match, matchType := isRobotErrorLine(tc.line)
			if match != tc.wantMatch {
				t.Errorf("isRobotErrorLine(%q) match = %v, want %v", tc.line, match, tc.wantMatch)
			}
			if matchType != tc.wantType {
				t.Errorf("isRobotErrorLine(%q) type = %q, want %q", tc.line, matchType, tc.wantType)
			}
		})
	}
}

func TestIsRobotErrorLine_NonErrors(t *testing.T) {
	t.Parallel()

	nonErrors := []string{
		"",
		"   ",
		"Hello world",
		"Processing file...",
		"ok\tgithub.com/foo/bar\t0.5s",
		"--- PASS: TestSomething (0.01s)",
		"Build succeeded",
		"exit code 0",
		"normal log output here",
		"no errors found",
	}

	for _, line := range nonErrors {
		t.Run(line, func(t *testing.T) {
			match, matchType := isRobotErrorLine(line)
			if match {
				t.Errorf("isRobotErrorLine(%q) matched as %q, want no match", line, matchType)
			}
		})
	}
}

func TestIsRobotErrorLine_StackTrace(t *testing.T) {
	t.Parallel()

	// The stacktrace pattern requires leading whitespace (^\s+at\s+...),
	// but isRobotErrorLine trims the input first, so trimmed stack trace
	// lines won't match (the leading spaces are gone). This tests that
	// current behavior is consistent.
	line := "    at Object.<anonymous> (/app/server.js:42:13)"
	match, _ := isRobotErrorLine(line)
	if match {
		t.Errorf("trimmed stack trace line should not match (leading whitespace stripped)")
	}
}

func TestParseErrorsIndex(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantOK  bool
		wantIdx int
	}{
		{"simple number", "5", true, 5},
		{"zero", "0", true, 0},
		{"multi-digit", "42", true, 42},
		{"large number", "999", true, 999},
		{"padded spaces", "  7  ", true, 7},
		{"empty string", "", false, 0},
		{"whitespace only", "   ", false, 0},
		{"non-numeric", "abc", false, 0},
		{"mixed", "12abc", false, 0},
		{"negative", "-1", false, 0},
		{"decimal", "3.5", false, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var idx int
			ok, err := parseErrorsIndex(tc.input, &idx)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok != tc.wantOK {
				t.Errorf("parseErrorsIndex(%q) ok = %v, want %v", tc.input, ok, tc.wantOK)
			}
			if ok && idx != tc.wantIdx {
				t.Errorf("parseErrorsIndex(%q) idx = %d, want %d", tc.input, idx, tc.wantIdx)
			}
		})
	}
}

func TestAgentTypeFromPaneType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    tmux.AgentType
		expected string
	}{
		{tmux.AgentClaude, "claude"},
		{tmux.AgentCodex, "codex"},
		{tmux.AgentGemini, "gemini"},
		{tmux.AgentCursor, "cursor"},
		{tmux.AgentWindsurf, "windsurf"},
		{tmux.AgentAider, "aider"},
		{tmux.AgentUser, "user"},
		{tmux.AgentUnknown, "unknown"},
		// Types not in the switch should return "unknown"
		{tmux.AgentType("unknown_type"), "unknown"},
	}

	for _, tc := range tests {
		t.Run(string(tc.input), func(t *testing.T) {
			result := agentTypeFromPaneType(tc.input)
			if result != tc.expected {
				t.Errorf("agentTypeFromPaneType(%q) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

func TestDefaultErrorsOptions(t *testing.T) {
	t.Parallel()

	opts := DefaultErrorsOptions()
	if opts.Lines != 1000 {
		t.Errorf("Lines = %d, want 1000", opts.Lines)
	}
	if opts.Context != 2 {
		t.Errorf("Context = %d, want 2", opts.Context)
	}
	if opts.Session != "" {
		t.Errorf("Session = %q, want empty", opts.Session)
	}
	if len(opts.Panes) != 0 {
		t.Errorf("Panes = %v, want empty", opts.Panes)
	}
	if opts.AgentType != "" {
		t.Errorf("AgentType = %q, want empty", opts.AgentType)
	}
	if opts.Since != "" {
		t.Errorf("Since = %q, want empty", opts.Since)
	}
}

func TestRobotErrorPatterns_NotEmpty(t *testing.T) {
	t.Parallel()

	if len(robotErrorPatterns) == 0 {
		t.Error("robotErrorPatterns should not be empty")
	}

	// Every pattern should have a non-empty MatchType
	for i, p := range robotErrorPatterns {
		if p.Pattern == nil {
			t.Errorf("pattern[%d] has nil regex", i)
		}
		if p.MatchType == "" {
			t.Errorf("pattern[%d] has empty MatchType", i)
		}
	}
}
