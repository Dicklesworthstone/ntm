package components

import (
	"os"
	"testing"
	"time"
)

// enableAnimations sets up the environment for animation tests.
// Must be called at the start of tests that check animation behavior.
func enableAnimations(t *testing.T) {
	t.Helper()
	t.Setenv("NTM_ANIMATIONS", "1")
	t.Setenv("TMUX", "")
	t.Setenv("CI", "")
}

// disableAnimations sets up the environment for reduced motion tests.
func disableAnimations(t *testing.T) {
	t.Helper()
	t.Setenv("NTM_ANIMATIONS", "0")
	t.Setenv("NTM_REDUCE_MOTION", "1")
	t.Setenv("TMUX", "")
	t.Setenv("CI", "")
}

func init() {
	// Ensure tests don't inherit CI/tmux detection that would disable animations
	os.Setenv("NTM_ANIMATIONS", "1")
	os.Setenv("TMUX", "")
	os.Setenv("CI", "")
}

// TestPushInitializesSpringState verifies spring animation is initialized on Push.
func TestPushInitializesSpringState(t *testing.T) {
	enableAnimations(t)
	tm := NewToastManager()
	tm.Push(Toast{
		ID:      "test-1",
		Message: "Hello",
		Level:   ToastInfo,
	})

	if tm.Count() != 1 {
		t.Fatalf("expected 1 toast, got %d", tm.Count())
	}

	// With reduced motion disabled, offset should start at 40 (offscreen right)
	// We can't directly access the toast, but we can verify IsAnimating returns true
	if !tm.IsAnimating() {
		t.Error("expected IsAnimating() to return true for new toast sliding in")
	}
}

// TestToastSlideInAnimation verifies toasts slide in with spring physics.
func TestToastSlideInAnimation(t *testing.T) {
	enableAnimations(t)
	tm := NewToastManager()
	tm.Push(Toast{
		ID:      "slide-in",
		Message: "Sliding in",
		Level:   ToastSuccess,
	})

	// Toast should be animating initially
	if !tm.IsAnimating() {
		t.Error("expected IsAnimating() true immediately after push")
	}

	// Simulate several ticks - offset should decrease toward 0
	for i := 0; i < 30; i++ {
		tm.Tick()
	}

	// After some ticks, animation may still be in progress or complete
	// We just verify no crash and count is still 1
	if tm.Count() != 1 {
		t.Errorf("expected 1 toast after animation, got %d", tm.Count())
	}
}

// TestDismissTriggersSlideOutAnimation verifies Dismiss() starts slide-out animation.
func TestDismissTriggersSlideOutAnimation(t *testing.T) {
	enableAnimations(t)
	tm := NewToastManager()
	tm.Push(Toast{
		ID:       "dismiss-test",
		Message:  "Will be dismissed",
		Level:    ToastWarning,
		Duration: 10 * time.Second, // Long duration so it doesn't auto-expire
	})

	// Let it finish sliding in
	for i := 0; i < 60; i++ {
		tm.Tick()
	}

	// Dismiss the toast
	dismissed := tm.Dismiss("dismiss-test")
	if !dismissed {
		t.Error("expected Dismiss() to return true")
	}

	// Should be animating out now
	if !tm.IsAnimating() {
		t.Error("expected IsAnimating() true after dismiss")
	}

	// Simulate more ticks until animation completes
	// Spring physics with freq=6Hz, damping=0.4 needs ~5s to settle to target=60
	for i := 0; i < 360; i++ {
		tm.Tick()
	}

	// Toast should be removed after slide-out animation completes
	if tm.Count() != 0 {
		t.Errorf("expected 0 toasts after slide-out, got %d", tm.Count())
	}
}

// TestDismissNonexistentToast verifies Dismiss() returns false for unknown ID.
func TestDismissNonexistentToast(t *testing.T) {
	tm := NewToastManager()
	tm.Push(Toast{
		ID:      "existing",
		Message: "I exist",
	})

	if tm.Dismiss("nonexistent") {
		t.Error("expected Dismiss() to return false for unknown ID")
	}

	// Existing toast should still be there
	if tm.Count() != 1 {
		t.Errorf("expected 1 toast, got %d", tm.Count())
	}
}

// TestIsAnimatingReturnsFalseWhenIdle verifies IsAnimating() is false when no animation.
func TestIsAnimatingReturnsFalseWhenIdle(t *testing.T) {
	enableAnimations(t)
	tm := NewToastManager()

	// Empty manager should not be animating
	if tm.IsAnimating() {
		t.Error("expected IsAnimating() false for empty manager")
	}

	tm.Push(Toast{
		ID:      "idle-test",
		Message: "Will settle",
		Level:   ToastInfo,
	})

	// Simulate many ticks until animation settles
	for i := 0; i < 120; i++ {
		tm.Tick()
	}

	// After settling, should not be animating (unless dismissed)
	// Note: Toast may have expired, so check if it exists first
	if tm.Count() > 0 && tm.IsAnimating() {
		t.Error("expected IsAnimating() false after animation settles")
	}
}

