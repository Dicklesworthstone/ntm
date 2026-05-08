package pressure

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestClassify_Levels(t *testing.T) {
	t.Parallel()
	th := Thresholds{Elevated: 0.60, High: 0.80, Critical: 0.92}
	cases := []struct {
		v    float64
		want Level
	}{
		{-1, LevelLow},
		{0, LevelLow},
		{0.10, LevelLow},
		{0.30, LevelNormal}, // >= Elevated/2 (0.30) is Normal
		{0.59, LevelNormal},
		{0.60, LevelElevated},
		{0.79, LevelElevated},
		{0.80, LevelHigh},
		{0.91, LevelHigh},
		{0.92, LevelCritical},
		{1.00, LevelCritical},
	}
	for _, c := range cases {
		got := Classify(c.v, th)
		if got != c.want {
			t.Errorf("Classify(%.2f) = %s, want %s", c.v, got, c.want)
		}
	}
}

func TestClassify_DefaultThresholdsCoverAllSources(t *testing.T) {
	t.Parallel()
	th := DefaultThresholds()
	for _, src := range []Source{
		SourceCPU, SourceMemory, SourceLoad, SourceProcCount,
		SourcePaneActivity, SourcePipelineFanout, SourceRchQueue, SourceLocalBuild,
	} {
		if _, ok := th[src]; !ok {
			t.Errorf("DefaultThresholds missing source %q", src)
		}
	}
}

func TestMergeBudgets_TightestWins(t *testing.T) {
	t.Parallel()
	base := Budget{
		MaxConcurrentSends: 16,
		MaxPipelineFanout:  16,
		MaxBuildSlots:      8,
		DeferAtLevel:       LevelHigh,
		DenyAtLevel:        LevelCritical,
		ScannerInterval:    5 * time.Second,
	}
	override := Budget{
		MaxConcurrentSends: 4,                // tighter
		MaxPipelineFanout:  0,                // unspecified, base wins
		MaxBuildSlots:      32,               // looser, base wins
		DeferAtLevel:       LevelElevated,    // less tolerant, override wins
		DenyAtLevel:        LevelHigh,        // less tolerant, override wins
		ScannerInterval:    10 * time.Second, // longer interval = more conservative
	}
	got := MergeBudgets(base, override)

	if got.MaxConcurrentSends != 4 {
		t.Errorf("MaxConcurrentSends = %d, want 4", got.MaxConcurrentSends)
	}
	if got.MaxPipelineFanout != 16 {
		t.Errorf("MaxPipelineFanout = %d, want 16 (base, override 0)", got.MaxPipelineFanout)
	}
	if got.MaxBuildSlots != 8 {
		t.Errorf("MaxBuildSlots = %d, want 8 (base, override looser)", got.MaxBuildSlots)
	}
	if got.DeferAtLevel != LevelElevated {
		t.Errorf("DeferAtLevel = %s, want elevated", got.DeferAtLevel)
	}
	if got.DenyAtLevel != LevelHigh {
		t.Errorf("DenyAtLevel = %s, want high", got.DenyAtLevel)
	}
	if got.ScannerInterval != 10*time.Second {
		t.Errorf("ScannerInterval = %s, want 10s", got.ScannerInterval)
	}
}

func TestMergeBudgets_ZeroOverridePreservesBase(t *testing.T) {
	t.Parallel()
	base := DefaultBudget()
	got := MergeBudgets(base, Budget{})
	if got.MaxConcurrentSends != base.MaxConcurrentSends {
		t.Errorf("MaxConcurrentSends = %d, want %d", got.MaxConcurrentSends, base.MaxConcurrentSends)
	}
	if got.DeferAtLevel != base.DeferAtLevel {
		t.Errorf("DeferAtLevel = %s, want %s", got.DeferAtLevel, base.DeferAtLevel)
	}
}

func TestEvaluateSpawnAdmission_AdmitsWithHeadroom(t *testing.T) {
	t.Parallel()
	admission := EvaluateSpawnAdmission(SpawnAdmissionInput{
		Session:             "proj",
		RequestedAgents:     3,
		RequestedPanes:      4,
		SessionPanes:        1,
		CurrentPanes:        8,
		RunningAgents:       5,
		RunningSessions:     2,
		MaxAgents:           7,
		LargeSpawnThreshold: 4,
		Pressure: buildSnapshot(fixedClock()(), []Reading{
			{Source: SourceCPU, Value: 0.20, Unit: "ratio"},
			{Source: SourceMemory, Value: 0.30, Unit: "ratio"},
		}, DefaultThresholds()),
	})
	if admission.Decision != SpawnAdmissionAdmit {
		t.Fatalf("Decision = %s, want admit", admission.Decision)
	}
	if admission.AdditionalPanes != 3 {
		t.Errorf("AdditionalPanes = %d, want 3", admission.AdditionalPanes)
	}
	if admission.ProjectedPanes != 11 {
		t.Errorf("ProjectedPanes = %d, want 11", admission.ProjectedPanes)
	}
	if admission.AgentHeadroom != 4 {
		t.Errorf("AgentHeadroom = %d, want 4", admission.AgentHeadroom)
	}
}

