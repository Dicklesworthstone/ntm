package pressure

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// HostProfileTrendSchemaVersion is the stable JSON schema token for compact
	// host profile trend records.
	HostProfileTrendSchemaVersion = "ntm.host_profile_trend.v1"

	hostProfileTrendDefaultID = "host-profile"
)

// HostProfileTrendRecordInput captures the data needed to persist one compact
// host profile trend record.
type HostProfileTrendRecordInput struct {
	Profile            HostCapacityProfile
	ProfileID          string
	BaselineID         string
	RecordedAt         time.Time
	MachineFingerprint string
	Dependencies       []HostProfileDependency
}

// HostProfileDependency records dependency versions that can explain drift.
type HostProfileDependency struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// HostProfileTrendRecord is a compact, append-friendly snapshot of one host
// capacity profile.
type HostProfileTrendRecord struct {
	SchemaVersion      string                   `json:"schema_version"`
	ProfileID          string                   `json:"profile_id"`
	BaselineID         string                   `json:"baseline_id,omitempty"`
	RecordedAt         time.Time                `json:"recorded_at"`
	MachineFingerprint string                   `json:"machine_fingerprint"`
	Dependencies       []HostProfileDependency  `json:"dependencies,omitempty"`
	Metrics            []HostProfileTrendMetric `json:"metrics"`
}

// HostProfileTrendMetric is one source's threshold summary in a trend record.
type HostProfileTrendMetric struct {
	Source     string  `json:"source"`
	Elevated   float64 `json:"elevated"`
	High       float64 `json:"high"`
	Critical   float64 `json:"critical"`
	Confidence float64 `json:"confidence,omitempty"`
	Samples    int     `json:"samples,omitempty"`
}

// HostProfileTrendInput compares a current record to an optional baseline.
type HostProfileTrendInput struct {
	Current  HostProfileTrendRecord
	Baseline *HostProfileTrendRecord
}

// HostProfileTrendReport is the robot-ready drift evaluation.
type HostProfileTrendReport struct {
	SchemaVersion      string                    `json:"schema_version"`
	ProfileID          string                    `json:"profile_id"`
	BaselineID         string                    `json:"baseline_id,omitempty"`
	Decision           string                    `json:"decision"`
	Warnings           []HostProfileDriftWarning `json:"warnings,omitempty"`
	LogRows            []HostProfileDriftLogRow  `json:"log_rows,omitempty"`
	RetainedProfileIDs []string                  `json:"retained_profile_ids,omitempty"`
	DeletedProfileIDs  []string                  `json:"deleted_profile_ids"`
}

// HostProfileDriftWarning describes a detected profile drift or degraded input.
type HostProfileDriftWarning struct {
	ProfileID   string `json:"profile_id"`
	BaselineID  string `json:"baseline_id,omitempty"`
	DriftMetric string `json:"drift_metric"`
	OldValue    string `json:"old_value,omitempty"`
	NewValue    string `json:"new_value,omitempty"`
	Severity    string `json:"severity"`
	Reason      string `json:"reason"`
}

// HostProfileDriftLogRow carries stable structured fields for slog or artifacts.
type HostProfileDriftLogRow struct {
	ProfileID   string `json:"profile_id"`
	BaselineID  string `json:"baseline_id,omitempty"`
	DriftMetric string `json:"drift_metric"`
	OldValue    string `json:"old_value,omitempty"`
	NewValue    string `json:"new_value,omitempty"`
	Severity    string `json:"severity"`
	Reason      string `json:"reason"`
}

// BuildHostProfileTrendRecord converts a calibration profile into a compact,
// redacted trend record. It does not write files or prune older records.
func BuildHostProfileTrendRecord(in HostProfileTrendRecordInput) HostProfileTrendRecord {
	recordedAt := in.RecordedAt
	if recordedAt.IsZero() {
		recordedAt = time.Now().UTC()
	}

	profileID := strings.TrimSpace(in.ProfileID)
	if profileTrendValueEqual(profileID, "") {
		profileID = strings.TrimSpace(in.Profile.ProfileID)
	}
	if profileTrendValueEqual(profileID, "") {
		profileID = hostProfileTrendDefaultID
	}

	return HostProfileTrendRecord{
		SchemaVersion:      HostProfileTrendSchemaVersion,
		ProfileID:          profileID,
		BaselineID:         strings.TrimSpace(in.BaselineID),
		RecordedAt:         recordedAt.UTC(),
		MachineFingerprint: SanitizeMachineFingerprint(in.MachineFingerprint),
		Dependencies:       normalizedHostProfileDependencies(in.Dependencies),
		Metrics:            metricsFromCapacityProfile(in.Profile),
	}
}

