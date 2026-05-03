package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/output"
	sessionPkg "github.com/Dicklesworthstone/ntm/internal/session"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

type ensembleStatusCounts struct {
	Pending int `json:"pending" yaml:"pending"`
	Working int `json:"working" yaml:"working"`
	Done    int `json:"done" yaml:"done"`
	Error   int `json:"error" yaml:"error"`
}

type ensembleBudgetSummary struct {
	MaxTokensPerMode     int `json:"max_tokens_per_mode" yaml:"max_tokens_per_mode"`
	MaxTotalTokens       int `json:"max_total_tokens" yaml:"max_total_tokens"`
	EstimatedTotalTokens int `json:"estimated_total_tokens" yaml:"estimated_total_tokens"`
}

type ensembleAssignmentRow struct {
	ModeID        string `json:"mode_id" yaml:"mode_id"`
	ModeCode      string `json:"mode_code,omitempty" yaml:"mode_code,omitempty"`
	ModeName      string `json:"mode_name,omitempty" yaml:"mode_name,omitempty"`
	AgentType     string `json:"agent_type" yaml:"agent_type"`
	Status        string `json:"status" yaml:"status"`
	TokenEstimate int    `json:"token_estimate" yaml:"token_estimate"`
	PaneName      string `json:"pane_name,omitempty" yaml:"pane_name,omitempty"`
}

type ensembleStatusOutput struct {
	GeneratedAt    time.Time                    `json:"generated_at" yaml:"generated_at"`
	Session        string                       `json:"session" yaml:"session"`
	Exists         bool                         `json:"exists" yaml:"exists"`
	EnsembleName   string                       `json:"ensemble_name,omitempty" yaml:"ensemble_name,omitempty"`
	Question       string                       `json:"question,omitempty" yaml:"question,omitempty"`
	StartedAt      time.Time                    `json:"started_at,omitempty" yaml:"started_at,omitempty"`
	Status         string                       `json:"status,omitempty" yaml:"status,omitempty"`
	SynthesisReady bool                         `json:"synthesis_ready,omitempty" yaml:"synthesis_ready,omitempty"`
	Synthesis      string                       `json:"synthesis,omitempty" yaml:"synthesis,omitempty"`
	Budget         ensembleBudgetSummary        `json:"budget,omitempty" yaml:"budget,omitempty"`
	StatusCounts   ensembleStatusCounts         `json:"status_counts,omitempty" yaml:"status_counts,omitempty"`
	Assignments    []ensembleAssignmentRow      `json:"assignments,omitempty" yaml:"assignments,omitempty"`
	Contributions  *ensemble.ContributionReport `json:"contributions,omitempty" yaml:"contributions,omitempty"`
}

func normalizeEnsembleAgentType(value string) string {
	switch agentpkg.AgentType(value).Canonical() {
	case agentpkg.AgentTypeClaudeCode:
		return "cc"
	case agentpkg.AgentTypeCodex:
		return "cod"
	case agentpkg.AgentTypeGemini:
		return "gmi"
	case agentpkg.AgentTypeCursor:
		return "cursor"
	case agentpkg.AgentTypeWindsurf:
		return "windsurf"
	case agentpkg.AgentTypeAider:
		return "aider"
	case agentpkg.AgentTypeOpencode:
		return "oc"
	case agentpkg.AgentTypeOllama:
		return "ollama"
	default:
		return ""
	}
}

func newEnsembleCmd() *cobra.Command {
	opts := ensembleSpawnOptions{
		Assignment: "affinity",
	}

	cmd := &cobra.Command{
		Use:   "ensemble [ensemble] [question]",
		Short: "Manage reasoning ensembles",
		Long: `Manage and run reasoning ensembles.

Primary usage:
  ntm ensemble <ensemble-name> "<question>"
`,
		Example: `  ntm ensemble project-diagnosis "What are the main issues?"
  ntm ensemble idea-forge "What features should we add next?"
  ntm ensemble spawn mysession --preset project-diagnosis --question "..."`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			if len(args) < 2 {
				return fmt.Errorf("ensemble name and question required (usage: ntm ensemble <ensemble-name> <question>)")
			}

			projectDir, err := resolveEnsembleProjectDir(opts.Project)
			if err != nil {
				if IsJSONOutput() {
					_ = output.PrintJSON(output.NewError(err.Error()))
				}
				return err
			}
			opts.Project = projectDir

			if err := tmux.EnsureInstalled(); err != nil {
				if IsJSONOutput() {
					_ = output.PrintJSON(output.NewError(err.Error()))
				}
				return err
			}

			baseName := defaultEnsembleSessionName(projectDir)
			opts.Session = uniqueEnsembleSessionName(baseName)
			opts.Preset = args[0]
			opts.Question = strings.Join(args[1:], " ")

			return runEnsembleSpawn(cmd, opts)
		},
	}

	bindEnsembleSharedFlags(cmd, &opts)
	cmd.AddCommand(newEnsembleSpawnCmd())
	cmd.AddCommand(newEnsemblePresetsCmd())
	cmd.AddCommand(newEnsembleExportCmd())
	cmd.AddCommand(newEnsembleImportCmd())
	cmd.AddCommand(newEnsembleStatusCmd())
	cmd.AddCommand(newEnsembleStopCmd())
	cmd.AddCommand(newEnsembleSuggestCmd())
	cmd.AddCommand(newEnsembleEstimateCmd())
	cmd.AddCommand(newEnsembleSynthesizeCmd())
	cmd.AddCommand(newEnsembleCacheCmd())
	cmd.AddCommand(newEnsembleExportFindingsCmd())
	cmd.AddCommand(newEnsembleProvenanceCmd())
	cmd.AddCommand(newEnsembleCompareCmd())
	cmd.AddCommand(newEnsembleResumeCmd())
	cmd.AddCommand(newEnsembleRerunModeCmd())
	cmd.AddCommand(newEnsembleCleanCheckpointsCmd())
	cmd.ValidArgsFunction = completeEnsemblePresetArgs
	return cmd
}

type ensembleStatusOptions struct {
	Format            string
	ShowContributions bool
}

