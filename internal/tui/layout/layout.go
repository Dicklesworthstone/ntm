package layout

import "github.com/charmbracelet/lipgloss"

// Width tiers are shared across TUI surfaces so behavior stays predictable on
// narrow laptops, wide displays, ultra‑wide, and now mega‑wide monitors. These
// thresholds are aligned with the design tokens in internal/tui/styles/tokens.go
// to avoid the previous drift between layout, palette, and style breakpoints.
//
// Tier semantics (consumer guidance):
//   - SplitView: switch from stacked → split list/detail layouts
//   - WideView: enable secondary metadata columns and richer badges
//   - UltraWideView: show tertiary metadata (labels, model/variant, locks)
//
// Rationale: tokens.DefaultBreakpoints define LG/XL/Wide/Ultra at 120/160/200/240;
// we place split at 120, wide at 200, ultra at 240 to line up with those tiers.
const (
	SplitViewThreshold     = 120
	WideViewThreshold      = 200
	UltraWideViewThreshold = 240
	MegaWideViewThreshold  = 320
)

// Surface guidance (rationale, not enforced):
//   - Palette: split list/preview at TierSplit; promote richer preview/badges at TierWide+.
//   - Dashboard/status: switch to split list/detail at TierSplit; add secondary metadata bars at
//     TierWide; show tertiary items (locks, model/variant) TierUltra.
//   - Tutorial/markdown views: re-render markdown per resize; use TierWide to loosen padding and
//     show side metadata when present.
// Keeping this guidance close to the thresholds helps avoid divergence across surfaces.
//
// Reference matrix (behavior by tier):
//   TierNarrow (<120): stacked layouts; minimal badges; truncate secondary columns.
//   TierSplit  (120-199): split list/detail; primary metadata only; conservative padding.
//   TierWide   (200-239): enable secondary metadata columns (age/comments/locks/model); richer
//                        preview styling and wider gutters.
//   TierUltra  (240-319): tertiary metadata (labels/variants), widest gutters, extra padding for
//                        markdown/detail panes to avoid wrap when showing side info.
//   TierMega   (>=320):   mega layouts (5-panel), richest gutters, ample padding for cockpit views.

// Tier describes the current width bucket.
type Tier int

const (
	TierNarrow Tier = 0
	TierSplit  Tier = 1
	TierWide   Tier = 2
	// Tier value 3 intentionally unused to preserve legacy ordering gaps.
	TierUltra Tier = 4
	TierMega  Tier = 5
)

// TierForWidth maps a terminal width to a tier.
func TierForWidth(width int) Tier {
	switch {
	case width >= MegaWideViewThreshold:
		return TierMega
	case width >= UltraWideViewThreshold:
		return TierUltra
	case width >= WideViewThreshold:
		return TierWide
	case width >= SplitViewThreshold:
		return TierSplit
	default:
		return TierNarrow
	}
}

// HysteresisMargin is the number of columns of padding around tier boundaries
// to prevent rapid tier changes when the width is near a boundary.
const HysteresisMargin = 5

// TierForWidthWithHysteresis maps width to a tier with hysteresis to prevent
// flickering when resizing near tier boundaries. If the previous tier is
// provided, the function will prefer staying in that tier if the width is
// within HysteresisMargin of the boundary.
func TierForWidthWithHysteresis(width int, prevTier Tier) Tier {
	newTier := TierForWidth(width)

	// If no change or previous tier is invalid, use the new tier directly
	if newTier == prevTier || prevTier < TierNarrow || prevTier > TierMega {
		return newTier
	}

	// Apply hysteresis: only change tier if we're clearly past the boundary
	// This prevents flickering when resizing near tier boundaries
	switch prevTier {
	case TierNarrow:
		// Stay narrow unless clearly into split
		if width < SplitViewThreshold+HysteresisMargin {
			return TierNarrow
		}
	case TierSplit:
		// Stay split unless clearly out of range
		if width >= SplitViewThreshold-HysteresisMargin && width < WideViewThreshold+HysteresisMargin {
			return TierSplit
		}
	case TierWide:
		// Stay wide unless clearly out of range
		if width >= WideViewThreshold-HysteresisMargin && width < UltraWideViewThreshold+HysteresisMargin {
			return TierWide
		}
	case TierUltra:
		// Stay ultra unless clearly out of range
		if width >= UltraWideViewThreshold-HysteresisMargin && width < MegaWideViewThreshold+HysteresisMargin {
			return TierUltra
		}
	case TierMega:
		// Stay mega unless clearly below threshold
		if width >= MegaWideViewThreshold-HysteresisMargin {
			return TierMega
		}
	}

	return newTier
}

