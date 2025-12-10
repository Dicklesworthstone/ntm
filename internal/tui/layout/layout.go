package layout

// Width tiers are shared across TUI surfaces so behavior stays predictable on
// narrow laptops, wide displays, and ultra‑wide monitors. These thresholds are
// aligned with the design tokens in internal/tui/styles/tokens.go to avoid the
// previous drift between layout, palette, and style breakpoints.
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
//   TierUltra  (>=240):   tertiary metadata (labels/variants), widest gutters, extra padding for
//                        markdown/detail panes to avoid wrap when showing side info.

// Tier describes the current width bucket.
type Tier int

const (
	TierNarrow Tier = iota
	TierSplit
	TierWide
	TierUltra
)

// TierForWidth maps a terminal width to a tier.
func TierForWidth(width int) Tier {
	switch {
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
