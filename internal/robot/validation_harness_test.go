package robot

// validation_harness_test.go provides fault-injection helpers, assertion utilities,
// and measurement/logging infrastructure for robot surface validation. This harness
// makes tests deterministic, debuggable, and benchmarkable.
//
// Bead: bd-j9jo3.9.6

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

// =============================================================================
// Test Observation Recording
// =============================================================================

// TestObservation records a single observation during test execution.
type TestObservation struct {
	Timestamp         time.Time              `json:"timestamp"`
	ScenarioID        ScenarioID             `json:"scenario_id"`
	SchemaID          string                 `json:"schema_id,omitempty"`
	RequestID         string                 `json:"request_id,omitempty"`
	IdempotencyKey    string                 `json:"idempotency_key,omitempty"`
	Cursor            int64                  `json:"cursor,omitempty"`
	SourceHealthDelta []string               `json:"source_health_delta,omitempty"`
	DegradedFeatures  []string               `json:"degraded_features,omitempty"`
	IncidentID        string                 `json:"incident_id,omitempty"`
	OperatorTransition string                `json:"operator_transition,omitempty"`
	SuppressionMarker string                 `json:"suppression_marker,omitempty"`
	ResurfacingDecision string               `json:"resurfacing_decision,omitempty"`
	PayloadSize       int                    `json:"payload_size,omitempty"`
	ErrorCode         string                 `json:"error_code,omitempty"`
	RemediationHint   string                 `json:"remediation_hint,omitempty"`
	DiagnosticHandle  string                 `json:"diagnostic_handle,omitempty"`
	EvidenceSummary   string                 `json:"evidence_summary,omitempty"`
	DurationMs        int64                  `json:"duration_ms,omitempty"`
	Extra             map[string]interface{} `json:"extra,omitempty"`
}

// TestRecorder accumulates observations for a test scenario.
type TestRecorder struct {
	t            *testing.T
	scenarioID   ScenarioID
	observations []TestObservation
	verbose      bool
}

// NewTestRecorder creates a recorder for a test scenario.
func NewTestRecorder(t *testing.T, scenarioID ScenarioID, verbose bool) *TestRecorder {
	return &TestRecorder{
		t:            t,
		scenarioID:   scenarioID,
		observations: make([]TestObservation, 0),
		verbose:      verbose,
	}
}

// Record adds an observation to the recorder.
func (r *TestRecorder) Record(obs TestObservation) {
	obs.Timestamp = time.Now()
	obs.ScenarioID = r.scenarioID
	r.observations = append(r.observations, obs)

	if r.verbose {
		data, _ := json.Marshal(obs)
		r.t.Logf("OBSERVATION: %s", string(data))
	}
}

// RecordRequest records a request observation.
func (r *TestRecorder) RecordRequest(requestID, idempotencyKey string) {
	r.Record(TestObservation{
		RequestID:      requestID,
		IdempotencyKey: idempotencyKey,
	})
}

// RecordCursor records a cursor observation.
func (r *TestRecorder) RecordCursor(cursor int64) {
	r.Record(TestObservation{Cursor: cursor})
}

// RecordSourceHealthChange records a source health delta.
func (r *TestRecorder) RecordSourceHealthChange(degraded, recovered []string) {
	r.Record(TestObservation{
		SourceHealthDelta: append(degraded, recovered...),
		DegradedFeatures:  degraded,
	})
}

// RecordIncident records an incident observation.
func (r *TestRecorder) RecordIncident(incidentID string) {
	r.Record(TestObservation{IncidentID: incidentID})
}

// RecordOperatorTransition records an operator state transition.
func (r *TestRecorder) RecordOperatorTransition(transition string) {
	r.Record(TestObservation{OperatorTransition: transition})
}

// RecordSuppression records a suppression decision.
func (r *TestRecorder) RecordSuppression(marker string) {
	r.Record(TestObservation{SuppressionMarker: marker})
}

// RecordResurfacing records a resurfacing decision.
func (r *TestRecorder) RecordResurfacing(decision string) {
	r.Record(TestObservation{ResurfacingDecision: decision})
}

// RecordPayloadSize records the size of a response payload.
func (r *TestRecorder) RecordPayloadSize(size int) {
	r.Record(TestObservation{PayloadSize: size})
}

// RecordError records an error observation.
func (r *TestRecorder) RecordError(code, hint string) {
	r.Record(TestObservation{ErrorCode: code, RemediationHint: hint})
}

