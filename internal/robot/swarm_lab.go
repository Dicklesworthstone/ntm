package robot

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/backpressure"
	"github.com/Dicklesworthstone/ntm/internal/pressure"
	"github.com/Dicklesworthstone/ntm/internal/swarm"
)

// SwarmLabOptions configures synthetic swarm lab, regression corpus, and
// host-capacity calibration execution across CLI and --robot-swarm-lab surfaces.
type SwarmLabOptions struct {
	Action        string
	Scenario      string
	Gate          string
	PaneCount     int
	CommandCount  int
	BaselinePath  string
	BaselineDir   string
	ArtifactDir   string
	WriteRunPath  string
	CorpusFile    string
	Calibrate     bool
	ProfileID     string
	MinConfidence float64
	IncludeLive   bool
	IncludeOptIn  bool
	Now           func() time.Time
	Logger        *slog.Logger
}

// SwarmLabScenarioRow exposes per-artifact pane and command scale dimensions.
type SwarmLabScenarioRow struct {
	ScenarioID   string                          `json:"scenario_id"`
	Gate         swarm.SyntheticExperimentGate   `json:"gate"`
	PaneCount    int                             `json:"pane_count"`
	CommandCount int                             `json:"command_count"`
	EventCount   int                             `json:"event_count"`
	Result       swarm.SyntheticExperimentResult `json:"result"`
}

// SwarmLabOutput is the robot-readable envelope for --robot-swarm-lab and ntm swarm lab.
type SwarmLabOutput struct {
	RobotResponse
	Action            string                                  `json:"action"`
	Guide             string                                  `json:"guide,omitempty"`
	Scenarios         []swarm.SyntheticExperimentScenario     `json:"scenarios"`
	ScenarioRows      []SwarmLabScenarioRow                   `json:"scenario_rows"`
	Experiments       []swarm.SyntheticExperimentArtifact     `json:"experiments"`
	ExperimentSummary *swarm.SyntheticExperimentSummary       `json:"experiment_summary,omitempty"`
	RunResult         *swarm.SyntheticRunResult               `json:"run_result,omitempty"`
	WrittenRunPath    string                                  `json:"written_run_path,omitempty"`
	CorpusSummary     *swarm.OverloadRegressionSummary        `json:"corpus_summary,omitempty"`
	Calibration       *pressure.HostCapacityCalibrationReport `json:"calibration,omitempty"`
}

// GetSwarmLab executes the requested synthetic swarm lab action.
func GetSwarmLab(ctx context.Context, opts SwarmLabOptions) (*SwarmLabOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	action := strings.ToLower(strings.TrimSpace(opts.Action))
	if action == "" {
		action = "list"
	}

	out := &SwarmLabOutput{
		RobotResponse: NewRobotResponse(true),
		Action:        action,
	}

	switch action {
	case "list":
		if err := populateSwarmLabList(out, opts); err != nil {
			out.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag, "Use --lab-scenario with a registered scenario ID or omit it to list all scenarios.")
		}
	case "run":
		if err := populateSwarmLabRun(ctx, out, opts, nowFn, logger); err != nil {
			out.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag, "Verify --lab-scenario, --lab-gate (short|benchmark|load), --baseline, and output paths.")
		}
	case "corpus":
		if err := populateSwarmLabCorpus(ctx, out, opts, nowFn, logger); err != nil {
			out.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag, "Verify --file points to a valid OverloadRegressionCorpus JSON fixture.")
		}
	case "calibrate":
		if err := populateSwarmLabCalibrate(ctx, out, opts, nowFn, logger); err != nil {
			out.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Inspect synthetic harness and host pressure readings.")
		}
	default:
		out.RobotResponse = NewErrorResponse(
			fmt.Errorf("unknown lab action %q", action),
			ErrCodeInvalidFlag,
			"Valid --lab-action values: list, run, corpus, calibrate.",
		)
	}

	EnsureArraysNeverNull(out)
	return out, nil
}

