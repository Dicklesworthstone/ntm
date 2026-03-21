package dashboard

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/components"
	"github.com/Dicklesworthstone/ntm/internal/tui/dashboard/panels"
	"github.com/Dicklesworthstone/ntm/internal/tui/layout"
	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// View implements tea.Model
func (m Model) View() string {
	if m.showHelp {
		// Use bubbles/help with FullHelp for the help overlay.
		// This replaces the manual DashboardHelpSections call.
		helpCopy := m.helpModel
		helpCopy.ShowAll = true
		helpCopy.Width = 56 // Content width within the 60-char box

		// Section labels for FullHelp columns
		sectionLabels := []string{"Navigation", "Panels", "Data", "Control"}
		labelStyle := lipgloss.NewStyle().
			Foreground(m.theme.Primary).
			Bold(true)

		// Build styled section labels
		styledLabels := make([]string, len(sectionLabels))
		for i, label := range sectionLabels {
			styledLabels[i] = labelStyle.Render(label)
		}

		var helpLines []string
		// Add section labels header
		helpLines = append(helpLines, strings.Join(styledLabels, "    "))

		// Render the keybindings
		helpContent := helpCopy.View(dashKeys)
		helpLines = append(helpLines, helpContent)

		fullContent := strings.Join(helpLines, "\n")

		// Append panel-specific keys if a panel is focused.
		if panelHints := m.getFocusedPanelHints(); len(panelHints) > 0 {
			var hintLines []string
			for _, hint := range panelHints {
				hintLines = append(hintLines, hint.Key+" "+hint.Desc)
			}
			fullContent += "\n\n" + lipgloss.NewStyle().
				Foreground(m.theme.Subtext).
				Render("Panel: "+strings.Join(hintLines, " • "))
		}

		// Wrap in styled box using Catppuccin theme.
		helpOverlay := m.renderHelpOverlayBox("Dashboard Shortcuts", fullContent, 60)
		backdrop := m.renderHeaderSection() + m.renderMainContentSection() + m.renderFooterSection()
		return renderModalOverlay(backdrop, helpOverlay, m.width, m.height, m.theme)
	}

	if m.showCassSearch {
		searchView := m.cassSearch.View()
		modalStyle := lipgloss.NewStyle().
			Border(lipgloss.DoubleBorder()).
			BorderForeground(m.theme.Primary).
			Background(m.theme.Base).
			Padding(1, 2)
		modal := modalStyle.Render(searchView)
		backdrop := m.renderHeaderSection() + m.renderMainContentSection() + m.renderFooterSection()
		return renderModalOverlay(backdrop, modal, m.width, m.height, m.theme)
	}

	if m.showEnsembleModes {
		modesView := m.ensembleModes.View()
		modalStyle := lipgloss.NewStyle().
			Border(lipgloss.DoubleBorder()).
			BorderForeground(m.theme.Primary).
			Background(m.theme.Base).
			Padding(1, 2)
		modal := modalStyle.Render(modesView)
		backdrop := m.renderHeaderSection() + m.renderMainContentSection() + m.renderFooterSection()
		return renderModalOverlay(backdrop, modal, m.width, m.height, m.theme)
	}

	// [tui-upgrade: bd-uz09d] Render spawn wizard overlay
	if m.showSpawnWizard && m.spawnWizard != nil {
		m.spawnWizard.SetSize(m.width, m.height)
		modal := m.spawnWizard.View()
		backdrop := m.renderHeaderSection() + m.renderMainContentSection() + m.renderFooterSection()
		return renderModalOverlay(backdrop, modal, m.width, m.height, m.theme)
	}

	if m.showToastHistory {
		modalWidth := m.width - 8
		if modalWidth > 72 {
			modalWidth = 72
		}
		if modalWidth < 36 {
			modalWidth = 36
		}
		modal := m.renderHelpOverlayBox("Toast History", m.renderToastHistoryContent(), modalWidth)
		backdrop := m.renderHeaderSection() + m.renderMainContentSection() + m.renderFooterSection()
		return renderModalOverlay(backdrop, modal, m.width, m.height, m.theme)
	}

	header := m.renderHeaderSection()
	footer := m.renderFooterSection()
	headerHeight := lipgloss.Height(header)
	footerHeight := lipgloss.Height(footer)
	toastMetrics := m.toastRenderMetrics(headerHeight, footerHeight)
	content := m.renderMainContentSection()

	if m.height > 0 {
		available := m.contentHeightBudget(headerHeight, footerHeight, toastMetrics.height)
		if !m.focusedPanelHandlesOwnHeight() {
			// Truncate content to fit within available height.
			// lipgloss Height/MaxHeight don't truncate - they're CSS-like properties.
			content = truncateToHeight(content, available)
		}
		// Apply height style to ensure consistent spacing
		content = lipgloss.NewStyle().Height(available).MaxHeight(available).Render(content)
	}

	// Render toast notifications inline, right-aligned above the footer.
	// We avoid pixel-level overlay (placeOverlay) because ANSI escape sequences
	// in lipgloss-rendered text make rune-based splicing corrupt the output.
	toastSection := ""
	if toastMetrics.section != "" {
		toastSection = toastMetrics.section + "\n"
	}

	return header + content + toastSection + footer
}

type toastRenderMetrics struct {
	section string
	left    int
	width   int
	height  int
}

