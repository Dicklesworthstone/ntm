package components

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tui/icons"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func enableAnimationsForTest(t *testing.T) {
	t.Helper()
	t.Setenv("NTM_ANIMATIONS", "1")
	t.Setenv("NTM_REDUCE_MOTION", "")
	t.Setenv("TMUX", "")
	t.Setenv("STY", "")
	t.Setenv("CI", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")
}

// Banner tests
func TestRenderBanner(t *testing.T) {
	t.Run("static", func(t *testing.T) {
		result := RenderBanner(false, 0)
		if result == "" {
			t.Error("RenderBanner should return non-empty string")
		}
	})

	t.Run("animated", func(t *testing.T) {
		result := RenderBanner(true, 5)
		if result == "" {
			t.Error("RenderBanner animated should return non-empty string")
		}
	})
}

func TestRenderBannerMedium(t *testing.T) {
	t.Run("static", func(t *testing.T) {
		result := RenderBannerMedium(false, 0)
		if result == "" {
			t.Error("RenderBannerMedium should return non-empty string")
		}
	})

	t.Run("animated", func(t *testing.T) {
		result := RenderBannerMedium(true, 5)
		if result == "" {
			t.Error("RenderBannerMedium animated should return non-empty string")
		}
	})
}

func TestRenderSubtitle(t *testing.T) {
	result := RenderSubtitle("Test subtitle")
	if result == "" {
		t.Error("RenderSubtitle should return non-empty string")
	}
}

func TestRenderAgentBadge(t *testing.T) {
	tests := []string{"claude", "cc", "codex", "cod", "gemini", "gmi", "grok", "grok-build", "cursor", "windsurf", "aider", "ollama", "user", "unknown"}

	for _, agent := range tests {
		t.Run(agent, func(t *testing.T) {
			result := RenderAgentBadge(agent)
			if result == "" {
				t.Errorf("RenderAgentBadge(%q) should return non-empty string", agent)
			}
		})
	}
}

func TestRenderAgentBadgeStyle(t *testing.T) {
	currentTheme := theme.Current()
	currentIcons := icons.Current()

	tests := []struct {
		name      string
		agentType string
		wantColor string
		wantIcon  string
	}{
		{"claude", "claude", string(currentTheme.Claude), currentIcons.Claude},
		{"codex alias", "openai-codex", string(currentTheme.Codex), currentIcons.Codex},
		{"gemini alias", "google-gemini", string(currentTheme.Gemini), currentIcons.Gemini},
		{"grok alias", "grok-build", string(currentTheme.Pink), currentIcons.Robot},
		{"cursor", "cursor", string(currentTheme.Cursor), currentIcons.Cursor},
		{"windsurf alias", "ws", string(currentTheme.Windsurf), currentIcons.Windsurf},
		{"aider", "aider", string(currentTheme.Aider), currentIcons.Aider},
		{"ollama", "ollama", string(currentTheme.Ollama), currentIcons.Ollama},
		{"user", "user", string(currentTheme.User), currentIcons.User},
		{"unknown", "mystery", string(currentTheme.Green), ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotColor, gotIcon := renderAgentBadgeStyle(tc.agentType, currentTheme, currentIcons)
			if gotColor != tc.wantColor || gotIcon != tc.wantIcon {
				t.Fatalf("renderAgentBadgeStyle(%q) = (%q, %q), want (%q, %q)", tc.agentType, gotColor, gotIcon, tc.wantColor, tc.wantIcon)
			}
		})
	}
}

func TestRenderAgentBadgeCanonicalizesLabel(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		wantLabel string
	}{
		{"codex alias", "openai-codex", "CODEX"},
		{"gemini alias", "google-gemini", "GEMINI"},
		{"grok alias", "grok-build", "GROK"},
		{"windsurf alias", "ws", "WINDSURF"},
		{"empty", "", "UNKNOWN"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := RenderAgentBadge(tc.agentType); !strings.Contains(got, tc.wantLabel) {
				t.Fatalf("RenderAgentBadge(%q) = %q, want label %q", tc.agentType, got, tc.wantLabel)
			}
		})
	}
}

