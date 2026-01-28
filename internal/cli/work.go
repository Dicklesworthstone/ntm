// Package cli provides command-line interface commands for ntm.
// work.go implements the `ntm work` command for intelligent work distribution.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

func newWorkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "work",
		Short: "Intelligent work distribution commands",
		Long: `Commands for intelligent work distribution using bv analysis.

These commands wrap bv -robot-* with caching and NTM context,
providing a unified interface for work prioritization.

Examples:
  ntm work triage              # Get complete triage analysis
  ntm work triage --by-label   # Grouped by label
  ntm work triage --by-track   # Grouped by execution track
  ntm work alerts              # Show alerts (drift + proactive)
  ntm work search "JWT auth"   # Semantic search
  ntm work impact src/api/*.go # Impact analysis for files`,
	}

	cmd.AddCommand(newWorkTriageCmd())
	cmd.AddCommand(newWorkAlertsCmd())
	cmd.AddCommand(newWorkSearchCmd())
	cmd.AddCommand(newWorkImpactCmd())
	cmd.AddCommand(newWorkNextCmd())
	cmd.AddCommand(newWorkHistoryCmd())
	cmd.AddCommand(newWorkForecastCmd())
	cmd.AddCommand(newWorkGraphCmd())
	cmd.AddCommand(newWorkLabelHealthCmd())
	cmd.AddCommand(newWorkLabelFlowCmd())
	cmd.AddCommand(newWorkBurndownCmd())

	return cmd
}

func newWorkTriageCmd() *cobra.Command {
	var (
		byLabel    bool
		byTrack    bool
		limit      int
		showQuick  bool
		showHealth bool
		format     string
		compact    bool
	)

	cmd := &cobra.Command{
		Use:   "triage",
		Short: "Get complete triage analysis",
		Long: `Display intelligent work prioritization using bv triage.

Results are cached for 30 seconds to prevent excessive bv calls.

Format options:
  --format=json      Full JSON output (default for Claude)
  --format=markdown  Compact markdown (default for Codex/Gemini, 50% token savings)
  --format=auto      Auto-select based on agent type

Examples:
  ntm work triage              # Full triage with top recommendations
  ntm work triage --by-label   # Grouped by label
  ntm work triage --by-track   # Grouped by execution track
  ntm work triage --quick      # Just show quick wins
  ntm work triage --health     # Include project health metrics
  ntm work triage --json       # Output as JSON
  ntm work triage --format=markdown --compact  # Ultra-compact markdown`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkTriage(byLabel, byTrack, limit, showQuick, showHealth, format, compact)
		},
	}

	cmd.Flags().BoolVar(&byLabel, "by-label", false, "Group by label")
	cmd.Flags().BoolVar(&byTrack, "by-track", false, "Group by execution track")
	cmd.Flags().IntVarP(&limit, "limit", "n", 10, "Maximum recommendations to show")
	cmd.Flags().BoolVar(&showQuick, "quick", false, "Show only quick wins")
	cmd.Flags().BoolVar(&showHealth, "health", false, "Include project health metrics")
	cmd.Flags().StringVar(&format, "format", "", "Output format: json, markdown, or auto (default: auto for agents)")
	cmd.Flags().BoolVar(&compact, "compact", false, "Use compact output (with --format=markdown)")

	return cmd
}

func newWorkAlertsCmd() *cobra.Command {
	var (
		criticalOnly bool
		alertType    string
		labelFilter  string
	)

	cmd := &cobra.Command{
		Use:   "alerts",
		Short: "Show alerts (drift + proactive)",
		Long: `Display alerts from bv analysis.

Includes drift alerts and proactive issue alerts (stale issues, etc.).

Examples:
  ntm work alerts                      # All alerts
  ntm work alerts --critical-only      # Only critical alerts
  ntm work alerts --type=stale_issue   # Filter by type
  ntm work alerts --label=backend      # Filter by label
  ntm work alerts --json               # Output as JSON`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkAlerts(criticalOnly, alertType, labelFilter)
		},
	}

	cmd.Flags().BoolVar(&criticalOnly, "critical-only", false, "Show only critical alerts")
	cmd.Flags().StringVar(&alertType, "type", "", "Filter by alert type")
	cmd.Flags().StringVar(&labelFilter, "label", "", "Filter by label")

	return cmd
}

