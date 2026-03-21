package cli

import (
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func newGetAllSessionTextCmd() *cobra.Command {
	var lines int
	var compact bool

	cmd := &cobra.Command{
		Use:     "get-all-session-text",
		Aliases: []string{"gast", "swarm-status"},
		Short:   "Get text from all panes across all sessions as markdown table",
		Long: `Captures output from all panes in all tmux sessions and displays as a markdown table.

Each row represents a session, with columns for:
- Session name
- Controller pane (pane 1) last message and status
- Worker panes (pane 2+) last messages and status
- Detected errors (rate limits, crashes, etc.)

This is useful for AI agents orchestrating multiple agent swarms to monitor progress.

Examples:
  ntm get-all-session-text           # All sessions, 10 lines per pane
  ntm get-all-session-text --lines=5 # Fewer lines, faster
  ntm gast --compact                 # Ultra-compact output`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGetAllSessionText(cmd.OutOrStdout(), lines, compact)
		},
	}

	cmd.Flags().IntVar(&lines, "lines", 10, "Number of lines to capture per pane")
	cmd.Flags().BoolVar(&compact, "compact", false, "Ultra-compact output (one line per session)")

	return cmd
}

// paneStatus holds captured status for a single pane
type paneStatus struct {
	Index     int
	Type      string // cc, cod, gmi, user
	LastLine  string
	State     string // WAITING, GENERATING, THINKING, ERROR, UNKNOWN
	Errors    []string
	RateLimit bool
	HasError  bool
}

// sessionStatus holds status for an entire session
type sessionStatus struct {
	Name       string
	Controller *paneStatus
	Workers    []*paneStatus
	ErrorCount int
	RateLimits int
}

func runGetAllSessionText(w io.Writer, lines int, compact bool) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	sessions, err := tmux.ListSessions()
	if err != nil {
		return fmt.Errorf("failed to list sessions: %w", err)
	}

	if len(sessions) == 0 {
		fmt.Fprintln(w, "No active tmux sessions found.")
		return nil
	}

	// Collect status for all sessions
	var statuses []sessionStatus
	for _, sess := range sessions {
		status := collectSessionStatus(sess.Name, lines)
		statuses = append(statuses, status)
	}

	// Render output
	if compact {
		renderCompactTable(w, statuses)
	} else {
		renderFullTable(w, statuses)
	}

	return nil
}

func collectSessionStatus(sessionName string, lines int) sessionStatus {
	status := sessionStatus{
		Name:    sessionName,
		Workers: make([]*paneStatus, 0),
	}

	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		return status
	}

	// Get first window index
	firstWin, err := tmux.GetFirstWindow(sessionName)
	if err != nil {
		firstWin = 1
	}

	for _, pane := range panes {
		target := fmt.Sprintf("%s:%d.%d", sessionName, firstWin, pane.Index)
		captured, err := tmux.CapturePaneOutput(target, lines)
		if err != nil {
			continue
		}

		ps := analyzePaneOutput(pane, captured)

		// Pane index 1 is controller (based on user's swarm setup)
		if pane.Index == 1 {
			status.Controller = ps
		} else {
			status.Workers = append(status.Workers, ps)
		}

		if ps.HasError {
			status.ErrorCount++
		}
		if ps.RateLimit {
			status.RateLimits++
		}
	}

	return status
}

func analyzePaneOutput(pane tmux.Pane, captured string) *paneStatus {
	ps := &paneStatus{
		Index:  pane.Index,
		Type:   getAgentTypeShort(pane.Type),
		Errors: make([]string, 0),
	}

	// Clean and get last meaningful line
	cleanOutput := status.StripANSI(captured)
	lines := strings.Split(cleanOutput, "\n")
	ps.LastLine = getLastMeaningfulLine(lines)

	// Detect state using robot patterns
	ps.State = detectPaneState(cleanOutput, ps.Type)

	// Check for specific errors
	ps.RateLimit = detectRateLimit(cleanOutput)
	ps.Errors = detectErrors(cleanOutput)
	ps.HasError = ps.RateLimit || len(ps.Errors) > 0 || ps.State == "ERROR"

	return ps
}

func getAgentTypeShort(agentType tmux.AgentType) string {
	switch agentType {
	case tmux.AgentClaude:
		return "cc"
	case tmux.AgentCodex:
		return "cod"
	case tmux.AgentGemini:
		return "gmi"
	case tmux.AgentUser:
		return "user"
	case tmux.AgentCursor:
		return "cursor"
	case tmux.AgentWindsurf:
		return "windsurf"
	case tmux.AgentAider:
		return "aider"
	default:
		if s := string(agentType); s != "" {
			return s
		}
		return "?"
	}
}

func getLastMeaningfulLine(lines []string) string {
	// Work backwards to find non-empty line
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" && !isOnlyWhitespaceOrControl(line) {
			// Truncate long lines
			if len(line) > 60 {
				return line[:57] + "..."
			}
			return line
		}
	}
	return "(empty)"
}

func isOnlyWhitespaceOrControl(s string) bool {
	for _, r := range s {
		if r > 32 && r != 127 { // Not whitespace or DEL
			return false
		}
	}
	return true
}

func detectPaneState(output, agentType string) string {
	// Use robot pattern library
	match := robot.MatchFirstPattern(output, agentType)
	if match != nil {
		return string(match.State)
	}
	return "UNKNOWN"
}

