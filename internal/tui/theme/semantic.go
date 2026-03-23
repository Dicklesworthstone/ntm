// Package theme provides semantic color names for consistent UI styling.
// This file defines role-based colors that map to theme colors.
package theme

import "github.com/charmbracelet/lipgloss"

// SemanticPalette provides role-based color names for UI elements.
// Use these instead of raw colors for consistent theming.
type SemanticPalette struct {
	// Backgrounds
	BgPrimary   lipgloss.Color // Main background
	BgSecondary lipgloss.Color // Elevated surfaces
	BgTertiary  lipgloss.Color // Highest elevation
	BgHighlight lipgloss.Color // Hover/focus background
	BgSelected  lipgloss.Color // Selected item background
	BgDisabled  lipgloss.Color // Disabled element background

	// Foregrounds (text)
	FgPrimary   lipgloss.Color // Primary text
	FgSecondary lipgloss.Color // Secondary/muted text
	FgTertiary  lipgloss.Color // Hint/placeholder text
	FgDisabled  lipgloss.Color // Disabled text
	FgInverse   lipgloss.Color // Text on accent backgrounds

	// Borders
	BorderDefault lipgloss.Color // Default border
	BorderFocused lipgloss.Color // Focused element border
	BorderError   lipgloss.Color // Error state border
	BorderSuccess lipgloss.Color // Success state border

	// Interactive states
	Interactive        lipgloss.Color // Default interactive element
	InteractiveHover   lipgloss.Color // Hover state
	InteractiveActive  lipgloss.Color // Active/pressed state
	InteractiveFocused lipgloss.Color // Focused state

	// Status indicators
	StatusSuccess  lipgloss.Color // Success/completed
	StatusWarning  lipgloss.Color // Warning/attention needed
	StatusError    lipgloss.Color // Error/failure
	StatusInfo     lipgloss.Color // Informational
	StatusPending  lipgloss.Color // Pending/in-progress
	StatusIdle     lipgloss.Color // Idle/inactive
	StatusDisabled lipgloss.Color // Disabled/unavailable

	// Agent identifiers
	AgentClaude   lipgloss.Color // Claude Code (purple)
	AgentCodex    lipgloss.Color // OpenAI Codex (blue)
	AgentGemini   lipgloss.Color // Google Gemini (yellow)
	AgentCursor   lipgloss.Color // Cursor (teal)
	AgentWindsurf lipgloss.Color // Windsurf (flamingo)
	AgentAider    lipgloss.Color // Aider (peach)
	AgentOllama   lipgloss.Color // Ollama (green)
	AgentUser     lipgloss.Color // User pane (green)
	AgentUnknown  lipgloss.Color // Unknown agent type

	// Accent colors for gradients and highlights
	Accent1 lipgloss.Color // Primary accent
	Accent2 lipgloss.Color // Secondary accent
	Accent3 lipgloss.Color // Tertiary accent
	Accent4 lipgloss.Color // Quaternary accent

	// Special purpose
	Link        lipgloss.Color // Hyperlinks
	Code        lipgloss.Color // Inline code
	CodeBlock   lipgloss.Color // Code block background
	Selection   lipgloss.Color // Selected text background
	Cursor      lipgloss.Color // Cursor/caret
	Scrollbar   lipgloss.Color // Scrollbar track/thumb
	Divider     lipgloss.Color // Divider lines
	Shadow      lipgloss.Color // Shadow/overlay
	Badge       lipgloss.Color // Badge background
	BadgeText   lipgloss.Color // Badge text
	Tooltip     lipgloss.Color // Tooltip background
	TooltipText lipgloss.Color // Tooltip text
}

