// Package robot provides machine-readable output for AI agents and automation.
// Use --robot-* flags to get JSON output suitable for piping to other tools.
package robot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/models"
	"github.com/Dicklesworthstone/ntm/internal/recipe"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Build info - these will be set by the caller from cli package
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
	BuiltBy = "unknown"
)

// outputFormat controls the output serialization format for robot commands.
// Access via GetOutputFormat/SetOutputFormat for thread safety.
var (
	outputMu     sync.RWMutex
	outputFormat RobotFormat = FormatAuto
)

// GetOutputFormat returns the current output format (thread-safe).
func GetOutputFormat() RobotFormat {
	outputMu.RLock()
	defer outputMu.RUnlock()
	return outputFormat
}

// SetOutputFormat sets the output format (thread-safe).
func SetOutputFormat(f RobotFormat) {
	outputMu.Lock()
	defer outputMu.Unlock()
	outputFormat = f
}

var (
	projectionStoreMu sync.RWMutex
	projectionStore   *state.Store
)

const (
	statusSchemaVersion       = "ntm.robot.status.v2"
	snapshotSchemaVersion     = "ntm.robot.snapshot.v1"
	statusLiveCollectionLimit = 2 * time.Second
)

// SetProjectionStore configures the shared runtime projection store used by
// store-backed robot surfaces such as --robot-status.
func SetProjectionStore(store *state.Store) {
	projectionStoreMu.Lock()
	defer projectionStoreMu.Unlock()
	projectionStore = store
}

func currentProjectionStore() *state.Store {
	projectionStoreMu.RLock()
	defer projectionStoreMu.RUnlock()
	return projectionStore
}

func resolveProjectionStore(explicit *state.Store, hasExplicit bool) *state.Store {
	if hasExplicit {
		return explicit
	}
	return currentProjectionStore()
}

type robotHelpSection struct {
	Title        string
	SurfaceNames []string
	Postlude     string
}

var robotHelpSections = []robotHelpSection{
	{
		Title: "Core Commands",
		SurfaceNames: []string{
			"status",
			"snapshot",
			"capabilities",
			"version",
		},
	},
	{
		Title: "Session Operations",
		SurfaceNames: []string{
			"spawn",
			"controller-spawn",
			"agent-names",
			"context-inject",
			"ensemble_spawn",
			"send",
			"tail",
			"ensemble",
			"ensemble-suggest",
			"ensemble-stop",
			"interrupt",
			"overlay",
			"is-working",
			"restart-pane",
			"smart-restart",
			"wait",
		},
		Postlude: `                        Conditions: idle, complete, generating, healthy, stalled, rate_limited
                                    attention, action_required, mail_pending, mail_ack_required
                                    context_hot, reservation_conflict, file_conflict, session_changed, pane_changed
                        Note: use --attention-cursor and --profile for attention-feed waits.
                        Note: bead_orphaned is deliberately unsupported — see --robot-capabilities

Note: Pane-targeting commands exclude the user pane by default.
Use --all to include the user pane (index depends on tmux pane-base-index).
`,
	},
	{
		Title: "Work Distribution",
		SurfaceNames: []string{
			"assign",
			"bulk-assign",
			"beads-list",
			"bead-claim",
			"bead-create",
			"bead-close",
		},
	},
	{
		Title: "Analysis & Monitoring",
		SurfaceNames: []string{
			"triage",
			"plan",
			"graph",
			"context",
			"health",
			"activity",
			"agent-health",
			"health-oauth",
			"diagnose",
			"errors",
			"logs",
			"monitor",
			"support-bundle",
		},
	},
	{
		Title: "Tool Bridges",
		SurfaceNames: []string{
			"cass-search",
			"cass-context",
			"cass-insights",
			"schema",
			"mail-check",
			"env",
			"quota-status",
			"quota-check",
			"account-status",
			"accounts-list",
			"switch-account",
			"xf-search",
			"xf-status",
			"acfs-status",
			"setup-status",
			"default-prompts",
			"profile-list",
			"profile-show",
			"giil-fetch",
			"jfp-search",
			"jfp-export",
			"jfp-update",
			"ms-search",
			"ms-show",
			"slb-pending",
			"slb-approve",
			"slb-deny",
			"tokens",
			"history",
		},
	},
}

