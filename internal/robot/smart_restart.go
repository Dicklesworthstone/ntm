// Package robot provides machine-readable output for AI agents.
// smart_restart.go implements the --robot-smart-restart command for safe agent restarts.
package robot

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/process"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/session"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const (
	// shellReturnTimeout bounds the post-exit poll for the shell to return.
	// Agents like Claude can take several seconds to tear down, so a fixed
	// short sleep produced false SHELL_NOT_RETURNED failures (#187).
	shellReturnTimeout = 12 * time.Second
	// shellReturnPollInterval is the shell-return poll cadence.
	shellReturnPollInterval = 400 * time.Millisecond
	// smartRestartMinReadyTimeout is the floor for the post-launch ready-gate.
	smartRestartMinReadyTimeout = 10 * time.Second
	// smartRestartMutationObservationTimeout bounds the independent observation
	// performed after caller cancellation when launch actuation may have occurred.
	smartRestartMutationObservationTimeout = 2 * time.Second
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
	Session        string        // Session name (required)
	PaneSelectors  []string      // N, W.P, or %N selectors (empty = all non-control panes)
	Panes          []int         // Legacy bare pane indices for internal callers
	Force          bool          // Force restart even if working (dangerous!)
	DryRun         bool          // Show what would happen without doing it
	Prompt         string        // Optional prompt to send after restart
	LinesCaptured  int           // Lines to capture for pre-check (default: 100)
	Verbose        bool          // Include extra debugging info
	PostWaitTime   time.Duration // Max time to wait for agent readiness after launch (ready-gate polls and exits early; floored at 10s, #187)
	HardKill       bool          // Use hard kill (kill -9) as fallback if soft exit fails (bd-bh74z)
	HardKillOnly   bool          // Skip soft exit entirely and use kill -9 immediately
	promptDispatch smartRestartPromptDispatcher
	executor       smartRestartExecutor
}

type smartRestartPromptDispatcher interface {
	Execute(context.Context, dispatchsvc.Request) (dispatchsvc.Result, error)
}

// smartRestartExecutor isolates the restart lifecycle from the prompt handoff.
// Production uses live tmux/process operations; tests inject a deterministic
// executor so prompt outcomes can be exercised without launching real agents.
type smartRestartExecutor interface {
	exitAgent(context.Context, string, int, int, string, *RestartSequence) error
	waitForShellReturn(context.Context, string, int, int, time.Duration) (bool, string, error)
	hardKillAgent(context.Context, string, int, int, *RestartSequence) (*HardKillResult, error)
	sendKeys(context.Context, string, int, int, string) error
	getShellPID(context.Context, string, int, int) (int, error)
	waitForPaneAgentReady(context.Context, string, int, string, time.Duration) (bool, error)
	capturePaneOutput(context.Context, string, int) (string, error)
	hasChildAlive(int) bool
}

type liveSmartRestartExecutor struct{}

func (liveSmartRestartExecutor) exitAgent(ctx context.Context, session string, win, pane int, agentType string, seq *RestartSequence) error {
	return exitAgent(ctx, session, win, pane, agentType, seq)
}

func (liveSmartRestartExecutor) waitForShellReturn(ctx context.Context, session string, win, pane int, timeout time.Duration) (bool, string, error) {
	return waitForShellReturn(ctx, session, win, pane, timeout)
}

func (liveSmartRestartExecutor) hardKillAgent(ctx context.Context, session string, win, pane int, seq *RestartSequence) (*HardKillResult, error) {
	return hardKillAgent(ctx, session, win, pane, seq)
}

func (liveSmartRestartExecutor) sendKeys(ctx context.Context, session string, win, pane int, keys string) error {
	return sendKeys(ctx, session, win, pane, keys)
}

func (liveSmartRestartExecutor) getShellPID(ctx context.Context, session string, win, pane int) (int, error) {
	return getShellPID(ctx, session, win, pane)
}

func (liveSmartRestartExecutor) waitForPaneAgentReady(ctx context.Context, target string, shellPID int, agentType string, timeout time.Duration) (bool, error) {
	return waitForPaneAgentReadyContext(ctx, target, shellPID, agentType, timeout)
}

func (liveSmartRestartExecutor) capturePaneOutput(ctx context.Context, target string, lines int) (string, error) {
	return tmux.CapturePaneOutputContext(ctx, target, lines)
}

