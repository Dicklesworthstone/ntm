// Package cli provides command-line interface commands for ntm.
// activity.go implements the `ntm activity` command for displaying agent activity states.
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

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func newActivityCmd() *cobra.Command {
	var (
		filterClaude bool
		filterCodex  bool
		filterGemini bool
		filterPane   string
		watchMode    bool
		interval     int
	)

	cmd := &cobra.Command{
		Use:   "activity [session]",
		Short: "Show agent activity states in a session",
		Long: `Display real-time activity states of agents in a session.

Shows a table with:
  - Pane index and agent type
  - Current state (GENERATING, WAITING, THINKING, ERROR, STALLED)
  - Output velocity (chars/sec)
  - Duration in current state

States are color-coded:
  - Green: WAITING (available for work)
  - Yellow: THINKING (processing)
  - Blue: GENERATING (actively outputting)
  - Red: ERROR or STALLED (needs attention)
  - Gray: UNKNOWN

Examples:
  ntm activity                     # Auto-detect session
  ntm activity myproject           # Specific session
  ntm activity --cc                # Only Claude agents
  ntm activity --watch             # Auto-refresh every 2s
  ntm activity --watch --interval 1000  # Refresh every 1s
  ntm activity --json              # Output as JSON`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var session string
			if len(args) > 0 {
				session = args[0]
			}

			opts := activityOptions{
				filterClaude: filterClaude,
				filterCodex:  filterCodex,
				filterGemini: filterGemini,
				filterPane:   filterPane,
				watchMode:    watchMode,
				interval:     time.Duration(interval) * time.Millisecond,
			}

			return runActivity(session, opts)
		},
	}

	cmd.Flags().BoolVar(&filterClaude, "cc", false, "Only show Claude agents")
	cmd.Flags().BoolVar(&filterCodex, "cod", false, "Only show Codex agents")
	cmd.Flags().BoolVar(&filterGemini, "gmi", false, "Only show Gemini agents")
	cmd.Flags().StringVar(&filterPane, "pane", "", "Show specific pane (by name or index)")
	cmd.Flags().BoolVarP(&watchMode, "watch", "w", false, "Auto-refresh display")
	cmd.Flags().IntVar(&interval, "interval", 2000, "Refresh interval in milliseconds (with --watch)")

	return cmd
}

type activityOptions struct {
	filterClaude bool
	filterCodex  bool
	filterGemini bool
	filterPane   string
	watchMode    bool
	interval     time.Duration
}

func runActivity(session string, opts activityOptions) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	res, err := ResolveSession(session, os.Stdout)
	if err != nil {
		return err
	}
	if res.Session == "" {
		return nil
	}
	res.ExplainIfInferred(os.Stderr)
	session = res.Session

	if !tmux.SessionExists(session) {
		if jsonOutput {
			return outputActivityError(session, fmt.Errorf("session '%s' not found", session))
		}
		return fmt.Errorf("session '%s' not found", session)
	}

	// Watch mode
	if opts.watchMode {
		return runActivityWatch(session, opts)
	}

	// Single run
	return runActivityOnce(session, opts)
}

func runActivityOnce(session string, opts activityOptions) error {
	result, err := collectActivityData(session, opts)
	if err != nil {
		if jsonOutput {
			return outputActivityError(session, err)
		}
		return err
	}

	if jsonOutput {
		return outputActivityJSON(result)
	}

	return renderActivityTUI(result, false)
}

func runActivityWatch(session string, opts activityOptions) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle Ctrl+C
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Print("\033[?25h") // Show cursor
		cancel()
	}()

	// Hide cursor for cleaner display
	fmt.Print("\033[?25l")
	defer fmt.Print("\033[?25h")

	ticker := time.NewTicker(opts.interval)
	defer ticker.Stop()

	firstRun := true
	for {
		if !firstRun {
			select {
			case <-ctx.Done():
				fmt.Println("\nWatch mode stopped.")
				return nil
			case <-ticker.C:
			}
		} else {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
		}

		// Clear screen and move to top
		if !firstRun {
			fmt.Print("\033[H\033[J")
		}

		result, err := collectActivityData(session, opts)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			firstRun = false
			continue
		}

		if err := renderActivityTUI(result, true); err != nil {
			fmt.Printf("Render error: %v\n", err)
		}

		firstRun = false
	}
}

type activityResult struct {
	Session    string
	CapturedAt time.Time
	Agents     []agentInfo
	Summary    map[string]int
}

