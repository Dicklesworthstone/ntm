// Package dashboard provides responsive layout utilities for wide displays.
// Inspired by beads_viewer's approach to high-resolution terminal rendering.
package dashboard

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/integrations/pt"
	status "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tokens"
	"github.com/Dicklesworthstone/ntm/internal/tracker"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
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
	HiddenColCount int // Number of columns hidden due to narrow width
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

	// Calculate hidden column count for header indicator
	// Only count columns that are rendered in the header: Context, Model, Cmd
	dims.HiddenColCount = 0
	if !dims.ShowContextCol {
		dims.HiddenColCount++
	}
	if !dims.ShowModelCol {
		dims.HiddenColCount++
	}
	if !dims.ShowCmdCol {
		dims.HiddenColCount++
	}

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

// RenderContextMiniBarWithHistory renders context usage with optional trend sparkline
// [tui-upgrade: bd-3btd6] - Enhanced progress bar with gradient and sparkline trend
func RenderContextMiniBarWithHistory(percent float64, history []float64, width int, tick int, t theme.Theme) string {
	if percent > 100 {
		percent = 100
	}
	if percent < 0 {
		percent = 0
	}

	// Calculate bar and sparkline widths
	barWidth := width - 4 // Reserve 4 chars for warning indicator
	sparkWidth := 0
	if len(history) >= 3 && barWidth > 12 {
		sparkWidth = min(6, len(history), barWidth/3)
		barWidth = barWidth - sparkWidth - 1 // -1 for space separator
	}

	// Create gradient colors based on usage level.
	// Keep this to two colors so styles.ProgressBar uses bubbles/progress.
	var gradientColors []string
	switch {
	case percent >= 90:
		gradientColors = []string{string(t.Peach), string(t.Red)}
	case percent >= 80:
		gradientColors = []string{string(t.Yellow), string(t.Peach)}
	case percent >= 60:
		gradientColors = []string{string(t.Blue), string(t.Yellow)}
	default:
		gradientColors = []string{string(t.Green), string(t.Teal)}
	}

	bar := styles.ProgressBar(percent/100, barWidth, "█", "░", gradientColors...)

	// Add mini sparkline for context growth trend if history available
	var sparkline string
	if sparkWidth > 0 {
		spark := components.SparklineStyled(history, sparkWidth)
		sparkline = " " + spark
	}

	// Add warning icon for high usage with shimmer effect
	var suffix string
	switch {
	case percent >= 90:
		// Critical: shimmer the warning in red/orange gradient
		suffix = " " + styles.Shimmer("!!", tick, string(t.Red), string(t.Maroon), string(t.Red))
	case percent >= 80:
		// Warning: shimmer the warning in yellow/orange gradient
		suffix = " " + styles.Shimmer("!", tick, string(t.Yellow), string(t.Peach), string(t.Yellow))
	default:
		suffix = "  "
	}

	return bar + sparkline + suffix
}

// PaneTableRow represents a single row in the pane table
type PaneTableRow struct {
	Index            int
	WindowIndex      int
	PaneID           string
	Address          string
	Type             string
	Variant          string
	ModelVariant     string
	Title            string
	Status           string
	HealthClass      pt.Classification // Health classification from process_triage
	HealthSince      time.Time         // When this health state started
	ContextPct       float64
	ContextHistory   []float64 // Context usage history for trend sparkline [tui-upgrade: bd-3btd6]
	Model            string
	Command          string
	CurrentBead      string
	CurrentBeadTitle string
	FileChanges      int
	TokenVelocity    float64
	LocalTokensPS    float64
	LocalMemoryBytes int64
	Tick             int
	IsSelected       bool
	IsCompacted      bool
	BorderColor      lipgloss.Color
}

