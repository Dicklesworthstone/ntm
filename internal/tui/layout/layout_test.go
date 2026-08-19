package layout

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestTierForWidth(t *testing.T) {
	tests := []struct {
		width int
		want  Tier
	}{
		{0, TierNarrow},
		{119, TierNarrow},
		{120, TierSplit},
		{121, TierSplit},
		{199, TierSplit},
		{200, TierWide},
		{239, TierWide},
		{240, TierUltra},
		{319, TierUltra},
		{320, TierMega},
		{400, TierMega},
	}

	for _, tt := range tests {
		if got := TierForWidth(tt.width); got != tt.want {
			t.Errorf("TierForWidth(%d) = %v, want %v", tt.width, got, tt.want)
		}
	}
}

func TestSplitProportions(t *testing.T) {
	// Below threshold: should return total,0
	l, r := SplitProportions(100)
	if l != 100 || r != 0 {
		t.Fatalf("SplitProportions(100) = %d,%d want 100,0", l, r)
	}

	// At threshold: ensure budget applied and non-zero panes
	l, r = SplitProportions(140) // avail = 132 -> ~52/80
	if l <= 0 || r <= 0 {
		t.Fatalf("SplitProportions(140) returned zero widths: %d,%d", l, r)
	}
	if l+r > 140 {
		t.Fatalf("SplitProportions(140) sum %d exceeds total 140", l+r)
	}
}

func TestUltraProportions(t *testing.T) {
	// Below threshold falls back to center-only
	l, c, r := UltraProportions(239)
	if l != 0 || r != 0 || c != 239 {
		t.Fatalf("UltraProportions(239) = %d,%d,%d want 0,239,0", l, c, r)
	}

	width := 300 // Ultra tier
	l, c, r = UltraProportions(width)

	total := l + c + r
	expectedTotal := width - 6 // padding budget

	if total != expectedTotal {
		t.Errorf("UltraProportions(%d) total width = %d, want %d", width, total, expectedTotal)
	}

	if l == 0 || c == 0 || r == 0 {
		t.Errorf("UltraProportions(%d) returned zero width panel: %d/%d/%d", width, l, c, r)
	}
}

// TestTierForWidthBoundaries specifically tests the Ultra/Mega boundaries as
// specified in the tier system documentation.
func TestTierForWidthBoundaries(t *testing.T) {
	// Ultra boundary: 239 is TierWide, 240 is TierUltra
	if got := TierForWidth(239); got != TierWide {
		t.Errorf("TierForWidth(239) = %v, want TierWide", got)
	}
	if got := TierForWidth(240); got != TierUltra {
		t.Errorf("TierForWidth(240) = %v, want TierUltra", got)
	}

	// Mega boundary: 319 is TierUltra, 320 is TierMega
	if got := TierForWidth(319); got != TierUltra {
		t.Errorf("TierForWidth(319) = %v, want TierUltra", got)
	}
	if got := TierForWidth(320); got != TierMega {
		t.Errorf("TierForWidth(320) = %v, want TierMega", got)
	}
}

// TestSplitProportionsBoundary tests SplitProportions at exact threshold.
func TestSplitProportionsBoundary(t *testing.T) {
	// Just below threshold (119): should return total, 0
	l, r := SplitProportions(119)
	if l != 119 || r != 0 {
		t.Errorf("SplitProportions(119) = %d,%d want 119,0", l, r)
	}

	// Exactly at threshold (120): should split
	l, r = SplitProportions(120)
	if l <= 0 || r <= 0 {
		t.Errorf("SplitProportions(120) returned zero width: %d,%d", l, r)
	}
	if l+r > 120 {
		t.Errorf("SplitProportions(120) sum %d exceeds total", l+r)
	}
}

// TestUltraProportionsBoundary tests UltraProportions at exact thresholds.
func TestUltraProportionsBoundary(t *testing.T) {
	// At 239 (below Ultra threshold): center-only
	l, c, r := UltraProportions(239)
	if l != 0 || r != 0 || c != 239 {
		t.Errorf("UltraProportions(239) = %d,%d,%d want 0,239,0", l, c, r)
	}

	// At 240 (exactly Ultra threshold): should give 3-panel
	l, c, r = UltraProportions(240)
	if l == 0 || c == 0 || r == 0 {
		t.Errorf("UltraProportions(240) returned zero panel: %d,%d,%d", l, c, r)
	}
	total := l + c + r
	expectedTotal := 240 - 6 // padding budget
	if total != expectedTotal {
		t.Errorf("UltraProportions(240) total = %d, want %d", total, expectedTotal)
	}
}

