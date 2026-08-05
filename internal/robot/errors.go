// Package robot provides machine-readable output for AI agents.
// errors.go implements the --robot-errors command for filtering error output from agent panes.
package robot

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// =============================================================================
// Robot Errors Command
// =============================================================================
//
// The errors command filters pane output to show only error-related lines.
// This helps AI agents quickly identify failures without scanning all output.

// ErrorsOptions configures the errors command.
type ErrorsOptions struct {
	Session   string   // Session name (required)
	Panes     []string // Pane indices to check (empty = all agent panes)
	Lines     int      // Lines to capture per pane (default: 1000)
	AgentType string   // Filter by agent type (claude, codex, gemini)
	Context   int      // Context lines before/after error (default: 2)
}

// DefaultErrorsOptions returns sensible defaults.
func DefaultErrorsOptions() ErrorsOptions {
	return ErrorsOptions{
		Lines:   1000,
		Context: 2,
	}
}

// ErrorEntry represents a single error detected in pane output.
type ErrorEntry struct {
	Timestamp  time.Time `json:"timestamp"`
	PaneID     string    `json:"pane_id"`
	PaneName   string    `json:"pane_name"`
	PaneIndex  int       `json:"pane_index"`
	AgentType  string    `json:"agent_type"`
	LineNumber int       `json:"line_number"`
	Content    string    `json:"content"`
	MatchType  string    `json:"match_type"`
	Context    []string  `json:"context,omitempty"`
}

// ErrorsSummary provides aggregate statistics.
type ErrorsSummary struct {
	TotalErrors   int            `json:"total_errors"`
	TotalLines    int            `json:"total_lines_searched"`
	PanesSearched int            `json:"panes_searched"`
	ByType        map[string]int `json:"by_type"`
	ByAgent       map[string]int `json:"by_agent"`
}

// ErrorsOutput is the response for --robot-errors.
type ErrorsOutput struct {
	RobotResponse
	Session string        `json:"session"`
	Errors  []ErrorEntry  `json:"errors"`
	Summary ErrorsSummary `json:"summary"`
}

// Error pattern matchers for robot errors command
var robotErrorPatterns = []struct {
	Pattern   *regexp.Regexp
	MatchType string
}{
	// Python errors
	{regexp.MustCompile(`(?i)^Traceback \(most recent call last\)`), "traceback"},
	{regexp.MustCompile(`(?i)^\s*(FileNotFoundError|ImportError|ModuleNotFoundError|AttributeError|TypeError|ValueError|KeyError|IndexError|RuntimeError|NameError|SyntaxError|IndentationError|ZeroDivisionError|AssertionError|OSError|PermissionError|ConnectionError|TimeoutError):`), "exception"},

	// Go errors
	{regexp.MustCompile(`(?i)^panic:`), "panic"},
	{regexp.MustCompile(`(?i)^goroutine \d+ \[running\]:`), "panic"},
	{regexp.MustCompile(`(?i)fatal error:`), "fatal"},

	// JavaScript/Node errors
	{regexp.MustCompile(`(?i)^(TypeError|ReferenceError|SyntaxError|RangeError|EvalError|URIError):`), "exception"},
	{regexp.MustCompile(`(?i)Error: ENOENT|EACCES|EEXIST|ENOTDIR|EISDIR|EMFILE|EPERM`), "error"},

	// Rust errors
	{regexp.MustCompile(`(?i)^thread '.+' panicked at`), "panic"},
	{regexp.MustCompile(`(?i)^error\[E\d+\]:`), "error"},

	// Generic error patterns
	{regexp.MustCompile(`(?i)^error:`), "error"},
	{regexp.MustCompile(`(?i)^Error:`), "error"},
	{regexp.MustCompile(`(?i)^ERROR[:\s]`), "error"},
	{regexp.MustCompile(`(?i)^FATAL[:\s]`), "fatal"},
	{regexp.MustCompile(`(?i)^CRITICAL[:\s]`), "critical"},

	// Build/test failures
	{regexp.MustCompile(`(?i)\bfailed\b.*:|\bFAILED\b`), "failed"},
	{regexp.MustCompile(`(?i)^FAIL\s+`), "failed"},
	{regexp.MustCompile(`(?i)^--- FAIL:`), "failed"},
	{regexp.MustCompile(`(?i)build failed`), "failed"},
	{regexp.MustCompile(`(?i)compilation failed`), "failed"},

	// Exit codes
	{regexp.MustCompile(`(?i)exit(?:ed)?\s+(?:with\s+)?(?:code|status)\s+[1-9]\d*`), "exit"},
	{regexp.MustCompile(`(?i)Process exited with code [1-9]\d*`), "exit"},

	// Stack traces
	{regexp.MustCompile(`^\s+at\s+.+\(.+:\d+:\d+\)`), "stacktrace"},

	// Agent-specific errors
	{regexp.MustCompile(`(?i)^claude.*error`), "agent_error"},
	{regexp.MustCompile(`(?i)rate limit|too many requests|429`), "rate_limit"},
	{regexp.MustCompile(`(?i)context.*(?:exceeded|limit|full|window)`), "context_limit"},
}

