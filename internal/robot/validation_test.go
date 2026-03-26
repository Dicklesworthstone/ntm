package robot

// validation_test.go contains integration tests for the validation harness.
// These tests verify that the fixture builders, fault injection, and assertion
// helpers work correctly and can be used for downstream validation tasks.
//
// Bead: bd-j9jo3.9.6

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

func TestFixedClock_Determinism(t *testing.T) {
	t.Parallel()

	clock1 := NewFixedClock(42)
	clock2 := NewFixedClock(42)

	if !clock1.Now().Equal(clock2.Now()) {
		t.Errorf("clocks with same seed should produce same time: %v != %v",
			clock1.Now(), clock2.Now())
	}

	clock1.Advance(time.Minute)
	clock2.Advance(time.Minute)

	if !clock1.Now().Equal(clock2.Now()) {
		t.Errorf("clocks should stay in sync after same advance: %v != %v",
			clock1.Now(), clock2.Now())
	}
}

func TestFixedClock_DifferentSeeds(t *testing.T) {
	t.Parallel()

	clock1 := NewFixedClock(1)
	clock2 := NewFixedClock(2)

	if clock1.Now().Equal(clock2.Now()) {
		t.Error("clocks with different seeds should produce different times")
	}
}

func TestCursorFixture_Monotonic(t *testing.T) {
	t.Parallel()

	cursor := NewCursorFixture(0)
	prev := cursor.Next()

	for i := 0; i < 100; i++ {
		curr := cursor.Next()
		if curr <= prev {
			t.Errorf("cursor not monotonic: %d <= %d", curr, prev)
		}
		prev = curr
	}
}

func TestCursorFixture_Determinism(t *testing.T) {
	t.Parallel()

	cursor1 := NewCursorFixture(42)
	cursor2 := NewCursorFixture(42)

	for i := 0; i < 10; i++ {
		if cursor1.Next() != cursor2.Next() {
			t.Error("cursors with same seed should produce same sequence")
		}
	}
}

func TestRuntimeSessionFixture_Valid(t *testing.T) {
	t.Parallel()

	opts := DefaultSessionFixtureOptions()
	session := RuntimeSessionFixture(opts)

	if session.Name != opts.Name {
		t.Errorf("session name: got %q, want %q", session.Name, opts.Name)
	}
	if session.AgentCount != opts.AgentCount {
		t.Errorf("agent count: got %d, want %d", session.AgentCount, opts.AgentCount)
	}
	if session.HealthStatus != opts.HealthStatus {
		t.Errorf("health status: got %q, want %q", session.HealthStatus, opts.HealthStatus)
	}
	// StaleAfter should be after CollectedAt (fresher than collection time)
	if !session.StaleAfter.After(session.CollectedAt) {
		t.Error("stale_after should be after collected_at")
	}
}

func TestRuntimeAgentFixture_Valid(t *testing.T) {
	t.Parallel()

	opts := DefaultAgentFixtureOptions()
	agent := RuntimeAgentFixture(opts)

	expectedID := "test-session:1"
	if agent.ID != expectedID {
		t.Errorf("agent ID: got %q, want %q", agent.ID, expectedID)
	}
	if agent.AgentType != opts.AgentType {
		t.Errorf("agent type: got %q, want %q", agent.AgentType, opts.AgentType)
	}
	if agent.State != opts.State {
		t.Errorf("agent state: got %q, want %q", agent.State, opts.State)
	}
}

func TestAttentionEventFixture_Valid(t *testing.T) {
	t.Parallel()

	opts := DefaultAttentionEventFixtureOptions()
	event := AttentionEventFixture(opts)

	if event.Category != opts.Category {
		t.Errorf("category: got %q, want %q", event.Category, opts.Category)
	}
	if event.Type != opts.Type {
		t.Errorf("type: got %q, want %q", event.Type, opts.Type)
	}
	if event.Cursor <= 0 {
		t.Error("cursor should be positive")
	}
	if event.Severity != opts.Severity {
		t.Errorf("severity: got %q, want %q", event.Severity, opts.Severity)
	}
	if event.Actionability != opts.Actionability {
		t.Errorf("actionability: got %q, want %q", event.Actionability, opts.Actionability)
	}
	if event.Summary != opts.Summary {
		t.Errorf("summary: got %q, want %q", event.Summary, opts.Summary)
	}
}