func newWorkSearchCmd() *cobra.Command {
	var (
		limit int
		mode  string
	)

	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Semantic search for issues",
		Long: `Search issues using semantic search.

Uses bv's vector-based search to find relevant issues.

Examples:
  ntm work search "JWT authentication"
  ntm work search "rate limiting" --limit=20
  ntm work search "database migration" --mode=hybrid
  ntm work search "API endpoints" --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkSearch(args[0], limit, mode)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 10, "Maximum results")
	cmd.Flags().StringVar(&mode, "mode", "text", "Search mode: text or hybrid")

	return cmd
}

func newWorkImpactCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "impact <paths...>",
		Short: "Analyze impact of file modifications",
		Long: `Analyze which issues are impacted by modifying specific files.

Helps understand the blast radius of code changes.

Examples:
  ntm work impact src/auth/*.go
  ntm work impact internal/api/users.go internal/api/auth.go
  ntm work impact "**/*_test.go" --json`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkImpact(args)
		},
	}

	return cmd
}

func newWorkNextCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "next",
		Short: "Get the single top recommendation",
		Long: `Display the single highest-priority recommendation.

Equivalent to 'bv -robot-next' but uses cached triage data.

Examples:
  ntm work next         # Show top pick
  ntm work next --json  # Output as JSON`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkNext()
		},
	}

	return cmd
}

// runWorkTriage executes the triage command
func runWorkTriage(byLabel, byTrack bool, limit int, showQuick, showHealth bool, format string, compact bool) error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	// Handle grouped views (these aren't cached yet, call bv directly)
	if byLabel || byTrack {
		return runGroupedTriage(dir, byLabel, byTrack)
	}

	// Determine output format
	outputFormat := resolveTriageFormat(format)

	// Handle markdown output
	if outputFormat == "markdown" {
		opts := bv.DefaultMarkdownOptions()
		if compact {
			opts = bv.CompactMarkdownOptions()
		}
		opts.MaxRecommendations = limit
		opts.IncludeScores = !compact

		md, err := bv.GetTriageMarkdown(dir, opts)
		if err != nil {
			return fmt.Errorf("getting triage markdown: %w", err)
		}
		fmt.Print(md)
		return nil
	}

	// Use cached triage for JSON/default output
	triage, err := bv.GetTriage(dir)
	if err != nil {
		return fmt.Errorf("getting triage: %w", err)
	}

	if jsonOutput || outputFormat == "json" {
		return outputJSON(triage)
	}

	return renderTriage(triage, limit, showQuick, showHealth)
}

// resolveTriageFormat determines the output format based on flags and context.
func resolveTriageFormat(format string) string {
	switch strings.ToLower(format) {
	case "json":
		return "json"
	case "markdown", "md":
		return "markdown"
	case "auto", "":
		// Auto-detect based on context (could check agent type in future)
		// For now, default to terminal rendering (not json or markdown)
		return "terminal"
	default:
		return "terminal"
	}
}

// runGroupedTriage runs bv with grouped output
func runGroupedTriage(dir string, byLabel, byTrack bool) error {
	adapter := tools.NewBVAdapter()
	output, err := adapter.GetGroupedTriage(context.Background(), dir, tools.BVGroupedTriageOptions{
		ByLabel: byLabel,
		ByTrack: byTrack,
	})
	if err != nil {
		return err
	}

	if jsonOutput {
		fmt.Println(string(output))
		return nil
	}

	// For non-JSON, just print the structured output
	fmt.Println(string(output))
	return nil
}

// renderTriage renders triage results in a human-friendly format
func renderTriage(triage *bv.TriageResponse, limit int, showQuick, showHealth bool) error {
	// Styles
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("75"))
	idStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	scoreStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	// Title
	fmt.Println()
	fmt.Println(titleStyle.Render("NTM Work Triage"))
	fmt.Println()

	// Quick ref
	qr := triage.Triage.QuickRef
	fmt.Printf("  Open: %d  Actionable: %d  Blocked: %d  In Progress: %d\n\n",
		qr.OpenCount, qr.ActionableCount, qr.BlockedCount, qr.InProgressCount)

	// Show quick wins or recommendations
	var items []bv.TriageRecommendation
	var sectionTitle string

	if showQuick && len(triage.Triage.QuickWins) > 0 {
		items = triage.Triage.QuickWins
		sectionTitle = "Quick Wins"
	} else {
		items = triage.Triage.Recommendations
		sectionTitle = "Top Recommendations"
	}

	if len(items) > limit {
		items = items[:limit]
	}

	fmt.Println(headerStyle.Render(sectionTitle + ":"))
	for i, rec := range items {
		// Score bar
		scoreBar := strings.Repeat("█", int(rec.Score*10))
		if len(scoreBar) == 0 {
			scoreBar = "▏"
		}

		fmt.Printf("  %d. %s %s %s\n",
			i+1,
			idStyle.Render(rec.ID),
			rec.Title,
			scoreStyle.Render(fmt.Sprintf("(%.2f)", rec.Score)))

		// Show reasons
		for _, reason := range rec.Reasons {
			fmt.Printf("     %s %s\n", mutedStyle.Render("→"), reason)
		}

		// Show action
		if rec.Action != "" {
			fmt.Printf("     %s\n", mutedStyle.Render(rec.Action))
		}
	}

	// Project health
	if showHealth && triage.Triage.ProjectHealth != nil {
		fmt.Println()
		fmt.Println(headerStyle.Render("Project Health:"))
		health := triage.Triage.ProjectHealth

		if len(health.StatusDistribution) > 0 {
			fmt.Print("  Status: ")
			for status, count := range health.StatusDistribution {
				fmt.Printf("%s=%d ", status, count)
			}
			fmt.Println()
		}

		if health.GraphMetrics != nil {
			gm := health.GraphMetrics
			fmt.Printf("  Graph: %d nodes, %d edges, density=%.3f\n",
				gm.TotalNodes, gm.TotalEdges, gm.Density)
		}
	}

	// Cache info
	if bv.IsCacheValid() {
		age := bv.GetCacheAge()
		fmt.Printf("\n%s\n", mutedStyle.Render(fmt.Sprintf("(cached %s ago)", age.Round(time.Second))))
	}

	fmt.Println()
	return nil
}

