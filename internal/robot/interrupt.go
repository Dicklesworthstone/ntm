// Package robot provides machine-readable output for AI agents.
// interrupt.go contains the --robot-interrupt flag implementation for priority course correction.
package robot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// InterruptOutput is the structured output for --robot-interrupt
type InterruptOutput struct {
	RobotResponse
	Session        string               `json:"session"`
	InterruptedAt  time.Time            `json:"interrupted_at"`
	CompletedAt    time.Time            `json:"completed_at"`
	Interrupted    []string             `json:"interrupted"`
	PreviousStates map[string]PaneState `json:"previous_states"`
	Method         string               `json:"method"`
	MessageSent    bool                 `json:"message_sent"`
	Message        string               `json:"message,omitempty"`
	Redaction      *RedactionSummary    `json:"redaction,omitempty"`
	Warnings       []string             `json:"warnings,omitempty"`
	ReadyForInput  []string             `json:"ready_for_input"`
	Failed         []InterruptError     `json:"failed"`
	TimeoutMs      int                  `json:"timeout_ms"`
	TimedOut       bool                 `json:"timed_out"`
	DryRun         bool                 `json:"dry_run,omitempty"`
	WouldAffect    []string             `json:"would_affect,omitempty"`
}

// PaneState captures the state of a pane before interruption
type PaneState struct {
	State                 string  `json:"state"`       // active, idle, error, unknown
	LastOutput            string  `json:"last_output"` // Truncated last output (for context)
	AgentType             string  `json:"agent_type"`  // claude, codex, gemini, user, unknown
	ObservationFreshness  string  `json:"observation_freshness"`
	ObservationConfidence float64 `json:"observation_confidence"`
	ObservedAt            string  `json:"observed_at"`
	ObservationError      string  `json:"observation_error,omitempty"`
	LastKnownState        string  `json:"last_known_state,omitempty"`
	LastKnownObservedAt   string  `json:"last_known_observed_at,omitempty"`
}

// InterruptError represents a failed interrupt attempt
type InterruptError struct {
	Pane   string `json:"pane"`
	Reason string `json:"reason"`
}

// InterruptOptions configures the PrintInterrupt operation
type InterruptOptions struct {
	Session        string   // Target session name
	Message        string   // Message to send after interrupt (optional)
	Panes          []string // Specific pane indices to interrupt (empty = all agents)
	All            bool     // Include all panes (including user)
	Force          bool     // Send Ctrl+C even if agent appears idle
	NoWait         bool     // Don't wait for ready state after interrupt
	TimeoutMs      int      // Timeout for waiting for ready state (default 10000)
	PollMs         int      // Poll interval (default 300)
	DryRun         bool     // Preview mode: show what would happen without executing
	RequestID      string   // External request identifier for REST parity
	CorrelationID  string   // Correlation identifier for tracing request/outcome/verification
	IdempotencyKey string   // Idempotency key when provided by an upstream caller
	Redaction      redaction.Config
}

