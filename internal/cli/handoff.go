package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/handoff"
)

func newHandoffCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "handoff",
		Short: "Create or manage session handoffs",
		Long: `Create, list, or view handoffs for preserving context across sessions.

Handoffs are compact YAML documents that capture session state:
  - What was accomplished (goal)
  - What to do next (now)
  - Blockers, decisions, and findings
  - File changes and active work items

Examples:
  ntm handoff create myproject --goal "Implemented auth" --now "Add tests"
  ntm handoff create myproject --auto            # Generate from agent output
  ntm handoff list myproject                     # List recent handoffs
  ntm handoff show path/to/handoff.yaml          # View a specific handoff`,
	}

	cmd.AddCommand(newHandoffCreateCmd())
	cmd.AddCommand(newHandoffListCmd())
	cmd.AddCommand(newHandoffShowCmd())
	cmd.AddCommand(newHandoffLedgerCmd())

	return cmd
}

func newHandoffCreateCmd() *cobra.Command {
	var (
		goal        string
		now         string
		fromFile    string
		auto        bool
		description string
		jsonFormat  bool
		output      string
		format      string
		includeGit  bool
	)

	cmd := &cobra.Command{
		Use:   "create [session]",
		Short: "Create a new handoff",
		Long: `Create a handoff for preserving context across sessions.

If --goal and --now are not provided, enters interactive mode.
Use --auto to generate from recent agent output.
Use --from-file to load from an existing YAML file.

Examples:
  ntm handoff create myproject --goal "Completed auth" --now "Add tests"
  ntm handoff create myproject --auto
  ntm handoff create myproject --auto --output - --format markdown  # Output to stdout as markdown
  ntm handoff create myproject --from-file handoff.yaml
  ntm handoff create                     # Interactive mode`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionName := ""
			if len(args) > 0 {
				sessionName = args[0]
			}
			return runHandoffCreate(cmd, sessionName, goal, now, fromFile, auto, description, jsonFormat, output, format, includeGit)
		},
	}

	cmd.Flags().StringVar(&goal, "goal", "", "What this session accomplished")
	cmd.Flags().StringVar(&now, "now", "", "What next session should do first")
	cmd.Flags().StringVar(&fromFile, "from-file", "", "Create from YAML file")
	cmd.Flags().BoolVar(&auto, "auto", false, "Generate from agent output")
	cmd.Flags().StringVar(&description, "description", "", "Short description for filename")
	cmd.Flags().BoolVar(&jsonFormat, "json", false, "Output as JSON (deprecated: use --format json)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output file path (use '-' for stdout)")
	cmd.Flags().StringVar(&format, "format", "yaml", "Output format: yaml, json, or markdown")
	cmd.Flags().BoolVar(&includeGit, "include-git", true, "Include git state in handoff")

	return cmd
}

func newHandoffListCmd() *cobra.Command {
	var (
		limit      int
		jsonFormat bool
	)

	cmd := &cobra.Command{
		Use:   "list [session]",
		Short: "List handoffs for a session",
		Long: `List all handoffs for a session, sorted by date descending.

If no session is specified, lists sessions with handoffs.

Examples:
  ntm handoff list                       # List sessions with handoffs
  ntm handoff list myproject             # List handoffs for myproject
  ntm handoff list myproject --limit 5   # Show only 5 most recent`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionName := ""
			if len(args) > 0 {
				sessionName = args[0]
			}
			return runHandoffList(cmd, sessionName, limit, jsonFormat)
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 10, "Maximum number of handoffs to list")
	cmd.Flags().BoolVar(&jsonFormat, "json", false, "Output as JSON")

	return cmd
}