// TruncateRunes trims a string to max runes and appends suffix if truncated.
// It is rune‑aware to avoid splitting emoji or wide glyphs.
func TruncateRunes(s string, max int, suffix string) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max < len([]rune(suffix)) {
		return string(runes[:max])
	}
	return string(runes[:max-len([]rune(suffix))]) + suffix
}

// Truncate is a convenience wrapper for TruncateRunes using the standard
// single-character ellipsis "…" (U+2026). This is the preferred truncation
// function for visual consistency across the TUI.
func Truncate(s string, max int) string {
	return TruncateRunes(s, max, "…")
}

// TruncateWidth trims a string to fit within maxWidth terminal columns,
// appending suffix if truncated. Unlike TruncateRunes, this uses lipgloss.Width()
// to properly account for double-width characters (CJK, emoji) and ANSI codes.
// This is the preferred function when the target is a fixed-width terminal column.
func TruncateWidth(s string, maxWidth int, suffix string) string {
	if maxWidth <= 0 {
		return ""
	}

	// Fast path: string already fits
	currentWidth := lipgloss.Width(s)
	if currentWidth <= maxWidth {
		return s
	}

	suffixWidth := lipgloss.Width(suffix)
	targetWidth := maxWidth - suffixWidth
	if targetWidth <= 0 {
		// Not enough room for suffix, just truncate hard
		return truncateToWidth(s, maxWidth)
	}

	return truncateToWidth(s, targetWidth) + suffix
}

// truncateToWidth removes characters from the end until string fits in maxWidth.
func truncateToWidth(s string, maxWidth int) string {
	runes := []rune(s)

	// Fast path for empty or zero
	if len(runes) == 0 || maxWidth <= 0 {
		return ""
	}

	// Binary search for the maximum length that fits
	low, high := 0, len(runes)
	best := 0

	for low <= high {
		mid := low + (high-low)/2
		candidate := string(runes[:mid])
		if lipgloss.Width(candidate) <= maxWidth {
			best = mid
			low = mid + 1
		} else {
			high = mid - 1
		}
	}

	return string(runes[:best])
}

// TruncateWidthDefault is a convenience wrapper for TruncateWidth using "…".
func TruncateWidthDefault(s string, maxWidth int) string {
	return TruncateWidth(s, maxWidth, "…")
}

// TruncateMiddle truncates a string by removing characters from the middle,
// preserving both the beginning and end.
func TruncateMiddle(s string, maxWidth int) string {
	return TruncateMiddleWidth(s, maxWidth, "…")
}

