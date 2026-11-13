package robot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/robot/adapters"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tracker"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

// SessionInfo contains machine-readable session information
type SessionInfo struct {
	Name        string     `json:"name"`
	Exists      bool       `json:"exists"`
	Attached    bool       `json:"attached,omitempty"`
	Windows     int        `json:"windows,omitempty"`
	Panes       int        `json:"panes,omitempty"`
	CreatedAt   *time.Time `json:"created_at,omitempty"`
	Agents      []Agent    `json:"agents,omitempty"`
	PrivacyMode bool       `json:"privacy_mode,omitempty"` // True if privacy mode is enabled
}

// Agent represents an AI agent in a session
type Agent struct {
	Type     string `json:"type"`              // claude, codex, antigravity, grok, gemini, or another supported type
	Variant  string `json:"variant,omitempty"` // Model alias or persona name
	Pane     string `json:"pane"`
	Name     string `json:"name,omitempty"` // Memorable agent name (e.g., claude-alpha)
	Window   int    `json:"window"`
	PaneIdx  int    `json:"pane_idx"`
	IsActive bool   `json:"is_active"`

	// Status enrichment fields
	PID                  int       `json:"pid,omitempty"`                     // Shell PID
	ChildPID             int       `json:"child_pid,omitempty"`               // Agent process PID
	LastOutputTS         time.Time `json:"last_output_ts,omitempty"`          // Last output timestamp
	SecondsSinceOutput   int       `json:"seconds_since_output,omitempty"`    // Time since last output
	RateLimitDetected    bool      `json:"rate_limit_detected,omitempty"`     // Rate limit pattern matched
	RateLimitMatch       string    `json:"rate_limit_match,omitempty"`        // The specific pattern matched
	ProcessState         string    `json:"process_state,omitempty"`           // R, S, D, Z, T
	ProcessStateName     string    `json:"process_state_name,omitempty"`      // running, sleeping, etc.
	MemoryMB             int       `json:"memory_mb,omitempty"`               // Resident memory in MB
	OutputLinesSinceLast int       `json:"output_lines_since_last,omitempty"` // Lines since last check
	ContextTokens        int       `json:"context_tokens,omitempty"`          // Estimated tokens used
	ContextLimit         int       `json:"context_limit,omitempty"`           // Model context limit
	ContextPercent       float64   `json:"context_percent,omitempty"`         // Usage percentage (0-100+)
	ContextModel         string    `json:"context_model,omitempty"`           // Model name for context limit lookup
	CaptureError         string    `json:"capture_error,omitempty"`

	// OutputSequence is the durable, privacy-preserving output-change signal
	// (#246). Absent when no runtime projection store is configured.
	OutputSequence *OutputSequenceInfo `json:"output_sequence,omitempty"`

	// UnsubmittedInput / QueuedMessages surface the composer state
	// (bd-v8dqd): real text sitting unsubmitted in the agent's input box,
	// and TUI-reported queued not-yet-processed messages, respectively.
	UnsubmittedInput bool `json:"unsubmitted_input,omitempty"`
	QueuedMessages   bool `json:"queued_messages,omitempty"`
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
	RobotResponse
	SchemaID        string                        `json:"schema_id"`
	SchemaVersion   string                        `json:"schema_version"`
	GeneratedAt     time.Time                     `json:"generated_at"`
	SafetyProfile   string                        `json:"safety_profile,omitempty"`
	OverallStatus   string                        `json:"overall_status,omitempty"`
	System          SystemInfo                    `json:"system"`
	Sessions        []StatusSessionHeader         `json:"sessions"`
	Pagination      *PaginationInfo               `json:"pagination,omitempty"`
	AgentHints      *AgentHints                   `json:"_agent_hints,omitempty"`
	Summary         StatusSummary                 `json:"summary"`
	AlertCounts     map[string]int                `json:"alert_counts,omitempty"`
	Sources         *adapters.SourceHealthSection `json:"sources,omitempty"`
	DegradedSources []string                      `json:"degraded_sources,omitempty"`
	Beads           *bv.BeadsSummary              `json:"beads,omitempty"`
	Progress        *ProgressSummary              `json:"progress,omitempty"`
	GraphMetrics    *GraphMetrics                 `json:"graph_metrics,omitempty"`
	AgentMail       *AgentMailSummary             `json:"agent_mail,omitempty"`
	Handoff         *HandoffSummary               `json:"handoff,omitempty"`
	Alerts          []StatusAlert                 `json:"alerts,omitempty"`
	FileChanges     []FileChangeInfo              `json:"file_changes,omitempty"`
	Conflicts       []tracker.Conflict            `json:"conflicts,omitempty"`
	SchedulerStats  *SchedulerStatsSummary        `json:"scheduler_stats,omitempty"`
}

// StatusSessionHeader is the compact session representation returned by
// --robot-status. It intentionally excludes nested agent detail.
type StatusSessionHeader struct {
	Name       string              `json:"name"`
	Attached   bool                `json:"attached,omitempty"`
	AgentCount int                 `json:"agent_count"`
	Health     StatusSessionHealth `json:"health"`
}

