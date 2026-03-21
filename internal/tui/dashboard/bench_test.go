package dashboard

import (
	"fmt"
	"os"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
)

// Benchmarks for wide rendering performance (bd ntm-34qr).
// Additional mega layout benchmarks (bd ntm-jypl).

// BenchmarkMegaLayout benchmarks renderMegaLayout with varying pane counts.
// Target: <50ms initial, <200ms for 1000 panes.

func BenchmarkMegaLayout_10(b *testing.B) {
	m := newBenchModel(400, 60, 10) // 400 width triggers TierMega (>=320)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.renderMegaLayout()
	}
}

func BenchmarkMegaLayout_50(b *testing.B) {
	m := newBenchModel(400, 60, 50)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.renderMegaLayout()
	}
}

func BenchmarkMegaLayout_100(b *testing.B) {
	m := newBenchModel(400, 60, 100)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.renderMegaLayout()
	}
}

func BenchmarkMegaLayout_1000(b *testing.B) {
	m := newBenchModel(400, 60, 1000)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.renderMegaLayout()
	}
}

// BenchmarkUltraLayout benchmarks renderUltraLayout with varying pane counts.

func BenchmarkUltraLayout_10(b *testing.B) {
	m := newBenchModel(280, 50, 10) // 280 width triggers TierUltra (240-319)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.renderUltraLayout()
	}
}

func BenchmarkUltraLayout_100(b *testing.B) {
	m := newBenchModel(280, 50, 100)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.renderUltraLayout()
	}
}

func BenchmarkUltraLayout_1000(b *testing.B) {
	m := newBenchModel(280, 50, 1000)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.renderUltraLayout()
	}
}

func BenchmarkPaneList_Wide_1000(b *testing.B) {
	m := newBenchModel(200, 50, 1000)
	listWidth := 90 // emulate wide split list panel

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.renderPaneList(listWidth)
	}
}

func BenchmarkPaneGrid_Compact_1000(b *testing.B) {
	m := newBenchModel(100, 40, 1000) // narrow/compact uses card grid

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.renderPaneGrid()
	}
}

// BenchmarkDashboardView benchmarks View() to verify 60fps capability.
// Target: < 16ms for 60 FPS.
func BenchmarkDashboardView(b *testing.B) {
	m := newBenchModel(200, 50, 20)

	// Simulate window resize
	m.width = 200
	m.height = 50

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.View()
	}
}

// BenchmarkDashboardViewAllocs reports allocation pressure during rendering.
// Target: < 200 allocs/op for smooth performance.
func BenchmarkDashboardViewAllocs(b *testing.B) {
	m := newBenchModel(200, 50, 20)
	m.width = 200
	m.height = 50

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.View()
	}
}

// BenchmarkDashboardViewWide benchmarks View() at wide terminal widths.
func BenchmarkDashboardViewWide(b *testing.B) {
	m := newBenchModel(400, 60, 50)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.View()
	}
}

// BenchmarkDashboardUpdate benchmarks the Update() loop with tick messages.
func BenchmarkDashboardUpdate(b *testing.B) {
	m := newBenchModel(200, 50, 20)
	tickMsg := DashboardTickMsg(time.Now())

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		updated, _ := m.Update(tickMsg)
		next, ok := updated.(Model)
		if !ok {
			b.Fatalf("Update() returned %T, want dashboard.Model", updated)
		}
		m = next
	}
}

// TestViewRenderTime verifies View() stays under 16ms for 60fps.
func TestViewRenderTime(t *testing.T) {
	m := newBenchModel(200, 50, 20)
	m.width = 200
	m.height = 50

	const iterations = 100
	start := time.Now()
	for i := 0; i < iterations; i++ {
		_ = m.View()
	}
	elapsed := time.Since(start)
	avgMs := durationPerIteration(elapsed, iterations, time.Millisecond)

	t.Logf("average View() time: %.2fms (target <16ms for 60fps)", avgMs)
	logPerfResult(t, "dashboard_view_ms", avgMs, "ms", "<16.0")

	if avgMs > 16.0 {
		t.Errorf("SLOW FRAME: View() too slow for 60fps: %.2fms (target <16ms)", avgMs)
	}
}

