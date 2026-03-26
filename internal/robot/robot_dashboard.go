package robot

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tracker"
)

// DashboardOutput provides a concise dashboard view for AI orchestrators.
type DashboardOutput struct {
	RobotResponse
	GeneratedAt  time.Time                 `json:"generated_at"`
	Fleet        string                    `json:"fleet"`
	Agents       []SnapshotSession         `json:"agents"`
	Metrics      map[string]any            `json:"metrics,omitempty"`
	System       SystemInfo                `json:"system"`
	Summary      StatusSummary             `json:"summary"`
	Beads        *bv.BeadsSummary          `json:"beads,omitempty"`
	Progress     *ProgressSummary          `json:"progress,omitempty"`
	Alerts       []AlertInfo               `json:"alerts,omitempty"`
	AlertSummary *AlertSummaryInfo         `json:"alert_summary,omitempty"`
	Conflicts    []tracker.Conflict        `json:"conflicts,omitempty"`
	FileChanges  []FileChangeInfo          `json:"file_changes,omitempty"`
	AgentMail    *SnapshotAgentMail        `json:"agent_mail,omitempty"`
	Attention    *SnapshotAttentionSummary `json:"attention,omitempty"`
}

// GetDashboard retrieves a dashboard-oriented view for AI orchestrators.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetDashboard() (*DashboardOutput, error) {
	wd, _ := os.Getwd()
	fleet := "ntm"
	if wd != "" {
		fleet = filepath.Base(wd)
	}
	cfg, err := config.LoadMerged(wd, config.DefaultPath())
	if err != nil {
		cfg = config.Default()
	}

	output := &DashboardOutput{
		RobotResponse: NewRobotResponse(true),
		GeneratedAt:   time.Now().UTC(),
		Fleet:         fleet,
		Agents:        []SnapshotSession{},
		Metrics:       map[string]any{},
		System: SystemInfo{
			OS:        runtime.GOOS,
			Arch:      runtime.GOARCH,
			GoVersion: runtime.Version(),
			Version:   Version,
			Commit:    Commit,
			BuildDate: Date,
			TmuxOK:    tmux.IsInstalled(),
		},
		Summary: StatusSummary{
			AgentsByState: map[string]int{},
			AgentsByType:  map[string]int{},
		},
	}

	// Sessions and agents (best-effort)
	if tmux.IsInstalled() {
		sessions, err := tmux.ListSessions()
		if err == nil {
			for _, sess := range sessions {
				snapSession := SnapshotSession{
					Name:     sess.Name,
					Attached: sess.Attached,
					Agents:   []SnapshotAgent{},
				}
				panes, err := tmux.GetPanes(sess.Name)
				if err == nil {
					for _, pane := range panes {
						agentType, ok := dashboardAgentType(pane)
						if !ok {
							continue
						}
						snapSession.Agents = append(snapSession.Agents, SnapshotAgent{
							Pane:           fmt.Sprintf("%d.%d", 0, pane.Index),
							Type:           agentType,
							Variant:        pane.Variant,
							TypeConfidence: 0.5,
							TypeMethod:     "tmux-pane",
							State:          "unknown",
						})

						switch agentType {
						case "claude":
							output.Summary.ClaudeCount++
						case "codex":
							output.Summary.CodexCount++
						case "gemini":
							output.Summary.GeminiCount++
						case "cursor":
							output.Summary.CursorCount++
						case "windsurf":
							output.Summary.WindsurfCount++
						case "aider":
							output.Summary.AiderCount++
						}
						output.Summary.TotalAgents++
						output.Summary.AgentsByType[agentType]++
						output.Summary.AgentsByState["unknown"]++
					}
				}
				output.Agents = append(output.Agents, snapSession)
				output.Summary.TotalSessions++
				if sess.Attached {
					output.Summary.AttachedCount++
				}
			}
		}
	}

	// Beads summary (best-effort)
	if bv.IsInstalled() {
		output.Beads = bv.GetBeadsSummary(wd, BeadLimit)
		output.Progress = ComputeProgress(output.Beads)
	}

	// Alerts (best-effort)
	alertCfg := alertConfigForProject(cfg, wd)
	activeAlerts := alerts.GetActiveAlerts(alertCfg)
	alertSummary := alerts.GetGlobalTracker().Summary()
	for _, a := range activeAlerts {
		output.Alerts = append(output.Alerts, AlertInfo{
			ID:         a.ID,
			Type:       string(a.Type),
			Severity:   string(a.Severity),
			Message:    a.Message,
			Session:    a.Session,
			Pane:       a.Pane,
			BeadID:     a.BeadID,
			Context:    a.Context,
			CreatedAt:  a.CreatedAt.Format(time.RFC3339),
			DurationMs: a.Duration().Milliseconds(),
			Count:      a.Count,
		})
	}
	output.AlertSummary = &AlertSummaryInfo{
		TotalActive: alertSummary.TotalActive,
		BySeverity:  alertSummary.BySeverity,
		ByType:      alertSummary.ByType,
	}

	// Conflicts and file changes (best-effort)
	statusOutput := StatusOutput{}
	appendConflicts(&statusOutput)
	appendFileChanges(&statusOutput)
	output.Conflicts = statusOutput.Conflicts
	output.FileChanges = statusOutput.FileChanges

	// Agent Mail summary (best-effort)
	cwd, _ := os.Getwd()
	output.AgentMail, _, _ = fetchAgentMailData(cwd)

	// Attention feed summary (best-effort)
	output.Attention = buildSnapshotAttentionSummary(GetAttentionFeed())

	return output, nil
}

