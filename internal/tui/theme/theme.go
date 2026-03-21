package theme

import (
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Theme defines a complete color palette for the TUI
type Theme struct {
	// Base colors
	Base     lipgloss.Color // Background
	Mantle   lipgloss.Color // Slightly lighter bg
	Crust    lipgloss.Color // Darkest bg
	Surface0 lipgloss.Color // Surface
	Surface1 lipgloss.Color // Surface highlight
	Surface2 lipgloss.Color // Surface bright

	// Text colors
	Text    lipgloss.Color // Primary text
	Subtext lipgloss.Color // Secondary text
	Overlay lipgloss.Color // Dimmed text

	// Accent colors
	Rosewater lipgloss.Color
	Flamingo  lipgloss.Color
	Pink      lipgloss.Color
	Mauve     lipgloss.Color
	Red       lipgloss.Color
	Maroon    lipgloss.Color
	Peach     lipgloss.Color
	Yellow    lipgloss.Color
	Green     lipgloss.Color
	Teal      lipgloss.Color
	Sky       lipgloss.Color
	Sapphire  lipgloss.Color
	Blue      lipgloss.Color
	Lavender  lipgloss.Color

	// Semantic colors
	Primary   lipgloss.Color
	Secondary lipgloss.Color
	Success   lipgloss.Color
	Warning   lipgloss.Color
	Error     lipgloss.Color
	Info      lipgloss.Color

	// Agent-specific colors
	Claude lipgloss.Color
	Codex  lipgloss.Color
	Gemini lipgloss.Color
	User   lipgloss.Color
}

// Catppuccin Mocha - the flagship dark theme
var CatppuccinMocha = Theme{
	// Base colors
	Base:     lipgloss.Color("#1e1e2e"),
	Mantle:   lipgloss.Color("#181825"),
	Crust:    lipgloss.Color("#11111b"),
	Surface0: lipgloss.Color("#313244"),
	Surface1: lipgloss.Color("#45475a"),
	Surface2: lipgloss.Color("#585b70"),

	// Text colors
	Text:    lipgloss.Color("#cdd6f4"),
	Subtext: lipgloss.Color("#a6adc8"),
	Overlay: lipgloss.Color("#6c7086"),

	// Accent colors
	Rosewater: lipgloss.Color("#f5e0dc"),
	Flamingo:  lipgloss.Color("#f2cdcd"),
	Pink:      lipgloss.Color("#f5c2e7"),
	Mauve:     lipgloss.Color("#cba6f7"),
	Red:       lipgloss.Color("#f38ba8"),
	Maroon:    lipgloss.Color("#eba0ac"),
	Peach:     lipgloss.Color("#fab387"),
	Yellow:    lipgloss.Color("#f9e2af"),
	Green:     lipgloss.Color("#a6e3a1"),
	Teal:      lipgloss.Color("#94e2d5"),
	Sky:       lipgloss.Color("#89dceb"),
	Sapphire:  lipgloss.Color("#74c7ec"),
	Blue:      lipgloss.Color("#89b4fa"),
	Lavender:  lipgloss.Color("#b4befe"),

	// Semantic colors
	Primary:   lipgloss.Color("#89b4fa"), // Blue
	Secondary: lipgloss.Color("#cba6f7"), // Mauve
	Success:   lipgloss.Color("#a6e3a1"), // Green
	Warning:   lipgloss.Color("#f9e2af"), // Yellow
	Error:     lipgloss.Color("#f38ba8"), // Red
	Info:      lipgloss.Color("#89dceb"), // Sky

	// Agent colors
	Claude: lipgloss.Color("#cba6f7"), // Mauve (Anthropic purple)
	Codex:  lipgloss.Color("#89b4fa"), // Blue (OpenAI blue)
	Gemini: lipgloss.Color("#f9e2af"), // Yellow (Google colors)
	User:   lipgloss.Color("#a6e3a1"), // Green
}

// Catppuccin Macchiato - darker variant
var CatppuccinMacchiato = Theme{
	Base:     lipgloss.Color("#24273a"),
	Mantle:   lipgloss.Color("#1e2030"),
	Crust:    lipgloss.Color("#181926"),
	Surface0: lipgloss.Color("#363a4f"),
	Surface1: lipgloss.Color("#494d64"),
	Surface2: lipgloss.Color("#5b6078"),

	Text:    lipgloss.Color("#cad3f5"),
	Subtext: lipgloss.Color("#a5adcb"),
	Overlay: lipgloss.Color("#6e738d"),

	Rosewater: lipgloss.Color("#f4dbd6"),
	Flamingo:  lipgloss.Color("#f0c6c6"),
	Pink:      lipgloss.Color("#f5bde6"),
	Mauve:     lipgloss.Color("#c6a0f6"),
	Red:       lipgloss.Color("#ed8796"),
	Maroon:    lipgloss.Color("#ee99a0"),
	Peach:     lipgloss.Color("#f5a97f"),
	Yellow:    lipgloss.Color("#eed49f"),
	Green:     lipgloss.Color("#a6da95"),
	Teal:      lipgloss.Color("#8bd5ca"),
	Sky:       lipgloss.Color("#91d7e3"),
	Sapphire:  lipgloss.Color("#7dc4e4"),
	Blue:      lipgloss.Color("#8aadf4"),
	Lavender:  lipgloss.Color("#b7bdf8"),

	Primary:   lipgloss.Color("#8aadf4"),
	Secondary: lipgloss.Color("#c6a0f6"),
	Success:   lipgloss.Color("#a6da95"),
	Warning:   lipgloss.Color("#eed49f"),
	Error:     lipgloss.Color("#ed8796"),
	Info:      lipgloss.Color("#91d7e3"),

	Claude: lipgloss.Color("#c6a0f6"),
	Codex:  lipgloss.Color("#8aadf4"),
	Gemini: lipgloss.Color("#eed49f"),
	User:   lipgloss.Color("#a6da95"),
}

// Catppuccin Latte - light theme for light terminals
var CatppuccinLatte = Theme{
	Base:     lipgloss.Color("#eff1f5"),
	Mantle:   lipgloss.Color("#e6e9ef"),
	Crust:    lipgloss.Color("#dce0e8"),
	Surface0: lipgloss.Color("#ccd0da"),
	Surface1: lipgloss.Color("#bcc0cc"),
	Surface2: lipgloss.Color("#acb0be"),

	Text:    lipgloss.Color("#4c4f69"),
	Subtext: lipgloss.Color("#6c6f85"),
	Overlay: lipgloss.Color("#7c7f93"),

	Rosewater: lipgloss.Color("#dc8a78"),
	Flamingo:  lipgloss.Color("#dd7878"),
	Pink:      lipgloss.Color("#ea76cb"),
	Mauve:     lipgloss.Color("#8839ef"),
	Red:       lipgloss.Color("#d20f39"),
	Maroon:    lipgloss.Color("#e64553"),
	Peach:     lipgloss.Color("#fe640b"),
	Yellow:    lipgloss.Color("#df8e1d"),
	Green:     lipgloss.Color("#40a02b"),
	Teal:      lipgloss.Color("#179299"),
	Sky:       lipgloss.Color("#04a5e5"),
	Sapphire:  lipgloss.Color("#209fb5"),
	Blue:      lipgloss.Color("#1e66f5"),
	Lavender:  lipgloss.Color("#7287fd"),

	Primary:   lipgloss.Color("#1e66f5"),
	Secondary: lipgloss.Color("#8839ef"),
	Success:   lipgloss.Color("#40a02b"),
	Warning:   lipgloss.Color("#df8e1d"),
	Error:     lipgloss.Color("#d20f39"),
	Info:      lipgloss.Color("#04a5e5"),

	Claude: lipgloss.Color("#8839ef"),
	Codex:  lipgloss.Color("#1e66f5"),
	Gemini: lipgloss.Color("#df8e1d"),
	User:   lipgloss.Color("#40a02b"),
}

// Plain is a no-color theme that uses empty/default colors.
// Used when NO_COLOR is set or for accessibility needs.
var Plain = Theme{
	// Base colors - empty strings mean terminal default
	Base:     lipgloss.Color(""),
	Mantle:   lipgloss.Color(""),
	Crust:    lipgloss.Color(""),
	Surface0: lipgloss.Color(""),
	Surface1: lipgloss.Color(""),
	Surface2: lipgloss.Color(""),

	// Text colors
	Text:    lipgloss.Color(""),
	Subtext: lipgloss.Color(""),
	Overlay: lipgloss.Color(""),

	// Accent colors - all default
	Rosewater: lipgloss.Color(""),
	Flamingo:  lipgloss.Color(""),
	Pink:      lipgloss.Color(""),
	Mauve:     lipgloss.Color(""),
	Red:       lipgloss.Color(""),
	Maroon:    lipgloss.Color(""),
	Peach:     lipgloss.Color(""),
	Yellow:    lipgloss.Color(""),
	Green:     lipgloss.Color(""),
	Teal:      lipgloss.Color(""),
	Sky:       lipgloss.Color(""),
	Sapphire:  lipgloss.Color(""),
	Blue:      lipgloss.Color(""),
	Lavender:  lipgloss.Color(""),

	// Semantic colors
	Primary:   lipgloss.Color(""),
	Secondary: lipgloss.Color(""),
	Success:   lipgloss.Color(""),
	Warning:   lipgloss.Color(""),
	Error:     lipgloss.Color(""),
	Info:      lipgloss.Color(""),

	// Agent colors
	Claude: lipgloss.Color(""),
	Codex:  lipgloss.Color(""),
	Gemini: lipgloss.Color(""),
	User:   lipgloss.Color(""),
}

// Nord - popular arctic theme
var Nord = Theme{
	Base:     lipgloss.Color("#2e3440"),
	Mantle:   lipgloss.Color("#272c36"),
	Crust:    lipgloss.Color("#21262e"),
	Surface0: lipgloss.Color("#3b4252"),
	Surface1: lipgloss.Color("#434c5e"),
	Surface2: lipgloss.Color("#4c566a"),

	Text:    lipgloss.Color("#eceff4"),
	Subtext: lipgloss.Color("#d8dee9"),
	Overlay: lipgloss.Color("#7b88a1"),

	Rosewater: lipgloss.Color("#d8dee9"),
	Flamingo:  lipgloss.Color("#d08770"),
	Pink:      lipgloss.Color("#b48ead"),
	Mauve:     lipgloss.Color("#b48ead"),
	Red:       lipgloss.Color("#bf616a"),
	Maroon:    lipgloss.Color("#d08770"),
	Peach:     lipgloss.Color("#d08770"),
	Yellow:    lipgloss.Color("#ebcb8b"),
	Green:     lipgloss.Color("#a3be8c"),
	Teal:      lipgloss.Color("#8fbcbb"),
	Sky:       lipgloss.Color("#88c0d0"),
	Sapphire:  lipgloss.Color("#81a1c1"),
	Blue:      lipgloss.Color("#5e81ac"),
	Lavender:  lipgloss.Color("#b48ead"),

	Primary:   lipgloss.Color("#88c0d0"),
	Secondary: lipgloss.Color("#b48ead"),
	Success:   lipgloss.Color("#a3be8c"),
	Warning:   lipgloss.Color("#ebcb8b"),
	Error:     lipgloss.Color("#bf616a"),
	Info:      lipgloss.Color("#81a1c1"),

	Claude: lipgloss.Color("#b48ead"),
	Codex:  lipgloss.Color("#81a1c1"),
	Gemini: lipgloss.Color("#ebcb8b"),
	User:   lipgloss.Color("#a3be8c"),
}

// Default is the currently active theme
var Default = CatppuccinMocha

// NoColorEnabled returns true if color output should be disabled.
// Respects the NO_COLOR standard (https://no-color.org/):
// - If NO_COLOR exists in environment (any value), colors are disabled
// - NTM_NO_COLOR=1 also disables colors
// - NTM_NO_COLOR=0 forces colors ON (overrides NO_COLOR)
func NoColorEnabled() bool {
	// NTM-specific override takes precedence
	ntmNoColor := strings.TrimSpace(os.Getenv("NTM_NO_COLOR"))
	switch strings.ToLower(ntmNoColor) {
	case "0", "false", "no", "off":
		return false // Force colors on
	case "1", "true", "yes", "on":
		return true // Force colors off
	}

	// Check standard NO_COLOR (presence means disabled, regardless of value)
	_, noColorSet := os.LookupEnv("NO_COLOR")
	return noColorSet
}

// FromName returns a theme by name
func FromName(name string) Theme {
	// Always return Plain theme if NO_COLOR is enabled
	if NoColorEnabled() {
		return Plain
	}

	switch strings.ToLower(strings.TrimSpace(name)) {
	case "plain", "none", "no-color", "nocolor":
		return Plain
	case "macchiato":
		return CatppuccinMacchiato
	case "nord":
		return Nord
	case "latte", "light":
		return CatppuccinLatte
	case "mocha":
		return CatppuccinMocha
	case "auto", "":
		return autoTheme()
	default:
		return autoTheme()
	}
}

// Current returns the current theme based on env var or default
func Current() Theme {
	return FromName(os.Getenv("NTM_THEME"))
}

// IsPlain reports whether the theme is the plain/no-color theme.
func IsPlain(t Theme) bool {
	return t == Plain
}

// IsDark reports whether the theme should be treated as dark for adaptive styling.
func IsDark(t Theme) bool {
	if IsPlain(t) {
		return true
	}
	return t.Base != CatppuccinLatte.Base
}

// ApplyLipGlossDefaults pins global Lip Gloss renderer settings without
// performing any terminal round-trips. This prevents adaptive Bubbles/Lip Gloss
// components from triggering their own background probes later.
func ApplyLipGlossDefaults(t Theme) {
	lipgloss.SetHasDarkBackground(IsDark(t))
	lipgloss.SetColorProfile(termenv.EnvColorProfile())
}

func explicitDarkBackground() (bool, bool) {
	for _, key := range []string{"NTM_BACKGROUND", "NTM_BG", "TERMINAL_BACKGROUND"} {
		switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
		case "dark":
			return true, true
		case "light":
			return false, true
		}
	}
	return false, false
}