// Rate limit patterns
var rateLimitPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)rate\s*limit`),
	regexp.MustCompile(`(?i)you'?ve?\s+hit\s+(?:your\s+)?limit`),
	regexp.MustCompile(`(?i)usage\s+limit\s+reached`),
	regexp.MustCompile(`(?i)too\s+many\s+requests`),
	regexp.MustCompile(`(?i)quota\s+exceeded`),
	regexp.MustCompile(`\b429\b`),
	regexp.MustCompile(`(?i)please\s+try\s+again\s+(?:in|later)`),
}

func detectRateLimit(output string) bool {
	for _, pattern := range rateLimitPatterns {
		if pattern.MatchString(output) {
			return true
		}
	}
	return false
}

// Error detection patterns
var errorPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)error:\s*(.{10,50})`),
	regexp.MustCompile(`(?i)exception:\s*(.{10,50})`),
	regexp.MustCompile(`(?i)panic:\s*(.{10,50})`),
	regexp.MustCompile(`(?i)failed\s+to\s+(.{10,40})`),
	regexp.MustCompile(`SIGSEGV`),
	regexp.MustCompile(`(?i)connection\s+refused`),
	regexp.MustCompile(`(?i)unauthorized`),
	regexp.MustCompile(`(?i)authentication\s+failed`),
}

func detectErrors(output string) []string {
	var errors []string
	seen := make(map[string]bool)

	for _, pattern := range errorPatterns {
		matches := pattern.FindAllStringSubmatch(output, 2) // Max 2 matches per pattern
		for _, match := range matches {
			errMsg := strings.TrimSpace(match[0])
			if len(errMsg) > 50 {
				errMsg = errMsg[:47] + "..."
			}
			if !seen[errMsg] {
				seen[errMsg] = true
				errors = append(errors, errMsg)
			}
		}
	}

	// Limit to 3 errors max
	if len(errors) > 3 {
		errors = errors[:3]
	}

	return errors
}

func renderCompactTable(w io.Writer, statuses []sessionStatus) {
	fmt.Fprintln(w, "## NTM Swarm Status (Compact)")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "| Session | Ctrl | Workers | Errors |")
	fmt.Fprintln(w, "|---------|------|---------|--------|")

	for _, s := range statuses {
		ctrlState := "?"
		if s.Controller != nil {
			ctrlState = shortState(s.Controller.State)
			if s.Controller.RateLimit {
				ctrlState = "RATE"
			}
		}

		workerStates := []string{}
		for _, w := range s.Workers {
			ws := shortState(w.State)
			if w.RateLimit {
				ws = "RATE"
			}
			workerStates = append(workerStates, fmt.Sprintf("%s:%s", w.Type, ws))
		}
		workerStr := strings.Join(workerStates, " ")
		if workerStr == "" {
			workerStr = "-"
		}

		errStr := fmt.Sprintf("%d", s.ErrorCount)
		if s.RateLimits > 0 {
			errStr = fmt.Sprintf("%d (%d rate)", s.ErrorCount, s.RateLimits)
		}

		fmt.Fprintf(w, "| %s | %s | %s | %s |\n", s.Name, ctrlState, workerStr, errStr)
	}
}

func renderFullTable(w io.Writer, statuses []sessionStatus) {
	fmt.Fprintln(w, "## NTM Swarm Status")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "| Session | Pane | Type | State | Last Message | Issues |")
	fmt.Fprintln(w, "|---------|------|------|-------|--------------|--------|")

	for _, s := range statuses {
		// Controller row
		if s.Controller != nil {
			issues := formatIssues(s.Controller)
			lastMsg := escapeMarkdown(s.Controller.LastLine)
			fmt.Fprintf(w, "| %s | 1 (ctrl) | %s | %s | %s | %s |\n",
				s.Name, s.Controller.Type, s.Controller.State, lastMsg, issues)
		}

		// Worker rows
		for _, wrk := range s.Workers {
			issues := formatIssues(wrk)
			lastMsg := escapeMarkdown(wrk.LastLine)
			fmt.Fprintf(w, "| %s | %d | %s | %s | %s | %s |\n",
				s.Name, wrk.Index, wrk.Type, wrk.State, lastMsg, issues)
		}
	}

	// Summary
	totalSessions := len(statuses)
	totalErrors := 0
	totalRateLimits := 0
	for _, s := range statuses {
		totalErrors += s.ErrorCount
		totalRateLimits += s.RateLimits
	}

	fmt.Fprintln(w, "")
	fmt.Fprintf(w, "**Summary:** %d sessions, %d errors, %d rate-limited\n", totalSessions, totalErrors, totalRateLimits)
}

func shortState(state string) string {
	switch state {
	case "WAITING":
		return "WAIT"
	case "GENERATING":
		return "GEN"
	case "THINKING":
		return "THINK"
	case "ERROR":
		return "ERR"
	case "STALLED":
		return "STAL"
	default:
		return "?"
	}
}

func formatIssues(ps *paneStatus) string {
	issues := []string{}
	if ps.RateLimit {
		issues = append(issues, "RATE_LIMIT")
	}
	issues = append(issues, ps.Errors...)
	if len(issues) == 0 {
		return "-"
	}
	return strings.Join(issues, "; ")
}

func escapeMarkdown(s string) string {
	// Escape pipe characters for markdown tables
	s = strings.ReplaceAll(s, "|", "\\|")
	// Remove newlines
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}
