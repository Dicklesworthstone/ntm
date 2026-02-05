package styles

import (
	"testing"
)

// ---------------------------------------------------------------------------
// ShimmerProgressBar — 0% → 100%
// ---------------------------------------------------------------------------

func TestShimmerProgressBar(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		percent float64
		width   int
		filled  string
		empty   string
		tick    int
		colors  []string
	}{
		{"zero width", 0.5, 0, "█", " ", 0, nil},
		{"negative width", 0.5, -5, "█", " ", 0, nil},
		{"zero percent", 0.0, 10, "█", " ", 0, nil},
		{"full percent", 1.0, 10, "█", " ", 0, nil},
		{"negative percent clamps", -0.5, 10, "█", " ", 0, nil},
		{"over 100% clamps", 1.5, 10, "█", " ", 0, nil},
		{"half filled", 0.5, 10, "█", " ", 0, nil},
		{"with tick (shimmer)", 0.5, 10, "█", " ", 5, nil},
		{"custom colors", 0.5, 10, "█", " ", 0, []string{"#FF0000", "#00FF00", "#0000FF"}},
		{"single color defaults", 0.5, 10, "█", " ", 0, []string{"#FF0000"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ShimmerProgressBar(tt.percent, tt.width, tt.filled, tt.empty, tt.tick, tt.colors...)
			// Non-empty result for non-zero width
			if tt.width > 0 && got == "" {
				t.Errorf("ShimmerProgressBar() returned empty for width %d", tt.width)
			}
			if tt.width <= 0 && got != "" {
				t.Errorf("ShimmerProgressBar() returned non-empty %q for width %d", got, tt.width)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// MiniBar branches — cover MidHigh palette branch
// ---------------------------------------------------------------------------

func TestMiniBar_MidHighBranch(t *testing.T) {
	t.Parallel()

	// MidHigh branch: value in [0.60, 0.80) with MidHigh set
	palette := DefaultMiniBarPalette()
	palette.MidHigh = "#AABBCC"

	got := MiniBar(0.65, 10, palette)
	if got == "" {
		t.Error("MiniBar() returned empty for valid input with MidHigh palette")
	}
}

func TestMiniBar_MidHighEmpty(t *testing.T) {
	t.Parallel()

	// MidHigh empty: value in [0.60, 0.80) without MidHigh, falls back to Mid
	palette := DefaultMiniBarPalette()
	palette.MidHigh = ""

	got := MiniBar(0.65, 10, palette)
	if got == "" {
		t.Error("MiniBar() returned empty for valid input without MidHigh")
	}
}