func newEnsembleStatusCmd() *cobra.Command {
	opts := ensembleStatusOptions{
		Format: "table",
	}
	cmd := &cobra.Command{
		Use:   "status [session]",
		Short: "Show status for an ensemble session",
		Long: `Show the current ensemble session state, assignments, and synthesis readiness.

Formats:
  --format=table (default)
  --format=json
  --format=yaml

Use --show-contributions to include mode contribution scores (requires completed outputs).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := ""
			if len(args) > 0 {
				session = args[0]
			}
			res, err := resolveEnsembleStateCommandSession(session, cmd.OutOrStdout())
			if err != nil {
				return err
			}
			if res.Session == "" {
				return nil
			}
			res.ExplainIfInferred(os.Stderr)
			return runEnsembleStatus(cmd.OutOrStdout(), res.Session, opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Format, "format", "f", "table", "Output format: table, json, yaml")
	cmd.Flags().BoolVar(&opts.ShowContributions, "show-contributions", false, "Include mode contribution scores")
	cmd.ValidArgsFunction = completeSessionArgs
	return cmd
}

type ensembleStopOptions struct {
	Force     bool
	NoCollect bool
	Quiet     bool
	Format    string
	Yes       bool
}

type ensembleStopOutput struct {
	GeneratedAt time.Time `json:"generated_at" yaml:"generated_at"`
	Session     string    `json:"session" yaml:"session"`
	Success     bool      `json:"success" yaml:"success"`
	Message     string    `json:"message,omitempty" yaml:"message,omitempty"`
	Captured    int       `json:"captured,omitempty" yaml:"captured,omitempty"`
	Stopped     int       `json:"stopped" yaml:"stopped"`
	Errors      int       `json:"errors,omitempty" yaml:"errors,omitempty"`
	FinalStatus string    `json:"final_status" yaml:"final_status"`
	Error       string    `json:"error,omitempty" yaml:"error,omitempty"`
}

func newEnsembleStopCmd() *cobra.Command {
	opts := ensembleStopOptions{
		Format: "text",
	}

	cmd := &cobra.Command{
		Use:   "stop [session]",
		Short: "Stop an ensemble run gracefully",
		Long: `Stop all agents in an ensemble run and save partial state.

Behavior:
  1. Signal all ensemble agents to stop (SIGTERM)
  2. Wait for graceful shutdown (5s timeout)
  3. Force kill remaining agents
  4. Collect any partial outputs available
  5. Update ensemble state to 'stopped'
  6. Show summary of what was captured

Flags:
  --force        Skip graceful shutdown, force kill immediately
  --no-collect   Don't attempt to collect partial outputs
  --quiet        Minimal output
  --format       Output format: text, json, yaml`,
		Example: `  ntm ensemble stop
  ntm ensemble stop my-ensemble-session
  ntm ensemble stop --force
  ntm ensemble stop --no-collect --quiet`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := ""
			if len(args) > 0 {
				session = args[0]
			}
			res, err := resolveEnsembleStateCommandSession(session, cmd.OutOrStdout())
			if err != nil {
				return err
			}
			if res.Session == "" {
				return nil
			}
			res.ExplainIfInferred(os.Stderr)
			return runEnsembleStop(cmd.OutOrStdout(), res.Session, opts)
		},
	}

	cmd.Flags().BoolVar(&opts.Force, "force", false, "Skip graceful shutdown, force kill immediately")
	cmd.Flags().BoolVar(&opts.NoCollect, "no-collect", false, "Don't attempt to collect partial outputs")
	cmd.Flags().BoolVarP(&opts.Quiet, "quiet", "q", false, "Minimal output")
	cmd.Flags().StringVarP(&opts.Format, "format", "f", "text", "Output format: text, json, yaml")
	cmd.Flags().BoolVarP(&opts.Yes, "yes", "y", false, "Skip confirmation prompt")
	cmd.ValidArgsFunction = completeSessionArgs
	return cmd
}

func runEnsembleStop(w io.Writer, session string, opts ensembleStopOptions) error {
	format := strings.ToLower(strings.TrimSpace(opts.Format))
	if format == "" {
		format = "text"
	}
	if jsonOutput {
		format = "json"
	}

	state, sessionLive, err := loadEnsembleStateWithRuntimePresence(session)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if !sessionLive {
				return fmt.Errorf("session '%s' not found", session)
			}
			return fmt.Errorf("no ensemble running in session '%s'", session)
		}
		return fmt.Errorf("load session: %w", err)
	}

	// Check if already stopped
	if state.Status.IsTerminal() {
		result := ensembleStopOutput{
			GeneratedAt: output.Timestamp(),
			Session:     session,
			Success:     true,
			Message:     fmt.Sprintf("Ensemble already in terminal state: %s", state.Status),
			FinalStatus: state.Status.String(),
		}
		return renderEnsembleStopOutput(w, result, format, opts.Quiet)
	}

	if !sessionLive {
		prevStatus := state.Status.String()
		state.Status = ensemble.EnsembleStopped
		if err := ensemble.SaveSession(session, state); err != nil {
			return fmt.Errorf("save stopped state: %w", err)
		}
		return renderEnsembleStopOutput(w, ensembleStopOutput{
			GeneratedAt: output.Timestamp(),
			Session:     session,
			Success:     true,
			Message:     fmt.Sprintf("Ensemble session already absent; marked state stopped (previously %s)", prevStatus),
			Stopped:     0,
			FinalStatus: ensemble.EnsembleStopped.String(),
		}, format, opts.Quiet)
	}

	slog.Default().Info("ensemble stop initiated",
		"session", session,
		"force", opts.Force,
		"no_collect", opts.NoCollect,
	)

	// Confirm before destructive action (unless --yes or --force)
	if !opts.Yes && !opts.Force && !opts.Quiet {
		agentCount := len(state.Assignments)
		var confirmed bool
		err := huh.NewConfirm().
			Title(fmt.Sprintf("Stop ensemble session '%s'?", session)).
			Description(fmt.Sprintf("This will terminate %d agents and collect partial outputs.", agentCount)).
			Affirmative("Yes, stop").
			Negative("Cancel").
			Value(&confirmed).
			WithTheme(theme.HuhDestructiveTheme()).
			Run()
		if err != nil {
			return fmt.Errorf("confirmation dialog: %w", err)
		}
		if !confirmed {
			return nil // User cancelled, no error
		}
	}

	var captured int
	var collectErrors []error

	// Collect partial outputs if requested
	if !opts.NoCollect {
		capture := ensemble.NewOutputCapture(tmux.DefaultClient)
		capturedOutputs, err := capture.CaptureAll(state)
		if err != nil {
			slog.Default().Warn("failed to capture partial outputs", "error", err)
			collectErrors = append(collectErrors, err)
		} else {
			captured = len(capturedOutputs)
			slog.Default().Info("captured partial outputs",
				"session", session,
				"count", captured,
			)
		}
	}

	// Get all panes for the session
	panes, err := tmux.GetPanes(session)
	if err != nil {
		slog.Default().Warn("failed to get panes", "error", err)
	}

	stoppedCount := 0
	var stopErrors []error

	// Graceful shutdown: send Ctrl+C to each pane
	if !opts.Force && len(panes) > 0 {
		for _, pane := range panes {
			// Send Ctrl+C (interrupt signal)
			if err := tmux.SendKeys(pane.ID, "C-c", false); err != nil {
				slog.Default().Warn("failed to send interrupt to pane",
					"pane", pane.ID,
					"error", err,
				)
			}
		}

		// Wait for graceful shutdown
		time.Sleep(5 * time.Second)
	}

	// Kill the session (force or after graceful timeout)
	if err := tmux.KillSession(session); err != nil {
		slog.Default().Warn("failed to kill session", "session", session, "error", err)
		stopErrors = append(stopErrors, err)
	} else {
		stoppedCount = len(panes)
		slog.Default().Info("killed session", "session", session, "panes", stoppedCount)
	}

	// Update ensemble state to stopped
	state.Status = ensemble.EnsembleStopped
	if err := ensemble.SaveSession(session, state); err != nil {
		slog.Default().Warn("failed to save stopped state", "error", err)
		stopErrors = append(stopErrors, err)
	}

	// Build result
	result := ensembleStopOutput{
		GeneratedAt: output.Timestamp(),
		Session:     session,
		Success:     len(stopErrors) == 0,
		Captured:    captured,
		Stopped:     stoppedCount,
		Errors:      len(stopErrors) + len(collectErrors),
		FinalStatus: ensemble.EnsembleStopped.String(),
	}

	if len(stopErrors) > 0 {
		result.Error = fmt.Sprintf("%d errors during stop", len(stopErrors))
	}

	if result.Success {
		result.Message = fmt.Sprintf("Ensemble stopped: %d panes terminated", stoppedCount)
		if captured > 0 {
			result.Message += fmt.Sprintf(", %d outputs captured", captured)
		}
	}

	slog.Default().Info("ensemble stop completed",
		"session", session,
		"stopped", stoppedCount,
		"captured", captured,
		"errors", len(stopErrors)+len(collectErrors),
	)

	return renderEnsembleStopOutput(w, result, format, opts.Quiet)
}

func renderEnsembleStopOutput(w io.Writer, payload ensembleStopOutput, format string, quiet bool) error {
	switch format {
	case "json":
		return output.WriteJSON(w, payload, true)
	case "yaml", "yml":
		data, err := yaml.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal yaml: %w", err)
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
		if len(data) == 0 || data[len(data)-1] != '\n' {
			_, err = w.Write([]byte("\n"))
			return err
		}
		return nil
	case "text", "table":
		if quiet {
			if payload.Success {
				fmt.Fprintf(w, "stopped\n")
			} else {
				fmt.Fprintf(w, "error: %s\n", payload.Error)
			}
			return nil
		}

		if payload.Error != "" {
			fmt.Fprintf(w, "Error: %s\n", payload.Error)
		}
		fmt.Fprintf(w, "Session:  %s\n", payload.Session)
		fmt.Fprintf(w, "Status:   %s\n", payload.FinalStatus)
		fmt.Fprintf(w, "Stopped:  %d panes\n", payload.Stopped)
		if payload.Captured > 0 {
			fmt.Fprintf(w, "Captured: %d outputs\n", payload.Captured)
		}
		if payload.Message != "" {
			fmt.Fprintf(w, "\n%s\n", payload.Message)
		}
		return nil
	default:
		return fmt.Errorf("invalid format %q (expected text, json, yaml)", format)
	}
}

func runEnsembleStatus(w io.Writer, session string, opts ensembleStatusOptions) error {
	format := strings.ToLower(strings.TrimSpace(opts.Format))
	if format == "" {
		format = "table"
	}
	if jsonOutput {
		format = "json"
	}

	state, sessionLive, err := loadEnsembleStateWithRuntimePresence(session)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if !sessionLive {
				return fmt.Errorf("session '%s' not found", session)
			}
			return renderEnsembleStatus(w, ensembleStatusOutput{
				GeneratedAt: output.Timestamp(),
				Session:     session,
				Exists:      false,
			}, format)
		}
		return err
	}

	if sessionLive {
		queryStart := time.Now()
		panes, err := tmux.GetPanes(session)
		queryDuration := time.Since(queryStart)
		if err != nil {
			return err
		}
		slog.Default().Info("ensemble status tmux query",
			"session", session,
			"panes", len(panes),
			"duration_ms", queryDuration.Milliseconds(),
		)
	} else {
		slog.Default().Info("ensemble status using persisted state without live tmux session",
			"session", session,
			"status", state.Status,
		)
	}

	catalog, _ := ensemble.GlobalCatalog()
	preset, budget := resolveEnsembleBudget(state)
	assignments, counts := buildEnsembleAssignments(state, catalog, budget.MaxTokensPerMode)

	totalEstimate := budget.MaxTokensPerMode * len(assignments)
	synthesisReady := counts.Done > 0 && counts.Pending == 0 && counts.Working == 0

	slog.Default().Info("ensemble status counts",
		"session", session,
		"pending", counts.Pending,
		"working", counts.Working,
		"done", counts.Done,
		"error", counts.Error,
	)

	outputData := ensembleStatusOutput{
		GeneratedAt:    output.Timestamp(),
		Session:        session,
		Exists:         true,
		EnsembleName:   preset,
		Question:       state.Question,
		StartedAt:      state.CreatedAt,
		Status:         state.Status.String(),
		SynthesisReady: synthesisReady,
		Synthesis:      state.SynthesisStrategy.String(),
		Budget: ensembleBudgetSummary{
			MaxTokensPerMode:     budget.MaxTokensPerMode,
			MaxTotalTokens:       budget.MaxTotalTokens,
			EstimatedTotalTokens: totalEstimate,
		},
		StatusCounts: counts,
		Assignments:  assignments,
	}

	// Compute contributions if requested and there are completed outputs
	if opts.ShowContributions && counts.Done > 0 {
		contributions, err := computeContributions(state, catalog)
		if err != nil {
			slog.Default().Warn("failed to compute contributions", "error", err)
		} else {
			outputData.Contributions = contributions
		}
	}

	return renderEnsembleStatus(w, outputData, format)
}

func ensembleSessionRuntimeExists(session string) bool {
	if strings.TrimSpace(session) == "" {
		return false
	}
	if !tmux.IsInstalled() {
		return false
	}
	return tmux.SessionExists(session)
}

func loadEnsembleStateWithRuntimePresence(session string) (*ensemble.EnsembleSession, bool, error) {
	state, err := ensemble.LoadSession(session)
	if err == nil {
		return state, ensembleSessionRuntimeExists(session), nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, ensembleSessionRuntimeExists(session), err
	}
	return nil, false, err
}

func capturedModeOutputs(captured []ensemble.CapturedOutput) []ensemble.ModeOutput {
	outputs := make([]ensemble.ModeOutput, 0, len(captured))
	for _, cap := range captured {
		if cap.Parsed == nil {
			continue
		}
		parsed := *cap.Parsed
		if parsed.ModeID == "" {
			parsed.ModeID = cap.ModeID
		}
		outputs = append(outputs, parsed)
	}
	return outputs
}

func loadEnsembleModeOutputs(state *ensemble.EnsembleSession, sessionLive bool) ([]ensemble.ModeOutput, error) {
	if state == nil {
		return nil, fmt.Errorf("ensemble session is nil")
	}

	var captured []ensemble.CapturedOutput
	if sessionLive {
		capture := ensemble.NewOutputCapture(tmux.DefaultClient)
		var err error
		captured, err = capture.CaptureAll(state)
		if err != nil {
			slog.Default().Warn("failed to capture live ensemble outputs; falling back to saved outputs",
				"session", state.SessionName,
				"error", err,
			)
		}
		if len(capturedModeOutputs(captured)) == 0 {
			slog.Default().Warn("captured no valid live ensemble outputs; falling back to saved outputs",
				"session", state.SessionName,
			)
		}
	}

	collector, err := ensemble.CollectModeOutputs(state, captured)
	if err != nil {
		return nil, fmt.Errorf("collect mode outputs: %w", err)
	}
	if collector.Count() == 0 {
		if sessionLive {
			return nil, fmt.Errorf("no valid outputs available for session '%s' (errors: %d)", state.SessionName, collector.ErrorCount())
		}
		return nil, fmt.Errorf("no saved outputs available for session '%s' (errors: %d)", state.SessionName, collector.ErrorCount())
	}
	return collector.Outputs, nil
}

// computeContributions collects outputs and computes mode contribution scores.
func computeContributions(state *ensemble.EnsembleSession, catalog *ensemble.ModeCatalog) (*ensemble.ContributionReport, error) {
	outputs, err := loadEnsembleModeOutputs(state, ensembleSessionRuntimeExists(state.SessionName))
	if err != nil {
		return nil, err
	}
	if len(outputs) == 0 {
		return nil, fmt.Errorf("no valid outputs to analyze")
	}

	// Create contribution tracker
	tracker := ensemble.NewContributionTracker()

	// Track original findings
	ensemble.TrackOriginalFindings(tracker, outputs)

	// Perform merge to identify surviving and unique findings
	merged := ensemble.MergeOutputs(outputs, ensemble.DefaultMergeConfig())

	// Track contributions from merged output
	ensemble.TrackContributionsFromMerge(tracker, merged)

	// Set mode names from catalog
	if catalog != nil {
		for _, o := range outputs {
			if mode := catalog.GetMode(o.ModeID); mode != nil {
				tracker.SetModeName(o.ModeID, mode.Name)
			}
		}
	}

	return tracker.GenerateReport(), nil
}

func resolveEnsembleBudget(state *ensemble.EnsembleSession) (string, ensemble.BudgetConfig) {
	name := state.PresetUsed
	if strings.TrimSpace(name) == "" {
		name = "custom"
	}
	budget := ensemble.DefaultBudgetConfig()

	registry, err := ensemble.GlobalEnsembleRegistry()
	if err != nil || registry == nil {
		return name, budget
	}

	if preset := registry.Get(state.PresetUsed); preset != nil {
		name = preset.DisplayName
		if name == "" {
			name = preset.Name
		}
		budget = mergeBudgetDefaults(preset.Budget, budget)
	}

	return name, budget
}

func mergeBudgetDefaults(current, defaults ensemble.BudgetConfig) ensemble.BudgetConfig {
	if current.MaxTokensPerMode == 0 {
		current.MaxTokensPerMode = defaults.MaxTokensPerMode
	}
	if current.MaxTotalTokens == 0 {
		current.MaxTotalTokens = defaults.MaxTotalTokens
	}
	if current.SynthesisReserveTokens == 0 {
		current.SynthesisReserveTokens = defaults.SynthesisReserveTokens
	}
	if current.ContextReserveTokens == 0 {
		current.ContextReserveTokens = defaults.ContextReserveTokens
	}
	if current.TimeoutPerMode == 0 {
		current.TimeoutPerMode = defaults.TimeoutPerMode
	}
	if current.TotalTimeout == 0 {
		current.TotalTimeout = defaults.TotalTimeout
	}
	if current.MaxRetries == 0 {
		current.MaxRetries = defaults.MaxRetries
	}
	return current
}

func buildEnsembleAssignments(state *ensemble.EnsembleSession, catalog *ensemble.ModeCatalog, tokenEstimate int) ([]ensembleAssignmentRow, ensembleStatusCounts) {
	rows := make([]ensembleAssignmentRow, 0, len(state.Assignments))
	var counts ensembleStatusCounts

	for _, assignment := range state.Assignments {
		modeCode := ""
		modeName := ""
		if catalog != nil {
			if mode := catalog.GetMode(assignment.ModeID); mode != nil {
				modeCode = mode.Code
				modeName = mode.Name
			}
		}

		status := assignment.Status.String()
		switch assignment.Status {
		case ensemble.AssignmentPending, ensemble.AssignmentInjecting:
			counts.Pending++
		case ensemble.AssignmentActive:
			counts.Working++
		case ensemble.AssignmentDone:
			counts.Done++
		case ensemble.AssignmentError:
			counts.Error++
		default:
			counts.Pending++
		}

		rows = append(rows, ensembleAssignmentRow{
			ModeID:        assignment.ModeID,
			ModeCode:      modeCode,
			ModeName:      modeName,
			AgentType:     assignment.AgentType,
			Status:        status,
			TokenEstimate: tokenEstimate,
			PaneName:      assignment.PaneName,
		})
	}

	return rows, counts
}

func renderEnsembleStatus(w io.Writer, payload ensembleStatusOutput, format string) error {
	switch format {
	case "json":
		return output.WriteJSON(w, payload, true)
	case "yaml", "yml":
		data, err := yaml.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal yaml: %w", err)
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
		if len(data) == 0 || data[len(data)-1] != '\n' {
			_, err = w.Write([]byte("\n"))
			return err
		}
		return nil
	case "table", "text":
		if !payload.Exists {
			fmt.Fprintf(w, "No ensemble running for session %s\n", payload.Session)
			return nil
		}

		fmt.Fprintf(w, "Session:   %s\n", payload.Session)
		fmt.Fprintf(w, "Ensemble:  %s\n", payload.EnsembleName)
		if strings.TrimSpace(payload.Question) != "" {
			fmt.Fprintf(w, "Question:  %s\n", payload.Question)
		}
		if !payload.StartedAt.IsZero() {
			fmt.Fprintf(w, "Started:   %s\n", payload.StartedAt.Format(time.RFC3339))
		}
		if payload.Status != "" {
			fmt.Fprintf(w, "Status:    %s\n", payload.Status)
		}
		if payload.Synthesis != "" {
			fmt.Fprintf(w, "Synthesis: %s\n", payload.Synthesis)
		}
		fmt.Fprintf(w, "Ready:     %t\n", payload.SynthesisReady)
		fmt.Fprintf(w, "Budget:    %d per mode, %d total (est %d)\n",
			payload.Budget.MaxTokensPerMode,
			payload.Budget.MaxTotalTokens,
			payload.Budget.EstimatedTotalTokens,
		)
		fmt.Fprintf(w, "Counts:    pending=%d working=%d done=%d error=%d\n\n",
			payload.StatusCounts.Pending,
			payload.StatusCounts.Working,
			payload.StatusCounts.Done,
			payload.StatusCounts.Error,
		)

		table := output.NewTable(w, "MODE", "CODE", "AGENT", "STATUS", "TOKENS", "PANE")
		for _, row := range payload.Assignments {
			table.AddRow(row.ModeID, row.ModeCode, row.AgentType, row.Status, fmt.Sprintf("%d", row.TokenEstimate), row.PaneName)
		}
		table.Render()

		// Render contribution report if present
		if payload.Contributions != nil && len(payload.Contributions.Scores) > 0 {
			fmt.Fprintf(w, "\nMode Contributions\n")
			fmt.Fprintf(w, "------------------\n")
			fmt.Fprintf(w, "Total Findings: %d (deduped: %d)  Overlap: %.1f%%  Diversity: %.2f\n\n",
				payload.Contributions.TotalFindings,
				payload.Contributions.DedupedFindings,
				payload.Contributions.OverlapRate*100,
				payload.Contributions.DiversityScore,
			)

			ctable := output.NewTable(w, "RANK", "MODE", "SCORE", "FINDINGS", "UNIQUE", "CITATIONS")
			for _, score := range payload.Contributions.Scores {
				name := score.ModeName
				if name == "" {
					name = score.ModeID
				}
				ctable.AddRow(
					fmt.Sprintf("#%d", score.Rank),
					name,
					fmt.Sprintf("%.1f", score.Score),
					fmt.Sprintf("%d/%d", score.FindingsCount, score.OriginalFindings),
					fmt.Sprintf("%d", score.UniqueInsights),
					fmt.Sprintf("%d", score.CitationCount),
				)
			}
			ctable.Render()
		}
		return nil
	default:
		return fmt.Errorf("invalid format %q (expected table, json, yaml)", format)
	}
}

type synthesizeOptions struct {
	Strategy string
	Output   string
	Format   string
	Force    bool
	Verbose  bool
	Explain  bool
	Stream   bool
	RunID    string
	Resume   bool
	UseCache bool
	NoCache  bool
}

func newEnsembleSynthesizeCmd() *cobra.Command {
	opts := synthesizeOptions{
		Format:   "markdown",
		UseCache: true,
	}

	cmd := &cobra.Command{
		Use:   "synthesize [session]",
		Short: "Synthesize outputs from ensemble agents",
		Long: `Trigger synthesis of ensemble outputs.

Collects outputs from all ensemble agents, validates them, and produces a
synthesized analysis using the configured strategy.

Output formats:
  --format=markdown (default) - Human-readable report
  --format=json               - Machine-readable JSON
  --format=yaml               - YAML format

Streaming:
  --stream                    - Emit incremental chunks (use --format=json or --json for JSONL)
  --resume --run-id=<id>      - Resume a streamed run from the last chunk index

Use --force to synthesize even if some agents haven't completed.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateSynthesizeOptions(opts); err != nil {
				return err
			}
			session := ""
			if len(args) > 0 {
				session = args[0]
			}
			if opts.Resume && strings.TrimSpace(opts.RunID) != "" {
				resumeSession, err := resolveSynthesisResumeSession(session, opts.RunID, cmd.OutOrStdout())
				if err != nil {
					return err
				}
				return runEnsembleSynthesize(cmd.OutOrStdout(), resumeSession, opts)
			}
			res, err := resolveEnsembleStateCommandSession(session, cmd.OutOrStdout())
			if err != nil {
				return err
			}
			if res.Session == "" {
				return nil
			}
			res.ExplainIfInferred(os.Stderr)
			return runEnsembleSynthesize(cmd.OutOrStdout(), res.Session, opts)
		},
	}

	cmd.Flags().StringVar(&opts.Strategy, "strategy", "", "Override synthesis strategy")
	cmd.Flags().StringVarP(&opts.Output, "output", "o", "", "Output file path (default: stdout)")
	cmd.Flags().StringVarP(&opts.Format, "format", "f", "markdown", "Output format: markdown, json, yaml")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Synthesize even if some agents incomplete")
	cmd.Flags().BoolVarP(&opts.Verbose, "verbose", "v", false, "Include verbose details in output")
	cmd.Flags().BoolVar(&opts.Explain, "explain", false, "Include detailed reasoning for each conclusion")
	cmd.Flags().BoolVar(&opts.Stream, "stream", false, "Stream synthesis output incrementally")
	cmd.Flags().StringVar(&opts.RunID, "run-id", "", "Checkpoint run ID for streaming resume")
	cmd.Flags().BoolVar(&opts.Resume, "resume", false, "Resume streaming from checkpoint run ID")
	cmd.Flags().BoolVar(&opts.UseCache, "use-cache", true, "Use cached mode outputs when available")
	cmd.Flags().BoolVar(&opts.NoCache, "no-cache", false, "Bypass cached mode outputs")
	cmd.ValidArgsFunction = completeSessionArgs
	return cmd
}

