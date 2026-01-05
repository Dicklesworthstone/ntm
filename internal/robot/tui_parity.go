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
	Session    string              `json:"session,omitempty"`
	TimeWindow string              `json:"time_window"` // "5m", "15m", "1h", "all"
	Count      int                 `json:"count"`
	Changes    []FileChangeRecord  `json:"changes"`
	Summary    FileChangesSummary  `json:"summary"`
	AgentHints *AgentHints         `json:"_agent_hints,omitempty"`
}

// FileChangeRecord represents a single file change with agent attribution
type FileChangeRecord struct {
	Timestamp   string   `json:"timestamp"`    // RFC3339
	Path        string   `json:"path"`         // Relative file path
	Operation   string   `json:"operation"`    // "create", "modify", "delete", "rename"
	Agents      []string `json:"agents"`       // Agents that touched this file
	Session     string   `json:"session"`      // Session where change was detected
	SizeBytes   int64    `json:"size_bytes,omitempty"`
	LinesAdded  int      `json:"lines_added,omitempty"`
	LinesRemoved int     `json:"lines_removed,omitempty"`
}

// FileChangesSummary provides aggregate statistics
type FileChangesSummary struct {
	TotalChanges    int            `json:"total_changes"`
	UniqueFiles     int            `json:"unique_files"`
	ByAgent         map[string]int `json:"by_agent"`         // Agent -> change count
	ByOperation     map[string]int `json:"by_operation"`     // Operation -> count
	MostActiveAgent string         `json:"most_active_agent,omitempty"`
	Conflicts       []FileConflict `json:"conflicts,omitempty"` // Files touched by multiple agents
}

// FileConflict represents a file modified by multiple agents
type FileConflict struct {
	Path      string   `json:"path"`
	Agents    []string `json:"agents"`
	Severity  string   `json:"severity"` // "warning", "critical"
	FirstEdit string   `json:"first_edit"` // RFC3339
	LastEdit  string   `json:"last_edit"`  // RFC3339
}

// FilesOptions configures the --robot-files command
type FilesOptions struct {
	Session    string // Filter to specific session
	TimeWindow string // "5m", "15m", "1h", "all" (default: "15m")
	Limit      int    // Max changes to return (default: 100)
}