// RecordDiagnostic records diagnostic information.
func (r *TestRecorder) RecordDiagnostic(handle, evidence string) {
	r.Record(TestObservation{DiagnosticHandle: handle, EvidenceSummary: evidence})
}

// RecordDuration records operation duration.
func (r *TestRecorder) RecordDuration(ms int64) {
	r.Record(TestObservation{DurationMs: ms})
}

// Observations returns all recorded observations.
func (r *TestRecorder) Observations() []TestObservation {
	return r.observations
}

// Summary returns a summary of all observations.
func (r *TestRecorder) Summary() string {
	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("Scenario: %s\n", r.scenarioID))
	buf.WriteString(fmt.Sprintf("Observations: %d\n", len(r.observations)))

	for i, obs := range r.observations {
		buf.WriteString(fmt.Sprintf("  [%d] ", i))
		if obs.RequestID != "" {
			buf.WriteString(fmt.Sprintf("request=%s ", obs.RequestID))
		}
		if obs.Cursor != 0 {
			buf.WriteString(fmt.Sprintf("cursor=%d ", obs.Cursor))
		}
		if obs.ErrorCode != "" {
			buf.WriteString(fmt.Sprintf("error=%s ", obs.ErrorCode))
		}
		if obs.PayloadSize > 0 {
			buf.WriteString(fmt.Sprintf("payload=%d ", obs.PayloadSize))
		}
		if obs.DurationMs > 0 {
			buf.WriteString(fmt.Sprintf("duration=%dms ", obs.DurationMs))
		}
		buf.WriteString("\n")
	}

	return buf.String()
}

// =============================================================================
// Fault Injection Helpers
// =============================================================================

// FaultType identifies the type of fault to inject.
type FaultType string

const (
	// FaultDegradedSource simulates a degraded data source.
	FaultDegradedSource FaultType = "degraded_source"

	// FaultDuplicateRequest simulates a duplicate request.
	FaultDuplicateRequest FaultType = "duplicate_request"

	// FaultSafeRetry simulates a safe retry scenario.
	FaultSafeRetry FaultType = "safe_retry"

	// FaultPartialFailure simulates a partial failure scenario.
	FaultPartialFailure FaultType = "partial_failure"

	// FaultTimeout simulates a timeout.
	FaultTimeout FaultType = "timeout"

	// FaultStaleData simulates stale data.
	FaultStaleData FaultType = "stale_data"

	// FaultCursorExpired simulates an expired cursor.
	FaultCursorExpired FaultType = "cursor_expired"
)

// FaultInjector provides controlled fault injection for tests.
type FaultInjector struct {
	activeFaults map[FaultType]FaultConfig
	triggerCount map[FaultType]int
}

// FaultConfig configures fault injection behavior.
type FaultConfig struct {
	// Enabled indicates whether the fault is active.
	Enabled bool `json:"enabled"`

	// Probability is the chance of triggering (0.0 to 1.0).
	// Set to 1.0 for deterministic tests.
	Probability float64 `json:"probability"`

	// Count is the number of times to trigger (0 = unlimited).
	Count int `json:"count"`

	// AffectedSources specifies which sources are affected.
	AffectedSources []string `json:"affected_sources,omitempty"`

	// ErrorCode is the error code to return.
	ErrorCode string `json:"error_code,omitempty"`

	// Delay is the simulated delay duration.
	Delay time.Duration `json:"delay,omitempty"`

	// FailAfter triggers failure after N successful operations.
	FailAfter int `json:"fail_after,omitempty"`
}

// NewFaultInjector creates a new fault injector.
func NewFaultInjector() *FaultInjector {
	return &FaultInjector{
		activeFaults: make(map[FaultType]FaultConfig),
		triggerCount: make(map[FaultType]int),
	}
}

// Enable activates a fault type.
func (f *FaultInjector) Enable(faultType FaultType, config FaultConfig) {
	config.Enabled = true
	f.activeFaults[faultType] = config
	f.triggerCount[faultType] = 0
}

// Disable deactivates a fault type.
func (f *FaultInjector) Disable(faultType FaultType) {
	if cfg, ok := f.activeFaults[faultType]; ok {
		cfg.Enabled = false
		f.activeFaults[faultType] = cfg
	}
}