func (m Model) contentHeightBudget(headerHeight, footerHeight, toastHeight int) int {
	available := m.height - headerHeight - footerHeight - toastHeight
	if available < 1 {
		available = 1
	}
	return available
}

func (m Model) toastRenderMetrics(headerHeight, footerHeight int) toastRenderMetrics {
	if m.toasts == nil || m.toasts.Count() == 0 || m.width <= 0 {
		return toastRenderMetrics{}
	}

	toastStr := m.toasts.RenderToasts(m.width / 3)
	if toastStr == "" {
		return toastRenderMetrics{}
	}

	height := lipgloss.Height(toastStr)
	width := lipgloss.Width(toastStr)
	left := m.width - width - 2
	if left < 0 {
		left = 0
	}

	return toastRenderMetrics{
		section: lipgloss.NewStyle().
			PaddingLeft(left).
			Render(toastStr),
		left:   left,
		width:  width,
		height: height,
	}
}

func (m Model) renderHeaderSection() string {
	t := m.theme

	var b strings.Builder
	b.WriteString("\n")

	// ═══════════════════════════════════════════════════════════════
	// HEADER — left-aligned block, centered as a unit
	//
	// All header elements share a common left edge. The block width
	// is the widest element; the block is horizontally centered in
	// the terminal. This avoids the "staircase" that results from
	// independently centering lines of different widths.
	// ═══════════════════════════════════════════════════════════════

	// 1. Build all header lines (plain + ANSI-decorated)
	bannerText := components.RenderBannerMedium(true, m.animTick)
	bannerLines := strings.Split(bannerText, "\n")

	sessionTitle := m.icons.Session + "  " + m.session
	animatedSession := styles.Shimmer(sessionTitle, m.animTick,
		string(t.Blue), string(t.Lavender), string(t.Mauve))

	contextLine := m.renderHeaderContextLine(m.width)
	handoffLine := m.renderHeaderHandoffLine(m.width)
	contextWarnLine := m.renderHeaderContextWarningLine(m.width)

	// 2. Find the widest element to size the centered block
	blockWidth := lipgloss.Width(animatedSession)
	if w := lipgloss.Width(contextLine); w > blockWidth {
		blockWidth = w
	}
	if w := lipgloss.Width(handoffLine); w > blockWidth {
		blockWidth = w
	}
	if w := lipgloss.Width(contextWarnLine); w > blockWidth {
		blockWidth = w
	}
	for _, bl := range bannerLines {
		if w := lipgloss.Width(bl); w > blockWidth {
			blockWidth = w
		}
	}

	// 3. Calculate left margin to center the block
	blockLeft := (m.width - blockWidth) / 2
	if blockLeft < 0 {
		blockLeft = 0
	}
	pad := strings.Repeat(" ", blockLeft)

	// 4. Render: all elements left-aligned within the centered block
	for _, bl := range bannerLines {
		b.WriteString(pad + bl + "\n")
	}
	b.WriteString(pad + animatedSession + "\n")
	if contextLine != "" {
		b.WriteString(pad + contextLine + "\n")
	}
	if handoffLine != "" {
		b.WriteString(pad + handoffLine + "\n")
	}
	if contextWarnLine != "" {
		b.WriteString(pad + contextWarnLine + "\n")
	}
	// [tui-upgrade: bd-28vsw] Animated header divider
	b.WriteString(styles.AnimatedGradientDivider(m.width, m.animTick,
		string(t.Blue), string(t.Mauve)) + "\n\n")

	// ═══════════════════════════════════════════════════════════════
	// STATS BAR with agent counts
	// ═══════════════════════════════════════════════════════════════
	statsBar := m.renderStatsBar()
	b.WriteString(styles.CenterText(statsBar, m.width) + "\n\n")

	if m.showDiagnostics {
		diagWidth := m.width - 4
		if diagWidth < 20 {
			diagWidth = 20
		}
		b.WriteString(m.renderDiagnosticsBar(diagWidth) + "\n\n")
	}

	// ═══════════════════════════════════════════════════════════════
	// RATE LIMIT ALERT (if any agent is rate limited)
	// ═══════════════════════════════════════════════════════════════
	if alert := m.renderRateLimitAlert(); alert != "" {
		b.WriteString(alert + "\n\n")
	}

	return b.String()
}