type agentInfo struct {
	Pane       int
	AgentType  string
	State      string
	Confidence float64
	Velocity   float64
	Duration   time.Duration
	StateSince time.Time
}

func collectActivityData(session string, opts activityOptions) (*activityResult, error) {
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return nil, fmt.Errorf("failed to get panes: %w", err)
	}

	result := &activityResult{
		Session:    session,
		CapturedAt: time.Now(),
		Agents:     make([]agentInfo, 0),
		Summary:    make(map[string]int),
	}

	for _, pane := range panes {
		agentType := detectAgentTypeFromPane(pane)

		// Skip non-agent panes
		if agentType == "unknown" || agentType == "user" {
			continue
		}

		// Apply filters
		if !passesFilter(agentType, pane, opts) {
			continue
		}

		// Create classifier and get state
		classifier := robot.NewStateClassifier(pane.ID, &robot.ClassifierConfig{
			AgentType: agentType,
		})

		activity, err := classifier.Classify()
		if err != nil {
			// Include with unknown state on error
			result.Agents = append(result.Agents, agentInfo{
				Pane:      pane.Index,
				AgentType: agentType,
				State:     "UNKNOWN",
			})
			result.Summary["UNKNOWN"]++
			continue
		}

		// Calculate duration
		var duration time.Duration
		if !activity.StateSince.IsZero() {
			duration = time.Since(activity.StateSince)
		}

		info := agentInfo{
			Pane:       pane.Index,
			AgentType:  agentType,
			State:      string(activity.State),
			Confidence: activity.Confidence,
			Velocity:   activity.Velocity,
			Duration:   duration,
			StateSince: activity.StateSince,
		}

		result.Agents = append(result.Agents, info)
		result.Summary[string(activity.State)]++
	}

	return result, nil
}

func detectAgentTypeFromPane(pane tmux.Pane) string {
	// Use the pane's Type field which is already parsed
	switch pane.Type {
	case tmux.AgentClaude:
		return "claude"
	case tmux.AgentCodex:
		return "codex"
	case tmux.AgentGemini:
		return "gemini"
	case tmux.AgentUser:
		return "user"
	default:
		return "unknown"
	}
}

func passesFilter(agentType string, pane tmux.Pane, opts activityOptions) bool {
	// Check pane filter first
	if opts.filterPane != "" {
		if pane.Title == opts.filterPane || fmt.Sprintf("%d", pane.Index) == opts.filterPane {
			return true
		}
		return false
	}

	// If no type filters, allow all
	if !opts.filterClaude && !opts.filterCodex && !opts.filterGemini {
		return true
	}

	// Apply type filters
	if opts.filterClaude && agentType == "claude" {
		return true
	}
	if opts.filterCodex && agentType == "codex" {
		return true
	}
	if opts.filterGemini && agentType == "gemini" {
		return true
	}

	return false
}

