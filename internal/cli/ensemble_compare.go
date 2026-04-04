package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

var compareTmuxInstalled = tmux.IsInstalled
var compareSessionExists = tmux.SessionExists

// compareOptions holds CLI flags for ensemble compare.
type compareOptions struct {
	Format  string
	Verbose bool
}

// compareOutput is the JSON/YAML output structure.
type compareOutput struct {
	GeneratedAt string                     `json:"generated_at" yaml:"generated_at"`
	RunA        string                     `json:"run_a" yaml:"run_a"`
	RunB        string                     `json:"run_b" yaml:"run_b"`
	Summary     string                     `json:"summary" yaml:"summary"`
	Result      *ensemble.ComparisonResult `json:"result,omitempty" yaml:"result,omitempty"`
	Error       string                     `json:"error,omitempty" yaml:"error,omitempty"`
}

func newEnsembleCompareCmd() *cobra.Command {
	opts := compareOptions{
		Format: "text",
	}

	cmd := &cobra.Command{
		Use:   "compare <run1> <run2>",
		Short: "Compare two ensemble runs",
		Long: `Compare two ensemble runs side-by-side to see mode, finding, and synthesis differences.

The comparison uses stable finding IDs (hashes) for deterministic matching,
so findings from the same mode with the same text will be correctly aligned
even if order or other attributes differ.

Output sections:
  - Mode Changes: modes added, removed, or unchanged between runs
  - Finding Changes: new, missing, changed, and unchanged findings
  - Conclusion Changes: thesis and synthesis differences
  - Contribution Changes: mode contribution score deltas and rank changes

Formats:
  --format=text (default) - Human-readable report
  --format=json           - Machine-readable JSON
  --format=yaml           - YAML format`,
		Example: `  ntm ensemble compare session1 session2
  ntm ensemble compare run-20240101 run-20240102 --format=json
  ntm ensemble compare mysession-v1 mysession-v2 --verbose`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnsembleCompare(cmd.OutOrStdout(), args[0], args[1], opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Format, "format", "f", "text", "Output format: text, json, yaml")
	cmd.Flags().BoolVarP(&opts.Verbose, "verbose", "v", false, "Show detailed diff including unchanged items")
	cmd.ValidArgsFunction = completeSessionArgs
	return cmd
}

func runEnsembleCompare(w io.Writer, runAID, runBID string, opts compareOptions) error {
	format := strings.ToLower(strings.TrimSpace(opts.Format))
	if format == "" {
		format = "text"
	}
	if jsonOutput {
		format = "json"
	}

	slog.Info("comparing ensemble runs",
		"run_a", runAID,
		"run_b", runBID,
		"format", format,
	)

	// Load run A
	inputA, err := loadCompareInput(runAID)
	if err != nil {
		return writeCompareError(w, runAID, runBID, fmt.Errorf("load run A (%s): %w", runAID, err), format)
	}

	// Load run B
	inputB, err := loadCompareInput(runBID)
	if err != nil {
		return writeCompareError(w, runAID, runBID, fmt.Errorf("load run B (%s): %w", runBID, err), format)
	}

	slog.Info("loaded ensemble runs for comparison",
		"run_a", runAID,
		"run_a_modes", len(inputA.ModeIDs),
		"run_a_outputs", len(inputA.Outputs),
		"run_b", runBID,
		"run_b_modes", len(inputB.ModeIDs),
		"run_b_outputs", len(inputB.Outputs),
	)

	// Compare
	result := ensemble.Compare(*inputA, *inputB)

	slog.Info("comparison complete",
		"run_a", runAID,
		"run_b", runBID,
		"summary", result.Summary,
		"modes_added", result.ModeDiff.AddedCount,
		"modes_removed", result.ModeDiff.RemovedCount,
		"findings_new", result.FindingsDiff.NewCount,
		"findings_missing", result.FindingsDiff.MissingCount,
		"findings_changed", result.FindingsDiff.ChangedCount,
	)

	return writeCompareResult(w, result, opts, format)
}

// loadCompareInput loads an ensemble session and constructs a CompareInput.
func loadCompareInput(runID string) (*ensemble.CompareInput, error) {
	return loadCompareInputResolved(runID, false)
}

func loadCompareInputResolved(runID string, retried bool) (*ensemble.CompareInput, error) {
	runID = strings.TrimSpace(runID)
	store, _, storeErr := resolveEnsembleCheckpointStoreForRunID(runID)
	checkpointExists := storeErr == nil && store.RunExists(runID)

	liveExists := false
	if compareTmuxInstalled() {
		liveExists = compareSessionExists(runID)
	}
	state, stateErr := ensemble.LoadSession(runID)
	stateExists := stateErr == nil
	if stateErr != nil && !errors.Is(stateErr, os.ErrNotExist) {
		return nil, fmt.Errorf("load session: %w", stateErr)
	}

	if !retried && !checkpointExists && !liveExists && !stateExists {
		resolved, err := normalizeOfflineCapableEnsembleSessionName(runID, !IsJSONOutput())
		if err == nil && resolved != "" && resolved != runID {
			return loadCompareInputResolved(resolved, true)
		}
	}

	switch {
	case checkpointExists && (liveExists || stateExists):
		return nil, fmt.Errorf("identifier %q is ambiguous: both ensemble session state and a checkpoint run exist", runID)
	case checkpointExists:
		return loadCheckpointCompareInputFromStore(store, runID)
	case stateExists:
		return loadLiveCompareInput(runID, state, liveExists)
	case liveExists:
		return nil, fmt.Errorf("no ensemble running in session '%s'", runID)
	case storeErr != nil && !compareTmuxInstalled():
		return nil, fmt.Errorf("open checkpoint store: %w", storeErr)
	case !compareTmuxInstalled():
		return nil, fmt.Errorf("checkpoint run %q not found and tmux is not installed", runID)
	default:
		return nil, fmt.Errorf("no ensemble session state or checkpoint run found for %q", runID)
	}
}

func loadLiveCompareInput(runID string, state *ensemble.EnsembleSession, sessionLive bool) (*ensemble.CompareInput, error) {
	// Extract mode IDs from assignments
	modeIDs := make([]string, 0, len(state.Assignments))
	for _, a := range state.Assignments {
		modeIDs = append(modeIDs, a.ModeID)
	}
	sort.Strings(modeIDs)

	outputs, err := loadEnsembleModeOutputs(state, sessionLive)
	if err != nil {
		return nil, err
	}

	return buildCompareInput(runID, state.Question, modeIDs, outputs, state.SynthesisOutput), nil
}

func buildCompareInput(runID, question string, modeIDs []string, outputs []ensemble.ModeOutput, persistedSynthesis string) *ensemble.CompareInput {
	provenance := ensemble.NewProvenanceTracker(question, modeIDs)
	var contributions *ensemble.ContributionReport
	synthesisOutput := persistedSynthesis
	if len(outputs) > 0 {
		synth, synthErr := ensemble.NewSynthesizer(ensemble.DefaultSynthesisConfig())
		if synthErr == nil {
			input := &ensemble.SynthesisInput{
				Outputs:          outputs,
				OriginalQuestion: question,
				Config:           synth.Config,
				Provenance:       provenance,
			}
			result, synthErr := synth.Synthesize(input)
			if synthErr == nil {
				if synthesisOutput == "" {
					synthesisOutput = result.Summary
				}
				contributions = result.Contributions
			}
		}
	}

	slog.Debug("loaded compare input",
		"run_id", runID,
		"modes", len(modeIDs),
		"outputs", len(outputs),
		"has_contributions", contributions != nil,
		"has_synthesis", synthesisOutput != "",
	)

	return &ensemble.CompareInput{
		RunID:           runID,
		ModeIDs:         modeIDs,
		Outputs:         outputs,
		Provenance:      provenance,
		Contributions:   contributions,
		SynthesisOutput: synthesisOutput,
	}
}

func loadCheckpointCompareInput(runID string) (*ensemble.CompareInput, error) {
	store, _, err := resolveEnsembleCheckpointStoreForRunID(runID)
	if err != nil {
		return nil, fmt.Errorf("open checkpoint store: %w", err)
	}
	return loadCheckpointCompareInputFromStore(store, runID)
}

func loadCheckpointCompareInputFromStore(store *ensemble.CheckpointStore, runID string) (*ensemble.CompareInput, error) {
	if !store.RunExists(runID) {
		return nil, fmt.Errorf("ensemble run '%s' not found", runID)
	}

	meta, err := store.LoadMetadata(runID)
	if err != nil {
		return nil, fmt.Errorf("load checkpoint metadata: %w", err)
	}
	outs, err := store.GetCompletedOutputs(runID)
	if err != nil {
		return nil, fmt.Errorf("load checkpoint outputs: %w", err)
	}

	modeSet := make(map[string]struct{})
	for _, modeID := range meta.CompletedIDs {
		modeSet[modeID] = struct{}{}
	}
	for _, modeID := range meta.PendingIDs {
		modeSet[modeID] = struct{}{}
	}
	for _, modeID := range meta.ErrorIDs {
		modeSet[modeID] = struct{}{}
	}

	outputs := make([]ensemble.ModeOutput, 0, len(outs))
	for _, output := range outs {
		if output == nil {
			continue
		}
		modeSet[output.ModeID] = struct{}{}
		outputs = append(outputs, *output)
	}

	modeIDs := make([]string, 0, len(modeSet))
	for modeID := range modeSet {
		modeIDs = append(modeIDs, modeID)
	}
	sort.Strings(modeIDs)

	return buildCompareInput(runID, meta.Question, modeIDs, outputs, ""), nil
}

func writeCompareResult(w io.Writer, result *ensemble.ComparisonResult, opts compareOptions, format string) error {
	switch format {
	case "json":
		out := compareOutput{
			GeneratedAt: output.Timestamp().Format(time.RFC3339),
			RunA:        result.RunA,
			RunB:        result.RunB,
			Summary:     result.Summary,
			Result:      result,
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)

	case "yaml":
		out := compareOutput{
			GeneratedAt: output.Timestamp().Format(time.RFC3339),
			RunA:        result.RunA,
			RunB:        result.RunB,
			Summary:     result.Summary,
			Result:      result,
		}
		return yaml.NewEncoder(w).Encode(out)

	default: // text
		formatted := ensemble.FormatComparison(result)
		if opts.Verbose {
			formatted += formatVerboseDetails(result)
		}
		_, err := fmt.Fprintln(w, formatted)
		return err
	}
}

// formatVerboseDetails returns additional details for verbose mode.
func formatVerboseDetails(result *ensemble.ComparisonResult) string {
	var b strings.Builder

	b.WriteString("\n--- Verbose Details ---\n\n")

	// Show unchanged modes
	if result.ModeDiff.UnchangedCount > 0 {
		b.WriteString("Unchanged Modes:\n")
		for _, m := range result.ModeDiff.Unchanged {
			fmt.Fprintf(&b, "  = %s\n", m)
		}
		b.WriteString("\n")
	}

	// Show unchanged findings count and sample
	if result.FindingsDiff.UnchangedCount > 0 {
		fmt.Fprintf(&b, "Unchanged Findings: %d total\n", result.FindingsDiff.UnchangedCount)
		if len(result.FindingsDiff.Unchanged) > 0 {
			b.WriteString("  Sample unchanged:\n")
			for i, f := range result.FindingsDiff.Unchanged {
				if i >= 5 {
					fmt.Fprintf(&b, "    ... and %d more\n", len(result.FindingsDiff.Unchanged)-5)
					break
				}
				text := f.Text
				if len(text) > 50 {
					text = text[:47] + "..."
				}
				fmt.Fprintf(&b, "    = [%s] %s\n", f.ModeID, text)
			}
		}
		b.WriteString("\n")
	}

	// Show contribution score deltas
	if len(result.ContributionDiff.ScoreDeltas) > 0 {
		b.WriteString("All Contribution Score Changes:\n")
		for _, sd := range result.ContributionDiff.ScoreDeltas {
			sign := "+"
			if sd.Delta < 0 {
				sign = ""
			}
			fmt.Fprintf(&b, "  %s: %.3f → %.3f (%s%.3f)\n",
				sd.ModeID, sd.ScoreA, sd.ScoreB, sign, sd.Delta)
		}
		b.WriteString("\n")
	}

	// Show diversity metrics
	fmt.Fprintf(&b, "Overlap Rate: %.3f → %.3f\n",
		result.ContributionDiff.OverlapRateA, result.ContributionDiff.OverlapRateB)
	fmt.Fprintf(&b, "Diversity Score: %.3f → %.3f\n",
		result.ContributionDiff.DiversityScoreA, result.ContributionDiff.DiversityScoreB)

	return b.String()
}

func writeCompareError(w io.Writer, runAID, runBID string, err error, format string) error {
	switch format {
	case "json":
		out := compareOutput{
			GeneratedAt: output.Timestamp().Format(time.RFC3339),
			RunA:        runAID,
			RunB:        runBID,
			Error:       err.Error(),
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return err

	case "yaml":
		out := compareOutput{
			GeneratedAt: output.Timestamp().Format(time.RFC3339),
			RunA:        runAID,
			RunB:        runBID,
			Error:       err.Error(),
		}
		_ = yaml.NewEncoder(w).Encode(out)
		return err

	default:
		return err
	}
}
