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
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot docs failed")
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
Terminal robot commands emit one machine-readable response on stdout and leave stderr empty whenever stdout is writable.
Command failures and machine-actionable context are carried inside the response envelope; human-only startup warnings are suppressed.`,
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
				Body: `Use --robot-capabilities for the registry-backed command and output contract.
Add --capability-compact for a bounded catalog, --capability-command=NAME for one command, or --capability-category=NAME to scope discovery.
Use --robot-schema=TYPE when you need the JSON Schema for a concrete response type.
Use --robot-docs=<topic> for topic-scoped JSON documentation.
Start with --robot-status to understand current state.`,
			},
		},
		Examples: []DocsExample{
			{
				Name:        "basic_session",
				Description: "Create a session with Claude agents",
				Command:     "ntm --robot-spawn=myproject --spawn-cc=2 --spawn-wait --timeout=30s",
				Notes:       "The --spawn-wait flag blocks until agents are ready; tune the wait with --timeout",
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
				Notes:       "Returns a cheap summary, session headers, and degraded-source indicators",
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
				Body: `--robot-status: Cheap summary surface (session headers, counts, health)
--robot-snapshot: Unified state query (sessions + beads + alerts + mail)
--robot-tail=SESSION: Capture recent pane output
--robot-watch-bead=SESSION: Capture bead mentions + current bead status
--robot-context=SESSION: Get context window usage
--robot-activity=SESSION: Classify pane work state (idle/busy/error) with optional pane/type filters
--robot-is-working=SESSION: Check if agents are busy
--robot-diagnose=SESSION: Comprehensive health check
--robot-health-restart-stuck=SESSION: Detect and restart stuck agents
--robot-probe=SESSION: Active pane responsiveness probe
--robot-pane-address=SESSION: Per-pane addressing cards (canonical selector, stable %pane_id, tmux target)`,
			},
			{
				Heading: "Agent Control",
				Body: `--robot-send=SESSION: Send message to panes
--robot-interrupt=SESSION: Send Ctrl+C to agents
--robot-restart-pane=SESSION: Immediate tmux respawn-pane -k restart with optional prompt/bead follow-up and --restart-model/--restart-agent-args relaunch overrides
--robot-exit-cli=SESSION: Graceful agent-CLI exit (double Ctrl+C choreography) preserving the pane and shell; optional --relaunch
--robot-kill-agent=SESSION: SIGTERM/SIGKILL the agent process tree, pane and shell preserved; optional --relaunch
--robot-kill-pane=SESSION: Remove specific panes (--panes required); session and sibling panes survive
--robot-smart-restart=SESSION: Safety-checked restart with optional --hard-kill / --hard-kill-only fallback
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
				Body: `--robot-capabilities: Registry-backed command/output discovery; scope with --capability-command, --capability-category, --capability-search, or --capability-compact
--robot-schema=TYPE: JSON Schema for one response type (or TYPE=all)
--robot-docs: Documentation (this command)
--robot-version: Version and build info
--robot-help: Human-readable help text
--robot-support-bundle[=SESSION]: Generate a diagnostic bundle with redaction/privacy controls
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
				Command:     "ntm --robot-spawn=myproject --spawn-cc=1 --spawn-wait --timeout=30s",
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
				Command:     "ntm --robot-send=proj --msg='Debug issue' --panes=0.1,%7",
				Notes:       "Use canonical N, W.P, or %N selectors; W.P and %N name one physical pane",
			},
			{
				Name:        "discover_one_command",
				Description: "Discover the bounded contract for one robot command",
				Command:     "ntm --robot-capabilities --capability-command=send --capability-compact",
				Notes:       "Compact one-command discovery preserves output formats and schema identity while omitting verbose examples and transport metadata",
			},
			{
				Name:        "send_and_track",
				Description: "Send prompt and wait for response",
				Command:     "ntm --robot-send=proj --msg='Quick fix' --track --timeout=60s",
				Notes:       "Blocks until agents respond or timeout",
			},
			// Monitoring
			{
				Name:        "capture_output",
				Description: "Capture recent pane output",
				Command:     "ntm --robot-tail=proj --lines=100 --panes=0.1,%7",
				Notes:       "Useful for checking progress",
			},
			{
				Name:        "check_working",
				Description: "Check if agents are working",
				Command:     "ntm --robot-is-working=proj",
				Notes:       "Returns work state and recommendations",
			},
			{
				Name:        "activity_filtered",
				Description: "Inspect activity state for specific panes and agent types",
				Command:     "ntm --robot-activity=proj --panes=0.1,%7 --activity-type=claude,codex",
				Notes:       "Useful when you want a lighter-weight pane classifier than a full diagnose pass",
			},
			{
				Name:        "wait_for_idle",
				Description: "Wait for all agents to become idle",
				Command:     "ntm --robot-wait=proj --wait-until=idle --timeout=5m",
				Notes:       "Blocks until condition met or timeout",
			},
			{
				Name:        "wait_for_attention",
				Description: "Wait for the next action-required attention signal",
				Command:     "ntm --robot-wait=proj --wait-until=action_required --attention-cursor=42 --profile=operator --timeout=2m",
				Notes:       "Useful after taking a snapshot when you want the next operator-relevant wakeup with explicit cursor handoff",
			},
			{
				Name:        "handoff_to_human",
				Description: "Open the dashboard overlay as a structured human handoff actuator",
				Command:     "ntm --robot-overlay --overlay-session=proj --overlay-cursor=42 --overlay-no-wait",
				Notes:       "Use this when an operator should jump directly to the relevant attention item instead of parsing free-form instructions",
			},
			{
				Name:        "restart_with_bead",
				Description: "Hard reset a pane and re-seed it with a bead-backed prompt",
				Command:     "ntm --robot-restart-pane=proj --panes=2 --restart-bead=bd-abc12",
				Notes:       "Useful when the pane is wedged but you want the relaunched agent to resume a specific bead immediately",
			},
			{
				Name:        "smart_restart_hard_kill",
				Description: "Escalate a safe restart to kill -9 fallback when soft exit is not working",
				Command:     "ntm --robot-smart-restart=proj --panes=2 --hard-kill",
				Notes:       "Prefer this over an immediate hard kill when you still want the usual working-state checks and structured restart sequence",
			},
			// Recovery
			{
				Name:        "delta_snapshot",
				Description: "Get state changes since timestamp",
				Command:     "ntm --robot-snapshot --since=2025-01-15T10:00:00Z",
				Notes:       "Useful for resuming after interruption",
			},
			{
				Name:        "support_bundle_redacted",
				Description: "Generate a redacted support bundle for a session",
				Command:     "ntm --robot-support-bundle=proj --bundle-since=1h --bundle-redact=redact",
				Notes:       "Use --all to collect every session, and --allow-secret only when you intentionally want to bypass privacy blocking",
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
				Body: `Robot mode uses exactly three process exit codes.
Exit code 0 indicates success, 1 indicates an error, and 2 indicates unavailable functionality.
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
			{Code: 1, Name: "ERROR", Description: "Command failed; inspect error_code, error, and hint", Recoverable: true},
			{Code: 2, Name: "UNAVAILABLE", Description: "Requested functionality is not implemented or unavailable", Recoverable: true},
		},
	}
}
