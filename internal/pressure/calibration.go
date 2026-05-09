package pressure

import (
	"math"
	"sort"
	"strings"
	"time"
)

const defaultMinCalibrationConfidence = 0.35

// HostCapacityProfile is the config-ready output of a calibration pass.
// Callers can serialize it or render it for review, but calibration never
// mutates governor defaults on its own.
type HostCapacityProfile struct {
	ProfileID  string                 `json:"profile_id"`
	Generated  time.Time              `json:"generated_at"`
	Thresholds map[Source]Thresholds  `json:"thresholds"`
	Notes      []CalibrationNote      `json:"notes,omitempty"`
	Sources    []CalibrationSourceRow `json:"sources,omitempty"`
}

// CalibrationSourceRow keeps profile source metadata deterministic in JSON.
type CalibrationSourceRow struct {
	Source     string  `json:"source"`
	Samples    int     `json:"samples"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason,omitempty"`
}

// CalibrationNote describes missing or weak evidence in machine-readable form.
type CalibrationNote struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// CalibrationEvidence is one observed source value from a synthetic or real run.
type CalibrationEvidence struct {
	Source      Source  `json:"source"`
	Value       float64 `json:"value"`
	Unit        string  `json:"unit,omitempty"`
	Stable      bool    `json:"stable"`
	Confidence  float64 `json:"confidence,omitempty"`
	SampleCount int     `json:"sample_count,omitempty"`
	TestRunID   string  `json:"test_run_id,omitempty"`
	Reason      string  `json:"reason,omitempty"`
}

// HostCapacityCalibrationInput contains all evidence for one calibration run.
type HostCapacityCalibrationInput struct {
	ProfileID     string
	Now           time.Time
	Baseline      map[Source]Thresholds
	Evidence      []CalibrationEvidence
	MissingSource []string
	MinConfidence float64
}

// ThresholdRecommendation is a per-source threshold change suggestion.
type ThresholdRecommendation struct {
	Source           string     `json:"source"`
	Current          Thresholds `json:"current"`
	ObservedCapacity float64    `json:"observed_capacity,omitempty"`
	Unit             string     `json:"unit,omitempty"`
	Recommended      Thresholds `json:"recommended"`
	Confidence       float64    `json:"confidence"`
	Samples          int        `json:"samples"`
	TestRunIDs       []string   `json:"test_run_ids,omitempty"`
	Apply            bool       `json:"apply"`
	Reason           string     `json:"reason"`
}

// CalibrationLogRow carries structured fields callers can pass directly to
// slog or a robot artifact without reinterpreting recommendation details.
type CalibrationLogRow struct {
	TestRunID      string     `json:"test_run_id,omitempty"`
	HostProfileID  string     `json:"host_profile_id"`
	Source         string     `json:"source"`
	Threshold      Thresholds `json:"threshold"`
	Recommendation Thresholds `json:"recommendation"`
	Confidence     float64    `json:"confidence"`
	Reason         string     `json:"reason"`
}

// HostCapacityCalibrationReport is a deterministic robot/config-ready envelope.
type HostCapacityCalibrationReport struct {
	Success         bool                      `json:"success"`
	GeneratedAt     time.Time                 `json:"generated_at"`
	ProfileID       string                    `json:"profile_id"`
	Recommendations []ThresholdRecommendation `json:"recommendations"`
	Profile         HostCapacityProfile       `json:"profile"`
	LogRows         []CalibrationLogRow       `json:"log_rows,omitempty"`
	Warnings        []CalibrationNote         `json:"warnings,omitempty"`
}

type calibrationBucket struct {
	source       Source
	current      Thresholds
	stableMax    float64
	hasStable    bool
	unstableMin  float64
	hasUnstable  bool
	confidence   float64
	samples      int
	unit         string
	testRunIDs   []string
	reasonParts  []string
	evidenceSeen bool
}

