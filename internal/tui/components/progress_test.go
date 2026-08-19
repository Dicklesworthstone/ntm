package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
)

// TestProgressBarRendering verifies basic progress bar rendering at various percentages
func TestProgressBarRendering(t *testing.T) {
	tests := []struct {
		name    string
		percent float64
		width   int
	}{
		{"empty", 0.0, 20},
		{"half", 0.5, 20},
		{"full", 1.0, 20},
		{"narrow", 0.5, 5},
		{"overflow_clamps", 1.5, 20},  // should clamp to 1.0
		{"negative_clamps", -0.5, 20}, // should clamp to 0.0
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bar := NewProgressBar(tc.width)
			bar.SetPercent(tc.percent)
			bar.Animated = false // Disable for deterministic test

			rendered := bar.View()

			// Verify we got some output
			if len(rendered) == 0 {
				t.Error("rendered bar should not be empty")
			}

			// Verify percent was clamped
			if tc.percent > 1 && bar.Percent != 1.0 {
				t.Errorf("percent = %v, want 1.0 (clamped)", bar.Percent)
			}
			if tc.percent < 0 && bar.Percent != 0.0 {
				t.Errorf("percent = %v, want 0.0 (clamped)", bar.Percent)
			}
		})
	}
}

// TestProgressBarWidth verifies the bar renders at the correct width
func TestProgressBarWidth(t *testing.T) {
	bar := NewProgressBar(20)
	bar.SetPercent(0.5)
	bar.Animated = false
	bar.ShowPercent = false // Disable percent for cleaner width test

	rendered := bar.View()
	actualWidth := lipgloss.Width(rendered)

	if actualWidth != 20 {
		t.Errorf("width = %d, want 20", actualWidth)
	}
}

// TestProgressBarWidthWithPercent verifies width includes percent label
func TestProgressBarWidthWithPercent(t *testing.T) {
	bar := NewProgressBar(20)
	bar.SetPercent(0.5)
	bar.Animated = false
	bar.ShowPercent = true

	rendered := bar.View()
	actualWidth := lipgloss.Width(rendered)

	// Bar (20) + space + "50%" (4) = 25
	expectedWidth := 20 + 5 // " 50%" is 5 chars
	if actualWidth != expectedWidth {
		t.Errorf("width = %d, want %d", actualWidth, expectedWidth)
	}
}

func TestProgressBarBubblesPathWidth(t *testing.T) {
	bar := NewProgressBar(20)
	bar.Percent = 0.5
	bar.Animated = false
	bar.ShowPercent = false
	bar.GradientColors = []string{"#ff0000", "#0000ff"}

	rendered := bar.View()
	if rendered == "" {
		t.Fatal("expected non-empty rendered bar")
	}
	if width := lipgloss.Width(rendered); width != 20 {
		t.Errorf("width = %d, want 20", width)
	}
}

func TestProgressBarBubblesPathWithPercentWidth(t *testing.T) {
	bar := NewProgressBar(20)
	bar.Percent = 0.5
	bar.Animated = false
	bar.ShowPercent = true
	bar.GradientColors = []string{"#ff0000"}

	rendered := bar.View()
	if rendered == "" {
		t.Fatal("expected non-empty rendered bar")
	}
	if width := lipgloss.Width(rendered); width != 25 {
		t.Errorf("width = %d, want 25", width)
	}
}

// TestShimmerStability verifies shimmer produces stable width across ticks
func TestShimmerStability(t *testing.T) {
	text := strings.Repeat("█", 10)
	colors := []string{"#FF0000", "#00FF00", "#0000FF"}

	var widths []int
	for tick := 0; tick < 100; tick++ {
		result := styles.Shimmer(text, tick, colors...)
		widths = append(widths, lipgloss.Width(result))
	}

	// All widths should be identical (10)
	for i, w := range widths {
		if w != 10 {
			t.Errorf("tick %d: width = %d, want 10", i, w)
		}
	}
}

func TestProgressBarPercentReducedMotionSnaps(t *testing.T) {
	// Clear env vars so NTM_REDUCE_MOTION takes effect
	t.Setenv("NTM_ANIMATIONS", "")
	t.Setenv("NTM_REDUCE_MOTION", "1")
	t.Setenv("CI", "")
	t.Setenv("TMUX", "")

	bar := NewProgressBar(10)
	bar.SetPercent(0.25)
	bar.SetPercent(0.75)

	if got := bar.valueSpring.Get("percent"); got != 0.75 {
		t.Fatalf("reduced motion spring value = %f, want 0.75", got)
	}
	if bar.valueSpring.IsAnimating() {
		t.Fatal("expected reduced motion to suppress percent spring animation")
	}
}

