// Package robot provides machine-readable output for AI agents.
// tui_parity.go implements robot commands that mirror TUI functionality,
// ensuring AI agents have access to the same information as human users.
package robot

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/robot/adapters"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tracker"
)

// =============================================================================
// File Changes (--robot-files)
// =============================================================================
// Mirrors the Files panel in the dashboard TUI, providing file change tracking
// with agent attribution and time window filtering.

// FilesOutput represents the output for --robot-files
type FilesOutput struct {
	RobotResponse
	Session    string             `json:"session,omitempty"`
	TimeWindow string             `json:"time_window"` // "5m", "15m", "1h", "all"
	Count      int                `json:"count"`
	Changes    []FileChangeRecord `json:"changes"`
	Summary    FileChangesSummary `json:"summary"`
	AgentHints *AgentHints        `json:"_agent_hints,omitempty"`
}

// FileChangeRecord represents a single file change with agent attribution
type FileChangeRecord struct {
	Timestamp    string   `json:"timestamp"` // RFC3339
	Path         string   `json:"path"`      // Relative file path
	Operation    string   `json:"operation"` // "create", "modify", "delete", "rename"
	Agents       []string `json:"agents"`    // Agents that touched this file
	Session      string   `json:"session"`   // Session where change was detected
	SizeBytes    int64    `json:"size_bytes,omitempty"`
	LinesAdded   int      `json:"lines_added,omitempty"`
	LinesRemoved int      `json:"lines_removed,omitempty"`
}

// FileChangesSummary provides aggregate statistics
type FileChangesSummary struct {
	TotalChanges    int            `json:"total_changes"`
	UniqueFiles     int            `json:"unique_files"`
	ByAgent         map[string]int `json:"by_agent"`     // Agent -> change count
	ByOperation     map[string]int `json:"by_operation"` // Operation -> count
	MostActiveAgent string         `json:"most_active_agent,omitempty"`
	Conflicts       []FileConflict `json:"conflicts,omitempty"` // Files touched by multiple agents
}

// FileConflict represents a file modified by multiple agents
type FileConflict struct {
	Path      string   `json:"path"`
	Agents    []string `json:"agents"`
	Severity  string   `json:"severity"`   // "warning", "critical"
	FirstEdit string   `json:"first_edit"` // RFC3339
	LastEdit  string   `json:"last_edit"`  // RFC3339
}

// FilesOptions configures the --robot-files command
type FilesOptions struct {
	Session    string // Filter to specific session
	TimeWindow string // "5m", "15m", "1h", "all" (default: "15m")
	Limit      int    // Max changes to return (default: 100)
}

// GetFiles returns file changes data.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetFiles(opts FilesOptions) (*FilesOutput, error) {
	// Set defaults
	if opts.TimeWindow == "" {
		opts.TimeWindow = "15m"
	}
	if opts.Limit <= 0 {
		opts.Limit = 100
	}

	output := &FilesOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		TimeWindow:    opts.TimeWindow,
		Changes:       []FileChangeRecord{},
		Summary: FileChangesSummary{
			ByAgent:     make(map[string]int),
			ByOperation: make(map[string]int),
		},
	}

	// Get file changes from the global store
	store := tracker.GlobalFileChanges
	if store == nil {
		output.AgentHints = &AgentHints{
			Summary: "File change tracking not initialized",
			Notes:   []string{"File changes are tracked when agents modify files within ntm sessions"},
		}
		return output, nil
	}

	// Calculate time cutoff
	var cutoff time.Time
	switch opts.TimeWindow {
	case "5m":
		cutoff = time.Now().Add(-5 * time.Minute)
	case "15m":
		cutoff = time.Now().Add(-15 * time.Minute)
	case "1h":
		cutoff = time.Now().Add(-1 * time.Hour)
	case "all":
		cutoff = time.Time{} // No cutoff
	default:
		// Try to parse as duration
		if d, err := time.ParseDuration(opts.TimeWindow); err == nil {
			cutoff = time.Now().Add(-d)
		} else {
			cutoff = time.Now().Add(-15 * time.Minute) // Default
			opts.TimeWindow = "15m"
		}
	}

	// Get changes from store
	allChanges := store.All()

	// Track unique files and conflicts
	fileAgents := make(map[string]map[string]time.Time) // path -> agent -> last touch time
	uniqueFiles := make(map[string]struct{})

	for _, change := range allChanges {
		// Apply time filter
		if !cutoff.IsZero() && change.Timestamp.Before(cutoff) {
			continue
		}

		// Apply session filter
		if opts.Session != "" && change.Session != opts.Session {
			continue
		}

		// Limit results
		if len(output.Changes) >= opts.Limit {
			break
		}

		record := FileChangeRecord{
			Timestamp: FormatTimestamp(change.Timestamp),
			Path:      change.Change.Path,
			Operation: string(change.Change.Type),
			Agents:    change.Agents,
			Session:   change.Session,
		}

		output.Changes = append(output.Changes, record)
		uniqueFiles[change.Change.Path] = struct{}{}

		// Track agent activity
		for _, agent := range change.Agents {
			output.Summary.ByAgent[agent]++

			// Track for conflict detection
			if fileAgents[change.Change.Path] == nil {
				fileAgents[change.Change.Path] = make(map[string]time.Time)
			}
			existing := fileAgents[change.Change.Path][agent]
			if change.Timestamp.After(existing) {
				fileAgents[change.Change.Path][agent] = change.Timestamp
			}
		}

		// Track operation counts
		output.Summary.ByOperation[string(change.Change.Type)]++
	}

	output.Count = len(output.Changes)
	output.Summary.TotalChanges = len(output.Changes)
	output.Summary.UniqueFiles = len(uniqueFiles)

	// Find most active agent
	maxCount := 0
	for agent, count := range output.Summary.ByAgent {
		if count > maxCount {
			maxCount = count
			output.Summary.MostActiveAgent = agent
		}
	}

	// Detect conflicts (files touched by multiple agents)
	for path, agents := range fileAgents {
		if len(agents) > 1 {
			var agentList []string
			var firstEdit, lastEdit time.Time
			for agent, ts := range agents {
				agentList = append(agentList, agent)
				if firstEdit.IsZero() || ts.Before(firstEdit) {
					firstEdit = ts
				}
				if ts.After(lastEdit) {
					lastEdit = ts
				}
			}

			severity := "warning"
			if len(agentList) >= 3 || lastEdit.Sub(firstEdit) < 10*time.Minute {
				severity = "critical"
			}

			output.Summary.Conflicts = append(output.Summary.Conflicts, FileConflict{
				Path:      path,
				Agents:    agentList,
				Severity:  severity,
				FirstEdit: FormatTimestamp(firstEdit),
				LastEdit:  FormatTimestamp(lastEdit),
			})
		}
	}

	// Generate agent hints
	var warnings []string
	var suggestions []RobotAction

	if len(output.Summary.Conflicts) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d file(s) modified by multiple agents - potential conflicts", len(output.Summary.Conflicts)))
		suggestions = append(suggestions, RobotAction{
			Action:   "review_conflicts",
			Target:   "conflicting files",
			Reason:   "Multiple agents touched the same files",
			Priority: 2,
		})
	}

	if output.Count == 0 {
		output.AgentHints = &AgentHints{
			Summary: fmt.Sprintf("No file changes in the last %s", opts.TimeWindow),
			Notes:   []string{"Use --files-window=all to see all tracked changes"},
		}
	} else {
		output.AgentHints = &AgentHints{
			Summary:          fmt.Sprintf("%d changes to %d files in the last %s", output.Count, output.Summary.UniqueFiles, opts.TimeWindow),
			Warnings:         warnings,
			SuggestedActions: suggestions,
		}
	}

	return output, nil
}

// PrintFiles outputs file changes as JSON.
// This is a thin wrapper around GetFiles() for CLI output.
func PrintFiles(opts FilesOptions) error {
	output, err := GetFiles(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// =============================================================================
// Inspect Pane (--robot-inspect-pane)
// =============================================================================
// Provides detailed inspection of a single pane, equivalent to zooming in
// the TUI dashboard. Includes full output capture, state detection, and context.

const (
	inspectSessionSchemaVersion      = "ntm.robot.inspect_session.v1"
	inspectAgentSchemaVersion        = "ntm.robot.inspect_agent.v1"
	inspectWorkSchemaVersion         = "ntm.robot.inspect_work.v1"
	inspectCoordinationSchemaVersion = "ntm.robot.inspect_coordination.v1"
	inspectQuotaSchemaVersion        = "ntm.robot.inspect_quota.v1"
	inspectIncidentSchemaVersion     = "ntm.robot.inspect_incident.v1"
)

// InspectProjectionInfo describes freshness metadata for projection-backed
// inspect surfaces.
type InspectProjectionInfo struct {
	Fresh        bool     `json:"fresh"`
	CollectedAt  string   `json:"collected_at,omitempty"`
	StaleAfter   string   `json:"stale_after,omitempty"`
	StaleReasons []string `json:"stale_reasons,omitempty"`
}

// InspectHealth summarizes the health of an inspected entity.
type InspectHealth struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// InspectDiagnosticEntry is one machine-readable explanation item attached to an inspect surface.
type InspectDiagnosticEntry struct {
	Code     string   `json:"code"`
	Severity string   `json:"severity,omitempty"`
	Summary  string   `json:"summary"`
	Source   string   `json:"source,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
}

// InspectDiagnosticSection groups related explainability entries for an inspect surface.
type InspectDiagnosticSection struct {
	Kind    string                   `json:"kind"`
	Summary string                   `json:"summary"`
	Entries []InspectDiagnosticEntry `json:"entries"`
}

// InspectAgentDetail is the projection-backed drill-down for one agent.
type InspectAgentDetail struct {
	ID               string                `json:"id"`
	Session          string                `json:"session"`
	Pane             string                `json:"pane"`
	Type             string                `json:"type"`
	Variant          string                `json:"variant,omitempty"`
	TypeConfidence   float64               `json:"type_confidence"`
	TypeMethod       string                `json:"type_method"`
	State            string                `json:"state"`
	StateReason      string                `json:"state_reason,omitempty"`
	PreviousState    string                `json:"previous_state,omitempty"`
	StateChangedAt   string                `json:"state_changed_at,omitempty"`
	LastOutputAt     string                `json:"last_output_at,omitempty"`
	LastOutputAgeSec int                   `json:"last_output_age_sec,omitempty"`
	OutputTailLines  int                   `json:"output_tail_lines,omitempty"`
	CurrentBead      string                `json:"current_bead,omitempty"`
	PendingMail      int                   `json:"pending_mail"`
	AgentMailName    string                `json:"agent_mail_name,omitempty"`
	Health           InspectHealth         `json:"health"`
	Projection       InspectProjectionInfo `json:"projection"`
}

// InspectSessionDetail is the projection-backed drill-down for one session.
type InspectSessionDetail struct {
	Name           string                `json:"name"`
	Label          string                `json:"label,omitempty"`
	ProjectPath    string                `json:"project_path,omitempty"`
	Attached       bool                  `json:"attached"`
	WindowCount    int                   `json:"window_count"`
	PaneCount      int                   `json:"pane_count"`
	AgentCount     int                   `json:"agent_count"`
	ActiveAgents   int                   `json:"active_agents"`
	IdleAgents     int                   `json:"idle_agents"`
	ErrorAgents    int                   `json:"error_agents"`
	CreatedAt      string                `json:"created_at,omitempty"`
	LastAttachedAt string                `json:"last_attached_at,omitempty"`
	LastActivityAt string                `json:"last_activity_at,omitempty"`
	Health         InspectHealth         `json:"health"`
	Projection     InspectProjectionInfo `json:"projection"`
	Agents         []InspectAgentDetail  `json:"agents"`
}

// InspectSessionOutput represents a projection-backed session drill-down.
type InspectSessionOutput struct {
	RobotResponse
	SchemaID      string                     `json:"schema_id"`
	SchemaVersion string                     `json:"schema_version"`
	Session       string                     `json:"session"`
	Detail        InspectSessionDetail       `json:"detail"`
	Diagnostics   []InspectDiagnosticSection `json:"diagnostics"`
	AgentHints    *AgentHints                `json:"_agent_hints,omitempty"`
}

// InspectAgentOutput represents a projection-backed agent drill-down.
type InspectAgentOutput struct {
	RobotResponse
	SchemaID      string                     `json:"schema_id"`
	SchemaVersion string                     `json:"schema_version"`
	AgentID       string                     `json:"agent_id"`
	Detail        InspectAgentDetail         `json:"detail"`
	Diagnostics   []InspectDiagnosticSection `json:"diagnostics"`
	AgentHints    *AgentHints                `json:"_agent_hints,omitempty"`
}

// InspectWorkDetail is the projection-backed drill-down for one work item.
type InspectWorkDetail struct {
	ID              string                       `json:"id"`
	Title           string                       `json:"title"`
	TitleDisclosure *adapters.DisclosureMetadata `json:"title_disclosure,omitempty"`
	Status          string                       `json:"status"`
	Queue           string                       `json:"queue"`
	Priority        int                          `json:"priority"`
	Type            string                       `json:"type,omitempty"`
	Assignee        string                       `json:"assignee,omitempty"`
	ClaimedAt       string                       `json:"claimed_at,omitempty"`
	BlockedByCount  int                          `json:"blocked_by_count"`
	UnblocksCount   int                          `json:"unblocks_count"`
	Labels          []string                     `json:"labels"`
	Score           *float64                     `json:"score,omitempty"`
	ScoreReason     string                       `json:"score_reason,omitempty"`
	Health          InspectHealth                `json:"health"`
	Projection      InspectProjectionInfo        `json:"projection"`
}

// InspectCoordinationDetail is the projection-backed drill-down for one agent's coordination state.
type InspectCoordinationDetail struct {
	AgentName        string                         `json:"agent_name"`
	Session          string                         `json:"session,omitempty"`
	Pane             string                         `json:"pane,omitempty"`
	Mail             adapters.AgentMailStats        `json:"mail"`
	LastMessageAt    string                         `json:"last_message_at,omitempty"`
	LastSentAt       string                         `json:"last_sent_at,omitempty"`
	LastReceivedAt   string                         `json:"last_received_at,omitempty"`
	Handoff          *HandoffSummary                `json:"handoff,omitempty"`
	RelatedIncidents []string                       `json:"related_incidents"`
	Problems         []adapters.CoordinationProblem `json:"problems"`
	Health           InspectHealth                  `json:"health"`
	Projection       InspectProjectionInfo          `json:"projection"`
}

// InspectQuotaDetail is the projection-backed drill-down for one provider/account quota row.
type InspectQuotaDetail struct {
	ID            string                `json:"id"`
	Provider      string                `json:"provider"`
	Account       string                `json:"account"`
	Status        string                `json:"status"`
	ReasonCode    adapters.ReasonCode   `json:"reason_code"`
	UsedPctKnown  bool                  `json:"used_pct_known"`
	UsagePercent  *float64              `json:"usage_percent,omitempty"`
	UsedPctSource string                `json:"used_pct_source,omitempty"`
	LimitHit      bool                  `json:"limit_hit"`
	ResetAt       string                `json:"reset_at,omitempty"`
	IsActive      bool                  `json:"is_active"`
	Healthy       bool                  `json:"healthy"`
	HealthReason  string                `json:"health_reason,omitempty"`
	Health        InspectHealth         `json:"health"`
	Projection    InspectProjectionInfo `json:"projection"`
}

// InspectIncidentDetail is the store-backed drill-down for one incident.
type InspectIncidentDetail struct {
	ID               string        `json:"id"`
	Fingerprint      string        `json:"fingerprint"`
	Title            string        `json:"title"`
	Family           string        `json:"family,omitempty"`
	Category         string        `json:"category,omitempty"`
	Status           string        `json:"status"`
	Severity         string        `json:"severity"`
	SessionNames     []string      `json:"session_names"`
	AgentIDs         []string      `json:"agent_ids"`
	AlertCount       int           `json:"alert_count"`
	EventCount       int           `json:"event_count"`
	FirstEventCursor *int64        `json:"first_event_cursor,omitempty"`
	LastEventCursor  *int64        `json:"last_event_cursor,omitempty"`
	StartedAt        string        `json:"started_at"`
	LastEventAt      string        `json:"last_event_at"`
	AcknowledgedAt   string        `json:"acknowledged_at,omitempty"`
	AcknowledgedBy   string        `json:"acknowledged_by,omitempty"`
	ResolvedAt       string        `json:"resolved_at,omitempty"`
	ResolvedBy       string        `json:"resolved_by,omitempty"`
	MutedAt          string        `json:"muted_at,omitempty"`
	MutedBy          string        `json:"muted_by,omitempty"`
	MutedReason      string        `json:"muted_reason,omitempty"`
	RootCause        string        `json:"root_cause,omitempty"`
	Resolution       string        `json:"resolution,omitempty"`
	Notes            string        `json:"notes,omitempty"`
	Health           InspectHealth `json:"health"`
}

// InspectWorkOutput represents a projection-backed work drill-down.
type InspectWorkOutput struct {
	RobotResponse
	SchemaID      string                     `json:"schema_id"`
	SchemaVersion string                     `json:"schema_version"`
	BeadID        string                     `json:"bead_id"`
	Detail        InspectWorkDetail          `json:"detail"`
	Diagnostics   []InspectDiagnosticSection `json:"diagnostics"`
	AgentHints    *AgentHints                `json:"_agent_hints,omitempty"`
}

// InspectCoordinationOutput represents a projection-backed coordination drill-down.
type InspectCoordinationOutput struct {
	RobotResponse
	SchemaID      string                     `json:"schema_id"`
	SchemaVersion string                     `json:"schema_version"`
	AgentName     string                     `json:"agent_name"`
	Detail        InspectCoordinationDetail  `json:"detail"`
	Diagnostics   []InspectDiagnosticSection `json:"diagnostics"`
	AgentHints    *AgentHints                `json:"_agent_hints,omitempty"`
}

// InspectQuotaOutput represents a projection-backed quota drill-down.
type InspectQuotaOutput struct {
	RobotResponse
	SchemaID      string                     `json:"schema_id"`
	SchemaVersion string                     `json:"schema_version"`
	QuotaID       string                     `json:"quota_id"`
	Detail        InspectQuotaDetail         `json:"detail"`
	Diagnostics   []InspectDiagnosticSection `json:"diagnostics"`
	AgentHints    *AgentHints                `json:"_agent_hints,omitempty"`
}

// InspectIncidentOutput represents a store-backed incident drill-down.
type InspectIncidentOutput struct {
	RobotResponse
	SchemaID      string                     `json:"schema_id"`
	SchemaVersion string                     `json:"schema_version"`
	IncidentID    string                     `json:"incident_id"`
	Detail        InspectIncidentDetail      `json:"detail"`
	Diagnostics   []InspectDiagnosticSection `json:"diagnostics"`
	AgentHints    *AgentHints                `json:"_agent_hints,omitempty"`
}

// InspectSessionOptions configures session inspection.
type InspectSessionOptions struct {
	Session string
}

// InspectAgentOptions configures agent inspection.
type InspectAgentOptions struct {
	AgentID string
}

// InspectWorkOptions configures work inspection.
type InspectWorkOptions struct {
	BeadID string
}

// InspectCoordinationOptions configures coordination inspection.
type InspectCoordinationOptions struct {
	AgentName string
}

// InspectQuotaOptions configures quota inspection.
type InspectQuotaOptions struct {
	QuotaID string
}

// InspectIncidentOptions configures incident inspection.
type InspectIncidentOptions struct {
	IncidentID string
}

// InspectPaneOutput represents detailed pane inspection
type InspectPaneOutput struct {
	RobotResponse
	Session    string             `json:"session"`
	PaneIndex  int                `json:"pane_index"`
	PaneID     string             `json:"pane_id"`
	Agent      InspectPaneAgent   `json:"agent"`
	Output     InspectPaneOutput_ `json:"output"`
	Context    InspectPaneContext `json:"context"`
	AgentHints *AgentHints        `json:"_agent_hints,omitempty"`
}

// InspectPaneAgent contains agent-specific information
type InspectPaneAgent struct {
	Type            string  `json:"type"` // claude, codex, gemini, user
	Variant         string  `json:"variant,omitempty"`
	Title           string  `json:"title"`
	State           string  `json:"state"` // generating, waiting, thinking, error
	StateConfidence float64 `json:"state_confidence"`
	Command         string  `json:"command,omitempty"`
	ProcessRunning  bool    `json:"process_running"`
}

// InspectPaneOutput_ contains the pane output analysis
type InspectPaneOutput_ struct {
	Lines       int             `json:"lines"`                  // Total lines captured
	Characters  int             `json:"characters"`             // Total characters
	LastLines   []string        `json:"last_lines"`             // Last N lines (configurable)
	CodeBlocks  []CodeBlockInfo `json:"code_blocks,omitempty"`  // Detected code blocks
	ErrorsFound []string        `json:"errors_found,omitempty"` // Detected error messages
}

// CodeBlockInfo represents a detected code block in output
type CodeBlockInfo struct {
	Language  string `json:"language,omitempty"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
	FilePath  string `json:"file_path,omitempty"` // Detected target file
}

// InspectPaneContext contains context information
type InspectPaneContext struct {
	WorkingDir     string   `json:"working_dir,omitempty"`
	RecentFiles    []string `json:"recent_files,omitempty"` // Files mentioned in output
	PendingMail    int      `json:"pending_mail"`
	CurrentBead    string   `json:"current_bead,omitempty"`
	ContextPercent float64  `json:"context_percent,omitempty"` // Estimated context usage
}

// InspectPaneOptions configures the inspection
type InspectPaneOptions struct {
	Session     string
	PaneIndex   int
	PaneID      string // Alternative to index
	Lines       int    // Lines to capture (default: 100)
	IncludeCode bool   // Parse code blocks
}

// GetInspectPane returns detailed pane inspection data.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetInspectPane(opts InspectPaneOptions) (*InspectPaneOutput, error) {
	if opts.Lines <= 0 {
		opts.Lines = 100
	}

	// Validate session
	if opts.Session == "" {
		return &InspectPaneOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("session name required"),
				ErrCodeInvalidFlag,
				"Specify session with --robot-inspect-pane=SESSION",
			),
		}, nil
	}

	if !tmux.SessionExists(opts.Session) {
		return &InspectPaneOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("session '%s' not found", opts.Session),
				ErrCodeSessionNotFound,
				"Use 'ntm list' to see available sessions",
			),
			Session: opts.Session,
		}, nil
	}

	// Get panes
	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		return &InspectPaneOutput{
			RobotResponse: NewErrorResponse(err, ErrCodeInternalError, "Failed to get panes"),
			Session:       opts.Session,
		}, nil
	}

	// Find the target pane
	var targetPane *tmux.Pane
	for i := range panes {
		if opts.PaneID != "" && panes[i].ID == opts.PaneID {
			targetPane = &panes[i]
			break
		} else if panes[i].Index == opts.PaneIndex {
			targetPane = &panes[i]
			break
		}
	}

	// If pane index 0 was requested but not found, and there are panes,
	// the user likely wants the first pane but tmux has pane-base-index > 0.
	// Fall back to the first pane in the list.
	if targetPane == nil && opts.PaneIndex == 0 && len(panes) > 0 {
		targetPane = &panes[0]
	}

	if targetPane == nil {
		// Build list of actual valid pane indices for a helpful hint
		var validIndices []string
		for _, p := range panes {
			validIndices = append(validIndices, fmt.Sprintf("%d", p.Index))
		}
		hint := "No panes found"
		if len(validIndices) > 0 {
			hint = fmt.Sprintf("Valid pane indices: %s", strings.Join(validIndices, ", "))
		}
		return &InspectPaneOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("pane %d not found in session '%s'", opts.PaneIndex, opts.Session),
				ErrCodePaneNotFound,
				hint,
			),
			Session:   opts.Session,
			PaneIndex: opts.PaneIndex,
		}, nil
	}

	// Capture output
	captured, captureErr := tmux.CapturePaneOutput(targetPane.ID, opts.Lines)

	output := &InspectPaneOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		PaneIndex:     targetPane.Index,
		PaneID:        targetPane.ID,
	}

	// Populate agent info
	detection := DetectAgentTypeEnhanced(*targetPane, captured)
	output.Agent = InspectPaneAgent{
		Type:            detection.Type,
		Variant:         targetPane.Variant,
		Title:           targetPane.Title,
		Command:         targetPane.Command,
		ProcessRunning:  targetPane.Command != "",
		StateConfidence: detection.Confidence,
	}

	// Detect state from output
	if captureErr == nil {
		lines := splitLines(stripANSI(captured))
		output.Agent.State = detectState(lines, targetPane.Title)

		output.Output.Lines = len(lines)
		output.Output.Characters = len(captured)

		// Get last N lines (up to 50 for reasonable output size)
		lastN := 50
		if len(lines) < lastN {
			lastN = len(lines)
		}
		if lastN > 0 {
			output.Output.LastLines = lines[len(lines)-lastN:]
		}

		// Detect errors in output
		output.Output.ErrorsFound = detectErrors(lines)

		// Parse code blocks if requested
		if opts.IncludeCode {
			output.Output.CodeBlocks = parseCodeBlocks(lines)
		}

		// Extract file references
		output.Context.RecentFiles = extractFileReferences(lines)
	}

	// Generate hints
	var suggestions []RobotAction
	var warnings []string

	switch output.Agent.State {
	case "error":
		warnings = append(warnings, "Agent is in error state")
		suggestions = append(suggestions, RobotAction{
			Action:   "investigate",
			Target:   fmt.Sprintf("pane %d", opts.PaneIndex),
			Reason:   "Error detected in output",
			Priority: 2,
		})
	case "waiting":
		suggestions = append(suggestions, RobotAction{
			Action:   "send_prompt",
			Target:   fmt.Sprintf("pane %d", opts.PaneIndex),
			Reason:   "Agent is idle and ready for work",
			Priority: 1,
		})
	}

	if len(output.Output.ErrorsFound) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d error(s) detected in recent output", len(output.Output.ErrorsFound)))
	}

	output.AgentHints = &AgentHints{
		Summary:          fmt.Sprintf("%s agent in %s state, %d lines of output", output.Agent.Type, output.Agent.State, output.Output.Lines),
		Warnings:         warnings,
		SuggestedActions: suggestions,
	}

	return output, nil
}