func TestEvaluateSpawnAdmission_DefersLargeSpawnUnderHighPressure(t *testing.T) {
	t.Parallel()
	admission := EvaluateSpawnAdmission(SpawnAdmissionInput{
		Session:             "proj",
		RequestedAgents:     4,
		RequestedPanes:      5,
		MaxAgents:           8,
		LargeSpawnThreshold: 4,
		Pressure: buildSnapshot(fixedClock()(), []Reading{
			{Source: SourceCPU, Value: 0.85, Unit: "ratio"},
		}, DefaultThresholds()),
	})
	if admission.Decision != SpawnAdmissionDefer {
		t.Fatalf("Decision = %s, want defer", admission.Decision)
	}
	if admission.Reason != "pressure_high" {
		t.Errorf("Reason = %q, want pressure_high", admission.Reason)
	}
	if !strings.Contains(admission.Hint, "cpu") {
		t.Errorf("Hint = %q, want cpu", admission.Hint)
	}
	if !admission.LargeSpawn {
		t.Error("LargeSpawn = false, want true")
	}
}

func TestEvaluateSpawnAdmission_RefusesCriticalPressure(t *testing.T) {
	t.Parallel()
	admission := EvaluateSpawnAdmission(SpawnAdmissionInput{
		Session:             "proj",
		RequestedAgents:     6,
		RequestedPanes:      7,
		MaxAgents:           8,
		LargeSpawnThreshold: 4,
		Pressure: buildSnapshot(fixedClock()(), []Reading{
			{Source: SourceMemory, Value: 0.95, Unit: "ratio"},
		}, DefaultThresholds()),
	})
	if admission.Decision != SpawnAdmissionRefuse {
		t.Fatalf("Decision = %s, want refuse", admission.Decision)
	}
	if admission.Reason != "pressure_critical" {
		t.Errorf("Reason = %q, want pressure_critical", admission.Reason)
	}
}

func TestEvaluateSpawnAdmission_RefusesAgentLimit(t *testing.T) {
	t.Parallel()
	admission := EvaluateSpawnAdmission(SpawnAdmissionInput{
		Session:             "proj",
		RequestedAgents:     9,
		RequestedPanes:      10,
		MaxAgents:           7,
		LargeSpawnThreshold: 4,
	})
	if admission.Decision != SpawnAdmissionRefuse {
		t.Fatalf("Decision = %s, want refuse", admission.Decision)
	}
	if admission.Reason != "agent_limit_exceeded" {
		t.Errorf("Reason = %q, want agent_limit_exceeded", admission.Reason)
	}
	if admission.AgentHeadroom != 0 {
		t.Errorf("AgentHeadroom = %d, want 0", admission.AgentHeadroom)
	}
}

