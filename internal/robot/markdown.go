package robot

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/robot/adapters"
)

// MarkdownOptions configures markdown output generation.
type MarkdownOptions struct {
	// IncludeSections specifies which sections to include.
	// Empty means the registry-backed markdown defaults.
	// Valid: "summary", "sessions", "work", "alerts", "attention", "beads"
	IncludeSections []string

	// MaxBeads limits the number of work items shown per category.
	MaxBeads int

	// MaxAlerts limits the number of alert items shown.
	MaxAlerts int

	// Compact uses an abbreviated section projection.
	Compact bool

	// Session filters the sessions section to a specific session (empty = all).
	// Project-wide sections such as work and alerts remain unfiltered.
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
	snapshot, err := GetSnapshot(cfg)
	if err != nil {
		return err
	}
	if sections, secErr := resolveMarkdownSections(opts.IncludeSections); secErr == nil && containsMarkdownSection(sections, "beads") {
		limit := markdownBeadLimit(opts.MaxBeads)
		if snapshot.BeadsSummary == nil || !snapshot.BeadsSummary.Available || (limit > BeadLimit && len(snapshot.BeadsSummary.ReadyPreview) == BeadLimit) {
			if summary := bv.GetBeadsSummary("", limit); summary != nil && (summary.Available || snapshot.BeadsSummary == nil) {
				snapshot.BeadsSummary = summary
			}
		}
	}
	return printMarkdownSnapshot(snapshot, opts)
}

func printMarkdownSnapshot(snapshot *SnapshotOutput, opts MarkdownOptions) error {
	if !snapshot.Success {
		return encodeTerminalRobotOutput(snapshot, snapshot.RobotResponse, "robot markdown failed")
	}
	rendered, err := renderMarkdownFromSnapshot(snapshot, opts)
	if err != nil {
		return err
	}
	fmt.Print(rendered)
	return nil
}

// renderMarkdownFromSnapshot renders markdown using the legacy section-specific approach.
// For new code, prefer using ProjectSections + RenderMarkdownFromProjection which
// provides explicit truncation semantics and stable ordering.
//
// Migration path (bd-j9jo3.6.5):
// 1. New consumers should use RenderMarkdownFromProjection
// 2. Existing callers continue using this function for backward compatibility
// 3. Once all consumers migrate, this function can be deprecated
func renderMarkdownFromSnapshot(snapshot *SnapshotOutput, opts MarkdownOptions) (string, error) {
	if snapshot == nil {
		snapshot = &SnapshotOutput{}
	}

	sections, err := resolveMarkdownSections(opts.IncludeSections)
	if err != nil {
		return "", err
	}

	generatedAt := strings.TrimSpace(snapshot.Timestamp)
	if generatedAt == "" {
		generatedAt = strings.TrimSpace(snapshot.RobotResponse.Timestamp)
	}
	if generatedAt == "" {
		generatedAt = time.Now().UTC().Format(time.RFC3339)
	}

	filteredSessions, sessionFound := filterMarkdownSessions(snapshot.Sessions, opts.Session)

	var sb strings.Builder
	sb.WriteString("## NTM Status\n")
	fmt.Fprintf(&sb, "_Generated: %s_\n", generatedAt)
	if opts.Session != "" {
		fmt.Fprintf(&sb, "_Session Filter: %s (sessions section only; project-wide sections remain unfiltered)_\n", opts.Session)
	}
	sb.WriteString("\n")

	for _, section := range sections {
		switch section {
		case "summary":
			writeMarkdownSummarySection(&sb, snapshot, opts)
		case "sessions":
			writeSnapshotSessionsMarkdown(&sb, filteredSessions, opts, sessionFound)
		case "work":
			writeSnapshotWorkMarkdown(&sb, snapshot, opts)
		case "alerts":
			writeSnapshotAlertsMarkdown(&sb, snapshot, opts)
		case "attention":
			writeSnapshotAttentionMarkdown(&sb, snapshot.AttentionSummary, opts)
		case "beads":
			writeSnapshotBeadsMarkdown(&sb, snapshot, opts)
		}
	}

	return sb.String(), nil
}