// PrintInspectPane outputs detailed pane inspection.
// This is a thin wrapper around GetInspectPane() for CLI output.
func PrintInspectPane(opts InspectPaneOptions) error {
	output, err := GetInspectPane(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

func newInspectSessionOutput(session string) *InspectSessionOutput {
	return &InspectSessionOutput{
		RobotResponse: NewRobotResponse(true),
		SchemaID:      defaultRobotSchemaID("inspect-session"),
		SchemaVersion: inspectSessionSchemaVersion,
		Session:       strings.TrimSpace(session),
		Diagnostics:   []InspectDiagnosticSection{},
		Detail: InspectSessionDetail{
			Agents: []InspectAgentDetail{},
		},
	}
}

func newInspectAgentOutput(agentID string) *InspectAgentOutput {
	return &InspectAgentOutput{
		RobotResponse: NewRobotResponse(true),
		SchemaID:      defaultRobotSchemaID("inspect-agent"),
		SchemaVersion: inspectAgentSchemaVersion,
		AgentID:       strings.TrimSpace(agentID),
		Diagnostics:   []InspectDiagnosticSection{},
	}
}

func newInspectWorkOutput(beadID string) *InspectWorkOutput {
	return &InspectWorkOutput{
		RobotResponse: NewRobotResponse(true),
		SchemaID:      defaultRobotSchemaID("inspect-work"),
		SchemaVersion: inspectWorkSchemaVersion,
		BeadID:        strings.TrimSpace(beadID),
		Diagnostics:   []InspectDiagnosticSection{},
		Detail: InspectWorkDetail{
			Labels: []string{},
		},
	}
}

func newInspectCoordinationOutput(agentName string) *InspectCoordinationOutput {
	return &InspectCoordinationOutput{
		RobotResponse: NewRobotResponse(true),
		SchemaID:      defaultRobotSchemaID("inspect-coordination"),
		SchemaVersion: inspectCoordinationSchemaVersion,
		AgentName:     strings.TrimSpace(agentName),
		Diagnostics:   []InspectDiagnosticSection{},
		Detail: InspectCoordinationDetail{
			RelatedIncidents: []string{},
			Problems:         []adapters.CoordinationProblem{},
		},
	}
}

func newInspectQuotaOutput(quotaID string) *InspectQuotaOutput {
	return &InspectQuotaOutput{
		RobotResponse: NewRobotResponse(true),
		SchemaID:      defaultRobotSchemaID("inspect-quota"),
		SchemaVersion: inspectQuotaSchemaVersion,
		QuotaID:       strings.TrimSpace(quotaID),
		Diagnostics:   []InspectDiagnosticSection{},
	}
}

func newInspectIncidentOutput(incidentID string) *InspectIncidentOutput {
	return &InspectIncidentOutput{
		RobotResponse: NewRobotResponse(true),
		SchemaID:      defaultRobotSchemaID("inspect-incident"),
		SchemaVersion: inspectIncidentSchemaVersion,
		IncidentID:    strings.TrimSpace(incidentID),
		Diagnostics:   []InspectDiagnosticSection{},
		Detail: InspectIncidentDetail{
			SessionNames: []string{},
			AgentIDs:     []string{},
		},
	}
}

func inspectHealth(status state.HealthStatus, reason string) InspectHealth {
	return InspectHealth{
		Status: statusHealthString(status),
		Reason: strings.TrimSpace(reason),
	}
}

func compactDiagnosticEvidence(values ...string) []string {
	evidence := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		evidence = append(evidence, trimmed)
	}
	return evidence
}

func diagnosticTimestamp(label string, ts *time.Time) string {
	if ts == nil || ts.IsZero() {
		return ""
	}
	return fmt.Sprintf("%s=%s", label, FormatTimestamp(*ts))
}

func diagnosticSeverityFromHealth(status string) string {
	switch strings.TrimSpace(status) {
	case "", "healthy":
		return "info"
	default:
		return strings.TrimSpace(status)
	}
}

func inspectProjectionStaleReasons(fresh bool, reasons ...string) []string {
	if fresh {
		return nil
	}
	return compactDiagnosticEvidence(append([]string{"projection freshness window expired"}, reasons...)...)
}

func inspectProjectionInfoWithReasons(collectedAt, staleAfter time.Time, reasons ...string) InspectProjectionInfo {
	return InspectProjectionInfo{
		Fresh:        time.Now().Before(staleAfter),
		CollectedAt:  FormatTimestamp(collectedAt),
		StaleAfter:   FormatTimestamp(staleAfter),
		StaleReasons: compactDiagnosticEvidence(reasons...),
	}
}

func inspectProjectionInfo(collectedAt, staleAfter time.Time) InspectProjectionInfo {
	return inspectProjectionInfoWithReasons(collectedAt, staleAfter)
}

func inspectDiagnosticEntry(code, severity, summary, source string, evidence ...string) InspectDiagnosticEntry {
	return InspectDiagnosticEntry{
		Code:     strings.TrimSpace(code),
		Severity: strings.TrimSpace(severity),
		Summary:  strings.TrimSpace(summary),
		Source:   strings.TrimSpace(source),
		Evidence: compactDiagnosticEvidence(evidence...),
	}
}

func appendInspectDiagnosticSection(sections []InspectDiagnosticSection, section *InspectDiagnosticSection) []InspectDiagnosticSection {
	if section == nil || len(section.Entries) == 0 {
		return sections
	}
	return append(sections, *section)
}

func buildProjectionDiagnosticSection(subject string, projection InspectProjectionInfo, health InspectHealth) *InspectDiagnosticSection {
	trimmedSubject := strings.TrimSpace(subject)
	if trimmedSubject == "" {
		trimmedSubject = "Inspect"
	}
	statusWord := "fresh"
	severity := "info"
	if !projection.Fresh {
		statusWord = "stale"
		severity = "warning"
	}
	evidence := []string{}
	if projection.CollectedAt != "" {
		evidence = append(evidence, fmt.Sprintf("collected_at=%s", projection.CollectedAt))
	}
	if projection.StaleAfter != "" {
		evidence = append(evidence, fmt.Sprintf("stale_after=%s", projection.StaleAfter))
	}
	for _, reason := range projection.StaleReasons {
		evidence = append(evidence, fmt.Sprintf("stale_reason=%s", reason))
	}
	entries := []InspectDiagnosticEntry{
		inspectDiagnosticEntry(
			"projection_freshness",
			severity,
			fmt.Sprintf("%s projection is %s", trimmedSubject, statusWord),
			"",
			evidence...,
		),
	}
	if strings.TrimSpace(health.Reason) != "" {
		entries = append(entries, inspectDiagnosticEntry(
			"health_reason",
			diagnosticSeverityFromHealth(health.Status),
			health.Reason,
			"",
		))
	}
	return &InspectDiagnosticSection{
		Kind:    "projection",
		Summary: fmt.Sprintf("%s projection %s", trimmedSubject, statusWord),
		Entries: entries,
	}
}

func inspectRelevantSourceRows(rows []state.SourceHealth, names ...string) []state.SourceHealth {
	if len(rows) == 0 || len(names) == 0 {
		return nil
	}
	index := make(map[string]state.SourceHealth, len(rows))
	for _, row := range rows {
		name := strings.TrimSpace(row.SourceName)
		if name == "" {
			continue
		}
		index[name] = row
	}
	relevant := make([]state.SourceHealth, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		row, ok := index[trimmed]
		if !ok {
			continue
		}
		relevant = append(relevant, row)
	}
	return relevant
}

func inspectSourceHealthSeverity(row state.SourceHealth) string {
	switch row.Status(normalizedProjectionStaleAfter) {
	case state.SourceStatusUnavailable:
		return "critical"
	case state.SourceStatusStale:
		return "warning"
	default:
		if row.Healthy {
			return "info"
		}
		return "warning"
	}
}

func inspectSourceHealthSummary(row state.SourceHealth) string {
	status := row.Status(normalizedProjectionStaleAfter)
	sourceName := strings.TrimSpace(row.SourceName)
	switch status {
	case state.SourceStatusUnavailable:
		return fmt.Sprintf("%s source unavailable", sourceName)
	case state.SourceStatusStale:
		return fmt.Sprintf("%s source stale", sourceName)
	default:
		if row.Healthy {
			return fmt.Sprintf("%s source healthy", sourceName)
		}
		return fmt.Sprintf("%s source degraded", sourceName)
	}
}

func buildSourceHealthDiagnosticSection(rows []state.SourceHealth, names ...string) *InspectDiagnosticSection {
	relevant := inspectRelevantSourceRows(rows, names...)
	if len(relevant) == 0 {
		return nil
	}
	entries := make([]InspectDiagnosticEntry, 0, len(relevant))
	degraded := 0
	for _, row := range relevant {
		status := row.Status(normalizedProjectionStaleAfter)
		if status != state.SourceStatusFresh {
			degraded++
		}
		evidence := compactDiagnosticEvidence(
			fmt.Sprintf("status=%s", status),
			fmt.Sprintf("available=%t", row.Available),
			fmt.Sprintf("healthy=%t", row.Healthy),
			fmt.Sprintf("last_check_at=%s", FormatTimestamp(row.LastCheckAt)),
			diagnosticTimestamp("last_success_at", row.LastSuccessAt),
			diagnosticTimestamp("last_failure_at", row.LastFailureAt),
			robotFirstNonEmpty("", func() string {
				if strings.TrimSpace(row.Reason) == "" {
					return ""
				}
				return fmt.Sprintf("reason=%s", row.Reason)
			}()),
			robotFirstNonEmpty("", func() string {
				if strings.TrimSpace(row.LastErrorCode) == "" {
					return ""
				}
				return fmt.Sprintf("last_error_code=%s", row.LastErrorCode)
			}()),
			robotFirstNonEmpty("", func() string {
				if strings.TrimSpace(row.LastError) == "" {
					return ""
				}
				return fmt.Sprintf("last_error=%s", row.LastError)
			}()),
		)
		entries = append(entries, inspectDiagnosticEntry(
			"source_health",
			inspectSourceHealthSeverity(row),
			inspectSourceHealthSummary(row),
			row.SourceName,
			evidence...,
		))
	}
	return &InspectDiagnosticSection{
		Kind:    "source_health",
		Summary: fmt.Sprintf("%d relevant source(s), %d degraded", len(relevant), degraded),
		Entries: entries,
	}
}

func buildSessionStateDiagnosticSection(detail InspectSessionDetail) *InspectDiagnosticSection {
	return &InspectDiagnosticSection{
		Kind:    "session_state",
		Summary: fmt.Sprintf("Session %s activity and agent counts", detail.Name),
		Entries: []InspectDiagnosticEntry{
			inspectDiagnosticEntry(
				"session_activity",
				diagnosticSeverityFromHealth(detail.Health.Status),
				fmt.Sprintf("Session %s is %s with %d projected agents", detail.Name, robotFirstNonEmpty(detail.Health.Status, "healthy"), detail.AgentCount),
				"",
				fmt.Sprintf("attached=%t", detail.Attached),
				fmt.Sprintf("window_count=%d", detail.WindowCount),
				fmt.Sprintf("pane_count=%d", detail.PaneCount),
				fmt.Sprintf("agent_count=%d", detail.AgentCount),
				fmt.Sprintf("active_agents=%d", detail.ActiveAgents),
				fmt.Sprintf("idle_agents=%d", detail.IdleAgents),
				fmt.Sprintf("error_agents=%d", detail.ErrorAgents),
				robotFirstNonEmpty("", func() string {
					if detail.LastActivityAt == "" {
						return ""
					}
					return fmt.Sprintf("last_activity_at=%s", detail.LastActivityAt)
				}()),
			),
		},
	}
}

func buildAgentStateDiagnosticSection(detail InspectAgentDetail) *InspectDiagnosticSection {
	entries := []InspectDiagnosticEntry{
		inspectDiagnosticEntry(
			"agent_state",
			diagnosticSeverityFromHealth(detail.Health.Status),
			fmt.Sprintf("Agent %s is %s", detail.ID, detail.State),
			"",
			fmt.Sprintf("type=%s", detail.Type),
			robotFirstNonEmpty("", func() string {
				if detail.StateReason == "" {
					return ""
				}
				return fmt.Sprintf("state_reason=%s", detail.StateReason)
			}()),
			robotFirstNonEmpty("", func() string {
				if detail.PreviousState == "" {
					return ""
				}
				return fmt.Sprintf("previous_state=%s", detail.PreviousState)
			}()),
			robotFirstNonEmpty("", func() string {
				if detail.CurrentBead == "" {
					return ""
				}
				return fmt.Sprintf("current_bead=%s", detail.CurrentBead)
			}()),
			fmt.Sprintf("pending_mail=%d", detail.PendingMail),
			robotFirstNonEmpty("", func() string {
				if detail.LastOutputAt == "" {
					return ""
				}
				return fmt.Sprintf("last_output_at=%s", detail.LastOutputAt)
			}()),
			robotFirstNonEmpty("", func() string {
				if detail.LastOutputAgeSec == 0 {
					return ""
				}
				return fmt.Sprintf("last_output_age_sec=%d", detail.LastOutputAgeSec)
			}()),
		),
	}
	if detail.OutputTailLines > 0 {
		entries = append(entries, inspectDiagnosticEntry(
			"output_tail_bounded",
			"info",
			fmt.Sprintf("Agent output is bounded to the last %d line(s) in the runtime projection", detail.OutputTailLines),
			"",
			fmt.Sprintf("output_tail_lines=%d", detail.OutputTailLines),
		))
	}
	return &InspectDiagnosticSection{
		Kind:    "agent_state",
		Summary: fmt.Sprintf("Agent %s projected state", detail.ID),
		Entries: entries,
	}
}

func disclosureDiagnosticEntry(code, label string, meta *adapters.DisclosureMetadata) *InspectDiagnosticEntry {
	if meta == nil {
		return nil
	}
	entry := inspectDiagnosticEntry(
		code,
		"info",
		fmt.Sprintf("%s disclosure is %s", strings.TrimSpace(label), robotFirstNonEmpty(strings.TrimSpace(meta.DisclosureState), "visible")),
		"",
		robotFirstNonEmpty("", func() string {
			if strings.TrimSpace(meta.DisclosureState) == "" {
				return ""
			}
			return fmt.Sprintf("disclosure_state=%s", strings.TrimSpace(meta.DisclosureState))
		}()),
		robotFirstNonEmpty("", func() string {
			if strings.TrimSpace(meta.Preview) == "" {
				return ""
			}
			return fmt.Sprintf("preview=%s", strings.TrimSpace(meta.Preview))
		}()),
		robotFirstNonEmpty("", func() string {
			if strings.TrimSpace(meta.RedactionMode) == "" {
				return ""
			}
			return fmt.Sprintf("redaction_mode=%s", strings.TrimSpace(meta.RedactionMode))
		}()),
		robotFirstNonEmpty("", func() string {
			if meta.Findings == 0 {
				return ""
			}
			return fmt.Sprintf("findings=%d", meta.Findings)
		}()),
	)
	return &entry
}

func buildWorkStateDiagnosticSection(detail InspectWorkDetail) *InspectDiagnosticSection {
	entries := []InspectDiagnosticEntry{
		inspectDiagnosticEntry(
			"queue_state",
			diagnosticSeverityFromHealth(detail.Health.Status),
			fmt.Sprintf("Work %s is %s", detail.ID, detail.Queue),
			"",
			fmt.Sprintf("status=%s", detail.Status),
			fmt.Sprintf("priority=%d", detail.Priority),
			fmt.Sprintf("blocked_by_count=%d", detail.BlockedByCount),
			fmt.Sprintf("unblocks_count=%d", detail.UnblocksCount),
			robotFirstNonEmpty("", func() string {
				if detail.Assignee == "" {
					return ""
				}
				return fmt.Sprintf("assignee=%s", detail.Assignee)
			}()),
			robotFirstNonEmpty("", func() string {
				if detail.Score == nil {
					return ""
				}
				return fmt.Sprintf("score=%.2f", *detail.Score)
			}()),
			robotFirstNonEmpty("", func() string {
				if detail.ScoreReason == "" {
					return ""
				}
				return fmt.Sprintf("score_reason=%s", detail.ScoreReason)
			}()),
		),
	}
	if entry := disclosureDiagnosticEntry("title_disclosure", "work title", detail.TitleDisclosure); entry != nil {
		entries = append(entries, *entry)
	}
	return &InspectDiagnosticSection{
		Kind:    "work_state",
		Summary: fmt.Sprintf("Work %s queue and disclosure evidence", detail.ID),
		Entries: entries,
	}
}

func buildCoordinationProblemDiagnosticSection(detail InspectCoordinationDetail) *InspectDiagnosticSection {
	entries := make([]InspectDiagnosticEntry, 0, len(detail.Problems)+4)
	for _, problem := range detail.Problems {
		entries = append(entries, inspectDiagnosticEntry(
			problem.Kind,
			problem.Severity,
			problem.Summary,
			"",
			robotFirstNonEmpty("", func() string {
				if len(problem.Agents) == 0 {
					return ""
				}
				return fmt.Sprintf("agents=%s", strings.Join(problem.Agents, ","))
			}()),
			robotFirstNonEmpty("", func() string {
				if len(problem.ThreadIDs) == 0 {
					return ""
				}
				return fmt.Sprintf("thread_ids=%s", strings.Join(problem.ThreadIDs, ","))
			}()),
			robotFirstNonEmpty("", func() string {
				if len(problem.Paths) == 0 {
					return ""
				}
				return fmt.Sprintf("paths=%s", strings.Join(problem.Paths, ","))
			}()),
		))
	}
	if entry := disclosureDiagnosticEntry("latest_subject_disclosure", "latest mail subject", detail.Mail.LatestSubjectDisclosure); entry != nil {
		entries = append(entries, *entry)
	}
	if entry := disclosureDiagnosticEntry("latest_preview_disclosure", "latest mail preview", detail.Mail.LatestPreviewDisclosure); entry != nil {
		entries = append(entries, *entry)
	}
	if detail.Handoff != nil {
		entries = append(entries, inspectDiagnosticEntry(
			"handoff_state",
			diagnosticSeverityFromHealth(detail.Health.Status),
			fmt.Sprintf("Latest handoff for %s is %s", detail.Handoff.Session, robotFirstNonEmpty(detail.Handoff.Status, "present")),
			"",
			robotFirstNonEmpty("", func() string {
				if detail.Handoff.UpdatedAt == "" {
					return ""
				}
				return fmt.Sprintf("updated_at=%s", detail.Handoff.UpdatedAt)
			}()),
			robotFirstNonEmpty("", func() string {
				if len(detail.Handoff.Blockers) == 0 {
					return ""
				}
				return fmt.Sprintf("blockers=%s", strings.Join(detail.Handoff.Blockers, ","))
			}()),
		))
	}
	if len(entries) == 0 {
		entries = append(entries, inspectDiagnosticEntry(
			"coordination_clear",
			"info",
			"No projected coordination problems are active",
			"",
		))
	}
	return &InspectDiagnosticSection{
		Kind:    "coordination_problems",
		Summary: fmt.Sprintf("Coordination evidence for %s", detail.AgentName),
		Entries: entries,
	}
}

func buildQuotaStateDiagnosticSection(detail InspectQuotaDetail) *InspectDiagnosticSection {
	return &InspectDiagnosticSection{
		Kind:    "quota_state",
		Summary: fmt.Sprintf("Quota %s usage and limit evidence", detail.ID),
		Entries: []InspectDiagnosticEntry{
			inspectDiagnosticEntry(
				"quota_status",
				diagnosticSeverityFromHealth(detail.Health.Status),
				fmt.Sprintf("Quota %s is %s", detail.ID, detail.Status),
				"",
				fmt.Sprintf("reason_code=%s", detail.ReasonCode),
				fmt.Sprintf("used_pct_known=%t", detail.UsedPctKnown),
				robotFirstNonEmpty("", func() string {
					if detail.UsagePercent == nil {
						return ""
					}
					return fmt.Sprintf("usage_percent=%.2f", *detail.UsagePercent)
				}()),
				robotFirstNonEmpty("", func() string {
					if detail.UsedPctSource == "" {
						return ""
					}
					return fmt.Sprintf("used_pct_source=%s", detail.UsedPctSource)
				}()),
				fmt.Sprintf("limit_hit=%t", detail.LimitHit),
				fmt.Sprintf("is_active=%t", detail.IsActive),
				robotFirstNonEmpty("", func() string {
					if detail.ResetAt == "" {
						return ""
					}
					return fmt.Sprintf("reset_at=%s", detail.ResetAt)
				}()),
				robotFirstNonEmpty("", func() string {
					if detail.HealthReason == "" {
						return ""
					}
					return fmt.Sprintf("health_reason=%s", detail.HealthReason)
				}()),
			),
		},
	}
}

func buildIncidentEvidenceDiagnosticSection(detail InspectIncidentDetail) *InspectDiagnosticSection {
	entries := []InspectDiagnosticEntry{
		inspectDiagnosticEntry(
			"incident_state",
			diagnosticSeverityFromHealth(detail.Health.Status),
			fmt.Sprintf("Incident %s is %s with %s severity", detail.ID, detail.Status, detail.Severity),
			"",
			fmt.Sprintf("alert_count=%d", detail.AlertCount),
			fmt.Sprintf("event_count=%d", detail.EventCount),
			robotFirstNonEmpty("", func() string {
				if detail.StartedAt == "" {
					return ""
				}
				return fmt.Sprintf("started_at=%s", detail.StartedAt)
			}()),
			robotFirstNonEmpty("", func() string {
				if detail.LastEventAt == "" {
					return ""
				}
				return fmt.Sprintf("last_event_at=%s", detail.LastEventAt)
			}()),
			robotFirstNonEmpty("", func() string {
				if detail.FirstEventCursor == nil {
					return ""
				}
				return fmt.Sprintf("first_event_cursor=%d", *detail.FirstEventCursor)
			}()),
			robotFirstNonEmpty("", func() string {
				if detail.LastEventCursor == nil {
					return ""
				}
				return fmt.Sprintf("last_event_cursor=%d", *detail.LastEventCursor)
			}()),
			robotFirstNonEmpty("", func() string {
				if len(detail.SessionNames) == 0 {
					return ""
				}
				return fmt.Sprintf("session_names=%s", strings.Join(detail.SessionNames, ","))
			}()),
			robotFirstNonEmpty("", func() string {
				if len(detail.AgentIDs) == 0 {
					return ""
				}
				return fmt.Sprintf("agent_ids=%s", strings.Join(detail.AgentIDs, ","))
			}()),
		),
	}
	if detail.MutedReason != "" {
		entries = append(entries, inspectDiagnosticEntry("muted_reason", "warning", detail.MutedReason, ""))
	}
	if detail.RootCause != "" {
		entries = append(entries, inspectDiagnosticEntry("root_cause", "info", detail.RootCause, ""))
	}
	if detail.Resolution != "" {
		entries = append(entries, inspectDiagnosticEntry("resolution", "info", detail.Resolution, ""))
	}
	if detail.Notes != "" {
		entries = append(entries, inspectDiagnosticEntry("notes", "info", detail.Notes, ""))
	}
	return &InspectDiagnosticSection{
		Kind:    "incident_evidence",
		Summary: fmt.Sprintf("Incident evidence for %s", detail.ID),
		Entries: entries,
	}
}

func buildAttentionStateDiagnosticSection(cursor *int64, itemState *state.AttentionItemState, replayWindow state.AttentionReplayWindow) *InspectDiagnosticSection {
	if cursor == nil && itemState == nil {
		return nil
	}
	entries := make([]InspectDiagnosticEntry, 0, 2)
	if cursor != nil {
		summary := fmt.Sprintf("Attention cursor %d is within the retained replay window", *cursor)
		severity := "info"
		evidence := []string{
			fmt.Sprintf("replay_oldest_cursor=%d", replayWindow.OldestCursor),
			fmt.Sprintf("replay_newest_cursor=%d", replayWindow.NewestCursor),
			fmt.Sprintf("replay_event_count=%d", replayWindow.EventCount),
		}
		switch {
		case replayWindow.EventCount == 0 || replayWindow.NewestCursor == 0:
			summary = fmt.Sprintf("Attention cursor %d cannot be validated because no retained replay window is available", *cursor)
			severity = "warning"
		case replayWindow.CursorExpired(*cursor):
			summary = fmt.Sprintf("Attention cursor %d has expired from the retained replay window", *cursor)
			severity = "warning"
		case itemState == nil:
			summary = fmt.Sprintf("Attention cursor %d is retained but has no durable operator-state record", *cursor)
		}
		if replayWindow.LastEventAt != nil {
			evidence = append(evidence, diagnosticTimestamp("replay_last_event_at", replayWindow.LastEventAt))
		}
		entries = append(entries, inspectDiagnosticEntry("cursor_window", severity, summary, "", evidence...))
	}
	if itemState != nil {
		entries = append(entries, inspectDiagnosticEntry(
			"attention_state",
			"info",
			fmt.Sprintf("Attention state is %s", itemState.State),
			"",
			fmt.Sprintf("item_key=%s", itemState.ItemKey),
			robotFirstNonEmpty("", func() string {
				if itemState.DedupKey == "" {
					return ""
				}
				return fmt.Sprintf("dedup_key=%s", itemState.DedupKey)
			}()),
			fmt.Sprintf("pinned=%t", itemState.Pinned),
			diagnosticTimestamp("pinned_at", itemState.PinnedAt),
			robotFirstNonEmpty("", func() string {
				if itemState.PinnedBy == "" {
					return ""
				}
				return fmt.Sprintf("pinned_by=%s", itemState.PinnedBy)
			}()),
			fmt.Sprintf("muted=%t", itemState.Muted),
			diagnosticTimestamp("muted_at", itemState.MutedAt),
			robotFirstNonEmpty("", func() string {
				if itemState.MutedBy == "" {
					return ""
				}
				return fmt.Sprintf("muted_by=%s", itemState.MutedBy)
			}()),
			diagnosticTimestamp("acknowledged_at", itemState.AcknowledgedAt),
			robotFirstNonEmpty("", func() string {
				if itemState.AcknowledgedBy == "" {
					return ""
				}
				return fmt.Sprintf("acknowledged_by=%s", itemState.AcknowledgedBy)
			}()),
			diagnosticTimestamp("snoozed_until", itemState.SnoozedUntil),
			diagnosticTimestamp("dismissed_at", itemState.DismissedAt),
			robotFirstNonEmpty("", func() string {
				if itemState.DismissedBy == "" {
					return ""
				}
				return fmt.Sprintf("dismissed_by=%s", itemState.DismissedBy)
			}()),
			robotFirstNonEmpty("", func() string {
				if itemState.OverridePriority == "" {
					return ""
				}
				return fmt.Sprintf("override_priority=%s", itemState.OverridePriority)
			}()),
			robotFirstNonEmpty("", func() string {
				if itemState.OverrideReason == "" {
					return ""
				}
				return fmt.Sprintf("override_reason=%s", itemState.OverrideReason)
			}()),
			diagnosticTimestamp("override_expires_at", itemState.OverrideExpiresAt),
			robotFirstNonEmpty("", func() string {
				if itemState.ResurfacingPolicy == "" {
					return ""
				}
				return fmt.Sprintf("resurfacing_policy=%s", itemState.ResurfacingPolicy)
			}()),
			fmt.Sprintf("created_at=%s", FormatTimestamp(itemState.CreatedAt)),
			fmt.Sprintf("updated_at=%s", FormatTimestamp(itemState.UpdatedAt)),
		))
	}
	return &InspectDiagnosticSection{
		Kind:    "attention_state",
		Summary: "Attention operator-state and cursor evidence",
		Entries: entries,
	}
}

func buildSessionDiagnostics(detail InspectSessionDetail, sourceRows []state.SourceHealth) []InspectDiagnosticSection {
	diagnostics := make([]InspectDiagnosticSection, 0, 3)
	diagnostics = appendInspectDiagnosticSection(diagnostics, buildProjectionDiagnosticSection("Session", detail.Projection, detail.Health))
	diagnostics = appendInspectDiagnosticSection(diagnostics, buildSessionStateDiagnosticSection(detail))
	relevantSources := []string{"tmux"}
	for _, agent := range detail.Agents {
		if agent.PendingMail > 0 || agent.AgentMailName != "" {
			relevantSources = append(relevantSources, "mail")
			break
		}
	}
	diagnostics = appendInspectDiagnosticSection(diagnostics, buildSourceHealthDiagnosticSection(sourceRows, relevantSources...))
	return diagnostics
}

func buildAgentDiagnostics(detail InspectAgentDetail, sourceRows []state.SourceHealth) []InspectDiagnosticSection {
	diagnostics := make([]InspectDiagnosticSection, 0, 3)
	diagnostics = appendInspectDiagnosticSection(diagnostics, buildProjectionDiagnosticSection("Agent", detail.Projection, detail.Health))
	diagnostics = appendInspectDiagnosticSection(diagnostics, buildAgentStateDiagnosticSection(detail))
	relevantSources := []string{"tmux"}
	if detail.PendingMail > 0 || detail.AgentMailName != "" {
		relevantSources = append(relevantSources, "mail")
	}
	diagnostics = appendInspectDiagnosticSection(diagnostics, buildSourceHealthDiagnosticSection(sourceRows, relevantSources...))
	return diagnostics
}

func buildWorkDiagnostics(detail InspectWorkDetail, sourceRows []state.SourceHealth) []InspectDiagnosticSection {
	diagnostics := make([]InspectDiagnosticSection, 0, 3)
	diagnostics = appendInspectDiagnosticSection(diagnostics, buildProjectionDiagnosticSection("Work", detail.Projection, detail.Health))
	diagnostics = appendInspectDiagnosticSection(diagnostics, buildWorkStateDiagnosticSection(detail))
	diagnostics = appendInspectDiagnosticSection(diagnostics, buildSourceHealthDiagnosticSection(sourceRows, "beads"))
	return diagnostics
}

func buildCoordinationDiagnostics(detail InspectCoordinationDetail, sourceRows []state.SourceHealth) []InspectDiagnosticSection {
	diagnostics := make([]InspectDiagnosticSection, 0, 3)
	diagnostics = appendInspectDiagnosticSection(diagnostics, buildProjectionDiagnosticSection("Coordination", detail.Projection, detail.Health))
	diagnostics = appendInspectDiagnosticSection(diagnostics, buildCoordinationProblemDiagnosticSection(detail))
	diagnostics = appendInspectDiagnosticSection(diagnostics, buildSourceHealthDiagnosticSection(sourceRows, "mail"))
	return diagnostics
}

func buildQuotaDiagnostics(detail InspectQuotaDetail, sourceRows []state.SourceHealth) []InspectDiagnosticSection {
	diagnostics := make([]InspectDiagnosticSection, 0, 3)
	diagnostics = appendInspectDiagnosticSection(diagnostics, buildProjectionDiagnosticSection("Quota", detail.Projection, detail.Health))
	diagnostics = appendInspectDiagnosticSection(diagnostics, buildQuotaStateDiagnosticSection(detail))
	diagnostics = appendInspectDiagnosticSection(diagnostics, buildSourceHealthDiagnosticSection(sourceRows, detail.Provider))
	return diagnostics
}

func buildIncidentDiagnostics(detail InspectIncidentDetail, itemState *state.AttentionItemState, replayWindow state.AttentionReplayWindow) []InspectDiagnosticSection {
	diagnostics := make([]InspectDiagnosticSection, 0, 2)
	diagnostics = appendInspectDiagnosticSection(diagnostics, buildIncidentEvidenceDiagnosticSection(detail))
	diagnostics = appendInspectDiagnosticSection(diagnostics, buildAttentionStateDiagnosticSection(detail.LastEventCursor, itemState, replayWindow))
	return diagnostics
}

func inspectAgentDetailFromRuntime(agent state.RuntimeAgent) InspectAgentDetail {
	return InspectAgentDetail{
		ID:               strings.TrimSpace(agent.ID),
		Session:          strings.TrimSpace(agent.SessionName),
		Pane:             strings.TrimSpace(agent.Pane),
		Type:             strings.TrimSpace(agent.AgentType),
		Variant:          strings.TrimSpace(agent.Variant),
		TypeConfidence:   agent.TypeConfidence,
		TypeMethod:       strings.TrimSpace(agent.TypeMethod),
		State:            strings.TrimSpace(string(agent.State)),
		StateReason:      strings.TrimSpace(agent.StateReason),
		PreviousState:    strings.TrimSpace(agent.PreviousState),
		StateChangedAt:   FormatTimestampPtr(agent.StateChangedAt),
		LastOutputAt:     FormatTimestampPtr(agent.LastOutputAt),
		LastOutputAgeSec: agent.LastOutputAgeSec,
		OutputTailLines:  agent.OutputTailLines,
		CurrentBead:      strings.TrimSpace(agent.CurrentBead),
		PendingMail:      agent.PendingMail,
		AgentMailName:    strings.TrimSpace(agent.AgentMailName),
		Health:           inspectHealth(agent.HealthStatus, agent.HealthReason),
		Projection:       inspectProjectionInfoWithReasons(agent.CollectedAt, agent.StaleAfter, inspectProjectionStaleReasons(agent.IsFresh(), agent.HealthReason)...),
	}
}

func inspectSessionDetailFromRuntime(sess *state.RuntimeSession, agents []state.RuntimeAgent) InspectSessionDetail {
	detail := InspectSessionDetail{
		Name:           strings.TrimSpace(sess.Name),
		Label:          strings.TrimSpace(sess.Label),
		ProjectPath:    strings.TrimSpace(sess.ProjectPath),
		Attached:       sess.Attached,
		WindowCount:    sess.WindowCount,
		PaneCount:      sess.PaneCount,
		AgentCount:     sess.AgentCount,
		ActiveAgents:   sess.ActiveAgents,
		IdleAgents:     sess.IdleAgents,
		ErrorAgents:    sess.ErrorAgents,
		CreatedAt:      FormatTimestampPtr(sess.CreatedAt),
		LastAttachedAt: FormatTimestampPtr(sess.LastAttachedAt),
		LastActivityAt: FormatTimestampPtr(sess.LastActivityAt),
		Health:         inspectHealth(sess.HealthStatus, sess.HealthReason),
		Projection:     inspectProjectionInfoWithReasons(sess.CollectedAt, sess.StaleAfter, inspectProjectionStaleReasons(sess.IsFresh(), sess.HealthReason)...),
		Agents:         make([]InspectAgentDetail, 0, len(agents)),
	}
	for _, agent := range agents {
		detail.Agents = append(detail.Agents, inspectAgentDetailFromRuntime(agent))
	}
	if len(detail.Agents) > 0 {
		detail.AgentCount = len(detail.Agents)
	}
	return detail
}

func inspectWorkQueue(row state.RuntimeWork) string {
	switch {
	case row.Status == "in_progress":
		return "in_progress"
	case row.Status == "closed":
		return "closed"
	case row.BlockedByCount > 0:
		return "blocked"
	default:
		return "ready"
	}
}

func inspectWorkHealth(row state.RuntimeWork) InspectHealth {
	if !row.IsFresh() {
		return InspectHealth{
			Status: "warning",
			Reason: "Work projection is stale; refresh snapshot/status if you need the latest state",
		}
	}
	switch inspectWorkQueue(row) {
	case "blocked":
		return InspectHealth{
			Status: "warning",
			Reason: fmt.Sprintf("Blocked by %d upstream issue(s)", row.BlockedByCount),
		}
	case "in_progress":
		reason := "Actively in progress"
		if assignee := strings.TrimSpace(row.Assignee); assignee != "" {
			reason = fmt.Sprintf("Claimed by %s", assignee)
		}
		return InspectHealth{Status: "healthy", Reason: reason}
	case "closed":
		return InspectHealth{Status: "healthy", Reason: "Closed"}
	default:
		return InspectHealth{Status: "healthy", Reason: "Ready to claim"}
	}
}

func inspectWorkDetailFromRuntime(row *state.RuntimeWork) InspectWorkDetail {
	item := snapshotWorkItemFromRuntime(*row)
	labels := item.Labels
	if labels == nil {
		labels = []string{}
	}
	return InspectWorkDetail{
		ID:              item.ID,
		Title:           item.Title,
		TitleDisclosure: item.TitleDisclosure,
		Status:          strings.TrimSpace(row.Status),
		Queue:           inspectWorkQueue(*row),
		Priority:        row.Priority,
		Type:            strings.TrimSpace(row.BeadType),
		Assignee:        strings.TrimSpace(row.Assignee),
		ClaimedAt:       FormatTimestampPtr(row.ClaimedAt),
		BlockedByCount:  row.BlockedByCount,
		UnblocksCount:   row.UnblocksCount,
		Labels:          labels,
		Score:           item.Score,
		ScoreReason:     strings.TrimSpace(row.ScoreReason),
		Health:          inspectWorkHealth(*row),
		Projection:      inspectProjectionInfoWithReasons(row.CollectedAt, row.StaleAfter, inspectProjectionStaleReasons(row.IsFresh())...),
	}
}

func inspectCoordinationMailFromRuntime(row state.RuntimeCoordination) adapters.AgentMailStats {
	return adapters.AgentMailStats{
		Unread:                  row.UnreadCount,
		Pending:                 row.PendingAckCount,
		Urgent:                  row.UrgentCount,
		Pane:                    strings.TrimSpace(row.Pane),
		LatestMessage:           robotFirstNonEmpty(strings.TrimSpace(row.LastMessageSubject), strings.TrimSpace(row.LastMessagePreview)),
		LatestSubject:           strings.TrimSpace(row.LastMessageSubject),
		LatestSubjectDisclosure: decodeDisclosureMetadata(row.LastMessageSubjectDisclosure),
		LatestPreview:           strings.TrimSpace(row.LastMessagePreview),
		LatestPreviewDisclosure: decodeDisclosureMetadata(row.LastMessagePreviewDisclosure),
	}
}

func incidentMatchesCoordination(incident state.Incident, row state.RuntimeCoordination) bool {
	agentName := strings.TrimSpace(row.AgentName)
	sessionName := strings.TrimSpace(row.SessionName)
	for _, candidate := range decodeStringList(incident.AgentIDs) {
		if strings.TrimSpace(candidate) == agentName {
			return true
		}
	}
	if sessionName == "" {
		return false
	}
	for _, candidate := range decodeStringList(incident.SessionNames) {
		if strings.TrimSpace(candidate) == sessionName {
			return true
		}
	}
	return false
}

func relatedIncidentIDsForCoordination(incidents []state.Incident, row state.RuntimeCoordination) []string {
	related := make([]string, 0)
	seen := make(map[string]struct{})
	for _, incident := range incidents {
		if !incidentMatchesCoordination(incident, row) {
			continue
		}
		id := strings.TrimSpace(incident.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		related = append(related, id)
	}
	return related
}

func inspectCoordinationProblems(row state.RuntimeCoordination, handoff *state.RuntimeHandoff, incidentIDs []string) []adapters.CoordinationProblem {
	problems := make([]adapters.CoordinationProblem, 0)
	agentName := strings.TrimSpace(row.AgentName)
	if row.UrgentCount > 0 {
		problems = append(problems, adapters.CoordinationProblem{
			Kind:     "urgent_mail",
			Severity: "error",
			Summary:  fmt.Sprintf("%d urgent message(s) need attention", row.UrgentCount),
			Agents:   []string{agentName},
		})
	}
	if row.PendingAckCount > 0 {
		problems = append(problems, adapters.CoordinationProblem{
			Kind:     "pending_ack",
			Severity: "warning",
			Summary:  fmt.Sprintf("%d message(s) still require acknowledgement", row.PendingAckCount),
			Agents:   []string{agentName},
		})
	}
	if row.UnreadCount >= 5 {
		problems = append(problems, adapters.CoordinationProblem{
			Kind:     "mail_backlog",
			Severity: "warning",
			Summary:  fmt.Sprintf("Unread mail backlog is %d messages", row.UnreadCount),
			Agents:   []string{agentName},
		})
	}
	if handoff != nil && strings.TrimSpace(handoff.SessionName) == strings.TrimSpace(row.SessionName) {
		blockers := decodeStringList(handoff.Blockers)
		if len(blockers) > 0 {
			problems = append(problems, adapters.CoordinationProblem{
				Kind:      "handoff_blockers",
				Severity:  "warning",
				Summary:   fmt.Sprintf("Latest handoff for %s reports %d blocker(s)", strings.TrimSpace(row.SessionName), len(blockers)),
				Agents:    []string{agentName},
				ThreadIDs: decodeStringList(handoff.AgentMailThreads),
			})
		}
	}
	if len(incidentIDs) > 0 {
		problems = append(problems, adapters.CoordinationProblem{
			Kind:      "related_incidents",
			Severity:  "error",
			Summary:   fmt.Sprintf("%d open incident(s) match this agent or session", len(incidentIDs)),
			Agents:    []string{agentName},
			ThreadIDs: append([]string(nil), incidentIDs...),
		})
	}
	return problems
}

func inspectCoordinationHealth(row state.RuntimeCoordination, handoff *state.RuntimeHandoff, incidentIDs []string) InspectHealth {
	switch {
	case !row.IsFresh():
		return InspectHealth{
			Status: "warning",
			Reason: "Coordination projection is stale; refresh snapshot/status if you need the latest state",
		}
	case len(incidentIDs) > 0:
		return InspectHealth{
			Status: "critical",
			Reason: fmt.Sprintf("%d open incident(s) are linked to this coordination surface", len(incidentIDs)),
		}
	case row.UrgentCount > 0:
		return InspectHealth{
			Status: "critical",
			Reason: fmt.Sprintf("%d urgent message(s) need attention", row.UrgentCount),
		}
	case row.PendingAckCount > 0:
		return InspectHealth{
			Status: "warning",
			Reason: fmt.Sprintf("%d acknowledgement(s) are still pending", row.PendingAckCount),
		}
	case row.UnreadCount >= 5:
		return InspectHealth{
			Status: "warning",
			Reason: fmt.Sprintf("Unread mail backlog is %d messages", row.UnreadCount),
		}
	case handoff != nil && strings.TrimSpace(handoff.SessionName) == strings.TrimSpace(row.SessionName) && len(decodeStringList(handoff.Blockers)) > 0:
		return InspectHealth{
			Status: "warning",
			Reason: "Latest session handoff still carries blockers",
		}
	default:
		return InspectHealth{
			Status: "healthy",
			Reason: "No urgent coordination pressure detected",
		}
	}
}

func inspectCoordinationDetailFromRuntime(row *state.RuntimeCoordination, handoff *state.RuntimeHandoff, incidents []state.Incident) InspectCoordinationDetail {
	relatedIncidents := relatedIncidentIDsForCoordination(incidents, *row)
	detail := InspectCoordinationDetail{
		AgentName:        strings.TrimSpace(row.AgentName),
		Session:          strings.TrimSpace(row.SessionName),
		Pane:             strings.TrimSpace(row.Pane),
		Mail:             inspectCoordinationMailFromRuntime(*row),
		LastMessageAt:    FormatTimestampPtr(row.LastMessageAt),
		LastSentAt:       FormatTimestampPtr(row.LastSentAt),
		LastReceivedAt:   FormatTimestampPtr(row.LastReceivedAt),
		RelatedIncidents: relatedIncidents,
		Problems:         []adapters.CoordinationProblem{},
		Health:           inspectCoordinationHealth(*row, handoff, relatedIncidents),
		Projection:       inspectProjectionInfoWithReasons(row.CollectedAt, row.StaleAfter, inspectProjectionStaleReasons(row.IsFresh())...),
	}
	if handoff != nil && strings.TrimSpace(handoff.SessionName) == detail.Session {
		detail.Handoff = statusHandoffFromRuntime(handoff)
	}
	detail.Problems = inspectCoordinationProblems(*row, handoff, relatedIncidents)
	return detail
}

func canonicalInspectQuotaID(provider, account string) string {
	provider = strings.TrimSpace(provider)
	account = strings.TrimSpace(account)
	if provider == "" {
		return account
	}
	if account == "" {
		return provider
	}
	return provider + "/" + account
}

func parseInspectQuotaID(value string) (string, string) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", ""
	}
	if strings.Contains(trimmed, "/") {
		parts := strings.SplitN(trimmed, "/", 2)
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	}
	if strings.Contains(trimmed, ":") {
		parts := strings.SplitN(trimmed, ":", 2)
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	}
	return "", ""
}