// PrintSwarmLab handles the --robot-swarm-lab flag.
func PrintSwarmLab(ctx context.Context, opts SwarmLabOptions) error {
	output, err := GetSwarmLab(ctx, opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot swarm-lab failed")
}

func parseSwarmLabGate(raw string) (swarm.SyntheticExperimentGate, error) {
	gate := swarm.SyntheticExperimentGate(strings.ToLower(strings.TrimSpace(raw)))
	switch gate {
	case "", swarm.SyntheticExperimentGateShort, swarm.SyntheticExperimentGateBenchmark, swarm.SyntheticExperimentGateLoad:
		return gate, nil
	default:
		return "", fmt.Errorf("invalid gate %q: want short, benchmark, or load", raw)
	}
}

func populateSwarmLabList(out *SwarmLabOutput, opts SwarmLabOptions) error {
	gate, err := parseSwarmLabGate(opts.Gate)
	if err != nil {
		return err
	}
	out.Guide = swarm.OverloadRegressionCorpusGuide()
	scenarioID := strings.TrimSpace(opts.Scenario)
	if scenarioID != "" {
		scenario, ok := swarm.FindSyntheticExperimentScenario(scenarioID)
		if !ok {
			ids := make([]string, 0, 3)
			for _, candidate := range swarm.SyntheticExperimentScenarios() {
				ids = append(ids, candidate.ID)
			}
			return fmt.Errorf("unknown synthetic experiment scenario %q (available: %s)", scenarioID, strings.Join(ids, ", "))
		}
		if gate != "" && scenario.Gate != gate {
			out.Scenarios = []swarm.SyntheticExperimentScenario{}
			return nil
		}
		out.Scenarios = []swarm.SyntheticExperimentScenario{scenario}
		return nil
	}

	all := swarm.SyntheticExperimentScenarios()
	if gate == "" {
		out.Scenarios = all
		return nil
	}
	filtered := make([]swarm.SyntheticExperimentScenario, 0, len(all))
	for _, s := range all {
		if s.Gate == gate {
			filtered = append(filtered, s)
		}
	}
	out.Scenarios = filtered
	return nil
}

func loadBaselineExperimentArtifact(path string) (*swarm.SyntheticExperimentArtifact, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read baseline %q: %w", path, err)
	}
	var artifact swarm.SyntheticExperimentArtifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		return nil, fmt.Errorf("decode baseline %q: %w", path, err)
	}
	return &artifact, nil
}

