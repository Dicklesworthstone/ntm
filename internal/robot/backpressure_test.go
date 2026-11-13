package robot

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/backpressure"
	"github.com/Dicklesworthstone/ntm/internal/swarm"
)

func requireEqual(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func captureRobotStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	prevFmt := GetOutputFormat()
	SetOutputFormat(FormatJSON)
	defer func() {
		SetOutputFormat(prevFmt)
		os.Stdout = origStdout
	}()

	runErr := fn()
	_ = w.Close()
	raw, readErr := io.ReadAll(r)
	_ = r.Close()
	if readErr != nil {
		t.Fatalf("read captured stdout: %v", readErr)
	}
	return string(raw), runErr
}

func TestBackpressureLiveAndProjections(t *testing.T) {
	fixedNow := func() time.Time { return time.Date(2026, 11, 13, 15, 0, 0, 0, time.UTC) }
	out, err := GetBackpressure(BackpressureOptions{
		Now: fixedNow,
	})
	if err != nil {
		t.Fatalf("GetBackpressure() unexpected error: %v", err)
	}
	if !out.Success {
		t.Fatalf("GetBackpressure() success=false error=%q code=%q", out.Error, out.ErrorCode)
	}
	if out.LoadShedding.SchemaVersion != backpressure.LoadSheddingSchemaVersion {
		t.Fatalf("load_shedding.schema_version = %q, want %q", out.LoadShedding.SchemaVersion, backpressure.LoadSheddingSchemaVersion)
	}
	if out.Heatmap.SchemaVersion != backpressure.LoadSheddingSchemaVersion {
		t.Fatalf("heatmap.schema_version = %q, want %q", out.Heatmap.SchemaVersion, backpressure.LoadSheddingSchemaVersion)
	}
	if out.Dashboard.SurfaceCount < 2 {
		t.Fatalf("dashboard.surface_count = %d, want >= 2 (tmux_capture + robot_command)", out.Dashboard.SurfaceCount)
	}
	if out.Snapshot.Surfaces == nil || out.Dashboard.ReasonCodes == nil || out.Heatmap.Buckets == nil || out.LoadShedding.ReasonCodes == nil {
		t.Fatalf("expected non-nil array projections: %+v", out)
	}
}

func TestBackpressureOverloadHeatmapAndSessionFilter(t *testing.T) {
	fixedNow := func() time.Time { return time.Date(2026, 11, 13, 15, 5, 0, 0, time.UTC) }
	out, err := GetBackpressure(BackpressureOptions{
		Session:       "alpha",
		SkipLiveProbe: true,
		Now:           fixedNow,
		CommandStats: []CommandBackpressureStats{
			{
				Command:       "robot-send",
				Session:       "alpha",
				QueueDepth:    600,
				QueueCapacity: 600,
				Latency:       6200 * time.Millisecond,
				SourceLoaded:  true,
			},
			{
				Command:       "robot-send",
				Session:       "other-session",
				QueueDepth:    999,
				QueueCapacity: 1000,
				Latency:       9000 * time.Millisecond,
				SourceLoaded:  true,
			},
		},
		ExtraInputs: []backpressure.SurfaceInput{
			{
				Surface:       backpressure.SurfaceWebSocket,
				Session:       "alpha",
				QueueDepth:    200,
				QueueCapacity: 256,
				DroppedCount:  120,
				ClientLagMS:   12000,
				SourceLoaded:  true,
			},
			{
				Surface:      backpressure.SurfaceSSE,
				Session:      "other-session",
				DroppedCount: 500,
				SourceLoaded: true,
			},
		},
	})
	if err != nil {
		t.Fatalf("GetBackpressure() error: %v", err)
	}
	if !out.Success {
		t.Fatalf("expected envelope success=true, got error=%q", out.Error)
	}
	if out.Snapshot.Decision != backpressure.DecisionDegrade {
		t.Fatalf("snapshot.decision = %q, want %q", out.Snapshot.Decision, backpressure.DecisionDegrade)
	}
	if out.Snapshot.ErrorCode != "RESOURCE_BUSY" || out.LoadShedding.ErrorCode != "RESOURCE_BUSY" {
		t.Fatalf("expected RESOURCE_BUSY in snapshot (%q) and load_shedding (%q)", out.Snapshot.ErrorCode, out.LoadShedding.ErrorCode)
	}
	if out.Dashboard.SurfaceCount != 2 {
		t.Fatalf("session filter should retain 2 alpha surfaces, got %d", out.Dashboard.SurfaceCount)
	}
	if out.Heatmap.Empty || len(out.Heatmap.Buckets) != 2 {
		t.Fatalf("expected 2 overloaded heatmap buckets, got empty=%v len=%d", out.Heatmap.Empty, len(out.Heatmap.Buckets))
	}
	if len(out.Snapshot.CoalescingRecommendations) == 0 {
		t.Fatalf("expected coalescing recommendations under overload")
	}

	rawJSON, printErr := captureRobotStdout(t, func() error {
		return PrintBackpressure(BackpressureOptions{
			Session:       "alpha",
			SkipLiveProbe: true,
			Now:           fixedNow,
			ExtraInputs: []backpressure.SurfaceInput{
				{
					Surface:      backpressure.SurfaceREST,
					Session:      "alpha",
					LatencyMS:    1500,
					SourceLoaded: true,
				},
			},
		})
	})
	if printErr != nil {
		t.Fatalf("PrintBackpressure() error: %v", printErr)
	}
	var decoded BackpressureOutput
	if err := json.Unmarshal([]byte(rawJSON), &decoded); err != nil {
		t.Fatalf("decode PrintBackpressure JSON: %v\nraw=%s", err, rawJSON)
	}
	if decoded.LoadShedding.Decision != backpressure.DecisionDefer {
		t.Fatalf("decoded load_shedding.decision = %q, want %q", decoded.LoadShedding.Decision, backpressure.DecisionDefer)
	}
}

