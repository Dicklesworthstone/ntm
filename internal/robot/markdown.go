package robot

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// MarkdownOptions configures markdown output generation.
type MarkdownOptions struct {
	// IncludeSections specifies which sections to include.
	// Empty means all sections. Valid: "sessions", "beads", "alerts", "mail"
	IncludeSections []string

	// MaxBeads limits the number of beads shown per category.
	MaxBeads int

	// MaxAlerts limits the number of alerts shown.
	MaxAlerts int

	// Compact uses even more abbreviated output.
	Compact bool

	// Session filters output to a specific session (empty = all).
	Session string
}

// DefaultMarkdownOptions returns sensible defaults for markdown output.
func DefaultMarkdownOptions() MarkdownOptions {
	return MarkdownOptions{
		IncludeSections: nil, // All sections
		MaxBeads:        5,
		MaxAlerts:       10,
		Compact:         false,
		Session:         "",
	}
}

// PrintMarkdown outputs system state as token-efficient markdown for LLM consumption.
// This is the main entry point for --robot-markdown.
func PrintMarkdown(cfg *config.Config, opts MarkdownOptions) error {
	if cfg == nil {
		cfg = config.Default()
	}

	var sb strings.Builder

	// Header with timestamp
	sb.WriteString("## NTM Status\n")
	sb.WriteString(fmt.Sprintf("_Generated: %s_\n\n", time.Now().UTC().Format("2006-01-02 15:04 UTC")))

	includeAll := len(opts.IncludeSections) == 0
	sectionSet := make(map[string]bool)
	for _, s := range opts.IncludeSections {
		sectionSet[strings.ToLower(s)] = true
	}

	// Sessions section
	if includeAll || sectionSet["sessions"] {
		writeSessionsMarkdown(&sb, opts)
	}

	// Beads section
	if includeAll || sectionSet["beads"] {
		writeBeadsMarkdown(&sb, opts)
	}

	// Alerts section
	if includeAll || sectionSet["alerts"] {
		writeAlertsSection(&sb, cfg, opts)
	}

	// Mail section
	if includeAll || sectionSet["mail"] {
		writeMailSection(&sb, opts)
	}

	fmt.Print(sb.String())
	return nil
}

// PrintMarkdownCompact outputs ultra-compact markdown suitable for system prompts.
func PrintMarkdownCompact(cfg *config.Config) error {
	opts := MarkdownOptions{
		MaxBeads:  3,
		MaxAlerts: 5,
		Compact:   true,
	}
	return PrintMarkdown(cfg, opts)
}

// writeSessionsMarkdown writes the sessions section.
func writeSessionsMarkdown(sb *strings.Builder, opts MarkdownOptions) {
	sessions, err := tmux.ListSessions()
	if err != nil || len(sessions) == 0 {
		if opts.Compact {
			sb.WriteString("### Sessions: none\n\n")
		} else {
			sb.WriteString("### Sessions\nNo active sessions.\n\n")
		}
		return
	}

	// Filter by session if specified
	if opts.Session != "" {
		filtered := make([]tmux.Session, 0)
		for _, s := range sessions {
			if s.Name == opts.Session {
				filtered = append(filtered, s)
			}
		}
		sessions = filtered
	}

	if len(sessions) == 0 {
		sb.WriteString(fmt.Sprintf("### Sessions\nSession '%s' not found.\n\n", opts.Session))
		return
	}

	sb.WriteString(fmt.Sprintf("### Sessions (%d)\n", len(sessions)))

	if opts.Compact {
		// Ultra-compact: one line per session
		for _, sess := range sessions {
			panes, _ := tmux.GetPanes(sess.Name)
			counts := countAgentsByType(panes)
			attached := ""
			if sess.Attached {
				attached = "*"
			}
			sb.WriteString(fmt.Sprintf("- %s%s: %d agents (cc:%d cod:%d gmi:%d)\n",
				sess.Name, attached, len(panes), counts["claude"], counts["codex"], counts["gemini"]))
		}
	} else {
		// Table format
		sb.WriteString("| Session | Attached | Agents | Claude | Codex | Gemini | Working | Idle | Error |\n")
		sb.WriteString("|---------|----------|--------|--------|-------|--------|---------|------|-------|\n")

		for _, sess := range sessions {
			panes, _ := tmux.GetPanes(sess.Name)
			counts := countAgentsByType(panes)
			states := countAgentsByState(panes)

			attached := "no"
			if sess.Attached {
				attached = "yes"
			}

			sb.WriteString(fmt.Sprintf("| %s | %s | %d | %d | %d | %d | %d | %d | %d |\n",
				sess.Name, attached, len(panes),
				counts["claude"], counts["codex"], counts["gemini"],
				states["working"], states["idle"], states["error"]))
		}
	}
	sb.WriteString("\n")
}