// BuildPaneTableRows hydrates pane table rows using live status, bead progress,
// file change activity, health states, and lightweight token velocity estimates.
// The theme is used to assign per-agent border colors.
func BuildPaneTableRows(
	panes []tmux.Pane,
	statuses map[string]status.AgentStatus,
	paneStatus map[string]PaneStatus,
	beads *bv.BeadsSummary,
	fileChanges []tracker.RecordedFileChange,
	healthStates map[string]*pt.AgentState,
	tick int,
	t theme.Theme,
) []PaneTableRow {
	changeCounts := fileChangesByPane(panes, fileChanges)
	multiWindow := tmux.PanesSpanMultipleWindows(panes)

	rows := make([]PaneTableRow, 0, len(panes))
	for _, pane := range panes {
		st, hasStatus := statuses[pane.ID]
		ps := paneStatus[paneStatusKey(pane)]
		row := PaneTableRow{
			Tick:           tick,
			Index:          pane.Index,
			WindowIndex:    pane.WindowIndex,
			PaneID:         pane.ID,
			Address:        pane.Ref().Canonical(multiWindow),
			Type:           string(pane.Type),
			Variant:        pane.Variant,
			ModelVariant:   pane.Variant,
			Title:          pane.Title,
			Status:         "unknown",
			HealthClass:    pt.ClassUnknown,
			Command:        pane.Command,
			FileChanges:    changeCounts[paneKey(pane)],
			TokenVelocity:  0,
			LocalTokensPS:  0,
			ContextPct:     ps.ContextPercent,
			ContextHistory: append([]float64(nil), ps.ContextHistory...), // [tui-upgrade: bd-3btd6]
			Model:          ps.ContextModel,
			IsCompacted:    ps.LastCompaction != nil,
			BorderColor:    AgentBorderColor(string(pane.Type), t),
		}

		// Populate health classification from process_triage
		if healthStates != nil {
			if hs, ok := healthStates[pane.Title]; ok {
				row.HealthClass = hs.Classification
				row.HealthSince = hs.Since
			} else if hs, ok := healthStates[pane.ID]; ok {
				row.HealthClass = hs.Classification
				row.HealthSince = hs.Since
			}
		}

		row.CurrentBead = currentBeadForPane(pane, beads)
		if hasStatus {
			row.Status = st.State.String()
			row.TokenVelocity = ps.TokenVelocity
			row.LocalTokensPS = ps.LocalTokensPerSecond
			row.LocalMemoryBytes = ps.LocalMemoryBytes
			if row.ModelVariant == "" {
				row.ModelVariant = st.AgentType
			}
		}
		// PaneStatus is the dashboard's presentation state and carries derived
		// states such as rate_limited and compacted that AgentStatus intentionally
		// represents as their underlying error/activity state.
		if ps.State != "" {
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

	lookup := make(map[string]string, len(panes)*2)
	for _, p := range panes {
		key := paneKey(p)
		if p.Title != "" {
			lookup[p.Title] = key
		}
		if p.ID != "" {
			lookup[p.ID] = key
		}
	}

	for _, ch := range changes {
		matched := make(map[string]struct{}, len(ch.Agents))
		for _, agent := range ch.Agents {
			if key, ok := lookup[agent]; ok {
				if _, seen := matched[key]; seen {
					continue
				}
				counts[key]++
				matched[key] = struct{}{}
			}
		}
	}

	return counts
}

func paneKey(pane tmux.Pane) string {
	if pane.Title != "" {
		return pane.Title
	}
	return pane.ID
}

// paneStatusKey returns the stable physical identity used by all dashboard
// state. Real tmux panes are keyed by %ID, so moving a pane between windows
// does not discard or misattribute its status. Synthetic test/preview panes
// without an ID fall back to their explicit window.pane address.
func paneStatusKey(pane tmux.Pane) string {
	return pane.Ref().StableKey()
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

// velocitySample stores the previous token count for a pane and the wall-clock
// time it was observed, so the next sample can derive a genuine fresh-token rate
// from the delta. It lives in the persistent dashboard Model (keyed by pane ID),
// NOT recomputed per render.
type velocitySample struct {
	tokens    int64
	sampledAt time.Time
}

// statusTokenCount returns the best available current cumulative token count for
// a status. It PREFERS the parsed agent metric (TokensUsed, a monotonic counter
// scraped from the agent's own TUI) over a screen-estimate, because TokensUsed
// reflects real consumption rather than the size of the last captured snapshot.
// When TokensUsed is unavailable (0 — not parsed for this agent/output), it
// falls back to estimating tokens from the last captured output. Either way the
// caller treats this as a count and rates are computed as a DELTA over a window,
// never as count/age.
func statusTokenCount(st status.AgentStatus) int64 {
	if st.TokensUsed > 0 {
		return st.TokensUsed
	}
	if st.LastOutput == "" {
		return 0
	}
	return int64(tokens.EstimateTokens(st.LastOutput))
}

// tokenVelocityRate computes a fresh-token rate (tokens/minute) from the delta
// between a previously sampled token count and the current one over the elapsed
// wall-clock window. It is the pure, testable core of the velocity calculation.
//
// Guards (all return 0 rather than a spurious spike):
//   - hasPrev == false: no prior sample yet (first observation of this pane).
//   - elapsed window <= 0: zero/negative duration (clock skew, duplicate sample).
//   - tokensNow < prevTokens: the count shrank (scroll-off, snapshot truncation,
//     or a fresh/compacted session) — never report a negative or fabricated rate.
//
// The result is the genuine growth (tokensNow - prevTokens) divided by the
// minutes between samples, so an idle pane (count unchanged) reads exactly 0 and
// only real new tokens move the needle.
func tokenVelocityRate(prev velocitySample, hasPrev bool, tokensNow int64, now time.Time) float64 {
	if !hasPrev {
		return 0
	}
	if tokensNow < prev.tokens {
		return 0
	}
	minutes := now.Sub(prev.sampledAt).Minutes()
	if minutes <= 0 {
		return 0
	}
	delta := tokensNow - prev.tokens
	if delta <= 0 {
		return 0
	}
	return float64(delta) / minutes
}

func activityLabelAndColor(state string, t theme.Theme) (string, lipgloss.Color) {
	switch strings.ToLower(state) {
	case "working":
		return "WORK", t.Green
	case "idle":
		return "IDLE", t.Yellow
	case "error":
		return "ERR", t.Red
	case "compacted":
		return "CMP", t.Peach
	case "rate_limited":
		return "RATE", t.Maroon
	default:
		return "UNK", t.Overlay
	}
}

func activityBadge(state string, t theme.Theme) string {
	label, color := activityLabelAndColor(state, t)
	if label == "" {
		return ""
	}
	return styles.TextBadge(label, color, t.Base, styles.BadgeOptions{
		Style:    styles.BadgeStyleCompact,
		Bold:     true,
		ShowIcon: false,
	})
}

func activityCountBadge(state string, count int, t theme.Theme) string {
	if count <= 0 {
		return ""
	}
	label, color := activityLabelAndColor(state, t)
	if label == "" {
		return ""
	}
	return styles.TextBadge(fmt.Sprintf("%s %d", label, count), color, t.Base, styles.BadgeOptions{
		Style:    styles.BadgeStyleCompact,
		Bold:     true,
		ShowIcon: false,
	})
}

// BuildPaneTableRow aggregates pane metadata into a single row structure.
// Beads/FileChanges/TokenVelocity are best-effort enrichments and may be empty
// when upstream data is unavailable.
func BuildPaneTableRow(pane tmux.Pane, ps PaneStatus, beads []bv.BeadPreview, fileChanges []tracker.RecordedFileChange) PaneTableRow {
	row := PaneTableRow{
		Index:          pane.Index,
		Type:           string(pane.Type),
		Variant:        pane.Variant,
		ModelVariant:   pane.Variant,
		Title:          pane.Title,
		Status:         ps.State,
		ContextPct:     ps.ContextPercent,
		ContextHistory: append([]float64(nil), ps.ContextHistory...),
		Model:          ps.ContextModel,
		Command:        pane.Command,
		IsCompacted:    ps.State == "compacted",
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
	row.FileChanges = fileChangesByPane([]tmux.Pane{pane}, fileChanges)[paneKey(pane)]

	// Approximate token velocity using recent command text as a proxy.
	if pane.Command != "" {
		row.TokenVelocity = float64(tokens.EstimateTokens(pane.Command))
	}

	return row
}

// RenderPaneRow renders a single pane as a table row with progressive columns
func RenderPaneRow(row PaneTableRow, dims LayoutDimensions, t theme.Theme) string {
	var parts []string

	// Per-agent colored border indicator (pulses when working)
	borderColor := row.BorderColor
	if borderColor == "" {
		borderColor = AgentBorderColor(row.Type, t)
	}
	if row.Status == "working" && row.Tick > 0 {
		borderColor = styles.Pulse(string(borderColor), row.Tick)
	}
	borderStyle := lipgloss.NewStyle().Foreground(borderColor).Bold(true)
	parts = append(parts, borderStyle.Render("▌"))

	// Selection indicator
	selectStyle := lipgloss.NewStyle().Foreground(t.Pink).Bold(true)
	if row.IsSelected {
		parts = append(parts, selectStyle.Render("▸"))
	} else {
		parts = append(parts, " ")
	}

	// Index badge
	idxStyle := lipgloss.NewStyle().Foreground(t.Overlay)
	address := row.Address
	if address == "" {
		address = fmt.Sprintf("%d", row.Index)
	}
	parts = append(parts, idxStyle.Render(fmt.Sprintf("%2s", address)))

	typeColor, typeIcon := agentRowTypePresentation(row.Type, t)

	// Apply pulse animation when agent is actively working
	if row.Status == "working" && row.Tick > 0 {
		typeColor = styles.Pulse(string(typeColor), row.Tick)
	}

	typeStyle := lipgloss.NewStyle().Foreground(typeColor).Bold(true)
	parts = append(parts, typeStyle.Render(typeIcon))

	// Status indicator (always shown except mobile)
	if dims.ShowStatusCol {
		statusStyle := lipgloss.NewStyle()
		var statusIcon string
		switch row.Status {
		case "working":
			// Animated spinner for working state
			statusIcon = WorkingSpinnerFrame(row.Tick)
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

	// Health indicator from process_triage (useful/waiting=green, idle=yellow, stuck=red, zombie=black)
	if dims.ShowStatusCol {
		healthStyle := lipgloss.NewStyle()
		var healthIcon string
		switch row.HealthClass {
		case pt.ClassUseful, pt.ClassWaiting:
			healthIcon = "🟢"
			healthStyle = healthStyle.Foreground(t.Green)
		case pt.ClassIdle:
			// Show yellow if idle for >5min
			if !row.HealthSince.IsZero() && time.Since(row.HealthSince) > 5*time.Minute {
				healthIcon = "🟡"
				healthStyle = healthStyle.Foreground(t.Yellow)
			} else {
				healthIcon = "🟢"
				healthStyle = healthStyle.Foreground(t.Green)
			}
		case pt.ClassStuck:
			healthIcon = "🔴"
			healthStyle = healthStyle.Foreground(t.Red)
		case pt.ClassZombie:
			healthIcon = "⚫"
			healthStyle = healthStyle.Foreground(t.Overlay)
		default:
			// Unknown - show subtle dot
			healthIcon = "·"
			healthStyle = healthStyle.Foreground(t.Surface1)
		}
		parts = append(parts, healthStyle.Render(healthIcon))
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
	if lipgloss.Width(title) > titleWidth {
		// Use smart truncation that preserves the agent suffix (e.g., __cc_1)
		// so panes with the same project prefix remain visually distinguishable
		title = layout.TruncatePaneTitle(title, titleWidth)
	}
	titleStyle := lipgloss.NewStyle().Foreground(t.Text)
	if row.IsSelected {
		titleStyle = titleStyle.Bold(true)
	}
	parts = append(parts, titleStyle.Width(titleWidth).Render(title))

	// Context bar (tablet and up) [tui-upgrade: bd-3btd6]
	if dims.ShowContextCol {
		contextBar := RenderContextMiniBarWithHistory(row.ContextPct, row.ContextHistory, 10, row.Tick, t)
		parts = append(parts, contextBar)
	}

	// Model variant (desktop and up)
	modelVariant := row.Variant
	if modelVariant == "" {
		modelVariant = row.ModelVariant
	}

	if dims.ShowModelCol && modelVariant != "" {
		badge := styles.ModelBadge(modelVariant, styles.BadgeOptions{
			Style:      styles.BadgeStyleCompact,
			Bold:       false,
			ShowIcon:   false,
			FixedWidth: styles.ModelBadgeWidth,
		})
		parts = append(parts, badge)
	} else if dims.ShowModelCol {
		parts = append(parts, strings.Repeat(" ", styles.ModelBadgeWidth))
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
	if dims.Mode >= LayoutWide && (row.CurrentBead != "" || row.FileChanges > 0 || row.TokenVelocity > 0 || row.LocalTokensPS > 0 || row.LocalMemoryBytes > 0 || len(row.ContextHistory) >= 3) {
		var subParts []string

		// Indent to align with title (approx 8 chars: sel(1)+space+idx(2)+icon(1)+status(1)+spaces)
		indent := "        "

		if badge := activityBadge(row.Status, t); badge != "" {
			subParts = append(subParts, badge)
		}
		if len(row.ContextHistory) >= 3 {
			subParts = append(subParts, components.SparklineWithLabel("ctx", row.ContextHistory, 18, fmt.Sprintf("%.0f%%", row.ContextPct)))
		}
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

		if row.LocalTokensPS > 0 {
			subParts = append(subParts, styles.TokensPerSecondBadge(row.LocalTokensPS, styles.BadgeOptions{
				Style:    styles.BadgeStyleCompact,
				Bold:     false,
				ShowIcon: true,
			}))
		}

		if row.LocalMemoryBytes > 0 {
			subParts = append(subParts, styles.MemoryUsageBadge(row.LocalMemoryBytes, styles.BadgeOptions{
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

func agentRowTypePresentation(agentType string, t theme.Theme) (lipgloss.Color, string) {
	switch tmux.AgentType(agentType).Canonical() {
	case tmux.AgentClaude:
		return t.Claude, "󰗣"
	case tmux.AgentCodex:
		return t.Codex, "󰘦"
	case tmux.AgentGemini:
		return t.Gemini, "󰇮"
	case tmux.AgentGrok:
		return t.Pink, "󰚩"
	case tmux.AgentAntigravity:
		return t.Lavender, "󰇮"
	default:
		return t.Green, "󰄛"
	}
}

// truncate shortens a string to maxLen with ellipsis.
// Uses the standard single-character ellipsis "…" (U+2026).
func truncate(s string, maxLen int) string {
	return layout.TruncateWidthDefault(s, maxLen)
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
	parts = append(parts, " ") // Border indicator placeholder (matches row's "▌")
	parts = append(parts, " ") // Selection column
	parts = append(parts, headerStyle.Width(2).Render("#"))
	parts = append(parts, headerStyle.Width(1).Render("T")) // Width(1) to match row's icon

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

	// Add hidden column indicator when columns are hidden due to narrow width
	if dims.HiddenColCount > 0 {
		hiddenIndicator := lipgloss.NewStyle().
			Foreground(t.Overlay).
			Italic(true).
			Render(fmt.Sprintf("+%d hidden", dims.HiddenColCount))
		parts = append(parts, hiddenIndicator)
	}

	return strings.Join(parts, " ")
}

// workingSpinnerFrames defines the animation frames for working state spinner
var workingSpinnerFrames = []string{"◐", "◓", "◑", "◒"}

// WorkingSpinnerFrame returns the spinner frame for agents in working state.
// The spinner animates through circular segments to indicate active processing.
func WorkingSpinnerFrame(tick int) string {
	return workingSpinnerFrames[tick%len(workingSpinnerFrames)]
}

// AgentBorderColor returns the theme color for a given agent type.
// Each agent type has a unique color: Claude=purple/Mauve, Codex=blue, Gemini=yellow, Grok=pink, User=green.
func AgentBorderColor(agentType string, t theme.Theme) lipgloss.Color {
	switch tmux.AgentType(agentType).Canonical() {
	case tmux.AgentClaude:
		return t.Claude
	case tmux.AgentCodex:
		return t.Codex
	case tmux.AgentGemini:
		return t.Gemini
	case tmux.AgentGrok:
		return t.Pink
	case tmux.AgentAntigravity:
		return t.Lavender
	case tmux.AgentCursor:
		return t.Claude
	case tmux.AgentWindsurf:
		return t.Codex
	case tmux.AgentAider:
		return t.Gemini
	case tmux.AgentOllama:
		// Fallback to a distinct color if missing from theme,
		// but typically we'd use t.Text if we don't have a specific color
		return t.Text
	case tmux.AgentUser:
		return t.User
	default:
		return t.Text
	}
}
