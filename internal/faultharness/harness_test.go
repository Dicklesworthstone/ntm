package faultharness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func newFakeClock() *FakeClock {
	return &FakeClock{NowTime: time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)}
}

func TestApply_Healthy(t *testing.T) {
	t.Parallel()
	c := newFakeClock()
	r := Apply(context.Background(), c, Behavior{Mode: ModeHealthy}, []byte(`{"ok":true}`))
	if r.Err != nil {
		t.Fatalf("Err = %v, want nil", r.Err)
	}
	if string(r.Payload) != `{"ok":true}` {
		t.Errorf("Payload = %s, want healthy body", r.Payload)
	}
	if len(c.Sleeps) != 0 {
		t.Errorf("Sleeps = %v, want none for zero-latency healthy", c.Sleeps)
	}
}

func TestApply_Slow_RecordsLatencyWithoutRealSleep(t *testing.T) {
	t.Parallel()
	c := newFakeClock()
	start := time.Now()
	r := Apply(context.Background(), c, Behavior{Mode: ModeSlow, Latency: 5 * time.Second}, []byte("body"))
	wallClock := time.Since(start)

	if wallClock > 100*time.Millisecond {
		t.Fatalf("real sleep happened (%s); harness should use FakeClock", wallClock)
	}
	if r.Latency != 5*time.Second {
		t.Errorf("Latency = %s, want 5s", r.Latency)
	}
	if len(c.Sleeps) != 1 || c.Sleeps[0] != 5*time.Second {
		t.Errorf("Sleeps = %v, want [5s]", c.Sleeps)
	}
	if !containsString(r.Warnings, "slow_response") {
		t.Errorf("Warnings = %v, want slow_response marker", r.Warnings)
	}
}

func TestApply_DeadlineExceededIsAlwaysErr(t *testing.T) {
	t.Parallel()
	c := newFakeClock()
	r := Apply(context.Background(), c, Behavior{Mode: ModeDeadlineExceeded, Latency: 10 * time.Second}, nil)
	if !errors.Is(r.Err, context.DeadlineExceeded) {
		t.Errorf("Err = %v, want context.DeadlineExceeded", r.Err)
	}
	if !containsString(r.Warnings, "deadline_exceeded") {
		t.Errorf("Warnings = %v, want deadline_exceeded marker", r.Warnings)
	}
}

func TestApply_DeadlineExceededHonorsCtxCancel(t *testing.T) {
	t.Parallel()
	c := newFakeClock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already done
	r := Apply(ctx, c, Behavior{Mode: ModeDeadlineExceeded, Latency: 10 * time.Second}, nil)
	if !errors.Is(r.Err, context.DeadlineExceeded) {
		t.Errorf("Err = %v, want context.DeadlineExceeded even when ctx is cancelled", r.Err)
	}
}

func TestApply_Unavailable(t *testing.T) {
	t.Parallel()
	r := Apply(context.Background(), newFakeClock(), Behavior{Mode: ModeUnavailable}, nil)
	if !errors.Is(r.Err, ErrUnavailable) {
		t.Errorf("Err = %v, want ErrUnavailable", r.Err)
	}
	if r.Latency != 0 {
		t.Errorf("Latency = %s, want 0 (Unavailable returns immediately)", r.Latency)
	}
}

func TestApply_MalformedJSON_ReturnsBytesAndError(t *testing.T) {
	t.Parallel()
	r := Apply(context.Background(), newFakeClock(), Behavior{Mode: ModeMalformedJSON}, nil)
	if !errors.Is(r.Err, ErrMalformedJSON) {
		t.Errorf("Err = %v, want ErrMalformedJSON", r.Err)
	}
	if len(r.Payload) == 0 {
		t.Error("Payload empty; expected default malformed body")
	}
	// And the default body must actually fail json.Unmarshal.
	var v interface{}
	if err := json.Unmarshal(r.Payload, &v); err == nil {
		t.Errorf("default malformed payload parses cleanly: %s", r.Payload)
	}
}

func TestApply_MalformedJSON_RespectsCustomPayload(t *testing.T) {
	t.Parallel()
	custom := []byte(`{"truncated":`)
	r := Apply(context.Background(), newFakeClock(), Behavior{Mode: ModeMalformedJSON, Payload: custom}, []byte("healthy"))
	if string(r.Payload) != string(custom) {
		t.Errorf("Payload = %s, want custom %s", r.Payload, custom)
	}
}