// GetInterrupt sends Ctrl+C to panes and optionally a follow-up message, returning the result.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetInterrupt(opts InterruptOptions) (*InterruptOutput, error) {
	if opts.TimeoutMs <= 0 {
		opts.TimeoutMs = 10000 // Default 10s timeout
	}
	if opts.PollMs <= 0 {
		opts.PollMs = 300 // Default 300ms poll interval
	}
	trace := normalizeActuationTrace(opts.RequestID, opts.CorrelationID, opts.IdempotencyKey)

	interruptedAt := time.Now().UTC()
	output := &InterruptOutput{
		RobotResponse:  NewRobotResponse(true),
		Session:        opts.Session,
		InterruptedAt:  interruptedAt,
		Interrupted:    []string{},
		PreviousStates: make(map[string]PaneState),
		Method:         "ctrl_c",
		MessageSent:    false,
		ReadyForInput:  []string{},
		Failed:         []InterruptError{},
		TimeoutMs:      opts.TimeoutMs,
		TimedOut:       false,
	}

	if interruptMessageBlocked(&opts, output) {
		output.CompletedAt = time.Now().UTC()
		return finalizeTerminalInterruptActuation(trace, opts, nil, output), nil
	}

	if !tmux.SessionExists(opts.Session) {
		output.Failed = append(output.Failed, InterruptError{
			Pane:   "session",
			Reason: fmt.Sprintf("session '%s' not found", opts.Session),
		})
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session '%s' not found", opts.Session),
			ErrCodeSessionNotFound,
			"Use --robot-status to list available sessions",
		)
		output.CompletedAt = time.Now().UTC()
		return finalizeTerminalInterruptActuation(trace, opts, nil, output), nil
	}

	observer := newRobotSessionObserver(20)
	observation, err := observer.Observe(context.Background(), opts.Session)
	if err != nil {
		output.Failed = append(output.Failed, InterruptError{
			Pane:   "panes",
			Reason: fmt.Sprintf("failed to get panes: %v", err),
		})
		output.RobotResponse = NewErrorResponse(
			err,
			ErrCodeInternalError,
			"Check tmux session state",
		)
		output.CompletedAt = time.Now().UTC()
		return finalizeTerminalInterruptActuation(trace, opts, nil, output), nil
	}
	panes := observationPanes(observation)
	observationsByID := observationPaneMap(observation)

	// Topology-aware keys (#172): on a multi-window session every key is the
	// canonical "window.pane" address so panes never collapse onto one entry.
	multiWindow := paneSessionIsMultiWindow(panes)
	targetPanes, err := resolveInterruptTargets(panes, opts.Panes, opts.All)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			err,
			paneSelectorRobotErrorCode(err),
			"Use --robot-status or --robot-interrupt without --panes to inspect canonical pane addresses",
		)
		output.CompletedAt = time.Now().UTC()
		return finalizeTerminalInterruptActuation(trace, opts, nil, output), nil
	}
	targetKeys := make([]string, 0, len(targetPanes))
	for _, pane := range targetPanes {
		targetKeys = append(targetKeys, paneTargetKey(pane, multiWindow))
	}

	if len(targetPanes) == 0 {
		// Fail loud (#172): nothing matched the request, so do not report
		// success:true while interrupting nothing. On multi-window /
		// window-per-agent layouts a window-local --panes index frequently
		// resolves to an empty set; surface the panes that DO exist so the
		// caller can re-target precisely.
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("no panes matched the interrupt request"),
			ErrCodePaneNotFound,
			interruptEmptyTargetHint(opts, panes),
		)
		output.CompletedAt = time.Now().UTC()
		return finalizeTerminalInterruptActuation(trace, opts, targetKeys, output), nil
	}
	if err := validateInterruptFollowUpTargets(panes, targetPanes, opts); err != nil {
		output.RobotResponse = robotDispatchPrepareErrorResponse(err)
		output.Failed = append(output.Failed, InterruptError{
			Pane:   "dispatch",
			Reason: fmt.Sprintf("follow-up message is unavailable: %v", err),
		})
		output.CompletedAt = time.Now().UTC()
		return finalizeTerminalInterruptActuation(trace, opts, targetKeys, output), nil
	}

	// Capture previous state for each pane before interrupting
	for _, pane := range targetPanes {
		paneKey := paneTargetKey(pane, multiWindow)

		paneObservation, found := observationsByID[pane.Ref().StableKey()]
		if !found {
			paneObservation = unavailableRobotPaneObservation(pane, "pane missing from canonical observation", interruptedAt)
		}
		output.PreviousStates[paneKey] = interruptPaneStateFromObservation(paneObservation, interruptPaneAgentType(pane))
	}

	// Dry-run mode: show what would happen without executing
	if opts.DryRun {
		output.DryRun = true
		for _, pane := range targetPanes {
			paneKey := paneTargetKey(pane, multiWindow)
			output.WouldAffect = append(output.WouldAffect, paneKey)
		}
		output.CompletedAt = time.Now().UTC()
		return output, nil
	}

	publishInterruptActuationRequest(trace, opts, targetKeys)

	// Send Ctrl+C to all targets
	for _, pane := range targetPanes {
		paneKey := paneTargetKey(pane, multiWindow)
		prevState := output.PreviousStates[paneKey]

		// Skip if not forced and already idle
		if !opts.Force && prevState.State == "idle" {
			// Already idle, mark as ready but don't interrupt
			output.ReadyForInput = append(output.ReadyForInput, paneKey)
			continue
		}

		err := tmux.SendInterrupt(pane.ID)
		if err != nil {
			output.Failed = append(output.Failed, InterruptError{
				Pane:   paneKey,
				Reason: fmt.Sprintf("failed to send Ctrl+C: %v", err),
			})
		} else {
			output.Interrupted = append(output.Interrupted, paneKey)
		}
	}

	// If we have nothing to wait for, finish early
	if len(output.Interrupted) == 0 && opts.Message == "" {
		markInterruptFailures(opts, output)
		publishInterruptActuationOutcome(trace, opts, targetKeys, output)
		publishInterruptActuationVerification(trace, opts, targetKeys, output)
		output.CompletedAt = time.Now().UTC()
		return output, nil
	}

	// Wait for agents to reach ready state (unless --no-wait)
	if !opts.NoWait && len(output.Interrupted) > 0 {
		deadline := time.Now().Add(time.Duration(opts.TimeoutMs) * time.Millisecond)
		pollInterval := time.Duration(opts.PollMs) * time.Millisecond

		// Small initial delay for interrupt to take effect
		time.Sleep(200 * time.Millisecond)

		pending := make(map[string]bool)
		for _, paneKey := range output.Interrupted {
			pending[paneKey] = true
		}

		for time.Now().Before(deadline) && len(pending) > 0 {
			for paneKey := range pending {
				// Find the pane
				var targetPane *tmux.Pane
				for i := range targetPanes {
					if paneTargetKey(targetPanes[i], multiWindow) == paneKey {
						targetPane = &targetPanes[i]
						break
					}
				}

				if targetPane == nil {
					delete(pending, paneKey)
					continue
				}

				// Check if the agent is ready. The individual capture is
				// intentional here: readiness is a per-pane transition poll.
				current := observeInterruptPoll(
					observer,
					opts.Session,
					*targetPane,
					tmux.CapturePaneOutput,
					tmux.GetPaneActivity,
				)
				state := interruptPaneStateFromObservation(current, interruptPaneAgentType(*targetPane))

				if state.State == "idle" {
					output.ReadyForInput = append(output.ReadyForInput, paneKey)
					delete(pending, paneKey)
				}
			}

			if len(pending) > 0 {
				time.Sleep(pollInterval)
			}
		}

		// Mark as timed out if we still have pending
		if len(pending) > 0 {
			output.TimedOut = true
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("interrupt timed out"),
				ErrCodeTimeout,
				"Increase --interrupt-timeout or check agent health",
			)
			// Pending panes are intentionally not marked ready. A timeout or
			// unavailable observation must not authorize the follow-up message.
		}
	} else if opts.NoWait {
		// If no wait, all interrupted panes are considered ready. Append rather
		// than assign: panes that were already idle were recorded as ready above
		// without being interrupted, and overwriting dropped them from the
		// follow-up message. Assigning also aliased the two JSON arrays.
		output.ReadyForInput = append(output.ReadyForInput, output.Interrupted...)
	}

	// Send follow-up message if provided
	if opts.Message != "" && len(output.ReadyForInput) > 0 {
		// Small delay to ensure interrupt settled
		time.Sleep(100 * time.Millisecond)

		messageTargets := make([]tmux.Pane, 0, len(output.ReadyForInput))
		for _, paneKey := range output.ReadyForInput {
			// Find the pane
			var targetPane *tmux.Pane
			for i := range targetPanes {
				if paneTargetKey(targetPanes[i], multiWindow) == paneKey {
					targetPane = &targetPanes[i]
					break
				}
			}

			if targetPane != nil {
				normalized := *targetPane
				normalized.Tags = append([]string(nil), targetPane.Tags...)
				normalized.Type = interruptPaneTMUXAgentType(*targetPane)
				messageTargets = append(messageTargets, normalized)
			}
		}

		if len(messageTargets) > 0 {
			dispatchPanes := make([]tmux.Pane, len(panes))
			for i, pane := range panes {
				dispatchPanes[i] = pane
				dispatchPanes[i].Tags = append([]string(nil), pane.Tags...)
				dispatchPanes[i].Type = interruptPaneTMUXAgentType(pane)
			}
			service, _, serviceErr := newRobotDispatchService(opts.Redaction, nil, nil)
			if serviceErr != nil {
				output.Failed = append(output.Failed, InterruptError{Pane: "dispatch", Reason: fmt.Sprintf("failed to initialize message dispatch: %v", serviceErr)})
			} else {
				sendOpts := SendOptions{Session: opts.Session, Message: opts.Message}
				prepared, prepareErr := service.Prepare(
					context.Background(),
					robotPreparedDispatchRequest(dispatchPanes, messageTargets, sendOpts, opts.Message, true),
				)
				if prepareErr != nil {
					output.RobotResponse = robotDispatchPrepareErrorResponse(prepareErr)
					output.Failed = append(output.Failed, InterruptError{Pane: "dispatch", Reason: fmt.Sprintf("failed to prepare follow-up message: %v", prepareErr)})
				} else {
					result, _ := service.Dispatch(context.Background(), prepared)
					for _, receipt := range result.Receipts {
						if receipt.Status == dispatchsvc.ReceiptDelivered {
							output.MessageSent = true
							continue
						}
						if receipt.Status == dispatchsvc.ReceiptFailed || receipt.Status == dispatchsvc.ReceiptSkipped || receipt.Status == dispatchsvc.ReceiptBlocked {
							output.Failed = append(output.Failed, InterruptError{Pane: receipt.Target.Address, Reason: fmt.Sprintf("failed to send message: %s", receipt.Error)})
						}
					}
				}
			}
		}
	}

	markInterruptFailures(opts, output)
	publishInterruptActuationOutcome(trace, opts, targetKeys, output)
	publishInterruptActuationVerification(trace, opts, targetKeys, output)
	output.CompletedAt = time.Now().UTC()
	return output, nil
}