func populateSwarmLabRun(ctx context.Context, out *SwarmLabOutput, opts SwarmLabOptions, nowFn func() time.Time, logger *slog.Logger) error {
	if opts.PaneCount < 0 {
		return fmt.Errorf("pane-count must be >= 0, got %d", opts.PaneCount)
	}
	if opts.CommandCount < 0 {
		return fmt.Errorf("command-count must be >= 0, got %d", opts.CommandCount)
	}
	gate, err := parseSwarmLabGate(opts.Gate)
	if err != nil {
		return err
	}
	baseline, err := loadBaselineExperimentArtifact(opts.BaselinePath)
	if err != nil {
		return err
	}

	scenarioID := strings.TrimSpace(opts.Scenario)
	writeRunPath := strings.TrimSpace(opts.WriteRunPath)
	artifactDir := strings.TrimSpace(opts.ArtifactDir)
	customRun := scenarioID == "" && (opts.PaneCount > 0 || opts.CommandCount > 0 || writeRunPath != "")

	artifacts := make([]swarm.SyntheticExperimentArtifact, 0, 3)

	if customRun || (scenarioID != "" && writeRunPath != "") {
		var expScenario swarm.SyntheticExperimentScenario
		if scenarioID != "" {
			found, ok := swarm.FindSyntheticExperimentScenario(scenarioID)
			if !ok {
				return fmt.Errorf("unknown synthetic experiment scenario %q", scenarioID)
			}
			expScenario = found
		} else {
			effectiveGate := gate
			if effectiveGate == "" {
				effectiveGate = swarm.SyntheticExperimentGateShort
			}
			expScenario = swarm.SyntheticExperimentScenario{
				ID:          "custom_run",
				Gate:        effectiveGate,
				Description: "custom synthetic swarm harness run",
				Synthetic: swarm.SyntheticScenario{
					Name:         "custom synthetic run",
					PaneCount:    opts.PaneCount,
					CommandCount: opts.CommandCount,
				},
			}
		}
		if opts.PaneCount > 0 {
			expScenario.Synthetic.PaneCount = opts.PaneCount
		}
		if opts.CommandCount > 0 {
			expScenario.Synthetic.CommandCount = opts.CommandCount
		}
		if gate != "" {
			expScenario.Gate = gate
		}

		rawResult, err := swarm.NewSyntheticHarness(logger).Run(ctx, expScenario.Synthetic)
		if err != nil {
			return err
		}
		if writeRunPath != "" {
			if dir := filepath.Dir(writeRunPath); dir != "" && dir != "." {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return fmt.Errorf("create run artifact directory: %w", err)
				}
			}
			if err := rawResult.WriteArtifact(writeRunPath); err != nil {
				return fmt.Errorf("write run artifact %q: %w", writeRunPath, err)
			}
			out.WrittenRunPath = writeRunPath
		}
		out.RunResult = rawResult

		bpSnap := backpressure.Evaluate([]backpressure.SurfaceInput{
			{
				Surface:       backpressure.SurfaceTmuxCapture,
				Session:       rawResult.Metrics.SessionName,
				QueueDepth:    rawResult.Metrics.PaneCount,
				QueueCapacity: backpressure.DefaultThresholds().QueueDepthCritical,
				LatencyMS:     rawResult.Metrics.LatencyP95Micros / 1000,
				SourceLoaded:  true,
			},
		}, backpressure.SnapshotOptions{
			Now:        nowFn,
			Thresholds: backpressure.DefaultThresholds(),
		})
		artifact := swarm.BuildSyntheticExperimentArtifact(rawResult, expScenario, bpSnap, nowFn)
		artifact.Comparison = swarm.CompareSyntheticExperiment(artifact, baseline, artifact.Budget)
		if artifactDir != "" {
			paths, err := swarm.WriteSyntheticExperimentArtifacts(artifactDir, artifact)
			if err != nil {
				return fmt.Errorf("write experiment artifacts: %w", err)
			}
			artifact.ArtifactPaths = paths
		}
		artifacts = append(artifacts, artifact)
	} else if scenarioID != "" {
		found, ok := swarm.FindSyntheticExperimentScenario(scenarioID)
		if !ok {
			return fmt.Errorf("unknown synthetic experiment scenario %q", scenarioID)
		}
		if opts.PaneCount > 0 {
			found.Synthetic.PaneCount = opts.PaneCount
		}
		if opts.CommandCount > 0 {
			found.Synthetic.CommandCount = opts.CommandCount
		}
		if gate != "" {
			found.Gate = gate
		}
		artifact, err := swarm.RunSyntheticExperiment(ctx, found, swarm.SyntheticExperimentOptions{
			Now:          nowFn,
			Logger:       logger,
			ArtifactRoot: artifactDir,
			Baseline:     baseline,
		})
		if err != nil {
			return err
		}
		artifacts = append(artifacts, artifact)
	} else {
		for _, candidate := range swarm.SyntheticExperimentScenarios() {
			if gate != "" {
				if candidate.Gate != gate {
					continue
				}
			} else if candidate.OptIn && !opts.IncludeOptIn {
				continue
			}
			artifact, err := swarm.RunSyntheticExperiment(ctx, candidate, swarm.SyntheticExperimentOptions{
				Now:          nowFn,
				Logger:       logger,
				ArtifactRoot: artifactDir,
				Baseline:     baseline,
			})
			if err != nil {
				return err
			}
			artifacts = append(artifacts, artifact)
		}
	}

	summary := swarm.BuildSyntheticExperimentSummary(artifacts, nowFn)
	out.Experiments = artifacts
	out.ExperimentSummary = &summary

	rows := make([]SwarmLabScenarioRow, 0, len(artifacts))
	calMetrics := make([]pressure.SyntheticCalibrationMetrics, 0, len(artifacts))
	for _, artifact := range artifacts {
		paneCount := artifact.ScenarioPaneCount()
		cmdCount := artifact.ScenarioCommandCount()
		rows = append(rows, SwarmLabScenarioRow{
			ScenarioID:   artifact.ScenarioID,
			Gate:         artifact.Gate,
			PaneCount:    paneCount,
			CommandCount: cmdCount,
			EventCount:   artifact.Metrics.EventCount,
			Result:       artifact.Comparison.Result,
		})
		calMetrics = append(calMetrics, pressure.SyntheticCalibrationMetrics{
			TestRunID:               artifact.TestRunID,
			ScenarioName:            artifact.ScenarioID,
			PaneCount:               paneCount,
			CommandCount:            cmdCount,
			EventCount:              artifact.Metrics.EventCount,
			LatencyP95Micros:        int64(artifact.Metrics.LatencyP95MS * 1000),
			MemoryGrowthBytes:       artifact.Metrics.MemoryGrowthBytes,
			GoroutinesLeaked:        artifact.Metrics.GoroutinesLeaked,
			SyntheticDurationMicros: artifact.Metrics.SyntheticDurationMicros,
		})
	}
	out.ScenarioRows = rows

	if opts.Calibrate {
		evidence := pressure.EvidenceFromSyntheticRuns(calMetrics, pressure.SyntheticCalibrationLimits{})
		report := pressure.CalibrateHostCapacity(pressure.HostCapacityCalibrationInput{
			ProfileID:     opts.ProfileID,
			Now:           nowFn().UTC(),
			Baseline:      pressure.DefaultThresholds(),
			Evidence:      evidence,
			MinConfidence: opts.MinConfidence,
		})
		out.Calibration = &report
	}
	return nil
}