func (m Model) renderMainContentSection() string {
	var b strings.Builder

	// ═══════════════════════════════════════════════════════════════
	// PANE GRID VISUALIZATION
	// ═══════════════════════════════════════════════════════════════
	stateWidth := m.width - 4
	if stateWidth < 20 {
		stateWidth = 20
	}

	if len(m.panes) == 0 {
		if m.err != nil {
			b.WriteString(components.ErrorState(m.err.Error(), hintForSessionFetchError(m.err), stateWidth) + "\n")
		} else if m.fetchingSession {
			message := "Fetching panes…"
			if !m.lastPaneFetch.IsZero() {
				elapsed := time.Since(m.lastPaneFetch).Round(100 * time.Millisecond)
				if elapsed > 0 {
					message = fmt.Sprintf("Fetching panes… (%s)", elapsed)
				}
			}
			b.WriteString(components.LoadingState(message, stateWidth) + "\n")
		} else {
			b.WriteString(components.RenderEmptyState(components.EmptyStateOptions{
				Icon:        components.IconEmpty,
				Title:       "No panes found",
				Description: "Session has no active panes",
				Width:       stateWidth,
				Centered:    true,
			}) + "\n")
		}
	} else {
		if m.err != nil {
			b.WriteString(components.ErrorState(m.err.Error(), hintForSessionFetchError(m.err), stateWidth) + "\n\n")
		}
		// Responsive layout selection, gated by help verbosity.
		// Minimal mode shows only core panels (activity/status) even on wide terminals.
		if m.dashboardHelpOptions().Verbosity == components.DashboardHelpVerbosityMinimal {
			if m.tier >= layout.TierSplit {
				b.WriteString(m.renderSplitView() + "\n")
			} else {
				b.WriteString(m.renderPaneGrid() + "\n")
			}
		} else {
			switch {
			case m.tier >= layout.TierMega:
				b.WriteString(m.renderMegaLayout() + "\n")
			case m.tier >= layout.TierUltra:
				b.WriteString(m.renderUltraLayout() + "\n")
			case m.tier >= layout.TierSplit:
				b.WriteString(m.renderSplitView() + "\n")
			default:
				b.WriteString(m.renderPaneGrid() + "\n")
			}
		}
	}

	return b.String()
}

// renderPanelTabBar renders a tab bar showing the visible panels and the focused one.
func (m Model) renderPanelTabBar(width int) string {
	visible := m.visiblePanelsForHelpVerbosity()

	var tabs []components.Tab
	for _, pid := range visible {
		idStr := panelIDString(pid)
		badge := 0
		hasErr := false

		// Set notification badges for panels with updates
		switch pid {
		case PanelAlerts:
			badge = len(m.activeAlerts)
			hasErr = m.alertsPanel.HasError()
		case PanelAttention:
			if m.attentionPanel != nil {
				badge = m.attentionPanel.ActionRequiredCount()
			}
		case PanelBeads:
			badge = m.beadsSummary.InProgress
			hasErr = m.beadsPanel.HasError()
		case PanelConflicts:
			badge = m.conflictsPanel.ConflictCount()
		}

		tabs = append(tabs, components.PanelIDToTab(idStr, badge, hasErr))
	}

	activeID := panelIDString(m.focusedPanel)

	// [tui-upgrade: bd-28vsw] Pass tick for shimmer animation
	return components.RenderTabBar(components.TabBarOptions{
		Tabs:       tabs,
		ActiveID:   activeID,
		Width:      width,
		Focused:    true,
		ShowBadges: true,
		Tick:       m.animTick,
	})
}

func (m Model) renderFooterSection() string {
	t := m.theme

	var b strings.Builder

	// ═══════════════════════════════════════════════════════════════
	// TICKER BAR (scrolling status summary)
	// ═══════════════════════════════════════════════════════════════
	b.WriteString("\n")
	m.tickerPanel.SetSize(m.width-4, 1)
	b.WriteString("  " + m.tickerPanel.View() + "\n")

	// ═══════════════════════════════════════════════════════════════
	// QUICK ACTIONS BAR (width-gated, only in wide+ modes)
	// ═══════════════════════════════════════════════════════════════
	if quickActions := m.renderQuickActions(); quickActions != "" {
		b.WriteString("  " + quickActions + "\n")
	}

	// ═══════════════════════════════════════════════════════════════
	// STATUS BAR (session info, focused panel, layout tier)
	// ═══════════════════════════════════════════════════════════════
	b.WriteString(components.RenderStatusBar(components.StatusBarOptions{
		Width:           m.width,
		Session:         m.session,
		ClaudeCount:     m.claudeCount,
		CodexCount:      m.codexCount,
		GeminiCount:     m.geminiCount,
		UserCount:       m.userCount,
		FocusedPanel:    panelIDString(m.focusedPanel),
		LayoutTier:      tierLabel(m.tier),
		Paused:          m.refreshPaused,
		CurrentVelocity: m.currentAggregateVelocity(),
		VelocityHistory: m.aggregateVelocityHistory,
	}) + "\n")

	// ═══════════════════════════════════════════════════════════════
	// HELP BAR
	// ═══════════════════════════════════════════════════════════════
	b.WriteString("  " + styles.GradientDivider(m.width-4,
		string(t.Surface2), string(t.Surface1)) + "\n")
	b.WriteString("  " + m.renderHelpBar() + "\n")

	return b.String()
}

