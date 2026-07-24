package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/export"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func newTimelineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "timeline",
		Short: "View and manage session timeline history",
		Long: `View and manage persisted timeline data for post-session analysis.

Timelines contain agent state transitions (idle, working, waiting, error)
that occurred during a session, useful for analyzing agent productivity
and coordination patterns.

Examples:
  ntm timeline                         # List all saved timelines
  ntm timeline list                    # Same as above
  ntm timeline show <session-id>       # Show timeline details
  ntm timeline delete <session-id>     # Delete a timeline
  ntm timeline cleanup                 # Remove old timelines
  ntm timeline export <session-id>     # Export timeline data`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTimelineList()
		},
	}

	// Subcommands
	cmd.AddCommand(newTimelineListCmd())
	cmd.AddCommand(newTimelineShowCmd())
	cmd.AddCommand(newTimelineDeleteCmd())
	cmd.AddCommand(newTimelineCleanupCmd())
	cmd.AddCommand(newTimelineExportCmd())
	cmd.AddCommand(newTimelineStatsCmd())

	return cmd
}

func newTimelineListCmd() *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all saved timelines",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTimelineListWithLimit(limit)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "Number of timelines to show")

	return cmd
}

// TimelineListResult contains the list output
type TimelineListResult struct {
	Timelines  []state.TimelineInfo `json:"timelines"`
	TotalCount int                  `json:"total_count"`
	Showing    int                  `json:"showing"`
}

func (r *TimelineListResult) Text(w io.Writer) error {
	t := theme.Current()

	if len(r.Timelines) == 0 {
		fmt.Fprintf(w, "%sNo timelines found%s\n", colorize(t.Warning), colorize(t.Text))
		return nil
	}

	// Print header
	fmt.Fprintf(w, " %sSESSION%s              %sEVENTS%s  %sAGENTS%s  %sSIZE%s     %sMODIFIED%s\n",
		colorize(t.Surface1), colorize(t.Text),
		colorize(t.Surface1), colorize(t.Text),
		colorize(t.Surface1), colorize(t.Text),
		colorize(t.Surface1), colorize(t.Text),
		colorize(t.Surface1), colorize(t.Text))

	for _, ti := range r.Timelines {
		sessionName := truncate(ti.SessionID, 20)
		modTime := ti.ModifiedAt.Local().Format("01-02 15:04")

		compressedIcon := ""
		if ti.Compressed {
			compressedIcon = " (gz)"
		}

		fmt.Fprintf(w, " %-20s %6d  %6d  %7s%s  %s\n",
			sessionName,
			ti.EventCount,
			ti.AgentCount,
			formatBytes(ti.Size),
			compressedIcon,
			modTime)
	}

	if r.TotalCount > r.Showing {
		fmt.Fprintf(w, "\n%sShowing %d of %d timelines. Use --limit for more.%s\n",
			colorize(t.Surface1), r.Showing, r.TotalCount, colorize(t.Text))
	}

	return nil
}

func (r *TimelineListResult) JSON() interface{} {
	return r
}

func runTimelineList() error {
	return runTimelineListWithLimit(20)
}

func runTimelineListWithLimit(limit int) error {
	persister, err := state.GetDefaultTimelinePersister()
	if err != nil {
		return fmt.Errorf("failed to get timeline persister: %w", err)
	}

	timelines, err := persister.ListTimelines()
	if err != nil {
		return fmt.Errorf("failed to list timelines: %w", err)
	}

	totalCount := len(timelines)
	showing := len(timelines)
	if showing > limit {
		timelines = timelines[:limit]
		showing = limit
	}

	result := &TimelineListResult{
		Timelines:  timelines,
		TotalCount: totalCount,
		Showing:    showing,
	}

	formatter := output.New(output.WithJSON(jsonOutput))
	return formatter.Output(result)
}

func newTimelineShowCmd() *cobra.Command {
	var showEvents bool

	cmd := &cobra.Command{
		Use:   "show <session-id>",
		Short: "Show timeline details for a session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTimelineShow(args[0], showEvents)
		},
	}

	cmd.Flags().BoolVarP(&showEvents, "events", "e", false, "Show all events")

	return cmd
}

