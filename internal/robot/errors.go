// Package robot provides machine-readable output for AI agents.
// errors.go implements the --robot-errors command for filtering error output from agent panes.
package robot

import (
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
	Since     string   // Filter to errors since duration (e.g., "5m", "1h")
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
	switch t {
	case tmux.AgentClaude:
		return "claude"
	case tmux.AgentCodex:
		return "codex"
	case tmux.AgentGemini:
		return "gemini"
	case tmux.AgentUser:
		return "user"
	default:
		return "unknown"
	}
}

// PrintErrors outputs filtered error lines from session panes.
func PrintErrors(opts ErrorsOptions) error {
	output := ErrorsOutput{
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
		output.Success = false
		output.Error = "session name required"
		output.ErrorCode = "INVALID_ARGS"
		return encodeJSON(output)
	}

	if !tmux.SessionExists(opts.Session) {
		output.Success = false
		output.Error = "session not found: " + opts.Session
		output.ErrorCode = "SESSION_NOT_FOUND"
		return encodeJSON(output)
	}

	// Get panes
	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		output.Success = false
		output.Error = "failed to get panes: " + err.Error()
		output.ErrorCode = "TMUX_ERROR"
		return encodeJSON(output)
	}

	// Set defaults
	if opts.Lines <= 0 {
		opts.Lines = 1000
	}
	if opts.Context <= 0 {
		opts.Context = 2
	}

	// Build pane filter set
	paneFilter := make(map[int]bool)
	for _, p := range opts.Panes {
		// Parse pane index
		var idx int
		if _, err := parseErrorsIndex(p, &idx); err == nil {
			paneFilter[idx] = true
		}
	}

	// Process each pane
	for _, pane := range panes {
		// Skip user pane by default
		if pane.Type == tmux.AgentUser {
			continue
		}

		// Apply pane filter
		if len(paneFilter) > 0 && !paneFilter[pane.Index] {
			continue
		}

		// Apply agent type filter
		agentType := agentTypeFromPaneType(pane.Type)
		if opts.AgentType != "" {
			if !strings.EqualFold(agentType, opts.AgentType) {
				continue
			}
		}

		// Capture pane output
		paneOutput, err := tmux.CapturePaneOutput(pane.ID, opts.Lines)
		if err != nil {
			continue
		}

		lines := strings.Split(paneOutput, "\n")
		output.Summary.TotalLines += len(lines)
		output.Summary.PanesSearched++

		// Search for errors
		for i, line := range lines {
			isError, matchType := isRobotErrorLine(line)
			if !isError {
				continue
			}

			// Build context
			var context []string
			for j := i - opts.Context; j < i && j >= 0; j++ {
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

	return encodeJSON(output)
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
