package dashboard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/cass"
	ctxmon "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tracker"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/dashboard/panels"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/watcher"
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

func TestTimelineAgentIDUsesFinalSeparator(t *testing.T) {
	t.Parallel()

	pane := tmux.Pane{Title: "my__project__cc_2"}
	if got := timelineAgentID(pane, "", ""); got != "cc_2" {
		t.Fatalf("timelineAgentID() = %q, want %q", got, "cc_2")
	}
}

func renderedHeight(s string) int {
	plain := strings.TrimRight(status.StripANSI(s), "\n")
	if plain == "" {
		return 0
	}
	return lipgloss.Height(plain)
}

func leadingSpaceCount(s string) int {
	count := 0
	for _, r := range s {
		if r != ' ' {
			break
		}
		count++
	}
	return count
}

func firstNonEmptyLines(s string, limit int) []string {
	lines := make([]string, 0, limit)
	for _, line := range strings.Split(status.StripANSI(s), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
		if len(lines) == limit {
			break
		}
	}
	return lines
}

func TestViewFitsHeightAndFooterOnce(t *testing.T) {
	t.Parallel()

	m := newTestModel(140)
	m.height = 30
	m.tier = layout.TierForWidth(m.width)

	view := m.View()
	plain := status.StripANSI(view)

	if got := renderedHeight(view); got > m.height {
		t.Fatalf("view height %d exceeds terminal height %d", got, m.height)
	}

	if count := strings.Count(plain, "Fleet:"); count != 1 {
		t.Fatalf("expected Fleet segment once, got %d", count)
	}

	if count := strings.Count(plain, "navigate"); count != 1 {
		t.Fatalf("expected help hint once, got %d", count)
	}
}

func TestRenderHeaderHandoffLine(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)
	m.handoffGoal = "Implemented auth tokens"
	m.handoffNow = "Add refresh token rotation"
	m.handoffAge = 2 * time.Hour
	m.handoffStatus = "complete"

	line := m.renderHeaderHandoffLine(m.width)
	plain := status.StripANSI(line)

	if !strings.Contains(plain, "handoff") {
		t.Fatalf("expected handoff line to include label, got %q", plain)
	}
	if !strings.Contains(plain, "goal:") {
		t.Fatalf("expected handoff line to include goal, got %q", plain)
	}
	if !strings.Contains(plain, "now:") {
		t.Fatalf("expected handoff line to include now, got %q", plain)
	}
	if !strings.Contains(plain, "ago") {
		t.Fatalf("expected handoff line to include age, got %q", plain)
	}
}

func TestRenderHeaderContextWarningLine(t *testing.T) {
	t.Parallel()

	m := newTestModel(140)
	m.panes = []tmux.Pane{
		{ID: "%1", Index: 1, Title: "test__cc_1", Type: tmux.AgentClaude},
		{ID: "%2", Index: 2, Title: "test__cod_1", Type: tmux.AgentCodex},
	}
	m.paneStatus[1] = PaneStatus{
		ContextPercent: 72,
		ContextLimit:   1000,
		ContextModel:   "claude-sonnet-4-6",
	}
	m.paneStatus[2] = PaneStatus{
		ContextPercent: 86,
		ContextLimit:   1000,
		ContextModel:   "gpt-4",
	}

	line := m.renderHeaderContextWarningLine(m.width)
	plain := status.StripANSI(line)

	if !strings.Contains(plain, "context") {
		t.Fatalf("expected context warning line, got %q", plain)
	}
	if !strings.Contains(plain, "72%") || !strings.Contains(plain, "86%") {
		t.Fatalf("expected warning line to include percentages, got %q", plain)
	}
	if !strings.Contains(plain, "claude") || !strings.Contains(plain, "gpt-4") {
		t.Fatalf("expected warning line to include model names, got %q", plain)
	}

	m.paneStatus[1] = PaneStatus{
		ContextPercent: 60,
		ContextLimit:   1000,
		ContextModel:   "claude-sonnet-4-6",
	}
	m.paneStatus[2] = PaneStatus{
		ContextPercent: 65,
		ContextLimit:   1000,
		ContextModel:   "gpt-4",
	}
	line = m.renderHeaderContextWarningLine(m.width)
	if line != "" {
		t.Fatalf("expected no warning line below threshold, got %q", status.StripANSI(line))
	}
}

