package profiler

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// resetProfiler clears all collected spans (test-only helper; the public
// Reset API was deleted as dead code, bd-ws2-wire-or-delete-ykmcz.9).
func resetProfiler() {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.spans = nil
	global.start = time.Now()
}

// disableProfiler turns off profiling (test-only helper).
func disableProfiler() {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.enabled = false
}

// recordedSpans returns snapshots of all recorded spans (test-only helper).
func recordedSpans() []*Span {
	global.mu.RLock()
	defer global.mu.RUnlock()
	result := make([]*Span, len(global.spans))
	for i, span := range global.spans {
		result[i] = cloneSpanSnapshot(span)
	}
	return result
}

// TestGetSpansByPhaseReturnsSnapshots verifies GetSpansByPhase returns clones,
// not live span pointers.
func TestGetSpansByPhaseReturnsSnapshots(t *testing.T) {
	resetProfiler()
	Enable()
	defer disableProfiler()

	span := StartWithPhase("snapshot-op", "startup")
	span.Tag("session", "live")
	span.End()

	phaseSpans := GetSpansByPhase("startup")
	if len(phaseSpans) != 1 {
		t.Fatalf("expected 1 startup span, got %d", len(phaseSpans))
	}
	phaseSpans[0].Tags["session"] = "phase-mutated"
	if again := GetSpansByPhase("startup"); again[0].Tags["session"] != "live" {
		t.Error("GetSpansByPhase returned live tag map")
	}
}

func TestEnableDisable(t *testing.T) {
	// Start disabled
	disableProfiler()
	resetProfiler()

	if IsEnabled() {
		t.Error("expected profiler to be disabled initially")
	}

	Enable()
	if !IsEnabled() {
		t.Error("expected profiler to be enabled after Enable()")
	}

	disableProfiler()
	if IsEnabled() {
		t.Error("expected profiler to be disabled after disableProfiler")
	}
}

func TestSpanCreation(t *testing.T) {
	resetProfiler()
	Enable()
	defer disableProfiler()

	span := Start("test-operation")
	if span == nil {
		t.Fatal("expected non-nil span")
	}
	if span.Name != "test-operation" {
		t.Errorf("expected name 'test-operation', got %q", span.Name)
	}

	time.Sleep(10 * time.Millisecond)
	span.End()

	if span.Duration < 10*time.Millisecond {
		t.Errorf("expected duration >= 10ms, got %v", span.Duration)
	}
}

func TestSpanWithPhase(t *testing.T) {
	resetProfiler()
	Enable()
	defer disableProfiler()

	span := StartWithPhase("init-config", "startup")
	span.End()

	if span.Phase != "startup" {
		t.Errorf("expected phase 'startup', got %q", span.Phase)
	}

	startupSpans := GetSpansByPhase("startup")
	if len(startupSpans) != 1 {
		t.Errorf("expected 1 startup span, got %d", len(startupSpans))
	}
}

func TestSpanTags(t *testing.T) {
	resetProfiler()
	Enable()
	defer disableProfiler()

	span := Start("tagged-op")
	span.Tag("session", "myproject")
	span.Tag("count", 42)
	span.End()

	if span.Tags["session"] != "myproject" {
		t.Errorf("expected tag session='myproject', got %v", span.Tags["session"])
	}
	if span.Tags["count"] != 42 {
		t.Errorf("expected tag count=42, got %v", span.Tags["count"])
	}
}

func TestDisabledProfilingNoOps(t *testing.T) {
	resetProfiler()
	disableProfiler()

	// Should not panic and return no-op spans
	span := Start("should-not-record")
	span.Tag("key", "value")
	span.End()

	spans := recordedSpans()
	if len(spans) != 0 {
		t.Errorf("expected no spans when disabled, got %d", len(spans))
	}
}

func TestGetProfile(t *testing.T) {
	resetProfiler()
	Enable()
	defer disableProfiler()

	// Create some spans
	s1 := StartWithPhase("op1", "startup")
	time.Sleep(5 * time.Millisecond)
	s1.End()

	s2 := StartWithPhase("op2", "startup")
	time.Sleep(5 * time.Millisecond)
	s2.End()

	s3 := StartWithPhase("op3", "command")
	time.Sleep(5 * time.Millisecond)
	s3.End()

	profile := GetProfile()

	if profile.SpanCount != 3 {
		t.Errorf("expected 3 spans, got %d", profile.SpanCount)
	}

	if len(profile.Phases) < 2 {
		t.Errorf("expected at least 2 phases, got %d", len(profile.Phases))
	}

	// Find startup phase
	var startupPhase *PhaseReport
	for i := range profile.Phases {
		if profile.Phases[i].Phase == "startup" {
			startupPhase = &profile.Phases[i]
			break
		}
	}
	if startupPhase == nil {
		t.Fatal("expected to find startup phase")
	}
	if startupPhase.SpanCount != 2 {
		t.Errorf("expected 2 startup spans, got %d", startupPhase.SpanCount)
	}
}