func inspectQuotaHealth(row state.RuntimeQuota) InspectHealth {
	reasonCode := snapshotQuotaReasonCode(row)
	switch reasonCode {
	case adapters.ReasonQuotaExceededTokens, adapters.ReasonQuotaExceededRequests,
		adapters.ReasonQuotaExceededCost, adapters.ReasonQuotaSuspended:
		return InspectHealth{
			Status: "critical",
			Reason: robotFirstNonEmpty(strings.TrimSpace(row.HealthReason), fmt.Sprintf("Quota status %s", snapshotQuotaStatus(row))),
		}
	case adapters.ReasonQuotaCriticalTokens, adapters.ReasonQuotaCriticalRequests,
		adapters.ReasonQuotaWarningTokens, adapters.ReasonQuotaWarningRequests,
		adapters.ReasonQuotaWarningRateLimit, adapters.ReasonQuotaUnavailable:
		return InspectHealth{
			Status: "warning",
			Reason: robotFirstNonEmpty(strings.TrimSpace(row.HealthReason), fmt.Sprintf("Quota status %s", snapshotQuotaStatus(row))),
		}
	default:
		return InspectHealth{
			Status: "healthy",
			Reason: robotFirstNonEmpty(strings.TrimSpace(row.HealthReason), "Quota healthy"),
		}
	}
}