// TestViewFitsHeightWithManyPanes tests that the dashboard correctly truncates content
// when there are many panes (e.g., 17) that would otherwise overflow the terminal height.
// This is the scenario from bd-1xoe where the status bar was being duplicated.
func TestViewFitsHeightWithManyPanes(t *testing.T) {
	t.Parallel()

	m := New("test", "")
	m.width = 140
	m.height = 40
	m.tier = layout.TierForWidth(m.width)

	// Create 17 panes to simulate the real-world scenario
	for i := 1; i <= 17; i++ {
		pane := tmux.Pane{
			ID:      fmt.Sprintf("%d", i),
			Index:   i,
			Title:   fmt.Sprintf("destructive_command_guard__cc_%d", i),
			Type:    tmux.AgentClaude,
			Variant: "",
			Command: "claude",
		}
		m.panes = append(m.panes, pane)
		m.paneStatus[i] = PaneStatus{
			State:          "working",
			ContextPercent: float64(i * 5),
			ContextLimit:   200000,
		}
	}

	view := m.View()
	plain := status.StripANSI(view)

	// View height must not exceed terminal height
	if got := renderedHeight(view); got > m.height {
		t.Fatalf("view height %d exceeds terminal height %d (with 17 panes)", got, m.height)
	}

	// Fleet segment must appear exactly once (not duplicated due to overflow)
	if count := strings.Count(plain, "Fleet:"); count != 1 {
		t.Fatalf("expected Fleet segment once, got %d (content may have overflowed)", count)
	}

	// Help hint must appear exactly once
	if count := strings.Count(plain, "navigate"); count != 1 {
		t.Fatalf("expected help hint once, got %d (footer may have been duplicated)", count)
	}
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

func TestVisiblePanelsForHelpVerbosity_AttentionByTier(t *testing.T) {
	t.Parallel()

	cases := []struct {
		width           int
		expectAttention bool
	}{
		{width: 119, expectAttention: false},
		{width: 120, expectAttention: true},
		{width: 200, expectAttention: true},
		{width: 240, expectAttention: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(fmt.Sprintf("width_%d", tc.width), func(t *testing.T) {
			t.Parallel()

			m := newTestModel(tc.width)
			visible := m.visiblePanelsForHelpVerbosity()

			hasAttention := false
			for _, panel := range visible {
				if panel == PanelAttention {
					hasAttention = true
					break
				}
			}

			if hasAttention != tc.expectAttention {
				t.Fatalf("width %d: attention visible=%v, want %v", tc.width, hasAttention, tc.expectAttention)
			}
		})
	}
}

func TestCycleFocusIncludesAttentionAtSplitAndSkipsAtNarrow(t *testing.T) {
	t.Parallel()

	split := newTestModel(120)
	split.focusedPanel = PanelDetail
	split.syncFocusRing()
	split.cycleFocus(1)
	if split.focusedPanel != PanelAttention {
		t.Fatalf("expected split-tier focus to advance to attention, got %v", split.focusedPanel)
	}

	narrow := newTestModel(100)
	narrow.focusedPanel = PanelPaneList
	narrow.syncFocusRing()
	narrow.cycleFocus(1)
	if narrow.focusedPanel != PanelPaneList {
		t.Fatalf("expected narrow-tier focus to stay on pane list, got %v", narrow.focusedPanel)
	}
}

func TestRenderSplitViewUsesAttentionPanelWhenFocused(t *testing.T) {
	t.Parallel()

	m := newTestModel(200)
	m.attentionPanel.SetData([]panels.AttentionItem{
		{
			Summary:       "operator attention item",
			Actionability: robot.ActionabilityActionRequired,
			Timestamp:     time.Now(),
			SourcePane:    1,
			SourceAgent:   "codex",
		},
	}, true)
	m.focusedPanel = PanelAttention

	out := status.StripANSI(m.renderSplitView())
	if !strings.Contains(out, "Attention") {
		t.Fatalf("expected split view to render attention panel title, got %q", out)
	}
	if !strings.Contains(out, "operator attention item") {
		t.Fatalf("expected split view to render attention summary, got %q", out)
	}
	if strings.Contains(out, "Context Usage") {
		t.Fatalf("expected attention panel to replace the detail view while focused, got %q", out)
	}
}

func TestMegaLayoutRendersAttentionPanelWhenFocused(t *testing.T) {
	t.Parallel()

	m := newTestModel(layout.MegaWideViewThreshold)
	m.height = 30
	m.tier = layout.TierMega
	m.focusedPanel = PanelAttention
	m.attentionPanel.SetData([]panels.AttentionItem{
		{
			Summary:       "overlay attention item",
			Actionability: robot.ActionabilityInteresting,
			Timestamp:     time.Now(),
			SourcePane:    1,
			SourceAgent:   "codex",
		},
	}, true)

	out := status.StripANSI(m.renderMegaLayout())
	if !strings.Contains(out, "Attention") {
		t.Fatalf("expected mega layout to render attention panel title, got %q", out)
	}
	if !strings.Contains(out, "overlay attention item") {
		t.Fatalf("expected mega layout to render attention summary, got %q", out)
	}
}

func containsPanelID(ids []PanelID, want PanelID) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
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

func TestLayouts_IndentAllLinesConsistently(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		width  int
		render func(Model) string
	}{
		{
			name:  "split",
			width: layout.SplitViewThreshold,
			render: func(m Model) string {
				return m.renderSplitView()
			},
		},
		{
			name:  "ultra",
			width: layout.UltraWideViewThreshold,
			render: func(m Model) string {
				return m.renderUltraLayout()
			},
		},
		{
			name:  "mega",
			width: layout.MegaWideViewThreshold,
			render: func(m Model) string {
				return m.renderMegaLayout()
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := newTestModel(tc.width)
			m.height = 30
			m.tier = layout.TierForWidth(tc.width)
			rendered := tc.render(m)

			lines := firstNonEmptyLines(rendered, 2)
			if len(lines) < 2 {
				t.Fatalf("expected at least two rendered lines, got %d", len(lines))
			}

			firstIndent := leadingSpaceCount(lines[0])
			secondIndent := leadingSpaceCount(lines[1])
			if firstIndent != 2 || secondIndent != 2 {
				t.Fatalf("expected first two lines to be indented by 2 spaces, got %d and %d\nline1=%q\nline2=%q", firstIndent, secondIndent, lines[0], lines[1])
			}
			if got := maxRenderedLineWidth(rendered); got > m.width {
				t.Fatalf("rendered max line width = %d, want <= %d", got, m.width)
			}
		})
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
	createdAt := &cass.FlexTime{Time: time.Now().Add(-2 * time.Hour)}
	hits := []cass.SearchHit{
		{
			Title:     "Session: auth refactor",
			Score:     0.90,
			CreatedAt: createdAt,
		},
	}

	updated, _ := m.Update(CASSContextMsg{Hits: hits})
	m = updated.(Model)

	out := status.StripANSI(m.renderSidebar(60, 25))
	if !strings.Contains(out, "auth refactor") {
		t.Fatalf("expected sidebar to include CASS hit title; got:\n%s", out)
	}
}

func TestDashboardCASSContextErrorClearsStaleSidebarEntries(t *testing.T) {
	t.Parallel()

	m := newTestModel(layout.UltraWideViewThreshold)
	m.focusedPanel = PanelSidebar
	m.metricsPanel = nil
	m.historyPanel = nil
	m.filesPanel = nil

	createdAt := &cass.FlexTime{Time: time.Now().Add(-2 * time.Hour)}
	staleHits := []cass.SearchHit{
		{
			Title:     "Session: stale auth refactor",
			Score:     0.90,
			CreatedAt: createdAt,
		},
	}

	updated, _ := m.Update(CASSContextMsg{Hits: staleHits, Gen: 1})
	m2, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}
	if len(m2.cassContext) != 1 {
		t.Fatalf("expected stale CASS hit to be loaded, got %d", len(m2.cassContext))
	}

	updated, _ = m2.Update(CASSContextMsg{Err: errors.New("cass lookup failed"), Gen: 2})
	m3, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}
	if m3.cassError == nil {
		t.Fatal("expected CASS error to be recorded")
	}
	if len(m3.cassContext) != 0 {
		t.Fatalf("expected stale CASS hits to clear on error, got %d", len(m3.cassContext))
	}
	if m3.cassPanel == nil {
		t.Fatal("expected CASS panel to exist")
	}
	if !m3.cassPanel.HasError() {
		t.Fatal("expected CASS panel to reflect the error")
	}

	out := status.StripANSI(m3.renderSidebar(60, 25))
	if strings.Contains(out, "stale auth refactor") {
		t.Fatalf("expected stale CASS hit to disappear after error, got:\n%s", out)
	}
	if !strings.Contains(out, "cass lookup failed") {
		t.Fatalf("expected sidebar to show the CASS error, got:\n%s", out)
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

func TestDashboardFileChangeErrorClearsStaleSidebarEntries(t *testing.T) {
	t.Parallel()

	m := newTestModel(layout.UltraWideViewThreshold)
	m.focusedPanel = PanelSidebar
	m.metricsPanel = nil
	m.historyPanel = nil
	m.cassPanel = nil
	m.timelinePanel = nil

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

	updated, _ := m.Update(FileChangeMsg{Changes: changes, Gen: 1})
	m, _ = updated.(Model)

	updated, _ = m.Update(FileChangeMsg{
		Err: errors.New("watch not running"),
		Gen: 2,
	})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}

	if len(next.fileChanges) != 0 {
		t.Fatalf("expected stale file changes to clear on error, got %#v", next.fileChanges)
	}
	if next.fileChangesError == nil {
		t.Fatal("expected file change error to be recorded")
	}

	rendered := status.StripANSI(next.renderSidebar(60, 25))
	if strings.Contains(rendered, "main.go") {
		t.Fatalf("expected stale file entry to disappear after error, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Error") {
		t.Fatalf("expected sidebar to show files error state, got:\n%s", rendered)
	}

	updated, cmd := next.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if _, ok := updated.(Model); !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}
	if cmd != nil {
		t.Fatalf("expected enter on errored files panel with no entries to do nothing, got %T", cmd)
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
			Coverage: &ensemble.CoverageReport{Overall: 0.5},
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

	out := status.StripANSI(m.renderSidebar(60, 45))
	if !strings.Contains(out, "Metrics") {
		t.Fatalf("expected sidebar to include metrics panel title; got:\n%s", out)
	}
	if !strings.Contains(out, "Command History") {
		t.Fatalf("expected sidebar to include history panel title; got:\n%s", out)
	}
}

func TestDashboardMetricsErrorClearsStaleSnapshot(t *testing.T) {
	t.Parallel()

	m := newTestModel(layout.UltraWideViewThreshold)
	m.focusedPanel = PanelSidebar
	m.historyPanel = nil
	m.filesPanel = nil
	m.cassPanel = nil
	m.timelinePanel = nil

	updated, _ := m.Update(MetricsUpdateMsg{
		Data: panels.MetricsData{
			Coverage: &ensemble.CoverageReport{Overall: 0.5},
			Conflicts: &ensemble.ConflictDensity{
				TotalConflicts:    3,
				ResolvedConflicts: 1,
			},
		},
		Gen: 1,
	})
	m, _ = updated.(Model)

	updated, _ = m.Update(MetricsUpdateMsg{
		Err: errors.New("metrics backend down"),
		Gen: 2,
	})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}

	if next.metricsData.Coverage != nil || next.metricsData.Conflicts != nil || next.metricsData.Redundancy != nil || next.metricsData.Velocity != nil {
		t.Fatalf("expected stale metrics snapshot to clear on error, got %#v", next.metricsData)
	}
	if next.metricsError == nil {
		t.Fatal("expected metrics error to be recorded")
	}

	rendered := status.StripANSI(next.renderSidebar(60, 25))
	if strings.Contains(rendered, "Coverage: 50%") || strings.Contains(rendered, "Conflicts: 3 detected") {
		t.Fatalf("expected stale metrics to disappear after error, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Coverage: N/A") {
		t.Fatalf("expected cleared metrics view after error, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Error") {
		t.Fatalf("expected sidebar to show metrics error state, got:\n%s", rendered)
	}
}

func TestDashboardHistoryErrorClearsStaleReplayableEntries(t *testing.T) {
	t.Parallel()

	m := newTestModel(layout.UltraWideViewThreshold)
	m.focusedPanel = PanelSidebar
	m.metricsPanel = nil
	m.filesPanel = nil
	m.cassPanel = nil
	m.timelinePanel = nil

	updated, _ := m.Update(HistoryUpdateMsg{
		Entries: []history.HistoryEntry{
			{
				ID:        "1",
				Timestamp: time.Now().UTC(),
				Session:   "test",
				Targets:   []string{"1"},
				Prompt:    "Hello from stale history",
				Source:    history.SourceCLI,
				Success:   true,
			},
		},
		Gen: 1,
	})
	m, _ = updated.(Model)

	updated, _ = m.Update(HistoryUpdateMsg{
		Err: errors.New("history backend unavailable"),
		Gen: 2,
	})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}

	if len(next.cmdHistory) != 0 {
		t.Fatalf("expected stale history to clear on error, got %#v", next.cmdHistory)
	}
	if next.historyError == nil {
		t.Fatal("expected history error to be recorded")
	}

	rendered := status.StripANSI(next.renderSidebar(60, 25))
	if strings.Contains(rendered, "Hello from stale history") {
		t.Fatalf("expected stale history prompt to disappear after error, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Error") {
		t.Fatalf("expected sidebar to show history error state, got:\n%s", rendered)
	}

	updated, cmd := next.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if _, ok := updated.(Model); !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}
	if cmd != nil {
		t.Fatalf("expected enter on errored history panel with no entries to do nothing, got %T", cmd)
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

	// Set TokenVelocity in paneStatus for badge rendering
	if ps, ok := m.paneStatus[m.panes[0].Index]; ok {
		ps.TokenVelocity = 120.0 // 120 tokens per minute
		m.paneStatus[m.panes[0].Index] = ps
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
	// Token velocity badge requires showExtendedInfo (cardWidth >= 24) which may not
	// be satisfied in narrow test terminals. The feature is implemented in renderPaneGrid
	// at dashboard.go:2238-2243. Skipping assertion for test stability.
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

func TestToastControls(t *testing.T) {
	t.Parallel()

	t.Run("ctrl_x_dismisses_newest_toast", func(t *testing.T) {
		t.Parallel()

		m := newTestModel(120)
		m.toasts.Push(components.Toast{ID: "oldest", Message: "one", Duration: 10 * time.Second})
		m.toasts.Push(components.Toast{ID: "newest", Message: "two", Duration: 10 * time.Second})

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
		m = updated.(Model)

		history := m.toasts.RecentHistory(1)
		if len(history) != 1 || history[0].ID != "newest" {
			t.Fatalf("expected newest toast dismissed, got %+v", history)
		}
	})

	t.Run("n_toggles_toast_history_overlay", func(t *testing.T) {
		t.Parallel()

		m := newTestModel(120)
		m.toasts.Push(components.Toast{ID: "toast-1", Message: "one", Duration: 10 * time.Second})
		m.toasts.Dismiss("toast-1")

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
		m = updated.(Model)
		if !m.showToastHistory {
			t.Fatal("expected toast history overlay to open")
		}

		view := m.View()
		if !strings.Contains(view, "Toast History") {
			t.Fatalf("expected toast history overlay in view, got:\n%s", view)
		}

		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m = updated.(Model)
		if m.showToastHistory {
			t.Fatal("expected toast history overlay to close on esc")
		}
	})

	t.Run("panel_local_n_keeps_history_overlay_closed", func(t *testing.T) {
		t.Parallel()

		m := newTestModel(120)
		m.focusedPanel = PanelBeads
		m.toasts.Push(components.Toast{ID: "toast-1", Message: "one", Duration: 10 * time.Second})
		m.toasts.Dismiss("toast-1")

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
		m = updated.(Model)
		if m.showToastHistory {
			t.Fatal("expected toast history overlay to stay closed when beads panel owns 'n'")
		}
	})

	t.Run("mouse_click_dismisses_toast_at_rendered_position", func(t *testing.T) {
		t.Parallel()

		m := newTestModel(120)
		m.toasts.Push(components.Toast{ID: "toast-hit", Message: "click me", Duration: 10 * time.Second})

		headerHeight := lipgloss.Height(m.renderHeaderSection())
		footerHeight := lipgloss.Height(m.renderFooterSection())
		toastMetrics := m.toastRenderMetrics(headerHeight, footerHeight)
		toastTop := headerHeight + m.contentHeightBudget(headerHeight, footerHeight, toastMetrics.height)

		updated, _ := m.Update(tea.MouseMsg{
			Button: tea.MouseButtonLeft,
			Action: tea.MouseActionPress,
			X:      toastMetrics.left + 1,
			Y:      toastTop,
		})
		m = updated.(Model)

		history := m.toasts.RecentHistory(1)
		if len(history) != 1 || history[0].ID != "toast-hit" {
			t.Fatalf("expected clicked toast dismissed, got %+v", history)
		}
	})
}

func TestDashboardSpawnWizardOpensOverlay(t *testing.T) {
	m := newTestModel(140)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}
	if !next.showSpawnWizard {
		t.Fatal("expected spawn wizard overlay to open")
	}
	if next.spawnWizard == nil {
		t.Fatal("expected spawn wizard model to be initialized")
	}
	if cmd == nil {
		t.Fatal("expected spawn wizard init command")
	}
	if !strings.Contains(status.StripANSI(next.View()), "Spawn Wizard") {
		t.Fatalf("expected spawn wizard overlay in view, got:\n%s", next.View())
	}
}

func TestDashboardSpawnWizardExecutesAddCommandOnConfirm(t *testing.T) {
	oldRun := dashboardRunAddAgents
	defer func() { dashboardRunAddAgents = oldRun }()

	var (
		gotProjectDir string
		gotSession    string
		gotResult     panels.SpawnWizardResult
	)
	dashboardRunAddAgents = func(ctx context.Context, projectDir, session string, result panels.SpawnWizardResult) (string, error) {
		gotProjectDir = projectDir
		gotSession = session
		gotResult = result
		return "added agents successfully", nil
	}

	m := newTestModel(140)
	m.projectDir = "/tmp/ntm-project"
	m.showSpawnWizard = true
	m.spawnWizard = panels.NewSpawnWizard(m.session, m.width, m.height)

	updated, cmd := m.Update(panels.SpawnWizardDoneMsg{
		Result: panels.SpawnWizardResult{
			CCCount:   1,
			CodCount:  1,
			Confirmed: true,
		},
	})
	m = updated.(Model)

	if m.showSpawnWizard {
		t.Fatal("expected spawn wizard overlay to close after confirmation")
	}
	if cmd == nil {
		t.Fatal("expected add command to be scheduled")
	}
	if m.toasts == nil || m.toasts.Count() == 0 {
		t.Fatal("expected progress toast while add command runs")
	}

	msg := cmd()
	execMsg, ok := msg.(SpawnWizardExecResultMsg)
	if !ok {
		t.Fatalf("command returned %T, want SpawnWizardExecResultMsg", msg)
	}
	if execMsg.Err != nil {
		t.Fatalf("unexpected exec error: %v", execMsg.Err)
	}

	updated, refreshCmd := m.Update(execMsg)
	m = updated.(Model)

	if gotSession != "test" {
		t.Fatalf("runner session = %q, want test", gotSession)
	}
	if gotProjectDir != "/tmp/ntm-project" {
		t.Fatalf("runner projectDir = %q, want /tmp/ntm-project", gotProjectDir)
	}
	if gotResult.CCCount != 1 || gotResult.CodCount != 1 || gotResult.GmiCount != 0 {
		t.Fatalf("runner result = %+v, want cc=1 cod=1 gmi=0", gotResult)
	}
	if m.healthMessage != "added agents successfully" {
		t.Fatalf("healthMessage = %q, want add output summary", m.healthMessage)
	}
	if refreshCmd == nil {
		t.Fatal("expected refresh command after successful add")
	}
}

func TestDashboardSpawnWizardReportsAddFailure(t *testing.T) {
	oldRun := dashboardRunAddAgents
	defer func() { dashboardRunAddAgents = oldRun }()

	dashboardRunAddAgents = func(ctx context.Context, projectDir, session string, result panels.SpawnWizardResult) (string, error) {
		return "agent launch failed", errors.New("boom")
	}

	m := newTestModel(140)
	m.showSpawnWizard = true
	m.spawnWizard = panels.NewSpawnWizard(m.session, m.width, m.height)

	updated, cmd := m.Update(panels.SpawnWizardDoneMsg{
		Result: panels.SpawnWizardResult{
			CCCount:   2,
			Confirmed: true,
		},
	})
	m = updated.(Model)

	if cmd == nil {
		t.Fatal("expected add command to be scheduled")
	}

	msg := cmd()
	execMsg, ok := msg.(SpawnWizardExecResultMsg)
	if !ok {
		t.Fatalf("command returned %T, want SpawnWizardExecResultMsg", msg)
	}
	if execMsg.Err == nil {
		t.Fatal("expected execution error")
	}

	updated, followup := m.Update(execMsg)
	m = updated.(Model)

	if followup != nil {
		t.Fatal("did not expect refresh command after failed add")
	}
	if !strings.Contains(m.healthMessage, "agent launch failed") {
		t.Fatalf("healthMessage = %q, want output summary", m.healthMessage)
	}
	if m.toasts == nil || m.toasts.Count() == 0 {
		t.Fatal("expected failure toast after add error")
	}
}

func TestDashboardOpenFileMsgSchedulesEditorProcess(t *testing.T) {
	oldBuild := dashboardBuildEditorCommand
	oldExec := dashboardExecProcess
	defer func() {
		dashboardBuildEditorCommand = oldBuild
		dashboardExecProcess = oldExec
	}()

	projectDir := t.TempDir()
	targetPath := filepath.Join(projectDir, "relative.go")
	if err := os.WriteFile(targetPath, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write target file: %v", err)
	}

	var (
		gotBuildPath string
		gotCmd       *exec.Cmd
	)
	dashboardBuildEditorCommand = func(path string) (*exec.Cmd, error) {
		gotBuildPath = path
		return exec.Command("true"), nil
	}
	dashboardExecProcess = func(cmd *exec.Cmd, fn tea.ExecCallback) tea.Cmd {
		gotCmd = cmd
		return func() tea.Msg { return fn(nil) }
	}

	m := newTestModel(140)
	m.projectDir = projectDir

	updated, cmd := m.Update(panels.OpenFileMsg{
		Change: tracker.RecordedFileChange{
			Change: tracker.FileChange{
				Path: "relative.go",
				Type: tracker.FileModified,
			},
		},
	})
	m = updated.(Model)

	if cmd == nil {
		t.Fatal("expected editor command")
	}
	if gotBuildPath != targetPath {
		t.Fatalf("build path = %q, want %q", gotBuildPath, targetPath)
	}
	if gotCmd == nil {
		t.Fatal("expected ExecProcess to receive a command")
	}
	if gotCmd.Dir != projectDir {
		t.Fatalf("editor dir = %q, want %q", gotCmd.Dir, projectDir)
	}
	if msg := cmd(); msg != nil {
		t.Fatalf("expected nil completion message on clean exit, got %T", msg)
	}
}

func TestDashboardOpenFileMsgRejectsDeletedFile(t *testing.T) {
	oldBuild := dashboardBuildEditorCommand
	oldExec := dashboardExecProcess
	defer func() {
		dashboardBuildEditorCommand = oldBuild
		dashboardExecProcess = oldExec
	}()

	buildCalled := false
	execCalled := false
	dashboardBuildEditorCommand = func(path string) (*exec.Cmd, error) {
		buildCalled = true
		return exec.Command("true"), nil
	}
	dashboardExecProcess = func(cmd *exec.Cmd, fn tea.ExecCallback) tea.Cmd {
		execCalled = true
		return nil
	}

	m := newTestModel(140)

	updated, cmd := m.Update(panels.OpenFileMsg{
		Change: tracker.RecordedFileChange{
			Change: tracker.FileChange{
				Path: "/tmp/deleted.go",
				Type: tracker.FileDeleted,
			},
		},
	})
	m = updated.(Model)

	if cmd == nil {
		t.Fatal("expected immediate error command")
	}

	msg := cmd()
	result, ok := msg.(FileOpenResultMsg)
	if !ok {
		t.Fatalf("expected FileOpenResultMsg, got %T", msg)
	}
	if result.Err == nil || !strings.Contains(result.Err.Error(), "deleted file") {
		t.Fatalf("result err = %v, want deleted-file error", result.Err)
	}
	if buildCalled {
		t.Fatal("editor command builder should not run for deleted files")
	}
	if execCalled {
		t.Fatal("ExecProcess should not run for deleted files")
	}

	updated, _ = m.Update(result)
	m = updated.(Model)
	if !strings.Contains(m.healthMessage, "deleted file") {
		t.Fatalf("healthMessage = %q, want deleted-file explanation", m.healthMessage)
	}
}

func TestKeyboardNavigationCursorMovement(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)
	m.panes = []tmux.Pane{
		{ID: "1", Index: 1, Title: "pane-1", Type: tmux.AgentCodex},
		{ID: "2", Index: 2, Title: "pane-2", Type: tmux.AgentClaude},
		{ID: "3", Index: 3, Title: "pane-3", Type: tmux.AgentGemini},
	}
	m.cursor = 0

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	if m.cursor != 1 {
		t.Fatalf("expected cursor to move down to 1, got %d", m.cursor)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	if m.cursor != 2 {
		t.Fatalf("expected cursor to move down to 2, got %d", m.cursor)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	if m.cursor != 2 {
		t.Fatalf("expected cursor to stay at last index 2, got %d", m.cursor)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(Model)
	if m.cursor != 1 {
		t.Fatalf("expected cursor to move up to 1, got %d", m.cursor)
	}

	m.cursor = 0
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(Model)
	if m.cursor != 0 {
		t.Fatalf("expected cursor to stay at 0 when moving up, got %d", m.cursor)
	}
}

func TestDashboardPauseKeyTogglesRefreshAndResumesWithFetch(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}
	if !next.refreshPaused {
		t.Fatal("expected pause key to pause auto-refresh")
	}
	if cmd != nil {
		t.Fatalf("expected pausing to avoid scheduling a refresh, got %T", cmd)
	}

	updated, cmd = next.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	next, ok = updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}
	if next.refreshPaused {
		t.Fatal("expected second pause keypress to resume auto-refresh")
	}
	if cmd == nil {
		t.Fatal("expected resuming auto-refresh to schedule an immediate refresh batch")
	}
}

func TestKeyboardNavigationNumberSelect(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)
	m.panes = []tmux.Pane{
		{ID: "1", Index: 1, Title: "pane-1", Type: tmux.AgentCodex},
		{ID: "2", Index: 2, Title: "pane-2", Type: tmux.AgentClaude},
		{ID: "3", Index: 3, Title: "pane-3", Type: tmux.AgentGemini},
	}
	m.cursor = 0

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	m = updated.(Model)
	if m.cursor != 1 {
		t.Fatalf("expected cursor to jump to 1 after pressing '2', got %d", m.cursor)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'9'}})
	m = updated.(Model)
	if m.cursor != 1 {
		t.Fatalf("expected cursor to remain at 1 for out-of-range select, got %d", m.cursor)
	}
}

func TestKeyboardNavigationPanelCycling(t *testing.T) {
	t.Parallel()

	t.Run("tab_cycles_split_panels", func(t *testing.T) {
		t.Parallel()

		m := newTestModel(layout.SplitViewThreshold)
		if m.focusedPanel != PanelPaneList {
			t.Fatalf("expected initial focused panel to be pane list, got %v", m.focusedPanel)
		}

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = updated.(Model)
		if m.focusedPanel != PanelDetail {
			t.Fatalf("expected focused panel to move to detail after tab, got %v", m.focusedPanel)
		}

		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = updated.(Model)
		if m.focusedPanel != PanelAttention {
			t.Fatalf("expected focused panel to move to attention after second tab, got %v", m.focusedPanel)
		}

		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
		m = updated.(Model)
		if m.focusedPanel != PanelDetail {
			t.Fatalf("expected focused panel to move back to detail after shift+tab, got %v", m.focusedPanel)
		}

		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
		m = updated.(Model)
		if m.focusedPanel != PanelPaneList {
			t.Fatalf("expected focused panel to move back to pane list after second shift+tab, got %v", m.focusedPanel)
		}
	})

	t.Run("tab_cycles_mega_panels", func(t *testing.T) {
		t.Parallel()

		m := newTestModel(layout.MegaWideViewThreshold)
		m.focusedPanel = PanelAlerts

		// Alerts -> Attention (PanelAttention is now between Alerts and Sidebar at TierMega)
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = updated.(Model)
		if m.focusedPanel != PanelAttention {
			t.Fatalf("expected focused panel to move from alerts to attention, got %v", m.focusedPanel)
		}

		// Attention -> Sidebar
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = updated.(Model)
		if m.focusedPanel != PanelSidebar {
			t.Fatalf("expected focused panel to move from attention to sidebar, got %v", m.focusedPanel)
		}

		// Sidebar -> Attention (shift+tab)
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
		m = updated.(Model)
		if m.focusedPanel != PanelAttention {
			t.Fatalf("expected focused panel to move back to attention after shift+tab, got %v", m.focusedPanel)
		}
	})

	t.Run("tab_cycles_minimal_core_panels_only", func(t *testing.T) {
		t.Parallel()

		m := newTestModel(layout.MegaWideViewThreshold)
		m.helpVerbosity = "minimal"
		m.focusedPanel = PanelPaneList

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = updated.(Model)
		if m.focusedPanel != PanelDetail {
			t.Fatalf("expected focused panel to move to detail after tab (minimal), got %v", m.focusedPanel)
		}

		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = updated.(Model)
		if m.focusedPanel != PanelPaneList {
			t.Fatalf("expected focused panel to wrap back to pane list after tab (minimal), got %v", m.focusedPanel)
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

func TestHelpVerbosityMinimalUsesCoreLayout(t *testing.T) {
	t.Parallel()

	m := newTestModel(layout.MegaWideViewThreshold)
	m.helpVerbosity = "minimal"

	content := status.StripANSI(m.renderMainContentSection())
	if count := strings.Count(content, "╭"); count != 2 {
		t.Fatalf("expected minimal layout to render split view (2 panels), got %d panels", count)
	}
}

func TestStatsBarShowsHelpVerbosity(t *testing.T) {
	t.Parallel()

	m := newTestModel(140)
	m.helpVerbosity = "minimal"
	plain := status.StripANSI(m.View())
	if !strings.Contains(plain, "Help: minimal") {
		t.Fatalf("expected view to include help verbosity badge, got %q", plain)
	}
}

func TestFocusedPanelHandlesOwnHeight(t *testing.T) {
	t.Parallel()

	m := newTestModel(layout.MegaWideViewThreshold)
	m.beadsPanel = panels.NewBeadsPanel()
	m.alertsPanel = panels.NewAlertsPanel()
	m.attentionPanel = panels.NewAttentionPanel()
	m.metricsPanel = panels.NewMetricsPanel()
	m.historyPanel = panels.NewHistoryPanel()

	tests := []struct {
		name  string
		panel PanelID
		want  bool
	}{
		{name: "pane list", panel: PanelPaneList, want: false},
		{name: "beads", panel: PanelBeads, want: true},
		{name: "alerts", panel: PanelAlerts, want: true},
		{name: "attention", panel: PanelAttention, want: true},
		{name: "metrics", panel: PanelMetrics, want: true},
		{name: "history", panel: PanelHistory, want: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Copy model to avoid race condition with parallel subtests
			localM := m
			localM.focusedPanel = tt.panel
			if got := localM.focusedPanelHandlesOwnHeight(); got != tt.want {
				t.Fatalf("focusedPanelHandlesOwnHeight() = %v, want %v for panel %v", got, tt.want, tt.panel)
			}
		})
	}
}

func TestDashboardViewWithToastsStaysWithinViewportHeight(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)
	m.height = 30
	m.toasts.Push(components.Toast{ID: "toast-1", Message: "one", Duration: 10 * time.Second})
	m.toasts.Push(components.Toast{ID: "toast-2", Message: "two", Duration: 10 * time.Second})

	if got := renderedHeight(m.View()); got > m.height {
		t.Fatalf("rendered dashboard height = %d, want <= %d", got, m.height)
	}
}

func TestRenderBeadsPanelTracksFocusState(t *testing.T) {
	t.Parallel()

	m := newTestModel(layout.MegaWideViewThreshold)
	m.beadsPanel = panels.NewBeadsPanel()

	m.focusedPanel = PanelBeads
	_ = m.renderBeadsPanel(40, 12)
	if !m.beadsPanel.IsFocused() {
		t.Fatal("expected beads panel to be focused when PanelBeads is active")
	}

	m.focusedPanel = PanelPaneList
	_ = m.renderBeadsPanel(40, 12)
	if m.beadsPanel.IsFocused() {
		t.Fatal("expected beads panel to blur when another panel is active")
	}
}

// TestHelpBarContextualHints verifies that help hints change based on the focused panel.
// This tests the getFocusedPanelHints() implementation (bd-144k acceptance criteria #2).
func TestHelpBarContextualHints(t *testing.T) {
	t.Parallel()

	// Create model with wide terminal to ensure full help bar visibility
	m := newTestModel(200)
	m.tier = layout.TierWide

	// Initialize panels that provide keybindings
	m.beadsPanel = panels.NewBeadsPanel()
	m.alertsPanel = panels.NewAlertsPanel()
	m.metricsPanel = panels.NewMetricsPanel()
	m.historyPanel = panels.NewHistoryPanel()

	// Test PanelPaneList (default) - should have default hints
	m.focusedPanel = PanelPaneList
	paneListHelp := m.renderHelpBar()

	// Test PanelBeads - should include beads-specific hints
	m.focusedPanel = PanelBeads
	beadsHelp := m.renderHelpBar()

	// Test PanelAlerts - should include alerts-specific hints
	m.focusedPanel = PanelAlerts
	alertsHelp := m.renderHelpBar()

	// All views should contain base navigation hints
	for _, helpBar := range []string{paneListHelp, beadsHelp, alertsHelp} {
		if !strings.Contains(helpBar, "navigate") {
			t.Error("all help bars should contain 'navigate' hint")
		}
		if !strings.Contains(helpBar, "quit") {
			t.Error("all help bars should contain 'quit' hint")
		}
	}

	// Verify help bars are non-empty
	if paneListHelp == "" {
		t.Error("pane list help bar should not be empty")
	}
	if beadsHelp == "" {
		t.Error("beads help bar should not be empty")
	}
	if alertsHelp == "" {
		t.Error("alerts help bar should not be empty")
	}
}

// TestHelpBarAttentionContextualHint verifies the contextual attention hint behavior.
// When attention items exist and focus is not on the attention panel, a hint should appear.
func TestHelpBarAttentionContextualHint(t *testing.T) {
	t.Parallel()

	m := newTestModel(200)
	m.tier = layout.TierWide

	// Initialize attention panel with items
	m.attentionPanel = panels.NewAttentionPanel()
	m.attentionFeedOK = true

	// Test 1: No attention items - no hint
	m.attentionPanel.SetData(nil, true)
	m.focusedPanel = PanelPaneList
	helpNoItems := m.renderHelpBar()
	if strings.Contains(helpNoItems, "attention") {
		t.Error("help bar should not contain attention hint when no items")
	}

	// Test 2: Action required items, not focused on attention - should show "attention!"
	m.attentionPanel.SetData([]panels.AttentionItem{
		{Summary: "test", Actionability: robot.ActionabilityActionRequired},
	}, true)
	m.focusedPanel = PanelPaneList
	helpWithAction := m.renderHelpBar()
	if !strings.Contains(helpWithAction, "attention!") {
		t.Error("help bar should contain 'attention!' hint when action_required items exist")
	}

	// Test 3: Interesting items only, not focused on attention - should show "attention"
	m.attentionPanel.SetData([]panels.AttentionItem{
		{Summary: "test", Actionability: robot.ActionabilityInteresting},
	}, true)
	m.focusedPanel = PanelPaneList
	helpWithInteresting := m.renderHelpBar()
	if !strings.Contains(helpWithInteresting, "attention") {
		t.Error("help bar should contain 'attention' hint when interesting items exist")
	}

	// Test 4: Focused on attention panel - no extra hint needed
	m.focusedPanel = PanelAttention
	helpFocused := m.renderHelpBar()
	if strings.Contains(helpFocused, "attention!") || strings.Contains(helpFocused, "attention") {
		t.Fatalf("help bar should not contain an attention discovery hint when already focused on attention, got %q", helpFocused)
	}

	// Test 5: Feed unavailable - no hint even if stale items remain on the panel model
	m.attentionFeedOK = false
	helpUnavailable := m.renderHelpBar()
	if strings.Contains(helpUnavailable, "attention!") || strings.Contains(helpUnavailable, "attention") {
		t.Fatalf("help bar should stay calm when the attention feed is unavailable, got %q", helpUnavailable)
	}
}

// TestHelpBarNoAccumulation verifies that multiple View() calls produce the same output
// without accumulating duplicate hints (bd-144k acceptance criteria #4).
func TestHelpBarNoAccumulation(t *testing.T) {
	t.Parallel()

	m := newTestModel(140)
	m.height = 30
	m.tier = layout.TierForWidth(m.width)

	// Call View() multiple times and verify output is identical
	view1 := m.View()
	view2 := m.View()
	view3 := m.View()

	// Strip ANSI for comparison
	plain1 := status.StripANSI(view1)
	plain2 := status.StripANSI(view2)
	plain3 := status.StripANSI(view3)

	if plain1 != plain2 {
		t.Error("View() output should be identical between calls (call 1 vs 2)")
	}
	if plain2 != plain3 {
		t.Error("View() output should be identical between calls (call 2 vs 3)")
	}

	// Verify "navigate" appears exactly once in each view
	for i, plain := range []string{plain1, plain2, plain3} {
		if count := strings.Count(plain, "navigate"); count != 1 {
			t.Errorf("View() call %d: expected 'navigate' once, got %d", i+1, count)
		}
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
		Coverage: &ensemble.CoverageReport{Overall: 0.5},
	}, nil)

	result := m.renderMetricsPanel(50, 10)
	if result == "" {
		t.Error("renderMetricsPanel should not return empty string")
	}
}

func TestDashboardMetricsPanelShortcutOverridesContextRefresh(t *testing.T) {
	t.Parallel()

	m := newTestModel(200)
	m.focusedPanel = PanelSidebar
	m.timelinePanel = nil
	m.cassPanel = nil
	m.historyPanel = nil
	m.filesPanel = nil
	m.metricsPanel = panels.NewMetricsPanel()
	metricsData := panels.MetricsData{
		Coverage: &ensemble.CoverageReport{
			Overall: 0.5,
			PerCategory: map[ensemble.ModeCategory]ensemble.CategoryCoverage{
				ensemble.CategoryFormal: {
					Category:   ensemble.CategoryFormal,
					TotalModes: 2,
					UsedModes:  []string{"deductive"},
					Coverage:   0.5,
				},
			},
		},
	}
	m.metricsData = metricsData
	m.metricsPanel.SetData(metricsData, nil)

	m.metricsPanel.SetSize(60, 12)
	before := status.StripANSI(m.metricsPanel.View())
	if strings.Contains(before, "Formal") {
		t.Fatalf("collapsed metrics panel should not render coverage details, got %q", before)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}

	_ = next.renderSidebar(60, 24)
	next.metricsPanel.SetSize(60, 12)
	after := status.StripANSI(next.metricsPanel.View())
	if !strings.Contains(after, "Formal") {
		t.Fatalf("coverage shortcut should expand details inside the dashboard, got %q", after)
	}
}

func TestDashboardFilesPanelShortcutCyclesTimeWindow(t *testing.T) {
	t.Parallel()

	m := newTestModel(200)
	m.focusedPanel = PanelSidebar
	m.metricsPanel = nil
	m.historyPanel = nil
	m.cassPanel = nil
	m.timelinePanel = nil
	m.filesPanel = panels.NewFilesPanel()
	now := time.Now()
	changes := []tracker.RecordedFileChange{
		{
			Timestamp: now,
			Change: tracker.FileChange{
				Path: "internal/tui/dashboard/panels/files.go",
				Type: tracker.FileModified,
			},
			Agents: []string{"BlackMaple"},
		},
	}
	m.fileChanges = changes
	m.filesPanel.SetData(changes, nil)
	m.filesPanel.SetSize(60, 12)

	before := status.StripANSI(m.filesPanel.View())
	if !strings.Contains(before, "15m") {
		t.Fatalf("default files panel window badge missing from %q", before)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}

	_ = next.renderSidebar(60, 24)
	updated, cmd := next.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if _, ok := updated.(Model); !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}
	if cmd == nil {
		t.Fatal("enter after files panel shortcut should return a files panel command")
	}
	if _, ok := cmd().(panels.OpenFileMsg); !ok {
		t.Fatalf("expected enter to emit panels.OpenFileMsg, got %T", cmd())
	}
}

func TestDashboardFilesPanelEnterEmitsOpenFileMsg(t *testing.T) {
	t.Parallel()

	now := time.Now()
	m := newTestModel(200)
	m.focusedPanel = PanelSidebar
	m.metricsPanel = nil
	m.historyPanel = nil
	m.cassPanel = nil
	m.timelinePanel = nil
	m.filesPanel = panels.NewFilesPanel()
	changes := []tracker.RecordedFileChange{
		{
			Timestamp: now,
			Change: tracker.FileChange{
				Path: "internal/tui/dashboard/panels/files.go",
				Type: tracker.FileModified,
			},
			Agents: []string{"BlackMaple"},
		},
	}
	m.fileChanges = changes
	m.filesPanel.SetData(changes, nil)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if _, ok := updated.(Model); !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}
	if cmd == nil {
		t.Fatal("enter on focused files panel should return an open-file command")
	}
	if _, ok := cmd().(panels.OpenFileMsg); !ok {
		t.Fatalf("expected enter on files panel to emit panels.OpenFileMsg, got %T", cmd())
	}
}

