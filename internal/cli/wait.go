package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// WaitCondition represents a condition to wait for.
type WaitCondition string

const (
	// ConditionIdle waits for agent(s) in WAITING state.
	ConditionIdle WaitCondition = "idle"

	// ConditionComplete waits for all agents idle AND no recent activity.
	ConditionComplete WaitCondition = "complete"

	// ConditionGenerating waits for at least one agent actively generating.
	ConditionGenerating WaitCondition = "generating"

	// ConditionHealthy waits for all agents in healthy state (not ERROR/STALLED).
	ConditionHealthy WaitCondition = "healthy"
)

// WaitOptions configures the wait operation.
type WaitOptions struct {
	Session      string
	Condition    WaitCondition
	Timeout      time.Duration
	PollInterval time.Duration
	PaneIndex    int  // -1 means all panes
	AgentType    string
	WaitForAny   bool // If true, wait for ANY agent; otherwise wait for ALL
	ExitOnError  bool // If true, exit immediately on ERROR state
	CountN       int  // With --any, wait for at least N agents (default 1)
}

// WaitResult is the JSON output for robot mode.
type WaitResult struct {
	Success       bool            `json:"success"`
	Timestamp     string          `json:"timestamp"`
	Session       string          `json:"session"`
	Condition     string          `json:"condition"`
	WaitedSeconds float64         `json:"waited_seconds"`
	Agents        []WaitAgentInfo `json:"agents,omitempty"`
	AgentsPending []string        `json:"agents_pending,omitempty"`
	Error         string          `json:"error,omitempty"`
	ErrorCode     string          `json:"error_code,omitempty"`
	Hint          string          `json:"hint,omitempty"`
}

// WaitAgentInfo describes an agent's state when the wait completed.
type WaitAgentInfo struct {
	Pane      string    `json:"pane"`
	State     string    `json:"state"`
	MetAt     time.Time `json:"met_at"`
	AgentType string    `json:"agent_type,omitempty"`
}

// DefaultWaitTimeout is the default maximum wait time.
const DefaultWaitTimeout = 5 * time.Minute

// DefaultWaitPoll is the default polling interval.
const DefaultWaitPoll = 2 * time.Second

// CompleteIdleThreshold is the time without activity to consider "complete".
const CompleteIdleThreshold = 5 * time.Second

func newWaitCmd() *cobra.Command {
	var (
		condition   string
		timeout     time.Duration
		poll        time.Duration
		paneIndex   int
		agentType   string
		waitForAny  bool
		exitOnError bool
		countN      int
	)

	cmd := &cobra.Command{
		Use:   "wait [session]",
		Short: "Wait until agents reach a desired state",
		Long: `Wait until agents in a session reach a desired state.

Essential for scripting and pipeline automation.

Wait Conditions:
  idle       All (or any with --any) agents in WAITING state
  complete   All agents idle AND no recent activity (5s threshold)
  generating At least one agent actively generating
  healthy    All agents in healthy state (not ERROR/STALLED)

Composed Conditions (advanced):
  idle,healthy  Agents must be WAITING AND healthy (comma-separated)

Exit Codes:
  0  Condition met successfully
  1  Timeout exceeded
  2  Error (invalid args, session not found)
  3  Agent error detected (with --exit-on-error)

Examples:
  ntm wait myproject --until=idle
  ntm wait myproject --until=idle --timeout=2m
  ntm wait myproject --until=generating --any
  ntm wait myproject --until=idle --type=claude
  ntm wait myproject --until=idle --pane=2
  ntm wait myproject --until=healthy --exit-on-error`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var session string
			if len(args) > 0 {
				session = args[0]
			}

			opts := WaitOptions{
				Session:      session,
				Condition:    WaitCondition(condition),
				Timeout:      timeout,
				PollInterval: poll,
				PaneIndex:    paneIndex,
				AgentType:    agentType,
				WaitForAny:   waitForAny,
				ExitOnError:  exitOnError,
				CountN:       countN,
			}

			return runWait(cmd.OutOrStdout(), opts)
		},
	}

	cmd.Flags().StringVar(&condition, "until", "idle", "Wait condition: idle, complete, generating, healthy")
	cmd.Flags().DurationVar(&timeout, "timeout", DefaultWaitTimeout, "Maximum wait time")
	cmd.Flags().DurationVar(&poll, "poll", DefaultWaitPoll, "Polling interval")
	cmd.Flags().IntVar(&paneIndex, "pane", -1, "Wait for specific pane only (-1 = all)")
	cmd.Flags().StringVar(&agentType, "type", "", "Wait for specific agent type (claude, codex, gemini)")
	cmd.Flags().BoolVar(&waitForAny, "any", false, "Wait for ANY agent (vs ALL)")
	cmd.Flags().BoolVar(&exitOnError, "exit-on-error", false, "Exit immediately if ERROR state detected")
	cmd.Flags().IntVar(&countN, "count", 1, "With --any, wait for at least N agents matching condition")

	return cmd
}