// DisableAll deactivates all faults.
func (f *FaultInjector) DisableAll() {
	for ft := range f.activeFaults {
		f.Disable(ft)
	}
}

// Reset clears all fault configurations and counters.
func (f *FaultInjector) Reset() {
	f.activeFaults = make(map[FaultType]FaultConfig)
	f.triggerCount = make(map[FaultType]int)
}

// ShouldTrigger checks if a fault should be triggered.
// For deterministic tests, use Probability=1.0.
func (f *FaultInjector) ShouldTrigger(faultType FaultType) bool {
	cfg, ok := f.activeFaults[faultType]
	if !ok || !cfg.Enabled {
		return false
	}

	// Check count limit
	if cfg.Count > 0 && f.triggerCount[faultType] >= cfg.Count {
		return false
	}

	// For deterministic tests, always trigger if probability is 1.0
	if cfg.Probability >= 1.0 {
		f.triggerCount[faultType]++
		return true
	}

	return false
}

// TriggerCount returns how many times a fault has triggered.
func (f *FaultInjector) TriggerCount(faultType FaultType) int {
	return f.triggerCount[faultType]
}

// GetConfig returns the configuration for a fault type.
func (f *FaultInjector) GetConfig(faultType FaultType) (FaultConfig, bool) {
	cfg, ok := f.activeFaults[faultType]
	return cfg, ok
}

// =============================================================================
// Degraded Source Injection
// =============================================================================

// DegradedSourceConfig configures degraded source simulation.
type DegradedSourceConfig struct {
	TmuxDegraded  bool
	BeadsDegraded bool
	MailDegraded  bool
	PtDegraded    bool
}

// ApplyDegradedSources modifies a source health fixture for degraded sources.
func ApplyDegradedSources(fixture *SourceHealthFixture, config DegradedSourceConfig) {
	degraded := make([]string, 0)

	if config.TmuxDegraded {
		fixture.TmuxStatus = state.SourceStatusUnavailable
		degraded = append(degraded, "tmux")
	}
	if config.BeadsDegraded {
		fixture.BeadsStatus = state.SourceStatusStale
		degraded = append(degraded, "beads")
	}
	if config.MailDegraded {
		fixture.MailStatus = state.SourceStatusUnavailable
		degraded = append(degraded, "mail")
	}
	if config.PtDegraded {
		fixture.PtStatus = state.SourceStatusStale
		degraded = append(degraded, "pt")
	}

	fixture.DegradedSources = degraded
}

// =============================================================================
// Request Identity Helpers
// =============================================================================

// RequestIdentity tracks request identity for idempotency testing.
type RequestIdentity struct {
	RequestID      string
	IdempotencyKey string
	CorrelationID  string
	Timestamp      time.Time
}

// NewRequestIdentity creates a new request identity.
func NewRequestIdentity(clock *FixedClock) *RequestIdentity {
	ts := clock.Now()
	return &RequestIdentity{
		RequestID:      fmt.Sprintf("req-%d", ts.UnixNano()),
		IdempotencyKey: fmt.Sprintf("idem-%d", ts.UnixNano()),
		CorrelationID:  fmt.Sprintf("corr-%d", ts.UnixNano()),
		Timestamp:      ts,
	}
}

// DuplicateRequestDetector tracks seen requests for duplicate detection.
type DuplicateRequestDetector struct {
	seen map[string]time.Time
}

// NewDuplicateRequestDetector creates a new detector.
func NewDuplicateRequestDetector() *DuplicateRequestDetector {
	return &DuplicateRequestDetector{
		seen: make(map[string]time.Time),
	}
}

// IsDuplicate checks if a request has been seen before.
func (d *DuplicateRequestDetector) IsDuplicate(idempotencyKey string) bool {
	_, exists := d.seen[idempotencyKey]
	return exists
}

// Record records that a request was processed.
func (d *DuplicateRequestDetector) Record(idempotencyKey string, ts time.Time) {
	d.seen[idempotencyKey] = ts
}

// =============================================================================
// Assertion Helpers
// =============================================================================

// AssertRobotResponseSuccess asserts that a RobotResponse indicates success.
func AssertRobotResponseSuccess(t *testing.T, resp RobotResponse) {
	t.Helper()
	if !resp.Success {
		t.Errorf("expected success=true, got false; error=%q code=%q", resp.Error, resp.ErrorCode)
	}
}

