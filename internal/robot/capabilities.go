// Package robot provides machine-readable output for AI agents.
// capabilities.go provides the --robot-capabilities command for programmatic discovery of robot mode features.
package robot

import "sort"

// CapabilitiesOutput represents the output for --robot-capabilities
type CapabilitiesOutput struct {
	RobotResponse
	Version    string                   `json:"version"`
	Commands   []RobotCommandInfo       `json:"commands"`
	Surfaces   []RobotSurfaceDescriptor `json:"surfaces,omitempty"`
	Categories []string                 `json:"categories"`
	Attention  *AttentionCapabilities   `json:"attention,omitempty"`
}

// RobotCommandInfo describes a single robot command
type RobotCommandInfo struct {
	Name        string               `json:"name"`
	Flag        string               `json:"flag"`
	Category    string               `json:"category"`
	Summary     string               `json:"summary,omitempty"`
	Description string               `json:"description"`
	SchemaID    string               `json:"schema_id,omitempty"`
	SchemaType  string               `json:"schema_type,omitempty"`
	Sections    []string             `json:"sections,omitempty"`
	Parameters  []RobotParameter     `json:"parameters"`
	Examples    []string             `json:"examples"`
	Transports  []RobotTransportInfo `json:"transports,omitempty"`
}

// RobotParameter describes a command parameter
type RobotParameter struct {
	Name        string `json:"name"`
	Flag        string `json:"flag"`
	Type        string `json:"type"` // bool, string, int, duration
	Required    bool   `json:"required"`
	Default     string `json:"default,omitempty"`
	Description string `json:"description"`
}

// categoryOrder defines the canonical order for categories
var categoryOrder = []string{
	"state",
	"attention",
	"ensemble",
	"control",
	"spawn",
	"beads",
	"bv",
	"cass",
	"pipeline",
	"utility",
}

// GetCapabilities collects robot mode capabilities.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetCapabilities() (*CapabilitiesOutput, error) {
	registry := GetRobotRegistry()
	surfaces := cloneRobotSurfaceDescriptors(registry.Surfaces)
	commands := buildCapabilitiesCommandCatalog(surfaces)

	return &CapabilitiesOutput{
		RobotResponse: NewRobotResponse(true),
		Version:       Version,
		Commands:      commands,
		Surfaces:      surfaces,
		Categories:    cloneStrings(registry.Categories),
		Attention:     DefaultAttentionCapabilities(),
	}, nil
}

// PrintCapabilities outputs robot mode capabilities as JSON.
// This is a thin wrapper around GetCapabilities() for CLI output.
func PrintCapabilities() error {
	output, err := GetCapabilities()
	if err != nil {
		return err
	}
	return outputJSON(output)
}

func categoryIndex(cat string) int {
	for i, c := range categoryOrder {
		if c == cat {
			return i
		}
	}
	return len(categoryOrder)
}

func buildCapabilitiesCommandCatalog(surfaces []RobotSurfaceDescriptor) []RobotCommandInfo {
	commands := make([]RobotCommandInfo, 0, len(surfaces))
	for _, surface := range surfaces {
		commands = append(commands, RobotCommandInfo{
			Name:        surface.Name,
			Flag:        surface.Flag,
			Category:    surface.Category,
			Summary:     surface.Summary,
			Description: surface.Description,
			SchemaID:    surface.SchemaID,
			SchemaType:  surface.SchemaType,
			Sections:    cloneStrings(surface.Sections),
			Parameters:  cloneRobotParameters(surface.Parameters),
			Examples:    cloneStrings(surface.Examples),
			Transports:  cloneTransports(surface.Transports),
		})
	}

	sort.Slice(commands, func(i, j int) bool {
		if commands[i].Category != commands[j].Category {
			return categoryIndex(commands[i].Category) < categoryIndex(commands[j].Category)
		}
		return commands[i].Name < commands[j].Name
	})

	return commands
}

