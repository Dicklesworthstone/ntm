package evidencebudget

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/faultharness"
)

func newClock() *faultharness.FakeClock {
	return &faultharness.FakeClock{NowTime: time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)}
}

func healthy() faultharness.Behavior {
	return faultharness.Behavior{Mode: faultharness.ModeHealthy}
}

// All-healthy baseline must finish well inside the budget with no
// warnings and a Healthy classification for every provider.
func TestRobotSnapshot_HealthyBaseline(t *testing.T) {
	t.Parallel()
	clock := newClock()
	ctx := context.Background()
	eval := Evaluate(ctx, clock, RobotSnapshotSurface(ctx, clock, healthy(), healthy()))

	if eval.Err != nil {
		t.Fatalf("Err = %v", eval.Err)
	}
	if eval.OverBudget {
		t.Errorf("OverBudget = true on healthy baseline (elapsed=%s, budget=5s)", eval.Elapsed)
	}
	if len(eval.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none on healthy baseline", eval.Warnings)
	}
	for name, h := range eval.SourceHealth {
		if h != HealthHealthy {
			t.Errorf("SourceHealth[%s] = %s, want healthy", name, h)
		}
	}
	if eval.Envelope == nil {
		t.Fatal("Envelope nil on healthy baseline")
	}
	if _, ok := eval.Envelope["mail_reservations"]; !ok {
		t.Errorf("envelope missing mail_reservations: %v", eval.Envelope)
	}
}

// Slow mail must NOT exceed the budget when the latency sits inside
// it. Critically, the test does this without burning real wall-clock.
func TestRobotSnapshot_SlowMailWithinBudget(t *testing.T) {
	t.Parallel()
	clock := newClock()
	ctx := context.Background()

	wallStart := time.Now()
	eval := Evaluate(ctx, clock, RobotSnapshotSurface(ctx, clock,
		faultharness.Behavior{Mode: faultharness.ModeSlow, Latency: 2 * time.Second},
		healthy(),
	))
	wall := time.Since(wallStart)

	if wall > 100*time.Millisecond {
		t.Fatalf("real wall-clock burned: %s; FakeClock should have absorbed the latency", wall)
	}
	if eval.Elapsed != 2*time.Second {
		t.Errorf("Elapsed = %s, want 2s of simulated time", eval.Elapsed)
	}
	if eval.OverBudget {
		t.Errorf("OverBudget = true; 2s slow within 5s budget should not flag")
	}
	if eval.SourceHealth["mail"] != HealthSlow {
		t.Errorf("SourceHealth[mail] = %s, want slow", eval.SourceHealth["mail"])
	}
	if eval.SourceHealth["bv"] != HealthHealthy {
		t.Errorf("SourceHealth[bv] = %s, want healthy", eval.SourceHealth["bv"])
	}
	if !warningMentions(eval.Warnings, "mail") {
		t.Errorf("Warnings = %v, want one mentioning 'mail'", eval.Warnings)
	}
}

// Latency that overruns the budget must flag OverBudget but still
// emit an envelope (degrade gracefully, not block forever).
func TestRobotSnapshot_OverBudgetStillReturnsEnvelope(t *testing.T) {
	t.Parallel()
	clock := newClock()
	ctx := context.Background()
	eval := Evaluate(ctx, clock, RobotSnapshotSurface(ctx, clock,
		faultharness.Behavior{Mode: faultharness.ModeSlow, Latency: 30 * time.Second},
		healthy(),
	))
	if !eval.OverBudget {
		t.Fatalf("OverBudget = false on 30s latency vs 5s budget")
	}
	if eval.Envelope == nil {
		t.Fatal("Envelope nil; surface must still emit something past the budget")
	}
}

// Causality must classify each degraded provider correctly and name
// every degraded one in Warnings.
func TestCausality_MultipleDegradedProvidersClassified(t *testing.T) {
	t.Parallel()
	clock := newClock()
	ctx := context.Background()
	eval := Evaluate(ctx, clock, CausalitySurface(ctx, clock,
		faultharness.Behavior{Mode: faultharness.ModeUnavailable},
		faultharness.Behavior{Mode: faultharness.ModeStaleCache, StaleSince: clock.NowTime.Add(-10 * time.Minute)},
		faultharness.Behavior{Mode: faultharness.ModePartialSuccess},
	))

	if eval.OverBudget {
		t.Errorf("OverBudget = true on instant-fail providers; want false")
	}
	if eval.SourceHealth["mail"] != HealthUnavailable {
		t.Errorf("SourceHealth[mail] = %s, want unavailable", eval.SourceHealth["mail"])
	}
	if eval.SourceHealth["bv"] != HealthStale {
		t.Errorf("SourceHealth[bv] = %s, want stale", eval.SourceHealth["bv"])
	}
	if eval.SourceHealth["tmux"] != HealthPartial {
		t.Errorf("SourceHealth[tmux] = %s, want partial", eval.SourceHealth["tmux"])
	}
	for _, p := range []string{"mail", "bv", "tmux"} {
		if !warningMentions(eval.Warnings, p) {
			t.Errorf("Warnings = %v, missing mention of %q", eval.Warnings, p)
		}
	}
	if eval.Envelope == nil {
		t.Fatal("Envelope nil; causality must degrade gracefully")
	}
}

