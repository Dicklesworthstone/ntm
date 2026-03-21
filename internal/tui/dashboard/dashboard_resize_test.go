package dashboard

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
)

func TestWindowSizeMsg_UpdatesDimensionsAndPanels(t *testing.T) {
	t.Parallel()

	m := New("test", "")
	m.focusedPanel = PanelBeads

	model, _ := m.Update(tea.WindowSizeMsg{Width: 340, Height: 60})
	updated := model.(Model)

	if updated.width != 340 || updated.height != 60 {
		t.Fatalf("expected width/height 340x60, got %dx%d", updated.width, updated.height)
	}

	if updated.tier != layout.TierForWidth(340) {
		t.Fatalf("expected tier %v, got %v", layout.TierForWidth(340), updated.tier)
	}

	contentHeight := contentHeightFor(60)
	panelHeight := maxInt(contentHeight-2, 0)

	_, _, p3, p4, p5, p6 := layout.MegaProportions6(340)
	p3Inner := maxInt(p3-4, 0)
	p4Inner := maxInt(p4-4, 0)
	p5Inner := maxInt(p5-4, 0)
	p6Inner := maxInt(p6-4, 0)

	if updated.beadsPanel.Width() != p3Inner || updated.beadsPanel.Height() != panelHeight {
		t.Fatalf("beadsPanel size mismatch: got %dx%d want %dx%d",
			updated.beadsPanel.Width(), updated.beadsPanel.Height(), p3Inner, panelHeight)
	}
	if updated.alertsPanel.Width() != p4Inner || updated.alertsPanel.Height() != panelHeight {
		t.Fatalf("alertsPanel size mismatch: got %dx%d want %dx%d",
			updated.alertsPanel.Width(), updated.alertsPanel.Height(), p4Inner, panelHeight)
	}
	if updated.attentionPanel.Width() != p5Inner || updated.attentionPanel.Height() != panelHeight {
		t.Fatalf("attentionPanel size mismatch: got %dx%d want %dx%d",
			updated.attentionPanel.Width(), updated.attentionPanel.Height(), p5Inner, panelHeight)
	}
	if updated.metricsPanel.Width() != p6Inner || updated.metricsPanel.Height() != panelHeight {
		t.Fatalf("metricsPanel size mismatch: got %dx%d want %dx%d",
			updated.metricsPanel.Width(), updated.metricsPanel.Height(), p6Inner, panelHeight)
	}
	if updated.historyPanel.Width() != p6Inner || updated.historyPanel.Height() != panelHeight {
		t.Fatalf("historyPanel size mismatch: got %dx%d want %dx%d",
			updated.historyPanel.Width(), updated.historyPanel.Height(), p6Inner, panelHeight)
	}
	if updated.filesPanel.Width() != p6Inner || updated.filesPanel.Height() != panelHeight {
		t.Fatalf("filesPanel size mismatch: got %dx%d want %dx%d",
			updated.filesPanel.Width(), updated.filesPanel.Height(), p6Inner, panelHeight)
	}
	if updated.cassPanel.Width() != p6Inner || updated.cassPanel.Height() != panelHeight {
		t.Fatalf("cassPanel size mismatch: got %dx%d want %dx%d",
			updated.cassPanel.Width(), updated.cassPanel.Height(), p6Inner, panelHeight)
	}
	if updated.spawnPanel.Width() != p6Inner || updated.spawnPanel.Height() != panelHeight {
		t.Fatalf("spawnPanel size mismatch: got %dx%d want %dx%d",
			updated.spawnPanel.Width(), updated.spawnPanel.Height(), p6Inner, panelHeight)
	}

	if updated.focusedPanel != PanelBeads {
		t.Fatalf("expected focus to remain on PanelBeads, got %v", updated.focusedPanel)
	}
}

func TestWindowSizeMsg_NormalizesFocusWhenPanelHidden(t *testing.T) {
	t.Parallel()

	m := New("test", "")
	m.focusedPanel = PanelBeads

	model, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	updated := model.(Model)

	if updated.focusedPanel != PanelPaneList {
		t.Fatalf("expected focus to normalize to PanelPaneList, got %v", updated.focusedPanel)
	}

	if updated.metricsPanel.Width() != 0 || updated.metricsPanel.Height() != 0 {
		t.Fatalf("expected sidebar panels to be cleared in split view, got %dx%d",
			updated.metricsPanel.Width(), updated.metricsPanel.Height())
	}
}

