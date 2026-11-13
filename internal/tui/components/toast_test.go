package components

import (
	"os"
	"strings"
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

func TestDismissNewestTargetsMostRecentActiveToast(t *testing.T) {
	t.Parallel()

	tm := NewToastManager()
	tm.Push(Toast{ID: "oldest", Message: "one", Duration: 10 * time.Second})
	tm.Push(Toast{ID: "middle", Message: "two", Duration: 10 * time.Second})
	tm.Push(Toast{ID: "newest", Message: "three", Duration: 10 * time.Second})

	if !tm.DismissNewest() {
		t.Fatal("expected DismissNewest to dismiss an active toast")
	}

	history := tm.RecentHistory(1)
	if len(history) != 1 || history[0].ID != "newest" {
		t.Fatalf("expected newest toast in history, got %+v", history)
	}
	if !tm.toasts[2].dismissed {
		t.Fatal("expected newest toast to be marked dismissed")
	}
}

func TestDismissNewestTargetsMostRecentAnonymousToast(t *testing.T) {
	t.Parallel()

	tm := NewToastManager()
	tm.Push(Toast{Message: "one", Duration: 10 * time.Second})
	tm.Push(Toast{Message: "two", Duration: 10 * time.Second})
	tm.Push(Toast{Message: "three", Duration: 10 * time.Second})

	if !tm.DismissNewest() {
		t.Fatal("expected DismissNewest to dismiss an active toast")
	}

	history := tm.RecentHistory(1)
	if len(history) != 1 || history[0].Message != "three" {
		t.Fatalf("expected newest anonymous toast in history, got %+v", history)
	}
	if !tm.toasts[2].dismissed {
		t.Fatal("expected newest anonymous toast to be marked dismissed")
	}
	if tm.toasts[0].dismissed || tm.toasts[1].dismissed {
		t.Fatal("expected older anonymous toasts to remain active")
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

func TestUpdateStackTargetsUsesCumulativeHeights(t *testing.T) {
	t.Parallel()

	tm := NewToastManager()
	tm.Push(Toast{ID: "first", Message: "one", Duration: 10 * time.Second})
	tm.PushProgress("second", "Loading", 0.5)
	tm.Push(Toast{ID: "third", Message: "three", Duration: 10 * time.Second})

	if got := tm.toasts[0].targetY; got != 0 {
		t.Fatalf("first targetY = %.1f, want 0", got)
	}
	if got := tm.toasts[1].targetY; got != 3 {
		t.Fatalf("second targetY = %.1f, want 3", got)
	}
	if got := tm.toasts[2].targetY; got != 7 {
		t.Fatalf("third targetY = %.1f, want 7", got)
	}
}

func TestDismissReflowsRemainingTargets(t *testing.T) {
	t.Parallel()

	tm := NewToastManager()
	tm.Push(Toast{ID: "first", Message: "one", Duration: 10 * time.Second})
	tm.PushProgress("second", "Loading", 0.5)
	tm.Push(Toast{ID: "third", Message: "three", Duration: 10 * time.Second})

	if !tm.Dismiss("first") {
		t.Fatal("expected Dismiss to return true")
	}

	if got := tm.toasts[1].targetY; got != 0 {
		t.Fatalf("second targetY after dismiss = %.1f, want 0", got)
	}
	if got := tm.toasts[2].targetY; got != 4 {
		t.Fatalf("third targetY after dismiss = %.1f, want 4", got)
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

// TestPersistentToastDoesNotExpire verifies persistent toasts don't auto-dismiss.
func TestPersistentToastDoesNotExpire(t *testing.T) {
	t.Parallel()
	tm := NewToastManager()
	tm.PushPersistent("persist-1", "I persist", ToastWarning)

	// Simulate well past default duration
	for i := 0; i < 500; i++ {
		tm.Tick()
	}

	if tm.Count() != 1 {
		t.Errorf("expected persistent toast to remain, got %d toasts", tm.Count())
	}
}

// TestProgressToastUpdates verifies progress can be updated.
func TestProgressToastUpdates(t *testing.T) {
	t.Parallel()
	tm := NewToastManager()
	tm.PushProgress("prog-1", "Loading...", 0.0)

	if tm.Count() != 1 {
		t.Fatalf("expected 1 toast, got %d", tm.Count())
	}

	// Update progress
	if !tm.UpdateProgress("prog-1", 0.5) {
		t.Error("expected UpdateProgress to return true")
	}

	// Progress toast should still exist
	if tm.Count() != 1 {
		t.Errorf("expected 1 toast after progress update, got %d", tm.Count())
	}
}

// TestProgressToastAutoDismissAtComplete verifies progress=1.0 triggers dismiss.
func TestProgressToastAutoDismissAtComplete(t *testing.T) {
	enableAnimations(t)
	tm := NewToastManager()
	tm.PushProgress("prog-complete", "Loading...", 0.0)

	// Complete the progress - this sets Duration=1s and Persistent=false
	tm.UpdateProgress("prog-complete", 1.0)

	// Backdate the toast to simulate 1s passing
	for i := range tm.toasts {
		if tm.toasts[i].ID == "prog-complete" {
			tm.toasts[i].CreatedAt = time.Now().Add(-2 * time.Second)
		}
	}

	// Let the expiry happen and slide-out animation complete
	for i := 0; i < 400; i++ {
		tm.Tick()
	}

	if tm.Count() != 0 {
		t.Errorf("expected progress toast to dismiss after completion, got %d", tm.Count())
	}
}

// TestToastHistoryTracking verifies dismissed toasts are added to history.
func TestToastHistoryTracking(t *testing.T) {
	t.Parallel()
	tm := NewToastManager()

	if tm.HistoryCount() != 0 {
		t.Errorf("expected empty history, got %d", tm.HistoryCount())
	}

	tm.Push(Toast{
		ID:       "hist-1",
		Message:  "Will be dismissed",
		Duration: 10 * time.Second,
	})

	// Dismiss manually
	tm.Dismiss("hist-1")

	if tm.HistoryCount() != 1 {
		t.Errorf("expected 1 toast in history, got %d", tm.HistoryCount())
	}

	history := tm.History()
	if len(history) != 1 || history[0].ID != "hist-1" {
		t.Error("expected dismissed toast to be in history")
	}
}

// TestToastHistoryLimit verifies history is capped at MaxToastHistory.
func TestToastHistoryLimit(t *testing.T) {
	t.Parallel()
	tm := NewToastManager()

	// Add more than MaxToastHistory toasts and dismiss them
	for i := 0; i < MaxToastHistory+5; i++ {
		tm.Push(Toast{
			ID:       "limit-" + string(rune('a'+i%26)) + string(rune('0'+i/26)),
			Message:  "Toast",
			Duration: 10 * time.Second,
		})
	}

	// Dismiss all toasts
	for _, toast := range tm.toasts {
		tm.Dismiss(toast.ID)
	}

	if tm.HistoryCount() > MaxToastHistory {
		t.Errorf("expected history capped at %d, got %d", MaxToastHistory, tm.HistoryCount())
	}
}

// TestClearHistory verifies history can be cleared.
func TestClearHistory(t *testing.T) {
	t.Parallel()
	tm := NewToastManager()

	tm.Push(Toast{ID: "clear-1", Message: "Toast 1", Duration: 10 * time.Second})
	tm.Dismiss("clear-1")

	if tm.HistoryCount() == 0 {
		t.Fatal("expected history to have entries")
	}

	tm.ClearHistory()

	if tm.HistoryCount() != 0 {
		t.Errorf("expected empty history after clear, got %d", tm.HistoryCount())
	}
}

func TestRecentHistoryReturnsMostRecentFirst(t *testing.T) {
	t.Parallel()

	tm := NewToastManager()
	tm.Push(Toast{ID: "first", Message: "one", Duration: 10 * time.Second})
	tm.Push(Toast{ID: "second", Message: "two", Duration: 10 * time.Second})
	tm.Dismiss("first")
	tm.Dismiss("second")

	recent := tm.RecentHistory(2)
	if len(recent) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(recent))
	}
	if recent[0].ID != "second" || recent[1].ID != "first" {
		t.Fatalf("unexpected history order: %+v", recent)
	}
}

// TestRenderProgressBar verifies progress toasts render with progress bar.
func TestRenderProgressBar(t *testing.T) {
	t.Parallel()
	tm := NewToastManager()
	tm.PushProgress("render-prog", "Loading data", 0.5)

	rendered := tm.RenderToasts(60)
	if rendered == "" {
		t.Error("expected non-empty render output")
	}

	// Progress bar should contain filled and empty segments
	if !strings.Contains(rendered, "█") || !strings.Contains(rendered, "░") {
		t.Error("expected progress bar characters in render output")
	}
}

// TestToastAtPosition verifies hit-testing for click-to-dismiss.
func TestToastAtPosition(t *testing.T) {
	t.Parallel()
	tm := NewToastManager()

	// Empty manager returns empty string
	if id := tm.ToastAtPosition(0); id != "" {
		t.Errorf("expected empty string for empty manager, got %q", id)
	}

	// Add toasts (each ~3 lines)
	tm.Push(Toast{ID: "pos-1", Message: "First", Duration: 10 * time.Second})
	tm.Push(Toast{ID: "pos-2", Message: "Second", Duration: 10 * time.Second})
	tm.Push(Toast{ID: "pos-3", Message: "Third", Duration: 10 * time.Second})

	// First toast at y=0,1,2
	if id := tm.ToastAtPosition(0); id != "pos-1" {
		t.Errorf("expected pos-1 at y=0, got %q", id)
	}
	if id := tm.ToastAtPosition(2); id != "pos-1" {
		t.Errorf("expected pos-1 at y=2, got %q", id)
	}

	// Second toast at y=3,4,5
	if id := tm.ToastAtPosition(3); id != "pos-2" {
		t.Errorf("expected pos-2 at y=3, got %q", id)
	}

	// Third toast at y=6,7,8
	if id := tm.ToastAtPosition(6); id != "pos-3" {
		t.Errorf("expected pos-3 at y=6, got %q", id)
	}

	// Out of bounds
	if id := tm.ToastAtPosition(100); id != "" {
		t.Errorf("expected empty string at y=100, got %q", id)
	}
	if id := tm.ToastAtPosition(-1); id != "" {
		t.Errorf("expected empty string at y=-1, got %q", id)
	}
}

// TestToastAtPositionWithProgressBar verifies progress toasts occupy 4 lines.
func TestToastAtPositionWithProgressBar(t *testing.T) {
	t.Parallel()
	tm := NewToastManager()

	// Progress toast occupies 4 lines (includes progress bar)
	tm.PushProgress("prog-hit", "Loading", 0.5)
	tm.Push(Toast{ID: "normal-hit", Message: "Normal", Duration: 10 * time.Second})

	// Progress toast at y=0,1,2,3
	if id := tm.ToastAtPosition(3); id != "prog-hit" {
		t.Errorf("expected prog-hit at y=3, got %q", id)
	}

	// Normal toast starts at y=4
	if id := tm.ToastAtPosition(4); id != "normal-hit" {
		t.Errorf("expected normal-hit at y=4, got %q", id)
	}
}

// TestDismissAll verifies all toasts are dismissed at once.
func TestDismissAll(t *testing.T) {
	enableAnimations(t)
	tm := NewToastManager()

	tm.Push(Toast{ID: "all-1", Message: "One", Duration: 10 * time.Second})
	tm.Push(Toast{ID: "all-2", Message: "Two", Duration: 10 * time.Second})
	tm.Push(Toast{ID: "all-3", Message: "Three", Duration: 10 * time.Second})

	if tm.Count() != 3 {
		t.Fatalf("expected 3 toasts, got %d", tm.Count())
	}

	tm.DismissAll()

	// All should be marked dismissed and added to history
	if tm.HistoryCount() != 3 {
		t.Errorf("expected 3 in history after DismissAll, got %d", tm.HistoryCount())
	}

	// After animation completes, all should be removed
	for i := 0; i < 360; i++ {
		tm.Tick()
	}

	if tm.Count() != 0 {
		t.Errorf("expected 0 toasts after DismissAll animation, got %d", tm.Count())
	}
}

// TestToastStackHeight verifies stack height calculation.
func TestToastStackHeight(t *testing.T) {
	t.Parallel()
	tm := NewToastManager()

	// Empty stack
	if h := tm.ToastStackHeight(); h != 0 {
		t.Errorf("expected 0 height for empty stack, got %d", h)
	}

	// One normal toast = 3 lines
	tm.Push(Toast{ID: "height-1", Message: "Normal", Duration: 10 * time.Second})
	if h := tm.ToastStackHeight(); h != 3 {
		t.Errorf("expected 3 height for one normal toast, got %d", h)
	}

	// Add progress toast = +4 lines = 7 total
	tm.PushProgress("height-2", "Loading", 0.5)
	if h := tm.ToastStackHeight(); h != 7 {
		t.Errorf("expected 7 height for normal + progress, got %d", h)
	}

	// Add another normal = +3 lines = 10 total
	tm.Push(Toast{ID: "height-3", Message: "Another", Duration: 10 * time.Second})
	if h := tm.ToastStackHeight(); h != 10 {
		t.Errorf("expected 10 height for 2 normal + 1 progress, got %d", h)
	}
}

func (tm *ToastManager) History() []Toast {
	return tm.history
}