// TestProportionsSmallValues tests proportion functions with edge case inputs.
func TestProportionsSmallValues(t *testing.T) {
	// Test SplitProportions with very small values
	l, r := SplitProportions(0)
	if l != 0 || r != 0 {
		t.Errorf("SplitProportions(0) = %d,%d want 0,0", l, r)
	}

	l, r = SplitProportions(5)
	if l != 5 || r != 0 {
		t.Errorf("SplitProportions(5) = %d,%d want 5,0", l, r)
	}

	// Test UltraProportions with very small values
	ul, uc, ur := UltraProportions(0)
	if ul != 0 || uc != 0 || ur != 0 {
		t.Errorf("UltraProportions(0) = %d,%d,%d want 0,0,0", ul, uc, ur)
	}
}

// TestTierConstants verifies tier constant values match thresholds.
func TestTierConstants(t *testing.T) {
	// Verify threshold constants
	if SplitViewThreshold != 120 {
		t.Errorf("SplitViewThreshold = %d, want 120", SplitViewThreshold)
	}
	if WideViewThreshold != 200 {
		t.Errorf("WideViewThreshold = %d, want 200", WideViewThreshold)
	}
	if UltraWideViewThreshold != 240 {
		t.Errorf("UltraWideViewThreshold = %d, want 240", UltraWideViewThreshold)
	}
	if MegaWideViewThreshold != 320 {
		t.Errorf("MegaWideViewThreshold = %d, want 320", MegaWideViewThreshold)
	}

	// Verify tier enum values
	if TierNarrow != 0 {
		t.Errorf("TierNarrow = %d, want 0", TierNarrow)
	}
	if TierSplit != 1 {
		t.Errorf("TierSplit = %d, want 1", TierSplit)
	}
	if TierWide != 2 {
		t.Errorf("TierWide = %d, want 2", TierWide)
	}
	if TierUltra != 4 {
		t.Errorf("TierUltra = %d, want 4", TierUltra)
	}
	if TierMega != 5 {
		t.Errorf("TierMega = %d, want 5", TierMega)
	}
}

// TestTruncatePaneTitle tests the smart truncation function for pane titles
// that preserves the agent suffix (e.g., __cc_1) to keep panes distinguishable.
func TestTruncatePaneTitle(t *testing.T) {
	tests := []struct {
		name     string
		title    string
		maxWidth int
		want     string
	}{
		{
			name:     "fits without truncation",
			title:    "myproject__cc_1",
			maxWidth: 20,
			want:     "myproject__cc_1",
		},
		{
			name:     "truncates prefix preserves suffix",
			title:    "destructive_command_guard__cc_1",
			maxWidth: 20,
			want:     "destructive_c…__cc_1",
		},
		{
			name:     "truncates long prefix preserves suffix",
			title:    "very_long_project_name__cod_10",
			maxWidth: 18,
			want:     "very_long…__cod_10",
		},
		{
			name:     "multiple panes remain distinguishable",
			title:    "destructive_command_guard__cc_2",
			maxWidth: 20,
			want:     "destructive_c…__cc_2",
		},
		{
			name:     "gemini agent suffix",
			title:    "destructive_command_guard__gmi_3",
			maxWidth: 20,
			want:     "destructive_…__gmi_3",
		},
		{
			name:     "no suffix falls back to standard truncation",
			title:    "just_a_regular_long_name",
			maxWidth: 15,
			want:     "just_a_regular…",
		},
		{
			name:     "empty string",
			title:    "",
			maxWidth: 10,
			want:     "",
		},
		{
			name:     "zero width",
			title:    "myproject__cc_1",
			maxWidth: 0,
			want:     "",
		},
		{
			name:     "suffix only when prefix doesn't fit",
			title:    "x__cc_1",
			maxWidth: 7,
			want:     "x__cc_1",
		},
		{
			name:     "very tight width shows suffix",
			title:    "project__cc_1",
			maxWidth: 10,
			want:     "pro…__cc_1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncatePaneTitle(tt.title, tt.maxWidth)
			if got != tt.want {
				t.Errorf("TruncatePaneTitle(%q, %d) = %q, want %q",
					tt.title, tt.maxWidth, got, tt.want)
			}
		})
	}
}

