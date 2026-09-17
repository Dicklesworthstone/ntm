package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/config"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/handoff"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	sessionpkg "github.com/Dicklesworthstone/ntm/internal/session"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const resumeErrorCodeFailed = "RESUME_FAILED"

// ResumeResult is the JSON output for the resume command.
type ResumeResult struct {
	Success    bool               `json:"success"`
	Action     string             `json:"action"` // display, spawn, inject
	Handoff    *ResumeHandoffInfo `json:"handoff,omitempty"`
	SpawnInfo  *ResumeSpawnInfo   `json:"spawn_info,omitempty"`
	InjectInfo *ResumeInjectInfo  `json:"inject_info,omitempty"`
	ErrorCode  string             `json:"error_code,omitempty"`
	Error      string             `json:"error,omitempty"`
}

type resumeJSONEncodeError struct {
	err error
}

func (e *resumeJSONEncodeError) Error() string { return "encode resume JSON: " + e.err.Error() }
func (e *resumeJSONEncodeError) Unwrap() error { return e.err }

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
	Session     string   `json:"session"`
	PaneCount   int      `json:"pane_count"`
	PanesFailed int      `json:"panes_failed"`
	PaneIDs     []string `json:"pane_ids,omitempty"`
}

// ResumeInjectInfo contains inject operation details.
type ResumeInjectInfo struct {
	Session     string `json:"session"`
	PanesSent   int    `json:"panes_sent"`
	PanesFailed int    `json:"panes_failed"`
}

func newResumeCmd() *cobra.Command {
	var (
		fromPath string
		spawn    bool
		inject   bool
		dryRun   bool
		ccCount  int
		codCount int
		gmiCount int
		agyCount int
		ompCount int
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
			effectiveJSON := IsJSONOutput()
			err := runResume(cmd, sessionName, fromPath, spawn, inject, dryRun,
				resumeSpawnCounts{CC: ccCount, Cod: codCount, Gmi: gmiCount, Agy: agyCount, Omp: ompCount}, effectiveJSON)
			return outputResumeCommandError(cmd, resumeAction(spawn, inject), effectiveJSON, err)
		},
	}

	cmd.Flags().StringVar(&fromPath, "from", "", "Specific handoff file to resume from")
	cmd.Flags().BoolVar(&spawn, "spawn", false, "Spawn new agents with handoff context")
	cmd.Flags().BoolVar(&inject, "inject", false, "Inject context into existing session")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be resumed without executing")
	cmd.Flags().IntVar(&ccCount, "cc", 0, "Number of Claude agents to spawn (requires --spawn)")
	cmd.Flags().IntVar(&codCount, "cod", 0, "Number of Codex agents to spawn (requires --spawn)")
	cmd.Flags().IntVar(&gmiCount, "gmi", 0, "Number of Gemini agents to spawn (requires --spawn)")
	cmd.Flags().IntVar(&agyCount, "agy", 0, "Number of Antigravity agents to spawn (requires --spawn)")
	cmd.Flags().IntVar(&ompCount, "omp", 0, "Number of Oh My Pi (omp) agents to spawn (requires --spawn)")

	return cmd
}

// resumeSpawnCounts carries the per-type agent counts for `resume --spawn`.
type resumeSpawnCounts struct {
	CC, Cod, Gmi, Agy, Omp int
}

func (c resumeSpawnCounts) total() int {
	return c.CC + c.Cod + c.Gmi + c.Agy + c.Omp
}