// TimelineShowResult contains timeline details
type TimelineShowResult struct {
	Info   *state.TimelineInfo `json:"info"`
	Events []state.AgentEvent  `json:"events,omitempty"`
	Stats  *TimelineEventStats `json:"stats"`
}

// TimelineEventStats contains aggregated statistics
type TimelineEventStats struct {
	TotalEvents    int                         `json:"total_events"`
	UniqueAgents   int                         `json:"unique_agents"`
	AgentBreakdown map[string]int              `json:"agent_breakdown"`
	StateBreakdown map[state.TimelineState]int `json:"state_breakdown"`
	Duration       time.Duration               `json:"duration"`
	FirstEvent     time.Time                   `json:"first_event"`
	LastEvent      time.Time                   `json:"last_event"`
}

func (r *TimelineShowResult) Text(w io.Writer) error {
	t := theme.Current()

	if r.Info == nil {
		fmt.Fprintf(w, "%sTimeline not found%s\n", colorize(t.Warning), colorize(t.Text))
		return nil
	}

	info := r.Info

	fmt.Fprintf(w, "%sTimeline:%s  %s\n", colorize(t.Blue), colorize(t.Text), info.SessionID)
	fmt.Fprintf(w, "%sPath:%s      %s\n", colorize(t.Blue), colorize(t.Text), info.Path)
	fmt.Fprintf(w, "%sEvents:%s    %d\n", colorize(t.Blue), colorize(t.Text), info.EventCount)
	fmt.Fprintf(w, "%sAgents:%s    %d\n", colorize(t.Blue), colorize(t.Text), info.AgentCount)
	fmt.Fprintf(w, "%sSize:%s      %s", colorize(t.Blue), colorize(t.Text), formatBytes(info.Size))
	if info.Compressed {
		fmt.Fprintf(w, " (compressed)")
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%sModified:%s  %s\n", colorize(t.Blue), colorize(t.Text), info.ModifiedAt.Local().Format("2006-01-02 15:04:05"))

	if r.Stats != nil && r.Stats.TotalEvents > 0 {
		fmt.Fprintf(w, "\n%sStatistics%s\n", colorize(t.Blue), colorize(t.Text))
		fmt.Fprintf(w, "───────────────────────\n")
		fmt.Fprintf(w, "Duration:     %s\n", r.Stats.Duration.Round(time.Second))
		fmt.Fprintf(w, "First event:  %s\n", r.Stats.FirstEvent.Local().Format("15:04:05"))
		fmt.Fprintf(w, "Last event:   %s\n", r.Stats.LastEvent.Local().Format("15:04:05"))

		if len(r.Stats.AgentBreakdown) > 0 {
			fmt.Fprintf(w, "\n%sEvents by Agent%s\n", colorize(t.Blue), colorize(t.Text))
			for agent, count := range r.Stats.AgentBreakdown {
				fmt.Fprintf(w, "  %-12s %d\n", agent, count)
			}
		}

		if len(r.Stats.StateBreakdown) > 0 {
			fmt.Fprintf(w, "\n%sEvents by State%s\n", colorize(t.Blue), colorize(t.Text))
			for st, count := range r.Stats.StateBreakdown {
				fmt.Fprintf(w, "  %-12s %d\n", st, count)
			}
		}
	}

	if len(r.Events) > 0 {
		fmt.Fprintf(w, "\n%sEvents%s\n", colorize(t.Blue), colorize(t.Text))
		fmt.Fprintf(w, "──────────────────────────────────────────────────────────\n")
		fmt.Fprintf(w, " %sTIME%s       %sAGENT%s       %sSTATE%s     %sDURATION%s\n",
			colorize(t.Surface1), colorize(t.Text),
			colorize(t.Surface1), colorize(t.Text),
			colorize(t.Surface1), colorize(t.Text),
			colorize(t.Surface1), colorize(t.Text))

		for _, e := range r.Events {
			timeStr := e.Timestamp.Local().Format("15:04:05")
			durStr := ""
			if e.Duration > 0 {
				durStr = e.Duration.Round(time.Second).String()
			}

			stateColor := t.Text
			switch e.State {
			case state.TimelineWorking:
				stateColor = t.Success
			case state.TimelineError:
				stateColor = t.Red
			case state.TimelineIdle:
				stateColor = t.Surface1
			}

			fmt.Fprintf(w, " %s  %-11s %s%-9s%s  %s\n",
				timeStr,
				e.AgentID,
				colorize(stateColor),
				e.State,
				colorize(t.Text),
				durStr)
		}
	}

	return nil
}

func (r *TimelineShowResult) JSON() interface{} {
	return r
}

func runTimelineShow(sessionID string, showEvents bool) error {
	persister, err := state.GetDefaultTimelinePersister()
	if err != nil {
		return fmt.Errorf("failed to get timeline persister: %w", err)
	}

	info, err := persister.GetTimelineInfo(sessionID)
	if err != nil {
		return fmt.Errorf("failed to get timeline info: %w", err)
	}

	if info == nil {
		return fmt.Errorf("timeline not found: %s", sessionID)
	}

	var events []state.AgentEvent
	var stats *TimelineEventStats

	// Always load events for stats
	loadedEvents, err := persister.LoadTimeline(sessionID)
	if err == nil && len(loadedEvents) > 0 {
		stats = computeTimelineStats(loadedEvents)
		if showEvents {
			events = loadedEvents
		}
	}

	result := &TimelineShowResult{
		Info:   info,
		Events: events,
		Stats:  stats,
	}

	formatter := output.New(output.WithJSON(jsonOutput))
	return formatter.Output(result)
}

func computeTimelineStats(events []state.AgentEvent) *TimelineEventStats {
	if len(events) == 0 {
		return nil
	}

	stats := &TimelineEventStats{
		TotalEvents:    len(events),
		AgentBreakdown: make(map[string]int),
		StateBreakdown: make(map[state.TimelineState]int),
		FirstEvent:     events[0].Timestamp,
		LastEvent:      events[len(events)-1].Timestamp,
	}

	agents := make(map[string]struct{})
	for _, e := range events {
		agents[e.AgentID] = struct{}{}
		stats.AgentBreakdown[e.AgentID]++
		stats.StateBreakdown[e.State]++
	}

	stats.UniqueAgents = len(agents)
	stats.Duration = stats.LastEvent.Sub(stats.FirstEvent)

	return stats
}

func newTimelineDeleteCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "delete <session-id>",
		Short: "Delete a timeline",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTimelineDelete(args[0], force)
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")

	return cmd
}