func inspectQuotaDetailFromRuntime(row *state.RuntimeQuota) InspectQuotaDetail {
	detail := InspectQuotaDetail{
		ID:            canonicalInspectQuotaID(row.Provider, row.Account),
		Provider:      strings.TrimSpace(row.Provider),
		Account:       strings.TrimSpace(row.Account),
		Status:        snapshotQuotaStatus(*row),
		ReasonCode:    snapshotQuotaReasonCode(*row),
		UsedPctKnown:  row.UsedPctKnown,
		UsedPctSource: strings.TrimSpace(string(row.UsedPctSource)),
		LimitHit:      row.LimitHit,
		ResetAt:       FormatTimestampPtr(row.ResetsAt),
		IsActive:      row.IsActive,
		Healthy:       row.Healthy,
		HealthReason:  strings.TrimSpace(row.HealthReason),
		Health:        inspectQuotaHealth(*row),
		Projection:    inspectProjectionInfoWithReasons(row.CollectedAt, row.StaleAfter, inspectProjectionStaleReasons(row.IsFresh(), row.HealthReason)...),
	}
	if row.UsedPctKnown {
		usagePercent := row.UsedPct
		detail.UsagePercent = &usagePercent
	}
	return detail
}

func inspectIncidentHealth(incident *state.Incident) InspectHealth {
	status := strings.TrimSpace(string(incident.Status))
	severity := strings.TrimSpace(string(incident.Severity))
	switch incident.Status {
	case state.IncidentStatusResolved:
		return InspectHealth{Status: "healthy", Reason: "Incident resolved"}
	case state.IncidentStatusMuted:
		return InspectHealth{Status: "warning", Reason: robotFirstNonEmpty(strings.TrimSpace(incident.MutedReason), "Incident muted")}
	case state.IncidentStatusInvestigating:
		return InspectHealth{Status: "warning", Reason: fmt.Sprintf("Incident investigating with %s severity", severity)}
	default:
		switch severity {
		case "critical", "error":
			return InspectHealth{Status: "critical", Reason: fmt.Sprintf("Incident %s with %s severity", status, severity)}
		case "warning":
			return InspectHealth{Status: "warning", Reason: fmt.Sprintf("Incident %s with warning severity", status)}
		default:
			return InspectHealth{Status: "healthy", Reason: fmt.Sprintf("Incident %s", robotFirstNonEmpty(status, "open"))}
		}
	}
}