// countAgentsByType counts agents by type from panes.
func countAgentsByType(panes []tmux.Pane) map[string]int {
	counts := map[string]int{
		"claude": 0,
		"codex":  0,
		"gemini": 0,
		"user":   0,
		"other":  0,
	}

	for _, pane := range panes {
		switch pane.Type {
		case tmux.AgentClaude:
			counts["claude"]++
		case tmux.AgentCodex:
			counts["codex"]++
		case tmux.AgentGemini:
			counts["gemini"]++
		case tmux.AgentUser:
			counts["user"]++
		default:
			counts["other"]++
		}
	}
	return counts
}

// countAgentsByState counts agents by state (working/idle/error).
func countAgentsByState(panes []tmux.Pane) map[string]int {
	counts := map[string]int{
		"working": 0,
		"idle":    0,
		"error":   0,
		"unknown": 0,
	}

	for _, pane := range panes {
		// Skip user panes
		if pane.Type == tmux.AgentUser {
			continue
		}

		// Capture output to detect state
		captured, err := tmux.CapturePaneOutput(pane.ID, 20)
		if err != nil {
			counts["unknown"]++
			continue
		}

		lines := splitLines(stripANSI(captured))
		state := detectState(lines, pane.Title)

		switch state {
		case "active":
			counts["working"]++
		case "idle":
			counts["idle"]++
		case "error":
			counts["error"]++
		default:
			counts["unknown"]++
		}
	}
	return counts
}

// writeBeadsMarkdown writes the beads section.
func writeBeadsMarkdown(sb *strings.Builder, opts MarkdownOptions) {
	summary := bv.GetBeadsSummary("", opts.MaxBeads)
	if summary == nil || !summary.Available {
		if opts.Compact {
			sb.WriteString("### Beads: unavailable\n\n")
		} else {
			reason := "bv not installed"
			if summary != nil && summary.Reason != "" {
				reason = summary.Reason
			}
			sb.WriteString(fmt.Sprintf("### Beads\n_%s_\n\n", reason))
		}
		return
	}

	total := summary.Ready + summary.InProgress + summary.Blocked
	sb.WriteString(fmt.Sprintf("### Beads (R:%d I:%d B:%d = %d)\n",
		summary.Ready, summary.InProgress, summary.Blocked, total))

	if opts.Compact {
		// Ultra-compact: comma-separated lists
		if len(summary.ReadyPreview) > 0 {
			ids := make([]string, 0, len(summary.ReadyPreview))
			for _, b := range summary.ReadyPreview {
				ids = append(ids, fmt.Sprintf("%s(%s)", b.ID, b.Priority))
			}
			sb.WriteString(fmt.Sprintf("- **Ready**: %s\n", strings.Join(ids, ", ")))
		}
		if len(summary.InProgressList) > 0 {
			ids := make([]string, 0, len(summary.InProgressList))
			for _, b := range summary.InProgressList {
				if b.Assignee != "" {
					ids = append(ids, fmt.Sprintf("%s→%s", b.ID, b.Assignee))
				} else {
					ids = append(ids, b.ID)
				}
			}
			sb.WriteString(fmt.Sprintf("- **In Progress**: %s\n", strings.Join(ids, ", ")))
		}
	} else {
		// Detailed format with titles
		if len(summary.ReadyPreview) > 0 {
			sb.WriteString("\n**Ready to work on:**\n")
			for _, b := range summary.ReadyPreview {
				title := truncateStr(b.Title, 50)
				sb.WriteString(fmt.Sprintf("- `%s` (%s): %s\n", b.ID, b.Priority, title))
			}
		}

		if len(summary.InProgressList) > 0 {
			sb.WriteString("\n**In Progress:**\n")
			for _, b := range summary.InProgressList {
				title := truncateStr(b.Title, 50)
				assignee := ""
				if b.Assignee != "" {
					assignee = fmt.Sprintf(" → %s", b.Assignee)
				}
				sb.WriteString(fmt.Sprintf("- `%s`%s: %s\n", b.ID, assignee, title))
			}
		}

		if summary.Blocked > 0 && len(summary.ReadyPreview) == 0 {
			sb.WriteString(fmt.Sprintf("\n_Note: %d beads blocked, waiting on dependencies_\n", summary.Blocked))
		}
	}
	sb.WriteString("\n")
}

