// Package robot provides machine-readable output for AI agents.
// capabilities.go provides the --robot-capabilities command for programmatic discovery of robot mode features.
package robot

import "sort"

// CapabilitiesOutput represents the output for --robot-capabilities
type CapabilitiesOutput struct {
	RobotResponse
	Version    string             `json:"version"`
	Commands   []RobotCommandInfo `json:"commands"`
	Categories []string           `json:"categories"`
}

// RobotCommandInfo describes a single robot command
type RobotCommandInfo struct {
	Name        string           `json:"name"`
	Flag        string           `json:"flag"`
	Category    string           `json:"category"`
	Description string           `json:"description"`
	Parameters  []RobotParameter `json:"parameters"`
	Examples    []string         `json:"examples"`
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
	commands := buildCommandRegistry()

	// Sort commands by category then name for stable output
	sort.Slice(commands, func(i, j int) bool {
		if commands[i].Category != commands[j].Category {
			return categoryIndex(commands[i].Category) < categoryIndex(commands[j].Category)
		}
		return commands[i].Name < commands[j].Name
	})

	return &CapabilitiesOutput{
		RobotResponse: NewRobotResponse(true),
		Version:       Version,
		Commands:      commands,
		Categories:    categoryOrder,
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
			Name:        "ensemble-synthesize",
			Flag:        "--robot-ensemble-synthesize",
			Category:    "ensemble",
			Description: "Trigger synthesis for an ensemble session, combining mode outputs into a unified report.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-ensemble-synthesize", Type: "string", Required: true, Description: "Session name with ensemble to synthesize"},
				{Name: "strategy", Flag: "--strategy", Type: "string", Required: false, Default: "manual", Description: "Synthesis strategy: manual, adversarial, consensus, creative, analytical, deliberative, prioritized, dialectical, meta-reasoning, voting, argumentation-graph"},
				{Name: "format", Flag: "--format", Type: "string", Required: false, Default: "markdown", Description: "Output format: markdown, json, yaml"},
				{Name: "output", Flag: "--output", Type: "string", Required: false, Description: "Path to write the synthesis report"},
				{Name: "force", Flag: "--force", Type: "bool", Required: false, Description: "Synthesize even if some outputs are incomplete"},
			},
			Examples: []string{
				"ntm --robot-ensemble-synthesize=myproject",
				"ntm --robot-ensemble-synthesize=myproject --strategy=adversarial --format=json",
				"ntm --robot-ensemble-synthesize=myproject --output=/tmp/report.md --force",
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
				{Name: "activity-type", Flag: "--activity-type", Type: "string", Required: false, Description: "Filter by agent type: claude, codex, gemini"},
			},
			Examples: []string{"ntm --robot-activity=myproject --activity-type=claude"},
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
				{Name: "diff-since", Flag: "--diff-since", Type: "string", Required: false, Default: "15m", Description: "Duration to look back"},
			},
			Examples: []string{"ntm --robot-diff=myproject --diff-since=10m"},
		},
		{
			Name:        "summary",
			Flag:        "--robot-summary",
			Category:    "state",
			Description: "Get session activity summary with agent metrics.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-summary", Type: "string", Required: true, Description: "Session name"},
				{Name: "summary-since", Flag: "--summary-since", Type: "string", Required: false, Default: "30m", Description: "Duration to look back"},
			},
			Examples: []string{"ntm --robot-summary=myproject --summary-since=1h"},
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
				{Name: "type", Flag: "--type", Type: "string", Required: false, Description: "Filter by agent type: claude|cc, codex|cod, gemini|gmi"},
				{Name: "all", Flag: "--all", Type: "bool", Required: false, Description: "Include user pane (default: agents only)"},
				{Name: "panes", Flag: "--panes", Type: "string", Required: false, Description: "Filter to specific pane indices"},
				{Name: "exclude", Flag: "--exclude", Type: "string", Required: false, Description: "Exclude pane indices"},
				{Name: "delay-ms", Flag: "--delay-ms", Type: "int", Required: false, Description: "Delay between sends (ms)"},
				{Name: "track", Flag: "--track", Type: "bool", Required: false, Description: "Combined send+ack: wait for response"},
				{Name: "dry-run", Flag: "--dry-run", Type: "bool", Required: false, Description: "Preview without executing"},
			},
			Examples: []string{
				"ntm --robot-send=proj --msg='Fix auth' --type=claude",
				"ntm --robot-send=proj --msg-file=/tmp/prompt.txt --type=codex",
				"ntm --robot-send=proj --msg='draft' --enter=false",
				"ntm --robot-send=proj --msg='hello' --track --ack-timeout=30s",
			},
		},
		{
			Name:        "ack",
			Flag:        "--robot-ack",
			Category:    "control",
			Description: "Watch for agent responses after sending a message.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-ack", Type: "string", Required: true, Description: "Session name"},
				{Name: "ack-timeout", Flag: "--ack-timeout", Type: "string", Required: false, Default: "30s", Description: "Max wait time (e.g., 30s, 5000ms, 1m)"},
				{Name: "ack-poll", Flag: "--ack-poll", Type: "int", Required: false, Default: "500", Description: "Poll interval in ms"},
				{Name: "type", Flag: "--type", Type: "string", Required: false, Description: "Filter by agent type"},
				{Name: "panes", Flag: "--panes", Type: "string", Required: false, Description: "Filter to specific pane indices"},
			},
			Examples: []string{"ntm --robot-ack=proj --ack-timeout=60s --type=claude"},
		},
		{
			Name:        "interrupt",
			Flag:        "--robot-interrupt",
			Category:    "control",
			Description: "Send Ctrl+C to stop agents, optionally send a new task.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-interrupt", Type: "string", Required: true, Description: "Session name"},
				{Name: "interrupt-msg", Flag: "--interrupt-msg", Type: "string", Required: false, Description: "New task to send after Ctrl+C"},
				{Name: "interrupt-all", Flag: "--interrupt-all", Type: "bool", Required: false, Description: "Interrupt all panes including user"},
				{Name: "type", Flag: "--type", Type: "string", Required: false, Description: "Filter by agent type"},
				{Name: "panes", Flag: "--panes", Type: "string", Required: false, Description: "Filter to specific pane indices"},
				{Name: "dry-run", Flag: "--dry-run", Type: "bool", Required: false, Description: "Preview without executing"},
			},
			Examples: []string{"ntm --robot-interrupt=proj --interrupt-msg='Stop and fix bug'"},
		},
		{
			Name:        "restart-pane",
			Flag:        "--robot-restart-pane",
			Category:    "control",
			Description: "Restart pane process (kill and respawn agent).",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-restart-pane", Type: "string", Required: true, Description: "Session name"},
				{Name: "panes", Flag: "--panes", Type: "string", Required: true, Description: "Pane indices to restart"},
			},
			Examples: []string{"ntm --robot-restart-pane=proj --panes=1,2"},
		},
		{
			Name:        "wait",
			Flag:        "--robot-wait",
			Category:    "control",
			Description: "Wait for agents to reach a specific state.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-wait", Type: "string", Required: true, Description: "Session name"},
				{Name: "wait-until", Flag: "--wait-until", Type: "string", Required: false, Default: "idle", Description: "Wait condition: idle, complete, generating, healthy"},
				{Name: "wait-timeout", Flag: "--wait-timeout", Type: "string", Required: false, Default: "5m", Description: "Maximum wait time"},
				{Name: "wait-poll", Flag: "--wait-poll", Type: "string", Required: false, Default: "2s", Description: "Polling interval"},
				{Name: "wait-panes", Flag: "--wait-panes", Type: "string", Required: false, Description: "Filter to specific pane indices"},
				{Name: "wait-type", Flag: "--wait-type", Type: "string", Required: false, Description: "Filter by agent type"},
				{Name: "wait-any", Flag: "--wait-any", Type: "bool", Required: false, Description: "Wait for ANY agent instead of ALL"},
				{Name: "wait-exit-on-error", Flag: "--wait-exit-on-error", Type: "bool", Required: false, Description: "Exit immediately if ERROR state detected"},
				{Name: "wait-transition", Flag: "--wait-transition", Type: "bool", Required: false, Description: "Require state transition before returning"},
			},
			Examples: []string{
				"ntm --robot-wait=proj --wait-until=idle",
				"ntm --robot-wait=proj --wait-until=idle --wait-transition --wait-timeout=2m",
			},
		},
		{
			Name:        "route",
			Flag:        "--robot-route",
			Category:    "control",
			Description: "Get routing recommendation for work distribution.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-route", Type: "string", Required: true, Description: "Session name"},
				{Name: "route-strategy", Flag: "--route-strategy", Type: "string", Required: false, Default: "least-loaded", Description: "Strategy: least-loaded, first-available, round-robin, random, sticky, explicit"},
				{Name: "route-type", Flag: "--route-type", Type: "string", Required: false, Description: "Filter by agent type"},
				{Name: "route-exclude", Flag: "--route-exclude", Type: "string", Required: false, Description: "Exclude pane indices"},
			},
			Examples: []string{"ntm --robot-route=proj --route-strategy=least-loaded --route-type=claude"},
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

		// === SPAWN ===
		{
			Name:        "spawn",
			Flag:        "--robot-spawn",
			Category:    "spawn",
			Description: "Create session with agents.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-spawn", Type: "string", Required: true, Description: "Session name to create"},
				{Name: "spawn-cc", Flag: "--spawn-cc", Type: "int", Required: false, Description: "Number of Claude agents"},
				{Name: "spawn-cod", Flag: "--spawn-cod", Type: "int", Required: false, Description: "Number of Codex agents"},
				{Name: "spawn-gmi", Flag: "--spawn-gmi", Type: "int", Required: false, Description: "Number of Gemini agents"},
				{Name: "spawn-preset", Flag: "--spawn-preset", Type: "string", Required: false, Description: "Use recipe preset instead of counts"},
				{Name: "spawn-no-user", Flag: "--spawn-no-user", Type: "bool", Required: false, Description: "Skip user pane creation"},
				{Name: "spawn-dir", Flag: "--spawn-dir", Type: "string", Required: false, Description: "Working directory for session"},
				{Name: "dry-run", Flag: "--dry-run", Type: "bool", Required: false, Description: "Preview without executing"},
			},
			Examples: []string{
				"ntm --robot-spawn=myproject --spawn-cc=2 --spawn-cod=1",
				"ntm --robot-spawn=myproject --spawn-preset=standard",
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
				{Name: "pipeline-dry-run", Flag: "--pipeline-dry-run", Type: "bool", Required: false, Description: "Validate without executing"},
				{Name: "pipeline-background", Flag: "--pipeline-background", Type: "bool", Required: false, Description: "Run in background"},
			},
			Examples: []string{"ntm --robot-pipeline-run=workflow.yaml --pipeline-session=proj"},
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
			Description: "Get AI agent help documentation.",
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
				{Name: "alert-id", Flag: "--robot-dismiss-alert", Type: "string", Required: true, Description: "Alert ID to dismiss"},
				{Name: "dismiss-session", Flag: "--dismiss-session", Type: "string", Required: false, Description: "Scope dismissal to session"},
				{Name: "dismiss-all", Flag: "--dismiss-all", Type: "bool", Required: false, Description: "Dismiss all matching alerts"},
			},
			Examples: []string{"ntm --robot-dismiss-alert=alert-abc123"},
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
				{Name: "history-pane", Flag: "--history-pane", Type: "string", Required: false, Description: "Filter by pane ID"},
				{Name: "history-type", Flag: "--history-type", Type: "string", Required: false, Description: "Filter by agent type"},
				{Name: "history-last", Flag: "--history-last", Type: "int", Required: false, Description: "Show last N entries"},
				{Name: "history-since", Flag: "--history-since", Type: "string", Required: false, Description: "Show entries since time"},
				{Name: "history-stats", Flag: "--history-stats", Type: "bool", Required: false, Description: "Show statistics instead of entries"},
				{Name: "robot-limit", Flag: "--robot-limit", Type: "int", Required: false, Default: "0", Description: "Max history entries to return (alias: --limit)"},
				{Name: "robot-offset", Flag: "--robot-offset", Type: "int", Required: false, Default: "0", Description: "Pagination offset for history entries (alias: --offset)"},
			},
			Examples: []string{"ntm --robot-history=myproject --history-last=10"},
		},
		{
			Name:        "replay",
			Flag:        "--robot-replay",
			Category:    "utility",
			Description: "Replay command from history.",
			Parameters: []RobotParameter{
				{Name: "session", Flag: "--robot-replay", Type: "string", Required: true, Description: "Session name"},
				{Name: "replay-id", Flag: "--replay-id", Type: "string", Required: true, Description: "History entry ID to replay"},
				{Name: "replay-dry-run", Flag: "--replay-dry-run", Type: "bool", Required: false, Description: "Preview without executing"},
			},
			Examples: []string{"ntm --robot-replay=myproject --replay-id=1735830245123-a1b2c3d4"},
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
