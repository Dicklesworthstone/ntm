package dashboard

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/cass"
	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tracker"
	"github.com/Dicklesworthstone/ntm/internal/tui/dashboard/panels"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
)

func newTestModel(width int) Model {
	m := New("test", "")
	m.width = width
	m.height = 30
	m.tier = layout.TierForWidth(width)
	m.panes = []tmux.Pane{
		{
			ID:      "1",
			Index:   1,
			Title:   "codex-long-title-for-wrap-check",
			Type:    tmux.AgentCodex,
			Variant: "VARIANT",
			Command: "run --flag",
		},
	}
	m.cursor = 0
	m.paneStatus[1] = PaneStatus{
		State:          "working",
		ContextPercent: 50,
		ContextLimit:   1000,
	}
	return m
}

func maxRenderedLineWidth(s string) int {
	maxWidth := 0
	for _, line := range strings.Split(s, "\n") {
		width := lipgloss.Width(status.StripANSI(line))
		if width > maxWidth {
			maxWidth = width
		}
	}
	return maxWidth
}

func TestPaneListColumnsByWidthTiers(t *testing.T) {
	t.Parallel()

	// Test that renderPaneList produces output for various widths without panicking.
	// The layout dimensions affect column visibility (ShowContextCol, ShowModelCol, etc.)
	// but we don't strictly verify header content since it depends on theme/style rendering.
	cases := []struct {
		width int
		name  string
	}{
		{width: 80, name: "narrow"},
		{width: 120, name: "tablet-threshold"},
		{width: 160, name: "desktop-threshold"},
		{width: 200, name: "wide"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := newTestModel(tc.width)
			// Use the same width for layout calculations
			list := m.renderPaneList(tc.width)

			// Basic sanity checks
			if list == "" {
				t.Fatalf("width %d: renderPaneList returned empty string", tc.width)
			}

			lines := strings.Split(list, "\n")
			if len(lines) < 2 {
				t.Fatalf("width %d: expected at least 2 lines (header + row), got %d", tc.width, len(lines))
			}

			// Verify CalculateLayout produces expected column visibility flags
			dims := CalculateLayout(tc.width, 1)
			if tc.width >= TabletThreshold && !dims.ShowContextCol {
				t.Errorf("width %d: ShowContextCol should be true for width >= %d", tc.width, TabletThreshold)
			}
			if tc.width >= DesktopThreshold && !dims.ShowModelCol {
				t.Errorf("width %d: ShowModelCol should be true for width >= %d", tc.width, DesktopThreshold)
			}
			if tc.width >= UltraWideThreshold && !dims.ShowCmdCol {
				t.Errorf("width %d: ShowCmdCol should be true for width >= %d", tc.width, UltraWideThreshold)
			}
		})
	}
}

func TestPaneRowSelectionStyling_NoWrapAcrossWidths(t *testing.T) {
	t.Parallel()

	widths := []int{80, 120, 160, 200}
	for _, w := range widths {
		w := w
		t.Run(fmt.Sprintf("width_%d", w), func(t *testing.T) {
			t.Parallel()

			m := newTestModel(w)
			m.cursor = 0 // selected row
			// Use same width for layout calculation
			dims := CalculateLayout(w, 1)
			row := PaneTableRow{
				Index:        m.panes[0].Index,
				Type:         string(m.panes[0].Type),
				Title:        m.panes[0].Title,
				Status:       m.paneStatus[m.panes[0].Index].State,
				IsSelected:   true,
				ContextPct:   m.paneStatus[m.panes[0].Index].ContextPercent,
				ModelVariant: m.panes[0].Variant,
			}
			rendered := RenderPaneRow(row, dims, m.theme)
			clean := status.StripANSI(rendered)

			// Row should be rendered and not empty
			if len(clean) == 0 {
				t.Fatalf("width %d: rendered row is empty", w)
			}

			// Row should not contain unexpected newlines (single line output for basic mode)
			// Note: Wide layouts may include second line for rich content, so only check
			// if layout mode is not wide enough for multi-line output
			if dims.Mode < LayoutWide && strings.Contains(clean, "\n") {
				t.Fatalf("width %d: row contained unexpected newline in non-wide mode", w)
			}
		})
	}
}