// TestTruncatePaneTitle_DistinguishableOutput verifies that panes with the same
// project prefix but different agent numbers produce distinguishable output.
func TestTruncatePaneTitle_DistinguishableOutput(t *testing.T) {
	titles := []string{
		"destructive_command_guard__cc_1",
		"destructive_command_guard__cc_2",
		"destructive_command_guard__cc_3",
		"destructive_command_guard__cod_1",
		"destructive_command_guard__gmi_1",
	}

	maxWidth := 20
	results := make(map[string]bool)

	for _, title := range titles {
		truncated := TruncatePaneTitle(title, maxWidth)
		if results[truncated] {
			t.Errorf("duplicate truncated output %q from different titles", truncated)
		}
		results[truncated] = true
	}

	// Verify we got unique outputs for each input
	if len(results) != len(titles) {
		t.Errorf("expected %d unique outputs, got %d", len(titles), len(results))
	}
}

// ============ TierForWidthWithHysteresis tests ============

func TestTierForWidthWithHysteresis_NoChange(t *testing.T) {
	t.Parallel()

	// When new tier equals previous tier, return it directly
	if got := TierForWidthWithHysteresis(150, TierSplit); got != TierSplit {
		t.Errorf("same tier: got %v, want TierSplit", got)
	}
}

func TestTierForWidthWithHysteresis_InvalidPrevTier(t *testing.T) {
	t.Parallel()

	// Invalid previous tier returns the plain TierForWidth result
	if got := TierForWidthWithHysteresis(150, Tier(-1)); got != TierSplit {
		t.Errorf("invalid prev tier: got %v, want TierSplit", got)
	}
	if got := TierForWidthWithHysteresis(150, Tier(99)); got != TierSplit {
		t.Errorf("too-large prev tier: got %v, want TierSplit", got)
	}
}

func TestTierForWidthWithHysteresis_NarrowSticky(t *testing.T) {
	t.Parallel()

	// Was narrow, now at 120 (exactly split threshold) — within hysteresis margin, stays narrow
	if got := TierForWidthWithHysteresis(120, TierNarrow); got != TierNarrow {
		t.Errorf("narrow sticky at 120: got %v, want TierNarrow", got)
	}
	// At 124 (split threshold + margin - 1), still stays narrow
	if got := TierForWidthWithHysteresis(124, TierNarrow); got != TierNarrow {
		t.Errorf("narrow sticky at 124: got %v, want TierNarrow", got)
	}
	// At 125 (split threshold + margin), transitions to split
	if got := TierForWidthWithHysteresis(125, TierNarrow); got != TierSplit {
		t.Errorf("narrow to split at 125: got %v, want TierSplit", got)
	}
}

func TestTierForWidthWithHysteresis_SplitSticky(t *testing.T) {
	t.Parallel()

	// Was split, shrink toward narrow — stays split within margin
	if got := TierForWidthWithHysteresis(116, TierSplit); got != TierSplit {
		t.Errorf("split sticky at 116: got %v, want TierSplit", got)
	}
	// Below (split - margin), transitions to narrow
	if got := TierForWidthWithHysteresis(114, TierSplit); got != TierNarrow {
		t.Errorf("split to narrow at 114: got %v, want TierNarrow", got)
	}

	// Was split, grow toward wide — stays split within margin
	if got := TierForWidthWithHysteresis(200, TierSplit); got != TierSplit {
		t.Errorf("split sticky at 200: got %v, want TierSplit", got)
	}
	if got := TierForWidthWithHysteresis(204, TierSplit); got != TierSplit {
		t.Errorf("split sticky at 204: got %v, want TierSplit", got)
	}
	// Past (wide + margin), transitions to wide
	if got := TierForWidthWithHysteresis(205, TierSplit); got != TierWide {
		t.Errorf("split to wide at 205: got %v, want TierWide", got)
	}
}

