// Package robot provides machine-readable output for AI agents.
// logs.go contains the --robot-logs flag implementation.
package robot

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// LogsOptions configures the robot-logs operation.
type LogsOptions struct {
	Session string        // Session name
	Since   time.Duration // Only show logs since this duration ago
	Panes   []int         // Filter to specific pane indices (empty = all)
	Limit   int           // Max lines per pane (default: 100)
	Filter  string        // Regex filter pattern
}

// LogEntry represents a single log line from a pane.
type LogEntry struct {
	Pane      int    `json:"pane"`
	AgentType string `json:"agent_type"`
	Line      string `json:"line"`
	LineNum   int    `json:"line_num"`
}

// PaneLogs contains logs for a single pane.
type PaneLogs struct {
	Pane       int       `json:"pane"`
	AgentType  string    `json:"agent_type"`
	Lines      []string  `json:"lines"`
	LineCount  int       `json:"line_count"`
	Truncated  bool      `json:"truncated"`
	CapturedAt time.Time `json:"captured_at"`
}

// LogsOutput is the response format for --robot-logs=SESSION.
type LogsOutput struct {
	RobotResponse
	Session    string      `json:"session"`
	CapturedAt time.Time   `json:"captured_at"`
	Panes      []PaneLogs  `json:"panes"`
	Summary    LogsSummary `json:"summary"`
}

// LogsSummary contains aggregate log statistics.
type LogsSummary struct {
	TotalPanes     int `json:"total_panes"`
	TotalLines     int `json:"total_lines"`
	TruncatedPanes int `json:"truncated_panes"`
	FilteredLines  int `json:"filtered_lines,omitempty"`
}

// DefaultLogsLimit is the default max lines per pane.
const DefaultLogsLimit = 100

// GetLogs captures logs from all agent panes in a session.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetLogs(opts LogsOptions) (*LogsOutput, error) {
	output := &LogsOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		CapturedAt:    time.Now().UTC(),
		Panes:         []PaneLogs{},
		Summary:       LogsSummary{},
	}

	// Set default limit
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultLogsLimit
	}

	// Check if session exists
	if !tmux.SessionExists(opts.Session) {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session '%s' not found", opts.Session),
			ErrCodeSessionNotFound,
			"Use --robot-status to list available sessions",
		)
		return output, nil
	}

	// Get panes in the session
	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			err,
			ErrCodeInternalError,
			"Check tmux session state",
		)
		return output, nil
	}

	// Build pane filter set
	paneFilter := make(map[int]bool)
	for _, p := range opts.Panes {
		paneFilter[p] = true
	}

	// Compile filter regex if provided
	var filterRe *regexp.Regexp
	if opts.Filter != "" {
		var err error
		filterRe, err = regexp.Compile(opts.Filter)
		if err != nil {
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("invalid filter regex: %w", err),
				ErrCodeInvalidFlag,
				"Check regex syntax",
			)
			return output, nil
		}
	}

	// Capture logs from each pane
	for _, pane := range panes {
		// Apply pane filter if specified
		if len(paneFilter) > 0 && !paneFilter[pane.Index] {
			continue
		}

		agentType := detectAgentTypeFromPane(pane)
		if agentType == "user" {
			continue // Skip user panes by default
		}

		paneLogs := capturePaneLogs(pane, agentType, limit, filterRe)
		output.Panes = append(output.Panes, paneLogs)

		// Update summary
		output.Summary.TotalPanes++
		output.Summary.TotalLines += paneLogs.LineCount
		if paneLogs.Truncated {
			output.Summary.TruncatedPanes++
		}
	}

	return output, nil
}

// capturePaneLogs captures and formats logs from a single pane.
func capturePaneLogs(pane tmux.Pane, agentType string, limit int, filterRe *regexp.Regexp) PaneLogs {
	logs := PaneLogs{
		Pane:       pane.Index,
		AgentType:  agentType,
		Lines:      []string{},
		CapturedAt: time.Now().UTC(),
	}

	// Capture pane output
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Capture more lines than limit to account for filtering
	captureLines := limit * 2
	if captureLines < 200 {
		captureLines = 200
	}

	output, err := tmux.CapturePaneOutputContext(ctx, pane.ID, captureLines)
	if err != nil {
		logs.Lines = []string{fmt.Sprintf("[error capturing pane output: %v]", err)}
		logs.LineCount = 1
		return logs
	}

	// Strip ANSI escape codes
	output = stripANSI(output)

	// Split into lines
	lines := strings.Split(output, "\n")

	// Apply filter if provided
	if filterRe != nil {
		var filtered []string
		for _, line := range lines {
			if filterRe.MatchString(line) {
				filtered = append(filtered, line)
			}
		}
		lines = filtered
	}

	// Trim empty lines from end
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}

	// Apply limit
	if len(lines) > limit {
		logs.Truncated = true
		lines = lines[len(lines)-limit:] // Keep most recent
	}

	logs.Lines = lines
	logs.LineCount = len(lines)

	return logs
}

