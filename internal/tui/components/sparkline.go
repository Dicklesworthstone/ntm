// Package components provides shared TUI building blocks.
package components

import (
	"math"

	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// sparkBlocks are Unicode block elements ordered by height fraction.
// Index 0 = empty, index 8 = full block.
var sparkBlocks = [9]rune{' ', '▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// Sparkline renders a compact sparkline chart from a series of values.
// Width determines how many data points are shown (rightmost values).
// The chart auto-scales to the min/max of the visible data.
func Sparkline(values []float64, width int) string {
	if len(values) == 0 || width <= 0 {
		return ""
	}

	// Take the rightmost 'width' values
	start := 0
	if len(values) > width {
		start = len(values) - width
	}
	visible := values[start:]

	// Find min/max for scaling
	minVal, maxVal := visible[0], visible[0]
	for _, v := range visible {
		if v < minVal {
			minVal = v
		}
		if v > maxVal {
			maxVal = v
		}
	}

	// Build sparkline string
	rng := maxVal - minVal
	if rng == 0 {
		rng = 1 // Avoid division by zero; flat line shows at half height
	}

	runes := make([]rune, len(visible))
	for i, v := range visible {
		normalized := (v - minVal) / rng
		idx := int(math.Round(normalized * 8))
		if idx < 0 {
			idx = 0
		}
		if idx > 8 {
			idx = 8
		}
		runes[i] = sparkBlocks[idx]
	}

	return string(runes)
}

// SparklineStyled renders a colored sparkline with theme-aware gradient.
// Low values use the 'low' color, high values use the 'high' color.
func SparklineStyled(values []float64, width int) string {
	if len(values) == 0 || width <= 0 {
		return ""
	}

	t := theme.Current()

	// Take the rightmost 'width' values
	start := 0
	if len(values) > width {
		start = len(values) - width
	}
	visible := values[start:]

	// Find min/max for scaling
	minVal, maxVal := visible[0], visible[0]
	for _, v := range visible {
		if v < minVal {
			minVal = v
		}
		if v > maxVal {
			maxVal = v
		}
	}

	rng := maxVal - minVal
	if rng == 0 {
		rng = 1
	}

	// Render each character with color based on value
	var result string
	for _, v := range visible {
		normalized := (v - minVal) / rng
		idx := int(math.Round(normalized * 8))
		if idx < 0 {
			idx = 0
		}
		if idx > 8 {
			idx = 8
		}

		// Color gradient: green (low) -> yellow (mid) -> red (high)
		var color lipgloss.Color
		switch {
		case normalized < 0.33:
			color = t.Green
		case normalized < 0.66:
			color = t.Yellow
		default:
			color = t.Red
		}

		style := lipgloss.NewStyle().Foreground(color)
		result += style.Render(string(sparkBlocks[idx]))
	}

	return result
}

// SparklineWithLabel renders a sparkline prefixed with a label and current value.
// Example: "tpm ▁▂▃▅▇▆▅▃ 2400"
func SparklineWithLabel(label string, values []float64, width int, currentValue string) string {
	t := theme.Current()

	labelStyled := lipgloss.NewStyle().
		Foreground(t.Overlay).
		Render(label)

	valueStyled := lipgloss.NewStyle().
		Foreground(t.Text).
		Bold(true).
		Render(currentValue)

	// Calculate sparkline width after label and value
	labelWidth := lipgloss.Width(labelStyled)
	valueWidth := lipgloss.Width(valueStyled)
	sparkWidth := width - labelWidth - valueWidth - 2 // 2 for spaces
	if sparkWidth < 4 {
		sparkWidth = 4
	}

	spark := SparklineStyled(values, sparkWidth)

	return labelStyled + " " + spark + " " + valueStyled
}
