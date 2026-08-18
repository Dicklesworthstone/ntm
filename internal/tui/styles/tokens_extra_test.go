package styles

import (
	"testing"
)

// ---------------------------------------------------------------------------
// UltraWide — 0% → 100%
// ---------------------------------------------------------------------------

func TestUltraWide(t *testing.T) {
	t.Parallel()

	tokens := UltraWide()
	if tokens.Spacing.MD <= 0 {
		t.Error("expected positive MD spacing")
	}
	// UltraWide should have larger values than Default
	def := DefaultTokens()
	if tokens.Spacing.MD < def.Spacing.MD {
		t.Errorf("UltraWide spacing MD (%d) should be >= DefaultTokens MD (%d)",
			tokens.Spacing.MD, def.Spacing.MD)
	}
}

// ---------------------------------------------------------------------------
// GetLayoutMode — 0% → 100%
// ---------------------------------------------------------------------------

func TestGetLayoutMode(t *testing.T) {
	t.Parallel()

	bp := DefaultBreakpoints

	tests := []struct {
		name  string
		width int
		want  LayoutMode
	}{
		{"narrow", bp.XS - 1, LayoutCompact},
		{"at_xs", bp.XS, LayoutDefault},
		{"default", bp.MD - 1, LayoutDefault},
		{"at_md", bp.MD, LayoutSpacious},
		{"spacious", bp.Wide - 1, LayoutSpacious},
		{"ultra_wide", bp.Wide, LayoutUltraWide},
		{"very_wide", 300, LayoutUltraWide},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := GetLayoutMode(tt.width)
			if got != tt.want {
				t.Errorf("GetLayoutMode(%d) = %d, want %d", tt.width, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// AdaptiveCardDimensions — 0% → 100%
// ---------------------------------------------------------------------------

func TestAdaptiveCardDimensions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		total      int
		minCard    int
		maxCard    int
		gap        int
		wantWidth  int
		wantPerRow int
	}{
		{"zero_width", 0, 20, 40, 2, 1, 1},
		{"zero_min", 100, 0, 40, 2, 1, 1},
		{"zero_max", 100, 20, 0, 2, 1, 1},
		{"narrow", 15, 20, 40, 2, 15, 1},
		{"single_card", 20, 20, 40, 2, 20, 1},
		{"two_cards", 44, 20, 40, 2, 21, 2},
		// Wide enough for max-width clamping branch:
		// total=100, min=40, max=45, gap=2: initial cards=(102/42)=2, width=(100-2)/2=49 > 45
		// After clamp: cards=(102/47)=2, width=45
		{"max_clamp", 100, 40, 45, 2, 45, 2},
		// Negative gap produces cardsPerRow<1 after initial calc
		{"negative_gap_guard", 10, 5, 40, -100, 10, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			width, perRow := AdaptiveCardDimensions(tt.total, tt.minCard, tt.maxCard, tt.gap)
			if width != tt.wantWidth {
				t.Errorf("AdaptiveCardDimensions(%d, %d, %d, %d) width = %d, want %d",
					tt.total, tt.minCard, tt.maxCard, tt.gap, width, tt.wantWidth)
			}
			if perRow != tt.wantPerRow {
				t.Errorf("AdaptiveCardDimensions(%d, %d, %d, %d) perRow = %d, want %d",
					tt.total, tt.minCard, tt.maxCard, tt.gap, perRow, tt.wantPerRow)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Style builder functions — 0% → 100%
// ---------------------------------------------------------------------------