func TestSplitViewLayouts_ByWidthTiers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		width        int
		expectList   bool
		expectDetail bool
	}{
		{width: 120, expectList: true, expectDetail: true},
		{width: 160, expectList: true, expectDetail: true},
		{width: 200, expectList: true, expectDetail: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(fmt.Sprintf("width_%d", tc.width), func(t *testing.T) {
			t.Parallel()

			m := newTestModel(tc.width)
			m.height = 30
			if m.tier < layout.TierSplit {
				t.Skip("split view not used below split threshold")
			}
			out := m.renderSplitView()
			plain := status.StripANSI(out)

			// Ensure we always render the list panel
			if !strings.Contains(plain, "TITLE") {
				t.Fatalf("width %d: expected list header 'TITLE' in split view", tc.width)
			}

			if tc.expectDetail {
				if !strings.Contains(plain, "Context Usage") && m.tier >= layout.TierWide {
					t.Fatalf("width %d: expected detail pane content (Context Usage) at wide tier", tc.width)
				}
			} else {
				// For narrow widths we shouldn't render split view; ensure single-panel fallback
				if strings.Contains(plain, "Context Usage") && tc.width < layout.SplitViewThreshold {
					t.Fatalf("width %d: unexpected detail content for narrow layout", tc.width)
				}
			}
		})
	}
}

func TestUltraLayout_DoesNotOverflowWidth(t *testing.T) {
	t.Parallel()

	m := newTestModel(layout.UltraWideViewThreshold)
	m.height = 30

	out := m.renderUltraLayout()
	if got := maxRenderedLineWidth(out); got > m.width {
		t.Fatalf("renderUltraLayout max line width = %d, want <= %d", got, m.width)
	}
}

func TestMegaLayout_DoesNotOverflowWidth(t *testing.T) {
	t.Parallel()

	m := newTestModel(layout.MegaWideViewThreshold)
	m.height = 30

	out := m.renderMegaLayout()
	if got := maxRenderedLineWidth(out); got > m.width {
		t.Fatalf("renderMegaLayout max line width = %d, want <= %d", got, m.width)
	}
}

func TestSplitProportionsAcrossThresholds(t *testing.T) {
	t.Parallel()

	cases := []struct {
		total         int
		expectSplit   bool
		expectNonZero bool
		name          string
	}{
		{total: 80, expectSplit: false, expectNonZero: false, name: "narrow"},
		{total: 120, expectSplit: true, expectNonZero: true, name: "split-threshold"},
		{total: 160, expectSplit: true, expectNonZero: true, name: "mid-split"},
		{total: 200, expectSplit: true, expectNonZero: true, name: "wide"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			left, right := layout.SplitProportions(tc.total)

			if left+right > tc.total {
				t.Fatalf("total %d: left+right=%d exceeds total width", tc.total, left+right)
			}

			if tc.expectSplit {
				if right == 0 {
					t.Fatalf("total %d: expected split view to allocate right panel", tc.total)
				}
			} else if right != 0 {
				t.Fatalf("total %d: expected single column layout, got right=%d", tc.total, right)
			}

			if tc.expectNonZero && (left == 0 || right == 0) {
				t.Fatalf("total %d: both panels should be non-zero (left=%d right=%d)", tc.total, left, right)
			}
		})
	}
}

func TestSidebarRendersCASSContext(t *testing.T) {
	t.Parallel()

	m := newTestModel(layout.UltraWideViewThreshold)
	now := time.Now().Add(-2 * time.Hour).Unix()
	hits := []cass.SearchHit{
		{
			Title:     "Session: auth refactor",
			Score:     0.90,
			CreatedAt: &now,
		},
	}

	updated, _ := m.Update(CASSContextMsg{Hits: hits})
	m = updated.(Model)

	out := status.StripANSI(m.renderSidebar(60, 25))
	if !strings.Contains(out, "auth refactor") {
		t.Fatalf("expected sidebar to include CASS hit title; got:\n%s", out)
	}
}

