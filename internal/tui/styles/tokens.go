// Package styles provides design tokens for consistent spacing and layout.
// This file defines a design token system for the NTM TUI.
package styles

import ()

// Spacing defines consistent spacing values (in terminal character units).
// Use these instead of raw numbers for consistent UI spacing.
type Spacing struct {
	None int // 0
	XS   int // 1 - Extra small
	SM   int // 2 - Small
	MD   int // 3 - Medium (default)
	LG   int // 4 - Large
	XL   int // 6 - Extra large
	XXL  int // 8 - Extra extra large
}

// DefaultSpacing provides standard spacing values.
var DefaultSpacing = Spacing{
	None: 0,
	XS:   1,
	SM:   2,
	MD:   3,
	LG:   4,
	XL:   6,
	XXL:  8,
}

// Size defines dimension tokens for widths and heights.
type Size struct {
	XS  int // 20 - Extra small component
	SM  int // 30 - Small component
	MD  int // 40 - Medium component
	LG  int // 60 - Large component
	XL  int // 80 - Extra large component
	XXL int // 100 - Full width typically
}

// DefaultSize provides standard size values.
var DefaultSize = Size{
	XS:  20,
	SM:  30,
	MD:  40,
	LG:  60,
	XL:  80,
	XXL: 100,
}

// BorderRadius defines corner rounding options.
type BorderRadius int

const (
	RadiusNone   BorderRadius = iota // No rounding (sharp corners)
	RadiusSmall                      // Slight rounding
	RadiusMedium                     // Standard rounding
	RadiusLarge                      // Heavy rounding
	RadiusFull                       // Pill/capsule shape
)

// Typography defines text sizing and styling tokens.
type Typography struct {
	// Font sizes (conceptual - terminals use fixed sizes)
	SizeXS  int // For hints, captions
	SizeSM  int // For secondary text
	SizeMD  int // For body text (default)
	SizeLG  int // For subheadings
	SizeXL  int // For headings
	SizeXXL int // For titles

	// Line heights (number of empty lines)
	LineHeightTight  int // 0 - Compact
	LineHeightNormal int // 1 - Standard
	LineHeightLoose  int // 2 - Spacious
}

// DefaultTypography provides standard typography values.
var DefaultTypography = Typography{
	SizeXS:  8,
	SizeSM:  10,
	SizeMD:  12,
	SizeLG:  14,
	SizeXL:  16,
	SizeXXL: 20,

	LineHeightTight:  0,
	LineHeightNormal: 1,
	LineHeightLoose:  2,
}

// LayoutTokens defines common layout measurements.
type LayoutTokens struct {
	// Content margins
	MarginPage    int // Outer page margin
	MarginSection int // Between major sections
	MarginItem    int // Between list items

	// Padding values
	PaddingCard   int // Inside cards/boxes
	PaddingInline int // Inline element padding
	PaddingInput  int // Input field padding

	// Component dimensions
	IconWidth      int // Width for icon columns
	LabelWidth     int // Width for label columns
	BadgeMinWidth  int // Minimum badge width
	InputMinWidth  int // Minimum input width
	ButtonMinWidth int // Minimum button width

	// List dimensions
	ListIndent      int // Nested list indentation
	ListItemPadding int // List item internal padding
	ListGutterWidth int // Space between columns

	// Table dimensions
	TableColumnGap  int // Gap between table columns
	TableRowPadding int // Padding above/below rows

	// Modal/Dialog dimensions
	ModalWidth     int // Standard modal width
	ModalMinHeight int // Minimum modal height

	// Dashboard dimensions
	DashCardWidth  int // Dashboard card width
	DashCardHeight int // Dashboard card height
	DashGridGap    int // Gap between dashboard cards
}

// DefaultLayout provides standard layout token values.
var DefaultLayout = LayoutTokens{
	// Margins
	MarginPage:    2,
	MarginSection: 2,
	MarginItem:    1,

	// Padding
	PaddingCard:   2,
	PaddingInline: 1,
	PaddingInput:  1,

	// Component dimensions
	IconWidth:      3,
	LabelWidth:     12,
	BadgeMinWidth:  6,
	InputMinWidth:  20,
	ButtonMinWidth: 8,

	// List dimensions
	ListIndent:      2,
	ListItemPadding: 1,
	ListGutterWidth: 2,

	// Table dimensions
	TableColumnGap:  2,
	TableRowPadding: 0,

	// Modal dimensions
	ModalWidth:     60,
	ModalMinHeight: 10,

	// Dashboard dimensions
	DashCardWidth:  25,
	DashCardHeight: 5,
	DashGridGap:    1,
}

// AnimationTokens defines timing values for animations.
type AnimationTokens struct {
	// Tick intervals (milliseconds)
	TickFast   int // Fast animations (spinners)
	TickNormal int // Normal animations (progress)
	TickSlow   int // Slow animations (pulse)

	// Frame counts
	FramesFast   int // Frames per fast animation cycle
	FramesNormal int // Frames per normal cycle
	FramesSlow   int // Frames per slow cycle
}