func loadBaselineArtifactDir(dir string) (map[string]swarm.SyntheticExperimentArtifact, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read baseline-dir %q: %w", dir, err)
	}
	baselines := make(map[string]swarm.SyntheticExperimentArtifact)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		fullPath := filepath.Join(dir, entry.Name())
		artifact, err := loadBaselineExperimentArtifact(fullPath)
		if err != nil {
			return nil, err
		}
		if artifact != nil && strings.TrimSpace(artifact.ScenarioID) != "" {
			baselines[artifact.ScenarioID] = *artifact
		}
	}
	return baselines, nil
}

func populateSwarmLabCorpus(ctx context.Context, out *SwarmLabOutput, opts SwarmLabOptions, nowFn func() time.Time, logger *slog.Logger) error {
	gate, err := parseSwarmLabGate(opts.Gate)
	if err != nil {
		return err
	}
	var corpus swarm.OverloadRegressionCorpus
	if path := strings.TrimSpace(opts.CorpusFile); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read corpus file %q: %w", path, err)
		}
		parsed, err := swarm.ParseOverloadRegressionCorpus(raw)
		if err != nil {
			return fmt.Errorf("parse corpus file %q: %w", path, err)
		}
		corpus = parsed
	} else {
		corpus = swarm.BuiltInOverloadRegressionCorpus()
	}

	baselines, err := loadBaselineArtifactDir(opts.BaselineDir)
	if err != nil {
		return err
	}

	artifactRoot := strings.TrimSpace(opts.ArtifactDir)
	if artifactRoot == "" {
		artifactRoot = filepath.Join(os.TempDir(), "ntm-swarm-lab-corpus")
	}

	includeOptIn := opts.IncludeOptIn || gate == swarm.SyntheticExperimentGateBenchmark || gate == swarm.SyntheticExperimentGateLoad
	summary, err := swarm.RunOverloadRegressionCorpus(ctx, corpus, swarm.OverloadRegressionOptions{
		Now:          nowFn,
		Logger:       logger,
		ArtifactRoot: artifactRoot,
		Baselines:    baselines,
		IncludeOptIn: includeOptIn,
	})
	if err != nil {
		return err
	}
	out.Guide = summary.Guide
	out.CorpusSummary = &summary
	if !summary.Success {
		out.Hint = "One or more overload regression corpus fixtures failed; inspect corpus_summary.results[].failure_reasons."
	}
	return nil
}