// StatusSessionHealth is the cheap health summary for a session header.
type StatusSessionHealth struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
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

// HandoffSummary is the latest handoff across all sessions.
type HandoffSummary struct {
	Session            string                        `json:"session"`
	Goal               string                        `json:"goal,omitempty"`
	GoalDisclosure     *adapters.DisclosureMetadata  `json:"goal_disclosure,omitempty"`
	Now                string                        `json:"now,omitempty"`
	NowDisclosure      *adapters.DisclosureMetadata  `json:"now_disclosure,omitempty"`
	Path               string                        `json:"path,omitempty"`
	AgeSeconds         int64                         `json:"age_seconds,omitempty"`
	Status             string                        `json:"status,omitempty"`
	UpdatedAt          string                        `json:"updated_at,omitempty"`
	ActiveBeads        []string                      `json:"active_beads,omitempty"`
	AgentMailThreads   []string                      `json:"agent_mail_threads,omitempty"`
	Blockers           []string                      `json:"blockers,omitempty"`
	BlockerDisclosures []adapters.DisclosureMetadata `json:"blocker_disclosures,omitempty"`
	Files              []string                      `json:"files,omitempty"`
}

// SchedulerStatsSummary provides spawn scheduler statistics for robot-status.
// This surfaces queue depth, backoff state, headroom, and rate limit status
// to help agents understand spawn pacing.
type SchedulerStatsSummary struct {
	// Enabled indicates if spawn pacing is active.
	Enabled bool `json:"enabled"`

	// QueueDepth is the current number of jobs waiting in queue.
	QueueDepth int `json:"queue_depth"`

	// RunningCount is the number of currently executing spawn jobs.
	RunningCount int `json:"running_count"`

	// TotalSubmitted is the total jobs submitted since scheduler start.
	TotalSubmitted int64 `json:"total_submitted"`

	// TotalCompleted is the total jobs completed successfully.
	TotalCompleted int64 `json:"total_completed"`

	// TotalFailed is the total jobs that failed after all retries.
	TotalFailed int64 `json:"total_failed"`

	// IsPaused indicates if the scheduler is paused.
	IsPaused bool `json:"is_paused"`

	// InBackoff indicates if global backoff is currently active.
	InBackoff bool `json:"in_backoff"`

	// BackoffRemainingMs is the remaining backoff time in milliseconds.
	BackoffRemainingMs int64 `json:"backoff_remaining_ms,omitempty"`

	// HeadroomOK indicates if resource headroom is sufficient.
	HeadroomOK bool `json:"headroom_ok"`

	// HeadroomReason describes why headroom is blocked (if any).
	HeadroomReason string `json:"headroom_reason,omitempty"`

	// RateLimitTokens is the current available rate limit tokens.
	RateLimitTokens float64 `json:"rate_limit_tokens"`

	// AgentCaps shows per-agent concurrency status.
	AgentCaps *AgentCapsStatus `json:"agent_caps,omitempty"`

	// UptimeSeconds is how long the scheduler has been running.
	UptimeSeconds int64 `json:"uptime_seconds,omitempty"`
}

// AgentCapsStatus shows per-agent-type concurrency cap status.
type AgentCapsStatus struct {
	ClaudeCurrent int `json:"claude_current"`
	ClaudeMax     int `json:"claude_max"`
	CodexCurrent  int `json:"codex_current"`
	CodexMax      int `json:"codex_max"`
	GeminiCurrent int `json:"gemini_current"`
	GeminiMax     int `json:"gemini_max"`
}

// StatusAlert represents a warning or alert emitted by robot status.
type StatusAlert struct {
	Type         string  `json:"type"`
	Session      string  `json:"session,omitempty"`
	Pane         string  `json:"pane,omitempty"`
	PaneIdx      int     `json:"pane_idx,omitempty"`
	UsagePercent float64 `json:"usage_percent,omitempty"`
	ContextModel string  `json:"context_model,omitempty"`
	Severity     string  `json:"severity,omitempty"`
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
	TotalSessions    int            `json:"total_sessions"`
	TotalAgents      int            `json:"total_agents"`
	AttachedCount    int            `json:"attached_count"`
	ClaudeCount      int            `json:"claude_count"`
	CodexCount       int            `json:"codex_count"`
	GeminiCount      int            `json:"gemini_count"`
	AntigravityCount int            `json:"antigravity_count"`
	GrokCount        int            `json:"grok_count"`
	CursorCount      int            `json:"cursor_count"`
	WindsurfCount    int            `json:"windsurf_count"`
	AiderCount       int            `json:"aider_count"`
	OpencodeCount    int            `json:"opencode_count"`
	OmpCount         int            `json:"omp_count"`
	OllamaCount      int            `json:"ollama_count"`
	AgentsByState    map[string]int `json:"agents_by_state"`
	AgentsByType     map[string]int `json:"agents_by_type"`
	ReadyWork        int            `json:"ready_work"`
	InProgress       int            `json:"in_progress"`
	HealthScore      float64        `json:"health_score"`
	HealthStatus     string         `json:"health_status,omitempty"`
	AlertsActive     int            `json:"alerts_active"`
	MailUnread       int            `json:"mail_unread"`
	MailUrgent       int            `json:"mail_urgent"`
}

