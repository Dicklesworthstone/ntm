package robot

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	ntmctx "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/models"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// TailOutput is the structured output for --robot-tail
type TailOutput struct {
	RobotResponse                       // Embed standard response fields (success, timestamp, error)
	Session       string                `json:"session"`
	CapturedAt    time.Time             `json:"captured_at"`
	Panes         map[string]PaneOutput `json:"panes"`
	AgentHints    *TailAgentHints       `json:"_agent_hints,omitempty"`

	// SourceHealth — same provenance contract as ActivityOutput. See ntm#117
	// and docs/freshness-degraded-state-contract.md.
	SourceHealth map[string]SourceHealthEntry `json:"source_health,omitempty"`
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

	ObservationState      string  `json:"observation_state"`
	ObservationFreshness  string  `json:"observation_freshness"`
	ObservationConfidence float64 `json:"observation_confidence"`
	SafeToDispatch        bool    `json:"safe_to_dispatch"`

	// PanePID is the tmux shell PID of this pane (`#{pane_pid}`). Populated
	// additively so downstream watchdogs can detect respawns by comparing
	// against the PID they last observed. See ntm#117.
	PanePID int `json:"pane_pid,omitempty"`

	// Per-pane capture provenance (ntm#117 follow-up). Output-level
	// `source_health.tmux` answers "is anyone reachable"; these fields
	// answer "which specific pane went dark". Populated for every pane,
	// fresh or failed, so a watchdog polling --robot-tail across many
	// sessions can pinpoint which panes are missing without reverse-
	// engineering empty-Lines.
	//
	//   capture_collected_at  RFC 3339 timestamp the pane capture started.
	//   capture_provenance    "live" when the capture succeeded;
	//                         "unavailable" when tmux returned an error
	//                         for this specific pane.
	//   capture_error         the underlying tmux error string when
	//                         capture_provenance == "unavailable". Omitted
	//                         on the happy path.
	CaptureCollectedAt string `json:"capture_collected_at,omitempty"`
	CaptureProvenance  string `json:"capture_provenance,omitempty"`
	CaptureError       string `json:"capture_error,omitempty"`
}

// TailOptions configures the GetTail operation.
type TailOptions struct {
	Session    string
	Lines      int
	PaneFilter []string
	// Fresh forces a direct live capture for every selected pane instead of
	// the shared observer's buffered content. Post-action verification needs
	// ground truth immediately after a keypress; buffered observations can
	// lag transient states for several ticks (ntm-mxcp / AP-41).
	Fresh bool
}

// GetTail returns recent pane output for AI consumption.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetTail(opts TailOptions) (*TailOutput, error) {
	// Resolve labeled session names (e.g. "myproject" -> "myproject--frontend")
	// so callers don't need to know the exact tmux session name. (ntm#104)
	opts.Session = resolveSessionName(opts.Session)

	if !tmux.SessionExists(opts.Session) {
		return &TailOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("session '%s' not found", opts.Session),
				ErrCodeSessionNotFound,
				"Use 'ntm list' to see available sessions",
			),
			Session:    opts.Session,
			CapturedAt: time.Now().UTC(),
			Panes:      make(map[string]PaneOutput),
		}, nil
	}
	if opts.Lines <= 0 {
		opts.Lines = 20
	}

	observer := newRobotSessionObserver(opts.Lines)
	observation, err := observer.Observe(context.Background(), opts.Session)
	if err != nil {
		errorCode := ErrCodeInternalError
		hint := "Check tmux is running and session is accessible"
		if tmux.ClassifyCommandError(err).Kind == tmux.CommandErrorSessionNotFound {
			errorCode = ErrCodeSessionNotFound
			hint = "Use 'ntm list' to see available sessions"
		}
		return &TailOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("failed to observe panes: %w", err),
				errorCode,
				hint,
			),
			Session:    opts.Session,
			CapturedAt: observation.ObservedAt,
			Panes:      make(map[string]PaneOutput),
			SourceHealth: map[string]SourceHealthEntry{
				"tmux": {
					Source:           "tmux",
					Status:           "unavailable",
					CollectedAt:      FormatTimestamp(observation.ObservedAt),
					FreshnessSec:     0,
					StaleAfterSec:    5,
					Provenance:       "live",
					DegradedFeatures: []string{"pane_output", "pane_pid", "agent_state"},
					LastError:        err.Error(),
					LastErrorAt:      time.Now().UTC().Format(time.RFC3339),
				},
			},
		}, nil
	}

	panes := observationPanes(observation)
	selectedIDs := make(map[string]struct{}, len(panes))
	if len(opts.PaneFilter) > 0 {
		selected, resolveErr := tmux.ResolvePaneSelectors(panes, opts.PaneFilter, false)
		if resolveErr != nil {
			return &TailOutput{
				RobotResponse: NewErrorResponse(
					resolveErr,
					paneSelectorRobotErrorCode(resolveErr),
					"Use --robot-status or --robot-tail without --panes to inspect canonical pane addresses",
				),
				Session:    opts.Session,
				CapturedAt: observation.ObservedAt,
				Panes:      make(map[string]PaneOutput),
			}, nil
		}
		for _, pane := range selected {
			selectedIDs[pane.Ref().StableKey()] = struct{}{}
		}
	}

	collectedAt := FormatTimestamp(observation.ObservedAt)
	output := &TailOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		CapturedAt:    observation.ObservedAt,
		Panes:         make(map[string]PaneOutput),
		SourceHealth: map[string]SourceHealthEntry{
			"tmux": {
				Source:        "tmux",
				Status:        "fresh",
				CollectedAt:   collectedAt,
				FreshnessSec:  0,
				StaleAfterSec: 5,
				Provenance:    "live",
			},
		},
	}
	if !observation.Complete {
		source := output.SourceHealth["tmux"]
		source.Status = "stale"
		source.DegradedFeatures = []string{"pane_output", "agent_state"}
		if len(observation.Failures) > 0 {
			source.LastError = observation.Failures[0].Error
			source.LastErrorAt = collectedAt
		}
		output.SourceHealth["tmux"] = source
	}

	multiWindow := paneSessionIsMultiWindow(panes)
	for _, paneObservation := range observation.Panes {
		pane := paneObservation.Metadata
		// Use the topology-aware key so window-per-agent layouts don't collapse
		// every window's pane onto a single output-map entry (#172).
		paneKey := paneTargetKey(pane, multiWindow)

		if len(opts.PaneFilter) > 0 {
			if _, selected := selectedIDs[pane.Ref().StableKey()]; !selected {
				continue
			}
		}

		paneCapturedAt := FormatTimestamp(paneObservation.Current.ObservedAt)
		if paneObservation.Current.Freshness != status.FreshnessFresh || paneObservation.Current.Error != "" {
			captureErr := paneObservation.Current.Error
			if captureErr == "" {
				captureErr = fmt.Sprintf("current pane observation is %s", paneObservation.Current.Freshness)
			}
			// Tail's contract is content-first: a stale or failed observation
			// must not silently displace the requested lines with advisory
			// metadata (ntm-ajeb). Fall back to a direct live capture; only
			// when that also fails does the pane report empty lines with an
			// explicit capture error. An empty pane ID must never reach
			// capture-pane: `-t ""` silently targets the ACTIVE pane and
			// would misattribute another pane's content.
			if fallbackRaw, fallbackErr := capturePaneFallback(pane, opts.Lines); fallbackErr == nil {
				fallbackLines := splitLines(status.StripANSI(fallbackRaw))
				output.Panes[paneKey] = PaneOutput{
					Type:                  paneAgentType(pane),
					State:                 "unknown",
					Lines:                 fallbackLines,
					Truncated:             len(fallbackLines) >= opts.Lines,
					PanePID:               pane.PID,
					CaptureCollectedAt:    FormatTimestamp(time.Now().UTC()),
					CaptureProvenance:     "live-fallback",
					CaptureError:          captureErr,
					ObservationState:      string(paneObservation.Current.Status.State),
					ObservationFreshness:  string(paneObservation.Current.Freshness),
					ObservationConfidence: paneObservation.Current.Confidence,
					SafeToDispatch:        false,
				}
				continue
			}
			output.Panes[paneKey] = PaneOutput{
				Type:                  paneAgentType(pane),
				State:                 "unknown",
				Lines:                 []string{},
				PanePID:               pane.PID,
				CaptureCollectedAt:    paneCapturedAt,
				CaptureProvenance:     "unavailable",
				CaptureError:          captureErr,
				ObservationState:      string(paneObservation.Current.Status.State),
				ObservationFreshness:  string(paneObservation.Current.Freshness),
				ObservationConfidence: paneObservation.Current.Confidence,
				SafeToDispatch:        false,
			}
			continue
		}

		rawOutput := paneObservation.RawOutput
		captureProvenance := "live"
		if opts.Fresh {
			// --fresh: bypass the observer's buffered content entirely and
			// capture the pane directly right now. Buffered observations can
			// lag transient states for several ticks, which is exactly when
			// post-action verification consults tail (ntm-mxcp / AP-41). A
			// failed direct capture keeps the buffered content rather than
			// degrading a working surface.
			if freshRaw, freshErr := capturePaneFallback(pane, opts.Lines); freshErr == nil {
				rawOutput = freshRaw
				captureProvenance = "fresh"
				paneCapturedAt = FormatTimestamp(time.Now().UTC())
			}
		}
		cleanOutput := status.StripANSI(rawOutput)
		outputLines := splitLines(cleanOutput)
		truncated := len(outputLines) >= opts.Lines

		output.Panes[paneKey] = PaneOutput{
			Type:                  paneAgentType(pane),
			State:                 tailObservationState(paneObservation.Current.Status.State),
			Lines:                 outputLines,
			Truncated:             truncated,
			PanePID:               pane.PID,
			CaptureCollectedAt:    paneCapturedAt,
			CaptureProvenance:     captureProvenance,
			ObservationState:      string(paneObservation.Current.Status.State),
			ObservationFreshness:  string(paneObservation.Current.Freshness),
			ObservationConfidence: paneObservation.Current.Confidence,
			SafeToDispatch:        paneObservation.SafeToDispatch(),
		}
	}

	// Generate agent hints based on pane states
	output.AgentHints = generateTailHints(output.Panes)

	return output, nil
}