// SanitizeMachineFingerprint returns a stable non-reversible machine token.
func SanitizeMachineFingerprint(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if profileTrendValueEqual(trimmed, "") {
		return "machine:unknown"
	}
	if isSanitizedMachineFingerprint(trimmed) {
		return trimmed
	}
	sum := sha256.Sum256([]byte(trimmed))
	return "machine:" + hex.EncodeToString(sum[:8])
}

// MigrateHostProfileTrendRecord normalizes older trend records to the current
// schema without dropping any caller-owned history.
func MigrateHostProfileTrendRecord(record HostProfileTrendRecord) HostProfileTrendRecord {
	if !profileTrendValueEqual(record.SchemaVersion, HostProfileTrendSchemaVersion) {
		record.SchemaVersion = HostProfileTrendSchemaVersion
	}
	record.ProfileID = strings.TrimSpace(record.ProfileID)
	if profileTrendValueEqual(record.ProfileID, "") {
		record.ProfileID = hostProfileTrendDefaultID
	}
	record.BaselineID = strings.TrimSpace(record.BaselineID)
	record.MachineFingerprint = SanitizeMachineFingerprint(record.MachineFingerprint)
	record.Dependencies = normalizedHostProfileDependencies(record.Dependencies)
	sort.Slice(record.Metrics, func(i, j int) bool { return record.Metrics[i].Source < record.Metrics[j].Source })
	return record
}

// EvaluateHostProfileTrend compares the current trend record to a baseline and
// emits drift warnings. It never returns delete actions for older profiles.
func EvaluateHostProfileTrend(in HostProfileTrendInput) HostProfileTrendReport {
	current := MigrateHostProfileTrendRecord(in.Current)
	report := HostProfileTrendReport{
		SchemaVersion:      HostProfileTrendSchemaVersion,
		ProfileID:          current.ProfileID,
		BaselineID:         current.BaselineID,
		Decision:           "stable",
		DeletedProfileIDs:  []string{},
		RetainedProfileIDs: []string{current.ProfileID},
	}

	if in.Baseline == nil {
		warning := HostProfileDriftWarning{
			ProfileID:   current.ProfileID,
			BaselineID:  current.BaselineID,
			DriftMetric: "baseline",
			Severity:    "unknown",
			Reason:      "missing_baseline",
		}
		report.appendWarning(warning)
		report.Decision = "unknown"
		return report
	}

	baselineBeforeMigration := *in.Baseline
	baseline := MigrateHostProfileTrendRecord(*in.Baseline)
	report.BaselineID = firstNonEmpty(current.BaselineID, baseline.ProfileID)
	report.RetainedProfileIDs = uniqueSortedStrings([]string{current.ProfileID, baseline.ProfileID})

	if !profileTrendValueEqual(baselineBeforeMigration.SchemaVersion, HostProfileTrendSchemaVersion) {
		report.appendWarning(HostProfileDriftWarning{
			ProfileID:   current.ProfileID,
			BaselineID:  baseline.ProfileID,
			DriftMetric: "schema.version",
			OldValue:    strings.TrimSpace(baselineBeforeMigration.SchemaVersion),
			NewValue:    HostProfileTrendSchemaVersion,
			Severity:    "info",
			Reason:      "schema_migrated",
		})
	}

	baselineMetrics := hostProfileMetricsBySource(baseline.Metrics)
	for _, metric := range current.Metrics {
		oldMetric, ok := baselineMetrics[metric.Source]
		if !ok {
			report.appendWarning(HostProfileDriftWarning{
				ProfileID:   current.ProfileID,
				BaselineID:  baseline.ProfileID,
				DriftMetric: metric.Source,
				NewValue:    "present",
				Severity:    "warning",
				Reason:      "new_metric",
			})
			continue
		}
		report.compareThreshold(metric.Source+".elevated", oldMetric.Elevated, metric.Elevated, current.ProfileID, baseline.ProfileID)
		report.compareThreshold(metric.Source+".high", oldMetric.High, metric.High, current.ProfileID, baseline.ProfileID)
		report.compareThreshold(metric.Source+".critical", oldMetric.Critical, metric.Critical, current.ProfileID, baseline.ProfileID)
	}

	report.compareDependencies(current, baseline)
	if len(report.Warnings) > 0 && profileTrendValueEqual(report.Decision, "stable") {
		report.Decision = highestHostProfileSeverity(report.Warnings)
	}
	return report
}

func (r *HostProfileTrendReport) compareThreshold(metric string, oldValue, newValue float64, profileID, baselineID string) {
	severity := hostProfileDriftSeverity(oldValue, newValue)
	if profileTrendValueEqual(severity, "") {
		return
	}
	r.appendWarning(HostProfileDriftWarning{
		ProfileID:   profileID,
		BaselineID:  baselineID,
		DriftMetric: metric,
		OldValue:    formatHostProfileValue(oldValue),
		NewValue:    formatHostProfileValue(newValue),
		Severity:    severity,
		Reason:      "threshold_drift",
	})
}

