package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/cass"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func newCassCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cass",
		Short: "Interact with CASS (Coding Agent Session Search)",
		Long:  `Search, analyze, and explore past agent sessions indexed by CASS.`,
	}

	cmd.AddCommand(newCassStatusCmd())
	cmd.AddCommand(newCassSearchCmd())
	cmd.AddCommand(newCassInsightsCmd())
	cmd.AddCommand(newCassTimelineCmd())
	cmd.AddCommand(newCassPreviewCmd())

	return cmd
}

func newSearchCmd() *cobra.Command {
	var (
		session string
		agent   string
		since   string
		limit   int
		offset  int
		quick   bool
	)

	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search archived agent output via CASS",
		Long: `Search past agent sessions indexed by CASS (Coding Agent Session Search).

Queries archived agent output across all sessions, with optional filtering
by session name, agent type, and time range.

This is a convenience wrapper around 'ntm cass search' with defaults
tuned for quick lookups.`,
		Example: `  ntm search 'rate limiting middleware'
  ntm search 'authentication' --session=myproject
  ntm search 'database migration' --agent=claude_code --since=7d
  ntm search 'error handling' --limit=5 --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCassSearchWithMode(args[0], agent, sessionWorkspace(session), since, limit, offset, quick)
		},
	}

	cmd.Flags().StringVarP(&session, "session", "s", "", "Filter by ntm session (searches its project directory) or an absolute workspace path")
	cmd.Flags().StringVarP(&agent, "agent", "a", "", "Filter by agent type (e.g. claude_code)")
	cmd.Flags().StringVar(&since, "since", "", "Filter by time (e.g. 1h, 7d, 30d)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "Max results to return")
	cmd.Flags().IntVar(&offset, "offset", 0, "Result offset for pagination")
	cmd.Flags().BoolVar(&quick, "quick", false, "Fast summary-only search with CASS defaults")

	return cmd
}

// sessionWorkspace maps `ntm search --session` to the workspace path cass
// filters on: cass matches --workspace against full project paths, so an ntm
// session name becomes its project directory; an absolute path passes through.
func sessionWorkspace(session string) string {
	session = strings.TrimSpace(session)
	if session == "" || filepath.IsAbs(session) {
		return session
	}
	c := cfg
	if c == nil {
		c = config.Default()
	}
	return c.GetProjectDir(session)
}

func handleCassError(err error) error {
	if err == nil {
		return nil
	}

	if IsJSONOutput() {
		return emitCassFailure(err, errors.Is(err, cass.ErrNotInstalled), nil)
	}

	if errors.Is(err, cass.ErrNotInstalled) {
		fmt.Println("CASS is not installed.")
		fmt.Println("To enable cross-agent session search:")
		fmt.Println("  brew install nightowlai/tap/cass    # macOS")
		fmt.Println("  cargo install cass                  # From source")
		return robot.ExitResultForCode(2, err, false)
	}
	return err
}

func emitCassFailure(cause error, unavailable bool, fields map[string]interface{}) error {
	code := robot.ErrCodeInternalError
	hint := "Check the CASS index and configuration, then retry"
	exitCode := 1
	if unavailable {
		code = robot.ErrCodeNotImplemented
		hint = "Install CASS to enable this feature"
		exitCode = 2
	}

	response := map[string]interface{}{
		"success":    false,
		"error":      cause.Error(),
		"error_code": code,
		"hint":       hint,
	}
	for key, value := range fields {
		response[key] = value
	}
	if err := output.PrintJSON(response); err != nil {
		return err
	}
	return robot.ExitResultForCode(exitCode, cause, true)
}

func newCassStatusCmd() *cobra.Command {
	var healthCheck bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show CASS index health and statistics",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCassStatusWithOptions(healthCheck)
		},
	}
	cmd.Flags().BoolVar(&healthCheck, "health", false, "Run CASS dedicated health probe")
	return cmd
}

func newCassClient() *cass.Client {
	var opts []cass.ClientOption
	if cfg != nil && cfg.CASS.BinaryPath != "" {
		opts = append(opts, cass.WithBinaryPath(cfg.CASS.BinaryPath))
	}
	return cass.NewClient(opts...)
}

func runCassStatusWithOptions(healthOnly bool) error {
	ctx := context.Background()
	client := newCassClient()
	var (
		status *cass.StatusResponse
		err    error
	)
	if healthOnly {
		status, err = client.Health(ctx)
	} else {
		status, err = client.Status(ctx)
	}
	if err != nil {
		return handleCassError(err)
	}
	if healthOnly && status != nil && !status.Healthy {
		status.Healthy = client.IsHealthy(ctx)
	}

	if IsJSONOutput() {
		return output.PrintJSON(status)
	}

	t := theme.Current()
	fmt.Printf("%sCASS Index Status%s\n", "\033[1m", "\033[0m")
	fmt.Printf("%s%s%s\n\n", "\033[2m", strings.Repeat("─", 40), "\033[0m")

	healthyMark := fmt.Sprintf("%s✓%s", colorize(t.Success), "\033[0m")
	if !status.Healthy {
		healthyMark = fmt.Sprintf("%s✗%s", colorize(t.Error), "\033[0m")
	}

	fmt.Printf("  Healthy:        %s\n", healthyMark)
	if status.Database.CountsSkipped {
		fmt.Printf("  Conversations:  not counted (cass skipped the database counts)\n")
	} else {
		fmt.Printf("  Conversations:  %d\n", status.Conversations)
		fmt.Printf("  Messages:       %d\n", status.Messages)
	}
	fmt.Printf("  Last Indexed:   %s\n", formatAge(status.LastIndexedAt.Time))
	fmt.Printf("  Documents:      %d\n", status.Index.Documents)
	fmt.Printf("  Database Size:  %.1f MB\n", status.Database.SizeMB())
	if status.Index.SizeBytes > 0 { // legacy cass reports the index size
		fmt.Printf("  Index Size:     %.1f MB\n", status.Index.SizeMB())
	}
	if status.Pending.HasPending() {
		fmt.Printf("  Pending:        %d sessions", status.Pending.Sessions)
		if status.Pending.Files > 0 {
			fmt.Printf(", %d files", status.Pending.Files)
		}
		fmt.Println()
	}

	return nil
}

func newCassSearchCmd() *cobra.Command {
	var (
		agent     string
		workspace string
		since     string
		limit     int
		offset    int
		quick     bool
	)

	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search past agent sessions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCassSearchWithMode(args[0], agent, workspace, since, limit, offset, quick)
		},
	}

	cmd.Flags().StringVar(&agent, "agent", "", "Filter by agent type")
	cmd.Flags().StringVar(&workspace, "workspace", "", "Filter by workspace/project")
	cmd.Flags().StringVar(&since, "since", "", "Filter by time (e.g. 7d)")
	cmd.Flags().IntVar(&limit, "limit", 10, "Max results")
	cmd.Flags().IntVar(&offset, "offset", 0, "Result offset")
	cmd.Flags().BoolVar(&quick, "quick", false, "Fast summary-only search with CASS defaults")

	return cmd
}

func runCassSearchWithMode(query, agent, workspace, since string, limit, offset int, quick bool) error {
	ctx := context.Background()
	client := newCassClient()
	var (
		resp *cass.SearchResponse
		err  error
	)
	if quick && agent == "" && workspace == "" && since == "" && offset == 0 {
		resp, err = client.SearchQuick(ctx, query)
	} else {
		resp, err = client.Search(ctx, cass.SearchOptions{
			Query:     query,
			Agent:     agent,
			Workspace: workspace,
			Since:     since,
			Limit:     limit,
			Offset:    offset,
		})
	}
	if err != nil {
		return handleCassError(err)
	}

	if IsJSONOutput() {
		return output.PrintJSON(resp)
	}

	t := theme.Current()
	fmt.Printf("%sSearch Results (%d of %d)%s\n", "\033[1m", resp.Count, resp.TotalMatches, "\033[0m")
	fmt.Printf("%s%s%s\n\n", "\033[2m", strings.Repeat("─", 60), "\033[0m")

	if !resp.HasResults() {
		fmt.Println("  No matching sessions found.")
		return nil
	}

	for _, hit := range resp.Hits {
		score := fmt.Sprintf("%.2f", hit.Score)
		fmt.Printf("  %s%s%s (%s)\n", colorize(t.Primary), hit.Title, "\033[0m", hit.Agent)
		fmt.Printf("    %s%s%s • score: %s • %s\n",
			colorize(t.Subtext), hit.Workspace, "\033[0m",
			score, formatAge(hit.CreatedAtTime()))
		if hit.Snippet != "" {
			fmt.Printf("    %s%s%s\n", "\033[2m", strings.TrimSpace(hit.Snippet), "\033[0m")
		}
		fmt.Println()
	}

	return nil
}

func newCassInsightsCmd() *cobra.Command {
	var since string
	cmd := &cobra.Command{
		Use:   "insights",
		Short: "Show insights on agent activity",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCassInsights(since)
		},
	}
	cmd.Flags().StringVar(&since, "since", "7d", "Analysis period")
	return cmd
}

func runCassInsights(since string) error {
	client := newCassClient()
	resp, err := client.Search(context.Background(), cass.SearchOptions{
		Query:     "*",
		Since:     since,
		Limit:     0,
		Aggregate: "agent,workspace",
	})
	if err != nil {
		return handleCassError(err)
	}

	if IsJSONOutput() {
		return output.PrintJSON(resp.Aggregations)
	}

	t := theme.Current()
	fmt.Printf("%sAgent Insights (Since %s)%s\n", "\033[1m", since, "\033[0m")

	if resp.Aggregations != nil {
		printAggregations("Top Agents", resp.Aggregations.Agent, t)
		printAggregations("Top Workspaces", resp.Aggregations.Workspace, t)
	}

	return nil
}

// printAggregations prints a field's top five cass buckets (cass already
// orders them largest first).
func printAggregations(title string, agg *cass.AggregationBuckets, t theme.Theme) {
	if agg == nil || len(agg.Buckets) == 0 {
		return
	}
	fmt.Printf("\n  %s%s%s\n", colorize(t.Info), title, "\033[0m")

	for i, bucket := range agg.Buckets {
		if i >= 5 {
			break
		}
		key := bucket.Key
		if key == "" {
			key = "(none)"
		}
		fmt.Printf("    %-20s %d\n", key, bucket.Count)
	}
}

func newCassTimelineCmd() *cobra.Command {
	var since, groupBy string
	cmd := &cobra.Command{
		Use:   "timeline",
		Short: "Show agent activity over time",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCassTimeline(since, groupBy)
		},
	}
	cmd.Flags().StringVar(&since, "since", "24h", "Timeline period")
	cmd.Flags().StringVar(&groupBy, "group-by", "hour", "Grouping (hour, day, none)")
	return cmd
}

func runCassTimeline(since, groupBy string) error {
	client := newCassClient()
	resp, err := client.Timeline(context.Background(), since, groupBy)
	if err != nil {
		return handleCassError(err)
	}

	if IsJSONOutput() {
		return output.PrintJSON(resp)
	}

	t := theme.Current()
	fmt.Printf("%sActivity Timeline (%s): %d sessions%s\n", colorize(t.Primary), since, resp.TotalSessions, "\033[0m")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if resp.Sessions != nil {
		fmt.Fprintln(w, "Started\tAgent\tTitle")
		fmt.Fprintln(w, "───────\t─────\t─────")
		for _, s := range resp.Sessions {
			fmt.Fprintf(w, "%s\t%s\t%s\n", s.StartTime().Format("Jan 02 15:04"), s.Agent, truncateRunes(s.Title, 60, "…"))
		}
	} else {
		fmt.Fprintln(w, "Period\tSessions\tAgents")
		fmt.Fprintln(w, "──────\t────────\t──────")
		periods := make([]string, 0, len(resp.Groups))
		for period := range resp.Groups {
			periods = append(periods, period)
		}
		sort.Strings(periods)
		for _, period := range periods {
			sessions := resp.Groups[period]
			fmt.Fprintf(w, "%s\t%d\t%s\n", period, len(sessions), timelineAgents(sessions))
		}
	}
	w.Flush()

	return nil
}

// timelineAgents summarizes which agents ran in a timeline period, busiest
// first, e.g. "claude_code×12, codex×3".
func timelineAgents(sessions []cass.TimelineSession) string {
	counts := make(map[string]int)
	for _, s := range sessions {
		counts[s.Agent]++
	}
	agents := make([]string, 0, len(counts))
	for agent := range counts {
		agents = append(agents, agent)
	}
	sort.Slice(agents, func(i, j int) bool {
		if counts[agents[i]] != counts[agents[j]] {
			return counts[agents[i]] > counts[agents[j]]
		}
		return agents[i] < agents[j]
	})
	parts := make([]string, 0, len(agents))
	for _, agent := range agents {
		parts = append(parts, fmt.Sprintf("%s×%d", agent, counts[agent]))
	}
	return strings.Join(parts, ", ")
}

func newCassPreviewCmd() *cobra.Command {
	var (
		maxResults int
		maxAgeDays int
		format     string
		maxTokens  int
	)

	cmd := &cobra.Command{
		Use:   "preview <prompt>",
		Short: "Preview what CASS would inject for a prompt",
		Long: `Preview the context that CASS would inject into an agent prompt.

This is useful for:
- Understanding what CASS finds for a given prompt
- Debugging why certain context is/isn't injected
- Tuning threshold and filter settings
- Seeing token counts before injection`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCassPreview(args[0], maxResults, maxAgeDays, format, maxTokens)
		},
	}

	cmd.Flags().IntVar(&maxResults, "max-results", 5, "Maximum CASS hits to retrieve")
	cmd.Flags().IntVar(&maxAgeDays, "max-age", 30, "Maximum age in days")
	cmd.Flags().StringVar(&format, "format", "markdown", "Injection format (markdown, minimal, structured)")
	cmd.Flags().IntVar(&maxTokens, "max-tokens", 500, "Maximum tokens for injection")

	return cmd
}

func runCassPreview(prompt string, maxResults, maxAgeDays int, format string, maxTokens int) error {
	queryConfig := robot.CASSConfig{
		Enabled:           true,
		MaxResults:        maxResults,
		MaxAgeDays:        maxAgeDays,
		PreferSameProject: true,
	}

	if cfg != nil {
		queryConfig.BinaryPath = cfg.CASS.BinaryPath
	}

	filterConfig := robot.DefaultFilterConfig()
	filterConfig.MaxItems = maxResults
	filterConfig.MaxAgeDays = maxAgeDays

	// Get current workspace for same-project preference
	if wd, err := os.Getwd(); err == nil {
		filterConfig.CurrentWorkspace = wd
	}

	// Query and filter
	queryResult, filterResult := robot.QueryAndFilterCASS(prompt, queryConfig, filterConfig)

	if IsJSONOutput() {
		if !queryResult.Success {
			cause := errors.New(queryResult.Error)
			unavailable := strings.Contains(strings.ToLower(queryResult.Error), "not found")
			return emitCassFailure(cause, unavailable, map[string]interface{}{
				"query":        queryResult,
				"filter":       filterResult,
				"keywords":     queryResult.Keywords,
				"total_hits":   queryResult.TotalMatches,
				"filtered_out": filterResult.RemovedByScore + filterResult.RemovedByAge,
			})
		}
		return output.PrintJSON(map[string]interface{}{
			"success":      true,
			"query":        queryResult,
			"filter":       filterResult,
			"keywords":     queryResult.Keywords,
			"total_hits":   queryResult.TotalMatches,
			"filtered_out": filterResult.RemovedByScore + filterResult.RemovedByAge,
		})
	}

	t := theme.Current()

	// Title
	fmt.Printf("%sCASS Injection Preview%s\n", "\033[1m", "\033[0m")
	fmt.Printf("%s%s%s\n\n", "\033[2m", strings.Repeat("─", 60), "\033[0m")

	// Query info
	fmt.Printf("%sPrompt:%s %s\n", colorize(t.Info), "\033[0m", truncateCassText(prompt, 60))
	if len(queryResult.Keywords) > 0 {
		fmt.Printf("%sExtracted Keywords:%s %s\n", colorize(t.Info), "\033[0m", strings.Join(queryResult.Keywords, ", "))
	}
	fmt.Printf("%sQuery:%s %s\n\n", colorize(t.Info), "\033[0m", queryResult.Query)

	// Results summary
	if !queryResult.Success {
		fmt.Printf("%sError:%s %s\n", colorize(t.Error), "\033[0m", queryResult.Error)
		cause := errors.New(queryResult.Error)
		unavailable := strings.Contains(strings.ToLower(queryResult.Error), "not found")
		exitCode := 1
		if unavailable {
			exitCode = 2
		}
		return robot.ExitResultForCode(exitCode, cause, false)
	}

	fmt.Printf("%sResults:%s %d hits found, %d after filtering\n",
		colorize(t.Success), "\033[0m",
		queryResult.TotalMatches, filterResult.FilteredCount)

	if filterResult.RemovedByAge > 0 {
		fmt.Printf("  %s→ %d removed (too old)%s\n", colorize(t.Subtext), filterResult.RemovedByAge, "\033[0m")
	}
	if filterResult.RemovedByScore > 0 {
		fmt.Printf("  %s→ %d removed (low relevance)%s\n", colorize(t.Subtext), filterResult.RemovedByScore, "\033[0m")
	}
	fmt.Println()

	// Show each hit with details
	if len(filterResult.Hits) > 0 {
		fmt.Printf("%sFiltered Hits:%s\n", colorize(t.Primary), "\033[0m")
		for i, hit := range filterResult.Hits {
			score := int(hit.ComputedScore * 100)
			sessionName := extractSessionNameFromPath(hit.SourcePath)
			fmt.Printf("  %d. %s%s%s (%d%% relevance)\n", i+1, colorize(t.Info), sessionName, "\033[0m", score)
			if hit.Content != "" {
				snippet := truncateCassText(hit.Content, 80)
				fmt.Printf("     %s%s%s\n", "\033[2m", snippet, "\033[0m")
			}
		}
		fmt.Println()
	}

	// Format injection preview
	injectFormat := robot.FormatMarkdown
	switch strings.ToLower(format) {
	case "minimal":
		injectFormat = robot.FormatMinimal
	case "structured":
		injectFormat = robot.FormatStructured
	}

	injectConfig := robot.InjectConfig{
		Format:    injectFormat,
		MaxTokens: maxTokens,
		DryRun:    true, // Don't modify prompt
	}

	injectResult := robot.InjectContext(prompt, filterResult.Hits, injectConfig)

	// Show injection preview
	fmt.Printf("%sInjection Preview (%s format):%s\n", colorize(t.Primary), format, "\033[0m")
	fmt.Printf("%s%s%s\n\n", "\033[2m", strings.Repeat("─", 40), "\033[0m")

	if injectResult.InjectedContext != "" {
		// Print context with indentation
		for _, line := range strings.Split(injectResult.InjectedContext, "\n") {
			fmt.Printf("  %s\n", line)
		}
		fmt.Println()
	} else {
		fmt.Printf("  %s(no context would be injected)%s\n\n", "\033[2m", "\033[0m")
	}

	// Token count
	fmt.Printf("%s%s%s\n", "\033[2m", strings.Repeat("─", 40), "\033[0m")
	fmt.Printf("%sToken estimate:%s ~%d tokens\n", colorize(t.Info), "\033[0m", injectResult.Metadata.TokensAdded)
	fmt.Printf("%sItems injected:%s %d\n", colorize(t.Info), "\033[0m", injectResult.Metadata.ItemsInjected)

	if injectResult.Metadata.SkippedReason != "" {
		fmt.Printf("%sSkipped:%s %s\n", colorize(t.Subtext), "\033[0m", injectResult.Metadata.SkippedReason)
	}

	return nil
}

// truncateCassText truncates text for CASS preview display.
func truncateCassText(s string, maxLen int) string {
	// Replace newlines with spaces for single-line display
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)

	// Handle edge cases where maxLen is too small
	if maxLen <= 0 {
		return ""
	}
	if len(s) <= maxLen {
		return s
	}

	// For very small maxLen (<=3), truncate without ellipsis but respect UTF-8 boundaries
	if maxLen <= 3 {
		byteLen := 0
		for i := range s {
			if i > maxLen {
				return s[:byteLen]
			}
			byteLen = i // Safe byte boundary *before* the rune that crosses maxLen
		}
		// If we finished the loop without crossing maxLen, the whole string fits
		if len(s) > maxLen {
			return s[:byteLen]
		}
		return s
	}

	// For maxLen >= 4, use ellipsis
	// Find last rune boundary at or before maxLen-3 bytes (UTF-8 safe)
	targetLen := maxLen - 3
	prevI := 0
	for i := range s {
		if i > targetLen {
			return s[:prevI] + "..."
		}
		prevI = i
	}
	return s[:prevI] + "..."
}

// extractSessionNameFromPath extracts a readable session name from the file path.
func extractSessionNameFromPath(path string) string {
	if path == "" {
		return "unknown"
	}

	parts := strings.Split(path, "/")
	filename := parts[len(parts)-1]

	if filename == "" {
		return "unknown"
	}

	filename = strings.TrimSuffix(filename, ".jsonl")
	filename = strings.TrimSuffix(filename, ".json")

	// Handle case where filename was only the extension
	if filename == "" {
		return "unknown"
	}

	if len(filename) > 40 {
		filename = filename[:37] + "..."
	}

	return filename
}