func populateSwarmLabCalibrate(ctx context.Context, out *SwarmLabOutput, opts SwarmLabOptions, nowFn func() time.Time, logger *slog.Logger) error {
	harness := swarm.NewSyntheticHarness(logger)
	probeScenarios := []swarm.SyntheticScenario{
		{
			TestRunID:             "calibrate-short-8p",
			Name:                  "calibrate short 8 pane",
			SessionName:           "synthetic_calibrate_short",
			PaneCount:             8,
			CommandCount:          6,
			OutputLinesPerCommand: 1,
		},
		{
			TestRunID:             "calibrate-medium-32p",
			Name:                  "calibrate medium 32 pane",
			SessionName:           "synthetic_calibrate_medium",
			PaneCount:             32,
			CommandCount:          4,
			OutputLinesPerCommand: 1,
		},
	}

	runs := make([]pressure.SyntheticCalibrationMetrics, 0, len(probeScenarios))
	for _, sc := range probeScenarios {
		res, err := harness.Run(ctx, sc)
		if err != nil {
			return err
		}
		runs = append(runs, pressure.SyntheticCalibrationMetrics{
			TestRunID:               res.Metrics.TestRunID,
			ScenarioName:            res.Metrics.ScenarioName,
			PaneCount:               res.Metrics.PaneCount,
			CommandCount:            res.Metrics.CommandCount,
			EventCount:              res.Metrics.EventCount,
			LatencyP95Micros:        res.Metrics.LatencyP95Micros,
			MemoryGrowthBytes:       res.Metrics.MemoryGrowthBytes,
			GoroutinesLeaked:        res.Metrics.GoroutinesLeaked,
			SyntheticDurationMicros: res.Metrics.SyntheticDurationMicros,
		})
	}

	evidence := pressure.EvidenceFromSyntheticRuns(runs, pressure.SyntheticCalibrationLimits{})
	var missingSources []string

	if opts.IncludeLive {
		sysProvider := pressure.NewSystemProvider()
		sysProvider.CPUSampleInterval = 10 * time.Millisecond
		gov := pressure.New(pressure.Config{
			Mode:      pressure.ModeObserve,
			Providers: []pressure.Provider{sysProvider},
			Logger:    logger,
			Now:       nowFn,
		})
		snap := gov.Refresh(ctx)
		seen := make(map[pressure.Source]bool, len(snap.Readings))
		for _, r := range snap.Readings {
			seen[r.Source] = true
			lvl := snap.Levels[r.Source]
			stable := lvl == pressure.LevelNormal || lvl == pressure.LevelElevated
			evidence = append(evidence, pressure.CalibrationEvidence{
				Source:      r.Source,
				Value:       r.Value,
				Unit:        r.Unit,
				Stable:      stable,
				Confidence:  0.60,
				SampleCount: 16,
				TestRunID:   "live-governor",
				Reason:      "live_governor_" + lvl.String(),
			})
		}
		for _, expected := range []pressure.Source{
			pressure.SourceCPU,
			pressure.SourceMemory,
			pressure.SourceLoad,
			pressure.SourceProcCount,
		} {
			if !seen[expected] {
				missingSources = append(missingSources, string(expected))
			}
		}
		sort.Strings(missingSources)
	}

	report := pressure.CalibrateHostCapacity(pressure.HostCapacityCalibrationInput{
		ProfileID:     opts.ProfileID,
		Now:           nowFn().UTC(),
		Baseline:      pressure.DefaultThresholds(),
		Evidence:      evidence,
		MissingSource: missingSources,
		MinConfidence: opts.MinConfidence,
	})
	out.Calibration = &report
	return nil
}

func swarmLabCapabilityCommand() RobotCommandInfo {
	return RobotCommandInfo{
		Name:        "swarm-lab",
		Flag:        "--robot-swarm-lab",
		Category:    "utility",
		Description: "Run synthetic swarm scale scenarios, overload regression corpus checks, and host-capacity calibration.",
		Parameters: []RobotParameter{
			{Name: "lab-action", Flag: "--lab-action", Type: "string", Required: false, Default: "list", Description: "Lab action: list, run, corpus, calibrate"},
			{Name: "lab-scenario", Flag: "--lab-scenario", Type: "string", Required: false, Description: "Named scenario ID (short_smoke, benchmark_32_pane, load_100_pane)"},
			{Name: "lab-gate", Flag: "--lab-gate", Type: "string", Required: false, Description: "Scenario cost gate filter: short, benchmark, load"},
		},
		Examples: []string{
			"ntm --robot-swarm-lab --lab-action=list",
			"ntm --robot-swarm-lab --lab-action=run --lab-scenario=short_smoke",
			"ntm --robot-swarm-lab --lab-action=corpus",
			"ntm --robot-swarm-lab --lab-action=calibrate",
		},
	}
}