func (liveSmartRestartExecutor) hasChildAlive(shellPID int) bool {
	return process.HasChildAlive(shellPID)
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

// PromptDeliveryStatus is the terminal state of a requested post-restart
// prompt handoff. Ambiguous means delivery may have occurred and callers must
// inspect the pane before retrying to avoid duplicate work.
type PromptDeliveryStatus string

const (
	PromptDeliveryNotAttempted PromptDeliveryStatus = "not_attempted"
	PromptDeliveryDelivered    PromptDeliveryStatus = "delivered"
	PromptDeliveryFailed       PromptDeliveryStatus = "failed"
	PromptDeliveryBlocked      PromptDeliveryStatus = "blocked"
	PromptDeliveryAmbiguous    PromptDeliveryStatus = "ambiguous"
)

// PromptDeliveryOutcome is a safe, message-free receipt summary for the
// optional post-restart prompt.
type PromptDeliveryOutcome struct {
	Requested     bool                 `json:"requested"`
	Target        string               `json:"target,omitempty"`
	Status        PromptDeliveryStatus `json:"status"`
	Delivered     int                  `json:"delivered"`
	Failed        int                  `json:"failed"`
	Blocked       int                  `json:"blocked"`
	Skipped       int                  `json:"skipped"`
	ReceiptStatus string               `json:"receipt_status,omitempty"`
	DispatchCode  string               `json:"dispatch_code,omitempty"`
	Error         string               `json:"error,omitempty"`
}

// SmartRestartLaunchStatus is the strongest known launch lifecycle fact.
// Unknown means launch input may have reached the pane, so callers must inspect
// the target before retrying. NotReady means an independent observation found
// no ready agent child; LaunchAttempted distinguishes it from a pre-launch stop.
type SmartRestartLaunchStatus string

const (
	SmartRestartLaunchReady    SmartRestartLaunchStatus = "ready"
	SmartRestartLaunchNotReady SmartRestartLaunchStatus = "not_ready"
	SmartRestartLaunchUnknown  SmartRestartLaunchStatus = "unknown"
)

// RestartSequence documents the restart execution steps.
type RestartSequence struct {
	ExitMethod             string                   `json:"exit_method"`
	ExitDurationMs         int                      `json:"exit_duration_ms"`
	ShellConfirmed         bool                     `json:"shell_confirmed"`
	AgentLaunched          bool                     `json:"agent_launched"`
	AgentLaunchStatus      SmartRestartLaunchStatus `json:"agent_launch_status"`
	LaunchAttempted        bool                     `json:"launch_attempted"`
	ProcessAlive           *bool                    `json:"process_alive,omitempty"`
	ShellPID               int                      `json:"shell_pid,omitempty"`
	LaunchObservationError string                   `json:"launch_observation_error,omitempty"`
	AgentType              string                   `json:"agent_type"`
	PromptSent             bool                     `json:"prompt_sent,omitempty"`
	PromptOutcome          *PromptDeliveryOutcome   `json:"prompt_outcome,omitempty"`
	HardKillUsed           bool                     `json:"hard_kill_used,omitempty"`   // True if hard kill was needed (bd-bh74z)
	HardKillResult         *HardKillResult          `json:"hard_kill_result,omitempty"` // Details of hard kill operation
}

// PostStateInfo contains the verified state after restart.
type PostStateInfo struct {
	AgentRunning bool    `json:"agent_running"`
	AgentType    string  `json:"agent_type"`
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
	PromptError     *StructuredError  `json:"prompt_error,omitempty"`
	// PromptOutcome is set on declined panes (SKIPPED/WAITING) when a prompt
	// was requested: status not_attempted states explicitly that smart-restart
	// did NOT deliver the caller's prompt to the incumbent agent (bd-moje8).
	// Restarted panes carry their outcome in restart_sequence.prompt_outcome.
	PromptOutcome *PromptDeliveryOutcome `json:"prompt_outcome,omitempty"`
	// StructuredError provides detailed error context for failure diagnosis (bd-3vc3s).
	StructuredError *StructuredError `json:"structured_error,omitempty"`
}

// RestartSummary aggregates results across all panes.
type RestartSummary struct {
	Restarted              int                 `json:"restarted"`
	Skipped                int                 `json:"skipped"`
	Waiting                int                 `json:"waiting"`
	Failed                 int                 `json:"failed"`
	WouldRestart           int                 `json:"would_restart,omitempty"`
	PromptDelivered        int                 `json:"prompt_delivered,omitempty"`
	PromptFailed           int                 `json:"prompt_failed,omitempty"`
	PanesWithPromptFailure []string            `json:"panes_with_prompt_failure,omitempty"`
	PanesByAction          map[string][]string `json:"panes_by_action"`
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
func PrintSmartRestart(ctx context.Context, opts SmartRestartOptions) error {
	output, err := GetSmartRestart(ctx, opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot smart-restart failed")
}

// GetSmartRestart performs intelligent agent restart with safety checks.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetSmartRestart(ctx context.Context, opts SmartRestartOptions) (*SmartRestartOutput, error) {
	output := &SmartRestartOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		DryRun:        opts.DryRun,
		Force:         opts.Force,
		Actions:       make(map[string]RestartAction),
		Summary: RestartSummary{
			PanesByAction: make(map[string][]string),
		},
	}
	if ctx == nil {
		output.RobotResponse = NewErrorResponse(errors.New("smart-restart context is required"), ErrCodeInternalError, "Retry the command")
		return output, nil
	}
	if err := ctx.Err(); err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeTimeout, "Retry after the cancellation or timeout condition clears")
		return output, nil
	}

	// Step 1: Pre-check all panes using IsWorking
	isWorkingOpts := IsWorkingOptions{
		Session:       opts.Session,
		PaneSelectors: opts.PaneSelectors,
		Panes:         opts.Panes,
		LinesCaptured: opts.LinesCaptured,
		Verbose:       opts.Verbose,
	}

	isWorkingResult, err := GetIsWorking(ctx, isWorkingOpts)
	if err != nil {
		return output, err
	}
	if !isWorkingResult.Success {
		output.Success = false
		output.Error = isWorkingResult.Error
		output.ErrorCode = isWorkingResult.ErrorCode
		output.Hint = isWorkingResult.Hint
		return output, nil
	}
	if err := validateSmartRestartTargets(isWorkingResult.Panes); err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeNotImplemented, agent.GrokPhaseOneCapabilityHint)
		return output, nil
	}

	// Step 2: Process each pane in canonical order. Deterministic actuation is a
	// safety property: after cancellation, every later target must be known and
	// reported as not attempted.
	paneKeys := sortedSmartRestartPaneKeys(isWorkingResult.Panes)
	for paneIndex, paneStr := range paneKeys {
		workStatus := isWorkingResult.Panes[paneStr]
		if err := ctx.Err(); err != nil {
			materializeSmartRestartCancellationActions(output, isWorkingResult.Panes, paneKeys[paneIndex:], err)
			setSmartRestartCancellation(output, err)
			return output, nil
		}
		// The is-working map key is "window.pane" on multi-window sessions and a
		// bare pane index on single-window sessions (#172). Parse both so the
		// restart targets the real window instead of defaulting to window 0/1
		// (the old `strconv.Atoi("1.2")` -> 0 mis-fire that ctrl-c'd a
		// nonexistent pane while the envelope claimed success).
		restartWin, paneNum := parseRestartPaneKey(paneStr)

		action := RestartAction{PreCheck: smartRestartPreCheck(workStatus)}

		// Determine action based on pre-check
		shouldRestart, reason, warning := decideRestart(&workStatus, opts.Force)

		switch {
		case workStatus.IsRateLimited && !opts.Force:
			action.Action = ActionWaiting
			action.Reason = "Rate limited - wait for reset"
			action.WaitInfo = buildWaitInfo(&workStatus)
			markSmartRestartPromptNotAttempted(&action, opts)
			output.Summary.Waiting++
			appendPaneToAction(output.Summary.PanesByAction, "WAITING", paneStr)

		case !shouldRestart:
			action.Action = ActionSkipped
			action.Reason = reason
			markSmartRestartPromptNotAttempted(&action, opts)
			output.Summary.Skipped++
			appendPaneToAction(output.Summary.PanesByAction, "SKIPPED", paneStr)

		case opts.DryRun:
			action.Action = ActionWouldRestart
			action.Reason = reason
			if warning != "" {
				action.Warning = warning
			}
			output.Summary.WouldRestart++
			appendPaneToAction(output.Summary.PanesByAction, "WOULD_RESTART", paneStr)

		default:
			// Actually perform restart
			if warning != "" {
				action.Warning = warning
			}
			restartResult, restartErr := executeRestart(ctx, opts.Session, restartWin, paneNum, workStatus.AgentType, opts)
			verificationErr := applyRestartExecutionOutcome(
				output,
				&action,
				paneStr,
				reason,
				restartResult,
				restartErr,
				func() (*PostStateInfo, error) { return verifyRestart(ctx, opts.Session, restartWin, paneNum, opts) },
			)
			if cancelErr := restartPaneCancellationError(ctx, errors.Join(restartErr, verificationErr)); cancelErr != nil {
				output.Actions[paneStr] = action
				materializeSmartRestartCancellationActions(output, isWorkingResult.Panes, paneKeys[paneIndex+1:], cancelErr)
				setSmartRestartCancellation(output, cancelErr)
				return output, nil
			}
		}

		output.Actions[paneStr] = action
	}
	if err := ctx.Err(); err != nil {
		setSmartRestartCancellation(output, err)
		return output, nil
	}

	// Fail loud (#172) instead of silently reporting success:true when the
	// restart accomplished nothing useful. Two dangerous classes are caught:
	//
	//   1. No restartable target resolved. On multi-window / window-per-agent
	//      layouts a window-local `--panes=N` filter can match nothing (or match
	//      a pane address that does not exist), leaving the action set empty or
	//      populated only with not-found error panes. Previously the top-level
	//      response still said success:true.
	//   2. One or more individual restart actions FAILED (e.g. executeRestart
	//      could not find the pane, or the agent never relaunched). Previously
	//      these were recorded under the action but the envelope stayed
	//      success:true.
	//
	// In dry-run mode no restart is attempted, so a non-empty would-restart set
	// is a successful preview and must keep success:true.
	finalizeSmartRestartOutput(output, opts)

	return output, nil
}

