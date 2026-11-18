package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// ReviewSuggestion represents a single review prompt suggestion for an idle agent.
type ReviewSuggestion = robot.ReviewSuggestion

// IdleAgent represents an agent detected as idle.
type IdleAgent = robot.IdleAgent

var (
	detectIdleAgents             = robot.DetectIdleAgents
	assignSuggestionsToAgents    = robot.AssignSuggestionsToAgents
	agentLabel                   = robot.AgentLabel
	matchesReviewQueueFilter     = robot.MatchesReviewQueueFilter
	minInt                       = robot.MinInt
	validateReviewPromptDelivery = robot.ValidateReviewPromptDelivery
	sendReviewPrompts            = robot.SendReviewPrompts
	sendReviewPromptsWithSender  = robot.SendReviewPromptsWithSender
)

type gitCommitInfo struct {
	hash    string
	subject string
}

func getRecentGitCommits(limit int) []gitCommitInfo {
	raw := robot.GetRecentGitCommits("", limit)
	if len(raw) == 0 {
		return nil
	}
	out := make([]gitCommitInfo, len(raw))
	for i, c := range raw {
		out[i] = gitCommitInfo{hash: c.Hash, subject: c.Subject}
	}
	return out
}

// ReviewQueueResponse is the JSON response for the review-queue command.
type ReviewQueueResponse struct {
	output.TimestampedResponse
	Session     string             `json:"session"`
	IdleAgents  []IdleAgent        `json:"idle_agents"`
	Suggestions []ReviewSuggestion `json:"suggestions"`
	Sent        int                `json:"sent,omitempty"`
	Skipped     int                `json:"skipped,omitempty"`
}

func newReviewQueueCmd() *cobra.Command {
	var (
		filter        string
		idleThreshold time.Duration
		send          bool
		formatOut     string
		commitLimit   int
	)

	cmd := &cobra.Command{
		Use:   "review-queue [session]",
		Short: "List idle agents and suggest review prompts",
		Long: `List idle agents and generate review prompt suggestions.

The review-queue command identifies idle agents in a session and suggests
review tasks based on completed work by other agents and recent git commits.

Idle Detection:
  An agent is idle when it has no active bead assignments (status "assigned"
  or "working"). The --idle-threshold flag sets the minimum idle time.

Review Sources:
  - Completed work by other agents (bead assignments marked completed)
  - Recent git commits in the project directory

Examples:
  ntm review-queue myproject                   # Show review suggestions
  ntm review-queue myproject --send            # Send after confirmation
  ntm review-queue myproject --filter cc       # Only Claude agents
  ntm review-queue myproject --idle-threshold 5m
  ntm review-queue myproject --format json     # Robot mode JSON output
  ntm review-queue myproject --commits 10      # Include last 10 commits`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			machineJSON := IsJSONOutput() || strings.EqualFold(strings.TrimSpace(formatOut), "json")
			if machineJSON && send {
				return fmt.Errorf("invalid argument: --send cannot be combined with JSON output")
			}
			session := ""
			if len(args) > 0 {
				session = args[0]
			}
			res, err := ResolveSessionWithOptions(session, cmd.OutOrStdout(), SessionResolveOptions{
				TreatAsJSON: machineJSON,
			})
			if err != nil {
				return err
			}
			if res.Session == "" {
				return nil
			}
			res.ExplainIfInferredForOutput(cmd.ErrOrStderr(), machineJSON)
			session = res.Session

			return runReviewQueue(session, filter, idleThreshold, send, formatOut, commitLimit)
		},
	}

	cmd.Flags().StringVar(&filter, "filter", "", "Filter by agent type alias (claude|cc, codex|cod, gemini|gmi, antigravity|agy, grok, omp, oc, cursor, windsurf|ws, aider, ollama)")
	cmd.Flags().DurationVar(&idleThreshold, "idle-threshold", 2*time.Minute, "Minimum idle time to consider an agent idle")
	cmd.Flags().BoolVar(&send, "send", false, "Prompt for confirmation then send review prompts")
	cmd.Flags().StringVar(&formatOut, "format", "", "Output format: json for robot mode")
	cmd.Flags().IntVar(&commitLimit, "commits", 5, "Number of recent git commits to consider")

	return cmd
}

