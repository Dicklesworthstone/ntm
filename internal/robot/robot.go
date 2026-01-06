// Package robot provides machine-readable output for AI agents and automation.
// Use --robot-* flags to get JSON output suitable for piping to other tools.
package robot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/cass"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/recipe"
	"github.com/Dicklesworthstone/ntm/internal/status"
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
		output.Index.LastIndexedAt = status.LastIndexedAt.Time.UnixMilli()
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
			createdAt = hit.CreatedAt.Time.UnixMilli() // Convert to ms
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
			ts := hit.CreatedAt.Time
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
		output.Beads = bv.GetBeadsSummary(mustGetwd(), BeadLimit)
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
func PrintMail(sessionName, projectKey string) error {
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
		GeneratedAt      time.Time                   `json:"generated_at"`
		Session          string                      `json:"session,omitempty"`
		ProjectKey       string                      `json:"project_key"`
		Available        bool                        `json:"available"`
		ServerURL        string                      `json:"server_url,omitempty"`
		SessionAgent     *agentmail.SessionAgentInfo `json:"session_agent,omitempty"`
		Agents           []AgentMailAgent            `json:"agents,omitempty"`
		UnmappedAgents   []AgentMailAgent            `json:"unmapped_agents,omitempty"`
		Messages         AgentMailMessageCounts      `json:"messages,omitempty"`
		FileReservations []AgentMailReservation      `json:"file_reservations,omitempty"`
		Conflicts        []AgentMailConflict         `json:"conflicts,omitempty"`
		Error            string                      `json:"error,omitempty"`
	}{
		GeneratedAt: time.Now().UTC(),
		Session:     sessionName,
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

	// Session coordinator agent info (best-effort, when a session name is provided).
	if sessionName != "" {
		if info, err := agentmail.LoadSessionAgent(sessionName, projectKey); err == nil && info != nil {
			output.SessionAgent = info
		}
	}

	agents, err := client.ListProjectAgents(ctx, projectKey)
	if err != nil {
		output.Error = fmt.Sprintf("list_agents: %v", err)
		return encodeJSON(output)
	}

	agentByName := make(map[string]agentmail.Agent, len(agents))
	inboxByName := make(map[string]inboxTally, len(agents))

	// Gather per-agent mail counts (best-effort).
	for _, a := range agents {
		agentByName[a.Name] = a
		tally := getInboxTally(ctx, client, projectKey, a.Name, 50)
		inboxByName[a.Name] = tally

		output.Messages.Total += tally.Total
		output.Messages.Unread += tally.Total
		output.Messages.Urgent += tally.Urgent
		output.Messages.PendingAck += tally.PendingAck
	}

	// Best-effort pane mapping when a session is provided and tmux is available.
	assigned := make(map[string]bool)
	if sessionName != "" && tmux.IsInstalled() && tmux.SessionExists(sessionName) {
		if panes, err := tmux.GetPanes(sessionName); err == nil {
			paneInfos := parseNTMPanes(panes)
			agentsByType := groupAgentsByType(agents)
			for _, paneType := range []string{"cc", "cod", "gmi"} {
				mapping := assignAgentsToPanes(paneInfos[paneType], agentsByType[paneType])
				for _, pane := range paneInfos[paneType] {
					entry := AgentMailAgent{Pane: pane.Label}
					if agentName := mapping[pane.Label]; agentName != "" {
						assigned[agentName] = true
						a := agentByName[agentName]
						tally := inboxByName[agentName]
						entry.AgentName = agentName
						entry.Program = a.Program
						entry.Model = a.Model
						entry.UnreadCount = tally.Total
						entry.UrgentCount = tally.Urgent
						entry.LastActiveTs = a.LastActiveTS
					}
					output.Agents = append(output.Agents, entry)
				}
			}
		}
	}

	// If no panes were added (no session context), fall back to listing agents as-is.
	if len(output.Agents) == 0 {
		for _, a := range agents {
			tally := inboxByName[a.Name]
			output.Agents = append(output.Agents, AgentMailAgent{
				AgentName:    a.Name,
				Program:      a.Program,
				Model:        a.Model,
				UnreadCount:  tally.Total,
				UrgentCount:  tally.Urgent,
				LastActiveTs: a.LastActiveTS,
			})
		}
	} else {
		// Include any registered agents that we couldn't map to panes.
		for _, a := range agents {
			if a.Program == "ntm" || assigned[a.Name] {
				continue
			}
			tally := inboxByName[a.Name]
			output.UnmappedAgents = append(output.UnmappedAgents, AgentMailAgent{
				AgentName:    a.Name,
				Program:      a.Program,
				Model:        a.Model,
				UnreadCount:  tally.Total,
				UrgentCount:  tally.Urgent,
				LastActiveTs: a.LastActiveTS,
			})
		}
	}

	reservations, err := client.ListReservations(ctx, projectKey, "", true)
	if err == nil {
		output.FileReservations = summarizeReservations(reservations)
		output.Conflicts = detectReservationConflicts(reservations)
	}

	return encodeJSON(output)
}

// AgentMailAgent is a per-agent view for --robot-mail.
type AgentMailAgent struct {
	Pane         string    `json:"pane,omitempty"`
	AgentName    string    `json:"agent_name,omitempty"`
	Program      string    `json:"program,omitempty"`
	Model        string    `json:"model,omitempty"`
	UnreadCount  int       `json:"unread_count,omitempty"`
	UrgentCount  int       `json:"urgent_count,omitempty"`
	LastActiveTs time.Time `json:"last_active_ts,omitempty"`
}

type AgentMailMessageCounts struct {
	Total      int `json:"total"`
	Unread     int `json:"unread"`
	Urgent     int `json:"urgent"`
	PendingAck int `json:"pending_ack"`
}

type AgentMailReservation struct {
	ID               int    `json:"id"`
	Pattern          string `json:"pattern"`
	Agent            string `json:"agent"`
	Exclusive        bool   `json:"exclusive"`
	ExpiresInSeconds int    `json:"expires_in_seconds"`
	Reason           string `json:"reason,omitempty"`
}

type AgentMailConflict struct {
	Pattern string   `json:"pattern"`
	Holders []string `json:"holders"`
}

type inboxTally struct {
	Total      int
	Urgent     int
	PendingAck int
}

func getInboxTally(ctx context.Context, client *agentmail.Client, projectKey, agentName string, limit int) inboxTally {
	opts := agentmail.FetchInboxOptions{
		ProjectKey:    projectKey,
		AgentName:     agentName,
		UrgentOnly:    false,
		Limit:         limit,
		IncludeBodies: false,
	}
	msgs, err := client.FetchInbox(ctx, opts)
	if err != nil {
		return inboxTally{}
	}

	tally := inboxTally{Total: len(msgs)}
	for _, m := range msgs {
		if strings.EqualFold(m.Importance, "urgent") {
			tally.Urgent++
		}
		if m.AckRequired {
			tally.PendingAck++
		}
	}
	return tally
}

type ntmPaneInfo struct {
	Label     string
	Type      string
	Index     int
	TmuxIndex int
	Variant   string
}

var ntmPaneTitleRE = regexp.MustCompile(`^.+__(cc|cod|gmi)_(\d+)(?:_([A-Za-z0-9._/@:+-]+))?(?:\[[^\]]*\])?$`)

func parseNTMPanes(panes []tmux.Pane) map[string][]ntmPaneInfo {
	out := map[string][]ntmPaneInfo{
		"cc":  {},
		"cod": {},
		"gmi": {},
	}

	for _, p := range panes {
		matches := ntmPaneTitleRE.FindStringSubmatch(strings.TrimSpace(p.Title))
		if matches == nil {
			continue
		}
		idx, err := strconv.Atoi(matches[2])
		if err != nil {
			continue
		}

		typ := matches[1]
		variant := matches[3]
		out[typ] = append(out[typ], ntmPaneInfo{
			Label:     fmt.Sprintf("%s_%d", typ, idx),
			Type:      typ,
			Index:     idx,
			TmuxIndex: p.Index,
			Variant:   variant,
		})
	}

	for typ := range out {
		sort.SliceStable(out[typ], func(i, j int) bool { return out[typ][i].Index < out[typ][j].Index })
	}
	return out
}

