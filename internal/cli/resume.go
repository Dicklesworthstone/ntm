package cli

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/handoff"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// ResumeResult is the JSON output for the resume command.
type ResumeResult struct {
	Success   bool                   `json:"success"`
	Action    string                 `json:"action"` // display, spawn, inject
	Handoff   *ResumeHandoffInfo     `json:"handoff,omitempty"`
	SpawnInfo *ResumeSpawnInfo       `json:"spawn_info,omitempty"`
	InjectInfo *ResumeInjectInfo     `json:"inject_info,omitempty"`
	Error     string                 `json:"error,omitempty"`
}

// ResumeHandoffInfo contains handoff details for JSON output.
type ResumeHandoffInfo struct {
	Path       string            `json:"path"`
	Session    string            `json:"session"`
	Goal       string            `json:"goal"`
	Now        string            `json:"now"`
	Status     string            `json:"status"`
	Outcome    string            `json:"outcome,omitempty"`
	Decisions  map[string]string `json:"decisions,omitempty"`
	Next       []string          `json:"next,omitempty"`
	Blockers   []string          `json:"blockers,omitempty"`
	AgeSeconds int64             `json:"age_seconds"`
	FileCount  int               `json:"file_count"`
}

// ResumeSpawnInfo contains spawn operation details.
type ResumeSpawnInfo struct {
	Session    string   `json:"session"`
	PaneCount  int      `json:"pane_count"`
	PaneIDs    []string `json:"pane_ids,omitempty"`
}

// ResumeInjectInfo contains inject operation details.
type ResumeInjectInfo struct {
	Session    string `json:"session"`
	PanesSent  int    `json:"panes_sent"`
	PanesFailed int   `json:"panes_failed"`
}

func newResumeCmd() *cobra.Command {
	var (
		fromPath   string
		spawn      bool
		inject     bool
		dryRun     bool
		jsonFormat bool
		ccCount    int
		codCount   int
		gmiCount   int
	)

	cmd := &cobra.Command{
		Use:   "resume [session]",
		Short: "Resume work from a handoff",
		Long: `Resume work from the most recent handoff for a session,
or from a specific handoff file.

Handoffs capture session state (goal, now, decisions, blockers, next steps)
and can be used to bootstrap new sessions or inject context into existing ones.

Examples:
  ntm resume myproject              # Display latest handoff for session
  ntm resume --from path/to/file    # Display specific handoff file
  ntm resume myproject --spawn --cc=2  # Resume and spawn 2 Claude agents
  ntm resume myproject --inject     # Inject context into existing session
  ntm resume myproject --dry-run    # Show what would be resumed`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionName := ""
			if len(args) > 0 {
				sessionName = args[0]
			}
			return runResume(cmd, sessionName, fromPath, spawn, inject, dryRun,
				ccCount, codCount, gmiCount, jsonFormat)
		},
	}

	cmd.Flags().StringVar(&fromPath, "from", "", "Specific handoff file to resume from")
	cmd.Flags().BoolVar(&spawn, "spawn", false, "Spawn new agents with handoff context")
	cmd.Flags().BoolVar(&inject, "inject", false, "Inject context into existing session")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be resumed without executing")
	cmd.Flags().BoolVar(&jsonFormat, "json", false, "Output as JSON")
	cmd.Flags().IntVar(&ccCount, "cc", 0, "Number of Claude agents to spawn (requires --spawn)")
	cmd.Flags().IntVar(&codCount, "cod", 0, "Number of Codex agents to spawn (requires --spawn)")
	cmd.Flags().IntVar(&gmiCount, "gmi", 0, "Number of Gemini agents to spawn (requires --spawn)")

	return cmd
}

