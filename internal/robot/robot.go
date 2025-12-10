// Package robot provides machine-readable output for AI agents and automation.
// Use --robot-* flags to get JSON output suitable for piping to other tools.
package robot

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tracker"
)

// Build info - these will be set by the caller from cli package
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
	BuiltBy = "unknown"
)

// Global state tracker for delta snapshots
var stateTracker = tracker.New()

// SessionInfo contains machine-readable session information
type SessionInfo struct {
	Name      string     `json:"name"`
	Exists    bool       `json:"exists"`
	Attached  bool       `json:"attached,omitempty"`
	Windows   int        `json:"windows,omitempty"`
	Panes     int        `json:"panes,omitempty"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
	Agents    []Agent    `json:"agents,omitempty"`
}

// Agent represents an AI agent in a session
type Agent struct {
	Type     string `json:"type"` // claude, codex, gemini
	Pane     string `json:"pane"`
	Window   int    `json:"window"`
	PaneIdx  int    `json:"pane_idx"`
	IsActive bool   `json:"is_active"`
}

// SystemInfo contains system and runtime information
type SystemInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	TmuxOK    bool   `json:"tmux_available"`
}

// StatusOutput is the structured output for robot-status
type StatusOutput struct {
	GeneratedAt   time.Time       `json:"generated_at"`
	System        SystemInfo      `json:"system"`
	Sessions      []SessionInfo   `json:"sessions"`
	Summary       StatusSummary   `json:"summary"`
	Beads         *BeadsSummary   `json:"beads,omitempty"`
	GraphMetrics  *GraphMetrics   `json:"graph_metrics,omitempty"`
}

// GraphMetrics provides bv graph analysis metrics for status output
type GraphMetrics struct {
	TopBottlenecks []BottleneckInfo `json:"top_bottlenecks,omitempty"`
	Keystones      int              `json:"keystones_count"`
	HealthStatus   string           `json:"health_status"`  // "ok", "warning", "critical"
	DriftMessage   string           `json:"drift_message,omitempty"`
}

// BottleneckInfo represents a bottleneck issue with its score
type BottleneckInfo struct {
	ID    string  `json:"id"`
	Title string  `json:"title,omitempty"`
	Score float64 `json:"score"`
}

// StatusSummary provides aggregate stats
type StatusSummary struct {
	TotalSessions   int `json:"total_sessions"`
	TotalAgents     int `json:"total_agents"`
	AttachedCount   int `json:"attached_count"`
	ClaudeCount     int `json:"claude_count"`
	CodexCount      int `json:"codex_count"`
	GeminiCount     int `json:"gemini_count"`
	CursorCount     int `json:"cursor_count"`
	WindsurfCount   int `json:"windsurf_count"`
	AiderCount      int `json:"aider_count"`
}

// PlanOutput provides an execution plan for what can be done
type PlanOutput struct {
	GeneratedAt    time.Time          `json:"generated_at"`
	Recommendation string             `json:"recommendation"`
	Actions        []PlanAction       `json:"actions"`
	BeadActions    []BeadAction       `json:"bead_actions,omitempty"`
	Warnings       []string           `json:"warnings,omitempty"`
}

// BeadAction represents a recommended action based on bead priority analysis
type BeadAction struct {
	BeadID        string         `json:"bead_id"`
	Title         string         `json:"title"`
	Priority      int            `json:"priority"`
	Impact        float64        `json:"impact_score"`
	Reasoning     []string       `json:"reasoning"`
	Command       string         `json:"command"`              // e.g., "bd update ntm-xyz --status in_progress"
	IsReady       bool           `json:"is_ready"`             // true if no blockers
	BlockedBy     []string       `json:"blocked_by,omitempty"` // blocking bead IDs
	GraphPosition *GraphPosition `json:"graph_position,omitempty"`
}

// GraphPosition represents the position of an issue in the dependency graph
type GraphPosition struct {
	IsBottleneck    bool    `json:"is_bottleneck,omitempty"`
	BottleneckScore float64 `json:"bottleneck_score,omitempty"`
	IsKeystone      bool    `json:"is_keystone,omitempty"`
	KeystoneScore   float64 `json:"keystone_score,omitempty"`
	IsHub           bool    `json:"is_hub,omitempty"`
	HubScore        float64 `json:"hub_score,omitempty"`
	IsAuthority     bool    `json:"is_authority,omitempty"`
	AuthorityScore  float64 `json:"authority_score,omitempty"`
	Summary         string  `json:"summary,omitempty"` // Human-readable summary
}

// PlanAction is a suggested action
type PlanAction struct {
	Priority    int      `json:"priority"` // 1=high, 2=medium, 3=low
	Command     string   `json:"command"`
	Description string   `json:"description"`
	Args        []string `json:"args,omitempty"`
}

// PrintHelp outputs AI agent help documentation
func PrintHelp() {
	help := `ntm (Named Tmux Manager) AI Agent Interface
=============================================
This tool helps AI agents manage tmux sessions with multiple coding assistants.

Commands for AI Agents:
-----------------------

--robot-status
    Outputs JSON with all session information and agent counts.
    Key fields:
    - sessions: Array of active sessions with their agents
    - summary: Aggregate counts (total_agents, claude_count, etc.)
    - system: Version, OS, tmux availability

--robot-plan
    Outputs a recommended execution plan based on current state.
    Key fields:
    - recommendation: What to do first
    - actions: Prioritized list of commands to run
    - warnings: Issues that need attention

--robot-sessions
    Outputs minimal session list for quick lookup.
    Faster than --robot-status when you only need names.

--robot-send <session> --msg="prompt" [options]
    Send prompts to multiple panes atomically with structured result.
    Options:
    --all          Send to all panes (including user)
    --panes=X,Y,Z  Specific pane indices
    --type=claude  Filter by agent type (claude, codex, gemini)
    --exclude=X,Y  Exclude specific panes
    --delay-ms=100 Stagger sends to avoid thundering herd

    Output includes:
    - session: Target session name
    - sent_at: Timestamp of send operation
    - targets: Panes that were targeted
    - successful: Panes where send succeeded
    - failed: Array of {pane, error} for failures
    - message_preview: First 50 chars of message

--robot-version
    Outputs version info as JSON.

Common Workflows:
-----------------

1) Start a coding session:
   ntm spawn myproject --cc=2 --cod=1 --gem=1 --json