func groupAgentsByType(agents []agentmail.Agent) map[string][]agentmail.Agent {
	out := map[string][]agentmail.Agent{
		"cc":  {},
		"cod": {},
		"gmi": {},
	}
	for _, a := range agents {
		if a.Program == "" || a.Program == "ntm" {
			continue
		}
		typ := agentTypeFromProgram(a.Program)
		if typ == "" {
			continue
		}
		out[typ] = append(out[typ], a)
	}

	for typ := range out {
		sort.SliceStable(out[typ], func(i, j int) bool { return out[typ][i].InceptionTS.Before(out[typ][j].InceptionTS) })
	}
	return out
}

func agentTypeFromProgram(program string) string {
	p := strings.ToLower(program)
	switch {
	case strings.Contains(p, "claude"):
		return "cc"
	case strings.Contains(p, "codex"):
		return "cod"
	case strings.Contains(p, "gemini"):
		return "gmi"
	default:
		return ""
	}
}

func normalizedProgramType(program string) string {
	p := strings.ToLower(program)
	switch {
	case strings.Contains(p, "claude"):
		return "claude"
	case strings.Contains(p, "codex"):
		return "codex"
	case strings.Contains(p, "gemini"):
		return "gemini"
	default:
		return "unknown"
	}
}

func assignAgentsToPanes(panes []ntmPaneInfo, agents []agentmail.Agent) map[string]string {
	assigned := make(map[string]bool)
	mapping := make(map[string]string)

	for _, pane := range panes {
		bestIdx := -1
		bestScore := -1

		for i, a := range agents {
			if assigned[a.Name] {
				continue
			}
			score := 0
			if pane.Variant != "" {
				v := strings.ToLower(pane.Variant)
				if strings.Contains(strings.ToLower(a.Model), v) {
					score = 2
				} else if strings.Contains(strings.ToLower(a.TaskDescription), v) {
					score = 1
				}
			}
			if bestIdx == -1 || score > bestScore {
				bestIdx = i
				bestScore = score
			}
		}

		if bestIdx == -1 {
			continue
		}

		chosen := agents[bestIdx]
		mapping[pane.Label] = chosen.Name
		assigned[chosen.Name] = true
	}

	return mapping
}

