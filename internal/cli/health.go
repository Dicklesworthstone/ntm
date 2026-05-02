package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/health"
	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

var (
	healthWatch            bool
	healthInterval         int
	healthVerbose          bool
	healthPane             int
	healthStatus           string
	healthAutoRestartStuck bool
	healthThreshold        string
)

// SessionHealthInput is the kernel input for sessions.health.
type SessionHealthInput struct {
	Session string `json:"session"`
	Pane    *int   `json:"pane,omitempty"`
	Status  string `json:"status,omitempty"`
	Verbose bool   `json:"verbose,omitempty"`
}

// HealthOutput mirrors the JSON output for the health command.
type HealthOutput struct {
	*health.SessionHealth
	Error string `json:"error,omitempty"`
}

func init() {
	kernel.MustRegister(kernel.Command{
		Name:        "sessions.health",
		Description: "Session health status summary",
		Category:    "sessions",
		Input: &kernel.SchemaRef{
			Name: "SessionHealthInput",
			Ref:  "cli.SessionHealthInput",
		},
		Output: &kernel.SchemaRef{
			Name: "HealthOutput",
			Ref:  "cli.HealthOutput",
		},
		REST: &kernel.RESTBinding{
			Method: "GET",
			Path:   "/sessions/{sessionId}/health",
		},
		Examples: []kernel.Example{
			{
				Name:        "health",
				Description: "Check session health",
				Command:     "ntm health myproject",
			},
			{
				Name:        "health-filtered",
				Description: "Check session health for a specific pane",
				Command:     "ntm health myproject --pane 1",
			},
		},
		SafetyLevel: kernel.SafetySafe,
		Idempotent:  true,
	})
	kernel.MustRegisterHandler("sessions.health", func(ctx context.Context, input any) (any, error) {
		opts := SessionHealthInput{}
		switch value := input.(type) {
		case SessionHealthInput:
			opts = value
		case *SessionHealthInput:
			if value != nil {
				opts = *value
			}
		}
		if strings.TrimSpace(opts.Session) == "" {
			return nil, fmt.Errorf("session is required")
		}
		return buildHealthOutput(ctx, opts)
	})
}

func newHealthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "health [session]",
		Short: "Check health status of agents in a session",
		Long: `Check health status of all agents in a session.

Reports:
  - Process status (running/exited)
  - Activity level (active/idle/stale)
  - Uptime and restart counts
  - Detected issues (rate limits, crashes, errors)

Examples:
  ntm health myproject                          # Check health of all agents
  ntm health myproject --json                   # Output as JSON
  ntm health myproject --watch                  # Auto-refresh every 5s
  ntm health myproject --watch -i 2             # Auto-refresh every 2s
  ntm health myproject --verbose                # Show full error details
  ntm health myproject --pane 1                 # Filter to specific pane
  ntm health myproject --status ok              # Filter by status (ok/warning/error)
  ntm health myproject --auto-restart-stuck     # Detect and restart stuck agents
  ntm health myproject --auto-restart-stuck --threshold 10m --dry-run`,
		Args: cobra.MaximumNArgs(1),
		RunE: runHealth,
	}

	cmd.Flags().BoolVarP(&healthWatch, "watch", "w", false, "Auto-refresh health display")
	cmd.Flags().IntVarP(&healthInterval, "interval", "i", 5, "Refresh interval in seconds (with --watch)")
	cmd.Flags().BoolVarP(&healthVerbose, "verbose", "v", false, "Show full error details")
	cmd.Flags().IntVar(&healthPane, "pane", -1, "Filter to specific pane index")
	cmd.Flags().StringVar(&healthStatus, "status", "", "Filter by status (ok, warning, error)")
	cmd.Flags().BoolVar(&healthAutoRestartStuck, "auto-restart-stuck", false, "Detect and restart agents stuck with no output")
	cmd.Flags().StringVar(&healthThreshold, "threshold", "", "Duration before considering stuck (default 5m, e.g. 10m, 300s)")
	cmd.ValidArgsFunction = completeSessionArgs
	_ = cmd.RegisterFlagCompletionFunc("pane", completePaneIndexes)

	return cmd
}