// ProgressSummary provides bead completion metrics for status and dashboard (bd-1qct).
type ProgressSummary struct {
	Assigned        int     `json:"assigned"`         // Beads currently in progress
	Completed       int     `json:"completed"`        // Beads closed
	Remaining       int     `json:"remaining"`        // Open + in-progress (not yet closed)
	Total           int     `json:"total"`            // All beads
	CompletionRatio float64 `json:"completion_ratio"` // Closed / Total (0.0 to 1.0)
}

// ComputeProgress derives a ProgressSummary from BeadsSummary counts.
func ComputeProgress(beads *bv.BeadsSummary) *ProgressSummary {
	if beads == nil || !beads.Available || beads.Total == 0 {
		return nil
	}
	remaining := beads.Open + beads.InProgress
	ratio := float64(beads.Closed) / float64(beads.Total)
	// Round to 4 decimal places for cleaner JSON
	ratio = float64(int(ratio*10000+0.5)) / 10000
	return &ProgressSummary{
		Assigned:        beads.InProgress,
		Completed:       beads.Closed,
		Remaining:       remaining,
		Total:           beads.Total,
		CompletionRatio: ratio,
	}
}

// GetStatusWithOptions collects status and applies pagination to sessions.
func GetStatusWithOptions(opts PaginationOptions) (*StatusOutput, error) {
	wd := mustGetwd()
	cfg, err := config.LoadMerged(wd, config.DefaultPath())
	if err != nil {
		cfg = config.Default()
	}

	if store := opts.resolveStore(); store != nil {
		output, err := buildProjectionBackedStatus(store, cfg, opts)
		if err == nil {
			return output, nil
		}
	}

	return buildLiveStatus(wd, cfg, opts)
}

func newStatusOutput(cfg *config.Config) *StatusOutput {
	if cfg == nil {
		cfg = config.Default()
	}
	return &StatusOutput{
		RobotResponse: NewRobotResponse(true),
		SchemaID:      defaultRobotSchemaID("status"),
		SchemaVersion: statusSchemaVersion,
		GeneratedAt:   time.Now().UTC(),
		SafetyProfile: cfg.Safety.Profile,
		System: SystemInfo{
			Version:   Version,
			Commit:    Commit,
			BuildDate: Date,
			GoVersion: runtime.Version(),
			OS:        runtime.GOOS,
			Arch:      runtime.GOARCH,
			TmuxOK:    tmux.IsInstalled(),
		},
		Sessions:        []StatusSessionHeader{},
		Summary:         StatusSummary{AgentsByState: map[string]int{}, AgentsByType: map[string]int{}},
		AlertCounts:     map[string]int{},
		Sources:         &adapters.SourceHealthSection{Sources: map[string]adapters.SourceInfo{}, Degraded: []string{}, AllFresh: true},
		DegradedSources: []string{},
	}
}

// errProjectionStaleEmpty signals that the runtime projection returned zero
// fresh session rows while live tmux reports running sessions. Every
// runtime_sessions row can age past stale_after between collector runs, in
// which case GetFreshRuntimeSessions legitimately returns no rows and no
// error — but presenting that as an authoritative empty fleet would be false
// (freshness contract: fail-visible, never treat stale data as authoritative).
// Callers treat this error as "projection cannot answer" and fall back to
// live tmux enumeration.
var errProjectionStaleEmpty = errors.New("runtime projection has no fresh sessions while tmux reports live sessions")

// projectionLiveSessions is a test seam for the live tmux check performed
// when the projection returns zero fresh rows.
var projectionLiveSessions = tmux.ListSessions