// Alert represents a bv alert
type Alert struct {
	Type     string   `json:"type"`
	Severity string   `json:"severity"`
	Message  string   `json:"message"`
	IssueID  string   `json:"issue_id,omitempty"`
	Labels   []string `json:"labels,omitempty"`
}

// AlertsResponse contains bv alerts
type AlertsResponse struct {
	Alerts []Alert `json:"alerts"`
}

// runWorkAlerts executes the alerts command
func runWorkAlerts(criticalOnly bool, alertType, labelFilter string) error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	severity := ""
	if criticalOnly {
		severity = "critical"
	}

	adapter := tools.NewBVAdapter()
	output, err := adapter.GetAlerts(context.Background(), dir, tools.BVAlertOptions{
		AlertType: alertType,
		Severity:  severity,
		Label:     labelFilter,
	})
	if err != nil {
		return err
	}

	if jsonOutput {
		fmt.Println(string(output))
		return nil
	}

	// Parse and render
	var resp AlertsResponse
	if err := json.Unmarshal(output, &resp); err != nil {
		// If parsing fails, just print raw output
		fmt.Println(string(output))
		return nil
	}

	return renderAlerts(resp.Alerts)
}

// renderAlerts renders alerts in a human-friendly format
func renderAlerts(alerts []Alert) error {
	if len(alerts) == 0 {
		fmt.Println("No alerts")
		return nil
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	criticalStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))
	warningStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	infoStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("75"))
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	fmt.Println()
	fmt.Println(titleStyle.Render("Alerts"))
	fmt.Println()

	// Group by severity
	critical := []Alert{}
	warning := []Alert{}
	info := []Alert{}

	for _, a := range alerts {
		switch a.Severity {
		case "critical":
			critical = append(critical, a)
		case "warning":
			warning = append(warning, a)
		default:
			info = append(info, a)
		}
	}

	printAlertGroup := func(label string, style lipgloss.Style, items []Alert) {
		if len(items) == 0 {
			return
		}
		fmt.Println(style.Render(fmt.Sprintf("%s (%d):", label, len(items))))
		for _, a := range items {
			icon := "•"
			if a.Severity == "critical" {
				icon = "✗"
			} else if a.Severity == "warning" {
				icon = "⚠"
			}
			fmt.Printf("  %s %s", icon, a.Message)
			if a.IssueID != "" {
				fmt.Printf(" %s", mutedStyle.Render("["+a.IssueID+"]"))
			}
			fmt.Println()
		}
		fmt.Println()
	}

	printAlertGroup("Critical", criticalStyle, critical)
	printAlertGroup("Warning", warningStyle, warning)
	printAlertGroup("Info", infoStyle, info)

	return nil
}

// SearchResult represents a search result from bv
type SearchResult struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	Score    float64 `json:"score"`
	Status   string  `json:"status"`
	Priority int     `json:"priority"`
	Snippet  string  `json:"snippet,omitempty"`
}

// SearchResponse contains bv search results
type SearchResponse struct {
	Query   string         `json:"query"`
	Results []SearchResult `json:"results"`
}

// runWorkSearch executes the search command
func runWorkSearch(query string, limit int, mode string) error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	adapter := tools.NewBVAdapter()
	output, err := adapter.GetSearchWithOptions(context.Background(), dir, tools.BVSearchOptions{
		Query: query,
		Limit: limit,
		Mode:  mode,
	})
	if err != nil {
		return err
	}

	if jsonOutput {
		fmt.Println(string(output))
		return nil
	}

	// Parse and render
	var resp SearchResponse
	if err := json.Unmarshal(output, &resp); err != nil {
		// If parsing fails, just print raw output
		fmt.Println(string(output))
		return nil
	}

	return renderSearchResults(query, resp.Results)
}