func runResume(cmd *cobra.Command, sessionName, fromPath string, spawn, inject, dryRun bool,
	counts resumeSpawnCounts, jsonFormat bool) error {

	// Check global JSON flag
	if IsJSONOutput() {
		jsonFormat = true
	}

	if !jsonFormat {
		slog.Debug("resume command",
			"session", sessionName,
			"from", fromPath,
			"spawn", spawn,
			"inject", inject,
			"dry_run", dryRun,
		)
	}

	// 1. Find handoff
	var h *handoff.Handoff
	var path string
	var err error
	projectDir := ""

	if fromPath != "" {
		// Resolve relative path
		if !filepath.IsAbs(fromPath) {
			if fromPath, err = filepath.Abs(fromPath); err != nil {
				return fmt.Errorf("failed to resolve handoff path: %w", err)
			}
		}
		reader := handoff.NewReader("")
		h, err = reader.Read(fromPath)
		if err != nil {
			if !jsonFormat {
				slog.Error("failed to read handoff file",
					"path", fromPath,
					"error", err,
				)
			}
			return fmt.Errorf("failed to read handoff: %w", err)
		}
		path = fromPath
	} else {
		allowPrefix := !jsonFormat && !IsJSONOutput()
		sessionName, projectDir, err = resolveResumeScope(cmd.Context(), sessionName, allowPrefix)
		if err != nil {
			return err
		}
		reader := handoff.NewReader(projectDir)
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
				Success:   false,
				Action:    resumeAction(spawn, inject),
				ErrorCode: resumeErrorCodeFailed,
				Error:     msg,
			})
		}
		return fmt.Errorf("%s", msg)
	}

	// Validate handoff (warn but continue)
	if errs := h.Validate(); len(errs) > 0 {
		if !jsonFormat {
			slog.Warn("handoff has validation issues",
				"path", path,
				"error_count", len(errs),
				"first_error", errs[0].Error(),
			)
		}
	}

	// Calculate age
	age := time.Since(h.CreatedAt)

	if !jsonFormat {
		slog.Info("found handoff",
			"path", path,
			"session", h.Session,
			"age", age,
			"status", h.Status,
		)
	}

	// Override session name if provided via args (not from handoff)
	if sessionName != "" && h.Session != sessionName {
		if !jsonFormat {
			slog.Debug("overriding session name from handoff",
				"handoff_session", h.Session,
				"arg_session", sessionName,
			)
		}
		// Use arg session name for operations, but keep handoff data
	} else if sessionName == "" {
		sessionName = h.Session
	}
	if err := validateResumeSessionName(sessionName); err != nil {
		return err
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
		if fromPath != "" {
			allowPrefix := !jsonFormat && !IsJSONOutput()
			projectDir, err = resolveResumeSourceProjectDir(cmd.Context(), sessionName, h.Session, path, allowPrefix)
			if err != nil {
				return err
			}
		}
		return spawnWithHandoff(cmd, sessionName, h, path, handoffInfo,
			counts, projectDir, jsonFormat)
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
	info *ResumeHandoffInfo, counts resumeSpawnCounts, projectDir string, jsonFormat bool) error {

	if !jsonFormat {
		slog.Info("spawning with handoff",
			"session", sessionName,
			"cc", counts.CC,
			"cod", counts.Cod,
			"gmi", counts.Gmi,
			"agy", counts.Agy,
			"omp", counts.Omp,
		)
	}

	// Validate counts
	if counts.total() == 0 {
		return fmt.Errorf("--spawn requires at least one agent count (--cc, --cod, --gmi, --agy, or --omp)")
	}

	// Format context for injection
	contextText := formatHandoffContext(h)

	// Check if session already exists
	exists, err := tmux.SessionExistsContext(cmd.Context(), sessionName)
	if err != nil {
		return fmt.Errorf("checking resume session: %w", err)
	}
	if exists {
		return fmt.Errorf("session %q already exists; use --inject to add context to existing session", sessionName)
	}

	opts := SpawnOptions{
		Session:            sessionName,
		ProjectDirOverride: projectDir,
		CCCount:            counts.CC,
		CodCount:           counts.Cod,
		GmiCount:           counts.Gmi,
		AgyCount:           counts.Agy,
		OmpCount:           counts.Omp,
		UserPane:           true,
		NoHooks:            true,
	}

	spawnFn := resumeSpawnLifecycle(jsonFormat)
	if err := spawnFn(cmd.Context(), opts); err != nil {
		if !jsonFormat {
			slog.Error("spawn failed", "session", sessionName, "error", err)
		}
		return fmt.Errorf("spawn failed: %w", err)
	}

	// Wait briefly for panes to initialize
	if err := waitContextDelay(cmd.Context(), 500*time.Millisecond); err != nil {
		return fmt.Errorf("resume spawn canceled while waiting for panes: %w", err)
	}

	// Get panes and send context
	panes, err := tmux.GetPanesContext(cmd.Context(), sessionName)
	if err != nil {
		if ctxErr := cmd.Context().Err(); ctxErr != nil {
			return fmt.Errorf("resume spawn canceled while loading panes: %w", ctxErr)
		}
		return fmt.Errorf("could not get panes after spawn: %w", err)
	}

	var paneIDs []string
	sentCount := 0
	failedCount := 0
	var dispatchErr error
	agentPanes := resumeAgentPanes(panes)
	if len(agentPanes) == 0 {
		failedCount = counts.total()
		dispatchErr = errors.New("no agent panes found after spawn; handoff context was not delivered")
	} else {
		service, serviceErr := dispatchsvc.NewService(dispatchsvc.Ports{
			Redactor:  shellFinalMessageRedactor(activeShellDispatchRedactionConfig()),
			Protocols: shellDispatchProtocolPlanner{},
			Deliverer: dispatchsvc.TMUXDeliverer{},
		})
		if serviceErr != nil {
			return fmt.Errorf("preparing handoff dispatch: %w", serviceErr)
		}
		result, executeErr := service.Execute(cmd.Context(), dispatchsvc.Request{
			Session:       sessionName,
			Panes:         agentPanes,
			Message:       contextText,
			Submit:        true,
			StopOnFailure: false,
		})
		if ctxErr := cmd.Context().Err(); ctxErr != nil {
			return fmt.Errorf("resume spawn context dispatch canceled: %w", ctxErr)
		}
		sentCount = result.Delivered
		failedCount = result.Failed + result.Blocked + result.Skipped
		dispatchErr = resumeDispatchError("sending handoff context after spawn", result, executeErr)
		for _, receipt := range result.Receipts {
			if receipt.Status == dispatchsvc.ReceiptDelivered {
				paneIDs = append(paneIDs, receipt.Target.Ref.ID)
			}
		}
	}
	if dispatchErr != nil {
		if !jsonFormat {
			slog.Warn("failed to send handoff context", "session", sessionName, "error", dispatchErr)
		}
	}

	if !jsonFormat {
		slog.Info("spawn complete with handoff",
			"session", sessionName,
			"panes", len(panes),
			"context_sent", sentCount,
		)
	}

	result := &ResumeResult{
		Action:  "spawn",
		Handoff: info,
		SpawnInfo: &ResumeSpawnInfo{
			Session:     sessionName,
			PaneCount:   sentCount,
			PanesFailed: failedCount,
			PaneIDs:     paneIDs,
		},
	}
	if err := finalizeResumeAction(cmd, result, jsonFormat, dispatchErr); err != nil {
		return err
	}
	if jsonFormat {
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Spawned session %q with %d agents and handoff context\n", sessionName, sentCount)
	fmt.Fprintf(cmd.OutOrStdout(), "  Handoff: %s\n", path)
	fmt.Fprintf(cmd.OutOrStdout(), "  Goal: %s\n", truncateForDisplay(h.Goal, 60))
	fmt.Fprintf(cmd.OutOrStdout(), "  Now: %s\n", truncateForDisplay(h.Now, 60))

	return nil
}

func injectHandoff(cmd *cobra.Command, sessionName string, h *handoff.Handoff,
	info *ResumeHandoffInfo, jsonFormat bool) error {

	if !jsonFormat {
		slog.Info("injecting handoff into session", "session", sessionName)
	}

	// Check session exists
	exists, err := tmux.SessionExistsContext(cmd.Context(), sessionName)
	if err != nil {
		return fmt.Errorf("checking resume session: %w", err)
	}
	if !exists {
		return fmt.Errorf("session %q does not exist; use --spawn to create it", sessionName)
	}

	// Format context
	contextText := formatHandoffContext(h)

	// Get panes
	panes, err := tmux.GetPanesContext(cmd.Context(), sessionName)
	if err != nil {
		return fmt.Errorf("failed to get session panes: %w", err)
	}

	if len(panes) == 0 {
		return fmt.Errorf("no panes found in session: %s", sessionName)
	}

	service, err := dispatchsvc.NewService(dispatchsvc.Ports{
		Redactor:  shellFinalMessageRedactor(activeShellDispatchRedactionConfig()),
		Protocols: shellDispatchProtocolPlanner{},
		Deliverer: dispatchsvc.TMUXDeliverer{},
	})
	if err != nil {
		return fmt.Errorf("preparing handoff dispatch: %w", err)
	}
	agentPanes := resumeAgentPanes(panes)
	if len(agentPanes) == 0 {
		return fmt.Errorf("no agent panes found in session: %s", sessionName)
	}
	result, executeErr := service.Execute(cmd.Context(), dispatchsvc.Request{
		Session:       sessionName,
		Panes:         agentPanes,
		Message:       contextText,
		Submit:        true,
		StopOnFailure: false,
	})
	if ctxErr := cmd.Context().Err(); ctxErr != nil {
		return fmt.Errorf("handoff injection canceled: %w", ctxErr)
	}
	sent := result.Delivered
	failed := result.Failed + result.Blocked + result.Skipped
	dispatchErr := resumeDispatchError("injecting handoff context", result, executeErr)
	if dispatchErr != nil {
		if !jsonFormat {
			slog.Warn("handoff dispatch completed with failures", "session", sessionName, "error", dispatchErr)
		}
	}

	if !jsonFormat {
		slog.Info("injected handoff",
			"session", sessionName,
			"panes_sent", sent,
			"panes_failed", failed,
		)
	}

	resumeResult := &ResumeResult{
		Action:  "inject",
		Handoff: info,
		InjectInfo: &ResumeInjectInfo{
			Session:     sessionName,
			PanesSent:   sent,
			PanesFailed: failed,
		},
	}
	if err := finalizeResumeAction(cmd, resumeResult, jsonFormat, dispatchErr); err != nil {
		return err
	}
	if jsonFormat {
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Injected handoff context into %d panes (session: %s)\n", sent, sessionName)
	if failed > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  Warning: %d panes failed to receive context\n", failed)
	}

	return nil
}

func resolveResumeScope(ctx context.Context, sessionName string, allowPrefix bool) (string, string, error) {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		projectDir := GetProjectRoot()
		if projectDir == "" {
			return "", "", fmt.Errorf("getting project root failed")
		}
		return "", projectDir, nil
	}

	resolvedSession, err := normalizeProjectScopedSessionName(ctx, sessionName, allowPrefix)
	if err != nil {
		return "", "", err
	}

	if configuredProjectDir, resolvedStoredSession, matched, err := resolveStoredResumeProjectDir(resolvedSession, allowPrefix); err != nil {
		return "", "", err
	} else if matched {
		return resolvedStoredSession, configuredProjectDir, nil
	}

	projectDir, explicitErr := resolveExplicitProjectDirForSessionContext(ctx, resolvedSession)
	if explicitErr != nil {
		localProjectDir, localSession, matched, err := resolveLocalStoredHandoffProjectDir(resolvedSession, allowPrefix)
		if err != nil {
			return "", "", err
		}
		if matched {
			return localSession, localProjectDir, nil
		}
		return "", "", explicitErr
	}

	reader := handoff.NewReader(projectDir)
	resolvedSession, _, err = resolveStoredHandoffSessionName(resolvedSession, reader, allowPrefix)
	if err != nil {
		return "", "", err
	}
	return resolvedSession, projectDir, nil
}

