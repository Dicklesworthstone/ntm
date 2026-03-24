package robot

import (
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

// HistoryOptions configures history queries
type HistoryOptions struct {
	Session   string // tmux session name
	Pane      string // filter by pane ID
	AgentType string // filter by agent type
	Last      int    // last N entries
	Since     string // time-based filter (e.g., "1h", "30m", "2025-12-15")
	Stats     bool   // show statistics instead of entries
	Limit     int    // pagination limit
	Offset    int    // pagination offset
}

// HistoryOutput is the structured output for --robot-history
type HistoryOutput struct {
	RobotResponse
	Session     string                 `json:"session"`
	GeneratedAt time.Time              `json:"generated_at"`
	Entries     []history.HistoryEntry `json:"entries,omitempty"`
	Stats       *history.Stats         `json:"stats,omitempty"`
	Total       int                    `json:"total"`
	Filtered    int                    `json:"filtered"`
	Pagination  *PaginationInfo        `json:"pagination,omitempty"`
	AgentHints  *HistoryAgentHints     `json:"_agent_hints,omitempty"`
}

// HistoryAgentHints provides actionable suggestions for AI agents
type HistoryAgentHints struct {
	Summary           string   `json:"summary,omitempty"`
	SuggestedCommands []string `json:"suggested_commands,omitempty"`
	Warnings          []string `json:"warnings,omitempty"`
	NextOffset        *int     `json:"next_offset,omitempty"`
	PagesRemaining    *int     `json:"pages_remaining,omitempty"`
}

// GetHistory returns command history as structured output.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetHistory(opts HistoryOptions) (*HistoryOutput, error) {
	output := &HistoryOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		GeneratedAt:   time.Now().UTC(),
		Entries:       []history.HistoryEntry{},
	}

	if opts.Session == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session name is required"),
			ErrCodeInvalidFlag,
			"Provide session name: ntm --robot-history=myproject",
		)
		return output, nil
	}

	// Verify session exists
	if !tmux.SessionExists(opts.Session) {
		// Session doesn't exist, but we might still have history
		// history.Exists() checks for global history file
		if !history.Exists() {
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("session '%s' not found and no history exists", opts.Session),
				ErrCodeSessionNotFound,
				"Use 'ntm list' to see available sessions",
			)
			return output, nil
		}
	}

	// Get entries for the session
	entries, err := history.ReadForSession(opts.Session)

	if err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to read history: %w", err),
			ErrCodeInternalError,
			"Check permissions on history file",
		)
		return output, nil
	}

	output.Total = len(entries)

	// Filter entries
	var filtered []history.HistoryEntry
	var sinceTime time.Time
	normalizedAgentType := normalizeAgentType(opts.AgentType)
	livePaneTypes := make(map[string]string)

	if opts.Since != "" {
		var parseErr error
		sinceTime, parseErr = parseSinceTime(opts.Since)
		if parseErr != nil {
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("invalid --since value: %w", parseErr),
				ErrCodeInvalidFlag,
				"Use duration (1h, 30m, 2d) or ISO8601 date",
			)
			return output, nil
		}
	}

	if normalizedAgentType != "" && tmux.SessionExists(opts.Session) {
		if panes, err := tmux.GetPanes(opts.Session); err == nil {
			for _, pane := range panes {
				livePaneTypes[fmt.Sprintf("%d", pane.Index)] = normalizeAgentType(pane.Type.String())
			}
		}
	}

	for _, e := range entries {
		// Filter by time
		if !sinceTime.IsZero() && e.Timestamp.Before(sinceTime) {
			continue
		}

		// Filter by pane/targets
		if opts.Pane != "" {
			// e.Targets contains pane indices or names
			found := false
			for _, t := range e.Targets {
				if t == opts.Pane {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		if normalizedAgentType != "" && !historyEntryMatchesAgentType(e, normalizedAgentType, livePaneTypes) {
			continue
		}

		filtered = append(filtered, e)
	}

	// Apply limit (Last N)
	if opts.Last > 0 && len(filtered) > opts.Last {
		filtered = filtered[len(filtered)-opts.Last:]
	}

	output.Filtered = len(filtered)
	if opts.Stats {
		output.Stats = buildHistoryStats(filtered)
		output.AgentHints = generateHistoryHints(*output, opts)
		return output, nil
	}
	if paged, page := ApplyPagination(filtered, PaginationOptions{Limit: opts.Limit, Offset: opts.Offset}); page != nil {
		output.Entries = paged
		output.Pagination = page
	} else {
		output.Entries = filtered
	}
	output.AgentHints = generateHistoryHints(*output, opts)

	return output, nil
}

func buildHistoryStats(entries []history.HistoryEntry) *history.Stats {
	stats := &history.Stats{
		TotalEntries: len(entries),
	}
	if len(entries) > 0 {
		stats.UniqueSessions = 1
	}
	for _, entry := range entries {
		if entry.Success {
			stats.SuccessCount++
		} else {
			stats.FailureCount++
		}
	}
	return stats
}

func historyEntryMatchesAgentType(entry history.HistoryEntry, want string, livePaneTypes map[string]string) bool {
	for _, agentType := range entry.AgentTypes {
		if normalizeAgentType(agentType) == want {
			return true
		}
	}
	for _, target := range entry.Targets {
		if livePaneTypes[target] == want {
			return true
		}
	}
	return false
}

// PrintHistory outputs command history as JSON.
// This is a thin wrapper around GetHistory() for CLI output.
func PrintHistory(opts HistoryOptions) error {
	output, err := GetHistory(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// parseSinceTime parses various time formats
func parseSinceTime(s string) (time.Time, error) {
	// Try duration format first (e.g., "1h", "30m", "2d")
	if dur, err := util.ParseDuration(s); err == nil {
		return time.Now().Add(-dur), nil
	}

	// Try RFC3339
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}

	// Try date only
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}

	// Try relative formats
	s = strings.ToLower(strings.TrimSpace(s))
	if strings.HasSuffix(s, " ago") {
		s = strings.TrimSuffix(s, " ago")
		if dur, err := util.ParseDuration(s); err == nil {
			return time.Now().Add(-dur), nil
		}
	}

	return time.Time{}, fmt.Errorf("unrecognized time format: %s", s)
}

// generateHistoryHints creates actionable hints for AI agents
func generateHistoryHints(output HistoryOutput, opts HistoryOptions) *HistoryAgentHints {
	hints := &HistoryAgentHints{}

	// Build summary
	if output.Stats != nil {
		s := output.Stats
		hints.Summary = fmt.Sprintf("%d total commands", s.TotalEntries)
	} else if output.Total == 0 {
		hints.Summary = "No command history for this session"
	} else {
		shown := output.Filtered
		totalVisible := output.Total
		if output.Pagination != nil {
			shown = output.Pagination.Count
			totalVisible = output.Filtered
		}
		hints.Summary = fmt.Sprintf("Showing %d of %d commands", shown, totalVisible)
		if output.Pagination != nil && output.Filtered != output.Total {
			hints.Summary += fmt.Sprintf(" (from %d total)", output.Total)
		}
		if opts.Pane != "" {
			hints.Summary += fmt.Sprintf(" (pane %s)", opts.Pane)
		}
	}

	// Suggested commands
	hints.SuggestedCommands = []string{
		fmt.Sprintf("ntm --robot-history=%s --stats", opts.Session),
		fmt.Sprintf("ntm --robot-history=%s --last=10", opts.Session),
		fmt.Sprintf("ntm --robot-history=%s --since=1h", opts.Session),
	}

	if next, pages := paginationHintOffsets(output.Pagination); next != nil {
		hints.NextOffset = next
		hints.PagesRemaining = pages
	}

	if output.Total > 1000 {
		hints.Warnings = append(hints.Warnings,
			"Large history - consider using --prune or filtering")
	}

	return hints
}