func runTimelineDelete(sessionID string, force bool) error {
	t := theme.Current()

	persister, err := state.GetDefaultTimelinePersister()
	if err != nil {
		return fmt.Errorf("failed to get timeline persister: %w", err)
	}

	// Check if exists
	info, err := persister.GetTimelineInfo(sessionID)
	if err != nil {
		return fmt.Errorf("failed to check timeline: %w", err)
	}
	if info == nil {
		return fmt.Errorf("timeline not found: %s", sessionID)
	}

	if !force {
		fmt.Printf("Delete timeline %q with %d events? [y/N]: ", sessionID, info.EventCount)
		var response string
		fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	if err := persister.DeleteTimeline(sessionID); err != nil {
		return fmt.Errorf("failed to delete timeline: %w", err)
	}

	fmt.Printf("%s✓%s Deleted timeline %s\n", colorize(t.Success), colorize(t.Text), sessionID)
	return nil
}

func newTimelineCleanupCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Remove old timelines exceeding retention limit",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTimelineCleanup(force)
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")

	return cmd
}

// TimelineCleanupResult contains cleanup results
type TimelineCleanupResult struct {
	Deleted    int `json:"deleted"`
	Remaining  int `json:"remaining"`
	Compressed int `json:"compressed"`
	// Warnings reports partial failures. Compression is opportunistic, so a
	// failure there does not fail the command — but it must not be invisible
	// either, or retention silently stops being enforced.
	Warnings []string `json:"warnings,omitempty"`
}