// CalibrateHostCapacity reduces observed source values into threshold
// recommendations. It is deliberately side-effect free: no defaults or config
// files are changed until a caller explicitly applies the returned profile.
func CalibrateHostCapacity(in HostCapacityCalibrationInput) HostCapacityCalibrationReport {
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	minConfidence := in.MinConfidence
	if minConfidence <= 0 {
		minConfidence = defaultMinCalibrationConfidence
	}
	baseline := cloneThresholds(in.Baseline)
	if len(baseline) == 0 {
		baseline = DefaultThresholds()
	}

	buckets := make(map[Source]*calibrationBucket, len(baseline))
	for source, current := range baseline {
		buckets[source] = &calibrationBucket{
			source:      source,
			current:     current,
			unstableMin: math.Inf(1),
		}
	}

	for _, ev := range in.Evidence {
		if _, ok := baseline[ev.Source]; !ok || !isFinitePositive(ev.Value) {
			continue
		}
		b := buckets[ev.Source]
		b.evidenceSeen = true
		samples := ev.SampleCount
		if samples <= 0 {
			samples = 1
		}
		conf := clamp01(ev.Confidence)
		if conf == 0 {
			conf = confidenceFromSamples(samples)
		}
		b.samples += samples
		if conf > b.confidence {
			b.confidence = conf
		}
		if ev.Reason != "" {
			b.reasonParts = append(b.reasonParts, ev.Reason)
		}
		if b.unit == "" && ev.Unit != "" {
			b.unit = ev.Unit
		}
		if id := strings.TrimSpace(ev.TestRunID); id != "" {
			b.testRunIDs = append(b.testRunIDs, id)
		}
		if ev.Stable {
			if !b.hasStable || ev.Value > b.stableMax {
				b.stableMax = ev.Value
				b.hasStable = true
			}
			continue
		}
		if ev.Value < b.unstableMin {
			b.unstableMin = ev.Value
			b.hasUnstable = true
		}
	}

	sources := sortedCalibrationSources(buckets)
	out := HostCapacityCalibrationReport{
		Success:     true,
		GeneratedAt: now.UTC(),
		ProfileID:   strings.TrimSpace(in.ProfileID),
	}
	if out.ProfileID == "" {
		out.ProfileID = "host-capacity"
	}
	out.Profile = HostCapacityProfile{
		ProfileID:  out.ProfileID,
		Generated:  out.GeneratedAt,
		Thresholds: cloneThresholds(baseline),
	}

	for _, missing := range in.MissingSource {
		missing = strings.TrimSpace(missing)
		if missing == "" {
			continue
		}
		out.Warnings = append(out.Warnings, CalibrationNote{
			Code:    "missing_source",
			Message: missing + " unavailable: source not loaded",
		})
	}

	for _, source := range sources {
		b := buckets[source]
		if !b.evidenceSeen {
			continue
		}
		rec := recommendationFromBucket(*b, minConfidence)
		out.Recommendations = append(out.Recommendations, rec)
		out.Profile.Sources = append(out.Profile.Sources, CalibrationSourceRow{
			Source:     string(source),
			Samples:    b.samples,
			Confidence: round3Float(b.confidence),
			Reason:     rec.Reason,
		})
		if rec.Apply {
			out.Profile.Thresholds[source] = rec.Recommended
		}
		out.LogRows = append(out.LogRows, logRowsForRecommendation(out.ProfileID, rec)...)
		if !rec.Apply {
			out.Warnings = append(out.Warnings, CalibrationNote{
				Code:    "low_confidence",
				Message: string(source) + " recommendation not applied: " + rec.Reason,
			})
		}
	}
	out.Profile.Notes = append([]CalibrationNote(nil), out.Warnings...)
	return out
}

func recommendationFromBucket(b calibrationBucket, minConfidence float64) ThresholdRecommendation {
	rec := ThresholdRecommendation{
		Source:      string(b.source),
		Current:     b.current,
		Unit:        b.unit,
		Recommended: b.current,
		Confidence:  round3Float(b.confidence),
		Samples:     b.samples,
		TestRunIDs:  uniqueSortedStrings(b.testRunIDs),
		Apply:       true,
	}
	switch {
	case b.hasUnstable:
		rec.ObservedCapacity = round3Float(b.unstableMin)
	case b.hasStable:
		rec.ObservedCapacity = round3Float(b.stableMax)
	}
	switch {
	case b.confidence < minConfidence:
		rec.Apply = false
		rec.Reason = "low_confidence"
	case b.hasUnstable:
		rec.Recommended = lowerThresholdsForInstability(b.current, b.unstableMin, b.source)
		rec.Reason = "unstable_before_current_limit"
	case b.hasStable:
		rec.Recommended = raiseThresholdsForStableCapacity(b.current, b.stableMax, b.source)
		rec.Reason = "stable_capacity_observed"
	default:
		rec.Apply = false
		rec.Reason = "no_usable_evidence"
	}
	if rec.Recommended == rec.Current {
		rec.Apply = false
		if rec.Reason == "" || rec.Reason == "stable_capacity_observed" {
			rec.Reason = "no_threshold_change"
		}
	}
	if len(b.reasonParts) > 0 && rec.Reason != "low_confidence" {
		rec.Reason = rec.Reason + ":" + joinUniqueReasons(b.reasonParts)
	}
	return rec
}

func logRowsForRecommendation(profileID string, rec ThresholdRecommendation) []CalibrationLogRow {
	ids := rec.TestRunIDs
	if len(ids) == 0 {
		ids = []string{""}
	}
	out := make([]CalibrationLogRow, 0, len(ids))
	for _, id := range ids {
		out = append(out, CalibrationLogRow{
			TestRunID:      id,
			HostProfileID:  profileID,
			Source:         rec.Source,
			Threshold:      rec.Current,
			Recommendation: rec.Recommended,
			Confidence:     rec.Confidence,
			Reason:         rec.Reason,
		})
	}
	return out
}

func raiseThresholdsForStableCapacity(current Thresholds, stableMax float64, source Source) Thresholds {
	next := current
	next.Elevated = math.Max(next.Elevated, stableMax*0.70)
	next.High = math.Max(next.High, stableMax*0.90)
	next.Critical = math.Max(next.Critical, stableMax*1.10)
	return normalizeThresholds(next, source)
}

