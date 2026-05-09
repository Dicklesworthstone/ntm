package pressure

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEvidenceFromSyntheticRunsClassifiesStability(t *testing.T) {
	t.Parallel()
	runs := []SyntheticCalibrationMetrics{
		{
			TestRunID:         "stable-100",
			PaneCount:         100,
			CommandCount:      3,
			EventCount:        300,
			LatencyP95Micros:  100_000,
			MemoryGrowthBytes: 8 << 20,
			GoroutinesLeaked:  0,
			ScenarioName:      "stable",
		},
		{
			TestRunID:         "slow-160",
			PaneCount:         160,
			CommandCount:      3,
			EventCount:        480,
			LatencyP95Micros:  900_000,
			MemoryGrowthBytes: 8 << 20,
			GoroutinesLeaked:  0,
			ScenarioName:      "slow",
		},
	}

	got := EvidenceFromSyntheticRuns(runs, SyntheticCalibrationLimits{
		MaxLatencyP95Micros:  250_000,
		MaxMemoryGrowthBytes: 32 << 20,
	})
	if len(got) != 2 {
		t.Fatalf("evidence count = %d, want 2", len(got))
	}
	if !got[0].Stable {
		t.Fatalf("first run Stable = false, want true")
	}
	if got[1].Stable {
		t.Fatalf("second run Stable = true, want false")
	}
	if got[0].Source != SourcePaneActivity || got[0].Unit != "panes" {
		t.Fatalf("first evidence source/unit = %s/%q, want pane_activity/panes", got[0].Source, got[0].Unit)
	}
	if got[0].TestRunID != "stable-100" {
		t.Fatalf("first evidence TestRunID = %q, want stable-100", got[0].TestRunID)
	}
}

func TestCalibrateHostCapacityRaisesStablePaneThreshold(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 9, 7, 0, 0, 0, time.UTC)
	report := CalibrateHostCapacity(HostCapacityCalibrationInput{
		ProfileID: "lab-host",
		Now:       now,
		Baseline: map[Source]Thresholds{
			SourcePaneActivity: {Elevated: 50, High: 100, Critical: 200},
		},
		Evidence: []CalibrationEvidence{
			{Source: SourcePaneActivity, Value: 140, Unit: "panes", Stable: true, Confidence: 0.9, SampleCount: 420, Reason: "synthetic_stable_140"},
		},
	})

	if !report.Success {
		t.Fatalf("Success = false")
	}
	if report.ProfileID != "lab-host" || !report.GeneratedAt.Equal(now) {
		t.Fatalf("profile/timestamp = %q/%s, want lab-host/%s", report.ProfileID, report.GeneratedAt, now)
	}
	if len(report.Recommendations) != 1 {
		t.Fatalf("recommendations = %d, want 1", len(report.Recommendations))
	}
	rec := report.Recommendations[0]
	if rec.Source != "pane_activity" || !rec.Apply {
		t.Fatalf("rec source/apply = %s/%v, want pane_activity/true", rec.Source, rec.Apply)
	}
	if rec.Recommended.High <= rec.Current.High {
		t.Fatalf("recommended high = %.3f, want above current %.3f", rec.Recommended.High, rec.Current.High)
	}
	if rec.ObservedCapacity != 140 || rec.Unit != "panes" {
		t.Fatalf("observed capacity/unit = %.3f/%q, want 140/panes", rec.ObservedCapacity, rec.Unit)
	}
	if got := report.Profile.Thresholds[SourcePaneActivity].High; got != rec.Recommended.High {
		t.Fatalf("profile high = %.3f, want recommendation %.3f", got, rec.Recommended.High)
	}
	if len(report.LogRows) != 1 {
		t.Fatalf("log rows = %d, want 1", len(report.LogRows))
	}
	logRow := report.LogRows[0]
	if logRow.HostProfileID != "lab-host" || logRow.Source != "pane_activity" || logRow.Confidence != 0.9 {
		t.Fatalf("log row = %+v, want profile/source/confidence fields populated", logRow)
	}
}