func summarizeReservations(reservations []agentmail.FileReservation) []AgentMailReservation {
	now := time.Now()
	out := make([]AgentMailReservation, 0, len(reservations))
	for _, r := range reservations {
		expiresIn := int(r.ExpiresTS.Sub(now).Seconds())
		if expiresIn < 0 {
			expiresIn = 0
		}
		out = append(out, AgentMailReservation{
			ID:               r.ID,
			Pattern:          r.PathPattern,
			Agent:            r.AgentName,
			Exclusive:        r.Exclusive,
			ExpiresInSeconds: expiresIn,
			Reason:           r.Reason,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Agent != out[j].Agent {
			return out[i].Agent < out[j].Agent
		}
		return out[i].Pattern < out[j].Pattern
	})
	return out
}

func detectReservationConflicts(reservations []agentmail.FileReservation) []AgentMailConflict {
	type patternState struct {
		agents    map[string]bool
		exclusive bool
	}
	byPattern := make(map[string]*patternState)
	for _, r := range reservations {
		state := byPattern[r.PathPattern]
		if state == nil {
			state = &patternState{agents: make(map[string]bool)}
			byPattern[r.PathPattern] = state
		}
		state.agents[r.AgentName] = true
		if r.Exclusive {
			state.exclusive = true
		}
	}

	var conflicts []AgentMailConflict
	for pattern, state := range byPattern {
		if !state.exclusive || len(state.agents) <= 1 {
			continue
		}
		var holders []string
		for name := range state.agents {
			holders = append(holders, name)
		}
		sort.Strings(holders)
		conflicts = append(conflicts, AgentMailConflict{Pattern: pattern, Holders: holders})
	}
	sort.SliceStable(conflicts, func(i, j int) bool { return conflicts[i].Pattern < conflicts[j].Pattern })
	return conflicts
}

// getGraphMetrics returns bv graph analysis metrics
func getGraphMetrics() *GraphMetrics {
	metrics := &GraphMetrics{
		HealthStatus: "unknown",
	}

	wd := mustGetwd()

	// Get drift status directly
	drift := bv.CheckDrift(wd)
	switch drift.Status {
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
	metrics.DriftMessage = drift.Message

	// Get insights once for bottlenecks and keystones
	insights, err := bv.GetInsights(wd)
	if err == nil && insights != nil {
		metrics.Keystones = len(insights.Keystones)

		// Top 3 bottlenecks
		limit := 3
		if len(insights.Bottlenecks) < limit {
			limit = len(insights.Bottlenecks)
		}
		for i := 0; i < limit; i++ {
			b := insights.Bottlenecks[i]
			metrics.TopBottlenecks = append(metrics.TopBottlenecks, BottleneckInfo{
				ID:    b.ID,
				Score: b.Value,
			})
		}
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
	recommendations, err := bv.GetNextActions("", limit)
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
	graphPositions, graphErr := bv.GetGraphPositionsBatch("", issueIDs)
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
	output, err := bv.RunBd("", "ready", "--json")
	if err != nil {
		return ready
	}

	// Parse JSON array of issues
	var issues []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(output), &issues); err != nil {
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
	output, err := bv.RunBd("", "show", issueID, "--json")
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
	if err := json.Unmarshal([]byte(output), &issues); err != nil {
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
	titleLower := toLower(title)

	// Check canonical forms
	switch {
	case contains(titleLower, "claude"):
		return "claude"
	case contains(titleLower, "codex"):
		return "codex"
	case contains(titleLower, "gemini"):
		return "gemini"
	case contains(titleLower, "cursor"):
		return "cursor"
	case contains(titleLower, "windsurf"):
		return "windsurf"
	case contains(titleLower, "aider"):
		return "aider"
	}

	// Check short forms in pane titles (e.g., "session__cc_1", "project__cod_2")
	// The pattern is: prefix__<short>_suffix or prefix__<short>__suffix
	// We use word boundary matching via "__<short>_" or "__<short>__"
	switch {
	case containsShortForm(titleLower, "cc"):
		return "claude"
	case containsShortForm(titleLower, "cod"):
		return "codex"
	case containsShortForm(titleLower, "gmi"):
		return "gemini"
	}

	return "unknown"
}

// containsShortForm checks if title contains the short form as a word boundary pattern
// It matches patterns like "__cc_" or "__cc__" to avoid false positives
func containsShortForm(title, short string) bool {
	// Check for "__<short>_" or "__<short>__"
	pattern1 := "__" + short + "_"
	pattern2 := "__" + short + "__"
	return containsLower(title, pattern1) || containsLower(title, pattern2)
}

// ResolveAgentType maps agent type aliases to canonical names.
// For example: "cc" -> "claude", "cod" -> "codex"
func ResolveAgentType(t string) string {
	// Trim whitespace
	trimmed := t
	for len(trimmed) > 0 && (trimmed[0] == ' ' || trimmed[0] == '\t') {
		trimmed = trimmed[1:]
	}
	for len(trimmed) > 0 && (trimmed[len(trimmed)-1] == ' ' || trimmed[len(trimmed)-1] == '\t') {
		trimmed = trimmed[:len(trimmed)-1]
	}

	lower := toLower(trimmed)
	switch lower {
	case "cc", "claude-code", "claude_code", "claude":
		return "claude"
	case "cod", "codex-cli", "codex_cli", "codex":
		return "codex"
	case "gmi", "gemini-cli", "gemini_cli", "gemini":
		return "gemini"
	case "cursor":
		return "cursor"
	case "windsurf":
		return "windsurf"
	case "aider":
		return "aider"
	case "user":
		return "user"
	default:
		return lower
	}
}

// detectModel attempts to detect the model from agent type and pane title.
func detectModel(agentType, title string) string {
	titleLower := toLower(title)
	// Check for specific model mentions in title
	switch {
	case contains(titleLower, "opus"):
		return "opus"
	case contains(titleLower, "sonnet"):
		return "sonnet"
	case contains(titleLower, "haiku"):
		return "haiku"
	case contains(titleLower, "gpt4") || contains(titleLower, "gpt-4"):
		return "gpt4"
	case contains(titleLower, "o1"):
		return "o1"
	case contains(titleLower, "o3"):
		return "o3"
	case contains(titleLower, "o4-mini"):
		return "o4-mini"
	case contains(titleLower, "flash"):
		return "flash"
	case contains(titleLower, "pro"):
		return "pro"
	case contains(titleLower, "gemini"):
		return "gemini"
	}
	// Default models by agent type
	switch agentType {
	case "claude":
		return "sonnet" // Default Claude model
	case "codex":
		return "gpt4" // Default Codex model
	case "gemini":
		return "gemini" // Default Gemini model
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
	RobotResponse                       // Embed standard response fields (success, timestamp, error)
	Session       string                `json:"session"`
	CapturedAt    time.Time             `json:"captured_at"`
	Panes         map[string]PaneOutput `json:"panes"`
	AgentHints    *TailAgentHints       `json:"_agent_hints,omitempty"`
}

// TailAgentHints provides agent guidance specific to tail output
type TailAgentHints struct {
	IdleAgents   []string `json:"idle_agents,omitempty"`   // Panes with idle agents ready for prompts
	ActiveAgents []string `json:"active_agents,omitempty"` // Panes with actively working agents
	Suggestions  []string `json:"suggestions,omitempty"`   // Actionable hints
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
		return RobotError(
			fmt.Errorf("session '%s' not found", session),
			ErrCodeSessionNotFound,
			"Use 'ntm list' to see available sessions",
		)
	}

	panes, err := tmux.GetPanes(session)
	if err != nil {
		return RobotError(
			fmt.Errorf("failed to get panes: %w", err),
			ErrCodeInternalError,
			"Check tmux is running and session is accessible",
		)
	}

	output := TailOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       session,
		CapturedAt:    time.Now().UTC(),
		Panes:         make(map[string]PaneOutput),
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
		cleanOutput := status.StripANSI(captured)
		outputLines := splitLines(cleanOutput)

		// Detect state from output
		agentType := detectAgentType(pane.Title)
		state := determineState(captured, agentType)

		// Check if truncated (we captured exactly the requested lines)
		truncated := len(outputLines) >= lines

		output.Panes[paneKey] = PaneOutput{
			Type:      agentType,
			State:     state,
			Lines:     outputLines,
			Truncated: truncated,
		}
	}

	// Generate agent hints based on pane states
	output.AgentHints = generateTailHints(output.Panes)

	return encodeJSON(output)
}

// generateTailHints analyzes pane states and provides actionable hints for AI agents
func generateTailHints(panes map[string]PaneOutput) *TailAgentHints {
	var idle, active []string
	var suggestions []string

	for paneKey, pane := range panes {
		switch pane.State {
		case "idle":
			idle = append(idle, paneKey)
		case "active":
			active = append(active, paneKey)
		case "error":
			suggestions = append(suggestions, fmt.Sprintf("Pane %s has an error - check output", paneKey))
		}
	}

	// Sort for deterministic output (map iteration order is random)
	sort.Strings(idle)
	sort.Strings(active)

	// Generate suggestions based on state distribution
	if len(idle) > 0 && len(active) == 0 {
		suggestions = append(suggestions, fmt.Sprintf("All %d agents idle - ready for new prompts", len(idle)))
	} else if len(idle) > 0 {
		suggestions = append(suggestions, fmt.Sprintf("%d idle agents available for parallel work", len(idle)))
	}
	if len(active) > 0 {
		suggestions = append(suggestions, fmt.Sprintf("%d agents actively working - wait or check progress", len(active)))
	}

	// Return nil if no useful hints
	if len(idle) == 0 && len(active) == 0 && len(suggestions) == 0 {
		return nil
	}

	return &TailAgentHints{
		IdleAgents:   idle,
		ActiveAgents: active,
		Suggestions:  suggestions,
	}
}

// determineState analyzes output to determine if agent is active, idle, or in error state.
// It delegates to the status package for consistent detection logic.
func determineState(output, agentType string) string {
	// Normalize agent type for status package (expects "cc", "cod", etc.)
	shortType := translateAgentTypeForStatus(agentType)

	if status.DetectErrorInOutput(output) != status.ErrorNone {
		return "error"
	}
	if status.DetectIdleFromOutput(output, shortType) {
		return "idle"
	}
	// If output is empty and it's a user pane, treat as idle (prompt)
	if strings.TrimSpace(output) == "" && (agentType == "" || agentType == "user") {
		return "idle"
	}
	// Otherwise assume active/working
	return "active"
}

// stripANSI removes ANSI escape sequences from text.
// This is a compatibility wrapper for status.StripANSI.
func stripANSI(s string) string {
	return status.StripANSI(s)
}

// detectState determines agent state from output lines and title.
// This is a compatibility wrapper for determineState that maintains
// the old function signature used by other files in this package.
func detectState(lines []string, title string) string {
	// Reconstruct the output from lines
	output := strings.Join(lines, "\n")
	agentType := detectAgentType(title)
	// Translate to short form for status package patterns
	agentType = translateAgentTypeForStatus(agentType)
	return determineState(output, agentType)
}

// translateAgentTypeForStatus converts long agent type names to short forms
// expected by the status package patterns.
func translateAgentTypeForStatus(agentType string) string {
	switch agentType {
	case "claude":
		return "cc"
	case "codex":
		return "cod"
	case "gemini":
		return "gmi"
	case "unknown":
		return ""
	default:
		return agentType
	}
}

// isIdlePrompt checks if a line looks like an idle prompt.
// This is a compatibility wrapper for status.IsPromptLine that uses an empty
// agent type for generic prompt detection.
func isIdlePrompt(line string) bool {
	return status.IsPromptLine(line, "")
}

// isPromptLine checks if a line looks like an idle prompt for a specific pane.
// This is a compatibility wrapper for status.IsPromptLine that extracts the
// agent type from the pane title.
func isPromptLine(line, paneTitle string) bool {
	agentType := translateAgentTypeForStatus(detectAgentType(paneTitle))
	return status.IsPromptLine(line, agentType)
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
				lines := splitLines(status.StripANSI(captured))
				agent.OutputTailLines = len(lines)
				agent.State = determineState(captured, agent.Type)
			}

			snapSession.Agents = append(snapSession.Agents, agent)
		}

		output.Sessions = append(output.Sessions, snapSession)
	}

	// Try to get beads summary
	beads := bv.GetBeadsSummary("", BeadLimit)
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
	RobotResponse                     // Embed standard response fields (success, timestamp, error)
	Session        string             `json:"session"`
	SentAt         time.Time          `json:"sent_at"`
	Targets        []string           `json:"targets"`
	Successful     []string           `json:"successful"`
	Failed         []SendError        `json:"failed"`
	MessagePreview string             `json:"message_preview"`
	DryRun         bool               `json:"dry_run,omitempty"`
	WouldSendTo    []string           `json:"would_send_to,omitempty"`
	CASSInjection  *CASSInjectionInfo `json:"cass_injection,omitempty"`
	AgentHints     *SendAgentHints    `json:"_agent_hints,omitempty"`
}

// CASSInjectionInfo reports CASS context injection details in robot responses.
type CASSInjectionInfo struct {
	// Enabled indicates whether CASS injection was enabled.
	Enabled bool `json:"enabled"`
	// Query is the search query that was executed.
	Query string `json:"query,omitempty"`
	// ItemsFound is how many CASS hits were found.
	ItemsFound int `json:"items_found"`
	// ItemsInjected is how many items were actually injected.
	ItemsInjected int `json:"items_injected"`
	// TokensAdded is the estimated token count of injected content.
	TokensAdded int `json:"tokens_added"`
	// Sources lists the sessions that provided context.
	Sources []CASSSource `json:"sources,omitempty"`
	// SkippedReason explains why injection was skipped, if applicable.
	SkippedReason string `json:"skipped_reason,omitempty"`
}