func TestWindowSizeMsg_MinimumSize(t *testing.T) {
	t.Parallel()

	m := New("test", "")

	// Test standard minimum terminal size 80x24
	model, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	updated := model.(Model)

	if updated.width != 80 || updated.height != 24 {
		t.Fatalf("expected width/height 80x24, got %dx%d", updated.width, updated.height)
	}

	// Should be TierNarrow at 80 columns (< 120)
	if updated.tier != layout.TierNarrow {
		t.Fatalf("expected TierNarrow at 80 width, got %v", updated.tier)
	}

	// Content height should clamp to minimum of 5 when terminal is small
	// contentHeightFor(24) = max(24-14, 5) = max(10, 5) = 10
	expectedContentHeight := contentHeightFor(24)
	if expectedContentHeight < 5 {
		t.Fatalf("expected content height >= 5, got %d", expectedContentHeight)
	}

	// Panels should be cleared in narrow mode (not mega/ultra)
	if updated.metricsPanel.Width() != 0 || updated.metricsPanel.Height() != 0 {
		t.Fatalf("expected sidebar panels cleared in narrow mode, got metrics %dx%d",
			updated.metricsPanel.Width(), updated.metricsPanel.Height())
	}

	// Test even smaller size (extreme case)
	model2, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	updated2 := model2.(Model)

	if updated2.width != 40 || updated2.height != 10 {
		t.Fatalf("expected width/height 40x10, got %dx%d", updated2.width, updated2.height)
	}

	// Content height should clamp to 5 minimum when height is very small
	// contentHeightFor(10) = max(10-14, 5) = max(-4, 5) = 5
	extremeContentHeight := contentHeightFor(10)
	if extremeContentHeight != 5 {
		t.Fatalf("expected content height clamped to 5 for height=10, got %d", extremeContentHeight)
	}
}

func TestWindowSizeMsg_TierTransition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		width        int
		expectedTier layout.Tier
		name         string
	}{
		// Use values that are past the hysteresis margin (5 columns) to ensure clean transitions
		// Since dashboard uses TierForWidthWithHysteresis, exact threshold values may stay in
		// the previous tier due to hysteresis. Use values HysteresisMargin past thresholds.
		{80, layout.TierNarrow, "narrow at 80"},
		{114, layout.TierNarrow, "narrow at 114 (before split threshold - hysteresis)"},
		{125, layout.TierSplit, "split at 125 (past hysteresis threshold)"},
		{194, layout.TierSplit, "split at 194"},
		{205, layout.TierWide, "wide at 205 (past hysteresis threshold)"},
		{234, layout.TierWide, "wide at 234"},
		{245, layout.TierUltra, "ultra at 245 (past hysteresis threshold)"},
		{314, layout.TierUltra, "ultra at 314"},
		{325, layout.TierMega, "mega at 325 (past hysteresis threshold)"},
		{400, layout.TierMega, "mega at 400"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := New("test", "")
			model, _ := m.Update(tea.WindowSizeMsg{Width: tc.width, Height: 40})
			updated := model.(Model)

			if updated.tier != tc.expectedTier {
				t.Errorf("width %d: expected tier %v, got %v", tc.width, tc.expectedTier, updated.tier)
			}

			// Verify tier matches layout package calculation
			layoutTier := layout.TierForWidth(tc.width)
			if updated.tier != layoutTier {
				t.Errorf("width %d: dashboard tier %v doesn't match layout.TierForWidth %v",
					tc.width, updated.tier, layoutTier)
			}
		})
	}
}

func TestWindowSizeMsg_ContentHeightCalculation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		height         int
		expectedHeight int
		name           string
	}{
		{60, 46, "standard height 60 -> 46"},
		{40, 26, "medium height 40 -> 26"},
		{24, 10, "minimum standard 24 -> 10"},
		{19, 5, "small height 19 -> 5 (at threshold)"},
		{15, 5, "tiny height 15 -> 5 (clamped)"},
		{10, 5, "extreme small 10 -> 5 (clamped)"},
		{5, 5, "very extreme 5 -> 5 (clamped)"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := contentHeightFor(tc.height)
			if result != tc.expectedHeight {
				t.Errorf("contentHeightFor(%d): expected %d, got %d",
					tc.height, tc.expectedHeight, result)
			}
		})
	}

	// Additional test: verify panel heights use content height correctly
	m := New("test", "")
	model, _ := m.Update(tea.WindowSizeMsg{Width: 340, Height: 60})
	updated := model.(Model)

	contentHeight := contentHeightFor(60)
	panelHeight := maxInt(contentHeight-2, 0)

	// In mega mode, panels should use calculated panel height
	if updated.beadsPanel.Height() != panelHeight {
		t.Errorf("beadsPanel height mismatch: expected %d, got %d",
			panelHeight, updated.beadsPanel.Height())
	}
}
