package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func newHistoryCmd() *cobra.Command {
	var (
		limit   int
		session string
		since   string
		search  string
		source  string
	)

	cmd := &cobra.Command{
		Use:   "history",
		Short: "View prompt history",
		Long: `View and manage prompt history from ntm send.

Examples:
  ntm history                          # Show recent prompts
  ntm history --session=myproject      # Filter by session
  ntm history --limit=50               # Show last 50
  ntm history --since=1h               # Last hour
  ntm history --search='auth'          # Search prompt text
  ntm history --json                   # Output as JSON
  ntm history show <id>                # Show entry details
  ntm history clear                    # Clear all history
  ntm history stats                    # Show statistics
  ntm history export history.jsonl     # Export to file`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHistoryList(limit, session, since, search, source)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "Number of entries to show")
	cmd.Flags().StringVarP(&session, "session", "s", "", "Filter by session name")
	cmd.Flags().StringVar(&since, "since", "", "Time filter (e.g., 1h, 1d, 1w)")
	cmd.Flags().StringVar(&search, "search", "", "Search prompt text")
	cmd.Flags().StringVar(&source, "source", "", "Filter by source (cli, palette, replay)")

	// Subcommands
	cmd.AddCommand(newHistoryShowCmd())
	cmd.AddCommand(newHistoryClearCmd())
	cmd.AddCommand(newHistoryStatsCmd())
	cmd.AddCommand(newHistoryExportCmd())
	cmd.AddCommand(newHistoryPruneCmd())

	return cmd
}

// HistoryListResult contains the history list output
type HistoryListResult struct {
	Entries    []history.HistoryEntry `json:"entries"`
	TotalCount int                    `json:"total_count"`
	Showing    int                    `json:"showing"`
}

// MarshalJSON ensures new fields remain stable for JSON consumers.
func (r *HistoryListResult) MarshalJSON() ([]byte, error) {
	// Alias to avoid recursion
	type Alias HistoryListResult
	return json.Marshal(&struct {
		*Alias
	}{Alias: (*Alias)(r)})
}

func (r *HistoryListResult) Text(w io.Writer) error {
	t := theme.Current()

	if len(r.Entries) == 0 {
		fmt.Fprintf(w, "%sNo history entries found%s\n", colorize(t.Warning), colorize(t.Text))
		return nil
	}

	// Print header
	fmt.Fprintf(w, " %s#%s  %sTIME%s        %sSESSION%s      %sTARGETS%s     %sDUR%s   %sPROMPT%s\n",
		colorize(t.Surface1), colorize(t.Text),
		colorize(t.Surface1), colorize(t.Text),
		colorize(t.Surface1), colorize(t.Text),
		colorize(t.Surface1), colorize(t.Text),
		colorize(t.Surface1), colorize(t.Text),
		colorize(t.Surface1), colorize(t.Text))

	// Print entries in reverse order (newest first)
	for i := len(r.Entries) - 1; i >= 0; i-- {
		e := r.Entries[i]
		num := len(r.Entries) - i

		// Format time
		timeStr := e.Timestamp.Local().Format("15:04:05")

		// Format targets
		targetsStr := formatTargets(e.Targets)

		// Truncate prompt
		prompt := truncate(strings.TrimSpace(e.Prompt), 38)

		// Session name (truncate if needed)
		sessionName := truncate(e.Session, 12)

		// Duration
		durStr := "--"
		if e.DurationMs > 0 {
			durStr = fmt.Sprintf("%4dms", e.DurationMs)
		}

		// Status indicator
		statusIcon := ""
		if !e.Success {
			statusIcon = " " + colorize(t.Red) + "✗" + colorize(t.Text)
		}

		fmt.Fprintf(w, " %s%2d%s  %s  %-12s %-11s %-6s %s%s\n",
			colorize(t.Blue), num, colorize(t.Text),
			timeStr,
			sessionName,
			targetsStr,
			durStr,
			prompt,
			statusIcon)
	}

	if r.TotalCount > r.Showing {
		fmt.Fprintf(w, "\n%sShowing %d of %d entries. Use --limit for more.%s\n",
			colorize(t.Surface1), r.Showing, r.TotalCount, colorize(t.Text))
	}

	return nil
}