// TestToastExpiry verifies toasts are pruned after duration.
func TestToastExpiry(t *testing.T) {
	enableAnimations(t)
	tm := NewToastManager()
	tm.Push(Toast{
		ID:        "expiry-test",
		Message:   "Short lived",
		Level:     ToastError,
		Duration:  50 * time.Millisecond,
		CreatedAt: time.Now().Add(-100 * time.Millisecond), // Already expired
	})

	// First tick should mark it for dismissal
	tm.Tick()

	// Additional ticks for slide-out animation
	for i := 0; i < 120; i++ {
		tm.Tick()
	}

	// Toast should be removed after slide-out
	if tm.Count() != 0 {
		t.Errorf("expected 0 toasts after expiry and slide-out, got %d", tm.Count())
	}
}

func TestToastExpiryReducedMotionRemovesImmediately(t *testing.T) {
	disableAnimations(t)
	tm := NewToastManager()
	tm.Push(Toast{
		ID:        "expiry-reduced-motion",
		Message:   "Short lived",
		Level:     ToastError,
		Duration:  50 * time.Millisecond,
		CreatedAt: time.Now().Add(-100 * time.Millisecond),
	})

	tm.Tick()

	if tm.Count() != 0 {
		t.Errorf("expected expired toast to be removed immediately in reduced motion, got %d", tm.Count())
	}
	if tm.IsAnimating() {
		t.Error("expected no animation in reduced motion")
	}
}

func TestDismissReducedMotionRemovesOnNextTick(t *testing.T) {
	disableAnimations(t)
	tm := NewToastManager()
	tm.Push(Toast{
		ID:       "dismiss-reduced-motion",
		Message:  "Dismiss me",
		Level:    ToastWarning,
		Duration: 10 * time.Second,
	})

	if tm.IsAnimating() {
		t.Error("expected reduced-motion toast to start without animation")
	}
	if !tm.Dismiss("dismiss-reduced-motion") {
		t.Fatal("expected Dismiss() to return true")
	}

	tm.Tick()

	if tm.Count() != 0 {
		t.Errorf("expected dismissed toast to be removed on next tick in reduced motion, got %d", tm.Count())
	}
}

// TestMultipleToastsAnimate verifies multiple toasts animate independently.
func TestMultipleToastsAnimate(t *testing.T) {
	enableAnimations(t)
	tm := NewToastManager()

	// Push multiple toasts
	tm.Push(Toast{ID: "toast-1", Message: "First", Level: ToastInfo})
	tm.Push(Toast{ID: "toast-2", Message: "Second", Level: ToastSuccess})
	tm.Push(Toast{ID: "toast-3", Message: "Third", Level: ToastWarning})

	if tm.Count() != 3 {
		t.Errorf("expected 3 toasts, got %d", tm.Count())
	}

	// All should be animating initially
	if !tm.IsAnimating() {
		t.Error("expected IsAnimating() true with multiple new toasts")
	}

	// Simulate ticks
	for i := 0; i < 60; i++ {
		tm.Tick()
	}

	// Still should have 3 toasts (not expired yet)
	if tm.Count() != 3 {
		t.Errorf("expected 3 toasts after animation, got %d", tm.Count())
	}
}

// TestRenderToastsWithOffset verifies rendering includes animation offset.
func TestRenderToastsWithOffset(t *testing.T) {
	enableAnimations(t)
	tm := NewToastManager()
	tm.Push(Toast{
		ID:      "render-test",
		Message: "Render me",
		Level:   ToastInfo,
	})

	// Render immediately (toast should have offset)
	rendered := tm.RenderToasts(80)
	if rendered == "" {
		t.Error("expected non-empty render output")
	}

	// We can't easily verify the offset visually, but we can ensure no crash
	// and that the output contains the message
	if len(rendered) < 10 {
		t.Error("expected substantial render output")
	}
}

// BenchmarkToastTickFourToasts benchmarks Tick() with 4 toasts.
func BenchmarkToastTickFourToasts(b *testing.B) {
	tm := NewToastManager()
	for i := 0; i < 4; i++ {
		tm.Push(Toast{
			ID:       "bench-" + string(rune('a'+i)),
			Message:  "Benchmark toast",
			Level:    ToastLevel(i % 4),
			Duration: 10 * time.Second,
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tm.Tick()
	}
}