func inspectIncidentDetailFromState(incident *state.Incident) InspectIncidentDetail {
	sessionNames := decodeStringList(incident.SessionNames)
	if sessionNames == nil {
		sessionNames = []string{}
	}
	agentIDs := decodeStringList(incident.AgentIDs)
	if agentIDs == nil {
		agentIDs = []string{}
	}
	return InspectIncidentDetail{
		ID:               strings.TrimSpace(incident.ID),
		Fingerprint:      strings.TrimSpace(incident.Fingerprint),
		Title:            strings.TrimSpace(incident.Title),
		Family:           strings.TrimSpace(incident.Family),
		Category:         strings.TrimSpace(incident.Category),
		Status:           strings.TrimSpace(string(incident.Status)),
		Severity:         strings.TrimSpace(string(incident.Severity)),
		SessionNames:     sessionNames,
		AgentIDs:         agentIDs,
		AlertCount:       incident.AlertCount,
		EventCount:       incident.EventCount,
		FirstEventCursor: incident.FirstEventCursor,
		LastEventCursor:  incident.LastEventCursor,
		StartedAt:        FormatTimestamp(incident.StartedAt),
		LastEventAt:      FormatTimestamp(incident.LastEventAt),
		AcknowledgedAt:   FormatTimestampPtr(incident.AcknowledgedAt),
		AcknowledgedBy:   strings.TrimSpace(incident.AcknowledgedBy),
		ResolvedAt:       FormatTimestampPtr(incident.ResolvedAt),
		ResolvedBy:       strings.TrimSpace(incident.ResolvedBy),
		MutedAt:          FormatTimestampPtr(incident.MutedAt),
		MutedBy:          strings.TrimSpace(incident.MutedBy),
		MutedReason:      strings.TrimSpace(incident.MutedReason),
		RootCause:        strings.TrimSpace(incident.RootCause),
		Resolution:       strings.TrimSpace(incident.Resolution),
		Notes:            strings.TrimSpace(incident.Notes),
		Health:           inspectIncidentHealth(incident),
	}
}

// GetInspectSession returns projection-backed session detail.
func GetInspectSession(opts InspectSessionOptions) (*InspectSessionOutput, error) {
	output := newInspectSessionOutput(opts.Session)
	if strings.TrimSpace(opts.Session) == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session name required"),
			ErrCodeInvalidFlag,
			"Specify session with --robot-inspect-session=SESSION",
		)
		return output, nil
	}

	store := currentProjectionStore()
	if store == nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("runtime projection store unavailable"),
			ErrCodeNotImplemented,
			"Projection-backed inspect requires the shared runtime store; refresh snapshot/status after projection initialization",
		)
		return output, nil
	}

	session, err := store.GetRuntimeSession(strings.TrimSpace(opts.Session))
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Failed to load session projection")
		return output, nil
	}
	if session == nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session '%s' not found in runtime projection", strings.TrimSpace(opts.Session)),
			ErrCodeSessionNotFound,
			"Use ntm --robot-snapshot or ntm --robot-status to list projected sessions",
		)
		return output, nil
	}

	agents, err := store.GetRuntimeAgentsBySession(session.Name)
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Failed to load session agent projections")
		return output, nil
	}

	output.Session = session.Name
	output.Detail = inspectSessionDetailFromRuntime(session, agents)
	sourceRows, _ := store.GetAllSourceHealth()
	output.Diagnostics = buildSessionDiagnostics(output.Detail, sourceRows)

	var warnings []string
	var notes []string
	if !output.Detail.Projection.Fresh {
		warnings = append(warnings, "Session projection is stale; refresh snapshot/status if you need the latest state")
	}
	if len(output.Detail.Agents) == 0 {
		notes = append(notes, "No fresh agent projections are attached to this session")
	} else {
		notes = append(notes, "Use returned agent ids with --robot-inspect-agent for per-agent drill-down")
	}

	output.AgentHints = &AgentHints{
		Summary:  fmt.Sprintf("Session %s: %d projected agents, health %s", output.Detail.Name, len(output.Detail.Agents), output.Detail.Health.Status),
		Warnings: warnings,
		Notes:    notes,
	}
	return output, nil
}

// PrintInspectSession outputs projection-backed session inspection.
func PrintInspectSession(opts InspectSessionOptions) error {
	output, err := GetInspectSession(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// GetInspectAgent returns projection-backed agent detail.
func GetInspectAgent(opts InspectAgentOptions) (*InspectAgentOutput, error) {
	output := newInspectAgentOutput(opts.AgentID)
	if strings.TrimSpace(opts.AgentID) == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("agent id required"),
			ErrCodeInvalidFlag,
			"Specify agent id with --robot-inspect-agent=SESSION:PANE",
		)
		return output, nil
	}

	store := currentProjectionStore()
	if store == nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("runtime projection store unavailable"),
			ErrCodeNotImplemented,
			"Projection-backed inspect requires the shared runtime store; refresh snapshot/status after projection initialization",
		)
		return output, nil
	}

	agent, err := store.GetRuntimeAgent(strings.TrimSpace(opts.AgentID))
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Failed to load agent projection")
		return output, nil
	}
	if agent == nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("agent '%s' not found in runtime projection", strings.TrimSpace(opts.AgentID)),
			ErrCodeInvalidFlag,
			"Use ntm --robot-inspect-session=SESSION to list stable agent ids for drill-down",
		)
		return output, nil
	}

	output.AgentID = strings.TrimSpace(agent.ID)
	output.Detail = inspectAgentDetailFromRuntime(*agent)
	sourceRows, _ := store.GetAllSourceHealth()
	output.Diagnostics = buildAgentDiagnostics(output.Detail, sourceRows)

	var warnings []string
	var notes []string
	if !output.Detail.Projection.Fresh {
		warnings = append(warnings, "Agent projection is stale; refresh snapshot/status if you need the latest state")
	}
	if output.Detail.CurrentBead != "" {
		notes = append(notes, fmt.Sprintf("Current bead: %s", output.Detail.CurrentBead))
	}
	notes = append(notes, fmt.Sprintf("Session drill-down: ntm --robot-inspect-session=%s", output.Detail.Session))

	output.AgentHints = &AgentHints{
		Summary:  fmt.Sprintf("Agent %s: %s %s", output.Detail.ID, output.Detail.Type, output.Detail.State),
		Warnings: warnings,
		Notes:    notes,
	}
	return output, nil
}

// PrintInspectAgent outputs projection-backed agent inspection.
func PrintInspectAgent(opts InspectAgentOptions) error {
	output, err := GetInspectAgent(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// GetInspectWork returns projection-backed work detail.
func GetInspectWork(opts InspectWorkOptions) (*InspectWorkOutput, error) {
	output := newInspectWorkOutput(opts.BeadID)
	if strings.TrimSpace(opts.BeadID) == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("bead id required"),
			ErrCodeInvalidFlag,
			"Specify bead id with --robot-inspect-work=BEAD_ID",
		)
		return output, nil
	}

	store := currentProjectionStore()
	if store == nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("runtime projection store unavailable"),
			ErrCodeNotImplemented,
			"Projection-backed inspect requires the shared runtime store; refresh snapshot/status after projection initialization",
		)
		return output, nil
	}

	work, err := store.GetRuntimeWork(strings.TrimSpace(opts.BeadID))
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Failed to load work projection")
		return output, nil
	}
	if work == nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("work item '%s' not found in runtime projection", strings.TrimSpace(opts.BeadID)),
			ErrCodeInvalidFlag,
			"Use ntm --robot-snapshot to list projected bead ids for drill-down",
		)
		return output, nil
	}

	output.BeadID = strings.TrimSpace(work.BeadID)
	output.Detail = inspectWorkDetailFromRuntime(work)
	sourceRows, _ := store.GetAllSourceHealth()
	output.Diagnostics = buildWorkDiagnostics(output.Detail, sourceRows)

	var warnings []string
	var notes []string
	if !output.Detail.Projection.Fresh {
		warnings = append(warnings, "Work projection is stale; refresh snapshot/status if you need the latest state")
	}
	switch output.Detail.Queue {
	case "blocked":
		notes = append(notes, fmt.Sprintf("Blocked by %d upstream issue(s)", output.Detail.BlockedByCount))
	case "in_progress":
		if output.Detail.Assignee != "" {
			notes = append(notes, fmt.Sprintf("Assigned to %s", output.Detail.Assignee))
		}
	default:
		notes = append(notes, fmt.Sprintf("Queue state: %s", output.Detail.Queue))
	}

	output.AgentHints = &AgentHints{
		Summary:  fmt.Sprintf("Work %s: %s (%s)", output.Detail.ID, output.Detail.Title, output.Detail.Queue),
		Warnings: warnings,
		Notes:    notes,
	}
	return output, nil
}

// PrintInspectWork outputs projection-backed work inspection.
func PrintInspectWork(opts InspectWorkOptions) error {
	output, err := GetInspectWork(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// GetInspectCoordination returns projection-backed coordination detail.
func GetInspectCoordination(opts InspectCoordinationOptions) (*InspectCoordinationOutput, error) {
	output := newInspectCoordinationOutput(opts.AgentName)
	if strings.TrimSpace(opts.AgentName) == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("agent name required"),
			ErrCodeInvalidFlag,
			"Specify Agent Mail identity with --robot-inspect-coordination=AGENT_NAME",
		)
		return output, nil
	}

	store := currentProjectionStore()
	if store == nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("runtime projection store unavailable"),
			ErrCodeNotImplemented,
			"Projection-backed inspect requires the shared runtime store; refresh snapshot/status after projection initialization",
		)
		return output, nil
	}

	coord, err := store.GetRuntimeCoordination(strings.TrimSpace(opts.AgentName))
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Failed to load coordination projection")
		return output, nil
	}
	if coord == nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("coordination state for '%s' not found in runtime projection", strings.TrimSpace(opts.AgentName)),
			ErrCodeInvalidFlag,
			"Use ntm --robot-inspect-agent=SESSION:PANE to discover Agent Mail identity for coordination drill-down",
		)
		return output, nil
	}

	handoff, handoffErr := store.GetRuntimeHandoff()
	if handoffErr != nil {
		output.RobotResponse = NewErrorResponse(handoffErr, ErrCodeInternalError, "Failed to load runtime handoff projection")
		return output, nil
	}
	incidents, incidentErr := store.ListOpenIncidents()
	if incidentErr != nil {
		output.RobotResponse = NewErrorResponse(incidentErr, ErrCodeInternalError, "Failed to load related incidents")
		return output, nil
	}

	output.AgentName = strings.TrimSpace(coord.AgentName)
	output.Detail = inspectCoordinationDetailFromRuntime(coord, handoff, incidents)
	sourceRows, _ := store.GetAllSourceHealth()
	output.Diagnostics = buildCoordinationDiagnostics(output.Detail, sourceRows)

	var warnings []string
	var notes []string
	if !output.Detail.Projection.Fresh {
		warnings = append(warnings, "Coordination projection is stale; refresh snapshot/status if you need the latest state")
	}
	if len(output.Detail.RelatedIncidents) > 0 {
		notes = append(notes, fmt.Sprintf("Related incidents: %s", strings.Join(output.Detail.RelatedIncidents, ", ")))
	}
	if output.Detail.Handoff != nil {
		notes = append(notes, fmt.Sprintf("Latest handoff session: %s", output.Detail.Handoff.Session))
	}
	if len(notes) == 0 {
		notes = append(notes, "Reservation and conflict escalation remain visible through incidents and attention until they are projected directly")
	}

	output.AgentHints = &AgentHints{
		Summary:  fmt.Sprintf("Coordination %s: unread=%d urgent=%d pending_ack=%d", output.Detail.AgentName, output.Detail.Mail.Unread, output.Detail.Mail.Urgent, output.Detail.Mail.Pending),
		Warnings: warnings,
		Notes:    notes,
	}
	return output, nil
}

// PrintInspectCoordination outputs projection-backed coordination inspection.
func PrintInspectCoordination(opts InspectCoordinationOptions) error {
	output, err := GetInspectCoordination(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// GetInspectQuota returns projection-backed quota detail.
func GetInspectQuota(opts InspectQuotaOptions) (*InspectQuotaOutput, error) {
	output := newInspectQuotaOutput(opts.QuotaID)
	provider, account := parseInspectQuotaID(opts.QuotaID)
	if provider == "" || account == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("quota id required"),
			ErrCodeInvalidFlag,
			"Specify quota identity with --robot-inspect-quota=PROVIDER/ACCOUNT",
		)
		return output, nil
	}

	store := currentProjectionStore()
	if store == nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("runtime projection store unavailable"),
			ErrCodeNotImplemented,
			"Projection-backed inspect requires the shared runtime store; refresh snapshot/status after projection initialization",
		)
		return output, nil
	}

	quota, err := store.GetRuntimeQuota(provider, account)
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Failed to load quota projection")
		return output, nil
	}
	if quota == nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("quota '%s' not found in runtime projection", canonicalInspectQuotaID(provider, account)),
			ErrCodeInvalidFlag,
			"Use ntm --robot-snapshot to list projected provider/account quota identities",
		)
		return output, nil
	}

	output.QuotaID = canonicalInspectQuotaID(quota.Provider, quota.Account)
	output.Detail = inspectQuotaDetailFromRuntime(quota)
	sourceRows, _ := store.GetAllSourceHealth()
	output.Diagnostics = buildQuotaDiagnostics(output.Detail, sourceRows)

	var warnings []string
	var notes []string
	if !output.Detail.Projection.Fresh {
		warnings = append(warnings, "Quota projection is stale; refresh snapshot/status if you need the latest state")
	}
	if output.Detail.ResetAt != "" {
		notes = append(notes, fmt.Sprintf("Next reset at %s", output.Detail.ResetAt))
	}
	if output.Detail.HealthReason != "" {
		notes = append(notes, output.Detail.HealthReason)
	}

	output.AgentHints = &AgentHints{
		Summary:  fmt.Sprintf("Quota %s: %s", output.Detail.ID, output.Detail.Status),
		Warnings: warnings,
		Notes:    notes,
	}
	return output, nil
}