func (r *HistoryListResult) JSON() interface{} {
	entries := make([]HistoryListEntry, 0, len(r.Entries))
	for _, e := range r.Entries {
		entries = append(entries, HistoryListEntry{
			ID:         e.ID,
			Timestamp:  e.Timestamp,
			Session:    e.Session,
			Targets:    e.Targets,
			Prompt:     e.Prompt,
			Source:     string(e.Source),
			Template:   e.Template,
			Success:    e.Success,
			Error:      e.Error,
			DurationMs: e.DurationMs,
		})
	}
	return struct {
		Entries    []HistoryListEntry `json:"entries"`
		TotalCount int                `json:"total_count"`
		Showing    int                `json:"showing"`
	}{
		Entries:    entries,
		TotalCount: r.TotalCount,
		Showing:    r.Showing,
	}
}

// HistoryListEntry is a pared-down view for JSON output to keep field names stable.
type HistoryListEntry struct {
	ID         string    `json:"id"`
	Timestamp  time.Time `json:"ts"`
	Session    string    `json:"session"`
	Targets    []string  `json:"targets"`
	Prompt     string    `json:"prompt"`
	Source     string    `json:"source"`
	Template   string    `json:"template,omitempty"`
	Success    bool      `json:"success"`
	Error      string    `json:"error,omitempty"`
	DurationMs int       `json:"duration_ms,omitempty"`
}

func runHistoryList(limit int, session, since, search, source string) error {
	if limit <= 0 {
		return fmt.Errorf("--limit must be greater than 0")
	}

	var entries []history.HistoryEntry
	var err error

	// Get entries based on filters
	if session != "" {
		entries, err = history.ReadForSession(session)
	} else if search != "" {
		entries, err = history.Search(search)
	} else if since == "" && source == "" {
		entries, err = history.ReadRecent(limit)
	} else {
		entries, err = history.ReadAll()
	}
	if err != nil {
		return err
	}

	// Apply time filter
	if since != "" {
		duration, parseErr := parseDuration(since)
		if parseErr != nil {
			return fmt.Errorf("invalid --since value: %w", parseErr)
		}
		cutoff := time.Now().Add(-duration)
		var filtered []history.HistoryEntry
		for _, e := range entries {
			if e.Timestamp.After(cutoff) {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}

	// Apply source filter
	if source != "" {
		var filtered []history.HistoryEntry
		for _, e := range entries {
			if string(e.Source) == source {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}

	totalCount := len(entries)

	// Apply limit (take last N)
	showing := len(entries)
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
		showing = limit
	}

	result := &HistoryListResult{
		Entries:    entries,
		TotalCount: totalCount,
		Showing:    showing,
	}

	formatter := output.New(output.WithJSON(jsonOutput))
	return formatter.Output(result)
}

func newHistoryShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id-or-index>",
		Short: "Show details of a history entry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHistoryShow(args[0])
		},
	}
}

// HistoryShowResult contains a single entry's details
type HistoryShowResult struct {
	Entry *history.HistoryEntry `json:"entry"`
}

