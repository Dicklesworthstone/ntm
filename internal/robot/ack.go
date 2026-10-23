// Package robot provides machine-readable output for AI agents.
// ack.go contains the --robot-ack flag implementation for send confirmation tracking.
package robot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
	AckEchoDetected   AckType = "echo_detected"   // New response followed the complete input echo
	AckExplicitAck    AckType = "explicit_ack"    // Agent responded with understood/acknowledged
	AckOutputStarted  AckType = "output_started"  // Agent began producing output
	AckNone           AckType = "none"            // No acknowledgment detected
)

// We capture enough scrollback to avoid missing short acks when the pane is chatty
// or when the capture window shifts between polls.
const ackCaptureLines = 200

// AckOptions configures the PrintAck operation
type AckOptions struct {
	Context    context.Context `json:"-"` // Caller cancellation; nil uses context.Background
	Session    string          // Target session name
	Message    string          // The message that was sent (for echo detection)
	Panes      []string        // Specific pane indices to monitor
	AgentTypes []string        // Only watch panes of these agent types (aliases accepted; empty = any type)
	TimeoutMs  int             // How long to wait for acknowledgments (default 30000)
	PollMs     int             // How often to poll for changes (default 500)
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
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(opts.TimeoutMs)*time.Millisecond)
	defer cancel()

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

	exists, err := tmux.SessionExistsContext(ctx, opts.Session)
	if err != nil {
		output.Failed = append(output.Failed, AckFailure{Pane: "session", Reason: err.Error()})
		output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Check tmux session state")
		finishAckOutput(ctx, output)
		return output, nil
	}
	if !exists {
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

	panes, err := tmux.GetPanesContext(ctx, opts.Session)
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
		finishAckOutput(ctx, output)
		return output, nil
	}

	targetPanes, targetErr := resolveAckTargets(panes, opts.Panes, opts.AgentTypes)
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
			ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
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

	pollAcknowledgments(ctx, output, targetByKey, initialStates, opts.Message, time.Duration(opts.PollMs)*time.Millisecond)
	return output, nil
}

// Both ack surfaces share one deadline. A slow capture or a poll interval
// longer than the requested timeout must not extend the acknowledgment wait.
// Baseline failures are terminal verification failures, not confirmations.
func pollAcknowledgments(ctx context.Context, output *AckOutput, targets map[string]tmux.Pane, initialStates map[string]string, message string, pollInterval time.Duration) {
	for ctx.Err() == nil && len(output.Pending) > 0 {
		stillPending := make([]string, 0, len(output.Pending))
		for i, paneKey := range output.Pending {
			if ctx.Err() != nil {
				stillPending = append(stillPending, output.Pending[i:]...)
				break
			}
			initialOutput, hasBaseline := initialStates[paneKey]
			if !hasBaseline {
				output.Failed = append(output.Failed, AckFailure{
					Pane: paneKey, Reason: "baseline capture failed, acknowledgment cannot be detected",
				})
				continue
			}
			pane, ok := targets[paneKey]
			if !ok {
				output.Failed = append(output.Failed, AckFailure{Pane: paneKey, Reason: "acknowledgment target is unavailable"})
				continue
			}
			captureCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			captured, err := tmux.CapturePaneOutputContext(captureCtx, pane.ID, ackCaptureLines)
			cancel()
			if err != nil || ctx.Err() != nil {
				stillPending = append(stillPending, paneKey)
				continue
			}
			ackType, detected := detectAcknowledgmentForAgent(initialOutput, status.StripANSI(captured), message, ackPaneAgentType(pane))
			if !detected {
				stillPending = append(stillPending, paneKey)
				continue
			}
			now := time.Now().UTC()
			output.Confirmations = append(output.Confirmations, AckConfirmation{
				Pane: paneKey, AckType: string(ackType), AckAt: now.Format(time.RFC3339),
				LatencyMs: int(now.Sub(output.SentAt).Milliseconds()),
			})
		}
		output.Pending = stillPending
		if len(output.Pending) > 0 {
			waitAckPoll(ctx, pollInterval)
		}
	}
	finishAckOutput(ctx, output)
}

func finishAckOutput(ctx context.Context, output *AckOutput) {
	switch {
	case ctx.Err() != nil:
		output.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
		output.RobotResponse = NewErrorResponse(ctx.Err(), ErrCodeTimeout, "Acknowledgment tracking stopped; inspect pending and failed panes before retrying the send")
	case len(output.Failed) > 0 && output.Success:
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("acknowledgment could not be verified for %d pane(s)", len(output.Failed)),
			ErrCodeInternalError, "Inspect failed panes; the message may already have been delivered",
		)
	}
	output.CompletedAt = time.Now().UTC()
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