func validateSynthesizeOptions(opts synthesizeOptions) error {
	runID := strings.TrimSpace(opts.RunID)
	if opts.Resume && !opts.Stream {
		return fmt.Errorf("--resume requires --stream")
	}
	if opts.Resume && runID == "" {
		return fmt.Errorf("--resume requires --run-id")
	}
	if !opts.Stream && runID != "" {
		return fmt.Errorf("--run-id requires --stream")
	}
	return nil
}

func runEnsembleSynthesize(w io.Writer, session string, opts synthesizeOptions) error {
	if err := validateSynthesizeOptions(opts); err != nil {
		return err
	}

	state, sessionLive, err := loadEnsembleStateWithRuntimePresence(session)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if !sessionLive {
				return fmt.Errorf("session '%s' not found", session)
			}
			return fmt.Errorf("no ensemble running in session '%s'", session)
		}
		return fmt.Errorf("load session: %w", err)
	}

	// Check if agents are ready
	ready, pending, working := countAgentStates(state)
	if !opts.Force && (pending > 0 || working > 0) {
		return fmt.Errorf("synthesis not ready: %d pending, %d working (use --force to override)", pending, working)
	}
	if ready == 0 && !opts.Force {
		return fmt.Errorf("no completed outputs to synthesize")
	}

	format := strings.ToLower(strings.TrimSpace(opts.Format))
	if format == "" {
		format = "markdown"
	}
	if jsonOutput {
		format = "json"
	}

	logger := slog.Default()
	logger.Info("ensemble synthesis starting",
		"session", session,
		"session_live", sessionLive,
		"ready", ready,
		"pending", pending,
		"working", working,
		"force", opts.Force,
	)

	collector := ensemble.NewOutputCollector(ensemble.DefaultOutputCollectorConfig())

	cacheEnabled := sessionLive && opts.UseCache && !opts.NoCache
	collectedModes := make(map[string]bool)
	cacheFingerprints := make(map[string]ensemble.ModeOutputFingerprint)
	var outputCache *ensemble.ModeOutputCache
	var cacheContextHash string

	if cacheEnabled {
		projectDir, projErr := resolveEnsembleProjectDirForSession(session)
		if projErr != nil {
			logger.Warn("mode output cache disabled (project dir)", "error", projErr)
			cacheEnabled = false
		} else {
			cacheCfg := ensemble.DefaultModeOutputCacheConfig()
			cacheCfg.Enabled = true
			cache, err := ensemble.NewModeOutputCache(projectDir, cacheCfg, logger)
			if err != nil {
				logger.Warn("mode output cache init failed", "error", err)
				cacheEnabled = false
			} else {
				outputCache = cache
			}

			if cacheEnabled {
				catalog, err := ensemble.LoadModeCatalog()
				if err != nil {
					logger.Warn("mode catalog load failed for cache", "error", err)
					cacheEnabled = false
				} else {
					generator := ensemble.NewContextPackGenerator(projectDir, nil, logger)
					pack, packErr := generator.Generate(state.Question, "", ensemble.CacheConfig{Enabled: false})
					if packErr != nil {
						logger.Warn("context pack generation failed for cache", "error", packErr)
						cacheEnabled = false
					} else if pack != nil {
						cacheContextHash = pack.Hash
					}

					if cacheEnabled {
						for _, assignment := range state.Assignments {
							mode := catalog.GetMode(assignment.ModeID)
							if mode == nil {
								logger.Warn("cache skip: mode not found", "mode_id", assignment.ModeID)
								continue
							}
							cfg := ensemble.ModeOutputConfig{
								Question:          state.Question,
								AgentType:         assignment.AgentType,
								SynthesisStrategy: state.SynthesisStrategy.String(),
								SchemaVersion:     ensemble.SchemaVersion,
							}
							fingerprint, err := ensemble.BuildModeOutputFingerprint(cacheContextHash, mode, cfg)
							if err != nil {
								logger.Warn("cache skip: fingerprint error", "mode_id", assignment.ModeID, "error", err)
								continue
							}
							cacheFingerprints[assignment.ModeID] = fingerprint

							lookup := outputCache.Lookup(fingerprint)
							if lookup.Hit {
								if lookup.Output == nil {
									logger.Warn("mode output cache hit but output is nil", "mode_id", assignment.ModeID, "reason", lookup.Reason)
									continue
								}
								if err := lookup.Output.Validate(); err != nil {
									logger.Warn("mode output cache hit but output invalid", "mode_id", assignment.ModeID, "error", err)
									continue
								}
								if err := collector.Add(*lookup.Output); err != nil {
									logger.Warn("cache hit add failed", "mode_id", assignment.ModeID, "error", err)
									continue
								}
								collectedModes[assignment.ModeID] = true
								logger.Info("mode output cache hit", "mode_id", assignment.ModeID, "reason", lookup.Reason)
							} else {
								logger.Info("mode output cache miss", "mode_id", assignment.ModeID, "reason", lookup.Reason)
							}
						}
					}
				}
			}
		}
	}

	// Collect outputs from panes for cache misses when the session is still live.
	var captured []ensemble.CapturedOutput
	if sessionLive && len(collectedModes) < len(state.Assignments) {
		capture := ensemble.NewOutputCapture(tmux.DefaultClient)
		var err error
		captured, err = capture.CaptureAll(state)
		if err != nil {
			if len(collectedModes) == 0 {
				logger.Warn("capture outputs failed; falling back to saved outputs", "error", err)
			} else {
				logger.Warn("capture outputs failed; continuing with cached outputs", "error", err)
			}
		}
	}

	availableOutputs, err := ensemble.CollectModeOutputs(state, captured)
	if err != nil {
		return fmt.Errorf("collect outputs: %w", err)
	}
	for _, modeOutput := range availableOutputs.Outputs {
		if collectedModes[modeOutput.ModeID] {
			continue
		}
		if err := collector.Add(modeOutput); err != nil {
			return fmt.Errorf("add output %s: %w", modeOutput.ModeID, err)
		}
		collectedModes[modeOutput.ModeID] = true
		if cacheEnabled && outputCache != nil {
			fingerprint, ok := cacheFingerprints[modeOutput.ModeID]
			if !ok {
				continue
			}
			outputCopy := modeOutput
			if err := outputCache.Put(fingerprint, &outputCopy); err != nil {
				logger.Warn("mode output cache write failed", "mode_id", modeOutput.ModeID, "error", err)
			}
		}
	}
	for modeID, errs := range availableOutputs.ValidationErrors {
		if collectedModes[modeID] {
			continue
		}
		if _, exists := collector.ValidationErrors[modeID]; !exists {
			collector.ValidationErrors[modeID] = errs
		}
	}

	if collector.Count() == 0 {
		if sessionLive {
			return fmt.Errorf("no valid outputs collected (errors: %d)", collector.ErrorCount())
		}
		return fmt.Errorf("no saved outputs collected (errors: %d)", collector.ErrorCount())
	}

	logger.Info("ensemble outputs collected",
		"session", session,
		"valid", collector.Count(),
		"errors", collector.ErrorCount(),
	)

	// Determine synthesis strategy
	strategy := state.SynthesisStrategy
	if opts.Strategy != "" {
		strategy = ensemble.SynthesisStrategy(opts.Strategy)
	}

	// Build synthesis config
	synthConfig := ensemble.SynthesisConfig{
		Strategy:           strategy,
		MaxFindings:        20,
		MinConfidence:      0.3,
		IncludeExplanation: opts.Explain,
	}

	// Create synthesizer
	synth, err := ensemble.NewSynthesizer(synthConfig)
	if err != nil {
		return fmt.Errorf("create synthesizer: %w", err)
	}

	// Build synthesis input
	input, err := collector.BuildSynthesisInput(state.Question, nil, synthConfig)
	if err != nil {
		return fmt.Errorf("build synthesis input: %w", err)
	}

	if opts.Stream {
		return streamEnsembleSynthesis(w, session, state, collector, synth, input, format, opts)
	}

	// Run synthesis
	result, err := synth.Synthesize(input)
	if err != nil {
		return fmt.Errorf("synthesis failed: %w", err)
	}

	slog.Default().Info("ensemble synthesis completed",
		"session", session,
		"findings", len(result.Findings),
		"risks", len(result.Risks),
		"recommendations", len(result.Recommendations),
		"confidence", float64(result.Confidence),
	)

	// Format output
	outputFormat := ensemble.FormatMarkdown
	switch format {
	case "json":
		outputFormat = ensemble.FormatJSON
	case "yaml", "yml":
		outputFormat = ensemble.FormatYAML
	}

	formatter := ensemble.NewSynthesisFormatter(outputFormat)
	formatter.Verbose = opts.Verbose
	formatter.IncludeAudit = true
	formatter.IncludeExplanation = opts.Explain

	// Determine output destination
	out := io.Writer(w)
	if opts.Output != "" {
		f, err := os.Create(opts.Output)
		if err != nil {
			return fmt.Errorf("create output file: %w", err)
		}
		defer f.Close()
		out = f
	}

	if err := formatter.FormatResult(out, result, input.AuditReport); err != nil {
		return fmt.Errorf("format output: %w", err)
	}

	return nil
}

