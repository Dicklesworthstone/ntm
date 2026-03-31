package panels

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
)

func TestNewMetricsPanel(t *testing.T) {
	panel := NewMetricsPanel()
	if panel == nil {
		t.Fatal("NewMetricsPanel returned nil")
	}
}

func TestMetricsPanelConfig(t *testing.T) {
	panel := NewMetricsPanel()
	cfg := panel.Config()

	if cfg.ID != "metrics" {
		t.Errorf("expected ID 'metrics', got %q", cfg.ID)
	}
	if cfg.Title != "Metrics" {
		t.Errorf("expected Title 'Metrics', got %q", cfg.Title)
	}
	if cfg.Priority != PriorityNormal {
		t.Errorf("expected PriorityNormal, got %v", cfg.Priority)
	}
	if cfg.RefreshInterval != 10*time.Second {
		t.Errorf("expected 10s refresh, got %v", cfg.RefreshInterval)
	}
	if !cfg.Collapsible {
		t.Error("expected Collapsible to be true")
	}
}

func TestMetricsPanelSetSize(t *testing.T) {
	panel := NewMetricsPanel()
	panel.SetSize(100, 30)

	if panel.Width() != 100 {
		t.Errorf("expected width 100, got %d", panel.Width())
	}
	if panel.Height() != 30 {
		t.Errorf("expected height 30, got %d", panel.Height())
	}
}

func TestMetricsPanelFocusBlur(t *testing.T) {
	panel := NewMetricsPanel()

	panel.Focus()
	if !panel.IsFocused() {
		t.Error("expected IsFocused to be true after Focus()")
	}

	panel.Blur()
	if panel.IsFocused() {
		t.Error("expected IsFocused to be false after Blur()")
	}
}

func TestMetricsPanelSetData(t *testing.T) {
	panel := NewMetricsPanel()
	data := sampleMetricsData()

	panel.SetData(data, nil)

	if panel.data.Coverage == nil {
		t.Error("expected Coverage to be set")
	}
	if panel.data.Redundancy == nil {
		t.Error("expected Redundancy to be set")
	}
	if panel.data.Velocity == nil {
		t.Error("expected Velocity to be set")
	}
	if panel.data.Conflicts == nil {
		t.Error("expected Conflicts to be set")
	}
}

func TestMetricsPanelKeybindings(t *testing.T) {
	panel := NewMetricsPanel()
	bindings := panel.Keybindings()

	if len(bindings) == 0 {
		t.Error("expected non-empty keybindings")
	}

	actions := make(map[string]string)
	for _, b := range bindings {
		keys := b.Key.Keys()
		if len(keys) == 0 {
			t.Fatalf("binding %q should expose at least one key", b.Action)
		}
		actions[b.Action] = keys[0]
	}

	expected := map[string]string{
		"toggle_coverage":   "c",
		"toggle_redundancy": "r",
		"toggle_velocity":   "v",
		"toggle_conflicts":  "x",
	}
	for action, wantKey := range expected {
		if gotKey, ok := actions[action]; !ok {
			t.Errorf("expected action %q in keybindings", action)
		} else if gotKey != wantKey {
			t.Errorf("binding %q key = %q, want %q", action, gotKey, wantKey)
		}
	}
}

func TestMetricsPanelInit(t *testing.T) {
	panel := NewMetricsPanel()
	cmd := panel.Init()
	if cmd != nil {
		t.Error("expected Init() to return nil")
	}
}

func TestMetricsPanelUpdate_TogglesCoverage(t *testing.T) {
	panel := NewMetricsPanel()
	panel.Focus()

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}}
	panel.Update(msg)

	if !panel.expanded["coverage"] {
		t.Error("expected coverage section to be expanded after toggle")
	}
}

func TestMetricsPanelViewContainsTitle(t *testing.T) {
	panel := NewMetricsPanel()
	panel.SetSize(80, 20)

	view := panel.View()

	if !strings.Contains(view, "Metrics") {
		t.Error("expected view to contain title")
	}
}