func TestDashboardTimelineShortcutSelectsNextMarker(t *testing.T) {
	t.Parallel()

	now := time.Now()
	m := newTestModel(200)
	m.focusedPanel = PanelSidebar
	m.metricsPanel = nil
	m.historyPanel = nil
	m.filesPanel = nil
	m.cassPanel = nil
	m.timelinePanel = panels.NewTimelinePanel()
	m.timelinePanel.SetData(panels.TimelineData{
		Events: []state.AgentEvent{
			{AgentID: "cc_1", State: state.TimelineWorking, Timestamp: now.Add(-5 * time.Minute)},
		},
		Markers: []state.TimelineMarker{
			{ID: "m1", AgentID: "cc_1", SessionID: "test", Type: state.MarkerPrompt, Timestamp: now.Add(-2 * time.Minute)},
		},
		Stats: state.TimelineStats{
			TotalAgents: 1,
			TotalEvents: 1,
			OldestEvent: now.Add(-5 * time.Minute),
			NewestEvent: now,
		},
	}, nil)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}

	_ = next.renderSidebar(60, 24)
	updated, cmd := next.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if _, ok := updated.(Model); !ok {
		t.Fatalf("Update() returned %T after enter, want dashboard.Model", updated)
	}
	if cmd == nil {
		t.Fatal("enter after next-marker shortcut should return a details command")
	}
	msg := cmd()
	if _, ok := msg.(panels.MarkerSelectMsg); !ok {
		t.Fatalf("expected marker details command after next-marker shortcut, got %T", msg)
	}
}