func dashboardAgentType(pane tmux.Pane) (string, bool) {
	agentType := agentTypeString(pane.Type)
	if agentType == "user" {
		return "", false
	}
	if agentType == "" {
		agentType = "unknown"
	}
	return agentType, true
}

// PrintDashboard outputs a dashboard-oriented view for AI orchestrators.
func PrintDashboard(jsonMode bool) error {
	output, err := GetDashboard()
	if err != nil {
		return err
	}

	if jsonMode {
		return encodeJSON(output)
	}

	return printDashboardMarkdown(*output)
}

func printDashboardMarkdown(output DashboardOutput) error {
	const (
		maxAlerts      = 10
		maxConflicts   = 10
		maxFileChanges = 20
		maxAgentRows   = 50
		maxMailAgents  = 25
	)

	totalPanes, userPanes, typeCounts := dashboardCounts(output.Agents)
	agentPanes := totalPanes - userPanes
	otherPanes := agentPanes - typeCounts["claude"] - typeCounts["codex"] - typeCounts["gemini"]

	var sb strings.Builder
	fmt.Fprintf(&sb, "# NTM Fleet Dashboard: %s\n\n", escapeMarkdownCell(output.Fleet, 120))
	fmt.Fprintf(&sb, "_Generated: %s_\n\n", output.GeneratedAt.Format(time.RFC3339))

	sb.WriteString("## System\n")
	sb.WriteString("| Key | Value |\n")
	sb.WriteString("|---|---|\n")
	fmt.Fprintf(&sb, "| NTM | %s |\n", escapeMarkdownCell(output.System.Version, 80))
	if output.System.Commit != "" {
		fmt.Fprintf(&sb, "| Commit | %s |\n", escapeMarkdownCell(output.System.Commit, 80))
	}
	if output.System.BuildDate != "" {
		fmt.Fprintf(&sb, "| Build Date | %s |\n", escapeMarkdownCell(output.System.BuildDate, 80))
	}
	fmt.Fprintf(&sb, "| Go | %s |\n", escapeMarkdownCell(output.System.GoVersion, 80))
	fmt.Fprintf(&sb, "| OS/Arch | %s/%s |\n", escapeMarkdownCell(output.System.OS, 40), escapeMarkdownCell(output.System.Arch, 40))
	fmt.Fprintf(&sb, "| tmux | %s |\n", yesNo(output.System.TmuxOK))
	sb.WriteString("\n")

	sb.WriteString("## Summary\n")
	sb.WriteString("| Key | Value |\n")
	sb.WriteString("|---|---|\n")
	fmt.Fprintf(&sb, "| Sessions | %d |\n", output.Summary.TotalSessions)
	fmt.Fprintf(&sb, "| Attached Sessions | %d |\n", output.Summary.AttachedCount)
	fmt.Fprintf(&sb, "| Panes (total) | %d |\n", totalPanes)
	fmt.Fprintf(&sb, "| Agent Panes | %d |\n", agentPanes)
	fmt.Fprintf(&sb, "| User Panes | %d |\n", userPanes)
	fmt.Fprintf(&sb, "| Claude | %d |\n", typeCounts["claude"])
	fmt.Fprintf(&sb, "| Codex | %d |\n", typeCounts["codex"])
	fmt.Fprintf(&sb, "| Gemini | %d |\n", typeCounts["gemini"])
	if otherPanes > 0 {
		fmt.Fprintf(&sb, "| Other Agents | %d |\n", otherPanes)
	}
	fmt.Fprintf(&sb, "| Alerts (active) | %d |\n", len(output.Alerts))
	fmt.Fprintf(&sb, "| Conflicts (30m) | %d |\n", len(output.Conflicts))
	fmt.Fprintf(&sb, "| File Changes (30m) | %d |\n", len(output.FileChanges))
	if output.Beads != nil && output.Beads.Available {
		fmt.Fprintf(&sb, "| Beads | Total %d (Ready %d, In Progress %d, Blocked %d) |\n", output.Beads.Total, output.Beads.Ready, output.Beads.InProgress, output.Beads.Blocked)
	} else if output.Beads != nil && !output.Beads.Available {
		fmt.Fprintf(&sb, "| Beads | unavailable (%s) |\n", escapeMarkdownCell(output.Beads.Reason, 120))
	}
	if output.AgentMail != nil {
		if output.AgentMail.Available {
			fmt.Fprintf(&sb, "| Agent Mail | %d unread (%d threads) |\n", output.AgentMail.TotalUnread, output.AgentMail.ThreadsKnown)
		} else {
			fmt.Fprintf(&sb, "| Agent Mail | unavailable (%s) |\n", escapeMarkdownCell(output.AgentMail.Reason, 120))
		}
	}
	fmt.Fprintf(&sb, "| Attention | %s |\n", escapeMarkdownCell(dashboardAttentionHeadline(output.Attention), 120))
	sb.WriteString("\n")

	sb.WriteString("## Attention\n")
	writeAttentionSection(&sb, output.Attention)
	sb.WriteString("\n")

	sb.WriteString("## Sessions\n")
	if len(output.Agents) == 0 {
		sb.WriteString("_No tmux sessions detected._\n\n")
	} else {
		sb.WriteString("| Session | Attached | Panes | User | Claude | Codex | Gemini | Other |\n")
		sb.WriteString("|---|---|---:|---:|---:|---:|---:|---:|\n")
		for _, sess := range output.Agents {
			sessTotal, sessUsers, sessCounts := dashboardCounts([]SnapshotSession{sess})
			sessAgents := sessTotal - sessUsers
			sessOther := sessAgents - sessCounts["claude"] - sessCounts["codex"] - sessCounts["gemini"]
			fmt.Fprintf(&sb, "| %s | %s | %d | %d | %d | %d | %d | %d |\n",
				escapeMarkdownCell(sess.Name, 80),
				yesNo(sess.Attached),
				sessTotal,
				sessUsers,
				sessCounts["claude"],
				sessCounts["codex"],
				sessCounts["gemini"],
				sessOther,
			)
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## Agents\n")
	writeAgentsTable(&sb, output.Agents, maxAgentRows)
	sb.WriteString("\n")

	sb.WriteString("## Alerts\n")
	writeAlertsTable(&sb, output.Alerts, maxAlerts)
	sb.WriteString("\n")

	sb.WriteString("## Beads\n")
	writeBeadsTables(&sb, output.Beads)
	sb.WriteString("\n")

	sb.WriteString("## Conflicts\n")
	writeConflictsTable(&sb, output.Conflicts, maxConflicts)
	sb.WriteString("\n")

	sb.WriteString("## File Changes\n")
	writeFileChangesTable(&sb, output.FileChanges, maxFileChanges)
	sb.WriteString("\n")

	sb.WriteString("## Agent Mail\n")
	writeAgentMailTable(&sb, output.AgentMail, maxMailAgents)
	sb.WriteString("\n")

	fmt.Print(sb.String())
	return nil
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func escapeMarkdownCell(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.TrimSpace(s)
	if maxLen > 0 {
		s = truncateStr(s, maxLen)
	}
	return s
}

func dashboardAttentionHeadline(summary *SnapshotAttentionSummary) string {
	if summary == nil {
		return "feed unavailable"
	}
	if summary.TotalEvents == 0 {
		return "clear"
	}

	parts := make([]string, 0, 2)
	if summary.ActionRequiredCount > 0 {
		parts = append(parts, fmt.Sprintf("%d action-required", summary.ActionRequiredCount))
	}
	if summary.InterestingCount > 0 {
		parts = append(parts, fmt.Sprintf("%d interesting", summary.InterestingCount))
	}
	if len(parts) == 0 {
		return "clear"
	}
	return strings.Join(parts, ", ")
}

// writeAttentionSection renders the attention summary for dashboard markdown.
func writeAttentionSection(sb *strings.Builder, attention *SnapshotAttentionSummary) {
	if attention == nil {
		sb.WriteString("_Attention feed not available._\n")
		return
	}
	if attention.TotalEvents == 0 {
		sb.WriteString("_No attention events. Feed is clear._\n")
		return
	}

	sb.WriteString("| Key | Value |\n")
	sb.WriteString("|---|---|\n")
	fmt.Fprintf(sb, "| Total Events | %d |\n", attention.TotalEvents)
	fmt.Fprintf(sb, "| Action Required | %d |\n", attention.ActionRequiredCount)
	fmt.Fprintf(sb, "| Interesting | %d |\n", attention.InterestingCount)
	sb.WriteString("\n")

	if len(attention.TopItems) > 0 {
		sb.WriteString("### Top Action Items\n")
		sb.WriteString("| Cursor | Category | Severity | Summary |\n")
		sb.WriteString("|---:|---|---|---|\n")
		for _, item := range attention.TopItems {
			fmt.Fprintf(sb, "| %d | %s | %s | %s |\n",
				item.Cursor,
				escapeMarkdownCell(item.Category, 20),
				escapeMarkdownCell(item.Severity, 10),
				escapeMarkdownCell(item.Summary, 80),
			)
		}
		sb.WriteString("\n")
	}

	if len(attention.NextSteps) > 0 {
		sb.WriteString("### Suggested Next Steps\n")
		for _, step := range attention.NextSteps {
			fmt.Fprintf(sb, "- `ntm %s` — %s\n", step.Args, escapeMarkdownCell(step.Reason, 100))
		}
		sb.WriteString("\n")
	}

	if len(attention.UnsupportedSignals) > 0 {
		sb.WriteString("### Unsupported Signals\n")
		sb.WriteString("_The following signals are not yet implemented:_ ")
		sb.WriteString(strings.Join(attention.UnsupportedSignals, ", "))
		sb.WriteString("\n")
	}
}

func dashboardCounts(sessions []SnapshotSession) (totalPanes, userPanes int, typeCounts map[string]int) {
	typeCounts = map[string]int{
		"claude": 0,
		"codex":  0,
		"gemini": 0,
		"user":   0,
	}
	for _, sess := range sessions {
		for _, agent := range sess.Agents {
			totalPanes++
			typ := strings.TrimSpace(strings.ToLower(agent.Type))
			if typ == "" {
				typ = "unknown"
			}
			if _, ok := typeCounts[typ]; ok {
				typeCounts[typ]++
			}
			if typ == "user" {
				userPanes++
			}
		}
	}
	return totalPanes, userPanes, typeCounts
}

func writeAgentsTable(sb *strings.Builder, sessions []SnapshotSession, maxRows int) {
	totalRows := 0
	for _, sess := range sessions {
		totalRows += len(sess.Agents)
	}
	if totalRows == 0 {
		sb.WriteString("_No agents detected._\n")
		return
	}

	sb.WriteString("| Session | Pane | Type | Variant | State |\n")
	sb.WriteString("|---|---|---|---|---|\n")

	written := 0
	for _, sess := range sessions {
		for _, agent := range sess.Agents {
			if maxRows > 0 && written >= maxRows {
				fmt.Fprintf(sb, "\n_Truncated: showing %d of %d panes._\n", written, totalRows)
				return
			}
			fmt.Fprintf(sb, "| %s | %s | %s | %s | %s |\n",
				escapeMarkdownCell(sess.Name, 80),
				escapeMarkdownCell(agent.Pane, 40),
				escapeMarkdownCell(agent.Type, 40),
				escapeMarkdownCell(agent.Variant, 40),
				escapeMarkdownCell(agent.State, 40))
			written++
		}
	}
}

func writeAlertsTable(sb *strings.Builder, alertsList []AlertInfo, maxRows int) {
	if len(alertsList) == 0 {
		sb.WriteString("_No active alerts._\n")
		return
	}
	sb.WriteString("| Severity | ID | Type | Session | Pane | Message |\n")
	sb.WriteString("|---|---|---|---|---|---|\n")

	written := 0
	for _, a := range alertsList {
		if maxRows > 0 && written >= maxRows {
			fmt.Fprintf(sb, "\n_Truncated: showing %d of %d alerts._\n", written, len(alertsList))
			return
		}
		fmt.Fprintf(sb, "| %s | %s | %s | %s | %s | %s |\n",
			escapeMarkdownCell(a.Severity, 16),
			escapeMarkdownCell(a.ID, 40),
			escapeMarkdownCell(a.Type, 24),
			escapeMarkdownCell(a.Session, 80),
			escapeMarkdownCell(a.Pane, 40),
			escapeMarkdownCell(a.Message, 160))
		written++
	}
}

func writeBeadsTables(sb *strings.Builder, beads *bv.BeadsSummary) {
	if beads == nil {
		sb.WriteString("_Beads summary unavailable._\n")
		return
	}
	if !beads.Available {
		fmt.Fprintf(sb, "_Beads summary unavailable: %s._\n", escapeMarkdownCell(beads.Reason, 160))
		return
	}

	sb.WriteString("| Total | Open | In Progress | Blocked | Ready | Closed |\n")
	sb.WriteString("|---:|---:|---:|---:|---:|---:|\n")
	fmt.Fprintf(sb, "| %d | %d | %d | %d | %d | %d |\n\n", beads.Total, beads.Open, beads.InProgress, beads.Blocked, beads.Ready, beads.Closed)

	if len(beads.ReadyPreview) > 0 {
		sb.WriteString("### Ready\n")
		sb.WriteString("| ID | Priority | Title |\n")
		sb.WriteString("|---|---|---|\n")
		for _, b := range beads.ReadyPreview {
			fmt.Fprintf(sb, "| %s | %s | %s |\n",
				escapeMarkdownCell(b.ID, 32),
				escapeMarkdownCell(b.Priority, 8),
				escapeMarkdownCell(b.Title, 140))
		}
		sb.WriteString("\n")
	} else {
		sb.WriteString("_No ready beads._\n\n")
	}

	if len(beads.InProgressList) > 0 {
		sb.WriteString("### In Progress\n")
		sb.WriteString("| ID | Assignee | Updated |\n")
		sb.WriteString("|---|---|---|\n")
		for _, b := range beads.InProgressList {
			updated := ""
			if !b.UpdatedAt.IsZero() {
				updated = b.UpdatedAt.UTC().Format(time.RFC3339)
			}
			fmt.Fprintf(sb, "| %s | %s | %s |\n",
				escapeMarkdownCell(b.ID, 32),
				escapeMarkdownCell(b.Assignee, 40),
				escapeMarkdownCell(updated, 40))
		}
		sb.WriteString("\n")
	} else {
		sb.WriteString("_No in-progress beads._\n")
	}
}

func writeConflictsTable(sb *strings.Builder, conflicts []tracker.Conflict, maxRows int) {
	if len(conflicts) == 0 {
		sb.WriteString("_No conflicts detected._\n")
		return
	}

	ordered := make([]tracker.Conflict, len(conflicts))
	copy(ordered, conflicts)
	sort.SliceStable(ordered, func(i, j int) bool {
		li, lj := ordered[i].LastAt, ordered[j].LastAt
		if !li.Equal(lj) {
			return li.After(lj)
		}
		return ordered[i].Path < ordered[j].Path
	})

	sb.WriteString("| Severity | Path | Agents | Last At |\n")
	sb.WriteString("|---|---|---|---|\n")

	written := 0
	for _, c := range ordered {
		if maxRows > 0 && written >= maxRows {
			fmt.Fprintf(sb, "\n_Truncated: showing %d of %d conflicts._\n", written, len(ordered))
			return
		}
		agents := append([]string(nil), c.Agents...)
		sort.Strings(agents)
		lastAt := ""
		if !c.LastAt.IsZero() {
			lastAt = c.LastAt.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(sb, "| %s | %s | %s | %s |\n",
			escapeMarkdownCell(c.Severity, 16),
			escapeMarkdownCell(c.Path, 160),
			escapeMarkdownCell(strings.Join(agents, ", "), 120),
			escapeMarkdownCell(lastAt, 40))
		written++
	}
}

func writeFileChangesTable(sb *strings.Builder, changes []FileChangeInfo, maxRows int) {
	if len(changes) == 0 {
		sb.WriteString("_No recent file changes detected._\n")
		return
	}

	ordered := make([]FileChangeInfo, len(changes))
	copy(ordered, changes)
	sort.SliceStable(ordered, func(i, j int) bool {
		ai, aj := ordered[i].At, ordered[j].At
		if !ai.Equal(aj) {
			return ai.After(aj)
		}
		if ordered[i].Path != ordered[j].Path {
			return ordered[i].Path < ordered[j].Path
		}
		return ordered[i].Session < ordered[j].Session
	})

	sb.WriteString("| At | Type | Path | Session | Agents |\n")
	sb.WriteString("|---|---|---|---|---|\n")

	written := 0
	for _, c := range ordered {
		if maxRows > 0 && written >= maxRows {
			fmt.Fprintf(sb, "\n_Truncated: showing %d of %d file changes._\n", written, len(ordered))
			return
		}
		at := ""
		if !c.At.IsZero() {
			at = c.At.UTC().Format(time.RFC3339)
		}
		agents := append([]string(nil), c.Agents...)
		sort.Strings(agents)
		fmt.Fprintf(sb, "| %s | %s | %s | %s | %s |\n",
			escapeMarkdownCell(at, 40),
			escapeMarkdownCell(c.Type, 16),
			escapeMarkdownCell(c.Path, 160),
			escapeMarkdownCell(c.Session, 80),
			escapeMarkdownCell(strings.Join(agents, ", "), 120))
		written++
	}
}

func writeAgentMailTable(sb *strings.Builder, mail *SnapshotAgentMail, maxRows int) {
	if mail == nil {
		sb.WriteString("_Agent Mail summary unavailable._\n")
		return
	}
	if !mail.Available {
		fmt.Fprintf(sb, "_Agent Mail unavailable: %s._\n", escapeMarkdownCell(mail.Reason, 160))
		return
	}
	if len(mail.Agents) == 0 {
		sb.WriteString("_No Agent Mail agents found._\n")
		return
	}

	type mailRow struct {
		name string
		s    SnapshotAgentMailStats
	}
	rows := make([]mailRow, 0, len(mail.Agents))
	for name, s := range mail.Agents {
		if s.Unread == 0 && s.PendingAck == 0 {
			continue
		}
		rows = append(rows, mailRow{name: name, s: s})
	}
	if len(rows) == 0 {
		sb.WriteString("_No unread Agent Mail messages._\n")
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].s.Unread != rows[j].s.Unread {
			return rows[i].s.Unread > rows[j].s.Unread
		}
		if rows[i].s.PendingAck != rows[j].s.PendingAck {
			return rows[i].s.PendingAck > rows[j].s.PendingAck
		}
		return rows[i].name < rows[j].name
	})

	fmt.Fprintf(sb, "_Project: %s_\n\n", escapeMarkdownCell(mail.Project, 160))
	sb.WriteString("| Agent | Pane | Unread | Pending Ack |\n")
	sb.WriteString("|---|---|---:|---:|\n")

	written := 0
	for _, r := range rows {
		if maxRows > 0 && written >= maxRows {
			fmt.Fprintf(sb, "\n_Truncated: showing %d of %d agents with unread mail._\n", written, len(rows))
			return
		}
		fmt.Fprintf(sb, "| %s | %s | %d | %d |\n",
			escapeMarkdownCell(r.name, 60),
			escapeMarkdownCell(r.s.Pane, 40),
			r.s.Unread,
			r.s.PendingAck)
		written++
	}
}
