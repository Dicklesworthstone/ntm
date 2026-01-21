// Package robot provides machine-readable output for AI agents.
// smart_restart.go implements the --robot-smart-restart command for safe agent restarts.
package robot

import (
	"fmt"
	"strconv"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// =============================================================================
// Robot Smart-Restart Command (bd-2c7f4)
// =============================================================================
//
// This is the SAFE restart mechanism that embodies the core principle:
// **NEVER interrupt agents doing useful work!!!**
//
// Unlike a naive restart that blindly kills and relaunches, smart-restart:
// 1. Checks first - Calls is-working before any action
// 2. Refuses if working - Returns SKIPPED, does NOT interrupt
// 3. Handles rate limits - Knows to wait rather than immediately restart
// 4. Verifies success - Confirms new agent actually launched

// RestartActionType represents the action taken (or not taken) for a pane.
type RestartActionType string

const (
	// ActionRestarted indicates the agent was successfully restarted.
	ActionRestarted RestartActionType = "RESTARTED"

	// ActionSkipped indicates restart was skipped (agent working or other reason).
	ActionSkipped RestartActionType = "SKIPPED"

	// ActionWaiting indicates agent is rate-limited and should wait.
	ActionWaiting RestartActionType = "WAITING"

	// ActionFailed indicates restart attempt failed.
	ActionFailed RestartActionType = "FAILED"

	// ActionWouldRestart indicates restart would occur (dry-run mode).
	ActionWouldRestart RestartActionType = "WOULD_RESTART"
)

// SmartRestartOptions configures the smart-restart command.
type SmartRestartOptions struct {
	Session       string        // Session name (required)
	Panes         []int         // Pane indices to restart (empty = all non-control panes)
	Force         bool          // Force restart even if working (dangerous!)
	DryRun        bool          // Show what would happen without doing it
	Prompt        string        // Optional prompt to send after restart
	LinesCaptured int           // Lines to capture for pre-check (default: 100)
	Verbose       bool          // Include extra debugging info
	PostWaitTime  time.Duration // Time to wait after launch before verification (default: 6s)
}

// DefaultSmartRestartOptions returns sensible defaults.
func DefaultSmartRestartOptions() SmartRestartOptions {
	return SmartRestartOptions{
		LinesCaptured: 100,
		PostWaitTime:  6 * time.Second,
	}
}

// PreCheckInfo contains the pre-restart state assessment.
type PreCheckInfo struct {
	Recommendation   string   `json:"recommendation"`
	IsWorking        bool     `json:"is_working"`
	IsIdle           bool     `json:"is_idle"`
	IsRateLimited    bool     `json:"is_rate_limited"`
	IsContextLow     bool     `json:"is_context_low"`
	ContextRemaining *float64 `json:"context_remaining,omitempty"`
	Confidence       float64  `json:"confidence"`
	AgentType        string   `json:"agent_type"`
}

// RestartSequence documents the restart execution steps.
type RestartSequence struct {
	ExitMethod     string `json:"exit_method"`
	ExitDurationMs int    `json:"exit_duration_ms"`
	ShellConfirmed bool   `json:"shell_confirmed"`
	AgentLaunched  bool   `json:"agent_launched"`
	AgentType      string `json:"agent_type"`
	PromptSent     bool   `json:"prompt_sent,omitempty"`
}

// PostStateInfo contains the verified state after restart.
type PostStateInfo struct {
	AgentRunning bool   `json:"agent_running"`
	AgentType    string `json:"agent_type"`
	Confidence   float64 `json:"confidence"`
}

// WaitInfo provides details about rate-limit waiting.
type WaitInfo struct {
	ResetsAt    string `json:"resets_at,omitempty"`
	WaitSeconds int    `json:"wait_seconds,omitempty"`
	Suggestion  string `json:"suggestion,omitempty"`
}

// RestartAction documents the action taken for a single pane.
type RestartAction struct {
	Action          RestartActionType `json:"action"`
	Reason          string            `json:"reason"`
	Warning         string            `json:"warning,omitempty"`
	PreCheck        *PreCheckInfo     `json:"pre_check,omitempty"`
	RestartSequence *RestartSequence  `json:"restart_sequence,omitempty"`
	PostState       *PostStateInfo    `json:"post_state,omitempty"`
	WaitInfo        *WaitInfo         `json:"wait_info,omitempty"`
	Error           string            `json:"error,omitempty"`
}

// RestartSummary aggregates results across all panes.
type RestartSummary struct {
	Restarted     int                `json:"restarted"`
	Skipped       int                `json:"skipped"`
	Waiting       int                `json:"waiting"`
	Failed        int                `json:"failed"`
	WouldRestart  int                `json:"would_restart,omitempty"`
	PanesByAction map[string][]int   `json:"panes_by_action"`
}

// SmartRestartOutput is the response for --robot-smart-restart.
type SmartRestartOutput struct {
	RobotResponse
	Session   string                   `json:"session"`
	Timestamp string                   `json:"timestamp"`
	DryRun    bool                     `json:"dry_run"`
	Force     bool                     `json:"force"`
	Actions   map[string]RestartAction `json:"actions"`
	Summary   RestartSummary           `json:"summary"`
}

// PrintSmartRestart outputs the smart restart result in JSON format.
func PrintSmartRestart(opts SmartRestartOptions) error {
	output, err := SmartRestart(opts)
	if err != nil {
		// SmartRestart already sets error fields on output
		return encodeJSON(output)
	}
	return encodeJSON(output)
}

// SmartRestart performs intelligent agent restart with safety checks.
// It returns the structured result instead of printing JSON.
func SmartRestart(opts SmartRestartOptions) (*SmartRestartOutput, error) {
	output := &SmartRestartOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		DryRun:        opts.DryRun,
		Force:         opts.Force,
		Actions:       make(map[string]RestartAction),
		Summary: RestartSummary{
			PanesByAction: make(map[string][]int),
		},
	}

	// Step 1: Pre-check all panes using IsWorking
	isWorkingOpts := IsWorkingOptions{
		Session:       opts.Session,
		Panes:         opts.Panes,
		LinesCaptured: opts.LinesCaptured,
		Verbose:       opts.Verbose,
	}

	isWorkingResult, err := IsWorking(isWorkingOpts)
	if err != nil {
		output.Success = false
		output.Error = err.Error()
		output.ErrorCode = isWorkingResult.ErrorCode
		output.Hint = isWorkingResult.Hint
		return output, err
	}

	// Step 2: Process each pane
	for paneStr, workStatus := range isWorkingResult.Panes {
		paneNum, _ := strconv.Atoi(paneStr)

		action := RestartAction{
			PreCheck: &PreCheckInfo{
				Recommendation:   workStatus.Recommendation,
				IsWorking:        workStatus.IsWorking,
				IsIdle:           workStatus.IsIdle,
				IsRateLimited:    workStatus.IsRateLimited,
				IsContextLow:     workStatus.IsContextLow,
				ContextRemaining: workStatus.ContextRemaining,
				Confidence:       workStatus.Confidence,
				AgentType:        workStatus.AgentType,
			},
		}

		// Determine action based on pre-check
		shouldRestart, reason, warning := decideRestart(&workStatus, opts.Force)

		switch {
		case workStatus.IsRateLimited && !opts.Force:
			action.Action = ActionWaiting
			action.Reason = "Rate limited - wait for reset"
			action.WaitInfo = buildWaitInfo(&workStatus)
			output.Summary.Waiting++
			appendPaneToAction(output.Summary.PanesByAction, "WAITING", paneNum)

		case !shouldRestart:
			action.Action = ActionSkipped
			action.Reason = reason
			output.Summary.Skipped++
			appendPaneToAction(output.Summary.PanesByAction, "SKIPPED", paneNum)

		case opts.DryRun:
			action.Action = ActionWouldRestart
			action.Reason = reason
			if warning != "" {
				action.Warning = warning
			}
			output.Summary.WouldRestart++
			appendPaneToAction(output.Summary.PanesByAction, "WOULD_RESTART", paneNum)

		default:
			// Actually perform restart
			if warning != "" {
				action.Warning = warning
			}
			restartResult, restartErr := executeRestart(opts.Session, paneNum, workStatus.AgentType, opts)
			if restartErr != nil {
				action.Action = ActionFailed
				action.Reason = reason
				action.Error = restartErr.Error()
				output.Summary.Failed++
				appendPaneToAction(output.Summary.PanesByAction, "FAILED", paneNum)
			} else {
				action.Action = ActionRestarted
				action.Reason = reason
				action.RestartSequence = restartResult
				action.PostState = verifyRestart(opts.Session, paneNum, opts)
				output.Summary.Restarted++
				appendPaneToAction(output.Summary.PanesByAction, "RESTARTED", paneNum)
			}
		}

		output.Actions[paneStr] = action
	}

	return output, nil
}