// PrintInspectQuota outputs projection-backed quota inspection.
func PrintInspectQuota(opts InspectQuotaOptions) error {
	output, err := GetInspectQuota(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// GetInspectIncident returns store-backed incident detail.
func GetInspectIncident(opts InspectIncidentOptions) (*InspectIncidentOutput, error) {
	output := newInspectIncidentOutput(opts.IncidentID)
	if strings.TrimSpace(opts.IncidentID) == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("incident id required"),
			ErrCodeInvalidFlag,
			"Specify incident id with --robot-inspect-incident=INCIDENT_ID",
		)
		return output, nil
	}

	store := currentProjectionStore()
	if store == nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("runtime projection store unavailable"),
			ErrCodeNotImplemented,
			"Incident drill-down requires the shared store; refresh snapshot/status after projection initialization",
		)
		return output, nil
	}

	incident, err := store.GetIncident(strings.TrimSpace(opts.IncidentID))
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Failed to load incident detail")
		return output, nil
	}
	if incident == nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("incident '%s' not found", strings.TrimSpace(opts.IncidentID)),
			ErrCodeInvalidFlag,
			"Use ntm --robot-snapshot or ntm --robot-attention to list active incident ids",
		)
		return output, nil
	}

	output.IncidentID = strings.TrimSpace(incident.ID)
	output.Detail = inspectIncidentDetailFromState(incident)
	var replayWindow state.AttentionReplayWindow
	var attentionState *state.AttentionItemState
	if output.Detail.LastEventCursor != nil {
		replayWindow, _ = store.GetAttentionReplayWindow()
		attentionState, _ = store.GetAttentionItemStateByCursor(*output.Detail.LastEventCursor)
	}
	output.Diagnostics = buildIncidentDiagnostics(output.Detail, attentionState, replayWindow)

	var notes []string
	if output.Detail.LastEventCursor != nil {
		notes = append(notes, fmt.Sprintf("Last attention/event cursor: %d", *output.Detail.LastEventCursor))
	}
	if output.Detail.ResolvedAt != "" {
		notes = append(notes, fmt.Sprintf("Resolved at %s", output.Detail.ResolvedAt))
	}

	output.AgentHints = &AgentHints{
		Summary: fmt.Sprintf("Incident %s: %s %s", output.Detail.ID, output.Detail.Status, output.Detail.Severity),
		Notes:   notes,
	}
	return output, nil
}

// PrintInspectIncident outputs store-backed incident inspection.
func PrintInspectIncident(opts InspectIncidentOptions) error {
	output, err := GetInspectIncident(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// =============================================================================
// Metrics Export (--robot-metrics)
// =============================================================================
// Exports session metrics in various formats for analysis

// MetricsOutput represents comprehensive session metrics
type MetricsOutput struct {
	RobotResponse
	Session      string                  `json:"session,omitempty"`
	Period       string                  `json:"period"` // e.g., "last_24h", "all_time"
	TokenUsage   MetricsTokenUsage       `json:"token_usage"`
	AgentStats   map[string]AgentMetrics `json:"agent_stats"`
	SessionStats MetricsSessionStats     `json:"session_stats"`
	AgentHints   *AgentHints             `json:"_agent_hints,omitempty"`
}

// MetricsTokenUsage contains token consumption data
type MetricsTokenUsage struct {
	TotalTokens    int64            `json:"total_tokens"`
	TotalCost      float64          `json:"total_cost_usd"`
	ByAgent        map[string]int64 `json:"by_agent"`
	ByModel        map[string]int64 `json:"by_model"`
	ContextCurrent map[string]int   `json:"context_current_percent"` // Current context usage per agent
}

// AgentMetrics contains per-agent statistics
type AgentMetrics struct {
	Type            string  `json:"type"`
	PromptsReceived int     `json:"prompts_received"`
	TokensUsed      int64   `json:"tokens_used"`
	AvgResponseTime float64 `json:"avg_response_time_sec"`
	ErrorCount      int     `json:"error_count"`
	RestartCount    int     `json:"restart_count"`
	Uptime          string  `json:"uptime"`
}

// MetricsSessionStats contains session-level statistics
type MetricsSessionStats struct {
	TotalPrompts    int    `json:"total_prompts"`
	TotalAgents     int    `json:"total_agents"`
	ActiveAgents    int    `json:"active_agents"`
	SessionDuration string `json:"session_duration"`
	FilesChanged    int    `json:"files_changed"`
	Commits         int    `json:"commits,omitempty"`
}

// MetricsOptions configures the metrics export
type MetricsOptions struct {
	Session string // Filter to specific session
	Period  string // "1h", "24h", "7d", "all" (default: "24h")
	Format  string // "json", "csv" (default: "json")
}

// GetMetrics returns session metrics data.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetMetrics(opts MetricsOptions) (*MetricsOutput, error) {
	if opts.Period == "" {
		opts.Period = "24h"
	}

	output := &MetricsOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		Period:        opts.Period,
		TokenUsage: MetricsTokenUsage{
			ByAgent:        make(map[string]int64),
			ByModel:        make(map[string]int64),
			ContextCurrent: make(map[string]int),
		},
		AgentStats: make(map[string]AgentMetrics),
	}

	// Get session info if specified
	if opts.Session != "" {
		if !tmux.SessionExists(opts.Session) {
			return &MetricsOutput{
				RobotResponse: NewErrorResponse(
					fmt.Errorf("session '%s' not found", opts.Session),
					ErrCodeSessionNotFound,
					"Use 'ntm list' to see available sessions",
				),
				Session: opts.Session,
				Period:  opts.Period,
				TokenUsage: MetricsTokenUsage{
					ByAgent:        make(map[string]int64),
					ByModel:        make(map[string]int64),
					ContextCurrent: make(map[string]int),
				},
				AgentStats: make(map[string]AgentMetrics),
			}, nil
		}

		panes, err := tmux.GetPanes(opts.Session)
		if err == nil {
			output.SessionStats.TotalAgents = len(panes)

			for _, pane := range panes {
				agentType := string(pane.Type)
				if agentType == "" || agentType == "unknown" {
					continue
				}

				if _, exists := output.AgentStats[pane.Title]; !exists {
					output.AgentStats[pane.Title] = AgentMetrics{
						Type: agentType,
					}
				}
				output.SessionStats.ActiveAgents++
			}
		}
	}

	// Get file change count
	fileStore := tracker.GlobalFileChanges
	if fileStore != nil {
		changes := fileStore.All()
		uniqueFiles := make(map[string]struct{})
		for _, c := range changes {
			if opts.Session == "" || c.Session == opts.Session {
				uniqueFiles[c.Change.Path] = struct{}{}
			}
		}
		output.SessionStats.FilesChanged = len(uniqueFiles)
	}

	sessionDesc := opts.Session
	if sessionDesc == "" {
		sessionDesc = "all sessions"
	}
	output.AgentHints = &AgentHints{
		Summary: fmt.Sprintf("Metrics for %s over %s", sessionDesc, opts.Period),
		Notes:   []string{"Token usage requires integration with provider APIs for accurate data"},
	}

	return output, nil
}

// PrintMetrics outputs session metrics.
// This is a thin wrapper around GetMetrics() for CLI output.
func PrintMetrics(opts MetricsOptions) error {
	output, err := GetMetrics(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// =============================================================================
// Replay Command (--robot-replay)
// =============================================================================
// Replays a command from history, equivalent to the History panel replay action

// ReplayOutput represents the result of a replay operation
type ReplayOutput struct {
	RobotResponse
	HistoryID   string      `json:"history_id"`
	OriginalCmd string      `json:"original_command"`
	Session     string      `json:"session"`
	TargetPanes []int       `json:"target_panes"`
	Replayed    bool        `json:"replayed"`
	AgentHints  *AgentHints `json:"_agent_hints,omitempty"`
}

// ReplayOptions configures the replay operation
type ReplayOptions struct {
	Session   string
	HistoryID string // History entry ID to replay
	DryRun    bool   // Just show what would be replayed
}

// GetReplay returns replay operation result.
// This function returns the data struct directly, enabling CLI/REST parity.
// Note: When DryRun is false, this executes the replay and returns nil (send handles output).
func GetReplay(opts ReplayOptions) (*ReplayOutput, error) {
	targetSession := strings.TrimSpace(opts.Session)
	output := &ReplayOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       targetSession,
		HistoryID:     opts.HistoryID,
		TargetPanes:   []int{},
	}

	// Get history entries
	entries, err := history.ReadRecent(100)
	if err != nil {
		return &ReplayOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("history tracking not available: %w", err),
				ErrCodeDependencyMissing,
				"History is recorded during send operations",
			),
			HistoryID:   opts.HistoryID,
			TargetPanes: []int{},
		}, nil
	}

	// Find the history entry
	var target *history.HistoryEntry
	for i := range entries {
		if entries[i].ID == opts.HistoryID {
			target = &entries[i]
			break
		}
	}

	if target == nil {
		return &ReplayOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("history entry '%s' not found", opts.HistoryID),
				ErrCodeInvalidFlag,
				"Use --robot-history to see available entries",
			),
			HistoryID:   opts.HistoryID,
			TargetPanes: []int{},
		}, nil
	}

	if targetSession == "" {
		targetSession = target.Session
		output.Session = targetSession
	}

	output.OriginalCmd = target.Prompt
	// Convert string targets to int
	for _, t := range target.Targets {
		// Targets are stored as strings, but we expose as ints
		var idx int
		if _, err := fmt.Sscanf(t, "%d", &idx); err == nil {
			output.TargetPanes = append(output.TargetPanes, idx)
		}
	}

	if opts.DryRun {
		output.Replayed = false
		output.AgentHints = &AgentHints{
			Summary: fmt.Sprintf("Would replay: %s", truncateString(target.Prompt, 50)),
			Notes:   []string{"Use without --replay-dry-run to execute"},
		}
		return output, nil
	}

	// Execute the replay by calling send logic
	// Build pane filter from original targets
	paneFilter := append([]string{}, target.Targets...)

	sendOpts := SendOptions{
		Session: targetSession,
		Message: target.Prompt,
		Panes:   paneFilter,
	}

	// Execute the send - GetSend handles the actual operation
	sendOutput, err := GetSend(sendOpts)
	if err != nil {
		return nil, err
	}

	// Return nil to signal that PrintReplay should use send output directly
	// This is a special case where we delegate to another command
	_ = sendOutput // Used by PrintReplay
	return nil, nil
}

// PrintReplay outputs replay operation result.
// This is a thin wrapper around GetReplay() for CLI output.
func PrintReplay(opts ReplayOptions) error {
	output, err := GetReplay(opts)
	if err != nil {
		return err
	}
	// nil output means GetReplay delegated to send
	if output == nil {
		// Execute send directly for non-dry-run case
		entries, _ := history.ReadRecent(100)
		for i := range entries {
			if entries[i].ID == opts.HistoryID {
				targetSession := strings.TrimSpace(opts.Session)
				if targetSession == "" {
					targetSession = entries[i].Session
				}
				sendOpts := SendOptions{
					Session: targetSession,
					Message: entries[i].Prompt,
					Panes:   append([]string{}, entries[i].Targets...),
				}
				return PrintSend(sendOpts)
			}
		}
	}
	return encodeJSON(output)
}

// =============================================================================
// Palette Info (--robot-palette)
// =============================================================================
// Queries command palette information - available commands, favorites, recents

// PaletteOutput represents palette state and available commands
type PaletteOutput struct {
	RobotResponse
	Session    string          `json:"session,omitempty"`
	Commands   []PaletteCmd    `json:"commands"`
	Favorites  []string        `json:"favorites"`
	Pinned     []string        `json:"pinned"`
	Recent     []PaletteRecent `json:"recent"`
	Categories []string        `json:"categories"`
	AgentHints *AgentHints     `json:"_agent_hints,omitempty"`
}

// PaletteCmd represents a single palette command
type PaletteCmd struct {
	Key        string   `json:"key"`
	Label      string   `json:"label"`
	Category   string   `json:"category"`
	Prompt     string   `json:"prompt"`
	Targets    string   `json:"targets,omitempty"` // "all", "claude", etc.
	IsFavorite bool     `json:"is_favorite"`
	IsPinned   bool     `json:"is_pinned"`
	UseCount   int      `json:"use_count"`
	Tags       []string `json:"tags,omitempty"`
}

// PaletteRecent represents a recently used command
type PaletteRecent struct {
	Key     string `json:"key"`
	UsedAt  string `json:"used_at"` // RFC3339
	Session string `json:"session"`
	Success bool   `json:"success"`
}

// PaletteOptions configures the palette query
type PaletteOptions struct {
	Session     string // Filter recents to session
	Category    string // Filter commands by category
	SearchQuery string // Filter commands by search term
}

// GetPalette returns palette information.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetPalette(cfg *config.Config, opts PaletteOptions) (*PaletteOutput, error) {
	if cfg == nil {
		cfg = config.Default()
	}

	favoriteSet := make(map[string]struct{}, len(cfg.PaletteState.Favorites))
	for _, key := range cfg.PaletteState.Favorites {
		key = strings.TrimSpace(key)
		if key != "" {
			favoriteSet[key] = struct{}{}
		}
	}
	pinnedSet := make(map[string]struct{}, len(cfg.PaletteState.Pinned))
	for _, key := range cfg.PaletteState.Pinned {
		key = strings.TrimSpace(key)
		if key != "" {
			pinnedSet[key] = struct{}{}
		}
	}

	output := &PaletteOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		Commands:      []PaletteCmd{},
		Favorites:     []string{},
		Pinned:        []string{},
		Recent:        []PaletteRecent{},
		Categories:    []string{},
	}

	seen := make(map[string]struct{})
	visibleKeys := make(map[string]struct{})
	categorySeen := make(map[string]struct{})
	query := strings.ToLower(opts.SearchQuery)
	matchesFilters := func(key, label, category string) bool {
		if opts.Category != "" && category != opts.Category {
			return false
		}
		if opts.SearchQuery == "" {
			return true
		}
		return strings.Contains(strings.ToLower(label), query) ||
			strings.Contains(strings.ToLower(key), query)
	}

	// Get commands from config
	for _, cmd := range cfg.Palette {
		if !matchesFilters(cmd.Key, cmd.Label, cmd.Category) {
			continue
		}

		palCmd := PaletteCmd{
			Key:        cmd.Key,
			Label:      cmd.Label,
			Category:   cmd.Category,
			Prompt:     cmd.Prompt,
			Tags:       cmd.Tags,
			IsFavorite: hasPaletteKey(favoriteSet, cmd.Key),
			IsPinned:   hasPaletteKey(pinnedSet, cmd.Key),
		}

		output.Commands = append(output.Commands, palCmd)
		if cmd.Key != "" {
			seen[cmd.Key] = struct{}{}
			visibleKeys[cmd.Key] = struct{}{}
		}
		if category := strings.TrimSpace(cmd.Category); category != "" {
			if _, exists := categorySeen[category]; !exists {
				categorySeen[category] = struct{}{}
				output.Categories = append(output.Categories, category)
			}
		}
	}

	for _, cmd := range kernel.List() {
		key := strings.TrimSpace(cmd.Name)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}

		label := strings.TrimSpace(cmd.Description)
		if label == "" {
			label = key
		}
		category := strings.TrimSpace(cmd.Category)
		if category == "" {
			category = "kernel"
		}

		if !matchesFilters(key, label, category) {
			continue
		}

		// Use the command's description as the prompt text sent to agent panes.
		// NEVER use Examples[0].Command — examples are CLI documentation strings
		// that may contain dangerous arguments (e.g. "rm -rf /") and must not be
		// sent verbatim to AI agents. See ntm#56.
		prompt := strings.TrimSpace(cmd.Description)
		if prompt == "" {
			prompt = label
		}

		palCmd := PaletteCmd{
			Key:        key,
			Label:      label,
			Category:   category,
			Prompt:     prompt,
			Tags:       []string{"kernel"},
			IsFavorite: hasPaletteKey(favoriteSet, key),
			IsPinned:   hasPaletteKey(pinnedSet, key),
		}

		output.Commands = append(output.Commands, palCmd)
		seen[key] = struct{}{}
		visibleKeys[key] = struct{}{}
		if _, exists := categorySeen[category]; !exists {
			categorySeen[category] = struct{}{}
			output.Categories = append(output.Categories, category)
		}
	}

	// Inject built-in xf search command when xf integration is enabled
	if cfg.Integrations.XF.Enabled {
		xfKey := "xf-search"
		if _, exists := seen[xfKey]; !exists {
			if matchesFilters(xfKey, "XF Archive Search", "xf") {
				output.Commands = append(output.Commands, PaletteCmd{
					Key:        xfKey,
					Label:      "XF Archive Search",
					Category:   "xf",
					Prompt:     "Search X/Twitter archive (Ctrl+K)",
					Tags:       []string{"xf", "search", "twitter"},
					IsFavorite: hasPaletteKey(favoriteSet, xfKey),
					IsPinned:   hasPaletteKey(pinnedSet, xfKey),
				})
				seen[xfKey] = struct{}{}
				visibleKeys[xfKey] = struct{}{}
				if _, exists := categorySeen["xf"]; !exists {
					categorySeen["xf"] = struct{}{}
					output.Categories = append(output.Categories, "xf")
				}
			}
		}
	}

	for _, key := range cfg.PaletteState.Favorites {
		key = strings.TrimSpace(key)
		if _, ok := visibleKeys[key]; ok {
			output.Favorites = append(output.Favorites, key)
		}
	}
	for _, key := range cfg.PaletteState.Pinned {
		key = strings.TrimSpace(key)
		if _, ok := visibleKeys[key]; ok {
			output.Pinned = append(output.Pinned, key)
		}
	}

	entries, err := history.ReadRecent(200)
	if err != nil {
		output.AgentHints = &AgentHints{
			Summary:  fmt.Sprintf("%d commands available across %d categories", len(output.Commands), len(output.Categories)),
			Warnings: []string{fmt.Sprintf("palette history unavailable: %v", err)},
			Notes:    []string{"Use --robot-send with a prompt to send commands to agents"},
		}
		return output, nil
	}

	useCounts := make(map[string]int)
	recentSeen := make(map[string]struct{})
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if entry.Source != history.SourcePalette {
			continue
		}
		if opts.Session != "" && entry.Session != opts.Session {
			continue
		}
		key := strings.TrimSpace(entry.Template)
		if _, ok := visibleKeys[key]; !ok {
			continue
		}
		useCounts[key]++
		if _, exists := recentSeen[key]; exists {
			continue
		}
		recentSeen[key] = struct{}{}
		output.Recent = append(output.Recent, PaletteRecent{
			Key:     key,
			UsedAt:  FormatTimestamp(entry.Timestamp),
			Session: entry.Session,
			Success: entry.Success,
		})
		if len(output.Recent) >= 8 {
			break
		}
	}

	for i := range output.Commands {
		output.Commands[i].UseCount = useCounts[output.Commands[i].Key]
	}

	output.AgentHints = &AgentHints{
		Summary: fmt.Sprintf("%d commands available across %d categories", len(output.Commands), len(output.Categories)),
		Notes:   []string{"Use --robot-send with a prompt to send commands to agents"},
	}

	return output, nil
}

