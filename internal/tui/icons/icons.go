package icons

import (
	"os"
	"path/filepath"
	"strings"
)

// IconSet contains all icons used in the TUI
type IconSet struct {
	// Navigation
	Pointer   string
	ArrowUp   string
	ArrowDown string
	ArrowLeft string
	ArrowRight string
	Enter     string
	Back      string

	// Status
	Check    string
	Cross    string
	Dot      string
	Circle   string
	Star     string
	Warning  string
	Info     string
	Question string

	// Objects
	Folder   string
	File     string
	Terminal string
	Pane     string
	Window   string
	Session  string

	// Actions
	Send     string
	Target   string
	Search   string
	Filter   string
	Copy     string
	Save     string
	Kill     string
	Zoom     string
	View     string

	// Branding
	Palette string
	Robot   string
	Claude  string
	Codex   string
	Gemini  string
	All     string
	User    string

	// Categories
	Quick         string
	CodeQuality   string
	Coordination  string
	Investigation string
	General       string

	// Decorations
	Sparkle  string
	Fire     string
	Lightning string
	Rocket   string
	Gear     string
}

// NerdFonts is the full icon set using Nerd Font symbols
var NerdFonts = IconSet{
	// Navigation
	Pointer:    "❯",
	ArrowUp:    "",
	ArrowDown:  "",
	ArrowLeft:  "",
	ArrowRight: "",
	Enter:      "⏎",
	Back:       "",

	// Status
	Check:    "",
	Cross:    "",
	Dot:      "●",
	Circle:   "○",
	Star:     "★",
	Warning:  "",
	Info:     "",
	Question: "",

	// Objects
	Folder:   "",
	File:     "",
	Terminal: "",
	Pane:     "",
	Window:   "",
	Session:  "󰆍",

	// Actions
	Send:   "",
	Target: "󰓾",
	Search: "",
	Filter: "",
	Copy:   "",
	Save:   "",
	Kill:   "󰅖",
	Zoom:   "",
	View:   "󰈈",

	// Branding
	Palette: "",
	Robot:   "󰚩",
	Claude:  "󰗣", // Alpha C circle - Anthropic-ish
	Codex:   "",  // Hubot - OpenAI-ish
	Gemini:  "󰊤", // Google icon
	All:     "󰕟", // Broadcast
	User:    "",  // User icon

	// Categories
	Quick:         "⚡",
	CodeQuality:   "",
	Coordination:  "󰍹",
	Investigation: "",
	General:       "",

	// Decorations
	Sparkle:   "✨",
	Fire:      "🔥",
	Lightning: "⚡",
	Rocket:    "🚀",
	Gear:      "",
}

// Unicode is a fallback icon set using standard Unicode
var Unicode = IconSet{
	// Navigation
	Pointer:    "›",
	ArrowUp:    "↑",
	ArrowDown:  "↓",
	ArrowLeft:  "←",
	ArrowRight: "→",
	Enter:      "↵",
	Back:       "←",

	// Status
	Check:    "✓",
	Cross:    "✗",
	Dot:      "•",
	Circle:   "○",
	Star:     "★",
	Warning:  "⚠",
	Info:     "ℹ",
	Question: "?",

	// Objects
	Folder:   "📁",
	File:     "📄",
	Terminal: "▢",
	Pane:     "▢",
	Window:   "◻",
	Session:  "◆",

	// Actions
	Send:   "➤",
	Target: "◎",
	Search: "🔍",
	Filter: "⊛",
	Copy:   "⎘",
	Save:   "💾",
	Kill:   "✕",
	Zoom:   "⊕",
	View:   "👁",

	// Branding
	Palette: "◆",
	Robot:   "⚙",
	Claude:  "C",
	Codex:   "O",
	Gemini:  "G",
	All:     "*",
	User:    "👤",

	// Categories
	Quick:         "⚡",
	CodeQuality:   "✎",
	Coordination:  "⇄",
	Investigation: "🔍",
	General:       "•",

	// Decorations
	Sparkle:   "✨",
	Fire:      "*",
	Lightning: "⚡",
	Rocket:    "→",
	Gear:      "⚙",
}

