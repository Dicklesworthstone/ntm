package profiler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func fixedClock() func() time.Time {
	t := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

// makeSpan builds a fully-populated, already-ended span for tests.
func makeSpan(name, phase string, dur time.Duration, tags Tags) *Span {
	start := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	return &Span{
		Name:      name,
		Phase:     phase,
		StartTime: start,
		EndTime:   start.Add(dur),
		Duration:  dur,
		Tags:      tags,
		ended:     true,
		tracked:   true,
	}
}

func TestComputeHotspots_GroupsByNameAndPhase(t *testing.T) {
	t.Parallel()
	spans := []*Span{
		makeSpan("robot.send", "robot", 30*time.Millisecond, Tags{"session": "proj", "pane": 2}),
		makeSpan("robot.send", "robot", 70*time.Millisecond, Tags{"session": "proj", "pane": 3}),
		makeSpan("tmux.capture", "tmux", 40*time.Millisecond, Tags{"session": "proj"}),
		makeSpan("serve.handler", "serve", 10*time.Millisecond, Tags{"command": "/api/status"}),
	}
	snap := ComputeHotspots(spans, BottleneckOptions{TopN: 5, Now: fixedClock()})
	if !snap.Success {
		t.Fatal("Success = false")
	}
	if snap.SpanCount != 4 {
		t.Errorf("SpanCount = %d, want 4", snap.SpanCount)
	}
	if len(snap.Hotspots) != 3 {
		t.Fatalf("Hotspots = %d, want 3 (grouped)", len(snap.Hotspots))
	}
	// Top hotspot must be robot.send: 30+70 = 100ms.
	top := snap.Hotspots[0]
	if top.Name != "robot.send" || top.Phase != "robot" {
		t.Errorf("Top = %s/%s, want robot.send/robot", top.Name, top.Phase)
	}
	if top.Count != 2 {
		t.Errorf("Top.Count = %d, want 2", top.Count)
	}
	if top.TotalMs != 100 {
		t.Errorf("Top.TotalMs = %v, want 100", top.TotalMs)
	}
	if top.MaxMs != 70 {
		t.Errorf("Top.MaxMs = %v, want 70", top.MaxMs)
	}
	if top.AvgMs != 50 {
		t.Errorf("Top.AvgMs = %v, want 50", top.AvgMs)
	}
	// Pane should be the latest seen (3, stringified).
	if top.Correlation.Pane != "3" {
		t.Errorf("Top.Correlation.Pane = %q, want 3", top.Correlation.Pane)
	}
	// Phases are populated for each non-empty phase.
	if len(snap.Phases) != 3 {
		t.Errorf("Phases = %d, want 3", len(snap.Phases))
	}
}

func TestComputeHotspots_DeterministicTieBreaking(t *testing.T) {
	t.Parallel()
	spans := []*Span{
		makeSpan("zzz", "robot", 5*time.Millisecond, nil),
		makeSpan("aaa", "robot", 5*time.Millisecond, nil),
		makeSpan("mmm", "tmux", 5*time.Millisecond, nil),
	}
	snap1 := ComputeHotspots(spans, BottleneckOptions{Now: fixedClock()})
	snap2 := ComputeHotspots(spans, BottleneckOptions{Now: fixedClock()})

	a, _ := json.Marshal(snap1)
	b, _ := json.Marshal(snap2)
	if string(a) != string(b) {
		t.Errorf("snapshots drifted between calls:\n%s\n%s", a, b)
	}
	// Order must be alphabetical when totals tie.
	if got := snap1.Hotspots[0].Name; got != "aaa" {
		t.Errorf("first hotspot = %s, want aaa (tie-break)", got)
	}
}

func TestComputeHotspots_PhaseFilter(t *testing.T) {
	t.Parallel()
	spans := []*Span{
		makeSpan("a", "robot", 10*time.Millisecond, nil),
		makeSpan("b", "tmux", 10*time.Millisecond, nil),
		makeSpan("c", "serve", 10*time.Millisecond, nil),
	}
	snap := ComputeHotspots(spans, BottleneckOptions{
		PhaseFilter: []string{"robot", "tmux"},
		Now:         fixedClock(),
	})
	if snap.SpanCount != 2 {
		t.Errorf("SpanCount = %d, want 2 (filter)", snap.SpanCount)
	}
	for _, h := range snap.Hotspots {
		if h.Phase != "robot" && h.Phase != "tmux" {
			t.Errorf("hotspot phase %q leaked through filter", h.Phase)
		}
	}
}

func TestComputeHotspots_TopN(t *testing.T) {
	t.Parallel()
	var spans []*Span
	for i, ms := range []int{50, 40, 30, 20, 10} {
		spans = append(spans, makeSpan(string(rune('a'+i)), "robot", time.Duration(ms)*time.Millisecond, nil))
	}
	snap := ComputeHotspots(spans, BottleneckOptions{TopN: 3, Now: fixedClock()})
	if len(snap.Hotspots) != 3 {
		t.Fatalf("Hotspots = %d, want 3 (TopN)", len(snap.Hotspots))
	}
	wantNames := []string{"a", "b", "c"}
	for i, want := range wantNames {
		if snap.Hotspots[i].Name != want {
			t.Errorf("Hotspots[%d].Name = %s, want %s", i, snap.Hotspots[i].Name, want)
		}
	}
}

func TestComputeHotspots_MinDurationDrops(t *testing.T) {
	t.Parallel()
	spans := []*Span{
		makeSpan("hot", "robot", 50*time.Millisecond, nil),
		makeSpan("cold", "robot", 1*time.Millisecond, nil),
	}
	snap := ComputeHotspots(spans, BottleneckOptions{
		MinDuration: 10 * time.Millisecond,
		Now:         fixedClock(),
	})
	if len(snap.Hotspots) != 1 {
		t.Fatalf("Hotspots = %d, want 1 (cold dropped)", len(snap.Hotspots))
	}
	if snap.Hotspots[0].Name != "hot" {
		t.Errorf("Hotspots[0] = %s, want hot", snap.Hotspots[0].Name)
	}
}

func TestComputeHotspots_UnendedSpansCounted(t *testing.T) {
	t.Parallel()
	half := makeSpan("running", "robot", 100*time.Millisecond, nil)
	half.ended = false
	half.Duration = 0
	spans := []*Span{
		makeSpan("done", "robot", 5*time.Millisecond, nil),
		half,
	}
	snap := ComputeHotspots(spans, BottleneckOptions{Now: fixedClock()})
	if snap.SpanCount != 1 {
		t.Errorf("SpanCount = %d, want 1", snap.SpanCount)
	}
	if snap.UnendedSpans != 1 {
		t.Errorf("UnendedSpans = %d, want 1", snap.UnendedSpans)
	}
}

func TestComputeHotspots_CorrelationFromTags(t *testing.T) {
	t.Parallel()
	spans := []*Span{
		makeSpan("h", "robot", 10*time.Millisecond, Tags{
			"session": "proj", "pane": 1, "command": "send", "run_id": "r-1",
		}),
		// later span overrides correlation with newer values.
		makeSpan("h", "robot", 10*time.Millisecond, Tags{
			"session": "proj", "pane": 4, "run_id": "r-2",
		}),
	}
	snap := ComputeHotspots(spans, BottleneckOptions{Now: fixedClock()})
	if len(snap.Hotspots) != 1 {
		t.Fatalf("Hotspots = %d, want 1", len(snap.Hotspots))
	}
	c := snap.Hotspots[0].Correlation
	if c.Session != "proj" {
		t.Errorf("Correlation.Session = %q, want proj", c.Session)
	}
	if c.Pane != "4" {
		t.Errorf("Correlation.Pane = %q, want 4 (latest wins)", c.Pane)
	}
	// command was only on first span; should still be retained.
	if c.Command != "send" {
		t.Errorf("Correlation.Command = %q, want send", c.Command)
	}
	if c.RunID != "r-2" {
		t.Errorf("Correlation.RunID = %q, want r-2 (latest wins)", c.RunID)
	}
}

func TestComputeTrend_NewUpDownStable(t *testing.T) {
	t.Parallel()
	prior := BottleneckSnapshot{Hotspots: []BottleneckHotspot{
		{Name: "hot", Phase: "robot", TotalMs: 50},
		{Name: "warm", Phase: "robot", TotalMs: 20},
		{Name: "leaving", Phase: "robot", TotalMs: 5},
	}}
	curr := BottleneckSnapshot{Hotspots: []BottleneckHotspot{
		{Name: "hot", Phase: "robot", TotalMs: 75},   // up
		{Name: "warm", Phase: "robot", TotalMs: 19},  // stable (within threshold)
		{Name: "fresh", Phase: "robot", TotalMs: 30}, // new
	}}
	got := ComputeTrend(curr, prior, 5)
	want := map[string]string{"hot": "up", "warm": "stable", "fresh": "new"}
	for _, h := range got.Hotspots {
		if h.Trend != want[h.Name] {
			t.Errorf("Trend[%s] = %s, want %s", h.Name, h.Trend, want[h.Name])
		}
		if h.DeltaMs == nil {
			t.Errorf("DeltaMs nil for %s", h.Name)
		}
	}

	// Drop hot below stable threshold to verify "down".
	curr.Hotspots[0].TotalMs = 10
	got = ComputeTrend(curr, prior, 5)
	if got.Hotspots[0].Trend != "down" {
		t.Errorf("Trend[hot] = %s, want down", got.Hotspots[0].Trend)
	}
}

// bd-2eif6: ComputeTrend's godoc says "changeMs <= 0 disables the
// threshold (any non-zero delta classifies as up/down)". The 0 case
// works correctly; pre-fix the negative case did not — `d > changeMs`
// against a negative threshold misclassified small drops and zero
// deltas as "up". This test pins the contract for negative changeMs
// values so a future refactor cannot reintroduce the inversion.
func TestComputeTrend_NegativeChangeMsTreatedAsZeroDisabled(t *testing.T) {
	t.Parallel()
	prior := BottleneckSnapshot{Hotspots: []BottleneckHotspot{
		{Name: "drop", Phase: "robot", TotalMs: 100},
		{Name: "rise", Phase: "robot", TotalMs: 50},
		{Name: "flat", Phase: "robot", TotalMs: 30},
	}}
	curr := BottleneckSnapshot{Hotspots: []BottleneckHotspot{
		{Name: "drop", Phase: "robot", TotalMs: 90}, // d = -10
		{Name: "rise", Phase: "robot", TotalMs: 60}, // d = +10
		{Name: "flat", Phase: "robot", TotalMs: 30}, // d = 0
	}}

	// Pre-fix this would misclassify "drop" as "up" and "flat" as "up"
	// because `d > -1` and `0 > -1` are both true.
	got := ComputeTrend(curr, prior, -1)
	want := map[string]string{
		"drop": "down",   // any non-zero negative delta → down
		"rise": "up",     // any non-zero positive delta → up
		"flat": "stable", // exactly zero → stable
	}
	for _, h := range got.Hotspots {
		if h.Trend != want[h.Name] {
			t.Errorf("Trend[%s] with changeMs=-1 = %q, want %q (negative changeMs must behave like 0-disable)",
				h.Name, h.Trend, want[h.Name])
		}
	}

	// And again with changeMs = -1000 to confirm the clamp covers
	// arbitrary-magnitude negative inputs.
	got = ComputeTrend(curr, prior, -1000)
	for _, h := range got.Hotspots {
		if h.Trend != want[h.Name] {
			t.Errorf("Trend[%s] with changeMs=-1000 = %q, want %q (any negative changeMs clamps to 0)",
				h.Name, h.Trend, want[h.Name])
		}
	}
}

// Integration-style test: feed a synthetic workload through the
// global profiler, take a snapshot, and assert the schema.
func TestLiveBottleneck_AfterSyntheticWorkload(t *testing.T) {
	// Reset global state — this test runs serially.
	Enable()
	t.Cleanup(func() { Disable(); Reset() })
	Reset()

	for i := 0; i < 3; i++ {
		s := StartWithPhase("robot.snapshot", "robot")
		s.Tag("session", "proj")
		s.Tag("command", "snapshot")
		// Yield enough nanos that Duration is non-zero but the test stays fast.
		s.EndTime = time.Now().Add(2 * time.Millisecond)
		s.Duration = 2 * time.Millisecond
		s.ended = true
	}
	for i := 0; i < 2; i++ {
		s := StartWithPhase("tmux.capture", "tmux")
		s.Tag("session", "proj")
		s.EndTime = time.Now().Add(1 * time.Millisecond)
		s.Duration = 1 * time.Millisecond
		s.ended = true
	}

	snap := LiveBottleneck(BottleneckOptions{TopN: 5, Now: fixedClock()})
	if !snap.Success {
		t.Fatal("Success = false")
	}
	if snap.SpanCount != 5 {
		t.Errorf("SpanCount = %d, want 5", snap.SpanCount)
	}
	if len(snap.Hotspots) != 2 {
		t.Fatalf("Hotspots = %d, want 2", len(snap.Hotspots))
	}
	// robot.snapshot should be the top hotspot (3 * 2ms = 6ms total).
	if snap.Hotspots[0].Name != "robot.snapshot" {
		t.Errorf("Top hotspot = %s, want robot.snapshot", snap.Hotspots[0].Name)
	}
	if snap.Hotspots[0].Correlation.Session != "proj" {
		t.Errorf("Top correlation session = %q, want proj", snap.Hotspots[0].Correlation.Session)
	}

	// JSON round-trip is stable.
	a, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !strings.Contains(string(a), `"hotspots"`) {
		t.Errorf("expected hotspots field, got: %s", a)
	}
}

func TestLiveBottleneck_DisabledReturnsEmpty(t *testing.T) {
	Disable()
	t.Cleanup(func() { Disable(); Reset() })
	snap := LiveBottleneck(BottleneckOptions{Now: fixedClock()})
	if !snap.Success {
		t.Fatal("Success = false on disabled profiler")
	}
	if snap.SpanCount != 0 || len(snap.Hotspots) != 0 {
		t.Errorf("expected empty snapshot, got SpanCount=%d Hotspots=%d", snap.SpanCount, len(snap.Hotspots))
	}
	if snap.Timestamp == "" {
		t.Error("Timestamp is empty even when disabled")
	}
}

func TestBottleneckCorrelation_OmitemptyOnZero(t *testing.T) {
	t.Parallel()
	h := BottleneckHotspot{Name: "h", TotalMs: 1}
	if !h.Correlation.IsZero() {
		t.Error("zero Correlation reports IsZero false")
	}
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(b), `"correlation"`) {
		t.Errorf("zero correlation leaked into JSON: %s", b)
	}
}