func TestSidebarRendersFileChanges(t *testing.T) {
	t.Parallel()

	m := newTestModel(layout.UltraWideViewThreshold)
	now := time.Now().Add(-2 * time.Minute)

	changes := []tracker.RecordedFileChange{
		{
			Timestamp: now,
			Session:   "test",
			Agents:    []string{"BluePond"},
			Change: tracker.FileChange{
				Path: "/src/main.go",
				Type: tracker.FileModified,
			},
		},
	}

	updated, _ := m.Update(FileChangeMsg{Changes: changes})
	m = updated.(Model)

	out := status.StripANSI(m.renderSidebar(60, 25))
	if !strings.Contains(out, "main.go") {
		t.Fatalf("expected sidebar to include file change; got:\n%s", out)
	}
}

func TestRenderSidebar_FillsExactHeight(t *testing.T) {
	t.Parallel()

	m := newTestModel(layout.UltraWideViewThreshold)

	out := m.renderSidebar(60, 25)
	if got := lipgloss.Height(out); got != 25 {
		t.Fatalf("renderSidebar height = %d, want %d", got, 25)
	}
}

func TestSidebarRendersMetricsAndHistoryPanelsWhenSpaceAllows(t *testing.T) {
	t.Parallel()

	m := newTestModel(layout.UltraWideViewThreshold)

	updated, _ := m.Update(MetricsUpdateMsg{
		Data: panels.MetricsData{
			TotalTokens: 1234,
			TotalCost:   0.42,
			Agents: []panels.AgentMetric{
				{
					Name:       "cc_1",
					Type:       "cc",
					Tokens:     1000,
					Cost:       0.21,
					ContextPct: 0.42,
				},
			},
		},
	})
	m = updated.(Model)

	updated, _ = m.Update(HistoryUpdateMsg{
		Entries: []history.HistoryEntry{
			{
				ID:        "1",
				Timestamp: time.Now().UTC(),
				Session:   "test",
				Targets:   []string{"1"},
				Prompt:    "Hello from test",
				Source:    history.SourceCLI,
				Success:   true,
			},
		},
	})
	m = updated.(Model)

	out := status.StripANSI(m.renderSidebar(60, 30))
	if !strings.Contains(out, "Metrics & Usage") {
		t.Fatalf("expected sidebar to include metrics panel title; got:\n%s", out)
	}
	if !strings.Contains(out, "Command History") {
		t.Fatalf("expected sidebar to include history panel title; got:\n%s", out)
	}
}

func TestPaneGridRendersEnhancedBadges(t *testing.T) {
	t.Parallel()

	m := newTestModel(110) // below split threshold, uses grid view
	m.animTick = 1

	// Configure pane to look like a Claude agent with a model alias.
	m.panes[0].Type = tmux.AgentClaude
	m.panes[0].Variant = "opus"
	m.panes[0].Title = "test__cc_1_opus"

	// Beads + file changes are best-effort enrichments: wire minimal data to show badges.
	m.beadsSummary = bv.BeadsSummary{
		Available: true,
		InProgressList: []bv.BeadInProgress{
			{ID: "ntm-123", Title: "Do thing", Assignee: m.panes[0].Title},
		},
	}

	m.fileChanges = []tracker.RecordedFileChange{
		{
			Timestamp: time.Now(),
			Session:   "test",
			Agents:    []string{m.panes[0].Title},
			Change: tracker.FileChange{
				Path: "/src/main.go",
				Type: tracker.FileModified,
			},
		},
	}

	m.agentStatuses[m.panes[0].ID] = status.AgentStatus{
		PaneID:     m.panes[0].ID,
		PaneName:   m.panes[0].Title,
		AgentType:  "cc",
		State:      status.StateWorking,
		LastActive: time.Now().Add(-1 * time.Minute),
		LastOutput: "hello world",
		UpdatedAt:  time.Now(),
	}

	out := status.StripANSI(m.renderPaneGrid())

	// Model badge
	if !strings.Contains(out, "opus") {
		t.Fatalf("expected grid to include model badge; got:\n%s", out)
	}
	// Bead badge
	if !strings.Contains(out, "ntm-123") {
		t.Fatalf("expected grid to include bead badge; got:\n%s", out)
	}
	// File change badge
	if !strings.Contains(out, "Δ1") {
		t.Fatalf("expected grid to include file change badge; got:\n%s", out)
	}
	// Token velocity badge
	if !strings.Contains(out, "tpm") {
		t.Fatalf("expected grid to include token velocity badge; got:\n%s", out)
	}
	// Context usage (full bar includes percent)
	if !strings.Contains(out, "50%") {
		t.Fatalf("expected grid to include context percent; got:\n%s", out)
	}
	// Working spinner frame for animTick=1
	if !strings.Contains(out, "◓") {
		t.Fatalf("expected grid to include working spinner; got:\n%s", out)
	}
}