// RenderHelp returns AI agent help documentation.
func RenderHelp() string {
	var builder strings.Builder
	builder.WriteString(`ntm (Named Tmux Manager) AI Agent Interface
=============================================
Robot mode provides a JSON API for AI agents to orchestrate coding sessions.

API Design Principles (see docs/robot-api-design.md):
-----------------------------------------------------
1. Global commands: bool flags (--robot-status, --robot-plan)
2. Session-scoped: =SESSION syntax (--robot-send=myproj, --robot-tail=myproj)
3. Modifiers: unprefixed global flags (--limit, --offset, and selected shared flags like --since and --type)
4. Output: JSON by default, TOON for token-efficient (--robot-format=toon)
`)
	for _, section := range robotHelpSections {
		builder.WriteString(renderHelpCommandSection(section))
		builder.WriteString("\n")
		if section.Postlude != "" {
			builder.WriteString(section.Postlude)
		}
	}
	builder.WriteString(`
Output Formats:
---------------
--robot-format=json     Full JSON (default)
--robot-format=toon     TOON format (~39% fewer tokens than JSON, measured)
--robot-markdown        Markdown tables (~84% fewer tokens than JSON, measured)
--robot-terse           Single-line state summary

Common Modifiers:
-----------------
--limit=N       Max results (works with search, list commands)
--offset=N      Pagination offset for list commands
--robot-limit=N  Explicit pagination alias for robot list outputs
--robot-offset=N Explicit pagination alias for robot list outputs
--since=VALUE   Time filter for commands that support it (history, diff, and summary accept duration or RFC3339; snapshot requires RFC3339; mail-check uses YYYY-MM-DD)
--type=TYPE     Agent type filter for commands that support it (claude, codex, antigravity, grok, omp, gemini, cursor, windsurf, aider)
--timeout=VALUE Shared timeout for wait/ack/interrupt and spawn --spawn-wait
--poll=VALUE    Shared polling interval for wait/ack/send --track
--strategy=NAME Strategy override for assign, route, and spawn --spawn-assign-work
--exclude=X,Y   Exclude pane indices for commands that support it
--msg=TEXT      Shared message payload for send, ack echo detection, interrupt retasks, and route affinity
--panes=X,Y     Pane filter (comma-separated indices)
--all           Include the user pane for commands that support it
--force         Force commands that support it past their normal safety checks
--dry-run       Preview without executing
--verbose       Detailed output

Quick Start:
------------
1) Create session:    ntm --robot-spawn=proj --spawn-cc=2 --spawn-wait
2) Check state:       ntm --robot-status
3) Send prompt:       ntm --robot-send=proj --msg="implement auth" --track
4) Monitor progress:  ntm --robot-is-working=proj
5) Get output:        ntm --robot-tail=proj --lines=100

Attention Feed (Operator Loop):
-------------------------------
The recommended tending loop for operator agents:

  1. Bootstrap:  ntm --robot-snapshot
     → Get system state + latest_cursor + replay_window
  2. Attend:     ntm --robot-attention --attention-cursor=<cursor>
     → Sleep until attention needed, wake with digest + reason
  3. Act:        ntm --robot-send=proj --msg="fix X"
     → Execute the suggested action
  4. Loop:       Use cursor from step 2 response as --attention-cursor
     → Repeat from step 2

If cursor expires: re-run --robot-snapshot to resync.

Attention Feed Commands:
  --robot-events         Raw event replay (--since-cursor=N, --events-limit=50)
  --robot-digest         Aggregated summary of recent changes
  --robot-attention      Wait-then-digest (the one obvious tending command)
  --robot-overlay        Human handoff actuator (optionally non-blocking with --overlay-no-wait)

Profiles (--profile=NAME):
  operator    Default. Shows actionable events + important state changes.
  debug       Full verbosity for troubleshooting.
  minimal     Only critical items requiring immediate action.
  alerts      Only synthesized alert events.

Rollout Guardrails:
- ntm is the nervous system, not a planner: it emits observable state and executes commands, but does not invent tasks or hidden coordinator state.
- Prefer --robot-attention for the steady-state loop; use --robot-events for replay/debug and --robot-digest for non-blocking summaries.
- On CURSOR_EXPIRED or stale replay, re-run --robot-snapshot to resync instead of guessing missed history.
- Unsupported conditions stay explicit until ntm can prove them from observable state; do not treat bead_orphaned as implemented.

Unsupported Conditions:
  bead_orphaned   Not supported — ntm cannot prove abandonment from observable state.
                  Use --robot-capabilities for full rationale.

Common Workflows:
-----------------
- Single agent: ntm --robot-spawn=proj --spawn-cc=1 --spawn-wait
- Send+wait:    ntm --robot-send=proj --msg="do X" --track
- Handoff:      ntm --robot-overlay --overlay-session=proj --overlay-cursor=42 --overlay-no-wait
- Bootstrap:    ntm --robot-snapshot   # use latest_cursor + replay_window for follow-up
- Tending:      ntm --robot-attention --attention-cursor=42  # wait-then-digest loop
- Recover:      ntm --robot-snapshot   # resync after cursor expiration

Tips for AI Agents:
-------------------
- Start with --robot-snapshot for bootstrap, then --robot-attention for steady-state.
- Use --robot-events for raw replay when you need full event history.
- Use --robot-digest for a quick summary without waiting.
- Use --robot-overlay to hand a specific cursor or session back to a human inside tmux.
- Snapshot returns latest_cursor plus replay_window metadata for mechanical resync.
- Prefer --robot-capabilities for schema discovery over parsing help text.
- Use --robot-send --verify-render when a bounded before/after pane capture must confirm delivery changed rendered output; inspect render_evidence and require delivered_and_rendered for every target. This confirms rendered delivery, not agent comprehension.
- Pass --op-id=ID to --robot-send or --robot-interrupt when you may retry after a timeout: an identical retry replays the recorded outcome instead of sending (or interrupting) again, a conflicting reuse fails IDEMPOTENCY_CONFLICT, and --robot-send-receipt=ID reports the outcome.
- Profiles reduce filter boilerplate: --profile=operator is the default.
- Attention feed is a sensing/actuation surface, not a planner: it does not assign beads, infer intent, or replace beads, bv, or Agent Mail.

For complete API documentation: docs/robot-api-design.md
For machine-readable schema:    ntm --robot-capabilities
`)
	return builder.String()
}