func buildProjectionBackedStatus(store *state.Store, cfg *config.Config, opts PaginationOptions) (*StatusOutput, error) {
	output := newStatusOutput(cfg)

	sessions, err := store.GetFreshRuntimeSessions()
	if err != nil {
		return nil, fmt.Errorf("status sessions: %w", err)
	}
	if len(sessions) == 0 {
		// Zero fresh rows is ambiguous: the machine may be idle, or every
		// projection row may have gone stale. Only trust the empty answer
		// when live tmux confirms it; otherwise degrade to the live path.
		live, liveErr := projectionLiveSessions()
		if liveErr != nil || len(live) > 0 {
			return nil, errProjectionStaleEmpty
		}
	}
	agentsBySession := make(map[string][]state.RuntimeAgent, len(sessions))
	for _, sess := range sessions {
		agents, err := store.GetRuntimeAgentsBySession(sess.Name)
		if err != nil {
			return nil, fmt.Errorf("status agents for %s: %w", sess.Name, err)
		}
		agentsBySession[sess.Name] = agents
	}

	coordinationRows, err := store.ListFreshRuntimeCoordination("")
	if err != nil {
		return nil, fmt.Errorf("status coordination: %w", err)
	}
	handoffRow, err := store.GetRuntimeHandoff()
	if err != nil {
		return nil, fmt.Errorf("status handoff: %w", err)
	}
	healthRows, err := store.GetAllSourceHealth()
	if err != nil {
		return nil, fmt.Errorf("status source health: %w", err)
	}

	for _, sess := range sessions {
		agents := agentsBySession[sess.Name]
		agentCount := sess.AgentCount
		if len(agents) > 0 {
			agentCount = len(agents)
		}
		output.Sessions = append(output.Sessions, StatusSessionHeader{
			Name:       sess.Name,
			Attached:   sess.Attached,
			AgentCount: agentCount,
			Health: StatusSessionHealth{
				Status: statusHealthString(sess.HealthStatus),
				Reason: strings.TrimSpace(sess.HealthReason),
			},
		})

		output.Summary.TotalSessions++
		output.Summary.TotalAgents += agentCount
		if sess.Attached {
			output.Summary.AttachedCount++
		}
		if len(agents) == 0 && agentCount > 0 {
			output.Summary.AgentsByState["unknown"] += agentCount
		}
		for _, agent := range agents {
			statusAccumulateAgentSummary(&output.Summary, agent.AgentType, string(agent.State))
		}
	}

	// Work counts come only from a source-verified observation; unverifiable
	// work reports none (GH #283). A failing work source is already named in
	// the source-health rows the refresh records.
	projectDir, err := resolveNormalizedProjectDir("")
	if err != nil {
		return nil, fmt.Errorf("status work: %w", err)
	}
	work := verifiedRobotWork(store, projectDir, 0)
	if work.Available {
		output.Summary.ReadyWork = work.Summary.Ready
		output.Summary.InProgress = work.Summary.InProgress
	}

	for _, item := range coordinationRows {
		output.Summary.MailUnread += item.UnreadCount
		output.Summary.MailUrgent += item.UrgentCount
	}
	output.Handoff = statusHandoffFromRuntime(handoffRow)

	output.Sources = statusSourceHealthFromRows(healthRows)
	if output.Sources != nil {
		output.DegradedSources = append(output.DegradedSources, output.Sources.Degraded...)
	}

	statusApplyAlertCounts(output, alerts.GetActiveAlerts(AlertConfigForProject(cfg, "")))
	statusFinalize(output, opts)
	return output, nil
}

func buildLiveStatus(projectDir string, cfg *config.Config, opts PaginationOptions) (*StatusOutput, error) {
	output := newStatusOutput(cfg)

	resolvedProjectDir, err := resolveNormalizedProjectDir(projectDir)
	if err != nil {
		return output, nil
	}

	snapshot, err := collectNormalizedTmuxProjection(resolvedProjectDir, normalizedProjectionStaleAfter)
	if err != nil {
		return output, nil
	}

	for _, sess := range snapshot.Sessions {
		agentCount := sess.AgentCount
		if agentCount < 0 {
			agentCount = 0
		}
		output.Sessions = append(output.Sessions, StatusSessionHeader{
			Name:       sess.Name,
			Attached:   sess.Attached,
			AgentCount: agentCount,
			Health: StatusSessionHealth{
				Status: statusHealthString(sess.HealthStatus),
				Reason: strings.TrimSpace(sess.HealthReason),
			},
		})
		output.Summary.TotalSessions++
		output.Summary.TotalAgents += agentCount
		if sess.Attached {
			output.Summary.AttachedCount++
		}
	}

	for _, agent := range snapshot.Agents {
		statusAccumulateAgentSummary(&output.Summary, agent.AgentType, string(agent.State))
		output.Summary.MailUnread += agent.PendingMail
	}

	aggregator := adapters.NewSignalAggregator(0)
	aggregator.RegisterAdapter(adapters.NewWorkCoordinationAdapter(
		adapters.DefaultWorkCoordinationAdapterConfig(resolvedProjectDir),
	))
	collectCtx, cancel := context.WithTimeout(context.Background(), statusLiveCollectionLimit)
	defer cancel()

	signals, err := aggregator.Collect(collectCtx)
	if err == nil && signals != nil {
		if signals.Work != nil {
			if signals.Work.Summary != nil {
				output.Summary.ReadyWork = signals.Work.Summary.Ready
				output.Summary.InProgress = signals.Work.Summary.InProgress
			} else {
				output.Summary.ReadyWork = len(signals.Work.Ready)
				output.Summary.InProgress = len(signals.Work.InProgress)
			}
		}
		if signals.Coordination != nil && signals.Coordination.Mail != nil {
			output.Summary.MailUnread = signals.Coordination.Mail.TotalUnread
			output.Summary.MailUrgent = signals.Coordination.Mail.UrgentUnread
		}
		if signals.Coordination != nil && signals.Coordination.Handoff != nil {
			output.Handoff = statusHandoffFromAdapter(signals.Coordination.Handoff)
		}
		if signals.Health != nil {
			output.Sources = cloneSourceHealthSection(signals.Health)
			output.DegradedSources = append(output.DegradedSources, signals.Health.Degraded...)
		}
		statusApplyAggregatedAlertCounts(output, signals.Alerts)
	} else if err != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
		output.DegradedSources = append(output.DegradedSources, "work")
		output.DegradedSources = append(output.DegradedSources, "coordination")
	}
	applyStatusTmuxCaptureFailures(output, snapshot.Agents)

	if output.Summary.MailUrgent == 0 && output.Summary.MailUnread > 0 {
		output.Summary.MailUrgent = 0
	}
	if len(output.AlertCounts) == 0 {
		statusApplyAlertCounts(output, alerts.GetActiveAlerts(AlertConfigForProject(cfg, resolvedProjectDir)))
	}

	statusFinalize(output, opts)
	return output, nil
}