func runResume(cmd *cobra.Command, sessionName, fromPath string, spawn, inject, dryRun bool,
	ccCount, codCount, gmiCount int, jsonFormat bool) error {

	// Check global JSON flag
	if IsJSONOutput() {
		jsonFormat = true
	}

	projectDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	slog.Debug("resume command",
		"session", sessionName,
		"from", fromPath,
		"spawn", spawn,
		"inject", inject,
		"dry_run", dryRun,
	)

	// 1. Find handoff
	reader := handoff.NewReader(projectDir)
	var h *handoff.Handoff
	var path string

	if fromPath != "" {
		// Resolve relative path
		if !filepath.IsAbs(fromPath) {
			fromPath = filepath.Join(projectDir, fromPath)
		}
		h, err = reader.Read(fromPath)
		if err != nil {
			slog.Error("failed to read handoff file",
				"path", fromPath,
				"error", err,
			)
			return fmt.Errorf("failed to read handoff: %w", err)
		}
		path = fromPath
	} else {
		if sessionName == "" {
			// Try to find any handoff
			h, path, err = reader.FindLatestAny()
			if err != nil {
				return fmt.Errorf("failed to find handoff: %w", err)
			}
			if h != nil {
				sessionName = h.Session
			}
		} else {
			h, path, err = reader.FindLatest(sessionName)
			if err != nil {
				return fmt.Errorf("failed to find handoff for session: %w", err)
			}
		}
	}

	if h == nil {
		msg := "no handoff found"
		if sessionName != "" {
			msg = fmt.Sprintf("no handoff found for session: %s", sessionName)
		}
		if jsonFormat {
			return outputResumeJSON(cmd, &ResumeResult{
				Success: false,
				Error:   msg,
			})
		}
		return fmt.Errorf(msg)
	}

	// Validate handoff (warn but continue)
	if errs := h.Validate(); len(errs) > 0 {
		slog.Warn("handoff has validation issues",
			"path", path,
			"error_count", len(errs),
			"first_error", errs[0].Error(),
		)
	}

	// Calculate age
	age := time.Since(h.CreatedAt)

	slog.Info("found handoff",
		"path", path,
		"session", h.Session,
		"age", age,
		"status", h.Status,
	)

	// Override session name if provided via args (not from handoff)
	if sessionName != "" && h.Session != sessionName {
		slog.Debug("overriding session name from handoff",
			"handoff_session", h.Session,
			"arg_session", sessionName,
		)
		// Use arg session name for operations, but keep handoff data
	} else if sessionName == "" {
		sessionName = h.Session
	}

	// Build handoff info for JSON output
	handoffInfo := &ResumeHandoffInfo{
		Path:       path,
		Session:    h.Session,
		Goal:       h.Goal,
		Now:        h.Now,
		Status:     h.Status,
		Outcome:    h.Outcome,
		Decisions:  h.Decisions,
		Next:       h.Next,
		Blockers:   h.Blockers,
		AgeSeconds: int64(age.Seconds()),
		FileCount:  h.TotalFileChanges(),
	}

	// 2. Execute action
	if dryRun {
		return displayHandoff(cmd, h, path, age, handoffInfo, jsonFormat)
	}

	if spawn {
		return spawnWithHandoff(cmd, sessionName, h, path, handoffInfo,
			ccCount, codCount, gmiCount, projectDir, jsonFormat)
	}

	if inject {
		return injectHandoff(cmd, sessionName, h, handoffInfo, jsonFormat)
	}

	// Default: display
	return displayHandoff(cmd, h, path, age, handoffInfo, jsonFormat)
}