func validateSmartRestartTargets(panes map[string]PaneWorkStatus) error {
	for _, paneKey := range sortedSmartRestartPaneKeys(panes) {
		status := panes[paneKey]
		if err := agent.AgentType(status.AgentType).ValidateAutomatedRelaunch(); err != nil {
			return fmt.Errorf("pane %s (%s): %w", paneKey, status.AgentType, err)
		}
	}
	return nil
}

func sortedSmartRestartPaneKeys(panes map[string]PaneWorkStatus) []string {
	keys := make([]string, 0, len(panes))
	for paneKey := range panes {
		keys = append(keys, paneKey)
	}
	sort.Strings(keys)
	return keys
}

func smartRestartPreCheck(workStatus PaneWorkStatus) *PreCheckInfo {
	return &PreCheckInfo{
		Recommendation:   workStatus.Recommendation,
		IsWorking:        workStatus.IsWorking,
		IsIdle:           workStatus.IsIdle,
		IsRateLimited:    workStatus.IsRateLimited,
		IsContextLow:     workStatus.IsContextLow,
		ContextRemaining: workStatus.ContextRemaining,
		Confidence:       workStatus.Confidence,
		AgentType:        workStatus.AgentType,
	}
}

// markSmartRestartPromptNotAttempted makes declines contract-explicit
// (bd-moje8): when the caller requested a post-restart prompt but the pane's
// restart was declined, the response must state that the prompt was NOT
// delivered — smart-restart never sends the prompt to an incumbent agent on
// decline, and orchestrators have misread a bare SKIPPED as a rotation.
func markSmartRestartPromptNotAttempted(action *RestartAction, opts SmartRestartOptions) {
	if action == nil || opts.Prompt == "" {
		return
	}
	action.PromptOutcome = &PromptDeliveryOutcome{
		Requested: true,
		Status:    PromptDeliveryNotAttempted,
	}
	action.Reason += "; requested prompt was NOT delivered (declined panes keep their incumbent agent and receive no message)"
}

func materializeSmartRestartCancellationActions(
	output *SmartRestartOutput,
	panes map[string]PaneWorkStatus,
	paneKeys []string,
	cancelErr error,
) {
	if output == nil || cancelErr == nil {
		return
	}
	for _, paneKey := range paneKeys {
		if _, exists := output.Actions[paneKey]; exists {
			continue
		}
		output.Actions[paneKey] = RestartAction{
			Action:   ActionSkipped,
			Reason:   fmt.Sprintf("Restart not attempted because smart-restart was canceled before this pane: %v", cancelErr),
			PreCheck: smartRestartPreCheck(panes[paneKey]),
		}
		output.Summary.Skipped++
		appendPaneToAction(output.Summary.PanesByAction, "SKIPPED", paneKey)
	}
}

func setSmartRestartCancellation(output *SmartRestartOutput, cancelErr error) {
	if output == nil || cancelErr == nil {
		return
	}
	output.RobotResponse = NewErrorResponse(cancelErr, ErrCodeTimeout, "Inspect completed pane actions before retrying")
}

func applyRestartExecutionOutcome(
	output *SmartRestartOutput,
	action *RestartAction,
	paneKey string,
	reason string,
	restartResult *RestartSequence,
	restartErr error,
	verify func() (*PostStateInfo, error),
) error {
	if restartResult != nil {
		action.RestartSequence = restartResult
	}
	if restartErr != nil {
		var structErr *StructuredError
		isStructured := errors.As(restartErr, &structErr)
		if isStructured && structErr.Code == ErrCodePromptSendFailed && restartResult != nil && restartResult.AgentLaunched {
			// The restart itself succeeded. Keep that fact honest while
			// surfacing the requested handoff failure separately.
			action.Action = ActionRestarted
			action.Reason = reason
			if verify != nil {
				postState, verifyErr := verify()
				action.PostState = postState
				if verifyErr != nil {
					action.Error = fmt.Sprintf("post-restart verification canceled: %v", verifyErr)
				}
				action.PromptError = structErr
				output.Summary.Restarted++
				output.Summary.PromptFailed++
				output.Summary.PanesWithPromptFailure = append(output.Summary.PanesWithPromptFailure, paneKey)
				appendPaneToAction(output.Summary.PanesByAction, "RESTARTED", paneKey)
				return verifyErr
			}
			action.PromptError = structErr
			output.Summary.Restarted++
			output.Summary.PromptFailed++
			output.Summary.PanesWithPromptFailure = append(output.Summary.PanesWithPromptFailure, paneKey)
			appendPaneToAction(output.Summary.PanesByAction, "RESTARTED", paneKey)
			return nil
		}

		action.Action = ActionFailed
		// Report what actually failed (bd-moje8): the pre-check verdict (e.g.
		// "Agent is idle") explains why a restart was ATTEMPTED, not why it
		// failed. Reusing it as the failure reason blamed the agent's idleness
		// for a restart sequence that never reached a shell.
		action.Reason = smartRestartFailureReason(restartResult, restartErr, structErr)
		action.Error = restartErr.Error()
		if isStructured {
			action.StructuredError = structErr
		}
		output.Summary.Failed++
		appendPaneToAction(output.Summary.PanesByAction, "FAILED", paneKey)
		return nil
	}

	action.Action = ActionRestarted
	action.Reason = reason
	var verifyErr error
	if verify != nil {
		action.PostState, verifyErr = verify()
		if verifyErr != nil {
			action.Error = fmt.Sprintf("post-restart verification canceled: %v", verifyErr)
		}
	}
	output.Summary.Restarted++
	if restartResult != nil && restartResult.PromptOutcome != nil && restartResult.PromptOutcome.Status == PromptDeliveryDelivered {
		output.Summary.PromptDelivered++
	}
	appendPaneToAction(output.Summary.PanesByAction, "RESTARTED", paneKey)
	return verifyErr
}

// smartRestartFailureReason derives a FAILED action's reason from the restart
// failure itself rather than the pre-check classification (bd-moje8). The
// shell-unconfirmed case gets an explicit exit-sequence reason so callers see
// "the exit never reached a shell", not "Agent is idle".
func smartRestartFailureReason(seq *RestartSequence, restartErr error, structErr *StructuredError) string {
	if structErr != nil {
		if structErr.Code == ErrCodeShellNotReturned {
			exitMethod := "unknown"
			if seq != nil && strings.TrimSpace(seq.ExitMethod) != "" {
				exitMethod = seq.ExitMethod
			}
			return fmt.Sprintf(
				"restart exit sequence unconfirmed: agent did not return to shell within %s (exit_method=%s)",
				shellReturnTimeout,
				exitMethod,
			)
		}
		if strings.TrimSpace(structErr.Phase) != "" {
			return fmt.Sprintf("restart failed during %s: %s", structErr.Phase, structErr.Message)
		}
		return "restart failed: " + structErr.Message
	}
	if restartErr != nil {
		return "restart failed: " + restartErr.Error()
	}
	return "restart failed"
}