// resolveLocalStoredHandoffProjectDir permits current-workspace fallback only
// when the workspace already contains a handoff for the requested session.
// This preserves explicit-session isolation while allowing create -> resume to
// work in projects that are not registered in projects_base or live tmux state.
func resolveLocalStoredHandoffProjectDir(sessionName string, allowPrefix bool) (string, string, bool, error) {
	candidates := make([]string, 0, 2)
	if projectRoot := strings.TrimSpace(GetProjectRoot()); projectRoot != "" {
		candidates = append(candidates, projectRoot)
	}
	if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
		candidates = append(candidates, cwd)
	}

	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}

		resolved, matched, err := resolveStoredHandoffSessionName(sessionName, handoff.NewReader(candidate), allowPrefix)
		if err != nil {
			return "", "", false, err
		}
		if matched {
			return candidate, resolved, true, nil
		}
	}
	return "", sessionName, false, nil
}

func resolveResumeSourceProjectDir(ctx context.Context, sessionName, handoffSession, handoffPath string, allowPrefix bool) (string, error) {
	if projectDir, ok := projectDirFromHandoffPath(handoffPath); ok {
		return projectDir, nil
	}

	explicitSession := strings.TrimSpace(sessionName)
	if explicitSession != "" && explicitSession != strings.TrimSpace(handoffSession) {
		return resolveCreationProjectDirForSession(explicitSession)
	}

	effectiveSession := explicitSession
	if effectiveSession == "" {
		effectiveSession = strings.TrimSpace(handoffSession)
	}
	if effectiveSession == "" {
		return "", fmt.Errorf("getting project root failed")
	}

	_, projectDir, err := resolveResumeScope(ctx, effectiveSession, allowPrefix)
	if err != nil {
		return "", err
	}
	return projectDir, nil
}