// AssertRobotResponseError asserts that a RobotResponse indicates an error with the expected code.
func AssertRobotResponseError(t *testing.T, resp RobotResponse, expectedCode string) {
	t.Helper()
	if resp.Success {
		t.Error("expected success=false, got true")
	}
	if resp.ErrorCode != expectedCode {
		t.Errorf("expected error_code=%q, got %q", expectedCode, resp.ErrorCode)
	}
}

// AssertCursorMonotonic asserts that a sequence of cursors is strictly increasing.
func AssertCursorMonotonic(t *testing.T, cursors []int64) {
	t.Helper()
	for i := 1; i < len(cursors); i++ {
		if cursors[i] <= cursors[i-1] {
			t.Errorf("cursor sequence not monotonic: cursor[%d]=%d <= cursor[%d]=%d",
				i, cursors[i], i-1, cursors[i-1])
		}
	}
}

// AssertJSONContainsKey asserts that a JSON object contains the expected key.
func AssertJSONContainsKey(t *testing.T, data []byte, key string) {
	t.Helper()
	var obj map[string]interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if _, ok := obj[key]; !ok {
		t.Errorf("JSON missing expected key %q", key)
	}
}

// AssertJSONFieldEquals asserts that a JSON field has the expected value.
func AssertJSONFieldEquals(t *testing.T, data []byte, key string, expected interface{}) {
	t.Helper()
	var obj map[string]interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	actual, ok := obj[key]
	if !ok {
		t.Errorf("JSON missing expected key %q", key)
		return
	}
	if actual != expected {
		t.Errorf("JSON field %q: got %v, want %v", key, actual, expected)
	}
}

// AssertSourceHealthDegraded asserts that the specified sources are degraded.
func AssertSourceHealthDegraded(t *testing.T, fixture *SourceHealthFixture, sources []string) {
	t.Helper()
	for _, src := range sources {
		found := false
		for _, degraded := range fixture.DegradedSources {
			if degraded == src {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected source %q to be degraded, but it was not", src)
		}
	}
}

// AssertPayloadWithinBudget asserts that a payload is within the size budget.
func AssertPayloadWithinBudget(t *testing.T, data []byte, budget int) {
	t.Helper()
	if len(data) > budget {
		t.Errorf("payload size %d exceeds budget %d", len(data), budget)
	}
}

// =============================================================================
// Response Diff Helpers
// =============================================================================

// ResponseDiff represents differences between two responses.
type ResponseDiff struct {
	Fields []FieldDiff
}

// FieldDiff represents a difference in a single field.
type FieldDiff struct {
	Path     string
	Expected interface{}
	Actual   interface{}
}

// DiffResponses compares two JSON responses and returns differences.
func DiffResponses(expected, actual []byte) (*ResponseDiff, error) {
	var exp, act map[string]interface{}
	if err := json.Unmarshal(expected, &exp); err != nil {
		return nil, fmt.Errorf("unmarshal expected: %w", err)
	}
	if err := json.Unmarshal(actual, &act); err != nil {
		return nil, fmt.Errorf("unmarshal actual: %w", err)
	}

	diff := &ResponseDiff{Fields: make([]FieldDiff, 0)}
	diffMaps("", exp, act, diff)
	return diff, nil
}

func diffMaps(prefix string, expected, actual map[string]interface{}, diff *ResponseDiff) {
	for k, expVal := range expected {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}

		actVal, ok := actual[k]
		if !ok {
			diff.Fields = append(diff.Fields, FieldDiff{
				Path:     path,
				Expected: expVal,
				Actual:   nil,
			})
			continue
		}

		// Recurse into nested objects
		expMap, expIsMap := expVal.(map[string]interface{})
		actMap, actIsMap := actVal.(map[string]interface{})
		if expIsMap && actIsMap {
			diffMaps(path, expMap, actMap, diff)
		} else if expVal != actVal {
			// Simple value comparison (works for strings, numbers, bools)
			// Note: This doesn't handle slices properly for brevity
			expJSON, _ := json.Marshal(expVal)
			actJSON, _ := json.Marshal(actVal)
			if !bytes.Equal(expJSON, actJSON) {
				diff.Fields = append(diff.Fields, FieldDiff{
					Path:     path,
					Expected: expVal,
					Actual:   actVal,
				})
			}
		}
	}

	// Check for extra fields in actual
	for k := range actual {
		if _, ok := expected[k]; !ok {
			path := k
			if prefix != "" {
				path = prefix + "." + k
			}
			diff.Fields = append(diff.Fields, FieldDiff{
				Path:     path,
				Expected: nil,
				Actual:   actual[k],
			})
		}
	}
}