// Semantic returns the semantic color palette for a theme.
func (t Theme) Semantic() SemanticPalette {
	return SemanticPalette{
		// Backgrounds
		BgPrimary:   t.Base,
		BgSecondary: t.Mantle,
		BgTertiary:  t.Crust,
		BgHighlight: t.Surface0,
		BgSelected:  t.Surface1,
		BgDisabled:  t.Surface0,

		// Foregrounds
		FgPrimary:   t.Text,
		FgSecondary: t.Subtext,
		FgTertiary:  t.Overlay,
		FgDisabled:  t.Overlay,
		FgInverse:   t.Crust,

		// Borders
		BorderDefault: t.Surface2,
		BorderFocused: t.Primary,
		BorderError:   t.Error,
		BorderSuccess: t.Success,

		// Interactive
		Interactive:        t.Primary,
		InteractiveHover:   t.Lavender,
		InteractiveActive:  t.Sapphire,
		InteractiveFocused: t.Primary,

		// Status
		StatusSuccess:  t.Success,
		StatusWarning:  t.Warning,
		StatusError:    t.Error,
		StatusInfo:     t.Info,
		StatusPending:  t.Yellow,
		StatusIdle:     t.Overlay,
		StatusDisabled: t.Overlay,

		// Agents
		AgentClaude:   t.Claude,
		AgentCodex:    t.Codex,
		AgentGemini:   t.Gemini,
		AgentCursor:   t.Cursor,
		AgentWindsurf: t.Windsurf,
		AgentAider:    t.Aider,
		AgentOllama:   t.Ollama,
		AgentUser:     t.User,
		AgentUnknown:  t.Overlay,

		// Accents
		Accent1: t.Blue,
		Accent2: t.Mauve,
		Accent3: t.Pink,
		Accent4: t.Lavender,

		// Special
		Link:        t.Blue,
		Code:        t.Peach,
		CodeBlock:   t.Surface0,
		Selection:   t.Surface1,
		Cursor:      t.Rosewater,
		Scrollbar:   t.Surface2,
		Divider:     t.Surface2,
		Shadow:      t.Crust,
		Badge:       t.Surface1,
		BadgeText:   t.Text,
		Tooltip:     t.Surface1,
		TooltipText: t.Text,
	}
}

// Semantic returns the semantic palette for the current theme.
func Semantic() SemanticPalette {
	return Current().Semantic()
}

// AgentColor returns the color for a given agent type string.
func (p SemanticPalette) AgentColor(agentType string) lipgloss.Color {
	switch agentType {
	case "claude", "cc":
		return p.AgentClaude
	case "codex", "cod":
		return p.AgentCodex
	case "gemini", "gmi":
		return p.AgentGemini
	case "cursor":
		return p.AgentCursor
	case "windsurf":
		return p.AgentWindsurf
	case "aider":
		return p.AgentAider
	case "ollama":
		return p.AgentOllama
	case "user":
		return p.AgentUser
	default:
		return p.AgentUnknown
	}
}

// StatusColor returns the color for a given status string.
func (p SemanticPalette) StatusColor(status string) lipgloss.Color {
	switch status {
	case "success", "ok", "complete", "completed", "done":
		return p.StatusSuccess
	case "warning", "warn", "attention":
		return p.StatusWarning
	case "error", "fail", "failed", "failure":
		return p.StatusError
	case "info", "information":
		return p.StatusInfo
	case "pending", "running", "in_progress", "working":
		return p.StatusPending
	case "idle", "inactive", "waiting":
		return p.StatusIdle
	case "disabled", "unavailable":
		return p.StatusDisabled
	default:
		return p.FgSecondary
	}
}

// SemanticStyles provides pre-built styles using semantic colors.
type SemanticStyles struct {
	// Text styles
	TextPrimary   lipgloss.Style
	TextSecondary lipgloss.Style
	TextMuted     lipgloss.Style
	TextDisabled  lipgloss.Style

	// Status text
	TextSuccess lipgloss.Style
	TextWarning lipgloss.Style
	TextError   lipgloss.Style
	TextInfo    lipgloss.Style

	// Interactive elements
	Link      lipgloss.Style
	LinkHover lipgloss.Style
	Code      lipgloss.Style
	CodeBlock lipgloss.Style

	// Containers
	Surface    lipgloss.Style
	Card       lipgloss.Style
	CardRaised lipgloss.Style
	Overlay    lipgloss.Style

	// Badges
	BadgeDefault lipgloss.Style
	BadgeSuccess lipgloss.Style
	BadgeWarning lipgloss.Style
	BadgeError   lipgloss.Style
	BadgeInfo    lipgloss.Style

	// Agent badges
	BadgeClaude   lipgloss.Style
	BadgeCodex    lipgloss.Style
	BadgeGemini   lipgloss.Style
	BadgeCursor   lipgloss.Style
	BadgeWindsurf lipgloss.Style
	BadgeAider    lipgloss.Style
	BadgeOllama   lipgloss.Style
	BadgeUser     lipgloss.Style

	// Input elements
	Input        lipgloss.Style
	InputFocused lipgloss.Style
	InputError   lipgloss.Style

	// Selection
	Selected   lipgloss.Style
	Unselected lipgloss.Style
}

