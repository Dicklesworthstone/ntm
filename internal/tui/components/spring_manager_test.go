package components

import (
	"fmt"
	"math"
	"testing"
	"time"
)

func TestSpringManagerSetAndGet(t *testing.T) {
	enableAnimationsForTest(t)

	sm := NewSpringManager()
	sm.Set("test", 10.0)
	if sm.Get("test") != 0.0 {
		t.Fatalf("initial value = %f, want 0", sm.Get("test"))
	}
	for i := 0; i < 200; i++ {
		sm.Tick()
	}
	if math.Abs(sm.Get("test")-10.0) > 0.5 {
		t.Fatalf("not settled near target: %f", sm.Get("test"))
	}
}

func TestSpringManagerIsAnimating(t *testing.T) {
	enableAnimationsForTest(t)

	sm := NewSpringManager()
	if sm.IsAnimating() {
		t.Fatal("empty manager should not animate")
	}

	sm.Set("test", 10.0)
	if !sm.IsAnimating() {
		t.Fatal("expected manager to animate after Set")
	}

	for i := 0; i < 300; i++ {
		sm.Tick()
	}
	if sm.IsAnimating() {
		t.Fatal("expected manager to settle")
	}
}

func TestSpringManagerDeadZone(t *testing.T) {
	enableAnimationsForTest(t)

	sm := NewSpringManager()
	sm.SetImmediate("test", 50.0)
	sm.SetWithDeadZone("test", 51.0, 5.0, 0.3, 2.0)
	if sm.IsAnimating() {
		t.Fatal("delta within dead zone should not animate")
	}

	// delta = 54 - 51 = 3.0 > deadZone 2.0
	sm.SetWithDeadZone("test", 54.0, 5.0, 0.3, 2.0)
	if !sm.IsAnimating() {
		t.Fatal("delta above dead zone should animate")
	}
}

func TestSpringManagerReducedMotion(t *testing.T) {
	// Clear NTM_ANIMATIONS so NTM_REDUCE_MOTION takes precedence
	t.Setenv("NTM_ANIMATIONS", "")
	t.Setenv("NTM_REDUCE_MOTION", "1")
	t.Setenv("CI", "")
	t.Setenv("TMUX", "")

	sm := NewSpringManager()
	sm.Set("test", 10.0)
	if sm.Get("test") != 10.0 {
		t.Fatalf("reduced motion should snap immediately, got %f", sm.Get("test"))
	}
	if sm.IsAnimating() {
		t.Fatal("reduced motion should suppress animation")
	}
}

func TestSpringManagerMultiple(t *testing.T) {
	enableAnimationsForTest(t)

	sm := NewSpringManager()
	sm.Set("a", 5.0)
	sm.Set("b", 10.0)
	sm.Set("c", 15.0)
	for i := 0; i < 300; i++ {
		sm.Tick()
	}

	for id, target := range map[string]float64{"a": 5.0, "b": 10.0, "c": 15.0} {
		if math.Abs(sm.Get(id)-target) > 0.5 {
			t.Fatalf("%s not settled: %f", id, sm.Get(id))
		}
	}
}

func TestSpringManagerOvershoot(t *testing.T) {
	enableAnimationsForTest(t)

	sm := NewSpringManager()
	sm.SetWithParams("test", 10.0, 5.0, 0.3)

	maxValue := 0.0
	for i := 0; i < 100; i++ {
		sm.Tick()
		if current := sm.Get("test"); current > maxValue {
			maxValue = current
		}
	}

	if maxValue <= 10.0 {
		t.Fatalf("expected underdamped spring to overshoot, max=%f", maxValue)
	}
}

func TestSpringManagerSetImmediate(t *testing.T) {
	enableAnimationsForTest(t)

	sm := NewSpringManager()
	sm.SetImmediate("test", 7.5)
	if sm.Get("test") != 7.5 {
		t.Fatalf("SetImmediate = %f, want 7.5", sm.Get("test"))
	}
	if !sm.IsSettled("test") {
		t.Fatal("SetImmediate should leave the spring settled")
	}
}

func BenchmarkSpringManagerTick20(b *testing.B) {
	sm := NewSpringManager()
	for i := 0; i < 20; i++ {
		sm.Set(fmt.Sprintf("s%d", i), float64(i)*10)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sm.Tick()
	}
}

// BenchmarkSpringManagerTick50 benchmarks Tick() with 50 active springs.
// Target: < 1μs per tick for smooth 60fps animation.
func BenchmarkSpringManagerTick50(b *testing.B) {
	sm := NewSpringManager()
	for i := 0; i < 50; i++ {
		sm.Set(fmt.Sprintf("s%d", i), float64(i)*10)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sm.Tick()
	}
}

// BenchmarkSpringManagerTick100 benchmarks Tick() at higher load.
func BenchmarkSpringManagerTick100(b *testing.B) {
	sm := NewSpringManager()
	for i := 0; i < 100; i++ {
		sm.Set(fmt.Sprintf("s%d", i), float64(i)*10)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sm.Tick()
	}
}

// TestSpringTickPerformance verifies Tick() performance target.
func TestSpringTickPerformance(t *testing.T) {
	sm := NewSpringManager()
	for i := 0; i < 50; i++ {
		sm.Set(fmt.Sprintf("s%d", i), float64(i)*10)
	}

	const iterations = 10000
	start := time.Now()
	for i := 0; i < iterations; i++ {
		sm.Tick()
	}
	elapsed := time.Since(start)
	avgNs := float64(elapsed.Nanoseconds()) / float64(iterations)

	t.Logf("average Tick() time with 50 springs: %.0fns (target <1000ns)", avgNs)

	if avgNs > 1000.0 {
		t.Errorf("Tick() too slow: %.0fns (target <1000ns = 1μs)", avgNs)
	}
}
