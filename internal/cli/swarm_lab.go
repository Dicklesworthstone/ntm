package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/pressure"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func newSwarmLabCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lab",
		Short: "Run synthetic swarm scale scenarios, overload regression corpus, and host-capacity calibration",
		Long: `Execute deterministic, tmux-free synthetic swarm scale experiments, repaired
overload regression corpus checks, and side-effect-free host-capacity threshold
calibration.`,
	}

	cmd.AddCommand(
		newSwarmLabListCmd(),
		newSwarmLabRunCmd(),
		newSwarmLabCorpusCmd(),
		newSwarmLabCalibrateCmd(),
	)
	return cmd
}

func swarmLabWantsJSON(cmd *cobra.Command) bool {
	if IsJSONOutput() {
		return true
	}
	if cmd != nil {
		if v, err := cmd.Flags().GetBool("json"); err == nil && v {
			return true
		}
	}
	return false
}

func writeSwarmLabJSON(cmd *cobra.Command, out *robot.SwarmLabOutput) error {
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return err
}

func newSwarmLabListCmd() *cobra.Command {
	var (
		scenario string
		gate     string
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List synthetic swarm scale scenarios and overload corpus authoring guidance",
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := robot.GetSwarmLab(cmd.Context(), robot.SwarmLabOptions{
				Action:   "list",
				Scenario: scenario,
				Gate:     gate,
			})
			if err != nil {
				return err
			}
			if swarmLabWantsJSON(cmd) {
				return writeSwarmLabJSON(cmd, out)
			}
			if !out.Success {
				return fmt.Errorf("%s", out.Error)
			}

			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "Synthetic Swarm Lab Scenarios (%d):\n", len(out.Scenarios))
			for _, sc := range out.Scenarios {
				optInLabel := ""
				if sc.OptIn {
					optInLabel = " [opt-in]"
				}
				fmt.Fprintf(w, "  - %-20s gate=%-9s panes=%-3d commands=%-2d%s — %s\n",
					sc.ID,
					sc.Gate,
					sc.Synthetic.PaneCount,
					sc.Synthetic.CommandCount,
					optInLabel,
					sc.Description,
				)
			}
			if strings.TrimSpace(out.Guide) != "" {
				fmt.Fprintf(w, "\nOverload Corpus Guide:\n  %s\n", out.Guide)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&scenario, "scenario", "", "Filter to a specific scenario ID")
	cmd.Flags().StringVar(&gate, "gate", "", "Filter by cost gate (short, benchmark, load)")
	return cmd
}