func interruptMessageBlocked(opts *InterruptOptions, output *InterruptOutput) bool {
	if opts == nil || output == nil || opts.Message == "" {
		return false
	}
	output.Method = "ctrl_c_then_send"
	message, preview, summary, warnings, blocked := applySendMessageRedaction(opts.Message, opts.Redaction)
	opts.Message = message
	output.Message = preview
	output.Redaction = &summary
	output.Warnings = warnings
	if !blocked {
		return false
	}
	errMsg := "refusing to interrupt: follow-up message contains potential secrets"
	if parts := formatRedactionCategoryCounts(summary.Categories); parts != "" {
		errMsg = fmt.Sprintf("%s (%s)", errMsg, parts)
	}
	output.RobotResponse = NewErrorResponse(
		errors.New(errMsg),
		"SENSITIVE_DATA_BLOCKED",
		"Remove the secret, use redaction mode, or omit the follow-up message",
	)
	output.Failed = append(output.Failed, InterruptError{Pane: "dispatch", Reason: errMsg})
	return true
}

func validateInterruptFollowUpTargets(allPanes, targetPanes []tmux.Pane, opts InterruptOptions) error {
	if opts.Message == "" {
		return nil
	}
	sendOpts := SendOptions{Session: opts.Session, Message: opts.Message}
	return validateRobotPromptDeliveryTargets(allPanes, targetPanes, sendOpts, opts.Message, true)
}