// smartRestartFailureCode derives the top-level error code for a failed run.
// When every FAILED pane carries the same structured code, that code is
// authoritative (e.g. SHELL_NOT_RETURNED); mixed or missing codes fall back to
// INTERNAL_ERROR.
func smartRestartFailureCode(output *SmartRestartOutput) string {
	code := ""
	for _, action := range output.Actions {
		if action.Action != ActionFailed {
			continue
		}
		actionCode := ""
		if action.StructuredError != nil {
			actionCode = strings.TrimSpace(action.StructuredError.Code)
		}
		if actionCode == "" {
			return ErrCodeInternalError
		}
		if code == "" {
			code = actionCode
			continue
		}
		if code != actionCode {
			return ErrCodeInternalError
		}
	}
	if code == "" {
		return ErrCodeInternalError
	}
	return code
}

func finalizeSmartRestartOutput(output *SmartRestartOutput, opts SmartRestartOptions) {
	if output == nil || opts.DryRun {
		return
	}
	if output.ErrorCode == ErrCodeTimeout {
		output.Success = false
		return
	}

	restartableTargets := output.Summary.Restarted + output.Summary.Failed +
		output.Summary.Skipped + output.Summary.Waiting
	switch {
	case output.Summary.Failed > 0:
		output.Success = false
		output.ErrorCode = smartRestartFailureCode(output)
		output.Error = fmt.Sprintf("%d of %d targeted pane(s) failed to restart", output.Summary.Failed, len(output.Actions))
		output.Hint = smartRestartTargetingHint(opts, output)
	case output.Summary.PromptFailed > 0:
		output.Success = false
		output.ErrorCode = ErrCodePromptSendFailed
		output.Error = fmt.Sprintf(
			"%d restarted pane(s) did not receive the requested prompt",
			output.Summary.PromptFailed,
		)
		output.Hint = "The restart completed, but prompt delivery was not confirmed. Inspect each prompt_outcome and prompt_error; inspect ambiguous panes before retrying to avoid duplicate work."
	case restartableTargets == 0:
		output.Success = false
		output.ErrorCode = ErrCodePaneNotFound
		output.Error = "no restartable panes matched the request"
		output.Hint = smartRestartTargetingHint(opts, output)
	default:
		// Documented escalation for unknown-state skips (bd-moje8): the run
		// succeeded but some panes could not be classified, so tell the caller
		// how to escalate instead of leaving a bare SKIPPED.
		if output.Hint == "" && smartRestartHasUnknownStateSkip(output) {
			output.Hint = "One or more panes were skipped because their state probe was inconclusive. Re-run with --force to restart despite unknown state, or use --robot-restart-pane for an explicit restart of specific panes."
		}
	}
}

// smartRestartHasUnknownStateSkip reports whether any pane was skipped because
// its work-state probe was inconclusive.
func smartRestartHasUnknownStateSkip(output *SmartRestartOutput) bool {
	for _, action := range output.Actions {
		if action.Action == ActionSkipped && strings.HasPrefix(action.Reason, "Unknown state") {
			return true
		}
	}
	return false
}

// smartRestartTargetingHint builds an actionable remediation hint for the
// fail-loud paths. On multi-window layouts a bare pane index is window-local and
// may need a `window.pane` address; we surface the panes that were actually
// found so the caller can re-target precisely.
func smartRestartTargetingHint(opts SmartRestartOptions, output *SmartRestartOutput) string {
	found := make([]string, 0, len(output.Actions))
	for paneKey := range output.Actions {
		found = append(found, paneKey)
	}
	sort.Strings(found)

	var b strings.Builder
	if len(opts.PaneSelectors) > 0 || len(opts.Panes) > 0 {
		b.WriteString("On multi-window / window-per-agent layouts a bare --panes index is window-local; ")
		b.WriteString("the pane may need a window.pane address. ")
	}
	if len(found) > 0 {
		b.WriteString("Panes evaluated: ")
		b.WriteString(strings.Join(found, ", "))
		b.WriteString(". ")
	} else {
		b.WriteString("No panes were evaluated for this session. ")
	}
	b.WriteString("Run 'ntm --robot-is-working=")
	b.WriteString(opts.Session)
	b.WriteString("' to see live pane addresses and states.")
	return b.String()
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

	case "MANUAL_INTERVENTION":
		if force {
			return true, "FORCED restart of gated pane", "The interactive gate will reappear after relaunch"
		}
		return false, "Blocked on an interactive gate screen - a restart cannot answer it; a human keystroke can", ""

	default:
		if force {
			return true, "FORCED restart of unknown state", "Unknown state - results unpredictable"
		}
		// Include WHICH probe was inconclusive (bd-moje8): a bare "Unknown
		// state" skip gave orchestrators nothing to inspect and no escalation
		// path.
		return false, fmt.Sprintf(
			"Unknown state - manual inspection needed (probe inconclusive: recommendation=%q, observation_state=%q, freshness=%q, confidence=%.2f); re-run with --force to restart despite unknown state, or use --robot-restart-pane for an explicit restart",
			rec,
			status.ObservationState,
			status.ObservationFreshness,
			status.Confidence,
		), ""
	}
}

// formatRestartReason formats a reason string with a value.
func formatRestartReason(format string, value float64) string {
	// Simple percentage formatting without fmt
	return formatReasonWithPercent(format, value)
}

// formatReasonWithPercent inserts a rounded percentage into a format string.
func formatReasonWithPercent(format string, pct float64) string {
	if !strings.Contains(format, "%") {
		return format
	}
	return fmt.Sprintf(format, pct)
}

// buildWaitInfo constructs wait information for rate-limited agents.
func buildWaitInfo(status *PaneWorkStatus) *WaitInfo {
	info := &WaitInfo{
		Suggestion: "Consider caam account switch",
	}

	// If we have rate limit info from indicators, use it
	// For now, provide generic guidance // placebo-waiver: bd-d7z7i
	info.WaitSeconds = 3600 // Default 1 hour estimate

	return info
}

// parseRestartPaneKey decodes the is-working response-map key into a
// (window, pane) pair. On multi-window sessions the key is "window.pane"
// (#172); on single-window sessions it is a bare pane index. For the bare-index
// case the window is reported as -1 ("unknown") so executeRestart/verifyRestart
// fall back to the session's first window — preserving single-window behavior
// while fixing the multi-window mis-fire where strconv.Atoi("1.2") yielded 0
// and ctrl-c'd a nonexistent pane.
func parseRestartPaneKey(key string) (win, pane int) {
	if w, p, ok := strings.Cut(key, "."); ok {
		wi, errW := strconv.Atoi(strings.TrimSpace(w))
		pi, errP := strconv.Atoi(strings.TrimSpace(p))
		if errW == nil && errP == nil {
			return wi, pi
		}
	}
	p, _ := strconv.Atoi(strings.TrimSpace(key))
	return -1, p
}