// Logo constants tests
func TestLogoConstants(t *testing.T) {
	if len(LogoLarge) == 0 {
		t.Error("LogoLarge should not be empty")
	}
	if len(LogoMedium) == 0 {
		t.Error("LogoMedium should not be empty")
	}
	if LogoSmall == "" {
		t.Error("LogoSmall should not be empty")
	}
	if LogoIcon == "" {
		t.Error("LogoIcon should not be empty")
	}
	if LogoIconPlain == "" {
		t.Error("LogoIconPlain should not be empty")
	}
}

// Progress tests
func TestNewProgressBar(t *testing.T) {
	pb := NewProgressBar(40)
	if pb.Width != 40 {
		t.Error("NewProgressBar should set width")
	}
}

func TestProgressBarSetPercent(t *testing.T) {
	pb := NewProgressBar(40)
	pb.SetPercent(0.5)
	if pb.Percent != 0.5 {
		t.Error("SetPercent should set percent")
	}

	// Test bounds
	pb.SetPercent(-0.5)
	if pb.Percent != 0 {
		t.Error("SetPercent should clamp to 0")
	}

	pb.SetPercent(1.5)
	if pb.Percent != 1.0 {
		t.Error("SetPercent should clamp to 1")
	}
}

func TestProgressBarView(t *testing.T) {
	pb := NewProgressBar(40)
	pb.SetPercent(0.5)
	result := pb.View()
	if result == "" {
		t.Error("ProgressBar.View should return non-empty string")
	}
}

// Gradient function tests
func TestGradientFunctions(t *testing.T) {
	t.Run("gradientPrimary", func(t *testing.T) {
		colors := gradientPrimary()
		if len(colors) == 0 {
			t.Error("gradientPrimary should return colors")
		}
	})
}

func TestRenderState(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		out := RenderState(StateOptions{
			Kind:    StateEmpty,
			Message: "No items",
			Width:   40,
		})
		if out == "" {
			t.Fatal("expected non-empty output")
		}
		if !strings.Contains(out, "No items") {
			t.Fatalf("expected message to be included, got %q", out)
		}
	})

	t.Run("loading has default message", func(t *testing.T) {
		out := RenderState(StateOptions{
			Kind:  StateLoading,
			Width: 40,
		})
		if out == "" {
			t.Fatal("expected non-empty output")
		}
		if !strings.Contains(out, "Loading") {
			t.Fatalf("expected loading message, got %q", out)
		}
	})

	t.Run("error includes hint", func(t *testing.T) {
		out := RenderState(StateOptions{
			Kind:    StateError,
			Message: "Bad news",
			Hint:    "Press r to retry",
			Width:   40,
		})
		if !strings.Contains(out, "Bad news") || !strings.Contains(out, "Press r") {
			t.Fatalf("expected message and hint, got %q", out)
		}
	})

	t.Run("truncates to width", func(t *testing.T) {
		out := RenderState(StateOptions{
			Kind:    StateEmpty,
			Message: "this is a very very long message",
			Width:   10,
		})
		if !strings.Contains(out, "…") {
			t.Fatalf("expected truncation ellipsis, got %q", out)
		}
	})
}

func TestRenderKeyHint(t *testing.T) {
	hint := KeyHint{Key: "Enter", Desc: "select"}
	out := RenderKeyHint(hint)

	if out == "" {
		t.Fatal("expected non-empty output")
	}
	if !strings.Contains(out, "Enter") {
		t.Fatalf("expected key to be included, got %q", out)
	}
	if !strings.Contains(out, "select") {
		t.Fatalf("expected desc to be included, got %q", out)
	}
}

func TestRenderKeyHintCompact(t *testing.T) {
	hint := KeyHint{Key: "q", Desc: "quit"}
	out := RenderKeyHintCompact(hint)

	if out == "" {
		t.Fatal("expected non-empty output")
	}
	if !strings.Contains(out, "q") {
		t.Fatalf("expected key to be included, got %q", out)
	}
	if !strings.Contains(out, "quit") {
		t.Fatalf("expected desc to be included, got %q", out)
	}
}