func streamEnsembleSynthesis(
	w io.Writer,
	session string,
	state *ensemble.EnsembleSession,
	collector *ensemble.OutputCollector,
	synth *ensemble.Synthesizer,
	input *ensemble.SynthesisInput,
	format string,
	opts synthesizeOptions,
) error {
	if opts.Resume && opts.RunID == "" {
		return fmt.Errorf("--resume requires --run-id")
	}

	var (
		store *ensemble.CheckpointStore
		err   error
	)
	runID := strings.TrimSpace(opts.RunID)
	resumeIndex := 0
	if opts.Resume {
		store, _, err = resolveEnsembleCheckpointStoreForRunID(runID)
		if err != nil {
			return fmt.Errorf("open checkpoint store: %w", err)
		}
		if !store.RunExists(runID) {
			return fmt.Errorf("checkpoint run '%s' not found", runID)
		}
		meta, err := store.LoadMetadata(runID)
		if err != nil {
			return fmt.Errorf("load checkpoint metadata: %w", err)
		}
		if checkpointSession := strings.TrimSpace(meta.SessionName); checkpointSession != "" && checkpointSession != session {
			return fmt.Errorf("checkpoint run '%s' belongs to session '%s', not '%s'", runID, checkpointSession, session)
		}
		checkpoint, err := store.LoadSynthesisCheckpoint(runID)
		if err != nil {
			return fmt.Errorf("load synthesis checkpoint: %w", err)
		}
		if checkpointSession := strings.TrimSpace(checkpoint.SessionName); checkpointSession != "" && checkpointSession != session {
			return fmt.Errorf("synthesis checkpoint '%s' belongs to session '%s', not '%s'", runID, checkpointSession, session)
		}
		resumeIndex = checkpoint.LastIndex
	} else {
		store, err = newEnsembleCheckpointStoreForSession(session)
		if err != nil {
			return fmt.Errorf("open checkpoint store: %w", err)
		}
		if runID == "" {
			runID = buildSynthesisRunID(session)
		}
		if store.RunExists(runID) {
			return fmt.Errorf("checkpoint run '%s' already exists (use --resume)", runID)
		}
		meta := buildSynthesisCheckpointMetadata(state, collector, runID)
		if err := store.SaveMetadata(meta); err != nil {
			return fmt.Errorf("save checkpoint metadata: %w", err)
		}
	}

	out := io.Writer(w)
	if opts.Output != "" {
		f, err := os.Create(opts.Output)
		if err != nil {
			return fmt.Errorf("create output file: %w", err)
		}
		defer f.Close()
		out = f
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	startedAt := time.Now()
	var firstChunkAt time.Time
	chunkCount := 0

	slog.Default().Info("ensemble synthesis streaming",
		"session", session,
		"run_id", runID,
		"resume", opts.Resume,
		"start_index", resumeIndex,
		"format", format,
	)

	chunkCh, errCh := synth.StreamSynthesize(ctx, input)
	lastIndex := resumeIndex

	for chunk := range chunkCh {
		var ok bool
		chunk, ok = applyResumeIndex(chunk, resumeIndex)
		if !ok {
			continue
		}
		if firstChunkAt.IsZero() {
			firstChunkAt = time.Now()
		}
		lastIndex = chunk.Index
		chunkCount++
		if err := writeSynthesisChunk(out, chunk, format); err != nil {
			stop()
			return err
		}
	}

	var streamErr error
	if errCh != nil {
		if err, ok := <-errCh; ok && err != nil {
			streamErr = err
		}
	}
	if ctx.Err() != nil {
		streamErr = ctx.Err()
	}

	timeToFirstChunk := time.Duration(0)
	if !firstChunkAt.IsZero() {
		timeToFirstChunk = firstChunkAt.Sub(startedAt)
	}
	streamDuration := time.Since(startedAt)

	if streamErr != nil {
		cancelReason := "error"
		if errors.Is(streamErr, context.Canceled) {
			cancelReason = "canceled"
		} else if errors.Is(streamErr, context.DeadlineExceeded) {
			cancelReason = "timeout"
		}
		slog.Default().Warn("ensemble synthesis streaming interrupted",
			"session", session,
			"run_id", runID,
			"chunk_count", chunkCount,
			"time_to_first_chunk", timeToFirstChunk,
			"duration", streamDuration,
			"reason", cancelReason,
			"error", streamErr,
		)
		saveErr := store.SaveSynthesisCheckpoint(runID, ensemble.SynthesisCheckpoint{
			RunID:       runID,
			SessionName: session,
			LastIndex:   lastIndex,
			Error:       streamErr.Error(),
		})
		if saveErr != nil {
			slog.Default().Warn("failed to save synthesis checkpoint",
				"run_id", runID,
				"error", saveErr,
			)
		}
		printSynthesisResumeHint(session, runID, format)
		return streamErr
	}

	saveErr := store.SaveSynthesisCheckpoint(runID, ensemble.SynthesisCheckpoint{
		RunID:       runID,
		SessionName: session,
		LastIndex:   lastIndex,
	})
	if saveErr != nil {
		slog.Default().Warn("failed to save synthesis checkpoint",
			"run_id", runID,
			"error", saveErr,
		)
	}

	slog.Default().Info("ensemble synthesis streaming completed",
		"session", session,
		"run_id", runID,
		"chunk_count", chunkCount,
		"time_to_first_chunk", timeToFirstChunk,
		"duration", streamDuration,
	)

	return nil
}

func resolveSynthesisResumeSession(session, runID string, _ io.Writer) (string, error) {
	store, _, err := resolveEnsembleCheckpointStoreForRunID(runID)
	if err != nil {
		return "", fmt.Errorf("open checkpoint store: %w", err)
	}
	if !store.RunExists(runID) {
		return "", fmt.Errorf("checkpoint run '%s' not found", runID)
	}
	meta, err := store.LoadMetadata(runID)
	if err != nil {
		return "", fmt.Errorf("load checkpoint metadata: %w", err)
	}
	checkpointSession := strings.TrimSpace(meta.SessionName)
	if checkpointSession == "" {
		return "", fmt.Errorf("checkpoint run '%s' missing session name", runID)
	}

	requested := strings.TrimSpace(session)
	if requested == "" {
		return checkpointSession, nil
	}
	if requested == checkpointSession {
		return checkpointSession, nil
	}
	resolvedRequested, err := normalizeOfflineCapableEnsembleSessionName(requested, !IsJSONOutput())
	if err != nil {
		return "", err
	}
	requested = resolvedRequested
	if requested != checkpointSession {
		return "", fmt.Errorf("checkpoint run '%s' belongs to session '%s', not '%s'", runID, checkpointSession, session)
	}
	return checkpointSession, nil
}

func buildSynthesisRunID(session string) string {
	name := strings.TrimSpace(session)
	if name == "" {
		name = "ensemble"
	}
	name = strings.ReplaceAll(name, " ", "-")
	return fmt.Sprintf("%s-synth-%s", name, time.Now().UTC().Format("20060102-150405"))
}

func buildSynthesisCheckpointMetadata(state *ensemble.EnsembleSession, collector *ensemble.OutputCollector, runID string) ensemble.CheckpointMetadata {
	modeIDs := make([]string, 0, collector.Count())
	for _, output := range collector.Outputs {
		modeIDs = append(modeIDs, output.ModeID)
	}
	sort.Strings(modeIDs)

	meta := ensemble.CheckpointMetadata{
		SessionName:  state.SessionName,
		Question:     state.Question,
		RunID:        runID,
		Status:       state.Status,
		CreatedAt:    state.CreatedAt,
		CompletedIDs: modeIDs,
		PendingIDs:   []string{},
		TotalModes:   len(state.Assignments),
	}

	if meta.TotalModes == 0 {
		meta.TotalModes = len(modeIDs)
	}

	return meta
}

func applyResumeIndex(chunk ensemble.SynthesisChunk, resumeIndex int) (ensemble.SynthesisChunk, bool) {
	if resumeIndex <= 0 {
		return chunk, true
	}
	if chunk.Index <= resumeIndex {
		return chunk, false
	}
	return chunk, true
}

func writeSynthesisChunk(w io.Writer, chunk ensemble.SynthesisChunk, format string) error {
	if format == "json" {
		data, err := json.Marshal(chunk)
		if err != nil {
			return fmt.Errorf("marshal chunk: %w", err)
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
		if _, err := w.Write([]byte("\n")); err != nil {
			return err
		}
		return nil
	}

	switch chunk.Type {
	case ensemble.ChunkStatus:
		_, err := fmt.Fprintf(w, "[synthesis] %s\n", chunk.Content)
		return err
	case ensemble.ChunkFinding:
		_, err := fmt.Fprintf(w, "- Finding: %s\n", chunk.Content)
		return err
	case ensemble.ChunkRisk:
		_, err := fmt.Fprintf(w, "- Risk: %s\n", chunk.Content)
		return err
	case ensemble.ChunkRecommendation:
		_, err := fmt.Fprintf(w, "- Recommendation: %s\n", chunk.Content)
		return err
	case ensemble.ChunkQuestion:
		_, err := fmt.Fprintf(w, "- Question: %s\n", chunk.Content)
		return err
	case ensemble.ChunkExplanation:
		_, err := fmt.Fprintf(w, "\n%s\n", chunk.Content)
		return err
	case ensemble.ChunkComplete:
		_, err := fmt.Fprintf(w, "\nSummary: %s\n", chunk.Content)
		return err
	default:
		_, err := fmt.Fprintf(w, "%s\n", chunk.Content)
		return err
	}
}

func printSynthesisResumeHint(session, runID, format string) {
	formatFlag := ""
	if format == "json" {
		formatFlag = " --format json"
	}
	fmt.Fprintf(os.Stderr, "Resume with: ntm ensemble synthesize %s --stream --resume --run-id %s%s\n", session, runID, formatFlag)
}

type exportFindingsOptions struct {
	Session string
	RunID   string
	All     bool
	DryRun  bool
	Format  string
	Type    string
	IDs     string
}

type exportFindingResult struct {
	FindingID  string `json:"finding_id" yaml:"finding_id"`
	Title      string `json:"title" yaml:"title"`
	Impact     string `json:"impact" yaml:"impact"`
	Confidence string `json:"confidence" yaml:"confidence"`
	Priority   int    `json:"priority" yaml:"priority"`
	BeadID     string `json:"bead_id,omitempty" yaml:"bead_id,omitempty"`
}

type exportFindingsOutput struct {
	GeneratedAt time.Time             `json:"generated_at" yaml:"generated_at"`
	Session     string                `json:"session,omitempty" yaml:"session,omitempty"`
	RunID       string                `json:"run_id,omitempty" yaml:"run_id,omitempty"`
	Question    string                `json:"question,omitempty" yaml:"question,omitempty"`
	DryRun      bool                  `json:"dry_run,omitempty" yaml:"dry_run,omitempty"`
	Created     int                   `json:"created,omitempty" yaml:"created,omitempty"`
	Findings    []exportFindingResult `json:"findings" yaml:"findings"`
	Errors      []string              `json:"errors,omitempty" yaml:"errors,omitempty"`
}

type exportFinding struct {
	ID          string
	Finding     ensemble.Finding
	SourceModes []string
	Provenance  *ensemble.ProvenanceChain
}

type exportFindingsContext struct {
	Question   string
	Session    string
	RunID      string
	ProjectDir string
	Outputs    []ensemble.ModeOutput
}

type beadSpec struct {
	Title       string
	Type        string
	Priority    int
	Description string
}

const defaultBrTimeout = 30 * time.Second

var runBrCommand = defaultBrCommand

func newEnsembleExportFindingsCmd() *cobra.Command {
	opts := exportFindingsOptions{
		Format: "text",
		Type:   "task",
	}

	cmd := &cobra.Command{
		Use:   "export-findings [session]",
		Short: "Convert ensemble findings into beads",
		Long: `Convert findings from the latest ensemble run into beads issues.

By default, this pulls findings from the current tmux session. Use --run-id
to export from a checkpoint run instead. Without --all or --ids, an interactive
selector is shown.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := opts.Session
			if len(args) > 0 {
				session = args[0]
			}

			return runEnsembleExportFindings(cmd.OutOrStdout(), session, opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Format, "format", "f", "text", "Output format: text, json, yaml")
	cmd.Flags().StringVar(&opts.RunID, "run-id", "", "Checkpoint run ID to export (overrides session)")
	cmd.Flags().BoolVar(&opts.All, "all", false, "Export all findings without prompting")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Preview beads without creating them")
	cmd.Flags().StringVar(&opts.Type, "type", "task", "Bead type (task, bug, feature, etc.)")
	cmd.Flags().StringVar(&opts.IDs, "ids", "", "Comma-separated finding IDs to export")
	cmd.Flags().StringVarP(&opts.Session, "session", "s", "", "Session name (default: current)")
	cmd.ValidArgsFunction = completeSessionArgs
	return cmd
}

func runEnsembleExportFindings(w io.Writer, session string, opts exportFindingsOptions) error {
	format := strings.ToLower(strings.TrimSpace(opts.Format))
	if format == "" {
		format = "text"
	}
	if jsonOutput {
		format = "json"
	}

	ctx, err := loadExportFindingsContext(w, session, opts)
	if err != nil {
		return err
	}
	if ctx == nil {
		return nil
	}

	findings, err := buildExportFindings(ctx)
	if err != nil {
		return err
	}
	if len(findings) == 0 {
		return fmt.Errorf("no findings available to export")
	}

	selected, err := selectExportFindings(w, findings, opts, format)
	if err != nil {
		return err
	}
	if len(selected) == 0 {
		return fmt.Errorf("no findings selected")
	}

	results := make([]exportFindingResult, 0, len(selected))
	var errs []string
	created := 0

	for _, f := range selected {
		priority := impactToBeadPriority(f.Finding.Impact)
		spec := buildBeadSpec(ctx, f, opts, priority)

		slog.Default().Info("export finding",
			"finding_id", f.ID,
			"impact", string(f.Finding.Impact),
			"priority", priority,
			"dry_run", opts.DryRun,
		)

		result := exportFindingResult{
			FindingID:  f.ID,
			Title:      spec.Title,
			Impact:     string(f.Finding.Impact),
			Confidence: f.Finding.Confidence.String(),
			Priority:   priority,
		}

		if opts.DryRun {
			results = append(results, result)
			continue
		}

		beadID, err := func() (string, error) {
			ctxTimeout, cancel := context.WithTimeout(context.Background(), defaultBrTimeout)
			defer cancel()
			return runBrCreate(ctxTimeout, ctx.ProjectDir, spec)
		}()
		if err != nil {
			errs = append(errs, err.Error())
			slog.Default().Warn("export finding failed",
				"finding_id", f.ID,
				"error", err,
			)
			results = append(results, result)
			continue
		}

		slog.Default().Info("bead created",
			"finding_id", f.ID,
			"bead_id", beadID,
		)

		result.BeadID = beadID
		results = append(results, result)
		created++
	}

	payload := exportFindingsOutput{
		GeneratedAt: output.Timestamp(),
		Session:     ctx.Session,
		RunID:       ctx.RunID,
		Question:    ctx.Question,
		DryRun:      opts.DryRun,
		Created:     created,
		Findings:    results,
	}
	if len(errs) > 0 {
		payload.Errors = errs
	}

	return renderExportFindingsOutput(w, payload, format)
}

func renderExportFindingsOutput(w io.Writer, payload exportFindingsOutput, format string) error {
	switch format {
	case "json":
		return output.WriteJSON(w, payload, true)
	case "yaml", "yml":
		data, err := yaml.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal yaml: %w", err)
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
		if len(data) == 0 || data[len(data)-1] != '\n' {
			_, err = w.Write([]byte("\n"))
			return err
		}
		return nil
	default:
		if payload.DryRun {
			fmt.Fprintf(w, "Dry run: %d finding(s) selected\n", len(payload.Findings))
		} else {
			fmt.Fprintf(w, "Exported %d finding(s)\n", payload.Created)
		}
		if payload.Session != "" {
			fmt.Fprintf(w, "Session: %s\n", payload.Session)
		}
		if payload.RunID != "" {
			fmt.Fprintf(w, "Run ID: %s\n", payload.RunID)
		}
		if payload.Question != "" {
			fmt.Fprintf(w, "Question: %s\n", payload.Question)
		}

		table := output.NewTable(w, "FINDING", "IMPACT", "CONF", "PRIORITY", "BEAD")
		for _, row := range payload.Findings {
			beadID := row.BeadID
			if payload.DryRun && beadID == "" {
				beadID = "preview"
			}
			table.AddRow(
				truncateWithEllipsis(row.Title, 60),
				row.Impact,
				row.Confidence,
				fmt.Sprintf("P%d", row.Priority),
				beadID,
			)
		}
		table.Render()

		if len(payload.Errors) > 0 {
			fmt.Fprintf(w, "\nErrors:\n")
			for _, err := range payload.Errors {
				safeErr := html.EscapeString(status.StripANSI(err))
				fmt.Fprintf(w, "  - %s\n", safeErr)
			}
		}
		return nil
	}
}

func loadExportFindingsContext(w io.Writer, session string, opts exportFindingsOptions) (*exportFindingsContext, error) {
	if opts.RunID != "" {
		projectDir := ""
		if strings.TrimSpace(session) != "" {
			resolvedSession, err := normalizeProjectScopedSessionName(session, !IsJSONOutput())
			if err != nil {
				return nil, err
			}
			projectDir, err = resolveExplicitProjectDirForSession(resolvedSession)
			if err != nil {
				return nil, err
			}
		}

		ctx, err := loadExportFindingsFromRun(opts.RunID, projectDir)
		if err != nil {
			return nil, err
		}
		if ctx.ProjectDir == "" {
			projectDir, err = resolveEnsembleProjectDirForSession(ctx.Session)
			if err != nil {
				return nil, err
			}
			ctx.ProjectDir = projectDir
		}
		return ctx, nil
	}

	res, err := resolveEnsembleStateCommandSession(session, w)
	if err != nil {
		return nil, err
	}
	if res.Session == "" {
		return nil, nil
	}
	res.ExplainIfInferred(os.Stderr)
	session = res.Session
	projectDir, err := resolveEnsembleProjectDirForSession(session)
	if err != nil {
		return nil, err
	}

	ctx, err := loadExportFindingsFromSession(session)
	if err != nil {
		return nil, err
	}
	ctx.ProjectDir = projectDir
	return ctx, nil
}

func resolveEnsembleProjectDirForSession(session string) (string, error) {
	session = strings.TrimSpace(session)
	if session == "" {
		projectDir := GetProjectRoot()
		if projectDir == "" {
			return "", fmt.Errorf("getting project root failed")
		}
		return projectDir, nil
	}
	resolved, err := normalizeProjectScopedSessionName(session, !IsJSONOutput())
	if err != nil {
		return "", err
	}
	session = resolved

	return resolveExplicitProjectDirForSession(session)
}

func loadExportFindingsFromRun(runID, projectDir string) (*exportFindingsContext, error) {
	var (
		store              *ensemble.CheckpointStore
		resolvedProjectDir string
		err                error
	)
	if strings.TrimSpace(projectDir) != "" {
		resolvedProjectDir = projectDir
		store, err = newEnsembleCheckpointStoreForProject(projectDir)
	} else {
		store, resolvedProjectDir, err = resolveEnsembleCheckpointStoreForRunID(runID)
	}
	if err != nil {
		return nil, fmt.Errorf("open checkpoint store: %w", err)
	}
	if !store.RunExists(runID) {
		return nil, fmt.Errorf("checkpoint run '%s' not found", runID)
	}

	meta, err := store.LoadMetadata(runID)
	if err != nil {
		return nil, fmt.Errorf("load checkpoint metadata: %w", err)
	}

	outs, err := store.GetCompletedOutputs(runID)
	if err != nil {
		return nil, fmt.Errorf("load checkpoint outputs: %w", err)
	}

	outputs := make([]ensemble.ModeOutput, 0, len(outs))
	for _, o := range outs {
		if o == nil {
			continue
		}
		outputs = append(outputs, *o)
	}
	if len(outputs) == 0 {
		return nil, fmt.Errorf("no completed outputs found for run '%s'", runID)
	}

	return &exportFindingsContext{
		Question:   meta.Question,
		Session:    meta.SessionName,
		RunID:      runID,
		ProjectDir: resolvedProjectDir,
		Outputs:    outputs,
	}, nil
}

func loadExportFindingsFromSession(session string) (*exportFindingsContext, error) {
	state, sessionLive, err := loadEnsembleStateWithRuntimePresence(session)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if !sessionLive {
				return nil, fmt.Errorf("session '%s' not found", session)
			}
			return nil, fmt.Errorf("no ensemble running in session '%s'", session)
		}
		return nil, fmt.Errorf("load session: %w", err)
	}

	outputs, err := loadEnsembleModeOutputs(state, sessionLive)
	if err != nil {
		return nil, err
	}

	return &exportFindingsContext{
		Question: state.Question,
		Session:  session,
		Outputs:  outputs,
	}, nil
}

func buildExportFindings(ctx *exportFindingsContext) ([]exportFinding, error) {
	if ctx == nil {
		return nil, fmt.Errorf("export context is nil")
	}

	modeIDs := make([]string, 0, len(ctx.Outputs))
	for _, o := range ctx.Outputs {
		if strings.TrimSpace(o.ModeID) != "" {
			modeIDs = append(modeIDs, o.ModeID)
		}
	}
	modeIDs = uniqueStrings(modeIDs)

	tracker := ensemble.NewProvenanceTracker(ctx.Question, modeIDs)
	merged := ensemble.MergeOutputsWithProvenance(ctx.Outputs, ensemble.DefaultMergeConfig(), tracker)

	results := make([]exportFinding, 0, len(merged.Findings))
	for i, mf := range merged.Findings {
		id := mf.ProvenanceID
		if id == "" {
			id = fmt.Sprintf("finding-%02d", i+1)
		}
		var chain *ensemble.ProvenanceChain
		if mf.ProvenanceID != "" {
			if c, ok := tracker.GetChain(mf.ProvenanceID); ok {
				chain = c
			}
		}
		results = append(results, exportFinding{
			ID:          id,
			Finding:     mf.Finding,
			SourceModes: mf.SourceModes,
			Provenance:  chain,
		})
	}

	return results, nil
}

func selectExportFindings(w io.Writer, findings []exportFinding, opts exportFindingsOptions, format string) ([]exportFinding, error) {
	if opts.All {
		return findings, nil
	}

	if strings.TrimSpace(opts.IDs) != "" {
		ids := strings.Split(opts.IDs, ",")
		for i := range ids {
			ids[i] = strings.TrimSpace(ids[i])
		}
		return selectFindingsByIDs(findings, ids)
	}

	if (format != "text" && format != "table") || !IsInteractive(w) || IsJSONOutput() {
		return nil, fmt.Errorf("non-interactive mode requires --all or --ids")
	}

	renderFindingsTable(w, findings)

	fmt.Fprint(w, "\nSelect findings (e.g., 1,3-5 or 'all'): ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("read selection: %w", err)
	}
	indices, err := parseSelectionIndices(line, len(findings))
	if err != nil {
		return nil, err
	}

	selected := make([]exportFinding, 0, len(indices))
	for _, idx := range indices {
		if idx <= 0 || idx > len(findings) {
			continue
		}
		selected = append(selected, findings[idx-1])
	}
	return selected, nil
}

func renderFindingsTable(w io.Writer, findings []exportFinding) {
	table := output.NewTable(w, "#", "ID", "IMPACT", "CONF", "EVIDENCE", "FINDING")
	for i, f := range findings {
		evidence := f.Finding.EvidencePointer
		if evidence == "" {
			evidence = "-"
		}
		table.AddRow(
			fmt.Sprintf("%d", i+1),
			f.ID,
			string(f.Finding.Impact),
			f.Finding.Confidence.String(),
			truncateWithEllipsis(evidence, 24),
			truncateWithEllipsis(f.Finding.Finding, 60),
		)
	}
	table.Render()
}

func parseSelectionIndices(input string, max int) ([]int, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, fmt.Errorf("selection cannot be empty")
	}
	if strings.EqualFold(trimmed, "all") {
		indices := make([]int, 0, max)
		for i := 1; i <= max; i++ {
			indices = append(indices, i)
		}
		return indices, nil
	}

	indexSet := make(map[int]struct{})
	parts := strings.Split(trimmed, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			if len(bounds) != 2 {
				return nil, fmt.Errorf("invalid range %q", part)
			}
			start, err := strconv.Atoi(strings.TrimSpace(bounds[0]))
			if err != nil {
				return nil, fmt.Errorf("invalid range start %q", bounds[0])
			}
			end, err := strconv.Atoi(strings.TrimSpace(bounds[1]))
			if err != nil {
				return nil, fmt.Errorf("invalid range end %q", bounds[1])
			}
			if start > end {
				start, end = end, start
			}
			for i := start; i <= end; i++ {
				if i < 1 || i > max {
					return nil, fmt.Errorf("selection %d out of range (1-%d)", i, max)
				}
				indexSet[i] = struct{}{}
			}
			continue
		}

		val, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("invalid selection %q", part)
		}
		if val < 1 || val > max {
			return nil, fmt.Errorf("selection %d out of range (1-%d)", val, max)
		}
		indexSet[val] = struct{}{}
	}

	indices := make([]int, 0, len(indexSet))
	for idx := range indexSet {
		indices = append(indices, idx)
	}
	sort.Ints(indices)
	return indices, nil
}

func selectFindingsByIDs(findings []exportFinding, ids []string) ([]exportFinding, error) {
	selected := make([]exportFinding, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		var matches []exportFinding
		for _, f := range findings {
			if strings.HasPrefix(f.ID, id) {
				matches = append(matches, f)
			}
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("finding id '%s' not found", id)
		}
		if len(matches) > 1 {
			return nil, fmt.Errorf("finding id '%s' is ambiguous", id)
		}
		selected = append(selected, matches[0])
	}
	return selected, nil
}

func impactToBeadPriority(impact ensemble.ImpactLevel) int {
	switch impact {
	case ensemble.ImpactCritical:
		return 0
	case ensemble.ImpactHigh:
		return 1
	case ensemble.ImpactMedium:
		return 2
	case ensemble.ImpactLow:
		return 3
	default:
		return 2
	}
}

func buildBeadSpec(ctx *exportFindingsContext, f exportFinding, opts exportFindingsOptions, priority int) beadSpec {
	title := strings.TrimSpace(f.Finding.Finding)
	if title == "" {
		title = fmt.Sprintf("Finding %s", f.ID)
	}
	title = truncateWithEllipsis(title, 80)

	beadType := strings.TrimSpace(opts.Type)
	if beadType == "" {
		beadType = "task"
	}

	return beadSpec{
		Title:       title,
		Type:        beadType,
		Priority:    priority,
		Description: formatBeadDescription(ctx, f),
	}
}

func formatBeadDescription(ctx *exportFindingsContext, f exportFinding) string {
	var b strings.Builder
	b.WriteString("## Finding\n\n")
	b.WriteString(f.Finding.Finding)
	b.WriteString("\n\n")

	b.WriteString("## Details\n\n")
	b.WriteString(fmt.Sprintf("**Impact:** %s\n\n", string(f.Finding.Impact)))
	b.WriteString(fmt.Sprintf("**Confidence:** %s\n\n", f.Finding.Confidence.String()))
	if strings.TrimSpace(f.Finding.EvidencePointer) != "" {
		b.WriteString(fmt.Sprintf("**Evidence:** `%s`\n\n", f.Finding.EvidencePointer))
	}
	if strings.TrimSpace(f.Finding.Reasoning) != "" {
		b.WriteString(fmt.Sprintf("**Reasoning:** %s\n\n", f.Finding.Reasoning))
	}

	if len(f.SourceModes) > 0 {
		modes := append([]string{}, f.SourceModes...)
		sort.Strings(modes)
		b.WriteString(fmt.Sprintf("**Source Modes:** %s\n\n", strings.Join(modes, ", ")))
	}

	if ctx != nil {
		if ctx.Question != "" {
			b.WriteString(fmt.Sprintf("**Question:** %s\n\n", ctx.Question))
		}
		if ctx.Session != "" {
			b.WriteString(fmt.Sprintf("**Session:** %s\n\n", ctx.Session))
		}
		if ctx.RunID != "" {
			b.WriteString(fmt.Sprintf("**Run ID:** %s\n\n", ctx.RunID))
		}
	}

	if f.Provenance != nil {
		b.WriteString("## Provenance\n\n")
		b.WriteString(fmt.Sprintf("**Provenance ID:** `%s`\n\n", f.Provenance.FindingID))
		if f.Provenance.SourceMode != "" {
			b.WriteString(fmt.Sprintf("**Source Mode:** %s\n\n", f.Provenance.SourceMode))
		}
		if f.Provenance.ContextHash != "" {
			b.WriteString(fmt.Sprintf("**Context Hash:** `%s`\n\n", f.Provenance.ContextHash))
		}
		if f.Provenance.OriginalText != "" && f.Provenance.OriginalText != f.Finding.Finding {
			b.WriteString(fmt.Sprintf("**Original Text:** %s\n\n", f.Provenance.OriginalText))
		}
		if len(f.Provenance.Steps) > 0 {
			b.WriteString("**Lifecycle:**\n")
			for _, step := range f.Provenance.Steps {
				details := step.Details
				if details == "" {
					details = step.Action
				}
				b.WriteString(fmt.Sprintf("- %s/%s: %s (%s)\n", step.Stage, step.Action, details, step.Timestamp.UTC().Format(time.RFC3339)))
			}
			b.WriteString("\n")
		}
	}

	b.WriteString(fmt.Sprintf("---\n*Generated by ntm ensemble export-findings at %s*\n", time.Now().UTC().Format(time.RFC3339)))

	return b.String()
}

func runBrCreate(ctx context.Context, dir string, spec beadSpec) (string, error) {
	args := []string{
		"create",
		"--json",
		"--title", spec.Title,
		"--type", spec.Type,
		"--priority", fmt.Sprintf("%d", spec.Priority),
		"--description", spec.Description,
	}

	outputBytes, err := runBrCommand(ctx, dir, args...)
	if err != nil {
		msg := strings.TrimSpace(string(outputBytes))
		if msg != "" {
			return "", fmt.Errorf("br create failed: %w: %s", err, msg)
		}
		return "", fmt.Errorf("br create failed: %w", err)
	}

	id, err := parseBrCreateID(outputBytes)
	if err != nil {
		return "", err
	}
	return id, nil
}

func parseBrCreateID(outputBytes []byte) (string, error) {
	var list []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(outputBytes, &list); err == nil {
		if len(list) > 0 && list[0].ID != "" {
			return list[0].ID, nil
		}
	}

	var single struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(outputBytes, &single); err == nil {
		if single.ID != "" {
			return single.ID, nil
		}
	}

	return "", fmt.Errorf("no bead ID returned")
}

func defaultBrCommand(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "br", args...)
	cmd.WaitDelay = 2 * time.Second
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = dir
	}
	return cmd.CombinedOutput()
}

func truncateWithEllipsis(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return util.SafeSlice(s, max)
	}
	return util.SafeSlice(s, max-3) + "..."
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	sort.Strings(unique)
	return unique
}

func countAgentStates(state *ensemble.EnsembleSession) (ready, pending, working int) {
	for _, a := range state.Assignments {
		switch a.Status {
		case ensemble.AssignmentDone:
			ready++
		case ensemble.AssignmentPending, ensemble.AssignmentInjecting:
			pending++
		case ensemble.AssignmentActive:
			working++
		}
	}
	return
}

// Provenance command types

type provenanceOptions struct {
	Format  string
	Session string
	All     bool
	Stats   bool
}

type provenanceOutput struct {
	GeneratedAt time.Time                   `json:"generated_at" yaml:"generated_at"`
	FindingID   string                      `json:"finding_id,omitempty" yaml:"finding_id,omitempty"`
	Chain       *ensemble.ProvenanceChain   `json:"chain,omitempty" yaml:"chain,omitempty"`
	Stats       *ensemble.ProvenanceStats   `json:"stats,omitempty" yaml:"stats,omitempty"`
	Chains      []*ensemble.ProvenanceChain `json:"chains,omitempty" yaml:"chains,omitempty"`
	Error       string                      `json:"error,omitempty" yaml:"error,omitempty"`
}

func newEnsembleProvenanceCmd() *cobra.Command {
	opts := provenanceOptions{
		Format: "text",
	}

	cmd := &cobra.Command{
		Use:   "provenance [finding-id]",
		Short: "Show provenance chain for a finding",
		Long: `Display the full provenance chain for a finding.

Shows the finding's origin, transformations, and synthesis usage.

Without a finding-id, use --all to list all tracked findings.
Use --stats to show provenance statistics.

Formats:
  --format=text (default) - Human-readable timeline
  --format=json           - Machine-readable JSON
  --format=yaml           - YAML format`,
		Example: `  ntm ensemble provenance abc123def456
  ntm ensemble provenance --all
  ntm ensemble provenance --stats
  ntm ensemble provenance --all --format=json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := opts.Session
			res, err := resolveEnsembleStateCommandSession(session, cmd.OutOrStdout())
			if err != nil {
				return err
			}
			if res.Session == "" {
				return nil
			}
			res.ExplainIfInferred(os.Stderr)
			session = res.Session

			findingID := ""
			if len(args) > 0 {
				findingID = args[0]
			}

			return runEnsembleProvenance(cmd.OutOrStdout(), session, findingID, opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Format, "format", "f", "text", "Output format: text, json, yaml")
	cmd.Flags().StringVarP(&opts.Session, "session", "s", "", "Session name (default: current)")
	cmd.Flags().BoolVar(&opts.All, "all", false, "List all tracked findings")
	cmd.Flags().BoolVar(&opts.Stats, "stats", false, "Show provenance statistics")
	cmd.ValidArgsFunction = completeSessionArgs
	return cmd
}

func resolveEnsembleSession(session string, w io.Writer) (SessionResolution, error) {
	return ResolveSessionWithOptions(session, w, SessionResolveOptions{TreatAsJSON: IsJSONOutput()})
}

func resolveEnsembleStateCommandSession(session string, w io.Writer) (SessionResolution, error) {
	session = strings.TrimSpace(session)
	if session == "" {
		return resolveEnsembleSession("", w)
	}
	resolved, err := normalizeOfflineCapableEnsembleSessionName(session, !IsJSONOutput())
	if err != nil {
		return SessionResolution{}, err
	}
	return SessionResolution{
		Session:  resolved,
		Reason:   "explicit session",
		Inferred: false,
	}, nil
}

func normalizeOfflineCapableEnsembleSessionName(session string, allowPrefix bool) (string, error) {
	session = strings.TrimSpace(session)
	if session == "" {
		return "", fmt.Errorf("session is required")
	}
	if err := tmux.ValidateSessionName(session); err != nil {
		return "", fmt.Errorf("invalid session name: %w", err)
	}

	if sessionList, err := tmux.ListSessions(); err == nil {
		resolved, _, err := resolveExplicitSessionName(session, sessionList, allowPrefix)
		if err == nil {
			session = resolved
		} else {
			var resolveErr *sessionPkg.ResolveExplicitSessionNameError
			if errors.As(err, &resolveErr) && resolveErr.Kind == sessionPkg.ResolveExplicitSessionNameErrorAmbiguous {
				return "", err
			}
		}
	}

	if !allowPrefix {
		return session, nil
	}

	resolvedBase, err := normalizeConfiguredProjectBase(config.SessionBase(session), allowPrefix)
	if err != nil {
		return "", fmt.Errorf("session %q is ambiguous: %w", session, err)
	}
	if resolvedBase == "" || resolvedBase == config.SessionBase(session) {
		return session, nil
	}

	label := config.SessionLabel(session)
	if label == "" {
		return resolvedBase, nil
	}
	return config.FormatSessionName(resolvedBase, label), nil
}

func newEnsembleCheckpointStoreForSession(session string) (*ensemble.CheckpointStore, error) {
	projectDir, err := resolveEnsembleProjectDirForSession(session)
	if err != nil {
		return nil, err
	}
	return newEnsembleCheckpointStoreForProject(projectDir)
}

func newEnsembleCheckpointStoreForProject(projectDir string) (*ensemble.CheckpointStore, error) {
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return nil, fmt.Errorf("getting project root failed")
	}
	return ensemble.NewCheckpointStore(filepath.Join(projectDir, ".ntm"))
}

func newEnsembleCheckpointStore() (*ensemble.CheckpointStore, error) {
	projectDir := GetProjectRoot()
	return newEnsembleCheckpointStoreForProject(projectDir)
}

func ensembleCheckpointProjectCandidates() []string {
	activeCfg := cfg
	if activeCfg == nil {
		activeCfg = config.Default()
	}

	candidates := make([]string, 0, 8)
	seen := make(map[string]struct{})
	addCandidate := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		if absDir, err := filepath.Abs(config.ExpandHome(dir)); err == nil {
			dir = absDir
		}
		dir = filepath.Clean(dir)
		if _, ok := seen[dir]; ok {
			return
		}
		seen[dir] = struct{}{}
		candidates = append(candidates, dir)
	}

	addCandidate(GetProjectRoot())

	if activeCfg == nil {
		return candidates
	}

	projectsBase := strings.TrimSpace(config.ExpandHome(activeCfg.ProjectsBase))
	if projectsBase == "" {
		return candidates
	}
	entries, err := os.ReadDir(projectsBase)
	if err != nil {
		return candidates
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		addCandidate(filepath.Join(projectsBase, entry.Name()))
	}

	return candidates
}

func resolveEnsembleCheckpointStoreForRunID(runID string) (*ensemble.CheckpointStore, string, error) {
	normalizedRunID, err := ensemble.NormalizeCheckpointRunID(runID)
	if err != nil {
		return nil, "", err
	}
	runID = normalizedRunID

	matches := make([]string, 0, 2)
	for _, projectDir := range ensembleCheckpointProjectCandidates() {
		metaPath := filepath.Join(projectDir, ".ntm", "ensemble-checkpoints", runID, "_meta.json")
		if info, err := os.Stat(metaPath); err == nil && !info.IsDir() {
			matches = append(matches, projectDir)
		}
	}

	switch len(matches) {
	case 0:
		store, err := newEnsembleCheckpointStore()
		return store, "", err
	case 1:
		store, err := newEnsembleCheckpointStoreForProject(matches[0])
		return store, matches[0], err
	default:
		sort.Strings(matches)
		return nil, "", fmt.Errorf("checkpoint run %q is ambiguous across projects: %s", runID, strings.Join(matches, ", "))
	}
}

func runEnsembleProvenance(w io.Writer, session, findingID string, opts provenanceOptions) error {
	format := strings.ToLower(strings.TrimSpace(opts.Format))
	if format == "" {
		format = "text"
	}
	if jsonOutput {
		format = "json"
	}

	state, sessionLive, err := loadEnsembleStateWithRuntimePresence(session)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if !sessionLive {
				return fmt.Errorf("session '%s' not found", session)
			}
			return fmt.Errorf("no ensemble running in session '%s'", session)
		}
		return fmt.Errorf("load session: %w", err)
	}

	// Create provenance tracker and populate from session
	modeIDs := make([]string, 0, len(state.Assignments))
	for _, a := range state.Assignments {
		modeIDs = append(modeIDs, a.ModeID)
	}
	tracker := ensemble.NewProvenanceTracker(state.Question, modeIDs)

	outputs, err := loadEnsembleModeOutputs(state, sessionLive)
	if err != nil {
		slog.Default().Warn("failed to load outputs for provenance", "error", err)
	}

	if len(outputs) > 0 {
		synth, synthErr := ensemble.NewSynthesizer(ensemble.DefaultSynthesisConfig())
		if synthErr != nil {
			slog.Default().Warn("failed to initialize synthesizer for provenance", "error", synthErr)
		} else if _, synthErr := synth.Synthesize(&ensemble.SynthesisInput{
			Outputs:          outputs,
			OriginalQuestion: state.Question,
			Config:           synth.Config,
			Provenance:       tracker,
		}); synthErr != nil {
			slog.Default().Warn("failed to synthesize for provenance", "error", synthErr)
		}
	}

	slog.Default().Info("provenance tracker populated",
		"session", session,
		"total", tracker.Count(),
		"active", tracker.ActiveCount(),
	)

	// Handle stats mode
	if opts.Stats {
		stats := tracker.Stats()
		return renderProvenanceOutput(w, provenanceOutput{
			GeneratedAt: output.Timestamp(),
			Stats:       &stats,
		}, format)
	}

	// Handle all mode
	if opts.All {
		chains := tracker.ListChains()
		return renderProvenanceOutput(w, provenanceOutput{
			GeneratedAt: output.Timestamp(),
			Chains:      chains,
		}, format)
	}

	// Handle single finding lookup
	if findingID == "" {
		return fmt.Errorf("finding-id required (or use --all or --stats)")
	}

	chain, found := tracker.GetChain(findingID)
	if !found {
		// Try partial match
		chains := tracker.ListChains()
		for _, c := range chains {
			if strings.HasPrefix(c.FindingID, findingID) {
				chain = c
				found = true
				break
			}
		}
	}

	if !found {
		return renderProvenanceOutput(w, provenanceOutput{
			GeneratedAt: output.Timestamp(),
			FindingID:   findingID,
			Error:       fmt.Sprintf("finding '%s' not found", findingID),
		}, format)
	}

	return renderProvenanceOutput(w, provenanceOutput{
		GeneratedAt: output.Timestamp(),
		FindingID:   findingID,
		Chain:       chain,
	}, format)
}