func applyStatusTmuxCaptureFailures(output *StatusOutput, agents []state.RuntimeAgent) {
	if output == nil {
		return
	}
	var failures []string
	for _, agent := range agents {
		if err := strings.TrimSpace(agent.CaptureError); err != "" {
			failures = append(failures, fmt.Sprintf("%s: %s", agent.Pane, err))
		}
	}
	if len(failures) == 0 {
		return
	}
	if output.Sources == nil {
		output.Sources = &adapters.SourceHealthSection{Sources: make(map[string]adapters.SourceInfo)}
	}
	if output.Sources.Sources == nil {
		output.Sources.Sources = make(map[string]adapters.SourceInfo)
	}
	info := output.Sources.Sources["tmux"]
	info.Name = "tmux"
	info.Available = true
	info.Fresh = false
	info.Degraded = true
	info.DegradedReason = "pane capture failed"
	info.LastError = strings.Join(failures, "; ")
	info.DegradedSince = time.Now().UTC().Format(time.RFC3339Nano)
	output.Sources.Sources["tmux"] = info
	output.Sources.Degraded = uniqueSortedStrings(append(output.Sources.Degraded, "tmux"))
	output.Sources.AllFresh = false
	output.DegradedSources = append(output.DegradedSources, "tmux")
}

func statusSourceHealthFromRows(rows []state.SourceHealth) *adapters.SourceHealthSection {
	section := &adapters.SourceHealthSection{
		Sources:  map[string]adapters.SourceInfo{},
		Degraded: []string{},
		AllFresh: true,
	}
	for _, row := range rows {
		status := row.Status(normalizedProjectionStaleAfter)
		fresh := status == state.SourceStatusFresh
		info := adapters.SourceInfo{
			Name:       row.SourceName,
			Available:  row.Available,
			Fresh:      fresh,
			AgeMs:      time.Since(row.LastCheckAt).Milliseconds(),
			UpdatedAt:  row.LastCheckAt.UTC().Format(time.RFC3339Nano),
			Degraded:   !fresh,
			LastError:  strings.TrimSpace(row.LastError),
			ReasonCode: adapters.ReasonCode(strings.TrimSpace(row.LastErrorCode)),
		}
		if !fresh {
			section.AllFresh = false
			info.DegradedReason = firstNonEmptyString(strings.TrimSpace(row.Reason), string(status))
			section.Degraded = append(section.Degraded, row.SourceName)
			if row.LastFailureAt != nil && !row.LastFailureAt.IsZero() {
				info.DegradedSince = row.LastFailureAt.UTC().Format(time.RFC3339Nano)
			}
		}
		section.Sources[row.SourceName] = info
	}
	if len(rows) == 0 {
		section.AllFresh = false
	}
	return section
}

// SourceHealthSectionFromRows rehydrates persisted source health rows into the
// normalized source-health model used by robot surfaces.
func SourceHealthSectionFromRows(rows []state.SourceHealth) *adapters.SourceHealthSection {
	return statusSourceHealthFromRows(rows)
}

func cloneSourceHealthSection(section *adapters.SourceHealthSection) *adapters.SourceHealthSection {
	if section == nil {
		return nil
	}
	cloned := &adapters.SourceHealthSection{
		Sources:  make(map[string]adapters.SourceInfo, len(section.Sources)),
		Degraded: append([]string(nil), section.Degraded...),
		AllFresh: section.AllFresh,
	}
	for key, value := range section.Sources {
		cloned.Sources[key] = value
	}
	return cloned
}

func statusAccumulateAgentSummary(summary *StatusSummary, agentType, agentState string) {
	if summary == nil {
		return
	}
	typeKey := strings.TrimSpace(agentType)
	if typeKey == "" {
		typeKey = "unknown"
	}
	stateKey := strings.TrimSpace(agentState)
	if stateKey == "" {
		stateKey = "unknown"
	}
	summary.AgentsByType[typeKey]++
	summary.AgentsByState[stateKey]++

	switch typeKey {
	case "claude":
		summary.ClaudeCount++
	case "codex":
		summary.CodexCount++
	case "gemini":
		summary.GeminiCount++
	case "antigravity":
		summary.AntigravityCount++
	case "grok":
		summary.GrokCount++
	case "cursor":
		summary.CursorCount++
	case "windsurf":
		summary.WindsurfCount++
	case "aider":
		summary.AiderCount++
	case "oc":
		summary.OpencodeCount++
	case "omp":
		summary.OmpCount++
	case "ollama":
		summary.OllamaCount++
	}
}