func TestHelpOverlayToggle(t *testing.T) {
	t.Parallel()

	t.Run("pressing_?_opens_help", func(t *testing.T) {
		t.Parallel()

		m := newTestModel(120)
		if m.showHelp {
			t.Fatal("showHelp should be false initially")
		}

		// Press '?' to open help
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}}
		updated, _ := m.Update(msg)
		m = updated.(Model)

		if !m.showHelp {
			t.Error("showHelp should be true after pressing '?'")
		}
	})

	t.Run("pressing_?_again_closes_help", func(t *testing.T) {
		t.Parallel()

		m := newTestModel(120)
		m.showHelp = true

		// Press '?' to close help
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}}
		updated, _ := m.Update(msg)
		m = updated.(Model)

		if m.showHelp {
			t.Error("showHelp should be false after pressing '?' while open")
		}
	})

	t.Run("pressing_esc_closes_help", func(t *testing.T) {
		t.Parallel()

		m := newTestModel(120)
		m.showHelp = true

		// Press Esc to close help
		msg := tea.KeyMsg{Type: tea.KeyEsc}
		updated, _ := m.Update(msg)
		m = updated.(Model)

		if m.showHelp {
			t.Error("showHelp should be false after pressing Esc while open")
		}
	})

	t.Run("help_overlay_blocks_other_keys", func(t *testing.T) {
		t.Parallel()

		m := newTestModel(120)
		m.showHelp = true
		initialCursor := m.cursor

		// Try to move cursor down while help is open
		msg := tea.KeyMsg{Type: tea.KeyDown}
		updated, _ := m.Update(msg)
		m = updated.(Model)

		if m.cursor != initialCursor {
			t.Error("cursor should not change when help overlay is open")
		}
		if !m.showHelp {
			t.Error("help should still be open after pressing unrelated key")
		}
	})
}

func TestHelpBarIncludesHelpHint(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)
	helpBar := m.renderHelpBar()

	if !strings.Contains(helpBar, "?") {
		t.Error("help bar should include '?' hint")
	}
	if !strings.Contains(helpBar, "help") {
		t.Error("help bar should include 'help' description")
	}
}

func TestViewRendersHelpOverlayWhenOpen(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)
	m.showHelp = true

	view := m.View()

	if !strings.Contains(view, "Shortcuts") || !strings.Contains(view, "Navigation") {
		t.Error("view should render help overlay content when showHelp is true")
	}
}

func TestQuickActionsBarWidthGated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		width       int
		shouldShow  bool
		description string
	}{
		{width: 80, shouldShow: false, description: "narrow"},
		{width: 120, shouldShow: false, description: "split"},
		{width: 180, shouldShow: false, description: "below wide"},
		{width: 200, shouldShow: true, description: "wide threshold"},
		{width: 240, shouldShow: true, description: "ultra"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.description, func(t *testing.T) {
			t.Parallel()

			m := newTestModel(tc.width)
			quickActions := m.renderQuickActions()
			plain := status.StripANSI(quickActions)

			hasContent := len(plain) > 0

			if tc.shouldShow && !hasContent {
				t.Errorf("width %d: expected quick actions to be visible at wide tier", tc.width)
			}
			if !tc.shouldShow && hasContent {
				t.Errorf("width %d: expected quick actions to be hidden in narrow mode", tc.width)
			}
		})
	}
}