func runReviewQueue(session, filter string, idleThreshold time.Duration, send bool, formatOut string, commitLimit int) error {
	// Honor both the command-local `--format json` and the global `--json`
	// flag so stdout stays strictly parseable JSON.
	isJSON := strings.EqualFold(strings.TrimSpace(formatOut), "json") || IsJSONOutput()
	if isJSON && send {
		return fmt.Errorf("invalid argument: --send cannot be combined with JSON output")
	}

	// In JSON mode, suppress slog telemetry for the duration of this call so
	// consumers that capture `2>&1` get a clean parseable stream.
	if isJSON {
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
		defer slog.SetDefault(prev)
	}

	normalizedFilter, err := robot.NormalizeReviewQueueFilter(filter)
	if err != nil {
		if isJSON {
			return outputReviewQueueError(session, err.Error())
		}
		return err
	}

	slog.Info("[E2E-REVIEWQ] start", "session", session, "filter", filter, "idle_threshold", idleThreshold)

	store, err := assignment.LoadStore(session)
	if err != nil {
		if isJSON {
			return outputReviewQueueError(session, fmt.Sprintf("failed to load assignments: %v", err))
		}
		return fmt.Errorf("failed to load assignment store: %w", err)
	}

	panes, err := tmux.GetPanes(session)
	if err != nil {
		if isJSON {
			return outputReviewQueueError(session, fmt.Sprintf("failed to list panes: %v", err))
		}
		return fmt.Errorf("failed to list panes: %w", err)
	}

	if len(panes) == 0 {
		if isJSON {
			return outputReviewQueueError(session, "no panes found in session")
		}
		return fmt.Errorf("no panes found in session %s", session)
	}

	idleAgents := detectIdleAgents(store, panes, normalizedFilter, idleThreshold)

	projectDir := strings.TrimSpace(resolveProjectDirForSession(session, false))
	var suggestions []ReviewSuggestion
	if projectDir != "" {
		suggestions = robot.GenerateReviewSuggestions(store, idleAgents, projectDir, commitLimit)
	} else {
		suggestions = generateReviewSuggestions(store, idleAgents, commitLimit)
	}
	if idleAgents == nil {
		idleAgents = []IdleAgent{}
	}
	if suggestions == nil {
		suggestions = []ReviewSuggestion{}
	}

	slog.Info("[E2E-REVIEWQ] analysis",
		"session", session,
		"idle_count", len(idleAgents),
		"review_count", len(suggestions))

	resp := ReviewQueueResponse{
		TimestampedResponse: output.NewTimestamped(),
		Session:             session,
		IdleAgents:          idleAgents,
		Suggestions:         suggestions,
	}

	if isJSON {
		return outputReviewQueueJSON(resp)
	}

	printReviewQueueReport(resp)

	if send && len(suggestions) > 0 {
		if err := validateReviewPromptDelivery(suggestions); err != nil {
			return err
		}
		fmt.Print("\nSend these prompts? (y/n): ")
		var confirm string
		fmt.Scanln(&confirm)
		if strings.ToLower(confirm) == "y" {
			sent, skipped, err := sendReviewPrompts(session, suggestions)
			if err != nil {
				return err
			}
			resp.Sent = sent
			resp.Skipped = skipped
			th := theme.Current()
			successStyle := lipgloss.NewStyle().Foreground(th.Success)
			fmt.Println(successStyle.Render(fmt.Sprintf("✓ Sent %d review prompts (%d skipped)", sent, skipped)))
			slog.Info("[E2E-REVIEWQ] sent", "session", session, "sent", sent, "skipped", skipped)
		} else {
			fmt.Println("Cancelled.")
		}
	}

	return nil
}

func generateReviewSuggestions(store *assignment.AssignmentStore, idleAgents []IdleAgent, commitLimit int) []ReviewSuggestion {
	return robot.GenerateReviewSuggestions(store, idleAgents, "", commitLimit)
}

func printReviewQueueReport(resp ReviewQueueResponse) {
	th := theme.Current()
	titleStyle := lipgloss.NewStyle().Foreground(th.Blue).Bold(true)

	fmt.Printf("\n%s Review Queue for '%s'\n\n", titleStyle.Render("📋"), resp.Session)

	fmt.Printf("Idle Agents: %d\n", len(resp.IdleAgents))
	if len(resp.IdleAgents) > 0 {
		for _, a := range resp.IdleAgents {
			idleInfo := ""
			if a.IdleDuration != "" {
				idleInfo = fmt.Sprintf(" idle for %s,", a.IdleDuration)
			}
			lastInfo := ""
			if a.LastTask != "" {
				lastInfo = fmt.Sprintf(" last task: %q", a.LastTask)
			}
			fmt.Printf("  %s (pane %d) -%s%s\n", a.AgentType, a.Pane, idleInfo, lastInfo)
		}
	}

	fmt.Printf("Pending Reviews: %d\n", len(resp.Suggestions))

	if len(resp.Suggestions) > 0 {
		fmt.Printf("\n%s Suggested Assignments:\n\n", titleStyle.Render("🔄"))
		for i, s := range resp.Suggestions {
			sourceLabel := ""
			switch s.Source {
			case "agent_work":
				sourceLabel = "completed work"
			case "git_commit":
				sourceLabel = "git commit"
			default:
				sourceLabel = s.Source
			}
			fmt.Printf("  %d. %s <- %s (%s)\n", i+1, s.Agent, s.Prompt, sourceLabel)
		}
	} else if len(resp.IdleAgents) == 0 {
		fmt.Println("\nNo idle agents found.")
	} else {
		fmt.Println("\nNo review sources available.")
	}
}

func outputReviewQueueError(session, errMsg string) error {
	resp := struct {
		output.TimestampedResponse
		Success bool   `json:"success"`
		Session string `json:"session"`
		Error   string `json:"error"`
	}{
		TimestampedResponse: output.NewTimestamped(),
		Success:             false,
		Session:             session,
		Error:               errMsg,
	}
	data, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return errJSONFailure
}

func outputReviewQueueJSON(v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}
