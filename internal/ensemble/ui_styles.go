package ensemble

import (
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// TierChip renders a tier badge for core/advanced/experimental modes.
func TierChip(tier ModeTier) string {
	if tier == "" {
		return ""
	}
	t := theme.Current()
	opts := styles.BadgeOptions{
		Style:      styles.BadgeStyleCompact,
		Bold:       true,
		ShowIcon:   false,
		FixedWidth: 4,
	}
	switch tier {
	case TierCore:
		return styles.TextBadge("CORE", t.Green, t.Base, opts)
	case TierAdvanced:
		return styles.TextBadge("ADV", t.Yellow, t.Base, opts)
	case TierExperimental:
		return styles.TextBadge("EXP", t.Red, t.Base, opts)
	default:
		return styles.TextBadge(strings.ToUpper(tier.String()), t.Surface1, t.Text, opts)
	}
}
