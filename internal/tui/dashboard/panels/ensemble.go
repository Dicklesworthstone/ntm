package panels

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// EnsembleStatusMsg updates the panel with the latest ensemble session state.
type EnsembleStatusMsg struct {
	Session *ensemble.EnsembleSession
	Err     error
}

// EnsembleAction identifies an action requested from the panel.
type EnsembleAction string

const (
	EnsembleActionSynthesize  EnsembleAction = "synthesize"
	EnsembleActionViewOutputs EnsembleAction = "view_outputs"
	EnsembleActionRefresh     EnsembleAction = "refresh"
)

// EnsembleActionMsg is emitted when the user triggers an ensemble action.
type EnsembleActionMsg struct {
	Action EnsembleAction
}

// ensembleConfig returns the configuration for the ensemble panel.
func ensembleConfig() PanelConfig {
	return PanelConfig{
		ID:              "ensemble",
		Title:           "Reasoning Ensemble",
		Priority:        PriorityHigh,
		RefreshInterval: 5 * time.Second,
		MinWidth:        30,
		MinHeight:       8,
		Collapsible:     true,
	}
}

// EnsemblePanel shows real-time ensemble status and assignments.
type EnsemblePanel struct {
	PanelBase
	session *ensemble.EnsembleSession
	catalog *ensemble.ModeCatalog
	err     error
	now     func() time.Time
}

// NewEnsemblePanel creates a new ensemble panel.
func NewEnsemblePanel() *EnsemblePanel {
	return &EnsemblePanel{
		PanelBase: NewPanelBase(ensembleConfig()),
		now:       time.Now,
	}
}

// SetSession updates the panel state with a new ensemble session.
func (p *EnsemblePanel) SetSession(session *ensemble.EnsembleSession, err error) {
	p.session = session
	p.err = err
	if err == nil {
		p.SetLastUpdate(time.Now())
	}
}

// SetCatalog provides the mode catalog for code/tier lookups.
func (p *EnsemblePanel) SetCatalog(catalog *ensemble.ModeCatalog) {
	p.catalog = catalog
}

// Init implements tea.Model.
func (p *EnsemblePanel) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (p *EnsemblePanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case EnsembleStatusMsg:
		p.SetSession(msg.Session, msg.Err)
		return p, nil
	case tea.KeyMsg:
		if !p.IsFocused() {
			return p, nil
		}
		switch msg.String() {
		case "s", "S":
			return p, func() tea.Msg { return EnsembleActionMsg{Action: EnsembleActionSynthesize} }
		case "v", "V":
			return p, func() tea.Msg { return EnsembleActionMsg{Action: EnsembleActionViewOutputs} }
		case "r", "R":
			return p, func() tea.Msg { return EnsembleActionMsg{Action: EnsembleActionRefresh} }
		}
	}
	return p, nil
}

// Keybindings returns ensemble panel specific shortcuts.
func (p *EnsemblePanel) Keybindings() []Keybinding {
	return []Keybinding{
		{
			Key:         key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "synthesize")),
			Description: "Trigger synthesis",
			Action:      "synthesize",
		},
		{
			Key:         key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "view outputs")),
			Description: "View ensemble outputs",
			Action:      "view_outputs",
		},
		{
			Key:         key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
			Description: "Refresh ensemble status",
			Action:      "refresh",
		},
	}
}