func TestRenderHelpBar(t *testing.T) {
	t.Run("renders multiple hints", func(t *testing.T) {
		hints := []KeyHint{
			{Key: "↑/↓", Desc: "navigate"},
			{Key: "Enter", Desc: "select"},
			{Key: "q", Desc: "quit"},
		}
		out := RenderHelpBar(HelpBarOptions{Hints: hints})

		if out == "" {
			t.Fatal("expected non-empty output")
		}
		if !strings.Contains(out, "navigate") {
			t.Error("expected 'navigate' in output")
		}
		if !strings.Contains(out, "select") {
			t.Error("expected 'select' in output")
		}
		if !strings.Contains(out, "quit") {
			t.Error("expected 'quit' in output")
		}
	})

	t.Run("empty hints returns empty string", func(t *testing.T) {
		out := RenderHelpBar(HelpBarOptions{Hints: nil})
		if out != "" {
			t.Fatalf("expected empty string, got %q", out)
		}
	})

	t.Run("truncates to width", func(t *testing.T) {
		hints := []KeyHint{
			{Key: "↑/↓", Desc: "navigate"},
			{Key: "Enter", Desc: "select"},
			{Key: "Esc", Desc: "back"},
			{Key: "q", Desc: "quit"},
		}
		// Very narrow width should drop some hints
		out := RenderHelpBar(HelpBarOptions{Hints: hints, Width: 30})

		// Should have at least one hint
		if out == "" {
			t.Fatal("expected at least some output")
		}
		// Shouldn't have all four if space is limited
		// Just verify it doesn't panic and produces something
	})

	t.Run("uses custom separator", func(t *testing.T) {
		hints := []KeyHint{
			{Key: "a", Desc: "one"},
			{Key: "b", Desc: "two"},
		}
		out := RenderHelpBar(HelpBarOptions{Hints: hints, Separator: " | "})
		if !strings.Contains(out, "|") {
			t.Error("expected custom separator in output")
		}
	})
}

func TestHelpOverlay(t *testing.T) {
	t.Run("renders with sections", func(t *testing.T) {
		opts := HelpOverlayOptions{
			Title: "Test Help",
			Sections: []HelpSection{
				{
					Title: "Navigation",
					Hints: []KeyHint{
						{Key: "↑", Desc: "Up"},
						{Key: "↓", Desc: "Down"},
					},
				},
				{
					Title: "Actions",
					Hints: []KeyHint{
						{Key: "Enter", Desc: "Select"},
					},
				},
			},
		}
		out := HelpOverlay(opts)

		if out == "" {
			t.Fatal("expected non-empty output")
		}
		if !strings.Contains(out, "Test Help") {
			t.Error("expected title in output")
		}
		if !strings.Contains(out, "Navigation") {
			t.Error("expected section title in output")
		}
		if !strings.Contains(out, "Up") {
			t.Error("expected hint desc in output")
		}
	})

	t.Run("uses default title", func(t *testing.T) {
		opts := HelpOverlayOptions{
			Sections: []HelpSection{
				{Hints: []KeyHint{{Key: "q", Desc: "quit"}}},
			},
		}
		out := HelpOverlay(opts)
		if !strings.Contains(out, "Keyboard Shortcuts") {
			t.Error("expected default title")
		}
	})

	t.Run("includes footer hint", func(t *testing.T) {
		opts := HelpOverlayOptions{
			Sections: []HelpSection{
				{Hints: []KeyHint{{Key: "x", Desc: "test"}}},
			},
		}
		out := HelpOverlay(opts)
		if !strings.Contains(out, "Esc") {
			t.Error("expected footer hint about Esc to close")
		}
	})
}

func TestDashboardHelpOptionsFrom(t *testing.T) {
	t.Run("minimal", func(t *testing.T) {
		opts := DashboardHelpOptionsFrom(" minimal ", false)
		if opts.Verbosity != DashboardHelpVerbosityMinimal {
			t.Fatalf("expected minimal verbosity, got %v", opts.Verbosity)
		}
		if opts.Debug {
			t.Fatal("expected debug false")
		}
	})

	t.Run("full default", func(t *testing.T) {
		opts := DashboardHelpOptionsFrom("full", false)
		if opts.Verbosity != DashboardHelpVerbosityFull {
			t.Fatalf("expected full verbosity, got %v", opts.Verbosity)
		}
	})

	t.Run("debug_adds_debug_hints", func(t *testing.T) {
		opts := DashboardHelpOptionsFrom("minimal", true)
		if opts.Verbosity != DashboardHelpVerbosityFull {
			t.Fatalf("expected full verbosity when debug, got %v", opts.Verbosity)
		}
		if !opts.Debug {
			t.Fatal("expected debug true")
		}
	})
}

