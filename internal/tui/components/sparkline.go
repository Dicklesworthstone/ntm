// Package components provides shared TUI building blocks.
package components

import (
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// sparkBlocks are Unicode block elements ordered by height fraction.
// Index 0 = empty, index 8 = full block.
var sparkBlocks = [9]rune{' ', '▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

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

	// Render each character with color based on value
	var result strings.Builder
	for _, v := range visible {
		var idx int
		var normalized float64
		if rng == 0 {
			// All values identical: flat line at half height, use green
			idx = 4
			normalized = 0
		} else {
			normalized = (v - minVal) / rng
			idx = int(math.Round(normalized * 8))
		}
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
		result.WriteString(style.Render(string(sparkBlocks[idx])))
	}

	return result.String()
}

// SparklineWithLabel renders a sparkline prefixed with a label and current value.
// Example: "tpm ▁▂▃▅▇▆▅▃ 2400"
func SparklineWithLabel(label string, values []float64, width int, currentValue string) string {
	if width <= 0 {
		return ""
	}

	t := theme.Current()

	labelStyle := lipgloss.NewStyle().
		Foreground(t.Overlay)
	valueStyle := lipgloss.NewStyle().
		Foreground(t.Text).
		Bold(true)

	labelWidth := lipgloss.Width(label)
	valueWidth := lipgloss.Width(currentValue)

	switch {
	case label == "" && currentValue == "":
		return SparklineStyled(values, width)
	case label == "":
		if width <= valueWidth {
			return valueStyle.Render(truncateCell(currentValue, width))
		}
		sparkWidth := width - valueWidth - 1
		if sparkWidth < 1 {
			return valueStyle.Render(truncateCell(currentValue, width))
		}
		spark := SparklineStyled(values, sparkWidth)
		if spark == "" {
			return valueStyle.Render(truncateCell(currentValue, width))
		}
		return spark + " " + valueStyle.Render(currentValue)
	case currentValue == "":
		if width <= labelWidth {
			return labelStyle.Render(truncateCell(label, width))
		}
		sparkWidth := width - labelWidth - 1
		if sparkWidth < 1 {
			return labelStyle.Render(truncateCell(label, width))
		}
		spark := SparklineStyled(values, sparkWidth)
		if spark == "" {
			return labelStyle.Render(truncateCell(label, width))
		}
		return labelStyle.Render(label) + " " + spark
	}

	staticWidth := labelWidth + valueWidth + 2 // spaces around the sparkline
	if width <= staticWidth {
		if width <= valueWidth {
			return valueStyle.Render(truncateCell(currentValue, width))
		}
		labelBudget := width - valueWidth - 1
		if labelBudget < 1 {
			return valueStyle.Render(truncateCell(currentValue, width))
		}
		return labelStyle.Render(truncateCell(label, labelBudget)) + " " + valueStyle.Render(currentValue)
	}

	sparkWidth := width - staticWidth
	spark := SparklineStyled(values, sparkWidth)
	if spark == "" {
		return labelStyle.Render(truncateCell(label, width))
	}

	return labelStyle.Render(label) + " " + spark + " " + valueStyle.Render(currentValue)
}
