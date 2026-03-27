package status

import (
	"context"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// UnifiedDetector implements the Detector interface by combining
// activity, prompt, and error detection into a unified status check.
type UnifiedDetector struct {
	config DetectorConfig
}

// NewDetector creates a new UnifiedDetector with default configuration
func NewDetector() *UnifiedDetector {
	return &UnifiedDetector{
		config: DefaultConfig(),
	}
}

// NewDetectorWithConfig creates a new UnifiedDetector with custom configuration
func NewDetectorWithConfig(config DetectorConfig) *UnifiedDetector {
	return &UnifiedDetector{
		config: config,
	}
}

// Config returns the current detector configuration
func (d *UnifiedDetector) Config() DetectorConfig {
	return d.config
}

// Analyze determines status from provided output and metadata without calling tmux.
// This allows reusing output captured for other purposes (e.g. live view).
func (d *UnifiedDetector) Analyze(paneID, paneName, agentType string, output string, lastActivity time.Time) AgentStatus {
	status := AgentStatus{
		PaneID:     paneID,
		PaneName:   paneName,
		AgentType:  agentType,
		LastActive: lastActivity,
		UpdatedAt:  time.Now(),
		State:      StateUnknown,
		LastOutput: truncateOutput(output, d.config.OutputPreviewLength),
	}

	state, errType := d.determineState(output, agentType, lastActivity)
	status.State = state
	status.ErrorType = errType

	// Extract metrics using agent parser
	if isKnownAgentType(agentType) {
		parser := agent.NewParser()
		if parsed, err := parser.ParseWithHint(output, agent.AgentType(agentType)); err == nil {
			if parsed.ContextRemaining != nil {
				status.ContextUsage = 100.0 - *parsed.ContextRemaining
			}
			if parsed.TokensUsed != nil {
				status.TokensUsed = *parsed.TokensUsed
			}
		}
	}

	return status
}

// determineState calculates state based on output and activity
func (d *UnifiedDetector) determineState(output, agentType string, lastActivity time.Time) (AgentState, ErrorType) {
	// Detection priority:
	// 1. Check for idle prompt when velocity is low (agent waiting for input)
	// 2. Check for errors (but only if not clearly at a prompt)
	// 3. Check activity recency (working vs unknown)
	// 4. Heuristic check for likely-idle state
	//
	// Key insight: if an agent is showing an idle prompt and not actively outputting,
	// it should be classified as WAITING regardless of historical error messages
	// in the scrollback. Error patterns from earlier in the session are not relevant
	// when the agent has clearly recovered and is now waiting for input.

	threshold := time.Duration(d.config.ActivityThreshold) * time.Second
	isLowVelocity := time.Since(lastActivity) >= threshold

	// Check if at prompt (idle) - prioritize this when velocity is low
	isAtPrompt := DetectIdleFromOutput(output, agentType)
	if isAtPrompt && isLowVelocity {
		// Agent is at prompt and not actively outputting - clearly idle
		return StateIdle, ErrorNone
	}

	// Check for errors (only relevant if not clearly at a prompt waiting for input)
	if errType := DetectErrorInOutput(output); errType != ErrorNone {
		return StateError, errType
	}

	// If at prompt but with recent activity, trust the activity signal —
	// agents often print prompt-like characters (>, ❯) as part of active
	// output (code examples, progress indicators, partial lines). Only
	// classify as idle when velocity is genuinely low (handled above).
	// Heuristic: for user panes with empty output, treat as idle
	if agentType == "" || agentType == "user" {
		if strings.TrimSpace(output) == "" {
			return StateIdle, ErrorNone
		}
	}

	// Check recent activity (reuse isLowVelocity computed earlier)
	if !isLowVelocity {
		return StateWorking, ErrorNone
	}

	// At prompt with no recent activity but didn't pass the isLowVelocity
	// check above — this means activity threshold was borderline. Trust
	// the prompt detection in this case since velocity has settled.
	if isAtPrompt {
		return StateIdle, ErrorNone
	}

	// Heuristic: if no recent activity and output suggests agent is waiting,
	// prefer idle over unknown. This catches cases where:
	// - The prompt pattern isn't recognized but the agent is clearly done
	// - The last line is short (typical of prompts)
	// - The output ends without indication of ongoing work
	if looksLikeIdle(output) {
		return StateIdle, ErrorNone
	}

	// For known AI agent types (cc, cod, gmi) with no recent activity and no
	// prompt detected, use the looksLikeIdle heuristic above. If that didn't
	// match either, default to unknown rather than falsely reporting idle —
	// false idle readings cause operators to send redundant prompts and miss
	// that agents are actually working.
	return StateUnknown, ErrorNone
}

// isKnownAgentType returns true for AI agent types that have predictable
// working/idle behavior (cc=Claude Code, cod=Codex, gmi=Gemini).
func isKnownAgentType(agentType string) bool {
	switch agentType {
	case string(agent.AgentTypeClaudeCode),
		string(agent.AgentTypeCodex),
		string(agent.AgentTypeGemini),
		"cursor", "windsurf", "aider":
		return true
	default:
		return false
	}
}

// looksLikeIdle applies heuristics to detect likely idle state when
// explicit prompt patterns don't match. This reduces false "unknown" states.
func looksLikeIdle(output string) bool {
	clean := StripANSI(output)
	clean = strings.TrimSpace(clean)

	if clean == "" {
		return false
	}

	lines := strings.Split(clean, "\n")
	if len(lines) == 0 {
		return false
	}

	// Check the last non-empty line
	var lastLine string
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			lastLine = line
			break
		}
	}

	if lastLine == "" {
		return false
	}

	// Heuristic 1: Last line is very short (< 20 chars) - likely a prompt
	if len(lastLine) < 20 {
		return true
	}

	// Heuristic 2: Last line ends with common prompt characters
	promptEndings := []string{">", "$", "%", ":", "❯", "→", "»", "#"}
	for _, ending := range promptEndings {
		if strings.HasSuffix(lastLine, ending) || strings.HasSuffix(lastLine, ending+" ") {
			return true
		}
	}

	// Heuristic 3: Last line contains common "done" indicators
	doneIndicators := []string{
		"completed",
		"finished",
		"done",
		"ready",
		"success",
	}
	lowerLine := strings.ToLower(lastLine)
	for _, indicator := range doneIndicators {
		if strings.Contains(lowerLine, indicator) {
			return true
		}
	}

	return false
}