func TestTierForWidthWithHysteresis_WideSticky(t *testing.T) {
	t.Parallel()

	// Was wide, shrink toward split
	if got := TierForWidthWithHysteresis(196, TierWide); got != TierWide {
		t.Errorf("wide sticky at 196: got %v, want TierWide", got)
	}
	if got := TierForWidthWithHysteresis(194, TierWide); got != TierSplit {
		t.Errorf("wide to split at 194: got %v, want TierSplit", got)
	}

	// Was wide, grow toward ultra
	if got := TierForWidthWithHysteresis(244, TierWide); got != TierWide {
		t.Errorf("wide sticky at 244: got %v, want TierWide", got)
	}
	if got := TierForWidthWithHysteresis(245, TierWide); got != TierUltra {
		t.Errorf("wide to ultra at 245: got %v, want TierUltra", got)
	}
}

func TestTierForWidthWithHysteresis_UltraSticky(t *testing.T) {
	t.Parallel()

	// Was ultra, shrink toward wide
	if got := TierForWidthWithHysteresis(236, TierUltra); got != TierUltra {
		t.Errorf("ultra sticky at 236: got %v, want TierUltra", got)
	}
	if got := TierForWidthWithHysteresis(234, TierUltra); got != TierWide {
		t.Errorf("ultra to wide at 234: got %v, want TierWide", got)
	}

	// Was ultra, grow toward mega
	if got := TierForWidthWithHysteresis(324, TierUltra); got != TierUltra {
		t.Errorf("ultra sticky at 324: got %v, want TierUltra", got)
	}
	if got := TierForWidthWithHysteresis(325, TierUltra); got != TierMega {
		t.Errorf("ultra to mega at 325: got %v, want TierMega", got)
	}
}

func TestTierForWidthWithHysteresis_MegaSticky(t *testing.T) {
	t.Parallel()

	// Was mega, shrink toward ultra
	if got := TierForWidthWithHysteresis(316, TierMega); got != TierMega {
		t.Errorf("mega sticky at 316: got %v, want TierMega", got)
	}
	if got := TierForWidthWithHysteresis(314, TierMega); got != TierUltra {
		t.Errorf("mega to ultra at 314: got %v, want TierUltra", got)
	}
}

// ============ TruncateWidth tests ============

func TestTruncateWidth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		s        string
		maxWidth int
		suffix   string
	}{
		{"fits unchanged", "hello", 10, "..."},
		{"empty string", "", 10, "..."},
		{"zero maxWidth", "hello", 0, "..."},
		{"negative maxWidth", "hello", -1, "..."},
		{"exact fit", "hi", 2, "..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := TruncateWidth(tt.s, tt.maxWidth, tt.suffix)
			if tt.maxWidth <= 0 {
				if got != "" {
					t.Errorf("TruncateWidth(%q, %d, %q) = %q, want empty", tt.s, tt.maxWidth, tt.suffix, got)
				}
				return
			}
			// Result should fit in maxWidth
			w := lipgloss.Width(got)
			if w > tt.maxWidth {
				t.Errorf("TruncateWidth(%q, %d, %q) = %q (width=%d), exceeds max", tt.s, tt.maxWidth, tt.suffix, got, w)
			}
		})
	}
}

func TestTruncateWidth_Truncation(t *testing.T) {
	t.Parallel()

	// Long string that needs truncation
	got := TruncateWidth("hello world this is long", 10, "…")
	w := lipgloss.Width(got)
	if w > 10 {
		t.Errorf("result %q has width %d, want <= 10", got, w)
	}
	if got == "" {
		t.Error("should not return empty for non-empty input with width > 0")
	}
}

func TestTruncateWidth_SuffixTooWide(t *testing.T) {
	t.Parallel()

	// Suffix wider than maxWidth — falls back to hard truncation
	got := TruncateWidth("hello world", 2, "...")
	w := lipgloss.Width(got)
	if w > 2 {
		t.Errorf("result %q has width %d, want <= 2", got, w)
	}
}

func TestTruncateWidthDefault(t *testing.T) {
	t.Parallel()

	got := TruncateWidthDefault("hello world this is long", 10)
	w := lipgloss.Width(got)
	if w > 10 {
		t.Errorf("result %q has width %d, want <= 10", got, w)
	}

	// Short string passes through
	got = TruncateWidthDefault("hi", 10)
	if got != "hi" {
		t.Errorf("short string should pass through, got %q", got)
	}
}
