package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/dashboard"
	"github.com/Dicklesworthstone/ntm/internal/watcher"
)

func newDashboardCmd() *cobra.Command {
	var noTUI bool
	var jsonOutput bool
	var debug bool
	var popup bool
	var attentionCursor int64

	cmd := &cobra.Command{
		Use:     "dashboard [session-name]",
		Aliases: []string{"dash", "d"},
		Short:   "Open interactive session dashboard",
		Long: `Open a stunning interactive dashboard for a tmux session.

The dashboard shows:
- Visual grid of all panes with agent types
- Agent counts (Claude, Codex, Gemini)
- Quick actions for zooming and sending commands

If no session is specified:
- Inside tmux: uses the current session
- Outside tmux: shows a session selector

Flags:
  --no-tui    Plain text output (no interactive UI)
  --json      JSON output (implies --no-tui)
  --debug     Enable debug mode with state inspection

Environment:
  CI=1              Auto-selects plain mode
  TERM=dumb         Auto-selects plain mode
  NO_COLOR=1        Disables colors in plain mode
  NTM_TUI_DEBUG=1   Enables debug mode

Examples:
  ntm dashboard myproject
  ntm dash                  # Auto-detect session
  ntm dashboard --no-tui    # Plain text output for scripting
  ntm dashboard --json      # JSON output for automation
  CI=1 ntm dashboard        # Auto-detects plain mode in CI`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var session string
			if len(args) > 0 {
				session = args[0]
			}

			// JSON implies no-tui
			if jsonOutput {
				noTUI = true
			}

			// Auto-detect non-interactive environments
			if !noTUI && shouldUsePlainMode() {
				noTUI = true
			}

			// Enable debug mode via environment variable
			if !debug && isTUIDebugEnabled() {
				debug = true
			}

			if jsonOutput {
				return runDashboardJSON(cmd.OutOrStdout(), cmd.ErrOrStderr(), session)
			}
			if noTUI {
				return runDashboardPlain(cmd.OutOrStdout(), cmd.ErrOrStderr(), session)
			}
			return runDashboard(cmd.OutOrStdout(), cmd.ErrOrStderr(), session, debug, popup, attentionCursor)
		},
	}

	cmd.Flags().BoolVar(&noTUI, "no-tui", false, "Plain text output (no interactive UI)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "JSON output (implies --no-tui)")
	cmd.Flags().BoolVar(&debug, "debug", false, "Enable debug mode with state inspection")
	cmd.Flags().BoolVar(&popup, "popup", false, "Run in popup/overlay mode (Esc closes, zoom focuses pane)")
	cmd.Flags().Int64Var(&attentionCursor, "attention-cursor", 0, "Pre-focus the attention panel on this event cursor")
	cmd.ValidArgsFunction = completeSessionArgs

	return cmd
}

// shouldUsePlainMode checks if plain text mode should be used based on environment
func shouldUsePlainMode() bool {
	return os.Getenv("CI") != "" || os.Getenv("TERM") == "dumb" || os.Getenv("NO_COLOR") != ""
}

// isTUIDebugEnabled checks if TUI debug mode is enabled
func isTUIDebugEnabled() bool {
	return os.Getenv("NTM_TUI_DEBUG") == "1"
}

func popupEnvEnabled() bool {
	value := strings.TrimSpace(os.Getenv("NTM_POPUP"))
	return value != "" && value != "0"
}

func isSessionMissingError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "can't find session") ||
		strings.Contains(msg, "session not found") ||
		strings.Contains(msg, "has-session")
}