// ValidMarkdownSections returns the canonical list of section names accepted by
// --robot-markdown (--md-sections / --sections).
func ValidMarkdownSections() []string {
	base := markdownSurfaceSections()
	valid := make([]string, 0, len(base)+1)
	hasBeads := false
	for _, section := range base {
		valid = append(valid, section)
		if section == "beads" {
			hasBeads = true
		}
	}
	if !hasBeads {
		valid = append(valid, "beads")
	}
	return valid
}

func resolveMarkdownSections(requested []string) ([]string, error) {
	if len(requested) == 0 {
		return append([]string(nil), markdownSurfaceSections()...), nil
	}

	supported := ValidMarkdownSections()
	supportedSet := make(map[string]struct{}, len(supported))
	for _, section := range supported {
		supportedSet[section] = struct{}{}
	}

	selected := make(map[string]struct{}, len(requested))
	for _, raw := range requested {
		section := strings.ToLower(strings.TrimSpace(raw))
		if section == "" {
			continue
		}
		if _, ok := supportedSet[section]; !ok {
			return nil, fmt.Errorf("invalid markdown section %q (supported: %s)", raw, strings.Join(supported, ", "))
		}
		selected[section] = struct{}{}
	}

	ordered := make([]string, 0, len(selected))
	for _, section := range supported {
		if _, ok := selected[section]; ok {
			ordered = append(ordered, section)
		}
	}
	return ordered, nil
}

func containsMarkdownSection(sections []string, target string) bool {
	for _, section := range sections {
		if section == target {
			return true
		}
	}
	return false
}

func markdownBeadLimit(maxBeads int) int {
	if maxBeads > 0 {
		return maxBeads
	}
	if BeadLimit > 0 {
		return BeadLimit
	}
	return 5
}

func markdownSurfaceSections() []string {
	registry := GetRobotRegistry()
	if registry != nil {
		if surface, ok := registry.Surface("markdown"); ok && len(surface.Sections) > 0 {
			return surface.Sections
		}
	}
	return []string{"summary", "sessions", "work", "alerts", "attention"}
}

func filterMarkdownSessions(sessions []SnapshotSession, session string) ([]SnapshotSession, bool) {
	if session == "" {
		return sessions, true
	}

	filtered := make([]SnapshotSession, 0, 1)
	for _, sess := range sessions {
		if sess.Name == session {
			filtered = append(filtered, sess)
		}
	}
	return filtered, len(filtered) > 0
}

func writeMarkdownSummarySection(sb *strings.Builder, snapshot *SnapshotOutput, opts MarkdownOptions) {
	sb.WriteString("### Summary\n")

	attentionHeadline := "feed unavailable"
	if snapshot.AttentionSummary != nil {
		attentionHeadline = dashboardAttentionHeadline(snapshot.AttentionSummary)
	}

	if opts.Compact {
		fmt.Fprintf(
			sb,
			"- sessions:%d agents:%d ready:%d in_progress:%d alerts:%d mail:%d attention:%s health:%s\n\n",
			snapshot.Summary.TotalSessions,
			snapshot.Summary.TotalAgents,
			snapshot.Summary.ReadyWork,
			snapshot.Summary.InProgress,
			snapshot.Summary.AlertsActive,
			snapshot.Summary.MailUnread,
			attentionHeadline,
			firstNonEmptyString(snapshot.Summary.HealthStatus, "unknown"),
		)
		return
	}

	sb.WriteString("| Key | Value |\n")
	sb.WriteString("|---|---|\n")
	fmt.Fprintf(sb, "| Sessions | %d |\n", snapshot.Summary.TotalSessions)
	fmt.Fprintf(sb, "| Agents | %d |\n", snapshot.Summary.TotalAgents)
	fmt.Fprintf(sb, "| Ready Work | %d |\n", snapshot.Summary.ReadyWork)
	fmt.Fprintf(sb, "| In Progress | %d |\n", snapshot.Summary.InProgress)
	fmt.Fprintf(sb, "| Active Alerts | %d |\n", snapshot.Summary.AlertsActive)
	fmt.Fprintf(sb, "| Unread Mail | %d |\n", snapshot.Summary.MailUnread)
	fmt.Fprintf(sb, "| Attention | %s |\n", escapeMarkdownCell(attentionHeadline, 120))
	if status := firstNonEmptyString(snapshot.Summary.HealthStatus); status != "" {
		fmt.Fprintf(sb, "| Health | %s |\n", escapeMarkdownCell(status, 80))
	}
	sb.WriteString("\n")
}