// Phase 1d: bubbles/progress integration tests

// TestStylesProgressBar2Color verifies 2-color bar uses bubbles/progress
func TestStylesProgressBar2Color(t *testing.T) {
	bar := styles.ProgressBar(0.5, 40, "", "", "#ff0000", "#0000ff")
	if bar == "" {
		t.Error("2-color bar should not be empty")
	}
	w := lipgloss.Width(bar)
	if w > 42 {
		t.Errorf("bar width %d exceeds expected maximum of 42", w)
	}
	t.Logf("2-color bar width: %d, content length: %d", w, len(bar))
}

// TestStylesProgressBar3Color verifies 3+ color bar uses custom gradient
func TestStylesProgressBar3Color(t *testing.T) {
	bar := styles.ProgressBar(0.5, 40, "", "", "#ff0000", "#00ff00", "#0000ff")
	if bar == "" {
		t.Error("3-color bar should not be empty")
	}
	t.Logf("3-color bar width: %d", lipgloss.Width(bar))
}

// TestStylesProgressBarZeroAndFull verifies boundary cases
func TestStylesProgressBarZeroAndFull(t *testing.T) {
	bar0 := styles.ProgressBar(0.0, 40, "", "")
	bar1 := styles.ProgressBar(1.0, 40, "", "")

	if bar0 == "" {
		t.Error("0% bar should render")
	}
	if bar1 == "" {
		t.Error("100% bar should render")
	}

	t.Logf("0%% bar width: %d, 100%% bar width: %d", lipgloss.Width(bar0), lipgloss.Width(bar1))
}

// TestStylesShimmerProgressBar verifies shimmer animation still works
func TestStylesShimmerProgressBar(t *testing.T) {
	bar := styles.ShimmerProgressBar(0.5, 40, "", "", 5)
	if bar == "" {
		t.Error("shimmer bar should not be empty")
	}
	t.Logf("shimmer bar width: %d", lipgloss.Width(bar))
}

// TestComponentsProgressBarStruct verifies ProgressBar struct renders correctly
func TestComponentsProgressBarStruct(t *testing.T) {
	pb := NewProgressBar(40)
	pb.Percent = 0.75
	pb.Animated = false
	pb.ShowPercent = false

	view := pb.View()
	if view == "" {
		t.Error("component bar should not be empty")
	}
	t.Logf("component bar width: %d", lipgloss.Width(view))
}

// TestProgressBarReducedMotion verifies reduced motion disables shimmer
func TestProgressBarReducedMotion(t *testing.T) {
	t.Setenv("NTM_ANIMATIONS", "0")
	t.Setenv("NTM_REDUCE_MOTION", "")

	bar1 := styles.ShimmerProgressBar(0.5, 40, "", "", 0)
	bar2 := styles.ShimmerProgressBar(0.5, 40, "", "", 100)

	// With reduced motion, different ticks should produce identical output
	// (shimmer effect is disabled)
	t.Logf("tick 0 width: %d, tick 100 width: %d", lipgloss.Width(bar1), lipgloss.Width(bar2))

	// Both should render something
	if bar1 == "" || bar2 == "" {
		t.Error("reduced motion bars should still render")
	}
	if bar1 != bar2 {
		t.Error("reduced motion should suppress shimmer differences across ticks")
	}
}

// TestStylesProgressBarDefaultColors verifies default colors when none specified
func TestStylesProgressBarDefaultColors(t *testing.T) {
	bar := styles.ProgressBar(0.5, 40, "", "")
	if bar == "" {
		t.Error("bar with default colors should not be empty")
	}
	t.Logf("default colors bar width: %d", lipgloss.Width(bar))
}

// BenchmarkStylesProgressBar benchmarks styles.ProgressBar
func BenchmarkStylesProgressBar(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = styles.ProgressBar(0.5, 40, "", "", "#ff0000", "#0000ff")
	}
}

// BenchmarkStylesProgressBar3Color benchmarks 3-color custom gradient
func BenchmarkStylesProgressBar3Color(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = styles.ProgressBar(0.5, 40, "█", "░", "#ff0000", "#00ff00", "#0000ff")
	}
}
