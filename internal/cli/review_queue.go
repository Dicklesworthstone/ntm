package cli

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// ReviewSuggestion represents a single review prompt suggestion for an idle agent
type ReviewSuggestion struct {
	Agent     string `json:"agent"`
	Pane      int    `json:"pane"`
	AgentType string `json:"agent_type"`
	Prompt    string `json:"prompt"`
	Source    string `json:"source"` // "agent_work", "git_commit", or "idle_available"
	SourceRef string `json:"source_ref,omitempty"`
}

// IdleAgent represents an agent detected as idle
type IdleAgent struct {
	Pane         int    `json:"pane"`
	AgentType    string `json:"agent_type"`
	AgentName    string `json:"agent_name,omitempty"`
	IdleDuration string `json:"idle_duration,omitempty"`
	LastTask     string `json:"last_task,omitempty"`
}

// ReviewQueueResponse is the JSON response for the review-queue command
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
			session := ""
			if len(args) > 0 {
				session = args[0]
			} else {
				sessions, err := tmux.ListSessions()
				if err != nil || len(sessions) == 0 {
					return fmt.Errorf("no session specified and no active sessions found")
				}
				session = sessions[0].Name
			}

			return runReviewQueue(session, filter, idleThreshold, send, formatOut, commitLimit)
		},
	}

	cmd.Flags().StringVar(&filter, "filter", "", "Filter by agent type (cc, cod, gmi)")
	cmd.Flags().DurationVar(&idleThreshold, "idle-threshold", 2*time.Minute, "Minimum idle time to consider an agent idle")
	cmd.Flags().BoolVar(&send, "send", false, "Prompt for confirmation then send review prompts")
	cmd.Flags().StringVar(&formatOut, "format", "", "Output format: json for robot mode")
	cmd.Flags().IntVar(&commitLimit, "commits", 5, "Number of recent git commits to consider")

	return cmd
}