// CASSSource represents a session that provided CASS context.
type CASSSource struct {
	// Session is the session name or path.
	Session string `json:"session"`
	// Relevance is the relevance score (0-100).
	Relevance int `json:"relevance"`
	// AgeDays is how many days old the session is.
	AgeDays int `json:"age_days"`
}

// NewCASSInjectionInfo creates CASSInjectionInfo from an InjectionResult.
func NewCASSInjectionInfo(result InjectionResult, query string, hits []ScoredHit) *CASSInjectionInfo {
	info := &CASSInjectionInfo{
		Enabled:       result.Metadata.Enabled,
		Query:         query,
		ItemsFound:    result.Metadata.ItemsFound,
		ItemsInjected: result.Metadata.ItemsInjected,
		TokensAdded:   result.Metadata.TokensAdded,
		SkippedReason: result.Metadata.SkippedReason,
		Sources:       make([]CASSSource, 0, len(hits)),
	}

	now := time.Now()
	for _, hit := range hits {
		sessionDate := extractSessionDate(hit.SourcePath)
		ageDays := 0
		if !sessionDate.IsZero() {
			ageDays = int(now.Sub(sessionDate).Hours() / 24)
		}

		info.Sources = append(info.Sources, CASSSource{
			Session:   extractSessionName(hit.SourcePath),
			Relevance: int(hit.ComputedScore * 100),
			AgeDays:   ageDays,
		})
	}

	return info
}

// SendAgentHints provides agent guidance specific to send output
type SendAgentHints struct {
	Summary     string   `json:"summary,omitempty"`     // One-line summary of what happened
	Suggestions []string `json:"suggestions,omitempty"` // Actionable next steps
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
	DryRun     bool     // If true, show what would be sent without actually sending

	// CASS injection options
	WithCASS     bool          // Enable CASS context injection
	CASSConfig   *CASSConfig   // CASS query configuration (optional)
	FilterConfig *FilterConfig // CASS filter configuration (optional)
	InjectConfig *InjectConfig // CASS injection configuration (optional)
}

