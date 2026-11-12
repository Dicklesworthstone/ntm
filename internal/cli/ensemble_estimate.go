package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/output"
)

const (
	estimateNearBudgetThreshold = 0.85
)

type ensembleEstimateMode struct {
	ModeID        string  `json:"mode_id" yaml:"mode_id"`
	ModeCode      string  `json:"mode_code,omitempty" yaml:"mode_code,omitempty"`
	ModeName      string  `json:"mode_name,omitempty" yaml:"mode_name,omitempty"`
	Category      string  `json:"category,omitempty" yaml:"category,omitempty"`
	Tier          string  `json:"tier,omitempty" yaml:"tier,omitempty"`
	TokenEstimate int     `json:"token_estimate" yaml:"token_estimate"`
	ValueScore    float64 `json:"value_score,omitempty" yaml:"value_score,omitempty"`
	ValuePerToken float64 `json:"value_per_token,omitempty" yaml:"value_per_token,omitempty"`
}

type ensembleEstimateBudget struct {
	MaxTokensPerMode       int `json:"max_tokens_per_mode" yaml:"max_tokens_per_mode"`
	MaxTotalTokens         int `json:"max_total_tokens" yaml:"max_total_tokens"`
	SynthesisReserveTokens int `json:"synthesis_reserve_tokens,omitempty" yaml:"synthesis_reserve_tokens,omitempty"`
	ContextReserveTokens   int `json:"context_reserve_tokens,omitempty" yaml:"context_reserve_tokens,omitempty"`
	EstimatedModeTokens    int `json:"estimated_mode_tokens" yaml:"estimated_mode_tokens"`
	EstimatedTotalTokens   int `json:"estimated_total_tokens" yaml:"estimated_total_tokens"`
	ModeCount              int `json:"mode_count" yaml:"mode_count"`
	BudgetOverride         int `json:"budget_override,omitempty" yaml:"budget_override,omitempty"`
}

type ensembleEstimateSuggestion struct {
	ReplaceModeID     string `json:"replace_mode_id" yaml:"replace_mode_id"`
	ReplaceModeName   string `json:"replace_mode_name,omitempty" yaml:"replace_mode_name,omitempty"`
	ReplaceTokens     int    `json:"replace_tokens" yaml:"replace_tokens"`
	SuggestedModeID   string `json:"suggested_mode_id" yaml:"suggested_mode_id"`
	SuggestedModeName string `json:"suggested_mode_name,omitempty" yaml:"suggested_mode_name,omitempty"`
	SuggestedTokens   int    `json:"suggested_tokens" yaml:"suggested_tokens"`
	SavingsTokens     int    `json:"savings_tokens" yaml:"savings_tokens"`
	Reason            string `json:"reason,omitempty" yaml:"reason,omitempty"`
}

type ensembleEstimateOutput struct {
	GeneratedAt time.Time                    `json:"generated_at" yaml:"generated_at"`
	PresetName  string                       `json:"preset_name,omitempty" yaml:"preset_name,omitempty"`
	PresetLabel string                       `json:"preset_label,omitempty" yaml:"preset_label,omitempty"`
	Modes       []ensembleEstimateMode       `json:"modes" yaml:"modes"`
	Budget      ensembleEstimateBudget       `json:"budget" yaml:"budget"`
	Warnings    []string                     `json:"warnings,omitempty" yaml:"warnings,omitempty"`
	Suggestions []ensembleEstimateSuggestion `json:"suggestions,omitempty" yaml:"suggestions,omitempty"`
}

type ensembleEstimateOptions struct {
	Format            string
	PresetName        string
	ModesRaw          string
	BudgetOverride    int
	BudgetPerAgent    int
	BudgetSynthesis   int
	BudgetContextPack int
}