func renderProvenanceOutput(w io.Writer, payload provenanceOutput, format string) error {
	switch format {
	case "json":
		return output.WriteJSON(w, payload, true)
	case "yaml", "yml":
		data, err := yaml.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal yaml: %w", err)
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
		if len(data) == 0 || data[len(data)-1] != '\n' {
			_, err = w.Write([]byte("\n"))
			return err
		}
		return nil
	case "text", "table":
		if payload.Error != "" {
			fmt.Fprintf(w, "Error: %s\n", payload.Error)
			return nil
		}

		if payload.Stats != nil {
			fmt.Fprintf(w, "Provenance Statistics\n")
			fmt.Fprintf(w, "=====================\n\n")
			fmt.Fprintf(w, "Total Findings:   %d\n", payload.Stats.TotalFindings)
			fmt.Fprintf(w, "Active Findings:  %d\n", payload.Stats.ActiveFindings)
			fmt.Fprintf(w, "Merged Findings:  %d\n", payload.Stats.MergedFindings)
			fmt.Fprintf(w, "Filtered Count:   %d\n", payload.Stats.FilteredCount)
			fmt.Fprintf(w, "Cited in Synthesis: %d\n\n", payload.Stats.CitedCount)

			if len(payload.Stats.ModeBreakdown) > 0 {
				fmt.Fprintf(w, "By Mode:\n")
				for mode, count := range payload.Stats.ModeBreakdown {
					fmt.Fprintf(w, "  %-20s %d\n", mode, count)
				}
			}
			return nil
		}

		if len(payload.Chains) > 0 {
			fmt.Fprintf(w, "Tracked Findings (%d)\n", len(payload.Chains))
			fmt.Fprintf(w, "====================\n\n")

			table := output.NewTable(w, "ID", "MODE", "IMPACT", "CONF", "TEXT")
			for _, chain := range payload.Chains {
				text := chain.CurrentText
				if len(text) > 60 {
					text = text[:57] + "..."
				}
				table.AddRow(
					chain.FindingID,
					chain.SourceMode,
					string(chain.Impact),
					chain.Confidence.String(),
					text,
				)
			}
			table.Render()
			return nil
		}

		if payload.Chain != nil {
			fmt.Fprint(w, ensemble.FormatProvenance(payload.Chain))
			return nil
		}

		fmt.Fprintf(w, "No provenance data available\n")
		return nil
	default:
		return fmt.Errorf("invalid format %q (expected text, json, yaml)", format)
	}
}