// renderSearchResults renders search results
func renderSearchResults(query string, results []SearchResult) error {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	idStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	scoreStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	fmt.Println()
	fmt.Printf("%s %s\n", titleStyle.Render("Search:"), query)
	fmt.Println()

	if len(results) == 0 {
		fmt.Println("No results found")
		return nil
	}

	for i, r := range results {
		status := mutedStyle.Render(fmt.Sprintf("[%s]", r.Status))
		priority := ""
		if r.Priority >= 0 {
			priority = fmt.Sprintf("P%d", r.Priority)
		}

		fmt.Printf("  %d. %s %s %s %s %s\n",
			i+1,
			idStyle.Render(r.ID),
			r.Title,
			status,
			priority,
			scoreStyle.Render(fmt.Sprintf("(%.2f)", r.Score)))

		if r.Snippet != "" {
			fmt.Printf("     %s\n", mutedStyle.Render(r.Snippet))
		}
	}

	fmt.Println()
	return nil
}

// ImpactResult represents an impact analysis result
type ImpactResult struct {
	File         string   `json:"file"`
	ImpactedIDs  []string `json:"impacted_ids"`
	TotalImpact  int      `json:"total_impact"`
	DirectImpact int      `json:"direct_impact"`
}

// ImpactResponse contains bv impact analysis
type ImpactResponse struct {
	Files       []ImpactResult `json:"files"`
	TotalBeads  int            `json:"total_beads"`
	UniqueBeads int            `json:"unique_beads"`
}

// runWorkImpact executes the impact command
func runWorkImpact(paths []string) error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	// Join paths with comma for bv
	pathArg := strings.Join(paths, ",")
	adapter := tools.NewBVAdapter()
	output, err := adapter.GetImpact(context.Background(), dir, pathArg)
	if err != nil {
		return err
	}

	if jsonOutput {
		fmt.Println(string(output))
		return nil
	}

	// Parse and render
	var resp ImpactResponse
	if err := json.Unmarshal(output, &resp); err != nil {
		// Try parsing as array of results
		var results []ImpactResult
		if err2 := json.Unmarshal(output, &results); err2 != nil {
			// If parsing fails, just print raw output
			fmt.Println(string(output))
			return nil
		}
		resp.Files = results
	}

	return renderImpactResults(paths, resp)
}

// renderImpactResults renders impact analysis
func renderImpactResults(paths []string, resp ImpactResponse) error {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	fileStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("75"))
	countStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	idStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	fmt.Println()
	fmt.Println(titleStyle.Render("Impact Analysis"))
	fmt.Println()

	if len(resp.Files) == 0 {
		fmt.Println("No impact detected for the specified paths")
		return nil
	}

	// Sort by impact
	sort.Slice(resp.Files, func(i, j int) bool {
		return resp.Files[i].TotalImpact > resp.Files[j].TotalImpact
	})

	for _, f := range resp.Files {
		fmt.Printf("  %s %s\n",
			fileStyle.Render(f.File),
			countStyle.Render(fmt.Sprintf("(%d beads impacted)", f.TotalImpact)))

		if len(f.ImpactedIDs) > 0 {
			// Show first few impacted beads
			shown := f.ImpactedIDs
			if len(shown) > 5 {
				shown = shown[:5]
			}
			ids := make([]string, len(shown))
			for i, id := range shown {
				ids[i] = idStyle.Render(id)
			}
			fmt.Printf("     %s", strings.Join(ids, ", "))
			if len(f.ImpactedIDs) > 5 {
				fmt.Printf(" %s", mutedStyle.Render(fmt.Sprintf("+%d more", len(f.ImpactedIDs)-5)))
			}
			fmt.Println()
		}
	}

	if resp.UniqueBeads > 0 {
		fmt.Printf("\n  Total: %d unique beads potentially impacted\n",
			resp.UniqueBeads)
	}

	fmt.Println()
	return nil
}