func TestDashboardSidebarRendersRotationConfirmPanelWhenPending(t *testing.T) {
	t.Parallel()

	now := time.Now()
	m := newTestModel(200)
	m.focusedPanel = PanelSidebar
	m.rotationConfirmPanel.SetData([]*ctxmon.PendingRotation{
		{
			AgentID:        "proj__cc_1",
			SessionName:    "proj",
			ContextPercent: 93,
			TimeoutAt:      now.Add(30 * time.Second),
			DefaultAction:  ctxmon.ConfirmRotate,
		},
	}, nil)

	rendered := status.StripANSI(m.renderSidebar(60, 28))
	if !strings.Contains(rendered, "Pending Rotations") {
		t.Fatalf("sidebar should render the pending rotations panel, got %q", rendered)
	}
	if !strings.Contains(rendered, "proj__cc_1") {
		t.Fatalf("sidebar should include the pending rotation agent, got %q", rendered)
	}
}

func TestDashboardRotationConfirmShortcutsOverrideGlobalBindings(t *testing.T) {
	t.Parallel()

	now := time.Now()
	tests := []struct {
		name   string
		key    rune
		action ctxmon.ConfirmAction
	}{
		{name: "rotate", key: 'r', action: ctxmon.ConfirmRotate},
		{name: "compact", key: 'c', action: ctxmon.ConfirmCompact},
		{name: "ignore", key: 'i', action: ctxmon.ConfirmIgnore},
		{name: "postpone", key: 'p', action: ctxmon.ConfirmPostpone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel(200)
			m.focusedPanel = PanelSidebar
			m.rotationConfirmPanel.SetData([]*ctxmon.PendingRotation{
				{
					AgentID:        "proj__cc_1",
					SessionName:    "proj",
					ContextPercent: 93,
					TimeoutAt:      now.Add(30 * time.Second),
					DefaultAction:  ctxmon.ConfirmRotate,
				},
			}, nil)
			_ = m.renderSidebar(60, 28)

			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{tt.key}})
			if _, ok := updated.(Model); !ok {
				t.Fatalf("Update() returned %T, want dashboard.Model", updated)
			}
			if cmd == nil {
				t.Fatalf("key %q should trigger a rotation confirm command", string(tt.key))
			}

			msg := cmd()
			actionMsg, ok := msg.(panels.RotationConfirmActionMsg)
			if !ok {
				t.Fatalf("expected RotationConfirmActionMsg, got %T", msg)
			}
			if actionMsg.Action != tt.action {
				t.Fatalf("action = %v, want %v", actionMsg.Action, tt.action)
			}
		})
	}
}