// --- Checkpoint Recovery Commands ---

type checkpointListOutput struct {
	GeneratedAt time.Time                     `json:"generated_at" yaml:"generated_at"`
	Checkpoints []ensemble.CheckpointMetadata `json:"checkpoints" yaml:"checkpoints"`
	Count       int                           `json:"count" yaml:"count"`
}

type checkpointResumeOutput struct {
	GeneratedAt time.Time `json:"generated_at" yaml:"generated_at"`
	RunID       string    `json:"run_id" yaml:"run_id"`
	Session     string    `json:"session" yaml:"session"`
	Success     bool      `json:"success" yaml:"success"`
	Message     string    `json:"message,omitempty" yaml:"message,omitempty"`
	Resumed     int       `json:"resumed,omitempty" yaml:"resumed,omitempty"`
	Skipped     int       `json:"skipped,omitempty" yaml:"skipped,omitempty"`
	Error       string    `json:"error,omitempty" yaml:"error,omitempty"`
}

type checkpointCleanOutput struct {
	GeneratedAt time.Time `json:"generated_at" yaml:"generated_at"`
	Removed     int       `json:"removed" yaml:"removed"`
	Message     string    `json:"message" yaml:"message"`
}

func newEnsembleResumeCmd() *cobra.Command {
	var (
		format   string
		quiet    bool
		skipDone bool
	)

	cmd := &cobra.Command{
		Use:   "resume <run-id>",
		Short: "Resume an interrupted ensemble run from checkpoint",
		Long: `Resume an ensemble run that was interrupted or failed.

The resume command loads the checkpoint state and:
  1. Identifies which modes completed successfully
  2. Re-runs any pending or errored modes
  3. Continues with synthesis when all modes complete

Use 'ntm ensemble list-checkpoints' to see available runs.`,
		Example: `  ntm ensemble resume my-ensemble-run
  ntm ensemble resume my-ensemble-run --skip-done
  ntm ensemble resume my-ensemble-run --format json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID := args[0]
			return runEnsembleResume(cmd.OutOrStdout(), runID, format, quiet, skipDone)
		},
	}

	cmd.Flags().StringVarP(&format, "format", "f", "text", "Output format: text, json, yaml")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Minimal output")
	cmd.Flags().BoolVar(&skipDone, "skip-done", true, "Skip already completed modes (default: true)")

	return cmd
}

func runEnsembleResume(w io.Writer, runID, format string, quiet, skipDone bool) error {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = "text"
	}
	if jsonOutput {
		format = "json"
	}

	store, _, err := resolveEnsembleCheckpointStoreForRunID(runID)
	if err != nil {
		result := checkpointResumeOutput{
			GeneratedAt: output.Timestamp(),
			RunID:       runID,
			Success:     false,
			Error:       err.Error(),
		}
		return renderCheckpointResumeOutput(w, result, format, quiet)
	}

	if !store.RunExists(runID) {
		errMsg := fmt.Sprintf("checkpoint run '%s' not found", runID)
		result := checkpointResumeOutput{
			GeneratedAt: output.Timestamp(),
			RunID:       runID,
			Success:     false,
			Error:       errMsg,
		}
		return renderCheckpointResumeOutput(w, result, format, quiet)
	}

	meta, err := store.LoadMetadata(runID)
	if err != nil {
		errPayload := checkpointResumeOutput{
			GeneratedAt: output.Timestamp(),
			RunID:       runID,
			Success:     false,
			Error:       fmt.Sprintf("load checkpoint metadata: %v", err),
		}
		return renderCheckpointResumeOutput(w, errPayload, format, quiet)
	}

	slog.Default().Info("resuming ensemble run",
		"run_id", runID,
		"session", meta.SessionName,
		"completed", len(meta.CompletedIDs),
		"pending", len(meta.PendingIDs),
		"errors", len(meta.ErrorIDs),
	)

	// Calculate modes to run
	toRun := append([]string{}, meta.PendingIDs...)
	toRun = append(toRun, meta.ErrorIDs...)
	skipped := len(meta.CompletedIDs)
	if !skipDone {
		toRun = append(toRun, meta.CompletedIDs...)
		skipped = 0
	}

	result := checkpointResumeOutput{
		GeneratedAt: output.Timestamp(),
		RunID:       runID,
		Session:     meta.SessionName,
		Success:     true,
		Resumed:     len(toRun),
		Skipped:     skipped,
		Message:     fmt.Sprintf("Resume initiated: %d modes to run, %d already complete", len(toRun), skipped),
	}

	if len(toRun) == 0 {
		result.Message = "All modes already completed - no resume needed"
	}

	return renderCheckpointResumeOutput(w, result, format, quiet)
}

func renderCheckpointResumeOutput(w io.Writer, payload checkpointResumeOutput, format string, quiet bool) error {
	switch format {
	case "json":
		return output.WriteJSON(w, payload, true)
	case "yaml":
		data, err := yaml.Marshal(payload)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	default:
		if quiet {
			if payload.Success {
				fmt.Fprintf(w, "resumed\n")
			} else {
				fmt.Fprintf(w, "error: %s\n", payload.Error)
			}
			return nil
		}

		if payload.Error != "" {
			fmt.Fprintf(w, "Resume failed: %s\n", payload.Error)
			return nil
		}

		fmt.Fprintf(w, "Ensemble Resume: %s\n", payload.RunID)
		fmt.Fprintf(w, "  Session: %s\n", payload.Session)
		fmt.Fprintf(w, "  Modes to run: %d\n", payload.Resumed)
		fmt.Fprintf(w, "  Already done: %d\n", payload.Skipped)
		fmt.Fprintf(w, "  %s\n", payload.Message)
		return nil
	}
}

func newEnsembleRerunModeCmd() *cobra.Command {
	var (
		format string
		quiet  bool
	)

	cmd := &cobra.Command{
		Use:   "rerun-mode <run-id> <mode>",
		Short: "Re-run a specific mode from a checkpoint",
		Long: `Re-run a single mode from an existing checkpoint run.

This is useful when:
  - A specific mode produced incorrect output
  - You want to try a mode with different parameters
  - A mode errored and you want to retry just that one

The mode's checkpoint will be updated with the new output.`,
		Example: `  ntm ensemble rerun-mode my-run deductive
  ntm ensemble rerun-mode my-run A1 --format json`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID := args[0]
			modeRef := args[1]
			return runEnsembleRerunMode(cmd.OutOrStdout(), runID, modeRef, format, quiet)
		},
	}

	cmd.Flags().StringVarP(&format, "format", "f", "text", "Output format: text, json, yaml")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Minimal output")

	return cmd
}