func displayHandoff(cmd *cobra.Command, h *handoff.Handoff, path string, age time.Duration,
	info *ResumeHandoffInfo, jsonFormat bool) error {

	if jsonFormat {
		return outputResumeJSON(cmd, &ResumeResult{
			Success: true,
			Action:  "display",
			Handoff: info,
		})
	}

	out := cmd.OutOrStdout()

	fmt.Fprintf(out, "Handoff: %s\n", path)
	fmt.Fprintf(out, "Session: %s\n", h.Session)
	fmt.Fprintf(out, "Created: %s (%s)\n", humanizeDuration(age), h.CreatedAt.Format("2006-01-02 15:04"))
	if h.Status != "" {
		fmt.Fprintf(out, "Status: %s\n", h.Status)
	}
	fmt.Fprintln(out)

	fmt.Fprintf(out, "Goal: %s\n", h.Goal)
	fmt.Fprintf(out, "Now: %s\n", h.Now)

	if len(h.Decisions) > 0 {
		fmt.Fprintln(out, "\nKey Decisions:")
		for k, v := range h.Decisions {
			fmt.Fprintf(out, "  %s: %s\n", k, v)
		}
	}

	if len(h.Next) > 0 {
		fmt.Fprintln(out, "\nNext Steps:")
		for i, step := range h.Next {
			fmt.Fprintf(out, "  %d. %s\n", i+1, step)
		}
	}

	if len(h.Blockers) > 0 {
		fmt.Fprintln(out, "\nBlockers:")
		for _, b := range h.Blockers {
			fmt.Fprintf(out, "  - %s\n", b)
		}
	}

	if h.HasChanges() {
		fmt.Fprintf(out, "\nFile Changes: %d\n", h.TotalFileChanges())
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Run with --spawn to create new agents, or --inject to send to existing session.")

	return nil
}

func spawnWithHandoff(cmd *cobra.Command, sessionName string, h *handoff.Handoff, path string,
	info *ResumeHandoffInfo, ccCount, codCount, gmiCount int, projectDir string, jsonFormat bool) error {

	slog.Info("spawning with handoff",
		"session", sessionName,
		"cc", ccCount,
		"cod", codCount,
		"gmi", gmiCount,
	)

	// Validate counts
	totalAgents := ccCount + codCount + gmiCount
	if totalAgents == 0 {
		return fmt.Errorf("--spawn requires at least one agent count (--cc, --cod, or --gmi)")
	}

	// Format context for injection
	contextText := formatHandoffContext(h)

	// Check if session already exists
	exists, err := tmux.SessionExists(sessionName)
	if err != nil {
		slog.Warn("error checking session existence", "error", err)
	}
	if exists {
		return fmt.Errorf("session %q already exists; use --inject to add context to existing session", sessionName)
	}

	// Build spawn args
	args := []string{"spawn", sessionName}
	if ccCount > 0 {
		args = append(args, fmt.Sprintf("--cc=%d", ccCount))
	}
	if codCount > 0 {
		args = append(args, fmt.Sprintf("--cod=%d", codCount))
	}
	if gmiCount > 0 {
		args = append(args, fmt.Sprintf("--gmi=%d", gmiCount))
	}
	args = append(args, "--no-hooks") // We'll send context ourselves

	slog.Debug("executing spawn", "args", args)

	// Execute spawn via cobra command lookup
	spawnCmd, _, err := cmd.Root().Find(args)
	if err != nil {
		return fmt.Errorf("failed to find spawn command: %w", err)
	}

	// Reset flags and set them properly
	spawnCmd.Flags().Set("cc", fmt.Sprintf("%d", ccCount))
	spawnCmd.Flags().Set("cod", fmt.Sprintf("%d", codCount))
	spawnCmd.Flags().Set("gmi", fmt.Sprintf("%d", gmiCount))
	spawnCmd.Flags().Set("no-hooks", "true")

	if err := spawnCmd.RunE(spawnCmd, []string{sessionName}); err != nil {
		slog.Error("spawn failed", "session", sessionName, "error", err)
		return fmt.Errorf("spawn failed: %w", err)
	}

	// Wait briefly for panes to initialize
	time.Sleep(500 * time.Millisecond)

	// Get panes and send context
	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		slog.Warn("could not get panes after spawn", "error", err)
	}

	var paneIDs []string
	sentCount := 0
	for _, pane := range panes {
		if pane.Type != tmux.AgentUser {
			// Send context to agent panes
			if err := tmux.SendKeys(pane.ID, contextText, true); err != nil {
				slog.Warn("failed to send context to pane", "pane", pane.ID, "error", err)
			} else {
				sentCount++
				paneIDs = append(paneIDs, pane.ID)
			}
		}
	}

	slog.Info("spawn complete with handoff",
		"session", sessionName,
		"panes", len(panes),
		"context_sent", sentCount,
	)

	if jsonFormat {
		return outputResumeJSON(cmd, &ResumeResult{
			Success: true,
			Action:  "spawn",
			Handoff: info,
			SpawnInfo: &ResumeSpawnInfo{
				Session:   sessionName,
				PaneCount: sentCount,
				PaneIDs:   paneIDs,
			},
		})
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Spawned session %q with %d agents and handoff context\n", sessionName, sentCount)
	fmt.Fprintf(cmd.OutOrStdout(), "  Handoff: %s\n", path)
	fmt.Fprintf(cmd.OutOrStdout(), "  Goal: %s\n", truncateForDisplay(h.Goal, 60))
	fmt.Fprintf(cmd.OutOrStdout(), "  Now: %s\n", truncateForDisplay(h.Now, 60))

	return nil
}

func injectHandoff(cmd *cobra.Command, sessionName string, h *handoff.Handoff,
	info *ResumeHandoffInfo, jsonFormat bool) error {

	slog.Info("injecting handoff into session", "session", sessionName)

	// Check session exists
	exists, err := tmux.SessionExists(sessionName)
	if err != nil {
		return fmt.Errorf("error checking session: %w", err)
	}
	if !exists {
		return fmt.Errorf("session %q does not exist; use --spawn to create it", sessionName)
	}

	// Format context
	contextText := formatHandoffContext(h)

	// Get panes
	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		return fmt.Errorf("failed to get session panes: %w", err)
	}

	if len(panes) == 0 {
		return fmt.Errorf("no panes found in session: %s", sessionName)
	}

	// Send to each agent pane
	sent := 0
	failed := 0
	for _, pane := range panes {
		if pane.Type == tmux.AgentUser {
			continue // Skip user pane
		}
		if err := tmux.SendKeys(pane.ID, contextText, true); err != nil {
			slog.Warn("failed to send to pane",
				"pane_id", pane.ID,
				"error", err,
			)
			failed++
		} else {
			sent++
		}
	}

	slog.Info("injected handoff",
		"session", sessionName,
		"panes_sent", sent,
		"panes_failed", failed,
	)

	if jsonFormat {
		return outputResumeJSON(cmd, &ResumeResult{
			Success: true,
			Action:  "inject",
			Handoff: info,
			InjectInfo: &ResumeInjectInfo{
				Session:     sessionName,
				PanesSent:   sent,
				PanesFailed: failed,
			},
		})
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Injected handoff context into %d panes (session: %s)\n", sent, sessionName)
	if failed > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  Warning: %d panes failed to receive context\n", failed)
	}

	return nil
}