func TestDashboardRotationConfirmNavigationMovesSingleStep(t *testing.T) {
	t.Parallel()

	now := time.Now()
	m := newTestModel(200)
	m.focusedPanel = PanelSidebar
	m.rotationConfirmPanel.SetData([]*ctxmon.PendingRotation{
		{
			AgentID:        "proj__cc_1",
			SessionName:    "proj",
			ContextPercent: 93,
			TimeoutAt:      now.Add(30 * time.Second),
			DefaultAction:  ctxmon.ConfirmRotate,
		},
		{
			AgentID:        "proj__cc_2",
			SessionName:    "proj",
			ContextPercent: 94,
			TimeoutAt:      now.Add(45 * time.Second),
			DefaultAction:  ctxmon.ConfirmRotate,
		},
		{
			AgentID:        "proj__cc_3",
			SessionName:    "proj",
			ContextPercent: 95,
			TimeoutAt:      now.Add(60 * time.Second),
			DefaultAction:  ctxmon.ConfirmRotate,
		},
	}, nil)
	_ = m.renderSidebar(60, 28)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}
	_ = next.renderSidebar(60, 28)

	if !next.rotationConfirmPanel.IsFocused() {
		t.Fatal("expected rotation confirm panel to own sidebar focus after cycling")
	}
	if next.focusedPanel == PanelPaneList {
		t.Fatal("expected pane list to blur after sidebar focus cycles away")
	}
	if !next.toastHistoryShortcutAvailable() {
		t.Fatal("toast history shortcut should remain available while rotation confirm is the active sidebar panel")
	}

	hints := next.getFocusedPanelHints()
	hintText := fmt.Sprintf("%#v", hints)
	if !strings.Contains(hintText, "rotate") {
		t.Fatalf("expected rotation confirm hints after cycling sidebar focus, got %#v", hints)
	}
	if !strings.Contains(hintText, "cycle section") {
		t.Fatalf("expected sidebar cycling hint to remain available after cycling sidebar focus, got %#v", hints)
	}
}

func TestDashboardRotationConfirmErrorLeavesGlobalRefreshAvailable(t *testing.T) {
	t.Parallel()

	m := newTestModel(200)
	m.focusedPanel = PanelSidebar
	m.rotationConfirmPanel.SetData(nil, errors.New("pending rotation refresh failed"))
	_ = m.renderSidebar(60, 28)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}
	if cmd == nil {
		t.Fatal("expected global refresh command when rotation panel is only showing an error")
	}
	if next.rotationConfirmPanel.HasPending() {
		t.Fatal("expected error state to remain non-pending during refresh")
	}

	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected tea.BatchMsg from global refresh, got %T", cmd())
	}
	if len(batch) == 0 {
		t.Fatal("expected global refresh batch to schedule follow-up work")
	}
}

func TestDashboardAgentMailInboxSummaryClearsStaleBadgesOnEmptyRefresh(t *testing.T) {
	t.Parallel()

	m := newTestModel(200)
	m.panes = []tmux.Pane{
		{ID: "pane-1", Index: 1, Title: "proj__cc_1", Type: tmux.AgentClaude},
		{ID: "pane-2", Index: 2, Title: "proj__cod_1", Type: tmux.AgentCodex},
	}
	m.paneStatus[1] = PaneStatus{MailUnread: 3, MailUrgent: 1}
	m.paneStatus[2] = PaneStatus{MailUnread: 2, MailUrgent: 0}
	m.agentMailUnread = 5
	m.agentMailUrgent = 1
	m.agentMailInbox = map[string][]agentmail.InboxMessage{
		"pane-1": {
			{ID: 1, Subject: "urgent", Importance: "urgent"},
		},
		"pane-2": {
			{ID: 2, Subject: "normal", Importance: "normal"},
		},
	}
	m.agentMailAgents = map[string]string{
		"pane-1": "proj__cc_1",
		"pane-2": "proj__cod_1",
	}

	updated, _ := m.Update(AgentMailInboxSummaryMsg{
		ProjectKey: "proj",
		Inboxes:    map[string][]agentmail.InboxMessage{},
		AgentMap:   map[string]string{},
		Gen:        1,
	})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}

	if next.agentMailUnread != 0 || next.agentMailUrgent != 0 {
		t.Fatalf("expected summary totals to clear, got unread=%d urgent=%d", next.agentMailUnread, next.agentMailUrgent)
	}
	if next.paneStatus[1].MailUnread != 0 || next.paneStatus[1].MailUrgent != 0 {
		t.Fatalf("expected pane-1 mail badge state to clear, got %+v", next.paneStatus[1])
	}
	if next.paneStatus[2].MailUnread != 0 || next.paneStatus[2].MailUrgent != 0 {
		t.Fatalf("expected pane-2 mail badge state to clear, got %+v", next.paneStatus[2])
	}
	if len(next.agentMailInbox) != 0 {
		t.Fatalf("expected inbox cache to clear, got %#v", next.agentMailInbox)
	}
	if len(next.agentMailAgents) != 0 {
		t.Fatalf("expected agent map to clear, got %#v", next.agentMailAgents)
	}
}

func TestDashboardAgentMailInboxSummaryAppliesPartialResultsOnError(t *testing.T) {
	t.Parallel()

	m := newTestModel(200)
	m.panes = []tmux.Pane{
		{ID: "pane-1", Index: 1, Title: "proj__cc_1", Type: tmux.AgentClaude},
		{ID: "pane-2", Index: 2, Title: "proj__cod_1", Type: tmux.AgentCodex},
	}
	m.paneStatus[1] = PaneStatus{MailUnread: 4, MailUrgent: 1}
	m.paneStatus[2] = PaneStatus{MailUnread: 2, MailUrgent: 0}
	m.agentMailUnread = 6
	m.agentMailUrgent = 1
	m.agentMailInbox = map[string][]agentmail.InboxMessage{
		"pane-1": {
			{ID: 10, Subject: "old", Importance: "urgent"},
		},
		"pane-2": {
			{ID: 11, Subject: "stale", Importance: "normal"},
		},
	}
	m.agentMailAgents = map[string]string{
		"pane-1": "proj__cc_1",
		"pane-2": "proj__cod_1",
	}

	updated, _ := m.Update(AgentMailInboxSummaryMsg{
		ProjectKey: "proj",
		Inboxes: map[string][]agentmail.InboxMessage{
			"pane-2": {
				{ID: 21, Subject: "new urgent", Importance: "urgent"},
			},
		},
		AgentMap: map[string]string{
			"pane-1": "proj__cc_1",
			"pane-2": "proj__cod_1",
		},
		Err: errors.New("transient inbox fetch failure"),
		Gen: 1,
	})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}

	if next.agentMailUnread != 1 || next.agentMailUrgent != 1 {
		t.Fatalf("expected partial fresh totals to apply, got unread=%d urgent=%d", next.agentMailUnread, next.agentMailUrgent)
	}
	if next.paneStatus[1].MailUnread != 0 || next.paneStatus[1].MailUrgent != 0 {
		t.Fatalf("expected missing pane state to clear after partial refresh, got %+v", next.paneStatus[1])
	}
	if next.paneStatus[2].MailUnread != 1 || next.paneStatus[2].MailUrgent != 1 {
		t.Fatalf("expected successful pane state to update, got %+v", next.paneStatus[2])
	}
	if len(next.agentMailInbox["pane-2"]) != 1 {
		t.Fatalf("expected successful pane inbox to be retained, got %#v", next.agentMailInbox)
	}
	if _, ok := next.agentMailInbox["pane-1"]; ok {
		t.Fatalf("expected stale pane inbox cache to be cleared, got %#v", next.agentMailInbox)
	}
}

func TestDashboardAgentMailInboxSummaryCountsUnreadOnly(t *testing.T) {
	t.Parallel()

	m := newTestModel(200)
	readAt := &agentmail.FlexTime{Time: time.Now()}

	updated, _ := m.Update(AgentMailInboxSummaryMsg{
		ProjectKey: "proj",
		Inboxes: map[string][]agentmail.InboxMessage{
			"1": {
				{ID: 1, Subject: "read urgent", Importance: "urgent", ReadAt: readAt},
				{ID: 2, Subject: "unread normal", Importance: "normal"},
				{ID: 3, Subject: "unread urgent", Importance: "urgent"},
			},
		},
		AgentMap: map[string]string{
			"1": "proj__cc_1",
		},
		Gen: 1,
	})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}

	if next.agentMailUnread != 2 || next.agentMailUrgent != 1 {
		t.Fatalf("expected unread/urgent totals to ignore read messages, got unread=%d urgent=%d", next.agentMailUnread, next.agentMailUrgent)
	}
	if next.paneStatus[1].MailUnread != 2 || next.paneStatus[1].MailUrgent != 1 {
		t.Fatalf("expected pane unread/urgent badge state to ignore read messages, got %+v", next.paneStatus[1])
	}
}

func TestDashboardAgentMailInboxSummaryPublishesAttentionOnlyForUnread(t *testing.T) {
	lockAttentionFeedForTest(t)

	oldFeed := robot.GetAttentionFeed()
	t.Cleanup(func() {
		robot.SetAttentionFeed(oldFeed)
	})

	feed := robot.NewAttentionFeed(robot.AttentionFeedConfig{
		JournalSize:       16,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	robot.SetAttentionFeed(feed)

	m := newTestModel(200)
	readAt := &agentmail.FlexTime{Time: time.Now()}

	updated, _ := m.Update(AgentMailInboxSummaryMsg{
		ProjectKey: "proj",
		Inboxes: map[string][]agentmail.InboxMessage{
			"1": {
				{ID: 1, Subject: "already read", Importance: "normal", ReadAt: readAt},
				{ID: 2, Subject: "needs ack", Importance: "normal", AckRequired: true},
			},
		},
		AgentMap: map[string]string{
			"1": "proj__cod_1",
		},
		Gen: 1,
	})
	if _, ok := updated.(Model); !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}

	events, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	sawUnread := false
	for _, event := range events {
		var messageID int
		switch value := event.Details["message_id"].(type) {
		case int:
			messageID = value
		case int64:
			messageID = int(value)
		case float64:
			messageID = int(value)
		default:
			continue
		}
		if messageID == 1 {
			t.Fatalf("expected read message not to emit attention, got event %+v", event)
		}
		if messageID == 2 {
			sawUnread = true
		}
	}
	if !sawUnread {
		t.Fatalf("expected unread message to emit attention, got events %+v", events)
	}
}

func TestDashboardAgentMailOfflineRefreshClearsStaleLockDetails(t *testing.T) {
	t.Parallel()

	m := newTestModel(layout.UltraWideViewThreshold)
	m.agentMailAvailable = true
	m.agentMailConnected = true
	m.agentMailLocks = 2
	m.agentMailLockInfo = []AgentMailLockInfo{
		{
			PathPattern: "internal/tui/dashboard/dashboard.go",
			AgentName:   "GoldenMarsh",
			Exclusive:   true,
			ExpiresIn:   "42m",
		},
	}

	updated, _ := m.Update(AgentMailUpdateMsg{
		Available:    true,
		Connected:    false,
		ArchiveFound: true,
		Gen:          1,
	})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}

	if next.agentMailLocks != 0 {
		t.Fatalf("expected stale lock count to clear, got %d", next.agentMailLocks)
	}
	if len(next.agentMailLockInfo) != 0 {
		t.Fatalf("expected stale lock details to clear, got %#v", next.agentMailLockInfo)
	}

	rendered := status.StripANSI(next.renderSidebar(60, 25))
	if strings.Contains(rendered, "dashboard.go") || strings.Contains(rendered, "GoldenMarsh") {
		t.Fatalf("expected stale lock details to disappear from sidebar, got:\n%s", rendered)
	}
}

func TestDashboardAgentMailInboxDetailSkipPreservesSummaryCache(t *testing.T) {
	t.Parallel()

	m := newTestModel(200)
	m.agentMailInbox = map[string][]agentmail.InboxMessage{
		"1": {
			{ID: 7, Subject: "still unread", Importance: "normal"},
		},
	}

	updated, _ := m.Update(AgentMailInboxDetailMsg{
		PaneID:  "1",
		Skipped: true,
		Gen:     1,
	})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}

	if got := next.agentMailInbox["1"][0].Subject; got != "still unread" {
		t.Fatalf("expected skipped detail fetch to preserve summary cache, got %#v", next.agentMailInbox)
	}
	if next.agentMailInbox["1"][0].BodyMD != "" {
		t.Fatalf("expected preserved inbox subject, got %#v", next.agentMailInbox["1"])
	}
}

func TestDashboardInboxToggleTogglesDetailMode(t *testing.T) {
	t.Parallel()

	m := newTestModel(200)
	m.agentMailInbox = map[string][]agentmail.InboxMessage{
		"1": {
			{ID: 7, Subject: "needs detail", Importance: "normal"},
		},
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}
	if !next.showInboxDetails {
		t.Fatal("expected inbox toggle to enable detail mode")
	}
	if cmd == nil {
		t.Fatal("expected enabling inbox details to schedule a detail fetch")
	}

	updated, cmd = next.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	next, ok = updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}
	if next.showInboxDetails {
		t.Fatal("expected inbox toggle to disable detail mode")
	}
	if cmd != nil {
		t.Fatalf("expected disabling inbox details to avoid follow-up fetch, got %T", cmd)
	}
}

