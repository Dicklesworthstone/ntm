package profiler

import (
	"testing"
	"time"
)

func TestDefaultThresholds(t *testing.T) {
	th := DefaultThresholds()

	if th.StartupWarningMs <= 0 {
		t.Error("expected positive startup warning threshold")
	}
	if th.StartupCriticalMs <= th.StartupWarningMs {
		t.Error("expected critical > warning threshold")
	}
	if th.SlowSpanMs <= 0 {
		t.Error("expected positive slow span threshold")
	}
}

func TestRecommendationsNoSpans(t *testing.T) {
	resetProfiler()
	Enable()
	defer disableProfiler()

	recs := GetRecommendations()

	// Should have info about no spans
	found := false
	for _, r := range recs {
		if r.Category == "instrumentation" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected instrumentation recommendation when no spans recorded")
	}
}

func TestRecommendationsGoodPerformance(t *testing.T) {
	resetProfiler()
	Enable()
	defer disableProfiler()

	// Use very permissive thresholds so fast spans don't trigger any warnings
	th := Thresholds{
		StartupWarningMs:  10000, // 10s is slow
		StartupCriticalMs: 20000, // 20s is critical
		SlowSpanMs:        1000,  // 1s span is slow
		VerySlowMs:        5000,  // 5s span is very slow
		PhasePercentWarn:  101,   // Never warn about phase balance
	}

	// Create some fast spans in different phases to avoid phase_balance warnings
	for i := 0; i < 3; i++ {
		s := StartWithPhase("fast-startup-op", "startup")
		time.Sleep(1 * time.Millisecond)
		s.End()
	}
	for i := 0; i < 2; i++ {
		s := StartWithPhase("fast-command-op", "command")
		time.Sleep(1 * time.Millisecond)
		s.End()
	}

	recs := GetRecommendationsWithThresholds(th)

	// Should have only positive feedback (no warnings/critical)
	for _, r := range recs {
		if r.Severity == SeverityWarning || r.Severity == SeverityCritical {
			t.Errorf("unexpected %s recommendation: %s", r.Severity, r.Message)
		}
	}

	// Should have at least one info-level recommendation
	foundInfo := false
	for _, r := range recs {
		if r.Severity == SeverityInfo {
			foundInfo = true
			break
		}
	}
	if !foundInfo {
		t.Error("expected at least one info-level recommendation")
	}
}

func TestRecommendationsSlowStartup(t *testing.T) {
	resetProfiler()
	Enable()
	defer disableProfiler()

	// Custom thresholds for testing
	th := Thresholds{
		StartupWarningMs:  10, // 10ms is slow
		StartupCriticalMs: 50, // 50ms is critical
		SlowSpanMs:        5,
		VerySlowMs:        20,
		PhasePercentWarn:  50,
	}

	// Create slow startup
	s := StartWithPhase("slow-init", "startup")
	time.Sleep(60 * time.Millisecond)
	s.End()

	recs := GetRecommendationsWithThresholds(th)

	foundCritical := false
	for _, r := range recs {
		if r.Category == "startup" && r.Severity == SeverityCritical {
			foundCritical = true
			break
		}
	}
	if !foundCritical {
		t.Error("expected critical startup recommendation for very slow startup")
	}
}

func TestRecommendationsSlowSpan(t *testing.T) {
	resetProfiler()
	Enable()
	defer disableProfiler()

	th := Thresholds{
		StartupWarningMs:  1000,
		StartupCriticalMs: 2000,
		SlowSpanMs:        5,  // 5ms is slow
		VerySlowMs:        20, // 20ms is very slow
		PhasePercentWarn:  80,
	}

	// Create a slow span
	s := Start("slow-operation")
	time.Sleep(25 * time.Millisecond)
	s.End()

	recs := GetRecommendationsWithThresholds(th)

	foundSlowSpan := false
	for _, r := range recs {
		if r.Category == "slow_span" && r.SpanName == "slow-operation" {
			foundSlowSpan = true
			if r.Severity != SeverityCritical {
				t.Errorf("expected critical severity for very slow span, got %s", r.Severity)
			}
			break
		}
	}
	if !foundSlowSpan {
		t.Error("expected slow_span recommendation")
	}
}

func TestSeverityConstants(t *testing.T) {
	// Ensure severity constants are defined correctly
	if SeverityInfo != "info" {
		t.Errorf("expected SeverityInfo='info', got %q", SeverityInfo)
	}
	if SeverityWarning != "warning" {
		t.Errorf("expected SeverityWarning='warning', got %q", SeverityWarning)
	}
	if SeverityCritical != "critical" {
		t.Errorf("expected SeverityCritical='critical', got %q", SeverityCritical)
	}
}

func TestPhaseBalanceRecommendation(t *testing.T) {
	resetProfiler()
	Enable()
	defer disableProfiler()

	th := Thresholds{
		StartupWarningMs:  1000,
		StartupCriticalMs: 2000,
		SlowSpanMs:        100,
		VerySlowMs:        500,
		PhasePercentWarn:  30, // 30% threshold
	}

	// Create spans heavily weighted to one phase
	for i := 0; i < 10; i++ {
		s := StartWithPhase("heavy-op", "startup")
		time.Sleep(5 * time.Millisecond)
		s.End()
	}

	// Add a tiny amount to another phase
	s := StartWithPhase("light-op", "command")
	time.Sleep(1 * time.Millisecond)
	s.End()

	recs := GetRecommendationsWithThresholds(th)

	foundBalance := false
	for _, r := range recs {
		if r.Category == "phase_balance" {
			foundBalance = true
			break
		}
	}
	if !foundBalance {
		t.Error("expected phase_balance recommendation for unbalanced phases")
	}
}