// formatHandoffContext formats a handoff for injection into an agent context.
func formatHandoffContext(h *handoff.Handoff) string {
	var sb strings.Builder

	sb.WriteString("=== Resuming from Previous Session ===\n\n")
	sb.WriteString(fmt.Sprintf("**Goal (previous session):** %s\n\n", h.Goal))
	sb.WriteString(fmt.Sprintf("**Now (your first task):** %s\n\n", h.Now))

	if len(h.Decisions) > 0 {
		sb.WriteString("**Key Decisions Made:**\n")
		for k, v := range h.Decisions {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", k, v))
		}
		sb.WriteString("\n")
	}

	if len(h.Next) > 0 {
		sb.WriteString("**Next Steps:**\n")
		for i, step := range h.Next {
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, step))
		}
		sb.WriteString("\n")
	}

	if len(h.Blockers) > 0 {
		sb.WriteString("**Blockers to Address:**\n")
		for _, b := range h.Blockers {
			sb.WriteString(fmt.Sprintf("- %s\n", b))
		}
		sb.WriteString("\n")
	}

	if len(h.Findings) > 0 {
		sb.WriteString("**Important Findings:**\n")
		for k, v := range h.Findings {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", k, v))
		}
		sb.WriteString("\n")
	}

	if h.HasChanges() {
		sb.WriteString(fmt.Sprintf("**Files Changed:** %d files\n", h.TotalFileChanges()))
		if len(h.Files.Created) > 0 {
			sb.WriteString(fmt.Sprintf("  Created: %s\n", strings.Join(h.Files.Created, ", ")))
		}
		if len(h.Files.Modified) > 0 {
			sb.WriteString(fmt.Sprintf("  Modified: %s\n", strings.Join(h.Files.Modified, ", ")))
		}
		sb.WriteString("\n")
	}

	if h.Test != "" {
		sb.WriteString(fmt.Sprintf("**Test Command:** %s\n\n", h.Test))
	}

	sb.WriteString("Please continue from where the previous session left off.\n")

	return sb.String()
}

// humanizeDuration returns a human-readable duration string.
func humanizeDuration(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		mins := int(d.Minutes())
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	}
	if d < 24*time.Hour {
		hours := int(d.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	}
	days := int(d.Hours() / 24)
	if days == 1 {
		return "1 day ago"
	}
	return fmt.Sprintf("%d days ago", days)
}

func outputResumeJSON(cmd *cobra.Command, result *ResumeResult) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}