// HasDifferences returns true if there are any differences.
func (d *ResponseDiff) HasDifferences() bool {
	return len(d.Fields) > 0
}

// String returns a human-readable diff summary.
func (d *ResponseDiff) String() string {
	if !d.HasDifferences() {
		return "no differences"
	}

	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("%d differences:\n", len(d.Fields)))
	for _, f := range d.Fields {
		if f.Expected == nil {
			buf.WriteString(fmt.Sprintf("  + %s: %v (unexpected)\n", f.Path, f.Actual))
		} else if f.Actual == nil {
			buf.WriteString(fmt.Sprintf("  - %s: %v (missing)\n", f.Path, f.Expected))
		} else {
			buf.WriteString(fmt.Sprintf("  ~ %s: %v -> %v\n", f.Path, f.Expected, f.Actual))
		}
	}
	return buf.String()
}

// =============================================================================
// Benchmark Measurement Helpers
// =============================================================================

// BenchmarkMeasurement records performance measurements.
type BenchmarkMeasurement struct {
	Operation    string        `json:"operation"`
	DurationNs   int64         `json:"duration_ns"`
	PayloadBytes int           `json:"payload_bytes,omitempty"`
	ItemCount    int           `json:"item_count,omitempty"`
	Cursor       int64         `json:"cursor,omitempty"`
	ScenarioID   ScenarioID    `json:"scenario_id,omitempty"`
}

// BenchmarkRecorder accumulates benchmark measurements.
type BenchmarkRecorder struct {
	measurements []BenchmarkMeasurement
}

// NewBenchmarkRecorder creates a new benchmark recorder.
func NewBenchmarkRecorder() *BenchmarkRecorder {
	return &BenchmarkRecorder{
		measurements: make([]BenchmarkMeasurement, 0),
	}
}

// Record adds a measurement.
func (b *BenchmarkRecorder) Record(m BenchmarkMeasurement) {
	b.measurements = append(b.measurements, m)
}

// Time records the duration of an operation.
func (b *BenchmarkRecorder) Time(operation string, fn func()) time.Duration {
	start := time.Now()
	fn()
	duration := time.Since(start)
	b.Record(BenchmarkMeasurement{
		Operation:  operation,
		DurationNs: duration.Nanoseconds(),
	})
	return duration
}

// Measurements returns all recorded measurements.
func (b *BenchmarkRecorder) Measurements() []BenchmarkMeasurement {
	return b.measurements
}

// Summary returns a summary of measurements.
func (b *BenchmarkRecorder) Summary() string {
	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("Benchmark: %d measurements\n", len(b.measurements)))

	for _, m := range b.measurements {
		buf.WriteString(fmt.Sprintf("  %s: %dns", m.Operation, m.DurationNs))
		if m.PayloadBytes > 0 {
			buf.WriteString(fmt.Sprintf(" (%d bytes)", m.PayloadBytes))
		}
		if m.ItemCount > 0 {
			buf.WriteString(fmt.Sprintf(" (%d items)", m.ItemCount))
		}
		buf.WriteString("\n")
	}

	return buf.String()
}

// =============================================================================
// Schema Validation Helpers
// =============================================================================

// SchemaValidation holds the result of schema validation.
type SchemaValidation struct {
	SchemaID      string
	Valid         bool
	MissingFields []string
	ExtraFields   []string
	TypeErrors    []string
}

// ValidateSchemaFields checks that required fields are present.
func ValidateSchemaFields(data []byte, requiredFields []string) *SchemaValidation {
	validation := &SchemaValidation{
		Valid:         true,
		MissingFields: make([]string, 0),
		ExtraFields:   make([]string, 0),
		TypeErrors:    make([]string, 0),
	}

	var obj map[string]interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		validation.Valid = false
		validation.TypeErrors = append(validation.TypeErrors, fmt.Sprintf("invalid JSON: %v", err))
		return validation
	}

	for _, field := range requiredFields {
		if _, ok := obj[field]; !ok {
			validation.Valid = false
			validation.MissingFields = append(validation.MissingFields, field)
		}
	}

	return validation
}