func TestDashboardHelpVerbosityMapping(t *testing.T) {
	t.Parallel()

	hasDesc := func(hints []KeyHint, want string) bool {
		for _, h := range hints {
			if h.Desc == want {
				return true
			}
		}
		return false
	}

	t.Run("minimal", func(t *testing.T) {
		hints := DashboardHelpBarHints(DashboardHelpOptions{Verbosity: DashboardHelpVerbosityMinimal})
		if !hasDesc(hints, "navigate") || !hasDesc(hints, "select") || !hasDesc(hints, "help") || !hasDesc(hints, "quit") {
			t.Fatalf("expected minimal hints to include navigate/select/help/quit, got %#v", hints)
		}
		if hasDesc(hints, "zoom") || hasDesc(hints, "refresh") {
			t.Fatalf("expected minimal hints to exclude zoom/refresh, got %#v", hints)
		}
	})

	t.Run("full", func(t *testing.T) {
		hints := DashboardHelpBarHints(DashboardHelpOptions{Verbosity: DashboardHelpVerbosityFull})
		if !hasDesc(hints, "zoom") || !hasDesc(hints, "refresh") {
			t.Fatalf("expected full hints to include zoom/refresh, got %#v", hints)
		}
		if hasDesc(hints, "send") {
			t.Fatalf("expected full hints to omit dead send action, got %#v", hints)
		}
	})

	t.Run("debug_adds_debug_hints", func(t *testing.T) {
		hints := DashboardHelpBarHints(DashboardHelpOptions{Verbosity: DashboardHelpVerbosityFull, Debug: true})
		if !hasDesc(hints, "diag") || !hasDesc(hints, "scan") || !hasDesc(hints, "checkpoint") {
			t.Fatalf("expected debug hints to include diag/scan/checkpoint, got %#v", hints)
		}
	})
}

// ============================================================================
// Regression Snapshot Tests
// These tests verify consistent rendering across width tiers to catch layout
// drift. Tests cover: narrow (<120), split (120-199), wide (200+).
// ============================================================================

func TestStateRenderingAcrossTiers(t *testing.T) {
	t.Parallel()

	// Width tiers based on layout.TierForWidth thresholds
	tiers := []struct {
		name  string
		width int
	}{
		{"narrow", 60},
		{"split", 140},
		{"wide", 220},
	}

	t.Run("EmptyState", func(t *testing.T) {
		t.Parallel()
		for _, tier := range tiers {
			tier := tier
			t.Run(tier.name, func(t *testing.T) {
				t.Parallel()
				out := EmptyState("No items found", tier.width)

				if out == "" {
					t.Fatalf("EmptyState(%d) should return non-empty string", tier.width)
				}
				if !strings.Contains(out, "No items") {
					t.Errorf("EmptyState(%d) should contain message", tier.width)
				}

				// Verify consistent structure: single-line output (no unexpected breaks)
				lines := strings.Split(out, "\n")
				if len(lines) > 1 {
					t.Errorf("EmptyState(%d) should be single line, got %d lines", tier.width, len(lines))
				}
			})
		}
	})

	t.Run("LoadingState", func(t *testing.T) {
		t.Parallel()
		for _, tier := range tiers {
			tier := tier
			t.Run(tier.name, func(t *testing.T) {
				t.Parallel()
				out := LoadingState("Fetching data", tier.width)

				if out == "" {
					t.Fatalf("LoadingState(%d) should return non-empty string", tier.width)
				}
				if !strings.Contains(out, "Fetching") {
					t.Errorf("LoadingState(%d) should contain message", tier.width)
				}

				lines := strings.Split(out, "\n")
				if len(lines) > 1 {
					t.Errorf("LoadingState(%d) should be single line, got %d lines", tier.width, len(lines))
				}
			})
		}
	})

	t.Run("ErrorState", func(t *testing.T) {
		t.Parallel()
		for _, tier := range tiers {
			tier := tier
			t.Run(tier.name, func(t *testing.T) {
				t.Parallel()
				out := ErrorState("Connection failed", "Press r to retry", tier.width)

				if out == "" {
					t.Fatalf("ErrorState(%d) should return non-empty string", tier.width)
				}
				if !strings.Contains(out, "Connection") {
					t.Errorf("ErrorState(%d) should contain message", tier.width)
				}
				if !strings.Contains(out, "retry") {
					t.Errorf("ErrorState(%d) should contain hint", tier.width)
				}

				// ErrorState with hint should be 2 lines
				lines := strings.Split(out, "\n")
				if len(lines) != 2 {
					t.Errorf("ErrorState(%d) with hint should be 2 lines, got %d", tier.width, len(lines))
				}
			})
		}
	})
}