// isRobotErrorLine checks if a line matches any error pattern.
func isRobotErrorLine(line string) (bool, string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return false, ""
	}

	for _, p := range robotErrorPatterns {
		if p.Pattern.MatchString(line) {
			return true, p.MatchType
		}
	}
	return false, ""
}

// agentTypeFromPaneType converts tmux pane type to string.
func agentTypeFromPaneType(t tmux.AgentType) string {
	return agentTypeString(t)
}

func detectErrorsPaneAgentType(pane tmux.Pane, paneOutput string) string {
	detection := DetectAgentTypeEnhanced(pane, paneOutput)
	return detection.Type
}

// GetErrors returns filtered error lines from session panes.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetErrors(opts ErrorsOptions) (*ErrorsOutput, error) {
	output := &ErrorsOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		Errors:        []ErrorEntry{},
		Summary: ErrorsSummary{
			ByType:  make(map[string]int),
			ByAgent: make(map[string]int),
		},
	}

	// Validate session
	if opts.Session == "" {
		output.RobotResponse = NewErrorResponse(
			nil,
			"INVALID_ARGS",
			"Provide session name: ntm --robot-errors=myproject",
		)
		output.Error = "session name required"
		return output, nil
	}

	if !tmux.SessionExists(opts.Session) {
		output.RobotResponse = NewErrorResponse(
			nil,
			ErrCodeSessionNotFound,
			"Use 'ntm list' to see available sessions",
		)
		output.Error = "session not found: " + opts.Session
		return output, nil
	}

	// Get panes
	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			err,
			ErrCodeInternalError,
			"Check tmux session state",
		)
		output.Error = "failed to get panes: " + err.Error()
		return output, nil
	}

	// Set defaults
	if opts.Lines <= 0 {
		opts.Lines = 1000
	}
	if opts.Context <= 0 {
		opts.Context = 2
	}

	// Build pane filter set. parseErrorsIndex reports success through its bool,
	// never through the error, so checking the error accepted every malformed
	// selector and silently filtered to pane 0 instead.
	paneFilter := make(map[int]bool)
	for _, p := range opts.Panes {
		var idx int
		ok, err := parseErrorsIndex(p, &idx)
		if err != nil || !ok {
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("invalid --panes value %q: expected a pane index", p),
				ErrCodeInvalidFlag,
				"Pass pane indices such as --panes=1,2",
			)
			return output, nil
		}
		paneFilter[idx] = true
	}

	// Process each pane
	for _, pane := range panes {
		// Apply pane filter
		if len(paneFilter) > 0 && !paneFilter[pane.Index] {
			continue
		}

		// Capture pane output before filtering so enhanced detection can use it.
		paneOutput, err := tmux.CapturePaneOutput(pane.ID, opts.Lines)
		if err != nil {
			continue
		}

		agentType := detectErrorsPaneAgentType(pane, paneOutput)
		if agentType == "user" || agentType == "unknown" || agentType == "" {
			continue
		}
		if !matchesAgentTypeFilter(agentType, opts.AgentType) {
			continue
		}

		lines := splitLines(paneOutput)
		output.Summary.TotalLines += len(lines)
		output.Summary.PanesSearched++

		// Search for errors
		for i, line := range lines {
			isError, matchType := isRobotErrorLine(line)
			if !isError {
				continue
			}

			// Build context. The start index must be clamped, not filtered by the
			// loop condition: for an error near the top of the buffer the initial
			// j was negative, `j >= 0` failed immediately, and every preceding
			// line was dropped even though some existed.
			var context []string
			for j := max(0, i-opts.Context); j < i; j++ {
				context = append(context, lines[j])
			}
			for j := i + 1; j <= i+opts.Context && j < len(lines); j++ {
				context = append(context, lines[j])
			}

			entry := ErrorEntry{
				Timestamp:  time.Now(),
				PaneID:     pane.ID,
				PaneName:   pane.Title,
				PaneIndex:  pane.Index,
				AgentType:  agentType,
				LineNumber: i + 1,
				Content:    line,
				MatchType:  matchType,
				Context:    context,
			}

			output.Errors = append(output.Errors, entry)
			output.Summary.TotalErrors++
			output.Summary.ByType[matchType]++
			output.Summary.ByAgent[agentType]++
		}
	}

	return output, nil
}

// PrintErrors outputs filtered error lines from session panes.
// This is a thin wrapper around GetErrors() for CLI output.
func PrintErrors(opts ErrorsOptions) error {
	output, err := GetErrors(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot error scan failed")
}

// parseErrorsIndex parses a string to int (helper).
func parseErrorsIndex(s string, idx *int) (bool, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return false, nil
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return false, nil
		}
		n = n*10 + int(c-'0')
	}
	*idx = n
	return true, nil
}