// PrintPalette outputs palette information.
// This is a thin wrapper around GetPalette() for CLI output.
func PrintPalette(cfg *config.Config, opts PaletteOptions) error {
	output, err := GetPalette(cfg, opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// =============================================================================
// Alerts Management (--robot-dismiss-alert)
// =============================================================================
// Provides alert dismissal capabilities, complementing PrintAlertsDetailed in robot.go.

// TUIAlertsOutput represents active alerts with TUI-parity fields
type TUIAlertsOutput struct {
	RobotResponse
	Session    string         `json:"session,omitempty"`
	Count      int            `json:"count"`
	Alerts     []TUIAlertInfo `json:"alerts"`
	Dismissed  []string       `json:"dismissed,omitempty"` // IDs of dismissed alerts
	AgentHints *AgentHints    `json:"_agent_hints,omitempty"`
}

// TUIAlertInfo represents a single alert with TUI-parity fields
type TUIAlertInfo struct {
	ID          string `json:"id"`
	Type        string `json:"type"`     // "agent_stuck", "disk_low", "mail_backlog", etc.
	Severity    string `json:"severity"` // "info", "warning", "error", "critical"
	Session     string `json:"session,omitempty"`
	Pane        string `json:"pane,omitempty"`
	Message     string `json:"message"`
	CreatedAt   string `json:"created_at"` // RFC3339
	AgeSeconds  int    `json:"age_seconds"`
	Dismissible bool   `json:"dismissible"`
}

// TUIAlertsOptions configures alerts query for TUI parity
type TUIAlertsOptions struct {
	Session  string
	Severity string // Filter by severity
	Type     string // Filter by type
}

// GetAlertsTUI returns current alerts with TUI-parity formatting.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetAlertsTUI(cfg *config.Config, opts TUIAlertsOptions) (*TUIAlertsOutput, error) {
	output := &TUIAlertsOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		Alerts:        []TUIAlertInfo{},
	}

	// Use the same project-aware alert config path as the rest of robot mode so
	// disabled/custom alert settings remain authoritative here too.
	alertCfg := alertConfigForProject(cfg, "")
	alertList := alerts.GetActiveAlerts(alertCfg)

	now := time.Now()
	for _, a := range alertList {
		// Apply filters
		if opts.Session != "" && a.Session != opts.Session {
			continue
		}
		if opts.Severity != "" && string(a.Severity) != opts.Severity {
			continue
		}
		if opts.Type != "" && string(a.Type) != opts.Type {
			continue
		}

		info := TUIAlertInfo{
			ID:          a.ID,
			Type:        string(a.Type),
			Severity:    string(a.Severity),
			Session:     a.Session,
			Pane:        a.Pane,
			Message:     a.Message,
			CreatedAt:   FormatTimestamp(a.CreatedAt),
			AgeSeconds:  int(now.Sub(a.CreatedAt).Seconds()),
			Dismissible: true,
		}

		output.Alerts = append(output.Alerts, info)
	}

	output.Count = len(output.Alerts)

	// Generate hints
	var warnings []string
	if output.Count > 5 {
		warnings = append(warnings, fmt.Sprintf("%d active alerts - consider addressing", output.Count))
	}

	criticalCount := 0
	for _, a := range output.Alerts {
		if a.Severity == "critical" || a.Severity == "error" {
			criticalCount++
		}
	}

	if criticalCount > 0 {
		warnings = append(warnings, fmt.Sprintf("%d critical/error alerts require attention", criticalCount))
	}

	output.AgentHints = &AgentHints{
		Summary:  fmt.Sprintf("%d active alerts", output.Count),
		Warnings: warnings,
	}

	return output, nil
}

// PrintAlertsTUI outputs current alerts with TUI-parity formatting.
// This is a thin wrapper around GetAlertsTUI() for CLI output.
func PrintAlertsTUI(cfg *config.Config, opts TUIAlertsOptions) error {
	output, err := GetAlertsTUI(cfg, opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// DismissAlertOutput represents the result of dismissing an alert
type DismissAlertOutput struct {
	RobotResponse
	AlertID        string      `json:"alert_id,omitempty"`
	Session        string      `json:"session,omitempty"`
	Dismissed      bool        `json:"dismissed"`
	DismissedCount int         `json:"dismissed_count"`
	DismissedIDs   []string    `json:"dismissed_ids,omitempty"`
	AgentHints     *AgentHints `json:"_agent_hints,omitempty"`
}

// DismissAlertOptions configures alert dismissal
type DismissAlertOptions struct {
	AlertID    string
	Session    string // Scope to session
	DismissAll bool   // Dismiss all alerts matching criteria
}

// GetDismissAlert dismisses one or more active alerts and returns the result.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetDismissAlert(cfg *config.Config, opts DismissAlertOptions) (*DismissAlertOutput, error) {
	output := &DismissAlertOutput{
		RobotResponse: NewRobotResponse(true),
		AlertID:       opts.AlertID,
		Session:       opts.Session,
		DismissedIDs:  []string{},
	}

	if opts.AlertID == "" && !opts.DismissAll {
		return &DismissAlertOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("alert ID required"),
				ErrCodeInvalidFlag,
				"Specify --robot-dismiss-alert=ALERT_ID or use --dismiss-all",
			),
			AlertID: opts.AlertID,
		}, nil
	}

	if cfg != nil {
		alerts.SetGlobalTrackerConfig(alerts.Config{
			Enabled:              cfg.Alerts.Enabled,
			AgentStuckMinutes:    cfg.Alerts.AgentStuckMinutes,
			DiskLowThresholdGB:   cfg.Alerts.DiskLowThresholdGB,
			MailBacklogThreshold: cfg.Alerts.MailBacklogThreshold,
			BeadStaleHours:       cfg.Alerts.BeadStaleHours,
			ResolvedPruneMinutes: cfg.Alerts.ResolvedPruneMinutes,
		})
	}

	tracker := alerts.GetGlobalTracker()
	active := tracker.GetActive()
	matchesSession := func(alert alerts.Alert) bool {
		if opts.Session == "" {
			return true
		}
		return alert.Session == opts.Session
	}

	if opts.AlertID != "" {
		var found *alerts.Alert
		for i := range active {
			if active[i].ID != opts.AlertID {
				continue
			}
			if !matchesSession(active[i]) {
				return &DismissAlertOutput{
					RobotResponse: NewErrorResponse(
						fmt.Errorf("alert %q is not active in session %q", opts.AlertID, opts.Session),
						ErrCodeNotFound,
						"Use --robot-alerts to inspect active alerts for that session",
					),
					AlertID: opts.AlertID,
					Session: opts.Session,
				}, nil
			}
			found = &active[i]
			break
		}
		if found == nil {
			return &DismissAlertOutput{
				RobotResponse: NewErrorResponse(
					fmt.Errorf("alert %q not found", opts.AlertID),
					ErrCodeNotFound,
					"Use --robot-alerts to inspect active alerts",
				),
				AlertID: opts.AlertID,
				Session: opts.Session,
			}, nil
		}
		if !tracker.ManualResolve(found.ID) {
			return &DismissAlertOutput{
				RobotResponse: NewErrorResponse(
					fmt.Errorf("failed to dismiss alert %q", found.ID),
					ErrCodeInternalError,
					"Refresh --robot-alerts and try again",
				),
				AlertID: found.ID,
				Session: opts.Session,
			}, nil
		}
		output.Dismissed = true
		output.DismissedCount = 1
		output.DismissedIDs = append(output.DismissedIDs, found.ID)
		output.AgentHints = &AgentHints{
			Summary: fmt.Sprintf("Dismissed alert %s", found.ID),
			Notes:   []string{"The alert may reappear if the underlying condition is still present"},
		}
		return output, nil
	}

	for _, alert := range active {
		if !matchesSession(alert) {
			continue
		}
		if tracker.ManualResolve(alert.ID) {
			output.DismissedIDs = append(output.DismissedIDs, alert.ID)
		}
	}
	output.DismissedCount = len(output.DismissedIDs)
	output.Dismissed = output.DismissedCount > 0
	if output.Dismissed {
		output.AgentHints = &AgentHints{
			Summary: fmt.Sprintf("Dismissed %d alert(s)", output.DismissedCount),
			Notes:   []string{"Alerts may reappear if the underlying conditions are still present"},
		}
		return output, nil
	}

	output.AgentHints = &AgentHints{
		Summary: "No active alerts matched the dismissal request",
		Notes:   []string{"Use --robot-alerts to inspect currently active alerts"},
	}
	return output, nil
}

// PrintDismissAlert dismisses an alert and outputs the result.
// This is a thin wrapper around GetDismissAlert() for CLI output.
func PrintDismissAlert(cfg *config.Config, opts DismissAlertOptions) error {
	output, err := GetDismissAlert(cfg, opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// =============================================================================
// Helper Functions
// =============================================================================

func hasPaletteKey(set map[string]struct{}, key string) bool {
	_, ok := set[strings.TrimSpace(key)]
	return ok
}

// detectErrors scans output lines for error patterns
func detectErrors(lines []string) []string {
	var errors []string
	errorPatterns := []string{
		"error:",
		"Error:",
		"ERROR:",
		"failed:",
		"Failed:",
		"FAILED:",
		"panic:",
		"Panic:",
		"exception:",
		"Exception:",
		"traceback",
		"Traceback",
	}

	for _, line := range lines {
		for _, pattern := range errorPatterns {
			if strings.Contains(line, pattern) {
				// Truncate long error messages
				if len(line) > 200 {
					line = line[:200] + "..."
				}
				errors = append(errors, line)
				break
			}
		}
	}

	// Limit to 10 errors
	if len(errors) > 10 {
		errors = errors[:10]
	}

	return errors
}

// parseCodeBlocks extracts code block information from output
func parseCodeBlocks(lines []string) []CodeBlockInfo {
	var blocks []CodeBlockInfo
	inBlock := false
	var currentBlock CodeBlockInfo

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if !inBlock {
				// Start of block
				inBlock = true
				currentBlock = CodeBlockInfo{
					LineStart: i,
					Language:  strings.TrimPrefix(trimmed, "```"),
				}
			} else {
				// End of block
				currentBlock.LineEnd = i
				blocks = append(blocks, currentBlock)
				inBlock = false
			}
		}
	}

	return blocks
}

// extractFileReferences finds file paths mentioned in output
func extractFileReferences(lines []string) []string {
	files := make(map[string]struct{})

	for _, line := range lines {
		// Look for common file path patterns
		// This is a simplified heuristic
		words := strings.Fields(line)
		for _, word := range words {
			// Clean up the word
			word = strings.Trim(word, "\"'`()[]{},:;")

			// Check if it looks like a file path
			if strings.Contains(word, "/") || strings.Contains(word, ".") {
				if isLikelyFilePath(word) {
					files[word] = struct{}{}
				}
			}
		}
	}

	var result []string
	for f := range files {
		result = append(result, f)
	}

	// Limit results
	if len(result) > 20 {
		result = result[:20]
	}

	return result
}

// isLikelyFilePath checks if a string looks like a file path
func isLikelyFilePath(s string) bool {
	// Must have an extension or look like a path
	if !strings.Contains(s, ".") && !strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "./") {
		return false
	}

	// Common file extensions
	extensions := []string{".go", ".py", ".js", ".ts", ".tsx", ".jsx", ".json", ".yaml", ".yml", ".toml", ".md", ".txt", ".css", ".html", ".sh", ".bash"}
	for _, ext := range extensions {
		if strings.HasSuffix(s, ext) {
			return true
		}
	}

	// Looks like a relative or absolute path
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") {
		return true
	}

	return false
}

// truncateString truncates a string to max length with ellipsis.
// Respects UTF-8 rune boundaries.
func truncateString(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return "..."[:max]
	}
	// Find last rune boundary at or before max-3 bytes
	targetLen := max - 3
	prevI := 0
	for i := range s {
		if i > targetLen {
			return s[:prevI] + "..."
		}
		prevI = i
	}
	return s[:prevI] + "..."
}

// =============================================================================
// Bead Listing (--robot-beads-list)
// =============================================================================
// Provides programmatic bead listing for AI agents, mirroring TUI beads panel.

// BeadsListOptions configures the bead listing query
type BeadsListOptions struct {
	Status   string // Filter by status: open, in_progress, closed, blocked
	Priority string // Filter by priority: 0-4 or P0-P4
	Assignee string // Filter by assignee
	Type     string // Filter by type: task, bug, feature, epic, chore
	Limit    int    // Max beads to return
}

// BeadListItem represents a single bead in the list output
type BeadListItem struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Status      string   `json:"status"`
	Priority    string   `json:"priority"`
	Type        string   `json:"type"`
	Assignee    string   `json:"assignee,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	BlockedBy   []string `json:"blocked_by,omitempty"`
	CreatedAt   string   `json:"created_at,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
	IsReady     bool     `json:"is_ready"`
	IsBlocked   bool     `json:"is_blocked"`
	Description string   `json:"description,omitempty"`
}

// BeadsListOutput represents the result of listing beads
type BeadsListOutput struct {
	RobotResponse
	Beads      []BeadListItem   `json:"beads"`
	Total      int              `json:"total"`
	Filtered   int              `json:"filtered"`
	Summary    BeadsListSummary `json:"summary"`
	AgentHints *AgentHints      `json:"_agent_hints,omitempty"`
}

// BeadsListSummary provides counts by status for bead listing
type BeadsListSummary struct {
	Open       int `json:"open"`
	InProgress int `json:"in_progress"`
	Blocked    int `json:"blocked"`
	Closed     int `json:"closed"`
	Ready      int `json:"ready"`
}

// GetBeadsList returns beads list with optional filtering.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetBeadsList(opts BeadsListOptions) (*BeadsListOutput, error) {
	output := &BeadsListOutput{
		RobotResponse: NewRobotResponse(true),
		Beads:         []BeadListItem{},
	}

	// Check if bv/bd is installed
	if !bv.IsInstalled() {
		return &BeadsListOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("beads system not available"),
				ErrCodeDependencyMissing,
				"Install bv/bd or run 'bd init' in your project",
			),
			Beads: []BeadListItem{},
		}, nil
	}

	// Build bd list command with filters
	args := []string{"list", "--json"}

	// Add status filter
	if opts.Status != "" {
		args = append(args, "--status="+opts.Status)
	}

	// Add priority filter (normalize P0-P4 to 0-4)
	if opts.Priority != "" {
		priority := opts.Priority
		if len(priority) == 2 && (priority[0] == 'P' || priority[0] == 'p') {
			priority = string(priority[1])
		}
		args = append(args, "--priority="+priority)
	}

	// Add assignee filter
	if opts.Assignee != "" {
		args = append(args, "--assignee="+opts.Assignee)
	}

	// Add type filter
	if opts.Type != "" {
		args = append(args, "--type="+opts.Type)
	}

	// Execute bd list
	result, err := bv.RunBd("", args...)
	if err != nil {
		// Check if this is just "no beads" vs actual error
		if strings.Contains(err.Error(), "no .beads") || strings.Contains(err.Error(), "not initialized") {
			output.AgentHints = &AgentHints{
				Summary: "Beads not initialized in this project",
				Notes:   []string{"Run 'bd init' to initialize beads tracking"},
			}
			return output, nil
		}
		return &BeadsListOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("failed to list beads: %w", err),
				ErrCodeInternalError,
				"Check that bd is installed and .beads/ exists",
			),
			Beads: []BeadListItem{},
		}, nil
	}

	// Parse bd list output
	// Note: bd list returns issue_type (not type), and doesn't include blocked_by
	// The status field already indicates if a bead is blocked
	var rawBeads []struct {
		ID              string   `json:"id"`
		Title           string   `json:"title"`
		Status          string   `json:"status"`
		Priority        int      `json:"priority"`
		IssueType       string   `json:"issue_type"`
		Assignee        string   `json:"assignee"`
		Labels          []string `json:"labels"`
		CreatedAt       string   `json:"created_at"`
		UpdatedAt       string   `json:"updated_at"`
		Description     string   `json:"description"`
		DependencyCount int      `json:"dependency_count"`
	}

	if err := json.Unmarshal([]byte(result), &rawBeads); err != nil {
		// Try parsing as single object (some bd versions return differently)
		return &BeadsListOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("failed to parse bead list: %w", err),
				ErrCodeInternalError,
				"Unexpected bd output format",
			),
			Beads: []BeadListItem{},
		}, nil
	}

	// Compute summary from the full result set (before applying limit)
	// This gives accurate counts - limit only affects returned items, not summary
	// Note: bd status "blocked" means unmet dependencies, "open" means ready to work
	for _, rb := range rawBeads {
		switch rb.Status {
		case "open":
			output.Summary.Open++
			output.Summary.Ready++ // open status means ready (no unmet deps)
		case "in_progress":
			output.Summary.InProgress++
		case "blocked":
			output.Summary.Blocked++
		case "closed":
			output.Summary.Closed++
		}
	}

	output.Total = len(rawBeads)

	// Apply limit for the returned items
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}

	// Convert to output format (limited)
	for i, rb := range rawBeads {
		if i >= limit {
			break
		}

		// Determine ready/blocked status from bd status field
		isBlocked := rb.Status == "blocked"
		isReady := rb.Status == "open" // open means ready (no unmet deps)

		// Format priority as P0-P4
		priorityStr := fmt.Sprintf("P%d", rb.Priority)

		item := BeadListItem{
			ID:          rb.ID,
			Title:       rb.Title,
			Status:      rb.Status,
			Priority:    priorityStr,
			Type:        rb.IssueType, // Use IssueType from bd output
			Assignee:    rb.Assignee,
			Labels:      rb.Labels,
			CreatedAt:   rb.CreatedAt,
			UpdatedAt:   rb.UpdatedAt,
			IsReady:     isReady,
			IsBlocked:   isBlocked,
			Description: rb.Description,
		}
		output.Beads = append(output.Beads, item)
	}

	output.Filtered = len(output.Beads)

	// Generate agent hints
	var notes []string
	var warnings []string

	if output.Summary.Ready > 0 {
		notes = append(notes, fmt.Sprintf("Claim one of %d ready beads with --robot-bead-claim=ID", output.Summary.Ready))
	}
	if output.Summary.InProgress > 0 {
		notes = append(notes, fmt.Sprintf("Review %d in-progress beads", output.Summary.InProgress))
	}
	if output.Summary.Blocked > 0 {
		warnings = append(warnings, fmt.Sprintf("%d beads are blocked by dependencies", output.Summary.Blocked))
	}
	if output.Total == 0 {
		notes = append(notes, "Create new beads with --robot-bead-create --bead-title='...'")
	}

	output.AgentHints = &AgentHints{
		Summary:  fmt.Sprintf("%d beads (%d ready, %d in progress)", output.Total, output.Summary.Ready, output.Summary.InProgress),
		Notes:    notes,
		Warnings: warnings,
	}

	return output, nil
}