// PrintSend sends a message to multiple panes atomically and returns structured results
func PrintSend(opts SendOptions) error {
	if strings.TrimSpace(opts.Session) == "" {
		return encodeJSON(SendOutput{
			RobotResponse:  NewErrorResponse(fmt.Errorf("session name is required"), ErrCodeInvalidFlag, "Provide a session name"),
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
			RobotResponse:  NewErrorResponse(fmt.Errorf("session '%s' not found", opts.Session), ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions"),
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
			RobotResponse:  NewErrorResponse(fmt.Errorf("failed to get panes: %w", err), ErrCodeInternalError, "Check tmux is running"),
			Session:        opts.Session,
			SentAt:         time.Now().UTC(),
			Targets:        []string{},
			Successful:     []string{},
			Failed:         []SendError{{Pane: "panes", Error: fmt.Sprintf("failed to get panes: %v", err)}},
			MessagePreview: truncateMessage(opts.Message),
		})
	}

	output := SendOutput{
		RobotResponse:  NewRobotResponse(true), // Will be updated based on results
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

	// Perform CASS injection if enabled
	messageToSend := opts.Message
	if opts.WithCASS {
		// Use provided configs or defaults
		queryConfig := DefaultCASSConfig()
		if opts.CASSConfig != nil {
			queryConfig = *opts.CASSConfig
		}

		filterConfig := DefaultFilterConfig()
		if opts.FilterConfig != nil {
			filterConfig = *opts.FilterConfig
		}

		injectConfig := DefaultInjectConfig()
		if opts.InjectConfig != nil {
			injectConfig = *opts.InjectConfig
		}

		// If there are target panes, try to determine agent type for formatting
		if len(targetPanes) > 0 {
			agentType := detectAgentType(targetPanes[0].Title)
			injectConfig.Format = FormatForAgent(agentType)
		}

		// Perform CASS query and injection
		injectResult, queryResult, filterResult := InjectContextFromQuery(
			opts.Message,
			queryConfig,
			filterConfig,
			injectConfig,
		)

		// Record injection metadata
		output.CASSInjection = NewCASSInjectionInfo(injectResult, queryResult.Query, filterResult.Hits)

		// Use modified message if injection succeeded
		if injectResult.Success && injectResult.ModifiedPrompt != "" {
			messageToSend = injectResult.ModifiedPrompt
		}
	}

	// Send to all targets
	for i, pane := range targetPanes {
		paneKey := fmt.Sprintf("%d", pane.Index)

		// Apply delay between sends (except for first)
		if i > 0 && opts.DelayMs > 0 {
			time.Sleep(time.Duration(opts.DelayMs) * time.Millisecond)
		}

		err := tmux.SendKeys(pane.ID, messageToSend, true)
		if err != nil {
			output.Failed = append(output.Failed, SendError{
				Pane:  paneKey,
				Error: err.Error(),
			})
		} else {
			output.Successful = append(output.Successful, paneKey)
		}
	}

	// Update success based on results
	output.Success = len(output.Failed) == 0 && len(output.Successful) > 0
	if len(output.Failed) > 0 {
		output.Error = fmt.Sprintf("%d of %d sends failed", len(output.Failed), len(output.Targets))
		output.ErrorCode = ErrCodeInternalError
	}

	// Generate agent hints
	output.AgentHints = generateSendHints(output)

	return encodeJSON(output)
}

// generateSendHints creates actionable hints based on send results
func generateSendHints(output SendOutput) *SendAgentHints {
	var suggestions []string
	var summary string

	if len(output.Failed) == 0 && len(output.Successful) > 0 {
		summary = fmt.Sprintf("Sent to %d agent(s) successfully", len(output.Successful))
		suggestions = append(suggestions, "Wait for agent acknowledgment using --robot-tail")
	} else if len(output.Failed) > 0 && len(output.Successful) > 0 {
		summary = fmt.Sprintf("Partial success: %d sent, %d failed", len(output.Successful), len(output.Failed))
		suggestions = append(suggestions, "Retry failed panes individually")
	} else if len(output.Failed) > 0 {
		summary = fmt.Sprintf("All %d sends failed", len(output.Failed))
		suggestions = append(suggestions, "Check agent states with --robot-tail")
		suggestions = append(suggestions, "Verify session and pane existence")
	} else if len(output.Targets) == 0 {
		summary = "No target panes matched the filter criteria"
		suggestions = append(suggestions, "Check --all, --panes, or --agent-types flags")
	}

	if summary == "" {
		return nil
	}

	return &SendAgentHints{
		Summary:     summary,
		Suggestions: suggestions,
	}
}

// truncateMessage truncates a message to 50 runes with ellipsis.
// Uses rune count instead of byte count to handle UTF-8 correctly.
func truncateMessage(msg string) string {
	runes := []rune(msg)
	if len(runes) > 50 {
		return string(runes[:47]) + "..."
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
	GeneratedAt   time.Time                   `json:"generated_at"`
	Assignments   []GraphAgentAssignment      `json:"assignments"`
	BeadGraph     map[string]GraphBeadNode    `json:"bead_graph"`
	MailSummary   map[string]GraphMailSummary `json:"mail_summary"`
	OrphanBeads   []string                    `json:"orphan_beads"`
	OrphanThreads []string                    `json:"orphan_threads"`
	Errors        []string                    `json:"errors,omitempty"`
}

// GraphAgentAssignment captures bead/thread membership for an agent.
type GraphAgentAssignment struct {
	Agent        string   `json:"agent"`
	AgentName    string   `json:"agent_name,omitempty"`
	AgentType    string   `json:"agent_type"`
	Program      string   `json:"program,omitempty"`
	Model        string   `json:"model,omitempty"`
	Beads        []string `json:"beads"`
	MailThreads  []string `json:"mail_threads"`
	Pane         string   `json:"pane,omitempty"`
	Session      string   `json:"session,omitempty"`
	Detected     string   `json:"detected_type,omitempty"`
	DetectedFrom string   `json:"detected_from,omitempty"`
}

// GraphBeadNode summarizes bead status and relationships.
type GraphBeadNode struct {
	Status     string   `json:"status"`
	AssignedTo *string  `json:"assigned_to"`
	BlockedBy  []string `json:"blocked_by"`
	Blocking   []string `json:"blocking"`
	Title      string   `json:"title,omitempty"`
}

// GraphMailSummary summarizes a mail thread for correlation.
type GraphMailSummary struct {
	Subject      string    `json:"subject"`
	Participants []string  `json:"participants,omitempty"`
	LastActivity time.Time `json:"last_activity"`
	Unread       int       `json:"unread,omitempty"`
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
		wd := mustGetwd()

		// Get insights (bottlenecks, keystones, etc.)
		insights, err := bv.GetInsights(wd)
		if err != nil {
			output.Error = fmt.Sprintf("failed to get insights: %v", err)
		} else {
			output.Insights = insights
		}

		// Get priority recommendations
		priority, err := bv.GetPriority(wd)
		if err != nil {
			if output.Error == "" {
				output.Error = fmt.Sprintf("failed to get priority: %v", err)
			}
		} else {
			output.Priority = priority
		}

		// Get health summary
		health, err := bv.GetHealthSummary(wd)
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
		GeneratedAt:   now,
		Assignments:   make([]GraphAgentAssignment, 0),
		BeadGraph:     make(map[string]GraphBeadNode),
		MailSummary:   make(map[string]GraphMailSummary),
		OrphanBeads:   make([]string, 0),
		OrphanThreads: make([]string, 0),
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

	assignmentByAgent := make(map[string]*GraphAgentAssignment)
	for _, a := range agents {
		assignmentByAgent[a.Name] = &GraphAgentAssignment{
			Agent:       a.Name,
			AgentName:   a.Name,
			AgentType:   normalizedProgramType(a.Program),
			Program:     a.Program,
			Model:       a.Model,
			Beads:       make([]string, 0),
			MailThreads: make([]string, 0),
		}
	}

	// Add bead assignments from bv summary (if present)
	if beads := bv.GetBeadsSummary(wd, BeadLimit); beads != nil && beads.Available {
		for _, inProg := range beads.InProgressList {
			node := GraphBeadNode{
				Status:    "in_progress",
				BlockedBy: make([]string, 0),
				Blocking:  make([]string, 0),
				Title:     inProg.Title,
			}
			if inProg.Assignee != "" {
				assign := inProg.Assignee
				node.AssignedTo = &assign
				a := assignmentByAgent[assign]
				if a == nil {
					a = &GraphAgentAssignment{
						Agent:       assign,
						AgentName:   assign,
						AgentType:   "unknown",
						Beads:       make([]string, 0),
						MailThreads: make([]string, 0),
					}
					assignmentByAgent[assign] = a
				}
				a.Beads = appendUnique(a.Beads, inProg.ID)
			} else {
				corr.OrphanBeads = appendUnique(corr.OrphanBeads, inProg.ID)
			}
			corr.BeadGraph[inProg.ID] = node
		}

		for _, ready := range beads.ReadyPreview {
			status := "ready"
			node := GraphBeadNode{
				Status:    status,
				BlockedBy: make([]string, 0),
				Blocking:  make([]string, 0),
				Title:     ready.Title,
			}
			corr.BeadGraph[ready.ID] = node
		}
	} else if beads != nil && !beads.Available && beads.Reason != "" {
		corr.Errors = append(corr.Errors, fmt.Sprintf("beads unavailable: %s", beads.Reason))
	}

	// Gather mail threads from per-agent inboxes (best-effort, bounded).
	if len(agents) > 0 && agentMailClient.IsAvailable() {
		const inboxLimit = 50
		for _, a := range agents {
			msgs, err := agentMailClient.FetchInbox(ctx, agentmail.FetchInboxOptions{
				ProjectKey:    wd,
				AgentName:     a.Name,
				Limit:         inboxLimit,
				IncludeBodies: false,
			})
			if err != nil {
				corr.Errors = append(corr.Errors, fmt.Sprintf("agent mail fetch_inbox %s: %v", a.Name, err))
				continue
			}
			for _, msg := range msgs {
				if msg.ThreadID == nil || strings.TrimSpace(*msg.ThreadID) == "" {
					continue
				}
				tid := strings.TrimSpace(*msg.ThreadID)
				thread := corr.MailSummary[tid]
				if thread.Subject == "" {
					thread.Subject = msg.Subject
				}
				if msg.CreatedTS.After(thread.LastActivity) {
					thread.LastActivity = msg.CreatedTS
				}
				thread.Unread++
				corr.MailSummary[tid] = thread

				assign := assignmentByAgent[a.Name]
				if assign == nil {
					assign = &GraphAgentAssignment{
						Agent:       a.Name,
						AgentName:   a.Name,
						AgentType:   normalizedProgramType(a.Program),
						Program:     a.Program,
						Model:       a.Model,
						Beads:       make([]string, 0),
						MailThreads: make([]string, 0),
					}
					assignmentByAgent[a.Name] = assign
				}
				assign.MailThreads = appendUnique(assign.MailThreads, tid)
			}
		}

		// Add participants (best-effort) for a few most-recent threads.
		var threadIDs []string
		for tid := range corr.MailSummary {
			threadIDs = append(threadIDs, tid)
		}
		sort.SliceStable(threadIDs, func(i, j int) bool {
			return corr.MailSummary[threadIDs[i]].LastActivity.After(corr.MailSummary[threadIDs[j]].LastActivity)
		})

		const maxSummaries = 10
		for i, tid := range threadIDs {
			if i >= maxSummaries {
				break
			}
			summary, err := agentMailClient.SummarizeThread(ctx, wd, tid, false)
			if err != nil {
				corr.Errors = append(corr.Errors, fmt.Sprintf("summarize_thread %s: %v", tid, err))
				continue
			}
			thread := corr.MailSummary[tid]
			thread.Participants = summary.Participants
			corr.MailSummary[tid] = thread

			for _, participant := range summary.Participants {
				a := assignmentByAgent[participant]
				if a == nil {
					a = &GraphAgentAssignment{
						Agent:       participant,
						AgentName:   participant,
						AgentType:   "unknown",
						Beads:       make([]string, 0),
						MailThreads: make([]string, 0),
					}
					assignmentByAgent[participant] = a
				}
				a.MailThreads = appendUnique(a.MailThreads, tid)
			}
		}
	}

	// Best-effort tmux pane mapping for Agent Mail agents (NTM sessions).
	if tmux.IsInstalled() {
		sessions, err := tmux.ListSessions()
		if err != nil {
			corr.Errors = append(corr.Errors, fmt.Sprintf("tmux list_sessions: %v", err))
		} else {
			agentsByType := groupAgentsByType(agents)
			for _, sess := range sessions {
				panes, err := tmux.GetPanes(sess.Name)
				if err != nil {
					continue
				}
				paneInfos := parseNTMPanes(panes)
				for _, paneType := range []string{"cc", "cod", "gmi"} {
					mapping := assignAgentsToPanes(paneInfos[paneType], agentsByType[paneType])
					for _, pane := range paneInfos[paneType] {
						agentName := mapping[pane.Label]
						if agentName == "" {
							continue
						}
						a := assignmentByAgent[agentName]
						if a == nil {
							a = &GraphAgentAssignment{
								Agent:       agentName,
								AgentName:   agentName,
								AgentType:   normalizedProgramType(""),
								Beads:       make([]string, 0),
								MailThreads: make([]string, 0),
							}
							assignmentByAgent[agentName] = a
						}
						a.Session = sess.Name
						a.Pane = fmt.Sprintf("%d.%d", 0, pane.TmuxIndex)
						a.Agent = fmt.Sprintf("%s:%s", sess.Name, a.Pane)
						a.Detected = paneType
						a.DetectedFrom = "ntm_pane_title"
					}
				}
			}
		}
	}

	// Fill dependency edges for in-progress beads (best-effort, bounded).
	if _, err := exec.LookPath("bd"); err == nil {
		for beadID, node := range corr.BeadGraph {
			if node.Status != "in_progress" {
				continue
			}
			blockedBy, deps, err := getBeadNeighbors(wd, beadID, "down")
			if err != nil {
				corr.Errors = append(corr.Errors, fmt.Sprintf("bd dep tree down %s: %v", beadID, err))
			} else {
				node.BlockedBy = blockedBy
				for _, dep := range deps {
					if _, ok := corr.BeadGraph[dep.ID]; ok {
						continue
					}
					corr.BeadGraph[dep.ID] = GraphBeadNode{
						Status:     dep.Status,
						AssignedTo: nil,
						BlockedBy:  make([]string, 0),
						Blocking:   make([]string, 0),
						Title:      dep.Title,
					}
				}
			}

			blocking, deps, err := getBeadNeighbors(wd, beadID, "up")
			if err != nil {
				corr.Errors = append(corr.Errors, fmt.Sprintf("bd dep tree up %s: %v", beadID, err))
			} else {
				node.Blocking = blocking
				for _, dep := range deps {
					if _, ok := corr.BeadGraph[dep.ID]; ok {
						continue
					}
					corr.BeadGraph[dep.ID] = GraphBeadNode{
						Status:     dep.Status,
						AssignedTo: nil,
						BlockedBy:  make([]string, 0),
						Blocking:   make([]string, 0),
						Title:      dep.Title,
					}
				}
			}

			corr.BeadGraph[beadID] = node
		}
	}

	// Orphan threads: threads not linked to any bead ID.
	for tid := range corr.MailSummary {
		if _, ok := corr.BeadGraph[tid]; !ok {
			corr.OrphanThreads = appendUnique(corr.OrphanThreads, tid)
		}
	}

	// Materialize assignment list (stable order).
	for _, a := range assignmentByAgent {
		corr.Assignments = append(corr.Assignments, *a)
	}
	sort.SliceStable(corr.Assignments, func(i, j int) bool {
		return corr.Assignments[i].AgentName < corr.Assignments[j].AgentName
	})

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

type bdDepTreeNode struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Depth  int    `json:"depth"`
}

func getBeadNeighbors(dir, issueID, direction string) ([]string, []bdDepTreeNode, error) {
	if issueID == "" {
		return nil, nil, fmt.Errorf("issue id is empty")
	}
	if direction != "down" && direction != "up" {
		return nil, nil, fmt.Errorf("invalid direction %q", direction)
	}

	out, err := bv.RunBd(dir, "dep", "tree", issueID, "--direction="+direction, "--max-depth=1", "--json")
	if err != nil {
		return nil, nil, fmt.Errorf("bd dep tree: %w", err)
	}

	var nodes []bdDepTreeNode
	if err := json.Unmarshal([]byte(out), &nodes); err != nil {
		return nil, nil, fmt.Errorf("parse bd dep tree json: %w", err)
	}

	seen := make(map[string]bool)
	ids := make([]string, 0)
	cleaned := make([]bdDepTreeNode, 0)
	for _, n := range nodes {
		n.ID = strings.TrimSpace(n.ID)
		if n.ID == "" || seen[n.ID] {
			continue
		}
		seen[n.ID] = true
		if strings.TrimSpace(n.Status) == "" {
			n.Status = "unknown"
		}
		ids = append(ids, n.ID)
		cleaned = append(cleaned, n)
	}

	sort.Strings(ids)
	sort.SliceStable(cleaned, func(i, j int) bool { return cleaned[i].ID < cleaned[j].ID })
	return ids, cleaned, nil
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
		beadsSummary = bv.GetBeadsSummary("", 0)
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
						_ = splitLines(status.StripANSI(captured)) // Just for consistency, unused here
						paneState := determineState(captured, agentType)
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

// ContextOutput is the structured output for --robot-context
type ContextOutput struct {
	RobotResponse
	Session    string             `json:"session"`
	CapturedAt time.Time          `json:"captured_at"`
	Agents     []AgentContextInfo `json:"agents"`
	Summary    ContextSummary     `json:"summary"`
	AgentHints *ContextAgentHints `json:"_agent_hints,omitempty"`
}

// AgentContextInfo contains context window information for a single agent pane
type AgentContextInfo struct {
	Pane            string  `json:"pane"`
	PaneIdx         int     `json:"pane_idx"`
	AgentType       string  `json:"agent_type"`
	Model           string  `json:"model"`
	EstimatedTokens int     `json:"estimated_tokens"`
	WithOverhead    int     `json:"with_overhead"`
	ContextLimit    int     `json:"context_limit"`
	UsagePercent    float64 `json:"usage_percent"`
	UsageLevel      string  `json:"usage_level"`
	Confidence      string  `json:"confidence"`
	State           string  `json:"state"`
}

// ContextSummary aggregates context usage across all agents
type ContextSummary struct {
	TotalAgents    int     `json:"total_agents"`
	HighUsageCount int     `json:"high_usage_count"`
	AvgUsage       float64 `json:"avg_usage"`
}

// ContextAgentHints provides agent guidance for context output
type ContextAgentHints struct {
	LowUsageAgents  []string `json:"low_usage_agents,omitempty"`
	HighUsageAgents []string `json:"high_usage_agents,omitempty"`
	Suggestions     []string `json:"suggestions,omitempty"`
}

// getUsageLevel returns a human-readable usage level based on percentage
func getUsageLevel(pct float64) string {
	switch {
	case pct < 40:
		return "Low"
	case pct < 70:
		return "Medium"
	case pct < 85:
		return "High"
	default:
		return "Critical"
	}
}

// getContextLimit returns the context window limit for a model
func getContextLimit(model string) int {
	switch model {
	case "opus", "sonnet":
		return 200000
	case "haiku":
		return 200000
	case "gpt4", "o4-mini":
		return 128000
	case "o1", "o3":
		return 200000
	case "gemini", "pro", "flash":
		return 1000000
	default:
		return 128000 // Conservative default
	}
}

// generateContextHints creates agent hints based on usage patterns
func generateContextHints(lowUsage, highUsage []string, highCount, total int) *ContextAgentHints {
	if total == 0 {
		return nil
	}

	hints := &ContextAgentHints{
		LowUsageAgents:  lowUsage,
		HighUsageAgents: highUsage,
		Suggestions:     make([]string, 0),
	}

	if highCount == 0 {
		// No high usage agents
		if len(lowUsage) == total {
			hints.Suggestions = append(hints.Suggestions, "All agents healthy - context usage is low across the board")
		} else if len(lowUsage) > 0 {
			hints.Suggestions = append(hints.Suggestions, fmt.Sprintf("%d agent(s) have low usage, others are moderate", len(lowUsage)))
		} else {
			hints.Suggestions = append(hints.Suggestions, "All agents at moderate context usage - no immediate concerns")
		}
	} else if highCount == total {
		hints.Suggestions = append(hints.Suggestions, "All agents have high context usage - consider spawning new sessions")
	} else {
		hints.Suggestions = append(hints.Suggestions, fmt.Sprintf("%d agent(s) have high context usage", highCount))
		if len(lowUsage) > 0 {
			hints.Suggestions = append(hints.Suggestions, fmt.Sprintf("%d agent(s) have room for additional work", len(lowUsage)))
		}
	}

	return hints
}

// PrintContext outputs context window usage information for all agents in a session.
func PrintContext(session string, lines int) error {
	if !tmux.SessionExists(session) {
		return encodeJSON(ContextOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("session '%s' not found", session),
				ErrCodeSessionNotFound,
				"Use 'ntm list' to see available sessions",
			),
			Session:    session,
			CapturedAt: time.Now().UTC(),
		})
	}

	panes, err := tmux.GetPanes(session)
	if err != nil {
		return encodeJSON(ContextOutput{
			RobotResponse: NewErrorResponse(err, ErrCodeInternalError, "Failed to get panes"),
			Session:       session,
			CapturedAt:    time.Now().UTC(),
		})
	}

	output := ContextOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       session,
		CapturedAt:    time.Now().UTC(),
		Agents:        make([]AgentContextInfo, 0, len(panes)),
	}

	var lowUsage, highUsage []string
	var totalUsage float64

	for _, pane := range panes {
		agentType := detectAgentType(pane.Title)
		if agentType == "unknown" || agentType == "user" {
			continue // Skip non-agent panes
		}

		model := detectModel(agentType, pane.Title)

		scrollback, _ := tmux.CapturePaneOutput(pane.ID, lines)
		cleanText := stripANSI(scrollback)
		lineList := splitLines(cleanText)
		state := detectState(lineList, pane.Title)

		charCount := len(cleanText)
		// Rough token estimate: ~4 chars per token
		estTokens := charCount / 4
		// Add overhead for system prompts and other context (2.5x multiplier)
		withOverhead := int(float64(estTokens) * 2.5)
		contextLimit := getContextLimit(model)
		usagePct := float64(withOverhead) / float64(contextLimit) * 100

		paneKey := fmt.Sprintf("%d", pane.Index)
		usageLevel := getUsageLevel(usagePct)

		// Align thresholds with getUsageLevel: <40% is Low, >=70% is High/Critical
		if usagePct < 40 {
			lowUsage = append(lowUsage, paneKey)
		} else if usagePct >= 70 {
			highUsage = append(highUsage, paneKey)
		}
		totalUsage += usagePct

		agentInfo := AgentContextInfo{
			Pane:            paneKey,
			PaneIdx:         pane.Index,
			AgentType:       agentType,
			Model:           model,
			EstimatedTokens: estTokens,
			WithOverhead:    withOverhead,
			ContextLimit:    contextLimit,
			UsagePercent:    usagePct,
			UsageLevel:      usageLevel,
			Confidence:      "low", // Scrollback-based estimation is low confidence
			State:           state,
		}
		output.Agents = append(output.Agents, agentInfo)
	}

	output.Summary.TotalAgents = len(output.Agents)
	output.Summary.HighUsageCount = len(highUsage)
	if len(output.Agents) > 0 {
		output.Summary.AvgUsage = totalUsage / float64(len(output.Agents))
	}

	output.AgentHints = generateContextHints(lowUsage, highUsage, len(highUsage), len(output.Agents))

	return encodeJSON(output)
}

// =============================================================================
// Activity Detection API
// =============================================================================

// ActivityOptions holds options for the activity API.
type ActivityOptions struct {
	Session    string   // Required: session name
	Panes      []string // Optional: filter to specific pane indices
	AgentTypes []string // Optional: filter to specific agent types (claude, codex, gemini)
}

// ActivityOutput represents the output for --robot-activity
type ActivityOutput struct {
	RobotResponse
	Session    string              `json:"session"`
	CapturedAt time.Time           `json:"captured_at"`
	Agents     []AgentActivityInfo `json:"agents"`
	Summary    ActivitySummary     `json:"summary"`
	AgentHints *ActivityAgentHints `json:"_agent_hints,omitempty"`
}

// AgentActivityInfo contains activity state for a single agent pane.
type AgentActivityInfo struct {
	Pane             string   `json:"pane"`                        // pane index as string
	PaneIdx          int      `json:"pane_idx"`                    // pane index as int
	AgentType        string   `json:"agent_type"`                  // claude, codex, gemini
	State            string   `json:"state"`                       // GENERATING, WAITING, THINKING, ERROR, STALLED, UNKNOWN
	Confidence       float64  `json:"confidence"`                  // 0.0-1.0
	Velocity         float64  `json:"velocity"`                    // chars/sec
	StateSince       string   `json:"state_since,omitempty"`       // RFC3339 timestamp
	DetectedPatterns []string `json:"detected_patterns,omitempty"` // pattern names that matched
	LastOutput       string   `json:"last_output,omitempty"`       // RFC3339 timestamp of last output
}

// ActivitySummary provides aggregate state counts.
type ActivitySummary struct {
	TotalAgents int            `json:"total_agents"`
	ByState     map[string]int `json:"by_state"` // state -> count
}

// ActivityAgentHints provides actionable hints for AI agents.
type ActivityAgentHints struct {
	Summary          string   `json:"summary"`
	AvailableAgents  []string `json:"available_agents,omitempty"` // panes in WAITING state
	BusyAgents       []string `json:"busy_agents,omitempty"`      // panes in GENERATING/THINKING state
	ProblemAgents    []string `json:"problem_agents,omitempty"`   // panes in ERROR/STALLED state
	SuggestedActions []string `json:"suggested_actions,omitempty"`
}

// PrintActivity outputs agent activity state for a session.
// This is the handler for --robot-activity flag.
func PrintActivity(opts ActivityOptions) error {
	if !tmux.SessionExists(opts.Session) {
		return RobotError(
			fmt.Errorf("session '%s' not found", opts.Session),
			ErrCodeSessionNotFound,
			"Use 'ntm list' to see available sessions",
		)
	}

	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		return RobotError(
			fmt.Errorf("failed to get panes: %w", err),
			ErrCodeInternalError,
			"Check tmux is running and session is accessible",
		)
	}

	output := ActivityOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		CapturedAt:    time.Now().UTC(),
		Agents:        make([]AgentActivityInfo, 0, len(panes)),
		Summary: ActivitySummary{
			ByState: make(map[string]int),
		},
	}

	// Build filter maps
	paneFilterMap := make(map[string]bool)
	for _, p := range opts.Panes {
		paneFilterMap[p] = true
	}
	hasPaneFilter := len(paneFilterMap) > 0

	typeFilterMap := make(map[string]bool)
	for _, t := range opts.AgentTypes {
		typeFilterMap[normalizeAgentType(t)] = true
	}
	hasTypeFilter := len(typeFilterMap) > 0

	// Collect activity data
	var availableAgents, busyAgents, problemAgents []string

	for _, pane := range panes {
		paneKey := fmt.Sprintf("%d", pane.Index)

		// Apply pane filter
		if hasPaneFilter && !paneFilterMap[paneKey] && !paneFilterMap[pane.ID] {
			continue
		}

		agentType := detectAgentType(pane.Title)

		// Skip non-agent panes (user, unknown)
		if agentType == "unknown" || agentType == "user" {
			continue
		}

		// Apply type filter
		if hasTypeFilter && !typeFilterMap[agentType] {
			continue
		}

		// Create classifier for this pane
		classifier := NewStateClassifier(pane.ID, &ClassifierConfig{
			AgentType: agentType,
		})

		// Classify current state
		activity, err := classifier.Classify()
		if err != nil {
			// Include with unknown state on error
			output.Agents = append(output.Agents, AgentActivityInfo{
				Pane:       paneKey,
				PaneIdx:    pane.Index,
				AgentType:  agentType,
				State:      string(StateUnknown),
				Confidence: 0.0,
			})
			output.Summary.ByState[string(StateUnknown)]++
			continue
		}

		// Build agent info
		info := AgentActivityInfo{
			Pane:             paneKey,
			PaneIdx:          pane.Index,
			AgentType:        activity.AgentType,
			State:            string(activity.State),
			Confidence:       activity.Confidence,
			Velocity:         activity.Velocity,
			DetectedPatterns: activity.DetectedPatterns,
		}

		if !activity.StateSince.IsZero() {
			info.StateSince = FormatTimestamp(activity.StateSince)
		}
		if !activity.LastOutput.IsZero() {
			info.LastOutput = FormatTimestamp(activity.LastOutput)
		}

		output.Agents = append(output.Agents, info)

		// Update summary
		stateStr := string(activity.State)
		output.Summary.ByState[stateStr]++

		// Categorize for hints
		switch activity.State {
		case StateWaiting:
			availableAgents = append(availableAgents, paneKey)
		case StateGenerating, StateThinking:
			busyAgents = append(busyAgents, paneKey)
		case StateError, StateStalled:
			problemAgents = append(problemAgents, paneKey)
		}
	}

	output.Summary.TotalAgents = len(output.Agents)

	// Generate agent hints
	output.AgentHints = generateActivityHints(availableAgents, busyAgents, problemAgents, output.Summary)

	return encodeJSON(output)
}