func TestIncidentFixture_Valid(t *testing.T) {
	t.Parallel()

	opts := DefaultIncidentFixtureOptions()
	incident := IncidentFixture(opts)

	if incident.ID != opts.ID {
		t.Errorf("incident ID: got %q, want %q", incident.ID, opts.ID)
	}
	if incident.Status != opts.Status {
		t.Errorf("status: got %q, want %q", incident.Status, opts.Status)
	}
	if incident.Severity != opts.Severity {
		t.Errorf("severity: got %q, want %q", incident.Severity, opts.Severity)
	}
	if incident.Title != opts.Title {
		t.Errorf("title: got %q, want %q", incident.Title, opts.Title)
	}
}

func TestSourceHealthFixture_DegradedSources(t *testing.T) {
	t.Parallel()

	opts := DefaultSourceHealthFixtureOptions()
	opts.TmuxStatus = state.SourceStatusUnavailable
	opts.BeadsStatus = state.SourceStatusStale

	fixture := NewSourceHealthFixture(opts)

	if len(fixture.DegradedSources) != 2 {
		t.Errorf("expected 2 degraded sources, got %d", len(fixture.DegradedSources))
	}

	AssertSourceHealthDegraded(t, fixture, []string{"tmux", "beads"})
}

func TestQuotaFixture_Exhausted(t *testing.T) {
	t.Parallel()

	opts := DefaultQuotaFixtureOptions()
	opts.Exhausted = true
	opts.RateLimitRemaining = 0

	fixture := NewQuotaFixture(opts)

	if !fixture.Exhausted {
		t.Error("expected exhausted=true")
	}
	if fixture.RateLimitRemaining != 0 {
		t.Errorf("expected rate_limit_remaining=0, got %d", fixture.RateLimitRemaining)
	}
}

func TestScenarioFixture_Complete(t *testing.T) {
	t.Parallel()

	scenario := NewScenarioFixture("TestScenarioFixture_Complete", 0)

	if string(scenario.ID) == "" {
		t.Error("scenario ID should not be empty")
	}
	if scenario.Session == nil {
		t.Error("session should not be nil")
	}
	if len(scenario.Agents) != 3 {
		t.Errorf("expected 3 agents, got %d", len(scenario.Agents))
	}
	if scenario.SourceHealth == nil {
		t.Error("source health should not be nil")
	}
	if scenario.Quota == nil {
		t.Error("quota should not be nil")
	}
}

func TestFaultInjector_DegradedSource(t *testing.T) {
	t.Parallel()

	injector := NewFaultInjector()
	injector.Enable(FaultDegradedSource, FaultConfig{
		Probability:     1.0,
		Count:           3,
		AffectedSources: []string{"beads", "mail"},
	})

	for i := 0; i < 3; i++ {
		if !injector.ShouldTrigger(FaultDegradedSource) {
			t.Errorf("iteration %d: expected fault to trigger", i)
		}
	}

	// Should stop after count is exhausted
	if injector.ShouldTrigger(FaultDegradedSource) {
		t.Error("fault should not trigger after count exhausted")
	}
}

func TestFaultInjector_DisableAll(t *testing.T) {
	t.Parallel()

	injector := NewFaultInjector()
	injector.Enable(FaultDegradedSource, FaultConfig{Probability: 1.0})
	injector.Enable(FaultTimeout, FaultConfig{Probability: 1.0})

	injector.DisableAll()

	if injector.ShouldTrigger(FaultDegradedSource) {
		t.Error("degraded source fault should be disabled")
	}
	if injector.ShouldTrigger(FaultTimeout) {
		t.Error("timeout fault should be disabled")
	}
}

func TestDuplicateRequestDetector(t *testing.T) {
	t.Parallel()

	detector := NewDuplicateRequestDetector()
	key := "test-key-123"

	if detector.IsDuplicate(key) {
		t.Error("new key should not be duplicate")
	}

	detector.Record(key, time.Now())

	if !detector.IsDuplicate(key) {
		t.Error("recorded key should be duplicate")
	}
}