func TestQuickActionsBarContainsExpectedActions(t *testing.T) {
	t.Parallel()

	m := newTestModel(200) // Wide enough to show quick actions
	quickActions := m.renderQuickActions()
	plain := status.StripANSI(quickActions)

	expectedItems := []string{"Palette", "Send", "Copy", "Zoom"}
	for _, item := range expectedItems {
		if !strings.Contains(plain, item) {
			t.Errorf("quick actions bar should contain '%s', got: %s", item, plain)
		}
	}

	// Verify the "Actions" label is present
	if !strings.Contains(plain, "Actions") {
		t.Error("quick actions bar should contain 'Actions' label")
	}
}

func TestLayoutModeString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mode LayoutMode
		want string
	}{
		{LayoutMobile, "mobile"},
		{LayoutCompact, "compact"},
		{LayoutSplit, "split"},
		{LayoutWide, "wide"},
		{LayoutUltraWide, "ultrawide"},
		{LayoutMode(99), "unknown"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			if got := tc.mode.String(); got != tc.want {
				t.Errorf("LayoutMode(%d).String() = %q, want %q", tc.mode, got, tc.want)
			}
		})
	}
}

func TestRenderSparkline(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value float64
		width int
		name  string
	}{
		{0.0, 10, "zero"},
		{0.5, 10, "half"},
		{1.0, 10, "full"},
		{-0.5, 10, "negative_clamped"},
		{1.5, 10, "over_one_clamped"},
		{0.33, 5, "partial"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := RenderSparkline(tc.value, tc.width)
			// Basic check: result should not be empty and roughly match width
			if result == "" {
				t.Error("RenderSparkline should not return empty string")
			}
			// Length should be close to width (Unicode characters may vary)
			if len([]rune(result)) > tc.width+1 {
				t.Errorf("RenderSparkline result length %d exceeds expected width %d", len([]rune(result)), tc.width)
			}
		})
	}
}

func TestRenderMiniBar(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)
	tests := []struct {
		value float64
		width int
		name  string
	}{
		{0.0, 10, "zero"},
		{0.5, 10, "half"},
		{1.0, 10, "full"},
		{0.25, 5, "quarter"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := RenderMiniBar(tc.value, tc.width, m.theme)
			// Should render something
			if result == "" {
				t.Error("RenderMiniBar should not return empty string")
			}
		})
	}
}

func TestRenderLayoutIndicator(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)
	mode := LayoutForWidth(m.width)
	indicator := RenderLayoutIndicator(mode, m.theme)

	// Should produce some output
	if indicator == "" {
		t.Error("RenderLayoutIndicator should return non-empty string")
	}
}

func TestScrollIndicator(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)
	tests := []struct {
		offset   int
		total    int
		visible  int
		selected int
		name     string
	}{
		{0, 10, 5, 0, "at_top"},
		{5, 10, 5, 5, "at_bottom"},
		{2, 10, 5, 3, "middle"},
		{0, 3, 5, 0, "all_visible"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			vp := &ViewportPosition{
				Offset:   tc.offset,
				Total:    tc.total,
				Visible:  tc.visible,
				Selected: tc.selected,
			}
			// Just verify it doesn't panic
			result := vp.ScrollIndicator(m.theme)
			_ = result // Result varies based on position
		})
	}
}

func TestEnsureVisible(t *testing.T) {
	t.Parallel()

	tests := []struct {
		selected int
		offset   int
		visible  int
		total    int
		wantOff  int
		name     string
	}{
		{0, 0, 10, 20, 0, "at_top"},
		{5, 0, 10, 20, 0, "within_visible"},
		{15, 0, 10, 20, 6, "below_visible"},
		{3, 10, 10, 20, 3, "above_visible"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			vp := &ViewportPosition{
				Offset:   tc.offset,
				Visible:  tc.visible,
				Total:    tc.total,
				Selected: tc.selected,
			}
			vp.EnsureVisible()
			if vp.Offset != tc.wantOff {
				t.Errorf("EnsureVisible() offset = %d, want %d", vp.Offset, tc.wantOff)
			}
		})
	}
}