func newHandoffShowCmd() *cobra.Command {
	var jsonFormat bool

	cmd := &cobra.Command{
		Use:   "show <path>",
		Short: "Show a specific handoff",
		Long: `Display the full contents of a handoff file.

The path can be absolute or relative to the current directory.

Examples:
  ntm handoff show .ntm/handoffs/myproject/2026-01-19_14-30_auth.yaml
  ntm handoff show /full/path/to/handoff.yaml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHandoffShow(cmd, args[0], jsonFormat)
		},
	}

	cmd.Flags().BoolVar(&jsonFormat, "json", false, "Output as JSON")

	return cmd
}

func newHandoffLedgerCmd() *cobra.Command {
	var jsonFormat bool

	cmd := &cobra.Command{
		Use:   "ledger [session]",
		Short: "Show continuity ledger for a session",
		Long: `Show continuity ledger entries for a session.

Ledger files live at:
  .ntm/ledgers/CONTINUITY_{session}.md

Examples:
  ntm handoff ledger myproject
  ntm handoff ledger            # defaults to "general"`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionName := ""
			if len(args) > 0 {
				sessionName = args[0]
			}
			return runHandoffLedger(cmd, sessionName, jsonFormat)
		},
	}

	cmd.Flags().BoolVar(&jsonFormat, "json", false, "Output as JSON")
	return cmd
}