func runReviewQueue(session, filter string, idleThreshold time.Duration, send bool, formatOut string, commitLimit int) error {
	isJSON := formatOut == "json"

	slog.Info("[E2E-REVIEWQ] start", "session", session, "filter", filter, "idle_threshold", idleThreshold)

	// Load assignment store
	store, err := assignment.LoadStore(session)
	if err != nil {
		if isJSON {
			return outputReviewQueueError(session, fmt.Sprintf("failed to load assignments: %v", err))
		}
		return fmt.Errorf("failed to load assignment store: %w", err)
	}

	// Get pane information
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

	// Detect idle agents
	idleAgents := detectIdleAgents(store, panes, filter, idleThreshold)

	// Generate review suggestions
	suggestions := generateReviewSuggestions(store, idleAgents, commitLimit)

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

	// Human-readable output
	printReviewQueueReport(resp)

	// If --send, prompt for confirmation
	if send && len(suggestions) > 0 {
		fmt.Print("\nSend these prompts? (y/n): ")
		var confirm string
		fmt.Scanln(&confirm)
		if strings.ToLower(confirm) == "y" {
			sent, skipped := sendReviewPrompts(session, suggestions)
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

// detectIdleAgents finds agents with no active assignments.
func detectIdleAgents(store *assignment.AssignmentStore, panes []tmux.Pane, filter string, idleThreshold time.Duration) []IdleAgent {
	active := store.ListActive()

	// Build set of busy panes
	busyPanes := make(map[int]bool)
	for _, a := range active {
		busyPanes[a.Pane] = true
	}

	// Find most recent completed/failed assignment per pane for "last task"
	allAssignments := store.GetAll()
	lastTaskByPane := make(map[int]string)
	lastTimeByPane := make(map[int]time.Time)
	for _, a := range allAssignments {
		if a.Status != assignment.StatusCompleted && a.Status != assignment.StatusFailed {
			continue
		}
		var ts time.Time
		if a.CompletedAt != nil {
			ts = *a.CompletedAt
		} else if a.FailedAt != nil {
			ts = *a.FailedAt
		}
		if ts.After(lastTimeByPane[a.Pane]) {
			lastTimeByPane[a.Pane] = ts
			lastTaskByPane[a.Pane] = a.BeadTitle
		}
	}

	var idle []IdleAgent
	for _, pane := range panes {
		// Skip pane 0 (user pane)
		if pane.Index == 0 {
			continue
		}

		if busyPanes[pane.Index] {
			continue
		}

		agentType := detectAgentTypeFromTitle(pane.Title)

		// Apply filter
		if filter != "" && !matchesReviewQueueFilter(agentType, filter) {
			continue
		}

		// Check idle threshold
		idleDuration := ""
		if lastTime, ok := lastTimeByPane[pane.Index]; ok {
			dur := time.Since(lastTime)
			if dur < idleThreshold {
				continue
			}
			idleDuration = formatIdleDuration(dur)
		}

		idle = append(idle, IdleAgent{
			Pane:         pane.Index,
			AgentType:    agentType,
			IdleDuration: idleDuration,
			LastTask:     lastTaskByPane[pane.Index],
		})
	}

	// Sort by pane index for deterministic output
	sort.Slice(idle, func(i, j int) bool {
		return idle[i].Pane < idle[j].Pane
	})

	return idle
}

// generateReviewSuggestions creates review prompts from completed work and git commits.
func generateReviewSuggestions(store *assignment.AssignmentStore, idleAgents []IdleAgent, commitLimit int) []ReviewSuggestion {
	if len(idleAgents) == 0 {
		return nil
	}

	var suggestions []ReviewSuggestion

	// Source 1: Completed work by other agents
	completed := store.ListByStatus(assignment.StatusCompleted)
	for _, a := range completed {
		suggestions = append(suggestions, ReviewSuggestion{
			Prompt:    fmt.Sprintf("Review the changes for bead %s: %q", a.BeadID, a.BeadTitle),
			Source:    "agent_work",
			SourceRef: a.BeadID,
		})
	}

	// Source 2: Recent git commits
	commits := getRecentGitCommits(commitLimit)
	for _, c := range commits {
		suggestions = append(suggestions, ReviewSuggestion{
			Prompt:    fmt.Sprintf("Review commit %s: %q", c.hash[:minInt(8, len(c.hash))], c.subject),
			Source:    "git_commit",
			SourceRef: c.hash,
		})
	}

	// Assign suggestions round-robin to idle agents
	assigned := assignSuggestionsToAgents(suggestions, idleAgents)

	return assigned
}

// assignSuggestionsToAgents distributes suggestions across idle agents round-robin.
func assignSuggestionsToAgents(suggestions []ReviewSuggestion, idleAgents []IdleAgent) []ReviewSuggestion {
	if len(suggestions) == 0 || len(idleAgents) == 0 {
		return nil
	}

	var result []ReviewSuggestion
	for i, s := range suggestions {
		agent := idleAgents[i%len(idleAgents)]
		s.Agent = agentLabel(agent)
		s.Pane = agent.Pane
		s.AgentType = agent.AgentType
		result = append(result, s)
	}

	return result
}

func agentLabel(a IdleAgent) string {
	if a.AgentName != "" {
		return a.AgentName
	}
	return fmt.Sprintf("%s_%d", a.AgentType, a.Pane)
}

type gitCommitInfo struct {
	hash    string
	subject string
}

func getRecentGitCommits(limit int) []gitCommitInfo {
	if limit <= 0 {
		return nil
	}
	cmd := exec.Command("git", "log", fmt.Sprintf("-%d", limit), "--pretty=format:%H|%s")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var commits []gitCommitInfo
	for _, line := range lines {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 || parts[0] == "" {
			continue
		}
		commits = append(commits, gitCommitInfo{hash: parts[0], subject: parts[1]})
	}
	return commits
}

func matchesReviewQueueFilter(agentType, filter string) bool {
	return matchesRebalanceFilter(agentType, filter)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func sendReviewPrompts(session string, suggestions []ReviewSuggestion) (sent, skipped int) {
	for _, s := range suggestions {
		target := fmt.Sprintf("%s:%d", session, s.Pane)
		slog.Info("[E2E-REVIEWQ] send",
			"session", session,
			"agent", s.Agent,
			"pane", s.Pane,
			"source", s.Source)
		if err := tmux.SendKeys(target, s.Prompt, true); err != nil {
			slog.Warn("[E2E-REVIEWQ] send_failed",
				"session", session,
				"pane", s.Pane,
				"error", err)
			skipped++
		} else {
			sent++
		}
	}
	return
}

func printReviewQueueReport(resp ReviewQueueResponse) {
	th := theme.Current()
	titleStyle := lipgloss.NewStyle().Foreground(th.Blue).Bold(true)

	fmt.Printf("\n%s Review Queue for '%s'\n\n", titleStyle.Render("📋"), resp.Session)

	// Idle agents section
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

	// Suggestions section
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
	data, _ := json.MarshalIndent(resp, "", "  ")
	fmt.Println(string(data))
	return fmt.Errorf("%s", errMsg)
}

func outputReviewQueueJSON(v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}