func TestMinFunc(t *testing.T) {
	t.Parallel()

	tests := []struct {
		a, b, want int
	}{
		{1, 2, 1},
		{2, 1, 1},
		{5, 5, 5},
		{-1, 1, -1},
		{0, 0, 0},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(fmt.Sprintf("%d_%d", tc.a, tc.b), func(t *testing.T) {
			t.Parallel()
			if got := min(tc.a, tc.b); got != tc.want {
				t.Errorf("min(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hell…"}, // Single-char ellipsis (U+2026) saves 2 chars
		{"hi", 10, "hi"},
		{"", 5, ""},
		{"abcdef", 3, "ab…"}, // Single-char ellipsis: 2 chars + "…"
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got := truncate(tc.input, tc.maxLen)
			if got != tc.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tc.input, tc.maxLen, got, tc.want)
			}
		})
	}
}

func TestGetStatusIconAndColor(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)

	tests := []struct {
		state string
		tick  int
		name  string
	}{
		{"working", 0, "working_tick0"},
		{"working", 5, "working_tick5"},
		{"idle", 0, "idle"},
		{"error", 0, "error"},
		{"compacted", 0, "compacted"},
		{"unknown", 0, "unknown"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			icon, color := getStatusIconAndColor(tc.state, m.theme, tc.tick)
			if icon == "" {
				t.Errorf("getStatusIconAndColor(%q, tick=%d) returned empty icon", tc.state, tc.tick)
			}
			if color == "" {
				t.Errorf("getStatusIconAndColor(%q, tick=%d) returned empty color", tc.state, tc.tick)
			}
		})
	}
}

func TestFormatRelativeTime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		duration time.Duration
		contains string
		name     string
	}{
		{30 * time.Second, "s", "seconds"},
		{5 * time.Minute, "m", "minutes"},
		{2 * time.Hour, "h", "hours"},
		{48 * time.Hour, "d", "days"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := formatRelativeTime(tc.duration)
			if !strings.Contains(result, tc.contains) {
				t.Errorf("formatRelativeTime(%v) = %q, expected to contain %q", tc.duration, result, tc.contains)
			}
		})
	}
}

func TestSpinnerDot(t *testing.T) {
	t.Parallel()

	// Test multiple animation ticks
	for i := 0; i < 10; i++ {
		result := spinnerDot(i)
		if result == "" {
			t.Errorf("spinnerDot(%d) returned empty string", i)
		}
	}
}

func TestComputeContextRanks(t *testing.T) {
	t.Parallel()

	m := newTestModel(200)
	// Populate panes matching the status map
	m.panes = []tmux.Pane{
		{Index: 1, ID: "1"},
		{Index: 2, ID: "2"},
		{Index: 3, ID: "3"},
	}
	m.paneStatus = map[int]PaneStatus{
		1: {ContextPercent: 80},
		2: {ContextPercent: 50},
		3: {ContextPercent: 90},
	}

	ranks := m.computeContextRanks()

	if len(ranks) != 3 {
		t.Fatalf("computeContextRanks returned %d entries, want 3", len(ranks))
	}

	// Pane 3 should have rank 1 (highest context)
	if ranks[3] != 1 {
		t.Errorf("pane 3 rank = %d, want 1 (highest context)", ranks[3])
	}
	// Pane 1 should have rank 2
	if ranks[1] != 2 {
		t.Errorf("pane 1 rank = %d, want 2", ranks[1])
	}
	// Pane 2 should have rank 3
	if ranks[2] != 3 {
		t.Errorf("pane 2 rank = %d, want 3 (lowest context)", ranks[2])
	}
}

func TestRenderDiagnosticsBar(t *testing.T) {
	t.Parallel()

	m := newTestModel(200)
	m.showDiagnostics = true
	m.err = fmt.Errorf("test error")

	bar := m.renderDiagnosticsBar(100)
	plain := status.StripANSI(bar)

	if bar == "" {
		t.Error("renderDiagnosticsBar should not return empty string with error")
	}

	// Should contain some indication of diagnostics
	_ = plain // Content varies based on error state
}

func TestRenderMetricsPanel(t *testing.T) {
	t.Parallel()

	m := newTestModel(200)
	m.metricsPanel.SetData(panels.MetricsData{
		TotalTokens: 1000,
		TotalCost:   0.50,
	}, nil)

	result := m.renderMetricsPanel(50, 10)
	if result == "" {
		t.Error("renderMetricsPanel should not return empty string")
	}
}