// decideRestart determines whether a pane should be restarted based on its state.
// Returns (shouldRestart, reason, warning).
func decideRestart(status *PaneWorkStatus, force bool) (bool, string, string) {
	rec := status.Recommendation
	var warning string

	// CRITICAL: Never restart working agents unless forced
	if status.IsWorking && !force {
		return false, "Agent is actively working", ""
	}

	// Handle force on working agent
	if status.IsWorking && force {
		warning = "FORCED restart of working agent - data may be lost!"
	}

	switch rec {
	case "DO_NOT_INTERRUPT":
		if force {
			return true, "FORCED restart of working agent", warning
		}
		return false, "Agent is actively working", ""

	case "SAFE_TO_RESTART":
		return true, "Agent is idle", ""

	case "CONTEXT_LOW_CONTINUE":
		if status.IsWorking {
			if force {
				return true, "FORCED restart of working agent with low context", warning
			}
			return false, "Working with low context - let finish", ""
		}
		if status.ContextRemaining != nil {
			return true, formatRestartReason("Idle with low context (%.0f%%)", *status.ContextRemaining), ""
		}
		return true, "Idle with low context", ""

	case "RATE_LIMITED_WAIT":
		if force {
			return true, "FORCED restart despite rate limit", "Restarting won't help - still rate limited"
		}
		return false, "Rate limited - waiting for reset", ""

	case "ERROR_STATE":
		return true, "Agent in error state", ""

	default:
		if force {
			return true, "FORCED restart of unknown state", "Unknown state - results unpredictable"
		}
		return false, "Unknown state - manual inspection needed", ""
	}
}