func startDashboardReservationWatcher(session, projectDir string) func() {
	if cfg == nil || !cfg.FileReservation.Enabled || !cfg.AgentMail.Enabled {
		return nil
	}

	amOpts := []agentmail.Option{
		agentmail.WithBaseURL(cfg.AgentMail.URL),
		agentmail.WithProjectKey(projectDir),
	}
	if cfg.AgentMail.Token != "" {
		amOpts = append(amOpts, agentmail.WithToken(cfg.AgentMail.Token))
	}
	amClient := agentmail.NewClient(amOpts...)

	watcherCtx, cancelWatcher := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)

		healthCtx, cancelHealth := context.WithTimeout(watcherCtx, 2*time.Second)
		_, err := amClient.HealthCheck(healthCtx)
		cancelHealth()
		if err != nil || watcherCtx.Err() != nil {
			return
		}

		cfgValues := watcher.FileReservationConfigValues{
			Enabled:               cfg.FileReservation.Enabled,
			AutoReserve:           cfg.FileReservation.AutoReserve,
			AutoReleaseIdleMin:    cfg.FileReservation.AutoReleaseIdleMin,
			NotifyOnConflict:      cfg.FileReservation.NotifyOnConflict,
			ExtendOnActivity:      cfg.FileReservation.ExtendOnActivity,
			DefaultTTLMin:         cfg.FileReservation.DefaultTTLMin,
			PollIntervalSec:       cfg.FileReservation.PollIntervalSec,
			CaptureLinesForDetect: cfg.FileReservation.CaptureLinesForDetect,
			Debug:                 cfg.FileReservation.Debug,
		}

		conflictCallback := func(conflict watcher.FileConflict) {
			if cfg.FileReservation.Debug {
				log.Printf("[FileReservation] Conflict: %s requested by %s, held by %v",
					conflict.Path, conflict.RequestorAgent, conflict.Holders)
			}
			// TODO: Integrate with dashboard notification system
		}

		reservationWatcher := watcher.NewFileReservationWatcherFromConfig(
			cfgValues,
			amClient,
			projectDir,
			session, // Use session name as agent name
			conflictCallback,
		)
		if reservationWatcher == nil {
			return
		}

		reservationWatcher.Start(watcherCtx)
		defer reservationWatcher.Stop()

		if cfg.FileReservation.Debug {
			log.Printf("[FileReservation] Watcher started for session %s", session)
		}

		<-watcherCtx.Done()
	}()

	return func() {
		cancelWatcher()
		<-done
	}
}

// runDashboardJSON outputs dashboard data in JSON format
func runDashboardJSON(w io.Writer, errW io.Writer, session string) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	res, err := ResolveSession(session, w)
	if err != nil {
		return err
	}
	if res.Session == "" {
		// Output empty JSON for no session
		fmt.Fprintln(w, "{}")
		return nil
	}
	session = res.Session

	if !tmux.SessionExists(session) {
		return fmt.Errorf("session '%s' not found", session)
	}

	panes, err := tmux.GetPanes(session)
	if err != nil {
		return fmt.Errorf("failed to get panes: %w", err)
	}

	// Build JSON structure
	type PaneInfo struct {
		ID      string   `json:"id"`
		Index   int      `json:"index"`
		Type    string   `json:"type"`
		Variant string   `json:"variant,omitempty"`
		Tags    []string `json:"tags,omitempty"`
		Command string   `json:"command,omitempty"`
		Width   int      `json:"width"`
		Height  int      `json:"height"`
		Active  bool     `json:"active"`
	}

	type DashboardOutput struct {
		Session    string         `json:"session"`
		PaneCount  int            `json:"pane_count"`
		AgentCount map[string]int `json:"agent_counts"`
		Panes      []PaneInfo     `json:"panes"`
	}

	counts := make(map[string]int)
	paneInfos := make([]PaneInfo, 0, len(panes))
	for _, p := range panes {
		agentType := string(p.Type)
		counts[agentType]++
		paneInfos = append(paneInfos, PaneInfo{
			ID:      p.ID,
			Index:   p.Index,
			Type:    agentType,
			Variant: p.Variant,
			Tags:    p.Tags,
			Command: p.Command,
			Width:   p.Width,
			Height:  p.Height,
			Active:  p.Active,
		})
	}

	out := DashboardOutput{
		Session:    session,
		PaneCount:  len(panes),
		AgentCount: counts,
		Panes:      paneInfos,
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// runDashboardPlain outputs dashboard data in plain text
func runDashboardPlain(w io.Writer, errW io.Writer, session string) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	res, err := ResolveSession(session, w)
	if err != nil {
		return err
	}
	if res.Session == "" {
		return nil
	}
	session = res.Session

	if !tmux.SessionExists(session) {
		return fmt.Errorf("session '%s' not found", session)
	}

	panes, err := tmux.GetPanes(session)
	if err != nil {
		return fmt.Errorf("failed to get panes: %w", err)
	}

	// Count agents by type
	counts := make(map[string]int)
	for _, p := range panes {
		counts[string(p.Type)]++
	}

	// Header
	fmt.Fprintf(w, "Session: %s\n", session)
	fmt.Fprintf(w, "Panes: %d\n", len(panes))
	fmt.Fprintf(w, "Agents: Claude=%d Codex=%d Gemini=%d Cursor=%d Windsurf=%d Aider=%d Other=%d\n",
		counts[string(tmux.AgentClaude)],
		counts[string(tmux.AgentCodex)],
		counts[string(tmux.AgentGemini)],
		counts[string(tmux.AgentCursor)],
		counts[string(tmux.AgentWindsurf)],
		counts[string(tmux.AgentAider)],
		counts[string(tmux.AgentUser)]+counts[string(tmux.AgentUnknown)])
	fmt.Fprintln(w, strings.Repeat("-", 60))

	// Pane details
	for _, p := range panes {
		status := "idle"
		if p.Active {
			status = "active"
		}
		tags := ""
		if len(p.Tags) > 0 {
			tags = " [" + strings.Join(p.Tags, ",") + "]"
		}
		variant := ""
		if p.Variant != "" {
			variant = " (" + p.Variant + ")"
		}
		fmt.Fprintf(w, "[%s] %s%s%s - %s (%dx%d)\n",
			p.Type, p.Title, variant, tags, status, p.Width, p.Height)
	}

	return nil
}