// View renders the panel content.
func (p *EnsemblePanel) View() string {
	t := theme.Current()
	w, h := p.Width(), p.Height()

	if w <= 0 {
		return ""
	}

	borderColor := t.Surface1
	bgColor := t.Base
	if p.IsFocused() {
		borderColor = t.Pink
		bgColor = t.Surface0
	}

	boxStyle := lipgloss.NewStyle().
		Background(bgColor).
		Width(w).
		Height(h)

	title := p.Config().Title
	if p.err != nil {
		errorBadge := lipgloss.NewStyle().
			Background(t.Red).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render("⚠ Error")
		title = title + " " + errorBadge
	}

	header := lipgloss.NewStyle().
		Bold(true).
		Foreground(t.Text).
		BorderStyle(lipgloss.NormalBorder()).
		BorderBottom(true).
		BorderForeground(borderColor).
		Width(w).
		Padding(0, 1).
		Render(title)

	var content strings.Builder
	content.WriteString(header + "\n")

	if p.err != nil {
		errMsg := layout.TruncateWidthDefault(p.err.Error(), w-6)
		content.WriteString(components.ErrorState(errMsg, "Press r to refresh", w))
		return boxStyle.Render(FitToHeight(content.String(), h))
	}

	if p.session == nil {
		content.WriteString(components.RenderEmptyState(components.EmptyStateOptions{
			Icon:        components.IconWaiting,
			Title:       "No ensemble running",
			Description: "Start with `ntm ensemble` to see live status",
			Width:       w,
			Centered:    true,
		}))
		return boxStyle.Render(FitToHeight(content.String(), h))
	}

	question := p.session.Question
	if strings.TrimSpace(question) == "" {
		question = "—"
	}
	preset := p.session.PresetUsed
	if strings.TrimSpace(preset) == "" {
		preset = "custom"
	}
	started := p.formatTimeAgo(p.session.CreatedAt)

	questionLine := lipgloss.NewStyle().
		Foreground(t.Text).
		Padding(0, 1).
		Render(layout.TruncateWidthDefault(fmt.Sprintf("Question: %s", question), w-4))
	metaLine := lipgloss.NewStyle().
		Foreground(t.Subtext).
		Padding(0, 1).
		Render(layout.TruncateWidthDefault(fmt.Sprintf("Preset: %s  Started: %s", preset, started), w-4))
	separator := lipgloss.NewStyle().
		Foreground(borderColor).
		Render(strings.Repeat("─", maxInt(0, w)))

	content.WriteString(questionLine + "\n")
	content.WriteString(metaLine + "\n")
	content.WriteString(separator + "\n")

	assignments := p.session.Assignments
	if len(assignments) == 0 {
		content.WriteString(components.RenderEmptyState(components.EmptyStateOptions{
			Icon:        components.IconWaiting,
			Title:       "No assignments yet",
			Description: "Waiting for ensemble modes to be assigned",
			Width:       w,
			Centered:    true,
		}))
		return boxStyle.Render(FitToHeight(content.String(), h))
	}

	activeCount, doneCount, pendingCount := summarizeAssignmentStatus(assignments)
	advancedCount := p.countAdvanced(assignments)

	footerLines := 2
	if advancedCount > 0 {
		footerLines++
	}

	usedHeight := lipgloss.Height(content.String())
	remaining := h - usedHeight - footerLines
	if remaining < 0 {
		remaining = 0
	}

	for i, assignment := range assignments {
		if i >= remaining {
			break
		}
		line := p.renderAssignmentLine(assignment, w-2)
		content.WriteString(line + "\n")
	}

	progressLine := fmt.Sprintf("Progress: %d/%d active  │  Done: %d  Pending: %d  Status: %s",
		activeCount, len(assignments), doneCount, pendingCount, p.session.Status.String())
	progressLine = layout.TruncateWidthDefault(progressLine, w-4)
	content.WriteString(lipgloss.NewStyle().Foreground(t.Subtext).Padding(0, 1).Render(progressLine) + "\n")

	if advancedCount > 0 {
		warn := fmt.Sprintf("Advanced modes active (%d) — press V for details", advancedCount)
		warn = layout.TruncateWidthDefault(warn, w-4)
		warnLine := lipgloss.NewStyle().Foreground(t.Yellow).Padding(0, 1).Render(warn)
		content.WriteString(warnLine + "\n")
	}

	help := components.RenderHelpBar(components.HelpBarOptions{
		Hints: []components.KeyHint{
			{Key: "S", Desc: "synthesize"},
			{Key: "V", Desc: "view outputs"},
			{Key: "R", Desc: "refresh"},
		},
		Width: w - 2,
	})
	if help != "" {
		content.WriteString(lipgloss.NewStyle().Padding(0, 1).Render(help))
	}

	return boxStyle.Render(FitToHeight(content.String(), h))
}