// PrintHelp outputs AI agent help documentation.
func PrintHelp() {
	_, _ = io.WriteString(os.Stdout, RenderHelp())
}

func renderHelpCommandSection(section robotHelpSection) string {
	registry := GetRobotRegistry()
	if registry == nil {
		return ""
	}

	var builder strings.Builder
	builder.WriteString(section.Title)
	builder.WriteString(":\n")
	builder.WriteString(strings.Repeat("-", len(section.Title)+1))
	builder.WriteString("\n")

	for _, name := range section.SurfaceNames {
		surface, ok := registry.Surface(name)
		if !ok {
			continue
		}
		summary := firstNonEmptyString(surface.Summary, surface.Description) + robotHelpRequiredParameterHints(surface)
		builder.WriteString(fmt.Sprintf("%-24s %s\n", robotHelpFlagUsage(surface), summary))
	}

	return builder.String()
}

func robotHelpFlagUsage(surface RobotSurfaceDescriptor) string {
	for _, param := range surface.Parameters {
		if param.Required && param.Flag == surface.Flag {
			return surface.Flag + "=" + robotHelpPlaceholder(param.Name)
		}
	}
	return surface.Flag
}

func robotHelpPlaceholder(name string) string {
	switch normalizeRobotRegistryName(name) {
	case "session":
		return "SESSION"
	case "query":
		return "QUERY"
	case "url":
		return "URL"
	case "id", "bead-id", "alert-id":
		return "ID"
	case "workflow":
		return "WORKFLOW"
	default:
		return strings.ToUpper(strings.ReplaceAll(normalizeRobotRegistryName(name), "-", "_"))
	}
}

