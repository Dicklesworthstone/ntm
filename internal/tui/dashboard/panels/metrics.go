package panels

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

const (
	coverageMiniBarWidth   = 2
	coverageDetailBarWidth = 4
	maxPairLines           = 2
	redundancyWarnLevel    = 0.7
)

// MetricsData holds observability metrics for the panel.
type MetricsData struct {
	Coverage   *ensemble.CoverageReport
	Redundancy *ensemble.RedundancyAnalysis
	Velocity   *ensemble.VelocityReport
	Conflicts  *ensemble.ConflictDensity
}

// MetricsPanel displays observability metrics for ensemble runs.
type MetricsPanel struct {
	PanelBase
	data       MetricsData
	err        error
	expanded   map[string]bool
	lastLogged MetricsData
	theme      theme.Theme
	scroll     *components.ScrollablePanel
	lastBody   string
}

// metricsConfig returns the configuration for the metrics panel.
func metricsConfig() PanelConfig {
	return PanelConfig{
		ID:              "metrics",
		Title:           "Metrics",
		Priority:        PriorityNormal,
		RefreshInterval: 10 * time.Second,
		MinWidth:        30,
		MinHeight:       8,
		Collapsible:     true,
	}
}

// NewMetricsPanel creates a new metrics panel.
func NewMetricsPanel() *MetricsPanel {
	return &MetricsPanel{
		PanelBase: NewPanelBase(metricsConfig()),
		theme:     theme.Current(),
		expanded:  map[string]bool{},
		scroll:    components.NewScrollablePanel(30, 8),
	}
}

// Init implements tea.Model.
func (m *MetricsPanel) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (m *MetricsPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if !m.IsFocused() {
		return m, nil
	}

	handled := false
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch strings.ToLower(msg.String()) {
		case "c":
			m.toggleExpanded("coverage")
			handled = true
		case "r":
			m.toggleExpanded("redundancy")
			handled = true
		case "v":
			m.toggleExpanded("velocity")
			handled = true
		case "x":
			m.toggleExpanded("conflicts")
			handled = true
		}
	}
	if handled {
		return m, nil
	}
	if m.scroll == nil {
		return m, nil
	}
	var cmd tea.Cmd
	m.scroll, cmd = m.scroll.Update(msg)
	return m, cmd
}

// SetData updates the panel data.
func (m *MetricsPanel) SetData(data MetricsData, err error) {
	changed := metricsChanged(m.lastLogged, data)
	m.data = data
	m.err = err
	if err == nil {
		m.SetLastUpdate(time.Now())
	}
	if err == nil && changed {
		m.logMetricsUpdate(data)
		m.lastLogged = data
	}
}

// HasError returns true if there's an active error.
func (m *MetricsPanel) HasError() bool {
	return m.err != nil
}

// Keybindings returns metrics panel specific shortcuts.
func (m *MetricsPanel) Keybindings() []Keybinding {
	return []Keybinding{
		{
			Key:         key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "toggle coverage")),
			Description: "Toggle coverage detail",
			Action:      "toggle_coverage",
		},
		{
			Key:         key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "toggle redundancy")),
			Description: "Toggle redundancy detail",
			Action:      "toggle_redundancy",
		},
		{
			Key:         key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "toggle velocity")),
			Description: "Toggle velocity detail",
			Action:      "toggle_velocity",
		},
		{
			Key:         key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "toggle conflicts")),
			Description: "Toggle conflict detail",
			Action:      "toggle_conflicts",
		},
	}
}

// HandlesOwnHeight returns true because metrics content is viewport-managed.
func (m *MetricsPanel) HandlesOwnHeight() bool {
	return true
}