// writeSnapshotSessionsMarkdown writes the sessions section from the shared snapshot projection.
func writeSnapshotSessionsMarkdown(sb *strings.Builder, sessions []SnapshotSession, opts MarkdownOptions, sessionFound bool) {
	if opts.Session != "" && !sessionFound {
		fmt.Fprintf(sb, "### Sessions\nSession `%s` not found.\n\n", opts.Session)
		return
	}
	if len(sessions) == 0 {
		if opts.Compact {
			sb.WriteString("### Sessions: none\n\n")
		} else {
			sb.WriteString("### Sessions\nNo active sessions.\n\n")
		}
		return
	}

	fmt.Fprintf(sb, "### Sessions (%d)\n", len(sessions))

	if opts.Compact {
		for _, sess := range sessions {
			typeCounts, stateCounts := snapshotSessionCounts(sess.Agents)
			attached := ""
			if sess.Attached {
				attached = "*"
			}
			fmt.Fprintf(
				sb,
				"- %s%s: %d agents (%s) w:%d i:%d e:%d\n",
				sess.Name,
				attached,
				len(sess.Agents),
				formatMarkdownAgentTypeCounts(typeCounts),
				stateCounts["active"],
				stateCounts["idle"],
				stateCounts["error"],
			)
		}
	} else {
		sb.WriteString("| Session | Attached | Agents | Types | Working | Idle | Error |\n")
		sb.WriteString("|---------|----------|--------|-------|---------|------|-------|\n")

		for _, sess := range sessions {
			typeCounts, stateCounts := snapshotSessionCounts(sess.Agents)

			attached := "no"
			if sess.Attached {
				attached = "yes"
			}

			fmt.Fprintf(sb, "| %s | %s | %d | %s | %d | %d | %d |\n",
				sess.Name, attached, len(sess.Agents),
				formatMarkdownAgentTypeCounts(typeCounts),
				stateCounts["active"], stateCounts["idle"], stateCounts["error"])
		}
	}
	sb.WriteString("\n")
}

var markdownAgentTypeOrder = []string{
	"claude",
	"codex",
	"gemini",
	"grok",
	"omp",
	"cursor",
	"windsurf",
	"aider",
	"oc",
	"ollama",
	"user",
	"other",
}

var markdownAgentTypeLabels = map[string]string{
	"claude":   "cc",
	"codex":    "cod",
	"gemini":   "gmi",
	"grok":     "grok",
	"omp":      "omp",
	"cursor":   "cur",
	"windsurf": "ws",
	"aider":    "aid",
	"oc":       "oc",
	"ollama":   "oll",
	"user":     "usr",
	"other":    "oth",
}