func outputActivityJSON(result *activityResult) error {
	type jsonAgent struct {
		Pane       int     `json:"pane"`
		AgentType  string  `json:"agent_type"`
		State      string  `json:"state"`
		Confidence float64 `json:"confidence"`
		Velocity   float64 `json:"velocity"`
		Duration   string  `json:"duration"`
		StateSince string  `json:"state_since,omitempty"`
	}

	type jsonOutput struct {
		Success    bool           `json:"success"`
		Session    string         `json:"session"`
		CapturedAt string         `json:"captured_at"`
		Agents     []jsonAgent    `json:"agents"`
		Summary    map[string]int `json:"summary"`
	}

	agents := make([]jsonAgent, len(result.Agents))
	for i, a := range result.Agents {
		agents[i] = jsonAgent{
			Pane:       a.Pane,
			AgentType:  a.AgentType,
			State:      a.State,
			Confidence: a.Confidence,
			Velocity:   a.Velocity,
			Duration:   formatActivityDuration(a.Duration),
		}
		if !a.StateSince.IsZero() {
			agents[i].StateSince = a.StateSince.Format(time.RFC3339)
		}
	}

	output := jsonOutput{
		Success:    true,
		Session:    result.Session,
		CapturedAt: result.CapturedAt.Format(time.RFC3339),
		Agents:     agents,
		Summary:    result.Summary,
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

func outputActivityError(session string, err error) error {
	type jsonOutput struct {
		Success bool   `json:"success"`
		Session string `json:"session"`
		Error   string `json:"error"`
	}

	output := jsonOutput{
		Success: false,
		Session: session,
		Error:   err.Error(),
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

func renderActivityTUI(result *activityResult, watchMode bool) error {
	t := theme.Current()

	// Define styles
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(t.Mauve)

	headerStyle := lipgloss.NewStyle().
		Foreground(t.Overlay).
		Bold(true)

	mutedStyle := lipgloss.NewStyle().
		Foreground(t.Overlay)

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Surface1).
		Padding(0, 1)

	// State color helper
	stateStyle := func(state string) lipgloss.Style {
		switch state {
		case "WAITING":
			return lipgloss.NewStyle().Foreground(t.Green)
		case "GENERATING":
			return lipgloss.NewStyle().Foreground(t.Blue)
		case "THINKING":
			return lipgloss.NewStyle().Foreground(t.Yellow)
		case "ERROR", "STALLED":
			return lipgloss.NewStyle().Foreground(t.Red)
		default:
			return lipgloss.NewStyle().Foreground(t.Overlay)
		}
	}

	// Agent type color helper
	agentStyle := func(agentType string) lipgloss.Style {
		switch agentType {
		case "claude":
			return lipgloss.NewStyle().Foreground(t.Claude)
		case "codex":
			return lipgloss.NewStyle().Foreground(t.Codex)
		case "gemini":
			return lipgloss.NewStyle().Foreground(t.Gemini)
		default:
			return lipgloss.NewStyle().Foreground(t.Text)
		}
	}

	// Build header
	fmt.Println()
	if watchMode {
		fmt.Printf("%s %s  %s\n",
			titleStyle.Render("Session:"),
			result.Session,
			mutedStyle.Render(fmt.Sprintf("(updated %s - Ctrl+C to stop)", result.CapturedAt.Format("15:04:05"))))
	} else {
		fmt.Printf("%s %s\n", titleStyle.Render("Session:"), result.Session)
	}
	fmt.Println()

	if len(result.Agents) == 0 {
		fmt.Println(mutedStyle.Render("No agents found in session"))
		return nil
	}

	// Build table header
	header := fmt.Sprintf("%-6s  %-8s  %-12s  %-10s  %s",
		"Pane", "Agent", "State", "Velocity", "Duration")
	fmt.Println(headerStyle.Render(header))
	fmt.Println(mutedStyle.Render(strings.Repeat("─", 55)))

	// Build table rows
	for _, agent := range result.Agents {
		// Format velocity
		velocityStr := "-"
		if agent.Velocity > 0 {
			velocityStr = fmt.Sprintf("%.1f c/s", agent.Velocity)
		}

		// Format duration
		durationStr := formatActivityDuration(agent.Duration)

		// Format state with icon
		stateIcon := stateIcon(agent.State)
		stateStr := stateStyle(agent.State).Render(fmt.Sprintf("%s %s", stateIcon, agent.State))

		row := fmt.Sprintf("%-6d  %s  %-12s  %-10s  %s",
			agent.Pane,
			agentStyle(agent.AgentType).Render(fmt.Sprintf("%-8s", agent.AgentType)),
			stateStr,
			velocityStr,
			durationStr)
		fmt.Println(row)
	}

	fmt.Println()

	// Summary box
	var summaryParts []string
	total := len(result.Agents)
	waiting := result.Summary["WAITING"]
	busy := result.Summary["GENERATING"] + result.Summary["THINKING"]
	problems := result.Summary["ERROR"] + result.Summary["STALLED"]

	summaryParts = append(summaryParts, fmt.Sprintf("%d total", total))
	if waiting > 0 {
		summaryParts = append(summaryParts, fmt.Sprintf("%d available", waiting))
	}
	if busy > 0 {
		summaryParts = append(summaryParts, fmt.Sprintf("%d busy", busy))
	}
	if problems > 0 {
		summaryParts = append(summaryParts, fmt.Sprintf("%d problems", problems))
	}

	summary := strings.Join(summaryParts, ", ")
	fmt.Println(boxStyle.Render(summary))
	fmt.Println()

	return nil
}

func stateIcon(state string) string {
	switch state {
	case "WAITING":
		return "●"
	case "GENERATING":
		return "▶"
	case "THINKING":
		return "◐"
	case "ERROR":
		return "✗"
	case "STALLED":
		return "◯"
	default:
		return "?"
	}
}

func formatActivityDuration(d time.Duration) string {
	if d == 0 {
		return "-"
	}

	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		mins := int(d.Minutes())
		secs := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm%ds", mins, secs)
	}
	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh%dm", hours, mins)
}