func lowerThresholdsForInstability(current Thresholds, unstableMin float64, source Source) Thresholds {
	next := current
	next.High = math.Min(next.High, unstableMin*0.90)
	next.Critical = math.Min(next.Critical, unstableMin*0.98)
	next.Elevated = math.Min(next.Elevated, next.High*0.75)
	return normalizeThresholds(next, source)
}

func normalizeThresholds(t Thresholds, source Source) Thresholds {
	if t.Elevated < 0 {
		t.Elevated = 0
	}
	if t.High < t.Elevated {
		t.High = t.Elevated
	}
	if t.Critical < t.High {
		t.Critical = t.High
	}
	if isRatioSource(source) {
		t.Elevated = math.Min(t.Elevated, 1)
		t.High = math.Min(t.High, 1)
		t.Critical = math.Min(t.Critical, 1)
	}
	t.Elevated = round3Float(t.Elevated)
	t.High = round3Float(t.High)
	t.Critical = round3Float(t.Critical)
	return t
}

func sortedCalibrationSources(buckets map[Source]*calibrationBucket) []Source {
	out := make([]Source, 0, len(buckets))
	for source := range buckets {
		out = append(out, source)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func cloneThresholds(in map[Source]Thresholds) map[Source]Thresholds {
	if len(in) == 0 {
		return nil
	}
	out := make(map[Source]Thresholds, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func isRatioSource(source Source) bool {
	switch source {
	case SourceCPU, SourceMemory, SourceRchQueue:
		return true
	default:
		return false
	}
}

func isFinitePositive(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v > 0
}

func clamp01(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}

func confidenceFromSamples(samples int) float64 {
	if samples <= 0 {
		return 0.1
	}
	return math.Min(1, math.Log1p(float64(samples))/math.Log1p(100))
}

func round3Float(v float64) float64 {
	return math.Round(v*1000) / 1000
}

func joinUniqueReasons(parts []string) string {
	out := uniqueSortedStrings(parts)
	return strings.Join(out, ",")
}

func uniqueSortedStrings(parts []string) []string {
	seen := make(map[string]struct{}, len(parts))
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		out = append(out, part)
	}
	sort.Strings(out)
	return out
}

// SyntheticCalibrationMetrics mirrors the stable fields emitted by
// internal/swarm.SyntheticMetrics without importing that package into pressure.
type SyntheticCalibrationMetrics struct {
	TestRunID               string
	ScenarioName            string
	PaneCount               int
	CommandCount            int
	EventCount              int
	LatencyP95Micros        int64
	MemoryGrowthBytes       int64
	GoroutinesLeaked        int
	SyntheticDurationMicros int64
}

// SyntheticCalibrationLimits classify a synthetic run as stable or unstable.
type SyntheticCalibrationLimits struct {
	MaxLatencyP95Micros  int64
	MaxMemoryGrowthBytes int64
	MaxGoroutinesLeaked  int
}

// EvidenceFromSyntheticRuns converts synthetic swarm artifacts into pane
// activity calibration evidence.
func EvidenceFromSyntheticRuns(runs []SyntheticCalibrationMetrics, limits SyntheticCalibrationLimits) []CalibrationEvidence {
	if limits.MaxLatencyP95Micros <= 0 {
		limits.MaxLatencyP95Micros = 250_000
	}
	if limits.MaxMemoryGrowthBytes <= 0 {
		limits.MaxMemoryGrowthBytes = 64 << 20
	}
	if limits.MaxGoroutinesLeaked < 0 {
		limits.MaxGoroutinesLeaked = 0
	}
	out := make([]CalibrationEvidence, 0, len(runs))
	for _, run := range runs {
		if run.PaneCount <= 0 {
			continue
		}
		stable := run.LatencyP95Micros <= limits.MaxLatencyP95Micros &&
			run.MemoryGrowthBytes <= limits.MaxMemoryGrowthBytes &&
			run.GoroutinesLeaked <= limits.MaxGoroutinesLeaked
		samples := run.EventCount
		if samples <= 0 {
			samples = run.PaneCount * maxInt(run.CommandCount, 1)
		}
		out = append(out, CalibrationEvidence{
			Source:      SourcePaneActivity,
			Value:       float64(run.PaneCount),
			Unit:        "panes",
			Stable:      stable,
			Confidence:  confidenceFromSamples(samples),
			SampleCount: samples,
			TestRunID:   strings.TrimSpace(run.TestRunID),
			Reason:      syntheticRunReason(run, stable),
		})
	}
	return out
}

func syntheticRunReason(run SyntheticCalibrationMetrics, stable bool) string {
	state := "stable"
	if !stable {
		state = "unstable"
	}
	id := strings.TrimSpace(run.TestRunID)
	if id == "" {
		id = strings.TrimSpace(run.ScenarioName)
	}
	if id == "" {
		return "synthetic_" + state
	}
	return "synthetic_" + state + "_" + id
}
