// Package cli provides command-line interface commands for ntm.
// activity.go implements the `ntm activity` command for displaying agent activity states.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func newActivityCmd() *cobra.Command {
	var (
		filterClaude      bool
		filterCodex       bool
		filterGemini      bool
		filterAntigravity bool
		filterGrok        bool
		filterPane        string
		watchMode         bool
		interval          int
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
  ntm activity --grok              # Only Grok Build agents
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
				filterClaude:      filterClaude,
				filterCodex:       filterCodex,
				filterGemini:      filterGemini,
				filterAntigravity: filterAntigravity,
				filterGrok:        filterGrok,
				filterPane:        filterPane,
				watchMode:         watchMode,
				interval:          time.Duration(interval) * time.Millisecond,
			}

			return runActivity(session, opts)
		},
	}

	cmd.Flags().BoolVar(&filterClaude, "cc", false, "Only show Claude agents")
	cmd.Flags().BoolVar(&filterCodex, "cod", false, "Only show Codex agents")
	cmd.Flags().BoolVar(&filterGemini, "gmi", false, "Only show Gemini agents")
	cmd.Flags().BoolVar(&filterAntigravity, "agy", false, "Only show Antigravity agents")
	cmd.Flags().BoolVar(&filterGrok, "grok", false, "Only show Grok Build agents")
	cmd.Flags().StringVar(&filterPane, "pane", "", "Show specific pane (by name or index)")
	cmd.Flags().BoolVarP(&watchMode, "watch", "w", false, "Auto-refresh display")
	cmd.Flags().IntVar(&interval, "interval", 2000, "Refresh interval in milliseconds (with --watch)")
	cmd.ValidArgsFunction = completeSessionArgs
	_ = cmd.RegisterFlagCompletionFunc("pane", completePaneIndexes)

	return cmd
}

type activityOptions struct {
	filterClaude      bool
	filterCodex       bool
	filterGemini      bool
	filterAntigravity bool
	filterGrok        bool
	filterPane        string
	watchMode         bool
	interval          time.Duration
	watermarkStore    robot.WatermarkStore
}

func runActivity(session string, opts activityOptions) error {
	// PersistentPreRun opens the runtime store for the real command path.
	// Carry it explicitly into collection so direct unit-test/library calls do
	// not depend on mutable package-global store lifecycle.
	if opts.watermarkStore == nil {
		opts.watermarkStore = robotStateStore
	}

	// bd-ixy2t: emit a JSON envelope on early-fail when --json is set so
	// automation pipelines (`ntm activity --json | jq ...`) always see a
	// parseable failure on stdout instead of a stderr "Error:" line.
	if err := tmux.EnsureInstalled(); err != nil {
		if jsonOutput {
			return outputActivityError(session, err)
		}
		return err
	}

	res, err := ResolveSession(session, os.Stdout)
	if err != nil {
		if jsonOutput {
			return outputActivityError(session, err)
		}
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

	// Watch mode. Under --json the watch stream is NDJSON: one compact JSON
	// frame per refresh tick (the same envelope shape as single-shot --json),
	// with no ANSI control sequences — previously --watch silently ignored
	// --json and painted the TUI (bd-ws7-docs-ux-truth-tqh3l.4).
	if opts.watchMode {
		if jsonOutput {
			return runActivityWatchNDJSON(session, opts)
		}
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
	defer signal.Stop(sigChan)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-sigChan:
			fmt.Print("\033[?25h") // Show cursor
			cancel()
		case <-done:
		}
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
	Pane int
	// WindowIndex and PaneID disambiguate agents whose per-window pane index
	// collides across windows: in a multi-window session every window has its
	// own "pane 1", so Pane alone is neither unique nor stable. PaneID is the
	// tmux %id, stable for the pane's lifetime.
	WindowIndex int
	PaneID      string
	AgentType   string
	State       string
	Confidence  float64
	Velocity    float64
	Duration    time.Duration
	StateSince  time.Time
}

func collectActivityData(session string, opts activityOptions) (*activityResult, error) {
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return nil, fmt.Errorf("failed to get panes: %w", err)
	}

	result := &activityResult{
		Session:    session,
		CapturedAt: time.Now().UTC(),
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
		if !passesFilter(agentType, pane, opts, tmux.PanesSpanMultipleWindows(panes)) {
			continue
		}

		// Create classifier and get state
		classifier := robot.NewStateClassifier(pane.ID, &robot.ClassifierConfig{
			AgentType:  agentType,
			PaneWidth:  pane.Width,
			PaneHeight: pane.Height,
		})

		activity, err := classifier.Classify()
		if err != nil {
			// Include with unknown state on error
			result.Agents = append(result.Agents, agentInfo{
				Pane:        pane.Index,
				WindowIndex: pane.WindowIndex,
				PaneID:      pane.ID,
				AgentType:   agentType,
				State:       "UNKNOWN",
			})
			result.Summary["UNKNOWN"]++
			continue
		}

		// A one-shot activity invocation creates a fresh classifier, so its
		// in-memory StateSince is necessarily "now". Reconcile the observed
		// state through the runtime store to preserve state age across both
		// watch ticks and separate CLI processes.
		stateSince := activity.StateSince
		if durableSince, persistErr := robot.ObserveActivityState(
			opts.watermarkStore,
			activityStateScope(session, pane),
			activity.State,
			result.CapturedAt,
		); persistErr == nil {
			stateSince = durableSince
		}

		// Calculate duration against the snapshot timestamp so every row in a
		// frame has a coherent clock and a future/corrupt watermark cannot emit
		// a negative duration.
		var duration time.Duration
		if !stateSince.IsZero() && !stateSince.After(result.CapturedAt) {
			duration = result.CapturedAt.Sub(stateSince)
		}

		info := agentInfo{
			Pane:        pane.Index,
			WindowIndex: pane.WindowIndex,
			PaneID:      pane.ID,
			AgentType:   agentType,
			State:       string(activity.State),
			Confidence:  activity.Confidence,
			Velocity:    activity.Velocity,
			Duration:    duration,
			StateSince:  stateSince,
		}

		result.Agents = append(result.Agents, info)
		result.Summary[string(activity.State)]++
	}

	return result, nil
}