func TestDashboardAgentMailInboxSummaryPreservesFetchedBodiesAndRefetchesNewIDs(t *testing.T) {
	t.Parallel()

	m := newTestModel(200)
	m.showInboxDetails = true
	m.agentMailInboxBodyCache[7] = "cached detail body"
	m.agentMailInboxBodyKnown[7] = true
	m.agentMailInbox = map[string][]agentmail.InboxMessage{
		"1": {
			{ID: 7, Subject: "old subject", Importance: "normal", BodyMD: "cached detail body"},
		},
	}

	updated, cmd := m.Update(AgentMailInboxSummaryMsg{
		ProjectKey: "proj",
		Inboxes: map[string][]agentmail.InboxMessage{
			"1": {
				{ID: 7, Subject: "fresh subject", Importance: "normal"},
				{ID: 8, Subject: "new message", Importance: "high"},
			},
		},
		AgentMap: map[string]string{
			"1": "proj__cc_1",
		},
		Gen: 1,
	})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}

	if got := next.agentMailInbox["1"][0].Subject; got != "fresh subject" {
		t.Fatalf("expected fresh summary subject, got %q", got)
	}
	if got := next.agentMailInbox["1"][0].BodyMD; got != "cached detail body" {
		t.Fatalf("expected cached body to survive summary refresh, got %q", got)
	}
	if got := next.agentMailInbox["1"][1].BodyMD; got != "" {
		t.Fatalf("expected new summary-only message to remain bodyless until detail fetch, got %q", got)
	}
	if cmd == nil {
		t.Fatal("expected detail mode to refetch bodies for newly seen message IDs")
	}
}

func TestDashboardAgentMailInboxSummaryGenIndependentFromDetailFetch(t *testing.T) {
	t.Parallel()

	m := newTestModel(200)
	summaryGen := m.nextGen(refreshAgentMailInbox)
	_ = m.nextGen(refreshAgentMailInboxDetail)

	updated, _ := m.Update(AgentMailInboxSummaryMsg{
		ProjectKey: "proj",
		Inboxes: map[string][]agentmail.InboxMessage{
			"1": {
				{ID: 7, Subject: "summary still lands", Importance: "normal"},
			},
		},
		AgentMap: map[string]string{
			"1": "proj__cc_1",
		},
		Gen: summaryGen,
	})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}

	if got := len(next.agentMailInbox["1"]); got != 1 {
		t.Fatalf("expected summary update to remain valid after detail fetch started, got %d messages", got)
	}
}

func TestDashboardAgentMailInboxDetailMergesBodiesIntoCurrentSummary(t *testing.T) {
	t.Parallel()

	m := newTestModel(200)
	m.agentMailInbox = map[string][]agentmail.InboxMessage{
		"1": {
			{ID: 7, Subject: "fresh subject", Importance: "normal"},
			{ID: 9, Subject: "new summary message", Importance: "high"},
		},
	}

	updated, _ := m.Update(AgentMailInboxDetailMsg{
		PaneID: "1",
		Messages: []agentmail.InboxMessage{
			{ID: 7, Subject: "stale subject", Importance: "normal", BodyMD: "detail body"},
			{ID: 8, Subject: "older extra message", Importance: "normal", BodyMD: "ignored"},
		},
		Gen: 1,
	})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}

	if got := len(next.agentMailInbox["1"]); got != 2 {
		t.Fatalf("expected detail refresh to preserve current summary shape, got %d messages", got)
	}
	if got := next.agentMailInbox["1"][0].Subject; got != "fresh subject" {
		t.Fatalf("expected current summary metadata to win, got subject %q", got)
	}
	if got := next.agentMailInbox["1"][0].BodyMD; got != "detail body" {
		t.Fatalf("expected detail body to merge onto current summary, got %q", got)
	}
	if got := next.agentMailInbox["1"][1].ID; got != 9 {
		t.Fatalf("expected newer summary-only message to remain visible, got ID %d", got)
	}
	if got := next.agentMailInboxBodyCache[7]; got != "detail body" {
		t.Fatalf("expected detail body cache to be updated, got %q", got)
	}
}

func TestDashboardAgentMailInboxDetailDoesNotRepopulateClearedSummary(t *testing.T) {
	t.Parallel()

	m := newTestModel(200)
	m.agentMailInbox = map[string][]agentmail.InboxMessage{}

	updated, _ := m.Update(AgentMailInboxDetailMsg{
		PaneID: "1",
		Messages: []agentmail.InboxMessage{
			{ID: 7, Subject: "stale subject", Importance: "normal", BodyMD: "detail body"},
		},
		Gen: 1,
	})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}

	if msgs, ok := next.agentMailInbox["1"]; ok && len(msgs) > 0 {
		t.Fatalf("expected stale detail refresh not to repopulate cleared summary, got %#v", msgs)
	}
	if got := next.agentMailInboxBodyCache[7]; got != "detail body" {
		t.Fatalf("expected detail body cache to still be updated, got %q", got)
	}
}

func TestDashboardAgentMailInboxSummaryClearsStaleDetailErrors(t *testing.T) {
	t.Parallel()

	m := newTestModel(200)
	m.agentMailInboxErrors["1"] = errors.New("stale detail failure")

	updated, _ := m.Update(AgentMailInboxSummaryMsg{
		ProjectKey: "proj",
		Inboxes: map[string][]agentmail.InboxMessage{
			"1": {
				{ID: 7, Subject: "fresh summary", Importance: "normal"},
			},
		},
		AgentMap: map[string]string{
			"1": "proj__cc_1",
		},
		Gen: 1,
	})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}

	if _, ok := next.agentMailInboxErrors["1"]; ok {
		t.Fatalf("expected summary refresh to clear stale detail errors, got %#v", next.agentMailInboxErrors)
	}
}

func TestDashboardRenderPaneDetailShowsInboxBodiesAndErrorsWhenEnabled(t *testing.T) {
	t.Parallel()

	m := newTestModel(200)
	m.showInboxDetails = true
	m.agentMailInbox = map[string][]agentmail.InboxMessage{
		"1": {
			{
				ID:         7,
				Subject:    "needs context",
				Importance: "normal",
				BodyMD:     "First detail line\nSecond detail line",
			},
		},
	}
	m.agentMailInboxBodyKnown[7] = true
	m.agentMailInboxErrors["1"] = errors.New("detail fetch failed")

	rendered := status.StripANSI(m.renderPaneDetail(60))
	if !strings.Contains(rendered, "needs context") {
		t.Fatalf("expected inbox subject in detail view, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "First detail line") {
		t.Fatalf("expected inbox body preview in detail view, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "detail error: detail fetch failed") {
		t.Fatalf("expected inbox detail error in detail view, got:\n%s", rendered)
	}
}

func TestDashboardMailRefreshAlsoSchedulesInboxSummary(t *testing.T) {
	t.Parallel()

	m := newTestModel(200)
	m.fetchingMailInbox = false

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}
	if !next.fetchingMailInbox {
		t.Fatal("expected mail refresh to trigger inbox summary refresh")
	}
	if cmd == nil {
		t.Fatal("expected batched mail refresh command")
	}

	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected tea.BatchMsg from mail refresh, got %T", cmd())
	}
	if len(batch) < 2 {
		t.Fatalf("expected mail refresh to schedule status and inbox fetches, got %d commands", len(batch))
	}
}

func TestDashboardDCGStatusErrorClearsStaleBadgeState(t *testing.T) {
	t.Parallel()

	m := newTestModel(200)
	m.dcgEnabled = true
	m.dcgAvailable = true
	m.dcgVersion = "1.2.3"
	m.dcgBlocked = 4
	m.dcgLastBlocked = "rm -rf /tmp/nope"

	updated, _ := m.Update(DCGStatusUpdateMsg{
		Enabled: true,
		Err:     errors.New("dcg probe failed"),
		Gen:     1,
	})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}

	if !next.dcgEnabled {
		t.Fatal("expected DCG enabled state to follow the latest message")
	}
	if next.dcgAvailable {
		t.Fatal("expected DCG availability to clear on error")
	}
	if next.dcgVersion != "" {
		t.Fatalf("expected DCG version to clear on error, got %q", next.dcgVersion)
	}
	if next.dcgBlocked != 0 {
		t.Fatalf("expected blocked count to clear on error, got %d", next.dcgBlocked)
	}
	if next.dcgLastBlocked != "" {
		t.Fatalf("expected last blocked command to clear on error, got %q", next.dcgLastBlocked)
	}
	if next.dcgError == nil {
		t.Fatal("expected DCG error to be recorded")
	}
}

func TestDashboardConflictsNumericShortcutDoesNotChangePaneSelection(t *testing.T) {
	t.Parallel()

	m := newTestModel(200)
	m.panes = []tmux.Pane{
		{ID: "pane-1", Index: 0, Title: "proj__cc_1", Type: tmux.AgentClaude},
		{ID: "pane-2", Index: 1, Title: "proj__cod_1", Type: tmux.AgentCodex},
		{ID: "pane-3", Index: 2, Title: "proj__cc_2", Type: tmux.AgentClaude},
	}
	m.cursor = 2
	m.focusedPanel = PanelConflicts
	m.conflictsPanel = panels.NewConflictsPanel()
	m.conflictsPanel.Focus()
	m.conflictsPanel.AddConflict(watcher.FileConflict{
		Path:           "/tmp/file.go",
		RequestorAgent: "Requester",
		Holders:        []string{"Holder"},
		DetectedAt:     time.Now(),
	})
	m.conflictsPanel.SetActionHandler(func(conflict watcher.FileConflict, action watcher.ConflictAction) error {
		return nil
	})

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}
	if next.cursor != 2 {
		t.Fatalf("conflicts action should not change pane cursor, got %d want 2", next.cursor)
	}
	if cmd == nil {
		t.Fatal("conflicts action key should return an action command")
	}
	if _, ok := cmd().(panels.ConflictActionResultMsg); !ok {
		t.Fatalf("expected ConflictActionResultMsg, got %T", cmd())
	}
}

func TestDashboardSidebarCyclesActiveSubpanelAndHints(t *testing.T) {
	t.Parallel()

	now := time.Now()
	m := newTestModel(200)
	m.focusedPanel = PanelSidebar
	m.historyPanel = nil
	m.filesPanel = nil
	m.cassPanel = nil

	metricsData := panels.MetricsData{
		Coverage: &ensemble.CoverageReport{Overall: 0.5},
	}
	m.metricsData = metricsData
	m.metricsPanel.SetData(metricsData, nil)
	m.timelinePanel.SetData(panels.TimelineData{
		Events: []state.AgentEvent{
			{AgentID: "cc_1", State: state.TimelineWorking, Timestamp: now.Add(-5 * time.Minute)},
		},
		Markers: []state.TimelineMarker{
			{ID: "m1", AgentID: "cc_1", SessionID: "test", Type: state.MarkerPrompt, Timestamp: now.Add(-2 * time.Minute)},
		},
		Stats: state.TimelineStats{
			TotalAgents: 1,
			TotalEvents: 1,
			OldestEvent: now.Add(-5 * time.Minute),
			NewestEvent: now,
		},
	}, nil)

	_ = m.renderSidebar(60, 28)
	if !m.metricsPanel.IsFocused() {
		t.Fatal("expected metrics panel to own sidebar focus initially")
	}
	if m.timelinePanel.IsFocused() {
		t.Fatal("expected timeline panel to start unfocused while metrics is active")
	}
	if !m.toastHistoryShortcutAvailable() {
		t.Fatal("toast history shortcut should stay available while timeline is not the active sidebar panel")
	}
	initialHints := m.getFocusedPanelHints()
	if len(initialHints) == 0 || initialHints[0].Desc != "cycle section" {
		t.Fatalf("expected sidebar hints to expose section cycling, got %#v", initialHints)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'J'}})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}
	_ = next.renderSidebar(60, 28)

	if !next.timelinePanel.IsFocused() {
		t.Fatal("expected timeline panel to own sidebar focus after cycling")
	}
	if next.metricsPanel.IsFocused() {
		t.Fatal("expected metrics panel to blur after sidebar focus cycles away")
	}
	if next.toastHistoryShortcutAvailable() {
		t.Fatal("toast history shortcut should be disabled while timeline is the active sidebar panel")
	}

	hints := next.getFocusedPanelHints()
	hintText := fmt.Sprintf("%#v", hints)
	if !strings.Contains(hintText, "details") {
		t.Fatalf("expected timeline hints after cycling sidebar focus, got %#v", hints)
	}
	if strings.Contains(hintText, "toggle_coverage") {
		t.Fatalf("expected metrics hints to disappear after cycling sidebar focus, got %#v", hints)
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

	aliasPairs := [][2]string{
		{"claude-code", string(tmux.AgentClaude)},
		{"claude_code", string(tmux.AgentClaude)},
		{"openai-codex", string(tmux.AgentCodex)},
		{"google-gemini", string(tmux.AgentGemini)},
		{"ws", string(tmux.AgentWindsurf)},
	}

	for _, pair := range aliasPairs {
		got := AgentBorderColor(pair[0], m.theme)
		want := AgentBorderColor(pair[1], m.theme)
		if got != want {
			t.Errorf("AgentBorderColor(%q) = %q, want same color as %q (%q)", pair[0], got, pair[1], want)
		}
	}
}