func TestBackpressureInvalidSessionName(t *testing.T) {
	out, err := GetBackpressure(BackpressureOptions{Session: "bad:session.name"})
	if err != nil {
		t.Fatalf("GetBackpressure() error: %v", err)
	}
	if out.Success || out.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("expected INVALID_FLAG error envelope, got %+v", out)
	}
}

func TestSwarmLabListRunCorpusAndCalibrate(t *testing.T) {
	ctx := context.Background()
	fixedNow := func() time.Time { return time.Date(2026, 11, 13, 15, 10, 0, 0, time.UTC) }

	// 1. list all + specific scenario
	listOut, err := GetSwarmLab(ctx, SwarmLabOptions{Action: "list", Now: fixedNow})
	if err != nil {
		t.Fatalf("GetSwarmLab(list) error: %v", err)
	}
	if !listOut.Success || len(listOut.Scenarios) < 3 || listOut.Guide == "" {
		t.Fatalf("unexpected list output: %+v", listOut)
	}

	filteredList, err := GetSwarmLab(ctx, SwarmLabOptions{Action: "list", Scenario: "short_smoke", Now: fixedNow})
	if err != nil || !filteredList.Success || len(filteredList.Scenarios) != 1 || filteredList.Scenarios[0].ID != "short_smoke" {
		t.Fatalf("filtered list output = %+v, err=%v", filteredList, err)
	}

	// 2. run named scenario with --artifact-dir and --calibrate, then compare against saved baseline
	artifactDir := filepath.Join(t.TempDir(), "artifacts")
	runOut, err := GetSwarmLab(ctx, SwarmLabOptions{
		Action:      "run",
		Scenario:    "short_smoke",
		ArtifactDir: artifactDir,
		Calibrate:   true,
		ProfileID:   "ci-smoke-profile",
		Now:         fixedNow,
	})
	if err != nil {
		t.Fatalf("GetSwarmLab(run short_smoke) error: %v", err)
	}
	if !runOut.Success || len(runOut.Experiments) != 1 || runOut.ExperimentSummary == nil || runOut.Calibration == nil {
		t.Fatalf("unexpected run output: %+v", runOut)
	}
	summaryPath := runOut.Experiments[0].ArtifactPaths.Summary
	if summaryPath == "" {
		t.Fatalf("expected summary artifact path to be populated")
	}
	if _, statErr := os.Stat(summaryPath); statErr != nil {
		t.Fatalf("summary artifact missing at %q: %v", summaryPath, statErr)
	}
	if len(runOut.ScenarioRows) != 1 || runOut.ScenarioRows[0].PaneCount != 6 || runOut.ScenarioRows[0].CommandCount != 4 {
		t.Fatalf("unexpected scenario_rows: %+v", runOut.ScenarioRows)
	}

	// 3. custom run with --write-run and --baseline comparison
	writeRunPath := filepath.Join(t.TempDir(), "raw-run.json")
	customOut, err := GetSwarmLab(ctx, SwarmLabOptions{
		Action:       "run",
		PaneCount:    6,
		CommandCount: 4,
		WriteRunPath: writeRunPath,
		BaselinePath: summaryPath,
		Now:          fixedNow,
	})
	if err != nil {
		t.Fatalf("GetSwarmLab(custom run) error: %v", err)
	}
	if !customOut.Success || customOut.RunResult == nil || customOut.WrittenRunPath != writeRunPath {
		t.Fatalf("unexpected custom run output: %+v", customOut)
	}
	if _, statErr := os.Stat(writeRunPath); statErr != nil {
		t.Fatalf("raw run file missing at %q: %v", writeRunPath, statErr)
	}

	// 4. corpus builtin + custom --file
	corpusArtifactDir := filepath.Join(t.TempDir(), "corpus-artifacts")
	corpusOut, err := GetSwarmLab(ctx, SwarmLabOptions{
		Action:      "corpus",
		ArtifactDir: corpusArtifactDir,
		Now:         fixedNow,
	})
	if err != nil {
		t.Fatalf("GetSwarmLab(corpus) error: %v", err)
	}
	if !corpusOut.Success || corpusOut.CorpusSummary == nil || !corpusOut.CorpusSummary.Success || len(corpusOut.CorpusSummary.Results) == 0 {
		t.Fatalf("unexpected corpus output: %+v", corpusOut)
	}

	corpusFixture := swarm.BuiltInOverloadRegressionCorpus()
	corpusBytes, err := json.MarshalIndent(corpusFixture, "", "  ")
	if err != nil {
		t.Fatalf("marshal corpus fixture: %v", err)
	}
	corpusFile := filepath.Join(t.TempDir(), "custom-corpus.json")
	if err := os.WriteFile(corpusFile, corpusBytes, 0o644); err != nil {
		t.Fatalf("write corpus file: %v", err)
	}
	customCorpusOut, err := GetSwarmLab(ctx, SwarmLabOptions{
		Action:      "corpus",
		CorpusFile:  corpusFile,
		ArtifactDir: filepath.Join(t.TempDir(), "custom-corpus-artifacts"),
		Now:         fixedNow,
	})
	if err != nil || !customCorpusOut.Success || customCorpusOut.CorpusSummary == nil || !customCorpusOut.CorpusSummary.Success {
		t.Fatalf("custom corpus failed: out=%+v err=%v", customCorpusOut, err)
	}

	// 5. calibrate (synthetic + live governor) via PrintSwarmLab JSON envelope
	rawCalJSON, printErr := captureRobotStdout(t, func() error {
		return PrintSwarmLab(ctx, SwarmLabOptions{
			Action:      "calibrate",
			ProfileID:   "live-test-profile",
			IncludeLive: true,
			Now:         fixedNow,
		})
	})
	if printErr != nil {
		t.Fatalf("PrintSwarmLab(calibrate) error: %v", printErr)
	}
	var decodedCal SwarmLabOutput
	if err := json.Unmarshal([]byte(rawCalJSON), &decodedCal); err != nil {
		t.Fatalf("decode PrintSwarmLab JSON: %v\nraw=%s", err, rawCalJSON)
	}
	if !decodedCal.Success || decodedCal.Calibration == nil || decodedCal.Calibration.ProfileID != "live-test-profile" {
		t.Fatalf("unexpected decoded calibration output: %+v", decodedCal)
	}

	// 6. invalid action / scenario error contracts
	badOut, err := GetSwarmLab(ctx, SwarmLabOptions{Action: "nonexistent"})
	if err != nil || badOut.Success || badOut.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("expected INVALID_FLAG for unknown action, got %+v err=%v", badOut, err)
	}
}
