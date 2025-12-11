// Package dashboard provides responsive layout utilities for wide displays.
// Inspired by beads_viewer's approach to high-resolution terminal rendering.
package dashboard

import (
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	status "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tokens"
	"github.com/Dicklesworthstone/ntm/internal/tracker"
	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wordwrap"
)

// Layout mode thresholds - defines breakpoints for responsive layouts
const (
	// MobileThreshold is the minimum width for basic layout
	MobileThreshold = 60

	// TabletThreshold enables split-view with list + detail panels
	TabletThreshold = 100

	// DesktopThreshold enables extra metadata columns
	DesktopThreshold = 140

	// UltraWideThreshold enables maximum information density
	UltraWideThreshold = 180
)

// LayoutMode represents the current display mode based on terminal width
type LayoutMode int

const (
	// LayoutMobile is for narrow terminals (<60 chars) - single column
	LayoutMobile LayoutMode = iota
	// LayoutCompact is for small terminals (60-100 chars) - card grid
	LayoutCompact
	// LayoutSplit is for medium terminals (100-140 chars) - list + detail
	LayoutSplit
	// LayoutWide is for large terminals (140-180 chars) - extra columns
	LayoutWide
	// LayoutUltraWide is for very large terminals (>180 chars) - max density
	LayoutUltraWide
)

// String returns the layout mode name
func (m LayoutMode) String() string {
	switch m {
	case LayoutMobile:
		return "mobile"
	case LayoutCompact:
		return "compact"
	case LayoutSplit:
		return "split"
	case LayoutWide:
		return "wide"
	case LayoutUltraWide:
		return "ultrawide"
	default:
		return "unknown"
	}
}

// LayoutForWidth returns the appropriate layout mode for a given terminal width
func LayoutForWidth(width int) LayoutMode {
	switch {
	case width >= UltraWideThreshold:
		return LayoutUltraWide
	case width >= DesktopThreshold:
		return LayoutWide
	case width >= TabletThreshold:
		return LayoutSplit
	case width >= MobileThreshold:
		return LayoutCompact
	default:
		return LayoutMobile
	}
}

// LayoutDimensions holds calculated dimensions for the current layout
type LayoutDimensions struct {
	Mode           LayoutMode
	Width          int
	Height         int
	ListWidth      int // Width of the list panel (for split view)
	DetailWidth    int // Width of the detail panel (for split view)
	CardWidth      int // Width of individual cards (for grid view)
	CardsPerRow    int // Number of cards per row (for grid view)
	BodyHeight     int // Height available for content (minus header/footer)
	ShowStatusCol  bool
	ShowContextCol bool
	ShowModelCol   bool
	ShowAgeCol     bool
	ShowCmdCol     bool
}

// CalculateLayout returns dimensions for the given width and height
func CalculateLayout(width, height int) LayoutDimensions {
	mode := LayoutForWidth(width)
	dims := LayoutDimensions{
		Mode:       mode,
		Width:      width,
		Height:     height,
		BodyHeight: height - 10, // Reserve space for header, stats bar, footer
	}

	// Determine which columns to show based on width
	dims.ShowStatusCol = width >= MobileThreshold
	dims.ShowContextCol = width >= TabletThreshold
	dims.ShowModelCol = width >= DesktopThreshold
	dims.ShowAgeCol = width >= DesktopThreshold
	dims.ShowCmdCol = width >= UltraWideThreshold

	switch mode {
	case LayoutMobile:
		dims.CardWidth = width - 4
		dims.CardsPerRow = 1

	case LayoutCompact:
		dims.CardWidth = 28
		dims.CardsPerRow = (width - 4) / (dims.CardWidth + 2)
		if dims.CardsPerRow < 1 {
			dims.CardsPerRow = 1
		}

	case LayoutSplit:
		// 40% list : 60% detail
		availWidth := width - 6 // Account for borders and gap
		dims.ListWidth = int(float64(availWidth) * 0.4)
		dims.DetailWidth = availWidth - dims.ListWidth
		dims.CardWidth = dims.ListWidth - 4

	case LayoutWide:
		// 35% list : 65% detail for more detail space
		availWidth := width - 6
		dims.ListWidth = int(float64(availWidth) * 0.35)
		dims.DetailWidth = availWidth - dims.ListWidth
		dims.CardWidth = dims.ListWidth - 4

	case LayoutUltraWide:
		// 30% list : 70% detail for maximum detail
		availWidth := width - 6
		dims.ListWidth = int(float64(availWidth) * 0.30)
		dims.DetailWidth = availWidth - dims.ListWidth
		dims.CardWidth = dims.ListWidth - 4
	}

	return dims
}

