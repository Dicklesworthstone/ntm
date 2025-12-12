// Package robot provides machine-readable output for AI agents and automation.
// Use --robot-* flags to get JSON output suitable for piping to other tools.
package robot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/cass"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/recipe"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tracker"
)

// ... existing code ...

// CASSStatusOutput represents the output for --robot-cass-status
type CASSStatusOutput struct {
	CASSAvailable bool           `json:"cass_available"`
	Healthy       bool           `json:"healthy"`
	Index         CASSIndexStats `json:"index"`
}

// CASSIndexStats holds index statistics
type CASSIndexStats struct {
	Exists        bool  `json:"exists"`
	Fresh         bool  `json:"fresh"`
	LastIndexedAt int64 `json:"last_indexed_at"`
	Conversations int64 `json:"conversations"`
	Messages      int64 `json:"messages"`
}

// PrintCASSStatus outputs CASS health and stats as JSON
func PrintCASSStatus() error {
	client := cass.NewClient()
	status, err := client.Status(context.Background())

	output := CASSStatusOutput{
		CASSAvailable: client.IsInstalled(),
		Healthy:       false,
		Index:         CASSIndexStats{},
	}

	if err == nil {
		output.Healthy = status.Healthy
		output.Index.Exists = true
		output.Index.Fresh = status.Index.Healthy
		output.Index.LastIndexedAt = status.LastIndexedAt.UnixMilli()
		output.Index.Conversations = status.Conversations
		output.Index.Messages = status.Messages
	}

	return encodeJSON(output)
}

// CASSSearchOutput represents the output for --robot-cass-search
type CASSSearchOutput struct {
	Query        string          `json:"query"`
	Count        int             `json:"count"`
	TotalMatches int             `json:"total_matches"`
	Hits         []CASSSearchHit `json:"hits"`
}

// CASSSearchHit represents a single hit in robot search output
type CASSSearchHit struct {
	SourcePath string  `json:"source_path"`
	Agent      string  `json:"agent"`
	Title      string  `json:"title"`
	Score      float64 `json:"score"`
	Snippet    string  `json:"snippet"`
	CreatedAt  int64   `json:"created_at"`
}

// PrintCASSSearch outputs search results as JSON
func PrintCASSSearch(query, agent, workspace, since string, limit int) error {
	client := cass.NewClient()
	resp, err := client.Search(context.Background(), cass.SearchOptions{
		Query:     query,
		Agent:     agent,
		Workspace: workspace,
		Since:     since,
		Limit:     limit,
	})

	if err != nil {
		return fmt.Errorf("search failed: %w", err)
	}

	output := CASSSearchOutput{
		Query:        resp.Query,
		Count:        resp.Count,
		TotalMatches: resp.TotalMatches,
		Hits:         make([]CASSSearchHit, len(resp.Hits)),
	}

	for i, hit := range resp.Hits {
		createdAt := int64(0)
		if hit.CreatedAt != nil {
			createdAt = *hit.CreatedAt * 1000 // Convert to ms
		}
		output.Hits[i] = CASSSearchHit{
			SourcePath: hit.SourcePath,
			Agent:      hit.Agent,
			Title:      hit.Title,
			Score:      hit.Score,
			Snippet:    hit.Snippet,
			CreatedAt:  createdAt,
		}
	}

	return encodeJSON(output)
}

// CASSInsightsOutput represents the output for --robot-cass-insights
type CASSInsightsOutput struct {
	Period string                   `json:"period"`
	Agents map[string]interface{}   `json:"agents"`
	Topics []map[string]interface{} `json:"topics"`
	Errors []map[string]interface{} `json:"errors"`
}

// PrintCASSInsights outputs aggregated insights as JSON
func PrintCASSInsights() error {
	client := cass.NewClient()
	// Get aggregations for the last 7 days by default
	resp, err := client.Search(context.Background(), cass.SearchOptions{
		Query: "*",
		Since: "7d",
		Limit: 0,
	})

	if err != nil {
		return fmt.Errorf("insights failed: %w", err)
	}

	output := CASSInsightsOutput{
		Period: "7d",
		Agents: map[string]interface{}{},
		Topics: []map[string]interface{}{},
		Errors: []map[string]interface{}{},
	}

	if resp.Aggregations != nil {
		// Convert agent map to buckets list
		var agentBuckets []map[string]interface{}
		for k, v := range resp.Aggregations.Agents {
			agentBuckets = append(agentBuckets, map[string]interface{}{
				"key":   k,
				"count": v,
			})
		}
		output.Agents["buckets"] = agentBuckets

		// Convert tags/topics
		for k, v := range resp.Aggregations.Tags {
			output.Topics = append(output.Topics, map[string]interface{}{
				"term":  k,
				"count": v,
			})
		}
	}

	return encodeJSON(output)
}

// CASSContextOutput represents output for --robot-cass-context
type CASSContextOutput struct {
	Query            string               `json:"query"`
	RelevantSessions []CASSContextSession `json:"relevant_sessions"`
	SuggestedContext string               `json:"suggested_context"`
}

// CASSContextSession represents a session in context output
type CASSContextSession struct {
	Summary   string   `json:"summary"`
	KeyPoints []string `json:"key_points"`
	Source    string   `json:"source"`
	Agent     string   `json:"agent"`
	When      string   `json:"when"`
}