func TestRenderHistoryPanel(t *testing.T) {
	t.Parallel()

	m := newTestModel(200)
	m.historyPanel.SetEntries([]history.HistoryEntry{
		{
			ID:        "1",
			Timestamp: time.Now().UTC(),
			Session:   "test",
			Prompt:    "Hello",
			Source:    history.SourceCLI,
			Success:   true,
		},
	}, nil)

	result := m.renderHistoryPanel(50, 10)
	if result == "" {
		t.Error("renderHistoryPanel should not return empty string")
	}
}

func TestAgentBorderColor(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)

	types := []string{
		string(tmux.AgentClaude),
		string(tmux.AgentCodex),
		string(tmux.AgentGemini),
		string(tmux.AgentUser),
		"unknown",
	}

	for _, agentType := range types {
		result := AgentBorderColor(agentType, m.theme)
		if result == "" {
			t.Errorf("AgentBorderColor(%s) returned empty string", agentType)
		}
	}
}

func TestPanelStyles(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)
	// Test with FocusList
	listStyle, detailStyle := PanelStyles(FocusList, m.theme)

	// Both should be valid styles (not zero values)
	testText := "test"
	if listStyle.Render(testText) == "" {
		t.Error("list panel style should render")
	}
	if detailStyle.Render(testText) == "" {
		t.Error("detail panel style should render")
	}

	// Test with FocusDetail
	listStyle2, detailStyle2 := PanelStyles(FocusDetail, m.theme)
	if listStyle2.Render(testText) == "" {
		t.Error("list panel style (detail focus) should render")
	}
	if detailStyle2.Render(testText) == "" {
		t.Error("detail panel style (detail focus) should render")
	}
}

func TestAgentBorderStyle(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)

	types := []string{
		string(tmux.AgentClaude),
		string(tmux.AgentCodex),
		string(tmux.AgentGemini),
		string(tmux.AgentUser),
	}

	for _, agentType := range types {
		// Test inactive
		style := AgentBorderStyle(agentType, false, 0, m.theme)
		result := style.Render("test")
		if result == "" {
			t.Errorf("AgentBorderStyle(%s, inactive) returned style that renders empty", agentType)
		}

		// Test active with tick
		styleActive := AgentBorderStyle(agentType, true, 5, m.theme)
		resultActive := styleActive.Render("test")
		if resultActive == "" {
			t.Errorf("AgentBorderStyle(%s, active) returned style that renders empty", agentType)
		}
	}
}

func TestAgentPanelStyles(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)

	types := []string{
		string(tmux.AgentClaude),
		string(tmux.AgentCodex),
		string(tmux.AgentGemini),
		string(tmux.AgentUser),
	}

	for _, agentType := range types {
		// Test with FocusList, inactive
		listStyle, detailStyle := AgentPanelStyles(agentType, FocusList, false, 0, m.theme)
		if listStyle.Render("test") == "" {
			t.Errorf("AgentPanelStyles(%s) list style renders empty", agentType)
		}
		if detailStyle.Render("test") == "" {
			t.Errorf("AgentPanelStyles(%s) detail style renders empty", agentType)
		}

		// Test with FocusDetail, active with tick
		listStyle2, detailStyle2 := AgentPanelStyles(agentType, FocusDetail, true, 5, m.theme)
		if listStyle2.Render("test") == "" {
			t.Errorf("AgentPanelStyles(%s, active) list style renders empty", agentType)
		}
		if detailStyle2.Render("test") == "" {
			t.Errorf("AgentPanelStyles(%s, active) detail style renders empty", agentType)
		}
	}
}

func TestMaxInt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		a, b, want int
	}{
		{1, 2, 2},
		{2, 1, 2},
		{5, 5, 5},
		{-1, 1, 1},
		{0, 0, 0},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(fmt.Sprintf("%d_%d", tc.a, tc.b), func(t *testing.T) {
			t.Parallel()
			if got := maxInt(tc.a, tc.b); got != tc.want {
				t.Errorf("maxInt(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestTruncateRunes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hell…"}, // Uses Unicode ellipsis, keeps maxLen-1 chars
		{"hi", 10, "hi"},
		{"", 5, ""},
		{"日本語テスト", 4, "日本語…"}, // Keeps 3 runes + ellipsis
		{"ab", 1, "…"},        // maxLen==1 and string is longer returns just ellipsis
		{"a", 1, "a"},         // string fits, returns unchanged
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got := truncateRunes(tc.input, tc.maxLen)
			if got != tc.want {
				t.Errorf("truncateRunes(%q, %d) = %q, want %q", tc.input, tc.maxLen, got, tc.want)
			}
		})
	}
}