// ASCII is a minimal fallback for terminals without Unicode
var ASCII = IconSet{
	// Navigation
	Pointer:    ">",
	ArrowUp:    "^",
	ArrowDown:  "v",
	ArrowLeft:  "<",
	ArrowRight: ">",
	Enter:      "[Enter]",
	Back:       "<-",

	// Status
	Check:    "[x]",
	Cross:    "[X]",
	Dot:      "*",
	Circle:   "o",
	Star:     "*",
	Warning:  "!",
	Info:     "i",
	Question: "?",

	// Objects
	Folder:   "[D]",
	File:     "[F]",
	Terminal: "[>",
	Pane:     "#",
	Window:   "[]",
	Session:  "[]",

	// Actions
	Send:   ">>",
	Target: "(o)",
	Search: "[?]",
	Filter: "[*]",
	Copy:   "[C]",
	Save:   "[S]",
	Kill:   "[K]",
	Zoom:   "[+]",
	View:   "[V]",

	// Branding
	Palette: "[P]",
	Robot:   "[R]",
	Claude:  "C",
	Codex:   "O",
	Gemini:  "G",
	All:     "*",
	User:    "U",

	// Categories
	Quick:         "!",
	CodeQuality:   "#",
	Coordination:  "<>",
	Investigation: "?",
	General:       "-",

	// Decorations
	Sparkle:   "*",
	Fire:      "*",
	Lightning: "!",
	Rocket:    ">>",
	Gear:      "@",
}

// HasNerdFonts detects if the terminal likely supports Nerd Fonts
func HasNerdFonts() bool {
	// Explicit user preference
	if os.Getenv("NTM_USE_ICONS") == "1" || os.Getenv("NERD_FONTS") == "1" {
		return true
	}
	if os.Getenv("NTM_USE_ICONS") == "0" || os.Getenv("NERD_FONTS") == "0" {
		return false
	}

	// Check for Powerlevel10k config (strong indicator)
	home, _ := os.UserHomeDir()
	if _, err := os.Stat(filepath.Join(home, ".p10k.zsh")); err == nil {
		return true
	}

	// Check terminal programs known to support Nerd Fonts well
	term := os.Getenv("TERM_PROGRAM")
	switch term {
	case "iTerm.app", "WezTerm", "Alacritty", "kitty", "Hyper":
		return true
	}

	// Check for Kitty
	if os.Getenv("KITTY_WINDOW_ID") != "" {
		return true
	}

	// Check for WezTerm
	if os.Getenv("WEZTERM_PANE") != "" {
		return true
	}

	// Check for VS Code integrated terminal
	if os.Getenv("TERM_PROGRAM") == "vscode" {
		return true
	}

	// Check for common Nerd Font environment hints
	if strings.Contains(strings.ToLower(os.Getenv("TERM")), "256color") {
		// Modern terminal, likely supports Unicode well
		// Default to Unicode icons (not full Nerd Fonts)
		return false
	}

	return false
}

// HasUnicode detects if the terminal supports Unicode
func HasUnicode() bool {
	lang := os.Getenv("LANG")
	lcAll := os.Getenv("LC_ALL")

	if strings.Contains(strings.ToLower(lang), "utf") ||
		strings.Contains(strings.ToLower(lcAll), "utf") {
		return true
	}

	// Most modern terminals support Unicode
	term := os.Getenv("TERM")
	if strings.Contains(term, "xterm") ||
		strings.Contains(term, "256color") ||
		strings.Contains(term, "screen") ||
		strings.Contains(term, "tmux") {
		return true
	}

	return true // Default to Unicode in modern era
}

// Detect returns the appropriate icon set for the current terminal
func Detect() IconSet {
	// Explicit preference via env var
	switch os.Getenv("NTM_ICONS") {
	case "nerd", "nerdfonts":
		return NerdFonts
	case "unicode":
		return Unicode
	case "ascii":
		return ASCII
	case "auto":
		if HasNerdFonts() {
			return NerdFonts
		}
		if HasUnicode() {
			return Unicode
		}
	}

	// Legacy: NTM_USE_ICONS or NERD_FONTS env vars (explicit opt-in)
	if os.Getenv("NTM_USE_ICONS") == "1" || os.Getenv("NERD_FONTS") == "1" {
		return NerdFonts
	}

	// Default to ASCII to avoid width drift issues.
	return ASCII
}

// Default is the auto-detected icon set
var Default = Detect()

// Current returns the currently active icon set
func Current() IconSet {
	return Default
}

// SetDefault allows overriding the default icon set
func SetDefault(icons IconSet) {
	Default = icons
}

// AgentIcon returns the icon for an agent type
func (i IconSet) AgentIcon(agentType string) string {
	switch agentType {
	case "cc", "claude":
		return i.Claude
	case "cod", "codex":
		return i.Codex
	case "gmi", "gemini":
		return i.Gemini
	case "user":
		return i.Terminal
	default:
		return i.Robot
	}
}

// CategoryIcon returns the icon for a palette category
func (i IconSet) CategoryIcon(category string) string {
	switch strings.ToLower(category) {
	case "quick actions", "quick":
		return i.Quick
	case "code quality", "quality":
		return i.CodeQuality
	case "coordination", "coord":
		return i.Coordination
	case "investigation", "investigate":
		return i.Investigation
	default:
		return i.General
	}
}

// StatusIcon returns a colored status icon string
func (i IconSet) StatusIcon(success bool) string {
	if success {
		return i.Check
	}
	return i.Cross
}