func observeInterruptPoll(
	observer *status.SessionObserver,
	session string,
	pane tmux.Pane,
	capture func(string, int) (string, error),
	activity func(string) (time.Time, error),
) status.PaneObservation {
	captured, captureErr := capture(pane.ID, 10)
	lastActive, activityErr := activity(pane.ID)
	if activityErr != nil {
		captureErr = errors.Join(captureErr, fmt.Errorf("refresh pane activity: %w", activityErr))
	}
	return observer.ObservePaneCapture(
		session,
		tmux.PaneActivity{Pane: pane, LastActivity: lastActive},
		captured,
		captureErr,
	)
}

// markInterruptFailures flips the envelope to success:false when one or more
// interrupt/message-send actions FAILED (#172) but the envelope is otherwise
// still reporting success. It must not clobber an already-set error envelope
// (e.g. a timeout, which is reported separately), so it only acts when
// output.Success is still true and at least one failure was recorded.
func markInterruptFailures(opts InterruptOptions, output *InterruptOutput) {
	if !output.Success || len(output.Failed) == 0 {
		return
	}
	output.Success = false
	output.ErrorCode = ErrCodeInternalError
	output.Error = fmt.Sprintf("%d interrupt action(s) failed", len(output.Failed))
	output.Hint = "Inspect the 'failed' array for per-pane reasons; verify pane addresses with --robot-is-working"
}