// TestUpdateLoopPerformance verifies Update() with ticks stays fast.
func TestUpdateLoopPerformance(t *testing.T) {
	m := newBenchModel(200, 50, 20)
	tickMsg := DashboardTickMsg(time.Now())

	const iterations = 1000
	start := time.Now()
	for i := 0; i < iterations; i++ {
		updated, _ := m.Update(tickMsg)
		next, ok := updated.(Model)
		if !ok {
			t.Fatalf("Update() returned %T, want dashboard.Model", updated)
		}
		m = next
	}
	elapsed := time.Since(start)
	avgUs := durationPerIteration(elapsed, iterations, time.Microsecond)

	t.Logf("average Update(tick) time: %.2fμs (target <1000μs)", avgUs)
	logPerfResult(t, "dashboard_update_tick_us", avgUs, "us", "<1000.0")

	if avgUs > 1000.0 {
		t.Errorf("Update() too slow: %.2fμs (target <1000μs)", avgUs)
	}
}

// BenchmarkDashboardWithSprings benchmarks View() with spring animations active.
func BenchmarkDashboardWithSprings(b *testing.B) {
	m := newBenchModel(200, 50, 20)

	// Activate springs by simulating panel changes
	if m.dashboardSprings != nil {
		for i := 0; i < 20; i++ {
			m.dashboardSprings.Set(fmt.Sprintf("panel_%d", i), float64(i*10))
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if m.dashboardSprings != nil {
			m.dashboardSprings.Tick()
		}
		_ = m.View()
	}
}

// TestSpringAnimationOverhead measures the overhead of spring physics.
func TestSpringAnimationOverhead(t *testing.T) {
	m := newBenchModel(200, 50, 20)

	// Baseline without springs
	const iterations = 100
	start := time.Now()
	for i := 0; i < iterations; i++ {
		_ = m.View()
	}
	baselineMs := durationPerIteration(time.Since(start), iterations, time.Millisecond)

	// With active springs
	if m.dashboardSprings != nil {
		for i := 0; i < 20; i++ {
			m.dashboardSprings.Set(fmt.Sprintf("panel_%d", i), float64(i*10))
		}
	}

	start = time.Now()
	for i := 0; i < iterations; i++ {
		if m.dashboardSprings != nil {
			m.dashboardSprings.Tick()
		}
		_ = m.View()
	}
	withSpringsMs := durationPerIteration(time.Since(start), iterations, time.Millisecond)

	overheadMs := withSpringsMs - baselineMs
	t.Logf("baseline View(): %.2fms", baselineMs)
	t.Logf("with springs: %.2fms", withSpringsMs)
	t.Logf("spring overhead: %.2fms", overheadMs)
	logPerfResult(t, "dashboard_spring_overhead_ms", overheadMs, "ms", "<1.0")

	// Spring overhead should be negligible (< 1ms)
	if overheadMs > 1.0 {
		t.Errorf("spring overhead too high: %.2fms (target <1ms)", overheadMs)
	}
}

// newBenchModel builds a dashboard model with synthetic panes for benchmarks.
func newBenchModel(width, height, panes int) Model {
	m := New("bench", "")
	m.width = width
	m.height = height
	m.tier = layout.TierForWidth(width)

	m.panes = make([]tmux.Pane, panes)
	for i := 0; i < panes; i++ {
		agentType := tmux.AgentCodex
		switch i % 3 {
		case 0:
			agentType = tmux.AgentClaude
		case 1:
			agentType = tmux.AgentCodex
		case 2:
			agentType = tmux.AgentGemini
		}
		m.panes[i] = tmux.Pane{
			ID:      fmt.Sprintf("%%%d", i),
			Index:   i,
			Title:   fmt.Sprintf("bench_pane_%04d", i),
			Type:    agentType,
			Variant: "opus",
			Command: "run --long-command --with-flags",
			Width:   width / 2,
			Height:  height / 2,
			Active:  i == 0,
		}

		m.paneStatus[i] = PaneStatus{
			State:          "working",
			ContextPercent: 42.0,
			ContextLimit:   200000,
			ContextTokens:  84000,
		}
	}

	updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	if sized, ok := updated.(Model); ok {
		m = sized
	}
	_ = m.rebuildPaneList()
	m.syncFocusRing()
	m.syncFocusAnimations()
	m.lastActivity = time.Now()

	return m
}

func durationPerIteration(total time.Duration, iterations int, unit time.Duration) float64 {
	if iterations <= 0 || unit <= 0 {
		return 0
	}
	return float64(total) / float64(iterations) / float64(unit)
}

func logPerfResult(t testing.TB, metric string, value float64, unit, target string) {
	t.Helper()

	f, err := os.OpenFile("/tmp/ntm_perf_results.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Logf("perf log open failed: %v", err)
		return
	}
	defer f.Close()

	_, _ = fmt.Fprintf(
		f,
		"%s metric=%s value=%.4f%s target=%s\n",
		time.Now().Format(time.RFC3339),
		metric,
		value,
		unit,
		target,
	)
}
