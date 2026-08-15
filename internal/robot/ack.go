// Package robot provides machine-readable output for AI agents.
// ack.go contains the --robot-ack flag implementation for send confirmation tracking.
package robot

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/cm"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// AckOutput is the structured output for --robot-ack
type AckOutput struct {
	RobotResponse
	Session       string            `json:"session"`
	SentAt        time.Time         `json:"sent_at"`
	CompletedAt   time.Time         `json:"completed_at"`
	Confirmations []AckConfirmation `json:"confirmations"`
	Pending       []string          `json:"pending"`
	Failed        []AckFailure      `json:"failed"`
	TimeoutMs     int               `json:"timeout_ms"`
	TimedOut      bool              `json:"timed_out"`
}

// removeAckPanes returns keys with every entry in drop removed, preserving order.
func removeAckPanes(keys []string, drop []string) []string {
	if len(drop) == 0 {
		return keys
	}
	dropped := make(map[string]struct{}, len(drop))
	for _, key := range drop {
		dropped[key] = struct{}{}
	}
	kept := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, ok := dropped[key]; ok {
			continue
		}
		kept = append(kept, key)
	}
	return kept
}

// AckConfirmation represents a successful acknowledgment from an agent
type AckConfirmation struct {
	Pane      string `json:"pane"`
	AckType   string `json:"ack_type"`
	AckAt     string `json:"ack_at"`
	LatencyMs int    `json:"latency_ms"`
}

// AckFailure represents a failed acknowledgment attempt
type AckFailure struct {
	Pane   string `json:"pane"`
	Reason string `json:"reason"`
}

// AckType represents the type of acknowledgment detected
type AckType string

const (
	AckPromptReturned AckType = "prompt_returned" // Agent showed ready prompt after processing
	AckEchoDetected   AckType = "echo_detected"   // Agent echoed the input
	AckExplicitAck    AckType = "explicit_ack"    // Agent responded with understood/acknowledged
	AckOutputStarted  AckType = "output_started"  // Agent began producing output
	AckNone           AckType = "none"            // No acknowledgment detected
)

// We capture enough scrollback to avoid missing short acks when the pane is chatty
// or when the capture window shifts between polls.
const ackCaptureLines = 200

// AckOptions configures the PrintAck operation
type AckOptions struct {
	Session   string   // Target session name
	Message   string   // The message that was sent (for echo detection)
	Panes     []string // Specific pane indices to monitor
	TimeoutMs int      // How long to wait for acknowledgments (default 30000)
	PollMs    int      // How often to poll for changes (default 500)
}

