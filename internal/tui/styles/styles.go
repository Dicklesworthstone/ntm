// Package styles provides advanced styling primitives for stunning TUI effects
package styles

import (
	"fmt"
	"log/slog"
	"math"
	"os"
	"strings"

	bubblesprogress "github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/truncate"

	"github.com/Dicklesworthstone/ntm/internal/tui/terminal"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func envBool(name string) (bool, bool) {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(name)))
	if v == "" {
		return false, false
	}
	switch v {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

// AnimationsEnabled reports whether motion-heavy TUI effects should run.
//
// We bias toward stability in multiplexers and limited terminals because repeated
// full-frame ANSI repaints are the main source of visible flashing/tearing.
func AnimationsEnabled() bool {
	if enabled, ok := envBool("NTM_ANIMATIONS"); ok {
		return enabled
	}
	if reduced, ok := envBool("NTM_REDUCE_MOTION"); ok && reduced {
		return false
	}
	if noColor, ok := envBool("NTM_NO_COLOR"); ok && noColor {
		return false
	}
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	if strings.TrimSpace(os.Getenv("CI")) != "" {
		return false
	}
	if strings.TrimSpace(os.Getenv("TMUX")) != "" || strings.TrimSpace(os.Getenv("STY")) != "" {
		return false
	}

	term := strings.TrimSpace(strings.ToLower(os.Getenv("TERM")))
	if term == "" || term == "dumb" {
		return false
	}

	return terminal.SupportsTrueColor()
}

// ReducedMotionEnabled reports whether TUI motion should be suppressed.
func ReducedMotionEnabled() bool {
	return !AnimationsEnabled()
}

func reducedMotionEnabled() bool {
	return ReducedMotionEnabled()
}

func defaultGradient() []string {
	t := theme.Current()
	return []string{string(t.Blue), string(t.Mauve), string(t.Pink)}
}

func defaultSurface1() lipgloss.Color {
	return theme.Current().Surface1
}

func normalizeProgressChars(filled, empty string) (string, string) {
	if filled == "" {
		filled = "█"
	}
	if empty == "" {
		empty = "░"
	}
	return filled, empty
}

func progressFillRunes(filled, empty string) (rune, rune) {
	filled, empty = normalizeProgressChars(filled, empty)
	fullRunes := []rune(filled)
	emptyRunes := []rune(empty)
	return fullRunes[0], emptyRunes[0]
}

func renderBubblesProgressBar(percent float64, width int, filled, empty string, colors ...string) string {
	fullRune, emptyRune := progressFillRunes(filled, empty)
	opts := []bubblesprogress.Option{
		bubblesprogress.WithWidth(width),
		bubblesprogress.WithoutPercentage(),
		bubblesprogress.WithFillCharacters(fullRune, emptyRune),
	}

	switch len(colors) {
	case 0:
		opts = append(opts, bubblesprogress.WithDefaultGradient())
	default:
		if len(colors) >= 2 {
			opts = append(opts, bubblesprogress.WithGradient(colors[0], colors[1]))
			break
		}
		fallthrough
	case 1:
		opts = append(opts, bubblesprogress.WithDefaultGradient())
	}

	bar := bubblesprogress.New(opts...)
	bar.EmptyColor = string(defaultSurface1())
	slog.Debug("progress: using bubbles backend", "colors", len(colors), "width", width)
	return bar.ViewAs(percent)
}

// GradientDirection specifies gradient orientation
type GradientDirection int

const (
	GradientHorizontal GradientDirection = iota
	GradientVertical
	GradientDiagonal
)

// Color represents an RGB color for gradient calculations
type Color struct {
	R, G, B int
}

// ParseHex converts a hex color string to Color
func ParseHex(hex string) Color {
	if len(hex) != 7 || hex[0] != '#' {
		return Color{}
	}

	hexToByte := func(b byte) int {
		switch {
		case b >= '0' && b <= '9':
			return int(b - '0')
		case b >= 'a' && b <= 'f':
			return int(b - 'a' + 10)
		case b >= 'A' && b <= 'F':
			return int(b - 'A' + 10)
		}
		return 0
	}

	r := hexToByte(hex[1])<<4 | hexToByte(hex[2])
	g := hexToByte(hex[3])<<4 | hexToByte(hex[4])
	b := hexToByte(hex[5])<<4 | hexToByte(hex[6])

	return Color{R: r, G: g, B: b}
}

// ToHex converts Color to hex string
func (c Color) ToHex() string {
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}

// ToLipgloss converts to lipgloss.Color
func (c Color) ToLipgloss() lipgloss.Color {
	return lipgloss.Color(c.ToHex())
}

// Lerp interpolates between two colors
func Lerp(c1, c2 Color, t float64) Color {
	return Color{
		R: int(float64(c1.R) + t*(float64(c2.R)-float64(c1.R))),
		G: int(float64(c1.G) + t*(float64(c2.G)-float64(c1.G))),
		B: int(float64(c1.B) + t*(float64(c2.B)-float64(c1.B))),
	}
}

// GradientText applies a horizontal gradient to text
func GradientText(text string, colors ...string) string {
	if len(colors) < 2 || len(text) == 0 {
		return text
	}

	runes := []rune(text)
	n := len(runes)
	if n == 0 {
		return text
	}

	// Parse colors
	parsedColors := make([]Color, len(colors))
	for i, c := range colors {
		parsedColors[i] = ParseHex(c)
	}

	var result strings.Builder
	segments := len(parsedColors) - 1

	for i, r := range runes {
		// Calculate position in gradient (0.0 to 1.0)
		var pos float64
		if n == 1 {
			pos = 0
		} else {
			pos = float64(i) / float64(n-1)
		}

		// Find which segment we're in
		segmentPos := pos * float64(segments)
		segmentIdx := int(segmentPos)
		if segmentIdx >= segments {
			segmentIdx = segments - 1
		}

		// Calculate local position within segment
		localPos := segmentPos - float64(segmentIdx)

		// Interpolate color
		c := Lerp(parsedColors[segmentIdx], parsedColors[segmentIdx+1], localPos)

		// Apply color to character
		result.WriteString(fmt.Sprintf("\x1b[38;2;%d;%d;%dm%c\x1b[0m", c.R, c.G, c.B, r))
	}

	return result.String()
}

// Shimmer creates an animated shimmer effect (returns frame for given tick)
func Shimmer(text string, tick int, colors ...string) string {
	if reducedMotionEnabled() {
		if len(colors) < 2 {
			colors = defaultGradient()
		}
		return GradientText(text, colors...)
	}

	if len(colors) < 2 {
		grad := defaultGradient()
		colors = append(append([]string{}, grad...), grad[0])
	}

	runes := []rune(text)
	n := len(runes)
	if n == 0 {
		return text
	}

	parsedColors := make([]Color, len(colors))
	for i, c := range colors {
		parsedColors[i] = ParseHex(c)
	}

	var result strings.Builder
	segments := len(parsedColors) - 1

	// Offset based on tick for animation (~10s full cycle at 4 FPS)
	offset := float64(tick%40) / 40.0

	for i, r := range runes {
		pos := (float64(i)/float64(n) + offset)
		pos = pos - float64(int(pos)) // Wrap around

		segmentPos := pos * float64(segments)
		segmentIdx := int(segmentPos)
		if segmentIdx >= segments {
			segmentIdx = segments - 1
		}

		localPos := segmentPos - float64(segmentIdx)
		c := Lerp(parsedColors[segmentIdx], parsedColors[segmentIdx+1], localPos)

		result.WriteString(fmt.Sprintf("\x1b[38;2;%d;%d;%dm%c\x1b[0m", c.R, c.G, c.B, r))
	}

	return result.String()
}

// Pulse creates a pulsing brightness effect (returns style for given tick)
func Pulse(baseColor string, tick int) lipgloss.Color {
	if reducedMotionEnabled() {
		return lipgloss.Color(baseColor)
	}

	base := ParseHex(baseColor)

	// Sine wave for smooth pulsing
	brightness := 0.7 + 0.3*math.Sin(float64(tick)*0.1)

	return Color{
		R: clamp(int(float64(base.R) * brightness)),
		G: clamp(int(float64(base.G) * brightness)),
		B: clamp(int(float64(base.B) * brightness)),
	}.ToLipgloss()
}

func clamp(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

// ProgressBar creates a beautiful gradient progress bar.
// For <=2 colors, uses charmbracelet/bubbles/progress for rendering.
// For 3+ colors, uses custom gradient implementation.
func ProgressBar(percent float64, width int, filled, empty string, colors ...string) string {
	if width <= 0 {
		return ""
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 1 {
		percent = 1
	}

	// Default colors if not specified.
	if len(colors) == 0 {
		t := theme.Current()
		colors = []string{string(t.Blue), string(t.Green)}
	}

	// Use bubbles/progress for 2-color gradients or solid fills.
	if len(colors) <= 2 {
		return renderBubblesProgressBar(percent, width, filled, empty, colors...)
	}

	// 3+ colors: use custom gradient implementation
	filled, empty = normalizeProgressChars(filled, empty)
	filledWidth := int(percent * float64(width))
	emptyWidth := width - filledWidth

	filledStr := GradientText(strings.Repeat(filled, filledWidth), colors...)
	emptyStr := lipgloss.NewStyle().Foreground(defaultSurface1()).Render(strings.Repeat(empty, emptyWidth))

	return filledStr + emptyStr
}

// ShimmerProgressBar creates a progress bar with animated shimmer effect on the filled portion
func ShimmerProgressBar(percent float64, width int, filled, empty string, tick int, colors ...string) string {
	if width <= 0 {
		return ""
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 1 {
		percent = 1
	}

	filled, empty = normalizeProgressChars(filled, empty)
	filledWidth := int(percent * float64(width))
	emptyWidth := width - filledWidth

	if len(colors) < 2 {
		t := theme.Current()
		colors = []string{string(t.Blue), string(t.Green)}
	}

	// Create base gradient
	filledStr := GradientText(strings.Repeat(filled, filledWidth), colors...)

	// Apply shimmer effect on top if tick > 0
	if tick > 0 && !ReducedMotionEnabled() {
		filledStr = Shimmer(strings.Repeat(filled, filledWidth), tick, colors...)
	} else if tick > 0 && ReducedMotionEnabled() {
		slog.Debug("progress: shimmer suppressed by reduced motion", "width", width)
	}

	emptyStr := lipgloss.NewStyle().Foreground(defaultSurface1()).Render(strings.Repeat(empty, emptyWidth))

	return filledStr + emptyStr
}

// Spinner frames for animated spinner
var SpinnerFrames = []string{
	"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏",
}

// DotsSpinnerFrames - alternative spinner
var DotsSpinnerFrames = []string{
	"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷",
}

// LineSpinnerFrames - line spinner
var LineSpinnerFrames = []string{
	"—", "\\", "|", "/",
}

// BounceSpinnerFrames - bouncing ball spinner
var BounceSpinnerFrames = []string{
	"⠁", "⠂", "⠄", "⡀", "⢀", "⠠", "⠐", "⠈",
}

// BoxChars defines box drawing characters
type BoxChars struct {
	TopLeft     string
	TopRight    string
	BottomLeft  string
	BottomRight string
	Horizontal  string
	Vertical    string
	TeeLeft     string
	TeeRight    string
	TeeTop      string
	TeeBottom   string
	Cross       string
}

// RoundedBox is a rounded box character set
var RoundedBox = BoxChars{
	TopLeft:     "╭",
	TopRight:    "╮",
	BottomLeft:  "╰",
	BottomRight: "╯",
	Horizontal:  "─",
	Vertical:    "│",
	TeeLeft:     "├",
	TeeRight:    "┤",
	TeeTop:      "┬",
	TeeBottom:   "┴",
	Cross:       "┼",
}

// DoubleBox is a double-line box character set
var DoubleBox = BoxChars{
	TopLeft:     "╔",
	TopRight:    "╗",
	BottomLeft:  "╚",
	BottomRight: "╝",
	Horizontal:  "═",
	Vertical:    "║",
	TeeLeft:     "╠",
	TeeRight:    "╣",
	TeeTop:      "╦",
	TeeBottom:   "╩",
	Cross:       "╬",
}

// HeavyBox is a heavy/thick box character set
var HeavyBox = BoxChars{
	TopLeft:     "┏",
	TopRight:    "┓",
	BottomLeft:  "┗",
	BottomRight: "┛",
	Horizontal:  "━",
	Vertical:    "┃",
	TeeLeft:     "┣",
	TeeRight:    "┫",
	TeeTop:      "┳",
	TeeBottom:   "┻",
	Cross:       "╋",
}

// GradientDivider creates a gradient divider
func GradientDivider(width int, colors ...string) string {
	if width <= 0 {
		return ""
	}
	if len(colors) < 2 {
		colors = []string{"#89b4fa", "#cba6f7"}
	}
	return GradientText(strings.Repeat("─", width), colors...)
}

// AnimatedGradientDivider creates a gradient divider with animated shimmer effect.
// [tui-upgrade: bd-28vsw] The shimmer sweeps across the divider based on tick.
func AnimatedGradientDivider(width, tick int, colors ...string) string {
	if width <= 0 {
		return ""
	}
	if len(colors) < 2 {
		colors = []string{"#89b4fa", "#cba6f7"}
	}
	if ReducedMotionEnabled() {
		return GradientDivider(width, colors...)
	}
	return Shimmer(strings.Repeat("─", width), tick, colors...)
}

// Truncate truncates text to max width with ellipsis
func Truncate(text string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	return truncate.StringWithTail(text, uint(maxWidth), "…")
}

// CenterText centers text within a given width
func CenterText(text string, width int) string {
	visLen := lipgloss.Width(text)
	if visLen >= width {
		return text
	}
	leftPad := (width - visLen) / 2
	rightPad := width - visLen - leftPad
	return strings.Repeat(" ", leftPad) + text + strings.Repeat(" ", rightPad)
}