// generateActivityHints creates actionable hints based on agent states.
func generateActivityHints(available, busy, problem []string, summary ActivitySummary) *ActivityAgentHints {
	hints := &ActivityAgentHints{
		AvailableAgents: available,
		BusyAgents:      busy,
		ProblemAgents:   problem,
	}

	// Build summary
	total := summary.TotalAgents
	availCount := len(available)
	busyCount := len(busy)
	problemCount := len(problem)

	if total == 0 {
		hints.Summary = "No agents found in session"
		hints.SuggestedActions = []string{"Use --robot-spawn to create agents"}
		return hints
	}

	hints.Summary = fmt.Sprintf("%d agents: %d available, %d busy, %d problems",
		total, availCount, busyCount, problemCount)

	// Generate suggestions
	if problemCount > 0 {
		hints.SuggestedActions = append(hints.SuggestedActions,
			fmt.Sprintf("Check error/stalled agents in panes: %s", strings.Join(problem, ", ")))
	}

	if availCount > 0 && busyCount == 0 {
		hints.SuggestedActions = append(hints.SuggestedActions,
			"All agents idle - ready for new prompts")
	}

	if availCount == 0 && busyCount > 0 {
		hints.SuggestedActions = append(hints.SuggestedActions,
			"All agents busy - wait or use --robot-ack to monitor completion")
	}

	if availCount > 0 {
		hints.SuggestedActions = append(hints.SuggestedActions,
			fmt.Sprintf("Send work to available panes: %s", strings.Join(available, ", ")))
	}

	return hints
}