func runHandoffCreate(cmd *cobra.Command, sessionName, goal, now, fromFile string, auto bool, description string, jsonFormat bool, output, format string, includeGit bool) error {
	// Check global JSON flag
	if IsJSONOutput() {
		jsonFormat = true
	}

	if strings.TrimSpace(sessionName) != "" {
		normalizedSession, err := normalizeHandoffSession(sessionName)
		if err != nil {
			return err
		}
		sessionName = normalizedSession
	}

	// Normalize format flag
	if jsonFormat && format == "yaml" {
		format = "json" // --json flag overrides default
	}

	projectDir, err := resolveHandoffProjectDir(sessionName)
	if err != nil {
		return err
	}

	slog.Debug("handoff create command",
		"session", sessionName,
		"goal_provided", goal != "",
		"now_provided", now != "",
		"from_file", fromFile,
		"auto", auto,
		"output", output,
		"format", format,
		"include_git", includeGit,
	)

	writer := handoff.NewWriter(projectDir)
	reader := handoff.NewReader(projectDir)
	generator := handoff.NewGenerator(projectDir)
	var h *handoff.Handoff

	if fromFile != "" {
		// Load from file
		h, err = reader.Read(fromFile)
		if err != nil {
			return fmt.Errorf("failed to read handoff file: %w", err)
		}
		// Override session name if provided
		if sessionName != "" {
			h.Session = sessionName
		}
	} else if auto {
		// Auto-generate using GenerateHandoff
		if sessionName == "" {
			sessionName = "general"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		agentName := ""
		if sessionName != "" && sessionName != "general" {
			if info, err := agentmail.LoadBestSessionAgent(sessionName, projectDir); err == nil && info != nil {
				agentName = info.AgentName
			} else {
				// Fallback to session name for reservation lookups when no registry exists.
				agentName = sessionName
			}
		}

		transferTTLSeconds := 0
		if cfg != nil && cfg.FileReservation.DefaultTTLMin > 0 {
			transferTTLSeconds = cfg.FileReservation.DefaultTTLMin * 60
		}

		opts := handoff.GenerateHandoffOptions{
			SessionName:          sessionName,
			ProjectKey:           projectDir,
			AgentName:            agentName,
			TransferTTLSeconds:   transferTTLSeconds,
			TransferGraceSeconds: 2,
		}
		h, err = generator.GenerateHandoff(ctx, opts)
		if err != nil {
			return fmt.Errorf("auto-generation failed: %w", err)
		}
	} else if goal == "" || now == "" {
		// Interactive mode
		if sessionName == "" {
			sessionName = "general"
		}
		h, err = runInteractiveHandoff(sessionName)
		if err != nil {
			return err
		}
	} else {
		// Use provided flags
		if sessionName == "" {
			sessionName = "general"
		}
		h = handoff.New(sessionName)
		h.Goal = goal
		h.Now = now
		h.Status = handoff.StatusComplete
		h.Outcome = handoff.OutcomeSucceeded

		// Enrich with git state if requested
		if includeGit {
			if err := generator.EnrichWithGitState(h); err != nil {
				slog.Warn("git enrichment failed", "error", err)
			}
		}
	}

	// Validate
	if errs := h.Validate(); len(errs) > 0 {
		return fmt.Errorf("validation failed: %v", errs[0])
	}

	// Handle output to stdout
	if output == "-" {
		return outputHandoffToStdout(cmd, h, format)
	}

	// Determine description for filename
	if description == "" {
		description = generateDescription(h.Goal)
	}

	// Write handoff to file
	var path string
	if output != "" {
		// Write to specified path
		path = output
		if err := writer.WriteToPath(h, path); err != nil {
			return fmt.Errorf("failed to write handoff: %w", err)
		}
	} else {
		// Write to auto-generated path
		path, err = writer.Write(h, description)
		if err != nil {
			return fmt.Errorf("failed to write handoff: %w", err)
		}
	}
	reader.InvalidateCache()

	slog.Info("handoff created",
		"path", path,
		"session", sessionName,
		"goal", truncateForDisplay(h.Goal, 50),
	)

	if format == "json" {
		return outputHandoffJSON(cmd, map[string]interface{}{
			"success":       true,
			"path":          path,
			"session":       h.Session,
			"goal":          h.Goal,
			"now":           h.Now,
			"status":        h.Status,
			"file_count":    h.TotalFileChanges(),
			"blocker_count": len(h.Blockers),
		})
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Handoff created: %s\n", path)
	fmt.Fprintf(cmd.OutOrStdout(), "  Session: %s\n", h.Session)
	fmt.Fprintf(cmd.OutOrStdout(), "  Goal: %s\n", truncateForDisplay(h.Goal, 70))
	fmt.Fprintf(cmd.OutOrStdout(), "  Now: %s\n", truncateForDisplay(h.Now, 70))
	return nil
}

func runHandoffLedger(cmd *cobra.Command, sessionName string, jsonFormat bool) error {
	if IsJSONOutput() {
		jsonFormat = true
	}

	sessionName, err := normalizeHandoffSession(sessionName)
	if err != nil {
		return err
	}

	projectDir, err := resolveHandoffProjectDir(sessionName)
	if err != nil {
		return err
	}

	ledgerPath := filepath.Join(projectDir, ".ntm", "ledgers", fmt.Sprintf("CONTINUITY_%s.md", sessionName))
	data, err := os.ReadFile(ledgerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no continuity ledger found for session: %s", sessionName)
		}
		return fmt.Errorf("read ledger: %w", err)
	}

	if jsonFormat {
		payload := map[string]interface{}{
			"session": sessionName,
			"path":    ledgerPath,
			"content": string(data),
		}
		return outputHandoffJSON(cmd, payload)
	}

	_, err = cmd.OutOrStdout().Write(data)
	return err
}

// outputHandoffToStdout outputs the handoff to stdout in the specified format.
func outputHandoffToStdout(cmd *cobra.Command, h *handoff.Handoff, format string) error {
	switch format {
	case "json":
		data, err := json.MarshalIndent(h, "", "  ")
		if err != nil {
			return fmt.Errorf("marshaling json: %w", err)
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(data))
	case "markdown":
		fmt.Fprintln(cmd.OutOrStdout(), formatHandoffMarkdown(h))
	default: // yaml
		data, err := handoff.MarshalYAML(h)
		if err != nil {
			return fmt.Errorf("marshaling yaml: %w", err)
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(data))
	}
	return nil
}

// formatHandoffMarkdown converts a handoff to human-readable markdown.
func formatHandoffMarkdown(h *handoff.Handoff) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Handoff: %s\n\n", h.Session))
	sb.WriteString(fmt.Sprintf("**Status:** %s (%s)\n\n", h.Status, h.Outcome))

	sb.WriteString("## Goal\n")
	sb.WriteString(h.Goal + "\n\n")

	sb.WriteString("## Now\n")
	sb.WriteString(h.Now + "\n\n")

	if len(h.DoneThisSession) > 0 {
		sb.WriteString("## Done This Session\n")
		for _, task := range h.DoneThisSession {
			sb.WriteString(fmt.Sprintf("- %s\n", task.Task))
		}
		sb.WriteString("\n")
	}

	if len(h.Next) > 0 {
		sb.WriteString("## Next Steps\n")
		for _, item := range h.Next {
			sb.WriteString(fmt.Sprintf("- %s\n", item))
		}
		sb.WriteString("\n")
	}

	if len(h.Blockers) > 0 {
		sb.WriteString("## Blockers\n")
		for _, blocker := range h.Blockers {
			sb.WriteString(fmt.Sprintf("- %s\n", blocker))
		}
		sb.WriteString("\n")
	}

	if len(h.Decisions) > 0 {
		sb.WriteString("## Key Decisions\n")
		for key, val := range h.Decisions {
			sb.WriteString(fmt.Sprintf("- **%s:** %s\n", key, val))
		}
		sb.WriteString("\n")
	}

	if h.TotalFileChanges() > 0 {
		sb.WriteString("## File Changes\n")
		if len(h.Files.Created) > 0 {
			sb.WriteString("**Created:**\n")
			for _, f := range h.Files.Created {
				sb.WriteString(fmt.Sprintf("- %s\n", f))
			}
		}
		if len(h.Files.Modified) > 0 {
			sb.WriteString("**Modified:**\n")
			for _, f := range h.Files.Modified {
				sb.WriteString(fmt.Sprintf("- %s\n", f))
			}
		}
		if len(h.Files.Deleted) > 0 {
			sb.WriteString("**Deleted:**\n")
			for _, f := range h.Files.Deleted {
				sb.WriteString(fmt.Sprintf("- %s\n", f))
			}
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

func runInteractiveHandoff(sessionName string) (*handoff.Handoff, error) {
	reader := bufio.NewReader(os.Stdin)
	h := handoff.New(sessionName)

	fmt.Printf("Creating handoff for session: %s\n\n", sessionName)

	// Goal (required)
	fmt.Print("What did this session accomplish?\n> ")
	goal, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("reading goal: %w", err)
	}
	h.Goal = strings.TrimSpace(goal)
	if h.Goal == "" {
		return nil, fmt.Errorf("goal is required")
	}

	// Now (required)
	fmt.Print("\nWhat should the next session do first?\n> ")
	now, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("reading now: %w", err)
	}
	h.Now = strings.TrimSpace(now)
	if h.Now == "" {
		return nil, fmt.Errorf("now is required")
	}

	// Blockers (optional)
	fmt.Print("\nAny blockers? (comma-separated, or empty)\n> ")
	blockersStr, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("reading blockers: %w", err)
	}
	blockersStr = strings.TrimSpace(blockersStr)
	if blockersStr != "" {
		h.Blockers = splitAndTrim(blockersStr, ",")
	}

	// Decisions (optional)
	fmt.Print("\nKey decisions made? (key=value, comma-separated, or empty)\n> ")
	decisionsStr, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("reading decisions: %w", err)
	}
	decisionsStr = strings.TrimSpace(decisionsStr)
	if decisionsStr != "" {
		h.Decisions = make(map[string]string)
		for _, kv := range splitAndTrim(decisionsStr, ",") {
			parts := strings.SplitN(kv, "=", 2)
			if len(parts) == 2 {
				h.Decisions[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
			}
		}
	}

	// Status
	h.Status = handoff.StatusComplete
	h.Outcome = handoff.OutcomeSucceeded
	if len(h.Blockers) > 0 {
		h.Status = handoff.StatusBlocked
		h.Outcome = handoff.OutcomePartialMinus
	}

	return h, nil
}

