// Package robot provides machine-readable output for AI agents.
// docs.go provides the --robot-docs command for programmatic discovery of robot mode documentation.
package robot

// DocsOutput represents the output for --robot-docs
type DocsOutput struct {
	RobotResponse
	Version       string       `json:"version"`
	SchemaVersion string       `json:"schema_version"`
	Topic         string       `json:"topic"`
	Topics        []DocsTopic  `json:"topics,omitempty"`
	Content       *DocsContent `json:"content,omitempty"`
}

// DocsTopic represents an available documentation topic
type DocsTopic struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// DocsContent represents the content for a specific topic
type DocsContent struct {
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Sections    []DocsSection  `json:"sections,omitempty"`
	Examples    []DocsExample  `json:"examples,omitempty"`
	ExitCodes   []DocsExitCode `json:"exit_codes,omitempty"`
}

// DocsSection represents a documentation section
type DocsSection struct {
	Heading string `json:"heading"`
	Body    string `json:"body"`
}

// DocsExample represents a documentation example
type DocsExample struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Command     string `json:"command"`
	Notes       string `json:"notes,omitempty"`
}

// DocsExitCode represents an exit code documentation entry
type DocsExitCode struct {
	Code        int    `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Recoverable bool   `json:"recoverable"`
}

// CurrentSchemaVersion is the current schema version for robot docs
const CurrentSchemaVersion = "1.0.0"

// availableTopics lists all documentation topics
var availableTopics = []DocsTopic{
	{Name: "quickstart", Description: "Getting started with robot mode for AI agents"},
	{Name: "commands", Description: "Complete command reference with parameters and flags"},
	{Name: "examples", Description: "Common workflow examples and patterns"},
	{Name: "exit-codes", Description: "Exit code conventions and error handling"},
}

// GetDocs returns documentation for the specified topic.
// If topic is empty, returns an index of available topics.
func GetDocs(topic string) (*DocsOutput, error) {
	output := &DocsOutput{
		RobotResponse: NewRobotResponse(true),
		Version:       Version,
		SchemaVersion: CurrentSchemaVersion,
		Topic:         topic,
	}

	if topic == "" {
		// Return index of available topics
		output.Topics = availableTopics
		return output, nil
	}

	// Get content for specific topic
	content := getDocsContent(topic)
	if content == nil {
		errResp := NewErrorResponse(nil, ErrCodeInvalidFlag, "unknown topic: "+topic+". Use --robot-docs without a topic to see available topics.")
		return &DocsOutput{
			RobotResponse: errResp,
			Version:       Version,
			SchemaVersion: CurrentSchemaVersion,
			Topic:         topic,
		}, nil
	}

	output.Content = content
	return output, nil
}

// PrintDocs outputs documentation as JSON.
// This is a thin wrapper around GetDocs() for CLI output.
func PrintDocs(topic string) error {
	output, err := GetDocs(topic)
	if err != nil {
		return err
	}
	return outputJSON(output)
}

// getDocsContent returns the content for a specific topic
func getDocsContent(topic string) *DocsContent {
	switch topic {
	case "quickstart":
		return getQuickstartContent()
	case "commands":
		return getCommandsContent()
	case "examples":
		return getExamplesContent()
	case "exit-codes":
		return getExitCodesContent()
	default:
		return nil
	}
}

func getQuickstartContent() *DocsContent {
	return &DocsContent{
		Title:       "Robot Mode Quickstart",
		Description: "Getting started with ntm robot mode for AI agent integration",
		Sections: []DocsSection{
			{
				Heading: "Overview",
				Body: `Robot mode provides a JSON API for AI agents to orchestrate coding sessions in tmux.
All robot commands output JSON to stdout with diagnostic messages to stderr.
This separation enables reliable parsing while providing useful context for debugging.`,
			},
			{
				Heading: "API Design Principles",
				Body: `1. Global commands use bool flags: --robot-status, --robot-plan
2. Session-scoped commands use =SESSION syntax: --robot-send=myproj
3. Modifiers use unprefixed flags: --limit, --offset, --since, --type
4. Output is JSON by default, with TOON format for token efficiency`,
			},
			{
				Heading: "First Steps",
				Body: `1. Check system state: ntm --robot-status
2. Create a session: ntm --robot-spawn=myproject --spawn-cc=2
3. Send a prompt: ntm --robot-send=myproject --msg="implement auth"
4. Monitor progress: ntm --robot-is-working=myproject
5. Capture output: ntm --robot-tail=myproject --lines=100
6. Track one bead: ntm --robot-watch-bead=myproject --bead=bd-123`,
			},
			{
				Heading: "Discovery",
				Body: `Use --robot-capabilities for machine-readable API schema.
Use --robot-docs=<topic> for human-readable documentation.
Start with --robot-status to understand current state.`,
			},
		},
		Examples: []DocsExample{
			{
				Name:        "basic_session",
				Description: "Create a session with Claude agents",
				Command:     "ntm --robot-spawn=myproject --spawn-cc=2 --spawn-wait",
				Notes:       "The --spawn-wait flag blocks until agents are ready",
			},
			{
				Name:        "send_prompt",
				Description: "Send a prompt to all agents with response tracking",
				Command:     "ntm --robot-send=myproject --msg='Fix the authentication bug' --track",
				Notes:       "The --track flag enables response waiting",
			},
			{
				Name:        "check_state",
				Description: "Get current system state",
				Command:     "ntm --robot-status",
				Notes:       "Returns sessions, panes, agents, and alerts",
			},
		},
	}
}

func getCommandsContent() *DocsContent {
	return &DocsContent{
		Title:       "Robot Mode Commands",
		Description: "Complete reference of all robot mode commands and their parameters",
		Sections: []DocsSection{
			{
				Heading: "State Inspection",
				Body: `--robot-status: Get tmux sessions, panes, and agent states
--robot-snapshot: Unified state query (sessions + beads + alerts + mail)
--robot-tail=SESSION: Capture recent pane output
--robot-watch-bead=SESSION: Capture bead mentions + current bead status
--robot-context=SESSION: Get context window usage
--robot-is-working=SESSION: Check if agents are busy
--robot-diagnose=SESSION: Comprehensive health check
--robot-health-restart-stuck=SESSION: Detect and restart stuck agents
--robot-probe=SESSION: Active pane responsiveness probe`,
			},
			{
				Heading: "Agent Control",
				Body: `--robot-send=SESSION: Send message to panes
--robot-interrupt=SESSION: Send Ctrl+C to agents
--robot-wait=SESSION: Wait for pane state or attention-feed condition
--robot-overlay: Open dashboard overlay for human handoff (--overlay-session optional inside tmux)
--robot-route=SESSION: Get routing recommendation`,
			},
			{
				Heading: "Session Management",
				Body: `--robot-spawn=SESSION: Create session with agents
--robot-ensemble-spawn=SESSION: Spawn reasoning ensemble
--robot-recipes: List available spawn presets`,
			},
			{
				Heading: "Beads Management",
				Body: `--robot-beads-list: List beads with filtering
--robot-bead-claim=ID: Claim a bead
--robot-bead-create: Create a new bead
--robot-bead-close=ID: Close a bead`,
			},
			{
				Heading: "BV Integration",
				Body: `--robot-plan: Get execution plan with parallelizable tracks
--robot-triage: Get prioritized work recommendations
--robot-graph: Get dependency graph insights
--robot-forecast: Get ETA predictions
--robot-suggest: Get hygiene suggestions
--robot-impact: File impact analysis
--robot-search: Semantic search
--robot-label-attention: Label attention ranking
--robot-label-flow: Cross-label dependency flow
--robot-label-health: Per-label health metrics
--robot-file-beads: File-to-bead mapping
--robot-file-hotspots: File hotspot analysis
--robot-file-relations: File co-change relations`,
			},
			{
				Heading: "Utilities",
				Body: `--robot-capabilities: Machine-discoverable API schema
--robot-docs: Documentation (this command)
--robot-version: Version and build info
--robot-help: Human-readable help text
--robot-proxy-status: rust_proxy daemon/route status
--robot-slb-pending: List SLB pending approvals
--robot-slb-approve=ID: Approve SLB request
--robot-slb-deny=ID: Deny SLB request`,
			},
		},
	}
}

func getExamplesContent() *DocsContent {
	return &DocsContent{
		Title:       "Robot Mode Examples",
		Description: "Common workflow examples and usage patterns",
		Examples: []DocsExample{
			// Session Creation
			{
				Name:        "single_agent",
				Description: "Create session with single Claude agent",
				Command:     "ntm --robot-spawn=myproject --spawn-cc=1 --spawn-wait",
				Notes:       "Best for focused, single-task work",
			},
			{
				Name:        "multi_agent",
				Description: "Create session with multiple agent types",
				Command:     "ntm --robot-spawn=myproject --spawn-cc=2 --spawn-cod=1 --spawn-gmi=1",
				Notes:       "Useful for parallel work distribution",
			},
			{
				Name:        "ensemble_session",
				Description: "Spawn reasoning ensemble for analysis",
				Command:     "ntm --robot-ensemble-spawn=analysis --preset=project-diagnosis --question='Review architecture'",
				Notes:       "Requires ensemble build tag",
			},
			// Prompt Sending
			{
				Name:        "send_to_all",
				Description: "Send prompt to all agents",
				Command:     "ntm --robot-send=proj --msg='Fix authentication'",
				Notes:       "Excludes user pane by default",
			},
			{
				Name:        "send_to_type",
				Description: "Send prompt to specific agent type",
				Command:     "ntm --robot-send=proj --msg='Review code' --type=claude",
				Notes:       "Filters by agent type: claude, codex, gemini",
			},
			{
				Name:        "send_to_panes",
				Description: "Send prompt to specific panes",
				Command:     "ntm --robot-send=proj --msg='Debug issue' --panes=1,2",
				Notes:       "Use comma-separated pane indices",
			},
			{
				Name:        "send_and_track",
				Description: "Send prompt and wait for response",
				Command:     "ntm --robot-send=proj --msg='Quick fix' --track --ack-timeout=60s",
				Notes:       "Blocks until agents respond or timeout",
			},
			// Monitoring
			{
				Name:        "capture_output",
				Description: "Capture recent pane output",
				Command:     "ntm --robot-tail=proj --lines=100 --panes=1,2",
				Notes:       "Useful for checking progress",
			},
			{
				Name:        "check_working",
				Description: "Check if agents are working",
				Command:     "ntm --robot-is-working=proj",
				Notes:       "Returns work state and recommendations",
			},
			{
				Name:        "wait_for_idle",
				Description: "Wait for all agents to become idle",
				Command:     "ntm --robot-wait=proj --wait-until=idle --wait-timeout=5m",
				Notes:       "Blocks until condition met or timeout",
			},
			{
				Name:        "wait_for_attention",
				Description: "Wait for the next action-required attention signal",
				Command:     "ntm --robot-wait=proj --wait-until=action_required --wait-timeout=2m",
				Notes:       "Useful after taking a snapshot when you want the next operator-relevant wakeup",
			},
			{
				Name:        "handoff_to_human",
				Description: "Open the dashboard overlay as a structured human handoff actuator",
				Command:     "ntm --robot-overlay --overlay-session=proj --overlay-cursor=42 --overlay-no-wait",
				Notes:       "Use this when an operator should jump directly to the relevant attention item instead of parsing free-form instructions",
			},
			// Recovery
			{
				Name:        "delta_snapshot",
				Description: "Get state changes since timestamp",
				Command:     "ntm --robot-snapshot --since=2025-01-15T10:00:00Z",
				Notes:       "Useful for resuming after interruption",
			},
			{
				Name:        "diagnose_session",
				Description: "Diagnose and auto-fix issues",
				Command:     "ntm --robot-diagnose=proj --diagnose-fix",
				Notes:       "Attempts automatic fixes for common issues",
			},
		},
	}
}

func getExitCodesContent() *DocsContent {
	return &DocsContent{
		Title:       "Exit Codes",
		Description: "Exit code conventions for robot mode commands",
		Sections: []DocsSection{
			{
				Heading: "Overview",
				Body: `Robot mode uses standard Unix exit codes with extensions for specific conditions.
Exit code 0 always indicates success. Non-zero codes indicate various error conditions.
All errors include a JSON response with error_code and error fields for programmatic handling.`,
			},
			{
				Heading: "Error Handling",
				Body: `When a command fails:
1. Exit code is non-zero
2. JSON output includes success=false
3. error_code provides machine-readable category
4. error provides human-readable message
5. Recoverable errors may include suggestions in _agent_hints`,
			},
		},
		ExitCodes: []DocsExitCode{
			{Code: 0, Name: "SUCCESS", Description: "Command completed successfully", Recoverable: true},
			{Code: 1, Name: "GENERAL_ERROR", Description: "General error (check error field for details)", Recoverable: true},
			{Code: 2, Name: "INVALID_ARGS", Description: "Invalid or missing command arguments", Recoverable: true},
			{Code: 3, Name: "SESSION_NOT_FOUND", Description: "Specified tmux session does not exist", Recoverable: true},
			{Code: 4, Name: "SESSION_EXISTS", Description: "Session already exists (for spawn commands)", Recoverable: true},
			{Code: 5, Name: "PANE_NOT_FOUND", Description: "Specified pane does not exist", Recoverable: true},
			{Code: 6, Name: "TIMEOUT", Description: "Operation timed out", Recoverable: true},
			{Code: 7, Name: "NO_AGENTS", Description: "No agents found matching criteria", Recoverable: true},
			{Code: 8, Name: "AGENT_BUSY", Description: "Agent is busy and cannot accept new work", Recoverable: true},
			{Code: 10, Name: "BEAD_NOT_FOUND", Description: "Specified bead does not exist", Recoverable: true},
			{Code: 11, Name: "BEAD_CONFLICT", Description: "Bead state conflict (e.g., already closed)", Recoverable: true},
			{Code: 20, Name: "TOOL_NOT_FOUND", Description: "External tool (br, bv, cass) not installed", Recoverable: false},
			{Code: 21, Name: "TOOL_ERROR", Description: "External tool returned an error", Recoverable: true},
			{Code: 30, Name: "TMUX_NOT_FOUND", Description: "tmux is not installed", Recoverable: false},
			{Code: 31, Name: "TMUX_ERROR", Description: "tmux command failed", Recoverable: true},
			{Code: 40, Name: "CONFIG_ERROR", Description: "Configuration file error", Recoverable: true},
			{Code: 50, Name: "INTERNAL_ERROR", Description: "Internal error (please report)", Recoverable: false},
		},
	}
}