// PrintCASSContext outputs relevant past context for spawning
func PrintCASSContext(query string) error {
	client := cass.NewClient()
	// Search for relevant sessions
	resp, err := client.Search(context.Background(), cass.SearchOptions{
		Query: query,
		Limit: 3,
	})

	if err != nil {
		return fmt.Errorf("context search failed: %w", err)
	}

	output := CASSContextOutput{
		Query:            query,
		RelevantSessions: []CASSContextSession{},
	}

	var suggestions []string

	for _, hit := range resp.Hits {
		when := "unknown"
		if hit.CreatedAt != nil {
			ts := time.Unix(*hit.CreatedAt, 0)
			when = ts.Format("2006-01-02")
		}

		session := CASSContextSession{
			Summary: hit.Title, // Use title as summary for now
			Source:  hit.SourcePath,
			Agent:   hit.Agent,
			When:    when,
		}
		// Extract potential key points from snippet?
		// For now just empty or placeholder
		session.KeyPoints = []string{}

		output.RelevantSessions = append(output.RelevantSessions, session)
		suggestions = append(suggestions, fmt.Sprintf("session '%s' (%s)", hit.Title, hit.Agent))
	}

	if len(suggestions) > 0 {
		output.SuggestedContext = fmt.Sprintf("Consider reviewing: %s", strings.Join(suggestions, ", "))
	}

	return encodeJSON(output)
}

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
	Type     string `json:"type"`              // claude, codex, gemini
	Variant  string `json:"variant,omitempty"` // Model alias or persona name
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
	GeneratedAt  time.Time          `json:"generated_at"`
	System       SystemInfo         `json:"system"`
	Sessions     []SessionInfo      `json:"sessions"`
	Summary      StatusSummary      `json:"summary"`
	Beads        *bv.BeadsSummary   `json:"beads,omitempty"`
	GraphMetrics *GraphMetrics      `json:"graph_metrics,omitempty"`
	AgentMail    *AgentMailSummary  `json:"agent_mail,omitempty"`
	FileChanges  []FileChangeInfo   `json:"file_changes,omitempty"`
	Conflicts    []tracker.Conflict `json:"conflicts,omitempty"`
}

// AgentMailSummary provides a lightweight Agent Mail state for --robot-status.
type AgentMailSummary struct {
	Available          bool   `json:"available"`
	ServerURL          string `json:"server_url,omitempty"`
	SessionsRegistered int    `json:"sessions_registered,omitempty"`
	TotalUnread        int    `json:"total_unread,omitempty"`
	UrgentMessages     int    `json:"urgent_messages,omitempty"`
	TotalLocks         int    `json:"total_locks,omitempty"`
	Error              string `json:"error,omitempty"`
}

// GraphMetrics provides bv graph analysis metrics for status output
type GraphMetrics struct {
	TopBottlenecks []BottleneckInfo `json:"top_bottlenecks,omitempty"`
	Keystones      int              `json:"keystones_count"`
	HealthStatus   string           `json:"health_status"` // "ok", "warning", "critical"
	DriftMessage   string           `json:"drift_message,omitempty"`
}

// BottleneckInfo represents a bottleneck issue with its score
type BottleneckInfo struct {
	ID    string  `json:"id"`
	Title string  `json:"title,omitempty"`
	Score float64 `json:"score"`
}

// FileChangeInfo is a sanitized view of recorded file changes.
type FileChangeInfo struct {
	Session string    `json:"session"`
	Path    string    `json:"path"`
	Type    string    `json:"type"`
	Agents  []string  `json:"agents,omitempty"`
	At      time.Time `json:"at"`
}

const (
	fileChangeLookback = 30 * time.Minute
	fileChangeLimit    = 50
	conflictLimit      = 20
)

// StatusSummary provides aggregate stats
type StatusSummary struct {
	TotalSessions int `json:"total_sessions"`
	TotalAgents   int `json:"total_agents"`
	AttachedCount int `json:"attached_count"`
	ClaudeCount   int `json:"claude_count"`
	CodexCount    int `json:"codex_count"`
	GeminiCount   int `json:"gemini_count"`
	CursorCount   int `json:"cursor_count"`
	WindsurfCount int `json:"windsurf_count"`
	AiderCount    int `json:"aider_count"`
}

// PlanOutput provides an execution plan for what can be done
type PlanOutput struct {
	GeneratedAt    time.Time    `json:"generated_at"`
	Recommendation string       `json:"recommendation"`
	Actions        []PlanAction `json:"actions"`
	BeadActions    []BeadAction `json:"bead_actions,omitempty"`
	Warnings       []string     `json:"warnings,omitempty"`
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
					Variant:  pane.Variant,
				}

				// Use authoritative type from tmux package if available
				ntmType := agentTypeString(pane.Type)
				if ntmType != "user" && ntmType != "unknown" {
					agent.Type = ntmType
				} else {
					// Fallback to loose detection for other agents (cursor, windsurf, etc.)
					agent.Type = detectAgentType(pane.Title)
				}
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
		output.Beads = bv.GetBeadsSummary(BeadLimit)
		output.GraphMetrics = getGraphMetrics()
	}

	// Enrich with Agent Mail summary (best-effort; degrade gracefully)
	if summary := getAgentMailSummary(); summary != nil {
		output.AgentMail = summary
	}

	// Include recent file changes (best-effort, bounded).
	appendFileChanges(&output)
	appendConflicts(&output)

	return encodeJSON(output)
}

func appendFileChanges(output *StatusOutput) {
	cutoff := time.Now().Add(-fileChangeLookback)
	changes := tracker.RecordedChangesSince(cutoff)
	if len(changes) == 0 {
		return
	}

	if len(changes) > fileChangeLimit {
		changes = changes[len(changes)-fileChangeLimit:]
	}

	wd, _ := os.Getwd()
	prefix := wd
	if prefix != "" && !strings.HasSuffix(prefix, string(os.PathSeparator)) {
		prefix += string(os.PathSeparator)
	}

	for _, change := range changes {
		path := change.Change.Path
		if prefix != "" && strings.HasPrefix(path, prefix) {
			path = strings.TrimPrefix(path, prefix)
		}

		output.FileChanges = append(output.FileChanges, FileChangeInfo{
			Session: change.Session,
			Path:    path,
			Type:    string(change.Change.Type),
			Agents:  change.Agents,
			At:      change.Timestamp,
		})
	}
}

func appendConflicts(output *StatusOutput) {
	conflicts := tracker.ConflictsSince(time.Now().Add(-fileChangeLookback), "")
	if len(conflicts) == 0 {
		return
	}
	if len(conflicts) > conflictLimit {
		conflicts = conflicts[:conflictLimit]
	}
	output.Conflicts = conflicts
}