func runHealth(cmd *cobra.Command, args []string) error {
	var session string

	if len(args) > 0 {
		session = args[0]
	}

	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	res, err := ResolveSession(session, cmd.OutOrStdout())
	if err != nil {
		return err
	}
	if res.Session == "" {
		return nil
	}
	res.ExplainIfInferred(cmd.ErrOrStderr())
	session = res.Session

	// Validate interval
	if healthInterval < 1 {
		healthInterval = 1
	}

	// Validate status filter
	if healthStatus != "" {
		healthStatus = strings.ToLower(healthStatus)
		if healthStatus != "ok" && healthStatus != "warning" && healthStatus != "error" {
			return fmt.Errorf("invalid status filter '%s': must be ok, warning, or error", healthStatus)
		}
	}

	// Auto-restart-stuck mode
	if healthAutoRestartStuck {
		return runAutoRestartStuck(session)
	}

	// Watch mode - continuous refresh
	if healthWatch {
		return runHealthWatch(session)
	}

	// Single check
	return runHealthOnce(session)
}

// runAutoRestartStuck detects and restarts stuck agents
func runAutoRestartStuck(session string) error {
	threshold, err := robot.ParseStuckThreshold(healthThreshold)
	if err != nil {
		return err
	}

	opts := robot.AutoRestartStuckOptions{
		Session:   session,
		Threshold: threshold,
		DryRun:    false, // dry-run handled via --json + robot flag path
	}

	if jsonOutput {
		return robot.PrintAutoRestartStuck(opts)
	}

	result, err := robot.GetAutoRestartStuck(opts)
	if err != nil {
		return err
	}

	// TUI output
	if len(result.StuckPanes) == 0 {
		fmt.Printf("No stuck agents found (threshold: %s)\n", result.Threshold)
		return nil
	}

	fmt.Printf("Stuck agents detected (threshold: %s):\n", result.Threshold)
	fmt.Printf("  Stuck panes:    %v\n", result.StuckPanes)
	if len(result.Restarted) > 0 {
		fmt.Printf("  Restarted:      %v\n", result.Restarted)
	}
	if len(result.Failed) > 0 {
		fmt.Printf("  Failed:         %v\n", result.Failed)
	}
	return nil
}