// fixedClock returns a deterministic time so snapshots are stable.
func fixedClock() func() time.Time {
	t := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

func newTestGovernor(t *testing.T, mode Mode, providers ...Provider) *Governor {
	t.Helper()
	return New(Config{
		Mode:      mode,
		Providers: providers,
		Now:       fixedClock(),
	})
}

func TestGovernor_RefreshLatestSnapshotIsDeterministic(t *testing.T) {
	t.Parallel()
	fp := NewFakeProvider("fake",
		Reading{Source: SourceCPU, Value: 0.40, Unit: "ratio"},
		Reading{Source: SourceMemory, Value: 0.50, Unit: "ratio"},
	)
	g := newTestGovernor(t, ModeObserve, fp)
	snap := g.Refresh(context.Background())
	// CPU 0.40 and Memory 0.50 are >= Elevated/2 of their respective
	// thresholds (0.30 / 0.325) so both classify as Normal.
	if snap.Overall != LevelNormal {
		t.Errorf("Overall = %s, want normal", snap.Overall)
	}
	if got := g.Latest().Overall; got != LevelNormal {
		t.Errorf("Latest().Overall = %s, want normal", got)
	}
}

func TestGovernor_ObserveOnlyAlwaysAllows(t *testing.T) {
	t.Parallel()
	fp := NewFakeProvider("fake",
		Reading{Source: SourceCPU, Value: 0.99, Unit: "ratio"}, // critical
	)
	g := newTestGovernor(t, ModeObserve, fp)
	g.Refresh(context.Background())
	res := g.Gate(ActionAgentSend, "proj1", false)
	if res.Decision != DecisionAllow {
		t.Fatalf("Decision = %s, want allow (observe)", res.Decision)
	}
	if !strings.HasPrefix(res.Reason, "observe_only:") {
		t.Errorf("Reason = %q, want observe_only:* prefix", res.Reason)
	}
	if res.LevelText != "critical" {
		t.Errorf("LevelText = %q, want critical", res.LevelText)
	}
}

func TestGovernor_EnforceDefersAtHigh(t *testing.T) {
	t.Parallel()
	fp := NewFakeProvider("fake",
		Reading{Source: SourceCPU, Value: 0.85, Unit: "ratio"}, // high
	)
	g := newTestGovernor(t, ModeEnforce, fp)
	g.Refresh(context.Background())
	res := g.Gate(ActionPipelineFanout, "", false)
	if res.Decision != DecisionDefer {
		t.Fatalf("Decision = %s, want defer", res.Decision)
	}
	if res.Hint == "" || !strings.Contains(res.Hint, "cpu") {
		t.Errorf("Hint = %q, want a hint mentioning cpu", res.Hint)
	}
}

func TestGovernor_EnforceDeniesAtCritical(t *testing.T) {
	t.Parallel()
	fp := NewFakeProvider("fake",
		Reading{Source: SourceMemory, Value: 0.95, Unit: "ratio"}, // critical
	)
	g := newTestGovernor(t, ModeEnforce, fp)
	g.Refresh(context.Background())
	res := g.Gate(ActionBuildOrTest, "build", false)
	if res.Decision != DecisionDeny {
		t.Fatalf("Decision = %s, want deny", res.Decision)
	}
	if !strings.Contains(res.Hint, "rch") {
		t.Errorf("Hint = %q, want rch offload suggestion for build", res.Hint)
	}
}

func TestGovernor_UrgentBypassesGate(t *testing.T) {
	t.Parallel()
	fp := NewFakeProvider("fake",
		Reading{Source: SourceCPU, Value: 0.99, Unit: "ratio"},
	)
	g := newTestGovernor(t, ModeEnforce, fp)
	g.Refresh(context.Background())
	res := g.Gate(ActionAgentInterrupt, "proj1", true)
	if res.Decision != DecisionAllow {
		t.Fatalf("Decision = %s, want allow (urgent)", res.Decision)
	}
	if res.Reason != "urgent" {
		t.Errorf("Reason = %q, want urgent", res.Reason)
	}
}

func TestGovernor_SessionBudgetOverridesGlobal(t *testing.T) {
	t.Parallel()
	fp := NewFakeProvider("fake",
		Reading{Source: SourceCPU, Value: 0.70, Unit: "ratio"}, // elevated
	)
	g := New(Config{
		Mode:      ModeEnforce,
		Providers: []Provider{fp},
		Now:       fixedClock(),
		// Per-session: defer at elevated.
		SessionBudget: map[string]Budget{
			"strict": {
				DeferAtLevel: LevelElevated,
				DenyAtLevel:  LevelCritical,
			},
		},
	})
	g.Refresh(context.Background())

	// Default global budget defers at high — elevated should still be allow.
	if got := g.Gate(ActionAgentSend, "loose", false); got.Decision != DecisionAllow {
		t.Errorf("loose session got Decision %s, want allow", got.Decision)
	}
	if got := g.Gate(ActionAgentSend, "strict", false); got.Decision != DecisionDefer {
		t.Errorf("strict session got Decision %s, want defer", got.Decision)
	}
}

func TestGovernor_ProviderErrorIsNonFatal(t *testing.T) {
	t.Parallel()
	good := NewFakeProvider("good",
		Reading{Source: SourceCPU, Value: 0.10, Unit: "ratio"},
	)
	bad := NewFakeProvider("bad")
	bad.SetError(errors.New("boom"))

	g := newTestGovernor(t, ModeObserve, good, bad)
	snap := g.Refresh(context.Background())
	if len(snap.Readings) != 1 {
		t.Fatalf("Readings = %d, want 1 (bad provider must be skipped)", len(snap.Readings))
	}
	if snap.Readings[0].Source != SourceCPU {
		t.Errorf("Reading source = %s, want cpu", snap.Readings[0].Source)
	}
}

// Integration-style test: simulate high CPU + high rch-queue pressure
// from independent fake providers and assert the robot snapshot is
// stable JSON the documented surface promises.
func TestRobotSnapshot_StableJSON_HighPressure(t *testing.T) {
	t.Parallel()
	cpu := NewFakeProvider("cpu",
		Reading{Source: SourceCPU, Value: 0.90, Unit: "ratio"}, // high
	)
	rch := NewFakeProvider("rch",
		Reading{Source: SourceRchQueue, Value: 0.85, Unit: "ratio"}, // high
	)
	mem := NewFakeProvider("mem",
		Reading{Source: SourceMemory, Value: 0.40, Unit: "ratio"}, // low/normal
	)
	g := New(Config{
		Mode:      ModeEnforce,
		Providers: []Provider{cpu, rch, mem},
		Now:       fixedClock(),
	})
	g.Refresh(context.Background())

	rp := g.RobotSnapshot()
	if !rp.Success {
		t.Fatalf("RobotSnapshot.Success = false")
	}
	if rp.Mode != "enforce" || !rp.Enforcing {
		t.Errorf("Mode/Enforcing = %s/%v, want enforce/true", rp.Mode, rp.Enforcing)
	}
	if rp.Overall != "high" {
		t.Errorf("Overall = %s, want high", rp.Overall)
	}
	if rp.RecommendedAction != "defer_non_urgent_work" {
		t.Errorf("RecommendedAction = %s, want defer_non_urgent_work", rp.RecommendedAction)
	}
	wantLimiting := []string{"cpu", "rch_queue"}
	if !equalStrings(rp.Limiting, wantLimiting) {
		t.Errorf("Limiting = %v, want %v", rp.Limiting, wantLimiting)
	}
	if len(rp.Sources) != 3 {
		t.Fatalf("Sources = %d, want 3", len(rp.Sources))
	}
	// Sources must be sorted alphabetically by source name.
	wantOrder := []string{"cpu", "memory", "rch_queue"}
	for i, s := range rp.Sources {
		if s.Source != wantOrder[i] {
			t.Errorf("Sources[%d].Source = %s, want %s", i, s.Source, wantOrder[i])
		}
	}

	// JSON round-trip must be stable across two calls (deterministic).
	a, err := json.Marshal(rp)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	b, err := json.Marshal(g.RobotSnapshot())
	if err != nil {
		t.Fatalf("json.Marshal (second): %v", err)
	}
	if string(a) != string(b) {
		t.Errorf("RobotSnapshot JSON drifted between calls:\nfirst:  %s\nsecond: %s", a, b)
	}
}

func TestRobotSnapshot_OkRecommendationWhenLow(t *testing.T) {
	t.Parallel()
	g := newTestGovernor(t, ModeObserve, NewFakeProvider("idle",
		Reading{Source: SourceCPU, Value: 0.05, Unit: "ratio"},
	))
	g.Refresh(context.Background())
	rp := g.RobotSnapshot()
	if rp.RecommendedAction != "ok" {
		t.Errorf("RecommendedAction = %s, want ok", rp.RecommendedAction)
	}
	if rp.Overall != "low" {
		t.Errorf("Overall = %s, want low", rp.Overall)
	}
}

func TestRobotSnapshot_NoRefreshIsEmptyButValid(t *testing.T) {
	t.Parallel()
	g := newTestGovernor(t, ModeObserve)
	rp := g.RobotSnapshot()
	if !rp.Success {
		t.Fatalf("Success = false on empty snapshot")
	}
	if rp.Overall != "low" {
		t.Errorf("Overall = %s, want low (empty)", rp.Overall)
	}
	if rp.Sources != nil {
		t.Errorf("Sources = %v, want nil for empty snapshot", rp.Sources)
	}
}

func TestSetMode_Toggles(t *testing.T) {
	t.Parallel()
	g := newTestGovernor(t, ModeObserve)
	if g.Mode() != ModeObserve {
		t.Fatalf("initial Mode = %s, want observe", g.Mode())
	}
	g.SetMode(ModeEnforce)
	if g.Mode() != ModeEnforce {
		t.Errorf("after SetMode(enforce), Mode = %s", g.Mode())
	}
	g.SetMode(Mode("garbage"))
	if g.Mode() != ModeObserve {
		t.Errorf("after SetMode(garbage), Mode = %s, want observe (sanitized)", g.Mode())
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
