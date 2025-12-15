// Package robot provides machine-readable output for AI agents.
// ack.go contains the --robot-ack flag implementation for send confirmation tracking.
package robot

import (
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// AckOutput is the structured output for --robot-ack
type AckOutput struct {
	Session       string            `json:"session"`
	SentAt        time.Time         `json:"sent_at"`
	CompletedAt   time.Time         `json:"completed_at"`
	Confirmations []AckConfirmation `json:"confirmations"`
	Pending       []string          `json:"pending"`
	Failed        []AckFailure      `json:"failed"`
	TimeoutMs     int               `json:"timeout_ms"`
	TimedOut      bool              `json:"timed_out"`
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

// AckOptions configures the PrintAck operation
type AckOptions struct {
	Session   string   // Target session name
	Message   string   // The message that was sent (for echo detection)
	Panes     []string // Specific pane indices to monitor
	TimeoutMs int      // How long to wait for acknowledgments (default 30000)
	PollMs    int      // How often to poll for changes (default 500)
}

// PrintAck monitors panes for acknowledgment after a send operation
func PrintAck(opts AckOptions) error {
	if opts.TimeoutMs <= 0 {
		opts.TimeoutMs = 30000 // Default 30s timeout
	}
	if opts.PollMs <= 0 {
		opts.PollMs = 500 // Default 500ms poll interval
	}

	sentAt := time.Now().UTC()
	output := AckOutput{
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
		output.CompletedAt = time.Now().UTC()
		return encodeJSON(output)
	}

	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		output.Failed = append(output.Failed, AckFailure{
			Pane:   "panes",
			Reason: fmt.Sprintf("failed to get panes: %v", err),
		})
		output.CompletedAt = time.Now().UTC()
		return encodeJSON(output)
	}

	// Build pane filter map
	paneFilterMap := make(map[string]bool)
	for _, p := range opts.Panes {
		paneFilterMap[p] = true
	}
	hasPaneFilter := len(paneFilterMap) > 0

	// Determine which panes to monitor
	var targetPanes []tmux.Pane
	for _, pane := range panes {
		paneKey := fmt.Sprintf("%d", pane.Index)

		// Check specific pane filter
		if hasPaneFilter && !paneFilterMap[paneKey] && !paneFilterMap[pane.ID] {
			continue
		}

		// Skip user panes by default if no filter
		if !hasPaneFilter {
			agentType := detectAgentType(pane.Title)
			if pane.Index == 0 && agentType == "unknown" {
				continue
			}
			if agentType == "user" {
				continue
			}
		}

		targetPanes = append(targetPanes, pane)
		output.Pending = append(output.Pending, paneKey)
	}

	if len(targetPanes) == 0 {
		output.CompletedAt = time.Now().UTC()
		return encodeJSON(output)
	}

	// Capture initial state of each pane
	initialStates := make(map[string]string)
	for _, pane := range targetPanes {
		paneKey := fmt.Sprintf("%d", pane.Index)
		captured, err := tmux.CapturePaneOutput(pane.ID, 20)
		if err == nil {
			initialStates[paneKey] = stripANSI(captured)
		}
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
			// Find the pane
			var targetPane *tmux.Pane
			for i := range targetPanes {
				if fmt.Sprintf("%d", targetPanes[i].Index) == paneKey {
					targetPane = &targetPanes[i]
					break
				}
			}

			if targetPane == nil {
				stillPending = append(stillPending, paneKey)
				continue
			}

			// Capture current output
			captured, err := tmux.CapturePaneOutput(targetPane.ID, 20)
			if err != nil {
				stillPending = append(stillPending, paneKey)
				continue
			}

			currentOutput := stripANSI(captured)
			initialOutput := initialStates[paneKey]

			// Check for acknowledgment
			ackType, detected := detectAcknowledgment(initialOutput, currentOutput, opts.Message, targetPane.Title)

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
	}

	output.CompletedAt = time.Now().UTC()
	return encodeJSON(output)
}

// detectAcknowledgment checks if the agent has acknowledged the input
func detectAcknowledgment(initialOutput, currentOutput, message, paneTitle string) (AckType, bool) {
	// No change means no ack yet
	if initialOutput == currentOutput {
		return AckNone, false
	}

	// Get the new content (what was added)
	newContent := getNewContent(initialOutput, currentOutput)
	if newContent == "" {
		return AckNone, false
	}

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
		if trimmed != "" && !isPromptLine(trimmed, paneTitle) {
			nonEmptyLines++
		}
	}
	if nonEmptyLines >= 2 {
		return AckOutputStarted, true
	}

	// Check for prompt returned (agent finished and showing ready prompt)
	lastLines := getLastNonEmptyLines(currentOutput, 3)
	for _, line := range lastLines {
		if isIdlePrompt(line) {
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
	if len(current) <= len(initial) {
		// Content might have been replaced, compare end
		initialLines := splitLines(initial)
		currentLines := splitLines(current)

		if len(currentLines) <= len(initialLines) {
			return ""
		}

		// Return new lines
		newLines := currentLines[len(initialLines):]
		return strings.Join(newLines, "\n")
	}

	// Simple case: content appended
	if strings.HasPrefix(current, initial) {
		return current[len(initial):]
	}

	// Content changed - find common prefix and return rest
	for i := 0; i < len(initial) && i < len(current); i++ {
		if initial[i] != current[i] {
			return current[i:]
		}
	}

	return current[len(initial):]
}

// truncateForMatch truncates message to first line or first 50 chars for matching
func truncateForMatch(message string) string {
	lines := strings.SplitN(message, "\n", 2)
	msg := lines[0]
	if len(msg) > 50 {
		msg = msg[:50]
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

// isPromptLine checks if a line looks like a prompt
func isPromptLine(line, paneTitle string) bool {
	promptSuffixes := []string{
		"> ", "$ ", "% ", "# ", "> ", ">>> ",
		"claude>", "codex>", "gemini>",
	}
	for _, suffix := range promptSuffixes {
		if strings.HasSuffix(line, suffix) {
			return true
		}
		if strings.TrimSpace(line) == strings.TrimSpace(suffix) {
			return true
		}
	}
	return false
}

// isIdlePrompt checks if a line is an idle/ready prompt
func isIdlePrompt(line string) bool {
	line = strings.TrimSpace(line)
	idlePrompts := []string{
		"> ", "$ ", "% ", "# ",
		"claude>", "claude >", "Claude>",
		"codex>", "codex >", "Codex>",
		"gemini>", "gemini >", "Gemini>",
		">>> ",
	}
	for _, prompt := range idlePrompts {
		if line == strings.TrimSpace(prompt) || strings.HasSuffix(line, prompt) {
			return true
		}
	}
	return false
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
	Send SendOutput `json:"send"`
	Ack  AckOutput  `json:"ack"`
}

// PrintSendAndAck sends a message and waits for acknowledgment
func PrintSendAndAck(opts SendAndAckOptions) error {
	// First, send the message
	sentAt := time.Now().UTC()

	if !tmux.SessionExists(opts.Session) {
		return encodeJSON(SendAndAckOutput{
			Send: SendOutput{
				Session:        opts.Session,
				SentAt:         sentAt,
				Targets:        []string{},
				Successful:     []string{},
				Failed:         []SendError{{Pane: "session", Error: fmt.Sprintf("session '%s' not found", opts.Session)}},
				MessagePreview: truncateMessage(opts.Message),
			},
			Ack: AckOutput{
				Session:       opts.Session,
				SentAt:        sentAt,
				CompletedAt:   time.Now().UTC(),
				Confirmations: []AckConfirmation{},
				Pending:       []string{},
				Failed:        []AckFailure{{Pane: "session", Reason: "send failed"}},
			},
		})
	}

	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		return encodeJSON(SendAndAckOutput{
			Send: SendOutput{
				Session:        opts.Session,
				SentAt:         sentAt,
				Targets:        []string{},
				Successful:     []string{},
				Failed:         []SendError{{Pane: "panes", Error: fmt.Sprintf("failed to get panes: %v", err)}},
				MessagePreview: truncateMessage(opts.Message),
			},
			Ack: AckOutput{
				Session:       opts.Session,
				SentAt:        sentAt,
				CompletedAt:   time.Now().UTC(),
				Confirmations: []AckConfirmation{},
				Pending:       []string{},
				Failed:        []AckFailure{{Pane: "panes", Reason: "failed to get panes"}},
			},
		})
	}

	// Build exclusion map
	excludeMap := make(map[string]bool)
	for _, e := range opts.Exclude {
		excludeMap[e] = true
	}

	// Build pane filter map
	paneFilterMap := make(map[string]bool)
	for _, p := range opts.Panes {
		paneFilterMap[p] = true
	}
	hasPaneFilter := len(paneFilterMap) > 0

	// Build agent type filter map (resolves aliases to canonical form)
	typeFilterMap := make(map[string]bool)
	for _, t := range opts.AgentTypes {
		typeFilterMap[ResolveAgentType(t)] = true
	}
	hasTypeFilter := len(typeFilterMap) > 0

	// Determine target panes and capture initial state
	var targetPanes []tmux.Pane
	var targetKeys []string
	initialStates := make(map[string]string)

	for _, pane := range panes {
		paneKey := fmt.Sprintf("%d", pane.Index)

		if excludeMap[paneKey] || excludeMap[pane.ID] {
			continue
		}

		if hasPaneFilter && !paneFilterMap[paneKey] && !paneFilterMap[pane.ID] {
			continue
		}

		if hasTypeFilter {
			agentType := detectAgentType(pane.Title)
			if !typeFilterMap[agentType] {
				continue
			}
		}

		if !opts.All && !hasPaneFilter && !hasTypeFilter {
			agentType := detectAgentType(pane.Title)
			if pane.Index == 0 && agentType == "unknown" {
				continue
			}
			if agentType == "user" {
				continue
			}
		}

		targetPanes = append(targetPanes, pane)
		targetKeys = append(targetKeys, paneKey)

		// Capture initial state before sending
		captured, err := tmux.CapturePaneOutput(pane.ID, 20)
		if err == nil {
			initialStates[paneKey] = stripANSI(captured)
		}
	}

	sendOutput := SendOutput{
		Session:        opts.Session,
		SentAt:         sentAt,
		Targets:        targetKeys,
		Successful:     []string{},
		Failed:         []SendError{},
		MessagePreview: truncateMessage(opts.Message),
	}

	// Send to all targets
	for i, pane := range targetPanes {
		paneKey := fmt.Sprintf("%d", pane.Index)

		if i > 0 && opts.DelayMs > 0 {
			time.Sleep(time.Duration(opts.DelayMs) * time.Millisecond)
		}

		err := tmux.SendKeys(pane.ID, opts.Message, true)
		if err != nil {
			sendOutput.Failed = append(sendOutput.Failed, SendError{
				Pane:  paneKey,
				Error: err.Error(),
			})
		} else {
			sendOutput.Successful = append(sendOutput.Successful, paneKey)
		}
	}

	// Now wait for acknowledgments (only for successful sends)
	ackOutput := AckOutput{
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
				if fmt.Sprintf("%d", targetPanes[i].Index) == paneKey {
					targetPane = &targetPanes[i]
					break
				}
			}

			if targetPane == nil {
				stillPending = append(stillPending, paneKey)
				continue
			}

			captured, err := tmux.CapturePaneOutput(targetPane.ID, 20)
			if err != nil {
				stillPending = append(stillPending, paneKey)
				continue
			}

			currentOutput := stripANSI(captured)
			initialOutput := initialStates[paneKey]

			ackType, detected := detectAcknowledgment(initialOutput, currentOutput, opts.Message, targetPane.Title)

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
	}

	ackOutput.CompletedAt = time.Now().UTC()

	return encodeJSON(SendAndAckOutput{
		Send: sendOutput,
		Ack:  ackOutput,
	})
}