2) Check session state:
   ntm status --robot-status

3) Send a prompt to all agents:
   ntm send myproject --all "implement feature X"

4) Get output from a specific agent:
   ntm copy myproject:1 --last=50

Tips for AI Agents:
-------------------
- Use --json flag on spawn/create for structured output
- Parse ntm status --robot-status to understand current state
- Use ntm send --all for broadcast, --pane=N for targeted
- Output is always UTF-8 JSON, one object per line where applicable
`
	fmt.Println(help)
}

// PrintStatus outputs machine-readable status
func PrintStatus() error {
	output := StatusOutput{
		GeneratedAt: time.Now().UTC(),
		System: SystemInfo{
			Version:   Version,
			Commit:    Commit,
			BuildDate: Date,
			GoVersion: runtime.Version(),
			OS:        runtime.GOOS,
			Arch:      runtime.GOARCH,
			TmuxOK:    tmux.IsInstalled(),
		},
		Sessions: []SessionInfo{},
		Summary:  StatusSummary{},
	}

	// Get all sessions
	sessions, err := tmux.ListSessions()
	if err != nil {
		// tmux not running is not an error for status
		return encodeJSON(output)
	}

	for _, sess := range sessions {
		info := SessionInfo{
			Name:     sess.Name,
			Exists:   true,
			Attached: sess.Attached,
			Windows:  sess.Windows,
		}

		// Try to get agents from panes
		panes, err := tmux.GetPanes(sess.Name)
		if err == nil {
			info.Panes = len(panes)
			for _, pane := range panes {
				agent := Agent{
					Pane:     pane.ID,
					Window:   0, // GetPanes doesn't include window index
					PaneIdx:  pane.Index,
					IsActive: pane.Active,
				}

				// Try to detect agent type from pane title or content
				agent.Type = detectAgentType(pane.Title)
				info.Agents = append(info.Agents, agent)

				// Update summary counts
				switch agent.Type {
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
			}
		}

		output.Sessions = append(output.Sessions, info)
		output.Summary.TotalSessions++
		if sess.Attached {
			output.Summary.AttachedCount++
		}
	}

	// Add beads summary if bv is available
	if bv.IsInstalled() {
		output.Beads = getBeadsSummary()
		output.GraphMetrics = getGraphMetrics()
	}

	return encodeJSON(output)
}

// getGraphMetrics returns bv graph analysis metrics
func getGraphMetrics() *GraphMetrics {
	metrics := &GraphMetrics{
		HealthStatus: "unknown",
	}

	// Get health summary (drift + bottleneck count)
	health, err := bv.GetHealthSummary()
	if err == nil && health != nil {
		switch health.DriftStatus {
		case bv.DriftOK:
			metrics.HealthStatus = "ok"
		case bv.DriftWarning:
			metrics.HealthStatus = "warning"
		case bv.DriftCritical:
			metrics.HealthStatus = "critical"
		default:
			metrics.HealthStatus = "unknown"
		}
		metrics.DriftMessage = health.DriftMessage
	}

	// Get top bottlenecks
	bottlenecks, err := bv.GetTopBottlenecks(3)
	if err == nil {
		for _, b := range bottlenecks {
			metrics.TopBottlenecks = append(metrics.TopBottlenecks, BottleneckInfo{
				ID:    b.ID,
				Score: b.Value,
			})
		}
	}

	// Get keystone count
	insights, err := bv.GetInsights()
	if err == nil && insights != nil {
		metrics.Keystones = len(insights.Keystones)
	}

	return metrics
}

// PrintVersion outputs version as JSON
func PrintVersion() error {
	info := struct {
		Version   string `json:"version"`
		Commit    string `json:"commit"`
		BuildDate string `json:"build_date"`
		BuiltBy   string `json:"built_by"`
		GoVersion string `json:"go_version"`
		OS        string `json:"os"`
		Arch      string `json:"arch"`
	}{
		Version:   Version,
		Commit:    Commit,
		BuildDate: Date,
		BuiltBy:   BuiltBy,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
	return encodeJSON(info)
}

// PrintSessions outputs minimal session list
func PrintSessions() error {
	sessions, err := tmux.ListSessions()
	if err != nil {
		return encodeJSON([]SessionInfo{})
	}

	output := make([]SessionInfo, 0, len(sessions))
	for _, sess := range sessions {
		output = append(output, SessionInfo{
			Name:     sess.Name,
			Exists:   true,
			Attached: sess.Attached,
			Windows:  sess.Windows,
		})
	}
	return encodeJSON(output)
}

// PrintPlan outputs an execution plan
func PrintPlan() error {
	plan := PlanOutput{
		GeneratedAt: time.Now().UTC(),
		Actions:     []PlanAction{},
		BeadActions: []BeadAction{},
	}

	// Check tmux availability
	if !tmux.IsInstalled() {
		plan.Recommendation = "Install tmux first"
		plan.Warnings = append(plan.Warnings, "tmux is not installed or not in PATH")
		plan.Actions = append(plan.Actions, PlanAction{
			Priority:    1,
			Command:     "brew install tmux",
			Description: "Install tmux using Homebrew (macOS)",
		})
		return encodeJSON(plan)
	}

	// Check for existing sessions
	sessions, _ := tmux.ListSessions()

	if len(sessions) == 0 {
		plan.Recommendation = "Create your first coding session"
		plan.Actions = append(plan.Actions, PlanAction{
			Priority:    1,
			Command:     "ntm spawn myproject --cc=2",
			Description: "Create session with 2 Claude Code agents",
			Args:        []string{"spawn", "myproject", "--cc=2"},
		})
		plan.Actions = append(plan.Actions, PlanAction{
			Priority:    2,
			Command:     "ntm tutorial",
			Description: "Learn NTM with an interactive tutorial",
			Args:        []string{"tutorial"},
		})
	} else {
		plan.Recommendation = "Attach to an existing session or create a new one"

		// Find unattached sessions
		for _, sess := range sessions {
			if !sess.Attached {
				plan.Actions = append(plan.Actions, PlanAction{
					Priority:    1,
					Command:     fmt.Sprintf("ntm attach %s", sess.Name),
					Description: fmt.Sprintf("Attach to session '%s' (%d windows)", sess.Name, sess.Windows),
					Args:        []string{"attach", sess.Name},
				})
			}
		}

		plan.Actions = append(plan.Actions, PlanAction{
			Priority:    2,
			Command:     "ntm palette",
			Description: "Open command palette for quick actions",
			Args:        []string{"palette"},
		})
		plan.Actions = append(plan.Actions, PlanAction{
			Priority:    3,
			Command:     "ntm dashboard",
			Description: "Open visual session dashboard",
			Args:        []string{"dashboard"},
		})
	}

	// Add bead-based recommendations from bv priority analysis
	beadActions, beadWarnings := getBeadRecommendations(5) // Top 5 recommendations
	plan.BeadActions = beadActions
	plan.Warnings = append(plan.Warnings, beadWarnings...)

	// Update recommendation if there are high-impact beads to work on
	if len(plan.BeadActions) > 0 && plan.BeadActions[0].IsReady {
		plan.Recommendation = fmt.Sprintf("Work on high-impact bead: %s", plan.BeadActions[0].Title)
	}

	return encodeJSON(plan)
}

// getBeadRecommendations returns recommended bead actions from bv priority analysis
func getBeadRecommendations(limit int) ([]BeadAction, []string) {
	var actions []BeadAction
	var warnings []string

	// Check if bv is available
	if !bv.IsInstalled() {
		warnings = append(warnings, "bv (beads_viewer) not installed - install for bead-based recommendations")
		return actions, warnings
	}

	// Get priority recommendations from bv
	recommendations, err := bv.GetNextActions(limit)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("failed to get bv priority: %v", err))
		return actions, warnings
	}

	// Get ready issues to check blockers
	readyIssues := getReadyIssueIDs()

	// Collect issue IDs for batch graph position lookup
	var issueIDs []string
	for _, rec := range recommendations {
		issueIDs = append(issueIDs, rec.IssueID)
	}

	// Get graph positions in batch for efficiency
	graphPositions, graphErr := bv.GetGraphPositionsBatch(issueIDs)
	if graphErr != nil {
		warnings = append(warnings, fmt.Sprintf("failed to get graph positions: %v", graphErr))
	}

	// Convert bv recommendations to BeadActions
	for _, rec := range recommendations {
		isReady := readyIssues[rec.IssueID]

		action := BeadAction{
			BeadID:    rec.IssueID,
			Title:     rec.Title,
			Priority:  rec.SuggestedPriority,
			Impact:    rec.ImpactScore,
			Reasoning: rec.Reasoning,
			Command:   fmt.Sprintf("bd update %s --status in_progress", rec.IssueID),
			IsReady:   isReady,
		}

		// Add graph position if available
		if graphPositions != nil {
			if pos, ok := graphPositions[rec.IssueID]; ok && pos != nil {
				action.GraphPosition = &GraphPosition{
					IsBottleneck:    pos.IsBottleneck,
					BottleneckScore: pos.BottleneckScore,
					IsKeystone:      pos.IsKeystone,
					KeystoneScore:   pos.KeystoneScore,
					IsHub:           pos.IsHub,
					HubScore:        pos.HubScore,
					IsAuthority:     pos.IsAuthority,
					AuthorityScore:  pos.AuthorityScore,
					Summary:         pos.Summary,
				}
			}
		}

		// If not ready, try to determine blockers
		if !isReady {
			blockers := getBlockersForIssue(rec.IssueID)
			action.BlockedBy = blockers
		}

		actions = append(actions, action)
	}

	return actions, warnings
}

// getReadyIssueIDs returns a set of issue IDs that are ready (unblocked)
func getReadyIssueIDs() map[string]bool {
	ready := make(map[string]bool)

	// Try to run bd ready --json to get ready issues
	cmd := exec.Command("bd", "ready", "--json")
	output, err := cmd.Output()
	if err != nil {
		return ready
	}

	// Parse JSON array of issues
	var issues []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(output, &issues); err != nil {
		return ready
	}

	for _, issue := range issues {
		ready[issue.ID] = true
	}

	return ready
}

// getBlockersForIssue returns the IDs of issues blocking the given issue
func getBlockersForIssue(issueID string) []string {
	var blockers []string

	// Try to run bd show <id> --json to get dependencies
	cmd := exec.Command("bd", "show", issueID, "--json")
	output, err := cmd.Output()
	if err != nil {
		return blockers
	}

	// Parse JSON - bd show returns an array with one element
	var issues []struct {
		Dependencies []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal(output, &issues); err != nil {
		return blockers
	}

	if len(issues) > 0 {
		for _, dep := range issues[0].Dependencies {
			// Only include non-closed dependencies as blockers
			if dep.Status != "closed" {
				blockers = append(blockers, dep.ID)
			}
		}
	}

	return blockers
}

func detectAgentType(title string) string {
	// Try to detect from pane title
	switch {
	case contains(title, "claude"):
		return "claude"
	case contains(title, "codex"):
		return "codex"
	case contains(title, "gemini"):
		return "gemini"
	case contains(title, "cursor"):
		return "cursor"
	case contains(title, "windsurf"):
		return "windsurf"
	case contains(title, "aider"):
		return "aider"
	default:
		return "unknown"
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		len(s) > 0 && containsLower(s, substr))
}

func containsLower(s, substr string) bool {
	s = toLower(s)
	substr = toLower(substr)
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func toLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

func encodeJSON(v interface{}) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(v)
}

// TailOutput is the structured output for --robot-tail
type TailOutput struct {
	Session    string               `json:"session"`
	CapturedAt time.Time            `json:"captured_at"`
	Panes      map[string]PaneOutput `json:"panes"`
}

// PaneOutput contains captured output from a single pane
type PaneOutput struct {
	Type      string   `json:"type"`
	State     string   `json:"state"` // active, idle, unknown
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated"`
}