func runWait(w io.Writer, opts WaitOptions) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	t := theme.Current()

	// Resolve session
	res, err := ResolveSession(opts.Session, w)
	if err != nil {
		return err
	}
	if res.Session == "" {
		return nil
	}
	res.ExplainIfInferred(os.Stderr)
	opts.Session = res.Session

	if !tmux.SessionExists(opts.Session) {
		return fmt.Errorf("session '%s' not found", opts.Session)
	}

	// Validate condition
	if !isValidCondition(opts.Condition) {
		return fmt.Errorf("invalid condition '%s': must be one of idle, complete, generating, healthy", opts.Condition)
	}

	// Start waiting
	fmt.Fprintf(w, "%s⏳%s Waiting for '%s' until %s (timeout: %v)...\n",
		colorize(t.Info), colorize(t.Text), opts.Session, opts.Condition, opts.Timeout)

	startTime := time.Now()
	deadline := startTime.Add(opts.Timeout)

	// Create activity monitor
	monitor := robot.NewActivityMonitor(nil)

	for {
		// Check timeout
		if time.Now().After(deadline) {
			fmt.Fprintf(w, "%s✗%s Timeout after %v\n",
				colorize(t.Error), colorize(t.Text), opts.Timeout)
			return &WaitTimeoutError{Duration: opts.Timeout}
		}

		// Get all panes
		panes, err := tmux.GetPanes(opts.Session)
		if err != nil {
			return fmt.Errorf("failed to list panes: %w", err)
		}

		// Filter panes based on options
		filteredPanes := filterPanesForWait(panes, opts)

		if len(filteredPanes) == 0 {
			fmt.Fprintf(w, "%s!%s No matching panes found\n",
				colorize(t.Warning), colorize(t.Text))
			return fmt.Errorf("no panes match the filter criteria")
		}

		// Update activity state for each pane
		var activities []*robot.AgentActivity
		for _, pane := range filteredPanes {
			classifier := monitor.GetOrCreate(pane.ID)
			// Set agent type if we can detect it from pane name
			if at := detectAgentType(pane.Title); at != "" {
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
				if a.State == robot.StateError {
					fmt.Fprintf(w, "%s✗%s Agent error detected in %s\n",
						colorize(t.Error), colorize(t.Text), a.PaneID)
					return &WaitErrorStateError{Pane: a.PaneID}
				}
			}
		}

		// Check if condition is met
		met, details := checkConditionMet(activities, opts)
		if met {
			elapsed := time.Since(startTime)
			fmt.Fprintf(w, "%s✓%s Condition '%s' met after %v\n",
				colorize(t.Success), colorize(t.Text), opts.Condition, elapsed.Round(time.Millisecond))
			for _, d := range details {
				fmt.Fprintf(w, "    %s: %s\n", d.Pane, d.State)
			}
			return nil
		}

		// Sleep and poll again
		time.Sleep(opts.PollInterval)
	}
}

// isValidCondition checks if the condition string is valid.
func isValidCondition(c WaitCondition) bool {
	// Handle composed conditions (comma-separated)
	parts := strings.Split(string(c), ",")
	for _, part := range parts {
		p := WaitCondition(strings.TrimSpace(part))
		switch p {
		case ConditionIdle, ConditionComplete, ConditionGenerating, ConditionHealthy:
			// Valid
		default:
			return false
		}
	}
	return len(parts) > 0
}

