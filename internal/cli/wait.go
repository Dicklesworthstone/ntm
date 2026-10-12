package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	sessionPkg "github.com/Dicklesworthstone/ntm/internal/session"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// DefaultWaitTimeout is the default maximum wait time.
const DefaultWaitTimeout = 5 * time.Minute

// DefaultWaitPoll is the default polling interval.
const DefaultWaitPoll = 2 * time.Second

func newWaitCmd() *cobra.Command {
	var opts robot.WaitOptions
	var paneSelector string

	cmd := &cobra.Command{
		Use:   "wait [session]",
		Short: "Wait until agents reach a desired state",
		Long: `Wait until agents in a session reach a desired state.

All matching agents must satisfy the condition unless --any is supplied.
A pane whose state cannot be observed stays pending.

Wait Conditions:
  idle               Agent is waiting for input
  complete           Agent is idle with no recent activity (5s threshold)
  generating         Agent is actively generating
  healthy            Agent is not in ERROR or STALLED state
  agent_ready        Agent CLI is ready after startup or relaunch
  stalled            Agent has stalled
  rate_limited       Agent has reached a rate limit
  rate_limit_lifted  Agent is no longer rate limited

Conditions can be composed with commas: idle,healthy requires both.

Pane selectors:
  N    Pane index in a single-window session; window index in a multi-window session
  W.P  Exact window and pane index
  %N   Stable tmux pane ID
  -1   All agent panes (default)

Exit Codes:
  0  Condition met successfully
  1  Timeout, cancellation, invalid input, or agent error

Use --json and inspect error_code to distinguish TIMEOUT, CANCELED,
INVALID_FLAG, SESSION_NOT_FOUND, PANE_NOT_FOUND, and AGENT_ERROR.

Examples:
  ntm wait myproject --until=idle
  ntm wait myproject --until=idle --timeout=2m
  ntm wait myproject --until=generating --any
  ntm wait myproject --until=idle --any --count=2
  ntm wait myproject --until=idle --type=claude
  ntm wait myproject --until=idle --pane=2
  ntm wait myproject --until=agent_ready --pane=1.2
  ntm wait myproject --until=healthy --pane=%12 --exit-on-error
  ntm wait myproject --until=idle --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Session = ""
			if len(args) > 0 {
				opts.Session = args[0]
			}
			opts.PaneSelectors = nil
			if selector := strings.TrimSpace(paneSelector); selector != "-1" {
				opts.PaneSelectors = []string{selector}
			}
			return runWait(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), opts)
		},
		ValidArgsFunction: completeSessionArgs,
	}

	cmd.Flags().StringVar(&opts.Condition, "until", robot.WaitConditionIdle, "Wait condition, or comma-separated conditions")
	cmd.Flags().DurationVar(&opts.Timeout, "timeout", DefaultWaitTimeout, "Maximum wait time")
	cmd.Flags().DurationVar(&opts.PollInterval, "poll", DefaultWaitPoll, "Polling interval (must be positive)")
	cmd.Flags().StringVar(&paneSelector, "pane", "-1", "Pane selector: N, W.P, or %N (-1 = all)")
	cmd.Flags().StringVar(&opts.AgentType, "type", "", "Wait for a specific agent type (claude, codex, gemini, agy, etc.)")
	cmd.Flags().BoolVar(&opts.WaitForAny, "any", false, "Wait for ANY agent (vs ALL)")
	cmd.Flags().BoolVar(&opts.ExitOnError, "exit-on-error", false, "Exit immediately if ERROR state is detected")
	cmd.Flags().IntVar(&opts.CountN, "count", 1, "With --any, wait for at least N agents matching the condition")

	return cmd
}

// runWait is the human/JSON transport for the canonical wait engine. It does
// not classify panes or maintain another polling loop: CLI, robot, and HTTP
// callers must agree about unreadable targets, selectors, and cancellation.
func runWait(ctx context.Context, w, diagnostics io.Writer, opts robot.WaitOptions) error {
	fail := func(err error, code, hint string) error {
		return outputWaitResponse(w, &robot.WaitResponse{
			RobotResponse: robot.NewErrorResponse(err, code, hint),
			Session:       opts.Session,
			Condition:     opts.Condition,
			Agents:        []robot.WaitAgentInfo{},
		}, err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err, "CANCELED", "Start a new wait when ready")
	}
	if opts.Timeout < 0 || opts.PollInterval <= 0 || opts.CountN <= 0 {
		return fail(errors.New("timeout must be non-negative; poll and count must be positive"),
			robot.ErrCodeInvalidFlag, "Check --timeout, --poll, and --count")
	}
	if opts.AgentType != "" {
		rawAgentType := opts.AgentType
		opts.AgentType = robot.ResolveAgentType(rawAgentType)
		if opts.AgentType == "" || opts.AgentType == "unknown" || opts.AgentType == "user" {
			return fail(fmt.Errorf("invalid agent type %q", strings.TrimSpace(rawAgentType)),
				robot.ErrCodeInvalidFlag, "Use a supported agent type such as claude, codex, gemini, or agy")
		}
	}
	if opts.Session != "" {
		if err := tmux.ValidateSessionName(opts.Session); err != nil {
			return fail(err, robot.ErrCodeInvalidArgs, "Pass a valid tmux session name")
		}
	}
	if err := tmux.EnsureInstalled(); err != nil {
		return fail(err, robot.ErrCodeDependencyMissing, "Install tmux before waiting for agents")
	}

	res, err := ResolveSessionWithOptionsContext(ctx, opts.Session, w, SessionResolveOptions{TreatAsJSON: IsJSONOutput()})
	if err != nil {
		if ctx.Err() != nil {
			return fail(ctx.Err(), "CANCELED", "Start a new wait when ready")
		}
		code, hint := classifyRobotExecuteError(err)
		var resolutionErr *sessionPkg.ResolveExplicitSessionNameError
		if errors.As(err, &resolutionErr) {
			code, hint = robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions"
			if resolutionErr.Kind == sessionPkg.ResolveExplicitSessionNameErrorAmbiguous {
				code, hint = robot.ErrCodeInvalidArgs, "Pass an exact session name"
			}
		}
		return fail(err, code, hint)
	}
	if res.Session == "" {
		return nil // The interactive session chooser was canceled.
	}
	res.ExplainIfInferredForOutput(diagnostics, IsJSONOutput())
	opts.Session = res.Session

	if !IsJSONOutput() {
		t := theme.Current()
		fmt.Fprintf(w, "%s⏳%s Waiting for '%s' until %s (timeout: %v)...\n",
			colorize(t.Info), colorize(t.Text), opts.Session, opts.Condition, opts.Timeout)
	}
	response, _ := robot.GetWaitContext(ctx, opts)
	var cause error
	if response.ErrorCode == "CANCELED" {
		cause = ctx.Err()
	}
	return outputWaitResponse(w, response, cause)
}

func outputWaitResponse(w io.Writer, response *robot.WaitResponse, cause error) error {
	if cause == nil && !response.Success {
		cause = errors.New(response.Error)
	}
	if IsJSONOutput() {
		response.OutputFormat = string(robot.FormatJSON)
		robot.EnsureArraysNeverNull(response)
		if err := json.NewEncoder(w).Encode(response); err != nil {
			return robot.ExitResultForCode(1, err, false)
		}
		// Execute recognizes the typed result and does not replace this
		// already-written response with a generic INTERNAL_ERROR envelope.
		return robot.ExitResultForResponse(response.RobotResponse, cause, true)
	}

	t := theme.Current()
	if response.Success {
		elapsed := time.Duration(response.WaitedSeconds * float64(time.Second))
		fmt.Fprintf(w, "%s✓%s Condition '%s' met after %v\n",
			colorize(t.Success), colorize(t.Text), response.Condition, elapsed.Round(time.Millisecond))
		for _, agent := range response.Agents {
			fmt.Fprintf(w, "    %s: %s\n", agent.Pane, agent.State)
		}
	} else {
		fmt.Fprintf(w, "%s✗%s %s\n", colorize(t.Error), colorize(t.Text), response.Error)
		if len(response.AgentsPending) > 0 {
			fmt.Fprintf(w, "    Pending: %s\n", strings.Join(response.AgentsPending, ", "))
		}
	}
	return robot.ExitResultForResponse(response.RobotResponse, cause, false)
}