func runHandoffList(cmd *cobra.Command, sessionName string, limit int, jsonFormat bool) error {
	// Check global JSON flag
	if IsJSONOutput() {
		jsonFormat = true
	}

	if strings.TrimSpace(sessionName) != "" {
		normalizedSession, err := normalizeHandoffSession(sessionName)
		if err != nil {
			return err
		}
		sessionName = normalizedSession
	}

	projectDir, err := resolveHandoffProjectDir(sessionName)
	if err != nil {
		return err
	}

	reader := handoff.NewReader(projectDir)

	slog.Debug("handoff list command",
		"session", sessionName,
		"limit", limit,
	)

	// If no session specified, list all sessions
	if sessionName == "" {
		sessions, err := reader.ListSessions()
		if err != nil {
			return fmt.Errorf("failed to list sessions: %w", err)
		}

		if jsonFormat {
			return outputHandoffJSON(cmd, map[string]interface{}{
				"sessions": sessions,
				"count":    len(sessions),
			})
		}

		if len(sessions) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No handoff sessions found.")
			return nil
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Sessions with handoffs (%d):\n", len(sessions))
		for _, s := range sessions {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", s)
		}
		return nil
	}

	// List handoffs for specific session
	metas, err := reader.ListHandoffs(sessionName)
	if err != nil {
		return fmt.Errorf("failed to list handoffs: %w", err)
	}

	if len(metas) > limit && limit > 0 {
		metas = metas[:limit]
	}

	slog.Debug("listed handoffs",
		"session", sessionName,
		"count", len(metas),
	)

	if jsonFormat {
		// Convert to JSON-friendly format
		handoffs := make([]map[string]interface{}, len(metas))
		for i, m := range metas {
			handoffs[i] = map[string]interface{}{
				"path":    m.Path,
				"session": m.Session,
				"date":    m.Date.Format(time.RFC3339),
				"status":  m.Status,
				"goal":    m.Goal,
				"is_auto": m.IsAuto,
			}
		}
		return outputHandoffJSON(cmd, map[string]interface{}{
			"session":  sessionName,
			"count":    len(metas),
			"handoffs": handoffs,
		})
	}

	if len(metas) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "No handoffs found for session: %s\n", sessionName)
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Session: %s (%d handoffs)\n\n", sessionName, len(metas))
	for _, m := range metas {
		autoTag := ""
		if m.IsAuto {
			autoTag = " [auto]"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  %s  %s%s\n", m.Date.Format("2006-01-02 15:04"), filepath.Base(m.Path), autoTag)
		fmt.Fprintf(cmd.OutOrStdout(), "    Goal: %s\n", truncateForDisplay(m.Goal, 60))
		if m.Status != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "    Status: %s\n", m.Status)
		}
		fmt.Fprintln(cmd.OutOrStdout())
	}

	return nil
}