func TestApply_StaleCache_ComputesAgeFromClock(t *testing.T) {
	t.Parallel()
	c := newFakeClock()
	stale := c.NowTime.Add(-30 * time.Minute)
	r := Apply(context.Background(), c, Behavior{Mode: ModeStaleCache, StaleSince: stale}, []byte("body"))
	if !r.Stale {
		t.Fatal("Stale = false, want true")
	}
	if r.StaleAge != 30*time.Minute {
		t.Errorf("StaleAge = %s, want 30m", r.StaleAge)
	}
	if r.Err != nil {
		t.Errorf("Err = %v, want nil (stale data is still data)", r.Err)
	}
	if string(r.Payload) != "body" {
		t.Errorf("Payload = %s, want healthy body", r.Payload)
	}
}

func TestApply_PartialSuccess_ReturnsPayloadAndError(t *testing.T) {
	t.Parallel()
	r := Apply(context.Background(), newFakeClock(), Behavior{
		Mode:    ModePartialSuccess,
		Payload: []byte(`["item1"]`),
	}, []byte(`["item1","item2","item3"]`))
	if !errors.Is(r.Err, ErrPartialSuccess) {
		t.Errorf("Err = %v, want ErrPartialSuccess", r.Err)
	}
	if string(r.Payload) != `["item1"]` {
		t.Errorf("Payload = %s, want partial body", r.Payload)
	}
}

func TestApply_PartialSuccess_FallsBackToHealthyWhenPayloadNil(t *testing.T) {
	t.Parallel()
	r := Apply(context.Background(), newFakeClock(), Behavior{Mode: ModePartialSuccess}, []byte(`["full"]`))
	if string(r.Payload) != `["full"]` {
		t.Errorf("Payload = %s, want healthy fallback", r.Payload)
	}
}

func TestApply_UnknownModeIsUnavailable(t *testing.T) {
	t.Parallel()
	r := Apply(context.Background(), newFakeClock(), Behavior{Mode: FailureMode("garbage")}, nil)
	if !errors.Is(r.Err, ErrUnavailable) {
		t.Errorf("Err = %v, want ErrUnavailable for unknown mode", r.Err)
	}
	if !containsString(r.Warnings, "unknown_failure_mode:garbage") {
		t.Errorf("Warnings = %v, want unknown_failure_mode marker", r.Warnings)
	}
}

func TestFakeClock_AdvancesNowAndCanReadConcurrently(t *testing.T) {
	t.Parallel()
	c := newFakeClock()
	start := c.Now()
	if err := c.Sleep(context.Background(), 5*time.Second); err != nil {
		t.Fatalf("Sleep err: %v", err)
	}
	if got := c.Now().Sub(start); got != 5*time.Second {
		t.Errorf("clock advance = %s, want 5s", got)
	}
}

func TestFakeClock_RespectsCancelledCtx(t *testing.T) {
	t.Parallel()
	c := newFakeClock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Sleep(ctx, 5*time.Second); !errors.Is(err, context.Canceled) {
		t.Errorf("Sleep err = %v, want context.Canceled", err)
	}
	if len(c.Sleeps) != 0 {
		t.Errorf("Sleeps = %v, want none recorded when ctx already cancelled", c.Sleeps)
	}
}

// Provider-wrapper tests: each fake forwards to Apply with the
// appropriate canonical healthy payload.

func TestMailReservationsFake_HealthyShape(t *testing.T) {
	t.Parallel()
	r := MailReservationsFake(context.Background(), newFakeClock(), Behavior{Mode: ModeHealthy})
	if r.Err != nil {
		t.Fatalf("Err = %v", r.Err)
	}
	var v []map[string]interface{}
	if err := json.Unmarshal(r.Payload, &v); err != nil {
		t.Fatalf("healthy mail payload not parseable: %v", err)
	}
	if len(v) != 2 {
		t.Errorf("expected 2 reservations, got %d", len(v))
	}
}