func newSwarmLabRunCmd() *cobra.Command {
	var (
		scenario      string
		gate          string
		paneCount     int
		commandCount  int
		baselinePath  string
		artifactDir   string
		writeRunPath  string
		calibrate     bool
		profileID     string
		minConfidence float64
		includeOptIn  bool
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Execute named or custom synthetic swarm scale experiments",
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := robot.GetSwarmLab(cmd.Context(), robot.SwarmLabOptions{
				Action:        "run",
				Scenario:      scenario,
				Gate:          gate,
				PaneCount:     paneCount,
				CommandCount:  commandCount,
				BaselinePath:  baselinePath,
				ArtifactDir:   artifactDir,
				WriteRunPath:  writeRunPath,
				Calibrate:     calibrate,
				ProfileID:     profileID,
				MinConfidence: minConfidence,
				IncludeOptIn:  includeOptIn,
			})
			if err != nil {
				return err
			}
			if swarmLabWantsJSON(cmd) {
				return writeSwarmLabJSON(cmd, out)
			}
			if !out.Success {
				return fmt.Errorf("%s", out.Error)
			}

			w := cmd.OutOrStdout()
			if out.ExperimentSummary != nil {
				fmt.Fprintf(w, "Swarm Lab Experiment Summary [%s] success=%v\n",
					out.ExperimentSummary.SchemaVersion,
					out.ExperimentSummary.Success,
				)
			}
			for _, artifact := range out.Experiments {
				fmt.Fprintf(w, "  - %-20s gate=%-9s panes=%d commands=%d events=%d p95=%.2fms bp=%s verdict=%s\n",
					artifact.ScenarioID,
					artifact.Gate,
					artifact.ScenarioPaneCount(),
					artifact.ScenarioCommandCount(),
					artifact.Metrics.EventCount,
					artifact.Metrics.LatencyP95MS,
					artifact.Backpressure.Decision,
					artifact.Comparison.Result,
				)
				if artifact.ArtifactPaths.Summary != "" {
					fmt.Fprintf(w, "    summary_artifact: %s\n", artifact.ArtifactPaths.Summary)
				}
			}
			if out.WrittenRunPath != "" {
				fmt.Fprintf(w, "Raw Run Artifact: %s\n", out.WrittenRunPath)
			}
			if out.Calibration != nil {
				renderCalibrationReportText(w, out.Calibration)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&scenario, "scenario", "", "Named synthetic experiment scenario ID (short_smoke, benchmark_32_pane, load_100_pane)")
	cmd.Flags().StringVar(&gate, "gate", "", "Cost gate filter: short, benchmark, load")
	cmd.Flags().IntVar(&paneCount, "pane-count", 0, "Custom or override pane count")
	cmd.Flags().IntVar(&commandCount, "command-count", 0, "Custom or override command count per pane")
	cmd.Flags().StringVar(&baselinePath, "baseline", "", "Baseline experiment summary JSON path for comparison")
	cmd.Flags().StringVar(&artifactDir, "artifact-dir", "", "Directory to write structured experiment JSON artifacts")
	cmd.Flags().StringVar(&writeRunPath, "write-run", "", "File path to write raw SyntheticRunResult JSON artifact")
	cmd.Flags().BoolVar(&calibrate, "calibrate", false, "Calibrate host capacity thresholds from the executed synthetic runs")
	cmd.Flags().StringVar(&profileID, "profile-id", "host-capacity", "Host capacity profile identifier when --calibrate is set")
	cmd.Flags().Float64Var(&minConfidence, "min-confidence", 0.35, "Minimum confidence required to apply calibration recommendations")
	cmd.Flags().BoolVar(&includeOptIn, "include-opt-in", false, "Include opt-in load scenarios when running without --scenario")
	return cmd
}

func newSwarmLabCorpusCmd() *cobra.Command {
	var (
		corpusFile   string
		baselineDir  string
		artifactDir  string
		gate         string
		includeOptIn bool
	)
	cmd := &cobra.Command{
		Use:   "corpus",
		Short: "Execute the overload regression corpus against backpressure and robot contracts",
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := robot.GetSwarmLab(cmd.Context(), robot.SwarmLabOptions{
				Action:       "corpus",
				CorpusFile:   corpusFile,
				BaselineDir:  baselineDir,
				ArtifactDir:  artifactDir,
				Gate:         gate,
				IncludeOptIn: includeOptIn,
			})
			if err != nil {
				return err
			}
			if swarmLabWantsJSON(cmd) {
				return writeSwarmLabJSON(cmd, out)
			}
			if !out.Success {
				return fmt.Errorf("%s", out.Error)
			}
			if out.CorpusSummary == nil {
				return fmt.Errorf("corpus summary missing")
			}

			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "Overload Regression Corpus [%s] success=%v (%d scenario(s))\n",
				out.CorpusSummary.SchemaVersion,
				out.CorpusSummary.Success,
				len(out.CorpusSummary.Results),
			)
			for _, res := range out.CorpusSummary.Results {
				fmt.Fprintf(w, "  - %s (bug=%s) decision=%s verdict=%s panes=%d dropped=%d max_queue=%d\n",
					res.ScenarioID,
					res.SourceBug,
					res.Decision,
					res.ComparisonResult,
					res.MeasuredMetrics.PaneCount,
					res.MeasuredMetrics.DroppedCount,
					res.MeasuredMetrics.MaxQueueDepth,
				)
				if len(res.FailureReasons) > 0 {
					fmt.Fprintf(w, "    failures: %s\n", strings.Join(res.FailureReasons, ", "))
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&corpusFile, "file", "", "Optional path to custom OverloadRegressionCorpus JSON file")
	cmd.Flags().StringVar(&baselineDir, "baseline-dir", "", "Optional directory containing baseline experiment JSON files")
	cmd.Flags().StringVar(&artifactDir, "artifact-dir", "", "Directory to write overload regression artifacts")
	cmd.Flags().StringVar(&gate, "gate", "", "Gate filter: short, benchmark, load")
	cmd.Flags().BoolVar(&includeOptIn, "include-opt-in", false, "Include opt-in benchmark/load corpus fixtures")
	return cmd
}

func newSwarmLabCalibrateCmd() *cobra.Command {
	var (
		profileID     string
		minConfidence float64
		includeLive   bool
	)
	cmd := &cobra.Command{
		Use:   "calibrate",
		Short: "Calibrate host-capacity pressure thresholds from synthetic probes and optional live readings",
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := robot.GetSwarmLab(cmd.Context(), robot.SwarmLabOptions{
				Action:        "calibrate",
				ProfileID:     profileID,
				MinConfidence: minConfidence,
				IncludeLive:   includeLive,
			})
			if err != nil {
				return err
			}
			if swarmLabWantsJSON(cmd) {
				return writeSwarmLabJSON(cmd, out)
			}
			if !out.Success {
				return fmt.Errorf("%s", out.Error)
			}
			if out.Calibration == nil {
				return fmt.Errorf("calibration report missing")
			}
			renderCalibrationReportText(cmd.OutOrStdout(), out.Calibration)
			return nil
		},
	}
	cmd.Flags().StringVar(&profileID, "profile-id", "host-capacity", "Host capacity profile identifier")
	cmd.Flags().Float64Var(&minConfidence, "min-confidence", 0.35, "Minimum confidence threshold (0..1)")
	cmd.Flags().BoolVar(&includeLive, "include-live", false, "Merge live host pressure governor readings into calibration")
	return cmd
}

func renderCalibrationReportText(w io.Writer, report *pressure.HostCapacityCalibrationReport) {
	if report == nil {
		return
	}
	fmt.Fprintf(w, "Host Capacity Calibration [profile=%s] success=%v (%d recommendation(s))\n",
		report.ProfileID,
		report.Success,
		len(report.Recommendations),
	)
	for _, rec := range report.Recommendations {
		fmt.Fprintf(w, "  - %-16s observed=%.2f%s confidence=%.2f samples=%d apply=%v reason=%s (elevated=%.2f->%.2f high=%.2f->%.2f critical=%.2f->%.2f)\n",
			rec.Source,
			rec.ObservedCapacity,
			rec.Unit,
			rec.Confidence,
			rec.Samples,
			rec.Apply,
			rec.Reason,
			rec.Current.Elevated, rec.Recommended.Elevated,
			rec.Current.High, rec.Recommended.High,
			rec.Current.Critical, rec.Recommended.Critical,
		)
	}
	for _, warn := range report.Warnings {
		fmt.Fprintf(w, "  ! [%s] %s\n", warn.Code, warn.Message)
	}
}