// interruptEmptyTargetHint builds an actionable remediation hint for the
// empty-target fail-loud path. It lists the pane indices that DO exist so the
// caller can re-target, and warns that on multi-window layouts a window-local
// --panes index may need a window.pane address.
func interruptEmptyTargetHint(opts InterruptOptions, panes []tmux.Pane) string {
	multiWindow := paneSessionIsMultiWindow(panes)
	existing := make([]string, 0, len(panes))
	for _, p := range panes {
		existing = append(existing, paneTargetKey(p, multiWindow))
	}
	var b strings.Builder
	if len(opts.Panes) > 0 {
		b.WriteString("On multi-window / window-per-agent layouts a bare --panes index is window-local; ")
		b.WriteString("the pane may need a window.pane address. ")
	}
	if len(existing) > 0 {
		b.WriteString("Panes present in this session: ")
		b.WriteString(strings.Join(existing, ", "))
		b.WriteString(". ")
	}
	b.WriteString("Use --robot-is-working to see live pane addresses, or drop --panes to target all agent panes.")
	return b.String()
}

// PrintInterrupt sends Ctrl+C to panes and optionally a follow-up message.
// This is a thin wrapper around GetInterrupt() for CLI output.
func PrintInterrupt(opts InterruptOptions) error {
	output, err := GetInterrupt(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot interrupt failed")
}

func resolveInterruptTargets(panes []tmux.Pane, selectors []string, all bool) ([]tmux.Pane, error) {
	if len(selectors) > 0 {
		return tmux.ResolvePaneSelectors(panes, selectors, false)
	}
	var targetPanes []tmux.Pane
	for _, pane := range panes {
		if !all {
			agentType := interruptPaneAgentType(pane)
			if pane.Index == 0 && agentType == "unknown" {
				continue
			}
			if agentType == "user" {
				continue
			}
		}

		targetPanes = append(targetPanes, pane)
	}
	return targetPanes, nil
}

// selectInterruptTargets preserves the legacy pure helper used by older tests
// and internal callers. Command paths use resolveInterruptTargets so selector
// errors remain typed instead of collapsing to an empty target set.
func selectInterruptTargets(panes []tmux.Pane, paneFilterMap map[string]bool, all bool) []tmux.Pane {
	selectors := make([]string, 0, len(paneFilterMap))
	for selector := range paneFilterMap {
		selectors = append(selectors, selector)
	}
	resolved, err := resolveInterruptTargets(panes, selectors, all)
	if err != nil {
		return nil
	}
	return resolved
}

func unavailableRobotPaneObservation(pane tmux.Pane, observationError string, observedAt time.Time) status.PaneObservation {
	return status.PaneObservation{
		Pane:      pane.Ref(),
		PaneName:  pane.Title,
		AgentType: interruptPaneAgentType(pane),
		Metadata:  pane,
		Current: status.StateObservation{
			Status: status.AgentStatus{
				PaneID:    pane.ID,
				PaneName:  pane.Title,
				AgentType: interruptPaneAgentType(pane),
				State:     status.StateUnknown,
				UpdatedAt: observedAt,
			},
			ObservedAt: observedAt,
			Freshness:  status.FreshnessUnavailable,
			Confidence: 0,
			Error:      observationError,
		},
	}
}

func interruptPaneStateFromObservation(observation status.PaneObservation, agentType string) PaneState {
	result := PaneState{
		State:                 "unknown",
		AgentType:             agentType,
		ObservationFreshness:  string(observation.Current.Freshness),
		ObservationConfidence: observation.Current.Confidence,
		ObservedAt:            FormatTimestamp(observation.Current.ObservedAt),
		ObservationError:      observation.Current.Error,
		LastKnownState:        lastKnownObservationState(observation),
		LastKnownObservedAt:   lastKnownObservationTime(observation),
	}
	if observation.Current.Freshness != status.FreshnessFresh || observation.Current.Error != "" {
		return result
	}

	cleanOutput := stripANSI(observation.RawOutput)
	shortAgentType := translateAgentTypeForStatus(agentType)
	result.LastOutput = getLastMeaningfulOutput(splitLines(cleanOutput), 200, shortAgentType)
	result.State = determineState(observation.RawOutput, agentType)
	switch observation.Current.Status.State {
	case status.StateWorking:
		if result.State == "idle" || result.State == "unknown" {
			result.State = "active"
		}
	case status.StateUnknown:
		result.State = "unknown"
	}
	return result
}

func interruptPaneAgentType(pane tmux.Pane) string {
	if resolved := ResolveAgentType(string(pane.Type)); resolved != "" && resolved != "unknown" {
		return resolved
	}
	return detectAgentType(pane.Title)
}

func interruptPaneTMUXAgentType(pane tmux.Pane) tmux.AgentType {
	if canonical := tmux.AgentType(pane.Type).Canonical(); canonical.IsValid() {
		return canonical
	}
	return tmux.AgentType(interruptPaneAgentType(pane)).Canonical()
}

type interruptMessageTarget struct {
	Pane      string
	Target    string
	AgentType tmux.AgentType
}

func sendInterruptMessages(
	targets []interruptMessageTarget,
	message string,
	send func(target, keys string, enter bool, enterDelay time.Duration, agentType tmux.AgentType) error,
) []InterruptError {
	var errors []InterruptError
	for _, target := range targets {
		enterDelay := tmux.DefaultEnterDelay
		if target.AgentType == tmux.AgentUser || target.AgentType == tmux.AgentUnknown {
			enterDelay = tmux.ShellEnterDelay
		}
		if err := send(target.Target, message, true, enterDelay, target.AgentType); err != nil {
			errors = append(errors, InterruptError{
				Pane:   target.Pane,
				Reason: fmt.Sprintf("failed to send message: %v", err),
			})
		}
	}
	return errors
}

// getLastMeaningfulOutput extracts the last meaningful output lines up to maxLen chars
func getLastMeaningfulOutput(lines []string, maxLen int, agentType string) string {
	// Guard against invalid maxLen values that would cause slice panic
	if maxLen < 4 {
		if maxLen <= 0 {
			return ""
		}
		// Too small for ellipsis, just truncate without it
		var meaningful []string
		totalLen := 0
		for i := len(lines) - 1; i >= 0 && totalLen < maxLen; i-- {
			line := strings.TrimSpace(lines[i])
			if line == "" || status.IsPromptLine(line, agentType) {
				continue
			}
			meaningful = append([]string{line}, meaningful...)
			totalLen += len(line) + 1
		}
		result := strings.Join(meaningful, "\n")
		if len(result) > maxLen {
			return result[:maxLen]
		}
		return result
	}

	var meaningful []string
	totalLen := 0

	// Work backwards through lines
	for i := len(lines) - 1; i >= 0 && totalLen < maxLen; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		// Skip pure prompt lines
		if status.IsPromptLine(line, agentType) {
			continue
		}

		meaningful = append([]string{line}, meaningful...)
		totalLen += len(line) + 1
	}

	result := strings.Join(meaningful, "\n")
	if len(result) > maxLen {
		return result[:maxLen-3] + "..."
	}
	return result
}