func activityStateScope(session string, pane tmux.Pane) string {
	return fmt.Sprintf("%s|%s|%d", session, pane.ID, pane.PID)
}

func detectAgentTypeFromPane(pane tmux.Pane) string {
	switch tmux.AgentType(pane.Type).Canonical() {
	case tmux.AgentClaude:
		return "claude"
	case tmux.AgentCodex:
		return "codex"
	case tmux.AgentGemini:
		return "gemini"
	case tmux.AgentAntigravity:
		return "antigravity"
	case tmux.AgentGrok:
		return "grok"
	case tmux.AgentCursor:
		return "cursor"
	case tmux.AgentWindsurf:
		return "windsurf"
	case tmux.AgentAider:
		return "aider"
	case tmux.AgentOpencode:
		return "oc"
	case tmux.AgentOllama:
		return "ollama"
	case tmux.AgentUser:
		return "user"
	default:
		return "unknown"
	}
}

func agentTypeForPane(pane tmux.Pane) string {
	if agentType := detectAgentTypeFromPane(pane); agentType != "" && agentType != "unknown" {
		return agentType
	}
	return detectAgentTypeFromTitle(pane.Title)
}

func passesFilter(agentType string, pane tmux.Pane, opts activityOptions, multiWindow bool) bool {
	// Check pane filter first
	if opts.filterPane != "" {
		if pane.Title == opts.filterPane || tmux.PaneMatchesSelector(pane, opts.filterPane, multiWindow) {
			return true
		}
		return false
	}

	// If no type filters, allow all
	if !opts.filterClaude && !opts.filterCodex && !opts.filterGemini && !opts.filterAntigravity && !opts.filterGrok {
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
	if opts.filterAntigravity && agentType == "antigravity" {
		return true
	}
	if opts.filterGrok && agentType == "grok" {
		return true
	}

	return false
}

// activityJSONAgent is one agent row in the --json envelope.
type activityJSONAgent struct {
	// Pane is the per-window pane index, kept for compatibility with existing
	// consumers. It collides across windows; use WindowIndex+Pane to address a
	// pane topologically, or PaneID (the tmux %id, stable for the pane's
	// lifetime) to identify it unambiguously.
	Pane        int     `json:"pane"`
	WindowIndex int     `json:"window_index"`
	PaneID      string  `json:"pane_id"`
	AgentType   string  `json:"agent_type"`
	State       string  `json:"state"`
	Confidence  float64 `json:"confidence"`
	Velocity    float64 `json:"velocity"`
	Duration    string  `json:"duration"`
	StateSince  string  `json:"state_since,omitempty"`
}

// activityJSONEnvelope is the --json envelope for `ntm activity`. Watch mode
// streams the same shape as NDJSON frames (one per refresh tick).
type activityJSONEnvelope struct {
	Success    bool                `json:"success"`
	Session    string              `json:"session"`
	CapturedAt string              `json:"captured_at"`
	Agents     []activityJSONAgent `json:"agents"`
	Summary    map[string]int      `json:"summary"`
	Error      string              `json:"error,omitempty"`
}

func buildActivityJSONEnvelope(result *activityResult) activityJSONEnvelope {
	agents := make([]activityJSONAgent, len(result.Agents))
	for i, a := range result.Agents {
		agents[i] = activityJSONAgent{
			Pane:        a.Pane,
			WindowIndex: a.WindowIndex,
			PaneID:      a.PaneID,
			AgentType:   a.AgentType,
			State:       a.State,
			Confidence:  a.Confidence,
			Velocity:    a.Velocity,
			Duration:    formatActivityDuration(a.Duration),
		}
		if !a.StateSince.IsZero() {
			agents[i].StateSince = a.StateSince.Format(time.RFC3339)
		}
	}

	return activityJSONEnvelope{
		Success:    true,
		Session:    result.Session,
		CapturedAt: result.CapturedAt.Format(time.RFC3339),
		Agents:     agents,
		Summary:    result.Summary,
	}
}

func outputActivityJSON(result *activityResult) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(buildActivityJSONEnvelope(result))
}

