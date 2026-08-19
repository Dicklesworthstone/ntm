// Package terminal provides terminal capability detection for graceful fallbacks.
package terminal

import (
	"os"
	"strings"
	"sync"
)

// Capabilities holds detected terminal capabilities
type Capabilities struct {
	TrueColor     bool // Supports 24-bit RGB colors
	UnicodeBlocks bool // Supports Unicode block characters
	Term          string
	ColorTerm     string
}

var capabilityCache struct {
	sync.RWMutex
	value Capabilities
	valid bool
}

// Detect returns the current terminal's capabilities.
// Results are cached for performance.
func Detect() Capabilities {
	capabilityCache.RLock()
	if capabilityCache.valid {
		caps := capabilityCache.value
		capabilityCache.RUnlock()
		return caps
	}
	capabilityCache.RUnlock()

	capabilityCache.Lock()
	defer capabilityCache.Unlock()

	if capabilityCache.valid {
		return capabilityCache.value
	}

	caps := Capabilities{
		Term:      os.Getenv("TERM"),
		ColorTerm: os.Getenv("COLORTERM"),
	}

	caps.TrueColor = supportsTrueColor(caps.Term, caps.ColorTerm)
	caps.UnicodeBlocks = supportsUnicodeBlocks(caps.Term)

	capabilityCache.value = caps
	capabilityCache.valid = true
	return caps
}

// SupportsTrueColor checks if the terminal supports 24-bit RGB colors.
func SupportsTrueColor() bool {
	return Detect().TrueColor
}

// SupportsUnicodeBlocks checks if the terminal supports Unicode block characters.
func SupportsUnicodeBlocks() bool {
	return Detect().UnicodeBlocks
}

// supportsTrueColor checks for 24-bit color support via COLORTERM or TERM.
func supportsTrueColor(term, colorterm string) bool {
	// COLORTERM is the most reliable indicator
	ct := strings.ToLower(colorterm)
	if ct == "truecolor" || ct == "24bit" {
		return true
	}

	// Some terminals advertise in TERM
	t := strings.ToLower(term)
	if strings.Contains(t, "truecolor") || strings.Contains(t, "24bit") {
		return true
	}

	// Modern terminals that typically support true color
	if strings.Contains(t, "kitty") ||
		strings.Contains(t, "iterm") ||
		strings.Contains(t, "alacritty") ||
		strings.Contains(t, "wezterm") ||
		strings.Contains(t, "ghostty") {
		return true
	}

	// 256-color terminals often support true color (but not guaranteed)
	// Be conservative here - if no explicit truecolor, assume false
	return false
}

// supportsUnicodeBlocks checks for Unicode block character support.
func supportsUnicodeBlocks(term string) bool {
	t := strings.ToLower(term)

	// Check for known limited terminals
	if t == "dumb" || t == "vt100" || t == "" {
		return false
	}

	// Check for ASCII-only mode
	if os.Getenv("NO_UNICODE") == "1" {
		return false
	}

	// Check locale - if not UTF-8, blocks may not render
	lang := os.Getenv("LANG")
	lcAll := os.Getenv("LC_ALL")
	lcCtype := os.Getenv("LC_CTYPE")

	hasUTF8 := strings.Contains(strings.ToLower(lang), "utf-8") ||
		strings.Contains(strings.ToLower(lang), "utf8") ||
		strings.Contains(strings.ToLower(lcAll), "utf-8") ||
		strings.Contains(strings.ToLower(lcAll), "utf8") ||
		strings.Contains(strings.ToLower(lcCtype), "utf-8") ||
		strings.Contains(strings.ToLower(lcCtype), "utf8")

	// Most modern terminals support UTF-8/Unicode
	// Only disable for explicitly limited terminals
	if strings.Contains(t, "xterm") ||
		strings.Contains(t, "256color") ||
		strings.Contains(t, "screen") ||
		strings.Contains(t, "tmux") ||
		strings.Contains(t, "linux") {
		return true
	}

	return hasUTF8
}