// GetAck monitors panes for acknowledgment after a send operation and returns the result.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetAck(opts AckOptions) (*AckOutput, error) {
	if opts.TimeoutMs <= 0 {
		opts.TimeoutMs = 30000 // Default 30s timeout
	}
	if opts.PollMs <= 0 {
		opts.PollMs = 500 // Default 500ms poll interval
	}

	sentAt := time.Now().UTC()
	output := &AckOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		SentAt:        sentAt,
		Confirmations: []AckConfirmation{},
		Pending:       []string{},
		Failed:        []AckFailure{},
		TimeoutMs:     opts.TimeoutMs,
		TimedOut:      false,
	}

	if !tmux.SessionExists(opts.Session) {
		output.Failed = append(output.Failed, AckFailure{
			Pane:   "session",
			Reason: fmt.Sprintf("session '%s' not found", opts.Session),
		})
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session '%s' not found", opts.Session),
			ErrCodeSessionNotFound,
			"Use --robot-status to list available sessions",
		)
		output.CompletedAt = time.Now().UTC()
		return output, nil
	}

	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		output.Failed = append(output.Failed, AckFailure{
			Pane:   "panes",
			Reason: fmt.Sprintf("failed to get panes: %v", err),
		})
		output.RobotResponse = NewErrorResponse(
			err,
			ErrCodeInternalError,
			"Check tmux session state",
		)
		output.CompletedAt = time.Now().UTC()
		return output, nil
	}

	targetPanes, targetErr := resolveAckTargets(panes, opts.Panes)
	if targetErr != nil {
		output.Failed = append(output.Failed, AckFailure{Pane: "selector", Reason: targetErr.Error()})
		output.RobotResponse = NewErrorResponse(
			targetErr,
			paneSelectorRobotErrorCode(targetErr),
			"Use --robot-status or --robot-is-working to inspect canonical pane addresses",
		)
		output.CompletedAt = time.Now().UTC()
		return output, nil
	}
	multiWindow := tmux.PanesSpanMultipleWindows(panes)
	targetByKey := make(map[string]tmux.Pane, len(targetPanes))
	for _, pane := range targetPanes {
		paneKey := tmux.PaneTargetKey(pane, multiWindow)
		targetByKey[paneKey] = pane
		output.Pending = append(output.Pending, paneKey)
	}

	if len(targetPanes) == 0 {
		output.CompletedAt = time.Now().UTC()
		return output, nil
	}

	// Capture initial state of each pane.
	//
	// A missing baseline must be reported, never treated as empty output.
	// Acknowledgment is detected by diffing against the baseline, and
	// getNewContent("", current) returns the entire scrollback — which almost
	// always contains one of the ack phrases — so a single transient capture-pane
	// failure produced a confirmed acknowledgment on the first poll for a message
	// the agent had never seen.
	initialStates := make(map[string]string)
	var baselineFailures []string
	for _, pane := range targetPanes {
		paneKey := tmux.PaneTargetKey(pane, multiWindow)
		captured, err := func() (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			return tmux.CapturePaneOutputContext(ctx, pane.ID, ackCaptureLines)
		}()
		if err != nil {
			baselineFailures = append(baselineFailures, paneKey)
			output.Failed = append(output.Failed, AckFailure{
				Pane:   paneKey,
				Reason: fmt.Sprintf("baseline capture failed, acknowledgment cannot be detected: %v", err),
			})
			continue
		}
		initialStates[paneKey] = status.StripANSI(captured)
	}
	if len(baselineFailures) > 0 {
		output.Pending = removeAckPanes(output.Pending, baselineFailures)
	}

	// Wait a short initial delay to let agents start processing
	time.Sleep(100 * time.Millisecond)

	// Poll for acknowledgments until timeout
	deadline := time.Now().Add(time.Duration(opts.TimeoutMs) * time.Millisecond)
	pollInterval := time.Duration(opts.PollMs) * time.Millisecond

	for time.Now().Before(deadline) && len(output.Pending) > 0 {
		// Check each pending pane
		stillPending := []string{}

		for _, paneKey := range output.Pending {
			targetPane, ok := targetByKey[paneKey]
			if !ok {
				stillPending = append(stillPending, paneKey)
				continue
			}

			// Capture current output
			captured, err := func() (string, error) {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				return tmux.CapturePaneOutputContext(ctx, targetPane.ID, ackCaptureLines)
			}()
			if err != nil {
				stillPending = append(stillPending, paneKey)
				continue
			}

			currentOutput := status.StripANSI(captured)
			initialOutput, hasBaseline := initialStates[paneKey]
			if !hasBaseline {
				// No baseline means no diff is possible; never claim an ack.
				continue
			}

			// Check for acknowledgment
			ackType, detected := detectAcknowledgmentForAgent(initialOutput, currentOutput, opts.Message, ackPaneAgentType(targetPane))

			if detected {
				latency := time.Since(sentAt)
				output.Confirmations = append(output.Confirmations, AckConfirmation{
					Pane:      paneKey,
					AckType:   string(ackType),
					AckAt:     time.Now().UTC().Format(time.RFC3339),
					LatencyMs: int(latency.Milliseconds()),
				})
			} else {
				stillPending = append(stillPending, paneKey)
			}
		}

		output.Pending = stillPending

		// If still have pending, wait before next poll
		if len(output.Pending) > 0 {
			time.Sleep(pollInterval)
		}
	}

	// Mark as timed out if we still have pending
	if len(output.Pending) > 0 {
		output.TimedOut = true
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("ack timeout"),
			ErrCodeTimeout,
			"Increase --timeout (or deprecated --ack-timeout) or check agent health",
		)
	}

	output.CompletedAt = time.Now().UTC()
	return output, nil
}