func projectDirFromHandoffPath(path string) (string, bool) {
	dir := filepath.Clean(strings.TrimSpace(path))
	if dir == "" || dir == "." {
		return "", false
	}

	for {
		if filepath.Base(dir) == "handoffs" && filepath.Base(filepath.Dir(dir)) == ".ntm" {
			projectDir := filepath.Clean(filepath.Dir(filepath.Dir(dir)))
			if projectDir == "" || projectDir == "." {
				return "", false
			}
			return projectDir, true
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func resolveStoredResumeProjectDir(sessionName string, allowPrefix bool) (string, string, bool, error) {
	activeCfg := cfg
	if activeCfg == nil {
		activeCfg = config.Default()
	}
	if activeCfg == nil {
		return "", "", false, nil
	}

	projectDir := strings.TrimSpace(activeCfg.GetProjectDir(sessionName))
	if projectDir == "" {
		return "", "", false, nil
	}

	resolvedSession, matched, err := resolveStoredHandoffSessionName(sessionName, handoff.NewReader(projectDir), allowPrefix)
	if err != nil {
		return "", "", false, err
	}
	if !matched {
		return "", "", false, nil
	}

	return projectDir, resolvedSession, true, nil
}

func resolveStoredHandoffSessionName(sessionName string, reader *handoff.Reader, allowPrefix bool) (string, bool, error) {
	if err := validateResumeSessionName(sessionName); err != nil {
		return "", false, err
	}
	if reader == nil {
		return sessionName, false, nil
	}

	candidates, err := storedSessionCandidatesFromDir(reader.BaseDir())
	if err != nil {
		return "", false, fmt.Errorf("list handoff sessions: %w", err)
	}
	if len(candidates) == 0 {
		return sessionName, false, nil
	}

	resolved, _, err := resolveExplicitSessionName(sessionName, candidates, allowPrefix)
	if err == nil {
		return resolved, true, nil
	}

	var resolveErr *sessionpkg.ResolveExplicitSessionNameError
	if errors.As(err, &resolveErr) && resolveErr.Kind == sessionpkg.ResolveExplicitSessionNameErrorNotFound {
		return sessionName, false, nil
	}
	return "", false, err
}

func validateResumeSessionName(sessionName string) error {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return nil
	}
	if err := tmux.ValidateSessionName(sessionName); err != nil {
		return fmt.Errorf("invalid session name: %w", err)
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

func resumeAgentPanes(panes []tmux.Pane) []tmux.Pane {
	agents := make([]tmux.Pane, 0, len(panes))
	for _, pane := range panes {
		if pane.Type != tmux.AgentUser {
			agents = append(agents, pane)
		}
	}
	return agents
}

func resumeSpawnLifecycle(jsonFormat bool) func(context.Context, SpawnOptions) error {
	if jsonFormat {
		return spawnSessionLogicComposable
	}
	return spawnSessionLogicContext
}

func resumeDispatchError(action string, result dispatchsvc.Result, err error) error {
	failed := result.Failed + result.Blocked + result.Skipped
	if err == nil && failed == 0 {
		return nil
	}
	if err == nil {
		err = errors.New("dispatch did not deliver to every target")
	}
	return fmt.Errorf("%s (%d delivered, %d failed): %w", action, result.Delivered, failed, err)
}

func finalizeResumeAction(cmd *cobra.Command, result *ResumeResult, jsonFormat bool, cause error) error {
	if result == nil {
		return errors.New("resume action result is required")
	}
	result.Success = cause == nil
	if cause != nil {
		result.ErrorCode = resumeErrorCode(cause)
		result.Error = cause.Error()
	}
	if jsonFormat {
		return outputResumeJSON(cmd, result)
	}
	return cause
}

func resumeAction(spawn, inject bool) string {
	if spawn {
		return "spawn"
	}
	if inject {
		return "inject"
	}
	return "display"
}

func resumeErrorCode(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return robot.ErrCodeTimeout
	}
	return resumeErrorCodeFailed
}

func outputResumeCommandError(cmd *cobra.Command, action string, jsonFormat bool, err error) error {
	if err == nil || !jsonFormat || errors.Is(err, errJSONFailure) {
		return err
	}
	var encodeErr *resumeJSONEncodeError
	if errors.As(err, &encodeErr) {
		return err
	}
	emitErr := outputResumeJSON(cmd, &ResumeResult{
		Success:   false,
		Action:    action,
		ErrorCode: resumeErrorCode(err),
		Error:     err.Error(),
	})
	if !errors.Is(emitErr, errJSONFailure) {
		return emitErr
	}
	return errors.Join(emitErr, err)
}

func outputResumeJSON(cmd *cobra.Command, result *ResumeResult) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		return &resumeJSONEncodeError{err: err}
	}
	// bd-oqwmf: signal non-zero exit when the resume envelope reports
	// failure, so `ntm resume --json` automation gating on `$?` no longer
	// silently misses "no handoff found" / validation errors.
	if result != nil && !result.Success {
		return jsonFailureExit()
	}
	return nil
}