func runEnsembleRerunMode(w io.Writer, runID, modeRef, format string, quiet bool) error {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = "text"
	}
	if jsonOutput {
		format = "json"
	}

	store, projectDir, err := resolveEnsembleCheckpointStoreForRunID(runID)
	if err != nil {
		errPayload := checkpointResumeOutput{
			GeneratedAt: output.Timestamp(),
			RunID:       runID,
			Success:     false,
			Error:       err.Error(),
		}
		return renderCheckpointResumeOutput(w, errPayload, format, quiet)
	}

	if !store.RunExists(runID) {
		errPayload := checkpointResumeOutput{
			GeneratedAt: output.Timestamp(),
			RunID:       runID,
			Success:     false,
			Error:       fmt.Sprintf("checkpoint run '%s' not found", runID),
		}
		return renderCheckpointResumeOutput(w, errPayload, format, quiet)
	}

	meta, err := store.LoadMetadata(runID)
	if err != nil {
		errPayload := checkpointResumeOutput{
			GeneratedAt: output.Timestamp(),
			RunID:       runID,
			Success:     false,
			Error:       fmt.Sprintf("load checkpoint metadata: %v", err),
		}
		return renderCheckpointResumeOutput(w, errPayload, format, quiet)
	}

	modeID := strings.TrimSpace(modeRef)
	if !checkpointRunContainsMode(meta, modeID) {
		catalog, err := loadModeCatalogForProjectDir(projectDir)
		if err != nil {
			return fmt.Errorf("load mode catalog: %w", err)
		}
		resolvedModeID, _, err := resolveModeID(modeRef, catalog)
		if err != nil {
			errPayload := checkpointResumeOutput{
				GeneratedAt: output.Timestamp(),
				RunID:       runID,
				Session:     meta.SessionName,
				Success:     false,
				Error:       err.Error(),
			}
			return renderCheckpointResumeOutput(w, errPayload, format, quiet)
		}
		modeID = resolvedModeID
		if !checkpointRunContainsMode(meta, modeID) {
			errPayload := checkpointResumeOutput{
				GeneratedAt: output.Timestamp(),
				RunID:       runID,
				Session:     meta.SessionName,
				Success:     false,
				Error:       fmt.Sprintf("mode %q not found in checkpoint run '%s'", modeID, runID),
			}
			return renderCheckpointResumeOutput(w, errPayload, format, quiet)
		}
	}

	slog.Default().Info("rerunning mode",
		"run_id", runID,
		"mode", modeID,
		"mode_ref", modeRef,
		"session", meta.SessionName,
	)

	result := checkpointResumeOutput{
		GeneratedAt: output.Timestamp(),
		RunID:       runID,
		Session:     meta.SessionName,
		Success:     true,
		Resumed:     1,
		Message:     fmt.Sprintf("Re-running mode '%s' in run '%s'", modeID, runID),
	}

	return renderCheckpointResumeOutput(w, result, format, quiet)
}

