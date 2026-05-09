// Package cli provides command-line interface commands for ntm.
// work.go implements the `ntm work` command for intelligent work distribution.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/commitlint"
	ideaplan "github.com/Dicklesworthstone/ntm/internal/ideation"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot/assurance"
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
	cmd.AddCommand(newWorkQueueDryCmd())
	cmd.AddCommand(newWorkCommitReadyCmd())
	cmd.AddCommand(newWorkHistoryCmd())
	cmd.AddCommand(newWorkForecastCmd())
	cmd.AddCommand(newWorkGraphCmd())
	cmd.AddCommand(newWorkLabelHealthCmd())
	cmd.AddCommand(newWorkLabelFlowCmd())
	cmd.AddCommand(newWorkBurndownCmd())

	return cmd
}

func newWorkCommitReadyCmd() *cobra.Command {
	var (
		format         string
		agentName      string
		syncLagMinutes int
	)

	cmd := &cobra.Command{
		Use:     "commit-ready",
		Aliases: []string{"commit-readiness"},
		Short:   "Check whether coordination state is safe for commit or handoff",
		Long: `Check whether local git, Beads, Agent Mail, and reservation state are safe for commit or handoff.

This command is advisory-only. It does not mutate files, claim beads, send mail, or release reservations.

Examples:
  ntm work commit-ready
  ntm work commit-ready --format=json --agent=YellowBluff`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkCommitReady(format, agentName, syncLagMinutes)
		},
	}

	cmd.Flags().StringVar(&format, "format", "", "Output format: text or json")
	cmd.Flags().StringVar(&agentName, "agent", "", "Agent Mail identity to verify reservations and urgent inbox")
	cmd.Flags().IntVar(&syncLagMinutes, "sync-lag-minutes", 10, "Allowable mtime lag between .beads/beads.db and .beads/issues.jsonl")

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

func newWorkQueueDryCmd() *cobra.Command {
	var (
		format          string
		staleHours      int
		commitLimit     int
		syncLagMinutes  int
		maxStaleEntries int
		ideate          bool
		force           bool
		includeNextBest bool
		createBeads     bool
		confirmCreate   bool
		planVersion     string
	)

	cmd := &cobra.Command{
		Use:   "queue-dry",
		Short: "Diagnose why no ready work is available and suggest safe next steps",
		Long: `Diagnose queue-dry situations with evidence from br, bv, sync state, and locks.

This command is advisory-only. It does not claim, reopen, or create beads automatically.

Examples:
  ntm work queue-dry
  ntm work queue-dry --format=json
  ntm work queue-dry --ideate --format=markdown
  ntm work queue-dry --ideate --create-beads --yes
  ntm work queue-dry --ideate --force --format=json
  ntm work queue-dry --stale-hours=24 --commits=5`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkQueueDry(format, staleHours, commitLimit, syncLagMinutes, maxStaleEntries, QueueDryIdeationOptions{
				Requested:       ideate || createBeads,
				Force:           force,
				IncludeNextBest: includeNextBest,
				CreateBeads:     createBeads,
				ConfirmCreate:   confirmCreate,
				PlanVersion:     planVersion,
			})
		},
	}

	cmd.Flags().StringVar(&format, "format", "", "Output format: text, json, or markdown")
	cmd.Flags().IntVar(&staleHours, "stale-hours", 24, "Mark in_progress beads older than this many hours as stale")
	cmd.Flags().IntVar(&commitLimit, "commits", 5, "Number of recent commits to include in evidence")
	cmd.Flags().IntVar(&syncLagMinutes, "sync-lag-minutes", 10, "Allowable mtime lag between .beads/beads.db and .beads/issues.jsonl")
	cmd.Flags().IntVar(&maxStaleEntries, "max-stale", 10, "Maximum stale in_progress beads to include")
	cmd.Flags().BoolVar(&ideate, "ideate", false, "Render duplicate-aware queue-dry ideation as a dry-run roadmap")
	cmd.Flags().BoolVar(&force, "force", false, "Preview ideation even when ready work exists or queue state is unverified")
	cmd.Flags().BoolVar(&includeNextBest, "include-next-best", false, "Include next-best candidates after the top five in the dry-run roadmap")
	cmd.Flags().BoolVar(&createBeads, "create-beads", false, "Create proposed beads through br after preview gates pass")
	cmd.Flags().BoolVar(&confirmCreate, "yes", false, "Confirm mutating bead creation for --create-beads")
	cmd.Flags().StringVar(&planVersion, "plan-version", "", "Plan version or review token recorded in creation output")

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
			switch a.Severity {
			case "critical":
				icon = "✗"
			case "warning":
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
	fmt.Printf("  %s br update %s --status=in_progress\n",
		mutedStyle.Render("Claim:"), rec.ID)

	fmt.Println()
	return nil
}

// QueueDryResponse captures queue-dry diagnostic data.
type QueueDryResponse struct {
	output.TimestampedResponse
	Success         bool                           `json:"success"`
	Project         string                         `json:"project"`
	QueueDry        bool                           `json:"queue_dry"`
	Evidence        QueueDryEvidence               `json:"evidence"`
	Quiescence      assurance.QuiescenceAssessment `json:"quiescence"`
	Ideation        *QueueDryIdeationReport        `json:"ideation,omitempty"`
	Recommendations []QueueDryRecommendation       `json:"recommendations"`
	Recipes         []QueueDryRecipe               `json:"recipes"`
	Warnings        []string                       `json:"warnings,omitempty"`
	Errors          []string                       `json:"errors,omitempty"`
}

// QueueDryIdeationOptions controls the advisory ideation dry-run path.
type QueueDryIdeationOptions struct {
	Requested       bool
	Force           bool
	IncludeNextBest bool
	CreateBeads     bool
	ConfirmCreate   bool
	PlanVersion     string
}

// QueueDryIdeationReport embeds the bd-e7xm1 dry-run planning pipeline in queue-dry output.
type QueueDryIdeationReport struct {
	Requested     bool                               `json:"requested"`
	DryRun        bool                               `json:"dry_run"`
	Forced        bool                               `json:"forced,omitempty"`
	Status        string                             `json:"status"`
	Reason        string                             `json:"reason"`
	Snapshot      *ideaplan.IdeaEvidenceSnapshot     `json:"snapshot,omitempty"`
	Ranking       *ideaplan.RankingResult            `json:"ranking,omitempty"`
	Guard         *ideaplan.NoveltyGuardAssessment   `json:"guard,omitempty"`
	Effectiveness *ideaplan.EffectivenessReport      `json:"effectiveness,omitempty"`
	Refinement    *ideaplan.CreationRefinementReport `json:"refinement,omitempty"`
	Roadmap       *ideaplan.RoadmapPlan              `json:"roadmap,omitempty"`
	Creation      *ideaplan.BeadCreationReport       `json:"creation,omitempty"`
	NextActions   []QueueDryRecommendation           `json:"next_actions"`
	Warnings      []string                           `json:"warnings,omitempty"`
}

// QueueDryEvidence stores collected evidence for queue-dry analysis.
type QueueDryEvidence struct {
	OpenCount          int                  `json:"open_count"`
	ActionableCount    int                  `json:"actionable_count"`
	BlockedCount       int                  `json:"blocked_count"`
	InProgressCount    int                  `json:"in_progress_count"`
	ReadyCount         int                  `json:"ready_count"`
	CountsVerified     bool                 `json:"counts_verified"`
	TriageTopIDs       []string             `json:"triage_top_ids,omitempty"`
	StaleInProgress    []QueueDryStaleIssue `json:"stale_in_progress,omitempty"`
	Sync               QueueDrySyncStatus   `json:"sync"`
	Reservations       QueueDryReservations `json:"reservations"`
	RecentCommits      []QueueDryCommit     `json:"recent_commits,omitempty"`
	BeadsSummaryReason string               `json:"beads_summary_reason,omitempty"`
	TriageError        string               `json:"triage_error,omitempty"`
}