// Queue-dry has bv as Required: an Unavailable bv must abort Compose
// and surface an error naming bv.
func TestQueueDry_RequiredBVUnavailableAborts(t *testing.T) {
	t.Parallel()
	clock := newClock()
	ctx := context.Background()
	eval := Evaluate(ctx, clock, QueueDrySurface(ctx, clock,
		faultharness.Behavior{Mode: faultharness.ModeUnavailable},
		healthy(),
	))
	if eval.Err == nil {
		t.Fatal("Err = nil; want abort because bv is Required and unavailable")
	}
	if !strings.Contains(eval.ErrText, "bv") {
		t.Errorf("ErrText = %q, want mention of 'bv'", eval.ErrText)
	}
	if eval.Envelope != nil {
		t.Errorf("Envelope set despite abort: %v", eval.Envelope)
	}
}

// Queue-dry with a healthy bv but a flaky mail must still produce
// a usable envelope — mail is optional.
func TestQueueDry_OptionalMailDegradedStillUsable(t *testing.T) {
	t.Parallel()
	clock := newClock()
	ctx := context.Background()
	eval := Evaluate(ctx, clock, QueueDrySurface(ctx, clock,
		healthy(),
		faultharness.Behavior{Mode: faultharness.ModeMalformedJSON},
	))
	if eval.Err != nil {
		t.Fatalf("Err = %v; mail is optional", eval.Err)
	}
	if eval.SourceHealth["mail"] != HealthDegraded {
		t.Errorf("SourceHealth[mail] = %s, want degraded", eval.SourceHealth["mail"])
	}
	if !warningMentions(eval.Warnings, "mail") {
		t.Errorf("Warnings = %v, want mention of 'mail'", eval.Warnings)
	}
	if eval.Envelope == nil {
		t.Fatal("Envelope nil despite non-Required mail being degraded")
	}
}

// JSON shape must include source_health for every provider so a
// dashboard can colour-code them.
func TestEvaluation_JSONIncludesSourceHealth(t *testing.T) {
	t.Parallel()
	clock := newClock()
	ctx := context.Background()
	eval := Evaluate(ctx, clock, RobotSnapshotSurface(ctx, clock,
		faultharness.Behavior{Mode: faultharness.ModeUnavailable},
		healthy(),
	))
	data, err := json.Marshal(eval)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	sh, ok := v["source_health"].(map[string]any)
	if !ok {
		t.Fatalf("source_health missing or wrong type: %s", data)
	}
	if sh["mail"] != "unavailable" || sh["bv"] != "healthy" {
		t.Errorf("source_health = %v, want mail=unavailable bv=healthy", sh)
	}
	if !strings.Contains(string(data), `"over_budget":false`) {
		t.Errorf("over_budget missing from JSON: %s", data)
	}
}

// Cross-surface matrix: every surface × every degraded provider mode
// must finish without panic and produce a valid Evaluation.
func TestSurfaces_DegradedMatrixSmoke(t *testing.T) {
	t.Parallel()
	clock := newClock()
	ctx := context.Background()
	modes := []faultharness.FailureMode{
		faultharness.ModeSlow,
		faultharness.ModeUnavailable,
		faultharness.ModeMalformedJSON,
		faultharness.ModeStaleCache,
		faultharness.ModePartialSuccess,
	}
	for _, m := range modes {
		t.Run("snapshot_mail_"+string(m), func(t *testing.T) {
			b := faultharness.Behavior{Mode: m, Latency: 1 * time.Second, StaleSince: clock.NowTime.Add(-1 * time.Hour)}
			eval := Evaluate(ctx, clock, RobotSnapshotSurface(ctx, clock, b, healthy()))
			if eval.SourceHealth["mail"] == HealthHealthy {
				t.Errorf("SourceHealth[mail] = healthy under degraded mode %s", m)
			}
			if eval.Envelope == nil && eval.Err == nil {
				t.Errorf("neither Envelope nor Err set for mode %s", m)
			}
		})
		t.Run("causality_bv_"+string(m), func(t *testing.T) {
			b := faultharness.Behavior{Mode: m, Latency: 1 * time.Second, StaleSince: clock.NowTime.Add(-1 * time.Hour)}
			eval := Evaluate(ctx, clock, CausalitySurface(ctx, clock, healthy(), b, healthy()))
			if eval.SourceHealth["bv"] == HealthHealthy {
				t.Errorf("SourceHealth[bv] = healthy under degraded mode %s", m)
			}
		})
	}
}

// Helper: does any warning mention the given substring (case-insens).
func warningMentions(warnings []string, needle string) bool {
	needle = strings.ToLower(needle)
	for _, w := range warnings {
		if strings.Contains(strings.ToLower(w), needle) {
			return true
		}
	}
	return false
}