func robotHelpRequiredParameterHints(surface RobotSurfaceDescriptor) string {
	hints := make([]string, 0, len(surface.Parameters))
	for _, param := range surface.Parameters {
		if !param.Required || param.Flag == surface.Flag {
			continue
		}
		hints = append(hints, robotHelpParameterUsage(param))
	}
	if len(hints) == 0 {
		return ""
	}
	return " Requires " + strings.Join(hints, ", ") + "."
}

func robotHelpParameterUsage(param RobotParameter) string {
	if param.Type == "bool" {
		return param.Flag
	}
	return param.Flag + "=" + robotHelpPlaceholder(param.Name)
}

// VersionOutput represents the output for --robot-version
type VersionOutput struct {
	RobotResponse
	System SystemInfo `json:"system"`
}

// GetVersion returns version information.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetVersion() (*VersionOutput, error) {
	return &VersionOutput{
		RobotResponse: NewRobotResponse(true),
		System: SystemInfo{
			Version:   Version,
			Commit:    Commit,
			BuildDate: Date,
			GoVersion: runtime.Version(),
			OS:        runtime.GOOS,
			Arch:      runtime.GOARCH,
		},
	}, nil
}

// PrintVersion outputs version as JSON.
// This is a thin wrapper around GetVersion() for CLI output.
func PrintVersion() error {
	output, err := GetVersion()
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot version failed")
}

func detectAgentType(title string) string {
	// Try to detect from pane title
	titleLower := strings.ToLower(title)

	// Check canonical forms
	switch {
	case strings.Contains(titleLower, "claude"):
		return "claude"
	case strings.Contains(titleLower, "codex"):
		return "codex"
	case strings.Contains(titleLower, "gemini"):
		return "gemini"
	case strings.Contains(titleLower, "antigravity"):
		return "antigravity"
	case strings.Contains(titleLower, "cursor"):
		return "cursor"
	case strings.Contains(titleLower, "windsurf"):
		return "windsurf"
	case strings.Contains(titleLower, "aider"):
		return "aider"
	case strings.Contains(titleLower, "opencode"):
		return "oc"
	case strings.Contains(titleLower, "ollama"):
		return "ollama"
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
	case containsShortForm(titleLower, "agy"):
		return "antigravity"
	case containsShortForm(titleLower, "grok"):
		return "grok"
	case containsShortForm(titleLower, "ws"):
		return "windsurf"
	case containsShortForm(titleLower, "oc"):
		return "oc"
	case containsShortForm(titleLower, "omp"):
		return "omp"
	}

	return "unknown"
}

// DetectAgentType detects the agent type from a pane title.
// Returns one of: "claude", "codex", "gemini", "antigravity", "grok",
// "cursor", "windsurf", "aider", "oc" (opencode), "omp", "ollama", or "unknown".
func DetectAgentType(title string) string {
	return detectAgentType(title)
}

// containsShortForm checks if title contains the short form as a word boundary pattern
// It matches patterns like "__cc_" or "__cc__" to avoid false positives
func containsShortForm(title, short string) bool {
	// Check for "__<short>_" or "__<short>__"
	pattern1 := "__" + short + "_"
	pattern2 := "__" + short + "__"
	return strings.Contains(title, pattern1) || strings.Contains(title, pattern2)
}

// ResolveAgentType maps agent type aliases to canonical names.
// For example: "cc" -> "claude", "cod" -> "codex"
func ResolveAgentType(t string) string {
	trimmed := strings.TrimSpace(t)

	switch agent.AgentType(trimmed).Canonical() {
	case agent.AgentTypeClaudeCode:
		return "claude"
	case agent.AgentTypeCodex:
		return "codex"
	case agent.AgentTypeGemini:
		return "gemini"
	case agent.AgentTypeAntigravity:
		return "antigravity"
	case agent.AgentTypeGrok:
		return "grok"
	case agent.AgentTypeCursor:
		return "cursor"
	case agent.AgentTypeWindsurf:
		return "windsurf"
	case agent.AgentTypeAider:
		return "aider"
	case agent.AgentTypeOpencode:
		return "oc"
	case agent.AgentTypeOmp:
		return "omp"
	case agent.AgentTypeOllama:
		return "ollama"
	case agent.AgentTypeUser:
		return "user"
	case agent.AgentTypeUnknown:
		return "unknown"
	default:
		return strings.ToLower(trimmed)
	}
}