// PrintLogs outputs logs from all agent panes in a session.
// This is a thin wrapper around GetLogs() for CLI output.
func PrintLogs(opts LogsOptions) error {
	output, err := GetLogs(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// AggregatedLogEntry represents a log entry with timing for aggregated view.
type AggregatedLogEntry struct {
	Pane      int       `json:"pane"`
	AgentType string    `json:"agent_type"`
	Line      string    `json:"line"`
	Timestamp time.Time `json:"timestamp,omitempty"`
}

// AggregatedLogsOutput is the response format for aggregated logs view.
type AggregatedLogsOutput struct {
	RobotResponse
	Session    string               `json:"session"`
	CapturedAt time.Time            `json:"captured_at"`
	Entries    []AggregatedLogEntry `json:"entries"`
	Summary    LogsSummary          `json:"summary"`
}

// GetAggregatedLogs captures and interleaves logs from all panes.
// Lines are prefixed with [agent_type:pane] for identification.
func GetAggregatedLogs(opts LogsOptions) (*AggregatedLogsOutput, error) {
	output := &AggregatedLogsOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		CapturedAt:    time.Now().UTC(),
		Entries:       []AggregatedLogEntry{},
		Summary:       LogsSummary{},
	}

	// Get logs per pane first
	logsOutput, err := GetLogs(opts)
	if err != nil {
		return nil, err
	}

	if !logsOutput.Success {
		output.RobotResponse = logsOutput.RobotResponse
		return output, nil
	}

	// Aggregate all entries
	var entries []AggregatedLogEntry
	for _, paneLogs := range logsOutput.Panes {
		for _, line := range paneLogs.Lines {
			if strings.TrimSpace(line) == "" {
				continue
			}
			entries = append(entries, AggregatedLogEntry{
				Pane:      paneLogs.Pane,
				AgentType: paneLogs.AgentType,
				Line:      line,
				Timestamp: paneLogs.CapturedAt,
			})
		}
	}

	output.Entries = entries
	output.Summary = logsOutput.Summary
	output.Summary.TotalLines = len(entries)

	return output, nil
}

// FormatAggregatedLog formats a log entry with pane prefix for CLI display.
// Format: "[cc:2] line content..."
func FormatAggregatedLog(entry AggregatedLogEntry) string {
	return fmt.Sprintf("[%s:%d] %s", shortAgentType(entry.AgentType), entry.Pane, entry.Line)
}

// shortAgentType returns a short agent type identifier.
func shortAgentType(agentType string) string {
	switch normalizeAgentType(agentType) {
	case "claude":
		return "cc"
	case "codex":
		return "cod"
	case "gemini":
		return "gmi"
	case "cursor":
		return "cur"
	case "windsurf":
		return "ws"
	case "aider":
		return "aid"
	case "ollama":
		return "oll"
	case "user":
		return "usr"
	default:
		agentType = normalizeAgentType(agentType)
		if len(agentType) > 3 {
			return agentType[:3]
		}
		return agentType
	}
}

// StreamLogsOptions configures the logs streaming operation.
type StreamLogsOptions struct {
	Session  string
	Panes    []int
	Interval time.Duration
	Filter   string
}

// LogsStreamEntry represents a streaming log update.
type LogsStreamEntry struct {
	Pane      int       `json:"pane"`
	AgentType string    `json:"agent_type"`
	NewLines  []string  `json:"new_lines"`
	Timestamp time.Time `json:"timestamp"`
}

// LogsStreamer provides streaming log updates.
type LogsStreamer struct {
	opts        StreamLogsOptions
	lastCapture map[int]int // pane index -> last line count
	filterRe    *regexp.Regexp
}

// NewLogsStreamer creates a new logs streamer.
func NewLogsStreamer(opts StreamLogsOptions) (*LogsStreamer, error) {
	ls := &LogsStreamer{
		opts:        opts,
		lastCapture: make(map[int]int),
	}

	if opts.Filter != "" {
		var err error
		ls.filterRe, err = regexp.Compile(opts.Filter)
		if err != nil {
			return nil, fmt.Errorf("invalid filter regex: %w", err)
		}
	}

	return ls, nil
}

// Poll captures and returns only new lines since last capture.
func (ls *LogsStreamer) Poll(ctx context.Context) ([]LogsStreamEntry, error) {
	var entries []LogsStreamEntry

	// Get panes
	panes, err := tmux.GetPanesContext(ctx, ls.opts.Session)
	if err != nil {
		return nil, err
	}

	// Build pane filter
	paneFilter := make(map[int]bool)
	for _, p := range ls.opts.Panes {
		paneFilter[p] = true
	}

	for _, pane := range panes {
		// Apply pane filter
		if len(paneFilter) > 0 && !paneFilter[pane.Index] {
			continue
		}

		agentType := detectAgentTypeFromPane(pane)
		if agentType == "user" {
			continue
		}

		// Capture output
		output, err := tmux.CapturePaneOutputContext(ctx, pane.ID, 200)
		if err != nil {
			continue
		}

		output = stripANSI(output)
		lines := strings.Split(output, "\n")

		// Apply filter
		if ls.filterRe != nil {
			var filtered []string
			for _, line := range lines {
				if ls.filterRe.MatchString(line) {
					filtered = append(filtered, line)
				}
			}
			lines = filtered
		}

		// Trim empty lines from end
		for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
			lines = lines[:len(lines)-1]
		}

		// Get new lines since last capture
		lastCount := ls.lastCapture[pane.Index]
		if len(lines) > lastCount {
			newLines := lines[lastCount:]
			// Filter out empty lines
			var nonEmpty []string
			for _, line := range newLines {
				if strings.TrimSpace(line) != "" {
					nonEmpty = append(nonEmpty, line)
				}
			}
			if len(nonEmpty) > 0 {
				entries = append(entries, LogsStreamEntry{
					Pane:      pane.Index,
					AgentType: agentType,
					NewLines:  nonEmpty,
					Timestamp: time.Now().UTC(),
				})
			}
		}

		ls.lastCapture[pane.Index] = len(lines)
	}

	// Sort by timestamp
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Timestamp.Before(entries[j].Timestamp)
	})

	return entries, nil
}

// Reset clears the streamer's state.
func (ls *LogsStreamer) Reset() {
	ls.lastCapture = make(map[int]int)
}