func TestGetProfileReturnsSnapshotTags(t *testing.T) {
	resetProfiler()
	Enable()
	defer disableProfiler()

	span := Start("profile-snapshot")
	span.Tag("session", "live")
	span.End()

	profile := GetProfile()
	if len(profile.Spans) != 1 {
		t.Fatalf("expected 1 profile span, got %d", len(profile.Spans))
	}
	profile.Spans[0].Tags["session"] = "mutated"

	again := GetProfile()
	if again.Spans[0].Tags["session"] != "live" {
		t.Errorf("GetProfile returned live tag map; session changed to %v", again.Spans[0].Tags["session"])
	}
}

func TestWriteJSON(t *testing.T) {
	resetProfiler()
	Enable()
	defer disableProfiler()

	span := Start("json-test")
	span.Tag("test", true)
	span.End()

	var buf bytes.Buffer
	if err := WriteJSON(&buf); err != nil {
		t.Fatalf("WriteJSON failed: %v", err)
	}

	// Verify it's valid JSON
	var profile Profile
	if err := json.Unmarshal(buf.Bytes(), &profile); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}

	if profile.SpanCount != 1 {
		t.Errorf("expected span_count=1, got %d", profile.SpanCount)
	}
}

func TestWriteText(t *testing.T) {
	resetProfiler()
	Enable()
	defer disableProfiler()

	s := StartWithPhase("text-test", "startup")
	time.Sleep(5 * time.Millisecond)
	s.End()

	var buf bytes.Buffer
	if err := WriteText(&buf); err != nil {
		t.Fatalf("WriteText failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "Performance Profile") {
		t.Error("expected output to contain 'Performance Profile'")
	}
	if !strings.Contains(output, "text-test") {
		t.Error("expected output to contain span name 'text-test'")
	}
}

func TestDoubleEnd(t *testing.T) {
	resetProfiler()
	Enable()
	defer disableProfiler()

	span := Start("double-end")
	time.Sleep(5 * time.Millisecond)
	span.End()

	firstDuration := span.Duration

	time.Sleep(10 * time.Millisecond)
	span.End() // Should be no-op

	if span.Duration != firstDuration {
		t.Error("expected duration to not change after second End()")
	}
}

func TestWriteJSONIncludesRecommendations(t *testing.T) {
	resetProfiler()
	Enable()
	defer disableProfiler()

	// Create some spans to generate recommendations
	span := Start("json-rec-test")
	span.End()

	var buf bytes.Buffer
	if err := WriteJSON(&buf); err != nil {
		t.Fatalf("WriteJSON failed: %v", err)
	}

	// Verify JSON contains recommendations field
	var profile Profile
	if err := json.Unmarshal(buf.Bytes(), &profile); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}

	// Should have at least the "performance looks good" recommendation
	if len(profile.Recommendations) == 0 {
		t.Error("expected recommendations in JSON output")
	}

	// Verify recommendation structure
	rec := profile.Recommendations[0]
	if rec.Category == "" {
		t.Error("expected recommendation to have category")
	}
	if rec.Message == "" {
		t.Error("expected recommendation to have message")
	}
	if rec.Suggestion == "" {
		t.Error("expected recommendation to have suggestion")
	}
}

func TestWriteTextIncludesRecommendations(t *testing.T) {
	resetProfiler()
	Enable()
	defer disableProfiler()

	s := StartWithPhase("text-rec-test", "startup")
	s.End()

	var buf bytes.Buffer
	if err := WriteText(&buf); err != nil {
		t.Fatalf("WriteText failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "Recommendations:") {
		t.Error("expected output to contain 'Recommendations:'")
	}
	// Should contain a suggestion (marked with →)
	if !strings.Contains(output, "→") {
		t.Error("expected output to contain recommendation suggestions")
	}
}

func TestWriteTextRecommendationSeverityIcons(t *testing.T) {
	resetProfiler()
	Enable()
	defer disableProfiler()

	// Create slow spans to trigger warnings
	span := StartWithPhase("very-slow-op", "startup")
	time.Sleep(150 * time.Millisecond) // Slow enough to trigger warning (>100ms)
	span.End()

	var buf bytes.Buffer
	if err := WriteText(&buf); err != nil {
		t.Fatalf("WriteText failed: %v", err)
	}

	output := buf.String()
	// Should contain warning icon for slow span
	if !strings.Contains(output, "⚠") && !strings.Contains(output, "❌") {
		t.Error("expected output to contain warning or critical icons for slow operation")
	}
}