func TestStateTruncationConsistency(t *testing.T) {
	t.Parallel()

	longMessage := "This is a very long message that should be truncated when the width is too narrow to display it fully"

	t.Run("EmptyState truncates at narrow width", func(t *testing.T) {
		t.Parallel()
		out := EmptyState(longMessage, 30)
		if !strings.Contains(out, "…") {
			t.Error("narrow EmptyState should truncate with ellipsis")
		}
	})

	t.Run("LoadingState truncates at narrow width", func(t *testing.T) {
		t.Parallel()
		out := LoadingState(longMessage, 30)
		if !strings.Contains(out, "…") {
			t.Error("narrow LoadingState should truncate with ellipsis")
		}
	})

	t.Run("ErrorState truncates at narrow width", func(t *testing.T) {
		t.Parallel()
		out := ErrorState(longMessage, "hint", 30)
		if !strings.Contains(out, "…") {
			t.Error("narrow ErrorState should truncate with ellipsis")
		}
	})

	t.Run("wide width preserves full message", func(t *testing.T) {
		t.Parallel()
		out := EmptyState("Short message", 200)
		if strings.Contains(out, "…") {
			t.Error("wide EmptyState should not truncate short message")
		}
	})
}

func TestHelpOverlayAcrossTiers(t *testing.T) {
	t.Parallel()

	sections := []HelpSection{
		{
			Title: "Navigation",
			Hints: []KeyHint{
				{Key: "↑/↓", Desc: "Move up/down"},
				{Key: "j/k", Desc: "Vim navigation"},
			},
		},
		{
			Title: "Actions",
			Hints: []KeyHint{
				{Key: "Enter", Desc: "Select item"},
				{Key: "q", Desc: "Quit"},
			},
		},
	}

	tiers := []struct {
		name     string
		width    int
		maxWidth int
	}{
		{"narrow", 50, 50},
		{"medium", 80, 80},
		{"wide", 0, 120}, // auto-size with cap
	}

	for _, tier := range tiers {
		tier := tier
		t.Run(tier.name, func(t *testing.T) {
			t.Parallel()

			opts := HelpOverlayOptions{
				Title:    "Test Shortcuts",
				Sections: sections,
				Width:    tier.width,
				MaxWidth: tier.maxWidth,
			}
			out := HelpOverlay(opts)

			if out == "" {
				t.Fatalf("HelpOverlay(%s) should return non-empty string", tier.name)
			}

			// Verify structure elements present
			if !strings.Contains(out, "Test Shortcuts") {
				t.Errorf("HelpOverlay(%s) should contain title", tier.name)
			}
			if !strings.Contains(out, "Navigation") {
				t.Errorf("HelpOverlay(%s) should contain section title", tier.name)
			}
			if !strings.Contains(out, "Move up/down") {
				t.Errorf("HelpOverlay(%s) should contain hint description", tier.name)
			}
			if !strings.Contains(out, "Esc") {
				t.Errorf("HelpOverlay(%s) should contain close hint", tier.name)
			}

			// Verify box structure (should have border characters)
			if !strings.Contains(out, "╭") || !strings.Contains(out, "╯") {
				t.Errorf("HelpOverlay(%s) should have rounded border", tier.name)
			}
		})
	}
}