func TestTestRecorder_Observations(t *testing.T) {
	t.Parallel()

	scenario := NewScenarioID("TestRecorder", 0)
	recorder := NewTestRecorder(t, scenario, false)

	recorder.RecordRequest("req-001", "idem-001")
	recorder.RecordCursor(100)
	recorder.RecordError("TIMEOUT", "retry with backoff")

	obs := recorder.Observations()
	if len(obs) != 3 {
		t.Errorf("expected 3 observations, got %d", len(obs))
	}

	if obs[0].RequestID != "req-001" {
		t.Errorf("observation 0 request ID: got %q, want %q", obs[0].RequestID, "req-001")
	}
	if obs[1].Cursor != 100 {
		t.Errorf("observation 1 cursor: got %d, want %d", obs[1].Cursor, 100)
	}
	if obs[2].ErrorCode != "TIMEOUT" {
		t.Errorf("observation 2 error code: got %q, want %q", obs[2].ErrorCode, "TIMEOUT")
	}
}

func TestAssertCursorMonotonic_Valid(t *testing.T) {
	t.Parallel()

	cursors := []int64{1, 2, 3, 5, 10, 100}
	AssertCursorMonotonic(t, cursors) // Should not fail
}

func TestDiffResponses_NoDifference(t *testing.T) {
	t.Parallel()

	json1 := []byte(`{"success": true, "count": 42}`)
	json2 := []byte(`{"success": true, "count": 42}`)

	diff, err := DiffResponses(json1, json2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff.HasDifferences() {
		t.Errorf("expected no differences, got: %s", diff.String())
	}
}

func TestDiffResponses_WithDifferences(t *testing.T) {
	t.Parallel()

	json1 := []byte(`{"success": true, "count": 42}`)
	json2 := []byte(`{"success": false, "count": 42, "extra": "field"}`)

	diff, err := DiffResponses(json1, json2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !diff.HasDifferences() {
		t.Error("expected differences")
	}
	if len(diff.Fields) < 2 {
		t.Errorf("expected at least 2 differences, got %d", len(diff.Fields))
	}
}

func TestBenchmarkRecorder_Time(t *testing.T) {
	t.Parallel()

	recorder := NewBenchmarkRecorder()

	duration := recorder.Time("test-op", func() {
		time.Sleep(time.Millisecond)
	})

	if duration < time.Millisecond {
		t.Errorf("duration should be at least 1ms, got %v", duration)
	}

	measurements := recorder.Measurements()
	if len(measurements) != 1 {
		t.Errorf("expected 1 measurement, got %d", len(measurements))
	}
	if measurements[0].Operation != "test-op" {
		t.Errorf("operation: got %q, want %q", measurements[0].Operation, "test-op")
	}
}

func TestValidateSchemaFields_Valid(t *testing.T) {
	t.Parallel()

	data := []byte(`{"success": true, "timestamp": "2026-01-01T00:00:00Z", "count": 42}`)
	required := []string{"success", "timestamp"}

	validation := ValidateSchemaFields(data, required)

	if !validation.Valid {
		t.Error("validation should pass")
	}
	if len(validation.MissingFields) != 0 {
		t.Errorf("expected no missing fields, got %v", validation.MissingFields)
	}
}

func TestValidateSchemaFields_Missing(t *testing.T) {
	t.Parallel()

	data := []byte(`{"success": true}`)
	required := []string{"success", "timestamp", "version"}

	validation := ValidateSchemaFields(data, required)

	if validation.Valid {
		t.Error("validation should fail")
	}
	if len(validation.MissingFields) != 2 {
		t.Errorf("expected 2 missing fields, got %v", validation.MissingFields)
	}
}

func TestApplyDegradedSources(t *testing.T) {
	t.Parallel()

	opts := DefaultSourceHealthFixtureOptions()
	fixture := NewSourceHealthFixture(opts)

	config := DegradedSourceConfig{
		BeadsDegraded: true,
		MailDegraded:  true,
	}
	ApplyDegradedSources(fixture, config)

	if fixture.BeadsStatus != state.SourceStatusStale {
		t.Errorf("beads should be stale, got %q", fixture.BeadsStatus)
	}
	if fixture.MailStatus != state.SourceStatusUnavailable {
		t.Errorf("mail should be unavailable, got %q", fixture.MailStatus)
	}
	if len(fixture.DegradedSources) != 2 {
		t.Errorf("expected 2 degraded sources, got %d", len(fixture.DegradedSources))
	}
}

func TestOperatorStateFixture_Acknowledged(t *testing.T) {
	t.Parallel()

	opts := DefaultOperatorStateFixtureOptions()
	opts.Acknowledged = true
	opts.AcknowledgedBy = "TestAgent"

	fixture := NewOperatorStateFixture(opts)

	if !fixture.Acknowledged {
		t.Error("expected acknowledged=true")
	}
	if fixture.AcknowledgedAt == nil {
		t.Error("expected acknowledged_at to be set")
	}
	if fixture.AcknowledgedBy != "TestAgent" {
		t.Errorf("acknowledged_by: got %q, want %q", fixture.AcknowledgedBy, "TestAgent")
	}
}

func TestOperatorStateFixture_Snoozed(t *testing.T) {
	t.Parallel()

	opts := DefaultOperatorStateFixtureOptions()
	opts.Snoozed = true
	opts.SnoozeUntil = 30 * time.Minute

	fixture := NewOperatorStateFixture(opts)

	if !fixture.Snoozed {
		t.Error("expected snoozed=true")
	}
	if fixture.SnoozedUntil == nil {
		t.Error("expected snoozed_until to be set")
	}
}

func TestOutcomeFixture_Success(t *testing.T) {
	t.Parallel()

	opts := DefaultOutcomeFixtureOptions()
	outcome := NewOutcomeFixture(opts)

	if !outcome.Success {
		t.Error("expected success=true")
	}
	if outcome.Command != "send" {
		t.Errorf("command: got %q, want %q", outcome.Command, "send")
	}
	if len(outcome.AffectedPanes) != 2 {
		t.Errorf("expected 2 affected panes, got %d", len(outcome.AffectedPanes))
	}
}

func TestOutcomeFixture_Error(t *testing.T) {
	t.Parallel()

	opts := DefaultOutcomeFixtureOptions()
	opts.Success = false
	opts.ErrorCode = "TIMEOUT"
	opts.ErrorMessage = "operation timed out"
	opts.RemediationHint = "retry with longer timeout"

	outcome := NewOutcomeFixture(opts)

	if outcome.Success {
		t.Error("expected success=false")
	}
	if outcome.ErrorCode != "TIMEOUT" {
		t.Errorf("error_code: got %q, want %q", outcome.ErrorCode, "TIMEOUT")
	}
	if outcome.RemediationHint != "retry with longer timeout" {
		t.Errorf("remediation_hint: got %q, want %q", outcome.RemediationHint, "retry with longer timeout")
	}
}

func TestScenarioID_Determinism(t *testing.T) {
	t.Parallel()

	id1 := NewScenarioID("TestName", 42)
	id2 := NewScenarioID("TestName", 42)

	if id1 != id2 {
		t.Errorf("scenario IDs with same inputs should match: %q != %q", id1, id2)
	}

	id3 := NewScenarioID("TestName", 43)
	if id1 == id3 {
		t.Error("scenario IDs with different seeds should differ")
	}
}

func TestCoordinationFixture_Valid(t *testing.T) {
	t.Parallel()

	opts := DefaultCoordinationFixtureOptions()
	opts.Conflicts = 2

	fixture := NewCoordinationFixture(opts)

	if fixture.ActiveAgents != opts.ActiveAgents {
		t.Errorf("active_agents: got %d, want %d", fixture.ActiveAgents, opts.ActiveAgents)
	}
	if fixture.ReservationConflicts != 2 {
		t.Errorf("conflicts: got %d, want 2", fixture.ReservationConflicts)
	}
}

func TestFixtureJSONSerialization(t *testing.T) {
	t.Parallel()

	scenario := NewScenarioFixture("TestJSON", 0)

	// Test that session can serialize
	data, err := json.Marshal(scenario.Session)
	if err != nil {
		t.Fatalf("failed to marshal session: %v", err)
	}

	AssertJSONContainsKey(t, data, "name")
	AssertJSONContainsKey(t, data, "health_status")
}

func TestRequestIdentity_Uniqueness(t *testing.T) {
	t.Parallel()

	clock := NewFixedClock(0)
	id1 := NewRequestIdentity(clock)

	clock.Advance(time.Nanosecond)
	id2 := NewRequestIdentity(clock)

	if id1.RequestID == id2.RequestID {
		t.Error("request IDs should be unique")
	}
	if id1.IdempotencyKey == id2.IdempotencyKey {
		t.Error("idempotency keys should be unique")
	}
}
