package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func newLogsCmd() *cobra.Command {
	var since string
	var panesArg string
	var follow bool
	var filter string
	var limit int
	var aggregate bool

	cmd := &cobra.Command{
		Use:   "logs <session>",
		Short: "Aggregate and filter logs from all agents",
		Long: `View aggregated logs from all agent panes in a session.

By default, shows recent logs from all agent panes. Use flags to filter
and customize the output.

Examples:
  ntm logs myproject                    # All agent logs
  ntm logs myproject --since=5m         # Logs from last 5 minutes
  ntm logs myproject --panes=1,2        # Only panes 1 and 2
  ntm logs myproject --follow           # Stream new logs
  ntm logs myproject --filter="error"   # Filter by regex pattern
  ntm logs myproject --aggregate        # Interleaved view with prefixes
  ntm logs myproject --json             # JSON output

Output Format (aggregated):
  [cc:2] 10:30:15 Starting implementation...
  [cod:3] 10:30:16 Running tests...
  [gmi:4] 10:30:17 Analyzing code...

The prefix shows agent type and pane index.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := args[0]
			if !IsJSONOutput() {
				res, err := ResolveSession(session, cmd.OutOrStdout())
				if err != nil {
					return err
				}
				session = res.Session
			}

			// Parse panes argument
			var panes []int
			if panesArg != "" {
				var err error
				panes, err = robot.ParsePanesArg(panesArg)
				if err != nil {
					return err
				}
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

			opts := robot.LogsOptions{
				Session: session,
				Since:   sinceDuration,
				Panes:   panes,
				Limit:   limit,
				Filter:  filter,
			}

			// Handle follow mode
			if follow {
				return runLogsFollow(opts)
			}

			// Handle JSON output
			if IsJSONOutput() {
				if aggregate {
					result, err := robot.GetAggregatedLogs(opts)
					if err != nil {
						return output.PrintJSON(output.NewError(err.Error()))
					}
					return output.PrintJSON(result)
				}
				return robot.PrintLogs(opts)
			}

			// Human-readable output
			if aggregate {
				return runLogsAggregated(opts)
			}
			return runLogsPanes(opts)
		},
	}

	cmd.Flags().StringVar(&since, "since", "", "Show logs since duration (e.g., 5m, 1h)")
	cmd.Flags().StringVar(&panesArg, "panes", "", "Filter to specific pane indices (comma-separated)")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Stream new logs continuously")
	cmd.Flags().StringVar(&filter, "filter", "", "Filter lines by regex pattern")
	cmd.Flags().IntVarP(&limit, "limit", "n", 100, "Max lines per pane")
	cmd.Flags().BoolVarP(&aggregate, "aggregate", "a", false, "Show interleaved aggregated view")

	cmd.ValidArgsFunction = completeSessionArgs

	return cmd
}

// runLogsPanes displays logs grouped by pane (default view).
func runLogsPanes(opts robot.LogsOptions) error {
	logsOutput, err := robot.GetLogs(opts)
	if err != nil {
		return err
	}

	if !logsOutput.Success {
		return fmt.Errorf("%s", logsOutput.Error)
	}

	th := theme.Current()

	// Style helpers
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(th.Primary)
	paneStyle := lipgloss.NewStyle().Bold(true).Foreground(th.Secondary)
	dimStyle := lipgloss.NewStyle().Foreground(th.Subtext)

	// Print header
	fmt.Println(headerStyle.Render(fmt.Sprintf("Logs from session: %s", logsOutput.Session)))
	fmt.Println(dimStyle.Render(fmt.Sprintf("Captured at: %s", logsOutput.CapturedAt.Format(time.RFC3339))))
	fmt.Println()

	// Print logs for each pane
	for _, paneLogs := range logsOutput.Panes {
		// Pane header
		paneHeader := fmt.Sprintf("=== Pane %d (%s) ===", paneLogs.Pane, paneLogs.AgentType)
		if paneLogs.Truncated {
			paneHeader += " [truncated]"
		}
		fmt.Println(paneStyle.Render(paneHeader))

		// Print lines
		if len(paneLogs.Lines) == 0 {
			fmt.Println(dimStyle.Render("  (no output)"))
		} else {
			for _, line := range paneLogs.Lines {
				fmt.Println("  " + line)
			}
		}
		fmt.Println()
	}

	// Summary
	fmt.Println(dimStyle.Render(fmt.Sprintf(
		"Total: %d panes, %d lines%s",
		logsOutput.Summary.TotalPanes,
		logsOutput.Summary.TotalLines,
		func() string {
			if logsOutput.Summary.TruncatedPanes > 0 {
				return fmt.Sprintf(" (%d truncated)", logsOutput.Summary.TruncatedPanes)
			}
			return ""
		}(),
	)))

	return nil
}

// runLogsAggregated displays interleaved logs with pane prefixes.
func runLogsAggregated(opts robot.LogsOptions) error {
	logsOutput, err := robot.GetAggregatedLogs(opts)
	if err != nil {
		return err
	}

	if !logsOutput.Success {
		return fmt.Errorf("%s", logsOutput.Error)
	}

	th := theme.Current()

	// Color map for different agent types
	agentColors := map[string]lipgloss.Color{
		"claude": th.Pink,
		"cc":     th.Pink,
		"codex":  th.Green,
		"cod":    th.Green,
		"gemini": th.Blue,
		"gmi":    th.Blue,
	}

	for _, entry := range logsOutput.Entries {
		// Get color for this agent type
		color := th.Text
		if c, ok := agentColors[entry.AgentType]; ok {
			color = c
		}

		// Format: [cc:2] line content
		prefix := lipgloss.NewStyle().
			Foreground(color).
			Bold(true).
			Render(fmt.Sprintf("[%s:%d]", robot.FormatAggregatedLog(entry)[:3], entry.Pane))

		fmt.Printf("%s %s\n", prefix, entry.Line)
	}

	return nil
}

// runLogsFollow streams logs continuously.
func runLogsFollow(opts robot.LogsOptions) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	if !tmux.SessionExists(opts.Session) {
		return fmt.Errorf("session '%s' not found", opts.Session)
	}

	// Create streamer
	streamOpts := robot.StreamLogsOptions{
		Session:  opts.Session,
		Panes:    opts.Panes,
		Interval: time.Second,
		Filter:   opts.Filter,
	}
	streamer, err := robot.NewLogsStreamer(streamOpts)
	if err != nil {
		return err
	}

	th := theme.Current()

	// Color map for different agent types
	agentColors := map[string]lipgloss.Color{
		"claude": th.Pink,
		"cc":     th.Pink,
		"codex":  th.Green,
		"cod":    th.Green,
		"gemini": th.Blue,
		"gmi":    th.Blue,
	}

	// Set up signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()
	defer signal.Stop(sigCh)

	// Print header
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(th.Primary)
	dimStyle := lipgloss.NewStyle().Foreground(th.Subtext)

	if !IsJSONOutput() {
		fmt.Println(headerStyle.Render(fmt.Sprintf("Following logs from session: %s", opts.Session)))
		fmt.Println(dimStyle.Render("Press Ctrl+C to stop"))
		fmt.Println()
	}

	// Do initial capture to establish baseline
	_, _ = streamer.Poll(ctx)

	// Stream loop
	ticker := time.NewTicker(streamOpts.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			if !IsJSONOutput() {
				fmt.Println()
				fmt.Println(dimStyle.Render("Stopped."))
			}
			return nil
		case <-ticker.C:
			entries, err := streamer.Poll(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil // Context cancelled, not an error
				}
				continue // Ignore transient errors
			}

			for _, entry := range entries {
				for _, line := range entry.NewLines {
					if IsJSONOutput() {
						// JSON output for each new line
						jsonEntry := map[string]interface{}{
							"pane":       entry.Pane,
							"agent_type": entry.AgentType,
							"line":       line,
							"timestamp":  entry.Timestamp.Format(time.RFC3339),
						}
						_ = json.NewEncoder(os.Stdout).Encode(jsonEntry)
					} else {
						// Human-readable output
						color := th.Text
						if c, ok := agentColors[entry.AgentType]; ok {
							color = c
						}

						shortType := robot.FormatAggregatedLog(robot.AggregatedLogEntry{
							AgentType: entry.AgentType,
						})[:3]

						prefix := lipgloss.NewStyle().
							Foreground(color).
							Bold(true).
							Render(fmt.Sprintf("[%s:%d]", shortType, entry.Pane))

						timestamp := lipgloss.NewStyle().
							Foreground(th.Subtext).
							Render(entry.Timestamp.Format("15:04:05"))

						fmt.Printf("%s %s %s\n", prefix, timestamp, line)
					}
				}
			}
		}
	}
}

// ParsePanesArg is defined in robot package but we need a local version for CLI.
// This function delegates to robot.ParsePanesArg.
func parsePanesArgLocal(s string) ([]int, error) {
	return robot.ParsePanesArg(s)
}

// Helper for short agent type formatting
func shortAgentTypeLocal(agentType string) string {
	switch strings.ToLower(agentType) {
	case "claude":
		return "cc"
	case "codex":
		return "cod"
	case "gemini":
		return "gmi"
	default:
		if len(agentType) > 3 {
			return agentType[:3]
		}
		return agentType
	}
}