func newEnsembleEstimateCmd() *cobra.Command {
	var opts ensembleEstimateOptions

	cmd := &cobra.Command{
		Use:   "estimate [preset]",
		Short: "Estimate token usage for an ensemble before running it",
		Long: `Estimate token usage for an ensemble preset or explicit mode list.

Examples:
  ntm ensemble estimate project-diagnosis
  ntm ensemble estimate --preset idea-forge --format=json
  ntm ensemble estimate --modes=deductive,edge-case,root-cause --budget=12000`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.PresetName == "" && len(args) > 0 {
				opts.PresetName = strings.TrimSpace(args[0])
			}
			if opts.PresetName == "" && strings.TrimSpace(opts.ModesRaw) == "" {
				return fmt.Errorf("preset name or --modes is required")
			}
			if opts.PresetName != "" && strings.TrimSpace(opts.ModesRaw) != "" {
				return fmt.Errorf("use either a preset name or --modes, not both")
			}

			modes := splitCommaSeparated(opts.ModesRaw)
			return runEnsembleEstimate(cmd, cmd.OutOrStdout(), opts.PresetName, modes, opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Format, "format", "f", "table", "Output format: table, json, yaml")
	cmd.Flags().StringVar(&opts.PresetName, "preset", "", "Ensemble preset name (alternative to positional arg)")
	cmd.Flags().StringVar(&opts.ModesRaw, "modes", "", "Explicit mode IDs or codes (comma-separated)")
	cmd.Flags().IntVar(&opts.BudgetOverride, "budget", 0, "Total token budget override for warnings")
	cmd.Flags().IntVar(&opts.BudgetOverride, "budget-total", 0, "Total token budget override for warnings (alias)")
	cmd.Flags().IntVar(&opts.BudgetPerAgent, "budget-per-agent", 0, "Per-agent token budget override")
	cmd.Flags().IntVar(&opts.BudgetSynthesis, "budget-synthesis", 0, "Synthesis reserve token budget override")
	cmd.Flags().IntVar(&opts.BudgetContextPack, "budget-context-pack", 0, "Context pack reserve token budget override")

	cmd.ValidArgsFunction = completeEnsemblePresetArgs
	_ = cmd.RegisterFlagCompletionFunc("preset", completeEnsemblePresetNames)
	_ = cmd.RegisterFlagCompletionFunc("modes", completeModeIDsCommaSeparated)

	return cmd
}

func runEnsembleEstimate(cmd *cobra.Command, w io.Writer, presetName string, modes []string, opts ensembleEstimateOptions) error {
	format := strings.ToLower(strings.TrimSpace(opts.Format))
	if format == "" {
		format = "table"
	}
	if jsonOutput {
		format = "json"
	}

	catalog, err := ensemble.GlobalCatalog()
	if err != nil {
		return fmt.Errorf("load mode catalog: %w", err)
	}

	var (
		modeIDs       []string
		presetLabel   string
		presetUsed    string
		question      string
		allowAdvanced bool
		cacheCfg      ensemble.CacheConfig
		budget        = ensemble.DefaultBudgetConfig()
	)

	if presetName != "" {
		registry, err := ensemble.GlobalEnsembleRegistry()
		if err != nil {
			return fmt.Errorf("load ensemble registry: %w", err)
		}
		preset := registry.Get(presetName)
		if preset == nil {
			return fmt.Errorf("ensemble preset %q not found", presetName)
		}
		presetUsed = preset.Name
		if preset.DisplayName != "" {
			presetLabel = preset.DisplayName
		} else {
			presetLabel = preset.Name
		}
		modeIDs, err = preset.ResolveIDs(catalog)
		if err != nil {
			return fmt.Errorf("resolve preset modes: %w", err)
		}
		budget = mergeBudgetDefaults(preset.Budget, budget)
		cacheCfg = preset.Cache
		allowAdvanced = preset.AllowAdvanced
		question = preset.Description
	} else {
		var err error
		modeIDs, err = resolveModeIDs(modes, catalog)
		if err != nil {
			return err
		}
		allowAdvanced = true
	}

	if cfg != nil {
		budget = ensemble.BudgetConfigFromTOML(budget, cfg.Ensemble.Budget)
	} else {
		budget = ensemble.BudgetConfigFromTOML(budget, config.Default().Ensemble.Budget)
	}

	flagChanged := func(name string) bool {
		return cmd != nil && cmd.Flags().Lookup(name) != nil && cmd.Flags().Changed(name)
	}
	if flagChanged("budget") || flagChanged("budget-total") || opts.BudgetOverride > 0 {
		budget.MaxTotalTokens = opts.BudgetOverride
	}
	if flagChanged("budget-per-agent") || opts.BudgetPerAgent > 0 {
		budget.MaxTokensPerMode = opts.BudgetPerAgent
	}
	if flagChanged("budget-synthesis") || opts.BudgetSynthesis > 0 {
		budget.SynthesisReserveTokens = opts.BudgetSynthesis
	}
	if flagChanged("budget-context-pack") || opts.BudgetContextPack > 0 {
		budget.ContextReserveTokens = opts.BudgetContextPack
	}

	projectDir, err := os.Getwd()
	if err != nil || strings.TrimSpace(projectDir) == "" {
		projectDir = "."
	}

	input := ensemble.EstimateInput{
		ModeIDs:       modeIDs,
		Question:      question,
		ProjectDir:    projectDir,
		Budget:        budget,
		Cache:         cacheCfg,
		AllowAdvanced: allowAdvanced,
	}

	payload, err := buildEnsembleEstimate(catalog, input, ensemble.EstimateOptions{}, opts.BudgetOverride)
	if err != nil {
		return err
	}
	payload.PresetName = presetUsed
	payload.PresetLabel = presetLabel

	return renderEnsembleEstimate(w, payload, format)
}

func buildEnsembleEstimate(catalog *ensemble.ModeCatalog, input ensemble.EstimateInput, opts ensemble.EstimateOptions, budgetOverride int) (ensembleEstimateOutput, error) {
	if catalog == nil {
		return ensembleEstimateOutput{}, fmt.Errorf("mode catalog is nil")
	}
	if len(input.ModeIDs) == 0 {
		return ensembleEstimateOutput{}, fmt.Errorf("no modes to estimate")
	}
	if strings.TrimSpace(input.ProjectDir) == "" {
		input.ProjectDir = "."
	}

	estimator := ensemble.NewEstimator(catalog, slog.Default())
	estimate, err := estimator.Estimate(context.Background(), input, opts)
	if err != nil {
		return ensembleEstimateOutput{}, err
	}

	rows, modeTokens := buildEstimateRows(estimate)
	warnings := buildEstimateWarnings(estimate)
	suggestions := buildEstimateSuggestions(estimate)

	payload := ensembleEstimateOutput{
		GeneratedAt: estimate.GeneratedAt,
		Modes:       rows,
		Budget: ensembleEstimateBudget{
			MaxTokensPerMode:       estimate.Budget.MaxTokensPerMode,
			MaxTotalTokens:         estimate.Budget.MaxTotalTokens,
			SynthesisReserveTokens: estimate.Budget.SynthesisReserveTokens,
			ContextReserveTokens:   estimate.Budget.ContextReserveTokens,
			EstimatedModeTokens:    modeTokens,
			EstimatedTotalTokens:   estimate.EstimatedTotalTokens,
			ModeCount:              len(rows),
			BudgetOverride:         budgetOverride,
		},
		Warnings:    warnings,
		Suggestions: suggestions,
	}

	return payload, nil
}

func buildEstimateRows(estimate *ensemble.EnsembleEstimate) ([]ensembleEstimateMode, int) {
	if estimate == nil {
		return nil, 0
	}

	rows := make([]ensembleEstimateMode, 0, len(estimate.Modes))
	total := 0
	for _, mode := range estimate.Modes {
		rows = append(rows, ensembleEstimateMode{
			ModeID:        mode.ID,
			ModeCode:      mode.Code,
			ModeName:      mode.Name,
			Category:      mode.Category,
			Tier:          mode.Tier,
			TokenEstimate: mode.TotalTokens,
			ValueScore:    mode.ValueScore,
			ValuePerToken: mode.ValuePerToken,
		})
		total += mode.TotalTokens
	}

	return rows, total
}

func buildEstimateWarnings(estimate *ensemble.EnsembleEstimate) []string {
	if estimate == nil {
		return nil
	}

	warnings := append([]string(nil), estimate.Warnings...)
	budgetTotal := estimate.Budget.MaxTotalTokens
	if budgetTotal > 0 && !estimate.OverBudget {
		if float64(estimate.EstimatedTotalTokens) >= estimateNearBudgetThreshold*float64(budgetTotal) {
			warnings = append(warnings, fmt.Sprintf("estimated tokens (%d) are near budget (%d)", estimate.EstimatedTotalTokens, budgetTotal))
		}
	}

	return warnings
}

func buildEstimateSuggestions(estimate *ensemble.EnsembleEstimate) []ensembleEstimateSuggestion {
	if estimate == nil || !estimate.OverBudget {
		return nil
	}

	modes := make([]ensemble.ModeEstimate, len(estimate.Modes))
	copy(modes, estimate.Modes)
	sort.Slice(modes, func(i, j int) bool {
		return modes[i].TotalTokens > modes[j].TotalTokens
	})

	suggestions := make([]ensembleEstimateSuggestion, 0, 3)
	for _, mode := range modes {
		if len(mode.Alternatives) == 0 {
			continue
		}
		alt := mode.Alternatives[0]
		suggestions = append(suggestions, ensembleEstimateSuggestion{
			ReplaceModeID:     mode.ID,
			ReplaceModeName:   mode.Name,
			ReplaceTokens:     mode.TotalTokens,
			SuggestedModeID:   alt.ID,
			SuggestedModeName: alt.Name,
			SuggestedTokens:   alt.EstimatedTokens,
			SavingsTokens:     alt.Savings,
			Reason:            alt.Reason,
		})
		if len(suggestions) >= 3 {
			break
		}
	}

	return suggestions
}

func renderEnsembleEstimate(w io.Writer, payload ensembleEstimateOutput, format string) error {
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
		if payload.PresetLabel != "" {
			fmt.Fprintf(w, "Preset: %s\n", payload.PresetLabel)
		}
		if payload.PresetName != "" && payload.PresetName != payload.PresetLabel {
			fmt.Fprintf(w, "Preset ID: %s\n", payload.PresetName)
		}
		fmt.Fprintf(w, "Modes: %d\n\n", len(payload.Modes))

		table := output.NewTable(w, "MODE", "CODE", "TIER", "EST TOKENS", "VALUE/TOKEN")
		for _, row := range payload.Modes {
			table.AddRow(
				row.ModeID,
				row.ModeCode,
				row.Tier,
				fmt.Sprintf("%d", row.TokenEstimate),
				fmt.Sprintf("%.4f", row.ValuePerToken),
			)
		}
		table.Render()

		fmt.Fprintf(w, "\nEstimated total: %d tokens\n", payload.Budget.EstimatedTotalTokens)
		if payload.Budget.MaxTotalTokens > 0 {
			fmt.Fprintf(w, "Budget total:    %d tokens\n", payload.Budget.MaxTotalTokens)
		}
		if payload.Budget.SynthesisReserveTokens > 0 || payload.Budget.ContextReserveTokens > 0 {
			fmt.Fprintf(w, "Reserves:        synthesis %d, context %d\n",
				payload.Budget.SynthesisReserveTokens, payload.Budget.ContextReserveTokens)
		}

		if len(payload.Warnings) > 0 {
			fmt.Fprintln(w, "\nWarnings:")
			for _, warn := range payload.Warnings {
				fmt.Fprintf(w, "  - %s\n", warn)
			}
		}

		if len(payload.Suggestions) > 0 {
			fmt.Fprintln(w, "\nSuggestions:")
			sTable := output.NewTable(w, "REPLACE", "WITH", "SAVINGS", "REASON")
			for _, s := range payload.Suggestions {
				replace := s.ReplaceModeID
				if s.ReplaceModeName != "" {
					replace = fmt.Sprintf("%s (%s)", s.ReplaceModeID, s.ReplaceModeName)
				}
				with := s.SuggestedModeID
				if s.SuggestedModeName != "" {
					with = fmt.Sprintf("%s (%s)", s.SuggestedModeID, s.SuggestedModeName)
				}
				sTable.AddRow(
					replace,
					with,
					fmt.Sprintf("%d", s.SavingsTokens),
					s.Reason,
				)
			}
			sTable.Render()
		}

		return nil
	default:
		return fmt.Errorf("invalid format %q (expected table, json, yaml)", format)
	}
}