// writeAlertsSection writes the alerts section.
func writeAlertsSection(sb *strings.Builder, cfg *config.Config, opts MarkdownOptions) {
	var alertCfg alerts.Config
	if cfg != nil {
		alertCfg = alerts.ToConfigAlerts(
			cfg.Alerts.Enabled,
			cfg.Alerts.AgentStuckMinutes,
			cfg.Alerts.DiskLowThresholdGB,
			cfg.Alerts.MailBacklogThreshold,
			cfg.Alerts.BeadStaleHours,
			cfg.Alerts.ResolvedPruneMinutes,
			cfg.ProjectsBase,
		)
	} else {
		alertCfg = alerts.DefaultConfig()
	}

	activeAlerts := alerts.GetActiveAlerts(alertCfg)

	if len(activeAlerts) == 0 {
		if opts.Compact {
			sb.WriteString("### Alerts: none\n\n")
		} else {
			sb.WriteString("### Alerts\nNo active alerts. ✓\n\n")
		}
		return
	}

	// Count by severity
	critical, warning := 0, 0
	for _, a := range activeAlerts {
		switch a.Severity {
		case alerts.SeverityCritical:
			critical++
		case alerts.SeverityWarning:
			warning++
		}
	}

	sb.WriteString(fmt.Sprintf("### Alerts (%d", len(activeAlerts)))
	if critical > 0 {
		sb.WriteString(fmt.Sprintf(", %d critical", critical))
	}
	if warning > 0 {
		sb.WriteString(fmt.Sprintf(", %d warning", warning))
	}
	sb.WriteString(")\n")

	// Sort by severity (critical first)
	sort.Slice(activeAlerts, func(i, j int) bool {
		return alertSeverityOrder(activeAlerts[i].Severity) < alertSeverityOrder(activeAlerts[j].Severity)
	})

	// Limit output
	shown := activeAlerts
	if opts.MaxAlerts > 0 && len(shown) > opts.MaxAlerts {
		shown = shown[:opts.MaxAlerts]
	}

	for _, a := range shown {
		icon := alertSeverityIcon(a.Severity)
		msg := a.Message
		if a.Session != "" {
			msg = fmt.Sprintf("[%s] %s", a.Session, msg)
		}
		if opts.Compact {
			sb.WriteString(fmt.Sprintf("- %s %s\n", icon, truncateStr(msg, 60)))
		} else {
			sb.WriteString(fmt.Sprintf("- %s %s\n", icon, msg))
		}
	}

	if len(activeAlerts) > opts.MaxAlerts && opts.MaxAlerts > 0 {
		sb.WriteString(fmt.Sprintf("_...and %d more_\n", len(activeAlerts)-opts.MaxAlerts))
	}
	sb.WriteString("\n")
}

// alertSeverityOrder returns sort order for severity (lower = more severe).
func alertSeverityOrder(s alerts.Severity) int {
	switch s {
	case alerts.SeverityCritical:
		return 0
	case alerts.SeverityWarning:
		return 1
	default:
		return 2
	}
}

// alertSeverityIcon returns an icon for the severity.
func alertSeverityIcon(s alerts.Severity) string {
	switch s {
	case alerts.SeverityCritical:
		return "🔴"
	case alerts.SeverityWarning:
		return "⚠️"
	default:
		return "ℹ️"
	}
}