// paneLaunchModel returns the model ntm launched a pane with, read from the
// pane's recorded launch specification, or "" when the pane has no record or
// was launched without an explicit/configured model. It is authoritative
// where detectModel only guesses from title substrings: an `--oc=1:MODEL`
// pane's title matched none of detectModel's patterns, so --robot-context
// reported model "unknown" for a model the operator had named (GH #333).
var paneLaunchModel = func(paneID string) string {
	return paneLaunchModelContext(context.Background(), paneID)
}

func paneLaunchModelContext(ctx context.Context, paneID string) string {
	if paneID == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	spec, err := tmux.ReadPaneLaunchSpecContext(ctx, paneID)
	if err != nil || spec == nil {
		return ""
	}
	return strings.TrimSpace(spec.Model)
}

// contextPaneModel is the model --robot-context reports for a pane: the
// recorded launch model when there is one, else detectModel's title guess.
func contextPaneModel(pane tmux.Pane, agentType string) string {
	if model := paneLaunchModel(pane.ID); model != "" {
		return model
	}
	return detectModel(agentType, pane.Title)
}

// contextPaneLimit is the context window --robot-context budgets a pane
// against. A recorded launch model the model registry does not know (agy's
// pinned "Gemini 4 Argon (High)", an `opus[1m]` alias, an OpenCode
// provider/model) would otherwise fall to the registry's generic default
// rather than the family limit detectModel's guess resolves to: an agy pane
// dropped from gemini's 1M window to 128K and read as nearly eight times
// fuller than before the launch model was consulted.
func contextPaneLimit(model string, pane tmux.Pane, agentType string) int {
	if models.KnownModel(model) {
		return getContextLimit(model)
	}
	return getContextLimit(detectModel(agentType, pane.Title))
}

// detectModel attempts to detect the model from agent type and pane title.
func detectModel(agentType, title string) string {
	titleLower := strings.ToLower(title)
	// Check for specific model mentions in title
	switch {
	case strings.Contains(titleLower, "opus"):
		return "opus"
	case strings.Contains(titleLower, "sonnet"):
		return "sonnet"
	case strings.Contains(titleLower, "haiku"):
		return "haiku"
	case strings.Contains(titleLower, "gpt4") || strings.Contains(titleLower, "gpt-4"):
		return "gpt4"
	case strings.Contains(titleLower, "o1"):
		return "o1"
	case strings.Contains(titleLower, "o3"):
		return "o3"
	case strings.Contains(titleLower, "o4-mini"):
		return "o4-mini"
	case strings.Contains(titleLower, "grok-4-fast"):
		return "grok-4-fast"
	case strings.Contains(titleLower, "grok-4.6") || strings.Contains(titleLower, "grok-4-6"):
		return "grok-4.6"
	case strings.Contains(titleLower, "grok-4"):
		return "grok-4"
	case strings.Contains(titleLower, "grok-3"):
		return "grok-3"
	case strings.Contains(titleLower, "flash"):
		return "flash"
	case strings.Contains(titleLower, "pro"):
		return "pro"
	case strings.Contains(titleLower, "gemini"):
		return "gemini"
	}
	// Default models by agent type
	switch agentType {
	case "claude":
		return "sonnet" // Default Claude model
	case "codex":
		return "gpt4" // Default Codex model
	case "gemini":
		return "gemini" // Default Gemini-class model
	case "antigravity":
		return models.DefaultAntigravityModel
	case "grok":
		return "grok-4"
	default:
		return "unknown"
	}
}