// RenderSparkline renders a mini horizontal bar graph (sparkline)
// Value should be between 0 and 1
func RenderSparkline(value float64, width int) string {
	if value < 0 {
		value = 0
	}
	if value > 1 {
		value = 1
	}

	// Unicode block characters for smooth gradients
	blocks := []string{"", "▏", "▎", "▍", "▌", "▋", "▊", "▉", "█"}

	fullChars := int(value * float64(width))
	remainder := (value * float64(width)) - float64(fullChars)

	var sb strings.Builder
	for i := 0; i < fullChars; i++ {
		sb.WriteString("█")
	}

	// Add partial block for smooth transition
	if fullChars < width {
		idx := int(remainder * float64(len(blocks)-1))
		if idx > 0 && idx < len(blocks) {
			sb.WriteString(blocks[idx])
		} else {
			sb.WriteString(" ")
		}
	}

	// Pad remainder
	current := fullChars + 1
	for current < width {
		sb.WriteString(" ")
		current++
	}

	return sb.String()
}

// RenderMiniBar renders a colored mini progress bar with semantic colors
func RenderMiniBar(value float64, width int, t theme.Theme) string {
	palette := styles.MiniBarPalette{
		Low:        t.Green,
		Mid:        t.Blue,   // info band (~40-59%)
		MidHigh:    t.Yellow, // warning band (~60-79%)
		High:       t.Red,    // critical (>=80%)
		Empty:      t.Surface1,
		FilledChar: "█",
		EmptyChar:  "░",
	}
	return styles.MiniBar(value, width, palette)
}

// RenderContextMiniBar renders context usage with warning indicator
func RenderContextMiniBar(percent float64, width int, tick int, t theme.Theme) string {
	bar := styles.ShimmerProgressBar(percent/100, width-4, "█", "░", tick, string(t.Green), string(t.Yellow), string(t.Red))

	// Add warning icon for high usage
	var suffix string
	if percent >= 90 {
		suffix = lipgloss.NewStyle().Foreground(t.Red).Bold(true).Render(" !!")
	} else if percent >= 80 {
		suffix = lipgloss.NewStyle().Foreground(t.Yellow).Bold(true).Render(" !")
	} else {
		suffix = "  "
	}

	return bar + suffix
}

// PaneTableRow represents a single row in the pane table
type PaneTableRow struct {
	Index            int
	Type             string
	Variant          string
	ModelVariant     string
	Title            string
	Status           string
	ContextPct       float64
	Model            string
	Command          string
	CurrentBead      string
	CurrentBeadTitle string
	FileChanges      int
	TokenVelocity    float64
	Tick             int
	IsSelected       bool
	IsCompacted      bool
	BorderColor      lipgloss.Color
}

