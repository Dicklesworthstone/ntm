// Package robot provides machine-readable output for AI agents.
// interrupt.go contains the --robot-interrupt flag implementation for priority course correction.
package robot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/state"
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

	// Operation is the durable idempotent-operation receipt, present when
	// the caller supplied an operation ID (--op-id / Idempotency-Key).
	Operation *OperationInfo `json:"operation,omitempty"`
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
	AgentTypes     []string // Only interrupt panes of these agent types (aliases accepted; empty = any type)
	All            bool     // Include all panes (including user)
	Force          bool     // Send Ctrl+C even if agent appears idle
	NoWait         bool     // Don't wait for ready state after interrupt
	TimeoutMs      int      // Timeout for waiting for ready state (default 10000)
	PollMs         int      // Poll interval (default 300)
	DryRun         bool     // Preview mode: show what would happen without executing
	RequestID      string   // External request identifier for REST parity
	CorrelationID  string   // Correlation identifier for tracing request/outcome/verification
	IdempotencyKey string   // Durable operation ID (--op-id / REST Idempotency-Key), claimed before any pane is touched
	Redaction      redaction.Config
	// MessageGuard checks a follow-up against the resolved physical targets
	// before any interrupt key or durable operation claim. Control-only
	// interrupts do not dispatch a message and do not invoke this guard. The
	// ready subset is checked again immediately before follow-up delivery.
	MessageGuard func(context.Context, string, []tmux.Pane) error `json:"-"`
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
	inputMessage := opts.Message
	// The operation binding digests the caller's INPUT follow-up message, so
	// it is computed before redaction rewrites opts.Message below.
	var operationBinding string
	if trace.IdempotencyKey != "" && !opts.DryRun {
		operationBinding = interruptOperationBindingHash(opts)
	}

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
	targetPanes, err := resolveInterruptTargets(panes, opts.Panes, opts.All, opts.AgentTypes)
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
	// The follow-up message is an arbitrary payload like any send: vet it
	// before interrupting anything, against the agent types it will reach.
	if opts.Message != "" {
		guardPanes := make([]tmux.Pane, len(targetPanes))
		for i, pane := range targetPanes {
			guardPanes[i] = pane
			guardPanes[i].Type = interruptPaneTMUXAgentType(pane)
		}
		if opts.MessageGuard != nil {
			if err := opts.MessageGuard(context.Background(), opts.Message, append([]tmux.Pane(nil), guardPanes...)); err != nil {
				return nil, fmt.Errorf("message policy check: %w", err)
			}
		}
		if resp, _, err := checkSendCommandGuard(context.Background(), opts.Message, opts.Session, guardPanes); err != nil {
			output.RobotResponse = resp
			output.Failed = append(output.Failed, InterruptError{Pane: "guard", Reason: err.Error()})
			output.CompletedAt = time.Now().UTC()
			return finalizeTerminalInterruptActuation(trace, opts, targetKeys, output), nil
		}
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

	// Durable idempotent operation claim (protocol shared with --robot-send,
	// operation_idempotency.go). It happens after every preflight check and
	// BEFORE the first interrupt key or follow-up message, so an identical
	// retry after a caller timeout replays the recorded outcome instead of
	// interrupting the agents — and delivering the new task — a second time.
	// Everything past the claim may touch panes, so the outcome is always
	// recorded as terminal rather than released.
	if trace.IdempotencyKey != "" {
		claim := claimRobotOperation(operationClaimRequest{
			Kind:        state.OperationKindInterrupt,
			OperationID: trace.IdempotencyKey,
			Session:     opts.Session,
			BindingHash: operationBinding,
			Payload:     opts.Message,
			Targets:     targetKeys,
		})
		switch claim.Verdict {
		case operationRefused:
			output.RobotResponse = claim.Response
			output.Operation = claim.Info
			output.CompletedAt = time.Now().UTC()
			return finalizeTerminalInterruptActuation(trace, opts, targetKeys, output), nil
		case operationReplay:
			if err := applyReplayedInterruptOutcome(output, claim.Record); err != nil {
				output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Stored interrupt outcome could not be decoded")
				output.CompletedAt = time.Now().UTC()
			}
			return finalizeTerminalInterruptActuation(trace, opts, targetKeys, output), nil
		}
		if err := claim.Owned.start(opts.Message, targetKeys); err != nil {
			output.RobotResponse = operationStartErrorResponse(err)
			if warning := claim.Owned.release(); warning != "" {
				output.Warnings = append(output.Warnings, warning)
			}
			output.CompletedAt = time.Now().UTC()
			return finalizeTerminalInterruptActuation(trace, opts, targetKeys, output), nil
		}
		output.Operation = operationInfoFromRecord(claim.Owned.record, false)
		defer completeInterruptOperation(claim.Owned, targetKeys, opts.Message != "", output)
	}

	publishInterruptActuationRequest(trace, opts, targetKeys)

	// Send each target its agent's interrupt key (Ctrl+C; Escape for omp,
	// where Ctrl+C only clears the draft and a double press quits omp).
	for _, pane := range targetPanes {
		paneKey := paneTargetKey(pane, multiWindow)
		prevState := output.PreviousStates[paneKey]

		// Skip if not forced and already idle
		if !opts.Force && prevState.State == "idle" {
			// Already idle, mark as ready but don't interrupt
			output.ReadyForInput = append(output.ReadyForInput, paneKey)
			continue
		}

		agentType := interruptPaneTMUXAgentType(pane)
		err := tmux.SendInterruptForAgent(pane.ID, agentType)
		if err != nil {
			output.Failed = append(output.Failed, InterruptError{
				Pane:   paneKey,
				Reason: fmt.Sprintf("failed to send %s: %v", tmux.InterruptKeyLabel(agentType), err),
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
			service, _, serviceErr := newRobotDispatchService(opts.Redaction, guardedRobotMessageDeliverer(opts.MessageGuard), nil)
			if serviceErr != nil {
				output.Failed = append(output.Failed, InterruptError{Pane: "dispatch", Reason: fmt.Sprintf("failed to initialize message dispatch: %v", serviceErr)})
			} else {
				// Apply final-message policy to the original input, as in the
				// preflight above. Re-scanning opts.Message would redact the
				// replacement markers again and change the frozen payload.
				sendOpts := SendOptions{Session: opts.Session, Message: inputMessage}
				prepared, prepareErr := service.Prepare(
					context.Background(),
					robotPreparedDispatchRequest(dispatchPanes, messageTargets, sendOpts, inputMessage, true),
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

// interruptOperationBindingHash binds an idempotent interrupt to the
// caller's canonical COMMAND spec: actuation kind, session, pane selectors,
// --all, --force, the --type filter, and a digest of the caller's INPUT
// follow-up message (before redaction).
//
// As with sends, the selector — not the resolved pane list — is bound, so a
// byte-identical retry replays even when pane topology changed between
// attempts; selector lists are canonicalized (trimmed, sorted). Wait
// behavior (--no-wait, --timeout) is deliberately not bound: it shapes how
// long the caller waits for closed-loop verification, and a caller retrying
// after a timeout commonly lengthens it.
//
// Unlike sends, the kind is the first hashed field: interrupt bindings have
// no pre-kind history to stay compatible with.
func interruptOperationBindingHash(opts InterruptOptions) string {
	b := newOperationBindingHasher()
	b.field(state.OperationKindInterrupt)
	b.field(opts.Session)
	b.list(opts.Panes)
	b.field(strconv.FormatBool(opts.All))
	b.field(strconv.FormatBool(opts.Force))
	b.list(normalizedAgentTypeFilter(opts.AgentTypes))
	inputSHA, _ := operationPayloadDigest(opts.Message)
	b.field(inputSHA)
	return b.sum()
}

// interruptOperationOutcome is the durable outcome record of a completed
// idempotent interrupt, replayed verbatim to identical retries. Pane output
// captured into previous_states is deliberately NOT persisted (last_output
// is blanked): the receipt store keeps digests and states, never pane
// contents.
type interruptOperationOutcome struct {
	Success        bool                 `json:"success"`
	Error          string               `json:"error,omitempty"`
	ErrorCode      string               `json:"error_code,omitempty"`
	InterruptedAt  time.Time            `json:"interrupted_at"`
	CompletedAt    time.Time            `json:"completed_at"`
	Method         string               `json:"method"`
	Targets        []string             `json:"targets"`
	Interrupted    []string             `json:"interrupted"`
	PreviousStates map[string]PaneState `json:"previous_states"`
	ReadyForInput  []string             `json:"ready_for_input"`
	Failed         []InterruptError     `json:"failed"`
	MessageSent    bool                 `json:"message_sent"`
	MessagePreview string               `json:"message_preview,omitempty"`
	TimedOut       bool                 `json:"timed_out"`
	Admissions     []OperationAdmission `json:"admissions"`
}

// admissionsFromInterruptOutput derives per-target admission states for a
// finished interrupt: rejected when any action against the target failed
// (interrupt key or follow-up message), submitted when the target accepted
// the interrupt key or — already ready, so not interrupted — the follow-up
// message, and not_attempted when the target was already ready and nothing
// was submitted to it.
func admissionsFromInterruptOutput(targets []string, messageRequested bool, output *InterruptOutput) []OperationAdmission {
	if output == nil {
		return nil
	}
	interrupted := make(map[string]bool, len(output.Interrupted))
	for _, target := range output.Interrupted {
		interrupted[target] = true
	}
	ready := make(map[string]bool, len(output.ReadyForInput))
	for _, target := range output.ReadyForInput {
		ready[target] = true
	}
	failures := make(map[string]string, len(output.Failed))
	for _, failure := range output.Failed {
		if previous, seen := failures[failure.Pane]; seen {
			failures[failure.Pane] = previous + "; " + failure.Reason
			continue
		}
		failures[failure.Pane] = failure.Reason
	}

	admissions := make([]OperationAdmission, 0, len(targets))
	for _, target := range targets {
		switch reason, failed := failures[target]; {
		case failed:
			admissions = append(admissions, OperationAdmission{Target: target, State: AdmissionRejected, Error: reason})
		case interrupted[target], messageRequested && output.MessageSent && ready[target]:
			admissions = append(admissions, OperationAdmission{Target: target, State: AdmissionSubmitted})
		default:
			admissions = append(admissions, OperationAdmission{Target: target, State: AdmissionNotAttempted})
		}
	}
	return admissions
}

// completeInterruptOperation persists the terminal outcome of a claimed
// interrupt and attaches the operation info to the output. Best-effort: a
// persistence failure surfaces as a warning rather than failing an
// interrupt that already happened.
func completeInterruptOperation(op *durableOperation, targets []string, messageRequested bool, output *InterruptOutput) {
	admissions := admissionsFromInterruptOutput(targets, messageRequested, output)
	recordedStates := make(map[string]PaneState, len(output.PreviousStates))
	for key, paneState := range output.PreviousStates {
		paneState.LastOutput = ""
		recordedStates[key] = paneState
	}
	info, warning := op.complete(interruptOperationOutcome{
		Success:        output.Success,
		Error:          output.Error,
		ErrorCode:      output.ErrorCode,
		InterruptedAt:  output.InterruptedAt,
		CompletedAt:    output.CompletedAt,
		Method:         output.Method,
		Targets:        targets,
		Interrupted:    output.Interrupted,
		PreviousStates: recordedStates,
		ReadyForInput:  output.ReadyForInput,
		Failed:         output.Failed,
		MessageSent:    output.MessageSent,
		MessagePreview: output.Message,
		TimedOut:       output.TimedOut,
		Admissions:     admissions,
	}, admissions)
	if warning != "" {
		output.Warnings = append(output.Warnings, warning)
		return
	}
	output.Operation = info
}

// applyReplayedInterruptOutcome restores a stored outcome onto a fresh
// InterruptOutput so an identical retry observes the original result without
// sending a second interrupt key or follow-up message.
func applyReplayedInterruptOutcome(output *InterruptOutput, op *state.SendOperation) error {
	var outcome interruptOperationOutcome
	if err := json.Unmarshal([]byte(op.OutcomeJSON), &outcome); err != nil {
		return fmt.Errorf("decode stored interrupt outcome: %w", err)
	}
	output.Success = outcome.Success
	output.Error = outcome.Error
	output.ErrorCode = outcome.ErrorCode
	output.InterruptedAt = outcome.InterruptedAt
	output.CompletedAt = outcome.CompletedAt
	if outcome.Method != "" {
		output.Method = outcome.Method
	}
	output.Interrupted = nonNilStrings(outcome.Interrupted)
	output.PreviousStates = outcome.PreviousStates
	if output.PreviousStates == nil {
		output.PreviousStates = map[string]PaneState{}
	}
	output.ReadyForInput = nonNilStrings(outcome.ReadyForInput)
	output.Failed = outcome.Failed
	if output.Failed == nil {
		output.Failed = []InterruptError{}
	}
	output.MessageSent = outcome.MessageSent
	if outcome.MessagePreview != "" {
		output.Message = outcome.MessagePreview
	}
	output.TimedOut = outcome.TimedOut
	output.Operation = replayedOperationInfo(op, outcome.Admissions)
	// A replayed failure (including a verification timeout) is terminal for
	// THIS operation ID: interrupt keys and the follow-up message may already
	// have landed, so the retry semantics the caller opted into forbid a
	// second attempt under the same ID.
	if !outcome.Success && output.Hint == "" {
		output.Hint = "recorded outcome replayed without re-interrupting; use a new operation ID to interrupt again"
	}
	return nil
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// InterruptReceiptOutcome is the recorded terminal result of a completed
// interrupt operation as exposed by receipt queries.
type InterruptReceiptOutcome struct {
	Success       bool             `json:"success"`
	InterruptedAt time.Time        `json:"interrupted_at"`
	CompletedAt   time.Time        `json:"completed_at"`
	Method        string           `json:"method"`
	Targets       []string         `json:"targets"`
	Interrupted   []string         `json:"interrupted"`
	ReadyForInput []string         `json:"ready_for_input"`
	Failed        []InterruptError `json:"failed"`
	MessageSent   bool             `json:"message_sent"`
	TimedOut      bool             `json:"timed_out"`
	Error         string           `json:"error,omitempty"`
	ErrorCode     string           `json:"error_code,omitempty"`
}

func interruptReceiptOutcomeFromJSON(outcomeJSON string) (*InterruptReceiptOutcome, error) {
	var outcome interruptOperationOutcome
	if err := json.Unmarshal([]byte(outcomeJSON), &outcome); err != nil {
		return nil, fmt.Errorf("decode stored interrupt outcome: %w", err)
	}
	failed := outcome.Failed
	if failed == nil {
		failed = []InterruptError{}
	}
	return &InterruptReceiptOutcome{
		Success:       outcome.Success,
		InterruptedAt: outcome.InterruptedAt,
		CompletedAt:   outcome.CompletedAt,
		Method:        outcome.Method,
		Targets:       nonNilStrings(outcome.Targets),
		Interrupted:   nonNilStrings(outcome.Interrupted),
		ReadyForInput: nonNilStrings(outcome.ReadyForInput),
		Failed:        failed,
		MessageSent:   outcome.MessageSent,
		TimedOut:      outcome.TimedOut,
		Error:         outcome.Error,
		ErrorCode:     outcome.ErrorCode,
	}, nil
}

// resolveInterruptTargets picks the panes an interrupt actuates: the explicit
// selectors when given, otherwise every agent pane (every pane with all).
// A non-empty agentTypes list then keeps only panes of those types, so
// --type narrows either set the way it does for --robot-send.
func resolveInterruptTargets(panes []tmux.Pane, selectors []string, all bool, agentTypes []string) ([]tmux.Pane, error) {
	var targetPanes []tmux.Pane
	if len(selectors) > 0 {
		resolved, err := tmux.ResolvePaneSelectors(panes, selectors, false)
		if err != nil {
			return nil, err
		}
		targetPanes = resolved
	} else {
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
	}
	return keepPanesOfAgentTypes(targetPanes, agentTypes, interruptPaneAgentType), nil
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
	}
	if observation.Current.Freshness != status.FreshnessFresh || observation.Current.Error != "" {
		return result
	}

	cleanOutput := stripANSI(observation.RawOutput)
	shortAgentType := translateAgentTypeForStatus(agentType)
	result.LastOutput = status.LastMeaningfulOutputLines(splitLines(cleanOutput), shortAgentType, 200)
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

// The private last-meaningful-output extractor that used to live here was
// subsumed by status.LastMeaningfulOutputLines (ORI: one reachable
// implementation). It only skipped blank and prompt lines, so it shared the
// dashboard's blind spot for the composer box and status line (ntm#322); the
// shared helper strips those too.