// formatRestartReason formats a reason string with a value.
func formatRestartReason(format string, value float64) string {
	// Simple percentage formatting without fmt
	return formatReasonWithPercent(format, value)
}

// formatReasonWithPercent inserts a rounded percentage into a format string.
func formatReasonWithPercent(format string, pct float64) string {
	result := ""
	pctStr := formatFloat(pct)
	for i := 0; i < len(format); i++ {
		if format[i] == '%' && i+1 < len(format) {
			if format[i+1] == '%' {
				result += "%"
				i++
			} else if format[i+1] == '.' && i+3 < len(format) && format[i+2] == '0' && format[i+3] == 'f' {
				result += pctStr
				i += 3
			} else {
				result += string(format[i])
			}
		} else {
			result += string(format[i])
		}
	}
	return result
}

// buildWaitInfo constructs wait information for rate-limited agents.
func buildWaitInfo(status *PaneWorkStatus) *WaitInfo {
	info := &WaitInfo{
		Suggestion: "Consider caam account switch",
	}

	// If we have rate limit info from indicators, use it
	// For now, provide generic guidance
	info.WaitSeconds = 3600 // Default 1 hour estimate

	return info
}

// appendPaneToAction adds a pane number to the action's pane list.
func appendPaneToAction(panesByAction map[string][]int, action string, pane int) {
	if panesByAction[action] == nil {
		panesByAction[action] = []int{}
	}
	panesByAction[action] = append(panesByAction[action], pane)
}

