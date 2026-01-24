package ensemble

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/tui/icons"
	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// ModeBadge renders a compact mode badge with icon + taxonomy code.
func ModeBadge(mode ReasoningMode) string {
	code := strings.TrimSpace(mode.Code)
	if code == "" {
		code = strings.ToUpper(mode.ID)
	}
	icon := strings.TrimSpace(mode.Icon)
	if icon == "" || (icons.IsASCII() && !isASCII(icon)) {
		icon = defaultModeIcon()
	}

	label := strings.TrimSpace(strings.TrimSpace(icon + " " + code))
	if label == "" {
		label = code
	}

	color := CategoryColor(mode.Category)
	return lipgloss.NewStyle().Foreground(color).Bold(true).Render(label)
}

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

// CategoryColor returns the color assigned to a taxonomy category (A-L).
func CategoryColor(cat ModeCategory) lipgloss.Color {
	t := theme.Current()
	switch cat {
	case CategoryFormal:
		return t.Blue
	case CategoryAmpliative:
		return t.Mauve
	case CategoryUncertainty:
		return t.Yellow
	case CategoryVagueness:
		return t.Sky
	case CategoryChange:
		return t.Teal
	case CategoryCausal:
		return t.Green
	case CategoryPractical:
		return t.Peach
	case CategoryStrategic:
		return t.Red
	case CategoryDialectical:
		return t.Maroon
	case CategoryModal:
		return t.Sapphire
	case CategoryDomain:
		return t.Lavender
	case CategoryMeta:
		return t.Pink
	default:
		return t.Surface2
	}
}

func defaultModeIcon() string {
	if icons.IsASCII() {
		return "*"
	}
	return "◆"
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 0x7f {
			return false
		}
	}
	return true
}
