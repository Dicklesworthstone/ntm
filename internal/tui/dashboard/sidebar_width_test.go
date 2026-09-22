package dashboard

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/tui/dashboard/panels"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
)

// TestUltraSidebarPanelsFitTerminalWidth populates the sidebar panels GH#327
// named (Attention, Activity & Locks, Network Activity, RCH Build Offload)
// with over-long content and asserts the ultra layout still never renders a
// line wider than the terminal.
func TestUltraSidebarPanelsFitTerminalWidth(t *testing.T) {
	long := strings.Repeat("Ack required from CoralCedar: Contact request needs a decision now. ", 5)
	ranoErr := errors.New("rano stats failed: exit status 1: error: Unexpected argument '--json' found; run with --help for the full usage of this subcommand")
	for _, width := range []int{layout.UltraWideViewThreshold, 280, layout.MegaWideViewThreshold - 1} {
		m := newBenchModel(width, 50, 12)
		m.attentionItems = []panels.AttentionItem{
			{Summary: long, Timestamp: time.Now(), SourcePane: 3, SourceAgent: "claude"},
			{Summary: strings.Repeat("x", 400), Timestamp: time.Now(), SourcePane: 4, SourceAgent: "codex"},
		}
		m.attentionFeedOK = true
		m.attentionPanel.SetData(m.attentionItems, true)
		m.agentMailLockInfo = []AgentMailLockInfo{{
			PathPattern: strings.Repeat("thrivalist-proxyapp/src/very/deep/path/", 6) + "file.go",
			AgentName:   "thrivalist-proxyapp__cc_1_claude-opus-4-8-no-thinking-extra-long",
			ExpiresIn:   "59m",
		}}
		m.ranoNetworkPanel.SetData(panels.RanoNetworkPanelData{Loaded: true, Enabled: true, Error: ranoErr})
		m.rchPanel.SetData(panels.RCHPanelData{Loaded: true, Enabled: true, Error: errors.New(long)})
		for key, st := range m.paneStatus {
			st.State = "error"
			m.paneStatus[key] = st
		}

		out := m.renderUltraLayout()
		if out == "" {
			t.Fatalf("width=%d rendered nothing", width)
		}
		maxWidth := 0
		for i, line := range strings.Split(out, "\n") {
			w := lipgloss.Width(line)
			if w > maxWidth {
				maxWidth = w
			}
			if w > width {
				t.Fatalf("width=%d: line %d is %d columns wide (overflows by %d):\n%q", width, i, w, w-width, line)
			}
		}
		t.Logf("terminal=%d widest line=%d", width, maxWidth)
	}
}