// executeRestart performs the actual restart sequence for a pane.
func executeRestart(session string, pane int, agentType string, opts SmartRestartOptions) (*RestartSequence, error) {
	seq := &RestartSequence{
		AgentType: agentType,
	}

	// Step 1: Exit the current agent using agent-specific method
	exitErr := exitAgent(session, pane, agentType, seq)
	if exitErr != nil {
		return seq, exitErr
	}

	// Step 2: Wait for shell to return
	seq.ExitDurationMs = 3000
	time.Sleep(3 * time.Second)

	// Step 3: Verify shell prompt
	target := fmt.Sprintf("%s:1.%d", session, pane)
	output, err := tmux.CapturePaneOutput(target, 10)
	if err != nil {
		return seq, wrapError("capture failed", err)
	}

	seq.ShellConfirmed = looksLikeShellPrompt(output)
	if !seq.ShellConfirmed {
		return seq, newError("shell prompt not detected after exit")
	}

	// Step 4: Launch new agent using alias
	alias := agentType // cc, cod, gmi
	if alias == "unknown" {
		alias = "cc" // Default to Claude Code if unknown
	}

	launchErr := sendKeys(session, pane, alias+"\n")
	if launchErr != nil {
		return seq, wrapError("launch failed", launchErr)
	}

	// Step 5: Wait for agent initialization
	waitTime := opts.PostWaitTime
	if waitTime == 0 {
		waitTime = 6 * time.Second
	}
	time.Sleep(waitTime)

	// Step 6: Send prompt if provided
	if opts.Prompt != "" {
		time.Sleep(500 * time.Millisecond)
		promptErr := sendKeys(session, pane, opts.Prompt+"\n")
		if promptErr != nil {
			// Non-fatal - agent launched but prompt failed
			seq.AgentLaunched = true
			seq.PromptSent = false
			return seq, nil
		}
		seq.PromptSent = true
	}

	seq.AgentLaunched = true
	return seq, nil
}

// verifyRestart checks the post-restart state of a pane.
func verifyRestart(session string, pane int, opts SmartRestartOptions) *PostStateInfo {
	// Capture current state
	target := fmt.Sprintf("%s:1.%d", session, pane)
	content, err := tmux.CapturePaneOutput(target, 50)
	if err != nil {
		return &PostStateInfo{
			AgentRunning: false,
			Confidence:   0.0,
		}
	}

	// Parse the output to determine agent state
	parser := agent.NewParser()
	state, err := parser.Parse(content)
	if err != nil {
		return &PostStateInfo{
			AgentRunning: false,
			Confidence:   0.0,
		}
	}

	return &PostStateInfo{
		AgentRunning: state.Type != agent.AgentTypeUnknown,
		AgentType:    string(state.Type),
		Confidence:   state.Confidence,
	}
}

// looksLikeShellPrompt checks if output appears to be at a shell prompt.
func looksLikeShellPrompt(output string) bool {
	// Look for common shell prompt indicators
	shellIndicators := []string{
		"$ ",
		"% ",
		"# ",
		"❯ ",
		"→ ",
		"> ",
	}

	// Check last few lines for prompt
	lines := splitLines(output)
	if len(lines) == 0 {
		return false
	}

	// Check last non-empty line
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-5; i-- {
		line := trimSpace(lines[i])
		if line == "" {
			continue
		}
		for _, indicator := range shellIndicators {
			if containsSuffix(line, indicator) || containsSuffix(line, trimSpace(indicator)) {
				return true
			}
		}
		// Also check if line ends with these characters
		lastChar := line[len(line)-1]
		if lastChar == '$' || lastChar == '%' || lastChar == '#' || lastChar == '>' {
			return true
		}
		break // Only check last non-empty line
	}

	return false
}

// containsSuffix checks if s ends with suffix.
func containsSuffix(s, suffix string) bool {
	if len(suffix) > len(s) {
		return false
	}
	return s[len(s)-len(suffix):] == suffix
}

// trimSpace removes leading and trailing whitespace.
func trimSpace(s string) string {
	start := 0
	end := len(s)
	for start < end && isSpace(s[start]) {
		start++
	}
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

// isSpace checks if a character is whitespace.
func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// Error helpers to avoid fmt import
type simpleError struct {
	msg string
}

func (e *simpleError) Error() string {
	return e.msg
}

func newError(msg string) error {
	return &simpleError{msg: msg}
}

func wrapError(prefix string, err error) error {
	return &simpleError{msg: prefix + ": " + err.Error()}
}