// Detect returns the current status of a single pane.
// Detection priority: error > idle > working > unknown
func (d *UnifiedDetector) Detect(paneID string) (AgentStatus, error) {
	status := AgentStatus{
		PaneID:    paneID,
		UpdatedAt: time.Now(),
		State:     StateUnknown,
	}

	// Get pane activity time
	lastActivity, err := tmux.GetPaneActivity(paneID)
	if err != nil {
		return status, err
	}
	status.LastActive = lastActivity

	// Capture recent output for analysis
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := tmux.CapturePaneOutputContext(ctx, paneID, d.config.ScanLines)
	if err != nil {
		return status, err
	}
	if strings.TrimSpace(output) == "" {
		// Give tmux a brief moment to flush output, then retry once
		time.Sleep(100 * time.Millisecond)
		ctxRetry, cancelRetry := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelRetry()
		if retry, err := tmux.CapturePaneOutputContext(ctxRetry, paneID, d.config.ScanLines); err == nil {
			output = retry
		}
	}
	status.LastOutput = truncateOutput(output, d.config.OutputPreviewLength)

	// Try to get pane details for agent type detection
	// We'll parse the pane title from output if needed
	// Use paneID as target - tmux list-panes -s -t paneID lists all panes in that pane's session
	panes, _ := tmux.GetPanesWithActivity(paneID)
	for _, p := range panes {
		if p.Pane.ID == paneID {
			status.PaneName = p.Pane.Title
			status.AgentType = string(p.Pane.Type)
			break
		}
	}

	// Use shared logic
	state, errType := d.determineState(output, status.AgentType, status.LastActive)
	status.State = state
	status.ErrorType = errType

	// Extract metrics using agent parser
	if isKnownAgentType(status.AgentType) {
		parser := agent.NewParser()
		if parsed, err := parser.ParseWithHint(output, agent.AgentType(status.AgentType)); err == nil {
			if parsed.ContextRemaining != nil {
				status.ContextUsage = 100.0 - *parsed.ContextRemaining
			}
			if parsed.TokensUsed != nil {
				status.TokensUsed = *parsed.TokensUsed
			}
		}
	}

	return status, nil
}

// DetectAll returns status for all panes in a session.
// Errors on individual panes don't fail the entire operation.
func (d *UnifiedDetector) DetectAll(session string) ([]AgentStatus, error) {
	return d.DetectAllContext(context.Background(), session)
}