// runActivityWatchNDJSON streams activity snapshots as NDJSON: one compact,
// newline-terminated JSON frame per refresh tick. Collection errors emit a
// success:false frame and the stream continues (best-effort watch, matching
// the text watch loop). The stream ends on SIGINT/SIGTERM with exit 0.
func runActivityWatchNDJSON(session string, opts activityOptions) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-sigChan:
			cancel()
		case <-done:
		}
	}()

	return activityWatchNDJSONLoop(ctx, os.Stdout, session, opts)
}

// activityWatchNDJSONLoop emits one compact frame per tick until ctx is
// canceled. Split from runActivityWatchNDJSON so tests can drive it with a
// cancellable context and an in-memory writer.
func activityWatchNDJSONLoop(ctx context.Context, w io.Writer, session string, opts activityOptions) error {
	// Compact one-object-per-line framing: no SetIndent.
	encoder := json.NewEncoder(w)

	ticker := time.NewTicker(opts.interval)
	defer ticker.Stop()

	firstRun := true
	for {
		if !firstRun {
			select {
			case <-ctx.Done():
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
		firstRun = false

		result, err := collectActivityData(session, opts)
		if err != nil {
			frame := activityJSONEnvelope{
				Success:    false,
				Session:    session,
				CapturedAt: time.Now().Format(time.RFC3339),
				Agents:     []activityJSONAgent{},
				Summary:    map[string]int{},
				Error:      err.Error(),
			}
			if encErr := encoder.Encode(frame); encErr != nil {
				return encErr
			}
			continue
		}
		if encErr := encoder.Encode(buildActivityJSONEnvelope(result)); encErr != nil {
			return encErr
		}
	}
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
	if encErr := encoder.Encode(output); encErr != nil {
		return encErr
	}
	// bd-usgfy: signal non-zero exit after writing the success:false envelope so
	// `ntm activity --json` automation can gate on $? without re-parsing JSON
	// (parity with #125).
	return jsonFailureExit()
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
		return lipgloss.NewStyle().Foreground(activityAgentTypeColor(agentType, t))
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

	// Build table header. Win and PaneID exist because the per-window Pane
	// index collides across windows in multi-window sessions; PaneID is the
	// stable tmux %id.
	header := fmt.Sprintf("%-4s  %-6s  %-7s  %-8s  %-12s  %-10s  %s",
		"Win", "Pane", "PaneID", "Agent", "State", "Velocity", "Duration")
	fmt.Println(headerStyle.Render(header))
	fmt.Println(mutedStyle.Render(strings.Repeat("─", 72)))

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

		row := fmt.Sprintf("%-4d  %-6d  %-7s  %s  %-12s  %-10s  %s",
			agent.WindowIndex,
			agent.Pane,
			agent.PaneID,
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

func activityAgentTypeColor(agentType string, t theme.Theme) lipgloss.Color {
	switch agent.AgentType(agentType).Canonical() {
	case agent.AgentTypeClaudeCode:
		return t.Claude
	case agent.AgentTypeCodex:
		return t.Codex
	case agent.AgentTypeGemini:
		return t.Gemini
	case agent.AgentTypeAntigravity:
		return t.Lavender
	case agent.AgentTypeGrok:
		return t.Pink
	case agent.AgentTypeCursor:
		return t.Cursor
	case agent.AgentTypeWindsurf:
		return t.Windsurf
	case agent.AgentTypeAider:
		return t.Aider
	case agent.AgentTypeOpencode:
		return t.Opencode
	case agent.AgentTypeOllama:
		return t.Ollama
	case agent.AgentTypeUser:
		return t.User
	default:
		return t.Text
	}
}