// PrintTail outputs recent pane output for AI consumption
func PrintTail(session string, lines int, paneFilter []string) error {
	if !tmux.SessionExists(session) {
		return fmt.Errorf("session '%s' not found", session)
	}

	panes, err := tmux.GetPanes(session)
	if err != nil {
		return fmt.Errorf("failed to get panes: %w", err)
	}

	output := TailOutput{
		Session:    session,
		CapturedAt: time.Now().UTC(),
		Panes:      make(map[string]PaneOutput),
	}

	// Build pane filter map
	filterMap := make(map[string]bool)
	for _, p := range paneFilter {
		filterMap[p] = true
	}
	hasFilter := len(filterMap) > 0

	for _, pane := range panes {
		paneKey := fmt.Sprintf("%d", pane.Index)

		// Skip if filter is set and this pane is not in it
		if hasFilter && !filterMap[paneKey] && !filterMap[pane.ID] {
			continue
		}

		// Capture pane output
		captured, err := tmux.CapturePaneOutput(pane.ID, lines)
		if err != nil {
			// Include empty output on error
			output.Panes[paneKey] = PaneOutput{
				Type:      detectAgentType(pane.Title),
				State:     "unknown",
				Lines:     []string{},
				Truncated: false,
			}
			continue
		}

		// Strip ANSI codes and split into lines
		cleanOutput := stripANSI(captured)
		outputLines := splitLines(cleanOutput)

		// Detect state from output
		state := detectState(outputLines, pane.Title)

		// Check if truncated (we captured exactly the requested lines)
		truncated := len(outputLines) >= lines

		output.Panes[paneKey] = PaneOutput{
			Type:      detectAgentType(pane.Title),
			State:     state,
			Lines:     outputLines,
			Truncated: truncated,
		}
	}

	return encodeJSON(output)
}