// DefaultAnimation provides standard animation timing values.
var DefaultAnimation = AnimationTokens{
	TickFast:   100,
	TickNormal: 250,
	TickSlow:   500,

	FramesFast:   8,
	FramesNormal: 10,
	FramesSlow:   4,
}

// ZIndex defines stacking order for overlapping elements.
type ZIndex int

const (
	ZIndexBase     ZIndex = 0   // Base layer (content)
	ZIndexFloating ZIndex = 10  // Floating elements (dropdowns)
	ZIndexModal    ZIndex = 20  // Modal dialogs
	ZIndexOverlay  ZIndex = 30  // Full-screen overlays
	ZIndexTooltip  ZIndex = 40  // Tooltips (highest)
	ZIndexMax      ZIndex = 100 // Maximum z-index
)

// Breakpoints defines responsive width thresholds.
// Inspired by beads_viewer's ultra-wide display optimizations.
type Breakpoints struct {
	XS        int // Extra small (< 40 cols)
	SM        int // Small (40-60 cols)
	MD        int // Medium (60-80 cols)
	LG        int // Large (80-120 cols)
	XL        int // Extra large (120-160 cols)
	Wide      int // Wide displays (160-200 cols)
	UltraWide int // Ultra-wide displays (> 200 cols)
}

// DefaultBreakpoints provides standard responsive breakpoints.
// These thresholds are optimized for modern high-resolution displays.
var DefaultBreakpoints = Breakpoints{
	XS:        40,
	SM:        60,
	MD:        80,
	LG:        120,
	XL:        160,
	Wide:      200,
	UltraWide: 240,
}

// DesignTokens aggregates all design tokens into a single structure.
type DesignTokens struct {
	Spacing     Spacing
	Size        Size
	Typography  Typography
	Layout      LayoutTokens
	Animation   AnimationTokens
	Breakpoints Breakpoints
}

// DefaultTokens returns the default design token configuration.
func DefaultTokens() DesignTokens {
	return DesignTokens{
		Spacing:     DefaultSpacing,
		Size:        DefaultSize,
		Typography:  DefaultTypography,
		Layout:      DefaultLayout,
		Animation:   DefaultAnimation,
		Breakpoints: DefaultBreakpoints,
	}
}

// Compact returns a compact token configuration for space-constrained UIs.
func Compact() DesignTokens {
	return DesignTokens{
		Spacing: Spacing{
			None: 0,
			XS:   0,
			SM:   1,
			MD:   1,
			LG:   2,
			XL:   3,
			XXL:  4,
		},
		Size: Size{
			XS:  15,
			SM:  20,
			MD:  30,
			LG:  45,
			XL:  60,
			XXL: 80,
		},
		Typography: Typography{
			SizeXS:           6,
			SizeSM:           8,
			SizeMD:           10,
			SizeLG:           12,
			SizeXL:           14,
			SizeXXL:          16,
			LineHeightTight:  0,
			LineHeightNormal: 0,
			LineHeightLoose:  1,
		},
		Layout: LayoutTokens{
			MarginPage:      1,
			MarginSection:   1,
			MarginItem:      0,
			PaddingCard:     1,
			PaddingInline:   0,
			PaddingInput:    0,
			IconWidth:       2,
			LabelWidth:      10,
			BadgeMinWidth:   4,
			InputMinWidth:   15,
			ButtonMinWidth:  6,
			ListIndent:      1,
			ListItemPadding: 0,
			ListGutterWidth: 1,
			TableColumnGap:  1,
			TableRowPadding: 0,
			ModalWidth:      50,
			ModalMinHeight:  8,
			DashCardWidth:   20,
			DashCardHeight:  4,
			DashGridGap:     1,
		},
		Animation:   DefaultAnimation,
		Breakpoints: DefaultBreakpoints,
	}
}

// Spacious returns a spacious token configuration for roomy UIs.
func Spacious() DesignTokens {
	return DesignTokens{
		Spacing: Spacing{
			None: 0,
			XS:   2,
			SM:   3,
			MD:   4,
			LG:   6,
			XL:   8,
			XXL:  12,
		},
		Size: Size{
			XS:  25,
			SM:  40,
			MD:  55,
			LG:  75,
			XL:  100,
			XXL: 120,
		},
		Typography: Typography{
			SizeXS:           10,
			SizeSM:           12,
			SizeMD:           14,
			SizeLG:           18,
			SizeXL:           22,
			SizeXXL:          28,
			LineHeightTight:  1,
			LineHeightNormal: 2,
			LineHeightLoose:  3,
		},
		Layout: LayoutTokens{
			MarginPage:      4,
			MarginSection:   3,
			MarginItem:      2,
			PaddingCard:     3,
			PaddingInline:   2,
			PaddingInput:    2,
			IconWidth:       4,
			LabelWidth:      16,
			BadgeMinWidth:   8,
			InputMinWidth:   25,
			ButtonMinWidth:  10,
			ListIndent:      4,
			ListItemPadding: 2,
			ListGutterWidth: 4,
			TableColumnGap:  4,
			TableRowPadding: 1,
			ModalWidth:      80,
			ModalMinHeight:  15,
			DashCardWidth:   30,
			DashCardHeight:  7,
			DashGridGap:     2,
		},
		Animation:   DefaultAnimation,
		Breakpoints: DefaultBreakpoints,
	}
}