// PrintAck monitors panes for acknowledgment after a send operation.
// This is a thin wrapper around GetAck() for CLI output.
func PrintAck(opts AckOptions) error {
	output, err := GetAck(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot ack failed")
}

func ackPaneAgentType(pane tmux.Pane) string {
	if resolved := ResolveAgentType(string(pane.Type)); resolved != "" && resolved != "unknown" {
		return resolved
	}
	return detectAgentType(pane.Title)
}

func ackPaneTMUXAgentType(pane tmux.Pane) tmux.AgentType {
	switch ackPaneAgentType(pane) {
	case "claude":
		return tmux.AgentClaude
	case "codex":
		return tmux.AgentCodex
	case "gemini":
		return tmux.AgentGemini
	case "antigravity":
		return tmux.AgentAntigravity
	case "cursor":
		return tmux.AgentCursor
	case "windsurf":
		return tmux.AgentWindsurf
	case "aider":
		return tmux.AgentAider
	case "oc":
		return tmux.AgentOpencode
	case "ollama":
		return tmux.AgentOllama
	case "user":
		return tmux.AgentUser
	default:
		return tmux.AgentUnknown
	}
}

func shouldSkipDefaultAgentPane(pane tmux.Pane, agentType string) bool {
	if pane.Index == 0 && agentType == "unknown" {
		return true
	}
	return agentType == "user"
}

func resolveAckTargets(panes []tmux.Pane, selectors []string) ([]tmux.Pane, error) {
	ordered := tmux.SortPanesByTopology(panes)
	if len(selectors) > 0 {
		return tmux.ResolvePaneSelectors(ordered, selectors, false)
	}
	targets := make([]tmux.Pane, 0, len(ordered))
	for _, pane := range ordered {
		if !shouldSkipDefaultAgentPane(pane, ackPaneAgentType(pane)) {
			targets = append(targets, pane)
		}
	}
	return targets, nil
}

func selectAckTargets(panes []tmux.Pane, paneFilterMap map[string]bool) []tmux.Pane {
	selectors := make([]string, 0, len(paneFilterMap))
	for selector := range paneFilterMap {
		selectors = append(selectors, selector)
	}
	sort.Strings(selectors)
	targets, _ := resolveAckTargets(panes, selectors)
	return targets
}

// detectAcknowledgment checks if the agent has acknowledged the input
func detectAcknowledgment(initialOutput, currentOutput, message, paneTitle string) (AckType, bool) {
	return detectAcknowledgmentForAgent(initialOutput, currentOutput, message, detectAgentType(paneTitle))
}

func detectAcknowledgmentForAgent(initialOutput, currentOutput, message, agentType string) (AckType, bool) {
	// No change means no ack yet
	if initialOutput == currentOutput {
		return AckNone, false
	}

	// Get the new content (what was added)
	newContent := getNewContent(initialOutput, currentOutput)
	if newContent == "" {
		return AckNone, false
	}

	// Normalize the agent type for prompt detection.
	agentType = translateAgentTypeForStatus(agentType)

	// Check for echo of the sent message (basic confirmation)
	if message != "" && strings.Contains(newContent, truncateForMatch(message)) {
		// Echo detected - but we need to see output AFTER the echo
		afterEcho := getContentAfterEcho(newContent, message)
		if afterEcho != "" {
			return AckEchoDetected, true
		}
	}

	// Check for explicit acknowledgment phrases
	explicitPatterns := []string{
		"understood", "got it", "i'll", "i will", "let me",
		"working on", "starting", "processing",
		"okay", "sure", "yes", "right",
		"looking at", "checking", "analyzing",
	}
	lowerNew := strings.ToLower(newContent)
	for _, pattern := range explicitPatterns {
		if strings.Contains(lowerNew, pattern) {
			return AckExplicitAck, true
		}
	}

	// Check for output started (agent is producing response)
	// This is detected by new lines that aren't just the prompt
	lines := splitLines(newContent)
	nonEmptyLines := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Use agentType for prompt detection to avoid matching generic content
		if trimmed != "" && !status.IsPromptLine(trimmed, agentType) {
			nonEmptyLines++
		}
	}
	if nonEmptyLines >= 2 {
		return AckOutputStarted, true
	}

	// Check for prompt returned (agent finished and showing ready prompt)
	lastLines := getLastNonEmptyLines(currentOutput, 3)
	for _, line := range lastLines {
		if status.IsPromptLine(line, agentType) {
			// If we see idle prompt after new content, agent processed and returned
			if newContent != "" && !strings.Contains(newContent, line) {
				return AckPromptReturned, true
			}
		}
	}

	return AckNone, false
}