func runHandoffShow(cmd *cobra.Command, path string, jsonFormat bool) error {
	// Check global JSON flag
	if IsJSONOutput() {
		jsonFormat = true
	}

	// Handle relative paths
	if !filepath.IsAbs(path) {
		resolvedPath, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("resolve handoff path: %w", err)
		}
		path = resolvedPath
	}

	reader := handoff.NewReader(GetProjectRoot())

	h, err := reader.Read(path)
	if err != nil {
		return fmt.Errorf("failed to read handoff: %w", err)
	}

	slog.Debug("handoff show",
		"path", path,
		"session", h.Session,
	)

	if jsonFormat {
		return outputHandoffJSON(cmd, h)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Handoff: %s\n", path)
	fmt.Fprintf(cmd.OutOrStdout(), "Session: %s\n", h.Session)
	if !h.CreatedAt.IsZero() {
		fmt.Fprintf(cmd.OutOrStdout(), "Created: %s\n", h.CreatedAt.Format("2006-01-02 15:04:05"))
	} else if h.Date != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Date: %s\n", h.Date)
	}
	if h.Status != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", h.Status)
	}
	if h.Outcome != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Outcome: %s\n", h.Outcome)
	}
	fmt.Fprintln(cmd.OutOrStdout())

	fmt.Fprintf(cmd.OutOrStdout(), "Goal: %s\n", h.Goal)
	fmt.Fprintf(cmd.OutOrStdout(), "Now: %s\n", h.Now)

	if h.Test != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Test: %s\n", h.Test)
	}

	if len(h.DoneThisSession) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "\nCompleted Tasks:\n")
		for _, task := range h.DoneThisSession {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", task.Task)
			for _, f := range task.Files {
				fmt.Fprintf(cmd.OutOrStdout(), "    * %s\n", f)
			}
		}
	}

	if len(h.Blockers) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "\nBlockers:\n")
		for _, b := range h.Blockers {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", b)
		}
	}

	if len(h.Decisions) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "\nDecisions:\n")
		for k, v := range h.Decisions {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s: %s\n", k, v)
		}
	}

	if len(h.Findings) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "\nFindings:\n")
		for k, v := range h.Findings {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s: %s\n", k, v)
		}
	}

	if len(h.Next) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "\nNext Steps:\n")
		for i, s := range h.Next {
			fmt.Fprintf(cmd.OutOrStdout(), "  %d. %s\n", i+1, s)
		}
	}

	if h.HasChanges() {
		fmt.Fprintf(cmd.OutOrStdout(), "\nFile Changes:\n")
		if len(h.Files.Created) > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "  Created:\n")
			for _, f := range h.Files.Created {
				fmt.Fprintf(cmd.OutOrStdout(), "    + %s\n", f)
			}
		}
		if len(h.Files.Modified) > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "  Modified:\n")
			for _, f := range h.Files.Modified {
				fmt.Fprintf(cmd.OutOrStdout(), "    ~ %s\n", f)
			}
		}
		if len(h.Files.Deleted) > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "  Deleted:\n")
			for _, f := range h.Files.Deleted {
				fmt.Fprintf(cmd.OutOrStdout(), "    - %s\n", f)
			}
		}
	}

	// Integration info
	if len(h.ActiveBeads) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "\nActive Beads:\n")
		for _, b := range h.ActiveBeads {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", b)
		}
	}

	if len(h.AgentMailThreads) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "\nAgent Mail Threads:\n")
		for _, t := range h.AgentMailThreads {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", t)
		}
	}

	// Token info
	if h.TokensUsed > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "\nToken Usage: %d", h.TokensUsed)
		if h.TokensMax > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), " / %d (%.1f%%)", h.TokensMax, h.TokensPct)
		}
		fmt.Fprintln(cmd.OutOrStdout())
	}

	// Agent info
	if h.AgentID != "" || h.AgentType != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "\nAgent Info:\n")
		if h.AgentID != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "  ID: %s\n", h.AgentID)
		}
		if h.AgentType != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "  Type: %s\n", h.AgentType)
		}
		if h.PaneID != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "  Pane: %s\n", h.PaneID)
		}
	}

	return nil
}