func statusApplyAlertCounts(output *StatusOutput, active []alerts.Alert) {
	if output == nil {
		return
	}
	if output.AlertCounts == nil {
		output.AlertCounts = map[string]int{}
	}
	for _, alert := range active {
		key := strings.TrimSpace(string(alert.Severity))
		if key == "" {
			key = "warning"
		}
		output.AlertCounts[key]++
	}
}

func statusApplyAggregatedAlertCounts(output *StatusOutput, section *adapters.AlertsSection) {
	if output == nil || section == nil {
		return
	}
	if output.AlertCounts == nil {
		output.AlertCounts = map[string]int{}
	}
	if section.Summary != nil && len(section.Summary.BySeverity) > 0 {
		for key, value := range section.Summary.BySeverity {
			output.AlertCounts[key] += value
		}
		return
	}
	for _, alert := range section.Active {
		key := strings.TrimSpace(alert.Severity)
		if key == "" {
			key = "warning"
		}
		output.AlertCounts[key]++
	}
}

func statusFinalize(output *StatusOutput, opts PaginationOptions) {
	if output == nil {
		return
	}
	if output.AlertCounts == nil {
		output.AlertCounts = map[string]int{}
	}
	if output.Summary.AgentsByState == nil {
		output.Summary.AgentsByState = map[string]int{}
	}
	if output.Summary.AgentsByType == nil {
		output.Summary.AgentsByType = map[string]int{}
	}
	output.DegradedSources = uniqueSortedStrings(output.DegradedSources)
	output.Summary.AlertsActive = statusAlertTotal(output.AlertCounts)
	output.OverallStatus = statusOverall(output)
	output.Summary.HealthStatus = output.OverallStatus
	output.Summary.HealthScore = statusHealthScore(output)

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

func statusHealthString(status state.HealthStatus) string {
	value := strings.TrimSpace(string(status))
	if value == "" {
		return string(state.HealthStatusUnknown)
	}
	return value
}

func statusAlertTotal(counts map[string]int) int {
	total := 0
	for _, value := range counts {
		total += value
	}
	return total
}

func statusOverall(output *StatusOutput) string {
	if output == nil {
		return "unknown"
	}
	if output.AlertCounts["critical"] > 0 || output.Summary.AgentsByState["error"] > 0 {
		return "critical"
	}
	if len(output.DegradedSources) > 0 || output.AlertCounts["error"] > 0 || output.AlertCounts["warning"] > 0 {
		return "degraded"
	}
	return "healthy"
}

func statusHealthScore(output *StatusOutput) float64 {
	score := 1.0
	if output == nil {
		return 0
	}
	score -= float64(len(output.DegradedSources)) * 0.1
	score -= float64(output.AlertCounts["warning"]) * 0.05
	score -= float64(output.AlertCounts["error"]) * 0.1
	score -= float64(output.AlertCounts["critical"]) * 0.2
	score -= float64(output.Summary.AgentsByState["error"]) * 0.1
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	return float64(int(score*100+0.5)) / 100
}

func uniqueSortedStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}
	sort.Strings(result)
	return result
}

