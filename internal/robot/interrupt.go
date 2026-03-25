// Package robot provides machine-readable output for AI agents.
// interrupt.go contains the --robot-interrupt flag implementation for priority course correction.
package robot

import (
	"fmt"
	"strings"
	"time"

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
	ReadyForInput  []string             `json:"ready_for_input"`
	Failed         []InterruptError     `json:"failed"`
	TimeoutMs      int                  `json:"timeout_ms"`
	TimedOut       bool                 `json:"timed_out"`
	DryRun         bool                 `json:"dry_run,omitempty"`
	WouldAffect    []string             `json:"would_affect,omitempty"`
}

// PaneState captures the state of a pane before interruption
type PaneState struct {
	State      string `json:"state"`       // active, idle, error, unknown
	LastOutput string `json:"last_output"` // Truncated last output (for context)
	AgentType  string `json:"agent_type"`  // claude, codex, gemini, user, unknown
}

// InterruptError represents a failed interrupt attempt
type InterruptError struct {
	Pane   string `json:"pane"`
	Reason string `json:"reason"`
}

// InterruptOptions configures the PrintInterrupt operation
type InterruptOptions struct {
	Session         string   // Target session name
	Message         string   // Message to send after interrupt (optional)
	Panes           []string // Specific pane indices to interrupt (empty = all agents)
	All             bool     // Include all panes (including user)
	Force           bool     // Send Ctrl+C even if agent appears idle
	NoWait          bool     // Don't wait for ready state after interrupt
	TimeoutMs       int      // Timeout for waiting for ready state (default 10000)
	PollMs          int      // Poll interval (default 300)
	DryRun          bool     // Preview mode: show what would happen without executing
	RequestID       string   // External request identifier for REST parity
	CorrelationID   string   // Correlation identifier for tracing request/outcome/verification
	IdempotencyKey  string   // Idempotency key when provided by an upstream caller
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

	if opts.Message != "" {
		output.Method = "ctrl_c_then_send"
		output.Message = truncateMessage(opts.Message)
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

	panes, err := tmux.GetPanes(opts.Session)
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

	// Build pane filter map
	paneFilterMap := make(map[string]bool)
	for _, p := range opts.Panes {
		paneFilterMap[p] = true
	}
	hasPaneFilter := len(paneFilterMap) > 0

	// Determine which panes to interrupt
	var targetPanes []tmux.Pane
	targetKeys := []string{}
	for _, pane := range panes {
		paneKey := fmt.Sprintf("%d", pane.Index)

		// Check specific pane filter
		if hasPaneFilter && !paneFilterMap[paneKey] && !paneFilterMap[pane.ID] {
			continue
		}

		// Skip user panes by default unless --all or specific pane filter
		if !opts.All && !hasPaneFilter {
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
	}

	if len(targetPanes) == 0 {
		output.CompletedAt = time.Now().UTC()
		return finalizeTerminalInterruptActuation(trace, opts, targetKeys, output), nil
	}

	// Capture previous state for each pane before interrupting
	for _, pane := range targetPanes {
		paneKey := fmt.Sprintf("%d", pane.Index)

		captured, err := tmux.CapturePaneOutput(pane.ID, 20)
		if err != nil {
			output.PreviousStates[paneKey] = PaneState{
				State:      "unknown",
				LastOutput: "",
				AgentType:  detectAgentType(pane.Title),
			}
			continue
		}

		cleanOutput := stripANSI(captured)
		lines := splitLines(cleanOutput)
		agentType := detectAgentType(pane.Title)
		state := determineState(captured, agentType)

		// Get last meaningful output (truncated)
		shortAgentType := translateAgentTypeForStatus(agentType)
		lastOutput := getLastMeaningfulOutput(lines, 200, shortAgentType)

		output.PreviousStates[paneKey] = PaneState{
			State:      state,
			LastOutput: lastOutput,
			AgentType:  agentType,
		}
	}

	// Dry-run mode: show what would happen without executing
	if opts.DryRun {
		output.DryRun = true
		for _, pane := range targetPanes {
			paneKey := fmt.Sprintf("%d", pane.Index)
			output.WouldAffect = append(output.WouldAffect, paneKey)
		}
		output.CompletedAt = time.Now().UTC()
		return output, nil
	}

	publishInterruptActuationRequest(trace, opts, targetKeys)

	// Send Ctrl+C to all targets
	for _, pane := range targetPanes {
		paneKey := fmt.Sprintf("%d", pane.Index)
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
					if fmt.Sprintf("%d", targetPanes[i].Index) == paneKey {
						targetPane = &targetPanes[i]
						break
					}
				}

				if targetPane == nil {
					delete(pending, paneKey)
					continue
				}

				// Check if agent is ready
				captured, err := tmux.CapturePaneOutput(targetPane.ID, 10)
				if err != nil {
					continue
				}

				agentType := detectAgentType(targetPane.Title)
				state := determineState(captured, agentType)

				if state == "idle" {
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
			// Still add them to ready_for_input since Ctrl+C was sent
			for paneKey := range pending {
				output.ReadyForInput = append(output.ReadyForInput, paneKey)
			}
		}
	} else if opts.NoWait {
		// If no wait, all interrupted panes are considered ready
		output.ReadyForInput = output.Interrupted
	}

	// Send follow-up message if provided
	if opts.Message != "" && len(output.ReadyForInput) > 0 {
		// Small delay to ensure interrupt settled
		time.Sleep(100 * time.Millisecond)

		for _, paneKey := range output.ReadyForInput {
			// Find the pane
			var targetPane *tmux.Pane
			for i := range targetPanes {
				if fmt.Sprintf("%d", targetPanes[i].Index) == paneKey {
					targetPane = &targetPanes[i]
					break
				}
			}

			if targetPane != nil {
				err := tmux.SendKeys(targetPane.ID, opts.Message, true)
				if err != nil {
					output.Failed = append(output.Failed, InterruptError{
						Pane:   paneKey,
						Reason: fmt.Sprintf("failed to send message: %v", err),
					})
				}
			}
		}

		if len(output.Failed) < len(output.ReadyForInput) {
			output.MessageSent = true
		}
	}

	publishInterruptActuationOutcome(trace, opts, targetKeys, output)
	publishInterruptActuationVerification(trace, opts, targetKeys, output)
	output.CompletedAt = time.Now().UTC()
	return output, nil
}

// PrintInterrupt sends Ctrl+C to panes and optionally a follow-up message.
// This is a thin wrapper around GetInterrupt() for CLI output.
func PrintInterrupt(opts InterruptOptions) error {
	output, err := GetInterrupt(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
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
