package cli

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
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
	GeneratedAt    time.Time               `json:"generated_at" yaml:"generated_at"`
	Session        string                  `json:"session" yaml:"session"`
	Exists         bool                    `json:"exists" yaml:"exists"`
	EnsembleName   string                  `json:"ensemble_name,omitempty" yaml:"ensemble_name,omitempty"`
	Question       string                  `json:"question,omitempty" yaml:"question,omitempty"`
	StartedAt      time.Time               `json:"started_at,omitempty" yaml:"started_at,omitempty"`
	Status         string                  `json:"status,omitempty" yaml:"status,omitempty"`
	SynthesisReady bool                    `json:"synthesis_ready,omitempty" yaml:"synthesis_ready,omitempty"`
	Synthesis      string                  `json:"synthesis,omitempty" yaml:"synthesis,omitempty"`
	Budget         ensembleBudgetSummary   `json:"budget,omitempty" yaml:"budget,omitempty"`
	StatusCounts   ensembleStatusCounts    `json:"status_counts,omitempty" yaml:"status_counts,omitempty"`
	Assignments    []ensembleAssignmentRow `json:"assignments,omitempty" yaml:"assignments,omitempty"`
}

func newEnsembleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ensemble",
		Short: "Manage reasoning ensembles",
	}

	cmd.AddCommand(newEnsembleStatusCmd())
	return cmd
}

func newEnsembleStatusCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "status [session]",
		Short: "Show status for an ensemble session",
		Long: `Show the current ensemble session state, assignments, and synthesis readiness.

Formats:
  --format=table (default)
  --format=json
  --format=yaml`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := ""
			if len(args) > 0 {
				session = args[0]
			} else {
				session = tmux.GetCurrentSession()
			}
			if session == "" {
				return fmt.Errorf("session required (not in tmux)")
			}

			return runEnsembleStatus(cmd.OutOrStdout(), session, format)
		},
	}

	cmd.Flags().StringVarP(&format, "format", "f", "table", "Output format: table, json, yaml")
	cmd.ValidArgsFunction = completeSessionArgs
	return cmd
}

func runEnsembleStatus(w io.Writer, session, format string) error {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = "table"
	}
	if jsonOutput {
		format = "json"
	}

	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}
	if !tmux.SessionExists(session) {
		return fmt.Errorf("session '%s' not found", session)
	}

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

	state, err := ensemble.LoadSession(session)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return renderEnsembleStatus(w, ensembleStatusOutput{
				GeneratedAt: output.Timestamp(),
				Session:     session,
				Exists:      false,
			}, format)
		}
		return err
	}

	catalog, _ := ensemble.GlobalCatalog()
	preset, budget := resolveEnsembleBudget(state)
	assignments, counts := buildEnsembleAssignments(state, catalog, budget.MaxTokensPerMode)

	totalEstimate := budget.MaxTokensPerMode * len(assignments)
	synthesisReady := counts.Pending == 0 && counts.Working == 0 && len(assignments) > 0

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

	return renderEnsembleStatus(w, outputData, format)
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
		return nil
	default:
		return fmt.Errorf("invalid format %q (expected table, json, yaml)", format)
	}
}