// normalizeAgentType normalizes agent type aliases.
func normalizeAgentType(t string) string {
	switch strings.ToLower(t) {
	case "cc", "claude-code", "claude":
		return "claude"
	case "cod", "codex-cli", "codex":
		return "codex"
	case "gmi", "gemini-cli", "gemini":
		return "gemini"
	default:
		return strings.ToLower(t)
	}
}

// ============================================================================
// --robot-diff: Compare agent activity and file changes
// ============================================================================

// DiffOptions holds options for the --robot-diff command.
type DiffOptions struct {
	Session string        // Required: session name
	Since   time.Duration // Duration to look back (default: 15m)
}

// DiffOutput is the structured output for --robot-diff.
type DiffOutput struct {
	RobotResponse
	Session       string          `json:"session"`
	Timeframe     DiffTimeframe   `json:"timeframe"`
	Files         DiffFiles       `json:"files"`
	AgentActivity []DiffAgentInfo `json:"agent_activity"`
	AgentHints    *DiffAgentHints `json:"_agent_hints,omitempty"`
}

// DiffTimeframe describes the analysis time window.
type DiffTimeframe struct {
	Since      string `json:"since"`       // Duration string (e.g., "15m")
	SinceTS    string `json:"since_ts"`    // RFC3339 timestamp
	CapturedAt string `json:"captured_at"` // RFC3339 timestamp
}