// QueueDryStaleIssue represents one stale in-progress bead candidate.
type QueueDryStaleIssue struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Assignee  string    `json:"assignee,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	AgeHours  int       `json:"age_hours"`
}

// QueueDrySyncStatus reports DB/JSONL synchronization state.
type QueueDrySyncStatus struct {
	HasLocalBeadsDB      bool       `json:"has_local_beads_db"`
	IssuesJSONLExists    bool       `json:"issues_jsonl_exists"`
	BeadsDBExists        bool       `json:"beads_db_exists"`
	IssuesJSONLUpdatedAt *time.Time `json:"issues_jsonl_updated_at,omitempty"`
	BeadsDBUpdatedAt     *time.Time `json:"beads_db_updated_at,omitempty"`
	AgeDeltaSeconds      int64      `json:"age_delta_seconds,omitempty"`
	Status               string     `json:"status"`
	NeedsFlush           bool       `json:"needs_flush"`
}

// QueueDryReservations reports active reservation metadata.
type QueueDryReservations struct {
	Available bool     `json:"available"`
	Count     int      `json:"count"`
	Holders   []string `json:"holders,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// QueueDryCommit stores a compact git commit for operator context.
type QueueDryCommit struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
}

// QueueDryRecommendation is an advisory-only next step.
type QueueDryRecommendation struct {
	Code     string `json:"code"`
	Summary  string `json:"summary"`
	Command  string `json:"command,omitempty"`
	Evidence string `json:"evidence,omitempty"`
}

// QueueDryRecipe lists a known operator loop recipe.
type QueueDryRecipe struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Purpose string `json:"purpose"`
}

// CommitReadyResponse captures advisory pre-commit coordination evidence.
type CommitReadyResponse struct {
	output.TimestampedResponse
	Success      bool                 `json:"success"`
	Project      string               `json:"project"`
	Agent        string               `json:"agent,omitempty"`
	SafeToCommit bool                 `json:"safe_to_commit"`
	Summary      commitlint.Summary   `json:"summary"`
	Findings     []commitlint.Finding `json:"findings"`
	Notes        []string             `json:"notes,omitempty"`
	Evidence     CommitReadyEvidence  `json:"evidence"`
	Warnings     []string             `json:"warnings,omitempty"`
	Errors       []string             `json:"errors,omitempty"`
}

// CommitReadyEvidence stores the raw evidence used by commit-ready.
type CommitReadyEvidence struct {
	Git          CommitReadyGitEvidence         `json:"git"`
	Sync         QueueDrySyncStatus             `json:"sync"`
	Reservations CommitReadyReservationEvidence `json:"reservations"`
	Mail         CommitReadyMailEvidence        `json:"mail"`
}

// CommitReadyGitEvidence summarizes local git state relevant to a commit.
type CommitReadyGitEvidence struct {
	Available       bool     `json:"available"`
	Dirty           bool     `json:"dirty"`
	ChangedFiles    []string `json:"changed_files,omitempty"`
	StagedFiles     []string `json:"staged_files,omitempty"`
	ModifiedFiles   []string `json:"modified_files,omitempty"`
	UntrackedFiles  []string `json:"untracked_files,omitempty"`
	ConflictedFiles []string `json:"conflicted_files,omitempty"`
	Error           string   `json:"error,omitempty"`
}

