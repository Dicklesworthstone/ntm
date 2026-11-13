package robot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/git"
	"github.com/Dicklesworthstone/ntm/internal/health"
	"github.com/Dicklesworthstone/ntm/internal/pressure"
	"github.com/Dicklesworthstone/ntm/internal/robot/adapters"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/status"
	swarmlib "github.com/Dicklesworthstone/ntm/internal/swarm"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/worksource"
)

func newSnapshotOutput(cfg *config.Config) *SnapshotOutput {
	if cfg == nil {
		cfg = config.Default()
	}
	return &SnapshotOutput{
		RobotResponse:            NewRobotResponse(true),
		SchemaID:                 defaultRobotSchemaID("snapshot"),
		SchemaVersion:            snapshotSchemaVersion,
		Timestamp:                time.Now().UTC().Format(time.RFC3339),
		SafetyProfile:            cfg.Safety.Profile,
		AttentionContractVersion: AttentionContractVersion,
		Sessions:                 []SnapshotSession{},
		ActiveIncidents:          []SnapshotIncident{},
		Summary:                  StatusSummary{AgentsByState: map[string]int{}, AgentsByType: map[string]int{}},
		Alerts:                   []string{},
	}
}

var collectSnapshotResourcePressure = collectLiveSnapshotResourcePressure

func attachSnapshotResourcePressure(output *SnapshotOutput) {
	if output == nil {
		return
	}
	output.ResourcePressure = collectSnapshotResourcePressure()
}

func collectLiveSnapshotResourcePressure() *pressure.RobotPressure {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	g := pressure.New(pressure.Config{
		Mode:      pressure.ModeObserve,
		Providers: []pressure.Provider{pressure.NewSystemProvider()},
	})
	g.Refresh(ctx)
	snapshot := g.RobotSnapshot()
	return &snapshot
}

func cloneSnapshotOutput(base *SnapshotOutput) *SnapshotOutput {
	if base == nil {
		return newSnapshotOutput(config.Default())
	}

	cloned := *base
	// sessions, active_incidents, and alerts are required arrays with no
	// omitempty, so they must clone to an empty slice rather than nil: appending
	// zero elements to a nil slice yields nil, which marshals as JSON null and
	// breaks consumers that treat these as always-present arrays.
	cloned.Sessions = append(make([]SnapshotSession, 0, len(base.Sessions)), base.Sessions...)
	cloned.ActiveIncidents = append(make([]SnapshotIncident, 0, len(base.ActiveIncidents)), base.ActiveIncidents...)
	cloned.Alerts = append(make([]string, 0, len(base.Alerts)), base.Alerts...)
	cloned.Tools = append([]ToolInfoOutput(nil), base.Tools...)
	cloned.AlertsDetailed = append([]AlertInfo(nil), base.AlertsDetailed...)
	cloned.Summary = StatusSummary{
		TotalSessions:    base.Summary.TotalSessions,
		TotalAgents:      base.Summary.TotalAgents,
		AttachedCount:    base.Summary.AttachedCount,
		ClaudeCount:      base.Summary.ClaudeCount,
		CodexCount:       base.Summary.CodexCount,
		GeminiCount:      base.Summary.GeminiCount,
		AntigravityCount: base.Summary.AntigravityCount,
		GrokCount:        base.Summary.GrokCount,
		CursorCount:      base.Summary.CursorCount,
		WindsurfCount:    base.Summary.WindsurfCount,
		AiderCount:       base.Summary.AiderCount,
		OpencodeCount:    base.Summary.OpencodeCount,
		OmpCount:         base.Summary.OmpCount,
		OllamaCount:      base.Summary.OllamaCount,
		AgentsByState:    make(map[string]int, len(base.Summary.AgentsByState)),
		AgentsByType:     make(map[string]int, len(base.Summary.AgentsByType)),
		ReadyWork:        base.Summary.ReadyWork,
		InProgress:       base.Summary.InProgress,
		HealthScore:      base.Summary.HealthScore,
		HealthStatus:     base.Summary.HealthStatus,
		AlertsActive:     base.Summary.AlertsActive,
		MailUnread:       base.Summary.MailUnread,
		MailUrgent:       base.Summary.MailUrgent,
	}
	for key, value := range base.Summary.AgentsByState {
		cloned.Summary.AgentsByState[key] = value
	}
	for key, value := range base.Summary.AgentsByType {
		cloned.Summary.AgentsByType[key] = value
	}

	if base.ResourcePressure != nil {
		resourcePressure := *base.ResourcePressure
		resourcePressure.Limiting = append([]string(nil), base.ResourcePressure.Limiting...)
		resourcePressure.Sources = append([]pressure.RobotSource(nil), base.ResourcePressure.Sources...)
		cloned.ResourcePressure = &resourcePressure
	}

	if base.Disk != nil {
		disk := *base.Disk
		if base.Disk.DeltaBytesPerMin != nil {
			delta := *base.Disk.DeltaBytesPerMin
			disk.DeltaBytesPerMin = &delta
		}
		cloned.Disk = &disk
	}

	if base.AgentMail != nil {
		agentMailCopy := *base.AgentMail
		if base.AgentMail.Agents != nil {
			agentMailCopy.Agents = make(map[string]SnapshotAgentMailStats, len(base.AgentMail.Agents))
			for name, stats := range base.AgentMail.Agents {
				agentMailCopy.Agents[name] = stats
			}
		}
		cloned.AgentMail = &agentMailCopy
	}

	return &cloned
}

func populateSnapshotFeedMetadata(output *SnapshotOutput, feed *AttentionFeed) {
	if output == nil || feed == nil {
		return
	}

	feedStats := feed.Stats()
	output.LatestCursor = feedStats.NewestCursor

	replayWindow := SnapshotReplayWindowInfo{
		OldestCursor:    feedStats.OldestCursor,
		LatestCursor:    feedStats.NewestCursor,
		EventCount:      feedStats.Count,
		RetentionPeriod: feedStats.RetentionPeriod.String(),
		ResyncCommand:   "ntm --robot-snapshot",
	}

	if feedStats.Count == 0 {
		replayWindow.Supported = false
		replayWindow.Reason = "no events in feed yet"
	} else {
		replayWindow.Supported = true
		replayWindow.Reason = "ready"
		if oldestEvents, _, err := feed.Replay(feedStats.OldestCursor-1, 1); err == nil && len(oldestEvents) > 0 {
			replayWindow.OldestTimestamp = oldestEvents[0].Ts
		}
		if newestEvents, _, err := feed.Replay(feedStats.NewestCursor-1, 1); err == nil && len(newestEvents) > 0 {
			replayWindow.LatestTimestamp = newestEvents[0].Ts
		}
	}

	output.ReplayWindow = replayWindow
	output.AttentionSummary = buildSnapshotAttentionSummary(feed)
}