// appendPaneToAction adds a canonical pane address to the action's pane list.
func appendPaneToAction(panesByAction map[string][]string, action string, pane string) {
	if panesByAction[action] == nil {
		panesByAction[action] = []string{}
	}
	panesByAction[action] = append(panesByAction[action], pane)
}

// newRestartError creates a StructuredError for restart failures with context.
func newRestartError(code, message, phase string, pane int, agentType string, attemptedActions []string, lastOutput string) *StructuredError {
	details := NewErrorDetails().
		WithAgentType(agentType).
		WithAttemptedActions(attemptedActions...)

	if lastOutput != "" {
		details.WithLastOutput(lastOutput, 500)
	}

	return NewStructuredError(code, message).
		WithPhase(phase).
		WithPane(pane).
		WithDetails(details)
}

// executeRestart performs the actual restart sequence for a pane.
func executeRestart(ctx context.Context, session string, win, pane int, agentType string, opts SmartRestartOptions) (*RestartSequence, error) {
	seq := &RestartSequence{
		AgentType:         agentType,
		AgentLaunchStatus: SmartRestartLaunchNotReady,
	}
	if ctx == nil {
		return seq, errors.New("smart-restart execution context is required")
	}
	if err := ctx.Err(); err != nil {
		return seq, err
	}
	if err := agent.AgentType(agentType).ValidateAutomatedRelaunch(); err != nil {
		return seq, newRestartError(
			ErrCodeNotImplemented,
			err.Error(),
			"preflight",
			pane,
			agentType,
			nil,
			"",
		).WithRecoveryHint(agent.GrokPhaseOneCapabilityHint)
	}
	executor := opts.executor
	if executor == nil {
		executor = liveSmartRestartExecutor{}
	}
	var attemptedActions []string
	var softExitFailed bool

	// win < 0 means "single-window session, window unknown" — fall back to the
	// session's first window. On multi-window sessions win is the pane's real
	// window index (#172), so the target addresses the correct window instead
	// of always window 1.
	if win < 0 {
		fw, err := tmux.GetFirstWindowContext(ctx, session)
		if err != nil {
			if cancelErr := restartPaneCancellationError(ctx, err); cancelErr != nil {
				return seq, cancelErr
			}
			fw = 1 // fallback
		}
		win = fw
	}
	target := fmt.Sprintf("%s:%d.%d", session, win, pane)

	// Step 1: Exit the current agent using agent-specific method (unless HardKillOnly)
	if !opts.HardKillOnly {
		attemptedActions = append(attemptedActions, "exit-agent-"+agentType)
		exitErr := executor.exitAgent(ctx, session, win, pane, agentType, seq)
		if exitErr != nil {
			if cancelErr := restartPaneCancellationError(ctx, exitErr); cancelErr != nil {
				return seq, cancelErr
			}
			if opts.HardKill {
				// Soft exit failed, but we're allowed to try hard kill
				softExitFailed = true
			} else {
				structErr := newRestartError(
					ErrCodeSoftExitFailed,
					"Agent did not respond to exit within timeout: "+exitErr.Error(),
					"soft_exit",
					pane,
					agentType,
					attemptedActions,
					"",
				).WithRecoveryHint(fmt.Sprintf("Try ntm --robot-smart-restart=%s --panes=%d --hard-kill to use kill -9 fallback", session, pane))
				return seq, structErr
			}
		}
		if err := ctx.Err(); err != nil {
			return seq, err
		}

		// Steps 2+3: Poll for the shell to return after the soft exit.
		// Confirmation is primarily by PROCESS DEATH (no agent child under
		// the pane shell): a fixed sleep plus prompt-glyph scraping produced
		// false SHELL_NOT_RETURNED during multi-second agent teardown and
		// false RESTARTED when a live agent's own "❯" input line satisfied
		// the glyph heuristic (#187).
		attemptedActions = append(attemptedActions, "wait-shell-return")
		waitStart := time.Now()
		shellReturned, lastOutput, waitErr := executor.waitForShellReturn(ctx, session, win, pane, shellReturnTimeout)
		seq.ExitDurationMs = int(time.Since(waitStart).Milliseconds())
		seq.ShellConfirmed = shellReturned
		if waitErr != nil {
			if cancelErr := restartPaneCancellationError(ctx, waitErr); cancelErr != nil {
				return seq, cancelErr
			}
			return seq, waitErr
		}
		if !shellReturned {
			if !opts.HardKill {
				structErr := newRestartError(
					ErrCodeShellNotReturned,
					fmt.Sprintf("Shell did not return within %s after exit - agent may still be running", shellReturnTimeout),
					"post_exit",
					pane,
					agentType,
					attemptedActions,
					lastOutput,
				).WithRecoveryHint(fmt.Sprintf("Try ntm --robot-smart-restart=%s --panes=%d --hard-kill, or manually kill the process", session, pane))
				return seq, structErr
			}
			softExitFailed = true
		}
	}

	// Step 3b: Hard kill fallback if soft exit failed or HardKillOnly (bd-bh74z)
	if opts.HardKillOnly || (opts.HardKill && softExitFailed) {
		attemptedActions = append(attemptedActions, "hard-kill")
		hardKillResult, err := executor.hardKillAgent(ctx, session, win, pane, seq)
		seq.HardKillUsed = true
		seq.HardKillResult = hardKillResult

		if err != nil {
			if cancelErr := restartPaneCancellationError(ctx, err); cancelErr != nil {
				return seq, cancelErr
			}
			structErr := newRestartError(
				ErrCodeHardKillFailed,
				"Hard kill (kill -9) failed: "+err.Error(),
				"hard_kill",
				pane,
				agentType,
				attemptedActions,
				"",
			)
			if hardKillResult != nil && hardKillResult.ShellPID > 0 {
				structErr.Details.WithChildPID(hardKillResult.ChildPID)
				structErr.Details.SetExtra("shell_pid", hardKillResult.ShellPID)
			}
			structErr.WithRecoveryHint("Manual intervention required - check process state with ps aux | grep <pid>")
			return seq, structErr
		}

		// Poll for the shell to return after hard kill — same process-death
		// confirmation as the soft-exit path (#187).
		attemptedActions = append(attemptedActions, "wait-shell-return-after-kill")
		shellReturned, lastOutput, waitErr := executor.waitForShellReturn(ctx, session, win, pane, shellReturnTimeout)
		seq.ShellConfirmed = shellReturned
		if waitErr != nil {
			if cancelErr := restartPaneCancellationError(ctx, waitErr); cancelErr != nil {
				return seq, cancelErr
			}
			return seq, waitErr
		}
		if !shellReturned {
			structErr := newRestartError(
				ErrCodeShellNotReturned,
				fmt.Sprintf("Shell did not return within %s after hard kill", shellReturnTimeout),
				"post_hard_kill",
				pane,
				agentType,
				attemptedActions,
				lastOutput,
			).WithRecoveryHint("Shell may be in unexpected state - try manually running 'reset' in the pane")
			return seq, structErr
		}
	}

	// Step 4: Launch new agent using the canonical agent alias/command name.
	alias := restartLaunchAlias(agentType)
	if err := ctx.Err(); err != nil {
		return seq, err
	}

	attemptedActions = append(attemptedActions, "launch-"+alias)
	seq.LaunchAttempted = true
	seq.AgentLaunchStatus = SmartRestartLaunchUnknown
	launchErr := executor.sendKeys(ctx, session, win, pane, alias+"\n")
	if launchErr != nil {
		if cancelErr := restartPaneCancellationError(ctx, launchErr); cancelErr != nil {
			observeSmartRestartLaunchAfterCancellation(ctx, session, win, pane, agentType, target, executor, seq)
			return seq, cancelErr
		}
		observeSmartRestartLaunchAfterCancellation(ctx, session, win, pane, agentType, target, executor, seq)
		structErr := newRestartError(
			ErrCodeCCLaunchFailed,
			"Failed to launch agent: "+launchErr.Error(),
			"launch",
			pane,
			agentType,
			attemptedActions,
			"", // No pane output available at launch step
		).WithRecoveryHint("Verify the agent CLI is installed and in PATH")
		return seq, structErr
	}
	if err := ctx.Err(); err != nil {
		observeSmartRestartLaunchAfterCancellation(ctx, session, win, pane, agentType, target, executor, seq)
		return seq, err
	}

	// Step 5: Ready-gate — poll until the agent TUI is actually up instead of
	// sleeping a fixed interval, so the prompt is never typed into a bare
	// shell when the agent boots slowly (#187).
	readyTimeout := opts.PostWaitTime
	if readyTimeout < smartRestartMinReadyTimeout {
		readyTimeout = smartRestartMinReadyTimeout
	}
	attemptedActions = append(attemptedActions, "wait-agent-ready")
	shellPID, pidErr := executor.getShellPID(ctx, session, win, pane)
	if pidErr != nil {
		if cancelErr := restartPaneCancellationError(ctx, pidErr); cancelErr != nil {
			observeSmartRestartLaunchAfterCancellation(ctx, session, win, pane, agentType, target, executor, seq)
			return seq, cancelErr
		}
		shellPID = 0 // ready-gate falls back to content-only detection
	} else if shellPID > 0 {
		seq.ShellPID = shellPID
		processAlive := executor.hasChildAlive(shellPID)
		seq.ProcessAlive = &processAlive
	}
	agentReady, readyErr := executor.waitForPaneAgentReady(ctx, target, shellPID, agentType, readyTimeout)
	if readyErr != nil {
		if cancelErr := restartPaneCancellationError(ctx, readyErr); cancelErr != nil {
			observeSmartRestartLaunchAfterCancellation(ctx, session, win, pane, agentType, target, executor, seq)
			return seq, cancelErr
		}
		updateSmartRestartLaunchObservation(seq, executor, shellPID, false)
		return seq, readyErr
	}
	if cancelErr := restartPaneCancellationError(ctx, nil); cancelErr != nil {
		observeSmartRestartLaunchAfterCancellation(ctx, session, win, pane, agentType, target, executor, seq)
		return seq, cancelErr
	}
	if !agentReady {
		updateSmartRestartLaunchObservation(seq, executor, shellPID, false)
		lastOutput, captureErr := executor.capturePaneOutput(ctx, target, 10)
		if cancelErr := restartPaneCancellationError(ctx, captureErr); cancelErr != nil {
			observeSmartRestartLaunchAfterCancellation(ctx, session, win, pane, agentType, target, executor, seq)
			return seq, cancelErr
		}
		structErr := newRestartError(
			ErrCodeCCLaunchFailed,
			fmt.Sprintf("Agent did not become ready within %s after launch", readyTimeout),
			"launch",
			pane,
			agentType,
			attemptedActions,
			lastOutput,
		).WithRecoveryHint("Verify the agent CLI is installed and in PATH, then check the pane with ntm status")
		return seq, structErr
	}

	// Step 6: Send the optional prompt through the shared dispatch application
	// service. PromptSent is only true after the service returns one delivered
	// receipt for this exact physical pane.
	seq.AgentLaunched = true
	seq.AgentLaunchStatus = SmartRestartLaunchReady
	if shellPID > 0 {
		seq.ShellPID = shellPID
		processAlive := executor.hasChildAlive(shellPID)
		seq.ProcessAlive = &processAlive
	}
	if opts.Prompt != "" {
		attemptedActions = append(attemptedActions, "send-prompt")
		promptOutcome, promptErr := dispatchSmartRestartPrompt(
			ctx,
			session,
			win,
			pane,
			agentType,
			agentReady,
			opts,
		)
		seq.PromptOutcome = &promptOutcome
		seq.PromptSent = promptOutcome.Status == PromptDeliveryDelivered
		if promptErr != nil {
			if cancelErr := restartPaneCancellationError(ctx, promptErr); cancelErr != nil {
				return seq, cancelErr
			}
			return seq, newPromptDeliveryError(pane, agentType, attemptedActions, promptOutcome, promptErr)
		}
	}

	_ = attemptedActions // Used for error reporting in failure paths
	return seq, nil
}

