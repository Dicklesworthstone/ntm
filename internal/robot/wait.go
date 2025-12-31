// Package robot provides machine-readable output for AI agents and automation.
// wait.go implements the --robot-wait command for waiting on agent states.
package robot

import (
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// WaitOptions configures the robot wait operation.
type WaitOptions struct {
	Session      string
	Condition    string // idle, complete, generating, healthy
	Timeout      time.Duration
	PollInterval time.Duration
	PaneIndices  []int  // Empty = all panes
	AgentType    string // Empty = all types
	WaitForAny   bool   // If true, wait for ANY; otherwise wait for ALL
	ExitOnError  bool   // If true, exit immediately on ERROR state
	CountN       int    // With WaitForAny, wait for at least N agents (default 1)
}

// WaitResponse is the JSON output for --robot-wait.
type WaitResponse struct {
	RobotResponse
	Session       string          `json:"session"`
	Condition     string          `json:"condition"`
	WaitedSeconds float64         `json:"waited_seconds"`
	Agents        []WaitAgentInfo `json:"agents,omitempty"`
	AgentsPending []string        `json:"agents_pending,omitempty"`
}

// WaitAgentInfo describes an agent's state when the wait completed or timed out.
type WaitAgentInfo struct {
	Pane      string `json:"pane"`
	State     string `json:"state"`
	MetAt     string `json:"met_at,omitempty"` // RFC3339 timestamp
	AgentType string `json:"agent_type,omitempty"`
}

// Wait condition constants
const (
	WaitConditionIdle       = "idle"
	WaitConditionComplete   = "complete"
	WaitConditionGenerating = "generating"
	WaitConditionHealthy    = "healthy"
)

// CompleteIdleThreshold is the time without activity to consider "complete".
const CompleteIdleThreshold = 5 * time.Second

// PrintWait executes the wait operation and outputs JSON.
// Returns exit code: 0 = success, 1 = timeout, 2 = error, 3 = agent error
func PrintWait(opts WaitOptions) int {
	// Validate session exists
	if !tmux.SessionExists(opts.Session) {
		resp := WaitResponse{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("session '%s' not found", opts.Session),
				ErrCodeSessionNotFound,
				"Use 'ntm list' to see available sessions",
			),
			Session:   opts.Session,
			Condition: opts.Condition,
		}
		outputJSON(resp)
		return 2
	}

	// Validate condition
	if !isValidWaitCondition(opts.Condition) {
		resp := WaitResponse{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("invalid condition '%s'", opts.Condition),
				ErrCodeInvalidFlag,
				"Valid conditions: idle, complete, generating, healthy",
			),
			Session:   opts.Session,
			Condition: opts.Condition,
		}
		outputJSON(resp)
		return 2
	}

	// Set default count for --any mode
	if opts.WaitForAny && opts.CountN <= 0 {
		opts.CountN = 1
	}

	// Start waiting
	startTime := time.Now()
	deadline := startTime.Add(opts.Timeout)

	// Create activity monitor
	monitor := NewActivityMonitor(nil)

	for {
		// Check timeout
		if time.Now().After(deadline) {
			elapsed := time.Since(startTime)
			// Collect pending agents
			panes, _ := tmux.GetPanes(opts.Session)
			var pending []string
			for _, pane := range filterWaitPanes(panes, opts) {
				pending = append(pending, pane.ID)
			}
			resp := WaitResponse{
				RobotResponse: NewErrorResponse(
					fmt.Errorf("timeout after %v", opts.Timeout),
					ErrCodeTimeout,
					"Try increasing --wait-timeout or check agent status with --robot-activity",
				),
				Session:       opts.Session,
				Condition:     opts.Condition,
				WaitedSeconds: elapsed.Seconds(),
				AgentsPending: pending,
			}
			outputJSON(resp)
			return 1
		}

		// Get all panes
		panes, err := tmux.GetPanes(opts.Session)
		if err != nil {
			resp := WaitResponse{
				RobotResponse: NewErrorResponse(
					fmt.Errorf("failed to list panes: %w", err),
					ErrCodeInternalError,
					"",
				),
				Session:   opts.Session,
				Condition: opts.Condition,
			}
			outputJSON(resp)
			return 2
		}

		// Filter panes based on options
		filteredPanes := filterWaitPanes(panes, opts)

		if len(filteredPanes) == 0 {
			resp := WaitResponse{
				RobotResponse: NewErrorResponse(
					fmt.Errorf("no panes match the filter criteria"),
					ErrCodePaneNotFound,
					"Check --wait-panes and --wait-type filters",
				),
				Session:   opts.Session,
				Condition: opts.Condition,
			}
			outputJSON(resp)
			return 2
		}

		// Update activity state for each pane
		var activities []*AgentActivity
		for _, pane := range filteredPanes {
			classifier := monitor.GetOrCreate(pane.ID)
			// Set agent type if we can detect it from pane name
			if at := detectAgentTypeFromTitle(pane.Title); at != "" {
				classifier.SetAgentType(at)
			}
			activity, err := classifier.Classify()
			if err != nil {
				// Pane may have disappeared, continue
				continue
			}
			activities = append(activities, activity)
		}

		// Check for error state (if --exit-on-error)
		if opts.ExitOnError {
			for _, a := range activities {
				if a.State == StateError {
					elapsed := time.Since(startTime)
					resp := WaitResponse{
						RobotResponse: NewErrorResponse(
							fmt.Errorf("agent error detected in pane '%s'", a.PaneID),
							"AGENT_ERROR",
							"Check agent output with --robot-tail",
						),
						Session:       opts.Session,
						Condition:     opts.Condition,
						WaitedSeconds: elapsed.Seconds(),
						Agents: []WaitAgentInfo{{
							Pane:      a.PaneID,
							State:     string(a.State),
							AgentType: a.AgentType,
						}},
					}
					outputJSON(resp)
					return 3
				}
			}
		}

		// Check if condition is met
		met, matching, pending := checkWaitConditionMet(activities, opts)
		if met {
			elapsed := time.Since(startTime)
			resp := WaitResponse{
				RobotResponse: NewRobotResponse(true),
				Session:       opts.Session,
				Condition:     opts.Condition,
				WaitedSeconds: elapsed.Seconds(),
				Agents:        matching,
			}
			outputJSON(resp)
			return 0
		}

		// Store pending for potential timeout response
		_ = pending

		// Sleep and poll again
		time.Sleep(opts.PollInterval)
	}
}