// encodeJSON outputs the payload using the current OutputFormat.
// Despite the name (kept for backward compatibility), this now supports
// multiple formats: json, toon, or auto (default).
func encodeJSON(v interface{}) error {
	// Arrays-never-null contract: normalize nil slices to [] before encoding
	// so every terminal envelope honors the documented array contract
	// (bd-ws3-contract-breadth-psvyu.2). Normalize returns a fixed copy for
	// value payloads, which cannot be mutated in place.
	v = NormalizeArraysNeverNull(v)
	return Output(applyVerbosity(v, GetOutputVerbosity()), GetOutputFormat())
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
	case tmux.AgentAntigravity:
		return "antigravity"
	case tmux.AgentGrok:
		return "grok"
	case tmux.AgentCursor:
		return "cursor"
	case tmux.AgentWindsurf:
		return "windsurf"
	case tmux.AgentAider:
		return "aider"
	case tmux.AgentOpencode:
		return "oc"
	case tmux.AgentOmp:
		return "omp"
	case tmux.AgentOllama:
		return "ollama"
	case tmux.AgentUser:
		return "user"
	default:
		// A registered agent plugin is its own type (ntm#260); anything else
		// that tmux could not classify stays "unknown".
		if agent.IsPluginType(agent.AgentType(t)) {
			return strings.ToLower(strings.TrimSpace(string(t)))
		}
		return "unknown"
	}
}

func paneAgentType(pane tmux.Pane) string {
	if resolved := agentTypeString(pane.Type); resolved != "unknown" {
		return resolved
	}
	return detectAgentType(pane.Title)
}

// sendInjectionFormat picks the CASS block format for a send. One prompt goes
// to every target, so a mixed --cc/--cod send uses the neutral markdown
// format instead of formatting for whichever pane happens to be first. No
// targets keeps the configured format.
func sendInjectionFormat(targetPanes []tmux.Pane) InjectionFormat {
	if len(targetPanes) == 0 {
		return ""
	}
	agentType := paneAgentType(targetPanes[0])
	for _, pane := range targetPanes[1:] {
		if paneAgentType(pane) != agentType {
			return FormatMarkdown
		}
	}
	return FormatForAgent(agentType)
}

func stateAgentTypeForPane(pane tmux.Pane, detectedType string) string {
	if normalized := normalizeAgentType(detectedType); normalized != "unknown" {
		return normalized
	}
	return paneAgentType(pane)
}

func determinePaneState(pane tmux.Pane, output, detectedType string) string {
	return determineState(output, stateAgentTypeForPane(pane, detectedType))
}

func modelNameForPane(pane tmux.Pane, cfg *config.Config) string {
	if pane.Variant != "" {
		return pane.Variant
	}
	agentType := tmux.AgentType(pane.Type).Canonical()
	if cfg != nil {
		switch agentType {
		case tmux.AgentClaude:
			if cfg.Models.DefaultClaude != "" {
				return cfg.Models.DefaultClaude
			}
		case tmux.AgentCodex:
			if cfg.Models.DefaultCodex != "" {
				return cfg.Models.DefaultCodex
			}
		case tmux.AgentGemini:
			if cfg.Models.DefaultGemini != "" {
				return cfg.Models.DefaultGemini
			}
		case tmux.AgentAntigravity:
			if cfg.Models.DefaultAntigravity != "" {
				return cfg.Models.DefaultAntigravity
			}
		case tmux.AgentGrok:
			if cfg.Models.DefaultGrok != "" {
				return cfg.Models.DefaultGrok
			}
		case tmux.AgentOllama:
			if cfg.Models.DefaultOllama != "" {
				return cfg.Models.DefaultOllama
			}
		case tmux.AgentOpencode:
			if cfg.Models.DefaultOpencode != "" {
				return cfg.Models.DefaultOpencode
			}
		case tmux.AgentOmp:
			if cfg.Models.DefaultOmp != "" {
				return cfg.Models.DefaultOmp
			}
		}
	}
	// Fall back to compiled-in defaults from config.DefaultModels() so the
	// values stay in sync with the canonical defaults. (ntm#105)
	defaults := config.DefaultModels()
	switch agentType {
	case tmux.AgentClaude:
		if defaults.DefaultClaude != "" {
			return defaults.DefaultClaude
		}
		// Launch model delegated to Claude Code (ntm#334): estimate context
		// and cost against the Claude family rather than dropping them.
		return config.ClaudeEstimationModel
	case tmux.AgentCodex:
		return defaults.DefaultCodex
	case tmux.AgentGemini:
		return defaults.DefaultGemini
	case tmux.AgentAntigravity:
		return defaults.DefaultAntigravity
	case tmux.AgentGrok:
		return defaults.DefaultGrok
	case tmux.AgentCursor:
		return "cursor"
	case tmux.AgentWindsurf:
		return "windsurf"
	case tmux.AgentAider:
		return "aider"
	case tmux.AgentOpencode:
		return "opencode"
	case tmux.AgentOmp:
		// Empty by default: omp's own config (modelRoles.default) chooses.
		return defaults.DefaultOmp
	case tmux.AgentOllama:
		return defaults.DefaultOllama
	default:
		return ""
	}
}