func (m Model) renderStatsBar() string {
	t := m.theme
	ic := m.icons

	var parts []string

	// Help verbosity indicator (minimal/full)
	helpLabel := "Help: " + normalizedHelpVerbosity(m.helpVerbosity)
	helpBadge := lipgloss.NewStyle().
		Background(t.Surface0).
		Foreground(t.Subtext).
		Padding(0, 1).
		Render(helpLabel)
	parts = append(parts, helpBadge)

	// Overlay mode badge — color reflects attention state
	if m.popupMode {
		overlayColor := t.Teal
		overlayIcon := "◉"
		overlayLabel := "overlay"

		if m.attentionPanel != nil && m.attentionFeedOK {
			actionCount := m.attentionPanel.ActionRequiredCount()
			interestingCount := m.attentionPanel.InterestingCount()
			if actionCount > 0 {
				overlayColor = t.Red
				overlayLabel = fmt.Sprintf("overlay ● %d", actionCount)
			} else if interestingCount > 0 {
				overlayColor = t.Yellow
				overlayLabel = fmt.Sprintf("overlay ▲ %d", interestingCount)
			}
		}

		overlayBadge := lipgloss.NewStyle().
			Background(overlayColor).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render(fmt.Sprintf("%s %s", overlayIcon, overlayLabel))
		parts = append(parts, overlayBadge)
	}

	if healthBadge := m.renderHealthBadge(); healthBadge != "" {
		parts = append(parts, healthBadge)
	}

	totalBadge := lipgloss.NewStyle().
		Background(t.Surface0).
		Foreground(t.Text).
		Padding(0, 1).
		Render(fmt.Sprintf("%s %d panes", ic.Pane, len(m.panes)))
	parts = append(parts, totalBadge)

	if m.claudeCount > 0 {
		parts = append(parts, lipgloss.NewStyle().
			Background(t.Claude).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render(fmt.Sprintf("%s %d", ic.Claude, m.claudeCount)))
	}
	if m.codexCount > 0 {
		parts = append(parts, lipgloss.NewStyle().
			Background(t.Codex).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render(fmt.Sprintf("%s %d", ic.Codex, m.codexCount)))
	}
	if m.geminiCount > 0 {
		parts = append(parts, lipgloss.NewStyle().
			Background(t.Gemini).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render(fmt.Sprintf("%s %d", ic.Gemini, m.geminiCount)))
	}
	if m.cursorCount > 0 {
		parts = append(parts, lipgloss.NewStyle().
			Background(t.Success).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render(fmt.Sprintf("%s %d", ic.Robot, m.cursorCount)))
	}
	if m.windsurfCount > 0 {
		parts = append(parts, lipgloss.NewStyle().
			Background(t.Success).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render(fmt.Sprintf("%s %d", ic.Robot, m.windsurfCount)))
	}
	if m.aiderCount > 0 {
		parts = append(parts, lipgloss.NewStyle().
			Background(t.Success).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render(fmt.Sprintf("%s %d", ic.Robot, m.aiderCount)))
	}
	if m.userCount > 0 {
		parts = append(parts, lipgloss.NewStyle().
			Background(t.Green).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render(fmt.Sprintf("%s %d", ic.User, m.userCount)))
	}
	if scanBadge := m.renderScanBadge(); scanBadge != "" {
		parts = append(parts, scanBadge)
	}
	if mailBadge := m.renderAgentMailBadge(); mailBadge != "" {
		parts = append(parts, mailBadge)
	}
	if cpBadge := m.renderCheckpointBadge(); cpBadge != "" {
		parts = append(parts, cpBadge)
	}
	if dcgBadge := m.renderDCGBadge(); dcgBadge != "" {
		parts = append(parts, dcgBadge)
	}
	if !m.popupMode {
		if attentionBadge := m.renderAttentionBadge(); attentionBadge != "" {
			parts = append(parts, attentionBadge)
		}
	}

	return strings.Join(parts, "  ")
}

func (m Model) renderHealthBadge() string {
	t := m.theme

	if m.healthStatus == "" || m.healthStatus == "unknown" {
		return ""
	}

	var bgColor, fgColor lipgloss.Color
	var icon, label string
	switch m.healthStatus {
	case "ok":
		bgColor, fgColor, icon, label = t.Green, t.Base, "✓", "healthy"
	case "warning":
		bgColor, fgColor, icon, label = t.Yellow, t.Base, "⚠", "drift"
	case "critical":
		bgColor, fgColor, icon, label = t.Red, t.Base, "✗", "critical"
	case "no_baseline":
		bgColor, fgColor, icon, label = t.Surface1, t.Overlay, "?", "no baseline"
	case "unavailable":
		return ""
	default:
		return ""
	}

	return lipgloss.NewStyle().
		Background(bgColor).
		Foreground(fgColor).
		Bold(true).
		Padding(0, 1).
		Render(fmt.Sprintf("%s %s", icon, label))
}

func (m Model) renderScanBadge() string {
	t := m.theme

	if m.scanStatus == "" || m.scanStatus == "unavailable" {
		return ""
	}

	var bgColor, fgColor lipgloss.Color
	var icon, label string
	switch m.scanStatus {
	case "clean":
		bgColor, fgColor, icon, label = t.Green, t.Base, "✓", "scan clean"
	case "warning":
		bgColor, fgColor = t.Yellow, t.Base
		icon = "⚠"
		label = fmt.Sprintf("scan %d warn", m.scanTotals.Warning)
	case "critical":
		bgColor, fgColor = t.Red, t.Base
		icon = "✗"
		label = fmt.Sprintf("scan %d crit", m.scanTotals.Critical)
	case "error":
		bgColor, fgColor, icon, label = t.Surface1, t.Overlay, "?", "scan error"
	default:
		return ""
	}

	if m.scanStatus == "clean" && (m.scanTotals.Critical+m.scanTotals.Warning+m.scanTotals.Info) > 0 {
		label = fmt.Sprintf("scan %d/%d/%d", m.scanTotals.Critical, m.scanTotals.Warning, m.scanTotals.Info)
	}

	return lipgloss.NewStyle().
		Background(bgColor).
		Foreground(fgColor).
		Bold(true).
		Padding(0, 1).
		Render(fmt.Sprintf("%s %s", icon, label))
}

