// Package cli provides command-line interface commands for ntm.
// work.go implements the `ntm work` command for intelligent work distribution.
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/bv"
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
	var args []string
	if byLabel {
		args = append(args, "-robot-triage-by-label")
	} else if byTrack {
		args = append(args, "-robot-triage-by-track")
	}

	output, err := bv.RunRaw(dir, args...)
	if err != nil {
		return err
	}

	if jsonOutput {
		fmt.Println(output)
		return nil
	}

	// For non-JSON, just print the structured output
	fmt.Println(output)
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

	args := []string{"-robot-alerts"}

	if alertType != "" {
		args = append(args, "-alert-type", alertType)
	}
	if labelFilter != "" {
		args = append(args, "-alert-label", labelFilter)
	}
	if criticalOnly {
		args = append(args, "-severity", "critical")
	}

	output, err := bv.RunRaw(dir, args...)
	if err != nil {
		return err
	}

	if jsonOutput {
		fmt.Println(output)
		return nil
	}

	// Parse and render
	var resp AlertsResponse
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		// If parsing fails, just print raw output
		fmt.Println(output)
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

	args := []string{"-robot-search", "-search", query}
	if limit > 0 {
		args = append(args, "-search-limit", fmt.Sprintf("%d", limit))
	}
	if mode != "" {
		args = append(args, "-search-mode", mode)
	}

	output, err := bv.RunRaw(dir, args...)
	if err != nil {
		return err
	}

	if jsonOutput {
		fmt.Println(output)
		return nil
	}

	// Parse and render
	var resp SearchResponse
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		// If parsing fails, just print raw output
		fmt.Println(output)
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
	args := []string{"-robot-impact", pathArg}

	output, err := bv.RunRaw(dir, args...)
	if err != nil {
		return err
	}

	if jsonOutput {
		fmt.Println(output)
		return nil
	}

	// Parse and render
	var resp ImpactResponse
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		// Try parsing as array of results
		var results []ImpactResult
		if err2 := json.Unmarshal([]byte(output), &results); err2 != nil {
			// If parsing fails, just print raw output
			fmt.Println(output)
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

// outputJSON outputs data as JSON
func outputJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
