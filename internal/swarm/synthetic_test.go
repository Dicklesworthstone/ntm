package swarm

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/backpressure"
	"github.com/Dicklesworthstone/ntm/internal/pressure"
)

func TestSyntheticHarnessShortScenario(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	harness := NewSyntheticHarness(logger)

	result, err := harness.Run(context.Background(), SyntheticScenario{
		TestRunID:             "run-123",
		Name:                  "short smoke",
		SessionName:           "synthetic_short",
		PaneCount:             4,
		CommandCount:          3,
		OutputLinesPerCommand: 2,
		Patterns: []SyntheticOutputPattern{
			SyntheticPatternIdle,
			SyntheticPatternWorking,
			SyntheticPatternRateLimit,
			SyntheticPatternCompleted,
		},
		StartTime: time.Unix(1_700_000_000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if strings.Compare(result.Metrics.TestRunID, "run-123") != 0 {
		t.Fatalf("TestRunID = %q, want run-123", result.Metrics.TestRunID)
	}
	if result.Metrics.PaneCount < 4 || result.Metrics.PaneCount > 4 {
		t.Fatalf("PaneCount = %d, want 4", result.Metrics.PaneCount)
	}
	if result.Metrics.CommandCount != 3 {
		t.Fatalf("CommandCount = %d, want 3", result.Metrics.CommandCount)
	}
	if result.Metrics.EventCount < 12 || result.Metrics.EventCount > 12 {
		t.Fatalf("EventCount = %d, want 12", result.Metrics.EventCount)
	}
	if len(result.Panes) != 4 {
		t.Fatalf("len(Panes) = %d, want 4", len(result.Panes))
	}
	if len(result.Events) != 12 {
		t.Fatalf("len(Events) = %d, want 12", len(result.Events))
	}

	wantStates := []SyntheticAgentState{
		SyntheticStateIdle,
		SyntheticStateWorking,
		SyntheticStateRateLimit,
		SyntheticStateCompleted,
	}
	for i, want := range wantStates {
		if strings.Compare(string(result.Panes[i].State), string(want)) != 0 {
			t.Fatalf("pane %d state = %q, want %q", i+1, result.Panes[i].State, want)
		}
		if result.Panes[i].CommandCount != 3 {
			t.Fatalf("pane %d command count = %d, want 3", i+1, result.Panes[i].CommandCount)
		}
		if len(result.Panes[i].OutputTail) == 0 {
			t.Fatalf("pane %d output tail is empty", i+1)
		}
	}

	if result.Metrics.LatencyP50Micros <= 0 {
		t.Fatalf("LatencyP50Micros = %d, want positive", result.Metrics.LatencyP50Micros)
	}
	if result.Metrics.LatencyP95Micros < result.Metrics.LatencyP50Micros {
		t.Fatalf("LatencyP95Micros = %d before p50 %d", result.Metrics.LatencyP95Micros, result.Metrics.LatencyP50Micros)
	}
	if result.Metrics.MemoryGrowthBytes < 0 {
		t.Fatalf("MemoryGrowthBytes = %d, want non-negative", result.Metrics.MemoryGrowthBytes)
	}
	if result.Metrics.GoroutinesLeaked < 0 {
		t.Fatalf("GoroutinesLeaked = %d, want non-negative", result.Metrics.GoroutinesLeaked)
	}
	if result.Metrics.Goroutines <= 0 {
		t.Fatalf("Goroutines = %d, want positive (absolute count after run)", result.Metrics.Goroutines)
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if !json.Valid(data) {
		t.Fatal("result did not marshal to valid JSON")
	}
	// bd-0ewtl: both the absolute-count and the delta keys must appear so
	// pre-bd-75unj artifacts and consumers reading either name keep working.
	jsonText := string(data)
	if !strings.Contains(jsonText, `"goroutines":`) {
		t.Fatalf(`marshalled result missing "goroutines" key: %s`, jsonText)
	}
	if !strings.Contains(jsonText, `"goroutines_leaked":`) {
		t.Fatalf(`marshalled result missing "goroutines_leaked" key: %s`, jsonText)
	}

	logText := logs.String()
	for _, fragment := range []string{"synthetic_swarm_start", "synthetic_swarm_complete", "test_run_id=run-123", "pane_count=4", "event_count=12"} {
		if !strings.Contains(logText, fragment) {
			t.Fatalf("logs missing %q:\n%s", fragment, logText)
		}
	}
}

func TestSyntheticHarnessRejectsInvalidScenario(t *testing.T) {
	harness := NewSyntheticHarness(nil)

	tests := []struct {
		name     string
		scenario SyntheticScenario
		wantErr  string
	}{
		{
			name:     "negative panes",
			scenario: SyntheticScenario{PaneCount: -1, CommandCount: 1},
			wantErr:  "pane count must be positive",
		},
		{
			name:     "negative commands",
			scenario: SyntheticScenario{PaneCount: 1, CommandCount: -1},
			wantErr:  "command count must be positive",
		},
		{
			name:     "unknown pattern",
			scenario: SyntheticScenario{PaneCount: 1, CommandCount: 1, Patterns: []SyntheticOutputPattern{"mystery"}},
			wantErr:  "unknown synthetic output pattern",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := harness.Run(context.Background(), tt.scenario)
			if err == nil {
				t.Fatal("Run returned nil error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestSyntheticHarnessFeedsHostCapacityCalibration(t *testing.T) {
	t.Parallel()

	harness := NewSyntheticHarness(nil)
	result, err := harness.Run(context.Background(), SyntheticScenario{
		TestRunID:             "calibration-12",
		Name:                  "calibration",
		SessionName:           "synthetic_calibration",
		PaneCount:             12,
		CommandCount:          2,
		OutputLinesPerCommand: 1,
		StartTime:             time.Unix(1_700_000_050, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	evidence := pressure.EvidenceFromSyntheticRuns([]pressure.SyntheticCalibrationMetrics{
		{
			TestRunID:               result.Metrics.TestRunID,
			ScenarioName:            result.Metrics.ScenarioName,
			PaneCount:               result.Metrics.PaneCount,
			CommandCount:            result.Metrics.CommandCount,
			EventCount:              result.Metrics.EventCount,
			LatencyP95Micros:        result.Metrics.LatencyP95Micros,
			MemoryGrowthBytes:       result.Metrics.MemoryGrowthBytes,
			GoroutinesLeaked:        result.Metrics.GoroutinesLeaked,
			SyntheticDurationMicros: result.Metrics.SyntheticDurationMicros,
		},
	}, pressure.SyntheticCalibrationLimits{
		MaxLatencyP95Micros:  result.Metrics.LatencyP95Micros + 1,
		MaxMemoryGrowthBytes: result.Metrics.MemoryGrowthBytes + 1,
	})
	report := pressure.CalibrateHostCapacity(pressure.HostCapacityCalibrationInput{
		ProfileID: "synthetic-host",
		Now:       time.Unix(1_700_000_060, 0).UTC(),
		Baseline: map[pressure.Source]pressure.Thresholds{
			pressure.SourcePaneActivity: {Elevated: 4, High: 8, Critical: 16},
		},
		Evidence: evidence,
	})

	if !report.Success {
		t.Fatal("calibration report Success = false")
	}
	if len(report.Recommendations) != 1 {
		t.Fatalf("recommendations = %d, want 1", len(report.Recommendations))
	}
	rec := report.Recommendations[0]
	if !rec.Apply || rec.Source != "pane_activity" {
		t.Fatalf("recommendation = %+v, want applied pane_activity recommendation", rec)
	}
	if rec.ObservedCapacity != 12 {
		t.Fatalf("ObservedCapacity = %.3f, want 12", rec.ObservedCapacity)
	}
	if len(report.LogRows) != 1 || report.LogRows[0].TestRunID != "calibration-12" {
		t.Fatalf("log rows = %+v, want test_run_id calibration-12", report.LogRows)
	}
}

func TestSyntheticExperimentRegistryCoversCostClasses(t *testing.T) {
	scenarios := SyntheticExperimentScenarios()
	if len(scenarios) < 3 {
		t.Fatalf("registry has %d scenarios, want at least 3", len(scenarios))
	}

	gates := make(map[SyntheticExperimentGate]bool)
	for _, scenario := range scenarios {
		gates[scenario.Gate] = true
		if scenario.ID == "" {
			t.Fatalf("scenario missing ID: %+v", scenario)
		}
		if scenario.Budget.Name == "" {
			t.Fatalf("scenario %s missing budget name", scenario.ID)
		}
	}
	for _, gate := range []SyntheticExperimentGate{SyntheticExperimentGateShort, SyntheticExperimentGateBenchmark, SyntheticExperimentGateLoad} {
		if !gates[gate] {
			t.Fatalf("registry missing %s gate: %+v", gate, scenarios)
		}
	}

	load, ok := FindSyntheticExperimentScenario("load_100_pane")
	if !ok {
		t.Fatal("load_100_pane scenario not found")
	}
	if !load.OptIn {
		t.Fatal("load_100_pane should be opt-in")
	}
}

func TestSyntheticExperimentWritesVersionedArtifactsAndLogs(t *testing.T) {
	scenario, ok := FindSyntheticExperimentScenario("short_smoke")
	if !ok {
		t.Fatal("short_smoke scenario not found")
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	now := func() time.Time { return time.Unix(1_700_020_000, 0).UTC() }

	artifact, err := RunSyntheticExperiment(context.Background(), scenario, SyntheticExperimentOptions{
		Now:          now,
		Logger:       logger,
		ArtifactRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("RunSyntheticExperiment returned error: %v", err)
	}

	if strings.Compare(artifact.SchemaVersion, SyntheticExperimentSchemaVersion) != 0 {
		t.Fatalf("schema_version = %q, want %q", artifact.SchemaVersion, SyntheticExperimentSchemaVersion)
	}
	if artifact.Metrics.PaneCount < scenario.Synthetic.PaneCount || artifact.Metrics.PaneCount > scenario.Synthetic.PaneCount {
		t.Fatalf("pane_count = %d, want %d", artifact.Metrics.PaneCount, scenario.Synthetic.PaneCount)
	}
	wantEventCount := scenario.Synthetic.PaneCount * scenario.Synthetic.CommandCount
	if artifact.Metrics.EventCount < wantEventCount || artifact.Metrics.EventCount > wantEventCount {
		t.Fatalf("event_count = %d, want pane*command", artifact.Metrics.EventCount)
	}
	if artifact.Metrics.EventThroughputPerSecond <= 0 {
		t.Fatalf("event throughput = %.3f, want positive", artifact.Metrics.EventThroughputPerSecond)
	}
	if strings.Compare(artifact.Backpressure.SchemaVersion, SyntheticExperimentSchemaVersion) != 0 {
		t.Fatalf("backpressure schema = %q, want %q", artifact.Backpressure.SchemaVersion, SyntheticExperimentSchemaVersion)
	}
	for name, path := range map[string]string{
		"summary":      artifact.ArtifactPaths.Summary,
		"latency":      artifact.ArtifactPaths.Latency,
		"mem":          artifact.ArtifactPaths.Memory,
		"goroutines":   artifact.ArtifactPaths.Goroutines,
		"backpressure": artifact.ArtifactPaths.Backpressure,
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s artifact %q: %v", name, path, err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal %s artifact: %v", name, err)
		}
		gotSchema, _ := decoded["schema_version"].(string)
		if strings.Compare(gotSchema, SyntheticExperimentSchemaVersion) != 0 {
			t.Fatalf("%s schema_version = %#v, want %q", name, decoded["schema_version"], SyntheticExperimentSchemaVersion)
		}
	}

	logText := logs.String()
	for _, fragment := range []string{
		"synthetic_swarm_experiment_complete",
		"test_run_id=lab-short-smoke",
		"scenario=short_smoke",
		"pane_count=6",
		"command_count=4",
		"event_count=24",
		"budget=short",
		"result=missing_baseline",
		"artifact_path=",
	} {
		if !strings.Contains(logText, fragment) {
			t.Fatalf("logs missing %q:\n%s", fragment, logText)
		}
	}
}

func TestSyntheticExperimentComparisonCases(t *testing.T) {
	budget := SyntheticExperimentBudget{
		Name:                        "test",
		MaxLatencyP95Micros:         50_000,
		MaxMemoryGrowthBytes:        10_000,
		MaxGoroutinesLeaked:         0,
		MinEventThroughputPerSecond: 10,
		WarnRegressionRatio:         0.10,
		FailRegressionRatio:         0.25,
	}
	baseline := syntheticExperimentFixture("baseline", 10, 1000, 0, 100)

	better := syntheticExperimentFixture("better", 9, 900, 0, 120)
	if got := CompareSyntheticExperiment(better, &baseline, budget).Result; strings.Compare(string(got), string(SyntheticExperimentPass)) != 0 {
		t.Fatalf("better result = %s, want pass", got)
	}

	worse := syntheticExperimentFixture("worse", 14, 900, 0, 120)
	if got := CompareSyntheticExperiment(worse, &baseline, budget).Result; strings.Compare(string(got), string(SyntheticExperimentFail)) != 0 {
		t.Fatalf("worse result = %s, want fail", got)
	}

	if got := CompareSyntheticExperiment(better, nil, budget).Result; strings.Compare(string(got), string(SyntheticExperimentMissingBaseline)) != 0 {
		t.Fatalf("missing baseline result = %s, want missing_baseline", got)
	}

	mismatched := baseline
	mismatched.SchemaVersion = "ntm.swarm.experiment.v0"
	if got := CompareSyntheticExperiment(better, &mismatched, budget).Result; strings.Compare(string(got), string(SyntheticExperimentSchemaMismatch)) != 0 {
		t.Fatalf("schema mismatch result = %s, want schema_mismatch", got)
	}
}

func TestSyntheticExperimentSummaryIsRobotReadable(t *testing.T) {
	now := func() time.Time { return time.Unix(1_700_030_000, 0).UTC() }
	pass := syntheticExperimentFixture("pass", 8, 800, 0, 120)
	pass.ScenarioID = "b"
	pass.Gate = SyntheticExperimentGateBenchmark
	pass.Comparison = SyntheticExperimentComparison{Result: SyntheticExperimentPass}
	pass.ArtifactPaths = SyntheticExperimentPaths{Root: "/tmp/pass"}
	missing := syntheticExperimentFixture("missing", 8, 800, 0, 120)
	missing.ScenarioID = "a"
	missing.Gate = SyntheticExperimentGateShort
	missing.Comparison = SyntheticExperimentComparison{Result: SyntheticExperimentMissingBaseline}
	missing.ArtifactPaths = SyntheticExperimentPaths{Root: "/tmp/missing"}

	summary := BuildSyntheticExperimentSummary([]SyntheticExperimentArtifact{pass, missing}, now)
	if !summary.Success {
		t.Fatalf("summary success = false for missing-baseline warning: %+v", summary)
	}
	if want := now().UTC().Format(time.RFC3339Nano); strings.Compare(summary.GeneratedAt, want) != 0 {
		t.Fatalf("generated_at = %q, want %q", summary.GeneratedAt, want)
	}
	if len(summary.Results) < 2 || len(summary.Results) > 2 || strings.Compare(summary.Results[0].ScenarioID, "a") != 0 {
		t.Fatalf("results not sorted for robot readers: %+v", summary.Results)
	}
	if len(summary.ArtifactPaths) != 2 ||
		strings.Compare(summary.ArtifactPaths[0].Root, missing.ArtifactPaths.Root) != 0 ||
		strings.Compare(summary.ArtifactPaths[1].Root, pass.ArtifactPaths.Root) != 0 {
		t.Fatalf("artifact_paths not aligned with sorted results: %+v", summary.ArtifactPaths)
	}
	if len(summary.Warnings) < 1 || len(summary.Warnings) > 1 || !strings.Contains(summary.Warnings[0], "missing baseline") {
		t.Fatalf("warnings = %+v, want missing baseline warning", summary.Warnings)
	}
}

func TestParseOverloadRegressionCorpusValidatesFixtureFormat(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(BuiltInOverloadRegressionCorpus())
	if err != nil {
		t.Fatalf("marshal built-in corpus: %v", err)
	}

	corpus, err := ParseOverloadRegressionCorpus(data)
	if err != nil {
		t.Fatalf("ParseOverloadRegressionCorpus returned error: %v", err)
	}
	if strings.Compare(corpus.SchemaVersion, SyntheticOverloadRegressionSchemaVersion) != 0 {
		t.Fatalf("schema_version = %q, want %q", corpus.SchemaVersion, SyntheticOverloadRegressionSchemaVersion)
	}
	if len(corpus.Scenarios) < 1 || len(corpus.Scenarios) > 1 {
		t.Fatalf("scenarios = %d, want 1 built-in repaired incident", len(corpus.Scenarios))
	}
	fixture := corpus.Scenarios[0]
	if strings.Compare(fixture.SourceBug, "bd-8kglp.3") != 0 {
		t.Fatalf("source_bug = %q, want bd-8kglp.3", fixture.SourceBug)
	}
	if !containsBackpressureReason(fixture.ExpectedSignals.ReasonCodes, backpressure.ReasonQueueDepth) {
		t.Fatalf("expected reasons = %v, want queue_depth", fixture.ExpectedSignals.ReasonCodes)
	}
	if len(fixture.ExpectedArtifacts) < 3 {
		t.Fatalf("expected artifacts = %+v, want summary/backpressure/regression schemas", fixture.ExpectedArtifacts)
	}
}

func TestParseOverloadRegressionCorpusRejectsMissingFixtureFields(t *testing.T) {
	t.Parallel()
	raw := `{
		"schema_version":"ntm.swarm.overload_regression.v1",
		"scenarios":[{
			"scenario_id":"missing_source_bug",
			"synthetic":{"name":"fixture","pane_count":1,"command_count":1,"output_lines_per_command":1},
			"backpressure_inputs":[{"surface":"robot_command","source_loaded":true}],
			"expected_signals":{"reason_codes":["queue_depth"]},
			"expected_artifacts":[{"name":"summary","schema_version":"ntm.swarm.experiment.v1"}]
		}]
	}`

	_, err := ParseOverloadRegressionCorpus([]byte(raw))
	if err == nil {
		t.Fatal("ParseOverloadRegressionCorpus returned nil error for missing source_bug")
	}
	if !strings.Contains(err.Error(), "source_bug") {
		t.Fatalf("error = %q, want source_bug", err.Error())
	}
}

func TestRunOverloadRegressionCorpusShortGateWritesRegressionArtifact(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	root := t.TempDir()
	now := func() time.Time { return time.Unix(1_700_060_000, 0).UTC() }

	summary, err := RunOverloadRegressionCorpus(context.Background(), BuiltInOverloadRegressionCorpus(), OverloadRegressionOptions{
		Now:          now,
		Logger:       logger,
		ArtifactRoot: root,
	})
	if err != nil {
		t.Fatalf("RunOverloadRegressionCorpus returned error: %v", err)
	}
	if !summary.Success {
		t.Fatalf("summary.Success = false: %+v", summary.Results)
	}
	if strings.Compare(summary.GeneratedAt, now().UTC().Format(time.RFC3339Nano)) != 0 {
		t.Fatalf("generated_at = %q, want fixed clock", summary.GeneratedAt)
	}
	if len(summary.Results) < 1 || len(summary.Results) > 1 {
		t.Fatalf("results = %d, want 1", len(summary.Results))
	}
	result := summary.Results[0]
	if strings.Compare(string(result.ComparisonResult), string(SyntheticExperimentPass)) != 0 {
		t.Fatalf("comparison_result = %s, want pass: %+v", result.ComparisonResult, result.FailureReasons)
	}
	if strings.Compare(result.SourceBug, "bd-8kglp.3") != 0 {
		t.Fatalf("source_bug = %q, want bd-8kglp.3", result.SourceBug)
	}
	if !containsBackpressureReason(result.ExpectedReasonCodes, backpressure.ReasonDroppedOutput) {
		t.Fatalf("expected_reason_codes = %v, want dropped_output", result.ExpectedReasonCodes)
	}
	if result.MeasuredMetrics.DroppedCount < 160 {
		t.Fatalf("dropped_count = %d, want at least 160", result.MeasuredMetrics.DroppedCount)
	}
	if result.ArtifactPaths.Regression == "" {
		t.Fatal("regression artifact path is empty")
	}

	data, err := os.ReadFile(result.ArtifactPaths.Regression)
	if err != nil {
		t.Fatalf("read regression artifact: %v", err)
	}
	var decoded OverloadRegressionResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal regression artifact: %v", err)
	}
	if strings.Compare(decoded.SchemaVersion, SyntheticOverloadRegressionSchemaVersion) != 0 {
		t.Fatalf("regression schema = %q, want %q", decoded.SchemaVersion, SyntheticOverloadRegressionSchemaVersion)
	}
	if strings.Compare(decoded.SourceBug, "bd-8kglp.3") != 0 ||
		len(decoded.ExpectedReasonCodes) < 1 ||
		decoded.MeasuredMetrics.PaneCount < 1 ||
		strings.Compare(string(decoded.ComparisonResult), string(SyntheticExperimentPass)) != 0 {
		t.Fatalf("regression artifact missing required closeout fields: %+v", decoded)
	}

	logText := logs.String()
	for _, fragment := range []string{
		"overload_regression_corpus_result",
		"scenario_id=bd_8kglp_3_robot_backpressure_resource_busy",
		"source_bug=bd-8kglp.3",
		"comparison_result=pass",
		"artifact_path=",
	} {
		if !strings.Contains(logText, fragment) {
			t.Fatalf("logs missing %q:\n%s", fragment, logText)
		}
	}
}

func TestBuildOverloadRegressionResultToleranceFailure(t *testing.T) {
	t.Parallel()
	fixture := BuiltInOverloadRegressionCorpus().Scenarios[0]
	artifact := overloadRegressionArtifactFixture(fixture)
	artifact.Metrics.LatencyP95MS = 50

	result := BuildOverloadRegressionResult(fixture, artifact)
	if strings.Compare(string(result.ComparisonResult), string(SyntheticExperimentFail)) != 0 {
		t.Fatalf("comparison_result = %s, want fail", result.ComparisonResult)
	}
	if !containsFailureReason(result.FailureReasons, "tolerance.latency_p95_ms") {
		t.Fatalf("failure_reasons = %v, want latency tolerance failure", result.FailureReasons)
	}
}

func TestBuildOverloadRegressionResultMissingArtifactFailsSchemaGate(t *testing.T) {
	t.Parallel()
	fixture := BuiltInOverloadRegressionCorpus().Scenarios[0]
	artifact := overloadRegressionArtifactFixture(fixture)
	artifact.ArtifactPaths.Regression = ""

	result := BuildOverloadRegressionResult(fixture, artifact)
	if strings.Compare(string(result.ComparisonResult), string(SyntheticExperimentFail)) != 0 {
		t.Fatalf("comparison_result = %s, want fail", result.ComparisonResult)
	}
	if !containsFailureReason(result.FailureReasons, "artifact.missing.overload_regression") {
		t.Fatalf("failure_reasons = %v, want missing regression artifact", result.FailureReasons)
	}
}

func TestOverloadRegressionCorpusGuideDocumentsNewCases(t *testing.T) {
	t.Parallel()
	guide := OverloadRegressionCorpusGuide()
	for _, want := range []string{
		"scenario_id",
		"source_bug",
		"expected_signals.reason_codes",
		"expected_robot_errors",
		"opt-in",
	} {
		if !strings.Contains(guide, want) {
			t.Fatalf("guide missing %q: %s", want, guide)
		}
	}
}

func TestSyntheticHarnessLargeOptInWritesArtifact(t *testing.T) {
	if os.Getenv("NTM_SYNTHETIC_SWARM_LOAD") == "" {
		t.Skip("set NTM_SYNTHETIC_SWARM_LOAD=1 to run the 100-pane synthetic artifact test")
	}

	harness := NewSyntheticHarness(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	result, err := harness.Run(context.Background(), SyntheticScenario{
		TestRunID:             "load-100",
		Name:                  "load artifact",
		SessionName:           "synthetic_load",
		PaneCount:             100,
		CommandCount:          5,
		OutputLinesPerCommand: 1,
		StartTime:             time.Unix(1_700_000_100, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	path := filepath.Join(t.TempDir(), "synthetic_swarm_artifact.json")
	if err := result.WriteArtifact(path); err != nil {
		t.Fatalf("WriteArtifact returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	var decoded SyntheticRunResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal artifact: %v", err)
	}
	if decoded.Metrics.PaneCount < 100 || decoded.Metrics.PaneCount > 100 {
		t.Fatalf("artifact pane count = %d, want 100", decoded.Metrics.PaneCount)
	}
	if decoded.Metrics.EventCount < 500 || decoded.Metrics.EventCount > 500 {
		t.Fatalf("artifact event count = %d, want 500", decoded.Metrics.EventCount)
	}
}

func TestSyntheticExperimentLoadScenarioOptInWritesArtifact(t *testing.T) {
	if os.Getenv("NTM_SYNTHETIC_SWARM_LOAD") == "" {
		t.Skip("set NTM_SYNTHETIC_SWARM_LOAD=1 to run the 100-pane experiment lab artifact test")
	}
	scenario, ok := FindSyntheticExperimentScenario("load_100_pane")
	if !ok {
		t.Fatal("load_100_pane scenario not found")
	}
	artifact, err := RunSyntheticExperiment(context.Background(), scenario, SyntheticExperimentOptions{
		Now:          func() time.Time { return time.Unix(1_700_040_000, 0).UTC() },
		ArtifactRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("RunSyntheticExperiment returned error: %v", err)
	}
	if !artifact.OptIn || strings.Compare(string(artifact.Gate), string(SyntheticExperimentGateLoad)) != 0 {
		t.Fatalf("artifact opt-in/gate = %v/%s, want opt-in load", artifact.OptIn, artifact.Gate)
	}
	if artifact.Metrics.PaneCount < 100 || artifact.Metrics.PaneCount > 100 ||
		artifact.Metrics.EventCount < 500 || artifact.Metrics.EventCount > 500 {
		t.Fatalf("load artifact metrics = %+v, want 100 panes and 500 events", artifact.Metrics)
	}
}

func syntheticExperimentFixture(testRunID string, p95MS float64, memoryGrowth int64, goroutinesLeaked int, throughput float64) SyntheticExperimentArtifact {
	return SyntheticExperimentArtifact{
		SchemaVersion: SyntheticExperimentSchemaVersion,
		TestRunID:     testRunID,
		ScenarioID:    "fixture",
		Gate:          SyntheticExperimentGateShort,
		Metrics: SyntheticExperimentMetrics{
			PaneCount:                2,
			CommandCount:             2,
			LatencyP95MS:             p95MS,
			MemoryGrowthBytes:        memoryGrowth,
			GoroutinesLeaked:         goroutinesLeaked,
			EventCount:               4,
			EventThroughputPerSecond: throughput,
		},
	}
}

func overloadRegressionArtifactFixture(fixture OverloadRegressionFixture) SyntheticExperimentArtifact {
	return SyntheticExperimentArtifact{
		SchemaVersion: SyntheticExperimentSchemaVersion,
		TestRunID:     "fixture",
		ScenarioID:    fixture.ScenarioID,
		Gate:          SyntheticExperimentGateShort,
		Metrics: SyntheticExperimentMetrics{
			PaneCount:                2,
			CommandCount:             2,
			LatencyP95MS:             5,
			MemoryGrowthBytes:        1024,
			GoroutinesLeaked:         0,
			EventCount:               4,
			EventThroughputPerSecond: 100,
		},
		Backpressure: SyntheticBackpressureArtifact{
			SchemaVersion: SyntheticExperimentSchemaVersion,
			Decision:      backpressure.DecisionDegrade,
			ErrorCode:     "RESOURCE_BUSY",
			RetryAfterMS:  backpressure.DefaultThresholds().DegradeRetryAfterMS,
			ReasonCodes:   append([]backpressure.ReasonCode(nil), fixture.ExpectedSignals.ReasonCodes...),
			DroppedCount:  160,
			MaxQueueDepth: 640,
		},
		ArtifactPaths: SyntheticExperimentPaths{
			Summary:      "/tmp/summary.json",
			Backpressure: "/tmp/backpressure.json",
			Regression:   "/tmp/overload_regression.json",
		},
	}
}

func containsBackpressureReason(reasons []backpressure.ReasonCode, want backpressure.ReasonCode) bool {
	for _, reason := range reasons {
		if strings.Compare(string(reason), string(want)) == 0 {
			return true
		}
	}
	return false
}

func containsFailureReason(reasons []string, want string) bool {
	for _, reason := range reasons {
		if strings.Compare(reason, want) == 0 {
			return true
		}
	}
	return false
}

func TestNonNegativeMemoryGrowth(t *testing.T) {
	t.Parallel()

	before := runtime.MemStats{Alloc: 4096}
	afterLower := runtime.MemStats{Alloc: 1024}
	if got := nonNegativeMemoryGrowth(before, afterLower); got != 0 {
		t.Fatalf("nonNegativeMemoryGrowth(lower alloc) = %d, want 0", got)
	}

	afterHigher := runtime.MemStats{Alloc: 6144}
	if got := nonNegativeMemoryGrowth(before, afterHigher); got != 2048 {
		t.Fatalf("nonNegativeMemoryGrowth(higher alloc) = %d, want 2048", got)
	}
}

func TestNonNegativeIntDelta(t *testing.T) {
	t.Parallel()

	if got := nonNegativeIntDelta(10, 7); got != 0 {
		t.Fatalf("nonNegativeIntDelta(10,7) = %d, want 0", got)
	}
	if got := nonNegativeIntDelta(10, 10); got != 0 {
		t.Fatalf("nonNegativeIntDelta(10,10) = %d, want 0", got)
	}
	if got := nonNegativeIntDelta(10, 15); got != 5 {
		t.Fatalf("nonNegativeIntDelta(10,15) = %d, want 5", got)
	}
}