func colorFGBGDarkBackground(value string) (bool, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return false, false
	}

	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ';' || r == ':'
	})
	if len(parts) == 0 {
		return false, false
	}

	bg, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return false, false
	}

	switch {
	case bg < 0:
		return false, false
	case bg <= 6 || bg == 8:
		return true, true
	default:
		return false, true
	}
}

// detectDarkBackground inspects the terminal to determine if a dark background is in use.
// It is defined as a variable for testability.
//
// This must never perform terminal I/O. Startup needs a deterministic answer in a few
// microseconds, not a multi-second OSC round-trip that blocks first paint.
var detectDarkBackground = func() bool {
	if dark, ok := explicitDarkBackground(); ok {
		return dark
	}

	// Skip terminal probing over SSH. Dark is the safest default and matches prior behavior.
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" {
		return true
	}

	if dark, ok := colorFGBGDarkBackground(os.Getenv("COLORFGBG")); ok {
		return dark
	}

	// Default to dark to keep the dashboard deterministic and avoid blocking startup.
	return true
}

var (
	cachedAutoTheme Theme
	autoThemeOnce   sync.Once
)

// resetAutoTheme resets the cached auto theme for testing purposes.
// This allows tests to re-run auto detection with different mock detectors.
var resetAutoTheme = func() {
	autoThemeOnce = sync.Once{}
	cachedAutoTheme = Theme{}
}