// capturePaneFallback performs the direct live capture behind GetTail's
// stale-observation fallback, refusing empty pane identities so the capture
// can never silently retarget the active pane.
func capturePaneFallback(pane tmux.Pane, lines int) (string, error) {
	if strings.TrimSpace(pane.ID) == "" {
		return "", errors.New("pane has no tmux ID; direct capture would target the active pane")
	}
	return tmux.CapturePaneOutput(pane.ID, lines)
}

// PrintTail outputs recent pane output for AI consumption.
// This is a thin wrapper around GetTail() for CLI output.
// PaneAddressEntry is one pane's complete addressing card (ntm-cac6): the
// canonical selector send/tail accept, the stable tmux %pane_id (immune to
// base-index settings and renumbering), the physical topology address, and a
// ready-to-paste tmux target for the rare raw-tmux fallback — replacing the
// documented `WIN=$(tmux list-windows ...)` discovery folklore.
type PaneAddressEntry struct {
	Selector    string `json:"selector"`
	PaneID      string `json:"pane_id"`
	WindowIndex int    `json:"window_index"`
	PaneIndex   int    `json:"pane_index"`
	NTMIndex    int    `json:"ntm_index,omitempty"`
	Title       string `json:"title"`
	Type        string `json:"type"`
	TmuxTarget  string `json:"tmux_target"`
}

// PaneAddressOutput is the --robot-pane-address response.
type PaneAddressOutput struct {
	RobotResponse
	Session     string             `json:"session"`
	MultiWindow bool               `json:"multi_window"`
	Panes       []PaneAddressEntry `json:"panes"`
}

// GetPaneAddresses returns every pane's addressing card for a session.
func GetPaneAddresses(session string) (*PaneAddressOutput, error) {
	session = resolveSessionName(session)
	output := &PaneAddressOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       session,
		Panes:         []PaneAddressEntry{},
	}
	if !tmux.SessionExists(session) {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session '%s' not found", session),
			ErrCodeSessionNotFound,
			"Use 'ntm list' to see available sessions",
		)
		return output, nil
	}
	panes, err := tmux.GetPanes(session)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to get panes: %w", err),
			ErrCodeInternalError,
			"Check tmux session state",
		)
		return output, nil
	}
	ordered := tmux.SortPanesByTopology(panes)
	multiWindow := tmux.PanesSpanMultipleWindows(ordered)
	output.MultiWindow = multiWindow
	for _, pane := range ordered {
		ref := pane.Ref()
		target := ref.ID
		if target == "" {
			target = fmt.Sprintf("%s:%s", session, ref.Physical())
		}
		output.Panes = append(output.Panes, PaneAddressEntry{
			Selector:    ref.Canonical(multiWindow),
			PaneID:      ref.ID,
			WindowIndex: ref.WindowIndex,
			PaneIndex:   ref.PaneIndex,
			NTMIndex:    ref.NTMIndex,
			Title:       pane.Title,
			Type:        paneAgentType(pane),
			TmuxTarget:  target,
		})
	}
	return output, nil
}