// stripANSI removes ANSI escape sequences from text
func stripANSI(s string) string {
	var result []byte
	inEscape := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			// End of escape sequence when we hit a letter
			if (s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z') {
				inEscape = false
			}
			continue
		}
		result = append(result, s[i])
	}
	return string(result)
}

// splitLines splits text into lines, preserving empty lines
func splitLines(s string) []string {
	if s == "" {
		return []string{}
	}
	// Normalize line endings
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")

	lines := strings.Split(s, "\n")
	// Remove trailing empty line if present
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// detectState attempts to determine if agent is active, idle, or unknown
func detectState(lines []string, title string) string {
	if len(lines) == 0 {
		return "unknown"
	}

	// Check the last few non-empty lines for prompt patterns
	lastLine := ""
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-5; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			lastLine = line
			break
		}
	}

	if lastLine == "" {
		return "unknown"
	}

	// Detect idle prompts
	idlePatterns := []string{
		"claude>", "Claude>", "claude >",
		"codex>", "Codex>",
		"gemini>", "Gemini>",
		"$ ", "% ", "❯ ", "> ",
		">>> ", "...", // Python prompts
	}

	for _, pattern := range idlePatterns {
		if strings.HasSuffix(lastLine, pattern) || lastLine == strings.TrimSpace(pattern) {
			return "idle"
		}
	}

	// Detect error states
	errorPatterns := []string{
		"rate limit", "Rate limit", "429",
		"error:", "Error:", "ERROR:",
		"failed:", "Failed:",
		"panic:", "PANIC:",
		"fatal:", "Fatal:", "FATAL:",
	}

	for _, pattern := range errorPatterns {
		if strings.Contains(lastLine, pattern) {
			return "error"
		}
	}

	// If we see recent output that doesn't match idle, assume active
	return "active"
}