func observeSmartRestartLaunchAfterCancellation(
	ctx context.Context,
	session string,
	win int,
	pane int,
	agentType string,
	target string,
	executor smartRestartExecutor,
	seq *RestartSequence,
) {
	if ctx == nil || executor == nil || seq == nil || !seq.LaunchAttempted {
		return
	}
	observationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), smartRestartMutationObservationTimeout)
	defer cancel()

	shellPID, pidErr := executor.getShellPID(observationCtx, session, win, pane)
	if pidErr == nil && shellPID > 0 {
		seq.ShellPID = shellPID
	}
	ready, readyErr := executor.waitForPaneAgentReady(
		observationCtx,
		target,
		shellPID,
		agentType,
		smartRestartMutationObservationTimeout,
	)
	updateSmartRestartLaunchObservation(seq, executor, shellPID, ready)

	var observationErrors []error
	if pidErr != nil {
		observationErrors = append(observationErrors, fmt.Errorf("observe shell PID: %w", pidErr))
	} else if shellPID <= 0 {
		observationErrors = append(observationErrors, errors.New("observe shell PID: pane PID is unavailable"))
	}
	if readyErr != nil {
		observationErrors = append(observationErrors, fmt.Errorf("observe readiness: %w", readyErr))
	}
	if observationErr := errors.Join(observationErrors...); observationErr != nil {
		seq.LaunchObservationError = observationErr.Error()
	}
}