// runWorkNext shows the single top recommendation
func runWorkNext() error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	rec, err := bv.GetNextRecommendation(dir)
	if err != nil {
		return fmt.Errorf("getting next recommendation: %w", err)
	}

	if rec == nil {
		fmt.Println("No recommendations available")
		return nil
	}

	if jsonOutput {
		return outputJSON(rec)
	}

	// Render single recommendation
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	idStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	scoreStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	fmt.Println()
	fmt.Println(titleStyle.Render("Next Recommendation"))
	fmt.Println()

	fmt.Printf("  %s %s %s\n",
		idStyle.Render(rec.ID),
		rec.Title,
		scoreStyle.Render(fmt.Sprintf("(%.2f)", rec.Score)))

	fmt.Printf("  %s P%d  %s\n",
		mutedStyle.Render("Type:"), rec.Priority,
		mutedStyle.Render(rec.Status))

	if len(rec.Reasons) > 0 {
		fmt.Println()
		fmt.Println(mutedStyle.Render("  Why:"))
		for _, r := range rec.Reasons {
			fmt.Printf("    → %s\n", r)
		}
	}

	if rec.Action != "" {
		fmt.Println()
		fmt.Printf("  %s %s\n", mutedStyle.Render("Action:"), rec.Action)
	}

	// Show claim command
	fmt.Println()
	fmt.Printf("  %s bd update %s --status=in_progress\n",
		mutedStyle.Render("Claim:"), rec.ID)

	fmt.Println()
	return nil
}

// newWorkHistoryCmd creates the history command
func newWorkHistoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Show bead-to-commit correlations and milestones",
		Long: `Display history analysis showing how beads correlate with commits.

Shows bead events, commit milestones, and provides insights into development patterns.

Examples:
  ntm work history               # Full history analysis
  ntm work history --json       # Output as JSON`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkHistory()
		},
	}

	return cmd
}

// newWorkForecastCmd creates the forecast command
func newWorkForecastCmd() *cobra.Command {
	var issueID string

	cmd := &cobra.Command{
		Use:   "forecast [issue-id]",
		Short: "ETA predictions with dependency-aware scheduling",
		Long: `Predict completion times for issues using dependency analysis.

Uses graph analysis to provide realistic estimates considering dependencies.

Examples:
  ntm work forecast                # Forecast all open issues
  ntm work forecast ntm-123        # Forecast specific issue
  ntm work forecast --json         # Output as JSON`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				issueID = args[0]
			}
			return runWorkForecast(issueID)
		},
	}

	return cmd
}

// newWorkGraphCmd creates the graph command
func newWorkGraphCmd() *cobra.Command {
	var graphFormat string

	cmd := &cobra.Command{
		Use:   "graph",
		Short: "Export dependency graph visualization",
		Long: `Export the dependency graph in various formats.

Supports JSON, DOT (Graphviz), and Mermaid formats for visualization.

Examples:
  ntm work graph                           # JSON format
  ntm work graph --format=dot             # DOT format for Graphviz
  ntm work graph --format=mermaid         # Mermaid format
  ntm work graph --json                   # Alias for JSON format`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkGraph(graphFormat)
		},
	}

	cmd.Flags().StringVar(&graphFormat, "format", "json", "Graph format: json, dot, or mermaid")

	return cmd
}

// newWorkLabelHealthCmd creates the label-health command
func newWorkLabelHealthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "label-health",
		Short: "Health metrics per label",
		Long: `Show health metrics for each label including velocity, staleness, and blocked count.

Helps identify which areas of the project need attention.

Examples:
  ntm work label-health           # All label health metrics
  ntm work label-health --json    # Output as JSON`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkLabelHealth()
		},
	}

	return cmd
}

// newWorkLabelFlowCmd creates the label-flow command
func newWorkLabelFlowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "label-flow",
		Short: "Cross-label dependency flows and bottlenecks",
		Long: `Analyze dependencies between labels to identify bottlenecks.

Shows which labels depend on others and where work gets blocked.

Examples:
  ntm work label-flow             # Flow analysis between labels
  ntm work label-flow --json      # Output as JSON`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkLabelFlow()
		},
	}

	return cmd
}

// newWorkBurndownCmd creates the burndown command
func newWorkBurndownCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "burndown <sprint>",
		Short: "Sprint burndown with scope changes and at-risk items",
		Long: `Generate burndown charts and analysis for sprints.

Shows progress, scope changes, and identifies at-risk items.

Examples:
  ntm work burndown sprint-1      # Burndown for sprint-1
  ntm work burndown current       # Current sprint burndown
  ntm work burndown sprint-2 --json  # Output as JSON`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkBurndown(args[0])
		},
	}

	return cmd
}

// Response types for the new commands

// HistoryResponse contains bead-to-commit correlation data
type HistoryResponse struct {
	Stats       HistoryStats          `json:"stats"`
	Histories   []BeadHistory         `json:"histories"`
	CommitIndex map[string]CommitInfo `json:"commit_index"`
}

// HistoryStats contains overall history statistics
type HistoryStats struct {
	TotalBeads      int `json:"total_beads"`
	TotalCommits    int `json:"total_commits"`
	CorrelatedCount int `json:"correlated_count"`
}