func (r *TimelineCleanupResult) Text(w io.Writer) error {
	t := theme.Current()

	// Warnings print even when nothing was cleaned up: a compression failure is
	// exactly the case that would otherwise read as "nothing to do".
	printWarnings := func() {
		for _, warning := range r.Warnings {
			fmt.Fprintf(w, "%s!%s %s\n", colorize(t.Warning), colorize(t.Text), warning)
		}
	}

	if r.Deleted == 0 && r.Compressed == 0 {
		fmt.Fprintf(w, "%sNo cleanup needed%s\n", colorize(t.Warning), colorize(t.Text))
		printWarnings()
		return nil
	}

	if r.Deleted > 0 {
		fmt.Fprintf(w, "%s✓%s Deleted %d old timelines\n", colorize(t.Success), colorize(t.Text), r.Deleted)
	}
	if r.Compressed > 0 {
		fmt.Fprintf(w, "%s✓%s Compressed %d timelines\n", colorize(t.Success), colorize(t.Text), r.Compressed)
	}
	fmt.Fprintf(w, "  %d timelines remaining\n", r.Remaining)
	printWarnings()

	return nil
}

func (r *TimelineCleanupResult) JSON() interface{} {
	return r
}

func runTimelineCleanup(force bool) error {
	persister, err := state.GetDefaultTimelinePersister()
	if err != nil {
		return fmt.Errorf("failed to get timeline persister: %w", err)
	}

	timelines, err := persister.ListTimelines()
	if err != nil {
		return fmt.Errorf("failed to list timelines: %w", err)
	}

	if !force && len(timelines) > 0 {
		fmt.Printf("Clean up old timelines? This may delete some data. [y/N]: ")
		var response string
		fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	deleted, err := persister.Cleanup()
	if err != nil {
		return fmt.Errorf("cleanup failed: %w", err)
	}

	var warnings []string
	compressed, compressErr := persister.CompressOldTimelines()
	if compressErr != nil {
		warnings = append(warnings, fmt.Sprintf("compression incomplete: %v", compressErr))
	}

	remaining, listErr := persister.ListTimelines()
	if listErr != nil {
		warnings = append(warnings, fmt.Sprintf("could not recount timelines: %v", listErr))
	}

	result := &TimelineCleanupResult{
		Deleted:    deleted,
		Remaining:  len(remaining),
		Compressed: compressed,
		Warnings:   warnings,
	}

	formatter := output.New(output.WithJSON(jsonOutput))
	return formatter.Output(result)
}

func newTimelineExportCmd() *cobra.Command {
	var (
		format     string
		outputFile string
		scale      int
		since      string
		until      string
		width      int
		lightTheme bool
		noLegend   bool
		noMeta     bool
	)

	cmd := &cobra.Command{
		Use:   "export <session-id>",
		Short: "Export timeline events to a file",
		Long: `Export timeline visualization to various formats.

Supported formats:
  jsonl - JSON Lines format (one event per line)
  svg   - Scalable Vector Graphics (for docs/reports)
  png   - PNG raster image (supports --scale for resolution)

Examples:
  ntm timeline export myproject                        # Export to JSONL
  ntm timeline export myproject --format=svg           # Export to SVG
  ntm timeline export myproject --format=png --scale=2 # Export to 2x PNG
  ntm timeline export myproject -o timeline.svg        # Specify output file
  ntm timeline export myproject --since=1h             # Last hour only
  ntm timeline export myproject --light                # Light theme for print`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID := args[0]
			return runTimelineExport(sessionID, exportOptions{
				format:     format,
				outputFile: outputFile,
				scale:      scale,
				since:      since,
				until:      until,
				width:      width,
				lightTheme: lightTheme,
				noLegend:   noLegend,
				noMeta:     noMeta,
			})
		},
	}

	cmd.Flags().StringVarP(&format, "format", "f", "jsonl", "Output format: jsonl, svg, png")
	cmd.Flags().StringVarP(&outputFile, "output", "o", "", "Output file path (default: <session>_timeline.<format>)")
	cmd.Flags().IntVarP(&scale, "scale", "s", 1, "Scale multiplier for PNG output (1, 2, or 3)")
	cmd.Flags().StringVar(&since, "since", "", "Filter events since duration (e.g., 1h, 30m)")
	cmd.Flags().StringVar(&until, "until", "", "Filter events until time/duration")
	cmd.Flags().IntVarP(&width, "width", "w", 1200, "Image width in pixels")
	cmd.Flags().BoolVar(&lightTheme, "light", false, "Use light theme (better for print)")
	cmd.Flags().BoolVar(&noLegend, "no-legend", false, "Omit color legend from export")
	cmd.Flags().BoolVar(&noMeta, "no-metadata", false, "Omit session metadata from export")

	return cmd
}