func encodeTerminalRobotOutput(output any, response RobotResponse, fallback string) error {
	if response.Success {
		return encodeJSON(output)
	}
	response.OutputFormat = string(FormatJSON)
	if err := encodeRobotFailureJSON(output); err != nil {
		return ExitResultForCode(1, fmt.Errorf("encode robot failure: %w", err), false)
	}
	cause := errors.New(response.Error)
	if response.Error == "" {
		cause = errors.New(fallback)
	}
	return ExitResultForResponse(response, cause, true)
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
	RobotResponse
	GeneratedAt time.Time    `json:"generated_at"`
	Count       int          `json:"count"`
	Recipes     []RecipeInfo `json:"recipes"`
}

// GetRecipes returns available recipes for AI orchestrators.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetRecipes() (*RecipesOutput, error) {
	loader := recipe.NewLoader()
	recipes, err := loader.LoadAll()
	if err != nil {
		// Return empty list on error
		return &RecipesOutput{
			RobotResponse: NewErrorResponse(
				err,
				ErrCodeInternalError,
				"Check recipe configuration and file paths",
			),
			GeneratedAt: time.Now().UTC(),
			Count:       0,
			Recipes:     []RecipeInfo{},
		}, nil
	}

	output := &RecipesOutput{
		RobotResponse: NewRobotResponse(true),
		GeneratedAt:   time.Now().UTC(),
		Count:         len(recipes),
		Recipes:       make([]RecipeInfo, len(recipes)),
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

	return output, nil
}

// PrintRecipes outputs available recipes as JSON for AI orchestrators.
// This is a thin wrapper around GetRecipes() for CLI output.
func PrintRecipes() error {
	output, err := GetRecipes()
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot recipes failed")
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

// GetDiff returns agent activity comparison and file change analysis.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetDiff(opts DiffOptions) (*DiffOutput, error) {
	// Default to 15 minutes if not specified
	if opts.Since == 0 {
		opts.Since = 15 * time.Minute
	}

	now := time.Now().UTC()
	sinceTime := now.Add(-opts.Since)

	output := &DiffOutput{
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

	// Validate session exists
	if !tmux.SessionExists(opts.Session) {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session '%s' not found", opts.Session),
			ErrCodeSessionNotFound,
			"Use 'ntm list' to see available sessions",
		)
		return output, nil
	}

	// Get panes for agent activity
	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to get panes: %w", err),
			ErrCodeInternalError,
			"Check tmux is running and session is accessible",
		)
		return output, nil
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
		// Tagged service panes are not agents (ntm#305).
		if pane.IsServicePane() {
			continue
		}
		// Capture pane output for state detection
		captured, _ := tmux.CapturePaneOutput(pane.ID, 100)
		detection := DetectAgentTypeEnhanced(pane, captured)
		agentType := stateAgentTypeForPane(pane, detection.Type)
		lines := splitLines(captured)

		// Use authoritative pane metadata and enhanced detection instead of titles.
		state := determinePaneState(pane, captured, detection.Type)
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

	return output, nil
}

// PrintDiff handles the --robot-diff command.
// This is a thin wrapper around GetDiff() for CLI output.
func PrintDiff(opts DiffOptions) error {
	output, err := GetDiff(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot diff failed")
}