func resolveModeIDs(inputs []string, catalog *ensemble.ModeCatalog) ([]string, error) {
	if catalog == nil {
		return nil, fmt.Errorf("mode catalog is nil")
	}

	seen := make(map[string]bool, len(inputs))
	result := make([]string, 0, len(inputs))

	for _, raw := range inputs {
		token := strings.TrimSpace(raw)
		if token == "" {
			continue
		}
		modeID, _, err := resolveModeID(token, catalog)
		if err != nil {
			return nil, err
		}
		if seen[modeID] {
			return nil, fmt.Errorf("duplicate mode %q", modeID)
		}
		seen[modeID] = true
		result = append(result, modeID)
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("no valid modes provided")
	}

	return result, nil
}

func resolveModeID(raw string, catalog *ensemble.ModeCatalog) (string, *ensemble.ReasoningMode, error) {
	if catalog == nil {
		return "", nil, fmt.Errorf("mode catalog is nil")
	}

	if mode := catalog.GetMode(raw); mode != nil {
		return mode.ID, mode, nil
	}
	// Try as code (case-insensitive)
	if mode := catalog.GetModeByCode(strings.ToUpper(raw)); mode != nil {
		return mode.ID, mode, nil
	}
	return "", nil, fmt.Errorf("mode %q not found (id or code)", raw)
}

func splitCommaSeparated(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		token := strings.TrimSpace(part)
		if token == "" {
			continue
		}
		out = append(out, token)
	}
	return out
}