func (r *HistoryShowResult) Text(w io.Writer) error {
	t := theme.Current()
	e := r.Entry

	fmt.Fprintf(w, "%sID:%s       %s\n", colorize(t.Blue), colorize(t.Text), e.ID)
	fmt.Fprintf(w, "%sTime:%s     %s\n", colorize(t.Blue), colorize(t.Text), e.Timestamp.Local().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(w, "%sSession:%s  %s\n", colorize(t.Blue), colorize(t.Text), e.Session)
	fmt.Fprintf(w, "%sTargets:%s  %s\n", colorize(t.Blue), colorize(t.Text), strings.Join(e.Targets, ", "))
	fmt.Fprintf(w, "%sSource:%s   %s\n", colorize(t.Blue), colorize(t.Text), e.Source)
	if e.Template != "" {
		fmt.Fprintf(w, "%sTemplate:%s %s\n", colorize(t.Blue), colorize(t.Text), e.Template)
	}

	statusColor := t.Success
	statusText := "Success"
	if !e.Success {
		statusColor = t.Red
		statusText = "Failed"
		if e.Error != "" {
			statusText += ": " + e.Error
		}
	}
	fmt.Fprintf(w, "%sStatus:%s   %s%s%s\n", colorize(t.Blue), colorize(t.Text), colorize(statusColor), statusText, colorize(t.Text))

	fmt.Fprintf(w, "\n%sPrompt:%s\n", colorize(t.Blue), colorize(t.Text))
	fmt.Fprintf(w, "  %s\n", strings.ReplaceAll(e.Prompt, "\n", "\n  "))

	return nil
}

func (r *HistoryShowResult) JSON() interface{} {
	return r.Entry
}

func runHistoryShow(idOrIndex string) error {
	entries, err := history.ReadAll()
	if err != nil {
		return err
	}

	if len(entries) == 0 {
		return fmt.Errorf("no history entries found")
	}

	var entry *history.HistoryEntry

	// Try to parse as index first (1-based, from end)
	if idx, err := strconv.Atoi(idOrIndex); err == nil {
		// Index from the end (1 = most recent)
		if idx > 0 && idx <= len(entries) {
			entry = &entries[len(entries)-idx]
		} else {
			return fmt.Errorf("index %d out of range (1-%d)", idx, len(entries))
		}
	} else {
		// Try to find by ID prefix
		for i := range entries {
			if strings.HasPrefix(entries[i].ID, idOrIndex) {
				entry = &entries[i]
				break
			}
		}
		if entry == nil {
			return fmt.Errorf("entry not found: %s", idOrIndex)
		}
	}

	result := &HistoryShowResult{Entry: entry}
	formatter := output.New(output.WithJSON(jsonOutput))
	return formatter.Output(result)
}

func newHistoryClearCmd() *cobra.Command {
	var force bool
	var before string

	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Clear prompt history",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHistoryClear(force, before)
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")
	cmd.Flags().StringVar(&before, "before", "", "Clear entries older than duration (e.g., 7d)")

	return cmd
}

func runHistoryClear(force bool, before string) error {
	t := theme.Current()

	if before != "" {
		// Prune old entries
		duration, err := parseDuration(before)
		if err != nil {
			return fmt.Errorf("invalid --before value: %w", err)
		}

		entries, err := history.ReadAll()
		if err != nil {
			return err
		}

		cutoff := time.Now().Add(-duration)
		var toKeep []history.HistoryEntry
		for _, e := range entries {
			if e.Timestamp.After(cutoff) {
				toKeep = append(toKeep, e)
			}
		}

		removed := len(entries) - len(toKeep)
		if removed == 0 {
			fmt.Printf("%sNo entries older than %s%s\n", colorize(t.Warning), before, colorize(t.Text))
			return nil
		}

		if !force {
			fmt.Printf("This will remove %d entries older than %s. Continue? [y/N]: ", removed, before)
			var response string
			fmt.Scanln(&response)
			if response != "y" && response != "Y" {
				fmt.Println("Aborted.")
				return nil
			}
		}

		// Rewrite history efficiently
		if _, err := history.PruneByTime(cutoff); err != nil {
			return err
		}

		fmt.Printf("%s✓%s Removed %d entries\n", colorize(t.Success), colorize(t.Text), removed)
		return nil
	}

	// Clear all
	count, err := history.Count()
	if err != nil {
		return err
	}

	if count == 0 {
		fmt.Printf("%sNo history to clear%s\n", colorize(t.Warning), colorize(t.Text))
		return nil
	}

	if !force {
		fmt.Printf("This will remove all %d history entries. Continue? [y/N]: ", count)
		var response string
		fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	if err := history.Clear(); err != nil {
		return err
	}

	fmt.Printf("%s✓%s Cleared %d entries\n", colorize(t.Success), colorize(t.Text), count)
	return nil
}

func newHistoryStatsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Show history statistics",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHistoryStats()
		},
	}
}

