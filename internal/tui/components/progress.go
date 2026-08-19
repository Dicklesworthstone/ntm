package components

import (
	"fmt"
	"log/slog"
	"strings"

	bubblesprogress "github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
	"github.com/Dicklesworthstone/ntm/internal/tui/terminal"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// ASCII fallback characters for terminals without Unicode block support
const (
	FilledASCII = "="
	EmptyASCII  = "-"
)

// ProgressBar is an animated progress bar with gradient support
type ProgressBar struct {
	Width          int
	Percent        float64
	ShowPercent    bool
	ShowLabel      bool
	Label          string
	GradientColors []string
	EmptyColor     lipgloss.Color
	FilledChar     string
	EmptyChar      string
	Animated       bool
	AnimationTick  int
	valueSpring    *SpringManager
}

// NewProgressBar creates a new progress bar with defaults
func NewProgressBar(width int) ProgressBar {
	t := theme.Current()
	return ProgressBar{
		Width:       width,
		Percent:     0,
		ShowPercent: true,
		ShowLabel:   false,
		GradientColors: []string{
			string(t.Blue),
			string(t.Teal),
			string(t.Green),
		},
		EmptyColor:    t.Surface0,
		FilledChar:    "█",
		EmptyChar:     "░",
		Animated:      styles.AnimationsEnabled() && terminal.SupportsTrueColor(),
		AnimationTick: 0,
		valueSpring:   NewSpringManager(),
	}
}

// View renders the progress bar
func (p ProgressBar) View() string {
	percent := p.Percent
	if p.valueSpring != nil && p.valueSpring.Has("percent") {
		percent = p.valueSpring.Get("percent")
	}
	if percent < 0 {
		percent = 0
	} else if percent > 1 {
		percent = 1
	}

	// Determine characters to use (ASCII fallback for limited terminals)
	filledChar, emptyChar := p.FilledChar, p.EmptyChar
	if !terminal.SupportsUnicodeBlocks() {
		filledChar, emptyChar = FilledASCII, EmptyASCII
	}

	var bar string
	if !p.Animated && len(p.GradientColors) <= 2 {
		bar = p.renderStaticBubblesBar(percent, filledChar, emptyChar)
	} else {
		slog.Debug("progress: using custom component backend", "animated", p.Animated, "colors", len(p.GradientColors), "width", p.Width)
		filledWidth := int(percent * float64(p.Width))
		emptyWidth := p.Width - filledWidth

		// Create filled portion with gradient (or solid color for limited terminals)
		var filledStr string
		if filledWidth > 0 {
			barText := strings.Repeat(filledChar, filledWidth)
			if terminal.SupportsTrueColor() {
				if p.Animated {
					// Animated shimmer effect
					filledStr = styles.Shimmer(barText, p.AnimationTick, p.GradientColors...)
				} else {
					filledStr = styles.GradientText(barText, p.GradientColors...)
				}
			} else {
				// Fallback to solid primary color for terminals without true color
				filledStr = lipgloss.NewStyle().
					Foreground(theme.Current().Primary).
					Render(barText)
			}
		}

		// Create empty portion
		emptyStr := lipgloss.NewStyle().
			Foreground(p.EmptyColor).
			Render(strings.Repeat(emptyChar, emptyWidth))

		bar = filledStr + emptyStr
	}

	if p.ShowPercent {
		percentStr := fmt.Sprintf(" %3.0f%%", percent*100)
		bar += lipgloss.NewStyle().
			Foreground(theme.Current().Text).
			Render(percentStr)
	}

	if p.ShowLabel && p.Label != "" {
		bar = lipgloss.NewStyle().
			Foreground(theme.Current().Subtext).
			Render(p.Label+" ") + bar
	}

	return bar
}

func (p ProgressBar) renderStaticBubblesBar(percent float64, filledChar, emptyChar string) string {
	opts := []bubblesprogress.Option{
		bubblesprogress.WithWidth(p.Width),
		bubblesprogress.WithoutPercentage(),
		bubblesprogress.WithFillCharacters(progressRune(filledChar, '█'), progressRune(emptyChar, '░')),
	}

	if len(p.GradientColors) >= 2 {
		opts = append(opts, bubblesprogress.WithGradient(p.GradientColors[0], p.GradientColors[1]))
	} else {
		opts = append(opts, bubblesprogress.WithDefaultGradient())
	}

	bar := bubblesprogress.New(opts...)
	bar.EmptyColor = string(p.EmptyColor)
	slog.Debug("progress: using bubbles component backend", "colors", len(p.GradientColors), "width", p.Width)
	return bar.ViewAs(percent)
}

// SetPercent updates the progress percentage
func (p *ProgressBar) SetPercent(percent float64) {
	if percent < 0 {
		percent = 0
	} else if percent > 1 {
		percent = 1
	}
	p.Percent = percent

	if p.valueSpring == nil {
		p.valueSpring = NewSpringManager()
	}
	if styles.ReducedMotionEnabled() || !p.valueSpring.Has("percent") {
		p.valueSpring.SetImmediate("percent", percent)
		return
	}
	p.valueSpring.SetWithDeadZone("percent", percent, 5.0, 0.3, 0.02)
}

func progressRune(value string, fallback rune) rune {
	runes := []rune(value)
	if len(runes) == 0 {
		return fallback
	}
	return runes[0]
}