// getNewContent extracts the content that was added
func getNewContent(initial, current string) string {
	if initial == current {
		return ""
	}

	// Simple case: content appended
	if strings.HasPrefix(current, initial) {
		return current[len(initial):]
	}

	// When capture windows are fixed-size (e.g., last 20 lines), the window can shift even if
	// only a few lines were appended. That produces different strings with the same line count,
	// and the old length/line-count diff would incorrectly return empty.
	if len(current) <= len(initial) {
		// Content might have been replaced, compare end
		initialLines := splitLines(initial)
		currentLines := splitLines(current)

		if len(currentLines) > len(initialLines) {
			// Return new lines
			newLines := currentLines[len(initialLines):]
			return strings.Join(newLines, "\n")
		}

		// Rolling window shift: find the longest suffix of initial that matches a prefix of current.
		maxK := len(initialLines)
		if len(currentLines) < maxK {
			maxK = len(currentLines)
		}
		for k := maxK; k > 0; k-- {
			ok := true
			initStart := len(initialLines) - k
			for i := 0; i < k; i++ {
				if initialLines[initStart+i] != currentLines[i] {
					ok = false
					break
				}
			}
			if ok {
				if k >= len(currentLines) {
					return ""
				}
				return strings.Join(currentLines[k:], "\n")
			}
		}

		return ""
	}

	// Content changed - find common prefix and return rest
	for i := 0; i < len(initial) && i < len(current); i++ {
		if initial[i] != current[i] {
			return current[i:]
		}
	}

	return current[len(initial):]
}

// truncateForMatch truncates message to first line or first 50 bytes for matching,
// respecting UTF-8 boundaries.
func truncateForMatch(message string) string {
	lines := strings.SplitN(message, "\n", 2)
	msg := lines[0]
	if len(msg) > 50 {
		// Find the last rune boundary at or before 50 bytes
		lastValid := 0
		for i := range msg {
			if i > 50 {
				break
			}
			lastValid = i
		}
		msg = msg[:lastValid]
	}
	return msg
}

// getContentAfterEcho returns content after the echo of the message
func getContentAfterEcho(content, message string) string {
	msgPart := truncateForMatch(message)
	idx := strings.Index(content, msgPart)
	if idx == -1 {
		return ""
	}
	afterIdx := idx + len(msgPart)
	if afterIdx >= len(content) {
		return ""
	}
	return strings.TrimSpace(content[afterIdx:])
}

// getLastNonEmptyLines returns the last N non-empty lines
func getLastNonEmptyLines(content string, n int) []string {
	lines := splitLines(content)
	var result []string
	for i := len(lines) - 1; i >= 0 && len(result) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			result = append(result, lines[i])
		}
	}
	return result
}

// SendAndAck combines sending a message with waiting for acknowledgment
type SendAndAckOptions struct {
	SendOptions
	AckTimeoutMs int // Timeout for acknowledgment (default 30000)
	AckPollMs    int // Poll interval (default 500)
}

// SendAndAckOutput combines send result with acknowledgment tracking
type SendAndAckOutput struct {
	RobotResponse
	Send SendOutput `json:"send"`
	Ack  AckOutput  `json:"ack"`
}