// NewSemanticStyles creates semantic styles from a theme.
func NewSemanticStyles(t Theme) SemanticStyles {
	p := t.Semantic()

	baseBadge := lipgloss.NewStyle().
		Padding(0, 1).
		Bold(true)

	styles := SemanticStyles{
		// Text
		TextPrimary: lipgloss.NewStyle().
			Foreground(p.FgPrimary),
		TextSecondary: lipgloss.NewStyle().
			Foreground(p.FgSecondary),
		TextMuted: lipgloss.NewStyle().
			Foreground(p.FgTertiary),
		TextDisabled: lipgloss.NewStyle().
			Foreground(p.FgDisabled),

		// Status text
		TextSuccess: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.StatusSuccess),
		TextWarning: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.StatusWarning),
		TextError: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.StatusError),
		TextInfo: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.StatusInfo),

		// Interactive
		Link: lipgloss.NewStyle().
			Foreground(p.Link).
			Underline(true),
		LinkHover: lipgloss.NewStyle().
			Foreground(p.InteractiveHover).
			Underline(true).
			Bold(true),
		Code: lipgloss.NewStyle().
			Foreground(p.Code).
			Background(p.CodeBlock).
			Padding(0, 1),
		CodeBlock: lipgloss.NewStyle().
			Background(p.CodeBlock).
			Padding(1, 2),

		// Containers
		Surface: lipgloss.NewStyle().
			Background(p.BgSecondary),
		Card: lipgloss.NewStyle().
			Background(p.BgSecondary).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(p.BorderDefault).
			Padding(1, 2),
		CardRaised: lipgloss.NewStyle().
			Background(p.BgTertiary).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(p.BorderDefault).
			Padding(1, 2),
		Overlay: lipgloss.NewStyle().
			Background(p.Shadow),

		// Badges (lipgloss styles are immutable, so method calls return new styles)
		BadgeDefault: baseBadge.
			Background(p.Badge).
			Foreground(p.BadgeText),
		BadgeSuccess: baseBadge.
			Background(p.StatusSuccess).
			Foreground(p.FgInverse),
		BadgeWarning: baseBadge.
			Background(p.StatusWarning).
			Foreground(p.FgInverse),
		BadgeError: baseBadge.
			Background(p.StatusError).
			Foreground(p.FgInverse),
		BadgeInfo: baseBadge.
			Background(p.StatusInfo).
			Foreground(p.FgInverse),

		// Agent badges
		BadgeClaude: baseBadge.
			Background(p.AgentClaude).
			Foreground(p.FgInverse),
		BadgeCodex: baseBadge.
			Background(p.AgentCodex).
			Foreground(p.FgInverse),
		BadgeGemini: baseBadge.
			Background(p.AgentGemini).
			Foreground(p.FgInverse),
		BadgeCursor: baseBadge.
			Background(p.AgentCursor).
			Foreground(p.FgInverse),
		BadgeWindsurf: baseBadge.
			Background(p.AgentWindsurf).
			Foreground(p.FgInverse),
		BadgeAider: baseBadge.
			Background(p.AgentAider).
			Foreground(p.FgInverse),
		BadgeOllama: baseBadge.
			Background(p.AgentOllama).
			Foreground(p.FgInverse),
		BadgeUser: baseBadge.
			Background(p.AgentUser).
			Foreground(p.FgInverse),

		// Input
		Input: lipgloss.NewStyle().
			Foreground(p.FgPrimary).
			Background(p.BgHighlight).
			Padding(0, 1),
		InputFocused: lipgloss.NewStyle().
			Foreground(p.FgPrimary).
			Background(p.BgSelected).
			Border(lipgloss.NormalBorder(), false, false, true, false).
			BorderForeground(p.BorderFocused).
			Padding(0, 1),
		InputError: lipgloss.NewStyle().
			Foreground(p.FgPrimary).
			Background(p.BgHighlight).
			Border(lipgloss.NormalBorder(), false, false, true, false).
			BorderForeground(p.BorderError).
			Padding(0, 1),

		// Selection
		Selected: lipgloss.NewStyle().
			Background(p.BgSelected).
			Foreground(p.FgPrimary).
			Bold(true),
		Unselected: lipgloss.NewStyle().
			Foreground(p.FgSecondary),
	}

	if t == Plain {
		styles.Selected = lipgloss.NewStyle().
			Bold(true).
			Reverse(true)
		styles.TextWarning = styles.TextWarning.Underline(true)
		styles.TextError = styles.TextError.Underline(true)
	}

	return styles
}

// SemanticStyles returns semantic styles for the current theme.
func DefaultSemanticStyles() SemanticStyles {
	return NewSemanticStyles(Current())
}