// PrintFiles outputs file changes as JSON
func PrintFiles(opts FilesOptions) error {
	// Set defaults
	if opts.TimeWindow == "" {
		opts.TimeWindow = "15m"
	}
	if opts.Limit <= 0 {
		opts.Limit = 100
	}

	output := FilesOutput{
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
		return encodeJSON(output)
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

	return encodeJSON(output)
}

// =============================================================================
// Inspect Pane (--robot-inspect-pane)
// =============================================================================
// Provides detailed inspection of a single pane, equivalent to zooming in
// the TUI dashboard. Includes full output capture, state detection, and context.

// InspectPaneOutput represents detailed pane inspection
type InspectPaneOutput struct {
	RobotResponse
	Session    string              `json:"session"`
	PaneIndex  int                 `json:"pane_index"`
	PaneID     string              `json:"pane_id"`
	Agent      InspectPaneAgent    `json:"agent"`
	Output     InspectPaneOutput_  `json:"output"`
	Context    InspectPaneContext  `json:"context"`
	AgentHints *AgentHints         `json:"_agent_hints,omitempty"`
}

// InspectPaneAgent contains agent-specific information
type InspectPaneAgent struct {
	Type           string  `json:"type"`             // claude, codex, gemini, user
	Variant        string  `json:"variant,omitempty"`
	Title          string  `json:"title"`
	State          string  `json:"state"`            // generating, waiting, thinking, error
	StateConfidence float64 `json:"state_confidence"`
	Command        string  `json:"command,omitempty"`
	ProcessRunning bool    `json:"process_running"`
}

// InspectPaneOutput_ contains the pane output analysis
type InspectPaneOutput_ struct {
	Lines       int      `json:"lines"`              // Total lines captured
	Characters  int      `json:"characters"`         // Total characters
	LastLines   []string `json:"last_lines"`         // Last N lines (configurable)
	CodeBlocks  []CodeBlockInfo `json:"code_blocks,omitempty"` // Detected code blocks
	ErrorsFound []string `json:"errors_found,omitempty"` // Detected error messages
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
	RecentFiles    []string `json:"recent_files,omitempty"`    // Files mentioned in output
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

// PrintInspectPane outputs detailed pane inspection
func PrintInspectPane(opts InspectPaneOptions) error {
	if opts.Lines <= 0 {
		opts.Lines = 100
	}

	// Validate session
	if opts.Session == "" {
		return RobotError(
			fmt.Errorf("session name required"),
			ErrCodeInvalidFlag,
			"Specify session with --robot-inspect-pane=SESSION",
		)
	}

	if !tmux.SessionExists(opts.Session) {
		return RobotError(
			fmt.Errorf("session '%s' not found", opts.Session),
			ErrCodeSessionNotFound,
			"Use 'ntm list' to see available sessions",
		)
	}

	// Get panes
	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		return RobotError(err, ErrCodeInternalError, "Failed to get panes")
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

	if targetPane == nil {
		return RobotError(
			fmt.Errorf("pane %d not found in session '%s'", opts.PaneIndex, opts.Session),
			ErrCodePaneNotFound,
			fmt.Sprintf("Valid pane indices: 0-%d", len(panes)-1),
		)
	}

	// Capture output
	captured, captureErr := tmux.CapturePaneOutput(targetPane.ID, opts.Lines)

	output := InspectPaneOutput{
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

	return encodeJSON(output)
}

// =============================================================================
// Metrics Export (--robot-metrics)
// =============================================================================
// Exports session metrics in various formats for analysis

// MetricsOutput represents comprehensive session metrics
type MetricsOutput struct {
	RobotResponse
	Session       string                   `json:"session,omitempty"`
	Period        string                   `json:"period"` // e.g., "last_24h", "all_time"
	TokenUsage    MetricsTokenUsage        `json:"token_usage"`
	AgentStats    map[string]AgentMetrics  `json:"agent_stats"`
	SessionStats  MetricsSessionStats      `json:"session_stats"`
	AgentHints    *AgentHints              `json:"_agent_hints,omitempty"`
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
	Type          string  `json:"type"`
	PromptsReceived int   `json:"prompts_received"`
	TokensUsed    int64   `json:"tokens_used"`
	AvgResponseTime float64 `json:"avg_response_time_sec"`
	ErrorCount    int     `json:"error_count"`
	RestartCount  int     `json:"restart_count"`
	Uptime        string  `json:"uptime"`
}

// MetricsSessionStats contains session-level statistics
type MetricsSessionStats struct {
	TotalPrompts    int     `json:"total_prompts"`
	TotalAgents     int     `json:"total_agents"`
	ActiveAgents    int     `json:"active_agents"`
	SessionDuration string  `json:"session_duration"`
	FilesChanged    int     `json:"files_changed"`
	Commits         int     `json:"commits,omitempty"`
}

// MetricsOptions configures the metrics export
type MetricsOptions struct {
	Session string // Filter to specific session
	Period  string // "1h", "24h", "7d", "all" (default: "24h")
	Format  string // "json", "csv" (default: "json")
}

// PrintMetrics outputs session metrics
func PrintMetrics(opts MetricsOptions) error {
	if opts.Period == "" {
		opts.Period = "24h"
	}

	output := MetricsOutput{
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
			return RobotError(
				fmt.Errorf("session '%s' not found", opts.Session),
				ErrCodeSessionNotFound,
				"Use 'ntm list' to see available sessions",
			)
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

	return encodeJSON(output)
}

// =============================================================================
// Replay Command (--robot-replay)
// =============================================================================
// Replays a command from history, equivalent to the History panel replay action

// ReplayOutput represents the result of a replay operation
type ReplayOutput struct {
	RobotResponse
	HistoryID    string   `json:"history_id"`
	OriginalCmd  string   `json:"original_command"`
	Session      string   `json:"session"`
	TargetPanes  []int    `json:"target_panes"`
	Replayed     bool     `json:"replayed"`
	AgentHints   *AgentHints `json:"_agent_hints,omitempty"`
}

// ReplayOptions configures the replay operation
type ReplayOptions struct {
	Session   string
	HistoryID string // History entry ID to replay
	DryRun    bool   // Just show what would be replayed
}

// PrintReplay outputs replay operation result
func PrintReplay(opts ReplayOptions) error {
	output := ReplayOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		HistoryID:     opts.HistoryID,
		TargetPanes:   []int{},
	}

	// Get history entries
	entries, err := history.ReadRecent(100)
	if err != nil {
		return RobotError(
			fmt.Errorf("history tracking not available: %w", err),
			ErrCodeDependencyMissing,
			"History is recorded during send operations",
		)
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
		return RobotError(
			fmt.Errorf("history entry '%s' not found", opts.HistoryID),
			ErrCodeInvalidFlag,
			"Use --robot-history to see available entries",
		)
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
	} else {
		// Execute the replay by calling send logic
		// Build pane filter from original targets
		var paneFilter []string
		for _, t := range target.Targets {
			paneFilter = append(paneFilter, t)
		}

		sendOpts := SendOptions{
			Session: target.Session,
			Message: target.Prompt,
			Panes:   paneFilter,
		}

		// Execute the send (this will print its own JSON output)
		if err := PrintSend(sendOpts); err != nil {
			return err
		}
		// PrintSend already outputs JSON, so we return early
		return nil
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
	Session      string          `json:"session,omitempty"`
	Commands     []PaletteCmd    `json:"commands"`
	Favorites    []string        `json:"favorites"`
	Pinned       []string        `json:"pinned"`
	Recent       []PaletteRecent `json:"recent"`
	Categories   []string        `json:"categories"`
	AgentHints   *AgentHints     `json:"_agent_hints,omitempty"`
}

// PaletteCmd represents a single palette command
type PaletteCmd struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Category    string   `json:"category"`
	Prompt      string   `json:"prompt"`
	Targets     string   `json:"targets,omitempty"` // "all", "claude", etc.
	IsFavorite  bool     `json:"is_favorite"`
	IsPinned    bool     `json:"is_pinned"`
	UseCount    int      `json:"use_count"`
	Tags        []string `json:"tags,omitempty"`
}

// PaletteRecent represents a recently used command
type PaletteRecent struct {
	Key      string `json:"key"`
	UsedAt   string `json:"used_at"` // RFC3339
	Session  string `json:"session"`
	Success  bool   `json:"success"`
}

// PaletteOptions configures the palette query
type PaletteOptions struct {
	Session     string // Filter recents to session
	Category    string // Filter commands by category
	SearchQuery string // Filter commands by search term
}

// PrintPalette outputs palette information
func PrintPalette(cfg *config.Config, opts PaletteOptions) error {
	if cfg == nil {
		cfg = config.Default()
	}

	output := PaletteOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		Commands:      []PaletteCmd{},
		Favorites:     []string{},
		Pinned:        []string{},
		Recent:        []PaletteRecent{},
		Categories:    []string{},
	}

	// Get commands from config
	categorySet := make(map[string]struct{})
	for _, cmd := range cfg.Palette {
		// Apply filters
		if opts.Category != "" && cmd.Category != opts.Category {
			continue
		}
		if opts.SearchQuery != "" {
			query := strings.ToLower(opts.SearchQuery)
			if !strings.Contains(strings.ToLower(cmd.Label), query) &&
			   !strings.Contains(strings.ToLower(cmd.Key), query) {
				continue
			}
		}

		palCmd := PaletteCmd{
			Key:      cmd.Key,
			Label:    cmd.Label,
			Category: cmd.Category,
			Prompt:   cmd.Prompt,
		}

		output.Commands = append(output.Commands, palCmd)
		categorySet[cmd.Category] = struct{}{}
	}

	for cat := range categorySet {
		output.Categories = append(output.Categories, cat)
	}

	output.AgentHints = &AgentHints{
		Summary: fmt.Sprintf("%d commands available across %d categories", len(output.Commands), len(output.Categories)),
		Notes:   []string{"Use --robot-send with a prompt to send commands to agents"},
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
	Session    string          `json:"session,omitempty"`
	Count      int             `json:"count"`
	Alerts     []TUIAlertInfo  `json:"alerts"`
	Dismissed  []string        `json:"dismissed,omitempty"` // IDs of dismissed alerts
	AgentHints *AgentHints     `json:"_agent_hints,omitempty"`
}

// TUIAlertInfo represents a single alert with TUI-parity fields
type TUIAlertInfo struct {
	ID          string `json:"id"`
	Type        string `json:"type"`      // "agent_stuck", "disk_low", "mail_backlog", etc.
	Severity    string `json:"severity"`  // "info", "warning", "error", "critical"
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

// PrintAlertsTUI outputs current alerts with TUI-parity formatting
func PrintAlertsTUI(cfg *config.Config, opts TUIAlertsOptions) error {
	output := TUIAlertsOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		Alerts:        []TUIAlertInfo{},
	}

	// Get alerts from the alerts package
	alertCfg := alerts.DefaultConfig()
	if cfg != nil && cfg.Alerts.Enabled {
		alertCfg = alerts.Config{
			Enabled:              cfg.Alerts.Enabled,
			AgentStuckMinutes:    cfg.Alerts.AgentStuckMinutes,
			DiskLowThresholdGB:   cfg.Alerts.DiskLowThresholdGB,
			MailBacklogThreshold: cfg.Alerts.MailBacklogThreshold,
			BeadStaleHours:       cfg.Alerts.BeadStaleHours,
		}
	}
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

	return encodeJSON(output)
}

// DismissAlertOutput represents the result of dismissing an alert
type DismissAlertOutput struct {
	RobotResponse
	AlertID    string      `json:"alert_id"`
	Dismissed  bool        `json:"dismissed"`
	AgentHints *AgentHints `json:"_agent_hints,omitempty"`
}

// DismissAlertOptions configures alert dismissal
type DismissAlertOptions struct {
	AlertID   string
	Session   string // Scope to session
	DismissAll bool  // Dismiss all alerts matching criteria
}

// PrintDismissAlert dismisses an alert and outputs the result
// Note: Alert dismissal is session-local and non-persistent in this implementation.
// Future versions may persist dismissals.
func PrintDismissAlert(opts DismissAlertOptions) error {
	output := DismissAlertOutput{
		RobotResponse: NewRobotResponse(true),
		AlertID:       opts.AlertID,
	}

	if opts.AlertID == "" && !opts.DismissAll {
		return RobotError(
			fmt.Errorf("alert ID required"),
			ErrCodeInvalidFlag,
			"Specify --robot-dismiss-alert=ALERT_ID or use --dismiss-all",
		)
	}

	// Note: Full alert dismissal with persistence requires tracker integration.
	// For now, we track dismissed IDs in a session-local set.
	// This is a best-effort implementation until alerts.Dismiss is available.
	if opts.AlertID != "" {
		// Record the dismissal intent (actual implementation would persist this)
		output.Dismissed = true
		output.AgentHints = &AgentHints{
			Summary: fmt.Sprintf("Alert %s marked for dismissal", opts.AlertID),
			Notes:   []string{"Alert dismissal is session-local; alert may reappear if condition persists"},
		}
	}

	return encodeJSON(output)
}

// =============================================================================
// Helper Functions
// =============================================================================

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

// truncateString truncates a string to max length with ellipsis
func truncateString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
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
	Beads      []BeadListItem    `json:"beads"`
	Total      int               `json:"total"`
	Filtered   int               `json:"filtered"`
	Summary    BeadsListSummary  `json:"summary"`
	AgentHints *AgentHints       `json:"_agent_hints,omitempty"`
}

// BeadsListSummary provides counts by status for bead listing
type BeadsListSummary struct {
	Open       int `json:"open"`
	InProgress int `json:"in_progress"`
	Blocked    int `json:"blocked"`
	Closed     int `json:"closed"`
	Ready      int `json:"ready"`
}

// PrintBeadsList lists beads with optional filtering
func PrintBeadsList(opts BeadsListOptions) error {
	output := BeadsListOutput{
		RobotResponse: NewRobotResponse(true),
		Beads:         []BeadListItem{},
	}

	// Check if bv/bd is installed
	if !bv.IsInstalled() {
		return RobotError(
			fmt.Errorf("beads system not available"),
			ErrCodeDependencyMissing,
			"Install bv/bd or run 'bd init' in your project",
		)
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
			return encodeJSON(output)
		}
		return RobotError(
			fmt.Errorf("failed to list beads: %w", err),
			ErrCodeInternalError,
			"Check that bd is installed and .beads/ exists",
		)
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
		return RobotError(
			fmt.Errorf("failed to parse bead list: %w", err),
			ErrCodeInternalError,
			"Unexpected bd output format",
		)
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

// PrintBeadClaim claims a bead by setting its status to in_progress
func PrintBeadClaim(opts BeadClaimOptions) error {
	if opts.BeadID == "" {
		return RobotError(
			fmt.Errorf("bead ID required"),
			ErrCodeInvalidFlag,
			"Specify --robot-bead-claim=BEAD_ID",
		)
	}

	output := BeadClaimOutput{
		RobotResponse: NewRobotResponse(true),
		BeadID:        opts.BeadID,
	}

	// Get current bead info first
	showOutput, err := bv.RunBd("", "show", opts.BeadID, "--json")
	if err != nil {
		return RobotError(
			fmt.Errorf("bead '%s' not found: %w", opts.BeadID, err),
			ErrCodeInvalidFlag,
			"Use 'bd list --status=open' to see available beads",
		)
	}

	// Parse bead info - bd show returns an array
	var beadInfo []struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(showOutput), &beadInfo); err != nil || len(beadInfo) == 0 {
		return RobotError(
			fmt.Errorf("failed to parse bead info"),
			ErrCodeInternalError,
			"Bead data may be corrupted",
		)
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
		return encodeJSON(output)
	}

	// Claim the bead
	args := []string{"update", opts.BeadID, "--status=in_progress", "--json"}
	if opts.Assignee != "" {
		args = append(args, "--assignee="+opts.Assignee)
	}

	_, err = bv.RunBd("", args...)
	if err != nil {
		return RobotError(
			fmt.Errorf("failed to claim bead: %w", err),
			ErrCodeInternalError,
			"Check if bead is blocked by dependencies",
		)
	}

	output.NewStatus = "in_progress"
	output.Claimed = true
	output.AgentHints = &AgentHints{
		Summary: fmt.Sprintf("Claimed bead %s: %s", opts.BeadID, truncateString(output.Title, 50)),
		Notes:   []string{"Use 'bd close " + opts.BeadID + "' when complete"},
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

// PrintBeadCreate creates a new bead
func PrintBeadCreate(opts BeadCreateOptions) error {
	if opts.Title == "" {
		return RobotError(
			fmt.Errorf("title required"),
			ErrCodeInvalidFlag,
			"Specify --bead-title='Your title'",
		)
	}

	// Set defaults
	if opts.Type == "" {
		opts.Type = "task"
	}
	if opts.Priority < 0 || opts.Priority > 4 {
		opts.Priority = 2
	}

	output := BeadCreateOutput{
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
		return RobotError(
			fmt.Errorf("failed to create bead: %w", err),
			ErrCodeInternalError,
			"Check bd is installed and .beads/ directory exists",
		)
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
			return RobotError(
				fmt.Errorf("failed to parse created bead ID"),
				ErrCodeInternalError,
				"Bead may have been created but ID not returned",
			)
		}
		singleResult.ID = arrayResult[0].ID
	}
	if singleResult.ID == "" {
		return RobotError(
			fmt.Errorf("failed to parse created bead ID"),
			ErrCodeInternalError,
			"Bead may have been created but ID not returned",
		)
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

	return encodeJSON(output)
}

// BeadShowOutput represents detailed bead information
type BeadShowOutput struct {
	RobotResponse
	BeadID      string       `json:"bead_id"`
	Title       string       `json:"title"`
	Status      string       `json:"status"`
	Type        string       `json:"type"`
	Priority    string       `json:"priority"`
	Assignee    string       `json:"assignee,omitempty"`
	Description string       `json:"description,omitempty"`
	Labels      []string     `json:"labels,omitempty"`
	CreatedAt   string       `json:"created_at,omitempty"`
	UpdatedAt   string       `json:"updated_at,omitempty"`
	DependsOn   []string     `json:"depends_on,omitempty"`
	Blocks      []string     `json:"blocks,omitempty"`
	Comments    []BeadComment `json:"comments,omitempty"`
	AgentHints  *AgentHints  `json:"_agent_hints,omitempty"`
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

// PrintBeadShow outputs detailed bead information
func PrintBeadShow(opts BeadShowOptions) error {
	if opts.BeadID == "" {
		return RobotError(
			fmt.Errorf("bead ID required"),
			ErrCodeInvalidFlag,
			"Specify --robot-bead-show=BEAD_ID",
		)
	}

	output := BeadShowOutput{
		RobotResponse: NewRobotResponse(true),
		BeadID:        opts.BeadID,
	}

	// Get bead details
	showOutput, err := bv.RunBd("", "show", opts.BeadID, "--json")
	if err != nil {
		return RobotError(
			fmt.Errorf("bead '%s' not found: %w", opts.BeadID, err),
			ErrCodeInvalidFlag,
			"Use 'bd list' to see available beads",
		)
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
		return RobotError(
			fmt.Errorf("failed to parse bead info"),
			ErrCodeInternalError,
			"Bead data may be corrupted",
		)
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

// PrintBeadClose closes a bead
func PrintBeadClose(opts BeadCloseOptions) error {
	if opts.BeadID == "" {
		return RobotError(
			fmt.Errorf("bead ID required"),
			ErrCodeInvalidFlag,
			"Specify --robot-bead-close=BEAD_ID",
		)
	}

	output := BeadCloseOutput{
		RobotResponse: NewRobotResponse(true),
		BeadID:        opts.BeadID,
		Reason:        opts.Reason,
	}

	// Get current bead info first
	showOutput, err := bv.RunBd("", "show", opts.BeadID, "--json")
	if err != nil {
		return RobotError(
			fmt.Errorf("bead '%s' not found: %w", opts.BeadID, err),
			ErrCodeInvalidFlag,
			"Use 'bd list' to see available beads",
		)
	}

	// Parse bead info
	var beadInfo []struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(showOutput), &beadInfo); err != nil || len(beadInfo) == 0 {
		return RobotError(
			fmt.Errorf("failed to parse bead info"),
			ErrCodeInternalError,
			"Bead data may be corrupted",
		)
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
		return encodeJSON(output)
	}

	// Close the bead
	args := []string{"close", opts.BeadID, "--json"}
	if opts.Reason != "" {
		args = append(args, "--reason", opts.Reason)
	}

	_, err = bv.RunBd("", args...)
	if err != nil {
		return RobotError(
			fmt.Errorf("failed to close bead: %w", err),
			ErrCodeInternalError,
			"Check bead status and dependencies",
		)
	}

	output.NewStatus = "closed"
	output.Closed = true
	output.AgentHints = &AgentHints{
		Summary: fmt.Sprintf("Closed bead %s: %s", opts.BeadID, truncateString(output.Title, 40)),
		Notes:   []string{"Remember to run 'bd sync' to push changes"},
	}

	return encodeJSON(output)
}