// UltraWide returns tokens optimized for ultra-wide displays (200+ cols).
// These take full advantage of available horizontal space.
func UltraWide() DesignTokens {
	return DesignTokens{
		Spacing: Spacing{
			None: 0,
			XS:   2,
			SM:   4,
			MD:   6,
			LG:   8,
			XL:   12,
			XXL:  16,
		},
		Size: Size{
			XS:  30,
			SM:  50,
			MD:  70,
			LG:  100,
			XL:  140,
			XXL: 180,
		},
		Typography: Typography{
			SizeXS:           12,
			SizeSM:           14,
			SizeMD:           16,
			SizeLG:           20,
			SizeXL:           24,
			SizeXXL:          32,
			LineHeightTight:  1,
			LineHeightNormal: 2,
			LineHeightLoose:  4,
		},
		Layout: LayoutTokens{
			MarginPage:      6,
			MarginSection:   4,
			MarginItem:      2,
			PaddingCard:     4,
			PaddingInline:   3,
			PaddingInput:    2,
			IconWidth:       5,
			LabelWidth:      20,
			BadgeMinWidth:   10,
			InputMinWidth:   35,
			ButtonMinWidth:  14,
			ListIndent:      6,
			ListItemPadding: 2,
			ListGutterWidth: 6,
			TableColumnGap:  6,
			TableRowPadding: 1,
			ModalWidth:      120,
			ModalMinHeight:  20,
			DashCardWidth:   40,
			DashCardHeight:  10,
			DashGridGap:     3,
		},
		Animation:   DefaultAnimation,
		Breakpoints: DefaultBreakpoints,
	}
}

// TokensForWidth returns appropriate tokens based on terminal width.
// Supports compact, default, spacious, and ultra-wide layouts.
func TokensForWidth(width int) DesignTokens {
	bp := DefaultBreakpoints
	switch {
	case width < bp.XS:
		return Compact()
	case width < bp.MD:
		return DefaultTokens()
	case width < bp.Wide:
		return Spacious()
	default:
		return UltraWide()
	}
}

// LayoutMode represents the current layout mode based on width.
type LayoutMode int

const (
	LayoutCompact   LayoutMode = iota // Narrow terminals
	LayoutDefault                     // Standard terminals
	LayoutSpacious                    // Wide terminals
	LayoutUltraWide                   // Ultra-wide displays
)

// GetLayoutMode returns the appropriate layout mode for the given width.
func GetLayoutMode(width int) LayoutMode {
	bp := DefaultBreakpoints
	switch {
	case width < bp.XS:
		return LayoutCompact
	case width < bp.MD:
		return LayoutDefault
	case width < bp.Wide:
		return LayoutSpacious
	default:
		return LayoutUltraWide
	}
}

// AdaptiveCardDimensions calculates optimal card dimensions for a grid layout.
// Inspired by beads_viewer's adaptive column width algorithm.
func AdaptiveCardDimensions(totalWidth, minCardWidth, maxCardWidth, gap int) (cardWidth, cardsPerRow int) {
	// Guard against invalid inputs
	if totalWidth <= 0 || minCardWidth <= 0 || maxCardWidth <= 0 {
		return 1, 1 // Return minimal safe values
	}

	if totalWidth < minCardWidth {
		return totalWidth, 1
	}

	// Calculate how many cards can fit
	cardsPerRow = (totalWidth + gap) / (minCardWidth + gap)
	if cardsPerRow < 1 {
		cardsPerRow = 1
	}

	// Calculate optimal card width to fill available space
	totalGaps := (cardsPerRow - 1) * gap
	availableWidth := totalWidth - totalGaps
	cardWidth = availableWidth / cardsPerRow

	// Clamp to max width
	if cardWidth > maxCardWidth {
		cardWidth = maxCardWidth
		// Recalculate cards per row with max width
		cardsPerRow = (totalWidth + gap) / (maxCardWidth + gap)
		if cardsPerRow < 1 {
			cardsPerRow = 1
		}
	}

	return cardWidth, cardsPerRow
}

// -----------------------------------------------------------------------------
// Style Builders - lipgloss.Style factories using design tokens
// -----------------------------------------------------------------------------