func TestHelpOverlayStructureStability(t *testing.T) {
	t.Parallel()

	sections := []HelpSection{
		{Title: "Navigation", Hints: []KeyHint{
			{Key: "↑ / k", Desc: "Move up"},
			{Key: "↓ / j", Desc: "Move down"},
			{Key: "Tab", Desc: "Cycle panels"},
		}},
		{Title: "Actions", Hints: []KeyHint{
			{Key: "z / Enter", Desc: "Zoom to pane"},
			{Key: "s", Desc: "Send prompt"},
		}},
		{Title: "View Controls", Hints: []KeyHint{
			{Key: "r", Desc: "Refresh data"},
			{Key: "?", Desc: "Toggle help"},
		}},
		{Title: "General", Hints: []KeyHint{
			{Key: "q / Esc", Desc: "Quit dashboard"},
			{Key: "Ctrl+C", Desc: "Force quit"},
		}},
	}
	opts := HelpOverlayOptions{
		Title:    "Dashboard Shortcuts",
		Sections: sections,
	}

	out := HelpOverlay(opts)
	lines := strings.Split(out, "\n")

	// Verify minimum line count (border top + title + empty + sections + footer + border bottom)
	// This catches accidental removal of sections
	if len(lines) < 15 {
		t.Errorf("HelpOverlay should have at least 15 lines, got %d", len(lines))
	}

	// Count section headers present
	sectionCount := 0
	for _, line := range lines {
		for _, section := range sections {
			if strings.Contains(line, section.Title) {
				sectionCount++
				break
			}
		}
	}

	if sectionCount != len(sections) {
		t.Errorf("HelpOverlay should show all %d sections, found %d", len(sections), sectionCount)
	}
}

func TestHelpBarTierAdaptation(t *testing.T) {
	t.Parallel()

	hints := []KeyHint{
		{Key: "↑/↓", Desc: "navigate"},
		{Key: "Enter", Desc: "select"},
		{Key: "Esc", Desc: "back"},
		{Key: "q", Desc: "quit"},
	}

	t.Run("narrow width uses compact style", func(t *testing.T) {
		t.Parallel()
		out := RenderHelpBar(HelpBarOptions{Hints: hints, Width: 60})

		if out == "" {
			t.Fatal("narrow HelpBar should render")
		}
		// Compact style doesn't have background boxes, so no padding chars
		// Just verify it fits and renders
	})

	t.Run("wide width uses full style", func(t *testing.T) {
		t.Parallel()
		out := RenderHelpBar(HelpBarOptions{Hints: hints, Width: 200})

		if out == "" {
			t.Fatal("wide HelpBar should render")
		}
		// Should have more hints visible at wide width
		if !strings.Contains(out, "navigate") || !strings.Contains(out, "quit") {
			t.Error("wide HelpBar should show all hints")
		}
	})

	t.Run("very narrow drops hints progressively", func(t *testing.T) {
		t.Parallel()
		// Very narrow should still render something
		out := RenderHelpBar(HelpBarOptions{Hints: hints, Width: 20})

		// Should have at least one hint or be empty if truly too narrow
		// The key point is it shouldn't panic
		_ = out
	})
}

func TestRenderStateAlignmentModes(t *testing.T) {
	t.Parallel()

	t.Run("left alignment is default", func(t *testing.T) {
		t.Parallel()
		out := RenderState(StateOptions{
			Kind:    StateEmpty,
			Message: "Test",
			Width:   40,
		})
		// Left-aligned should contain the indent (ANSI codes may appear first)
		if !strings.Contains(out, "  ") {
			t.Error("default alignment should have left indent")
		}
	})

	t.Run("center alignment centers content", func(t *testing.T) {
		t.Parallel()
		out := RenderState(StateOptions{
			Kind:    StateEmpty,
			Message: "Test",
			Width:   40,
			Align:   1, // lipgloss.Center
		})
		// Center-aligned rendering should produce non-empty output
		if out == "" {
			t.Error("center-aligned RenderState should render")
		}
	})
}

func TestStateDefaultMessages(t *testing.T) {
	t.Parallel()

	t.Run("empty state has default message", func(t *testing.T) {
		t.Parallel()
		out := RenderState(StateOptions{Kind: StateEmpty, Width: 40})
		if !strings.Contains(out, "Nothing to show") {
			t.Error("empty state should have default message")
		}
	})

	t.Run("loading state has default message", func(t *testing.T) {
		t.Parallel()
		out := RenderState(StateOptions{Kind: StateLoading, Width: 40})
		if !strings.Contains(out, "Loading") {
			t.Error("loading state should have default message")
		}
	})

	t.Run("error state has default message", func(t *testing.T) {
		t.Parallel()
		out := RenderState(StateOptions{Kind: StateError, Width: 40})
		if !strings.Contains(out, "Something went wrong") {
			t.Error("error state should have default message")
		}
	})
}