// SnapshotOutput provides complete system state for AI orchestration
type SnapshotOutput struct {
	Timestamp    string            `json:"ts"`
	Sessions     []SnapshotSession `json:"sessions"`
	BeadsSummary *BeadsSummary     `json:"beads_summary,omitempty"`
	MailUnread   int               `json:"mail_unread,omitempty"`
	Alerts       []string          `json:"alerts"`
}

// SnapshotSession represents a session in the snapshot
type SnapshotSession struct {
	Name     string          `json:"name"`
	Attached bool            `json:"attached"`
	Agents   []SnapshotAgent `json:"agents"`
}

// SnapshotAgent represents an agent in the snapshot
type SnapshotAgent struct {
	Pane               string  `json:"pane"`
	Type               string  `json:"type"`
	TypeConfidence     float64 `json:"type_confidence"`
	TypeMethod         string  `json:"type_method"`
	State              string  `json:"state"`
	LastOutputAgeSec   int     `json:"last_output_age_sec"`
	OutputTailLines    int     `json:"output_tail_lines"`
	CurrentBead        *string `json:"current_bead"`
	PendingMail        int     `json:"pending_mail"`
}

// BeadsSummary provides issue tracking stats for snapshot
type BeadsSummary struct {
	Available      bool             `json:"available"`
	Reason         string           `json:"reason,omitempty"` // Reason if not available
	Project        string           `json:"project,omitempty"`
	Total          int              `json:"total,omitempty"`
	Open           int              `json:"open,omitempty"`
	InProgress     int              `json:"in_progress,omitempty"`
	Blocked        int              `json:"blocked,omitempty"`
	Ready          int              `json:"ready,omitempty"`
	Closed         int              `json:"closed,omitempty"`
	ReadyPreview   []BeadPreview    `json:"ready_preview,omitempty"`
	InProgressList []BeadInProgress `json:"in_progress_list,omitempty"`
}

