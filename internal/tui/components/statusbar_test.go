package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestRenderStatusBar_ZeroWidth(t *testing.T) {
	got := RenderStatusBar(StatusBarOptions{Width: 0})
	if got != "" {
		t.Errorf("expected empty string for zero width, got %q", got)
	}
}

func TestRenderStatusBar_EmptySession(t *testing.T) {
	got := RenderStatusBar(StatusBarOptions{
		Width:   80,
		Session: "",
	})
	if got == "" {
		t.Fatal("expected non-empty output for default session")
	}
	// Should contain the dash placeholder for empty session
	if !strings.Contains(got, "—") {
		t.Error("expected dash placeholder for empty session name")
	}
}

func TestRenderStatusBar_ZeroAgentCounts(t *testing.T) {
	got := RenderStatusBar(StatusBarOptions{
		Width:       80,
		Session:     "test-session",
		ClaudeCount: 0,
		CodexCount:  0,
		GeminiCount: 0,
		UserCount:   0,
	})
	if got == "" {
		t.Fatal("expected non-empty output")
	}
	// Agent badge labels should NOT appear when counts are zero
	for _, label := range []string{"CC:", "COD:", "GMI:", "USR:"} {
		if strings.Contains(got, label) {
			t.Errorf("expected no %s badge when count is zero", label)
		}
	}
}

func TestRenderStatusBar_AgentBadgesPresent(t *testing.T) {
	got := RenderStatusBar(StatusBarOptions{
		Width:       120,
		Session:     "dev",
		ClaudeCount: 3,
		CodexCount:  1,
		GeminiCount: 2,
		UserCount:   1,
	})
	for _, label := range []string{"CC:3", "COD:1", "GMI:2", "USR:1"} {
		if !strings.Contains(got, label) {
			t.Errorf("expected badge %q in output", label)
		}
	}
}

func TestRenderStatusBar_AllLayoutTiers(t *testing.T) {
	tiers := []string{"Narrow", "Split", "Wide", "Ultra", "Mega"}
	for _, tier := range tiers {
		t.Run(tier, func(t *testing.T) {
			got := RenderStatusBar(StatusBarOptions{
				Width:      100,
				Session:    "s",
				LayoutTier: tier,
			})
			if !strings.Contains(got, tier) {
				t.Errorf("expected layout tier %q in output", tier)
			}
		})
	}
}

func TestRenderStatusBar_UnknownLayoutTier(t *testing.T) {
	got := RenderStatusBar(StatusBarOptions{
		Width:      100,
		Session:    "s",
		LayoutTier: "Custom",
	})
	if !strings.Contains(got, "Custom") {
		t.Error("expected custom tier label in output")
	}
}

func TestRenderStatusBar_EmptyLayoutTier(t *testing.T) {
	got := RenderStatusBar(StatusBarOptions{
		Width:   80,
		Session: "s",
	})
	// Should show dash placeholder for empty tier
	if got == "" {
		t.Fatal("expected non-empty output")
	}
}

func TestRenderStatusBar_FocusedPanel(t *testing.T) {
	got := RenderStatusBar(StatusBarOptions{
		Width:        120,
		Session:      "s",
		FocusedPanel: "Detail",
	})
	if !strings.Contains(got, "Detail") {
		t.Error("expected focused panel name in output")
	}
}

func TestRenderStatusBar_PausedIndicator(t *testing.T) {
	got := RenderStatusBar(StatusBarOptions{
		Width:   80,
		Session: "s",
		Paused:  true,
	})
	if !strings.Contains(got, "⏸") {
		t.Error("expected paused indicator in output")
	}
}

func TestRenderStatusBar_NarrowWidth(t *testing.T) {
	// Even at very narrow widths the function should not panic
	got := RenderStatusBar(StatusBarOptions{
		Width:        10,
		Session:      "long-session-name",
		ClaudeCount:  5,
		CodexCount:   3,
		GeminiCount:  2,
		FocusedPanel: "Detail",
		LayoutTier:   "Ultra",
	})
	if got == "" {
		t.Fatal("expected non-empty output even at narrow width")
	}
	// The visible width of the bar line should not exceed opts.Width
	// (border line is the first line)
	lines := strings.Split(got, "\n")
	if len(lines) < 2 {
		t.Fatal("expected at least two lines (border + bar)")
	}
	borderWidth := lipgloss.Width(lines[0])
	if borderWidth > 10 {
		t.Errorf("border width %d exceeds requested width 10", borderWidth)
	}
}

func TestRenderStatusBar_NegativeWidth(t *testing.T) {
	got := RenderStatusBar(StatusBarOptions{Width: -1})
	if got != "" {
		t.Errorf("expected empty string for negative width, got %q", got)
	}
}