func (r *HostProfileTrendReport) compareDependencies(current, baseline HostProfileTrendRecord) {
	baselineDeps := hostProfileDependenciesByName(baseline.Dependencies)
	for _, dep := range current.Dependencies {
		oldVersion, ok := baselineDeps[dep.Name]
		if !ok || profileTrendValueEqual(oldVersion, dep.Version) {
			continue
		}
		severity := "warning"
		if strings.Contains(dep.Name, "rch") {
			severity = "critical"
		}
		r.appendWarning(HostProfileDriftWarning{
			ProfileID:   current.ProfileID,
			BaselineID:  baseline.ProfileID,
			DriftMetric: "dependency." + dep.Name,
			OldValue:    oldVersion,
			NewValue:    dep.Version,
			Severity:    severity,
			Reason:      "dependency_drift",
		})
	}
}

func (r *HostProfileTrendReport) appendWarning(warning HostProfileDriftWarning) {
	r.Warnings = append(r.Warnings, warning)
	r.LogRows = append(r.LogRows, HostProfileDriftLogRow(warning))
}

func metricsFromCapacityProfile(profile HostCapacityProfile) []HostProfileTrendMetric {
	sourceRows := make(map[string]CalibrationSourceRow, len(profile.Sources))
	for _, row := range profile.Sources {
		sourceRows[row.Source] = row
	}

	sources := make([]Source, 0, len(profile.Thresholds))
	for source := range profile.Thresholds {
		sources = append(sources, source)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i] < sources[j] })

	metrics := make([]HostProfileTrendMetric, 0, len(sources))
	for _, source := range sources {
		thresholds := profile.Thresholds[source]
		row := sourceRows[string(source)]
		metrics = append(metrics, HostProfileTrendMetric{
			Source:     string(source),
			Elevated:   round3Float(thresholds.Elevated),
			High:       round3Float(thresholds.High),
			Critical:   round3Float(thresholds.Critical),
			Confidence: row.Confidence,
			Samples:    row.Samples,
		})
	}
	return metrics
}

func normalizedHostProfileDependencies(deps []HostProfileDependency) []HostProfileDependency {
	byName := make(map[string]string, len(deps))
	for _, dep := range deps {
		name := strings.TrimSpace(dep.Name)
		if profileTrendValueEqual(name, "") {
			continue
		}
		byName[name] = strings.TrimSpace(dep.Version)
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]HostProfileDependency, 0, len(names))
	for _, name := range names {
		out = append(out, HostProfileDependency{Name: name, Version: byName[name]})
	}
	return out
}

func hostProfileMetricsBySource(metrics []HostProfileTrendMetric) map[string]HostProfileTrendMetric {
	bySource := make(map[string]HostProfileTrendMetric, len(metrics))
	for _, metric := range metrics {
		bySource[metric.Source] = metric
	}
	return bySource
}

func hostProfileDependenciesByName(deps []HostProfileDependency) map[string]string {
	byName := make(map[string]string, len(deps))
	for _, dep := range deps {
		byName[dep.Name] = dep.Version
	}
	return byName
}

func hostProfileDriftSeverity(oldValue, newValue float64) string {
	if oldValue <= 0 || math.IsNaN(oldValue) || math.IsInf(oldValue, 0) {
		if newValue > 0 {
			return "warning"
		}
		return ""
	}
	delta := math.Abs(newValue-oldValue) / oldValue
	switch {
	case delta >= 0.25:
		return "critical"
	case delta >= 0.10:
		return "warning"
	default:
		return ""
	}
}

func highestHostProfileSeverity(warnings []HostProfileDriftWarning) string {
	highest := "stable"
	for _, warning := range warnings {
		switch warning.Severity {
		case "critical":
			return "critical"
		case "warning":
			highest = "warning"
		case "unknown":
			if profileTrendValueEqual(highest, "stable") {
				highest = "unknown"
			}
		case "info":
			if profileTrendValueEqual(highest, "stable") {
				highest = "info"
			}
		}
	}
	return highest
}

func formatHostProfileValue(value float64) string {
	return strconv.FormatFloat(round3Float(value), 'f', -1, 64)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !profileTrendValueEqual(value, "") {
			return value
		}
	}
	return ""
}

func isSanitizedMachineFingerprint(value string) bool {
	const prefix = "machine:"
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	digest := strings.TrimPrefix(value, prefix)
	if len(digest) != 16 {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func profileTrendValueEqual[T comparable](left, right T) bool {
	return left == right
}