// BeadHistory contains history for a single bead
type BeadHistory struct {
	ID         string      `json:"id"`
	Title      string      `json:"title"`
	Events     []BeadEvent `json:"events"`
	Commits    []string    `json:"commits"`
	Milestones []string    `json:"milestones"`
}

// BeadEvent represents a bead state change
type BeadEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Event     string    `json:"event"`
	Status    string    `json:"status,omitempty"`
}

// CommitInfo contains commit details
type CommitInfo struct {
	Hash      string    `json:"hash"`
	Timestamp time.Time `json:"timestamp"`
	Message   string    `json:"message"`
	Beads     []string  `json:"beads,omitempty"`
}

// ForecastResponse contains ETA predictions
type ForecastResponse struct {
	Forecasts []ForecastItem `json:"forecasts"`
}

// ForecastItem represents a forecast for a single issue
type ForecastItem struct {
	ID              string    `json:"id"`
	Title           string    `json:"title"`
	EstimatedETA    time.Time `json:"estimated_eta"`
	ConfidenceLevel float64   `json:"confidence_level"`
	DependencyCount int       `json:"dependency_count"`
	CriticalPath    bool      `json:"critical_path"`
	BlockingFactors []string  `json:"blocking_factors,omitempty"`
}

// GraphResponse contains dependency graph data
type GraphResponse struct {
	Format string      `json:"format"`
	Data   interface{} `json:"data"`
}

// LabelHealthResponse contains health metrics per label
type LabelHealthResponse struct {
	Results LabelHealthResults `json:"results"`
}

// LabelHealthResults contains the actual health data
type LabelHealthResults struct {
	Labels []LabelHealth `json:"labels"`
}

// LabelHealth contains health metrics for a single label
type LabelHealth struct {
	Label         string  `json:"label"`
	HealthLevel   string  `json:"health_level"` // healthy, warning, critical
	VelocityScore float64 `json:"velocity_score"`
	Staleness     float64 `json:"staleness"`
	BlockedCount  int     `json:"blocked_count"`
}

// LabelFlowResponse contains cross-label dependency analysis
type LabelFlowResponse struct {
	FlowMatrix       map[string]map[string]int `json:"flow_matrix"`
	Dependencies     []LabelDependency         `json:"dependencies"`
	BottleneckLabels []string                  `json:"bottleneck_labels"`
}

// LabelDependency represents a dependency between labels
type LabelDependency struct {
	From   string  `json:"from"`
	To     string  `json:"to"`
	Count  int     `json:"count"`
	Weight float64 `json:"weight"`
}

// BurndownResponse contains sprint burndown data
type BurndownResponse struct {
	Sprint       string           `json:"sprint"`
	Progress     BurndownProgress `json:"progress"`
	ScopeChanges []ScopeChange    `json:"scope_changes,omitempty"`
	AtRisk       []AtRiskItem     `json:"at_risk,omitempty"`
}

// BurndownProgress contains progress metrics
type BurndownProgress struct {
	TotalPoints     int     `json:"total_points"`
	CompletedPoints int     `json:"completed_points"`
	PercentComplete float64 `json:"percent_complete"`
	DaysRemaining   int     `json:"days_remaining"`
}

// ScopeChange represents a change in sprint scope
type ScopeChange struct {
	Timestamp time.Time `json:"timestamp"`
	Action    string    `json:"action"` // added, removed, modified
	IssueID   string    `json:"issue_id"`
	Points    int       `json:"points"`
}

// AtRiskItem represents an at-risk sprint item
type AtRiskItem struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Risk    string   `json:"risk"` // behind_schedule, blocked, scope_creep
	Reasons []string `json:"reasons"`
}

// Implementation functions for the new commands

// runWorkHistory executes the history command
func runWorkHistory() error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	adapter := tools.NewBVAdapter()
	output, err := adapter.GetHistory(context.Background(), dir)
	if err != nil {
		return err
	}

	if jsonOutput {
		fmt.Println(string(output))
		return nil
	}

	// Parse and render
	var resp HistoryResponse
	if err := json.Unmarshal(output, &resp); err != nil {
		// If parsing fails, just print raw output
		fmt.Println(string(output))
		return nil
	}

	return renderHistory(resp)
}