func autoTheme() (result Theme) {
	autoThemeOnce.Do(func() {
		// Default to dark theme (Mocha) - safer for most terminals
		cachedAutoTheme = CatppuccinMocha

		defer func() {
			if recover() != nil {
				// If detection panics, fall back to dark theme
				cachedAutoTheme = CatppuccinMocha
			}
		}()

		if detectDarkBackground() {
			cachedAutoTheme = CatppuccinMocha
		} else {
			cachedAutoTheme = CatppuccinLatte
		}
	})
	return cachedAutoTheme
}

// Styles contains pre-built lipgloss styles for the theme
type Styles struct {
	// Base styles
	App     lipgloss.Style
	Header  lipgloss.Style
	Title   lipgloss.Style
	Divider lipgloss.Style

	// Text styles
	Normal    lipgloss.Style
	Bold      lipgloss.Style
	Dim       lipgloss.Style
	Highlight lipgloss.Style

	// Status styles
	Success lipgloss.Style
	Warning lipgloss.Style
	Error   lipgloss.Style
	Info    lipgloss.Style

	// Component styles
	Box          lipgloss.Style
	BoxTitle     lipgloss.Style
	List         lipgloss.Style
	ListItem     lipgloss.Style
	ListSelected lipgloss.Style
	ListCursor   lipgloss.Style

	// Agent styles
	Claude lipgloss.Style
	Codex  lipgloss.Style
	Gemini lipgloss.Style
	User   lipgloss.Style

	// Interactive styles
	Button       lipgloss.Style
	ButtonActive lipgloss.Style
	Input        lipgloss.Style
	InputFocused lipgloss.Style

	// Help/status bar
	Help      lipgloss.Style
	StatusBar lipgloss.Style
}