// SnapshotOutput provides complete system state for AI orchestration
type SnapshotOutput struct {
	RobotResponse
	SchemaID                 string                        `json:"schema_id"`
	SchemaVersion            string                        `json:"schema_version"`
	Timestamp                string                        `json:"ts"`
	SafetyProfile            string                        `json:"safety_profile,omitempty"`
	AttentionContractVersion string                        `json:"attention_contract_version"`
	LatestCursor             int64                         `json:"latest_cursor"`
	ReplayWindow             SnapshotReplayWindowInfo      `json:"replay_window"`
	Sessions                 []SnapshotSession             `json:"sessions"`
	ActiveIncidents          []SnapshotIncident            `json:"active_incidents"`
	Sources                  *adapters.SourceHealthSection `json:"sources,omitempty"`
	DegradedSources          []string                      `json:"degraded_sources,omitempty"`
	Pagination               *PaginationInfo               `json:"pagination,omitempty"`
	AgentHints               *AgentHints                   `json:"_agent_hints,omitempty"`
	Summary                  StatusSummary                 `json:"summary"`
	Work                     *adapters.WorkSection         `json:"work,omitempty"`
	BeadsSummary             *bv.BeadsSummary              `json:"beads_summary,omitempty"`
	Handoff                  *HandoffSummary               `json:"handoff,omitempty"`
	Coordination             *SnapshotCoordinationSummary  `json:"coordination,omitempty"`
	Quota                    *adapters.QuotaSection        `json:"quota,omitempty"`
	AgentMail                *SnapshotAgentMail            `json:"agent_mail,omitempty"`
	MailUnread               int                           `json:"mail_unread,omitempty"`
	Tools                    []ToolInfoOutput              `json:"tools,omitempty"`             // Flywheel tool inventory and health
	ResourcePressure         *pressure.RobotPressure       `json:"resource_pressure,omitempty"` // Host pressure projection for large-swarm operators
	Disk                     *DiskSection                  `json:"disk,omitempty"`              // Disk usage trajectory for the working dir's filesystem (ntm-1k9g)
	Swarm                    *SwarmSnapshot                `json:"swarm,omitempty"`             // Active swarm orchestration state (optional)
	Alerts                   []string                      `json:"alerts"`                      // Legacy: simple string alerts
	AlertsDetailed           []AlertInfo                   `json:"alerts_detailed,omitempty"`   // Rich alert objects
	AlertSummary             *AlertSummaryInfo             `json:"alert_summary,omitempty"`
	AttentionSummary         *SnapshotAttentionSummary     `json:"attention_summary,omitempty"` // Compact feed summary for bootstrap orientation
}

// NOTE: SnapshotAttentionSummary and SnapshotAttentionItem types are defined in
// attention_contract.go (br-slg9g: Attention Feed Phase 2a2)

// SnapshotReplayWindowInfo describes the currently replayable cursor window.
// This gives operators a mechanical handoff boundary for replay-oriented commands.
// The snapshot bootstrap contract is:
//   - Use `latest_cursor` as the starting point for --robot-events --since-cursor=<cursor>
//   - If `supported` is false, see `reason` for why replay is unavailable
//   - If a cursor expires, use `resync_command` to get a fresh snapshot
type SnapshotReplayWindowInfo struct {
	Supported       bool   `json:"supported"`
	Reason          string `json:"reason,omitempty"`           // Why replay is/isn't supported
	OldestCursor    int64  `json:"oldest_cursor"`              // Earliest cursor still in retention
	LatestCursor    int64  `json:"latest_cursor"`              // Most recent cursor (use for --since-cursor)
	EventCount      int    `json:"event_count"`                // Events in the replay window
	OldestTimestamp string `json:"oldest_timestamp,omitempty"` // RFC3339 timestamp of oldest event
	LatestTimestamp string `json:"latest_timestamp,omitempty"` // RFC3339 timestamp of latest event
	RetentionPeriod string `json:"retention_period,omitempty"` // How long events are retained
	ResyncCommand   string `json:"resync_command,omitempty"`   // Command to run when cursor expires
}

// AlertInfo provides detailed alert information for robot output
type AlertInfo struct {
	ID         string                 `json:"id"`
	Source     string                 `json:"source,omitempty"`
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

// SnapshotIncident is the stable incident shape surfaced to robot clients.
type SnapshotIncident struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	Family           string   `json:"family,omitempty"`
	Category         string   `json:"category,omitempty"`
	Status           string   `json:"status"`
	Severity         string   `json:"severity"`
	SessionNames     []string `json:"session_names"`
	AgentIDs         []string `json:"agent_ids"`
	AlertCount       int      `json:"alert_count"`
	EventCount       int      `json:"event_count"`
	FirstEventCursor *int64   `json:"first_event_cursor,omitempty"`
	LastEventCursor  *int64   `json:"last_event_cursor,omitempty"`
	StartedAt        string   `json:"started_at"`
	LastEventAt      string   `json:"last_event_at"`
	AcknowledgedAt   string   `json:"acknowledged_at,omitempty"`
	ResolvedAt       string   `json:"resolved_at,omitempty"`
	MutedAt          string   `json:"muted_at,omitempty"`
	RootCause        string   `json:"root_cause,omitempty"`
	Resolution       string   `json:"resolution,omitempty"`
	Notes            string   `json:"notes,omitempty"`
}

// SnapshotSession represents a session in the snapshot
type SnapshotSession struct {
	Name     string          `json:"name"`
	Attached bool            `json:"attached"`
	Agents   []SnapshotAgent `json:"agents"`
	// Services lists the session's tagged non-agent service panes. They are
	// excluded from every agent count, state summary and health rollup
	// (ntm#305) and reported here so they stay visible.
	Services []SnapshotService `json:"services,omitempty"`
}

// SnapshotService is a long-lived, non-agent service pane — an ACFS-managed
// `cm` or `cass` process, say — identified by its durable pane-option tag
// rather than by its title. A service's terminal going quiet is normal and
// must never be reported as an agent error (ntm#305).
type SnapshotService struct {
	Pane    string `json:"pane"`
	Name    string `json:"name"`
	Manager string `json:"manager"`
	Command string `json:"command,omitempty"`
}

// snapshotServiceFromPane renders a tagged service pane for robot output.
func snapshotServiceFromPane(pane tmux.Pane) SnapshotService {
	return SnapshotService{
		Pane:    fmt.Sprintf("%d.%d", pane.WindowIndex, pane.Index),
		Name:    pane.Service,
		Manager: pane.ServiceManager,
		Command: pane.Command,
	}
}

// SnapshotAgent represents an agent in the snapshot
type SnapshotAgent struct {
	Pane             string   `json:"pane"`
	Type             string   `json:"type"`              // claude, codex, antigravity, grok, gemini, or another supported type
	Variant          string   `json:"variant,omitempty"` // Model alias or persona name
	TypeConfidence   float64  `json:"type_confidence"`
	TypeMethod       string   `json:"type_method"`
	State            string   `json:"state"`
	LastOutputAgeSec int      `json:"last_output_age_sec"`
	OutputTailLines  int      `json:"output_tail_lines"`
	ContextPercent   *float64 `json:"context_percent,omitempty"`
	CurrentBead      *string  `json:"current_bead,omitempty"`
	PendingMail      int      `json:"pending_mail"`
}

// SnapshotAgentMail represents Agent Mail availability and inbox state.
type SnapshotAgentMail struct {
	Available    bool                              `json:"available"`
	Reason       string                            `json:"reason,omitempty"`
	Project      string                            `json:"project,omitempty"`
	TotalUnread  int                               `json:"total_unread,omitempty"`
	Agents       map[string]SnapshotAgentMailStats `json:"agents,omitempty"`
	ThreadsKnown int                               `json:"threads_known,omitempty"`
}

// SnapshotAgentMailStats holds per-agent inbox counts.
type SnapshotAgentMailStats struct {
	Pane       string `json:"pane,omitempty"`
	Unread     int    `json:"unread"`
	PendingAck int    `json:"pending_ack"`
}

// SnapshotCoordinationSummary is the compact projection-backed coordination
// section surfaced by snapshot. It intentionally summarizes the normalized
// runtime coordination model rather than duplicating full Agent Mail details.
type SnapshotCoordinationSummary struct {
	Available      bool   `json:"available"`
	MailUnread     int    `json:"mail_unread"`
	MailUrgent     int    `json:"mail_urgent"`
	PendingAck     int    `json:"pending_ack"`
	AgentsWithMail int    `json:"agents_with_mail,omitempty"`
	HasHandoff     bool   `json:"has_handoff"`
	HandoffSession string `json:"handoff_session,omitempty"`
	HandoffStatus  string `json:"handoff_status,omitempty"`
}