// PrintStatusWithOptions outputs status with pagination options.
func PrintStatusWithOptions(opts PaginationOptions) error {
	output, err := GetStatusWithOptions(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot status failed")
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

// AlertConfigForProject is the one alert-generator configuration for every
// surface (robot status/snapshot/alerts, markdown, dashboard): the [alerts]
// thresholds plus the transcript-backed context usage source that turns
// alerts.context_warning_threshold into per-pane context warnings.
func AlertConfigForProject(cfg *config.Config, projectDir string) alerts.Config {
	resolvedProject := strings.TrimSpace(projectDir)
	if resolvedProject == "" {
		resolvedProject = util.ResolveProjectDir("")
	}

	alertCfg := alerts.DefaultConfig()
	if cfg != nil {
		alertCfg = alerts.ToConfigAlerts(
			cfg.Alerts.Enabled,
			cfg.Alerts.AgentStuckMinutes,
			cfg.Alerts.DiskLowThresholdGB,
			cfg.Alerts.MailBacklogThreshold,
			cfg.Alerts.BeadStaleHours,
			cfg.Alerts.ContextWarningThreshold,
			cfg.Alerts.ResolvedPruneMinutes,
			resolvedProject,
		)
	}
	alertCfg.ProjectsDir = resolvedProject
	alertCfg.PaneContextUsage = paneContextUsagePercents
	return alertCfg
}

// AlertsOutput provides machine-readable alert information
type AlertsOutput struct {
	RobotResponse
	GeneratedAt time.Time        `json:"generated_at"`
	Enabled     bool             `json:"enabled"`
	Active      []AlertInfo      `json:"active"`
	Resolved    []AlertInfo      `json:"resolved,omitempty"`
	Summary     AlertSummaryInfo `json:"summary"`
}

// GetAlertsDetailed returns all alerts.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetAlertsDetailed(includeResolved bool) (*AlertsOutput, error) {
	wd := mustGetwd()
	cfg, err := config.LoadMerged(wd, config.DefaultPath())
	if err != nil {
		cfg = config.Default()
	}
	alertCfg := AlertConfigForProject(cfg, wd)
	tracker := alerts.GenerateAndTrack(alertCfg)

	active, resolved := tracker.GetAll()
	summary := tracker.Summary()

	output := &AlertsOutput{
		RobotResponse: NewRobotResponse(true),
		GeneratedAt:   time.Now().UTC(),
		Enabled:       alertCfg.Enabled,
		Active:        make([]AlertInfo, len(active)),
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
	}

	return output, nil
}

// TerseState represents the ultra-compact state for token-constrained scenarios.
// Format: S:session|A:active/total|W:working|I:idle|E:errors|C:ctx%|B:Rn/In/Bn|M:mail|^:NaNi|!:
type TerseState struct {
	Session           string `json:"session"`
	ActiveAgents      int    `json:"active_agents"`
	TotalAgents       int    `json:"total_agents"`
	WorkingAgents     int    `json:"working_agents"` // Agents actively processing
	IdleAgents        int    `json:"idle_agents"`    // Agents waiting at prompt
	ErrorAgents       int    `json:"error_agents"`   // Agents in error state
	ContextPct        int    `json:"context_pct"`    // Average context usage %
	ReadyBeads        int    `json:"ready_beads"`    // Beads ready to work on
	BlockedBeads      int    `json:"blocked_beads"`  // Blocked beads
	InProgressBead    int    `json:"in_progress_beads"`
	UnreadMail        int    `json:"unread_mail"`
	AttentionAction   int    `json:"attention_action"`      // Action-required attention events
	AttentionInterest int    `json:"attention_interesting"` // Interesting attention events
	CriticalAlerts    int    `json:"critical_alerts"`
	WarningAlerts     int    `json:"warning_alerts"`
}

// String returns the ultra-compact string representation.
// Format: S:session|A:active/total|W:working|I:idle|E:errors|C:ctx%|B:Rn/In/Bn|M:mail|^:NaNi|!:
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

	// Build attention string: Na,Ni (action, interesting) or 0 if none
	attnStr := ""
	if t.AttentionAction > 0 || t.AttentionInterest > 0 {
		var parts []string
		if t.AttentionAction > 0 {
			parts = append(parts, fmt.Sprintf("%da", t.AttentionAction))
		}
		if t.AttentionInterest > 0 {
			parts = append(parts, fmt.Sprintf("%di", t.AttentionInterest))
		}
		attnStr = strings.Join(parts, ",")
	} else {
		attnStr = "0"
	}

	return fmt.Sprintf("S:%s|A:%d/%d|W:%d|I:%d|E:%d|C:%d%%|B:R%d/I%d/B%d|M:%d|^:%s|!:%s",
		t.Session,
		t.ActiveAgents, t.TotalAgents,
		t.WorkingAgents, t.IdleAgents, t.ErrorAgents,
		t.ContextPct,
		t.ReadyBeads, t.InProgressBead, t.BlockedBeads,
		t.UnreadMail,
		attnStr,
		alertStr)
}

// TerseOutput wraps terse state for robot API output.
type TerseOutput struct {
	RobotResponse
	States        []TerseState `json:"states"`
	TerseLines    []string     `json:"terse_lines"`    // Pre-formatted terse strings
	AttentionHint string       `json:"attention_hint"` // Compact attention summary (e.g., "2!action 5?interesting")
}

// GetTerse retrieves ultra-compact single-line state for token-constrained scenarios.
func GetTerse(cfg *config.Config) (*TerseOutput, error) {
	return GetTerseForStore(currentProjectionStore(), cfg)
}

// GetTerseForStore returns compact state using an explicit projection store.
func GetTerseForStore(store *state.Store, cfg *config.Config) (*TerseOutput, error) {
	snapshot, err := GetSnapshotWithOptions(cfg, PaginationOptions{
		Store:         store,
		ExplicitStore: true,
	})
	if err != nil {
		return nil, err
	}
	return buildTerseOutputFromSnapshot(snapshot), nil
}