// GetSendAndAck sends a message and waits for acknowledgment, returning the result.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetSendAndAck(opts SendAndAckOptions) (*SendAndAckOutput, error) {
	// First, send the message
	sentAt := time.Now().UTC()
	trace := normalizeActuationTrace(opts.RequestID, opts.CorrelationID, opts.IdempotencyKey)

	redactCfg := normalizeSendRedactionConfig(opts.Redaction)
	messageToSend, preview, redactionSummary, redactionWarnings, blocked := applySendMessageRedaction(opts.Message, redactCfg)
	if blocked {
		errMsg := "refusing to proceed: potential secrets detected (redaction mode: block)"
		if parts := formatRedactionCategoryCounts(redactionSummary.Categories); parts != "" {
			errMsg = fmt.Sprintf("refusing to proceed: potential secrets detected (%s) (redaction mode: block)", parts)
		}
		errResp := NewErrorResponse(fmt.Errorf("%s", errMsg), "SENSITIVE_DATA_BLOCKED", "Re-run with --allow-secret to bypass, or use --redact=warn/--redact=redact")
		return finalizeTerminalSendAndAckActuation(trace, opts, &SendAndAckOutput{
			RobotResponse: errResp,
			Send: SendOutput{
				RobotResponse:  errResp,
				Session:        opts.Session,
				SentAt:         sentAt,
				Blocked:        true,
				Redaction:      redactionSummary,
				Warnings:       redactionWarnings,
				Targets:        []string{},
				Successful:     []string{},
				Failed:         []SendError{},
				MessagePreview: preview,
			},
			Ack: AckOutput{
				RobotResponse: errResp,
				Session:       opts.Session,
				SentAt:        sentAt,
				CompletedAt:   time.Now().UTC(),
				Confirmations: []AckConfirmation{},
				Pending:       []string{},
				Failed:        []AckFailure{{Pane: "send", Reason: "blocked by redaction"}},
			},
		}), nil
	}
	opts.Message = messageToSend

	if !tmux.SessionExists(opts.Session) {
		return finalizeTerminalSendAndAckActuation(trace, opts, &SendAndAckOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("session '%s' not found", opts.Session),
				ErrCodeSessionNotFound,
				"Use --robot-status to list available sessions",
			),
			Send: SendOutput{
				RobotResponse: NewErrorResponse(
					fmt.Errorf("session '%s' not found", opts.Session),
					ErrCodeSessionNotFound,
					"Use --robot-status to list available sessions",
				),
				Session:        opts.Session,
				SentAt:         sentAt,
				Blocked:        false,
				Redaction:      redactionSummary,
				Warnings:       redactionWarnings,
				Targets:        []string{},
				Successful:     []string{},
				Failed:         []SendError{{Pane: "session", Error: fmt.Sprintf("session '%s' not found", opts.Session)}},
				MessagePreview: preview,
			},
			Ack: AckOutput{
				RobotResponse: NewErrorResponse(
					fmt.Errorf("session '%s' not found", opts.Session),
					ErrCodeSessionNotFound,
					"Use --robot-status to list available sessions",
				),
				Session:       opts.Session,
				SentAt:        sentAt,
				CompletedAt:   time.Now().UTC(),
				Confirmations: []AckConfirmation{},
				Pending:       []string{},
				Failed:        []AckFailure{{Pane: "session", Reason: "send failed"}},
			},
		}), nil
	}

	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		return finalizeTerminalSendAndAckActuation(trace, opts, &SendAndAckOutput{
			RobotResponse: NewErrorResponse(
				err,
				ErrCodeInternalError,
				"Check tmux session state",
			),
			Send: SendOutput{
				RobotResponse: NewErrorResponse(
					err,
					ErrCodeInternalError,
					"Check tmux session state",
				),
				Session:        opts.Session,
				SentAt:         sentAt,
				Blocked:        false,
				Redaction:      redactionSummary,
				Warnings:       redactionWarnings,
				Targets:        []string{},
				Successful:     []string{},
				Failed:         []SendError{{Pane: "panes", Error: fmt.Sprintf("failed to get panes: %v", err)}},
				MessagePreview: preview,
			},
			Ack: AckOutput{
				RobotResponse: NewErrorResponse(
					err,
					ErrCodeInternalError,
					"Check tmux session state",
				),
				Session:       opts.Session,
				SentAt:        sentAt,
				CompletedAt:   time.Now().UTC(),
				Confirmations: []AckConfirmation{},
				Pending:       []string{},
				Failed:        []AckFailure{{Pane: "panes", Reason: "failed to get panes"}},
			},
		}), nil
	}

	// Build exclusion map
	excludeMap := make(map[string]bool)
	for _, e := range opts.Exclude {
		excludeMap[e] = true
	}

	// Reuse the canonical robot-send resolver so --track has identical singular,
	// plural, multi-window, exclusion, and type-filter semantics.
	targetPanes, targetKeys, targetErr := selectSendTargets(panes, opts.SendOptions, excludeMap)
	if targetErr != nil {
		errorCode := ErrCodeInvalidFlag
		var selectorErr *tmux.PaneSelectorError
		if errors.As(targetErr, &selectorErr) && selectorErr.Kind == tmux.PaneSelectorNotFound {
			errorCode = ErrCodePaneNotFound
		}
		errResp := NewErrorResponse(targetErr, errorCode, "Use --robot-is-working to inspect canonical pane addresses")
		return finalizeTerminalSendAndAckActuation(trace, opts, &SendAndAckOutput{
			RobotResponse: errResp,
			Send: SendOutput{
				RobotResponse:  errResp,
				Session:        opts.Session,
				SentAt:         sentAt,
				Blocked:        false,
				Redaction:      redactionSummary,
				Warnings:       redactionWarnings,
				Targets:        []string{},
				Successful:     []string{},
				Failed:         []SendError{{Pane: "selector", Error: targetErr.Error()}},
				MessagePreview: preview,
			},
			Ack: AckOutput{
				RobotResponse: errResp,
				Session:       opts.Session,
				SentAt:        sentAt,
				CompletedAt:   time.Now().UTC(),
				Confirmations: []AckConfirmation{},
				Pending:       []string{},
				Failed:        []AckFailure{{Pane: "selector", Reason: "send target resolution failed"}},
			},
		}), nil
	}
	// Perform CM memory rule injection if enabled (bd-3j6hm), mirroring the
	// GetSend path. The injected rule IDs feed the automatic outcome report
	// after a confirmed acknowledgment. cm being unavailable records a skip
	// reason and the send proceeds unmodified.
	var memoryInfo *CMInjectionInfo
	memCfg := DefaultCMInjectConfig()
	if opts.MemoryInject != nil {
		memCfg = *opts.MemoryInject
	}
	if opts.WithMemory && len(targetPanes) > 0 {
		modified, memInfo := InjectCMRules(context.Background(), opts.Message, opts.Message, memCfg)
		memoryInfo = memInfo
		if modified != "" {
			opts.Message = modified
		}
	}

	// A pane with no baseline is excluded from acknowledgment tracking below
	// rather than diffed against an empty string, which would return the whole
	// scrollback and confirm an ack the agent never gave.
	initialStates := make(map[string]string)

	for i, pane := range targetPanes {
		paneKey := targetKeys[i]
		// Capture initial state before sending
		captured, err := func() (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			return tmux.CapturePaneOutputContext(ctx, pane.ID, ackCaptureLines)
		}()
		if err == nil {
			initialStates[paneKey] = status.StripANSI(captured)
		}
	}

	sendOutput := SendOutput{
		RobotResponse:   NewRobotResponse(true), // Will be updated based on results
		Session:         opts.Session,
		SentAt:          sentAt,
		Blocked:         false,
		Redaction:       redactionSummary,
		Warnings:        redactionWarnings,
		Targets:         targetKeys,
		Successful:      []string{},
		Failed:          []SendError{},
		MessagePreview:  preview,
		MemoryInjection: memoryInfo,
	}
	if len(targetPanes) == 0 {
		sendOutput.RobotResponse = NewErrorResponse(
			errors.New("no target panes matched the filter criteria"),
			ErrCodeInvalidFlag,
			"Check --pane, --panes, --type, --exclude, and --all",
		)
	} else {
		sendEnter := true
		if opts.Enter != nil {
			sendEnter = *opts.Enter
		}
		service, finalRedactor, serviceErr := newRobotDispatchService(redactCfg, nil, nil)
		if serviceErr != nil {
			sendOutput.RobotResponse = NewErrorResponse(serviceErr, ErrCodeInternalError, "Dispatch service initialization failed")
			sendOutput.Failed = append(sendOutput.Failed, SendError{Pane: "dispatch", Error: serviceErr.Error()})
		} else {
			prepared, prepareErr := service.Prepare(
				context.Background(),
				robotPreparedDispatchRequest(panes, targetPanes, opts.SendOptions, opts.Message, sendEnter),
			)
			finalPreview, finalSummary, finalWarnings := finalRedactor.outputView()
			if finalSummary.Mode != "" && (finalSummary.Findings > 0 || sendOutput.Redaction.Findings == 0) {
				sendOutput.MessagePreview = finalPreview
				sendOutput.Redaction = finalSummary
				sendOutput.Warnings = finalWarnings
			}
			if prepareErr != nil {
				sendOutput.RobotResponse = robotDispatchPrepareErrorResponse(prepareErr)
				sendOutput.Failed = append(sendOutput.Failed, SendError{Pane: "dispatch", Error: prepareErr.Error()})
			} else if opts.DryRun {
				result, dispatchErr := service.Dispatch(context.Background(), prepared)
				if dispatchErr != nil {
					sendOutput.RobotResponse = NewErrorResponse(dispatchErr, ErrCodeInternalError, "Dispatch dry-run failed")
					sendOutput.Failed = append(sendOutput.Failed, SendError{Pane: "dispatch", Error: dispatchErr.Error()})
				} else {
					sendOutput.DryRun = true
					sendOutput.WouldSendTo = append(sendOutput.WouldSendTo, targetKeys...)
					sendOutput.Success = result.Success
				}
			} else {
				publishSendActuationRequest(trace, opts.SendOptions, targetKeys, sendOutput.MessagePreview)
				result, _ := service.Dispatch(context.Background(), prepared)
				applyRobotDispatchResult(&sendOutput, result)
			}
		}
	}

	// Update success based on send results without clobbering typed preflight errors.
	finalizeRobotSendDispatchStatus(&sendOutput)
	publishSendActuationOutcome(trace, opts.SendOptions, sendOutput)
	if opts.DryRun {
		ackOutput := AckOutput{
			RobotResponse: NewRobotResponse(sendOutput.Success),
			Session:       opts.Session,
			SentAt:        sentAt,
			CompletedAt:   time.Now().UTC(),
			Confirmations: []AckConfirmation{},
			Pending:       []string{},
			Failed:        []AckFailure{},
			TimeoutMs:     opts.AckTimeoutMs,
		}
		combined := sendOutput.RobotResponse
		if sendOutput.Success {
			combined = NewRobotResponse(true)
		}
		return &SendAndAckOutput{RobotResponse: combined, Send: sendOutput, Ack: ackOutput}, nil
	}

	// Now wait for acknowledgments (only for successful sends)
	ackOutput := AckOutput{
		RobotResponse: NewRobotResponse(true), // Will be updated based on results
		Session:       opts.Session,
		SentAt:        sentAt,
		Confirmations: []AckConfirmation{},
		Pending:       sendOutput.Successful,
		Failed:        []AckFailure{},
		TimeoutMs:     opts.AckTimeoutMs,
		TimedOut:      false,
	}

	if opts.AckTimeoutMs <= 0 {
		opts.AckTimeoutMs = 30000
	}
	if opts.AckPollMs <= 0 {
		opts.AckPollMs = 500
	}

	// Wait for initial processing delay
	time.Sleep(100 * time.Millisecond)

	// Poll for acknowledgments
	deadline := time.Now().Add(time.Duration(opts.AckTimeoutMs) * time.Millisecond)
	pollInterval := time.Duration(opts.AckPollMs) * time.Millisecond

	for time.Now().Before(deadline) && len(ackOutput.Pending) > 0 {
		stillPending := []string{}

		for _, paneKey := range ackOutput.Pending {
			var targetPane *tmux.Pane
			for i := range targetPanes {
				if targetKeys[i] == paneKey {
					targetPane = &targetPanes[i]
					break
				}
			}

			if targetPane == nil {
				stillPending = append(stillPending, paneKey)
				continue
			}

			captured, err := func() (string, error) {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				return tmux.CapturePaneOutputContext(ctx, targetPane.ID, ackCaptureLines)
			}()
			if err != nil {
				stillPending = append(stillPending, paneKey)
				continue
			}

			currentOutput := status.StripANSI(captured)
			initialOutput, hasBaseline := initialStates[paneKey]
			if !hasBaseline {
				// Baseline capture failed before the send, so no diff is possible
				// and an acknowledgment cannot be honestly confirmed.
				ackOutput.Failed = append(ackOutput.Failed, AckFailure{
					Pane:   paneKey,
					Reason: "baseline capture failed before send, acknowledgment cannot be detected",
				})
				continue
			}

			ackType, detected := detectAcknowledgmentForAgent(initialOutput, currentOutput, opts.Message, ackPaneAgentType(*targetPane))

			if detected {
				latency := time.Since(sentAt)
				ackOutput.Confirmations = append(ackOutput.Confirmations, AckConfirmation{
					Pane:      paneKey,
					AckType:   string(ackType),
					AckAt:     time.Now().UTC().Format(time.RFC3339),
					LatencyMs: int(latency.Milliseconds()),
				})
			} else {
				stillPending = append(stillPending, paneKey)
			}
		}

		ackOutput.Pending = stillPending

		if len(ackOutput.Pending) > 0 {
			time.Sleep(pollInterval)
		}
	}

	if len(ackOutput.Pending) > 0 {
		ackOutput.TimedOut = true
		ackOutput.RobotResponse = NewErrorResponse(
			fmt.Errorf("ack timeout"),
			ErrCodeTimeout,
			"Increase --timeout (or deprecated --ack-timeout) or check agent health",
		)
	}

	ackOutput.CompletedAt = time.Now().UTC()
	publishSendActuationVerification(trace, opts, sendOutput, ackOutput)

	// Automatic outcome feedback (bd-3j6hm): only clear evidence is reported.
	// A confirmed acknowledgment with no panes left pending is a success for
	// the injected rules; an ack timeout is ambiguous and reports nothing.
	// reportCMOutcome is bounded and never fails the send.
	if shouldReportCMSendOutcome(opts.WithMemory, memoryInfo, len(ackOutput.Confirmations), ackOutput.TimedOut) {
		reportCMOutcome(memCfg, cm.OutcomeSuccess, memoryInfo.RulesInjected)
	}

	combined := NewRobotResponse(true)
	if !sendOutput.Success {
		combined = sendOutput.RobotResponse
	} else if !ackOutput.Success {
		combined = ackOutput.RobotResponse
	}

	return &SendAndAckOutput{
		RobotResponse: combined,
		Send:          sendOutput,
		Ack:           ackOutput,
	}, nil
}