type SwarmSnapshot struct {
	Active       bool               `json:"active"`
	Plan         SwarmSnapshotPlan  `json:"plan"`
	Sessions     []SwarmSessionInfo `json:"sessions"`
	Health       SwarmHealthSummary `json:"health"`
	RecentEvents []SwarmRecentEvent `json:"recent_events"`
}

type SwarmSnapshotPlan struct {
	CreatedAt   string                `json:"created_at"`
	ScanDir     string                `json:"scan_dir"`
	TotalAgents int                   `json:"total_agents"`
	Allocations []SwarmPlanAllocation `json:"allocations"`
}

type SwarmPlanAllocation struct {
	Project   string `json:"project"`
	Tier      int    `json:"tier"`
	OpenBeads int    `json:"open_beads"`
	CCAgents  int    `json:"cc_agents"`
	CodAgents int    `json:"cod_agents"`
	GmiAgents int    `json:"gmi_agents"`
	AgyAgents int    `json:"agy_agents"`
}

type SwarmSessionInfo struct {
	Name      string `json:"name"`
	AgentType string `json:"agent_type"`
	PaneCount int    `json:"pane_count"`
	Healthy   int    `json:"healthy"`
	LimitHit  int    `json:"limit_hit"`
}

type SwarmHealthSummary struct {
	TotalAgents int `json:"total_agents"`
	Healthy     int `json:"healthy"`
	LimitHit    int `json:"limit_hit"`
	Respawning  int `json:"respawning"`
}

type SwarmRecentEvent struct {
	Type      string `json:"type"`
	Pane      string `json:"pane"`
	Timestamp string `json:"timestamp"`
}

// BeadLimit controls how many ready/in-progress beads to include in snapshot
var BeadLimit = 5

// GetSnapshot retrieves complete system state for AI orchestration.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetSnapshot(cfg *config.Config) (*SnapshotOutput, error) {
	return GetSnapshotWithOptions(cfg, PaginationOptions{})
}