type exportOptions struct {
	format     string
	outputFile string
	scale      int
	since      string
	until      string
	width      int
	lightTheme bool
	noLegend   bool
	noMeta     bool
}

func runTimelineExport(sessionID string, opts exportOptions) error {
	t := theme.Current()

	persister, err := state.GetDefaultTimelinePersister()
	if err != nil {
		return fmt.Errorf("failed to get timeline persister: %w", err)
	}

	events, err := persister.LoadTimeline(sessionID)
	if err != nil {
		return fmt.Errorf("failed to load timeline: %w", err)
	}

	if len(events) == 0 {
		return fmt.Errorf("timeline not found or empty: %s", sessionID)
	}

	// Determine output format and file
	format := strings.ToLower(opts.format)
	outputFile := opts.outputFile
	if outputFile == "" {
		ext := format
		if ext == "jsonl" {
			ext = "jsonl"
		}
		outputFile = sessionID + "_timeline." + ext
	}

	// Handle JSONL export (original behavior)
	if format == "jsonl" {
		exportConfig := &state.TimelinePersistConfig{
			BaseDir: ".",
		}
		jsonExporter, err := state.NewTimelinePersister(exportConfig)
		if err != nil {
			return fmt.Errorf("failed to create exporter: %w", err)
		}

		baseName := strings.TrimSuffix(outputFile, ".jsonl")
		if err := jsonExporter.SaveTimeline(baseName, events); err != nil {
			return fmt.Errorf("failed to export: %w", err)
		}

		fmt.Printf("%s✓%s Exported %d events to %s\n", colorize(t.Success), colorize(t.Text), len(events), baseName+".jsonl")
		return nil
	}

	// Handle SVG/PNG export
	if format != "svg" && format != "png" {
		return fmt.Errorf("unsupported format: %s (use jsonl, svg, or png)", format)
	}

	// Parse time filters
	var sinceTime, untilTime time.Time
	if opts.since != "" {
		duration, err := time.ParseDuration(opts.since)
		if err != nil {
			return fmt.Errorf("invalid --since value: %w", err)
		}
		sinceTime = time.Now().Add(-duration)
	}
	if opts.until != "" {
		duration, err := time.ParseDuration(opts.until)
		if err != nil {
			// Try parsing as absolute time
			untilTime, err = time.Parse(time.RFC3339, opts.until)
			if err != nil {
				return fmt.Errorf("invalid --until value: %w", err)
			}
		} else {
			untilTime = time.Now().Add(-duration)
		}
	}

	// Filter events by time range if specified
	if !sinceTime.IsZero() || !untilTime.IsZero() {
		filteredEvents := make([]state.AgentEvent, 0, len(events))
		for _, ev := range events {
			if !sinceTime.IsZero() && ev.Timestamp.Before(sinceTime) {
				continue
			}
			if !untilTime.IsZero() && ev.Timestamp.After(untilTime) {
				continue
			}
			filteredEvents = append(filteredEvents, ev)
		}
		events = filteredEvents
	}

	if len(events) == 0 {
		return fmt.Errorf("no events in the specified time range")
	}

	// Build export options
	exportOpts := export.DefaultExportOptions()
	exportOpts.SessionName = sessionID
	exportOpts.Width = opts.width
	exportOpts.Scale = opts.scale
	exportOpts.Since = sinceTime
	exportOpts.Until = untilTime
	exportOpts.IncludeLegend = !opts.noLegend
	exportOpts.IncludeMetadata = !opts.noMeta

	if opts.lightTheme {
		exportOpts.Theme = export.LightTheme()
	}

	if format == "svg" {
		exportOpts.Format = export.FormatSVG
	} else {
		exportOpts.Format = export.FormatPNG
	}

	// Create exporter and export
	exporter := export.NewTimelineExporter(exportOpts)

	var data []byte
	if format == "svg" {
		data, err = exporter.ExportSVG(events)
	} else {
		data, err = exporter.ExportPNG(events)
	}
	if err != nil {
		return fmt.Errorf("failed to export: %w", err)
	}

	// Write to file
	if err := os.WriteFile(outputFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	fmt.Printf("%s✓%s Exported %d events to %s (%s format)\n",
		colorize(t.Success), colorize(t.Text),
		len(events), outputFile, strings.ToUpper(format))
	return nil
}

func newTimelineStatsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Show timeline storage statistics",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTimelineStats()
		},
	}
}