func updateSmartRestartLaunchObservation(
	seq *RestartSequence,
	executor smartRestartExecutor,
	shellPID int,
	ready bool,
) {
	if seq == nil || executor == nil {
		return
	}
	seq.AgentLaunched = ready
	if ready {
		seq.AgentLaunchStatus = SmartRestartLaunchReady
	} else {
		seq.AgentLaunchStatus = SmartRestartLaunchUnknown
	}
	if shellPID <= 0 {
		return
	}
	seq.ShellPID = shellPID
	processAlive := executor.hasChildAlive(shellPID)
	seq.ProcessAlive = &processAlive
	if !ready && !processAlive {
		seq.AgentLaunchStatus = SmartRestartLaunchNotReady
	}
}

// dispatchSmartRestartPrompt performs the post-launch prompt handoff through
// the canonical dispatch service. ready is explicit so the safety gate remains
// independently testable: a dry run or failed ready gate must never actuate.
func dispatchSmartRestartPrompt(
	ctx context.Context,
	sessionName string,
	windowIndex int,
	paneIndex int,
	agentType string,
	ready bool,
	opts SmartRestartOptions,
) (PromptDeliveryOutcome, error) {
	outcome := PromptDeliveryOutcome{
		Requested: opts.Prompt != "",
		Target:    tmux.PaneRef{WindowIndex: windowIndex, PaneIndex: paneIndex}.Physical(),
		Status:    PromptDeliveryNotAttempted,
	}
	if !ready || opts.DryRun || opts.Prompt == "" {
		return outcome, nil
	}

	dispatcher := opts.promptDispatch
	if dispatcher == nil {
		redactionConfig := redaction.DefaultConfig()
		if configured := session.GetRedactionConfig(); configured != nil {
			redactionConfig = *configured
		}
		service, _, err := newRobotDispatchService(redactionConfig, nil, nil)
		if err != nil {
			outcome.Status = PromptDeliveryFailed
			outcome.Error = fmt.Sprintf("initialize prompt dispatch: %v", err)
			return outcome, fmt.Errorf("initialize prompt dispatch: %w", err)
		}
		dispatcher = service
	}

	pane := tmux.Pane{
		Index:       paneIndex,
		WindowIndex: windowIndex,
		Type:        restartCanonicalAgentType(agentType),
	}
	physicalTarget := pane.Ref().Physical()
	result, err := dispatcher.Execute(ctx, dispatchsvc.Request{
		Session:               sessionName,
		Panes:                 []tmux.Pane{pane},
		Selectors:             []string{physicalTarget},
		RequireSingleSelector: true,
		IncludeUser:           true,
		Message:               opts.Prompt,
		Submit:                true,
		StopOnFailure:         true,
	})
	outcome = promptDeliveryOutcomeFromResult(pane.Ref(), result)
	if err != nil {
		outcome.DispatchCode = promptDispatchErrorCode(err)
		if result.Delivered > 0 || hasDeliveredReceipt(result.Receipts) {
			outcome.Status = PromptDeliveryAmbiguous
			outcome.Error = fmt.Sprintf("prompt delivery outcome is ambiguous after a delivered receipt: %v", err)
			return outcome, fmt.Errorf("prompt delivery outcome is ambiguous after a delivered receipt: %w", err)
		}
		if result.Blocked > 0 || hasReceiptStatus(result.Receipts, dispatchsvc.ReceiptBlocked) {
			outcome.Status = PromptDeliveryBlocked
		} else {
			outcome.Status = PromptDeliveryFailed
		}
		outcome.Error = fmt.Sprintf("prompt dispatch failed: %v", err)
		return outcome, fmt.Errorf("prompt dispatch failed: %w", err)
	}
	if err := verifySmartRestartPromptReceipt(result, pane.Ref()); err != nil {
		if result.Blocked > 0 || hasReceiptStatus(result.Receipts, dispatchsvc.ReceiptBlocked) {
			outcome.Status = PromptDeliveryBlocked
		} else if result.Failed > 0 || hasReceiptStatus(result.Receipts, dispatchsvc.ReceiptFailed) {
			outcome.Status = PromptDeliveryFailed
		} else {
			outcome.Status = PromptDeliveryAmbiguous
		}
		outcome.Error = err.Error()
		return outcome, err
	}
	outcome.Status = PromptDeliveryDelivered
	return outcome, nil
}

func promptDeliveryOutcomeFromResult(expected tmux.PaneRef, result dispatchsvc.Result) PromptDeliveryOutcome {
	outcome := PromptDeliveryOutcome{
		Requested: true,
		Target:    expected.Physical(),
		Status:    PromptDeliveryFailed,
		Delivered: result.Delivered,
		Failed:    result.Failed,
		Blocked:   result.Blocked,
		Skipped:   result.Skipped,
	}
	if len(result.Receipts) == 1 {
		outcome.ReceiptStatus = string(result.Receipts[0].Status)
	}
	return outcome
}

func promptDispatchErrorCode(err error) string {
	var dispatchErr *dispatchsvc.Error
	if errors.As(err, &dispatchErr) {
		return string(dispatchErr.Code)
	}
	return ""
}

func hasReceiptStatus(receipts []dispatchsvc.Receipt, status dispatchsvc.ReceiptStatus) bool {
	for _, receipt := range receipts {
		if receipt.Status == status {
			return true
		}
	}
	return false
}

func newPromptDeliveryError(pane int, agentType string, attemptedActions []string, outcome PromptDeliveryOutcome, cause error) *StructuredError {
	details := NewErrorDetails().
		WithAgentType(agentType).
		WithAttemptedActions(attemptedActions...)
	details.SetExtra("prompt_status", outcome.Status)
	details.SetExtra("target", outcome.Target)
	details.SetExtra("delivered", outcome.Delivered)
	details.SetExtra("failed", outcome.Failed)
	details.SetExtra("blocked", outcome.Blocked)
	details.SetExtra("skipped", outcome.Skipped)
	if outcome.ReceiptStatus != "" {
		details.SetExtra("receipt_status", outcome.ReceiptStatus)
	}
	if outcome.DispatchCode != "" {
		details.SetExtra("dispatch_code", outcome.DispatchCode)
	}

	message := "Agent restarted, but requested prompt delivery was not confirmed"
	if cause != nil {
		message += ": " + cause.Error()
	}
	structured := NewStructuredError(ErrCodePromptSendFailed, message).
		WithPhase("prompt").
		WithPane(pane).
		WithDetails(details)
	switch outcome.Status {
	case PromptDeliveryAmbiguous:
		structured.WithRecoveryHint("Inspect the target pane before retrying because the prompt may already have been delivered")
	case PromptDeliveryBlocked:
		structured.WithRecoveryHint("Revise the prompt or redaction policy, then retry delivery to the exact pane")
	default:
		structured.WithRecoveryHint("Retry the prompt with --robot-send using the exact window.pane target")
	}
	return structured
}

func hasDeliveredReceipt(receipts []dispatchsvc.Receipt) bool {
	for _, receipt := range receipts {
		if receipt.Status == dispatchsvc.ReceiptDelivered {
			return true
		}
	}
	return false
}