func runHistoryStats() error {
	stats, err := history.GetStats()
	if err != nil {
		return err
	}

	if jsonOutput {
		return output.PrintJSON(stats)
	}

	t := theme.Current()
	fmt.Printf("%sHistory Statistics%s\n", colorize(t.Blue), colorize(t.Text))
	fmt.Printf("─────────────────────────\n")
	fmt.Printf("Total entries:    %d\n", stats.TotalEntries)
	fmt.Printf("Successful:       %s%d%s\n", colorize(t.Success), stats.SuccessCount, colorize(t.Text))
	fmt.Printf("Failed:           %s%d%s\n", colorize(t.Red), stats.FailureCount, colorize(t.Text))
	fmt.Printf("Unique sessions:  %d\n", stats.UniqueSessions)
	fmt.Printf("File size:        %s\n", formatBytes(stats.FileSizeBytes))
	fmt.Printf("File location:    %s\n", history.StoragePath())

	return nil
}

func newHistoryExportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "export <file>",
		Short: "Export history to a file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHistoryExport(args[0])
		},
	}
}

func runHistoryExport(path string) error {
	t := theme.Current()

	if err := history.ExportTo(path); err != nil {
		return err
	}

	count, _ := history.Count()
	fmt.Printf("%s✓%s Exported %d entries to %s\n", colorize(t.Success), colorize(t.Text), count, path)
	return nil
}

func newHistoryPruneCmd() *cobra.Command {
	var keep int

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Prune old history entries",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHistoryPrune(keep)
		},
	}

	cmd.Flags().IntVar(&keep, "keep", 10000, "Number of recent entries to keep")

	return cmd
}

func runHistoryPrune(keep int) error {
	t := theme.Current()

	removed, err := history.Prune(keep)
	if err != nil {
		return err
	}

	if removed == 0 {
		fmt.Printf("%sNothing to prune (<%d entries)%s\n", colorize(t.Warning), keep, colorize(t.Text))
		return nil
	}

	fmt.Printf("%s✓%s Pruned %d entries, kept %d\n", colorize(t.Success), colorize(t.Text), removed, keep)
	return nil
}

// Helper functions

func formatTargets(targets []string) string {
	if len(targets) == 0 {
		return "(none)"
	}
	if len(targets) == 1 {
		return targets[0]
	}
	if len(targets) <= 3 {
		return strings.Join(targets, ",")
	}
	return fmt.Sprintf("all (%d)", len(targets))
}

func truncate(s string, maxLen int) string {
	// Remove newlines for display
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")

	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func parseDuration(s string) (time.Duration, error) {
	// Support human-friendly formats: 1h, 1d, 1w
	if len(s) < 2 {
		return 0, fmt.Errorf("invalid duration: %s", s)
	}

	unit := s[len(s)-1]
	value, err := strconv.Atoi(s[:len(s)-1])
	if err != nil {
		return 0, err
	}

	switch unit {
	case 's':
		return time.Duration(value) * time.Second, nil
	case 'm':
		return time.Duration(value) * time.Minute, nil
	case 'h':
		return time.Duration(value) * time.Hour, nil
	case 'd':
		return time.Duration(value) * 24 * time.Hour, nil
	case 'w':
		return time.Duration(value) * 7 * 24 * time.Hour, nil
	default:
		// Try standard Go duration
		return time.ParseDuration(s)
	}
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