func TestCalibrateHostCapacityLowersUnstableRatioThreshold(t *testing.T) {
	t.Parallel()
	report := CalibrateHostCapacity(HostCapacityCalibrationInput{
		ProfileID: "unstable-host",
		Baseline: map[Source]Thresholds{
			SourceCPU: {Elevated: 0.60, High: 0.80, Critical: 0.92},
		},
		Evidence: []CalibrationEvidence{
			{Source: SourceCPU, Value: 0.70, Unit: "ratio", Stable: false, Confidence: 0.8, SampleCount: 30, Reason: "real_run_latency_spike"},
		},
	})

	if len(report.Recommendations) != 1 {
		t.Fatalf("recommendations = %d, want 1", len(report.Recommendations))
	}
	rec := report.Recommendations[0]
	if !rec.Apply {
		t.Fatalf("Apply = false, want true")
	}
	if rec.Recommended.High >= rec.Current.High {
		t.Fatalf("recommended high = %.3f, want below current %.3f", rec.Recommended.High, rec.Current.High)
	}
	if rec.Recommended.Critical > 1 {
		t.Fatalf("recommended critical = %.3f, want ratio capped at <= 1", rec.Recommended.Critical)
	}
	if rec.Recommended.Elevated > rec.Recommended.High || rec.Recommended.High > rec.Recommended.Critical {
		t.Fatalf("thresholds not monotonic: %+v", rec.Recommended)
	}
}

func TestCalibrateHostCapacityLowConfidenceDoesNotApply(t *testing.T) {
	t.Parallel()
	report := CalibrateHostCapacity(HostCapacityCalibrationInput{
		ProfileID:     "weak-host",
		MissingSource: []string{"synthetic_swarm_artifacts"},
		Baseline: map[Source]Thresholds{
			SourceMemory: {Elevated: 0.65, High: 0.82, Critical: 0.92},
		},
		Evidence: []CalibrationEvidence{
			{Source: SourceMemory, Value: 0.95, Unit: "ratio", Stable: true, Confidence: 0.1, SampleCount: 1},
		},
		MinConfidence: 0.5,
	})

	if len(report.Recommendations) != 1 {
		t.Fatalf("recommendations = %d, want 1", len(report.Recommendations))
	}
	rec := report.Recommendations[0]
	if rec.Apply {
		t.Fatalf("Apply = true, want false for low confidence")
	}
	if rec.Reason != "low_confidence" {
		t.Fatalf("Reason = %q, want low_confidence", rec.Reason)
	}
	if got := report.Profile.Thresholds[SourceMemory]; got != rec.Current {
		t.Fatalf("profile thresholds changed despite low confidence: %+v vs %+v", got, rec.Current)
	}
	if len(report.Warnings) != 2 {
		t.Fatalf("warnings = %d, want missing source + low confidence", len(report.Warnings))
	}
}

func TestCalibrateHostCapacityDeterministicJSONOrdering(t *testing.T) {
	t.Parallel()
	input := HostCapacityCalibrationInput{
		ProfileID: "order-host",
		Now:       time.Date(2026, 5, 9, 7, 30, 0, 0, time.UTC),
		Baseline: map[Source]Thresholds{
			SourceMemory:       {Elevated: 0.65, High: 0.82, Critical: 0.92},
			SourceCPU:          {Elevated: 0.60, High: 0.80, Critical: 0.92},
			SourcePaneActivity: {Elevated: 50, High: 100, Critical: 200},
		},
		Evidence: []CalibrationEvidence{
			{Source: SourcePaneActivity, Value: 120, Stable: true, Confidence: 0.9, SampleCount: 20},
			{Source: SourceCPU, Value: 0.75, Stable: false, Confidence: 0.9, SampleCount: 20},
			{Source: SourceMemory, Value: 0.70, Stable: true, Confidence: 0.9, SampleCount: 20},
		},
	}
	first := CalibrateHostCapacity(input)
	second := CalibrateHostCapacity(input)

	a, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("json.Marshal(first): %v", err)
	}
	b, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("json.Marshal(second): %v", err)
	}
	if string(a) != string(b) {
		t.Fatalf("calibration JSON drifted:\nfirst:  %s\nsecond: %s", a, b)
	}
	wantOrder := []string{"cpu", "memory", "pane_activity"}
	for i, rec := range first.Recommendations {
		if rec.Source != wantOrder[i] {
			t.Fatalf("recommendations[%d] = %s, want %s", i, rec.Source, wantOrder[i])
		}
	}
}