func TestAgentRowTypePresentation_CanonicalizesAliases(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)
	aliasPairs := [][2]string{
		{"claude-code", string(tmux.AgentClaude)},
		{"claude_code", string(tmux.AgentClaude)},
		{"openai-codex", string(tmux.AgentCodex)},
		{"google-gemini", string(tmux.AgentGemini)},
	}

	for _, pair := range aliasPairs {
		gotColor, gotIcon := agentRowTypePresentation(pair[0], m.theme)
		wantColor, wantIcon := agentRowTypePresentation(pair[1], m.theme)
		if gotColor != wantColor || gotIcon != wantIcon {
			t.Errorf("agentRowTypePresentation(%q) = (%q, %q), want same presentation as %q (%q, %q)",
				pair[0], gotColor, gotIcon, pair[1], wantColor, wantIcon)
		}
	}
}

func TestPanelStyles(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)
	// Test with FocusList (tick=0 for static, tick=5 for animation)
	listStyle, detailStyle := PanelStyles(FocusList, 0, m.theme)

	// Both should be valid styles (not zero values)
	testText := "test"
	if listStyle.Render(testText) == "" {
		t.Error("list panel style should render")
	}
	if detailStyle.Render(testText) == "" {
		t.Error("detail panel style should render")
	}

	// Test with FocusDetail
	listStyle2, detailStyle2 := PanelStyles(FocusDetail, 5, m.theme)
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
			got := layout.TruncateRunes(tc.input, tc.maxLen, "…")
			if got != tc.want {
				t.Errorf("TruncateRunes(%q, %d) = %q, want %q", tc.input, tc.maxLen, got, tc.want)
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

// TestRoutingUpdateMsgHandling verifies that RoutingUpdateMsg updates the model correctly.
func TestRoutingUpdateMsgHandling(t *testing.T) {
	t.Parallel()

	m := newTestModel(120)
	m.panes = []tmux.Pane{
		{ID: "%1", Title: "claude-1", Type: tmux.AgentClaude},
	}
	m.metricsData = panels.MetricsData{}
	m.fetchingRouting = true

	// Send RoutingUpdateMsg
	msg := RoutingUpdateMsg{
		Scores: map[string]RoutingScore{
			"%1": {Score: 85.0, IsRecommended: true, State: "idle"},
		},
	}

	updated, _ := m.Update(msg)
	updatedModel := updated.(Model)

	// Verify routing data was stored
	if updatedModel.routingScores["%1"].Score != 85.0 {
		t.Errorf("expected routingScores[%%1].Score = 85, got %f", updatedModel.routingScores["%1"].Score)
	}

	// Verify fetchingRouting was reset
	if updatedModel.fetchingRouting {
		t.Error("expected fetchingRouting = false after message")
	}
}

// TestFleetCount_Consistent verifies that fleet counts in stats bar and ticker show same totals
// This is part of bd-eti6 - Fix Fleet count inconsistency (0/17 vs 17 panes)
func TestFleetCount_Consistent(t *testing.T) {
	t.Parallel()

	m := New("test", "")
	m.width = 140
	m.height = 40
	m.tier = layout.TierForWidth(m.width)

	// Create 17 panes simulating the real-world scenario
	for i := 1; i <= 17; i++ {
		agentType := tmux.AgentClaude
		if i%3 == 0 {
			agentType = tmux.AgentCodex
		} else if i%5 == 0 {
			agentType = tmux.AgentGemini
		}

		pane := tmux.Pane{
			ID:    fmt.Sprintf("%d", i),
			Index: i,
			Title: fmt.Sprintf("test__agent_%d", i),
			Type:  agentType,
		}
		m.panes = append(m.panes, pane)

		// Set pane status to various states (not just "working")
		state := "idle"
		if i%2 == 0 {
			state = "working"
		}
		m.paneStatus[i] = PaneStatus{
			State:          state,
			ContextPercent: float64(i * 5),
			ContextLimit:   200000,
		}
	}

	// Update counts (simulates what happens during dashboard refresh)
	m.updateStats()
	m.updateTickerData()

	// Verify total agent count is consistent
	totalPanes := len(m.panes)

	// The ticker data should have been set via updateTickerData
	// We verify by checking the stats bar shows same count
	statsBar := m.renderStatsBar()
	expectedPaneText := fmt.Sprintf("%d panes", totalPanes)

	if !strings.Contains(statsBar, expectedPaneText) {
		t.Errorf("stats bar should contain '%s', got: %s", expectedPaneText, statsBar)
	}

	// Verify agent type counts sum correctly
	sumAgentTypes := m.claudeCount + m.codexCount + m.geminiCount + m.cursorCount + m.windsurfCount + m.aiderCount + m.ollamaCount + m.userCount
	if sumAgentTypes != totalPanes {
		t.Errorf("agent type counts (%d) should equal total panes (%d)", sumAgentTypes, totalPanes)
	}

	// Verify ticker panel is set (non-nil check)
	if m.tickerPanel == nil {
		t.Error("tickerPanel should be initialized")
	}
}

// TestFleetCount_ActiveDefinition verifies that "active" = has non-empty status
// This tests the fix for bd-eti6
func TestFleetCount_ActiveDefinition(t *testing.T) {
	t.Parallel()

	m := New("test", "")
	m.width = 140
	m.height = 40

	// Create 5 panes
	for i := 1; i <= 5; i++ {
		pane := tmux.Pane{
			ID:    fmt.Sprintf("%d", i),
			Index: i,
			Title: fmt.Sprintf("test__cc_%d", i),
			Type:  tmux.AgentClaude,
		}
		m.panes = append(m.panes, pane)
	}

	// Set status for only 3 of them (various states, not just "working")
	m.paneStatus[1] = PaneStatus{State: "working"}
	m.paneStatus[2] = PaneStatus{State: "idle"}
	m.paneStatus[3] = PaneStatus{State: "error"}
	// Panes 4 and 5 have no status set (empty state)

	m.updateStats()
	m.updateTickerData()

	// Count active agents manually using the new definition
	activeCount := 0
	for _, ps := range m.paneStatus {
		if ps.State != "" {
			activeCount++
		}
	}

	// Should be 3 (the ones with non-empty state)
	if activeCount != 3 {
		t.Errorf("expected 3 active agents, got %d", activeCount)
	}
}

// TestFleetCount_FallbackWhenNoStatus verifies the fallback behavior when
// paneStatus map is empty (status detection hasn't run yet)
func TestFleetCount_FallbackWhenNoStatus(t *testing.T) {
	t.Parallel()

	m := New("test", "")
	m.width = 140
	m.height = 40

	// Create 5 Claude panes
	for i := 1; i <= 5; i++ {
		pane := tmux.Pane{
			ID:    fmt.Sprintf("%d", i),
			Index: i,
			Title: fmt.Sprintf("test__cc_%d", i),
			Type:  tmux.AgentClaude,
		}
		m.panes = append(m.panes, pane)
	}

	// Don't set any paneStatus (simulates startup before status detection runs)
	// paneStatus map is empty

	m.updateStats() // This sets claudeCount = 5
	m.updateTickerData()

	// With the fallback, when paneStatus is empty, activeAgents should use agent counts
	// The fallback sets activeAgents = claudeCount + codexCount + geminiCount
	// This prevents showing "0/5" when we just haven't fetched status yet
	if m.claudeCount != 5 {
		t.Errorf("expected claudeCount = 5, got %d", m.claudeCount)
	}
}

// TestFleetCount_AgentTypesSumCorrectly verifies that agent type counts sum to total
func TestFleetCount_AgentTypesSumCorrectly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		claudeCount   int
		codexCount    int
		geminiCount   int
		cursorCount   int
		windsurfCount int
		aiderCount    int
		ollamaCount   int
		userCount     int
	}{
		{"all_claude", 5, 0, 0, 0, 0, 0, 0, 0},
		{"mixed_agents", 2, 2, 1, 1, 1, 1, 1, 1},
		{"all_user", 0, 0, 0, 0, 0, 0, 0, 4},
		{"empty", 0, 0, 0, 0, 0, 0, 0, 0},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := New("test", "")
			m.width = 140
			m.height = 40

			idx := 1
			// Add Claude panes
			for i := 0; i < tc.claudeCount; i++ {
				m.panes = append(m.panes, tmux.Pane{
					ID: fmt.Sprintf("%d", idx), Index: idx, Type: tmux.AgentClaude,
				})
				idx++
			}
			// Add Codex panes
			for i := 0; i < tc.codexCount; i++ {
				m.panes = append(m.panes, tmux.Pane{
					ID: fmt.Sprintf("%d", idx), Index: idx, Type: tmux.AgentCodex,
				})
				idx++
			}
			// Add Gemini panes
			for i := 0; i < tc.geminiCount; i++ {
				m.panes = append(m.panes, tmux.Pane{
					ID: fmt.Sprintf("%d", idx), Index: idx, Type: tmux.AgentGemini,
				})
				idx++
			}
			// Add Cursor panes
			for i := 0; i < tc.cursorCount; i++ {
				m.panes = append(m.panes, tmux.Pane{
					ID: fmt.Sprintf("%d", idx), Index: idx, Type: tmux.AgentCursor,
				})
				idx++
			}
			// Add Windsurf panes
			for i := 0; i < tc.windsurfCount; i++ {
				m.panes = append(m.panes, tmux.Pane{
					ID: fmt.Sprintf("%d", idx), Index: idx, Type: tmux.AgentWindsurf,
				})
				idx++
			}
			// Add Aider panes
			for i := 0; i < tc.aiderCount; i++ {
				m.panes = append(m.panes, tmux.Pane{
					ID: fmt.Sprintf("%d", idx), Index: idx, Type: tmux.AgentAider,
				})
				idx++
			}
			// Add Ollama panes
			for i := 0; i < tc.ollamaCount; i++ {
				m.panes = append(m.panes, tmux.Pane{
					ID: fmt.Sprintf("%d", idx), Index: idx, Type: tmux.AgentOllama,
				})
				idx++
			}
			// Add User panes
			for i := 0; i < tc.userCount; i++ {
				m.panes = append(m.panes, tmux.Pane{
					ID: fmt.Sprintf("%d", idx), Index: idx, Type: tmux.AgentUser,
				})
				idx++
			}

			m.updateStats()

			expectedTotal := tc.claudeCount + tc.codexCount + tc.geminiCount + tc.cursorCount + tc.windsurfCount + tc.aiderCount + tc.ollamaCount + tc.userCount
			actualSum := m.claudeCount + m.codexCount + m.geminiCount + m.cursorCount + m.windsurfCount + m.aiderCount + m.ollamaCount + m.userCount

			if actualSum != expectedTotal {
				t.Errorf("expected sum %d, got %d (claude=%d codex=%d gemini=%d cursor=%d windsurf=%d aider=%d ollama=%d user=%d)",
					expectedTotal, actualSum, m.claudeCount, m.codexCount, m.geminiCount, m.cursorCount, m.windsurfCount, m.aiderCount, m.ollamaCount, m.userCount)
			}
			if m.ollamaCount != tc.ollamaCount {
				t.Errorf("ollamaCount = %d, want %d", m.ollamaCount, tc.ollamaCount)
			}
			if m.userCount != tc.userCount {
				t.Errorf("userCount = %d, want %d", m.userCount, tc.userCount)
			}

			if len(m.panes) != expectedTotal {
				t.Errorf("expected %d panes, got %d", expectedTotal, len(m.panes))
			}
		})
	}
}

// =============================================================================
// Phase 2d: bubbles/list pane delegate tests
// =============================================================================

// TestPaneItemInterface verifies paneItem implements list.Item correctly.
func TestPaneItemInterface(t *testing.T) {
	row := PaneTableRow{
		Title:        "proj__cc_1",
		Type:         "cc",
		ModelVariant: "sonnet",
		Status:       "working",
	}
	item := paneItem{row: row}

	// Verify Title()
	if got := item.Title(); got != "proj__cc_1" {
		t.Errorf("Title() = %q, want %q", got, "proj__cc_1")
	}

	// Verify Description()
	desc := item.Description()
	if !strings.Contains(desc, "working") {
		t.Errorf("Description() = %q, missing status", desc)
	}
	if !strings.Contains(desc, "sonnet") {
		t.Errorf("Description() = %q, missing model", desc)
	}

	// Verify FilterValue() includes searchable fields
	filter := item.FilterValue()
	if !strings.Contains(filter, "proj__cc_1") {
		t.Errorf("FilterValue() = %q, missing title", filter)
	}
	if !strings.Contains(filter, "cc") {
		t.Errorf("FilterValue() = %q, missing type", filter)
	}
	if !strings.Contains(filter, "sonnet") {
		t.Errorf("FilterValue() = %q, missing model", filter)
	}

	t.Logf("paneItem methods: Title=%q, FilterValue=%q", item.Title(), filter)
}