// BeadPreview is a minimal bead representation for ready items
type BeadPreview struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Priority string `json:"priority"` // e.g., "P0", "P1"
}

// BeadInProgress represents an in-progress bead with assignee
type BeadInProgress struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Assignee string `json:"assignee,omitempty"`
}

// BeadLimit controls how many ready/in-progress beads to include in snapshot
var BeadLimit = 5

// PrintSnapshot outputs complete system state for AI orchestration
func PrintSnapshot() error {
	output := SnapshotOutput{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Sessions:  []SnapshotSession{},
		Alerts:    []string{},
	}

	// Check tmux availability
	if !tmux.IsInstalled() {
		output.Alerts = append(output.Alerts, "tmux is not installed")
		return encodeJSON(output)
	}

	// Get all sessions
	sessions, err := tmux.ListSessions()
	if err != nil {
		// No sessions is not an error for snapshot
		return encodeJSON(output)
	}

	for _, sess := range sessions {
		snapSession := SnapshotSession{
			Name:     sess.Name,
			Attached: sess.Attached,
			Agents:   []SnapshotAgent{},
		}

		// Get panes for this session
		panes, err := tmux.GetPanes(sess.Name)
		if err != nil {
			output.Alerts = append(output.Alerts, fmt.Sprintf("failed to get panes for %s: %v", sess.Name, err))
			continue
		}

		for _, pane := range panes {
			// Capture output for state detection and enhanced type detection
			captured := ""
			capturedErr := error(nil)
			captured, capturedErr = tmux.CapturePaneOutput(pane.ID, 50)

			// Use enhanced agent type detection
			detection := DetectAgentTypeEnhanced(pane, captured)

			agent := SnapshotAgent{
				Pane:             fmt.Sprintf("%d.%d", 0, pane.Index),
				Type:             detection.Type,
				TypeConfidence:   detection.Confidence,
				TypeMethod:       string(detection.Method),
				State:            "unknown",
				LastOutputAgeSec: -1, // Unknown without pane_last_activity
				OutputTailLines:  0,
				CurrentBead:      nil,
				PendingMail:      0,
			}

			// Process captured output for state
			if capturedErr == nil {
				lines := splitLines(stripANSI(captured))
				agent.OutputTailLines = len(lines)
				agent.State = detectState(lines, pane.Title)
			}

			snapSession.Agents = append(snapSession.Agents, agent)
		}

		output.Sessions = append(output.Sessions, snapSession)
	}

	// Try to get beads summary
	beads := getBeadsSummary()
	if beads != nil {
		output.BeadsSummary = beads
	}

	// Add alerts for detected issues
	for _, sess := range output.Sessions {
		for _, agent := range sess.Agents {
			if agent.State == "error" {
				output.Alerts = append(output.Alerts, fmt.Sprintf("agent %s in %s has error state", agent.Pane, sess.Name))
			}
		}
	}

	return encodeJSON(output)
}