func TestBVTriageFake_HealthyShape(t *testing.T) {
	t.Parallel()
	r := BVTriageFake(context.Background(), newFakeClock(), Behavior{Mode: ModeHealthy})
	var v map[string]interface{}
	if err := json.Unmarshal(r.Payload, &v); err != nil {
		t.Fatalf("healthy bv payload not parseable: %v", err)
	}
	if _, ok := v["quick_ref"]; !ok {
		t.Errorf("bv payload missing quick_ref: %s", r.Payload)
	}
}

func TestCASSSearchFake_HealthyShape(t *testing.T) {
	t.Parallel()
	r := CASSSearchFake(context.Background(), newFakeClock(), Behavior{Mode: ModeHealthy})
	var v map[string]interface{}
	if err := json.Unmarshal(r.Payload, &v); err != nil {
		t.Fatalf("healthy cass payload not parseable: %v", err)
	}
	hits, _ := v["hits"].([]interface{})
	if len(hits) != 1 {
		t.Errorf("expected 1 hit, got %d", len(hits))
	}
}

func TestTmuxCaptureFake_HealthyShape(t *testing.T) {
	t.Parallel()
	r := TmuxCaptureFake(context.Background(), newFakeClock(), Behavior{Mode: ModeHealthy})
	if !strings.Contains(string(r.Payload), "claude>") {
		t.Errorf("expected prompt in tmux capture, got %s", r.Payload)
	}
}

// Cross-provider matrix: every provider wrapper must surface every
// failure mode via the same Result shape.
func TestProviderMatrix_AllModesFlowThrough(t *testing.T) {
	t.Parallel()
	type wrapper struct {
		name string
		fn   func(context.Context, Clock, Behavior) Result
	}
	wrappers := []wrapper{
		{"mail", MailReservationsFake},
		{"bv", BVTriageFake},
		{"cass", CASSSearchFake},
		{"tmux", TmuxCaptureFake},
	}
	modes := []FailureMode{ModeUnavailable, ModeDeadlineExceeded, ModeMalformedJSON, ModeStaleCache, ModePartialSuccess}
	for _, w := range wrappers {
		for _, m := range modes {
			t.Run(w.name+"_"+string(m), func(t *testing.T) {
				r := w.fn(context.Background(), newFakeClock(), Behavior{
					Mode:       m,
					StaleSince: time.Date(2026, 5, 8, 11, 30, 0, 0, time.UTC),
				})
				switch m {
				case ModeUnavailable:
					if !errors.Is(r.Err, ErrUnavailable) {
						t.Errorf("Err = %v, want ErrUnavailable", r.Err)
					}
				case ModeDeadlineExceeded:
					if !errors.Is(r.Err, context.DeadlineExceeded) {
						t.Errorf("Err = %v, want DeadlineExceeded", r.Err)
					}
				case ModeMalformedJSON:
					if !errors.Is(r.Err, ErrMalformedJSON) {
						t.Errorf("Err = %v, want ErrMalformedJSON", r.Err)
					}
				case ModeStaleCache:
					if !r.Stale {
						t.Errorf("Stale = false, want true")
					}
				case ModePartialSuccess:
					if !errors.Is(r.Err, ErrPartialSuccess) {
						t.Errorf("Err = %v, want ErrPartialSuccess", r.Err)
					}
				}
			})
		}
	}
}

