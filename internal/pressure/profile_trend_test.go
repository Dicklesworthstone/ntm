package pressure

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestHostProfileTrendStableProfile(t *testing.T) {
	t.Parallel()
	baseline := testTrendRecord("baseline", "host-a", map[Source]Thresholds{
		SourceCPU:    {Elevated: 0.60, High: 0.80, Critical: 0.92},
		SourceMemory: {Elevated: 0.65, High: 0.82, Critical: 0.92},
	})
	current := baseline
	current.ProfileID = "current"
	current.BaselineID = baseline.ProfileID
	current.RecordedAt = current.RecordedAt.Add(time.Hour)

	report := EvaluateHostProfileTrend(HostProfileTrendInput{
		Current:  current,
		Baseline: &baseline,
	})

	if profileTrendTestNotEqual(report.Decision, "stable") {
		t.Fatalf("Decision = %q, want stable", report.Decision)
	}
	if len(report.Warnings) != 0 {
		t.Fatalf("Warnings = %+v, want none", report.Warnings)
	}
	if len(report.DeletedProfileIDs) != 0 {
		t.Fatalf("DeletedProfileIDs = %+v, want none", report.DeletedProfileIDs)
	}
	if len(report.RetainedProfileIDs) != 2 {
		t.Fatalf("RetainedProfileIDs = %+v, want baseline and current retained", report.RetainedProfileIDs)
	}
}

func TestHostProfileTrendDetectsCPUMemoryDrift(t *testing.T) {
	t.Parallel()
	baseline := testTrendRecord("baseline", "host-a", map[Source]Thresholds{
		SourceCPU:    {Elevated: 0.60, High: 0.80, Critical: 0.92},
		SourceMemory: {Elevated: 0.65, High: 0.82, Critical: 0.92},
	})
	current := testTrendRecord("current", "host-a", map[Source]Thresholds{
		SourceCPU:    {Elevated: 0.50, High: 0.58, Critical: 0.70},
		SourceMemory: {Elevated: 0.65, High: 0.95, Critical: 0.98},
	})
	current.BaselineID = baseline.ProfileID

	report := EvaluateHostProfileTrend(HostProfileTrendInput{
		Current:  current,
		Baseline: &baseline,
	})

	if profileTrendTestNotEqual(report.Decision, "critical") {
		t.Fatalf("Decision = %q, want critical", report.Decision)
	}
	if !trendHasWarning(report, "cpu.high", "critical", "threshold_drift") {
		t.Fatalf("missing critical cpu.high warning: %+v", report.Warnings)
	}
	if !trendHasWarning(report, "memory.high", "warning", "threshold_drift") {
		t.Fatalf("missing warning memory.high warning: %+v", report.Warnings)
	}
	row := report.LogRows[0]
	if profileTrendTestEqual(row.ProfileID, "") ||
		profileTrendTestEqual(row.BaselineID, "") ||
		profileTrendTestEqual(row.DriftMetric, "") ||
		profileTrendTestEqual(row.OldValue, "") ||
		profileTrendTestEqual(row.NewValue, "") ||
		profileTrendTestEqual(row.Severity, "") ||
		profileTrendTestEqual(row.Reason, "") {
		t.Fatalf("log row missing required fields: %+v", row)
	}
}

func TestHostProfileTrendDetectsRCHFleetDrift(t *testing.T) {
	t.Parallel()
	baseline := testTrendRecord("baseline", "host-a", map[Source]Thresholds{
		SourceRchQueue: {Elevated: 0.60, High: 0.80, Critical: 0.95},
	})
	baseline.Dependencies = []HostProfileDependency{{Name: "rch_fleet", Version: "8-workers"}}
	current := baseline
	current.ProfileID = "current"
	current.BaselineID = baseline.ProfileID
	current.Dependencies = []HostProfileDependency{{Name: "rch_fleet", Version: "5-workers"}}

	report := EvaluateHostProfileTrend(HostProfileTrendInput{
		Current:  current,
		Baseline: &baseline,
	})

	if profileTrendTestNotEqual(report.Decision, "critical") {
		t.Fatalf("Decision = %q, want critical", report.Decision)
	}
	if !trendHasWarning(report, "dependency.rch_fleet", "critical", "dependency_drift") {
		t.Fatalf("missing rch dependency drift warning: %+v", report.Warnings)
	}
}