func (m Model) renderAgentMailBadge() string {
	t := m.theme

	if !m.agentMailAvailable {
		return ""
	}

	var bgColor, fgColor lipgloss.Color
	var icon, label string
	if m.agentMailConnected {
		if m.agentMailLocks > 0 {
			bgColor, fgColor = t.Lavender, t.Base
			icon = "🔒"
			label = fmt.Sprintf("%d locks", m.agentMailLocks)
		} else {
			bgColor, fgColor = t.Surface1, t.Text
			icon = "📬"
			label = "mail"
		}
	} else {
		bgColor, fgColor, icon, label = t.Yellow, t.Base, "📭", "offline"
	}

	return lipgloss.NewStyle().
		Background(bgColor).
		Foreground(fgColor).
		Bold(true).
		Padding(0, 1).
		Render(fmt.Sprintf("%s %s", icon, label))
}

func (m Model) renderCheckpointBadge() string {
	t := m.theme

	if m.checkpointCount == 0 || m.checkpointStatus == "" || m.checkpointStatus == "none" {
		return ""
	}

	var bgColor, fgColor lipgloss.Color
	var icon, label string
	switch m.checkpointStatus {
	case "recent":
		bgColor, fgColor = t.Green, t.Base
		icon = "💾"
		label = fmt.Sprintf("%d ckpt", m.checkpointCount)
	case "stale":
		bgColor, fgColor = t.Yellow, t.Base
		icon = "💾"
		label = fmt.Sprintf("%d stale", m.checkpointCount)
	case "old":
		bgColor, fgColor = t.Surface1, t.Overlay
		icon = "💾"
		label = fmt.Sprintf("%d old", m.checkpointCount)
	default:
		return ""
	}

	return lipgloss.NewStyle().
		Background(bgColor).
		Foreground(fgColor).
		Bold(true).
		Padding(0, 1).
		Render(fmt.Sprintf("%s %s", icon, label))
}

func (m Model) renderDCGBadge() string {
	t := m.theme

	if !m.dcgEnabled {
		return ""
	}

	var bgColor, fgColor lipgloss.Color
	var icon, label string
	if !m.dcgAvailable {
		bgColor, fgColor, icon, label = t.Yellow, t.Base, "⚠", "DCG missing"
	} else if m.dcgBlocked > 0 {
		bgColor, fgColor = t.Lavender, t.Base
		icon = "🛡️"
		label = fmt.Sprintf("DCG %d blocked", m.dcgBlocked)
	} else {
		bgColor, fgColor, icon, label = t.Green, t.Base, "🛡️", "DCG"
	}

	return lipgloss.NewStyle().
		Background(bgColor).
		Foreground(fgColor).
		Bold(true).
		Padding(0, 1).
		Render(fmt.Sprintf("%s %s", icon, label))
}

// renderAttentionBadge renders a compact attention state badge for the stats bar.
// Shows action_required count in red, interesting count in yellow, or nothing if all clear.
func (m Model) renderAttentionBadge() string {
	if m.attentionPanel == nil || !m.attentionFeedOK {
		return ""
	}

	actionCount := m.attentionPanel.ActionRequiredCount()
	interestingCount := m.attentionPanel.InterestingCount()
	if actionCount == 0 && interestingCount == 0 {
		return ""
	}

	t := m.theme
	var parts []string
	if actionCount > 0 {
		parts = append(parts, lipgloss.NewStyle().
			Background(t.Red).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render(fmt.Sprintf("● %d", actionCount)))
	}
	if interestingCount > 0 {
		parts = append(parts, lipgloss.NewStyle().
			Background(t.Yellow).
			Foreground(t.Base).
			Bold(true).
			Padding(0, 1).
			Render(fmt.Sprintf("▲ %d", interestingCount)))
	}

	return strings.Join(parts, " ")
}

func (m Model) renderRateLimitAlert() string {
	t := m.theme

	var rateLimitedPanes []int
	for _, p := range m.panes {
		if ps, ok := m.paneStatus[p.Index]; ok && ps.State == "rate_limited" {
			rateLimitedPanes = append(rateLimitedPanes, p.Index)
		}
	}
	if len(rateLimitedPanes) == 0 {
		return ""
	}

	var msg string
	if len(rateLimitedPanes) == 1 {
		msg = fmt.Sprintf("⏳ Rate limit hit on pane %d! Run: ntm rotate %s --pane=%d",
			rateLimitedPanes[0], m.session, rateLimitedPanes[0])
	} else {
		msg = fmt.Sprintf("⏳ Rate limit hit on panes %v! Press 'r' to rotate", rateLimitedPanes)
	}

	alertStyle := lipgloss.NewStyle().
		Background(t.Maroon).
		Foreground(t.Base).
		Bold(true).
		Padding(0, 2).
		Width(m.width - 6)

	return "  " + alertStyle.Render(msg)
}