func (p *EnsemblePanel) renderAssignmentLine(a ensemble.ModeAssignment, width int) string {
	t := theme.Current()
	mode := p.lookupMode(a.ModeID)

	statusIcon := assignmentStatusIcon(a.Status)
	pane := shortenPaneName(a.PaneName)
	modeIcon := "•"
	modeName := a.ModeID
	modeCode := strings.ToUpper(a.ModeID)
	tierBadge := ""

	if mode != nil {
		if mode.Icon != "" {
			modeIcon = mode.Icon
		}
		if mode.Name != "" {
			modeName = mode.Name
		}
		if mode.Code != "" {
			modeCode = mode.Code
		}
		tierBadge = renderTierBadge(mode.Tier, t)
	}

	progress := renderProgressBar(assignmentProgress(a.Status), maxInt(6, minInt(16, width/6)))
	statusText := string(a.Status)

	line := fmt.Sprintf("%s %-6s %s %-4s %-18s %s %s %s",
		statusIcon,
		pane,
		modeIcon,
		modeCode,
		modeName,
		tierBadge,
		progress,
		statusText,
	)

	return lipgloss.NewStyle().Padding(0, 1).Render(layout.TruncateWidthDefault(line, width))
}

func (p *EnsemblePanel) lookupMode(modeID string) *ensemble.ReasoningMode {
	if p.catalog == nil {
		return nil
	}
	if mode := p.catalog.GetMode(modeID); mode != nil {
		return mode
	}
	for _, m := range p.catalog.ListModes() {
		if strings.EqualFold(m.Code, modeID) {
			return p.catalog.GetMode(m.ID)
		}
	}
	return nil
}

func (p *EnsemblePanel) countAdvanced(assignments []ensemble.ModeAssignment) int {
	count := 0
	for _, a := range assignments {
		mode := p.lookupMode(a.ModeID)
		if mode == nil {
			continue
		}
		if mode.Tier == ensemble.TierAdvanced || mode.Tier == ensemble.TierExperimental {
			count++
		}
	}
	return count
}

func assignmentProgress(status ensemble.AssignmentStatus) float64 {
	switch status {
	case ensemble.AssignmentPending:
		return 0.05
	case ensemble.AssignmentInjecting:
		return 0.25
	case ensemble.AssignmentActive:
		return 0.6
	case ensemble.AssignmentDone:
		return 1.0
	case ensemble.AssignmentError:
		return 1.0
	default:
		return 0
	}
}

func assignmentStatusIcon(status ensemble.AssignmentStatus) string {
	switch status {
	case ensemble.AssignmentActive:
		return "●"
	case ensemble.AssignmentInjecting:
		return "◐"
	case ensemble.AssignmentPending:
		return "○"
	case ensemble.AssignmentDone:
		return "✓"
	case ensemble.AssignmentError:
		return "✗"
	default:
		return "•"
	}
}

func renderTierBadge(tier ensemble.ModeTier, t theme.Theme) string {
	if tier == "" {
		return ""
	}
	opts := styles.BadgeOptions{
		Style:      styles.BadgeStyleCompact,
		Bold:       true,
		ShowIcon:   false,
		FixedWidth: 4,
	}
	switch tier {
	case ensemble.TierCore:
		return styles.TextBadge("CORE", t.Green, t.Base, opts)
	case ensemble.TierAdvanced:
		return styles.TextBadge("ADV", t.Yellow, t.Base, opts)
	case ensemble.TierExperimental:
		return styles.TextBadge("EXP", t.Red, t.Base, opts)
	default:
		return styles.TextBadge(strings.ToUpper(tier.String()), t.Surface1, t.Text, opts)
	}
}

func renderProgressBar(percent float64, width int) string {
	if width < 4 {
		width = 4
	}
	bar := components.NewProgressBar(width)
	bar.ShowPercent = false
	bar.ShowLabel = false
	bar.Animated = false
	bar.SetPercent(percent)
	return bar.View()
}

func summarizeAssignmentStatus(assignments []ensemble.ModeAssignment) (active, done, pending int) {
	for _, a := range assignments {
		switch a.Status {
		case ensemble.AssignmentActive, ensemble.AssignmentInjecting:
			active++
		case ensemble.AssignmentDone:
			done++
		case ensemble.AssignmentPending:
			pending++
		}
	}
	return active, done, pending
}

func shortenPaneName(title string) string {
	if idx := strings.LastIndex(title, "__"); idx != -1 && idx+2 < len(title) {
		return title[idx+2:]
	}
	return title
}

func (p *EnsemblePanel) formatTimeAgo(t time.Time) string {
	now := p.now
	if now == nil {
		now = time.Now
	}
	d := now().Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