func buildTerseOutputFromSnapshot(snapshot *SnapshotOutput) *TerseOutput {
	if snapshot != nil && !snapshot.Success {
		return &TerseOutput{
			RobotResponse: snapshot.RobotResponse,
			States:        []TerseState{},
			TerseLines:    []string{},
		}
	}

	output := &TerseOutput{
		RobotResponse: RobotResponse{
			Success:   true,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
		States:     []TerseState{},
		TerseLines: []string{},
	}
	if snapshot == nil {
		state := TerseState{Session: "-"}
		output.States = append(output.States, state)
		output.TerseLines = append(output.TerseLines, formatTerseLine(state, "feed:unavail"))
		output.AttentionHint = "feed:unavail"
		return output
	}

	criticalAlerts, warningAlerts := terseAlertCounts(snapshot)
	readyBeads, inProgressBeads, blockedBeads := terseWorkCounts(snapshot)
	mailCount := snapshot.MailUnread
	if mailCount == 0 {
		mailCount = snapshot.Summary.MailUnread
	}

	var attnAction, attnInterest int
	if snapshot.AttentionSummary == nil {
		output.AttentionHint = "feed:unavail"
	} else {
		attnAction = snapshot.AttentionSummary.ActionRequiredCount
		attnInterest = snapshot.AttentionSummary.InterestingCount
		output.AttentionHint = buildAttentionHintFromSummary(snapshot.AttentionSummary)
	}

	if len(snapshot.Sessions) == 0 {
		state := TerseState{
			Session:           "-",
			CriticalAlerts:    criticalAlerts,
			WarningAlerts:     warningAlerts,
			UnreadMail:        mailCount,
			AttentionAction:   attnAction,
			AttentionInterest: attnInterest,
			ReadyBeads:        readyBeads,
			BlockedBeads:      blockedBeads,
			InProgressBead:    inProgressBeads,
		}
		output.States = append(output.States, state)
		output.TerseLines = append(output.TerseLines, formatTerseLine(state, output.AttentionHint))
		return output
	}

	for _, sess := range snapshot.Sessions {
		state := TerseState{
			Session:           sess.Name,
			CriticalAlerts:    criticalAlerts,
			WarningAlerts:     warningAlerts,
			UnreadMail:        mailCount,
			AttentionAction:   attnAction,
			AttentionInterest: attnInterest,
			ReadyBeads:        readyBeads,
			BlockedBeads:      blockedBeads,
			InProgressBead:    inProgressBeads,
		}

		state.TotalAgents = len(sess.Agents)
		var totalContextPercent float64
		var contextAgentCount int
		for _, agent := range sess.Agents {
			if strings.EqualFold(agent.Type, "user") {
				continue
			}
			state.ActiveAgents++
			if agent.ContextPercent != nil {
				totalContextPercent += *agent.ContextPercent
				contextAgentCount++
			}
			switch strings.ToLower(strings.TrimSpace(agent.State)) {
			case "idle":
				state.IdleAgents++
			case "error":
				state.ErrorAgents++
			default:
				state.WorkingAgents++
			}
		}
		if contextAgentCount > 0 {
			state.ContextPct = int(totalContextPercent / float64(contextAgentCount))
		}

		output.States = append(output.States, state)
		output.TerseLines = append(output.TerseLines, formatTerseLine(state, output.AttentionHint))
	}

	return output
}

func terseAlertCounts(snapshot *SnapshotOutput) (critical, warning int) {
	if snapshot == nil {
		return 0, 0
	}
	if snapshot.AlertSummary != nil {
		return snapshot.AlertSummary.BySeverity["critical"], snapshot.AlertSummary.BySeverity["warning"]
	}
	for _, alert := range snapshot.AlertsDetailed {
		switch strings.ToLower(strings.TrimSpace(alert.Severity)) {
		case "critical":
			critical++
		case "warning":
			warning++
		}
	}
	return critical, warning
}

func terseWorkCounts(snapshot *SnapshotOutput) (ready, inProgress, blocked int) {
	if snapshot == nil {
		return 0, 0, 0
	}
	if snapshot.Work != nil && snapshot.Work.Summary != nil {
		return snapshot.Work.Summary.Ready, snapshot.Work.Summary.InProgress, snapshot.Work.Summary.Blocked
	}
	if snapshot.BeadsSummary != nil {
		return snapshot.BeadsSummary.Ready, snapshot.BeadsSummary.InProgress, snapshot.BeadsSummary.Blocked
	}
	return snapshot.Summary.ReadyWork, snapshot.Summary.InProgress, 0
}

func buildAttentionHintFromSummary(summary *SnapshotAttentionSummary) string {
	if summary == nil || summary.TotalEvents == 0 {
		return "clear"
	}
	parts := []string{}
	if summary.ActionRequiredCount > 0 {
		parts = append(parts, fmt.Sprintf("%d!action", summary.ActionRequiredCount))
	}
	if summary.InterestingCount > 0 {
		parts = append(parts, fmt.Sprintf("%d?interest", summary.InterestingCount))
	}
	if len(parts) == 0 {
		return "clear"
	}
	return strings.Join(parts, " ")
}

func formatTerseLine(state TerseState, attentionHint string) string {
	line := state.String()
	if attentionHint != "feed:unavail" {
		return line
	}
	return line + "|T:" + attentionHint
}

// PrintTerse outputs ultra-compact single-line state for token-constrained scenarios.
// Output format: S:session|A:active/total|W:working|I:idle|E:errors|C:ctx%|B:Rn/In/Bn|M:mail|^:NaNi|!:
// If the attention feed is unavailable, an additional |T:feed:unavail suffix is appended.
// Multiple sessions are separated by semicolons.
func PrintTerse(cfg *config.Config) error {
	output, err := GetTerse(cfg)
	if err != nil {
		return err
	}
	if !output.Success {
		return encodeTerminalRobotOutput(output, output.RobotResponse, "robot terse failed")
	}

	// Output all sessions separated by semicolons (preserving original format)
	fmt.Println(strings.Join(output.TerseLines, ";"))
	return nil
}