// isValidWaitCondition checks if the condition string is valid.
func isValidWaitCondition(condition string) bool {
	// Handle composed conditions (comma-separated)
	parts := strings.Split(condition, ",")
	for _, part := range parts {
		p := strings.TrimSpace(part)
		switch p {
		case WaitConditionIdle, WaitConditionComplete, WaitConditionGenerating, WaitConditionHealthy:
			// Valid
		default:
			return false
		}
	}
	return len(parts) > 0
}

// filterWaitPanes filters panes based on wait options.
func filterWaitPanes(panes []tmux.Pane, opts WaitOptions) []tmux.Pane {
	var result []tmux.Pane

	// Build pane index set for quick lookup
	paneIndexSet := make(map[int]bool)
	for _, idx := range opts.PaneIndices {
		paneIndexSet[idx] = true
	}

	for _, pane := range panes {
		// Skip user pane (typically has no agent type indicator)
		agentType := detectAgentTypeFromTitle(pane.Title)
		if agentType == "" && pane.Index == 0 {
			continue
		}

		// Filter by specific pane indices
		if len(opts.PaneIndices) > 0 && !paneIndexSet[pane.Index] {
			continue
		}

		// Filter by agent type
		if opts.AgentType != "" {
			if !strings.EqualFold(agentType, opts.AgentType) {
				continue
			}
		}

		result = append(result, pane)
	}

	return result
}

// detectAgentTypeFromTitle extracts the agent type from a pane title.
// Pane titles follow the pattern: <session>__<type>_<index>
func detectAgentTypeFromTitle(title string) string {
	parts := strings.Split(title, "__")
	if len(parts) < 2 {
		return ""
	}
	typePart := parts[1]
	// Extract type before underscore and number
	for i, c := range typePart {
		if c == '_' {
			return typePart[:i]
		}
	}
	return typePart
}

// checkWaitConditionMet checks if the wait condition is satisfied.
// Returns: met (bool), matching agents, pending agents
func checkWaitConditionMet(activities []*AgentActivity, opts WaitOptions) (bool, []WaitAgentInfo, []string) {
	if len(activities) == 0 {
		return false, nil, nil
	}

	// Parse composed conditions
	conditions := strings.Split(opts.Condition, ",")

	var matchingAgents []WaitAgentInfo
	var pendingAgents []string

	now := time.Now()

	for _, activity := range activities {
		if meetsAllWaitConditions(activity, conditions) {
			matchingAgents = append(matchingAgents, WaitAgentInfo{
				Pane:      activity.PaneID,
				State:     string(activity.State),
				MetAt:     FormatTimestamp(now),
				AgentType: activity.AgentType,
			})
		} else {
			pendingAgents = append(pendingAgents, activity.PaneID)
		}
	}

	// Determine if condition is met based on --any vs ALL
	if opts.WaitForAny {
		// With --any, need at least CountN agents matching
		return len(matchingAgents) >= opts.CountN, matchingAgents, pendingAgents
	}

	// Default: ALL agents must match (no pending)
	return len(pendingAgents) == 0 && len(matchingAgents) > 0, matchingAgents, pendingAgents
}

// meetsAllWaitConditions checks if an activity meets all specified conditions.
func meetsAllWaitConditions(activity *AgentActivity, conditions []string) bool {
	for _, cond := range conditions {
		c := strings.TrimSpace(cond)
		if !meetsSingleWaitCondition(activity, c) {
			return false
		}
	}
	return true
}

// meetsSingleWaitCondition checks if an activity meets a single condition.
func meetsSingleWaitCondition(activity *AgentActivity, condition string) bool {
	switch condition {
	case WaitConditionIdle:
		return activity.State == StateWaiting

	case WaitConditionComplete:
		// Must be waiting AND no recent output
		if activity.State != StateWaiting {
			return false
		}
		// Check last output time - must be older than threshold
		if activity.LastOutput.IsZero() {
			return true // No output recorded = complete
		}
		return time.Since(activity.LastOutput) >= CompleteIdleThreshold

	case WaitConditionGenerating:
		return activity.State == StateGenerating

	case WaitConditionHealthy:
		// Not ERROR and not STALLED
		return activity.State != StateError && activity.State != StateStalled

	default:
		return false
	}
}