func selectSendAndAckTargets(panes []tmux.Pane, excludeMap, paneFilterMap, typeFilterMap map[string]bool, all bool) ([]tmux.Pane, []string) {
	hasPaneFilter := len(paneFilterMap) > 0
	hasTypeFilter := len(typeFilterMap) > 0

	var targetPanes []tmux.Pane
	var targetKeys []string
	for _, pane := range panes {
		paneKey := fmt.Sprintf("%d", pane.Index)

		if excludeMap[paneKey] || excludeMap[pane.ID] {
			continue
		}
		if hasPaneFilter && !paneFilterMap[paneKey] && !paneFilterMap[pane.ID] {
			continue
		}

		agentType := ackPaneAgentType(pane)
		if hasTypeFilter && !typeFilterMap[agentType] {
			continue
		}
		if !all && !hasPaneFilter && !hasTypeFilter && shouldSkipDefaultAgentPane(pane, agentType) {
			continue
		}

		targetPanes = append(targetPanes, pane)
		targetKeys = append(targetKeys, paneKey)
	}

	return targetPanes, targetKeys
}

func sendAndAckToPane(
	pane tmux.Pane,
	message string,
	enterOverride *bool,
	send func(target, keys string, enter bool, enterDelay time.Duration, agentType tmux.AgentType) error,
) error {
	sendEnter := true
	if enterOverride != nil {
		sendEnter = *enterOverride
	}

	enterDelay := tmux.DefaultEnterDelay
	agentType := ackPaneAgentType(pane)
	if pane.Type == tmux.AgentUser || agentType == "user" || agentType == "unknown" {
		enterDelay = tmux.ShellEnterDelay
	}

	return send(pane.ID, message, sendEnter, enterDelay, ackPaneTMUXAgentType(pane))
}

// PrintSendAndAck sends a message and waits for acknowledgment.
// This is a thin wrapper around GetSendAndAck() for CLI output.
func PrintSendAndAck(opts SendAndAckOptions) error {
	output, err := GetSendAndAck(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot send-and-ack failed")
}