// CommitReadyReservationEvidence reports whether changed files are reservation-covered.
type CommitReadyReservationEvidence struct {
	Available bool     `json:"available"`
	Count     int      `json:"count"`
	Holders   []string `json:"holders,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// CommitReadyMailEvidence summarizes urgent or acknowledgement-required inbox state.
type CommitReadyMailEvidence struct {
	Available        bool   `json:"available"`
	Agent            string `json:"agent,omitempty"`
	CheckedCount     int    `json:"checked_count"`
	UrgentCount      int    `json:"urgent_count"`
	AckRequiredCount int    `json:"ack_required_count"`
	Error            string `json:"error,omitempty"`
}

const (
	queueDryReservationTimeout = 2 * time.Second
	queueDryTriageTimeout      = 2 * time.Second
)

var queueDryGetTriage = func(dir string) (*bv.TriageResponse, error) {
	return bv.GetTriageWithTimeout(dir, queueDryTriageTimeout)
}

var queueDryCollectOptional = func(ctx context.Context, snapshot *ideaplan.IdeaEvidenceSnapshot, opts ideaplan.OptionalAdapterOptions) {
	ideaplan.Collector{}.CollectOptional(ctx, snapshot, opts)
}

func runWorkCommitReady(format string, agentName string, syncLagMinutes int) error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	if syncLagMinutes < 1 {
		syncLagMinutes = 1
	}

	report := collectCommitReadyReport(dir, agentName, time.Now().UTC(), time.Duration(syncLagMinutes)*time.Minute)
	outputMode := strings.ToLower(strings.TrimSpace(format))
	if outputMode == "" && jsonOutput {
		outputMode = "json"
	}
	if outputMode == "json" {
		return outputJSON(report)
	}
	return renderCommitReady(report)
}

func collectCommitReadyReport(dir string, agentName string, now time.Time, syncLagThreshold time.Duration) CommitReadyResponse {
	agentName = resolveCommitReadyAgentName(agentName)
	report := CommitReadyResponse{
		TimestampedResponse: output.NewTimestamped(),
		Success:             true,
		Project:             dir,
		Agent:               agentName,
	}

	gitInfo, gitErr := getGitInfo(dir)
	if gitErr != nil {
		report.Evidence.Git = CommitReadyGitEvidence{
			Available: false,
			Error:     gitErr.Error(),
		}
	} else {
		changedFiles := commitReadyChangedFiles(gitInfo)
		report.Evidence.Git = CommitReadyGitEvidence{
			Available:       true,
			Dirty:           gitInfo.Dirty || len(changedFiles) > 0,
			ChangedFiles:    changedFiles,
			StagedFiles:     sortedUniqueStrings(gitInfo.StagedFiles),
			ModifiedFiles:   sortedUniqueStrings(gitInfo.ModifiedFiles),
			UntrackedFiles:  sortedUniqueStrings(gitInfo.UntrackedFiles),
			ConflictedFiles: sortedUniqueStrings(gitInfo.ConflictedFiles),
		}
	}

	report.Evidence.Sync = evaluateQueueDrySync(dir, syncLagThreshold)
	reservations, reservationsEvidence := collectCommitReadyReservations(dir, len(report.Evidence.Git.ChangedFiles) > 0 || agentName != "")
	report.Evidence.Reservations = reservationsEvidence
	inbox, mailEvidence := collectCommitReadyMail(dir, agentName)
	report.Evidence.Mail = mailEvidence

	lintReport := commitlint.Evaluate(commitlint.Inputs{
		AgentName:                 agentName,
		TouchedPaths:              report.Evidence.Git.ChangedFiles,
		Reservations:              reservations,
		Inbox:                     inbox,
		Sync:                      commitReadySyncView(report.Evidence.Sync),
		StaleReservationThreshold: commitlint.DefaultStaleReservationThreshold,
		Now:                       now,
	})
	applyCommitLintReport(&report, lintReport)
	if gitErr != nil {
		report.Success = false
		appendCommitReadyFinding(&report, commitlint.Finding{
			Code:        "git_unavailable",
			Severity:    commitlint.SeverityCritical,
			Summary:     "git state could not be inspected",
			Remediation: "run this command from a git worktree before committing",
			Evidence:    evidenceList(report.Evidence.Git.Error),
		})
	}
	if len(report.Evidence.Git.ConflictedFiles) > 0 {
		appendCommitReadyFinding(&report, commitlint.Finding{
			Code:        "git_conflicts",
			Severity:    commitlint.SeverityCritical,
			Summary:     "unresolved git conflicts are present",
			Remediation: "resolve conflicted files before committing",
			Evidence:    report.Evidence.Git.ConflictedFiles,
		})
	}
	if reservationsEvidence.Error != "" && (len(report.Evidence.Git.ChangedFiles) > 0 || agentName != "") {
		appendCommitReadyFinding(&report, commitlint.Finding{
			Code:        "agent_mail_unavailable",
			Severity:    commitlint.SeverityCritical,
			Summary:     "Agent Mail reservations could not be verified",
			Remediation: "retry after Agent Mail recovers, or verify reservations manually before committing",
			Evidence:    evidenceList(reservationsEvidence.Error),
		})
	}
	if mailEvidence.Error != "" && agentName != "" && !commitReadyHasFindingCode(report.Findings, "agent_mail_unavailable") {
		appendCommitReadyFinding(&report, commitlint.Finding{
			Code:        "agent_mail_unavailable",
			Severity:    commitlint.SeverityCritical,
			Summary:     "Agent Mail inbox could not be checked",
			Remediation: "retry after Agent Mail recovers, or inspect urgent/ack-required mail manually",
			Evidence:    evidenceList(mailEvidence.Error),
		})
	}
	return report
}

func resolveCommitReadyAgentName(agentName string) string {
	agentName = strings.TrimSpace(agentName)
	if agentName != "" {
		return agentName
	}
	return strings.TrimSpace(os.Getenv("AGENT_NAME"))
}

func commitReadyChangedFiles(info *GitInfo) []string {
	if info == nil {
		return nil
	}
	files := make([]string, 0, len(info.StagedFiles)+len(info.ModifiedFiles)+len(info.UntrackedFiles)+len(info.ConflictedFiles))
	files = append(files, info.StagedFiles...)
	files = append(files, info.ModifiedFiles...)
	files = append(files, info.UntrackedFiles...)
	files = append(files, info.ConflictedFiles...)
	return sortedUniqueStrings(files)
}

func collectCommitReadyReservations(projectDir string, needed bool) ([]commitlint.ReservationView, CommitReadyReservationEvidence) {
	var evidence CommitReadyReservationEvidence
	if !needed {
		return nil, evidence
	}
	client := newAgentMailClient(projectDir)
	if !client.IsAvailable() {
		evidence.Error = "Agent Mail server unavailable"
		return nil, evidence
	}

	ctx, cancel := context.WithTimeout(context.Background(), queueDryReservationTimeout)
	defer cancel()

	reservations, err := fetchActiveReservations(ctx, client, projectDir, "", true)
	if err != nil {
		evidence.Error = err.Error()
		return nil, evidence
	}

	evidence.Available = true
	evidence.Count = len(reservations)
	holderSet := make(map[string]struct{})
	views := make([]commitlint.ReservationView, 0, len(reservations))
	for _, r := range reservations {
		if strings.TrimSpace(r.AgentName) != "" {
			holderSet[r.AgentName] = struct{}{}
		}
		views = append(views, commitlint.ReservationView{
			ID:          r.ID,
			PathPattern: r.PathPattern,
			AgentName:   r.AgentName,
			Exclusive:   r.Exclusive,
			CreatedAt:   r.CreatedTS.UTC(),
			ExpiresAt:   r.ExpiresTS.UTC(),
		})
	}
	for holder := range holderSet {
		evidence.Holders = append(evidence.Holders, holder)
	}
	sort.Strings(evidence.Holders)
	return views, evidence
}

func collectCommitReadyMail(projectDir string, agentName string) ([]commitlint.InboxView, CommitReadyMailEvidence) {
	evidence := CommitReadyMailEvidence{
		Agent: agentName,
	}
	if agentName == "" {
		return nil, evidence
	}

	client := newAgentMailClient(projectDir)
	if !client.IsAvailable() {
		evidence.Error = "Agent Mail server unavailable"
		return nil, evidence
	}

	ctx, cancel := context.WithTimeout(context.Background(), queueDryReservationTimeout)
	defer cancel()

	messages, err := client.FetchInbox(ctx, agentmail.FetchInboxOptions{
		ProjectKey:    projectDir,
		AgentName:     agentName,
		UrgentOnly:    true,
		Limit:         50,
		IncludeBodies: false,
	})
	if err != nil {
		evidence.Error = err.Error()
		return nil, evidence
	}

	evidence.Available = true
	evidence.CheckedCount = len(messages)
	views := make([]commitlint.InboxView, 0, len(messages))
	for _, msg := range messages {
		importance := strings.ToLower(strings.TrimSpace(msg.Importance))
		if importance == "urgent" || importance == "high" {
			evidence.UrgentCount++
		}
		if msg.AckRequired {
			evidence.AckRequiredCount++
		}
		var readAt *time.Time
		if msg.ReadAt != nil {
			t := msg.ReadAt.UTC()
			readAt = &t
		}
		views = append(views, commitlint.InboxView{
			ID:          msg.ID,
			Subject:     msg.Subject,
			From:        msg.From,
			Importance:  msg.Importance,
			AckRequired: msg.AckRequired,
			ReadAt:      readAt,
		})
	}
	return views, evidence
}

func commitReadySyncView(status QueueDrySyncStatus) commitlint.SyncView {
	return commitlint.SyncView{
		HasLocalBeadsDB: status.HasLocalBeadsDB,
		NeedsFlush:      status.NeedsFlush,
		Status:          status.Status,
	}
}

func applyCommitLintReport(report *CommitReadyResponse, lintReport commitlint.Report) {
	if report == nil {
		return
	}
	report.SafeToCommit = lintReport.SafeToCommit
	report.Summary = lintReport.Summary
	report.Findings = append([]commitlint.Finding(nil), lintReport.Findings...)
	report.Notes = append([]string(nil), lintReport.Notes...)
	for _, finding := range report.Findings {
		appendCommitReadyStatus(report, finding)
	}
}

func appendCommitReadyFinding(report *CommitReadyResponse, finding commitlint.Finding) {
	if report == nil {
		return
	}
	report.Findings = append(report.Findings, finding)
	switch finding.Severity {
	case commitlint.SeverityCritical:
		report.Summary.Critical++
		report.SafeToCommit = false
	case commitlint.SeverityWarning:
		report.Summary.Warning++
	case commitlint.SeverityInfo:
		report.Summary.Info++
	}
	appendCommitReadyStatus(report, finding)
}

func appendCommitReadyStatus(report *CommitReadyResponse, finding commitlint.Finding) {
	switch finding.Severity {
	case commitlint.SeverityCritical:
		report.Errors = append(report.Errors, finding.Code+": "+finding.Summary)
	case commitlint.SeverityWarning:
		report.Warnings = append(report.Warnings, finding.Code+": "+finding.Summary)
	}
}

func commitReadyHasFindingCode(findings []commitlint.Finding, code string) bool {
	for _, finding := range findings {
		if strings.Compare(finding.Code, code) == 0 {
			return true
		}
	}
	return false
}

func evidenceList(items ...string) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			result = append(result, item)
		}
	}
	return result
}

func sortedUniqueStrings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		item = normalizeCommitReadyPath(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	sort.Strings(result)
	return result
}

func normalizeCommitReadyPath(value string) string {
	value = filepath.ToSlash(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "./")
	return strings.Trim(value, "/")
}

func runWorkQueueDry(format string, staleHours, commitLimit, syncLagMinutes, maxStaleEntries int, ideationOpts QueueDryIdeationOptions) error {
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	if staleHours < 1 {
		staleHours = 1
	}
	if commitLimit < 0 {
		commitLimit = 0
	}
	if syncLagMinutes < 1 {
		syncLagMinutes = 1
	}
	if maxStaleEntries < 1 {
		maxStaleEntries = 1
	}

	report := collectQueueDryReport(dir, time.Now().UTC(), time.Duration(staleHours)*time.Hour, commitLimit, time.Duration(syncLagMinutes)*time.Minute, maxStaleEntries)
	if ideationOpts.Requested {
		ideationReport := collectQueueDryIdeationReport(dir, report, ideationOpts)
		report.Ideation = &ideationReport
	}

	outputMode := strings.ToLower(strings.TrimSpace(format))
	if outputMode == "" && jsonOutput {
		outputMode = "json"
	}
	if outputMode == "json" {
		return outputJSON(report)
	}
	if outputMode == "markdown" || outputMode == "md" {
		return renderQueueDryMarkdown(report)
	}
	return renderQueueDry(report)
}

func collectQueueDryIdeationReport(dir string, report QueueDryResponse, opts QueueDryIdeationOptions) QueueDryIdeationReport {
	if !opts.Force && !report.QueueDry {
		return skippedQueueDryIdeationReport(report, opts)
	}

	snapshot := ideaplan.CollectLocalEvidence(context.Background(), ideaplan.CollectorOptions{
		ProjectDir: dir,
	})
	annotateQueueDryOptionalSources(&snapshot, report)
	collectQueueDryOptionalSignals(context.Background(), &snapshot, dir)
	return buildQueueDryIdeationReport(report, snapshot, opts)
}

func skippedQueueDryIdeationReport(report QueueDryResponse, opts QueueDryIdeationOptions) QueueDryIdeationReport {
	status := "skipped_unverified_queue"
	reason := "queue state is unverified; use --force only if a human explicitly wants an ideation preview"
	if report.Evidence.CountsVerified {
		status = "skipped_ready_work"
		reason = "ready or actionable work exists; work that queue before ideating, or pass --force for a preview"
	}
	return QueueDryIdeationReport{
		Requested:   true,
		DryRun:      !opts.CreateBeads,
		Forced:      opts.Force,
		Status:      status,
		Reason:      reason,
		NextActions: append([]QueueDryRecommendation(nil), report.Recommendations...),
		Warnings:    sortedUniqueStrings(report.Warnings),
	}
}

func annotateQueueDryOptionalSources(snapshot *ideaplan.IdeaEvidenceSnapshot, report QueueDryResponse) {
	if snapshot == nil {
		return
	}
	agentMail := ideaplan.CandidateSource{
		ID:        "agent_mail:reservations",
		Kind:      ideaplan.SourceAgentMail,
		Available: report.Evidence.Reservations.Available,
		Required:  false,
		Evidence:  []string{"Agent Mail reservation visibility for queue-dry ideation"},
	}
	if !agentMail.Available {
		agentMail.Error = strings.TrimSpace(report.Evidence.Reservations.Error)
		if agentMail.Error == "" {
			agentMail.Error = "Agent Mail server unavailable"
		}
	}
	snapshot.RecordSource(agentMail)
}

func collectQueueDryOptionalSignals(ctx context.Context, snapshot *ideaplan.IdeaEvidenceSnapshot, projectDir string) {
	if snapshot == nil {
		return
	}
	queueDryCollectOptional(ctx, snapshot, ideaplan.OptionalAdapterOptions{
		ProjectDir:     projectDir,
		CommandTimeout: 6 * time.Second,
		CASSQueries:    []string{"queue-dry ideation"},
		CMQuery:        "queue-dry ideation",
	})
}

func buildQueueDryIdeationReport(report QueueDryResponse, snapshot ideaplan.IdeaEvidenceSnapshot, opts QueueDryIdeationOptions) QueueDryIdeationReport {
	ranking := ideaplan.RankCandidates(snapshot, ideaplan.DefaultRankOptions())
	guard := ideaplan.AssessNoveltyGuard(snapshot, ranking, ideaplan.NoveltyGuardOptions{
		ReservationStateUnknown: !report.Evidence.Reservations.Available,
		ActiveReservationCount:  report.Evidence.Reservations.Count,
		StaleInProgressIDs:      queueDryStaleIDs(report.Evidence.StaleInProgress),
	})
	roadmap := ideaplan.RenderRoadmap(ranking, ideaplan.RoadmapRenderOptions{
		PlanID:          "queue-dry-ideation-dry-run",
		ParentID:        "bd-e7xm1",
		IncludeNextBest: opts.IncludeNextBest,
		VerificationCommands: []string{
			"gofmt -l <touched-go-files>",
			"goimports -l <touched-go-files>",
			"rch exec -- go test -short ./internal/ideation/...",
			"rch exec -- go test -short ./internal/cli/...",
			"git diff --check",
		},
		NonGoals: []string{
			"do not mutate Beads during dry-run rendering",
			"do not require network, real agents, or model calls",
			"do not add a new top-level robot surface for queue-dry planning",
		},
	})
	refinedRoadmap, refinement := ideaplan.RefinePlanForCreation(snapshot, roadmap, ideaplan.CreationRefinementOptions{MaxPasses: 3})
	creation := ideaplan.RunBeadCreation(context.Background(), refinedRoadmap, ideaplan.BeadCreationOptions{
		ProjectDir:      report.Project,
		PlanVersion:     opts.PlanVersion,
		CreateRequested: opts.CreateBeads,
		Confirmed:       opts.ConfirmCreate,
		AllowCreate:     guard.CreationAllowed && (report.QueueDry || opts.Force),
	})
	effectiveness := ideaplan.BuildEffectivenessReport(
		ideaplan.EffectivenessEventsFromArtifacts(ranking, refinedRoadmap, creation),
		ideaplan.EffectivenessOptions{ScenarioID: "queue-dry-ideation"},
	)

	status := "rendered"
	reason := "queue is dry; rendered duplicate-aware dry-run roadmap"
	if opts.Force && !report.QueueDry {
		status = "forced_preview"
		reason = "force requested; rendered dry-run roadmap even though ready work or unverified tracker state exists"
	}
	if opts.CreateBeads && !creation.Success {
		status = "creation_blocked"
		reason = "creation requested, but creation gate did not pass"
	}

	return QueueDryIdeationReport{
		Requested:     true,
		DryRun:        true,
		Forced:        opts.Force,
		Status:        status,
		Reason:        reason,
		Snapshot:      &snapshot,
		Ranking:       &ranking,
		Guard:         &guard,
		Effectiveness: &effectiveness,
		Refinement:    &refinement,
		Roadmap:       &refinedRoadmap,
		Creation:      &creation,
		NextActions:   queueDryIdeationNextActions(report, guard, refinedRoadmap, creation),
		Warnings:      queueDryIdeationWarnings(snapshot, report),
	}
}

func queueDryStaleIDs(items []QueueDryStaleIssue) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if item.ID != "" {
			ids = append(ids, item.ID)
		}
	}
	return sortedUniqueStrings(ids)
}

func queueDryIdeationWarnings(snapshot ideaplan.IdeaEvidenceSnapshot, report QueueDryResponse) []string {
	warnings := append([]string{}, report.Warnings...)
	for _, note := range snapshot.DegradedSources {
		message := strings.TrimSpace(note.Message)
		if message == "" {
			message = "source unavailable"
		}
		if note.SourceID != "" {
			message = note.SourceID + ": " + message
		}
		warnings = append(warnings, message)
	}
	return sortedUniqueStrings(warnings)
}

func queueDryIdeationNextActions(report QueueDryResponse, guard ideaplan.NoveltyGuardAssessment, roadmap ideaplan.RoadmapPlan, creation ideaplan.BeadCreationReport) []QueueDryRecommendation {
	actions := make([]QueueDryRecommendation, 0, len(report.Recommendations)+2)
	if guard.Recommendation != ideaplan.GuardRecommendationIdeate {
		actions = append(actions, QueueDryRecommendation{
			Code:     "respect_novelty_guard",
			Summary:  "novelty guard recommends " + string(guard.Recommendation) + " before mutating beads",
			Command:  commandForGuardRecommendation(guard.Recommendation),
			Evidence: strings.Join(guard.ReasonCodes, ", "),
		})
	}
	if roadmap.RenderedCount > 0 {
		actions = append(actions, QueueDryRecommendation{
			Code:     "inspect_dry_run_bead_preview",
			Summary:  "inspect proposed bead previews; commands are dry-run only",
			Evidence: fmt.Sprintf("%d proposed bead command(s)", roadmap.RenderedCount),
		})
	}
	if creation.CreateRequested && !creation.Success {
		actions = append(actions, QueueDryRecommendation{
			Code:     "resolve_creation_gate",
			Summary:  "creation was requested but did not pass all explicit gates",
			Evidence: fmt.Sprintf("%d remaining command(s)", len(creation.RemainingCommands)),
		})
	}
	return append(actions, report.Recommendations...)
}

func commandForGuardRecommendation(rec ideaplan.GuardRecommendation) string {
	switch rec {
	case ideaplan.GuardRecommendationReviewRecentWork:
		return "ntm review-queue <session>"
	case ideaplan.GuardRecommendationValidateCloseout:
		return "ntm work alerts --critical-only"
	case ideaplan.GuardRecommendationWaitForCoordination:
		return "ntm work commit-ready --format=json"
	default:
		return ""
	}
}

func collectQueueDryReport(dir string, now time.Time, staleThreshold time.Duration, commitLimit int, syncLagThreshold time.Duration, staleLimit int) QueueDryResponse {
	report := QueueDryResponse{
		TimestampedResponse: output.NewTimestamped(),
		Success:             true,
		Project:             dir,
	}

	summary := bv.GetBeadsSummary(dir, 5)
	if summary == nil || !summary.Available {
		report.Success = false
		report.Evidence.BeadsSummaryReason = "beads summary unavailable"
		if summary != nil && strings.TrimSpace(summary.Reason) != "" {
			report.Evidence.BeadsSummaryReason = summary.Reason
		}
		report.Errors = append(report.Errors, report.Evidence.BeadsSummaryReason)
	} else {
		report.Evidence.OpenCount = summary.Open
		report.Evidence.ActionableCount = summary.Ready
		report.Evidence.BlockedCount = summary.Blocked
		report.Evidence.InProgressCount = summary.InProgress
		report.Evidence.ReadyCount = summary.Ready
		report.Evidence.CountsVerified = true
		report.Evidence.StaleInProgress = findStaleInProgress(summary.InProgressList, now, staleThreshold, staleLimit)
	}

	triage, triageErr := queueDryGetTriage(dir)
	if triageErr != nil {
		report.Warnings = append(report.Warnings, fmt.Sprintf("bv triage unavailable: %v", triageErr))
		report.Evidence.TriageError = triageErr.Error()
	} else if triage != nil {
		report.Evidence.OpenCount = triage.Triage.QuickRef.OpenCount
		report.Evidence.ActionableCount = triage.Triage.QuickRef.ActionableCount
		report.Evidence.BlockedCount = triage.Triage.QuickRef.BlockedCount
		report.Evidence.InProgressCount = triage.Triage.QuickRef.InProgressCount
		report.Evidence.CountsVerified = true
		if report.Evidence.ReadyCount == 0 {
			report.Evidence.ReadyCount = triage.Triage.QuickRef.ActionableCount
		}
		for i, rec := range triage.Triage.Recommendations {
			if i >= 3 {
				break
			}
			report.Evidence.TriageTopIDs = append(report.Evidence.TriageTopIDs, rec.ID)
		}
	}

	report.Evidence.Sync = evaluateQueueDrySync(dir, syncLagThreshold)
	report.Evidence.Reservations = collectQueueDryReservations(dir)
	appendQueueDryReservationWarning(&report)

	commits := getRecentGitCommits(commitLimit)
	for _, c := range commits {
		hash := c.hash
		if len(hash) > 8 {
			hash = hash[:8]
		}
		report.Evidence.RecentCommits = append(report.Evidence.RecentCommits, QueueDryCommit{
			Hash:    hash,
			Subject: c.subject,
		})
	}

	report.QueueDry = report.Evidence.CountsVerified && report.Evidence.ActionableCount == 0 && report.Evidence.ReadyCount == 0
	report.Quiescence = evaluateQueueDryQuiescence(report)
	report.Recommendations = buildQueueDryRecommendations(report)
	report.Recipes = queueDryRecipes()

	return report
}

func evaluateQueueDryQuiescence(report QueueDryResponse) assurance.QuiescenceAssessment {
	if !report.Evidence.CountsVerified {
		return queueDryUnsafeQuiescence(
			assurance.ReasonQuiescenceTrackerUnknown,
			"tracker_counts_unavailable",
			"queue state is unknown because tracker counts were unavailable",
			"restore br/bv tracker access before standing down or creating queue-dry follow-up work",
		)
	}
	assessment := assurance.EvaluateQuiescence(assurance.QuiescenceInput{
		ReadyCount:             report.Evidence.ReadyCount,
		ActionableCount:        report.Evidence.ActionableCount,
		InProgressCount:        report.Evidence.InProgressCount,
		ActiveReservationCount: report.Evidence.Reservations.Count,
		TrackerNeedsFlush:      report.Evidence.Sync.NeedsFlush,
	})
	if assessment.SafeToStandDown && !report.Evidence.Reservations.Available {
		return queueDryUnsafeQuiescence(
			assurance.ReasonReservationUnknown,
			"reservations_unavailable",
			"queue appears dry, but reservation state could not be verified",
			"restore Agent Mail reservation visibility before standing down",
		)
	}
	return assessment
}

func queueDryUnsafeQuiescence(reason assurance.ReasonCode, evidence, summary, nextAction string) assurance.QuiescenceAssessment {
	reasons := []assurance.ReasonCode{reason}
	return assurance.QuiescenceAssessment{
		State:           assurance.QuiescenceUnsafeToStandDown,
		SafeToStandDown: false,
		Signal: assurance.Signal{
			Type:     assurance.SignalQuiescenceCandidate,
			Status:   assurance.SignalStatusDegraded,
			Reasons:  reasons,
			Evidence: evidence,
		},
		ReasonCodes:         reasons,
		Summary:             summary,
		SuggestedNextAction: nextAction,
	}
}

func findStaleInProgress(items []bv.BeadInProgress, now time.Time, threshold time.Duration, limit int) []QueueDryStaleIssue {
	stale := make([]QueueDryStaleIssue, 0)
	for _, item := range items {
		if item.UpdatedAt.IsZero() {
			continue
		}
		age := now.Sub(item.UpdatedAt)
		if age < threshold {
			continue
		}
		stale = append(stale, QueueDryStaleIssue{
			ID:        item.ID,
			Title:     item.Title,
			Assignee:  item.Assignee,
			UpdatedAt: item.UpdatedAt.UTC(),
			AgeHours:  int(age.Hours()),
		})
	}

	sort.Slice(stale, func(i, j int) bool {
		return stale[i].UpdatedAt.Before(stale[j].UpdatedAt)
	})
	if len(stale) > limit {
		stale = stale[:limit]
	}
	return stale
}

func evaluateQueueDrySync(projectDir string, lagThreshold time.Duration) QueueDrySyncStatus {
	status := QueueDrySyncStatus{
		HasLocalBeadsDB: bv.HasLocalBeadsDB(projectDir),
		Status:          "unknown",
	}
	if !status.HasLocalBeadsDB {
		status.Status = "no_local_beads_db"
		return status
	}

	beadsDir := filepath.Join(projectDir, ".beads")
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	dbPath := filepath.Join(beadsDir, "beads.db")

	issuesInfo, issuesErr := os.Stat(issuesPath)
	dbInfo, dbErr := os.Stat(dbPath)

	status.IssuesJSONLExists = issuesErr == nil
	status.BeadsDBExists = dbErr == nil

	switch {
	case issuesErr != nil && dbErr != nil:
		status.Status = "missing_jsonl_and_db"
		return status
	case issuesErr != nil:
		status.Status = "missing_issues_jsonl"
		return status
	case dbErr != nil:
		status.Status = "missing_beads_db"
		return status
	}

	issuesUpdatedAt := issuesInfo.ModTime().UTC()
	dbUpdatedAt := dbInfo.ModTime().UTC()
	status.IssuesJSONLUpdatedAt = &issuesUpdatedAt
	status.BeadsDBUpdatedAt = &dbUpdatedAt
	status.AgeDeltaSeconds = int64(issuesUpdatedAt.Sub(dbUpdatedAt).Seconds())

	switch {
	case dbUpdatedAt.After(issuesUpdatedAt.Add(lagThreshold)):
		status.Status = "beads_db_newer_than_jsonl"
		status.NeedsFlush = true
	case issuesUpdatedAt.After(dbUpdatedAt.Add(lagThreshold)):
		status.Status = "issues_jsonl_newer_than_beads_db"
	default:
		status.Status = "in_sync"
	}

	return status
}

func collectQueueDryReservations(projectDir string) QueueDryReservations {
	client := newAgentMailClient(projectDir)
	if !client.IsAvailable() {
		return QueueDryReservations{
			Available: false,
			Error:     "Agent Mail server unavailable",
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), queueDryReservationTimeout)
	defer cancel()

	reservations, err := fetchActiveReservations(ctx, client, projectDir, "", true)
	if err != nil {
		return QueueDryReservations{
			Available: false,
			Error:     err.Error(),
		}
	}

	holdersSet := make(map[string]struct{})
	for _, r := range reservations {
		if strings.TrimSpace(r.AgentName) == "" {
			continue
		}
		holdersSet[r.AgentName] = struct{}{}
	}

	holders := make([]string, 0, len(holdersSet))
	for holder := range holdersSet {
		holders = append(holders, holder)
	}
	sort.Strings(holders)

	return QueueDryReservations{
		Available: true,
		Count:     len(reservations),
		Holders:   holders,
	}
}

func appendQueueDryReservationWarning(report *QueueDryResponse) {
	if report == nil || report.Evidence.Reservations.Available {
		return
	}
	errText := strings.TrimSpace(report.Evidence.Reservations.Error)
	if errText == "" {
		errText = "unknown error"
	}
	report.Warnings = append(report.Warnings, "reservations_unavailable: "+errText)
}

func buildQueueDryRecommendations(report QueueDryResponse) []QueueDryRecommendation {
	recs := make([]QueueDryRecommendation, 0, 8)

	if report.Evidence.Sync.NeedsFlush {
		recs = append(recs, QueueDryRecommendation{
			Code:     "flush_jsonl",
			Summary:  "beads DB appears newer than issues.jsonl; flush JSONL export",
			Command:  "br sync --flush-only",
			Evidence: report.Evidence.Sync.Status,
		})
	}

	if len(report.Evidence.StaleInProgress) > 0 {
		oldest := report.Evidence.StaleInProgress[0]
		recs = append(recs, QueueDryRecommendation{
			Code:     "inspect_stale_in_progress",
			Summary:  "stale in_progress work exists; resolve ownership before creating new work",
			Command:  fmt.Sprintf("br show %s --json", oldest.ID),
			Evidence: fmt.Sprintf("%d stale in_progress items; oldest=%s", len(report.Evidence.StaleInProgress), oldest.ID),
		})
	}

	if report.Evidence.Reservations.Available && report.Evidence.Reservations.Count > 0 {
		recs = append(recs, QueueDryRecommendation{
			Code:     "inspect_active_reservations",
			Summary:  "active file reservations may block work pickup",
			Command:  "ntm locks list <session> --all-agents",
			Evidence: fmt.Sprintf("%d active reservations", report.Evidence.Reservations.Count),
		})
	}

	if report.QueueDry {
		recs = append(recs,
			QueueDryRecommendation{
				Code:     "review_pass",
				Summary:  "queue is dry; pivot to review-queue prompts for idle agents",
				Command:  "ntm review-queue <session>",
				Evidence: fmt.Sprintf("actionable=%d ready=%d", report.Evidence.ActionableCount, report.Evidence.ReadyCount),
			},
			QueueDryRecommendation{
				Code:    "alerts_sweep",
				Summary: "run critical alert sweep for hidden blockers or stale graph issues",
				Command: "ntm work alerts --critical-only",
			},
			QueueDryRecommendation{
				Code:    "seed_new_task",
				Summary: "if still dry after checks, create one scoped operator bead (advisory only)",
				Command: "br create --title=\"Queue-dry follow-up\" --type=task --priority=2",
			},
		)
	} else if len(report.Evidence.TriageTopIDs) > 0 {
		recs = append(recs, QueueDryRecommendation{
			Code:     "claim_top_ready",
			Summary:  "queue is not dry; claim the top triage recommendation",
			Command:  fmt.Sprintf("br update %s --status=in_progress", report.Evidence.TriageTopIDs[0]),
			Evidence: strings.Join(report.Evidence.TriageTopIDs, ", "),
		})
	}

	if len(recs) == 0 {
		recs = append(recs, QueueDryRecommendation{
			Code:    "refresh_triage",
			Summary: "refresh br/bv state before taking action",
			Command: "br ready --json && bv --robot-triage",
		})
	}

	return recs
}

func queueDryRecipes() []QueueDryRecipe {
	return []QueueDryRecipe{
		{
			Name:    "Critical Alerts Sweep",
			Command: "ntm work alerts --critical-only",
			Purpose: "Find hidden blockers and stale dependency warnings before creating new work.",
		},
		{
			Name:    "Review Queue Pivot",
			Command: "ntm review-queue <session>",
			Purpose: "Keep idle agents productive when no ready beads are available.",
		},
		{
			Name:    "Short Verification Sweep",
			Command: "rch exec -- go test -short ./internal/<pkg>/...",
			Purpose: "Run a quick confidence pass on the package you plan to touch next.",
		},
		{
			Name:    "Idea Wizard Backfill",
			Command: "br create --title=\"Queue-dry follow-up\" --type=task --priority=2",
			Purpose: "Record one concrete follow-up instead of widening scope without traceability.",
		},
	}
}

func renderCommitReady(report CommitReadyResponse) error {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))

	fmt.Println()
	fmt.Println(titleStyle.Render("Commit Readiness"))
	fmt.Println()
	fmt.Printf("  Project: %s\n", report.Project)
	if report.Agent != "" {
		fmt.Printf("  Agent: %s\n", report.Agent)
	} else {
		fmt.Println("  Agent: " + mutedStyle.Render("not provided"))
	}

	status := okStyle.Render("SAFE")
	if !report.SafeToCommit {
		status = errStyle.Render("BLOCKED")
	}
	fmt.Printf("  Status: %s\n", status)
	fmt.Println()

	gitStatus := "clean"
	if report.Evidence.Git.Dirty {
		gitStatus = "dirty"
	}
	if !report.Evidence.Git.Available {
		gitStatus = "unavailable"
	}
	fmt.Printf("  Git: %s", gitStatus)
	if len(report.Evidence.Git.ChangedFiles) > 0 {
		fmt.Printf(" (%d changed)", len(report.Evidence.Git.ChangedFiles))
	}
	fmt.Println()
	if len(report.Evidence.Git.ChangedFiles) > 0 {
		fmt.Printf("    Changed: %s\n", strings.Join(report.Evidence.Git.ChangedFiles, ", "))
	}

	fmt.Printf("  Beads sync: %s\n", report.Evidence.Sync.Status)
	if report.Evidence.Sync.NeedsFlush {
		fmt.Printf("    %s\n", warnStyle.Render("DB is newer than issues.jsonl; run br sync --flush-only"))
	}

	if report.Evidence.Reservations.Available {
		fmt.Printf("  Reservations: %d active project-wide\n", report.Evidence.Reservations.Count)
		if len(report.Evidence.Reservations.Holders) > 0 {
			fmt.Printf("    Holders: %s\n", strings.Join(report.Evidence.Reservations.Holders, ", "))
		}
	} else if report.Evidence.Reservations.Error != "" {
		fmt.Printf("  Reservations: %s\n", warnStyle.Render(report.Evidence.Reservations.Error))
	}

	if report.Evidence.Mail.Available {
		fmt.Printf("  Agent Mail: checked=%d urgent=%d ack_required=%d\n", report.Evidence.Mail.CheckedCount, report.Evidence.Mail.UrgentCount, report.Evidence.Mail.AckRequiredCount)
	} else if report.Evidence.Mail.Error != "" {
		fmt.Printf("  Agent Mail: %s\n", warnStyle.Render(report.Evidence.Mail.Error))
	}

	if len(report.Findings) > 0 {
		fmt.Println()
		fmt.Println(mutedStyle.Render("  Findings:"))
		for _, finding := range report.Findings {
			style := mutedStyle
			switch finding.Severity {
			case commitlint.SeverityCritical:
				style = errStyle
			case commitlint.SeverityWarning:
				style = warnStyle
			case commitlint.SeverityInfo:
				style = okStyle
			}
			fmt.Printf("    - %s %s: %s\n", style.Render(string(finding.Severity)), finding.Code, finding.Summary)
			if finding.Remediation != "" {
				fmt.Printf("      Fix: %s\n", finding.Remediation)
			}
			if len(finding.Evidence) > 0 {
				fmt.Printf("      Evidence: %s\n", strings.Join(finding.Evidence, ", "))
			}
		}
	}

	fmt.Println()
	return nil
}

func renderQueueDry(report QueueDryResponse) error {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))

	fmt.Println()
	fmt.Println(titleStyle.Render("Queue-Dry Diagnostic"))
	fmt.Println()
	fmt.Printf("  Project: %s\n", report.Project)
	switch {
	case !report.Evidence.CountsVerified:
		fmt.Printf("  Status: %s\n", warnStyle.Render("UNKNOWN - TRACKER UNAVAILABLE"))
	case report.QueueDry:
		fmt.Printf("  Status: %s\n", warnStyle.Render("QUEUE DRY"))
	default:
		fmt.Printf("  Status: %s\n", okStyle.Render("ACTIONABLE WORK EXISTS"))
	}
	fmt.Println()

	fmt.Printf("  Open: %d  Ready: %d  Actionable: %d  Blocked: %d  In Progress: %d\n",
		report.Evidence.OpenCount,
		report.Evidence.ReadyCount,
		report.Evidence.ActionableCount,
		report.Evidence.BlockedCount,
		report.Evidence.InProgressCount,
	)

	if len(report.Evidence.TriageTopIDs) > 0 {
		fmt.Printf("  Top triage IDs: %s\n", strings.Join(report.Evidence.TriageTopIDs, ", "))
	}

	if report.Quiescence.State != "" {
		standDown := "no"
		if report.Quiescence.SafeToStandDown {
			standDown = "yes"
		}
		fmt.Printf("  Quiescence: %s (safe_to_stand_down=%s)\n", report.Quiescence.State, standDown)
		if len(report.Quiescence.ReasonCodes) > 0 {
			reasons := make([]string, 0, len(report.Quiescence.ReasonCodes))
			for _, code := range report.Quiescence.ReasonCodes {
				reasons = append(reasons, string(code))
			}
			fmt.Printf("    Reasons: %s\n", strings.Join(reasons, ", "))
		}
	}

	fmt.Printf("  Sync: %s\n", report.Evidence.Sync.Status)
	if report.Evidence.Sync.NeedsFlush {
		fmt.Printf("    %s\n", warnStyle.Render("DB is newer than issues.jsonl; consider br sync --flush-only"))
	}

	if report.Evidence.Reservations.Available {
		fmt.Printf("  Active reservations: %d\n", report.Evidence.Reservations.Count)
	} else {
		fmt.Printf("  Active reservations: unavailable (%s)\n", report.Evidence.Reservations.Error)
	}

	if len(report.Evidence.StaleInProgress) > 0 {
		fmt.Println()
		fmt.Printf("  Stale in_progress (%d):\n", len(report.Evidence.StaleInProgress))
		for _, stale := range report.Evidence.StaleInProgress {
			fmt.Printf("    - %s (%dh): %s\n", stale.ID, stale.AgeHours, stale.Title)
		}
	}

	if len(report.Recommendations) > 0 {
		fmt.Println()
		fmt.Println(titleStyle.Render("Recommended Next Steps:"))
		for i, rec := range report.Recommendations {
			fmt.Printf("  %d. %s\n", i+1, rec.Summary)
			if rec.Command != "" {
				fmt.Printf("     %s %s\n", mutedStyle.Render("Command:"), rec.Command)
			}
			if rec.Evidence != "" {
				fmt.Printf("     %s %s\n", mutedStyle.Render("Evidence:"), rec.Evidence)
			}
		}
	}

	if report.Ideation != nil {
		fmt.Println()
		fmt.Println(titleStyle.Render("Ideation Dry Run:"))
		fmt.Printf("  Status: %s\n", report.Ideation.Status)
		fmt.Printf("  Reason: %s\n", report.Ideation.Reason)
		if report.Ideation.Guard != nil {
			fmt.Printf("  Guard: %s (creation_allowed=%t)\n", report.Ideation.Guard.Recommendation, report.Ideation.Guard.CreationAllowed)
			if len(report.Ideation.Guard.ReasonCodes) > 0 {
				fmt.Printf("    Reasons: %s\n", strings.Join(report.Ideation.Guard.ReasonCodes, ", "))
			}
		}
		if report.Ideation.Roadmap != nil {
			fmt.Printf("  Proposed beads: %d\n", report.Ideation.Roadmap.RenderedCount)
			for _, command := range report.Ideation.Roadmap.CommandPreview {
				fmt.Printf("    %s %s\n", mutedStyle.Render("Preview:"), command)
			}
		}
		if report.Ideation.Creation != nil {
			fmt.Printf("  Creation: success=%t dry_run=%t created=%d remaining=%d\n",
				report.Ideation.Creation.Success,
				report.Ideation.Creation.DryRun,
				len(report.Ideation.Creation.Created),
				len(report.Ideation.Creation.RemainingCommands),
			)
		}
	}

	if len(report.Warnings) > 0 {
		fmt.Println()
		fmt.Println(warnStyle.Render("Warnings:"))
		for _, warning := range report.Warnings {
			fmt.Printf("  - %s\n", warning)
		}
	}
	if len(report.Errors) > 0 {
		fmt.Println()
		fmt.Println(errStyle.Render("Errors:"))
		for _, err := range report.Errors {
			fmt.Printf("  - %s\n", err)
		}
	}

	if len(report.Evidence.RecentCommits) > 0 {
		fmt.Println()
		fmt.Println(titleStyle.Render("Recent Commits:"))
		for _, c := range report.Evidence.RecentCommits {
			fmt.Printf("  - %s %s\n", c.Hash, c.Subject)
		}
	}

	if len(report.Recipes) > 0 {
		fmt.Println()
		fmt.Println(titleStyle.Render("Known Recipes:"))
		for _, recipe := range report.Recipes {
			fmt.Printf("  - %s\n", recipe.Name)
			fmt.Printf("    %s %s\n", mutedStyle.Render("Command:"), recipe.Command)
		}
	}

	fmt.Println()
	return nil
}

func renderQueueDryMarkdown(report QueueDryResponse) error {
	fmt.Print(queueDryMarkdown(report))
	return nil
}

func queueDryMarkdown(report QueueDryResponse) string {
	var b strings.Builder
	b.WriteString("# Queue-Dry Diagnostic\n\n")
	fmt.Fprintf(&b, "- Project: `%s`\n", report.Project)
	fmt.Fprintf(&b, "- Queue dry: %t\n", report.QueueDry)
	fmt.Fprintf(&b, "- Counts verified: %t\n", report.Evidence.CountsVerified)
	fmt.Fprintf(&b, "- Open: %d\n", report.Evidence.OpenCount)
	fmt.Fprintf(&b, "- Ready: %d\n", report.Evidence.ReadyCount)
	fmt.Fprintf(&b, "- Actionable: %d\n", report.Evidence.ActionableCount)
	fmt.Fprintf(&b, "- Blocked: %d\n", report.Evidence.BlockedCount)
	fmt.Fprintf(&b, "- In progress: %d\n", report.Evidence.InProgressCount)
	if report.Quiescence.State != "" {
		fmt.Fprintf(&b, "- Quiescence: `%s` (safe_to_stand_down=%t)\n", report.Quiescence.State, report.Quiescence.SafeToStandDown)
	}
	if len(report.Recommendations) > 0 {
		b.WriteString("\n## Recommended Next Steps\n")
		for _, rec := range report.Recommendations {
			fmt.Fprintf(&b, "\n- `%s`: %s\n", rec.Code, rec.Summary)
			if rec.Command != "" {
				fmt.Fprintf(&b, "  - Command: `%s`\n", rec.Command)
			}
			if rec.Evidence != "" {
				fmt.Fprintf(&b, "  - Evidence: %s\n", rec.Evidence)
			}
		}
	}
	if report.Ideation != nil {
		b.WriteString("\n")
		b.WriteString(queueDryIdeationMarkdown(*report.Ideation))
	}
	if len(report.Warnings) > 0 {
		b.WriteString("\n## Warnings\n")
		for _, warning := range report.Warnings {
			fmt.Fprintf(&b, "\n- %s\n", warning)
		}
	}
	if len(report.Errors) > 0 {
		b.WriteString("\n## Errors\n")
		for _, err := range report.Errors {
			fmt.Fprintf(&b, "\n- %s\n", err)
		}
	}
	return b.String()
}

func queueDryIdeationMarkdown(report QueueDryIdeationReport) string {
	var b strings.Builder
	b.WriteString("# Queue-Dry Ideation Dry Run\n\n")
	fmt.Fprintf(&b, "- Dry run: %t\n", report.DryRun)
	fmt.Fprintf(&b, "- Status: `%s`\n", report.Status)
	fmt.Fprintf(&b, "- Forced: %t\n", report.Forced)
	if report.Reason != "" {
		fmt.Fprintf(&b, "- Reason: %s\n", report.Reason)
	}
	if report.Guard != nil {
		fmt.Fprintf(&b, "- Guard recommendation: `%s`\n", report.Guard.Recommendation)
		fmt.Fprintf(&b, "- Creation allowed: %t\n", report.Guard.CreationAllowed)
		if len(report.Guard.ReasonCodes) > 0 {
			fmt.Fprintf(&b, "- Guard reasons: %s\n", strings.Join(report.Guard.ReasonCodes, ", "))
		}
	}
	if report.Refinement != nil {
		fmt.Fprintf(&b, "- Refinement passes: %d\n", report.Refinement.Passes)
		fmt.Fprintf(&b, "- Refinement changed plan: %t\n", report.Refinement.Changed)
	}
	if report.Effectiveness != nil {
		fmt.Fprintf(&b, "- Effectiveness generated candidates: %d\n", report.Effectiveness.CandidateGeneratedCount)
		fmt.Fprintf(&b, "- Effectiveness rendered candidates: %d\n", report.Effectiveness.RenderedCount)
		if len(report.Effectiveness.CleanFamilyIDs) > 0 {
			fmt.Fprintf(&b, "- Clean families: %s\n", strings.Join(report.Effectiveness.CleanFamilyIDs, ", "))
		}
		if len(report.Effectiveness.ChurnFamilyIDs) > 0 {
			fmt.Fprintf(&b, "- Churn families: %s\n", strings.Join(report.Effectiveness.ChurnFamilyIDs, ", "))
		}
		if len(report.Effectiveness.LowYieldSourceIDs) > 0 {
			fmt.Fprintf(&b, "- Low-yield sources: %s\n", strings.Join(report.Effectiveness.LowYieldSourceIDs, ", "))
		}
	}
	if report.Creation != nil {
		fmt.Fprintf(&b, "- Creation requested: %t\n", report.Creation.CreateRequested)
		fmt.Fprintf(&b, "- Creation success: %t\n", report.Creation.Success)
		fmt.Fprintf(&b, "- Created beads: %d\n", len(report.Creation.Created))
		fmt.Fprintf(&b, "- Remaining create commands: %d\n", len(report.Creation.RemainingCommands))
	}
	if len(report.NextActions) > 0 {
		b.WriteString("\n## Ideation Next Actions\n")
		for _, action := range report.NextActions {
			fmt.Fprintf(&b, "\n- `%s`: %s\n", action.Code, action.Summary)
			if action.Command != "" {
				fmt.Fprintf(&b, "  - Command: `%s`\n", action.Command)
			}
			if action.Evidence != "" {
				fmt.Fprintf(&b, "  - Evidence: %s\n", action.Evidence)
			}
		}
	}
	if len(report.Warnings) > 0 {
		b.WriteString("\n## Ideation Warnings\n")
		for _, warning := range report.Warnings {
			fmt.Fprintf(&b, "\n- %s\n", warning)
		}
	}
	if report.Roadmap != nil {
		b.WriteString("\n")
		b.WriteString(ideaplan.RenderRoadmapMarkdown(*report.Roadmap))
	}
	return b.String()
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