// BuildPaneTableRows hydrates pane table rows using live status, bead progress,
// file change activity, and lightweight token velocity estimates.
func BuildPaneTableRows(
	panes []tmux.Pane,
	statuses map[string]status.AgentStatus,
	paneStatus map[int]PaneStatus,
	beads *bv.BeadsSummary,
	fileChanges []tracker.RecordedFileChange,
) []PaneTableRow {
	changeCounts := fileChangesByPane(panes, fileChanges)

	rows := make([]PaneTableRow, 0, len(panes))
	for _, pane := range panes {
		st, hasStatus := statuses[pane.ID]
		ps := paneStatus[pane.Index]
		row := PaneTableRow{
			Index:         pane.Index,
			Type:          string(pane.Type),
			Variant:       pane.Variant,
			ModelVariant:  pane.Variant,
			Title:         pane.Title,
			Status:        "unknown",
			Command:       pane.Command,
			FileChanges:   changeCounts[pane.Title],
			TokenVelocity: 0,
			ContextPct:    ps.ContextPercent,
			Model:         ps.ContextModel,
			IsCompacted:   ps.LastCompaction != nil,
		}

		row.CurrentBead = currentBeadForPane(pane, beads)
		if hasStatus {
			row.Status = st.State.String()
			row.TokenVelocity = tokenVelocityFromStatus(st)
			if row.ModelVariant == "" {
				row.ModelVariant = st.AgentType
			}
		} else if ps.State != "" {
			row.Status = ps.State
		}

		rows = append(rows, row)
	}

	return rows
}

func fileChangesByPane(panes []tmux.Pane, changes []tracker.RecordedFileChange) map[string]int {
	counts := make(map[string]int)
	if len(changes) == 0 {
		return counts
	}

	paneTitles := make(map[string]struct{}, len(panes))
	for _, p := range panes {
		paneTitles[p.Title] = struct{}{}
		paneTitles[p.ID] = struct{}{}
	}

	for _, ch := range changes {
		for _, agent := range ch.Agents {
			if _, ok := paneTitles[agent]; ok {
				counts[agent]++
			}
		}
	}

	return counts
}

func currentBeadForPane(pane tmux.Pane, beads *bv.BeadsSummary) string {
	if beads == nil || !beads.Available {
		return ""
	}

	for _, item := range beads.InProgressList {
		if item.Assignee == "" {
			continue
		}
		if strings.EqualFold(item.Assignee, pane.Title) || strings.EqualFold(item.Assignee, pane.ID) {
			return fmt.Sprintf("%s: %s", item.ID, item.Title)
		}
	}
	return ""
}

func tokenVelocityFromStatus(st status.AgentStatus) float64 {
	if st.LastOutput == "" {
		return 0
	}
	// Avoid zero/negative durations; fall back to a 60s window.
	minutes := time.Since(st.LastActive).Minutes()
	if minutes <= 0 {
		minutes = 1.0
	}

	tokensOut := tokens.EstimateTokens(st.LastOutput)
	if tokensOut == 0 {
		return 0
	}
	return float64(tokensOut) / minutes
}

// BuildPaneTableRow aggregates pane metadata into a single row structure.
// Beads/FileChanges/TokenVelocity are best-effort enrichments and may be empty
// when upstream data is unavailable.
func BuildPaneTableRow(pane tmux.Pane, ps PaneStatus, beads []bv.BeadPreview, fileChanges []tracker.RecordedFileChange) PaneTableRow {
	row := PaneTableRow{
		Index:        pane.Index,
		Type:         string(pane.Type),
		Variant:      pane.Variant,
		ModelVariant: pane.Variant,
		Title:        pane.Title,
		Status:       ps.State,
		ContextPct:   ps.ContextPercent,
		Model:        ps.ContextModel,
		Command:      pane.Command,
		IsCompacted:  ps.State == "compacted",
	}

	// Prefer context model as variant when pane title lacks one.
	if row.ModelVariant == "" {
		row.ModelVariant = ps.ContextModel
	}

	// Attach a current bead hint (first ready preview as a lightweight default).
	if len(beads) > 0 {
		row.CurrentBead = beads[0].ID
		row.CurrentBeadTitle = beads[0].Title
	}

	// Count file changes mentioning this pane's agent.
	for _, fc := range fileChanges {
		for _, agent := range fc.Agents {
			if agent == pane.Title || agent == pane.ID || strings.Contains(agent, string(pane.Type)) {
				row.FileChanges++
				break
			}
		}
	}

	// Approximate token velocity using recent command text as a proxy.
	if pane.Command != "" {
		row.TokenVelocity = float64(tokens.EstimateTokens(pane.Command))
	}

	return row
}