func runDashboard(w io.Writer, errW io.Writer, session string, debug bool, popup bool, attentionCursor int64) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	// Enable debug mode via environment variable if --debug flag is set
	if debug {
		os.Setenv("NTM_TUI_DEBUG", "1")
	}

	res, err := ResolveSession(session, w)
	if err != nil {
		return err
	}
	if res.Session == "" {
		return nil
	}
	res.ExplainIfInferred(errW)
	session = res.Session

	// Auto-popup: if we're inside tmux AND inside the same session we're
	// monitoring, launch as an overlay popup instead of consuming a pane.
	// Skip if already in popup mode (--popup flag or NTM_POPUP env) to
	// prevent nested popups.
	if !popup && !popupEnvEnabled() && tmux.InTmux() {
		currentSession := tmux.GetCurrentSession()
		if currentSession == session {
			return launchOverlayPopup(session, "", attentionCursor)
		}
	}

	prefetchCtx, cancelPrefetch := context.WithTimeout(context.Background(), 500*time.Millisecond)
	initialPanes, err := tmux.GetPanesWithActivityContext(prefetchCtx, session)
	cancelPrefetch()
	if err != nil {
		if isSessionMissingError(err) {
			return fmt.Errorf("session '%s' not found", session)
		}
		// Fall back to the original lazy dashboard fetch path for transient tmux
		// errors or slow prefetches so startup optimization never changes results.
		initialPanes = nil
	}

	projectDir := ""
	if cfg != nil {
		projectDir = cfg.GetProjectDir(session)
	} else {
		// Fallback to default if config not loaded
		projectDir = config.Default().GetProjectDir(session)
	}

	// Validate project directory exists, warn if not
	if _, err := os.Stat(projectDir); os.IsNotExist(err) {
		fmt.Fprintf(errW, "Warning: project directory does not exist: %s\n", projectDir)
		fmt.Fprintf(errW, "Some features (beads, file tracking) may not work correctly.\n")
		fmt.Fprintf(errW, "Check your projects_base setting in config: ntm config show\n\n")
	}

	if stopWatcher := startDashboardReservationWatcher(session, projectDir); stopWatcher != nil {
		defer stopWatcher()
	}

	action, err := dashboard.RunWithOptions(session, projectDir, dashboard.RunOptions{
		PopupMode:       popup,
		AttentionCursor: attentionCursor,
		InitialPanes:    initialPanes,
	})
	if err != nil {
		return err
	}
	if action != nil && action.AttachSession != "" {
		return tmux.AttachOrSwitch(action.AttachSession)
	}
	return nil
}