// PrintBeadsList lists beads with optional filtering.
// This is a thin wrapper around GetBeadsList() for CLI output.
func PrintBeadsList(opts BeadsListOptions) error {
	output, err := GetBeadsList(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// =============================================================================
// Bead Management (--robot-bead-claim, --robot-bead-create, --robot-bead-show)
// =============================================================================
// Provides programmatic bead operations for AI agents, mirroring TUI beads panel actions.

// BeadClaimOutput represents the result of claiming a bead
type BeadClaimOutput struct {
	RobotResponse
	BeadID     string      `json:"bead_id"`
	Title      string      `json:"title"`
	PrevStatus string      `json:"prev_status,omitempty"`
	NewStatus  string      `json:"new_status"`
	Claimed    bool        `json:"claimed"`
	AgentHints *AgentHints `json:"_agent_hints,omitempty"`
}

// BeadClaimOptions configures the claim operation
type BeadClaimOptions struct {
	BeadID   string // Bead ID to claim (e.g., "ntm-abc123")
	Assignee string // Optional assignee name
}

// GetBeadClaim claims a bead by setting its status to in_progress.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetBeadClaim(opts BeadClaimOptions) (*BeadClaimOutput, error) {
	if opts.BeadID == "" {
		return &BeadClaimOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("bead ID required"),
				ErrCodeInvalidFlag,
				"Specify --robot-bead-claim=BEAD_ID",
			),
		}, nil
	}

	output := &BeadClaimOutput{
		RobotResponse: NewRobotResponse(true),
		BeadID:        opts.BeadID,
	}

	// Get current bead info first
	showOutput, err := bv.RunBd("", "show", opts.BeadID, "--json")
	if err != nil {
		return &BeadClaimOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("bead '%s' not found: %w", opts.BeadID, err),
				ErrCodeInvalidFlag,
				"Use 'bd list --status=open' to see available beads",
			),
			BeadID: opts.BeadID,
		}, nil
	}

	// Parse bead info - bd show returns an array
	var beadInfo []struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(showOutput), &beadInfo); err != nil || len(beadInfo) == 0 {
		return &BeadClaimOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("failed to parse bead info"),
				ErrCodeInternalError,
				"Bead data may be corrupted",
			),
			BeadID: opts.BeadID,
		}, nil
	}

	output.Title = beadInfo[0].Title
	output.PrevStatus = beadInfo[0].Status

	// Check if already in_progress
	if beadInfo[0].Status == "in_progress" {
		output.NewStatus = "in_progress"
		output.Claimed = false
		output.AgentHints = &AgentHints{
			Summary:  fmt.Sprintf("Bead %s is already in progress", opts.BeadID),
			Warnings: []string{"Bead was already claimed"},
		}
		return output, nil
	}

	// Claim the bead
	args := []string{"update", opts.BeadID, "--status=in_progress", "--json"}
	if opts.Assignee != "" {
		args = append(args, "--assignee="+opts.Assignee)
	}

	_, err = bv.RunBd("", args...)
	if err != nil {
		return &BeadClaimOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("failed to claim bead: %w", err),
				ErrCodeInternalError,
				"Check if bead is blocked by dependencies",
			),
			BeadID:     opts.BeadID,
			Title:      output.Title,
			PrevStatus: output.PrevStatus,
		}, nil
	}

	output.NewStatus = "in_progress"
	output.Claimed = true
	output.AgentHints = &AgentHints{
		Summary: fmt.Sprintf("Claimed bead %s: %s", opts.BeadID, truncateString(output.Title, 50)),
		Notes:   []string{"Use 'bd close " + opts.BeadID + "' when complete"},
	}

	return output, nil
}

// PrintBeadClaim claims a bead by setting its status to in_progress.
// This is a thin wrapper around GetBeadClaim() for CLI output.
func PrintBeadClaim(opts BeadClaimOptions) error {
	output, err := GetBeadClaim(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// BeadCreateOutput represents the result of creating a bead
type BeadCreateOutput struct {
	RobotResponse
	BeadID      string      `json:"bead_id"`
	Title       string      `json:"title"`
	Type        string      `json:"type"`
	Priority    string      `json:"priority"`
	Description string      `json:"description,omitempty"`
	Labels      []string    `json:"labels,omitempty"`
	Created     bool        `json:"created"`
	AgentHints  *AgentHints `json:"_agent_hints,omitempty"`
}

// BeadCreateOptions configures bead creation
type BeadCreateOptions struct {
	Title       string   // Required: bead title
	Type        string   // task, bug, feature, epic, chore (default: task)
	Priority    int      // 0-4 (default: 2)
	Description string   // Optional description
	Labels      []string // Optional labels
	DependsOn   []string // Optional dependency IDs
}

// GetBeadCreate creates a new bead and returns the result.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetBeadCreate(opts BeadCreateOptions) (*BeadCreateOutput, error) {
	if opts.Title == "" {
		return &BeadCreateOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("title required"),
				ErrCodeInvalidFlag,
				"Specify --bead-title='Your title'",
			),
		}, nil
	}

	// Set defaults
	if opts.Type == "" {
		opts.Type = "task"
	}
	if opts.Priority < 0 || opts.Priority > 4 {
		opts.Priority = 2
	}

	output := &BeadCreateOutput{
		RobotResponse: NewRobotResponse(true),
		Title:         opts.Title,
		Type:          opts.Type,
		Priority:      fmt.Sprintf("P%d", opts.Priority),
		Description:   opts.Description,
		Labels:        opts.Labels,
	}

	// Build bd create command
	args := []string{
		"create",
		"--json",
		"--type", opts.Type,
		"--priority", fmt.Sprintf("%d", opts.Priority),
		"--title", opts.Title,
	}

	if opts.Description != "" {
		args = append(args, "--description", opts.Description)
	}

	if len(opts.Labels) > 0 {
		args = append(args, "--labels", strings.Join(opts.Labels, ","))
	}

	// Execute creation
	createOutput, err := bv.RunBd("", args...)
	if err != nil {
		return &BeadCreateOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("failed to create bead: %w", err),
				ErrCodeInternalError,
				"Check bd is installed and .beads/ directory exists",
			),
			Title:    opts.Title,
			Type:     opts.Type,
			Priority: fmt.Sprintf("P%d", opts.Priority),
		}, nil
	}

	// Parse the result to get the bead ID
	// bd create returns a single object, not an array
	var singleResult struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(createOutput), &singleResult); err != nil {
		// Try array format as fallback
		var arrayResult []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(createOutput), &arrayResult); err != nil || len(arrayResult) == 0 {
			return &BeadCreateOutput{
				RobotResponse: NewErrorResponse(
					fmt.Errorf("failed to parse created bead ID"),
					ErrCodeInternalError,
					"Bead may have been created but ID not returned",
				),
				Title:    opts.Title,
				Type:     opts.Type,
				Priority: fmt.Sprintf("P%d", opts.Priority),
			}, nil
		}
		singleResult.ID = arrayResult[0].ID
	}
	if singleResult.ID == "" {
		return &BeadCreateOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("failed to parse created bead ID"),
				ErrCodeInternalError,
				"Bead may have been created but ID not returned",
			),
			Title:    opts.Title,
			Type:     opts.Type,
			Priority: fmt.Sprintf("P%d", opts.Priority),
		}, nil
	}

	output.BeadID = singleResult.ID
	output.Created = true

	// Add dependencies if specified
	var depWarnings []string
	for _, dep := range opts.DependsOn {
		_, depErr := bv.RunBd("", "dep", "add", output.BeadID, dep)
		if depErr != nil {
			// Non-fatal, just note it
			depWarnings = append(depWarnings,
				fmt.Sprintf("Failed to add dependency %s: %v", dep, depErr))
		}
	}

	output.AgentHints = &AgentHints{
		Summary: fmt.Sprintf("Created %s: %s", output.BeadID, truncateString(output.Title, 40)),
		Notes: []string{
			fmt.Sprintf("Claim with: bd update %s --status=in_progress", output.BeadID),
			fmt.Sprintf("View with: bd show %s", output.BeadID),
		},
		Warnings: depWarnings, // Preserve any dependency warnings
	}

	return output, nil
}

// PrintBeadCreate creates a new bead.
// This is a thin wrapper around GetBeadCreate() for CLI output.
func PrintBeadCreate(opts BeadCreateOptions) error {
	output, err := GetBeadCreate(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// BeadShowOutput represents detailed bead information
type BeadShowOutput struct {
	RobotResponse
	BeadID      string        `json:"bead_id"`
	Title       string        `json:"title"`
	Status      string        `json:"status"`
	Type        string        `json:"type"`
	Priority    string        `json:"priority"`
	Assignee    string        `json:"assignee,omitempty"`
	Description string        `json:"description,omitempty"`
	Labels      []string      `json:"labels,omitempty"`
	CreatedAt   string        `json:"created_at,omitempty"`
	UpdatedAt   string        `json:"updated_at,omitempty"`
	DependsOn   []string      `json:"depends_on,omitempty"`
	Blocks      []string      `json:"blocks,omitempty"`
	Comments    []BeadComment `json:"comments,omitempty"`
	AgentHints  *AgentHints   `json:"_agent_hints,omitempty"`
}

// BeadComment represents a comment on a bead
type BeadComment struct {
	Author    string `json:"author"`
	CreatedAt string `json:"created_at"`
	Body      string `json:"body"`
}

// BeadShowOptions configures the show operation
type BeadShowOptions struct {
	BeadID          string // Bead ID to show
	IncludeComments bool   // Include comments in output
}

// GetBeadShow returns detailed bead information.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetBeadShow(opts BeadShowOptions) (*BeadShowOutput, error) {
	if opts.BeadID == "" {
		return &BeadShowOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("bead ID required"),
				ErrCodeInvalidFlag,
				"Specify --robot-bead-show=BEAD_ID",
			),
		}, nil
	}

	output := &BeadShowOutput{
		RobotResponse: NewRobotResponse(true),
		BeadID:        opts.BeadID,
	}

	// Get bead details
	showOutput, err := bv.RunBd("", "show", opts.BeadID, "--json")
	if err != nil {
		return &BeadShowOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("bead '%s' not found: %w", opts.BeadID, err),
				ErrCodeInvalidFlag,
				"Use 'bd list' to see available beads",
			),
			BeadID: opts.BeadID,
		}, nil
	}

	// Parse bead info - bd show returns an array with detailed info
	// Dependencies/dependents are arrays of objects with id/title/etc.
	type depInfo struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	var beadInfo []struct {
		ID           string    `json:"id"`
		Title        string    `json:"title"`
		Status       string    `json:"status"`
		IssueType    string    `json:"issue_type"`
		Priority     int       `json:"priority"`
		Assignee     string    `json:"assignee"`
		Description  string    `json:"description"`
		Labels       []string  `json:"labels"`
		CreatedAt    string    `json:"created_at"`
		UpdatedAt    string    `json:"updated_at"`
		Dependencies []depInfo `json:"dependencies"`
		Dependents   []depInfo `json:"dependents"`
	}

	if err := json.Unmarshal([]byte(showOutput), &beadInfo); err != nil || len(beadInfo) == 0 {
		return &BeadShowOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("failed to parse bead info"),
				ErrCodeInternalError,
				"Bead data may be corrupted",
			),
			BeadID: opts.BeadID,
		}, nil
	}

	info := beadInfo[0]
	output.Title = info.Title
	output.Status = info.Status
	output.Type = info.IssueType
	output.Priority = fmt.Sprintf("P%d", info.Priority)
	output.Assignee = info.Assignee
	output.Description = info.Description
	output.Labels = info.Labels
	output.CreatedAt = info.CreatedAt
	output.UpdatedAt = info.UpdatedAt

	// Extract dependency IDs from the nested objects
	for _, dep := range info.Dependencies {
		output.DependsOn = append(output.DependsOn, dep.ID)
	}
	for _, dep := range info.Dependents {
		output.Blocks = append(output.Blocks, dep.ID)
	}

	// Generate hints based on status
	var suggestions []RobotAction
	var notes []string

	switch output.Status {
	case "open":
		suggestions = append(suggestions, RobotAction{
			Action:   "claim",
			Target:   opts.BeadID,
			Reason:   "Bead is available to work on",
			Priority: 1,
		})
		notes = append(notes, fmt.Sprintf("Claim with: ntm --robot-bead-claim=%s", opts.BeadID))
	case "in_progress":
		notes = append(notes, fmt.Sprintf("Close when done: bd close %s", opts.BeadID))
		if output.Assignee != "" {
			notes = append(notes, fmt.Sprintf("Currently assigned to: %s", output.Assignee))
		}
	case "blocked":
		if len(output.DependsOn) > 0 {
			notes = append(notes, fmt.Sprintf("Blocked by: %s", strings.Join(output.DependsOn, ", ")))
		}
	}

	if len(output.Blocks) > 0 {
		notes = append(notes, fmt.Sprintf("Completing this unblocks: %s", strings.Join(output.Blocks, ", ")))
	}

	output.AgentHints = &AgentHints{
		Summary:          fmt.Sprintf("%s [%s] %s: %s", output.Priority, output.Status, output.Type, truncateString(output.Title, 40)),
		Notes:            notes,
		SuggestedActions: suggestions,
	}

	return output, nil
}

// PrintBeadShow outputs detailed bead information.
// This is a thin wrapper around GetBeadShow() for CLI output.
func PrintBeadShow(opts BeadShowOptions) error {
	output, err := GetBeadShow(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// BeadCloseOutput represents the result of closing a bead
type BeadCloseOutput struct {
	RobotResponse
	BeadID     string      `json:"bead_id"`
	Title      string      `json:"title"`
	PrevStatus string      `json:"prev_status,omitempty"`
	NewStatus  string      `json:"new_status"`
	Closed     bool        `json:"closed"`
	Reason     string      `json:"reason,omitempty"`
	AgentHints *AgentHints `json:"_agent_hints,omitempty"`
}

// BeadCloseOptions configures the close operation
type BeadCloseOptions struct {
	BeadID string // Bead ID to close
	Reason string // Optional closure reason
}

// GetBeadClose closes a bead and returns the result.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetBeadClose(opts BeadCloseOptions) (*BeadCloseOutput, error) {
	if opts.BeadID == "" {
		return &BeadCloseOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("bead ID required"),
				ErrCodeInvalidFlag,
				"Specify --robot-bead-close=BEAD_ID",
			),
		}, nil
	}

	output := &BeadCloseOutput{
		RobotResponse: NewRobotResponse(true),
		BeadID:        opts.BeadID,
		Reason:        opts.Reason,
	}

	// Get current bead info first
	showOutput, err := bv.RunBd("", "show", opts.BeadID, "--json")
	if err != nil {
		return &BeadCloseOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("bead '%s' not found: %w", opts.BeadID, err),
				ErrCodeInvalidFlag,
				"Use 'bd list' to see available beads",
			),
			BeadID: opts.BeadID,
		}, nil
	}

	// Parse bead info
	var beadInfo []struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(showOutput), &beadInfo); err != nil || len(beadInfo) == 0 {
		return &BeadCloseOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("failed to parse bead info"),
				ErrCodeInternalError,
				"Bead data may be corrupted",
			),
			BeadID: opts.BeadID,
		}, nil
	}

	output.Title = beadInfo[0].Title
	output.PrevStatus = beadInfo[0].Status

	// Check if already closed
	if beadInfo[0].Status == "closed" {
		output.NewStatus = "closed"
		output.Closed = false
		output.AgentHints = &AgentHints{
			Summary:  fmt.Sprintf("Bead %s is already closed", opts.BeadID),
			Warnings: []string{"Bead was already closed"},
		}
		return output, nil
	}

	// Close the bead
	args := []string{"close", opts.BeadID, "--json"}
	if opts.Reason != "" {
		args = append(args, "--reason", opts.Reason)
	}

	_, err = bv.RunBd("", args...)
	if err != nil {
		return &BeadCloseOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("failed to close bead: %w", err),
				ErrCodeInternalError,
				"Check bead status and dependencies",
			),
			BeadID:     opts.BeadID,
			Title:      output.Title,
			PrevStatus: output.PrevStatus,
		}, nil
	}

	output.NewStatus = "closed"
	output.Closed = true
	output.AgentHints = &AgentHints{
		Summary: fmt.Sprintf("Closed bead %s: %s", opts.BeadID, truncateString(output.Title, 40)),
		Notes:   []string{"Remember to run 'bd sync' to push changes"},
	}

	return output, nil
}

// PrintBeadClose closes a bead.
// This is a thin wrapper around GetBeadClose() for CLI output.
func PrintBeadClose(opts BeadCloseOptions) error {
	output, err := GetBeadClose(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}