// RenderPaneRow renders a single pane as a table row with progressive columns
func RenderPaneRow(row PaneTableRow, dims LayoutDimensions, t theme.Theme) string {
	var parts []string

	// Selection indicator
	selectStyle := lipgloss.NewStyle().Foreground(t.Pink).Bold(true)
	if row.IsSelected {
		parts = append(parts, selectStyle.Render("▸"))
	} else {
		parts = append(parts, " ")
	}

	// Index badge
	idxStyle := lipgloss.NewStyle().Foreground(t.Overlay)
	parts = append(parts, idxStyle.Render(fmt.Sprintf("%2d", row.Index)))

	// Type icon with color
	var typeColor lipgloss.Color
	var typeIcon string
	switch row.Type {
	case "cc":
		typeColor = t.Claude
		typeIcon = "󰗣"
	case "cod":
		typeColor = t.Codex
		typeIcon = "󰘦"
	case "gmi":
		typeColor = t.Gemini
		typeIcon = "󰇮"
	default:
		typeColor = t.Green
		typeIcon = "󰄛"
	}
	typeStyle := lipgloss.NewStyle().Foreground(typeColor).Bold(true)
	parts = append(parts, typeStyle.Render(typeIcon))

	// Status indicator (always shown except mobile)
	if dims.ShowStatusCol {
		statusStyle := lipgloss.NewStyle()
		var statusIcon string
		switch row.Status {
		case "working":
			statusIcon = "●"
			statusStyle = statusStyle.Foreground(t.Green)
		case "idle":
			statusIcon = "○"
			statusStyle = statusStyle.Foreground(t.Yellow)
		case "error":
			statusIcon = "✗"
			statusStyle = statusStyle.Foreground(t.Red)
		case "compacted":
			statusIcon = "⚠"
			statusStyle = statusStyle.Foreground(t.Peach).Bold(true)
		case "rate_limited":
			statusIcon = "⏳"
			statusStyle = statusStyle.Foreground(t.Maroon).Bold(true)
		default:
			statusIcon = "•"
			statusStyle = statusStyle.Foreground(t.Overlay)
		}
		parts = append(parts, statusStyle.Render(statusIcon))
	}

	// Title (flexible width)
	titleWidth := dims.CardWidth - 16 // Base width minus fixed columns
	if dims.ShowContextCol {
		titleWidth -= 12 // Context bar width
	}
	if dims.ShowModelCol {
		titleWidth -= 10 // Model column width
	}
	if titleWidth < 10 {
		titleWidth = 10
	}

	title := row.Title
	if len(title) > titleWidth {
		title = title[:titleWidth-3] + "..."
	}
	titleStyle := lipgloss.NewStyle().Foreground(t.Text)
	if row.IsSelected {
		titleStyle = titleStyle.Bold(true)
	}
	parts = append(parts, titleStyle.Width(titleWidth).Render(title))

	// Context bar (tablet and up)
	if dims.ShowContextCol {
		contextBar := RenderContextMiniBar(row.ContextPct, 10, row.Tick, t)
		parts = append(parts, contextBar)
	}

	// Model variant (desktop and up)
	modelVariant := row.Variant
	if modelVariant == "" {
		modelVariant = row.ModelVariant
	}

	if dims.ShowModelCol && modelVariant != "" {
		badge := styles.ModelBadge(modelVariant, styles.BadgeOptions{
			Style:    styles.BadgeStyleCompact,
			Bold:     false,
			ShowIcon: false,
		})
		// Ensure fixed width alignment
		if lipgloss.Width(badge) < 8 {
			badge = badge + strings.Repeat(" ", 8-lipgloss.Width(badge))
		}
		parts = append(parts, badge)
	} else if dims.ShowModelCol {
		parts = append(parts, strings.Repeat(" ", 8))
	}

	// Command (ultrawide only)
	if dims.ShowCmdCol && row.Command != "" {
		cmdStyle := lipgloss.NewStyle().
			Foreground(t.Overlay).
			Italic(true).
			Width(20)
		parts = append(parts, cmdStyle.Render(truncate(row.Command, 20)))
	}

	firstLine := strings.Join(parts, " ")

	// Render second line for rich content (Wide+)
	// Show bead info, file changes, etc.
	if dims.Mode >= LayoutWide && (row.CurrentBead != "" || row.FileChanges > 0 || row.TokenVelocity > 0) {
		var subParts []string

		// Indent to align with title (approx 8 chars: sel(1)+space+idx(2)+icon(1)+status(1)+spaces)
		indent := "        "

		if row.CurrentBead != "" {
			beadText := row.CurrentBead
			if row.CurrentBeadTitle != "" {
				beadText += ": " + row.CurrentBeadTitle
			}
			subParts = append(subParts, lipgloss.NewStyle().Foreground(t.Primary).Render("● "+truncate(beadText, 40)))
		}

		if row.FileChanges > 0 {
			subParts = append(subParts, lipgloss.NewStyle().Foreground(t.Yellow).Render(fmt.Sprintf("%d files", row.FileChanges)))
		}

		if row.TokenVelocity > 0 {
			subParts = append(subParts, styles.TokenVelocityBadge(row.TokenVelocity, styles.BadgeOptions{
				Style:    styles.BadgeStyleCompact,
				Bold:     false,
				ShowIcon: true,
			}))
		}

		secondLine := indent + strings.Join(subParts, " │ ")

		if row.IsSelected {
			return lipgloss.NewStyle().Background(t.Surface0).Render(firstLine + "\n" + secondLine)
		}
		return firstLine + "\n" + secondLine
	}

	if row.IsSelected {
		return lipgloss.NewStyle().Background(t.Surface0).Render(firstLine)
	}
	return firstLine
}