func loadModeCatalogForProjectDir(projectDir string) (*ensemble.ModeCatalog, error) {
	loader := ensemble.NewModeLoader()
	if strings.TrimSpace(projectDir) != "" {
		loader.ProjectDir = projectDir
	}
	return loader.Load()
}

func checkpointRunContainsMode(meta *ensemble.CheckpointMetadata, modeID string) bool {
	if meta == nil || strings.TrimSpace(modeID) == "" {
		return false
	}
	for _, candidate := range meta.CompletedIDs {
		if candidate == modeID {
			return true
		}
	}
	for _, candidate := range meta.PendingIDs {
		if candidate == modeID {
			return true
		}
	}
	for _, candidate := range meta.ErrorIDs {
		if candidate == modeID {
			return true
		}
	}
	return false
}

func newEnsembleCleanCheckpointsCmd() *cobra.Command {
	var (
		format string
		maxAge string
		all    bool
		dryRun bool
	)

	cmd := &cobra.Command{
		Use:   "clean-checkpoints",
		Short: "Remove old or all checkpoint data",
		Long: `Remove checkpoint data to reclaim disk space.

By default, removes checkpoints older than 7 days.
Use --max-age to specify a different retention period.
Use --all to remove all checkpoints regardless of age.`,
		Example: `  ntm ensemble clean-checkpoints
  ntm ensemble clean-checkpoints --max-age 24h
  ntm ensemble clean-checkpoints --all
  ntm ensemble clean-checkpoints --dry-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnsembleCleanCheckpoints(cmd.OutOrStdout(), format, maxAge, all, dryRun)
		},
	}

	cmd.Flags().StringVarP(&format, "format", "f", "text", "Output format: text, json, yaml")
	cmd.Flags().StringVar(&maxAge, "max-age", "168h", "Remove checkpoints older than this duration (e.g., 24h, 7d)")
	cmd.Flags().BoolVar(&all, "all", false, "Remove all checkpoints regardless of age")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be removed without actually removing")

	return cmd
}

func runEnsembleCleanCheckpoints(w io.Writer, format, maxAge string, all, dryRun bool) error {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = "text"
	}
	if jsonOutput {
		format = "json"
	}

	store, err := newEnsembleCheckpointStore()
	if err != nil {
		return fmt.Errorf("open checkpoint store: %w", err)
	}

	var removed int
	var msg string

	if all {
		runs, err := store.ListRuns()
		if err != nil {
			return fmt.Errorf("list checkpoints: %w", err)
		}

		if dryRun {
			removed = len(runs)
			msg = fmt.Sprintf("Would remove %d checkpoint(s)", removed)
		} else {
			for _, run := range runs {
				if err := store.DeleteRun(run.RunID); err != nil {
					slog.Default().Warn("failed to delete checkpoint", "run_id", run.RunID, "error", err)
					continue
				}
				removed++
			}
			msg = fmt.Sprintf("Removed %d checkpoint(s)", removed)
		}
	} else {
		duration, err := time.ParseDuration(maxAge)
		if err != nil {
			return fmt.Errorf("invalid max-age duration: %w", err)
		}

		if dryRun {
			runs, err := store.ListRuns()
			if err != nil {
				return fmt.Errorf("list checkpoints: %w", err)
			}
			cutoff := time.Now().Add(-duration)
			for _, run := range runs {
				ts := run.UpdatedAt
				if ts.IsZero() {
					ts = run.CreatedAt
				}
				if ts.Before(cutoff) {
					removed++
				}
			}
			msg = fmt.Sprintf("Would remove %d checkpoint(s) older than %s", removed, maxAge)
		} else {
			removed, err = store.CleanOld(duration)
			if err != nil {
				return fmt.Errorf("clean checkpoints: %w", err)
			}
			msg = fmt.Sprintf("Removed %d checkpoint(s) older than %s", removed, maxAge)
		}
	}

	slog.Default().Info("checkpoint cleanup",
		"removed", removed,
		"all", all,
		"dry_run", dryRun,
	)

	result := checkpointCleanOutput{
		GeneratedAt: output.Timestamp(),
		Removed:     removed,
		Message:     msg,
	}

	return renderCheckpointCleanOutput(w, result, format)
}

func renderCheckpointCleanOutput(w io.Writer, payload checkpointCleanOutput, format string) error {
	switch format {
	case "json":
		return output.WriteJSON(w, payload, true)
	case "yaml":
		data, err := yaml.Marshal(payload)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	default:
		fmt.Fprintf(w, "%s\n", payload.Message)
		return nil
	}
}

type ensembleCacheStatsOutput struct {
	GeneratedAt time.Time                     `json:"generated_at" yaml:"generated_at"`
	ProjectDir  string                        `json:"project_dir" yaml:"project_dir"`
	CacheDir    string                        `json:"cache_dir" yaml:"cache_dir"`
	Stats       ensemble.ModeOutputCacheStats `json:"stats" yaml:"stats"`
}

type ensembleCacheClearOutput struct {
	GeneratedAt time.Time `json:"generated_at" yaml:"generated_at"`
	ProjectDir  string    `json:"project_dir" yaml:"project_dir"`
	CacheDir    string    `json:"cache_dir" yaml:"cache_dir"`
	Removed     int       `json:"removed" yaml:"removed"`
	Message     string    `json:"message" yaml:"message"`
}

func newEnsembleCacheCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Manage ensemble mode output cache",
	}

	cmd.AddCommand(newEnsembleCacheStatsCmd())
	cmd.AddCommand(newEnsembleCacheClearCmd())
	return cmd
}

func newEnsembleCacheStatsCmd() *cobra.Command {
	var (
		format  string
		project string
	)

	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show mode output cache statistics",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnsembleCacheStats(cmd.OutOrStdout(), format, project)
		},
	}

	cmd.Flags().StringVarP(&format, "format", "f", "text", "Output format: text, json, yaml")
	cmd.Flags().StringVar(&project, "project", "", "Project directory (default: current dir)")
	return cmd
}

func newEnsembleCacheClearCmd() *cobra.Command {
	var (
		format  string
		project string
	)

	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Clear cached mode outputs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnsembleCacheClear(cmd.OutOrStdout(), format, project)
		},
	}

	cmd.Flags().StringVarP(&format, "format", "f", "text", "Output format: text, json, yaml")
	cmd.Flags().StringVar(&project, "project", "", "Project directory (default: current dir)")
	return cmd
}

func runEnsembleCacheStats(w io.Writer, format, project string) error {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = "text"
	}
	if jsonOutput {
		format = "json"
	}

	projectDir, err := resolveEnsembleProjectDir(project)
	if err != nil {
		return err
	}

	cacheCfg := ensemble.DefaultModeOutputCacheConfig()
	cache, err := ensemble.NewModeOutputCache(projectDir, cacheCfg, slog.Default())
	if err != nil {
		return fmt.Errorf("open mode output cache: %w", err)
	}

	cacheDir := filepath.Join(projectDir, ".ntm", "ensemble-cache")
	payload := ensembleCacheStatsOutput{
		GeneratedAt: output.Timestamp(),
		ProjectDir:  projectDir,
		CacheDir:    cacheDir,
		Stats:       cache.Stats(),
	}

	switch format {
	case "json":
		return output.WriteJSON(w, payload, true)
	case "yaml":
		data, err := yaml.Marshal(payload)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	default:
		fmt.Fprintf(w, "Cache Dir: %s\n", payload.CacheDir)
		fmt.Fprintf(w, "Entries: %d\n", payload.Stats.Entries)
		fmt.Fprintf(w, "Expired: %d\n", payload.Stats.Expired)
		fmt.Fprintf(w, "Size: %d bytes\n", payload.Stats.SizeBytes)
		if !payload.Stats.Oldest.IsZero() {
			fmt.Fprintf(w, "Oldest: %s\n", payload.Stats.Oldest.Format(time.RFC3339))
		}
		if !payload.Stats.Newest.IsZero() {
			fmt.Fprintf(w, "Newest: %s\n", payload.Stats.Newest.Format(time.RFC3339))
		}
		fmt.Fprintf(w, "TTL: %s\n", payload.Stats.TTL)
		fmt.Fprintf(w, "Max Entries: %d\n", payload.Stats.MaxEntries)
		return nil
	}
}

func runEnsembleCacheClear(w io.Writer, format, project string) error {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = "text"
	}
	if jsonOutput {
		format = "json"
	}

	projectDir, err := resolveEnsembleProjectDir(project)
	if err != nil {
		return err
	}

	cacheCfg := ensemble.DefaultModeOutputCacheConfig()
	cache, err := ensemble.NewModeOutputCache(projectDir, cacheCfg, slog.Default())
	if err != nil {
		return fmt.Errorf("open mode output cache: %w", err)
	}

	cacheDir := filepath.Join(projectDir, ".ntm", "ensemble-cache")
	removed := cache.Clear()
	payload := ensembleCacheClearOutput{
		GeneratedAt: output.Timestamp(),
		ProjectDir:  projectDir,
		CacheDir:    cacheDir,
		Removed:     removed,
		Message:     fmt.Sprintf("Removed %d cached outputs", removed),
	}

	switch format {
	case "json":
		return output.WriteJSON(w, payload, true)
	case "yaml":
		data, err := yaml.Marshal(payload)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	default:
		fmt.Fprintf(w, "%s\n", payload.Message)
		return nil
	}
}