// GetSnapshotWithOptions retrieves complete system state with pagination applied to sessions.
func GetSnapshotWithOptions(cfg *config.Config, opts PaginationOptions) (*SnapshotOutput, error) {
	if cfg == nil {
		cfg = config.Default()
	}
	output := newSnapshotOutput(cfg)
	attachSnapshotResourcePressure(output)
	// Disk trajectory must attach before either snapshot path (projection or
	// fallback) drains alerts.GetActiveAlerts, so a within-horizon projection
	// surfaces in this same snapshot's alert sections.
	attachSnapshotDisk(output, cfg)

	feed := GetAttentionFeed()
	populateSnapshotFeedMetadata(output, feed)

	// Check tmux availability
	if !tmux.IsInstalled() {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("tmux is not installed"),
			ErrCodeDependencyMissing,
			"Install tmux to enable snapshot",
		)
		output.Alerts = append(output.Alerts, "tmux is not installed")
		return output, nil
	}

	// Fetch Agent Mail data early
	var mailAgents []agentmail.Agent
	var mailStats map[string]SnapshotAgentMailStats
	var projectKey string

	cwd, err := os.Getwd()
	if err == nil {
		if root, err := git.FindProjectRoot(cwd); err == nil {
			projectKey = root
		} else {
			projectKey = cwd
		}
		// Build initial mail summary without pane mapping
		if summary, agents, stats := fetchAgentMailData(projectKey); summary != nil {
			output.AgentMail = summary
			output.MailUnread = summary.TotalUnread
			mailAgents = agents
			mailStats = stats
		}
	}

	// Get all sessions
	sessions, err := tmux.ListSessions()
	if err != nil {
		// ListSessions returns an empty result without an error when no tmux
		// server is running. A non-nil error means the control plane could not
		// enumerate sessions, so a successful empty snapshot would be false.
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("list tmux sessions: %w", err),
			ErrCodeInternalError,
			"Check tmux is running and accessible, then retry the snapshot",
		)
		output.Alerts = append(output.Alerts, "failed to list tmux sessions: "+err.Error())
		return output, nil
	}

	store := opts.resolveStore()
	if store != nil {
		projectionOutput, err := buildProjectionBackedSnapshot(store, cfg, opts, output, sessions, projectKey)
		if err == nil {
			return projectionOutput, nil
		}
		if errors.Is(err, errProjectionStaleEmpty) {
			// Surface the degradation instead of hiding it: the live loop
			// below is authoritative, but callers should know the projection
			// aged out (freshness contract: fail-visible).
			output.Alerts = append(output.Alerts, "runtime projection stale (no fresh session rows); served live tmux enumeration")
		}
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

		// Resolve agent mapping for this session
		agentMapping := resolveAgentsForSession(panes, mailAgents)

		// Ground-truth transcript usage, unambiguous panes only (memoized
		// per agent-type+cwd group).
		paneTranscripts := ResolvePaneTranscripts(panes)

		for _, pane := range panes {
			// A tagged service pane is not an agent: keep it out of the agent
			// list (and therefore out of every count derived from it) but
			// report it alongside (ntm#305).
			if pane.IsServicePane() {
				snapSession.Services = append(snapSession.Services, snapshotServiceFromPane(pane))
				continue
			}

			// Capture output for state detection and enhanced type detection
			captured := ""
			capturedErr := error(nil)
			captured, capturedErr = tmux.CapturePaneOutput(pane.ID, 50)

			// Use enhanced agent type detection
			detection := DetectAgentTypeEnhanced(pane, captured)

			agent := SnapshotAgent{
				// Emit the pane's real window index (#172) so the W.P address
				// round-trips on multi-window / window-per-agent layouts;
				// hardcoding window 0 misaddressed every pane outside window 0.
				Pane:             fmt.Sprintf("%d.%d", pane.WindowIndex, pane.Index),
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

			// Map pending mail if available
			if agentName, ok := agentMapping[pane.ID]; ok {
				if stats, ok := mailStats[agentName]; ok {
					agent.PendingMail = stats.Unread

					// Update the mail summary with the pane ID
					if output.AgentMail != nil && output.AgentMail.Agents != nil {
						if s, exists := output.AgentMail.Agents[agentName]; exists {
							s.Pane = agent.Pane
							output.AgentMail.Agents[agentName] = s
						}
					}
				}
			}

			// Process captured output for state
			if capturedErr == nil {
				lines := splitLines(status.StripANSI(captured))
				agent.OutputTailLines = len(lines)
				agent.State = determineState(captured, agent.Type)
				measured := Agent{Type: agent.Type}
				enrichAgentContext(&measured, contextPaneModel(pane, agent.Type), captured, paneTranscripts[pane.ID])
				agent.ContextPercent = contextPercentForAgent(&measured)
			}

			snapSession.Agents = append(snapSession.Agents, agent)
		}

		output.Sessions = append(output.Sessions, snapSession)
	}

	output.Swarm = buildSwarmSnapshot(cfg, sessions)

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

	// Include tool inventory and health status
	toolCtx, toolCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer toolCancel()
	output.Tools = GetToolsSummary(toolCtx, DisabledTools(cfg))

	// Generate and add detailed alerts using the alerts package
	alertCfg := AlertConfigForProject(cfg, projectKey)
	activeAlerts := alerts.GetActiveAlerts(alertCfg)

	if len(activeAlerts) > 0 {
		output.AlertsDetailed = make([]AlertInfo, len(activeAlerts))
		for i, a := range activeAlerts {
			output.AlertsDetailed[i] = AlertInfo{
				ID:         a.ID,
				Source:     a.Source,
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

	if incidents, err := snapshotIncidentsFromStore(store); err == nil {
		output.ActiveIncidents = incidents
	} else {
		output.Alerts = append(output.Alerts, fmt.Sprintf("failed to list active incidents: %v", err))
	}
	if store != nil {
		output.Work = verifiedRobotWork(store, projectKey, BeadLimit)
		if rows, err := store.GetAllSourceHealth(); err == nil {
			applySnapshotSourceHealth(output, rows)
		}
		handoffRow, handoffErr := store.GetRuntimeHandoff()
		if handoffErr == nil {
			output.Handoff = statusHandoffFromRuntime(handoffRow)
		}
		if coordinationRows, err := store.ListFreshRuntimeCoordination(""); err == nil {
			output.Coordination = snapshotCoordinationFromRuntime(coordinationRows, handoffRow)
		}
		if quotaRows, err := store.ListFreshRuntimeQuota(""); err == nil {
			output.Quota = snapshotQuotaFromRuntime(quotaRows)
		}
	}
	snapshotFinalize(output, opts)
	return output, nil
}

func buildProjectionBackedSnapshot(
	store *state.Store,
	cfg *config.Config,
	opts PaginationOptions,
	base *SnapshotOutput,
	tmuxSessions []tmux.Session,
	projectKey string,
) (*SnapshotOutput, error) {
	if store == nil {
		return nil, fmt.Errorf("snapshot projection store unavailable")
	}

	output := cloneSnapshotOutput(base)
	sessions, err := buildProjectionBackedSnapshotSessions(store, tmuxSessions)
	if err != nil {
		return nil, err
	}
	output.Sessions = sessions
	output.Work = verifiedRobotWork(store, projectKey, BeadLimit)
	if rows, err := store.GetAllSourceHealth(); err == nil {
		applySnapshotSourceHealth(output, rows)
	}
	handoffRow, handoffErr := store.GetRuntimeHandoff()
	if handoffErr == nil {
		output.Handoff = statusHandoffFromRuntime(handoffRow)
	}
	if coordinationRows, err := store.ListFreshRuntimeCoordination(""); err == nil {
		output.Coordination = snapshotCoordinationFromRuntime(coordinationRows, handoffRow)
	}
	if quotaRows, err := store.ListFreshRuntimeQuota(""); err == nil {
		output.Quota = snapshotQuotaFromRuntime(quotaRows)
	}
	if output.AgentMail != nil && len(output.AgentMail.Agents) > 0 {
		if paneMap, err := buildProjectionAgentMailPaneMap(store, tmuxSessions); err == nil {
			for agentName, pane := range paneMap {
				stats, ok := output.AgentMail.Agents[agentName]
				if !ok {
					continue
				}
				stats.Pane = pane
				output.AgentMail.Agents[agentName] = stats
			}
		}
	}

	for _, sess := range output.Sessions {
		for _, agent := range sess.Agents {
			if agent.State == "error" {
				output.Alerts = append(output.Alerts, fmt.Sprintf("agent %s in %s has error state", agent.Pane, sess.Name))
			}
		}
	}

	output.Swarm = buildSwarmSnapshot(cfg, tmuxSessions)

	output.BeadsSummary = beadsSummaryFromWork(output.Work, projectKey, BeadLimit)

	toolCtx, toolCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer toolCancel()
	output.Tools = GetToolsSummary(toolCtx, DisabledTools(cfg))

	alertCfg := AlertConfigForProject(cfg, projectKey)
	activeAlerts := alerts.GetActiveAlerts(alertCfg)
	if len(activeAlerts) > 0 {
		output.AlertsDetailed = make([]AlertInfo, len(activeAlerts))
		for i, a := range activeAlerts {
			output.AlertsDetailed[i] = AlertInfo{
				ID:         a.ID,
				Source:     a.Source,
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
		for _, a := range activeAlerts {
			msg := a.Message
			if a.Session != "" {
				msg = a.Session + ": " + msg
			}
			output.Alerts = append(output.Alerts, msg)
		}

		tracker := alerts.GetGlobalTracker()
		summary := tracker.Summary()
		output.AlertSummary = &AlertSummaryInfo{
			TotalActive: summary.TotalActive,
			BySeverity:  summary.BySeverity,
			ByType:      summary.ByType,
		}
	}

	incidents, err := snapshotIncidentsFromStore(store)
	if err != nil {
		output.Alerts = append(output.Alerts, fmt.Sprintf("failed to list active incidents: %v", err))
	} else {
		output.ActiveIncidents = incidents
	}
	snapshotFinalize(output, opts)
	return output, nil
}

func applySnapshotSourceHealth(output *SnapshotOutput, rows []state.SourceHealth) {
	if output == nil {
		return
	}
	output.Sources = statusSourceHealthFromRows(rows)
	if output.Sources == nil {
		output.DegradedSources = nil
		return
	}
	output.DegradedSources = append([]string(nil), output.Sources.Degraded...)
}

func snapshotFinalize(output *SnapshotOutput, opts PaginationOptions) {
	if output == nil {
		return
	}

	output.DegradedSources = uniqueSortedStrings(output.DegradedSources)
	output.Summary = buildSnapshotSummary(output)
	if output.MailUnread == 0 && output.AgentMail != nil {
		output.MailUnread = output.AgentMail.TotalUnread
	}

	if paged, page := ApplyPagination(output.Sessions, opts); page != nil {
		output.Sessions = paged
		output.Pagination = page
		if next, pages := paginationHintOffsets(page); next != nil {
			output.AgentHints = &AgentHints{
				NextOffset:     next,
				PagesRemaining: pages,
			}
		}
	}
}

func buildSnapshotSummary(output *SnapshotOutput) StatusSummary {
	summary := StatusSummary{
		AgentsByState: map[string]int{},
		AgentsByType:  map[string]int{},
	}
	if output == nil {
		return summary
	}

	for _, sess := range output.Sessions {
		summary.TotalSessions++
		if sess.Attached {
			summary.AttachedCount++
		}
		summary.TotalAgents += len(sess.Agents)
		for _, agent := range sess.Agents {
			statusAccumulateAgentSummary(&summary, agent.Type, agent.State)
		}
	}

	if output.Work != nil && output.Work.Summary != nil {
		summary.ReadyWork = output.Work.Summary.Ready
		summary.InProgress = output.Work.Summary.InProgress
	} else if output.BeadsSummary != nil {
		summary.ReadyWork = output.BeadsSummary.Ready
		summary.InProgress = output.BeadsSummary.InProgress
	}
	if output.Coordination != nil {
		summary.MailUnread = output.Coordination.MailUnread
		summary.MailUrgent = output.Coordination.MailUrgent
	} else if output.AgentMail != nil {
		summary.MailUnread = output.AgentMail.TotalUnread
	} else if output.MailUnread > 0 {
		summary.MailUnread = output.MailUnread
	}

	alertCounts := snapshotAlertCounts(output)
	summary.AlertsActive = statusAlertTotal(alertCounts)
	summary.HealthStatus = snapshotOverallStatus(output, alertCounts)
	summary.HealthScore = snapshotHealthScore(output, alertCounts)
	return summary
}

func snapshotAlertCounts(output *SnapshotOutput) map[string]int {
	counts := map[string]int{}
	if output == nil {
		return counts
	}
	if output.AlertSummary != nil && len(output.AlertSummary.BySeverity) > 0 {
		for key, value := range output.AlertSummary.BySeverity {
			counts[strings.TrimSpace(key)] += value
		}
		return counts
	}
	if len(output.AlertsDetailed) > 0 {
		for _, alert := range output.AlertsDetailed {
			key := strings.TrimSpace(alert.Severity)
			if key == "" {
				key = "warning"
			}
			counts[key]++
		}
		return counts
	}
	if len(output.Alerts) > 0 {
		counts["warning"] = len(output.Alerts)
	}
	return counts
}

func snapshotOverallStatus(output *SnapshotOutput, alertCounts map[string]int) string {
	if output == nil {
		return "unknown"
	}
	if alertCounts["critical"] > 0 || snapshotAgentStateCount(output, "error") > 0 {
		return "critical"
	}
	if len(output.DegradedSources) > 0 || alertCounts["error"] > 0 || alertCounts["warning"] > 0 {
		return "degraded"
	}
	return "healthy"
}

func snapshotHealthScore(output *SnapshotOutput, alertCounts map[string]int) float64 {
	score := 1.0
	if output == nil {
		return 0
	}
	score -= float64(len(output.DegradedSources)) * 0.1
	score -= float64(alertCounts["warning"]) * 0.05
	score -= float64(alertCounts["error"]) * 0.1
	score -= float64(alertCounts["critical"]) * 0.2
	score -= float64(snapshotAgentStateCount(output, "error")) * 0.1
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	return float64(int(score*100+0.5)) / 100
}

func snapshotAgentStateCount(output *SnapshotOutput, state string) int {
	if output == nil {
		return 0
	}
	target := strings.TrimSpace(state)
	count := 0
	for _, sess := range output.Sessions {
		for _, agent := range sess.Agents {
			if strings.TrimSpace(agent.State) == target {
				count++
			}
		}
	}
	return count
}

func snapshotCoordinationFromRuntime(rows []state.RuntimeCoordination, handoff *state.RuntimeHandoff) *SnapshotCoordinationSummary {
	summary := &SnapshotCoordinationSummary{
		Available: len(rows) > 0 || handoff != nil,
	}
	for _, row := range rows {
		summary.MailUnread += row.UnreadCount
		summary.MailUrgent += row.UrgentCount
		summary.PendingAck += row.PendingAckCount
		if row.UnreadCount > 0 || row.UrgentCount > 0 || row.PendingAckCount > 0 {
			summary.AgentsWithMail++
		}
	}
	if handoff != nil {
		summary.HasHandoff = true
		summary.HandoffSession = strings.TrimSpace(handoff.SessionName)
		summary.HandoffStatus = strings.TrimSpace(handoff.Status)
	}
	return summary
}

// robotWorkTimeout bounds the verified work read behind a snapshot or status.
const robotWorkTimeout = 10 * time.Second

// collectRobotWork is the verified work read behind verifiedRobotWork. It is a
// seam so tests never read the tracker of the repository they run in.
var collectRobotWork = adapters.CollectDurableWork

// verifiedRobotWork is the project's work as the robot snapshot and status
// report it: only a source-verified observation (adapters.CollectDurableWork),
// never the RuntimeWork rows, which are fresh by age alone, carry no source
// identity, and so could advertise work the tracker no longer has as ready
// (GH #283).
//
// The projection refresh that precedes every robot command publishes its own
// verified collection, so this is normally a re-verified read of that
// observation. If the tracker or checkout has moved on since, one new verified
// observation is taken: the old one is stale, not wrong to replace. Anything
// still unverifiable is reported unavailable with its reason and no ready
// work, never filled in from rows.
func verifiedRobotWork(store *state.Store, projectDir string, limit int) *adapters.WorkSection {
	ctx, cancel := context.WithTimeout(context.Background(), robotWorkTimeout)
	defer cancel()

	var work *adapters.WorkSection
	err := errors.New("work projection store unavailable")
	if store != nil {
		cfg := adapters.DefaultWorkCoordinationAdapterConfig(projectDir)
		if limit > 0 {
			cfg.WorkItemLimit = limit
		}
		work, err = collectRobotWork(ctx, store, cfg, false)
		if err != nil && errors.Is(err, worksource.ErrStale) && ctx.Err() == nil {
			work, err = collectRobotWork(ctx, store, cfg, true)
		}
	}
	if work == nil {
		work = &adapters.WorkSection{}
	}
	if err != nil {
		work.Available = false
		work.Ready = nil
		if work.Summary != nil {
			work.Summary.Ready = 0
		}
		if work.Reason == "" {
			work.Reason = err.Error()
		}
	}
	// Critical arrays are always present, empty when there is nothing.
	if work.Ready == nil {
		work.Ready = []adapters.WorkItem{}
	}
	if work.Blocked == nil {
		work.Blocked = []adapters.WorkItem{}
	}
	if work.InProgress == nil {
		work.InProgress = []adapters.WorkItem{}
	}
	if work.Summary == nil {
		work.Summary = &adapters.WorkSummary{}
	}
	return work
}

func snapshotWorkItemFromRuntime(row state.RuntimeWork) adapters.WorkItem {
	item := adapters.WorkItem{
		ID:              strings.TrimSpace(row.BeadID),
		Title:           robotFirstNonEmpty(strings.TrimSpace(row.Title), strings.TrimSpace(row.BeadID)),
		TitleDisclosure: decodeDisclosureMetadata(row.TitleDisclosure),
		Priority:        row.Priority,
		Type:            strings.TrimSpace(row.BeadType),
		Labels:          decodeStringList(row.Labels),
		Assignee:        strings.TrimSpace(row.Assignee),
		Unblocks:        row.UnblocksCount,
	}
	if row.Score != nil {
		score := *row.Score
		item.Score = &score
	}
	if row.ClaimedAt != nil && !row.ClaimedAt.IsZero() {
		item.UpdatedAt = row.ClaimedAt.UTC().Format(time.RFC3339)
	}
	return item
}

func snapshotQuotaFromRuntime(rows []state.RuntimeQuota) *adapters.QuotaSection {
	section := &adapters.QuotaSection{
		Accounts:  []adapters.AccountQuota{},
		Available: len(rows) > 0,
	}
	if len(rows) == 0 {
		section.Reason = "runtime projection empty"
		return section
	}

	for _, row := range rows {
		displayProvider := canonicalRobotProvider(row.Provider)
		usagePercent := row.UsedPct
		account := adapters.AccountQuota{
			ID:         robotFirstNonEmpty(strings.TrimSpace(row.Account), displayProvider),
			Provider:   displayProvider,
			Status:     snapshotQuotaStatus(row),
			ReasonCode: snapshotQuotaReasonCode(row),
			IsActive:   row.IsActive,
			IsPrimary:  row.IsActive,
		}
		if row.UsedPctKnown {
			account.UsagePercent = &usagePercent
		}
		if row.ResetsAt != nil && !row.ResetsAt.IsZero() {
			account.ResetAt = row.ResetsAt.UTC().Format(time.RFC3339)
		}
		section.Accounts = append(section.Accounts, account)
	}
	section.Summary = snapshotQuotaSummary(section.Accounts)
	return section
}

func snapshotQuotaReasonCode(row state.RuntimeQuota) adapters.ReasonCode {
	switch {
	case row.LimitHit:
		if row.UsedPctSource == state.RuntimeQuotaUsedPctSourceRequests {
			return adapters.ReasonQuotaExceededRequests
		}
		return adapters.ReasonQuotaExceededTokens
	case row.UsedPctKnown && row.UsedPct >= 95:
		if row.UsedPctSource == state.RuntimeQuotaUsedPctSourceRequests {
			return adapters.ReasonQuotaCriticalRequests
		}
		return adapters.ReasonQuotaCriticalTokens
	case row.UsedPctKnown && row.UsedPct >= 80:
		if row.UsedPctSource == state.RuntimeQuotaUsedPctSourceRequests {
			return adapters.ReasonQuotaWarningRequests
		}
		return adapters.ReasonQuotaWarningTokens
	case !row.Healthy:
		return adapters.ReasonQuotaUnavailable
	default:
		return adapters.ReasonQuotaOK
	}
}

func snapshotQuotaStatus(row state.RuntimeQuota) string {
	switch snapshotQuotaReasonCode(row) {
	case adapters.ReasonQuotaExceededTokens, adapters.ReasonQuotaExceededRequests,
		adapters.ReasonQuotaExceededCost, adapters.ReasonQuotaSuspended:
		return "exceeded"
	case adapters.ReasonQuotaCriticalTokens, adapters.ReasonQuotaCriticalRequests,
		adapters.ReasonQuotaWarningTokens, adapters.ReasonQuotaWarningRequests,
		adapters.ReasonQuotaWarningRateLimit, adapters.ReasonQuotaUnavailable:
		return "warning"
	default:
		return "ok"
	}
}

func snapshotQuotaSummary(accounts []adapters.AccountQuota) *adapters.QuotaSummary {
	summary := &adapters.QuotaSummary{
		TotalAccounts: len(accounts),
	}
	for _, account := range accounts {
		switch account.Status {
		case "ok":
			summary.HealthyAccounts++
		case "warning", "critical":
			summary.WarningAccounts++
		case "exceeded":
			summary.ExceededAccounts++
		}
	}
	return summary
}

func buildProjectionBackedSnapshotSessions(store *state.Store, tmuxSessions []tmux.Session) ([]SnapshotSession, error) {
	rows, err := store.GetFreshRuntimeSessions()
	if err != nil {
		return nil, fmt.Errorf("snapshot sessions: %w", err)
	}
	if len(rows) == 0 && len(tmuxSessions) > 0 {
		// The caller already holds live tmux sessions; an empty projection
		// here means every runtime_sessions row aged past stale_after, not
		// that the fleet is gone. Refuse to answer so the caller serves the
		// live enumeration instead of a false empty snapshot.
		return nil, errProjectionStaleEmpty
	}

	agentsBySession := make(map[string][]state.RuntimeAgent, len(rows))
	for _, sess := range rows {
		agents, err := store.GetRuntimeAgentsBySession(sess.Name)
		if err != nil {
			return nil, fmt.Errorf("snapshot agents for %s: %w", sess.Name, err)
		}
		agentsBySession[sess.Name] = agents
	}

	paneLabels, paneServices := snapshotPaneIndex(tmuxSessions)
	output := make([]SnapshotSession, 0, len(rows))
	for _, sess := range rows {
		item := SnapshotSession{
			Name:     sess.Name,
			Attached: sess.Attached,
			Agents:   make([]SnapshotAgent, 0, len(agentsBySession[sess.Name])),
			Services: paneServices[sess.Name],
		}
		for _, agent := range agentsBySession[sess.Name] {
			snapshotAgent := snapshotAgentFromRuntime(sess.Name, agent, paneLabels)
			item.Agents = append(item.Agents, snapshotAgent)
		}
		output = append(output, item)
	}

	return output, nil
}

// beadsSummaryFromWork renders the snapshot's beads summary from the same
// verified work section the snapshot reports, so the two can never disagree
// and neither is derived from unverified RuntimeWork rows (GH #283).
func beadsSummaryFromWork(work *adapters.WorkSection, projectKey string, limit int) *bv.BeadsSummary {
	summary := &bv.BeadsSummary{
		Project:        strings.TrimSpace(projectKey),
		ReadyPreview:   []bv.BeadPreview{},
		InProgressList: []bv.BeadInProgress{},
	}
	if work == nil || !work.Available {
		summary.Reason = "work source unverified"
		if work != nil && work.Reason != "" {
			summary.Reason = work.Reason
		}
		return summary
	}
	summary.Available = true
	if s := work.Summary; s != nil {
		summary.Total, summary.Open, summary.InProgress = s.Total, s.Open, s.InProgress
		summary.Blocked, summary.Ready, summary.Closed = s.Blocked, s.Ready, s.Closed
	}
	for _, item := range work.Ready {
		if limit > 0 && len(summary.ReadyPreview) >= limit {
			break
		}
		summary.ReadyPreview = append(summary.ReadyPreview, bv.BeadPreview{
			ID:       item.ID,
			Title:    item.Title,
			Priority: fmt.Sprintf("P%d", item.Priority),
			Type:     item.Type,
		})
	}
	for _, item := range work.InProgress {
		if limit > 0 && len(summary.InProgressList) >= limit {
			break
		}
		entry := bv.BeadInProgress{ID: item.ID, Title: item.Title, Assignee: item.Assignee}
		if updated, err := time.Parse(time.RFC3339, item.UpdatedAt); err == nil {
			entry.UpdatedAt = updated
		}
		summary.InProgressList = append(summary.InProgressList, entry)
	}
	return summary
}

func buildProjectionAgentMailPaneMap(store *state.Store, tmuxSessions []tmux.Session) (map[string]string, error) {
	if store == nil {
		return map[string]string{}, nil
	}

	sessions, err := store.GetFreshRuntimeSessions()
	if err != nil {
		return nil, fmt.Errorf("snapshot mail sessions: %w", err)
	}

	paneLabels := snapshotPaneLabelMap(tmuxSessions)
	output := make(map[string]string)
	for _, sess := range sessions {
		agents, err := store.GetRuntimeAgentsBySession(sess.Name)
		if err != nil {
			return nil, fmt.Errorf("snapshot mail agents for %s: %w", sess.Name, err)
		}
		for _, agent := range agents {
			agentName := strings.TrimSpace(agent.AgentMailName)
			if agentName == "" {
				continue
			}
			paneKey := fmt.Sprintf("%s:%s", sess.Name, strings.TrimSpace(agent.Pane))
			paneRef := strings.TrimSpace(agent.Pane)
			if label, ok := paneLabels[paneKey]; ok && strings.TrimSpace(label) != "" {
				paneRef = label
			}
			output[agentName] = paneRef
		}
	}

	return output, nil
}

func snapshotPaneLabelMap(tmuxSessions []tmux.Session) map[string]string {
	labels, _ := snapshotPaneIndex(tmuxSessions)
	return labels
}

// snapshotPaneIndex walks the live panes of each session once and returns both
// the pane-label map (session:pane_id -> "W.P") and the session's tagged
// non-agent service panes (ntm#305), so the projection-backed snapshot can
// report services without a second tmux enumeration.
func snapshotPaneIndex(tmuxSessions []tmux.Session) (map[string]string, map[string][]SnapshotService) {
	labels := make(map[string]string)
	services := make(map[string][]SnapshotService)
	for _, sess := range tmuxSessions {
		panes := append([]tmux.Pane(nil), sess.Panes...)
		if len(panes) == 0 {
			if fetched, err := tmux.GetPanes(sess.Name); err == nil {
				panes = fetched
			}
		}
		for _, pane := range panes {
			key := fmt.Sprintf("%s:%s", sess.Name, strings.TrimSpace(pane.ID))
			labels[key] = fmt.Sprintf("%d.%d", pane.WindowIndex, pane.Index)
			if pane.IsServicePane() {
				services[sess.Name] = append(services[sess.Name], snapshotServiceFromPane(pane))
			}
		}
	}
	return labels, services
}

func snapshotAgentFromRuntime(sessionName string, row state.RuntimeAgent, paneLabels map[string]string) SnapshotAgent {
	paneKey := fmt.Sprintf("%s:%s", sessionName, strings.TrimSpace(row.Pane))
	paneRef := strings.TrimSpace(row.Pane)
	if label, ok := paneLabels[paneKey]; ok && strings.TrimSpace(label) != "" {
		paneRef = label
	}

	item := SnapshotAgent{
		Pane:             paneRef,
		Type:             strings.TrimSpace(row.AgentType),
		Variant:          strings.TrimSpace(row.Variant),
		TypeConfidence:   row.TypeConfidence,
		TypeMethod:       strings.TrimSpace(row.TypeMethod),
		State:            strings.TrimSpace(string(row.State)),
		LastOutputAgeSec: row.LastOutputAgeSec,
		OutputTailLines:  row.OutputTailLines,
		ContextPercent:   row.ContextPercent,
		PendingMail:      row.PendingMail,
	}
	if bead := strings.TrimSpace(row.CurrentBead); bead != "" {
		item.CurrentBead = &bead
	}
	return item
}

// buildSnapshotAttentionSummary creates a compact attention orientation from
// the current feed state. This helps operators choose the next command without
// reading the full snapshot. Uses the same signal taxonomy as digest/events.
func buildSnapshotAttentionSummary(feed *AttentionFeed) *SnapshotAttentionSummary {
	if feed == nil {
		return nil
	}

	stats := feed.Stats()
	unsupported := make([]string, 0, len(UnsupportedConditions()))
	for _, uc := range UnsupportedConditions() {
		unsupported = append(unsupported, uc.Name)
	}

	if stats.Count == 0 {
		return &SnapshotAttentionSummary{
			TotalEvents:        0,
			ByCategoryCount:    map[string]int{},
			UnsupportedSignals: unsupported,
			NextSteps: []NextAction{
				attentionStatusNextAction("Inspect the current robot state while the attention feed is still empty"),
				{Action: "robot-snapshot", Args: "--robot-snapshot", Reason: "Refresh the bootstrap view after agents start emitting replayable attention events"},
			},
		}
	}

	replaySince := stats.OldestCursor - 1
	if replaySince < 0 {
		replaySince = 0
	}
	events, _, err := feed.Replay(replaySince, stats.Count)

	summary := &SnapshotAttentionSummary{
		TotalEvents:        stats.Count,
		ByCategoryCount:    map[string]int{},
		TopItems:           []SnapshotAttentionItem{},
		UnsupportedSignals: unsupported,
		NextSteps:          []NextAction{},
	}
	if err != nil {
		summary.NextSteps = append(summary.NextSteps, NextAction{
			Action: "robot-snapshot",
			Args:   "--robot-snapshot",
			Reason: "Replay metadata changed while building the snapshot; fetch a fresh snapshot before following cursors",
		})
		return summary
	}

	topItems := make([]SnapshotAttentionItem, 0, 3)
	actionRequiredEvents := make([]AttentionEvent, 0, len(events))
	interestingEvents := make([]AttentionEvent, 0, len(events))
	for _, ev := range events {
		cat := string(ev.Category)
		summary.ByCategoryCount[cat]++

		switch ev.Actionability {
		case ActionabilityActionRequired:
			summary.ActionRequiredCount++
			actionRequiredEvents = append(actionRequiredEvents, cloneAttentionEvent(ev))
			topItems = append(topItems, SnapshotAttentionItem{
				Cursor:        ev.Cursor,
				Category:      cat,
				Actionability: string(ev.Actionability),
				Severity:      string(ev.Severity),
				Summary:       ev.Summary,
			})
		case ActionabilityInteresting:
			summary.InterestingCount++
			interestingEvents = append(interestingEvents, cloneAttentionEvent(ev))
		}
	}

	// Keep only the 3 most recent action_required items
	if len(topItems) > 3 {
		topItems = topItems[len(topItems)-3:]
	}
	summary.TopItems = topItems

	for _, ev := range actionRequiredEvents {
		for _, action := range ev.NextActions {
			if len(summary.NextSteps) >= 3 {
				break
			}
			duplicate := false
			for _, existing := range summary.NextSteps {
				if existing.Action == action.Action && existing.Args == action.Args {
					duplicate = true
					break
				}
			}
			if !duplicate {
				summary.NextSteps = append(summary.NextSteps, action)
			}
		}
	}

	if len(summary.NextSteps) == 0 && len(actionRequiredEvents) > 0 {
		since := actionRequiredEvents[0].Cursor - 1
		if since < replaySince {
			since = replaySince
		}
		summary.NextSteps = append(summary.NextSteps, NextAction{
			Action: "robot-events",
			Args:   fmt.Sprintf("--robot-events --since-cursor=%d --events-limit=20", since),
			Reason: fmt.Sprintf("Inspect the %d surfaced action-required event(s) in the current replay window", len(summary.TopItems)),
		})
	}
	if len(summary.NextSteps) == 0 && len(interestingEvents) > 0 {
		summary.NextSteps = append(summary.NextSteps, NextAction{
			Action: "robot-digest",
			Args:   "--robot-digest",
			Reason: fmt.Sprintf("%d interesting event(s) are present; use the digest for the steady-state delta summary", summary.InterestingCount),
		})
	}
	if len(summary.NextSteps) < 3 && stats.NewestCursor > 0 {
		summary.NextSteps = append(summary.NextSteps, NextAction{
			Action: "robot-events",
			Args:   fmt.Sprintf("--robot-events --since-cursor=%d --events-limit=20", stats.NewestCursor),
			Reason: "Continue following new attention events from the snapshot cursor",
		})
	}
	if len(summary.NextSteps) < 3 {
		summary.NextSteps = append(summary.NextSteps, NextAction{
			Action: "robot-snapshot",
			Args:   "--robot-snapshot",
			Reason: "Resync if a saved cursor falls outside the current replay window",
		})
	}

	return summary
}

func snapshotIncidentsFromStore(store *state.Store) ([]SnapshotIncident, error) {
	if store == nil {
		return []SnapshotIncident{}, nil
	}

	rows, err := store.ListOpenIncidents()
	if err != nil {
		return nil, err
	}

	items := make([]SnapshotIncident, 0, len(rows))
	for _, row := range rows {
		items = append(items, snapshotIncidentFromState(row))
	}
	return items, nil
}

func snapshotIncidentFromState(row state.Incident) SnapshotIncident {
	sessionNames := decodeStringList(row.SessionNames)
	if sessionNames == nil {
		sessionNames = []string{}
	}
	agentIDs := decodeStringList(row.AgentIDs)
	if agentIDs == nil {
		agentIDs = []string{}
	}

	item := SnapshotIncident{
		ID:           strings.TrimSpace(row.ID),
		Title:        strings.TrimSpace(row.Title),
		Family:       strings.TrimSpace(row.Family),
		Category:     strings.TrimSpace(row.Category),
		Status:       strings.TrimSpace(string(row.Status)),
		Severity:     strings.TrimSpace(string(row.Severity)),
		SessionNames: sessionNames,
		AgentIDs:     agentIDs,
		AlertCount:   row.AlertCount,
		EventCount:   row.EventCount,
		StartedAt:    FormatTimestamp(row.StartedAt),
		LastEventAt:  FormatTimestamp(row.LastEventAt),
		RootCause:    strings.TrimSpace(row.RootCause),
		Resolution:   strings.TrimSpace(row.Resolution),
		Notes:        strings.TrimSpace(row.Notes),
	}
	if row.FirstEventCursor != nil {
		cursor := *row.FirstEventCursor
		item.FirstEventCursor = &cursor
	}
	if row.LastEventCursor != nil {
		cursor := *row.LastEventCursor
		item.LastEventCursor = &cursor
	}
	item.AcknowledgedAt = FormatTimestampPtr(row.AcknowledgedAt)
	item.ResolvedAt = FormatTimestampPtr(row.ResolvedAt)
	item.MutedAt = FormatTimestampPtr(row.MutedAt)
	return item
}

func buildSwarmSnapshot(cfg *config.Config, sessions []tmux.Session) *SwarmSnapshot {
	if cfg == nil || !cfg.Swarm.Enabled {
		return nil
	}

	type sessSpec struct {
		name      string
		agentType string
	}

	swarmSessions := make([]sessSpec, 0, len(sessions))
	for _, sess := range sessions {
		agentType, ok := parseSwarmSessionName(sess.Name)
		if !ok {
			continue
		}
		swarmSessions = append(swarmSessions, sessSpec{name: sess.Name, agentType: agentType})
	}
	if len(swarmSessions) == 0 {
		return nil
	}

	out := &SwarmSnapshot{
		Active:       true,
		Sessions:     make([]SwarmSessionInfo, 0, len(swarmSessions)),
		RecentEvents: []SwarmRecentEvent{},
	}

	totalAgents := 0
	totalHealthy := 0
	totalLimitHit := 0

	for _, sess := range swarmSessions {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		h, err := health.CheckSession(ctx, sess.name)
		cancel()
		if err != nil {
			continue
		}

		paneCount := 0
		healthy := 0
		limitHit := 0

		for _, agent := range h.Agents {
			// Swarm sessions are agent-only; ignore any user panes if present.
			if agent.AgentType == "user" {
				continue
			}
			paneCount++
			if agent.Status == health.StatusOK {
				healthy++
			}
			if agent.RateLimited {
				limitHit++
			}
		}

		totalAgents += paneCount
		totalHealthy += healthy
		totalLimitHit += limitHit

		out.Sessions = append(out.Sessions, SwarmSessionInfo{
			Name:      sess.name,
			AgentType: sess.agentType,
			PaneCount: paneCount,
			Healthy:   healthy,
			LimitHit:  limitHit,
		})
	}

	out.Health = SwarmHealthSummary{
		TotalAgents: totalAgents,
		Healthy:     totalHealthy,
		LimitHit:    totalLimitHit,
		Respawning:  0,
	}

	out.Plan = buildSwarmSnapshotPlan(cfg, totalAgents)

	return out
}

func buildSwarmSnapshotPlan(cfg *config.Config, fallbackTotalAgents int) SwarmSnapshotPlan {
	planOut := SwarmSnapshotPlan{
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		ScanDir:     cfg.Swarm.DefaultScanDir,
		TotalAgents: fallbackTotalAgents,
		Allocations: []SwarmPlanAllocation{},
	}

	if cfg.Swarm.DefaultScanDir == "" {
		return planOut
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	scanner := swarmlib.NewBeadScanner(cfg.Swarm.DefaultScanDir)
	scan, err := scanner.Scan(ctx)
	if err != nil || scan == nil {
		return planOut
	}

	calc := swarmlib.NewAllocationCalculator(&cfg.Swarm)
	plan := calc.GenerateSwarmPlan(cfg.Swarm.DefaultScanDir, scan.Projects)
	if plan == nil {
		return planOut
	}

	planOut.CreatedAt = plan.CreatedAt.UTC().Format(time.RFC3339)
	planOut.ScanDir = plan.ScanDir
	planOut.TotalAgents = plan.TotalAgents
	planOut.Allocations = make([]SwarmPlanAllocation, 0, len(plan.Allocations))

	for _, alloc := range plan.Allocations {
		planOut.Allocations = append(planOut.Allocations, SwarmPlanAllocation{
			Project:   alloc.Project.Name,
			Tier:      alloc.Project.Tier,
			OpenBeads: alloc.Project.OpenBeads,
			CCAgents:  alloc.CCAgents,
			CodAgents: alloc.CodAgents,
			GmiAgents: alloc.GmiAgents,
			AgyAgents: alloc.AgyAgents,
		})
	}

	return planOut
}

func parseSwarmSessionName(name string) (string, bool) {
	switch {
	case strings.HasPrefix(name, "cc_agents_"):
		return "cc", true
	case strings.HasPrefix(name, "cod_agents_"):
		return "cod", true
	case strings.HasPrefix(name, "gmi_agents_"):
		return "gmi", true
	default:
		return "", false
	}
}

// PrintSnapshotWithOptions outputs snapshot with pagination options.
func PrintSnapshotWithOptions(cfg *config.Config, opts PaginationOptions) error {
	output, err := GetSnapshotWithOptions(cfg, opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot snapshot failed")
}

// SnapshotDeltaOutput provides changes since a given timestamp.
type SnapshotDeltaOutput struct {
	RobotResponse
	Timestamp string   `json:"ts"`
	Since     string   `json:"since"`
	Changes   []Change `json:"changes"`
	// Truncated reports that the delta hit its per-response cap, so the
	// caller must narrow --since instead of assuming it saw every change.
	Truncated bool `json:"truncated,omitempty"`
}

// Change represents a state change event.
type Change struct {
	Type    string                 `json:"type"`
	Session string                 `json:"session,omitempty"`
	Pane    string                 `json:"pane,omitempty"`
	Data    map[string]interface{} `json:"data,omitempty"`
}

// GetSnapshotDelta retrieves state changes since the given timestamp.
// Replays the durable attention feed, including event-specific payloads.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetSnapshotDelta(since time.Time) (*SnapshotDeltaOutput, error) {
	output := &SnapshotDeltaOutput{
		RobotResponse: NewRobotResponse(true),
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		Since:         since.Format(time.RFC3339),
		Changes:       []Change{},
	}

	// Deltas come from the durable attention feed. They used to come from
	// `stateTracker`, a process-local in-memory ring buffer whose only writer
	// (RecordStateChange) had zero production callers — so every CLI
	// invocation, being a fresh process, reported "nothing changed" with
	// success:true forever, indistinguishable from a genuinely empty delta
	// (bd-fresh-eyes-audit .11).
	store := currentProjectionStore()
	if store == nil {
		output.RobotResponse = NewErrorResponse(
			errors.New("snapshot deltas require the runtime projection store"),
			ErrCodeNotImplemented,
			"Deltas are recorded by the durable attention feed; run 'ntm serve' (or refresh snapshot/status to initialize the projection) and retry",
		)
		return output, nil
	}

	events, err := store.GetAttentionEventsInTimeRange(since.UTC(), time.Now().UTC(), snapshotDeltaEventLimit)
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Failed to read the durable attention feed")
		return output, nil
	}
	for _, event := range events {
		change := Change{
			Type:    event.EventType,
			Session: event.SessionName,
			Pane:    event.Pane,
			Data: map[string]interface{}{
				"cursor":        event.Cursor,
				"ts":            event.Ts.UTC().Format(time.RFC3339Nano),
				"category":      event.Category,
				"source":        event.Source,
				"severity":      string(event.Severity),
				"actionability": string(event.Actionability),
				"summary":       event.Summary,
			},
		}
		if event.ReasonCode != "" {
			change.Data["reason_code"] = event.ReasonCode
		}
		// Preserve the canonical event payload and validated follow-up actions
		// used by --robot-events. Keep the stored scope above so window.pane
		// addresses survive even when they are not integer pane indices.
		replayed := attentionEventFromStored(event)
		if len(replayed.Details) > 0 {
			change.Data["details"] = replayed.Details
		}
		if len(replayed.NextActions) > 0 {
			change.Data["next_actions"] = replayed.NextActions
		}
		output.Changes = append(output.Changes, change)
	}
	output.Truncated = len(events) >= snapshotDeltaEventLimit

	return output, nil
}

// snapshotDeltaEventLimit bounds one delta response; Truncated tells the
// caller to narrow --since rather than silently believing it saw everything.
const snapshotDeltaEventLimit = 500

// PrintSnapshotDelta outputs changes since the given timestamp.
func PrintSnapshotDelta(since time.Time) error {
	output, err := GetSnapshotDelta(since)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot snapshot delta failed")
}