// Helper functions

func outputHandoffJSON(cmd *cobra.Command, v interface{}) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(v)
}

var handoffSessionNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func normalizeHandoffSession(sessionName string) (string, error) {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		sessionName = "general"
	}
	if !handoffSessionNameRegex.MatchString(sessionName) {
		return "", fmt.Errorf("invalid session name: %s", sessionName)
	}
	return sessionName, nil
}

func resolveHandoffProjectDir(sessionName string) (string, error) {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" || sessionName == "general" {
		projectDir := GetProjectRoot()
		if projectDir == "" {
			return "", fmt.Errorf("getting project root failed")
		}
		return projectDir, nil
	}
	resolved, err := normalizeProjectScopedSessionName(sessionName, !IsJSONOutput())
	if err != nil {
		return "", err
	}
	sessionName = resolved
	projectDir, err := resolveExplicitProjectDirForSession(sessionName)
	if err != nil {
		return "", err
	}
	projectDir = refineAgentMailProjectKey(sessionName, projectDir)
	return projectDir, nil
}

func generateDescription(goal string) string {
	if goal == "" {
		return "handoff"
	}

	// Lowercase
	desc := strings.ToLower(goal)

	// Take first few words (up to 8)
	words := strings.Fields(desc)
	if len(words) > 8 {
		words = words[:8]
	}
	desc = strings.Join(words, "-")

	// Remove non-alphanumeric except hyphens
	var result strings.Builder
	for _, r := range desc {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			result.WriteRune(r)
		}
	}
	desc = result.String()

	// Collapse multiple hyphens
	for strings.Contains(desc, "--") {
		desc = strings.ReplaceAll(desc, "--", "-")
	}

	// Trim and limit length
	desc = strings.Trim(desc, "-")
	if len(desc) > 30 {
		desc = desc[:30]
		desc = strings.TrimRight(desc, "-")
	}

	if desc == "" {
		desc = "handoff"
	}

	return desc
}

func truncateForDisplay(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

func splitAndTrim(s, sep string) []string {
	parts := strings.Split(s, sep)
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}