// DetectAllContext returns status for all panes in a session with cancellation support.
// Errors on individual panes don't fail the entire operation.
func (d *UnifiedDetector) DetectAllContext(ctx context.Context, session string) ([]AgentStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	panes, err := tmux.GetPanesWithActivityContext(ctx, session)
	if err != nil {
		return nil, err
	}

	// Capture outputs in parallel
	type captureResult struct {
		paneID string
		output string
		err    error
	}

	resultsCh := make(chan captureResult, len(panes))

	// Create a worker pool or just spawn per pane (assuming pane count is reasonable < 50)
	// For simplicity and typical tmux usage, one goroutine per pane is fine.
	for _, pane := range panes {
		go func(pID string) {
			// Use a short timeout for individual captures to avoid hanging the whole batch
			capCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()

			output, err := tmux.CapturePaneOutputContext(capCtx, pID, d.config.ScanLines)
			resultsCh <- captureResult{paneID: pID, output: output, err: err}
		}(pane.Pane.ID)
	}

	// Collect results
	outputs := make(map[string]string)
	for i := 0; i < len(panes); i++ {
		select {
		case res := <-resultsCh:
			if res.err == nil {
				outputs[res.paneID] = res.output
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	statuses := make([]AgentStatus, 0, len(panes))
	for _, pane := range panes {
		status := AgentStatus{
			PaneID:     pane.Pane.ID,
			PaneName:   pane.Pane.Title,
			AgentType:  string(pane.Pane.Type),
			LastActive: pane.LastActivity,
			UpdatedAt:  time.Now(),
			State:      StateUnknown,
		}

		output, ok := outputs[pane.Pane.ID]
		if !ok {
			// Capture failed or timed out, keep unknown state but preserve metadata
			statuses = append(statuses, status)
			continue
		}

		status.LastOutput = truncateOutput(output, d.config.OutputPreviewLength)

		// Use shared logic
		state, errType := d.determineState(output, status.AgentType, status.LastActive)
		status.State = state
		status.ErrorType = errType

		// Extract metrics using agent parser
		if isKnownAgentType(status.AgentType) {
			parser := agent.NewParser()
			if parsed, err := parser.ParseWithHint(output, agent.AgentType(status.AgentType)); err == nil {
				if parsed.ContextRemaining != nil {
					status.ContextUsage = 100.0 - *parsed.ContextRemaining
				}
				if parsed.TokensUsed != nil {
					status.TokensUsed = *parsed.TokensUsed
				}
			}
		}

		statuses = append(statuses, status)
	}

	return statuses, nil
}

// truncateOutput returns the last n bytes of output, respecting UTF-8 boundaries.
// If maxLen falls in the middle of a multi-byte rune, it advances to the next
// valid rune boundary to avoid producing invalid UTF-8.
func truncateOutput(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}

	// Start position for the tail
	start := len(s) - maxLen

	// If start is in the middle of a UTF-8 rune, advance to the next rune boundary.
	// UTF-8 continuation bytes have the form 10xxxxxx (0x80-0xBF).
	// We need to find the next byte that is NOT a continuation byte.
	for start < len(s) && s[start]&0xC0 == 0x80 {
		start++
	}

	return s[start:]
}

// GetStateSummary returns a summary of states for a set of statuses
func GetStateSummary(statuses []AgentStatus) map[AgentState]int {
	summary := make(map[AgentState]int)
	for _, s := range statuses {
		summary[s.State]++
	}
	return summary
}

// FilterByState returns only statuses matching the given state
func FilterByState(statuses []AgentStatus, state AgentState) []AgentStatus {
	var filtered []AgentStatus
	for _, s := range statuses {
		if s.State == state {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

// FilterByAgentType returns only statuses for the given agent type
func FilterByAgentType(statuses []AgentStatus, agentType string) []AgentStatus {
	var filtered []AgentStatus
	for _, s := range statuses {
		if s.AgentType == agentType {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

// HasErrors returns true if any status is in error state
func HasErrors(statuses []AgentStatus) bool {
	for _, s := range statuses {
		if s.State == StateError {
			return true
		}
	}
	return false
}

// AllHealthy returns true if all statuses are healthy (idle or working)
func AllHealthy(statuses []AgentStatus) bool {
	for _, s := range statuses {
		if !s.IsHealthy() {
			return false
		}
	}
	return len(statuses) > 0
}