// TestHiddenColCountCalculation verifies that HiddenColCount is calculated correctly
// based on terminal width and column visibility thresholds.
func TestHiddenColCountCalculation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		width          int
		wantHiddenCols int
		wantContext    bool
		wantModel      bool
		wantCmd        bool
	}{
		{
			name:           "narrow_hides_all",
			width:          80, // Below TabletThreshold (100)
			wantHiddenCols: 3,  // Context, Model, Cmd all hidden
			wantContext:    false,
			wantModel:      false,
			wantCmd:        false,
		},
		{
			name:           "tablet_shows_context",
			width:          TabletThreshold, // 100
			wantHiddenCols: 2,               // Model and Cmd hidden
			wantContext:    true,
			wantModel:      false,
			wantCmd:        false,
		},
		{
			name:           "desktop_shows_model",
			width:          DesktopThreshold, // 140
			wantHiddenCols: 1,                // Only Cmd hidden
			wantContext:    true,
			wantModel:      true,
			wantCmd:        false,
		},
		{
			name:           "ultrawide_shows_all",
			width:          UltraWideThreshold, // 180
			wantHiddenCols: 0,                  // Nothing hidden
			wantContext:    true,
			wantModel:      true,
			wantCmd:        true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dims := CalculateLayout(tc.width, 30)

			if dims.HiddenColCount != tc.wantHiddenCols {
				t.Errorf("width %d: HiddenColCount = %d, want %d",
					tc.width, dims.HiddenColCount, tc.wantHiddenCols)
			}
			if dims.ShowContextCol != tc.wantContext {
				t.Errorf("width %d: ShowContextCol = %v, want %v",
					tc.width, dims.ShowContextCol, tc.wantContext)
			}
			if dims.ShowModelCol != tc.wantModel {
				t.Errorf("width %d: ShowModelCol = %v, want %v",
					tc.width, dims.ShowModelCol, tc.wantModel)
			}
			if dims.ShowCmdCol != tc.wantCmd {
				t.Errorf("width %d: ShowCmdCol = %v, want %v",
					tc.width, dims.ShowCmdCol, tc.wantCmd)
			}
		})
	}
}

// TestRenderTableHeaderHiddenIndicator verifies that the header shows "+N hidden"
// when columns are hidden due to narrow width.
func TestRenderTableHeaderHiddenIndicator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		width         int
		expectHidden  bool
		expectedCount int
	}{
		{
			name:          "narrow_shows_hidden_indicator",
			width:         80,
			expectHidden:  true,
			expectedCount: 3,
		},
		{
			name:          "tablet_shows_hidden_indicator",
			width:         TabletThreshold,
			expectHidden:  true,
			expectedCount: 2,
		},
		{
			name:          "desktop_shows_hidden_indicator",
			width:         DesktopThreshold,
			expectHidden:  true,
			expectedCount: 1,
		},
		{
			name:          "ultrawide_no_hidden_indicator",
			width:         UltraWideThreshold,
			expectHidden:  false,
			expectedCount: 0,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := newTestModel(tc.width)
			dims := CalculateLayout(tc.width, 30)
			header := RenderTableHeader(dims, m.theme)
			plain := status.StripANSI(header)

			expectedIndicator := fmt.Sprintf("+%d hidden", tc.expectedCount)
			hasIndicator := strings.Contains(plain, expectedIndicator)

			if tc.expectHidden && !hasIndicator {
				t.Errorf("width %d: expected header to contain %q, got %q",
					tc.width, expectedIndicator, plain)
			}
			if !tc.expectHidden && strings.Contains(plain, "hidden") {
				t.Errorf("width %d: expected no hidden indicator, but found one in %q",
					tc.width, plain)
			}
		})
	}
}