// View renders the panel.
func (m *MetricsPanel) View() string {
	t := m.theme
	w, h := m.Width(), m.Height()

	borderColor := t.Surface1
	bgColor := t.Base
	if m.IsFocused() {
		borderColor = t.Primary
		bgColor = t.Surface0
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Background(bgColor).
		Width(w-2).
		Height(h-2).
		Padding(0, 1)

	var content strings.Builder

	title := m.Config().Title
	if m.err != nil {
		errorBadge := lipgloss.NewStyle().
			Background(t.Red).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render("Error")
		title = title + " " + errorBadge
	} else if staleBadge := components.RenderStaleBadge(m.LastUpdate(), m.Config().RefreshInterval); staleBadge != "" {
		title = title + " " + staleBadge
	}

	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(t.Lavender).
		Border(lipgloss.NormalBorder(), false, false, true, false).
		BorderForeground(t.Surface1).
		Width(w - 4).
		Align(lipgloss.Center)

	content.WriteString(headerStyle.Render(title) + "\n")

	if m.err != nil {
		content.WriteString(components.ErrorState(m.err.Error(), "Refresh data to retry", w-4) + "\n")
	}

	bodySections := []string{
		m.renderCoverageMap(),
		m.renderRedundancy(),
		m.renderVelocity(),
		m.renderConflicts(),
	}
	body := strings.Join(bodySections, "\n\n")

	footer := components.RenderFreshnessFooter(components.FreshnessOptions{
		LastUpdate:      m.LastUpdate(),
		RefreshInterval: m.Config().RefreshInterval,
		Width:           w - 4,
	})

	bodyHeight := h - lipgloss.Height(headerStyle.Render(title)) - 4
	if footer != "" {
		bodyHeight -= lipgloss.Height(footer) + 1
	}
	if bodyHeight < 3 {
		bodyHeight = 3
	}
	if m.scroll == nil {
		m.scroll = components.NewScrollablePanel(w-4, bodyHeight)
	}
	m.scroll.SetSize(w-4, bodyHeight)
	if body != m.lastBody {
		m.scroll.SetContent(body)
		m.lastBody = body
	}
	content.WriteString(m.scroll.RenderWithIndicators(w-4) + "\n")

	if footer != "" {
		content.WriteString(footer + "\n")
	}

	return boxStyle.Render(FitToHeight(content.String(), h-4))
}

func (m *MetricsPanel) renderCoverageMap() string {
	if m.data.Coverage == nil {
		return "Coverage: N/A"
	}

	covered, total := coverageCounts(m.data.Coverage)
	summary := fmt.Sprintf("Coverage: %.0f%% (%d/%d categories)", m.data.Coverage.Overall*100, covered, total)
	bar := m.coverageBarLine()
	if !m.expanded["coverage"] {
		return summary + "\n" + bar
	}

	lines := []string{summary, bar}
	lines = append(lines, m.coverageDetailLines()...)
	return strings.Join(lines, "\n")
}

func (m *MetricsPanel) renderRedundancy() string {
	if m.data.Redundancy == nil {
		return "Redundancy: N/A"
	}
	score := m.data.Redundancy.OverallScore
	summary := fmt.Sprintf("Redundancy: %.2f (%s)", score, redundancyLabel(score))
	if !m.expanded["redundancy"] {
		return summary
	}

	pairLines := m.redundancyPairLines()
	if len(pairLines) == 0 {
		return summary
	}
	return summary + "\n" + strings.Join(pairLines, "\n")
}

func (m *MetricsPanel) renderVelocity() string {
	if m.data.Velocity == nil {
		return "Velocity: N/A"
	}

	summary := fmt.Sprintf("Velocity: %.2f findings/1k tokens", m.data.Velocity.Overall)
	if !m.expanded["velocity"] {
		return summary
	}

	lines := []string{summary}
	if topLine := m.velocityTopLine(); topLine != "" {
		lines = append(lines, topLine)
	}
	if lowLine := m.velocityLowLine(); lowLine != "" {
		lines = append(lines, lowLine)
	}
	return strings.Join(lines, "\n")
}

func (m *MetricsPanel) renderConflicts() string {
	if m.data.Conflicts == nil {
		return "Conflicts: N/A"
	}

	summary := fmt.Sprintf("Conflicts: %d detected, %d resolved", m.data.Conflicts.TotalConflicts, m.data.Conflicts.ResolvedConflicts)
	if !m.expanded["conflicts"] {
		return summary
	}

	lines := []string{summary}
	for i, pair := range m.data.Conflicts.HighConflictPairs {
		if i >= maxPairLines {
			break
		}
		lines = append(lines, fmt.Sprintf("- %s", pair))
	}
	return strings.Join(lines, "\n")
}

func (m *MetricsPanel) coverageBarLine() string {
	if m.data.Coverage == nil {
		return ""
	}

	parts := make([]string, 0, len(ensemble.AllCategories()))
	for _, category := range ensemble.AllCategories() {
		coverage := m.coverageFor(category)
		bar := coverageBar(coverage.Coverage, coverageMiniBarWidth)
		parts = append(parts, fmt.Sprintf("[%s]%s", category.CategoryLetter(), bar))
	}
	return strings.Join(parts, " ")
}

func (m *MetricsPanel) coverageDetailLines() []string {
	lines := make([]string, 0, len(ensemble.AllCategories()))
	for _, category := range ensemble.AllCategories() {
		coverage := m.coverageFor(category)
		used := len(coverage.UsedModes)
		total := coverage.TotalModes
		bar := coverageBar(coverageFraction(used, total), coverageDetailBarWidth)
		lines = append(lines, fmt.Sprintf("[%s] %-12s %s %d/%d", category.CategoryLetter(), category.String(), bar, used, total))
	}
	return lines
}

func (m *MetricsPanel) coverageFor(category ensemble.ModeCategory) ensemble.CategoryCoverage {
	if m.data.Coverage == nil || m.data.Coverage.PerCategory == nil {
		return ensemble.CategoryCoverage{Category: category}
	}
	coverage, ok := m.data.Coverage.PerCategory[category]
	if !ok {
		return ensemble.CategoryCoverage{Category: category}
	}
	return coverage
}

func (m *MetricsPanel) redundancyPairLines() []string {
	if m.data.Redundancy == nil || len(m.data.Redundancy.PairwiseScores) == 0 {
		return nil
	}

	pairs := m.data.Redundancy.PairwiseScores
	limit := maxPairLines
	if len(pairs) < limit {
		limit = len(pairs)
	}
	lines := make([]string, 0, limit)
	for i := 0; i < len(pairs) && i < maxPairLines; i++ {
		pair := pairs[i]
		lines = append(lines, fmt.Sprintf("- %s <-> %s: %.2f (%s)", pair.ModeA, pair.ModeB, pair.Similarity, redundancyPairLabel(pair.Similarity)))
	}
	return lines
}

func (m *MetricsPanel) velocityTopLine() string {
	top := topVelocityEntries(m.data.Velocity, maxPairLines)
	if len(top) == 0 {
		return ""
	}
	return "Top: " + strings.Join(formatVelocityEntries(top), ", ")
}

func (m *MetricsPanel) velocityLowLine() string {
	if m.data.Velocity == nil || len(m.data.Velocity.LowPerformers) == 0 {
		return ""
	}
	lowIDs := append([]string(nil), m.data.Velocity.LowPerformers...)
	sort.Strings(lowIDs)
	low := make([]string, 0, len(lowIDs))
	for _, mode := range lowIDs {
		velocity := lookupVelocity(m.data.Velocity, mode)
		if velocity >= 0 {
			low = append(low, fmt.Sprintf("%s (%.1f)", mode, velocity))
		} else {
			low = append(low, mode)
		}
	}
	return "Low: " + strings.Join(low, ", ")
}

func (m *MetricsPanel) toggleExpanded(section string) {
	if m.expanded == nil {
		m.expanded = make(map[string]bool)
	}
	m.expanded[section] = !m.expanded[section]
}

func (m *MetricsPanel) logMetricsUpdate(data MetricsData) {
	coveragePercent := 0.0
	if data.Coverage != nil {
		coveragePercent = data.Coverage.Overall * 100
	}
	redundancyScore := 0.0
	if data.Redundancy != nil {
		redundancyScore = data.Redundancy.OverallScore
	}
	velocityOverall := 0.0
	if data.Velocity != nil {
		velocityOverall = data.Velocity.Overall
	}
	conflictsTotal := 0
	if data.Conflicts != nil {
		conflictsTotal = data.Conflicts.TotalConflicts
	}

	slog.Info("observability metrics updated",
		"coverage_percent", fmt.Sprintf("%.1f", coveragePercent),
		"redundancy_score", fmt.Sprintf("%.2f", redundancyScore),
		"velocity", fmt.Sprintf("%.2f", velocityOverall),
		"conflicts", conflictsTotal,
	)

	if data.Redundancy != nil && data.Redundancy.OverallScore >= redundancyWarnLevel {
		slog.Warn("high redundancy detected",
			"score", fmt.Sprintf("%.2f", data.Redundancy.OverallScore),
		)
	}
	if data.Velocity != nil && len(data.Velocity.LowPerformers) > 0 {
		slog.Warn("low velocity modes detected",
			"modes", data.Velocity.LowPerformers,
		)
	}
}

func metricsChanged(prev, next MetricsData) bool {
	return prev.Coverage != next.Coverage ||
		prev.Redundancy != next.Redundancy ||
		prev.Velocity != next.Velocity ||
		prev.Conflicts != next.Conflicts
}

func coverageCounts(report *ensemble.CoverageReport) (int, int) {
	if report == nil {
		return 0, len(ensemble.AllCategories())
	}
	covered := 0
	for _, category := range ensemble.AllCategories() {
		coverage, ok := report.PerCategory[category]
		if ok && len(coverage.UsedModes) > 0 {
			covered++
		}
	}
	return covered, len(ensemble.AllCategories())
}

func coverageFraction(used, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(used) / float64(total)
}

func coverageBar(fraction float64, width int) string {
	if width <= 0 {
		return ""
	}
	filled := int(fraction*float64(width) + 0.5)
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	return strings.Repeat("#", filled) + strings.Repeat("-", width-filled)
}

func redundancyLabel(score float64) string {
	switch {
	case score >= 0.7:
		return "high"
	case score >= 0.5:
		return "moderate"
	case score >= 0.3:
		return "acceptable"
	default:
		return "low"
	}
}

func redundancyPairLabel(score float64) string {
	switch {
	case score >= 0.7:
		return "high"
	case score >= 0.4:
		return "moderate"
	default:
		return "low"
	}
}

func topVelocityEntries(report *ensemble.VelocityReport, limit int) []ensemble.VelocityEntry {
	if report == nil || len(report.PerMode) == 0 || limit <= 0 {
		return nil
	}
	entries := append([]ensemble.VelocityEntry(nil), report.PerMode...)
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Velocity == entries[j].Velocity {
			return entries[i].ModeID < entries[j].ModeID
		}
		return entries[i].Velocity > entries[j].Velocity
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries
}

func formatVelocityEntries(entries []ensemble.VelocityEntry) []string {
	formatted := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.ModeID
		if entry.ModeName != "" {
			name = entry.ModeName
		}
		formatted = append(formatted, fmt.Sprintf("%s (%.1f)", name, entry.Velocity))
	}
	return formatted
}

func lookupVelocity(report *ensemble.VelocityReport, modeID string) float64 {
	if report == nil {
		return -1
	}
	for _, entry := range report.PerMode {
		if entry.ModeID == modeID {
			return entry.Velocity
		}
	}
	return -1
}