func TestHostProfileTrendMissingBaseline(t *testing.T) {
	t.Parallel()
	current := testTrendRecord("current", "host-a", map[Source]Thresholds{
		SourceCPU: {Elevated: 0.60, High: 0.80, Critical: 0.92},
	})
	current.BaselineID = "missing-baseline"

	report := EvaluateHostProfileTrend(HostProfileTrendInput{Current: current})

	if profileTrendTestNotEqual(report.Decision, "unknown") {
		t.Fatalf("Decision = %q, want unknown", report.Decision)
	}
	if !trendHasWarning(report, "baseline", "unknown", "missing_baseline") {
		t.Fatalf("missing baseline warning: %+v", report.Warnings)
	}
	if len(report.DeletedProfileIDs) != 0 {
		t.Fatalf("DeletedProfileIDs = %+v, want none", report.DeletedProfileIDs)
	}
}

func TestHostProfileTrendMigratesLegacySchema(t *testing.T) {
	t.Parallel()
	baseline := testTrendRecord("baseline", "raw-hostname", map[Source]Thresholds{
		SourceCPU: {Elevated: 0.60, High: 0.80, Critical: 0.92},
	})
	baseline.SchemaVersion = "legacy.v0"
	baseline.MachineFingerprint = "raw-hostname"
	current := baseline
	current.ProfileID = "current"
	current.BaselineID = baseline.ProfileID

	report := EvaluateHostProfileTrend(HostProfileTrendInput{
		Current:  current,
		Baseline: &baseline,
	})

	if !trendHasWarning(report, "schema.version", "info", "schema_migrated") {
		t.Fatalf("missing schema migration warning: %+v", report.Warnings)
	}
	migrated := MigrateHostProfileTrendRecord(baseline)
	if profileTrendTestNotEqual(migrated.SchemaVersion, HostProfileTrendSchemaVersion) {
		t.Fatalf("SchemaVersion = %q, want %q", migrated.SchemaVersion, HostProfileTrendSchemaVersion)
	}
	if strings.Contains(migrated.MachineFingerprint, "raw-hostname") {
		t.Fatalf("MachineFingerprint leaked raw host: %q", migrated.MachineFingerprint)
	}
}

func TestHostProfileTrendRedactsMachineFingerprint(t *testing.T) {
	t.Parallel()
	rawHost := "prod-host-with-sensitive-project-name"
	record := testTrendRecord("profile", rawHost, map[Source]Thresholds{
		SourceCPU: {Elevated: 0.60, High: 0.80, Critical: 0.92},
	})

	if strings.Contains(record.MachineFingerprint, rawHost) {
		t.Fatalf("MachineFingerprint leaked raw host: %q", record.MachineFingerprint)
	}
	if !strings.HasPrefix(record.MachineFingerprint, "machine:") {
		t.Fatalf("MachineFingerprint = %q, want machine: prefix", record.MachineFingerprint)
	}
	if profileTrendTestNotEqual(record.MachineFingerprint, SanitizeMachineFingerprint(rawHost)) {
		t.Fatalf("MachineFingerprint is not deterministic")
	}

	payload, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("json.Marshal(record): %v", err)
	}
	if strings.Contains(string(payload), rawHost) {
		t.Fatalf("trend record JSON leaked raw host: %s", payload)
	}
}

func testTrendRecord(profileID, rawHost string, thresholds map[Source]Thresholds) HostProfileTrendRecord {
	profile := HostCapacityProfile{
		ProfileID:  profileID,
		Generated:  time.Date(2026, 5, 9, 10, 0, 0, 0, time.UTC),
		Thresholds: thresholds,
		Sources: []CalibrationSourceRow{
			{Source: string(SourceCPU), Samples: 10, Confidence: 0.9, Reason: "test"},
			{Source: string(SourceMemory), Samples: 10, Confidence: 0.9, Reason: "test"},
			{Source: string(SourceRchQueue), Samples: 10, Confidence: 0.9, Reason: "test"},
		},
	}
	return BuildHostProfileTrendRecord(HostProfileTrendRecordInput{
		Profile:            profile,
		ProfileID:          profileID,
		RecordedAt:         profile.Generated,
		MachineFingerprint: rawHost,
		Dependencies: []HostProfileDependency{
			{Name: "go", Version: "1.25"},
			{Name: "ntm", Version: "test"},
		},
	})
}

func trendHasWarning(report HostProfileTrendReport, metric, severity, reason string) bool {
	for _, warning := range report.Warnings {
		if profileTrendTestEqual(warning.DriftMetric, metric) &&
			profileTrendTestEqual(warning.Severity, severity) &&
			profileTrendTestEqual(warning.Reason, reason) {
			return true
		}
	}
	return false
}

func profileTrendTestEqual[T comparable](left, right T) bool {
	return left == right
}

func profileTrendTestNotEqual[T comparable](left, right T) bool {
	return !profileTrendTestEqual(left, right)
}