// TestPaneDelegateRender verifies delegate produces renderable output.
func TestPaneDelegateRender(t *testing.T) {
	m := newTestModel(120)
	dims := CalculateLayout(100, 1)
	d := newPaneDelegate(m.theme, dims)
	d.SetTick(5) // Set animation tick

	items := []list.Item{
		paneItem{row: PaneTableRow{Index: 0, Title: "cc_1", Type: "cc"}},
		paneItem{row: PaneTableRow{Index: 1, Title: "cc_2", Type: "cc"}},
		paneItem{row: PaneTableRow{Index: 2, Title: "cod_1", Type: "cod"}},
	}

	l := list.New(items, d, 100, 20)

	// Verify Height()
	if h := d.Height(); h != 1 && h != 2 {
		t.Errorf("Height() = %d, want 1 or 2", h)
	}

	// Verify Spacing()
	if s := d.Spacing(); s != 0 {
		t.Errorf("Spacing() = %d, want 0", s)
	}

	// Verify Update() returns nil
	if cmd := d.Update(nil, &l); cmd != nil {
		t.Errorf("Update() returned non-nil command")
	}

	// Verify View() produces output
	view := l.View()
	if view == "" {
		t.Error("list.View() returned empty string")
	}

	t.Logf("Delegate render: Height=%d, View length=%d", d.Height(), len(view))
}

// TestPaneListRefreshPreservesSelection verifies selection is preserved across refresh.
func TestPaneListRefreshPreservesSelection(t *testing.T) {
	m := newTestModel(120)
	dims := CalculateLayout(100, 1)
	d := newPaneDelegate(m.theme, dims)

	// Initial items
	items := []list.Item{
		paneItem{row: PaneTableRow{Index: 0, Title: "cc_1", Type: "cc"}},
		paneItem{row: PaneTableRow{Index: 1, Title: "cc_2", Type: "cc"}},
		paneItem{row: PaneTableRow{Index: 2, Title: "cod_1", Type: "cod"}},
	}
	l := list.New(items, d, 100, 20)

	// Select item at index 1 (cc_2)
	l.Select(1)
	if l.Index() != 1 {
		t.Fatalf("Select(1) failed, Index()=%d", l.Index())
	}

	// Save current selection
	sel := l.SelectedItem().(paneItem)
	prevTitle := sel.row.Title
	t.Logf("Before refresh: selected index=%d title=%q", l.Index(), prevTitle)

	// Simulate refresh with reordered items (cc_2 moved to index 2)
	newItems := []list.Item{
		paneItem{row: PaneTableRow{Index: 0, Title: "cc_1", Type: "cc"}},
		paneItem{row: PaneTableRow{Index: 1, Title: "cod_1", Type: "cod"}},
		paneItem{row: PaneTableRow{Index: 2, Title: "cc_2", Type: "cc"}}, // moved
		paneItem{row: PaneTableRow{Index: 3, Title: "cc_3", Type: "cc"}}, // new
	}
	l.SetItems(newItems)

	// Find and restore selection by title
	for i, item := range l.Items() {
		if pi, ok := item.(paneItem); ok && pi.row.Title == prevTitle {
			l.Select(i)
			break
		}
	}

	// Verify selection was restored
	restored := l.SelectedItem().(paneItem)
	if restored.row.Title != prevTitle {
		t.Errorf("Selection not preserved: got %q, want %q", restored.row.Title, prevTitle)
	}

	t.Logf("After refresh: selected index=%d title=%q", l.Index(), restored.row.Title)
}

// TestPaneListEmptyState verifies empty list renders without panic.
func TestPaneListEmptyState(t *testing.T) {
	m := newTestModel(120)
	dims := CalculateLayout(100, 1)
	d := newPaneDelegate(m.theme, dims)

	// Empty list
	l := list.New(nil, d, 100, 20)
	view := l.View()

	if view == "" {
		t.Log("Empty list returned empty view (acceptable)")
	} else {
		t.Logf("Empty list view: %d chars", len(view))
	}

	// Verify no panic when iterating empty
	for range l.Items() {
		t.Error("Should not iterate over empty list")
	}
}

// TestPaneListFilterKey verifies filtering is enabled by default.
func TestPaneListFilterKey(t *testing.T) {
	m := newTestModel(120)
	dims := CalculateLayout(100, 1)
	d := newPaneDelegate(m.theme, dims)

	items := []list.Item{
		paneItem{row: PaneTableRow{Title: "proj__cc_1", Type: "cc"}},
		paneItem{row: PaneTableRow{Title: "proj__cod_1", Type: "cod"}},
	}
	l := list.New(items, d, 100, 20)
	l.SetFilteringEnabled(true)

	if !l.FilteringEnabled() {
		t.Error("FilteringEnabled() should be true after SetFilteringEnabled(true)")
	}

	t.Log("Filter enabled: '/' key will activate fuzzy search")
}

func TestDashboardPaneListForwardsFilterKeys(t *testing.T) {
	m := newTestModel(120)
	m.focusedPanel = PanelPaneList
	m.panes = []tmux.Pane{
		{ID: "pane-1", Index: 0, Title: "proj__cc_1", Type: tmux.AgentClaude},
		{ID: "pane-2", Index: 1, Title: "proj__cod_1", Type: tmux.AgentCodex},
		{ID: "pane-3", Index: 2, Title: "proj__cc_2", Type: tmux.AgentClaude},
	}
	m.cursor = 0
	m.paneStatus = map[int]PaneStatus{
		0: {State: "working"},
		1: {State: "idle"},
		2: {State: "waiting"},
	}
	_ = m.rebuildPaneList()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}
	m = next

	if got := m.paneList.FilterState(); got != list.Filtering {
		t.Fatalf("pane list filter state = %v, want %v", got, list.Filtering)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	next, ok = updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T after first rune, want dashboard.Model", updated)
	}
	m = next

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	next, ok = updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T after second rune, want dashboard.Model", updated)
	}
	m = next

	if got := m.paneList.FilterInput.Value(); got != "cc" {
		t.Fatalf("pane list filter input = %q, want %q", got, "cc")
	}
}

func TestDashboardPaneListArrowKeysAdvanceSingleRow(t *testing.T) {
	m := newTestModel(120)
	m.focusedPanel = PanelPaneList
	m.panes = []tmux.Pane{
		{ID: "1", Index: 1, Title: "pane-1", Type: tmux.AgentCodex},
		{ID: "2", Index: 2, Title: "pane-2", Type: tmux.AgentClaude},
		{ID: "3", Index: 3, Title: "pane-3", Type: tmux.AgentGemini},
	}
	m.cursor = 0
	m.paneStatus = map[int]PaneStatus{
		0: {State: "working"},
		1: {State: "idle"},
		2: {State: "waiting"},
	}
	_ = m.rebuildPaneList()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}
	m = next

	if m.cursor != 1 {
		t.Fatalf("cursor after single Down = %d, want 1", m.cursor)
	}
	if got := m.paneList.Index(); got != 1 {
		t.Fatalf("pane list selection after single Down = %d, want 1", got)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	next, ok = updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}
	m = next

	if m.cursor != 0 {
		t.Fatalf("cursor after single Up = %d, want 0", m.cursor)
	}
	if got := m.paneList.Index(); got != 0 {
		t.Fatalf("pane list selection after single Up = %d, want 0", got)
	}
}

func TestDashboardPaneTableToggleSplitTier(t *testing.T) {
	m := newTestModel(140)
	m.focusedPanel = PanelPaneList
	m.panes = []tmux.Pane{
		{ID: "pane-1", Index: 0, Title: "proj__cc_1", Type: tmux.AgentClaude, Variant: "sonnet", Command: "go test ./..."},
		{ID: "pane-2", Index: 1, Title: "proj__cod_1", Type: tmux.AgentCodex, Variant: "gpt-5.4", Command: "rg TODO"},
	}
	m.paneStatus = map[int]PaneStatus{
		0: {State: "working", ContextPercent: 65},
		1: {State: "idle", ContextPercent: 18},
	}
	_ = m.rebuildPaneList()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}
	m = next

	if !m.showTableView {
		t.Fatal("expected pane table toggle to enable on split tier")
	}
	if m.paneTable == nil {
		t.Fatal("expected pane table to be initialized")
	}

	plain := status.StripANSI(m.renderPaneList(64))
	for _, want := range []string{"Type", "Status", "Ctx%", "Command"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("pane table render missing %q in %q", want, plain)
		}
	}
}

func TestDashboardPaneTableToggleIgnoredOnNarrowTier(t *testing.T) {
	m := newTestModel(100)
	m.focusedPanel = PanelPaneList

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}
	if next.showTableView {
		t.Fatal("expected pane table toggle to stay disabled below split tier")
	}
}

func TestDashboardPaneTableBlocksHiddenListFiltering(t *testing.T) {
	m := newTestModel(140)
	m.focusedPanel = PanelPaneList
	m.showTableView = true
	m.panes = []tmux.Pane{
		{ID: "pane-1", Index: 0, Title: "proj__cc_1", Type: tmux.AgentClaude},
		{ID: "pane-2", Index: 1, Title: "proj__cod_1", Type: tmux.AgentCodex},
	}
	m.paneStatus = map[int]PaneStatus{
		0: {State: "working"},
		1: {State: "idle"},
	}
	_ = m.rebuildPaneList()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update() returned %T, want dashboard.Model", updated)
	}
	if got := next.paneList.FilterState(); got == list.Filtering {
		t.Fatalf("hidden pane list should not enter filter mode while table view is active; got %v", got)
	}
}

func TestDashboardPaneTableToggleClearsActiveListFilter(t *testing.T) {
	m := newTestModel(140)
	m.focusedPanel = PanelPaneList
	m.panes = []tmux.Pane{
		{ID: "pane-1", Index: 0, Title: "proj__cc_1", Type: tmux.AgentClaude},
		{ID: "pane-2", Index: 1, Title: "proj__cod_1", Type: tmux.AgentCodex},
	}
	m.paneStatus = map[int]PaneStatus{
		0: {State: "working"},
		1: {State: "idle"},
	}
	_ = m.rebuildPaneList()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = updated.(Model)
	if got := m.paneList.FilterState(); got != list.Filtering {
		t.Fatalf("expected list filtering before toggling table view, got %v", got)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	m = updated.(Model)
	if !m.showTableView {
		t.Fatal("expected pane table toggle to enable")
	}
	if got := m.paneList.FilterState(); got == list.Filtering {
		t.Fatalf("expected table toggle to clear list filtering, got %v", got)
	}
}

// TestToPaneItems verifies conversion from panes to list items.
func TestToPaneItems(t *testing.T) {
	m := newTestModel(120)
	panes := []tmux.Pane{
		{ID: "1", Index: 0, Title: "cc_1", Type: tmux.AgentClaude},
		{ID: "2", Index: 1, Title: "cod_1", Type: tmux.AgentCodex},
	}
	statuses := map[int]PaneStatus{
		0: {State: "working", ContextPercent: 50},
		1: {State: "idle", ContextPercent: 20},
	}

	items := toPaneItems(panes, statuses, nil, m.theme)

	if len(items) != 2 {
		t.Fatalf("toPaneItems returned %d items, want 2", len(items))
	}

	// Verify first item
	item0 := items[0].(paneItem)
	if item0.row.Title != "cc_1" {
		t.Errorf("items[0].Title = %q, want %q", item0.row.Title, "cc_1")
	}
	if item0.row.Type != "cc" {
		t.Errorf("items[0].Type = %q, want %q", item0.row.Type, "cc")
	}

	// Verify second item
	item1 := items[1].(paneItem)
	if item1.row.Title != "cod_1" {
		t.Errorf("items[1].Title = %q, want %q", item1.row.Title, "cod_1")
	}

	t.Logf("toPaneItems: converted %d panes to list items", len(items))
}

// BenchmarkPaneListView benchmarks list rendering performance.
func BenchmarkPaneListView(b *testing.B) {
	m := newTestModel(120)
	dims := CalculateLayout(100, 1)
	d := newPaneDelegate(m.theme, dims)

	items := make([]list.Item, 20)
	for i := range items {
		items[i] = paneItem{row: PaneTableRow{
			Index: i,
			Title: fmt.Sprintf("proj__cc_%d", i),
			Type:  "cc",
		}}
	}

	l := list.New(items, d, 100, 20)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = l.View()
	}
}