func TestMetricsPanelViewShowsNA(t *testing.T) {
	panel := NewMetricsPanel()
	panel.SetSize(80, 20)

	panel.SetData(MetricsData{}, nil)
	view := panel.View()

	if !strings.Contains(view, "Coverage: N/A") {
		t.Error("expected view to show Coverage N/A")
	}
	if !strings.Contains(view, "Redundancy: N/A") {
		t.Error("expected view to show Redundancy N/A")
	}
	if !strings.Contains(view, "Velocity: N/A") {
		t.Error("expected view to show Velocity N/A")
	}
	if !strings.Contains(view, "Conflicts: N/A") {
		t.Error("expected view to show Conflicts N/A")
	}
}

func TestMetricsPanelViewRendersMetrics(t *testing.T) {
	panel := NewMetricsPanel()
	panel.SetSize(80, 24)

	panel.SetData(sampleMetricsData(), nil)
	view := panel.View()

	if !strings.Contains(view, "Coverage: 50%") {
		t.Error("expected view to include coverage summary")
	}
	if !strings.Contains(view, "Redundancy: 0.34") {
		t.Error("expected view to include redundancy summary")
	}
	if !strings.Contains(view, "Velocity: 2.30 findings/1k tokens") {
		t.Error("expected view to include velocity summary")
	}
	if !strings.Contains(view, "Conflicts: 3 detected") {
		t.Error("expected view to include conflict summary")
	}
}

func TestMetricsPanelViewShowsErrorState(t *testing.T) {
	panel := NewMetricsPanel()
	panel.SetSize(80, 20)

	panel.SetData(MetricsData{}, errors.New("metrics backend down"))
	view := panel.View()

	if !strings.Contains(view, "Error") {
		t.Error("expected view to include error badge")
	}
	if !strings.Contains(view, "metrics backend down") {
		t.Error("expected view to contain error message")
	}
}

func TestMetricsPanelHandlesOwnHeight(t *testing.T) {
	panel := NewMetricsPanel()
	if !panel.HandlesOwnHeight() {
		t.Fatal("expected metrics panel to manage its own height")
	}
}

func TestMetricsPanelViewShowsScrollIndicatorWhenOverflowing(t *testing.T) {
	panel := NewMetricsPanel()
	panel.SetSize(48, 10)
	panel.SetData(sampleMetricsData(), nil)
	panel.expanded["coverage"] = true
	panel.expanded["redundancy"] = true
	panel.expanded["velocity"] = true
	panel.expanded["conflicts"] = true

	view := panel.View()
	if !strings.Contains(view, "%") {
		t.Fatalf("expected overflowing metrics panel to show percent badge, got %q", view)
	}
}

func sampleMetricsData() MetricsData {
	coverage := &ensemble.CoverageReport{
		Overall: 0.5,
		PerCategory: map[ensemble.ModeCategory]ensemble.CategoryCoverage{
			ensemble.CategoryFormal: {
				Category:   ensemble.CategoryFormal,
				TotalModes: 2,
				UsedModes:  []string{"deductive"},
				Coverage:   0.5,
			},
			ensemble.CategoryAmpliative: {
				Category:   ensemble.CategoryAmpliative,
				TotalModes: 2,
				UsedModes:  []string{"inductive"},
				Coverage:   0.5,
			},
		},
	}

	redundancy := &ensemble.RedundancyAnalysis{
		OverallScore: 0.34,
		PairwiseScores: []ensemble.PairSimilarity{
			{ModeA: "F1", ModeB: "E2", Similarity: 0.23},
			{ModeA: "K1", ModeB: "H2", Similarity: 0.67},
		},
	}

	velocity := &ensemble.VelocityReport{
		Overall: 2.3,
		PerMode: []ensemble.VelocityEntry{
			{ModeID: "F1", ModeName: "F1", TokensSpent: 1000, UniqueFindings: 3, Velocity: 3.1},
			{ModeID: "K1", ModeName: "K1", TokensSpent: 1000, UniqueFindings: 1, Velocity: 1.2},
		},
		HighPerformers: []string{"F1"},
		LowPerformers:  []string{"K1"},
		Suggestions:    []string{"K1 underperforming, consider early stop"},
	}

	conflicts := &ensemble.ConflictDensity{
		TotalConflicts:      3,
		ResolvedConflicts:   1,
		UnresolvedConflicts: 2,
		HighConflictPairs:   []string{"F1 <-> E2"},
	}

	return MetricsData{
		Coverage:   coverage,
		Redundancy: redundancy,
		Velocity:   velocity,
		Conflicts:  conflicts,
	}
}