// renderHistory renders history data in a human-friendly format
func renderHistory(resp HistoryResponse) error {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	idStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	fmt.Println()
	fmt.Println(titleStyle.Render("Bead History & Correlation"))
	fmt.Println()

	// Stats
	stats := resp.Stats
	fmt.Printf("  Total Beads: %d  Commits: %d  Correlated: %d\n\n",
		stats.TotalBeads, stats.TotalCommits, stats.CorrelatedCount)

	// Recent bead histories (limit to first 10)
	histories := resp.Histories
	if len(histories) > 10 {
		histories = histories[:10]
	}

	for _, bead := range histories {
		fmt.Printf("  %s %s\n", idStyle.Render(bead.ID), bead.Title)

		if len(bead.Events) > 0 {
			fmt.Printf("    %s %d events, %d commits\n",
				mutedStyle.Render("Events:"), len(bead.Events), len(bead.Commits))
		}

		if len(bead.Milestones) > 0 {
			fmt.Printf("    %s %s\n",
				mutedStyle.Render("Milestones:"), strings.Join(bead.Milestones, ", "))
		}
		fmt.Println()
	}

	return nil
}

// runWorkForecast executes the forecast command
func runWorkForecast(issueID string) error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	target := "all"
	if issueID != "" {
		target = issueID
	}

	adapter := tools.NewBVAdapter()
	output, err := adapter.GetForecast(context.Background(), dir, target)
	if err != nil {
		return err
	}

	if jsonOutput {
		fmt.Println(string(output))
		return nil
	}

	// Parse and render
	var resp ForecastResponse
	if err := json.Unmarshal(output, &resp); err != nil {
		// If parsing fails, just print raw output
		fmt.Println(string(output))
		return nil
	}

	return renderForecast(resp)
}

// renderForecast renders forecast data
func renderForecast(resp ForecastResponse) error {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	idStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	dateStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	riskStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	fmt.Println()
	fmt.Println(titleStyle.Render("Issue Forecasts"))
	fmt.Println()

	if len(resp.Forecasts) == 0 {
		fmt.Println("No forecasts available")
		return nil
	}

	for i, forecast := range resp.Forecasts {
		if i >= 10 { // Limit display
			break
		}

		fmt.Printf("  %s %s\n", idStyle.Render(forecast.ID), forecast.Title)

		eta := forecast.EstimatedETA.Format("2006-01-02")
		confidence := fmt.Sprintf("%.0f%%", forecast.ConfidenceLevel*100)
		fmt.Printf("    %s %s %s %s\n",
			mutedStyle.Render("ETA:"), dateStyle.Render(eta),
			mutedStyle.Render("Confidence:"), confidence)

		if forecast.CriticalPath {
			fmt.Printf("    %s Critical path item\n", riskStyle.Render("⚠"))
		}

		if len(forecast.BlockingFactors) > 0 {
			fmt.Printf("    %s %s\n",
				mutedStyle.Render("Blocking:"), strings.Join(forecast.BlockingFactors, ", "))
		}
		fmt.Println()
	}

	return nil
}

// runWorkGraph executes the graph command
func runWorkGraph(format string) error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	adapter := tools.NewBVAdapter()
	output, err := adapter.GetGraph(context.Background(), dir, tools.BVGraphOptions{
		Format: format,
	})
	if err != nil {
		return err
	}

	if jsonOutput || format == "json" {
		fmt.Println(string(output))
		return nil
	}

	// For non-JSON formats like DOT or Mermaid, just print directly
	fmt.Println(string(output))
	return nil
}

// runWorkLabelHealth executes the label-health command
func runWorkLabelHealth() error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	adapter := tools.NewBVAdapter()
	output, err := adapter.GetLabelHealth(context.Background(), dir)
	if err != nil {
		return err
	}

	if jsonOutput {
		fmt.Println(string(output))
		return nil
	}

	// Parse and render
	var resp LabelHealthResponse
	if err := json.Unmarshal(output, &resp); err != nil {
		// If parsing fails, just print raw output
		fmt.Println(string(output))
		return nil
	}

	return renderLabelHealth(resp)
}

// renderLabelHealth renders label health data
func renderLabelHealth(resp LabelHealthResponse) error {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	healthyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warningStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	criticalStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	fmt.Println()
	fmt.Println(titleStyle.Render("Label Health"))
	fmt.Println()

	labels := resp.Results.Labels
	if len(labels) == 0 {
		fmt.Println("No label health data available")
		return nil
	}

	// Sort by health level (critical first)
	sort.Slice(labels, func(i, j int) bool {
		order := map[string]int{"critical": 0, "warning": 1, "healthy": 2}
		return order[labels[i].HealthLevel] < order[labels[j].HealthLevel]
	})

	for _, label := range labels {
		var healthStyle lipgloss.Style
		var icon string

		switch label.HealthLevel {
		case "critical":
			healthStyle = criticalStyle
			icon = "✗"
		case "warning":
			healthStyle = warningStyle
			icon = "⚠"
		default:
			healthStyle = healthyStyle
			icon = "✓"
		}

		fmt.Printf("  %s %s %s\n",
			icon, label.Label, healthStyle.Render(label.HealthLevel))

		fmt.Printf("    %s %.2f  %s %.2f  %s %d\n",
			mutedStyle.Render("Velocity:"), label.VelocityScore,
			mutedStyle.Render("Staleness:"), label.Staleness,
			mutedStyle.Render("Blocked:"), label.BlockedCount)
		fmt.Println()
	}

	return nil
}