// filterPanesForWait filters panes based on wait options.
func filterPanesForWait(panes []tmux.Pane, opts WaitOptions) []tmux.Pane {
	var result []tmux.Pane

	for _, pane := range panes {
		// Skip user pane (index 0 typically has no agent type indicator)
		if detectAgentType(pane.Title) == "" && pane.Index == 0 {
			continue
		}

		// Filter by specific pane index
		if opts.PaneIndex >= 0 && pane.Index != opts.PaneIndex {
			continue
		}

		// Filter by agent type
		if opts.AgentType != "" {
			paneType := detectAgentType(pane.Title)
			if !strings.EqualFold(paneType, opts.AgentType) {
				continue
			}
		}

		result = append(result, pane)
	}

	return result
}

// detectAgentType extracts the agent type from a pane title.
// Pane titles follow the pattern: <session>__<type>_<index>
func detectAgentType(title string) string {
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

// checkConditionMet checks if the wait condition is satisfied.
func checkConditionMet(activities []*robot.AgentActivity, opts WaitOptions) (bool, []WaitAgentInfo) {
	if len(activities) == 0 {
		return false, nil
	}

	// Parse composed conditions
	conditions := strings.Split(string(opts.Condition), ",")

	var matchingAgents []WaitAgentInfo
	var pendingAgents []*robot.AgentActivity

	for _, activity := range activities {
		if meetsAllConditions(activity, conditions) {
			matchingAgents = append(matchingAgents, WaitAgentInfo{
				Pane:      activity.PaneID,
				State:     string(activity.State),
				MetAt:     time.Now(),
				AgentType: activity.AgentType,
			})
		} else {
			pendingAgents = append(pendingAgents, activity)
		}
	}

	// Determine if condition is met based on --any vs ALL
	if opts.WaitForAny {
		// With --any, need at least CountN agents matching
		return len(matchingAgents) >= opts.CountN, matchingAgents
	}

	// Default: ALL agents must match (no pending)
	return len(pendingAgents) == 0 && len(matchingAgents) > 0, matchingAgents
}

// meetsAllConditions checks if an activity meets all specified conditions.
func meetsAllConditions(activity *robot.AgentActivity, conditions []string) bool {
	for _, cond := range conditions {
		c := WaitCondition(strings.TrimSpace(cond))
		if !meetsSingleCondition(activity, c) {
			return false
		}
	}
	return true
}

// meetsSingleCondition checks if an activity meets a single condition.
func meetsSingleCondition(activity *robot.AgentActivity, condition WaitCondition) bool {
	switch condition {
	case ConditionIdle:
		return activity.State == robot.StateWaiting

	case ConditionComplete:
		// Must be waiting AND no recent output
		if activity.State != robot.StateWaiting {
			return false
		}
		// Check last output time - must be older than threshold
		if activity.LastOutput.IsZero() {
			return true // No output recorded = complete
		}
		return time.Since(activity.LastOutput) >= CompleteIdleThreshold

	case ConditionGenerating:
		return activity.State == robot.StateGenerating

	case ConditionHealthy:
		// Not ERROR and not STALLED
		return activity.State != robot.StateError && activity.State != robot.StateStalled

	default:
		return false
	}
}

// WaitTimeoutError indicates the wait timed out.
type WaitTimeoutError struct {
	Duration time.Duration
}

func (e *WaitTimeoutError) Error() string {
	return fmt.Sprintf("wait timed out after %v", e.Duration)
}

// ExitCode returns the exit code for this error (1 for timeout).
func (e *WaitTimeoutError) ExitCode() int {
	return 1
}

// WaitErrorStateError indicates an agent entered error state.
type WaitErrorStateError struct {
	Pane string
}

func (e *WaitErrorStateError) Error() string {
	return fmt.Sprintf("agent in pane '%s' entered ERROR state", e.Pane)
}

// ExitCode returns the exit code for this error (3 for error state).
func (e *WaitErrorStateError) ExitCode() int {
	return 3
}