// TruncateMiddleWidth is like TruncateMiddle but with a custom ellipsis string.
func TruncateMiddleWidth(s string, maxWidth int, ellipsis string) string {
	if maxWidth <= 0 {
		return ""
	}

	// Fast path: string already fits
	currentWidth := lipgloss.Width(s)
	if currentWidth <= maxWidth {
		return s
	}

	ellipsisWidth := lipgloss.Width(ellipsis)
	available := maxWidth - ellipsisWidth

	if available < 2 {
		return truncateToWidth(s, maxWidth)
	}

	endChars := (available * 2) / 3
	startChars := available - endChars

	if startChars < 1 {
		startChars = 1
		endChars = available - 1
	}
	if endChars < 1 {
		endChars = 1
		startChars = available - 1
	}

	runes := []rune(s)

	// Binary search for start portion
	low, high := 0, len(runes)
	bestStart := 0
	for low <= high {
		mid := low + (high-low)/2
		if lipgloss.Width(string(runes[:mid])) <= startChars {
			bestStart = mid
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	startRunes := runes[:bestStart]

	// Binary search for end portion
	low, high = 0, len(runes)
	bestEnd := len(runes)
	for low <= high {
		mid := low + (high-low)/2
		if lipgloss.Width(string(runes[mid:])) <= endChars {
			bestEnd = mid
			high = mid - 1
		} else {
			low = mid + 1
		}
	}

	var endRunes []rune
	if bestEnd < len(runes) {
		endRunes = runes[bestEnd:]
	}

	result := string(startRunes) + ellipsis + string(endRunes)

	if lipgloss.Width(result) > maxWidth {
		return truncateToWidth(result, maxWidth)
	}

	return result
}

// TruncatePaneTitle truncates a pane title while preserving the differentiating suffix.
// NTM pane titles follow the pattern: <project>__<agent>_<number> (e.g., "myproject__cc_1").
// When multiple panes share the same project prefix, naive truncation makes them all
// look identical ("myproject__c…"). This function preserves the agent suffix to keep
// panes visually distinguishable.
//
// Examples:
//   - "destructive_command_guard__cc_1" (width 20) -> "destructive…__cc_1"
//   - "myproject__gmi_2" (width 20) -> "myproject__gmi_2" (fits, no truncation)
//   - "very_long_project_name__cod_10" (width 15) -> "very…__cod_10"
func TruncatePaneTitle(title string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}

	// Fast path: title already fits
	currentWidth := lipgloss.Width(title)
	if currentWidth <= maxWidth {
		return title
	}

	// Find the agent suffix pattern: __<agent>_<number>
	// Agent types: cc (Claude), cod (Codex), gmi (Gemini), usr (user)
	suffixStart := -1
	for i := len(title) - 1; i >= 2; i-- {
		if i >= 2 && title[i-1] == '_' && title[i-2] == '_' {
			suffixStart = i - 2
			break
		}
	}

	// No suffix found or suffix is the whole string - fall back to standard truncation
	if suffixStart <= 0 {
		return TruncateWidthDefault(title, maxWidth)
	}

	prefix := title[:suffixStart]
	suffix := title[suffixStart:] // Includes "__cc_1" etc.
	suffixWidth := lipgloss.Width(suffix)

	// If suffix alone is too wide, truncate normally
	if suffixWidth >= maxWidth {
		return TruncateWidthDefault(title, maxWidth)
	}

	// Calculate available width for prefix (with ellipsis)
	ellipsis := "…"
	ellipsisWidth := lipgloss.Width(ellipsis)
	prefixMaxWidth := maxWidth - suffixWidth - ellipsisWidth

	if prefixMaxWidth <= 0 {
		// Not enough room for prefix, just show suffix
		return TruncateWidthDefault(suffix, maxWidth)
	}

	// Truncate prefix to fit using binary search
	prefixRunes := []rune(prefix)
	low, high := 0, len(prefixRunes)
	best := 0

	for low <= high {
		mid := low + (high-low)/2
		candidate := string(prefixRunes[:mid]) + ellipsis + suffix
		if lipgloss.Width(candidate) <= maxWidth {
			best = mid
			low = mid + 1
		} else {
			high = mid - 1
		}
	}

	if best == 0 {
		return TruncateWidthDefault(suffix, maxWidth)
	}
	return string(prefixRunes[:best]) + ellipsis + suffix
}

// SplitProportions returns left/right widths for split view given total width.
// It removes a small padding budget to prevent edge wrapping.
func SplitProportions(total int) (left int, right int) {
	if total < SplitViewThreshold {
		return total, 0
	}
	// Budget 4 cols for borders/padding on each panel (8 total)
	avail := total - 8
	if avail < 10 {
		avail = total
	}
	left = int(float64(avail) * 0.4)
	right = avail - left
	return
}

// UltraProportions returns left/center/right widths for 3-panel layout (25/50/25).
func UltraProportions(total int) (left, center, right int) {
	if total < UltraWideViewThreshold {
		return 0, total, 0
	}
	// Budget 6 cols for borders/padding (2 per panel)
	avail := total - 6
	if avail < 10 {
		return 0, total, 0
	}
	left = int(float64(avail) * 0.25)
	right = int(float64(avail) * 0.25)
	center = avail - left - right
	return
}

// MegaProportions returns widths for 5-panel layout (18/28/20/17/17).
func MegaProportions(total int) (p1, p2, p3, p4, p5 int) {
	if total < MegaWideViewThreshold {
		return 0, total, 0, 0, 0
	}
	// Budget 10 cols for borders/padding (2 per panel)
	avail := total - 10
	if avail < 10 {
		return 0, total, 0, 0, 0
	}

	p1 = int(float64(avail) * 0.18)
	p2 = int(float64(avail) * 0.28)
	p3 = int(float64(avail) * 0.20)
	p4 = int(float64(avail) * 0.17)
	p5 = avail - p1 - p2 - p3 - p4
	return
}

// MegaProportions6 returns widths for 6-panel layout: Agents | Detail | Beads | Alerts | Attention | Sidebar.
// Proportions: 15/26/18/15/12/14 with attention panel inserted between alerts and sidebar.
func MegaProportions6(total int) (p1, p2, p3, p4, p5, p6 int) {
	if total < MegaWideViewThreshold {
		return 0, total, 0, 0, 0, 0
	}
	// Budget 12 cols for borders/padding (2 per panel)
	avail := total - 12
	if avail < 12 {
		return 0, total, 0, 0, 0, 0
	}

	p1 = int(float64(avail) * 0.15)
	p2 = int(float64(avail) * 0.26)
	p3 = int(float64(avail) * 0.18)
	p4 = int(float64(avail) * 0.15)
	p5 = int(float64(avail) * 0.12)
	p6 = avail - p1 - p2 - p3 - p4 - p5
	return
}