// renderContextBar renders a progress bar showing context usage percentage.
// High context (>80%) uses shimmer effect on warning indicators.
func (m Model) renderContextBar(percent float64, width int) string {
	t := m.theme

	barWidth := width - 8
	if barWidth < 5 {
		barWidth = 5
	}

	colors := []string{string(t.Green), string(t.Blue), string(t.Yellow), string(t.Red)}
	barContent := styles.ShimmerProgressBar(percent/100.0, barWidth, "█", "░", m.animTick, colors...)

	percentStyle := lipgloss.NewStyle().Foreground(t.Overlay)

	var warningIcon string
	switch {
	case percent >= 95:
		warningIcon = " " + styles.Shimmer("!!!", m.animTick, string(t.Red), string(t.Maroon), string(t.Red))
	case percent >= 90:
		warningIcon = " " + styles.Shimmer("!!", m.animTick, string(t.Red), string(t.Maroon), string(t.Red))
	case percent >= 80:
		warningIcon = " " + styles.Shimmer("!", m.animTick, string(t.Yellow), string(t.Peach), string(t.Yellow))
	default:
		warningIcon = ""
	}

	return "[" + barContent + "]" + percentStyle.Render(fmt.Sprintf("%3.0f%%", percent)) + warningIcon
}

func formatTokenDisplay(used, limit int) string {
	formatTokens := func(n int) string {
		if n >= 1000000 {
			return fmt.Sprintf("%.1fM", float64(n)/1000000)
		}
		if n >= 1000 {
			return fmt.Sprintf("%.1fK", float64(n)/1000)
		}
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%s / %s", formatTokens(used), formatTokens(limit))
}

func formatRelativeTime(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "now"
	}
	d = d.Round(time.Second)
	if d >= time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	if d >= time.Minute {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

func (m Model) renderDiagnosticsBar(width int) string {
	t := m.theme

	labelStyle := lipgloss.NewStyle().Foreground(t.Subtext).Bold(true)
	valueStyle := lipgloss.NewStyle().Foreground(t.Text)
	warnStyle := lipgloss.NewStyle().Foreground(t.Warning)
	errStyle := lipgloss.NewStyle().Foreground(t.Error)

	sessionPart := valueStyle.Render("ok")
	if m.fetchingSession {
		elapsed := time.Since(m.lastPaneFetch).Round(100 * time.Millisecond)
		sessionPart = warnStyle.Render("fetching " + elapsed.String())
	} else if m.sessionFetchLatency > 0 {
		sessionPart = valueStyle.Render(m.sessionFetchLatency.Round(time.Millisecond).String())
	}
	if m.err != nil {
		sessionPart = errStyle.Render("error")
	}

	statusPart := valueStyle.Render("ok")
	if m.fetchingContext {
		elapsed := time.Since(m.lastContextFetch).Round(100 * time.Millisecond)
		statusPart = warnStyle.Render("fetching " + elapsed.String())
	} else if m.statusFetchLatency > 0 {
		statusPart = valueStyle.Render(m.statusFetchLatency.Round(time.Millisecond).String())
	}
	if m.statusFetchErr != nil {
		statusPart = errStyle.Render("error")
	}

	parts := []string{
		labelStyle.Render("diag"),
		labelStyle.Render("tmux") + ":" + sessionPart,
		labelStyle.Render("status") + ":" + statusPart,
	}
	if width >= 120 {
		age := func(src refreshSource) string {
			t := m.lastUpdated[src]
			if t.IsZero() {
				return "n/a"
			}
			return formatAgeShort(time.Since(t))
		}
		agePart := valueStyle.Render(fmt.Sprintf("panes %s, status %s, beads %s", age(refreshSession), age(refreshStatus), age(refreshBeads)))
		parts = append(parts, labelStyle.Render("age")+":"+agePart)
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Surface1).
		Padding(0, 1).
		Width(width)

	return box.Render(strings.Join(parts, "  "))
}

func hintForSessionFetchError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "tmux is responding slowly. Press r to retry, p to pause auto-refresh, or try running ntm outside of tmux"
	}

	msg := err.Error()
	switch {
	case strings.Contains(msg, "tmux is not installed"):
		return "Install tmux, then run: ntm deps -v"
	case strings.Contains(msg, "executable file not found"):
		return "Install tmux, then run: ntm deps -v"
	case strings.Contains(msg, "no server running"):
		return "Start tmux or create a session with: ntm spawn <name>"
	case strings.Contains(msg, "failed to connect to server"):
		return "Start tmux or create a session with: ntm spawn <name>"
	case strings.Contains(msg, "can't find session"), strings.Contains(msg, "session not found"):
		return "Session may have ended. Create a new one with: ntm spawn <name>"
	}

	return "Press r to retry"
}