// RenderPaneDetail renders the detail panel for a selected pane
func RenderPaneDetail(pane tmux.Pane, ps PaneStatus, dims LayoutDimensions, t theme.Theme) string {
	var lines []string
	innerWidth := dims.DetailWidth
	if innerWidth < 12 {
		innerWidth = 12
	}

	// Header with pane title
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(t.Text).
		BorderStyle(lipgloss.NormalBorder()).
		BorderBottom(true).
		BorderForeground(t.Surface1).
		Width(innerWidth-4).
		Padding(0, 1)
	lines = append(lines, headerStyle.Render(truncate(pane.Title, innerWidth-6)))
	lines = append(lines, "")

	// Info grid
	labelStyle := lipgloss.NewStyle().Foreground(t.Subtext).Width(12)
	valueStyle := lipgloss.NewStyle().Foreground(t.Text)

	// Type
	var typeColor lipgloss.Color
	switch pane.Type {
	case tmux.AgentClaude:
		typeColor = t.Claude
	case tmux.AgentCodex:
		typeColor = t.Codex
	case tmux.AgentGemini:
		typeColor = t.Gemini
	default:
		typeColor = t.Green
	}
	typeBadge := lipgloss.NewStyle().
		Background(typeColor).
		Foreground(t.Base).
		Bold(true).
		Padding(0, 1).
		Render(string(pane.Type))
	lines = append(lines, labelStyle.Render("Type:")+typeBadge)

	// Index
	lines = append(lines, labelStyle.Render("Index:")+valueStyle.Render(fmt.Sprintf("%d", pane.Index)))

	// Dimensions
	lines = append(lines, labelStyle.Render("Size:")+valueStyle.Render(fmt.Sprintf("%d × %d", pane.Width, pane.Height)))

	// Variant/Model
	if pane.Variant != "" {
		variantBadge := lipgloss.NewStyle().
			Background(t.Surface1).
			Foreground(t.Text).
			Padding(0, 1).
			Render(pane.Variant)
		lines = append(lines, labelStyle.Render("Model:")+variantBadge)
	}

	lines = append(lines, "")

	// Context usage section
	if ps.ContextLimit > 0 {
		lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(t.Lavender).Render("Context Usage"))
		lines = append(lines, "")

		// Large context bar
		barWidth := innerWidth - 20
		if barWidth > 60 {
			barWidth = 60
		}
		if barWidth < 10 {
			barWidth = 10
		}
		contextBar := renderDetailContextBar(ps.ContextPercent, barWidth, t)
		lines = append(lines, contextBar)

		// Stats
		statsStyle := lipgloss.NewStyle().Foreground(t.Subtext)
		lines = append(lines, statsStyle.Render(fmt.Sprintf(
			"  %d / %d tokens (%.1f%%)",
			ps.ContextTokens, ps.ContextLimit, ps.ContextPercent,
		)))
	}

	lines = append(lines, "")

	// Status section
	lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(t.Lavender).Render("Status"))
	lines = append(lines, "")

	statusIcon, statusColor := getStatusIconAndColor(ps.State, t)
	statusStyle := lipgloss.NewStyle().Foreground(statusColor)
	lines = append(lines, "  "+statusStyle.Render(statusIcon+" "+ps.State))

	// Compaction warning
	if ps.LastCompaction != nil {
		warnStyle := lipgloss.NewStyle().Foreground(t.Peach).Bold(true)
		lines = append(lines, "")
		lines = append(lines, warnStyle.Render("  ⚠ Context compaction detected"))
		if ps.RecoverySent {
			lines = append(lines, lipgloss.NewStyle().Foreground(t.Green).Render("    ↻ Recovery prompt sent"))
		}
	}

	// Command (if running)
	if pane.Command != "" {
		lines = append(lines, "")
		lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(t.Lavender).Render("Command"))
		lines = append(lines, "")
		cmdWidth := innerWidth - 6
		if cmdWidth < 10 {
			cmdWidth = innerWidth
		}
		wrappedCmd := wordwrap.String(strings.TrimSpace(pane.Command), cmdWidth)
		cmdStyle := lipgloss.NewStyle().
			Foreground(t.Overlay).
			Italic(true).
			Width(cmdWidth).
			MaxWidth(cmdWidth)
		lines = append(lines, "  "+cmdStyle.Render(wrappedCmd))
	}

	return strings.Join(lines, "\n")
}