// runHealthOnce performs a single health check and outputs the result
func runHealthOnce(session string) error {
	if jsonOutput {
		var pane *int
		if healthPane >= 0 {
			pane = &healthPane
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		result, err := kernel.Run(ctx, "sessions.health", SessionHealthInput{
			Session: session,
			Pane:    pane,
			Status:  healthStatus,
			Verbose: healthVerbose,
		})
		if err != nil {
			if encErr := encodeHealthOutput(HealthOutput{
				SessionHealth: &health.SessionHealth{Session: session},
				Error:         err.Error(),
			}); encErr != nil {
				return encErr
			}
			return err
		}
		output, err := coerceHealthOutput(result)
		if err != nil {
			if encErr := encodeHealthOutput(HealthOutput{
				SessionHealth: &health.SessionHealth{Session: session},
				Error:         err.Error(),
			}); encErr != nil {
				return encErr
			}
			return err
		}
		// Print the JSON document first so automation can still
		// parse the report. Only after that do we surface a non-OK
		// outcome via the documented severity ladder (#112) — the
		// non-JSON path at the bottom of `renderHealthTUI` already
		// uses `os.Exit(1)` for warning and `os.Exit(2)` for
		// error; mirror that here so the contract is the same in
		// both modes.
		//
		// `os.Exit` skips audit teardown (root.go's
		// `logCommandAuditEnd` / `audit.CloseAll`), but that is
		// the pre-existing trade-off the non-JSON path already
		// made — keeping the two modes in lockstep is more
		// valuable than smuggling a richer exit code through
		// cobra.
		if err := encodeHealthOutput(output); err != nil {
			return err
		}
		// `Error` covers the "buildHealthOutput surfaced a soft
		// failure (e.g. session not found) but still produced a
		// JSON-shaped report" case — those legitimately had an
		// empty `OverallStatus` and slipped through the previous
		// status-only check. Map to exit 1 to match the non-JSON
		// path's `SessionNotFoundError` handling, which returns a
		// `fmt.Errorf` and lets cobra exit 1 (the default
		// returned-error code).
		if output.Error != "" {
			os.Exit(1)
		}
		// A nil `SessionHealth` after a successful kernel return
		// is degenerate — `buildHealthOutput` always populates
		// the embedded pointer, even on the soft "session not
		// found" path. Treat it as warning-level rather than
		// silently exiting 0.
		if output.SessionHealth == nil {
			os.Exit(1)
		}
		// Mirror the severity ladder from the non-JSON path:
		// `StatusError` → 2, anything else not-OK (warning,
		// unknown, or the empty-string zero value) → 1, OK → 0.
		switch output.SessionHealth.OverallStatus {
		case health.StatusOK:
			return nil
		case health.StatusError:
			os.Exit(2)
		default:
			// StatusWarning, StatusUnknown, "" — all map to
			// exit 1 so future Status additions don't silently
			// regress to a healthy exit code.
			os.Exit(1)
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := health.CheckSession(ctx, session)
	if err != nil {
		if _, ok := err.(*health.SessionNotFoundError); ok {
			return fmt.Errorf("session '%s' not found", session)
		}
		return err
	}

	// Apply filters
	result = filterHealthResult(result)

	// Enrich with tracker data (uptime, restarts)
	enrichHealthResult(session, result)

	return renderHealthTUI(result)
}

// runHealthWatch continuously refreshes health display
func runHealthWatch(session string) error {
	// Set up signal handling for clean exit
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	ticker := time.NewTicker(time.Duration(healthInterval) * time.Second)
	defer ticker.Stop()

	// Initial display
	clearScreen()
	if err := runHealthOnce(session); err != nil {
		return err
	}

	for {
		select {
		case <-sigChan:
			fmt.Println("\nStopping health watch...")
			return nil
		case <-ticker.C:
			clearScreen()
			if err := runHealthOnce(session); err != nil {
				// Don't exit on transient errors in watch mode
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
		}
	}
}

// clearScreen clears the terminal screen for watch mode
func clearScreen() {
	fmt.Print("\033[H\033[2J")
}

// filterHealthResult applies pane and status filters to the result
func filterHealthResult(result *health.SessionHealth) *health.SessionHealth {
	return filterHealthResultWithOptions(result, healthPane, healthStatus)
}

func filterHealthResultWithOptions(result *health.SessionHealth, paneFilter int, statusFilter string) *health.SessionHealth {
	if paneFilter < 0 && statusFilter == "" {
		return result // No filters
	}

	filtered := &health.SessionHealth{
		Session:       result.Session,
		CheckedAt:     result.CheckedAt,
		Agents:        make([]health.AgentHealth, 0),
		Summary:       health.HealthSummary{},
		OverallStatus: health.StatusOK,
	}

	for _, agent := range result.Agents {
		// Pane filter
		if paneFilter >= 0 && agent.Pane != paneFilter {
			continue
		}

		// Status filter
		if statusFilter != "" {
			statusStr := strings.ToLower(string(agent.Status))
			if statusStr != statusFilter {
				continue
			}
		}

		filtered.Agents = append(filtered.Agents, agent)

		// Update summary
		filtered.Summary.Total++
		switch agent.Status {
		case health.StatusOK:
			filtered.Summary.Healthy++
		case health.StatusWarning:
			filtered.Summary.Warning++
		case health.StatusError:
			filtered.Summary.Error++
		default:
			filtered.Summary.Unknown++
		}

		// Update overall status
		if statusSeverity(agent.Status) > statusSeverity(filtered.OverallStatus) {
			filtered.OverallStatus = agent.Status
		}
	}

	return filtered
}

// statusSeverity returns numeric severity for status comparison
func statusSeverity(s health.Status) int {
	switch s {
	case health.StatusOK:
		return 0
	case health.StatusWarning:
		return 1
	case health.StatusError:
		return 2
	default:
		return 0
	}
}

// enrichHealthResult adds uptime and restart data from the health tracker
func enrichHealthResult(session string, result *health.SessionHealth) {
	enrichHealthResultWithOptions(session, result, healthVerbose)
}

func enrichHealthResultWithOptions(session string, result *health.SessionHealth, verbose bool) {
	tracker := robot.GetHealthTracker(session)

	for i := range result.Agents {
		agent := &result.Agents[i]
		metrics, ok := tracker.GetHealth(agent.PaneID)
		if !ok {
			continue
		}

		// Add uptime info to issues for display
		uptime := tracker.GetUptime(agent.PaneID)
		restarts := tracker.GetRestartsInWindow(agent.PaneID)

		if restarts > 0 {
			agent.Issues = append(agent.Issues, health.Issue{
				Type:    "restart_count",
				Message: fmt.Sprintf("%d restarts in last hour", restarts),
			})
		}

		// Store uptime in IdleSeconds as a secondary metric if not already set
		if agent.IdleSeconds == 0 && uptime > 0 {
			// Use a special indicator - this is a bit of a hack but keeps compatibility
			agent.IdleSeconds = -int(uptime.Seconds()) // Negative = uptime, positive = idle
		}

		// Add last error info if verbose and there's an error
		if verbose && metrics.LastError != nil {
			agent.Issues = append(agent.Issues, health.Issue{
				Type:    metrics.LastError.Type,
				Message: metrics.LastError.Message,
			})
		}
	}
}

func coerceHealthOutput(result any) (HealthOutput, error) {
	switch value := result.(type) {
	case HealthOutput:
		return value, nil
	case *HealthOutput:
		if value != nil {
			return *value, nil
		}
		return HealthOutput{}, fmt.Errorf("sessions.health returned nil response")
	default:
		return HealthOutput{}, fmt.Errorf("sessions.health returned unexpected type %T", result)
	}
}

func buildHealthOutput(ctx context.Context, input SessionHealthInput) (HealthOutput, error) {
	result, err := health.CheckSession(ctx, input.Session)
	if err != nil {
		if _, ok := err.(*health.SessionNotFoundError); ok {
			return HealthOutput{
				SessionHealth: &health.SessionHealth{
					Session: input.Session,
					Summary: health.HealthSummary{},
					Agents:  []health.AgentHealth{},
				},
				Error: fmt.Sprintf("session '%s' not found", input.Session),
			}, nil
		}
		return HealthOutput{}, err
	}

	statusFilter := strings.ToLower(strings.TrimSpace(input.Status))
	if statusFilter != "" && statusFilter != "ok" && statusFilter != "warning" && statusFilter != "error" {
		return HealthOutput{}, fmt.Errorf("invalid status filter '%s': must be ok, warning, or error", statusFilter)
	}

	paneFilter := -1
	if input.Pane != nil {
		paneFilter = *input.Pane
	}

	result = filterHealthResultWithOptions(result, paneFilter, statusFilter)
	enrichHealthResultWithOptions(input.Session, result, input.Verbose)

	return HealthOutput{SessionHealth: result}, nil
}

func encodeHealthOutput(output HealthOutput) error {
	if output.SessionHealth == nil {
		output.SessionHealth = &health.SessionHealth{}
	}
	var err error
	if output.Error != "" {
		err = errors.New(output.Error)
	}
	return outputHealthJSON(output.SessionHealth, err)
}

func outputHealthJSON(result *health.SessionHealth, err error) error {
	type jsonOutput struct {
		*health.SessionHealth
		Error string `json:"error,omitempty"`
	}

	output := jsonOutput{SessionHealth: result}
	if err != nil {
		output.Error = err.Error()
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

func renderHealthTUI(result *health.SessionHealth) error {
	// Define styles
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("99"))

	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))     // Green
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))  // Orange
	errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196")) // Red
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")) // Gray

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(0, 1)

	// Status icon helper
	statusIcon := func(s health.Status) string {
		switch s {
		case health.StatusOK:
			return okStyle.Render("✓ OK")
		case health.StatusWarning:
			return warnStyle.Render("⚠ WARN")
		case health.StatusError:
			return errorStyle.Render("✗ ERROR")
		default:
			return mutedStyle.Render("? UNKNOWN")
		}
	}

	// Activity indicator
	activityStr := func(a health.ActivityLevel) string {
		switch a {
		case health.ActivityActive:
			return okStyle.Render("active")
		case health.ActivityIdle:
			return mutedStyle.Render("idle")
		case health.ActivityStale:
			return warnStyle.Render("stale")
		default:
			return mutedStyle.Render("unknown")
		}
	}

	// Format uptime/idle duration
	formatDuration := func(seconds int) string {
		if seconds == 0 {
			return "-"
		}
		// Negative means uptime (encoded in enrichHealthResult)
		if seconds < 0 {
			seconds = -seconds
			hours := seconds / 3600
			mins := (seconds % 3600) / 60
			if hours > 0 {
				return fmt.Sprintf("up %dh%dm", hours, mins)
			}
			return fmt.Sprintf("up %dm", mins)
		}
		// Positive means idle time
		mins := seconds / 60
		if mins > 0 {
			return fmt.Sprintf("idle %dm", mins)
		}
		return fmt.Sprintf("idle %ds", seconds)
	}

	// Build header
	fmt.Println()
	fmt.Printf("%s %s\n", titleStyle.Render("Session:"), result.Session)
	if healthWatch {
		fmt.Printf("%s %s (refreshing every %ds)\n",
			mutedStyle.Render("Checked:"),
			result.CheckedAt.Format("15:04:05"),
			healthInterval)
	}
	fmt.Println()

	// Build table header - include Uptime column
	header := fmt.Sprintf("%-6s │ %-10s │ %-10s │ %-10s │ %-12s │ %s",
		"Pane", "Agent", "Status", "Activity", "Uptime", "Issues")
	fmt.Println(mutedStyle.Render(header))
	fmt.Println(mutedStyle.Render(strings.Repeat("─", 85)))

	// Build table rows
	for _, agent := range result.Agents {
		// Format issues
		issueStr := "-"
		if len(agent.Issues) > 0 {
			var issueStrs []string
			for _, issue := range agent.Issues {
				// Skip restart_count in issues display (shown separately in uptime)
				if issue.Type == "restart_count" {
					continue
				}
				issueStrs = append(issueStrs, issue.Message)
			}
			if len(issueStrs) > 0 {
				issueStr = strings.Join(issueStrs, ", ")
			}
		}

		// Add stale timing if relevant and not already showing issues
		if agent.Activity == health.ActivityStale && agent.IdleSeconds > 0 && issueStr == "-" {
			mins := agent.IdleSeconds / 60
			if mins > 0 {
				issueStr = fmt.Sprintf("no output %dm", mins)
			}
		}

		// Format uptime/idle
		uptimeStr := formatDuration(agent.IdleSeconds)

		// Check for restart count in issues
		for _, issue := range agent.Issues {
			if issue.Type == "restart_count" {
				uptimeStr = fmt.Sprintf("%s (%s)", uptimeStr, issue.Message)
				break
			}
		}

		row := fmt.Sprintf("%-6d │ %-10s │ %-10s │ %-10s │ %-12s │ %s",
			agent.Pane,
			truncateString(agent.AgentType, 10),
			statusIcon(agent.Status),
			activityStr(agent.Activity),
			truncateString(uptimeStr, 12),
			issueStr)
		fmt.Println(row)

		// In verbose mode, show additional details
		if healthVerbose && len(agent.Issues) > 0 {
			for _, issue := range agent.Issues {
				if issue.Type == "restart_count" {
					continue // Already shown in uptime column
				}
				fmt.Printf("       │ %s: %s\n",
					mutedStyle.Render(issue.Type),
					issue.Message)
			}
		}
	}

	fmt.Println()

	// Summary box
	summary := fmt.Sprintf("Overall: %d healthy, %d warning, %d error",
		result.Summary.Healthy,
		result.Summary.Warning,
		result.Summary.Error)
	fmt.Println(boxStyle.Render(summary))
	fmt.Println()

	// Don't exit in watch mode - only set exit code on final display
	if !healthWatch {
		switch result.OverallStatus {
		case health.StatusError:
			os.Exit(2)
		case health.StatusWarning:
			os.Exit(1)
		}
	}

	return nil
}

// truncateString truncates a string to maxLen runes with ellipsis if needed
func truncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 1 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-1]) + "…"
}