// NewStyles creates a Styles instance from a theme
func NewStyles(t Theme) Styles {
	styles := Styles{
		// Base styles
		App: lipgloss.NewStyle().
			Background(t.Base),

		Header: lipgloss.NewStyle().
			Bold(true).
			Foreground(t.Primary).
			Padding(0, 1),

		Title: lipgloss.NewStyle().
			Bold(true).
			Foreground(t.Text),

		Divider: lipgloss.NewStyle().
			Foreground(t.Surface2),

		// Text styles
		Normal: lipgloss.NewStyle().
			Foreground(t.Text),

		Bold: lipgloss.NewStyle().
			Bold(true).
			Foreground(t.Text),

		Dim: lipgloss.NewStyle().
			Foreground(t.Overlay),

		Highlight: lipgloss.NewStyle().
			Bold(true).
			Foreground(t.Rosewater),

		// Status styles
		Success: lipgloss.NewStyle().
			Bold(true).
			Foreground(t.Success),

		Warning: lipgloss.NewStyle().
			Bold(true).
			Foreground(t.Warning),

		Error: lipgloss.NewStyle().
			Bold(true).
			Foreground(t.Error),

		Info: lipgloss.NewStyle().
			Bold(true).
			Foreground(t.Info),

		// Component styles
		Box: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.Surface2).
			Padding(1, 2),

		BoxTitle: lipgloss.NewStyle().
			Bold(true).
			Foreground(t.Primary).
			Padding(0, 1),

		List: lipgloss.NewStyle().
			Padding(0, 1),

		ListItem: lipgloss.NewStyle().
			Foreground(t.Text).
			Padding(0, 1),

		ListSelected: lipgloss.NewStyle().
			Bold(true).
			Foreground(t.Base).
			Background(t.Primary).
			Padding(0, 1),

		ListCursor: lipgloss.NewStyle().
			Bold(true).
			Foreground(t.Primary),

		// Agent styles
		Claude: lipgloss.NewStyle().
			Foreground(t.Claude),

		Codex: lipgloss.NewStyle().
			Foreground(t.Codex),

		Gemini: lipgloss.NewStyle().
			Foreground(t.Gemini),

		User: lipgloss.NewStyle().
			Foreground(t.User),

		// Interactive styles
		Button: lipgloss.NewStyle().
			Foreground(t.Text).
			Background(t.Surface1).
			Padding(0, 2),

		ButtonActive: lipgloss.NewStyle().
			Bold(true).
			Foreground(t.Base).
			Background(t.Primary).
			Padding(0, 2),

		Input: lipgloss.NewStyle().
			Foreground(t.Text).
			Background(t.Surface0).
			Padding(0, 1),

		InputFocused: lipgloss.NewStyle().
			Foreground(t.Text).
			Background(t.Surface1).
			Border(lipgloss.NormalBorder(), false, false, true, false).
			BorderForeground(t.Primary).
			Padding(0, 1),

		// Help/status bar
		Help: lipgloss.NewStyle().
			Foreground(t.Overlay),

		StatusBar: lipgloss.NewStyle().
			Foreground(t.Subtext).
			Background(t.Surface0).
			Padding(0, 1),
	}

	// Guard rails for no-color / extremely low-color environments:
	// do not rely on subtle background shades for selection, and avoid
	// encoding status meaning by color alone.
	if t == Plain {
		styles.ListSelected = lipgloss.NewStyle().
			Bold(true).
			Reverse(true).
			Padding(0, 1)
		styles.Warning = styles.Warning.Underline(true)
		styles.Error = styles.Error.Underline(true)
	}

	return styles
}

// DefaultStyles returns styles for the current theme
func DefaultStyles() Styles {
	return NewStyles(Current())
}

// Gradient returns a slice of colors for gradient effects
func (t Theme) Gradient(steps int) []lipgloss.Color {
	// Simple gradient from Blue to Pink
	colors := []lipgloss.Color{
		t.Blue,
		t.Sapphire,
		t.Lavender,
		t.Mauve,
		t.Pink,
	}

	if steps <= len(colors) {
		return colors[:steps]
	}

	// Repeat colors if more steps needed
	result := make([]lipgloss.Color, steps)
	for i := 0; i < steps; i++ {
		result[i] = colors[i%len(colors)]
	}
	return result
}