// writeMailSection writes the mail section.
func writeMailSection(sb *strings.Builder, opts MarkdownOptions) {
	projectKey, err := os.Getwd()
	if err != nil {
		sb.WriteString("### Mail: unavailable\n\n")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := agentmail.NewClient(agentmail.WithProjectKey(projectKey))
	if !client.IsAvailable() {
		if opts.Compact {
			sb.WriteString("### Mail: offline\n\n")
		} else {
			sb.WriteString("### Mail\nAgent Mail server not available.\n\n")
		}
		return
	}

	// Ensure project exists
	if _, err := client.EnsureProject(ctx, projectKey); err != nil {
		sb.WriteString("### Mail: error\n\n")
		return
	}

	agents, err := client.ListProjectAgents(ctx, projectKey)
	if err != nil {
		sb.WriteString("### Mail: error\n\n")
		return
	}

	if len(agents) == 0 {
		if opts.Compact {
			sb.WriteString("### Mail: no agents\n\n")
		} else {
			sb.WriteString("### Mail\nNo registered agents.\n\n")
		}
		return
	}

	// Gather unread counts
	type agentMailInfo struct {
		name   string
		unread int
		urgent int
	}
	var mailStats []agentMailInfo
	totalUnread := 0

	for _, a := range agents {
		unread := countInbox(ctx, client, projectKey, a.Name, false)
		urgent := countInbox(ctx, client, projectKey, a.Name, true)
		if unread > 0 || !opts.Compact {
			mailStats = append(mailStats, agentMailInfo{name: a.Name, unread: unread, urgent: urgent})
		}
		totalUnread += unread
	}

	if totalUnread == 0 {
		if opts.Compact {
			sb.WriteString("### Mail: 0 unread\n\n")
		} else {
			sb.WriteString("### Mail\nNo unread messages.\n\n")
		}
		return
	}

	sb.WriteString(fmt.Sprintf("### Mail (%d unread)\n", totalUnread))

	if opts.Compact {
		// One-liner
		parts := make([]string, 0, len(mailStats))
		for _, m := range mailStats {
			if m.unread > 0 {
				if m.urgent > 0 {
					parts = append(parts, fmt.Sprintf("%s:%d(%d!)", m.name, m.unread, m.urgent))
				} else {
					parts = append(parts, fmt.Sprintf("%s:%d", m.name, m.unread))
				}
			}
		}
		sb.WriteString(strings.Join(parts, ", "))
		sb.WriteString("\n")
	} else {
		for _, m := range mailStats {
			if m.unread > 0 {
				urgentNote := ""
				if m.urgent > 0 {
					urgentNote = fmt.Sprintf(" (%d urgent)", m.urgent)
				}
				sb.WriteString(fmt.Sprintf("- **%s**: %d unread%s\n", m.name, m.unread, urgentNote))
			}
		}
	}
	sb.WriteString("\n")
}

// truncateStr truncates a string to maxLen with ellipsis, respecting UTF-8 boundaries.
func truncateStr(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return "..."[:maxLen]
	}
	// Find first rune boundary at or after maxLen-3 bytes
	targetLen := maxLen - 3
	for i := range s {
		if i >= targetLen {
			return s[:i] + "..."
		}
	}
	return s
}

// AgentTable renders a markdown table summarizing agents per session.
func AgentTable(sessions []SnapshotSession) string {
	var b strings.Builder
	b.WriteString("| Session | Pane | Type | Variant | State |\n")
	b.WriteString("|---|---|---|---|---|\n")
	for _, sess := range sessions {
		for _, agent := range sess.Agents {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n",
				sess.Name,
				agent.Pane,
				agent.Type,
				agent.Variant,
				agent.State)
		}
	}
	return b.String()
}