// agentTypeString converts AgentType to string for JSON
func agentTypeString(t tmux.AgentType) string {
	switch t {
	case tmux.AgentClaude:
		return "claude"
	case tmux.AgentCodex:
		return "codex"
	case tmux.AgentGemini:
		return "gemini"
	case tmux.AgentUser:
		return "user"
	default:
		return "unknown"
	}
}

// getBeadsSummary attempts to get bead statistics from bd command
func getBeadsSummary() *BeadsSummary {
	// Try to run bd stats --json to get summary
	cmd := exec.Command("bd", "stats", "--json")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	// Parse the JSON output - bd returns fields with "_issues" suffix
	var stats struct {
		Open       int `json:"open_issues"`
		InProgress int `json:"in_progress_issues"`
		Blocked    int `json:"blocked_issues"`
		Ready      int `json:"ready_issues"`
	}
	if err := json.Unmarshal(output, &stats); err != nil {
		return nil
	}

	return &BeadsSummary{
		Open:       stats.Open,
		InProgress: stats.InProgress,
		Blocked:    stats.Blocked,
		Ready:      stats.Ready,
	}
}

// SendOutput is the structured output for --robot-send
type SendOutput struct {
	Session        string      `json:"session"`
	SentAt         time.Time   `json:"sent_at"`
	Targets        []string    `json:"targets"`
	Successful     []string    `json:"successful"`
	Failed         []SendError `json:"failed"`
	MessagePreview string      `json:"message_preview"`
}

// SendError represents a failed send attempt
type SendError struct {
	Pane  string `json:"pane"`
	Error string `json:"error"`
}

// SendOptions configures the PrintSend operation
type SendOptions struct {
	Session    string   // Target session name
	Message    string   // Message to send
	All        bool     // Send to all panes (including user)
	Panes      []string // Specific pane indices (e.g., "0", "1", "2")
	AgentTypes []string // Filter by agent types (e.g., "claude", "codex")
	Exclude    []string // Panes to exclude
	DelayMs    int      // Delay between sends in milliseconds
}

// PrintSend sends a message to multiple panes atomically and returns structured results
func PrintSend(opts SendOptions) error {
	if !tmux.SessionExists(opts.Session) {
		return encodeJSON(SendOutput{
			Session:        opts.Session,
			SentAt:         time.Now().UTC(),
			Targets:        []string{},
			Successful:     []string{},
			Failed:         []SendError{{Pane: "session", Error: fmt.Sprintf("session '%s' not found", opts.Session)}},
			MessagePreview: truncateMessage(opts.Message),
		})
	}

	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		return encodeJSON(SendOutput{
			Session:        opts.Session,
			SentAt:         time.Now().UTC(),
			Targets:        []string{},
			Successful:     []string{},
			Failed:         []SendError{{Pane: "panes", Error: fmt.Sprintf("failed to get panes: %v", err)}},
			MessagePreview: truncateMessage(opts.Message),
		})
	}

	output := SendOutput{
		Session:        opts.Session,
		SentAt:         time.Now().UTC(),
		Targets:        []string{},
		Successful:     []string{},
		Failed:         []SendError{},
		MessagePreview: truncateMessage(opts.Message),
	}

	// Build exclusion map
	excludeMap := make(map[string]bool)
	for _, e := range opts.Exclude {
		excludeMap[e] = true
	}

	// Build pane filter map (if specific panes requested)
	paneFilterMap := make(map[string]bool)
	for _, p := range opts.Panes {
		paneFilterMap[p] = true
	}
	hasPaneFilter := len(paneFilterMap) > 0

	// Build agent type filter map
	typeFilterMap := make(map[string]bool)
	for _, t := range opts.AgentTypes {
		typeFilterMap[strings.ToLower(t)] = true
	}
	hasTypeFilter := len(typeFilterMap) > 0

	// Determine which panes to target
	var targetPanes []tmux.Pane
	for _, pane := range panes {
		paneKey := fmt.Sprintf("%d", pane.Index)

		// Check exclusions
		if excludeMap[paneKey] || excludeMap[pane.ID] {
			continue
		}

		// Check specific pane filter
		if hasPaneFilter && !paneFilterMap[paneKey] && !paneFilterMap[pane.ID] {
			continue
		}

		// Check agent type filter
		if hasTypeFilter {
			agentType := detectAgentType(pane.Title)
			if !typeFilterMap[agentType] {
				continue
			}
		}

		// If not --all and no filters, skip user panes by default
		if !opts.All && !hasPaneFilter && !hasTypeFilter {
			agentType := detectAgentType(pane.Title)
			// Skip user panes (first pane or explicitly marked as user)
			if pane.Index == 0 && agentType == "unknown" {
				continue
			}
			if agentType == "user" {
				continue
			}
		}

		targetPanes = append(targetPanes, pane)
		output.Targets = append(output.Targets, paneKey)
	}

	// Send to all targets
	for i, pane := range targetPanes {
		paneKey := fmt.Sprintf("%d", pane.Index)

		// Apply delay between sends (except for first)
		if i > 0 && opts.DelayMs > 0 {
			time.Sleep(time.Duration(opts.DelayMs) * time.Millisecond)
		}

		err := tmux.SendKeys(pane.ID, opts.Message, true)
		if err != nil {
			output.Failed = append(output.Failed, SendError{
				Pane:  paneKey,
				Error: err.Error(),
			})
		} else {
			output.Successful = append(output.Successful, paneKey)
		}
	}

	return encodeJSON(output)
}

