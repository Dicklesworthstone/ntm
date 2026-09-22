package dashboard

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
)

// TestWideLayoutsFitTerminalWidth renders every multi-column layout at the
// boundaries of its tier and asserts that no rendered line is wider than the
// terminal. GH#327 reported the ultra right column running past the terminal
// edge; the border/padding budget in layout proportions must match what the
// dashboard actually draws around each panel.
func TestWideLayoutsFitTerminalWidth(t *testing.T) {
	cases := []struct {
		name   string
		width  int
		render func(Model) string
	}{
		{"ultra-min", layout.UltraWideViewThreshold, Model.renderUltraLayout},
		{"ultra-mid", 280, Model.renderUltraLayout},
		{"ultra-max", layout.MegaWideViewThreshold - 1, Model.renderUltraLayout},
		{"mega-min", layout.MegaWideViewThreshold, Model.renderMegaLayout},
		{"mega-wide", 400, Model.renderMegaLayout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, panes := range []int{1, 10, 40} {
				m := newBenchModel(tc.width, 50, panes)
				out := tc.render(m)
				if out == "" {
					t.Fatalf("width=%d panes=%d rendered nothing", tc.width, panes)
				}
				for i, line := range strings.Split(out, "\n") {
					if w := lipgloss.Width(line); w > tc.width {
						t.Fatalf("width=%d panes=%d: line %d is %d columns wide (overflows by %d):\n%q",
							tc.width, panes, i, w, w-tc.width, line)
					}
				}
			}
		})
	}
}
