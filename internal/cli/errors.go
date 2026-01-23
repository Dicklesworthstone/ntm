package cli

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// ErrorEntry represents a single error detected in pane output
type ErrorEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Session   string    `json:"session"`
	Pane      string    `json:"pane"`
	PaneIndex int       `json:"pane_index"`
	Line      int       `json:"line"`
	Content   string    `json:"content"`
	MatchType string    `json:"match_type"`
	Context   []string  `json:"context,omitempty"`
	AgentType string    `json:"agent_type,omitempty"`
}

// ErrorsResult contains all errors found in a session
type ErrorsResult struct {
	Session     string       `json:"session"`
	Errors      []ErrorEntry `json:"errors"`
	TotalErrors int          `json:"total_errors"`
	TotalLines  int          `json:"total_lines_searched"`
	PaneCount   int          `json:"panes_searched"`
	Timestamp   time.Time    `json:"timestamp"`
}

// Text outputs the errors result as human-readable text
func (r *ErrorsResult) Text(w io.Writer) error {
	t := theme.Current()

	if len(r.Errors) == 0 {
		fmt.Fprintf(w, "%s✓%s No errors found in session '%s'\n",
			colorize(t.Success), colorize(t.Text), r.Session)
		return nil
	}

	fmt.Fprintf(w, "\n%s%d error(s)%s found in session '%s'\n\n",
		colorize(t.Error), r.TotalErrors, colorize(t.Text), r.Session)

	lastPaneKey := ""
	for i, e := range r.Errors {
		// Print pane header when pane changes
		paneKey := e.Pane
		if paneKey != lastPaneKey {
			if i > 0 {
				fmt.Fprintln(w)
			}
			fmt.Fprintf(w, "%s─── %s (%s) ───%s\n",
				colorize(t.Surface1), e.Pane, e.AgentType, colorize(t.Text))
			lastPaneKey = paneKey
		}

		// Print context before (if any)
		contextBefore := len(e.Context) / 2
		for j := 0; j < contextBefore && j < len(e.Context); j++ {
			lineNum := e.Line - contextBefore + j
			if lineNum > 0 {
				fmt.Fprintf(w, "%s%d-%s %s\n",
					colorize(t.Surface1), lineNum, colorize(t.Text), e.Context[j])
			}
		}

		// Print error line with highlighting
		fmt.Fprintf(w, "%s%d:%s %s%s%s [%s]\n",
			colorize(t.Red), e.Line, colorize(t.Text),
			colorize(t.Red), e.Content, colorize(t.Text), e.MatchType)

		// Print context after (if any)
		for j := contextBefore; j < len(e.Context); j++ {
			lineNum := e.Line + j - contextBefore + 1
			fmt.Fprintf(w, "%s%d-%s %s\n",
				colorize(t.Surface1), lineNum, colorize(t.Text), e.Context[j])
		}
	}

	fmt.Fprintf(w, "\n%sSearched %d pane(s), %d line(s)%s\n",
		colorize(t.Surface1), r.PaneCount, r.TotalLines, colorize(t.Text))

	return nil
}

// JSON returns the JSON-serializable data
func (r *ErrorsResult) JSON() interface{} {
	return r
}

// Error pattern matchers for the errors command
var errorsCommandPatterns = []struct {
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

// isErrorLine checks if a line matches any error pattern and returns the match type
func isErrorLine(line string) (bool, string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return false, ""
	}

	for _, p := range errorsCommandPatterns {
		if p.Pattern.MatchString(line) {
			return true, p.MatchType
		}
	}
	return false, ""
}

// ErrorsOptions contains options for the errors operation
type ErrorsOptions struct {
	Since        time.Duration
	Panes        []int
	ContextLines int
	MaxLines     int
	Follow       bool
	Filter       AgentFilter
}

