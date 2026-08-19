// Package theme provides semantic color names for consistent UI styling.
// This file defines role-based colors that map to theme colors.
package theme

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

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
	AgentClaude      lipgloss.Color // Claude Code (purple)
	AgentCodex       lipgloss.Color // OpenAI Codex (blue)
	AgentGemini      lipgloss.Color // Google Gemini (yellow)
	AgentGrok        lipgloss.Color // Grok Build (pink)
	AgentAntigravity lipgloss.Color // Antigravity / agy (lavender)
	AgentCursor      lipgloss.Color // Cursor (teal)
	AgentWindsurf    lipgloss.Color // Windsurf (flamingo)
	AgentAider       lipgloss.Color // Aider (peach)
	AgentOpencode    lipgloss.Color // Opencode (lavender)
	AgentOllama      lipgloss.Color // Ollama (green)
	AgentUser        lipgloss.Color // User pane (green)
	AgentUnknown     lipgloss.Color // Unknown agent type

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
		AgentClaude:      t.Claude,
		AgentCodex:       t.Codex,
		AgentGemini:      t.Gemini,
		AgentGrok:        t.Pink,
		AgentAntigravity: t.Lavender,
		AgentCursor:      t.Cursor,
		AgentWindsurf:    t.Windsurf,
		AgentAider:       t.Aider,
		AgentOpencode:    t.Opencode,
		AgentOllama:      t.Ollama,
		AgentUser:        t.User,
		AgentUnknown:     t.Overlay,

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

// AgentColor returns the color for a given agent type string.
func (p SemanticPalette) AgentColor(agentType string) lipgloss.Color {
	switch agent.AgentType(agentType).Canonical() {
	case agent.AgentTypeClaudeCode:
		return p.AgentClaude
	case agent.AgentTypeCodex:
		return p.AgentCodex
	case agent.AgentTypeGemini:
		return p.AgentGemini
	case agent.AgentTypeGrok:
		return p.AgentGrok
	case agent.AgentTypeAntigravity:
		return p.AgentAntigravity
	case agent.AgentTypeCursor:
		return p.AgentCursor
	case agent.AgentTypeWindsurf:
		return p.AgentWindsurf
	case agent.AgentTypeAider:
		return p.AgentAider
	case agent.AgentTypeOpencode:
		return p.AgentOpencode
	case agent.AgentTypeOllama:
		return p.AgentOllama
	case agent.AgentTypeUser:
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