// renderDetailContextBar renders a large context bar for the detail view
func renderDetailContextBar(percent float64, width int, t theme.Theme) string {
	if percent > 100 {
		percent = 100
	}

	filled := int(percent * float64(width) / 100)
	empty := width - filled

	// Determine color based on percentage
	var barColor lipgloss.Color
	if percent >= 80 {
		barColor = t.Red
	} else if percent >= 60 {
		barColor = t.Yellow
	} else {
		barColor = t.Green
	}

	filledStyle := lipgloss.NewStyle().Foreground(barColor)
	emptyStyle := lipgloss.NewStyle().Foreground(t.Surface1)

	bar := "  [" +
		filledStyle.Render(strings.Repeat("█", filled)) +
		emptyStyle.Render(strings.Repeat("░", empty)) +
		"]"

	return bar
}

// getStatusIconAndColor returns icon and color for a status state
func getStatusIconAndColor(state string, t theme.Theme) (string, lipgloss.Color) {
	switch state {
	case "working":
		return "●", t.Green
	case "idle":
		return "○", t.Yellow
	case "error":
		return "✗", t.Red
	case "compacted":
		return "⚠", t.Peach
	default:
		return "•", t.Overlay
	}
}

// truncate shortens a string to maxLen with ellipsis
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// RenderTableHeader renders the header row for pane table
func RenderTableHeader(dims LayoutDimensions, t theme.Theme) string {
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(t.Subtext).
		BorderStyle(lipgloss.NormalBorder()).
		BorderBottom(true).
		BorderForeground(t.Surface1)

	var parts []string
	parts = append(parts, " ") // Selection column
	parts = append(parts, headerStyle.Width(2).Render("#"))
	parts = append(parts, headerStyle.Width(2).Render("T"))

	if dims.ShowStatusCol {
		parts = append(parts, headerStyle.Width(1).Render("S"))
	}

	titleWidth := dims.CardWidth - 16
	if dims.ShowContextCol {
		titleWidth -= 12
	}
	if dims.ShowModelCol {
		titleWidth -= 10
	}
	if titleWidth < 10 {
		titleWidth = 10
	}
	parts = append(parts, headerStyle.Width(titleWidth).Render("TITLE"))

	if dims.ShowContextCol {
		parts = append(parts, headerStyle.Width(10).Render("CONTEXT"))
	}

	if dims.ShowModelCol {
		parts = append(parts, headerStyle.Width(8).Render("MODEL"))
	}

	if dims.ShowCmdCol {
		parts = append(parts, headerStyle.Width(20).Render("COMMAND"))
	}

	return strings.Join(parts, " ")
}