// AlertsList renders alerts as a markdown bullet list.
func AlertsList(alerts []AlertInfo) string {
	if len(alerts) == 0 {
		return "_No active alerts._"
	}
	var b strings.Builder
	for _, a := range alerts {
		fmt.Fprintf(&b, "- [%s] %s", strings.ToUpper(a.Severity), a.Message)
		if a.Session != "" {
			fmt.Fprintf(&b, " (session: %s", a.Session)
			if a.Pane != "" {
				fmt.Fprintf(&b, ", pane: %s", a.Pane)
			}
			fmt.Fprintf(&b, ")")
		}
		if a.BeadID != "" {
			fmt.Fprintf(&b, " [bead: %s]", a.BeadID)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// BeadsSummary renders a concise markdown summary of bead counts.
func BeadsSummary(summary *bv.BeadsSummary) string {
	if summary == nil || !summary.Available {
		return "_Beads summary unavailable._"
	}
	return fmt.Sprintf(
		"- Total: %d (Open: %d, In Progress: %d, Blocked: %d, Ready: %d, Closed: %d)",
		summary.Total,
		summary.Open,
		summary.InProgress,
		summary.Blocked,
		summary.Ready,
		summary.Closed,
	)
}

// SuggestedActions renders planned actions as markdown list items.
func SuggestedActions(actions []BeadAction) string {
	if len(actions) == 0 {
		return "_No suggested actions._"
	}
	var b strings.Builder
	for _, act := range actions {
		fmt.Fprintf(&b, "- %s: %s", act.BeadID, act.Title)
		if len(act.BlockedBy) > 0 {
			fmt.Fprintf(&b, " (blocked by: %s)", strings.Join(act.BlockedBy, ", "))
		}
		if act.Command != "" {
			fmt.Fprintf(&b, " — `%s`", act.Command)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// AgentTableRow represents a row in the agent markdown table.
type AgentTableRow struct {
	Agent  string
	Type   string
	Status string
}

// SuggestedAction is a lightweight action item for numbered lists.
type SuggestedAction struct {
	Title  string
	Reason string
}

// RenderAgentTable returns a markdown table of agents.
func RenderAgentTable(rows []AgentTableRow) string {
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("| Agent | Type | Status |\n")
	b.WriteString("| --- | --- | --- |\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", r.Agent, r.Type, r.Status)
	}
	return b.String()
}

// RenderAlertsList groups alerts by severity and returns markdown bullets.
// Order of severities is: critical, warning, info, other.
func RenderAlertsList(alerts []AlertInfo) string {
	if len(alerts) == 0 {
		return ""
	}

	grouped := make(map[string][]AlertInfo)
	for _, a := range alerts {
		sev := strings.ToLower(a.Severity)
		grouped[sev] = append(grouped[sev], a)
	}

	severityOrder := []string{"critical", "warning", "info"}

	var b strings.Builder
	for _, sev := range severityOrder {
		if len(grouped[sev]) == 0 {
			continue
		}
		fmt.Fprintf(&b, "### %s\n", strings.Title(sev))
		for _, a := range grouped[sev] {
			loc := strings.TrimSpace(strings.Join([]string{a.Session, a.Pane}, " "))
			if loc != "" {
				loc = " (" + loc + ")"
			}
			fmt.Fprintf(&b, "- [%s] %s%s\n", a.Type, a.Message, loc)
		}
		b.WriteString("\n")
	}

	var others []string
	for sev := range grouped {
		if sev != "critical" && sev != "warning" && sev != "info" {
			others = append(others, sev)
		}
	}
	sort.Strings(others)
	for _, sev := range others {
		fmt.Fprintf(&b, "### %s\n", strings.Title(sev))
		for _, a := range grouped[sev] {
			fmt.Fprintf(&b, "- [%s] %s\n", a.Type, a.Message)
		}
		b.WriteString("\n")
	}

	return strings.TrimSpace(b.String())
}

// RenderSuggestedActions returns a numbered markdown list.
func RenderSuggestedActions(actions []SuggestedAction) string {
	if len(actions) == 0 {
		return ""
	}
	var b strings.Builder
	for i, a := range actions {
		line := a.Title
		if a.Reason != "" {
			line = fmt.Sprintf("%s — %s", a.Title, a.Reason)
		}
		fmt.Fprintf(&b, "%d. %s\n", i+1, line)
	}
	return strings.TrimSpace(b.String())
}