func newErrorsCmd() *cobra.Command {
	var (
		since        string
		panes        string
		contextLines int
		maxLines     int
		follow       bool
		ccFlag       bool
		codFlag      bool
		gmiFlag      bool
	)

	cmd := &cobra.Command{
		Use:   "errors [session-name]",
		Short: "Show only error output from agents",
		Long: `Filter and display only error-related output from agent panes.

Searches pane output for error patterns including:
- Exception tracebacks (Python, JavaScript, Go, Rust)
- Error/FATAL/CRITICAL messages
- Build and test failures
- Non-zero exit codes
- Rate limit and context errors

Examples:
  ntm errors myproject                   # Show all errors
  ntm errors myproject --since 5m        # Errors in last 5 minutes
  ntm errors myproject --cc              # Only Claude pane errors
  ntm errors myproject --panes 1,2,3     # Specific panes only
  ntm errors myproject -C 5              # 5 lines context
  ntm errors myproject --follow          # Stream new errors`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var session string
			if len(args) > 0 {
				session = args[0]
			}

			// Parse since duration
			var sinceDuration time.Duration
			if since != "" {
				var err error
				sinceDuration, err = time.ParseDuration(since)
				if err != nil {
					return fmt.Errorf("invalid --since duration: %w", err)
				}
			}

			// Parse pane indices
			var paneIndices []int
			if panes != "" {
				for _, p := range strings.Split(panes, ",") {
					p = strings.TrimSpace(p)
					if p == "" {
						continue
					}
					idx, err := strconv.Atoi(p)
					if err != nil {
						return fmt.Errorf("invalid pane index '%s': %w", p, err)
					}
					paneIndices = append(paneIndices, idx)
				}
			}

			filter := AgentFilter{
				Claude: ccFlag,
				Codex:  codFlag,
				Gemini: gmiFlag,
			}

			opts := ErrorsOptions{
				Since:        sinceDuration,
				Panes:        paneIndices,
				ContextLines: contextLines,
				MaxLines:     maxLines,
				Follow:       follow,
				Filter:       filter,
			}

			return runErrors(session, opts)
		},
	}

	cmd.Flags().StringVar(&since, "since", "", "Only errors from last duration (e.g., 5m, 1h)")
	cmd.Flags().StringVar(&panes, "panes", "", "Comma-separated pane indices to filter")
	cmd.Flags().IntVarP(&contextLines, "context", "C", 2, "Lines of context before and after each error")
	cmd.Flags().IntVarP(&maxLines, "max-lines", "n", 1000, "Search last N lines per pane")
	cmd.Flags().BoolVar(&follow, "follow", false, "Stream new errors in real-time")
	cmd.Flags().BoolVar(&ccFlag, "cc", false, "Show Claude pane errors only")
	cmd.Flags().BoolVar(&codFlag, "cod", false, "Show Codex pane errors only")
	cmd.Flags().BoolVar(&gmiFlag, "gmi", false, "Show Gemini pane errors only")

	return cmd
}

func runErrors(session string, opts ErrorsOptions) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	// Resolve session
	res, err := ResolveSession(session, os.Stdout)
	if err != nil {
		return err
	}
	if res.Session == "" {
		return nil
	}
	res.ExplainIfInferred(os.Stderr)
	session = res.Session

	if !tmux.SessionExists(session) {
		return fmt.Errorf("session '%s' not found", session)
	}

	// Get panes
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return fmt.Errorf("failed to get panes: %w", err)
	}

	// Collect errors from all matching panes
	var allErrors []ErrorEntry
	totalLines := 0
	panesSearched := 0

	for _, pane := range panes {
		// Apply pane index filter
		if len(opts.Panes) > 0 {
			found := false
			for _, idx := range opts.Panes {
				if pane.Index == idx {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		// Apply agent filter
		if opts.Filter.IsEmpty() {
			// By default, skip user pane
			if pane.Type == tmux.AgentUser {
				continue
			}
		} else if !opts.Filter.Matches(pane.Type) {
			continue
		}

		// Capture pane output
		paneOutput, err := tmux.CapturePaneOutput(pane.ID, opts.MaxLines)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to capture pane %s: %v\n", pane.Title, err)
			continue
		}

		lines := strings.Split(paneOutput, "\n")
		totalLines += len(lines)
		panesSearched++

		// Search for errors
		for i, line := range lines {
			isError, matchType := isErrorLine(line)
			if !isError {
				continue
			}

			// Build context
			var context []string
			beforeCount := opts.ContextLines
			afterCount := opts.ContextLines

			// Get lines before
			for j := i - beforeCount; j < i && j >= 0; j++ {
				context = append(context, lines[j])
			}
			// Get lines after
			for j := i + 1; j <= i+afterCount && j < len(lines); j++ {
				context = append(context, lines[j])
			}

			// Determine agent type string
			agentType := "unknown"
			switch pane.Type {
			case tmux.AgentClaude:
				agentType = "claude"
			case tmux.AgentCodex:
				agentType = "codex"
			case tmux.AgentGemini:
				agentType = "gemini"
			case tmux.AgentUser:
				agentType = "user"
			}

			allErrors = append(allErrors, ErrorEntry{
				Timestamp: time.Now(),
				Session:   session,
				Pane:      pane.Title,
				PaneIndex: pane.Index,
				Line:      i + 1, // 1-indexed
				Content:   line,
				MatchType: matchType,
				Context:   context,
				AgentType: agentType,
			})
		}
	}

	// Build result
	result := &ErrorsResult{
		Session:     session,
		Errors:      allErrors,
		TotalErrors: len(allErrors),
		TotalLines:  totalLines,
		PaneCount:   panesSearched,
		Timestamp:   time.Now(),
	}

	// Output result
	formatter := output.New(output.WithJSON(jsonOutput))
	return formatter.Output(result)
}

// filterBySince filters errors to only those after the since duration
func filterBySince(errors []ErrorEntry, since time.Duration) []ErrorEntry {
	if since <= 0 {
		return errors
	}

	cutoff := time.Now().Add(-since)
	var filtered []ErrorEntry
	for _, e := range errors {
		if e.Timestamp.After(cutoff) {
			filtered = append(filtered, e)
		}
	}
	return filtered
}