func verifySmartRestartPromptReceipt(result dispatchsvc.Result, expected tmux.PaneRef) error {
	if len(result.Targets) != 1 {
		return fmt.Errorf("prompt delivery target plan is ambiguous: got %d targets for pane %s", len(result.Targets), expected.Physical())
	}
	if result.Targets[0].Ref.Physical() != expected.Physical() || result.Targets[0].Ref.StableKey() != expected.StableKey() {
		return fmt.Errorf("prompt delivery target plan selected pane %s, want %s", result.Targets[0].Ref.Physical(), expected.Physical())
	}
	if len(result.Receipts) != 1 {
		return fmt.Errorf("prompt delivery receipt is ambiguous: got %d receipts for pane %s", len(result.Receipts), expected.Physical())
	}
	receipt := result.Receipts[0]
	if receipt.Target.Ref.Physical() != expected.Physical() || receipt.Target.Ref.StableKey() != expected.StableKey() {
		return fmt.Errorf("prompt delivery receipt targeted pane %s, want %s", receipt.Target.Ref.Physical(), expected.Physical())
	}
	if !result.Success || result.Delivered != 1 || result.Failed != 0 || result.Blocked != 0 || result.Skipped != 0 || receipt.Status != dispatchsvc.ReceiptDelivered {
		return fmt.Errorf(
			"prompt delivery was not confirmed for pane %s: success=%t delivered=%d failed=%d blocked=%d skipped=%d receipt=%s",
			expected.Physical(),
			result.Success,
			result.Delivered,
			result.Failed,
			result.Blocked,
			result.Skipped,
			receipt.Status,
		)
	}
	return nil
}

// waitForShellReturn polls a pane until its shell has returned after an agent
// exit, primarily by PROCESS DEATH: the shell has returned iff the pane shell
// has no live child process. Pane-content heuristics are only used when the
// process state is unknowable (#187). Returns confirmation plus the last
// captured pane content for diagnostics.
func waitForShellReturn(ctx context.Context, session string, win, pane int, timeout time.Duration) (bool, string, error) {
	if ctx == nil {
		return false, "", errors.New("shell-return context is required")
	}
	target := formatTargetWin(session, win, pane)
	deadline := time.Now().Add(timeout)
	lastOutput := ""
	for {
		if err := ctx.Err(); err != nil {
			return false, lastOutput, err
		}
		if content, err := tmux.CapturePaneOutputContext(ctx, target, 10); err == nil {
			lastOutput = content
		} else if cancelErr := restartPaneCancellationError(ctx, err); cancelErr != nil {
			return false, lastOutput, cancelErr
		}
		childAlive := false
		childKnown := false
		if shellPID, err := getShellPID(ctx, session, win, pane); err == nil && shellPID > 0 {
			childKnown = true
			childAlive = process.HasChildAlive(shellPID)
		} else if cancelErr := restartPaneCancellationError(ctx, err); cancelErr != nil {
			return false, lastOutput, cancelErr
		}
		if confirmShellReturned(childAlive, childKnown, lastOutput) {
			return true, lastOutput, nil
		}
		if time.Now().After(deadline) {
			return false, lastOutput, nil
		}
		if err := waitForExitSequenceDelay(ctx, shellReturnPollInterval); err != nil {
			return false, lastOutput, err
		}
	}
}

// confirmShellReturned reports whether a single observation confirms the pane
// shell has returned. The primary signal is process death: when the child
// state is known, the shell has returned iff no agent child is alive. Only
// when process state is unknowable does it fall back to the prompt-glyph
// heuristic — and then rejects frames the agent parser still classifies as a
// live agent, because a busy Claude pane renders its own "❯" input line which
// satisfies the glyph heuristic (#187).
func confirmShellReturned(childAlive, childKnown bool, paneContent string) bool {
	if childKnown {
		return !childAlive
	}
	if !looksLikeShellPrompt(paneContent) {
		return false
	}
	return !paneShowsLiveAgent(paneContent)
}

// paneShowsLiveAgent reports whether pane content is classified by the agent
// parser as a recognizable agent TUI (the approach verifyRestart uses).
func paneShowsLiveAgent(content string) bool {
	parser := agent.NewParser()
	state, err := parser.Parse(content)
	if err != nil || state == nil {
		return false
	}
	return state.Type != agent.AgentTypeUnknown && state.Type != agent.AgentTypeUser
}

func restartCanonicalAgentType(agentType string) agent.AgentType {
	switch canonical := agent.AgentType(agentType).Canonical(); canonical {
	case agent.AgentTypeClaudeCode,
		agent.AgentTypeCodex,
		agent.AgentTypeGemini,
		agent.AgentTypeAntigravity,
		agent.AgentTypeGrok,
		agent.AgentTypeCursor,
		agent.AgentTypeWindsurf,
		agent.AgentTypeAider,
		agent.AgentTypeOpencode,
		agent.AgentTypeOllama,
		agent.AgentTypeUser,
		agent.AgentTypeUnknown:
		return canonical
	default:
		return agent.AgentTypeUnknown
	}
}

func restartLaunchAlias(agentType string) string {
	switch canonical := restartCanonicalAgentType(agentType); canonical {
	case agent.AgentTypeGrok:
		return ""
	case "", agent.AgentTypeUnknown:
		return string(agent.AgentTypeClaudeCode)
	default:
		return string(canonical)
	}
}

// verifyRestart checks the post-restart state of a pane. win is the pane's real
// window index (#172); win < 0 falls back to the session's first window for the
// single-window case.
func verifyRestart(ctx context.Context, session string, win, pane int, opts SmartRestartOptions) (*PostStateInfo, error) {
	if ctx == nil {
		return nil, errors.New("smart-restart verification context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if win < 0 {
		fw, err := tmux.GetFirstWindowContext(ctx, session)
		if err != nil {
			if cancelErr := restartPaneCancellationError(ctx, err); cancelErr != nil {
				return nil, cancelErr
			}
			fw = 1 // fallback
		}
		win = fw
	}
	// Capture current state
	target := fmt.Sprintf("%s:%d.%d", session, win, pane)
	executor := opts.executor
	if executor == nil {
		executor = liveSmartRestartExecutor{}
	}
	content, err := executor.capturePaneOutput(ctx, target, 50)
	if err != nil {
		if cancelErr := restartPaneCancellationError(ctx, err); cancelErr != nil {
			return nil, cancelErr
		}
		return &PostStateInfo{
			AgentRunning: false,
			Confidence:   0.0,
		}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Parse the output to determine agent state
	parser := agent.NewParser()
	state, err := parser.Parse(content)
	if err != nil {
		return &PostStateInfo{
			AgentRunning: false,
			Confidence:   0.0,
		}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return &PostStateInfo{
		AgentRunning: state.Type != agent.AgentTypeUnknown,
		AgentType:    string(state.Type),
		Confidence:   state.Confidence,
	}, nil
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

// Error helpers to avoid fmt import for performance
func newError(msg string) error {
	return &robotError{msg: msg}
}

func wrapError(prefix string, err error) error {
	return &robotError{msg: prefix + ": " + err.Error()}
}

type robotError struct {
	msg string
}

func (e *robotError) Error() string {
	return e.msg
}