// TimelineStatsResult contains overall statistics
type TimelineStatsResult struct {
	TotalTimelines  int    `json:"total_timelines"`
	TotalEvents     int    `json:"total_events"`
	TotalAgents     int    `json:"total_agents"`
	TotalSize       int64  `json:"total_size_bytes"`
	CompressedCount int    `json:"compressed_count"`
	OldestTimeline  string `json:"oldest_timeline,omitempty"`
	NewestTimeline  string `json:"newest_timeline,omitempty"`
}

func (r *TimelineStatsResult) Text(w io.Writer) error {
	t := theme.Current()

	fmt.Fprintf(w, "%sTimeline Statistics%s\n", colorize(t.Blue), colorize(t.Text))
	fmt.Fprintf(w, "───────────────────────────\n")
	fmt.Fprintf(w, "Total timelines:    %d\n", r.TotalTimelines)
	fmt.Fprintf(w, "Total events:       %d\n", r.TotalEvents)
	fmt.Fprintf(w, "Unique agents:      %d\n", r.TotalAgents)
	fmt.Fprintf(w, "Total size:         %s\n", formatBytes(r.TotalSize))
	fmt.Fprintf(w, "Compressed:         %d\n", r.CompressedCount)
	if r.OldestTimeline != "" {
		fmt.Fprintf(w, "Oldest:             %s\n", r.OldestTimeline)
	}
	if r.NewestTimeline != "" {
		fmt.Fprintf(w, "Newest:             %s\n", r.NewestTimeline)
	}

	return nil
}

func (r *TimelineStatsResult) JSON() interface{} {
	return r
}

func runTimelineStats() error {
	persister, err := state.GetDefaultTimelinePersister()
	if err != nil {
		return fmt.Errorf("failed to get timeline persister: %w", err)
	}

	timelines, err := persister.ListTimelines()
	if err != nil {
		return fmt.Errorf("failed to list timelines: %w", err)
	}

	result := &TimelineStatsResult{
		TotalTimelines: len(timelines),
	}

	if len(timelines) == 0 {
		formatter := output.New(output.WithJSON(jsonOutput))
		return formatter.Output(result)
	}

	allAgents := make(map[string]struct{})
	var totalSize int64

	// Sort for oldest/newest
	sort.Slice(timelines, func(i, j int) bool {
		return timelines[i].ModifiedAt.Before(timelines[j].ModifiedAt)
	})

	for _, ti := range timelines {
		result.TotalEvents += ti.EventCount
		totalSize += ti.Size
		if ti.Compressed {
			result.CompressedCount++
		}

		// Load to count unique agents across all timelines
		events, _ := persister.LoadTimeline(ti.SessionID)
		for _, e := range events {
			allAgents[e.AgentID] = struct{}{}
		}
	}

	result.TotalSize = totalSize
	result.TotalAgents = len(allAgents)

	if len(timelines) > 0 {
		result.OldestTimeline = timelines[0].SessionID
		result.NewestTimeline = timelines[len(timelines)-1].SessionID
	}

	formatter := output.New(output.WithJSON(jsonOutput))
	return formatter.Output(result)
}