func formatMarkdownAgentTypeCounts(counts map[string]int) string {
	var parts []string
	for _, agentType := range markdownAgentTypeOrder {
		if counts[agentType] <= 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s:%d", markdownAgentTypeLabels[agentType], counts[agentType]))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}

func snapshotSessionCounts(agents []SnapshotAgent) (map[string]int, map[string]int) {
	counts := make(map[string]int, len(markdownAgentTypeOrder))
	for _, agentType := range markdownAgentTypeOrder {
		counts[agentType] = 0
	}
	states := map[string]int{
		"active": 0,
		"idle":   0,
		"error":  0,
	}

	for _, agent := range agents {
		switch normalizedType := normalizeAgentType(agent.Type); normalizedType {
		case "claude", "codex", "gemini", "grok", "omp", "cursor", "windsurf", "aider", "oc", "ollama", "user":
			counts[normalizedType]++
		default:
			counts["other"]++
		}

		if normalizeAgentType(agent.Type) == "user" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(agent.State)) {
		case "idle":
			states["idle"]++
		case "error":
			states["error"]++
		default:
			states["active"]++
		}
	}
	return counts, states
}

// writeSnapshotWorkMarkdown writes the work section from the shared snapshot projection.
func writeSnapshotWorkMarkdown(sb *strings.Builder, snapshot *SnapshotOutput, opts MarkdownOptions) {
	work := snapshot.Work
	if work == nil {
		writeFallbackWorkMarkdown(sb, snapshot.BeadsSummary, opts)
		return
	}
	if !work.Available {
		if opts.Compact {
			sb.WriteString("### Work: unavailable\n\n")
		} else {
			reason := "projection unavailable"
			if work.Reason != "" {
				reason = work.Reason
			}
			fmt.Fprintf(sb, "### Work\n_%s_\n\n", reason)
		}
		return
	}

	summary := work.Summary
	if summary == nil {
		summary = &adapters.WorkSummary{
			Ready:      len(work.Ready),
			InProgress: len(work.InProgress),
			Blocked:    len(work.Blocked),
			Open:       len(work.Ready) + len(work.InProgress) + len(work.Blocked),
			Total:      len(work.Ready) + len(work.InProgress) + len(work.Blocked),
		}
	}

	total := summary.Ready + summary.InProgress + summary.Blocked
	fmt.Fprintf(sb, "### Work (R:%d I:%d B:%d = %d)\n",
		summary.Ready, summary.InProgress, summary.Blocked, total)

	if opts.Compact {
		if len(work.Ready) > 0 {
			ids := make([]string, 0, minInt(len(work.Ready), maxItemLimit(opts.MaxBeads)))
			for _, item := range limitWorkItems(work.Ready, opts.MaxBeads) {
				ids = append(ids, formatWorkItemCompact(item))
			}
			fmt.Fprintf(sb, "- **Ready**: %s\n", strings.Join(ids, ", "))
		}
		if len(work.InProgress) > 0 {
			ids := make([]string, 0, minInt(len(work.InProgress), maxItemLimit(opts.MaxBeads)))
			for _, item := range limitWorkItems(work.InProgress, opts.MaxBeads) {
				ids = append(ids, formatWorkItemCompact(item))
			}
			fmt.Fprintf(sb, "- **In Progress**: %s\n", strings.Join(ids, ", "))
		}
		if len(work.Blocked) > 0 {
			fmt.Fprintf(sb, "- **Blocked**: %d\n", summary.Blocked)
		}
	} else {
		if len(work.Ready) > 0 {
			sb.WriteString("\n**Ready to work on:**\n")
			writeWorkItemsMarkdown(sb, limitWorkItems(work.Ready, opts.MaxBeads))
			writeWorkTruncationNotice(sb, len(work.Ready), opts.MaxBeads)
		}

		if len(work.InProgress) > 0 {
			sb.WriteString("\n**In Progress:**\n")
			writeWorkItemsMarkdown(sb, limitWorkItems(work.InProgress, opts.MaxBeads))
			writeWorkTruncationNotice(sb, len(work.InProgress), opts.MaxBeads)
		}

		if len(work.Blocked) > 0 {
			sb.WriteString("\n**Blocked:**\n")
			writeWorkItemsMarkdown(sb, limitWorkItems(work.Blocked, opts.MaxBeads))
			writeWorkTruncationNotice(sb, len(work.Blocked), opts.MaxBeads)
		}

		if work.Triage != nil && work.Triage.TopRecommendation != nil {
			rec := work.Triage.TopRecommendation
			fmt.Fprintf(
				sb,
				"\n**Top Recommendation:** `%s` (P%d, score %.3f) %s\n",
				rec.ID,
				rec.Priority,
				rec.Score,
				rec.Title,
			)
		}
	}
	sb.WriteString("\n")
}

func writeFallbackWorkMarkdown(sb *strings.Builder, summary *bv.BeadsSummary, opts MarkdownOptions) {
	if summary == nil || !summary.Available {
		if opts.Compact {
			sb.WriteString("### Work: unavailable\n\n")
		} else {
			sb.WriteString("### Work\n_Beads summary unavailable._\n\n")
		}
		return
	}

	total := summary.Ready + summary.InProgress + summary.Blocked
	fmt.Fprintf(sb, "### Work (R:%d I:%d B:%d = %d)\n", summary.Ready, summary.InProgress, summary.Blocked, total)
	if opts.Compact {
		sb.WriteString("- detailed work projection unavailable; showing shared counts only\n\n")
		return
	}
	sb.WriteString("_Detailed work items unavailable in snapshot; showing shared summary counts only._\n\n")
}

// writeSnapshotBeadsMarkdown writes the beads section using snapshot.BeadsSummary
// or falling back to bv.GetBeadsSummary("", limit), gracefully degrading when
// br/bv are unavailable.
func writeSnapshotBeadsMarkdown(sb *strings.Builder, snapshot *SnapshotOutput, opts MarkdownOptions) {
	limit := markdownBeadLimit(opts.MaxBeads)
	var beads *bv.BeadsSummary
	if snapshot != nil {
		beads = snapshot.BeadsSummary
		if beads == nil && snapshot.Work != nil && snapshot.Work.Available {
			beads = beadsSummaryFromWork(snapshot.Work, "", limit)
		}
	}
	if beads == nil {
		beads = bv.GetBeadsSummary("", limit)
	}

	if beads == nil || !beads.Available {
		if opts.Compact {
			sb.WriteString("### Beads: unavailable\n\n")
			return
		}
		if beads != nil && strings.TrimSpace(beads.Reason) != "" {
			fmt.Fprintf(sb, "### Beads\n_Beads summary unavailable: %s._\n\n", escapeMarkdownCell(beads.Reason, 160))
		} else {
			sb.WriteString("### Beads\n_Beads summary unavailable._\n\n")
		}
		return
	}

	if opts.Compact {
		fmt.Fprintf(
			sb,
			"### Beads (total:%d open:%d ready:%d in_progress:%d blocked:%d closed:%d)\n",
			beads.Total, beads.Open, beads.Ready, beads.InProgress, beads.Blocked, beads.Closed,
		)
		if len(beads.ReadyPreview) > 0 {
			shown := limitBeadPreviews(beads.ReadyPreview, opts.MaxBeads)
			parts := make([]string, 0, len(shown))
			for _, b := range shown {
				if strings.TrimSpace(b.Priority) != "" {
					parts = append(parts, fmt.Sprintf("%s (%s)", b.ID, b.Priority))
				} else {
					parts = append(parts, b.ID)
				}
			}
			fmt.Fprintf(sb, "- **Ready**: %s\n", strings.Join(parts, ", "))
		}
		if len(beads.InProgressList) > 0 {
			shown := limitBeadsInProgress(beads.InProgressList, opts.MaxBeads)
			parts := make([]string, 0, len(shown))
			for _, b := range shown {
				if strings.TrimSpace(b.Assignee) != "" {
					parts = append(parts, fmt.Sprintf("%s→%s", b.ID, b.Assignee))
				} else {
					parts = append(parts, b.ID)
				}
			}
			fmt.Fprintf(sb, "- **In Progress**: %s\n", strings.Join(parts, ", "))
		}
		sb.WriteString("\n")
		return
	}

	sb.WriteString("### Beads\n")
	sb.WriteString("| Total | Open | In Progress | Blocked | Ready | Closed |\n")
	sb.WriteString("|---:|---:|---:|---:|---:|---:|\n")
	fmt.Fprintf(sb, "| %d | %d | %d | %d | %d | %d |\n\n",
		beads.Total, beads.Open, beads.InProgress, beads.Blocked, beads.Ready, beads.Closed)

	if len(beads.ReadyPreview) > 0 {
		shown := limitBeadPreviews(beads.ReadyPreview, opts.MaxBeads)
		sb.WriteString("**Ready:**\n")
		sb.WriteString("| ID | Priority | Title |\n")
		sb.WriteString("|---|---|---|\n")
		for _, b := range shown {
			fmt.Fprintf(sb, "| %s | %s | %s |\n",
				escapeMarkdownCell(b.ID, 32),
				escapeMarkdownCell(b.Priority, 8),
				escapeMarkdownCell(b.Title, 140))
		}
		totalReady := beads.Ready
		if len(beads.ReadyPreview) > totalReady {
			totalReady = len(beads.ReadyPreview)
		}
		if totalReady > len(shown) {
			fmt.Fprintf(sb, "_Truncated: showing %d of %d ready beads._\n", len(shown), totalReady)
		}
		sb.WriteString("\n")
	} else {
		sb.WriteString("_No ready beads._\n\n")
	}

	if len(beads.InProgressList) > 0 {
		shown := limitBeadsInProgress(beads.InProgressList, opts.MaxBeads)
		sb.WriteString("**In Progress:**\n")
		sb.WriteString("| ID | Assignee | Title | Updated |\n")
		sb.WriteString("|---|---|---|---|\n")
		for _, b := range shown {
			updated := ""
			if !b.UpdatedAt.IsZero() {
				updated = b.UpdatedAt.UTC().Format(time.RFC3339)
			}
			fmt.Fprintf(sb, "| %s | %s | %s | %s |\n",
				escapeMarkdownCell(b.ID, 32),
				escapeMarkdownCell(b.Assignee, 40),
				escapeMarkdownCell(b.Title, 120),
				escapeMarkdownCell(updated, 40))
		}
		totalInProg := beads.InProgress
		if len(beads.InProgressList) > totalInProg {
			totalInProg = len(beads.InProgressList)
		}
		if totalInProg > len(shown) {
			fmt.Fprintf(sb, "_Truncated: showing %d of %d in-progress beads._\n", len(shown), totalInProg)
		}
		sb.WriteString("\n")
	} else {
		sb.WriteString("_No in-progress beads._\n\n")
	}
}

func limitBeadPreviews(items []bv.BeadPreview, max int) []bv.BeadPreview {
	if max <= 0 || len(items) <= max {
		return items
	}
	return items[:max]
}

func limitBeadsInProgress(items []bv.BeadInProgress, max int) []bv.BeadInProgress {
	if max <= 0 || len(items) <= max {
		return items
	}
	return items[:max]
}

func writeSnapshotAlertsMarkdown(sb *strings.Builder, snapshot *SnapshotOutput, opts MarkdownOptions) {
	totalActive, critical, warning, info := alertSummaryCounts(snapshot)
	if totalActive == 0 {
		if opts.Compact {
			sb.WriteString("### Alerts: none\n\n")
		} else {
			sb.WriteString("### Alerts\nNo active alerts. ✓\n\n")
		}
		return
	}

	fmt.Fprintf(sb, "### Alerts (%d", totalActive)
	if critical > 0 {
		fmt.Fprintf(sb, ", %d critical", critical)
	}
	if warning > 0 {
		fmt.Fprintf(sb, ", %d warning", warning)
	}
	if info > 0 {
		fmt.Fprintf(sb, ", %d info", info)
	}
	sb.WriteString(")\n")

	if opts.Compact {
		fmt.Fprintf(sb, "- critical:%d warning:%d info:%d total:%d\n\n", critical, warning, info, totalActive)
		return
	}

	if len(snapshot.AlertsDetailed) == 0 {
		shown := snapshot.Alerts
		if opts.MaxAlerts > 0 && len(shown) > opts.MaxAlerts {
			shown = shown[:opts.MaxAlerts]
		}
		for _, alert := range shown {
			fmt.Fprintf(sb, "- %s\n", alert)
		}
		if opts.MaxAlerts > 0 && len(snapshot.Alerts) > opts.MaxAlerts {
			fmt.Fprintf(sb, "\n_Truncated: showing %d of %d alerts._\n", len(shown), len(snapshot.Alerts))
		}
		sb.WriteString("\n")
		return
	}

	shown := snapshot.AlertsDetailed
	if opts.MaxAlerts > 0 && len(shown) > opts.MaxAlerts {
		shown = shown[:opts.MaxAlerts]
	}
	if rendered := RenderAlertsList(shown); rendered != "" {
		sb.WriteString(rendered)
		sb.WriteString("\n")
	}
	if opts.MaxAlerts > 0 && len(snapshot.AlertsDetailed) > opts.MaxAlerts {
		fmt.Fprintf(sb, "\n_Truncated: showing %d of %d alerts._\n", len(shown), len(snapshot.AlertsDetailed))
	}
	sb.WriteString("\n")
}

func alertSummaryCounts(snapshot *SnapshotOutput) (totalActive, critical, warning, info int) {
	if snapshot.AlertSummary != nil {
		totalActive = snapshot.AlertSummary.TotalActive
		critical = snapshot.AlertSummary.BySeverity["critical"]
		warning = snapshot.AlertSummary.BySeverity["warning"]
		info = snapshot.AlertSummary.BySeverity["info"]
	} else {
		totalActive = len(snapshot.AlertsDetailed)
		for _, alert := range snapshot.AlertsDetailed {
			switch strings.ToLower(strings.TrimSpace(alert.Severity)) {
			case "critical":
				critical++
			case "warning":
				warning++
			case "info":
				info++
			}
		}
	}
	totalActive += len(snapshot.Alerts)
	return totalActive, critical, warning, info
}

func writeSnapshotAttentionMarkdown(sb *strings.Builder, attention *SnapshotAttentionSummary, opts MarkdownOptions) {
	sb.WriteString("### Attention\n")
	if opts.Compact {
		headline := "feed unavailable"
		if attention != nil {
			headline = dashboardAttentionHeadline(attention)
		}
		fmt.Fprintf(sb, "- %s\n\n", headline)
		return
	}
	writeAttentionSection(sb, attention)
	sb.WriteString("\n")
}

func limitWorkItems(items []adapters.WorkItem, max int) []adapters.WorkItem {
	if max <= 0 || len(items) <= max {
		return items
	}
	return items[:max]
}

func writeWorkItemsMarkdown(sb *strings.Builder, items []adapters.WorkItem) {
	for _, item := range items {
		label := ""
		if item.Assignee != "" {
			label = fmt.Sprintf(" → %s", item.Assignee)
		}
		fmt.Fprintf(sb, "- `%s`%s: %s\n", item.ID, label, item.Title)
	}
}

func writeWorkTruncationNotice(sb *strings.Builder, total, max int) {
	if max > 0 && total > max {
		fmt.Fprintf(sb, "_Truncated: showing %d of %d items._\n", max, total)
	}
}

func formatWorkItemCompact(item adapters.WorkItem) string {
	if item.Assignee != "" {
		return fmt.Sprintf("%s→%s", item.ID, item.Assignee)
	}
	return item.ID
}

func maxItemLimit(max int) int {
	if max > 0 {
		return max
	}
	return 0
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
		lastValid := 0
		for i := range s {
			if i > maxLen {
				break
			}
			lastValid = i
		}
		if lastValid == 0 && len(s) > 0 {
			return ""
		}
		return s[:lastValid]
	}
	targetLen := maxLen - 3
	prevI := 0
	for i := range s {
		if i > targetLen {
			return s[:prevI] + "..."
		}
		prevI = i
	}
	return s[:prevI] + "..."
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
		fmt.Fprintf(&b, "### %s\n", capitalize(sev))
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
		fmt.Fprintf(&b, "### %s\n", capitalize(sev))
		for _, a := range grouped[sev] {
			fmt.Fprintf(&b, "- [%s] %s\n", a.Type, a.Message)
		}
		b.WriteString("\n")
	}

	return strings.TrimSpace(b.String())
}

// =============================================================================
// Section Projection-Based Markdown Rendering (bd-j9jo3.6.5)
// =============================================================================