// PrintMail outputs detailed Agent Mail state for AI orchestrators.
func PrintMail(projectKey string) error {
	if projectKey == "" {
		wd, err := os.Getwd()
		if err == nil {
			projectKey = wd
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	client := agentmail.NewClient(agentmail.WithProjectKey(projectKey))
	serverURL := client.BaseURL()

	output := struct {
		GeneratedAt time.Time                   `json:"generated_at"`
		ProjectKey  string                      `json:"project_key"`
		Available   bool                        `json:"available"`
		ServerURL   string                      `json:"server_url,omitempty"`
		Agents      []AgentMailAgent            `json:"agents,omitempty"`
		Locks       []agentmail.FileReservation `json:"locks,omitempty"`
		Error       string                      `json:"error,omitempty"`
	}{
		GeneratedAt: time.Now().UTC(),
		ProjectKey:  projectKey,
		Available:   false,
		ServerURL:   serverURL,
	}

	if !client.IsAvailable() {
		return encodeJSON(output)
	}
	output.Available = true

	// Ensure project exists
	if _, err := client.EnsureProject(ctx, projectKey); err != nil {
		output.Error = fmt.Sprintf("ensure_project: %v", err)
		return encodeJSON(output)
	}

	agents, err := client.ListProjectAgents(ctx, projectKey)
	if err != nil {
		output.Error = fmt.Sprintf("list_agents: %v", err)
		return encodeJSON(output)
	}

	// Gather per-agent mail counts
	for _, a := range agents {
		unread := countInbox(ctx, client, projectKey, a.Name, false)
		urgent := countInbox(ctx, client, projectKey, a.Name, true)
		output.Agents = append(output.Agents, AgentMailAgent{
			Name:         a.Name,
			Program:      a.Program,
			Model:        a.Model,
			UnreadCount:  unread,
			UrgentCount:  urgent,
			LastActiveTs: a.LastActiveTS,
		})
	}

	locks, err := client.ListReservations(ctx, projectKey, "", true)
	if err == nil {
		output.Locks = locks
	}

	return encodeJSON(output)
}

// AgentMailAgent is a per-agent view for --robot-mail.
type AgentMailAgent struct {
	Name         string    `json:"name"`
	Program      string    `json:"program,omitempty"`
	Model        string    `json:"model,omitempty"`
	UnreadCount  int       `json:"unread_count,omitempty"`
	UrgentCount  int       `json:"urgent_count,omitempty"`
	LastActiveTs time.Time `json:"last_active_ts,omitempty"`
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
		case bv.DriftNoBaseline:
			metrics.HealthStatus = "unknown"
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
	Session    string                `json:"session"`
	CapturedAt time.Time             `json:"captured_at"`
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

// buildSnapshotAgentMail assembles Agent Mail state for robot snapshot.
// Best-effort: failures do not fail snapshot generation.
func buildSnapshotAgentMail() *SnapshotAgentMail {
	cwd, err := os.Getwd()
	if err != nil {
		return &SnapshotAgentMail{Available: false, Reason: "unable to determine working directory"}
	}

	client := agentmail.NewClient(agentmail.WithProjectKey(cwd))

	// Quick availability check
	if !client.IsAvailable() {
		return &SnapshotAgentMail{
			Available: false,
			Reason:    fmt.Sprintf("agent mail server not available at %s", client.BaseURL()),
			Project:   cwd,
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Ensure project exists; if this fails, degrade gracefully.
	if _, err := client.EnsureProject(ctx, cwd); err != nil {
		return &SnapshotAgentMail{
			Available: true,
			Reason:    fmt.Sprintf("ensure_project failed: %v", err),
			Project:   cwd,
		}
	}

	agents, err := client.ListProjectAgents(ctx, cwd)
	if err != nil {
		return &SnapshotAgentMail{
			Available: true,
			Reason:    fmt.Sprintf("list_agents failed: %v", err),
			Project:   cwd,
		}
	}

	summary := &SnapshotAgentMail{
		Available: true,
		Project:   cwd,
		Agents:    make(map[string]SnapshotAgentMailStats),
	}

	// Best-effort: map Agent Mail identities to tmux panes by exact title match.
	agentNames := make(map[string]struct{}, len(agents))
	for _, a := range agents {
		if a.Name == "HumanOverseer" {
			continue
		}
		agentNames[a.Name] = struct{}{}
	}
	paneByAgent := make(map[string]string)
	if tmux.IsInstalled() && len(agentNames) > 0 {
		if sessions, err := tmux.ListSessions(); err == nil {
			for _, sess := range sessions {
				panes, err := tmux.GetPanes(sess.Name)
				if err != nil {
					continue
				}
				for _, pane := range panes {
					if _, ok := agentNames[pane.Title]; ok {
						// Mirror the snapshot pane format: windowIndex.paneIndex
						paneByAgent[pane.Title] = fmt.Sprintf("%d.%d", 0, pane.Index)
					}
				}
			}
		}
	}

	// Fetch limited inbox slices to keep the call lightweight.
	threadSet := make(map[string]struct{})
	for _, agent := range agents {
		if agent.Name == "HumanOverseer" {
			continue
		}

		inbox, err := client.FetchInbox(ctx, agentmail.FetchInboxOptions{
			ProjectKey:    cwd,
			AgentName:     agent.Name,
			Limit:         25,
			IncludeBodies: false,
		})
		if err != nil {
			continue
		}
		unread := len(inbox)
		pendingAck := 0
		for _, msg := range inbox {
			if msg.AckRequired {
				pendingAck++
			}
			threadKey := ""
			if msg.ThreadID != nil && *msg.ThreadID != "" {
				threadKey = *msg.ThreadID
			} else {
				// Messages without an explicit thread_id can be treated as their own thread.
				threadKey = fmt.Sprintf("%d", msg.ID)
			}
			threadSet[threadKey] = struct{}{}
		}
		summary.TotalUnread += unread
		stats := SnapshotAgentMailStats{
			Unread:     unread,
			PendingAck: pendingAck,
		}
		if paneRef, ok := paneByAgent[agent.Name]; ok {
			stats.Pane = paneRef
		}
		summary.Agents[agent.Name] = stats
	}

	if len(threadSet) > 0 {
		summary.ThreadsKnown = len(threadSet)
	}

	return summary
}

// SnapshotOutput provides complete system state for AI orchestration
type SnapshotOutput struct {
	Timestamp      string             `json:"ts"`
	Sessions       []SnapshotSession  `json:"sessions"`
	BeadsSummary   *bv.BeadsSummary   `json:"beads_summary,omitempty"`
	AgentMail      *SnapshotAgentMail `json:"agent_mail,omitempty"`
	MailUnread     int                `json:"mail_unread,omitempty"`
	Alerts         []string           `json:"alerts"`                    // Legacy: simple string alerts
	AlertsDetailed []AlertInfo        `json:"alerts_detailed,omitempty"` // Rich alert objects
	AlertSummary   *AlertSummaryInfo  `json:"alert_summary,omitempty"`
}

// AlertInfo provides detailed alert information for robot output
type AlertInfo struct {
	ID         string                 `json:"id"`
	Type       string                 `json:"type"`
	Severity   string                 `json:"severity"`
	Message    string                 `json:"message"`
	Session    string                 `json:"session,omitempty"`
	Pane       string                 `json:"pane,omitempty"`
	BeadID     string                 `json:"bead_id,omitempty"`
	Context    map[string]interface{} `json:"context,omitempty"`
	CreatedAt  string                 `json:"created_at"`
	DurationMs int64                  `json:"duration_ms"`
	Count      int                    `json:"count"`
}

// AlertSummaryInfo provides aggregate alert statistics
type AlertSummaryInfo struct {
	TotalActive int            `json:"total_active"`
	BySeverity  map[string]int `json:"by_severity"`
	ByType      map[string]int `json:"by_type"`
}

// SnapshotSession represents a session in the snapshot
type SnapshotSession struct {
	Name     string          `json:"name"`
	Attached bool            `json:"attached"`
	Agents   []SnapshotAgent `json:"agents"`
}

// SnapshotAgent represents an agent in the snapshot
type SnapshotAgent struct {
	Pane             string  `json:"pane"`
	Type             string  `json:"type"`
	Variant          string  `json:"variant,omitempty"` // Model alias or persona name
	TypeConfidence   float64 `json:"type_confidence"`
	TypeMethod       string  `json:"type_method"`
	State            string  `json:"state"`
	LastOutputAgeSec int     `json:"last_output_age_sec"`
	OutputTailLines  int     `json:"output_tail_lines"`
	CurrentBead      *string `json:"current_bead"`
	PendingMail      int     `json:"pending_mail"`
}

// SnapshotAgentMail represents Agent Mail availability and inbox state.
type SnapshotAgentMail struct {
	Available    bool                              `json:"available"`
	Reason       string                            `json:"reason,omitempty"`
	Project      string                            `json:"project,omitempty"`
	TotalUnread  int                               `json:"total_unread,omitempty"`
	Agents       map[string]SnapshotAgentMailStats `json:"agents,omitempty"`
	ThreadsKnown int                               `json:"threads_active,omitempty"`
}

// SnapshotAgentMailStats holds per-agent inbox counts.
type SnapshotAgentMailStats struct {
	Pane       string `json:"pane,omitempty"`
	Unread     int    `json:"unread"`
	PendingAck int    `json:"pending_ack"`
}

// BeadLimit controls how many ready/in-progress beads to include in snapshot
var BeadLimit = 5

// PrintSnapshot outputs complete system state for AI orchestration
func PrintSnapshot(cfg *config.Config) error {
	if cfg == nil {
		cfg = config.Default()
	}
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

	// Agent Mail summary (best-effort, graceful degradation). Populate early so we can attach
	// per-pane PendingMail hints during snapshot construction.
	output.AgentMail = buildSnapshotAgentMail()
	if output.AgentMail != nil && output.AgentMail.Available {
		output.MailUnread = output.AgentMail.TotalUnread
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
				Variant:          pane.Variant,
				TypeConfidence:   detection.Confidence,
				TypeMethod:       string(detection.Method),
				State:            "unknown",
				LastOutputAgeSec: -1, // Unknown without pane_last_activity
				OutputTailLines:  0,
				CurrentBead:      nil,
				PendingMail:      0,
			}

			// Best-effort mapping: if the pane title matches an Agent Mail identity, attach its
			// unread count as a PendingMail hint.
			if output.AgentMail != nil && output.AgentMail.Agents != nil {
				if stats, ok := output.AgentMail.Agents[pane.Title]; ok {
					agent.PendingMail = stats.Unread
				}
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
	beads := bv.GetBeadsSummary(BeadLimit)
	if beads != nil {
		output.BeadsSummary = beads
	}

	// Add alerts for detected issues (legacy string format)
	for _, sess := range output.Sessions {
		for _, agent := range sess.Agents {
			if agent.State == "error" {
				output.Alerts = append(output.Alerts, fmt.Sprintf("agent %s in %s has error state", agent.Pane, sess.Name))
			}
		}
	}

	// Generate and add detailed alerts using the alerts package
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

	if len(activeAlerts) > 0 {
		output.AlertsDetailed = make([]AlertInfo, len(activeAlerts))
		for i, a := range activeAlerts {
			output.AlertsDetailed[i] = AlertInfo{
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
			}
		}

		// Add to legacy alerts too for backwards compatibility
		for _, a := range activeAlerts {
			msg := a.Message
			if a.Session != "" {
				msg = a.Session + ": " + msg
			}
			output.Alerts = append(output.Alerts, msg)
		}

		// Add summary
		tracker := alerts.GetGlobalTracker()
		summary := tracker.Summary()
		output.AlertSummary = &AlertSummaryInfo{
			TotalActive: summary.TotalActive,
			BySeverity:  summary.BySeverity,
			ByType:      summary.ByType,
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
	if strings.TrimSpace(opts.Session) == "" {
		return encodeJSON(SendOutput{
			Session:        opts.Session,
			SentAt:         time.Now().UTC(),
			Targets:        []string{},
			Successful:     []string{},
			Failed:         []SendError{{Pane: "session", Error: "session name is required"}},
			MessagePreview: truncateMessage(opts.Message),
		})
	}

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
			// Use authoritative type if available, otherwise fallback to loose detection
			agentType := agentTypeString(pane.Type)
			if agentType == "user" || agentType == "unknown" {
				agentType = detectAgentType(pane.Title)
			}

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
	GeneratedAt time.Time            `json:"generated_at"`
	Available   bool                 `json:"available"`
	Error       string               `json:"error,omitempty"`
	Insights    *bv.InsightsResponse `json:"insights,omitempty"`
	Priority    *bv.PriorityResponse `json:"priority,omitempty"`
	Health      *bv.HealthSummary    `json:"health,omitempty"`
	Correlation *GraphCorrelation    `json:"correlation,omitempty"`
}

// GraphCorrelation provides a best-effort cross-tool view of agents, beads, and mail threads.
type GraphCorrelation struct {
	GeneratedAt   time.Time                  `json:"generated_at"`
	Agents        []GraphAgentAssignment     `json:"agents,omitempty"`
	BeadGraph     map[string]GraphBeadNode   `json:"bead_graph,omitempty"`
	MailThreads   map[string]GraphMailThread `json:"mail_threads,omitempty"`
	OrphanBeads   []string                   `json:"orphan_beads,omitempty"`
	OrphanThreads []string                   `json:"orphan_threads,omitempty"`
	Errors        []string                   `json:"errors,omitempty"`
}

// GraphAgentAssignment captures bead/thread membership for an agent.
type GraphAgentAssignment struct {
	Agent        string   `json:"agent"`
	Program      string   `json:"program,omitempty"`
	Model        string   `json:"model,omitempty"`
	Beads        []string `json:"beads,omitempty"`
	Threads      []string `json:"threads,omitempty"`
	Pane         string   `json:"pane,omitempty"`
	Session      string   `json:"session,omitempty"`
	Detected     string   `json:"detected_type,omitempty"`
	DetectedFrom string   `json:"detected_from,omitempty"`
}

// GraphBeadNode summarizes bead status and relationships.
type GraphBeadNode struct {
	Status     string   `json:"status"`
	AssignedTo *string  `json:"assigned_to,omitempty"`
	BlockedBy  []string `json:"blocked_by,omitempty"`
	Blocking   []string `json:"blocking,omitempty"`
	Title      string   `json:"title,omitempty"`
}

// GraphMailThread summarizes a mail thread for correlation.
type GraphMailThread struct {
	ThreadID     string    `json:"thread_id"`
	Subject      string    `json:"subject"`
	Participants []string  `json:"participants,omitempty"`
	LastActivity time.Time `json:"last_activity"`
	UnreadHint   int       `json:"unread_hint,omitempty"`
}

// PrintGraph outputs bv graph insights for AI consumption
func PrintGraph() error {
	output := GraphOutput{
		GeneratedAt: time.Now().UTC(),
		Available:   bv.IsInstalled(),
	}

	if !bv.IsInstalled() {
		output.Error = "bv (beads_viewer) is not installed"
		// Even if bv is missing, still attempt correlation to provide partial data.
	} else {
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
	}

	// Build correlation graph (best-effort, independent of bv availability)
	output.Correlation = buildCorrelationGraph()

	return encodeJSON(output)
}

// buildCorrelationGraph assembles a best-effort correlation map across agents, beads, and mail.
func buildCorrelationGraph() *GraphCorrelation {
	now := time.Now().UTC()
	corr := &GraphCorrelation{
		GeneratedAt: now,
		BeadGraph:   make(map[string]GraphBeadNode),
		MailThreads: make(map[string]GraphMailThread),
	}

	wd, err := os.Getwd()
	if err != nil {
		corr.Errors = append(corr.Errors, fmt.Sprintf("working directory unavailable: %v", err))
		return corr
	}

	// Collect Agent Mail agents (if available)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	agentMailClient := agentmail.NewClient(agentmail.WithProjectKey(wd))
	var agents []agentmail.Agent
	if agentMailClient.IsAvailable() {
		if _, err := agentMailClient.EnsureProject(ctx, wd); err != nil {
			corr.Errors = append(corr.Errors, fmt.Sprintf("agent mail ensure_project: %v", err))
		} else if list, err := agentMailClient.ListProjectAgents(ctx, wd); err != nil {
			corr.Errors = append(corr.Errors, fmt.Sprintf("agent mail list_agents: %v", err))
		} else {
			agents = list
		}
	} else {
		corr.Errors = append(corr.Errors, "agent mail not available")
	}

	agentIndex := make(map[string]int)
	for _, a := range agents {
		corr.Agents = append(corr.Agents, GraphAgentAssignment{
			Agent:   a.Name,
			Program: a.Program,
			Model:   a.Model,
		})
		agentIndex[a.Name] = len(corr.Agents) - 1
	}

	// Add bead assignments from bv summary (if present)
	if beads := bv.GetBeadsSummary(BeadLimit); beads != nil && beads.Available {
		for _, inProg := range beads.InProgressList {
			node := GraphBeadNode{
				Status: "in_progress",
				Title:  inProg.Title,
			}
			if inProg.Assignee != "" {
				assign := inProg.Assignee
				node.AssignedTo = &assign
				if idx, ok := agentIndex[assign]; ok {
					corr.Agents[idx].Beads = append(corr.Agents[idx].Beads, inProg.ID)
				} else {
					corr.OrphanBeads = append(corr.OrphanBeads, inProg.ID)
				}
			} else {
				corr.OrphanBeads = append(corr.OrphanBeads, inProg.ID)
			}
			corr.BeadGraph[inProg.ID] = node
		}

		for _, ready := range beads.ReadyPreview {
			status := "ready"
			node := GraphBeadNode{
				Status: status,
				Title:  ready.Title,
			}
			corr.BeadGraph[ready.ID] = node
		}
	} else if beads != nil && !beads.Available && beads.Reason != "" {
		corr.Errors = append(corr.Errors, fmt.Sprintf("beads unavailable: %s", beads.Reason))
	}

	// Attempt to gather mail threads (limited for performance)
	if len(agents) > 0 && agentMailClient.IsAvailable() {
		msgs, err := agentMailClient.SearchMessages(ctx, agentmail.SearchOptions{
			ProjectKey: wd,
			Query:      "*",
			Limit:      25,
		})
		if err != nil {
			corr.Errors = append(corr.Errors, fmt.Sprintf("agent mail search_messages: %v", err))
		} else {
			threadOrder := make([]string, 0)
			for _, msg := range msgs {
				if msg.ThreadID == nil {
					continue
				}
				tid := *msg.ThreadID
				if _, ok := corr.MailThreads[tid]; !ok {
					threadOrder = append(threadOrder, tid)
					corr.MailThreads[tid] = GraphMailThread{
						ThreadID:     tid,
						Subject:      msg.Subject,
						LastActivity: msg.CreatedTS,
					}
				}
			}

			// Summarize a limited number of threads for participants
			maxSummaries := 10
			for i, tid := range threadOrder {
				if i >= maxSummaries {
					break
				}
				summary, err := agentMailClient.SummarizeThread(ctx, wd, tid, false)
				if err != nil {
					corr.Errors = append(corr.Errors, fmt.Sprintf("summarize_thread %s: %v", tid, err))
					continue
				}
				thread := corr.MailThreads[tid]
				thread.Participants = summary.Participants
				corr.MailThreads[tid] = thread

				// Map thread participation to agents
				for _, participant := range summary.Participants {
					if idx, ok := agentIndex[participant]; ok {
						corr.Agents[idx].Threads = appendUnique(corr.Agents[idx].Threads, tid)
					}
				}
			}

			// Identify orphan threads (participants not known)
			for tid, thread := range corr.MailThreads {
				if len(thread.Participants) == 0 {
					corr.OrphanThreads = append(corr.OrphanThreads, tid)
				} else {
					known := false
					for _, p := range thread.Participants {
						if _, ok := agentIndex[p]; ok {
							known = true
							break
						}
					}
					if !known {
						corr.OrphanThreads = append(corr.OrphanThreads, tid)
					}
				}
			}
		}
	}

	// Append basic tmux pane hints for agent assignments (best-effort)
	if sessions, err := tmux.ListSessions(); err == nil {
		for _, sess := range sessions {
			panes, err := tmux.GetPanes(sess.Name)
			if err != nil {
				continue
			}
			for _, pane := range panes {
				// Try to match by pane title == agent name (common pattern)
				if idx, ok := agentIndex[pane.Title]; ok {
					corr.Agents[idx].Pane = fmt.Sprintf("%d.%d", 0, pane.Index)
					corr.Agents[idx].Session = sess.Name
					corr.Agents[idx].Detected = string(pane.Type)
					corr.Agents[idx].DetectedFrom = "pane_title"
				}
			}
		}
	}

	return corr
}

// appendUnique adds a value if absent.
func appendUnique(list []string, value string) []string {
	for _, v := range list {
		if v == value {
			return list
		}
	}
	return append(list, value)
}

// AlertsOutput provides machine-readable alert information
type AlertsOutput struct {
	GeneratedAt time.Time        `json:"generated_at"`
	Enabled     bool             `json:"enabled"`
	Active      []AlertInfo      `json:"active"`
	Resolved    []AlertInfo      `json:"resolved,omitempty"`
	Summary     AlertSummaryInfo `json:"summary"`
}

// PrintAlertsDetailed outputs all alerts in JSON format
func PrintAlertsDetailed(includeResolved bool) error {
	alertCfg := alerts.DefaultConfig()
	tracker := alerts.GenerateAndTrack(alertCfg)

	active, resolved := tracker.GetAll()
	summary := tracker.Summary()

	output := AlertsOutput{
		GeneratedAt: time.Now().UTC(),
		Enabled:     alertCfg.Enabled,
		Active:      make([]AlertInfo, len(active)),
		Summary: AlertSummaryInfo{
			TotalActive: summary.TotalActive,
			BySeverity:  summary.BySeverity,
			ByType:      summary.ByType,
		},
	}

	for i, a := range active {
		output.Active[i] = AlertInfo{
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
		}
	}

	if includeResolved {
		output.Resolved = make([]AlertInfo, len(resolved))
		for i, a := range resolved {
			output.Resolved[i] = AlertInfo{
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
			}
		}
	}

	return encodeJSON(output)
}

// RecipeInfo represents a recipe in JSON output
type RecipeInfo struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Source      string            `json:"source"` // builtin, user, project
	TotalAgents int               `json:"total_agents"`
	Agents      []RecipeAgentInfo `json:"agents"`
}

// RecipeAgentInfo represents an agent specification in a recipe
type RecipeAgentInfo struct {
	Type    string `json:"type"` // cc, cod, gmi
	Count   int    `json:"count"`
	Model   string `json:"model,omitempty"`
	Persona string `json:"persona,omitempty"`
}

// RecipesOutput is the structured output for --robot-recipes
type RecipesOutput struct {
	GeneratedAt time.Time    `json:"generated_at"`
	Count       int          `json:"count"`
	Recipes     []RecipeInfo `json:"recipes"`
}

// PrintRecipes outputs available recipes as JSON for AI orchestrators
func PrintRecipes() error {
	loader := recipe.NewLoader()
	recipes, err := loader.LoadAll()
	if err != nil {
		// Return empty list on error
		return encodeJSON(RecipesOutput{
			GeneratedAt: time.Now().UTC(),
			Count:       0,
			Recipes:     []RecipeInfo{},
		})
	}

	output := RecipesOutput{
		GeneratedAt: time.Now().UTC(),
		Count:       len(recipes),
		Recipes:     make([]RecipeInfo, len(recipes)),
	}

	for i, r := range recipes {
		agents := make([]RecipeAgentInfo, len(r.Agents))
		for j, a := range r.Agents {
			agents[j] = RecipeAgentInfo{
				Type:    a.Type,
				Count:   a.Count,
				Model:   a.Model,
				Persona: a.Persona,
			}
		}

		output.Recipes[i] = RecipeInfo{
			Name:        r.Name,
			Description: r.Description,
			Source:      r.Source,
			TotalAgents: r.TotalAgents(),
			Agents:      agents,
		}
	}

	return encodeJSON(output)
}

// TerseState represents the ultra-compact state for token-constrained scenarios.
// Format: S:session|A:active/total|W:working|I:idle|E:errors|C:ctx%|B:Rn/In/Bn|M:mail|!:Nc,Nw
type TerseState struct {
	Session        string `json:"session"`
	ActiveAgents   int    `json:"active_agents"`
	TotalAgents    int    `json:"total_agents"`
	WorkingAgents  int    `json:"working_agents"` // Agents actively processing
	IdleAgents     int    `json:"idle_agents"`    // Agents waiting at prompt
	ErrorAgents    int    `json:"error_agents"`   // Agents in error state
	ContextPct     int    `json:"context_pct"`    // Average context usage %
	ReadyBeads     int    `json:"ready_beads"`    // Beads ready to work on
	BlockedBeads   int    `json:"blocked_beads"`  // Blocked beads
	InProgressBead int    `json:"in_progress_beads"`
	UnreadMail     int    `json:"unread_mail"`
	CriticalAlerts int    `json:"critical_alerts"`
	WarningAlerts  int    `json:"warning_alerts"`
}

// String returns the ultra-compact string representation.
// Format: S:session|A:5/8|W:3|I:2|E:0|C:78%|B:R3/I2/B1|M:4|!:1c,2w
func (t TerseState) String() string {
	// Build alerts string (only include if non-zero)
	alertStr := ""
	if t.CriticalAlerts > 0 || t.WarningAlerts > 0 {
		var parts []string
		if t.CriticalAlerts > 0 {
			parts = append(parts, fmt.Sprintf("%dc", t.CriticalAlerts))
		}
		if t.WarningAlerts > 0 {
			parts = append(parts, fmt.Sprintf("%dw", t.WarningAlerts))
		}
		alertStr = strings.Join(parts, ",")
	} else {
		alertStr = "0"
	}

	return fmt.Sprintf("S:%s|A:%d/%d|W:%d|I:%d|E:%d|C:%d%%|B:R%d/I%d/B%d|M:%d|!:%s",
		t.Session,
		t.ActiveAgents, t.TotalAgents,
		t.WorkingAgents, t.IdleAgents, t.ErrorAgents,
		t.ContextPct,
		t.ReadyBeads, t.InProgressBead, t.BlockedBeads,
		t.UnreadMail,
		alertStr)
}

// ParseTerse parses the ultra-compact terse string into a TerseState.
// Format: S:session|A:active/total|W:working|I:idle|E:errors|C:ctx%|B:Rn/In/Bn|M:mail|!:Nc,Nw
func ParseTerse(s string) (*TerseState, error) {
	state := &TerseState{}

	// Split by pipe
	parts := strings.Split(s, "|")
	for _, part := range parts {
		kv := strings.SplitN(part, ":", 2)
		if len(kv) != 2 {
			continue
		}
		key, val := kv[0], kv[1]

		switch key {
		case "S":
			state.Session = val
		case "A":
			// Parse "active/total" format
			agentParts := strings.Split(val, "/")
			if len(agentParts) == 2 {
				fmt.Sscanf(agentParts[0], "%d", &state.ActiveAgents)
				fmt.Sscanf(agentParts[1], "%d", &state.TotalAgents)
			}
		case "W":
			fmt.Sscanf(val, "%d", &state.WorkingAgents)
		case "I":
			fmt.Sscanf(val, "%d", &state.IdleAgents)
		case "E":
			fmt.Sscanf(val, "%d", &state.ErrorAgents)
		case "C":
			// Parse "78%" format
			fmt.Sscanf(strings.TrimSuffix(val, "%"), "%d", &state.ContextPct)
		case "B":
			// Parse "R3/I2/B1" format
			beadParts := strings.Split(val, "/")
			for _, bp := range beadParts {
				if len(bp) < 2 {
					continue
				}
				prefix := bp[0]
				var count int
				fmt.Sscanf(bp[1:], "%d", &count)
				switch prefix {
				case 'R':
					state.ReadyBeads = count
				case 'I':
					state.InProgressBead = count
				case 'B':
					state.BlockedBeads = count
				}
			}
		case "M":
			fmt.Sscanf(val, "%d", &state.UnreadMail)
		case "!":
			// Parse "1c,2w" or "0" format
			if val == "0" {
				state.CriticalAlerts = 0
				state.WarningAlerts = 0
			} else {
				alertParts := strings.Split(val, ",")
				for _, ap := range alertParts {
					if strings.HasSuffix(ap, "c") {
						fmt.Sscanf(strings.TrimSuffix(ap, "c"), "%d", &state.CriticalAlerts)
					} else if strings.HasSuffix(ap, "w") {
						fmt.Sscanf(strings.TrimSuffix(ap, "w"), "%d", &state.WarningAlerts)
					}
				}
			}
		}
	}

	return state, nil
}

// PrintTerse outputs ultra-compact single-line state for token-constrained scenarios.
// Output format: S:session|A:active/total|W:working|I:idle|E:errors|C:ctx%|B:Rn/In/Bn|M:mail|!:Nc,Nw
// Multiple sessions are separated by semicolons.
func PrintTerse(cfg *config.Config) error {
	var results []string

	// Get alert breakdown (critical vs warning)
	var criticalAlerts, warningAlerts int
	if cfg != nil {
		alertCfg := alerts.ToConfigAlerts(
			cfg.Alerts.Enabled,
			cfg.Alerts.AgentStuckMinutes,
			cfg.Alerts.DiskLowThresholdGB,
			cfg.Alerts.MailBacklogThreshold,
			cfg.Alerts.BeadStaleHours,
			cfg.Alerts.ResolvedPruneMinutes,
			cfg.ProjectsBase,
		)
		activeAlerts := alerts.GetActiveAlerts(alertCfg)
		for _, a := range activeAlerts {
			switch a.Severity {
			case alerts.SeverityCritical:
				criticalAlerts++
			case alerts.SeverityWarning:
				warningAlerts++
			}
		}
	}

	// Get beads summary (shared across sessions)
	var beadsSummary *bv.BeadsSummary
	if bv.IsInstalled() {
		beadsSummary = bv.GetBeadsSummary(0)
	}

	// Get mail count (best-effort)
	mailCount := getTerseMailCount()

	// Get all sessions
	sessions, err := tmux.ListSessions()
	if err != nil {
		// No sessions - output minimal state with just beads info
		state := TerseState{
			Session:        "-",
			CriticalAlerts: criticalAlerts,
			WarningAlerts:  warningAlerts,
			UnreadMail:     mailCount,
		}
		if beadsSummary != nil {
			state.ReadyBeads = beadsSummary.Ready
			state.BlockedBeads = beadsSummary.Blocked
			state.InProgressBead = beadsSummary.InProgress
		}

		fmt.Println(state.String())
		return nil
	}

	for _, sess := range sessions {
		state := TerseState{
			Session:        sess.Name,
			CriticalAlerts: criticalAlerts,
			WarningAlerts:  warningAlerts,
			UnreadMail:     mailCount,
		}

		// Get panes for this session
		panes, err := tmux.GetPanes(sess.Name)
		if err == nil {
			state.TotalAgents = len(panes)
			// Count agents by state: working (active), idle, error
			for _, pane := range panes {
				agentType := agentTypeString(pane.Type)
				if agentType != "user" && agentType != "unknown" {
					// Capture output to detect state
					captured, captureErr := tmux.CapturePaneOutput(pane.ID, 20)
					if captureErr == nil {
						lines := splitLines(stripANSI(captured))
						paneState := detectState(lines, pane.Title)
						switch paneState {
						case "active":
							state.WorkingAgents++
							state.ActiveAgents++
						case "idle":
							state.IdleAgents++
							state.ActiveAgents++
						case "error":
							state.ErrorAgents++
							state.ActiveAgents++
						default:
							// Unknown state counts as active
							state.ActiveAgents++
						}
					} else {
						// Assume active/working if we can't capture
						state.WorkingAgents++
						state.ActiveAgents++
					}
				}
			}
		}

		// Add beads summary (same for all sessions in same project)
		if beadsSummary != nil {
			state.ReadyBeads = beadsSummary.Ready
			state.BlockedBeads = beadsSummary.Blocked
			state.InProgressBead = beadsSummary.InProgress
		}

		// Context percentage is not available at session level yet
		// Would require aggregating from individual agent outputs
		state.ContextPct = 0

		results = append(results, state.String())
	}

	// Output all sessions separated by semicolons
	fmt.Println(strings.Join(results, ";"))
	return nil
}

// getTerseMailCount returns unread mail count for terse output (best-effort).
func getTerseMailCount() int {
	projectKey, err := os.Getwd()
	if err != nil {
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client := agentmail.NewClient(agentmail.WithProjectKey(projectKey))
	if !client.IsAvailable() {
		return 0
	}

	// Ensure project exists
	if _, err := client.EnsureProject(ctx, projectKey); err != nil {
		return 0
	}

	agents, err := client.ListProjectAgents(ctx, projectKey)
	if err != nil {
		return 0
	}

	// Sum unread across all agents
	total := 0
	for _, a := range agents {
		total += countInbox(ctx, client, projectKey, a.Name, false)
	}

	return total
}

// getAgentMailSummary returns a best-effort Agent Mail summary for --robot-status.
func getAgentMailSummary() *AgentMailSummary {
	projectKey, err := os.Getwd()
	if err != nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	client := agentmail.NewClient(agentmail.WithProjectKey(projectKey))
	summary := &AgentMailSummary{
		Available: false,
		ServerURL: client.BaseURL(),
	}

	if !client.IsAvailable() {
		return summary
	}
	summary.Available = true

	// Ensure project exists
	if _, err := client.EnsureProject(ctx, projectKey); err != nil {
		summary.Error = fmt.Sprintf("ensure_project: %v", err)
		return summary
	}

	agents, err := client.ListProjectAgents(ctx, projectKey)
	if err != nil {
		summary.Error = fmt.Sprintf("list_agents: %v", err)
		return summary
	}
	summary.SessionsRegistered = len(agents)

	// Aggregate unread/urgent counts
	for _, a := range agents {
		summary.TotalUnread += countInbox(ctx, client, projectKey, a.Name, false)
		summary.UrgentMessages += countInbox(ctx, client, projectKey, a.Name, true)
	}

	// Locks (best-effort)
	if locks, err := client.ListReservations(ctx, projectKey, "", true); err == nil {
		summary.TotalLocks = len(locks)
	}

	return summary
}

// countInbox returns the count of inbox entries for an agent.
// If urgentOnly is true, only urgent messages are counted.
func countInbox(ctx context.Context, client *agentmail.Client, projectKey, agentName string, urgentOnly bool) int {
	limit := 50
	opts := agentmail.FetchInboxOptions{
		ProjectKey:    projectKey,
		AgentName:     agentName,
		UrgentOnly:    urgentOnly,
		Limit:         limit,
		IncludeBodies: false,
	}
	msgs, err := client.FetchInbox(ctx, opts)
	if err != nil {
		return 0
	}
	return len(msgs)
}