// buildCommandRegistry returns all robot commands with their metadata
func buildCommandRegistry() []RobotCommandInfo {
	return []RobotCommandInfo{
		// === STATE INSPECTION ===
		{
			Name:        "status",
			Flag:        "--robot-status",
			Category:    "state",
			Description: "Get tmux sessions, panes, and agent states. The primary entry point for understanding current system state.",
			Parameters: []RobotParameter{
				{Name: "robot-limit", Flag: "--robot-limit", Type: "int", Required: false, Default: "0", Description: "Max sessions to return (alias: --limit)"},
				{Name: "robot-offset", Flag: "--robot-offset", Type: "int", Required: false, Default: "0", Description: "Pagination offset for sessions (alias: --offset)"},
			},
			Examples: []string{"ntm --robot-status"},
		},
		{
			Name:        "context",
			Flag:        "--robot-context",
			Category:    "state",
			Description: "Get context window usage estimates for all agents in a session.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-context", Type: "string", Required: true, Description: "Session name to analyze"},
			},
			Examples: []string{"ntm --robot-context=myproject"},
		},
		{
			Name:        "ensemble",
			Flag:        "--robot-ensemble",
			Category:    "ensemble",
			Description: "Get ensemble state for a session including modes, status, and synthesis readiness.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-ensemble", Type: "string", Required: true, Description: "Session name to inspect"},
			},
			Examples: []string{"ntm --robot-ensemble=myproject"},
		},
		{
			Name:        "ensemble-modes",
			Flag:        "--robot-ensemble-modes",
			Category:    "ensemble",
			Description: "List available reasoning modes with filtering by category and tier.",
			Parameters: []RobotParameter{
				{Name: "category", Flag: "--category", Type: "string", Required: false, Description: "Filter by category code (A-L) or name (Formal, Heuristic, etc.)"},
				{Name: "tier", Flag: "--tier", Type: "string", Required: false, Default: "core", Description: "Filter by tier: core, advanced, experimental, all"},
				{Name: "limit", Flag: "--limit", Type: "int", Required: false, Default: "50", Description: "Max modes to return"},
				{Name: "offset", Flag: "--offset", Type: "int", Required: false, Default: "0", Description: "Pagination offset"},
			},
			Examples: []string{
				"ntm --robot-ensemble-modes",
				"ntm --robot-ensemble-modes --tier=all",
				"ntm --robot-ensemble-modes --category=Formal --tier=all",
				"ntm --robot-ensemble-modes --limit=10 --offset=20",
			},
		},
		{
			Name:        "ensemble-presets",
			Flag:        "--robot-ensemble-presets",
			Category:    "ensemble",
			Description: "List available ensemble presets with their mode configurations and budgets.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-ensemble-presets"},
		},
		{
			Name:        "ensemble-suggest",
			Flag:        "--robot-ensemble-suggest",
			Category:    "ensemble",
			Description: "Suggest the best ensemble preset for a question.",
			Parameters: []RobotParameter{
				{Name: "question", Flag: "--robot-ensemble-suggest", Type: "string", Required: true, Description: "Question or task to analyze"},
				{Name: "suggest-id-only", Flag: "--suggest-id-only", Type: "bool", Required: false, Description: "Return only the preset ID"},
			},
			Examples: []string{
				"ntm --robot-ensemble-suggest='What security issues exist?'",
				"ntm --robot-ensemble-suggest='Review architecture' --suggest-id-only",
			},
		},
		{
			Name:        "ensemble-stop",
			Flag:        "--robot-ensemble-stop",
			Category:    "ensemble",
			Description: "Stop an ensemble session and optionally collect partial output.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-ensemble-stop", Type: "string", Required: true, Description: "Session name to stop"},
				{Name: "stop-force", Flag: "--stop-force", Type: "bool", Required: false, Description: "Force stop without graceful shutdown"},
				{Name: "stop-no-collect", Flag: "--stop-no-collect", Type: "bool", Required: false, Description: "Skip partial output collection"},
			},
			Examples: []string{
				"ntm --robot-ensemble-stop=myproject",
				"ntm --robot-ensemble-stop=myproject --stop-force",
			},
		},
		{
			Name:        "snapshot",
			Flag:        "--robot-snapshot",
			Category:    "state",
			Description: "Unified state query: sessions + beads + alerts + mail. Use --since for delta snapshots.",
			Parameters: []RobotParameter{
				{Name: "since", Flag: "--since", Type: "string", Required: false, Description: "RFC3339 timestamp for delta snapshot"},
				{Name: "bead-limit", Flag: "--bead-limit", Type: "int", Required: false, Default: "5", Description: "Max beads per category"},
				{Name: "robot-limit", Flag: "--robot-limit", Type: "int", Required: false, Default: "0", Description: "Max sessions to return (alias: --limit)"},
				{Name: "robot-offset", Flag: "--robot-offset", Type: "int", Required: false, Default: "0", Description: "Pagination offset for sessions (alias: --offset)"},
			},
			Examples: []string{
				"ntm --robot-snapshot",
				"ntm --robot-snapshot --since=2025-01-15T10:00:00Z",
			},
		},
		{
			Name:        "tail",
			Flag:        "--robot-tail",
			Category:    "state",
			Description: "Capture recent output from panes. Useful for checking agent progress or errors.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-tail", Type: "string", Required: true, Description: "Session name"},
				{Name: "lines", Flag: "--lines", Type: "int", Required: false, Default: "20", Description: "Lines per pane"},
				{Name: "panes", Flag: "--panes", Type: "string", Required: false, Description: "Comma-separated pane indices to filter"},
			},
			Examples: []string{
				"ntm --robot-tail=myproject",
				"ntm --robot-tail=myproject --lines=50 --panes=1,2",
			},
		},
		{
			Name:        "is-working",
			Flag:        "--robot-is-working",
			Category:    "state",
			Description: "Check if agents are actively working, idle, rate-limited, or safe to restart. Returns per-pane recommendations and an aggregate work-state summary.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-is-working", Type: "string", Required: true, Description: "Session name"},
				{Name: "panes", Flag: "--panes", Type: "string", Required: false, Description: "Comma-separated pane indices to filter"},
				{Name: "verbose", Flag: "--verbose", Type: "bool", Required: false, Description: "Include raw sample output in the response"},
			},
			Examples: []string{
				"ntm --robot-is-working=myproject",
				"ntm --robot-is-working=myproject --panes=2,3",
				"ntm --robot-is-working=myproject --verbose",
			},
		},
		{
			Name:        "errors",
			Flag:        "--robot-errors",
			Category:    "state",
			Description: "Filter pane output to show only errors and recent failure context.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-errors", Type: "string", Required: true, Description: "Session name"},
				{Name: "errors-since", Flag: "--errors-since", Type: "string", Required: false, Description: "Filter to errors from the last duration"},
				{Name: "lines", Flag: "--lines", Type: "int", Required: false, Default: "1000", Description: "Max lines captured per pane when no explicit value is provided"},
				{Name: "panes", Flag: "--panes", Type: "string", Required: false, Description: "Comma-separated pane indices to filter"},
				{Name: "type", Flag: "--type", Type: "string", Required: false, Description: "Filter by agent type"},
			},
			Examples: []string{
				"ntm --robot-errors=myproject --lines=100",
				"ntm --robot-errors=myproject --errors-since=5m --type=claude",
			},
		},
		{
			Name:        "agent-health",
			Flag:        "--robot-agent-health",
			Category:    "state",
			Description: "Run a comprehensive per-agent health check using local state and provider data.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-agent-health", Type: "string", Required: true, Description: "Session name"},
				{Name: "panes", Flag: "--panes", Type: "string", Required: false, Description: "Comma-separated pane indices to filter"},
				{Name: "lines", Flag: "--lines", Type: "int", Required: false, Default: "20", Description: "Lines captured per pane for analysis"},
				{Name: "no-caut", Flag: "--no-caut", Type: "bool", Required: false, Description: "Skip caut provider queries for faster local-only checks"},
				{Name: "verbose", Flag: "--verbose", Type: "bool", Required: false, Description: "Include raw sample output in the response"},
			},
			Examples: []string{
				"ntm --robot-agent-health=myproject",
				"ntm --robot-agent-health=myproject --panes=2,3 --verbose",
			},
		},
		{
			Name:        "health-oauth",
			Flag:        "--robot-health-oauth",
			Category:    "state",
			Description: "Get per-agent OAuth and rate-limit status for a session.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-health-oauth", Type: "string", Required: true, Description: "Session name"},
			},
			Examples: []string{"ntm --robot-health-oauth=myproject"},
		},
		{
			Name:        "logs",
			Flag:        "--robot-logs",
			Category:    "state",
			Description: "Get aggregated logs from agent panes.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-logs", Type: "string", Required: true, Description: "Session name"},
				{Name: "logs-panes", Flag: "--logs-panes", Type: "string", Required: false, Description: "Comma-separated pane indices to filter"},
				{Name: "logs-limit", Flag: "--logs-limit", Type: "int", Required: false, Default: "100", Description: "Max lines returned per pane"},
			},
			Examples: []string{
				"ntm --robot-logs=myproject",
				"ntm --robot-logs=myproject --logs-panes=1,2 --logs-limit=50",
			},
		},
		{
			Name:        "monitor",
			Flag:        "--robot-monitor",
			Category:    "state",
			Description: "Start proactive JSONL monitoring for context and provider-usage limits.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-monitor", Type: "string", Required: true, Description: "Session name"},
				{Name: "panes", Flag: "--panes", Type: "string", Required: false, Description: "Comma-separated pane indices to filter"},
				{Name: "interval", Flag: "--interval", Type: "string", Required: false, Default: "30s", Description: "Polling interval"},
				{Name: "warn-threshold", Flag: "--warn-threshold", Type: "string", Required: false, Default: "25", Description: "Context percentage for warning level"},
				{Name: "crit-threshold", Flag: "--crit-threshold", Type: "string", Required: false, Default: "15", Description: "Context percentage for critical level"},
				{Name: "info-threshold", Flag: "--info-threshold", Type: "string", Required: false, Default: "40", Description: "Context percentage for info level"},
				{Name: "alert-threshold", Flag: "--alert-threshold", Type: "string", Required: false, Default: "80", Description: "Provider usage percentage for alert level"},
				{Name: "include-caut", Flag: "--include-caut", Type: "bool", Required: false, Description: "Include caut provider usage data"},
				{Name: "output", Flag: "--output", Type: "string", Required: false, Description: "Output file path for JSONL warnings"},
				{Name: "lines", Flag: "--lines", Type: "int", Required: false, Default: "100", Description: "Lines captured per pane for health sampling"},
			},
			Examples: []string{
				"ntm --robot-monitor=myproject --interval=30s",
				"ntm --robot-monitor=myproject --panes=2,3 --output=/tmp/monitor.jsonl",
			},
		},
		{
			Name:        "watch-bead",
			Flag:        "--robot-watch-bead",
			Category:    "state",
			Description: "Capture recent mentions of a bead across panes and report current bead status.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-watch-bead", Type: "string", Required: true, Description: "Session name"},
				{Name: "bead", Flag: "--bead", Type: "string", Required: true, Description: "Bead ID to track"},
				{Name: "panes", Flag: "--panes", Type: "string", Required: false, Description: "Comma-separated pane indices to filter"},
				{Name: "lines", Flag: "--lines", Type: "int", Required: false, Default: "200", Description: "Lines captured per pane"},
				{Name: "interval", Flag: "--interval", Type: "string", Required: false, Default: "30s", Description: "Status polling interval"},
			},
			Examples: []string{
				"ntm --robot-watch-bead=myproject --bead=bd-abc123",
				"ntm --robot-watch-bead=myproject --bead=bd-abc123 --panes=2,3 --lines=300 --interval=45s",
			},
		},
		{
			Name:        "inspect-pane",
			Flag:        "--robot-inspect-pane",
			Category:    "state",
			Description: "Detailed pane inspection with state detection and optional code block parsing.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-inspect-pane", Type: "string", Required: true, Description: "Session name"},
				{Name: "inspect-index", Flag: "--inspect-index", Type: "int", Required: false, Default: "0", Description: "Pane index to inspect"},
				{Name: "inspect-lines", Flag: "--inspect-lines", Type: "int", Required: false, Default: "100", Description: "Lines to capture"},
				{Name: "inspect-code", Flag: "--inspect-code", Type: "bool", Required: false, Description: "Parse code blocks from output"},
			},
			Examples: []string{"ntm --robot-inspect-pane=myproject --inspect-index=1 --inspect-code"},
		},
		{
			Name:        "inspect-session",
			Flag:        "--robot-inspect-session",
			Category:    "state",
			Description: "Projection-backed session drill-down with stable session and agent detail.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-inspect-session", Type: "string", Required: true, Description: "Session name"},
			},
			Examples: []string{"ntm --robot-inspect-session=myproject"},
		},
		{
			Name:        "inspect-agent",
			Flag:        "--robot-inspect-agent",
			Category:    "state",
			Description: "Projection-backed agent drill-down with stable runtime agent identity.",
			Parameters: []RobotParameter{
				{Name: "agent_id", Flag: "--robot-inspect-agent", Type: "string", Required: true, Description: "Runtime agent id in SESSION:PANE form"},
			},
			Examples: []string{"ntm --robot-inspect-agent=myproject:%1"},
		},
		{
			Name:        "inspect-work",
			Flag:        "--robot-inspect-work",
			Category:    "state",
			Description: "Projection-backed work-item drill-down with stable bead identity.",
			Parameters: []RobotParameter{
				{Name: "bead_id", Flag: "--robot-inspect-work", Type: "string", Required: true, Description: "Bead id from snapshot/work surfaces"},
			},
			Examples: []string{"ntm --robot-inspect-work=bd-j9jo3.6.6"},
		},
		{
			Name:        "inspect-coordination",
			Flag:        "--robot-inspect-coordination",
			Category:    "state",
			Description: "Projection-backed coordination drill-down keyed by Agent Mail identity.",
			Parameters: []RobotParameter{
				{Name: "agent_name", Flag: "--robot-inspect-coordination", Type: "string", Required: true, Description: "Agent Mail identity / coordination key"},
			},
			Examples: []string{"ntm --robot-inspect-coordination=BlueLake"},
		},
		{
			Name:        "inspect-quota",
			Flag:        "--robot-inspect-quota",
			Category:    "state",
			Description: "Projection-backed quota drill-down keyed by provider/account.",
			Parameters: []RobotParameter{
				{Name: "quota_id", Flag: "--robot-inspect-quota", Type: "string", Required: true, Description: "Provider/account identity, e.g. anthropic/default"},
			},
			Examples: []string{"ntm --robot-inspect-quota=anthropic/default"},
		},
		{
			Name:        "inspect-incident",
			Flag:        "--robot-inspect-incident",
			Category:    "state",
			Description: "Store-backed incident drill-down keyed by stable incident id.",
			Parameters: []RobotParameter{
				{Name: "incident_id", Flag: "--robot-inspect-incident", Type: "string", Required: true, Description: "Incident id from snapshot/attention surfaces"},
			},
			Examples: []string{"ntm --robot-inspect-incident=inc_20260323_abc123"},
		},
		{
			Name:        "files",
			Flag:        "--robot-files",
			Category:    "state",
			Description: "Get file changes with agent attribution and conflict detection.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-files", Type: "string", Required: false, Description: "Optional session filter"},
				{Name: "files-window", Flag: "--files-window", Type: "string", Required: false, Default: "15m", Description: "Time window: 5m, 15m, 1h, all"},
				{Name: "files-limit", Flag: "--files-limit", Type: "int", Required: false, Default: "100", Description: "Max changes to return"},
			},
			Examples: []string{"ntm --robot-files=myproject --files-window=1h"},
		},
		{
			Name:        "metrics",
			Flag:        "--robot-metrics",
			Category:    "state",
			Description: "Session metrics export for analysis.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-metrics", Type: "string", Required: false, Description: "Optional session filter"},
				{Name: "metrics-period", Flag: "--metrics-period", Type: "string", Required: false, Default: "24h", Description: "Period: 1h, 24h, 7d, all"},
			},
			Examples: []string{"ntm --robot-metrics=myproject --metrics-period=7d"},
		},
		{
			Name:        "activity",
			Flag:        "--robot-activity",
			Category:    "state",
			Description: "Get agent activity state (idle/busy/error) for all agents in a session.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-activity", Type: "string", Required: true, Description: "Session name"},
				{Name: "panes", Flag: "--panes", Type: "string", Required: false, Description: "Comma-separated pane indices to filter"},
				{Name: "activity-type", Flag: "--activity-type", Type: "string", Required: false, Description: "Comma-separated agent types to filter: claude, codex, gemini"},
			},
			Examples: []string{
				"ntm --robot-activity=myproject --activity-type=claude",
				"ntm --robot-activity=myproject --panes=1,2 --activity-type=claude,codex",
			},
		},
		{
			Name:        "dashboard",
			Flag:        "--robot-dashboard",
			Category:    "state",
			Description: "Dashboard summary as markdown or JSON.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-dashboard"},
		},
		{
			Name:        "terse",
			Flag:        "--robot-terse",
			Category:    "state",
			Description: "Single-line encoded state for minimal token usage.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-terse"},
		},
		{
			Name:        "markdown",
			Flag:        "--robot-markdown",
			Category:    "state",
			Description: "System state as markdown tables.",
			Parameters: []RobotParameter{
				{Name: "md-compact", Flag: "--md-compact", Type: "bool", Required: false, Description: "Ultra-compact markdown with abbreviations"},
				{Name: "md-session", Flag: "--md-session", Type: "string", Required: false, Description: "Filter to one session"},
				{Name: "md-max-beads", Flag: "--md-max-beads", Type: "int", Required: false, Description: "Max beads per category"},
				{Name: "md-max-alerts", Flag: "--md-max-alerts", Type: "int", Required: false, Description: "Max alerts to show"},
			},
			Examples: []string{"ntm --robot-markdown --md-compact --md-session=myproject"},
		},
		{
			Name:        "health",
			Flag:        "--robot-health",
			Category:    "state",
			Description: "Get session or project health status.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-health", Type: "string", Required: false, Description: "Session for per-agent health, empty for project health"},
			},
			Examples: []string{"ntm --robot-health=myproject"},
		},
		{
			Name:        "diagnose",
			Flag:        "--robot-diagnose",
			Category:    "state",
			Description: "Comprehensive health check with fix recommendations.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-diagnose", Type: "string", Required: true, Description: "Session name"},
				{Name: "diagnose-fix", Flag: "--diagnose-fix", Type: "bool", Required: false, Description: "Attempt auto-fix for fixable issues"},
				{Name: "diagnose-brief", Flag: "--diagnose-brief", Type: "bool", Required: false, Description: "Minimal output (summary only)"},
				{Name: "diagnose-pane", Flag: "--diagnose-pane", Type: "int", Required: false, Description: "Diagnose specific pane only"},
			},
			Examples: []string{"ntm --robot-diagnose=myproject --diagnose-fix"},
		},
		{
			Name:        "health-restart-stuck",
			Flag:        "--robot-health-restart-stuck",
			Category:    "state",
			Description: "Detect and restart agents stuck with no output for N minutes.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-health-restart-stuck", Type: "string", Required: true, Description: "Session name"},
				{Name: "stuck-threshold", Flag: "--stuck-threshold", Type: "duration", Required: false, Default: "5m", Description: "Duration before considering agent stuck (e.g. 5m, 10m, 300s)"},
				{Name: "dry-run", Flag: "--dry-run", Type: "bool", Required: false, Description: "Report stuck panes without restarting"},
			},
			Examples: []string{
				"ntm --robot-health-restart-stuck=myproject",
				"ntm --robot-health-restart-stuck=myproject --stuck-threshold=10m --dry-run",
			},
		},
		{
			Name:        "probe",
			Flag:        "--robot-probe",
			Category:    "state",
			Description: "Active pane responsiveness probe using keystroke or interrupt methods.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-probe", Type: "string", Required: true, Description: "Session name"},
				{Name: "panes", Flag: "--panes", Type: "string", Required: false, Description: "Comma-separated pane indices to probe"},
				{Name: "probe-method", Flag: "--probe-method", Type: "string", Required: false, Default: "keystroke_echo", Description: "Probe method: keystroke_echo, interrupt_test"},
				{Name: "probe-timeout", Flag: "--probe-timeout", Type: "int", Required: false, Default: "5000", Description: "Probe timeout in milliseconds"},
				{Name: "probe-aggressive", Flag: "--probe-aggressive", Type: "bool", Required: false, Description: "Fallback to interrupt_test if keystroke_echo fails"},
			},
			Examples: []string{
				"ntm --robot-probe=myproject",
				"ntm --robot-probe=myproject --panes=2 --probe-method=interrupt_test",
			},
		},
		{
			Name:        "diff",
			Flag:        "--robot-diff",
			Category:    "state",
			Description: "Compare agent activity and file changes over time.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-diff", Type: "string", Required: true, Description: "Session name"},
				{Name: "since", Flag: "--since", Type: "string", Required: false, Default: "15m", Description: "Duration or RFC3339 timestamp to look back from"},
			},
			Examples: []string{
				"ntm --robot-diff=myproject --since=10m",
				"ntm --robot-diff=myproject --since=2026-03-22T03:50:00Z",
			},
		},
		{
			Name:        "summary",
			Flag:        "--robot-summary",
			Category:    "state",
			Description: "Get session activity summary with agent metrics.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-summary", Type: "string", Required: true, Description: "Session name"},
				{Name: "since", Flag: "--since", Type: "string", Required: false, Default: "30m", Description: "Duration or RFC3339 timestamp to look back from"},
			},
			Examples: []string{
				"ntm --robot-summary=myproject --since=1h",
				"ntm --robot-summary=myproject --since=2026-03-22T03:50:00Z",
			},
		},

		// === ATTENTION FEED (Operator Loop) ===
		{
			Name:        "events",
			Flag:        "--robot-events",
			Category:    "attention",
			Description: "Raw event replay from the attention feed. Returns events since the specified cursor with pagination support. Use this for full event history or debugging; prefer --robot-digest or --robot-attention for operator loops.",
			Parameters: []RobotParameter{
				{Name: "since-cursor", Flag: "--since-cursor", Type: "int", Required: false, Default: "0", Description: "Replay events after this cursor (0 = all)"},
				{Name: "events-limit", Flag: "--events-limit", Type: "int", Required: false, Default: "100", Description: "Max events to return"},
				{Name: "events-incident", Flag: "--events-incident", Type: "string", Required: false, Description: "Bounded historical replay around a durable incident ID"},
				{Name: "events-as-of", Flag: "--events-as-of", Type: "string", Required: false, Description: "Bounded historical reconstruction at or before an RFC3339 timestamp"},
				{Name: "events-window-before", Flag: "--events-window-before", Type: "duration", Required: false, Default: "5m", Description: "Context before incident start for incident replay"},
				{Name: "events-window-after", Flag: "--events-window-after", Type: "duration", Required: false, Default: "1m", Description: "Context after incident end for incident replay"},
				{Name: "events-category", Flag: "--events-category", Type: "string", Required: false, Description: "Filter by event category"},
				{Name: "events-session", Flag: "--events-session", Type: "string", Required: false, Description: "Filter by session name"},
				{Name: "events-actionability", Flag: "--events-actionability", Type: "string", Required: false, Description: "Filter by actionability level"},
				{Name: "profile", Flag: "--profile", Type: "string", Required: false, Default: "operator", Description: "Attention profile: operator, debug, minimal, alerts"},
			},
			Examples: []string{
				"ntm --robot-events",
				"ntm --robot-events --since-cursor=42 --events-limit=50",
				"ntm --robot-events --events-incident=inc-20260322-abc --events-window-before=5m --events-window-after=1m",
				"ntm --robot-events --events-as-of=2026-03-22T03:50:00Z --events-limit=25",
				"ntm --robot-events --events-category=agent --events-actionability=action_required",
				"ntm --robot-events --profile=debug",
			},
		},
		{
			Name:        "digest",
			Flag:        "--robot-digest",
			Category:    "attention",
			Description: "Aggregated summary of recent attention events. Returns counts, top items, and category breakdown without waiting. Use for quick status checks; prefer --robot-attention for blocking operator loops.",
			Parameters: []RobotParameter{
				{Name: "profile", Flag: "--profile", Type: "string", Required: false, Default: "operator", Description: "Attention profile: operator, debug, minimal, alerts"},
			},
			Examples: []string{
				"ntm --robot-digest",
				"ntm --robot-digest --profile=minimal",
			},
		},
		{
			Name:        "attention",
			Flag:        "--robot-attention",
			Category:    "attention",
			Description: "The canonical tending primitive: wait until attention is needed, then return a digest with wake reason and next cursor. This is the one obvious way to implement an operator loop. Combines wait + digest semantics with cursor handoff for mechanical replay.",
			Parameters: []RobotParameter{
				{Name: "attention-cursor", Flag: "--attention-cursor", Type: "int", Required: false, Default: "0", Description: "Wait for attention after this cursor"},
				{Name: "attention-session", Flag: "--attention-session", Type: "string", Required: false, Description: "Filter to a specific session"},
				{Name: "attention-timeout", Flag: "--attention-timeout", Type: "string", Required: false, Default: "5m", Description: "Maximum wait time before returning"},
				{Name: "attention-poll", Flag: "--attention-poll", Type: "string", Required: false, Default: "1s", Description: "Polling interval"},
				{Name: "profile", Flag: "--profile", Type: "string", Required: false, Default: "operator", Description: "Attention profile: operator, debug, minimal, alerts"},
				{Name: "attention-condition", Flag: "--attention-condition", Type: "string", Required: false, Default: "attention", Description: "Which condition to wait for: attention, action_required, mail_pending"},
			},
			Examples: []string{
				"ntm --robot-attention",
				"ntm --robot-attention --attention-cursor=42 --attention-timeout=2m",
				"ntm --robot-attention --attention-session=proj --profile=debug --attention-condition=action_required",
			},
		},

		// === AGENT CONTROL ===
		{
			Name:        "send",
			Flag:        "--robot-send",
			Category:    "control",
			Description: "Send message to panes atomically. Supports type filtering and tracking.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-send", Type: "string", Required: true, Description: "Session name"},
				{Name: "msg", Flag: "--msg", Type: "string", Required: true, Description: "Message content to send (or use --msg-file)"},
				{Name: "msg-file", Flag: "--msg-file", Type: "string", Required: false, Description: "Read message content from file"},
				{Name: "enter", Flag: "--enter", Type: "bool", Required: false, Description: "Send Enter after paste (default true). Alias: --submit"},
				{Name: "type", Flag: "--type", Type: "string", Required: false, Description: "Filter by agent type: claude|cc, codex|cod, gemini|gmi, cursor, windsurf, aider"},
				{Name: "all", Flag: "--all", Type: "bool", Required: false, Description: "Include user pane (default: agents only)"},
				{Name: "panes", Flag: "--panes", Type: "string", Required: false, Description: "Filter to specific pane indices"},
				{Name: "exclude", Flag: "--exclude", Type: "string", Required: false, Description: "Exclude pane indices"},
				{Name: "delay-ms", Flag: "--delay-ms", Type: "int", Required: false, Description: "Delay between sends (ms)"},
				{Name: "track", Flag: "--track", Type: "bool", Required: false, Description: "Combined send+ack: wait for response"},
				{Name: "timeout", Flag: "--timeout", Type: "string", Required: false, Default: "30s", Description: "Max wait time when --track is enabled"},
				{Name: "poll", Flag: "--poll", Type: "string", Required: false, Default: "500ms", Description: "Poll interval when --track is enabled"},
				{Name: "dry-run", Flag: "--dry-run", Type: "bool", Required: false, Description: "Preview without executing"},
			},
			Examples: []string{
				"ntm --robot-send=proj --msg='Fix auth' --type=claude",
				"ntm --robot-send=proj --msg-file=/tmp/prompt.txt --type=codex",
				"ntm --robot-send=proj --msg='draft' --enter=false",
				"ntm --robot-send=proj --msg='hello' --track --timeout=30s",
			},
		},
		{
			Name:        "ack",
			Flag:        "--robot-ack",
			Category:    "control",
			Description: "Watch for agent responses after sending a message.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-ack", Type: "string", Required: true, Description: "Session name"},
				{Name: "timeout", Flag: "--timeout", Type: "string", Required: false, Default: "30s", Description: "Max wait time (e.g., 30s, 500ms, 1m)"},
				{Name: "poll", Flag: "--poll", Type: "string", Required: false, Default: "500ms", Description: "Poll interval"},
				{Name: "type", Flag: "--type", Type: "string", Required: false, Description: "Filter by agent type"},
				{Name: "panes", Flag: "--panes", Type: "string", Required: false, Description: "Filter to specific pane indices"},
			},
			Examples: []string{"ntm --robot-ack=proj --timeout=60s --type=claude"},
		},
		{
			Name:        "interrupt",
			Flag:        "--robot-interrupt",
			Category:    "control",
			Description: "Send Ctrl+C to stop agents, optionally send a new task.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-interrupt", Type: "string", Required: true, Description: "Session name"},
				{Name: "msg", Flag: "--msg", Type: "string", Required: false, Description: "New task to send after Ctrl+C"},
				{Name: "all", Flag: "--all", Type: "bool", Required: false, Description: "Interrupt all panes including the user pane"},
				{Name: "force", Flag: "--force", Type: "bool", Required: false, Description: "Send Ctrl+C even if an agent already appears ready"},
				{Name: "type", Flag: "--type", Type: "string", Required: false, Description: "Filter by agent type"},
				{Name: "panes", Flag: "--panes", Type: "string", Required: false, Description: "Filter to specific pane indices"},
				{Name: "no-wait", Flag: "--no-wait", Type: "bool", Required: false, Description: "Return immediately after Ctrl+C without closed-loop verification"},
				{Name: "timeout", Flag: "--timeout", Type: "string", Required: false, Default: "10s", Description: "Max wait for ready state after interrupt"},
				{Name: "dry-run", Flag: "--dry-run", Type: "bool", Required: false, Description: "Preview without executing"},
			},
			Examples: []string{
				"ntm --robot-interrupt=proj --msg='Stop and fix bug'",
				"ntm --robot-interrupt=proj --all --force --timeout=15s",
			},
		},
		{
			Name:        "overlay",
			Flag:        "--robot-overlay",
			Category:    "control",
			Description: "Open the dashboard overlay for human handoff from an agent or operator loop. Runs inside tmux, can resolve the current session automatically, and can pre-focus the attention panel on a specific cursor.",
			Parameters: []RobotParameter{
				{Name: "overlay-session", Flag: "--overlay-session", Type: "string", Required: false, Description: "Session to open. Defaults to the current tmux session when omitted."},
				{Name: "overlay-cursor", Flag: "--overlay-cursor", Type: "int", Required: false, Default: "0", Description: "Pre-focus the overlay attention panel on this cursor"},
				{Name: "overlay-no-wait", Flag: "--overlay-no-wait", Type: "bool", Required: false, Description: "Return after launch instead of blocking until the popup is dismissed"},
			},
			Examples: []string{
				"ntm --robot-overlay --overlay-session=myproject",
				"ntm --robot-overlay --overlay-cursor=42 --overlay-no-wait",
			},
		},
		{
			Name:        "restart-pane",
			Flag:        "--robot-restart-pane",
			Category:    "control",
			Description: "Restart pane processes with tmux respawn-pane -k, optionally scoped by pane or agent type.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-restart-pane", Type: "string", Required: true, Description: "Session name"},
				{Name: "panes", Flag: "--panes", Type: "string", Required: false, Description: "Comma-separated pane indices to restart"},
				{Name: "type", Flag: "--type", Type: "string", Required: false, Description: "Filter restart targets by agent type"},
				{Name: "all", Flag: "--all", Type: "bool", Required: false, Description: "Include the user pane when no explicit pane filter is provided"},
				{Name: "dry-run", Flag: "--dry-run", Type: "bool", Required: false, Description: "Preview restart targets without executing"},
				{Name: "restart-bead", Flag: "--restart-bead", Type: "string", Required: false, Description: "Bead id to assign after the restart and use for the synthesized prompt"},
				{Name: "restart-prompt", Flag: "--restart-prompt", Type: "string", Required: false, Description: "Explicit prompt to send after restart; overrides the bead template"},
			},
			Examples: []string{
				"ntm --robot-restart-pane=proj --panes=1,2",
				"ntm --robot-restart-pane=proj --type=claude --dry-run",
				"ntm --robot-restart-pane=proj --panes=2 --restart-bead=bd-abc12",
			},
		},
		{
			Name:        "smart-restart",
			Flag:        "--robot-smart-restart",
			Category:    "control",
			Description: "Safely restart panes after checking whether agents are actively working.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-smart-restart", Type: "string", Required: true, Description: "Session name"},
				{Name: "panes", Flag: "--panes", Type: "string", Required: false, Description: "Comma-separated pane indices to restart"},
				{Name: "force", Flag: "--force", Type: "bool", Required: false, Description: "Force restart even if an agent appears to be working"},
				{Name: "dry-run", Flag: "--dry-run", Type: "bool", Required: false, Description: "Preview the restart plan without executing"},
				{Name: "prompt", Flag: "--prompt", Type: "string", Required: false, Description: "Prompt to send after the restart"},
				{Name: "lines", Flag: "--lines", Type: "int", Required: false, Default: "20", Description: "Lines captured per pane for safety checks"},
				{Name: "verbose", Flag: "--verbose", Type: "bool", Required: false, Description: "Include extra debugging details in the response"},
				{Name: "hard-kill", Flag: "--hard-kill", Type: "bool", Required: false, Description: "Use kill -9 fallback if the normal exit sequence fails"},
				{Name: "hard-kill-only", Flag: "--hard-kill-only", Type: "bool", Required: false, Description: "Skip the normal exit sequence and go straight to kill -9"},
			},
			Examples: []string{
				"ntm --robot-smart-restart=myproject --panes=2,3",
				"ntm --robot-smart-restart=myproject --dry-run --prompt='resume work'",
				"ntm --robot-smart-restart=myproject --panes=2 --hard-kill",
			},
		},
		{
			Name:        "wait",
			Flag:        "--robot-wait",
			Category:    "control",
			Description: "Wait for pane-state or attention-feed conditions and return a machine-usable wake result.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-wait", Type: "string", Required: true, Description: "Session name"},
				{Name: "wait-until", Flag: "--wait-until", Type: "string", Required: false, Default: "idle", Description: "Wait condition: idle, complete, generating, healthy, stalled, rate_limited, attention, action_required, mail_pending, mail_ack_required, context_hot, reservation_conflict, file_conflict, session_changed, pane_changed. bead_orphaned remains deliberately unsupported."},
				{Name: "timeout", Flag: "--timeout", Type: "string", Required: false, Default: "5m", Description: "Maximum wait time"},
				{Name: "poll", Flag: "--poll", Type: "string", Required: false, Default: "2s", Description: "Polling interval"},
				{Name: "panes", Flag: "--panes", Type: "string", Required: false, Description: "Filter to specific pane indices"},
				{Name: "type", Flag: "--type", Type: "string", Required: false, Description: "Filter by agent type"},
				{Name: "attention-cursor", Flag: "--attention-cursor", Type: "int", Required: false, Default: "0", Description: "Cursor handoff for attention-feed wait conditions"},
				{Name: "profile", Flag: "--profile", Type: "string", Required: false, Default: "operator", Description: "Attention profile for attention-feed wait conditions"},
				{Name: "any", Flag: "--any", Type: "bool", Required: false, Description: "Wait for ANY agent instead of ALL"},
				{Name: "wait-exit-on-error", Flag: "--wait-exit-on-error", Type: "bool", Required: false, Description: "Exit immediately if ERROR state detected"},
				{Name: "wait-transition", Flag: "--wait-transition", Type: "bool", Required: false, Description: "Require pane-state conditions to leave and re-enter the target state before returning"},
			},
			Examples: []string{
				"ntm --robot-wait=proj --wait-until=idle",
				"ntm --robot-wait=proj --wait-until=action_required --attention-cursor=42 --profile=operator",
				"ntm --robot-wait=proj --wait-until=idle --wait-transition --timeout=2m --panes=1,2",
			},
		},
		{
			Name:        "route",
			Flag:        "--robot-route",
			Category:    "control",
			Description: "Get routing recommendation for work distribution.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-route", Type: "string", Required: true, Description: "Session name"},
				{Name: "strategy", Flag: "--strategy", Type: "string", Required: false, Default: "least-loaded", Description: "Strategy: least-loaded, first-available, round-robin, round-robin-available, random, sticky, explicit"},
				{Name: "type", Flag: "--type", Type: "string", Required: false, Description: "Filter by agent type"},
				{Name: "exclude", Flag: "--exclude", Type: "string", Required: false, Description: "Exclude pane indices"},
			},
			Examples: []string{"ntm --robot-route=proj --strategy=least-loaded --type=claude --exclude=0"},
		},
		{
			Name:        "assign",
			Flag:        "--robot-assign",
			Category:    "control",
			Description: "Get work distribution recommendations for assigning beads to agents.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-assign", Type: "string", Required: true, Description: "Session name"},
				{Name: "beads", Flag: "--beads", Type: "string", Required: false, Description: "Specific bead IDs to assign (comma-separated)"},
				{Name: "strategy", Flag: "--strategy", Type: "string", Required: false, Default: "balanced", Description: "Strategy: balanced, speed, quality, dependency"},
			},
			Examples: []string{"ntm --robot-assign=proj --strategy=speed --beads=bd-abc,bd-xyz"},
		},
		{
			Name:        "bulk-assign",
			Flag:        "--robot-bulk-assign",
			Category:    "control",
			Description: "Bulk assign beads across idle agents using bv triage or an explicit pane-to-bead map.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-bulk-assign", Type: "string", Required: true, Description: "Session name"},
				{Name: "from-bv", Flag: "--from-bv", Type: "bool", Required: false, Description: "Select beads from bv triage"},
				{Name: "allocation", Flag: "--allocation", Type: "string", Required: false, Description: "Explicit pane-to-bead allocation JSON"},
				{Name: "bulk-strategy", Flag: "--bulk-strategy", Type: "string", Required: false, Default: "impact", Description: "Selection strategy when using --from-bv"},
				{Name: "skip-panes", Flag: "--skip-panes", Type: "string", Required: false, Description: "Comma-separated pane indices to skip"},
				{Name: "prompt-template", Flag: "--prompt-template", Type: "string", Required: false, Description: "Custom prompt template file"},
				{Name: "dry-run", Flag: "--dry-run", Type: "bool", Required: false, Description: "Preview assignments without sending prompts"},
			},
			Examples: []string{
				"ntm --robot-bulk-assign=proj --from-bv",
				"ntm --robot-bulk-assign=proj --allocation='{\"2\":\"bd-abc\"}' --dry-run",
			},
		},

		// === SPAWN ===
		{
			Name:        "spawn",
			Flag:        "--robot-spawn",
			Category:    "spawn",
			Description: "Create session with agents and optionally wait for readiness or assign initial work.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-spawn", Type: "string", Required: true, Description: "Session name to create"},
				{Name: "spawn-cc", Flag: "--spawn-cc", Type: "int", Required: false, Description: "Number of Claude agents"},
				{Name: "spawn-cod", Flag: "--spawn-cod", Type: "int", Required: false, Description: "Number of Codex agents"},
				{Name: "spawn-gmi", Flag: "--spawn-gmi", Type: "int", Required: false, Description: "Number of Gemini agents"},
				{Name: "spawn-preset", Flag: "--spawn-preset", Type: "string", Required: false, Description: "Use recipe preset instead of counts"},
				{Name: "spawn-no-user", Flag: "--spawn-no-user", Type: "bool", Required: false, Description: "Skip user pane creation"},
				{Name: "spawn-wait", Flag: "--spawn-wait", Type: "bool", Required: false, Description: "Wait for agents to show ready state before returning"},
				{Name: "timeout", Flag: "--timeout", Type: "string", Required: false, Default: "30s", Description: "Max wait for ready state when --spawn-wait is enabled"},
				{Name: "spawn-dir", Flag: "--spawn-dir", Type: "string", Required: false, Description: "Working directory for session"},
				{Name: "spawn-label", Flag: "--spawn-label", Type: "string", Required: false, Description: "Goal label for multi-session support (creates SESSION--LABEL)"},
				{Name: "spawn-assign-work", Flag: "--spawn-assign-work", Type: "bool", Required: false, Description: "Claim and assign ready work during spawn"},
				{Name: "strategy", Flag: "--strategy", Type: "string", Required: false, Default: "top-n", Description: "Assignment strategy when --spawn-assign-work is enabled: top-n, diverse, dependency-aware, skill-matched"},
				{Name: "dry-run", Flag: "--dry-run", Type: "bool", Required: false, Description: "Preview without executing"},
			},
			Examples: []string{
				"ntm --robot-spawn=myproject --spawn-cc=2 --spawn-cod=1 --spawn-wait --timeout=30s",
				"ntm --robot-spawn=myproject --spawn-preset=standard",
				"ntm --robot-spawn=myproject --spawn-label=frontend --spawn-cc=3",
				"ntm --robot-spawn=myproject --spawn-assign-work --strategy=dependency-aware",
			},
		},
		{
			Name:        "agent-names",
			Flag:        "--robot-agent-names",
			Category:    "state",
			Description: "Get stable agent-name mappings for a session.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-agent-names", Type: "string", Required: true, Description: "Session name"},
			},
			Examples: []string{"ntm --robot-agent-names=myproject"},
		},
		{
			Name:        "controller-spawn",
			Flag:        "--robot-controller-spawn",
			Category:    "spawn",
			Description: "Launch a controller agent pane inside an existing session.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-controller-spawn", Type: "string", Required: true, Description: "Session name"},
				{Name: "controller-agent-type", Flag: "--controller-agent-type", Type: "string", Required: false, Default: "cc", Description: "Controller agent type: cc, cod, gmi"},
				{Name: "controller-prompt", Flag: "--controller-prompt", Type: "string", Required: false, Description: "Custom initial prompt file"},
				{Name: "controller-no-prompt", Flag: "--controller-no-prompt", Type: "bool", Required: false, Description: "Skip sending the initial prompt"},
			},
			Examples: []string{
				"ntm --robot-controller-spawn=proj",
				"ntm --robot-controller-spawn=proj --controller-agent-type=cod --controller-no-prompt",
			},
		},
		{
			Name:        "context-inject",
			Flag:        "--robot-context-inject",
			Category:    "control",
			Description: "Inject project context files such as AGENTS.md and README.md into agent panes.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-context-inject", Type: "string", Required: true, Description: "Session name"},
				{Name: "inject-files", Flag: "--inject-files", Type: "string", Required: false, Description: "Comma-separated files to inject"},
				{Name: "inject-max-bytes", Flag: "--inject-max-bytes", Type: "int", Required: false, Default: "0", Description: "Max content size in bytes (0 = unlimited)"},
				{Name: "inject-all", Flag: "--inject-all", Type: "bool", Required: false, Description: "Include the user pane"},
				{Name: "inject-pane", Flag: "--inject-pane", Type: "int", Required: false, Default: "-1", Description: "Inject only into a specific pane index"},
				{Name: "inject-dry-run", Flag: "--inject-dry-run", Type: "bool", Required: false, Description: "Preview without sending content"},
			},
			Examples: []string{
				"ntm --robot-context-inject=myproject",
				"ntm --robot-context-inject=myproject --inject-files=AGENTS.md,README.md --inject-dry-run",
			},
		},
		{
			Name:        "ensemble_spawn",
			Flag:        "--robot-ensemble-spawn",
			Category:    "ensemble",
			Description: "Spawn a reasoning ensemble session with mode assignments.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-ensemble-spawn", Type: "string", Required: true, Description: "Session name to create"},
				{Name: "preset", Flag: "--preset", Type: "string", Required: false, Description: "Ensemble preset name (required unless --modes is set)"},
				{Name: "modes", Flag: "--modes", Type: "string", Required: false, Description: "Explicit mode IDs or codes (comma-separated)"},
				{Name: "question", Flag: "--question", Type: "string", Required: true, Description: "Question for the ensemble to analyze"},
				{Name: "agents", Flag: "--agents", Type: "string", Required: false, Description: "Agent mix (e.g., cc=2,cod=1,gmi=1)"},
				{Name: "assignment", Flag: "--assignment", Type: "string", Required: false, Default: "affinity", Description: "Assignment strategy: round-robin, affinity, category, explicit"},
				{Name: "allow-advanced", Flag: "--allow-advanced", Type: "bool", Required: false, Description: "Allow advanced/experimental modes"},
				{Name: "budget-total", Flag: "--budget-total", Type: "int", Required: false, Description: "Override total token budget"},
				{Name: "budget-per-agent", Flag: "--budget-per-agent", Type: "int", Required: false, Description: "Override per-agent token cap"},
				{Name: "no-cache", Flag: "--no-cache", Type: "bool", Required: false, Description: "Bypass context pack cache"},
				{Name: "no-questions", Flag: "--no-questions", Type: "bool", Required: false, Description: "Skip targeted questions (future)"},
				{Name: "project", Flag: "--project", Type: "string", Required: false, Description: "Project directory override"},
			},
			Examples: []string{
				"ntm --robot-ensemble-spawn=myproject --preset=project-diagnosis --question='Review architecture'",
				"ntm --robot-ensemble-spawn=myproject --modes=A1,B13 --allow-advanced --question='Analyze risks'",
			},
		},
		{
			Name:        "recipes",
			Flag:        "--robot-recipes",
			Category:    "spawn",
			Description: "List available spawn recipes/presets.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-recipes"},
		},

		// === BEADS MANAGEMENT ===
		{
			Name:        "beads-list",
			Flag:        "--robot-beads-list",
			Category:    "beads",
			Description: "List beads with filtering options.",
			Parameters: []RobotParameter{
				{Name: "beads-status", Flag: "--beads-status", Type: "string", Required: false, Description: "Filter by status: open, in_progress, closed, blocked"},
				{Name: "beads-priority", Flag: "--beads-priority", Type: "string", Required: false, Description: "Filter by priority: 0-4 or P0-P4"},
				{Name: "beads-assignee", Flag: "--beads-assignee", Type: "string", Required: false, Description: "Filter by assignee"},
				{Name: "beads-type", Flag: "--beads-type", Type: "string", Required: false, Description: "Filter by type: task, bug, feature, epic, chore"},
				{Name: "beads-limit", Flag: "--beads-limit", Type: "int", Required: false, Default: "20", Description: "Max beads to return"},
			},
			Examples: []string{"ntm --robot-beads-list --beads-status=open --beads-priority=1"},
		},
		{
			Name:        "bead-claim",
			Flag:        "--robot-bead-claim",
			Category:    "beads",
			Description: "Claim a bead for work.",
			Parameters: []RobotParameter{
				{Name: "bead-id", Flag: "--robot-bead-claim", Type: "string", Required: true, Description: "Bead ID to claim"},
				{Name: "bead-assignee", Flag: "--bead-assignee", Type: "string", Required: false, Description: "Assignee name"},
			},
			Examples: []string{"ntm --robot-bead-claim=bd-abc123 --bead-assignee=agent1"},
		},
		{
			Name:        "bead-create",
			Flag:        "--robot-bead-create",
			Category:    "beads",
			Description: "Create a new bead.",
			Parameters: []RobotParameter{
				{Name: "bead-title", Flag: "--bead-title", Type: "string", Required: true, Description: "Title for new bead"},
				{Name: "bead-type", Flag: "--bead-type", Type: "string", Required: false, Default: "task", Description: "Type: task, bug, feature, epic, chore"},
				{Name: "bead-priority", Flag: "--bead-priority", Type: "int", Required: false, Default: "2", Description: "Priority 0-4 (0=critical, 4=backlog)"},
				{Name: "bead-description", Flag: "--bead-description", Type: "string", Required: false, Description: "Description"},
				{Name: "bead-labels", Flag: "--bead-labels", Type: "string", Required: false, Description: "Comma-separated labels"},
				{Name: "bead-depends-on", Flag: "--bead-depends-on", Type: "string", Required: false, Description: "Comma-separated dependency bead IDs"},
			},
			Examples: []string{"ntm --robot-bead-create --bead-title='Fix auth bug' --bead-type=bug --bead-priority=1"},
		},
		{
			Name:        "bead-show",
			Flag:        "--robot-bead-show",
			Category:    "beads",
			Description: "Show bead details.",
			Parameters: []RobotParameter{
				{Name: "bead-id", Flag: "--robot-bead-show", Type: "string", Required: true, Description: "Bead ID to show"},
			},
			Examples: []string{"ntm --robot-bead-show=bd-abc123"},
		},
		{
			Name:        "bead-close",
			Flag:        "--robot-bead-close",
			Category:    "beads",
			Description: "Close a bead.",
			Parameters: []RobotParameter{
				{Name: "bead-id", Flag: "--robot-bead-close", Type: "string", Required: true, Description: "Bead ID to close"},
				{Name: "bead-close-reason", Flag: "--bead-close-reason", Type: "string", Required: false, Description: "Reason for closing"},
			},
			Examples: []string{"ntm --robot-bead-close=bd-abc123 --bead-close-reason='Completed'"},
		},

		// === BV INTEGRATION ===
		{
			Name:        "plan",
			Flag:        "--robot-plan",
			Category:    "bv",
			Description: "Get bv execution plan with parallelizable tracks.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-plan"},
		},
		{
			Name:        "triage",
			Flag:        "--robot-triage",
			Category:    "bv",
			Description: "Get bv triage analysis with recommendations, quick wins, and blockers.",
			Parameters: []RobotParameter{
				{Name: "triage-limit", Flag: "--triage-limit", Type: "int", Required: false, Default: "10", Description: "Max recommendations per category"},
			},
			Examples: []string{"ntm --robot-triage --triage-limit=20"},
		},
		{
			Name:        "graph",
			Flag:        "--robot-graph",
			Category:    "bv",
			Description: "Get dependency graph insights.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-graph"},
		},
		{
			Name:        "forecast",
			Flag:        "--robot-forecast",
			Category:    "bv",
			Description: "Get ETA predictions from bv.",
			Parameters: []RobotParameter{
				{Name: "target", Flag: "--robot-forecast", Type: "string", Required: true, Description: "Issue ID or 'all'"},
			},
			Examples: []string{"ntm --robot-forecast=bd-123", "ntm --robot-forecast=all"},
		},
		{
			Name:        "suggest",
			Flag:        "--robot-suggest",
			Category:    "bv",
			Description: "Get hygiene suggestions from bv.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-suggest"},
		},
		{
			Name:        "impact",
			Flag:        "--robot-impact",
			Category:    "bv",
			Description: "Get file impact analysis from bv.",
			Parameters: []RobotParameter{
				{Name: "file", Flag: "--robot-impact", Type: "string", Required: true, Description: "File path to analyze"},
			},
			Examples: []string{"ntm --robot-impact=internal/cli/root.go"},
		},
		{
			Name:        "search",
			Flag:        "--robot-search",
			Category:    "bv",
			Description: "Run semantic search against beads via bv.",
			Parameters: []RobotParameter{
				{Name: "query", Flag: "--robot-search", Type: "string", Required: true, Description: "Search query"},
				{Name: "limit", Flag: "--limit", Type: "int", Required: false, Default: "20", Description: "Max results"},
			},
			Examples: []string{"ntm --robot-search='auth error' --limit=10"},
		},
		{
			Name:        "label-attention",
			Flag:        "--robot-label-attention",
			Category:    "bv",
			Description: "Get attention-ranked labels from bv.",
			Parameters: []RobotParameter{
				{Name: "limit", Flag: "--limit", Type: "int", Required: false, Default: "10", Description: "Max labels to return"},
			},
			Examples: []string{"ntm --robot-label-attention --limit=20"},
		},
		{
			Name:        "label-flow",
			Flag:        "--robot-label-flow",
			Category:    "bv",
			Description: "Get cross-label dependency flow from bv.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-label-flow"},
		},
		{
			Name:        "label-health",
			Flag:        "--robot-label-health",
			Category:    "bv",
			Description: "Get per-label health metrics from bv.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-label-health"},
		},
		{
			Name:        "file-beads",
			Flag:        "--robot-file-beads",
			Category:    "bv",
			Description: "Get bead mappings for a file from bv.",
			Parameters: []RobotParameter{
				{Name: "file", Flag: "--robot-file-beads", Type: "string", Required: true, Description: "File path to analyze"},
				{Name: "limit", Flag: "--limit", Type: "int", Required: false, Default: "10", Description: "Max bead mappings"},
			},
			Examples: []string{"ntm --robot-file-beads=internal/cli/root.go --limit=10"},
		},
		{
			Name:        "file-hotspots",
			Flag:        "--robot-file-hotspots",
			Category:    "bv",
			Description: "Get file hotspot analysis from bv.",
			Parameters: []RobotParameter{
				{Name: "limit", Flag: "--limit", Type: "int", Required: false, Default: "10", Description: "Max hotspots"},
			},
			Examples: []string{"ntm --robot-file-hotspots --limit=10"},
		},
		{
			Name:        "file-relations",
			Flag:        "--robot-file-relations",
			Category:    "bv",
			Description: "Get file co-change relations from bv.",
			Parameters: []RobotParameter{
				{Name: "file", Flag: "--robot-file-relations", Type: "string", Required: true, Description: "File path to analyze"},
				{Name: "limit", Flag: "--limit", Type: "int", Required: false, Default: "10", Description: "Max relations"},
				{Name: "threshold", Flag: "--threshold", Type: "float", Required: false, Default: "0.0", Description: "Minimum relation weight"},
			},
			Examples: []string{"ntm --robot-file-relations=internal/cli/root.go --limit=10"},
		},

		// === CASS INTEGRATION ===
		{
			Name:        "cass-search",
			Flag:        "--robot-cass-search",
			Category:    "cass",
			Description: "Search past agent conversations.",
			Parameters: []RobotParameter{
				{Name: "query", Flag: "--robot-cass-search", Type: "string", Required: true, Description: "Search query"},
			},
			Examples: []string{"ntm --robot-cass-search='authentication error'"},
		},
		{
			Name:        "cass-context",
			Flag:        "--robot-cass-context",
			Category:    "cass",
			Description: "Get relevant past context for a task.",
			Parameters: []RobotParameter{
				{Name: "query", Flag: "--robot-cass-context", Type: "string", Required: true, Description: "Task description"},
			},
			Examples: []string{"ntm --robot-cass-context='how to implement auth'"},
		},
		{
			Name:        "cass-status",
			Flag:        "--robot-cass-status",
			Category:    "cass",
			Description: "Get CASS health and statistics.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-cass-status"},
		},
		{
			Name:        "cass-insights",
			Flag:        "--robot-cass-insights",
			Category:    "cass",
			Description: "Get aggregated CASS insights about topics, patterns, and agent activity.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-cass-insights"},
		},

		// === PIPELINE ===
		{
			Name:        "pipeline-run",
			Flag:        "--robot-pipeline-run",
			Category:    "pipeline",
			Description: "Run a workflow pipeline.",
			Parameters: []RobotParameter{
				{Name: "workflow", Flag: "--robot-pipeline-run", Type: "string", Required: true, Description: "Workflow file path"},
				{Name: "pipeline-session", Flag: "--pipeline-session", Type: "string", Required: true, Description: "Tmux session for execution"},
				{Name: "pipeline-vars", Flag: "--pipeline-vars", Type: "string", Required: false, Description: "JSON variables for pipeline"},
				{Name: "dry-run", Flag: "--dry-run", Type: "bool", Required: false, Description: "Validate without executing"},
				{Name: "pipeline-background", Flag: "--pipeline-background", Type: "bool", Required: false, Description: "Run in background"},
			},
			Examples: []string{"ntm --robot-pipeline-run=workflow.yaml --pipeline-session=proj --dry-run"},
		},
		{
			Name:        "pipeline-status",
			Flag:        "--robot-pipeline",
			Category:    "pipeline",
			Description: "Get pipeline status.",
			Parameters: []RobotParameter{
				{Name: "run-id", Flag: "--robot-pipeline", Type: "string", Required: true, Description: "Pipeline run ID"},
			},
			Examples: []string{"ntm --robot-pipeline=run-20241230-123456-abcd"},
		},
		{
			Name:        "pipeline-list",
			Flag:        "--robot-pipeline-list",
			Category:    "pipeline",
			Description: "List all tracked pipelines.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-pipeline-list"},
		},
		{
			Name:        "pipeline-cancel",
			Flag:        "--robot-pipeline-cancel",
			Category:    "pipeline",
			Description: "Cancel a running pipeline.",
			Parameters: []RobotParameter{
				{Name: "run-id", Flag: "--robot-pipeline-cancel", Type: "string", Required: true, Description: "Pipeline run ID to cancel"},
			},
			Examples: []string{"ntm --robot-pipeline-cancel=run-20241230-123456-abcd"},
		},

		// === UTILITY ===
		{
			Name:        "help",
			Flag:        "--robot-help",
			Category:    "utility",
			Description: "Get human-readable AI agent help documentation.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-help"},
		},
		{
			Name:        "version",
			Flag:        "--robot-version",
			Category:    "utility",
			Description: "Get ntm version, commit, and build info.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-version"},
		},
		{
			Name:        "capabilities",
			Flag:        "--robot-capabilities",
			Category:    "utility",
			Description: "Get complete list of robot mode commands and their parameters (this command).",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-capabilities"},
		},
		{
			Name:        "schema",
			Flag:        "--robot-schema",
			Category:    "utility",
			Description: "Generate JSON Schema for a robot response type. Use TYPE=all to emit every available schema.",
			Parameters: []RobotParameter{
				{Name: "type", Flag: "--robot-schema", Type: "string", Required: true, Description: "Schema type name or all"},
			},
			Examples: []string{
				"ntm --robot-schema=status",
				"ntm --robot-schema=all",
			},
		},
		{
			Name:        "docs",
			Flag:        "--robot-docs",
			Category:    "utility",
			Description: "Get documentation for a topic in JSON format. Topics: quickstart, commands, examples, exit-codes.",
			Parameters: []RobotParameter{
				{Name: "topic", Flag: "--robot-docs", Type: "string", Required: false, Default: "", Description: "Documentation topic. Empty returns topic index."},
			},
			Examples: []string{
				"ntm --robot-docs=\"\"",
				"ntm --robot-docs=quickstart",
				"ntm --robot-docs=exit-codes",
			},
		},
		{
			Name:        "tools",
			Flag:        "--robot-tools",
			Category:    "utility",
			Description: "Get tool inventory and health status.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-tools"},
		},
		{
			Name:        "acfs-status",
			Flag:        "--robot-acfs-status",
			Category:    "utility",
			Description: "Get setup status via ACFS (core tool availability).",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-acfs-status"},
		},
		{
			Name:        "setup-status",
			Flag:        "--robot-setup",
			Category:    "utility",
			Description: "Alias for --robot-acfs-status.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-setup"},
		},
		{
			Name:        "default-prompts",
			Flag:        "--robot-default-prompts",
			Category:    "utility",
			Description: "Get configured per-agent-type default prompts.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-default-prompts"},
		},
		{
			Name:        "profile-list",
			Flag:        "--robot-profile-list",
			Category:    "utility",
			Description: "List saved session profiles.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-profile-list"},
		},
		{
			Name:        "profile-show",
			Flag:        "--robot-profile-show",
			Category:    "utility",
			Description: "Show a saved session profile by name.",
			Parameters: []RobotParameter{
				{Name: "profile", Flag: "--robot-profile-show", Type: "string", Required: true, Description: "Profile name"},
			},
			Examples: []string{"ntm --robot-profile-show=myproject"},
		},
		{
			Name:        "jfp-status",
			Flag:        "--robot-jfp-status",
			Category:    "utility",
			Description: "Get JeffreysPrompts (JFP) health status.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-jfp-status"},
		},
		{
			Name:        "jfp-list",
			Flag:        "--robot-jfp-list",
			Category:    "utility",
			Description: "List JFP prompts (optionally filtered by category/tag).",
			Parameters: []RobotParameter{
				{Name: "category", Flag: "--category", Type: "string", Required: false, Description: "Filter by category"},
				{Name: "tag", Flag: "--tag", Type: "string", Required: false, Description: "Filter by tag"},
			},
			Examples: []string{"ntm --robot-jfp-list", "ntm --robot-jfp-list --category=debugging"},
		},
		{
			Name:        "jfp-search",
			Flag:        "--robot-jfp-search",
			Category:    "utility",
			Description: "Search JFP prompts by query.",
			Parameters: []RobotParameter{
				{Name: "query", Flag: "--robot-jfp-search", Type: "string", Required: true, Description: "Search query"},
			},
			Examples: []string{"ntm --robot-jfp-search='debugging'"},
		},
		{
			Name:        "jfp-show",
			Flag:        "--robot-jfp-show",
			Category:    "utility",
			Description: "Show a JFP prompt by ID.",
			Parameters: []RobotParameter{
				{Name: "id", Flag: "--robot-jfp-show", Type: "string", Required: true, Description: "Prompt ID"},
			},
			Examples: []string{"ntm --robot-jfp-show=prompt-123"},
		},
		{
			Name:        "jfp-suggest",
			Flag:        "--robot-jfp-suggest",
			Category:    "utility",
			Description: "Get JFP prompt suggestions for a task.",
			Parameters: []RobotParameter{
				{Name: "task", Flag: "--robot-jfp-suggest", Type: "string", Required: true, Description: "Task description"},
			},
			Examples: []string{"ntm --robot-jfp-suggest='build a REST API'"},
		},
		{
			Name:        "jfp-installed",
			Flag:        "--robot-jfp-installed",
			Category:    "utility",
			Description: "List installed Claude Code skills from JFP.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-jfp-installed"},
		},
		{
			Name:        "jfp-categories",
			Flag:        "--robot-jfp-categories",
			Category:    "utility",
			Description: "List JFP categories with counts.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-jfp-categories"},
		},
		{
			Name:        "jfp-tags",
			Flag:        "--robot-jfp-tags",
			Category:    "utility",
			Description: "List JFP tags with counts.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-jfp-tags"},
		},
		{
			Name:        "jfp-bundles",
			Flag:        "--robot-jfp-bundles",
			Category:    "utility",
			Description: "List JFP bundles.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-jfp-bundles"},
		},
		{
			Name:        "jfp-install",
			Flag:        "--robot-jfp-install",
			Category:    "utility",
			Description: "Install JFP prompt(s) by ID.",
			Parameters: []RobotParameter{
				{Name: "ids", Flag: "--robot-jfp-install", Type: "string", Required: true, Description: "Prompt ID(s), comma-separated"},
				{Name: "project", Flag: "--project", Type: "string", Required: false, Description: "Project directory override (alias: --jfp-project)"},
				{Name: "jfp-project", Flag: "--jfp-project", Type: "string", Required: false, Description: "Optional project directory for installs"},
			},
			Examples: []string{"ntm --robot-jfp-install=prompt-123", "ntm --robot-jfp-install=prompt-1,prompt-2 --jfp-project=/path/to/project"},
		},
		{
			Name:        "jfp-export",
			Flag:        "--robot-jfp-export",
			Category:    "utility",
			Description: "Export JFP prompt(s) by ID.",
			Parameters: []RobotParameter{
				{Name: "ids", Flag: "--robot-jfp-export", Type: "string", Required: true, Description: "Prompt ID(s), comma-separated"},
				{Name: "format", Flag: "--jfp-format", Type: "string", Required: false, Description: "Export format (skill or md)"},
			},
			Examples: []string{"ntm --robot-jfp-export=prompt-123", "ntm --robot-jfp-export=prompt-123 --jfp-format=md"},
		},
		{
			Name:        "jfp-update",
			Flag:        "--robot-jfp-update",
			Category:    "utility",
			Description: "Update JFP registry cache.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-jfp-update"},
		},
		{
			Name:        "ms-search",
			Flag:        "--robot-ms-search",
			Category:    "utility",
			Description: "Search Meta Skill catalog.",
			Parameters: []RobotParameter{
				{Name: "query", Flag: "--robot-ms-search", Type: "string", Required: true, Description: "Search query"},
			},
			Examples: []string{"ntm --robot-ms-search='commit workflow'"},
		},
		{
			Name:        "ms-show",
			Flag:        "--robot-ms-show",
			Category:    "utility",
			Description: "Show Meta Skill details by ID.",
			Parameters: []RobotParameter{
				{Name: "id", Flag: "--robot-ms-show", Type: "string", Required: true, Description: "Skill ID"},
			},
			Examples: []string{"ntm --robot-ms-show=commit-and-release"},
		},
		{
			Name:        "dcg-status",
			Flag:        "--robot-dcg-status",
			Category:    "utility",
			Description: "Show DCG status and configuration.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-dcg-status"},
		},
		{
			Name:        "dcg-check",
			Flag:        "--robot-dcg-check",
			Category:    "utility",
			Description: "Preflight a shell command via DCG (no execution). Aliases: --robot-guard, --cmd.",
			Parameters: []RobotParameter{
				{Name: "command", Flag: "--command", Type: "string", Required: true, Description: "Shell command to preflight"},
			},
			Examples: []string{"ntm --robot-dcg-check --command='rm -rf /tmp'"},
		},
		{
			Name:        "slb-pending",
			Flag:        "--robot-slb-pending",
			Category:    "utility",
			Description: "List pending SLB approval requests.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-slb-pending"},
		},
		{
			Name:        "slb-approve",
			Flag:        "--robot-slb-approve",
			Category:    "utility",
			Description: "Approve an SLB request by ID.",
			Parameters: []RobotParameter{
				{Name: "id", Flag: "--robot-slb-approve", Type: "string", Required: true, Description: "Request ID"},
			},
			Examples: []string{"ntm --robot-slb-approve=req-123"},
		},
		{
			Name:        "slb-deny",
			Flag:        "--robot-slb-deny",
			Category:    "utility",
			Description: "Deny an SLB request by ID.",
			Parameters: []RobotParameter{
				{Name: "id", Flag: "--robot-slb-deny", Type: "string", Required: true, Description: "Request ID"},
				{Name: "reason", Flag: "--reason", Type: "string", Required: false, Description: "Optional denial reason"},
			},
			Examples: []string{"ntm --robot-slb-deny=req-123 --reason='Too risky'"},
		},
		{
			Name:        "ru-sync",
			Flag:        "--robot-ru-sync",
			Category:    "utility",
			Description: "Run ru sync and return JSON summary.",
			Parameters: []RobotParameter{
				{Name: "dry-run", Flag: "--dry-run", Type: "bool", Required: false, Description: "Preview without executing"},
			},
			Examples: []string{
				"ntm --robot-ru-sync",
				"ntm --robot-ru-sync --dry-run",
			},
		},
		{
			Name:        "giil-fetch",
			Flag:        "--robot-giil-fetch",
			Category:    "utility",
			Description: "Download image from share URL via giil and return JSON metadata.",
			Parameters: []RobotParameter{
				{Name: "url", Flag: "--robot-giil-fetch", Type: "string", Required: true, Description: "Share URL (iCloud, Dropbox, Google Photos, Google Drive)"},
			},
			Examples: []string{
				"ntm --robot-giil-fetch=https://share.icloud.com/photos/abc123",
			},
		},
		{
			Name:        "env",
			Flag:        "--robot-env",
			Category:    "utility",
			Description: "Get environment details for agent operation for a session or the global environment.",
			Parameters: []RobotParameter{
				{Name: "target", Flag: "--robot-env", Type: "string", Required: true, Description: "Session name or global"},
			},
			Examples: []string{
				"ntm --robot-env=myproject",
				"ntm --robot-env=global",
			},
		},
		{
			Name:        "mail-check",
			Flag:        "--robot-mail-check",
			Category:    "utility",
			Description: "Check Agent Mail inboxes with filtering by project, agent, thread, or urgency.",
			Parameters: []RobotParameter{
				{Name: "mail-project", Flag: "--mail-project", Type: "string", Required: true, Description: "Project name for the mail check"},
				{Name: "mail-agent", Flag: "--mail-agent", Type: "string", Required: false, Description: "Filter to a specific agent inbox; omit to aggregate project agents"},
				{Name: "thread", Flag: "--thread", Type: "string", Required: false, Description: "Filter to a specific thread"},
				{Name: "mail-status", Flag: "--mail-status", Type: "string", Required: false, Description: "Filter by read status: read, unread, all"},
				{Name: "include-bodies", Flag: "--include-bodies", Type: "bool", Required: false, Description: "Include full message bodies"},
				{Name: "urgent-only", Flag: "--urgent-only", Type: "bool", Required: false, Description: "Only show urgent/high-priority messages"},
				{Name: "mail-verbose", Flag: "--mail-verbose", Type: "bool", Required: false, Description: "Include extra diagnostic detail"},
				{Name: "limit", Flag: "--limit", Type: "int", Required: false, Default: "20", Description: "Max messages to return (deprecated alias: --cass-limit)"},
				{Name: "mail-offset", Flag: "--mail-offset", Type: "int", Required: false, Default: "0", Description: "Pagination offset"},
				{Name: "since", Flag: "--since", Type: "string", Required: false, Description: "Filter to messages since date (YYYY-MM-DD; deprecated alias: --cass-since)"},
				{Name: "mail-until", Flag: "--mail-until", Type: "string", Required: false, Description: "Filter to messages before a date"},
			},
			Examples: []string{
				"ntm --robot-mail-check --mail-project=myproject",
				"ntm --robot-mail-check --mail-project=myproject --mail-agent=cc_1 --urgent-only",
				"ntm --robot-mail-check --mail-project=myproject --since=2025-01-01 --limit=20",
			},
		},
		{
			Name:        "support-bundle",
			Flag:        "--robot-support-bundle",
			Category:    "utility",
			Description: "Generate a support bundle with diagnostic state, logs, and optional redaction controls.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-support-bundle", Type: "string", Required: false, Description: "Optional session name; omit and use --all for every session"},
				{Name: "bundle-output", Flag: "--bundle-output", Type: "string", Required: false, Description: "Output file path"},
				{Name: "bundle-format", Flag: "--bundle-format", Type: "string", Required: false, Default: "zip", Description: "Archive format: zip or tar.gz"},
				{Name: "bundle-since", Flag: "--bundle-since", Type: "string", Required: false, Description: "Only include content newer than this duration"},
				{Name: "bundle-lines", Flag: "--bundle-lines", Type: "int", Required: false, Default: "1000", Description: "Max scrollback lines per pane"},
				{Name: "bundle-max-size", Flag: "--bundle-max-size", Type: "int", Required: false, Default: "100", Description: "Max bundle size in MB"},
				{Name: "bundle-redact", Flag: "--bundle-redact", Type: "string", Required: false, Default: "redact", Description: "Redaction mode: off, warn, redact, block"},
				{Name: "all", Flag: "--all", Type: "bool", Required: false, Description: "Include all sessions"},
				{Name: "allow-secret", Flag: "--allow-secret", Type: "bool", Required: false, Description: "Allow persistence when redaction policy would otherwise block it"},
			},
			Examples: []string{
				"ntm --robot-support-bundle=myproject",
				"ntm --robot-support-bundle --all --bundle-output=/tmp/debug.zip",
			},
		},
		{
			Name:        "rano-stats",
			Flag:        "--robot-rano-stats",
			Category:    "utility",
			Description: "Get per-agent network stats via rano.",
			Parameters: []RobotParameter{
				{Name: "panes", Flag: "--panes", Type: "string", Required: false, Description: "Comma-separated pane indices to filter (applies across sessions)"},
				{Name: "rano-window", Flag: "--rano-window", Type: "duration", Required: false, Default: "5m", Description: "Time window for stats (e.g., 5m, 1h)"},
			},
			Examples: []string{
				"ntm --robot-rano-stats",
				"ntm --robot-rano-stats --panes=2,3 --rano-window=10m",
			},
		},
		{
			Name:        "rch-status",
			Flag:        "--robot-rch-status",
			Category:    "utility",
			Description: "Get RCH status summary including worker counts.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-rch-status"},
		},
		{
			Name:        "proxy-status",
			Flag:        "--robot-proxy-status",
			Category:    "utility",
			Description: "Get rust_proxy daemon status, route metrics, and failover history.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-proxy-status"},
		},
		{
			Name:        "rch-workers",
			Flag:        "--robot-rch-workers",
			Category:    "utility",
			Description: "List RCH workers with status details.",
			Parameters: []RobotParameter{
				{Name: "worker", Flag: "--worker", Type: "string", Required: false, Description: "Filter to a specific worker name"},
			},
			Examples: []string{
				"ntm --robot-rch-workers",
				"ntm --robot-rch-workers --worker=builder-1",
			},
		},
		{
			Name:        "quota-status",
			Flag:        "--robot-quota-status",
			Category:    "utility",
			Description: "Show caut quota status for all providers.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-quota-status"},
		},
		{
			Name:        "quota-check",
			Flag:        "--robot-quota-check",
			Category:    "utility",
			Description: "Check caut quota for a specific provider.",
			Parameters: []RobotParameter{
				{Name: "provider", Flag: "--provider", Type: "string", Required: true, Description: "Provider to check (alias: --quota-check-provider)"},
			},
			Examples: []string{
				"ntm --robot-quota-check --provider=claude",
			},
		},
		{
			Name:        "account-status",
			Flag:        "--robot-account-status",
			Category:    "utility",
			Description: "Show CAAM account status, optionally filtered to one provider.",
			Parameters: []RobotParameter{
				{Name: "provider", Flag: "--provider", Type: "string", Required: false, Description: "Filter to a specific provider (alias: --account-status-provider)"},
			},
			Examples: []string{
				"ntm --robot-account-status",
				"ntm --robot-account-status --provider=claude",
			},
		},
		{
			Name:        "accounts-list",
			Flag:        "--robot-accounts-list",
			Category:    "utility",
			Description: "List CAAM accounts, optionally filtered to one provider.",
			Parameters: []RobotParameter{
				{Name: "provider", Flag: "--provider", Type: "string", Required: false, Description: "Filter to a specific provider (alias: --accounts-list-provider)"},
			},
			Examples: []string{
				"ntm --robot-accounts-list",
				"ntm --robot-accounts-list --provider=claude",
			},
		},
		{
			Name:        "switch-account",
			Flag:        "--robot-switch-account",
			Category:    "utility",
			Description: "Switch the active CAAM account for a provider, optionally targeting one pane.",
			Parameters: []RobotParameter{
				{Name: "target", Flag: "--robot-switch-account", Type: "string", Required: true, Description: "Provider or provider:account"},
				{Name: "switch-account-pane", Flag: "--switch-account-pane", Type: "string", Required: false, Description: "Restrict switching to a specific pane"},
			},
			Examples: []string{
				"ntm --robot-switch-account=claude",
				"ntm --robot-switch-account=claude:work --switch-account-pane=agent-1",
			},
		},
		{
			Name:        "xf-search",
			Flag:        "--robot-xf-search",
			Category:    "utility",
			Description: "Search the X/Twitter archive through xf.",
			Parameters: []RobotParameter{
				{Name: "query", Flag: "--robot-xf-search", Type: "string", Required: true, Description: "Search query"},
				{Name: "xf-limit", Flag: "--xf-limit", Type: "int", Required: false, Default: "20", Description: "Max results to return"},
				{Name: "xf-mode", Flag: "--xf-mode", Type: "string", Required: false, Description: "Search mode: semantic, keyword, fuzzy"},
				{Name: "xf-sort", Flag: "--xf-sort", Type: "string", Required: false, Description: "Sort order: relevance or date"},
			},
			Examples: []string{
				"ntm --robot-xf-search='error handling patterns'",
				"ntm --robot-xf-search='quota failures' --xf-limit=50 --xf-sort=date",
			},
		},
		{
			Name:        "xf-status",
			Flag:        "--robot-xf-status",
			Category:    "utility",
			Description: "Get XF installation and index health.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-xf-status"},
		},
		{
			Name:        "alerts",
			Flag:        "--robot-alerts",
			Category:    "utility",
			Description: "List active alerts with filtering.",
			Parameters: []RobotParameter{
				{Name: "alerts-severity", Flag: "--alerts-severity", Type: "string", Required: false, Description: "Filter by severity: info, warning, error, critical"},
				{Name: "alerts-type", Flag: "--alerts-type", Type: "string", Required: false, Description: "Filter by alert type"},
				{Name: "alerts-session", Flag: "--alerts-session", Type: "string", Required: false, Description: "Filter by session"},
			},
			Examples: []string{"ntm --robot-alerts --alerts-severity=critical"},
		},
		{
			Name:        "dismiss-alert",
			Flag:        "--robot-dismiss-alert",
			Category:    "utility",
			Description: "Dismiss an alert.",
			Parameters: []RobotParameter{
				{Name: "alert-id", Flag: "--robot-dismiss-alert", Type: "string", Required: false, Description: "Alert ID to dismiss; omit when using --dismiss-all"},
				{Name: "dismiss-session", Flag: "--dismiss-session", Type: "string", Required: false, Description: "Scope dismissal to session"},
				{Name: "dismiss-all", Flag: "--dismiss-all", Type: "bool", Required: false, Description: "Dismiss all matching alerts"},
			},
			Examples: []string{"ntm --robot-dismiss-alert=alert-abc123", "ntm --robot-dismiss-alert --dismiss-all"},
		},
		{
			Name:        "palette",
			Flag:        "--robot-palette",
			Category:    "utility",
			Description: "Query palette commands.",
			Parameters: []RobotParameter{
				{Name: "palette-session", Flag: "--palette-session", Type: "string", Required: false, Description: "Filter recents to session"},
				{Name: "palette-category", Flag: "--palette-category", Type: "string", Required: false, Description: "Filter by category"},
				{Name: "palette-search", Flag: "--palette-search", Type: "string", Required: false, Description: "Search commands"},
			},
			Examples: []string{"ntm --robot-palette --palette-category=quick"},
		},
		{
			Name:        "history",
			Flag:        "--robot-history",
			Category:    "utility",
			Description: "Get command history for a session.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-history", Type: "string", Required: true, Description: "Session name"},
				{Name: "pane", Flag: "--pane", Type: "string", Required: false, Description: "Filter by pane ID"},
				{Name: "type", Flag: "--type", Type: "string", Required: false, Description: "Filter by agent type"},
				{Name: "last", Flag: "--last", Type: "int", Required: false, Description: "Show last N entries"},
				{Name: "since", Flag: "--since", Type: "string", Required: false, Description: "Show entries since time"},
				{Name: "stats", Flag: "--stats", Type: "bool", Required: false, Description: "Show statistics instead of entries"},
				{Name: "limit", Flag: "--limit", Type: "int", Required: false, Default: "0", Description: "Max history entries to return"},
				{Name: "offset", Flag: "--offset", Type: "int", Required: false, Default: "0", Description: "Pagination offset for history entries"},
			},
			Examples: []string{
				"ntm --robot-history=myproject --last=10",
				"ntm --robot-history=myproject --since=1h --type=claude",
			},
		},
		{
			Name:        "replay",
			Flag:        "--robot-replay",
			Category:    "utility",
			Description: "Replay command from history.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-replay", Type: "string", Required: true, Description: "Session name"},
				{Name: "replay-id", Flag: "--replay-id", Type: "string", Required: true, Description: "History entry ID to replay"},
				{Name: "dry-run", Flag: "--dry-run", Type: "bool", Required: false, Description: "Preview without executing"},
			},
			Examples: []string{"ntm --robot-replay=myproject --replay-id=1735830245123-a1b2c3d4 --dry-run"},
		},
		{
			Name:        "tokens",
			Flag:        "--robot-tokens",
			Category:    "utility",
			Description: "Get token usage analytics.",
			Parameters: []RobotParameter{
				{Name: "tokens-days", Flag: "--tokens-days", Type: "int", Required: false, Default: "30", Description: "Days to analyze"},
				{Name: "tokens-since", Flag: "--tokens-since", Type: "string", Required: false, Description: "Analyze since date"},
				{Name: "tokens-group-by", Flag: "--tokens-group-by", Type: "string", Required: false, Default: "agent", Description: "Grouping: agent, model, day, week, month"},
				{Name: "tokens-session", Flag: "--tokens-session", Type: "string", Required: false, Description: "Filter to session"},
				{Name: "tokens-agent", Flag: "--tokens-agent", Type: "string", Required: false, Description: "Filter to agent type"},
			},
			Examples: []string{"ntm --robot-tokens --tokens-days=7 --tokens-group-by=model"},
		},
		{
			Name:        "save",
			Flag:        "--robot-save",
			Category:    "utility",
			Description: "Save session state for later restore.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-save", Type: "string", Required: true, Description: "Session name"},
				{Name: "save-output", Flag: "--save-output", Type: "string", Required: false, Description: "Output file path"},
			},
			Examples: []string{"ntm --robot-save=proj --save-output=backup.json"},
		},
		{
			Name:        "restore",
			Flag:        "--robot-restore",
			Category:    "utility",
			Description: "Restore session from saved state.",
			Parameters: []RobotParameter{
				{Name: "path", Flag: "--robot-restore", Type: "string", Required: true, Description: "Path to save file"},
				{Name: "dry-run", Flag: "--dry-run", Type: "bool", Required: false, Description: "Preview without executing"},
			},
			Examples: []string{"ntm --robot-restore=backup.json --dry-run"},
		},
		{
			Name:        "mail",
			Flag:        "--robot-mail",
			Category:    "utility",
			Description: "Get Agent Mail state.",
			Parameters:  []RobotParameter{},
			Examples:    []string{"ntm --robot-mail"},
		},
	}
}