func formatAgeShort(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

func (m Model) renderQuickActions() string {
	if m.tier < layout.TierWide {
		return ""
	}

	t := m.theme
	ic := m.icons

	buttonStyle := lipgloss.NewStyle().
		Background(t.Surface1).
		Foreground(t.Text).
		Bold(true).
		Padding(0, 2).
		MarginRight(1)
	disabledButtonStyle := buttonStyle.Foreground(t.Surface2)
	keyHintStyle := lipgloss.NewStyle().
		Foreground(t.Overlay).
		Italic(true)

	type action struct {
		icon    string
		label   string
		key     string
		enabled bool
	}

	hasSelection := m.cursor >= 0 && m.cursor < len(m.panes)
	actions := []action{
		{icon: ic.Palette, label: "Palette", key: "F6", enabled: true},
		{icon: ic.Send, label: "Send", key: "s", enabled: hasSelection},
		{icon: ic.Copy, label: "Copy", key: "y", enabled: hasSelection},
		{icon: ic.Zoom, label: "Zoom", key: "z", enabled: hasSelection},
	}

	var parts []string
	labelStyle := lipgloss.NewStyle().
		Foreground(t.Subtext).
		Bold(true).
		MarginRight(2)
	parts = append(parts, labelStyle.Render("Actions"))

	for _, a := range actions {
		style := buttonStyle
		if !a.enabled {
			style = disabledButtonStyle
		}
		btn := style.Render(a.icon + " " + a.label)
		hint := keyHintStyle.Render(" " + a.key)
		parts = append(parts, btn+hint)
	}

	return strings.Join(parts, " ")
}

func (m Model) renderHelpBar() string {
	opts := m.dashboardHelpOptions()

	baseOpts := opts
	baseOpts.Debug = false
	hints := components.DashboardHelpBarHints(baseOpts)

	if opts.Verbosity != components.DashboardHelpVerbosityMinimal {
		panelHints := m.getFocusedPanelHints()
		for i, hint := range panelHints {
			if i >= 3 {
				break
			}
			hints = append(hints, hint)
		}
	}

	if m.focusedPanel != PanelAttention && m.attentionPanel != nil && m.attentionFeedOK {
		if m.attentionPanel.ActionRequiredCount() > 0 {
			hints = append(hints, components.KeyHint{Key: "Tab", Desc: "attention!"})
		} else if m.attentionPanel.InterestingCount() > 0 {
			hints = append(hints, components.KeyHint{Key: "Tab", Desc: "attention"})
		}
	}

	if opts.Debug {
		hints = append(hints,
			components.KeyHint{Key: "d", Desc: "diag"},
			components.KeyHint{Key: "u", Desc: "scan"},
			components.KeyHint{Key: "ctrl+k", Desc: "checkpoint"},
		)
	}

	if m.toasts != nil && m.toasts.Count() > 0 {
		hints = append(hints, components.KeyHint{Key: "ctrl+x", Desc: "dismiss toast"})
	}
	if m.toasts != nil && m.toasts.HistoryCount() > 0 && m.toastHistoryShortcutAvailable() {
		hints = append(hints, components.KeyHint{Key: "n", Desc: "toast history"})
	}

	return components.RenderHelpBar(components.HelpBarOptions{
		Hints: hints,
		Width: m.width - 4,
	})
}

func (m Model) renderToastHistoryContent() string {
	subtle := lipgloss.NewStyle().Foreground(m.theme.Subtext)
	if m.toasts == nil || m.toasts.HistoryCount() == 0 {
		return subtle.Render("No dismissed toasts yet.\n\nPress ctrl+x to dismiss the newest active toast.")
	}

	var lines []string
	lines = append(lines, subtle.Render("Most recent first. Press n or Esc to close."))
	lines = append(lines, "")

	for _, toast := range m.toasts.RecentHistory(8) {
		lines = append(lines, toastHistoryLine(m.theme, toast))
	}

	if active := m.toasts.Count(); active > 0 {
		lines = append(lines, "")
		lines = append(lines, subtle.Render(fmt.Sprintf("%d active toast(s). ctrl+x dismisses the newest.", active)))
	}

	return strings.Join(lines, "\n")
}

func toastHistoryLine(t theme.Theme, toast components.Toast) string {
	var label string
	var color lipgloss.Color
	switch toast.Level {
	case components.ToastSuccess:
		label, color = "SUCCESS", t.Green
	case components.ToastWarning:
		label, color = "WARN", t.Yellow
	case components.ToastError:
		label, color = "ERROR", t.Red
	default:
		label, color = "INFO", t.Blue
	}

	badge := lipgloss.NewStyle().
		Background(color).
		Foreground(t.Base).
		Bold(true).
		Padding(0, 1).
		Render(label)

	return badge + " " + toast.Message
}

func (m Model) renderHelpOverlayBox(title, content string, maxWidth int) string {
	t := m.theme

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(t.Primary).
		Padding(0, 1)
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(t.Primary).
		Background(t.Base).
		Padding(1, 2).
		Width(maxWidth)

	return boxStyle.Render(titleStyle.Render(title) + "\n\n" + content)
}

func (m Model) getFocusedPanelHints() []components.KeyHint {
	var keybindings []panels.Keybinding

	switch m.focusedPanel {
	case PanelBeads:
		if m.beadsPanel != nil {
			keybindings = m.beadsPanel.Keybindings()
		}
	case PanelAlerts:
		if m.alertsPanel != nil {
			keybindings = m.alertsPanel.Keybindings()
		}
	case PanelAttention:
		if m.attentionPanel != nil {
			keybindings = m.attentionPanel.Keybindings()
		}
	case PanelMetrics:
		if m.metricsPanel != nil {
			keybindings = m.metricsPanel.Keybindings()
		}
	case PanelHistory:
		if m.historyPanel != nil {
			keybindings = m.historyPanel.Keybindings()
		}
	case PanelSidebar:
		if m.timelinePanel != nil && m.timelinePanel.IsFocused() {
			keybindings = m.timelinePanel.Keybindings()
		} else if m.filesPanel != nil && m.filesPanel.IsFocused() {
			keybindings = m.filesPanel.Keybindings()
		} else if m.cassPanel != nil && m.cassPanel.IsFocused() {
			keybindings = m.cassPanel.Keybindings()
		} else if m.historyPanel != nil && m.historyPanel.IsFocused() {
			keybindings = m.historyPanel.Keybindings()
		} else if m.metricsPanel != nil && m.metricsPanel.IsFocused() {
			keybindings = m.metricsPanel.Keybindings()
		}
	}

	var hints []components.KeyHint
	for _, kb := range keybindings {
		action := kb.Action
		if action == "up" || action == "down" {
			continue
		}
		hints = append(hints, components.KeyHint{
			Key:  kb.Key.Help().Key,
			Desc: action,
		})
	}
	return hints
}

func (m Model) renderHeaderContextLine(width int) string {
	if width < 20 {
		return ""
	}

	t := m.theme
	var parts []string
	remote := strings.TrimSpace(tmux.DefaultClient.Remote)
	if remote == "" {
		parts = append(parts, "local")
	} else {
		parts = append(parts, "ssh "+remote)
	}

	if !m.lastRefresh.IsZero() {
		parts = append(parts, "refreshed "+formatRelativeTime(time.Since(m.lastRefresh)))
	} else if m.fetchingSession || m.fetchingContext {
		parts = append(parts, "refreshing…")
	}
	if m.refreshPaused {
		parts = append(parts, "paused")
	}
	if m.scanDisabled {
		parts = append(parts, "scan off")
	}

	line := layout.TruncateWidthDefault(strings.Join(parts, " · "), width-4)
	return lipgloss.NewStyle().
		Foreground(t.Subtext).
		Render(line)
}

func (m Model) renderHeaderHandoffLine(width int) string {
	if width < 20 {
		return ""
	}

	goal := strings.TrimSpace(m.handoffGoal)
	now := strings.TrimSpace(m.handoffNow)
	status := strings.TrimSpace(m.handoffStatus)
	if goal == "" && now == "" && status == "" {
		return ""
	}

	var parts []string
	if goal != "" {
		parts = append(parts, "goal: "+layout.TruncateWidthDefault(goal, 60))
	}
	if now != "" {
		parts = append(parts, "now: "+layout.TruncateWidthDefault(now, 40))
	}
	if m.handoffAge > 0 {
		parts = append(parts, formatRelativeTime(m.handoffAge)+" ago")
	}
	if status != "" {
		parts = append(parts, status)
	}
	if len(parts) == 0 {
		return ""
	}

	line := "handoff · " + strings.Join(parts, " · ")
	line = layout.TruncateWidthDefault(line, width-4)
	return lipgloss.NewStyle().
		Foreground(m.theme.Subtext).
		Render(line)
}

func (m Model) renderHeaderContextWarningLine(width int) string {
	if width < 20 {
		return ""
	}

	type contextAlert struct {
		label   string
		percent float64
		model   string
	}

	paneByIndex := make(map[int]tmux.Pane, len(m.panes))
	for _, pane := range m.panes {
		paneByIndex[pane.Index] = pane
	}

	var alerts []contextAlert
	for idx, ps := range m.paneStatus {
		if ps.ContextLimit <= 0 || ps.ContextPercent < 70 {
			continue
		}
		pane, ok := paneByIndex[idx]
		if !ok {
			continue
		}
		model := ps.ContextModel
		if model == "" {
			model = pane.Variant
		}
		if model == "" {
			model = "unknown"
		}
		model = layout.TruncateWidthDefault(model, 24)
		label := formatPaneLabel(m.session, pane)
		label = layout.TruncateWidthDefault(label, 12)
		alerts = append(alerts, contextAlert{
			label:   label,
			percent: ps.ContextPercent,
			model:   model,
		})
	}
	if len(alerts) == 0 {
		return ""
	}

	sort.Slice(alerts, func(i, j int) bool {
		return alerts[i].percent > alerts[j].percent
	})

	t := m.theme
	prefix := "context"
	if m.icons.Warning != "" {
		prefix = m.icons.Warning + " " + prefix
	}

	warnStyle := lipgloss.NewStyle().Foreground(t.Warning)
	criticalStyle := lipgloss.NewStyle().Foreground(t.Error).Bold(true)

	sep := " · "
	sepWidth := lipgloss.Width(sep)
	maxWidth := width - 4

	prefixText := prefix + ":"
	rendered := []string{warnStyle.Render(prefixText)}
	currentWidth := lipgloss.Width(prefixText)

	for _, alert := range alerts {
		segmentText := fmt.Sprintf("%s %.0f%% of %s context", alert.label, alert.percent, alert.model)
		segmentWidth := lipgloss.Width(segmentText)
		if currentWidth+sepWidth+segmentWidth > maxWidth {
			break
		}

		style := warnStyle
		if alert.percent >= 85 {
			style = criticalStyle
		}
		rendered = append(rendered, style.Render(segmentText))
		currentWidth += sepWidth + segmentWidth
	}

	if len(rendered) == 1 {
		return ""
	}

	return strings.Join(rendered, sep)
}

func formatPaneLabel(session string, pane tmux.Pane) string {
	label := strings.TrimSpace(pane.Title)
	prefix := session + "__"
	label = strings.TrimPrefix(label, prefix)
	if label == "" {
		label = fmt.Sprintf("pane %d", pane.Index)
	}
	return label
}