// truncateMessage truncates a message to 50 chars with ellipsis
func truncateMessage(msg string) string {
	if len(msg) > 50 {
		return msg[:47] + "..."
	}
	return msg
}

// SnapshotDeltaOutput provides changes since a given timestamp.
type SnapshotDeltaOutput struct {
	Timestamp string   `json:"ts"`
	Since     string   `json:"since"`
	Changes   []Change `json:"changes"`
}

// Change represents a state change event.
type Change struct {
	Type    string                 `json:"type"`
	Session string                 `json:"session,omitempty"`
	Pane    string                 `json:"pane,omitempty"`
	Data    map[string]interface{} `json:"data,omitempty"`
}

// PrintSnapshotDelta outputs changes since the given timestamp.
// Uses the internal state tracker ring buffer to return delta changes.
func PrintSnapshotDelta(since time.Time) error {
	output := SnapshotDeltaOutput{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Since:     since.Format(time.RFC3339),
		Changes:   []Change{},
	}

	// Query the state tracker for changes since the given timestamp
	trackerChanges := stateTracker.Since(since)

	// Convert tracker.StateChange to robot.Change
	for _, tc := range trackerChanges {
		change := Change{
			Type:    string(tc.Type),
			Session: tc.Session,
			Pane:    tc.Pane,
			Data:    tc.Details,
		}
		output.Changes = append(output.Changes, change)
	}

	return encodeJSON(output)
}

// RecordStateChange records a state change to the global tracker.
// This should be called by other parts of the application when state changes occur.
func RecordStateChange(changeType tracker.ChangeType, session, pane string, details map[string]interface{}) {
	stateTracker.Record(tracker.StateChange{
		Timestamp: time.Now(),
		Type:      changeType,
		Session:   session,
		Pane:      pane,
		Details:   details,
	})
}

// GetStateTracker returns the global state tracker for direct access.
func GetStateTracker() *tracker.StateTracker {
	return stateTracker
}

// GraphOutput provides project graph analysis from bv
type GraphOutput struct {
	GeneratedAt time.Time                  `json:"generated_at"`
	Available   bool                       `json:"available"`
	Error       string                     `json:"error,omitempty"`
	Insights    *bv.InsightsResponse       `json:"insights,omitempty"`
	Priority    *bv.PriorityResponse       `json:"priority,omitempty"`
	Health      *bv.HealthSummary          `json:"health,omitempty"`
}

// PrintGraph outputs bv graph insights for AI consumption
func PrintGraph() error {
	output := GraphOutput{
		GeneratedAt: time.Now().UTC(),
		Available:   bv.IsInstalled(),
	}

	if !bv.IsInstalled() {
		output.Error = "bv (beads_viewer) is not installed"
		return encodeJSON(output)
	}

	// Get insights (bottlenecks, keystones, etc.)
	insights, err := bv.GetInsights()
	if err != nil {
		output.Error = fmt.Sprintf("failed to get insights: %v", err)
	} else {
		output.Insights = insights
	}

	// Get priority recommendations
	priority, err := bv.GetPriority()
	if err != nil {
		if output.Error == "" {
			output.Error = fmt.Sprintf("failed to get priority: %v", err)
		}
	} else {
		output.Priority = priority
	}

	// Get health summary
	health, err := bv.GetHealthSummary()
	if err != nil {
		if output.Error == "" {
			output.Error = fmt.Sprintf("failed to get health: %v", err)
		}
	} else {
		output.Health = health
	}

	return encodeJSON(output)
}