// RenderLayoutIndicator renders a small indicator showing current layout mode
func RenderLayoutIndicator(mode LayoutMode, t theme.Theme) string {
	modeStyle := lipgloss.NewStyle().
		Foreground(t.Overlay).
		Italic(true)

	icon := ""
	switch mode {
	case LayoutMobile:
		icon = "📱"
	case LayoutCompact:
		icon = "🖥"
	case LayoutSplit:
		icon = "◫"
	case LayoutWide:
		icon = "▭"
	case LayoutUltraWide:
		icon = "⬚"
	}

	return modeStyle.Render(icon + " " + mode.String())
}

// FocusedPanel tracks which panel has focus in split view
type FocusedPanel int

const (
	FocusList FocusedPanel = iota
	FocusDetail
)

// PanelStyles returns styles for panels based on focus state
func PanelStyles(focused FocusedPanel, t theme.Theme) (listStyle, detailStyle lipgloss.Style) {
	baseStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(0, 1)

	focusedBorder := t.Pink
	unfocusedBorder := t.Surface1

	if focused == FocusList {
		listStyle = baseStyle.BorderForeground(focusedBorder)
		detailStyle = baseStyle.BorderForeground(unfocusedBorder)
	} else {
		listStyle = baseStyle.BorderForeground(unfocusedBorder)
		detailStyle = baseStyle.BorderForeground(focusedBorder)
	}

	return listStyle, detailStyle
}

// ViewportPosition tracks scroll position in pane list
type ViewportPosition struct {
	Offset   int // First visible item index
	Visible  int // Number of visible items
	Total    int // Total items
	Selected int // Currently selected index
}

// EnsureVisible adjusts offset to keep selected item visible
func (vp *ViewportPosition) EnsureVisible() {
	if vp.Selected < vp.Offset {
		vp.Offset = vp.Selected
	}
	if vp.Selected >= vp.Offset+vp.Visible {
		vp.Offset = vp.Selected - vp.Visible + 1
	}
	if vp.Offset < 0 {
		vp.Offset = 0
	}
	if vp.Offset > vp.Total-vp.Visible {
		vp.Offset = vp.Total - vp.Visible
		if vp.Offset < 0 {
			vp.Offset = 0
		}
	}
}

// ScrollIndicator returns a scroll position indicator
func (vp *ViewportPosition) ScrollIndicator(t theme.Theme) string {
	if vp.Total <= vp.Visible {
		return ""
	}

	style := lipgloss.NewStyle().Foreground(t.Overlay)
	return style.Render(fmt.Sprintf("(%d-%d of %d)",
		vp.Offset+1,
		min(vp.Offset+vp.Visible, vp.Total),
		vp.Total,
	))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// GetTokens returns the design tokens for the current width
func GetTokens(width int) styles.DesignTokens {
	return styles.TokensForWidth(width)
}