// DiffFiles categorizes files by modification status.
type DiffFiles struct {
	Modified           []string       `json:"modified"`
	PotentialConflicts []DiffConflict `json:"potential_conflicts"`
	Clean              []string       `json:"clean"`
}

// DiffConflict represents a potential file conflict.
type DiffConflict struct {
	File            string   `json:"file"`
	LikelyModifiers []string `json:"likely_modifiers"`
	Reason          string   `json:"reason"`
	Confidence      float64  `json:"confidence"`
}

// DiffAgentInfo provides activity info for a single agent pane.
type DiffAgentInfo struct {
	Pane        string `json:"pane"`
	AgentType   string `json:"agent_type"`
	State       string `json:"state"`
	OutputLines int    `json:"output_lines"`
	ActiveTime  string `json:"active_time,omitempty"`
}

// DiffAgentHints provides actionable hints for AI agents.
type DiffAgentHints struct {
	Summary          string   `json:"summary"`
	ConflictWarnings []string `json:"conflict_warnings,omitempty"`
	SuggestedActions []string `json:"suggested_actions,omitempty"`
}

// PrintDiff outputs agent activity comparison and file change analysis.
func PrintDiff(opts DiffOptions) error {
	// Validate session exists
	if !tmux.SessionExists(opts.Session) {
		return RobotError(
			fmt.Errorf("session '%s' not found", opts.Session),
			ErrCodeSessionNotFound,
			"Use 'ntm list' to see available sessions",
		)
	}

	// Default to 15 minutes if not specified
	if opts.Since == 0 {
		opts.Since = 15 * time.Minute
	}

	now := time.Now().UTC()
	sinceTime := now.Add(-opts.Since)

	output := DiffOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		Timeframe: DiffTimeframe{
			Since:      opts.Since.String(),
			SinceTS:    sinceTime.Format(time.RFC3339),
			CapturedAt: now.Format(time.RFC3339),
		},
		Files: DiffFiles{
			Modified:           []string{},
			PotentialConflicts: []DiffConflict{},
			Clean:              []string{},
		},
		AgentActivity: []DiffAgentInfo{},
	}

	// Get panes for agent activity
	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		return RobotError(
			fmt.Errorf("failed to get panes: %w", err),
			ErrCodeInternalError,
			"Check tmux is running and session is accessible",
		)
	}

	// Create conflict detector for file analysis
	wd, err := os.Getwd()
	if err != nil {
		// Fall back to empty path - conflict detection will be limited
		wd = ""
	}
	detector := NewConflictDetector(&ConflictDetectorConfig{
		RepoPath: wd,
	})

	// Analyze activity windows per pane
	for _, pane := range panes {
		agentType := string(pane.Type)
		if agentType == "" || agentType == "unknown" {
			agentType = "user"
		}

		// Capture pane output for state detection
		captured, _ := tmux.CapturePaneOutput(pane.ID, 100)
		lines := strings.Split(captured, "\n")

		// Use the proper detectState function for accurate state detection
		state := detectState(lines, pane.Title)
		if state == "" {
			state = "idle"
		}

		info := DiffAgentInfo{
			Pane:        pane.Title,
			AgentType:   agentType,
			State:       state,
			OutputLines: len(lines),
		}
		output.AgentActivity = append(output.AgentActivity, info)

		// Record activity window for conflict detection
		detector.RecordActivity(pane.ID, agentType, sinceTime, now, len(lines) > 0)
	}

	// Track issues for hints
	var analysisIssues []string

	// Get modified files from git
	gitStatus, gitErr := detector.GetGitStatus()
	if gitErr == nil {
		for _, fs := range gitStatus {
			output.Files.Modified = append(output.Files.Modified, fs.Path)
		}
	} else if wd != "" {
		// Only note git issues if we had a valid working directory
		analysisIssues = append(analysisIssues, "Could not get git status")
	}

	// Detect potential conflicts
	ctx := context.Background()
	conflicts, conflictErr := detector.DetectConflicts(ctx)
	if conflictErr != nil && wd != "" {
		analysisIssues = append(analysisIssues, "Conflict detection incomplete")
	}
	for _, c := range conflicts {
		output.Files.PotentialConflicts = append(output.Files.PotentialConflicts, DiffConflict{
			File:            c.Path,
			LikelyModifiers: c.LikelyModifiers,
			Reason:          string(c.Reason),
			Confidence:      c.Confidence,
		})
	}

	// Generate hints
	hints := &DiffAgentHints{
		Summary: fmt.Sprintf("%s activity: %d files modified, %d agents",
			opts.Session, len(output.Files.Modified), len(panes)),
	}

	// Add analysis issues as warnings
	if len(analysisIssues) > 0 {
		hints.ConflictWarnings = append(hints.ConflictWarnings, analysisIssues...)
	}

	if len(output.Files.PotentialConflicts) > 0 {
		hints.ConflictWarnings = append(hints.ConflictWarnings,
			fmt.Sprintf("%d potential conflict(s) detected", len(output.Files.PotentialConflicts)))
		hints.SuggestedActions = append(hints.SuggestedActions,
			"Review conflicts before committing")
	}

	if len(output.Files.Modified) == 0 {
		hints.SuggestedActions = append(hints.SuggestedActions,
			fmt.Sprintf("No file changes in the last %s", opts.Since.String()))
	}

	output.AgentHints = hints

	return encodeJSON(output)
}
