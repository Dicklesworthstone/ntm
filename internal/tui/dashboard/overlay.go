package dashboard

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// renderModalOverlay composites a centered modal on top of a dimmed backdrop.
// The backdrop (the full dashboard render) is stripped of ANSI colors and
// re-rendered in the theme's Surface1 color to create a muted "dim" effect.
// The modal is drawn centered over this dimmed backdrop with a drop shadow.
func renderModalOverlay(backdrop, modal string, width, height int, t theme.Theme) string {
	dimmed := dimBackdrop(backdrop, width, height, t)
	shadowed := addDropShadow(modal, t)

	// Center the shadowed modal within a transparent canvas the size of the
	// terminal, then overlay it onto the dimmed backdrop line by line.
	placed := lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, shadowed)
	return compositeOver(dimmed, placed, width, height)
}

// dimBackdrop strips ANSI escapes from the backdrop and re-renders every
// character in the theme Surface1 color, producing a uniform dim effect that
// preserves the spatial layout of the underlying dashboard.
func dimBackdrop(backdrop string, width, height int, t theme.Theme) string {
	plain := ansi.Strip(backdrop)
	dimStyle := lipgloss.NewStyle().Foreground(t.Surface1)

	lines := strings.Split(plain, "\n")
	for i, line := range lines {
		lines[i] = dimStyle.Render(line)
	}

	result := strings.Join(lines, "\n")
	// Ensure the dimmed backdrop fills the full terminal area.
	return lipgloss.Place(width, height, lipgloss.Left, lipgloss.Top, result)
}

// addDropShadow adds a 1-cell dark border on the right and bottom edges of
// the modal string, simulating a drop shadow cast to the bottom-right.
func addDropShadow(modal string, t theme.Theme) string {
	shadowStyle := lipgloss.NewStyle().
		Background(t.Crust).
		Foreground(t.Crust)

	modalWidth := lipgloss.Width(modal)
	modalLines := strings.Split(modal, "\n")

	var result []string
	for _, line := range modalLines {
		// Pad line to modal width, then append 1-char shadow on the right.
		padded := line + strings.Repeat(" ", modalWidth-lipgloss.Width(line))
		result = append(result, padded+shadowStyle.Render(" "))
	}

	// Bottom shadow row: offset 1 char right, spanning the modal width.
	result = append(result, " "+shadowStyle.Render(strings.Repeat(" ", modalWidth)))

	return strings.Join(result, "\n")
}

// compositeOver overlays the foreground onto the background, replacing
// background characters wherever the foreground has a non-space visible
// character. Both strings must be newline-delimited and sized to (width x height).
// This uses a simple heuristic: foreground lines that are not all spaces replace
// the corresponding background lines entirely.
func compositeOver(background, foreground string, width, height int) string {
	bgLines := strings.Split(background, "\n")
	fgLines := strings.Split(foreground, "\n")

	// Ensure both slices have at least height entries.
	for len(bgLines) < height {
		bgLines = append(bgLines, "")
	}
	for len(fgLines) < height {
		fgLines = append(fgLines, "")
	}

	out := make([]string, height)
	for i := 0; i < height; i++ {
		// Check if the foreground line has any visible (non-space) content
		// after stripping ANSI codes.
		fgPlain := ansi.Strip(fgLines[i])
		if strings.TrimSpace(fgPlain) != "" {
			out[i] = fgLines[i]
		} else {
			out[i] = bgLines[i]
		}
	}
	return strings.Join(out, "\n")
}