// runWorkLabelFlow executes the label-flow command
func runWorkLabelFlow() error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	adapter := tools.NewBVAdapter()
	output, err := adapter.GetLabelFlow(context.Background(), dir)
	if err != nil {
		return err
	}

	if jsonOutput {
		fmt.Println(string(output))
		return nil
	}

	// Parse and render
	var resp LabelFlowResponse
	if err := json.Unmarshal(output, &resp); err != nil {
		// If parsing fails, just print raw output
		fmt.Println(string(output))
		return nil
	}

	return renderLabelFlow(resp)
}

// renderLabelFlow renders label flow data
func renderLabelFlow(resp LabelFlowResponse) error {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	bottleneckStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))

	fmt.Println()
	fmt.Println(titleStyle.Render("Label Flow Analysis"))
	fmt.Println()

	// Show bottleneck labels first
	if len(resp.BottleneckLabels) > 0 {
		fmt.Printf("  %s\n", bottleneckStyle.Render("Bottleneck Labels:"))
		for _, label := range resp.BottleneckLabels {
			fmt.Printf("    ⚠ %s\n", label)
		}
		fmt.Println()
	}

	// Show top dependencies
	fmt.Printf("  %s\n", mutedStyle.Render("Top Dependencies:"))

	// Sort dependencies by count (descending)
	deps := resp.Dependencies
	sort.Slice(deps, func(i, j int) bool {
		return deps[i].Count > deps[j].Count
	})

	count := 0
	for _, dep := range deps {
		if count >= 10 { // Limit display
			break
		}
		fmt.Printf("    %s → %s %s\n",
			labelStyle.Render(dep.From),
			labelStyle.Render(dep.To),
			mutedStyle.Render(fmt.Sprintf("(%d)", dep.Count)))
		count++
	}

	return nil
}

// runWorkBurndown executes the burndown command
func runWorkBurndown(sprint string) error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	adapter := tools.NewBVAdapter()
	output, err := adapter.GetBurndown(context.Background(), dir, sprint)
	if err != nil {
		return err
	}

	if jsonOutput {
		fmt.Println(string(output))
		return nil
	}

	// Parse and render
	var resp BurndownResponse
	if err := json.Unmarshal(output, &resp); err != nil {
		// If parsing fails, just print raw output
		fmt.Println(string(output))
		return nil
	}

	return renderBurndown(resp)
}

// renderBurndown renders burndown data
func renderBurndown(resp BurndownResponse) error {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	progressStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	riskStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	fmt.Println()
	fmt.Printf("%s %s\n", titleStyle.Render("Sprint Burndown:"), resp.Sprint)
	fmt.Println()

	// Progress
	progress := resp.Progress
	fmt.Printf("  %s %d/%d points %s\n",
		mutedStyle.Render("Progress:"),
		progress.CompletedPoints,
		progress.TotalPoints,
		progressStyle.Render(fmt.Sprintf("(%.0f%%)", progress.PercentComplete)))

	fmt.Printf("  %s %d days\n\n",
		mutedStyle.Render("Remaining:"), progress.DaysRemaining)

	// At-risk items
	if len(resp.AtRisk) > 0 {
		fmt.Printf("  %s\n", riskStyle.Render("At Risk:"))
		for _, item := range resp.AtRisk {
			fmt.Printf("    ⚠ %s - %s\n", item.ID, item.Title)
			if len(item.Reasons) > 0 {
				fmt.Printf("      %s %s\n",
					mutedStyle.Render("Reason:"), strings.Join(item.Reasons, ", "))
			}
		}
		fmt.Println()
	}

	// Scope changes (show recent ones)
	if len(resp.ScopeChanges) > 0 {
		fmt.Printf("  %s\n", mutedStyle.Render("Recent Scope Changes:"))
		count := 0
		for _, change := range resp.ScopeChanges {
			if count >= 5 { // Limit display
				break
			}
			fmt.Printf("    %s %s %s (%d pts)\n",
				change.Timestamp.Format("01/02"),
				change.Action,
				change.IssueID,
				change.Points)
			count++
		}
	}

	return nil
}

// outputJSON outputs data as JSON
func outputJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