func TestRunRecoveryDrill_ClassifiesSwarmFailures(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	pipelineInterrupted := func(context.Context, Clock, Behavior) Result {
		return Result{
			Err:      context.Canceled,
			Warnings: []string{"pipeline_interrupted"},
		}
	}
	paneExited := func(context.Context, Clock, Behavior) Result {
		return Result{
			Err:      ErrUnavailable,
			Warnings: []string{"pane_exited"},
		}
	}
	checkpointRestore := func(ctx context.Context, clock Clock, b Behavior) Result {
		return Apply(ctx, clock, b, []byte(`{"checkpoint":"ok"}`))
	}

	report := RunRecoveryDrill(context.Background(), clock, RecoveryDrill{
		Name: "large-swarm-recovery",
		Steps: []RecoveryDrillStep{
			{
				Name:     "tmux command latency",
				Provider: "tmux",
				Behavior: Behavior{Mode: ModeDeadlineExceeded, Latency: 3 * time.Second},
				Run:      TmuxCaptureFake,
			},
			{
				Name:     "pane exit",
				Provider: "pane",
				Behavior: Behavior{Mode: ModeUnavailable},
				Run:      paneExited,
			},
			{
				Name:     "agent mail unavailable",
				Provider: "agent_mail",
				Behavior: Behavior{Mode: ModeUnavailable},
				Run:      MailReservationsFake,
			},
			{
				Name:     "stale reservations",
				Provider: "agent_mail",
				Behavior: Behavior{
					Mode:       ModeStaleCache,
					StaleSince: clock.NowTime.Add(-45 * time.Minute),
				},
				Run: MailReservationsFake,
			},
			{
				Name:     "interrupted pipeline step",
				Provider: "pipeline",
				Behavior: Behavior{Mode: ModeHealthy},
				Run:      pipelineInterrupted,
			},
			{
				Name:     "checkpoint restore malformed output",
				Provider: "checkpoint",
				Behavior: Behavior{Mode: ModeMalformedJSON},
				Run:      checkpointRestore,
			},
		},
	})

	if report.Name != "large-swarm-recovery" {
		t.Fatalf("Name = %q, want large-swarm-recovery", report.Name)
	}
	if len(report.Outcomes) != 6 {
		t.Fatalf("Outcomes = %d, want 6", len(report.Outcomes))
	}

	tests := []struct {
		index      int
		name       string
		diagnostic string
		action     RecoveryAction
	}{
		{0, "tmux command latency", "tmux timed out", RecoveryActionRetryWithBackoff},
		{1, "pane exit", "pane unavailable", RecoveryActionRetryWithBackoff},
		{2, "agent mail unavailable", "agent_mail unavailable", RecoveryActionRetryWithBackoff},
		{3, "stale reservations", "agent_mail returned stale data", RecoveryActionRefreshBeforeMutation},
		{4, "interrupted pipeline step", "pipeline interrupted", RecoveryActionResumeFromCheckpoint},
		{5, "checkpoint restore malformed output", "checkpoint returned malformed output", RecoveryActionRequestFreshSnapshot},
	}
	for _, tt := range tests {
		got := report.Outcomes[tt.index]
		if got.Name != tt.name {
			t.Errorf("outcome[%d].Name = %q, want %q", tt.index, got.Name, tt.name)
		}
		if got.Diagnostic != tt.diagnostic {
			t.Errorf("outcome[%d].Diagnostic = %q, want %q", tt.index, got.Diagnostic, tt.diagnostic)
		}
		if got.RecoveryAction != tt.action {
			t.Errorf("outcome[%d].RecoveryAction = %q, want %q", tt.index, got.RecoveryAction, tt.action)
		}
	}
	if report.Outcomes[0].Latency != 3*time.Second {
		t.Errorf("tmux latency = %s, want 3s", report.Outcomes[0].Latency)
	}
	if report.Outcomes[3].StaleAge != 45*time.Minute+3*time.Second {
		t.Errorf("stale age = %s, want 45m3s", report.Outcomes[3].StaleAge)
	}
	if !containsString(report.Outcomes[1].Warnings, "pane_exited") {
		t.Errorf("pane warnings = %v, want pane_exited", report.Outcomes[1].Warnings)
	}
	if !errors.Is(report.Outcomes[4].Err, context.Canceled) {
		t.Errorf("pipeline err = %v, want context.Canceled", report.Outcomes[4].Err)
	}
}

func TestRunRecoveryDrill_MissingProviderEscalates(t *testing.T) {
	t.Parallel()
	report := RunRecoveryDrill(context.Background(), newFakeClock(), RecoveryDrill{
		Name: "missing-provider",
		Steps: []RecoveryDrillStep{
			{Name: "unwired step", Provider: "robot"},
		},
	})
	if len(report.Outcomes) != 1 {
		t.Fatalf("Outcomes = %d, want 1", len(report.Outcomes))
	}
	got := report.Outcomes[0]
	if got.Diagnostic != "robot unavailable" {
		t.Errorf("Diagnostic = %q, want robot unavailable", got.Diagnostic)
	}
	if got.RecoveryAction != RecoveryActionRetryWithBackoff {
		t.Errorf("RecoveryAction = %q, want %q", got.RecoveryAction, RecoveryActionRetryWithBackoff)
	}
	if !containsString(got.Warnings, "missing_provider") {
		t.Errorf("Warnings = %v, want missing_provider", got.Warnings)
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