func shouldSkipDefaultAgentPane(pane tmux.Pane, agentType string) bool {
	if pane.Index == 0 && agentType == "unknown" {
		return true
	}
	return agentType == "user"
}

// resolveAckTargets picks the panes an ack watches: the explicit selectors
// when given, otherwise every agent pane; a --type list then narrows either
// set, as it does for --robot-send and --robot-interrupt.
func resolveAckTargets(panes []tmux.Pane, selectors, agentTypes []string) ([]tmux.Pane, error) {
	ordered := tmux.SortPanesByTopology(panes)
	if len(selectors) > 0 {
		resolved, err := tmux.ResolvePaneSelectors(ordered, selectors, false)
		if err != nil {
			return nil, err
		}
		return keepPanesOfAgentTypes(resolved, agentTypes, ackPaneAgentType), nil
	}
	targets := make([]tmux.Pane, 0, len(ordered))
	for _, pane := range ordered {
		if !shouldSkipDefaultAgentPane(pane, ackPaneAgentType(pane)) {
			targets = append(targets, pane)
		}
	}
	return keepPanesOfAgentTypes(targets, agentTypes, ackPaneAgentType), nil
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

	// A terminal echoes input before the agent reads it. Require new response
	// content after the entire message, including multiline and wrapped input.
	// Stripping only a prefix made the rest of the user's own prompt an ack.
	echoed := false
	for {
		afterEcho, found := getContentAfterEcho(newContent, message)
		if !found {
			break
		}
		echoed = true
		newContent = afterEcho
	}
	if echoed {
		// A submitted input is followed by the live composer and its footer
		// even when the agent has not responded. Neither is response evidence.
		newContent = status.AgentTranscript(newContent, agentType)
	}
	messageText, _ := compactAckEcho(status.StripANSI(message))
	baselineLines := make(map[string]bool)
	for _, line := range splitLines(initialOutput) {
		baselineLines[strings.TrimSpace(line)] = true
	}
	responseLines := make([]string, 0)
	for _, line := range splitLines(newContent) {
		line = strings.TrimSpace(line)
		if line == "" || status.IsPromptLine(line, agentType) || trimAckInputRow(line) == "" {
			continue
		}
		if !echoed {
			if baselineLines[line] {
				continue
			}
			// The capture may start partway through a long or still-rendering
			// prompt. Its visible rows are not evidence of a response either.
			row, _ := compactAckEcho(trimAckInputRow(line))
			if row != "" && strings.Contains(messageText, row) {
				continue
			}
		}
		responseLines = append(responseLines, line)
	}
	newContent = strings.Join(responseLines, "\n")
	if newContent == "" {
		return AckNone, false
	}
	if echoed {
		return AckEchoDetected, true
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
	if len(responseLines) >= 2 {
		return AckOutputStarted, true
	}

	// Check for prompt returned (agent finished and showing ready prompt)
	lastLines := getLastNonEmptyLines(currentOutput, 1)
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

	// A fixed-height capture can grow in bytes while its lines scroll. Always
	// remove the overlapping history before interpreting any new response.
	initialLines := splitLines(initial)
	currentLines := splitLines(current)
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
	if len(current) <= len(initial) {
		if len(currentLines) > len(initialLines) {
			return strings.Join(currentLines[len(initialLines):], "\n")
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

// compactAckEcho ignores whitespace introduced by terminal wrapping while
// retaining the original byte boundary after every compacted byte.
func compactAckEcho(text string) (string, []int) {
	var compact strings.Builder
	ends := make([]int, 0, len(text))
	for offset := 0; offset < len(text); {
		r, size := utf8.DecodeRuneInString(text[offset:])
		if !unicode.IsSpace(r) {
			compact.WriteString(text[offset : offset+size])
			for i := 0; i < size; i++ {
				ends = append(ends, offset+size)
			}
		}
		offset += size
	}
	return compact.String(), ends
}

// trimAckInputRow removes the framing a terminal may add around an input row.
// It is used only for matching known input, not for identifying response text.
func trimAckInputRow(row string) string {
	row = strings.TrimSpace(row)
	if strings.Trim(row, "─━╌┄┈╰╯└┘╭╮┌┐┏┓┗┛│┃| ") == "" {
		return ""
	}
	row = strings.TrimSpace(strings.Trim(row, "│┃|"))
	return strings.TrimSpace(strings.TrimLeft(row, ">❯›»$ "))
}

// getContentAfterEcho matches a complete input at the beginning of new content,
// allowing terminal wrapping and composer framing. A mention of the request
// inside a response is not an input echo. The bool distinguishes an echo with
// no response from no echo at all.
func getContentAfterEcho(content, message string) (string, bool) {
	msg, _ := compactAckEcho(status.StripANSI(message))
	if msg == "" {
		return "", false
	}
	// Try exact rows first so literal prompt/border glyphs in the user's
	// message are preserved, then allow the TUI's surrounding decorations.
	rows := strings.Split(status.StripANSI(content), "\n")
	for _, framed := range []bool{false, true} {
		matched := 0
		for i, row := range rows {
			row = strings.TrimSpace(row)
			if framed {
				row = trimAckInputRow(row)
			}
			compact, ends := compactAckEcho(row)
			if compact == "" {
				continue
			}
			remaining := msg[matched:]
			if strings.HasPrefix(compact, remaining) {
				after := []string{row[ends[len(remaining)-1]:]}
				after = append(after, rows[i+1:]...)
				return strings.TrimSpace(strings.Join(after, "\n")), true
			}
			if !strings.HasPrefix(remaining, compact) {
				break
			}
			matched += len(compact)
		}
	}
	return "", false
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
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opts.AckTimeoutMs <= 0 {
		opts.AckTimeoutMs = 30000
	}
	if opts.AckPollMs <= 0 {
		opts.AckPollMs = 500
	}
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

	exists, sessionErr := tmux.SessionExistsContext(ctx, opts.Session)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if sessionErr != nil || !exists {
		errorCode, hint := ErrCodeInternalError, "Check tmux session state"
		if sessionErr == nil {
			sessionErr = fmt.Errorf("session '%s' not found", opts.Session)
			errorCode, hint = ErrCodeSessionNotFound, "Use --robot-status to list available sessions"
		}
		errResp := NewErrorResponse(sessionErr, errorCode, hint)
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
				Failed:         []SendError{{Pane: "session", Error: sessionErr.Error()}},
				MessagePreview: preview,
			},
			Ack: AckOutput{
				RobotResponse: errResp,
				Session:       opts.Session,
				SentAt:        sentAt,
				CompletedAt:   time.Now().UTC(),
				Confirmations: []AckConfirmation{},
				Pending:       []string{},
				Failed:        []AckFailure{{Pane: "session", Reason: "send failed"}},
			},
		}), nil
	}

	panes, err := tmux.GetPanesContext(ctx, opts.Session)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
		memCfg, memoryInfo = prepareCMSendContext(ctx, opts.Session, targetPanes, memCfg)
		if memoryInfo == nil {
			opts.Message, memoryInfo = InjectCMRules(ctx, opts.Message, opts.Message, memCfg)
			// Feedback belongs to the exact daemon/session that supplied the
			// rules, even if PID files change while acknowledgment is pending.
			memCfg.Outcome = memoryInfo.outcome
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// A pane with no baseline is excluded from acknowledgment tracking below
	// rather than diffed against an empty string, which would return the whole
	// scrollback and confirm an ack the agent never gave.
	initialStates := make(map[string]string)

	for i, pane := range targetPanes {
		paneKey := targetKeys[i]
		// Capture initial state before sending
		captured, err := func() (string, error) {
			ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
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
	} else if guardSendCommand(ctx, &sendOutput, opts.Message, opts.Session, targetPanes, opts.DryRun) {
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
				ctx,
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
				result, dispatchErr := service.Dispatch(ctx, prepared)
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
				dispatchStarted := time.Now()
				result, _ := service.Dispatch(ctx, prepared)
				applyRobotDispatchResult(&sendOutput, result)
				recordRobotSendHistory(opts.SendOptions, redactCfg, targetPanes, targetKeys, sendOutput, dispatchStarted)
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
		Pending:       append([]string{}, sendOutput.Successful...),
		Failed:        []AckFailure{},
		TimeoutMs:     opts.AckTimeoutMs,
		TimedOut:      false,
	}

	targetByKey := make(map[string]tmux.Pane, len(targetPanes))
	for i, pane := range targetPanes {
		targetByKey[targetKeys[i]] = pane
	}
	ackCtx, ackCancel := context.WithTimeout(ctx, time.Duration(opts.AckTimeoutMs)*time.Millisecond)
	pollAcknowledgments(ackCtx, &ackOutput, targetByKey, initialStates, opts.Message, time.Duration(opts.AckPollMs)*time.Millisecond)
	ackCancel()
	publishSendActuationVerification(trace, opts, sendOutput, ackOutput)

	// Automatic outcome feedback (bd-3j6hm): only clear evidence is reported.
	// A confirmed acknowledgment with no panes left pending is a success for
	// the injected rules; an ack timeout is ambiguous and reports nothing.
	// reportCMOutcome is bounded and never fails the send.
	if ctx.Err() == nil && sendOutput.Success && ackOutput.Success && shouldReportCMSendOutcome(opts.WithMemory, memoryInfo, len(ackOutput.Confirmations), ackOutput.TimedOut) {
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

func waitAckPoll(ctx context.Context, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
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