// PrintPaneAddresses outputs the pane addressing cards for CLI consumption.
func PrintPaneAddresses(session string) error {
	output, err := GetPaneAddresses(session)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot pane-address failed")
}

func PrintTail(opts TailOptions) error {
	output, err := GetTail(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot tail failed")
}

func tailObservationState(state status.AgentState) string {
	if state == status.StateWorking {
		return "active"
	}
	return string(state)
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
	normalizedType := normalizeAgentType(agentType)
	// Normalize agent type for status package (expects "cc", "cod", etc.)
	shortType := translateAgentTypeForStatus(normalizedType)
	lastLine := status.GetLastNonEmptyLine(output)

	detectedError := status.DetectErrorInOutput(output)
	if detectedError == status.ErrorRateLimit {
		return "error"
	}
	// Oh My Pi's composer-anchored chrome is authoritative in both directions:
	// in-flight signals mean active whatever the transcript says, and a
	// dismissable provider-error block framed above the quiet composer is the
	// current (retryable) error, never idle-after-completion.
	if normalizedType == "omp" {
		if agent.OmpActivelyWorking(output, 0) {
			return "active"
		}
		if _, failed := agent.OmpProviderError(output); failed {
			return "error"
		}
	}
	// A shell prompt in a Claude pane means the agent process exited. Preserve
	// that guard before considering an older completion marker as idle TUI state.
	if normalizedType == "claude" &&
		lastLine != "" &&
		status.IsPromptLine(lastLine, "user") &&
		!status.IsPromptLine(lastLine, shortType) &&
		!HasIdlePattern(lastLine, normalizedType) {
		return "active"
	}
	if normalizedType == "claude" {
		// Claude's newest dynamic marker orders live spinners, completion, and
		// glyph-led terminal errors. A completed turn may supersede an older
		// generic tool failure, while rate limits remain sticky above.
		// determineState receives only captured text; no pane metadata (and
		// so no width) is in scope here, so keep the calibrated 0 budget.
		switch agent.DetectClaudeTurnState(output, 0) {
		case agent.ClaudeTurnWorking:
			return "active"
		case agent.ClaudeTurnError:
			return "error"
		case agent.ClaudeTurnEnded:
			return "idle"
		}
	}
	if detectedError != status.ErrorNone {
		return "error"
	}
	// Other agents use position-blind live patterns. They may supersede idle
	// chrome, but never a current error.
	if normalizedType != "claude" && isAIAgentLiveBusy(output, normalizedType, 0) {
		return "active"
	}
	// A bare shell prompt in a known agent pane usually means the agent exited
	// and dropped back to the shell, not that the agent is idle at its own prompt.
	// Guard this explicitly so generic shell patterns cannot override pane identity.
	if isKnownAgentPatternType(normalizedType) &&
		lastLine != "" &&
		status.IsPromptLine(lastLine, "user") &&
		!status.IsPromptLine(lastLine, shortType) &&
		!HasIdlePattern(lastLine, normalizedType) {
		return "active"
	}
	if status.DetectIdleFromOutput(output, shortType) {
		return "idle"
	}
	// Also check the robot pattern library which has richer agent-specific
	// idle patterns (Claude Code version banner, bypass status, welcome
	// message, arrow prompt) that the status package doesn't cover.
	if HasIdlePattern(output, normalizedType) {
		return "idle"
	}
	if isPythonPrompt(lastLine) {
		return "idle"
	}
	// If output is empty and it's a user pane, treat as idle (prompt)
	if strings.TrimSpace(output) == "" && (normalizedType == "" || normalizedType == "user") {
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

// translateAgentTypeForStatus converts long agent type names to short forms
// expected by the status package patterns.
func translateAgentTypeForStatus(agentType string) string {
	switch canonical := agent.AgentType(agentType).Canonical(); canonical {
	case "", agent.AgentTypeUnknown:
		return ""
	default:
		return string(canonical)
	}
}

func isPythonPrompt(line string) bool {
	return strings.TrimSpace(stripANSI(line)) == ">>>"
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

// ContextOutput is the structured output for --robot-context
type ContextOutput struct {
	RobotResponse
	Session          string                       `json:"session"`
	CapturedAt       time.Time                    `json:"captured_at"`
	Agents           []AgentContextInfo           `json:"agents"`
	Summary          ContextSummary               `json:"summary"`
	PendingRotations []ContextPendingRotationInfo `json:"pending_rotations,omitempty"`
	AgentHints       *ContextAgentHints           `json:"_agent_hints,omitempty"`
}

// ContextPendingRotationInfo contains information about a pending rotation confirmation
type ContextPendingRotationInfo struct {
	AgentID        string  `json:"agent_id"`
	SessionName    string  `json:"session_name"`
	PaneID         string  `json:"pane_id"`
	ContextPercent float64 `json:"context_percent"`
	CreatedAt      string  `json:"created_at"`
	TimeoutAt      string  `json:"timeout_at"` // the request lapses unconfirmed; nothing runs on its own
	WorkDir        string  `json:"work_dir,omitempty"`
}

// AgentContextInfo contains context window information for a single agent pane
type AgentContextInfo struct {
	Pane            string  `json:"pane"`
	PaneID          string  `json:"pane_id"`
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
	// Source discriminates where the primary numbers came from:
	// "status_bar" (the agent's own live context gauge, parsed from the
	// pane; Oh My Pi), "transcript" (ground truth read from the agent CLI's
	// own session transcript) or "scrollback_estimate" (legacy char-count
	// heuristic).
	Source string `json:"source"`
	// Transcript* fields are populated only when Source == "transcript".
	TranscriptTokens    int    `json:"transcript_tokens,omitempty"`
	TranscriptModel     string `json:"transcript_model,omitempty"`
	TranscriptPath      string `json:"transcript_path,omitempty"`
	TranscriptUpdatedAt string `json:"transcript_updated_at,omitempty"`
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

// getContextLimit returns the context window limit for a model.
// Delegates to the canonical registry in internal/models.
func getContextLimit(model string) int {
	return models.GetContextLimit(model)
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

	switch highCount {
	case 0:
		// No high usage agents
		if len(lowUsage) == total {
			hints.Suggestions = append(hints.Suggestions, "All agents healthy - context usage is low across the board")
		} else if len(lowUsage) > 0 {
			hints.Suggestions = append(hints.Suggestions, fmt.Sprintf("%d agent(s) have low usage, others are moderate", len(lowUsage)))
		} else {
			hints.Suggestions = append(hints.Suggestions, "All agents at moderate context usage - no immediate concerns")
		}
	case total:
		hints.Suggestions = append(hints.Suggestions, "All agents have high context usage - consider spawning new sessions")
	default:
		hints.Suggestions = append(hints.Suggestions, fmt.Sprintf("%d agent(s) have high context usage", highCount))
		if len(lowUsage) > 0 {
			hints.Suggestions = append(hints.Suggestions, fmt.Sprintf("%d agent(s) have room for additional work", len(lowUsage)))
		}
	}

	return hints
}

// paneCurrentPath returns a pane's current working directory. Overridable
// for tests.
var paneCurrentPath = func(paneID string) (string, bool) {
	cwd, err := tmux.DefaultClient.Run("display-message", "-p", "-t", tmux.ExactTarget(paneID), "#{pane_current_path}")
	if err != nil {
		return "", false
	}
	cwd = strings.TrimSpace(cwd)
	return cwd, cwd != ""
}

// transcriptUsageForCwd finds the newest transcript usage for an agent type
// working in cwd. Overridable for tests.
var transcriptUsageForCwd = func(agentType, cwd string) (*ntmctx.TranscriptUsage, bool) {
	return ntmctx.LatestAgentTranscriptUsage(agentType, cwd, time.Time{})
}

// transcriptUsageForProcess reads the transcript of the session the agent
// process under a pane is writing. Overridable for tests.
var transcriptUsageForProcess = func(agentType string, panePID int) (*ntmctx.TranscriptUsage, bool) {
	return ntmctx.ProcessTranscriptUsage(context.Background(), agentType, panePID)
}

// ResolvePaneTranscripts correlates panes with their agent CLI's own session
// transcripts, returning usage keyed by pane ID.
//
// A pane whose agent process names its session (agentsession process-tree
// binding: Claude Code's per-process session record, a resumed session id)
// reads that transcript: exact per pane even where several panes of one agent
// type share a directory, the normal NTM swarm layout. Otherwise correlation
// falls back to (agent type, cwd) under ntmctx.AttributePaneTranscripts'
// sole-pane rule, so a pane never shows another pane's numbers (scrollback
// estimation stands instead).
func ResolvePaneTranscripts(panes []tmux.Pane) map[string]*ntmctx.TranscriptUsage {
	return resolvePaneTranscripts(context.Background(), panes, paneCurrentPath, transcriptUsageForProcess, transcriptUsageForCwd)
}

// resolvePaneTranscriptsContext keeps the projection refresh's deadline on
// tmux and process discovery, instead of starting independent background work.
func resolvePaneTranscriptsContext(ctx context.Context, panes []tmux.Pane) (map[string]*ntmctx.TranscriptUsage, error) {
	currentPath := func(paneID string) (string, bool) {
		cwd, err := tmux.DefaultClient.RunContext(ctx, "display-message", "-p", "-t", tmux.ExactTarget(paneID), "#{pane_current_path}")
		cwd = strings.TrimSpace(cwd)
		return cwd, err == nil && cwd != ""
	}
	byProcess := func(agentType string, pid int) (*ntmctx.TranscriptUsage, bool) {
		return ntmctx.ProcessTranscriptUsage(ctx, agentType, pid)
	}
	result := resolvePaneTranscripts(ctx, panes, currentPath, byProcess, transcriptUsageForCwd)
	return result, ctx.Err()
}

func resolvePaneTranscripts(ctx context.Context, panes []tmux.Pane,
	currentPath func(string) (string, bool),
	byProcess func(string, int) (*ntmctx.TranscriptUsage, bool),
	byCwd func(string, string) (*ntmctx.TranscriptUsage, bool),
) map[string]*ntmctx.TranscriptUsage {
	// Remote tmux PIDs and paths belong to another host. They cannot identify
	// a local process or transcript; pane-rendered gauges remain usable.
	if ctx.Err() != nil || tmux.DefaultClient.Remote != "" {
		return map[string]*ntmctx.TranscriptUsage{}
	}
	refs := make([]ntmctx.PaneTranscriptRef, 0, len(panes))
	for _, pane := range panes {
		if ctx.Err() != nil {
			return map[string]*ntmctx.TranscriptUsage{}
		}
		if pane.IsServicePane() {
			continue
		}
		agentType := paneAgentType(pane)
		if agentType == "unknown" || agentType == "user" {
			continue
		}
		cwd, _ := currentPath(pane.ID)
		refs = append(refs, ntmctx.PaneTranscriptRef{PaneID: pane.ID, AgentType: agentType, PID: pane.PID, Cwd: cwd})
	}
	return ntmctx.AttributePaneTranscripts(refs, nil,
		func(agentType string, pid int) (*ntmctx.TranscriptUsage, bool) {
			if ctx.Err() != nil {
				return nil, false
			}
			return byProcess(agentType, pid)
		},
		func(agentType, cwd string) (*ntmctx.TranscriptUsage, bool) {
			if ctx.Err() != nil {
				return nil, false
			}
			return byCwd(agentType, cwd)
		},
	)
}

// transcriptUsagePercent converts a transcript usage reading into a percent
// of the context window: the window the transcript reports (Codex), else the
// model registry's window for the transcript's model, or fallbackModel when
// the transcript names none. limit <= 0 means no window is known and pct is
// meaningless. Real usage cannot exceed the real window, so an over-100%
// reading means the registry's window for this (possibly unrecognized) model
// is wrong: it is capped at 100, the safe direction for rotation, and
// reported through capped.
func transcriptUsagePercent(tu *ntmctx.TranscriptUsage, fallbackModel string) (pct float64, limit int, capped bool) {
	limit = tu.ContextWindow
	if limit <= 0 {
		model := tu.Model
		if model == "" {
			model = fallbackModel
		}
		limit = getContextLimit(model)
	}
	if limit <= 0 {
		return 0, limit, false
	}
	pct = float64(tu.Tokens) / float64(limit) * 100
	if pct > 100 {
		return 100, limit, true
	}
	return pct, limit, false
}

// transcriptContextReading measures a pane's transcript reading against its
// context window: the transcript's own model when it names one, else
// paneModel (the pane's launch or title model, contextPaneModel).
// --robot-context and --robot-metrics both read a pane through it, so the two
// never disagree about the same pane.
func transcriptContextReading(paneModel string, usage *ntmctx.TranscriptUsage) (model string, pct float64, limit int, capped bool) {
	model = paneModel
	if usage.Model != "" {
		model = usage.Model
	}
	pct, limit, capped = transcriptUsagePercent(usage, model)
	return model, pct, limit, capped
}

// paneContextUsagePercents is the alert generator's context-usage source
// (alerts.Config.PaneContextUsage): each agent pane's transcript-attributed
// usage percent, keyed by pane ID. Panes with no attributable transcript or
// no known window are absent, so they never raise a context warning.
func paneContextUsagePercents(_ string, panes []tmux.Pane) map[string]float64 {
	transcripts := ResolvePaneTranscripts(panes)
	usage := make(map[string]float64, len(transcripts))
	for _, pane := range panes {
		tu, ok := transcripts[pane.ID]
		if !ok {
			continue
		}
		if pct, limit, _ := transcriptUsagePercent(tu, detectModel(paneAgentType(pane), pane.Title)); limit > 0 {
			usage[pane.ID] = pct
		}
	}
	return usage
}

// ompStatusBarUsage reads an omp pane's live context gauge (see
// ntmctx.OmpStatusBarUsage); every other agent type reports false. The
// reading always carries a positive ContextWindow.
func ompStatusBarUsage(agentType, cleanCapture string, capturedAt time.Time) (*ntmctx.TranscriptUsage, bool) {
	if agent.AgentType(agentType).Canonical() != agent.AgentTypeOmp {
		return nil, false
	}
	return ntmctx.OmpStatusBarUsage(cleanCapture, capturedAt)
}

func opencodeStatusBarUsage(agentType, cleanCapture string, fallbackLimit int, capturedAt time.Time) (*ntmctx.TranscriptUsage, bool) {
	if agent.AgentType(agentType).Canonical() != agent.AgentTypeOpencode {
		return nil, false
	}
	return ntmctx.OpencodeStatusBarUsage(cleanCapture, fallbackLimit, capturedAt)
}

// GetContext retrieves context window usage information for all agents in a session.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetContext(session string, lines int) (*ContextOutput, error) {
	if !tmux.SessionExists(session) {
		return &ContextOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("session '%s' not found", session),
				ErrCodeSessionNotFound,
				"Use 'ntm list' to see available sessions",
			),
			Session:    session,
			CapturedAt: time.Now().UTC(),
		}, nil
	}

	panes, err := tmux.GetPanes(session)
	if err != nil {
		return &ContextOutput{
			RobotResponse: NewErrorResponse(err, ErrCodeInternalError, "Failed to get panes"),
			Session:       session,
			CapturedAt:    time.Now().UTC(),
		}, nil
	}

	output := &ContextOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       session,
		CapturedAt:    time.Now().UTC(),
		Agents:        make([]AgentContextInfo, 0, len(panes)),
	}

	var lowUsage, highUsage []string
	var totalUsage float64

	// Topology-aware key (#172): emit a round-trippable "window.pane" address on
	// multi-window sessions instead of a window-blind bare index.
	multiWindow := paneSessionIsMultiWindow(panes)

	// Ground-truth transcripts, resolved once for the whole pane set
	// (memoized per agent-type+cwd group; ambiguous groups get none).
	paneTranscripts := ResolvePaneTranscripts(panes)

	for _, pane := range panes {
		// Tagged service panes are not agents and hold no agent context
		// (ntm#305).
		if pane.IsServicePane() {
			continue
		}
		agentType := paneAgentType(pane)
		if agentType == "unknown" || agentType == "user" {
			continue // Skip non-agent panes
		}

		model := contextPaneModel(pane, agentType)

		scrollback, _ := tmux.CapturePaneOutput(pane.ID, lines)
		cleanText := stripANSI(scrollback)
		state := determineState(cleanText, agentType)
		if agentType == "grok" && paneLaunchModel(pane.ID) == "" {
			if statusModel := agent.GrokStatusLineModel(cleanText); statusModel != "" {
				model = statusModel
			}
		}

		charCount := len(cleanText)
		// Rough token estimate: ~4 chars per token
		estTokens := charCount / 4
		// Add overhead for system prompts and other context (2.5x multiplier)
		withOverhead := int(float64(estTokens) * 2.5)
		contextLimit := contextPaneLimit(model, pane, agentType)
		// A non-positive limit would yield NaN or +Inf, which json.Encode rejects
		// outright — the whole response would be replaced by an empty stdout and a
		// nonzero exit. Treat an unusable limit as fully consumed instead, which
		// is the safe direction for routing and rotation decisions.
		usagePct := 100.0
		if contextLimit > 0 {
			usagePct = float64(withOverhead) / float64(contextLimit) * 100
		}

		paneKey := paneTargetKey(pane, multiWindow)

		agentInfo := AgentContextInfo{
			Pane:            paneKey,
			PaneID:          pane.ID,
			PaneIdx:         pane.Index,
			AgentType:       agentType,
			Model:           model,
			EstimatedTokens: estTokens,
			WithOverhead:    withOverhead,
			ContextLimit:    contextLimit,
			UsagePercent:    usagePct,
			Confidence:      "low", // Scrollback-based estimation is low confidence
			State:           state,
			Source:          "scrollback_estimate",
		}

		// Ground truth beats scrollback heuristics. omp renders its own
		// context gauge in the composer border: that reading belongs to this
		// pane, so it wins even where several omp panes share a directory.
		// Otherwise read the agent CLI's own session transcript (Claude Code
		// / Codex / omp usage records) when one is unambiguously correlated
		// to this pane via its working directory.
		if gauge, ok := ompStatusBarUsage(agentType, cleanText, output.CapturedAt); ok {
			agentInfo.Source = "status_bar"
			agentInfo.EstimatedTokens = gauge.Tokens
			agentInfo.WithOverhead = gauge.Tokens
			agentInfo.ContextLimit = gauge.ContextWindow
			// Multiply before dividing so a whole-percent gauge reads back
			// exactly ("7", not "7.000000000000001").
			agentInfo.UsagePercent = float64(gauge.Tokens) * 100 / float64(gauge.ContextWindow)
			agentInfo.Confidence = "high"
		} else if gauge, ok := opencodeStatusBarUsage(agentType, cleanText, contextLimit, output.CapturedAt); ok {
			agentInfo.Source = "status_bar"
			agentInfo.EstimatedTokens = gauge.Tokens
			agentInfo.WithOverhead = gauge.Tokens
			agentInfo.ContextLimit = gauge.ContextWindow
			agentInfo.UsagePercent = float64(gauge.Tokens) * 100 / float64(gauge.ContextWindow)
			agentInfo.Confidence = "high"
		} else if usage, ok := paneTranscripts[pane.ID]; ok {
			agentInfo.Source = "transcript"
			agentInfo.TranscriptTokens = usage.Tokens
			agentInfo.TranscriptModel = usage.Model
			agentInfo.TranscriptPath = usage.Path
			agentInfo.TranscriptUpdatedAt = usage.UpdatedAt.UTC().Format(time.RFC3339)
			agentInfo.EstimatedTokens = usage.Tokens
			agentInfo.WithOverhead = usage.Tokens // transcript totals need no overhead multiplier
			readingModel, pct, limit, capped := transcriptContextReading(agentInfo.Model, usage)
			agentInfo.Model = readingModel
			agentInfo.ContextLimit = limit
			agentInfo.UsagePercent = 100.0
			agentInfo.Confidence = ntmctx.TranscriptConfidence(usage.UpdatedAt, time.Now())
			if limit > 0 {
				agentInfo.UsagePercent = pct
				if capped {
					// The limit, not the tokens, is suspect: say so.
					agentInfo.Confidence = "low"
				}
			}
		}

		agentInfo.UsageLevel = getUsageLevel(agentInfo.UsagePercent)

		// Align thresholds with getUsageLevel: <40% is Low, >=70% is High/Critical
		if agentInfo.UsagePercent < 40 {
			lowUsage = append(lowUsage, paneKey)
		} else if agentInfo.UsagePercent >= 70 {
			highUsage = append(highUsage, paneKey)
		}
		totalUsage += agentInfo.UsagePercent

		output.Agents = append(output.Agents, agentInfo)
	}

	output.Summary.TotalAgents = len(output.Agents)
	output.Summary.HighUsageCount = len(highUsage)
	if len(output.Agents) > 0 {
		output.Summary.AvgUsage = totalUsage / float64(len(output.Agents))
	}

	// Add pending rotations for this session
	pendingRotations, _ := ntmctx.GetPendingRotationsForSession(session)
	for _, p := range pendingRotations {
		output.PendingRotations = append(output.PendingRotations, ContextPendingRotationInfo{
			AgentID:        p.AgentID,
			SessionName:    p.SessionName,
			PaneID:         p.PaneID,
			ContextPercent: p.ContextPercent,
			CreatedAt:      p.CreatedAt.Format(time.RFC3339),
			TimeoutAt:      p.TimeoutAt.Format(time.RFC3339),
			WorkDir:        p.WorkDir,
		})
	}

	output.AgentHints = generateContextHints(lowUsage, highUsage, len(highUsage), len(output.Agents))

	return output, nil
}

// PrintContext outputs context window usage information for all agents in a session.
func PrintContext(session string, lines int) error {
	output, err := GetContext(session, lines)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot context failed")
}

// =============================================================================
// Activity Detection API
// =============================================================================

// ActivityOptions holds options for the activity API.
type ActivityOptions struct {
	Session       string       // Required: session name
	Panes         []string     // Optional: filter to specific pane indices
	AgentTypes    []string     // Optional: filter to specific agent types (claude, codex, antigravity, grok, gemini)
	Store         *state.Store `json:"-"`
	ExplicitStore bool         `json:"-"`
}

// ActivityOutput represents the output for --robot-activity
type ActivityOutput struct {
	RobotResponse
	Session    string              `json:"session"`
	CapturedAt time.Time           `json:"captured_at"`
	Agents     []AgentActivityInfo `json:"agents"`
	Summary    ActivitySummary     `json:"summary"`
	AgentHints *ActivityAgentHints `json:"_agent_hints,omitempty"`

	// SourceHealth reports per-source freshness/provenance metadata for the
	// data feeding this output. Populated for the `tmux` source today; the
	// `degraded_features` slice names which output fields fall back to a
	// stale or unavailable value when a source is unhealthy. See ntm#117 —
	// downstream watchdogs must distinguish "live tmux observation" from
	// "stale snapshot" before making restart/capacity decisions.
	SourceHealth map[string]SourceHealthEntry `json:"source_health,omitempty"`
}

// AgentActivityInfo contains activity state for a single agent pane.
type AgentActivityInfo struct {
	Pane             string   `json:"pane"`                        // pane index as string
	PaneIdx          int      `json:"pane_idx"`                    // pane index as int
	AgentType        string   `json:"agent_type"`                  // claude, codex, antigravity, grok, gemini, or another supported type
	State            string   `json:"state"`                       // GENERATING, WAITING, THINKING, ERROR, STALLED, UNKNOWN
	Confidence       float64  `json:"confidence"`                  // 0.0-1.0
	Velocity         float64  `json:"velocity"`                    // chars/sec
	StateSince       string   `json:"state_since,omitempty"`       // RFC3339 timestamp
	DetectedPatterns []string `json:"detected_patterns,omitempty"` // pattern names that matched
	LastOutput       string   `json:"last_output,omitempty"`       // RFC3339 timestamp of last output

	// PanePID is the tmux shell PID (`#{pane_pid}`) of the pane this
	// observation came from. Populated additively so callers can detect a
	// pane respawn — the pane index stays the same after restart, but
	// PanePID changes. Without this, downstream watchdogs treat post-respawn
	// observations as a silent continuation of the old observation stream
	// and make wrong recovery decisions. See ntm#117.
	PanePID int `json:"pane_pid,omitempty"`

	// Per-pane capture provenance (ntm#117 deferred item #1). Output-level
	// `source_health.tmux` answers "is anyone reachable"; these fields
	// answer "which specific pane went dark" when a fleet watchdog polls
	// --robot-activity across many sessions and one pane's classification
	// silently fails (today the pane appears with State=UNKNOWN and a
	// classification error is dropped on the floor).
	//
	//   capture_collected_at  RFC 3339 timestamp the per-pane classification
	//                         attempt started.
	//   capture_provenance    "live" when the classifier ran cleanly;
	//                         "unavailable" when classification returned an
	//                         error (and we synthesized State=UNKNOWN).
	//   capture_error         the underlying classifier error string when
	//                         capture_provenance == "unavailable". Omitted
	//                         on the happy path.
	CaptureCollectedAt    string  `json:"capture_collected_at,omitempty"`
	CaptureProvenance     string  `json:"capture_provenance,omitempty"`
	CaptureError          string  `json:"capture_error,omitempty"`
	ObservationState      string  `json:"observation_state"`
	ObservationFreshness  string  `json:"observation_freshness"`
	ObservationConfidence float64 `json:"observation_confidence"`
	SafeToDispatch        bool    `json:"safe_to_dispatch"`

	// OutputSequence is the durable, privacy-preserving output-change signal
	// (#246): an opaque epoch plus a monotonic sequence that advances only
	// when NTM detects a real output change for this pane. Absent when no
	// runtime projection store is configured. See OutputSequenceInfo for the
	// comparison contract.
	OutputSequence *OutputSequenceInfo `json:"output_sequence,omitempty"`
}

// SourceHealthEntry describes the freshness of a source feeding a robot
// output. Field names, types, and JSON keys exactly match the documented
// contract in docs/freshness-degraded-state-contract.md §2.2, and the
// timestamp fields use the same `string` (RFC 3339) shape as the existing
// adapters.SourceInfo timestamps (UpdatedAt / DegradedSince / RetryingAt)
// so this surface can be consumed interchangeably with the existing
// adapters.SourceHealthSection used by --robot-status. See ntm#117.
type SourceHealthEntry struct {
	// Identity
	Source string `json:"source"` // e.g. "tmux"
	// Status enum: "fresh" | "stale" | "unavailable" | "unknown".
	Status string `json:"status"`
	// Timing — RFC 3339 timestamp + integer seconds. CollectedAt is a
	// string (not time.Time) because the contract spec types it as a
	// string and the matching adapters.SourceInfo timestamps are also
	// strings; storing a Go time.Time here would marshal to RFC 3339Nano
	// (sub-second precision) and silently surprise downstream consumers
	// that expect the doc's stripped-to-seconds RFC 3339 shape. Integer
	// FreshnessSec / StaleAfterSec match the contract for the same reason
	// — float values would emit `0.000123` and break strict consumers.
	CollectedAt   string `json:"collected_at"`
	FreshnessSec  int    `json:"freshness_sec"`
	StaleAfterSec int    `json:"stale_after_sec"`
	// Degradation — degraded_features names which output fields are
	// stale or missing because of this source. last_error / last_error_at
	// are populated when status != "fresh"; they're optional in the
	// healthy case so a fresh entry stays compact.
	DegradedFeatures []string `json:"degraded_features,omitempty"`
	LastError        string   `json:"last_error,omitempty"`
	LastErrorAt      string   `json:"last_error_at,omitempty"`
	// Provenance enum: "live" | "cached" | "derived".
	Provenance string `json:"provenance"`
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

// GetActivity returns agent activity state for a session.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetActivity(opts ActivityOptions) (*ActivityOutput, error) {
	// Resolve labeled session names (e.g. "myproject" -> "myproject--frontend")
	// so callers don't need to know the exact tmux session name. (ntm#104)
	opts.Session = resolveSessionName(opts.Session)

	output := &ActivityOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		CapturedAt:    time.Now().UTC(),
		Agents:        make([]AgentActivityInfo, 0),
		Summary: ActivitySummary{
			ByState: make(map[string]int),
		},
	}

	if !tmux.SessionExists(opts.Session) {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session '%s' not found", opts.Session),
			ErrCodeSessionNotFound,
			"Use 'ntm list' to see available sessions",
		)
		return output, nil
	}

	// Track tmux source health from one bounded canonical observation. Pane-level
	// capture failures remain in the result as UNKNOWN instead of disappearing.
	tmuxCollectedAt := time.Now().UTC()
	tmuxCollectedAtStr := tmuxCollectedAt.Format(time.RFC3339)
	observer := newRobotSessionObserver(0)
	observation, err := observer.Observe(context.Background(), opts.Session)
	if err != nil {
		output.SourceHealth = map[string]SourceHealthEntry{
			"tmux": {
				Source:           "tmux",
				Status:           "unavailable",
				CollectedAt:      tmuxCollectedAtStr,
				FreshnessSec:     0,
				StaleAfterSec:    5,
				Provenance:       "live",
				DegradedFeatures: []string{"agent_states", "pane_pid", "agent_list"},
				LastError:        err.Error(),
				LastErrorAt:      time.Now().UTC().Format(time.RFC3339),
			},
		}
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to get panes: %w", err),
			ErrCodeInternalError,
			"Check tmux is running and session is accessible",
		)
		return output, nil
	}
	panes := observationPanes(observation)
	observationsByID := observationPaneMap(observation)

	// Mark tmux source as fresh — pane data was collected live from tmux
	// just now. `stale_after_sec` is a hint that downstream watchdogs
	// should re-poll if they see a freshness exceeding it; we conservatively
	// suggest 5s for live observation.
	output.SourceHealth = map[string]SourceHealthEntry{
		"tmux": {
			Source:        "tmux",
			Status:        "fresh",
			CollectedAt:   tmuxCollectedAtStr,
			FreshnessSec:  int(time.Since(tmuxCollectedAt).Seconds()),
			StaleAfterSec: 5,
			Provenance:    "live",
		},
	}
	if !observation.Complete {
		source := output.SourceHealth["tmux"]
		source.Status = "stale"
		source.DegradedFeatures = []string{"agent_states"}
		if len(observation.Failures) > 0 {
			source.LastError = observation.Failures[0].Error
			source.LastErrorAt = tmuxCollectedAtStr
		}
		output.SourceHealth["tmux"] = source
	}

	output.Agents = make([]AgentActivityInfo, 0, len(panes))

	// Build filter maps (topology-aware, #172).
	multiWindow := paneSessionIsMultiWindow(panes)
	paneFilter := opts.Panes
	hasPaneFilter := len(paneFilter) > 0
	selectedPaneIDs, resolveErr := resolveActivityPaneIDs(panes, paneFilter)
	if resolveErr != nil {
		output.RobotResponse = NewErrorResponse(
			resolveErr,
			paneSelectorRobotErrorCode(resolveErr),
			"Use --robot-status or --robot-activity without --panes to inspect canonical pane addresses",
		)
		return output, nil
	}

	typeFilterMap := make(map[string]bool)
	for _, t := range opts.AgentTypes {
		typeFilterMap[normalizeAgentType(t)] = true
	}
	hasTypeFilter := len(typeFilterMap) > 0

	// Collect activity data
	var availableAgents, busyAgents, problemAgents []string

	for _, pane := range panes {
		paneKey := paneTargetKey(pane, multiWindow)

		// Apply pane filter
		if hasPaneFilter {
			if _, selected := selectedPaneIDs[pane.Ref().StableKey()]; !selected {
				continue
			}
		}

		// Tagged service panes are not agents: a long-lived service whose
		// terminal has gone quiet must never be reported busy, idle or in
		// error (ntm#305).
		if pane.IsServicePane() {
			continue
		}

		agentType := paneAgentType(pane)

		// Skip non-agent panes (user, unknown)
		if agentType == "unknown" || agentType == "user" {
			continue
		}

		// Apply type filter
		if hasTypeFilter && !typeFilterMap[agentType] {
			continue
		}

		paneObservation, observed := observationsByID[pane.Ref().StableKey()]
		if !observed {
			paneObservation = status.PaneObservation{
				Pane:      pane.Ref(),
				AgentType: agentType,
				Current: status.StateObservation{
					Status:     status.AgentStatus{State: status.StateUnknown},
					ObservedAt: tmuxCollectedAt,
					Freshness:  status.FreshnessUnavailable,
					Error:      "pane missing from canonical observation",
				},
			}
		}

		// Preserve the richer velocity/pattern classifier, but feed it the exact
		// output from the canonical observation instead of capturing tmux again.
		classifier := NewStateClassifier(pane.ID, &ClassifierConfig{
			AgentType: agentType,
			PaneWidth: pane.Width,
		})

		paneCapturedAt := FormatTimestamp(paneObservation.Current.ObservedAt)
		var activity *AgentActivity
		if paneObservation.Current.Freshness == status.FreshnessFresh && paneObservation.Current.Error == "" {
			activity, err = classifier.ClassifyWithOutput(paneObservation.RawOutput)
		} else {
			err = fmt.Errorf("%s", paneObservation.Current.Error)
			if paneObservation.Current.Error == "" {
				err = fmt.Errorf("current pane observation is %s", paneObservation.Current.Freshness)
			}
		}
		if err != nil {
			// Include with unknown state on error, plus per-pane
			// capture provenance so a watchdog can distinguish
			// "this pane was classified UNKNOWN" from "this pane's
			// classifier silently errored". See ntm#117 deferred item #1.
			output.Agents = append(output.Agents, AgentActivityInfo{
				Pane:                  paneKey,
				PaneIdx:               pane.Index,
				AgentType:             agentType,
				State:                 string(StateUnknown),
				Confidence:            0.0,
				PanePID:               pane.PID,
				CaptureCollectedAt:    paneCapturedAt,
				CaptureProvenance:     string(paneObservation.Current.Freshness),
				CaptureError:          err.Error(),
				ObservationState:      string(paneObservation.Current.Status.State),
				ObservationFreshness:  string(paneObservation.Current.Freshness),
				ObservationConfidence: paneObservation.Current.Confidence,
				SafeToDispatch:        false,
			})
			output.Summary.ByState[string(StateUnknown)]++
			continue
		}

		// Build agent info
		info := AgentActivityInfo{
			Pane:                  paneKey,
			PaneIdx:               pane.Index,
			AgentType:             activity.AgentType,
			State:                 string(activity.State),
			Confidence:            activity.Confidence,
			Velocity:              activity.Velocity,
			DetectedPatterns:      activity.DetectedPatterns,
			PanePID:               pane.PID,
			CaptureCollectedAt:    paneCapturedAt,
			CaptureProvenance:     "live",
			ObservationState:      string(paneObservation.Current.Status.State),
			ObservationFreshness:  string(paneObservation.Current.Freshness),
			ObservationConfidence: paneObservation.Current.Confidence,
			SafeToDispatch:        paneObservation.SafeToDispatch(),
			OutputSequence: observeOutputSequenceWithStore(
				resolveProjectionStore(opts.Store, opts.ExplicitStore),
				opts.Session, pane.ID, paneObservation.RawOutput,
				paneObservation.Current.ObservedAt,
			),
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

	return output, nil
}

func resolveActivityPaneIDs(panes []tmux.Pane, selectors []string) (map[string]struct{}, error) {
	selectedIDs := make(map[string]struct{})
	if len(selectors) == 0 {
		return selectedIDs, nil
	}
	selected, err := tmux.ResolvePaneSelectors(panes, selectors, false)
	if err != nil {
		return nil, err
	}
	for _, pane := range selected {
		selectedIDs[pane.Ref().StableKey()] = struct{}{}
	}
	return selectedIDs, nil
}

// PrintActivity handles the --robot-activity command.
// This is a thin wrapper around GetActivity() for CLI output.
func PrintActivity(opts ActivityOptions) error {
	output, err := GetActivity(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot activity failed")
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

// normalizeAgentType normalizes agent type aliases via the shared resolver.
func normalizeAgentType(t string) string {
	return ResolveAgentType(t)
}

// matchesAgentTypeFilter compares an observed agent type against a user-supplied
// filter using shared alias normalization. Empty filters match everything.
func matchesAgentTypeFilter(agentType, filter string) bool {
	if strings.TrimSpace(filter) == "" {
		return true
	}
	return normalizeAgentType(agentType) == normalizeAgentType(filter)
}

// normalizedAgentTypeFilter canonicalizes a --type list (aliases resolved,
// blanks dropped, sorted, deduplicated) for target filtering and for
// idempotency bindings.
func normalizedAgentTypeFilter(agentTypes []string) []string {
	out := make([]string, 0, len(agentTypes))
	for _, t := range agentTypes {
		if strings.TrimSpace(t) == "" {
			continue
		}
		out = append(out, normalizeAgentType(t))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// keepPanesOfAgentTypes narrows panes to those whose agent type (as typeOf
// classifies it) is in the --type list; an empty list keeps every pane.
func keepPanesOfAgentTypes(panes []tmux.Pane, agentTypes []string, typeOf func(tmux.Pane) string) []tmux.Pane {
	wanted := normalizedAgentTypeFilter(agentTypes)
	if len(wanted) == 0 {
		return panes
	}
	kept := make([]tmux.Pane, 0, len(panes))
	for _, pane := range panes {
		if slices.Contains(wanted, normalizeAgentType(typeOf(pane))) {
			kept = append(kept, pane)
		}
	}
	return kept
}
